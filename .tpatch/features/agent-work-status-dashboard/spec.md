# Specification — agent-work-status-dashboard

## 1. Problem statement

`tws` already owns every fact needed to answer "which branch needs me right now?", but the facts are
scattered across four text-only surfaces (`tws list`, `tws doctor`, `tws stack`, and the `[tmux]` /
`[session]` tags) and one of them does not exist at all: external `openDirect`
(`internal/cli/open.go:239-279`) — the **default** external open path — writes no durable state, so
the most common way a tws-launched agent runs is invisible.

This feature adds one new command, `tws status [feature] [--json]`, a read-only projection over
tws-owned topology and runtime state, plus one narrow additive writer: hidden per-invocation direct
session records for external `openDirect`, without which `tws` cannot be authoritative even for the
sessions it launches itself.

The request's headline capability — `blocked (needs approval/input)` — is **not derivable at
baseline** and this spec does not fake it. Nothing in tws observes agent stdin, tool-permission
prompts, or turn boundaries. The semantic axis therefore ships as a stable enum whose only baseline
value is `unknown`; the structural axis (`needs_attention`) ships fully populated and authoritative.

## 2. Goals

- **G1.** One new command `tws status [feature]` with `--json`, working from all five supported cwd
  locations in both workspace modes.
- **G2.** A versioned, stable-key-set JSON envelope with two permanently separate axes:
  `runtime_presence = present|absent|stale|unknown` and
  `agent_state = working|ready|blocked|done|unknown`.
- **G3.** A three-level attention model (`entry` / `feature` / `workspace`) with rollup precedence
  `needs_attention > active > idle`, where `needs_attention` is derived only from structural signals
  `tws` owns end to end, **inherits upward** (a bad entry makes its feature and the workspace
  `needs_attention`, so one polled field can never miss a branch) and **never smears downward** (a
  feature- or workspace-level fault never marks an innocent branch). Per-level `issue_count`/`codes`
  stay own-scope only.
- **G4.** Per-invocation external direct session records so a tws-launched direct agent is
  observable, crash-detectable, and concurrency-safe.
- **G5.** Never leak a secret: no checkout `lock_token`, no lock-owner `token`, no transcript, no
  prompt, no agent argv.
- **G6.** Remain useful under stale/corrupt *operational* state (exit 0, reported), while refusing to
  silently hide unreadable *topology*.
- **G7.** Full backward compatibility when no records exist; no Git/worktree layout change and no
  change to agent behaviour beyond an error-returning refactor.
- **G8.** Make the later `tss` provider purely additive: it populates `agent_state` and adds session
  observations without changing `schema_version` semantics or any existing key.

## 3. Non-goals

- **N1.** Generic "blocked / needs approval" detection. No TTY polling, no `tmux capture-pane`
  scraping, no idle-CPU heuristics, no agent-UI coupling. `agent_state` is `unknown` at baseline.
- **N2.** Externally launched runtimes (an agent a human started by hand in a worktree). tws did not
  launch it and holds no record; it is `absent` on the tws-owned axis, never inferred.
- **N3.** Any `tss` invocation, shell-out, provider interface, provider config, or provider Go type.
  Not even a stub. That is the later child feature `tss-agent-state-provider`.
- **N4.** Any `tpatch` integration or feature-state projection.
- **N5.** Killing, signalling, or otherwise terminating a live direct session from any command.
  `tws close --force` is not added and not reserved as a flag.
- **N6.** `--json` on `doctor` or `list`. Their structs are already tagged; wiring them up is a
  separate change.
- **N7.** Fixing the pre-existing `internal/cli/list.go:81` bug that passes `entry.Name` to
  `CheckWorktreeBranch` where `internal/health.go` uses `entry.GitBranch()`. `tws status` uses
  `GitBranch()` correctly for its own probe; `tws list` output is untouched. Split as
  `fix-list-wrong-branch-check`.
- **N8.** Portable process birth identity (`/proc/<pid>/stat` field 22, `kern.proc`, `pidfd`) to
  close the PID-reuse window. Baseline states the limit instead of mitigating it.
- **N9.** Any process-table scan (`ps`, `/proc` walk, agent-binary search). Liveness is only ever
  `Signal(0)` against a PID tws itself recorded, plus one tmux inventory.
- **N10.** Any change to `tws sync`, `tws stack`, `tws inject`, `tws push`, `tws export`,
  `tws import`, `tws migrate-layout`, `tws new`, or `tws add` behaviour. (`tws add` changes only by
  propagating the new `openDirect` error.)
- **N11.** Windows support for direct records; they inherit the POSIX `Signal(0)` boundary already
  present in `processAlive` (`internal/session.go:263-269`).
- **N12.** Recording a `tws add --open --tmux` / `tws open --tmux` session. tmux opens are observed
  through the tmux inventory, not through records.

## 4. Command surface

### 4.1 Syntax and registration

```
tws status [feature] [--json]
```

Registered in `internal/cli/root.go:23-50` as `statusCmd()`, alphabetically irrelevant but placed
directly after `doctorCmd()` in the `AddCommand` list. `Args: cobra.MaximumNArgs(1)`,
`ValidArgsFunction` returning `internal.ListFeatures()` for position 0 (identical to `doctorCmd`),
`cobra.ShellCompDirectiveNoFileComp`.

### 4.2 Help text (exact)

`Short`:

```
Show agent work status for every logical branch
```

`Long`:

```
Report what tws knows about each logical branch: whether it is materialized,
whether a tws-launched session is running, and whether anything needs
attention.

Scope. With no argument the report always covers every feature in the resolved
workspace, from any working directory. It is never scoped by your current
location — pass a feature name to filter. (This deliberately differs from
'tws space list', which is cwd-scoped.)

Your working directory selects which workspace is resolved; it never changes
what the report says about that workspace. Run from the repository, from a
worktree, or from the workspace root and the reported features, entries, and
issues are identical.

Two axes, never collapsed. 'runtime_presence' answers "is a tws-owned runtime
alive?" (present, absent, stale, unknown). 'agent_state' answers "what is the
agent doing?" (working, ready, blocked, done, unknown) and is always 'unknown'
at this version: tws launches agents but does not observe their turns. Use
'needs_attention', which is authoritative, to decide where to intervene.

Exit status is 0 whenever a report was produced, including when branches need
attention or operational state is stale or corrupt. A non-zero exit means no
report could be produced at all.

--json prints one versioned document with a stable key set; absent values are
null and lists are never null.
```

Flag help:

- `--json` — `Output as JSON`

There is exactly one flag. Base ancestry is **not** computed by this command in any form; see §5.6.

### 4.3 Filtering

- **No argument** — every feature returned by `Workspace.ListFeaturesResolved()`
  (`internal/resolve.go:139`), sorted ascending. This is true from every cwd; there is no
  auto-detection from `DetectFeatureFromCwd`.
- **`tws status <feature>`** — exactly that feature. Resolution order:
  1. `internal.GuardFeatureName(ws.MetadataRoot, feature)` — **before** any path join, stat,
     `.sessions/` read, or tmux probe. A registered space name is refused with the canonical
     `ErrSpaceNameConflict` message and zero side effects. The guard root is `ws.MetadataRoot` in
     **both** modes, deliberately, because `status` reads workspace-scoped state through
     `Workspace` methods rooted at `MetadataRoot` (`ListFeaturesResolved`, `ResolveFeaturePath`,
     `SpaceDirOwners`) and the guard must protect the same root that the subsequent reads join
     against. In external mode `ws.MetadataRoot` *is* `internal.TwsRoot()` for the resolved
     workspace, so this is not a behaviour change there; it is a correctness statement for
     checkout mode, where `TwsRoot()` is the wrong root. (`tws close` keeps `internal.TwsRoot()`
     — §11.1 — because its external branch joins a caller-supplied name under `TwsRoot()` before
     any workspace is resolved.)
  2. Membership check against `ListFeaturesResolved()`. A name not in that list fails with
     `feature not found: <feature>` — the same string `CheckoutHealthReport.FilterFeature`
     (`internal/checkout_health.go:164`) already uses.
  3. `ws.ResolveFeaturePath(feature)`; an `ErrAmbiguousFeature` is propagated verbatim.
- **`ErrAmbiguousFeature` is fatal in the unfiltered run too.** The builder resolves a path for every
  feature in `ListFeaturesResolved()`; if any one of them is ambiguous (present in both the legacy
  and the new checkout layout, `internal/resolve.go:58-59`) the command exits 1 with that error and
  emits no document. Reporting the other features while silently picking one of two candidate
  directories for the ambiguous one would make the whole document untrustworthy — the ambiguity is
  a topology fault (§12), not operational state.
- `--json` composes freely with a feature filter.
- A feature filter narrows `features[]` to one element. It does **not** remove the `workspace` object
  and does **not** drop workspace-scoped issues: a workspace-level orphan lock is still reported,
  because suppressing it would hide the very thing the operator must fix. Workspace-scoped issues
  are, however, excluded from the *filtered* summary counters (§7.6).

### 4.4 Behaviour from every supported cwd

`tws status` resolves its workspace through `internal.ResolveStatusWorkspace()` (§13.1), a thin
wrapper over the same resolution `RequireWorkspace()` performs (`internal/workspace.go:440-465`).

| # | cwd | Resolution path | Result |
|---|-----|-----------------|--------|
| 1 | source repository root | `MainRepoRoot()` → `ResolveCurrentWorkspaceE` | full report |
| 2 | linked worktree root or nested subdir | `MainRepoRootIn` → `--git-common-dir` → main repo root | full report, identical to (1) |
| 3 | external workspace root | `MainRepoRoot()` fails → `DetectWorkspaceRoot` marker walk-up → `inferExternalRepoRoot` | full report when exactly one repo is inferred; **degraded report** when ambiguous or unknown (§4.5) |
| 4 | external feature dir or nested subdir | same as (3) — the marker walk-up reaches the workspace root | same as (3); the report is still workspace-wide |
| 5 | checkout repository root | `MainRepoRoot()` → mode `checkout` | full report |

Location never changes *what* is reported, only whether Git-derived fields can be filled. For a given
workspace and a given on-disk state, the `features[]` array — every entry, every field, in every
order — is **byte-identical** from all five locations. No field in the document is derived from the
process working directory (§5.5).

### 4.4.1 Metadata-root precondition (before any listing)

`BuildAgentStatus` performs one explicit precondition check as its **first** action, before
`ListFeaturesResolved()`, before `SpaceDirOwners`, and before any probe:

1. `os.Stat(ws.MetadataRoot)`. `ENOENT`, any other stat error, or a non-directory result is
   **fatal**: return the error, produce **no** report, exit 1.
2. `os.ReadDir(ws.MetadataRoot)`. Any error (permission denied, I/O error) is **fatal** on the same
   terms.

This exists because `ListFeaturesResolved` swallows its `os.ReadDir` errors
(`internal/resolve.go:159-186`: every listing loop is `if entries, err := os.ReadDir(...); err == nil`)
and therefore returns an empty, successful list for a metadata root that is missing or unreadable.
A dashboard that prints "no features" for an unreadable workspace is the exact failure mode §12
forbids: it is silence about topology, not a report about runtime. The successful `os.ReadDir`
result is reused by the builder, so this costs one extra `Stat` per invocation and no extra read.

The error text is `workspace metadata root unreadable: <path>: <err>` and it is the only content
written (to stderr, via `Execute()`); stdout stays empty in both human and `--json` form.

### 4.5 Degraded workspace (external, repo not inferable)

`inferExternalRepoRoot` (`internal/workspace.go:339-393`) can fail two ways: "maps to multiple
default repositories" and "cannot determine source repository". `tws list` today papers over this by
fabricating `Workspace{MetadataRoot: wsRoot, Mode: ModeExternal}` with an empty `RepoRoot` **and an
empty `StableID`** (`internal/cli/list.go:20-29`). `tws status` does neither: it neither fabricates
an empty identity nor dies.

When the workspace root is detected and exists but the repo cannot be inferred, the report is
produced with:

- `workspace.degraded: true`;
- `workspace.degraded_reason` = the verbatim `inferExternalRepoRoot` error text;
- `workspace.repo_root: null` and `workspace.stable_id: null` — **never** `""`, so an orchestrator
  cannot key on an empty-string identity;
- `workspace.branch`, `detached`, `dirty`, `active_git_op` all `null`;
- every Git-derived per-entry field `null` and `materialization.state: "unknown"`;
- one workspace-scoped issue `workspace-degraded` with severity `warning`, which **does** roll up to
  `needs_attention` (the operator must fix discovery before trusting materialization);
- exit 0.

Topology (`stack.yaml`), direct records, and tmux path verification are all still read, because none
of them requires `RepoRoot`. No Git command is executed with an empty `-C`, ever: every Git helper
call site is guarded by `if ws.RepoRoot == "" { skip }`.

When the workspace root itself cannot be detected, `ResolveStatusWorkspace` returns the original
error (`not inside a git repository or tws workspace`) and the command exits 1 having printed
nothing to stdout.

### 4.6 Exit codes

Exactly two, because `Execute()` maps any `RunE` error to `1` (`internal/cli/root.go:52-56`) and this
feature does not bypass that.

| Code | Meaning | Examples |
|------|---------|----------|
| `0` | A report was produced | any number of `needs_attention` rollups; stale sync lock; dead session owner; corrupt `active.json`; corrupt/unsupported direct record; corrupt `stack.yaml`; unreadable worktree; missing tmux binary; degraded workspace |
| `1` | No report could be produced | workspace not resolvable (§4.5 last paragraph); metadata root missing or unreadable (§4.4.1); untrusted `spaces.yaml` (`ListFeaturesResolved` error); `ErrSpaceNameConflict` from the guard; `feature not found`; `ErrAmbiguousFeature` (filtered **or** unfiltered); JSON encoder failure |

This deliberately differs from `tws doctor`, which returns an error when `HasErrors()`
(`internal/cli/doctor.go:16-20`). `status` is a polling surface: a loop that exits non-zero every
time a branch needs attention is unusable as a monitor. The rationale is stated in the `Long` help
and in the skills. §12 defines the reportable-vs-fatal boundary in full.

On exit 1 with `--json`, **nothing** is written to stdout — the document is encoded only after the
whole report is built successfully. The error goes to stderr through `Execute()`.

### 4.7 Output streams and encoder

All output goes through `cmd.OutOrStdout()` (never bare `fmt.Printf`), so both forms are assertable
in tests. JSON uses the established convention (`internal/cli/registry.go:74-83`,
`internal/cli/space.go:170-175`):

```go
enc := json.NewEncoder(cmd.OutOrStdout())
enc.SetIndent("", "  ")
return enc.Encode(report)
```

Two-space indent, trailing newline from `Encode`. Diagnostics and errors go to stderr.

## 5. Per-entry identity and materialization

### 5.1 Identity fields

One entry is emitted per `StackEntry` in `stack.yaml` (`internal/stack.go:13-33`), in **stack file
order**, including archived entries.

| JSON field | Source | Notes |
|---|---|---|
| `feature` | the resolved feature name | |
| `name` | `StackEntry.Name` | **tws identity** |
| `git_branch` | `StackEntry.GitBranch()` | **Git identity**; equals `Name` when `Branch` is empty |
| `base` | `StackEntry.Base` | a tws `Name` or a literal ref |
| `base_git_branch` | parent lookup, then `parent.GitBranch()`; else `Base` verbatim | same loop as `buildOneFeatureEntry` (`internal/checkout_health.go:552-563`) |
| `repo` | `StackEntry.Repo` or `null` | non-empty ⇒ cross-repo, §5.4 |
| `archived` | `StackEntry.Archived` | |
| `is_current_checkout` | see §5.5 | checkout mode only; `null` in external mode |

### 5.2 Which identity feeds which operation (normative)

`StackEntry.Name` — and only `Name` — is used for:

- the external worktree directory `<featurePath>/worktrees/<Name>` (`internal/cli/add.go:70`,
  `internal/cli/list.go:63`);
- the external tmux session name `ExternalTmuxSessionName(feature, Name)`, because both
  `internal/cli/open.go:168` and `internal/cli/close.go:66` build it from the CLI branch argument,
  which is the stack `Name`;
- the direct-record `<branch-id>` hash input `feature + "/" + Name`;
- matching the checkout session record (`CheckoutAgentSession.Name`);
- matching `CheckoutAgentSessionName(ws.StableID, feature, Name)`;
- every user-facing label and every `tws open/close/archive` command printed in guidance.

`StackEntry.GitBranch()` — and only `GitBranch()` — is used for:

- `gitRefExists(repoRoot, gitBranch)` (`internal/checkout_health.go:617`);
- comparing against `git -C <worktree> rev-parse --abbrev-ref HEAD`;
- matching `branch refs/heads/<gitBranch>` lines in `git worktree list --porcelain`;
- comparing against the checkout repository's current branch for `is_current_checkout` (§5.5).

A test asserts both directions with a decoupled fixture (`Name: api`, `Branch: jd/api`).

### 5.3 Materialization

`materialization` is an object, never a bare string, because "what materialization means" differs by
mode and a consumer must not have to guess.

```json
"materialization": {
  "kind": "worktree" | "ref",
  "state": "present" | "archived" | "missing" | "prunable-missing" | "cross-repo-unsupported" | "unknown",
  "path": "/abs/path" | null,
  "ref_exists": true | false | null,
  "checked_out_branch": "jd/api" | null,
  "dirty": true | false | null
}
```

**External mode** (`kind: "worktree"`), with `wtPath = <featurePath>/worktrees/<Name>`:

| Condition | `state` | `path` | Attention (code, §7.3) |
|---|---|---|---|
| `Repo != ""` | `cross-repo-unsupported` | `null` | none — `info` `cross-repo-unsupported` |
| `ws.RepoRoot == ""` (degraded) | `unknown` | `wtPath` | none at entry scope — the workspace-scoped `workspace-degraded` warning covers it |
| `wtPath` exists and is a dir | `present` | `wtPath` | none (plus `worktree-wrong-branch` / `worktree-dirty` / `worktree-dirty-blocking` when they apply) |
| absent, `Archived == true` | `archived` | `null` | none, no issue |
| absent, not archived, branch listed prunable | `prunable-missing` | `null` | **entry** `warning` `worktree-prunable-missing` |
| absent, not archived, not prunable | `missing` | `null` | **entry** `warning` `worktree-missing` |
| `wtPath` exists but is not a dir, or `Stat` fails with a non-`ENOENT` error | `unknown` | `wtPath` | **entry** `warning` `worktree-unreadable` |

`ref_exists` is `gitRefExists(ws.RepoRoot, git_branch)` when `RepoRoot != ""`, else `null`.
`checked_out_branch` and `dirty` are filled only when `state == "present"` and `RepoRoot != ""`.

Prunability is **not** `internal.IsPrunableWorktree` (`internal/exec.go:201`), which runs
`git worktree list` with no `-C` and matches on the caller-supplied name; invoked from an external
workspace root it fails and returns `false`, silently downgrading a missing worktree to `archived`
(analysis risk 5). Instead, one new helper runs **once per invocation**:

```go
// internal/agent_status.go
type WorktreeInventory struct {
    Available bool                // false when RepoRoot == "" or git failed
    ByBranch  map[string]string   // git branch -> worktree path (live entries)
    Prunable  map[string]bool     // git branch -> prunable
}
func BuildWorktreeInventory(repoRoot string) WorktreeInventory
```

backed by a single `git -C <repoRoot> worktree list --porcelain`, parsed by `worktree`/`branch`/
`prunable` blocks. `Available == false` ⇒ prunability is `null` and an absent non-archived worktree
is `missing`, not `prunable-missing`. `internal.IsPrunableWorktree` is left untouched for its
existing callers.

**Checkout mode** (`kind: "ref"`): there is no `worktrees/` directory at all
(`Workspace.WorktreePath` returns `""` for `ModeCheckout`, `internal/workspace.go:311-316`), so
materialization is ref existence.

| Condition | `state` | Attention (code, §7.3) |
|---|---|---|
| `Repo != ""` | `cross-repo-unsupported` | none — `info` `cross-repo-unsupported` |
| `gitRefExists` true | `present` | none |
| ref missing, `Archived == false` | `missing` | **entry** `warning` `ref-missing` |
| ref missing, `Archived == true` | `missing` | none — `info` `ref-missing-archived`; `tws archive` preserves branches, so a vanished ref for an archived entry is notable but nobody is blocked on it |

`path` is always `null`, `checked_out_branch` is `null` (there is one physical checkout; its branch
is `workspace.branch`), and `dirty` is always `null` per entry — checkout dirtiness is a single
workspace property and reporting it per entry would be a lie (§7.4).

### 5.4 Cross-repo entries

`Repo != ""` short-circuits every Git and worktree probe (matching
`internal/checkout_health.go:566-571`). The entry is emitted with real identity, `runtime_presence`
from records/tmux only, and one `info`-severity issue `cross-repo-unsupported`. That issue never
produces `needs_attention` by itself; a stale or unverifiable session record on the same entry still
does, because it is real evidence about a runtime tws launched (§6.6).

### 5.5 `is_current_checkout` (no cwd-derived field exists)

**No field in this document is derived from the process working directory.** The report is a
projection of repository and workspace state only, so that a poll from the repository root, a linked
worktree, the external workspace root, or a feature directory yields a byte-identical `features[]`
array (criterion 7). A cwd-derived `current` flag was specified in an earlier draft and is removed:
it made two otherwise identical polls disagree, which breaks diffing, caching, and any orchestrator
that compares documents produced by different processes.

What survives is the one genuinely valuable, mode-aware fact, derived only from repository Git state:

```
"is_current_checkout": true | false | null
```

- **Checkout mode** — `true` when `workspace.branch == git_branch`, or when the recorded checkout
  session's `Feature`/`Name` match this entry (identical to `buildOneFeatureEntry`,
  `internal/checkout_health.go:546-550`). `false` otherwise. `null` when `workspace.branch` is
  unavailable (degraded workspace, `RepoRoot == ""`, or `healthCurrentBranch` failed) **and** no
  session record supplies the answer — "unknown", never a fabricated `false`.
- **External mode** — always `null`. There is no single current checkout: every materialized branch
  has its own worktree, all of them equally "checked out". Emitting `false` for all of them would
  imply a meaningful negative; `null` states correctly that the concept does not apply.

The human view's `[current]` tag (§8.8) is printed only when `is_current_checkout == true`, i.e.
only in checkout mode.

### 5.6 Base ancestry is out of scope

`tws status` computes no ancestry and emits no ancestry key. There is no `--ancestry` flag, no
`ancestry` field on any object, no ancestry issue code, and no ancestry helper, test, or fixture.

Rationale: ancestry is a *stack correctness* question, not an *agent attention* question, and it is
already an owned roadmap item — "Stack ancestry doctor" and "Stack status" in the P1 stack safety
and observability backlog (`docs/roadmap.md`). Shipping a second, weaker ancestry projection here
would fork the semantics before the owning feature defines them, and it costs three extra `git`
forks per entry on a surface designed to be polled. `tws doctor` already reports ancestry
(`CheckoutFeatureEntry.AncestryStatus`, `internal/checkout_health.go:110`) with no flag involved —
**`tws doctor` has no `--ancestry` flag** (`internal/cli/doctor.go:12-27` registers none), and any
statement to the contrary in an earlier draft was false and is deleted.

Adding an `ancestry` key later is additive under §8.1 and does **not** bump `schema_version`, so
deferring costs the future feature nothing.

## 6. Runtime state model

### 6.1 The two axes

```go
type RuntimePresence string // "present" | "absent" | "stale" | "unknown"
type AgentState string      // "working" | "ready" | "blocked" | "done" | "unknown"
```

Two **distinct Go types** in `internal/agent_status.go` with distinct constants, so no assignment
between them compiles. Baseline emits `AgentStateUnknown` for every session observation, every entry,
every feature, and the workspace, unconditionally — there is no code path that produces any other
value, and a test asserts that `working|ready|blocked|done` appear nowhere in an encoded document.

`tss` `ready` means "the agent finished its turn and awaits input". It is **not** tws `idle`. A
comment on the `AgentState` declaration states this, because that collapse is the single most likely
future regression.

### 6.2 Session observations

Every runtime fact is a `SessionObservation`; an entry carries `sessions: []` of them.

```json
{
  "kind": "checkout-direct" | "checkout-tmux" | "external-direct" | "external-tmux",
  "presence": "present" | "absent" | "stale" | "unknown",
  "agent_state": "unknown",
  "stage": "agent" | "shell" | "tmux" | "starting" | "<raw>" | null,
  "stage_recognized": true | false,
  "owner_pid": 4242 | null,
  "child_pid": 4243 | null,
  "liveness": "live" | "dead" | "unknown" | null,
  "tmux_session": "auth-api" | null,
  "path": "/abs/path" | null,
  "agent": "claude" | null,
  "started_at": "2026-08-11T09:00:00Z" | null,
  "updated_at": "2026-08-11T09:04:08Z" | null,
  "record_id": "9f3c1a2b" | null,
  "record_state": "ok" | "invalid" | "unsupported",
  "detail": "free text" | null
}
```

- `owner_pid` is the **liveness anchor** in every kind that has one. For `checkout-direct` it is
  `CheckoutAgentSession.PID`, which is the **tws** PID, not the agent PID
  (`internal/session.go:583`). For `external-direct` it is the record's `owner_pid`, also the tws
  PID. `child_pid` is detail only and is never the sole presence proof: it is absent during
  `starting` and changes at the agent → shell transition.
- `liveness` is the raw probe result; `presence` is the interpreted verdict.
- `record_id` is the **first 8 hex characters** of an external direct record's ownership token, for
  correlating `status` output with `close` output and with the on-disk filename. It is `null` for
  every other kind. See §9 for why the checkout lock token never appears even as a prefix.
- `stage` is passed through verbatim. The complete recognized set is `agent`, `shell`, `tmux`
  (`internal/session.go:583,599,657`) plus `starting` (new, external records only).
  `stage_recognized: false` for anything else — including the empty string — and the raw value is
  still emitted so an operator can see what a newer tws wrote.

### 6.3 Process liveness seam

`processAlive` (`internal/session.go:263-269`) collapses ESRCH and EPERM into `false`, which
misreports a live process owned by another user as dead. `status` needs the distinction, so a new
three-valued prober is added **without forking the implementation**:

```go
// internal/checkout_health.go (beside the existing seams)
type ProcessLiveness string
const (
    ProcessLive    ProcessLiveness = "live"
    ProcessDead    ProcessLiveness = "dead"
    ProcessUnknown ProcessLiveness = "unknown"
)

type ProcessProber interface{ Probe(pid int) ProcessLiveness }
```

`realProcessChecker` gains `Probe`: `pid <= 0` → `dead`; `os.FindProcess` error → `dead`;
`Signal(0) == nil` → `live`; `errors.Is(err, os.ErrProcessDone)` or `errors.Is(err, syscall.ESRCH)`
→ `dead`; `errors.Is(err, syscall.EPERM)` → `unknown`; any other error → `unknown`.
`realProcessChecker.Alive` is redefined as `Probe(pid) == ProcessLive`, which is behaviourally
identical to today (EPERM was already `false`), so `tws doctor` output does not change.
`internal/checkout_sync.go:289-296`'s duplicate `isProcessAlive` is **not** touched — it is the sync
transaction's own path and is out of scope.

Mapping to presence for record-backed observations:

| `Probe(owner_pid)` | `liveness` | `presence` | Attention |
|---|---|---|---|
| `live` | `live` | `present` | none |
| `dead` | `dead` | `stale` | **entry**, `warning` |
| `unknown` | `unknown` | `unknown` | **entry**, `warning` (names the pid and the reason) |

The `unknown` row is a `warning`, not an `info`, by the §7.1 invariant: an `unknown`
`runtime_presence` means tws holds a record it **cannot verify**, which is a state an operator must
resolve, so it must reach `needs_attention`. Every path that can produce `presence: "unknown"`
therefore emits a `warning` (§7.3).

**PID reuse is an accepted, documented limit.** A successful probe proves *a process with that PID
exists*, not that it is the recorded one. No mitigation is attempted: `started_at`-vs-mtime
cross-checking is explicitly forbidden (both are written by the same owner at the same instant and
cannot disagree in a way that reveals a *later* process), and **record age must never on its own move
a record from `present` to `stale`** — long agent sessions are normal. `started_at`/`updated_at` are
reported so a consumer can compute age itself. A test asserts an artificially old but live record
still reads `present`.

### 6.4 tmux inventory seam

All tmux observation goes through one snapshot per invocation. No per-entry `has-session`.

```go
// internal/agent_status.go
type TmuxPane struct{ Session, Path string }

type TmuxSnapshot struct {
    Available      bool            // tmux binary found in PATH
    ServerRunning  bool            // false when tmux reports no server
    Sessions       map[string]bool
    Panes          []TmuxPane
    PanesAvailable bool            // false when list-panes failed
    Err            error           // non-nil => inventory unusable
}

type TmuxInventoryProbe interface{ Snapshot() TmuxSnapshot }
```

`RealTmuxInventory.Snapshot()`:

1. `exec.LookPath("tmux")` — failure ⇒ `{Available: false}` and stop.
2. `tmux list-sessions -F '#{session_name}'`. Success ⇒ `ServerRunning: true`, `Sessions` filled
   (empty output ⇒ empty set). Failure whose combined output contains `no server running` or
   `error connecting to` ⇒ `ServerRunning: false`, empty `Sessions`, `Err: nil`. Any other failure
   ⇒ `Err` set, inventory unusable.
3. `tmux list-panes -a -F '#{session_name}\t#{pane_current_path}'`. Success ⇒ `PanesAvailable: true`
   and `Panes` filled; failure ⇒ `PanesAvailable: false` (not fatal).

`workspace.tmux` reports `{available, server_running, session_count, path_verification}` where
`path_verification` is `PanesAvailable`.

**Collision-safe verification.** `sanitizeSessionName` maps `.`→`_`, `:`→`_`, `/`→`-`
(`internal/cli/open.go:356-359`), so feature `a` + branch `b` and `tws open a-b --all` both produce
`a-b`, and the tmux namespace is global across workspaces. A name match alone is therefore not proof.

| Session kind | Name | Verification |
|---|---|---|
| `external-tmux` per branch | `ExternalTmuxSessionName(feature, Name)` | name in `Sessions` **and** at least one pane of that session whose canonicalized `pane_current_path` equals the canonicalized worktree path |
| feature `--all` | `ExternalFeatureTmuxSessionName(feature)` | name in `Sessions` **and** at least one pane whose canonicalized path equals the canonicalized feature path (the orchestrator window is created with `-c featurePath`, `internal/cli/open.go:305`) |
| `checkout-tmux` | the recorded `CheckoutAgentSession.TmuxSession`, verbatim | name in `Sessions` only — `CheckoutAgentSessionName` already hashes `workspaceID/feature/name` (`internal/session.go:124-136`), so it is collision-safe by construction and needs no path check |

Verification outcomes for external names (per-branch and feature `--all` alike):

| `Available` | `Err` | name present | pane path matches | `PanesAvailable` | Observation | Issue |
|---|---|---|---|---|---|---|
| false | – | – | – | – | **none** | workspace `info` `tmux-missing` |
| true | non-nil | – | – | – | **none** | workspace `warning` `tmux-unverifiable` |
| true | nil | no | – | – | **none** | none |
| true | nil | yes | yes | true | `present` | none |
| true | nil | yes | no | true | `unknown` | entry/feature `warning` `tmux-path-mismatch` |
| true | nil | yes | – | false | `unknown` | entry/feature `warning` `tmux-panes-unverified` |

Two rules make this table consistent with §7.1:

1. **No evidence ⇒ no observation.** tws records nothing for a tmux open (N12), so when tmux is
   missing or its inventory is unusable there is no per-branch evidence at all — the branch keeps
   whatever presence its *records* imply (normally `absent`) and the unverifiability is reported
   once, at workspace scope. Emitting an `unknown` observation on every branch because the tmux
   binary is absent would make an entire tmux-free workspace `needs_attention`, which is noise.
   The single exception is the checkout session record, which **is** durable evidence: an
   unverifiable `Mode=tmux` record is a `warning` (§6.5, criterion 60).
2. **Evidence we cannot confirm ⇒ `unknown` + `warning`.** A name match that no pane corroborates,
   or a name match with pane listing unavailable, is real evidence in an ambiguous state, so it
   yields `presence: "unknown"` and a `warning`, and therefore `needs_attention` (§7.1).

`tmux-path-mismatch` detail text (deliberately non-accusatory — a name collision is only one of
several benign explanations):

```
tmux session %q exists but no pane reports a working directory under %s;
it may belong to another workspace, or its panes may have changed directory
```

`tmux-panes-unverified` detail text:

```
tmux session %q exists but pane paths are unavailable, so the match is unverified
```

`ExternalTmuxSessionName`/`ExternalFeatureTmuxSessionName` are new exported helpers in a new
`internal/tmux_names.go`, wrapping an unexported `sanitizeExternalSessionName` moved verbatim from
`internal/cli/open.go:356-359`. `cli.sanitizeSessionName` and `cli.TmuxSessionName`
(`internal/cli/close.go:84-86`) become one-line delegations, so there is exactly one sanitizer and
every existing session name stays byte-identical. This resolves the package-direction constraint:
`internal` cannot import `internal/cli`.

### 6.5 Checkout session decision table

Read order is mandatory and differs from `buildSessionReport` (`internal/checkout_health.go:426-441`),
which treats "file missing" and "file unparseable" identically because
`LoadCheckoutAgentSession` (`internal/session.go:154-164`) returns one undifferentiated error:

1. `os.Stat(sessionStatePath(ws))`.
2. `os.Stat(sessionLockDir(ws))`.
3. Only if the state file exists, read and unmarshal it.
4. Only if the lock dir exists, read `sessionLockOwnerPath(ws)`.

| State file | Lock dir | Parse | Mode / probe | Observation | Issue scope + severity |
|---|---|---|---|---|---|
| ENOENT | ENOENT | – | – | none | none |
| ENOENT | exists | – | – | none | **workspace** `warning` `session-orphan-lock` — "session lock exists but no session state; run: tws close" |
| exists | any | fails | – | one observation, `presence: unknown`, `record_state: invalid`, **workspace-only** | **workspace** `warning` `session-state-invalid`, naming the path |
| exists | any | ok, `schema_version > 1` | – | `presence: unknown`, `record_state: unsupported`, **workspace-only** | **workspace** `warning` `session-state-unsupported` — "written by a newer tws" |
| exists | ENOENT | ok | any | as below, plus `detail: "lock missing"` | **workspace** `warning` `session-lock-missing` |
| exists | exists, `owner.json` unreadable or `PID <= 0` or `Token == ""` | ok | any | as below | **workspace** `warning` `session-lock-invalid` |
| exists | exists | ok | `direct`, probe `live` | `presence: present`, `stage` verbatim | none |
| exists | exists | ok | `direct`, probe `dead` | `presence: stale` | **entry** `warning` `session-owner-dead` — "session owner pid N is dead; run: tws close" |
| exists | exists | ok | `direct`, probe `unknown` | `presence: unknown` | **entry** `warning` `session-owner-unknown` |
| exists | exists | ok | `tmux`, tmux available, name present | `presence: present` | none |
| exists | exists | ok | `tmux`, tmux available, name absent | `presence: stale` | **entry** `warning` `session-tmux-gone` — "tmux session %q is gone; run: tws close" |
| exists | exists | ok | `tmux`, tmux binary missing or inventory `Err` | `presence: unknown`, **workspace-only** | **workspace** `warning` `tmux-unverifiable` — a record we cannot verify |
| exists | any | ok | `stage` not in the recognized set | as its mode dictates, `stage_recognized: false` | **workspace** `info` `session-stage-unrecognized` |
| exists | any | ok | `WorkspaceID != ws.StableID` | as its mode dictates | **workspace** `info` `session-workspace-id-mismatch` |
| exists | any | ok | `Feature`/`Name` match no stack entry, **or either is empty** | observation attached to `workspace.checkout_session`, **not** to any entry | **workspace** `warning` `session-unattributed` |

