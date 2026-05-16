# Analysis: template-sync-existing
## Summary
Add --template flag to tws add for external template dirs. Add tws template sync for backfilling templates into existing features. Skip conflicting files for now (conflict resolution is a separate feature).
## Affected Areas
- `internal/cli/add.go` — add --template flag, support multiple
- New: `internal/cli/template.go` — tws template sync command
- `internal/cli/root.go` — register template command
