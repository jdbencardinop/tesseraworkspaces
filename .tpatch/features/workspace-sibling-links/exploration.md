# Exploration — workspace-sibling-links

Implementation-grounded map for one implementer. Every path/line below was verified against the
working tree at `481b94b` (baseline: `go build ./...`, `go vet ./...`, `gofmt -l internal assets cmd`,
`golangci-lint run ./...` all clean — 0 issues). Line numbers are *current* and will drift as edits
are applied; anchor on the symbol name, use the line as a locator.

Nothing named `space`, `spaces.yaml`, `spaceCmd`, or `SpacesAnchor` exists in `internal`, `cmd`, or
`assets` today. This is net-new surface.

---

## 1. Existing resolution call graph (verified)

### 1.1 Root resolution — the two independent resolvers

```
internal.TwsRoot()                              internal.RequireWorkspace()
  paths.go:77                                     workspace.go:440
    MainRepoRoot()                                  MainRepoRoot() ok ─→ ResolveCurrentWorkspaceE(repo,cfg)  workspace.go:401
    LoadConfig(); os.Getwd()                          repoCfg .tws/config.yaml → workspace_mode
    resolveTwsRoot(env,cwd,repo,err,cfg) paths.go:56    checkout  → MetadataRoot = <repo>/.tws
      0. $TWS_ROOT                    (wins)           external  → resolveExternalRoot()  workspace.go:281
      1. DetectWorkspaceRoot(cwd,cfg) paths.go:13                   cfg.Workspaces[repo] | <repo>.tws
           t1 .tws-workspace walk-up                MainRepoRoot() err ─→ fallback tier
           t2 configured workspaces prefix            DetectWorkspaceRoot(cwd,cfg)   paths.go:13
           t3 ~/tws prefix                            metadataRootExists()           workspace.go:334
      2. resolveWorkspaceMetadataRoot() workspace.go:325            inferExternalRepoRoot()        workspace.go:339
      3. ~/tws                                          → Workspace{Mode: external, MetadataRoot: canonicalize(root)}
```

`TwsRoot()` **ignores workspace mode and honours `$TWS_ROOT` unconditionally**;
`RequireWorkspace()` **ignores `$TWS_ROOT` entirely**. That is the divergence the spec pins in §4.1
and refuses to reconcile (N11). Confirmed: `resolveTwsRoot` returns `envRoot` before any workspace
logic (`paths.go:57-60`), and `ResolveCurrentWorkspaceE` never reads the env.

### 1.2 Feature-path resolution

| Entry | Location | Root actually used |
| --- | --- | --- |
| `internal.FeaturePath(f)` | `paths.go:84` | `TwsRoot()` |
| `internal.WorktreePath(f,b)` | `paths.go:88` | `TwsRoot()` |
| `Workspace.FeaturePath(f)` | `workspace.go:296` | `w.MetadataRoot` (`features/` in checkout) |
| `Workspace.LegacyFeaturePath(f)` | `workspace.go:305` | `w.MetadataRoot` |
| `Workspace.ResolveFeaturePath(f)` | `resolve.go:36` | `w.MetadataRoot`; checkout prefers `features/<f>`, falls back legacy, `*ErrAmbiguousFeature` when both |
| `Workspace.ResolveFeaturePathOrLegacy(f)` | `resolve.go:67` | same; `("", nil)` when absent |
| `internal.RequireFeaturePath(f)` | `resolve.go:247` | `RequireWorkspace()` + `ResolveFeaturePath` → `ws.MetadataRoot` |

### 1.3 Feature listing

| Entry | Location | Root scanned | Error channel |
| --- | --- | --- | --- |
| `Workspace.ListFeaturesResolved()` | `resolve.go:117` | `w.MetadataRoot` (+`/features`) | `([]string, error)` — **always `nil` error today** |
| `Workspace.LegacyFeatureNames()` | `resolve.go:99` | `w.MetadataRoot`, checkout only | none |
| `internal.ListFeatures()` | `paths.go:139` | `ws.MetadataRoot` via `ListFeaturesResolved`, else raw `os.ReadDir(TwsRoot())` at `paths.go:147` | none (swallows) |
| `internal.ListBranches(f)` | `paths.go:162` | `RequireFeaturePath` → `MetadataRoot` | none (swallows) |
| `internal.DetectFeatureFromCwd()` | `paths.go:95` | `TwsRoot()`; requires `worktrees/` **or** `stack.yaml` (`paths.go:126-131`) | none |
| `Workspace.DetectFeatureFromCwdE()` | `resolve.go:206` | lexical only, **no non-test callers** (`resolve_test.go:208,:223`) | none |

`ListFeaturesResolved` non-test callers (all already propagate the error, **no source change needed**):
`internal/cli/list.go:36`, `internal/cli/open_checkout.go:61`, `internal/checkout_health.go:497`
(`buildFeatureEntries`), `internal/checkout_health.go:867` (`BuildCheckoutList`), plus
`internal/paths.go:141`.

### 1.4 Mode dispatch shape (external ↔ checkout)

Every lifecycle command follows the same shape: `RunE` → `internal.RequireWorkspace()` →
`if ws.Mode == internal.ModeCheckout { …Checkout(ws, …) } else { …External(…) }`. The **external**
half then re-derives paths from `internal.FeaturePath`/`WorktreePath` (i.e. `TwsRoot()`), while the
**checkout** half uses `ws.*`. This asymmetry is exactly why §7.6 assigns two different guard roots.

---

## 2. `internal/spaces.go` — new types and symbols

Package `internal`; `internal/cli` already imports `internal`. **No package cycle is possible and
nothing needs to be duplicated or extracted.** All helpers below are same-package.

### 2.1 Reuse map (verified existing unexported/exported utilities)

| Need | Reuse | Location | Note |
| --- | --- | --- | --- |
| name charset | `ValidateAlias` + `aliasRegexp` | `registry.go:911,913` | `^[a-zA-Z0-9._-]+$`, ≤64. Exported, reuse verbatim; add explicit `.`/`..` rejection on top (`ValidateAlias(".")` currently **passes**) |
| abs+clean | `cleanAbsolute` | `workspace.go:212` | |
| symlink-resolved abs | `canonicalize` | `workspace.go:205` | §6.6 step 3 and `SpacesAnchor.Canon` |
| feature-name safety | `validateFeatureName` | `resolve.go:171` | used by `AnchorFeaturePath` |
| reserved-name test | `isReservedDir` | `resolve.go:166` | `.`-prefix rule keeps `.spaces.lock` out of listings |
| dir test rejecting symlinks | `dirExists` | `resolve.go:191` | **only** for `AnchorFeaturePath` (mirror `ResolveFeaturePath`). **Do not** use for space targets — §6.7 requires `os.Stat` (follows symlinks) |
| atomic write | pattern of `saveRegistry` | `registry.go:264-308` | CreateTemp → Write → Sync → Close → Chmod(0600) → Rename, `errors.Join` on cleanup |
| decode staging | pattern of `decodeRegistry` | `registry.go:164-192` | probe `version` → range check → `dec.KnownFields(true)` → validate |
| absent-file contract | pattern of `readRegistry` | `registry.go:147-162` | `errors.Is(err, fs.ErrNotExist)` → `(nil, nil)` |
| advisory lock | `flockExclusive` / `flockUnlock` | `registry_lock_unix.go:13,21` | call directly; **do not** reuse `AcquireRegistryLock` (`registry.go:114`) — it locks `registryDir()` (XDG), not a workspace root |
| lock struct shape | `RegistryLock` + `Release()` | `registry.go:108,136` | `errors.Join(unlockErr, closeErr)` |
| external marker | `EnsureExternalWorkspaceMarker` | `workspace.go:192` | `space add` external only |
| test-only step hook precedent | `StepHook` | `checkout_sync.go:303` | precedent for the failure-injection hooks in §8.3 |
| `atomicWriteFile` | `checkout_sync.go:128` | | **not used**, but not for the reason a first read suggests: it *does* set the mode (`tmp.Chmod(mode)` at `:136`), so `0600` would be honoured. It is rejected because it `os.MkdirAll(filepath.Dir(path), 0700)` unconditionally at `:129` — it would **create the spaces root** (at the wrong mode: §8.6/§10.1 require `0755` and only `space add` may create it), which §8.5 forbids absolutely for every other writer. Use the `saveRegistry` shape, which never creates its directory as a side effect of writing |

