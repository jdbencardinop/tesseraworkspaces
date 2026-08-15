# Specification — sync-modes

Status: normative definition (Path B). Author: definition agent.
Input of record: `.tpatch/features/sync-modes/analysis.md` (approved, normative),
`.tpatch/features/sync-modes/request.md`, `docs/engineering-workflow.md`,
`docs/roadmap.md`, `.tpatch/steering/local.md`, `.claude/skills/tessera-patch/SKILL.md`.

This document settles every decision D1–D20 and every mismatch M1–M15 raised by the analysis.
Nothing in it is left to implementer discretion. Where this specification refines a *recommendation*
of the analysis rather than adopting it verbatim, the refinement is listed explicitly in §21 with
its justification; every `D`-table default is adopted unless §21 records otherwise.

Normative language: **MUST**, **MUST NOT**, **REQUIRED**, and **REFUSE** are binding. "Refuse"
always means: return a Cobra `RunE` error, exit 1, and perform **no** Git mutation, **no** fetch,
**no** state write, and **no** guard claim before returning.

---

## 0. Decision index

| Where | Settles |
|---|---|
| §3 | D1, D14, D16, requirement 1 (CLI contract) |
| §4 | requirement 2 (no-flag compatibility), M15 |
| §5 | D2, D3, D4, D11, D12, M4, M5, M6, requirement 3 (selector/plan model) |
| §6 | D14, M1, M2, M3, requirement 4 (fetch/propagation matrix) |
| §7 | D5, D6, D13, M7, M8, M11, M14, requirement 5 (rebase execution) |
| §8 | D7, D15, D17, D17b, D18, D18b, M12, M13, requirement 6 (external state machine) |
| §9 | D17b, D18, requirement 7 (downgrade/recovery) |
| §10 | D8, D9, D11, D12, M9, M10, requirement 8 (checkout transaction) |
| §11 | D19, D20, requirement 9 (status/import) |
| §12 | requirement 10 (safety/security) |
| §13 | D9, D10, requirement 11 (implementation plan) |
| §15 | dependency verdict |
| §16, §17 | requirement 12 (acceptance criteria, test matrix) |
| §19 | M1–M15 resolution register |
| §20 | D1–D20 resolution register |

---

## 1. Problem statement

`tws sync <feature>` today performs exactly one point of a three-dimensional behaviour cube, and a
*different* point in each workspace mode:

- external mode: `fetch × root-advancing × full-stack`;
- checkout mode: `no-fetch × literal-base × full-stack`.

Three genuinely independent decisions are compressed into that one point:

1. **Fetch policy (axis F)** — does the run contact the network to refresh remote-tracking refs
   before planning?
2. **Propagation policy (axis P)** — are stack *anchors* (roots and cross-repo/literal-base entries)
   advanced onto their configured base, or is the run restricted to replaying local parent tips
   into local children?
3. **Selection scope (axis S)** — which logical entries participate: the whole stack, exactly one
   entry, or one explicit entry plus its descendant closure?

