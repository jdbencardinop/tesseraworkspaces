# tws cheat sheet

## Install

```sh
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest

# Enable shell completions (zsh)
tws completion zsh > $(brew --prefix)/share/zsh/site-functions/_tws
```

## Quick start (one command)

```sh
cd ~/projects/myapp

# Create feature + branch + open agent — all in one
tws add auth -n auth-models --open

# With an existing remote branch
git fetch origin
tws add auth -n feature/auth-api --open

# With a custom template
tws add auth -n auth-models --template ~/templates/go-project --open

# Without opening (just setup)
tws add auth -n auth-models
```

## Create branches (stacked diffs)

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-routes --base auth-middleware        # stacks on auth-middleware
tws new auth auth-tests --base auth-models             # diverges (parallel to middleware)
tws new auth release-check --base origin/release        # explicit remote ref
tws new auth wiki-docs --repo ../wiki --base master     # local master in wiki repo

# Result:
# (<default>)
# └── auth-models
#     ├── auth-middleware
#     │   └── auth-routes
#     └── auth-tests
```

## Migrate an existing branch

```sh
tws new auth my-existing-branch                   # auto-detects existing branch
tws new auth main --force                         # force if already checked out
```

## Work in a worktree

```sh
tws open auth auth-models              # cd + run agent (default)
tws open auth auth-models --tmux       # wrap in tmux session
tws open auth auth-models --no-agent   # just print the path
tws open                               # interactive picker (fzf if available)
tws open auth                          # pick branch within feature
tws open auth --feature-dir            # feature orchestrator directory
tws open auth --all                    # tmux: orchestrator + one window per worktree
```

## Decisions (cross-worktree communication)

```sh
# Record a decision (broadcast to all worktrees)
tws decide auth "Changed User.ID from string to uuid" --type breaking

# Record a targeted decision (only for a specific branch)
tws decide auth "Review the API surface" --type review --to auth-middleware

# Add details
tws decide auth "Added UserRepository" --type info \
  --details "Use internal.UserRepository instead of direct DB calls"

# View unread decisions (default — only shows new ones)
tws decisions show auth

# View all decisions (including already read)
tws decisions show auth --all

# Filter by source branch
tws decisions show auth --branch auth-models

# Show only decisions relevant to your branch
tws decisions show auth --mine

# Mark all as read
tws decisions ack auth
```

Decision types: `breaking` | `info` | `deprecation` | `review` | `question`

When you `tws open`, unread decisions are shown automatically:
```
  2 new decision(s) (1 for you) (run: tws decisions show auth)
```

## See what you have

```sh
tws list                     # all features and branches
tws stack auth               # dependency tree for a feature
tws stack status auth        # per-entry ancestry, materialization, upstream, parent counts
tws stack status auth --json | jq '.entries[] | select(.ancestry.status!="current")'
tws doctor auth              # health checks (branch mismatch, dirty, ancestry, etc.)
```

`tws stack status` is local-only: it never fetches, so upstream state and parent
counts describe local refs only. A `null` field means tws could not establish the
fact locally — it never means clean, attached, zero, or no upstream. Any
reportable state exits 0, including `stale`, `divergent`, `missing`,
`cross-repo-unsupported`, and `unevaluated`. For a feature literally named
`status`, `tws stack -- status` prints the legacy dependency tree and
`tws stack status status` reports its stack status.

Ancestry is reported per configured parent-child edge as `current`, `stale`,
`divergent`, `missing`, `cross-repo-unsupported`, or `unevaluated`, with a reason
and guidance when one applies. It is read-only, never fetches, and never changes
the exit status: `stale` means the parent moved (run `tws sync`), `divergent`
means the recorded base commit left the parent's history so an `--onto` rebase is
needed, and `cross-repo-unsupported` means the entry targets another repository
that tws does not probe.

## Check agent status

```sh
tws status                   # every feature, from any directory
tws status auth              # filter to one feature
tws status --json | jq '.issues[] | select(.severity=="warning")'
```

`needs_attention` is the authoritative field and inherits upward: a workspace or
feature can be `needs_attention` with `issue_count: 0` because a child is — read
`report.issues[]` for the detail. The human view prints the workspace verdict in
its header and every issue in a `Branch:`/`Feature:`/`Workspace:` block, so a
`[!] attn` row always shows its guidance. `agent_state` is always `unknown` at
this version. Exit status is 0 whenever a report was produced.

## Sync (rebase in dependency order)

```sh
tws sync auth                # fetches, then rebases parent→child
                             # if auth-models fails, middleware+routes are skipped
                             # archived branches synced via --update-refs or optimistic rebase
