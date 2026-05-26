# Feature Request: When a base branch has amended commits, downstream branches hold stale SHAs that create ghost conflicts during rebase. Track last_base_sha per branch in stack.yaml. On sync, use git rebase --onto to only replay commits unique to each branch, skipping the stale base commits entirely. This avoids conflicts from amends, fixups, and interactive rebases on parent branches. Medium effort but high value for stacked PR workflows where amends from code review are common. Do NOT recommend users avoid amending — the tool should handle any git workflow gracefully.

**Slug**: `amend-aware-rebase`
**Created**: 2026-05-26T23:22:49Z

## Description

When a base branch has amended commits, downstream branches hold stale SHAs that create ghost conflicts during rebase. Track last_base_sha per branch in stack.yaml. On sync, use git rebase --onto to only replay commits unique to each branch, skipping the stale base commits entirely. This avoids conflicts from amends, fixups, and interactive rebases on parent branches. Medium effort but high value for stacked PR workflows where amends from code review are common. Do NOT recommend users avoid amending — the tool should handle any git workflow gracefully.