`sync-modes` exposes all three axes explicitly, in both modes, without changing what a
no-new-flag invocation does in either mode — except for the **four** declared, bounded categories of
observable change enumerated in §4.1 rules 3–7 and §4.5 (a corrupt `.sync-state.yaml`, C1; a
`--push` over a decoupled `Name`/`Branch`, C2; a checkout over a stack with duplicate `GitBranch()`
values, C3, plus C5's additive plan `name:` key; and the two broken cwd cells, C4) — and makes a
partial (scoped) run recoverable through
`--continue` / `--abort` with the run's decisions frozen in persisted state.

Three consequences drive most of this document:

- **Scoped completion.** The external completion gate `staleStackEdges`
  (`internal/cli/sync_helpers.go:157-173`) already applies the right predicate — in-stack,
  same-repo, materialized parent→child containment, never an `origin/<default>` probe — but it
  walks **every** such edge, so an unrelated stale edge fails a correct scoped run with exit 1.
  The gate becomes a **filter** over the selected propagation edges. No new ancestry rule is added.
- **Fail-closed downgrade.** An old binary's `--continue` rebuilds a full `TopoSort` and would
  broad-resume scoped state (`internal/cli/sync.go:126-140`). A `state_version` field cannot stop
  that. New-mode external state is therefore written as a versioned payload old binaries never
  open, plus a legacy-path sentinel whose `failed_branch` is a per-run nonce that no old code path
  can resolve.
- **Three consumers of `.sync-state.yaml`.** Writing a sentinel into that file forces two adjacent
  compatibility fixes inside this boundary: `BuildAgentStatus` must never publish the marker as a
  branch (`internal/agent_status.go:1408-1440`), and `isRuntimeState` must discard the new runtime
  filenames on import (`internal/cli/importcmd.go:173-179`).

---

## 2. Non-goals

Out of scope, and MUST NOT appear in the implementation:

1. Rebase-plan preview of any kind — old base, new base, replay count, or a dry-run listing of the
   plan. That is the roadmap's separate `rebase plan guard` feature. **No preview.**
2. Safe reparent/restack (mutating `StackEntry.Base` in metadata and Git).
3. Patch identity, `patch-id` equivalence, or any tesserapatch contract.
4. Multi-parent composition or implicit multi-parent rebases.
5. Arbitrary Git ref selectors (`--onto <sha>`, globs, ranges, remote refs). The selector is a
   logical `StackEntry.Name`.
6. Automatic stash, `git reset --hard`, plain `--force` push, or any destructive cleanup.
7. New merge strategies, `--rebase-merges`, or interactive rebase.
8. A `tws status` redesign, a `tws stack status` change, a `tws doctor` change, a `tws export`
   change, or any `tws import` behaviour beyond the runtime-state filter of §11.2.
9. A strict "fetch failure is fatal" policy. Fetch tolerance is preserved (§6.4); a strict variant
   is a follow-up (§18).
10. Collapsing `staleStackEdges` / `branchContainsConfiguredParent` onto the shipped `StackEdge`
    evaluator (D6). Recorded as a follow-up (§18).
11. Making external `--test` effective (D16). It stays inert in external mode.

---

## 3. CLI contract

### 3.1 Flag inventory (exact and complete)

`syncCmd` (`internal/cli/sync.go:16-81`) keeps `Use: "sync <feature>"`, `cobra.ExactArgs(1)`, and
its existing `ValidArgsFunction`. Existing flags keep their exact spelling, shorthand, type,
default, and help string:

```go
cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show full git fetch output")
cmd.Flags().BoolVar(&push, "push", false, "Push all branches after syncing")
cmd.Flags().BoolVar(&cont, "continue", false, "Resume after conflict resolution")
cmd.Flags().BoolVar(&abort, "abort", false, "Discard sync state and start fresh")
cmd.Flags().StringVar(&testCmd, "test", "", "Validation command to run after each rebase (checkout mode)")
```

Six flags are added. Their registration order in the source is fixed below as **source
organization only** — it does not control help output:

```go
cmd.Flags().BoolVar(&doFetch, "fetch", false, "Fetch before planning (external default)")
cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Plan and rebase from local refs only; no automatic network input (checkout default)")
cmd.Flags().BoolVar(&full, "full", false, "Advance anchors onto their configured base (default)")
cmd.Flags().BoolVar(&localOnly, "local-only", false, "Replay local parent tips into children; never advance an anchor")
cmd.Flags().StringVar(&only, "only", "", "Sync exactly one stack entry by its logical name")
cmd.Flags().StringVar(&from, "from", "", "Sync one stack entry and its descendant closure, by logical name")
```

**Help ordering is pflag's, not this document's.** `FlagSet.SortFlags` keeps its default value
`true`, so `tws sync --help` renders the local flag block **alphabetically by long name**, whatever
order the flags were registered in. The resulting local-flags block is therefore exactly:

```
--abort, --continue, --fetch, --from, --full, -h/--help, --local-only, --no-fetch, --only, --push, --test, -v/--verbose
```

The implementation MUST NOT set `SortFlags = false` on `syncCmd` (or on any parent flag set) to
force registration order, and no acceptance criterion may assert registration order through help
output. AC 3 snapshots the alphabetical block above.

**Axis mapping.**

| Axis | Values | Flags | Type |
|---|---|---|---|
| F — fetch policy | `fetch`, `no-fetch` | `--fetch`, `--no-fetch` (mutually exclusive) | `bool` |
| P — propagation policy | `full`, `local-only` | `--full`, `--local-only` (mutually exclusive) | `bool` |
| S — selection scope | `all`, `one`, `subtree` | none (default `all`), `--only <name>`, `--from <name>` (mutually exclusive) | `string` |

**Request-to-flag mapping**, so the request's wording is provably covered:

- "one logical branch" → `--only <name>` (scope `one`);
- "an explicit root" → `--only <root-name>` when only the root is wanted, `--from <root-name>` when
  the root is the head of a subtree;
- "an optional descendant subtree" → `--from <name>` (scope `subtree`, **root included**, D2);
- "all-stack default" → neither `--only` nor `--from`.

There is no `--descendants`, no `--children-only`, no `--mode`, and no `--root`. A single `--mode`
enum is rejected (D1): it cannot express `fetch × local-only` or `no-fetch × full` without a
combinatorial name explosion, and the three axes are independently meaningful (§6.1).

### 3.2 Presence semantics — `cmd.Flags().Changed`

Every axis/scope flag, plus `--push`, MUST be read through `cmd.Flags().Changed("<name>")` and not
through the bound variable alone. Rules, binding:

1. **Axis selectors are presence-only.** For `--fetch`, `--no-fetch`, `--full`, `--local-only`, an
   explicit *false* value is REFUSED before any side effect:

   ```
   --fetch does not take an explicit value; use --no-fetch to disable automatic fetch
   --no-fetch does not take an explicit value; use --fetch to enable automatic fetch
   --full does not take an explicit value; use --local-only to restrict propagation
   --local-only does not take an explicit value; use --full to advance anchors
   ```

   Detection: `cmd.Flags().Changed(name) && !value`. This makes `Changed(name)` unambiguously mean
   "the user selected this axis value" and removes the `--full=false` ⇒ `local-only`? ambiguity.
2. **`--push` keeps a real boolean meaning.** `--push` (true), `--push=false` (explicit false), and
   omitted are three distinct inputs. `Changed("push")` distinguishes explicit-false from omitted;
   this is the case AC 26 exercises.
3. **`--only` / `--from` are strings.** Presence is `Changed(name)`. An explicitly supplied empty
   value (`--only ""`) is REFUSED: `--only requires a stack entry name`.
4. **`--test` and `--verbose`** are not axis flags and are never new-mode triggers.

### 3.3 New-mode trigger set (normative)

A run is a **new-mode run** when **any** of exactly these six flags is `Changed`:

```
--fetch  --no-fetch  --full  --local-only  --only  --from
```

Anything else — including `--push`, `--test`, `--verbose`, `--continue`, `--abort` — is **not** a
trigger. `tws sync <f>`, `tws sync <f> --push`, `tws sync <f> -v`, `tws sync <f> --continue`, and
`tws sync <f> --abort` therefore remain **no-flag runs** and take the frozen path of §4.

A `--continue` or `--abort` invocation is classified by the persisted state it resumes, not by the
trigger set: resuming a v2 payload is a new-mode continuation even when no trigger is supplied
(§8.6). Supplying a trigger flag *with* `--abort` is refused (§3.5). Supplying a trigger flag *with*
`--continue` is allowed **only** against v2 state — an external v2 payload or a checkout
transaction with `StateVersion >= 2` — where it is compared against the persisted decision (§10.5);
against absent or real-legacy state the flags would be silently ignored or broad-resumed, so the
invocation is refused by I20 (§3.5).

The trigger set is identical in both workspace modes. In checkout mode `--no-fetch` names the
current default and `--full` names the current propagation policy; supplying either still makes the
run a new-mode run (presence, not value), which means the transaction gains `state_version: 2` and
the header of §3.7 is printed. This is intentional and is what makes `--continue` mismatch
detection possible.

### 3.4 Axis defaults per mode

| Mode | Fetch default | Propagation default | Scope default |
|---|---|---|---|
| external | `fetch` | `full` | `all` |
| checkout | `no-fetch` | `full` (with checkout's literal base resolution, §6.3) | `all` |

Defaults are *mode-dependent for F and identical for P and S*. Defaults apply whenever the
corresponding axis flag pair is absent, in both no-flag and new-mode runs. A new-mode run that
supplies only `--only x` therefore runs `fetch × full × one` in external mode and
`no-fetch × full × one` in checkout mode — i.e. each mode's own default on the axes the user did
not name.

### 3.5 Incompatibility matrix (complete, checked before any side effect)

| # | Condition | Exact error |
|---|---|---|
| I1 | `Changed("fetch") && Changed("no-fetch")` | `--fetch and --no-fetch are mutually exclusive` |
| I2 | `Changed("full") && Changed("local-only")` | `--full and --local-only are mutually exclusive` |
| I3 | `Changed("only") && Changed("from")` | `--only and --from are mutually exclusive` |
| I4 | explicit false on an axis selector | the four strings of §3.2 rule 1 |
| I5 | `Changed("only")` with empty value | `--only requires a stack entry name` |
| I6 | `Changed("from")` with empty value | `--from requires a stack entry name` |
| I7 | `cont && abort` **and** the invocation is a new-mode run (any trigger flag of §3.3 is `Changed`) **or** the state it would act on is new-mode state (external: a sentinel or a payload was observed by §3.6 step 8; checkout: `tx.StateVersion >= 2`) | `--continue and --abort are mutually exclusive` |
| I8 | `abort` and any trigger flag `Changed` **and** `cont` is **not** set (when `cont` is also set, I7 wins — see "I7 before I8" below) | `--abort cannot be combined with %s; abort is defined by the persisted run` (`%s` = the flag names that were changed, sorted, comma-joined, each with the `--` prefix) |
| I9 | any trigger flag `Changed` and the feature has no loadable `stack.yaml` | `sync modes require a stack; feature %q has no readable stack.yaml` (D10) |
| I10 | `--only`/`--from` names an entry not in the stack | `unknown stack entry %q in feature %q; run: tws stack status %s` (feature from `opts.Feature`, §5.2) |
| I11 | `--only`/`--from` names an entry with `Archived: true` | `stack entry %q is archived; restore it with: tws new %s %s` (feature from `opts.Feature`, §5.2) (D12) |
| I12 | checkout mode and the selected set contains an entry whose `Repo` differs from the workspace repo | `stack entry %q belongs to repository %q; checkout sync is single-repository (cross-repo-unsupported)` (D11) |
| I13 | new-mode run and two **selected** entries share one `GitBranch()` | `stack entries %q and %q share Git branch %q; select one of them with --only` |
| I14 | `no-fetch` and a base ref required by the selected plan does not resolve locally | `base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first` |
| I15 | `--continue` with an explicitly supplied axis/scope/push flag that conflicts with persisted **v2** state (a trigger flag against non-v2 state is I20, not I15) | §10.5 / §8.6 message form |
| I16 | new-mode external run while a live run guard is held | `a scoped sync is already running for %q (pid %d, started %s); wait for it or use --continue/--abort after it exits` |
| I17 | generated marker collides with a current `StackEntry.Name` or `GitBranch()` | `refusing to start: generated sync marker %q collides with stack entry %q; re-run` |
| I18 | a **new-mode state path** is a symlink: `.sync-state.v2.yaml` on any run; `.sync-run.lock` whenever §3.6 step 8c reads it; `.sync-state.yaml` on a new-mode run, or on any run whose legacy file decoded to a sentinel marker, or that is handling a v2 payload or the guard | `refusing to use %s: runtime state path is a symlink` |
| I19 | checkout mode and cwd resolves to a different working tree than `ws.RepoRoot` | `checkout sync operates on %s but the current directory belongs to working tree %s; run it from the repository checkout` (§10.9) |
| I20 | `cont` **and** any trigger flag of §3.3 is `Changed` **and** the state being resumed is not v2 (external: the classifier found **no** valid-supported payload, i.e. cells 1 and 7; checkout: no transaction exists, or `tx.StateVersion < 2`) | `cannot use sync mode flags on --continue without v2 state; continue without them or abort and start a new run` |

**I20 in full.** A `--continue` carrying `--fetch`, `--no-fetch`, `--full`, `--local-only`,
`--only`, or `--from` is a request to compare the supplied axes/scope against the run being
resumed. That comparison is only meaningful against authoritative v2 state: an external v2 payload
(§8.4) or a checkout transaction with `StateVersion >= 2` (§10.2). When the state is absent or real
legacy — external cells 1 and 7 — or when the checkout transaction is absent or legacy, there is
nothing to compare against, and the pre-I20 behaviour would either silently ignore the flags or
resume broadly over the whole stack while the user asked for a scope. Both outcomes are refused
instead, with the **one exact string** above, used verbatim and identically in both workspace
modes, before any Git command, any fetch, any lock or guard claim, and any state write. The message
is a Cobra `RunE` error, exit 1. It is a state-dependent check, so it is ordered at §3.6 step 9
(external) and §10.5 rule 0 (checkout), not with the pure command-line checks.

**I20 precedence, binding.** I20 fires **only** in the cells whose `--continue` would otherwise
proceed and silently discard the flags: external cells 1 (`{absent, absent}`) and 7
(`{real legacy, absent}`), and the checkout absent/legacy-transaction cases. Cell 5
(`{sentinel, valid}`) is authoritative v2 state, so I20 never applies there: trigger flags are
compared against the payload by §10.5 rules 1–5. In every other external cell — 2, 3, 4, 6, 8, 9,
10, 11, 12 — and under a live owning guard, `--continue` is already refused for the cell's own
reason, that cell's §8.7 message wins, and I20 is not evaluated. Trigger flags supplied with
`--abort` **and no `--continue`** are refused earlier by I8, in every cell and every transaction
version; when `--continue` is also present, I7 is refused earlier still (see "I7 before I8" below),
and in neither case is I20 reached.

**I20 never touches the no-flag contract.** It requires an explicit trigger flag, so a plain
`--continue` — which is a no-flag invocation (§3.3) — keeps today's absent/legacy behaviour byte
for byte, in both modes (§4.2 item 5, §4.3 item 12).

**I7 before I8 — precedence, binding.** `--continue`, `--abort`, and a trigger flag can all appear
in one invocation, and both I7 and I8 match it. **I7 is evaluated first, and its exact string wins**:

```
--continue and --abort are mutually exclusive
```

I8 is then **not evaluated** and its string MUST NOT appear for that input. This is why the I8 row
carries the extra conjunct "`cont` is not set": I8 governs `--abort` + trigger **only when
`--continue` is absent**. Rationale: `--continue --abort` is a contradiction in the command line
itself and is decidable without knowing which trigger flags were supplied, so reporting it is
strictly more useful than reporting a trigger-flag combination for a verb pair that cannot both run;
and one input MUST produce exactly one exact error. The complete table for these three inputs, with
no other cell possible:

| `--continue` | `--abort` | trigger flag | Refusal, exact and single |
|---|---|---|---|
| yes | yes | yes | **I7** — `--continue and --abort are mutually exclusive`, at §3.6 step 3 (both modes), before any state read |
| yes | yes | no | **deferred I7** — same string, but only when the state is new-mode (external §3.6 step 8e; checkout §10.5 rule 8); otherwise today's behaviour: `--abort` wins, `--continue` ignored |
| no | yes | yes | **I8** — `--abort cannot be combined with %s; abort is defined by the persisted run` |
| no | yes | no | no refusal; today's `--abort` behaviour, frozen |
| yes | no | yes | not an I7/I8 case; governed by I20 (§3.5) or §10.5 rules 1–5 according to state |

The precedence is a pure ordering rule inside the command-line check block: I1–I6, then I7, then I8
(§3.6 step 3, §10.1/§10.5 for checkout). It adds no state read, and it changes no message text.

I1–I6 are pure command-line checks and MUST be evaluated before any filesystem access beyond
what Cobra already did; I8 is the same kind of check, evaluated in the same block, immediately
**after** I7. I7 is a pure command-line check **whenever a trigger flag is `Changed`**;
without a trigger flag it is deferred to step 8e (external) / §10.5 rule 8 (checkout), because
distinguishing legacy state from new-mode state requires reading state that those steps already
read. I9–I20 require reading state and are ordered in §3.6; I20 specifically is evaluated at step 9
(external) and §10.5 rule 0 (checkout), against state those steps already load, and adds no
additional read.

Rejected combinations that are **not** errors, stated so the implementer does not add them:

- `--continue --abort` on a **legacy or absent** state with **no** trigger flag keeps today's
  behaviour exactly: `--abort` wins and `--continue` is ignored, in both modes
  (`internal/cli/sync.go:49-55`, `internal/cli/checkout_sync.go:30-43`). Freezing this is required
  by §4.1; I7 only fires for new-mode invocations and new-mode state.
- an ordinary legacy `.sync-state.yaml` that is a **symlink**, with no payload beside it and no
  trigger flag, is **not** refused: the no-flag path reads it exactly as today, following the link.
  This is a declared legacy safety limitation, not a fix deferred by oversight (§12 item 7).
- `--no-fetch --push` is **legal** in both modes (D14). `no-fetch` constrains *input* refs; `--push`
  is an explicit, opt-in *output*.
- `--local-only --push` is legal.
- `--local-only` on an entry that is an anchor is a **no-op success**, not an error (D3, §5.6).
- `--only`/`--from` naming an entry with no materialized worktree is **not** an error (§5.7).
- `--fetch` in checkout mode is legal (§6.2).

### 3.6 Validation order (zero side effects until step 13)

Binding order for `RunE`. Steps 1–12 mutate nothing.

1. `internal.RequireTool("git")` — unchanged, first, `os.Exit(1)` behaviour untouched.
2. `internal.RequireWorkspace()`.
3. Pure command-line checks, in this exact order: I1–I6; then I7 when any trigger flag is `Changed`;
   then I8, which is **skipped** when I7 already fired ("I7 before I8", §3.5). These
   run **before** mode dispatch so both modes reject identically and identically early.
4. Mode dispatch: `ws.Mode == internal.ModeCheckout` → `runCheckoutSync(...)` with the resolved
   options struct (§10.1). External continues below.
5. `internal.GuardFeatureName(internal.TwsRoot(), feature)` — unchanged.
6. `ws.ResolveFeaturePath(feature)` — unchanged.
7. **New-mode runs only**: symlink refusal I18 over `.sync-state.yaml`, `.sync-state.v2.yaml`, and
   `.sync-run.lock` (`os.Lstat`, `Mode()&os.ModeSymlink != 0`). A no-flag run does **not** perform
   this pass; its only symlink-sensitive read is step 8b.
8. **State discrimination** (§8.6), through the single shared classifier
   `internal.ClassifyExternalSyncState(featurePath, internal.SyncClassifyOpts{AlwaysReadGuard: newMode})`
   (§11.1). This is where the one declared no-flag added **runtime-state-path** read lives (§4.4);
   the two declared read-only Git argv changes are separate and live in §4.1 rule 6. Sub-order,
   binding:
   - a. **legacy path**: on a new-mode run step 7 already refused a symlink; on a no-flag run the
     file is read exactly as today (`HasSyncState` + `LoadSyncState`), following a symlink as today.
   - b. **payload**: `os.Lstat(<featurePath>/.sync-state.v2.yaml)` — the single added ordinary
     runtime-state-path read. A symlink at this path is refused (I18) on **every** run, because no
     legacy run ever creates it; a regular file is read and decoded.
   - c. **guard**: `.sync-run.lock` is `Lstat`-ed (I18) and read **only** when the run is a new-mode
     run, or when step 8a decoded a sentinel marker, or when step 8b found a payload. A no-flag run
     with no payload and no sentinel never touches the guard.
   - d. produces a cell of the 12-cell matrix plus guard precedence.
   - e. **deferred I7**: when `cont && abort` survived step 3 (no trigger flag), refuse now **iff**
     step 8a decoded a sentinel marker or step 8b found a payload; otherwise `--abort` wins exactly
     as today.
9. Dispatch on `--abort` / `--continue` / plain per the matrix cell (§8.6). Before the `--continue`
   arm of cells 1 and 7 runs, refuse **I20** when `cont` is set and any trigger flag of §3.3 is
   `Changed` — i.e. when step 8 found no valid-supported payload. `--abort` and
   `--continue` end here in every cell except the two resumable ones.
10. New-mode only: `internal.LoadStack(featurePath)`; on failure → I9.
11. New-mode only:
    `internal.ResolveSyncSelection(stack, policy, internal.SyncSelectionOpts{Mode: ws.Mode, NewMode: true, Feature: feature})`
    (§5.2) → I10, I11, I12, I13; then the `no-fetch` local-ref preflight → I14. `Feature` is the
    `<feature>` argument this run already guarded and resolved (§3.6 steps 5–6), so the I10/I11
    strings name the feature the user asked for.
12. New-mode only: generate the marker through the package-`cli` seam `syncMarkerFn` (§8.2) and run
    the collision preflight → I17.
13. New-mode only: claim the run guard → I16. **First side effect.**
14. New-mode only: write the sentinel, then the payload (§8.5).
15. Print the header (§3.7).
16. Fetch (or skip under `no-fetch`).
17. Plan and rebase.

For a **no-flag** run, steps 7 and 10–15 do not exist; step 8 degrades to today's `HasSyncState`
behaviour plus the single payload `Lstat` of step 8b (§4.4), with the guard read of step 8c reached
only when that `Lstat` found a payload or the legacy file decoded to a sentinel; and step 16 is
today's fetch loop.

### 3.7 New-mode output — header, no-op, informational, and error text

**Header.** Printed to stdout exactly once, **only** on new-mode runs and on `--continue` of v2
state, and always **before any fetch**:

- **External** — printed by `RunE` immediately after §3.6 step 14 (sentinel + payload written) and
  before the fetch of step 16.
- **Checkout** — printed by `internal.RunCheckoutSync` at §10.3 step 10, i.e. after the whole
  read-only preflight I9–I14 has passed and after the lock is held, and before the optional
  `--fetch` refresh of step 11; on `--continue` of a `state_version >= 2` transaction it is printed
  by `internal.ContinueCheckoutSync` after the §10.5 rules have passed and before any Git call. It
  is therefore **never** printed by a run refused during validation, and a crash between the header
  and the transaction write can leave the header on stdout with no state on disk (§10.3, AC 57).

```
Sync mode: fetch=<fetch|no-fetch> propagation=<full|local-only> scope=<all|only:NAME|subtree:NAME>
```

Format string: `"Sync mode: fetch=%s propagation=%s scope=%s\n"`. `NAME` is the resolved logical
`StackEntry.Name` exactly as supplied. No trailing blank line.

**No-op success** (D3). When the selected set under `local-only` contains no propagation edge:

```
  [-] NAME (no in-stack parent edge to propagate)
Nothing to propagate.
```

Exit 0, no fetch beyond what the fetch policy already performed, no rebase, no `stack.yaml` write.
The `[-]` symbol is the existing `skipped` symbol from `formatSyncStatus`
(`internal/cli/sync_helpers.go:217-224`). When the scope is `subtree`/`all` and *several* selected
entries are anchors, one `[-]` line is printed per anchor, in topological order, and the trailing
`Nothing to propagate.` line is printed only when **no** selected entry was rebased. In external
mode the block is printed by package `cli`; in checkout mode it is printed by
`internal.RunCheckoutSync` through `printLocalOnlyNoOp` (§3.10 path 3, §10.3), and by nothing else.

**Informational out-of-scope stale edges.** Printed after a successful scoped run (scope ≠ `all`),
before `Sync complete.`, only when the list is non-empty:

```
Stale stack edges outside this scope (unchanged by this run):
  [i] CHILD does not contain parent PARENT
```

Exit code remains 0. The `CHILD does not contain parent PARENT` payload is byte-identical to the
string `staleStackEdges` already produces, so one formatter serves both blocks.

**Terminal lines.** `Sync complete.` (external), `Checkout sync complete.` (fresh checkout run),
and `Checkout sync completed.` (checkout `--continue`) are unchanged in every run, new-mode
included. These are three distinct strings and MUST NOT be normalised.

**Error text.** All error strings are exactly as tabulated in §3.5, §8.7, §9, and §10.5.

### 3.8 Completion

`syncCmd.ValidArgsFunction` is unchanged. Two flag completions are registered immediately after
flag registration:

```go
_ = cmd.RegisterFlagCompletionFunc("only", syncEntryCompletion)
_ = cmd.RegisterFlagCompletionFunc("from", syncEntryCompletion)
```

`syncEntryCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective)`:

1. If `len(args) == 0` → return `nil, cobra.ShellCompDirectiveNoFileComp`. It MUST NOT error and
   MUST NOT print anything: the feature argument may legitimately not be typed yet.
2. Resolve the workspace and the feature path with the existing resolvable helpers —
   `internal.RequireWorkspace()` then `ws.ResolveFeaturePath(feature)` — and **degrade every error
   to "no candidates"**: on any error return `nil, cobra.ShellCompDirectiveNoFileComp`. It MUST NOT
   call `internal.RequireTool`, MUST NOT print, and MUST NOT exit. (There is no
   `internal.ResolveWorkspace`; these two helpers are the resolution path.)
3. Load `stack.yaml`; on error return `nil, cobra.ShellCompDirectiveNoFileComp`.
4. Return every `StackEntry.Name` where `!entry.Archived`, in `stack.yaml` file order, with
   `cobra.ShellCompDirectiveNoFileComp`.

Archived entries are omitted because selecting one is refused by I11.

### 3.9 Accepted help drift

Adding six flags changes `tws sync --help` and the usage block printed on an argument error. This
drift is **accepted and snapshot-tested** (AC 3), exactly as `stack-status` accepted `tws stack`
help drift. The six new flags appear **interleaved alphabetically** with the existing ones, because
`SortFlags` stays at its default `true` (§3.1); the snapshot records that alphabetical block, and
the source registration order of §3.1 is organization only and is not asserted through help output.
No other command's help changes. The committed
`internal/cli/testdata/existing_commands/**` goldens for `status`, `doctor`, and `list` MUST NOT be
re-baselined.

### 3.10 Writers and output capture

Sync output comes from bare `fmt.Print*` calls and from Git subprocesses wired directly to
`os.Stdout`/`os.Stderr` (`internal/exec.go:117,126,138`; `RunDirClean` writes its filtered stderr
through `fmt.Fprint*(os.Stderr, …)`). New output MUST use the same bare
`fmt.Print*` calls — introducing `cmd.OutOrStdout()` for some lines and not others would make the
stream interleaving untestable. Golden capture therefore swaps `os.Stdout`/`os.Stderr` for an
`os.Pipe` around the invocation, or execs the built binary (§17.1).

**Package `internal` gains exactly three new print paths, all reached only on new-mode / v2
checkout runs.** `internal/checkout_sync.go` prints nothing today; after this feature it contains
exactly these three, and no others:

| # | Output | Owning function | Call site |
|---|---|---|---|
| 1 | the §3.7 header line | unexported helper `printSyncModeHeader(p SyncRunPolicy)` in `internal/checkout_sync.go` | called from `internal.RunCheckoutSync` at §10.3 step 10, and from `internal.ContinueCheckoutSync` on a `state_version >= 2` resume (§3.7). One helper, two call sites, one print path |
| 2 | `Fetching default repo... ` plus `done`/`failed` | `internal.RunCheckoutSync` inline | the optional pre-plan `--fetch` refresh, §10.3 step 11 |
| 3 | the `local-only` no-op block — one `  [-] NAME (no in-stack parent edge to propagate)` line per selected anchor, plus the trailing `Nothing to propagate.` | unexported helper `printLocalOnlyNoOp(sel SyncSelection, plan []CheckoutPlanEntry)` in `internal/checkout_sync.go` | called by `internal.RunCheckoutSync` exactly once, immediately after `BuildCheckoutPlan` returns (§10.3, between steps 12 and 13) |

Rule 3 is binding and settles ownership so the implementation is determinate: **`RunCheckoutSync`
owns the checkout no-op print through `printLocalOnlyNoOp`; no formatter callback is introduced, and
`internal/cli/checkout_sync.go` MUST NOT print the block.** `printLocalOnlyNoOp` prints one `[-]`
line per selected anchor in topological order — anchors are excluded from the plan under
`local-only` (§10.3, §6.6), so the lines cannot come from the plan — and prints
`Nothing to propagate.` **only** when the resulting plan is empty, i.e. when nothing will be
rebased, matching §3.7 exactly. It prints nothing at all when the policy is not `local-only` or when
the selection contains no anchor, and it is unreachable on a no-flag run because a no-flag run has
no selection.

`Checkout sync complete.` / `Checkout sync completed.` keep their existing CLI owners
(`internal/cli/checkout_sync.go`) and are printed after the block. All three paths are guarded by
the new-mode / v2 condition, so no-flag checkout output is unchanged, and they are the only
`fmt.Print*` calls this feature adds to package `internal`.

---

## 4. No-flag compatibility contract

### 4.1 What is frozen

For every invocation that is **not** a new-mode run (§3.3), the following are frozen: stdout, stderr
(both as defined by rule 1), exit code, the **set** of files written under the feature directory and
under `<metadataRoot>/state/`, the file modes, and the Git commands issued.

The freeze is **not unconditional, and this document MUST NOT state it as if it were**. It holds for
every no-flag input **outside the four declared categories of observable no-flag change**, which are
enumerated in rules 3–7 below and listed together, with their justifications and acceptance
criteria, in §4.5:

1. **C1 — corrupt external state.** An **unreadable** `.sync-state.yaml` (rule 4): all three verbs
   change.
2. **C2 — decoupled-name push.** A no-flag `--push` (and `tws push`) over an entry whose logical
   `Name` and Git `Branch` are **decoupled** (rule 7): the `push` argv, the per-entry stdout line,
   the exit code, and the resulting remote ref move from broken to correct.
3. **C3 + C5 — duplicate-`GitBranch()` checkout metadata.** A no-flag checkout run over a stack
   containing two entries that share one `GitBranch()` (rule 7) writes the **correct** entry's
   `last_base_sha`, so its `stack.yaml` deliberately differs from the pre-change tree; and, on
   **every** checkout run, each persisted plan entry gains the additive `name:` key (rule 3).
4. **C4 — cwd resolution.** The two broken cwd cells (rule 5), plus the two closed read-only Git
   probe carve-outs of rule 6.

Everything outside those four categories is frozen. File **content** is frozen at the level defined
below, because two independently created state files can never be raw-byte equal:

1. **Output goldens are tws-owned bytes under exactly two closed, enumerated mechanisms.** A raw
   capture of a sync run is **not** portable: it embeds the per-run temporary fixture paths, and it
   embeds Git's own streamed prose, whose wording varies by Git version and platform. Claiming raw
   byte identity for such a capture would be false. Exactly two mechanisms are permitted, both
   closed and both enumerated here; **no** other rewriting of any kind — no regex, no prose
   substitution, no whitespace or ordering normalization, no "fuzzy" match — is allowed, and any
   remaining difference is a real regression.

   a. **One closed path→token substitution table.** Built with the shipped
      `goldenReplacements` / `goldenApplyReplacements` / `goldenAssertNoResidual` pattern
      (`internal/cli/stack_status_test.go:421-492`), reused verbatim, not re-invented. The table is
      **enumerated, never discovered**, and contains exactly these sources:

      | Source path | Token |
      |---|---|
      | `ws.RepoRoot` and the fixture's repository root | `<REPO>` |
      | each additional repository of a multi-repo fixture, in fixture declaration order | `<REPO2>`, `<REPO3>`, … |
      | `ws.MetadataRoot` and the fixture's metadata root | `<META>` |
      | the resolved feature path (`ws.ResolveFeaturePath(feature)`) | `<FEATURE>` |
      | the worktrees root | `<WORKTREES>` |
      | each bare remote directory, in fixture declaration order | `<REMOTE>`, `<REMOTE2>`, … |
      | the fixture root | `<ROOT>` |
      | `HOME`, `XDG_DATA_HOME` | `<HOME>`, `<XDG_DATA>` |

      For **every** entry, the `filepath.EvalSymlinks` form of the path is added under the **same**
      token, so macOS `/var` → `/private/var` cannot leak. Substitution is literal
      `strings.ReplaceAll`, applied **longest `from` first** (ties broken by lexical order), so a
      shorter enclosing root can never corrupt a longer nested path. The workspace stable ID is
      replaced by literal match on its value, exactly as the shipped helper does; a
      `[0-9a-f]{16}`-style regex is forbidden because it would also rewrite abbreviated commit
      SHAs, which the date-pinned fixtures exist to compare verbatim. After substitution,
      `goldenAssertNoResidual` MUST fail the capture when **any** temporary root
      (`os.TempDir()` and its `EvalSymlinks` form) or the stable ID still appears anywhere in it.
   b. **Git-owned streamed prose is moved out of the compared stream, not rewritten — and only
      where tws neither classifies nor persists those bytes as data.** A test-only `git` PATH
      wrapper (§17.1), installed
      **only** around the measured sync invocation and removed immediately after, delegates to the
      real Git binary and records one argv/exit record per invocation. Diversion is **conditional
      and per-capture**, and the condition is binding:

      - **External-mode captures — divert.** For the streamed **mutating** verbs `rebase`, `fetch`,
        and `push` **only**, the child's stdout and stderr go to a sidecar file while the real exit
        status is preserved byte for byte in semantics. What external mode does with those bytes,
        stated exactly:
        - **`rebase` and `push` run through `internal.RunDirClean`**
          (`internal/cli/sync_helpers.go:54,232`, `internal/cli/push.go:69`), which inherits stdout
          and **does read the child's stderr line by line** through its filter
          (`runWithFilteredStderr`, `internal/exec.go:138-186`): it drops lines beginning `hint:` or
          `Disable this message`, rewrites any line containing `skipped previously applied commit`
          to the tws-owned literal `    (skipped duplicate commit)`, and forwards every other line
          to `os.Stderr` verbatim.
        - **`fetch` never reaches that filter.** The non-verbose path — the one every frozen no-flag
          golden takes — uses the silent helpers `internal.RunSilentDir` / `internal.RunSilent`
          (`internal/cli/sync.go:215-219`, `internal/exec.go:188-197`), which wire no stream at all,
          so the child's output is discarded and only `done`/`failed` is printed; the `--verbose`
          path uses `internal.RunDir` / `internal.Run` (`internal/exec.go:117,126`), which inherit
          the streams unfiltered.

        Diversion is safe because the only consumer of those bytes is `RunDirClean`'s
        **presentation** filter: **nothing in external mode classifies, branches on, or persists
        them as data**. Only the exit status drives `done`/`failed`, the `[+]`/`[!]` lines, state
        transitions, and the exit code, all of which the wrapper preserves exactly. Its one effect
        on tws-owned output is that `RunDirClean` observes an **empty** stderr stream and therefore
        emits none of its filtered or reformatted lines. The consequence is stated rather than
        hidden: **no no-flag golden pins the tws-owned `    (skipped duplicate commit)` reformat**,
        so that line is covered by a direct regression assertion on `RunDirClean` taken **outside**
        the wrapper (§17.1 "Consequences, stated exactly", AC 2).
      - **Checkout-mode captures — record only, never divert.** The wrapper records argv and exit
        status and passes the child's stdout and stderr through to the caller **unchanged, for
        every verb including `rebase`, `fetch`, and `push`**. `internal/checkout_sync.go` runs Git
        through `cmd.CombinedOutput()` / `cmd.Output()`
        (`internal/checkout_sync.go:328-364,416-433`) and **classifies and persists** those bytes:
        `gitRebaseOnto` / `gitPlainRebase` match `CONFLICT` / `could not apply` in the combined
        output to raise `*RebaseConflictError`, whose `Error()` (`rebase conflict: ` + output) is
        written to the transaction's `failure_msg` with `failure_kind: conflict` and stage
        `conflict`. Diverting those bytes would empty `CombinedOutput`, silently reclassify a
        conflict as `failure_kind: switch`, and change the returned error, the exit path, and the
        persisted state — i.e. it would alter behaviour, not merely relocate output.

      Every other Git verb, in every capture — in particular the read-only `rev-parse`,
      `merge-base`, `for-each-ref`, `status`, `symbolic-ref`, and `check-ref-format` calls whose
      **stdout tws itself parses** — passes through completely untouched, in **both** wrapper
      modes. Three of those read-only calls are additionally **tee-recorded, identically in both
      modes** — the checkout containment probe `rev-parse --show-toplevel` and the two
      `DefaultBranchIn` reads `rev-parse --abbrev-ref origin/HEAD` and `symbolic-ref --short HEAD`,
      whose resolved values the C4 carve-outs of §17.1 comparison mode 3 assert. For those three,
      and only those three, the wrapper runs real Git with stdout captured to a temporary file,
      **replays the captured bytes verbatim** to its own stdout so tws parses exactly what real Git
      wrote, records the same bytes in the argv sidecar, leaves stderr inherited, preserves the real
      exit status, and removes the temporary file. That is a tee, not a diversion: it is
      observationally inert, it is never mode-dependent, and it is required in divert mode as well —
      the three **mutating** verbs `rebase`, `fetch`, and `push` remain the only diverted commands,
      and they are diverted in external mode only. Git prose is therefore
      never pinned and never edited; where it is diverted it is asserted separately, as command and
      exit-status semantics, from the sidecar (§17.1).

      **Checkout output goldens are unaffected by this asymmetry.** Because checkout never wires a
      Git child to `os.Stdout`/`os.Stderr`, no Git prose reaches the captured process streams on a
      checkout run at all: the checkout output goldens contain tws-owned bytes with or without
      diversion. The Git-version-dependent bytes checkout does keep land in **persisted state**
      (`failure_msg`), and are governed by the conditional dynamic-state rule of rule 2 below —
      not by any stream rewriting.

   Everything left in the compared stream is tws-owned output. It is compared **byte for byte**
   after mechanism (a) and nothing else, together with the exit code.
2. **State files are compared semantically against an exact closed set of dynamic fields**
   (§17.1). The YAML key set, key order, file mode, and every value outside that closed set MUST be
   identical to the pre-change tree, on every input outside the declared C2/C3 defect fixtures of
   rule 7 (whose `stack.yaml` difference is a declared change, not a comparison). The closed set is,
   exhaustively:
   - `.sync-state.yaml`: `started_at`;
   - `<feature>-checkout-sync.yaml`: `started_at`, `lock_pid`, `lock_created`;
   - `<feature>-checkout-sync.yaml`, **conditionally**: `failure_msg` — and only in the one case
     defined immediately below;
   - (new-mode only, no frozen counterpart) `.sync-state.v2.yaml`: `started_at`, `updated_at`,
     `marker`, `owner_token`; `.sync-run.lock`: `pid`, `created`, `token`.
   Each dynamic field is still asserted for **presence and shape** (RFC3339 UTC string, positive
   integer PID, 32-character lower-case hex token), so a wrong type or a dropped field is a
   failure. Any key not in this list, any new key, any removed key, and any reordering is a
   regression.

   **The one conditional dynamic-state rule — checkout conflict `failure_msg`.** Checkout keeps
   Git's rebase prose in persisted state (rule 1b), so a conflict transaction's `failure_msg`
   embeds Git-version- and platform-dependent bytes and cannot be pinned. This rule applies **only**
   to a checkout transaction reference whose `failure_kind` is `conflict`, and it is the only
   conditional entry in the closed set. For such a reference the comparator MUST assert, in this
   order:
   - `failure_kind` equals exactly `conflict` (pinned, never normalized);
   - `stage` equals exactly `conflict` (pinned, never normalized);
   - `failure_msg` is present and its value **starts with the exact prefix** `rebase conflict: `
     (`(*RebaseConflictError).Error()`, `internal/checkout_sync.go:440-442`), and the remaining
     suffix is **non-empty** — an empty suffix means the conflict bytes were lost and is a failure,
     not a normalization;
   then, and only then, it replaces that suffix with the token `<GIT-CONFLICT-OUTPUT>` in **both**
   documents before the value comparison, so the prefix is still compared literally.
   Nothing else about the value is rewritten and no other file or field is touched by this rule.

   **What the comparator does *not* claim about the other `failure_kind`s.** The comparator only
   ever sees the state references AC 1 captures, and that set is closed and small: the external
   `.sync-state.yaml` of the captures that leave one — a file that has **no** `failure_msg` field at
   all — plus the checkout transaction files the checkout captures leave behind. A checkout
   transaction survives a run only where the run stops mid-flight, so the **only** transaction
   reference in the frozen set is the **conflict** capture's, whose `failure_kind` is `conflict` and
   which the rule above handles: the clean run and the `--continue` resume delete the transaction on
   success (`internal/checkout_sync.go:1016`) and `--abort` deletes it as well
   (`internal/checkout_sync.go:618`). Therefore:

   - The frozen state-reference set contains **no** `switch`, **no** `validation`, **no**
     `restoration`, **no** `persistence` (`stack.yaml`-save), and **no** push-failure transaction,
     and likewise no `ancestry` and no `interruption` transaction. Their `failure_msg` values are
     simply **outside the comparator**: they are not pinned, not normalized, not asserted, and not
     reproduced by AC 1/AC 2, because no reference document containing one exists.
   - This specification makes **no** version-independence claim about them, and MUST NOT. Several of
     them embed foreign bytes by construction: `switch`, `restoration`, `persistence`, and
     push-failure interpolate a Git command's own error text
     (`internal/checkout_sync.go:823,952,982,992`), and `validation` interpolates the user's
     `--test` command output (`internal/checkout_sync.go:742-743,767-768`). Only `ancestry` is a
     tws-owned format string (`%s not descended from %s`,
     `internal/checkout_sync.go:724-725,847-848`), and `interruption` sets `failure_kind` without
     ever writing `failure_msg` — but neither is in the frozen set either, so neither is asserted.
   - The comparator's default rule stands as written — a `failure_msg` in a document whose
     `failure_kind` is not `conflict` is compared byte for byte (§17.1) — because that is the
     comparator's **behaviour**, not a claim about any string it will actually meet. It is reachable
     only if a future capture deliberately adds such a reference.
   - Adding one is a **deliberate** act: the new reference MUST be added to the frozen set together
     with its own explicitly justified comparison rule. Broadening AC 1 is **not** required by this
     feature and MUST NOT be done to satisfy this paragraph.
3. **One declared additive exception**: every checkout plan entry gains a `name:` key, in no-flag
   transactions too (C5, §4.5). No-flag transaction state is therefore **semantically equivalent but
   not byte-identical** in this one field. Comparisons remove exactly the additive `name` key from
   each plan entry and nothing else, and then require the remaining key set, key order, and values
   to be identical to the pre-change tree; older binaries decode the transaction and ignore the
   unknown key.
4. **Declared behavioural exception 1 — unreadable legacy external state**: an **unreadable**
   legacy `.sync-state.yaml` (external cell 10, §8.6) is **outside** this freeze. Rules 1–3 above
   apply to every no-flag invocation
   whose legacy state file is absent or decodable; when the file exists and `LoadSyncState` fails,
   all three verbs change, deliberately and by declaration, under C1 (§4.5, §8.6 row 10, §8.7).
   The change is stated in full there and is covered by AC 40; it MUST NOT be described anywhere as
   frozen, and the AC 1 goldens MUST NOT contain a corrupt-state capture as if it were.
5. **Declared behavioural exception 2 — the two C4 cwd cells (5, with its nested form 6, and 9)**:
   an invocation whose **cwd** falls in matrix cell 5, 6, or 9 (§12.11) is **outside** this freeze,
   in no-flag runs
   too, because in exactly those cells today's behaviour is provably wrong and C4 fixes it
   (§13.4, §10.9). Precisely, and exhaustively:
   - **Cell 5 (external, cwd = the feature directory) and its nested form, cell 6 (a nested
     `docs/`/`inject/` subdirectory of it)** — *only where the two path derivations disagree*.
     Today `syncFeature` re-derives the feature path through `internal.FeaturePath(feature)`
     (`internal/cli/sync.go:173-174`) instead of using the path `ws.ResolveFeaturePath` already
     resolved; when the two disagree (`TWS_ROOT` / workspace-detection edge cases) the run loads
     **no** stack and falls into `syncFallback`'s hard-coded `origin/main` rebase over every
     worktree, with its `internal.Must`/`os.Exit(1)` failure mode. After C4 the run **loads the
     resolved feature's `stack.yaml` and performs the ordinary stack sync**: different stdout,
     different Git commands, different refs. Where the two derivations agree — every healthy
     single-workspace layout, and every fixture behind the AC 1 goldens — nothing changes at all.
   - **Cell 9 (checkout, cwd = a linked worktree of the checkout repository)** — today the run
     takes `os.Getwd()` as `RepoDir` and silently mutates the **wrong** working tree. After C4 it is
     **refused** with the exact I19 message and exit 1, before any lock, transaction, or Git
     mutation.

   There is **no** "checkout run from outside any repository" cell, and this specification claims
   no behaviour change for one. Checkout mode is only ever dispatched *after*
   `internal.RequireWorkspace()` has returned a workspace whose `Mode` is `ModeCheckout`
   (`internal/cli/sync.go:30-36`), and from a cwd outside any repository `RequireWorkspace` either
   resolves an **external** workspace through `DetectWorkspaceRoot` or fails with `not inside a git
   repository or tws workspace` — in both cases before the checkout branch is taken. The `err != nil`
   arm of the §10.9 containment probe therefore remains as **defence in depth** only; it is
   unreachable through the shipped CLI dispatch and carries no shipped-behaviour-change claim, no
   matrix cell, and no declared-change evidence directory.

   These two cells are covered by AC 46, are declared in §4.5 (C4) and in the changelog (§14), and
   MUST NOT be described anywhere as frozen. No AC 1 golden may be captured from cell 5, 6, or 9:
   every frozen capture is taken from a supported, agreeing cwd (cells 1–4, 7–8).
6. **Declared read-only Git argv changes — the two C4 probe carve-outs, and nothing else.** C4 is a
   cwd *resolution* fix, and resolving cwd correctly means asking Git where it is. Rules 1–5 freeze
   observable behaviour; this rule states the **exactly two read-only argv carve-outs** in which an
   otherwise frozen no-flag run — in a **frozen** cwd cell, on an input outside the declared C2/C3
   defect fixtures of rule 7, producing frozen stdout, stderr, exit code, files, and modes — may
   differ from the pre-change tree. Both are read-only: neither writes an object, a ref, an index, a
   working tree, or a file; neither changes stdout, stderr, the exit code, the set of files written,
   or any file mode. There is no third. This rule does **not** touch the mutating verbs (`fetch`,
   `push`, `rebase`, `checkout`) at all: their one declared no-flag change is C2's push ref on a
   decoupled-name fixture (rule 7), and on every input outside the rule-7 fixtures no mutating verb
   differs.

   a. **Checkout containment probe — one *added* invocation.** Every checkout run, no-flag
      included, issues exactly one additional `git -C <cwd> rev-parse --show-toplevel`
      (`internal.GitRepoRootIn(cwd)`, §10.9) **before the operation** — before the lock, before the
      transaction, and before any Git mutation. Its position in the argv log is **not** "before
      every pre-change record": it runs *after* `RequireWorkspace`/`RequireFeaturePath` have already
      completed, so every Git record those helpers emit (`rev-parse --git-common-dir` and any other
      workspace-resolution read) still precedes it, unchanged, on both sides. The probe sits
      **immediately before the first `RunCheckoutSync` preflight record** — the
      `rev-parse --git-path rebase-merge` of `gitOperationInProgress` (§10.3 step 2) — and the
      remaining pre-change checkout-sync records follow it in their original order. In frozen
      cells 7–8 its output equals `ws.RepoRoot`,
      the run then proceeds exactly as today, and every other record in the capture is identical in
      argv, order, and exit status. In cell 9 it is the evidence for the rule-5 refusal.
   b. **External default-base probe — one *replaced logical event*, the whole `DefaultBranchIn`
      resolution.** Where `resolveBase` has a repo context (`entry.Repo`, else the entry's
      materialized worktree path; §13.4 rule 3), default-branch resolution stops asking whatever
      repository the process cwd happens to be in and asks that repo context instead:
      `internal.DefaultBranch()` becomes `internal.DefaultBranchIn(repoCtx)`
      (`internal/exec.go:67-90`). The carve-out unit is therefore **not a single argv record** but
      the **complete `DefaultBranchIn` logical event**, which is closed and has exactly these three
      shapes on either side:

      - one **successful** `rev-parse --abbrev-ref origin/HEAD` (`-C`-scoped after the change); or
      - a **failed** `rev-parse --abbrev-ref origin/HEAD` followed by `symbolic-ref --short HEAD`
        (likewise `-C`-scoped after the change); or
      - **both** of those failing, followed by the hard-coded `main` fallback, which issues no
        further Git command at all.

      The pre-change and post-change events **may legitimately differ in command count and in
      exit-status class**, and this specification claims otherwise nowhere. The pre-change call runs
      in the process cwd — which for an external workspace is commonly the workspace root or a
      feature directory, i.e. *not* the entry's repository — while the post-change call runs in the
      materialized repository context. A pre-change `rev-parse` may therefore fail where the
      post-change one succeeds, the `symbolic-ref` arm may be reached on one side and not the other,
      and the `main` fallback may be taken on one side and not the other. Claiming the same
      invocation count, the same exit-status class, or a difference of only an added `-C <path>`
      prefix would be **false**, and MUST NOT be asserted anywhere.

      What the comparator validates instead, for a **frozen** fixture, is the pair of properties
      that actually matter:

      - **Same resolved value.** The **fixture-pinned resolved default branch** produced by the
        pre-change event and by the post-change event MUST be the **same** string. The value is a
        fixture constant, so this is an equality assertion against a pinned expectation on both
        sides, not an inference from argv.
      - **Closed event containment.** Every command in the compared window MUST belong to exactly
        this closed event — one of the three shapes above, in that order, with those verbs, flags,
        and ref operands and no others. No unrelated argv may be hidden inside the carve-out; a
        `fetch`, `push`, `rebase`, `checkout`, `merge-base`, `for-each-ref`, `status`, or
        `check-ref-format` record inside the window fails the comparison.

      Fixtures in which the two sides are **declared to disagree** on the resolved default branch —
      the multi-repo and wrong-cwd cases C4 exists to fix — are **not** frozen and are **not**
      required to be equivalent: they are C4 declared-change evidence, stored and reviewed as such
      (AC 46, AC 53). Where no repo context is available the call is byte-for-byte today's argv
      (§13.4 rule 3) and no carve-out is applied at all.

   Every other Git invocation of a no-flag run — every verb, every flag, every operand, in the same
   order, with the same exit-status class — MUST be identical to the pre-change tree on every input
   **outside** the declared C2/C3 defect fixtures of rule 7. AC 2 and §17.1 implement this as a
   **closed** argv comparison carve-out; any argv difference outside (a), (b), and rule 7 is a
   regression, not a declared change. These probes are declared as part of C4 in §4.5, in the
   changelog, and in the docs (§14).
7. **Declared behavioural exception 3 — the two defect fixtures of C2 and C3.** C2 and C3 fix two
   metadata-identity defects in code the no-flag path also executes. Both are inert on ordinary
   inputs and both are observable on exactly one fixture shape each, which is declared here,
   captured as declared-change evidence rather than as a frozen golden, and asserted as a
   before/after **diff** (AC 33, AC 34):

   - **C2 — decoupled `Name` / `Branch`, `--push` only.** A no-flag `tws sync <feature> --push` (and
     `tws push <feature>`) over an entry whose `Branch` differs from its `Name` changes, by
     declaration: the `push` **argv** (`git push --force-with-lease origin <gitbranch>` instead of
     `… origin <name>`), the per-entry **stdout** line (`  [+] NAME (pushed)` where today's run
     prints `  [x] NAME (push failed)` because the ref it names does not exist), the process **exit
     code** where that failure was the only one, and the **remote ref** actually updated
     (`refs/heads/user/work` instead of a stray `refs/heads/work`, which is no longer created). This
     is a broken→correct change, not a regression. Fixtures whose names are **coupled**
     (`GitBranch() == Name`) are unaffected and remain fully frozen, argv included — which is every
     AC 1 golden fixture.
   - **C3 — duplicate `GitBranch()`, checkout finalization.** A no-flag checkout sync over a stack
     in which two logical entries share one `GitBranch()` updates the **correct** entry's
     `last_base_sha` instead of the first `GitBranch()` match, so the post-change `stack.yaml`
     **deliberately differs** from the pre-change tree on that fixture. Nothing else about the run
     changes: same Git argv, same stdout, same exit code, same file set and modes. Fixtures whose
     `GitBranch()` values are **unique** are unaffected and remain fully frozen, `stack.yaml`
     included — which is every AC 1 golden fixture. C3 depends on C5 persisting the plan's `name`
     key on the no-flag path (rule 3), which is already declared and is what makes the attribution
     possible at all.

   Consequently the **mutating-argv equality** of rule 6 and the **persisted-byte freeze** of
   rules 2–3 are scoped to inputs **outside** these two declared defect fixtures. Pre-change
   evidence for them is captured in the AC 1 pre-change run into
   `internal/cli/testdata/sync_noflag/declared_c2/` and `…/declared_c3/` and is labelled
   **declared-change evidence, not a golden**, exactly as C1 and C4 evidence is. No frozen golden
   and no frozen state reference may be captured from a decoupled-name or duplicate-`GitBranch()`
   fixture.

Pre-change goldens (AC 1) are captured against the unmodified tree **before any production edit**
and are pre-change evidence; a regeneration that alters a committed output golden, or that changes
any pinned field or ordering of a state file, **is** the regression. This document MUST NOT claim
raw byte identity for state files anywhere, MUST NOT claim raw byte identity for a captured
stream before the closed normalization of rule 1, and MUST NOT claim that the freeze holds on the
four declared categories enumerated above.

### 4.2 External mode, no flags — pinned behaviour

1. `RequireTool("git")` → `os.Exit(1)` when git is missing (`internal/exec.go:11-16`). Unchanged.
2. `RequireWorkspace()` including the external fallback via `DetectWorkspaceRoot` +
   `inferExternalRepoRoot` (`internal/workspace.go:440-470`). Unchanged.
3. `GuardFeatureName` then `ws.ResolveFeaturePath`. Unchanged.
4. `--abort`: **absent** state → `Nothing to abort — no sync in progress.`, exit 0. Present,
   **decodable** state → abort a rebase in `internal.WorktreePath(feature, state.FailedBranch)` if
   one is in progress, delete `.sync-state.yaml`, print `Sync state cleared.`, exit 0. **No branch
   rollback** (M10). Both are unchanged. Present but **unreadable** state is the declared C1
   change: it no longer takes the "absent" arm — see C1, §4.1 rule 4, §8.6 row 10, §8.7, AC 40.
5. `--continue`: refusal while a rebase is still in progress; `load stack: %w` on stack failure;
   `failed branch %q no longer exists in stack`;
   `resolved branch %s still does not contain its configured parent %s`;
   `  [~] NAME (active)`; `Resuming sync with %d pending branch(es)` using `len(state.Pending)`;
   whole-stack `TopoSort`; `syncWithStackFiltered` with `done`. `--push` is taken from the current
   invocation and is not persisted. All unchanged for absent and decodable state; an **unreadable**
   `.sync-state.yaml` stops producing today's opaque `nothing to continue — no sync in progress`
   and produces the cell-10 message instead (C1). This is the **trigger-free** `--continue`; it is
   the only external `--continue` that reaches this path against absent or real-legacy state,
   because one carrying a trigger flag is refused first by I20 (§3.5) and is therefore not a
   no-flag invocation at all.
6. Plain run with existing **decodable** state → `previous sync incomplete (failed on: %s); use --continue or --abort`.
   Unchanged for real legacy state (§8.6 row 7). With **unreadable** state today's nil-pointer panic
   is replaced by the cell-10 error (C1).
7. **Fallback without `stack.yaml`**: `fetchQuiet("", "", verbose)` then `syncFallback`, which
   rebases every worktree directory onto the hard-coded literal `origin/main` through
   `internal.Must` (`os.Exit(1)` on failure) and returns `Complete: true` unconditionally.
   The fallback **mechanics** — the fetch, the hard-coded ref, the `internal.Must`/`os.Exit`, and
   the unconditional `Complete: true` — are unchanged, but they are frozen **only for the input
   they are meant for**: a run whose *resolved* feature genuinely has no readable `stack.yaml`.
   What is **not** frozen is *reaching* this path because `syncFeature` re-derived a different
   feature path than the one already resolved: that mis-routing is the C4 defect, is fixed by
   §13.4 rule 2, and is the declared cwd-cell-5/6 exception of §4.1 rule 5 (AC 46). A feature that
   does have a `stack.yaml` at its resolved path therefore stops falling into the fallback, in
   no-flag runs too. Only new-mode runs refuse the fallback path outright (I9, D10).
8. **Fetch**: once per unique repo from `internal.UniqueRepos(stack, featurePath)`; `Fetching
   <label>... ` + `done`/`failed`, or `Fetching <label>...` + streamed output under `--verbose`.
   **Fetch failure remains non-fatal** and the run proceeds against existing remote-tracking refs
   (§6.4). The map-iteration order of `Fetching …` lines with multiple repos remains
   non-deterministic and MUST NOT be pinned by any golden.
9. **Validation source**: `internal.LoadConfig().TestCommand` split by `strings.Fields`, run
   silently in the worktree. `--test` remains **inert** in external mode (D16, M8).
10. **Rebase flags**: `git rebase --update-refs <base>` or
    `git rebase --update-refs --onto <base> <LastBaseSHA>`; archived (non-materialized) pass uses
    `git rebase <base> <gitbranch>` without `--update-refs`; `markUpdatedAncestors` still short
    circuits ancestors moved by `--update-refs`. Unchanged.
11. **Completion gate**: unfiltered `staleStackEdges(feature, stack)`;
    `Sync incomplete; stale stack edges remain:` + `  [!] <edge>`; state persisted with an **empty**
    `FailedBranch`; exit 1. On success `.sync-state.yaml` is deleted and `Sync complete.` printed.
    Unchanged.
12. **Push**: `\nPushing...` then `pushFeature(feature, false)` over **every** stack entry, skipping
    entries with no worktree directory. The ref used changes from `entry.Name` to
    `entry.GitBranch()` (D13, M14). For **coupled** names this is inert — same argv, same output,
    same refs. For a **decoupled** entry it is the declared C2 change of §4.1 rule 7: the `push`
    argv, the per-entry line, the exit code, and the updated remote ref all change from broken to
    correct, on the no-flag path too — see §4.5 (C2) and AC 33.
13. **State**: `SyncState{StartedAt, FailedBranch, Pending, Completed, Skipped}` at
    `<featurePath>/.sync-state.yaml`, mode `0644`, written **only on failure**
    (`saveIncompleteSync`), never before the run. `Skipped` stays declared and never written. No
    lock is taken and none is checked. Unchanged except that the write becomes atomic and its
    readers gain explicit decode-error branches, whose one observable consequence is the declared
    cell-10 change of items 4–6 (C1, §4.5).

### 4.3 Checkout mode, no flags — pinned behaviour

1. Dispatch to `runCheckoutSync` before the external guard/resolution block. Unchanged. `RepoDir`
   becomes `ws.RepoRoot` with the I19 containment refusal (§10.9) on **every** checkout run,
   no-flag included: for the supported cwds (cells 7–8) the resolved toplevel is `ws.RepoRoot` and
   nothing changes except the one added read-only `git -C <cwd> rev-parse --show-toplevel` probe
   declared in §4.1 rule 6a — which runs after workspace and feature-path resolution and
   immediately before the first `RunCheckoutSync` preflight Git call — and cell 9 is the declared
   exception of §4.1 rule 5 (AC 46).
2. `RequireFeaturePath` legacy/new layout resolution. Unchanged.
3. Pre-flight order and exact messages: existing transaction →
   `previous checkout-sync incomplete; use --continue or --abort` (CLI) /
   `checkout sync transaction already exists; use --continue or --abort` (library);
   `another Git operation is in progress; complete or abort it before checkout sync`;
   `working tree is dirty; commit or stash changes before checkout sync`;
   `cannot sync from detached HEAD: %w`. Unchanged.
4. `AcquireCheckoutLock` semantics, including refusal of a live lock and refusal of a stale lock
   that still has a transaction. Unchanged, and it remains the **first side effect** of a checkout
   run. For a **new-mode** run the read-only preflight of §10.3 steps 6–8 (I9, I10–I13, I14) is
   inserted between item 3 and this item; on a **no-flag** run those steps do not exist and the
   order below is exactly today's.
5. **No fetch.** Checkout remains a no-fetch command by default (M1).
6. `BuildCheckoutPlan`: `TopoSort`, skip `entry.Archived`, skip empty `Base`, resolve `entry.Base`
   **literally** with `git rev-parse`, resolve `entry.GitBranch()`, record
   `{Branch, Base, LastBaseSHA, NewBaseSHA, PreSHA}`; an unresolvable base aborts the plan before
   any mutation; `StackEntry.Repo` is not consulted. Unchanged for no-flag runs (M3, M6) **except
   that every newly written plan entry also records `Name`** (C5, §4.5): the plan's Git behaviour is
   identical, the persisted entry gains one additive key.
7. Empty plan → release lock, return nil, caller prints `Checkout sync complete.` Unchanged.
8. Transaction persisted before the first `git checkout`; stage sequence
   `planned → switched → rebasing → (conflict) → rebased → validating → completed → restoring`;
   `StepHook` call sites unchanged.
9. `doRebase`: `git rebase --no-fork-point --onto <NewBaseSHA> <LastBaseSHA>` when the recorded base
   moved, else `git rebase --no-fork-point <Base>`; never `--update-refs` (M7). Unchanged, including
   that the rebase runs through `cmd.CombinedOutput()` and that those bytes — not the exit status
   alone — drive `*RebaseConflictError` classification and the persisted `failure_msg`. No harness
   may divert them (§4.1 rule 1b, §17.1).
10. Validation: `opts.TestCommand` through `sh -c` in `RepoDir` (M8). Unchanged.
11. Finalization: `LastBaseSHA` update, `SaveStack`, per-entry final ancestry re-verification,
    `StageRestoring`, `restoreOriginal`, push with `--force-with-lease` using `pe.Branch`.
    Unchanged except for the `Name`-keyed attribution fix, which applies to no-flag runs too and
    depends on the plan's additive `name` key (C3 + C5, §4.5). For a stack whose `GitBranch()`
    values are **unique** the fix is inert and `stack.yaml` is unchanged; for a stack containing
    **duplicate** `GitBranch()` values it is the declared C3 change of §4.1 rule 7 — the correct
    entry's `last_base_sha` is written and that fixture's `stack.yaml` deliberately differs from the
    pre-change tree (AC 34). Attribution falls back to today's
    `GitBranch()` first match when `pe.Name == ""`, i.e. for a transaction written by an older
    binary (§10.6).
12. `--continue`: `forceAcquireCheckoutLock`, the one-way push rule
    `cannot add --push to an existing transaction that was started without it; persisted push=%v wins`,
    then `opts.Push`/`opts.TestCommand` overwritten from the transaction; stage-dispatched resume;
    CLI prints `Checkout sync completed.` Unchanged for legacy (`state_version` absent)
    transactions (§10.5), and reachable against them only for a **trigger-free** `--continue`: one
    carrying a trigger flag is refused first by I20 (§3.5, §10.5 rule 0) and is not a no-flag
    invocation.
13. `--abort`: abort in-progress rebase, `restoreOriginal`, delete transaction, release lock, print
    `Checkout sync aborted, original branch restored.` Unchanged.
14. Persistence: `atomicWriteFile`, mode `0600`, at
    `<metadataRoot>/state/<feature>-checkout-sync.yaml`. Unchanged apart from the additive
    per-plan-entry `name:` key (C5, §4.1 rule 3).
15. **No dirty/detached guard is added to external mode** (M15). External keeps allowing dirty and
    detached worktrees, for no-flag *and* new-mode runs (§12 item 8).

### 4.4 The one declared added runtime-state-path read

On **every** external run — no-flag included — the state discrimination of step 8b (§3.6) performs
one additional `os.Lstat` of `<featurePath>/.sync-state.v2.yaml`, before any Git command. This is
the single, deliberate, declared addition to the no-flag **runtime-state read set**, and it is the
only added ordinary no-flag **runtime-state-path** read. It is deliberately **not** a claim that no
added read of any other kind exists: the two **read-only Git argv changes** of C4 — the checkout
containment probe and the repo-scoped default-base probe — are declared separately and exhaustively
in §4.1 rule 6, and this section neither covers nor contradicts them. (The declared **behavioural**
exceptions are the four categories of §4.1 — C1, rule 4; C2 and C3, rule 7; C4, rules 5 and 6 — and
the one unconditional persisted-byte exception is C5, §4.1 rule 3.) Its exact scope:

- When the payload is **absent**, nothing changes on an input outside those declared categories:
  same stdout, same stderr, same exit code, same
  files, and the same Git commands apart from the two declared read-only C4 argv carve-outs of §4.1
  rule 6 (and, on the declared C2/C3 defect fixtures only, the changes of §4.1 rule 7). The guard is
  not read — an unreadable guard beside such a run changes nothing, which is how AC 38 proves it —
  the legacy path is read exactly as today, and no
  symlink refusal is applied to the legacy path. Every frozen golden lives in this column.
- When the payload is **present**, plain and `--continue` refuse before Git with the messages of
  §8.7; the guard of step 8c is then read as well, and a symlink at the payload or guard path is
  refused (I18). Without this read, a plain run would broadly overrun a payload recording an
  unfinished scoped run, and the `{absent, valid}` and `{real legacy, valid}` cells would be
  invisible to the invocation most likely to be typed next.
- It is **unreachable** in any repository that has never run a new mode, and unreachable again once
  the residue is cleared.
- The unqualified claim "sentinel-absent means unaffected" survives only for **old binaries**,
  which never open the payload.

No other runtime-state read is added to the no-flag path, and no write or behaviour beyond the four
declared categories of §4.1 (C1, C2, C3+C5, C4) is added anywhere on it.

### 4.5 Declared changes that are not frozen

Five changes touch shared code that no-flag runs also execute. Each is declared here, justified,
and covered by an acceptance criterion. Together they produce **exactly four categories of
observable no-flag change**, each bounded to a declared input class — the same four enumerated in
§4.1, restated here in full:

1. **C1 — corrupt external state**, on exactly one input: an **unreadable** `.sync-state.yaml`.
   Declared behavioural exception 1 (§4.1 rule 4), spelled out per verb in §8.6 row 10 and §8.7,
   asserted by AC 40. All three verbs change: stdout/stderr, exit code, and what is deleted.
2. **C2 — decoupled-name push**, on exactly one input class: a `--push` run (and `tws push`) over an
   entry whose `Name` and `Branch` are **decoupled**. Declared behavioural exception 3 (§4.1
   rule 7), asserted by AC 33. The `push` **argv**, the per-entry **stdout** line, the **exit code**
   where that push was the only failure, and the **remote ref** updated all move from broken to
   correct. **Coupled-name fixtures remain fully frozen**, argv included.
3. **C3 + C5 — duplicate-`GitBranch()` metadata attribution, and the additive plan name.** On
   exactly one input class — a checkout run over a stack containing two entries that share one
   `GitBranch()` — finalization updates the **correct** entry's `last_base_sha` instead of the first
   match, so that fixture's `stack.yaml` **deliberately differs** from the pre-change tree (declared
   behavioural exception 3, §4.1 rule 7, AC 34). **Unique-branch fixtures remain fully frozen**,
   `stack.yaml` included. Independently of the fixture shape, **C5 persists the logical `name` key**
   on every checkout plan entry, including no-flag transactions — the already-declared additive
   persisted-byte change of §4.1 rule 3 (AC 6, AC 54).
4. **C4 — cwd resolution**, on exactly two cwd cells: external cell 5 (and its nested form,
   cell 6) where the two feature-path derivations disagree, and checkout cell 9. Declared
   behavioural exception 2 (§4.1 rule 5), spelled out in §13.4 and §10.9, asserted by AC 46.
   Separately, C4 carries the **two read-only Git argv carve-outs** of §4.1 rule 6 — the checkout
   containment probe and the repo-scoped `DefaultBranchIn` event — which are visible only in the
   argv log and never in stdout, stderr, exit code, files, or modes.

No other no-flag fixture moves: every no-flag capture outside those four declared categories, in
both modes, stays frozen under §4.1's comparison rules, including its mutating-verb argv and its
persisted bytes.

| # | Change | Why it is not a compatibility break | AC |
|---|---|---|---|
| C1 | `SaveSyncState` becomes atomic (temp + `Sync` + rename) and every reader of `.sync-state.yaml` gains an explicit decode-error branch, **keeping mode `0644` and an identical key set, key order, and values** (D15). **Declared behaviour change on unreadable legacy state (external cell 10, §8.6 row 10), on all three verbs**: plain stops panicking, `--continue` stops failing opaquely, and `--abort` **fails closed with exit 1 instead of silently treating corrupt state as absent** | A truncated `.sync-state.yaml` today makes the plain path dereference a nil `*SyncState` and panic (`internal/cli/sync.go:56-58`); `--continue` reports `nothing to continue — no sync in progress` although the file exists (`internal/cli/sync.go:102-105`); and `--abort` prints `Nothing to abort — no sync in progress.` with exit 0 while leaving the file and any real mid-rebase worktree untouched (`internal/cli/sync.go:85-89`). All three are the *same* defect — an ignored decode error — and they are **directly coupled** to this change: adding the decode-error branch is what removes the panic, and an abort that keeps calling corrupt state "no sync in progress" would keep the operator's only recovery verb lying about the file the hardening exists to protect. Silently deleting a file whose contents are unknown is refused by §8.6's fail-closed rule, so the honest outcome is a clean exit-1 error naming the file and deleting nothing. Atomicity itself changes only the crash window; the resulting file stays mode-identical and content-identical up to the per-run `started_at` timestamp, which is dynamic in the pre-change tree too (§4.1 rule 2) | AC 40 |
| C2 | `pushFeature` pushes `entry.GitBranch()` instead of `entry.Name` (D13, M14). **Declared observable no-flag change on exactly one input class** (§4.1 rule 7): for an entry whose `Name` and `Branch` are **decoupled**, a no-flag `tws sync <feature> --push` (and `tws push <feature>`) changes the `push` **argv** (`git push --force-with-lease origin <gitbranch>`), the per-entry **stdout** line (`  [+] NAME (pushed)` where today prints `  [x] NAME (push failed)`), the **exit code** where that push was the run's only failure, and the **remote ref** updated (`refs/heads/user/work`; the stray `refs/heads/work` is no longer created) | For coupled names `GitBranch() == Name`, so argv, output, exit code, and refs are identical and every coupled fixture stays frozen. For decoupled names today's code pushes a ref that is not the branch: the push fails, or worse creates a ref nobody uses, while the real branch is never published — a defect, and the change is broken→correct. Selected push cannot share the helper otherwise. Because the observable difference is declared, the decoupled fixture is captured as **declared-change evidence**, not as a frozen golden (§4.1 rule 7) | AC 33 |
| C3 | `finalizeTransaction` attributes `LastBaseSHA` by logical `Name` instead of first-match `GitBranch()` (M4). **Declared observable no-flag change on exactly one input class** (§4.1 rule 7): a no-flag checkout run over a stack with two entries sharing one `GitBranch()` updates the **correct** entry's `last_base_sha`, so that fixture's `stack.yaml` deliberately differs from the pre-change tree. Git argv, stdout, exit code, file set, and modes are unchanged even there | Identical for unique branches, so every unique-branch fixture stays frozen, `stack.yaml` included. With duplicate `GitBranch()` values today's `break`-on-first-match writes the wrong entry's `last_base_sha` — silent metadata corruption on the frozen path, and the change is broken→correct. Requires C5, because attribution by `Name` needs the plan to carry `Name`. The duplicate-branch fixture is captured as **declared-change evidence**, not as a frozen golden (§4.1 rule 7) | AC 34 |
| C4 | cwd resolution fixes: checkout `RepoDir`, external `syncFeature` feature-path re-derivation, and `resolveBase`'s **repo-scoped** default branch (D9 escape clause, §10.9, §13.4). **Declared observable no-flag change in exactly two cwd cells** (§4.1 rule 5): external cell 5/6 runs whose resolved and re-derived feature paths disagree now load the resolved feature's `stack.yaml` and sync the stack instead of falling into `syncFallback`'s hard-coded `origin/main` rebase; checkout cell 9 (a linked worktree of the checkout repository) now refuses with I19 and exit 1 instead of mutating the wrong working tree. No checkout-from-outside-any-repository change is claimed: `RequireWorkspace` resolves external mode or errors before checkout dispatch, so the probe's `err != nil` arm is defensive only. **Declared read-only Git argv change on otherwise frozen no-flag runs** (§4.1 rule 6), in exactly two carve-outs and no others: (a) every checkout run adds one containment probe `git -C <cwd> rev-parse --show-toplevel` after workspace/feature-path resolution and immediately before the first `RunCheckoutSync` preflight Git call; (b) whenever a materialized/repo context is available, external default-base resolution replaces today's cwd-scoped `internal.DefaultBranch()` with `internal.DefaultBranchIn(repoCtx)` — compared as the **whole closed `DefaultBranchIn` logical event** (successful `rev-parse --abbrev-ref origin/HEAD`; or failed `rev-parse` then `symbolic-ref --short HEAD`; or both failing then the hard-coded `main` fallback), whose command **count and exit-status classes may differ** between the two sides because the pre-change call runs in the process cwd and the post-change call runs in the repository | Each changes behaviour only where the current code is provably wrong: cells 5 and 9 of the §12.11 matrix fail today, which is D9's own escape clause. Supported cwds are untouched — cells 1–4 and 7–8 resolve to the same feature path and the same toplevel as today, so every frozen no-flag golden lives there. The two argv carve-outs are **read-only**: they write no object, ref, index, or file, and leave stdout, stderr, exit code, files, and modes untouched; asking Git where the cwd is, and asking the right repository for its default branch, is precisely the fix. Carve-out (b) is validated by **resolved value**, not by argv shape: for a frozen fixture the pre-change and post-change events MUST resolve to the same fixture-pinned default branch and every command in the window MUST belong to that one closed event; fixtures declared to **disagree** on the resolved value are C4 declared-change evidence, reviewed rather than required to be equivalent. The `resolveBase` change is deliberately narrow: a repo context is used **only when one exists** (`entry.Repo`, else the entry's materialized worktree path); when no repo context is available the call is exactly today's `internal.DefaultBranch()` with byte-for-byte today's argv, so healthy no-flag single-repo runs keep their default-base semantics unchanged (§13.4) | AC 2, AC 46, AC 53 |
| C5 | every **newly written** checkout plan entry records `name:` — in no-flag transactions too | This is the shared-path half of C3: without `name` on disk, `finalizeTransaction` cannot attribute by logical `Name`, and duplicate-`GitBranch()` metadata stays corrupt on exactly the frozen path where the defect bites. The key is additive and `omitempty`; every existing key keeps its value, type, and position; old binaries decode the transaction and silently drop the unknown key, and their `GitBranch()` first-match attribution keeps working. Declared consequence: a no-flag checkout transaction is **semantically equivalent but not byte-identical** to the pre-change tree in this one field | AC 6, AC 34, AC 54 |

Everything else on the no-flag path — every input outside the four declared categories above — is
byte-, exit-, and state-identical under §4.1's comparison rules and its declared exceptions (rule 3,
C5's additive plan key; rule 4, C1; rule 5, C4's two cwd cells; rule 6, C4's two read-only Git
argv carve-outs; rule 7, the C2 decoupled-name and C3 duplicate-`GitBranch()` defect fixtures).

---

## 5. Shared selector and plan model

One model, one resolution function, shared by both modes. There is **no** second topological sort,
**no** second descendant walker, **no** second parent lookup, and **no** second ancestry rule.

### 5.1 Types (new file `internal/sync_selection.go`, package `internal`)

```go
// SyncFetchPolicy is axis F.
type SyncFetchPolicy string

const (
    SyncFetchEnabled  SyncFetchPolicy = "fetch"
    SyncFetchDisabled SyncFetchPolicy = "no-fetch"
)

// SyncPropagationPolicy is axis P.
type SyncPropagationPolicy string

const (
    SyncPropagationFull      SyncPropagationPolicy = "full"
    SyncPropagationLocalOnly SyncPropagationPolicy = "local-only"
)

// SyncScopeKind is axis S.
type SyncScopeKind string

const (
    SyncScopeAll     SyncScopeKind = "all"
    SyncScopeOne     SyncScopeKind = "one"
    SyncScopeSubtree SyncScopeKind = "subtree"
)

// SyncRunPolicy is the frozen decision of one run. It is persisted verbatim.
type SyncRunPolicy struct {
    Fetch       SyncFetchPolicy       `yaml:"fetch_policy"`
    Propagation SyncPropagationPolicy `yaml:"propagation_policy"`
    ScopeKind   SyncScopeKind         `yaml:"scope_kind"`
    Selector    string                `yaml:"scope_selector,omitempty"` // logical StackEntry.Name
}

// SyncSelectionRole classifies one selected entry.
type SyncSelectionRole string

const (
    // SyncRoleAnchor: base is a literal ref, empty, or a stack entry in a
    // different repo. Never rebased under local-only.
    SyncRoleAnchor SyncSelectionRole = "anchor"
    // SyncRolePropagated: base names another stack entry in the same repo.
    SyncRolePropagated SyncSelectionRole = "propagated"
)

// SyncSelectedEntry is one resolved member of the selection, in topological order.
type SyncSelectedEntry struct {
    Name      string            // StackEntry.Name — the tws identity
    GitBranch string            // StackEntry.GitBranch() — the Git identity
    Repo      string            // StackEntry.Repo ("" = default repo)
    Base      string            // StackEntry.Base, verbatim
    Role      SyncSelectionRole
    ParentName string           // "" unless Role == SyncRolePropagated
    Archived  bool              // StackEntry.Archived (metadata flag)
}

// SyncSelection is the whole resolved plan-independent selection.
type SyncSelection struct {
    Policy   SyncRunPolicy
    Entries  []SyncSelectedEntry // topological order, filtered
    Names    map[string]bool     // membership set over Entries
    Repos    []string            // unique Repo values across Entries, sorted; "" allowed
}

// SyncSelectionOpts carries everything the validator needs that the policy does
// not. It is deliberately tiny and value-only: resolution stays pure.
type SyncSelectionOpts struct {
    Mode    WorkspaceMode // ModeExternal or ModeCheckout; selects the checkout-only rules
    NewMode bool          // any trigger flag was Changed; selects the new-mode-only rules
    Feature string        // resolved feature name, interpolated into the I10/I11 messages
}
```

`SameStackRepo(a, b string) bool` moves into this file as the **single** cross-repo predicate.
`internal/cli/new.go`'s `sameStackRepo` becomes a one-line delegation to it so exactly one
definition exists.

### 5.2 `ResolveSyncSelection` — the one resolution function

```go
func ResolveSyncSelection(stack Stack, policy SyncRunPolicy, opts SyncSelectionOpts) (SyncSelection, error)
```

**Validation ownership is total and unambiguous**: `ResolveSyncSelection` owns I10, I11, I12, and
I13 — every selection-validity rule — and no caller re-implements, re-checks, or supplements them.
`opts` carries the only three facts the rules need beyond `(stack, policy)`:

| Rule | Fires when | Owner |
|---|---|---|
| I10 unknown selector | `policy.ScopeKind != SyncScopeAll` and the selector is not in the stack | `ResolveSyncSelection`, always |
| I11 archived selector | `policy.ScopeKind != SyncScopeAll` and the **named** entry has `Archived: true` | `ResolveSyncSelection`, always |
| I12 cross-repo entry | `opts.Mode == ModeCheckout` and a selected entry has `!SameStackRepo(entry.Repo, "")` | `ResolveSyncSelection`, checkout mode only |
| I13 duplicate `GitBranch()` | `opts.NewMode` and two selected entries share one `GitBranch()` | `ResolveSyncSelection`, **every** new-mode caller, in **either** workspace mode |

**`opts.Feature` and the two messages that need it, binding.** I10 and I11 name the feature, so the
feature name is an **input to the owner**, never a reason for a caller to re-phrase or re-raise the
rule. `opts.Feature` is the **resolved feature name** — exactly the `<feature>` argument as accepted
by `GuardFeatureName` and used by `ws.ResolveFeaturePath`, never a path, never a directory basename
derived inside the function. The two messages are formatted **only** here, with these exact
argument orders:

- I10: `fmt.Errorf("unknown stack entry %q in feature %q; run: tws stack status %s", policy.Selector, opts.Feature, opts.Feature)`
- I11: `fmt.Errorf("stack entry %q is archived; restore it with: tws new %s %s", policy.Selector, opts.Feature, policy.Selector)`

**Every caller MUST pass the resolved feature name.** `opts.Feature` is REQUIRED whenever
`opts.NewMode` is true; it is not optional, not defaulted, and never derived by the function.
External passes the `<feature>` argument of `tws sync <feature>` (§3.6 step 11); checkout passes
`opts.Feature` from `CheckoutSyncOpts` (§10.1, §10.3 step 7). Unit tests pass it as a literal
(AC 50). `Feature` is a plain string value: it is read for message interpolation and for nothing
else, so carrying it changes nothing about purity — `ResolveSyncSelection` still performs no Git
command, no filesystem access, and no path resolution.

I12 needs no `RepoRoot`: checkout mode is single-repository by construction, so "the workspace repo"
is the default repo and the predicate reduces to `entry.Repo != ""` (expressed through
`SameStackRepo` so exactly one cross-repo predicate exists). In **external** mode a cross-repo entry
is never refused — it is an anchor (§5.6, §6.6).

Binding algorithm:

1. `sorted, err := TopoSort(stack)`. On error return it verbatim. **This is the only ordering
   truth**; the selection is a filter over `sorted` and never re-derives order.
2. Compute the member name set by scope:
   - `SyncScopeAll`: every `sorted` entry.
   - `SyncScopeOne`: `{Selector}`. If `!HasBranch(stack, Selector)` → I10 (formatted with
     `opts.Feature`, above).
   - `SyncScopeSubtree`: `{Selector} ∪ Descendants(stack, Selector)` (D2 — **the named entry is
     included**). If `!HasBranch(stack, Selector)` → I10. `Descendants`
     (`internal/stack.go:185-205`) is the only descendant-closure truth.
3. For a named selector (`one`/`subtree`), if `GetBranch(stack, Selector).Archived` → I11
   (formatted with `opts.Feature`, above).
   Closure members and `all` members are **not** filtered on `Archived` here (§5.5).
4. Build `Entries` by walking `sorted` in order and keeping members. Order is therefore
   parent-before-child by construction.
5. Classify each entry:
   - `parent := GetBranch(stack, entry.Base)`;
   - if `parent.Name != "" && SameStackRepo(parent.Repo, entry.Repo)` → `SyncRolePropagated`,
     `ParentName = parent.Name`;
   - otherwise → `SyncRoleAnchor` (covers literal refs, empty base, and a stack-entry base in a
     different repo).
   This is exactly the predicate `resolveEntryBase` already applies
   (`internal/cli/sync_helpers.go:179-184`) and exactly the combination the analysis requires:
   `stackBaseRef`'s `StackBaseLiteralRef`/`StackBaseNone` classification **or** `SameStackRepo`
   false. `stackBaseRef` alone is insufficient because it never consults `Repo`.
6. Cross-repo check: when `opts.Mode == ModeCheckout`, any entry in `Entries` with
   `!SameStackRepo(entry.Repo, "")` → I12. Not applied in external mode.
7. Duplicate-`GitBranch()` check across `Entries` → I13, when `opts.NewMode` is true, in **both**
   workspace modes. The frozen no-flag path never calls this function at all (§5.5), so no caller
   flag is needed to suppress the rule.
8. `Repos` = unique `Repo` values across `Entries`, sorted with `""` first.
9. Return.

`ResolveSyncSelection` performs **no** Git command, **no** filesystem access, **no** path
resolution, and **no** I/O — `opts.Feature` is consumed as a message value only. It is
pure over `(Stack, SyncRunPolicy, SyncSelectionOpts)` and is therefore unit-testable without a
repository.

### 5.3 Selector identity

The selector is a **`StackEntry.Name`**, never a Git branch, in both modes and in every message,
state file, and completion. Justification, all pre-existing: `stack.yaml` keys `GetBranch`,
`UpdateBaseSHA`, `RenameBranch`, `Descendants`, and `TopoSort` adjacency by `Name`; external state,
worktree paths, and every `formatSyncStatus` line already use `Name`; and two entries may
legitimately share one `GitBranch()`, so a Git branch is not a key.

`GitBranch()` is used for exactly and only: `git rebase` ref arguments, `git merge-base
--is-ancestor` operands, `git push` refs, `checkSyncWorktreeBranch` comparison, and
`CheckoutPlanEntry.Branch`.

### 5.4 Duplicate and decoupled identities

- Selection by `Name` is unambiguous by construction.
- Two entries with distinct `Name`s and the same `Branch` are legal in `stack.yaml`. In a
  **new-mode** run, if two **selected** entries share a `GitBranch()`, the run is refused (I13):
  replaying one Git branch twice from two different bases in one run is incoherent and would make
  `LastBaseSHA` attribution and the completion gate meaningless.
- In a **no-flag** run the duplicate check is not applied — today's *Git* behaviour (two rebases of
  the same ref) is frozen, and it is frozen structurally: the no-flag path never calls
  `ResolveSyncSelection` (§5.5).
