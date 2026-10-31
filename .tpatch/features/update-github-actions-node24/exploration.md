# Exploration: CI maintenance insertion points

## Existing surfaces

- `.github/workflows/ci.yml`: two checkout references, one setup-go reference,
  and one release reference. The setup `with` block is the insertion point for
  `cache-dependency-path: go.sum`. The remaining workflow must be identical.
- `go.mod` / `go.sum`: one root module, Go 1.26.1, no toolchain directive.
  These files must not change.
- `Makefile` and `cmd/tws/main.go`: source builds use tag-derived ldflags and
  versioned installs retain module-version fallback. Do not edit version code.
- `CHANGELOG.md`: add the new maintenance entry under `Unreleased`; preserve
  the now-completed stable v1.2.18 section and older component disclosures.
- `docs/engineering-workflow.md`, `docs/roadmap.md`, and
  `docs/v1.2-rc-validation.md`: the separately committed handoff already records
  stable v1.2.18 and the Node24-then-open sequence. No further planning-prose
  changes are needed for the version-only candidate.
- `internal/cli/sync_plan_docs_test.go`: existing prose contracts cover the
  completed stable record and current roadmap target. Preserve those tests.

## Validation strategy

The project has no existing action-runtime unit-test framework. Do not add Go
tests that merely assert version constants. Use the existing YAML dependency to
compare the complete parsed workflow with the narrowly transformed baseline,
and use real GitHub main/tag jobs to establish runtime execution and release
compatibility. The upstream source proves the changed default cache input and
the preserved explicit override.

Run the existing focused CLI documentation selectors, the full Go suite, vet,
lint, build, diff and DAG gates in an ordinary environment. Reuse the existing
real temporary-repository smoke harness for external and checkout stack cycles
and actual completion/publication checks. The CI candidate changes no tws
runtime behavior; smoke validates that the baseline remains intact.

## Isolation and artifacts

Implementation worktree: `feature/update-github-actions-node24`, based on
`6114e4f4dadb111c5fd47c11bd6f4d96e195c67a`.

Keep product paths limited to the workflow and changelog. Keep request,
analysis, specification, exploration, generated artifacts and validation
receipts under this feature's own metadata. Use the established exact-base
recipe/patch replay and one `tpatch land` commit; do not land the following
open-command feature or advance any unrelated requested feature.
