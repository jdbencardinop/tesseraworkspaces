# Analysis

## Summary

`tws` today resolves exactly one thing: the workspace for the current repository, plus an
opt-in global registry of *workspaces*. There is no way to record where a workspace's
tool-owned sibling spaces (learning, tickets, patching, research, documentation) live, so
that knowledge is either lost or hard-coded into agent skills — which are embedded,
immutable build artifacts. This feature adds a workspace-scoped `spaces.yaml` discovery
registry plus `tws space add/list/show/remove`, keeping `tws` authoritative for *location*
only, never for the linked tool's schema or lifecycle. Nothing equivalent exists in the tree
today, so this is net-new surface rather than a re-implementation; it is additive, opt-in,
and creates no file unless the user runs a `space` command. The main real risks are anchor
divergence between the two workspace-root resolvers, collision with external-mode feature
listing, and path-validation/traversal handling — all addressable, so the feature is viable.

## Upstream / already-present check

Not present. `git grep` finds no `space`/`spaces.yaml` symbol, no `spaceCmd`, and no
workspace-level (non-feature, non-global) YAML file of any kind. The nearest neighbours are:

- `internal/registry.go` — a *global*, XDG-scoped registry of repos/workspaces
  (`~/.local/share/tws/registry.yaml`), not workspace-scoped and not about sibling tools.
- `internal/decisions.go`, `internal/stack.go` — *feature*-scoped YAML, one level too deep.

No language/framework/license blocker: Go 1.26.1, `gopkg.in/yaml.v3` and `spf13/cobra` are
already direct dependencies and are exactly what this feature needs.

## Current behaviour and missing capability

Workspace resolution has two independent entry points that can disagree:

- `internal.RequireWorkspace()` (`internal/workspace.go`) → `MainRepoRoot()` →
  `ResolveCurrentWorkspaceE` → `Workspace.MetadataRoot`. Repo-config driven; falls back to
  `DetectWorkspaceRoot` + `inferExternalRepoRoot` when cwd is not a Git repo (this is the
  path that makes external workspace-root and feature-dir invocation work, per
  `internal/cli/external_feature_dir_test.go`).
- `internal.TwsRoot()` (`internal/paths.go`) → `TWS_ROOT` env, then `DetectWorkspaceRoot`
  (`.tws-workspace` marker walk-up → configured workspaces → `~/tws`), then
  `resolveWorkspaceMetadataRoot`.

These are **not** equivalent. Verified experimentally in a throwaway temp repo: with
`TWS_ROOT` set and cwd at the repo root, `TwsRoot()` returned the env root while
`RequireWorkspace().MetadataRoot` returned `<repo>.tws`. Any `spaces.yaml` anchor must pick
one resolver deliberately; using both would let `space add` and `space list` read and write
different files from different directories.

Missing capability: no persisted, workspace-level mapping from a stable name to a sibling
directory, and no command surface to manage or query it. Skills (`assets/skills/...`) are
embedded via `assets/skills/embed.go` and installed by `internal/cli/init.go` /
`internal/cli/add.go`; because they are compiled in, any path written into them is frozen at
build time — which is precisely why mutable paths must not live there.

## Affected real files and symbols

New (expected):

- `internal/spaces.go` — `SpacesFile`, `SpaceEntry`, load/validate/save, path resolution.
- `internal/cli/space.go` — `spaceCmd()` with `add`/`list`/`show`/`remove` subcommands.
- `internal/spaces_test.go`, `internal/cli/space_test.go`.

Existing files that must change or be re-checked:

- `internal/cli/root.go` — register `spaceCmd()` in the `rootCmd.AddCommand(...)` list.
- `internal/resolve.go` — `reservedDirs` currently contains `config.yaml`, `features`,
  `state`, `templates`, `hooks`, `skills`. `spaces.yaml` should be added so
  `validateFeatureName` rejects a feature named `spaces.yaml` and `isReservedDir` keeps it
  out of `ListFeaturesResolved`, `LegacyFeatureNames`, and `DetectFeatureFromCwdE`.
- `internal/workspace.go` — `Workspace.MetadataRoot`, `EnsureExternalWorkspaceMarker`
  (needed when `space add` runs before any `tws add` has created the external root).
