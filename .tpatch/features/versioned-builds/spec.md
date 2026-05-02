# Specification: versioned-builds

## Minimal Changeset

1. `cmd/tws/main.go` — version var + `--version`/`--help` handling
2. `Makefile` — ldflags for version injection
3. `.github/workflows/ci.yml` — add smoke test with `--version`
