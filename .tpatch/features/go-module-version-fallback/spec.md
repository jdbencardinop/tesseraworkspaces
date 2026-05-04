# Specification: go-module-version-fallback

## Goal

Make `tws --version` and `tws -v` report the correct release version for the documented Go module install path while preserving source-build version injection.

## Acceptance Criteria

1. `make install` from a tagged checkout continues to report the git tag, e.g. `tws version v0.3.0`.
2. A binary built as an installed module version reports `debug.BuildInfo.Main.Version` when no ldflag version was injected.
3. A local development build without ldflags reports embedded Go module build info when Go provides it, or `dev` when build info has no usable version.
4. Cobra remains the source of `--version` / `-v` behavior through the root command `Version` field.
5. CI fails if a tag build reports `dev` for `tws --version`.
6. `make lint` uses POSIX-compatible shell syntax and reliably fails when `gofmt -l .` reports files.
7. Existing tests continue to pass.

## Minimal Changeset

1. `cmd/tws/main.go` — add a small `resolveVersion` helper using `runtime/debug.ReadBuildInfo`.
2. `cmd/tws/main_test.go` — cover injected, module-build-info, devel-build-info, and missing-build-info cases.
3. `Makefile` — replace the shell-specific `read` formatting check with an explicit `gofmt -l` result check.
4. `.github/workflows/ci.yml` — assert non-`dev` version output on tag builds while keeping normal branch/PR smoke tests.

## Non-goals

1. Do not hardcode release versions in source.
2. Do not introduce GoReleaser or binary artifact publishing in this feature.
3. Do not change the documented install command.
4. Do not add commit/date output unless a future feature asks for expanded build metadata.
