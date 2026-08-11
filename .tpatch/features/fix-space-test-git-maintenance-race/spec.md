# Specification — fix-space-test-git-maintenance-race

## 1. Problem statement

`TestSpaceAbsentRegistryCreatesNothing/checkout` compares a byte-exact recursive tree snapshot of
the checkout repository before and after read-only `tws space list/show/remove` against an absent
registry. In checkout mode that tree includes `.git`, where Git creates and removes lock files on
its own schedule. On macOS CI run `31501550220` the before snapshot contained
`.git/objects/maintenance.lock` and Git background maintenance removed it before the after
snapshot, so the test reported a side effect the product never produced.

The test's purpose is to prove that read-only space commands with no registry create **no tws-owned
artifacts**. Some tws-owned artifacts live inside `.git`: the checkout workspace marker
`.git/tws/workspace-id` (`internal/workspace.go`) and the local-ignore entry `.git/info/exclude`
(`internal/enable.go`, `internal/cli/init.go`). Excluding the whole `.git` subtree would therefore
blind the test to real regressions and is rejected. Only Git's **transient lock files** are
excluded.

**Hard parent**: `workspace-sibling-links`, which owns the checkout-mode `.tws` layout and the
`parent` vs `root` distinction that this test snapshots.

## 2. Acceptance criteria

1. `internal/cli/space_test.go` gains `isTransientGitLockPath(rel string) bool`, returning true iff
   `filepath.ToSlash(rel)` has a `/`-separated segment equal to `.git` **and** a base name ending in
   `.lock`. It matches `.git/objects/maintenance.lock`, `.git/index.lock`,
   `.git/refs/heads/main.lock`, and `worktrees/x/.git/foo.lock`. It matches nothing else — notably
   not `.git`, `.git/objects`, `.git/tws/workspace-id`, `.git/info/exclude`, `.github`,
   `.gitignore`, `.tws/config.yaml`, `spaces.yaml`, or a `.lock` file outside `.git` such as
   `.spaces.lock`.
2. `internal/cli/space_test.go` gains a dedicated stable collector rather than a post-filter over a
   finished snapshot:
   - `type treeWalker func(root string, fn filepath.WalkFunc) error` — a seam matching
     `filepath.Walk` so the collector can be driven deterministically from tests.
   - `collectStableTreePaths(dir string, walk treeWalker) ([]string, error)` — invokes `walk`, and
     for each callback computes `rel` via `filepath.Rel`. When the callback error is non-nil it is
     tolerated (returning `nil`, contributing no path) **iff** `isTransientGitLockPath(rel)` **and**
     (`errors.Is(err, fs.ErrNotExist)` or `os.IsNotExist(err)`); every other callback error is
     returned and aborts the walk. When the callback error is nil, transient lock paths are skipped
     during traversal and every other path is appended in walk order. On error it returns
     `nil, err`.
   - `snapshotTreeIgnoringGitLocks(t *testing.T, dir string) string` — calls
     `collectStableTreePaths(dir, filepath.Walk)`, `t.Fatal`s on error, and joins the paths with
     `"\n"`.

   Filtering **during** traversal is required: a lock file that Git removes between the directory
   read and the `Lstat` of its entry surfaces as a walk callback error, not as a listed path, so a
   wrapper that calls `snapshotTree` first can still fail. The names and comments refer to **lock
   transience**, not to `.git` metadata ownership, and state that all other `.git` paths — including
   the tws-owned ones — are retained and that every non-lock/non-not-exist walk error stays fatal.
3. The generic `snapshotTree` is **byte-identical to HEAD**, and so is
   `snapshotTreeIgnoringLock` (`internal/cli/space_guard_test.go:78`, which concerns the tws
   `.spaces.lock` registry lock) and all of its call sites in `space_guard_test.go` and
   `status_test.go`.
4. `TestSpaceAbsentRegistryCreatesNothing` uses `snapshotTreeIgnoringGitLocks(t, parent)` for both
   the before and the after snapshot, in both the `external` and `checkout` subtests. No other line
   of that test changes.
5. That test keeps its explicit `os.Lstat` assertions that `spaces.yaml`, `.spaces.lock`, and
   `.tws-workspace` under `root` are not created, and keeps asserting `list --json` == `"[]\n"`,
   `show learning` exiting nonzero, and `remove learning` failing with exactly
   `no space named "learning"`.
6. A new test `TestSpaceSnapshotIgnoresTransientGitLocks` builds a temp dir containing
   `.git/objects/`, `.git/info/`, `.github/workflows/ci.yml`, `.gitignore`, and `.tws/config.yaml`,
   and asserts, in order:
   a. the stable snapshot retains `.git`, `.git/objects`, `.git/info`, `.github/workflows/ci.yml`,
      `.gitignore`, and `.tws/config.yaml` as exact lines;
   b. after creating `.git/objects/maintenance.lock`, the **raw** `snapshotTree` does contain
      `maintenance.lock` (otherwise the test would prove nothing) while the stable snapshot is
      unchanged;
   c. after removing that lock, the stable snapshot is still unchanged;
   d. creating `.git/tws/workspace-id` **does** change the stable snapshot and the marker path is
      listed;
   e. creating `.git/info/exclude` **does** change the stable snapshot and the path is listed;
      rewriting its content does **not** change it (asserted explicitly, because `snapshotTree` is
      path-only), and renaming it **does** change it, with the old path gone and the new path
      present;
   f. after writing `spaces.yaml` in the same dir, the stable snapshot **does** change and lists it.
   A local `snapshotHasPath(snapshot, rel string) bool` helper performs exact whole-line matching so
   these assertions cannot pass on a substring coincidence.
