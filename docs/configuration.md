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

Known inherited limitation: materialized external feature rename can leave
stale Git backpointers ([#5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5)).
Acknowledgements survive metadata moves, but this release does not repair
that Git behavior or make them portable in exports.

Each key is the absolute path to a git repository. The value is the workspace root where features and worktrees are stored for that repo.

If no entry exists for the current repo, tesseraworkspaces defaults to a sibling directory named `<repo>.tws/` next to the repo.

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
