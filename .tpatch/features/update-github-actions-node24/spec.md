# Specification: Node 24-native CI maintenance

## Problem

The project relies on forced compatibility for Node 20 actions. Upgrade every
JavaScript action used by the CI/release workflow to a maintained native
Node 24 line without changing the product or its validation/release contract.

## Acceptance criteria

1. Both checkout steps use `actions/checkout@v7`, setup uses
   `actions/setup-go@v7`, and release uses `softprops/action-gh-release@v3`.
   The selected upstream metadata declares Node 24.
2. Setup retains `go-version-file: go.mod` and `cache: true`, and explicitly
   sets `cache-dependency-path: go.sum` to preserve the old invalidation input.
   No Go version, module dependency, or cache-disable change is allowed.
3. Apart from those action references and the one explicit cache input, the
   parsed workflow is identical to the baseline: triggers, branch/tag filters,
   concurrency, permissions, matrix, job/step conditions, fetch depth, build
   and test commands, timeout, and exact-tag version script are unchanged.
4. Hosted Ubuntu/macOS runner versions satisfy the documented Node 24 minimum.
   Do not add an unsafe fork-checkout override or a Node compatibility
   environment override.
5. Run focused documentation checks and the normal full gates:
   `go test ./... -count=1 -timeout 40m`, vet, golangci-lint, build, formatting,
   diff and DAG checks. Keep real-Git smoke coverage of the existing CLI in
   ordinary temporary repositories.
6. The final candidate has a direct review, exact preimages/postimages, two
   fresh exact-base recipe replays, and matching canonical patch replay.
   Keep the published v1.2.18 artifacts immutable.
7. After one traceable tpatch landing and post-land gates, main CI succeeds on
   both platforms using the upgraded actions. Then the next RC tag/release CI
   succeeds, both tag jobs print the exact RC version, and the release remains
   a non-draft prerelease. Stable v1.2.18 stays unchanged.
8. Update the unreleased changelog with the maintenance scope. Keep the
   historical v1.2.18 release record and the separate open-command backlog
   intact. Close issue #8 only after verified RC publication.

## Implementation plan

1. Edit only `.github/workflows/ci.yml` and the new maintenance changelog entry.
2. Compare parsed before/after workflow documents after applying only the four
   reference substitutions and the explicit dependency-path addition to the
   expected baseline. Verify selected upstream runtime and input metadata.
3. Run the focused/full gates, record and normalize artifacts, and replay them
   at the exact baseline in isolated worktrees.
4. Land, repeat gates, and verify the main/tag/release matrix before advancing
   to open-command implementation.

## Non-goals

No runtime Go code, Go dependency, CLI flag, workspace state, template behavior,
session behavior, workflow trigger, permission expansion, runner-label change,
test-timeout change, new stable promotion, or unrelated supply-chain policy
change is included. Major-tag pinning remains the established convention.
