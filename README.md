# tesseraworkspaces

A CLI tool for creating feature-scoped workspaces with multiple git worktrees. Work on parallel branches or stacked diffs within a single feature, each with its own coding agent. Built for teams and solo developers who use AI coding agents in parallel.

## Quick Start

```sh
# Install
go install github.com/jdbencardinop/tesseraworkspaces/cmd/tws@latest

# Create a feature with a branch and start coding
cd ~/projects/myapp
tws add auth -n auth-models --open
```

## How it works

1. **Add a feature** — creates a workspace with shared context and inject files
2. **Create worktrees** — spin up isolated branches, stacked or parallel
3. **Open a worktree** — launch your coding agent in the worktree directory
4. **Decide** — broadcast design decisions to sibling worktrees
5. **Sync** — rebase all branches in dependency order, amend-aware
6. **Push** — push all branches with `--force-with-lease`

## Features

### Stacked & Divergent Branches

```sh
tws new auth auth-models                              # selected repo's origin/HEAD
tws new auth auth-middleware --base auth-models        # stacks on auth-models
tws new auth auth-tests --base auth-models             # parallel to middleware
tws new auth release-check --base origin/release        # explicit remote ref
tws new auth wiki-docs --repo ../wiki --base master     # local master in wiki repo

# Result:
# (<default>)
# └── auth-models
#     ├── auth-middleware
#     └── auth-tests
```

### Cross-Worktree Agent Communication

Agents in different worktrees can communicate via decisions:

```sh
tws decide auth "Changed User.ID to uuid" --type breaking
tws decide auth "Review API surface" --type review --to auth-middleware
tws decisions show                  # auto-detects feature, shows unread only
tws decisions ack                   # mark as read
```

With hooks installed, Claude Code agents see new decisions automatically on session start:

```sh
tws hooks install auth              # install on all worktrees
tws config set auto_hooks true      # auto-install on every tws new
```

### Smart Sync

```sh
tws sync auth                       # quiet fetch + rebase in dependency order
tws sync auth --push                # sync + push all branches
tws sync auth --continue            # resume after conflict resolution
tws sync auth --abort               # discard sync state

tws sync auth --only api            # sync exactly one stack entry
tws sync auth --from api            # sync one entry and its descendant subtree
tws sync auth --local-only          # replay local parent tips; never advance an anchor
tws sync auth --no-fetch            # plan from local refs; no automatic network input
tws sync auth --fetch --full        # name both axes explicitly (external defaults)

tws sync auth --plan                                    # preview: old base, new base, candidates per entry
tws sync auth --plan --max-replay-per-entry 10          # bound the previewed run's replay work
tws sync auth --max-replay-per-entry 10 \
  --approve-plan <fingerprint>                          # execute the approved plan
```

**Sync modes.** Three independent axes select what a run does. `--fetch` /
`--no-fetch` chooses the *input-ref* policy (external defaults to `fetch`,
checkout to `no-fetch`); `--full` / `--local-only` chooses propagation; `--only`
/ `--from` chooses scope. Successful `tws sync <feature>` runs retain their
existing no-flag behavior; recovery now preserves rollback evidence. Notes:

- `--no-fetch` means "no automatic network **input**" — not "offline". An
  explicit `--push` is still allowed and is the only way a `no-fetch` run
  reaches the network.
- `--local-only` never advances a root from its remote base; selecting only an
  anchor is a no-op success, not an error.
- A scoped run drops `git rebase --update-refs`, so it cannot move a branch
  outside the selection.
- Every selected row is preflighted before branch mutation. A recorded
  `last_base_sha` must resolve, be an ancestor of the child, and not predate
  history already shared by parent and child. When it is absent, tws freezes
  the current parent tip only if Git proves that tip is an ancestor of the
  child. It never substitutes a merge-base, fork point, reflog entry, or
  arbitrary ancestor.
- New branches record the exact full commit used for creation. Existing,
  restored, and imported branches preserve their evidence or remain unknown.
- Materialized, archived, and actual `--update-refs` collateral branches
  refresh `last_base_sha` only after verified Git success. Failed and untouched
  rows retain their previous metadata.
- Incompatible combinations are refused before any fetch, lock, or rebase.
- Every new ordinary-sync `--push` is strict: the run stops at the first
  rejected push, keeps its recovery state, and `tws sync <feature> --continue`
  retries only the entries that were never pushed. A `scope=all` run and
  no-flag `tws sync --push` still push the whole feature, but preserve recovery
  state on failure. Standalone `tws push` keeps its lenient per-entry behavior.
