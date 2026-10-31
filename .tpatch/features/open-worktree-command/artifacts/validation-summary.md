# Pre-land validation: open-worktree-command

Base: `034126331db73522bb98d1f1a1fc08e146a68fe5`.
Reviewed candidate manifest:
`33d4d39e7b6601314efd3153e486bf92535eae15cace6f8266a493adbc6f7a2d`.
Scope: 23 product files, four intent files, and this feature's own artifacts.

## Review and runtime gates

All three independent code-safety, architecture/tests and CLI-contract
reviews approved these exact images after the documented revisions. See
`reviews/independent-review.md` and the adjacent candidate manifest.

The ordinary, unmodified temporary-directory environment passed:

- focused new and legacy open/session regressions (26.839 seconds);
- unfiltered `go test ./... -count=1 -timeout 40m` (1338.557 seconds);
- `go vet ./...`;
- `golangci-lint run ./...` with an isolated cache;
- changed-file formatting, `make build`, `git diff --check`, and
  `tpatch feature deps --validate-all`.

The optional real transport also passed:

```sh
go test -tags=tmux_integration ./internal -run '^TestCheckoutCommandTmux' -count=1 -timeout 5m
go vet -tags=tmux_integration ./internal
golangci-lint run --build-tags=tmux_integration ./internal
```

The independent actual-CLI matrix used real temporary repositories/remotes,
the retained pre-feature binary for fixture setup, actual direct children,
and isolated tmux servers/PTYs. Both workspace modes passed exact no-write
queries, selector/fallback/legacy-path behavior, literal argv, configuration
precedence, runtime redaction, established guarded close, and safe ownership
and cleanup. Added regressions cover semicolons/backslashes, literal tmux
formats with no shell-format job, leading-dash selector escaping, foreign
external intent retention, and pre-publication checkout owner replacement.
Dirty and fast-exit checkout cases retain evidence rather than losing work.

Separate ordinary external and checkout stack smoke passed repeated creation/
sync cycles, real conflict continuation through descendants, unchanged
unselected refs/remotes, and caller restoration.

## Recording and exact replay

Canonical patch SHA-256:
`0b89b8dccd3a2c0b17633f1fda1593395f4f38f676e7072739c35dc6ebefa8a3`.

The original auto-generated recipe receipt remains:
`98cb9becb5684800220e3787d24ecc15b5c1698e2b1d1124eacb2702dd84a59f`.
Exact existing-file preimage guards were then added without changing product
postimages. Effective normalized recipe SHA-256:
`67284c3d44774843359abd69b491d42d881a6e772f4bbdd5970f024ca2835eb8`.

Two independent fresh worktrees at the exact base verified 14 existing
preimages, nine absent new paths, and all 23 postimages after actual
`tpatch apply --mode execute`. `replay-1.json` and `replay-2.json` preserve the
portable receipts. Canonical patch replay in a private Git index independently
matched all 23 postimages and left the real index untouched.

## Boundaries and remaining release gates

Whole-file later-touch advisories are expected for shared files; they do not
authorize rewriting older artifacts. The pre-existing scoped-push parent
verifier reported history-incomplete despite non-shallow/non-partial metadata
and present probed objects. Historical repair remains separate, and no global
verifier-green claim is made.

Pre-existing ordinary-sync/checkout-archive behavior with already-live legacy
agents remains a separate admission follow-up, not a universal exclusion
guarantee added by this feature.

Land only these reviewed images with their binding trailers. After landing,
rerun the full gates and actual committed-binary checks, verify the landing
attestation and clean tree, then require successful exact-head main CI before
an immutable new RC. Both tag versions, release job and non-draft prerelease
metadata must be verified before publication is considered complete.
