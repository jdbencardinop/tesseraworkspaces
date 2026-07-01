# Feature Request: Default --base is hardcoded to "main" but many repos use "master" or other default branches. Detect the actual default branch from origin/HEAD (git rev-parse --abbrev-ref origin/HEAD) and use that as the default. Warn if the given base branch doesn't exist.

**Slug**: `fix-default-base-branch`
**Created**: 2026-07-01T03:53:18Z

## Description

Default --base is hardcoded to "main" but many repos use "master" or other default branches. Detect the actual default branch from origin/HEAD (git rev-parse --abbrev-ref origin/HEAD) and use that as the default. Warn if the given base branch doesn't exist.
