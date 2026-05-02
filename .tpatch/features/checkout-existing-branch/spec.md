# Specification: checkout-existing-branch
## Acceptance Criteria
1. `tws new` with an existing branch checks it out into the worktree
2. Works even if the branch is currently checked out in main repo
3. New branches still work as before
4. Stack.yaml is updated in both cases
