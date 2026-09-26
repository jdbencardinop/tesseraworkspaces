# Exploration

- `internal/reparent_fixtures_test.go:newReparentPrimitiveRepo` sets process-wide
  Git config sanitization with `t.Setenv`, but omits author/committer identity.
- `internal/checkout_health_test.go:gitInTest` sets all four identity variables
  only in the environment of its fixture-side child process.
- `internal/reparent_exec.go:runReparentGit` inherits its caller environment,
  as production Git operations should.
- `internal/cli/sync_golden_test.go` already provides author/committer identity
  via `t.Setenv`, explaining why the CLI package passes on the Ubuntu runner.
- `internal/reparent_run_test.go:newReparentWorkspace` and its `approvedInput`
  and `begin` helpers support actual external/checkout reparent execution.
- The separate SHA-256 workspace and reftable test in the same file construct
  repositories directly, so both must use the shared test environment helper.

Regression: poison inherited identity, disable global Git config, build each
workspace fixture (including SHA-256), inspect identity through `runReparentGit`, execute
the real approved reparent, and assert that transaction residue is removed.
