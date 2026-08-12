# Analysis

## Summary

Ancestry classification already exists, but it exists **three times, in two disagreeing dialects, in
one mode only**. Checkout doctor classifies edges in `buildOneFeatureEntry`
(`internal/checkout_health.go:588-670`), checkout list re-implements a shorter variant in
`BuildCheckoutList` (`internal/checkout_health.go:924-991`), and `tws status` re-implements base
resolution a third time in `buildEntry` (`internal/agent_status.go:1339-1368`). External `tws doctor`
has **no** ancestry at all: `CheckFeatureHealth` (`internal/health.go:105-131`) only checks
worktree-branch match, dirtiness, and inject symlinks. Neither classifier reads `LastBaseSHA`, which
is the only recorded fact that can distinguish "parent moved forward" from "parent history was
rewritten" — so the most common stacked-diff state is reported as `divergent`, the alarming label.

This feature is mostly a *correction plus extraction*, not net-new machinery: one read-only,
mode-independent evaluator over `(repoDir, feature, stack)`, consumed by checkout doctor, checkout
list, and external doctor, returning a projection rich enough for `stack-status` to render without
recomputing anything. It requires no fetch, no mutation, no new dependency, and no new status
vocabulary. It does require new *plumbing*: external doctor currently has no repository handle at
all, so the caller chain must carry one (see "Repository plumbing").

## Current behaviour, verified

Reproduced in a throwaway temp Git repo inside the project directory (deleted afterwards), replaying
the exact commands the code issues.

### Git facts this design depends on (each verified)

| Probe | Result |
| --- | --- |
| `git rev-parse <annotated-tag>` | tag-object SHA (`5361c26`), **not** the commit |
| `git rev-parse <annotated-tag>^{commit}` | commit SHA (`fb84483`) |
| `git merge-base --is-ancestor <tag-object-sha> <branch>` | exit 0 — Git peels tag objects itself |
| `git merge-base <tag-object-sha> <branch>` | prints the peeled commit |
| `git merge-base --is-ancestor A B`, unrelated histories | exit **1** (a normal "no") |
| `git merge-base A B`, unrelated histories | exit **1**, empty output — no merge base exists |
| `git rev-parse --verify --quiet <bogus 40-hex>` | exit **0** (accepted as an abbreviated-object candidate) |
| `git rev-parse --verify --quiet <bogus 40-hex>^{commit}` | exit **1** |
| `git merge-base --is-ancestor <unknown-sha> <ref>` | exit **128** (fatal), *not* exit 1 |

Two consequences fall straight out. Unrelated histories are **resolvable refs**, so they must never
be labelled `missing`; and a non-zero `--is-ancestor` exit is only meaningful when it is exactly 1,
which `gitIsAncestor` (`internal/checkout_sync.go:365-377`) already gets right by returning an error
for any other code.

### Misclassifications in the current code

| Scenario | Reality | Current label | Correct label |
| --- | --- | --- | --- |
| Parent advanced by new commits; child holds unique commits on the old base | plain `git rebase parent` is safe | **divergent** | stale |
| Parent tip amended/rebased after the child's last sync (`L` no longer in parent history) | plain rebase replays already-rewritten commits | divergent | divergent (right answer, accidental reasoning) |
| Parent advanced; child holds *no* unique commits | fast-forward | stale | stale |
| Parent branch reset/force-moved **backwards inside the child's history** | nothing to do | current | current (already correct; only incidentally, via `merge-base(C,P) == P`) |
| Parent force-moved to a sibling/unrelated commit not in the child's history | `--onto` needed | divergent | divergent |
| Base is an annotated tag whose commit is the child's ancestor | up to date | **stale/divergent forever** | current |
| Base ref and a tag share a name | tws branch head | **tag's head** | branch head |
| Child and base share no history at all | rebase would replay everything | **divergent by luck** | divergent (`unrelated-histories`) |

Details:

1. **Stale/divergent collapse.** `buildOneFeatureEntry` decides with
   `gitIsAncestor(repo, gitBranch, baseGitBranch)` — "is the *child* an ancestor of the parent?"
   (`internal/checkout_health.go:661`). That is only true when the child has zero unique commits, so
   any child with real work plus any parent movement lands in the `divergent` branch at line 665.
   Verified: parent fast-forwarded → `merge-base --is-ancestor child parent` exits 1 → `divergent`,
   even though `LastBaseSHA` was still an ancestor of the parent head. The existing test
   `TestCheckoutHealth_StaleChild` (`internal/checkout_health_test.go:189-229`) only covers the
   zero-unique-commit case, which is why this never surfaced. No test asserts `divergent` at all.
