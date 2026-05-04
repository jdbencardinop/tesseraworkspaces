# Exploration: go-module-version-fallback

## Relevant Files

- `cmd/tws/main.go` — defines the ldflag-overridable `version` variable and passes it into `internal/cli`.
- `internal/cli/root.go` — constructs the Cobra root command and sets `Version`.
- `Makefile` — injects `main.version` with `git describe --tags --always --dirty` for source installs and runs local lint checks.
- `.github/workflows/ci.yml` — currently installs and runs `tws --version` without checking the value.
- `README.md` and `docs/cheatsheet.md` — document `go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest`.

## Current Behavior

`cmd/tws/main.go` initializes `version` as `dev`. The Makefile overrides it with:

```sh
go install -ldflags "-X main.version=$(git describe --tags --always --dirty)" ./cmd/tws
```

Direct Go module installs do not receive those ldflags, so the binary prints `dev` even when installed from tag `v0.3.0`.

The existing Makefile lint target also uses `gofmt -l . | tee /dev/stderr | (! read)`, which is not portable to `/bin/sh` implementations that require a variable name for `read`. Replace it with an explicit captured `gofmt -l` check.

## Implementation Notes

The version resolver can stay in `cmd/tws` because the ldflag target is `main.version` and Go build metadata is process-level information read by the main package. Tests can avoid depending on real build metadata by passing synthetic `debug.BuildInfo` values into the resolver.

The resolver should treat both an empty module version and `(devel)` as absent. Modern Go versions may embed a VCS-derived main module version for local builds, including a `+dirty` suffix; that is useful build provenance and should be preserved when present.

## Validation Plan

1. Run `gofmt`.
2. Run `go test ./...`.
3. Run `make install` and confirm `tws --version` reports `v0.3.0` in the tagged checkout.
4. Build a local binary without ldflags and confirm it reports embedded Go build info when present, or `dev` when absent.
5. Run `make lint` and confirm the formatting check no longer emits a shell `read` error.
