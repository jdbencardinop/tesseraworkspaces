# Exploration — rebase-plan-guard

Status: Path B exploration (author: isolated exploration agent). Inputs of record, all read before
writing: `.tpatch/features/rebase-plan-guard/spec.md` (**normative**),
`.tpatch/features/rebase-plan-guard/analysis.md`, `.../request.md`, `.../status.json`,
`AGENTS.md`, `CLAUDE.md`, `docs/engineering-workflow.md`, `docs/roadmap.md`,
`.tpatch/steering/local.md`, `.claude/skills/tessera-patch/SKILL.md`, and the hard-parent artefacts
`.tpatch/features/sync-modes/{spec,exploration}.md`,
`.tpatch/features/stack-status/{spec,exploration}.md`,
`.tpatch/features/amend-aware-rebase/{spec,exploration}.md`.

**Baseline**: working tree at `81659e1`, `git describe --tags` = `v1.2.15-3-g81659e1`, clean.
Every path, symbol and line number below was read in that tree. **A path with no `NEW` label
exists today.** Line numbers are the verification anchor at `81659e1`; re-derive with
`grep -n '<symbol>' <file>` if they drift.

**What this document is**: an implementation map — the exact existing call graph, the exact
insertion point per route, the exact new/changed symbol per file, a dependency-ordered build
sequence, and the test/probe ledgers. **What it is not**: a second spec. Every decision here is the
spec's; where this exploration found a mismatch between spec prose and the current tree it is
recorded in §19 as a *declared precision item*, never silently amended.

---

## 0. Ground truth — sizes and the frozen surface

| Fact | Value at `81659e1` |
|---|---|
| `internal/` non-test Go | 26 files, 14 331 lines |
| `internal/cli/` non-test Go | 35 files, 7 305 lines |
| `internal/cli/testdata/sync_noflag/**` | **126** files — the frozen golden corpus of §22.1 |
| Shipped `syncCmd` flags | **11** (`internal/cli/sync.go:132-142`) |
| `downgradeTag` | `"v1.2.14"` (`internal/cli/sync_downgrade_test.go:22`) — §23.1 seam 9 retargets it to `v1.2.15`, which exists as a real tag |
| `isRuntimeState` name list | exactly `.sync-state.yaml`, `.sync-state.v2.yaml`, `.sync-run.lock` plus the `.tws/state/` prefix (`internal/cli/importcmd.go:177-184`) — §19.3 holds |

The frozen artefacts a no-flag run must not disturb: `internal/cli/testdata/sync_noflag/**` (126
files), driven by `TestSyncNoFlag_*` (`internal/cli/sync_golden_test.go:1450`, `:1463`, `:1472`,
`:1489`, `:1501`, `:1510`, `:1524`, `:1546`, `:1555`, `:1564`, `:1580`) and
`internal/cli/sync_validation_test.go:250`.

---

## 1. Package boundary and plumbing, measured

### 1.1 `internal` vs `internal/cli` — the boundary as it really is

- `internal` imports **no** Cobra and **no** `internal/cli` today; `internal/cli` imports
  `internal` and `github.com/spf13/cobra`. §9.0 rule 5's import assertion is a *preservation*
  assertion, not a new constraint.
- `externalSyncLayout` (`internal/cli/sync_modes.go:176-179`), its `WorktreePath`
  (`:182-184`) and `resolveExternalSyncLayout` (`:192-208`) are **unexported members of package
  `cli`** and stay there. `newExternalSyncLayout` is at `:210`.
- The checkout half already lives in `internal`: `CheckoutSyncOpts` (`internal/checkout_sync.go:545-561`)
  carries `FeaturePath`/`RepoDir` as plain strings, so `internal.RebasePlanLayout{RepoRoot: …}`
  (**NEW**, `internal/rebase_plan.go`) is constructible there with no `cli` type.
- Consequence for every shared planner signature (§9.0 rule 1): the layout parameter is
  `internal.RebasePlanLayout`; package `cli` builds it once via `planLayout(externalSyncLayout)`
  (**NEW**, `internal/cli/sync_plan_guard.go`).
- `ExternalPlanInspection`/`InspectExternalPlan` **must** live in `cli` (they compose
  `externalSyncLayout` and `firstUnresolvedSelectedBase`); `CheckoutPlanInspection`/
  `InspectCheckoutPlan` **must** live in `internal` (they compose `CheckoutSyncOpts`). That is why
  §19.1 puts the checkout inspector in `internal/rebase_plan_guard.go` and §13.7a puts the external
  one in `internal/cli/sync_plan_guard.go`. Do not "unify" them.

### 1.2 Writer plumbing today (the reason §3.6 exists)

Every operational print on the sync path is a bare `fmt.Print*` to the process-global streams:

| Printer | Site |
|---|---|
| `printSyncModeHeader` (cli) | `internal/cli/sync_modes.go:502-504` |
| `printSyncModeHeader` (internal) | `internal/checkout_sync.go:519-521` — a byte-identical twin, deliberately duplicated |
| `fetchQuiet` | `internal/cli/sync.go:604-635` — the only external fetch body; verbose arm calls `internal.RunDir`/`internal.Run`, quiet arm `internal.RunSilentDir`/`internal.RunSilent` |
| checkout fetch | **inline**, `internal/checkout_sync.go:618-625`, inside `RunCheckoutSync` — there is **no** `fetchCheckoutRepo` function today (see resolved §19 P-3) |
| `printLocalOnlyNoOp` | `internal/checkout_sync.go:526-543` |
| executor status lines | `formatSyncStatus` (`internal/cli/sync_helpers.go:381-388`) printed with `fmt.Println` throughout `syncWithStackScoped` |

`internal/exec.go` has **no** writer-taking helper: `Run` (`:117`), `RunDir` (`:126`),
`RunDirClean` (`:138`), `runWithFilteredStderr` (`:142`), `RunSilent` (`:188`), `RunSilentDir`
(`:193`). `RunTo`/`RunDirTo` are **NEW and purely additive** (§8).

### 1.3 Error plumbing today

- External: `syncCmd`'s `RunE` returns `error`; `cli.Execute()` (`internal/cli/root.go:16-58`)
  prints it with `fmt.Fprintln(os.Stderr, err)` at `:53-56` and returns `1`. **This is the only
  place a returned error reaches stderr in production** — hence §23.1 seam 1 must drive
  `cli.Execute()`, not `syncExecute` (`internal/cli/sync_golden_test.go:1110-1128`), which sets
  `SilenceErrors` and prints the error itself.
- Checkout: `runCheckoutSync` (`internal/cli/checkout_sync.go:15`) returns errors produced in
  `internal` (`RunCheckoutSync`, `ContinueCheckoutSync`, `AbortCheckoutSync`) unchanged. The typed
  `*internal.PlanGuardRefusalError` therefore crosses the boundary as a plain `error` and is
  rendered by `planGuardRefusal(cmd, err)` (**NEW**) via `cmd.ErrOrStderr()`.
- `syncResult` (`internal/cli/sync_helpers.go:12-19`) has no error channel — which is exactly why
  §19.2 adds `Refusal *internal.PlanGuardRefusalError` to it: the executor's JIT seam must surface
  a typed refusal without inventing a second return.

---

## 2. Verified current call graphs and the exact insertion points

### 2.1 `syncCmd` `RunE` — the measured statement ladder (`internal/cli/sync.go:35-127`)

```
:36  internal.RequireTool("git")
:38  ws, err := internal.RequireWorkspace()
:45  policy, newMode, changed, err := resolveSyncPolicy(cmd, ws.Mode)     <-- the ONE `changed` map
:50  if ws.Mode == internal.ModeCheckout { return runCheckoutSync(ws, CheckoutSyncOpts{…}) }   :51
:68  feature := args[0]; twsRoot := internal.TwsRoot()
:69  internal.GuardFeatureName(twsRoot, feature)
:72  layout, layoutErr := resolveExternalSyncLayout(ws, twsRoot, feature)
:77  state, stateErr := classifySyncState(layout.FeaturePath, newMode)
:83  deferred I7:  cont && abort && (state.Marker != "" || state.Payload != nil)  -> errSyncContinueAbort()
:87  if abort  { :88 syncCellRefusal(abort) ; :91 handleSyncAbortCell }
:93  if cont   { :94 syncCellRefusal(continue) ; :97 I20 (newMode && cell 1|7) ;
                 :100 cell 5 -> handleScopedSyncContinue ; :103 -> handleSyncContinue }
:105 syncCellRefusal(plain)
:108 internal.HasSyncState -> "previous sync incomplete …" refusal
:113 if newMode { :114 runScopedSync }
:117 result := syncFeature(feature, layout, verbose)          <-- legacy fresh
```

**Insertion points on this ladder (spec §3.3 / §13.7a step 1, §19.2):**

| # | Insert | Exact position |
|---|---|---|
| 1 | five flag registrations + §3.1a `Long` | beside `:132-142`; `Long` on the `&cobra.Command{}` literal at `:25-33` |
| 2 | `opts, err := resolvePlanGuardOptions(cmd)` | §3.3 step 3 — immediately after `:45`, before the checkout dispatch, so both modes validate identically |
| 3 | pass `cmd` + `opts` into `runCheckoutSync` | rewrite `:51` call to `runCheckoutSync(cmd, ws, internal.CheckoutSyncOpts{… PlanGuard: opts.checkoutGuard()})` |
| 4 | **plan dispatch** `runExternalPlan(...)` | immediately below the `stateErr` check after `:77`, and strictly **above** deferred I7 and the executing live-guard/sentinel block. A plan therefore always reaches the document route; its own `InspectExternalPlan` projects sentinel/live-owner facts and owns the plan-only vanished-sentinel reclassification |
| 5 | deferred I7 becomes `syncStateRefusesContinueAbort(state)` | replaces the predicate at `:83`, **same position** (§13.6 rule 4b) |
| 6 | `syncCellLiveGuardRefusal(feature, state, verb)` + **executing cell-4 sentinel interception** | immediately below deferred I7 and before the abort/continue/plain split; derive the one shipped `syncVerb`, call the live extraction, then seam 2a and `InspectGuardedLegacySentinel` only when `state.Cell == 4`. On verdict `absent`, call `classifySyncState` exactly once more and dispatch that second state directly through the ordinary post-interception route; never re-enter. The three shipped `syncCellRefusal` calls at `:88`, `:94`, `:105` remain below this block |
| 7 | I20 becomes `syncTriggersNeedV2(state)` | replaces the predicate at `:97`, **same position** (§13.6 rule 4a) |
| 8 | cell-5 three-arm dispatch | replaces the single `:100-102` arm |
| 9 | cell-7 two-arm dispatch | replaces the fall-through at `:103` |
| 10 | `plan-unavailable` interception before `syncFallback` | inside `syncFeature` (`:535-539`, the `LoadStack` error arm that calls `syncFallback`) |
| 11 | guarded fresh routes | `:113-118` — `opts.Armed()` selects `runGuardedScopedSync`/`runGuardedLegacySync`; unarmed keeps `runScopedSync` (`:114`) and `syncFeature` (`:117`) byte-identical |
| 12 | `planGuardRefusal(cmd, err)` wrapper | around the external `RunE`'s return value |

### 2.2 External legacy fresh — `syncFeature` (`internal/cli/sync.go:535-557`)

```
:536 LoadStack        -- on error: fetchQuiet("","",verbose) ; syncFallback(layout) ; return Complete
:543 internal.UniqueRepos(stack, layout.FeaturePath) -> :544-546 for repo, wtPath { fetchQuiet }   <-- legacy fetch loop
:548 internal.TopoSort(stack)                                                             <-- legacy's ONE sort
:553 syncWithStack(feature, layout, stack, sorted)
```

- **Extraction (§19.2):** `:543-546` becomes `fetchStackReposTo(out, errw, layout, stack, verbose, plan)`
  **NEW**; the shipped call passes `os.Stdout, os.Stderr` and the zero `internal.PlanFetchPlan`.
- **Guarded twin** `runGuardedLegacySync` **NEW** does *not* call `syncFeature`; it calls
  `InspectExternalPlan` → `fetchStackReposTo` → `BuildRebasePlan` → `EvaluatePlanGuard` →
  `setupGuardedLegacyRunState` → `syncWithStackScoped(…, insp.Order, nil, run{Route: legacy}, guard)`.
- `syncFallback` (`internal/cli/sync_helpers.go:390-399`) must be unreachable from planned/guarded
  routes; the `plan-unavailable` interception at `:536` is what guarantees it.

### 2.3 External new-mode fresh — `runScopedSync` (`internal/cli/sync.go:151-214`)

```
:152 LoadStack                          :156 internal.ResolveSyncSelection      <-- sort #1 (inside)
:164 verifySelectedBasesLocally (I14)   :168 syncMarkerFn()   :175 newSyncOwnerToken()
:180 internal.LoadConfig().TestCommand  -> testCommand/validationSource
:186 setupSyncRunState(...)             <-- FIRST shared-state write (guard, sentinel, payload)
:191 printSyncModeHeader(policy)
:194 syncFeatureScoped(feature, layout, verbose, stack, run)
        :560-566 payload check, :561 SyncStageFetching write, then fetch loop
                 (UniqueRepos over selectedRealEntries)
        :568 internal.TopoSort(stack)   <-- sort #2
        :573 syncWithStackScoped(...)
:203 runNewModePush   :207 finalizeScopedSyncRun
```

- Shipped sort count on this route is **2** (`:156` inside `ResolveSyncSelection`, `:568`) — §9.1a
  rule 3a's control.
- **Extractions (§19.2):** `syncFeatureScoped`'s fetch block (`:560-566`) →
  `fetchScopedReposTo(out, errw, layout, stack, sel, verbose, plan)` **NEW** (payload-free by
  signature, so it *cannot* write `SyncStageFetching`); the remainder minus its `TopoSort` →
  `syncFeatureScopedPlanned(feature, layout, verbose, stack, sorted, run, guard)` **NEW**.
- **Guarded twin** `runGuardedScopedSync` **NEW** reorders exactly two things versus `runScopedSync`:
  the header moves **above** the fetch, and the fetch moves **above** `setupSyncRunState`. The
  guarded stage sequence is `initializing → rebasing` with `fetching` **never written** (§22.28c is
  the one-divergence assertion).

### 2.4 External legacy continuation — `handleSyncContinue` (`internal/cli/sync.go:311-361`)

```
:312 LoadSyncState        :317 isRebaseInProgress gate      :321 LoadStack
:326 GetBranch(failed) / :330 branchContainsConfiguredParent / :333 "[~] resolved"
:336 done set from state.Completed + FailedBranch
:343 internal.TopoSort(stack)                    <-- the SINGLE shipped sort (§19.2 cites :343)
:347 "Resuming sync with %d pending branch(es)"
:348 syncWithStackFiltered(feature, layout, stack, sorted, done)
```

**Frozen when unarmed.** The armed cell-7 arm is a *different* function,
`handleGuardedLegacySyncContinue` **NEW**, which enters `syncWithStackScoped(…, run{Route: legacy,
Payload: envelope}, guard)` — never `syncWithStackFiltered`, whose `run == nil` would strand the
envelope and let `saveIncompleteSync` (`internal/cli/sync_helpers.go:264`) write a real
`.sync-state.yaml` over the sentinel.

### 2.5 External scoped continuation — `handleScopedSyncContinue` (`internal/cli/sync.go:363-433`)

```
:368 syncContinueMismatches       :373 isRebaseInProgress      :377 LoadStack
:381 scopedSelectionFromPayload   -> :486 internal.ResolveSyncSelection   <-- sort #1 (inside)
:387 GetBranch(failed)  :391 branchContainsConfiguredParent  :394 "[~] resolved"
:397 state.GuardForeign() refusal
:400 internal.ReclaimSyncRunGuard(featurePath, payload.OwnerToken)     <-- FIRST write
:411 internal.TopoSort(stack)                                          <-- sort #2
:416 printSyncModeHeader(payload.Policy())
:417 "Resuming sync with %d pending branch(es)"
:420 syncWithStackScoped(feature, layout, stack, sorted, done, run)
:426 runNewModePush   :430 finalizeScopedSyncRun
```

- **Extraction (§9.2a rule 6):** `scopedSelectionFromPayloadOrder(stack, order, payload, feature, mode)`
  **NEW** is the order-taking body; `scopedSelectionFromPayload` keeps its shipped signature,
  sentences and call site (`:486`) as the sorting wrapper.
- **Guarded twin** `handleGuardedScopedSyncContinue` **NEW** places the guard seam **above**
  `ReclaimSyncRunGuard` (`:400`) and after `checkSyncRunGuardReclaimable`, so a limit refusal leaves
  `.sync-state.v2.yaml`, the sentinel and `.sync-run.lock` byte-identical.