2. **Parent-contained test is indirect.** The `current` arm compares
   `mb == gitFullSHA(repo, baseGitBranch)` (`internal/checkout_health.go:658-660`), which is the
   right idea (`merge-base(C,P) == P` ⇔ `P` is an ancestor of `C`) but is implemented on unpeeled
   SHAs, and the fallback arm then asks the reverse question. For plain branch heads this arm is
   already correct, including when the parent has been reset *backwards* into the child's own
   history — that case is **not** a misclassification and is only pinned by a new test. The direct
   predicate `merge-base --is-ancestor P C` is cheaper, peels tags (which the unpeeled equality does
   not), and states the intended rule once.
3. **Annotated tags can never be current.** `git rev-parse v1` on an annotated tag returns the tag
   object (`5361c26`) while `git merge-base` returns the commit (`fb84483`) — verified — so the
   equality at line 659 never holds. Nothing peels with `^{commit}`, unlike `internal.VerifyGitRef`
   (`internal/exec.go:91-97`), which already does it correctly.
4. **`gitRefExists` accepts non-existent SHAs.** `git rev-parse --verify --quiet <40-hex>` exits 0
   for `0000…0` and `deadbeef…` — verified. `gitRefExists` (`internal/checkout_health.go:672-674`)
   therefore reports `true` for a literal-SHA base absent from the repository; the edge only degrades
   to `missing` by accident, when `git merge-base` later fails.
5. **Ambiguous refs resolve to the tag.** With a branch and a tag both named `dup`, bare
   `rev-parse dup` returns the tag. Branch identity must be read through `refs/heads/<GitBranch()>`.
6. **Doctor and list disagree by construction.** `BuildCheckoutList` has no `divergent` arm at all
   (`internal/checkout_health.go:976-980`): everything non-current is `stale`. Same repo, same
   moment, two different answers from `tws doctor` and `tws list`.
7. **Archived entries over-warn.** `buildOneFeatureEntry` returns `missing` + `SeverityWarning` for a
   missing ref regardless of `se.Archived` (`internal/checkout_health.go:629-640`), while `tws status`
   already distinguishes `IssueRefMissingArchived` at `SeverityInfo`
   (`internal/agent_status.go:1391-1394`).
8. **External mode is blind, and has no repo handle.** The external `tws doctor` path
   (`internal/cli/doctor.go:39-42,45-67,73-105`) reports only per-worktree issues; a materialized worktree
   sitting on a rewritten parent looks healthy. `checkFeatureE` receives a feature *name*, derives
   only `featurePath`, and never resolves a `Workspace` — there is no repository directory in scope.
   Adjacent (out of scope, worth a backlog note): `internal/cli/list.go:63,66,81` still passes
   `entry.Name` where `entry.GitBranch()` is required — a `branch-name-decoupling` leftover.
9. **Base identity is genuinely ambiguous today.** `tws new` stores `baseName` (`main`) in
   `stack.yaml` while creating the branch from `baseRef` (`origin/main`)
   (`internal/cli/new.go:186,135,224`, resolver at `internal/cli/new.go:307-325`). External sync then
   rebases onto `origin/<default>` via `resolveEntryBase`/`resolveBase`
   (`internal/cli/sync_helpers.go:179-192`) and records **that** SHA into `LastBaseSHA`
   (`internal/cli/sync_helpers.go:48,66-72`), whereas checkout sync resolves the base literally
   (`internal/checkout_sync.go:456-459`) and records the literal SHA
   (`internal/checkout_sync.go:936-953`). Doctor and list compare against the literal `se.Base`.
10. **`LastBaseSHA` is often absent, and is stored unpeeled.** It is written only by the two sync
    paths, through `GetBranchSHA` (`internal/stack.go:85-95`) and `gitResolveRef`
    (`internal/checkout_sync.go:307-314`), both bare `rev-parse` — so when the base is an annotated
    tag, `L` is a **tag-object** SHA. `tws new` never records it at all, so a never-synced branch has
    no base record.

## Correct semantic model

Per edge: `C` = child head, `P` = parent head, `L` = `LastBaseSHA`, `M` = `merge-base(C, P)`.

### Resolution (before any classification)

1. `se.Repo != ""` → **cross-repo-unsupported**, informational, **no Git process is started**.
2. `C := rev-parse --verify --quiet refs/heads/<se.GitBranch()>^{commit}`. Failure → `missing`,
   reason `child-ref-missing`.
3. `P := rev-parse --verify --quiet <base ref>^{commit}`, where the base ref is
   `refs/heads/<parent.GitBranch()>` when `se.Base` names a stack entry, else the literal `se.Base`.
   Failure → `missing`, reason `base-ref-missing`.
4. `L`: empty → `base_record: absent`. Otherwise
   `rev-parse --verify --quiet <L>^{commit}` → `base_record: present` (peeled `L`) or
   `base_record: unresolvable`. Peeling normalises the tag-object case in item 10 above, and matches
   what `merge-base` would have done anyway (verified: `--is-ancestor <tag-object> <commit>` = exit 0).

