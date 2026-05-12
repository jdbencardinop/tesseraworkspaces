# Analysis: worktree-context-injection

## Summary

Add an `inject/` directory at the feature level. Files placed there are symlinked into every worktree so agents auto-discover them. Supports re-syncing all worktrees or a single worktree.

## Commands

- `tws add` — creates `inject/` with default `CLAUDE.local.md`
- `tws new` — symlinks inject/ contents into the new worktree
- `tws open` — re-syncs symlinks before launching agent
- `tws inject <feature>` — manual re-sync across all worktrees
- `tws inject <feature> <branch>` — re-sync a single worktree

## Affected Areas

- New: `internal/inject.go` — InjectFiles(featurePath, worktreePath) logic
- `internal/cli/add.go` — create inject/ dir with default CLAUDE.local.md
- `internal/cli/new.go` — call InjectFiles after worktree creation
- `internal/cli/open.go` — call InjectFiles before launching agent
- New: `internal/cli/inject.go` — tws inject command
- `internal/cli/root.go` — register inject command

## Acceptance Criteria

1. `tws add` creates inject/ with a default CLAUDE.local.md
2. `tws new` symlinks inject/ contents into the worktree
3. `tws inject <feature>` re-syncs all worktrees
4. `tws inject <feature> <branch>` re-syncs one worktree
5. Symlinks are relative so they work regardless of absolute paths
6. Existing files in the worktree are not overwritten (skip with warning)
7. Nested directories in inject/ are handled (e.g., .claude/skills/)