- Every mutating external sync route, including the no-flag route, holds the
  shared feature mutation guard through rebase, metadata, optional push, and
  remote-follow-up clearing. Concurrent mutation of one feature is refused.
- Downgrading in the middle of a scoped run: an older tws fails closed on plain
  sync and on `--continue`. Downgrading *after* an explicit old `--abort` is
  unsupported.

**Plan and guard.** Run `--plan` before a wide sync that could rebase several
branches at once, and read its `entries[]` rows first. `--plan` describes the
rebase this invocation would perform and exits: it moves no branch, rewrites
no working tree, and writes no tws state — but it is not Git-write-free. A
plan fetches exactly where the run it describes fetches: an external plan
fetches by default, a checkout plan only under `--fetch`, and `--plan
--continue` never fetches — so `--plan --no-fetch` previews a **different**,
new-mode route, not the one a bare `tws sync <feature>` takes. Bare `--plan`
(no other flag) describes exactly the no-flag run. Its `candidates` counts are
an upper bound on what a guarded run might replay, never a promise of what
gets applied.

Each `entries[].cutoff` reports the raw recorded value, its resolved commit,
the frozen `effective_sha`, `source`, `validity`, and `reason`. Human plans
render the recorded and effective cutoffs separately. A cutoff refusal is a
hard safety blocker; an approval token or replay-limit waiver cannot override
it. Read-only plans do not repair metadata.

For selected branches moved collaterally by external `--update-refs`, sync
captures that row's own canonical parent in the correct repository immediately
before the Git action. Local branch parents use their verified action
postimage when they move with the action. Nonmoving local, remote-tracking,
tag, and object parents record the exact executed `--onto` transition only
after their action-time commit is proven to be contained by that destination
and their frozen old cutoff exactly matches the primary action's replay
cutoff. A cutoff inside that replay range needs a moving local parent's exact
`AfterRefs` counterpart; otherwise sync refuses before the rebase instead of
moving the branch and guessing. The recorded transition remains an old cutoff,
not a replacement configured parent: if tag/OID parent C produced collateral
transition C→U, a later explicit sync plans and runs `--onto C U`, replays only
U..child, and records C only after success.

Scoped and local-only external rebases, archived external rebases, and all
checkout rebases invoke `git -c rebase.updateRefs=false rebase`.
Repository-local or inherited `rebase.updateRefs=true` therefore cannot
re-enable collateral movement, while these routes remain compatible with Git
2.26-2.37. Full external actions intentionally using `--update-refs` still
require Git 2.38. Their moved selected collateral metadata and durable
completion progress are recorded with the primary action before execution
advances.

Doctor prints sanitized raw/effective/source/validity/reason cutoff evidence
for every evaluated stack edge, abbreviating object IDs in prose; structured
reports retain full values. Unevaluated edges do not claim a cutoff
decision. A missing record with an advanced parent is reported as a
known-history repair requirement; doctor no longer promises a plain rebase
that sync would refuse.

`--max-replay-per-entry <n>` and `--max-replay-total <n>` refuse before
rebasing if this invocation would replay more candidates than the bound, for
one entry or in total — the bound applies to this invocation only and is
never cumulative across resumes. `--approve-plan <fingerprint>` re-supplies
the 64-hex fingerprint `--plan` printed and requires at least one of those two
limits, `--plan` included; a workflow that mints or presents a limitless
fingerprint is a documentation bug, never a valid one. Extract the fingerprint
explicitly — e.g. `sed -n 's/^Approval fingerprint: //p'` — never pipe
`tail -1` into `--approve-plan`.

A guarded run's limits are recorded in recovery state, so an older tws release
refuses to resume it instead of silently dropping the guard. A guard refusal
exits `1` and writes exactly one `plan-guard: <kind>: <detail>` line on
stderr; a detail beginning `state-preserved: ` means something on disk
outlives the refusal. A refusal tws already performs — a dirty tree, a held
lock, a base that does not resolve, an incomplete previous run — keeps its own
wording, exits `1`, and is never marked. `--plan` itself exits `0` even when
it describes a refusal, so decide whether to execute from the plan's own
fields, never from that exit status.

Reparent downgrade protection is narrower: v1.2.16 same-feature sync
plain/continue/abort fails closed only after the compatibility envelope exists.
It cannot see artifact-before-compat crash window 1, workspace-global locks,
unrelated-feature checkout reparent, or top-level push. **Do not use an older
tws while any reparent is active or recoverable.**

