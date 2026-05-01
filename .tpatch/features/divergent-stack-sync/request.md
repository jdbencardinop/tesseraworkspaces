# Feature Request: The stack.yaml data model should support DAG dependencies from day one (A->B, A->C divergent stacks), but only implement linear sync initially. Divergent sync (rebasing B and C independently from A) is a future feature. The data model cost of supporting DAGs upfront is minimal, and retrofitting it later into a linear-only schema would be painful.

**Slug**: `divergent-stack-sync`
**Created**: 2026-05-01T09:03:44Z

## Description

The stack.yaml data model should support DAG dependencies from day one (A->B, A->C divergent stacks), but only implement linear sync initially. Divergent sync (rebasing B and C independently from A) is a future feature. The data model cost of supporting DAGs upfront is minimal, and retrofitting it later into a linear-only schema would be painful.
