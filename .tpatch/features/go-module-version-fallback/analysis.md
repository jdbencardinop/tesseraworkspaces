# Analysis: go-module-version-fallback

## Summary

`tws --version` currently reports `dev` for binaries installed with:

```sh
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest
```

The binary reports `dev` because `cmd/tws/main.go` defines `version = "dev"` and only source builds routed through the Makefile inject a tag with `-ldflags "-X main.version=..."`. A direct `go install module@version` does not run this repository's Makefile, so the default value remains.

## Root Cause

1. The Go code has no runtime fallback for Go module build metadata.
2. The Makefile path works because it injects `git describe --tags --always --dirty`.
3. The GitHub Actions smoke test only verifies that `tws --version` exits successfully; it does not fail if the output is `dev`.

## Recommended Approach

Use a hybrid version resolver:

1. Prefer an ldflag-injected version when `main.version` is not `dev`.
2. Fall back to `runtime/debug.ReadBuildInfo().Main.Version` when the binary was installed as a versioned module.
3. Keep `dev` for local unversioned builds.

This preserves the existing Makefile behavior while making the documented `go install ...@latest` path report the module tag.

## Affected Areas

- `cmd/tws/main.go` — resolve the version before passing it to the Cobra root command.
- `internal/cli/root.go` — keep Cobra's built-in `Version` field; optionally set an explicit version template for stable output.
- `Makefile` — keep ldflag version injection and fix the POSIX shell portability issue in `make lint`.
- `.github/workflows/ci.yml` — assert version output rather than only smoke-testing process success.
- `README.md` / `docs/cheatsheet.md` — no install command change required; the fix makes the documented command correct.

## Compatibility

The change is backward-compatible:

1. `make install` still reports the git-derived version because ldflags take precedence.
2. `go install github.com/.../cmd/tws@latest` reports the module version.
3. local `go build ./cmd/tws` from a checkout without ldflags reports Go's embedded module version when available, such as `v0.3.0+dirty` in a dirty tagged checkout, and otherwise falls back to `dev`.