7. A new test `TestCollectStableTreePathsWalkErrors` covers the mid-traversal race deterministically,
   with no dependence on Git maintenance timing, by passing a scripted `treeWalker` to
   `collectStableTreePaths`. The script replays `filepath.Walk` semantics (callback receives the
   joined path; a non-nil callback return aborts the walk) over a fixed entry list, using a minimal
   `scriptedFileInfo` for successful entries. Table cases:
   a. a vanished `.git/objects/maintenance.lock` (`&fs.PathError{Err: fs.ErrNotExist}`) → no error,
      and the surrounding paths — including `.git/tws/workspace-id` — are collected in walk order;
   b. a vanished `.git/index.lock` reported as `&fs.PathError{Err: syscall.ENOENT}` → also tolerated
      (covers the `os.IsNotExist` branch as reported by real syscalls);
   c. a lock listed without error → filtered, no error;
   d. a vanished **non-lock** path under `.git` (`.git/tws/workspace-id`) → error matching
      `fs.ErrNotExist`;
   e. a vanished `.lock` path **outside** `.git` (`.spaces.lock`) → error matching `fs.ErrNotExist`;
   f. a permission error on a lock (`fs.ErrPermission`) → error matching `fs.ErrPermission`.
   Every error case additionally asserts that the collector returns no paths.
8. No product code changes: `git diff --stat` lists `internal/cli/space_test.go` as the only
   non-`.tpatch` file; `go.mod` / `go.sum` unchanged; the only added imports are standard-library
   (`errors`, `io/fs`, `path`, `syscall`, `time`), used for `errors.Is`, the `fs` sentinel errors and
   `fs.PathError`, `path.Base` on slash-normalized relative paths, `syscall.ENOENT`, and the
   `time.Time` returned by `scriptedFileInfo.ModTime`.
9. Runnable gates, all with a hermetic Git environment (the local shell injects
   `GIT_CONFIG_KEY_0=safe.bareRepository=explicit`, which breaks the pre-existing `setupGitRepo`
   fixture independently of this change):

   ```bash
   HERMETIC="env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 \
                 -u GIT_CONFIG_KEY_1 -u GIT_CONFIG_VALUE_1"
   gofmt -l internal                       # silent
   $HERMETIC go test ./internal/cli/ -run 'TestSpace|TestCollectStableTreePaths' -count=1
   $HERMETIC go test ./... -count=1
   go vet ./... && golangci-lint run ./... && make build
   git diff --check && git --no-pager diff --stat
   ```
10. `TestSpaceAbsentRegistryCreatesNothing/checkout` passes while Git maintenance is racing: running
    the focused selector repeatedly (`-count=5`) must never reproduce a lock-only diff or a walk
    error from a vanished lock.

## 3. Out of scope

- Any product change. `tws space list/show/remove` semantics, the absent-registry error text, and
  the checkout/external anchor rules stay exactly as they are.
- Widening `snapshotTree` or `snapshotTreeIgnoringLock`, or changing any test other than
  `TestSpaceAbsentRegistryCreatesNothing` and the new regression test.
- Excluding the whole `.git` subtree — rejected: it would hide the tws-owned `.git/tws/workspace-id`
  and `.git/info/exclude`.
- Filtering by individual filename (`maintenance.lock` only) — rejected as whack-a-mole across the
  same lock class.
- Pre-emptively excluding non-lock Git transients (`packed-refs`, `commit-graph`, pack `.tmp`
  files): no evidence they flaked, and they must keep being compared.
- Disabling Git background maintenance in the fixtures or in CI (`gc.auto=0`, `maintenance.auto`):
  it would not cover pre-existing locks, and it makes the fixture diverge from real repositories.
- Making the stable collector tolerant of any other walk error class (permission, I/O, vanished
  non-lock paths): rejected — those must keep failing the test loudly.
- Retrying or re-walking the tree on a transient failure, or comparing snapshots with a fuzzy diff:
  rejected as timing-dependent; the collector is deterministic by construction and its seam is
  exercised by a scripted walker.
- Making `snapshotTree` content-aware (hashing file bodies) so that `.git/info/exclude` edits are
  detected: a larger change to a shared helper, out of scope here.
- Windows path semantics beyond `filepath.ToSlash` normalization; CI is Linux + macOS.
- Any dependency, tooling, workflow, or CI-matrix change.

## 4. Plan

1. Add `isTransientGitLockPath`, `treeWalker`, `collectStableTreePaths`, and
   `snapshotTreeIgnoringGitLocks` next to `snapshotTree` in `internal/cli/space_test.go`
   (criteria 1–3).
2. Repoint the two snapshot calls inside `TestSpaceAbsentRegistryCreatesNothing` to the wrapper,
   leaving every assertion intact (criteria 4–5).
3. Append `TestSpaceSnapshotIgnoresTransientGitLocks` and `snapshotHasPath` immediately after that
   test (criterion 6).
4. Append `TestCollectStableTreePathsWalkErrors` and `scriptedFileInfo`, driving the collector with
   a scripted walker so the vanished-lock race is covered without timing (criterion 7).
5. Run `gofmt`, the focused `TestSpace|TestCollectStableTreePaths` selector, then the full gates, all
   under the hermetic Git env (criteria 8–10).
6. Keep `workspace-sibling-links` registered as the hard parent and confirm
   `tpatch feature deps --validate-all` is clean before landing.
