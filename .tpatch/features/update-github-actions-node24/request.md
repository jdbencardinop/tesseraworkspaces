# Feature Request: Update GitHub Actions dependencies to versions that natively support Node.js 24. Current CI is green, but checkout@v4 and setup-go@v5 emit deprecation warnings because GitHub forces their Node 20 runtime to Node 24. Verify replacement action versions, preserve Go caching/version-file behavior, and keep the Linux/macOS/tag/release matrix green.

**Slug**: `update-github-actions-node24`
**Created**: 2026-08-05T01:48:41Z

## Description

Update GitHub Actions dependencies to versions that natively support Node.js 24. Current CI is green, but checkout@v4 and setup-go@v5 emit deprecation warnings because GitHub forces their Node 20 runtime to Node 24. Verify replacement action versions, preserve Go caching/version-file behavior, and keep the Linux/macOS/tag/release matrix green.
