# Specification: fix-open-cwd-after-exit
## Minimal Changeset
1. `internal/cli/open.go` — replace syscall.Exec with exec.Command, spawn shell after agent exits
