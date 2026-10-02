# Scoped status projection

## Boundary and approved behavior

Issue #3 changes `tws status [feature] [--json]` only. A named report builds
that feature plus necessary workspace evidence, rather than building every
feature and filtering afterward. Healthy no-argument reports remain global.

The user approved:

- scoped workspace rollups from the requested feature plus true workspace
  evidence, including the shared checkout session even if it belongs to an
  unselected feature;
- five seconds per Git/tmux subprocess and thirty seconds total, beginning
  before workspace resolution, with no new command-line flags;
- later measurement of the existing global view, not a new global
  best-effort/error-continuation implementation in this feature.

The scoped rollup is an intentional change from the old post-filter global
presence. It must be documented explicitly. Preserve schema version 1, keys,
types, existing enum values, normalization, deterministic ordering, and
two-space JSON with a final newline. New issue codes may be additive if
existing codes cannot represent a probe failure honestly; register and
document them. `agent_state` remains unknown.

## Scope and resolution acceptance criteria

1. With a feature argument, select and validate only that feature before
   feature-dependent probes or inventories. Invalid, reserved, registered
   space, missing, and requested ambiguous-layout cases fail without stdout.
   Runtime resolution may need bounded Git before the metadata-root guard,
   but no unvalidated feature path may be joined/read.
2. Preserve trusted `spaces.yaml` validation and feature-name protection. A
   corrupt/future registry remains fatal, even on a named report.
3. Do not call the all-feature builder then `FilterFeature` on the CLI named
   path. Keep `FilterFeature` as a compatible public projection of already
   built reports for its existing callers/tests.
4. Do not read another feature's stack, direct session records, sync state,
   reparent state, decisions, or worktree dirty/branch probes for a named
   report. Unrelated invalid YAML, missing worktrees, unreadable records,
   and checkout legacy/new ambiguity cannot fail or delay selection.
5. Fix the resolver path as well as the builder: from an external workspace
   or feature directory, infer the source from explicit workspace config,
   the conventional sibling repository, and requested-feature evidence only.
   Do not scan other feature stacks/worktrees to infer a default repository.
6. Repository candidates must still be validated; multiple distinct valid
   candidates produce explicit degraded/ambiguous evidence rather than
   arbitrarily selecting one. Insufficient repository evidence retains the
   existing degraded-report behavior where the workspace is independently
   known. Do not guess checkout mode from `.tws` existence.
7. Normal configured/conventional fixtures are equivalent from source repo,
   linked worktree/nested directory, external workspace/feature/nested
   directory, and explicit checkout repo. Any unavoidable limitation when
   the only default-repository evidence is in an excluded feature must be
   explicitly degraded and documented rather than triggering a sibling scan.
8. Necessary workspace evidence includes root/config/space ownership, source
   repository facts, one bounded tmux snapshot, and shared checkout
   session/lock state. One bounded source-repository worktree inventory is
   allowed when needed for selected materialization; avoid dereferencing
   unrelated worktree paths solely to parse its records.
9. Missing/invalid requested features fail before tmux inventory and dirty,
   ref, materialization, session, or reparent probes. Only bounded
   workspace-identity discovery necessary to locate the root is permitted.

## Observation and compatibility acceptance criteria

10. The selected report contains exactly one feature and only its feature/
    entry issues plus genuine workspace issues. Counters describe that
    report; local faults and workspace warnings remain visible.
11. Scoped workspace runtime/attention rollups consume only selected
    observations plus genuine workspace evidence. An unrelated external
    direct/tmux session is neither read nor represented as selected activity.
    An unselected feature's shared checkout session remains visible at the
    workspace level and may make its rollup active/attention-worthy.
12. Do not emit `session-unattributed` merely because a valid checkout session
    names an excluded feature. Do not read the excluded stack to prove its
    entry. Still diagnose empty session identity and a missing recorded
    entry within the selected feature; existing global attribution stays
    unchanged.
13. Preserve local sync recovery guidance, stale/failed records, selected
    dirty-blocking attribution, and direct-process live/dead/unknown values.
    No process-birth or provider-state feature is introduced.
14. Preserve the CLI's execution-consistent reparent location resolution,
    including distinct configured and environment roots; selected reparent
    warnings appear once on stderr before stdout. Compatibility artifacts
    never gain unsafe remove/repair advice because of this change.
15. A worktree/repository probe that fails or times out cannot become
    `dirty: false`, `ref_exists: false`, attached HEAD, or a provably absent
    runtime. Keep unknown facts null/unknown and emit appropriately scoped
    diagnostic issues. Distinguish a verified missing ref from an unavailable
    Git probe.
16. Retain already measured independent facts if a later probe fails. A
    failed dirty probe does not erase a successfully observed branch.
