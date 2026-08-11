# Analysis

## Summary

`tws` can already answer "what worktrees/branches exist and is a recorded session alive?" but only
in fragments: `tws list` prints a per-branch `[tmux]` tag in external mode and a `[session]` tag in
checkout mode, and `tws doctor` builds a rich, JSON-tagged `CheckoutHealthReport` that is never
emitted as JSON. There is no `status` command, no `--json` on `doctor`/`list`, and no single
machine-readable per-branch view an orchestrator can poll. This feature adds `tws status [feature]
--json` as a **mostly read-only projection over data tws already owns** — stack topology,
worktree/branch materialization, sync transaction state, and the checkout session record — plus one
narrow additive write: hidden per-invocation runtime records for external `openDirect`, the default
external open path, which today leaves no trace at all. It is net-new surface, not a re-implementation, and it is
viable — but only if the baseline is honest about which axis it can answer. Nothing in tws observes
*semantic* agent state, so the request's `blocked (needs approval/input)` is not derivable and
`agent_state` must read `unknown` without a provider. What tws *can* answer authoritatively is
`needs_attention`, from structural signals it owns end to end.

Not present upstream: `git grep` finds no `statusCmd`, no `status` entry in `rootCmd.AddCommand`
(`internal/cli/root.go:23-50`), and no `runtime_presence`/`agent_state` symbol. No language,
framework, or license blocker: Go 1.26.1 with `spf13/cobra` and `gopkg.in/yaml.v3` already direct
dependencies (`go.mod`), and `encoding/json` is already used for CLI output in
`internal/cli/registry.go` and `internal/cli/space.go`.

## Agreed inter-tool boundary (recorded, not implemented here)

Per the tesserasessions agreement, this feature is **baseline only**:

- `tws` is authoritative for worktree/logical-branch topology and for the sessions it launches.
- A future `tss` integration is an *optional, versioned CLI JSON provider* for semantic agent state
  and externally launched runtimes. No SQLite access, no Go package dependency, no build-time link.
- Two separate axes, never collapsed:
  `runtime_presence = present|absent|stale|unknown` and
  `agent_state = working|ready|blocked|done|unknown`.
- tws rollup precedence: `needs_attention` → `active` → `idle`. tss `ready` is a semantic
  "agent finished its turn and is awaiting input" and must **not** be mapped to tws `idle`.
- This feature must not invoke, shell out to, or depend on `tss`. **tss provider integration is
  explicitly out of scope here** and must be a later child feature (suggested slug
  `tss-agent-state-provider`) that adds the provider invocation, its versioned contract, its
  timeout/failure degradation, and the `agent_state` population — with this feature's
  `schema_version` and stable enums as its extension point.

### What the baseline rollup can actually produce

The earlier claim that "at baseline the rollup can only ever produce `active` or `idle`" is **false**
and is corrected here. Only the *semantic* axis is dark:

- `agent_state` stays `unknown` for every entry until a provider exists. Baseline never emits
  `working`, `ready`, `blocked`, or `done`.
- `needs_attention` is **authoritative at baseline**, derived entirely from tws-owned structural
  signals, and is expected to fire in real workspaces on day one. It is not a placeholder.

Emitting `agent_state` now, with a stable enum and an `unknown` value, is what makes the later
provider additive rather than breaking.

Structural `needs_attention` inputs tws owns end to end, each with a real code source:

| Signal | Source | Scope |
| --- | --- | --- |
| checkout sync transaction stale (`.yaml` with no `.lock`) | `buildOneSyncReport` (`internal/checkout_health.go:351-422`) | feature |
| checkout sync transaction failed (`FailureKind`/`FailureMsg`, stage `conflict`/`restoring`) | `CheckoutTransaction` (`internal/checkout_sync.go:62-88`) | feature, with `CurrentBranch` attributable to one branch |
| checkout sync lock dead (`LockInfo.PID` not alive) | `ReadCheckoutLock` + `ProcessChecker` | feature |
| checkout sync lock/transaction corrupt (unparseable YAML, `PID <= 0`) | same | feature |
| checkout session state present but lock missing | `buildSessionReport` mismatch branch (`internal/checkout_health.go:481-489`) | workspace (single-owner record) |
| checkout session lock present but no state (orphan lock) | `buildSessionReport` (`internal/checkout_health.go:426-441`) | workspace |
| checkout direct session owner PID dead | `processAlive(state.PID)` | workspace, attributed to `state.Feature`/`state.Name` |
| checkout tmux session recorded but `has-session` false | `TmuxChecker` | workspace, attributed to `state.Feature`/`state.Name` |
| one or more external direct records for a branch whose owner PID is dead (new records, see below) | new hidden per-invocation runtime records + `ProcessChecker` | branch |
| missing tmux binary while a tmux-mode record exists | one-shot `LookPath` probe | workspace-level degradation, see below |
| logical branch missing its Git ref (checkout) / worktree missing and prunable (external) | `gitRefExists` (`internal/checkout_health.go:617`), dir presence + `git worktree list --porcelain` | branch |
| dirty tree blocking restore **where observable** | `sessionDirty` (`internal/session.go:533`) for the checkout repo; `CheckWorktreeDirty` (`internal/health.go:48`) per external worktree | checkout: workspace; external: branch |
| external `.sync-state.yaml` with a non-empty `FailedBranch` | `LoadSyncState` (`internal/syncstate.go:23`) | feature, attributable to the named branch |

"Where observable" is a real qualifier for the dirty-tree signal: in checkout mode there is exactly
one physical checkout, so `sessionDirty(ws.RepoRoot)` is only meaningful for the branch that is
currently checked out and only blocks `restoreCheckoutSession` (`internal/session.go:704-712`) for
the recorded session. It must not be reported per stack entry. In external mode dirtiness is per
worktree and is genuinely branch-local, but it does not block a restore (there is nothing to
restore) — it is at most an `info` unless a sync transaction wants that worktree.

### Branch-local vs feature/workspace-level attention

The rollup must be computed at three levels and the JSON must say which level produced it, because
these signals do not share a scope:

- **Branch-local** (`entries[].attention`): missing ref / prunable-missing worktree, wrong branch
  checked out in an external worktree, external worktree dirty, any dead external direct record for
  that branch, and the checkout session signals *when* `state.Feature`/`state.Name` match that entry.
- **Feature-level** (`features[].attention`): checkout sync transaction stale/failed/corrupt for
  that feature, external `.sync-state.yaml` `FailedBranch` for that feature, and the feature-level
  `--all` tmux session. A feature-level attention must **not** be silently smeared onto every child
  entry; entries inherit a `feature_attention` reference, not the flag itself.
- **Workspace-level** (`workspace.attention`): checkout session record/lock mismatch, orphan session
  lock, corrupt `active.json`, and tmux-binary absence degradation. The checkout session record is
  single-owner per workspace (`state/sessions/active.json`), so an orphan lock has no branch to
  attach to at all — forcing it onto a branch would be a lie.

A feature rolls up to `needs_attention` if any of its own feature-level signals fire or any child
entry is `needs_attention`; the workspace rolls up the same way. `active` requires a live tws-owned
runtime record; `idle` is the residue and must never be produced from a semantic signal.

## Current behaviour, per real code path

### Stack entries and materialization

- `internal.Stack`/`StackEntry` (`internal/stack.go:13-33`) is the topology source of truth:
  `Name` (tws identity), `Branch`/`GitBranch()` (Git identity), `Base`, `Archived`, `Repo`,
  `LastBaseSHA`. Loaded per feature from `<feature>/stack.yaml`.
- External materialization is directory presence at `<feature>/worktrees/<name>`
  (`internal/cli/add.go:70`, `internal/cli/list.go:63-71`). `tws list` classifies
  `active` / `archived` / `missing`, where `missing` requires `internal.IsPrunableWorktree`.
- Checkout mode has **no** `worktrees/` directory at all (`internal/cli/add.go:122`,
  `Workspace.WorktreePath` returns `""` for `ModeCheckout`, `internal/workspace.go`). Materialization
  in checkout mode means "the Git ref exists", checked by `gitRefExists`
  (`internal/checkout_health.go:617`).
- `tws archive` only sets `StackEntry.Archived` and removes the worktree; the Git branch is
  preserved (`internal/cli/archive.go:63-132`).

### Direct / tmux open and close flows

- External direct (`openDirect`, `internal/cli/open.go:239-279`): runs the agent as a child of the
  `tws` process, then drops into `$SHELL`. **No durable state of any kind is written.** When `tws`
  exits, all evidence is gone. This is the single largest observability gap, and it is not a corner
  case: direct is the **default** external open path — tmux is used only when `--tmux` is passed or
  `config.use_tmux` is explicitly true (`internal/cli/open.go:163-181`). `openDirect` is also the
  target of `tws add --open` (`internal/cli/add.go:105`), `tws open --feature-dir` in both modes
  (`internal/cli/open.go:68,107`), so any instrumentation must handle a feature-dir open that has no
  logical branch. It currently has **no `error` return** — it signals the "agent not found in PATH"
  failure with `fmt.Printf` + `os.Exit(1)` (`internal/cli/open.go:248-251`), which is why the call
  sites carry the "Guard before openDirect, which has no error channel" comments
  (`internal/cli/open.go:94`).
- External tmux (`openWithTmux`, `internal/cli/open.go:323-348`): creates a *detached* session, then
  `tmux send-keys` the agent command. The session outlives the agent — after the agent exits the
  session persists holding a shell. So external tmux presence proves a **session**, never a running
  agent.
- External `--all` (`openAll`, `internal/cli/open.go:283-321`): one feature-level tmux session named
  `sanitizeSessionName(feature)` with one *window* per worktree.
- Checkout direct (`internal.OpenCheckoutDirect`, `internal/session.go:549`): writes
  `<metadata>/state/sessions/active.json` atomically (0600) with `Mode=direct`,
  `PID=os.Getpid()` (the **tws** PID, not the agent PID), and `Stage` transitioning `agent` →
  `shell` (`internal/session.go:583,599`). `Stage` is the one durable signal that distinguishes
  "agent process running" from "agent exited, user in follow-up shell".
- **Complete checkout stage enum**: `agent` and `shell` for `Mode=direct`, and `tmux` for
  `Mode=tmux` (`internal/session.go:657`). There are exactly three values today, they are bare
  strings with no named constants, and `status` must treat any other value as `unknown` rather than
  assuming a two-value direct-only enum.
