# Analysis: fix-auto-select-behavior
## The problem
Single feature + single branch auto-selects silently. User doesn't know what was picked.
## The fix
Print a message when auto-selecting. Verify the worktree path is correct.
