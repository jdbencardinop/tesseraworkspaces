# Configuration

tesseraworkspaces resolves the workspace root directory using the following priority chain:

| Priority | Source | Example |
|----------|--------|---------|
| 1 | `TWS_ROOT` environment variable | `export TWS_ROOT=/data/workspaces` |
| 2 | Per-repo config (`.tws/config.yaml` in repo root) | See below |
| 3 | Global config (`~/.config/tws/config.yaml`) keyed by repo path | See below |
| 4 | Repo-relative sibling directory | `../myapp.tws/` |
| 5 | Global fallback | `~/tws` |

## Per-repo config file

Location: `.tws/config.yaml` in the repo root. Committable so team members share settings.

```yaml
# Override agent command for this repo
agent_command: opencode

# Use tmux by default for this repo
use_tmux: true

# Named managed-template defaults for newly created features
worktree_template: global:worktree-base
orchestrator_template: repo:orchestrator-base
```

Per-repo values override global config. Template references may be unqualified
(repository registry first), `repo:<name>`, or `global:<name>`.
For managed feature commands, “repo” means the workspace's primary owning
repository, even when the command is invoked from a secondary worktree.

## Global config file

Location: `~/.config/tws/config.yaml`

```yaml
# Workspace paths (keyed by repo path)
workspaces:
  /Users/you/projects/myapp: /data/workspaces/myapp
  /Users/you/projects/api: /custom/path/api-workspaces

# Agent command launched by tws open (default: claude)
agent_command: claude

# Use tmux for tws open (default: false)
# Override per-invocation with --tmux or --no-tmux
use_tmux: false

# Opt newly created features into managed role templates
worktree_template: worktree-base
orchestrator_template: orchestrator-base
```

### Agent command

The `agent_command` field controls what `tws open` runs in the worktree. Defaults to `claude`.

Examples:

```yaml
agent_command: claude          # default — includes -c auto-detection
agent_command: opencode        # OpenCode
agent_command: aider           # Aider
agent_command: codex            # Codex CLI
agent_command: claude-dev      # Claude Code dev build — -c auto-detection works
```

When the agent is `claude`, `claude-dev`, or `cc`, tws automatically detects existing Claude sessions and appends `-c` (continue) on subsequent opens.

Managed template rendering recognizes exact Claude aliases above and the
`copilot` executable. A wrapper or custom `agent_command` must use explicit
`--agent claude` or `--agent copilot`; tws does not guess from substrings or
execute the command to identify it. An explicit renderer records the current
`agent_command` as decision provenance: ordinary sync and checkout context
continue to honor that renderer while the launch command is unchanged, and
require explicit reselection after a later launch-command change.

### Managed template defaults

`worktree_template` and `orchestrator_template` supply role defaults only when
a new feature enters managed mode. Existing legacy features are not migrated
implicitly. Once selected, each role persists its resolved registry origin and
rendering agent in feature-local `.tws-templates.json`; later default or
registry shadowing changes do not retarget it. Registry roots carry an opaque,
move-stable identity, so moving a repository with its local registry preserves
the saved origin while another HOME or repository containing the same name is
never substituted.

```sh
tws config set worktree_template global:worktree-base
tws config set orchestrator_template repo:orchestrator-base --repo
```

The global registry is `~/.config/tws/template-registry/`; the repository
registry is `.tws/.template-registry/`. Managed worktree guidance requires
root injection, so a non-root `inject_into` is compatible only with legacy/user
inject files or orchestrator-only managed updates. `tws inject ... --into .`
explicitly overrides a configured non-root target for that invocation; an
explicit non-root `--into` does not.

### Managed conflict decisions (released in v1.2.18-rc.6)

`tws add <feature> --resolve-conflicts` and
`tws template sync <feature>|--all --resolve-conflicts` explicitly review
selected ledger-owned regular UTF-8/no-NUL text. There is no configuration key
for an automatic or unattended conflict policy: the flag is not a managed
selector, does not migrate legacy features, and cannot accompany directory
templates. Permission drift, unmanaged files, symlinks, tracked injection
collisions, missing sources, invalid identities/metadata, and pending recovery
remain hard refusals.

The terminal menu offers keep/replace/diff/edit/cancel and defaults to cancel.
Keep/edit acknowledges only the effective bytes/mode and the current rendered
incoming proposal, bound to the saved source/role/path/renderer. An ordinary
repeat with unchanged evidence preserves it noninteractively. Changed incoming
bytes/mode or selection, or an unacknowledged local edit, needs another
decision. Explicit review with the flag includes unchanged overrides and
requires a real terminal; piped answers are not consent. Replace clears an
override; accepted bytes equal to incoming are canonical.

