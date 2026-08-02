# Analysis

The workspace-mode foundation can resolve external versus checkout metadata roots, but no lifecycle command consumes checkout mode yet. Implementing checkout behavior by scattering mode checks would risk external-mode regressions. This feature should introduce a narrow mode-aware lifecycle layer: explicit enablement, local ignored metadata, logical branch creation without linked worktrees, and mode-specific rename/archive/delete/export/import behavior.

Compatibility: external mode remains the default and must keep current paths, worktrees, and command results. Checkout mode is one repository and one physical checkout; sync/open/session behavior is deferred.

Risks: treating any .tws config as checkout, accidental branch deletion, writing metadata before Git validation, mixing runtime state into exports, and altering external behavior.
