# Analysis: workspace-templates
## Summary
When tws add creates a feature, copy files from a template directory into inject/. Templates live at ~/.config/tws/templates/ (global) or .tws/templates/ (per-repo). Per-repo overrides global.
## Affected Areas
- `internal/cli/add.go` — copy template into inject/ on feature creation
- `internal/config.go` — template path resolution