**Transactional sync recovery.** Before the first selected-branch mutation, a
new ordinary sync records the exact selected branch tips, exact pre-run
`stack.yaml` bytes, repository identity, and checkout/worktree holders, and
protects the branch preimages from Git GC. `--abort` restores only changes
still attributable to that run, using compare-and-swap ref updates per
repository; Git refs, metadata, and holder state remain separate effects, and
a multi-repository rollback is not cross-repository atomic. If a branch gained
later user work, abort refuses rather than overwriting it. Where native Git
reflog evidence cannot prove that an allowed ref move came from tws's rebase,
recovery refuses conservatively.

Validation commands are ref/checkout-read-only. Builds and tests may leave
ordinary ignored or untracked outputs, but a validator that commits, amends,
rebases, resets, moves a ref, switches branches, or detaches `HEAD` stops the
run with its journal and work preserved for manual recovery; tws does not adopt
or automatically roll back those unproven changes.

Immediately before the first push **attempt**, sync persists a publication
marker bound to the recorded repository, destination ref, and source object
ID. From that point recovery is forward-only: use `--continue`; `--abort`
will not rewrite local or remote refs. Runs created by older tws versions have
no complete selected-ref/metadata snapshot, so their legacy abort path warns
that it cannot fully restore earlier branch movement. Once all forward effects
succeed, sync durably records completion before removing GC-protection refs.
After that decision, either recovery verb only finishes cleanup and reports
that sync already completed; neither rewinds branches or metadata. Successful
no-flag runs retain their established behavior and output.

If external `stack.yaml` is genuinely absent, the historical compatibility
sync is available only after an interactive warning and confirmation, or with
`--allow-nontransactional` for automation. This deliberate exception has no
complete rollback, so earlier branches may remain moved after a later failure.
Noninteractive execution refuses by default, and an existing unreadable or
malformed `stack.yaml` always refuses. Restore or create stack metadata to use
transactional recovery. Publishing with `--push` requires stack metadata.
The consented fallback rechecks recovery evidence under its mutation guard and
never switches into a stack-based executor if metadata appears concurrently.

A valid transactional record remains authoritative if only its compatibility
marker is missing: recovery restores that marker without replacing any existing
file, then follows the recorded phase. Conflicting legacy and transactional
records must both be preserved for manual inspection; do not delete either.

- **Amend-aware** — uses `--onto` to avoid ghost conflicts from amended commits
- **Archived branch support** — syncs archived branches via `--update-refs` or optimistic rebase
- **Post-rebase validation** — run `test_command` after each rebase (e.g., `go build ./...`)
- **Conflict recovery** — saves state, guides resolution, resumes with `--continue`

### Context Injection

In external mode, shared files in `inject/` are symlinked into every
materialized linked worktree. Feature-wide injection follows logical
`stack.yaml` names at any depth, so entries such as `review/pr-123` receive the
files without writing into the intermediate `worktrees/review` directory:

```sh
# Edit once, all worktrees see changes
echo "# Auth context" > ../myapp.tws/auth/inject/CLAUDE.local.md

# Re-sync after adding new files
tws inject auth

# A slash-containing logical name can also be selected explicitly
tws inject auth review/pr-123

# Target a gitignored subdirectory
tws inject auth --into .context
```

Checkout mode has one physical checkout and no linked worktrees, so both
feature-wide and branch-specific `tws inject` commands refuse explicitly.
Existing destination files are preserved.

### Named, Role-Aware Templates

Named template packages are copied into a registry, then selected independently
for worktree and orchestrator guidance:

```sh
# Register an independent copy of a package
tws template add review ./review-template            # global
tws template add review ./review-template --repo     # owning repository
tws template list
tws template show review                              # repo entry wins
tws template show global:review                       # explicit origin

# Select one package for both roles, or select roles independently
tws add auth --template review --agent copilot
tws add auth \
  --worktree-template global:worktree-base \
  --orchestrator-template repo:orchestrator-base \
  --agent claude

# Refresh saved selections, or change only one role explicitly
tws template sync auth
tws template sync auth --scope worktree --worktree-template global:new-base
```

Packages contain `inject/` assets for worktree context and `feature/` assets
for the feature directory. Optional `template.yaml` version 1 supplies literal
role guidance and skill **references**; tws never executes package content or
copies skill implementations. Registry storage is separate from legacy
templates:

- global: `~/.config/tws/template-registry/<name>/`
- repository: `.tws/.template-registry/<name>/`

An unqualified name prefers the repository entry. Saved selections retain
their resolved `repo:` or `global:` origin plus an opaque registry identity, so
a later shadowing entry or same-named registry under another HOME/repository
cannot silently retarget a feature. Moving a repository together with its
registry preserves that identity. Removing a registry entry never removes
feature files; a feature whose saved source disappeared requires explicit
reselection.

