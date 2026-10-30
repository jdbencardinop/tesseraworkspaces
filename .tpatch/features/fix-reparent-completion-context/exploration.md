# Reparent completion: implementation map

Base: `54f214f080f82ac66b44e3d16de4cf7f8f285419`.

## Verified code and insertion points

- `internal/cli/stack_reparent.go:146-152`: positional `ValidArgsFunction`
  calls `reparentEntryCandidates`.
- `internal/cli/stack_reparent.go:387-455`: `reparentOntoCompletion` separately
  resolves entry candidates and caller-repository branches. Replace that
  split discovery with one read-only completion context.
- `internal/cli/stack_reparent.go:495-548`: `requireReparentWorkspace` and
  execution's explicit external/checkout layout selection are the precedent.
  Reuse the same helpers without invoking a plan, fetch or mutation route.
- `internal/cli/stack_reparent.go:886-898`: `prepareFreshTarget` demonstrates
  the shared target-repository resolver.
- `internal/reparent_exec.go:203-265`: `ResolveReparentWorkspace` and
  `ResolveReparentTargetRepository` already own these decisions. The latter
  validates logical identity and checkout repository restrictions and proves
  the selected repository locally.
- `internal/cli/sync_modes.go`: `resolveExternalSyncLayout` now retains
  selected-root pending rename/publication checks and valid fallback.
- `internal/cli/stack_reparent_cli_test.go`: existing real fixture helpers,
  closed flag/help contracts and `TestStackReparent_OntoKindCompletion`.
  Prefer focused additions here or a small dedicated completion test file.

## Guardrails and delivery

Do not modify execution's owner-resolution semantics to make completion more
permissive. Do not run the full reparent planner merely to obtain candidates.
Retain one logical closure implementation and existing candidate ordering.
Do not acquire writer reservations in a completion callback.

Update README/cheatsheet and the three embedded guidance surfaces as needed.
Handoff documents and their executable assertions must mark the preceding
release only after parent verification and keep earlier release milestones.
Keep all five forbidden sync-plan literals out of roadmap/workflow prose.

After focused completion, layout, help and no-flag regressions, the parent runs
`go test ./... -count=1 -timeout 40m`, `go vet ./...`,
`golangci-lint run ./...` with an owned cache, `make build`,
`git diff --check` and `tpatch feature deps --validate-all`.
Use real temporary repositories outside the source checkout where permitted;
do not relax assertions to accommodate nested fixture discovery or long paths.

The user authorized a separate Path B feature commit/RC. Implementation is
isolated while rc.8 publication finishes; no landing or publication of this
boundary precedes the verified prior release. Historical dependency-closure
verification limitations remain separately documented, not repaired here.
