# Exploration: keep-track-of-stacked-diffs-and-dependencies

## Relevant Files

- **New: `internal/stack.go`** — Stack data model, YAML load/save, topological sort
- **New: `internal/stack_test.go`** — Tests for topo sort, cycle detection, load/save
- **New: `internal/cli/stack.go`** — `ts stack <feature>` command to print dependency tree
- **`internal/cli/new.go`** — Add `--base` flag, append to stack.yaml on worktree creation
- **`internal/cli/sync.go`** — Rewrite to use topo-sorted stack for sync order
- **`cmd/ts/main.go`** — Register `stack` subcommand
- **`docs/configuration.md`** — Document stack.yaml format
- **`README.md`** — Add `ts stack` to usage

## Minimal Changeset

1. **`internal/stack.go`** — Core data model:
   - `StackEntry{Name, Base string}`, `Stack{Branches []StackEntry}`
   - `LoadStack(featurePath)` reads `<feature>/stack.yaml`
   - `SaveStack(featurePath, stack)` writes it
   - `TopoSort(stack)` — Kahn's algorithm, returns sorted entries or error on cycle
   - `PrintTree(stack)` — walks the tree and prints indented output

2. **`internal/cli/new.go`** — After creating worktree:
   - Parse `--base` from args (simple manual parsing, no flag library yet)
   - Load stack, append `StackEntry{Name: branch, Base: base}`, save

3. **`internal/cli/sync.go`** — Rewrite:
   - Load stack; if empty, fall back to current behavior
   - Topo-sort; fetch once
   - For each entry: `RunDir(worktreePath, "git", "rebase", base)`
   - On failure: mark descendants as skipped

4. **`internal/cli/stack.go`** — Simple: load stack, print tree

5. **`cmd/ts/main.go`** — Add `case "stack":` to switch

6. **Tests** — `internal/stack_test.go` for TopoSort (linear, DAG, cycle, single node)
