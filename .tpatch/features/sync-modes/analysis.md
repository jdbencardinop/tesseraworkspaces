# Analysis

## Summary

`sync-modes` adds **explicit, orthogonal control axes** to `tws sync` without changing what a
no-flag invocation does in either workspace mode. It is not a new rebase engine, not a new ancestry
evaluator, and not a new selector language.

The request compresses three genuinely independent decisions into one word ("mode"). They must not
be modelled as a single enum:

1. **Fetch policy** — does the run contact the network before planning?
2. **Propagation policy** — are stack *roots* advanced onto their configured external/remote base,
   or is the run restricted to replaying local parent tips into local children?
3. **Selection scope** — which logical entries participate: the whole stack, exactly one entry, or
   one explicit root plus its descendant closure?

Any pairing of the three is meaningful, and today's no-flag behaviour is exactly one point in that
cube — a *different* point per mode, which is the first real finding of this analysis. External
sync today is `fetch × remote-root-advancing × full-stack`. Checkout sync today is
`no-fetch × literal-base × full-stack`. The feature must expose the cube honestly rather than
pretend both modes already sit at the same origin.

The second finding is that the two sync implementations disagree on almost every semantic the new
axes touch — base resolution, plan identity (logical name vs Git branch), archived handling,
cross-repo handling, validation source, push persistence, `--update-refs` usage, and completion
gating. Those mismatches are enumerated below and must be *decided in the spec*, not silently
harmonised by the implementer.

The third finding is a hard blocker for selective sync as currently written, and its shape is
**scope, not predicate**. External sync's completion gate `staleStackEdges`
(`internal/cli/sync_helpers.go:117-124,157-173`) already checks the right *kind* of edge: only
in-stack, same-repo parent→child edges whose child worktree is materialized. An entry whose base is
a literal ref (`master`, `origin/master`, a tag, a SHA) or empty yields `GetBranch(...).Name == ""`
and is skipped, so roots are **not** probed against `origin/<default>` today. The blocker is that
the gate iterates **all** such edges in `stack.yaml`: a selective run over one subtree reports
`sync incomplete` and exits 1 because of an untouched stale edge elsewhere. Selective scope
therefore requires the same predicate applied to a **filtered edge set**; it does not require a new
ancestry rule.

The feature is **not present upstream or in the current source** (verified: no selection or policy
flags exist on `syncCmd`, `internal/cli/sync.go:76-80`; no filter fields exist on
`CheckoutSyncOpts`, `internal/checkout_sync.go:488-496`; no `sync-modes` record existed in
`.tpatch/FEATURES.md` before this registration). It **is** one coherent implementation boundary:
one command, one persisted-run contract per mode, one selector resolution shared by both. The
boundary carries exactly two adjacent-surface obligations, both forced by the external downgrade
mechanism writing new runtime state under the feature directory:

- `.sync-state.yaml` has a **second production reader**: `BuildAgentStatus`. `tws status` must be
  adapted so it never attributes the sentinel as a branch (§4.2.5).
- `.sync-state.yaml` has a **third production consumer**: `isRuntimeState`
  (`internal/cli/importcmd.go:110-112,173-179`), which strips runtime-state filenames out of an
  imported archive. The new v2 payload and the run guard must be added to that filter, or an
  imported tarball can plant foreign live sync state into a feature directory (§4.2.6).

Both are compatibility fixes inside this boundary, not `tws status` or `tws import` features.

---

## 1. Grounded current behaviour that must remain byte- and exit-compatible

### 1.1 External mode, no flags

Command surface: `tws sync <feature>` is `cobra.ExactArgs(1)` with a feature-name
`ValidArgsFunction` over `internal.ListFeatures()` (`internal/cli/sync.go:17-27`). Existing flags
are `--verbose/-v`, `--push`, `--continue`, `--abort`, `--test`
(`internal/cli/sync.go:76-80`). Errors surface through Cobra `RunE`, printed to stderr with exit 1
(`internal/cli/root.go:50-54`).

Ordered behaviour of a no-flag external run:

1. `internal.RequireTool("git")` — `os.Exit(1)` on missing git (`internal/exec.go:11-16`).
2. `RequireWorkspace()` (`internal/cli/sync.go:28-32`). This is what makes external
   feature-directory invocation work: when `MainRepoRoot()` fails it falls back to
   `DetectWorkspaceRoot` + `inferExternalRepoRoot` (`internal/workspace.go:440-470`).
3. `GuardFeatureName(internal.TwsRoot(), feature)` — one guard for the plain, `--abort`, and
   `--continue` paths (`internal/cli/sync.go:38-43`).
4. `ws.ResolveFeaturePath(feature)` (`internal/cli/sync.go:44-47`).
5. If `.sync-state.yaml` exists: hard error
   `previous sync incomplete (failed on: %s); use --continue or --abort`
   (`internal/cli/sync.go:56-59`). **This message is a compatibility surface.** Note the exact
   shape: the guard is `internal.HasSyncState` (a `Stat`), while the message interpolates
   `state.FailedBranch` from `state, _ := internal.LoadSyncState(...)` with the error discarded —
   so a state file that **fails to decode** makes `state` nil and panics here rather than erroring
   cleanly. This matters for §4.2's downgrade mechanism.
6. `syncFeature` → `internal.FeaturePath(feature)`. Note this re-derives the path through
   `TwsRoot()` (`internal/paths.go:84-86`) rather than reusing the already-resolved
   `featurePath`; the two can disagree under `TWS_ROOT`/workspace-detection edge cases
   (`internal/cli/sync.go:173-174`).
7. If `stack.yaml` cannot be loaded: `fetchQuiet("", "", verbose)` then `syncFallback`, which
   rebases every worktree directory onto the **hard-coded literal `origin/main`** and calls
   `internal.Must`, i.e. `os.Exit(1)` from inside reusable logic
   (`internal/cli/sync.go:176-181`, `internal/cli/sync_helpers.go:226-235`,
   `internal/exec.go:223-228`). It then returns `Complete: true` unconditionally.
8. **Fetch once per unique repo**: `internal.UniqueRepos(stack, featurePath)` keys by
   `StackEntry.Repo` (empty = default repo) and picks the *first* entry's worktree path as the git
   context, or `""` when that worktree is not materialized (`internal/stack.go:36-52`).
   `fetchQuiet` prints `Fetching <label>... ` then `done`/`failed`, or in `--verbose` prints
   `Fetching <label>...` and streams full git output (`internal/cli/sync.go:196-226`).
   Three facts follow, all of which are current contract:
   - **Fetch failure is non-fatal.** It prints `failed` and the run continues against whatever
     remote-tracking refs already exist. A stale-ref sync can still exit 0.
   - When a repo has no materialized worktree and `Repo == ""`, the fetch runs in the **process
     cwd** (`internal.Run("git","fetch")`, `internal/cli/sync.go:209,219`). From an external
     feature directory that is not a Git repository, so it prints `failed`.
   - The `for repo, wtPath := range repos` loop iterates a Go map, so with multiple repos the
     `Fetching …` line order is **not deterministic** (`internal/cli/sync.go:183-186`). Any
     golden test of multi-repo fetch output must not assume order.
9. `internal.TopoSort(stack)` over the **whole** stack; on cycle it prints `Error: %v` and returns
   an empty `syncResult`, which becomes `sync incomplete` (`internal/cli/sync.go:188-192`,
   `internal/stack.go:138-181`). `TopoSort` seeds its Kahn queue from a **map**, so the order of
   independent roots/siblings is not stable across runs.
10. `syncWithStack` → `syncWithStackFiltered(..., nil)` (`internal/cli/sync_helpers.go:17-19`).

Pass 1 — materialized entries, in topological order (`internal/cli/sync_helpers.go:28-77`):

- Skip entries whose worktree directory does not exist.
- `checkSyncWorktreeBranch` compares the worktree's checked-out branch to `entry.GitBranch()`;
  mismatch prints `[?] <name> (active)` plus problem/hint and **stops the whole run**
  (`internal/cli/sync_helpers.go:35-41`, `internal/health.go:102-121`).
- Base is `resolveEntryBase`: if `entry.Base` names another stack entry **in the same repo**, use
  that parent's `GitBranch()`; otherwise `resolveBase(entry.Base)`, which maps a base equal to
  `internal.DefaultBranch()` to `origin/<default>` and leaves anything else literal
  (`internal/cli/sync_helpers.go:179-191`). `DefaultBranch()` runs `git rev-parse --abbrev-ref
  origin/HEAD` **with no repo argument**, i.e. in the process cwd (`internal/exec.go:59-66`).
- Amend-aware rebase: `git rebase --update-refs <base>`, or
  `git rebase --update-refs --onto <base> <LastBaseSHA>` when the recorded base moved
  (`internal/cli/sync_helpers.go:44-52`).
- Conflict prints `[!] <name> (active)`, the worktree path, `git add . && git rebase --continue`,
  and `tws sync <feature> --continue`, then persists incomplete state
  (`internal/cli/sync_helpers.go:54-60`).
- Validation: `runValidation` uses **`internal.LoadConfig().TestCommand`**, split by
  `strings.Fields`, run silently in the worktree (`internal/cli/sync_helpers.go:237-251`). The
  `--test` flag is *not* consulted in external mode.
- Success prints `[+] <name> (active)`, calls `markUpdatedAncestors`, and persists
  `LastBaseSHA` via `UpdateBaseSHA` + `SaveStack` **after every branch**
  (`internal/cli/sync_helpers.go:66-76`, `internal/stack.go:75-83`).

Pass 2 — non-materialized ("archived") entries (`internal/cli/sync_helpers.go:79-115`):

- A branch whose worktree entry is *prunable* stops the run with
  `[?] <name> (missing — run: tws archive … or tws new …)`.
- An ancestor already moved by `--update-refs` prints `[+] <name> (archived)` and is marked done
  without any Git call (`markUpdatedAncestors`, `internal/cli/sync_helpers.go:194-215`).
- Otherwise a **non-`--update-refs`** `git rebase <base> <gitbranch>` runs in `entry.Repo` or the
  process cwd, with `git rebase --abort` on failure.
- **External sync never reads `StackEntry.Archived`.** "Archived" in external mode means "worktree
  directory absent", not the metadata flag (verified: no `Archived` reference in
  `internal/cli/sync_helpers.go` or `internal/cli/sync.go`).
- Archived-path entries never update `LastBaseSHA`.

Completion gate (`internal/cli/sync_helpers.go:117-125`): `staleStackEdges` walks **all**
`stack.Branches` and, for each child, resolves `internal.GetBranch(stack, child.Base)`. Edges whose
base does not name another stack entry — i.e. every literal-ref and empty base, which is exactly the
root case — are **skipped**, as are cross-repo edges and non-materialized children. For the
remaining in-stack same-repo edges it asserts
`merge-base --is-ancestor <parent.GitBranch()> <child.GitBranch()>`. So the gate is already a
parent→child *local* containment check with no `origin/<default>` probe in it. Any stale edge prints
`Sync incomplete; stale stack edges remain:` plus `[!] <child> does not contain parent <parent>`,
persists state with an **empty** `FailedBranch`, and yields exit 1. Otherwise `.sync-state.yaml`
is deleted and `Sync complete.` is printed. The single problem for this feature is that the walk is
unfiltered.

Optional push (`internal/cli/sync.go:59-68`): prints `\nPushing...` then `pushFeature(feature,
false)`, which pushes **every** stack entry, skipping ones with no worktree directory, using
`git push --force-with-lease origin <entry.Name>` (`internal/cli/push.go:36-77`).
**This uses the logical `Name`, not `GitBranch()`** — a pre-existing decoupled-name defect that
selective push will inherit if it reuses `pushFeature` unchanged.

`--continue` (`internal/cli/sync.go:102-151`): loads state, refuses while a rebase is still in
progress in the failed worktree, reloads the stack, requires the failed entry to still exist and
to now contain its configured parent (`branchContainsConfiguredParent`,
`internal/cli/sync.go:153-160`), prints `[~] <failed> (active)`, builds `done` from
`state.Completed` + the failed branch, **re-runs `TopoSort` over the whole stack**, prints
`Resuming sync with %d pending branch(es)` using `len(state.Pending)`, and re-enters
`syncWithStackFiltered`. `--push` is taken from the *current* invocation and is **not** persisted.

`--abort` (`internal/cli/sync.go:85-100`): missing state prints
`Nothing to abort — no sync in progress.` and exits 0; otherwise it aborts an in-progress rebase in
the failed worktree, deletes state, prints `Sync state cleared.` It does **not** restore any branch
to its pre-sync SHA — external abort is state cleanup, not rollback.

Persisted shape: `SyncState{StartedAt, FailedBranch, Pending, Completed, Skipped}` written
non-atomically with `os.WriteFile(..., 0644)` to `<featurePath>/.sync-state.yaml`
(`internal/syncstate.go:11-49`). `Skipped` is declared and never written. Identity is the
**logical `StackEntry.Name`**.

### 1.2 Checkout mode, no flags

`ws.Mode == ModeCheckout` dispatches to `runCheckoutSync` **before** the external guard/resolution
block (`internal/cli/sync.go:33-35`, `internal/cli/checkout_sync.go:10-56`).

- Feature path via `RequireFeaturePath` (guard + legacy/new layout resolution,
  `internal/resolve.go:294-303`).
- **`RepoDir` is `os.Getwd()`** (`internal/cli/checkout_sync.go:16-20`), *not* `ws.RepoRoot`. Git
  resolves upward so a repo subdirectory works, but a cwd inside a linked worktree of the same
  repository would drive `git checkout` against that worktree instead of the single checkout, and
  a cwd outside the repo fails at the first Git call. This is the checkout half of the cwd
  invocation matrix the v1.2.7 retrospective demands
  (`docs/retrospectives/v1.2.7-upgrade-operations.md:42,50-58`).
- Pre-flight, all before any mutation (`internal/checkout_sync.go:498-522`): existing transaction →
  error; `gitOperationInProgress` (rebase/merge/cherry-pick/revert) → error; dirty working tree →
  `working tree is dirty; commit or stash changes before checkout sync`; detached HEAD →
  `cannot sync from detached HEAD`. `OriginalBranch`/`OriginalHEAD` are captured here.
- Lock: `AcquireCheckoutLock` writes an `O_EXCL` PID lock under `<metadataRoot>/state/`, never
  steals a live lock, and refuses a stale lock that still has a transaction
  (`internal/checkout_sync.go:92-105,172-206`).
- **No fetch anywhere.** Verified: `internal/checkout_sync.go` and
  `internal/cli/checkout_sync.go` contain no `fetch` invocation. Checkout sync is already a
  **no-fetch** command — it is *not* a no-network command, because `--push` still performs
  `git push --force-with-lease` at finalization (`internal/checkout_sync.go:425-433,988-999`).
- `BuildCheckoutPlan` (`internal/checkout_sync.go:447-484`): `TopoSort`, then for each entry —
  **skip `entry.Archived`** (the metadata flag, unlike external), **skip empty `Base`**, resolve
  `entry.Base` **literally** with `git rev-parse` (never through the parent entry's `GitBranch()`,
  never through `origin/<default>`), resolve the child `GitBranch()`, and record
  `{Branch, Base, LastBaseSHA, NewBaseSHA, PreSHA}`. A base that fails to resolve aborts the whole
  plan before any mutation. **`StackEntry.Repo` is never consulted**, so a cross-repo entry is
  planned and rebased inside the single checkout repository.
- Empty plan → release lock, return nil, caller prints `Checkout sync complete.`
  (`internal/checkout_sync.go:530-533`).
- Transaction persisted **before** the first `git checkout` (`internal/checkout_sync.go:535-570`).
- Per branch (`internal/checkout_sync.go:797-873`): re-resolve base SHA, `StagePlanned` hook,
  persist `StageSwitched`, `git checkout`, `doRebase`, ancestry verification, optional validation.
  `doRebase` uses `git rebase --no-fork-point --onto <NewBaseSHA> <LastBaseSHA>` when the recorded
  base moved, else `git rebase --no-fork-point <Base>` — **no `--update-refs`**
  (`internal/checkout_sync.go:875-934`, `internal/checkout_sync.go:337-360`).
- Validation is `opts.TestCommand` run through `sh -c` in `RepoDir`
  (`internal/checkout_sync.go:1062-1074`) — a different source *and* a different execution model
  from external's `cfg.TestCommand` + `strings.Fields`.
