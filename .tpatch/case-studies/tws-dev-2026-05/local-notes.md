# Local Notes — tws-dev-2026-05

## Session observations

### Provider issues
- copilot proxy at localhost:4141 returned 500 errors consistently
- Restarting the server didn't help
- The provider configured as `--preset copilot --model gpt-5.5`
- Fell back to heuristic mode on every invocation after the first few attempts
- Eventually stopped trying and went full manual

### Commit to wrong branch incident
- During divergent stack smoke testing, we were left on the `routes` branch
- Committed two production commits to `routes` instead of `main`
- Caught during `git push` (no upstream for `routes`)
- Fixed with cherry-pick to main, then deleted stale branch
- Lesson: always check `git branch --show-current` after smoke tests with worktrees

### Features that evolved during implementation
- `keep-track-of-stacked-diffs-and-dependencies` was scoped for linear only, but the DAG support came for free from Kahn's algorithm
- `tmux-free-mode` started as "add --no-tmux flag" but evolved into flipping the default (exec is now default, tmux is opt-in)
- `archive-worktree` gained the archived-vs-missing distinction during implementation based on `git worktree list --porcelain` prunable detection
- `cross-worktree-agent-context` started as a complex messaging system but was simplified to extending the existing decisions.yaml with a `to` field

### Steering file was key
- The dependency validation clause added to steering saved us multiple times
- After explore phase, we often discovered new links between features
- Without the steering rule, we would have implemented features out of order

### Smoke tests as quality gate
- Every feature got at least one end-to-end smoke test before committing
- Caught real bugs: inject symlinks causing `git worktree remove` to need `--force`, wrong cwd after agent exit, auto-select not printing what was picked
- The user explicitly valued this practice — saved as feedback memory

### tpatch amend was useful
- Used `tpatch amend --append` to add scope to `auto-read-decisions-hooks` after the --all and auto_hooks additions
- Better than creating a separate child feature for what was essentially a scope extension
