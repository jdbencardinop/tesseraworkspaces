# Exploration — fix-space-test-git-maintenance-race

Read from the working tree. Line numbers are locators — anchor on symbol names.

## 1. The only file that changes

`internal/cli/space_test.go`. No product file, no `go.mod`/`go.sum`, no workflow, no other test file.

## 2. Code read but never modified

| Symbol | Location | Why it matters |
| --- | --- | --- |
| `snapshotTree` | `internal/cli/space_test.go:63` | Walks every path under `dir` and joins relative paths — path-only, no content hashing, and `t.Fatal`s on any walk callback error. The raw, unfiltered basis. Kept byte-identical; the new stable collector does **not** compose it (see §4), but the regression test still uses it as the raw control. |
| `snapshotTreeIgnoringLock` | `internal/cli/space_guard_test.go:78` | Drops the tws registry lock `.spaces.lock` only, and still compares `.git`. A different lock and a different concern; untouched so the guard/status call sites keep their strictness. |
| `setupGitRepoCheckout` | `internal/cli/checkout_lifecycle_test.go:19` | Creates the real temp repo whose `.git` Git maintenance mutates; the source of the flake. |
| `withCheckoutEnv` | `internal/cli/space_test.go:32` | Checkout mode: `root = <repo>/.tws`, `parent = repo` ⇒ the snapshot descends into `.git`. |
| `withWorkspaceEnv` | `internal/cli/new_integration_test.go:152` | External mode: `parent = filepath.Dir(TWS_ROOT)`, a plain temp dir with no `.git`. |
| `spacesFileIn` / `spacesLockIn` | `internal/cli/space_test.go:59-61` | Name the explicit `os.Lstat` assertions that are retained. |
| `workspaceMarkerIDFile`, `CheckoutMarkerDir`, `GitMarkerDir`, `EnsureWorkspaceMarkerID` | `internal/workspace.go:81`, `:86`, `:99`, `:134` | Proof that tws writes `.git/tws/workspace-id`. This is why the whole-`.git` exclusion was rejected. |
| `AddGitLocalExclude` / `addGitExclude` | `internal/enable.go:65`, `internal/cli/init.go:114` | Proof that tws writes `.git/info/exclude`. Same reason. |

## 3. Rationale for the exclusion boundary

Three candidate boundaries were considered:

1. **Single filename (`maintenance.lock`)** — rejected: `index.lock`, `packed-refs.lock`,
   `HEAD.lock`, `refs/**/*.lock`, `config.lock` are the same Git-scheduled class.
2. **Whole `.git` subtree (ownership)** — rejected: `.git` is *not* exclusively Git-owned here. tws
   writes `.git/tws/workspace-id` and `.git/info/exclude`, so this filter would hide precisely the
   tws-owned artifacts the no-side-effect test exists to detect.
3. **Transient lock files under `.git` (chosen)** — a path with a `.git` segment whose base name
   ends in `.lock`. Covers the whole observed flake class; retains every durable `.git` path,
   including both tws-owned ones. Non-lock Git transients (`packed-refs`, `commit-graph`, pack
   `.tmp`) are deliberately still compared: no evidence they flaked, and masking them would weaken
   the assertion for no gain.

The helper name and comment say **lock**, not metadata/ownership, so the boundary cannot be
misread as "`.git` is Git's".

## 4. Test file map — `internal/cli/space_test.go`

New helpers, inserted directly below `snapshotTree`:

- `isTransientGitLockPath(rel string) bool` — `internal/cli/space_test.go:92`. Requires both a
  `.lock` base name (`path.Base` on the slash-normalized path) and a path segment equal to `.git`.
  The conjunction is what keeps `.github`, `.gitignore`, `.git/tws/workspace-id`,
  `.git/info/exclude`, and the non-`.git` `.spaces.lock` in the comparison.
- `treeWalker` — `internal/cli/space_test.go:107`. A function type matching `filepath.Walk`; the
  seam that lets the collector be driven by a scripted walk in tests.
