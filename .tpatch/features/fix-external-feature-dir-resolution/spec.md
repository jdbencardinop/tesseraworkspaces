# Specification

1. RequireWorkspace succeeds from source repo, linked worktree, external workspace root, feature dir, and nested feature dirs.
2. External metadata root is the marker directory actually entered, including custom configured paths.
3. Infer the default source repo by sibling <repo>.tws convention or active default-repo worktree metadata; reject ambiguity/missing context clearly.
4. Multi-repo entries retain per-entry Repo handling; default repo inference uses entries with empty Repo.
5. Checkout mode behavior remains unchanged and still requires its repository context.
6. Feature commands stack/sync/decisions/doctor/list/inject work from external feature dir; tests cover stale state abort/continue routing.
