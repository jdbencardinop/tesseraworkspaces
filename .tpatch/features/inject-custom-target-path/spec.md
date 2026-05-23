# Specification: inject-custom-target-path
## Minimal Changeset
1. `internal/config.go` — InjectInto field
2. `internal/inject.go` — target prefix support
3. `internal/cli/inject.go` — --into flag
4. `internal/cli/new.go` — use config inject_into
5. `internal/cli/open.go` — use config inject_into