Managed rendering writes native guidance:

| Role | Claude | Copilot |
|---|---|---|
| worktree | `inject/CLAUDE.local.md` | `inject/.github/instructions/tws-worktree.instructions.md` |
| orchestrator | `CLAUDE.md` | `AGENTS.md` |

`--agent` selects rendering only; it does not change `agent_command`. If the
renderer was explicit, tws honors it while the recorded launch command remains
unchanged—even for a supported renderer different from the launcher or for a
custom wrapper. A later launch-command change requires explicit rerendering
instead of silently switching native guidance.

Managed worktree guidance requires repository-root injection. A non-root
`inject_into` or `--into` remains supported for legacy/user inject files, but a
managed worktree operation refuses it; orchestrator-only sync is unaffected.
For `tws inject`, an explicit `--into .` is the supported one-run override of a
configured non-root `inject_into`; an explicit non-root flag is still refused.
Checkout mode keeps durable files under `.tws/features/<feature>` and exposes
worktree context only through the existing session-owned temporary links.

Managed state lives in feature-local `.tws-templates.json`. tws replaces or
removes only regular files and external links whose recorded hash, mode,
filesystem identity, exact Git-worktree identity, source mapping, and link
target still match. Same-content files without ownership metadata,
tracked paths, and symlinks are never adoption candidates. Unacknowledged
modified owned files refuse by default; the resolver below is an
explicit, text-only exception. When an update would retire unchanged owned paths, tws prints every
feature, role, kind, and exact path, then asks default-No:

```text
Remove these unchanged TWS-managed files and apply the template update? [y/N]
```

Only `y`/`yes` proceeds. Automation must pass
`--allow-managed-removal`; the full warning is still printed, and the flag
never authorizes modified or unmanaged content. `--scope worktree`,
`--scope orchestrator`, and `--scope both` preserve unselected role state.

Directory-valued `--template` arguments remain the legacy copy-if-missing
route, including repeated first-existing-file-wins layering and custom
injection targets. Legacy features are not adopted implicitly, and directory
inputs cannot be mixed with named/role selectors. New features may opt into
managed mode through `worktree_template` and `orchestrator_template` defaults.
An explicit directory input remains legacy even when those defaults exist.

External managed creation and later new/open/inject/sync operations keep one
execution-selected feature root, including the documented `TWS_ROOT` override.
A verified link-sync no-op may reopen or attach to an existing session; actual
managed writes or link changes remain blocked by live sessions and by any
ordinary sync recovery evidence.

Managed mutations pin the selected package, feature, registry, and worktree
roots. New leaves are published without replacement. Before replacing or
removing an existing owned object, tws moves it into a private same-filesystem
`.tws-template-recovery/` area and records pending evidence. Successful
ownership publication triggers cleanup only after the old copy is reverified;
any mismatch or failure preserves the evidence and reports its exact path.
Retries fail closed while that evidence remains. This is not a multi-file
transaction and cannot preserve later writes made through an already-open
descriptor to an old object after its final successful verification.

#### Managed text conflict resolution (released in v1.2.18-rc.6)

```sh
tws template sync auth --resolve-conflicts
tws template sync auth --scope orchestrator --resolve-conflicts
tws add auth --resolve-conflicts            # existing managed feature
VISUAL='code --wait' tws template sync auth --resolve-conflicts
```

This flag is **not a managed selector**. It never migrates legacy features or
works with directory-valued templates. It reviews selected, ledger-owned
regular UTF-8 text without NUL, with unchanged recorded permissions. Ordinary
non-conflicting binary updates remain supported. Unmanaged same-content
files, symlinks/parents, changed worktree or link identities, tracked injection
collisions, missing sources, invalid metadata, and pending recovery remain
hard refusals, even with `--force` or `--allow-managed-removal`.

Each escaped feature/role/path prompt shows current and incoming rendered
digests/modes and offers **keep / replace / diff / edit / cancel**
(`k/r/d/e/c`), defaulting to **cancel**. Keep preserves exact local bytes/mode;
replace takes the rendered incoming bytes/mode. Diff is plain, unpaged,
two-way output with terminal controls escaped; it invokes no external diff,
textconv, repository drivers, or pager. There is no three-way base/history.