Every row that yields `presence: "unknown"` carries a `warning` (§7.1). For the four workspace-scoped
unknown rows (`session-state-invalid`, `session-state-unsupported`, `tmux-unverifiable`, and the
`session-lock-*` rows) the warning lives at workspace scope, which is where the observation lives:
those records are single-owner workspace state, and `workspace.runtime_presence` is `unknown` there.
The two entry-scoped unknown rows (`session-owner-unknown`, and `session-tmux-gone`'s stale sibling)
carry their warning on the attributed entry.

**Attribution is gated on trustworthiness.** `(state.Feature, state.Name)` are read only *after* the
record has parsed and its `schema_version` has been accepted, and the rows marked **workspace-only**
above are never attributed to an entry at all. This is not cosmetic: an entry that receives an
`unknown` presence while owning no `warning` would read `idle`, breaking the §6.7/§7.1 invariant
"`stale` or `unknown` presence always coincides with `needs_attention`". Each workspace-only row
already carries the `warning` that owns its unknown observation, at the scope where the observation
lives, so the signal is never lost — only correctly homed. Where a choice exists (a valid `tmux`
record that an unusable tmux inventory cannot verify) the observation stays **workspace-only**
rather than gaining a second, redundant entry-scoped warning. A parsed record whose `feature` or
`name` is empty is `session-unattributed` for exactly the reason a record naming a vanished branch
is: it matches no stack entry. A checkout twin of the presence-invariant test walks the workspace,
every feature, and every entry, and asserts the summary counters still sum to the entry count; its
fixture creates real Git refs so no `ref-missing` warning can make it pass vacuously.

Attribution: the observation is appended to the `sessions[]` of the entry whose `(feature, name)`
equals `(state.Feature, state.Name)`, and only that entry. It never appears on siblings. The
projected copy is also placed at `workspace.checkout_session` so a consumer can find the single-owner
record without scanning; both copies are the same purpose-built struct and **neither** is the raw
`CheckoutAgentSession` (§9).

The lock is single-owner per workspace (`state/sessions/active.json`), so orphan-lock and
lock-mismatch signals have no branch to attach to; forcing them onto one would be a lie. They are
workspace-scoped by construction.

### 6.6 External direct record decision table

**External mode only.** In checkout mode the builder never computes a `<branch-id>`, never joins
`.sessions/`, and never stats or reads any direct record — checkout sessions are the single-owner
`state/sessions/active.json` pair and nothing else (§10.6). If a `.sessions/` tree exists under a
checkout metadata root (it cannot be created by this tool; only a hand-copied external tree could
put one there), it is **ignored entirely**: no observation, no issue, no `direct-record-orphan-branch`.
A test asserts the builder makes no filesystem call under any `.sessions/` path in checkout mode.

For each entry, records are read from `<featurePath>/.sessions/<branch-id>/` (§8).

| Record set for the branch | Entry `runtime_presence` contribution | Issue |
|---|---|---|
| directory absent, or empty | no observation | none |
| ≥1 record with `Probe(owner_pid) == live` | `present` | none |
| all records `dead` | `stale` | **entry** `warning` `direct-record-stale`, one per record, naming `record_id`, `owner_pid`, `stage`, `started_at` |
| some `live`, some `dead` | `present` **and** the dead ones still raise their `direct-record-stale` issues | **entry** `warning` per dead record |
| record `unknown` (EPERM) | `unknown` | **entry** `warning` `direct-record-unknown` |
| record `record_state: invalid` | `unknown` | **entry** `warning` `direct-record-invalid`, naming the file |
| record `record_state: unsupported` | `unknown` | **entry** `warning` `direct-record-unsupported` |
| `<branch-id>` directory matching no stack entry, holding **≥1** record | no entry to attach to | **feature** `warning` `direct-record-orphan-branch`, naming the directory (§10.5) |
| `<branch-id>` directory matching no stack entry, holding **0** records | no entry to attach to | **none** — an empty directory is residue of a cleaned-up session whose prune lost a race; it holds nothing to attribute, so reporting it would manufacture a `needs_attention` verdict out of zero records |
| `.sessions/` exists but cannot be enumerated | no observation | **feature** `warning` `direct-record-dir-unreadable`, naming the directory — orphan detection cannot run at all, and silence would understate the feature |

Two concurrent opens on the same branch are a **supported, observable** state: both records are
emitted individually in `sessions[]` (never collapsed), and `session_counts` reports
`{total, live, stale, unknown, invalid}`. `status` never removes a record, stale or not.

### 6.7 Entry-level presence rollup

```
present > unknown > stale > absent
```

Any `present` observation ⇒ `present`. Else any `unknown` ⇒ `unknown` (an unverifiable session might
be alive; claiming `stale` would be a stronger statement than the evidence supports). Else any
`stale` ⇒ `stale`. Else `absent`. No observations at all ⇒ `absent`.

`absent` means "tws holds no record and the probe was trustworthy". `stale` means "tws holds a record
that the runtime contradicts". They are never conflated, because the recovery guidance differs.

Feature-level `runtime_presence` rolls up its entries by the same precedence, and additionally
considers the feature `--all` tmux observation. Workspace-level rolls up its features and the
checkout session observation.

**Invariant.** By §6.3/§6.4/§6.5/§6.6, every observation with `presence: "unknown"` is accompanied by
a `warning` issue at the scope that owns it, and every `stale` observation is accompanied by a
`warning` at the scope that owns it. Therefore an `unknown` or `stale` `runtime_presence` at any
level always coincides with `needs_attention` at that level or above (§7.1). This is asserted by a
test, not assumed.

## 7. Attention model

### 7.1 Rollup precedence and hierarchical attention

Exactly three rollup values exist, at exactly three levels (`entry`, `feature`, `workspace`), with
the precedence:

```
needs_attention > active > idle
```

**Attention is hierarchical upward and never smears downward.** The three levels are defined
normatively as:

| Level | `needs_attention` when | `active` when | `idle` |
|---|---|---|---|
| `entry` | it has ≥1 **own** issue of severity `warning` or `error` (an issue whose `scope == "entry"` and whose `(feature, name)` is this entry) | no such issue and `runtime_presence == "present"` | otherwise |
| `feature` | it has ≥1 own `warning`/`error` issue (`scope == "feature"`, this `feature`) **or** any of its entries is `needs_attention` | no such condition and `runtime_presence == "present"` | otherwise |
| `workspace` | it has ≥1 own `warning`/`error` issue (`scope == "workspace"`) **or** any feature in the (possibly filtered) document is `needs_attention` | no such condition and `runtime_presence == "present"` | otherwise |

Consequences, all normative:

- A single stale direct record on `auth/api` makes that **entry**, the feature `auth`, and the
  **workspace** all `needs_attention`. An orchestrator can therefore poll
  `.workspace.attention.status` alone and be guaranteed never to miss a branch that needs a human.
  This is the whole point of the surface; without upward inheritance the top-level verdict would be
  silent while a child is on fire.
- A feature-level `sync-stale` does **not** touch any child entry. Downward smearing would make
  every branch in the feature look individually broken and would destroy the ability to point at the
  branch that actually needs work.
- **`issue_count` and `codes` are always own-scope only.** They describe the issues homed at that
  exact level (§7.2) and are never augmented by descendants. It is therefore normal and expected for
  a level to read `{"status": "needs_attention", "issue_count": 0, "codes": []}` — that is precisely
  the machine-readable statement "nothing is wrong *here*, something is wrong *below*". A consumer
  that wants the offending issues walks `report.issues[]`, which is the single home for all of them.
  A test asserts this exact shape for a feature and a workspace whose only fault is one entry-scoped
  `direct-record-stale`.
- `active` and `idle` are only ever reached when nothing below needs attention, so the three values
  are totally ordered and a parent is never "better" than its worst child.
- **`idle` is the residue.** It is never produced from a semantic signal, only from the absence of
  warnings plus a non-`present` presence. `absent` presence with no warning ⇒ `idle`; `present` with
  no warning ⇒ `active`. `stale` and `unknown` presence always arrive with a `warning` (§6.7
  invariant) and therefore always read `needs_attention`, so "stale but idle" is unreachable by
  construction — asserted by a test rather than assumed.

One tested function computes every level:

```go
// RollupAttention returns the attention status for one level.
//   own                 — issues homed at this exact level (scope+feature+name match), never a
//                         descendant's issues.
//   childNeedsAttention — true when any immediate child level already rolled up to
//                         needs_attention. Always false for an entry (entries have no children).
func RollupAttention(presence RuntimePresence, own []AgentStatusIssue, childNeedsAttention bool) AttentionStatus
```

with the body exactly:

```
if childNeedsAttention || anyWarningOrError(own) { return AttentionNeedsAttention }
if presence == PresencePresent                  { return AttentionActive }
return AttentionIdle
```

Evaluation order is bottom-up — entries, then features, then workspace — so `childNeedsAttention` is
always computed from an already-final child value and the rollup is a single pass with no fixpoint.

### 7.2 Issue model and the single-home rule

Every signal is one `AgentStatusIssue`, stored **exactly once**, in the flat top-level
`report.issues[]`:

```json
{
  "code": "direct-record-stale",
  "severity": "info" | "warning" | "error",
  "scope": "workspace" | "feature" | "entry",
  "feature": "auth" | null,
  "name": "auth-api" | null,
  "message": "direct session record 9f3c1a2b owner pid 4242 is dead (stage agent, started 2026-08-11T09:00:00Z)",
  "guidance": "run: tws close auth auth-api" | null
}
```

`severity` reuses the existing `CheckoutSeverity` vocabulary (`internal/checkout_health.go:39-44`)
minus `ok`. `workspace`-scoped issues have `feature: null, name: null`; `feature`-scoped issues have
`name: null`.

**No baseline code emits `error`.** The vocabulary keeps the value because `severity` mirrors
`CheckoutSeverity` and because a future code (or `tss`) may need it, but the boundary in §12 makes it
unreachable today: any condition severe enough to make the *report itself* untrustworthy is
command-fatal and produces no document at all, so everything that survives to be reported is at most
a `warning`. `summary.errors` is consequently always `0` at this version, which is a stable, honest
fact rather than an accident.

Levels carry only a rollup, never a copy of the issues:

```json
"attention": { "status": "needs_attention", "issue_count": 2, "codes": ["direct-record-stale", "worktree-missing"] }
```

`codes` is the sorted, de-duplicated code list **at that exact scope** — enough to branch on without
re-scanning, small enough not to be a duplicate payload. `issue_count` counts only `warning` and
`error` **at that exact scope**, so an `info` never inflates it and a descendant never inflates it
(§7.1).

### 7.3 Normative issue-code table

This table is **exhaustive and closed**: `internal/agent_status.go` declares one constant per row and
no issue is ever constructed with a code outside it. A test enumerates the declared constants and
asserts the set equals this table, so adding a code requires editing this section. Adding a code is
additive under §8.1 and does not bump `schema_version`.

Columns: `scope` is the single home (§7.2); `severity` drives `needs_attention` (`warning`/`error`)
or not (`info`); `guidance` is the `guidance` field, `—` meaning `null`.

**Workspace scope**

| Code | Severity | Trigger | Guidance |
|---|---|---|---|
| `workspace-degraded` | warning | External workspace root detected but `inferExternalRepoRoot` failed (§4.5); `repo_root`/`stable_id` are `null` and all Git-derived fields are `null` | `run tws doctor and check the workspace marker and default repositories` |
| `session-orphan-lock` | warning | `state/sessions/checkout-session.lock` exists, `active.json` ENOENT (§6.5 row 2) | `run: tws close` |
| `session-state-invalid` | warning | `active.json` exists but is unreadable or unparseable | `inspect or remove <path>` |
| `session-state-unsupported` | warning | `active.json` parses with `schema_version > checkoutSessionSchema` | `written by a newer tws; upgrade tws` |
| `session-lock-missing` | warning | `active.json` exists, lock dir ENOENT | `run: tws close` |
| `session-lock-invalid` | warning | Lock dir exists but `owner.json` is unreadable, or `PID <= 0`, or `Token == ""` | `run: tws close` |
| `session-unattributed` | warning | Session record's `(Feature, Name)` match no stack entry; the observation lives only at `workspace.checkout_session` | `run: tws close` |
| `session-stage-unrecognized` | info | Record `stage` outside `starting\|agent\|shell\|tmux`; raw value still emitted | — |
| `session-workspace-id-mismatch` | info | Record `WorkspaceID != ws.StableID` | — |
| `repo-dirty` | info | Checkout repository is dirty and **no** checkout session record exists (§7.4) | — |
| `repo-dirty-blocking` | warning | Checkout repository is dirty **and** a checkout session record exists, so `restoreCheckoutSession` will refuse (§7.4) | `commit or stash before: tws close` |
| `repo-detached` | info | `healthCurrentBranch` reports a detached HEAD | — |
| `repo-git-op` | warning | `gitActiveOp` reports an in-progress rebase/merge/cherry-pick/bisect | `finish or abort the in-progress git operation` |
| `tmux-missing` | info | `exec.LookPath("tmux")` failed **and** no record requires tmux verification; no observation is emitted anywhere (§6.4) | — |
| `tmux-unverifiable` | warning | `tmux list-sessions` failed for a reason other than "no server", **or** a `Mode=tmux` checkout session record exists that cannot be verified | `start tmux or run: tws close` |

**Feature scope**

| Code | Severity | Trigger | Guidance |
|---|---|---|---|
| `stack-missing` | info | `LoadStack` failed with `ENOENT`; `stack_state: "missing"`, `entries: []`. Normal immediately after `tws add` | `run: tws new <feature> <branch>` |
| `stack-invalid` | warning | `stack.yaml` exists but fails to parse, or fails to read for a non-`ENOENT` reason; `stack_state: "invalid"`, `entries: []` | `inspect <path>` |
| `sync-in-progress` | info | Checkout transaction present with a **live** lock (`buildOneSyncReport` `liveness: "live"`) | — |
| `sync-stale` | warning | Checkout transaction present and `liveness: "stale"` — lock file absent, or lock PID not live | `run: tws sync <feature> --continue  or  tws sync <feature> --abort` |
| `sync-invalid` | warning | Checkout transaction unreadable **or** unparseable **or** its lock file unparseable **or** lock `PID <= 0` — i.e. every `buildOneSyncReport` `liveness: "invalid"` case, deliberately **collapsed into one code** | `corrupt sync state; inspect <state dir> then rerun: tws sync <feature> --abort` |
| `sync-failed` | warning | Checkout transaction carries a failure (`FailureMsg` or `FailureKind` non-empty), whatever its liveness | `run: tws sync <feature> --continue  or  tws sync <feature> --abort` |
| `sync-state-present` | info | External `.sync-state.yaml` exists with an empty `failed_branch` | — |
| `sync-state-invalid` | warning | External `.sync-state.yaml` exists but `LoadSyncState` failed (unreadable or unparseable) | `inspect <path>` |
| `direct-record-orphan-branch` | warning | A `<featurePath>/.sessions/<branch-id>/` directory holding **≥1 record** whose id matches no current stack entry of this feature (§10.5) — the records cannot be attributed to a branch. An **empty** such directory emits nothing | `inspect <dir>; it belongs to a renamed, archived, or deleted branch` |
| `direct-record-dir-unreadable` | warning | `ListDirectSessions(featurePath)` failed, so the feature's records could not be enumerated and orphan detection could not run | `inspect <featurePath>/.sessions` |
| `tmux-path-mismatch` | warning | The feature `--all` tmux session name exists but no pane reports a path under the feature path (§6.4) | `check which tmux session owns that name` |
| `tmux-panes-unverified` | warning | The feature `--all` tmux session name exists but `list-panes` failed, so the match is unverified | `check which tmux session owns that name` |

**Entry scope**

| Code | Severity | Trigger | Guidance |
|---|---|---|---|
| `worktree-missing` | warning | External, not archived, worktree dir absent, Git reports no prunable entry (or the inventory is unavailable) | `run: tws add <feature> <branch>` |
| `worktree-prunable-missing` | warning | External, not archived, worktree dir absent and `git worktree list --porcelain` marks the branch's entry prunable | `run: git worktree prune, then: tws add <feature> <branch>` |
| `worktree-unreadable` | warning | External, the worktree path exists but is not a directory, `Stat` failed for a non-`ENOENT` reason, or `rev-parse` in it failed | `inspect <path>` |
| `worktree-wrong-branch` | warning | External, worktree present, `checked_out_branch != git_branch` | `run: git -C <path> switch <git_branch>` |
| `worktree-dirty` | info | External, worktree present and dirty, and no sync wants it | — |
| `worktree-dirty-blocking` | warning | External, worktree present and dirty, **and** the feature's `.sync-state.yaml` names this branch in `failed_branch` or `pending` | `commit or stash in <path> before: tws sync <feature>` |
| `ref-missing` | warning | Checkout, `Archived == false`, `gitRefExists(git_branch)` false | `run: tws new <feature> <branch>` |
| `ref-missing-archived` | info | Checkout, `Archived == true`, ref absent — `tws archive` preserves branches, so this is notable but blocks nobody | — |
| `cross-repo-unsupported` | info | `StackEntry.Repo != ""`; every Git and worktree probe is short-circuited (§5.4) | — |
| `direct-record-stale` | warning | External direct record whose `owner_pid` probes `dead`; one issue per record | `run: tws close <feature> <branch>` |
| `direct-record-unknown` | warning | External direct record whose `owner_pid` probes `unknown` (EPERM) — held by another user, not provably dead | `check pid <n>; it may belong to another user` |
| `direct-record-invalid` | warning | Record file unreadable, unparseable, token mismatch, identity mismatch, or `owner_pid <= 0` (§10.5) | `inspect <file>` |
| `direct-record-unsupported` | warning | Record `schema_version > directSessionSchema` | `written by a newer tws; upgrade tws` |
| `session-owner-dead` | warning | Checkout session record, `Mode: direct`, owner pid probes `dead` | `run: tws close` |
| `session-owner-unknown` | warning | Checkout session record, `Mode: direct`, owner pid probes `unknown` (EPERM) | `check pid <n>; it may belong to another user` |
| `session-tmux-gone` | warning | Checkout session record, `Mode: tmux`, tmux available and the recorded session name is absent | `run: tws close` |
| `sync-failed-branch` | warning | External `.sync-state.yaml` `failed_branch` names this branch | `resolve the conflict in <path>, then: tws sync <feature> --continue` |
| `sync-current-branch` | info | Checkout transaction `Plan[CurrentIndex].Branch` names this branch; the attention lives on the feature-scoped sync code | — |
| `tmux-path-mismatch` | warning | The per-branch tmux session name exists but no pane reports a path under the worktree path (§6.4) | `check which tmux session owns that name` |
| `tmux-panes-unverified` | warning | The per-branch tmux session name exists but `list-panes` failed | `check which tmux session owns that name` |

Notes on the reconciliations this table performs:

- **`stack-missing` now always emits an `info` issue.** An earlier draft said "no issue" in §8.3 and
  simultaneously listed `stack-missing` as a feature-scoped code; both cannot be true. The issue is
  emitted (so a machine consumer sees *why* `entries` is empty) at `info` severity (so an
  empty-but-valid feature never reads `needs_attention`). §8.3 is aligned with this.
- **`sync-lock-invalid` is deleted; `sync-invalid` absorbs it.** `buildOneSyncReport` produces
  `liveness: "invalid"` for four distinct causes (unreadable transaction, unparseable transaction,
  unparseable lock, `lock.PID <= 0`) and does not tell the caller which. Emitting two codes would
  force the builder to re-derive a distinction the reused helper never exposes, so the two codes are
  collapsed into one and the *cause* is carried in `message` (which quotes the helper's own
  `Guidance` text verbatim). A typed `cause` field was considered and rejected: it would be a new
  public key encoding an internal enum that no consumer asked for.
- **`feature-tmux-unknown` is deleted.** The feature `--all` tmux observation now uses the same two
  codes as the per-branch one (`tmux-path-mismatch`, `tmux-panes-unverified`) at feature scope, and
  they are `warning`s because they produce `presence: "unknown"` (§6.4, §7.1).
- **No ancestry codes exist** (§5.6): `ancestry-stale`, `ancestry-divergent`, and `ancestry-missing`
  from an earlier draft are removed with the feature.
- **`direct-record-dir-unreadable` is new and feature-scoped.** An earlier draft dropped the
  `ListDirectSessions` error on the floor (`if inventory, err := ...; err == nil`), which is silence
  about a whole feature's records. It is feature-scoped because the failure is the feature's
  `.sessions` root, and no branch can be named from it.
- **`direct-record-orphan-branch` is feature-scoped**, not workspace-scoped as an earlier draft said:
  the orphan directory lives at `<featurePath>/.sessions/`, so the feature is known exactly and only
  the branch is unattributable. It rolls up to the workspace through §7.1 regardless.

**Inheritance downward is a reference, not a status.** Each entry carries
`feature_attention: true|false`, a read-only mirror of *whether its feature has an own-scope
`warning`/`error` issue* — deliberately **not** a mirror of `features[i].attention.status`, which
under §7.1 may itself be `needs_attention` merely because this very entry is. It does **not** change
the entry's own `attention.status`, does not appear in the entry's `codes`, and does not increment
the entry's `issue_count`. A test asserts that a feature-level `sync-stale` leaves every child entry
at `idle`/`active` with `feature_attention: true`, while the feature and workspace read
`needs_attention`.

### 7.4 Dirty trees — "where observable"

- **Checkout**: there is one physical checkout, so `sessionDirty(ws.RepoRoot)`
  (`internal/session.go:533`) is a **workspace** property, reported as `workspace.dirty`. It is
  `info` normally, and escalates to a `warning` `repo-dirty-blocking` **only when a checkout session
  record exists**, because dirt is exactly what makes `restoreCheckoutSession`
  (`internal/session.go:704-712`) refuse and therefore what will make `tws close` fail. It is never
  reported per stack entry.
- **External**: dirt is per worktree and genuinely branch-local. It is `info` `worktree-dirty` by
  default and escalates to `warning` `worktree-dirty-blocking` only when a sync wants that worktree
  (§7.3). Rationale: external dirt blocks nothing at rest — there is no restore to fail.

### 7.5 `unread_decisions`

Reported per entry as an integer, `len(internal.UnreadDecisions(featurePath, Name))` —
`UnreadDecisions` returns `[]Decision` (`internal/decisions.go:161`), not a count, and the status
document deliberately emits only the count, never the decision payloads (those are `tws decisions`'
surface and would be an unbounded payload on a polled endpoint). `UnreadDecisions` returns `nil` on
any load error, so an unreadable decisions file reads `0` and raises no issue.

It **never** contributes to `needs_attention` and raises no issue. Consumers that want it as an
attention input can compute that themselves. This is why `decision-read-tracking` stays a **soft**
dependency.

### 7.6 Summary counters

`report.summary` counts what is present in the (possibly filtered) document:

```json
"summary": {
  "features": 2,
  "entries": 6,
  "needs_attention": 2,
  "active": 1,
  "idle": 3,
  "runtime_present": 1,
  "runtime_stale": 1,
  "runtime_unknown": 0,
  "runtime_absent": 4,
  "issues": 4,
  "warnings": 3,
  "errors": 0
}
```

Normative counter definitions:

- `features` = `len(report.features)`; `entries` = the total number of entries across them.
- `needs_attention` + `active` + `idle` counts **entries only** and always sums exactly to `entries`,
  because the three rollups are exhaustive and mutually exclusive (§7.1). Feature- and
  workspace-level rollups are **not** counted here; they are read from `features[i].attention` and
  `workspace.attention` directly. A test asserts the sum identity.
- `runtime_present` + `runtime_stale` + `runtime_unknown` + `runtime_absent` likewise counts entries
  only and sums to `entries`. By the §6.7 invariant, every entry counted in `runtime_stale` or
  `runtime_unknown` is also counted in `needs_attention`.
- `issues` = `len(report.issues)`; `warnings` and `errors` count issues of that severity at **every**
  scope, including workspace-scoped issues that survive a feature filter (§4.3). `info` issues are
  therefore `issues - warnings - errors`. `errors` is always `0` at this version (§7.2).
- Under a feature filter, every counter describes the filtered document; workspace-scoped issues are
  excluded from the entry counters (they have no entry) but **are** included in
  `issues`/`warnings`/`errors`.

`workspace.attention` — not `summary` — is the top-level verdict an orchestrator polls, because it is
the only field that inherits from every level (§7.1).

## 8. JSON contract

### 8.1 Envelope

```json
{
  "schema_version": 1,
  "generated_at": "2026-08-11T09:04:08Z",
  "workspace": { ... },
  "features": [ ... ],
  "issues": [ ... ],
  "summary": { ... }
}
```

`schema_version` is the integer constant `agentStatusSchema = 1` in `internal/agent_status.go`,
following the `checkoutSessionSchema` precedent (`internal/session.go:18`). It is bumped only for a
**breaking** change (a removed key, a changed type, a narrowed enum). Adding a key, adding an enum
value, or adding an issue code is additive and does **not** bump it — this is exactly the extension
point `tss-agent-state-provider` uses to populate `agent_state`.

### 8.2 `workspace`

```json
{
  "mode": "external" | "checkout",
  "stable_id": "3f2a1b0c9d8e7f60" | null,
  "repo_root": "/abs/repo" | null,
  "metadata_root": "/abs/repo.tws",
  "degraded": false,
  "degraded_reason": null,
  "branch": "main" | null,
  "detached": false | null,
  "dirty": false | null,
  "active_git_op": "rebase" | null,
  "tmux": { "available": true, "server_running": true, "session_count": 3, "path_verification": true },
  "checkout_session": { ...SessionObservation... } | null,
  "runtime_presence": "present",
  "agent_state": "unknown",
  "attention": { "status": "needs_attention", "issue_count": 1, "codes": ["session-orphan-lock"] }
}
```

`branch`, `detached`, `dirty`, `active_git_op` come from `healthCurrentBranch`, `gitDirty`, and
`gitActiveOp` (`internal/checkout_health.go:266-300`) when `repo_root != ""`; all four are `null`
otherwise. In external mode they describe the **source repository**, not any worktree, and the help
text says so.

### 8.3 `features[]`

```json
{
  "feature": "auth",
  "path": "/abs/repo.tws/auth",
  "stack_state": "ok" | "missing" | "invalid",
  "sync": { ... } | null,
  "feature_tmux": { ...SessionObservation with kind "external-tmux"... } | null,
  "entries": [ ... ],
  "runtime_presence": "present",
  "agent_state": "unknown",
  "attention": { "status": "idle", "issue_count": 0, "codes": [] }
}
```

`stack_state`: `ok` when `stack.yaml` parsed; `missing` when `LoadStack` failed with `ENOENT` (a
feature dir with no stack is normal right after `tws add`) ⇒ `entries: []` plus a feature-scoped
`info` `stack-missing` — an issue is emitted so a machine consumer can tell "empty because there is
no stack yet" from "empty because the stack failed to parse", and it is `info` so the feature still
reads `idle`; `invalid` when the file exists but fails to parse, or fails to read for a non-`ENOENT`
reason ⇒ `entries: []`, feature-scoped `warning` `stack-invalid` naming the path. A feature is
**never** silently skipped the way `buildFeatureEntries` (`internal/checkout_health.go:520-524`)
skips it today.

`sync` is a discriminated projection, never the raw `CheckoutTransaction` or `SyncState`. It is
`null` when neither a checkout transaction nor an external `.sync-state.yaml` exists for the feature:

```json
{
  "kind": "checkout" | "external",
  "stage": "rebasing" | null,
  "liveness": "live" | "stale" | "invalid" | null,
  "failure_reason": "conflict" | null,
  "current_branch": "auth-api" | null,
  "failed_branch": "auth-api" | null,
  "lock_pid": 4242 | null,
  "lock_live": true | false | null,
  "pending": ["auth-routes"],
  "completed": ["auth-models"],
  "skipped": []
}
```

**Exact field mapping — `kind: "checkout"`.** The source is one `CheckoutSyncReport` from
`buildOneSyncReport(feature, txPath, stateDir, proc)` (`internal/checkout_health.go:351-422`),
reused verbatim, never reimplemented. `<feature>-checkout-sync.yaml` under the checkout state dir is
what makes the object exist at all; its absence ⇒ `sync: null`.

| JSON field | Source | Null when |
|---|---|---|
| `stage` | `CheckoutSyncReport.Stage` (i.e. `CheckoutTransaction.Stage`) | the report never parsed the transaction (`liveness: "invalid"`), or `Stage` is empty |
| `liveness` | `CheckoutSyncReport.Liveness` — `"live"`, `"stale"`, or `"invalid"` verbatim | never (always one of the three for `kind: "checkout"`) |
| `failure_reason` | `CheckoutSyncReport.FailureReason`, which is `tx.FailureMsg` when non-empty else `string(tx.FailureKind)` | both are empty |
| `current_branch` | `CheckoutSyncReport.CurrentBranch` = `tx.Plan[tx.CurrentIndex].Branch` when `0 <= CurrentIndex < len(Plan)` | index out of range, or unparsed |
| `lock_pid` | `CheckoutSyncReport.LockPID` — `lock.PID` when the lock parsed, else `tx.LockPID` | both absent or `<= 0` |
| `lock_live` | `CheckoutSyncReport.LockLive` | the lock was never evaluated (`liveness: "invalid"` from an unparsed transaction) |
| `failed_branch` | — | **always `null`**; a checkout transaction has no failed-branch field, the failing branch is `current_branch` |
| `pending`, `completed`, `skipped` | — | **always `[]`**; a checkout transaction's plan is not projected here (it is `tws sync`'s surface) |