- Finalization (`internal/checkout_sync.go:936-1001`): update `LastBaseSHA` for each plan entry by
  matching `stack.Branches[i].GitBranch() == pe.Branch` and `break` on first match, save stack,
  re-verify ancestry for **every** plan entry, `StageRestoring`, `restoreOriginal`, then push.
- `restoreOriginal` (`internal/checkout_sync.go:1023-1055`): checkout the original branch; if it was
  **not** in the plan, assert HEAD still equals `OriginalHEAD` and fail loudly otherwise.
- Push: `git push --force-with-lease origin <pe.Branch>` for every plan entry, using
  `GitBranch()` correctly (`internal/checkout_sync.go:425-433,988-999`).
- `--continue` (`internal/checkout_sync.go:572-594`): force-reclaims a dead lock, **rejects adding
  `--push` to a transaction started without it**, and then *overrides* `opts.Push` and
  `opts.TestCommand` from the transaction. Resume is stage-dispatched
  (`internal/checkout_sync.go:641-739`).
- `--abort` (`internal/checkout_sync.go:596-623`): aborts any in-progress rebase, restores the
  original branch, deletes the transaction, releases the lock. Prints
  `Checkout sync aborted, original branch restored.`
- Persistence is atomic (`atomicWriteFile`, temp + `Sync` + rename, mode `0600`,
  `internal/checkout_sync.go:128-155`) and lives **outside** the feature directory in
  `<metadataRoot>/state/<feature>-checkout-sync.yaml` (`internal/checkout_sync.go:92-102`).
- Identity throughout the transaction is the **Git branch**, not the logical name.

### 1.3 External ↔ checkout mismatch register

Each row is a real divergence in today's code that the spec must resolve explicitly. "Harmonise"
is a decision, not a default.

| # | Concern | External | Checkout |
|---|---------|----------|----------|
| M1 | Fetch | once per unique repo, failure ignored (`sync.go:183-226`) | never (no fetch call exists) |
| M2 | Parent base | parent entry's `GitBranch()` when same repo (`sync_helpers.go:179-184`) | literal `entry.Base` (`checkout_sync.go:459-467`) |
| M3 | Root base | `origin/<default>` when base == default branch (`sync_helpers.go:186-191`) | literal `entry.Base`, so a `master` root does **not** advance from remote |
| M4 | Run identity | logical `StackEntry.Name` (`syncstate.go:11-17`) | Git branch (`checkout_sync.go:50-57`) |
| M5 | Archived | worktree directory absence; `Archived` flag ignored | `entry.Archived` skipped from the plan |
| M6 | Cross-repo | `sameStackRepo` guards base resolution and the stale gate | `Repo` never consulted; planned in the one repo |
| M7 | Rebase flags | `--update-refs`, no `--no-fork-point` | `--no-fork-point`, no `--update-refs` |
| M8 | Validation | `cfg.TestCommand`, `strings.Fields` | `--test`, `sh -c` |
| M9 | `--push` on continue | taken from current invocation, not persisted | persisted; adding it later is rejected |
| M10 | Abort | clears state; no branch rollback | aborts rebase **and** restores original branch |
| M11 | Completion gate | global (unfiltered) in-stack same-repo edge re-probe | per-plan-entry ancestry re-verification |
| M12 | State durability | `os.WriteFile` 0644 inside feature dir | atomic write 0600 outside feature dir |
| M13 | Concurrency | none | PID lock with live/stale discrimination |
| M14 | Push ref | `entry.Name` (**defect** for decoupled names) | `GitBranch()` (correct) |
| M15 | Guards | none (dirty/detached allowed per worktree) | dirty, detached, in-progress-op, lock |

M2/M3 are already documented as a known, deliberate divergence by the shipped ancestry evaluator's
`StackBasePolicy` (`internal/stack_ancestry.go:34-70,498-536`). That is the authority this feature
should cite rather than re-derive.

---

## 2. Three independent axes

### 2.1 Axis F — fetch policy

Values: **fetch** (current external default) and **no-fetch**.

`no-fetch` is strictly an **input-ref policy**: the run performs zero *automatic* remote refresh or
probe — no `git fetch`, no `git ls-remote`, no implicit `origin/HEAD` network lookup, no other
implicit remote contact. Reading local `refs/remotes/*` (`origin/master`, `origin/HEAD` as a local
symref) is explicitly allowed, because those are local object-store reads. Planning and rebasing use
only refs already present locally.

`no-fetch` says nothing about **outbound** actions: an explicit `--push` remains legal and is the
sole opt-in outbound network action. "Zero automatic input refresh" and "zero network at all" are
different guarantees; the second holds for any run without `--push`, in any fetch policy that
disables fetching.

Consequences to settle:

- Under `no-fetch`, an `origin/<default>` root base is still *usable*; it just may be stale. The
  run must not fail merely because the remote-tracking ref is behind — it must say so.
- `internal.DefaultBranch()` prefers `git rev-parse --abbrev-ref origin/HEAD`
  (`internal/exec.go:59-90`), which is a local ref read, not a network call. It is safe under
  `no-fetch`, but it runs in the **process cwd**, which is already wrong for multi-repo external
  stacks and should be routed through `DefaultBranchIn(<repo>)`.
- Under `no-fetch` an `origin/<default>` base that does not exist locally must be a **pre-flight
  rejection**, not a mid-run failure.
- `--push` under `no-fetch` is not a contradiction: `no-fetch` constrains *input* refs, `--push` is
  an explicit output. The spec must say so plainly, because "no hidden network" and "no network"
  are different guarantees and only the first is what `no-fetch` sells.

### 2.2 Axis P — propagation policy

Values: **full** (advance everything, including roots, onto their configured base) and
**local-only** (replay local parent tips into local children; do not advance any root).

Definition that avoids ambiguity: an entry is an **anchor** under `local-only` when its configured
base is not another `StackEntry` **in the same repo**. Two conditions, both required:

- the `stackBaseRef` classification is `StackBaseLiteralRef` or `StackBaseNone`
  (`internal/stack_ancestry.go:318-328`); **or**
- the classification is `StackBaseStackEntry` but `sameStackRepo(parent.Repo, entry.Repo)` is false.

The second clause is not redundant: `stackBaseRef` matches purely on `parent.Name == se.Base` and
**never consults `Repo`**, so a cross-repo parent is classified `StackBaseStackEntry` even though no
in-repo parent tip exists to propagate. `resolveEntryBase` already applies both conditions
(`internal/cli/sync_helpers.go:179-184`), and it is that combined predicate — not `stackBaseRef`
alone — that defines an anchor. Anchors are **not rebased**. Every entry whose base resolves to an
in-stack, same-repo parent is a **propagation edge** and is rebased onto that parent's current local
tip.

This is precisely the "children-only sync" gap recorded in the retrospective
(`docs/retrospectives/v1.2.7-upgrade-operations.md:43`).

### 2.3 Why local-only ≠ no-fetch

They are orthogonal because they constrain different things: F constrains *whether remote-tracking
refs are refreshed*; P constrains *which edges are replayed*. The four combinations are all
distinct and all useful:

| | fetch | no-fetch |
|---|---|---|
| **full** | today's external no-flag behaviour: refresh remotes, advance roots onto `origin/<default>`, replay the whole stack | replay the whole stack onto whatever `origin/<default>` already is locally — reproducible, offline, still root-advancing |
| **local-only** | refresh remotes (so `tws stack status` is honest and a later full run is cheap) but deliberately do **not** move roots this run | fully offline parent→child propagation |

Both `no-fetch` cells have the same network guarantee: with no explicit `--push`, neither performs
any network operation at all. `--push` is the only way either of them reaches the network, and it is
opt-in in every combination.

Concrete separation cases:

- `fetch × local-only` is not a no-op: the fetch changes `origin/*`, which changes what
  `tws stack status` and `tws doctor` report, while the stack itself only reconciles internally.
- `no-fetch × full` still moves a root if `origin/master` has already advanced locally from an
  earlier fetch, a `git pull` in another worktree, or a clone-time state. Calling it "local-only"
  would be a lie.
- On today's checkout mode, `no-fetch` is *already* the input behaviour — checkout never fetches —
  but checkout still pushes when `--push` is given (`internal/checkout_sync.go:425-433,988-999`), so
  "checkout is a no-network command" is false as stated: it is a **no-fetch** command that can
  perform an explicit push. `full` vs `local-only` is still an open choice there, because a checkout
  root spelled literally `origin/master` **is** advanced by `BuildCheckoutPlan` while one spelled
  `master` is not (M3). The axes are therefore needed in checkout mode even though its fetch axis is
  currently pinned.

### 2.4 Axis S — selection scope

Values: **all** (default), **one** (exactly one logical entry), **subtree** (one explicit logical
root plus its descendant closure).

Selector identity — resolved here, not deferred: the selector is a **`StackEntry.Name`**, never a
bare Git branch. Reasons:

- `stack.yaml` keys everything by `Name`: `GetBranch`, `UpdateBaseSHA`, `RenameBranch`,
  `Descendants`, and `TopoSort`'s adjacency all resolve `Base → Name`
  (`internal/stack.go:55-83,93-105,131-190`).
- External state, worktree paths, and all user-facing sync lines already use `Name`
  (`internal/cli/sync_helpers.go:128-145,217-224`).
- `Name` is the documented tws identity; `GitBranch()` is the Git-operation identity
  (`docs/engineering-workflow.md`, "Coding conventions"). A Git branch is not a key: two entries
  may legitimately share one (see §3.2).
- Consequence: **checkout mode must gain a Name↔plan mapping**, because `CheckoutPlanEntry.Branch`
  is a Git branch today (M4). Adding a logical `Name` field to the plan entry is the minimum honest
  fix and is additive to the persisted schema.

Open questions the spec must answer for S (recommendations in §11):

- **Root inclusion for `subtree`:** is the named entry itself rebased, or only its descendants?
  Recommendation: **include it**, because a subtree whose own edge is stale would otherwise be
  replayed onto a stale parent and immediately fail the scoped completion check.
- **No same-stack edge:** when a `local-only` selection resolves to an entry that is an *anchor*
  (base is literal/external), there is nothing to propagate. Recommendation: succeed with an
  explicit `no in-stack parent edge to propagate` line and exit 0 — a no-op selection is not an
  error. Under `full`, the same selection *does* have work (advance onto the literal base).
- **Are ancestors prerequisites or anchors?** Recommendation: under `local-only`, ancestors are
  **anchors** — their current local tips are read, never advanced, never required to be current.
  Under `full` with an explicit selection, ancestors outside the selection are still anchors, but
  the run must *report* a stale ancestor edge as a warning rather than silently replaying a child
  onto a parent that itself needs syncing. Promoting ancestors to prerequisites (auto-expanding the
  selection upward) is a scope creep the spec should reject.

---

## 3. Selected-plan semantics

### 3.1 Order and topological safety

A selected plan is a **filter over the existing topological order**, never a re-derivation. Both
modes already sort first and act second (`internal/cli/sync.go:188-192`,
`internal/checkout_sync.go:448-451`). Filtering after `TopoSort` preserves parent-before-child for
free and cannot introduce a new ordering truth.

Caveat to record: `TopoSort` seeds its queue from a map (`internal/stack.go:157-162`), so sibling
order is unstable across runs. Selection must not be specified in terms of "the Nth entry", and no
golden test may pin sibling ordering. If deterministic display order is wanted, it belongs to the
rebase-plan feature, which is explicitly out of scope.

### 3.2 Duplicate and decoupled branch identities

`stack.yaml` permits two entries with distinct `Name`s and the same `Branch`
(`internal/stack.go:13-29`); `stack-status` already treats duplicate Git branches as a first-class
case. Implications:

- Selection by `Name` is unambiguous; selection by Git branch would not be.
- Checkout's `finalizeTransaction` LastBaseSHA update matches on `GitBranch()` and `break`s on the
  first match (`internal/checkout_sync.go:940-948`) — with duplicates it updates the wrong entry.
  A `Name`-keyed plan fixes this as a side effect; the spec should say whether that correction is
  in scope (recommendation: yes, it is required for correct selected persistence).
- External push already mis-uses `Name` as a Git ref (M14). Selected push must use `GitBranch()`;
  whether the *unselected* `pushFeature` defect is fixed here is a spec decision (recommendation:
  fix it, because selected push cannot share the helper otherwise).

### 3.3 Archived, missing, and materialized entries

- External: selection must not resurrect the pass-2 archived path for an entry the user did not
  select, and must not fail a selected run because an *unselected* prunable worktree exists — today
  that stops the whole run (`internal/cli/sync_helpers.go:86-90`).
- Checkout: `entry.Archived` entries are skipped from the plan. If a user explicitly names an
  archived entry, silently doing nothing is dishonest. Recommendation: reject the selection with a
  message naming the archived state.
- Missing worktree in external mode + explicit selection: the correct answer is an explicit error
  (`<name> has no materialized worktree`) rather than the current silent `continue`
  (`internal/cli/sync_helpers.go:32-34`), because an explicit selection is a user assertion.
- The `Archived`-flag vs directory-absence split (M5) must be stated in the spec; a selector that
  means two different things per mode is not "consistent external and checkout behaviour".

### 3.4 Cross-repo edges and multi-repo fetch boundaries

- External already treats a parent in a different `Repo` as unresolvable and falls back to literal
  base resolution, and skips such edges in the stale gate (`internal/cli/sync_helpers.go:157-173`,
  `internal/cli/new.go:336-341`). Under `local-only` a cross-repo edge is by definition an anchor,
  because there is no in-repo parent tip to propagate.
- Checkout currently ignores `Repo` entirely (M6). A selected checkout run that resolves to a
  cross-repo entry must be **rejected**, matching the shipped ancestry evaluator's
  `cross-repo-unsupported` classification (`internal/stack_ancestry.go:421-425`), rather than
  rebasing something in the wrong repository.
- Multi-repo fetch boundary under selection: fetch must be restricted to the repos actually
  represented in the **selected** plan. Fetching a repo no selected entry touches is exactly the
  hidden work this feature exists to remove. `UniqueRepos` currently derives from the whole stack
  (`internal/stack.go:36-52`) and needs a stack-subset input, not a second implementation.

### 3.5 Literal refs

A literal base (`master`, `origin/master`, a tag, a SHA) is an anchor under `local-only` and a
target under `full`. Two hazards:

- Under `no-fetch` a tag or SHA base is stable; an `origin/*` base is stale-but-valid; a base that
  does not resolve locally must fail pre-flight.
- External `resolveBase` rewrites *only* a base string equal to `DefaultBranch()`
  (`internal/cli/sync_helpers.go:186-191`). Under `local-only` that rewrite must be **suppressed
  for anchors** — but since anchors are not rebased at all under `local-only`, the cleanest
  statement is: `local-only` never calls `resolveBase`, so the `origin/` rewrite cannot leak in.

### 3.6 Selected pushes and validation

- Push must cover exactly the selected, successfully-rebased entries. Pushing the whole feature
  after a one-branch sync would contradict the request and would re-introduce the
  "pushing all branches may include intentional empty placeholders" complaint
  (`docs/retrospectives/v1.2.7-upgrade-operations.md:45`).
- Validation runs per selected entry, in both modes, with the mode's existing execution model
  unless the spec deliberately unifies M8.
- Both push and validation choices must be frozen into persisted state (§4).

### 3.7 Selected-edge completion

This is the blocker named in the summary, and it is a **scoping** change, not a new predicate:

- External: replace the unconditional global `staleStackEdges(feature, stack)` call with the same
  per-edge predicate evaluated over the **selected propagation edge set**. Unselected stale edges
  must be reported as *informational* (they are real, and hiding them would regress observability)
  but must not change the exit code of a scoped run.
- Checkout: the final ancestry loop already iterates `tx.Plan`
  (`internal/checkout_sync.go:957-966`), so it is scope-correct **once the plan itself is scoped**.
- A full-scope run keeps today's exact gate and exit semantics, including the
  `Sync incomplete; stale stack edges remain:` block.
- Under `local-only`, the predicate needed is "each selected child contains its **local** parent
  tip" — which is exactly what `staleStackEdges` already asserts, since it skips literal/empty-base
  (root) and cross-repo edges and never probes `origin/<default>`. So `local-only` completion is
  the existing predicate **filtered to the selected propagation edges**, with no change to the
  ancestry rule itself. What must not happen is filtering it to something *wider* than the
  propagation edges the run actually replayed.

