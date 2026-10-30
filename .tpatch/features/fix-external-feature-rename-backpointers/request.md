# Feature Request: Fix GitHub issue #5: keep every affected linked Git worktree correctly registered when an external feature is renamed. Preserve dirty worktree and index content, branch refs, managed ownership, unrelated worktrees, and checkout behavior; coordinate exact pointer updates and spaces metadata with safe verified rollback and explicit partial-failure evidence.

**Slug**: `fix-external-feature-rename-backpointers`
**GitHub issue**: [#5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5)
**Created**: 2026-10-12T00:08:52Z

## Description

Fix GitHub issue #5: keep every affected linked Git worktree correctly registered when an external feature is renamed. Preserve dirty worktree and index content, branch refs, managed ownership, unrelated worktrees, and checkout behavior; coordinate exact pointer updates and spaces metadata with safe verified rollback and explicit partial-failure evidence.

## Approved preparation scope

This is a new implementation boundary, not a rewrite of the shipped
`rename-feature-worktree`, `fix-atomic-rename`, or template features.
The planning baseline is `e578451` after the verified `v1.2.18-rc.6` release.
Implementation starts at `6819a4e` after separately approved status-test
fixture prerequisites (`d8e821b`, `6819a4e`); no production status behavior
or deadline changed.

The user approved narrowly scoped, verified Git pointer updates, emphasizing
that successful completion must leave no dangling Git references. An isolated
probe confirmed that `git worktree repair <path>` can repair an unrelated
sibling, so it is not a suitably scoped mutation primitive here.

The first slice protects future external feature renames, including legacy
and managed features. It does not repair arbitrary layouts broken by older
releases. Preserve ordinary staged, unstaged, and untracked work; refuse active
Git operations, sessions, locks, ambiguous identities, and unsupported
repository/submodule relocation.

On failure, attempt rollback only when exact checks prove it safe. Otherwise
preserve durable evidence and expose explicit journal-bound continuation and
abort for this implementation's own interrupted operations. Neither recovery
verb may adopt unknown state or overwrite a newly occupied path.

Analysis, specification, and exploration are complete. The user has now
authorized Path B implementation, with the required isolated review,
validation, record/replay, and single-feature landing process.
Pushing or tagging still requires separate approval.
