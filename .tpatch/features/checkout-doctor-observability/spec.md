# Specification

1. Doctor reports checkout workspace ID/mode/repo/metadata root and current attached/detached branch.
2. Report dirty state and active Git merge/rebase/cherry-pick/revert.
3. Report checkout sync transaction/lock stage, failed/current branch, stale/live lock, and recovery command.
4. Report checkout agent session/lock, direct PID or tmux liveness, logical owner, and stale/recovery guidance.
5. For each feature/entry report short name, GitBranch, archive state, ref existence, current selection, base and ancestry current/stale/divergent/missing.
6. Validate recorded context links and expected targets; warn on missing/replaced links.
7. `tws list` uses checkout terminology (logical branch/current/session) rather than active worktree count.
8. External doctor/list output and behavior remain unchanged.
9. Read-only: doctor never mutates locks/state/Git.
