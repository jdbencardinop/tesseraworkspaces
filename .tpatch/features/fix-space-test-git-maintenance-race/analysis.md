# Analysis — fix-space-test-git-maintenance-race

## Summary

macOS CI run `31501550220` failed `TestSpaceAbsentRegistryCreatesNothing/checkout`
(`internal/cli/space_test.go`). The reported diff between the before and after trees was a single
line, `.git/objects/maintenance.lock`, present in the *before* snapshot and gone from the *after*
snapshot. The product created nothing: `tws space list/show/remove` against an absent registry are
read-only.

The failure is a **test-environment race, not a product defect**. Git runs background maintenance
(`git maintenance`, auto-gc) asynchronously after the repository operations performed by
`setupGitRepoCheckout`. That maintenance takes `.git/objects/maintenance.lock` and releases it on
its own schedule, so a byte-exact recursive tree comparison that includes Git lock files is
inherently non-deterministic on a machine where maintenance happens to fire between the two walks.

The fix is to remove **only transient Git lock files** from the comparison basis for this one test,
and to prove that exclusion is exactly as narrow as claimed with a dedicated regression test.

The race has **two** observable shapes, and both must be handled:

1. the lock is listed in one snapshot and absent from the other — a spurious diff;
2. the lock vanishes *inside a single walk*, between `filepath.Walk`'s directory read and the
   `Lstat` of that entry — which reaches the walk callback as a non-nil error, not as a listed path,
   and fails the walk outright.

A wrapper that post-filters the output of `snapshotTree` only fixes shape 1. Shape 2 requires the
filtering to happen **during** traversal, so the exclusion is implemented as a dedicated stable
collector rather than a line filter over a finished snapshot.

## Root cause

`TestSpaceAbsentRegistryCreatesNothing` compares `snapshotTree(t, parent)` before and after the
read-only commands. `snapshotTree` (`internal/cli/space_test.go:63`) walks *every* path under `dir`
and joins the relative paths. In checkout mode `parent` is the repository itself, so the walk
descends into `.git`:

| Mode | `parent` | Contains `.git`? | Deterministic? |
| --- | --- | --- | --- |
| external | `filepath.Dir(TWS_ROOT)` — a bare temp dir | no | yes |
| checkout | the temporary repo from `setupGitRepoCheckout` | yes | no — Git lock files come and go |

`snapshotTree` also returns the *first* walk error to `t.Fatal`, and `filepath.Walk` reports a failed
`Lstat` by invoking the callback with a non-nil error. A `maintenance.lock` removed between the
`ReadDir` of `.git/objects` and its own `Lstat` therefore fails the walk with
`lstat .../.git/objects/maintenance.lock: no such file or directory`, independently of any snapshot
comparison.

## Design: stable collector, not a post-filter

The exclusion lives in `collectStableTreePaths(dir string, walk treeWalker) ([]string, error)`:

- `treeWalker` matches `filepath.Walk`'s signature, so production use passes `filepath.Walk` and
  tests pass a scripted walker — the seam that makes the race testable without timing.
- A non-nil callback error is tolerated **iff** the relative path satisfies `isTransientGitLockPath`
  **and** the error is a not-exist error (`errors.Is(err, fs.ErrNotExist)` or `os.IsNotExist(err)`,
  the latter covering the raw `syscall.ENOENT` real syscalls return). The entry contributes no path.
- Every other walk error — a vanished non-lock path, a vanished `.lock` outside `.git`, a permission
  or I/O error on anything including a lock — is returned and aborts the walk, so genuine traversal
  failures still fail the test loudly.
- Successful entries matching `isTransientGitLockPath` are skipped; all others are appended in walk
  order.

`snapshotTreeIgnoringGitLocks` is then a thin `t.Fatal`-on-error join over that collector. Generic
`snapshotTree` is untouched and is still used by the regression test to prove the raw walk really
does see the lock.

## Why not exclude the whole `.git` subtree

Excluding all of `.git` was considered and **rejected**: `.git` is not exclusively Git-owned in this
project. tws itself writes inside it:

- `.git/tws/workspace-id` — the persistent checkout workspace marker
  (`internal/workspace.go:81`, `CheckoutMarkerDir` / `GitMarkerDir` at `internal/workspace.go:86-111`,
  `EnsureWorkspaceMarkerID` at `internal/workspace.go:134`).
- `.git/info/exclude` — the local-ignore entry written by enable/init
  (`internal/enable.go:65` `AddGitLocalExclude`, `internal/cli/init.go:114` `addGitExclude`).