`missing` is therefore reserved for exactly one thing: **a ref that does not resolve to a commit**.
It is never inferred from a failed `merge-base`.

### Classification (first match wins)

1. **current** — `merge-base --is-ancestor P C` exits 0. Includes `C == P` and, importantly, the case
   where the parent branch was **reset or force-moved backwards to a commit still inside `C`'s
   history**: there is nothing to replay, so the edge is current no matter what `L` says. Only a
   parent that moved to a commit *outside* `C`'s history can be non-current.
2. **divergent / `unrelated-histories`** — `C` and `P` share no history (`merge-base C P` exits 1 with
   empty output; both `--is-ancestor` directions exit 1). Both refs exist, so this is not `missing`.
   `M` is reported as null.
3. **divergent / `base-rewritten`** — `base_record: present` and `merge-base --is-ancestor L P` exits
   1: the commit the child was last replayed onto has left the parent's history (amend, rebase,
   force-move to a sibling, reset outside the child's history). Plain `git rebase <parent>` would
   replay commits the parent already rewrote; `git rebase --onto <parent> <L>` is **required**.
4. **stale / `parent-advanced`** — otherwise. The parent moved forward (or `L` is still in its
   history), so `git rebase <parent>` replays exactly the child's unique commits. This is the common,
   unalarming case.

A non-0/1 exit from any `--is-ancestor` (e.g. exit 128 after `L` was garbage-collected between
resolution and probe) is an **error**, never a "no": it degrades `base_record` to `unresolvable` with
reason `base-record-unresolvable`.

`M` is a *reported*, nullable field only — a debugging/reporting aid and a `stack-status` input. No
classification arm reads it. `base_record: unresolvable` warns and says the recorded base is gone,
because `rebase --onto <L>` would also fail.

### Relationship to what sync actually does (`--onto`)

This must be stated precisely, because the previous draft overstated it. Both sync paths select
`--onto` on **SHA inequality with the new base**, not on ancestry:

- external: `entry.LastBaseSHA != "" && currentBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA`
  (`internal/cli/sync_helpers.go:48-52`);
- checkout: `entry.LastBaseSHA != "" && entry.LastBaseSHA != entry.NewBaseSHA`
  (`internal/checkout_sync.go:891-896`).

So a **pure parent fast-forward with a recorded base still triggers `--onto`**, while the evaluator
correctly reports that edge as `stale`. `divergent` is the strict **subset** of the sync trigger in
which `L` is not contained in the current parent history — the subset where `--onto` is not merely
*chosen* but *required* for correctness, because plain rebase would duplicate rewritten commits.

Guidance wording must follow that split exactly:

- `stale` → "parent advanced; run `tws sync <feature>` — plain `git rebase <parent>` replays only your
  commits (sync may still use `--onto`, which is equivalent here)."
- `divergent` → "the parent's recorded base commit `<L>` is no longer in `<parent>`'s history; an
  equivalent manual repair is `git rebase --onto <parent> <L> <child>`. `tws sync` also uses an
  `--onto` rebase, with mode-specific flags."

Doctor must never claim "sync will use a plain rebase" for `stale`.

### Severity

| State | Active entry | Archived entry |
| --- | --- | --- |
| `current` | ok | ok |
| `stale` | warning | **info** |
| `divergent` | warning | **info** |
| `missing` (child or base ref) | warning | **info** |
| `cross-repo-unsupported` | info | info |
| `evaluation-unavailable` | info | info |

Ancestry is **always computed and always reported** when the refs resolve, archived or not — the data
is identical, only the severity differs. Archived entries never raise the checkout issue count
(`countIssues`, `internal/checkout_health.go:755-782`), never affect `HasErrors`
(`internal/checkout_health.go:188-208`), and never contribute to the external doctor's issue total.
Active, non-materialized entries keep today's warning semantics (`missing` ref → warning). This
aligns checkout doctor with `tws status`'s existing `IssueRefMissingArchived`/`IssueRefMissing` split
(`internal/agent_status.go:1384-1398`) and **resolves D7** (below).

The five status values stay exactly as they are (`internal/checkout_health.go:144-152`); everything
new is an additive field.

## Repository plumbing

The evaluator is mode-independent because it takes a repository directory, not a `Workspace`. That
only works if callers can supply a *validated* one, which external doctor cannot today.

**Evaluator contract.** `EvaluateStackAncestry(repoDir string, feature string, stack Stack)
([]StackEdge, error)` **refuses** an empty or unvalidated `repoDir` and returns
`ErrRepoUnavailable` **before issuing any Git process**. There is no `git -C ""` anywhere: an empty
`-C` silently means "current working directory", which would make results depend on where the user
stood. Validation is `repoDir != ""` plus a successful `MainRepoRootIn(repoDir)`
(`internal/exec.go:27-42`), performed once per evaluation and cached.

