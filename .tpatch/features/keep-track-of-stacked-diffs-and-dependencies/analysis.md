# Analysis: keep-track-of-stacked-diffs-and-dependencies

## Summary

Replace the blind "rebase everything against origin/main" sync with a dependency-aware model. Branches within a feature declare their parent via `stack.yaml`. `ts sync` rebases in topological order (parent first, then children). The data model supports DAGs (A→B, A→C) from day one, but only linear chains are synced for now — divergent sync is deferred to `divergent-stack-sync`.

## Compatibility

**Status**: safe (behavioral change, but improves correctness)

Current `ts sync` rebases every worktree against `origin/main`. The new behavior rebases each branch against its declared parent instead, which is what users actually want for stacked diffs. Features without a `stack.yaml` fall back to the current behavior (rebase against origin/main).

## Affected Areas

- **New: `internal/stack.go`** — `Stack` struct, `LoadStack()`/`SaveStack()`, topological sort
- **`internal/cli/new.go`** — Append branch entry to `stack.yaml` on worktree creation
- **`internal/cli/sync.go`** — Rewrite to sync in topological order using stack metadata
- **`cmd/ts/main.go`** — Add `ts stack` subcommand for viewing the dependency graph
- **`docs/configuration.md`** — Document `stack.yaml` format

## Data Model

```yaml
# <workspace>/<feature>/stack.yaml
branches:
  - name: auth-models
    base: main
  - name: auth-middleware
    base: auth-models
  - name: auth-routes
    base: auth-middleware
  # DAG example (supported in schema, sync deferred):
  # - name: auth-tests
  #   base: auth-models    # diverges from auth-middleware
```

Each entry declares its base (parent). The list order doesn't matter — topological sort resolves execution order. `base: main` means "rebase against origin/main" (root of the stack).

## Design Decisions (Resolved)

1. **One worktree per branch** — our model, parallel agent work is the goal
2. **DAG-ready schema** — `base` field supports any parent, not just the previous entry
3. **Linear sync only for now** — topological sort, rebase children after parent. Divergent sync is a separate feature.
4. **Fallback** — features without `stack.yaml` rebase all worktrees against origin/main (current behavior preserved)
5. **`ts new` auto-registers** — when creating a worktree, user specifies the base branch; defaults to `main`
6. **`ts stack` command** — prints the dependency tree for a feature

## Acceptance Criteria

1. `ts new <feature> <branch> [--base <parent>]` creates worktree and appends to `stack.yaml`
2. `stack.yaml` is created on first `ts new` for a feature
3. `ts sync <feature>` rebases branches in topological order (parent before child)
4. If a rebase fails, dependent branches are skipped with a warning
5. `ts stack <feature>` prints the dependency tree
6. Features without `stack.yaml` fall back to current rebase-all behavior
7. Cycle detection rejects invalid `stack.yaml` configurations
8. Tests cover: linear chain sync, missing stack.yaml fallback, cycle detection, rebase failure cascading