- **Armed v2 upgrade (§13.2a step 10a):** after the seam passes, call
  `ReclaimSyncRunGuard` at `:400` first; then call
  `upgradeGuardedSyncRunState(layout, payload, limits)` **immediately after that successful
  reclaim and before** `printSyncModeHeader` (`:416`) or any resume prose. There is no second
  external-payload upgrade site. A refusal above reclaim leaves all three artefacts byte-identical;
  an upgrade failure is below the first write but above the first Git mutation.

### 2.6 External abort cells — `handleSyncAbortCell` (`internal/cli/sync.go:262-292`) / `handleSyncAbort` (`:294-309`)

```
:263 state.GuardForeign() refusal
:264 case 2, 5:  rebase --abort if needed ; DeleteSyncRunState ; (cell5) DeleteSyncState ;
                 ReleaseSyncRunGuard ; "Sync state cleared."
:281 case 4:     HasSyncRunState -> "scoped sync state appeared …" ; DeleteSyncState ;
                 ReleaseSyncRunGuard ; "Sync state cleared."
:291 default -> handleSyncAbort  (cells 1, 3, 6, 7, 8):
       :295 LoadSyncState err -> "Nothing to abort — no sync in progress."   <-- cell 1 today
       :299 rebase --abort ; :306 DeleteSyncState ; :307 "Sync state cleared."   <-- cell 7 today
```

**Insertions (§12.8, §12.8a, §12.8b, §19.2):**
- `case 1:` **NEW** arm → `internal.ReleaseStaleSyncRunGuard` (§12.8).
- `case 7:` **NEW** arm → `internal.ReleaseStaleSyncRunGuardWith(…, SyncGuardReleaseOpts{AllowSelfPID: true})`
  then the print-free `abortLegacySyncState` **NEW** extraction of `handleSyncAbort`'s body;
  `handleSyncAbort` becomes that extraction's printing wrapper, byte-identical for every shipped caller.
- `case 4:` keeps its **shipped body byte-for-byte**; §12.8b's discard is a print-free helper called
  from the single interception at §2.1 row 4.

### 2.7 The shared executor and the two external JIT seams — `internal/cli/sync_helpers.go`

```
:70  syncWithStack         -> syncWithStackFiltered(…, nil)
:74  syncWithStackFiltered -> syncWithStackScoped(…, nil)          <-- run == nil is the freeze idiom
:78  syncWithStackScoped(feature, layout, stack, sorted, alreadyDone, run)
       :82  scoped := run.scoped()
       :87  run.Payload != nil -> SyncStageRebasing write
       :92  pass 1 loop over `sorted`
             :98  run.selects / :101 run.skipsAnchor
             :108 os.Stat(path) — non-materialized entries fall through to pass 2
             :112 checkSyncWorktreeBranch
             :117 base := resolveEntryBase(...)          <-- JIT SEAM (pass 1) :117-135
             :122 currentBaseSHA := internal.GetBranchSHA(gitContext, base)
             :123 rebaseArgs = ["rebase","--update-refs",base]        (unscoped)
             :125 …["rebase","--update-refs","--onto",base,LastBaseSHA]
             :130 scoped: ["rebase",base] / :132 ["rebase","--onto",base,LastBaseSHA]
             :136 syncStepHook(SyncStageRebasing, rebased)
             :140 internal.RunDirClean(path,"git",rebaseArgs...)      <-- the pass-1 mutation
             :148 run.validate(...)
             :153 markUpdatedAncestors (unscoped only)
             :159-160 internal.UpdateBaseSHA + internal.SaveStack
       :164 pass 2 loop (non-materialized entries)
             :183 internal.IsPrunableWorktree
             :192 base := resolveEntryBase(stack, entry, entry.Repo)
             :196 RunSilentDir(rebaseDir,"git","rebase",base,GitBranch())  <-- JIT SEAM (pass 2) :196-201
             :198 RunSilent(...)                                            (process-cwd arm)
       :220 staleStackEdgesFiltered completion gate ("Sync incomplete; stale stack edges remain:")
       :228 staleStackEdgesComplement (scoped, informational)
       :236 "Nothing to propagate." when anchorsSkipped>0 && rebased==0
       :243 payload completion write / :249 clearSyncRunState(featurePath,false)
```

**Signature change (§19.2):** all three of `syncWithStack`, `syncWithStackFiltered`,
`syncWithStackScoped` gain a trailing `guard *planGuardRun`, `nil` on every unguarded route —
structurally identical to how `run *syncRunContext` is already threaded. Both seams call
`internal.RevalidatePlanEntry` + `internal.RevalidatePlanGuardEntry` only when `guard != nil`.

Also here: `resolveEntryBase` (`:335-343`) and `resolveBase` (`:345-356`) — the bodies §9.1
extracts into `internal.ResolveSyncBase`; `runValidation` (`:401-403`) — the site that must take the
frozen validation value instead of re-reading `internal.LoadConfig()`; `runValidationCommand`
(`:408-421`) already takes an explicit command and is the correct target.

### 2.8 Checkout CLI arm — `internal/cli/checkout_sync.go` (69 lines, whole file)

```
:15  func runCheckoutSync(ws internal.Workspace, opts internal.CheckoutSyncOpts) error
:17  internal.RequireFeaturePath(feature)
:22-31 I19 containment: os.Getwd -> internal.GitRepoRootIn -> filepath.Clean compare -> refusal
:33-34 opts.FeaturePath = featurePath ; opts.RepoDir = ws.RepoRoot          <-- HOIST THESE ABOVE :22
:36  --abort  -> internal.AbortCheckoutSync ; "Checkout sync aborted, original branch restored."
:44  --continue -> :46 I20 (opts.NewMode && loadErr||tx==nil||tx.StateVersion<CheckoutTransactionVersion)
                   :52 internal.ContinueCheckoutSync ; "Checkout sync completed."
:60  fresh -> internal.HasCheckoutTransaction refusal ; :64 internal.RunCheckoutSync ;
      "Checkout sync complete."
```

**Insertions:** signature becomes `runCheckoutSync(cmd *cobra.Command, ws, opts)`; `:33-34` hoisted
above `:22`; the `:22-31` predicate evaluated **once** into an `internal.PlanGateResult` reused by
both arms; plan dispatch below `:17` and above the containment refusal; `:48` predicate replaced by
`checkoutTriggersNeedV2(tx, loadErr)`; `opts.PlanGuard.PersistedGuarded = internal.TransactionGuarded(tx)`
set below the I20 refusal and above `:52`; `planGuardRefusal(cmd, …)` wrapping `:52` and `:64`
(the `--abort` arm at `:37` is unwrapped — §3.4 forbids a control flag there).

### 2.9 Checkout fresh — `RunCheckoutSync` (`internal/checkout_sync.go:563-690`)

```
:564 HasCheckoutTransaction refusal      :567 gitOperationInProgress refusal
:570 gitWorkingTreeDirty refusal         :577 gitCurrentBranch (detached refusal)
:581 gitResolveRef "HEAD"
:588-608 new-mode pre-lock preflight: LoadStack -> ResolveSyncSelection (:597, sorts inside)
         -> verifyCheckoutBasesLocally (:602)   [legacy arm does NONE of this]
:612 AcquireCheckoutLock                                     <-- FIRST write
:616 if NewMode { printSyncModeHeader(:617) ; fetch-policy gate + inline fetch body(:618-625) }
:628-638 legacy arm's LoadStack (below the lock)
:641 BuildCheckoutPlan(opts.RepoDir, stack, sel)   -> TopoSort at :465  <-- sort #2 on new-mode
:648 printLocalOnlyNoOp        :651 len(plan)==0 -> release lock, return nil
:657-682 tx := &CheckoutTransaction{…}; new-mode block sets StateVersion/policies/Selected
:684 SaveCheckoutTransaction                                 <-- guard seam goes ABOVE this
:689 executeTransaction
```

**Insertions:** on a **guarded** run only, `InspectCheckoutPlan` is called **above `:612`**; it owns
the one `InspectCheckoutPlanState`, the one `LoadStack`/`TopoSort`/`ResolveSyncSelectionFromOrder`/
`ProbeGitVersion`/I14 and the `PlanFetchPlan` enumeration. Then `fetchCheckoutRepoTo(os.Stdout,
insp.FetchPlan.Contexts[0])` at `:618`, `buildCheckoutPlanFrom(..., insp.Order, ...)` at `:641`, the
guard seam between `:641` and `:684`, and the v3 envelope fields on the `:657` literal. The
**unguarded** route calls the inspector zero times and keeps `:597` and `:465` exactly where they are.

### 2.10 Checkout continuation — `ContinueCheckoutSync` (`:718-753`) and `resumeTransaction` (`:866-931`)

```
:719 LoadCheckoutTransaction        :723 StateVersion > CheckoutTransactionVersion refusal
:728 v2 arm: checkoutContinueMismatches(:729) + checkoutSelectedStillPresent(:732)
:735 else-arm: --push-added-to-legacy refusal
:740 forceAcquireCheckoutLock                                <-- guard seam goes AFTER this
:744 tx.LockPID = os.Getpid() ; :747-748 opts.Push/TestCommand from tx
:749 v2 arm: :750 printSyncModeHeader(transactionPolicy(tx))
:753 resumeTransaction
       :868 StageConflict   -> rebase-in-progress gate ; gitIsAncestor gate ; PostSHA ; -> resumeFromRebased
       :895 StageSwitched   -> resumeFromSwitched (:933-939)   <-- dedicated JIT seam, pinned destination
       :899 StageRebased    -> resumeFromRebased (:941-986)
       :903 StageValidating -> resumeFromValidating (:988-1008)
       :907 StageRestoring  -> resumeFromRestoring (:1010-1020)
       :911 StagePlanned/StageRebasing -> rebase-in-progress -> StageConflict, else executeTransaction
       :924 StageCompleted  -> finalizeCleanup
```

**Insertions:** `InspectCheckoutPlan` above `:740` on a guarded run; the guard seam immediately
after `:740`; `upgradeGuardedCheckoutTransaction` **NEW** immediately below the seam and above
`:753`; the `resumeFromSwitched` JIT seam at `:933-939`; the shipped `processBranch` re-resolution
JIT seam at `:1024-1030`.

### 2.11 Checkout abort — `AbortCheckoutSync` (`:815-848`)

```
:816 LoadCheckoutTransaction        :823 deferred I7: opts.Continue && tx.StateVersion >= CheckoutTransactionVersion
:827 forceAcquireCheckoutLock       :831 rebase --abort       :838 restoreOriginal
:843 DeleteCheckoutTransaction      :844 ReleaseCheckoutLock
```
Only `:823`'s version comparison changes — to `checkoutRecoveryIsNewMode` (§13.6 rule 4c).

### 2.12 Push and restore call sites

- External push: `runNewModePush` (`internal/cli/sync.go:216-229`) → `pushFeature`
  (`internal/cli/push.go`) for `scope=all`, `pushScoped` otherwise. `internal/cli/push.go` is
  **untouched**; the plan only *projects* its targets.
- Checkout push: inside `finalizeTransaction` (`internal/checkout_sync.go:1161-1235`) via `gitPush`
  (`:438-447`).
- Restore: `restoreOriginal` (`:1257-1285`) and `finalizeCleanup` (`:1237-1255`) — the two arms
  `restore.applies` is the disjunction of (§14.4). `finalizeTransaction`'s body — the `SaveStack`
  `LastBaseSHA` update, the whole-plan `gitIsAncestor` loop, its sentence, and its `StageRestoring`
  transition — is **explicitly frozen** (§19.3): no seam, no hook site, no guard verdict, no write.

### 2.13 Setup / teardown / reclaim sites

| Concern | Symbol | Site |
|---|---|---|
| external setup (claim → sentinel → payload) | `setupSyncRunState` | `internal/cli/sync_modes.go:385-431`; claim `:386`, hooks `syncStepHook(SyncStageInitializing, 0/1/2)` |
| external teardown | `clearSyncRunState` | `internal/cli/sync_modes.go:433-451` |
| failure persistence | `saveScopedSyncFailure` `:453`, `saveScopedPushFailure` `:479` |
| guard claim / reclaim / release | `ClaimSyncRunGuard` `:171`, `ReclaimSyncRunGuard` `:207`, `ReadSyncRunGuard` `:236`, `ReleaseSyncRunGuard` `:249`, `writeSyncGuardExclusive` `:253` — all `internal/sync_run_state.go` |
| payload load/save/delete | `LoadSyncRunState` `:105`, `SaveSyncRunState` `:122`, `DeleteSyncRunState` `:133`, `HasSyncRunState` `:138`, `NewSyncRunState` `:144` |
| legacy state | `internal/syncstate.go` — `SyncStatePath` `:19`, `LoadSyncState` `:23`, `SaveSyncState` `:35`, `DeleteSyncState` `:43`, `HasSyncState` `:47`, `NewSyncState` `:52` |
| checkout lock | `AcquireCheckoutLock` `:185`, `writeLockExclusive` `:222`, `removeLockIfUnchanged` `:240`, `ReleaseCheckoutLock` `:251`, `HasCheckoutLock` `:255`, `forceAcquireCheckoutLock` `:262`, `ReadCheckoutLock` `:290` |
| classifier | `ClassifyExternalSyncState` `internal/sync_run_state.go:343-428`, `syncStateCell` `:429`, `GuardForeign` `:435`, `HasGuardFile` `:440`; wrapper `classifySyncState` `internal/cli/sync_modes.go:272` |
| crash hooks | `SyncStepHook` var `internal/sync_run_state.go:84`; `syncStepHook` wrapper `internal/cli/sync_modes.go:489`; `StepHook` var `internal/checkout_sync.go:316` |

---

## 3. Route → insertion-point matrix (the closed set of routes this feature touches)

| # | Route | Entry today | Controlled entry | Guard seam | JIT seam | First write on the controlled path |
|---|---|---|---|---|---|---|
| 1 | external legacy fresh (no-flag) | `syncFeature` `sync.go:535` | `runGuardedLegacySync` **NEW** (`sync.go`) | after `fetchStackReposTo` + `BuildRebasePlan`, above `setupGuardedLegacyRunState` | `sync_helpers.go:117-135`, `:196-201` | `ClaimSyncRunGuard` inside `setupGuardedLegacyRunState` (§13.2a step 2) |
| 2 | external new-mode fresh | `runScopedSync` `sync.go:151` | `runGuardedScopedSync` **NEW** | after `fetchScopedReposTo` + `BuildRebasePlan`, above `setupSyncRunState` | same two seams | `setupSyncRunState(..., birth)` |
| 3 | external plan-only (both routes) | — | `runExternalPlan` **NEW** (`sync_plan_guard.go`), dispatched at `sync.go:77`→`:83` | none — a document, never a refusal | none | **none** (§18.1) |
| 4 | external legacy continuation, cell 7 | `handleSyncContinue` `sync.go:311` | armed ⇒ `handleGuardedLegacySyncContinue` **NEW** (legacy-state arm) | after the projected ladder + shipped prose, above the first write | both | `ClaimSyncRunGuard` in `setupGuardedLegacyRunState` |
| 5 | external legacy continuation, cell 5 v3 `route: legacy` | — | `handleGuardedLegacySyncContinue` (envelope arm) | same | both | `ReclaimSyncRunGuard` |
| 6 | external cell-4 recovery | `handleSyncAbortCell` `:281` / refusals | `handleGuardedLegacySyncContinue` (recovery arm) + §12.8b interception | same | both | guard claim/reclaim + payload write only |
| 7 | external scoped continuation, cell 5 | `handleScopedSyncContinue` `sync.go:363` | v3 `route: new-mode` **or** armed ⇒ `handleGuardedScopedSyncContinue` **NEW** | after the ladder + `checkSyncRunGuardReclaimable`, **above** `ReclaimSyncRunGuard` (`:400`) | both | `ReclaimSyncRunGuard`, then the sole `upgradeGuardedSyncRunState` call immediately below it on an armed v2 payload |
| 8 | checkout plan-only fresh | — | `internal.PlanCheckoutRebase` → `BuildCheckoutRebasePlan` | none | none | **none** |
| 9 | checkout plan-only continuation | — | `PlanCheckoutRebase` → `BuildCheckoutContinuationPlan` | none | none | **none** |
| 10 | checkout guarded fresh | `RunCheckoutSync` `checkout_sync.go:563` | same function, guarded arm | after `buildCheckoutPlanFrom` (`:641`), before `SaveCheckoutTransaction` (`:684`) | `processBranch` `:1024-1030` | `AcquireCheckoutLock` `:612` |
| 11 | checkout guarded continuation | `ContinueCheckoutSync` `:718` | same function, guarded arm | after `forceAcquireCheckoutLock` `:740`, before `resumeTransaction` `:753` | `processBranch` `:1024-1030` **and** `resumeFromSwitched` `:933-939` | `forceAcquireCheckoutLock` |
| 12 | checkout abort | `AbortCheckoutSync` `:815` | unchanged apart from the `:823` route predicate | — | — | `forceAcquireCheckoutLock` `:827` |
| 13 | push (external) | `runNewModePush` `sync.go:216` | projected only | — | — | `SyncStagePushing` write `:219` |
| 14 | push (checkout) | `finalizeTransaction` `:1161` | projected only | — | — | frozen (§19.3) |
| 15 | restore | `restoreOriginal` `:1257` / `finalizeCleanup` `:1237` | projected only; **one** pre-`restoreOriginal` holder refresh | — | that one refresh | frozen |