- `internal/resolve.go` — `ResolveFeaturePathOrLegacy` for validating `--feature` scope,
  including its `*ErrAmbiguousFeature` return.
- `assets/skills/claude/tesseraworkspaces/SKILL.md`,
  `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
  `assets/skills/copilot/tws.prompt.md` — teach `tws space list` as the discovery step;
  the workflow contract requires skills to be updated on user-facing change.
- `docs/roadmap.md`, `docs/engineering-workflow.md`, `README.md`, `CHANGELOG.md`.

Reference implementations to imitate (not to modify): `internal/registry.go`
(`decodeRegistry` version probe + `dec.KnownFields(true)`, `validateRegistry`,
`saveRegistry` temp+`Sync`+`Chmod`+`Rename`), and `internal/checkout_sync.go`
(`atomicWriteFile`, `AcquireCheckoutLock`/`writeLockExclusive` PID-lock pattern).

## Proposed storage semantics for both workspace modes

Anchor on `Workspace.MetadataRoot` from `RequireWorkspace()` — it is the only resolver that
is mode-aware, error-returning, and already exercised from all five invocation sites in the
retrospective's required regression matrix.

- external: `<repo>.tws/spaces.yaml` (sibling to `.tws-workspace/` and the feature dirs).
- checkout: `<repo>/.tws/spaces.yaml` (sibling to `features/`, `state/`, `config.yaml`).

In both modes the file is local-only and never committed: the external root is outside the
repo, and `.tws/` is added to `.git/info/exclude` by `EnableCheckoutMode` /
`AddGitLocalExclude` in `internal/enable.go`. Agents on a fresh clone therefore start with no spaces; the
skills must teach `tws space list` as a query that can legitimately return empty.

Entry shape (mirroring the request): `name` (stable identifier, alias-style charset),
`kind` (free-form label such as `learning`/`tickets`/`patching`/`research`/`docs` —
`tws` must not enumerate a closed set it does not own), `path`, optional `description`,
optional `feature` scope. File carries `version: 1` like `RegistryFile`.

Paths: store absolute paths canonically (as `validateRegistry` requires for registry
entries) **or** workspace-relative paths that are explicitly marked as such. Relative paths
resolve against `MetadataRoot`. Existence is validated (`os.Lstat`), but Git-ness is not:
unlike `inspectRegistryTarget`, which hard-fails with "is not a Git repository or tws
external workspace", `space add` must accept any existing directory.

Feature-scoped entries validate the feature name through `ResolveFeaturePathOrLegacy` so
checkout legacy layout and `ErrAmbiguousFeature` are surfaced rather than swallowed.

## Compatibility and migration

- Fully additive. No existing command reads or writes `spaces.yaml`; absence is the normal
  state and must never be an error (same contract as `readRegistry` returning `nil, nil`).
- No migration is required and none should be invented — there is no prior format.
- External multi-worktree behaviour is untouched: no change to `TwsRoot`, `FeaturePath`,
  `WorktreePath`, worktree creation, sync, or inject.
- One real compatibility hazard: in **external** mode `ListFeaturesResolved` treats *every*
  non-reserved directory under `MetadataRoot` as a feature. A sibling space physically
  placed inside the external workspace root (e.g. `<repo>.tws/learning/`) will therefore
  appear in `tws list`, `tws doctor`, and `inferExternalRepoRoot`'s scan as a phantom
  feature. Registering such a path must be either rejected, warned about, or handled by an
  explicit exclusion rule; the spec must decide.
- `spaces.yaml` is a file, not a directory, so it does not itself pollute feature listing —
  but adding it to `reservedDirs` is still required to block a same-named feature.
- `WorkspaceExport` (`internal/export.go`) is feature-scoped; workspace-level spaces are
  deliberately outside export/import for this slice.

## Security and data-integrity risks

- **Anchor divergence** (verified above): `TWS_ROOT` vs repo-config resolution can point at
  two different roots from two different cwds. Highest-severity correctness risk.
- **Path traversal / escape**: workspace-relative paths must be rejected when they escape
  `MetadataRoot` after `filepath.Clean`; absolute paths must be canonical
  (`filepath.IsAbs` + `Clean(p) == p`), matching the invariant `validateRegistry` enforces.
- **Symlink drift**: the codebase deliberately resolves symlinks (`canonicalize`,
  `filepath.EvalSymlinks`) in some places and rejects them outright in others
  (`dirExists`, `MigrateFeatureLayout`). The spec must pick one policy for space targets and
  state it, rather than inheriting whichever helper is convenient.
- **Torn writes**: `stack.yaml`/`decisions.yaml` use plain `os.WriteFile`, but newer state
  files do not. Follow the newer convention (`registry.saveRegistry` or
  `checkout_sync.atomicWriteFile`): temp file → write → `Sync` → `Chmod` → `Rename`.
- **Concurrent writers**: `AcquireRegistryLock` locks `registryDir()` globally and is not
  reusable for a per-workspace file; `flockExclusive`/`flockUnlock` in
  `internal/registry_lock_unix.go` are `//go:build !windows` and could be reused for a
  workspace-scoped lock file, or the `writeLockExclusive` PID-lock pattern can be followed.
  A decision is required — silently racing concurrent `space add` calls is not acceptable.
