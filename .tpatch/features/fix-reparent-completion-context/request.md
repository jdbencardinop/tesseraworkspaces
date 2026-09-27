# Feature Request: Make tws stack reparent shell completion use the same resolved feature layout and target-entry repository as explicit execution. When TWS_ROOT differs from Workspace.MetadataRoot, suggest entries from the selected external feature; for multi-repository stacks, suggest Git destinations from the target entry repository rather than Workspace.RepoRoot. Preserve target/descendant exclusion, logical versus Git branch names, checkout-mode routing, silent unavailable-context completion, and read-only/no-fetch behavior. Explicit command execution remains protected; this is a nonblocking completion-only follow-up from safe-reparent-restack certification.

**Slug**: `fix-reparent-completion-context`
**Created**: 2026-09-27T09:26:23Z

## Description

Make tws stack reparent shell completion use the same resolved feature layout and target-entry repository as explicit execution. When TWS_ROOT differs from Workspace.MetadataRoot, suggest entries from the selected external feature; for multi-repository stacks, suggest Git destinations from the target entry repository rather than Workspace.RepoRoot. Preserve target/descendant exclusion, logical versus Git branch names, checkout-mode routing, silent unavailable-context completion, and read-only/no-fetch behavior. Explicit command execution remains protected; this is a nonblocking completion-only follow-up from safe-reparent-restack certification.