**Frozen-by-construction:** rows 1, 2, 4, 7 and 10–12 all have an unguarded twin that keeps its
shipped body; the guarded route never enters `syncFeature`, `syncFeatureScoped`, `handleSyncContinue`
or `handleScopedSyncContinue` (§10.3, §17.1, §19.2).

---

## 4. NEW files and symbols — §19.1 reconciled against the tree

None of these paths exists at `81659e1`. All are **NEW**.
The spec's §19.1 heading says “package `internal`”, but its final row is deliberately package
`internal/cli`; the `Package` column below is authoritative for implementation (see §19 P-6).

| File | Package | Declares |
|---|---|---|
| `internal/rebase_plan.go` | `internal` | `RebasePlan` (the 25 §4.1 keys, in order), `RebasePlanRequest` (§9.1a), `RebasePlanLayout` + `WorktreePath`, `PlanWorkspace`, `PlanPolicy`, `PlanRefusal`, `PlanGuardLimitConflict`, `PlanBasePreflight`, `PlanContinuationGate`, `PlanEntry`, `PlanContext`, `PlanRepository`, `PlanConfigSlot`, `PlanConfigIssue`, `PlanEncodingIssue`, `PlanFetch`, `PlanFetchRepo`, `PlanFetchEffect` (nullable `HeadBranch`), `PlanFetchCandidate`, `PlanSubmoduleRecursion`, `PlanLocalBranchDestinations`, `PlanIntent`, `PlanPush`, `PlanPushTarget` (10 members), `PlanLease` (5), `PlanPushContext`, `PlanPushRemoteFacts`, `PlanPushFacts`, `PlanPushRequest`, `PlanRefspec`, `PlanContextIdentities`/`PlanContextIdentity`, `PlanRestore` (10 members), `PlanState`, `PlanGuardBlock`, `PlanGuardLimit`, `PlanGuardEvaluation`, `PlanApproval`, `PlanBlocker`, `PlanWarning`, `PlanSummary`; consts `RebasePlanSchemaVersion = 1`, `RouteLegacy`/`RouteNewMode`; types `RefusalKind` + `RefusalKinds`, `PushBlockedKind`, `RestoreBlockedKind`, `ControlledPathBlocker` + the five members + `ControlledPathBlockers`. **Types and constants only** — no `func` of §14.1a, no `SelectPrimaryRefusal`, no `PlanArtefactPresence`. |
| `internal/rebase_planner.go` | `internal` | `EntryContexts`, `ResolveSyncBase`, `RebaseStrategy`, `ExecutionOrder`, `DestinationDeferred`, `ReplayUpstream`, `RemainingRebaseEntries`, the **six** push producers `ResolvePushContext`, `MeasurePushRemoteFacts`, `RefreshPushTrackingRefs`, `PushContextRefreshed`, `ResolvePushLease`, `PushTargets(req PlanPushRequest) []PlanPushTarget`, plus `GateBlockers`, `GateControlledTokens`, `SelectPrimaryRefusal`. |
| `internal/rebase_plan_guard.go` | `internal` | `CheckoutPlanGuard` (+`Armed()`/`Guarded()`), `PlanGuardRefusalError{Kind,Detail,StatePreserved}` + `Error()`, `EvaluatePlanGuard`, `RevalidatePlanGuardEntry`, `PlanWriters{Prose io.Writer}` (**exactly one field**), `PlanFetchOutcome`, `PlanFetchRepoResult` (8 members), `PlanFetchContext`, `PlanFetchPlan`, `PlanGateResult`, `PlanGuardLimits`, `PlanValidationIdentity`, `PlanStageFact`, `CheckoutPlanInspectionRequest`, `CheckoutPlanInspection`, `InspectCheckoutPlan`, `PlanCheckoutRebase`, `BuildCheckoutRebasePlan`, `BuildCheckoutContinuationPlan`. |
| `internal/rebase_plan_state.go` | `internal` | `PlanPresence`, `PlanUnreadableReason`, `PlanFilePresence` (incl. non-published `Err`), `PlanSnapshotFacts`, the five artefact types `PlanCheckoutTransactionFile`, `PlanCheckoutLockFile`, `PlanLegacySyncStateFile`, `PlanSyncRunPayloadFile`, `PlanSyncRunGuardFile`, `PlanWorktreeFacts`, `PlanHeadFacts`, `PlanGitOp`, `CheckoutPlanState`/`CheckoutPlanFiles`/`CheckoutPlanStateOpts`/`CheckoutPlanStateVerdict`+`LiveForeignLock()`, `ExternalPlanState`/`ExternalPlanFiles`/`ExternalPlanStateOpts`+`LiveForeignOwner()`, `InspectCheckoutPlanState`, `InspectExternalPlanState`. |
| `internal/rebase_plan_build.go` | `internal` | `BuildRebasePlan(req) (RebasePlan, error)` and `RevalidatePlanEntry`. |
| `internal/rebase_plan_probe.go` | `internal` | candidate count/first/stream+digest probes; the ordered config inventory + the two typed boolean reads; fetch-effect resolution (producer of `PlanFetchContext.Effect` for **both** modes); submodule reach walk; local-branch destination enumeration; `BranchHoldMechanism` (4 members), `BranchHolderRecord`, `BranchHolderInventory`, `BuildBranchHolderInventory(repoRoot)`, `PlanHolderIndex`, `BuildPlanHolderIndex(ids, need)`; context identity (`PlanContextIdentities`); the read helpers `MeasurePushRemoteFacts`/`RefreshPushTrackingRefs` call; the `Lstat`-first artefact helpers the two inspectors use. **No symbol named in §14.1a is declared here.** |
| `internal/rebase_plan_fingerprint.go` | `internal` | TLV writer, field-id table (retired workspace-root id), `PlanFingerprint`, `RevalidationDigest`, test-only annotated pre-image accessor. |
| `internal/rebase_plan_render.go` | `internal` | `FormatRebasePlan(plan) ([]byte, error)`, `MarshalRebasePlan(plan) ([]byte, error)`. Neither takes an `io.Writer`; neither uses `fmt.Print*`. |
| `internal/git_capability.go` | `internal` | `GitVersion{Probed,OK,Raw,Major,Minor,Patch}`, `ProbeGitVersion() (GitVersion, error)`, `GitCapabilities` with the **six** gates — `pruneTags` @2.17, `CapConfigShowScope` @2.26, `CapDefaultBackendMerge` @2.26, sole-remote fallback @2.37, `CapRebaseUpdateRefs` @2.38, `fetch.all` @2.44 — and the locked parser. |
| `internal/cli/sync_plan_guard.go` | `cli` | `planGuardOptions`, `resolvePlanGuardOptions(cmd)`, `planGuardOptions.checkoutGuard()`, `planLayout(externalSyncLayout) internal.RebasePlanLayout`, `planGuardRun`, `runExternalPlan`, `ExternalPlanInspectionRequest`, `ExternalPlanInspection`, `InspectExternalPlan`, the external guard/JIT seam helpers, `renderPlanDocumentTo(stdout, stderr io.Writer, plan, jsonMode) error`, `renderPlanDocument(cmd, …)`, `writePlanGuardMarker(w, *PlanGuardRefusalError)`, `planGuardRefusal(cmd, err)`; `printSyncModeHeaderTo`/`fetchQuietTo` twins if not placed beside their originals. |

**Single-definition rules the implementer must not violate** (all asserted by §22.33h (ix) and
§22.33d): the six push producers exist **only** in `internal/rebase_planner.go`;
`SelectPrimaryRefusal` is a *function* and `RefusalKind` a *type* — never the same name;
`ControlledPathBlocker` is declared only in `internal/rebase_plan.go` and `GuardBlock.ExecuteBlockedBy`
is `[]ControlledPathBlocker`, never `[]RefusalKind`/`[]string`; the identifier `PlanArtefactPresence`
is declared in **no** package.

---

## 5. CHANGED files and symbols — §19.2 reconciled against the tree

`✚` marks a **new declaration inside an existing file** (an easy thing to miss when reading §19.2 as
"changed files"). `~` marks an edit to a shipped symbol.

### `internal/cli/sync.go`
- `~` `syncCmd`: five flags at `:132-142`; `Long` on the literal at `:25`; `resolvePlanGuardOptions`
  after `:45`; `cmd` threaded into `runCheckoutSync` `:51`; plan dispatch after `:77`; deferred-I7
  predicate at `:83`; I20 predicate at `:97`; cell-5 three-arm dispatch at `:100`; cell-7 two-arm at
  `:103`; the new `syncCellLiveGuardRefusal` call and cell-4 interception above `:87`;
  the sentinel-`absent` arm's one fresh `classifySyncState` call plus direct, no-re-entry dispatch;
  `planGuardRefusal` around the return.
- `~` `handleSyncAbortCell` `:262`: **new** `case 1:` and `case 7:` arms; `case 4:` byte-frozen.
- `~` `handleSyncAbort` `:294`: becomes the printing wrapper over `✚ abortLegacySyncState`.
- `~` `syncFeature` `:535`: fetch loop `:543-546` → `✚ fetchStackReposTo`; `plan-unavailable`
  interception on the `:536` `LoadStack` error arm above `syncFallback`.
- `~` `syncFeatureScoped` `:559`: fetch block → `✚ fetchScopedReposTo`; remainder →
  `✚ syncFeatureScopedPlanned` (no fetch, no `TopoSort`, no `SyncStageFetching`).
- `~` `fetchQuiet` `:604`: becomes the `os.Stdout`/`os.Stderr`, zero-context, result-discarding
  wrapper over `✚ fetchQuietTo(out, errw, repo, wtPath, verbose, ctx) PlanFetchRepoResult`.
- `~` `verifySelectedBasesLocally` `:241`: `✚ firstUnresolvedSelectedBase` carved out (§10.7).
- `~` `scopedSelectionFromPayload` `:480`: `✚ scopedSelectionFromPayloadOrder` carved out; the
  wrapper keeps `:486`.
- `✚` `syncTriggersNeedV2`, `✚ syncStateRefusesContinueAbort`, `✚ runGuardedScopedSync`,
  `✚ runGuardedLegacySync`, `✚ handleGuardedScopedSyncContinue`, `✚ handleGuardedLegacySyncContinue`
  (exact signature in §12.3/§19.2, identical in shape to the scoped twin plus one
  `sentinel internal.GuardedLegacySentinelView`).
- **Untouched:** `runScopedSync` `:151`, `handleScopedSyncContinue` `:363`, `handleSyncContinue`
  `:311`, `runNewModePush` `:216`, `finalizeScopedSyncRun` `:231`, `syncContinueMismatches` `:435`,
  `payloadCompleted` `:468`, `branchContainsConfiguredParent` `:515`, `isRebaseInProgress` `:524`,
  `selectedRealEntries` `:579`, `syncRepoContext` `:593`, both `RegisterFlagCompletionFunc` calls
  `:144-145`.

### `internal/cli/sync_helpers.go`
- `~` `syncResult` `:12` gains `Refusal *internal.PlanGuardRefusalError`.
- `~` `syncWithStack` `:70`, `syncWithStackFiltered` `:74`, `syncWithStackScoped` `:78` gain a
  trailing `guard *planGuardRun` (`nil` unguarded).
- `~` `syncRunContext` `:21` gains `Route` and `Validation`; `scoped()` `:27`, `selects()` `:31`,
  `skipsAnchor()` `:38` answer for `Route == legacy` exactly as `run == nil` answers today.
- `~` pass-1 JIT seam at `:117-135`; pass-2 JIT seam immediately before the Git call at `:196-201`.
- `✚` `syncStepHook(internal.SyncStageRebasing, -1)` immediately before the pass-1 seam's first
  post-claim revalidation probe; it uses the shipped hook variable and belongs in this file, beside
  the existing per-entry hook at `:136`.
- `~` `runValidation` `:401-403` takes the frozen validation value.
- `~` `syncFallback` `:390` made unreachable from planned/guarded routes (by the caller, not by an
  edit to its body).

### `internal/cli/sync_modes.go`
- `~` `setupSyncRunState` `:385` gains exactly one trailing `birth syncRunStateBirth`, applied
  **before** the first `SaveSyncRunState`.
- `~` `syncCellRefusal` `:302`: its first `switch` becomes the pure extraction
  `✚ syncCellLiveGuardRefusal(feature, st, verb)`, called as its own first statement.
- `~` `printSyncModeHeader` `:502` becomes a one-line wrapper over `✚ printSyncModeHeaderTo(w, p)`.
- `✚` `syncRunStateBirth{StateVersion, Route, MaxPerEntry *int, MaxTotal *int}`,
  `✚ guardedLegacyCarry{Completed, FailedBranch, PriorPendingCount}`, `✚ syncStateResidue`,
  `✚ rollbackGuardedRunState(featurePath) (syncStateResidue, error)`,
  `✚ setupGuardedLegacyRunState(...)` (with its own `syncStepHook(SyncStageInitializing, 3)` capture
  hook), `✚ rollbackGuardedLegacyRunState(featurePath, prior, sentinel []byte, made guardedLegacySetupProgress)`,
  `✚ guardedLegacySetupProgress`, `✚ upgradeGuardedSyncRunState(layout, payload, limits) error`.
- **Untouched:** `syncTriggerFlags` `:31`, `syncPresenceFlags` `:34`, `resolveSyncPolicy` `:41`,
  `classifySyncState` `:272`, `syncSymlinkRefusal` `:281`, `clearSyncRunState` `:433`,
  `externalSyncLayout` `:176`, `resolveExternalSyncLayout` `:192`.

### `internal/cli/checkout_sync.go`
- `~` `runCheckoutSync` `:15` → `runCheckoutSync(cmd *cobra.Command, ws, opts)`; `:33-34` hoisted
  above `:22`; containment evaluated once into a `PlanGateResult`; plan dispatch below `:17`;
  `:48` → `✚ checkoutTriggersNeedV2(tx, loadErr)`; `PersistedGuarded` set between the I20 refusal and
  `:52`; `planGuardRefusal` around `:52` and `:64`.

### `internal/checkout_sync.go`
- `~` `CheckoutSyncOpts` `:545` gains `PlanGuard CheckoutPlanGuard` (a **separate** field, never
  merged into `Changed`).
- `~` `CheckoutTransaction` `:63-101` gains `MaxReplayPerEntry *int`, `MaxReplayTotal *int`,
  `Route string`, all `omitempty` (§13.1).
- `~` `BuildCheckoutPlan` `:464` → shipped-signature wrapper doing the one unguarded `TopoSort`
  (`:465`) and building `RebasePlanLayout{RepoRoot: repoDir}`, over `✚ buildCheckoutPlanFrom(repoDir,
  stack, order, sel)`; **D1 fixed inside the body** (§9.2).
- `~` `verifyCheckoutBasesLocally` `:693`: `✚ firstUnresolvedCheckoutBase` carved out; the only
  plan-route caller is `InspectCheckoutPlan`.
- `~` the inline fetch body `:618-625` → `✚ fetchCheckoutRepoTo(w io.Writer, ctx PlanFetchContext)
  PlanFetchOutcome`, with the shipped call site going through the **new** `✚ fetchCheckoutRepo(repoDir)`
  wrapper; `~ printSyncModeHeader` `:519` → one-line wrapper over `✚ printSyncModeHeaderTo(os.Stdout, p)`.
- `~` `RunCheckoutSync` `:563`: guarded arm calls `InspectCheckoutPlan` above `:612`, refuses on
  `insp.SortErr` (and legacy `insp.StackErr`) there; `fetchCheckoutRepoTo` over the shipped
  inline-fetch body at `:618-625`;
  `buildCheckoutPlanFrom(..., insp.Order, ...)` at `:641`; guard seam between `:641` and `:684`;
  v3 fields on the `:657` literal.
- `~` `ContinueCheckoutSync` `:718`: guard seam after `:740`; `✚ upgradeGuardedCheckoutTransaction`
  below it and above `:753`; `:723` upper bound → `> CheckoutTransactionGuardedVersion`; `:728`
  and `:749` gates → `✚ txNewMode`/`✚ TransactionNewMode`.
- `~` `AbortCheckoutSync` `:823` deferred-I7 disjunct → `✚ checkoutRecoveryIsNewMode`.
- `~` `resumeFromSwitched` `:933-939`: dedicated JIT seam honouring the pinned-destination rule and
  the HEAD-identity preflight of §13.3a.