```

### Sync modes

Three independent axes; no flags keeps today's behaviour exactly.

```sh
tws sync auth --only auth-models     # scope: exactly one stack entry (logical name)
tws sync auth --from auth-models     # scope: that entry plus its descendant subtree
tws sync auth --local-only           # propagation: replay local parent tips only
tws sync auth --full                 # propagation: advance anchors onto their base (default)
tws sync auth --no-fetch             # input refs: no automatic network input
tws sync auth --fetch                # input refs: fetch first (external default)
```

| Axis | Flags | External default | Checkout default |
|---|---|---|---|
| fetch | `--fetch` / `--no-fetch` | `fetch` | `no-fetch` |
| propagation | `--full` / `--local-only` | `full` | `full` |
| scope | `--only` / `--from` | `all` | `all` |

- Selectors are logical `stack.yaml` names, never Git branches.
- `--no-fetch` is an input-ref policy, not an offline mode: `--push` is still allowed.
- A scoped run drops `--update-refs`, so unselected branches never move.
- `--only`/`--from` on an archived entry is refused; on an unmaterialized entry it is allowed.
- Trigger flags on `--continue` require v2 state; against legacy or absent state they are refused.
- `--abort` cannot be combined with a mode flag: abort is defined by the persisted run.
- Every new ordinary-sync `--push` is strict: the run stops at the first rejected push and `--continue` retries only the entries that were never pushed. A `scope=all` run still pushes the whole feature; only standalone `tws push` retains lenient failure behavior.
- Checkout `--fetch` refreshes remote-tracking refs once, before the plan is built and before the transaction exists. It is best-effort and deliberately **not** resumable: an interrupted refresh leaves no transaction, so the same command simply re-runs.
- A checkout sync must be run from the repository checkout or any subdirectory of it. A linked worktree of that repository is refused (`checkout sync operates on <repo> but the current directory belongs to working tree <other>`), so a sync can never mutate the wrong working tree.
- `tws push` is an **external-mode** command. In a checkout workspace it still refuses with `linked worktrees are not supported in checkout mode`; push checkout branches with `tws sync <feature> --push`.
- New ordinary sync recovery snapshots exact selected branch tips and exact
  pre-run `stack.yaml` bytes before branch mutation. Abort uses per-repository
  compare-and-swap ref updates and refuses if later user work moved a ref;
  refs, metadata, and checkout/worktree state are separate effects, and
  multi-repository rollback is not atomic as one unit.
- Immediately before the first push **attempt**, sync records publication.
  Recovery is then forward-only with `--continue`; do not offer `--abort`.
  Older recovery state has no complete snapshot and warns that abort cannot
  restore every earlier movement. If native Git reflog attribution cannot
  prove a changed allowed ref came from tws's rebase, recovery refuses.
- After every forward effect succeeds, sync records completion before deleting
  protection refs. A late recovery verb only finishes cleanup and reports the
  already-completed run; it does not roll back refs or metadata.
- Validators are ref/checkout-read-only. If validation moves a ref, switches or
  detaches `HEAD`, or otherwise changes checkout identity, preserve the journal
  and work and recover manually; tws will not adopt or erase those changes.
- An absent external `stack.yaml` may use the historical nontransactional path
  only after interactive confirmation or `--allow-nontransactional`.
  Noninteractive default is refusal, complete rollback is unavailable, and an
  existing malformed or unreadable file never qualifies.
- The consented missing-stack path still refuses any recovery evidence that
  appears after its guard claim. `--push` requires stack metadata.
- If a valid transactional journal lacks only its compatibility marker,
  recovery follows the journal's phase and restores the marker exclusively.
  Mixed legacy/transactional records require manual inspection, never deletion.

### Plan and guard

```sh
tws sync auth --plan                                   # preview only: no branch moves, no state is written
tws sync auth --plan --max-replay-per-entry 10          # bound the previewed run's replay work
tws sync auth --max-replay-per-entry 10 --approve-plan <fingerprint>   # execute the approved plan
```

Two full round trips — always pair `--approve-plan` with the same limit the plan carried:

```sh
# legacy external route — the run a bare `tws sync auth` performs
tws sync auth --plan --json --max-replay-per-entry 10
tws sync auth --approve-plan <fingerprint> --max-replay-per-entry 10