- What is **not** frozen on such a stack is the *metadata attribution*:
  `finalizeTransaction`'s `LastBaseSHA` attribution becomes `Name`-keyed (C3), fed by the plan's
  additive `name` key (C5), which fixes the duplicate-branch mis-attribution regardless of scope and
  on no-flag runs too. On a duplicate-`GitBranch()` stack a no-flag checkout therefore writes a
  different `stack.yaml` than the pre-change tree — the declared C2/C3 exception of §4.1 rule 7,
  §4.5 C3, AC 34.

### 5.5 Per-mode application of one selection

The selection produces a **name set and classification**. Each mode then applies its own existing
execution filters *within* that set. This is the resolution of M5 and M6 and is deliberate: unifying
the selector validation while leaving execution filters mode-specific is what keeps the frozen paths
frozen.

| Concern | External | Checkout |
|---|---|---|
| Membership source | `SyncSelection.Entries` | `SyncSelection.Entries` |
| `Archived` **flag** | ignored during execution (today's behaviour); refused only when *explicitly named* (I11) | entries with `Archived: true` are skipped from the plan (today's behaviour); refused when *explicitly named* (I11) |
| Materialization | pass 1 = worktree present, pass 2 = worktree absent (today's behaviour), both restricted to `Entries` | not applicable (single checkout) |
| Cross-repo entry in the set | anchor (never a propagation edge); base resolved literally under `full` | **refused** (I12, D11), matching the shipped `cross-repo-unsupported` classification |
| Fetch boundary | `UniqueRepos` over the **selected** subset (§6.5) | single repo (`RepoDir`) |
| Completion | selected propagation edges (§7.5) | per-plan-entry ancestry, already scope-correct once the plan is scoped |
| Push | selected successful entries, `GitBranch()` (§7.6) | selected plan entries, `pe.Branch` (already `GitBranch()`) |

For `SyncScopeAll` with **no** trigger flag (the frozen path), the caller MUST bypass
`ResolveSyncSelection` entirely and keep today's code path; this is what structurally suppresses
I13 there rather than a suppression flag. The selection model is used for new-mode runs only.
Rationale: an equivalent-by-inspection refactor of the frozen path is a compatibility risk with no
user-visible benefit, and the goldens can only prove equivalence for the fixtures they cover.
`SyncScopeAll` *with* a trigger flag (e.g. `--local-only` alone) does use the model, and therefore
does get I12/I13.

### 5.6 Anchors, propagation edges, and the no-op rule (D3, D4)

- An **anchor** is a selected entry with `Role == SyncRoleAnchor`. Under `local-only` an anchor is
  **read, never advanced, never required to be current**, and never rebased. Under `full` an anchor
  is rebased onto its configured base exactly as today.
- A **propagation edge** is `(ParentName → Name)` for every selected entry with
  `Role == SyncRolePropagated`. The parent need **not** be in the selection: ancestors outside the
  selection are anchors, never prerequisites (D4). Auto-expanding the selection upward is refused
  as scope creep — it is precisely the surprising broad rebase this feature exists to stop.
- Under `full` with an explicit selection, a stale ancestor edge outside the selection is reported
  through the informational block of §3.7 and does not change the exit code.
- **No-op rule (D3):** when the selection under `local-only` contains no propagation edge, the run
  prints the no-op block of §3.7 and exits **0**. A no-op selection is not an error. Under `full`
  the same selection has real work (advance onto the literal/external base) and is not a no-op.

### 5.7 Missing, prunable, and archived entries under an explicit selection

Settled explicitly (this refines the analysis §3.3 recommendation; see §21.1):

- **`Archived: true` explicitly named** → refuse (I11). Silently doing nothing is dishonest, and
  external's execution path would otherwise treat the flag as meaningless.
- **Explicitly named entry with no materialized worktree, not flagged archived** → **allowed**, and
  handled by the existing non-materialized path: `git rebase <base> <gitbranch>` in `entry.Repo` or
  the process cwd, printing `  [+] NAME (archived)` on success and
  `  [!] NAME (archived)` + `    Restore with: tws new <feature> NAME` on failure. It is never
  silently skipped. Erroring here would remove real, correct capability that the code already has.
- **Explicitly named entry whose worktree is prunable** → today's stop-the-run message, preserved
  verbatim: `  [?] NAME (missing — run: tws archive <feature> NAME or tws new <feature> NAME)`,
  state persisted, exit 1.
- **Unselected** prunable worktrees, unselected missing worktrees, and unselected archived entries
  MUST NOT stop or affect a scoped run. Today the prunable arm stops the whole run
  (`internal/cli/sync_helpers.go:86-90`); scoping the loop to `Entries` closes that.
- **Wrong branch checked out** in a selected worktree → today's stop:
  `  [?] NAME (active)` + problem + hint, state persisted, exit 1. Unselected worktrees on the
  wrong branch are not probed in a scoped run.

---

## 6. Fetch and propagation matrix

### 6.1 The four cells are all reachable, in both modes

| | `fetch` | `no-fetch` |
|---|---|---|
| **full** | external: default (no flag). checkout: `--fetch` | external: `--no-fetch`. checkout: default (no flag) |
| **local-only** | external: `--local-only`. checkout: `--fetch --local-only` | external: `--no-fetch --local-only`. checkout: `--local-only` |

Every cell is selectable in both modes, and every cell is also selectable *explicitly* (naming both
axes) so a script never depends on a mode default: e.g. external `fetch × full` is
`--fetch --full`, checkout `no-fetch × local-only` is `--no-fetch --local-only`. Combined with the
three scopes this yields 12 reachable cells per mode; AC 12–AC 17 cover all of them.

The cells are not redundant:

- `fetch × local-only` is not a no-op: the fetch updates `origin/*`, which changes what
  `tws stack status` and `tws doctor` report, while the stack reconciles only internally.
- `no-fetch × full` still advances a root when `origin/master` has already moved locally (an
  earlier fetch, a `git pull` elsewhere, clone state). Calling that "local-only" would be false.

### 6.2 `no-fetch` is an input-ref policy, not an offline mode (D14)

Binding definition. Under `SyncFetchDisabled` the run MUST issue **zero** automatic remote-input
operations: no `git fetch`, no `git ls-remote`, no `git remote update`, no network `origin/HEAD`
lookup, and no other implicit remote contact.

Explicitly **allowed** under `no-fetch`:

- reading `refs/remotes/*` (`origin/master`, `origin/HEAD` as a local symref) — these are local
  object-store reads;
- `internal.DefaultBranchIn(repo)`, whose first probe is `git rev-parse --abbrev-ref origin/HEAD`,
  a local ref read (`internal/exec.go:67-90`);
- `git push --force-with-lease` when `--push` was explicitly supplied.

Consequences, all binding:

- `--push` under `no-fetch` is legal in both modes and is the **only** way either reaches the
  network. The guarantee to state in help text and docs is "no automatic network **input**", never
  "offline".
- The strong property "**zero** network operations" holds for any `no-fetch` run **without**
  `--push`, and that is what AC 14 asserts with an unreachable `origin`.
- A stale `origin/<default>` under `no-fetch` is **not** an error. The run uses it and, when it is
  behind, simply does less work.
- A base ref that does not resolve locally under `no-fetch` is a **pre-flight fatal** (I14),
  never a mid-run failure. The preflight runs `git rev-parse --verify <ref>^{commit}` (through
  `internal.VerifyGitRef`) in the entry's repo context for every base that the selected plan will
  actually use — i.e. for anchors under `full`, and for propagation-edge parent branches always.
  Under `local-only`, anchor bases are **not** probed, because they are never used.
- Checkout mode with `--fetch` performs exactly one `git fetch` in `RepoDir`, as a **best-effort,
  pre-plan, pre-transaction** remote-ref refresh: after the existing read-only guards and after the
  complete new-mode preflight I9–I14, and before `BuildCheckoutPlan` and before the transaction
  exists. It mutates only remote-tracking refs, keeps external's tolerance (§6.4) and the same
  `Fetching <label>... done|failed` bytes, where `<label>` is `default repo`, and is deliberately
  not resumable. The full boundary — including what an interruption inside that window leaves
  behind — is binding in §10.3.

### 6.3 Propagation policy — exact base resolution per cell

| Mode | Policy | Anchor (`SyncRoleAnchor`) | Propagation edge (`SyncRolePropagated`) |
|---|---|---|---|
| external | `full` | `resolveBase(entry.Base)`: rewrite to `origin/<default>` **only** when `entry.Base == DefaultBranchIn(repoCtx)`; otherwise literal (M3) | parent's `GitBranch()` (M2) |
| external | `local-only` | **skipped, not rebased.** `resolveBase` is never called, so the `origin/` rewrite cannot leak in | parent's `GitBranch()` |
| checkout | `full` | `entry.Base` resolved **literally** with `git rev-parse` — a `master` root does not advance from remote, an `origin/master` root does (M3, frozen) | `entry.Base` resolved literally (M2, frozen) |
| checkout | `local-only` | **skipped, not rebased** | parent's `GitBranch()` — checkout converges with external here |

M2/M3 are therefore **kept divergent under `full`** and **converged under `local-only`**. This is
deliberate and is the resolution of M2/M3: the divergence is already documented and shipped as
`StackBasePolicy` / `StackBasePolicyForMode` (`internal/stack_ancestry.go:34-70,498-536`), which is
the authority this feature cites rather than re-derives; `local-only` is a *new* mode with no frozen
behaviour to preserve, so it is defined once, identically, for both.

Under `local-only` in checkout mode, `BuildCheckoutPlan` MUST resolve the plan entry's `Base` field
to the parent's `GitBranch()` before `gitResolveRef`, so `NewBaseSHA`, the `--onto` replay, and the
final ancestry check all describe the same ref.

### 6.4 Fetch failure tolerance (frozen)

A failed fetch prints `failed` and the run proceeds against whatever remote-tracking refs exist,
in **every** fetch-policy cell that fetches, including an explicit `--fetch`. Changing this to a
hard error would break offline-tolerant behaviour users rely on. `no-fetch` is the explicit way to
make "do not refresh from the network" a guarantee rather than an accident. A strict variant is a
follow-up (§18), not a redefinition of the default.

### 6.5 Fetch boundary under selection

External fetch MUST be restricted to the repos actually represented in the **selected** plan:

```go
sub := Stack{Branches: <selected entries as StackEntry values, in selection order>}
repos := internal.UniqueRepos(sub, featurePath)
```

`UniqueRepos` is reused verbatim with a stack **subset** input; no second per-repo walker is added.
Fetching a repo no selected entry touches is precisely the hidden work this feature removes.
Fetch remains **once per unique repo**, and the `Fetching …` line order across multiple repos
remains non-deterministic and MUST NOT be pinned.

### 6.6 Propagation under `local-only` — what is skipped

`local-only` skips, and MUST NOT rebase, fetch a base for, or probe:

- entries whose base is a **literal ref** (`master`, `origin/master`, a tag, a SHA);
- entries whose base is **empty**;
- entries whose base names a stack entry in a **different repo** (`SameStackRepo` false).

It propagates **only** selected same-repo parent→child edges, using the parent's current **local**
tip. It never advances a root, never consults `origin/<default>` for an anchor, and never reports a
run as incomplete because an anchor is behind its remote.

---

## 7. Rebase execution

### 7.1 External — exact rebase arguments

Let `base` be the resolved base from §6.3, `sha := internal.GetBranchSHA(gitContext, base)`, and
`scoped := run is new-mode AND Policy.ScopeKind != SyncScopeAll`.

| Case | Arguments |
|---|---|
| **no-flag run, or new-mode `scope=all`**, materialized entry, base unmoved | `rebase --update-refs <base>` (unchanged) |
| **no-flag run, or new-mode `scope=all`**, materialized entry, base moved | `rebase --update-refs --onto <base> <LastBaseSHA>` (unchanged) |
| **scoped run**, materialized entry, base unmoved | `rebase <base>` |
| **scoped run**, materialized entry, base moved | `rebase --onto <base> <LastBaseSHA>` |
| any run, non-materialized entry | `rebase <base> <gitbranch>` (unchanged; already without `--update-refs`) |

`--update-refs` is **dropped for scoped runs** (D5). Justification, binding: `git rebase
--update-refs` rewrites the refs of **any** branch pointing into the rebased range, including
entries outside the selection. That is not hypothetical — `markUpdatedAncestors`
(`internal/cli/sync_helpers.go:194-215`) exists precisely because `--update-refs` moves
non-materialized ancestors. Keeping it in a scoped run would directly contradict the feature's core
safety promise ("no unrelated ref movement"), which AC 19 and AC 21 assert with before/after SHA
snapshots.

Consequences that MUST be handled:

- `markUpdatedAncestors` MUST NOT be called in a scoped run. `updatedByRef` stays empty, so every
  selected non-materialized entry takes the explicit `rebase <base> <gitbranch>` path rather than
  being marked done for free. This is correct: without `--update-refs` nothing moved it.
- A scoped run may therefore replay a commit range that a full run would have deduplicated through
  `--update-refs`. That is the intended trade: correctness of scope over convenience.
- **A full-scope run keeps `--update-refs` exactly**, so no-flag bytes, conflict behaviour, and
  duplicate-commit behaviour are untouched.

The amend-aware `--onto <base> <LastBaseSHA>` replay (`amend-aware-rebase`) is preserved in **every**
cell, scoped and full, in both modes. It is the mechanism that keeps a recorded base that left the
parent's history replayable, and dropping it under any policy would be a regression.

`internal.RunDirClean` remains the runner for materialized rebases and `internal.RunSilentDir` /
`internal.RunSilent` for the non-materialized path, unchanged. Its stderr filter — `hint:` /
`Disable this message` removal plus the `skipped previously applied commit` →
`    (skipped duplicate commit)` reformat (`internal/exec.go:138-186`) — is **not** exercised by any
frozen no-flag golden: the §17.1 wrapper diverts the `rebase`/`push` child streams, so
`RunDirClean` sees an empty stderr stream in every external capture (§4.1 rule 1b). The filter is
therefore covered by focused tests of `RunDirClean` itself — any that `clean-git-output` owns stay
unchanged, plus the direct regression assertion AC 2 requires — never by a golden.

### 7.2 External — archived (non-materialized) entries in a scoped run

Pass 2 iterates `Entries` only. For each selected non-materialized entry:

1. If the worktree is prunable → today's stop (§5.7).
2. `updatedByRef` is always empty in a scoped run, so the `[+] NAME (archived)` free-mark path is
   never taken; the entry is genuinely rebased.
3. Under `local-only`, an anchor in pass 2 is **skipped** with the `[-]` line, exactly as in pass 1.
4. `LastBaseSHA` is **not** updated for pass-2 entries (today's behaviour, frozen).

### 7.3 External — `LastBaseSHA` and `stack.yaml` writes

`UpdateBaseSHA` + `SaveStack` after each successful materialized entry is unchanged in mechanism.
In a scoped run only selected entries are iterated, so only selected entries' `last_base_sha` values
change. `SaveStack` still rewrites the whole file; AC 19 asserts that every **unselected** entry's
serialized fields are byte-identical before and after.

### 7.4 Checkout — plan, scope, and identity

`CheckoutPlanEntry` gains one additive field, first in the struct so YAML ordering is stable:

```go
type CheckoutPlanEntry struct {
    Name        string `yaml:"name,omitempty"` // logical StackEntry.Name (added)
    Branch      string `yaml:"branch"`
    Base        string `yaml:"base"`
    LastBaseSHA string `yaml:"last_base_sha"`
    NewBaseSHA  string `yaml:"new_base_sha"`
    PreSHA      string `yaml:"pre_sha"`
    PostSHA     string `yaml:"post_sha"`
}
```

- `BuildCheckoutPlan` gains a `SyncSelection` parameter (a nil/zero selection means "all", the
  frozen path) and fills `Name` for every entry it builds — **including on the frozen no-flag
  path** (C5). Writing `Name` only for new-mode runs would leave C3's attribution fix inert exactly
  where the duplicate-branch defect occurs today.
- Plan identity for metadata attribution becomes `Name` (C3); plan identity for Git operations
  stays `Branch`.
- `finalizeTransaction` matches `stack.Branches[i].Name == pe.Name` when `pe.Name != ""`, and falls
  back to today's `GitBranch()` first-match when `pe.Name == ""` (an old transaction written by a
  previous binary). This is the old-transaction compatibility rule (§10.6).
- `restoreOriginal`'s `inPlan` test keeps comparing `pe.Branch == tx.OriginalBranch` (a Git branch
  on both sides). With a scoped plan, an original branch excluded from the scope becomes the
  `!inPlan` case and is HEAD-asserted — a free safety win that MUST be preserved.
- The final ancestry loop over `tx.Plan` (`internal/checkout_sync.go:957-966`) is already
  scope-correct once the plan is scoped; it is not changed.
- Under `local-only`, anchors are **excluded from the plan** (not planned-and-skipped), so
  `CurrentIndex`, `CompletedIndices`, and the final ancestry loop all describe only replayed edges.

### 7.5 Selected-edge completion (both modes)

External gains one function beside the existing one:

```go
func staleStackEdgesFiltered(feature string, stack internal.Stack, selected map[string]bool) []string
```

It is `staleStackEdges` with one additional guard — `if selected != nil && !selected[child.Name] { continue }`
— and MUST otherwise be byte-identical in predicate and message. `staleStackEdges(feature, stack)`
becomes `staleStackEdgesFiltered(feature, stack, nil)`, so there is exactly one predicate.

Binding rules:

1. **Full-scope runs** (no-flag and new-mode `scope=all`) call it with `nil` and keep today's exact
   failure block, empty-`FailedBranch` state write, and exit 1.
2. **Scoped runs** call it with the selected name set. The edges considered are exactly the selected
   propagation edges whose child worktree is materialized — never wider. A stale edge in that set
   fails the run with the same block and exit 1.
3. Stale edges **outside** the selection are collected by a second call with the complement set and
   printed as the informational block of §3.7. They never change the exit code.
4. Under `local-only` the predicate is unchanged: "each selected child contains its **local** parent
   tip" is exactly what `staleStackEdges` already asserts, since it skips literal/empty-base and
   cross-repo edges and never probes `origin/<default>`. AC 22 proves this by leaving the root
   behind `origin/<default>` and asserting exit 0.

`branchContainsConfiguredParent` (`internal/cli/sync.go:153-160`) is reused unchanged for the
`--continue` precondition on the failed entry. No third ancestry classifier is introduced, and the
shipped `StackEdge` evaluator is **not** collapsed into these two (D6, §18).

### 7.6 Selected push

New helper in `internal/cli/push.go`:

```go
func pushSelected(feature string, stack internal.Stack, names []string) error
```

- Pushes **only** the entries named in `names`, which the caller populates with the selected
  entries that were **successfully** rebased in this run (`completed`), in topological order.
- Uses `entry.GitBranch()` as the ref (never `entry.Name`).
- Uses `git push --force-with-lease origin <gitbranch>` through `internal.RunDirClean` in the same
  repo context `pushFeature` computes (`entry.Repo` when set, else the worktree path).
- Emits the same per-entry lines `pushFeature` emits (`  [+] NAME (pushed)`,
  `  [x] NAME (push failed)`), keyed by logical `Name`.
- Skips entries with no worktree directory with `  [-] NAME (archived, skipped)`.

`pushFeature` itself is changed only by C2 (`GitBranch()` ref). The **full legacy push ref defect
(M14) is fixed** in this feature, and the fix applies to `tws push` as well as to no-flag
`tws sync --push`. Documented behaviour after the fix: for coupled names nothing changes — same
argv, same output, same exit code, same refs — and for a
decoupled entry (`name: work`, `branch: user/work`) the pushed ref becomes `user/work`, which is the
branch that actually exists, so the push argv, the per-entry line, the exit code, and the updated
remote ref all change on the **no-flag** path too. That is the declared C2 exception of §4.1 rule 7
(§4.5 C2, AC 33), not a frozen behaviour. The CHANGELOG entry MUST call this out as a fix with a
behaviour note.

New-mode runs with `scope=all` use `pushFeature` (whole stack) so the semantics of "`--push` pushes
the feature" are unchanged when nothing was scoped; scoped runs use `pushSelected`.

### 7.7 Validation

- **External**: `runValidation` is unchanged — `internal.LoadConfig().TestCommand`,
  `strings.Fields`, run silently in the worktree, printing
  `    validating NAME: CMD... ` + `ok`/`FAILED`. `--test` stays inert in external mode (D16, M8).
  New-mode runs persist the *resolved* command string and its source (`config`) in the payload and
  use the persisted string on `--continue`, so a `--continue` from a different shell or after a
  config edit cannot validate with a different command. No-flag runs persist nothing and re-read
  config on `--continue`, exactly as today.
- **Checkout**: `runValidation(opts)` is unchanged — `opts.TestCommand` via `sh -c` in `RepoDir`.
  `--test` remains the source, and `tx.TestCommand` remains the persisted, resume-authoritative
  value. New-mode runs additionally record `validation_source: flag` for symmetry.
- M8 is therefore **kept divergent and documented**, not harmonised. Unifying the source or the
  execution model is an independent behaviour change that would confuse the axis story and break
  external users whose validation runs today from config.

---

## 8. External new-mode state machine

This section is binding in full. Every filename, mode, field, order, and message is exact.

### 8.1 Files, permissions, and version constants

| Path | Written by | Mode | Atomic | Read by |
|---|---|---|---|---|
| `<featurePath>/.sync-state.yaml` | no-flag failures (`saveIncompleteSync`) **and** the new-mode sentinel, never both in one run | `0644` | yes (C1) | new binary, old binary, `BuildAgentStatus`, filtered by `isRuntimeState` |
| `<featurePath>/.sync-state.v2.yaml` | new-mode runs only | `0600` | yes | new binary, `BuildAgentStatus` (marker-aware projection), filtered by `isRuntimeState` |
| `<featurePath>/.sync-run.lock` | new-mode runs only | `0600` (`O_EXCL`) | n/a (single exclusive create) | new binary, `BuildAgentStatus` (read-only), filtered by `isRuntimeState` |

- The sentinel keeps mode `0644` **deliberately**: it lives at the legacy path, must be
  indistinguishable in shape from a legacy state file to an old binary, and carries only a nonce.
  The payload and the guard, which carry the real decision and the ownership token, are `0600`.
- Any directory this feature creates is `0700` (`atomicWriteFile` already does
  `os.MkdirAll(dir, 0700)`); the feature directory itself is not re-chmod'ed.
- Version constants, in `internal/sync_run_state.go`:

  ```go
  const SyncRunStateVersion = 2          // external payload state_version
  const CheckoutTransactionVersion = 2   // checkout transaction state_version
  ```

  `state_version` is **forward-only** protection: a new binary refuses a version it does not
  understand. It provides **no** downgrade protection, because an old decoder drops unknown keys
  silently, and this document MUST never claim otherwise (D7). Absent `state_version` means
  "legacy" = `fetch`(external)/`no-fetch`(checkout) × `full` × `all`, i.e. exactly today's
  semantics.

