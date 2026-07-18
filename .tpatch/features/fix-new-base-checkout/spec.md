# Specification: fix-new-base-checkout

## Problem

`tws new --base` records a base but does not create the new branch from it.

## Acceptance Criteria

1. `go test ./internal/cli -run TestCreateWorktreeFrom` passes using real temporary Git repositories.
2. A new branch created with `--base master` starts at local `master`, even when source-repo `HEAD` differs.
3. `--base origin/master`, tags, and commit SHAs are used literally.
4. An omitted base uses the selected repo's `origin/HEAD`.
5. `--repo` scopes default/base resolution to that repo.
6. From a feature directory, a single source repo is inferred; ambiguous multi-repo features require `--repo`.
7. Invalid bases fail before writing a stack entry or worktree.
8. Existing branch checkout does not reset the branch to `--base`.

## Out of Scope

Sync-mode changes and reparenting existing stack entries.

## Plan

Refactor repo/base resolution in `internal/cli/new.go` and add repo-scoped Git helpers in `internal/exec.go`. Add real-Git integration tests under `internal/cli/`.