**Caller refactor.**

- `doctorCmd` (`internal/cli/doctor.go:30-37`) already calls `internal.RequireWorkspace()` once. Keep
  that single call and pass the resolved `internal.Workspace` down. Because `checkFeatureE` no longer
  routes through `RequireFeaturePath` → `RequireWorkspace`, `doctorCmd` must re-assert the
  fail-closed rule itself: `wsErr` is returned unchanged when `internal.MainRepoRoot()` succeeds, and
  tolerated (zero `Workspace`) only when the cwd is in no Git repository at all.
- Checkout mode: `runCheckoutDoctor` / `buildFeatureEntries` / `BuildCheckoutList` pass
  `ws.RepoRoot`, which is already canonical and repo-derived.
- External mode: `checkFeatureE(feature string) (int, error)` becomes
  `checkFeatureE(ws internal.Workspace, feature string) (int, error)`, and
  `internal.CheckFeatureHealth(featurePath)` gains an ancestry-aware sibling that takes
  `(repoDir, featurePath string, stack Stack)` — `CheckFeatureHealth` itself keeps its signature and
  behaviour so other callers stay untouched. Both call sites in `doctorCmd`
  (`internal/cli/doctor.go:40,56`) are updated.
- External repository resolution, first match that validates:
  1. `se.Repo != ""` → cross-repo, no probe, no repo needed.
  2. Feature-scoped evidence: `MainRepoRootIn(<featurePath>/worktrees/<name>)` for any materialized
     worktree of this feature. This is the same evidence `inferExternalRepoRoot` already trusts
     (`internal/workspace.go:359-378`) and is the only source that is correct when `TWS_ROOT` points
     at a metadata root belonging to a different repository than the current directory.
  3. `ws.RepoRoot` from the caller's `Workspace`, when non-empty and validated.
  4. `inferExternalRepoRoot(metadataRoot, cfg)` (`internal/workspace.go:339-392`), reached from the
     `.tws-workspace` marker walk in `DetectWorkspaceRoot` (`internal/paths.go:13-24`).
  5. Otherwise → an explicit **`evaluation-unavailable`** result: informational, carrying the reason
     (`no-source-repository`, or the ambiguity error text from `inferExternalRepoRoot`), producing
     **no ancestry issue** and **no issue-count change**. The regular non-Git health checks
     (`CheckWorktreeBranch`, `CheckWorktreeDirty`, `CheckWorktreeInjectLinks`,
     `internal/health.go:26-102`) run exactly as before.
- The chosen source is recorded in an additive `repo_source` field
  (`workspace` | `worktree` | `inferred` | `unavailable`) so the output is auditable and so a future
  bug report can distinguish "wrong repo" from "wrong ancestry".

## Rendering and guidance

- The existing per-entry line
  `base=%s ancestry=%s%s [head=…] [parent=…]` (`internal/checkout_health.go:861-872`) keeps its shape
  **only when no guidance is emitted**. Guidance is *not* appended to it. When present it is rendered
  as an **additive indented detail line** underneath, exactly like the existing sync/session guidance
  lines (`internal/checkout_health.go:819-821,840-842`). The compatibility claim is therefore
  "existing tokens on the existing line are unchanged and no token is removed", not "output is
  byte-identical" — the entry block gains a line whenever guidance or a reason is present.
- Additive fields on `CheckoutFeatureEntry` (`internal/checkout_health.go:154-168`): `base_kind`,
  `base_ref`, `last_base_sha`, `base_record`, `merge_base` (nullable), `reason`, `guidance`,
  `repo_source`. Existing JSON keys and types are untouched.
- External mode has no severity channel today: `HealthIssue` (`internal/health.go:11-23`) is
  Branch/Problem/Hint and always renders `[!]`, and `checkFeatureE` prints
  `"%s: %d issue(s)"` from `len(issues)` (`internal/cli/doctor.go:84,96-104`). Adding informational
  ancestry through `HealthIssue` as-is would both mis-render and inflate the count. Resolution: add
  an **additive `Severity CheckoutSeverity` field** to `HealthIssue`, defaulting to the zero value
  that renders and counts exactly as today, render `SeverityInfo` entries with `[i]` on their own
  detail lines, and count only warning/error entries into the returned total. Archived,
  cross-repo, and evaluation-unavailable ancestry results are therefore visible but never change
  the `healthy (N active worktree(s))` / `N issue(s)` line.

## Base-record ref mismatch (both modes)

The recorded `L` and the ref the evaluator compares against can be **different refs**, in both modes,
for different reasons:

- **External:** for a root entry whose base is the default branch, sync rebases onto and records
  `origin/<default>` (`internal/cli/sync_helpers.go:48,66-72,186-192`), while doctor compares against
  the literal `main`. A local `main` lagging behind `origin/main` makes `L` look "ahead of" the
  compared parent.
- **Checkout:** `BuildCheckoutPlan` resolves `base := entry.Base` **literally**
  (`internal/checkout_sync.go:452-462`) and rebases with `gitPlainRebase(opts.RepoDir, entry.Base)`
  (`internal/checkout_sync.go:896`) — it never maps a parent stack entry to `parent.GitBranch()` the
  way doctor, list, and `tws status` do. With `branch_prefix` set or a parent renamed under
  `branch-name-decoupling`, checkout sync resolves the *logical* name as a ref: it either errors out
  of plan construction or targets the wrong ref, and any `LastBaseSHA` it records belongs to that
  other ref. **This is a pre-existing checkout-sync planning gap, not something this feature fixes**;
  the evaluator must not copy it, and must not pretend the disagreement does not exist.

Mitigation inside this feature (read-only): report `base_ref` (the ref actually probed) and
`base_kind`, and emit an informational note when a plausible alternative resolution exists and
differs — `origin/<base>` resolving to a different commit than the literal base, or the literal
`se.Base` resolving to something other than the parent entry's `GitBranch()`. Doctor then never
silently contradicts what sync will do; it shows both. The actual fix belongs to a checkout-sync
feature and is recorded as a backlog note.

## Ref existence consistency

`CheckoutFeatureEntry.RefExists` (`internal/checkout_health.go:599`) and the `[ref-missing]` tag
(`internal/checkout_health.go:858-860`) are currently derived from `gitRefExists`, which is unpeeled
and ambiguous. Decision: **populate `RefExists` from the evaluator's peeled child resolution**
(`refs/heads/<GitBranch()>^{commit}`), so a single probe backs both the flag and the classification
and they can never disagree. This is byte-identical for every realistic branch name; it changes the
answer only for (a) a branch/tag name collision, where the branch now wins — the intended fix — and
(b) a 40-hex branch name, which tws never creates.

`gitRefExists` itself is **left untouched**, because `tws status` shares it
(`internal/agent_status.go:1384`); that keeps `agent-work-status-dashboard` byte-identical and its
dependency edge soft. The evaluator adds its own peel-correct resolver. If a later revision decides
to change `gitRefExists`, the edge must be upgraded to hard.

## Proposed extraction

New `internal/stack_ancestry.go`:

- `type StackEdge struct` — feature, name, `git_branch`, archived, `repo`, `base_name`,
  `base_kind` (`stack-entry` | `literal-ref`), `base_ref` (the ref actually probed), `local_head`,
  `parent_head`, `merge_base` (nullable), `last_base_sha`, `base_record`
  (`absent` | `present` | `unresolvable`), `status`, `reason`, `severity`, `guidance`, `repo_source`.
- `func EvaluateStackAncestry(repoDir, feature string, stack Stack) ([]StackEdge, error)` plus a
  single-edge form. **Mode-independent by construction**: it takes a validated repo directory and a
  `Stack`, never a `Workspace`, never a worktree path, never a mode flag; it refuses an unvalidated
  directory without touching Git.
- Base resolution stays the existing rule — `se.Base` matching a `StackEntry.Name` resolves to that
  entry's `GitBranch()`, otherwise the string is a literal ref — but it lives in one place instead of
  three, and `base_kind` records which arm fired.
- One ref→peeled-SHA cache per evaluation, replacing today's repeated `rev-parse` of the same base
  (`gitShortSHA`/`gitFullSHA` are called on the base in both arms at
  `internal/checkout_health.go:643-658`).

Callers map mode to `repoDir` only:

| Caller | `repoDir` | Change |
| --- | --- | --- |
| `buildOneFeatureEntry` (checkout doctor) | `ws.RepoRoot` | replace `internal/checkout_health.go:622-669` with evaluator output |
| `BuildCheckoutList` (checkout list) | `ws.RepoRoot` | replace `internal/checkout_health.go:959-981`; the doctor/list disagreement disappears |
| `checkFeatureE` (external doctor) | resolved per "Repository plumbing"; `evaluation-unavailable` when none | new signature carrying `Workspace`, new ancestry section |
| `stack-status` (child feature) | same | consumes `StackEdge` unchanged |

External ancestry is deliberately **ref-based, not worktree-based**: branches are shared across
linked worktrees, so an archived or non-materialized entry still has an evaluable edge.
Materialization stays a separate axis and is already modelled by `tws status`.

## Why raw SHA equality now, and why tpatch patch identity stays deferred