### 8.2 Marker grammar and mandatory collision pre-flight

**Generation lives in package `cli`.** Per run, never a compiled-in constant, declared in
`internal/cli/sync_modes.go`:

```go
// package cli, internal/cli/sync_modes.go
func newSyncMarker() (string, error)   // "tws-scoped-sync-" + hex(crypto/rand 16 bytes) + ".lock"

var syncMarkerFn = newSyncMarker // overridable by package-cli tests only
```

Exactly 16 bytes of `crypto/rand`, lower-case hex, giving
`tws-scoped-sync-<32 hex chars>.lock` (53 characters: 16 + 32 + 5). `RunE` (and the helpers it calls
in package `cli`) invoke `syncMarkerFn` directly at §3.6 step 12; package-cli tests override the
variable to force a chosen value (AC 30). The seam is a **package-private** `var` in `cli`: it MUST
NOT be exported, and no exported generator, setter, or mutable global is introduced in any package.

**Recognition lives in package `internal`.** `func isSyncMarker(s string) bool` — unexported, in
`internal/sync_run_state.go` — matches `^tws-scoped-sync-[0-9a-f]{32}\.lock$` and nothing else. It
is used **only** for classification, and it has **exactly one caller**:
`ClassifyExternalSyncState`, in package `internal`. No other function in any package calls it or
branches on it — in particular `buildFeatureSync` (§11.1) MUST NOT, and consumes only
`SyncExternalState.Cell` and the classifier's decoded fields. Package `cli` never calls it and never
calls any unexported `internal` symbol:
it consumes the classification through `SyncExternalState.Cell` and `SyncExternalState.Marker`
(§11.1). The grammar is the single shared contract between the two packages; it is written once in
this section and asserted from both sides (AC 29).

**Structural properties, both mandatory and directly testable:**

1. **Safe single path component.** Contains no `/`, no `\`, no NUL, is neither `.` nor `..`, and
   does not begin with `-`. This matters because the old `--abort` path interpolates
   `state.FailedBranch` straight into a worktree path with no validation
   (`internal.WorktreePath(feature, state.FailedBranch)`, `internal/cli/sync.go:91-96`): a marker
   that cannot escape the feature directory turns that interpolation into a harmless miss.
2. **Rejected by `git check-ref-format --branch`.** The trailing `.lock` suffix guarantees it, so
   `git branch <marker>` fails outright and the marker can never name a real ref in any repository
   the run touches.

**Mandatory pre-flight (I17), before the guard claim and before any side effect.** One pass over
`stack.Branches` asserting that the generated marker equals **neither** any `StackEntry.Name`
**nor** any `entry.GitBranch()`. On collision the run is refused, and nothing is mutated: no guard,
no sentinel, no payload, no fetch, no rebase. This single rule is what makes the mechanism sound;
the nonce merely makes reaching it improbable.

**Honest strength statement**, to be reproduced in the docs:

- *As a Git branch*: structurally impossible — Git refuses to create a ref ending in `.lock`.
- *As a `StackEntry.Name`*: not producible by normal CLI creation, because `tws new` runs
  `git branch <gitBranch>` before registering the entry (`internal/cli/new.go:125-128`). With
  `--branch` decoupling, a hand-crafted `stack.yaml` *can* contain a `.lock`-suffixed `Name`; the
  nonce makes an accidental match astronomically improbable and the pre-flight turns a deliberate
  one into a refusal.
- *Residual*: if `stack.yaml` is hand-edited **after** the sentinel is on disk to introduce an entry
  named exactly the live marker, an old binary's `--continue` could resolve it and broad-resume.
  This is not claimed to be impossible. New binaries re-assert the invariant on every `--continue`
  and `--abort` and refuse when it is violated; the case is documented as unsupported tampering.

### 8.3 The run guard

Path `<featurePath>/.sync-run.lock`. Claimed with `os.OpenFile(path, O_WRONLY|O_CREATE|O_EXCL, 0600)`
— the same idiom as `writeLockExclusive` (`internal/checkout_sync.go:207-222`), not a second lock
idiom. Content:

```yaml
pid: 12345
created: "2026-08-14T12:00:00Z"
token: "9f2c…"          # 16 bytes crypto/rand, hex; the run ownership token
state_version: 2
```

Go type `SyncRunGuard` in `internal/sync_run_state.go`. Rules, binding:

1. **Claim ordering**: the guard is claimed **before** the sentinel and the payload, and **after**
   the marker collision pre-flight.
2. **Fresh new-mode run** behaves like `AcquireCheckoutLock`: it never steals a **live** guard
   (I16), and it **refuses** a stale guard that still has a payload —
   `stale sync guard from PID %d with existing scoped state; use --continue or --abort to recover`.
3. **`--continue` / `--abort`** reclaim like `forceAcquireCheckoutLock`
   (`internal/checkout_sync.go:247-270`): a live guard owned by another PID is never reclaimed
   (`sync guard held by live process %d; cannot reclaim`); a stale guard is removed only if its
   bytes are unchanged since the read (the `removeLockIfUnchanged` pattern) and then re-claimed.
   In cells 2, 4, and 5 the live-guard precedence of §8.6 fires **first**, so the user-facing §8.7
   live-guard messages are what an operator sees and no reclaim is even attempted; the string in
   this rule covers the residual case where a guard classified stale becomes live before the
   reclaim, and it, too, mutates nothing.
4. **Liveness** uses the existing `isProcessAlive` (`internal/checkout_sync.go:288-296`). A
   substitutable predicate seam is added for tests (§17.4). A guard with `pid <= 0` is treated as
   *invalid*, not stale: `sync guard is being initialized or is invalid; retry or inspect %s`.
5. **Ownership token**: the payload records the same `owner_token`. A guard whose token differs
   from the payload's token is a foreign guard: it is reported and never silently reclaimed —
   `sync guard %s does not belong to the recorded scoped run; inspect it and remove it manually`.
   This is what makes an imported or hand-planted guard diagnosable rather than authoritative.
6. **Release** is **last** on success and on a clean `--abort`, after the payload and the sentinel
   have been deleted, in that order.
7. **No-flag runs never claim, reclaim, or release the guard of a run they do not own**, and never
   read it on the ordinary path. Legacy concurrency behaviour (unguarded) is untouched: a no-flag
   run with no payload and
   no sentinel does not open `.sync-run.lock` at all (§3.6 step 8c); AC 38 proves this by placing an
   **unreadable** guard beside exactly that run and reproducing the AC 1 golden unchanged. The guard is read — read-only,
   for message context and liveness — only in the cells the no-flag run already refuses, i.e. when
   the declared payload `Lstat` found a payload or the legacy file decoded to a sentinel. In those
   cells a no-flag `--abort` that performs the documented cleanup (cell 2's §9.2 recovery, cell 4's
   sentinel deletion, cell 5's teardown) deletes a **stale or unowned** guard as the last step of
   that cleanup; it never removes, reclaims, or overwrites a **live owning** guard, because
   live-guard precedence refuses all three verbs first (§8.6). A stale
   guard never blocks a no-flag run beyond the existing incomplete-state check and the narrow
   payload-residue refusal.

### 8.4 The v2 payload

Type `SyncRunState` in `internal/sync_run_state.go`, marshalled with `gopkg.in/yaml.v3`:

```yaml
state_version: 2                 # SyncRunStateVersion; unknown ⇒ refuse
feature: auth
started_at: "2026-08-14T12:00:00Z"
updated_at: "2026-08-14T12:03:11Z"
marker: tws-scoped-sync-3f9c….lock
owner_token: "9f2c…"
stage: rebasing                  # see enum below
fetch_policy: fetch              # SyncFetchPolicy
propagation_policy: local-only   # SyncPropagationPolicy
scope_kind: subtree              # SyncScopeKind
scope_selector: parent           # logical StackEntry.Name, "" for scope_kind: all
selected:                        # resolved selection, topological order, logical Names
  - parent
  - child
push: true                       # frozen --push decision
test_command: "go test ./..."    # resolved validation command actually used
validation_source: config        # config | flag | none
failed_branch: child             # REAL logical StackEntry.Name, never the marker
pending:                         # subset of `selected`
  - grandchild
completed:                       # subset of `selected`
  - parent
pushed: []                       # selected entries already pushed by this run
repos:                           # unique Repo values in the selection ("" = default repo)
  - ""
