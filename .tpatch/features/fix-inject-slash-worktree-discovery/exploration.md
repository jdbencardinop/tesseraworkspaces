# Exploration

## Existing control flow

- `internal/cli/inject.go`
  - `RequireFeaturePath` provides guarded, mode-aware feature selection.
  - Branch-specific injection calls `RequireWorktreePath` and then
    `InjectFiles`.
  - Feature-wide injection calls `InjectFilesForFeature`.
  - This is the correct place to preserve ambiguity/space-guard precedence and
    add an explicit checkout capability refusal before either route writes.
- `internal/inject.go`
  - `InjectFiles` already preserves the required relative symlink,
    existing-file skip, recursive inject source, and `injectInto` behavior.
  - `InjectFilesForFeature` is the defect: it reads only immediate
    `worktrees/` children, silently maps read errors to zero targets, writes
    warnings directly, and counts any directory whose injection returns nil.
  - This file is the smallest shared boundary for metadata-driven discovery,
    legacy fallback, destination verification, deterministic ordering, and
    contextual errors.
- `internal/agent_status.go`
  - `BuildWorktreeInventory` establishes prior art for fail-closed worktree
    registration checks. Injection uses the same authoritative Git inventory
    concept but consumes `--porcelain -z` directly so pathname whitespace is
    byte-preserving.
- `internal/rebase_plan_probe.go`
  - `resolveWorktreeGitDir` establishes prior art for symlink-free linked
    worktree pointer parsing. Injection additionally verifies the admin
    `gitdir` backpointer names the exact selected `.git` file.
- `internal/resolve.go`
  - `RequireFeaturePath` resolves the selected root and preserves ambiguity and
    sibling-space errors.
  - `RequireWorktreePath` already returns `ErrWorktreeUnsupported` in checkout
    mode, but it only joins paths in external mode.
- `internal/workspace.go`
  - `Workspace.WorktreePath` joins the full logical name and returns empty in
    checkout mode.
  - `filesystemGitRepositoryRoot` demonstrates strict `.git` marker parsing,
    but fallback discovery must additionally distinguish linked-worktree files
    from arbitrary repository directories and prove the exact top-level path.
- `internal/stack.go`
  - `StackEntry.Name` is the logical path identity.
  - `StackEntry.GitBranch()` is deliberately separate and must not participate
    in injection path construction.
  - `LoadStack` cleanly distinguishes absent metadata from malformed or
    unreadable metadata.

## Shared callers

- `internal/cli/template.go:syncFeatureTemplate` calls
  `InjectFilesForFeature` after copying template assets. It already has a
  warning convention for helper errors.
- `internal/cli/importcmd.go:recreateExternal` creates worktrees from imported
  stack entries, then discards the feature-wide injection error. It should
  retain its existing workflow and return a contextual injection error.
- `internal/cli/new.go`, `open.go`, and `rename.go` call `InjectFiles` for one
  freshly resolved target. They are not feature-wide discovery surfaces and
  do not require unrelated redesign.

## Existing tests and fixture constraints

- `internal/cli/checkout_lifecycle_test.go`
  - `TestInject_PropagatesAmbiguity` pins ambiguity precedence.
  - `setupGitRepoCheckout` and `gitInDir` show checkout fixtures and explicit
    per-process identity.
- `internal/cli/external_feature_dir_test.go`
  - exercises `inject` from a nested external feature directory and protects
    cwd/workspace resolution.
- `internal/cli/space_guard_test.go`
  - command matrices protect registered sibling-space exclusion and template
    sync's existing error conventions.
- `internal/cli/new_integration_test.go`
  - provides real bare remote/repository/worktree patterns, but its generic
    `writeAndCommit` relies on repository config. New injection fixtures will
    use a private `GIT_CONFIG_GLOBAL` and explicit identity on commit
    subprocesses because CLI `TestMain` resets `GIT_CONFIG_COUNT=0`.
- There are no dedicated injection tests at the released base. Add focused
  `internal/inject_test.go` coverage for discovery/linking safety and
  `internal/cli/inject_test.go` coverage for command, mode, caller, cwd, and
  root-selection behavior.

## Documentation surfaces

- `README.md` context injection and command table.
- `docs/cheatsheet.md` context injection examples.
- `internal/cli/inject.go` long help.
- `assets/skills/claude/tesseraworkspaces/SKILL.md`.
- `assets/skills/copilot/tws.prompt.md`.
- `docs/roadmap.md`, whose verified current target can advance from released
  issue #4 to issue #1 without changing the approved queue or Lane B sections.

## Minimal changeset

- Production: `internal/inject.go`, `internal/cli/inject.go`,
  `internal/cli/template.go`, `internal/cli/importcmd.go`.
- Tests: new focused injection test files plus only surgical additions to
  existing precedence/matrix tests if required.
- Docs: the six directly related surfaces listed above.
- Tpatch: this feature's Path B artifacts and lifecycle state only.