- Checkout tmux (`internal.OpenCheckoutTmux`): `tmux new-session -d -s <name> -c <dir> -- <command>`
  (`RealSessionTmuxRunner.NewSession`, `internal/session.go:95-102`). Because the agent *is* the
  session's command, session death tracks agent exit far more closely than the external `send-keys`
  form does.
- Close: external `tws close` requires exactly two args and kills only the per-branch session
  (`internal/cli/close.go:59-79`); it cannot address a feature-level `--all` session. Checkout close
  goes through `CloseCheckoutSession` → `finishCheckoutSession` → restore branch, remove links, drop
  state, release lock (`internal/session.go:695-712`).
- **Runner seams already exist in checkout mode and are the model to copy.** `SessionAgentRunner`,
  `SessionShellRunner`, and `SessionTmuxRunner` (`internal/session.go:57-66`) are interfaces with
  real implementations (`internal/session.go:77-113`) that `OpenCheckoutDirect`/`OpenCheckoutTmux`
  accept as parameters and default to the real ones when nil (`internal/session.go:590-595,612-614`).
  External `openDirect` has no equivalent seam at all: it builds `exec.Command` inline
  (`internal/cli/open.go:256-277`), so nothing about its behaviour is testable today.

### Checkout session files, locks, PIDs, stages

- State: `<metadata>/state/sessions/active.json`, single-owner, `CheckoutAgentSession`
  (`internal/session.go:32-49`) including `LockToken`.
- `LoadCheckoutAgentSession` (`internal/session.go:154-164`) **collapses "file missing" and
  "file unparseable" into one error**, and `buildSessionReport` (`internal/checkout_health.go:426-441`)
  treats any error as "no active session", falling through to an orphan-lock check. A corrupt
  `active.json` with a lock held is therefore reported today as a lock/state mismatch, and a corrupt
  `active.json` with no lock is reported as *nothing at all*. `status` must `os.Stat` the path first
  and differentiate: `ENOENT` → no session; stat OK but parse fails → `session_state: invalid`,
  workspace-level `needs_attention`, with guidance, never a silent `absent`.
- Lock: `<metadata>/state/checkout-session.lock/` mkdir-lock plus `owner.json`
  (`sessionLockDir`/`sessionLockOwnerPath`, `internal/session.go:117-122`), with recovery in
  `acquireAgentSessionLock` that only breaks a lock after confirming both the recorded session and
  the lock owner PID are dead. `owner.json` deserializes to `sessionLockOwner{Token, PID, CreatedAt}`
  (`internal/session.go:51-55`) — the `Token` there is the **same secret** as
  `CheckoutAgentSession.LockToken` and must be redacted too.
- Liveness today is `processAlive` (`internal/session.go:263-269`), i.e. `Signal(0)`. The
  checkout-sync path has a second, duplicate copy, `isProcessAlive`
  (`internal/checkout_sync.go:289-296`); `status` must go through the injectable
  `ProcessChecker`/`TmuxChecker` seams rather than either bare helper.

### External tmux naming

`sanitizeSessionName` maps `.`→`_`, `:`→`_`, `/`→`-` (`internal/cli/open.go:356-359`), and the only
exported wrapper, `TmuxSessionName(feature, branch)`, lives in **package `cli`**
(`internal/cli/close.go:84-86`), *not* in package `internal`. That is a hard constraint on placement:
`internal` cannot import `internal/cli`, so a status builder in `internal` cannot call
`TmuxSessionName` at all. Either the builder receives the already-computed external session names
from the `cli` layer, or the naming helper is promoted into `internal` (with `cli` delegating to it).
Do not fork a second sanitizer. Two real consequences of the naming scheme itself:

1. **Collision**: feature `a` + branch `b` → `a-b`, and `tws open a-b --all` → `a-b`. A per-branch
   probe can therefore be satisfied by an unrelated feature-level session.
2. **No workspace scoping**: the tmux server namespace is global, so two workspaces with the same
   feature/branch names share a session name. Checkout mode does not have this problem —
   `CheckoutAgentSessionName` (`internal/session.go:124-136`) hashes `workspaceID/feature/name` and
   appends a 4-byte suffix.

### Sync transactions and failure stages

- Checkout: `<metadata>/state/<feature>-checkout-sync.yaml` + `<feature>-checkout-sync.lock`, with
  `CheckoutStage` = `planned|switched|rebasing|conflict|rebased|validating|completed|restoring`
  (`internal/checkout_sync.go:20-31`), `FailureKind` (`:34-45`), and the `CheckoutTransaction` struct
  itself (`:62-88`) carrying `FailureKind`/`FailureMsg`. `buildOneSyncReport`
  (`internal/checkout_health.go:351-422`) already derives `live|stale|invalid` plus recovery guidance.
- External: entirely different shape — a per-feature dotfile `<feature>/.sync-state.yaml`
  (`internal/syncstate.go`) with `FailedBranch`/`Pending`/`Completed`/`Skipped`. There is no external
  lock file and no PID.
- `CheckoutSessionPreconditions` treats any `*-checkout-sync.{yaml,lock}` in `state/` as "sync
  active" (`anyCheckoutSyncActive`, `internal/session.go:303-318`).

### Doctor / list health models

- `BuildCheckoutHealthReport` (`internal/checkout_health.go:199-240`) composes header, sync reports,
  session report, per-entry ancestry, and context links, each carrying `CheckoutSeverity`
  (`ok|info|warning|error`) and a `Guidance` string. `HasErrors` drives exit status;
  warnings return exit 0 by deliberate design (`internal/cli/doctor.go:16-20`).
- `BuildCheckoutList` (`internal/checkout_health.go:869-937`) is the cheaper cousin producing
  `CheckoutListEntry` with `Current`, `Archived`, `AncestryStatus`, `SessionActive`.
- **Every one of these structs already carries `json:` tags but no command emits JSON.** Doctor and
  list are text-only.
- External health is `internal/health.go` (`CheckFeatureHealth`, `CheckWorktreeBranch`,
  `CheckWorktreeDirty`, `CheckWorktreeInjectLinks`) — worktree-shaped, with no session concept.

### Workspace resolution from supported cwd locations

`internal.RequireWorkspace()` (`internal/workspace.go`) resolves from the repo root and from a linked
worktree via `MainRepoRootIn` → `git rev-parse --git-common-dir`. When cwd is not a Git repo it falls
back to `DetectWorkspaceRoot` (`.tws-workspace` marker walk-up → configured workspaces → `~/tws`,
`internal/paths.go:13-51`) plus `inferExternalRepoRoot`, which is what makes the external workspace
root and external feature directory work. Two failure modes matter for `status`:

- `inferExternalRepoRoot` can return "maps to multiple default repositories" — a hard error.
- `tws list` currently *swallows* a `RequireWorkspace` failure and builds a degraded
  `Workspace{MetadataRoot: wsRoot, Mode: ModeExternal}` (`internal/cli/list.go:20-29`) with an
  **empty `RepoRoot` and an empty `StableID`** — the struct literal sets only two fields, and
  `StableID` is normally filled by `stableID(canon)` (`internal/workspace.go:273,431,462`). Both
  omissions matter: any Git-touching status check must tolerate an empty `RepoRoot` or refuse it
  explicitly rather than silently shelling out with an empty `-C`, and the report header's
  `stable_id` (`CheckoutWorkspaceHeader`, `internal/checkout_health.go:50-59`, with `StableID` at
  `:52` populated by `buildHeader` at `:247`) would be an empty string, which also makes
  `CheckoutAgentSessionName(ws.StableID, ...)` non-reconstructable.
  In the degraded path `status` must emit an explicit `workspace.degraded: true` with
  `stable_id: null`/omitted rather than an empty-string identity an orchestrator could key on.

### JSON and CLI conventions

`--json` exists today on exactly five commands: `tws registry list` (`internal/cli/registry.go:100`),
`tws registry show` (`:145`), `tws registry check` (`:223`), `tws space list`
(`internal/cli/space.go:222`), and `tws space show` (`:296`). The established shape is
`enc := json.NewEncoder(cmd.OutOrStdout()); enc.SetIndent("", "  "); return enc.Encode(v)` plus
nil-slice normalization to `[]` (`internal/cli/registry.go:74-83`); `registry check` additionally
shows the convention of wrapping a single result in a one-element array so the JSON shape does not
change with arity (`internal/cli/registry.go:186-191`), and `space list --json` documents "bare
array, no header" in its own help text (`internal/cli/space.go:147`). Feature names must be guarded
with `internal.GuardFeatureName` before any path join (`internal/spaces.go:681-690`), and listings
must go through `Workspace.ListFeaturesResolved` — a **method on `Workspace`**, not a package-level
function (`internal/resolve.go:139`) — which fails closed on untrusted `spaces.yaml`.

### Skill / documentation surfaces

`assets/skills/claude/tesseraworkspaces/SKILL.md` (command table ~line 32-47, checkout doctor/list
sections ~239-260), `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` (~18-22, 72-95),
`assets/skills/copilot/tws.prompt.md` (~23-36, 80, 117-123), plus `README.md` (~130-144),
`docs/cheatsheet.md` (~107-131), `docs/roadmap.md`, `docs/engineering-workflow.md`, `CHANGELOG.md`.
Skills are `go:embed`-compiled, so they must be updated in the same change.

## Proposed baseline boundary

### What baseline can state authoritatively

| Axis | Source | Confidence |
| --- | --- | --- |
| feature / branch name / git branch / base / archived | `stack.yaml` | authoritative |
| worktree materialized (external) | dir presence + `git worktree list --porcelain` | authoritative |
| ref exists / current branch (checkout) | `gitRefExists`, `healthCurrentBranch` | authoritative |
| checkout session identity, mode, stage, started_at | `state/sessions/active.json` | authoritative |
| checkout session runtime presence | `processAlive(PID)` / `tmux has-session` | good, caveats below |
| checkout session lock held / orphaned | `state/checkout-session.lock` | authoritative |
| external direct session identity, owner PID, stage, started_at | new hidden per-invocation runtime records (this feature) | authoritative for sessions tws launched |
| sync in progress / stalled / failure stage | checkout transaction + lock; external `.sync-state.yaml` | authoritative |
| unread decisions per branch | `internal.UnreadDecisions` | authoritative (an *attention* input, not agent state) |

### What must remain `unknown`

- **Generic approval/input blocking.** The original request's `blocked (needs approval/input)` is
  **not knowable at baseline.** Nothing in tws observes agent stdin, tool-permission prompts, or turn
  boundaries; the agent runs as an opaque child process (`RealSessionAgentRunner.Run`) or an opaque
  tmux command. Any attempt to infer it — TTY polling, `tmux capture-pane` scraping, idle-CPU
  heuristics — would be a fragile guess, would couple tws to specific agent UIs, and would violate
  the roadmap non-goal of pretending to replace the agent harness. It requires the `tss` provider.
