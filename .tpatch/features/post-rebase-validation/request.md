# Feature Request: Add a configurable validation command (test_command in config) that runs after each successful rebase during tws sync. Example: test_command: go build ./... or test_command: npm run typecheck. Catches syntax errors and type errors before moving to the next branch. If validation fails, treat like a rebase failure — skip descendants. Support per-repo config. Update skills to mention validation.

**Slug**: `post-rebase-validation`
**Created**: 2026-05-26T19:39:23Z

## Description

Add a configurable validation command (test_command in config) that runs after each successful rebase during tws sync. Example: test_command: go build ./... or test_command: npm run typecheck. Catches syntax errors and type errors before moving to the next branch. If validation fails, treat like a rebase failure — skip descendants. Support per-repo config. Update skills to mention validation.
