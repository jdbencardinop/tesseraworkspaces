# Specification

## Acceptance Criteria

1. `tws init --mode checkout` or `tws enable --mode checkout` writes repository-local `.tws/config.yaml` with explicit mode and stable workspace identity, creates `.tws/features/` and `.tws/state/`, and adds `/.tws/` to `.git/info/exclude` by default.
2. `tws mode` reports the resolved mode and metadata root; legacy/no-mode repos remain external.
3. Checkout-mode `tws add` stores feature metadata under `.tws/features/<feature>`.
4. Checkout-mode `tws new` creates or registers logical Git branches and stack entries without creating linked worktree directories. Explicit base semantics remain literal and repo-scoped.
5. Rename/archive/delete are mode-aware and metadata-atomic. They preserve Git branches by default; destructive branch deletion requires an explicit flag.
6. Export/import include durable feature metadata and exclude `.tws/state/` runtime files.
7. Checkout mode rejects multi-repo entries and unsupported worktree-only operations clearly.
8. Existing external-mode behavior and integration tests remain unchanged.

## Out of Scope

- Branch switching/sync transactions in one checkout.
- Agent/tmux session activation.
- Mode conversion between external and checkout.
- Multiple repositories or simultaneous branch-owning agents in checkout mode.
