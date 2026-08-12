# Feature Request: Research an optional higher-level tws hub that discovers multiple project `.tws` roots and tool-owned sibling spaces (learning, tickets, patching) without becoming their authority. Evaluate whether a registry/index provides enough value before designing a super-workspace hierarchy. Coordinate contracts with tesserapatch, tesseratickets, and learning tools such as /teach.

**Slug**: `workspace-hub-research`
**Created**: 2026-07-22T05:45:39Z

## Description

Research an optional higher-level tws hub that discovers multiple project `.tws` roots and tool-owned sibling spaces (learning, tickets, patching) without becoming their authority. Evaluate whether a registry/index provides enough value before designing a super-workspace hierarchy. Coordinate contracts with tesserapatch, tesseratickets, and learning tools such as /teach.

## Multi-repository composition research

Evaluate whether an optional meta-workspace should coordinate multiple project repositories through Git submodules, a lightweight repository manifest, or plain registered sibling clones. Compare these mechanisms with tws's existing multi-repo worktrees, global workspace registry, and workspace sibling links. Treat submodules as a Git-owned revision-pinning option rather than making tws reimplement submodule semantics. Assess detached-HEAD and branch-tracking behavior, dirty child repositories, recursive clone/update, authentication, pointer conflicts, nested worktrees, CI portability, failure recovery, and whether any cross-repository operation can be made honest without pretending to be atomic. The council should require a concrete workflow that discovery plus links cannot solve before proposing a super-workspace hierarchy. Produce a recommendation and bounded prototype only; do not make submodules mandatory or make tws authoritative for child repository schemas or revision history.