```

**Stage enum** (`SyncRunStage`), exhaustive and closed:

```
initializing | fetching | rebasing | validating | pushing | finalizing | failed
```

- `initializing` — payload written, nothing fetched or rebased yet.
- `fetching` — inside the fetch loop.
- `rebasing` — inside the rebase loop.
- `validating` — running the frozen validation command for `failed_branch`'s successor.
- `pushing` — inside selected push.
- `finalizing` — completion gate passed, teardown starting.
- `failed` — a conflict, a validation failure, a wrong-branch stop, a prunable stop, or a stale
  selected edge stopped the run. `failed_branch` names the **real** logical entry, or is `""` for
  the stale-edge case (mirroring today's empty-`FailedBranch` semantics).

**Field rules, binding:**

- `failed_branch` is a **real** `StackEntry.Name` or `""`. The marker MUST NOT ever be written
  there, published there, or surfaced from there.
- `selected` is the **materialized resolved list** captured at plan time, so a `stack.yaml` edit
  between failure and `--continue` cannot silently re-scope the run. On `--continue`, any name in
  `selected` that no longer exists in the stack is a refusal:
  `selected stack entry %q no longer exists in stack; use --abort` (mirroring the existing
  `failed branch %q no longer exists in stack` shape).
- `pending`/`completed` are derived from `selected`, never from the full sorted list. This is what
  keeps `Resuming sync with %d pending branch(es)` honest under selection.
- `push`, `test_command`, and `validation_source` are frozen at plan time and re-read, never
  re-inferred, on `--continue`.
- Every write is atomic (`atomicWriteFile`, `0600`) and refreshes `updated_at`.

### 8.5 Setup and teardown ordering (the whole mechanism)

**Setup**, in this exact order:

1. claim the run guard (§8.3);
2. atomically write the **legacy sentinel** to `.sync-state.yaml`:
   `SyncState{StartedAt: <now>, FailedBranch: <marker>, Pending: [], Completed: [], Skipped: []}`
   — **first**, before any other state, and **written exactly once per run**;
3. atomically write the **v2 payload** to `.sync-state.v2.yaml` — **second**;
4. only then print the header, fetch, plan, and rebase.

**Progress** is written **only** to the payload. The sentinel is never rewritten and in particular
is **never overwritten with a real branch or entry name**.

**Teardown**, on success and on a clean `--abort`, in this exact reverse order:

5. delete the **v2 payload** first;
6. delete the **sentinel** second;
7. release the **guard** last.

`saveIncompleteSync` MUST NOT be called by a new-mode run. Calling it would overwrite the sentinel
with a real, *resolvable* `Name` and hand an old `--continue` exactly the broad resume this
mechanism exists to prevent — silently, at the moment of failure, which is the moment an operator is
most likely to reach for another binary. New-mode failure persistence writes the payload only,
through `saveScopedSyncFailure(featurePath, payload, failed string)`. No-flag runs keep calling
`saveIncompleteSync` unchanged, byte-shape included.

**Two consequences are normative and every recovery rule below derives from them:**

- **A crash in a healthy run can never produce payload-without-sentinel**, because setup writes the
  sentinel first and teardown deletes the payload first. `{absent, valid}` is therefore reachable
  **only** through an old binary's `--abort` or through tampering.
- **Sentinel-without-payload is ambiguous**: it is produced both by a crash between steps 2 and 3
  (interrupted **initialization**: nothing fetched, nothing rebased) and by a crash between steps 5
  and 6 (interrupted **finalization**: a complete run, or a complete abort, already happened). From
  disk alone the two are indistinguishable, so no message, no status projection, and no test may
  claim "no fetch and no rebase happened" in this state.

Crash-point → cell mapping (asserted by AC 36):

| Crash point | Resulting cell |
|---|---|
| after guard, before sentinel | `{absent, absent}` + guard-only residue |
| after sentinel, before payload | `{sentinel, absent}` (initialization half) |
| during fetch / rebase / at a conflict | `{sentinel, valid}` |
| after payload delete, before sentinel delete | `{sentinel, absent}` (finalization half) |
| after sentinel delete, before guard release | `{absent, absent}` + guard-only residue |
| torn payload write (only if `atomicWriteFile` were bypassed) | `{sentinel, unreadable}` |

### 8.6 The complete state matrix

Axes:

- **legacy path** `.sync-state.yaml` ∈ `{absent, sentinel marker, real legacy state, unreadable/invalid}`;
- **v2 payload** `.sync-state.v2.yaml` ∈ `{absent, valid-supported, unreadable/unknown-version}`;
- **run guard** ∈ `{live owner, stale/absent}` — **precedence and context, not a state axis**. It
  never changes which cell the disk is in; it decides whether that cell is a live window or a
  residue.

Classification rules:

- "sentinel marker" = the legacy file decodes as `SyncState` **and** `isSyncMarker(FailedBranch)`.
- "real legacy state" = decodes as `SyncState` and `!isSyncMarker(FailedBranch)` (including an
  empty `FailedBranch`, which is today's stale-edge state).
- "unreadable/invalid" = the file exists but `LoadSyncState` returns an error.
- "valid-supported" = the payload decodes **and** `state_version == SyncRunStateVersion`.
- "unreadable/unknown-version" = the payload exists but fails to decode, or decodes with a
  `state_version` this binary does not implement.

"Refuse" always means before any fetch and before any Git mutation.

| # | legacy path | payload | Meaning | New binary: plain / `--continue` | New binary: `--abort` |
|---|---|---|---|---|---|
| 1 | absent | absent | no new-mode run touched this feature | **fully normal, frozen**: plain proceeds exactly as today; a `--continue` with **no** trigger flag behaves exactly as today, and a `--continue` carrying any trigger flag is refused by I20 | exactly today: `Nothing to abort — no sync in progress.` — unless a stale or live guard file exists, in which case the **guard-only residue** bullet of the guard-precedence list below applies |
| 2 | absent | valid | **old-`--abort` residue or tampering**; never produced by healthy teardown | *(stale/absent guard)* refuse both; name the payload's **real** failed entry and its worktree path; never surface the marker. *(live owning guard)* refuse both with the live-run messages — the run is still alive and owns the payload | *(stale/absent guard)* perform the §9.2 recovery: abort the real rebase named by the payload if one is still in progress, delete the payload, remove any residual guard; **does not require the sentinel to exist**. *(live owning guard)* refuse; touch nothing |
| 3 | absent | unreadable/unknown | new-mode residue that cannot be interpreted | refuse both, fail closed, guidance naming the payload path | refuse to guess: report the unreadable payload and require explicit manual removal after inspecting the worktrees; **never delete state whose contents are unknown** |
| 4 | sentinel | absent | **ambiguous**: interrupted initialization *or* interrupted finalization | refuse both; the message states the ambiguity and MUST NOT assert that no fetch or rebase happened; `--continue` refuses because there is no scope to resume | stale/absent guard: re-check that no payload exists, then delete the sentinel and the guard, exit 0 clean. **Live guard**: refuse — this is an in-progress setup/teardown window owned by another process |
| 5 | sentinel | valid | **authoritative new-mode state** | the payload is the single source of truth for scope, policy, progress, and the real failed entry; the sentinel is never parsed for anything. `--continue` resumes scoped (stale/absent guard only); plain refuses | drives the abort entirely from the payload, then deletes payload → sentinel → guard in that order |
| 6 | sentinel | unreadable/unknown | new-mode run whose authoritative state is unusable | refuse both, fail closed, manual guidance | refuse to guess, as row 3; the sentinel is **not** deleted while an uninterpretable payload sits beside it |
| 7 | real legacy | absent | **the frozen legacy path** | byte-compatible: today's `previous sync incomplete (failed on: %s); use --continue or --abort` on plain; today's resume on a `--continue` with **no** trigger flag; a `--continue` carrying any trigger flag is refused by I20 before Git | today's abort, byte for byte |
| 8 | real legacy | valid | **inconsistent mixed state** (reachable — see §9.3) | refuse both; the message names **both** the legacy failed entry and the payload's real failed entry, because either can own an unfinished rebase | refuse to auto-resolve; walk the operator through both states. Clearing them is two explicit decisions, not one |
| 9 | real legacy | unreadable/unknown | inconsistent, half uninterpretable | refuse both, fail closed, manual guidance | refuse; manual only |
| 10 | unreadable/invalid | absent | corrupt legacy state — **declared no-flag behaviour change 1 (C1, §4.1 rule 4; the other is C4, rule 5)**; all three verbs change | fail closed with a clean error naming the file, exit 1: plain no longer panics on the nil `*SyncState` of today (`internal/cli/sync.go:56-58`) and `--continue` no longer reports today's opaque `nothing to continue — no sync in progress` for a file that exists. Nothing is deleted | fail closed, exit 1, naming the file and requiring manual removal. **This replaces today's `Nothing to abort — no sync in progress.` with exit 0**, which silently treats corrupt state as absent, leaves the file in place, and never aborts a real rebase that may still be in progress. Deletes nothing |
| 11 | unreadable/invalid | valid | corrupt legacy state over a new-mode payload | refuse both with the dedicated cell-11 message of §8.7, naming the corrupt legacy path **and** the payload's real failed entry and worktree | refuse to auto-resolve with the dedicated cell-11 abort message of §8.7, naming both; deletes nothing |
| 12 | unreadable/invalid | unreadable/unknown | nothing on disk is interpretable | refuse both, fail closed | refuse; manual only |

**Guard precedence**, applied on top of the cell:

- **Live owning guard** (`isProcessAlive(pid)` true and the token matches the payload when both
  exist): rows **2**, 4 and 5 report an **active** scoped sync and refuse plain, `--continue`, **and**
  `--abort` (I16 wording for plain/continue;
  `a scoped sync is running for %q (pid %d); wait for it to exit before --abort` for abort). No
  reclaim, in any payload state. Row 2 is included because it is exactly the shape an old
  `--abort` produces **against a still-running new-mode run**: the old binary deletes the sentinel,
  but the owning process, its guard, its payload, and its real in-progress rebase all survive. A
  live-guard `--abort` in row 2 therefore MUST NOT run the §9.2 recovery: it MUST NOT abort the
  real rebase, MUST NOT delete or rewrite the payload, and MUST NOT remove or reclaim the guard,
  because that would mutate a repository another live process is actively rebasing.
- **Stale or absent guard**: rows behave exactly as tabulated. The row-2 recovery of §9.2, the
  row-4 sentinel deletion, and the row-5 resume are all reachable **only** in this direction.
- **Guard-only residue** (`{absent, absent}` plus a guard file): a stale guard is reclaimed silently
  by the next new-mode run; a live guard refuses it (I16). A **no-flag** run — including `--abort`,
  which is a no-flag invocation (§3.3) — neither reads it nor is blocked by it: with no payload and
  no sentinel, §3.6 step 8c never opens the guard, so `--abort` prints
  `Nothing to abort — no sync in progress.` (today's string) and **leaves the guard file alone**.
  Clearing it from that verb would require a second added no-flag runtime-state-path read, which
  §4.4 forbids; the
  residue is cleared by the next new-mode run or by deleting the file, and `tws status` names it
  (§11.1).

Rows 1 and 7 are the **only** cells in which a new binary behaves exactly as today, and only for
invocations that carry **no** trigger flag; a `--continue` carrying one is refused by I20 in both of
them. Row 5 (with a stale/absent guard) is the only cell in which it resumes. Everything else
refuses.

### 8.7 Exact new-binary messages

The marker interception fires **only** for the cells the classifier reports as sentinel cells —
i.e. when `isSyncMarker(state.FailedBranch)` is true inside `internal.ClassifyExternalSyncState`
(D18b); package `cli` branches on the returned `Cell`, never on the predicate.
Ordinary legacy state keeps producing today's
`previous sync incomplete (failed on: %s); use --continue or --abort` byte for byte, and today's
`Nothing to abort — no sync in progress.` and `Sync state cleared.` are untouched **for absent and
decodable state**. They are *not* reused for an unreadable legacy file: cell 10 is the declared C1
behaviour change (§4.1 rule 4, §4.5), and its abort no longer takes the "nothing to abort" arm.

| Cell / guard | Verb | Exact message |
|---|---|---|
| 2/4/5, live guard | plain, `--continue` | `a scoped sync is already running for %q (pid %d, started %s); wait for it or use --continue/--abort after it exits` |
| 2/4/5, live guard | `--abort` | `a scoped sync is running for %q (pid %d); wait for it to exit before --abort` |
| 5, stale/absent | plain | `a scoped sync is incomplete (failed on: %s); use --continue or --abort` |
| 5, stale/absent | `--continue` | *resumes* — prints the header of §3.7 then `Resuming sync with %d pending branch(es)` using `len(payload.Pending)` |
| 5, stale/absent | `--abort` | on success prints `Sync state cleared.` (today's string) |
| 4, stale/absent | plain, `--continue` | `a scoped sync left partial state for %q: it was interrupted either while starting up or while finishing, and this cannot be distinguished on disk; work may or may not have been done. Inspect the worktrees, then run: tws sync %s --abort` |
| 4, stale/absent | `--abort` | prints `Sync state cleared.` after deleting sentinel + guard |
| 2, stale/absent | plain, `--continue` | `a scoped sync record survives without its state file for %q: it failed on %s (worktree %s) and that rebase was never aborted. Resolve or abort it there, then run: tws sync %s --abort` |
| 2, stale/absent | `--abort` | prints `Sync state cleared.` after the §9.2 recovery |
| 8 | plain, `--continue` | `two unfinished syncs are recorded for %q: a legacy sync failed on %s and a scoped sync failed on %s; resolve both before syncing (inspect %s and %s)` |
| 8 | `--abort` | `refusing to clear two unfinished syncs at once for %q: a legacy sync failed on %s and a scoped sync failed on %s; inspect %s and %s and remove them explicitly` |
| 3, 6, 9, 12 | plain, `--continue`, `--abort` | `scoped sync state at %s is unreadable or uses an unsupported version (%s); inspect it and remove it manually — tws will not guess` |
| 11 | plain, `--continue` | `sync state at %s is unreadable, and a scoped sync record beside it failed on %s (worktree %s); resolve or abort that rebase, then remove %s manually — tws will not guess` |
| 11 | `--abort` | `refusing to clear unreadable sync state at %s while a scoped sync record beside it is still unfinished: it failed on %s (worktree %s); inspect both and remove %s explicitly` |
| 10 | plain, `--continue` | `sync state at %s is unreadable: %v` — exit 1, deletes nothing (replaces today's panic on plain and today's `nothing to continue — no sync in progress` on `--continue`; declared C1 change) |
| 10 | `--abort` | `sync state at %s is unreadable: %v; inspect and remove it manually` — exit 1, deletes nothing (replaces today's `Nothing to abort — no sync in progress.` at exit 0; declared C1 change) |
| 1, 7 | `--continue` **with any trigger flag** (I20) | `cannot use sync mode flags on --continue without v2 state; continue without them or abort and start a new run` |

Placeholders bind positionally, in the order they appear in each message; the values used are the
feature name, the real failed logical entry (`payload.FailedBranch`), its worktree path
(`internal.WorktreePath(feature, payload.FailedBranch)`), a runtime file path, a PID, an RFC3339
timestamp, and a `state_version` value. In the cell-11 messages specifically the bindings are, in
order: the corrupt legacy path `<featurePath>/.sync-state.yaml`, the payload's real failed entry,
that entry's worktree path, and the payload path `<featurePath>/.sync-state.v2.yaml` — so a corrupt
legacy file never hides the live scoped record beside it. Every message is a Cobra `RunE` error
(stderr, exit 1) except the ones documented as printed on success.

**Determinacy check — every cell has a defined behaviour for all three verbs:**

| Cell | plain | `--continue` | `--abort` |
|---|---|---|---|
| 1 | today's run (frozen) | today's behaviour (frozen) without a trigger flag; I20 refusal with one | today's `Nothing to abort — no sync in progress.`; guard-only residue per §8.6 |
| 2 | table row 2 (live-guard row when live) | table row 2 (live-guard row when live) | §9.2 recovery, then `Sync state cleared.` (stale/absent guard); refused under a live guard, mutating nothing |
| 3 | unreadable-payload row | unreadable-payload row | unreadable-payload row (deletes nothing) |
| 4 | table row 4 (live-guard row when live) | table row 4 (live-guard row when live) | deletes sentinel + guard, `Sync state cleared.`; refused under a live guard |
| 5 | table row 5 (live-guard row when live) | **resumes** (stale/absent guard); refused under a live guard | payload → sentinel → guard teardown, `Sync state cleared.`; refused under a live guard |
| 6 | unreadable-payload row | unreadable-payload row | unreadable-payload row (deletes nothing) |
| 7 | today's `previous sync incomplete (failed on: %s); use --continue or --abort` | today's resume without a trigger flag; I20 refusal with one | today's abort, byte for byte |
| 8 | mixed-state row | mixed-state row | mixed-state abort row (refusal) |
| 9 | unreadable-payload row | unreadable-payload row | unreadable-payload row (deletes nothing) |
| 10 | `sync state at %s is unreadable: %v` (exit 1; today: panic) | same (exit 1; today: `nothing to continue — no sync in progress`) | `…; inspect and remove it manually` (exit 1, deletes nothing; today: `Nothing to abort — no sync in progress.`, exit 0) |
| 11 | cell-11 row | cell-11 row | cell-11 abort row (refusal) |
| 12 | unreadable-payload row | unreadable-payload row | unreadable-payload row (deletes nothing) |

Guard precedence (§8.6) overrides only rows 2, 4 and 5, and only in the live-guard direction. No
other cell has a guard-dependent message.

### 8.8 Concurrency, stated honestly

- Two concurrent **new-mode** external runs: the second is rejected by the guard (I16) **before**
  the fetch and before any state write.
- Two concurrent **no-flag** external runs behave exactly as they do today (unguarded). This is
  frozen and untouched.
- **Residual race, not closed:** a no-flag run that started *before* the sentinel was written is
  blocked by nothing, because no-flag runs take no lock and never consult the guard on the ordinary
  path.
  A new-mode run can therefore begin while a legacy run is already rebasing. Closing this would
  require the no-flag path to take or check a lock — exactly the frozen behaviour this feature must
  not change. It is documented as a known limitation of running two syncs against one feature
  concurrently, which is already unsafe today. This limitation MUST appear in the README/skill note
  (§14) and MUST NOT be described as fixed.

---

## 9. Downgrade and recovery

The reference "old binary" is **v1.2.14**, the current released tag.

### 9.1 Exact v1.2.14 behaviour against the sentinel

While the sentinel exists, with `<marker>` standing for the generated value:

- `tws sync <f>` (plain): `HasSyncState` is true → error
  `previous sync incomplete (failed on: <marker>); use --continue or --abort`, exit 1, **before any
  fetch or rebase** (`internal/cli/sync.go:56-59`).
- `tws sync <f> --continue`: `GetBranch(stack, "<marker>")` returns an empty entry →
  `failed branch "<marker>" no longer exists in stack`, exit 1, **before any Git mutation**
  (`internal/cli/sync.go:118-123`) — **no broad resume**.
- `tws sync <f> --abort`: resolves `internal.WorktreePath(feature, "<marker>")`, which cannot exist
  (the marker is a per-run nonce and a safe single path component), so the rebase abort is a no-op
  or an ignored failure; then deletes `.sync-state.yaml` and prints `Sync state cleared.`

The sentinel is deliberately kept **decodable**. A type-incompatible legacy file (e.g. `pending`
encoded as a mapping) would also fail closed on `--continue`, but it would make old plain sync
dereference a nil `*SyncState` and panic (`internal/cli/sync.go:56-58`). Fail-closed with a clean
message beats fail-closed with a crash.

**The guarantee, with its scope attached:** *while the sentinel exists*, an old plain sync and an
old `--continue` both fail closed before any Git mutation. Nothing here makes downgrades safe in
general.

### 9.2 Old `--abort` residual and the new binary's recovery obligation

Net effect of an old `--abort` against a live new-mode run: **only the sentinel is deleted.** The
payload survives, the real conflicted worktree is still mid-rebase, and nothing is rolled back.
That is cell 2 (`{absent, valid}`), and because teardown deletes the payload first, an old
`--abort` (or tampering) is the **only** way to reach it.

New-binary obligation in cell 2 — a *recovery*, never a resume. **It applies only when the run
guard is stale or absent.** When a live owning guard sits beside the payload, the original run is
still executing (an old `--abort` deletes only the sentinel, not the process), and every verb —
plain, `--continue`, and `--abort` — refuses with the live-run messages of §8.7 and touches
nothing: no rebase abort, no payload write or delete, no guard removal or reclaim. With a stale or
absent guard:

1. never resume, and never infer scope from the payload as if the run were still owned;
2. never surface the marker; read the payload and name the **real** failed logical entry and its
   worktree path (`internal.WorktreePath(feature, payload.FailedBranch)`);
3. tell the operator to abort or resolve **that actual rebase** (`git rebase --abort` in the named
   worktree, or resolve and stage there), because no tws command has done it for them;
4. state that `tws sync <f> --abort` on the new binary then cleans up safely: it aborts the real
   rebase named by the payload **if one is still in progress**, deletes the payload, removes any
   residual guard, and **does not require the sentinel to exist**;
5. reaching this cell at all requires the payload `Lstat` on the sentinel-absent path — the narrow
   declared added runtime-state-path read of §4.4.

### 9.3 Mixed state: how `real legacy + valid payload` actually arises

Two reachable sequences, neither hypothetical:

1. *Old abort, then old plain sync.* An old `--abort` leaves cell 2. The old binary now sees no
   legacy state, so a subsequent **old plain** `tws sync <f>` starts a normal broad full-stack run.
   When it conflicts it calls `saveIncompleteSync`, writing a **real** legacy `SyncState` with a
   resolvable `FailedBranch` while the earlier payload is still on disk → cell 8.
2. *Old plain sync racing new-mode setup.* External no-flag runs take no lock and do not consult the
   guard (§8.8), so an old plain run already in flight can fail and write real legacy state at any
   moment, including after a new-mode run wrote its payload → cell 8.

In both sequences the payload is a real record of a real run. A new binary MUST therefore never
treat "the legacy file looks like an ordinary failure, so this is the frozen path" as sufficient: it
stats the payload first and, when the payload is present, reports the mixed state naming **both**
failed entries rather than resuming either.

### 9.4 Downgrade after an old `--abort` is unsupported

Once an old `--abort` has run, the sentinel is gone. A subsequent **old plain** sync therefore sees
no state at all and starts a normal, broad, full-stack run against a tree that may still be
mid-rebase. (In practice the first `git rebase` usually fails loudly because a rebase is already in
progress — that is luck, not a designed guarantee.)

This is handled by being **tested and documented**, not by a safety claim:

- the test asserts the observable facts — "old abort leaves the payload, leaves the real rebase, and
  removes the sentinel", and "the following old plain sync is no longer blocked" (AC 31);
- a release-note / README line states that downgrading after an explicit old `--abort` is
  unsupported;
- the implementation MUST print an explicit warning when it detects this shape. Cell 2's message
  (§8.7) is that warning; it names the real failed entry and the unaborted rebase.

The mechanism cannot make an old `--abort` understand a payload format that postdates it, because
that code path is already shipped. Any claim of general downgrade safety is false and MUST NOT
appear in code comments, help text, docs, or the CHANGELOG.

### 9.5 Cleanup rules summary

| Residue | Cleared by | Requires |
|---|---|---|
| stale guard only | next new-mode run (silently), or manual deletion | stale PID, unchanged bytes; `--abort` does **not** clear it (§8.6 guard-precedence) |
| sentinel only (cell 4) | `--abort` | stale/absent guard, payload verified absent at abort time |
| sentinel + payload (cell 5) | `--abort` | stale/absent guard; deletes payload → sentinel → guard |
| payload only (cell 2) | `--abort` | **stale/absent guard** (a live owning guard refuses and mutates nothing); aborts the real rebase if in progress; sentinel not required |
| any unreadable payload | **manual only** | tws never deletes state it could not read |
| corrupt legacy only (cell 10) | **manual only** | tws never deletes state it could not read; `--abort` now refuses with exit 1 instead of reporting "nothing to abort" (declared C1 change, §8.6 row 10) |
| corrupt legacy + valid payload (cell 11) | **manual only** | two explicit operator decisions; the payload's real rebase is resolved or aborted first |
| mixed (cell 8) | **manual only** | two explicit operator decisions |

### 9.6 How the downgrade tests obtain a prior binary

Requirement: no dependency on network access or on tag availability at test time.

1. **Preferred — real prior binary.** If `TWS_DOWNGRADE_BINARY` is set and executable, use it. The
   engineering workflow's release step builds and keeps such binaries; CI may export one.
2. **Second — build from the local tag, offline.** If `git rev-parse -q --verify refs/tags/v1.2.14`
   succeeds, create a detached worktree at that tag under `t.TempDir()` and run
   `go build -o <tmp>/tws-v1.2.14 ./cmd/tws` with `GOFLAGS=-mod=mod` and `GOPROXY=off`. The module
   graph is two direct dependencies (`gopkg.in/yaml.v3`, `github.com/spf13/cobra`) already present
   in the module cache after any local build, so this succeeds offline. If it fails for any reason,
   fall through.
3. **Always — the frozen legacy replay harness.** `legacySyncHarness` in the test package
   transcribes v1.2.14's three state-handling paths verbatim:
   - `legacyPlainSync`: `HasSyncState` → `LoadSyncState` → the exact error string;
   - `legacyContinue`: `LoadSyncState` → worktree-path rebase-in-progress check →
     `LoadStack` → `GetBranch(stack, FailedBranch)` → `failed branch %q no longer exists in stack`;
   - `legacyAbort`: `LoadSyncState` → `WorktreePath(feature, FailedBranch)` rebase abort →
     `DeleteSyncState` → `Sync state cleared.`

   The harness is **proven equivalent** by a fidelity test that runs both the harness and the real
   binary (when available) over the same fixtures and asserts identical observable outcomes; when no
   binary is available the fidelity test is skipped with an explicit `t.Log`, and the harness-based
   assertions still run. Every downgrade acceptance criterion (AC 27–AC 32) runs against whichever
   of paths 1–3 is available, in that order, and **never** skips entirely.

---

## 10. Checkout transaction extension

### 10.1 Options

`internal.CheckoutSyncOpts` gains the frozen decision and the presence map:

```go
type CheckoutSyncOpts struct {
    Feature     string
    FeaturePath string
    RepoDir     string
    Push        bool
    TestCommand string
    Verbose     bool

    // added
    Policy    SyncRunPolicy        // axes; zero value ⇒ legacy defaults (no-fetch × full × all)
    NewMode   bool                 // any trigger flag was Changed
    Continue  bool                 // --continue was supplied; read by runCheckoutSync for the
                                   // I20 check (§10.5 rule 0) and by AbortCheckoutSync for the
                                   // deferred I7 check (§10.5 rule 8)
    Changed   map[string]bool      // "fetch","no-fetch","full","local-only","only","from","push"
}
```

`runCheckoutSync` (`internal/cli/checkout_sync.go:10-56`) takes the options struct instead of its
current positional parameters; its behaviour for a zero `Policy` and `NewMode == false` is
unchanged.

### 10.2 Additive transaction fields and state version

```go
type CheckoutTransaction struct {
    StateVersion int `yaml:"state_version,omitempty"` // added; absent ⇒ 1 (legacy)
    // ... every existing field, unchanged, in its existing order ...

    // added, all omitempty so a legacy transaction loaded from disk round-trips
    // without gaining any of these keys
    FetchPolicy       string   `yaml:"fetch_policy,omitempty"`
    PropagationPolicy string   `yaml:"propagation_policy,omitempty"`
    ScopeKind         string   `yaml:"scope_kind,omitempty"`
    ScopeSelector     string   `yaml:"scope_selector,omitempty"`
    Selected          []string `yaml:"selected,omitempty"`      // logical Names, plan order
    ValidationSource  string   `yaml:"validation_source,omitempty"`
}
```

- A **no-flag** checkout run writes `state_version: 0` — i.e. the key is omitted — and none of the
  new keys. Its transaction file is therefore identical to today's in key set, key order, and every
  value, **except** for the additive per-plan-entry `name:` key of C5. AC 6 asserts exactly that:
  semantic equivalence after removing the `name` key, not raw byte identity.
- A **new-mode** checkout run writes `state_version: 2` and the policy keys.
- A new binary reading `state_version > CheckoutTransactionVersion` refuses:
  `checkout sync transaction state version %d is newer than %d; upgrade tws or remove %s`.
  This is forward-only protection (D7).
- An **old binary** reading a v2 transaction stays bounded by the persisted `tx.Plan` — it cannot
  broaden the run — but it **ignores** the policy keys and may re-resolve bases with old rules for
  the entries in the plan. This is *bounded, not perfect*, and MUST be documented as such
  (§14). Persisting resolved SHAs (`NewBaseSHA`, `LastBaseSHA`, `PreSHA`) — which the plan already
  does — is what keeps the bound tight.

### 10.3 Preflight ownership, scoped plan, fetch boundary, and persistence before mutation

**Binding order inside `RunCheckoutSync(opts)`.** Steps 1–8 mutate nothing at all; step 9 is the
first side effect of any checkout run. Before step 1, and outside this function, the CLI wrapper
`runCheckoutSync` has already resolved the workspace and the feature path and has then issued the
single read-only containment probe `git -C <cwd> rev-parse --show-toplevel`, resolving
`opts.RepoDir = ws.RepoRoot` (§10.9); that probe is the one added read-only argv record declared in
§4.1 rule 6a and is the only Git invocation of a checkout run that did not exist before this
feature. It is emitted **after** every workspace/feature-resolution Git read (`RequireWorkspace`'s
`rev-parse --git-common-dir` and anything `RequireFeaturePath` reads) and **immediately before**
step 2's `rev-parse --git-path rebase-merge`, which is the first Git record of this function.

1. `HasCheckoutTransaction(opts.FeaturePath)` → `checkout sync transaction already exists; use --continue or --abort`.
2. `gitOperationInProgress(opts.RepoDir)` → `another Git operation is in progress; complete or abort it before checkout sync`.
3. `gitWorkingTreeDirty(opts.RepoDir)` → `check working tree: %w` / `working tree is dirty; commit or stash changes before checkout sync`.
4. `gitCurrentBranch(opts.RepoDir)` → `cannot sync from detached HEAD: %w`.
5. `gitResolveRef(opts.RepoDir, "HEAD")` → the original HEAD, error returned verbatim.

   Steps 1–5 are today's read-only pre-flight (`internal/checkout_sync.go:499-521`), unchanged in
   order, messages, and Git commands, for no-flag and new-mode runs alike.

6. **New-mode only — I9.** `stack, err := LoadStack(opts.FeaturePath)`. On error return exactly
   `sync modes require a stack; feature %q has no readable stack.yaml` (§3.5), with `%q` = the
   feature name. It is **not** wrapped in today's `load stack: %w`, and the underlying read/decode
   error is deliberately not interpolated, so both workspace modes emit one identical I9 string.
7. **New-mode only — I10–I13.**
   `sel, err := ResolveSyncSelection(stack, opts.Policy, SyncSelectionOpts{Mode: ModeCheckout, NewMode: true, Feature: opts.Feature})`.
   Any error is returned **verbatim and unwrapped**, so the I10/I11/I12/I13 strings of §3.5 reach
   the user exactly as `ResolveSyncSelection` produced them (§5.2 owns them; nothing here re-checks
   or re-phrases them). `opts.Feature` is the already-resolved feature name of §10.1, so the I10/I11
   strings are byte-identical to external mode's for the same stack and selector.
8. **New-mode only — I14**, evaluated only when `opts.Policy.Fetch == SyncFetchDisabled` (checkout's
   default, and the only cell whose input refs cannot be refreshed by this run). For every base ref
   the selected plan will actually use — the entry's literal `Base` for anchors under `full`, the
   parent's `GitBranch()` for propagation edges, and nothing for anchors under `local-only` (§6.2) —
   run `internal.VerifyGitRef` (`git rev-parse --verify <ref>^{commit}`) in `opts.RepoDir`. The
   first failure returns exactly
   `base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first`
   (§3.5), verbatim and unwrapped, in selection order so the message is deterministic. Under
   `--fetch` this preflight is **skipped**, because the fetch may create the ref; an unresolvable
   base then remains today's `BuildCheckoutPlan` failure (`build plan: %w`) after the fetch.

   Steps 6–8 are read-only: `LoadStack` and `ResolveSyncSelection` touch no Git state at all
   (§5.2), and `VerifyGitRef` is a local ref read. On any failure in 6–8 the function returns
   **before** `AcquireCheckoutLock`, so **no lock file, no transaction, no fetch, no `git checkout`,
   no header, and no write of any kind exists** — the exact counterpart of external's §3.6 ordering,
   where I9/I14 are steps 10–11 and the first side effect is step 13.

9. `AcquireCheckoutLock(opts.FeaturePath)` — unchanged (§10.7). **First side effect.**
10. **New-mode only:** print the §3.7 header, exactly once.
11. **New-mode `--fetch` only:** the pre-plan remote refresh defined immediately below.
12. `BuildCheckoutPlan(opts.RepoDir, stack, sel)` — using the **preloaded** stack of step 6 and the
    **preresolved** selection of step 7. `RunCheckoutSync` MUST NOT call `LoadStack` or
    `ResolveSyncSelection` a second time and MUST NOT re-check I9–I14: there is exactly one
    validator truth per rule. On the **no-flag** path nothing moves: the stack is loaded here,
    after the lock, with today's `load stack: %w`, and plan failures keep today's `build plan: %w`
    (`internal/checkout_sync.go:528-540`) — frozen.
12b. **New-mode `local-only` only:** `printLocalOnlyNoOp(sel, plan)` — the third and last print path
    package `internal` gains (§3.10). One `  [-] NAME (no in-stack parent edge to propagate)` line
    per selected anchor in topological order, then `Nothing to propagate.` **only** when the plan is
    empty. It is owned by `RunCheckoutSync`, never by the CLI and never by a callback.
13. Empty plan → release the lock and return nil.
14. Create and persist the transaction (`state_version: 2` plus the policy keys on new-mode runs).
15. `executeTransaction` — the first `git checkout`, rebase, index, or local-branch mutation.

**Explicit `--fetch` in checkout — boundary, binding (step 11).**

- **Position.** Best-effort, pre-plan remote-ref refresh, performed after every read-only guard
  (1–5), after the complete new-mode validation and I9–I14 preflight (6–8), after the lock (9) and
  the header (10), and **before** `BuildCheckoutPlan` (12) and before transaction creation (14).
  Exactly one `git fetch` in `opts.RepoDir`, with the same tolerance (§6.4) and the same
  `Fetching <label>... ` + `done`/`failed` bytes as external, `<label>` = `default repo`.
- **Mutation surface.** It may mutate **only** remote-tracking refs (`refs/remotes/*`) and the
  object store. It MUST NOT touch the index, the working tree, any local branch, `HEAD`,
  `stack.yaml`, the transaction, or the lock, and MUST NOT add `--prune`, `--tags`, `--all`, or any
  refspec beyond the remote's configured default.
- **Persistence claim, exact.** The frozen policy/scope reaches disk at step 14 — i.e. **before the
  first checkout, rebase, index, or local-branch mutation**, which is what §10.2/§12 mean by
  "persisted before mutation". It is **not** persisted before the fetch, and no statement anywhere
  in this document may claim otherwise.
- **Interruption.** If the process dies inside this window, **no transaction exists**. At most the
  remote-tracking refs were refreshed, the header was printed, and a lock file remains with a dead
  PID and no transaction — which `AcquireCheckoutLock` already reclaims silently
  (`internal/checkout_sync.go:191-205`). The next invocation therefore starts **fresh** and
  re-fetches. A remote-ref refresh is idempotent and converges, so it is safe to repeat and is
  **deliberately not resumable**: no resume point, no checkpoint, and **no new checkout stage** is
  introduced for it; `planned → switched → rebasing → (conflict) → rebased → validating →
  completed → restoring` is unchanged.
- **Output without state is possible, and declared.** Because the header (10) precedes the fetch
  (11), a crash in this window can leave the header — and possibly `Fetching default repo... ` — on
  stdout with nothing on disk to continue or abort. This is documented (§14) and tested (AC 57).

**Plan and persistence.**

- `BuildCheckoutPlan(repoDir string, stack Stack, sel SyncSelection) ([]CheckoutPlanEntry, error)`.
  With a zero `sel` it behaves exactly as today. With a selection it plans only `sel.Entries`,
  preserving `TopoSort` order and every existing skip rule (`entry.Archived`, empty `Base`), and
  under `local-only` it excludes anchors entirely.
- Plan identity gains `Name` (§7.4).
- The transaction is persisted **before** the first `git checkout` — unchanged
  (`internal/checkout_sync.go:559-566`) — and the new fields are part of that first write, so the
  frozen decision is on disk before any checkout, rebase, index, or local-branch mutation.
- An empty scoped plan releases the lock and returns nil, and the CLI prints
  `Checkout sync complete.` — with the `local-only` no-op block of §3.7 printed first, by
  `RunCheckoutSync` through `printLocalOnlyNoOp` at step 12b (§3.10 path 3).

### 10.4 Push, test, and options frozen

- `Push`, `TestCommand`, and `ValidationSource` are frozen into the transaction at creation, as
  `Push`/`TestCommand` already are, and re-read on `--continue`, as they already are.
- Selected push at finalization iterates `tx.Plan` (already scoped) and uses `pe.Branch`
  (`GitBranch()`), which is already correct. Only plan entries that completed are pushed: the push
  loop runs after every plan entry has been processed, so "completed" and "in the plan" coincide;
  when a push fails mid-way the transaction is kept at `StageCompleted` for a `--continue` retry,
  unchanged.

### 10.5 `Flags().Changed` mismatch rules on `--continue` / `--abort` (D8, M9)

Let `tx.StateVersion >= 2` mean "the transaction was created by a new-mode run".

0. **Trigger flags on `--continue` require v2 state (I20), evaluated first.** When `--continue` is
   supplied together with any trigger flag of §3.3, `runCheckoutSync` loads the transaction (the
   read `ContinueCheckoutSync` performs anyway) and refuses, before `forceAcquireCheckoutLock` and
   before any Git call, whenever **no** transaction exists or the loaded transaction has
   `StateVersion < 2`:
   `cannot use sync mode flags on --continue without v2 state; continue without them or abort and start a new run`
   — the same exact string external mode uses. This refusal takes precedence over the
   missing-transaction and legacy-resume paths, because the supplied scope flags would otherwise be
   silently dropped and the resume would proceed over the transaction's full persisted plan. It
   requires a trigger flag, so a plain `--continue` against an absent or legacy transaction is
   untouched (§4.3 item 12), and rules 1–5 below are only ever reached once the state is known to
   be v2 or the invocation carries no trigger flag.
1. `--continue` with **no** trigger flag and **no** `--push` flag: use the persisted values
   verbatim. A plain `--continue` is **never** rejected merely because persisted push is true — the
   persisted value simply wins, exactly as `opts.Push = tx.Push` does today.
2. `--continue` with flags **explicitly supplied and matching** persisted values: accept
   (idempotent, script-friendly).
3. `--continue` with flags **explicitly supplied and conflicting**: refuse before any Git call,
   naming both values:
   `cannot change %s on --continue: the run was started with %s=%s and this invocation requests %s`.
4. **Legacy transactions (`StateVersion < 2`)** keep today's one-way push rule byte for byte:
   only `opts.Push && !tx.Push` is rejected, with the exact existing string
   `cannot add --push to an existing transaction that was started without it; persisted push=%v wins`.
   The opposite direction still silently loses to the persisted value. Nothing else about a legacy
   transaction's resume changes.
5. **New-mode transactions (`StateVersion >= 2`)** get a **symmetric** rule: an explicitly supplied
   `--push` **or** `--push=false` that disagrees with `tx.Push` is refused with the message of rule
   3 (`%s` = `push`). This is a declared new-mode behaviour change (D8), applied **only** when
   `Changed("push")` is true and **only** to v2 transactions.
6. Any trigger flag explicitly supplied together with `--abort` and **without** `--continue` is
   refused by I8, in both modes,
   for every transaction version, because abort is defined by the persisted run and not by the
   current command line. When `--continue` is supplied as well, I7 fires first and I8 is not
   evaluated (§3.5, "I7 before I8").
7. A `selected` name that no longer exists in `stack.yaml` on `--continue` refuses:
   `selected stack entry %q no longer exists in stack; use --abort`.
8. **`--continue` together with `--abort`** (I7): when a trigger flag is `Changed`, the invocation
   was already refused at §3.6 step 3 with the I7 string — **not** with I8, which is skipped for
   that input (§3.5, "I7 before I8"). Without a trigger flag the check is deferred to the abort
   path, which already loads the transaction: `runCheckoutSync` passes `cont` through in the
   options, and `AbortCheckoutSync` refuses with the I7 message **only** when the loaded
   transaction has `StateVersion >= 2`. With a legacy transaction, or with no transaction at all,
   today's behaviour is preserved exactly — `--abort` wins and `--continue` is ignored
   (`internal/cli/checkout_sync.go:30-43`) — and **no additional file is read** on that path.

Rules 0–5 and 7 apply verbatim to external `--continue` against a v2 payload (§8.6 row 5), with
the payload's `push`, `fetch_policy`, `propagation_policy`, `scope_kind`, and `scope_selector` as
the persisted side; rule 0's external half is the I20 refusal of cells 1 and 7, ordered at §3.6
step 9. External `--continue` against **legacy** state keeps today's behaviour exactly when no
trigger flag is supplied: `--push` is taken from the current invocation and is not persisted.

### 10.6 Old-transaction and old-binary caveats

- A transaction with `pe.Name == ""` (written by an older binary) uses the `GitBranch()` first-match
  fallback for `LastBaseSHA` attribution (§7.4). No migration is written, no file is rewritten.
- An old binary resuming a v2 transaction is **bounded by the plan** and cannot broaden the run,
  but ignores the policy keys. Documented (§14) as: *do not resume a scoped checkout sync with an
  older tws; abort it instead.*
- No checkout downgrade sentinel is introduced. Checkout does not need one: `resumeTransaction` is
  driven by the persisted plan and index, so the failure mode an external sentinel prevents does not
  exist there. This asymmetry is deliberate and is stated in the docs.

### 10.7 Lock, reclaim, rollback, and original branch

All unchanged: `AcquireCheckoutLock` for fresh runs (never steals a live lock, refuses a stale lock
with a transaction), `forceAcquireCheckoutLock` for `--continue`/`--abort`, `restoreOriginal` on
abort and finalization, `Checkout sync aborted, original branch restored.` The only interaction with
scope is the free safety win of §7.4 (an out-of-scope original branch becomes the HEAD-asserted
`!inPlan` case).

### 10.8 Guards

Dirty tree, detached HEAD, in-progress Git operation, and existing transaction remain **rejections**
with their exact existing messages, for no-flag and new-mode runs alike. No auto-stash is added.
External gains none of these guards, for either kind of run (M15, §12 item 8).

### 10.9 `RepoDir` and cwd (D9)

Decision: **`RepoDir` becomes `ws.RepoRoot`**, plus an explicit containment refusal.

```go
top, err := internal.GitRepoRootIn(cwd)   // git rev-parse --show-toplevel
if err != nil || filepath.Clean(top) != filepath.Clean(ws.RepoRoot) {
    return fmt.Errorf("checkout sync operates on %s but the current directory belongs to working tree %s; run it from the repository checkout", ws.RepoRoot, top)
}
opts.RepoDir = ws.RepoRoot
```

Justification, and why this is blocking scope rather than a follow-up: D9 defers the cwd fix
*unless the invocation matrix fails*. It fails today in exactly this cell — a cwd inside a linked
worktree of the same repository resolves `os.Getwd()` as `RepoDir` and drives `git checkout` against
**that worktree** instead of the single checkout, mutating the wrong tree
(`internal/cli/checkout_sync.go:16-20`). The refusal is a clean exit-1 error where today the outcome
is silent corruption; supported cwds (repo root and any subdirectory) resolve
to the same toplevel and are unaffected. The `err != nil` arm (cwd outside any repository) is
**defensive only** and no behaviour change is claimed for it: checkout mode is reached only after
`internal.RequireWorkspace()` has returned `ModeCheckout` (`internal/cli/sync.go:30-36`), and from a
cwd outside any repository `RequireWorkspace` resolves an **external** workspace through
`DetectWorkspaceRoot` or fails with `not inside a git repository or tws workspace` — either way
before checkout dispatch. That arm therefore has no matrix cell, no golden, and no declared-change
evidence; it exists so the containment check is total rather than partial.
The refusal applies to **every** checkout run, no-flag included; cell 9 is therefore declared
behavioural exception 2 (§4.1 rule 5, §4.3 item 1, §4.5 C4), covered by AC 46. The probe itself is
one **added read-only** Git invocation on every checkout run, frozen cells included, issued after
workspace and feature-path resolution and immediately before `RunCheckoutSync`'s first preflight
Git call (§10.3); it is declared
in §4.1 rule 6a, and AC 2 asserts it is exactly one record, `git -C <cwd> rev-parse --show-toplevel`,
that exits zero and whose output equals `ws.RepoRoot`.

---

## 11. Agent status and import compatibility

Both changes are **compatibility adaptations forced by writing new runtime state into a directory
two other production consumers already read or filter**. Neither is a redesign, and §2 item 8 stays
in force.

### 11.1 `BuildAgentStatus` — marker-aware, read-only projection (D19)

**The shared classifier.** Exactly one read-only external-state classifier exists, in package
`internal`, in `internal/sync_run_state.go`:

```go
// SyncStateCell is one cell of the §8.6 matrix, 1-12.
type SyncStateCell int

// SyncExternalState is the decoded, read-only view of a feature's external sync
// state. It performs no mutation, takes no guard, and repairs nothing.
type SyncExternalState struct {
    Cell       SyncStateCell // 1..12, per §8.6
    Legacy     *SyncState    // decoded legacy file; nil when absent or unreadable
    LegacyErr  error         // decode error for the legacy file, if any
    Marker     string        // sentinel marker value when the legacy file is a sentinel
    Payload    *SyncRunState // decoded v2 payload; nil when absent or unreadable
    PayloadErr error         // decode error / unsupported version for the payload
    Guard      *SyncRunGuard // decoded guard; nil when absent or unreadable
    GuardLive  bool          // syncProcessAlive(Guard.PID) && token matches the payload when both exist
    GuardErr   error
}

// SyncClassifyOpts controls only *when the guard file is opened*. It never
// changes the returned cell: the guard is precedence and context, not an axis.
type SyncClassifyOpts struct {
    // AlwaysReadGuard opens .sync-run.lock unconditionally. tws status and
    // new-mode sync runs pass true. A no-flag sync run passes false, so the
    // guard is opened only when the legacy file decoded to a sentinel or a
    // payload was found — exactly §3.6 step 8c, which keeps the payload Lstat
    // the only added runtime-state-path read on an ordinary no-flag run.
    // (C4's two read-only Git probes are declared separately, §4.1 rule 6.)
    AlwaysReadGuard bool
}

// ClassifyExternalSyncState reads the legacy path, the payload, and (per opts)
// the guard, and returns the §8.6 cell plus every decoded value. It is
// read-only and never returns an error for "absent"; absence is expressed by
// the cell.
func ClassifyExternalSyncState(featurePath string, opts SyncClassifyOpts) SyncExternalState
```

Both consumers use it and neither re-implements it:

- `internal/cli/sync_modes.go`'s `classifySyncState(featurePath string, newMode bool)` is a thin
  wrapper over it, passing `AlwaysReadGuard: newMode`, applying the symlink scoping of §3.6 step 8,
  the deferred I7 of step 8e, the I20 refusal of step 9, and the message table of §8.7;
- `buildFeatureSync` (`internal/agent_status.go:1354,1408-1440`) calls it with
  `AlwaysReadGuard: true` as its **first** external action, because status must be able to report
  guard-only residue.

**Call ordering in `buildFeatureSync`, binding.** After the checkout branch returns, and **before**
the `os.Stat(statePath)` early return and the `LoadSyncState` legacy block
(`internal/agent_status.go:1408-1412`), `buildFeatureSync` MUST call
`internal.ClassifyExternalSyncState(featurePath, SyncClassifyOpts{AlwaysReadGuard: true})` and
dispatch on the returned cell. Today's
`if _, err := os.Stat(statePath); err != nil { return nil, nil }` short-circuits on the legacy path
alone, which would make `{absent, valid}` (cell 2), `{absent, unreadable}` (cell 3), guard-only
residue, and every marker cell invisible to status. Dispatch:

| Classifier result | `buildFeatureSync` behaviour |
|---|---|
| cell 1 `{absent, absent}`, no guard file | `return nil, nil` — exactly today |
| cell 1 `{absent, absent}` + guard | project the guard: `IssueSyncInProgress`/`SeverityInfo` when live, `IssueSyncStale`/`SeverityWarning` when stale, with `Liveness`, `LockPID`, `LockLive` populated |
| cell 7 `{real legacy, absent}` | **delegate to today's projection unchanged** — the existing `LoadSyncState` block, the existing issue codes, the committed goldens |
| cell 10 `{unreadable legacy, absent}` | today's `IssueSyncStateInvalid` branch, unchanged |
| cells 2, 4, 5, under a **live owning** guard | the live-guard projection of rule 4 below |
| cells 4, 5 (sentinel), guard stale or absent | the marker-aware projection of rules 1–3 and 5 below |
| cells 3, 6, 8, 9, 11, 12, and cell 2 with a stale or absent guard | the degenerate-cell table of rule 5 below |

`buildFeatureSync` MUST NOT call or branch on `isSyncMarker` — it is not a caller of that predicate
at all; the marker predicate lives inside the classifier, whose sole caller of it is
`ClassifyExternalSyncState` (§8.2), and the projection branches only on
`SyncExternalState.Cell` and the classifier's decoded fields.

Binding rules:

1. **Projection source.** Use the classifier's decoded `Payload` (never a second read of
   `<featurePath>/.sync-state.v2.yaml`). Project the **real**
   `failed_branch` (a logical `StackEntry.Name`), the real `pending` and `completed` arrays, and
   `skipped: []`. Construct the returned `*SyncState` from those real names so that
   `attributeSyncBranch` (`internal/agent_status.go:1324-1348`) and `syncWantsBranch`
   (`internal/agent_status.go:1629-1645`) keep working **unchanged** — no second attribution rule is
   introduced, `sync-failed-branch` lands on the real entry, and a dirty worktree the scoped run
   needs is upgraded to `worktree-dirty-blocking`.
2. **The marker is never exposed.** It MUST NOT appear in `failed_branch`, in any issue detail or
   hint, in rendered output, or in JSON — and MUST NOT be attributed to any entry as a branch.
3. **Liveness from the guard.** Use the classifier's `Guard`/`GuardLive` (never a second read of
   `.sync-run.lock`) and set `Liveness` to `live` / `stale` /
   `invalid` and `LockPID` / `LockLive` accordingly, using the same live/stale discrimination the
   rest of the feature uses. These are **existing nullable keys** on `AgentStatusFeatureSync`
   (`internal/agent_status.go:294-306`) that are simply populated for external state for the first
   time.
4. **Live-guard precedence.** A live owning guard **dominates** transient sentinel/payload presence.
   `{sentinel, absent}` and `{sentinel, valid}` occur inside the normal lifetime of a healthy
   run — the first is exactly the setup window and the teardown window, the second the steady
   state — and `{absent, valid}` (cell 2) occurs whenever an old `--abort` deletes the sentinel out
   from under a run that is still executing. While the guard's PID is alive and its token matches
   the payload, status MUST project an **in-progress scoped sync** owned by that
   PID (`IssueSyncInProgress`, `SeverityInfo`, feature scope) for **all three** of those cells and
   MUST NOT emit a degenerate warning. Emitting "interrupted" against a healthy start-up or
   shut-down, or "record survives without its state file" against a live run, is a false alarm on
   the most frequently sampled states.
5. **Degenerate cells, only when the guard is stale or absent**, mapped to **existing** issue codes:

   | Cell | Code | Severity | Wording constraint |
   |---|---|---|---|
   | `{sentinel, absent}` | `IssueSyncStale` | `SeverityWarning` | states the initialization-or-finalization ambiguity; MUST NOT claim no work was done; hint `run: tws sync %s --abort` |
   | `{sentinel, valid}` | `IssueSyncStale` | `SeverityWarning` | names the real failed entry; hint `run: tws sync %s --continue  or  tws sync %s --abort` |
   | `{absent, valid}` | `IssueSyncInvalid` | `SeverityWarning` | names the real failed entry and carries the §9.2 recovery guidance |
   | `{real legacy, valid}` | `IssueSyncInvalid` | `SeverityWarning` | names **both** the legacy failed entry and the payload's real failed entry |
   | `{unreadable legacy, valid}` (cell 11) | `IssueSyncStateInvalid` | `SeverityWarning` | names the corrupt legacy path **and** the payload's real failed entry; wording mirrors the §8.7 cell-11 message and MUST NOT suggest either file be deleted automatically |
   | guard-only residue (`{absent, absent}` + stale guard) | `IssueSyncStale` | `SeverityWarning` | names the guard path and its PID; hint states that the next scoped sync reclaims it, or that the file can be removed — it MUST NOT hint `--abort`, which does not clear this residue |
   | payload unreadable / unknown version | `IssueSyncStateInvalid` | `SeverityWarning` | worded as "payload unreadable"; may fire **even under a live guard**, because it is a durable fact about bytes, not a timing artefact — and MUST NOT claim the run is dead or abandoned |
   | `{real legacy, absent}` | today's codes, unchanged | unchanged | legacy goldens MUST NOT be re-baselined |

   **No new issue code is added.** The closed enums in `internal/agent_status.go:82-131` are
   unchanged.
6. **No JSON schema change.** No new keys and no new enum values are introduced;
   `AgentStatusFeatureSync` is reused as it stands, and `agentStatusSchema` stays **1**
   (`internal/agent_status.go:19`). The schema-version rule, stated normatively: *populating an
   existing nullable key with an existing enum value is additive and does not bump
   `schema_version`; adding a key or an enum value does.* This feature does neither.
7. **Status remains strictly read-only.** `ClassifyExternalSyncState` opens files read-only and
   returns decoded values; neither it nor `buildFeatureSync` deletes, rewrites, reclaims, or repairs
   the sentinel, the payload, or the guard, and neither ever takes the guard, refuses on a symlink,
   or writes anything. Exactly one mutating authority — the sync command — is preserved. AC 44
   asserts the bytes of all three files are unchanged across a status run.
8. **Test determinism.** Live/stale MUST come from a controlled seam — a guard file written with the
   test's own PID for `live` and with a PID asserted dead for `stale`, plus a substitutable liveness
   predicate — never from sleeping, racing a real sync, or hard-killing a process.

### 11.2 `tws import` — runtime-state filtering (D20)

`isRuntimeState` (`internal/cli/importcmd.go:173-179`) becomes:

```go
func isRuntimeState(path string) bool {
    normalized := filepath.ToSlash(path)
    return strings.HasPrefix(normalized, ".tws/state/") ||
        normalized == ".tws/state" ||
        normalized == ".sync-state.yaml" ||
        normalized == ".sync-state.v2.yaml" ||
        normalized == ".sync-run.lock"
}
```

Exact names, matching the existing exact-name style for feature-directory entries. No prefix
matching is introduced, so no user file can be filtered accidentally; any future runtime file MUST
be added here explicitly, and that obligation is stated in a code comment.

Why this is inside the boundary, concretely:

- An imported archive could otherwise **plant foreign live state** — a payload naming entries and
  worktrees from another machine, or a guard naming a PID that happens to be alive locally.
- With the payload `Lstat` of §4.4 in place, planted state is not inert: it makes the very next plain
  `tws sync <feature>` refuse, in a feature the operator just imported and has never synced.
- It would make the cell-2 and cell-8 states reachable without any new-mode run ever having happened
  locally, breaking the "unreachable in a repository that never used new modes" property that
  justifies the narrow exception.

**Export is unaffected and needs no change.** `exportTarball` is allow-listed by construction to
`workspace.yaml` plus paths under `inject/` (`internal/cli/export.go:146-168`), so no runtime file
can enter an archive this tool produces. One cheap assertion is added anyway as insurance against
that allow-list regressing (AC 45).

---

## 12. Safety and security

1. **No hidden network.** Under `no-fetch` (and therefore under every default checkout run) the run
   MUST issue zero `fetch` / `ls-remote` / implicit remote-probe commands. `git push` is not covered
   by that promise: it is legal, explicit, and opt-in via `--push`. The strong "zero network at all"
   property holds for any `no-fetch` run **without** `--push`, and that is what AC 14 asserts by
   pointing `origin` at a removed path. Checkout's `--fetch` is the one explicit opt-in on the input
   side: exactly one `git fetch`, in a bounded pre-plan window (§10.3), never implicit.
2. **No unrelated mutation.** A scoped run MUST leave every unselected branch SHA, every unselected
   `last_base_sha`, and every remote ref untouched. The two known leaks are closed: `--update-refs`
   is dropped for scoped runs (§7.1), and although `SaveStack` still rewrites the whole file, only
   selected entries' values may change (§7.3). AC 19 and AC 21 assert this with full before/after
   snapshots of `git for-each-ref` and of `stack.yaml`. The single deliberate exception is the
   explicit `--fetch` refresh, in either mode, which updates remote-tracking refs and nothing else;
   in checkout mode it runs before any transaction exists, so an interruption there leaves no
   local mutation and nothing to resume (§10.3).
3. **Force-with-lease only.** Both push paths already comply
   (`internal/cli/push.go:70`, `internal/checkout_sync.go:426`). Selected push MUST NOT introduce a
   plain `--force`. A grep gate asserts no `--force` without `-with-lease` exists in the repository
   (AC 51).
4. **Path containment.** The marker is a safe single path component (§8.2 property 1), so the old
   `--abort` worktree-path interpolation cannot escape the feature directory. All three runtime
   paths are constructed with `filepath.Join(featurePath, <constant>)` and never from user input.
5. **Permissions.** Payload and guard `0600`; created directories `0700`; the sentinel `0644` at the
   legacy path for old-binary shape compatibility (§8.1).
6. **Ownership token.** The guard carries 16 random bytes echoed in the payload; a mismatch is
   reported and never silently reclaimed (§8.3 rule 5), so a planted or foreign guard is diagnosable
   rather than authoritative.
7. **Symlink refusal, scoped to new-mode state paths.** Before writing or trusting
   `.sync-state.v2.yaml` or `.sync-run.lock`, and before writing or trusting `.sync-state.yaml` on a
   new-mode run or on any run handling a sentinel/payload/guard, `os.Lstat` the path; a symlink is
   refused (I18). This prevents a planted symlink from redirecting a `0600` write outside the
   feature directory. **Declared legacy limitation:** an ordinary legacy `.sync-state.yaml` that is
   a symlink, with no payload beside it, on a run with no trigger flag, is still **followed**,
   exactly as today. Refusing it would change frozen no-flag behaviour (§4.1) and is therefore not
   done here; it is recorded as a known legacy safety limitation (§18 item 9), not as a fix.
8. **No auto-stash, no reset, no destructive cleanup.** Checkout keeps rejecting dirty and detached
   states; external keeps *not* rejecting them (M15) — adding those guards to external would be a
   breaking change for no-flag runs, and adding them only for scoped runs would make the modes
   inconsistent in a way no user asked for. External relies on the existing per-worktree branch
   check.
9. **Concurrency.** New-mode external runs are serialized by the `O_EXCL` guard; checkout runs by
   the existing PID lock. No-flag external concurrency is unchanged and its residual race is
   documented, not silently "fixed" (§8.8).
10. **Failure and crash injection.** Every stage boundary in both modes is covered:
    `StepHook` for checkout (`internal/checkout_sync.go:298-303`) and a new equivalent
    `SyncStepHook func(stage SyncRunStage, index int) error` for external new-mode runs, called at
    the six ordering points of §8.5. The hook is nil in production and MUST NOT be reachable from
    the CLI.
11. **Supported cwd matrix**, per the v1.2.7 retrospective
    (`docs/retrospectives/v1.2.7-upgrade-operations.md:50-58`) — every cell is required coverage
    (AC 46):

    | # | cwd | Mode | Expected |
    |---|---|---|---|
    | 1 | source repository root | external | works |
    | 2 | linked worktree root | external | works |
    | 3 | nested subdirectory of a linked worktree | external | works |
    | 4 | external workspace root | external | works |
    | 5 | external feature directory | external | works (requires the C4 `syncFeature` path fix; **declared no-flag change** where the derivations disagree, §4.1 rule 5) |
    | 6 | nested `docs/`/`inject/` subdirectory of a feature directory | external | works (same fix, same declared no-flag change as cell 5) |
    | 7 | checkout repository root | checkout | works |
    | 8 | nested subdirectory of the checkout repository | checkout | works |
    | 9 | linked worktree of the checkout repository | checkout | **refused** with the I19 message (today: silently mutates the wrong tree); **declared no-flag change**, §4.1 rule 5 |

    There is deliberately **no** "outside any repository, checkout" cell: checkout mode is
    dispatched only after `RequireWorkspace` has returned `ModeCheckout`, and outside any repository
    that call resolves external mode or errors first (§10.9), so the cell is unreachable.

    Cells 1–4 and 7–8 are frozen: they resolve to the same feature path and the same repository
    toplevel as today, and every AC 1 golden is captured from one of them. "Frozen" here means
    frozen in stdout, stderr, exit code, files, and modes, and frozen in Git commands **except**
    for the closed, read-only argv carve-out of §4.1 rule 6 — the one added checkout containment
    probe (cells 7–8) and the repo-scoped default-branch probe (cells 1–4) — which AC 2 asserts
    exactly and which changes nothing observable.

---

## 13. Implementation plan — real files, symbols, and boundaries

Order matters: step 1 must land before any production edit, because it is the pre-change evidence.

### 13.1 Step 1 — pre-change goldens (no production edit)

New file `internal/cli/sync_golden_test.go` (test-only): capture no-flag stdout/stderr/exit for both
modes into `internal/cli/testdata/sync_noflag/**`, using process-level `os.Pipe` capture, the
date-pinned builder pattern of `internal/cli/stack_status_test.go:32-90`, the closed path→token
normalization of §4.1 rule 1a (`goldenReplacements` / `goldenAssertNoResidual`), and the §17.1 `git`
PATH wrapper installed only around the measured invocation — in **divert** mode for external
captures and in **record-only** mode for checkout captures, which is mandatory so checkout's
`CombinedOutput` conflict classification and its persisted `failure_msg` stay real (§4.1 rule 1b).
Output goldens are **tws-owned bytes**
compared byte for byte after that one normalization; in external captures Git's own
`rebase`/`fetch`/`push` prose is diverted to a sidecar and asserted as command/exit semantics only,
and in checkout captures no Git prose reaches the process streams in the first place. State files
are captured
alongside them as pre-change *reference* copies and compared by the semantic comparator of §17.1
(§4.1 rules 2–3), because `started_at`, `lock_pid`, and `lock_created` differ on every run, the
checkout plan gains the additive `name` key (C5), and a checkout conflict transaction's
`failure_msg` carries Git-version-dependent bytes under the single conditional rule of §4.1 rule 2.
The wrapper's argv/exit sidecar log is captured in the same run and committed as the pre-change
baseline for §17.1 comparison mode 3; it also records the child's stdout, **in both wrapper modes
and as a verbatim tee rather than a diversion**, for the three closed argv
shapes `rev-parse --show-toplevel`, `rev-parse --abbrev-ref origin/HEAD`, and
`symbolic-ref --short HEAD`, so AC 2 can assert the C4 containment probe resolves to `ws.RepoRoot`
and that both sides of the `DefaultBranchIn` event resolve to the same fixture-pinned default
branch.
Captures are taken only from the frozen cwd cells (1–4, 7–8) and only on inputs outside the declared
C2/C3 defect fixtures; the declared C1, C2, C3, and C4 evidence
directories are captured in the same run and are explicitly not goldens (AC 1, AC 33, AC 34, AC 40,
AC 46).
Committed as pre-change evidence; a regeneration that alters an output golden, or any pinned state
field, ordering, or mode, is the regression (AC 1).

### 13.2 Step 2 — new files

| File | Package | Contents |
|---|---|---|
| `internal/sync_selection.go` | `internal` | `SyncFetchPolicy`, `SyncPropagationPolicy`, `SyncScopeKind`, `SyncRunPolicy`, `SyncSelectionRole`, `SyncSelectedEntry`, `SyncSelection`, `SyncSelectionOpts`, `ResolveSyncSelection`, `SameStackRepo` |
| `internal/sync_run_state.go` | `internal` | `SyncRunStateVersion`, `CheckoutTransactionVersion`, `SyncRunStage`, `SyncRunState`, `SyncRunGuard`, `SyncStateCell`, `SyncExternalState`, `SyncClassifyOpts`, `ClassifyExternalSyncState`, `SyncRunStatePath`, `SyncRunGuardPath`, `LoadSyncRunState`, `SaveSyncRunState`, `DeleteSyncRunState`, `HasSyncRunState`, `ClaimSyncRunGuard`, `ReclaimSyncRunGuard`, `ReadSyncRunGuard`, `ReleaseSyncRunGuard`, `isSyncMarker` (classification only; sole caller `ClassifyExternalSyncState`), `SyncStepHook`, `syncProcessAlive` (seam) |
| `internal/cli/sync_modes.go` | `cli` | flag wiring helpers, `syncEntryCompletion`, `resolveSyncPolicy(cmd) (SyncRunPolicy, bool, map[string]bool, error)` (I1–I6, then I7 when a trigger flag is present, then I8 — skipped when I7 fired, §3.5), `newSyncMarker` and the unexported test seam `var syncMarkerFn = newSyncMarker` (§8.2), `classifySyncState(featurePath string, newMode bool)` — a thin wrapper over `internal.ClassifyExternalSyncState` passing `AlwaysReadGuard: newMode` and adding the §3.6 step-8 symlink scoping, the deferred I7 and the I20 refusal — the message table of §8.7, and `saveScopedSyncFailure` |

### 13.3 Step 3 — changed files and symbols

| File | Symbols | Change |
|---|---|---|
| `internal/cli/sync.go` | `syncCmd` | six flags registered in the §3.1 source order with `SortFlags` left at its default `true` (help renders alphabetically, §3.9), two flag completions, `resolveSyncPolicy` call, validation order §3.6 (including scoped I18 and deferred I7), state-cell dispatch via `classifySyncState`, header |
| | `handleSyncAbort` | cell-aware abort (§8.6 abort column), guard reclaim, reverse teardown |
| | `handleSyncContinue` | payload-driven scoped resume, I20 refusal in cells 1 and 7 (§3.5, §3.6 step 9), `Changed` mismatch rules (§10.5), `pending` from `selected` |
| | `syncFeature` | takes the already-resolved `featurePath` (C4); selection-aware fetch loop (§6.5); scoped `TopoSort` filter; passes `SyncSelectionOpts{Mode: ModeExternal, NewMode: true, Feature: <feature>}` on new-mode runs |
| | `fetchQuiet` | unchanged bytes; called per **selected** repo |
| `internal/cli/sync_helpers.go` | `syncWithStackFiltered` | scoped iteration over `SyncSelection.Entries`, anchor skip under `local-only`, scoped rebase args (§7.1), no `markUpdatedAncestors` when scoped, new-mode failure → payload |
| | `staleStackEdges` | becomes `staleStackEdgesFiltered(feature, stack, selected)`; old signature is a `nil` wrapper |
| | `resolveEntryBase` / `resolveBase` | `resolveBase(base, repoCtx)` with the narrow repo-scoped `DefaultBranchIn` rule and the `repoCtx == ""` → today's `DefaultBranch()` fallback (C4, §13.4); untouched for the parent-entry branch |
| | `saveIncompleteSync` | unchanged; **never** called by new-mode runs |
| `internal/syncstate.go` | `SaveSyncState` | atomic write, mode `0644` preserved (C1) |
| | `LoadSyncState` | unchanged; callers gain explicit decode-error branches |
| `internal/checkout_sync.go` | `CheckoutPlanEntry` | `Name` field |
| | `CheckoutTransaction` | `StateVersion` + five policy fields |
| | `CheckoutSyncOpts` | `Policy`, `NewMode`, `Continue`, `Changed` |
| | `BuildCheckoutPlan` | selection parameter, `local-only` base resolution, anchor exclusion, `Name` fill on **every** run including no-flag (C5) |
| | `RunCheckoutSync` | new-mode read-only preflight **before `AcquireCheckoutLock`** — I9 stack load, `ResolveSyncSelection` (called once with `Feature: opts.Feature`, which owns I10–I13; `RunCheckoutSync` re-checks nothing), and the `no-fetch` I14 local-ref probe, each returning its §3.5 string unwrapped (§10.3 steps 6–8); header via `printSyncModeHeader` (§3.7, §3.10); optional pre-plan `--fetch` refresh at step 11; plan built from the preloaded stack/selection; `printLocalOnlyNoOp` at step 12b (§3.10 path 3); version write |
| | `printSyncModeHeader`, `printLocalOnlyNoOp` | new unexported print helpers — the only `fmt.Print*` additions to package `internal` besides the inline `Fetching default repo... ` line (§3.10) |
| | `ContinueCheckoutSync` | version refusal, symmetric push rule for v2, legacy rule preserved, header on a `state_version >= 2` resume via `printSyncModeHeader` (§3.7, §3.10) |
| | `finalizeTransaction` | `Name`-keyed `LastBaseSHA` attribution (C3), fed by C5 |
| | `AbortCheckoutSync` | deferred I7 refusal for `StateVersion >= 2` transactions only (§10.5 rule 8); otherwise unchanged |
| `internal/cli/checkout_sync.go` | `runCheckoutSync` | options struct, `RepoDir = ws.RepoRoot` + I19 containment (§10.9), I20 refusal on `--continue` with a trigger flag against an absent or legacy transaction (§10.5 rule 0), passes `cont` into the abort options for the deferred I7 check; it does **not** print the header and does **not** load the stack — both belong to `internal.RunCheckoutSync` (§3.7, §10.3) |
| `internal/cli/push.go` | `pushFeature` | `entry.GitBranch()` ref (C2) |
| | `pushSelected` | new (§7.6) |
| `internal/cli/new.go` | `sameStackRepo` | delegates to `internal.SameStackRepo` |
| `internal/agent_status.go` | `buildFeatureSync` | calls `internal.ClassifyExternalSyncState` (with `AlwaysReadGuard: true`) **before** the `os.Stat(statePath)` early return, then dispatches on the cell: marker-aware projection, guard liveness, degenerate-cell issues, cell 7 delegated to today's projection (§11.1) |
| `internal/cli/importcmd.go` | `isRuntimeState` | two additional exact names (§11.2) |

**Identifier spelling and package ownership.** The marker predicate is spelled `isSyncMarker`
everywhere — one unexported function in package `internal`
(`internal/sync_run_state.go`), used **only** for classification and called by **exactly one**
function, `ClassifyExternalSyncState`, also in package `internal`. `buildFeatureSync` is **not** a
caller: it consumes `SyncExternalState.Cell` and the classifier's decoded fields only (§11.1). No
exported alias
exists. Marker **generation** is a separate concern owned by package `cli`: `newSyncMarker` and the
unexported seam `var syncMarkerFn = newSyncMarker` live in `internal/cli/sync_modes.go`, are called
directly by `RunE` at §3.6 step 12, and are overridden only by package-`cli` tests. Package `cli`
therefore never references an unexported `internal` symbol; it reaches the classification through
`SyncExternalState.Cell` and `SyncExternalState.Marker`, and it reaches generation through its own
package-private seam. The `^tws-scoped-sync-[0-9a-f]{32}\.lock$` grammar of §8.2 is the only
contract shared across the boundary, and it is asserted from both sides (AC 29). Exporting either
the predicate or the generator — or making the seam an exported mutable variable — is a separate,
declared decision, not an incidental spelling drift.

### 13.4 The three C4 cwd code fixes, precisely (two declared matrix cells)

1. `runCheckoutSync`: `RepoDir = ws.RepoRoot` + containment refusal (§10.9). Its one read-only
   `git -C <cwd> rev-parse --show-toplevel` probe is the added argv record declared in §4.1
   rule 6a and asserted by AC 2; it is issued after `RequireWorkspace`/`RequireFeaturePath` and
   immediately before `RunCheckoutSync`'s first preflight Git call. The observable cwd change is
   cell 9 (linked worktree) only; the probe's `err != nil` arm is defensive and unreachable through
   CLI dispatch (§10.9).
2. `syncFeature`: accept the `featurePath` already resolved by `ws.ResolveFeaturePath` instead of
   re-deriving it through `internal.FeaturePath(feature)` (`internal/cli/sync.go:173-174`). The two
   disagree only under `TWS_ROOT` / workspace-detection edge cases, and when they disagree today's
   code loads no stack and silently falls into `syncFallback`'s hard-coded `origin/main` rebase.
   Where they agree — every healthy layout, and every AC 1 fixture — the fix is a no-op. Where they
   disagree, the run changes observably **including on the no-flag path** (cwd matrix cells 5 and
   6): this is declared behavioural exception 2 (§4.1 rule 5, §4.2 item 7, §4.5 C4) and is asserted
   by AC 46.
3. `resolveBase` gains a repo-context parameter: `resolveBase(base, repoCtx string) string`, where
   `repoCtx` is the entry's `Repo` when set, otherwise the entry's **materialized** worktree path
   when that directory exists, otherwise `""`. Precise, deliberately narrow semantics:
   - `repoCtx == ""` → the call is exactly today's `internal.DefaultBranch()`
     (`internal/exec.go:57-65`), same probe, same argv, same `main` fallback. Healthy no-flag runs,
     where cwd
     resolution already yields the right answer, keep their default-base semantics **unchanged**;
     this change MUST NOT silently alter them.
   - `repoCtx != ""` → `internal.DefaultBranchIn(repoCtx)`; on error, fall back to today's
     `internal.DefaultBranch()` value so no entry loses its `origin/<default>` rewrite. On the wire
     this replaces today's cwd-scoped default-branch resolution with the same resolution run in
     `repoCtx`: `git -C <repoCtx> rev-parse --abbrev-ref origin/HEAD`, its `-C`-scoped
     `symbolic-ref --short HEAD` fallback, and the hard-coded `main` fallback behind both. Because
     the pre-change call runs wherever the process cwd happens to be and the post-change call runs
     in the repository, the two sides **may take different arms of that event**, so the invocation
     count and the exit-status classes may differ; the specification claims no equality there.
     That replacement is the declared read-only argv carve-out of §4.1
     rule 6b and is compared as one **closed logical event validated by resolved value** by AC 2 /
     §17.1 comparison mode 3 — same fixture-pinned default branch on both sides, and no unrelated
     argv inside the window.
   The fix therefore applies only where cwd-based resolution is unavailable or provably wrong: a
   multi-repo entry whose repository has a different default branch, and an invocation from a cwd
   that is not inside the entry's repository. `DefaultBranchIn` is a local ref read and is legal
   under `no-fetch`. Regression coverage: AC 2 (closed argv carve-out), AC 46 (cwd matrix), and AC 53 (multi-repo **no-flag**
   default-base regression).

### 13.5 Explicitly untouched

`internal/cli/export.go`, `internal/stack_ancestry.go`, `internal/stack_status.go`,
`internal/cli/stack_status.go`, `internal/cli/doctor.go`, `internal/checkout_health.go`,
`internal/cli/list.go`, `internal/cli/archive.go`, `internal/cli/new.go` (beyond the one-line
delegation), `internal/health.go`, `internal/stack.go` (`TopoSort`, `Descendants`, `GetBranch`,
`UpdateBaseSHA`, `UniqueRepos`, `PrintTree` all unchanged), and every `tws space`, `tws registry`,
`tws session`, and `tws template` file.

No feature other than `sync-modes` is modified. `PrintTree`'s ad-hoc `children`/`roots` maps
(`internal/stack.go:207-280`) are display-only and MUST NOT become a selector source.

### 13.6 Minimality statement

The changeset is: two new `internal` files, one new `cli` file, eleven changed files, and the test
and documentation files of §13.1/§14. No new command, no new package, no new dependency, and no
change to `go.mod`.

---

## 14. Documentation, skills, and changelog

User-facing behaviour changes, so agent skills and documentation MUST be updated
(`docs/engineering-workflow.md`, "Coding conventions").

| File | Change |
|---|---|
| `README.md` | the `tws sync` example block (lines ~64-67) and the command table row (~127) gain the six flags; a short "sync modes" paragraph states the four fetch × propagation cells, the three scopes, that `no-fetch` means no automatic network **input** (not offline) and composes with `--push`, that concurrent syncs of one feature remain unsafe, and that the sync-mode flags can be repeated on `--continue` only for a run that was started with them |
| `docs/cheatsheet.md` | the sync section (~148-150) gains `--only`, `--from`, `--local-only`, `--no-fetch`, `--fetch`, `--full` one-liners, including the note that checkout `--fetch` refreshes remote-tracking refs before planning and that an interrupted refresh simply re-runs, plus one line on where a checkout sync may be run from (the repository checkout or any subdirectory of it; a linked worktree of that repository is refused, C4, which is why every checkout sync runs one read-only `git rev-parse --show-toplevel` containment check just before its pre-flight checks) |
| `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` | the sync command list (~52-54) gains the scoped/local-only/no-fetch forms and one guidance line: prefer `--from <entry>` after resolving a conflict in a subtree rather than a full-stack sync |
| `assets/skills/copilot/tws.prompt.md` | the `tws sync` signature line (~18) and the checkout paragraph (~80) gain the new flags and the scoped-recovery note |
| `.github/skills/tessera-patch/SKILL.md`, `.claude/skills/tessera-patch/SKILL.md` | **unchanged** — tpatch skills are not tws documentation |
| `CHANGELOG.md` | one `## Unreleased` block covering: the three axes and three scopes; the two mode defaults; scoped completion; scoped push; frozen no-flag behaviour with its **four** declared categories of observable change (C1 corrupt state; C2 decoupled-name push; C3+C5 duplicate-branch metadata attribution plus the additive plan `name:` key; C4 cwd resolution) and the single declared payload-`Lstat` runtime-state-path read; the fail-closed downgrade mechanism **with its stated boundary** (holds while the sentinel exists; unsupported after an old `--abort`); C1–C5 as declared fixes, with the `pushFeature` ref fix called out explicitly for decoupled names **as a behaviour change on flag-free `tws sync --push` and `tws push` too** (the pushed ref becomes the entry's real branch, so the push now succeeds and updates `refs/heads/<branch>` instead of failing on a ref named after the logical entry), the duplicate-branch `last_base_sha` attribution fix called out as a behaviour change on flag-free checkout syncs (the correct entry's metadata is updated, so `stack.yaml` differs from previous releases on such a stack), the checkout plan's additive `name:` key called out as a persisted-format change that is semantically equivalent, not byte-identical, for no-flag transactions and is ignored by older binaries, **C1's unreadable-state behaviour change called out per verb** (a corrupt `.sync-state.yaml` now yields a clean error naming the file instead of a panic on a plain run and instead of `nothing to continue — no sync in progress` on `--continue`, and `--abort` now fails closed with exit 1 and deletes nothing instead of reporting `Nothing to abort — no sync in progress.` with exit 0), and **C4's cwd behaviour change called out per cell** (running `tws sync` from an external feature directory or a nested subdirectory of one now syncs the resolved feature's stack instead of silently rebasing every worktree onto `origin/main`; running a checkout sync from a linked worktree of the checkout repository is now refused with a clear error instead of mutating the wrong working tree — in ordinary flag-free invocations too), and **C4's two declared read-only Git probes called out as part of C4** (a checkout sync now runs one extra read-only containment check, `git -C <cwd> rev-parse --show-toplevel`, immediately before its pre-flight checks and before anything is locked, written, or mutated, and external default-branch resolution now asks the entry's own repository, `git -C <repo> rev-parse --abbrev-ref origin/HEAD` with its usual `symbolic-ref` and `main` fallbacks, instead of whatever repository the current directory happens to be in — which is a different *question*, so it may take a different fallback arm and may resolve to a different, correct branch; both are read-only, write nothing, and change no output, exit code, file, mode, or ref by themselves); checkout's explicit `--fetch` described as a best-effort pre-plan remote-ref refresh that runs before the transaction exists and is deliberately not resumable; the `tws status` marker-aware projection with no schema change; the `tws import` filter extension; the new refusal of sync-mode flags on `--continue` without v2 state (trigger-free `--continue` is unchanged); and the documented concurrency and legacy-symlink limitations |
| `docs/roadmap.md` | move **sync modes** from the P1 backlog into the shipped list, and update the "Current target" line to the next P1 item (`rebase plan guard`) |
| `docs/engineering-workflow.md` | append sync modes as checkout slice/shipped item 11 and update the "Next roadmap feature" line |

Documentation MUST NOT claim: general downgrade safety; that `no-fetch` is offline; that concurrent
syncs are now safe; that no-flag behaviour is unchanged in **every** cwd (cells 5/6 and 9 change, C4,
§4.1 rule 5) or on **every** input (a corrupt `.sync-state.yaml` changes, C1; a decoupled-name
`--push` changes, C2; a duplicate-`GitBranch()` checkout stack's `stack.yaml` changes, C3 — §4.1
rule 7); that a no-flag run
issues exactly the same Git commands as before (it may differ by the two declared read-only C4
probes, §4.1 rule 6, which MUST be described as read-only and as part of C4, and by C2's corrected
`push` ref on a decoupled entry); that the two default-branch probes issue the same number of Git
commands or fail and succeed in the same way (they ask different repositories, §4.1 rule 6b); that
no **mutating** Git command changes on the no-flag path (C2's `push` ref does, on decoupled names);
that no-flag checkout
transaction files are byte-identical to previous releases
(they gain the additive `name:` key, C5) or that a no-flag checkout leaves `stack.yaml` unchanged on
a stack with duplicate branches (C3); that a legacy `.sync-state.yaml` symlink is refused; that
external and checkout base resolution have been unified (they are unified only under `local-only`);
that a checkout run's frozen policy is persisted before its `--fetch` refresh (it is persisted
before the first checkout/rebase/index/local-branch mutation, §10.3); or that an interruption
during that refresh can be resumed (it cannot, and does not need to be).

---

## 15. Dependency verdict

`status.json` registers **16 hard** and **9 soft** edges. Each is reconciled below against what this
definition actually changes. **No DAG change is required by this definition** — no edge is added,
removed, or re-kinded.

**Hard (16)** — this feature modifies a symbol or semantic the parent introduced:

| Parent | Confirmed by this definition |
|---|---|
| `keep-track-of-stacked-diffs-and-dependencies` | `TopoSort`/`Descendants`/`stack.yaml` are the ordering and closure the selection filters (§5.2) |
| `sync-continue` | `SyncState`, `--continue`, `--abort` are extended by the sentinel/payload contract (§8) |
| `amend-aware-rebase` | `LastBaseSHA` + `--onto` replay preserved in every cell (§7.1) |
| `checkout-stack-safety` | `CheckoutTransaction`, `BuildCheckoutPlan`, staging, locking, restoration extended (§10) |
| `branch-name-decoupling` | `GitBranch()` drives selector identity, selected push, and the C2/C3 fixes (§5.3, §7.6) |
| `multi-repo-workspaces` | `Repo`, `UniqueRepos`, `SameStackRepo` drive fetch boundaries and anchors (§5.2, §6.5) |
| `fix-default-base-branch` | `DefaultBranchIn`/`origin/HEAD` is exactly what `local-only` suppresses and what C4 re-routes (§6.3, §13.4) |
| `archive-worktree` | the archived/prunable arm of `syncWithStackFiltered` is scoped (§5.7, §7.2) |
| `quiet-fetch-output` | owns the `Fetching …/done/failed` bytes and the fetch-failure tolerance the fetch axis extends (§6.4) |
| `cobra-migration` | owns the `RunE`/flag surface and `cmd.Flags().Changed`, which the presence rules require (§3.2) |
| `fix-sync-continue-descendants` | `staleStackEdges` and `branchContainsConfiguredParent` are both modified/scoped (§7.5) |
| `push-branches` | `pushFeature`'s ref changes and `pushSelected` filters its entry set (§7.6) |
| `fix-checkout-feature-path-routing` | `runCheckoutSync`'s feature-path routing sits under the C4 `RepoDir` change and the invocation matrix (§10.9) |
| `fix-external-feature-dir-resolution` | `RequireWorkspace`'s external fallback is what makes cells 5–6 of the cwd matrix work; C4 fixes the `syncFeature` half (§13.4) |
| `agent-work-status-dashboard` | `buildFeatureSync`'s external projection semantics change (§11.1) |
| `checkout-workspace-lifecycle` | `isRuntimeState`'s semantics change and its committed test gains cases (§11.2) |

**Soft (9)** — consumed unchanged or adjacent:

`fix-sync-branch-identity` (`checkSyncWorktreeBranch` reused verbatim), `post-rebase-validation`
(external `runValidation` unchanged; only the command string is frozen into state),
`divergent-stack-sync` (fixtures reused), `stack-ancestry-doctor` (`StackEdge`/`StackBasePolicy`
cited as the authority for M2/M3, not modified — D6 defers the collapse), `stack-status` (cited in
selector-validation guidance; its read-only no-fetch contract untouched), `worktree-health-check`
(`CheckWorktreeBranch` reused), `clean-git-output` (`RunDirClean`'s stderr filter is consumed
unchanged and is **not** pinned by any golden — the §17.1 wrapper leaves it an empty stream — so any
focused test it owns stays unchanged and AC 2 requires a direct regression assertion for the
`    (skipped duplicate commit)` reformat outside the wrapper), `fix-missing-completions` (flag
completion is additive alongside `ValidArgsFunction`),
`skill-distribution` (skills updated in §14).

**Candidates deliberately not registered**, with reasons:

- `workspace-sibling-links` (`GuardFeatureName` in `sync.go`) — consumed unchanged. It becomes hard
  only if the implementation modifies the guard or listing path, which this definition forbids.
- `fix-checkout-lifecycle-layout`, `workspace-mode-foundation`, `delete-feature` — genuinely
  transitive; read through, never modified.

**Cycle risk: none.** Every parent is `applied` and none depends on `sync-modes`, which has no
dependents. `tpatch feature deps --validate-all` MUST still report a clean DAG after implementation
(AC 52). If implementation proves a new directly-modified parent symbol, the edge is registered
**then**, by the parent agent, not pre-emptively here.

---

## 16. Acceptance criteria

Every criterion is runnable. Git behaviour uses **real** temporary repositories, real local bare
remotes, and real linked worktrees — never mocks. Every test that shells out to Git MUST call
`t.Setenv("GIT_CONFIG_COUNT", "0")` and pass `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_COUNT=0`, and a
temp `HOME` in `cmd.Env`, so a host `GIT_CONFIG_COUNT` injecting `safe.bareRepository=explicit`
cannot leak into a fixture or a permanent golden. All tests MUST pass on macOS and Ubuntu: no
`/proc`, no GNU-only flags, no `timeout(1)`, no `sed -i` without a suffix, and no assumptions about
`sh` being bash.

### Pre-change evidence

1. `internal/cli/testdata/sync_noflag/**` goldens are captured against the **unmodified** tree, via
   `os.Stdout`/`os.Stderr` pipe swap (or the built binary), for: external clean run, external
   conflict run, external `--continue`, external `--abort` with and without state, external missing
   `stack.yaml` (`syncFallback`), external stale-edge failure block, checkout clean run
   (`Checkout sync complete.`), checkout conflict run, checkout `--continue`
   (`Checkout sync completed.`), and checkout `--abort`. Every capture is taken from a **supported,
   agreeing cwd** — matrix cells 1–4 for external and 7–8 for checkout (§12.11) — because cells
   5, 6, and 9 are the declared C4 exception (§4.1 rule 5) and MUST NOT be frozen. Each capture is
   stored **after** the closed
   path→token normalization of §4.1 rule 1a — stdout and stderr as separate goldens, with
   `goldenAssertNoResidual` asserting no temporary root and no stable ID survives — and is taken
   with the §17.1 `git` PATH wrapper installed **only** around the measured invocation, in the mode
   its workspace mode requires: **divert mode for the external captures** (`rebase`/`fetch`/`push`
   prose lands in the sidecar) and **record-only mode for every checkout capture**, where the
   wrapper only logs argv/exit and passes all streams through, so checkout's `CombinedOutput`
   receives the real Git bytes and the conflict capture classifies as a real conflict (§4.1 rule 1b).
   In **both** modes the wrapper additionally tees the stdout of the three read-only shapes
   `rev-parse --show-toplevel`, `rev-parse --abbrev-ref origin/HEAD`, and
   `symbolic-ref --short HEAD` into the argv log — captured, replayed verbatim to tws, stderr
   inherited, real exit status preserved — which is inert for the capture and is what AC 2's
   resolved-value assertions consume.
   Either way the golden contains tws-owned bytes — though in divert mode `RunDirClean` sees an
   empty stderr stream, so no golden pins its filtering or its
   `    (skipped duplicate commit)` reformat, which AC 2 covers by a direct assertion outside the
   wrapper. The wrapper's argv/exit log is committed
   alongside each capture, path-normalized by the
   same table. No corrupt-`.sync-state.yaml` capture is committed as a frozen golden (§4.1 rule 4);
   the pre-change behaviour of the three verbs on a corrupt file is captured in the same run into
   `internal/cli/testdata/sync_noflag/declared_c1/`, labelled **declared-change evidence, not a
   golden**, for the AC 40 diff. The pre-change behaviour of the disagreeing cwd fixture is captured
   the same way into `internal/cli/testdata/sync_noflag/declared_c4/` for the AC 46 diff, the
   pre-change no-flag `--push` behaviour of the `decoupled` fixture into `…/declared_c2/` for the
   AC 33 diff, and the pre-change no-flag checkout behaviour of the `duplicate-branch` fixture —
   including its `stack.yaml` — into `…/declared_c3/` for the AC 34 diff. None of the four
   `declared_*` directories is a golden, and no frozen golden or frozen state reference is captured
   from a decoupled-name or duplicate-`GitBranch()` fixture (§4.1 rule 7).
   Alongside each capture, any state file the run wrote (`.sync-state.yaml`,
   `<feature>-checkout-sync.yaml`) is stored as a pre-change **reference** for the §17.1 semantic
   comparator, together with its file mode. The checkout **conflict** capture's transaction
   reference is asserted at capture time to carry `failure_kind: conflict`, `stage: conflict`, and a
   `failure_msg` beginning `rebase conflict: ` with a non-empty suffix — proof the wrapper did not
   swallow the conflict bytes. Committed before any production edit.
2. Re-running the whole suite after the implementation reproduces every **output** golden in AC 1
   byte for byte — after the same §4.1 rule 1a normalization and with the same §17.1 wrapper in the
   same per-mode configuration, and
   with no other transformation applied — and with the same exit code; every captured state file
   passes the semantic comparator of §17.1 against its pre-change reference, the checkout conflict
   transaction under the single conditional `conflictFailureMsg` rule (`failure_kind` and `stage`
   pinned to `conflict`, the `rebase conflict: ` prefix pinned, only the non-empty suffix
   normalized; the conflict transaction is the only transaction reference in the frozen set, so no
   non-`conflict` `failure_msg` is compared at all — §4.1 rule 2); and the sidecar
   assertion passes under **comparison mode 3** (§17.1), i.e. the ordered `(verb, argv)` records and
   their exit-status classes match the pre-change log **exactly**, except for the closed C4
   carve-out of §4.1 rule 6 and nothing else, while Git's own prose is asserted nowhere. AC 2
   applies to the frozen captures only: the declared C2 decoupled-name `--push` fixture and the
   declared C3 duplicate-`GitBranch()` checkout fixture (§4.1 rule 7) are **not** in the AC 1
   golden set and are asserted as declared-change diffs by AC 33 and AC 34 instead.
   The carve-out is asserted, not assumed — and both assertions below read the child's stdout from
   the sidecar, which is possible because the wrapper **tees** the stdout of exactly three
   read-only shapes (`rev-parse --show-toplevel`, `rev-parse --abbrev-ref origin/HEAD`,
   `symbolic-ref --short HEAD`) **in both wrapper modes**, replaying the captured bytes verbatim to
   tws, inheriting stderr, and preserving the real exit status, so recording them changes nothing
   about either run:
   - **Checkout captures.** The post-change log carries **exactly one** record absent from the
     pre-change log, whose argv is exactly `git -C <cwd> rev-parse --show-toplevel`, with exit
     status zero, and whose **output is asserted equal to `ws.RepoRoot`** (both sides
     `filepath.Clean`ed and `EvalSymlinks`ed). Its position is asserted **exactly** and is *not*
     "before every pre-change record": the post-change log MUST be the pre-change log's
     workspace/feature-resolution prefix (`RequireWorkspace`'s `rev-parse --git-common-dir` and any
     other resolution read) **verbatim, in order**, then the one added probe record, then the
     remaining pre-change records — beginning, in a fresh-run capture, with `RunCheckoutSync`'s
     first preflight record, `rev-parse --git-path rebase-merge`, and in a `--continue`/`--abort`
     capture with that path's own first Git record — **verbatim, in order**. Every record other than the probe
     matches verbatim in argv, relative order, and exit-status class. A second added record, the
     probe at any other position (in particular ahead of the workspace-resolution prefix), a missing
     probe, or a probe resolving anywhere other than `ws.RepoRoot` fails AC 2.
   - **External captures.** Default-branch resolution is compared as the **whole closed
     `DefaultBranchIn` logical event**, not as a record pair. On each side the event is exactly one
     of its three legal shapes (§4.1 rule 6b): a successful `rev-parse --abbrev-ref origin/HEAD`; a
     failed `rev-parse` followed by `symbolic-ref --short HEAD`; or both failing followed by the
     hard-coded `main` fallback. The two sides **may differ in record count and in exit-status
     class** — the pre-change event runs in the process cwd, the post-change event in the
     materialized repository — and AC 2 MUST NOT assert equal counts, equal exit classes, or a
     difference of only a leading `-C <path>`. What AC 2 asserts is: (i) the **resolved default
     branch** of the pre-change event equals that of the post-change event and equals the value
     **pinned as a constant of the frozen fixture**, read from the recorded stdout of the event's
     own records (or the literal `main` for the fallback shape); and (ii) every record in the
     compared window on either side belongs to exactly this closed event, so no unrelated argv is
     hidden inside the carve-out. A frozen fixture whose two sides resolve to **different** default
     branches fails AC 2; a fixture **declared** to disagree is not frozen and is AC 46 / AC 53
     declared-change evidence instead. Where the entry has no repo context, both sides are `-C`-free
     and compare verbatim with no carve-out applied.
   No other argv difference — added, removed, reordered, or altered, in any verb — is allowed by
   AC 2 in a frozen capture; every such difference is a regression.

   **What the goldens deliberately do not cover, and how it is covered instead.** In divert mode
   `RunDirClean` receives an empty stderr stream for `rebase` and `push`, and the non-verbose
   `fetch` already runs through the silent helpers, so **no no-flag golden pins `RunDirClean`'s
   stderr filtering** — in particular not the tws-owned `    (skipped duplicate commit)` reformat.
   AC 2 therefore additionally requires, and any claim to the contrary is a specification violation:
   (i) any focused test owned by the `clean-git-output` parent feature is **unchanged** by this
   feature; and (ii) a **direct regression assertion outside the §17.1 wrapper** exists and is
   retained (added if absent — nothing asserts the filter today) — a focused test invoking
   `internal.RunDirClean` on a child that writes a `hint:` line,
   a `Disable this message` line, a line containing `skipped previously applied commit`, and an
   ordinary error line to stderr, with `os.Stderr` captured, asserting byte for byte that the first
   two are dropped, that the third yields exactly `    (skipped duplicate commit)\n`, that the
   ordinary line is forwarded verbatim, and that the child's exit status is returned unchanged.

   A test asserting Git prose, a golden
   regenerated with any normalization beyond the closed table of §4.1 rule 1a, an argv comparison
   widened beyond the two carve-out events, a claim that the two default-branch events have the same
   count or exit class, a checkout capture
   taken in divert mode, or a wrapper that `exec`s (and so fails to record) any of the three teed
   read-only shapes in either mode — or that diverts them in either mode — is a specification
   violation, not a fix.
3. `tws sync --help` is snapshot-tested; the snapshot shows the local flag block in pflag's
   **alphabetical** order — `--abort`, `--continue`, `--fetch`, `--from`, `--full`, `-h/--help`,
   `--local-only`, `--no-fetch`, `--only`, `--push`, `--test`, `-v/--verbose` — containing exactly the six new flags
   with their §3.1 spellings, types, defaults, and help strings, and every pre-existing flag
   unchanged. A companion assertion checks `syncCmd.Flags().SortFlags` is `true`, so the ordering is
   pflag's default and not a registration-order artefact. No other command's help snapshot changes;
   `internal/cli/testdata/existing_commands/**` is unchanged.
4. `git for-each-ref` and `stack.yaml` snapshots taken before and after every AC 1 no-flag run are
   identical to the pre-change tree's, proving no state or ref drift from the refactor. This
   criterion covers the AC 1 golden fixtures only, none of which is a decoupled-name or
   duplicate-`GitBranch()` fixture: on those two declared fixtures (§4.1 rule 7) the remote ref set
   (C2) and `stack.yaml` (C3) deliberately differ, and are asserted as declared-change diffs by
   AC 33 and AC 34.
5. A no-flag external failure writes `.sync-state.yaml` with mode `0644`, and its content is
   equivalent to the pre-change tree under §17.1: identical key set, identical key order, identical
   values for `failed_branch`, `pending`, `completed`, and `skipped`, with `started_at` present and
   matching `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$` — the only normalized field. Raw-byte equality
   is **not** asserted and MUST NOT be, because `started_at` is per-run. The run writes **neither**
   `.sync-state.v2.yaml` **nor** `.sync-run.lock`.
6. A no-flag checkout run writes a transaction file with mode `0600` whose content is equivalent to
   the pre-change tree under §17.1: after removing the additive per-plan-entry `name:` key (C5), the
   key set, key order, and every value are identical, `state_version` is absent, none of the five
   policy keys is present, and the three normalized fields `started_at`, `lock_created` (RFC3339
   UTC) and `lock_pid` (positive integer) are present with the right shape. The removed `name`
   values are separately asserted to equal the corresponding `StackEntry.Name`s in plan order, and a
   binary built from the pre-change tree is asserted to still load the file and resume it. Raw-byte
   equality is **not** asserted. The same criterion is repeated for the **no-flag checkout conflict**
   transaction, where the single conditional rule of §4.1 rule 2 applies and is the only additional
   licence taken: `failure_kind` is exactly `conflict`, `stage` is exactly `conflict`, `failure_msg`
   is present, begins with the exact prefix `rebase conflict: `, and has a **non-empty** suffix —
   which is the direct proof that the checkout capture ran the wrapper in record-only mode and that
   `CombinedOutput` saw the real Git bytes — with only that suffix normalized on both sides. The
   conflict transaction is the **only** checkout transaction reference in the frozen set (a clean
   run, a successful `--continue`, and `--abort` all delete the file), so no captured reference
   carries a non-`conflict` `failure_msg` and the comparator's byte-for-byte branch for such values
   is never exercised by these criteria (§4.1 rule 2).

### CLI contract

7. Each of I1–I6 and I8 is rejected with its exact message, exit 1, and **zero** side effects: no
   fetch (an unreachable `origin` proves it), no rebase, no state file, no guard, and an unchanged
   `stack.yaml`. I7 is asserted in both directions: `--continue --abort` **with** a trigger flag, and
   `--continue --abort` against v2 state (external payload, checkout `state_version: 2`), are
   refused with the I7 message; `--continue --abort` with **no** trigger flag against legacy or
   absent state behaves exactly as the AC 1 goldens (abort wins), in both modes. **I7/I8 precedence
   is asserted directly** (§3.5, "I7 before I8"), in both modes and with the same zero-side-effect
   assertions: `--continue --abort --only x` (and, separately, each of the other five trigger flags)
   produces **exactly** `--continue and --abort are mutually exclusive` and stderr contains **no**
   occurrence of `--abort cannot be combined with`; the same invocation **without** `--continue`
   produces exactly the I8 string naming the changed flags, sorted and comma-joined; and both are
   refused before any state read, so the assertions hold identically against absent, legacy, and v2
   state. I20 is asserted in
   **both modes** and with the same zero-side-effect assertions: `--continue` plus each of the six
   trigger flags in turn, against (a) absent state / no transaction and (b) real legacy state /
   a transaction with no `state_version`, is refused with the exact string
   `cannot use sync mode flags on --continue without v2 state; continue without them or abort and start a new run`
   before any Git command, any fetch, any lock or guard claim, and any state write; while the same
   `--continue` **without** trigger flags against the same states reproduces the AC 1 goldens byte
   for byte, and the same `--continue` **with** trigger flags against v2 state (external payload,
   checkout `state_version: 2`) is **not** refused by I20 and is governed by §10.5 rules 1–5.
8. `--fetch=false`, `--no-fetch=false`, `--full=false`, and `--local-only=false` each produce the
   corresponding §3.2 message; `--push=false` does **not** error and is distinguishable from an
   omitted `--push` via `Changed`.
9. `--only ""` and `--from ""` are rejected with their exact messages.
10. The header line is printed exactly once, before any fetch, for every new-mode run that reaches
    its first side effect, and is **absent** from every no-flag run. It is also absent from every
    new-mode run refused during validation — external §3.6 steps 1–12 and checkout §10.3 steps 1–8,
    including I9–I14 — asserted per mode with the refused-run captures of AC 7 and AC 55. In
    checkout mode the header is emitted by `internal.RunCheckoutSync` through
    `printSyncModeHeader` after the preflight and lock
    and before the optional `--fetch` (§3.7, §3.10, §10.3 steps 10–11), and by
    `internal.ContinueCheckoutSync` on a `state_version >= 2` resume; the CLI never prints it.
11. `syncEntryCompletion` returns the feature's non-archived `StackEntry.Name`s in `stack.yaml`
    order with `ShellCompDirectiveNoFileComp` when `args[0]` is present, and returns
    `nil, ShellCompDirectiveNoFileComp` — without erroring or printing — when it is absent, when
    `internal.RequireWorkspace()` fails, when `ws.ResolveFeaturePath` fails, and when `stack.yaml`
    is missing.

### Axes and scopes

12. All 12 cells (fetch × propagation × scope) are exercised in **external** mode, each asserting
    the resulting SHAs of every stack entry.
13. All 12 cells are exercised in **checkout** mode with the same assertions.
14. `no-fetch` **without** `--push`, against an `origin` whose path has been removed, succeeds
    (exit 0) and issues zero `fetch`/`ls-remote` commands. Asserted in both modes and in every
    `no-fetch` cell, not only `local-only`.
15. `no-fetch --push` against the same unreachable `origin` completes every rebase and fails **only**
    at the push step; the branch SHAs show the rebases happened.
16. The default external run against an unreachable `origin` still prints `failed` and proceeds
    (exit 0) — tolerance preserved. The same holds for an explicit `--fetch` in either mode.
17. A default external run fetches **once per unique repo** (asserted by advancing a bare remote and
    observing `origin/*` move), and a scoped run fetches only the repos represented in the
    selection — a second repo's `origin/*` refs are byte-identical before and after.
18. With `origin/master` ahead: `--local-only` leaves the root SHA and **every** `origin/*` ref
    untouched while each selected child contains its local parent tip; `--full` advances the root.
    Repeated for a literal-ref root (`master`), an `origin/master` root, and a tag-based root.
19. `--only` and `--from`: every **unselected** branch SHA and every unselected entry's serialized
    `stack.yaml` fields are byte-identical before and after; the selected entries' `last_base_sha`
    values are the only changes. Repeated on the `duplicate-branch` fixture, where "the selected
    entry" is resolved by logical `Name` (C3): the selected entry's `last_base_sha` moves and its
    duplicate-`GitBranch()` sibling's is byte-identical before and after, in **both** modes.
20. `--from X` rebases X **and** its whole descendant closure (D2); `--only X` rebases exactly X and
    nothing else.
21. A scoped external run issues **no** `--update-refs` (asserted by a Git wrapper on `PATH` that
    records argv, or by asserting an out-of-range ancestor ref did not move); a full-scope run still
    issues it.
22. A deliberately stale **unrelated** edge does not fail a scoped run (exit 0) and still fails a
    full run (exit 1) with the exact `Sync incomplete; stale stack edges remain:` block. A
    `local-only` run whose root is behind `origin/<default>` exits 0, proving the gate never probes
    the remote-tracking ref.
23. After a successful scoped run with an unrelated stale edge, the informational block of §3.7 is
    printed and the exit code is 0.
24. `--local-only` naming an anchor prints the no-op block and exits 0, mutating nothing (D3); the
    same selection under `--full` performs real work. Asserted in **both** modes: in checkout mode
    the block is emitted by `internal.RunCheckoutSync` through `printLocalOnlyNoOp` (§3.10 path 3,
    §10.3 step 12b) — one `[-]` line per selected anchor in topological order, with
    `Nothing to propagate.` present only when the plan is empty and absent when some other selected
    entry was rebased — and `Checkout sync complete.` still comes from the CLI, after the block.
25. Ancestors outside the selection are never rebased and are never required to be current (D4),
    asserted by leaving an ancestor deliberately stale and running `--only <descendant>` to exit 0.

### Recovery, continuation, and state

26. Conflict inside a scoped run → the payload carries scope, policy, push, validation command, and
    the resolved `selected` list → `--continue` with **no** flags resumes scoped and completes;
    `--continue` with explicitly **matching** flags is accepted; `--continue` with explicitly
    **conflicting** flags is refused before any Git call; a plain `--continue` against persisted
    `push: true` is accepted and pushes as persisted; `--abort` with an explicitly supplied
    axis/scope flag is refused. Includes an explicit `--push=false` case distinguishable from an
    omitted `--push`. The matching/conflicting comparison is asserted to be reachable **only**
    against v2 state: the same matching and conflicting invocations run against absent or
    real-legacy external state, and against an absent or legacy checkout transaction, are refused
    with the I20 string instead — in **both** modes — proving no scope flag is ever silently
    ignored or broad-resumed, while the trigger-free `--continue` against those same states still
    reproduces today's behaviour byte for byte.
27. Downgrade, plain: with a live sentinel + payload on disk, the prior binary (§9.6) stops with
    `previous sync incomplete (failed on: <marker>); use --continue or --abort` **before any fetch
    or rebase** — asserted with an unreachable `origin` so any fetch would be observable.
28. Downgrade, `--continue`: the prior binary refuses with
    `failed branch "<marker>" no longer exists in stack` and rebases nothing (all SHAs unchanged).
29. Marker properties, asserted from **both** packages because generation and recognition live in
    different ones (§8.2, §13.3):
    - package `cli`: `newSyncMarker()` returns a value matching the literal §8.2 grammar
      `^tws-scoped-sync-[0-9a-f]{32}\.lock$`, is a safe single path component, is **rejected** by
      `git check-ref-format --branch`, and `git branch <marker>` fails; repeated calls never
      collide;
    - package `internal`: an in-package unit test (the predicate is unexported) asserts
      `isSyncMarker` accepts values of that same literal grammar and rejects near-misses — wrong
      prefix, upper-case hex, wrong length, missing `.lock`, and any path separator;
    - round trip: a marker produced by `newSyncMarker` and written as a sentinel is classified by
      the exported `internal.ClassifyExternalSyncState` as cell 4 (or cell 5 with a payload beside
      it), which proves the two packages agree on the grammar without either calling the other's
      unexported symbol.
30. Collision refusal: with the package-`cli` seam `syncMarkerFn` overridden (from a package-`cli`
    test) to a value equal to an existing `StackEntry.Name`,
    and separately to an existing `entry.GitBranch()`, the run is refused with the I17 message and
    leaves no guard, no sentinel, no payload, no fetch, and no rebase on disk.
31. Old `--abort`: after running it against a live new-mode run, the sentinel is gone, the payload
    remains, and the real worktree is still mid-rebase. Two guard variants are asserted:
    - **stale or absent guard** (the owning process has exited): the new binary refuses plain and
      `--continue`, naming the payload's **real** failed entry and its worktree path; new `--abort`
      aborts that real rebase, deletes the payload, removes the guard, and prints
      `Sync state cleared.`
    - **live owning guard** (constructed through the §17.4 PID seam): plain, `--continue`, **and**
      `--abort` are all refused with the §8.7 live-guard messages, and the payload bytes, the guard
      bytes, and the mid-rebase worktree state (`.git/rebase-merge` present, branch SHA unchanged)
      are identical before and after each of the three invocations.

    The documented unsupported case is asserted as an observed fact: a following **old plain** sync
    is no longer blocked.
32. Mixed-state genesis: reproduce §9.3 sequence 1 end to end and assert the resulting cell is
    `{real legacy, valid}` and that the new binary refuses plain and `--continue` naming **both**
    failed entries, and refuses `--abort` rather than clearing either file.
33. `pushFeature` pushes `GitBranch()` (C2, declared no-flag change, §4.1 rule 7): with a decoupled
    entry (`name: work`, `branch: user/work`), the **no-flag** `tws sync <f> --push` and `tws push
    <f>` both update `refs/heads/user/work` on the bare remote and create no ref named `work`. This
    is asserted as a **declared-change diff** against
    `internal/cli/testdata/sync_noflag/declared_c2/`, not against a frozen golden: the pre-change
    capture shows `git push --force-with-lease origin work`, the `  [x] NAME (push failed)` line,
    its exit code, and the absent `refs/heads/user/work`; the post-change run shows
    `git push --force-with-lease origin user/work`, `  [+] NAME (pushed)`, its exit code, and the
    updated remote ref. The same criterion asserts the fix is **inert for coupled names**: on the
    `linear` fixture the push argv, output bytes, exit code, and remote refs are identical to the
    pre-change tree, so every coupled AC 1 golden stays frozen.
34. `finalizeTransaction` attributes `last_base_sha` by `Name` (C3, declared no-flag change, §4.1
    rule 7): with two entries sharing one
    `GitBranch()`, the correct entry's `last_base_sha` is updated and the other is untouched —
    asserted for a **no-flag** checkout run (which is where the defect bites today), proving C5
    wrote `name:` on the frozen path, and again for a new-mode run. For the no-flag run this is a
    **declared-change diff** against `internal/cli/testdata/sync_noflag/declared_c3/`, not a frozen
    comparison: the post-change `stack.yaml` deliberately differs from the pre-change tree's, and
    the assertion is that it differs **only** in the two entries' `last_base_sha` attribution, with
    the pre-change tree shown to have written the wrong entry. Everything else about the run —
    argv log, stdout, stderr, exit code, file set, and modes — is asserted **identical** to the
    pre-change capture. The companion assertion runs the same no-flag checkout on the unique-branch
    `checkout` fixture and requires `stack.yaml` to be byte-identical to the pre-change tree,
    proving the fix is inert outside the declared fixture.
35. Scoped push targets exactly the selected, successfully rebased entries: unselected branches have
    unchanged remote SHAs, and the per-entry lines are keyed by logical `Name`.
36. Lifecycle ordering: `SyncStepHook` crash injection at each of the six points of §8.5 produces
    exactly the predicted matrix cell; a new-mode failure never rewrites the sentinel (its bytes are
    identical before and after a conflict); a crash in a healthy run **never** yields
    `{absent, valid}`; and the setup crash and the finalization crash yield the **same**
    `{sentinel, absent}` cell.
37. State matrix: each of the 12 cells of §8.6 is constructed **directly on disk** and the new
    binary's plain, `--continue`, and `--abort` behaviour is asserted per cell against the
    determinacy table of §8.7 — no cell/verb pair may be unasserted. Cells 2, 4, and 5 are asserted
    **twice**, once with a stale/absent guard and once with a live owning guard built through the
    §17.4 seam: under the live guard all three verbs are refused with the §8.7 live-guard messages
    and nothing on disk changes, including cell 2, where `--abort` MUST NOT run the §9.2 recovery.
    This includes that
    `{real legacy, absent}` reproduces the AC 1 output goldens under the §4.1 rule 1 comparison
    (closed path normalization plus the §17.1 wrapper, nothing else) on all three verbs
    (state files compared per §17.1), that
    `{sentinel, valid}` resumes scoped, that every unreadable cell fails closed and **deletes
    nothing**, and that cell 11 `{unreadable legacy, valid}` emits its **dedicated** §8.7 message
    on plain/`--continue` and its dedicated abort message, each naming the corrupt legacy path
    `<featurePath>/.sync-state.yaml` **and** the payload's real failed entry and that entry's
    worktree path — the generic unreadable-payload string MUST NOT appear for cell 11.
38. Payload-`Lstat` exception: with **only** a valid payload on disk (no sentinel), plain
    `tws sync <feature>` refuses before any fetch or rebase (unreachable `origin` proves it, and the
    §17.1 argv log shows no `fetch` and no `rebase` record), and
    `--continue` refuses. That payload-present variant uses an ordinary, **readable** `.sync-run.lock`
    where a guard is present at all, because a payload-present run legitimately reads the guard for
    classification, liveness, and message context (§3.6 step 8c, §8.6, §8.7).
    With **no** payload and **no** sentinel on disk the identical no-flag invocation reproduces the
    AC 1 output golden under the §4.1 rule 1 comparison (closed path normalization plus the §17.1
    wrapper, nothing else), with the same exit code — and **that** variant is the one carrying an
    **unreadable-mode `.sync-run.lock`** (mode `0000`, or an equivalent fail-if-opened guard whose
    successful `Lstat` and failing `Open` are asserted by the test's own setup check on platforms
    where mode `0000` is not honoured for the test user). The golden reproducing byte for byte with
    that guard in place is the direct proof that the no-flag payload-absent, sentinel-absent path
    **never opens the guard**: opening it would fail and the run would have to diverge from the
    golden (§4.4, §8.3 rule 7). A **symlinked**
    `.sync-state.v2.yaml` is refused with I18 on a no-flag run; a **symlinked** legacy
    `.sync-state.yaml` with no payload and no trigger flag is **followed**, exactly as today
    (§12 item 7).
39. Guard: two concurrent new-mode runs — the second is rejected with I16 before fetch and before
    any state write; two concurrent **no-flag** runs behave exactly as today. A stale guard with no
    payload is reclaimed silently by the next new-mode run, and a no-flag `--abort` against that
    guard-only residue prints today's `Nothing to abort — no sync in progress.` and leaves the guard
    file byte-identical; a stale guard **with** a payload is refused for a fresh run and reclaimed by
    `--continue`/`--abort`. A **live** owning guard beside a payload with **no** sentinel (cell 2)
    refuses plain, `--continue`, and `--abort` with the §8.7 live-guard messages and is never
    reclaimed, never removed, and never deleted, and the payload is left byte-identical. A guard
    whose `token` does not match the payload's is reported and never reclaimed.
40. Atomic legacy write and the declared cell-10 change (C1). With a deliberately truncated
    `.sync-state.yaml` (no payload, no guard) and **no** trigger flag, all three verbs are asserted
    against the exact §8.7 cell-10 strings, and against the pre-change behaviour they replace:
    - plain `tws sync <feature>` → `sync state at <featurePath>/.sync-state.yaml is unreadable: <err>`,
      exit 1, **no panic**. The pre-change behaviour of all three verbs on this exact input is
      captured once, during the AC 1 pre-change run, into a **declared-change evidence** directory
      that is explicitly **not** a frozen golden (`internal/cli/testdata/sync_noflag/declared_c1/`),
      so the diff between old and new is reviewable; the §9.6 prior binary is used for the same
      capture when it is available;
    - `tws sync <feature> --continue` → the same string, exit 1, replacing today's
      `nothing to continue — no sync in progress`;
    - `tws sync <feature> --abort` → `sync state at <path> is unreadable: <err>; inspect and remove it manually`,
      **exit 1**, replacing today's `Nothing to abort — no sync in progress.` at exit 0.

    For every one of the three verbs: the corrupt file still exists afterwards and is **byte-
    identical**, no `.sync-state.v2.yaml` or `.sync-run.lock` is created, no fetch and no rebase is
    issued (unreachable `origin` plus the §17.1 argv log), no branch SHA moves, and `stack.yaml` is
    unchanged. A mid-rebase worktree present at the same time is left mid-rebase, proving abort
    aborts nothing it cannot identify. This criterion is a **declared change**, not a frozen
    golden: no AC 1 capture covers a corrupt state file, and the pre-change strings above MUST NOT
    be asserted as still-current behaviour anywhere. Separately, a normal (decodable) failure write
    is mode `0644` with content equivalent to the pre-change tree under §17.1 (only `started_at`
    normalized), and an interrupted write never leaves a partial file, because the write is
    temp + `Sync` + rename.

### Status and import

41. With sentinel + payload on disk, `BuildAgentStatus` reports the **real** `failed_branch`, the
    real `pending`/`completed` arrays, and external liveness from the guard; the marker appears
    **nowhere** in the report; `sync-failed-branch` is attributed to the real entry; and a dirty
    worktree the scoped run needs is `worktree-dirty-blocking`.
42. Classifier-driven status ordering: `buildFeatureSync` observes every cell, not just the ones
    with a legacy file. With a **stale/absent** guard, `{sentinel, absent}`, `{absent, valid}`,
    `{unreadable legacy, valid}`, `{real legacy, valid}`, and guard-only residue each produce their
    §11.1 issue code and severity, with the required wording constraints (the `{sentinel, absent}`
    detail states the ambiguity and never claims no work was done; the cell-11 detail names the
    corrupt legacy path and the real failed entry). A dedicated regression asserts that
    `{absent, valid}` and `{absent, unreadable}` are **not** silently dropped — i.e. that the
    classifier runs before the `os.Stat(statePath)` early return — and that
    `{real legacy, absent}` still produces today's projection and today's committed goldens
    unchanged.
43. With a **live** guard, the `{sentinel, absent}`, `{sentinel, valid}`, and `{absent, valid}`
    inputs each produce an
    in-progress scoped-sync projection and **no** degenerate warning — in particular
    `{absent, valid}` under a live owning guard MUST NOT be projected as `IssueSyncInvalid`. An
    unreadable payload keeps
    its `IssueSyncStateInvalid` detail even under a live guard, worded as unreadable rather than
    dead. Live/stale comes from the controlled PID seam; no sleeping, no racing, no process kills.
44. Status mutates nothing: the bytes of `.sync-state.yaml`, `.sync-state.v2.yaml`, and
    `.sync-run.lock` are identical before and after a `tws status` run, and the committed
    legacy-state status goldens are unchanged.
45. `TestExport_RuntimeStateExcluded` gains `.sync-state.v2.yaml` and `.sync-run.lock` (both
    `true`), keeping every existing row unchanged. An end-to-end import of a tarball that
    deliberately contains `.sync-state.yaml`, `.sync-state.v2.yaml`, and a guard file at the
    feature-directory root plants **none** of them, and a subsequent plain `tws sync <feature>` in
    the imported feature is **not** refused. A feature directory containing a payload and a guard
    exports neither.

### Structure, invocation, and gates

46. Every cell of the §12.11 cwd matrix is exercised, per mode, for at least one scoped and one
    no-flag run. Frozen cells and declared cells are asserted differently, and both directions are
    required:
    - **Cells 1–4 and 7–8 (frozen).** On an input outside the declared C2/C3 defect fixtures of
      §4.1 rule 7, the no-flag run reproduces the AC 1 output goldens byte for
      byte under the §4.1 rule 1 comparison, with identical exit code, identical state files under
      the §17.1 comparator, identical refs and `stack.yaml`, and an argv log that matches the
      pre-change log exactly under §17.1 comparison mode 3 — i.e. differing **only** by the closed
      C4 carve-out of §4.1 rule 6: the checkout containment probe, whose output is asserted equal to
      `ws.RepoRoot` and whose position is asserted to be directly before the first
      `RunCheckoutSync` preflight record (`rev-parse --git-path rebase-merge`), with the
      workspace-resolution records that precede it unchanged, and the repo-scoped default-branch
      resolution compared as **one closed
      `DefaultBranchIn` logical event validated by resolved value** (same fixture-pinned default
      branch on both sides, every record in the window belonging to that event; **no** claim of
      equal invocation count or equal exit-status class).
    - **Cells 5 and 6 (declared, §4.1 rule 5).** Two fixtures. In the **agreeing** fixture, where
      `ws.ResolveFeaturePath` and `internal.FeaturePath` yield the same path, the no-flag run is
      asserted to be **identical to the pre-change tree** — output, exit code, refs, `stack.yaml` —
      proving the fix is inert on healthy layouts. In the **disagreeing** fixture (constructed
      through `TWS_ROOT`/workspace-detection), the no-flag run is asserted as a **declared change**:
      before the fix it fell into `syncFallback` and rebased every worktree onto the literal
      `origin/main`; after the fix it loads the resolved feature's `stack.yaml`, syncs the stack,
      and the §17.1 argv log shows the stack's real bases and **no** `origin/main` fallback rebase.
      The pre-change behaviour for this fixture is captured once, in the AC 1 pre-change run, into
      `internal/cli/testdata/sync_noflag/declared_c4/` and labelled **declared-change evidence, not
      a golden**, exactly as AC 40 does for C1.
    - **Cell 9 (declared, §4.1 rule 5).** Asserted for a scoped **and** a no-flag checkout
      invocation from a linked worktree of the `checkout` fixture's repository: the exact I19
      message, exit 1, and zero side effects — no lock file, no
      transaction, no `fetch`/`checkout`/`rebase` record in the §17.1 argv log, no header on stdout,
      and HEAD, every local branch SHA, every remote ref, and `stack.yaml` byte-identical before and
      after. The linked worktree itself is additionally asserted
      **unmutated**, which is the defect the refusal replaces. These captures are stored as
      declared-change evidence, never as frozen goldens.
    - **No outside-any-repository checkout cell is asserted**, because none is reachable: from such
      a cwd `RequireWorkspace` resolves external mode or returns `not inside a git repository or tws
      workspace` before checkout dispatch (§10.9). The containment probe's `err != nil` arm is
      defensive; AC 46 makes no claim about it and no evidence directory exists for it.
47. Archived / missing / prunable: explicitly naming an `Archived: true` entry is refused (I11);
    explicitly naming a non-materialized entry succeeds through the archived path; explicitly naming
    a prunable-worktree entry stops with today's message; and **unselected** instances of all three
    never affect a scoped run.
48. Cross-repo and multi-repo: a cross-repo edge is an anchor under `local-only`; a cross-repo entry
    explicitly selected in checkout mode is refused with I12, **raised by `ResolveSyncSelection`**
    (asserted directly as a pure unit call with
    `SyncSelectionOpts{Mode: ModeCheckout, NewMode: true, Feature: "<feature>"}`, and end to end
    through the command); the
    same selection with `Mode: ModeExternal` is **not** refused and classifies the entry as an
    anchor; a scoped multi-repo run fetches only the selected repos (AC 17).
49. Decoupled and duplicate names: selection by `Name` where `Branch` differs works; two selected
    entries sharing a `GitBranch()` are refused with I13 by `ResolveSyncSelection` for **every**
    new-mode caller, asserted in **both** workspace modes (`Mode: ModeExternal` and
    `Mode: ModeCheckout`, `NewMode: true`, `Feature: "<feature>"`), and are **not** refused in a
    no-flag run, which never calls the function.
50. `ResolveSyncSelection` unit tests cover: all/one/subtree membership, subtree root inclusion,
    unknown selector (I10), archived selector (I11), checkout cross-repo refusal (I12) and its
    external non-refusal, duplicate-`GitBranch()` refusal (I13) under `NewMode: true` in both modes
    and its absence under `NewMode: false`, anchor vs propagated classification including the
    cross-repo parent case, topological order preservation, and `Repos` computation — with **no**
    Git repository at all and no filesystem access, proving the function is pure over
    `(Stack, SyncRunPolicy, SyncSelectionOpts)`. The I10 and I11 cases additionally assert
    **`opts.Feature` interpolation** by exact string equality: with `Feature: "alpha"` and
    `--only ghost`, the error is exactly
    `unknown stack entry "ghost" in feature "alpha"; run: tws stack status alpha`; with
    `Feature: "alpha"` and `--only shelved` naming an `Archived: true` entry, it is exactly
    `stack entry "shelved" is archived; restore it with: tws new alpha shelved`; and re-running the
    same two cases with `Feature: "beta"` changes only the feature token, proving the value comes
    from `opts` and is never derived inside the function (§5.2).
51. Boundary greps pass: no `--force` without `-with-lease`; no `git reset --hard`; no
    `os.Exit`/`internal.Must` added on any new code path; `saveIncompleteSync` has exactly one
    caller and it is the no-flag path; `isSyncMarker` is the only marker predicate and is spelled
    exactly once, unexported, in package `internal` (`internal/sync_run_state.go`; no
    `IsSyncMarker` exists), and it has **exactly one caller**, `ClassifyExternalSyncState` — the
    grep asserts that `internal/agent_status.go` contains **no** occurrence of `isSyncMarker` at
    all, so `buildFeatureSync` neither calls nor branches on it (§8.2, §11.1);
    `newSyncMarker` and `syncMarkerFn` are declared exactly once, both
    unexported, both in package `cli` (`internal/cli/sync_modes.go`; no `NewSyncMarker` and no
    exported marker seam or setter exists), and package `cli` contains no reference to
    `internal.isSyncMarker` or `internal.newSyncMarker`;
    `ClassifyExternalSyncState` is the only external-state classifier and both `buildFeatureSync`
    and `classifySyncState` call it;     `ResolveSyncSelection` is the only place I10–I13 are raised, and the I10 and I11 format strings
    are each spelled exactly once, in `internal/sync_selection.go` (no caller re-formats them and no
    call site interpolates a feature name into a selection error);
    the exact I20 string of §3.5 appears exactly once per mode-owning call site and nowhere else;
    `staleStackEdgesFiltered` is the only stale-edge predicate; `TopoSort` and `Descendants` each
    have exactly one production implementation; **no `SortFlags` assignment exists anywhere in
    `internal/cli`** (help ordering is pflag's default, §3.1/§3.9); `RunCheckoutSync` contains at
    most one `LoadStack` call and at most one `ResolveSyncSelection` call, and the I9/I14 strings
    are constructed exactly once each; and no `git fetch` invocation exists in
    `internal/checkout_sync.go` outside the single pre-plan refresh of §10.3 step 11. Print
    ownership is asserted the same way (§3.10): package `internal` gains **exactly three** new
    print paths — `printSyncModeHeader`, the inline `Fetching default repo... ` line, and
    `printLocalOnlyNoOp` — each declared exactly once and unexported in
    `internal/checkout_sync.go`; `printLocalOnlyNoOp` has exactly one caller, `RunCheckoutSync`;
    `internal/cli/checkout_sync.go` contains no header print and no `Nothing to propagate.` print;
    and no other `fmt.Print*` call is added anywhere in package `internal`.
52. Full gates pass: `gofmt -l` empty for changed files, `go test ./... -count=1`, `go vet ./...`,
    `golangci-lint run ./...`, `make build`, `git diff --check`, and
    `tpatch feature deps --validate-all` reporting a clean DAG.
53. Multi-repo **no-flag** default-base regression (C4 rule 3): in a two-repo external fixture where
    the secondary repository's default branch differs from the primary's, a no-flag
    `tws sync <feature>` resolves each anchor's `origin/<default>` rewrite against **its own**
    repository, and the primary repository's entries produce output, refs, and `stack.yaml` values
    identical to the AC 1 golden. Because the secondary repository's pre-change and post-change
    resolutions deliberately **disagree** on the resolved default branch, that half is a **declared
    C4 disagreement fixture**: its pre/post argv logs and resolved values are stored as
    declared-change evidence (`internal/cli/testdata/sync_noflag/declared_c4/`) and reviewed, never
    required to be equivalent. A second case runs the same no-flag invocation in a fixture where
    cwd resolution already yields the correct default branch and asserts **zero** difference from
    the pre-change tree in output, exit code, refs, and `stack.yaml`, with the argv log differing
    **only** by the §4.1 rule 6b carve-out compared as one closed logical event whose two sides
    resolve to the **same fixture-pinned default branch** and contain no unrelated argv — with **no**
    assertion of equal invocation count or equal exit-status class, since the pre-change event runs
    in the process cwd and the post-change event in the repository and may legitimately take
    different arms — proving the change does not silently alter healthy no-flag default-base
    semantics. A third case, with no repo context available (`Repo` empty and no materialized
    worktree), asserts the resolution is exactly today's `internal.DefaultBranch()` result and that
    its argv carries **no** `-C` on either side, i.e. no carve-out is applied at all.
54. C5 old-binary compatibility: a transaction written by the new binary for a **no-flag** checkout
    run is loaded and resumed successfully by the prior binary of §9.6 (or the frozen replay
    harness), proving the additive `name:` key is ignored; and a transaction written by the prior
    binary (no `name:` key) is finalized by the new binary through the documented `GitBranch()`
    first-match fallback (§10.6).

### Checkout preflight and fetch boundary

55. **Checkout new-mode preflight runs before the lock, with zero side effects** (§10.3 steps 6–8).
    In the `checkout` fixture, each of the following new-mode invocations is asserted:
    - `stack.yaml` missing, and separately unreadable, with `--only x` → exact I9 string
      `sync modes require a stack; feature %q has no readable stack.yaml`, exit 1;
    - a base ref the selected plan needs deleted locally, run under checkout's default `no-fetch`
      (and again with an explicit `--no-fetch`) → exact I14 string
      `base %q for stack entry %q does not resolve locally; drop --no-fetch or fetch manually first`,
      exit 1;
    - `--only <unknown>` → I10, `--only <archived>` → I11, `--only <cross-repo>` → I12, and a
      selection with two entries sharing one `GitBranch()` → I13, each exit 1. The I10 and I11
      strings are asserted with the **feature name interpolated from `opts.Feature`** — exactly
      `unknown stack entry %q in feature %q; run: tws stack status %s` and
      `stack entry %q is archived; restore it with: tws new %s %s` with the fixture's feature name
      in the `%q`/`%s` feature positions — and are asserted **byte-identical to the external-mode
      refusal** for the same stack and selector, proving one owner formats both (§5.2).

    For **every** case: the message is emitted **verbatim and unwrapped** (no `load stack:` and no
    `build plan:` prefix, asserted by exact string equality); no `<feature>-checkout-sync.yaml`
    exists; **no checkout lock file exists** (asserted by inspecting the lock path and by a
    concurrent `AcquireCheckoutLock` succeeding immediately afterwards); the §17.1 argv log contains
    **no** `fetch`, `checkout`, or `rebase` record; the §3.7 header is absent from stdout; and HEAD,
    every local branch SHA, every remote ref, and `stack.yaml` are byte-identical before and after.
    A companion case proves the frozen path is untouched: a **no-flag** checkout run with a broken
    `stack.yaml` still acquires the lock first and still fails with today's `load stack: %w`,
    reproducing the AC 1 golden.
56. **Checkout `--fetch` boundary** (§6.2, §10.3 step 11). With the §17.1 wrapper in **record-only**
    mode (mandatory for every checkout invocation) recording argv and,
    at each `fetch` invocation, a snapshot of the feature's state directory:
    - exactly **one** `fetch` record exists, in `RepoDir`, with no `--prune`, `--tags`, `--all`, or
      extra refspec;
    - at fetch time the transaction file does **not** exist and the checkout lock **does**, proving
      the refresh is inside the lock and outside the transaction;
    - the `fetch` record precedes every `checkout` and `rebase` record and follows the guard reads;
    - across the fetch alone (asserted by a wrapper that exits immediately after the real fetch on
      a run whose plan is empty) only `refs/remotes/*` changed: HEAD, every local branch SHA, the
      index, the working tree, and `stack.yaml` are byte-identical;
    - with the bare remote removed, `--fetch` prints `Fetching default repo... ` + `failed` and the
      run still completes (exit 0), preserving §6.4 tolerance;
    - a run refused by I9–I14 (AC 55) issues no `fetch` at all, proving the refresh is ordered after
      the preflight.
57. **Interruption inside the pre-transaction fetch window** (§10.3). The measured invocation is the
    **built binary** run as a subprocess with `--fetch`; the §17.1 wrapper, in record-only mode and,
    for the `fetch` verb
    only and only in this test, delegates to real Git and then terminates its parent (`kill -9
    $PPID`) — deterministic, with no sleep, no polling, and no race. Asserted afterwards:
    the §3.7 header **is** on the captured stdout (output without state is real and declared);
    `<feature>-checkout-sync.yaml` does **not** exist; HEAD, every local branch SHA, the index, the
    working tree, and `stack.yaml` are unchanged; the remote-tracking refs are refreshed; and the
    lock file left behind carries the dead PID with no transaction. A second, ordinary invocation
    then starts **fresh**: it reclaims the stale lock through today's `AcquireCheckoutLock` rules,
    the argv log shows a **second** `fetch`, and the run completes normally. No `--continue` or
    `--abort` is required, offered, or accepted for that window, and no new stage exists for it.

---

## 17. Test matrix and harnesses

### 17.1 Golden harness

`internal/cli/sync_golden_test.go`. Swaps `os.Stdout`/`os.Stderr` for an `os.Pipe` around the
invocation and captures the two process streams separately, because sync output comes from bare
`fmt.Print*` calls and from Git subprocesses wired to the process streams — a Cobra output buffer
captures neither. Fixtures use the date-pinned builder pattern
(`internal/cli/stack_status_test.go:32-90`) so object IDs are byte-stable across runs and machines.
No golden pins multi-repo `Fetching …` ordering or sibling ordering: `UniqueRepos` and `TopoSort`
both seed from maps, and no golden pins Git's own prose (comparison mode 1b).

**Three comparison modes, deliberately different:**

1. **Output goldens — tws-owned bytes under the closed normalization of §4.1 rule 1.** A raw
   capture is not portable, so exactly two closed mechanisms are applied and nothing else.

   **(a) Path normalization.** `goldenReplacements`, `goldenApplyReplacements`, and
   `goldenAssertNoResidual` (`internal/cli/stack_status_test.go:421-492`) are reused verbatim. The
   sync harness supplies the enumerated table of §4.1 rule 1 — repository roots (`<REPO>`,
   `<REPO2>`, …), metadata root (`<META>`), resolved feature path (`<FEATURE>`), worktrees root
   (`<WORKTREES>`), bare remotes (`<REMOTE>`, `<REMOTE2>`, …), fixture root (`<ROOT>`), `HOME`, and
   `XDG_DATA_HOME` — each also registered in its `filepath.EvalSymlinks` form under the same token,
   substituted literally, **longest source first**. Stdout and stderr are normalized independently
   and stored as separate goldens; `goldenAssertNoResidual` then fails the test if any temporary
   root or the stable ID survives. Nothing else is rewritten: no regex over prose, no whitespace or
   ordering normalization, no per-line filtering.

   **(b) The `git` PATH wrapper.** Git's streamed prose (`Successfully rebased and updated …`,
   `CONFLICT (content): …`, `To /path/remote`, `hint:` blocks) is version- and platform-dependent
   and MUST NOT be pinned. The harness therefore installs a test-only wrapper:

   - **Scope.** A directory containing a single POSIX `sh` script named `git` is prepended to `PATH`
     **immediately before** the measured sync invocation and removed **immediately after** it. It
     is never installed around fixture construction, around the before/after `for-each-ref` and
     `stack.yaml` snapshots, or around any other test.
   - **Delegation.** The absolute path of the real `git` is resolved **before** the wrapper
     directory is prepended and passed to the script in a dedicated environment variable; the script
     never re-resolves `git` from `PATH`. Every invocation runs the real Git binary with the exact
     argv, cwd, and environment it was given.
   - **Verb detection.** The script skips the global option forms tws actually emits — `-C <path>`,
     `-c <k=v>`, `--git-dir=…`, `--work-tree=…`, `--no-pager` — and takes the first remaining
     argument as the verb.
   - **The stdout tee — the three authoritative read-only shapes, byte-identical in both modes and
     the one behaviour the wrapper MUST NOT make mode-dependent.** For **exactly three** closed
     argv shapes — the containment probe `rev-parse --show-toplevel` and the two `DefaultBranchIn`
     reads `rev-parse --abbrev-ref origin/HEAD` and `symbolic-ref --short HEAD` — the script MUST
     **not** `exec`, in **either** mode, because an `exec`ed child's stdout cannot be recorded and
     comparison mode 3 asserts the **resolved values** these three shapes print. It tees instead,
     with the identical sequence in divert mode and in record-only mode: run the real Git with the
     exact argv, cwd, and environment it was given, with **stdout redirected to a temporary file**
     and **stderr and stdin inherited untouched**; capture the real exit status; **replay the
     captured bytes verbatim** — in full, unmodified, unreordered, with no added or withheld byte —
     to the wrapper's own stdout, so tws's parsing of that stdout is bit-for-bit what an unwrapped
     run would parse; record **those same bytes** in the argv sidecar record below; remove the
     temporary file; and exit with the **real** Git exit status. This is a tee, never a diversion:
     the sidecar copy is a copy, not a redirection, and no caller of these three shapes ever sees a
     stream the wrapper emptied. Because the tee is mode-independent, the containment probe and the
     `DefaultBranchIn` reads carry their output in **every** capture, external and checkout alike,
     which is exactly what makes the two closed C4 carve-outs of comparison mode 3 assertable on
     both sides in both modes. A wrapper that `exec`s any of these three shapes in either mode
     records no output and violates this specification.
   - **Two modes, selected per capture by one environment variable** (`TWS_GIT_WRAPPER_DIVERT=1` or
     unset; never auto-detected, never inferred from argv). Neither mode alters the tee above; the
     modes differ only in what happens to the **other** verbs:
     - **Divert mode — external-mode captures only.** For **`rebase`, `fetch`, and `push` only**,
       the child's stdout and stderr are redirected to sidecar files and the script exits with the
       real Git exit status. Those three **mutating** verbs are the only diverted ones, and they
       are diverted only in this mode. For **every other verb** — the three teed read-only shapes
       excepted, which behave exactly as the tee bullet above requires — the script `exec`s the
       real Git with stdout, stderr, and stdin inherited and untouched. That is mandatory, because
       tws parses read-only Git stdout (`rev-parse`, `merge-base`, `symbolic-ref`, `for-each-ref`,
       `status`, `check-ref-format`) and diverting it would change behaviour rather than only
       capture output; the tee preserves those bytes exactly, which is why recording them is not a
       diversion.
     - **Record-only mode — checkout-mode captures, and the default.** The script `exec`s the real
       Git with stdout, stderr, and stdin inherited and untouched for **every** verb — again
       excepting only the three teed read-only shapes, which tee rather than `exec` so their stdout
       can be recorded, and whose bytes reach the caller unchanged either way — `rebase`,
       `fetch`, and `push` included; the only thing it does is append the argv record below. This
       mode is **mandatory** for every checkout capture: checkout runs Git through
       `cmd.CombinedOutput()` / `cmd.Output()` and *parses and persists* those bytes —
       `gitRebaseOnto` / `gitPlainRebase` classify `*RebaseConflictError` from them, and the
       resulting `rebase conflict: <output>` string is persisted as `failure_msg` with
       `failure_kind: conflict` (`internal/checkout_sync.go:328-364,890-912`). A wrapper that
       diverted them would empty `CombinedOutput` and silently turn a conflict into
       `failure_kind: switch` — a behaviour change, not a capture technique. Checkout
       `CombinedOutput` MUST therefore see the real bytes (§4.1 rule 1b).
   - **Argv log.** Every invocation, in either mode, appends one record — ordinal, verb, full argv,
     the child's cwd (path-normalized by the same table), and exit status — to a sidecar log. The
     cwd is what lets comparison mode 3 place each record and identify the window of a
     `DefaultBranchIn` event on either side. For the **exactly three** closed argv shapes of the
     tee bullet above — the containment probe `rev-parse --show-toplevel`, and the two
     `DefaultBranchIn` reads `rev-parse --abbrev-ref origin/HEAD` and `symbolic-ref --short HEAD` —
     the record additionally carries the child's stdout, so comparison mode 3 can assert that the
     C4 containment probe resolved to `ws.RepoRoot` and that the pre-change and post-change
     default-branch events resolved to the **same fixture-pinned value**. These three shapes MUST be
     declared as literal constants beside the carve-out constants; no other record carries output.
     The stdout recording of these three shapes is performed **identically in both wrapper modes** —
     stdout captured to a temp file, replayed verbatim to the wrapper's stdout, stderr inherited,
     real exit status preserved, temp file removed — so tws parses the same bytes on every capture
     and the recorded copy is a tee, never a diversion. The argv
     log is mode-independent, so every argv/exit assertion
     in §16 works identically for external and checkout captures.
   - **Consequences, stated exactly.** In divert mode, for the three diverted **mutating** verbs the
     wrapper
     writes nothing to the inherited streams. For `rebase` and `push` that means tws's own stderr
     filter (`RunDirClean` / `runWithFilteredStderr`, `internal/exec.go:138-186`) reads an **empty**
     stderr stream and therefore emits none of its output: no forwarded Git line, and none of its
     two tws-owned transformations (`hint:` / `Disable this message` removal, and the
     `skipped previously applied commit` → `    (skipped duplicate commit)` reformat). For `fetch`
     the diversion is inert on the frozen path, because the non-verbose fetch already runs through
     the silent helpers `internal.RunSilentDir` / `internal.RunSilent`, which wire no stream at all.
     tws's own lines around them — `Fetching <label>... `, `done`, `failed`, `  [+] NAME`,
     `  [!] NAME`, `Sync complete.` — are unaffected and remain in the compared golden. Exit status
     is preserved exactly, so conflict detection, `done`/`failed`, state transitions, and exit codes
     are still driven by **real** Git behaviour, and argv, cwd, and every persisted state decision
     are identical to an unwrapped run. The three teed read-only shapes are **not** part of this
     consequence in either mode: their stdout is replayed to tws verbatim, their stderr is
     inherited, and their exit status is real, so the tee is observationally inert and nothing that
     reads `rev-parse --show-toplevel`, `rev-parse --abbrev-ref origin/HEAD`, or
     `symbolic-ref --short HEAD` can tell a wrapped run from an unwrapped one.

     **The coverage gap this creates is declared, not papered over.** Because the diverted stream is
     empty, **no no-flag golden pins the `    (skipped duplicate commit)` reformat**, and this
     specification MUST NOT claim anywhere that the goldens cover `RunDirClean`'s stderr filtering.
     Two obligations follow, both binding:

     - Any existing focused test owned by the `clean-git-output` parent feature MUST remain
       **unchanged** — neither adapted to the wrapper nor re-pointed at a golden. `RunDirClean` and
       its filter are consumed verbatim by this feature (§15, soft dependency), so no such test may
       be edited to accommodate the harness.
     - A **direct regression assertion outside the wrapper** MUST exist when this feature lands —
       retained if one already covers the filter, added otherwise (no test in the tree asserts
       `runWithFilteredStderr` today, so in practice it is added): a
       focused test that runs `internal.RunDirClean` against a child emitting, on stderr, a
       `hint:` line, a `Disable this message` line, a line containing
       `skipped previously applied commit`, and an ordinary error line, with the process `os.Stderr`
       captured, and asserts byte for byte that the first two are dropped, that the third produces
       exactly `    (skipped duplicate commit)\n`, that the ordinary line is forwarded verbatim, and
       that the child's exit status is returned unchanged. The child is a plain command, not the
       golden harness, and the §17.1 `git` PATH wrapper is **not** installed around it. This is the
       **only** coverage of that reformat; no golden may be cited for it.

     In record-only mode nothing about the run changes at all — the mode-independent stdout tee of
     the three read-only shapes included, since it hands tws the same bytes real Git wrote — and
     the checkout goldens stay portable anyway: checkout never wires a Git child to
     `os.Stdout`/`os.Stderr`, so no Git prose reaches the captured process streams and the checkout
     goldens (`Checkout sync complete.`, `Checkout sync completed.`, the §3.7 header, the no-op
     block, the CLI's error lines) are tws-owned bytes in both modes.
   - **Separate assertions instead of pinned prose.** The sidecar log is asserted on its own,
     under comparison mode 3 below:
     the ordered list of `(verb, argv)` records — path-normalized by the same table of (a) — and
     each record's exit-status class (zero / non-zero). Git prose is asserted **nowhere**, in
     either mode: in divert mode it lands in the sidecar and is not compared, and in record-only
     mode it is consumed by tws itself and reaches only `failure_msg`, which comparison mode 2
     handles through the single conditional rule of §4.1 rule 2. The argv log is the same evidence
     AC 21 uses for `--update-refs`, so no second Git-recording mechanism exists.
   - **Real-Git integration tests are not weakened.** The wrapper is confined to the golden harness.
     Every behavioural, recovery, matrix, cwd, downgrade, and multi-repo criterion of §16 runs real
     Git unwrapped with unmodified streams. The wrapper never stubs, simulates, replaces, delays, or
     alters Git behaviour; in divert mode its only effect is the destination of the three
     **mutating** verbs' output streams, and in record-only mode it has no effect on the run at all.
     The stdout tee of the three read-only shapes has no effect on the run in **either** mode,
     because the captured bytes are replayed to the caller verbatim.

   Compared this way, stdout, stderr, and the exit code are compared **byte for byte** against the
   committed goldens.
2. **State files — semantic comparison over `yaml.Node`.** Raw-byte equality across two
   independently created state files is impossible (timestamps, PIDs, and random tokens differ by
   construction), so it is **not required anywhere in this specification**. The comparator
   `compareStateSemantic(t, wantPath, gotPath, spec stateCompareSpec)`:

   - decodes both documents into `yaml.Node` trees, preserving key order;
   - asserts the **file mode** matches the reference;
   - asserts the key set and key order are identical at every level, after removing exactly the
     keys listed in `spec.AdditiveKeys` from the *new* document (`{"plan[].name"}` for the checkout
     transaction, C5; empty for every other file);
   - asserts every value not listed in `spec.DynamicKeys` is byte-identical to the reference;
   - asserts every key listed in `spec.DynamicKeys` is **present** and matches its declared shape:
     `rfc3339UTC` for `started_at`, `updated_at`, `lock_created`, `created`; `positiveInt` for
     `lock_pid`, `pid`; `hex32` for `token`, `owner_token`; `markerPattern` (§8.2) for `marker`;
   - applies exactly one **conditional** rule, `conflictFailureMsg`, and only to a
     `<feature>-checkout-sync.yaml` document whose `failure_kind` is `conflict` (§4.1 rule 2): it
     asserts `failure_kind == "conflict"` and `stage == "conflict"` as pinned literals, asserts
     `failure_msg` is present, starts with the exact prefix `rebase conflict: `, and has a
     **non-empty** suffix, and only then rewrites that suffix to `<GIT-CONFLICT-OUTPUT>` in both
     documents before the value comparison. When `failure_kind` is anything else — or absent —
     `failure_msg` is compared byte for byte like any other static value; that branch is the
     comparator's default behaviour and **no committed reference exercises it with a non-empty
     value**, because the frozen set contains no `switch`, `validation`, `ancestry`, `restoration`,
     `persistence`, `interruption`, or push-failure transaction (§4.1 rule 2). This rule exists because
     checkout persists Git's own rebase output (§4.1 rule 1b) and that output is Git-version- and
     platform-dependent; it is the **only** conditional entry, and it MUST be declared as a literal
     constant beside the closed dynamic sets.

   The closed dynamic sets are exactly those of §4.1 rule 2 and MUST be declared as literal
   constants in the test file, never computed: a field that becomes dynamic without being added to
   the list fails the comparison, and a field added to the list is a reviewable diff. Any extra key,
   missing key, reordered key, mode change, or changed static value fails.

   Where the test controls creation end to end it MAY instead inject deterministic seams — a
   `Now func() time.Time` and a PID provider — but no such seam exists in `SaveSyncState`
   (`internal/syncstate.go:54`) or in the checkout transaction writer
   (`internal/checkout_sync.go:550-552`) today, and this feature does **not** add production seams
   solely for goldens. The semantic comparator is therefore the normative path, and any seam added
   later is an optimization, not a requirement.
3. **Argv log — exact ordered match under exactly one closed carve-out (the two C4 events).**
   For every frozen no-flag capture (cells 1–4, 7–8) taken on an input **outside** the declared
   C2/C3 defect fixtures of §4.1 rule 7, the pre-change and post-change sidecar logs
   are compared as ordered `(verb, argv, exit-status class)` records, path-normalized by the same
   table of mode 1(a). The comparison is **exact**: every record MUST match its pre-change
   counterpart in verb, in every flag and operand, in position, and in exit-status class. Exactly
   one carve-out exists; it is **closed**, contains exactly the two read-only C4 argv carve-outs of
   §4.1 rule 6, and MUST be declared as literal constants in the test file beside the closed
   dynamic sets — never computed, never discovered, never widened by a helper. The **decoupled-name
   `--push` fixture (C2)** and the **duplicate-`GitBranch()` checkout fixture (C3)** are not
   compared under this mode at all: their logs are declared-change evidence for AC 33 and AC 34.

   - **`c4ContainmentProbe` — checkout only, exactly one *added* record at one exact position.**
     The post-change log of a checkout capture MUST contain **exactly one** record absent from the
     pre-change log, whose argv is exactly `git -C <cwd> rev-parse --show-toplevel`. The probe is
     **not** required to precede every pre-change record, and a comparator that requires that is
     wrong: `RequireWorkspace` and `RequireFeaturePath` complete first and their Git reads
     (`rev-parse --git-common-dir` and any other resolution read) still come first on both sides.
     The required shape is a three-part split of the post-change log:

     1. the pre-change log's **workspace/feature-resolution prefix**, matched **verbatim** in argv,
        order, and exit-status class;
     2. **exactly one** added containment record, `git -C <cwd> rev-parse --show-toplevel`;
     3. the **remaining pre-change records**, matched **verbatim** in argv, order, and exit-status
        class.

     The split point is anchored implementably: the probe is inserted **immediately before the first
     `RunCheckoutSync` preflight record**, which is `rev-parse --git-path rebase-merge`
     (`gitOperationInProgress`, §10.3 step 2). The comparator locates that record in the pre-change
     log, requires the added probe to sit directly before it, and requires everything earlier to be
     the untouched resolution prefix. For the `--continue` and `--abort` captures, whose entry point
     is `ContinueCheckoutSync`/`AbortCheckoutSync` rather than `RunCheckoutSync`, the same rule
     applies with the anchor being that path's own **first** pre-change Git record: the probe is
     always the last record of part 1's boundary — directly after the workspace/feature-resolution
     prefix and directly before the first record the checkout-sync operation itself emits. The comparator MUST additionally assert that this record's
     exit status is zero and that its **output equals `ws.RepoRoot`** after `filepath.Clean` and
     `filepath.EvalSymlinks` on both sides. That assertion is possible because
     `rev-parse --show-toplevel` is one of the **three** closed stdout-carrying argv shapes declared
     in the argv-log bullet above — `rev-parse --show-toplevel`,
     `rev-parse --abbrev-ref origin/HEAD`, and `symbolic-ref --short HEAD` — for which, and only for
     which, the wrapper tees the child's stdout beside the argv record, **identically in both
     wrapper modes**; the other two carry the
     resolved values `c4DefaultBranchProbe` compares. The tee never diverts — the captured bytes are
     replayed verbatim to the caller — so tws still parses the
     same bytes. Zero added probes, two or more added records, a probe anywhere other than directly
     before `rev-parse --git-path rebase-merge`, any change to the resolution prefix or to the
     records that follow, a non-zero exit, or an output that is not `ws.RepoRoot` is a **failure** —
     §4.1 rule 6a makes exactly one successful probe mandatory on every checkout run.
   - **`c4DefaultBranchProbe` — external only, the *whole `DefaultBranchIn` logical event*, compared
     by resolved value.** The carve-out unit is **not** a single record. It is the complete,
     closed default-branch resolution event, on each side independently, in exactly one of its three
     legal shapes (§4.1 rule 6b):

     1. one **successful** `rev-parse --abbrev-ref origin/HEAD` (`-C`-scoped post-change); or
     2. a **failed** `rev-parse --abbrev-ref origin/HEAD` followed by `symbolic-ref --short HEAD`
        (`-C`-scoped post-change); or
     3. **both** failing, followed by the hard-coded `main` fallback, which contributes no further
        record.

     The two sides **may differ in record count and in exit-status class**, because the pre-change
     event runs in the process cwd (in an external workspace typically the workspace root or a
     feature directory, i.e. not a repository) while the post-change event runs in the materialized
     repository context. The comparator MUST NOT require the same count, the same exit-status class,
     or a difference of only a leading `-C <path>`; asserting any of those would be false and is
     forbidden.

     For a **frozen** fixture the comparator asserts exactly two things and nothing more:

     - **Resolved-value equality.** The default branch **resolved** by the pre-change event and by
       the post-change event is the **same** string, equal to the value **pinned as a fixture
       constant**. The resolved value of shape 1 is the `rev-parse` output, of shape 2 the
       `symbolic-ref` output, and of shape 3 the literal `main`; the comparator reads it from the
       event's own recorded output, so no separate re-derivation is needed.
     - **Closed-event containment.** Every record inside the compared window on either side belongs
       to exactly this event — one of the three shapes above, with those verbs, flags, and ref
       operands, in that order — so no unrelated argv can hide inside the carve-out. A `fetch`,
       `push`, `rebase`, `checkout`, `merge-base`, `for-each-ref`, `status`, or `check-ref-format`
       record inside the window fails the comparison.

     If the two sides resolve to **different** default branches, that fixture is not frozen: it is a
     declared C4 disagreement fixture (the multi-repo and wrong-cwd cases the fix exists for), and
     its pre/post logs and resolved values are stored and reviewed as **declared-change evidence**
     (AC 46, AC 53) instead of being required to be equivalent. A frozen fixture that disagrees is a
     **failure**. Where the entry has no repo context, no `-C` appears on either side and the
     records compare verbatim with no carve-out applied.

   **No other argv difference is permitted in a compared capture.** Any added, removed, reordered,
   or otherwise
   altered record — in particular any difference in a `fetch`, `push`, `rebase`, or `checkout`
   record, or in any `rev-parse`, `merge-base`, `for-each-ref`, `status`, `symbolic-ref`, or
   `check-ref-format` record outside the two carve-outs above — fails the comparison and is a
   regression, not a declared change. This mode applies to **frozen** captures only: the declared
   cells 5, 6, and 9 (C4) and the declared C2/C3 defect fixtures are not compared against a
   frozen baseline at all, and their argv logs are the declared-change evidence of AC 46, AC 33, and
   AC 34 respectively.

### 17.2 Fixture inventory

| Fixture | Shape | Used by |
|---|---|---|
| `linear` | `root → parent → child` in one repo, bare remote | AC 1, 12–26 |
| `subtree` | `root → a → {b, c}`, `c → d` | AC 20, 22, 25 |
| `decoupled` | `name: work`, `branch: user/work` — **declared C2 fixture**: no-flag `--push` captures live in `declared_c2/`, never in the frozen goldens (§4.1 rule 7) | AC 33, 49 |
| `duplicate-branch` | two names, one `Branch` — **declared C3 fixture**: no-flag checkout captures and its `stack.yaml` live in `declared_c3/`, never in the frozen goldens (§4.1 rule 7) | AC 19, 34, 49 |
| `multi-repo` | two repos, cross-repo base edge, secondary repo whose default branch differs from the primary's | AC 17, 48, 53 |
| `archived` | one `Archived: true` entry, one non-materialized entry, one prunable worktree | AC 47 |
| `literal-root` | roots spelled `master`, `origin/master`, and a tag | AC 18 |
| `checkout` | `.tws/features/**` single checkout, plus a linked worktree for cwd cell 9 and a bare remote that can be removed | AC 6, 13, 46, 55–57 |
| `cwd-agree` | external feature directory where `ws.ResolveFeaturePath` and `internal.FeaturePath` resolve to the **same** path (cells 5–6, frozen half) | AC 46 |
| `cwd-disagree` | external feature directory where the two derivations **disagree** (constructed through `TWS_ROOT`/workspace detection), so the pre-change run falls into `syncFallback` (cells 5–6, declared half) | AC 46 |

### 17.3 Crash and failure injection

- Checkout: existing `StepHook` at each stage (`internal/checkout_sync.go:298-303`).
- External: new `SyncStepHook` at the six ordering points of §8.5 — after guard, after sentinel,
  after payload, during rebase, after payload delete, after sentinel delete.
- Conflicts are produced by real conflicting commits, never by simulated Git failures.
- Unreachable remotes are produced by `os.RemoveAll` on a bare remote directory, so any network
  attempt fails observably.
- Checkout's pre-transaction fetch window (§10.3 step 11) has no stage and therefore no `StepHook`.
  AC 57 injects its crash through the §17.1 wrapper instead: for the `fetch` verb only, and only in
  that test, the wrapper — still in **record-only** mode, so every stream stays inherited and
  checkout's `CombinedOutput` classification is untouched — delegates to real Git and then
  terminates the tws process it was spawned by
  (`kill -9 $PPID`), which is deterministic and kills only the subprocess the test started — never
  the test binary, never an unrelated process, and with no sleeping or polling. The measured
  invocation for that criterion is therefore the built binary, not an in-process call.

### 17.4 Deterministic liveness seam

```go
var syncProcessAlive = isProcessAlive   // internal/sync_run_state.go
```

Tests set it to a controlled predicate, and write guard files with the test's own PID for `live` and
with a PID asserted dead for `stale`. Every state in §8.6 is constructed on disk directly. **No**
test sleeps, races a real sync process, hard-kills a process, or assumes anything about how long a
setup or teardown window lasts.

### 17.5 Downgrade harness

Per §9.6: `TWS_DOWNGRADE_BINARY` → offline tag build → frozen replay harness, in that order, with a
fidelity test comparing the harness against the real binary whenever one is available. The
downgrade criteria never skip entirely.

### 17.6 Portability rules

`t.Setenv("GIT_CONFIG_COUNT", "0")` in every Git-touching test; `GIT_CONFIG_NOSYSTEM=1`,
`GIT_CONFIG_COUNT=0`, and a temp `HOME` in every `cmd.Env`; pinned author/committer identity and
dates for golden fixtures; `filepath.Join` everywhere; no `/proc`; no GNU-only flags; no reliance on
`sh` being bash; and no test that depends on network access or on a tag existing. The §17.1 `git`
wrapper is a POSIX `sh` script with no bashisms, no GNU-only utilities, and no `sed -i`; it resolves
the real Git through an absolute path passed in the environment, uses only `$PPID`, `exec`,
plain redirection, and one temporary file per teed read-only invocation (created under that same
temporary directory and removed before the wrapper exits), selects divert or record-only mode from
one environment variable, and is created
under the test's own temporary directory. Because Git prose is never compared — diverted to a
sidecar in external captures and never reaching the process streams at all in checkout captures
(§4.1 rule 1b) — every fixture path is normalized (§4.1 rule 1a), and the one Git-version-dependent
persisted value **the frozen reference set contains**, a checkout conflict's `failure_msg` suffix,
is covered by the single conditional
comparator rule of §4.1 rule 2, the committed
goldens are identical on macOS and Ubuntu and across Git versions.

---

## 18. Follow-ups (explicitly not this feature)

1. **Rebase plan guard** — old base, new base, replay count preview. Roadmap P1, and the reason §2
   item 1 forbids any preview here.
2. **Collapse the duplicate ancestry logic** (D6) — fold `staleStackEdges` and
   `branchContainsConfiguredParent` onto the shipped `StackEdge` evaluator, once the full-scope
   failure block's exact bytes can be preserved or are deliberately re-baselined.
3. **Strict fetch policy** — an opt-in that makes a failed fetch fatal (§6.4).
4. **External `--test`** (D16) — make the flag effective in external mode, or remove it from
   external help.
5. **Unify validation execution** (M8) — one source and one execution model across modes.
6. **External dirty/detached guards** (M15) — if ever wanted, as a declared breaking change.
7. **Selective `tws status`** — the other half of the retrospective's selective push/status gap.
8. **Portable process birth identity** — close the PID-reuse window that both the checkout lock and
   the new run guard share (already on the roadmap).
9. **Legacy `.sync-state.yaml` symlink refusal** — extending I18 to the ordinary no-flag legacy
   path, as a declared breaking change to frozen behaviour (§12 item 7).

---

## 19. Mismatch register — M1–M15 resolved

| # | Concern | Resolution |
|---|---|---|
| M1 | Fetch | **Kept divergent by default, harmonised by flag.** external default `fetch`, checkout default `no-fetch`; `--fetch`/`--no-fetch` make every cell explicit in both modes (§6.1, §6.2) |
| M2 | Parent base | **Divergent under `full`** (external: parent `GitBranch()`; checkout: literal), **converged under `local-only`** (both use the parent's `GitBranch()`). Authority cited: `StackBasePolicy` (§6.3) |
| M3 | Root base | **Divergent under `full`** (external rewrites `base == default` to `origin/<default>`; checkout stays literal), **irrelevant under `local-only`** because anchors are never rebased (§6.3, §6.6) |
| M4 | Run identity | **Unified on logical `StackEntry.Name`.** Checkout's plan gains `Name` on **every** run, no-flag included (C5), so the `Name`-keyed `LastBaseSHA` attribution of C3 also fixes the frozen path; Git operations keep `GitBranch()` (§4.5, §5.3, §7.4) |
| M5 | Archived | **Selection validation unified on the metadata `Archived` flag** (I11 in both modes); **execution filters left mode-specific** and documented (external: directory absence; checkout: the flag) (§5.5, §5.7) |
| M6 | Cross-repo | **External**: anchor, never a propagation edge. **Checkout**: an explicitly selected cross-repo entry is refused (I12), matching `cross-repo-unsupported` (§5.5) |
| M7 | Rebase flags | External keeps `--update-refs` for full scope and **drops it for scoped runs** (D5); checkout keeps `--no-fork-point` and never gains `--update-refs` (§7.1, §7.4) |
| M8 | Validation | **Kept divergent and documented.** External `cfg.TestCommand` + `strings.Fields`; checkout `--test` + `sh -c`. Both are frozen into state for new-mode runs. Unification is a follow-up (§7.7, §18.5) |
| M9 | `--push` on continue | External **persists** push in the payload and rejects only an explicitly supplied conflicting value; checkout keeps its one-way rule for legacy transactions and gains a symmetric rule for v2 (D8, §10.5) |
| M10 | Abort | **Kept divergent.** External abort remains state cleanup with no branch rollback; checkout abort keeps restoring the original branch. Making external roll back would be a new, unrequested behaviour with no pre-sync snapshot to roll back to (§4.2 item 4, §4.3 item 13) |
| M11 | Completion gate | **Both become scope-relative.** External filters the existing predicate to the selected propagation edges; checkout's per-plan loop is already correct once the plan is scoped (§7.5) |
| M12 | State durability | External state becomes **atomic** while keeping mode `0644` and an identical key set, key order, and values (C1; only the inherently per-run `started_at` varies, §4.1); **unreadable** legacy state now fails closed on all three verbs, including `--abort` (declared C1 change, §8.6 row 10); the new payload and guard are `0600`; checkout is unchanged apart from the additive plan `name` key (C5) (§8.1) |
| M13 | Concurrency | External gains an `O_EXCL` run guard **for new-mode runs only**; no-flag concurrency is untouched and its residual race is documented (§8.3, §8.8) |
| M14 | Push ref | **Fixed here** for both `tws push` and `tws sync --push`: `entry.GitBranch()` (D13, C2, §7.6). Declared as an observable no-flag change on decoupled names (§4.1 rule 7, §4.5 C2, AC 33); inert for coupled names |
| M15 | Guards | **Kept divergent.** Checkout keeps dirty/detached/in-progress/lock rejections; external gains none, for no-flag **and** scoped runs (§10.8, §12.8) |

## 20. Decision register — D1–D20 resolved

| # | Decision | Settled as |
|---|---|---|
| D1 | Flag shape | **Three independent axes**: `--fetch`/`--no-fetch`, `--full`/`--local-only`, `--only`/`--from`. No `--mode` enum (§3.1) |
| D2 | Subtree root inclusion | **Included.** `--from X` = `{X} ∪ Descendants(X)` (§5.2) |
| D3 | `local-only` with no in-stack parent edge | **No-op success, exit 0**, with the explicit no-op block (§3.7, §5.6) |
| D4 | Ancestors of a selection | **Anchors**, never prerequisites; no upward auto-expansion (§5.6) |
| D5 | `--update-refs` in scoped runs | **Dropped for scoped runs, kept for full scope**; `markUpdatedAncestors` is not called when scoped (§7.1, §7.2) |
| D6 | Collapse onto `StackEdge` | **Keep and scope now**; collapse is a follow-up (§7.5, §18.2) |
| D7 | State versioning | **Added** (`SyncRunStateVersion = 2`, `CheckoutTransactionVersion = 2`) for **new→future protection only**; absent = legacy; unknown = refused. Never described as downgrade protection (§8.1) |
| D8 | External `--push` on `--continue` | **Persisted in the payload**; rejected only when explicitly supplied and conflicting. Plain `--continue` accepts persisted `push: true`. Checkout's symmetric rule applies only to v2 transactions. A `--continue` carrying any **trigger** flag is allowed to compare at all only against v2 state; against absent or legacy state it is refused by I20 rather than silently ignored or broad-resumed (§3.5, §10.5 rules 0–5) |
| D9 | cwd resolution | **Fixed here**, because the invocation matrix fails today — D9's own escape clause. Three bounded code fixes (C4) covering **two** declared matrix cells — external 5/6 and checkout 9; there is no outside-any-repository checkout cell, because `RequireWorkspace` resolves external mode or errors before checkout dispatch, leaving the probe's error arm defensive only. The `resolveBase` fix is repo-scoped **only** where a repo context exists, and is otherwise exactly today's `DefaultBranch()`. The two **read-only** Git argv changes the fixes imply — the checkout containment probe and the repo-scoped `DefaultBranchIn` event, the latter compared as one closed logical event validated by resolved value, with no claim of equal command count or exit-status class — are declared as a closed carve-out in §4.1 rule 6 and asserted by AC 2 (§10.9, §13.4, §12.11, AC 46, AC 53) |
| D10 | `syncFallback` | **Refused when any trigger flag is present** (I9); the no-flag path, including `origin/main` and `internal.Must`, is untouched (§4.2.7) |
| D11 | Cross-repo selection in checkout | **Refused** (I12), by `ResolveSyncSelection` under `SyncSelectionOpts{Mode: ModeCheckout}`; never refused in external mode (§5.2, §5.5) |
| D12 | Archived selection semantics | **Unified for selection validation** on the metadata flag; execution paths unchanged per mode (§5.5, §5.7) |
| D13 | `pushFeature` ref defect | **Fixed here** (C2), for `tws push` and `tws sync --push` alike (§7.6), and declared as an observable no-flag change on decoupled `Name`/`Branch` entries — push argv, per-entry line, exit code, and remote ref (§4.1 rule 7, §4.5 C2, AC 33) |
| D14 | `no-fetch` and `--push` | **Allowed.** `no-fetch` constrains input refs; `--push` is explicit output. Help and docs say "no automatic network input", never "offline" (§6.2) |
| D15 | Atomic external state | **Now**, keeping mode `0644` and an identical key set, key order, and values, with the coupled decode-error hardening declared per verb for unreadable state, `--abort` included (C1, §4.1 rule 4, §4.5, §8.6 row 10) |
| D16 | External `--test` | **Left inert**; changing it is a follow-up (§7.7, §18.4) |
| D17 | New-mode external concurrency | **`O_EXCL` run guard**, claimed before persistence and before fetch, for new-mode runs only; fresh runs refuse a stale guard with a payload, `--continue`/`--abort` reclaim it; released last; filtered by `isRuntimeState`; no-flag concurrency untouched (§8.3) |
| D17b | Downgrade safety mechanism | **Fail-closed**: versioned payload in a file old binaries never open, plus a legacy-path sentinel whose `failed_branch` is a per-run `crypto/rand` marker ending in `.lock`, collision-refused before any side effect; symmetric ordering with the sentinel outermost; `saveIncompleteSync` unusable for new-mode runs; guarantee scoped to "while the sentinel exists" (§8.2, §8.5, §9.1) |
| D18 | New-binary handling of the state matrix | **Refuse everywhere except** `{real legacy, absent}` (frozen legacy) and `{sentinel, valid}` (authoritative, resumable with a stale/absent guard). Full 12-cell table with guard precedence — live-guard precedence covers rows **2**, 4 and 5 — in §8.6 |
| D18b | New-binary UX for its own marker | **Intercept** before the legacy error; discriminate from guard liveness + payload state; reclaim with `forceAcquireCheckoutLock` semantics; ordinary legacy state keeps today's message byte for byte (§8.7) |
| D19 | `tws status` behaviour | **Marker-aware, read-only projection with live-guard precedence**, driven by the shared `internal.ClassifyExternalSyncState` called **before** the `os.Stat(statePath)` early return so every cell is observable; real names only; existing issue codes; no new keys, no schema bump; status never mutates; deterministic PID seam in tests (§11.1) |
| D20 | `tws import` filtering | **Extend `isRuntimeState`** with the two new exact names; export unchanged and already allow-listed (§11.2) |

## 21. Declared refinements of analysis recommendations

Everything in the analysis's D-table is adopted as written except where noted here. Each refinement
is a decision, taken deliberately, with its reason.

1. **§3.3 "explicit selection of an entry with no materialized worktree is an error."** Refined to:
   it is **allowed** and handled by the existing non-materialized rebase path (§5.7). The analysis's
   concern is that the entry must not be *silently* skipped, which pass 1's `continue` appears to do
   — but pass 2 already rebases it correctly. Erroring would delete real capability. What the
   specification forbids is the silence: the entry is always reported, as `[+] NAME (archived)`,
   `[!] NAME (archived)`, or the prunable stop.
2. **§4.2 rule 5 / D15 "make external state writes atomic."** Adopted, with two additions the
   analysis did not state. First, the file mode stays `0644`: switching it to `0600` would be an
   observable change to frozen no-flag state, and atomicity does not require it. Second, the
   decode-error hardening that necessarily accompanies it is declared as a **behaviour change on
   all three verbs** for an unreadable `.sync-state.yaml` (cell 10): plain stops panicking,
   `--continue` stops reporting "no sync in progress" for a file that exists, and `--abort` fails
   closed with exit 1 instead of silently treating corrupt state as absent at exit 0. Hiding the
   abort half — the only verb an operator reaches for after the panic — would leave the defect
   half-fixed and the documentation false, so it is declared (§4.1 rule 4, §4.5 C1, §8.6 row 10,
   §8.7, AC 40) rather than described as frozen.
3. **D9 "test here, fix in a follow-up."** The escape clause fires: matrix cells 5 and 9 fail
   today, so the three bounded code fixes of C4 are in scope. No broader cwd refactor is undertaken.
   The consequence is declared rather than hidden: those two cells (plus cell 6, the nested form of
   cell 5) change observably **including on the no-flag path**, and are declared behavioural
   exception 2 (§4.1 rule 5, §4.5 C4, §14, AC 46). No "checkout from outside any repository" cell is
   claimed or fixed: `RequireWorkspace` resolves external mode or errors before checkout dispatch,
   so the containment probe's error arm is defensive depth, not shipped behaviour change (§10.9).
   Every other cwd cell stays frozen in stdout,
   stderr, exit code, files, and modes — and stays frozen in Git commands too, apart from the two
   **read-only** argv carve-outs the fixes necessarily imply (the checkout containment probe and the
   repo-scoped default-branch resolution). Those are declared as a closed carve-out, §4.1 rule 6,
   and asserted exactly by AC 2 / §17.1 comparison mode 3 rather than left as an unstated exception
   to a frozen-Git-command claim the implementation could not honour. The default-branch carve-out
   is deliberately **not** claimed to be argv-shaped: the two sides ask *different repositories* the
   same question, so their command count and exit-status classes may differ, and the comparator
   validates the **resolved default branch** and the closure of the event instead — an equal-count,
   equal-exit-class, "only a `-C` prefix" claim would be false and is forbidden.
4. **§4.0 guard file naming.** The analysis leaves the guard filename open; this specification fixes
   it as `.sync-run.lock` and requires the exact name in `isRuntimeState`, rather than a reserved
   prefix, to keep the import filter's exact-name style and avoid filtering user files by accident.
5. **Checkout plan `name:` on the frozen path (C5).** The analysis treats the checkout plan's
   logical-name field as new-mode scope. Refined to: it is written on **every** run, including
   no-flag ones, because C3's duplicate-`GitBranch()` attribution fix is otherwise inert exactly
   where the defect occurs. The declared cost is that a no-flag transaction is semantically
   equivalent but **not** byte-identical to previous releases in this one additive field (§4.1
   rule 3, §4.5).
6. **I7 and I18 are scoped, not global.** Refusing `--continue --abort` and refusing a symlinked
   legacy state file are both improvements, but applying them unconditionally would change frozen
   no-flag behaviour. I7 fires only for new-mode invocations and v2 state; I18 covers the new-mode
   state paths only, and an ordinary legacy `.sync-state.yaml` symlink with no payload is still
   followed. Both limitations are documented (§3.5, §12 item 7, §18 item 9) rather than silently
   fixed or silently dropped.
7. **One shared, read-only state classifier.** `internal.ClassifyExternalSyncState` is introduced so
   `tws sync` and `tws status` cannot drift, and `buildFeatureSync` calls it **before** the
   `os.Stat(statePath)` early return that would otherwise hide every payload-only, guard-only, and
   marker cell (§11.1). Status stays read-only.
8. **State goldens are semantic, and output goldens are normalized, not raw.** Timestamps, PIDs, and
   random tokens make raw-byte equality between two independently created state files unachievable.
   The specification therefore requires structural equality plus an exact, literal, closed set of
   normalized dynamic fields (§4.1 rule 2, §17.1) — plus exactly one conditional rule, for a
   checkout conflict transaction's `failure_msg`, whose suffix is Git's own output — and forbids
   production seams added solely for goldens. Output goldens are refined the same way, for the same
   reason: a raw capture embeds
   per-run temporary fixture paths and Git's version-dependent prose, so §4.1 rule 1 defines exactly
   two closed mechanisms — one enumerated path→token table and a test-only PATH wrapper that, **in
   external captures only**, moves three streamed mutating verbs' Git-owned output into a sidecar
   (with the declared consequence that `RunDirClean`'s stderr filter then sees an empty stream, so
   no golden pins its `    (skipped duplicate commit)` reformat; AC 2 covers that by a direct
   assertion outside the wrapper) — and, **in both capture modes**, tee-records the stdout of the
   three read-only shapes whose resolved values comparison mode 3 asserts, replaying those bytes
   verbatim so the run is unchanged —
   and compares everything that
   remains, all of it tws-owned, byte for byte. Checkout captures run the wrapper in record-only
   mode because checkout parses and persists those bytes, so diverting them would change behaviour;
   checkout needs no diversion anyway, since it never wires a Git child to the process streams. No
   other rewriting is permitted, and the wrapper is
   confined to the golden harness so real-Git integration coverage is unchanged.
9. **Marker generation and marker recognition live in different packages.** The analysis treats the
   marker as one helper. Refined to: recognition (`isSyncMarker`) stays unexported in package
   `internal`, where its **single** caller — `ClassifyExternalSyncState` — lives, and generation
   (`newSyncMarker`) plus its
   unexported test seam (`syncMarkerFn`) move to package `cli`, where `RunE` calls them (§8.2,
   §13.3). Every other consumer, `buildFeatureSync` included, reads the classifier's `Cell` and
   decoded fields instead of the predicate. This keeps package `cli` from having to reach an
   unexported `internal` symbol and avoids
   exporting a mutable seam purely for tests; the §8.2 grammar is the shared contract and is
   asserted from both sides (AC 29).
10. **Live-guard precedence covers the payload-only cell.** The analysis scopes live-guard
    precedence to the sentinel cells. Refined to: `{absent, valid}` (cell 2) is also guard-
    dependent, because an old `--abort` against a *still-running* new-mode run produces exactly
    that shape while the owner, its guard, its payload, and its real rebase are all alive. Plain,
    `--continue`, and `--abort` therefore all refuse under a live owning guard, and the §9.2
    recovery is reachable only when the guard is stale or absent (§8.6, §8.7, §9.2, §11.1).
11. **Trigger flags on `--continue` require v2 state (I20).** The analysis does not consider a
    `--continue` that carries scope or axis flags against pre-v2 state. Refined to: such an
    invocation is refused before Git with one exact string, in both modes, because the only two
    alternatives — silently ignoring the flags, or resuming broadly over the whole stack while the
    user asked for a scope — are both wrong and both silent. The refusal requires an explicit
    trigger flag, so trigger-free `--continue` behaviour against absent or legacy state stays
    frozen byte for byte (§3.5, §3.6 step 9, §8.6 rows 1 and 7, §10.5 rule 0).
12. **C2 and C3 are declared no-flag changes, not silent ones.** The analysis treats the
    `pushFeature` ref fix and the `finalizeTransaction` attribution fix as invisible on the frozen
    path. Refined to: each is observable on exactly one declared fixture shape — a decoupled
    `Name`/`Branch` for C2 (push argv, per-entry line, exit code, remote ref), and a duplicate
    `GitBranch()` for C3 (`stack.yaml` attribution) — and both are reachable from ordinary flag-free
    invocations. Calling them frozen would make §4.1 false in exactly the cases the fixes exist for,
    so they are declared as behavioural exception 3 (§4.1 rule 7, §4.5 C2/C3, §14, AC 33, AC 34),
    their pre-change behaviour is stored as declared-change evidence rather than as a golden, and
    the frozen claims of §4.1 are scoped to inputs outside those fixtures. Coupled-name and
    unique-branch fixtures — every AC 1 golden — stay frozen, argv and `stack.yaml` included.

No other recommendation is altered. Where the analysis marks something "unresolved", §20 settles it;
where it marks something "resolved", this document restates it in binding form.
