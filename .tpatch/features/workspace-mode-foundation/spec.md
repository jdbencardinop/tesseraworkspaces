# Specification

## Acceptance Criteria

1. `WorkspaceMode` supports `external` and `checkout`; missing mode defaults to `external`.
2. A resolved `Workspace` exposes stable ID, mode, repo root, metadata root, and explicit capabilities.
3. Existing `TwsRoot`, `FeaturePath`, `WorktreePath`, workspace-marker, config, and worktree detection results are unchanged in external mode.
4. Merely having `<repo>/.tws/config.yaml` does not enable checkout mode.
5. Invalid mode values fail clearly rather than silently changing behavior.
6. Characterization tests cover env override, configured workspace, repo-relative sibling, global fallback, workspace root, feature dir, and linked worktree invocation.
7. `go test ./...`, `go vet ./...`, golangci-lint, and production build pass.

## Out of Scope

- Creating checkout metadata or branches.
- Switching branches in one checkout.
- Mode migration/conversion.
- Global registry or open-anywhere behavior.
