# Exploration

## Critical Files

- `internal/workspace.go`, `internal/config.go`, `internal/paths.go`
- `internal/cli/init.go`, `internal/cli/add.go`, `internal/cli/new.go`
- `internal/cli/rename.go`, `archive.go`, `delete.go`
- `internal/cli/export.go`, `importcmd.go`, `root.go`
- new mode-aware lifecycle helpers/tests under `internal/`

## Reuse

- WorkspaceMode/ResolveCurrentWorkspace from workspace-mode-foundation
- Stack, Decisions, inject helpers, configured-base resolution
- Existing external implementations as protected backend behavior

## Tests

Use real temporary Git repos. Pair checkout tests with external regression tests for add/new/rename/archive/delete/export/import. Verify `.git/info/exclude`, no linked worktree creation, metadata atomicity, state exclusion, and explicit destructive flags.
