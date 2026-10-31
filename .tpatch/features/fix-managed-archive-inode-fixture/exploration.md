# Exploration: fixture retention insertion point

- `internal/cli/named_template_final_regression_test.go`, function
  `TestManagedTemplateArchiveReactivateRefreshesLinkIdentity`: the original
  device/inode is read before archive; the new ownership is later compared
  strictly against it. Insert retention after confirming the original identity
  was recorded and before calling `archiveCmd`.
- `internal/cli/inject_test.go`: `injectFixture.root` is an owned temporary
  directory containing separate `repo` and `workspace` children. Retention at
  its root stays on the same filesystem but outside Git worktree removal.
- `internal/template_managed_test.go`, the same-target replacement test:
  existing inode-retention precedent, landed as `e578451`. Its rename mechanism
  cannot be copied literally here because archive must see the original path.
- The existing `golang.org/x/sys/unix` dependency exposes `Linkat` on the
  supported platforms. Zero flags retain the symlink rather than following it;
  this is required after `os.Link` failed the macOS retained-inode precondition.

Base: `caee4aa4aaec7154d35c3e5ffe29faf6de3a146a`.
Hard parent: `named-feature-templates`, whose lifecycle regression is tested.
Soft parent: `update-github-actions-node24`, the validation boundary exposing
the failure; it is not a semantic dependency of the fixture itself.

Use an isolated worktree. Keep the product patch to this one test file,
preserve all original assertions, and retain the failed CI evidence outside the
source tree alongside the new focused/full/replay receipts.