- **Externally launched runtimes** (an agent a human started by hand in a worktree, or any agent not
  started through `tws open`/`tws add --open`). tws did not launch it and has no record of it.
- **External direct sessions started before this feature ships**, and any direct session whose record
  was lost. With the new records (below) a tws-launched direct session becomes `present`/`stale`;
  absence of any record still only means "tws has no record", so the honest value for an unrecorded
  worktree remains `absent` for the *tws-owned* axis and `unknown` for "is any agent running".
- **Which recorded process is which after PID reuse.** A live PID probe proves a process with that
  PID exists, not that it is the recorded one; see the PID-reuse discussion below.
- **Checkout direct agent-vs-shell liveness beyond `Stage`.** The recorded PID is the `tws` process,
  not the agent, so `present` means "the tws-owned session is alive", qualified by `stage`.
- **External tmux "is an agent running"**: presence proves a session, not an agent → at best
  `present` for the *session*, with `agent_state: unknown`.
- **tmux absence when `tmux` is not installed.** `sessionExists` and `HasSession` both return false
  on a `LookPath` failure, which is indistinguishable from "no session". Baseline must probe tmux
  availability once and degrade the whole axis to `unknown` rather than reporting a false `absent`.

### Rollup

`needs_attention` → `active` → `idle`, evaluated per branch, then per feature, then per workspace,
with the scope table and the branch-local/feature-level split given under "Agreed inter-tool
boundary". The inputs are structural and tws-owned; none of them require a provider. Unread
decisions are a defensible optional input but should be a distinct field (`unread_decisions`) so an
orchestrator can choose whether it counts as attention. `idle` must never be produced from a
semantic signal.

## External direct-session records (in scope for this feature)

An earlier draft recommended deferring this. That was wrong for two reasons: **direct is the default
external open path**, so deferring leaves the most common case unobservable and makes the whole
command close to useless in external mode; and the inter-tool agreement says tws must be
authoritative **for the sessions it launches**, which it cannot be if it launches a session and
writes nothing. Scope a *narrow, additive*, **concurrency-safe** set of records into this feature.

### Shape: one record per invocation, never one file per branch

An earlier revision proposed a single `<feature>/.sessions/<slug>.json` per logical branch. That is
**wrong and is replaced here**: nothing in external mode prevents two `tws open <feature> <branch>`
invocations in two terminals against the same worktree (external mode has no session lock and no
single-checkout invariant), so a one-file-per-branch layout makes the second open silently overwrite
the first, and makes the first exit delete the second's record. Both directions are data loss. The
layout is therefore **per invocation**:

```
<feature>/.sessions/                          0700  dir, hidden, tws-owned, created lazily
<feature>/.sessions/<branch-id>/              0700  dir, one per logical branch
<feature>/.sessions/<branch-id>/<token>.json  0600  file, one per open invocation
```

- **Feature-scoped and hidden.** `.sessions/` sits next to the existing feature-scoped external
  dotfile `.sync-state.yaml` (`SyncStatePath`, `internal/syncstate.go:19-21`) instead of forcing
  external mode to grow a workspace `state/` convention it does not have. A leading-dot directory is
  already invisible to feature listing (`isReservedDir` skips any `.`-prefixed name,
  `internal/resolve.go:207-209`), and the external feature dir is not itself a Git working tree
  (worktrees are its children), so nothing becomes committable.
- **`<branch-id>` is collision-safe, not the raw name.** The logical branch name may contain `/` and
  other path-hostile characters (`branch-name-decoupling` allows `StackEntry.Branch` ≠ `Name`), so
  `<branch-id>` is a sanitized prefix plus a short hash suffix of the exact `feature/name` identity —
  the same construction `CheckoutAgentSessionName` already uses (`internal/session.go:124-136`,
  `sanitizeSessionPart` + `sha256[:4]` hex). The record also carries `feature` and `name` verbatim,
  so a reader never inverts the directory name and a hash collision is *detectable* (record identity
  disagrees with the requested identity) rather than silently merging two branches. Because the hash
  is over `feature/name`, **any rename invalidates it** — see unresolved decision 13.
- **`<token>` is the ownership token.** 16 bytes from `crypto/rand`, hex-encoded — the same scheme
  `acquireAgentSessionLock` uses for the checkout lock token (`internal/session.go:235-240`). It is
  both the filename and a field inside the record, so a process can prove it owns a file without
  trusting the path. It is **not a secret** (it grants nothing, unlike `CheckoutAgentSession.LockToken`),
  but it is still omitted from `status` output because it has no consumer value.
- **Atomic writes.** Each file is written `0600` via the temp + `Sync` + `Rename` pattern of
  `atomicSessionWrite` (`internal/session.go:169-195`), so a reader sees old or new and never a
  partial record. Because each writer owns a distinct filename, concurrent writers never race on the
  same path at all — the atomicity is only needed for the create → update transitions of one owner.
- **No lock.** These records are advisory liveness data, not a mutual-exclusion primitive. External
  mode deliberately permits concurrent opens; the records describe reality rather than constraining
  it.

Record fields (deliberately minimal):

| Field | Meaning |
| --- | --- |
| `schema_version` | int, versioned like `checkoutSessionSchema` (`internal/session.go:18`) |
| `token` | random ownership token; equals the filename stem |
| `feature`, `name`, `git_branch` | exact identity; `git_branch` from `StackEntry.GitBranch()` (`internal/stack.go:23`) |
| `path` | worktree directory the session runs in |
| `owner_pid` | the **tws** process that owns this record; stable for the record's whole lifetime |
| `child_pid` | the current agent or shell child PID when known; omitted during the `starting` window |
| `stage` | `starting` \| `agent` \| `shell` — extends the checkout vocabulary with the pre-spawn state |
| `started_at` | RFC3339 UTC, set once at creation |
| `updated_at` | RFC3339 UTC, rewritten on every stage/PID transition |

`owner_pid` is the liveness anchor because it is stable: the child PID changes at the agent → shell
transition and is absent in `starting`, so a reader that keyed liveness on `child_pid` alone would
see spurious gaps. `child_pid` is reported as detail, never as the sole presence proof.

**No transcript, no prompt, no agent argv beyond the resolved binary name, no environment.** This is
a liveness record, not a session log. Recording the full agent command line risks capturing secrets
passed as flags; record `agent` as the resolved `parts[0]` at most, or omit it.

### Implementation mechanics in `openDirect`

`openDirect(path string)` (`internal/cli/open.go:239-279`) must change shape in three ways.

**1. It gains an `error` return and its four callers propagate it.** This replaces the
`fmt.Printf` + `os.Exit(1)` "agent not found in PATH" failure (`internal/cli/open.go:248-251`) noted
above. The new signature is roughly
`openDirect(opts directOpenOpts) error` carrying `path`, `feature`, `name`, `gitBranch`, `tracked`,
and the injected seams. All four call sites are already inside `RunE` bodies that return `error`:

| Call site | Change |
| --- | --- |
| `internal/cli/open.go:68` (checkout `--feature-dir`) | `return openDirect(...)`, untracked |
| `internal/cli/open.go:107` (external `--feature-dir`) | `return openDirect(...)`, untracked |
| `internal/cli/open.go:181` (external per-branch direct) | `return openDirect(...)`, tracked |
| `internal/cli/add.go:105` (`tws add --open`) | `if err := openDirect(...); err != nil { return err }` |

**Non-zero exit is achievable without `os.Exit`.** `Execute()` already returns `int` and maps any
`RunE` error to `1` after printing it to stderr (`internal/cli/root.go:16,52-56`), and `main` uses
that value. So every `os.Exit(1)` inside `openDirect` is replaced by a returned error, which also
makes the failure paths assertable in tests — `os.Exit` in a `cli` helper is untestable by
construction.

**2. Injectable runner seams.** `openDirect` builds `exec.Command` inline for both the agent and the
shell (`internal/cli/open.go:256-277`), so no test can observe start/wait/cleanup ordering. Mirror
the checkout-mode precedent (`SessionAgentRunner`/`SessionShellRunner`, `internal/session.go:57-60`,
defaulted to the real implementations when nil, `internal/session.go:590-595`) with a start/wait
shaped seam, because the record needs the child PID *between* start and wait:

```go
type directProcess interface { PID() int; Wait() error; Terminate() error }
type directRunner interface { Start(dir string, command []string) (directProcess, error) }
type directLookPath func(bin string) (string, error)
```

plus a persistence seam so the failure branches are reachable in tests — either the
`internal/direct_session.go` writer functions passed in as a small interface, or a tracked-open
helper (`runTrackedDirectOpen(opts, runner, shellRunner, store)`) that owns the whole protocol and
takes all three seams. Either shape is acceptable; what is not acceptable is calling `exec.Command`
and `os.WriteFile` directly from the command body, because then none of the ordering below can be
tested. Real implementations wrap `exec.Cmd`: `Start`/`Process.Pid`/`Wait`, and `Terminate` sends
`SIGTERM` (then `Kill` after a bounded grace period) and is always followed by `Wait` so no zombie
is left behind.

**3. Safe ordering — record before spawn, own-record-only cleanup.** For a tracked open, with
`LookPath` resolved *before* anything is written so a missing agent binary never leaves a record
behind:

1. **Create own record before spawning.** Generate the token, `MkdirAll` the `0700` `.sessions/` and
   `<branch-id>/` dirs, atomically write `stage: starting`, `owner_pid: os.Getpid()`, no `child_pid`.
   If this write fails, nothing has been spawned: return the error. The window in which a live child
   is unrecorded is now *closed by construction* — the record exists before any process does.
2. **Start the agent child.** On start failure, remove **only this token's file**, then return the
   error.
3. **Atomically update the record** to `stage: agent`, `child_pid: <pid>`, refreshed `updated_at`.
   If this update fails, `Terminate()` the child, `Wait()` it, remove only this token's file, and
   return the joined error. Never continue with a child whose record cannot be maintained.
4. **Wait for the agent.** A non-zero agent exit is reported exactly as today and does not by itself
   abort the shell transition.
5. **Transition to the shell the same way**: update the record to `stage: shell` with `child_pid`
   cleared *before* starting the shell; start the shell; atomically update `child_pid`. Pre-start
   update failure → do not start the shell, remove own record, return the error. Post-start update
   failure → `Terminate()`/`Wait()` the shell, remove own record, return the error. (Whether the
   post-start shell case should instead be downgraded to a warning — the record still truthfully
   carries a live `owner_pid` and `stage: shell`, and killing an interactive shell the user is
   already typing in is itself hostile — is recorded as an unresolved decision, not decided here.)
