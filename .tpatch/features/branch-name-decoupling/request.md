# Feature Request: Decouple worktree directory name from git branch name. Many repos enforce branch naming policies (e.g., user/feature/name). Currently stack.yaml name == git branch name, causing: nested worktree dirs when branch has slashes, broken symlink depths, rename destroys untracked content. Add branch_prefix config or name mapping in stack.yaml (display_name + branch_name). Keep worktree dirs flat by sanitizing slashes. Make rename preserve untracked files (move vs re-checkout). Fix doctor/list to handle nested dirs correctly.

**Slug**: `branch-name-decoupling`
**Created**: 2026-07-01T04:27:43Z

## Description

Decouple worktree directory name from git branch name. Many repos enforce branch naming policies (e.g., user/feature/name). Currently stack.yaml name == git branch name, causing: nested worktree dirs when branch has slashes, broken symlink depths, rename destroys untracked content. Add branch_prefix config or name mapping in stack.yaml (display_name + branch_name). Keep worktree dirs flat by sanitizing slashes. Make rename preserve untracked files (move vs re-checkout). Fix doctor/list to handle nested dirs correctly.