- `~` `processBranch` `:1024-1030`: JIT seam at the re-resolution.
- `✚` `checkoutRecoveryIsGuarded`, `✚ TransactionGuarded` (both nil-safe).
- **Untouched:** `finalizeTransaction` `:1161` in full and its
  `StepHook(StageRestoring, …)` site at `:1207` (not moved); the shipped
  `StepHook(StageRebased, …)` site in `doRebase` at `:1149` (reused by tests, not moved);
  `finalizeCleanup` `:1237`, `restoreOriginal` `:1257`, `executeTransaction` `:850`,
  `doRebase` otherwise `:1100`, every `git*` helper `:320-447`.

### `internal/sync_selection.go` — split only, no behaviour change
- `~` `ResolveSyncSelection` `:165` loses its opening `sorted, err := TopoSort(stack)` (`:166`);
  the remainder becomes `✚ ResolveSyncSelectionFromOrder(stack, order []StackEntry, policy, opts)`,
  iterating the supplied `order` exactly where the shipped body iterated `sorted`. The wrapper keeps
  the shipped signature and sorts, so `internal/cli/sync.go:156`, `:486` and
  `internal/checkout_sync.go:597` are untouched and every error string is byte-identical.

### `internal/sync_run_state.go`
- `~` `SyncRunState` `:38-63` gains the three §13.1 keys.
- `~` `SaveSyncRunState` `:122-130`: **`s.StateVersion = SyncRunStateVersion` at `:123` must become
  "preserve non-zero, default zero, refuse other"** — without this the guarded envelope is
  overwritten on its first save.
- `~` `LoadSyncRunState` `:105` accepts `2` **and** `3`.
- `~` `ReclaimSyncRunGuard` `:207` rewired over `✚ checkSyncRunGuardReclaimable` (§12.4).
- `✚` `SyncRunStateGuardedVersion` / `CheckoutTransactionGuardedVersion` (beside `SyncRunStateVersion`
  `:19` and `CheckoutTransactionVersion` `:22`), `✚ PayloadNewMode(*SyncRunState) bool` (nil-safe),
  `✚ RemoveSyncRunState`/`✚ RemoveSyncRunGuard` (error-returning) with `DeleteSyncRunState` `:133`
  and `ReleaseSyncRunGuard` `:249` becoming discarding one-liners, `✚ SyncGuardReleaseReason`,
  `✚ SyncGuardRelease` (incl. `Self`), `✚ SyncGuardReleaseOpts`, `✚ ReleaseStaleSyncRunGuardWith`,
  `✚ ReleaseStaleSyncRunGuard`.
- **Untouched:** `NewSyncRunState` `:144-169` (signature *and* body), every `SyncRunStage` constant
  `:28-35`, `writeSyncGuardExclusive` `:253`, the guard document's `state_version: 2`.

### `internal/syncstate.go` — additive only
- `✚` `RemoveSyncState(featurePath) error` with `DeleteSyncState` `:43` becoming its discarding
  wrapper; `✚ ErrSyncStateChanged`/`✚ ErrSyncStateHasPayload`;
  `✚ RestoreSyncStateBytes(featurePath, prior, expect []byte) error`;
  `✚ RemoveSyncStateIfUnchanged(featurePath, expect []byte) error`;
  `✚ GuardedLegacySentinelVersion`, `✚ GuardedLegacySentinel` (the shipped `SyncState` inline plus
  extension keys), `✚ SaveGuardedLegacySentinel(featurePath, s, expect []byte) error`,
  `✚ GuardedLegacySentinelVerdict` (10 values: `not-applicable`, `absent`, `not-guarded`, `valid`,
  `symlink`, `unreadable`, `unsupported-version`, `corrupt`, `foreign`, `hash-mismatch`),
  `✚ GuardedLegacySentinelView{Verdict, Path, Sentinel, Prior, Version, Err}`,
  `✚ InspectGuardedLegacySentinel(featurePath, feature) GuardedLegacySentinelView`,
  `✚ GuardedLegacySentinelResumable(st, v) bool`.
- **Untouched:** `SyncStatePath` `:19`, `LoadSyncState` `:23`, `SaveSyncState` `:35`, `HasSyncState`
  `:47`, `NewSyncState` `:52`. **No new runtime path** — the sentinel *is* `.sync-state.yaml`.

### `internal/exec.go` — additive only
- `✚` `RunTo(stdout, stderr io.Writer, name string, args ...string) error`
  `✚ RunDirTo(stdout, stderr io.Writer, dir, name string, args ...string) error` — each the body of
  its shipped twin (`Run` `:117`, `RunDir` `:126`) with `cmd.Stdout`/`cmd.Stderr` from parameters;
  `cmd.Stdin = os.Stdin` and `cmd.Dir` unchanged; no filtering, buffering or reordering.
- **Untouched:** every shipped body, including `RunDirClean` `:138`, `runWithFilteredStderr` `:142`
  and the mirrored (not fixed) `IsPrunableWorktree` `:201`.

---

## 6. Explicitly untouched — §19.3 verified in the tree

| Claim | Verification |
|---|---|
| `internal/config.go` — no configuration key | `LoadConfig()` untouched; the guard reads `TestCommand` through it exactly once per controlled route |
| `internal/stack.go` — `TopoSort` `:138`, `UniqueRepos` unchanged | callers move; the function does not |
| `internal/stack_ancestry.go` — helpers reused unexported | `ancestrySanitize` `:551`, `ancestryCommandToken` `:578` etc. are same-package; **no export, no edit** |
| `internal/agent_status.go` — `BuildWorktreeInventory` `:527` reused as-is | called by `BuildBranchHolderInventory` and nothing else on this path; `WorktreeRecord` `:476` / `WorktreeInventory` `:497` (`Available`, `ByBranch`, `Prunable`, `Records`, `ByPath`, `Err`) consumed unchanged |
| `internal/stack_status.go` — `BuildBranchRefInventory` `:427-447` consumed verbatim | one `git for-each-ref`; `BranchRefInventory{Available, ByRef, Err}` `:418-422` is already fail-closed, which is what §25.102 input 2 requires |
| `internal/cli/importcmd.go` — `isRuntimeState` `:177-184` | exact-name list stays at three names; no new runtime path is introduced |
| `internal/cli/push.go` | push behaviour unchanged; only projected |
| `finalizeTransaction` body `internal/checkout_sync.go:1161-1235` | no **new** seam, hook site, guard verdict or write; its shipped `StepHook(StageRestoring, …)` call at `:1207` is unmoved |
| `internal/checkout_health.go` | transaction decode is already version-agnostic: `LoadCheckoutTransaction` (`internal/checkout_sync.go:121-131`) has no version gate, and checkout health unmarshals directly at `internal/checkout_health.go:416`; no edit |
| `writeSyncGuardExclusive` `internal/sync_run_state.go:253` and the guard `state_version: 2` | unchanged |
| `internal/cli/testdata/sync_noflag/**` (126 files) | zero diffs |

---

## 7. Type-to-file map (every concern the task enumerates)

### 7.1 `RebasePlanRequest` and `RebasePlan`
Both in `internal/rebase_plan.go` (**NEW**). `RebasePlanRequest` has five field groups (§9.1a):
identity/boundary (`Layout`, `Mode`, `Feature`, `Workspace`); the effective run (`Route`,
`RequestedRoute`, `RouteTriggers`, `Invocation`, `Policy`, `PolicyFetchDefaultApplied`, `Push`,
`PushSource`, `Guard`, `Limits`, `LimitConflicts`, `Validation`, `Approve`); the subject (`Stack`,
`Order`, `SortErr`, `StackErr`, `Selection`, `SelectionResolved`, `SelectionErr`, `RowsAvailable`);
continuation inputs (`Continue`, `Remaining`, `StageFacts`, `Changed`, `ContinuationGate`); and
already-measured facts (`Fetch`, `FetchPlan`, `PushFacts`, `BasePreflight`, `ExternalState`,
`CheckoutState`, `Gates`, `Version`, `Capabilities`). **`Gates` is `[]PlanGateResult`, never
`[]PlanBlocker`; `PushFacts` is the single `PlanPushFacts` carrier, never a map.**
`RebasePlan` has exactly the twenty-five §4.1 keys in §4.1 order with JSON tags:
`schema_version, route, requested_route, route_triggers, invocation, workspace, feature, policy,
intent, push, restore, fetch, freshness, repositories, state, runnable, blockers, warnings,
encoding_issues, config_issues, entries, summary, guard, refusal, approval`.

**Constructed in exactly three places**: `runExternalPlan` (`internal/cli/sync_plan_guard.go`),
the two guarded external routes, and `BuildCheckoutRebasePlan`/`BuildCheckoutContinuationPlan`
(`internal/rebase_plan_guard.go`). No `map[string]any` staging document may exist (§22.33g).

### 7.2 Nested schema types
All in `internal/rebase_plan.go` except the four families deliberately relocated:
- runtime-snapshot types → `internal/rebase_plan_state.go`;
- writer/fetch/gate/inspection types → `internal/rebase_plan_guard.go`;
- fingerprint field-id table → `internal/rebase_plan_fingerprint.go`;
- `GitVersion`/`GitCapabilities` → `internal/git_capability.go`.

### 7.3 TLV field table and fingerprint
`internal/rebase_plan_fingerprint.go` (**NEW**): the TLV writer, the field-id table with the
**retired workspace-root id** (§8.3 — `workspace.repo_root` is display-only and a fingerprint
non-member), `PlanFingerprint(plan) (string, []PlanEncodingIssue)`, `RevalidationDigest`, plus the
test-only annotated pre-image accessor that §23.1 seam 5 requires. `route_triggers[]` is likewise a
fingerprint non-member (§22.33b).

### 7.4 Context IDs
`internal/rebase_plan_probe.go` owns `PlanContextIdentities` — the **one** table every `context_id`
comes from — and `internal/rebase_planner.go`'s `EntryContexts` is the sole producer of the
base/execution context pair. `context_id` is the only join key between `entries[]`,
`repositories[]`, `fetch.repos[]`, `push.targets[]` and `restore`; `null == null` is a legal join
and `null` sorts first (§4.8, §22.33b).

