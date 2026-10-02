---
mode: agent
description: Manage feature workspaces and stacked worktrees with tws
tools: terminal, editFiles, readFile
---

# tesseraworkspaces (tws)

You are working in a project that uses `tws` for feature-scoped workspaces with stacked git worktrees.

## CLI Reference

`tws` is a compiled Go binary on PATH. Invoke it directly:

- `tws add <feature> [-n <branch>] [--open]` — Create feature (quick start with -n)
- `tws new <feature> <branch> [--base <parent>] [--force]` — Create worktree branch
- `tws open [feature] [branch] [--tmux] [--no-agent]` — Open worktree and run agent
- `tws sync <feature> [--push] [--verbose] [--continue] [--abort] [--fetch|--no-fetch] [--full|--local-only] [--only <entry>|--from <entry>]` — Rebase worktrees in dependency order, optionally scoped
- `tws sync <feature> --plan [--json] [--max-replay-per-entry N] [--max-replay-total N]` — Preview the rebase and guard limits; candidates are an upper bound, never applied
- `tws sync <feature> --approve-plan <fingerprint> [--max-replay-per-entry N | --max-replay-total N]` — Execute a previewed, guarded plan
- `tws push <feature> [--dry-run]` — Push all branches with --force-with-lease
- `tws export <feature> [--full] [--to-repo]` — Export workspace metadata
- `tws import <file> [--from-repo <feature>]` — Import workspace
- `tws stack <feature>` — Show branch dependency tree
- `tws stack status <feature> [--json]` — Stack ancestry, materialization, and upstream status
- `tws stack reparent <feature> <entry> --onto <dest> [--onto-kind auto|entry|ref] [--cutoff <ref>]` — Move one entry onto a new parent and replay its descendants
- `tws stack reparent <feature> <entry> --onto <dest> --plan --max-replay-total N [--json]` — Preview the bounded reparent; moves no branch and writes no tws state; may fetch according to policy
- `tws stack reparent <feature> --continue` / `--abort` — Resume or roll back the persisted reparent; neither takes an approval token
- `tws list` — List features and branches
- `tws delete <feature>` — Remove feature and all worktrees
- `tws archive <feature> <branch>` — Remove worktree, keep branch ref
- `tws decide <feature> "<summary>" [--type T] [--to B]` — Record a decision
- `tws decisions show [feature] [--mine] [--all]` — View decisions (auto-detects feature)
- `tws decisions ack [feature]` — Mark all decisions as read
- `tws inject <feature> [branch] [--into <path>]` — Sync inject/ files into external linked worktrees
- `tws hooks install/remove [feature]` — Manage agent hooks
- `tws registry add/list/show/check/...` — Manage opt-in global workspace discovery
- `tws space add/list/show/remove` — Link and discover tool-owned sibling spaces
- `tws doctor [feature]` — Run health checks, including stack ancestry per configured parent-child edge
- `tws status [feature] [--json]` — Global status without an argument; selected-feature projection with a feature
- `tws rename feature/branch` — Rename feature or branch
- `tws config show/set/get` — Manage configuration
- `tws close <feature> <branch>` — Close a session: refuses while a direct record is live, then kills tmux
- `tws template sync <feature> [--template <dir>]` — Backfill templates