---

## 4. Persistence and recovery

Rule: **every choice that changes what a run does must be resolved and persisted before the first
mutating Git command** *for runs that use a new policy/selection flag*, and must be re-read (never
re-inferred) on `--continue`. The qualifier is load-bearing: today's no-flag external run creates no
state until something fails (`saveIncompleteSync`, `internal/cli/sync_helpers.go:128-145`), and that
write-on-failure-only behaviour is part of the frozen no-flag contract. Introducing a pre-run state
write for legacy invocations would change what a plain `tws sync <feature>` leaves on disk and how a
second process interacts with it, which this feature must not do.

So the rule splits:

- **No-flag runs (both modes):** unchanged. External writes `.sync-state.yaml` only on failure and
  takes no lock. Checkout keeps its existing pre-`checkout` transaction write and PID lock, which it
  already has today.
- **New-mode runs (any policy/selection flag present):** the frozen decision is validated and
  persisted **before** the first mutating Git command, and — in external mode — before the fetch
  loop, because fetch mutates `refs/remotes/*`. This is an intentional new-mode behaviour, declared
  as such, not a retrofit onto legacy runs.

The mutation boundary per mode:

- External: the first `git rebase` in `syncWithStackFiltered` (`internal/cli/sync_helpers.go:54`).
  The *fetch* happens earlier (`internal/cli/sync.go:183-186`); fetch policy is therefore validated
  before the fetch loop in every run, and **persisted** before it only in new-mode runs.
- Checkout: the first `git checkout` in `processBranch` (`internal/checkout_sync.go:821`), which is
  already preceded by a transaction write (`internal/checkout_sync.go:559-566`).

### 4.0 External new-mode concurrency (unresolved; recommended default)

Pre-run persistence in external mode creates a window that does not exist today: two concurrent
`tws sync <f> --<new-flag>` processes could both write state and both rebase. External mode has no
lock at all (M13). Recommended safe default, to be confirmed in the spec (D17):

- Before persistence and before fetch, a new-mode external run **atomically claims** a per-feature
  run guard — `os.OpenFile(..., O_CREATE|O_EXCL, 0600)` under the feature directory, or the
  equivalent already used by `AcquireCheckoutLock` (`internal/checkout_sync.go:172-206`), reusing
  its live-vs-stale PID discrimination rather than inventing a second lock idiom. Note the split in
  the precedent: `AcquireCheckoutLock` **refuses** a stale lock that still has a transaction
  (`internal/checkout_sync.go:196-198`), while `forceAcquireCheckoutLock`
  (`internal/checkout_sync.go:247-270`) is the routine that reclaims a stale lock *with* state, and
  is used only by `--continue`/`--abort` (`internal/checkout_sync.go:579,602`). The external run
  guard must copy the same split: a fresh new-mode run refuses a stale guard that still has a
  payload; only `--continue`/`--abort` reclaim it.
- A concurrent new-mode run is **rejected** with an explicit message; it never proceeds to fetch or
  rebase.
- The guard is released **last** on success and on `--abort` — after the v2 payload and the legacy
  sentinel have been deleted, in that order (§4.2.2) — so a crash can never leave state that no
  live guard explains without also leaving a reclaimable stale guard. A stale guard whose PID is
  dead and which has no accompanying state is reclaimable.
- The guard file lives under the feature directory, so its name (or a reserved prefix) must be
  added to the import runtime-state filter alongside the v2 payload (§4.2.6).
- **No-flag runs neither take nor check this guard**, so legacy concurrency behaviour (currently
  unguarded) is untouched, and a no-flag run is not blocked by a stale new-mode guard beyond the
  existing "previous sync incomplete" state check and the narrow payload-residue refusal of
  §4.2.2.

This is listed as unresolved because it is a new user-visible failure mode; it is recommended
because pre-run persistence without an atomic claim is strictly worse than today.

### 4.1 `SyncState` vs `CheckoutTransaction`

| | `SyncState` | `CheckoutTransaction` |
|---|---|---|
| Location | `<featurePath>/.sync-state.yaml` | `<metadataRoot>/state/<feature>-checkout-sync.yaml` |
| Write | `os.WriteFile`, 0644, non-atomic | temp + fsync + rename, 0600 |
| Version field | none | none |
| Identity | logical `Name` | Git branch |
| Progress | `Pending` / `Completed` / `FailedBranch` | `Plan` + `CurrentIndex` + `CompletedIndices` + `Stage` |
| Invocation context | none (push re-read per invocation) | `Push`, `TestCommand` |
| Pre-mutation snapshot | none | `OriginalBranch`, `OriginalHEAD`, `PreSHA`/`NewBaseSHA` per entry |
| Lock | none | PID lock, live/stale discrimination |

Both structs are unversioned YAML decoded with `yaml.Unmarshal` and no `KnownFields` strictness
(`internal/syncstate.go:22-31`), so **adding fields is backward-compatible on read** in both
directions: absent keys decode to zero values, and old binaries **silently ignore unknown keys**.
The second half is the danger — an old binary reading new scoped state resumes it as a full-stack
run. A version field does not fix this (§4.2).

### 4.2 Schema, downgrade safety, and old-state compatibility

**A schema/version field is forward-only.** It protects *new* binaries from *future* state (a new
binary can refuse a version it does not understand). It cannot protect against an *old* binary
reading new state, because the old decoder was written before the field existed and drops it
silently. Any claim that `schema_version` makes downgrades safe is false; it must be stated as
"new→future protection" only.

**The two modes are asymmetric under downgrade:**

- **Checkout** is largely safe by construction. `resumeTransaction` is driven by the persisted
  `tx.Plan` and `CurrentIndex`/`CompletedIndices` (`internal/checkout_sync.go:641-739`), and a
  scoped run persists a *scoped* plan. An old binary resuming it stays bounded by that plan — it
  cannot broaden the run. It may, however, **ignore newer policy metadata** (e.g. a persisted
  propagation policy or fetch policy), so it can re-resolve bases with old rules for the entries in
  the plan. Bounded, not perfect.
- **External is dangerous.** `handleSyncContinue` re-runs `TopoSort` over the whole stack and calls
  `syncWithStackFiltered` with only `done` as a filter (`internal/cli/sync.go:126-140`). An old
  binary handed new scoped state therefore rebases **every** entry not already in `Completed` — the
  exact broad rebase this feature exists to prevent.

**Required mechanism: fail-closed legacy state for new-flag external runs.** State written by a
new-flag external run must be something today's `LoadSyncState` **cannot successfully consume as a
resumable run**, while still tripping the existing "previous sync incomplete" guard before any Git
mutation. One implementable, testable shape (exact on-disk encoding remains a `define` decision):

1. **New payload in a new file.** The full scoped decision and progress live in a versioned
   document old binaries never open — e.g. `<featurePath>/.sync-state.v2.yaml` with an explicit
   `state_version`. This is the authoritative state for new binaries.
2. **Legacy-path sentinel at `.sync-state.yaml`.** New-flag runs also write the legacy path with a
   *deliberately unusable* `SyncState`: `failed_branch` set to a **per-run collision-resistant
   marker** (construction and mandatory pre-flight in §4.2.1), with empty `pending`/`completed`.
   Consequences on an old binary, all verified against current code, writing `<marker>` for the
   generated value:
   - plain `tws sync <f>`: `HasSyncState` is true → error
     `previous sync incomplete (failed on: <marker>); use --continue or --abort` and
     exit 1, **before any fetch or rebase** (`internal/cli/sync.go:56-59`);
   - `tws sync <f> --continue`: `GetBranch(stack, "<marker>")` returns an empty entry →
     `failed branch %q no longer exists in stack` and exit 1 before any Git mutation
     (`internal/cli/sync.go:118-123`) — **no broad resume**;
   - `tws sync <f> --abort`: clears the legacy sentinel and prints `Sync state cleared.`; it does
     not resume anything, but it also does not abort the real rebase and does not clear the v2
     payload. That combination is a distinct, diagnosable state with a real residual — §4.2.3.

   The sentinel is deliberately kept **decodable**: a type-incompatible legacy file (e.g. encoding
   `pending` as a mapping) would also fail closed on `--continue`, but it makes old plain sync
   dereference a nil `*SyncState` at `internal/cli/sync.go:57-58` and panic instead of printing the
   compatibility error. Fail-closed with a clean message beats fail-closed with a crash. Any
   equivalent custom envelope is acceptable provided it satisfies the same three assertions above
   **and** the marker properties of §4.2.1.
3. **No sentinel for no-flag runs.** Legacy invocations keep writing exactly today's
   `.sync-state.yaml`, byte-shape included, so old and new binaries interoperate unchanged on the
   frozen path. The one qualification is on the *read* side, not the write side: a new binary also
   stats the v2 payload on a no-flag run, so that payload residue left by a prior new-mode run is
   refused rather than silently overrun (the narrow exception of §4.2.2).

Other rules, recommended and to be confirmed in the spec:

1. Add the frozen run decision to both structures: fetch policy, propagation policy, selection
   scope, resolved selector (logical `Name`), and the **materialized selected entry list** (so a
   `stack.yaml` edit between failure and `--continue` cannot silently re-scope the run).
2. Add a `state_version` to the new external payload and to `CheckoutTransaction`. Absent version =
   legacy = full-stack, fetch, full-propagation, which is exactly today's semantics; an *unknown*
   version is refused by new binaries. This is new→future protection only — see above.
3. Persist `Push` in the new external payload, so external `--continue` stops silently changing push
   behaviour (M9). Whether external adopts a symmetric mismatch rejection is a spec decision (D8).
4. Persist the validation command actually used, per mode, so a `--continue` from a different shell
   cannot validate with a different command.
5. Make external state writes atomic by reusing `atomicWriteFile`
   (`internal/checkout_sync.go:128-155`). A crash mid-write currently leaves a truncated
   `.sync-state.yaml` that `LoadSyncState` rejects — and, per §1.1, then panics the plain path —
   which strands the run with no `--continue` route.

#### 4.2.1 Legacy marker construction and mandatory collision pre-flight

A **fixed** literal such as `__tws-scoped-state__` does not satisfy the requirement and is
withdrawn. It is a perfectly valid Git branch name and a perfectly valid `StackEntry.Name`
(verified: `git check-ref-format --branch __tws-scoped-state__` exits 0). A `stack.yaml` that
happens to contain an entry with that `Name` — hand-written, generated, or adversarial — turns the
sentinel into a *resolvable* entry, and the old `--continue` path then finds it in
`GetBranch(stack, ...)` and broad-resumes. A constant cannot carry a safety property that depends
on absence from user data.

Requirement: the marker is **generated per run**, not compiled in. Recommended shape
`tws-scoped-sync-<nonce>.lock`, where `<nonce>` is at least 16 bytes of `crypto/rand` rendered as
hex. Two structural properties are mandatory, and both are directly testable:

1. **Safe single path component.** No `/`, no `\`, no NUL, not `.` or `..`, no leading `-`. This
   matters because the old `--abort` path interpolates `state.FailedBranch` straight into a
   worktree path with no validation — `internal.WorktreePath(feature, state.FailedBranch)`
   (`internal/cli/sync.go:91-96`). A marker that cannot escape the feature directory turns that
   interpolation into a harmless miss. `attributeSyncBranch` is **not** such a site: it joins
   `e.Name` — the *matched* real entry — into the path, and only after `e.Name ==
   external.FailedBranch` has already matched (`internal/agent_status.go:1325-1336`), so a marker
   that matches nothing never reaches `filepath.Join` there. Its hazard is the opposite one
   (silent non-attribution) and is handled in §4.2.5.
2. **Rejected by `git check-ref-format --branch`.** The trailing `.lock` suffix guarantees this
   (verified: `foo.lock` and `tws-scoped-sync-<hex>.lock` are both rejected, while
   `__tws-scoped-state__` is accepted). Consequently `git branch <marker>` fails outright
   (`fatal: 'foo.lock' is not a valid branch name`), so the marker can never name a real ref in any
   repository the run touches.

**Mandatory pre-flight, before the guard claim and before any side effect:** assert that the
generated marker equals neither any current `StackEntry.Name` nor any current
`entry.GitBranch()` in the loaded stack. On collision the run is **refused** with an explicit
message and mutates nothing — no guard, no sentinel, no payload, no fetch. This is one pass over
`stack.Branches` and it is the only rule that has to hold for the mechanism to be sound; the nonce
merely makes reaching it improbable.

Honest strength statement, in three parts, because the three are not equally strong:

- **As a Git branch: structurally impossible.** Git refuses to create a ref ending in `.lock`, so
  no `entry.GitBranch()` can ever equal the marker.
- **As a `StackEntry.Name`: not producible by normal CLI creation.** `tws new` runs
  `git branch <gitBranch>` before registering the entry (`internal/cli/new.go:125-128`), so an
  entry whose `Name` is used as its branch cannot end in `.lock`. With `--branch` decoupling the
  `Name` is not ref-validated, so a hand-crafted `stack.yaml` *can* represent a `.lock`-suffixed
  `Name`; the nonce makes an accidental match astronomically improbable, and the pre-flight turns a
  deliberate one into a refusal rather than a hazard.
- **Residual, stated rather than claimed away.** The pre-flight validates the stack as it exists
  when the run starts. If `stack.yaml` is hand-edited *after* the sentinel is on disk to introduce
  an entry named exactly the live marker, an old binary's `--continue` would resolve it and could
  broad-resume; nothing in the new binary is running at that moment to prevent it. This is not
  claimed to be impossible. New binaries re-assert the invariant on every `--continue`/`--abort`
  and refuse when it is violated, and the case is documented as unsupported tampering.

Test obligation: the collision refusal is a required case (§10.6a), asserted with a stack that
contains an entry whose `Name` is the marker the run would generate (injected through the marker
generator seam) and with one whose `Branch` is that value.

#### 4.2.2 Sentinel/payload lifecycle invariant and the full state matrix

One ordering invariant governs every external **new-mode** run. It is the whole mechanism; every
recovery rule below is derived from it.

1. Claim the run guard (§4.0).
2. Atomically write the **legacy sentinel** to `.sync-state.yaml` — `failed_branch: <marker>`,
   empty `pending`/`completed`. **First**, before any other state.
3. Atomically write the **v2 payload** to `.sync-state.v2.yaml`. **Second.**
4. Only then fetch, plan, and rebase.
5. All progress — completed entries, pending set, real failed entry, frozen decision — is written
   **only** to v2. The sentinel is written exactly once and is never rewritten; in particular it is
   **never overwritten with a real branch or entry name**.
6. On success or on a clean `--abort`: delete the **v2 payload first**, delete the **sentinel
   second**, release the **guard last**.

The ordering is deliberately *symmetric*: sentinel-outermost on both setup and teardown, payload
strictly inside it. Two consequences follow directly, and both are load-bearing for every rule
below:

- **A crash during a healthy new-mode run can never produce payload-without-sentinel.** Setup
  writes the sentinel before the payload; teardown deletes the payload before the sentinel. The
  sentinel is therefore present for the entire lifetime of the payload. Payload-without-sentinel
  is *only* reachable through an old binary's `--abort` (which deletes the sentinel and nothing
  else, §4.2.3) or through external tampering.
- **Sentinel-without-payload is ambiguous, in both directions.** It is produced by a crash between
  step 2 and step 3 (**interrupted initialization**, nothing fetched or rebased) *and* by a crash
  between step 6a and step 6b (**interrupted finalization**, after a complete run or a complete
  abort, with the payload already gone). From disk alone the two are indistinguishable, so no
  message, test, or status projection may claim "no fetch and no rebase happened" in this state.

**`saveIncompleteSync` cannot be reused unchanged for new-mode runs.** It writes
`SyncState{FailedBranch: <real name>, Pending, Completed}` directly to `.sync-state.yaml`
(`internal/cli/sync_helpers.go:128-145`). Calling it from a new-mode run would overwrite the
sentinel with a real, *resolvable* `Name` and hand an old `--continue` precisely the broad resume
this mechanism exists to prevent — silently, at the moment of failure, which is the moment an
operator is most likely to reach for another binary. New-mode failure persistence therefore writes
the v2 payload only, through a separate function. No-flag runs keep calling `saveIncompleteSync`
unchanged, byte-shape included.

**The binary `{sentinel, payload}` model is insufficient and is withdrawn.** The legacy path is not
a boolean: it can hold a *real* legacy `SyncState` written by a genuine no-flag failure, which is
semantically nothing like the sentinel and must not be conflated with it. The payload is not a
boolean either: it can be unreadable or carry an unknown `state_version`. The complete model is:

- **legacy path** (`.sync-state.yaml`) ∈ `{absent, sentinel marker, real legacy state,
  unreadable/invalid}`;
- **v2 payload** (`.sync-state.v2.yaml`) ∈ `{absent, valid-supported, unreadable/unknown-version}`;
- **run guard** ∈ `{live owner, stale/absent}`, which is **precedence and context**, not a fourth
  state axis: it never changes which cell the on-disk state is in, it decides whether that cell is
  a live window or a residue (§4.2.4, §4.2.5).

New-binary behaviour for every material cell. "Refuse" always means *before* any Git mutation and
before any fetch.

| legacy path | v2 payload | Meaning | New-binary plain / `--continue` | New-binary `--abort` |
|---|---|---|---|---|
| absent | absent | no new-mode run touched this feature | **fully normal, frozen**: plain proceeds, `--continue` behaves exactly as today | exactly today: `Nothing to abort — no sync in progress.` (unless a stale guard exists, below) |
| absent | valid-supported | **old-`--abort` residue** (or tampering) — never produced by normal teardown (§4.2.3) | refuse both; name the payload's **real** failed logical entry and its worktree path | performs the §4.2.3 recovery: aborts the real rebase named by the payload if one is still in progress, deletes the payload, removes any residual guard, and **does not require the sentinel to exist** |
| absent | unreadable/unknown-version | new-mode residue that cannot be interpreted | refuse both, fail closed, with manual guidance naming the payload path | refuse to guess: report the unreadable payload and require the operator to remove it explicitly after inspecting the worktrees; never silently delete state whose contents are unknown |
| sentinel marker | absent | **ambiguous**: interrupted initialization *or* interrupted finalization (see above) | refuse both; the message must state the ambiguity and must **not** assert that no fetch or rebase happened. `--continue` refuses because there is no scope to resume | with a stale/absent guard: re-check that no payload exists, then delete the sentinel and the guard and exit clean. With a **live** guard: refuse — this is an in-progress setup/teardown window owned by another process |
| sentinel marker | valid-supported | **authoritative new-mode state** | v2 is the single source of truth for scope, policy, progress, and the real failed entry; the sentinel is never parsed for anything. `--continue` resumes scoped from the payload (stale/absent guard only); plain refuses | drives the abort entirely from the payload, then deletes payload → sentinel → guard in that order |
| sentinel marker | unreadable/unknown-version | new-mode run whose authoritative state is unusable | refuse both, fail closed, manual guidance | refuse to guess, as in the `{absent, unreadable}` cell; the sentinel is not deleted while an uninterpretable payload sits next to it |
| real legacy state | absent | **the frozen legacy path** | byte-compatible: today's `previous sync incomplete (failed on: %s); use --continue or --abort` on plain, today's resume on `--continue` | today's abort, byte for byte |
| real legacy state | valid-supported | **inconsistent mixed state** — a real legacy failure landed on top of surviving new-mode payload | refuse both. The message must name **both** states: the legacy failed entry *and* the payload's real failed entry, because either can own an unfinished rebase | refuse to auto-resolve; walk the operator through both states. Clearing them is two explicit decisions, not one |
| real legacy state | unreadable/unknown-version | inconsistent, and half of it uninterpretable | refuse both, fail closed, manual guidance | refuse; manual only |
| unreadable/invalid | absent | corrupt legacy state | fail closed with a clean error naming the file — **not** the nil-`*SyncState` panic of §1.1, which D15's atomic write plus an explicit decode-error branch removes | report the corrupt file; do not resume |
| unreadable/invalid | valid-supported | corrupt legacy state over new-mode payload | refuse both; name the payload's real failed entry and the corrupt legacy file | refuse to auto-resolve; manual guidance naming both |
| unreadable/invalid | unreadable/unknown-version | nothing on disk is interpretable | refuse both, fail closed | refuse; manual only |

**How `real legacy state + valid payload` actually arises — it is not hypothetical, and it must not
be orphaned silently.** Two reachable sequences:

1. *Old abort, then old plain sync.* An old `--abort` against a live new-mode run deletes the
   sentinel and leaves the payload (§4.2.3), giving `{absent, valid}`. The old binary now sees no
   legacy state at all, so a subsequent **old plain** `tws sync <f>` starts a normal broad
   full-stack run. When that run hits a conflict it calls `saveIncompleteSync`, which writes a
   **real** legacy `SyncState` with a resolvable `FailedBranch` — while the earlier run's payload
   is still on disk. The cell is now `real legacy + valid payload`, and the two states describe
   two different, possibly overlapping unfinished rebases.
2. *Old plain sync racing new-mode setup.* External no-flag runs take no lock and do not consult
   the guard (§4.0, §4.2.4), so an old plain run already in flight can fail and write real legacy
   state at any moment, including after a new-mode run has written its payload. The window is the
   documented concurrency limitation, not a new one, but it lands in the same cell.

In both sequences the payload is a real record of a real run. A new binary must therefore **never**
treat "the legacy file looks like an ordinary failure, so this is the frozen path" as sufficient:
it stats the payload first, and when the payload is present it reports the mixed state and names
both failed entries rather than resuming either.

**The one narrow no-flag exception.** Frozen no-flag behaviour is preserved in the `absent`-payload
column *only*. To reach any of the payload-present cells at all, a new binary must **stat the v2
payload even when the sentinel is absent**, on plain and `--continue`, before Git. That is a
deliberate, explicitly declared exception to "no-flag behaviour is untouched", and it is the only
one:

- It is scoped to a single extra `Stat` and a refusal; it never changes output, exit code, or
  on-disk effect in the `absent`-payload cells, which are the frozen cases the goldens pin.
- It is **unreachable in any repository that has never run a new mode**, and unreachable again once
  the residue is cleared. A repository that only ever used no-flag sync, or that has been fully
  downgraded, never has a payload on disk and therefore never observes the exception.
- Without it, the `{absent, valid}` and `{real legacy, valid}` cells would be invisible to exactly
  the invocation most likely to be typed next, and a new binary would broadly overrun a payload
  that records an unfinished scoped run.

**Old/legacy binaries only** get the unqualified statement: an *old* binary consults only
`HasSyncState`/`LoadSyncState` on `.sync-state.yaml`, never opens the payload, and never reads the
guard, so for **old binaries** sentinel-absent genuinely does mean "unaffected". Every earlier
phrasing of that claim as a property of no-flag behaviour on *new* binaries is re-scoped here and
is false outside the old-binary case.

Crash points map onto the cells as follows, which is why the ordering is fixed:

- after guard, before sentinel → `{absent, absent}` plus a guard-only residue (§4.2.4);
- after sentinel, before payload → `{sentinel, absent}` (**initialization** half of the ambiguity);
- during fetch/rebase, or at a real conflict → `{sentinel, valid}`;
- after payload delete, before sentinel delete → `{sentinel, absent}` (**finalization** half of the
  same ambiguity — the reason that cell can never be described as "nothing happened");
- after sentinel delete, before guard release → `{absent, absent}` plus a guard-only residue;
- torn/partial payload write, if `atomicWriteFile` is bypassed → `{sentinel, unreadable}`.

No healthy crash point produces `{absent, valid}`; that cell is reserved for old-`--abort` residue
and tampering.

#### 4.2.3 Old `--abort` consequences and the residual downgrade risk

What an old binary's `tws sync <f> --abort` actually does against a live new-mode run
(`internal/cli/sync.go:85-100`), step by step:

1. loads the sentinel and reads `FailedBranch`, obtaining the marker;
2. resolves the **marker path** — the worktree directory it believes the failed branch occupies —
   and attempts to abort a rebase there. That directory does not exist (the marker is a per-run
   nonce and, by §4.2.1, a safe single path component under the feature directory), so the call is
   a no-op or an ignored failure. It therefore **does not abort the real failed worktree rebase**,
   which is sitting in the worktree of whichever real entry the new-mode run was replaying;
3. deletes `.sync-state.yaml` and prints `Sync state cleared.`

Net effect, stated plainly: **only the sentinel is deleted.** The v2 payload survives, the real
conflicted worktree is still mid-rebase, and nothing is rolled back. That is the
`{absent, valid-supported}` cell — and, because teardown deletes the payload before the sentinel
(§4.2.2), an old `--abort` (or external tampering) is the **only** way to reach it. No crash in a
healthy run can produce it.

New-binary obligation in that cell — it is a *recovery* obligation, not a resume:

- never resume, and never infer scope from the payload as if the run were still owned;
- never surface the marker; read the payload and name the **real** failed logical entry and its
  worktree path;
- tell the operator to abort or resolve **that actual rebase** (`git rebase --abort` in the named
  worktree, or resolve and stage there), because no tws command has done it for them;
- state that `tws sync <f> --abort` on the new binary then cleans up safely: it aborts the real
  rebase named by the payload if one is still in progress, deletes the payload, removes any residual
  guard, and **does not require the sentinel to exist**;
- reaching this cell at all requires the payload `Stat` on the sentinel-absent path — the narrow
  no-flag exception declared in §4.2.2. Without it a new plain sync would walk straight past the
  residue.

**Unavoidable residual, and where it lands.** Once an old `--abort` has run, the sentinel is gone. A
subsequent **old plain sync** therefore sees no state at all and starts a normal, broad, full-stack
run — against a tree that may still be mid-rebase. (In practice the first `git rebase` usually fails
loudly because a rebase is already in progress, but that is luck, not a designed guarantee.) If
that old run then fails and persists state, it writes a **real** legacy `SyncState` through
`saveIncompleteSync` on top of the surviving payload, producing the
`real legacy state + valid payload` cell of §4.2.2 — the mixed state a new binary must report with
**both** failed entries named rather than resuming either. The mechanism's real guarantee must be
written with its scope attached: **while the sentinel exists**, an old plain sync and an old
`--continue` both fail closed before any Git mutation. It cannot make an old `--abort` understand a
payload format that postdates it, because that code path is already shipped.

**Downgrade after an explicit old `--abort` is therefore unsupported.** It is handled by being
tested and documented — the test asserts the observable facts ("old abort leaves the payload, leaves
the real rebase, and removes the sentinel", and "the new binary refuses and prints the real failed
entry"), not a safety claim — and it is called out in release notes. Claiming the mechanism is
downgrade-safe in general would be false.

#### 4.2.4 New-binary stale and interrupted-state UX

A hard-killed new-mode run **intentionally** leaves a sentinel behind: blocking legacy and no-flag
paths is the point. The cost is that the *new* binary must not present that sentinel through the
legacy channel either, or every recovery message names a nonce.

Requirement: a new binary intercepts its own marker **before** the legacy incomplete-state error is
produced (`internal/cli/sync.go:56-59`) and renders an explicit scoped-sync recovery message
instead of `previous sync incomplete (failed on: <marker>); use --continue or --abort`. It
discriminates cases from the **PID guard** (live/stale) combined with **payload state** — never
from the sentinel, which carries no information beyond "a new-mode run touched this feature":

- **guard live** (owning PID alive): report an active scoped sync, name the owning PID, and refuse.
  No reclaim, in any payload state — including sentinel-only, which under a live guard is simply
  the setup or teardown window of the owning run.
- **guard stale or absent + `{sentinel, valid}`**: interrupted scoped run. Offer `--continue`
  (payload supplies scope, policy, and progress) or `--abort`. Reclaim the stale guard through the
  reclaim routine that already handles "stale lock *with* state" —
  `forceAcquireCheckoutLock` (`internal/checkout_sync.go:247-270`), which is what
  `--continue`/`--abort` use today (`internal/checkout_sync.go:579,602`) — not a second liveness
  idiom, and not `AcquireCheckoutLock`, which deliberately **refuses** a stale lock that still has
  a transaction (`internal/checkout_sync.go:196-198`).
- **guard stale or absent + `{sentinel, absent}`**: **ambiguous** — interrupted initialization
  (crash between sentinel and payload write) or interrupted finalization (crash between payload
  delete and sentinel delete). The message must present it as such. It must **not** say "nothing
  was fetched or rebased": in the finalization case a complete scoped run, or a complete abort,
  already happened. `--continue` refuses, because there is no scope to resume in either reading.
  `--abort` is the resolution: it re-checks that no payload exists, then deletes the sentinel and
  the guard and exits clean — it has nothing to roll back precisely because there is no payload
  naming a rebase, not because it knows the run never started.
- **`{absent, valid}`**: the §4.2.3 message. `--continue` refuses; `--abort` performs the
  recovery described there.
- **`{real legacy, valid}`**: the mixed state of §4.2.2. Refuse both, name both failed entries, and
  require two explicit operator decisions; never auto-clear either file.
- **any unreadable/unknown-version payload**, in any legacy-path state: fail closed with manual
  guidance naming the payload path. Never delete a payload whose contents could not be read.
- **guard only** (`{absent, absent}` plus a stale guard file): reclaimable silently by the next
  new-mode run; refused while the guard is live. A no-flag run neither reads it nor is blocked by
  it (§4.0).

**Byte-compatibility constraint.** The interception fires **only** when the on-disk `failed_branch`
matches the marker shape. Ordinary legacy state keeps producing exactly today's
`previous sync incomplete (failed on: %s); use --continue or --abort`, byte for byte, and today's
`Nothing to abort — no sync in progress.` and `Sync state cleared.` are untouched. The nil-`*SyncState`
panic path noted in §1.1 is addressed by D15's atomic write, not by this branch.

**Residual race, stated honestly.** A no-flag sync that started *before* the sentinel was written is
blocked by nothing: external no-flag runs take no lock at all (M13), and the new-mode guard is
deliberately not consulted by them (§4.0). A new-mode run can therefore begin while a legacy run is
already rebasing, and the guard cannot protect a legacy process it never sees. Closing this would
require the no-flag path to take or check a lock — exactly the frozen behaviour this feature must
not change. It is documented as a known limitation of running two syncs against one feature
concurrently, which is already unsafe today.

#### 4.2.5 Second reader: `tws status` / `BuildAgentStatus`

`.sync-state.yaml` has **three** production consumers, not one: the sync command, `tws status`, and
the import filter (§4.2.6). The second is `BuildAgentStatus` (`internal/agent_status.go:803`),
which reads it through `buildFeatureSync`
(`internal/agent_status.go:1354,1408-1440`) and projects it into `AgentStatusFeatureSync`
(`internal/agent_status.go:294-306`), which is public JSON. Two further internal consumers depend
on that projection: `attributeSyncBranch` (`internal/agent_status.go:1324-1348`) and
`syncWantsBranch` (`internal/agent_status.go:1629-1645`).

**Current behaviour if the sentinel is left unhandled — all false attribution, all silent:**

- `view.FailedBranch` is set to the marker and published as `failed_branch` in the status JSON
  (`internal/agent_status.go:1428-1430`): status asserts a sync failed on a branch that does not
  exist.
- `attributeSyncBranch` looks for an entry whose `Name` equals the marker, finds none, and emits no
  `sync-failed-branch` issue (`internal/agent_status.go:1327-1339`). The real failed entry gets no
  entry-scoped guidance at all.
- `syncWantsBranch` returns false for every real entry, so the dirty-worktree upgrade never fires:
  a worktree the scoped sync genuinely needs is reported as ordinary `worktree-dirty` instead of
  `worktree-dirty-blocking` (`internal/agent_status.go:1617-1624`).
- The sentinel's `pending`/`completed` are empty, so status reports a sync with zero pending work
  while a scoped run is mid-flight.
- The run guard is invisible to status, so external sync has no liveness signal at all — unlike
  checkout, which already reports `live`/`stale`/`invalid` (`internal/agent_status.go:1385-1400`).

**Definition decision D19, recommended default: marker-aware, read-only projection.**
`BuildAgentStatus` detects the marker shape in `.sync-state.yaml` and, instead of projecting the
sentinel, reads `.sync-state.v2.yaml` and projects from it:

- the **real** `failed_branch` (logical `StackEntry.Name`), the real `pending`/`completed` arrays,
  and the existing external sync liveness/state derived from the run guard's PID using the same
  live/stale discrimination the rest of the feature uses;
- the marker is **never** exposed as a branch and never attributed to an entry, in JSON or in
  rendered output;
- because the projection hands `attributeSyncBranch` and `syncWantsBranch` a `*SyncState` carrying
  real logical names, both work unchanged — no second attribution rule is introduced.

The two degenerate states become explicit warnings, in the shape status already uses for sync
problems (`IssueSyncInvalid`, `IssueSyncStale`, `IssueSyncStateInvalid`,
`internal/agent_status.go:108-113`) — but **only when the run guard is stale or absent**:

- `{sentinel, absent}` + stale/absent guard → an **interrupted** scoped sync: warning severity,
  worded for the ambiguity of §4.2.2 (initialization *or* finalization), with the
  `tws sync <f> --abort` hint. It must not assert that no work was done;
- `{absent, valid}` + stale/absent guard → an **invalid** scoped sync: names the inconsistent state
  and the payload's real failed entry, and carries the same recovery guidance as §4.2.3;
- `{real legacy, valid}` + stale/absent guard → an **invalid** sync naming **both** the legacy
  failed entry and the payload's real failed entry (§4.2.2).

**Live-guard precedence.** A live owning guard **dominates** transient sentinel/payload presence.
Both `{sentinel, absent}` and `{sentinel, valid}` occur inside the normal lifetime of a healthy
run — the first is precisely the setup window (sentinel written, payload not yet) and the teardown
window (payload deleted, sentinel not yet), the second is the steady state. While the guard's PID
is alive, status must project an **in-progress scoped sync** owned by that PID, not a degenerate
warning. Emitting "initialization incomplete" against a run that is healthily starting up or
healthily finishing is a false alarm on the most common possible state, and it is exactly the
window a status poll is most likely to sample.

The single exception is a **truly unreadable/corrupt payload**. That is a durable fact about bytes
on disk, not a timing artefact, so it may remain a warning detail even under a live guard — but it
must be phrased as "payload unreadable", never as a claim that the run is dead or abandoned, since
the owning process is demonstrably alive.

**Test determinism.** Status tests must never depend on sampling a real-time window. The
live/stale decision has to be injected through a controlled seam — a guard file written with a PID
the test controls (its own PID for "live"; a PID asserted dead for "stale"), and, where the
implementation needs it, a substitutable liveness predicate alongside `isProcessAlive`. Every state
in the matrix is then constructed on disk directly and asserted deterministically, with no
sleeping, no racing against a real sync process, and no assumption about how long a setup or
teardown window lasts.

**Status remains read-only.** It never deletes, rewrites, reclaims, or repairs the sentinel, the
payload, or the guard, and it never takes the guard. Exactly one mutating authority — the sync
command — is preserved; adding a second would recreate the concurrency hazard §4.0 exists to close.

**Scope clarification.** This is a *necessary compatibility adaptation* forced by writing a sentinel
into a file `tws status` already reads. It is **not** a `tws status` redesign and does not reopen
the `tws status` work excluded in §8. Recommendation: reuse `AgentStatusFeatureSync` as it stands —
since the marker never appears and the projected fields are the ones the struct already has, the
JSON schema is unchanged. No-flag and legacy external state project exactly as they do today, and
the committed status goldens for legacy state must not be re-baselined.

Consequences elsewhere in this analysis: `agent-work-status-dashboard` becomes a **hard** dependency
(§11), and the test strategy gains real status assertions (§10.6a).

#### 4.2.6 Third consumer: `tws import` runtime-state filtering

`.sync-state.yaml` is not only read by two commands — it is also **named explicitly** by a third
production consumer that neither reads nor writes it, but *filters it out*. `tws import` streams a
tarball and skips every entry `isRuntimeState` matches before extraction
(`internal/cli/importcmd.go:110-112`):

```go
func isRuntimeState(path string) bool {
    normalized := filepath.ToSlash(path)
    return strings.HasPrefix(normalized, ".tws/state/") ||
        normalized == ".tws/state" ||
        normalized == ".sync-state.yaml"
}
```
(`internal/cli/importcmd.go:173-179`)

The intent is explicit in the code's own words — "never imports runtime state"
(`internal/cli/importcmd.go:188-190`). The list is **exact-name based**, not prefix based, for the
feature-directory case: `.sync-state.yaml` is matched literally. So a new file named
`.sync-state.v2.yaml`, and a run guard file under the feature directory, are **not** matched today
and would be extracted verbatim into the target feature directory.

**Requirement.** The v2 payload name and the run-guard name (or a single reserved prefix covering
both — e.g. everything matching `.sync-state*` plus the guard's exact name) must be recognized and
discarded by `isRuntimeState`, exactly as `.sync-state.yaml` is. Consequences of not doing this are
concrete, not theoretical:

- An imported archive could **plant foreign live state**: a payload naming entries and worktrees
  from another machine, or a guard file naming a PID that happens to be alive locally.
- With the payload `Stat` of §4.2.2 in place, planted state is not inert — it makes the very next
  plain `tws sync <feature>` refuse, in a feature the operator just imported and has never synced.
  A planted guard with a coincidentally-live PID would additionally block new-mode runs.
- The mixed-state and old-`--abort` cells of §4.2.2 become reachable without any new-mode run ever
  having happened locally, which breaks the "unreachable in a repository that never used new
  modes" property that justifies the narrow exception.

The filter is the natural place for this: it is a single structural allow/deny point, it already
carries the intent, and it is the only import-side code that needs to know these names exist.

**Export is already safe and is unaffected.** `exportTarball` is **allow-listed by construction**:
it writes `workspace.yaml` plus only files whose path relative to the feature directory begins with
`inject/` — "Structurally exclude runtime state: only include inject/ prefix"
(`internal/cli/export.go:146-168`). Neither `.sync-state.yaml` nor any new sibling can enter an
archive produced by this tool, so no export change is required. The import filter exists precisely
because archives are not necessarily produced by this tool.

**Dependency and test consequences.** `isRuntimeState` was introduced by `16e862b`
(`feat(workspace): add checkout workspace lifecycle`, `Tpatch-Feature:
checkout-workspace-lifecycle`), so `checkout-workspace-lifecycle` moves from "genuinely transitive,
left unrecorded" to a **hard** edge (§11). The existing table-driven test
`TestExport_RuntimeStateExcluded` (`internal/cli/checkout_lifecycle_test.go:617-637`) is the
regression seam and gains cases for the new names, alongside an end-to-end import assertion
(§10.6a).

### 4.3 Selector/mode mismatch on continuation

Mismatch detection is only implementable if the command can distinguish "flag absent" from "flag set
to its zero value". Cobra bool flags default to `false`, so `--push` unset and `--push=false` are
indistinguishable by value. **Every policy/selection/bool selector must therefore be read through
`cmd.Flags().Changed("<name>")`**, not through the bound variable alone
(`internal/cli/sync.go:76-80` binds `push`, `cont`, `abort`, `verbose`, `testCmd` today). Rules:

- `--continue` with **no** policy/selection/push flags supplied: use the persisted values verbatim.
  This is the only behaviour that keeps today's `tws sync <f> --continue` working, and it means a
  plain `--continue` is **never** rejected merely because persisted push is true — the persisted
  value simply wins, exactly as checkout already does by overwriting `opts.Push` from `tx.Push`
  (`internal/checkout_sync.go:587-589`).
- `--continue` with flags **explicitly supplied and matching** persisted values: accept (idempotent,
  script-friendly).
- `--continue` with flags **explicitly supplied and conflicting**: reject before any Git call,
  naming both values, in the style of the existing checkout message
  (`cannot add --push to an existing transaction that was started without it; persisted push=%v
  wins`, `internal/checkout_sync.go:583-586`).
- **Today's checkout rule is one-way**, and the analysis must not overstate it: checkout rejects
  only `opts.Push && !tx.Push` — *adding* push to a transaction started without it. The opposite
  direction (`--push=false` explicitly supplied against `tx.Push == true`) is not rejected; the
  persisted `true` silently wins. Making that symmetric is a **new-mode behaviour change** and must
  be declared as such, applied only when the flag was explicitly changed, and never applied to a
  legacy transaction resumed by a plain `--continue`.
- `--abort` must reject explicitly-supplied policy/selection flags (again via `Changed`), because
  abort is defined by the persisted run, not by the current command line.
- Selected entry no longer in `stack.yaml` on continue: refuse, mirroring the existing
  `failed branch %q no longer exists in stack` error (`internal/cli/sync.go:120-123`).

### 4.4 Pending/completed semantics under selection

`saveIncompleteSync` derives `Pending` from the **full sorted list** minus completed minus failed
(`internal/cli/sync_helpers.go:128-145`). Under selection, `Pending` must be derived from the
*selected* list, or `Resuming sync with %d pending branch(es)`
(`internal/cli/sync.go:136`) will report entries the run will never touch. Checkout's
`CompletedIndices` are already plan-relative and need no change beyond the plan being scoped.

### 4.5 All-or-nothing validation before the first mutation

Both modes must perform one combined pre-flight that rejects, with no side effects:

- unknown/incompatible policy or scope combinations (§5);
- an unresolvable selector (unknown `Name`, ambiguous input, archived, cross-repo in checkout);
- a selection that is empty after filtering, when the user named something explicitly;
- refs that cannot resolve locally under `no-fetch`;
- existing incomplete state/transaction (already enforced: `internal/cli/sync.go:56-59`,
  `internal/checkout_sync.go:499-501`), plus the full legacy-path × payload state-matrix
  discrimination of §4.2.2, evaluated with the run guard as precedence context;
- for **every** external run, new-mode or no-flag, a `Stat` of the v2 payload — the one narrow
  exception to frozen no-flag behaviour (§4.2.2). A payload present with no sentinel, or present
  alongside real legacy state, refuses before Git; a payload absent leaves the no-flag path
  byte-identical;
- for new-mode external runs only, the marker collision check of §4.2.1 (marker equals no current
  `StackEntry.Name` and no current `GitBranch()`), evaluated **before** the run guard is claimed;
- for new-mode external runs only, a concurrent run guard already claimed (§4.0);
- checkout guards: dirty, detached, in-progress operation, live lock.

Checkout already has this shape. External has almost none of it and gains it here only for the
**new** flags — plus the single payload `Stat` above — and it must not start rejecting today's
no-flag runs for dirtiness or detachment (M15), or the feature becomes a breaking change.

---

## 5. CLI compatibility and UX constraints

### 5.1 Fixed points

- `tws sync <feature>` with no new flags must produce **byte-identical** stdout/stderr and the same
  exit code in both modes. That includes `Fetching <label>... done`, every `  [x] name (mode)`
  line from `formatSyncStatus` (`internal/cli/sync_helpers.go:217-224`), the conflict guidance
  block, `Sync complete.`, `Checkout sync complete.` (fresh checkout run) and
  `Checkout sync completed.` (checkout `--continue`, `internal/cli/checkout_sync.go:43,55` — two
  distinct strings that must not be normalised), `Sync state cleared.`,
  `Nothing to abort — no sync in progress.`, `Checkout sync aborted, original branch restored.`,
  `previous checkout-sync incomplete; use --continue or --abort`,
  `Resuming sync with %d pending branch(es)`, and the
  `previous sync incomplete (failed on: %s); use --continue or --abort` error.
- **Capturing those goldens requires process-level stdout/stderr capture, not Cobra writers.** Sync
  output comes from two sources that both bypass `cmd.OutOrStdout()`: the package's bare
  `fmt.Println`/`fmt.Printf` calls, and the Git subprocesses themselves, which are wired directly to
  `os.Stdout`/`os.Stderr` (`internal/exec.go:119,128,144`). A test that sets a Cobra output buffer
  captures neither. The golden harness must swap `os.Stdout`/`os.Stderr` for an `os.Pipe` around the
  invocation (or exec the built binary) and compare the combined process streams.
- Adding flags to an existing Cobra command changes `tws sync --help` and the usage block shown on
  argument errors. That drift is unavoidable and must be explicitly accepted and snapshot-tested,
  exactly as `stack-status` accepted help drift for `tws stack`.
- The committed `internal/cli/testdata/existing_commands/**` goldens are pre-change evidence for
  `status`, `doctor`, and `list` and must not be re-baselined
  (`internal/cli/stack_status_test.go:21-32,600-615`). `sync` has no golden today; adding one for
  the no-flag surfaces is the right regression device.
- Completion: `syncCmd`'s `ValidArgsFunction` currently returns feature names for position 0 and
  nothing after (`internal/cli/sync.go:20-27`). A selector flag needs its own
  `RegisterFlagCompletionFunc` over the feature's `StackEntry.Name`s — which requires the feature
  argument to already be present on the command line, and must degrade to no candidates rather than
  error when it is not.

### 5.2 Rejected combinations (before any side effect)

At minimum:

- selection flag without a resolvable feature stack;
- `one` and `subtree` selectors given simultaneously;
- any selector or policy flag **explicitly supplied** together with `--abort`;
- selector/policy flags **explicitly supplied** (via `cmd.Flags().Changed`) and conflicting with
  persisted values on `--continue` (§4.3); absent flags are never a conflict;
- `--continue` and `--abort` together (currently `--abort` silently wins,
  `internal/cli/sync.go:49-55`) — worth rejecting, and it is a small, defensible behaviour change
  the spec should call out rather than sneak in;
- a `local-only` selection naming an entry that does not exist;
- external `--test` remains inert today; if the spec makes `--test` apply to external mode, that is
  a **behaviour change** and must be declared, not smuggled in with the new axes.

### 5.3 Acceptable output additions

New lines are acceptable **only on runs that used a new flag**. Suggested and sufficient: one
header line naming the effective fetch policy, propagation policy, and scope; and, at completion,
an informational block listing stale edges outside the scope. Explicitly **excluded** from this
feature: any old-base/new-base/replay-count preview, which belongs to the `rebase plan guard`
feature (`docs/roadmap.md`, P1 backlog).

---

## 6. Safety and transactions

1. **No hidden network.** Under `no-fetch` (and therefore under every checkout run today), the run
   must issue zero `fetch`/`ls-remote`/implicit-probe commands. `git push` is **not** covered by
   this promise: it is legal, explicit, and opt-in via `--push` in both modes, and checkout performs
   it today (`internal/checkout_sync.go:988-999`). The stronger "zero network at all" property
   therefore holds for any `no-fetch` run **without** `--push`, which is what tests must assert.
   Both are testable by pointing `origin` at a removed/unreachable path: without `--push` the run
   must succeed; with `--push` the push must be the only thing that fails.
2. **No unrelated rebase, push, or metadata write.** A scoped run must leave every unselected
   branch SHA, `LastBaseSHA`, and remote ref untouched. Two known leaks to close:
   - `git rebase --update-refs` (`internal/cli/sync_helpers.go:49-52`) **rewrites refs of any
     branch pointing into the rebased range**, including entries outside the selection. This is not
     hypothetical: `markUpdatedAncestors` exists precisely because `--update-refs` moves
     non-materialized ancestors (`internal/cli/sync_helpers.go:194-215`). The spec must decide
     whether scoped external runs keep `--update-refs` (accepting out-of-scope ref movement, which
     contradicts the safety goal) or drop it for scoped runs (changing conflict/duplicate-commit
     behaviour). Recommendation in §11 (D5).
   - `SaveStack` after each branch (`internal/cli/sync_helpers.go:73-76`) rewrites the whole
     `stack.yaml`; only the selected entries' `LastBaseSHA` may change value.
3. **Current branch restoration.** Checkout already restores `OriginalBranch` and asserts an
   unchanged `OriginalHEAD` when it was not in the plan (`internal/checkout_sync.go:1023-1055`).
   With a scoped plan, an original branch that is *excluded from the scope* becomes the
   `!inPlan` case and is HEAD-asserted — which is correct and is a free safety win. External has no
   equivalent because it never switches branches.
4. **Dirty/detached.** Checkout rejects both (`internal/checkout_sync.go:502-520`) and must
   continue to. External must not gain these rejections for no-flag runs; whether an explicitly
   scoped external run gains them is a spec decision (recommendation: no — keep external guard
   behaviour identical and rely on per-worktree branch checks).
5. **Force-with-lease only.** Both push paths already comply
   (`internal/cli/push.go:70`, `internal/checkout_sync.go:426`). Selected push must not introduce
   a plain `--force`.
6. **Failure state.** External persists incomplete state and exits 1; checkout persists a staged
   transaction, keeps the lock, and exits 1. Both must continue to; scoped state must carry the
   scope so recovery is scoped too.
7. **Mode-aware cwd invocation matrix.** The retrospective requires every feature command to work
   from: repo root; linked worktree root and nested subdirectory; external workspace root; external
   feature directory and nested subdirectory; checkout repo root
   (`docs/retrospectives/v1.2.7-upgrade-operations.md:50-58`). Known weak points this feature
   inherits and should at least *test* (fixing them may be a separate feature — see D9):
   `runCheckoutSync`'s `os.Getwd()` RepoDir (`internal/cli/checkout_sync.go:16-20`);
   `syncFeature`'s re-derivation through `internal.FeaturePath`
   (`internal/cli/sync.go:173-174`); `DefaultBranch()`'s cwd-scoped `origin/HEAD` probe
   (`internal/exec.go:59-66`); and `fetchQuiet`'s cwd fallback (`internal/cli/sync.go:209,219`).
8. **Fetch failure handling, exactly.** Today a failed fetch prints `failed` and the run proceeds
   (`internal/cli/sync.go:212-224`). Changing that to a hard error would break the offline-tolerant
   behaviour users rely on. Recommendation: keep the current tolerance under the default policy,
   and let `no-fetch` be the explicit way to make "do not refresh from the network" a guarantee
   rather than an accident (outbound `--push` stays opt-in and orthogonal). If the spec wants a strict variant, it must be a separate opt-in, not a
   redefinition of the default.

---

## 7. Reuse vs duplication

**Must reuse, unchanged where possible:**

- `internal.TopoSort` (`internal/stack.go:138-181`) — the only ordering truth. Filter its output;
  never re-implement Kahn's algorithm.
- `internal.Descendants` (`internal/stack.go:185-205`) — the only descendant-closure truth, already
  used by `archive` and exercised by `divergent-stack-sync` fixtures. Subtree scope is
  `{selector} ∪ Descendants(stack, selector)` intersected with the sorted list.
- `internal.GetBranch`, `HasBranch`, `UpdateBaseSHA` (`internal/stack.go:55-83`) — selector
  resolution and per-entry persistence.
- `StackEntry.GitBranch()` (`internal/stack.go:23-29`) — every Git-level ref, including the
  selected-push fix.
- `sameStackRepo` (`internal/cli/new.go:336-341`) — the single cross-repo predicate.
- `internal.UniqueRepos` (`internal/stack.go:36-52`) — fetch boundaries, given a scoped stack
  subset rather than a new per-repo walker.
- The shipped `StackEdge` projection and `FeatureStackEdges` /
  `StackBasePolicyForMode` (`internal/stack_ancestry.go:64-70,318-328,706-730,846-863`) — the
  authoritative statement of anchor-vs-parent base selection and of the external/checkout base
  divergence. Anchor classification should be expressed in those terms **combined with
  `sameStackRepo`**, since `stackBaseRef` alone does not distinguish a cross-repo parent (§2.2).
- `internal.CheckWorktreeBranch` (`internal/health.go:102-121`) — pre-rebase worktree identity.
- `atomicWriteFile` (`internal/checkout_sync.go:128-155`) — for the hardened external state write.
- Checkout's plan/transaction/lock/stage machinery
  (`internal/checkout_sync.go:50-105,625-739`) — extend it with scope; do not fork it.

**Must not create a second truth:**

- No second topological sort, descendant walker, or parent lookup. `PrintTree`'s ad-hoc
  `children`/`roots` maps (`internal/stack.go:207-280`) are display-only and must not become a
  selector source.
- No second ancestry classifier. `staleStackEdges` and `branchContainsConfiguredParent`
  (`internal/cli/sync_helpers.go:157-173`, `internal/cli/sync.go:153-160`) are already duplicate
  ancestry logic that the shipped evaluator supersedes. This feature must not add a third; whether
  it *collapses* the existing two into the evaluator is a spec decision (recommendation: only if
  the byte output of the full-scope failure block is preserved exactly — see D6).
- No second selector vocabulary. One selector concept (`StackEntry.Name`) shared by both modes.
- No second base-resolution rule. Anchors and parents are defined once, in the terms
  `stackBaseRef` already uses.
- **Patch identity and tpatch semantics stay out.** The roadmap places change-equivalence behind a
  tesserapatch contract and lists reimplementing patch identity as a non-goal
  (`docs/roadmap.md`, "Tool collaboration contracts" and "Non-goals"). This feature reasons about
  refs and SHAs only.

**Reasonable projection reuse:** `tws stack status` already surfaces per-entry ancestry,
materialization, and upstream state (`internal/stack_status.go:614+`). Sync should not print a
status table, but selector *validation* messages may cite `tws stack status <feature>` as the way
to see why a selection is a no-op.

---

## 8. Scope boundaries

Explicitly **out of scope** for `sync-modes`:

- Rebase plan guard / preview of old base, new base, and replay count — next roadmap feature.
- Safe reparent/restack (changing `Base` in metadata and Git atomically).
- Patch identity, patch-id equivalence, or any tesserapatch contract.
- Multi-parent composition or implicit multi-parent rebases (an explicit roadmap non-goal).
- Arbitrary Git ref selectors (`--onto <sha>`, globs, ranges). The selector is a logical entry name.
- Automatic stash or any auto-cleanup of a dirty tree — checkout must keep rejecting instead
  (`docs/engineering-workflow.md`, "Coding conventions").
- New merge strategies, `--rebase-merges`, or interactive rebase.
- Reworking `tws push` beyond what selected push requires, or redesigning `tws status` or
  `tws import`/`tws export`. Exactly two adjacent-surface changes are in scope, both compatibility
  adaptations forced by writing new runtime state under the feature directory: the **§4.2.5**
  `BuildAgentStatus` change (must not falsely attribute the legacy sentinel marker as a branch; no
  new report fields, no new rendering, no change to legacy/no-flag status projection or its
  goldens), and the **§4.2.6** `isRuntimeState` change (must discard the v2 payload and the run
  guard on import, exactly as it already discards `.sync-state.yaml`; no other import behaviour
  changes, and export needs no change at all because it is already allow-listed to `inject/`).
- Fixing the external feature-directory/cwd resolution gaps beyond covering them with tests
  (D9).
- Any change to `syncFallback`'s hard-coded `origin/main` / `Must` behaviour beyond, at most,
  refusing to run it when new flags are present (D10).

---

## 9. Risk, complexity, and value

**Value: high.** The retrospective lists **five** remaining workflow gaps
(`docs/retrospectives/v1.2.7-upgrade-operations.md:42-46`). This feature closes **one** of them
outright — "No local/no-fetch/children-only sync mode" — and partially addresses a second, the
selective-push half of "Pushing all branches may include intentional empty placeholders; selective
push/status remains useful" (the *selective* `tws status` half stays out of scope, §8 — the
marker-aware projection of §4.2.5 is a compatibility fix, not that feature). It also *covers with
tests* the highest-priority gap, external feature-directory command resolution, without claiming to
fix it (D9). The remaining two — safe reparent/restack and rename/untracked-content loss — are
untouched. It is the current roadmap target (`docs/roadmap.md:37-39`). Offline and children-only
propagation are the difference between a sync that is safe to run mid-review and one that is not.

**Complexity: medium-high**, concentrated in three places rather than spread thin:

1. Reconciling two divergent implementations behind one flag vocabulary (§1.3).
2. Scoping the completion contract without changing full-scope bytes (§3.7).
3. Freezing run decisions in two differently-shaped state files, plus a fail-closed downgrade
   mechanism for external state (§4.2) whose sentinel is also read by `tws status` (§4.2.5) and
   whose new filenames must also be filtered by `tws import` (§4.2.6).

**Principal risks**

| Risk | Impact | Mitigation |
|---|---|---|
| Scoped run silently moves out-of-scope refs via `--update-refs` | breaks the core safety promise | decide D5 explicitly; assert unselected SHAs in tests |
| Old external binary broad-resumes new scoped state | unexpected full-stack rebase | a version field does **not** stop this; fail-closed legacy sentinel per §4.2, tested with the previous release binary |
| Fixed sentinel marker collides with a real `StackEntry.Name` | old `--continue` resolves it and broad-resumes; the whole mechanism fails silently | per-run `crypto/rand` nonce with a `.lock` suffix (never a valid ref) plus a mandatory pre-flight refusal against every `Name` and `GitBranch()` (§4.2.1); collision-refusal test is required |
| New-mode failure path reuses `saveIncompleteSync` | sentinel overwritten with a real, resolvable name at the exact moment an operator reaches for another binary | new-mode failures write the v2 payload only; the sentinel is written once and never rewritten (§4.2.2) |
| Old `--abort` run against a live new-mode run | payload orphaned, the real rebase left in progress, and a later old plain sync starts broadly | detect `{absent, valid}` — reachable only through old `--abort` or tampering, never through healthy teardown — and refuse with recovery instructions naming the real entry (§4.2.3); downgrade after an old `--abort` declared unsupported, tested and documented rather than claimed safe |
| Old plain sync after an old `--abort` fails and writes real legacy state over the surviving payload | mixed `real legacy + valid payload` state describing two different unfinished rebases; either could be silently orphaned | refuse plain and `--continue` and name **both** failed entries; never treat "the legacy file looks ordinary" as sufficient — the payload is stat'ed first (§4.2.2, §4.2.3) |
| New binary trusts sentinel absence and overruns payload residue | broad run on top of an unfinished scoped run's state | the one narrow no-flag exception: stat the v2 payload even when the sentinel is absent (§4.2.2, §4.5); unreachable in repositories that never used new modes |
| Sentinel-only state described as "initialization, nothing happened" | false recovery guidance after an interrupted *finalization*, where a full run or abort already completed | treat sentinel-only as ambiguous everywhere — message, status projection, and tests (§4.2.2, §4.2.4, §4.2.5) |
| Hard-killed new-mode run leaves sentinel/guard behind | feature looks permanently blocked; recovery message names a nonce | marker interception with guard-liveness + payload-state discrimination and per-state reclaim rules via `forceAcquireCheckoutLock` semantics (§4.2.4) |
| `tws status` reads the sentinel unmodified | `failed_branch` published as a nonexistent branch; real failed entry gets no guidance; dirty-blocking upgrade never fires | marker-aware read-only projection in `BuildAgentStatus` over the v2 payload (§4.2.5, D19); status assertions in the suite |
| `tws status` warns on the normal setup/teardown window of a healthy run | false "sync broken" alarms on the most frequently sampled state | live owning guard takes precedence over transient sentinel/payload presence; degenerate warnings only when the guard is stale/absent (§4.2.5, D19) |
| Status tests sample a real-time window | flaky suite, or a green suite that never exercises the live case | construct every state on disk and inject live/stale through a controlled PID seam; no sleeps, no racing a real sync (§4.2.5, §10.6a) |
| `isRuntimeState` does not know the new filenames | an imported archive plants foreign payload/guard state; the next plain sync in a freshly imported feature refuses | add the v2 payload and guard names/prefixes to the import filter (§4.2.6, D20); import regression test; export is already allow-listed to `inject/` and unaffected |
| Old checkout binary resumes with newer policy metadata ignored | bounded but stale-rule replay of the persisted plan | `tx.Plan` already bounds it; persist resolved SHAs, not just policies |
| Full-scope output drift | breaks scripts and the no-flag contract | pinned no-flag goldens for both modes, captured pre-change via process-level stdout/stderr capture (§5.1) |
| Selector meaning differs per mode (archived/cross-repo) | "consistent behaviour" claim is false | resolve M5/M6 in the spec; test both modes on one matrix |
| Scoped completion filtered wider than the replayed edges | scoped run fails on work it never did | derive the gate from the selected propagation edge set only (§3.7) |
| New-mode pre-run persistence without an atomic claim | two concurrent runs rebase the same stack | O_EXCL run guard for new-mode runs only (§4.0, D17); no-flag concurrency untouched |
| Non-atomic external state write | truncated state, nil-deref panic on the plain path | reuse `atomicWriteFile` (§4.2) |
| Checkout `RepoDir = os.Getwd()` under a linked worktree | wrong worktree mutated | at minimum test and document; ideally use `ws.RepoRoot` (D9) |
| Sibling ordering assumptions in tests | flaky suite | never assert sibling order; assert sets and ancestry |

---

## 10. Test strategy (real Git, no mocks)

Existing patterns to build on: `setupGitRepo`/`withWorkspaceEnv`/`createLinearStack`
(`internal/cli/sync_continue_integration_test.go:11-100`), the checkout fixtures and `StepHook`
crash injection (`internal/cli/checkout_sync_test.go:16-90`, `internal/checkout_sync.go:298-303`),
the decoupled-name fixtures (`internal/cli/sync_branch_identity_test.go`), and the external-mode
regression guard `TestCheckoutSync_ExternalSyncUnchanged`
(`internal/cli/checkout_sync_test.go:1167-1194`). Real bare remotes are required for fetch,
no-fetch, and push assertions; `docs/engineering-workflow.md` forbids relying on mocks for Git
behaviour.

Required coverage:

1. **No-flag byte/exit goldens**, captured against the pre-change tree, for both modes: clean run,
   conflict run, `--continue` (including checkout's distinct `Checkout sync completed.` line),
   `--abort`, missing stack (`syncFallback`), stale-edge failure block. Capture must swap
   `os.Stdout`/`os.Stderr` for pipes (or exec the built binary), because Git output and the bare
   `fmt.Print*` calls bypass Cobra's writers (§5.1).
2. **Fetch axis:** default run fetches once per unique repo (assert via a bare remote whose tip
   advanced); `no-fetch` run **without** `--push` against a deleted/unreachable `origin` path still
   succeeds — this is the zero-network assertion, and it applies to every `no-fetch` combination,
   not only `local-only`; `no-fetch --push` against the same unreachable origin fails only at the
   push step, after the rebases succeeded; default run against an unreachable origin still prints
   `failed` and proceeds (current tolerance). A checkout no-flag run against an unreachable origin
   must also succeed, and a checkout `--push` run against it must fail only at push.
3. **Propagation axis:** with `origin/master` ahead, `local-only` leaves the root SHA and every
   `origin/*` ref untouched while children contain the local parent tip; `full` advances the root.
   Repeat with a literal-ref root and an external-base root.
4. **Selection axis:** `one`, `subtree` (root included), and `all`; assert unselected branch SHAs
   and `LastBaseSHA` values are byte-identical before/after.
5. **Scoped completion:** a deliberately stale unrelated edge must not fail a scoped run, and must
   still fail a full run; a `local-only` run whose root is behind `origin/<default>` must still
   exit 0, proving the gate never probes the remote-tracking ref.
6. **Conflict recovery:** conflict inside a scoped run → state carries scope/policy → `--continue`
   with no flags resumes scoped → `--abort` restores (checkout) / clears (external). Plus:
   `--continue` with explicitly conflicting flags rejected before any Git call; `--continue` with
   explicitly matching flags accepted; a plain `--continue` against state whose persisted push is
   true accepted (not rejected) and pushing as persisted; `--abort` with an explicitly supplied
   policy/selection flag rejected. All four must exercise `cmd.Flags().Changed`, i.e. include an
   explicit `--push=false` case that is distinguishable from an omitted `--push`.
6a. **Downgrade / concurrency / sentinel lifecycle:** with the previous released binary (or a test
   harness replaying `LoadSyncState` + `handleSyncContinue` semantics), assert that new-mode
   external state makes a plain old sync stop with the compatibility error before any fetch or
   rebase, and an old `--continue` refuse without rebasing anything. Then, specifically:
   - **Marker properties:** the generated marker is a safe single path component and is rejected by
     `git check-ref-format --branch`; `git branch <marker>` fails.
   - **Collision refusal:** with the marker generator seeded to a value that equals an existing
     `StackEntry.Name`, and separately an existing `entry.GitBranch()`, the run is refused with no
     guard, no sentinel, no payload, no fetch and no rebase on disk.
   - **Lifecycle ordering:** crash injection at each of the six points in §4.2.2 produces exactly
     the predicted state-matrix cell, and a new-mode failure never rewrites the sentinel
     (assert the sentinel bytes are identical before and after a conflict). Two ordering assertions
     are mandatory and follow from teardown deleting the payload first: a crash anywhere in a
     healthy run **never** yields `{absent, valid}`, and both the setup crash (between sentinel and
     payload write) and the finalization crash (between payload delete and sentinel delete) yield
     the **same** `{sentinel, absent}` cell — the ambiguity the messages must respect.
   - **State-matrix coverage:** construct each material cell of §4.2.2 directly on disk (legacy path
     ∈ absent / sentinel / real legacy state / unreadable; payload ∈ absent / valid / unreadable)
     and assert new-binary plain, `--continue`, and `--abort` behaviour per cell. Specifically:
     `real legacy + absent` is byte-compatible with the pre-change goldens on all three verbs;
     `sentinel + valid` resumes scoped; `real legacy + valid` refuses plain and `--continue` and
     names **both** failed entries; `absent + valid` refuses and new `--abort` recovers from the
     payload's real failed entry; every unreadable cell fails closed with manual guidance and
     deletes nothing.
   - **Payload-stat exception:** with only a valid payload on disk (no sentinel, no guard), a plain
     `tws sync <feature>` on the new binary refuses **before any fetch or rebase** — asserted with
     an unreachable `origin` so any fetch would be observable — and a `--continue` refuses too.
     With no payload on disk, the identical invocation is byte-identical to the pre-change golden,
     proving the exception is confined to the payload-present cells.
   - **Mixed-state genesis:** reproduce the §4.2.3 sequence end to end — new-mode run conflicts,
     old `--abort` deletes the sentinel, old plain sync runs and conflicts and writes real legacy
     state — and assert the resulting cell is `real legacy + valid payload` and that the new binary
     reports both states rather than orphaning the payload.
   - **Old `--abort`:** after running it against a live new-mode run, assert the sentinel is gone,
     the payload remains, and the real worktree is still mid-rebase; then assert the new binary
     refuses plain and `--continue`, names the payload's real failed entry and worktree, and that
     new `--abort` cleans payload + guard and aborts the real rebase. Assert the documented
     unsupported case as an observed fact: a following **old plain** sync is no longer blocked.
   - **Stale/interrupted UX:** assert the new binary emits the scoped recovery message rather than
     `previous sync incomplete (failed on: <marker>)`, for each of live-guard, stale-guard +
     `{sentinel, valid}`, stale-guard + `{sentinel, absent}`, `{absent, valid}`,
     `{real legacy, valid}`, and guard-only; that the `{sentinel, absent}` message states the
     ambiguity and never claims no fetch or rebase happened; and that ordinary legacy state still
     emits today's message byte for byte. Live vs stale must come from a **controlled PID seam**
     (guard written with the test's own PID for live, with a PID asserted dead for stale, plus a
     substitutable liveness predicate where needed) — never from hard-killing a process and racing
     its teardown.
   - **Status projection (§4.2.5):** with a sentinel + payload on disk, `BuildAgentStatus` reports
     the **real** `failed_branch`, the real pending/completed arrays, and an external liveness
     derived from the guard; the marker appears nowhere in the report; `sync-failed-branch` is
     attributed to the real entry; a dirty worktree the scoped run needs is reported as
     `worktree-dirty-blocking`. With a **stale/absent** guard, `{sentinel, absent}`,
     `{absent, valid}`, and `{real legacy, valid}` each produce their explicit warning issue. With
     a **live** guard, the same `{sentinel, absent}` and `{sentinel, valid}` inputs produce an
     in-progress scoped-sync projection and **no** degenerate warning — this is the live-precedence
     assertion and it uses the same controlled PID seam, with no timing dependence. An unreadable
     payload keeps its warning detail even under a live guard, worded as unreadable rather than
     dead. Status runs mutate nothing (assert sentinel, payload, and guard bytes unchanged).
     Legacy-state status output is unchanged against the committed goldens.
   - **Import filtering (§4.2.6):** extend the table-driven `TestExport_RuntimeStateExcluded`
     (`internal/cli/checkout_lifecycle_test.go:617-637`) with the v2 payload name and the run-guard
     name (both expected `true`), keeping the existing `.sync-state.yaml`, `.tws/state/…`,
     `stack.yaml`, and `inject/…` rows unchanged. Add an end-to-end import regression: build a
     tarball that deliberately contains `.sync-state.v2.yaml` and a guard file at the feature-
     directory root, import it, and assert neither file exists in the imported feature directory
     and that a subsequent plain `tws sync <feature>` is **not** refused. Assert the same archive
     also fails to plant `.sync-state.yaml`, pinning the pre-existing guarantee. No export test
     change is needed — `exportTarball` is allow-listed to `workspace.yaml` + `inject/`
     (`internal/cli/export.go:146-168`) — but one assertion that a feature directory containing a
     payload and a guard exports neither is cheap insurance against that allow-list regressing.

   Separately, two concurrent new-mode external runs: the second is rejected before fetch, and two
   concurrent **no-flag** runs behave exactly as they do today.
7. **Crash recovery:** `StepHook` interruption at each stage of a scoped checkout transaction,
   resumed by `--continue`.
8. **Archived / missing / prunable:** explicit selection of an archived entry (both meanings),
   an entry with no worktree, and a prunable worktree; unselected instances of each must not stop a
   scoped run.
9. **Cross-repo and multi-repo:** cross-repo edge as anchor under `local-only`; cross-repo selection
   rejected in checkout; scoped multi-repo fetch touches only the selected repos.
10. **Decoupled names:** selection by `Name` where `Branch` differs; duplicate `Branch` across two
    `Name`s; selected push targets `GitBranch()`; `LastBaseSHA` lands on the right entry.
11. **No-op selections:** `local-only` on an anchor, an already-current subtree, and an empty
    filtered plan — each exits 0 with an explicit message and mutates nothing.
12. **Invocation matrix:** every supported cwd from the retrospective, per mode, for at least one
    scoped and one no-flag run.
13. **Safety assertions:** no `fetch`/`ls-remote` ran under `no-fetch`, and no `push` ran without
    `--push` (unreachable-remote technique, §6.1); `git reflog` / SHA snapshots proving unselected
    branches did not move.

---

## 11. Dependency verdict

`tpatch feature deps --validate-all` currently reports `DAG: ok (0 violations)`, and `sync-modes`
has **no** registered edges. Provenance below is from `git log` over the exact files this feature
changes; every proposed parent is already `applied`, so no edge can create a cycle. **Do not
register these here — the parent agent registers approved edges after review.**

Direct commits touching `internal/cli/sync.go` / `sync_helpers.go`:
`88760dc`, `a1dad73`, `0ae3d85`, `57f2c99`, `91371bd`, `def13f9`, `2137112`, `43be9ec`, `4e2f978`,
`aa5559f`, `d23ea7f`, `def87b5`, `1defd28`, `16e862b`, `178a555`, `f526f8f`, `20e4ac8`.
`internal/checkout_sync.go` is touched only by `178a555` and `f526f8f`.

**Recommended hard edges** (this feature rewrites or depends on the semantics they introduced):

| Parent | Reason |
|---|---|
| `keep-track-of-stacked-diffs-and-dependencies` | `88760dc` introduced `stack.yaml`, `TopoSort`, `Descendants`, and dependency-ordered sync — the ordering and closure this feature filters. |
| `sync-continue` | `43be9ec` introduced `SyncState`, `--continue`, `--abort`; this feature extends that persisted contract. |
| `amend-aware-rebase` | `4e2f978` introduced `LastBaseSHA` and `--onto` replay, which every propagation policy must preserve. |
| `checkout-stack-safety` | `178a555` introduced `CheckoutTransaction`, `BuildCheckoutPlan`, staging, locking, restoration — directly extended. |
| `branch-name-decoupling` | `d23ea7f` introduced `StackEntry.Branch`/`GitBranch()`; the selector-identity rule and the selected-push fix depend on it. |
| `multi-repo-workspaces` | `def13f9` introduced `StackEntry.Repo`, `UniqueRepos`, `sameStackRepo` — the fetch-boundary and cross-repo-anchor rules. |
| `fix-default-base-branch` | `aa5559f` introduced `DefaultBranchIn`/`origin/HEAD` resolution — the exact root-advancement behaviour `local-only` suppresses. |
| `archive-worktree` | `a1dad73` introduced the archived/prunable arm of `syncWithStackFiltered` that scoped runs must not trip on. |
| `quiet-fetch-output` | `2137112` owns the `Fetching …/done/failed` bytes and the fetch-failure tolerance the fetch axis modifies. |
| `cobra-migration` | `0ae3d85` owns the `RunE`/flag surface every new flag attaches to, and the `cmd.Flags().Changed` mechanism the continuation rules require (§4.3). |
| `fix-sync-continue-descendants` | `def87b5` directly introduced `branchContainsConfiguredParent` and `staleStackEdges`. The scoped completion contract **modifies both**: the gate is filtered to the selected propagation edges and the continue-time predicate becomes scope-aware. Modified symbols are a hard edge, not an ordering hint. |
| `push-branches` | `2137112` directly introduced `pushFeature`. D13 changes its ref from `entry.Name` to `GitBranch()` and selected push filters its entry set — a semantic change to a directly introduced symbol. |
| `fix-checkout-feature-path-routing` | `f526f8f` directly changed the checkout feature-path routing (`FeaturePath` vs `ResolveFeaturePath`, legacy/new layout) that `runCheckoutSync` calls on every invocation (`internal/cli/checkout_sync.go:11-14`); the invocation matrix and any `RepoDir` work sit on top of it. |
| `fix-external-feature-dir-resolution` | `RequireWorkspace`'s external fallback (`internal/workspace.go:440-470`) is what makes external invocation from a feature directory work at all. The invocation matrix is required acceptance coverage (§10.12), so this is **hard**, not conditional. |
| `agent-work-status-dashboard` | `3b2f4aa` introduced `BuildAgentStatus`, `buildFeatureSync`, `AgentStatusFeatureSync`, `attributeSyncBranch`, and `syncWantsBranch` — the **second reader** of `.sync-state.yaml` (`internal/agent_status.go:294-306,1324-1348,1354,1408-1440,1629-1645`). D19 changes the semantics of `buildFeatureSync`'s external projection so the sentinel is never attributed as a branch. A modified symbol the parent introduced is a hard edge, not an ordering hint. |
| `checkout-workspace-lifecycle` | `16e862b` (`Tpatch-Feature: checkout-workspace-lifecycle`, verified via `git log -S"isRuntimeState" -- internal/cli/importcmd.go`) introduced `isRuntimeState` — the **third consumer** of `.sync-state.yaml` (`internal/cli/importcmd.go:110-112,173-179`). D20 changes its semantics so the v2 payload and the run guard are also discarded on import (§4.2.6), and its committed test `TestExport_RuntimeStateExcluded` (`internal/cli/checkout_lifecycle_test.go:617-637`) gains cases. Same criterion as `agent-work-status-dashboard`: a modified symbol the parent introduced is a hard edge. |

**Recommended soft edges** (ordering/consistency only — consumed unchanged, or adjacent surfaces):

| Parent | Reason |
|---|---|
| `fix-sync-branch-identity` | `1defd28` owns `checkSyncWorktreeBranch` via `GitBranch()`; consumed, not rewritten. |
| `post-rebase-validation` | `2137112` owns external `runValidation`/`cfg.TestCommand`; the command is frozen into state but the helper's behaviour is unchanged. |
| `divergent-stack-sync` | owns the DAG/multi-child fixtures the subtree-scope tests overlap with; test-level only. |
| `stack-ancestry-doctor` | owns `StackEdge`, `stackBaseRef`, and `StackBasePolicyForMode`, reused as vocabulary and read-only classification; D6 defers collapsing onto it. |
| `stack-status` | consumer of the same projection; adding sync scope must not alter its read-only, no-fetch contract. |
| `worktree-health-check` | `57f2c99` owns `CheckWorktreeBranch`, reused unchanged. |
| `clean-git-output` | `2137112` owns `RunDirClean`'s stderr filtering, which shapes rebase output bytes the goldens pin. |
| `fix-missing-completions` | `91371bd` owns `syncCmd`'s `ValidArgsFunction`; selector completion is additive alongside it, not a rewrite. |
| `skill-distribution` | agent skills document the sync surface and must be updated (`docs/engineering-workflow.md`, "Coding conventions"). |

**Direct vs transitive.** The earlier claim that `fix-checkout-feature-path-routing` is "already a
hard ancestor of `checkout-stack-safety` / reachable through it" is **false and withdrawn**. In the
registered DAG the two are siblings: `checkout-stack-safety → checkout-workspace-lifecycle →
workspace-mode-foundation`, while `fix-checkout-feature-path-routing → fix-checkout-lifecycle-layout
→ checkout-workspace-lifecycle → workspace-mode-foundation`. Neither reaches the other, and
`f526f8f` post-dates `178a555` in history, so the asserted direction was reversed as well. It is
recorded as a hard edge above on directness grounds — note that it *is* also reachable through
`fix-external-feature-dir-resolution → fix-checkout-feature-path-routing`, so recording it is a
deliberate directness statement rather than an ordering necessity.

**`checkout-workspace-lifecycle` is transitively covered but recorded as hard anyway.** Verified
path: `.tpatch/features/checkout-stack-safety/status.json` declares
`depends_on: [{slug: checkout-workspace-lifecycle, kind: hard}]`, and `checkout-stack-safety` is a
hard edge above, so `checkout-workspace-lifecycle` is already reachable as an ancestor and the edge
is **not** an ordering necessity. It is recorded on the same **directness** grounds used for
`agent-work-status-dashboard`: §4.2.6 changes the semantics of `isRuntimeState`, a symbol
`checkout-workspace-lifecycle` introduced in `16e862b`, and modifies its committed test. The
earlier listing of `checkout-workspace-lifecycle` among "genuinely transitive candidates, left
unrecorded" was correct only while this feature merely *read* through the lifecycle machinery; it
no longer does, and that listing is withdrawn.

Genuinely transitive candidates, left unrecorded: `workspace-mode-foundation` through
`checkout-workspace-lifecycle`; `delete-feature` through `archive-worktree`
(`archive-worktree → delete-feature`); and `fix-checkout-lifecycle-layout`, which this feature
reads through the routing machinery rather than modifying. `workspace-sibling-links` (`20e4ac8`,
which added `GuardFeatureName` to `sync.go`) is consumed unchanged — leave it out unless the
implementation modifies the guard/listing path, in which case it becomes hard.

**Cycle risk: none.** Every candidate is `applied` and none of them depends, directly or
transitively, on `sync-modes` (which has no dependents). Directness, not reachability, is the
criterion used above: an edge is hard when this feature changes a symbol or semantic the parent
introduced, and soft when it only reads one.

---

## 12. Decisions resolved in this analysis

- **Three axes, not one enum.** Fetch policy, propagation policy, and selection scope are
  independent and must be independently expressible. Exact flag spellings belong to `define`.
- **Selector identity is `StackEntry.Name`.** Never a bare Git branch. Checkout's plan gains a
  logical-name field to make this possible.
- **Anchor definition.** Under `local-only`, an entry is an anchor when `stackBaseRef` classifies
  its base as `StackBaseLiteralRef`/`StackBaseNone` **or** the base names a stack entry in a
  *different* repo (`sameStackRepo` false). `stackBaseRef` alone is insufficient because it never
  consults `Repo`. Anchors are read, never advanced, never required to be current.
- **`no-fetch` is an input-ref policy**, not an offline mode: zero automatic `fetch`/`ls-remote`/
  implicit remote probe, local `origin/*` reads allowed, explicit `--push` still legal. Without
  `--push`, every `no-fetch` combination performs no network operation at all.
- **Local-only ≠ no-fetch**, with the four-cell matrix in §2.3 as the normative statement.
- **Scoped completion is mandatory**, not optional polish — and it is a *filter*, not a new
  predicate: today's gate already checks only in-stack same-repo materialized edges and already
  ignores roots, but it checks all of them, which breaks selective runs.
- **Run decisions are frozen before the first mutation for new-mode runs only** — including before
  the fetch, because the fetch mutates `refs/remotes/*`. No-flag external runs keep today's
  write-on-failure-only, lock-free behaviour.
- **A version field does not make downgrades safe.** It is forward-only; downgrade safety needs a
  fail-closed legacy-path mechanism (§4.2), and even that mechanism's guarantee is bounded: it
  holds *while the sentinel exists*, and cannot survive an explicit old `--abort` (§4.2.3).
- **The legacy sentinel marker is generated per run, not a constant.** A fixed literal is a valid
  Git branch name and a valid `StackEntry.Name`, so it cannot carry a safety property; the marker
  is a `crypto/rand` nonce with a `.lock` suffix (rejected by `git check-ref-format --branch`) and
  is refused before any side effect if it collides with a current `Name` or `GitBranch()` (§4.2.1).
- **Sentinel and payload have one symmetric ordering invariant** — sentinel first, payload second,
  progress to v2 only, teardown deleting **payload first and sentinel second**, guard released
  last — from which every recovery rule is derived, and which makes `saveIncompleteSync` unusable
  for new-mode runs (§4.2.2). Two facts follow and are normative: a healthy run can never leave
  payload-without-sentinel, and sentinel-without-payload is **ambiguous** between interrupted
  initialization and interrupted finalization.
- **The `{sentinel, payload}` boolean model is withdrawn in favour of a full state matrix** —
  legacy path ∈ `{absent, sentinel marker, real legacy state, unreadable/invalid}` × payload ∈
  `{absent, valid-supported, unreadable/unknown-version}`, with the run guard
  (`{live owner, stale/absent}`) as precedence and context rather than a state axis (§4.2.2).
  `real legacy + valid payload` is a reachable mixed state, not a theoretical one, and must be
  reported with **both** failed entries named.
- **One narrow, declared exception to frozen no-flag behaviour.** New binaries stat the v2 payload
  even when the sentinel is absent, so payload residue refuses plain and `--continue` before Git.
  It changes nothing in the payload-absent cells the goldens pin, and it is unreachable in a
  repository that never ran a new mode. The unqualified "sentinel-absent means unaffected" claim
  now applies to **old/legacy binaries only** (§4.2.2, §4.5).
- **`.sync-state.yaml` has three production consumers.** `BuildAgentStatus` reads it, so the
  sentinel forces a marker-aware, read-only projection in `tws status` in which a **live owning
  guard takes precedence** over transient sentinel/payload presence (§4.2.5, D19); and
  `isRuntimeState` filters it out on import, so the v2 payload and the run guard must be filtered
  too or an imported archive can plant foreign live state (§4.2.6, D20). Export is already
  allow-listed to `inject/` and needs no change. Status gains no mutating authority and no schema
  change.
- **Flag presence, not flag value, drives continuation mismatch detection** (`cmd.Flags().Changed`).
- **No-flag behaviour is frozen per mode**, including external's tolerant fetch failure and
  external's lack of dirty/detached guards.
- **Rebase-plan display is excluded**, per the roadmap's separate `rebase plan guard` item.

---

## 13. Unresolved decisions for the spec

| # | Decision | Options | Recommended default |
|---|---|---|---|
| D1 | Flag shape for the three axes | one `--mode` enum vs three independent flags | **Three independent flags.** An enum cannot express `fetch × local-only`, and would force a combinatorial name explosion. |
| D2 | Does `subtree` include the named entry? | include vs descendants-only | **Include.** Excluding it replays children onto a possibly-stale parent. |
| D3 | `local-only` selection with no in-stack parent edge | error vs no-op success | **No-op success, exit 0**, with an explicit "nothing to propagate" line. |
| D4 | Ancestors of a selection | prerequisites (auto-expand) vs anchors | **Anchors.** Auto-expanding upward is a surprising broad rebase, exactly what the roadmap wants stopped. |
| D5 | `--update-refs` in scoped external runs | keep (may move out-of-scope refs) vs drop for scoped runs | **Drop for scoped runs; keep for full-scope runs** so no-flag bytes/behaviour are untouched and scoped runs honour "no unrelated mutation". Requires explicit archived-ancestor handling, since `markUpdatedAncestors` assumes `--update-refs`. |
| D6 | Collapse `staleStackEdges` / `branchContainsConfiguredParent` onto the shipped `StackEdge` evaluator? | collapse now vs keep and scope | **Keep and scope now.** Collapsing risks the full-scope failure block's exact bytes; file the collapse as a follow-up. |
| D7 | State versioning | add `state_version` to both vs rely on absent-key defaults | **Add it, for new→future protection only.** Absent version = legacy full/fetch/full-propagation; unknown version is refused by new binaries. It does **not** address downgrades — see D17b. |
| D8 | External `--push` on `--continue` | keep current (invocation wins, unpersisted) vs persist + reject *explicitly supplied* mismatch | **Persist, and reject only when `--push` was explicitly supplied and conflicts** (`cmd.Flags().Changed`). A plain `--continue` against persisted `push=true` is accepted and pushes as persisted. Checkout's current rule is one-way (rejects adding push only); making it symmetric is a declared new-mode change. |
| D9 | Checkout `RepoDir = os.Getwd()` and external cwd re-derivation | fix here vs test-and-defer | **Test here, fix in a scoped follow-up**, unless the invocation matrix fails — then the fix becomes blocking scope for this feature. The `fix-external-feature-dir-resolution` and `fix-checkout-feature-path-routing` edges are hard either way (§11), since the matrix is required coverage. |
| D10 | `syncFallback` (no `stack.yaml`, hard-coded `origin/main`, `Must` → `os.Exit`) | leave untouched vs refuse when new flags are present | **Refuse when any new flag is present**; leave the no-flag path byte-identical. |
| D11 | Cross-repo entry explicitly selected in checkout mode | reject vs silently skip | **Reject**, matching `cross-repo-unsupported`. |
| D12 | Archived-entry selection semantics (M5) | unify meaning vs mode-specific | **Unify on the metadata `Archived` flag for selection validation**, while leaving each mode's execution path unchanged; report materialization separately. |
| D13 | Fix `pushFeature`'s `entry.Name` push ref (M14) | fix here vs separate feature | **Fix here**, because selected push cannot correctly share the helper otherwise. |
| D14 | Does `no-fetch` forbid `--push`? | forbid vs allow | **Allow.** `no-fetch` constrains input refs; `--push` is explicit output, and checkout already behaves this way. State it in help text, and phrase the guarantee as "no automatic network input", not "offline". |
| D15 | Make external state writes atomic | now vs later | **Now.** A truncated `.sync-state.yaml` currently strands the run with no `--continue` path, and the feature adds more fields to that file. |
| D16 | External `--test` inertness (M8) | leave inert vs make it apply | **Leave inert in this feature**; changing it is an independent behaviour change that would confuse the axis story. |
| D17 | External new-mode concurrency (§4.0) | no guard (today) vs atomic per-feature run guard for new-mode runs | **Atomic O_EXCL claim before persistence and fetch, for new-mode runs only**; reject a concurrent new-mode run; clean the guard on success/abort. Reuse the checkout lock's live/stale PID discrimination *with its existing split*: a fresh run behaves like `AcquireCheckoutLock` and refuses a stale guard that still has a payload (`internal/checkout_sync.go:196-198`), while `--continue`/`--abort` reclaim it like `forceAcquireCheckoutLock` (`internal/checkout_sync.go:247-270,579,602`). The guard file's name must also be added to the import runtime-state filter (§4.2.6). No-flag concurrency behaviour is untouched. |
| D17b | Downgrade safety for new-flag external state (§4.2) | rely on `state_version` (ineffective) vs fail-closed legacy mechanism | **Fail-closed mechanism**: a versioned payload in a file old binaries never read, plus a legacy-path sentinel whose `failed_branch` is a **per-run `crypto/rand` marker** that is a safe single path component and is rejected by `git check-ref-format --branch` (e.g. `tws-scoped-sync-<hex>.lock`), refused before any side effect if it collides with any `StackEntry.Name` or `GitBranch()` (§4.2.1). Ordering is symmetric with the sentinel outermost: sentinel written first and payload second on setup, **payload deleted first and sentinel second** on teardown, guard released last, and the sentinel never rewritten — so `saveIncompleteSync` is **not** reusable for new-mode runs (§4.2.2). That ordering is what makes payload-without-sentinel unreachable in a healthy run and sentinel-without-payload ambiguous. Guarantee is scoped: *while the sentinel exists*, an old plain sync and an old `--continue` fail closed before Git mutation. Exact encoding is a `define` decision; the marker properties, the ordering invariant, and the three assertions in §4.2 are not. |
| D18 | New-binary handling of the §4.2.2 state matrix | resume vs refuse, per cell | **Refuse everywhere except `real legacy + absent payload` (frozen legacy path) and `sentinel + valid payload` (authoritative new-mode state).** `sentinel + valid` drives `--continue`/`--abort` entirely from the payload. `sentinel + absent` is **ambiguous** — interrupted initialization *or* interrupted finalization, indistinguishable on disk — so refuse plain/`--continue` without claiming nothing happened; `--abort` re-checks payload absence, then clears sentinel + guard. `absent + valid` is old-`--abort` residue or tampering (never healthy teardown, which deletes the payload first): refuse, name the payload's **real** failed entry and worktree, instruct the operator to abort/resolve that actual rebase, and let new `--abort` clean payload + guard. `real legacy + valid` is a mixed state after an old abort followed by an old plain run, or an old-run race: refuse and name **both** failed entries; never auto-clear either. Any unreadable/unknown-version payload, or unreadable legacy file, fails closed with manual guidance and deletes nothing. `absent + absent` is fully normal. Never infer scope from the payload alone outside `sentinel + valid`, and never surface the marker. Downgrade **after** an old `--abort` is unsupported, tested, and documented (§4.2.3). |
| D18b | New-binary UX for its own marker (§4.2.4) | reuse the legacy `previous sync incomplete` string vs intercept | **Intercept.** New binaries detect the marker shape before the legacy error is produced and render an explicit scoped-sync recovery message, discriminating live-guard / stale-guard+payload / ambiguous sentinel-only / old-abort residue / mixed state / guard-only from PID liveness + payload state. Stale reclaim uses `forceAcquireCheckoutLock`'s semantics (`internal/checkout_sync.go:247-270`), the routine `--continue`/`--abort` already use to reclaim a stale lock *with* state — not `AcquireCheckoutLock`, which deliberately refuses that case (`internal/checkout_sync.go:196-198`). Ordinary legacy state keeps today's message byte for byte. The residual race with a no-flag sync that started before the sentinel existed is documented, not closed — closing it would change frozen no-flag behaviour. |
| D19 | `tws status` (`BuildAgentStatus`) behaviour on sentinel/v2 state (§4.2.5) | leave it reading the sentinel (false attribution) vs marker-aware projection vs teach status to repair state | **Marker-aware, read-only projection with live-guard precedence.** `BuildAgentStatus` detects the marker, reads the v2 payload, and projects the real `failed_branch`, the real pending/completed arrays, and external sync liveness from the run guard, so `attributeSyncBranch`/`syncWantsBranch` keep working unchanged on real logical names. The marker is never exposed or attributed as a branch. A **live owning guard dominates**: `sentinel + absent` and `sentinel + valid` under a live PID project an in-progress scoped sync, because those are exactly the healthy setup, steady, and teardown windows — no degenerate warning. Degenerate warnings (`IssueSyncInvalid`/`IssueSyncStale`/`IssueSyncStateInvalid`) fire only when the guard is stale/absent, covering `sentinel + absent` (worded for the ambiguity), `absent + valid`, and `real legacy + valid` (naming both entries). A truly unreadable/corrupt payload may keep warning detail even under a live guard, worded as unreadable rather than dead. Status **never** mutates or reclaims state — one mutating authority only. Tests inject live/stale through a controlled PID seam, never a real-time window. This is a compatibility adaptation, not a `tws status` redesign: no schema change, and legacy/no-flag status goldens stay as they are. |
| D20 | `tws import` runtime-state filtering (§4.2.6) | leave `isRuntimeState` matching only `.sync-state.yaml` vs extend it to the new names | **Extend it.** `isRuntimeState` (`internal/cli/importcmd.go:173-179`) matches `.sync-state.yaml` by exact name, so `.sync-state.v2.yaml` and a feature-directory run guard would be extracted verbatim from an untrusted archive — planting foreign live state that, given the §4.2.2 payload `Stat`, would make the very next plain sync of a freshly imported feature refuse. Add both names (or one reserved prefix covering them) so they are discarded exactly as `.sync-state.yaml` is. Cover it in the existing `TestExport_RuntimeStateExcluded` table plus an end-to-end import regression (§10.6a). **Export needs no change**: `exportTarball` is allow-listed by construction to `workspace.yaml` + `inject/` (`internal/cli/export.go:146-168`). Makes `checkout-workspace-lifecycle` a hard edge (§11). |

---

## 14. Reviewer summary

`sync-modes` is viable, correctly scoped to one implementation boundary, and not present upstream
or in the current source. Its real content is three orthogonal axes — fetch policy, propagation
policy, and selection scope — plus the scoped completion and frozen-state contracts that make a
partial run recoverable.

Six things a reviewer should insist on:

1. **The axes stay orthogonal.** A single `--mode` enum cannot express `fetch × local-only` or
   `no-fetch × full`, both of which are distinct and useful. `no-fetch` is an input-ref policy, so
   it composes with an explicit `--push` rather than excluding it.
2. **The completion gate becomes scope-relative.** The predicate is already right —
   `staleStackEdges` (`internal/cli/sync_helpers.go:117-124,157-173`) checks only in-stack,
   same-repo, materialized parent→child edges and never probes `origin/<default>` — but it walks
   every such edge, so an unrelated stale edge fails a correct selective run. Filter it to the
   selected propagation edges; do not invent a second ancestry rule.
3. **Downgrade safety is a mechanism with a stated boundary, not a field and not a promise.** A
   `state_version` is forward-only. New-flag external state must be fail-closed against old
   binaries, whose `--continue` rebuilds a full `TopoSort` and would broad-resume a scoped run
   (§4.2, D17b). The sentinel that achieves this must use a **per-run nonce marker** that is not a
   valid Git ref and is collision-checked against every `Name` and `GitBranch()` before any side
   effect — a fixed literal is a valid branch name and would fail exactly when a stack contains it
   (§4.2.1). The guarantee holds *while the sentinel exists*; an explicit old `--abort` deletes the
   sentinel, leaves the payload and the real rebase behind, and makes a later old plain sync broad
   again. That case is unsupported, tested, and documented — not claimed safe (§4.2.3). Checkout is
   bounded by its persisted plan and needs none of this, so the two modes get different answers.
4. **The state model is a matrix, and its cells follow from the teardown order.** Teardown deletes
   the **payload first**, so a healthy run can never leave payload-without-sentinel — that cell is
   old-`--abort` residue or tampering — and sentinel-without-payload is **ambiguous** between
   interrupted initialization and interrupted finalization, so nothing may claim "no fetch or
   rebase happened" there. The legacy path is not a boolean either: it can hold *real* legacy state
   next to a surviving payload (reachable via old abort → old plain sync → conflict), which must be
   refused with **both** failed entries named rather than either being silently orphaned (§4.2.2).
   Reaching those cells requires the one declared exception to frozen no-flag behaviour: new
   binaries stat the payload even when the sentinel is absent. It is inert in the payload-absent
   cells the goldens pin and unreachable in a repository that never used new modes; the
   unqualified "sentinel-absent means unaffected" claim survives only for **old** binaries.
5. **The sentinel has consequences in two other production consumers.** It must never be surfaced
   to the operator as a branch: new binaries intercept their own marker and render a scoped-sync
   recovery message rather than the legacy `previous sync incomplete` string (§4.2.4, D18b), and
   `BuildAgentStatus` — the **second reader** of `.sync-state.yaml` — must project the real failed
   entry from the v2 payload instead of publishing the marker as `failed_branch`, with a **live
   owning guard taking precedence** so the normal setup/teardown window is projected as an
   in-progress sync rather than a warning (§4.2.5, D19). Separately, `isRuntimeState` — the
   **third consumer** (`internal/cli/importcmd.go:173-179`) — must discard the v2 payload and the
   run guard on import exactly as it discards `.sync-state.yaml`, or an imported archive can plant
   foreign live state that blocks the next sync of a freshly imported feature (§4.2.6, D20). Export
   is already allow-listed to `inject/` and is unaffected. That makes both
   `agent-work-status-dashboard` and `checkout-workspace-lifecycle` hard dependencies. Status stays
   read-only; there is exactly one mutating authority.
6. **The external/checkout mismatches in §1.3 are decided, not absorbed.** Base resolution, plan
   identity, archived meaning, cross-repo handling, validation source, push persistence, and
   `--update-refs` differ today; a claim of "consistent external and checkout behaviour" is false
   until each is settled in the spec.

The no-flag contract in both modes — including external's tolerant fetch failure, its lack of
dirty/detached guards, and checkout's already-no-fetch nature — is frozen and must be pinned with
pre-change goldens before any production edit.
