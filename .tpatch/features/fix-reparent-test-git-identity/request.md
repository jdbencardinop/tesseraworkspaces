# Feature Request: Make safe-reparent integration fixtures provide test-scoped Git author and committer identity to production Git child processes, not only fixture-side commands. Ubuntu CI currently fails rebase execution when the host lacks a usable committer identity. Preserve production behavior and user Git configuration, add hostile-host regression coverage in both workspace modes, and restore green main CI before releasing Lane A.

**Slug**: `fix-reparent-test-git-identity`
**Created**: 2026-09-26T05:31:02Z

## Description

Make safe-reparent integration fixtures provide test-scoped Git author and committer identity to production Git child processes, not only fixture-side commands. Ubuntu CI currently fails rebase execution when the host lacks a usable committer identity. Preserve production behavior and user Git configuration, add hostile-host regression coverage in both workspace modes, and restore green main CI before releasing Lane A.