A whole-subtree filter would therefore hide exactly the tws-owned artifacts this no-side-effect test
exists to catch: a regression that created a checkout marker or rewrote the local exclude file under
an absent registry would pass silently.

The non-deterministic class that actually caused the flake is *lock files*: `maintenance.lock`,
`index.lock`, `packed-refs.lock`, `HEAD.lock`, `refs/**/*.lock`, `shallow.lock`, `config.lock`. Git
creates and deletes them under `.git` on its own schedule and they are never durable state. The
exclusion boundary is therefore "a path with a `.git` segment whose base name ends in `.lock`" —
transience, not ownership. Everything else under `.git` is retained and compared.

## Already upstream?

No. `internal/cli/space_test.go` at HEAD has only the generic `snapshotTree`; there is no
lock-aware wrapper. The neighbouring `snapshotTreeIgnoringLock`
(`internal/cli/space_guard_test.go:78`) excludes the tws `.spaces.lock` registry lock only, is
defined in a different file, and deliberately still compares `.git` — it must not be widened, and
its name refers to a different lock entirely.

## Compatibility

- **No product change.** The changeset is one test file; `internal/` product code, `go.mod`, and
  `go.sum` are untouched.
- **Generic `snapshotTree` is unchanged**, so `snapshotTreeIgnoringLock` and its call sites in
  `space_guard_test.go` / `status_test.go` keep their exact current strictness. Weakening the shared
  helper was rejected for that reason; a dedicated wrapper is used instead.
- **Coverage is not weakened.** The wrapper still compares `.tws`, `spaces.yaml`, `.spaces.lock`,
  `.tws-workspace`, external workspace paths, every repository worktree file, **and every non-lock
  `.git` path**, including `.git/tws/workspace-id` and `.git/info/exclude`. The explicit `os.Lstat`
  assertions for `spaces.yaml`, `.spaces.lock`, and `.tws-workspace` are retained verbatim.
- **Hard parent**: `workspace-sibling-links` — that feature owns the checkout-mode `.tws` layout and
  the `parent`/`root` distinction this test snapshots.

## Risks

| Risk | Assessment |
| --- | --- |
| The exclusion hides a real tws side effect inside `.git` | Only `*.lock` base names under a `.git` segment are dropped; tws writes `.git/tws/workspace-id` and `.git/info/exclude`, neither of which ends in `.lock`, and the regression test asserts both change the snapshot. |
| The exclusion is too broad (drops non-`.git` paths) | The predicate requires a path segment equal to `.git` *and* a `.lock` base name; the regression test asserts `.github/workflows/ci.yml`, `.gitignore`, `.tws/config.yaml`, and `spaces.yaml` are all retained/observable. |
| Non-lock Git transients still flake (`packed-refs`, `commit-graph`, pack `.tmp`) | Not observed in CI and deliberately not pre-empted: they remain compared so a real change is never masked. If one is ever seen, it is a separate, evidence-driven widening. |
| Regression test proves nothing if `snapshotTree` stopped seeing the lock | Guarded: the test asserts the **raw** `snapshotTree` does contain `maintenance.lock` before asserting the stable snapshot is unchanged. |
| `snapshotTree` cannot see content-only mutations of `.git/info/exclude` | True and pre-existing: `snapshotTree` is path-only. The regression test pins what is actually captured — creation and path changes are observable, and the content-rewrite no-op is asserted explicitly so the limit is documented rather than assumed. |
| Silently masking a future flake elsewhere | The wrapper is used by this test only; every other snapshot assertion in the package keeps comparing lock files too. |
| Tolerating walk errors hides a real traversal failure | Tolerance is doubly gated: the path must be a transient `.git` lock *and* the error must be not-exist. `TestCollectStableTreePathsWalkErrors` asserts that a vanished non-lock `.git` path, a vanished `.lock` outside `.git`, and a permission error on a lock each still return an error and no paths. |
| The race is only reproducible under real Git maintenance timing | The collector takes a `treeWalker` seam, so the vanished-lock callback is replayed deterministically from a scripted walk; no sleeps, retries, or `-race`-dependent scheduling. |

## Verdict

Compatible and low risk. One test-only file, no product change, no shared-helper weakening, and the
narrow lock-only exclusion is pinned by two regressions: one over a real temp tree that fails if the
exclusion is either too broad or too narrow, and one over a scripted walk that fails if the
mid-traversal tolerance is either absent or wider than not-exist-on-a-lock.