6. **On normal exit, remove exactly the token-owned file.** Compare the on-disk record's `token`
   with the owned token before unlinking, so a file rewritten by another owner is never removed.
   Then attempt `os.Remove` on the branch dir and on `.sessions/`; both are best-effort and an
   `ENOTEMPTY` (a concurrent sibling session still holds a record there) is expected and ignored.
   **There is no last-writer cleanup and no directory-wide sweep.** Removal failure is a warning,
   not a fatal error — the record is then merely stale, which the reader already handles.

**Untracked opens.** `--feature-dir` opens (`internal/cli/open.go:68,107`) have no logical branch and
therefore no `<branch-id>`; they are **explicitly unrecorded**. They still go through the same
`openDirect` and still return errors (`LookPath`, start, wait) — "unrecorded" means "writes no
record", not "swallows failures". The checkout-mode `--feature-dir` call site additionally must never
touch external state at all. Inventing a synthetic branch id for feature-dir opens is rejected: it
would attribute a session to a branch that does not exist and pollute the per-branch aggregation.

Checkout mode is otherwise untouched: it keeps its existing single-owner
`state/sessions/active.json` + `checkout-session.lock` pair, and must not gain a second parallel
record. The external records exist precisely because external mode has no equivalent — and, unlike
checkout mode, no single-owner invariant either.

### Concurrency, crash, staleness, and cleanup

- **Two concurrent opens on the same branch are a supported, observable state.** Each invocation
  writes its own token file under the same `<branch-id>` directory; neither can overwrite or delete
  the other. `status` aggregates *all* records under a `<branch-id>` into that logical branch's
  entry — a `sessions` array plus a count — and derives branch presence from the aggregate: any live
  record → `present`; records exist but none live → `stale`; no records → `absent`. Collapsing the
  array to "the" session is a spec-level presentation choice, but the aggregation must not lose the
  fact that there were two.
- A crash or `kill -9` leaves records behind. That is the intended fallback: the reader detects
  staleness by `owner_pid` liveness through the injected `ProcessChecker`
  (`internal/checkout_health.go:16-18`), exactly like the checkout session report, and reports
  `runtime_presence: stale` with recovery guidance. **Stale records remain attention signals** —
  they are branch-local `needs_attention` input, never silently discarded and never auto-collected
  by a reader.
- `status` itself stays read-only and never deletes a record, stale or not.
- External `tws close <feature> <branch>` gains record awareness: it removes provably stale records
  and refuses while any record is live, never killing a direct process. Full guard requirement and
  step ordering are specified under "External `close`: guard and exact ordering" below.
- **PID liveness proves less than it looks like, and this analysis will not pretend otherwise.**
  `processAlive` is a bare `Signal(0)` (`internal/session.go:263-269`). A successful probe proves
  exactly one thing: **a process with the recorded PID currently exists**. It does *not* prove that
  it is the process that wrote the record. After PID wraparound an unrelated process can inherit the
  PID, so a `present` verdict carries a bounded false-positive risk. This is a real, accepted limit
  of the baseline, identical in kind to the limit the checkout session record and the checkout-sync
  lock already live with.
  - Portable **process birth identity** — comparing a recorded start time or using a birth-stable
    handle (`/proc/<pid>/stat` field 22 on Linux, `kern.proc` / `KERN_PROC_PID` on Darwin, `pidfd`
    where available) — is the actual fix and is **future work**, not part of this feature.
  - An earlier revision proposed cross-checking `started_at` against the record file's mtime as a
    PID-reuse mitigation. That is **removed**: mtime and `started_at` are both written by the same
    owner at the same moment, so they cannot disagree in a way that reveals a *later*, unrelated
    process reusing the PID. It detected nothing real and would have produced arbitrary `unknown`s.
  - **Age must never become a hard stale verdict.** A long-running agent session is completely
    normal, and a record's age is not evidence of death. Age may be reported as an informational
    field (`started_at`/`updated_at` are already in the record, and a consumer can compute it), and a
    spec may choose to *annotate* an old-and-dead record differently from a freshly dead one, but no
    age threshold may on its own move a record from `present` to `stale`.
- `Signal(0)` also returns `EPERM` for a live process owned by another user, so a record written by
  another user reads dead. Prefer `EPERM` → `unknown` over a confident `stale`.

### External `close`: guard and exact ordering

#### The "deliberately guard-free" comment stops being true

`closeCmd` today carries an explicit comment stating it is *deliberately* guard-free:

> "the external branch only builds a tmux session name and kills that session — it joins no root and
> creates, reads, or removes nothing under `TwsRoot()`. The checkout branch reads the recorded
> session rather than a caller-supplied name. A registered space therefore cannot be reached through
> this command." (`internal/cli/close.go:39-43`)