- **Schema forward-compatibility**: decode with a permissive version probe first, then
  `dec.KnownFields(true)`, so a newer-version file is reported as a version error and never
  silently truncated by a rewrite (the exact failure mode `decodeRegistry` guards against).
- **Information disclosure**: `spaces.yaml` records filesystem paths that may name customer
  or client directories. It is uncommitted in both modes, which is good, but the file mode
  should follow the stricter local-state convention rather than world-readable `0644`.
- **Never own the target**: `space remove` deletes a registry line only, never files —
  the same guarantee `RegistryRemove`/`RegistryPrune` already make.

## CLI and automation considerations

- Subcommand grouping should mirror `registryCmd()` in `internal/cli/registry.go`: a parent
  `space` command with `add`/`list`/`show`/`remove` children, `RunE` everywhere (never
  `os.Exit`), and output through `cmd.OutOrStdout()` so tests can capture it.
- `--json` on `list` and `show` is effectively mandatory: agents are the primary consumer,
  and the existing registry contract already promises a JSON array (`[]` when empty) with
  snake_case keys. Match it.
- No-argument behaviour: `tws space list` with no args must work from the source repo root,
  a linked worktree root or nested subdir, the external workspace root, an external feature
  directory or nested subdir, and the checkout repo root — the exact matrix demanded by
  `docs/retrospectives/v1.2.7-upgrade-operations.md`. `RequireWorkspace()` covers all of
  these; a direct `MainRepoRoot()` call would reintroduce the `not inside a git repository`
  regression that retrospective recorded.
- `tws space add` may be the first command run in a brand-new external workspace, so it must
  create the root and marker via `EnsureExternalWorkspaceMarker`, as `addExternal` does.
- Cobra `ValidArgsFunction` completion for space names, consistent with
  `internal.ListFeatures()` usage in `doctor.go`/`open.go`.
- Missing/broken targets should be *reported*, not auto-repaired or auto-pruned; interactive
  warnings with exit 0 match the `doctor` convention.
- Skills teach the *command*, never a path. The skill tables in
  `assets/skills/claude/tesseraworkspaces/SKILL.md` and `assets/skills/copilot/tws.prompt.md`
  gain a `tws space ...` row plus a short discovery section.

## Testing implications

- Real temporary Git repos with local bare remotes, per `setupGitRepo` in
  `internal/cli/new_integration_test.go` (init bare remote, `push -u`, `symbolic-ref HEAD`,
  `remote set-head`) and `setupGitRepoCheckout` in `internal/cli/checkout_lifecycle_test.go`.
  No mock-only Git coverage.
- Real linked worktrees created through the existing `createWorktree` helper, then
  `tws space list` executed from the worktree root and a nested subdirectory.
- Full invocation matrix from the retrospective: repo root; worktree root; nested worktree
  dir; external workspace root; external feature dir; nested external feature dir; checkout
  repo root. `internal/cli/external_feature_dir_test.go` is the template.
- Both modes exercised independently, asserting the file lands at `<repo>.tws/spaces.yaml`
  and `<repo>/.tws/spaces.yaml` respectively.