**Nothing must be duplicated.** No containment helper exists today (`filepath.Rel` is open-coded in
`resolve.go:210,219`, `paths.go:111`, `importcmd.go:166`); `containsPath` is genuinely new.

### 2.2 Symbol inventory for `internal/spaces.go`

| Symbol | Kind | Spec |
| --- | --- | --- |
| `spacesVersion = 1`, `spacesFileName = "spaces.yaml"`, `spacesLockName = ".spaces.lock"` | const | §15.1 |
| `SpacesAnchor{Root, Canon string; Mode WorkspaceMode}` | type | §4.1 — **no `Legacy` field** (criterion 6) |
| `SpacesFile`, `SpaceEntry` | type | §5 (`UpdatedAt *time.Time`) |
| `SpaceStatus`, `SpaceScope`, `SpaceScopeStatus`, `SpaceView` | type | §5 |
| `SpaceOwners{TopLevel, Features map[string]string; root string; targets []spaceOwnerTarget}` + `TopLevelOwner/FeatureOwner/OwnerOfDir` | type | §7.1 — maps are the exact-spelling fast path; the methods add `os.SameFile` identity so an absolute in-root, symlinked, or differently cased spelling is still owned. Every consumer uses the methods, never map indexing |
| `spaceOwnerTarget{space, path, canon string; info os.FileInfo}` | type | §7.1 — one resolved registered target plus its stat result |
| `SpaceListResult{Views []SpaceView; Total int; ScopeFeature string}` + `Scope()` | type | §10.2 — list metadata returned from the one read; the CLI never rereads `spaces.yaml` for its header or empty state |
| `SpaceSelector{Name string; Scope SpaceScopeSelector; Feature string}` + `NewSpaceSelector` | type | §10.3 — the explicit `--feature` / `--workspace` scope selector threaded into `SpaceShow` and `SpaceRemove` |
| `SpacesLock{f *os.File}` + `Release() error` | type | §9 |
| `ErrSpaceNameConflict{Feature, Space, Root string}` + `Error()` | type | §7.3, must be a typed error so all call sites format identically |
| `SpacesRenameTx`, `SpacesDeleteTx` | type | §12.1/§12.2 |
| `ResolveSpacesAnchor() (SpacesAnchor, error)` | func | §4.1 tiers 1–4 |
| `spacesPath(root)`, `spacesLockPath(root)` | func | |
| `acquireSpacesLock(root) (*SpacesLock, error)` | func | `MkdirAll(root,0755)` only if missing, `OpenFile(...,0600)`, `flockExclusive`. **Because it creates the root and the lock file, every caller except `SpaceAdd` must `os.Lstat` the registry path first and skip acquisition when it is absent (§8.5)** |
| `readSpaces(root) (*SpacesFile, error)` | func | §8.1 — `os.Lstat` symlink refusal, `ReadFile`, `decodeSpaces` |
| `decodeSpaces(data, path)`, `validateSpaces(f, path)` | func | §8.1/§6 |
| `saveSpaces(root, f) error` | func | §8.2 |
| `sortSpaces(f)` | func | `(feature, name)`, `feature==""` first |
| `SpaceDirOwners(root) (SpaceOwners, error)` | func | §7.1 — one read; wraps every non-absent failure as ``cannot verify registered spaces in <root>/spaces.yaml: %w`` |
| `ownersFrom(root string, f *SpacesFile) SpaceOwners` | func | pure, testable |
| `isFeatureDir(dir) bool` | func | signals: `stack.yaml`, `worktrees/`, `FEATURE.md` (see 2.3) |
| `GuardFeatureName(root, feature) error` | func | §7.3 |
| `guardFeatureNameIn(owners SpaceOwners, root, feature) error` | func | locked-transaction form |
| `AnchorFeaturePath(anchor, owners, feature) (string, error)` | func | §7.4 — no file read |
| `containsPath(dir, target) bool` | func | §12.0 inclusive, lexical fast path |
| `pathContains(dir, target) bool`, `ancestorMatches`, `samePathSpelling`, `sameTargetDir`, `canonicalPath` | func | §12.0 — identity-aware containment and equality; `canonicalPath` canonicalizes through the longest existing ancestor so a not-yet-created path stays comparable with a symlinked root |
| `spaceRelUnderRoot(root, stored, resolved) (string, bool)` | func | §7.1 — root-relative form of an entry, covering absolute stored paths that resolve inside the root |
| `describeSpaceRemoveCommands(entries)`, `spaceRemoveCommand(entry)` | func | §12.1/§12.2 — scope-qualified removal guidance per blocker |
| `normalizeSpacePath(anchor, input) (stored, resolved string, err error)` | func | §6.6 |
| `SpaceStatusOf(resolved) SpaceStatus` | func | `os.Stat`, follows symlinks |
| `spaceScopeStatus(anchor, feature) SpaceScopeStatus` | func | direct `<root>/<f>` / `<root>/features/<f>` existence, never `ResolveFeaturePath` |
| `detectAnchorFeature(anchor, owners, cwd) string` | func | **name not fixed by the spec** — §10.2 anchor-rooted cwd walk; drops a first segment owned by `owners` |
| `SpaceAdd/SpaceList/SpaceShow/SpaceRemove(anchor, …)` | func | §10 — every mutator except `SpaceAdd` `os.Lstat`s `spacesPath(root)` **before** `acquireSpacesLock`; `SpaceRemove` on an absent file returns `no space named <name>` and creates nothing (§8.5) |
| `BeginSpacesFeatureDelete(root, feature, featurePath) (*SpacesDeleteTx, error)` | func | §12.2 — same `Lstat`-before-lock probe |
| `BeginSpacesFeatureRename(root, old, new, oldPath, newPath) (*SpacesRenameTx, error)` | func | §12.1 — same `Lstat`-before-lock probe |
| `SpacesSaveHook func(root string) error` (**exported**, nil in prod) | var | test-only deterministic write failure — see §8.3 below |
| `spacesReadHook func(path string)` (unexported, nil in prod) | var | read-count instrumentation for criteria 26/21 |

**Recursion fence (spec §7.1).** None of `readSpaces`, `decodeSpaces`, `validateSpaces`,
`ownersFrom`, `isFeatureDir`, `SpaceDirOwners`, `GuardFeatureName`, or `guardFeatureNameIn` may
call `ResolveFeaturePath`, `ResolveFeaturePathOrLegacy`, `ListFeaturesResolved`,
`LegacyFeatureNames`, `ListFeatures`, `ListFeaturesE`, `RequireFeaturePath`, `RequireWorkspace`,
or `TwsRoot`. `ListFeaturesResolved` calls `SpaceDirOwners` (§7.1 of this file), so any such call
is unbounded recursion, not merely a layering smell. Feature-ness inside `internal/spaces.go` is
decided by direct `os.Stat`/`os.Lstat` probes on paths joined from the passed-in root only.
`ResolveSpacesAnchor` is the sole exception and is called by no other spaces helper — only by the
four `space` subcommands (§4.2).

