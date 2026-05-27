# Analysis: workspace-portability

## Summary

Export/import workspace metadata for cross-machine portability. Three modes:

1. `tws export auth` — YAML to stdout (lightweight, pipe-friendly)
2. `tws export auth --full -o auth.tar.gz` — tarball with inject files
3. `tws export auth --to-repo` — writes to .tws/workspaces/auth.yaml (travels with git push)
4. `tws import <file>` — recreates workspace from YAML or tarball
5. `tws import --from-repo auth` — reads from .tws/workspaces/

## Export Format (YAML)

```yaml
feature: auth
exported_at: "2026-05-26T..."
stack:
  branches:
    - name: pr2
      base: main
      repo: ""
      last_base_sha: abc123
    - name: pr3
      base: pr2
decisions:
  entries:
    - id: 1
      branch: pr2
      type: breaking
      summary: "Changed User.ID"
```

## Import Flow

1. Parse YAML/tarball
2. Create feature dir in workspace (tws add)
3. Write stack.yaml and decisions.yaml
4. If tarball, extract inject/ files
5. For each branch: tws new (checks out existing branches from git)
6. Inject files into worktrees

## Affected Areas

- New: `internal/export.go` — WorkspaceExport struct, marshal/unmarshal
- New: `internal/cli/export.go` — tws export command
- New: `internal/cli/import.go` — tws import command
- `internal/cli/root.go` — register commands
