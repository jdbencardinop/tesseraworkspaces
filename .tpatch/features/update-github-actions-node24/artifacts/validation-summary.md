# Candidate validation summary

Baseline: `6114e4f4dadb111c5fd47c11bd6f4d96e195c67a`.
Candidate manifest:
`a15518fad514f280db6bf02d353915e041c113ca2d3d56bfc74cac5337c6a0fa`.
Scope: two product files and four unchanged intent documents.

## Completed candidate checks

- Complete parsed-workflow comparison passed after only the four approved
  action-reference substitutions and `cache-dependency-path: go.sum`.
  All other parsed fields, commands, conditions, permissions and triggers match
  the baseline. Selected upstream metadata declares Node 24 and the required
  inputs.
- Existing CLI documentation/guidance checks passed.
- `gofmt -l internal cmd` reported no files.
- `make build`, `git diff --check`, and global tpatch DAG validation passed.
- `go test ./... -count=1 -timeout 40m` passed in 1304.225 seconds.
- `go vet ./...` and full `golangci-lint run ./...` with an isolated cache passed.
- Real temporary-repository external and checkout smoke cases passed through
  multiple create/sync cycles and a real conflict resumed through descendants.
  Unselected refs and caller checkout were preserved; local-only sync did not
  mutate the local bare remote.

## Record and replay

Canonical patch:
`e9ccbeb105001b6d13f280bba78ca4602ed9c10e53ea470bedc6af572ed44196`.

The automatically generated recipe originally had hash
`6a23e1aeccd2552328bca7f4373b665827128cf28d2b834bec5dbfba4cb46a98`.
It was normalized to include exact existing-file preimage hashes; the current
recipe hash is
`94dabf1fd4b323f3839bd33106689d2fd999a8895df10695ec779310acbe48a4`.
The original generation receipt is retained as historical evidence of the
automatic generation, not rewritten to claim it generated the normalized form.

Two fresh exact-base worktrees independently replayed the normalized recipe.
Each verified two existing preimages, zero new paths, and both final postimages.
An independent private-index replay of the canonical patch matched the same
two postimages without using the real index. The reviewed candidate was
unchanged after every check.

The record step's later-touch warnings for the older whole-file owners
`versioned-builds` and `agent-work-status-dashboard` are expected. This bounded
later feature changes their shared files without rewriting old generations or
inventing dependency edges merely to silence warnings.

## Remaining publication gates

Land exactly this patch/recipe with one feature commit, then repeat full gates
on the committed main tree. Hosted Ubuntu/macOS main CI, subsequent RC tag CI,
exact-tag version checks, and actual release metadata are required before
publication is declared complete. This candidate summary does not pre-certify
those future results.