The first noncanonical keep/edit upgrades `.tws-templates.json` to **v2** after
a warning. v1 is still readable without eager conversion; v2 remains v2 after
the last override is cleared. **v1.2.18-rc.5 refuses v2 in managed-state
readers/writers** instead of ignoring decisions and may refuse workspace owner
discovery while scanning another feature. This is not a universal disable
switch: rc.5 checkout `tws new` may still create logical branch/stack metadata
without reading the ledger, leaving template bytes and acknowledgements
unchanged. Use the newer CLI consistently throughout an affected workspace.
There is no downgrade or metadata-stripping command.

Editor environment precedence is nonempty `VISUAL`, then `EDITOR`, then `vi`.
Commands support quoted arguments but no shell interpolation, expansion,
pipelines, or evaluation; the absolute staged path is passed separately.

```sh
VISUAL='code --wait' tws template sync auth --resolve-conflicts
```

GUI editors must wait for completion. Staging uses `TMPDIR` (or the platform
temporary base), and refuses a base inside feature, worktree, or registry
roots, including physical case aliases. Directories are 0700 and files 0600;
safe regular atomic saves are
accepted, symlinks/hardlinks and changed parents are not. A separate snapshot
contains the final rendered incoming bytes. Diff is two-way, plain/unpaged,
escapes terminal controls, and runs no external diff/textconv/drivers.
Edited output is previewed and requires default-No acceptance; editor exit
alone is not consent. Editor/validation failures preserve scratch edits and
report their location. Native Copilot worktree content must retain valid
`applyTo` frontmatter; accepted bytes are never silently rewritten.

Missing or obsolete customized files are preserved/refused, never recreated
or deleted to discard a decision. `--allow-managed-removal` remains separate
consent for exact listed ordinary unchanged obsolete paths, not a conflict
policy. All managed preflight and decisions precede any mixed-batch effects,
without holding writer reservations during prompts. Failures after execution
retain per-feature progress and recovery, not all-file/cross-feature atomicity.

