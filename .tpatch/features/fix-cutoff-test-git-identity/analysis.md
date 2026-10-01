# Analysis: fix cutoff test Git identity

## Summary

Ubuntu CI run 36818136245 failed only in
`TestCheckoutNew_PinsCreationRefBeforeItMoves` with `Author identity unknown`;
macOS passed because the host could auto-detect an identity. A local red
reproduction using a private `GIT_CONFIG_GLOBAL` with
`user.useConfigOnly=true` proves the test depends on host identity.

## Root cause

The checkout lifecycle fixture and `gitInDir` helper inject explicit author
and committer identity per Git command. The creation-ref race hook instead
calls generic `writeAndCommit`, whose `gitRun` path does not inject identity.
The failing commit therefore bypasses the fixture contract.

## Compatibility and scope

This is a test-only fix. Replacing the hook commit with the existing
identity-aware helper preserves production behavior and every creation-OID,
moving-ref, and metadata assertion. The regression should also install a
fixture-local hostile Git configuration and clear inherited identity
environment variables so future helper drift cannot pass through host
autodetection.

No production code, TestMain environment policy, host-global Git
configuration, release behavior, or other feature is changed.

## Risk

Low. The main risk is accidentally weakening the moving-base race. The test
must still commit after `creationResolvedHook` receives the frozen OID and
must continue asserting both the created branch and `last_base_sha` equal that
pre-move OID.
