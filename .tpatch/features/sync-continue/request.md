# Feature Request: Add tws sync <feature> --continue to resume after conflict resolution. Persist sync state (failed branch, pending branches) in a .sync-state.yaml in the feature dir. After the user resolves conflicts and runs git rebase --continue in the worktree, tws sync --continue picks up where it left off and processes remaining branches. Print clear guidance on conflict: "Resolve in <worktree>, then run tws sync <feature> --continue". Update skills to document this workflow.

**Slug**: `sync-continue`
**Created**: 2026-05-26T19:35:55Z

## Description

Add tws sync <feature> --continue to resume after conflict resolution. Persist sync state (failed branch, pending branches) in a .sync-state.yaml in the feature dir. After the user resolves conflicts and runs git rebase --continue in the worktree, tws sync --continue picks up where it left off and processes remaining branches. Print clear guidance on conflict: "Resolve in <worktree>, then run tws sync <feature> --continue". Update skills to document this workflow.