# explicit new-mode no-fetch route — stable and fully local
tws sync auth --plan --no-fetch --json --max-replay-per-entry 10
tws sync auth --no-fetch --approve-plan <fingerprint> --max-replay-per-entry 10
```

- `--plan` renders each entry's old base, new base, and `candidates` count and exits before rebasing anything — but it still fetches exactly where the run it describes fetches: an external plan fetches by default, a checkout plan only under `--fetch`, and `--plan --continue` never fetches. `--plan --no-fetch` previews a different, fully local route.
- `candidates` is an upper bound, never a promise of what gets applied.
- `--max-replay-per-entry <n>` / `--max-replay-total <n>` bound only this invocation's work and refuse before rebasing if exceeded; they are never cumulative across resumes.
- `--approve-plan <fingerprint>` re-supplies the 64-hex fingerprint `--plan` printed, and requires at least one of those limits on every route, `--plan` included. A plan paired with `--approve-plan` but no limit mints no fingerprint — that pairing is a documentation bug, never a valid workflow.
- Extract the fingerprint explicitly — `sed -n 's/^Approval fingerprint: //p'` — never pipe `tail -1` into `--approve-plan`.
- Admission for the guarded run is one predicate: `runnable && !guard.would_refuse && guard.execute_blocked_by == [] && refusal.kind == null`. Branch on it, never on `--plan`'s own exit status — a plan-only run exits `0` even when it describes a refusal.
- A guarded refusal exits `1` and writes exactly one `plan-guard: <kind>: <detail>` line on stderr; a detail beginning `state-preserved: ` means something on disk outlives the refusal. A refusal tws already performs — a dirty tree, a held lock, an unresolvable base, an incomplete previous run — keeps its own wording, exits `1`, and is never marked.
- A guarded run's limits are recorded in recovery state, so an older tws release refuses to resume it rather than silently dropping the guard.

## Reparent a branch onto a new base

```sh
tws stack reparent auth auth-middleware --onto main --plan --max-replay-total 20            # preview, exits 0
tws stack reparent auth auth-middleware --onto main --plan --json --max-replay-total 20     # same document, machine readable
tws stack reparent auth auth-middleware --onto main \
  --approve-plan <fingerprint> --max-replay-total 20                  # execute
