# Fix reparent test Git identity

## Acceptance criteria

1. The internal reparent fixture provides explicit test author and committer
   name/email to both fixture-side Git commands and production Git subprocesses.
2. With global Git configuration disabled and inherited identity empty, real
   reparent execution succeeds in external and checkout modes and cleans up.
3. Identity setup is test-scoped and never writes global or system Git config.
4. `go test ./internal -run 'TestReparentFixtureProvidesGitIdentity|TestRunReparentExternalSuccess' -count=1`
   passes without depending on the host identity.
5. Full tests, vet, golangci-lint, build, formatting, and dependency gates pass;
   main CI must succeed before Lane A is tagged.

## Scope and plan

Update `internal/reparent_fixtures_test.go` and the SHA-256/reftable fixture
setup in `internal/reparent_run_test.go`: share the existing test-scoped
environment pattern and add real-Git hostile-environment regression coverage.
Do not modify production error classification, transaction behavior, the main
safe-reparent feature's canonical patch, or unrelated workflow dependencies.
Record and land this CI portability correction as its own traceable feature
commit on top of the already-pushed safe-reparent implementation.