- `collectStableTreePaths(dir string, walk treeWalker) ([]string, error)` —
  `internal/cli/space_test.go:118`. Filters **during** traversal instead of post-filtering a
  finished snapshot, because a lock removed between `ReadDir` and `Lstat` arrives as a walk callback
  *error*, never as a listed path — the exact failure a `snapshotTree`-first wrapper cannot survive.
  A non-nil callback error is swallowed only when `isTransientGitLockPath(rel)` holds **and** the
  error is not-exist (`errors.Is(err, fs.ErrNotExist)` or `os.IsNotExist(err)`, the latter covering
  raw `syscall.ENOENT`); anything else is returned and aborts the walk with no paths. Successful
  lock entries are skipped; all other paths are appended in walk order.
- `snapshotTreeIgnoringGitLocks(t, dir) string` — `internal/cli/space_test.go:150`. Calls
  `collectStableTreePaths(dir, filepath.Walk)`, `t.Fatal`s on error, joins with `"\n"`. Walk order
  is preserved, so the before/after strings stay directly comparable.
- `snapshotHasPath(snapshot, rel string) bool` — `internal/cli/space_test.go:619`. Exact whole-line
  membership, so retention assertions cannot pass by substring coincidence.
- `scriptedFileInfo` — `internal/cli/space_test.go:606`. Minimal `os.FileInfo` for the scripted
  walk; the collector inspects paths only, never metadata.

Changed test:

| Test | Line | Change |
| --- | --- | --- |
| `TestSpaceAbsentRegistryCreatesNothing` | 345 | `before` and `after` now call `snapshotTreeIgnoringGitLocks`. Everything else — `list --json == "[]\n"`, nonzero `show`, exact `no space named "learning"` from `remove`, and the `os.Lstat` loop over `spaces.yaml` / `.spaces.lock` / `.tws-workspace` — is unchanged. |

New test, appended immediately after it:

- `TestSpaceSnapshotIgnoresTransientGitLocks` — `internal/cli/space_test.go:401`. Fixture:
  `t.TempDir()` + `.git/objects/` + `.git/info/` + `.github/workflows/ci.yml` + `.gitignore` +
  `.tws/config.yaml`. Assertions in order:
  1. the stable snapshot retains `.git`, `.git/objects`, `.git/info`, `.github/workflows/ci.yml`,
     `.gitignore`, `.tws/config.yaml`;
  2. creating `.git/objects/maintenance.lock` makes the **raw** `snapshotTree` contain
     `maintenance.lock` while the stable snapshot is unchanged, and removing it leaves the stable
     snapshot unchanged;
  3. creating `.git/tws/workspace-id` **changes** the stable snapshot and is listed;
  4. creating `.git/info/exclude` **changes** it and is listed; rewriting its content does **not**
     (asserted explicitly — `snapshotTree` is path-only, so content is out of its reach); renaming
     it **does**, with the old path absent and the new path present;
  5. writing `spaces.yaml` **changes** it and is listed.
  The raw-snapshot check in step 2 is the guard that keeps this regression honest — without it, a
  helper that returned a constant would pass; steps 3–5 are the guard against over-broad filtering.

- `TestCollectStableTreePathsWalkErrors` — `internal/cli/space_test.go:501`. Covers the
  mid-traversal race with **no timing dependence**: a table of scripted walks replays
  `filepath.Walk` semantics (joined path to the callback, abort on a non-nil callback return) over a
  fixed entry list. Cases: a vanished `.git/objects/maintenance.lock`
  (`&fs.PathError{Err: fs.ErrNotExist}`) → ignored, surrounding paths still collected in order; a
  vanished `.git/index.lock` as `syscall.ENOENT` → ignored (the `os.IsNotExist` branch); a lock
  listed without error → filtered; a vanished non-lock `.git/tws/workspace-id` → error; a vanished
  `.spaces.lock` outside `.git` → error; `fs.ErrPermission` on a lock → error. Error cases also
  assert no paths are returned, so tolerance cannot leak into a partial result.

