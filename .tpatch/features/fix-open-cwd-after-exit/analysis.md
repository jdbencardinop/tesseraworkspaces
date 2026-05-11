# Analysis: fix-open-cwd-after-exit
## The problem
syscall.Exec replaces the tws process with the agent. When the agent exits, control returns to the parent shell whose cwd never changed.
## The fix
Use exec.Command subprocess instead of syscall.Exec. Set cmd.Dir to the worktree path. After the agent exits, spawn an interactive shell in the worktree dir so the user stays there.
