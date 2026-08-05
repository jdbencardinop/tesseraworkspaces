# Specification

1. Workspace.FeaturePath returns new layout in checkout and unchanged external path.
2. New checkout add/import always write `.tws/features/<feature>`.
3. Existing feature operations resolve new or legacy via ResolveFeaturePath and reject dual-layout ambiguity.
4. Checkout transaction state always stays `.tws/state` for both layouts.
5. All checkout lifecycle commands and decisions/list/doctor paths are mode-aware.
6. External paths/behavior unchanged.