Same rule for `space add`'s §7.2 "is it a feature of this workspace" test: probe
`<anchor.Root>/<name>` (external / checkout legacy) and `<anchor.Root>/features/<name>` (checkout
new layout) directly. Do **not** call `ws.ListFeaturesResolved()` — it recurses, and under
`TWS_ROOT` divergence it scans `ws.MetadataRoot` while the anchor is `TwsRoot()`, so it would
answer about the wrong root.

### 2.3 `isFeatureDir` signal justification (verified against real creators)

| Creator | Location | `worktrees/` | `stack.yaml` | `FEATURE.md` |
| --- | --- | --- | --- | --- |
| `addExternal` | `cli/add.go:59` | yes (`:65`) | on first `tws new` | yes (`:69`) |
| `createWorktree` | `cli/new.go:159` | yes | yes | no |
| `addCheckout` | `cli/add.go:111` | **no** | **no** | yes (`:120-122`) |
| `createCheckoutBranch` | `cli/new.go:59` | no | yes | — |
| `recreateCheckout` / `recreateExternal` | `cli/importcmd.go:190,250` | ext only | yes | yes |

Confirms the spec: `FEATURE.md` is mandatory in the signal set or a fresh `addCheckout` feature is
invisible to `isFeatureDir`. Also confirms `internal.DetectFeatureFromCwd` (`paths.go:126-131`)
checks only `worktrees/`/`stack.yaml` — it can never surface a plain sibling space, so §7.5's
"no change" decision holds.

---

## 3. Lock portability, build tags, file organization

- `internal/registry_lock_unix.go` is `//go:build !windows` and there is **no** `*_windows.go`
  counterpart anywhere in `internal` (verified). The package therefore **already** fails to compile
  on Windows. Reusing `flockExclusive`/`flockUnlock` from an untagged `internal/spaces.go`
  introduces **no new portability regression** and needs **no new build-tagged file**.
- **Do not** create `internal/spaces_lock_unix.go` / `_windows.go`. If Windows support is ever
  wanted, one stub `internal/registry_lock_windows.go` fixes both consumers at once — out of scope
  here (N9), and it must not be added in this slice.
- **Do not** rename or move `registry_lock_unix.go`; a rename would make the tpatch record noisier
  for zero behavioural gain.

Proposed file organization (final):

| File | Contents |
| --- | --- |
| `internal/spaces.go` (new, ~700 lines) | everything in 2.2 |
| `internal/spaces_test.go` (new) | unit layer |
| `internal/cli/space.go` (new) | `spaceCmd()` + 4 subcommands |
| `internal/cli/space_test.go`, `space_guard_test.go`, `space_lifecycle_test.go`, `space_migrate_test.go`, `space_matrix_test.go` (new) | see §8 |

If `internal/spaces.go` exceeds ~800 lines, the only acceptable split is
`internal/spaces_lifecycle.go` (the two transactions) — still untagged, same package. Do not split
by mode.

---

## 4. CLI wiring and command contracts

### 4.1 `internal/cli/root.go`

One line: add `spaceCmd(),` to the `rootCmd.AddCommand(...)` list (`root.go:23-49`), placed after
`registryCmd()` at `root.go:48`.

### 4.2 `internal/cli/space.go` contracts

Model on `internal/cli/registry.go:15-98` (parent `cobra.Command` with `Short` only + `AddCommand`;
children all `RunE`; output via `cmd.OutOrStdout()`; `--json` via
`json.NewEncoder(cmd.OutOrStdout())` + `SetIndent("", "  ")`; nil slice normalized to `[]T{}` before
encoding so empty prints `[]`).

| Command | Args | Flags | Contract |
| --- | --- | --- | --- |
| `space` | — | — | parent, `Short: "Manage workspace sibling space links"` |
| `space add <name> <path>` | `cobra.ExactArgs(2)` | `--kind` (**required**, `MarkFlagRequired`), `--description`, `--feature` | §10.1 order verbatim. Prints `registered: …` / `already registered: …` |
| `space list` | `cobra.NoArgs` | `--feature`, `--all`, `--kind`, `--json` | `--all` + `--feature` mutually exclusive (`cmd.MarkFlagsMutuallyExclusive("all","feature")`); `Long` documents the cwd-scoped default and `--all`; the human header always precedes results and carries the active scope |
| `space show <name>` | `cobra.ExactArgs(1)` | `--feature`, `--workspace`, `--json` | `--feature`/`--workspace` mutually exclusive; ambiguity/absence → exit 1; bad target status → exit **0** |
| `space remove <name>` | `cobra.ExactArgs(1)` | `--feature`, `--workspace` | `--feature`/`--workspace` mutually exclusive; prints `removed space: <name>` |

Completion (mirrors `doctor.go:24` / `open.go:32`):
`--kind` → `RegisterFlagCompletionFunc` returning the five conventional kinds (suggestion only,
never validation); `--feature` → `internal.ListFeatures()` (completion wrapper); `show`/`remove`
`ValidArgsFunction` → registered names, resolved through `ResolveSpacesAnchor()`+`readSpaces` with
**errors discarded** (completion row of §8.3).

`space` subcommands are the **only** callers of `ResolveSpacesAnchor()`.

---

## 5. Logical-name guard call sites (complete, grouped, verified)

Guard = `internal.GuardFeatureName(<root>, <feature>)`, inserted **before** any resolution,
`Stat`, `MkdirAll`, `WriteFile`, `Rename`, or `RemoveAll`.

### 5.1 External-rooted (`root := internal.TwsRoot()`)

| # | Site | Insert at | Class |
| --- | --- | --- | --- |
| E1 | `addExternal` | `cli/add.go:60` (top of body, before `internal.FeaturePath`) | creates |
| E2 | `createWorktree` | `cli/new.go:160` (top of body) | creates |
| E3 | `archiveExternal` | `cli/archive.go:82` (top of body) | destroys worktree |
| E4 | `deleteExternal` | `cli/delete.go:137` — via `BeginSpacesFeatureDelete` (§6.2) | destroys |
| E5 | `renameBranchExternal` | `cli/rename.go:170` (top of body) | mutates |
| E6 | `sync` `RunE`, external branch | `cli/sync.go:38` — after the `ModeCheckout` dispatch (`:34-36`), after `feature := args[0]` (`:37`), **before** `ws.ResolveFeaturePath` at `:39` | mutates |
| E7 | `recreateExternal` | `cli/importcmd.go:251` (after `feature := export.Feature`) | creates |
| E8 | `open --all` external | `cli/open.go` inside `if all {` after the arity check, **before** `openAll(args[0])` at `:77` | reads + execs |
| E9 | `open --feature-dir` external | `cli/open.go` inside `if featureDir {` after `feature := args[0]` (`:86`), **before** `internal.FeaturePath` at `:87` | reads + execs |
| E10 | `open` normal worktree external | `cli/open.go` before `resolveOpenArgs(args)` at `:101`, guarded **only when `len(args) >= 1`** (0-arg picker is covered by `ListFeaturesE`) | writes (`InjectFiles`) + execs |

### 5.2 Checkout-rooted (`root := ws.MetadataRoot`)