Published v1.2.18-rc.6 retains the external rename/backpointer defect
([#5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5)).
External feature rename is released in v1.2.18-rc.7 and fixes future renames;
#5 is closed. It preserves dirty work, index/ref identities, and v1/v2
template bytes, link identities, selections, and acknowledgements without
re-rendering or making them portable in exports. A later explicit template
sync can require new consent when the feature name changes rendered guidance.

External rename journals live outside the moved feature, under the
execution-selected `<workspace-root>/.tws-workspace/feature-renames/`.
Use `tws rename feature OLD NEW --continue` or
`tws rename feature OLD NEW --abort` with the same names, root, and controlling
repository; each stack entry's repository is recorded separately. Recovery
never replans from changed defaults. These mutually exclusive flags do not
apply to checkout or branch rename and cannot repair older broken layouts.
After durable completion, only continuation for verified cleanup is allowed.

Equivalent ambient/root spellings (including macOS `/var` and `/private/var`)
share the physical workspace's admission locks. Root-alias identities and
targets are captured and rechecked, including during recovery; a late retarget
or replacement refuses. Selected feature/worktree and administrative
descendants still use no-follow inspection. Invalid special lock objects,
including FIFOs and symlinks, refuse promptly and are left intact.
Root resolution must not traverse the old/new feature, including a directory
later canceled by `..`; `commondir` and forward admin bindings are inspected
component-by-component before lexical cancellation, refusing unknown traversed
symlinks even when the cleaned path looks stationary. Recorded ambient/root
aliases remain supported.
Pending execution-selected rename names are checked before existence-based
fallback, including sync and push. A healthy fallback keeps both selected-name
and actual-root reservations through mutation; previews create no admission
state. Metadata/context writers hold admission for their actual resolved
feature. Context init discovers the external workspace containing its physical
cwd independently of an unrelated `TWS_ROOT`; equivalent aliases retain the
same exclusion and retarget checks. Genuine outside-workspace init creates no
feature admission state.

Success requires no dangling Git administrative links or old-path
registrations. Nested/primary repositories, populated submodules, nested
mounts, ambiguous markers, and unsupported path-bearing Git settings (including
`core.worktree`, hook paths, includes, filters, and nonempty object alternates)
refuse rather than being rewritten. Active sessions/Git operations/locks and
other recovery also refuse. Per-record pointer style, not the current
`worktree.useRelativePaths` setting, determines a pointer's new spelling.
An absent `spaces.yaml` stays absent; only the existing guarded relative rewrite
is published after Git validation. Concurrent registry edits are not clobbered.
Directory movement, Git pointers, and registry publication are separate effects,
not all-file/cross-repository atomicity or snapshot isolation. Failures retain
exact evidence and may leave temporary partial links; automatic rollback needs
verified ownership. Do not delete unknown residue or use an older tws while
rename is active or recoverable. Completed receipts and name-lock files remain.
Continuation re-syncs the recorded parents of visible effects before marking
them complete. Invalid registry postimages (including scope/name collisions)
refuse before operation-journal publication, without changing either entry.

Each key is the absolute path to a git repository. The value is the workspace root where features and worktrees are stored for that repo.

If no entry exists for the current repo, tesseraworkspaces defaults to a sibling directory named `<repo>.tws/` next to the repo.

### Scoped publication ownership and Git configuration

Selected standalone push never searches unrelated features/worktrees for a
default publisher. It proves the selected repository using an explicit
`workspaces` mapping, the conventional `<repo>.tws` root, a matching controlling
repository context, or selected worktree evidence. It does not borrow an
unrelated caller's repository. For custom roots with no selected materialized
worktree, configure an unambiguous absolute repository-to-workspace mapping.
Explicit selected entry `repo` paths must be absolute working-tree or bare
repository roots; checkout stays single-repository. Authored/imported
subdirectory contexts are refused before publication. Root proof also supports
linked working-tree roots and explicit working-tree roots with a separate Git
directory, while retaining the original path/alias binding. A relative origin
URL is therefore anchored only at Git's actual execution root, never a nested
directory that happens to discover the same repository.

Selected-root pending rename and pending/untrusted publication checks precede
stack-existence fallback, even if that feature's `stack.yaml` disappeared.
Publication is checked again under the retained selected-name admission; both
selected-root and actual-root admissions remain held through effects. Matching
standalone continuation/cancellation still finds its own journal before live
stack lookup; other commands never adopt it or silently use the configured root.
Recovery classifies at most those two journal candidates: selected pending
publication has priority, but a terminal selected receipt cannot hide a matching
pending configured-root fallback. Untrusted or foreign bindings still refuse.
After an external feature rename, terminal history is recognized by the same
physical workspace and feature-directory identities, not its historical name.
It cannot authorize publication or hide pending recovery; copied receipts
beside a different feature inode still refuse.

The new standalone route requires Git 2.45+ for enforced `--no-lazy-fetch`
support; older Git refuses rather than risking hidden network access. Existing
legacy push and sync Git requirements/arguments are unchanged.

Only origin publication is supported. It must have one effective fetch URL
and one effective push URL that agree; distinct transports/endpoints cannot
prove that tracking belongs to the publisher and refuse. The selected
destination must have exactly one supported origin fetch mapping: conventional
`refs/heads/*:refs/remotes/origin/*`, or an exact selected ref mapping into
`refs/remotes/origin/`. Negative/ambiguous/unsupported wildcard mappings refuse.
The journal stores an endpoint/config fingerprint, never URL credentials.
URL rewrite rules must leave the resolved endpoint unchanged if passed back
to Git; ambiguous second expansion is refused rather than contacting another
destination.
An endpoint that is itself the name of a configured Git remote or a
`remotes.<endpoint>` group also refuses: Git could otherwise follow different
`url`/`pushurl` values or dispatch a multi-remote push. Group lookup uses Git's
native config-key normalization, including the case-insensitive variable
component (such as an endpoint ending in `.GIT`). These local checks repeat
before publication and uncertainty reconciliation, even for expected-absence
leases. They do not contact a remote or expose endpoint credentials.
Local endpoint spellings containing a parent traversal are accepted only when
the traversed prefixes are real directories, not symlinks or missing objects.
This covers both absolute and relative paths and preserves ordinary
`../remote.git`; tws never silently collapses `link/../remote.git` into another
remote.
Continuation verifies it again but never refreshes the recorded expected OID.
Tracking absence grants only expected remote absence.

Explicit one-ref OID refspecs and command-level Git overrides disable automatic
tag following, mirror/matching defaults, prune and recursive submodule pushes.
User hooks remain enabled; their arbitrary effects are not covered by this
isolation guarantee. No persistent Git configuration or upstream is changed.
Pending publication requires consistent use of the new binary. State is
`.push-state.v1.json` in an external feature, or
`.tws/state/<feature>-push.v1.json` in checkout mode; completed/canceled
receipts remain after exact-owned private-ref and reservation cleanup.

Pending reparent follow-up tips are protected by token-owned `followup` refs
alongside source/lease/witness refs. Per-entry keep/clear decisions are saved
before any protection ref is deleted; recovery uses those decisions rather
than re-reading an object that later GC may legitimately remove.

Checkout lifecycle and layout migration use the shared workspace lock with
`operation: metadata`, not a fabricated sync transaction. Under that exclusion,
verified terminal receipts are linked into
`.tws/state/publication-history/<token>.json`, the history file and directories
are synced, and only then is the reusable live receipt name removed and synced.
A retry may find two links to the same owned inode; a copied file or foreign
history collision is never adopted. Rename/delete/migration can thus reuse
logical names without discarding exact historical bytes. Retirement can finish
before a later lifecycle failure; history stays retained, not rolled back.
Pending state, missing/stale guards, source/destination collisions and late
state changes are rechecked under exclusion through the actual movement.
Rename and one/all migration persist narrow movement intent at
`.tws/state/.checkout-metadata-<token>` before the directory syscall:
kind `tws-checkout-metadata-moves`, version `1`. It binds the workspace,
source/destination parents, exact feature-directory inode and registry preimage.
A rename error does not imply that nothing moved. The caller reconciles the
recorded inode location, repeats durability barriers and compensates an
already-moved directory under the same reservation; batch rollback includes
the current failing item. The original failure is still returned.
Evidence is completed/removed only after coherent registry/layout completion
or verified restoration. If compensation encounters a foreign path or cannot
finish, the real workspace guard and evidence remain; missing/stale guards
cannot bypass pending evidence. Preserve and reconcile that exact directory/
registry evidence before further mutation, rather than deleting it to unblock
the workspace. This is not a sync/reparent transaction or an additional
publication `--continue`/`--cancel` operation.
Shared checkout launch preconditions reuse the movement-evidence validator,
including the final check under session intent. Pending or untrusted evidence
blocks direct/tmux branch launches and feature-directory agents/shells even
without `checkout-mutation.lock`, before branch switching or context injection.
Only validated completed movement receipts are nonblocking.

## Environment variable

Set `TWS_ROOT` to override all config resolution for the current session:

```sh
TWS_ROOT=/tmp/scratch tws add my-feature
```

Managed external add/new/open/inject/template-sync operations use the same
execution-selected feature root. New features honor `TWS_ROOT`; an already
existing feature remains coherent with the root where that feature is found
rather than splitting its assets, worktrees, links, and reservations.
The workspace's primary repository supplies role defaults, repository
registry entries, and agent configuration even when the command runs inside
a secondary-repository worktree. Ambiguous primary ownership is refused.

## Default behavior

When no config exists and no env var is set:

- **Inside a git repo** (`/Users/you/projects/myapp`): workspace root is `../myapp.tws/`
- **Outside a git repo**: workspace root falls back to `~/tws`

## Workspace layout

```
<workspace-root>/
  <feature-name>/
    FEATURE.md
    CLAUDE.local.md
    stack.yaml
    worktrees/
      <branch-name>/
```

## Branch stacks (stack.yaml)

Each feature can have a `stack.yaml` that tracks branch dependencies:

```yaml
branches:
  - name: auth-models
    branch: juanbe/feature/auth-models  # optional; defaults to name
    base: master
  - name: auth-middleware
    base: auth-models
  - name: auth-routes
    base: auth-middleware
```

- **name**: short tws name and flat worktree folder name
- **branch**: actual Git branch name (optional; defaults to `name`)
- **base**: parent tws branch name or an explicit Git ref

`stack.yaml` is created automatically when you use `tws new`. An omitted base uses the selected repository's `origin/HEAD`. Explicit refs are literal: `master` means local `master`, while `origin/master` means the remote-tracking ref. Tags and commit SHAs are accepted.

```sh
tws new auth auth-models
tws new auth auth-middleware --base auth-models
tws new auth release-check --base origin/release
tws new auth wiki-docs --repo ../wiki --base master
```

`tws sync <feature>` rebases branches in topological order. If a conflict occurs, resolve it and run `tws sync <feature> --continue`; deferred descendants resume and completion is reported only after relevant ancestry edges are current.

`tws stack <feature>` prints the dependency tree:

```
(main)
└── auth-models
    └── auth-middleware
        └── auth-routes
```

`tws stack status <feature> [--json]` reports, for every entry in `stack.yaml`
order, its configured base and parent head, ancestry state, materialization,
dirty and in-progress Git operation, upstream state, and ahead/behind counts
against the parent. It consumes the shared ancestry evaluator, is read-only and
local-only, and leaves the legacy tree above unchanged.

Features without a `stack.yaml` use the repository's detected default remote branch for fallback sync.

### Divergent stacks

Multiple branches can share the same parent, creating a tree structure:

```yaml
branches:
  - name: auth-models
    base: main
  - name: auth-middleware
    base: auth-models
  - name: auth-routes
    base: auth-middleware
  - name: auth-tests
    base: auth-models       # diverges from middleware
```

```sh
tws new auth auth-tests --base auth-models
```

```
(main)
└── auth-models
    ├── auth-middleware
    │   └── auth-routes
    └── auth-tests
```

Sync handles divergent stacks correctly — each lineage is rebased independently. A failure in `auth-middleware` skips `auth-routes` but does NOT skip `auth-tests` (different lineage).
