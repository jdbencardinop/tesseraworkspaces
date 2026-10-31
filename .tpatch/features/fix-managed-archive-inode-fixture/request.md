# Feature Request: Stabilize the managed archive/reactivation identity regression fixture by retaining the original symlink inode across removal and recreation. Preserve the strict freshness assertion and all production behavior; do not rely on filesystem inode non-reuse to validate link refresh.

**Slug**: `fix-managed-archive-inode-fixture`
**Created**: 2026-10-31T12:08:06Z

## Description

Tracking: [#9](https://github.com/jdbencardinop/tesseraworkspaces/issues/9).
This is the test-only prerequisite for the failed Node 24 RC validation, not
a runtime change to archive, templates, or inode ownership checks.

Stabilize the managed archive/reactivation identity regression fixture by retaining the original symlink inode across removal and recreation. Preserve the strict freshness assertion and all production behavior; do not rely on filesystem inode non-reuse to validate link refresh.