**Exact field mapping — `kind: "external"`.** The source is `*SyncState` from
`LoadSyncState(featurePath)` (`internal/syncstate.go:23`); `<featurePath>/.sync-state.yaml` existing
is what makes the object exist. A read/parse failure other than `ENOENT` produces
`sync: {kind: "external", liveness: "invalid", ...all else null/[]}` plus the feature-scoped
`sync-state-invalid` warning.

| JSON field | Source | Null when |
|---|---|---|
| `failed_branch` | `SyncState.FailedBranch` | empty |
| `pending` | `SyncState.Pending`, nil normalized to `[]` | never (array) |
| `completed` | `SyncState.Completed`, nil normalized to `[]` | never (array) |
| `skipped` | `SyncState.Skipped`, nil normalized to `[]` | never (array) |
| `stage` | — | **always `null`**; external sync records no stage |
| `liveness` | `"invalid"` only for an unparseable file | **`null`** otherwise — external sync has no lock and no PID, so it has no liveness |
| `failure_reason` | — | **always `null`**; `SyncState` records no reason, only the branch |
| `current_branch` | — | **always `null`** |
| `lock_pid`, `lock_live` | — | **always `null`** |

`SyncState.StartedAt` is deliberately not projected: no duration or age field exists anywhere in the
document (§8.7).

`feature_tmux` is always `null` in checkout mode (`--all` is rejected there,
`internal/cli/open.go:45`).

### 8.4 `entries[]`

```json
{
  "feature": "auth",
  "name": "auth-api",
  "git_branch": "jd/auth-api",
  "base": "main",
  "base_git_branch": "main",
  "repo": null,
  "archived": false,
  "is_current_checkout": false,
  "materialization": { ... },
  "sessions": [ ... ],
  "session_counts": { "total": 2, "live": 1, "stale": 1, "unknown": 0, "invalid": 0 },
  "unread_decisions": 0,
  "runtime_presence": "present",
  "agent_state": "unknown",
  "attention": { "status": "needs_attention", "issue_count": 1, "codes": ["direct-record-stale"] },
  "feature_attention": false
}
```

`session_counts` counts entries in `sessions[]`: `live` = `presence == present`, `stale` =
`presence == stale`, `unknown` = `presence == unknown`, `invalid` = `record_state != "ok"`. An
invalid record is counted in both `total` and `invalid` and in `unknown`.

### 8.5 Null vs omission (normative)

**Every key listed in this section is always present in every document.** There is no `omitempty`
anywhere in the status structs; absent scalar values are `null` and absent objects are `null`. Lists
(`features`, `entries`, `sessions`, `issues`, `codes`, `pending`, `completed`, `skipped`) are always
arrays and are **never** `null` — nil slices are normalized to `[]` before encoding, matching
`internal/cli/registry.go:74-83`.

Nullable scalars are modelled as Go pointers (`*int`, `*bool`, `*string`) or as `json.RawMessage`-free
purpose-built types; the encoder is never asked to distinguish "zero" from "absent" via `omitempty`.
This is the single reason status defines its own structs instead of reusing
`CheckoutFeatureEntry`/`CheckoutSessionReport`, which are `omitempty`-tagged throughout.

A test decodes the document into `map[string]any` and asserts the exact key set at every level, in
both modes, for both an empty workspace and a fully populated one.

### 8.6 Sorting (deterministic across polls)

| Collection | Order |
|---|---|
| `features[]` | feature name ascending (`sort.Strings`, byte order) — the order `ListFeaturesResolved` already returns |
| `entries[]` | **`stack.yaml` order**, unchanged — it is dependency order and is meaningful; it is stable as long as the file is |
| `sessions[]` | `started_at` ascending, then `record_id` ascending, then `kind` ascending; entries with a null `started_at` sort last |
| `issues[]` | `scope` rank (`workspace` < `feature` < `entry`), then `feature` (nulls first), then `name` (nulls first), then `code`, then `message` |
| `attention.codes` | ascending, de-duplicated |

### 8.7 Timestamps

- `generated_at` is produced by tws as `time.Now().UTC().Format(time.RFC3339)` — the same call
  `CheckoutAgentSession.StartedAt` and `sessionLockOwner.CreatedAt` already use
  (`internal/session.go:583,240`).
- Every timestamp **read from disk** (`started_at`, `updated_at`) is emitted **verbatim**, never
  reparsed or reformatted, so a malformed stored value is visible to the operator rather than
  normalized away. An empty stored value is emitted as `null`.
- No duration or age field is emitted. Consumers compute age from `generated_at` and `started_at`.

### 8.8 Human output

Header, then one line per entry, then issues, then a summary. All to `cmd.OutOrStdout()`.

```
Workspace: /Users/x/myapp.tws (mode: external)
  Attention: [!] needs_attention
  ID:        3f2a1b0c9d8e7f60
  Repo:      /Users/x/myapp
  tmux:      available (3 sessions)

STATUS      BRANCH                       PRESENCE  AGENT    SESSIONS  DETAIL
[!] attn    auth/auth-models             stale     unknown  0/1       direct record 9f3c1a2b owner pid 4242 stale
[i] active  auth/auth-api (git: jd/api)  present   unknown  1/1       direct stage=agent pid 5150
[ok] idle   auth/auth-routes             absent    unknown  0/0       -
[ok] idle   billing/api                  absent    unknown  0/0       [archived]

Branch: auth/auth-models
  [!] direct-record-stale: direct session record 9f3c1a2b owner pid 4242 is dead (stage agent, started 2026-08-11T09:00:00Z); run: tws close auth auth-models

Feature: auth
  [!] sync-stale: stale sync transaction; run: tws sync auth --abort

Workspace:
  [!] session-orphan-lock: session lock exists but no session state; run: tws close

4 branch(es): 1 active, 2 idle, 1 needs attention. 3 issue(s).
```

Two workspace-only shapes render the same way and are covered by tests. When only the workspace owns
a `warning` the header still reads `Attention: [!] needs_attention`, every row reads `[ok] idle`, and
the tail reads `... 0 needs attention` — the tail counts **branches**, never the workspace. When only
a feature owns a `warning` the `Feature:` block carries it, its entries stay `idle`/`active` with
`feature_attention: true` (§7.3), and the header verdict is reached by upward inheritance (§7.1).

Rules:

- **The workspace verdict is always printed**, as `  Attention: <glyph> <status>` in the header,
  never only when something is wrong. It is the one field an operator polls (§7.1), it inherits
  upward, and a workspace-owned fault has no table row it could otherwise appear on. Header labels
  are aligned to a common width.
- **Every issue in the document is rendered exactly once**, in a block keyed by its own home, in the
  order `Branch: <feature>/<name>` → `Feature: <feature>` → `Workspace:`; each line is
  `<glyph> <code>: <message>[; <guidance>]`. The branch blocks are what make the rule
  "a `[!] attn` row never has hidden guidance" true: the `DETAIL` column is a summary, so without
  them an entry-scoped `message` and — critically — its `guidance` would be reachable only through
  `--json`. A message that quotes a multi-line parser error is folded onto one line for the human
  view only; the JSON document keeps it verbatim (§8.7).
- The tail is explicitly branch-scoped: `N branch(es): A active, I idle, X needs attention. Y issue(s).`
  `N`, `A`, `I`, and `X` count entries only (§8.4 summary), while `Y` counts every issue at every
  scope. The workspace verdict is deliberately not repeated here, where it could be mistaken for a
  branch count; it is in the header.
- Glyphs reuse `severityIcon` (`internal/checkout_health.go:840-852`) and invent no new vocabulary.
  Two distinct mappings exist and neither is ad hoc:

  | Value | Glyph | Via |
  |---|---|---|
  | attention `needs_attention` | `[!]` | `severityIcon(SeverityWarning)` |
  | attention `active` | `[i]` | `severityIcon(SeverityInfo)` |
  | attention `idle` | `[ok]` | `severityIcon(SeverityOK)` |
  | issue severity `error` | `[E]` | `severityIcon(SeverityError)` |
  | issue severity `warning` | `[!]` | `severityIcon(SeverityWarning)` |
  | issue severity `info` | `[i]` | `severityIcon(SeverityInfo)` |

  The attention column maps a rollup onto the severity glyph that carries the same urgency; the issue
  blocks map each issue's own severity. `[E]` is unreachable at this version (§7.2) but is mapped so
  that a future `error` code needs no formatter change, and a unit test covers all four glyphs.
- `BRANCH` is `<feature>/<name>`, followed by ` (git: <git_branch>)` **only when it differs from the
  name** — identical to `FormatCheckoutHealth`/`FormatCheckoutList`.
- `SESSIONS` is `<live>/<total>`.
- `DETAIL` shows at most **two** session summaries, separated by `"; "` because each summary itself
  contains spaces and a space-joined pair would read as one sentence; a branch with more prints
  `+N more (see --json)`. A branch with no sessions and no tags prints `-`. Tags `[archived]`,
  `[current]` (printed only when `is_current_checkout == true`, i.e. checkout mode only, §5.5),
  `[missing]`, `[prunable-missing]`, `[wrong-branch]`, `[dirty]`, `[cross-repo]` append to `DETAIL`.
- Columns are computed from the widest value per column (like `space list`,
  `internal/cli/space.go:192-205`), never fixed-width truncated.
- Feature and workspace issue blocks are printed only when non-empty. `info`-severity issues are
  printed in the human view too, with `[i]`.
- An empty workspace prints the header, `No features found. Use 'tws add <feature>' to create one.`
  — the same string `tws list` uses (`internal/cli/list.go:41`) — then any issue blocks and the tail,
  and exits 0. The blocks and tail are not suppressed: a workspace-scoped fault (a degraded
  workspace, an orphan lock) has no other surface to appear on. A workspace whose features exist but
  hold no entry yet prints no table and no "no features" line, only its blocks and the tail, because
  its features **are** found.
- A feature filter prints only that feature's rows plus workspace issues.

## 9. Redaction (security)

