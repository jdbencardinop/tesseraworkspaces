# Analysis: Node 24-native CI actions

## Summary and upstream status

The requested maintenance is not present in the project baseline
`6114e4f4dadb111c5fd47c11bd6f4d96e195c67a`. `.github/workflows/ci.yml` still uses
checkout v4 twice, setup-go v5, and action-gh-release v2. All three action
families have maintained Node 24 releases. This is a workflow dependency change,
not a Go, CLI, workspace, or runtime-state change.

## Verified upstream selections

Preserve the repository's existing major-tag reference convention:

| Action | Selected major / current release | Observed commit | Runtime |
| --- | --- | --- | --- |
| `actions/checkout` | v7 / v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` | node24 |
| `actions/setup-go` | v7 / v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` | node24 |
| `softprops/action-gh-release` | v3 / v3.0.3 | `efb35369e0ad2afab669f228072c1b0d510eae64` | node24 |

Each major resolved to the same commit as its stated stable release at
inspection. Runtime declarations were read from the selected `action.yml`
files. This records the observation, not a promise that moving major tags are
immutable.

Sources:

- [checkout metadata](https://github.com/actions/checkout/blob/3d3c42e5aac5ba805825da76410c181273ba90b1/action.yml)
  and [migration notes](https://github.com/actions/checkout/blob/3d3c42e5aac5ba805825da76410c181273ba90b1/README.md).
- [setup-go metadata](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/action.yml)
  and [migration notes](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/README.md).
- [release action metadata](https://github.com/softprops/action-gh-release/blob/efb35369e0ad2afab669f228072c1b0d510eae64/action.yml)
  and [Node 24 migration](https://github.com/softprops/action-gh-release/releases/tag/v3.0.0).

## Compatibility and cache preservation

Node 24 actions require runner v2.327.1 or later. Checkout also documents a
v2.329.0 minimum for authenticated Git commands from Docker container actions;
this workflow has no container action. The prior v1.2.18 Ubuntu and macOS jobs
both reported runner v2.338.2.

Checkout v6 moved persisted credentials under `RUNNER_TEMP`; normal host Git
commands continue to work. Checkout v7 adds refusal for unsafe fork code under
`pull_request_target` and `workflow_run`; neither trigger is used here. Do not
enable the unsafe-checkout override. Fetch depth remains zero so tag-derived
versions and full-history tests retain their inputs.

Setup-go v7 retains its inputs and outputs, but the intervening v6 release
changed the default dependency file from `go.sum` to `go.mod`. The actual
v1.2.18 setup-go commit was
`40f1582b2485089dde7abd97c1529aa768e1baff`, whose
[`package-managers.ts`](https://github.com/actions/setup-go/blob/40f1582b2485089dde7abd97c1529aa768e1baff/src/package-managers.ts)
uses `go.sum`. The selected v7
[`package-managers.ts`](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/src/package-managers.ts)
uses `go.mod`; its
[`cache-restore.ts`](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/src/cache-restore.ts)
honors an explicit dependency path first. Add `cache-dependency-path: go.sum`.
Both versions cache `go env GOMODCACHE` and `go env GOCACHE`; cache remains
enabled. An ordinary cache miss is not a reason to disable caching.

The repository has one tracked `go.mod` and one `go.sum`. `go.mod` specifies
Go 1.26.1 and has no `toolchain` directive, so the new toolchain precedence does
not change the selected version. Keep `go-version-file: go.mod`.

Action-gh-release v3 is the native Node 24 successor of v2. Its existing
`generate_release_notes`, `draft`, and `prerelease` inputs remain supported.
Keep the tag-only condition, test dependency, contents permission, and current
stable/prerelease expression unchanged.

## Risks and boundary

The main risks are silently changing cache invalidation, losing tag history,
changing release permissions/conditions, or upgrading only the test actions
while leaving the release job on Node 20. A structural workflow comparison and
real Ubuntu/macOS main plus tag/release CI are the decisive checks.

Keep the existing soft dependency on `fix-ci-rebase-editor`; no new hard
dependency is justified by replacing action versions. Do not change Go
dependencies, product behavior, workflow triggers, test budgets, permissions,
or unrelated action policies. Keep open-command implementation separate.
