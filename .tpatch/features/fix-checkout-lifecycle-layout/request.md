# Feature Request: Repair the released checkout workspace lifecycle before agent sessions. Wire the documented `tws enable --mode ...` and `tws mode` commands, store new checkout feature metadata under `<repo>/.tws/features/<feature>`, make feature listing mode-aware, and avoid treating `.tws/state`, templates, config, or other internal directories as features. Preserve backward compatibility for v1.2.2/v1.2.3 users with legacy `<repo>/.tws/<feature>` metadata through explicit legacy discovery/migration behavior. Keep external workspace paths unchanged. Update embedded skills and lifecycle tests.

**Slug**: `fix-checkout-lifecycle-layout`
**Created**: 2026-08-05T04:09:00Z

## Description

Repair the released checkout workspace lifecycle before agent sessions. Wire the documented `tws enable --mode ...` and `tws mode` commands, store new checkout feature metadata under `<repo>/.tws/features/<feature>`, make feature listing mode-aware, and avoid treating `.tws/state`, templates, config, or other internal directories as features. Preserve backward compatibility for v1.2.2/v1.2.3 users with legacy `<repo>/.tws/<feature>` metadata through explicit legacy discovery/migration behavior. Keep external workspace paths unchanged. Update embedded skills and lifecycle tests.
