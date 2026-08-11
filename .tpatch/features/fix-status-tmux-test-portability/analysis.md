# Analysis — fix-status-tmux-test-portability

## Summary

`agent-work-status-dashboard` landed at `3b2f4aa`. Main CI run `31499102361` passed on
`ubuntu-latest` and failed on `macos-latest` for exactly two tests in
`internal/cli/status_test.go`:

- `TestStatusEmptyWorkspace` — `status_test.go:89`: `features/issues must be empty arrays, got [] /
  [map[code:tmux-missing ... scope:workspace severity:info]]`.
- `TestStatusReportsDirectRecordsAndExitsZero` — `status_test.go:255`: `issue_count and codes stay
  own-scope: map[codes:[tmux-missing] issue_count:0 status:needs_attention]`.

Both failures are **test-environment assumptions, not product defects**. GitHub's `macos-latest`
image does not ship `tmux`; the Ubuntu image and developer machines do. The product behaviour under
a missing binary is the specified one: `RealTmuxInventory.Snapshot` (`internal/agent_status.go:429`)
returns `Available=false` when `exec.LookPath("tmux")` fails, and
`statusBuilder.emitTmuxWorkspaceIssue` (`internal/agent_status.go:810`) then emits one
workspace-scoped `tmux-missing` issue at `SeverityInfo` with no branch attribution. That is correct
and must not change. The two tests simply asserted an exact issue set while letting the host decide
whether that extra issue existed.

The fix is to remove the host dependency from the tests: make the tmux inventory an explicit
fixture, so the exact-issue assertions run against a known-idle tmux, and add one dedicated test
that pins the `tmux-missing` behaviour against a provably tmux-free PATH. This restores the missing
half of the coverage — before the fix, the `tmux-missing` path was only ever exercised accidentally,
on whichever runner happened to lack the binary.

## Root cause

`internal/cli` status tests exercise the real `RealTmuxInventory` through the default probe wiring
(`internal/agent_status.go:397`), which shells out to the host `tmux`. The resulting issue set is
therefore a function of the host image:

| Host | `LookPath("tmux")` | Emitted issue | Exact-issue assertions |
| --- | --- | --- | --- |
| ubuntu-latest / dev machines | found, no server | none | pass |
| macos-latest | not found | `tmux-missing` (info, workspace) | fail |

`tmux-missing` is `SeverityInfo`, so it does not by itself raise attention; the second failure is a
consequence of the workspace-level `codes` list gaining an entry while `issue_count` stayed 0, which
the own-scope assertion reads as a violation.

## Already upstream?

No. Nothing in `internal/cli/status_test.go` at `3b2f4aa` controls PATH or the tmux binary; the
helpers `withIdleTmuxOnPath` / `withoutTmuxOnPath` do not exist upstream. Existing helpers
`setupGitRepo` (`internal/cli/new_integration_test.go:135`) and `withUnifiedWorkspaceEnv`
(`internal/cli/space_guard_test.go:58`) isolate the workspace but not the executable environment.

## Compatibility

- **No product behaviour change.** `internal/agent_status.go` is untouched; `tmux-missing` semantics
  (code, `info`, `workspace` scope, no `feature`/`name`) stay exactly as specified by the parent.
- **No dependency change.** The fixture uses only `os`, `os/exec`, `path/filepath` from the standard
  library; `os/exec` is the single new import in the test file.
- **No new tooling, no CI change.** `.github/workflows/ci.yml` keeps its
  `[ubuntu-latest, macos-latest]` matrix and its `go test ./... -count=1` step; installing tmux on
  the macOS runner is explicitly rejected (see risks).
- **Scope is one test-only file**, `internal/cli/status_test.go`.
- **Hard parent**: `agent-work-status-dashboard`. This feature has no meaning without the `status`
  command, its issue codes, and `RealTmuxInventory`.

## Risks

| Risk | Assessment |
| --- | --- |
| Stub does not reproduce the real no-server condition | Mitigated in-fixture: the stub writes `no server running` to stderr and exits 1 — the exact substring `RealTmuxInventory.Snapshot` matches (`internal/agent_status.go:439`) — and the helper asserts the resulting snapshot is `Available=true, ServerRunning=false, Err=nil, len(Sessions)==0` before any test body runs. |
| Stub not executable (umask) | Mitigated: explicit `os.Chmod(stub, 0o755)` after `os.WriteFile`, plus a `exec.LookPath("tmux")` identity assertion that the resolved path is the stub. |
| A truncated PATH breaks Git-dependent tests | `withoutTmuxOnPath` symlinks the real `git` into the temporary directory and asserts `git` stays resolvable while `tmux` does not, so status still builds real Git inventories. |
| PATH leaks between tests | `t.Setenv` restores PATH at test end; `t.TempDir` removes the stub. Both make the fixtures unsafe for `t.Parallel`, which these tests do not use. |
| Windows | Out of scope, as for the parent: the `#!/bin/sh` stub is POSIX-only and CI runs Linux + macOS only. |
| Installing tmux on the macOS runner instead | Rejected: it hides the `tmux-missing` code path entirely, adds runner install time and a network dependency, and leaves the test suite host-dependent for every contributor. Injecting fixtures makes both halves deterministic everywhere. |

## Verdict

Compatible and low risk. One test-only file, no product or dependency changes, and it converts a
host-dependent assertion into two explicit, deterministic cases that also close a real coverage gap.
