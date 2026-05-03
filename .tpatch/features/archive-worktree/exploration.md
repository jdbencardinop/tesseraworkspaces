# Exploration: archive-worktree

## Relevant Files

- **New: `internal/cli/archive.go`** — Archive command
- **`internal/cli/sync.go`** — Add `--update-refs`, optimistic rebase for archived
- **`internal/cli/new.go`** — Idempotent stack.yaml registration
- **`internal/cli/list.go`** — Change "missing" → "archived"
- **`internal/stack.go`** — Add `HasBranch()` helper
- **`internal/exec.go`** — Add `RunSilent()` for suppressed output
- **`cmd/tws/main.go`** — Register `archive` subcommand

## Minimal Changeset

1. **`internal/stack.go`** — `HasBranch(s Stack, name string) bool` — simple loop check

2. **`internal/exec.go`** — `RunSilent(name string, args ...string) error` — like `Run()` but discards stdout/stderr. Used for optimistic rebase where we only care about success/failure.

3. **`internal/cli/archive.go`** — Simple:
   - Validate feature and branch exist in stack
   - `git worktree remove <path>` (skip if already gone)
   - `git worktree prune`
   - Print status

4. **`internal/cli/sync.go`** — Rework the sync loop:
   - Active branches: `RunDir(path, "git", "rebase", "--update-refs", base)` — this automatically updates archived intermediates
   - Track which archived branches were updated by `--update-refs` (any archived branch that's an ancestor of an active branch)
   - Remaining archived branches (no active descendant): try `RunSilent("git", "rebase", base, branch)`, abort on failure

5. **`internal/cli/new.go`** — Before `stack.Branches = append(...)`, check `internal.HasBranch(stack, branch)` and skip if true

6. **`internal/cli/list.go`** — Change `status = "missing"` to `status = "archived"`
