# Exploration: fix cutoff test Git identity

## Relevant code

- `internal/cli/checkout_lifecycle_test.go`
  - `setupGitRepoCheckout` and `gitInDir` append fixture-local author and
    committer identity to every Git subprocess.
  - `TestCheckoutNew_PinsCreationRefBeforeItMoves` installs
    `creationResolvedHook` and currently calls `writeAndCommit` from inside the
    hook.
- `internal/cli/new_integration_test.go`
  - `writeAndCommit` uses generic `gitRun`.
  - `gitRun` inherits the process environment and does not provide author or
    committer identity.

## Failure path

The hook runs after `main` is resolved to the creation OID but before branch
creation. Its commit is necessary to move `main` and prove branch creation uses
the pinned OID. On Ubuntu, the generic commit sees no identity and exits 128,
so the race assertions never execute.

The confirmed local red command uses a private global config containing only
`user.useConfigOnly=true` and clears identity environment variables. CLI
TestMain resets `GIT_CONFIG_COUNT`, so the config-file route is the reliable
host-independent reproduction.

## Minimal change

1. In this test, install a temporary config file with
   `user.useConfigOnly=true`, set it as `GIT_CONFIG_GLOBAL`, disable system
   config, and clear inherited identity variables with cleanup.
2. Inside `creationResolvedHook`, write `raced.txt` directly and use
   `gitInDir` for `add` and `commit`.
3. Preserve every existing base-token, resolved-OID, created-branch, and
   `last_base_sha` assertion.

No shared helper, production source, TestMain, or test-count structure needs
to change.

## Focused validation

- Exact hostile-config reproduction for
  `TestCheckoutNew_PinsCreationRefBeforeItMoves`.
- Related checkout creation tests and external creation-ref race test.
- `gofmt`, `git diff --check`, and dependency DAG validation.