17. All status output is read-only: no fetch, Git ref/index refresh, lock
    acquisition, metadata initialization, session cleanup, or provider call.
    Use `GIT_OPTIONAL_LOCKS=0` for read-only Git status commands.
18. Healthy unscoped output remains equivalent to the baseline apart from
    generated timestamps and deliberately documented failed-probe behavior.
    Unscoped topology failures remain fatal; do not silently skip broken
    features or create a second global schema.

## Budget and cleanup acceptance criteria

19. One status invocation owns an absolute thirty-second budget spanning
    configuration/workspace discovery and subsequent subprocess probes.
    Every dispatched Git/tmux command gets the smaller of five seconds or
    the remaining invocation time.
20. No status path bypasses this budget through `LoadConfig`/`RepoRoot`,
    source-repository inference, `MainRepoRootIn`, worktree inventory, branch/
    ref/dirty probes, tmux inventory, or an observability wrapper.
    Keep non-status mutation callers' existing execution semantics.
21. Once the budget or caller context expires, dispatch no further
    subprocesses. Populate remaining measurable-without-subprocess facts
    where safe, retain unknown fields and an explicit exhaustion diagnostic,
    and return a report when topology was established.
22. If resolution cannot establish a workspace after exhaustion, fail with a
    useful diagnostic and no stdout. Do not convert a timeout into a
    successful report for a guessed repository or silently retry unbounded.
23. Timeout/cancellation terminates the actual probe and its owned children
    and bounds waiting for inherited stdout/stderr pipes. Do not leave
    background goroutines or child processes accumulating between polls.
    Limit cleanup to the subprocess group created by this invocation.
24. Real process-liveness checking is signal zero, not a subprocess. Preserve
    its three-valued semantics; do not wrap arbitrary synchronous operations
    in abandoned goroutines to claim cancellation.
25. The budget is not a hard interrupt for filesystem I/O. Document this
    limit; do not claim that context deadlines can cancel an OS metadata
    read blocked on a filesystem.
26. Keep duration injection in narrow internal test seams, not new public
    flags, environment overrides, or repository-global mutable test state.
    Production defaults are exactly five and thirty seconds.

## Tests and implementation sequence

27. Add real Git fixtures for both modes, multiple features, new/legacy
    checkout paths, decoupled Git/logical names, and relevant invocation
    directories. Use deterministic tmux fixtures and test-scoped Git identity.
28. Count/record actual Git argv to prove no excluded-feature probe occurs,
    including during workspace-root inference. A sleep-only timing assertion
    is insufficient.
29. Cover unrelated layout ambiguity, malformed stack and unreadable metadata,
    delayed/failed unrelated worktrees, unknown selected dirty/ref/HEAD facts,
    missing requested features, and registered-space refusal.
30. Cover active/stale/unknown off-scope checkout sessions and selected
    session attribution without false orphan warnings; assert scoped and
    global runtime-rollup behavior separately.
31. Use short injected deadlines with blocking Git/tmux and child-holds-pipe
    fixtures. Assert bounded completion, no dispatch after total expiry,
    null/unknown fields with issues, and child cleanup. Test the exact default
    constants separately; tolerate scheduler noise without weakening the
    configured threshold.
32. Preserve existing schema snapshots, scope/guard tests, read-only tests,
    external/checkout session tests, and selected reparent/sync observability
    coverage. Add focused tests for any new issue codes.
33. Update CLI help, README status guidance, cheatsheet, and embedded agent
    skills for scoped rollups, global no-argument behavior, budgets,
    degradation, and filesystem limitations. Update CHANGELOG as unreleased,
    not as a fabricated publication.
34. Targeted command:
    `go test ./internal ./internal/cli -run 'TestAgentStatus|TestStatus|TestSyncPlanDocs_PlanningProse|TestReparentDocs' -count=1`.
    Before landing, also run full tests, vet, lint, build, formatting,
    `git diff --check`, dependency validation, and exact recipe replay.
35. Inspect and record the global view's probe counts/behavior with the same
    controlled fixtures. Do not treat those timings as a large-repository
    performance promise or implement global fault continuation here.

Prefer a status-specific bounded runner and scoped resolver/builder options,
reusing existing parsers and classifiers. Do not fork the entire status
implementation or change every global Git helper. Introduce only helpers/
files that keep the caller boundary clear.

Implement only after manual define/explore and dependency validation. Use an
isolated worktree, one implementation boundary, explicit OpenAI model
selection for delegated implementation/reviews, and the normal review loop.
No automatic push/tag authorization is granted by this specification.

## Non-goals

- tss status enrichment, provider/session control, or mailbox coordination;
- scope changes to `tws list`, doctor, stack status, or command completion;
- changed sync/reparent/session mutation admission, recovery formats,
  injection, templates, or Git topology;
- a new global status error-continuation mode, cache, daemon, or runtime store;
- historical tpatch artifact repair or unrelated refactoring.