Hard rules, each backed by a test that scans the **encoded document bytes** and the human output for
the literal secret value:

1. **`CheckoutAgentSession.LockToken` never appears anywhere**, in any form, including a prefix, a
   hash, or a length. `CheckoutAgentSession` is `json:"lock_token"`-tagged
   (`internal/session.go:47`), so it is **never** serialized directly, never embedded, and never
   passed to the encoder. It is projected field-by-field into `SessionObservation`.
2. **`sessionLockOwner.Token` never appears** (`internal/session.go:51-55`,
   `state/checkout-session.lock/owner.json`). It is the *same secret* as (1). When the builder reads
   `owner.json` it projects out `PID` and `CreatedAt` only. A separate test seeds a distinct token
   into `owner.json` from the one in `active.json` and asserts both are absent.
3. **The external direct record ownership token is never emitted in full.** Only `record_id`, the
   first 8 of its 32 hex characters, appears. The token grants nothing (it is an ownership tag, not a
   capability), which is why a prefix is acceptable here and is *not* acceptable for (1)/(2), where a
   prefix would reduce the entropy of a real capability.
4. **No transcript, no prompt, no agent argv.** A direct record stores `agent` as `parts[0]` of the
   configured agent command only — the bare binary token, never its arguments, never the resolved
   absolute path, never the environment. `status` emits that single token. Recording a full command
   line risks capturing a secret passed as a flag.
5. **No environment variables**, no `os.Environ` capture, anywhere.
6. Paths **are** emitted (`repo_root`, `metadata_root`, `feature.path`, `materialization.path`,
   session `path`) — `tws doctor` already prints comparable paths and an orchestrator cannot act
   without them. `CheckoutAgentSession.Links[]` is **not** emitted: link paths are a doctor concern,
   they add no attention signal, and they are the largest incidental path payload.
7. File modes for anything this feature writes: directories `0700`, files `0600` (§10.2).

## 10. External direct session records

### 10.1 Why they are in scope

Direct is the **default** external open path — tmux is used only with `--tmux` or an explicit
`config.use_tmux` (`internal/cli/open.go:161-181`, a `tmux-free-mode` behaviour). Deferring records
would leave the most common case unobservable and would make `tws status` close to useless in
external mode, while the inter-tool agreement requires tws to be authoritative **for the sessions it
launches**.

### 10.2 Layout, naming, permissions

```
<featurePath>/.sessions/                          0700  dir, created lazily
<featurePath>/.sessions/<branch-id>/              0700  dir, one per logical branch
<featurePath>/.sessions/<branch-id>/<token>.json  0600  file, one per open invocation
```

- **Feature-scoped and hidden**, beside the existing external dotfile `.sync-state.yaml`
  (`SyncStatePath`, `internal/syncstate.go:19-21`). `isReservedDir` already skips any `.`-prefixed
  name (`internal/resolve.go:207-209`), so it can never be listed as a feature. The external feature
  directory is not a Git working tree (worktrees are its children), so nothing becomes committable.
  `tws export` only walks `injectDir` (`internal/cli/export.go:153`), so records are never exported.
- **`<branch-id>`** = `hashedSessionID(identity: feature+"/"+Name, prefix: feature+"_"+Name)`, where
  `hashedSessionID` is the construction extracted verbatim from `CheckoutAgentSessionName`
  (`internal/session.go:124-136`): `sanitizeSessionPart(prefix)` truncated to `64 - 8 - 1 = 55`
  characters, `+ "_" +` the first 4 bytes of `sha256(identity)` as 8 hex characters.
  `CheckoutAgentSessionName` is refactored to call the same helper and a golden test asserts its
  output is byte-identical to before. The raw name is never used as a path component because
  `branch-name-decoupling` allows `/` and other path-hostile characters in `StackEntry.Name`.
- **`<token>`** = 16 bytes from `crypto/rand`, hex-encoded (32 chars) — the same scheme
  `acquireAgentSessionLock` uses (`internal/session.go:235-240`). It is both the filename stem and a
  field inside the record, so an owner can prove it owns a file without trusting the path.
- Modes are subject to umask; tests assert `perm & 0077 == 0` rather than exact equality.
- A `.sessions/` tree created by another user makes a second user's open fail with a permission
  error. That is honest and intended: the failure is surfaced with guidance naming the path, never
  silently downgraded to an unrecorded open.

### 10.3 Record schema (version 1)

```go
const directSessionSchema = 1

type DirectSessionRecord struct {
    SchemaVersion int    `json:"schema_version"`
    Token         string `json:"token"`
    Feature       string `json:"feature"`
    Name          string `json:"name"`
    GitBranch     string `json:"git_branch"`
    Path          string `json:"path"`
    Agent         string `json:"agent,omitempty"`
    OwnerPID      int    `json:"owner_pid"`
    ChildPID      int    `json:"child_pid,omitempty"`
    Stage         string `json:"stage"`
    StartedAt     string `json:"started_at"`
    UpdatedAt     string `json:"updated_at"`
}
```

(`omitempty` is fine *on disk* — the on-disk record is not the public JSON contract; §8.5's
no-omission rule governs `tws status --json` only.)

- `GitBranch` comes from `StackEntry.GitBranch()` (`internal/stack.go:23`) and is therefore a
  `stack.yaml`-derived value, not derivable from the path.
- `OwnerPID` is `os.Getpid()` — the **tws** process — and is stable for the record's whole lifetime.
- `ChildPID` is the current agent or shell PID; absent during `starting`.
- `Stage` ∈ `starting | agent | shell`.
- `StartedAt` is set once; `UpdatedAt` is rewritten on every transition. Both
  `time.Now().UTC().Format(time.RFC3339)`.
- No transcript, no prompt, no argv, no environment (§9.4).

### 10.4 API (`internal/direct_session.go`)

```go
func DirectSessionsDir(featurePath string) string                       // <featurePath>/.sessions
func DirectSessionBranchID(feature, name string) string
func CreateDirectSession(featurePath string, rec DirectSessionRecord) (token string, err error)
func UpdateDirectSession(featurePath, branchID, token string, mutate func(*DirectSessionRecord)) error
func LoadDirectSessions(featurePath, branchID string, want *DirectSessionIdentity) ([]LoadedDirectSession, error)
func ListDirectSessions(featurePath string) (map[string][]LoadedDirectSession, error)
func RemoveOwnedDirectSession(featurePath, branchID, token string) error

// DirectSessionIdentity is the (feature, name) a caller expects every record in a
// <branch-id> directory to carry. It exists because <branch-id> is a truncated hash
// (§10.2) and a hash collision must be detected, not silently merged.
type DirectSessionIdentity struct {
    Feature string
    Name    string
}

type DirectRecordState string // "ok" | "invalid" | "unsupported"

type LoadedDirectSession struct {
    Record   DirectSessionRecord
    File     string             // absolute path
    BranchID string             // the <branch-id> directory the record was found in
    State    DirectRecordState
    Problem  string             // non-empty when State != ok
}
```

**Why the loader takes an identity.** `<branch-id>` is `sha256(feature+"/"+Name)` truncated to 8 hex
characters (§10.2), so two distinct logical branches can, in principle, land in the same directory.
The identity check in §10.5 step 8 is the *only* collision detector in the design, and a loader that
cannot express "these are the records I expect" cannot perform it. Passing `featurePath, branchID`
alone is not enough: the feature is recoverable from the path, but the branch `Name` is not
recoverable from a hash. The explicit `want` parameter therefore makes identity validation a
**capability of the signature** rather than something a caller must remember to re-check.

`want` semantics:

| `want` | Behaviour |
|---|---|
| non-nil | Every record whose `(Feature, Name)` differ from `*want` is returned with `State: "invalid"`, `Problem: "identity mismatch"`. Used by the entry builder (§6.6), by `close` (§11.2), and by the rename/archive/delete guard for a single known branch (§11.3). |
| `nil` | **Identity matching is skipped.** Records are validated for every other rule (§10.5 steps 1–7, 9) and returned as found. Used by inventory scans that enumerate `<branch-id>` directories without knowing which branch each one belongs to — `ListDirectSessions`, the feature-wide guard in `tws rename feature` / `tws delete` (§11.3), and orphan-directory reporting. |

`ListDirectSessions(featurePath)` is exactly "for each `<branch-id>` directory under `.sessions/`,
call `LoadDirectSessions(featurePath, id, nil)`", keyed by `<branch-id>`. It never invents an
identity to check against.

**Orphan `<branch-id>` directories.** A directory whose id matches no `DirectSessionBranchID(feature,
entry.Name)` for any current stack entry of the feature is **not** an error, is **not** dropped, and
is **not** identity-checked (there is nothing to check against). It becomes exactly one report-level
issue — feature-scoped `warning` `direct-record-orphan-branch` (§7.3) — naming the directory and its
record count. Its records still appear in `report.issues[]` context only; they are attached to no
entry, because attaching them to an arbitrary branch would be a lie. `status` never removes such a
directory (§10.7).

- `CreateDirectSession` mints the token, `MkdirAll`s both `0700` directories, sets
  `SchemaVersion`/`StartedAt`/`UpdatedAt`/`Stage: "starting"`/`OwnerPID: os.Getpid()`, and writes
  atomically. It never overwrites an existing file (the token is fresh from `crypto/rand`); if the
  destination somehow exists it returns an error rather than clobbering.
- `UpdateDirectSession` re-reads, verifies `Record.Token == token` (refusing otherwise), applies
  `mutate`, refreshes `UpdatedAt`, and rewrites atomically.
- All writes go through `atomicSessionWrite` (`internal/session.go:169-195`): temp in the same
  directory, `Chmod(0600)`, `Write`, `Sync`, `Close`, `Rename`. A reader therefore sees the old
  bytes or the new bytes, never a partial record. Because each writer owns a distinct filename,
  concurrent writers never contend on a path at all — atomicity is only needed for one owner's
  create → update transitions.
- **No lock.** These are advisory liveness data, not a mutual-exclusion primitive. External mode
  deliberately permits concurrent opens and has no single-checkout invariant to protect. Concurrency
  safety comes from disjoint filenames.

### 10.5 Read validation

`LoadDirectSessions(featurePath, branchID, want)`:

1. `os.ReadDir(dir)`; `ENOENT` → `(nil, nil)`. Any other error → returned.
2. Skip non-regular entries and every name not matching `^[0-9a-f]{32}\.json$`. This also skips the
   `.tmp-session-*` files `atomicSessionWrite` creates, so a concurrent write is never read.
3. `ReadFile` returning `ENOENT` → **skipped silently**: a session exited between `ReadDir` and
   `ReadFile`, which is a benign race, not a finding.
4. Any other read error → `State: invalid`.
5. JSON parse failure → `State: invalid`.
6. `SchemaVersion > directSessionSchema` → `State: unsupported`.
7. `Record.Token != <filename stem>` → `State: invalid` (`token mismatch`) — catches a copied or
   renamed file.
8. **Only when `want != nil`**: `Record.Feature != want.Feature || Record.Name != want.Name` →
   `State: invalid` (`identity mismatch`). This is the **hash-collision detector**: a collision is
   *detected* rather than silently merging two branches. With `want == nil` this step is skipped
   entirely — an inventory scan has no expected identity and must not fabricate one.
9. `OwnerPID <= 0` → `State: invalid`.

An invalid record never aborts enumeration of its siblings. `ListDirectSessions` walks `.sessions/*/`,
calls `LoadDirectSessions(featurePath, id, nil)` for each, and returns a map keyed by `<branch-id>`;
a `<branch-id>` directory that matches no current stack entry is returned too, so `status` can report
it as an unattributable feature-scoped `direct-record-orphan-branch` issue rather than dropping it.

### 10.6 `openDirect` — signature, seams, ordering

**Signature change.** `openDirect(path string)` becomes:

```go
type directOpenOpts struct {
    Path      string
    Feature   string   // "" => untracked
    Name      string   // "" => untracked
    GitBranch string
    FeaturePath string
    Runner    directRunner   // nil => real
    Shell     directRunner   // nil => real
    LookPath  func(string) (string, error) // nil => exec.LookPath
    Store     directSessionStore           // nil => real
    Out       io.Writer                    // nil => os.Stdout
}

func openDirect(opts directOpenOpts) error
```

**`GitBranch` is a stack lookup with an empty fallback (normative).** `DirectSessionRecord.GitBranch`
(§10.3) is `stack.yaml`-derived and is **not** derivable from the path, so the tracked call site
(`internal/cli/open.go:181`) resolves it as:

1. `internal.LoadStack(featurePath)`.
2. Find the entry whose `Name == branch` and take `entry.GitBranch()`
   (`internal/stack.go:23` — returns `Branch` when set, else `Name`).
3. **If the stack fails to load, or contains no entry with that `Name`, `GitBranch` is the empty
   string `""`** and the open proceeds normally. It is never defaulted to `branch`, never guessed
   from Git, and the failure is never fatal: refusing to open an agent because a metadata file is
   unreadable would make an advisory record a hard dependency of the primary workflow.

`tws add --open` (`internal/cli/add.go:105`) has the entry in hand and passes `entry.GitBranch()`
directly. Untracked `--feature-dir` opens pass `""` (there is no entry).

On the read side, `status` treats an empty `git_branch` in a record as "not recorded" and falls back
to the live `StackEntry.GitBranch()` for the entry it attributes the record to; it never emits the
record's empty value as the entry's `git_branch` and never raises an issue for it. A record whose
non-empty `git_branch` disagrees with the current stack value is **not** an error either — a branch
may have been renamed under a live session — and the record's value is reported only inside the
session observation.

with the process seams mirroring the checkout precedent
(`SessionAgentRunner`/`SessionShellRunner`, `internal/session.go:57-66`, defaulted when nil at
`:590-595`), but start/wait shaped because the record needs the child PID **between** start and wait:

```go
type directProcess interface { PID() int; Wait() error; Terminate() error }
type directRunner  interface { Start(dir string, command []string) (directProcess, error) }
type directSessionStore interface {
    Create(featurePath string, rec internal.DirectSessionRecord) (string, error)
    Update(featurePath, branchID, token string, mutate func(*internal.DirectSessionRecord)) error
    RemoveOwned(featurePath, branchID, token string) error
}
```

The real `directProcess` wraps `exec.Cmd`: `Start`, `Process.Pid`, `Wait`. **`Terminate` only
signals; the caller always reaps.** It sends `SIGTERM` and arms a bounded 5-second escalation to
`SIGKILL` in a goroutine that selects on a `done` channel closed by `Wait`, so a child that honoured
`SIGTERM` is never killed late and no signal can land on a pid the OS has already recycled. It must
not call `Wait` itself: `exec.Cmd.Wait` is not safe to call twice, so a self-waiting `Terminate`
would race the caller's own reap. `Wait` is therefore idempotent (a `sync.Once` guarding the call and
caching its error), and every call site that terminates a child calls `Terminate()` and then
`Wait()` explicitly, so a process is waited for exactly once whichever path ends the session. These live in a new `internal/cli/direct_open.go` so the command body stays a
dispatcher; `open.go` never calls `exec.Command` or `os.WriteFile` for this path again.

**`os.Exit` removal.** The `fmt.Printf` + `os.Exit(1)` "agent not found in PATH" failure
(`internal/cli/open.go:248-251`) becomes `return fmt.Errorf("agent %q not found in PATH", parts[0])`.
`Execute()` already maps any `RunE` error to exit 1 after printing it to stderr
(`internal/cli/root.go:16,52-56`), so the exit status is unchanged while the failure becomes
assertable. All four call sites propagate:

| Call site | Change |
|---|---|
| `internal/cli/open.go:68` (checkout `--feature-dir`) | `return openDirect(...)`, untracked |
| `internal/cli/open.go:107` (external `--feature-dir`) | `return openDirect(...)`, untracked |
| `internal/cli/open.go:181` (external per-branch direct) | `return openDirect(...)`, **tracked** |
| `internal/cli/add.go:105` (`tws add --open`) | `if err := openDirect(...); err != nil { return err }` |

The two "Guard before openDirect, which has no error channel" comments
(`internal/cli/open.go:80,94`) are rewritten: the guards stay (they are correct and required), but
the stated justification becomes "guarded because the feature name is joined under `TwsRoot()`".

**Ordering for a tracked open** — `LookPath` is resolved **before** anything is written, so a missing
agent binary never leaves a record behind:

1. Resolve the agent command (`cfg.GetAgentCommand()`, plus the existing `-c` continuation for
   Claude, `internal/cli/open.go:243-246`) and `LookPath(parts[0])`. Failure → return the error,
   **no record created**.
2. **Create the record before spawning anything**: `Store.Create` writes `stage: "starting"`,
   `owner_pid: os.Getpid()`, no `child_pid`. Failure → nothing has been spawned; return the error.
   The window in which a live child is unrecorded is closed *by construction*.
3. Print the existing lines verbatim: `Opening: %s\nRunning: %s\n`.
4. `Runner.Start(path, parts)`. Failure → `RemoveOwned` **this token only**, return the error.
5. `Store.Update` → `stage: "agent"`, `child_pid: p.PID()`. Failure → `p.Terminate()`, `p.Wait()`,
   `RemoveOwned` this token, return `errors.Join(updateErr, termErr)`. Rationale: a persistence
   failure this early means the record store is broken for the session's whole lifetime, and the
   agent has produced no interactive state worth preserving.
6. `p.Wait()`. A non-zero agent exit prints the existing `Agent exited: %v` line and does **not**
   abort the shell transition (unchanged behaviour).
7. `Store.Update` → `stage: "shell"`, `child_pid` cleared, **before** starting the shell.
   Two failure modes, deliberately different:
   - **The record is gone (`ENOENT` on the record file, or `RemoveOwned`-style "not found").** This
     is a benign, expected race: `tws close` (§11.2), `tws rename`/`archive`/`delete` (§11.3), or an
     operator may have removed a record they believed stale while the owner was between stages.
     Treat it as a **warning**, never as a reason to end the session: print
     `Warning: session record was removed; recreating` and attempt **one** `Store.Create` with the
     same identity, `stage: "shell"`, `owner_pid: os.Getpid()`, and the original `StartedAt`
     preserved. The new token replaces the old one for the rest of the invocation, including step
     10's token-matched cleanup. If the recreate also fails, print
     `Warning: continuing without a session record: %v` and continue **unrecorded** — the branch then
     simply reads `absent` in `status`, which is the pre-feature behaviour and is honest.
     **The shell is started in every one of these cases.** Killing an interactive session because an
     advisory record vanished would destroy work the command was never asked to touch.
   - **Any other error** (permission denied, I/O error, token mismatch) → do **not** start the shell,
     `RemoveOwned` this token, return the error. (If it were started, the record would claim
     `stage: agent` with a dead child PID, and the store is demonstrably broken rather than merely
     raced.)
8. Print `Dropped into shell at: %s`, `Shell.Start(path, [$SHELL or /bin/sh])`. Start failure →
   `RemoveOwned`, return the error.
