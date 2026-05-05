# Feature Request: When using tws open without tmux, exiting the agent leaves the user at the repo root instead of the worktree dir. syscall.Exec replaces the process so the parent shell cwd is unchanged. Fix by using exec.Command with Dir set, or consider a shell wrapper approach like worktrunk's shell install. The user should end up in the worktree directory after the agent exits.

**Slug**: `fix-open-cwd-after-exit`
**Created**: 2026-05-05T06:49:14Z

## Description

When using tws open without tmux, exiting the agent leaves the user at the repo root instead of the worktree dir. syscall.Exec replaces the process so the parent shell cwd is unchanged. Fix by using exec.Command with Dir set, or consider a shell wrapper approach like worktrunk's shell install. The user should end up in the worktree directory after the agent exits.
