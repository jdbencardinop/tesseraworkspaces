# Feature Request: Implement safe stack synchronization for checkout workspace mode using the single physical Git checkout. Enforce a clean attached checkout, acquire an operation lock, persist a transaction with original branch/HEAD and ordered plan, sequentially switch and rebase logical branches, restore the original branch on success and abort, and support interruption/conflict recovery through sync --continue/--abort. Reuse explicit-base semantics, amend-aware rebasing, validation commands, and final ancestry checks. Preserve external sync behavior exactly. Update embedded skills when behavior stabilizes.

**Slug**: `checkout-stack-safety`
**Created**: 2026-08-03T04:03:32Z

## Description

Implement safe stack synchronization for checkout workspace mode using the single physical Git checkout. Enforce a clean attached checkout, acquire an operation lock, persist a transaction with original branch/HEAD and ordered plan, sequentially switch and rebase logical branches, restore the original branch on success and abort, and support interruption/conflict recovery through sync --continue/--abort. Reuse explicit-base semantics, amend-aware rebasing, validation commands, and final ancestry checks. Preserve external sync behavior exactly. Update embedded skills when behavior stabilizes.