## Stacked & Divergent Branches

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-tests --base auth-models             # diverges (parallel)
tws new auth wiki-docs --repo ../wiki --base master     # base resolved in wiki repo
```

Explicit base refs are literal (`master` is local, `origin/master` is remote); tags and commit SHAs are accepted. Sync rebases in topological order. After resolving a conflict, `tws sync <feature> --continue` resumes deferred descendants and only reports completion after parent-child ancestry is current.

New branches store the exact full commit used for creation. Before any sync
branch moves, tws freezes one effective replay cutoff per selected row. A
recorded `last_base_sha` must resolve, be an ancestor of the captured child,
and must not predate any best merge base already shared by parent and child.
If it is absent, the exact captured parent tip is accepted only when Git proves
it is an ancestor of the child. tws never substitutes a merge-base, fork point,
reflog entry, or arbitrary ancestor. Plans/status/doctor distinguish the raw
record from the effective cutoff and its source/validity; approval cannot waive
invalid evidence.

When the recorded parent itself is wrong — squash-merged, abandoned, or never the intended base — reparent instead of syncing:

```sh
tws stack reparent auth auth-middleware --onto main --plan --max-replay-total 20
tws stack reparent auth auth-middleware --onto main --approve-plan <fingerprint> --max-replay-total 20
```

A fresh reparent is always guarded: preview and execution use the same replay
limit flag(s) and values, and execution adds `--approve-plan`. A limitless
preview has a null fingerprint. Destination storage is canonical: a stack-entry destination stores the logical entry name, a named literal ref stores its full `refs/...` name, and a raw object-id destination stores the full lowercase OID. It commits every moved branch in one race-atomic compare-and-swap transaction (crash-atomic only on the reftable backend), has exactly one commit point requiring both durable post-image `stack.yaml` and planned-or-no-op refs, and recovers **forward** past that point. Git refs, `stack.yaml` metadata, and worktree/index state are three separate effects; runtime state is durable recovery evidence, not a transactional effect. `--continue` and `--abort` take the whole frozen decision from persisted state and never accept an approval token, destination, cutoff, limit or fetch flag. While a reparent is recorded, `tws sync <feature>` refuses on every verb. Checkout mode blocks opening every feature in that workspace, serializes checkout sync/reparent mutations across features, and rejects non-empty entry `repo` values; external direct/tmux/all launches publish scoped intent before their final mutation check. tws changes no remote ref and no pull request; real top-level external push holds the feature mutation lock across all entries, clears obsolete follow-up rows before lease preflight, waits for a proven commit point, then warns and uses `--force-with-lease --force-if-includes`. Every mutating external sync route holds that same lock through rebase, metadata, optional push and remote follow-up clearing, rechecks reparent state after claiming, and releases last. If the checkout-global lock is absent, current tws scans every feature's recoverable checkout sync/reparent state before fresh mutation and recovery reconstructs only its own reservation. Checkout feature-directory opens hold the workspace launch intent through the agent/shell after a final guard check. Different stored `repo` spellings on one logical edge are refused even when they resolve to one common directory.

v1.2.16 only fails closed for same-feature sync after the compatibility envelope exists; it cannot see artifact-before-compat window 1, workspace-global locks, unrelated-feature checkout reparent, or top-level push. **Do not use an older tws while any reparent is active or recoverable.**

## Context Injection

In external mode, files in `inject/` are symlinked into every materialized
linked worktree. Complete logical names such as `review/pr-123` are supported;
intermediate directories are never injection targets, and existing files are
preserved. Injected files appear as untracked in git — add them to `.gitignore`
or use an ignored subfolder.

Checkout mode has no linked worktrees, so both feature-wide and
branch-specific `tws inject` refuse explicitly.

## Decisions

```sh
tws decide <feature> "Changed X" --type breaking           # broadcast
tws decide <feature> "Review Y" --type review --to <branch> # targeted
tws decisions show <feature>                                 # unread only
tws decisions ack <feature>                                  # mark as read
```

## Checkout Workspace Mode

For a small repo using one physical checkout:

```sh
tws init --mode checkout
tws mode
tws add auth
tws new auth auth-models
git switch auth-models
```

Checkout mode stores local metadata under `.tws/features/`, adds `.tws/` to the repo's local Git exclude, creates logical Git branches without linked worktrees, and preserves branches on archive/delete by default. It is single-repository; `--repo` is rejected.

`tws sync <feature>` is transactional in checkout mode: it requires a clean attached checkout, switches/rebases logical branches sequentially, persists recovery state under `.tws/state/`, and restores the original branch. Use `--continue` after resolving conflicts and `--abort` to recover. It must be run from the repository checkout: a cwd inside a linked worktree of the same repository is refused.

Sync modes apply to both workspace modes and are three independent axes: `--fetch`/`--no-fetch` (input refs; external defaults to `fetch`, checkout to `no-fetch`), `--full`/`--local-only` (propagation), and `--only <entry>`/`--from <entry>` (scope, by logical `stack.yaml` name). Successful no-mode-flag behavior remains unchanged. `--no-fetch` forbids automatic network *input* only — an explicit `--push` is still allowed. Scoped/local-only, archived, and checkout rebases use `git -c rebase.updateRefs=false rebase`, so unselected branches never move even when `rebase.updateRefs=true` is configured, including on Git 2.26-2.37. Intentional full external `--update-refs` requires Git 2.38. Every new ordinary-sync `--push` is strict: it stops at the first rejected push, keeps recovery state, and `--continue` retries only entries that were never pushed. A `scope=all` run still pushes the whole feature; standalone `tws push` retains lenient failure behavior. Do not use an older tws to continue or abort a transactional sync run.

New ordinary sync runs snapshot exact selected branch tips, frozen replay
cutoffs, original checkout positions, and exact pre-run `stack.yaml` bytes
before the first mutation. Exact executed destinations, checkout transition/
restoration intents, and per-collateral parent identity (including local,
remote-tracking, tag, and object parents) are measured and persisted before
their respective effects, after any preceding row has moved. Abort restores only run-attributable local
effects using per-repository compare-and-swap ref updates; refs, metadata, and
checkout/worktree state remain separate effects, so multi-repository rollback
is not one atomic operation. A later user ref move is preserved by refusing
rollback. If native Git reflog evidence cannot prove an allowed ref move came
from tws's rebase, recovery refuses conservatively.

For collateral `--update-refs` movement, the recorded destination must describe
the actual old-cutoff → new-cutoff replay. A fixed tag/OID parent may transition
to the primary `--onto` destination only when its frozen cutoff exactly equals
the primary replay cutoff and containment is proven. A cutoff inside the replay
range needs its moving local parent's exact postimage; otherwise refuse before
mutation. The recorded transition is still an old cutoff, not a configured
destination override: after fixed parent C yields C→U collateral movement, an
explicit later sync uses destination C and cutoff U, replays only U..child,
then records C after verified success.

Doctor renders sanitized raw/effective/source/validity/reason cutoff evidence
for evaluated rows. Unevaluated rows claim no decision. Missing-record parent
advancement requires known-history repair; do not recommend a plain rebase or
ordinary sync that the cutoff preflight will refuse.

Validators are ref/checkout-read-only. If validation commits, rebases, resets,
moves refs, switches branches, or detaches `HEAD`, preserve the journal and
work and require manual recovery; tws must not adopt or erase those changes.

The first push **attempt** is the publication boundary. Once recorded,
recovery is forward-only with `--continue`; never recommend `--abort`. Older
recovery with unknown remaining replay refuses continuation, while safely
completed publication/cleanup phases still recover under their ownership
locks. Once all forward effects succeed, durable completion
precedes pin deletion; subsequent recovery only finishes cleanup without
rolling back refs or metadata. Successful no-flag sync behavior remains unchanged.

If external `stack.yaml` is absent, the legacy compatibility sync requires an
interactive warning and confirmation or `--allow-nontransactional`. It has no
complete rollback and earlier branches may remain moved. Noninteractive use
refuses by default; malformed or unreadable metadata always refuses.

`--plan` describes the rebase a run would perform and exits without moving a branch, rewriting a working tree, or writing tws state — but it still fetches exactly where the run it describes fetches: an external plan fetches by default, a checkout plan only under `--fetch`, and `--plan --continue` never fetches, so `--plan --no-fetch` previews a different, fully local route. Read `entries[]` for each row's old base, new base, and `candidates` count — an upper bound, never a promise of what gets applied — before deciding to execute, and decide from `runnable && !guard.would_refuse && guard.execute_blocked_by == [] && refusal.kind == null`, never from `--plan`'s own exit status, which is `0` even for a refusal. `--max-replay-per-entry <n>` and `--max-replay-total <n>` bound this invocation's replay work and refuse before rebasing if exceeded; `--approve-plan <fingerprint>` re-supplies the fingerprint `--plan` printed (extract it with `sed -n 's/^Approval fingerprint: //p'`, never `tail -1`) and requires at least one of those limits — a plan paired with `--approve-plan` but no limit mints no fingerprint, which is a documentation bug, never a valid workflow. A guarded refusal exits `1` and writes exactly one `plan-guard: <kind>: <detail>` line on stderr, with a `state-preserved: ` prefix meaning something on disk outlives the refusal; a refusal tws already performs (dirty tree, held lock, unresolvable base, incomplete previous run) keeps its own wording and is never marked. A guarded run's limits are recorded in recovery state, so an older tws release refuses to resume it instead of silently dropping the guard.

`tws open <feature> <branch>` runs the configured agent in the repository root and restores the original branch after the agent/follow-up shell exits. `--tmux` keeps the branch owned by a recorded tmux session until `tws close`. Only one checkout session may own the repository; `--all` and automatic hooks remain unsupported.

## Global Workspace Registry

Opt-in discovery index under the XDG data directory. It never owns, moves, or deletes repositories/workspaces.

```sh
tws registry add /path/to/repo --alias rp
tws init --register --register-alias rp    # enroll after a successful init
tws registry list --json                   # array output; empty is []
tws registry check                         # ok / missing / mismatched / invalid
tws registry repair rp /new/path           # moved target: no extra flag needed
tws registry remove rp                     # metadata only; files are untouched
tws registry prune --missing --force       # --force required in non-TTY use
```

Selectors are exact ID, alias, or canonical path — never guess fuzzy names. An alias may not shadow an entry ID or a registered path.

Enrollment writes a small opaque marker file in tool-owned metadata. Git-backed targets use `.git/tws/workspace-id`; checkout mode and linked worktrees share the main repository's Git common directory. External workspaces use `.tws-workspace/workspace-id`. Identity survives moves and workspace-mode switches and detects replacement. `tws registry check` is read-only. Use `--allow-identity-change` on repair only when the target was intentionally replaced.

## Workspace Sibling Links

Learning notes, ticket stores, patch metadata, research, and authored docs live in sibling directories that `tws` locates but never owns. Discover them by command; never hard-code a path.

```sh
tws space list --json --all                # the complete registry; [] when empty
tws space list --json                       # cwd-scoped: workspace-wide + detected feature
tws space list --json --feature <feature>  # workspace-wide links plus that feature's
tws space show <name> --json               # add --workspace or --feature <f> if ambiguous
tws space add learning ./learning --kind learning --description "notes"
tws space remove learning --workspace      # drops the link; never deletes the target
```

Use the `resolved_path` field of the JSON output. `status: missing` and `scope_status: feature-missing` are reports, not repairs. An empty result is normal on a fresh clone — ask or register a link instead of guessing. A bare `tws space list` is scoped to the current directory (workspace-wide links plus the detected feature); use `--all` for the complete view. A registered directory is never a feature: feature commands and `tws migrate-layout` refuse that name by filesystem identity — including a different letter case or absolute spelling — and it is excluded from `tws list`. When a name exists in two scopes, disambiguate `show`/`remove` with `--workspace` or `--feature <feature>`. A malformed or future-schema `spaces.yaml` makes feature and space commands fail loudly; fix the file rather than working around it.

## Agent Work Status

`tws status [feature] [--json]` projects what tws knows about each logical branch. With no argument it builds every feature in the resolved workspace, from any working directory. With a feature argument it builds only that feature plus genuine workspace evidence, including the shared checkout session even when it belongs to another feature; unrelated feature stacks, worktrees, and external session records are not read.

Git/tmux subprocesses are read-only, capped at five seconds each, and share a thirty-second budget beginning with workspace/config resolution. Failed or timed-out facts are `null`/`unknown` with issues rather than clean or absent. Ordinary filesystem reads cannot be interrupted by that subprocess budget.

Two axes are never collapsed: `runtime_presence` (`present|absent|stale|unknown`) answers "is a tws-owned runtime alive?", and `agent_state` (`working|ready|blocked|done|unknown`) answers "what is the agent doing?". **`agent_state` is always `unknown` at this version; use `needs_attention`.** **`attention.status` inherits upward: a workspace or feature can be `needs_attention` with `issue_count: 0` because a child is — read `report.issues[]` for the detail.** **A `present` from tws means a process with that PID exists, not that that exact process exists.**

Exit status is 0 whenever a report was produced, including when branches need attention; a non-zero exit means no report could be produced at all. `tws status` is strictly read-only and tws never kills a direct agent process. The human view prints the workspace verdict in its header and every issue in a `Branch:`/`Feature:`/`Workspace:` block, so a `[!] attn` row always shows its guidance without `--json`.

```sh
tws status
tws status auth --json | jq '.issues[] | select(.severity=="warning")'
```

## Workflow

0. Run `tws status --json` and act on `.workspace.attention.status == "needs_attention"` plus `report.issues[]`; never act on `agent_state`
1. Run `tws list` to see current state
2. Run `tws decisions show <feature>` to check for unread decisions from siblings
3. Run `tws stack <feature>` to understand dependencies
4. Run `tws stack status <feature> --json` before syncing and check `ancestry.status` and `materialization.dirty`. `tws stack status` never fetches; upstream and parent counts describe local refs only. A null field means tws could not establish the fact locally — it never means clean, attached, zero, or no upstream. `tws stack -- status` prints the legacy tree for a feature literally named `status`
5. Use `tws sync <feature>` to keep branches up to date
6. Use `tws sync <feature> --push` to sync and push in one command
7. After breaking changes, run `tws decide <feature> "summary" --type breaking`
8. Run `tws doctor` if something seems wrong
9. Use `tws archive` to free disk space, `tws new` to restore
10. Set `test_command` in config for automatic validation after rebase
