# Specification: keep-track-of-stacked-diffs-and-dependencies

## Acceptance Criteria

1. `ts new <feature> <branch> [--base <parent>]` creates worktree and appends entry to `stack.yaml`; base defaults to `main`
2. `stack.yaml` is created automatically on first `ts new` for a feature
3. `ts sync <feature>` fetches, then rebases branches in topological order (parent→child)
4. If a rebase fails on branch X, all descendants of X are skipped with a warning
5. `ts stack <feature>` prints the dependency tree to stdout
6. Features without `stack.yaml` fall back to current behavior (rebase all against origin/main)
7. Cycle detection rejects invalid configurations with a clear error
8. Tests cover: linear sync order, fallback, cycle detection, rebase failure skip

## Implementation Plan

1. **Create `internal/stack.go`**
   - `StackEntry{Name, Base string}` and `Stack{Branches []StackEntry}`
   - `LoadStack(featurePath string) (Stack, error)` — reads `stack.yaml`, returns empty stack if missing
   - `SaveStack(featurePath string, stack Stack) error` — writes `stack.yaml`
   - `TopoSort(stack Stack) ([]StackEntry, error)` — topological sort, returns error on cycles
   - Uses `gopkg.in/yaml.v3` (already a dependency)

2. **Update `internal/cli/new.go`**
   - Accept optional `--base` flag (default: `main`)
   - After creating worktree, load stack, append entry, save stack
   - Parse args: `ts new <feature> <branch>` or `ts new <feature> <branch> --base <parent>`

3. **Rewrite `internal/cli/sync.go`**
   - Load `stack.yaml` from feature path
   - If missing, fall back to current behavior (rebase all against origin/main)
   - If present, topo-sort and rebase each branch against its base in order
   - On failure, collect descendants and skip them
   - Print clear status per branch (synced/failed/skipped)

4. **Add `ts stack <feature>` command**
   - New `internal/cli/stack.go` — loads stack, prints tree
   - Register in `cmd/ts/main.go`

5. **Tests in `internal/stack_test.go`**
   - TopoSort: linear chain, single branch, cycle detection, DAG (valid sort)
   - LoadStack: missing file returns empty, valid yaml parses, invalid yaml errors

6. **Update docs**
   - `docs/configuration.md` — document `stack.yaml` format
   - `README.md` — add `ts stack` to usage section
