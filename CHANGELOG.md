# Changelog

## v1.2.11

- **Workspace sibling links** — `tws space add/list/show/remove` maintains
  `<spaces-root>/spaces.yaml` (mode `0600`, per-workspace `flock`) as a
  discovery registry for tool-owned learning, ticket, patching, research, and
  documentation spaces. `tws` owns the location only; it never reads, writes, or
  deletes linked content, and `tws space remove` never deletes the target
- **Two path forms** — targets inside the workspace root are stored
  workspace-relative; targets outside are stored absolute. Targets must exist and
  be directories but need not be Git repositories
- **Feature-name protection** — a registered directory can never masquerade as a
  feature: feature listings exclude it and `tws add`, `new`, `delete`, `rename`,
  `archive`, `sync`, `export`, `import`, `open`, `stack`, `inject`, `push`,
  `decide`, `doctor`, `template sync`, and `hooks install` refuse it. Ownership
  is decided by filesystem identity, so a hand-edited absolute path inside the
  workspace root, a symlinked spelling, or a different letter case on a
  case-insensitive volume names the same protected directory. `tws delete` also
  refuses when a registered target lives inside the feature — naming the exact
  scope-qualified `tws space remove` command for each blocker — and
  `tws rename feature` rewrites relative entries while refusing pinned ones
- **Scoped listing and selectors** — `tws space list` always prints
  `Workspace: <root> (mode: <mode>, scope: <scope>)` before its results,
  distinguishes an empty registry from filters that hide every entry, and
  documents that a bare list is cwd-scoped while `--all` is the complete view.
  `tws space show` and `tws space remove` accept `--workspace` alongside
  `--feature` so an entry is always reachable when both scopes share a name
- **`tws migrate-layout` refuses to move a registered space directory**, or any
  legacy feature directory that still contains a registered target — the refusal
  names every blocker with its scope and its exact `tws space remove` command,
  and says the link can be re-added once the migration is done. Nothing is
  rewritten and nothing is moved; `--all` is all-or-nothing rather than
  producing a partial migration
- **Strict failure on untrusted spaces metadata** — an unreadable, symlinked,
  malformed, unknown-field, or future-version `spaces.yaml` makes every command
  that consults workspace features or spaces exit nonzero having changed
  nothing; only shell completion degrades to no candidates
- **Breaking (exit status):** `tws template sync` and `tws hooks install` now
  report failures on **stderr with a nonzero exit** instead of printing to stdout
  and exiting 0. Affected paths: `template sync` with no feature and no `--all`;
  `hooks install` when the workspace cannot be resolved, in checkout mode, or
  with no detectable feature. Success output, per-feature progress lines,
  `No features found.`, and the best-effort per-feature error lines of `--all`
  are unchanged
- **No-op when absent** — with no `spaces.yaml` nothing is created (no root, no
  marker, no lock, no temp file) and every pre-existing command keeps its exact
  behaviour

## v1.2.0-rc.1

- **Configured-base checkout** — new branches now start from the requested local branch, remote ref, tag, or commit SHA; omitted bases use the selected repo's `origin/HEAD`
- **Feature-directory repo inference** — `tws new` can infer a single source repo from existing feature metadata/worktrees and fails clearly on multi-repo ambiguity
- **Correct sync continuation** — `tws sync --continue` resumes deferred descendants, preserves state on later failures, and only reports completion after stack ancestry is current
- **Decoupled branch validation** — sync validates the actual Git branch (`StackEntry.GitBranch`) while retaining short tws names for paths and output
- **Feature-level open** — `tws open <feature> --feature-dir` opens the orchestrator directory; `--all` creates a tmux session for the feature and its worktrees
- **P0 integration tests** — real temporary Git repos, remotes, worktrees, explicit refs, conflict continuation, and prefixed branch names

## v1.1.1

- **Feature-level open** — orchestrator directory and tmux `--all` mode

## v1.1.0

- **Directory-mirrored inject routing** — `inject/dev/file` lands at `worktree/dev/file`
- **Branch-name decoupling** — short worktree names may map to policy-compliant Git branches via `branch_prefix`
- **Default branch detection** — omitted bases detect `origin/HEAD`
- **Atomic rename** — Git operations succeed before stack metadata is mutated

## v1.0.0

- **Auto-read decision hooks** — Claude Code SessionStart hooks with optional `auto_hooks`
- **Tiered skills** — global, feature-orchestrator, and worktree guidance
- **Cross-worktree decisions** — broadcast, targeted messages, and read tracking

## v0.7.3

- **Decision read tracking** — `tws decisions show` defaults to unread only, `tws decisions ack` marks all as read, `tws open` shows unread count

## v0.7.2

- **Tab completions** — all commands now provide feature/branch suggestions via `tws completion zsh/bash/fish`

## v0.7.1

- **Template `--template` flag** — `tws add --template <dir>` copies external templates into inject/
- **`tws template sync`** — backfill templates into existing features
- **Quick start** — registered for next release

## v0.7.0

- **Targeted messaging** — `tws decide --to <branch>` for directed decisions, `tws decisions show --mine`
- **Cross-worktree decisions** — `tws decide` and `tws decisions` for broadcasting design decisions

## v0.6.0

- **Context injection** — `inject/` directory with symlinks into worktrees, `tws inject` command
- **Health checks** — `tws doctor` for branch consistency, dirty worktree, and inject validation
- **Workspace templates** — `~/.config/tws/templates/` auto-populate inject/ on `tws add`
- **Rename** — `tws rename feature/branch` with stack.yaml ref updates

## v0.5.0

- **Config command** — `tws config show/set/get` with `--repo` flag
- **Close command** — `tws close` kills tmux sessions, stale session warnings
- **Bug fixes** — cwd after agent exit, auto-select messaging

## v0.4.0

- **Pluggable agent** — `agent_command` in config, Claude/OpenCode/Aider/Codex support
- **tmux-free mode** — direct exec by default, `--tmux` opt-in
- **Archive** — `tws archive` with smart sync, archived vs missing detection

## v0.3.0

- **Cobra migration** — proper flag parsing, auto-generated help, shell completions
- **Skill distribution** — `tws init` installs Claude + Copilot agent skills
- **Interactive picker** — `tws open` with fzf/numbered list, 0/1/2 arg modes
- **Divergent stacks** — DAG support formalized with tests
- **Per-repo config** — `.tws/config.yaml` overrides global config

## v0.2.0

- **Delete** — `tws delete` removes feature and worktrees
- **Versioned builds** — `--version`, `--help`, ldflags from git tags
- **Fix claude session** — auto-detect existing sessions for `-c` flag
- **List** — `tws list/ls` shows features and branches
- **Checkout existing** — `tws new` with existing branches, `--force` for checked-out
- **Lint fixes** — golangci-lint errcheck compliance

## v0.1.0

- **Initial release** — `tws add`, `tws new`, `tws open`, `tws sync`, `tws stack`
- **Configurable workspace** — TWS_ROOT env, global config, repo-relative sibling default
- **Auto-detect** — workspace and worktree detection with `.tws-workspace` marker
- **Git worktree** — direct `git worktree add`, no worktrunk dependency
- **Stacked diffs** — `stack.yaml` with dependency tracking and topo-sorted sync