| # | Site | Insert at | Class |
| --- | --- | --- | --- |
| C1 | `addCheckout` | `cli/add.go:112` (top of body) | creates |
| C2 | `createCheckoutBranch` | `cli/new.go:60` (before `ws.ResolveFeaturePath`) | creates |
| C3 | `archiveCheckout` | `cli/archive.go:46` (before `ws.ResolveFeaturePath`) | mutates |
| C4 | `deleteCheckout` | `cli/delete.go:53` — via `BeginSpacesFeatureDelete` (§6.2) | destroys |
| C5 | `recreateCheckout` | `cli/importcmd.go:191` (after `feature := export.Feature`) | creates |
| C6 | `renameBranchCheckout` | `cli/rename.go:102` (before `ws.ResolveFeaturePath`) | mutates |
| C7 | `runCheckoutOpen` | `cli/open_checkout.go:11` — **top of the function, before `resolveCheckoutOpenArgs`**, when `len(args) >= 1`. See §11 finding F2: the spec's `:19` anchor is one call too late | reads + execs |
| C8 | `open --feature-dir` checkout | `cli/open.go:56` (before `ws.ResolveFeaturePath`) | reads + execs |
| C9 | `MigrateFeatureLayout` | `internal/migrate.go:27` — via `BeginSpacesLayoutMigration` (§7.4), after `validateFeatureName` at `:24-26` and before the source `Lstat`; the lock is held through `os.Rename` | moves feature dir |
| C10 | `MigrateAllFeatures` batch preflight | `internal/migrate.go:66` — one `BeginSpacesLayoutMigration` for every candidate, after the `ModeCheckout` check and the read-only `os.ReadDir`, before `os.MkdirAll`/`os.Rename`; the lock is held through all moves and rollback | moves feature dirs |

### 5.3 Shared / wrapper-level

| # | Site | Root | Effect |
| --- | --- | --- | --- |
| S1 | `internal.RequireFeaturePath` | `resolve.go:252`, `ws.MetadataRoot` | one insertion covers `checkout_sync.go:11`, `decide.go:32`, `decisions.go:41,:122`, `doctor.go:71`, `hooks.go:98,:153`, `inject.go:43`, `push.go:36`, `stack.go:23`, `template.go:67` (criterion 21). **Not sufficient on its own for `template sync <f>` / `hooks install <f>`** — see S5/S6 |
| S2 | `tws export` | `cli/export.go:44` (before `ws.ResolveFeaturePath` at `:45`), `ws.MetadataRoot` | only remaining named-feature surface routing solely through `ws.ResolveFeaturePath` |
| S3 | `tws rename feature` | `cli/rename.go:28` `RunE`, `ws.MetadataRoot` | via `BeginSpacesFeatureRename` (§6.1) — old **and** new names, both from the one locked read |
| S4 | `tws delete` | `cli/delete.go:53` / `:137` | via `BeginSpacesFeatureDelete` (§6.2) |
| S5 | `templateSyncCmd` `RunE`, single-feature branch | `cli/template.go:55` — after the usage block (`:50-54`), immediately before `syncFeatureTemplate(args[0], templates)` at `:56`, `ws.MetadataRoot` | returns the guard error; see §7.3 |
| S6 | `hooksInstallCmd` `RunE`, single-feature branch | `cli/hooks.go:87` — after the `feature` selection block (`:77-86`), immediately before `installHooksForFeature(feature)` at `:88`, `ws.MetadataRoot` from the `ws` already resolved at `:54` | returns the guard error; see §7.3 |

**Why S5/S6 exist.** `syncFeatureTemplate` (`template.go:66`) and `installHooksForFeature`
(`hooks.go:97`) are `func(...)` with no return value: the S1 guard *does* fire inside them through
`RequireFeaturePath`, but both print `"<feature>: <err>"` / `"  [x] <feature>: <err>"` to stdout
and `return`, so the command exits 0. The `RunE`-level guard is what makes a registered-space
conflict or malformed metadata exit nonzero (spec §8.5.1).

`internal.ListBranches` (`paths.go:163`) calls `RequireFeaturePath` and discards the error — it is a
completion helper reached from `open.go:32`, `sync.go:23` etc. and from `resolveOpenArgs`
(`open.go:186,205`). Its `RunE` reachability is fully covered by guard E10, which fires first.

### 5.4 Deliberately unguarded (record the reason in a comment)

| Site | Reason |
| --- | --- |
| `Workspace.ResolveFeaturePath` (`resolve.go:36`), `ResolveFeaturePathOrLegacy` (`:67`) | generic resolvers; guarding them would add a registry read to every loop iteration and let the `continue`-on-error branches swallow §8.3 failures (criterion 22) |
| `cli/list.go:49`, `checkout_health.go:514`, `:882` | loop input already filtered by `ListFeaturesResolved` |
| `internal.PlanCheckoutSessionLinks` (`session.go:343`) | reached only from the guarded checkout open flow |
| `internal.DetectFeatureFromCwd` (`paths.go:95`) | requires `worktrees/`/`stack.yaml` on disk |
| `Workspace.DetectFeatureFromCwdE` (`resolve.go:206`) | zero non-test callers |
| `inferExternalRepoRoot` (`workspace.go:339`) | promotes only when `LoadStack` succeeds **and** a live worktree path exists (`workspace.go:363-378`) — regression test only |
| `syncFeature` (`sync.go:167`) | returns `syncResult{Complete bool}`, no error channel: a guard here would degrade to `sync incomplete` and lose the §7.3 message. Guard lives at E6 (`sync.go:38`) instead, which also covers `--abort`/`--continue` (finding F3) with **one** read |
| `syncFeatureTemplate` (`template.go:66`), `installHooksForFeature` (`hooks.go:97`) | `func(...)` with no return value; they print to stdout and continue. Guards live at S5/S6 in the `RunE`. Their `--all` loop bodies stay best-effort by design |
| `cli/close.go` — `closeCmd` `RunE` (`:38`) and `runCheckoutClose` | **deliberately guard-free and safe.** External branch only builds a tmux session name (`sanitizeSessionName(feature + "/" + branch)` at `close.go:62`) and kills that session; it joins no root and never touches a path under `TwsRoot()`. Checkout branch delegates to `runCheckoutClose`, which reads the recorded session (`LoadCheckoutAgentSession`) rather than a caller-supplied name. Verified: no `FeaturePath`/`WorktreePath`/`ResolveFeaturePath`/`MkdirAll`/`RemoveAll` in the file. A guard would only add a `spaces.yaml` read |

**Read hygiene — one guard evaluation per invocation.** No guard is placed on both a `RunE` and a
helper it calls, and none is placed inside a loop body:

- `open`: E8/E9/E10 are mutually exclusive `RunE` branches, so at most one fires.
- `sync`: one guard at `sync.go:38` covers plain, `--abort`, and `--continue`.
- `rename feature` / `delete`: the single locked read inside the transaction feeds
  `guardFeatureNameIn` for both names plus the containment scan — `GuardFeatureName` (which reads)
  is never called there.
- `template sync --all` / `hooks install --all`: no per-feature guard; `ListFeaturesE` already
  applied the §7.5 exclusion from its own single read.
- `MigrateAllFeatures`: one `BeginSpacesLayoutMigration` transaction, hence one locked read, for the batch (`migrate.go:66`).
- `space list`/`show`/`remove`: one `readSpaces`; owners come from the pure `ownersFrom(root, f)`
  over the already-decoded file, never a second `SpaceDirOwners`. Per-entry `status` /
  `scope_status` are `os.Stat`/existence probes and read no metadata.

---

## 6. Lifecycle transactions

### 6.1 Rename — `internal/cli/rename.go:28-57`

`BeginSpacesFeatureRename` opens with `os.Lstat(spacesPath(root))`; on `fs.ErrNotExist` it returns
the no-op transaction **without calling `acquireSpacesLock`**, which is what keeps `<root>`,
`.spaces.lock`, and `spaces.yaml` from being created (spec §8.5). Current body is 6 statements;
the transaction wraps them:

