# Feature Request: Add tws stack status <feature> [--json] while preserving the existing tws stack <feature> dependency tree. Use the shared stack ancestry evaluator to show each logical and Git branch, local head, configured parent/base and parent head, LastBaseSHA, ancestry state, materialization, dirty/rebase state, upstream, and ahead/behind counts. Keep it deterministic, read-only, local-only by default with no implicit fetch, mode-aware for external worktrees and checkout logical branches, and explicit about literal refs and cross-repo entries.

**Slug**: `stack-status`
**Created**: 2026-08-11T17:39:15Z

## Description

Add tws stack status <feature> [--json] while preserving the existing tws stack <feature> dependency tree. Use the shared stack ancestry evaluator to show each logical and Git branch, local head, configured parent/base and parent head, LastBaseSHA, ancestry state, materialization, dirty/rebase state, upstream, and ahead/behind counts. Keep it deterministic, read-only, local-only by default with no implicit fetch, mode-aware for external worktrees and checkout logical branches, and explicit about literal refs and cross-repo entries.