tws stack reparent auth --continue                                    # resume after a conflict
tws stack reparent auth --abort                                       # roll back
```

- A **fresh execution is always guarded**: it requires `--approve-plan` plus at
  least one of `--max-replay-per-entry` / `--max-replay-total`. There is no
  unguarded reparent route.
- The preview and execution carry the same replay limit flag(s) and values.
  A limitless preview publishes a null fingerprint and cannot be executed.
- A reparent preview moves no branch and writes no tws state; may fetch according
  to policy.
- `--onto-kind auto|entry|ref` decides how `--onto` is read; `auto` prefers a
  sibling stack entry and falls back to a ref. A stack-entry destination stores
  the logical entry name; a named literal ref stores its full `refs/...` name;
  a raw object-id destination stores the full lowercase OID.
- `--cutoff <ref>` supplies the target boundary only when no authoritative
  recorded `LastBaseSHA` exists. It may confirm that recorded SHA, but cannot
  override a different or unresolvable record. Descendant cutoffs are always
  snapshotted per row and never overridden.
- A plan fetches exactly where the run it describes fetches: external by
  default, checkout only under `--fetch`. `--continue` and `--abort` never
  fetch and refuse every fetch flag.
- The ref commit is **race-atomic**: every moved branch lands in one
  compare-and-swap transaction. It is **crash-atomic only on the reftable
  backend**.
- Three effects — refs, `stack.yaml`, worktree/index state — and one commit point,
  which requires both durable post-image metadata and refs already at their
  planned values (or a no-op). Recovery past that point is **forward-only**.
- `--continue` and `--abort` take the target, destination, cutoff, policy,
  limits, validation command and closure from persisted state; supplying any of
  them is refused before any lock or Git command.
- A refusal is one anchored line: `reparent: <kind>: <detail>`. A detail
  beginning `state-preserved: ` means something on disk outlives the refusal. A
  conflict is a **pause**, not a refusal, and carries no marker.
- While a reparent is recorded, `tws sync <feature>` (every verb, `--plan`
  included) refuses. In checkout mode its single physical checkout means the
  run blocks opening every feature in that workspace; in external mode only
  direct/tmux/all sessions on the target and affected descendants block
  admission, with launch intent published before the final mutation check.
- Checkout sync and reparent share a workspace-global mutation lock across
  features, and checkout refuses every non-empty stack-entry `repo`.
- Checkout `tws open <feature> --feature-dir` publishes the same workspace
  launch intent, repeats the final mutation/reparent/session check, and keeps
  the intent through the agent and shell.
- Dead checkout launch intents are removed only after that global lock is
  held. Recovery never infers commit from an exact pre-image ref and never
  redetaches a restored holder that the operator switched to another branch.
- A real top-level external push holds the feature mutation lock across its
  entire multi-entry invocation. Obsolete/archived follow-up rows clear before
  lease preflight; `--dry-run` evaluates that clearing in memory only.
- Every mutating external sync route holds that feature lock through rebase,
  metadata, optional push, and remote follow-up clearing; it rechecks reparent
  state after claiming and releases last.
- If the checkout-global lock is absent, current tws scans every feature's
  recoverable checkout sync/reparent state before a fresh mutation; recovery
  reconstructs only its own reservation.
- Different stored `repo` spellings on one logical edge are refused even when
  they resolve to one common directory; preexisting validation untracked paths
  are allowed, but new untracked paths or tracked changes refuse. Abort journals
  remote-record restoration before changing the record.
- v1.2.16 only fails closed for same-feature sync after the compatibility
  envelope exists; it cannot see window 1, global locks, unrelated-feature
  checkout reparent, or top-level push. **Do not use an older tws while any
  reparent is active or recoverable.**
- tws changes **no remote ref and no pull request**. The next push warns and
  pushes with `--force-with-lease --force-if-includes`; a real push is refused
  until the reparent commit point is proven. If a newer run temporarily cannot
  resolve the tracking ref, it carries forward older same-branch publication
  evidence until a positive local clear.

## Archive and restore

```sh
tws archive auth auth-middleware   # remove worktree, keep branch ref
tws new auth auth-middleware       # restore (idempotent, no stack.yaml duplicate)
```

## Clean up

```sh
tws delete auth              # removes all worktrees + feature dir
tws close auth auth-models   # kill tmux session for a worktree
```

## Context injection

```sh
# Files in inject/ are symlinked into every worktree
ls ../myapp.tws/auth/inject/       # CLAUDE.local.md, .claude/skills/, etc.

# Re-sync after adding new files to inject/
tws inject auth                    # all worktrees
tws inject auth auth-models        # single worktree

# Backfill templates into existing features
tws template sync auth --template ~/templates/base
tws template sync --all            # all features
```

## Configuration

```sh
tws config show                              # show resolved config
tws config set agent_command opencode        # change agent globally
tws config set use_tmux true --repo          # per-repo setting
tws config get agent_command                 # check current value

# Config files:
#   Global: ~/.config/tws/config.yaml
#   Per-repo: .tws/config.yaml
#   Env override: TWS_ROOT=/custom/path
```

## Rename

```sh
tws rename feature old-name new-name               # rename feature
tws rename branch auth old-branch new-branch        # rename branch + update refs
```

## Agent skills

```sh
tws init                        # install Claude + Copilot skills
tws init --agent claude         # Claude only
tws init --agent copilot        # Copilot only
tws init --force                # overwrite existing
```