```
28  RunE:
31    ws, err := internal.RequireWorkspace()             (unchanged)
      root := ws.MetadataRoot                            NEW
38    oldPath, err = ws.ResolveFeaturePath(oldName)      (unchanged, guard-free)
41    newPath = ws.FeaturePath(newName)                  (unchanged)
43-49 os.Stat(oldPath)/os.Stat(newPath) checks           (unchanged)
      tx, err := internal.BeginSpacesFeatureRename(       NEW  ← insertion point A
          root, oldName, newName, oldPath, newPath)
      defer func() { retErr = errors.Join(retErr, tx.Release()) }()   NEW
51    if err := os.Rename(oldPath, newPath); …           (unchanged, now under the lock)
      if cerr := tx.Commit(); cerr != nil {              NEW  ← rollback point B
          if rb := os.Rename(newPath, oldPath); rb != nil { return errors.Join(cerr, rb-message) }
          return cerr
      }
55    fmt.Printf("Renamed feature: …")                   (unchanged)
```

Rollback insertion point **B** is the only place `os.Rename(newPath, oldPath)` appears. The
named-return form (`RunE: func(...) (retErr error)`) is required for the deferred `Release` join.

### 6.2 Delete — `internal/cli/delete.go`

Two insertion points, both at the **top of the function body, before the first `os.Stat`**:

- `deleteCheckout` — `:53`, `root = ws.MetadataRoot`, `featurePath` must be resolved *first* via the
  existing `ws.ResolveFeaturePath` (`:53`) so the exact path is passed in; guard order inside the
  tx is top-level-name-conflict → nested containment (criterion 33).
- `deleteExternal` — `:137`, `root = internal.TwsRoot()`, `featurePath := internal.FeaturePath(feature)`
  (already the first statement at `:137`).

Both need `defer` + `errors.Join` on `Release()`; both functions already return `error`, so a named
return is needed. `Release()` **never writes**. `BeginSpacesFeatureDelete` takes the same
`os.Lstat`-before-`acquireSpacesLock` probe as the rename transaction, so a workspace with no
`spaces.yaml` gains no lock file, no data file, and no root directory (spec §8.5; criterion 35).

### 6.3 `space remove` — the third `Lstat`-before-lock site

`SpaceRemove` is a mutator, so it follows the same rule as the two transactions:
`os.Lstat(spacesPath(anchor.Root))` first; on `fs.ErrNotExist` return `no space named <name>`
(exit 1) immediately, **before** any `acquireSpacesLock`, `MkdirAll`,
`EnsureExternalWorkspaceMarker`, `CreateTemp`, or `WriteFile`. Only `SpaceAdd` may create the
root, the marker, the lock, and the file (spec §10.1). A file that exists at probe time but
vanishes before the locked read yields the same `no space named <name>` error rather than an
empty write. `space list` and `space show` never lock and therefore need no probe beyond
`readSpaces`'s own `(nil, nil)` absent-file contract.

---

## 7. Listing / exclusion / strict propagation

### 7.1 `internal/resolve.go`

| Change | Location |
| --- | --- |
| `reservedDirs["spaces.yaml"] = true` | `resolve.go:157-164` (add one map entry) |
| `ListFeaturesResolved`: one `SpaceDirOwners(w.MetadataRoot)` call at the **top**, `return nil, err` on failure; `Features` map filters the `features/` loop (`:123-129`), `TopLevel` filters both `MetadataRoot` loops (`:131-137` checkout-legacy, `:139-145` external) | `resolve.go:117-155` |
| `LegacyFeatureNames`: best-effort — `owners, err := SpaceDirOwners(w.MetadataRoot); if err != nil { return nil }`, else filter with `TopLevel` | `resolve.go:99-113` |
| `RequireFeaturePath`: insert `GuardFeatureName(ws.MetadataRoot, feature)` between `RequireWorkspace()` and `ws.ResolveFeaturePath` | `resolve.go:248-252` |
| Comment on `ResolveFeaturePath`/`ResolveFeaturePathOrLegacy`/`DetectFeatureFromCwdE` recording the deliberate guard-free decision | `resolve.go:29-35`, `:65-66`, `:203-205` |

**One read per listing call** is structural: `SpaceDirOwners` is called once at the top of
`ListFeaturesResolved`, never inside a loop. Same for `LegacyFeatureNames` and the migrate preflight.

**Recursion warning for this edit.** `ListFeaturesResolved` now calls `SpaceDirOwners`, so the
spaces layer must never call back into feature listing or resolution (§2.2 recursion fence). In
particular `isFeatureDir` stays a pure `os.Stat` probe for `stack.yaml` / `worktrees/` /
`FEATURE.md`; reaching for `ListFeaturesResolved` or `ResolveFeaturePath` there would recurse
forever on the very first `tws list`.

### 7.2 `internal/paths.go`

```go
func ListFeaturesE() ([]string, error)   // NEW: real logic, error-propagating
func ListFeatures() []string             // becomes: names, _ := ListFeaturesE(); return names
```

`ListFeaturesE` mirrors current `ListFeatures` (`paths.go:139-158`) but:
- workspace branch: `return ws.ListFeaturesResolved()` (exclusion already applied there);
- fallback branch (`paths.go:147-157`): `owners, err := SpaceDirOwners(root)` with
  `root := TwsRoot()` — the root it actually `ReadDir`s — then skip `owners.TopLevel[name]`; keep
  the existing `entry.Name() != ".tws-workspace"` filter byte-for-byte.

Runtime callers migrating to `ListFeaturesE`: `cli/doctor.go:45`, `cli/open.go:197`
(`resolveOpenArgs`, already returns `error`), `cli/template.go:39`, `cli/hooks.go:65`.
All 16 `ValidArgsFunction` references stay on `ListFeatures()`:
`archive.go:19`, `close.go:32`, `decide.go:24`, `decisions.go:33,:115`, `delete.go:24`,
`doctor.go:24`, `export.go:32`, `inject.go:34`, `new.go:26`, `open.go:32`, `push.go:20`,
`rename.go:70`, `stack.go:17`, `sync.go:23`, `template.go:33`.

### 7.3 `Run:` → `RunE:` migration (§8.5.1, exactly two commands)

| Command | Current | Change |
| --- | --- | --- |
| `templateSyncCmd` | `Run:` at `template.go:37`; usage `fmt.Println` + `os.Exit(1)` at `:50-53` | `RunE:`; `--all` → `ListFeaturesE()`; usage block → `return fmt.Errorf("usage: tws template sync <feature> [--template <dir>]\n       tws template sync --all")`; **single-feature branch** gains the S5 guard at `:55`; drop the now-unused `os` import if nothing else uses it (**it does** — `os.MkdirAll` at `:73`; keep) |
| `hooksInstallCmd` | `Run:` at `hooks.go:53`; `fmt.Printf("Error: %v\n", wsErr)`+`return` at `:55-58`; checkout refusal at `:59-62`; `os.Exit(1)` at `:82-85` | `RunE:`; those three become returned errors; `--all` → `ListFeaturesE()` at `:65`; **single-feature branch** gains the S6 guard at `:87` |

Single-feature guard shape (both commands, spec §8.5.1):

```go
// template.go — ws is NOT already resolved here
ws, wsErr := internal.RequireWorkspace()
if wsErr == nil {                                   // failure => fall through, legacy shape
    if err := internal.GuardFeatureName(ws.MetadataRoot, args[0]); err != nil {
        return err                                  // §7.3 message or §8.3 canonical message
    }
}
syncFeatureTemplate(args[0], templates)             // unchanged void helper
return nil
```