That justification is **exactly and only** valid while external `close` touches nothing on disk. This
feature invalidates it: reading `.sessions/` means joining `internal.FeaturePath(feature)` — i.e.
`TwsRoot()/<caller-supplied feature>` (`internal/paths.go:84`) — and then enumerating, reading, and
`os.Remove`-ing files underneath it. A caller-supplied feature name that names a **registered space
directory** would therefore reach into that space's tree and delete files in it. That is precisely
the class of reachability `GuardFeatureName` exists to refuse (`internal/spaces.go:681-690`, whose
doc comment requires the root be "the root the calling operation actually reads from, writes to, or
destroys under").

Required change, non-negotiable:

- Call `internal.GuardFeatureName(internal.TwsRoot(), feature)` in the external branch **before**
  `internal.FeaturePath(feature)` is computed and before any `.sessions/` path is joined, statted,
  read, or removed — matching the existing external guard placement in `open`
  (`internal/cli/open.go:95,115`) and `renameBranchExternal` (`internal/cli/rename.go:196`).
- The guard runs before the tmux-name build too, so a refused name never reaches any side effect.
- Rewrite the comment. It becomes a statement of what *is* guarded: the external branch resolves a
  caller-supplied feature name to a path under `TwsRoot()` and mutates files beneath it, so it is
  guarded like every other external path-joining command; the checkout branch still reads the
  recorded session rather than a caller-supplied name and needs no guard. Leaving the old text in
  place would be an actively misleading claim about a command that now deletes files.
- Guarding is scoped to the external branch only; the checkout branch (`runCheckoutClose`) keeps its
  current behaviour and gains no guard, because its identity still comes from `active.json`.

Regression to add: `internal/cli/space_guard_test.go` already hosts the "guarded lifecycle surfaces"
matrix (`:425-444`, currently `add`/`new`/`archive`/`delete`/`rename`/`sync`/`export`) and `close` is
**absent from it today** precisely because it was guard-free; this feature must add a `close` entry.
With a `spaces.yaml` registering a space that owns directory `X`, `tws close X <branch>` must fail
with the canonical `ErrSpaceNameConflict` message **and** leave the target untouched: the existing
`snapshotTreeIgnoringLock` comparison covers "nothing created or removed", and the test additionally
asserts a pre-seeded `.sessions/<branch-id>/<token>.json` inside the space directory is still
byte-identical afterwards and that the fake tmux runner recorded no `kill-session`. The refusal must
be the first observable action — no path join, no stat, no tmux probe before it.

#### Exact ordering of `tws close <feature> <branch>` (external)

This **intentionally changes the early-return ordering**. Today the first thing that can end the
command is `!sessionExists(session)` → `no tmux session found` (`internal/cli/close.go:69-71`). After
this feature, records are consulted *before* tmux, because refusing to disturb a live direct session
outranks killing a tmux session, and because a "no tmux session found" error would otherwise mask a
branch that has live records. The change is deliberate and must be stated in `CHANGELOG.md`.

1. **Guard.** After the existing two-arg check (`internal/cli/close.go:60-62`), call
   `internal.GuardFeatureName(internal.TwsRoot(), feature)`; on conflict return the error with zero
   side effects.
2. **Load all per-invocation records for the branch.** Compute `<branch-id>` from `feature`/`branch`
   and call `LoadDirectSessions(featurePath, branchID)`, which returns **every** record under that
   directory, with an unparseable file surfaced as an `invalid` record rather than dropped. `ENOENT`
   on the directory is a normal empty result. `ENOENT` on an individual listed file mid-enumeration
   is a benign race and is skipped. This happens before any tmux call.
3. **Refuse if any record is live.** A record is live when its `owner_pid` probes alive through the
   injected `ProcessChecker`. If ≥1 record is live: print the live records (token-prefix, `owner_pid`,
   `child_pid` when present, `stage`, `started_at`), return a non-zero error, **kill no tmux session**,
   and **remove no record — not even the provably stale siblings**, so the reported state matches the
   state on disk. The message must state that baseline `close` never kills a direct process and point
   at exiting the session (or a future `tws close --force`), mirroring `CloseCheckoutSession`'s
   "direct checkout session is still active" refusal (`internal/session.go:690-692`).
   **This applies even when a tmux session also exists for the branch.** A live direct record wins:
   tmux is not killed, and the user is told why. Killing tmux while a direct agent runs in another
   terminal would destroy state the command was never asked to touch.
   An `invalid` (unparseable) record is treated as *not provably stale*: it is neither counted live
   nor removed; it is reported, and it does not by itself block the tmux kill.
4. **Remove only provably stale token-owned records.** With no live record, unlink each record whose
   `owner_pid` is provably dead, one file at a time, matching on the record's own `token` exactly as
   the owner-side cleanup does (never a directory sweep, never "remove the records for this branch").
   `EPERM` from `Signal(0)` means *not provably dead* (a live process owned by another user) → keep
   the record, report it, and do not treat it as live-blocking either; it is reported as `unknown`.
   Then best-effort `os.Remove` the now-possibly-empty `<branch-id>/` and `.sessions/` dirs,
   tolerating `ENOTEMPTY`. A removal failure is a warning, not a fatal error.
5. **Preserve the tmux kill when tmux exists.** If `sessionExists(session)`, run the identical
   `tmux kill-session -t <session>` and print the identical `Closed tmux session: %s` line
   (`internal/cli/close.go:73-78`). A `tmux kill-session` failure is still the existing
   `error killing session: %w`.
6. **No tmux, but stale records were cleaned → success.** Return exit 0 with a cleanup message
   (e.g. `Removed N stale direct session record(s) for <feature>/<branch>.`) instead of the old
   `no tmux session found` error. This is the ordering change with the most visible effect: a branch
   whose direct agent crashed now has a working `close`.
7. **Neither tmux nor any record → unchanged error.** Return the existing
   `no tmux session found for %s/%s` verbatim (`internal/cli/close.go:70`). No new wording, no new
   exit code: this is the byte-for-byte path most scripts depend on.

Result matrix (external branch, after the guard):

| Live records | Stale-only records | tmux session | Outcome |
| --- | --- | --- | --- |
| ≥1 | any | present or absent | refuse, non-zero, name live PIDs, kill nothing, remove nothing |
| 0 | ≥1 | present | remove stale records, then kill tmux, existing tmux output preserved |
| 0 | ≥1 | absent | remove stale records, exit 0 with cleanup message (**was** `no tmux session found`) |
| 0 | 0 | present | **byte-for-byte identical to today** |
| 0 | 0 | absent | `no tmux session found for <feature>/<branch>`, unchanged |

**Scope of the byte-for-byte compatibility claim.** The earlier blanket phrasing "existing tmux-kill
behaviour byte-for-byte unchanged" was too broad and is narrowed here: output and exit status are
byte-for-byte identical **only** for a branch with *no direct records at all* and a live tmux session
(row 4), plus the no-tmux/no-record error (row 5). Every other row is a deliberate behaviour change —
new refusal, new cleanup message, or additional cleanup lines preceding the unchanged tmux line — and
must be documented as such rather than claimed as compatible.

Tests (all four behavioural branches, plus the guard):

- **a. Live record + tmux present** → non-zero, message names the live `owner_pid`(s), fake tmux
  runner records **no** `kill-session` call, every record file (live *and* stale sibling) is
  byte-identical afterwards.
- **b. Stale records + tmux present** → stale files gone, live-none, tmux `kill-session` invoked once
  with the expected session name, `Closed tmux session:` line present.
- **c. Stale records + no tmux** → exit 0, stale files gone, empty `<branch-id>/` and `.sessions/`
  pruned, cleanup message present, and the string `no tmux session found` **absent** from output.
- **d. No records + no tmux** → exact existing error string and non-zero status.
- Plus the row-4 control: **no records + tmux present** → stdout/exit compared byte-for-byte against
  the pre-feature baseline.
- Plus the guard regression described above.
- Liveness is driven through the injected `ProcessChecker` and a fake tmux checker/runner; no real
  `tmux` and no real process in CI.

### What this must not change

External worktree and Git behaviour is untouched: no new Git command, no worktree layout change, no
change to `add`, `sync`, `archive`, `list`, `doctor`, or tmux open. `openDirect` keeps printing the
same lines and keeps dropping the user into the same shell in the same directory; the only observable
differences are a hidden `0700` directory tree under the feature dir and a hard failure if a record
cannot be persisted. Records are per-feature, per-invocation, and self-describing, so a workspace that
never opens directly never grows one, and one user's concurrent opens never disturb another's.
`openDirect` gaining an `error` return is a signature change inside package `cli` only — no exported
API changes, and the four call sites already sit in `RunE` bodies, so no user-visible flow changes
except that a previously `os.Exit(1)` failure now returns the same non-zero status through
`Execute()` with the message on stderr instead of stdout.

## Affected real files and symbols

New (expected):

- `internal/agent_status.go` — `AgentStatusReport`, `AgentStatusFeature`, `AgentStatusEntry`,
  `RuntimePresence`, `AgentState`, `AttentionRollup`, `BuildAgentStatus(ws, opts)`,
  `FormatAgentStatus(report)`. **Must live in package `internal`**: the builder needs
  `sessionLockDir`/`sessionLockOwnerPath` (`internal/session.go:117-122`), `sessionStatePath`,
  `sessionDirty` (`:533`), and `processAlive` (`:263`), all of which are unexported. A builder in
  `internal/cli` cannot reach them, and re-deriving `.tws/state/checkout-session.lock` or a second
  `Signal(0)` helper in `cli` would fork the lock/liveness contract. The `cli` layer stays a thin
  dispatcher that formats and encodes.
- `internal/direct_session.go` — the external direct runtime records, per invocation. Expected API:
  `DirectSessionRecord`; `DirectSessionsDir(featurePath)`; `DirectSessionBranchID(feature, name)`
  (sanitized prefix + `sha256[:4]`, mirroring `CheckoutAgentSessionName`, `internal/session.go:124-136`);
  `CreateDirectSession(featurePath, rec) (token string, err error)` which mints the token, creates the
  `0700` dirs and writes the `starting` record; `UpdateDirectSession(featurePath, branchID, token, mutate)`
  for the atomic stage/PID transitions; `LoadDirectSessions(featurePath, branchID)` and
  `ListDirectSessions(featurePath)` returning **all** records (a slice, never one record) with a
  per-file parse error surfaced as an `invalid` record rather than dropped;
  `RemoveOwnedDirectSession(featurePath, branchID, token)` which unlinks only the file whose recorded
  `token` matches and best-effort removes now-empty parent dirs. Note the deliberate absence of any
  `RemoveDirectSessionsFor(branch)`-style sweep. Also in `internal`, because the status builder must
  read the records and `cli` must write them.
- `internal/cli/status.go` — `statusCmd()` with `[feature]` arg, `--json`, mode dispatch.
- `internal/agent_status_test.go`, `internal/direct_session_test.go`, `internal/cli/status_test.go`,
  `internal/cli/direct_open_test.go` (runner/persistence seam behaviour).

Existing files that must change:

- `internal/cli/root.go` — register `statusCmd()`.
- `internal/cli/open.go` — `openDirect` gains an `error` return, feature/name/branch context, the
  injectable runner/persistence seams, and the create → start → update → wait → own-record cleanup
  protocol; `os.Exit(1)` on the `LookPath` failure (`internal/cli/open.go:248-251`) becomes a returned
  error. All four call sites (`internal/cli/open.go:68,107,181`, `internal/cli/add.go:105`) propagate
  it, and the two "Guard before openDirect, which has no error channel" comments
  (`internal/cli/open.go:94` and the `openAll` sibling at `:80`) are re-evaluated — the guards stay,
  the justification comment for `openDirect` no longer applies.
- `internal/cli/direct_open.go` (optional, expected) — the tracked-open helper plus the
  `directRunner`/`directProcess`/persistence seams and their real `exec.Cmd`-backed implementations,
  kept out of `open.go` so the command body stays a dispatcher.
- `internal/cli/add.go` — pass the feature/branch context at the `--open` call site and propagate the
  error.
- `internal/cli/close.go` — external `close` gains `internal.GuardFeatureName(internal.TwsRoot(), feature)`
  **before** any `internal.FeaturePath`/`.sessions` access, the "Deliberately guard-free" comment
  (`internal/cli/close.go:38-42`) is rewritten because the command now reads and removes files under
  `TwsRoot()`, and the record-first ordering of "External `close`: guard and exact ordering" replaces
  the current tmux-first early return. It removes *provably stale* records file by file, must not kill
  a live direct process, and must not touch a live record. If external tmux naming is promoted into
  `internal` for reuse, `TmuxSessionName` (`internal/cli/close.go:84-86`) delegates rather than being
  duplicated.
- `assets/skills/claude/tesseraworkspaces/SKILL.md`,
  `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
  `assets/skills/copilot/tws.prompt.md` — teach `tws status --json` as the orchestrator poll, teach
  that `agent_state` is `unknown` until a provider exists while `needs_attention` is authoritative,
  and document the direct-record staleness caveat: a `present` verdict means "a process with the
  recorded PID exists", not "that exact process exists".
- `README.md`, `docs/cheatsheet.md`, `docs/roadmap.md`, `docs/engineering-workflow.md`,
  `CHANGELOG.md`.

Reused, not modified (reference the existing seams instead of duplicating them):
`internal.ProcessChecker` / `internal.TmuxChecker` (`internal/checkout_health.go:16-33`) are the
correct injection points for deterministic tests; `LoadCheckoutAgentSession`, `sessionLockDir`,
`BuildCheckoutList`, `LoadStack`, `Workspace.ListFeaturesResolved` (`internal/resolve.go:139`),
`GuardFeatureName`, `RequireWorkspace`, `IsPrunableWorktree`, `UnreadDecisions`,
`LoadCheckoutTransaction`, `ReadCheckoutLock`, `LoadSyncState`, `atomicSessionWrite`.
`TmuxSessionName` is **not** in this list in its current form: it lives in package `cli`
(`internal/cli/close.go:84`) and is unreachable from `internal`, so external session names are either
computed in `cli` and handed to the builder, or the helper is promoted into `internal`.

Whether `ProcessChecker`/`TmuxChecker`/`sessionLockDir` should be promoted out of
`checkout_health.go` (or the status builder placed in that file) is a spec decision; do not fork a
second liveness implementation either way.

## Compatibility and storage considerations

- **Additive, with one deliberate exception.** New command, new flag, one new hidden per-feature
  record directory. No change to `sync`, `list`, `doctor`, or `archive`; no change to any existing
  on-disk format; no change to external worktree or Git behaviour. `open` changes only by gaining
  record bookkeeping and an `error` return, with output and worktree semantics preserved. The
  exception is external `close`, whose early-return ordering changes intentionally (records are
  consulted before tmux) and which gains a feature-name guard; the compatibility scope is the matrix
  under "External `close`: guard and exact ordering".
- **One new persisted state, scoped tightly.** `<feature>/.sessions/<branch-id>/<token>.json`, `0600`,
  under `0700` directories, atomic writes, **one file per open invocation**, written only by external
  `openDirect` and removed — by token — on normal exit; full design under "External direct-session
  records". It deliberately does **not** introduce a lock: the records are advisory liveness data, not
  a mutual-exclusion primitive, and external mode has no single-checkout invariant to protect (unlike
  checkout mode, which keeps its single-owner `active.json` + `checkout-session.lock` pair).
  Concurrency safety comes from *disjoint filenames*, not from locking.
- **Stale records are expected, not exceptional.** Crash-left records are detected by `owner_pid`
  liveness on read; `status` never cleans them and always keeps them as attention signals; `close`
  may unlink provably-dead ones individually. No `ps` scan and no directory sweep is ever required.
- **Schema versioning.** Both the JSON payload and each direct record carry an explicit
  `schema_version` (precedent: `checkoutSessionSchema`, `RegistryFile` version probe) so the later
  `tss` enrichment can extend them without breaking consumers.
- **Never serialize `CheckoutAgentSession` directly.** It carries `LockToken` with a
  `json:"lock_token"` tag; leaking it into `tws status --json` would publish the lock secret that
  `releaseAgentSessionLock` uses for ownership. The same secret is also stored in the lock owner file
  as `sessionLockOwner.Token` (`internal/session.go:51-55`, `state/checkout-session.lock/owner.json`),
  so a status builder that reads `owner.json` to report lock ownership must project out `PID` and
  `CreatedAt` only and **never** surface `token` — redaction has to cover both files, not just
  `active.json`. Project into purpose-built structs in both cases.
- **Checkout mode** reads only `.tws/state/**` and `stack.yaml`; `.tws` is git-excluded by
  `EnableCheckoutMode`, so nothing new becomes committable. In external mode the new `.sessions/`
  directory sits in the feature dir, which is not a Git working tree, so it is not committable
  either.
- **Growth is bounded by concurrent opens, not by history.** A branch dir holds one file per *live*
  invocation plus any records left by crashes; normal exits remove their own file and prune empty
  parents, so the steady state for an idle workspace is an empty (or absent) `.sessions/` tree.

## State and rollup risks

1. **Axis collapse.** The main design risk is a future contributor mapping tss `ready` onto tws
   `idle`. The enums must be separate Go types with separate `unknown` zero-ish values, and the
   precedence must be a single tested function.
2. **PID reuse — stated honestly, not mitigated.** A successful `processAlive` probe proves only that
   **a process with the recorded PID currently exists**, never that it is the one that wrote the
   record; after PID wraparound a dead session reads `present`. Full treatment, including the two
   explicitly forbidden "mitigations" (mtime-vs-`started_at` cross-checking, and age as a stale
   verdict) and the deferral of portable birth identity, is under "Concurrency, crash, staleness, and
   cleanup". The risk applies equally to the checkout session record, the checkout-sync lock, and the
   new external direct records. Baseline accepts it and documents it verbatim in the skills and docs:
   "a `present` from tws means a process with that PID exists, not that that exact process exists".
3. **Foreign-owner PID.** `Signal(0)` returns `EPERM` for a live process owned by another user, so
   `processAlive` reports `false`. A genuinely running session under another user is misreported as
   `stale`. This is a pre-existing accuracy limit of doctor too; status should at minimum document
   it, and ideally distinguish `EPERM` → `unknown` from `ESRCH` → `absent`.
4. **External tmux name collision** (`a` + `b` vs `a-b --all`) means a per-branch `present` can be
   produced by a feature-level session. Baseline should probe the feature-level session name
   separately and report it as a distinct `feature_tmux` object rather than folding it into per-branch
   presence.
5. **`IsPrunableWorktree` runs `git worktree list` with no `-C`** (`internal/exec.go:201`),
   inheriting cwd. Invoked from an external workspace root it fails and returns `false`, so a
   genuinely missing worktree is reported as `archived`. Status must pass an explicit repo directory
   rather than reusing this helper as-is.
6. **`tws list` external passes `entry.Name` to `CheckWorktreeBranch`** (`internal/cli/list.go:81`)
   where `internal/health.go` correctly uses `entry.GitBranch()`. Any decoupled-branch entry is
   flagged wrong-branch. Status must use `GitBranch()`; whether to fix `list.go` is a spec decision
   (adjacent, arguably in scope since it is the same reported signal).
7. **Stale ≠ absent.** `stale` must mean "tws holds a record that is contradicted by the runtime"
   (state file present, owner dead / tmux gone / lock mismatch). `absent` must mean "tws holds no
   record and the runtime probe is trustworthy". Conflating them makes recovery guidance wrong.
8. **Direct-record write ordering.** The records introduce one genuinely new failure mode: a started
   child whose record could not be created or updated. The only safe resolutions are "never start it
   unrecorded" (create the `starting` record first) and "terminate and reap it if the record cannot be
   maintained"; silently continuing would create the exact untracked agent this feature exists to
   eliminate. Every one of these branches must be an explicitly tested path through the injected
   seams, not an ignored error return.
9. **Cleanup over-reach.** The symmetric risk to (8) is deleting someone else's record. Cleanup must
   match on the recorded `token`, must unlink exactly one file, and must never sweep a branch
   directory, never "remove the record for this branch", and never treat "I am the last writer" as
   permission to clear the directory. Parent-directory removal is best-effort and must tolerate
   `ENOTEMPTY` from a concurrent sibling session.
10. **Concurrent same-branch opens are normal.** Any design that assumes one session per branch in
    external mode is wrong; the aggregation, the attention rollup, and `close` must all be correct
    with N ≥ 2 records under one `<branch-id>`, including a mix of live and stale ones.

## Concurrency, crash, privacy, security

- **`tws status` is strictly read-only.** It must never acquire the session lock, never `git switch`,
  never break a stale lock, never delete a stale direct record, never write state. Recovery stays in
  `tws close` / `tws sync --continue|--abort`; status only *reports* and emits the same style of
  `Guidance` doctor already produces. The only writer added by this feature is `openDirect`, and only
  for the record it itself owns by token.
- **Concurrent writers are the normal case, and they never share a path.** Each `openDirect`
  invocation owns exactly one `<token>.json`; readers (`status`, `close`) enumerate the branch
  directory. A file appearing or disappearing mid-enumeration must be tolerated: `ENOENT` on a
  subsequent read of a listed entry is a benign race (a session exited between `ReadDir` and
  `ReadFile`), not an error to report.
- **Torn reads.** `active.json` is written via temp+`Sync`+`Rename` (`atomicSessionWrite`,
  `internal/session.go:169-195`) and the new direct records use the same helper, so a reader sees old
  or new, never partial. A record that fails to parse is reported as an `invalid` record for that
  branch and does not abort enumeration of its siblings. A missing file must be a normal "no session"; an existing-but-unparseable
  file must be `invalid` with guidance — not a crash and not a silent `absent`. Because
  `LoadCheckoutAgentSession` returns one undifferentiated error for both cases, the reader must
  `os.Stat` first and branch on `os.IsNotExist` before attributing the failure to a parse error.
- **Crash recovery without process scanning.** The existing triad plus the new records is sufficient
  and must be reused: recorded session PID, lock `owner.json` PID, tmux `has-session`, and each direct
  record's `owner_pid`. No `ps`/proc-table walk, no scanning for agent binaries — that is both non-portable and
  a privacy problem on shared machines.
- **Secrets.** Omit `CheckoutAgentSession.LockToken` **and** `sessionLockOwner.Token` from every
  output path; the lock owner file holds the same secret as the session state file. A direct record's
  `token` is an ownership tag, not a capability — it grants nothing and unlocks nothing — but it is
  still omitted from status output because no consumer needs it. The direct records store no prompt,
  no transcript, and no full agent argv. Consider whether `RepoDir`,
  `Links[].Path`, `Links[].Target`, and each direct record's `path` belong in JSON; doctor already
  prints comparable paths, so parity is defensible, but status output is far more likely to be piped
  into an orchestrator log.
- **File modes.** The direct records inherit the checkout convention: `0700` directories (both
  `.sessions/` and each `<branch-id>/`), `0600` files, so a multi-user host cannot read another user's
  session paths. A `.sessions/` tree created by one user therefore blocks a second user's open on that
  feature with a permission error rather than silently going unrecorded — an acceptable and honest
  failure, and one the spec should surface with actionable guidance.
- **Multi-user hosts.** tmux probes hit the invoking user's tmux server only; a session owned by
  another user is invisible. Report `unknown`, not `absent`.
- **Untrusted metadata fails closed.** `Workspace.ListFeaturesResolved` already hard-fails on a bad
  `spaces.yaml` (`internal/resolve.go:139-147`); status must propagate that error rather than
  emitting a partial JSON document.
- **Exit codes.** Follow doctor: structural warnings exit 0 (orchestrators poll this in a loop);
  reserve non-zero for corrupt/unreadable state and for workspace resolution failure. `--json` must
  emit either a complete document or nothing plus a stderr error — never a half-written body.

## CLI and automation considerations

- `tws status [feature]` with optional feature filter, mirroring `tws doctor [feature]`'s
  `FilterFeature` semantics and its `feature not found` error.
- `--json` matching the registry/space convention exactly (`cmd.OutOrStdout()`, two-space indent,
  `[]` not `null`). Human output goes to stdout, diagnostics to stderr.
- `ValidArgsFunction` returning `internal.ListFeatures()` like `doctor`/`open`.
- Deterministic ordering (feature then entry, stack order or sorted) so orchestrators can diff
  successive polls.
- Cost: `BuildCheckoutHealthReport`/`BuildCheckoutList` fork `git` several times per stack entry
  (`rev-parse`, `merge-base`, `rev-parse --short`). Status is a *polling* surface; the spec should
  decide whether ancestry is included by default or behind a flag, and cache `tmux has-session`
  results per invocation rather than per entry.
- `tws status` must work from all five locations in the retrospective's regression matrix
  (`docs/retrospectives/v1.2.7-upgrade-operations.md`): repo root, linked worktree root and nested
  dir, external workspace root, external feature dir and nested subdir, checkout repo root.

## Real-Git test implications

Per `docs/engineering-workflow.md`, Git behaviour needs real temporary repositories. Existing
helpers to reuse: `setupGitRepoCheckout`, `gitInDir`, `requireWorkspaceForTest`
(`internal/cli/checkout_doctor_test.go`, `internal/cli/checkout_lifecycle_test.go`). Required
coverage:

1. External: active / archived / prunable-missing worktrees; decoupled `Branch` ≠ `Name`;
   feature-level vs per-branch tmux naming, including the `a`+`b` vs `a-b` collision.
2. Checkout: no session; direct session `stage=agent`; direct session `stage=shell`; tmux session
   `stage=tmux` live; tmux session gone (stale); orphan lock with no state; state with no lock;
   `active.json` present but unparseable (must be `invalid`, not "no session") versus `active.json`
   absent (must be a clean "no session") — the stat-vs-parse differentiation asserted explicitly.
3. Sync interaction: a checkout transaction at `conflict` with a dead lock PID must roll up to
   `needs_attention`; external `.sync-state.yaml` with a `FailedBranch` likewise, attributed to the
   named branch at feature level.
4. Liveness must be driven through `ProcessChecker`/`TmuxChecker` fakes — never a real `tmux`
   dependency in CI, and never a real spawned agent.
5. External direct records — record lifecycle, driven entirely through the injected runner and
   persistence seams (no real agent, no real shell, no `os.Exit`):
   a. **Happy path ordering**: assert the `starting` record exists *before* `Start` is called, that
      `stage=agent` + `child_pid` is written after `Start` and before `Wait` returns, that the
      transition to `stage=shell` happens before the shell is started, and that the file is gone
      after normal exit. A fake runner that records call order is the assertion vehicle.
   b. **Two concurrent same-branch records**: create two records for the same `feature`/`name` (two
      simulated invocations), assert two distinct files exist under one `<branch-id>` directory, that
      neither create overwrote the other, that `ListDirectSessions` returns both, and that the status
      aggregation reports both under the single logical branch with a live/stale mix handled
      correctly (one live + one dead → branch `present` **and** branch-local `needs_attention` from
      the dead one).
   c. **Token-owned cleanup**: after two concurrent records, the first owner's cleanup removes
      **only** its own token file and leaves the sibling untouched; the branch directory survives
      (`ENOTEMPTY` tolerated); after the second owner cleans up, the now-empty branch dir and
      `.sessions/` are pruned best-effort. Also assert that cleanup with a non-matching token is a
      no-op (a file whose recorded `token` differs is never unlinked).
   d. **Runner failure paths**: agent `Start` returns an error → own record removed, no child, error
      returned (non-zero exit via `Execute()`), sibling records untouched; `LookPath` failure →
      error returned rather than `os.Exit`, **and no record created at all**.
   e. **Persistence failure paths**: create failure → nothing spawned, error returned; post-start
      update failure → fake process observes `Terminate` **and** `Wait`, own record removed, joined
      error returned; shell-stage pre-start update failure → shell never started, own record removed,
      error returned. Assert no surviving child in every case.
   f. **Staleness**: a record whose `owner_pid` is dead (fake `ProcessChecker`) reads `stale` and
      rolls up to branch-local `needs_attention`; it is **not** removed by `status`; record age alone
      never changes the verdict (assert an artificially old but live record still reads `present`,
      and that no mtime/`started_at` comparison is consulted).
   g. **Path safety**: `<branch-id>` collision safety for `Name` containing `/` and for two distinct
      identities whose sanitized prefixes collide (distinct hash suffixes ⇒ distinct directories);
      a record whose `feature`/`name` disagree with the requested identity is surfaced as a
      collision, not merged.
   h. **Modes**: `.sessions/` and `<branch-id>/` are `0700`, record files are `0600`.
   i. **Untracked opens**: `--feature-dir` opens in both modes write **no** record anywhere under
      `.sessions/`, yet still propagate `LookPath`/start/wait errors to the caller.
6. External `close`: the guard regression and all four behavioural branches (live-record refusal,
   stale+tmux, stale+no-tmux cleanup success, neither) plus the row-4 byte-for-byte control, exactly
   as enumerated under "External `close`: guard and exact ordering".
7. Rollup scoping: a feature-level sync failure must set `features[].attention` without setting every
   `entries[].attention`; a workspace-level orphan lock must not be attributed to any branch.
8. JSON contract tests: stable key set, `schema_version` present, `agent_state == "unknown"`
   everywhere at baseline, `needs_attention` actually produced by a structural fixture, and explicit
   assertions that neither `lock_token` (session state) nor `token` (lock `owner.json`) appears
   anywhere in the encoded document.
9. Workspace-resolution matrix test for all five cwd locations, including the degraded
   external fallback where `RepoRoot` **and** `StableID` are empty.
10. External-mode regression: `tws list`, `tws doctor`, `tws open` output unchanged, `tws close`
    output unchanged **for the two compatible rows only** (no records + live tmux; no records + no
    tmux), and external worktree/Git behaviour (worktree creation, layout, branch handling)
    unchanged.
11. `openDirect` error propagation: each of the four call sites (`internal/cli/open.go:68,107,181`,
    `internal/cli/add.go:105`) returns the error to `RunE`, and `Execute()` maps it to exit `1`
    (`internal/cli/root.go:52-56`) — asserted without any `os.Exit` in the path, which is what makes
    these tests possible at all.

## Dependency recommendations

All slugs below are real directories in `.tpatch/features/` and currently in state `applied`. This
section **recommends**; it does not mutate the DAG. Register with
`tpatch feature deps agent-work-status-dashboard add <parent>` and then run
`tpatch feature deps --validate-all`, per `.tpatch/steering/local.md`.

Hard — true current parents (unchanged from the earlier draft):

- `checkout-agent-sessions` — the `CheckoutAgentSession` record, the `agent`/`shell`/`tmux` stage
  vocabulary, the session lock and its random token scheme, `atomicSessionWrite`, and
  `CheckoutAgentSessionName` are the primary data source, and the new external direct records reuse
  its stage vocabulary, atomic-write helper, token generation, and collision-safe naming.
- `checkout-doctor-observability` — severity/liveness/guidance vocabulary and the
  `ProcessChecker`/`TmuxChecker` seams are reused directly.
- `workspace-mode-foundation` — external/checkout dispatch, `Workspace`, `StableID`.
- `checkout-workspace-lifecycle` — checkout feature layout and logical branch metadata.
- `tmux-session-management` — external tmux naming, `sessionExists`, `TmuxSessionName`, and the
  existing `[tmux]` tag in `tws list`. **Not reachable transitively** through any other recommended
  parent (see the reachability review below), so this edge must be registered explicitly.
- `fix-external-feature-dir-resolution` — the cwd resolution matrix status must satisfy.
- `workspace-sibling-links` — `GuardFeatureName` / `ListFeaturesResolved` exclusion and fail-closed
  `spaces.yaml` semantics, which any feature-listing command must honour.

Hard — **added by this revision** (each is a signal this feature reads or a behaviour it must not
break; omitting them understates the real coupling):

- `checkout-stack-safety` — owns `CheckoutTransaction`, the `planned…restoring` stage enum,
  `FailureKind`, and the `<feature>-checkout-sync.{yaml,lock}` pair (`internal/checkout_sync.go`).
  The sync half of `needs_attention` is entirely this feature's data model.
- `sync-continue` — owns the external `.sync-state.yaml` schema with `FailedBranch`/`Pending`/
  `Completed`/`Skipped` (`internal/syncstate.go`) and the `--continue`/`--abort` recovery verbs that
  status's guidance strings point at. External sync attention is unreadable without it.
- `branch-name-decoupling` — makes `StackEntry.Branch` ≠ `Name` legal, which is precisely why status
  must report both identities, must use `GitBranch()` for Git probes, and why the direct-record
  `<branch-id>` directory needs sanitization plus a hash suffix rather than the raw name.
- `worktree-health-check` — owns `CheckWorktreeBranch`/`CheckWorktreeDirty`/`CheckFeatureHealth`
  (`internal/health.go`), the source of the external wrong-branch and dirty-tree attention inputs,
  and the origin of the `list.go:81` `entry.Name`-vs-`GitBranch()` discrepancy noted above.

Hard — **open/close call-path owners, added by this revision**. Each of these owns a code path this
feature rewrites (`openDirect` and its four call sites, or the direct-vs-tmux dispatch that decides
whether a record is written at all). None of them is reachable through any other recommended parent —
`fix-open-cwd-after-exit`, `open-feature-dir`, and `quick-start-add-and-open` have **no recorded
`depends_on` and no recorded reverse edges from any recommended parent** (isolated nodes), and
`tmux-free-mode` depends only on `fix-initial-claude-session`, which is itself outside the recommended
closure. So each edge must be registered directly or the coupling is unrecorded:

- `fix-open-cwd-after-exit` — owns the post-agent behaviour of `openDirect`: the deliberate move away
  from `syscall.Exec` to `exec.Command` with `Dir` set, plus the interactive `$SHELL` spawned in the
  worktree dir so the user lands there (`internal/cli/open.go:265-278`). The record's `shell` stage
  exists *only* because that shell exists, and the "update to `stage: shell` before starting the
  shell" ordering, the `child_pid` clearing, and the unresolved decision about terminating a live
  interactive shell (decision 10) are all direct consequences of this feature's behaviour. Any
  regression here silently changes where the user ends up after the agent exits.
- `tmux-free-mode` — created the tmux-optional direct path and the `--no-tmux` flag plus the
  `config.use_tmux` resolution that makes direct the **default** external open
  (`internal/cli/open.go:161-181`). It is the reason the observability gap exists at all and the
  reason the records are in scope rather than deferred; the dispatch it owns decides whether an open
  is recorded (direct) or not (tmux).
- `open-feature-dir` — owns `--feature-dir` and `--all` (`internal/cli/open.go:51-70,89-108`, `openAll`
  at `:283-321`). It supplies two of the four `openDirect` call sites — the branch-less opens that are
  **explicitly unrecorded** — and the feature-level session name that collides with the per-branch
  name (`a`+`b` vs `a-b`), which is why `feature_tmux` must be reported separately.
- `quick-start-add-and-open` — owns the `tws add --open` call site (`internal/cli/add.go:105`), the
  fourth `openDirect` caller and the only one outside `open.go`. It must propagate the new `error`
  return and supply feature/branch context so a quick-start open is recorded like any other.

Registration commands (recommendation only; run these to actually mutate the DAG):

```sh
# prior hard parents (kept, unchanged)
tpatch feature deps agent-work-status-dashboard add checkout-agent-sessions:hard
tpatch feature deps agent-work-status-dashboard add checkout-doctor-observability:hard
tpatch feature deps agent-work-status-dashboard add workspace-mode-foundation:hard
tpatch feature deps agent-work-status-dashboard add checkout-workspace-lifecycle:hard
tpatch feature deps agent-work-status-dashboard add tmux-session-management:hard
tpatch feature deps agent-work-status-dashboard add fix-external-feature-dir-resolution:hard
tpatch feature deps agent-work-status-dashboard add workspace-sibling-links:hard
# signal owners added by the previous revision
tpatch feature deps agent-work-status-dashboard add checkout-stack-safety:hard
tpatch feature deps agent-work-status-dashboard add sync-continue:hard
tpatch feature deps agent-work-status-dashboard add branch-name-decoupling:hard
tpatch feature deps agent-work-status-dashboard add worktree-health-check:hard
# open/close call-path owners added by this revision
tpatch feature deps agent-work-status-dashboard add fix-open-cwd-after-exit:hard
tpatch feature deps agent-work-status-dashboard add tmux-free-mode:hard
tpatch feature deps agent-work-status-dashboard add open-feature-dir:hard
tpatch feature deps agent-work-status-dashboard add quick-start-add-and-open:hard
# soft
tpatch feature deps agent-work-status-dashboard add decision-read-tracking:soft
tpatch feature deps agent-work-status-dashboard add workspace-registry:soft
tpatch feature deps agent-work-status-dashboard add list-features-branches:soft
tpatch feature deps agent-work-status-dashboard add persist-agent-workflow-guidance:soft
tpatch feature deps agent-work-status-dashboard add skill-distribution:soft

tpatch feature deps agent-work-status-dashboard
tpatch feature deps --validate-all
```

Soft:

- `decision-read-tracking` — stays **soft**. `UnreadDecisions` is only consumed if the unread-decision
  count is accepted into scope (unresolved decision 2). If the spec accepts it as a reported field or
  an attention input, promote this to hard at that point; until then status compiles and behaves
  correctly without it.
- `workspace-registry` — precedent for the `--json` output convention and versioned file schema.
  Note that it is already a transitive hard ancestor via `workspace-sibling-links`, so registering it
  soft is documentation of intent rather than an added constraint.
- `list-features-branches` — the `tws list` surface status sits beside.
- `persist-agent-workflow-guidance` / `skill-distribution` — embedded skill surfaces that must be
  updated with the new command.

Redundancy / transitivity review (recorded; **no DAG mutation performed**):

Reachability here means "appears in the transitive `depends_on` closure of one of the *other*
recommended parents". A slug's own `depends_on` list says nothing about whether it is reachable — only
its *reverse* edges do. An earlier revision inverted this test and drew the wrong conclusions; the
corrected result, read from the current `.tpatch/features/*/status.json` files, is:

- **Reachable transitively (registering them directly is documentation, not a new constraint):**
  `workspace-mode-foundation`, `fix-external-feature-dir-resolution`, `checkout-doctor-observability`,
  `checkout-workspace-lifecycle`, `checkout-stack-safety`, and `checkout-agent-sessions` — all inside
  the closure of `workspace-sibling-links`, which depends hard on `workspace-mode-foundation`,
  `fix-external-feature-dir-resolution`, `workspace-registry`, `checkout-doctor-observability`, and
  `persist-agent-workflow-guidance`, and whose `checkout-doctor-observability` edge in turn pulls in
  `checkout-workspace-lifecycle`, `checkout-stack-safety`, `checkout-agent-sessions`,
  `fix-checkout-feature-path-routing`, and `fix-external-feature-dir-resolution`.
- **NOT reachable — must be registered directly or the coupling is simply unrecorded:**
  `workspace-sibling-links` (nothing among the recommended parents depends on it),
  **`tmux-session-management`** (it has no recorded `depends_on` *and* no recorded reverse edges from
  any recommended parent — it is an isolated node, so the external tmux naming coupling is invisible
  unless registered), `sync-continue`, `branch-name-decoupling`, `worktree-health-check`, and all four
  open/close call-path owners: `fix-open-cwd-after-exit`, `open-feature-dir`, and
  `quick-start-add-and-open` are isolated nodes (no `depends_on`, no reverse edge from any recommended
  parent), and `tmux-free-mode`'s only edge is to `fix-initial-claude-session`, which is itself outside
  the recommended closure. Among the soft candidates, `list-features-branches` and
  `decision-read-tracking` are likewise unreachable.
- Keeping the reachable ones explicit is still the right call for this repository: the parents are
  named for the *symbols this feature actually reads*, they survive any future re-parenting of
  `workspace-sibling-links`, and `tpatch` treats the edge set as documentation of coupling, not only
  as an ordering constraint. The one thing to avoid is a *contradiction* — `workspace-registry` must
  not be recorded soft in a way that a reader mistakes for "optional", since it is transitively hard
  via `workspace-sibling-links`.
- No cycle is introduced: none of the recommended parents depends, directly or transitively, on
  `agent-work-status-dashboard`, which has no children. `quick-start-add-and-open` has one reverse
  edge (`fix-add-tmux-flag` depends on it), which is unrelated to this feature and does not create a
  cycle.

## Unresolved decisions for spec

1. Exact JSON schema: field names, nesting (workspace → feature → entries), and whether `sync` and
   `feature_tmux` are siblings of the entry list or embedded per entry; how branch-local,
   feature-level, and workspace-level attention are represented without duplication.
2. Whether `unread_decisions` contributes to `needs_attention` or is reported as a separate signal
   (this also decides whether `decision-read-tracking` becomes a hard parent).
3. Whether ancestry (`current|stale|divergent|missing`) is in the default payload or behind a flag,
   given the per-entry `git` fork cost on a polling surface.
4. Whether `tws status` also gains `--all` (all features, no filter) and whether `--json` should be
   allowed to coexist with a feature filter (recommend: yes to both).
5. Whether to promote `ProcessChecker`/`TmuxChecker`/`sessionLockDir` into a shared file or place the
   builder inside `checkout_health.go`. Either way the builder stays in package `internal`; only
   *where within* `internal` is open.
6. Whether `TmuxSessionName` is promoted from `internal/cli` into `internal` (with `cli` delegating)
   or external session names are computed in `cli` and passed into the builder.
7. Whether to add `--json` to `doctor`/`list` in the same change (the structs are already tagged) or
   keep this feature to one new command.
8. Whether the `list.go:81` `entry.Name` vs `GitBranch()` discrepancy is fixed here or split out.
9. Direct-record details now that they are **in scope**: exact `<branch-id>` hash length and prefix
   truncation; whether the resolved agent binary name is stored at all; how many records per branch
   `status` renders in human output before summarising ("3 sessions, 1 stale"). Settled here and
   **not** reopened: one record per invocation, token-owned cleanup, `--feature-dir` opens being
   unrecorded, and `close` cleaning provably-stale records by default (not opt-in) under the ordering
   specified above.
10. The one deliberate asymmetry in the write protocol: whether a **post-start** `child_pid` update
    failure during the `shell` stage should terminate the user's interactive shell (symmetric with the
    agent stage, as specified above) or be downgraded to a warning, given the record already carries a
    live `owner_pid` and the correct `shell` stage. Recommend deciding explicitly in `spec.md`; the
    analysis specifies the symmetric behaviour as the default.
11. Whether the JSON exposes each record individually (`entries[].sessions[]`) or only an aggregate
    (`count` + worst-case presence). Recommend individually: two same-branch sessions are precisely
    the state an orchestrator needs to see, and aggregation is lossy.
12. Whether a future `tws close --force` (terminating a live direct session) is worth reserving in
    the docs now, given this feature explicitly refuses to kill live direct processes.
13. **Rename × direct records — must be decided, not left accidental.** Both rename verbs invalidate
    record identity, and neither is aware of `.sessions/` today:
    - `tws rename feature <old> <new>` is a single `os.Rename(oldPath, newPath)` of the whole feature
      directory (`internal/cli/rename.go:64`). The `.sessions/` tree moves with it silently, so every
      record's `feature` field, and the `<branch-id>` hash (computed over `feature/name`,
      mirroring `CheckoutAgentSessionName`, `internal/session.go:124-136`), now disagree with their
      own location. A live owner still holding the old path would also keep writing stage updates to
      a path that no longer exists, and its own token-matched cleanup on exit would silently no-op —
      leaving a permanently stale record under the new name.
    - `tws rename branch <feature> <old> <new>` rewrites `StackEntry.Name` and may rewrite the Git
      branch (`renameBranchCheckout` / `renameBranchExternal`, `internal/cli/rename.go:117-230`),
      changing both `<branch-id>` and the record's `name`/`git_branch` fields. Note that
      `git_branch` is **not** derivable from the directory name or the record path: it comes from
      `StackEntry.GitBranch()` (`internal/stack.go:23`) and therefore requires a `stack.yaml` lookup,
      so any record rewrite is a stack-reading operation, not a pure path move. The external branch
      rename also removes and re-adds the worktree, so a record's `path` can go stale independently.
    - **Recommended default (to be confirmed in `spec.md`): refuse the rename while any record for
      the affected identity is live**, with the same message shape as external `close`'s live-record
      refusal (name the live `owner_pid`s, state that tws will not kill a direct process). For
      records that are only *provably stale*, either clean them as part of the rename or require the
      user to run `close` first; both are acceptable, cleaning is friendlier and reuses the exact
      token-matched unlink from `close`.
    - An **atomic rewrite** of records to the new identity (rewriting `feature`/`name`/`git_branch`
      and relocating `<branch-id>/`) is the only alternative worth considering, and it is only safe
      if the later spec proves that a concurrently live owner cannot lose or duplicate its record
      across the move — which it cannot do today, because owners hold a path, not a handle, and there
      is no lock in external mode. Until that proof exists, refuse-while-live is the position.
    - Whichever way this is decided, `internal/cli/rename.go` is either explicitly in scope (refusal
      or cleanup) or explicitly documented as out of scope with the stale-record consequence stated;
      it must not be left unmentioned. Tests: rename with a live record refuses and touches nothing;
      rename with only stale records behaves per the chosen policy; rename with no records is
      byte-for-byte unchanged.

## Follow-up child features (explicitly not this feature)

- `tss-agent-state-provider` — invoke the versioned `tss` CLI JSON provider to populate
  `agent_state` and to cover runtimes tws did not launch. Owns provider discovery, timeout,
  version negotiation, and degradation to `unknown` on any failure. This feature ships the stable
  two-axis schema and `schema_version` that make it purely additive.
- Termination semantics for live direct sessions (`tws close --force`), if wanted.
- **Portable process birth identity** — recording and verifying a process start time or using a
  birth-stable handle (`/proc/<pid>/stat`, `kern.proc` on Darwin, `pidfd`) to close the PID-reuse
  false-positive window for the checkout session record, the checkout-sync lock, and the external
  direct records alike. Baseline deliberately ships without it and says so.

## Viability

Viable and well-scoped **as a topology-and-tws-owned-runtime report plus one narrow writer**. Almost
every authoritative field already exists on disk; the work is projection, a stable JSON contract,
honest `unknown`s on the semantic axis, and the missing external direct records without which tws
cannot be authoritative for sessions it launches. The writer is small but must be built with seams
and explicit ordering from the start — per-invocation records, token-owned cleanup, an `error`
return instead of `os.Exit`, and injectable runner/persistence seams — because every one of its
failure modes (unrecorded live child, deleted sibling record, untestable exit) is cheap to prevent by
design and expensive to retrofit. The request's headline capability —
"blocked (needs approval/input)" — is explicitly **out of reach at baseline** and must be documented
as such in `spec.md` and in the skills, so orchestrators do not build on a field that will always
read `unknown` until the `tss` provider ships. What orchestrators *can* build on immediately is
`needs_attention`, which is fully derivable from tws-owned structural state. Delivering the two-axis
schema now is what makes the provider feature additive.