### 7.5 Gates
`PlanGateResult` in `internal/rebase_plan_guard.go`. The ladders are produced by
`InspectCheckoutPlan` (checkout, gates a–l) and `InspectExternalPlan` (external), each **typed**
and in shipped order. The **only** gate→blocker conversion is
`GateBlockers`/`GateControlledTokens` in `internal/rebase_planner.go`, called by `BuildRebasePlan`
and by nothing else (§13.5 rule G). The projected checkout lock ladder is the *whole* native ladder
of `AcquireCheckoutLock`/`forceAcquireCheckoutLock`, eight rows, each mapped to the shipped sentence
(§13.7's table; rows 6 and 8 are deliberately **not** synthesized).
`CheckoutPlanInspectionRequest.Containment internal.PlanGateResult` is gate a's copied-in value:
`runCheckoutSync` evaluates `os.Getwd`/`GitRepoRootIn` once, and the inspector consumes that exact
verdict without probing containment again.

### 7.6 Push facts (§14.1a)
Two measurement phases, three files:
- **MAPPING half** — `MeasurePushRemoteFacts(ctx, ids) PlanPushRemoteFacts` (declared in
  `internal/rebase_planner.go`, read helpers in `internal/rebase_plan_probe.go`): canonical common
  dir, `origin` existence, `remote.origin.fetch` decomposed into `[]PlanRefspec` in configured
  order, and `MappingReadOK`. Measured **above** the fetch, once per distinct push context of an
  **applicable** `PlanPushFacts`.
- **BASELINE half** — `RefreshPushTrackingRefs(facts, fetched) PlanPushFacts`: the remote-tracking
  ref inventory, read **after** the route's fetch (external step 7a; checkout step 4a/3a).
- **Pure projection** — `PushTargets(req PlanPushRequest) []PlanPushTarget`, `ResolvePushContext`,
  `ResolvePushLease`, `PushContextRefreshed`: **zero** Git processes, asserted over hand-built
  requests (§22.33h).
`Phase` values: `pre-fetch` on every applicable inspection, `post-fetch`/`plan-point` after the
refresh, `not-applicable` when `intent.push` is false. `Applies && Phase == "pre-fetch"` reaching
the builder is a defect.

### 7.7 State inspectors (§12.5, §12.5a)
`internal/rebase_plan_state.go` (**NEW**) is the **definition site** of the runtime snapshot.
`InspectExternalPlanState(featurePath, ExternalPlanStateOpts{Classified: state})` takes the
classifier result (`internal/sync_run_state.go:343`) as **input**, never as the snapshot;
`InspectCheckoutPlanState(opts, CheckoutPlanStateOpts)` returns **both** a `CheckoutPlanState` and a
`CheckoutPlanStateVerdict`. Both are `Lstat`-first, never follow a symlink, do one decode per
regular file, and take no lock, no fetch and no write. `RebasePlanRequest.ExternalState`/
`.CheckoutState` carry the opposite mode's `Applicable: false` rows (§12.5a rule 5), and
`BuildRebasePlan` renders `state.*` from those alone — it performs **no** filesystem probe for those
rows (rule 6).
The five artefacts: `.checkout-sync.tx.yaml`, `.checkout-sync.lock`, `.sync-state.yaml`,
`.sync-state.v2.yaml`, `.sync-run.lock` — paths from `CheckoutTransactionPath`
(`internal/checkout_sync.go:111`), `CheckoutLockPath` (`:115`), `SyncStatePath`
(`internal/syncstate.go:19`), `SyncRunStatePath` (`internal/sync_run_state.go:93`),
`SyncRunGuardPath` (`:98`).

### 7.8 Capability model (§16)
`internal/git_capability.go` (**NEW**). `ProbeGitVersion()` has **exactly two call sites** —
`InspectCheckoutPlan` and `InspectExternalPlan` — once each per controlled invocation, issued
**after** the subject read and **before** the first config / fetch-effect / `git fetch` process, and
**skipped entirely** on a rows-less no-subject document (`Version.Probed == false`, no rank 5.9).
Six independent gates; the two `2.26` rows are separate fields (`CapConfigShowScope` **refuses**;
`CapDefaultBackendMerge` merely models the apply arm); `CapRebaseUpdateRefs` @2.38 is the second
refusing gate and is **argv-derived** — it fires only where a published `entries[].argv` really
carries `--update-refs`, which today means the unscoped pass-1 argv at
`internal/cli/sync_helpers.go:123-126`, never a scoped run and never checkout.

### 7.9 Holder inventories (§14.4)
`internal/rebase_plan_probe.go` (**NEW**) owns `BranchHoldMechanism` (four members: `checked-out`
plus detached `rebase-merge`, `rebase-apply`, `bisect`), `BranchHolderRecord`,
`BranchHolderInventory`, `BuildBranchHolderInventory(repoRoot)` and the per-invocation
`PlanHolderIndex` / `BuildPlanHolderIndex(ids, need)`. `BuildBranchHolderInventory` performs
**exactly one** `internal.BuildWorktreeInventory` call (`internal/agent_status.go:527`) — its only
`git worktree list --porcelain` process — keys holders by **short branch name**, keeps prunable
records as holders, adds the three detached holders from bounded symlink-free reads of each detached
worktree's own Git directory, canonicalizes paths by the §18.3 rule, and fails closed
**whole-inventory, per repository**. `BuildPlanHolderIndex` calls it once per **distinct canonical
common dir** among contexts that need a holder fact. It is the single producer of
`branch_checked_out_at`, the rank 5.4 question, §11.8's collateral exclusion, §11.6's `held[]` rows
and the document-level `restore-target-held` question.

### 7.10 Selection-from-order and the checkout adapter
- `ResolveSyncSelectionFromOrder` (**NEW**, `internal/sync_selection.go`) is the order-taking body;
  it **MUST NOT** call `TopoSort`. `ResolveSyncSelection` (`:165`) keeps its signature and sorts.
- `buildCheckoutPlanFrom(repoDir, stack, order, sel)` (**NEW**, `internal/checkout_sync.go`) is the
  order-taking plan body; `BuildCheckoutPlan` (`:464`) keeps its shipped
  `(repoDir string, stack Stack, sel SyncSelection) ([]CheckoutPlanEntry, error)` signature, does
  the one unguarded `TopoSort` (`:465`) and builds `RebasePlanLayout{RepoRoot: repoDir}` itself
  (§9.0 rule 2). D1 is fixed inside the body: the two declared cells are **D1-a** (a decoupled
  in-stack parent whose run fails today now succeeds) and **D1-b** (a *silent* destination change
  where a real Git branch shares an in-stack parent's logical name). `StackEntry.Name` identifies
  the tws entry; `StackEntry.GitBranch()` identifies the Git branch — that rule is what decides D1-b.

---

## 8. Fetch writers, result helpers and `RunTo`/`RunDirTo`

| Concern | Today | After |
|---|---|---|
| external fetch body | `fetchQuiet(repo, wtPath, verbose)` `internal/cli/sync.go:604-635`, `void`, prints with `fmt.Printf`, verbose arm calls `internal.RunDir`/`internal.Run`, quiet arm `RunSilentDir`/`RunSilent` | `fetchQuietTo(out, errw io.Writer, repo, wtPath string, verbose bool, ctx PlanFetchContext) PlanFetchRepoResult` **NEW** — fills **all eight** members from its own cwd ladder plus the pre-fetch context; `fetchQuiet` becomes its `os.Stdout`/`os.Stderr`, zero-context, **result-discarding** wrapper |
| legacy fetch loop | inline `internal/cli/sync.go:543-546` | `fetchStackReposTo(out, errw, layout, stack, verbose, plan)` **NEW**; shipped call passes `os.Stdout, os.Stderr` and the **zero** `PlanFetchPlan` |
| new-mode fetch loop | inline `internal/cli/sync.go:560-566` (inside `syncFeatureScoped`, with the `SyncStageFetching` write at `:561-562`) | `fetchScopedReposTo(out, errw, layout, stack, sel, verbose, plan)` **NEW** — **payload-free by signature**, so it structurally cannot write a stage |
| checkout fetch | **inline** `internal/checkout_sync.go:618-625` inside the `opts.NewMode`/fetch-policy gates — there is no function today | `fetchCheckoutRepoTo(w io.Writer, ctx PlanFetchContext) PlanFetchOutcome` **NEW** plus the **NEW** `fetchCheckoutRepo(repoDir)` wrapper the shipped call site uses (see resolved §19 P-3) |
| child process output | `Run` `internal/exec.go:117`, `RunDir` `:126` | `RunTo`/`RunDirTo` **NEW**, additive; called **only** from `fetchQuietTo`'s verbose arm, passing through the two writers it received. Under the shipped wrapper those are exactly `os.Stdout`/`os.Stderr`, so byte-for-byte the shipped wiring |

**Non-negotiable**: `Run`, `RunDir`, `RunDirClean`, `runWithFilteredStderr`, `RunSilent`,
`RunSilentDir` keep byte-identical bodies and are **not** rewritten as wrappers, so no executing
route's process wiring changes. `RunDirTo(os.Stdout, os.Stderr, dir, …)` must be observationally
identical to `RunDir(dir, …)` (§22 stream-plumbing row).

**Document writers**: `FormatRebasePlan`/`MarshalRebasePlan` build the complete document in memory,
return `[]byte`, take **no** writer and use **no** `fmt.Print*`. `renderPlanDocumentTo(stdout,
stderr io.Writer, plan, jsonMode)` writes what they return in **exactly one** `Write`.
`PlanWriters` has **exactly one field** (`Prose`) — no document writer crosses into `internal`.

---

## 9. State versions, load/save, sentinel, crash hooks, removers, recovery cells

### 9.1 Versions
```
internal/sync_run_state.go:19   const SyncRunStateVersion = 2            (unchanged)
internal/sync_run_state.go:22   const CheckoutTransactionVersion = 2     (unchanged)
                          NEW   const SyncRunStateGuardedVersion = 3
                          NEW   const CheckoutTransactionGuardedVersion = 3
```
Birth is decided **once**, at the run's birth, by whether the run is guarded. There are exactly
**three birth sites** (`setupSyncRunState`, `setupGuardedLegacyRunState`, the `CheckoutTransaction`
literal at `internal/checkout_sync.go:657`) and exactly **two upgrade writers**
(`upgradeGuardedSyncRunState`, `upgradeGuardedCheckoutTransaction`), each in **one owner file**
(§13.6 rule 2a binding 6; asserted as a source-level enumeration by §22.24i (x)).
The sole external upgrade call is in `handleGuardedScopedSyncContinue`, immediately after a
successful `ReclaimSyncRunGuard` and before header/prose or Git. The cell-7/cell-4 legacy arms
create their v3 payload through `setupGuardedLegacyRunState` at step 9; an already-v3 legacy
envelope calls neither upgrade writer.

### 9.2 Load / save
- `LoadSyncRunState` `internal/sync_run_state.go:105` accepts `2` **and** `3`.
- `SaveSyncRunState` `:122-130` — the line `s.StateVersion = SyncRunStateVersion` at **`:123`** is
  the single most dangerous shipped statement for this feature: it force-writes `2` on **every**
  save. It must become preserve-non-zero / default-zero / refuse-other. Missing this makes the v3
  envelope silently self-downgrade on the first progress write.
- `ContinueCheckoutSync`'s upper bound `internal/checkout_sync.go:723` becomes
  `> CheckoutTransactionGuardedVersion`.

### 9.3 Route derivation — the three replaced comparisons
| Shipped comparison | Site | Replacement |
|---|---|---|
| `tx.StateVersion >= CheckoutTransactionVersion` (mismatch/selection gate) | `internal/checkout_sync.go:728` | `TransactionNewMode(tx)` |
| `tx.StateVersion >= CheckoutTransactionVersion` (header gate) | `:749` | `TransactionNewMode(tx)` |
| `tx.StateVersion >= CheckoutTransactionVersion` (deferred-I7 disjunct) | `:823` | `checkoutRecoveryIsNewMode(tx)` |
Plus the CLI I20 at `internal/cli/checkout_sync.go:48` → `checkoutTriggersNeedV2(tx, loadErr)`,
which **keeps** the `loadErr != nil || tx == nil` disjuncts verbatim, and the external I20 at
`internal/cli/sync.go:97` → `syncTriggersNeedV2(state)` (cells 1, 7 always; cell 5 only when
`!PayloadNewMode(state.Payload)`; **no cell-4 arm**), both at their **shipped positions**.
Equivalence obligation: for an absent `route`,
`!TransactionNewMode(tx) ≡ tx.StateVersion < CheckoutTransactionVersion`.

### 9.4 `GuardedLegacySentinel` (§13.6 rule 2c)
Lives in `internal/syncstate.go`; the runtime path is **still `.sync-state.yaml`** — no new file
name, so `isRuntimeState` (`internal/cli/importcmd.go:177`) stays at three names. The document is
the shipped `SyncState` **inline** plus extension keys, so an old binary still decodes it as legacy
state (which is exactly why the downgrade sentences of §13.6 rule 5 hold). Written only through the
**conditional** `SaveGuardedLegacySentinel(featurePath, s, expect []byte)`; undone only through
`RestoreSyncStateBytes` (refusing an empty `prior` rather than truncating) or
`RemoveSyncStateIfUnchanged` — **never** the unconditional `RemoveSyncState`. Read by
`InspectGuardedLegacySentinel` (one `Lstat`, at most one read of a regular file, never follows a
symlink, prints nothing, refuses nothing); dispatched by `GuardedLegacySentinelResumable`, evaluated
on **cell 4 and no other cell**.

### 9.5 §13.2a canonical four-step order and its crash windows
`setupGuardedLegacyRunState` performs, in this order: **capture (1) → `ClaimSyncRunGuard` (2) →
conditional backup sentinel (3) → payload (4)**, firing `syncStepHook(SyncStageInitializing, …)`
with indices **3 → 0 → 1 → 2** (index 3 is the **new** capture hook; 0/1/2 are the shipped artefact
indices already used by `setupSyncRunState` at `internal/cli/sync_modes.go:389` and below).
Rollback (`rollbackGuardedLegacyRunState`) undoes in reverse creation order — payload → conditional
sentinel undo → guard released **last** — and returns the **measured residue** rather than pretending
on failure. Residue is reachable only from a process that died, never from a returned error.

### 9.6 Error-returning removers and post-claim cleanup
`RemoveSyncState`, `RemoveSyncRunState`, `RemoveSyncRunGuard` (all **NEW**, error-returning), with
`DeleteSyncState` `internal/syncstate.go:43`, `DeleteSyncRunState` `internal/sync_run_state.go:133`
and `ReleaseSyncRunGuard` `:249` becoming one-line discarding wrappers. `rollbackGuardedRunState(featurePath)
(syncStateResidue, error)` (**NEW**, `internal/cli/sync_modes.go`) is the §12.2c post-claim rollback and
is the **only** caller that treats a removal error as real. The three `os.Remove` sites of §12.2c rule 2
are the seam §23.1 seam 4 must be able to fail.

### 9.7 The three no-flag recovery cells (declared behaviour changes)
| Cell | Residue | Verb | New behaviour | Owner |
|---|---|---|---|---|
| 1 (§12.8) | `.sync-run.lock` alone | `--abort` | `ReleaseStaleSyncRunGuard` — a precise release sentence, or a specific refusal, instead of `Nothing to abort — no sync in progress.` | **new** `case 1:` in `handleSyncAbortCell` `internal/cli/sync.go:262` |
| 7 (§12.8a) | real `.sync-state.yaml` + stale/self guard | `--abort` | `ReleaseStaleSyncRunGuardWith(…, SyncGuardReleaseOpts{AllowSelfPID: true})` then `abortLegacySyncState`; prints `Sync state cleared; stale sync guard from PID <pid> cleared.` | **new** `case 7:` arm + `abortLegacySyncState` extraction |
| 4 (§12.8b) | valid guarded backup sentinel | plain / `--continue` / `--abort` | plain names **both** verbs (exit 1); `--continue` **resumes**; `--abort` prints `Sync state cleared; the interrupted guarded setup's backup of the previous sync state was discarded.` | the **one** interception between `classifySyncState` `:77` and `syncCellRefusal` `:88`, below the extracted `syncCellLiveGuardRefusal` |

Every non-`valid`/non-`not-guarded` sentinel verdict fails closed with a sentence that is
**byte-identical across all three verbs** and removes nothing. A `not-guarded` (plain) sentinel keeps
every shipped cell-4 answer verbatim, and `handleSyncAbortCell`'s `case 4:` body stays byte-for-byte.

---

## 10. The one-sort rules and `RemainingRebaseEntries`, mapped to call sites

### 10.1 Sort ledger
| Route | Shipped calls | Sites | Controlled calls |
|---|---|---:|---|
| external new-mode fresh | **2** | `sync.go:156` (inside `ResolveSyncSelection`), `sync.go:568` | **1** (inside `InspectExternalPlan`) |
| external legacy fresh | **1** | `sync.go:548` | **1** |
| external scoped continuation | **2** | `sync.go:486` (inside `ResolveSyncSelection`), `sync.go:411` | **1** |
| external legacy continuation | **1** | `sync.go:343` | **1** |
| checkout new-mode fresh | **2** | `internal/sync_selection.go:166` via `checkout_sync.go:597`, `checkout_sync.go:465` via `:641` | **1** (inside `InspectCheckoutPlan`) |
| checkout legacy fresh | **1** | `checkout_sync.go:465` | **1** |
| checkout continuation | **0** | — | **0** |

**MUST NOT call `TopoSort`**: `ExecutionOrder`, `ResolveSyncSelectionFromOrder`,
`buildCheckoutPlanFrom`, `RemainingRebaseEntries`, `PushTargets`, `ResolvePushContext`,
`DestinationDeferred`, `BuildRebasePlan`, `syncFeatureScopedPlanned`,
`handleGuardedScopedSyncContinue`, `handleGuardedLegacySyncContinue`, both checkout builders, and
the §25.102 stack-identity reload at the JIT seam.

### 10.2 `RemainingRebaseEntries(route, layout, state, order, sel) []string`
`internal/rebase_planner.go` (**NEW**). External formula:
`universe := order` (the invocation's single sort, never persisted `Pending`, never a second sort);
`selected := run.selects(name)` (scoped: rebuilt from `payload.Selected`; legacy: everything —
`run == nil` or `Route == legacy`); `done := set(Completed) ∪ {FailedBranch}` (from the payload on a
scoped/envelope arm, from `.sync-state.yaml` on the cell-7 arm); `remaining` in `universe` order.
`--local-only` anchors stay as `skipped-anchor` rows.

Checkout stage table → current call sites in `resumeTransaction`
(`internal/checkout_sync.go:866-931`):

| `tx.Stage` | current index | later | resume path |
|---|---|---|---|
| `planned` `:911` | included | included | `executeTransaction` `:850` |
| `switched` `:895` | **included, pinned-destination arm (§13.3a)** | included | `resumeFromSwitched` `:933-939` → `doRebase` **directly** |
| `rebasing` `:911` | included | included | `executeTransaction` |
| `conflict` `:868` | excluded | included | ancestry gate then `resumeFromRebased` |
| `rebased` `:899` | excluded | included | `resumeFromRebased` `:941` |
| `validating` `:903` | excluded | included | `resumeFromValidating` `:988` |
| `restoring` `:907` | none | none | `resumeFromRestoring` `:1010` — the restore **is** the work |
| `completed` `:924` | none | none | `finalizeCleanup` `:1237` — push retry + delete + release **is** the work |

`CompletedIndices` (`internal/checkout_sync.go:91`) **MUST NOT** be the remaining-work source.
The plan, the guard seam and the JIT seam consume the **same** result, so document, seams and token
cannot disagree. Both rows-less stages still publish `restore.applies: true`.

---

## 11. JIT revalidation — inputs, seams, and what refreshes

### 11.1 Seams (all REQUIRED, §10.3)
| Route | Guard seam | JIT seam(s) |
|---|---|---|
| external legacy fresh | `runGuardedLegacySync`, after `fetchStackReposTo` + plan, before the first rebase | `sync_helpers.go:117-135` (pass 1), `:196-201` (pass 2) |
| external new-mode fresh | `runGuardedScopedSync`, after `fetchScopedReposTo` + plan, **above** `setupSyncRunState` | same |
| external legacy continuation | `handleGuardedLegacySyncContinue`, after the projected ladder + shipped prose, above the first write | same |
| external scoped continuation | `handleGuardedScopedSyncContinue`, after the ladder + reclaim precheck, **above** `ReclaimSyncRunGuard` `sync.go:400` | same |
| checkout fresh | after `buildCheckoutPlanFrom` (`:641`), before `SaveCheckoutTransaction` (`:684`) | `processBranch` `:1024-1030` |
| checkout continuation | `ContinueCheckoutSync` after `forceAcquireCheckoutLock` `:740`, before `resumeTransaction` `:753` | `processBranch` `:1024-1030` **and** `resumeFromSwitched` `:933-939` |
| legacy external fallback | a seam MUST exist — **no guarded fallthrough into `syncFallback`** | — |

Both external seams are inside `syncWithStackScoped` (`sync_helpers.go:78`) and are reached with a
non-`nil` `guard` **only** from the four guarded entry points; every unguarded invocation runs the
identical statements with `guard == nil` and re-probes nothing.

### 11.2 Per-row JIT inputs
Always: `base.decision_sha` in `base_context`; destination SHA, head SHA, candidate count and full
candidate digest in `execution_context`; `rebase-merge`/`rebase-apply` presence; tracked dirty state;
untracked presence; every other mutable fact the row's action reads.

Holder refresh is issued **exactly when** the row's next command reads a holder — i.e. for a planned
switch (`mutation.will_switch_head: true`: every checkout row's standalone `git checkout` **and every
external pass-2 row**) **and** for any row whose published argv can invoke `--update-refs` (external
**pass 1** is in this class — its unscoped argv is `["rebase","--update-refs",<base>]`,
`sync_helpers.go:123-126`). At most one `BuildBranchHolderInventory` per **canonical common dir**
per seam, replacing only that common dir's entries, never shared across seams.

**Collateral-class rows re-measure all four §25.102 inputs, in this order, then compare:**
1. the holder inventory for the row's **execution** canonical common dir;
2. the complete local branch ref inventory of that repository through **exactly one**
   `internal.BuildBranchRefInventory` (`internal/stack_status.go:427-447`) — one `git for-each-ref`;
3. a **safe reload of the current `stack.yaml`** from which **only** the stack-owned membership
   mapping is rebuilt (a read-only identity probe over `StackEntry.GitBranch()` — **no** `TopoSort`,
   **no** re-selection, **no** change to order/universe/remaining);
4. the row's candidate range/digest.
Any difference in the whole `[{repo, ref, sha, stack_owned}]` tuple array or in `collateral_exposed`
⇒ rank 9 `revalidation-mismatch` **before** any Git mutation. An unreadable `stack.yaml`, an
unavailable ref inventory and an unavailable worktree inventory each ⇒ rank 5.9 `probe-failed`
before Git — **never** an empty set read as "no collateral". `RevalidatePlanGuardEntry` declares
**no** new probe symbol, no second inventory type, no `TopoSort` and no selection: it *calls*
`BuildBranchHolderInventory`, the consumed-verbatim `BuildBranchRefInventory`, and the shipped stack
loader.

### 11.3 Stage-specific and finalization facts
- `StageSwitched` (`:895`/`:933-939`): pinned destination — the persisted `NewBaseSHA` is the
  destination, the persisted `LastBaseSHA` the upstream; **one** `rev-parse --verify <persisted
  NewBaseSHA>^{commit}` existence probe with a missing-object rank 5.7 cell; plus the HEAD-identity
  preflight (matching / mismatched / detached), enforced on the plan and guarded routes only.
- `StageConflict` (`:868-886`): the same live-ancestry question as `rebased` — **one**
  `merge-base --is-ancestor` probe when the short-circuit did not fire, and **never a second** for
  the shipped fall-through into `resumeFromRebased`.
- `StageRebased` (`:899`): the ancestry gate, one probe; zero probes at the other six stages.
- **Finalization postcondition** (`finalizeTransaction` `:1161-1235`): a whole-plan check that only
  the future can decide. It is **disclosed, never projected** — no blocker, no warning, no refusal
  kind, no token, no new key, `runnable: true`, zero extra `merge-base --is-ancestor` at any seam.
  The executing failure is **marker-free** with a non-`PlanGuardRefusalError` return, leaves the
  transaction and lock in place, and `stack.yaml` already carries updated `LastBaseSHA` values.
- **External pass 2** (`sync_helpers.go:167-211`): plain explicit-branch argv
  (`git rebase <base> <gitBranch>`), the cutoff is **never read** (`cutoff-not-used-on-arm`), the
  HEAD switch is **permanent** (including after an abort), and the context ladder is
  `entry.Repo` else process cwd.
- **Restore**: exactly **one** holder refresh immediately before `restoreOriginal` (`:1257`), with a
  `StageCompleted` no-probe twin.

---

## 12. Documentation, skills and roadmap — exact surfaces (all eight exist)

| Surface | Path | Insertion point verified |
|---|---|---|
| README sync flag table + sync-modes narrative | `README.md` | the sync flag table and the sync-modes section |
| Cheatsheet sync block | `docs/cheatsheet.md` | the sync block |
| Claude in-worktree skill | `assets/skills/claude/tesseraworkspaces/SKILL.md` | plan → read → approve → execute; marker parsing |
| Claude orchestrator skill | `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` | pre-sync orchestration; narrow with `--only`/`--from`; broadcast the plan |
| Copilot prompt | `assets/skills/copilot/tws.prompt.md` | same plan/guard contract |
| Changelog | `CHANGELOG.md` | one entry under the existing `## Unreleased` heading (`CHANGELOG.md:3`) |
| Roadmap | `docs/roadmap.md` | remove `Rebase plan guard` from the *P1 stack safety and observability backlog* entirely; add it to the *Shipped foundations* list; replace the `Current target: **rebase plan guard**` sentence at current `:46` with **safe reparent/restack**. It must not remain in any backlog/current-target position. **Prose, not flag reference** |
| Engineering workflow | `docs/engineering-workflow.md` | add rebase plan guard to the equivalent shipped-foundations paragraph/list; replace `Next roadmap feature: **rebase plan guard**` at current `:32` with **safe reparent/restack**; add the destructive-action convention. **Prose, not flag reference** |

There are **exactly three** embedded skills (`assets/skills/claude/tesseraworkspaces`,
`assets/skills/claude/tesseraworkspaces-orchestrator`, `assets/skills/copilot`) — verified by
`ls -R assets/skills`. No text may say or imply two. Skills are embedded through
`assets/skills/embed.go`, so adding a *file* would change the embed set; this feature edits existing
files only.

**§22.33's split gate**: the **six** flag/workflow surfaces (README, cheatsheet, three skills,
CHANGELOG) each carry §22.33-i's five required literals — `--plan`, `--max-replay-per-entry`,
`--max-replay-total`, `--approve-plan`, `plan-guard:` — and no limitless mint; the **two** planning-prose
surfaces (roadmap, engineering workflow) carry the shipped/current-target prose and are **exempt**
from any flag-literal requirement. §22.33a's negative grep is scoped to the prose/asset corpus plus
the exact `tws sync --help` output and must **not** scan any Go tree.

The CHANGELOG entry must name, as separate, individually greppable items: the five flags; that a
plan fetches where the run it describes fetches; **both** D1 cells (D1-a and D1-b, with the
`StackEntry.Name` vs `GitBranch()` rule); the guarded `state_version: 3` recovery state with the two
exact downgrade sentences of §13.6 rule 5; the **three** no-flag recovery cells of §12.8, §12.8a and
§12.8b **each as its own row** (a corpus disclosing only the first two fails §22.33 33-i-a); and the
arm-scoped controlled-path hoist (legacy checkout: cyclic **and** unreadable stacks refused before
the lock with their shipped sentences; new-mode unmoved; unguarded unchanged).

---

## 13. Dependency-ordered implementation sequence

Each numbered checkpoint is one compilable batch; files named in the same checkpoint land
together when they have cross-file type dependencies. No control flag is registered until the
plan builders, state writers, JIT seams and guarded executors all exist. After **every** checkpoint,
run `go test ./internal/cli/ -run 'TestSyncNoFlag_' -count=1`; those 126 artefacts are the
no-flag-freeze tripwire.

**Phase A — pure additions, zero reachable behaviour change**
1. `internal/exec.go`: add `RunTo` and `RunDirTo`; touch no shipped body.
2. `internal/git_capability.go` (**NEW**): `GitVersion`, `ProbeGitVersion`, `GitCapabilities` and
   the locked parser; no caller yet.
3. Add the mutually referential type tranche together: document/request/enums in
   `internal/rebase_plan.go`; guard/fetch/gate/inspection carriers in
   `internal/rebase_plan_guard.go`; snapshot carriers in `internal/rebase_plan_state.go`; and probe
   carriers in `internal/rebase_plan_probe.go`. Types and constants only.
4. Implement `internal/rebase_plan_probe.go` and `internal/rebase_plan_state.go` together. The
   inspectors call the probe file's `Lstat` helpers, so separating these into sequential
   compile checkpoints is forbidden. Add candidate/config/fetch/submodule/local-destination/
   holder/context/push reads and both state inspectors.
5. `internal/rebase_planner.go` (**NEW**): pure decisions, all six push producers,
   `GateBlockers`/`GateControlledTokens`, `SelectPrimaryRefusal`, `RemainingRebaseEntries`.
6. Add `internal/rebase_plan_fingerprint.go` and `internal/rebase_plan_render.go` together.
7. `internal/rebase_plan_build.go` (**NEW**): `BuildRebasePlan`, `RevalidatePlanEntry`.
8. Finish only the dependency-free half of `internal/rebase_plan_guard.go`: guard evaluation,
   refusal and JIT types/functions. Defer checkout inspection/build functions until checkpoints
   9–10 have added their order-taking selection, plan and I14 dependencies.

**Phase B — behaviour-preserving splits and dormant carriers**
9. `internal/sync_selection.go`: `ResolveSyncSelectionFromOrder` + shipped wrapper (§9.2a).
10. `internal/checkout_sync.go`: `buildCheckoutPlanFrom` + wrapper, including the declared D1-a/D1-b
    fix; `firstUnresolvedCheckoutBase`; extract only `:618-625` into
    `fetchCheckoutRepoTo` + the new unguarded `fetchCheckoutRepo` wrapper; header writer + wrapper;
    add the dormant `CheckoutSyncOpts.PlanGuard` field.
11. Add dormant CLI carrier declarations before anything references them:
    `planGuardOptions`/`planGuardRun` in the new `internal/cli/sync_plan_guard.go`, and
    `syncRunStateBirth`/`guardedLegacyCarry`/`syncStateResidue` in `sync_modes.go`. In the same
    checkpoint perform the external pure extractions: fetch writers/wrappers,
    `syncFeatureScopedPlanned`, the two base locators, selection-from-order wrapper,
    `syncCellLiveGuardRefusal`, `abortLegacySyncState`, and the CLI header writer.
12. Thread trailing parameters in one batch:
    `syncWithStack`/`syncWithStackFiltered`/`syncWithStackScoped` gain
    `guard *planGuardRun` with `nil` at every shipped call; `syncRunContext` gains
    `Route`/`Validation`; `syncResult` gains `Refusal`; `setupSyncRunState` gains
    `birth syncRunStateBirth` with its zero value at every shipped call. This checkpoint now
    compiles because both new carrier types were declared in checkpoint 11.

**Phase C — dormant state-v3 and recovery machinery**
13. `internal/sync_run_state.go`: three keys, guarded versions, save-version preservation,
    v2/v3 load, `PayloadNewMode`, error-returning removers/wrappers, reclaimability split and
    `SyncGuardRelease*`.
14. `internal/syncstate.go`: error-returning removal, conditional restore/remove and the complete
    guarded-backup-sentinel surface.
15. `internal/cli/sync_modes.go`: apply `syncRunStateBirth`; add guarded legacy setup/rollback,
    scoped rollback, the sole `upgradeGuardedSyncRunState` declaration and the
    `syncStepHook(SyncStageInitializing, 3)` capture hook.
16. `internal/checkout_sync.go`: transaction v3 fields and route predicates, save/load upper-bound
    support and the sole `upgradeGuardedCheckoutTransaction` declaration.
17. Add the dormant recovery helpers: external `syncTriggersNeedV2`,
    `syncStateRefusesContinueAbort`, cell-1/cell-7 abort helpers and the direct second-state
    dispatcher; checkout `checkoutTriggersNeedV2` and persisted-guard derivation. No command route
    calls them yet.

**Phase D — complete but unreachable plan routes**
18. Finish the checkout half of `internal/rebase_plan_guard.go`: `InspectCheckoutPlan`,
    `PlanCheckoutRebase`, `BuildCheckoutRebasePlan`, `BuildCheckoutContinuationPlan`. This now
    compiles against checkpoints 9–10's order-taking selection/plan/I14 functions and
    checkpoints 13–16's v3 state types.
19. Finish `internal/cli/sync_plan_guard.go`: option resolution, checkout adapter, layout, the
    one-write render/marker helpers, `ExternalPlanInspection*`, `InspectExternalPlan` and
    `runExternalPlan` in §13.7a order. Sentinel projection and plan-only vanished-state
    reclassification now compile against checkpoint 14. Do **not** register flags or change
    command dispatch yet.

**Phase E — controlled executors, still unreachable from Cobra**
20. Add the conditional JIT seams first: external pass 1/pass 2, with
    `syncStepHook(SyncStageRebasing, -1)` in `sync_helpers.go` immediately before the first pass-1
    post-claim probe; checkout `processBranch` and `resumeFromSwitched`; the one
    pre-`restoreOriginal` refresh. A `nil` guard leaves every shipped process and byte untouched.
21. Add external guarded fresh and continuation entry points. The scoped v2 arm's order is
    guard seam → `ReclaimSyncRunGuard` → **the sole call to**
    `upgradeGuardedSyncRunState` → header/prose → executor. Add all three guarded legacy
    continuation arms and their step-9 payload setup.
22. Add checkout guarded fresh/continuation arms in `internal/checkout_sync.go`; both inspectors
    remain above their lock, the continuation seam remains below `forceAcquireCheckoutLock`, and
    its upgrade remains below the seam and above `resumeTransaction`.

**Phase F — atomic command exposure**
23. Wire both CLI files in one compile checkpoint: register all five flags and `Long`; resolve the
    one options value; change the checkout caller and callee signatures together to thread `cmd`;
    add plan dispatch, containment copy, persisted-guard dispatch, guarded fresh/continue dispatch,
    `plan-unavailable`, marker wrapping, the `syncCellLiveGuardRefusal` call and the cell-4
    interception. Its sentinel-`absent` arm performs exactly one second classification and calls
    the direct ordinary-state dispatcher without re-entering the interceptor.

**Phase G — docs and tests**
24. Update the eight documentation surfaces of §12.
25. Add tests in §15's ownership order, including the new help snapshot and documentation corpus
    gate. Run the focused commands after each test-file cluster and the full gate at the end.

---

## 14. Closed file ledger

### NEW (10 production files)
`internal/rebase_plan.go`, `internal/rebase_planner.go`, `internal/rebase_plan_guard.go`,
`internal/rebase_plan_state.go`, `internal/rebase_plan_build.go`, `internal/rebase_plan_probe.go`,
`internal/rebase_plan_fingerprint.go`, `internal/rebase_plan_render.go`,
`internal/git_capability.go`, `internal/cli/sync_plan_guard.go`.

### MODIFIED (9 production + 8 documentation)
Production: `internal/cli/sync.go`, `internal/cli/sync_helpers.go`, `internal/cli/sync_modes.go`,
`internal/cli/checkout_sync.go`, `internal/checkout_sync.go`, `internal/sync_selection.go`,
`internal/sync_run_state.go`, `internal/syncstate.go`, `internal/exec.go`.
Documentation: `README.md`, `docs/cheatsheet.md`, `docs/roadmap.md`,
`docs/engineering-workflow.md`, `CHANGELOG.md`,
`assets/skills/claude/tesseraworkspaces/SKILL.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
`assets/skills/copilot/tws.prompt.md`.

### EXPLICITLY UNTOUCHED
`internal/config.go`, `internal/stack.go`, `internal/stack_ancestry.go`, `internal/agent_status.go`,
`internal/stack_status.go`, `internal/checkout_health.go`, `internal/cli/importcmd.go`,
`internal/cli/push.go`, `internal/cli/root.go`, `internal/cli/status.go`,
`internal/cli/stack_status.go`, `internal/cli/doctor.go`, `assets/skills/embed.go`,
`cmd/tws/**`, and every file under `internal/cli/testdata/sync_noflag/` (126 files).

### Ownership parents for the modified surfaces
`internal/sync_selection.go`, `internal/sync_run_state.go`, `internal/cli/sync_modes.go`,
`internal/cli/checkout_sync.go` and the sync half of `internal/checkout_sync.go` are owned by
**`sync-modes`**. `internal/stack_status.go` (`BuildBranchRefInventory`) is owned by
**`stack-status`**. `LastBaseSHA` and the `--onto` strategy are owned by **`amend-aware-rebase`**.
`internal/agent_status.go` (`BuildWorktreeInventory`) is owned by **`agent-work-status-dashboard`**,
transitively in `sync-modes`' hard closure. The three skill files are owned by
**`skill-distribution`** (two) and **`tiered-skill-system`** (the orchestrator), which is exactly why
both are registered soft.

---

## 15. Test-file ledger

### 15.1 Existing fixtures and helpers to reuse (all verified at `81659e1`)
| Symbol | Site |
|---|---|
| `scopedFixture` / `newScopedFixture` / `runSync` / `(*scopedFixture).stateFilesGone` | `internal/cli/sync_scoped_test.go:18-24`, `:26-44`, `:59-65`, `:83-94` |
| `(*scopedFixture).wt`/`.sha`/`.advanceRoot`/`.detachGuard`/`.makeConflict` | `:46`, `:48`, `:53`, `:71`, `:304` |
| `setupGitRepo(t, defaultBranch)` | `internal/cli/new_integration_test.go:135` |
| `createLinearStack(t, repo, feature)` | `internal/cli/sync_continue_integration_test.go:76` |
| `saveTestStack(t, featurePath, entries)` | `internal/cli/checkout_sync_test.go:75` |
| `checkoutModeFixture` / `newModeOpts` / `captureRun` | `internal/cli/checkout_sync_modes_test.go:17`, `:36`, `:51` |
| `setupCheckoutSyncRepo`, `setupFeaturePath`, `createStackBranch`, `gitRunCS`, `gitSHA`, `clearStepHook` | `internal/cli/checkout_sync_test.go` / `checkout_sync_modes_test.go` |
| `syncWrapperScript` (POSIX `sh`, argv sidecar) | `internal/cli/sync_golden_test.go:59-137` |
| `newSyncGitWrapper`, `around`, `records` | `internal/cli/sync_golden_test.go:195-333` |
| `syncExecute` | `internal/cli/sync_golden_test.go:1110-1128` — **not** suitable for marker tests (`SilenceErrors`) |
| frozen no-flag suite | `TestSyncNoFlag_*` at `internal/cli/sync_golden_test.go:1450`, `:1463`, `:1472`, `:1489`, `:1501`, `:1510`, `:1524`, `:1546`, `:1555`, `:1564`, `:1580` over `internal/cli/testdata/sync_noflag/**` |
| downgrade harness | `internal/cli/sync_downgrade_test.go`, `downgradeTag` at `:22` (currently `"v1.2.14"`) |
| crash hooks | `internal.StepHook` `internal/checkout_sync.go:316`; `internal.SyncStepHook` `internal/sync_run_state.go:84`; shipped `StepHook(StageRebased, …)` site `internal/checkout_sync.go:1148-1149` |
| cell fixtures | `internal/cli/sync_cells_test.go`, `internal/cli/sync_state_matrix_test.go`, `internal/cli/sync_teardown_test.go`, `internal/cli/sync_push_resume_test.go`, `internal/cli/sync_validation_test.go` |
| planner units | `internal/sync_selection_test.go`, `internal/sync_run_state_test.go`, `internal/stack_test.go`, `internal/stack_status_test.go`, `internal/agent_status_test.go` |

### 15.2 New test-only seams (all REQUIRED, §23.1)
1. A production entry-point harness driving `cli.Execute()` (`internal/cli/root.go:16-58`; error
   block `:53-56`) — the **only** valid harness for marker assertions.
2. A barrier between the state snapshot and the guard seam.
2a. A barrier between `classifySyncState` (`internal/cli/sync.go:77`) and the cell-4 interception,
   so a second process can really create/remove/replace `.sync-state.yaml` in that window. It
   injects **no** verdict.
3. A barrier between the reclaimability check and the reclaim compare-and-swap.
4. An injected reader/remover/**restorer**/**writer** forcing non-`ErrNotExist` I/O failures at:
   both read paths, the three `os.Remove` sites of §12.2c rule 2, the `.sync-state.yaml` capture
   read, the JIT seam's `stack.yaml` reload (which the seam also **counts**),
   `SaveGuardedLegacySentinel`, `RestoreSyncStateBytes`, `RemoveSyncStateIfUnchanged`; plus a
   document/prose `io.Writer` failing in **two** shapes — sole `Write` → `(0, error)` and sole
   `Write` → short write `(n < len(p), error)` — and **counting** its `Write` calls.
5. A fingerprint debug accessor returning the annotated canonical pre-image.
6. A Git `PATH` wrapper able to fail exactly one probe class and record cwd/argv/env/process count,
   plus a `git --version` stub for §16.
7. An **owned sleeping PID** helper with a **deterministic dead-PID** half (spawn → record → wait →
   re-check not-alive) and, on top of it, one **guard-detaching** helper that rewrites an existing
   `.sync-run.lock` so only the recorded PID becomes that known-dead value (every other byte
   asserted unchanged). It is the **only** sanctioned way to produce a dead-foreign-owner residue.
   The shipped `(*scopedFixture).detachGuard` (`sync_scoped_test.go:71-81`) hard-codes `pid: 999999`
   and rewrites the whole document — it is **not** that helper and must not be reused for §22.28a
   rule L.
8. Hook usage: the **two new** hook sites are owned separately:
   `syncStepHook(SyncStageRebasing, -1)` in `internal/cli/sync_helpers.go` (immediately before
   the first post-claim revalidation probe, checkpoint 20) and
   `syncStepHook(SyncStageInitializing, 3)` in `internal/cli/sync_modes.go`
   (immediately after the guarded legacy capture, above the claim, checkpoint 15).
   **No new checkout hook site**:
   §22.13d (b) and §22.13n both reuse the shipped `StepHook(StageRebased, …)` at
   `internal/checkout_sync.go:1148-1149` — returning for the ancestry gate, **non-returning** for
   the finalization postcondition. Each boundary is driven in **two** modes: a hook that **returns
   an error** asserts rollback; a hook that **terminates the process** (`os.Exit` in an
   out-of-process child, or `SIGKILL`) asserts the crash windows. A crash-window assertion driven by
   a returning hook fails its criterion.
9. The downgrade harness pattern retargeted to `v1.2.15` (a real tag). When the tagged binary cannot
   be built, the frozen replay transcription is used and the substitution recorded.
10. A **test-only coverage-counter harness** for §22.33g (iii)–(v): an out-of-process child runs
    exactly one named sync route under the repository's existing Go coverage instrumentation
    (`-coverpkg=./internal/...`), writes its profile to a temporary path, and the parent reads the
    execution count of `internal.TopoSort`'s entry block. One child fixture is run per controlled
    and unguarded row of §10.1, so the count is per invocation (1/0 controlled, 2/1/0 unguarded),
    not a static call-site estimate. A child-only environment discriminator prevents recursion.
    This harness adds **no production hook**, does not edit a source copy, and preserves
    `internal/stack.go`/`TopoSort` byte-for-byte; source-AST assertions separately enforce the
    wrapper/body direction in §22.33g (v).

### 15.3 Test-file placement
| File | Kind | Required test prefix | Covers (§22/§23 group) |
|---|---|---|---|
| `internal/rebase_plan_test.go` | **NEW** | `TestRebasePlanSchema` | schema totality, 25-key order/JSON tags, never-null arrays, closed token domains, `ControlledPathBlocker` distinctness, total `restore` shape, `RebasePlanLayout.WorktreePath`, P-4's document/runtime `PlanFetch*` file owners, and source/compile assertions that no shared signature names a `cli` type (§22.33b, 33c, 33d, 33e, 33g; layout boundary) |
| `internal/rebase_planner_test.go` | **NEW** | `TestRebasePlanner` | strategy/ordering/destination/replay/remaining matrices including switched/HEAD facts, refusal collapse, gate conversion, pure push/lease producers and effective-backend projection (§22.25, 25a–25c, 27c, 33f, 33h) |
| `internal/rebase_plan_state_test.go` | **NEW** | `TestRebasePlanState` | five-artefact matrix through both inspectors, verdict/error identity, one read per path, never-opened symlinks (§22.13f, 13i) |
| `internal/rebase_plan_probe_test.go` | **NEW** | `TestRebasePlanProbe` | candidate/config/fetch-effect/submodule/local-destination/holder/context probe units and per-common-dir inventory counts (§22.13g, 13k, 33i) |
| `internal/rebase_plan_fingerprint_test.go` | **NEW** | `TestPlanFingerprint` | annotated TLV pre-image, separators, scalar distinctions, retired id, fingerprint non-members |
| `internal/rebase_plan_render_test.go` | **NEW** | `TestRebasePlanRender` | complete human grammar/tail/block rendering and 12-char SHA formatting with zero `rev-parse --short` |
| `internal/git_capability_test.go` | **NEW** | `TestGitCapability` | locked parser, `Probed`/`OK`, six independent capability calculations and host-real-Git unit assertions; **not** the production-route/argv biconditional |
| `internal/checkout_sync_plan_test.go` | **NEW** | `TestCheckoutSyncPlan` | internal checkout inspection/build order, copied containment identity, full projected lock ladder, freshness attribution, `PlanWriters`, shipped `BuildCheckoutPlan` signature, and D1-a/D1-b/decoupled cross-repo adapter cells (§22.13b, 13d, 13e, 13h, 13j, 13l, 25b, 25c, 26, 26a, 26b) |
| `internal/cli/sync_plan_test.go` | **NEW** | `TestSyncPlan` | external orchestration's exact 1→10 order, rows-less runnability, I14, one inspection/fetch, safe snapshot, continuation mismatch/guarded-entry dispatch, suppression and outcome→document identity (§22.12c, 13a, 13c, 13f, 14a, 14b) |
| `internal/cli/sync_plan_guard_test.go` | **NEW** | `TestSyncPlanGuard` | limits/approval/JIT drift, evaluation/token domains, flag identity, plan→approval round trip, one production marker through `Execute()` per mode (§22.17–23, 13m) |
| `internal/cli/sync_guarded_state_test.go` | **NEW** | `TestSyncGuardedState` | birth/version cells, byte-identical zero birth, guarded triples, v2/v3 round trips, all four armed-continuation upgrade subjects and downgrade (§22.24a–24j) |
| `internal/cli/sync_recovery_test.go` | **NEW** | `TestSyncRecovery` | complete cell-1/cell-7/cell-4 recovery tables, live/self twins, conditional writes and the one-reclassification vanished-sentinel race (§22.28a–28e) |
| `internal/cli/sync_plan_integration_test.go` | **NEW** | `TestSyncPlanIntegration` | **production `Execute()` in both modes**, plan-only and executed customer topologies; rows-less/runnability/I14/external plan-route cells; external pass 2; multi-repo route split and D1-a/D1-b; switched/HEAD execution; contexts; dirt/autostash; holder/collateral JIT; fetch/submodule/config/effective backend; capability process ordering, zero-probe controls and the corpus-wide `entries[].argv`/2.38 biconditional; coverage-profile `TopoSort` counters for every controlled/unguarded route plus source-AST wrapper-direction assertions (§22.1–16, 25b, 25c, 26, 26a, 26b, 27b, 27c, 32a, 32b, 33g, 33i) |
| `internal/cli/sync_plan_docs_test.go` | **NEW** | `TestSyncPlanDocs` | production `tws sync --help` snapshot; six flag/workflow surfaces; two planning-prose surfaces; semantic CHANGELOG cell-1/cell-7/cell-4 assertions; scoped negative grep that cannot traverse a Go tree (§22.33, 33-i, 33-i-a, 33-ii, 33a) |
| `internal/cli/sync_downgrade_test.go` | **MODIFIED** | `TestSyncDowngrade` | `downgradeTag` → `"v1.2.15"` (`:22`) plus comments (`:18`, `:202`, `:468`) |
| `internal/cli/sync_scoped_test.go` | **MODIFIED** | `TestSyncScoped` | deterministic dead-PID + byte-preserving guard-detaching helpers beside the shipped helper |
| `internal/cli/checkout_sync_modes_test.go` | **MODIFIED** | `TestCheckoutSyncModes` | guarded checkout dispatch and inspector/snapshot zero-call unguarded controls |
| `internal/exec_clean_test.go` | **MODIFIED** | `TestRunDirTo` | writer-taking helper observationally identical to `RunDir` |

New snapshot fixture: `internal/cli/testdata/rebase_plan/sync_help.txt`. It is asserted through
production `Execute()` by `sync_plan_docs_test.go`; it is **not** placed under
`internal/cli/testdata/sync_noflag/`, whose 126 files remain immutable.
`internal/cli/sync_golden_test.go` is reused but not modified.

### 15.4 §22/§23 group coverage map (every matrix row has an executable owner)

| §23.2 group | Owning test file(s) |
|---|---|
| Frozen path and matched guarded/unguarded argv | existing `sync_golden_test.go` + `sync_plan_integration_test.go` |
| Customer topologies; external pass 2; contexts; dirty/untracked/autostash; holders; collateral; fetch; submodules; config; capability route semantics | `sync_plan_integration_test.go` through production `Execute()`; corresponding pure probes additionally in `rebase_plan_probe_test.go`/`git_capability_test.go` |
| Human rendering | `rebase_plan_render_test.go`; production human/JSON fingerprint twins in `sync_plan_integration_test.go` |
| Candidates and determinacy | `rebase_plan_probe_test.go` + `rebase_planner_test.go`; real interrupted-rebase/real-execution assertions in `sync_plan_integration_test.go` |
| Runnability, I14 preflight and external plan/snapshot/continuation routes | `sync_plan_test.go` + production `Execute()` twins in `sync_plan_integration_test.go` |
| Guard/approval; marker; guard JIT; control-flag identity | `sync_plan_guard_test.go` + production-route pairs in `sync_plan_integration_test.go` |
| State, recovery, concurrency, guarded legacy envelope/sentinel, guarded continuations, armed upgrades, downgrade | `sync_guarded_state_test.go`, `sync_recovery_test.go`, modified `sync_downgrade_test.go` |
| Checkout plan gates/order, finalization, restore and push integration | `checkout_sync_plan_test.go` + production `Execute()` assertions in `sync_plan_integration_test.go` |
| Multi-repository route split, D1-a/D1-b and switched/HEAD continuation cells | `checkout_sync_plan_test.go` + both-mode execution in `sync_plan_integration_test.go` |
| Layout/package boundary and shipped checkout adapter signature | `rebase_plan_test.go` + `checkout_sync_plan_test.go` |
| Planner declarations, wrapper direction and one-sort ledger (§22.33g) | `rebase_plan_test.go` for declarations; `checkout_sync_plan_test.go` for checkout adapter structure; `sync_plan_integration_test.go` for source direction plus per-invocation controlled/unguarded coverage counters across all seven §10.1 rows |
| Effective-backend merge recreation/config collateral (§22.27c) | `rebase_planner_test.go` + real Git in `sync_plan_integration_test.go` |
| Push/lease pure projection and real force-with-lease controls | `rebase_planner_test.go` + `sync_plan_integration_test.go` |
| Fingerprint/TLV and schema/source/restore totality | `rebase_plan_fingerprint_test.go` + `rebase_plan_test.go` |
| Stream plumbing | `exec_clean_test.go`, `rebase_plan_render_test.go`, `sync_plan_test.go` |
| Help and all eight documentation surfaces | `sync_plan_docs_test.go`; no existing documentation gate is assumed |

This ownership deliberately keeps route claims out of package-`internal` tests: every criterion
that says “through production `Execute()`”, every external executor claim, and §22.32a/32b's
sidecar ordering/biconditional lives in `internal/cli/sync_plan_integration_test.go`.

### 15.5 Commands
```sh
# focused, while implementing
go test ./internal/ -run 'Test(RebasePlanSchema|RebasePlanner|RebasePlanState|RebasePlanProbe|PlanFingerprint|RebasePlanRender|GitCapability|CheckoutSyncPlan|RunDirTo)' -count=1
go test ./internal/cli/ -run 'Test(SyncPlan|SyncPlanGuard|SyncGuardedState|SyncRecovery|SyncPlanIntegration|SyncPlanDocs|SyncDowngrade|SyncScoped|CheckoutSyncModes)' -count=1
go test ./internal/cli/ -run 'TestSyncNoFlag_' -count=1

# full gate, before landing
gofmt -l internal cmd
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

### 15.6 Portability rules to honour in every new test
Invalid-UTF-8 **path** cells skipped with a recorded reason on macOS APFS/HFS+ (branch/ref
invalid-byte cells still run); permission cells use mode `0000` with a `t.Skip` reason when root/ACLs
bypass mode bits, then fall back to the injected reader; Git holder/refusal wording matched by
concept or substring, never byte-matched; command-scope config fixtures use the inherited
`GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_*`/`GIT_CONFIG_VALUE_*`/`GIT_CONFIG_PARAMETERS` environment,
never a tws-added `-c` (the shipped fixtures already set `GIT_CONFIG_COUNT=0` and
`GIT_CONFIG_NOSYSTEM=1` — `sync_scoped_test.go:28-29`, `checkout_sync_modes_test.go:19-20`);
`TopoSort` gets a 20 000-iteration randomized ordering test permitting only inter-component
interleaving to vary.

---

## 16. Process-budget probe ledger — what caches, what JIT-refreshes

### 16.1 Once per controlled invocation (cached; never re-issued)
| Fact | Producer | Owner |
|---|---|---|
| `TopoSort` order | `InspectExternalPlan` / `InspectCheckoutPlan` / `BuildCheckoutPlan` wrapper | `req.Order`, `insp.Order` |
| selection | `ResolveSyncSelectionFromOrder` / `scopedSelectionFromPayloadOrder` | `insp.Selection` |
| I14 preflight | `firstUnresolvedSelectedBase` / `firstUnresolvedCheckoutBase` | `insp.BasePreflight` — evaluated **at most once** (§10.7 rule 1a) |
| `git --version` | `ProbeGitVersion` | `insp.Version`/`.Capabilities`; **zero** on a rows-less no-subject document and on every unguarded route |
| ordered config inventory | one `git config --list --show-scope -z` **per context** + **≤ 2** typed reads | per-context, run-invariant, reusable at seams |
| context identity | `PlanContextIdentities` | the one table every `context_id` comes from |
| push **mapping** facts | `MeasurePushRemoteFacts` | once per distinct push context of an **applicable** `PlanPushFacts`; **zero** when `intent.push` is false or the document is rows-less |
| the five-artefact snapshot | `InspectExternalPlanState` / `InspectCheckoutPlanState` | one `Lstat` + at most one decode per regular file; never re-probed by `BuildRebasePlan` (§12.5a rule 6) |
| fetch context enumeration + effect | `PlanFetchPlan` / `PlanFetchContext.Effect` | measured **before** the fetch, **copied** into `PlanFetchRepoResult`, never re-probed after |
| the policy fetch | `fetchScopedReposTo` / `fetchStackReposTo` / `fetchCheckoutRepoTo` | **exactly once** per invocation, above the claim/lock on a guarded run |
| push **baseline** refs | `RefreshPushTrackingRefs` | the ONE post-fetch remote-tracking read, skipped entirely for a non-applicable `PlanPushFacts` |
| the guarded run's validation command | one `internal.LoadConfig().TestCommand` into `planGuardRun.Validation` | consumed by the digest, by the setup writers and by `runValidation` **as a value** — never a second `LoadConfig()` under the claim |

### 16.2 Re-measured at every JIT seam (never cached across seams)
`base.decision_sha`; destination SHA; head SHA; candidate count + full candidate digest;
`rebase-merge`/`rebase-apply` presence; tracked dirty state; untracked presence; the holder inventory
(≤ 1 `worktree list --porcelain` per **canonical common dir per seam**, only for switching rows and
`--update-refs`-carrying rows); on a collateral-class row additionally one `BuildBranchRefInventory`
(`for-each-ref`) and one safe `stack.yaml` identity reload (**zero** Git processes).

### 16.3 Ceilings
Per-row external: **13** processes (**11** without the default-branch read); per-row checkout:
**8** — checkout never publishes `--update-refs`, so it has no collateral-class row. Per-context
config budget: exactly one ordered `--list --show-scope -z` plus **≤ 2** typed reads, with **no**
typed read of `rebase.rebaseMerges` in the argv sidecar. Holder counts to assert: **1** on checkout,
**2** on a two-repository external fixture, **1** on a two-linked-worktree fixture, **1** on a
rows-less `restoring` continuation whose restore really runs, **0** on a rows-less document that asks
no holder question (a `completed`-stage resume included). Restore: exactly **one** pre-`restoreOriginal`
refresh, with a `StageCompleted` no-probe twin.

### 16.4 Explicit zero-budget cells
`--plan --no-fetch` performs **no** network I/O and leaves every ref byte-identical.
`--plan --continue` never fetches in either mode. A checkout `--plan`/`--plan --no-fetch` performs
zero `git fetch`, and so does **each refusing pre-fetch gate**. Plan routes claim no guard, take no
lock, write no state and print nothing to a process-global stream from `internal`.

---

## 17. Risk and precision notes (implementation traps, no open design questions)

**R1 — `SaveSyncRunState:123` force-writes `state_version: 2`.** The single highest-risk shipped
line. Fix it in the same commit that introduces `SyncRunStateGuardedVersion`, or the v3 envelope
self-downgrades on the first progress write and every §22.24 assertion fails in a confusing place.

**R2 — `run.scoped()`/`selects()`/`skipsAnchor()` are nil-receiver methods.** `syncRunContext`'s
methods (`sync_helpers.go:27-49`) are called on a possibly-`nil` receiver (`syncWithStackScoped`
calls `run.scoped()` at `:82` before any nil check). Adding `Route` must preserve that: a
`Route == legacy` context must answer **exactly** as `run == nil` answers today, and the methods must
stay nil-safe. Assert the three answers directly (§22.14c).

**R3 — the guarded legacy arm must use `syncWithStackScoped`, never `syncWithStackFiltered`.**
`syncWithStackFiltered` passes `run == nil`, which routes failures into `saveIncompleteSync`
(`sync_helpers.go:264`) and writes a **real** `.sync-state.yaml` over the backup sentinel. That is the
"cell 8 never occurs" invariant of §22.24d.

**R4 — D1 is a *declared* behaviour change reachable with no flag.** Step 11's `BuildCheckoutPlan`
rewrite changes two real cells (D1-a, D1-b). The goldens must be **re-inspected**, not
re-baselined; if a `sync_noflag` golden moves, the change is wrong unless it is exactly a D1 cell.

**R5 — `opts.FeaturePath`/`opts.RepoDir` hoist changes nothing observable but is load-bearing.**
`internal/cli/checkout_sync.go:33-34` must move above `:22`; both assignments are pure and observable
to nothing before their current position, and the containment refusal keeps its predicate, sentence
and position in the *executing* order. Evaluate the predicate **once** into a `PlanGateResult`.

**R6 — the checkout legacy arm's subject hoist is arm-specific.** On the **legacy** checkout arm the
controlled routes now read and sort `stack.yaml` **above** the lock, so a cyclic **and** an unreadable
stack refuse earlier with their shipped sentences (`build plan: cycle detected in stack.yaml`,
`load stack: %w`). The **new-mode** arm already sorted there (`checkout_sync.go:597` →
`sync_selection.go:166`) and must be proved unmoved. **No unguarded run changes.**

**R7 — two `printSyncModeHeader` functions exist** (`internal/cli/sync_modes.go:502` and
`internal/checkout_sync.go:519`), byte-identical by design. Both become one-line wrappers over their
own `printSyncModeHeaderTo`; do not "de-duplicate" them across the package boundary.

**R8 — `syncExecute` is not a marker harness.** It sets `SilenceErrors` (`sync_golden_test.go:1120`)
and prints the error itself (`:1124`), so the anchored `^plan-guard: ` line would be produced by the
harness, not by production. Marker assertions must go through `cli.Execute()`
(`internal/cli/root.go:53-56`).

**R9 — `detachGuard` is not the §23.1 seam-7 helper.** `sync_scoped_test.go:71-81` rewrites the whole
guard document with `pid: 999999`. Seam 7 requires a **known-dead** PID and every other byte
asserted unchanged. Add the new helper; do not repurpose the old one.

**R10 — `PlanWriters` must have exactly one field.** A second, never-written document writer is the
precise mistake §3.6 rule 5 exists to prevent; §22 asserts the field count by reflection.

**R11 — the cell-4 interception is one call site for three verbs.** Placing it above
`syncCellLiveGuardRefusal` would let a stale sentinel outrank a live foreign guard; placing it below
`syncCellRefusal` would make it unreachable. It sits strictly between them, and no second site may
consult `InspectGuardedLegacySentinel`.

**R12 — `syncTriggersNeedV2` has no cell-4 arm.** Its shipped position is *below* `syncCellRefusal`,
so a cell-4 document is already decided above it; the interception raises the shipped I20 sentence
itself, byte-identically (§25.82).

**R13 — the JIT `stack.yaml` reload must not sort or re-select.** It rebuilds **only** the
short-branch → `stack_owned` mapping over `StackEntry.GitBranch()`. Any `TopoSort` there breaks the
one-sort counter (§9.1a rule 3) and the §22.33g assertion.

**R14 — an empty probe result is never "no collateral".** Unreadable `stack.yaml`, unavailable ref
inventory and unavailable worktree inventory are each rank 5.9 `probe-failed` **before** Git.

**R15 — `finalizeTransaction` is frozen.** It is tempting to add the JIT seam or a guard verdict to
its whole-plan `gitIsAncestor` loop. §13.5 rule F and §19.3 forbid it; the postcondition is
disclosed in docs, never projected in the document.

**R16 — pass-1 argv really carries `--update-refs`.** `sync_helpers.go:123` builds
`["rebase","--update-refs",base]` on the unscoped path, so external pass 1 **is** collateral-class
and **is** gated by `CapRebaseUpdateRefs` @2.38 when its row is published. Scoped runs drop the
option (`:130`) and are not gated.

**R17 — `NewSyncRunState` stays byte-identical.** The birth values are applied by
`setupSyncRunState`/`setupGuardedLegacyRunState` **after** construction and **before** the first
save. Do not "fix" `NewSyncRunState` to take a version.

**R18 — the guarded external ordering has exactly one observable divergence.** Header above fetch,
fetch above claim, stage sequence `initializing → rebasing` with `fetching` never written. Anything
else diverging from the unguarded control is a bug (§22.28c).

**R19 — `internal` must keep importing no Cobra.** `planGuardRefusal`/`renderPlanDocument` take
`*cobra.Command`; they live in `cli`. `PlanGuardRefusalError` and `writePlanGuardMarker`'s payload
cross as plain values.

**R20 — three skills, not two.** Any doc sentence implying two embedded skills fails §20 and §22.33.

---

## 18. Dependency verdict

**The three hard and two soft parents recorded in `status.json` are necessary and sufficient. No
change is recommended.**

| Parent | Kind | Exploration evidence |
|---|---|---|
| `sync-modes` | hard | owns every file this feature edits on the sync path: `internal/sync_selection.go`, `internal/sync_run_state.go`, `internal/cli/sync_modes.go`, `internal/cli/checkout_sync.go`, the sync half of `internal/checkout_sync.go`, the v2 state machine and the 126-file golden harness |
| `stack-status` | hard | `BuildBranchRefInventory` (`internal/stack_status.go:427-447`) is consumed **verbatim** as §25.102 input 2; the versioned/null-honest projection style is the document's model; transitively owns `StackEdge` |
| `amend-aware-rebase` | hard | `LastBaseSHA` (`CheckoutPlanEntry.LastBaseSHA` `internal/checkout_sync.go:54`, `StackEntry.LastBaseSHA`) and the `--onto <base> <last_base_sha>` strategy at `sync_helpers.go:125`/`:132` are exactly what the plan describes |
| `skill-distribution` | soft | this feature edits `assets/skills/claude/tesseraworkspaces/SKILL.md` and `assets/skills/copilot/tws.prompt.md`, both created there |
| `tiered-skill-system` | soft | this feature edits `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`, created there |

**Deliberately not added, confirmed by exploration:**
- `fix-missing-completions` — no completion function is registered; two integers and a 64-hex digest
  have no completable domain, and `internal/cli/sync.go:144-145`'s two
  `RegisterFlagCompletionFunc` calls are untouched.
- `stack-ancestry-doctor` / `agent-work-status-dashboard` — reused **unchanged**. `ancestrySanitize`
  and friends are unexported members of the same package `internal` (no export, no edit);
  `BuildWorktreeInventory` (`internal/agent_status.go:527`) is already exported and
  `BuildBranchHolderInventory` wraps it **inside** package `internal`, so no new edge is created.

Run `tpatch feature deps --validate-all` after this exploration and again before implementation.

---

## 19. Spec-to-code precision items to declare (do **not** silently amend the spec)

Nothing below blocks implementation; each is a naming/baseline clarification the implementer needs.
All source anchors in §1–§2 were re-derived at `81659e1`. Symbol names and statement boundaries are
authoritative; line numbers are navigation aids. The extraction/mutation boundaries that must be
exact are `sync.go:543-546`, `sync_helpers.go:117-135`/`:196-201`,
`checkout_sync.go:618-625`/`:933-939`/`:1024-1030`, `sync_run_state.go:122-130`,
`cli/checkout_sync.go:22-34`, `stack_status.go:427-447` and `root.go:53-56`. The measured ladders in
§2 record the corrected statement lines (including checkout stage rows `:895/:899/:903/:907/:911`
and abort predicate `:823`) rather than repeating broader, drifted spec ranges. The residue is:

**P-1 (naming, no defect).** §19.2 lists many symbols under "Changed files" that do not exist today
and are **new declarations inside existing files**: `syncTriggersNeedV2`,
`syncStateRefusesContinueAbort`, `checkoutTriggersNeedV2`, `syncCellLiveGuardRefusal`,
`abortLegacySyncState`, `fetchQuietTo`, `fetchScopedReposTo`, `fetchStackReposTo`,
`syncFeatureScopedPlanned`, `firstUnresolvedSelectedBase`, `firstUnresolvedCheckoutBase`,
`scopedSelectionFromPayloadOrder`, `printSyncModeHeaderTo` (×2), `fetchCheckoutRepoTo`,
`fetchCheckoutRepo`, `buildCheckoutPlanFrom`, `ResolveSyncSelectionFromOrder`, `runGuardedScopedSync`,
`runGuardedLegacySync`, `handleGuardedScopedSyncContinue`, `handleGuardedLegacySyncContinue`,
`setupGuardedLegacyRunState`, `rollbackGuardedLegacyRunState`, `rollbackGuardedRunState`,
`upgradeGuardedSyncRunState`, `upgradeGuardedCheckoutTransaction`, `txNewMode`,
`TransactionNewMode`, `TransactionGuarded`, `checkoutRecoveryIsNewMode`, `checkoutRecoveryIsGuarded`,
`PayloadNewMode`, `checkSyncRunGuardReclaimable`, the three `Remove*` functions, the whole
`SyncGuardRelease*` and `GuardedLegacySentinel*` families, `RunTo`, `RunDirTo`. §5 above marks each
with `✚`. **No spec change needed** — this is a reading aid.

**P-2 (baseline).** `downgradeTag` is `"v1.2.14"` at `internal/cli/sync_downgrade_test.go:22`; §23.1
seam 9 retargets it to `v1.2.15`. The tag exists (`git tag` shows `v1.2.15`) and the working baseline
is `v1.2.15-3-g81659e1`, so the retarget is correct and the comments at `:18`, `:202`, `:468` must
move with it. **No spec change needed.**

**P-3 (resolved precision correction).** §3.6 rule 3 and the §23.2 stream-plumbing row speak of
`fetchCheckoutRepo` as a **shipped symbol** becoming a wrapper ("a wrapper test per row … asserting
`printSyncModeHeader` (both packages), `fetchQuiet` and `fetchCheckoutRepo` emit the **pre-change**
bytes"). In the tree there is **no `fetchCheckoutRepo` function**: the checkout fetch is an inline
body inside `RunCheckoutSync` at `internal/checkout_sync.go:618-625`. The correction is purely about
the *baseline of comparison*: `fetchCheckoutRepo` is a **NEW** wrapper, and its "pre-change bytes"
control is that inline block (captured before the extraction, e.g. through the existing
`checkoutModeFixture` + `captureRun` harness), not a pre-existing function. The `printSyncModeHeader`
and `fetchQuiet` rows are unaffected — both are real shipped symbols
(`sync_modes.go:502`, `checkout_sync.go:519`, `sync.go:604`). **Implementation ruling:** treat
`:618-625` as the pre-change byte baseline and declare the wrapper new; do not edit the approved
spec or infer a missing shipped function.

**P-4 (declare, low impact).** §19.1's `internal/rebase_plan_guard.go` row places `PlanFetchOutcome`,
`PlanFetchRepoResult`, `PlanFetchContext` and `PlanFetchPlan` in that file, while §19.1's
`internal/rebase_plan.go` row places `PlanFetch`, `PlanFetchRepo`, `PlanFetchEffect` and
`PlanFetchCandidate` there. The split is deliberate — *document* types in `rebase_plan.go`,
*runtime carrier* types in `rebase_plan_guard.go` — but the near-identical names invite a wrong
file. Implementers should treat §19.1's two lists as authoritative and add a compile-time file-owner
assertion in `internal/rebase_plan_test.go`. **No spec change needed.**

**P-5 (declare, informational).** §23.1 cites `internal/cli/sync_golden_test.go:1450-1600` for "the
frozen no-flag golden suite". The `TestSyncNoFlag_*` functions really span `:1450`–`:1594`
(`TestSyncNoFlag_CheckoutAbort` at `:1580`), inside a 1 850-line file, and one further frozen-path
test lives in `internal/cli/sync_validation_test.go:250`. The by-name citation rule of §23.1 already
covers this; noted so the implementer does not conclude a test is missing. **No spec change needed.**

**P-6 (declare, low impact).** §19.1 is headed “New files (package `internal`)”, but its final row is
`internal/cli/sync_plan_guard.go` with package `cli`. Section 4's explicit `Package` column resolves
the heading error: nine new files belong to package `internal`, and this one belongs to
`internal/cli`. **No spec change needed.**

---

## 20. Implementer handoff checklist and stop condition

### 20.1 Before writing any production code
- [ ] Re-read §19.1/§19.2/§19.3 of the spec; they are the authoritative file/symbol tables and this
      document is their verified index.
- [ ] Capture the pre-change baseline: `go test ./internal/cli/ -run 'TestSyncNoFlag_' -count=1`
      green, and the argv sidecar transcript of a no-flag run in **both** modes.
- [ ] Capture the pre-change byte transcript of the inline checkout fetch block
      (`internal/checkout_sync.go:618-625`) — the resolved control for P-3.
- [ ] `tpatch feature deps --validate-all`.

### 20.2 Invariants to re-assert after every step
- [ ] the 126 `sync_noflag` goldens are byte-identical;
- [ ] `go build ./...` and `go vet ./...` clean;
- [ ] `internal` imports neither Cobra nor `internal/cli`;
- [ ] the unguarded `TopoSort` counts of §10.1 are unchanged (2/1/2/1/2/1/0);
- [ ] no new runtime file name — `isRuntimeState` still lists exactly three names;
- [ ] no shipped sentence changed except the three declared recovery lines of §9.7 and the two D1
      cells.

### 20.3 Definition-of-done for the implementation phase
- [ ] every NEW file of §14 exists and every `✚`/`~` symbol of §5 is present;
- [ ] every §22 criterion ID has a named test, including every lettered/sub-lettered criterion;
- [ ] every §23.2 matrix row has an owning test file per §15.3;
- [ ] the eight documentation surfaces of §12 are updated, with the roadmap target moved to
      **safe reparent/restack**;
- [ ] the full gate of §15.5 passes;
- [ ] `tpatch feature deps --validate-all` passes.

### 20.4 STOP CONDITION

**This exploration ends here. Do not begin implementation from this document.**

Per `.tpatch/steering/local.md` and `docs/engineering-workflow.md`, this agent owns **only**
`.tpatch/features/rebase-plan-guard/exploration.md`; it has edited no production file, run no
`tpatch` phase command, created no commit, tag or scratch artefact, and changed no dependency
registration.

Before implementation begins, the maintainer must:
1. accept this exploration;
2. acknowledge P-1–P-6, including P-3's resolved inline-byte baseline;
3. re-run `tpatch feature deps --validate-all`;
4. advance exploration with `tpatch explore rebase-plan-guard --manual`;
5. author `artifacts/apply-recipe.json` and advance with
   `tpatch implement rebase-plan-guard --manual` before any `tpatch apply` session.

Spec decisions are **fixed**. Nothing in this document reopens one; where the tree and the spec
disagreed, §19 records the disagreement for a declared correction rather than absorbing it.
