# Analysis: versioned-builds

## Summary

Add `--version` and `--help` flags to the tws CLI. Inject version at build time via ldflags from git tags. Split CI so tests/lint run on every push, but releases only trigger on `v*` tags.

## Affected Areas

- `cmd/tws/main.go` — add `--version` and `--help` flag handling, version variable
- `Makefile` — add ldflags to inject version from `git describe`
- `.github/workflows/ci.yml` — already mostly correct, just needs the install smoke test with `--version`

## Acceptance Criteria

1. `tws --version` prints the version
2. `tws --help` prints available commands
3. `make build` injects version from git tags via ldflags
4. `go install` without ldflags shows "dev" as version
5. CI runs tests on push/PR, releases only on tags
