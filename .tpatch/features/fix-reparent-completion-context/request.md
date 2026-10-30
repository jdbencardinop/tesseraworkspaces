# Feature Request: Make tws stack reparent shell completion use the same resolved feature layout and target-entry repository as explicit execution. When TWS_ROOT differs from Workspace.MetadataRoot, suggest entries from the selected external feature; for multi-repository stacks, suggest Git destinations from the target entry repository rather than Workspace.RepoRoot. Preserve target/descendant exclusion, logical versus Git branch names, checkout-mode routing, silent unavailable-context completion, and read-only/no-fetch behavior. Explicit command execution remains protected; this is a nonblocking completion-only follow-up from safe-reparent-restack certification.

**Slug**: `fix-reparent-completion-context`
**Created**: 2026-09-27T09:26:23Z

## Description

Make tws stack reparent shell completion use the same resolved feature layout and target-entry repository as explicit execution. When TWS_ROOT differs from Workspace.MetadataRoot, suggest entries from the selected external feature; for multi-repository stacks, suggest Git destinations from the target entry repository rather than Workspace.RepoRoot. Preserve target/descendant exclusion, logical versus Git branch names, checkout-mode routing, silent unavailable-context completion, and read-only/no-fetch behavior. Explicit command execution remains protected; this is a nonblocking completion-only follow-up from safe-reparent-restack certification.

Track GitHub issue #7. Preserve the execution-selected context and existing completion eligibility/order without changing reparent execution. Autonomous Path B implementation/review/landing and a separate RC after successful CI are authorized. Work is isolated while rc.8 tag CI runs; do not land or publish this boundary until the scoped-push release is verified.
