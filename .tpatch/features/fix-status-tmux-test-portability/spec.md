# Specification — fix-status-tmux-test-portability

## 1. Problem statement

Two `tws status` CLI tests assert an exact issue set while letting the host decide whether `tmux`
exists on PATH. On `macos-latest` — which ships no tmux — the correct, specified workspace issue
`tmux-missing` appears and both assertions fail (CI run `31499102361`, green on `ubuntu-latest`).

The product is right; the tests are not portable. This feature makes the tmux inventory an explicit
test fixture so the same assertions hold on every runner, and adds the missing test that pins the
`tmux-missing` behaviour on purpose instead of by accident of host image.

**Hard parent**: `agent-work-status-dashboard` (landed `3b2f4aa`). This change is meaningless
without `tws status`, its issue vocabulary, and `internal.RealTmuxInventory`.

## 2. Acceptance criteria

1. `internal/cli/status_test.go` gains `withIdleTmuxOnPath(t *testing.T)`, which prepends a
   `t.TempDir()` to `PATH` (via `t.Setenv`) containing a **real executable** `tmux` stub that writes
   `no server running` to stderr and exits non-zero — the exact condition
   `RealTmuxInventory.Snapshot` recognizes at `internal/agent_status.go:439`.
2. `withIdleTmuxOnPath` self-verifies before returning: `exec.LookPath("tmux")` resolves to the stub
   path, and `internal.RealTmuxInventory{}.Snapshot()` yields `Available == true`,
   `ServerRunning == false`, `Err == nil`, `len(Sessions) == 0` — i.e. **no tmux issue is emitted**.
3. The stub is created with mode `0o755` **and** explicitly `os.Chmod`-ed to `0o755`, so a
   restrictive umask cannot make it non-executable.
4. `internal/cli/status_test.go` gains `withoutTmuxOnPath(t *testing.T)`, which sets `PATH` to a
   single `t.TempDir()` containing a symlink to the real `git` binary, and asserts that `git` stays
   resolvable, `tmux` does not resolve, and `RealTmuxInventory{}.Snapshot().Available == false`.
5. The two tests that failed on macOS — `TestStatusEmptyWorkspace` and
   `TestStatusReportsDirectRecordsAndExitsZero` — call `withIdleTmuxOnPath(t)` before running
   `status`, and their existing assertions are otherwise unchanged.
6. `TestStatusIsCwdIndependent`, which likewise compares status output across cwds while the real
   inventory runs, also calls `withIdleTmuxOnPath(t)` so its comparison cannot vary by host.
7. A new test `TestStatusReportsTmuxMissingWhenTmuxIsAbsent` uses `withoutTmuxOnPath(t)` and asserts,
   on `tws status --json`: exit 0, exactly one issue, `code == "tmux-missing"`,
   `severity == "info"`, `scope == "workspace"`, `feature` and `name` both null, and
   `workspace.attention.status == "idle"`.
8. `internal/agent_status.go` and every other non-test file are byte-identical to `3b2f4aa`; the
   only changed file is `internal/cli/status_test.go`.
9. `go.mod` / `go.sum` are unchanged; the only new import in the changed file is `os/exec`.
10. `go test ./internal/cli/ -run 'TestStatus' -count=1` passes, and so do the full gates:
    `go test ./... -count=1`, `go vet ./...`, `golangci-lint run ./...`, `make build`,
    `gofmt -l internal` (silent), `git diff --check`.
11. The suite passes on a host **with** tmux and on a host **without** it. Simulate the macOS runner
    locally with a PATH that excludes tmux (see `exploration.md` §4) and confirm the same result.

## 3. Out of scope

- Any change to `tws status` behaviour, output, issue codes, severities, scopes, or attention
  rollup. `tmux-missing` semantics are the parent's and stay frozen.
- Installing tmux on the macOS CI runner, or any edit to `.github/workflows/ci.yml`.
- Adding a `TmuxInventoryProbe` injection seam to `statusCmd` / CLI wiring; `internal`-package tests
  already inject the probe directly, and the CLI tests deliberately exercise the real shell-out.
- Fixing or restructuring `internal/agent_status_test.go` (it already drives the probe interface).
- Windows portability of the fixtures; CI is Linux + macOS only.
- Making these tests `t.Parallel`-safe — `t.Setenv` forbids it.
- Any dependency, module, or tooling change.

## 4. Plan

1. Add `withIdleTmuxOnPath` and `withoutTmuxOnPath` near the top of `internal/cli/status_test.go`,
   above `runStatus`, each with a comment stating *why* the host cannot be trusted (criteria 1–4).
2. Insert `withIdleTmuxOnPath(t)` into `TestStatusEmptyWorkspace`, `TestStatusIsCwdIndependent`, and
   `TestStatusReportsDirectRecordsAndExitsZero`, after the workspace env helper and before any
   `status` invocation (criteria 5–6).
3. Append `TestStatusReportsTmuxMissingWhenTmuxIsAbsent` at the end of the file (criterion 7).
4. Run the focused selector, then the full gates, then re-run the focused selector under a
   tmux-free PATH (criteria 10–11).
5. Land as one test-only commit with `agent-work-status-dashboard` recorded as the hard parent;
   confirm `tpatch feature deps --validate-all` is clean.