Edit starts from current bytes in private staging outside feature, worktree,
and registry roots, including their physical aliases, alongside a separate
incoming reference. Editor selection
is nonempty `VISUAL`, then `EDITOR`, then `vi`; quoted arguments are parsed
without shell expansion and the absolute file path is a separate argument.
GUI editors must wait, for example `code --wait`. A preview and explicit
default-No acceptance are required after the editor exits. Safe regular-file
atomic saves are supported; symlink/hard-link or unsafe staging replacements
refuse. Editor/validation errors retain scratch edits and report their path.
Copilot worktree instructions kept or edited this way need valid `applyTo`
frontmatter; accepted bytes are never silently rewritten.

Keep/edit remembers the **effective local bytes separately from the incoming
proposal**. Ordinary sync preserves an unchanged acknowledgement without a
terminal; changed incoming bytes/mode, source/renderer selection, or later
unacknowledged local edits require a new decision. Passing the flag explicitly
reviews unchanged overrides too and therefore needs a terminal. Replace
clears the override, and a result equal to incoming is canonical. Piped
answers, unattended keep-all/replace-all, and automatic prompts are unsupported.

The first noncanonical keep/edit upgrades `.tws-templates.json` from v1 to
**v2**, after a warning; existing v1 state is not eagerly converted. Released
**v1.2.18-rc.5 refuses v2 in managed-state readers/writers**, potentially
including workspace owner discovery. This is not a universal disable switch:
rc.5 checkout `tws new` may still create logical branch/stack metadata without
reading the ledger, leaving template bytes and acknowledgements unchanged.
Use the newer CLI consistently in that workspace. v2 never automatically
downgrades, even after clearing the last override; do not strip metadata.
Missing or obsolete customized files hard-refuse rather than discarding
decisions. Their deletion is excluded even with removal consent.

All managed hard preflight and decisions finish before any effects in a mixed
`--all` batch; legacy members are identified and still copy only missing files.
Ordinary obsolete-file removal has its separate exact-path/default-No consent
after decisions. Cancellation or input/output/editor errors before execution
leave all feature and add branch/session effects unapplied. No writer
reservation is held while waiting for a person or editor. Decisions are
revalidated under exclusion, including unwritten kept files before ledger
publication and cleanup. Failures after effects report per-feature progress
and retained recovery; neither all-file nor cross-feature atomicity is claimed.