`LastBaseSHA` is a raw commit SHA because a SHA is the only identity Git itself guarantees, and
because the repair command needs an exact commit: `git rebase --onto <newbase> <lastbase>` takes a
commit, not a concept. Ancestry over recorded SHAs is deterministic, offline, O(1) per edge, and
needs no extra tooling — it answers precisely "is the base I last replayed onto still in the parent's
history?", which is exactly the question that separates "plain rebase is correct" from "`--onto` is
required".

What it cannot answer is *change equivalence*: after an amend the old parent commit is gone even
though the change is logically the same, and after every rebase the child's own commits get new
SHAs, so SHA identity cannot distinguish "already applied upstream" from "still unique to me".
`git patch-id` is content-derived and breaks on renames and context drift, so it would trade this
feature's honest uncertainty for silent false negatives. Change equivalence is owned by tesserapatch
per the roadmap's explicit non-goal ("Reimplementing tpatch patch identity, reconciliation, or patch
theory inside tws", `docs/roadmap.md:127`, with the read-only contract framing at
`docs/roadmap.md:81`) and by the requested `tpatch-patch-identity-research`, which hard-depends on
`stack-status` and is out of scope here. Consequence for wording: `divergent` must be phrased as
"the parent's recorded base commit is no longer in its history", never as "your change diverged".

## Compatibility

- Status vocabulary and existing JSON keys are unchanged; all new data is additive. The
  `base=%s ancestry=%s` line keeps every existing token and gains no inline suffix, but an entry may
  now be followed by an indented reason/guidance line. Doctor gains no `--json` here — that surface
  belongs to `stack-status`.
- Exit semantics unchanged: ancestry is warning-level at most, `runCheckoutDoctor`
  (`internal/cli/doctor.go:107-120`) still errors only on `HasErrors()` (corrupt/unreadable state),
  external doctor still returns nil with a count.
- Deliberate, user-visible reclassifications: "parent advanced, child has work" now prints `stale`
  instead of `divergent`; true rewrites now print `divergent` in `tws list` where it previously
  printed `stale`; an annotated-tag base now reaches `current`. A parent reset backwards inside the
  child's history is **not** in this list: checkout doctor and list already answered `current`
  there, and the new rule only pins that answer explicitly. No existing test asserts any of these
  transitions. `TestCheckoutHealth_StaleChild` (`internal/checkout_health_test.go:189-229`) creates a
  child with **zero** unique commits, so it keeps its current `stale` + warning answer under the new
  rules; `TestCheckoutList_*` keep theirs.
- Archived entries change severity from warning to info for `missing`/`stale`/`divergent`, which
  lowers the checkout issue count for workspaces containing archived entries. No current test asserts
  that count for an archived entry (`TestCheckoutList_ArchivedEntry`,
  `internal/checkout_health_test.go:996-1016`, asserts only the `[archived]` tag).
- External doctor output grows an ancestry section — an intentional scope change stated in the
  request, bounded so that a healthy feature still prints zero issues
  (`TestExternalDoctor_UnchangedBehavior`, `internal/cli/checkout_doctor_test.go:116-132`) and so
  that a feature whose repository cannot be resolved keeps its previous issue total
  (`TestExternalFeatureDir_DoctorRegression`, `internal/cli/checkout_doctor_test.go:156-189`).
- Embedded skills state the checkout ancestry set as `current/stale/missing/cross-repo`
  (`assets/skills/claude/tesseraworkspaces/SKILL.md:251`) and the list set as `stale/missing`
  (line 262); both must be updated for `divergent`, the new detail fields, and external-mode ancestry.

## Risks

- **Semantic churn**: any operator habit built on today's `divergent` flips meaning. Mitigated by
  guidance text that names the rebase strategy.
- **Base-ref ambiguity and base-record mismatch (D1, and the checkout planning gap above)**: choosing
  literal vs `origin/<base>` changes answers for external root entries, and checkout sync's literal
  base resolution can already disagree with `GitBranch()`-based parent resolution. Doctor must show
  both rather than silently pick a side.
- **Wrong repository in external mode**: with `TWS_ROOT` pointed at another repository's metadata,
  a naive `ws.RepoRoot` would report every edge as `missing`. Mitigated by the resolution order in
  "Repository plumbing" (worktree evidence before workspace) and by `repo_source`.
- **Performance**: today ~5-7 `git` processes per entry in doctor plus ~4 more in list; adding the
  `L` probe makes it worse without the per-evaluation cache. Keep it O(edges) with at most three
  ancestry/merge-base probes per edge (`P⊆C`, then `L⊆P` or `merge-base`), plus cached resolutions.
- **Read-only discipline**: no fetch, no `--fork-point` (reflog-dependent and non-deterministic), no
  `--is-ancestor` result inferred from a non-1 exit code, no `git -C ""`.
- **Privacy/safety**: only SHAs, ref names, and already-recorded `Repo` paths are printed; no reflog,
  no remote credentials, no probing of foreign repositories for cross-repo entries.