9. `Store.Update` → `child_pid: shell.PID()`. **On failure: warn on stderr and continue.** This is
   the one deliberate asymmetry with step 5, and it is decided here (analysis unresolved decision
   10): `child_pid` is detail only, `owner_pid` is the liveness anchor and is already correct, the
   record already carries the correct `stage: shell`, and terminating an interactive shell the user
   is already typing in destroys work the command was never asked to touch. The warning text is
   `Warning: could not update session record (child pid): %v`. `openDirect` returns `nil` for this
   case alone.
10. `shell.Wait()`, then **remove exactly the token-owned file** via `RemoveOwned`, which re-reads the
    file and unlinks only if the recorded `token` matches, then best-effort `os.Remove` on
    `<branch-id>/` and `.sessions/`. `ENOTEMPTY` (a concurrent sibling still holds a record) is
    expected and ignored. Removal failure is a **warning**, not a fatal error — the record is then
    merely stale, which the reader already handles correctly. There is no last-writer cleanup and no
    directory-wide sweep, ever.

**Untracked opens.** `--feature-dir` opens (`internal/cli/open.go:68,107`) have no logical branch and
therefore no `<branch-id>`. They are **explicitly unrecorded**: `Feature`/`Name` empty ⇒ steps 2, 5,
7, 9, 10 are skipped entirely. They still propagate every `LookPath`/start/wait error. Inventing a
synthetic branch id for them is rejected — it would attribute a session to a branch that does not
exist and pollute per-branch aggregation. The checkout-mode `--feature-dir` call site
(`internal/cli/open.go:68`) additionally must never touch external state at all; a test asserts no
`.sessions/` directory appears anywhere under the checkout metadata root.

**Checkout mode is otherwise untouched.** It keeps its single-owner `state/sessions/active.json` +
`checkout-session.lock` pair and gains no second parallel record.

### 10.7 Crash, staleness, and cleanup ownership

| Actor | May create | May remove | Rule |
|---|---|---|---|
| `openDirect` (owner) | its own token file | its own token file | token match, one file |
| `tws status` | nothing | **nothing** | strictly read-only, even for provably dead records |
| `tws close` (external) | nothing | provably dead records | token match, one file at a time (§11) |
| `tws rename` / `archive` / `delete` | nothing | provably dead records | token match, after refusing on live/unknown (§11.3) |

A crash or `kill -9` leaves records behind; that is the intended fallback. Staleness is detected on
read by probing `owner_pid`, exactly as the checkout session report does. **Stale records remain
attention signals** — never silently discarded, never auto-collected by a reader.

`EPERM` from `Signal(0)` means *not provably dead*. Such a record is kept, reported as `unknown`, and
is not counted as live-blocking by `close` (§11.2) but **is** blocking for the destructive commands
(§11.3).

Growth is bounded by concurrent opens, not by history: normal exits remove their own file and prune
empty parents, so an idle workspace's steady state is an absent `.sessions/` tree.

## 11. Command interactions

### 11.1 `tws close <feature> <branch>` (external) — guard

The current `closeCmd` comment claims it is *deliberately* guard-free because "the external branch
only builds a tmux session name and kills that session — it joins no root and creates, reads, or
removes nothing under `TwsRoot()`" (`internal/cli/close.go:38-43`). **This feature invalidates that
justification**: reading `.sessions/` means joining `internal.FeaturePath(feature)` =
`TwsRoot()/<caller-supplied feature>` (`internal/paths.go:84`) and then enumerating, reading, and
`os.Remove`-ing files underneath it. A caller-supplied name that names a **registered space
directory** would reach into that space's tree and delete files in it, and a name carrying a
separator or a traversal segment (`../outside`) would leave `TwsRoot()` altogether and reach a
prepared `.sessions/<branch-id>/<token>.json` outside the workspace. `internal.FeaturePath` performs
a bare `filepath.Join` and validates nothing.

Required, non-negotiable:

- `internal.GuardFeatureName(internal.TwsRoot(), feature)` is called in the external branch
  **immediately after** the two-arg check (`internal/cli/close.go:60-62`) and **before** any path is
  computed, statted, read, or removed, and before the tmux name is built — matching the guard
  placement in `open` (`internal/cli/open.go:95,115`) and `renameBranchExternal`
  (`internal/cli/rename.go:196`). The refusal is the first observable action.
  The guard root here is `internal.TwsRoot()`, **not** `ws.MetadataRoot` as in `status` (§4.3): the
  external `close` branch never resolves a `Workspace` at all — it joins the caller-supplied name
  directly under `TwsRoot()` via `internal.FeaturePath` — so `TwsRoot()` *is* the root that must be
  protected. The two roots coincide in external mode; the difference matters only because `status`
  also runs in checkout mode, where `close` takes its other branch.
- **`GuardFeatureName` itself validates the name.** It calls the existing
  `validateFeatureName(feature)` (`internal/resolve.go:212`) **before** it reads the registry and
  before its absent-registry `nil`, so every guarded command — not only `close` — refuses a
  separator, a traversal segment, or a reserved directory name before any join, stat, read, remove,
  or tmux name build, with the resolver's canonical message. This is the shared boundary: guarding
  only at `close` would leave every other `FeaturePath`-joining command (`open --feature-dir`,
  `open --all`, `archive`, `rename branch`, external `import`) validated nowhere, because
  `internal.FeaturePath` never validates and those paths never reach `Workspace.ResolveFeaturePath`.
  Consequence: `tws template sync <invalid-name>` and `tws hooks install <invalid-name>` now exit
  nonzero with that message instead of printing the void helper's stdout line and exiting 0; the
  rest of the §8.5.1 carve-out of `workspace-sibling-links` (unrelated `RequireFeaturePath`
  failures, a failing `RequireWorkspace` in `template sync`, and the `--all` loop bodies) is
  unchanged.
- **A malformed or untrusted `spaces.yaml` makes `close` fail closed.** `GuardFeatureName` calls
  `SpaceDirOwners(root)` (`internal/spaces.go:681-687`), which returns an error for unreadable,
  malformed, symlinked, or future-version spaces metadata. `close` propagates that error verbatim
  and exits non-zero **before** loading a record, before probing a PID, and before building or
  killing any tmux session. This is a deliberate behaviour change for a workspace with broken spaces
  metadata — `close` previously succeeded there — and it is the only safe option: without a trusted
  owners map, tws cannot prove the caller-supplied name does not reach into a registered space's
  tree, and `close` now deletes files under that name. It is listed in `CHANGELOG.md` alongside the
  ordering change and covered by a test using the existing `malformedSpacesFixtures()`.
- The comment is rewritten to state what *is* guarded: the external branch resolves a caller-supplied
  feature name to a path under `TwsRoot()` and mutates files beneath it, so it is guarded like every
  other external path-joining command; the checkout branch still resolves its identity from
  `active.json` rather than from a caller-supplied name and needs no guard.
- The checkout branch is unchanged and gains no guard.
- `internal/cli/space_guard_test.go:425-444`'s "guarded lifecycle surfaces" matrix gains `close`
  **and** `status`, both currently absent.

### 11.2 `tws close <feature> <branch>` (external) — exact ordering

This **intentionally changes the early-return ordering**. Today the first thing that can end the
command is `!sessionExists(session)` → `no tmux session found` (`internal/cli/close.go:69-71`). After
this feature, records are consulted before tmux, because refusing to disturb a live direct session
outranks killing a tmux session and because the old error would otherwise mask a branch with live
records. This is called out in `CHANGELOG.md`.

1. **Guard** (§11.1). On conflict, return with zero side effects.
2. **Load every record for the branch.** `branchID := internal.DirectSessionBranchID(feature, branch)`,
   `internal.LoadDirectSessions(featurePath, branchID, &internal.DirectSessionIdentity{Feature: feature, Name: branch})`.
   The identity is supplied because `close` knows exactly which branch it is acting on, so a
   hash-collided record from another branch must surface as `invalid` rather than be removed as this
   branch's stale record. `ENOENT` on the directory is a normal empty result. This happens before any
   tmux call.
3. **Refuse if any record is live** (`Probe(owner_pid) == live`). Print the live records
   (`record_id`, `owner_pid`, `child_pid` when present, `stage`, `started_at`), return a non-zero
   error, **kill no tmux session**, and **remove no record — not even provably stale siblings**, so
   the reported state matches the state on disk. The message states that `close` never kills a direct
   process and points at exiting the session, mirroring `CloseCheckoutSession`'s
   `direct checkout session is still active` refusal (`internal/session.go:690-692`). This applies
   **even when a tmux session also exists**: a live direct record wins, because killing tmux while a
   direct agent runs in another terminal destroys state the command was never asked to touch.
   A record with `State != ok` is *not provably stale*: it is neither counted live nor removed; it is
   reported (step 4a) and it does not by itself block the tmux kill.
4. **Remove only provably stale token-owned records.** With no live record, unlink each record whose
   `Probe(owner_pid) == dead`, one file at a time, matching the record's own `token` exactly as the
   owner-side cleanup does — never a directory sweep, never "remove the records for this branch".
   `Probe == unknown` (EPERM) ⇒ keep, report, do not block. Then best-effort `os.Remove` the
   now-possibly-empty `<branch-id>/` and `.sessions/`, tolerating `ENOTEMPTY`. A removal failure is a
   warning, not fatal.
4a. **Report every unverifiable record, before tmux is touched.** `State != ok` **and**
   `Probe == unknown` (EPERM) records are collected together as *unverifiable*: never removed, never
   blocking, always printed as
   `N direct session record(s) for <feature>/<branch> could not be verified and were left in place:`
   followed by one redacted `DescribeDirectSession` line each. They precede tmux handling so the
   operator reads what was left behind before reading what was killed, and because a close that
   changed nothing must not look like a close that cleaned up. `DescribeDirectSession` already
   renders the record path and ownership token redacted (§9), so this surface leaks nothing new.
5. **Preserve the tmux kill.** If the session exists, run the identical `tmux kill-session -t <name>`
   and print the identical `Closed tmux session: %s` line (`internal/cli/close.go:73-78`). A failure
   is still `error killing session: %w`.
6. **No tmux, but stale records were cleaned → success.** Exit 0 with
   `Removed %d stale direct session record(s) for %s/%s.` instead of the old error.
7. **Neither tmux nor any record → unchanged.** Return `no tmux session found for %s/%s` verbatim
   (`internal/cli/close.go:70`) — no new wording, no new exit code.
8. **No tmux, nothing removed, but unverifiable records remain → actionable error.** The flat
   `no tmux session found` would be a false negative: `close` *did* find state for this branch, it
   just could not act on any of it. Return instead
   `no tmux session found for <feature>/<branch>, and N direct session record(s) could not be verified, so nothing was removed; inspect them with: tws status --json`.
   This is a non-zero exit like row 5, so no caller's success/failure contract changes.

| Live records | Stale | Unverifiable | tmux session | Outcome |
|---|---|---|---|---|
| ≥1 | any | any | present or absent | refuse, non-zero, name live PIDs, kill nothing, remove nothing |
| 0 | ≥1 | any | present | report unverifiable, remove stale records, then kill tmux; existing tmux output preserved (extra lines precede it) |
| 0 | ≥1 | any | absent | report unverifiable, remove stale records, exit 0 with cleanup message (**was** `no tmux session found`) |
| 0 | 0 | ≥1 | present | report unverifiable, remove nothing, kill tmux |
| 0 | 0 | ≥1 | absent | report unverifiable, remove nothing, non-zero actionable error naming them and `tws status --json` (step 8) |
| 0 | 0 | 0 | present | **byte-for-byte identical to today** |
| 0 | 0 | 0 | absent | `no tmux session found for <feature>/<branch>`, unchanged |

The byte-for-byte compatibility claim is scoped to the **last two rows only**. Every other row is a
deliberate behaviour change and is documented as one.

Testability: the external branch is extracted into
`runExternalClose(out io.Writer, feature, branch string, proc internal.ProcessProber, tmux externalTmuxOps) error`
where `externalTmuxOps` has `Exists(name) bool` and `Kill(name) error`. `closeCmd`'s `RunE` supplies
the real implementations (`sessionExists`, `internal.Run("tmux","kill-session",...)`). No package-level
test globals.

### 11.3 Rename, archive, delete — records must not silently relocate or be destroyed

Both rename verbs invalidate record identity: `tws rename feature` is a single
`os.Rename(oldPath, newPath)` of the whole feature directory (`internal/cli/rename.go:64`), which
would move `.sessions/` silently so that every record's `feature` field and its `<branch-id>` hash
(computed over `feature/name`) disagree with their own location — and a live owner would keep writing
to a path that no longer exists while its token-matched cleanup silently no-ops, leaving a
permanently stale record under the new name. `tws rename branch` rewrites `StackEntry.Name` and may
rewrite the Git branch (`internal/cli/rename.go:117-230`), changing `<branch-id>`, `name`, and
`git_branch`; the external form also removes and re-adds the worktree, so `path` goes stale
independently.

**Decision: refuse-live, clean-stale, then proceed.** An atomic rewrite is rejected for version 1:
there is no lock in external mode, so no proof exists that a concurrently live owner cannot lose or
duplicate its record across the rewrite, and `git_branch` is not derivable from the path (it needs a
`stack.yaml` read), which makes the rewrite a stack-reading operation rather than a pure move.
Refuse-live is strictly simpler and cannot lose data.

The shared helper is:

```go
// internal/direct_session.go
//
// GuardDirectSessionsFor reports records that block a destructive operation.
// blocking = live OR unknown(EPERM) OR State != ok.
//
// targets pairs each <branch-id> with the identity its records must carry, or with a
// nil identity for a whole-feature inventory scan where the branch is not known
// per directory. Each target is loaded with LoadDirectSessions(featurePath, id, want),
// so a single-branch guard detects hash collisions while a feature-wide guard does not
// fabricate an identity to check against (§10.4).
type DirectSessionTarget struct {
    BranchID string
    Want     *DirectSessionIdentity // nil => skip identity matching
}

func GuardDirectSessionsFor(featurePath string, targets []DirectSessionTarget, proc ProcessProber) (blocking []LoadedDirectSession, stale []LoadedDirectSession, err error)
func RemoveStaleDirectSessions(featurePath string, stale []LoadedDirectSession) (removed int, err error)
```

`RemoveStaleDirectSessions` unlinks by `LoadedDirectSession.File` after re-verifying the recorded
token, one file at a time, and uses each record's `BranchID` to prune its now-possibly-empty parent —
never a directory sweep.

The CLI-side wrapper is
`guardDirectRecords(out io.Writer, featurePath, verb, subject string, targets []internal.DirectSessionTarget) error`.
Its `Removed %d stale direct session record(s).` line is written through the injected writer rather
than to process stdout, so a test asserts it without capturing a global; the rename-feature call site
passes `cmd.OutOrStdout()` and the three helper call sites pass `os.Stdout`. A `nil` writer defaults
to `os.Stdout`, so the seam cannot make a caller silent by omission.

Applied as:

| Command | `targets` | Behaviour |
|---|---|---|
| `tws rename feature <old> <new>` (external) | every `<branch-id>` under `<oldPath>/.sessions/`, via `ListDirectSessions`, each with `Want: nil` | refuse if any blocking; else remove stale, prune empty dirs, then proceed with the existing `BeginSpacesFeatureRename` → `os.Rename` flow unchanged |
| `tws rename branch <feature> <old> <new>` (external) | one target: `DirectSessionBranchID(feature, old)` with `Want: {feature, old}` | same; the old `<branch-id>` dir is empty and pruned before the rename, so no record survives to relocate |
| `tws archive <feature> <branch>` (external) | one target: `DirectSessionBranchID(feature, branch)` with `Want: {feature, branch}` | same — archive removes the worktree a live session's `path` points at, i.e. the agent's cwd |
| `tws delete <feature>` (external) | every `<branch-id>` under `<featurePath>/.sessions/`, each with `Want: nil` | refuse if any blocking; else proceed (`os.RemoveAll` takes `.sessions/` with the rest) |
| checkout `rename` / `archive` / `delete` | — | **unchanged**; checkout mode never writes or reads direct records |
| `tws migrate-layout` | — | **unchanged**; it moves only checkout legacy feature dirs, which never contain `.sessions/` |
| `tws sync`, `tws new`, `tws add`, `tws inject`, `tws push`, `tws export`, `tws import` | — | **unchanged** |

`blocking` deliberately includes `unknown` and `invalid` records here, unlike `close` (§11.2). The
distinction is principled and is stated in the code comment and the docs: **`close` is
non-destructive to identity** (it kills a tmux session and unlinks provably dead files), so leaving
an unverifiable record in place is harmless; **rename/archive/delete destroy or relocate the
identity**, so anything not provably dead must block. The refusal names every blocker with its
`record_id`, `owner_pid`, `stage`, and absolute file path, states that tws will not kill a direct
process, and points at `tws status <feature> --json` and at exiting the session. The refusal happens
**before** any Git command, any `os.Rename`, any worktree removal, and any spaces transaction commit,
and leaves the tree byte-identical.

`GuardFeatureName` already runs first in `renameBranchExternal` (`internal/cli/rename.go:196`),
`archiveExternal` (`internal/cli/archive.go:86`), and via `BeginSpacesFeatureDelete`/
`BeginSpacesFeatureRename`; the record guard is inserted **after** the name guard and **before** the
first mutation in each.

## 12. Corrupt state: reportable vs command-fatal

The dividing line is **operational state vs topology**.

**Reportable, exit 0** — a durable file describing a *runtime or transaction* that tws itself wrote,
which is stale, corrupt, or from a newer schema. Reporting it *is* the product:

- corrupt or unreadable `state/sessions/active.json` → `session-state-invalid` (workspace)
- `active.json` with `schema_version > 1` → `session-state-unsupported`
- orphan `checkout-session.lock`, missing lock, unreadable/invalid `owner.json`
- corrupt checkout transaction **or** corrupt lock → the single collapsed `sync-invalid` (§7.3)
- stale sync transaction, dead lock PID, failed stage
- corrupt/unsupported/token-mismatched/identity-mismatched direct record
- a `<branch-id>` directory matching no stack entry → `direct-record-orphan-branch` (feature scope)
- missing `tmux` binary; unusable tmux inventory
- missing ref, missing/prunable worktree, wrong branch, dirty tree
- unreadable individual worktree (`rev-parse` failure) → `worktree-unreadable`, that entry only
- `stack.yaml` present but unparseable → `stack-invalid`, that feature only, `entries: []`
- `stack.yaml` absent → `stack-missing` (`info`), that feature only, `entries: []`
- external `.sync-state.yaml` present but unparseable → `sync-state-invalid`, that feature only
- degraded workspace (§4.5)

**Command-fatal, exit 1** — the topology itself cannot be read, so any document would be a lie by
omission:

- workspace not resolvable at all
- **metadata root missing, not a directory, or unreadable** — checked explicitly before any listing
  (§4.4.1), because `ListFeaturesResolved` would otherwise return an empty, successful list