Known inherited limitation: renaming an external feature with materialized
worktrees can leave stale Git backpointers
([#5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5)).
Acknowledgements survive feature metadata moves; this release does not
repair that Git behavior.

### Multi-Repo Workspaces

Work on code and docs repos in the same feature:

```sh
tws add auth -n code-branch
tws new auth wiki-docs --repo ~/projects/myapp-wiki
```

### Workspace Portability

```sh
tws export auth                     # YAML to stdout
tws export auth --to-repo           # save to .tws/workspaces/ (travels with git push)
tws export auth --full -o auth.tar.gz  # tarball with inject files
tws import --from-repo auth         # recreate on another machine
```

The export schema is unchanged. Managed selections, ownership/link evidence,
and orchestrator assets are not portable; `--full` includes inject bytes only,
and imported bytes are unmanaged until an explicit future migration feature.

### 3-Tier Skill System

- **Worktree guidance** — native Claude/Copilot files injected into each worktree
- **Orchestrator guidance** — native Claude/Copilot files in the feature dir
- **Global skills** — installed via `tws init`, knows how to create workspaces

```sh
tws init                            # install Claude + Copilot skills
tws init --agent claude             # Claude only
tws init --register --register-alias myapp   # also enroll in the global registry
```

## Agent work status

`tws status --json` remains the global view and builds every feature in the
resolved workspace. `tws status <feature> --json` is a true scoped projection:
it reads and probes only the requested feature plus workspace-level evidence.
That workspace evidence includes the single shared checkout session even when
it belongs to another feature; unrelated external session records are not
included.

Status Git/tmux commands are read-only, limited to five seconds each, and share
a thirty-second subprocess budget beginning with workspace/config resolution.
Failed or timed-out facts are `null`/`unknown` with issues such as
`git-probe-unavailable` or `status-probe-exhausted`, never fabricated as clean,
missing, or absent. The budget cannot interrupt an operating-system filesystem
read. No timeout flags are added.

## All Commands

| Command | Description |
|---------|-------------|
| `tws add <feature> [-n branch] [--open] [--tmux] [--template dir-or-name] [--worktree-template ref] [--orchestrator-template ref] [--agent claude\|copilot] [--resolve-conflicts]` | Create legacy or managed feature workspace |
| `tws new <feature> <branch> [--base] [--repo] [--force]` | Create worktree branch |
| `tws open [feature] [branch] [--tmux] [--no-agent]` | Open worktree (interactive picker if no args) |
| `tws sync <feature> [--push] [--continue] [--abort] [--verbose] [--fetch\|--no-fetch] [--full\|--local-only] [--only <entry>\|--from <entry>]` | Rebase in dependency order, optionally scoped |
| `tws sync <feature> --plan [--json] [--max-replay-per-entry N] [--max-replay-total N]` | Preview the rebase; candidates are an upper bound, never applied |
| `tws sync <feature> --approve-plan <fingerprint> [--max-replay-per-entry N \| --max-replay-total N]` | Execute a previewed, guarded plan |
| `tws push <feature> [--dry-run]` | Push all branches |
| `tws stack <feature>` | Show dependency tree |
| `tws stack status <feature> [--json]` | Stack ancestry, materialization, and upstream status |
| `tws stack reparent <feature> <entry> --onto <dest>` | Move one entry onto a new parent and replay its descendants |
| `tws stack reparent <feature> <entry> --onto <dest> --plan --max-replay-total N [--json]` | Preview the bounded reparent; moves no branch and writes no tws state; may fetch according to policy |
| `tws stack reparent <feature> --continue` / `--abort` | Resume or roll back the persisted reparent |
| `tws list` / `tws ls` | List features and branches |
| `tws delete <feature>` | Remove feature and worktrees |
| `tws archive <feature> <branch>` | Remove worktree, keep branch |
| `tws decide <feature> "<msg>" [--type] [--to]` | Record a decision |
| `tws decisions show [feature] [--mine] [--all]` | View decisions |
| `tws decisions ack [feature]` | Mark decisions as read |
| `tws inject <feature> [branch] [--into path]` | Sync inject files into external linked worktrees |
| `tws doctor [feature]` | Health checks |
| `tws status [feature] [--json]` | Agent work status per branch |
| `tws rename feature/branch` | Rename feature or branch |
| `tws config show/set/get` | Manage configuration |
| `tws hooks install/remove [--all]` | Manage agent hooks |
| `tws export <feature> [--full] [--to-repo]` | Export workspace |
| `tws import <file> [--from-repo]` | Import workspace |
| `tws template add/list/show/remove` | Manage global/per-repository named template packages |
| `tws template sync [feature\|--all] [--template dir-or-name] [--scope worktree\|orchestrator\|both] [--resolve-conflicts] [--allow-managed-removal]` | Backfill legacy templates or refresh managed roles |
| `tws close <feature> <branch>` | Kill tmux session |
| `tws registry add <path> [--alias name]` | Register a repo/workspace for discovery |
| `tws registry list/show/check [--json]` | Inspect registered workspaces |
| `tws registry alias <selector> <alias> [--remove]` | Manage aliases |
| `tws registry repair <selector> <new-path> [--allow-identity-change]` | Re-point a moved entry |
| `tws registry remove <selector>` | Drop registry metadata (never deletes files) |
| `tws registry prune --missing [--force]` | Drop entries whose targets are gone |
| `tws space add <name> <path> --kind <kind> [--description text] [--feature f]` | Link a tool-owned sibling space |
| `tws space list [--feature f] [--all] [--kind k] [--json]` | Discover linked sibling spaces (bare list is cwd-scoped) |
| `tws space show <name> [--feature f \| --workspace] [--json]` | Show one linked space |
| `tws space remove <name> [--feature f \| --workspace]` | Drop the link (never deletes the target) |
| `tws init [--agent] [--force] [--register] [--register-alias name]` | Install agent skills |

### Reparent a branch onto a new base

```sh
# 1. preview — moves no branch, writes no tws state, makes no provider call
tws stack reparent auth auth-middleware --onto main --plan --max-replay-total 20

# 2. approve the exact previewed plan; a fresh run is ALWAYS guarded
tws stack reparent auth auth-middleware --onto main \
  --approve-plan <fingerprint> --max-replay-total 20

# 3. recover, if a replay conflicts
tws stack reparent auth --continue     # resume forward
tws stack reparent auth --abort        # roll back
```

The stored base is canonical: a stack-entry destination stores its logical
entry name, a named literal ref stores its full `refs/...` name, and a raw
object-id destination stores the full lowercase OID. The ref commit is
race-atomic in the compare-and-swap sense: a concurrent write to any
expected-old ref aborts the ref transaction during prepare; this is not reader
snapshot isolation. It is crash-atomic only on the reftable backend. A
reparent has exactly one commit point. Refs, `stack.yaml`, and worktree/index
state are separate effects; the commit point requires both a
durably written post-image metadata file and refs that are either already at
their planned values or a no-op. After that point recovery is forward-only:
`--abort` completes and cleans up rather than undoing the
topology change.

`--continue` and `--abort` never take an approval token, a destination, a
cutoff, a limit or a fetch flag: they take the whole frozen decision from
persisted state.

The preview and execution MUST carry the same replay limit flag(s) and values;
a limitless preview has a null fingerprint and cannot be approved.

Because checkout mode has one physical checkout, any checkout reparent blocks
opening a session for every feature in that workspace until recovery finishes.
Checkout `tws open <feature> --feature-dir` holds the same workspace launch
intent through its agent and shell and repeats the mutation/reparent/session
check after publishing that intent.
External direct, tmux, feature-directory, and `--all` launches publish intent
before their final mutation check; exclusion is limited to the target and
affected descendants. Checkout sync and reparent also share one
workspace-global mutation lock, so different features cannot move the physical
checkout at the same time; any checkout stack entry with a non-empty `repo` is
refused. If the checkout-global lock is absent, current tws scans every
feature's recoverable checkout sync/reparent state before admitting a fresh
mutation, while recovery reconstructs only its own reservation. A real
top-level external push holds the feature mutation lock across
its whole multi-entry invocation, so reparent cannot enter between preflight
and a later push. Every mutating external sync route holds the same feature
mutation lock through rebase, metadata, optional push, and remote follow-up
clearing; it rechecks reparent state after claiming and releases last. Under
that lock, obsolete/archived follow-up rows clear
before lease capability preflight; dry-run evaluates the same clears in memory.
Crash recovery removes a dead checkout launch intent only while holding the
workspace-global lock, never treats an exact pre-reparent ref as proof that the
commit happened, and refuses to redetach a restored holder if the operator
switched it to another branch.

Repository aliases are not silently normalized into topology: if two sides of
a logical edge use different stored `repo` spellings even though both resolve
to one Git common directory, reparent refuses until all shipped stack readers
are common-dir-aware. Validation snapshots the untracked set before each
command, so preexisting untracked paths are allowed while new untracked paths
and all tracked modifications remain residue. Pre-commit abort journals remote
record restoration before writing/removing it and resumes that restore
idempotently after a crash.

tws changes no remote ref and no pull request. A reparented branch whose pull
request still points at the old base is recorded locally, so the next `tws push`
warns and strengthens its lease with `--force-if-includes`. Real pushes are
refused while the reparent commit point is still unproven, so a later abort
cannot lose its remote-protection record. A newer run also preserves an older
same-branch publication observation when the local tracking ref is temporarily
missing; only a positive local clear removes that protection.

### Global Workspace Registry

Opt-in discovery index at `${XDG_DATA_HOME:-~/.local/share}/tws/registry.yaml`
(directory `0700`, file `0600`). Nothing is created until you enroll explicitly.

```sh
tws registry add . --alias myapp          # enroll the current repo/workspace
tws init --register --register-alias myapp  # enroll after a successful init
tws registry list --json                  # deterministic output; empty is []
tws registry check                        # ok / missing / mismatched / invalid
tws registry repair myapp /new/path       # re-point after a move
tws registry prune --missing --force      # --force required in non-TTY use
```

Selectors are exact: entry ID, alias, or canonical path. Aliases may not shadow
an entry ID or a registered path.

**Identity and markers.** Git-backed targets carry a small opaque marker at
`.git/tws/workspace-id`; checkout mode and linked worktrees share the main
repository's Git common directory. External workspaces use
`.tws-workspace/workspace-id`. Markers are created only on explicit enrollment,
survive moves and workspace-mode switches, and detect replacement.

- Moved target: `tws registry repair <selector> <new-path>` — no extra flag needed.
- Replaced target (marker or Git identity changed): add `--allow-identity-change`.
- `tws registry remove`/`prune` only drop registry metadata; targets and marker
  files are never deleted.

### Workspace sibling links

A tws workspace is surrounded by tool-owned sibling spaces: learning notes,
ticket stores, patch metadata, research, and authored documentation. `tws space`
records **where** they live in `<workspace-root>/spaces.yaml` so agents and
humans discover them by command instead of by hard-coded path.

```sh
tws space add learning ./learning --kind learning --description "notes"
tws space add patching ./acme/patching --kind patching --feature acme
tws space list                        # cwd-scoped: workspace-wide + detected feature
tws space list --all                  # the complete registry, from anywhere
tws space list --json                 # deterministic output; empty is []
tws space list --feature acme         # workspace-wide entries plus acme's
tws space show learning --workspace   # scope selectors disambiguate a shared name
tws space remove learning --workspace # drops the link; never deletes the target
```

- **Location metadata only.** `tws` never reads, writes, validates, or deletes
  the content of a linked space, and it never learns the linked tool's schema or
  lifecycle. `tws space add` is the only command that creates anything for this
  feature, and it never creates the target directory.
- **Where the file lives.** External mode uses the resolved external root
  (`$TWS_ROOT` when set); checkout mode uses `<repo>/.tws` and ignores
  `TWS_ROOT`, as every other checkout command does. `tws space list` always
  prints `Workspace: <root> (mode: <mode>, scope: <scope>)` before its results,
  including the empty state, so the active file and scope are unambiguous.
- **Default scope is your location.** A bare `tws space list` shows every
  workspace-wide entry plus the entries of the feature you are inside when one
  is detected; outside a feature it is already complete. Use `--all` for the
  complete registry from anywhere, and `--kind` to filter. `--json` is a bare
  array with no header. A filter that hides everything says so and reports how
  many entries are registered, which is never confused with an empty registry.
- **Scope selectors.** When the same name exists workspace-wide and inside a
  feature, `tws space show` / `tws space remove` report the ambiguity and
  accept `--workspace` or `--feature <name>` (mutually exclusive) to select
  exactly one.
- **Two path forms.** Targets inside the workspace root are stored
  workspace-relative and stay portable; targets outside are stored absolute. A
  target must exist and be a directory, but it does **not** need to be a Git
  repository.
- **Local state.** `spaces.yaml` and `.spaces.lock` are mode `0600`, are not
  shared, and are not included in `tws export` / `tws import`. The advisory lock
  is POSIX-only (macOS and Linux).
- **Feature-name protection.** A registered target directory can never
  masquerade as a feature. Ownership is decided by filesystem identity, so a
  hand-edited absolute path inside the workspace root, a symlinked spelling, or
  a different letter case on a case-insensitive volume is recognised as the
  same directory. `tws add`, `new`, `delete`, `rename`, `archive`,
  `sync`, `export`, `import`, `open`, `stack`, `inject`, `push`, `decide`,
  `doctor`, `template sync`, `hooks install`, and `tws migrate-layout` all
  refuse a feature name owned by a registered space, and feature listings
  exclude it. `tws delete` and `tws migrate-layout` refuse when a registered
  target lives inside the feature — `migrate-layout` never rewrites a registered
  path, it names the blockers and the exact scope-qualified
  `tws space remove` command for each, and `--all` is all-or-nothing — and
  `tws rename feature` rewrites relative entries while refusing pinned absolute
  ones.
- **Strict on untrusted metadata.** If `spaces.yaml` exists but is unreadable,
  symlinked, malformed, carries an unknown field, or declares a future schema
  version, every command that consults workspace features or spaces exits
  nonzero having changed nothing. Only shell completion degrades, silently
  offering no candidates. When the file is absent — the normal state — nothing
  is created and every pre-existing command behaves exactly as before.
- **Inside a sibling space the enclosing `.tws-workspace` marker wins**, so
  `tws space list` keeps targeting the parent workspace even when the space is
  its own Git repository. No `spaces.yaml` or `.tws` directory is ever created
  for the sibling repo.

## Requirements

- [Go](https://go.dev/dl/) 1.26+
- [git](https://git-scm.com/)
- [tmux](https://github.com/tmux/tmux) (optional, for `tws open --tmux`)
- A coding agent: [Claude Code](https://claude.ai/claude-code) (default), [OpenCode](https://opencode.ai), [Aider](https://aider.chat), or any CLI agent

## Configuration

```sh
tws config set agent_command opencode        # change agent
tws config set use_tmux true --repo          # owning-repository tmux default
tws config set test_command "go build ./..."  # post-rebase validation
tws config set auto_hooks true               # auto-install hooks on tws new
tws config set inject_into .context          # inject target subdirectory
tws config set worktree_template global:worktree-base
tws config set orchestrator_template repo:orchestrator-base --repo
```

Config files: `~/.config/tws/config.yaml` (global), `.tws/config.yaml` (per-repo). Env: `TWS_ROOT`.

Shell completions: `tws completion zsh/bash/fish/powershell`

## Documentation

- [Cheatsheet](docs/cheatsheet.md)
- [Configuration](docs/configuration.md)
- [Agent Hooks](docs/hooks.md)
- [v1.2 RC Validation](docs/v1.2-rc-validation.md)
- [Roadmap](docs/roadmap.md)

## Install from Source

```sh
git clone https://github.com/jdbencardinop/tesseraworkspaces.git
cd tesseraworkspaces
make install       # installs to $GOPATH/bin/tws
make build         # builds to bin/tws with version from git tag
```

## License

[MIT](LICENSE)
