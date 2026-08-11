# Feature Request: Extract one mode-independent, read-only stack edge evaluator and integrate it into tws doctor. For every configured parent-child edge, use StackEntry.GitBranch(), literal base refs, the current parent head, and LastBaseSHA to classify current, stale, divergent, missing, or cross-repo; report the relevant heads/merge base/last base plus actionable guidance. Support external and checkout modes without fetching or mutating Git, preserve doctor exit semantics, and expose a reusable projection for stack status and later additive tws status enrichment.

**Slug**: `stack-ancestry-doctor`
**Created**: 2026-08-11T17:39:15Z

## Description

Extract one mode-independent, read-only stack edge evaluator and integrate it into tws doctor. For every configured parent-child edge, use StackEntry.GitBranch(), literal base refs, the current parent head, and LastBaseSHA to classify current, stale, divergent, missing, or cross-repo; report the relevant heads/merge base/last base plus actionable guidance. Support external and checkout modes without fetching or mutating Git, preserve doctor exit semantics, and expose a reusable projection for stack status and later additive tws status enrichment.
