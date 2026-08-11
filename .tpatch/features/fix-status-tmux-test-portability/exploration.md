# Exploration — fix-status-tmux-test-portability

Everything below was read from the working tree (product code at `3b2f4aa`). Line numbers are
locators — anchor on symbol names.

## 1. The only file that changes

`internal/cli/status_test.go` — the sole file in the changeset. No product file, no `go.mod`, no
workflow file.

## 2. Product code that is read but never modified

| Symbol | Location | Why it matters |
| --- | --- | --- |
| `TmuxInventoryProbe` | `internal/agent_status.go:420-423` | The seam; CLI tests use the real impl, not a fake. |
| `RealTmuxInventory` / `Snapshot()` | `internal/agent_status.go:427-429` | `exec.LookPath("tmux")` failure ⇒ `Available=false`; this is the whole host dependency. |
| no-server detection | `internal/agent_status.go:439` | Matches lowercased `no server running` or `error connecting to` on `tmux list-sessions` failure ⇒ returns `Available=true, ServerRunning=false, Err=nil`. This is exactly what the idle stub reproduces. |
| default probe wiring | `internal/agent_status.go:397` (`Tmux: RealTmuxInventory{}`) | Why `statusCmd()` shells out to the host tmux. |
| `IssueTmuxMissing = "tmux-missing"` | `internal/agent_status.go:99` | Code asserted by the new test. |
| `emitTmuxWorkspaceIssue` | `internal/agent_status.go:810-826` | `!Available` + no tmux record ⇒ one `SeverityInfo`, `ScopeWorkspace` issue with empty feature/name. `Err != nil` ⇒ `tmux-unverifiable` warning — which is why the idle stub must produce `Err == nil`. |

## 3. Test file map — `internal/cli/status_test.go`

Existing helpers reused unchanged:

- `runStatus(t, args...)` — builds `statusCmd()`, captures stdout/stderr, returns `(out, err, error)`.
- `setupGitRepo(t, "main")` — `internal/cli/new_integration_test.go:135`, real temporary Git repo.
- `withUnifiedWorkspaceEnv(t, repo)` — `internal/cli/space_guard_test.go:58`, isolates `TwsRoot()`.
- `captureStdout(t, fn)` and `addExternal(...)` — used by the record-bearing tests.

New helpers (inserted above `runStatus`):

- `withIdleTmuxOnPath(t)` — `t.TempDir()` + `tmux` stub `#!/bin/sh\necho 'no server running' >&2\nexit 1\n`,
  `os.WriteFile(..., 0o755)` then `os.Chmod(..., 0o755)` (umask guard), `t.Setenv("PATH", dir + os.PathListSeparator + os.Getenv("PATH"))`,
  then two assertions: `exec.LookPath("tmux") == stub`, and
  `internal.RealTmuxInventory{}.Snapshot()` is `{Available:true, ServerRunning:false, Err:nil, Sessions:{}}`.
  Prepending (not replacing) PATH keeps `git` and everything else reachable.
- `withoutTmuxOnPath(t)` — `exec.LookPath("git")`, `os.Symlink(gitPath, dir/git)`,
  `t.Setenv("PATH", dir)` (replace, not prepend), then assertions: `git` resolves, `tmux` does not,
  `Snapshot().Available == false`.

Tests that take the idle fixture (these compare exact issue sets / cross-cwd output):

| Test | Line | Failure it fixes |
| --- | --- | --- |
| `TestStatusEmptyWorkspace` | 117 | CI `status_test.go:89` — `features/issues must be empty arrays` saw `tmux-missing`. |
| `TestStatusIsCwdIndependent` | 182 | Not failing on CI, but compares real-inventory output across cwds; pinned for the same reason. |
| `TestStatusReportsDirectRecordsAndExitsZero` | 287 | CI `status_test.go:255` — `codes:[tmux-missing]` broke the own-scope assertion at line 312. |

New test appended at end of file:

- `TestStatusReportsTmuxMissingWhenTmuxIsAbsent` — `setupGitRepo` + `withUnifiedWorkspaceEnv` +
  `os.MkdirAll(root, 0755)` + `withoutTmuxOnPath(t)`, then `runStatus(t, "--json")`: exit 0, exactly
  one issue, `code=tmux-missing`, `severity=info`, `scope=workspace`, `feature`/`name` nil,
  `workspace.attention.status == "idle"`.

Tests deliberately left alone: `TestStatusHelpSurface` (88), `TestStatusRejectsUnknownFlag` (108),
`TestStatusFeatureFilterAndNotFound` (152), `TestStatusGuardsRegisteredSpaceName` (245),
`TestStatusFailsClosedOnMalformedSpaces` (267) — none asserts an exact issue set or full output.
`internal/agent_status_test.go` (e.g. `:699`, `:959`) already injects a fake probe and stays
untouched.

Imports: `os/exec` is the only addition; `internal`, `os`, `path/filepath`, `testing` are already
imported at `internal/cli/status_test.go:3-13`.

## 4. Commands

Focused:

```bash
go test ./internal/cli/ -run 'TestStatus' -count=1
```

Simulate the macOS runner locally (no tmux on PATH) — the fixtures must make this identical:

```bash
command -v tmux   # confirm the local host does have tmux
env PATH="$(dirname "$(command -v git)"):$(go env GOROOT)/bin" go test ./internal/cli/ -run 'TestStatus' -count=1
```

Full gates before landing:

```bash
gofmt -l internal
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

## 5. Smallest changeset

One file, two helpers, three one-line insertions, one new test. `git diff --stat` must read
`internal/cli/status_test.go` only.

## 6. Notes and constraints

- `t.Setenv` marks the test non-parallel; none of the touched tests call `t.Parallel`, and none may
  start doing so.
- Do **not** replace `RealTmuxInventory` with a fake in the CLI tests: the value of these tests is
  that they drive the real shell-out end to end. The fixture controls the *environment*, not the
  code path.
- The stub must exit non-zero **and** print the recognized substring. Exiting 0 would set
  `ServerRunning=true`; a different message would set `snap.Err` and emit `tmux-unverifiable`
  instead of nothing.
- `withoutTmuxOnPath` must keep `git` reachable — status builds real Git inventories from the
  temporary repo, and a bare empty PATH would fail for unrelated reasons.