- Isolation via `t.Setenv("HOME", ...)`, `t.Setenv("TWS_ROOT", ...)`, `t.TempDir()`, and
  `t.Setenv("XDG_DATA_HOME", ...)` so the developer's real registry is never touched.
- Negative cases: traversal path, non-canonical absolute path, non-existent target,
  duplicate name, non-Git target (must succeed), unknown YAML field, future schema version,
  malformed file, feature-scoped entry naming an ambiguous checkout feature.
- Regression: `tws list`, `tws doctor`, `tws stack`, `tws sync` unchanged in external mode
  after `spaces.yaml` exists in the workspace root.
- Gates per `docs/engineering-workflow.md`: `go test ./... -count=1`, `go vet ./...`,
  `golangci-lint run ./...`, `make build`, `gofmt -w`, plus a CLI smoke test.

## Dependency assessment

Current declared dependencies (`status.json`): soft on `workspace-hub-research` (state:
`requested`) and soft on `workspace-registry` (state: `applied`). These are **not
sufficient** as an ordering contract:

- `workspace-hub-research` is unimplemented research and the multi-project super-hub is
  explicitly out of scope; keeping it **soft** is correct — it must not gate this work.
- `workspace-registry` is a conventions/pattern reference (schema versioning, atomic write,
  validation, selector rules), not a compile-time dependency. **Soft is correct.**
- Missing: `workspace-mode-foundation` (`applied`). This feature's storage location is
  defined entirely in terms of `Workspace.MetadataRoot` and the external/checkout split that
  feature introduced. Recommend adding it as a **hard** dependency.
- Missing: `fix-external-feature-dir-resolution` (`applied`). No-argument auto-detection
  from an external workspace root or feature directory depends on the `RequireWorkspace()`
  marker-fallback path that fix restored. Recommend adding it as a **hard** dependency.

Recommendation only — dependencies were not mutated here. Both proposed parents are already
`applied`, so adding them cannot create a cycle; `tpatch feature deps --validate-all` should
still be run after they are registered, per `.tpatch/steering/local.md`.

No new Go module dependency is needed: `gopkg.in/yaml.v3` and `spf13/cobra` already cover
serialization and CLI, and `go.mod` should not change.

## Unresolved decisions for the spec

1. Anchor resolver: confirm `RequireWorkspace().MetadataRoot`, and define the behaviour when
   `TWS_ROOT` disagrees with it (honour, error, or warn).
2. Path storage form: absolute-only, or absolute plus explicitly-marked
   workspace-relative — and the exact escape/canonicalization rule.
3. Symlink policy for space targets: resolve (like `canonicalize`) or reject (like
   `dirExists`).
4. Concurrency: workspace-scoped `flock` reuse vs. PID lock file vs. documented
   single-writer assumption.
5. External-mode collision: reject, warn, or exclude when a registered space lives inside
   the external workspace root and would appear as a phantom feature.
6. `kind` validation: free-form string vs. a validated-but-open set; `tws` must not become
   the authority on the linked tool's taxonomy.
7. Feature-scoped entry lifecycle: what happens to `feature`-scoped spaces on
   `tws rename feature` and `tws delete <feature>`.
8. Whether `show` reports target health (exists / missing / not-a-directory) or stays purely
   declarative, and whether `tws doctor` gains a spaces section in this slice or later.
9. File permissions for `spaces.yaml` (`0600` local-state style vs. `0644` metadata style).
10. Exact JSON key names and empty-array contract, to stay consistent with
    `tws registry list --json`.

## Implementation viability

Viable and low-risk. The change is purely additive, needs no new module dependency, and
every primitive it requires — mode-aware root resolution, versioned+validated YAML, atomic
writes, advisory locking, Cobra subcommand grouping with `--json`, and real-Git integration
test harnesses — already exists in this tree and can be followed rather than invented. The
only genuine design hazards are the verified `TwsRoot()`/`RequireWorkspace()` anchor
divergence and the external-mode phantom-feature collision; both are containable by explicit
decisions in the spec. Recommend proceeding to `define` once items 1, 2, and 5 above are
settled.