- **Transient state**: ancestry computed mid-rebase or on a dirty tree is still valid for refs but
  may look surprising; checkout reports it beside the existing `ActiveGitOp`/dirty header, external
  has no such header and needs an explicit note. Dirty/rebase detection itself stays out of scope
  beyond the checks regular doctor already runs.

## Tests (real temporary Git repos and remotes, no mocks)

Reuse `setupHealthTestRepo`/`gitInTest` (`internal/checkout_health_test.go:40-129`) and the external
`setupGitRepo`/`createWorktree`/`withWorkspaceEnv` helpers
(`internal/cli/new_integration_test.go:152-160`).

**Classification.**
- parent contained in child → `current`;
- child identical to parent → `current`;
- **parent reset/force-moved backwards to a commit still inside the child's history → `current`**
  (own test; asserts `L` being ahead of `P` does not force `divergent`. This is a pin, not a
  reclassification: the baseline already answered `current` here);
- **parent force-moved sideways to a commit outside the child's history → `divergent`/`base-rewritten`**
  (own test, separate from the backwards-move test);
- parent advanced, child has unique commits, `L` = old parent → **`stale` (regression for the
  headline misclassification)**;
- parent advanced, child has no unique commits → `stale`;
- parent tip amended after sync → `divergent` + `--onto` guidance naming `L`;
- parent rebased → `divergent`;
- `L` empty → `stale` with `base_record: absent`, never `divergent`;
- `L` recorded but pruned from the repo → warning with `base_record: unresolvable`, not `divergent`
  (asserts the exit-128 path is not read as "not an ancestor");
- **`L` present, pure fast-forward → `stale`, asserted alongside the fact that sync would still pick
  `--onto`** (documents the deliberate non-equivalence of the two triggers).

**Unrelated histories.** Child created with `git checkout --orphan`: both refs resolve, `merge-base`
exits 1, `--is-ancestor` exits 1 in both directions → `divergent` with reason `unrelated-histories`,
`merge_base` reported as null, and **not** `missing`.

**Ref handling.** annotated-tag base at the child's ancestor → `current` (peel regression);
annotated-tag `LastBaseSHA` (tag-object SHA recorded by the unpeeled writers) → classified the same
as its peeled commit; literal commit-SHA base → correct classification; non-existent 40-hex base →
`missing` (peel regression); deleted base branch → `missing`; missing child ref on an active entry →
warning; renamed Git branch on child and on parent → both sides use `GitBranch()`; branch/tag name
collision → `refs/heads/` wins and `RefExists` agrees with the classification; cross-repo entry →
`cross-repo-unsupported` with **zero** Git processes issued against the foreign path.

**Archived severity.** archived entry with a missing ref → `missing` + info, issue count unchanged;
archived entry that is `stale`/`divergent` → status still computed and printed, severity info, issue
count unchanged, `HasErrors()` false; the same fixtures with `Archived: false` → warning and counted.
Assert both the checkout `Issues` total and the external returned total.

**Repository plumbing.** `EvaluateStackAncestry("", …)` returns `ErrRepoUnavailable` and spawns no
process (assert via a PATH-shim `git` that fails the test if invoked, or by pointing at a directory
that is not a repo and asserting no `.git` access); external doctor with a resolvable repo →
ancestry reported; external doctor whose source repo cannot be determined → `evaluation-unavailable`,
issue total identical to today's, and the worktree-branch/dirty/inject checks still run and still
report; `TWS_ROOT` pointed at a metadata root for a different repository → worktree-derived
`repo_source: worktree` yields correct ancestry.

**Cross-cutting.** doctor and list agree on identical fixtures; two consecutive runs are
byte-identical; read-only assertions extending `TestCheckoutHealth_ReadOnly`
(`internal/checkout_health_test.go:885`) to remote-tracking refs and `FETCH_HEAD` against a real
local bare remote whose branch was moved behind tws's back; exit status stays 0 for
`stale`/`divergent` in both modes.

**Invocation matrix.** checkout repo root; external repo root; inside a linked worktree; external
workspace root; external feature directory (`TestExternalFeatureDir_DoctorRegression`); with and
without a feature filter; detached HEAD; dirty tree; rebase in progress.

Gates per `docs/engineering-workflow.md`: `gofmt`, `go test ./... -count=1`, `go vet ./...`,
`golangci-lint run ./...`, `make build`, `git diff --check`, `tpatch feature deps --validate-all`.

## Dependencies

