# Changelog

## Unreleased

- **Agent work status** — new `tws status [feature] [--json]` reports what tws
  knows about every logical branch: materialization, tws-launched runtimes, and
  whether anything needs attention. With no argument it always covers every
  feature in the resolved workspace, from any working directory; no field in the
  document is derived from the process working directory
- **Versioned two-axis schema** — `--json` emits `schema_version: 1` with a
  stable key set, absent values as `null` and lists never `null`.
  `runtime_presence` (`present|absent|stale|unknown`) and `agent_state`
  (`working|ready|blocked|done|unknown`) are permanently separate axes.
  `agent_state` is **always `unknown`** at this version: tws launches agents but
  does not observe their turns. Use `needs_attention`, which is authoritative
- **Hierarchical attention** — `entry`, `feature`, and `workspace` each carry
  `needs_attention > active > idle`. Attention inherits upward and never smears
  downward, while `issue_count` and `codes` stay own-scope, so a level may read
  `needs_attention` with `issue_count: 0` because a child does. Every issue has
  exactly one home in `report.issues[]`
- **Human output shows the whole verdict** — the header always carries
  `Attention: <status>` for the workspace, and every issue is printed in a block
  keyed by its own home (`Branch: <feature>/<name>`, `Feature: <feature>`,
  `Workspace:`) with its code, message, and guidance. A branch that reads
  `[!] attn` therefore never hides its remediation behind `--json`. The tail
  counts branches only
- **Exit 0 on attention** — unlike `tws doctor`, `tws status` exits 0 whenever a
  report was produced, including for stale or corrupt operational state. A
  non-zero exit means no report could be produced at all (unresolvable
  workspace, unreadable metadata root, untrusted `spaces.yaml`, ambiguous
  feature layout, unknown feature)
- **External direct session records** — external `tws open` (the default,
  non-tmux path) now writes one hidden per-invocation record under
  `<feature>/.sessions/<branch-id>/<token>.json` (`0700` directories, `0600`
  files, random ownership tokens). Records are created before the agent is
  spawned, updated at each stage transition, and removed by token on exit;
  concurrent opens on one branch are supported and individually observable.
  `tws status` never removes a record, not even a provably dead one
- **`tws close` (external) consults records before tmux** — a behaviour change.
  A live direct record refuses the close, names the live pids, kills no tmux
  session, and removes nothing. With only provably stale records, they are
  cleaned and the command then kills tmux, or exits 0 with a cleanup message
  where it previously reported `no tmux session found`. Records that are neither
  live nor provably dead (corrupt, or owned by another user) are never removed
  and never block, but are always listed before tmux is touched; when they are
  all that remains and there is no tmux session, the error names them and points
  at `tws status --json` instead of reporting a flat `no tmux session found`.
  With no records at all the behaviour is byte-for-byte unchanged
- **`tws close` (external) is now feature-name guarded** — it resolves a
  caller-supplied name under `TwsRoot()` and mutates files beneath it, so a
  registered space name is refused. Consequently a malformed or untrusted
  `spaces.yaml` now makes external `tws close` fail closed, where it previously
  succeeded
- **Feature-name guard now rejects unusable names** — `GuardFeatureName`
  validates the name before it reads the registry, so every guarded command
  (`close`, `status`, `open`, `add`, `new`, `archive`, `rename`, `delete`,
  `sync`, `export`, `import`, `template sync`, `hooks install`) refuses a path
  separator, a traversal segment such as `../outside`, or a reserved directory
  name before any path join, stat, record read, removal, or tmux call. The
  message is the resolver's existing one. `tws template sync <invalid-name>` and
  `tws hooks install <invalid-name>` therefore now exit nonzero instead of
  printing a per-feature line and exiting 0
- **External `rename`, `archive`, and `delete` refuse live records** — anything
  not provably dead (live, EPERM, or corrupt) blocks the operation before any
  Git command, rename, or worktree removal; provably stale records are cleaned
  first. tws never kills a direct process. Checkout mode never writes or reads
  direct records
- **`openDirect` no longer calls `os.Exit`** — a missing agent binary, a failed
  spawn, and a broken record store are returned as errors through `RunE` from
  all four call sites
- **Secrets are never emitted** — no checkout `lock_token`, no lock-owner
  `token`, no session `links`, no transcript, prompt, argv, or environment. Only
  an 8-character `record_id` prefix of a direct record's ownership token appears.
  A direct record's file path is an ownership token in its basename, so every
  operator-facing message, refusal, guidance string, and error renders it
  redacted as `<dir>/<record-id>*.json`
- **`gitActiveOp` now also detects an in-progress `git bisect`**, so `tws doctor`
  and `tws status` report `active_git_op: bisect` where `doctor` previously
  reported nothing

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