- `ListFeaturesResolved` error — untrusted, malformed, symlinked, or future-version `spaces.yaml`
  (`internal/resolve.go:139-147` already fails closed; status propagates it and emits **no**
  document, partial or otherwise)
- `ErrSpaceNameConflict` from the feature-name guard
- `ErrAmbiguousFeature` (a feature in both legacy and new checkout layouts) — fatal for a filtered
  run **and** for an unfiltered run, where it may be raised while resolving any feature's path (§4.3)
- `feature not found` for an explicit filter
- JSON encoding failure

The rule in one sentence: **a broken runtime record is data the dashboard exists to show; a broken
topology source means the dashboard does not know what exists, and it must say so loudly instead of
printing a confident partial answer.**

Note the specific hardening this requires over `buildSessionReport`: it must `os.Stat` the state path
**before** attempting a parse, because `LoadCheckoutAgentSession` collapses "missing" and
"unparseable" into one error, which today makes a corrupt `active.json` with no lock report as
*nothing at all*.

## 13. Implementation plan

### 13.1 New — `internal/agent_status.go` (package `internal`)

Must live in `internal`, not `internal/cli`: the builder needs `sessionStatePath`, `sessionLockDir`,
`sessionLockOwnerPath` (`internal/session.go:115-122`), `sessionDirty` (`:533`), `processAlive`
(`:263`), `gitRefExists`/`healthCurrentBranch`/`gitDirty`/`gitActiveOp`
(`internal/checkout_health.go`), and `canonicalize`/`metadataRootExists`
(`internal/workspace.go:205,334`) — all unexported. `internal/cli` stays a thin dispatcher.
`gitShortSHA`/`gitFullSHA`/`gitMergeBase` are **not** used: no ancestry is computed (§5.6).

Constants: `agentStatusSchema = 1`, plus one exported constant per issue code in §7.3 and nothing
else.

Types: `RuntimePresence`, `AgentState`, `AttentionStatus`, `AttentionRollup`, `AgentStatusIssue`,
`SessionObservation`, `SessionKind`, `EntryMaterialization`, `AgentStatusEntry`,
`AgentStatusFeatureSync`, `AgentStatusFeature`, `AgentStatusWorkspace`, `AgentStatusReport`,
`AgentStatusOpts`, `WorktreeInventory`, `TmuxSnapshot`, `TmuxPane`, `TmuxInventoryProbe`,
`RealTmuxInventory`.

Functions:

- `ResolveStatusWorkspace() (Workspace, string, error)` — §4.4/§4.5.
- `BuildAgentStatus(ws Workspace, degradedReason string, opts *AgentStatusOpts) (*AgentStatusReport, error)`
  — the whole builder; read-only, never mutates a lock, a record, or Git. Its **first** statements are
  the metadata-root `Stat` + `ReadDir` precondition of §4.4.1, whose errors are returned before any
  report object is allocated.
- `(*AgentStatusReport) FilterFeature(feature string) error` — §4.3, mirroring
  `CheckoutHealthReport.FilterFeature` (`internal/checkout_health.go:155-179`); it also recomputes
  `summary` and re-derives `workspace.attention` from the surviving features (§7.1), so a filtered
  document is internally consistent.
- `FormatAgentStatus(r *AgentStatusReport) string` — §8.8.
- `RollupAttention(presence RuntimePresence, own []AgentStatusIssue, childNeedsAttention bool) AttentionStatus`
  and `RollupPresence(...)` — single tested functions (§6.7, §7.1).
- `BuildWorktreeInventory(repoRoot string) WorktreeInventory` — §5.3.
- `NormalizeAgentStatus(r *AgentStatusReport)` — nil-slice → `[]` and sorting (§8.6), called once
  before encoding and before formatting.

`AgentStatusOpts{ Proc ProcessProber; Tmux TmuxInventoryProbe; Now func() time.Time }`, each
defaulted when nil, following `CheckoutHealthOpts`/`defaultOpts`
(`internal/checkout_health.go:185-195`). There is **no** `Ancestry` field (§5.6) and **no** `Cwd`
field (§5.5): nothing in the builder may observe the process working directory.

### 13.2 New — `internal/direct_session.go` (package `internal`)

Everything in §10.3–§10.5 plus `GuardDirectSessionsFor` / `RemoveStaleDirectSessions` (§11.3). In
`internal` because the status builder reads the records and `internal/cli` writes them.

### 13.3 New — `internal/tmux_names.go` (package `internal`)

`sanitizeExternalSessionName` (moved verbatim from `internal/cli/open.go:356-359`),
`ExternalTmuxSessionName(feature, name string) string`,
`ExternalFeatureTmuxSessionName(feature string) string`.

### 13.4 New — `internal/cli/status.go`

`statusCmd()`: the single `--json` flag, `ValidArgsFunction`, `ResolveStatusWorkspace`, the
`GuardFeatureName(ws.MetadataRoot, ...)` guard for a filtered run (§4.3), `BuildAgentStatus`,
`FilterFeature`, then either the encoder (§4.7) or `FormatAgentStatus`. Returns `nil` on success in
every reportable case (§4.6).

### 13.5 New — `internal/cli/direct_open.go`

`directOpenOpts`, `directRunner`, `directProcess`, `directSessionStore`, their real `exec.Cmd`- and
`internal`-backed implementations, and `runTrackedDirectOpen` implementing §10.6 steps 1–10.

### 13.6 Changed — existing files

| File | Change |
|---|---|
| `internal/cli/root.go:23-50` | register `statusCmd()` after `doctorCmd()` |
| `internal/cli/open.go` | `openDirect` new signature + error return + record protocol; `os.Exit(1)` removed; `sanitizeSessionName` delegates to `internal.ExternalTmuxSessionName`'s sanitizer; guard comments rewritten; call sites at `:68,:107,:181` propagate |
| `internal/cli/add.go:105` | pass feature/branch/featurePath context; propagate the error |
| `internal/cli/close.go` | guard (§11.1); comment rewritten; `runExternalClose` extraction and record-first ordering (§11.2); `TmuxSessionName` delegates to `internal.ExternalTmuxSessionName` |
| `internal/cli/rename.go` | record guard in `renameFeatureCmd` (external only) and `renameBranchExternal` (§11.3) |
| `internal/cli/archive.go` | record guard in `archiveExternal` (§11.3) |
| `internal/cli/delete.go` | record guard in `deleteExternal` (§11.3) |
| `internal/checkout_health.go` | add `ProcessLiveness`, `ProcessProber`, `realProcessChecker.Probe`; redefine `realProcessChecker.Alive` as `Probe(pid) == ProcessLive` |
| `internal/session.go` | extract `hashedSessionID(identity, prefix string) string`; `CheckoutAgentSessionName` delegates to it (byte-identical output) |

Reused, **not** modified: `internal.ProcessChecker`/`TmuxChecker`, `LoadCheckoutAgentSession`,
`BuildCheckoutList`, `LoadStack`, `Workspace.ListFeaturesResolved`, `GuardFeatureName`,
`IsPrunableWorktree` (left alone for its existing callers), `UnreadDecisions`,
`LoadCheckoutTransaction`, `ReadCheckoutLock`, `LoadSyncState`, `atomicSessionWrite`,
`sanitizeSessionPart`, `buildOneSyncReport`.

## 14. Dependencies

Already registered in `.tpatch/features/agent-work-status-dashboard/status.json` — **15 hard**:
`checkout-agent-sessions`, `checkout-doctor-observability`, `workspace-mode-foundation`,
`checkout-workspace-lifecycle`, `tmux-session-management`, `fix-external-feature-dir-resolution`,
`workspace-sibling-links`, `checkout-stack-safety`, `sync-continue`, `branch-name-decoupling`,
`worktree-health-check`, `fix-open-cwd-after-exit`, `tmux-free-mode`, `open-feature-dir`,
`quick-start-add-and-open`; and **5 soft**: `decision-read-tracking`, `workspace-registry`,
`list-features-branches`, `persist-agent-workflow-guidance`, `skill-distribution`.

This spec adds **no new edges** and removes none. `decision-read-tracking` correctly stays soft:
§7.5 reports `unread_decisions` as an inert integer that never feeds attention, so `status` compiles
and behaves correctly without it. Re-run `tpatch feature deps --validate-all` before implementation
per `.tpatch/steering/local.md`; no mutation is expected.

## 15. Documentation, skills, and metadata

All of these change in the same commit, because `assets/skills/**` is `go:embed`-compiled
(`assets/skills/embed.go`) and would otherwise ship stale.

1. `assets/skills/claude/tesseraworkspaces/SKILL.md` — add
   `| tws status [feature] [--json] | Agent work status per branch |` to the command table (~line
   32-47) and a short section after the checkout doctor/list sections (~239-260) covering the two
   axes, `needs_attention` being authoritative, exit 0 on attention, and the PID caveat.
2. `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` — add
   `tws status --json` to the "View state" block (~18-22) and make it step 0 of the orchestration
   workflow (~72-95): poll `tws status --json`, act on `attention.status == "needs_attention"` and
   `issues[]`, never on `agent_state`.
3. `assets/skills/copilot/tws.prompt.md` — the command list (~23-36) and the workflow (~117-123).
4. All three skills carry the same verbatim caveats: *"a `present` from tws means a process with that
   PID exists, not that that exact process exists"*, *"`agent_state` is always `unknown` at this
   version; use `needs_attention`"*, and *"`attention.status` inherits upward: a workspace or feature
   can be `needs_attention` with `issue_count: 0` because a child is — read `report.issues[]` for the
   detail"*.
5. `README.md` (~130-144) — one command-table row.
6. `docs/cheatsheet.md` — a `## Check agent status` block beside "See what you have" (~107-131),
   showing `tws status`, `tws status auth`, and
   `tws status --json | jq '.issues[] | select(.severity=="warning")'`.
7. `docs/roadmap.md` — move "agent work status" from *Now / current target* to shipped foundations,
   and record the explicit follow-ups: `tss-agent-state-provider`, portable process birth identity,
   and **base ancestry**, which stays with the existing P1 "Stack ancestry doctor" and "Stack status"
   items rather than being duplicated in `tws status` (§5.6). State that "blocked (needs
   approval/input)" is deferred to the provider, not dropped.
8. `docs/engineering-workflow.md` — update "Next roadmap feature" (~line 26) to the next target.
9. `CHANGELOG.md` — a new version section covering: the new command; the two-axis schema and
   `schema_version: 1`; `agent_state` always `unknown`; hierarchical `attention` with own-scope
   `issue_count`/`codes`; exit 0 on attention (contrast with `doctor`); external direct records;
   **the external `tws close` ordering change with its compatibility matrix (§11.2)**; the new
   `close` feature-name guard **and its new hard failure on malformed spaces metadata** (§11.1); the
   refuse-live behaviour of external `rename`/`archive`/`delete`; and `openDirect` no longer calling
   `os.Exit`.

## 16. Acceptance criteria

Runnable against a real temporary workspace unless stated. `make build` first; `tws` means
`./bin/tws`.

**Command surface**

1. `./bin/tws status --help` shows exactly one flag, `--json` (plus inherited `-h`), and its `Long`
   text contains `always covers every feature` and `agent_state`. `./bin/tws status --ancestry`
   exits 1 with `unknown flag: --ancestry`, and `git grep -n 'ancestry' internal/agent_status.go
   internal/cli/status.go` returns nothing.
2. `./bin/tws --help` lists `status`.
3. `./bin/tws status nosuch` exits 1 with `feature not found: nosuch` and prints nothing to stdout.
4. `./bin/tws status --json | jq -e '.schema_version == 1'` succeeds in an empty external workspace,
   and `jq -e '.features == [] and .issues == [] and .summary.entries == 0'` succeeds.
5. Human `tws status` in an empty workspace prints the header plus
   `No features found. Use 'tws add <feature>' to create one.` and exits 0.
6. With a feature `auth` (branches `a`, `b`), `tws status` and `tws status auth` both exit 0;
   `tws status auth --json | jq -e '.features | length == 1'` succeeds.
7. `tws status --json` produces a **byte-identical** `.features` array from all five cwds of §4.4
   (repo root, linked worktree root, nested worktree dir, external workspace root, external feature
   dir): serialize `.features` with `jq -cS .features` from each location and compare the bytes
   directly. The whole documents are compared too, after removing `.generated_at` only. No key
   anywhere in the document is derived from cwd (§5.5), so a difference here is a bug, not a
   tolerated variance.
8. With `spaces.yaml` registering a space owning directory `learning`, `tws status learning` exits 1
   with the canonical `ErrSpaceNameConflict` message, and a recursive tree snapshot of the workspace
   root is byte-identical before and after (no `.sessions/`, no lock, no temp file).
9. With a malformed `spaces.yaml`, `tws status` and `tws status auth` both exit 1 and print **no**
   JSON to stdout.

**Two axes and rollup**

10. `tws status --json | jq -e '[.. | objects | select(has("agent_state")) | .agent_state] | unique == ["unknown"]'`
    succeeds for a fully populated fixture.
11. `grep -R '"working"\|"ready"\|"blocked"\|"done"' internal/agent_status.go` matches only the
    `AgentState` constant declarations — no producer.
12. With a stale checkout sync transaction for `auth` (and no other fault),
    `jq -e '.features[0].attention.status == "needs_attention"'` succeeds **and**
    `jq -e '[.features[0].entries[].attention.status] | index("needs_attention") == null'` succeeds,
    while `jq -e 'all(.features[0].entries[]; .feature_attention == true)'` succeeds, and
    `jq -e '.workspace.attention.status == "needs_attention" and .workspace.attention.issue_count == 0 and .workspace.attention.codes == []'`
    succeeds — the workspace inherits from the feature without owning an issue (§7.1).
13. With an orphan `checkout-session.lock` and no `active.json`,
    `jq -e '.issues[] | select(.code=="session-orphan-lock") | .scope == "workspace" and .feature == null and .name == null'`
    succeeds, **no entry and no feature** has `needs_attention` (the issue is workspace-owned and
    never smears down), and `.workspace.attention.issue_count == 1`.
14. `jq -e '.workspace.attention.status == "needs_attention"'` succeeds whenever any feature or entry
    does; exit status is still 0.

**Identity and materialization**

15. For a decoupled entry (`name: api`, `branch: jd/api`), `jq -e '.features[0].entries[0] | .name=="api" and .git_branch=="jd/api"'`
    succeeds, and the per-branch tmux name probed is `auth-api` (built from `name`), asserted in the
    unit test through the fake tmux inventory.
16. External: after `rm -rf <feature>/worktrees/api` on a non-archived entry,
    `materialization.state` is `prunable-missing` (with a real prunable Git entry) or `missing`, and
    the entry has `needs_attention`. Run from the **external workspace root**, which is exactly where
    `internal.IsPrunableWorktree` fails today.
17. External: `tws archive auth api` then `tws status --json` reports `materialization.state ==
    "archived"` with `attention.status == "idle"` and no issue.
18. Checkout: `git branch -D jd/api` then `tws status --json` reports
    `materialization.kind == "ref"`, `state == "missing"`, `path == null`, and entry
    `needs_attention`.
19. No ancestry survives anywhere: `tws status --json | jq -e '[.. | objects | has("ancestry")] | any | not'`
    succeeds, `git grep -rn 'ancestry' internal/agent_status.go internal/agent_status_test.go
    internal/cli/status.go internal/cli/status_test.go` returns nothing, and no issue code in
    `report.issues[]` matches `^ancestry-`.

**JSON contract**

20. `tws status --json | jq -e '.features[0].entries[0] | has("repo") and has("is_current_checkout") and has("unread_decisions") and has("feature_attention") and (has("ancestry") | not) and (has("current") | not)'`
    succeeds even when every value is null/zero. In external mode
    `jq -e '.features[0].entries[0].is_current_checkout == null'` succeeds; in checkout mode it is
    `true` for the checked-out branch and `false` for the others.
21. `tws status --json | grep -c 'null'` is > 0 for a sparse fixture, and
    `jq -e '[.. | arrays] | all(. != null)'` succeeds (no null arrays anywhere).
22. Output is two-space indented and ends with a newline; parsing with `jq` succeeds.
23. Two consecutive `tws status --json` runs with no intervening change differ only in
    `generated_at`.
24. `jq -r '.generated_at'` matches `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`.
25. A record whose stored `started_at` is the literal `not-a-time` is emitted verbatim as
    `not-a-time`, not normalized and not dropped.

**Redaction**

26. Seed `active.json` with `lock_token: SECRETLOCKTOKEN` and `owner.json` with
    `token: SECRETOWNERTOKEN`; `tws status --json` and human `tws status` contain **neither** string,
    asserted by substring scan on the raw bytes.
27. `jq -e '.workspace.checkout_session | has("lock_token") | not'` succeeds, and no key anywhere in
    the document is named `lock_token` or `token`.
28. A direct record with token `abcdef0123456789abcdef0123456789` appears as
    `record_id == "abcdef01"` and the full token appears nowhere.
29. No `sessions[]` object has a key named `links`, `env`, `command`, `argv`, or `prompt`.

**Direct records**

30. `tws open auth api` (with a fake runner, in-test) creates exactly one file matching
    `<feature>/.sessions/<branch-id>/[0-9a-f]{32}.json` with mode `& 0077 == 0`, and both parent
    directories with mode `& 0077 == 0`.
31. The `starting` record exists on disk **before** `Start` is called; `stage == "agent"` with a
    `child_pid` is on disk **before** `Wait` returns; `stage == "shell"` is on disk **before** the
    shell is started; the file is gone after normal exit and both parent dirs are pruned.
32. Two simulated concurrent opens on the same branch produce two distinct files under one
    `<branch-id>`; neither overwrote the other; `ListDirectSessions` returns both; `status` shows
    `session_counts.total == 2`.
33. With one live and one dead record, the entry is `runtime_presence: "present"` **and**
    `attention.status: "needs_attention"` with code `direct-record-stale`.
34. `tws status` never removes a record: the `.sessions/` tree is byte-identical before and after,
    including for provably dead records.
35. An artificially old (`started_at` one year ago) but **live** record still reads `present`; no
    mtime comparison exists in the code (`grep -R 'ModTime' internal/direct_session.go internal/agent_status.go` is empty).
36. `LookPath` failure → error returned (not `os.Exit`), exit 1 through `Execute()`, and **no**
    `.sessions/` directory is created.
37. `Start` failure → own record removed, sibling record untouched, error returned.
38. Post-start agent-stage `Update` failure → the fake process records both `Terminate` **and**
    `Wait`, own record removed, joined error returned, no surviving child.
39. Pre-start shell-stage `Update` failure with a **non-`ENOENT`** error → shell never started, own
    record removed, error returned.
40. Post-start shell-stage `Update` failure → shell **not** terminated, warning on stderr,
    `openDirect` returns nil, record still on disk with `stage == "shell"` and a live `owner_pid`.
41. `tws open auth --feature-dir` and checkout `tws open auth --feature-dir` create **no** file under
    any `.sessions/` path, yet still propagate a `LookPath` failure as an error.