Tests deliberately left alone: everything in `space_guard_test.go` (`:200`, `:303`, `:324`, `:366`,
`:411`, `:522`, `:545`, `:615`, `:949`) and `status_test.go` (`:253`, `:262`).

Imports: standard-library only — `errors` (`errors.Is`), `io/fs` (`fs.ErrNotExist`,
`fs.ErrPermission`, `fs.PathError`), `path` (`path.Base` on slash-normalized relative paths),
`syscall` (`syscall.ENOENT`), and `time` (`scriptedFileInfo.ModTime`). `os`, `path/filepath`,
`strings`, `testing` were already imported. `go.mod` / `go.sum` untouched.

## 5. Commands

The local shell exports `GIT_CONFIG_KEY_0=safe.bareRepository` / `GIT_CONFIG_VALUE_0=explicit`,
which makes the pre-existing `setupGitRepo` bare-remote fixture fail on `git symbolic-ref` —
verified to fail identically on a stashed, unmodified tree. Strip it for every run:

```bash
HERMETIC="env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 \
              -u GIT_CONFIG_KEY_1 -u GIT_CONFIG_VALUE_1"

gofmt -l internal
$HERMETIC go test ./internal/cli/ -run 'TestCollectStableTreePathsWalkErrors' -count=1 -v
$HERMETIC go test ./internal/cli/ -run 'TestSpace|TestCollectStableTreePaths' -count=1
$HERMETIC go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check && git --no-pager diff --stat
tpatch feature deps --validate-all
```

## 6. Results

- `gofmt -l internal` — silent.
- `TestCollectStableTreePathsWalkErrors` — PASS, all six subtests (0.23s).
- `go test ./internal/cli/ -run 'TestSpace|TestCollectStableTreePaths' -count=1` — ok (11.2s),
  including `TestSpaceSnapshotIgnoresTransientGitLocks` and
  `TestSpaceAbsentRegistryCreatesNothing/{external,checkout}`.
- `go test ./... -count=1` — ok: `cmd/tws` 0.21s, `internal` 12.1s, `internal/cli` 30.6s.
- `go vet ./...` — clean. `golangci-lint run ./...` — `0 issues.` `make build` — ok.
- `git diff --check` — clean. `git diff --stat` — `internal/cli/space_test.go` is the only
  non-`.tpatch` file; no product file.

## 7. Smallest changeset

One file: four helpers plus a walk-seam type and a stub `os.FileInfo`, two call-site
substitutions, two regression tests. No product code, no shared helper weakened — `snapshotTree`
and `snapshotTreeIgnoringLock` stay byte-identical to HEAD.

## 8. Notes and constraints

- Exclude by **transience**, not by ownership. `.git` holds tws-owned state
  (`.git/tws/workspace-id`, `.git/info/exclude`), so a subtree exclusion is a coverage hole, not a
  simplification.
- `snapshotTree` is path-only. `.git/info/exclude` content edits are therefore invisible to it; the
  regression test asserts that limit explicitly instead of implying content coverage. Making the
  snapshot content-aware is a separate, larger change.
- Filter **during** the walk, not after it. A vanished lock is a walk *error*, not a missing line,
  so a `snapshotTree`-first wrapper still fails; and error tolerance must stay double-gated
  (transient lock path **and** not-exist) so real traversal failures keep aborting the test.
- Keep the `treeWalker` seam. It is the only reason the vanished-lock path is testable without
  sleeps, retries, or dependence on Git maintenance firing.
- Do **not** widen `snapshotTree` or `snapshotTreeIgnoringLock`: the guard and status tests depend
  on the stricter basis, and a shared change would silently relax them.
- Hard parent `workspace-sibling-links` owns the checkout `.tws` layout and the `parent`/`root`
  split this test snapshots; re-register it with
  `tpatch feature deps fix-space-test-git-maintenance-race add workspace-sibling-links` if it is
  ever dropped, and re-run `tpatch feature deps --validate-all`.