```go
// hooks.go — ws already resolved at :54 and checkout already refused at :59-62
if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
    return err
}
installHooksForFeature(feature)                     // unchanged void helper
return nil
```

`ws.MetadataRoot` is deliberately the same root `RequireFeaturePath` (S1) resolves under, so the
guard and the helper agree about which `spaces.yaml` is authoritative. The `templateSyncCmd`
skip-on-`RequireWorkspace`-failure is what keeps the legacy shape exact: today that command never
resolves a workspace in `RunE`, and a broken workspace still reaches `syncFeatureTemplate`, which
prints `"<feature>: <err>"` on stdout and exits 0. Promoting it would be a seventh §8.5.1 row that
nobody asked for.

Unchanged and asserted so: `No features found.` (stdout, exit 0) in both; per-feature progress
lines; `syncFeatureTemplate` (`template.go:66`) and `installHooksForFeature` (`hooks.go:97`) keep
printing best-effort errors to stdout and keep not aborting — including for an unrelated
`RequireFeaturePath` failure (feature absent, ambiguous, invalid `workspace_mode`) in the
single-feature path, which still exits 0. **Only the `--all` loop bodies retain best-effort
per-feature output**; they carry no guard of their own, because `ListFeaturesE` already excluded
space-owned names from its single read, so a registered space can never enter either loop.

### 7.4 `internal/migrate.go`

Both entry points go through one migration-specific lifecycle transaction,
`BeginSpacesLayoutMigration(root, []LayoutMigrationTarget)`, built on the shared
`beginSpacesTx` (`Lstat`-before-lock probe, lock, authoritative re-read). It is its own type
with its own message: the delete-specific text and `SpacesDeleteTx` are deliberately not reused.

- `MigrateFeatureLayout` (`:23`): open the transaction for the single target at `:25`, before the
  source `Lstat`, and hold its lock through `os.Rename`; release with `errors.Join` on a named
  return.
- `MigrateAllFeatures` (`:59`): discovery (`os.ReadDir`, read-only) first, then **one**
  transaction for every candidate before `os.MkdirAll(featuresDir)` and the first `os.Rename`; on
  error append the message to `result.Errors` and `return result` immediately. A blocked batch is
  an error (**never** `Skipped`, never `migrations`), so one blocked feature aborts every
  candidate. The lock is held across all moves and the existing all-or-nothing rollback, and a
  release failure is appended to `result.Errors`.
- Inside the transaction, top-level name ownership is rejected **first** (verbatim §7.3 message),
  then identity-aware inclusive containment via `pathContains` catches targets nested inside a
  legacy feature directory — the case §7.1's feature-hub exception deliberately leaves unclaimed
  (`<root>/acme/patching` under legacy feature `acme`). This version **refuses**; it never
  rewrites a registered path during migration. The refusal names every blocker sorted, with its
  scope and its exact `tws space remove <name> --workspace` / `--feature <f>` command, and says
  the link can be re-added afterwards. Verified: no `features/` directory is created on refusal —
  criteria 43/44/45a/45b hold.
- `internal/cli/migrate.go` needs **no** source change: `:49-53` already prints `error:` lines and
  returns `migration failed with %d error(s)`.

---

## 8. Tests

### 8.1 Existing tests to extend

| File:line | Test | Action |
| --- | --- | --- |
| `internal/resolve_test.go:133` | `TestListFeaturesResolved_Sorted` | add a space-owned name case |
| `internal/resolve_test.go:159` | `TestListFeaturesResolved_ExcludesReserved` | add `spaces.yaml` + malformed-file error case |
| `internal/resolve_test.go:208,:223` | `TestDetectFeatureFromCwdE_*` | add an assertion pinning "still exclusion-free" |
| `internal/migrate_test.go:73` | `TestMigrateAllFeatures_PreflightCollision` | must keep passing (no `spaces.yaml`); add sibling `_SpaceConflict`, `_SpaceFeaturesOwner`, `_MalformedSpaces`, `_NestedTargetBlocksBatch` |
| `internal/migrate_test.go:117` | `TestMigrateAllFeatures_ReservedDirsUntouched` | keep passing unchanged |
| `internal/cli/external_feature_dir_test.go:11` | `TestExternalFeatureDirectoryCommandMatrix` | must keep passing (`doctor` row at `:69`, `list`, `sync`) |
| `internal/cli/checkout_doctor_test.go:145` | uses `ws.ListFeaturesResolved()` directly | add a malformed-`spaces.yaml` assertion |
| `internal/checkout_health_test.go:1079` | `TestCheckoutHealth_Ambiguity` | add sibling asserting `BuildCheckoutList`/`buildFeatureEntries` return the error, not an empty slice |

No existing test constructs `templateCmd()` or `hooksCmd()` (verified by grep) — the `Run:`→`RunE:`
migration breaks nothing today, but new tests must exercise it, including the single-feature guard
(S5/S6) and the four carve-out cases that must **not** change: `--all` still exits 0 with
best-effort per-feature stdout lines, an unrelated `RequireFeaturePath` failure still exits 0 on
stdout, a failing `RequireWorkspace()` in `template sync` still exits 0 on stdout, and malformed
metadata now exits 1 on stderr.

### 8.2 Harnesses to reuse (do not reinvent)

| Helper | Location | Use |
| --- | --- | --- |
| `setupGitRepo(t, branch)` | `cli/new_integration_test.go:135` | real repo + **local bare remote** + `push -u` + `symbolic-ref HEAD` + `remote set-head` |
| `withWorkspaceEnv(t, repo)` | `cli/new_integration_test.go:152` | `HOME`, `TWS_ROOT`, `chdir` + restore |
| `createWorktree(...)` | `cli/new.go:159` | real linked worktrees |
| `setupGitRepoCheckout(t)` | `cli/checkout_lifecycle_test.go:19` | checkout repo + `.tws/config.yaml` |
| `requireWorkspaceForTest(t, dir)` | `cli/checkout_lifecycle_test.go:53` | |
| `setupRegistryTestEnv(t)` | `cli/registry_test.go:15` | `HOME` + `XDG_DATA_HOME` isolation |
| `cmd.SetOut(&bytes.Buffer{})` | `cli/registry_test.go:74` | capture `cmd.OutOrStdout()` |
| `gitRun/gitOutput/gitInDir/writeAndCommit` | `new_integration_test.go:180,191`, `checkout_lifecycle_test.go:63` | |

**Isolation is mandatory in every new test**: `t.Setenv("HOME", …)`, `t.Setenv("XDG_DATA_HOME", …)`,
`t.Setenv("TWS_ROOT", …)`, `t.TempDir()` — the developer's real `~/tws` and XDG registry must never
be touched.

**Output-capture caveat.** Most legacy commands print with bare `fmt.Println`/`fmt.Printf` to
`os.Stdout`, *not* `cmd.OutOrStdout()` (`list.go`, `doctor.go`, `template.go`, `hooks.go`,
`migrate.go`). `cmd.SetOut` will **not** capture them. New `space` commands must use
`cmd.OutOrStdout()` (buffer-capturable). For the criterion-31 byte-identical assertions, add one
`os.Pipe`-based `captureStdout(t, func())` helper in `internal/cli/space_guard_test.go` — no such
helper exists today.

### 8.3 Deterministic failure injection (required; do **not** use file permissions)