Registered edges are **not sufficient**. The current hard set covers stack data
(`keep-track-of-stacked-diffs-and-dependencies`), checkout doctor/list
(`checkout-doctor-observability`, which transitively pulls `checkout-workspace-lifecycle`,
`checkout-agent-sessions`, `fix-checkout-feature-path-routing`, `fix-external-feature-dir-resolution`,
`workspace-mode-foundation`), checkout sync (`checkout-stack-safety`), `GitBranch()`
(`branch-name-decoupling`), `LastBaseSHA`/`--onto` (`amend-aware-rebase` → `sync-continue`), and
external health (`worktree-health-check`). Missing:

- **`multi-repo-workspaces` → hard.** `StackEntry.Repo` and `sameStackRepo` define the cross-repo arm
  the evaluator must honour; nothing in the current closure provides it.
- **`workspace-sibling-links` → hard.** Both checkout consumers sit inside `ListFeaturesResolved` /
  `SpaceDirOwners` fail-closed listing (`internal/checkout_health.go:551-586,924-950`) and the
  external path resolves through `GuardFeatureName` (`internal/resolve.go:294-303`). It is currently
  reachable only through the *soft* `agent-work-status-dashboard` edge, so it is outside the hard
  closure.
- **`fix-default-base-branch` → hard.** It introduced `DefaultBranchIn` / `origin/HEAD` base
  resolution, which is the source of the base-identity ambiguity the spec must settle (D1).
- **`divergent-stack-sync` → soft.** It owns the DAG `TopoSort`/`Descendants` tests the evaluator's
  multi-child fixtures overlap with.
- **`fix-sync-branch-identity`, `fix-sync-continue-descendants`, `post-rebase-validation` → soft.**
  Future consumers of the evaluator (`branchContainsConfiguredParent`, `staleStackEdges`) that this
  feature deliberately does not rewrite.
- Keep `agent-work-status-dashboard` **soft** only because the implementation adds a new peel-correct
  resolver instead of changing `gitRefExists`/`healthCurrentBranch`; if those shared helpers are
  modified, upgrade it to hard.

`tpatch feature deps --validate-all` currently reports `DAG: ok (0 violations)`; the additions above
introduce no cycle (all are already-applied ancestors).

## Decisions resolved in this analysis

- **D7 — do external ancestry findings count into `checkFeatureE`'s total?** **Resolved.** Only
  warning-severity findings on **active** entries count, exactly like today's worktree checks.
  Archived entries, `cross-repo-unsupported`, and `evaluation-unavailable` are informational: they
  print, they never change the `healthy (N active worktree(s))` / `N issue(s)` line, and they never
  change the exit status. This requires the additive `Severity` field on `HealthIssue` described in
  "Rendering and guidance".
- **Repository resolution order (external).** Resolved as specified in "Repository plumbing":
  cross-repo short-circuit → materialized-worktree common dir → validated `ws.RepoRoot` →
  `inferExternalRepoRoot` → `evaluation-unavailable`. Recorded in `repo_source`.
- **`missing` vs `divergent` for unrelated histories.** Resolved: refs that resolve are never
  `missing`; unrelated histories are `divergent` with reason `unrelated-histories`.
- **`merge-base` role.** Resolved: reported nullable field and reason aid only; never a classifier.

## Unresolved decisions for the spec

1. **D1 — configured base identity.** Compare against the literal recorded `se.Base`, or against
   `origin/<base>` the way external sync does? Recommendation: probe the literal ref (deterministic,
   no network semantics) *and* surface the informational mismatch note described in "Base-record ref
   mismatch", so doctor never silently disagrees with what sync will do.
2. **D2 — absent base record.** Confirm `stale` + `base_record: absent` rather than a sixth status.
3. **D3 — cross-repo.** Keep every `Repo != ""` entry unsupported, or evaluate when parent and child
   share the same `Repo` and it resolves? Recommendation: stay unsupported in this feature and record
   the same-repo case as a follow-up.
4. **D4** — confirm `divergent` is warning (info when archived), never error, in both modes.
5. **D5** — does the evaluator compute ahead/behind counts, or only heads/merge base, leaving counts
   to `stack-status`? Recommendation: SHAs only here, opt-in counts later.
6. **D6** — no `--json` on `doctor`/`list` in this feature; `stack-status` owns machine output.
7. **D8** — should `tws new` persist `LastBaseSHA` at creation so new branches start with a base
   record? That is a mutation and belongs to a separate feature.
8. **D9** — the checkout-sync literal-base planning gap (`BuildCheckoutPlan` never resolving a parent
   entry's `GitBranch()`) is reported but not fixed here; confirm it is filed as its own feature
   rather than folded in.

## Viability

Viable and well-scoped. It removes duplicated semantics rather than adding a fourth copy, corrects
five demonstrable misclassifications, keeps the enum, exit codes, and external contract stable, adds
the repository plumbing external mode has always lacked in an explicitly fail-soft way, and produces
exactly the projection `stack-status` needs — with the change-equivalence question left where the
roadmap already put it.
