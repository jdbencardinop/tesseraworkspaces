# Specification: fix cutoff test Git identity

## Problem

The checkout creation-ref race regression performs its hook commit through a
generic test helper that does not inject Git identity. Ubuntu CI has no
discoverable identity and fails before exercising the intended race.

## Acceptance criteria

1. `TestCheckoutNew_PinsCreationRefBeforeItMoves` performs the hook's file
   write, add, and commit through the existing fixture-local identity-aware Git
   path.
2. The test installs a temporary `GIT_CONFIG_GLOBAL` containing only
   `user.useConfigOnly=true`, disables system config, and clears inherited
   author/committer/email environment variables for its own lifetime.
3. The original race remains exact: the base ref advances only after the
   creation OID is resolved, while the created branch and recorded
   `last_base_sha` both remain pinned to the original full OID.
4. No production source, CLI TestMain, real global Git configuration, or
   unrelated counted test structure changes.
5. The confirmed hostile-config reproduction command passes, along with the
   related checkout/external creation race tests, `gofmt`, `git diff --check`,
   and `tpatch feature deps --validate-all`. Required repository-wide test,
   vet, lint, and build gates also pass before landing.

## Implementation plan

1. Add a small fixture-local identity-isolation setup in
   `internal/cli/checkout_lifecycle_test.go`.
2. Replace the race hook's generic `writeAndCommit` call with an explicit file
   write plus `gitInDir` add/commit.
3. Run the exact red reproduction and focused creation tests; save evidence
   outside the source tree.

## Out of scope

- Production Git identity behavior.
- CLI TestMain or host/global configuration changes.
- Release behavior changes, other cutoff behavior, reparent test-budget
  refactors, Lane B, or issues #1/#3/#2.
