# Feature Request: Complete the checkout layout repair: Workspace.FeaturePath must return `.tws/features/<feature>` for checkout mode and external paths unchanged; existing checkout lifecycle commands that operate on existing features must use error-returning ResolveFeaturePath to discover legacy `.tws/<feature>` or new layout and reject ambiguity. New add/import writes only new layout. Checkout sync/session state remains `.tws/state`. Add full lifecycle legacy/new regression tests and update skills.

**Slug**: `fix-checkout-feature-path-routing`
**Created**: 2026-08-05T07:27:58Z

## Description

Complete the checkout layout repair: Workspace.FeaturePath must return `.tws/features/<feature>` for checkout mode and external paths unchanged; existing checkout lifecycle commands that operate on existing features must use error-returning ResolveFeaturePath to discover legacy `.tws/<feature>` or new layout and reject ambiguity. New add/import writes only new layout. Checkout sync/session state remains `.tws/state`. Add full lifecycle legacy/new regression tests and update skills.