The spec itself bans permission fixtures in criterion 27 (root no-ops, platform variance) — and
criterion 42 (now corrected in the spec) **cannot work with a permission fixture**: `saveSpaces`
writes a `CreateTemp` file in `root` and `Rename`s over the target, so a read-only `spaces.yaml`
is irrelevant. Use hooks instead:

| Hook (`internal/spaces.go`, nil in production) | Visibility | Purpose |
| --- | --- | --- |
| `var SpacesSaveHook func(root string) error` — consulted at the top of `saveSpaces` | **exported** | criterion 42 rename-rollback, criterion 36 tail |
| `var spacesReadHook func(path string)` — called at the top of `readSpaces` | unexported | criterion 26 "exactly one read" and the criterion-21 read-count assertion |

**Visibility rule: export only what a test outside `internal` must set — nothing more.**

- `SpacesSaveHook` is exported because the rollback it triggers lives in `internal/cli/rename.go`
  (`os.Rename(newPath, oldPath)`, §6.1 point B), so criterion 42 has to be asserted by running
  `tws rename feature` from package `internal/cli`. This is exactly the `internal.StepHook`
  precedent (`checkout_sync.go:299-303`, set from `internal/cli/checkout_sync_test.go:373` and
  cleared by `clearStepHook` at `:83-86`) — same doc-comment shape, same `t.Cleanup` discipline.
  Keeping it unexported and testing only `BeginSpacesFeatureRename`+`Commit` inside `internal`
  would leave the rollback branch — the actual subject of criterion 42 — untested; adding a second
  exported seam just for the test would be larger, not smaller.
- `spacesReadHook` stays unexported because every assertion that uses it lives in
  `internal/spaces_test.go`: criterion 26 against
  `Workspace{MetadataRoot: root, Mode: ModeExternal}.ListFeaturesResolved()` over five features
  (behaviourally identical to `tws list`), and the criterion-21 read counts against
  `GuardFeatureName`. Nothing in `internal/cli` needs to observe read counts.

Both hooks are cleared with `t.Cleanup` in every test that sets them, and neither is referenced
from production code paths other than the single `if hook != nil` check.

Malformed fixture set (criteria 27–30), all root-independent: bad YAML; `version: 0`; `version: 99`;
unknown field; symlinked `spaces.yaml`; **directory** named `spaces.yaml`. Verified the directory
case works: `os.Lstat` passes (not a symlink), `os.ReadFile` returns `EISDIR`.

### 8.4 New test files

| File | Package | Covers |
| --- | --- | --- |
| `internal/spaces_test.go` | `internal` | decode/validate/save/normalize units; traversal escape; non-canonical absolute; symlinked-root relativization (`/var`→`/private/var`); unknown field; future/malformed version; symlinked file; duplicate `(feature,name)`; duplicate path in scope; `0600` mode; `SpaceDirOwners` feature-dir exception; `features/<x>` mapping; `containsPath` inclusive equality; `updated_at` omission; absent-file no-op; recursion-fence termination test (§2.2); `spacesReadHook` read counts; criteria 15/21-read-count/22/26/31/35/41/42-transaction-half |
| `internal/cli/space_test.go` | `cli` | both modes; `TWS_ROOT` divergence (criteria 2–6); human + JSON output; exit codes; empty state incl. the criterion-15 `show`/`remove` no-create walk and the criterion-18a header/filter-empty distinction; the criterion-18b `--workspace` / `--feature` scope selectors and their mutual exclusion; criterion-18c `Long` help; missing target; non-Git target; idempotent add; concurrency (criterion 47) |
| `internal/cli/space_identity_test.go` | `cli` | criterion 32a — absolute-in-root entries excluded from listings and refused by `tws add`/`tws delete`/`tws migrate-layout`/`space list --feature`; nested absolute in-root targets block `tws delete` with scope-qualified guidance; probe-and-skip case-insensitivity pair proving `tws add`/`tws delete` refuse a case-variant feature name while the target bytes and `spaces.yaml` survive |
| `internal/cli/space_guard_test.go` | `cli` | criteria 19–31: guard matrix in both modes, guard-free resolver assertion, malformed-fixture strict-failure table, completion best-effort rows, no-file baseline, §8.5.1 normalized-error table (eight rows), the criterion-21 `template sync`/`hooks install` single-feature guard table with its four carve-out cases, `captureStdout` helper |
| `internal/cli/space_lifecycle_test.go` | `cli` | criteria 32–42, including the criterion-42 rollback driven by `internal.SpacesSaveHook` with `t.Cleanup` |
| `internal/cli/space_migrate_test.go` | `cli` | criteria 43–45b, including the §7.4 nested-target refusal in both stored forms |
| `internal/cli/space_matrix_test.go` | `cli` | §11 nine-row matrix, template `external_feature_dir_test.go` |

### 8.5 Real-Git invocation matrix (build once per test, reuse)

| Row | Fixture construction |
| --- | --- |
| 1 external repo root | `setupGitRepo` + `withWorkspaceEnv` |
| 2 worktree root | `createWorktree("f","root","master","",false)` then `chdir(internal.WorktreePath("f","root"))` |
| 3 nested in worktree | `MkdirAll(<wt>/a/b)` + `chdir` |
| 4 external workspace root | `MkdirAll($TWS_ROOT/.tws-workspace)` + `chdir($TWS_ROOT)` |
| 5 external feature dir | `chdir(internal.FeaturePath("f"))` |
| 6 nested feature dir | `chdir(<fp>/docs/nested)` |
| 7 checkout repo root | `setupGitRepoCheckout` + `chdir` |
| 8 checkout feature dir | `chdir(<repo>/.tws/features/f)` |
| 9a space = plain dir | `MkdirAll($TWS_ROOT/learning/notes)`, register, `chdir` |
| 9b space = own Git repo under the marker | `git init` + one commit in `$TWS_ROOT/tickets`, register, `chdir` there **and** into a nested dir; assert `space list --json` is byte-identical to the same command from `$TWS_ROOT`, and that no `spaces.yaml`, `.tws`, or `tickets.tws` is created for the sibling repo |
| 9c boundary | sibling repo **outside** any marker/config/`~/tws` → anchors on its own external root |

Rows 2–6 must use the real `createWorktree` helper, never a mocked directory tree.

---

## 9. Skills / docs / roadmap edits

| File | Edit |
| --- | --- |
| `assets/skills/claude/tesseraworkspaces/SKILL.md` | command-table row after `tws registry …` at `:41`; new "Sibling Spaces" section after the "Global Workspace Registry" section (`:176-200`) |
| `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` | one bullet in "View state" (`:19-25`): `tws space list --json --feature <f>` before delegating |
| `assets/skills/copilot/tws.prompt.md` | bullet after `:31`; code block after the registry block (`:86-91`) |
| `README.md` | 4 rows in the command table (after `:150`); new "Workspace sibling links" section after "Global Workspace Registry" (`:153`) — must state the row-9 marker-wins rule verbatim |
| `docs/roadmap.md` | `:5-22` move sibling links from "Now / Current target" to shipped foundations; update the `:53` bullet; name the next target |
| `docs/engineering-workflow.md` | `:9-24` add slice 7 to the shipped list and name the next target |
| `CHANGELOG.md` | new entry above `## v1.2.0-rc.1` (`:3`) for the next patch after `v1.2.10`; must call out the `tws template sync` / `tws hooks install` stderr+nonzero change and the `tws migrate-layout` refusal |

Skills are compiled via `assets/skills/embed.go` (3 `//go:embed` directives, all three files are
edited); they ship on rebuild and re-install with `tws init --force`.

---

## 10. Minimal ordered implementation sequence

