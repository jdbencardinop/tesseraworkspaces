# Analysis: closure-aware sync abort

## Verdict and priority

**Confirmed on released v1.2.17** (`6d89bf116664cd87c087a2fe1cbba89f3d8f5b79`).
GitHub [#4, defect 1](https://github.com/jdbencardinop/tesseraworkspaces/issues/4)
remains open and is the first implementation boundary in the approved queue.
The separately released safe-reparent command does not fix ordinary sync abort.

## Reproduction

Real temporary repositories, local bare remotes, and external worktrees or a
single checkout were used. The stack was:

```text
master -> pr1 -> {pr2, pr3, pr4}; pr3 -> pr5 -> pr6
```

Each entry had its correct recorded parent cutoff. `pr1` was amended with an
isolated generated file. Upstream then advanced with a change that deliberately
conflicted with `pr2`, so `sync customer --full --no-fetch` successfully moved
the anchor before failing on the child. Every branch tip and the exact
`stack.yaml` bytes were captured before sync, at interruption, and after
`sync customer --abort`.

| Mode | Sync exit | Abort exit | Ref not restored | Metadata restored |
|---|---|---|---|---|
| external | 1 | 0 | `refs/heads/feat-pr1` | No |
| checkout | 1 | 0 | `refs/heads/feat-pr1` | Yes |

External abort printed `Sync state cleared.`; checkout abort printed
`Checkout sync aborted, original branch restored.`. Neither meant that the
whole selected stack had returned to its pre-sync state. The fixture deliberately
causes the child conflict; it does not claim to reproduce the cutoff defect as
the source of that conflict.

The observation was repeated in two complete runs. The session's
reproduction harness and raw JSON preserve command stdout/stderr, Git Trace2
argv, all branch snapshots, and metadata hashes. Disposable repositories were
removed after collecting evidence.

## Relevant code

- `internal/cli/sync_helpers.go:syncWithStackScoped` mutates entries in order and
  updates external `LastBaseSHA` after each successful row.
- `internal/cli/sync.go` owns external legacy/scoped recovery dispatch;
  `abortLegacySyncState` aborts the active Git rebase and removes its sentinel,
  not earlier successful branch movements.
- `internal/checkout_sync.go:AbortCheckoutSync` aborts the current Git rebase
  and calls `restoreOriginal`, which restores the physical checkout rather than
  every previously moved logical branch.
- `internal/sync_run_state.go` and `internal/checkout_sync.go` own the durable
  recovery formats and compatibility gates.

## Implementation boundary

Define one closure-aware transaction contract before editing production code:
protect selected pre-sync branch tips and metadata before mutation, durably
record progress, and restore all run-owned changes on abort. Later user work
must cause a clear refusal instead of being erased. Preserve physical checkout
restoration, external holder safety, no-flag successful behavior, scoped
selection, reparent exclusion, and existing versioned recovery protections.

Keep this feature separate from `sync-cutoff-integrity`. Snapshot/rollback
safety must not depend on guessing whether a child conflict came from a bad
cutoff. Both existing feature requests remain the owners; no duplicate feature
or ordinary-sync fix is claimed by this analysis.
