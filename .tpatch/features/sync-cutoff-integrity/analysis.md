# Analysis: ordinary-sync replay cutoff integrity

## Verdict and scope of evidence

GitHub [#4, defect 2](https://github.com/jdbencardinop/tesseraworkspaces/issues/4)
remains a high-priority investigation on released v1.2.17
(`6d89bf116664cd87c087a2fe1cbba89f3d8f5b79`).

**The unsafe behavior with stale recorded metadata is reproduced. The process
that originally produced that stale metadata is not yet established.** A
correct recorded old-parent cutoff succeeds in the same topology, so it would
be incorrect to claim that every amended-parent sync computes the wrong range.

## Controlled reproduction

Real temporary repositories and local bare remotes were used in external and
checkout modes, each under `--full --no-fetch` and `--local-only --no-fetch`.
The common master history contained 34 commits successively changing one
upstream-only file. `pr1` added two commits; `pr2` added one child-only commit.
Then `pr1`'s tip was amended with an isolated generated file.

Three otherwise-identical recorded-cutoff conditions were compared:

| Recorded cutoff | Measured replay range | Result in all four mode/policy combinations |
|---|---|---|
| Correct old `pr1` tip | 1 child-only commit | Exit 0; exactly `child-only` remains above the new parent |
| Deliberately stale ancestor before the 34 master commits | 37 commits: 34 upstream + 2 parent + 1 child | Exit 1; conflicts replaying already-present upstream history |
| Absent | Plain-rebase range contains 2 commits | Exit 0 in this fixture; Git drops the empty old-parent patch and leaves exactly `child-only` |

External Git progress explicitly showed `1/37` for the stale-cutoff case.
Git Trace2 captured the released executor passing the recorded stale SHA as
the `--onto` upstream, rather than the known correct old-parent SHA.
Checkout uses the same stored-cutoff decision but captures Git output
internally, so no terminal progress count is claimed for that mode.

These are controlled metadata-input cases, not proof that v1.2.17 created the
stale value from valid metadata. The absent-cutoff control is also not evidence
that plain rebase is safe in every rewritten-parent topology.

## Relevant code

- `internal/cli/sync_helpers.go:syncWithStackScoped` selects
  `rebase --onto <base> <LastBaseSHA>` when the recorded value differs from the
  destination; the unguarded path does not establish that this ancestor is the
  intended old-parent boundary.
- `internal/checkout_sync.go:processBranch` uses the corresponding persisted
  `LastBaseSHA` / `NewBaseSHA` decision.
- `internal/rebase_planner.go` and `internal/cli/sync_plan_guard.go` expose the
  plan/guard interfaces; parent-dependent rows in these fixture previews are
  deferred, so their candidate counts are unknown until remeasurement.
- Branch creation, successful sync attribution, interrupted sync, and legacy
  import/migration must be traced to determine how a wrong cutoff can arise.

## Next boundary

After `sync-transactional-abort`, investigate cutoff attribution and provenance
using the correct/stale/absent controls above. Require existence and ancestry,
but do not mistake those checks for proof of the correct boundary: the failing
stale cutoff in this fixture is a real ancestor. Do not substitute merge-base
heuristics or broaden replay silently when evidence is missing.

Keep no-flag successful behavior and both workspace modes covered. Any
intentional refusal or storage change requires explicit acceptance criteria
and compatibility coverage. No production fix is included in this analysis.