42. A record whose `feature`/`name` disagree with the requested identity is reported as
    `record_state: "invalid"` and never merged into another branch — with `want != nil`. The same
    directory loaded with `want == nil` returns the record as `State: "ok"` (identity matching
    skipped, §10.4).
43. A record file named `deadbeef.json` (not 32 hex chars) and a `.tmp-session-xyz` file are both
    ignored by `LoadDirectSessions`.

**`tws close`**

44. Live record + tmux present → non-zero exit; the message names the live `owner_pid`; the fake tmux
    ops record **no** `Kill`; every record file (live and stale sibling) is byte-identical after.
45. Stale records + tmux present → stale files gone, `Kill` invoked once with the expected name,
    `Closed tmux session:` present.
46. Stale records + no tmux → exit 0, stale files gone, `<branch-id>/` and `.sessions/` pruned,
    cleanup message present, and `no tmux session found` **absent** from the output.
47. No records + no tmux → exact existing error string `no tmux session found for auth/api`,
    non-zero.
48. No records + tmux present → stdout and exit status byte-for-byte identical to the pre-feature
    baseline (golden-string control).
49. `tws close <registered-space> api` exits 1 with `ErrSpaceNameConflict`; a pre-seeded
    `.sessions/<branch-id>/<token>.json` inside the space directory is byte-identical afterwards; the
    fake tmux ops recorded no call at all.
50. An `invalid` record neither blocks the tmux kill nor is removed.

**Rename / archive / delete**

51. `tws rename feature auth auth2` with one live record exits non-zero, names the blocker's file
    path and `owner_pid`, and leaves the tree byte-identical (no `os.Rename` occurred).
52. The same with only provably stale records: the stale files are removed, the dirs pruned, and the
    rename then succeeds.
53. The same with no records at all: output and effects byte-for-byte identical to pre-feature.
54. `tws rename branch auth api api2` and `tws archive auth api` and `tws delete auth` each refuse
    while a live record exists for the affected identity, before any Git command runs (asserted by
    the branch still existing and the worktree still present).
55. Checkout-mode `rename`/`archive`/`delete` are byte-for-byte unchanged and never stat a
    `.sessions/` path (asserted with a fake store that fails if called).

**Corrupt state and exit codes**

56. `printf 'not json' > .tws/state/sessions/active.json` with **no** lock: `tws status` exits **0**
    and reports `session-state-invalid` at workspace scope. (Today `tws doctor` reports nothing at
    all for this case.)
57. The same with a lock present: still exit 0, still `session-state-invalid` — not
    `session-orphan-lock`.
58. A corrupt `stack.yaml` for `auth`: exit 0, `features[0].stack_state == "invalid"`,
    `entries == []`, feature `needs_attention`, and `billing`'s entries still fully reported.
59. `tws status` with a corrupt checkout transaction exits 0 while `tws doctor` on the same fixture
    still exits non-zero — the deliberate divergence.
60. tmux absent **with Git still present**: build a shim directory containing only a `git` symlink to
    the real `git` and run `PATH=<shim> tws status --json`. (A bare `PATH=/nonexistent` would also
    remove `git` and would silently test the degraded-workspace path instead of the tmux path.) It
    exits 0, `workspace.tmux.available == false`, `workspace.repo_root` is still non-null, **no**
    session observation of kind `external-tmux` is emitted at all (§6.4 rule 1), every entry keeps
    the presence its records imply, and `tmux-missing` is an `info` workspace issue — unless a
    `Mode=tmux` checkout record exists, in which case `tmux-unverifiable` is a `warning` and the
    workspace reads `needs_attention`.
61. External workspace root whose repo cannot be inferred: exit 0,
    `workspace.degraded == true`, `repo_root == null`, `stable_id == null`, and no `git` command was
    executed with an empty `-C` (asserted by a `PATH`-shim `git` that fails the test if invoked).

**Compatibility**

62. In a workspace with no `.sessions/` anywhere, `tws list`, `tws doctor`, `tws stack`, `tws open`,
    and `tws close` produce byte-identical output to the pre-feature binary (golden files).
63. `tws open auth api` prints the same `Opening:`, `Running:`, `Agent exited:`, and
    `Dropped into shell at:` lines as before, in the same order.
64. `CheckoutAgentSessionName("ws","feat","name")` returns the exact pre-refactor golden string.
65. `git grep -n 'os.Exit' internal/cli/open.go` returns nothing for the `openDirect` path.
66. `.sessions/` never appears in `tws export` output, and `tws list` never lists it as a feature.

**Gates**

67. `gofmt -l internal internal/cli` is empty; `go vet ./...`, `golangci-lint run ./...`,
    `go test ./... -count=1`, and `make build` all pass.
68. `tpatch feature deps --validate-all` passes with the dependency set unchanged.

**Hierarchical attention, issue codes, and preconditions**

69. One stale direct record on `auth/api` and nothing else wrong:
    `jq -e '.features[0].entries[] | select(.name=="api") | .attention.status == "needs_attention" and .attention.issue_count == 1 and .attention.codes == ["direct-record-stale"]'`,
    `jq -e '.features[0].attention.status == "needs_attention" and .features[0].attention.issue_count == 0 and .features[0].attention.codes == []'`, and
    `jq -e '.workspace.attention.status == "needs_attention" and .workspace.attention.issue_count == 0 and .workspace.attention.codes == []'`
    all succeed, and `jq -e '[.issues[] | select(.code=="direct-record-stale")] | length == 1'`
    succeeds (single home). Sibling entries of the same feature stay `idle`/`active`.
70. `RollupAttention` unit table: `(absent, [], false) == idle`; `(present, [], false) == active`;
    `(present, [info], false) == active`; `(present, [warning], false) == needs_attention`;
    `(absent, [], true) == needs_attention`; `(present, [], true) == needs_attention`.
71. Every issue code emitted by a fully populated fixture is a member of the §7.3 table, and the set
    of exported issue-code constants in `internal/agent_status.go` equals the table exactly
    (enumerated in the test, no reflection over strings). `sync-lock-invalid`,
    `feature-tmux-unknown`, and any `ancestry-*` code are absent from the binary
    (`git grep -n 'sync-lock-invalid\|feature-tmux-unknown\|ancestry-' internal` returns nothing).
72. Presence/attention invariant on a fixture containing one dead record, one EPERM record, one
    unverified tmux name, and one healthy branch:
    `jq -e '[.. | objects | select(.runtime_presence == "stale" or .runtime_presence == "unknown") | .attention.status] | unique == ["needs_attention"]'`
    succeeds, and `summary.needs_attention + summary.active + summary.idle == summary.entries`.
73. Metadata-root precondition: with the workspace resolvable but the metadata root removed
    (`rm -rf <repo>.tws` after resolution, or `chmod 000` on it), `tws status` and
    `tws status --json` both exit **1**, print nothing to stdout, and print
    `workspace metadata root unreadable` on stderr — they do **not** print an empty report. A second
    control asserts an *empty but readable* metadata root still exits 0 with `features == []`.
74. Unfiltered ambiguity: a checkout workspace with both `.tws/auth/` and `.tws/features/auth/`
    makes plain `tws status` exit 1 with the `ErrAmbiguousFeature` message and print no JSON, exactly
    as `tws status auth` does.
75. `tws close` with malformed `spaces.yaml` (via `malformedSpacesFixtures()`) exits non-zero, the
    fake tmux ops record no call, and a pre-seeded record file is byte-identical afterwards.
76. Shell-transition record loss: a `directSessionStore` fake whose shell-stage `Update` returns
    `fs.ErrNotExist` → the shell **is** started, a `recreating` warning is on stderr, `openDirect`
    returns nil, and a record with `stage == "shell"` and the original `started_at` exists on disk
    under a new token; a second fake whose `Create` also fails leaves no record, still starts the
    shell, and still returns nil.
77. Checkout mode never touches direct records: with a hand-planted
    `.tws/features/auth/.sessions/<id>/<token>.json`, `tws status --json` in checkout mode emits no
    session observation for it, no `direct-record-*` issue, and no `direct-record-orphan-branch`;
    a filesystem-call-recording fake asserts no path containing `.sessions` was ever opened.
78. An orphan `<branch-id>` directory under an external feature (branch renamed away) produces
    exactly one feature-scoped `warning` `direct-record-orphan-branch`, attaches its records to no
    entry, makes the feature and workspace `needs_attention`, and leaves the directory on disk.

## 17. Test matrix

Real temporary Git repositories via the existing helpers `setupGitRepoCheckout`, `gitInDir`,
`requireWorkspaceForTest` (`internal/cli/checkout_lifecycle_test.go:19-77`) and the external
`setupGitRepo`/`withUnifiedWorkspaceEnv` helpers used by `internal/cli/space_guard_test.go`. **No
real `tmux`, no real agent, no real spawned process in CI** — every runtime fact goes through the
injected `ProcessProber`, `TmuxInventoryProbe`, `directRunner`, and `directSessionStore`.

New test files: `internal/agent_status_test.go`, `internal/direct_session_test.go`,
`internal/cli/status_test.go`, `internal/cli/direct_open_test.go`,
`internal/cli/close_records_test.go`.

1. **External topology**: active / archived / prunable-missing / missing worktrees; decoupled
   `Branch != Name`; wrong-branch detection via `GitBranch()`; dirty worktree as `info` and as
   `worktree-dirty-blocking` when `.sync-state.yaml` names it.
2. **Checkout topology**: ref present / missing / missing-and-archived; `is_current_checkout` from
   HEAD and from the session record, and `null` when the branch is unknowable; external mode always
   `null`; cross-repo entry short-circuit.
3. **Checkout session matrix** — every row of §6.5, with the stat-vs-parse differentiation asserted
   explicitly: `active.json` absent (clean "no session") versus present-but-unparseable
   (`session-state-invalid`).
4. **Sync interaction**: checkout transaction at `conflict` with a dead lock PID → feature
   `needs_attention`; external `.sync-state.yaml` with `failed_branch` → feature-level issue plus an
   entry-level `sync-failed-branch` on the named branch only.
5. **Direct record lifecycle** — criteria 30–43, driven entirely through the seams, with a
   call-order-recording fake runner as the assertion vehicle for step ordering.
6. **Token-owned cleanup**: after two concurrent records, the first owner's cleanup removes only its
   own file and leaves the sibling; the branch dir survives (`ENOTEMPTY` tolerated); after the second
   cleans up, both dirs are pruned; cleanup with a non-matching token is a no-op.
7. **Path safety**: `<branch-id>` for a `Name` containing `/`; two distinct identities whose
   sanitized prefixes collide get distinct hash suffixes and distinct directories.
8. **`close`** — criteria 44–50, including the row-4 byte-for-byte control and the guard regression
   added to `internal/cli/space_guard_test.go:425-444`.
9. **Rename/archive/delete** — criteria 51–55.
10. **Rollup scoping** — criteria 12–14 and 69–72: a feature-level failure must not set any entry's
    own attention; an entry-level failure **must** set its feature's and the workspace's status while
    leaving their `issue_count`/`codes` at own-scope zero; a workspace-level orphan lock must attach
    to no branch; `RollupAttention` is unit-tested against its full truth table.
11. **JSON contract** — stable key set at every level in both modes for empty and populated
    workspaces; `schema_version` present; `agent_state == "unknown"` everywhere; no `ancestry` and no
    `current` key anywhere; `needs_attention` produced by a real structural fixture; secret-absence
    scans (criteria 26–29).
12. **Workspace-resolution matrix** — all five cwds (§4.4) in both modes with byte-identical
    `features[]` (criterion 7), the degraded external fallback where `RepoRoot` and `StableID` are
    both empty, and the fatal metadata-root precondition (criterion 73).
13. **External regression** — `tws list`, `tws doctor`, `tws open`, `tws close` (compatible rows
    only), and worktree/Git behaviour unchanged; golden files.
14. **`openDirect` error propagation** — each of the four call sites returns to `RunE`, and
    `Execute()` maps it to exit 1, asserted with no `os.Exit` in the path.
15. **Loader identity** — `LoadDirectSessions` with a non-nil `want` flags a mismatched record
    `invalid`; the same directory with `want == nil` returns it `ok`; `ListDirectSessions` uses
    `nil`; an orphan `<branch-id>` becomes exactly one `direct-record-orphan-branch` (criteria 42,
    78).
16. **Issue-code closure** — the exported code constants equal the §7.3 table, and the removed codes
    (`sync-lock-invalid`, `feature-tmux-unknown`, `ancestry-*`) exist nowhere (criterion 71).

### 17.0 Contracted tests added by the expert-review revision

These are additional to the numbered criteria and are named so a reviewer can find them.

| Area | Test | Asserts |
|---|---|---|
| human output | `TestFormatAgentStatusWorkspaceOnlyAttention` | the header always carries `Attention: <glyph> <status>`; a workspace-only warning leaves every row `idle` and the tail at `0 needs attention` |
| human output | `TestFormatAgentStatusFeatureInheritedAttention` | a feature-only warning renders in its `Feature:` block, leaves entries `idle` with `feature_attention: true`, and still reaches the header verdict |
| human output | `TestFormatAgentStatusRendersEveryEntryIssue` | every entry-scoped issue appears under `Branch: <feature>/<name>` with code, message, **and guidance**; no needs-attention row lacks a block |
| human output | `TestFormatAgentStatusSeparatesSessionSummaries` | two session summaries in `DETAIL` are separated by `"; "` |
| human output | `TestFormatAgentStatusKeepsOneIssuePerLine` | a multi-line parser error is folded onto one line in the human view only |
| orphan dirs | `TestAgentStatusEmptyOrphanDirectoryIsSilent` | an empty `<branch-id>` emits no `direct-record-orphan-branch`; the same directory with one record still does |
| records | `TestAgentStatusUnreadableRecordRootIsReported` | a failed `ListDirectSessions` becomes one feature-scoped `direct-record-dir-unreadable` |
| checkout session | `TestAgentStatusCheckoutUntrustworthyRecordStaysWorkspaceOnly` | unparseable, unsupported-schema, and unverifiable-tmux records are never attributed to an entry, emit no entry-scoped issue, and leave the entry `absent`/`idle` while the workspace is `unknown`/`needs_attention` |
| checkout session | `TestAgentStatusCheckoutEmptyIdentityIsUnattributed` | a parsed record with an empty feature or name yields workspace `session-unattributed` |
| checkout session | `TestAgentStatusCheckoutPresenceInvariant` | the checkout twin of the `stale\|unknown ⇒ needs_attention` invariant, walking workspace, features, and entries, with summary counters summing to `entries`, over **real** Git refs so `ref-missing` cannot mask the verdict |
| materialization | `TestAgentStatusExternalWorktreeEdgeCases` | worktree path that is not a directory; a present directory whose `rev-parse` fails (no fabricated `dirty: false`); a detached worktree (`checked_out_branch: null`, no `worktree-wrong-branch`) |
| JSON | `TestAgentStatusJSONKeySetSnapshots` | exact key set at **every** level for external-populated, checkout-populated, and empty documents, including `tmux`, `attention`, `summary`, `session_counts`, `sync`, `feature_tmux`, sessions, and issues |
| JSON | `TestStatusIsCwdIndependent` | the **whole** document, not merely `features[]`, is byte-identical from all five cwds after removing only `generated_at` |
| close | `TestExternalCloseReportsUnverifiableRecords` | invalid and EPERM records are reported before tmux handling and never removed; with no tmux and nothing removed the error names the remaining records and `tws status --json`; the report leaks no full token |
| open | `TestDirectOpenAgentStageUpdateFailureTerminatesChild` | the fake process records `Terminate` **then** `Wait`, exactly once |
| open | `TestDirectOpenShellStageRecordLossRecreates` | the recreate preserves `started_at` and records `stage: shell` under a **new** token, with identity and `owner_pid` intact |
| open | `TestRealDirectProcessTerminateThenWait` / `TestRealDirectProcessWaitReleasesEscalation` | the real process reaps once, `Wait` is idempotent, and a successful `Wait` releases the bounded SIGKILL escalation |

### 17.1 Failure injection

| Injection | Vehicle | Asserts |
|---|---|---|
| dead / live / EPERM PID | fake `ProcessProber` returning `dead`/`live`/`unknown` | §6.3 mapping, `close` refusal, rename blocking |
| tmux missing / no server / inventory error / panes unavailable / path mismatch | fake `TmuxInventoryProbe` returning each `TmuxSnapshot` shape | §6.4 table, including "no evidence ⇒ no observation" |
| record create/update/remove failure | `directSessionStore` fake failing at a chosen step, and one returning `fs.ErrNotExist` at the shell stage | §10.6 steps 2, 5, 7 (both branches), 9, 10 |
| agent/shell `Start` failure, non-zero `Wait` | fake `directRunner` | steps 4, 6, 8 |
| `Terminate`/`Wait` observation | fake `directProcess` recording calls | step 5 asserts both |
| corrupt `active.json`, `owner.json`, transaction, lock, record | seeded byte fixtures | §12 reportable set |
| future `schema_version` | seeded `schema_version: 99` in `active.json` and in a record | `*-unsupported` codes |
| untrusted `spaces.yaml` | existing `malformedSpacesFixtures()` (`internal/cli/space_guard_test.go`) | `status` exit 1 with no stdout; `close` exit non-zero with no side effect (criterion 75) |
| unreadable metadata root | removed or `chmod 000` metadata root | criterion 73: exit 1, no document |
| Git absent / failing | `PATH` shim `git` that fails or records invocations | degraded workspace, no empty `-C` |
| tmux absent with Git present | `PATH` shim containing only a `git` symlink | criterion 60 |

## 18. Follow-up child features (explicitly not this feature)

- **`tss-agent-state-provider`** — invoke the versioned `tss` CLI JSON provider to populate
  `agent_state` and to cover runtimes tws did not launch. Owns provider discovery, timeout, version
  negotiation, and degradation to `unknown` on any failure. This feature ships the stable two-axis
  schema and `schema_version` that make it purely additive.
- **`fix-list-wrong-branch-check`** — fix `internal/cli/list.go:81` passing `entry.Name` instead of
  `entry.GitBranch()` to `CheckWorktreeBranch`.
- **Base ancestry per branch** — deliberately deferred to the existing P1 roadmap items "Stack
  ancestry doctor" and "Stack status" (`docs/roadmap.md`), which own the `current | stale |
  divergent | missing` semantics for the whole tool. If `tws status` ever surfaces ancestry it must
  consume that feature's projection rather than compute its own; adding an `ancestry` key is
  additive and does not bump `schema_version` (§5.6, §8.1).
- **Portable process birth identity** — record and verify a process start time or a birth-stable
  handle (`/proc/<pid>/stat` field 22, `kern.proc`/`KERN_PROC_PID` on Darwin, `pidfd`) to close the
  PID-reuse false-positive window for the checkout session record, the checkout-sync lock, and the
  external direct records alike.
- **`--json` for `doctor` and `list`** — the structs are already tagged.
- **Termination semantics for live direct sessions**, if ever wanted. Not reserved here.