| Step | Work | Verify |
| --- | --- | --- |
| 0 | **Capture goldens first**: run `tws list`, `tws doctor`, `tws template sync --all`, `tws hooks install --all` on a fixture workspace with and without features; record stdout/stderr/exit into the criterion-31 test table. Impossible to do after step 4. | manual |
| 1 | `internal/spaces.go`: constants, types, `readSpaces`/`decodeSpaces`/`validateSpaces`/`saveSpaces`/`sortSpaces`, `containsPath`, `normalizeSpacePath`, `SpaceStatusOf`, `SpacesSaveHook` (exported) + `spacesReadHook` (unexported) | `go test ./internal -run TestSpaces` |
| 2 | `SpaceDirOwners`/`ownersFrom`/`isFeatureDir`/`GuardFeatureName`/`guardFeatureNameIn`/`ErrSpaceNameConflict`, `ResolveSpacesAnchor`, `AnchorFeaturePath`, `acquireSpacesLock` (+ the `Lstat`-before-lock probe helper) | `go test ./internal -run 'TestSpaces\|TestSpaceDirOwners'` |
| 3 | `internal/resolve.go` + `internal/paths.go`: `reservedDirs`, `ListFeaturesResolved` exclusion, `LegacyFeatureNames`, `RequireFeaturePath` guard, `ListFeaturesE`/`ListFeatures` split | `go test ./internal -run 'TestListFeatures\|TestResolveFeaturePath' -count=1` |
| 4 | `internal/migrate.go` guards (C9/C10) | `go test ./internal -run TestMigrate -count=1` |
| 5 | `internal/cli/space.go` + `root.go` registration (`SpaceRemove` `Lstat`-before-lock, §6.3) | `go test ./internal/cli -run TestSpace -count=1` |
| 6 | Guards E1–E10 (E6 at `sync.go:38`), C1–C8, S2 | `go test ./internal/cli -run 'TestSpace\|TestExternalFeatureDirectoryCommandMatrix' -count=1` |
| 7 | `BeginSpacesFeatureDelete` + `BeginSpacesFeatureRename` and their two CLI integrations | `go test ./internal/cli -run 'TestSpaceLifecycle\|TestCheckoutDelete' -count=1` |
| 8 | `template.go` / `hooks.go` `Run:`→`RunE:` + `ListFeaturesE` migration (`doctor.go:45`, `open.go:197` too) **and** the S5/S6 single-feature guards | `go test ./internal/cli -count=1` |
| 9 | All new/extended test files | focused suites below |
| 10 | Skills, README, roadmap, engineering-workflow, CHANGELOG | `make build` |

Focused commands (from §14 criterion 49, all valid against the real tree):

```bash
go test ./internal -run 'TestSpaces|TestListFeatures|TestResolveFeaturePath|TestMigrate' -count=1 -v
go test ./internal/cli -run 'TestSpace' -count=1 -v
go test ./internal/cli -run 'TestExternalFeatureDirectoryCommandMatrix' -count=1
go test ./internal/cli -run 'TestCheckout|TestMigrateLayout' -count=1
```

Full gates (all four are green on the pre-change tree, so any failure is caused by this change):

```bash
gofmt -l internal assets cmd
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
```

---

## 11. Findings — spec details that need a decision at implementation time

Nothing in the spec is **impossible** or **unsafe**. F1–F3 have been folded back into the spec by
the exploration review (criterion 42, §7.6 sync row, §8.5.1); F4–F5 remain implementer-level
choices. Each fix preserves the spec's stated intent and does **not** redesign it.

| # | Finding | Evidence | Minimal fix |
| --- | --- | --- | --- |
| F1 | **Criterion 42 could not pass as originally written.** "`spaces.yaml` made unwritable" does not fail `saveSpaces`, which writes a temp file in `root` and `Rename`s. And criterion 27 already forbids permission fixtures. | `registry.go:264-308` pattern mandated by §8.2 | **Resolved in spec**: criterion 42 now names the exported test-only `internal.SpacesSaveHook` (§8.3, spec §15.1) and explicitly rejects the permission fixture. Export is required because the rollback lives in `internal/cli/rename.go`; `StepHook` is the precedent |
| F2 | **C7 guard is one call too late.** §7.6 anchors the checkout `open` guard at `open_checkout.go:19`, but `resolveCheckoutOpenArgs` runs first at `:11` and, for `tws open <feature>`, reaches `checkoutActiveNames` → `ws.ResolveFeaturePath` → `LoadStack` → `pick()` before the guard. Criterion 28's `tws open f` would produce a "no active branches"/picker error instead of the canonical §8.3 message. | `open_checkout.go:10-19,46-53,88-95` | move the guard to the **top of `runCheckoutOpen`** (`:11`), gated on `len(args) >= 1`. Same function, same root, same message |
| F3 | **`tws sync --continue` / `--abort` bypassed a guard placed in `syncFeature`, and `syncFeature` cannot report one anyway.** `syncCmd` `RunE` resolves `featurePath` at `sync.go:39` and returns via `handleSyncAbort`/`handleSyncContinue` (`:79`, `:96`) without reaching `syncFeature` (`:167`); and `syncFeature` returns `syncResult{Complete bool}`, so a guard error there would surface as `sync incomplete`, not the §7.3 message. `external_feature_dir_test.go:101` exercises `sync --abort`. | `sync.go:27-66,167` | **Resolved in spec**: the single guard moves to the `sync` `RunE` at `sync.go:38` (external branch, after the `ModeCheckout` dispatch, before `ws.ResolveFeaturePath`), rooted at `internal.TwsRoot()`. `syncFeature` keeps no guard — one read, all three paths covered, message preserved |
| F4 | **`ValidateAlias` accepts `"."` and `".."`.** §6.1 says they must be rejected explicitly; the reused validator does not do it (`^[a-zA-Z0-9._-]+$`). | `registry.go:911-926` | add the two explicit rejections in `validateSpaceName` on top of `ValidateAlias`; do not modify `ValidateAlias` (shared with the registry) |
| F5 | **`detectAnchorFeature` is unnamed in the spec.** §10.2 describes an "anchor-rooted cwd walk" but §15.1 lists no symbol for it. | §10.2 vs §15.1 | add `detectAnchorFeature(anchor, owners, cwd) string` to `internal/spaces.go`; it must not call `DetectFeatureFromCwdE` (§7.5) or any other resolver (§2.2 recursion fence) |

Two informational notes (no action):

- The `internal` package **already** does not compile on Windows (`registry_lock_unix.go` is
  `//go:build !windows` with no counterpart). N9 is a statement of the status quo, not a new limit.
- `RequireWorkspace()` canonicalizes `MetadataRoot` in the fallback tier (`workspace.go:461`) but
  `ResolveCurrentWorkspaceE` returns `resolveExternalRoot(original, canon, cfg)` (`workspace.go:424`)
  which may be non-canonical. `SpacesAnchor.Canon` handles this; do not "fix" it here (N11).

---

## 12. Dependency assessment (recommendation only — nothing mutated)

`status.json` already records the four links the analysis recommended:
`workspace-mode-foundation` (**hard**), `fix-external-feature-dir-resolution` (**hard**),
`workspace-registry` (soft), `workspace-hub-research` (soft). No change is needed.

- **No new Go module dependency.** `gopkg.in/yaml.v3` and `github.com/spf13/cobra` are already
  direct requirements; everything else is stdlib (`encoding/json`, `time`, `syscall` via the
  existing flock file). **`go.mod` and `go.sum` must not change** — treat any diff there as a bug.
- No new tpatch feature dependency surfaced during exploration.
- Run `tpatch feature deps --validate-all` before implementation per `.tpatch/steering/local.md`.
