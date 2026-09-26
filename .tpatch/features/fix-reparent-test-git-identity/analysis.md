# Analysis

Main CI run 36220348728 passes on macOS but fails 37 internal reparent test
groups on Ubuntu while computing the first replayed commit. The shared
`gitInTest` helper gives identity only to its own child commands, whereas
`runReparentGit` correctly inherits the test process environment. The reparent
fixture configures neither a process-scoped test identity nor repository-local
identity, so production rebases depend on host Git identity inference.

The failure reproduces on macOS with empty author/committer environment values.
Set identity with `testing.T.Setenv` in the shared reparent fixture, following
the existing CLI integration-fixture pattern. This changes tests only, restores
the original environment after each test, and does not alter production
commands, user configuration, transaction semantics, or the certified recipe.
