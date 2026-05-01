# Tracked Features

| Slug | Title | State | Compatibility |
|------|-------|-------|---------------|
| `auto-detect-repos-workspaces` | We should try and see if we are already in a git repo to define if we should use the default workdir or a custom one for the current repo, and in case we are inside of a worktree we can work backwards and make ts work the same in every of the workspaces folders | analyzed | unknown |
| `keep-track-of-stacked-diffs-and-dependencies` | Our sync method currently just rebases every single branch we have a worktree for, but this might not be what we want, we might just need to advance some of the worktrees or take just changes in one worktree, check if we need one worktree per stack or one worktree per feature, we need to implement a better approach to handling a graph of dependencies in the changes | requested | unknown |
| `lightweight-worktree-handler` | We should have a way to work with worktrees that suits our way of modeling our features, in which a feature folder can have many worktrees, so not all worktrees in the repo are in the same folder, worktrunk might do that out of the box, so we should also document how to do that in worktrunk | requested | unknown |
| `user-defined-workdir` | Currently we use a default working dir for everythin under /Users/jbencardino/ts, but we should have a workspace per repo and let the user configure where to set it up | applied | unknown |
