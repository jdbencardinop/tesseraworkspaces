# Specification — workspace-sibling-links

## 1. Problem statement

A tws workspace is surrounded by tool-owned sibling spaces: learning notes, ticket stores,
patch metadata, research, and authored documentation. Today `tws` has no way to record where
those spaces live. The knowledge is lost between sessions or hard-coded into agent skills —
and skills are compiled into the binary through `assets/skills/embed.go`, so any path written
there is frozen at build time and wrong for every other machine.

This feature adds a workspace-scoped discovery registry, `<spaces-root>/spaces.yaml`, plus
`tws space add/list/show/remove`. `tws` becomes authoritative for **location metadata only**.
It never reads, writes, validates, or deletes the content of a linked space, and it never
learns the linked tool's schema or lifecycle.

## 2. Goals

- G1. Persist a versioned, workspace-scoped mapping from a stable name to a sibling directory.
- G2. Work in both workspace modes from **one** explicitly resolved root value that is passed
  into every read, guard, and mutation helper. No spaces helper ever re-derives a root.
- G3. Support absolute paths and workspace-relative paths, with one unambiguous resolution rule.
- G4. Validate that a target exists and is a directory, without requiring it to be a Git repository.
- G5. Support workspace-wide entries and feature-scoped entries, including the optional
  feature-hub layout described in `docs/roadmap.md` (`<feature>/learning`, `<feature>/tickets`,
  `<feature>/patching`).
- G6. Provide no-argument, mode-aware discovery from every supported invocation location.
- G7. Emit agent-grade JSON with the same encoder settings and empty-array convention as
  `tws registry list --json`.
- G8. Teach skills the *command* (`tws space list`), never a path.
- G9. When `spaces.yaml` is absent — the normal state — every pre-existing command keeps its
  **successful** behaviour and its filesystem effects byte-for-byte, and no guard, transaction, or
  listing path creates any file (§8.5). The one intentional exception is the error-path
  normalization of `tws template sync` and `tws hooks install`, which migrate `Run:` → `RunE:` so
  they can propagate a spaces failure at all; their pre-existing failure paths stop exiting 0 and
  stop printing to stdout (§8.5.1).
- G10. Never let a registered space masquerade as a feature, and never let a feature operation
  create into, or destroy, a registered space directory.
- G11. When `spaces.yaml` exists but cannot be trusted, fail loudly rather than degrade silently
  (§8.3).

## 3. Non-goals (out of scope)

- N1. A multi-project "super tws" hub spanning several workspace roots (research-only; `workspace-hub-research`).
- N2. `tws space edit`, `check`, `prune`, `move`, or `open`. Correcting an entry is `remove` + `add`.
  No such command is added now or planned by this spec.
- N3. A `spaces` section in `tws doctor`. Health is reported by `space list` / `space show` only.
  `doctor` gains no spaces output whatsoever; it only inherits the strict failure of §8.3.
- N4. Any knowledge of a linked tool's schema, file format, lifecycle, or health.
- N5. Creating, populating, moving, or deleting a linked target directory. `tws` only ever creates the
  spaces root itself, its external marker, `spaces.yaml`, and `.spaces.lock` (§10.1).
- N6. Including `spaces.yaml` in `tws export` / `tws import` (`internal/export.go` stays feature-scoped).
- N7. Registering a space in the *global* registry (`internal/registry.go`) or vice versa.
- N8. Sharing/committing `spaces.yaml`. It is local, uncommitted state in both modes.
- N9. Windows support for the spaces lock, which inherits the POSIX-only boundary of
  `internal/registry_lock_unix.go`.
- N10. Forbidding spaces inside a feature directory. The roadmap hub layout is explicitly supported;
  it is protected by refusal-on-delete (§12.2) and refusal-on-rename (§12.1), never by
  refusal-on-add.
- N11. Reconciling the pre-existing divergence between `internal.TwsRoot()` and
  `Workspace.MetadataRoot`. This feature never bridges the two roots; every helper is told which
  root it is operating on (§4.1, §7.6).

## 4. Spaces root

### 4.1 One explicit root per operation (settles B1)

`internal.TwsRoot()` and `internal.RequireWorkspace().MetadataRoot` can disagree (verified in the
analysis with `TWS_ROOT` set). This feature does not fix that divergence and does not paper over
it. Instead there is exactly one rule:

> **Every spaces helper receives the concrete root or anchor that the calling operation actually
> reads from, writes to, or destroys under. A helper never calls `TwsRoot()`,
> `RequireWorkspace()`, or any other resolver of its own, and never consults a second root.**

For the `space` subcommands, the root is resolved **once per command** by a single resolver:

```go
type SpacesAnchor struct {
    Root  string        // absolute path that holds spaces.yaml; used verbatim for I/O
    Canon string        // canonicalize(Root); used only for containment comparisons
    Mode  WorkspaceMode // external | checkout
}

func ResolveSpacesAnchor() (SpacesAnchor, error)
```

Resolution:

1. `ws, err := internal.RequireWorkspace()`.
2. `err == nil && ws.Mode == ModeCheckout` → `Root = ws.MetadataRoot` (`<repo>/.tws`).
   **`TWS_ROOT` is ignored in checkout mode**, exactly as every checkout command already ignores
   it (they all route through `ws.FeaturePath` / `ws.ResolveFeaturePath`). `Mode = checkout`.
3. `err == nil && ws.Mode == ModeExternal` → `Root = internal.TwsRoot()`.
   **`TWS_ROOT` wins in external mode**, because external feature *mutations* use it:
   `addExternal` (`internal/cli/add.go:60`), `createWorktree` (`internal/cli/new.go:159`),
   `deleteExternal` (`internal/cli/delete.go:137`), `archiveExternal` (`internal/cli/archive.go:82`),
   `syncFeature` (`internal/cli/sync.go:168`), `renameBranchExternal` (`internal/cli/rename.go:170`),
   `recreateExternal` (`internal/cli/importcmd.go:252`), and the external `open` paths all build
   from `internal.FeaturePath` / `internal.WorktreePath`, i.e. from `TwsRoot()`. `Mode = external`.
4. `err != nil` → if `internal.TwsRoot()` names an existing directory, `Root` is that directory
   and `Mode = external` (the same documented fallback `internal/cli/list.go:22` performs).
   Otherwise return the `RequireWorkspace()` error unchanged.

There is no `Legacy` field. Nothing consumed it; the fallback tier is behaviourally
indistinguishable from tier 3 once resolved, so recording it would be dead state.

Rationale for the external choice: the root must be the directory where this workspace's features
are actually created and destroyed. Anchoring external mode on `MetadataRoot` would write
`spaces.yaml` to `<repo>.tws` while features were created under `$TWS_ROOT` — the split-brain the
analysis flagged.

`Root` is used verbatim (`RequireWorkspace` already canonicalizes `MetadataRoot`; `TwsRoot()`
returns an absolute path). `Canon` is used only for the containment tests in §6.6 and §12.

Non-`space` commands never call `ResolveSpacesAnchor()`. They pass the root literal they already
hold — `internal.TwsRoot()` or `ws.MetadataRoot` — directly into `GuardFeatureName`,
`SpaceDirOwners`, or a transaction constructor (§7.3, §7.6).

### 4.2 Resulting locations and the exact external tier behaviour

| Mode | `TWS_ROOT` | `spaces.yaml` |
| --- | --- | --- |
| external | set | `$TWS_ROOT/spaces.yaml` |
| external | unset | first hit of `TwsRoot()`'s tiers (below) |
| checkout | unset or set | `<repo>/.tws/spaces.yaml` (env ignored, by design) |

With `TWS_ROOT` unset, `TwsRoot()` resolves in this order (`internal/paths.go:56`):

1. `DetectWorkspaceRoot(cwd)`: `.tws-workspace` marker walk-up, then a configured
   `workspaces[...]` root that is a prefix of cwd, then `~/tws` when cwd is inside it;
2. `resolveWorkspaceMetadataRoot(repo)`: `workspaces[<repo>]` (original then canonical key),
   else `<repo>.tws`;
3. `~/tws`.

So external mode is **not** guaranteed to be per-repo isolated: with no marker, no configured
entry, and cwd under `~/tws`, several repositories legitimately share `~/tws/spaces.yaml`.
That is existing `tws` behaviour and this feature neither strengthens nor weakens it. Entry
names are unique per root, not per repo, and the `space list` header always prints the resolved
root so the active file is never ambiguous.

Both locations are outside version control already: the external root is outside the repo, and
`.tws/` is added to `.git/info/exclude` by `EnableCheckoutMode` → `AddGitLocalExclude`
(`internal/enable.go:40`). No `.gitignore` change is required and none is made.

### 4.3 Related files

- `<root>/spaces.yaml` — the registry, mode `0600`.
- `<root>/.spaces.lock` — advisory write lock, mode `0600` (§9). The leading dot keeps it out of
  feature listing through the existing `isReservedDir` prefix rule. It is created only by a
  transaction that has already observed an existing `spaces.yaml`, or by `space add` (§8.5).
- `"spaces.yaml"` is added to `reservedDirs` in `internal/resolve.go` so `validateFeatureName`
  rejects a feature literally named `spaces.yaml`.

## 5. File schema and Go types (version 1)

```yaml
version: 1
spaces:
  - name: learning
    kind: learning
    path: /Users/me/Projects/acme-learning
    description: /teach-owned learning notes for acme
    added_at: 2026-08-10T21:14:03Z
  - name: tickets
    kind: tickets
    path: tickets
    description: tesseratickets store for this workspace
    added_at: 2026-08-10T21:15:40Z
  - name: patching
    kind: patching
    path: workspace-sibling-links/patching
    feature: workspace-sibling-links
    description: tpatch artifacts for this feature
    added_at: 2026-08-10T21:16:02Z
    updated_at: 2026-08-11T09:02:11Z
```

Go types in `internal/spaces.go`. Optional timestamps are pointers so they are genuinely
omittable; `time.Time` with `omitempty` is never omitted (settles B7):

```go
const spacesVersion = 1

type SpacesFile struct {
    Version int          `yaml:"version" json:"version"`
    Spaces  []SpaceEntry `yaml:"spaces" json:"spaces"`
}

type SpaceEntry struct {
    Name        string     `yaml:"name" json:"name"`
    Kind        string     `yaml:"kind" json:"kind"`
    Path        string     `yaml:"path" json:"path"`
    Description string     `yaml:"description,omitempty" json:"description,omitempty"`
    Feature     string     `yaml:"feature,omitempty" json:"feature,omitempty"`
    AddedAt     time.Time  `yaml:"added_at" json:"added_at"`
    UpdatedAt   *time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

type SpaceStatus string      // "ok" | "missing" | "not-a-directory"
type SpaceScope string       // "workspace" | "feature"
type SpaceScopeStatus string // "ok" | "feature-missing"

// SpaceView is the only type ever serialized to JSON by the CLI.
type SpaceView struct {
    Name         string           `json:"name"`
    Kind         string           `json:"kind"`
    Path         string           `json:"path"`
    ResolvedPath string           `json:"resolved_path"`
    Description  string           `json:"description,omitempty"`
    Feature      string           `json:"feature,omitempty"`
    Scope        SpaceScope       `json:"scope"`
    ScopeStatus  SpaceScopeStatus `json:"scope_status"`
    Status       SpaceStatus      `json:"status"`
    AddedAt      time.Time        `json:"added_at"`
    UpdatedAt    *time.Time       `json:"updated_at,omitempty"`
}
```

- Absence of the file is never an error for the *file reader*; `readSpaces` returns `nil, nil`,
  mirroring `readRegistry`. Every consumer treats a `nil` file as "no spaces registered" (§8.5).
- Entries are stored sorted by `(feature, name)` with workspace-wide (`feature == ""`) first, so
  the file has a stable diff and `list` needs no re-sort.
- `UpdatedAt` is set only by the feature rename rewrite (§12.1). `add` never sets it.

## 6. Field and path rules

### 6.1 `name`

- Required. Validated with the existing `internal.ValidateAlias` (`^[a-zA-Z0-9._-]+$`, ≤64 chars),
  which already excludes `/` and `\`. `.` and `..` are rejected explicitly.
- Unique **within its scope**: the composite key is `(feature, name)`, `feature == ""` meaning
  workspace-wide. `learning` may exist once workspace-wide and once per feature.

### 6.2 `kind`

- Required, single token, shape-validated only: `^[a-z0-9][a-z0-9-]*$`, ≤32 characters.
- **`tws` enumerates no closed set**: any conforming token is accepted, because the taxonomy
  belongs to the linked tools. `learning`, `tickets`, `patching`, `research`, and `docs` are
  documented conventions offered by shell completion for `--kind`; they are suggestions and are
  never used for validation, so an unknown-but-conforming kind must succeed.

### 6.3 `description`

- Optional, free-form UTF-8, ≤200 characters.
- Rejected when it contains any control character, including newline and tab, so that one entry
  always renders as one output line.

### 6.4 `feature`

- Optional. When set, the entry is feature-scoped.
- Resolved at `add` time with `internal.AnchorFeaturePath(anchor, owners, feature)` (§7.4), which
  is rooted at the **same** `anchor.Root` used for every other operation in the command and
  consumes the `SpaceOwners` value the command already loaded (it performs **no** file read). It
  mirrors `ResolveFeaturePath` semantics: it returns `*internal.ErrAmbiguousFeature` when both
  `<root>/<f>` and `<root>/features/<f>` exist, so ambiguity is surfaced rather than swallowed.
- The feature must already exist; otherwise `feature "x" not found in this workspace`.
- On read, an entry naming a now-missing feature is **not** a validation error: it is reported
  with `scope_status: feature-missing` (§12.2).

### 6.5 `path` — stored form

Two stored forms are legal; there is no marker field, the form is inferred:

- **absolute** — `filepath.IsAbs(p) && filepath.Clean(p) == p`.
- **workspace-relative** — not absolute, `filepath.Clean(p) == p`, not `""`, not `.`, does not
  begin with `..`, and `filepath.Join(root, p)` stays lexically under `root`.

Resolution is one rule: `resolved = p` if absolute, else `filepath.Join(anchor.Root, p)`.

### 6.6 `path` — normalization at `add` time

Given user input `in` and the resolved `anchor`:

1. `abs := cleanAbsolute(in)` — a relative CLI argument resolves against the **current working
   directory**, standard shell behaviour. It never resolves against the spaces root at input time.
2. If `filepath.Rel(anchor.Canon, abs)` yields a path that is neither `.` nor prefixed with `..`,
   store that relative path.
3. Otherwise retry step 2 with `canonicalize(abs)`. This second attempt is required so symlinked
   roots (macOS `/var` → `/private/var`, which every `t.TempDir()` test hits) still produce
   portable relative entries.
4. Otherwise store `abs` as an absolute entry.

Targets inside the spaces root are stored workspace-relative (portable); targets outside are
stored absolute. The `.` case (target *is* the spaces root) is rejected:
`refusing to register the workspace root itself`.

### 6.7 Symlinks

- **Storage is lexical.** `EvalSymlinks` is applied only through the containment retry in §6.6
  step 3. A deliberately symlinked space keeps its symlink path in the file.
- **Validation follows symlinks.** Existence and type use `os.Stat` (which follows), not
  `os.Lstat`. A symlink to a directory is a valid space.
- A symlink whose physical target is outside the spaces root is **allowed** — pointing at other
  tools' directories is the point of the feature. The `..` rule of §6.5 constrains the stored
  literal only.
- `spaces.yaml` and `.spaces.lock` are the exception: `os.Lstat` is used on them and a symlink is
  refused with `refusing to follow symlinked <path>`. Per §8.3 this refusal is fatal to every
  runtime command that consults workspace features or spaces.

### 6.8 Existence and type checks

- At `add`: `os.Stat(resolved)` must succeed and `IsDir()` must be true. Failures:
  - not found → `space target <resolved> does not exist`
  - not a directory → `space target <resolved> is not a directory`
- Git-ness is **not** checked. Unlike `inspectRegistryTarget` in `internal/registry.go`, any
  existing directory is acceptable.
- At `list`/`show`, the target is re-checked and reported as computed status (§10.4). A missing
  target is never an error and never mutates the file.

### 6.9 Duplicates

- Duplicate `(feature, name)`:
  - identical `kind` **and** identical resolved path → idempotent no-op, prints
    `already registered: <name>`, exit 0, file untouched;
  - otherwise → `space "<name>" already exists in this scope; remove it first`, exit 1.
- Duplicate resolved path within the same scope under a different name →
  `path <resolved> is already registered as "<other>"`, exit 1. Across different scopes it is
  allowed (a feature may legitimately re-point at a workspace-wide directory). Sameness is
  decided by resolved path and filesystem identity, not by raw string equality, so a second
  spelling of the same directory is refused.
- Validation of a hand-edited file applies the same rule: duplicates are keyed on the resolved
  path, so an absolute entry that shadows a workspace-relative entry for the same directory in
  the same scope is rejected at read time.

## 7. Name-to-path protection (settles B2 and B6)

In external mode `ListFeaturesResolved` treats every non-reserved directory under the scanned
root as a feature; in checkout mode the legacy-layout branch does the same for `.tws/*`. A
relative top-level space such as `learning` would therefore appear as a phantom feature, and —
far worse — `tws delete learning` or `tws add learning` would operate on a directory `tws` does
not own. Read-time exclusion alone does not prevent that, so protection has two layers, both
inert when `spaces.yaml` is absent.

### 7.1 The single ownership query

```go
// SpaceOwners maps a directory name that a feature listing would surface to
// the name of the registered space entry that owns it. The maps are the
// exact-spelling fast path; the lookup methods add filesystem identity.
type SpaceOwners struct {
    TopLevel map[string]string // "<seg1>" directly under root -> owning space name
    Features map[string]string // "<seg2>" under root/features -> owning space name

    root    string             // the one root these owners were derived from
    targets []spaceOwnerTarget // every registered target, resolved and stat'ed
}

// Lookups are always performed through these methods, never by indexing the
// maps, so a differently spelled but identical directory is still owned.
func (o SpaceOwners) TopLevelOwner(name string) (string, bool) // "<root>/<name>"
func (o SpaceOwners) FeatureOwner(name string) (string, bool)  // "<root>/features/<name>"
func (o SpaceOwners) OwnerOfDir(dir string) (string, bool)     // an actual directory path

// SpaceDirOwners scans the given root and only that root. It never re-resolves
// a root of its own and never reads a second spaces.yaml.
//
// A nil/absent <root>/spaces.yaml yields a zero SpaceOwners and a nil error.
// Any other read/decode/validate failure is returned already wrapped as
//   cannot verify registered spaces in <root>/spaces.yaml: <err>
// so every caller propagates one identical message.
func SpaceDirOwners(root string) (SpaceOwners, error)
```

Population rules, from a **single** read of `<root>/spaces.yaml`:

- Every entry contributes its **resolved target** to `targets`, together with its `os.Stat`
  result when the target exists. This is the filesystem-identity half of the query.
- An entry whose resolved path is **inside `root`** contributes its first root-relative segment
  to `TopLevel`, except that a root-relative path of the form `features/<seg2>/...` contributes
  `<seg2>` to `Features` instead. `tws space add` only ever stores such an entry
  workspace-relative (§6.6), but hand-edited metadata may store it absolute, and both forms are
  treated identically. Containment is decided lexically against both the literal and the
  canonical spelling of `root`.
- An entry whose resolved path is **outside `root`** contributes no map segment; it can only be
  matched by filesystem identity (for example a symlink that names a directory inside `root`).
- A candidate segment is **dropped** when the directory it names — `<root>/<seg1>` for a
  `TopLevel` candidate, `<root>/features/<seg2>` for a `Features` candidate — is a real feature
  directory. That exception is what makes the roadmap hub layout work: `patching` stored as
  `<feature>/patching` contributes the segment `<feature>`, which is a real feature and is
  therefore never claimed as a space directory. `OwnerOfDir` re-applies the same exception, so
  identity lookups can never claim a feature directory either.

**Filesystem identity (settles the case-insensitivity and absolute-in-root gaps).** A lookup
first consults the map, then compares the candidate directory with each target by:

1. cleaned-absolute equality;
2. canonical equality, where a path that does not exist yet canonicalizes through its longest
   existing ancestor; and
3. `os.SameFile` on both `os.Stat` results, when both paths exist.

Step 3 is what makes `<root>/LEARNING` and `<root>/learning` the same registered directory on a
case-insensitive volume. When neither path exists, only the safe lexical and canonical
comparisons apply.

```go
// isFeatureDir reports whether dir carries a tws feature signal.
// Signals: stack.yaml, worktrees/, FEATURE.md.
func isFeatureDir(dir string) bool
```

**Accuracy note on existing detectors.** These signals are defined here, not inherited:
`internal.DetectFeatureFromCwd` (`internal/paths.go:126-131`) checks only `worktrees/` and
`stack.yaml` — it never checks `FEATURE.md`; and `Workspace.DetectFeatureFromCwdE`
(`internal/resolve.go:206`) checks no on-disk signal at all, being purely lexical plus
`isReservedDir`. `FEATURE.md` is added to the set here because `addCheckout`
(`internal/cli/add.go:112`) creates `FEATURE.md` and `inject/` but neither `stack.yaml` nor
`worktrees/`, so a fresh checkout feature would otherwise be unrecognised.

**One read per operation.** `SpaceDirOwners` is called **once per command invocation or once per
list call**, never once per feature. Listing loops, guard batches, and `space list` scope
detection all consume the single returned `SpaceOwners` value.

**Recursion fence (mandatory).** Reading, decoding, validating, or deriving owners from
`spaces.yaml` — `readSpaces`, `decodeSpaces`, `validateSpaces`, `ownersFrom`, `isFeatureDir`,
`SpaceDirOwners`, `GuardFeatureName`, `guardFeatureNameIn` — **must never call feature resolution
or feature listing**: not `Workspace.ResolveFeaturePath`, `ResolveFeaturePathOrLegacy`,
`ListFeaturesResolved`, `LegacyFeatureNames`, `ListFeatures`, `ListFeaturesE`,
`RequireFeaturePath`, `RequireWorkspace`, or `TwsRoot`. `ListFeaturesResolved` calls
`SpaceDirOwners`, so any such call would be unbounded recursion. Feature-directory questions
inside the spaces layer are answered only by direct `os.Stat` / `os.Lstat` probes on paths joined
from the root that was passed in (`isFeatureDir`, §7.2, `spaceScopeStatus`). This is asserted by
criterion 22's `rg` check extended to `internal/spaces.go`.

**Owner derivation from an already-read file.** A command that has already read the file (`space
list`, `space show`, `space remove`, and both §12 transactions after their locked read) derives
owners with the pure `ownersFrom(root, f)` and **must not** call `SpaceDirOwners` a second time.
`SpaceDirOwners` is the read-plus-derive convenience for callers that hold no file yet (guards,
listings, the migrate preflight).

### 7.2 Write-time guard on `space add`

`space add` refuses a resolved target that **is** a feature directory (`isFeatureDir` true, or the
name is a feature directory of `anchor.Root` itself):

```
cannot register <path>: it is the feature directory "<name>" in this workspace
```

The "is a feature of this workspace" half of that test is a **direct existence probe rooted at
`anchor.Root`** — `<anchor.Root>/<name>` in external and checkout-legacy layout,
`<anchor.Root>/features/<name>` in checkout new layout — never `Workspace.ListFeaturesResolved`,
`internal.ListFeatures`, or any resolver that re-derives a root of its own. When `TWS_ROOT` makes
`TwsRoot() != ws.MetadataRoot` the two roots diverge, and only the anchor root is authoritative
here; using the listing API would consult the wrong root and would also violate the §7.1 recursion
fence.

Registering a directory *inside* a feature directory stays allowed (N10, roadmap hub layout).

### 7.3 `GuardFeatureName` — the explicit, call-site-installed guard

```go
// GuardFeatureName fails closed when feature would collide with a registered
// space directory under the given root. root MUST be the root the calling
// operation actually reads from, writes to, or destroys under.
//
// Absent <root>/spaces.yaml -> nil, with no file, lock, or directory created.
func GuardFeatureName(root, feature string) error
```

It reports a conflict when `owners.TopLevelOwner(feature)` or `owners.FeatureOwner(feature)`
resolves an owner — that is, when `<root>/<feature>` or `<root>/features/<feature>` is, by
spelling or by filesystem identity, a registered target. Both layouts are consulted regardless of
mode, so one function serves external, checkout-legacy, and checkout new-layout roots without
being told the mode.

Error text, used verbatim everywhere (`ErrSpaceNameConflict` carries the feature name, the owning
space name, and the root so callers format it consistently):

```
cannot use feature name "learning": it is the top-level directory of registered space "notes" in <root>/spaces.yaml; choose another feature name or run 'tws space remove notes'
```

**The guard is NOT installed in `Workspace.ResolveFeaturePath` or
`Workspace.ResolveFeaturePathOrLegacy`.** Those stay generic, guard-free path resolvers. Burying
conflict logic there would (a) make a pure path join depend on registry state for every caller
including tight listing loops, and (b) let `internal/checkout_health.go:514` and `:882` —
which `continue` on any resolve error — silently swallow an unreadable-`spaces.yaml` failure.
Both problems disappear when the guard lives at named call sites instead.

### 7.4 Anchor-rooted feature resolution for the `space` commands

```go
// AnchorFeaturePath resolves feature under anchor.Root using the SpaceOwners
// value the caller already loaded. It performs NO file read of its own and
// never resolves a second root.
func AnchorFeaturePath(anchor SpacesAnchor, owners SpaceOwners, feature string) (string, error)
```

Mirrors `ResolveFeaturePath` but is rooted at `anchor.Root`: checkout mode prefers
`<root>/features/<f>` and falls back to legacy `<root>/<f>`, returning `*ErrAmbiguousFeature`
when both exist; external mode uses `<root>/<f>`. Because `owners` is passed in, a space
directory can never masquerade as a feature and no second `spaces.yaml` read occurs. The `space`
subcommands use only this resolver, so every path they touch derives from `anchor.Root`.

### 7.5 Read-time exclusion in feature listing

The `SpaceOwners` value is consumed for exclusion by:

| Consumer | Root passed | Lookup used |
| --- | --- | --- |
| `Workspace.ListFeaturesResolved` — external branch (`internal/resolve.go:139-145`) | `w.MetadataRoot` | `TopLevelOwner` |
| `Workspace.ListFeaturesResolved` — checkout legacy branch (`internal/resolve.go:130-137`) | `w.MetadataRoot` | `TopLevelOwner` |
| `Workspace.ListFeaturesResolved` — checkout new-layout branch (`internal/resolve.go:121-129`) | `w.MetadataRoot` | `FeatureOwner` |
| `internal.ListFeaturesE()` non-workspace fallback (`internal/paths.go:146`) | `TwsRoot()` (the root it reads) | `TopLevelOwner` |
| `Workspace.LegacyFeatureNames` (`internal/resolve.go:99`) | `w.MetadataRoot` | `TopLevelOwner` |
| `MigrateAllFeatures` batch preflight (`internal/migrate.go:59`, §12.4, via `BeginSpacesLayoutMigration`) | `ws.MetadataRoot` | both |
| `space list` scope detection (§10.2) | `anchor.Root` | both |

Each of these performs exactly one `SpaceDirOwners` call per invocation.

`LegacyFeatureNames` returns `[]string` with no error channel and has exactly one caller,
`internal/cli/migrate.go:30`, a `ValidArgsFunction`. It therefore stays best-effort: an
unreadable `spaces.yaml` yields no completion candidates (§8.3, completion row).

**No change is needed to two existing detectors, and none is made:**

- `internal.DetectFeatureFromCwd` (`internal/paths.go:95`, callers `internal/cli/hooks.go:81`,
  `:147`, `internal/cli/decisions.go:47`, `:128`) already requires `worktrees/` or `stack.yaml`
  under `TwsRoot()/<seg>` (`internal/paths.go:126-131`) before returning a feature, so a plain
  sibling space directory can never be auto-detected as a feature by it.
- `Workspace.DetectFeatureFromCwdE` (`internal/resolve.go:206`) has **no non-test callers**
  (`internal/resolve_test.go:208`, `:223` only). Adding an exclusion and a file read there would
  be dead weight; `space list` uses its own anchor-rooted scope detection instead.

`inferExternalRepoRoot` (`internal/workspace.go:339`) needs **no** change: it promotes a
directory only when `LoadStack` succeeds *and* an active worktree path exists, which a sibling
space never satisfies. This is asserted by a regression test rather than changed.

**Scanned root vs. spaces root — no cross-root reads.** Every listing call site passes the exact
root it is scanning and applies exclusions only from a `spaces.yaml` found in *that* root. When
the scanned root differs from the external anchor (external mode with `TWS_ROOT` set),
`SpaceDirOwners(w.MetadataRoot)` finds no file, the exclusion set is empty, and the listing is
byte-identical to today's output. This is correct rather than a compromise: spaces registered
under `$TWS_ROOT` create directories under `$TWS_ROOT`, so they cannot appear as phantom features
in a scan of `<repo>.tws`. Reaching across roots is explicitly forbidden (N11).

### 7.6 Exact guard call sites

Two kinds of site appear below:

- sites that **bypass** `Workspace.ResolveFeaturePath` entirely, building paths from
  `internal.FeaturePath` / `internal.WorktreePath` / `ws.FeaturePath`;
- sites that **do call** `ResolveFeaturePath` (`new.go:60`, `archive.go:46`, `delete.go:53`,
  `rename.go:102`, `open.go:56`, `open_checkout.go:19`, `export.go:45`) — which is deliberately
  guard-free (§7.3) and therefore never consults `spaces.yaml` on their behalf.

Both kinds need the **same explicit logical-name guard at the call site**, evaluated with the root
the operation actually touches, before any resolution, creation, mutation, or destruction. Using
`ResolveFeaturePath` is never a substitute for the guard.

**External bypasses — root is `internal.TwsRoot()`,** because each builds its paths from
`internal.FeaturePath` / `internal.WorktreePath`:

| Site | Location | Class |
| --- | --- | --- |
| `addExternal` | `internal/cli/add.go:60` | creates |
| `createWorktree` (`tws new`, `tws add -n`) | `internal/cli/new.go:159` | creates |
| `archiveExternal` | `internal/cli/archive.go:82` | destroys worktree |
| `deleteExternal` | `internal/cli/delete.go:137` | destroys — via the §12.2 transaction |
| `renameBranchExternal` | `internal/cli/rename.go:170` | mutates |
| external `tws sync` | `sync` `RunE`, `internal/cli/sync.go:38` (after the `ModeCheckout` dispatch, before `ws.ResolveFeaturePath` at `:39`) | mutates |
| `recreateExternal` (`tws import`) | `internal/cli/importcmd.go:252` | creates |
| external `open --feature-dir` | `open` `RunE`, `internal/cli/open.go:85` (before `internal.FeaturePath` / `openDirect`) | reads + execs agent |
| external `open` normal worktree | `internal/cli/open.go:101` (before `resolveOpenArgs`) | writes (`InjectFiles`) + execs |
| external `open --all` | `open` `RunE`, `internal/cli/open.go:75` (before `openAll(args[0])` at `:77`) | reads + execs |

**Checkout surfaces — root is `ws.MetadataRoot`:**

| Site | Location | Class |
| --- | --- | --- |
| `addCheckout` | `internal/cli/add.go:112` | creates |
| `createCheckoutBranch` (`tws new`) | `internal/cli/new.go:60` | creates |
| `archiveCheckout` | `internal/cli/archive.go:46` | mutates |
| `deleteCheckout` | `internal/cli/delete.go:53` | destroys — via the §12.2 transaction |
| `recreateCheckout` (`tws import`) | `internal/cli/importcmd.go:192` | creates |
| `renameBranchCheckout` | `internal/cli/rename.go:102` | mutates |
| `runCheckoutOpen` | `internal/cli/open_checkout.go:19` | reads + execs |
| checkout `open --feature-dir` | `internal/cli/open.go:56` (before `ws.ResolveFeaturePath`) | reads + execs |
| `MigrateFeatureLayout` | `internal/migrate.go:23` (after `validateFeatureName`, before any `Lstat`/`MkdirAll`/`Rename`) — via the §12.4 `BeginSpacesLayoutMigration` transaction | moves the feature directory (§12.4) |
| `MigrateAllFeatures` preflight | `internal/migrate.go:59` (fail-closed one-transaction batch preflight, §12.4) | moves feature directories |

**`openAll` and `openDirect` keep their current signatures.** Both are `func(...)` with no error
return and both call `os.Exit`; the spec does **not** plumb an error out of them. The guard is
installed in the parent `open` `RunE` **before** either is called, so `tws open learning --all`
and `tws open learning --feature-dir` return the §7.3 error from `RunE` and never reach
`openAll` / `openDirect` (criterion 20).

**Void helpers never carry the guard; their `RunE` does.** Three helpers reached from a named
feature argument have no usable error channel, so a guard placed inside them would be swallowed
and the command would exit 0 with a stdout line instead of exiting 1 with the §7.3 message:

| Helper | Signature | Guard lives instead in |
| --- | --- | --- |
| `syncFeature` (`internal/cli/sync.go:167`) | returns `syncResult{Complete bool}`; a failure there degrades to `sync incomplete`, losing the message | `sync` `RunE` at `internal/cli/sync.go:38` (row above). One guard, one read, and it also covers `tws sync --abort` / `--continue`, which return from `handleSyncAbort` / `handleSyncContinue` (`:79`, `:96`) without ever reaching `syncFeature` |
| `syncFeatureTemplate` (`internal/cli/template.go:66`) | `func(string, []string)`; prints `"<feature>: <err>"` to stdout and returns | `templateSyncCmd` `RunE`, single-feature branch (§8.5.1) |
| `installHooksForFeature` (`internal/cli/hooks.go:97`) | `func(string)`; prints `"  [x] <feature>: <err>"` to stdout and returns | `hooksInstallCmd` `RunE`, single-feature branch (§8.5.1) |

The last two are the reason the `RequireFeaturePath` wrapper guard below, on its own, is not
sufficient for `tws template sync <feature>` and `tws hooks install <feature>`: the guard error is
real, but the void helper discards it. §8.5.1 pins the exact remedy and its blast radius.

**Whole-command transactions** (guard evaluated under the spaces lock, never as a separate
unlocked read): `tws delete` (§12.2) and `tws rename feature` (§12.1), both rooted at the root
whose feature directory they destroy or rename.

**One package-level wrapper — `internal.RequireFeaturePath` (`internal/resolve.go:247`)** guards
with `ws.MetadataRoot`. This is a convenience wrapper (`RequireWorkspace` + `ResolveFeaturePath`),
not the generic resolver, and it is never used inside a feature-iteration loop. Guarding it once
covers `tws decide`, `tws decisions`, `tws inject`, `tws push`, `tws stack`, `tws hooks install`,
`tws template sync`, `tws doctor <feature>` (`checkFeatureE`), and `runCheckoutSync`
(`internal/cli/checkout_sync.go:11`) — all of which read or write inside a named feature
directory. For `tws template sync <feature>` and `tws hooks install <feature>` it is necessary but
**not sufficient**, because their void helpers swallow the error (table above); §8.5.1 adds the
`RunE`-level guard that makes them exit nonzero. Its one completion consumer,
`internal.ListBranches` (`internal/paths.go:163`), discards the error and yields no branch
candidates, consistent with the completion row of §8.3.

**`tws export` gets its own guard** at `internal/cli/export.go:45` with `ws.MetadataRoot`, the
root it resolves under. It is the one remaining surface that names a feature explicitly and
routes only through `ws.ResolveFeaturePath`.

**Deliberately unguarded, with the reason stated:**

- `Workspace.ResolveFeaturePath` / `ResolveFeaturePathOrLegacy` — generic resolvers (§7.3).
- The per-feature `ResolveFeaturePath` calls inside `internal/cli/list.go:49`,
  `internal/checkout_health.go:514`, and `:882`. Their loop input comes from
  `ListFeaturesResolved`, which has **already** removed space-owned names via §7.5, so a
  registered space can never reach them. Their `continue`-on-error branches therefore cannot mask
  a spaces failure: an unreadable `spaces.yaml` aborts `ListFeaturesResolved` before the loop
  starts (§8.3).
- `internal.PlanCheckoutSessionLinks` (`internal/session.go:343`) — reached only from the
  checkout open/session flow, which is guarded at `runCheckoutOpen`, with a feature name that a
  guarded creating surface produced.
- `tws close` (`internal/cli/close.go`) — **deliberately guard-free and safe**. Its external
  branch derives only a tmux session name (`sanitizeSessionName(feature + "/" + branch)`) and
  kills that session; it joins no root, and creates, reads, or removes nothing under
  `TwsRoot()`. Its checkout branch delegates to `runCheckoutClose`, which reads the recorded
  session state (`LoadCheckoutAgentSession`) rather than a caller-supplied name. A registered
  space therefore cannot be reached through it, and adding a guard would only add a
  `spaces.yaml` read to a command that touches no feature directory.

**One guard read per command invocation.** Every guarded surface above must evaluate its guard
exactly once per run; guards are never placed inside a loop body or on both a `RunE` and a helper
it calls:

- `open` — E8/E9/E10 are mutually exclusive `RunE` branches (`--all`, `--feature-dir`, normal), so
  at most one guard runs.
- `sync` — one guard in `RunE` (`sync.go:38`) covers the plain, `--abort`, and `--continue` paths;
  `syncFeature` carries none.
- `rename feature` and `delete` — a single locked read feeds `guardFeatureNameIn` for **both**
  names (rename `old` and `new`) and the containment scan; `GuardFeatureName` is never called
  twice.
- `template sync <feature>` / `hooks install <feature>` — one `RunE` guard; the `--all` branches
  carry **no** per-feature guard, because `ListFeaturesE` has already applied the §7.5 exclusion
  from its own single read.
- `MigrateAllFeatures` — one `BeginSpacesLayoutMigration` transaction, hence one locked read, for the whole batch (§12.4).
- `space list` / `space show` / `space remove` — one `readSpaces`, with owners derived via
  `ownersFrom` (§7.1); per-entry `status` and `scope_status` are `os.Stat` / existence probes that
  re-read no metadata.

Because each guard receives the root its operation will touch, divergence is handled correctly
rather than silently: when `TWS_ROOT` makes `TwsRoot() != ws.MetadataRoot`, external `tws add`,
`tws new`, and `tws delete` are guarded against `$TWS_ROOT/spaces.yaml` (where their features
live), while `tws list` scans `<repo>.tws` and finds no spaces file there.

## 8. Reading, writing, and failure behaviour

### 8.1 Read

`os.Lstat` (reject symlink) → `os.ReadFile` → permissive version probe → strict decode with
`dec.KnownFields(true)` → `validateSpaces`. Identical staging to `decodeRegistry`
(`internal/registry.go:164`), so a future-version file is reported as a version error rather than
an unknown-field error, and is never silently truncated by a later write.

- missing file → `(nil, nil)`, no error, nothing created;
- `version <= 0` or unparsable → `spaces file <path> is malformed: missing or invalid schema version`;
- `version > 1` → `spaces file <path> uses schema version N but this tws supports version 1; upgrade tws instead of modifying the file`;
- unknown field → decode error naming the field;
- validation failure (§6) → error naming the offending entry index and field;
- symlinked `spaces.yaml` → `refusing to follow symlinked <path>`.

### 8.2 Write

`saveSpaces(root, f)` follows `saveRegistry` (`internal/registry.go:264`) exactly:
`os.CreateTemp` in `root` → `Write` → `Sync` → `Close` → `Chmod(0600)` → `Rename`. Write, sync,
and rename errors are never masked; cleanup errors are joined with `errors.Join`. Mutators
re-read **under the lock**; a read taken before the lock is never reused for a write.

### 8.3 Strict failure contract (settles B3)

There is **one** rule and **one** documented exception. There is no warn-and-continue path, no
`Warnings` channel, no silent empty exclusion set, and no `FeatureListing` type.

> If `<root>/spaces.yaml` exists but is unreadable, symlinked, malformed, carries an unknown
> field, or declares a future schema version, then **every runtime command that consults
> workspace features or spaces fails explicitly and exits nonzero**, having created, renamed, or
> deleted nothing.

| Caller class | Behaviour |
| --- | --- |
| `tws space add / list / show / remove` | exit 1 with the underlying spaces error; file left byte-identical |
| Every guard, owner load, and transaction — `SpaceDirOwners`, `GuardFeatureName`, `BeginSpacesFeatureDelete` (§12.2), `BeginSpacesFeatureRename` (§12.1), `BeginSpacesLayoutMigration` (§12.4) | exit 1 with `cannot verify registered spaces in <root>/spaces.yaml: <err>`; nothing created, renamed, moved, or deleted. `AnchorFeaturePath` reads nothing itself; its input `SpaceOwners` already came from the command's single guarded load |
| Every runtime command that lists features or scans for legacy features — `tws list` (both modes), `tws doctor` (all-features and checkout report), `tws open` interactive picker, `tws template sync --all`, `tws hooks install --all`, `tws migrate-layout --all` (§12.4), `BuildCheckoutList`, `buildFeatureEntries` | exit 1 with the same message; **no partial listing is printed and nothing is moved** |
| Shell completion only — `internal.ListFeatures()` and `internal.ListBranches()` called from a `ValidArgsFunction`, plus `Workspace.LegacyFeatureNames` | best-effort: return **no candidates**, print nothing, leave the command's exit status untouched. This is the single documented degradation, because completion has no error channel |

A **missing** file is none of these cases: it yields a zero `SpaceOwners`, no error, and no
warning (§8.5).

### 8.4 The error-returning listing API

`Workspace.ListFeaturesResolved() ([]string, error)` keeps its current signature and now returns
the `SpaceDirOwners` error directly. Nothing wraps it a second time — `SpaceDirOwners` already
emits the canonical `cannot verify registered spaces in ...` message (§7.1). Its four existing
non-test callers (`internal/cli/list.go:36`, `internal/cli/open_checkout.go:61`,
`internal/checkout_health.go:497`, `:867`) already propagate the error and need no change beyond
recompilation.

The package-level convenience wrapper is split in two:

```go
// ListFeaturesE returns the sorted feature names for the resolved workspace,
// propagating any failure. Runtime commands MUST use this.
func ListFeaturesE() ([]string, error)

// ListFeatures is the completion-only wrapper. It discards errors and returns
// no candidates on failure. Do not use it from a RunE path.
func ListFeatures() []string { names, _ := ListFeaturesE(); return names }
```

`ListFeaturesE` applies the §7.5 exclusion in both of its branches, passing `w.MetadataRoot` in
the workspace branch and `TwsRoot()` in the non-workspace fallback branch
(`internal/paths.go:146`) — always the root it is actually reading.

The four **real runtime callers** of the legacy wrapper move to `ListFeaturesE`:

| Caller | Location | Change |
| --- | --- | --- |
| `tws doctor` (no-arg, all features) | `internal/cli/doctor.go:45` | already `RunE`; return the error |
| `tws open` interactive picker | `internal/cli/open.go:197` (`resolveOpenArgs`) | already returns `error`; return it |
| `tws template sync --all` | `internal/cli/template.go:39` | command migrates `Run:` → `RunE:`; its `os.Exit(1)` usage path becomes a returned error (§8.5.1) |
| `tws hooks install --all` | `internal/cli/hooks.go:65` | command migrates `Run:` → `RunE:`; the existing `fmt.Println("Error: ...")`-then-return paths become returned errors so they no longer exit 0 (§8.5.1) |

Every other `internal.ListFeatures()` reference is a `ValidArgsFunction` and stays on the
completion wrapper: `internal/cli/{doctor,export,close,delete,push,sync,rename,decide,archive,inject,new,decisions,stack,template,open}.go`.

### 8.5 Absent registry — exact no-op contract (G9)

When `<root>/spaces.yaml` does not exist:

- `readSpaces(root)` returns `(nil, nil)`; `SpaceDirOwners(root)` returns a zero `SpaceOwners`
  and `nil`; `GuardFeatureName` returns `nil`; the `MigrateAllFeatures` batch transaction adds no error
  and drops no candidate.
- `BeginSpacesFeatureDelete` and `BeginSpacesFeatureRename` return a **true no-op transaction**:
  no lock is acquired, `<root>/.spaces.lock` is **not** created, `spaces.yaml` is **not** created,
  and `Commit()` / `Release()` write nothing and return `nil`.
- No directory, marker, temp file, or lock file is created anywhere by any command other than
  `tws space add` (§10.1). This filesystem-effect guarantee is absolute and admits no exception.
- **Lstat-before-lock (mandatory).** Every mutating path except `tws space add` — `tws space
  remove`, `BeginSpacesFeatureRename` (§12.1), `BeginSpacesFeatureDelete` (§12.2), and any future
  mutator — probes `<root>/spaces.yaml` with `os.Lstat` **before** it calls
  `acquireSpacesLock`, and takes its absent-registry path when the probe reports
  `fs.ErrNotExist`. `acquireSpacesLock` creates `<root>` (`MkdirAll`) and `<root>/.spaces.lock`,
  so acquiring it first would itself break the guarantee above. The probe is **advisory only**
  in the other direction: when it reports the file *exists*, the lock is still acquired first and
  the authoritative read happens under it, and a file that vanished in between degrades back to
  the absent-registry path and releases the lock (§9).
- `tws space remove <name>` with no `spaces.yaml` therefore fails with `no space named <name>`
  and exit 1, having created **no** root directory, **no** `.tws-workspace` marker, **no**
  `.spaces.lock`, **no** temp file, and **no** `spaces.yaml` (§10.5). Nothing is created merely to
  discover that there is nothing to remove.
- `space list` and `space show` are read-only and take no lock at all (§9), so the same
  guarantee holds for them by construction.
- Every pre-existing command's **success path** — stdout, stderr, and exit status — is
  **byte-for-byte identical** to the pre-feature binary, and so is every pre-existing failure
  path except the ones enumerated in §8.5.1.

### 8.5.1 Intentionally normalized error paths (the one G9 carve-out)

`tws template sync` and `tws hooks install` cannot propagate a spaces failure at all today: they
use `Run:` (no error return), print diagnostics to **stdout**, and either `return` (exit 0) or
call `os.Exit(1)`. Making §8.3 achievable for their `--all` listing requires migrating both to
`RunE:` (§8.4), and that migration necessarily re-routes their *pre-existing* failure paths
through `cli.Execute`, which prints to stderr and returns 1. Those changes are deliberate, are
the only permitted deviations from G9, and are exhaustively listed here:

| Path | Before | After |
| --- | --- | --- |
| `tws template sync` with no feature arg and no `--all` (`internal/cli/template.go:50-54`) | usage lines on **stdout**, `os.Exit(1)` | usage error returned from `RunE`, printed on **stderr**, exit 1 |
| `tws template sync --all` when feature listing fails (new, §8.3) | not reachable (listing could not fail) | error on stderr, exit 1 |
| `tws template sync <feature>` when `<feature>` is a registered space or spaces metadata is untrusted (new, §7.3/§8.3) | not reachable | `RunE` guard error on stderr, exit 1 |
| `tws hooks install` when `RequireWorkspace()` fails (`internal/cli/hooks.go:55-58`) | `Error: <err>` on **stdout**, exit **0** | error returned, stderr, exit **1** |
| `tws hooks install` in checkout mode (`internal/cli/hooks.go:59-62`) | `Error: hooks install requires linked worktrees; not supported in checkout mode` on **stdout**, exit **0** | same sentence as a returned error, stderr, exit **1** |
| `tws hooks install` with no feature detected (`internal/cli/hooks.go:82-85`) | message on **stdout**, `os.Exit(1)` | returned error, stderr, exit 1 |
| `tws hooks install --all` when feature listing fails (new, §8.3) | not reachable | error on stderr, exit 1 |
| `tws hooks install <feature>` when `<feature>` is a registered space or spaces metadata is untrusted (new, §7.3/§8.3) | not reachable | `RunE` guard error on stderr, exit 1 |

**Single-feature guard placement (the B1 rule).** `syncFeatureTemplate` (`template.go:66`) and
`installHooksForFeature` (`hooks.go:97`) are `func(...)` with no error return: the
`RequireFeaturePath` guard of §7.6 fires inside them, but they print `"<feature>: <err>"` /
`"  [x] <feature>: <err>"` to stdout and return, so the command would still exit 0. Therefore, in
the **single-feature branch only** of each `RunE`:

1. resolve the concrete current root — `ws, wsErr := internal.RequireWorkspace()`, root is
   `ws.MetadataRoot`, the exact root `RequireFeaturePath` will resolve under. `hooksInstallCmd`
   already holds `ws` (it resolves it at `hooks.go:54` and refuses checkout mode);
   `templateSyncCmd` must resolve it itself;
2. call `internal.GuardFeatureName(ws.MetadataRoot, feature)` **before** invoking the legacy void
   helper, and **return** its error, so a registered-space conflict yields the verbatim §7.3
   message and untrusted metadata yields the canonical
   `cannot verify registered spaces in <root>/spaces.yaml: <err>` — both on stderr, exit 1, with
   no `inject/` directory created and no hook file written;
3. then call the unchanged void helper exactly as today.

**The carve-out is exactly this and no wider.** Three behaviours are preserved byte-for-byte:

- **Unrelated `RequireFeaturePath` failures keep the legacy shape.** A feature that simply does
  not exist, an ambiguous feature, or an invalid `workspace_mode` still reaches the void helper,
  still prints its `"<feature>: <err>"` / `"  [x] <feature>: <err>"` line to **stdout**, and still
  exits **0**. The `RunE` guard adds no error of its own for these.
- **A failing `RequireWorkspace()` in `templateSyncCmd` is not promoted.** If it returns an error,
  the guard is **skipped** (there is no concrete root to guard with) and the command falls through
  to `syncFeatureTemplate` exactly as today — same stdout line, same exit 0. Only
  `hooksInstallCmd`'s `RequireWorkspace()` failure becomes an error, and only because it is
  already listed as a pre-existing row above. This keeps the absent-registry compatibility
  carve-out exact: with no `spaces.yaml`, `GuardFeatureName` returns `nil` and creates nothing,
  so both commands are indistinguishable from the pre-feature binary.
- **`--all` loop bodies stay best-effort.** The `--all` branches install **no** per-feature guard:
  `ListFeaturesE` has already excluded space-owned names from its single read (§7.5), so a
  registered space can never enter the loop. Inside the loop, `syncFeatureTemplate` and
  `installHooksForFeature` keep printing per-feature errors to stdout and keep not aborting, and
  the run still exits 0. Only the listing call itself can fail the `--all` run (rows above).

Not changed by the migration, and asserted as unchanged: the success output of both commands,
their per-feature progress lines, the `No features found.` empty-state line (stdout, exit 0), and
the per-feature best-effort error lines emitted by `syncFeatureTemplate` and
`installHooksForFeature`, which keep printing to stdout and keep not aborting the run. The
CHANGELOG entry (§13) calls out the exit-status changes explicitly.

### 8.6 Permissions

`spaces.yaml` `0600`, `.spaces.lock` `0600` — the file records filesystem paths that may name
client directories, so it follows the local-state convention rather than world-readable `0644`.
The spaces root is created `0755` only when it does not exist (§10.1); an existing directory's
mode is never changed.

## 9. Concurrency

- Mutating operations (`space add`, `space remove`) and both lifecycle transactions (§12) hold an
  exclusive advisory lock on `<root>/.spaces.lock` for the whole critical section, reusing
  `flockExclusive` / `flockUnlock` from `internal/registry_lock_unix.go`. `AcquireRegistryLock`
  is not reusable because it locks the global XDG registry directory.
- The lock type mirrors `RegistryLock` (`internal/registry.go:102-111`): `Release() error` returns
  `errors.Join(unlockErr, closeErr)` and callers must check it.
- **No invariant may be claimed before the lock is held.** Any pre-lock existence probe is
  advisory only; the authoritative read always happens after acquisition (§12.1, §12.2). The one
  thing a pre-lock probe *does* decide is whether to acquire the lock at all: every mutator except
  `space add` `os.Lstat`s `<root>/spaces.yaml` first and skips acquisition entirely when it is
  absent, because `acquireSpacesLock` would otherwise create `<root>` and `<root>/.spaces.lock`
  and break the §8.5 no-create guarantee. A file that exists at probe time but vanishes before the
  locked read degrades to the absent-registry path and releases the lock.
- Read-only operations (`space list`, `space show`, `GuardFeatureName`, all exclusion reads) take
  **no** lock. The single atomic `Rename` of §8.2 guarantees a reader sees either the old or the
  new complete file.
- Platform boundary: POSIX `flock` (macOS, Linux); Windows unsupported, documented in the same
  words already used for `RegistryLock`.
- If the root is unwritable, mutators fail with the OS error and write nothing partial.

## 10. CLI surface

Parent command `spaceCmd()` in `internal/cli/space.go`, registered in the
`rootCmd.AddCommand(...)` list in `internal/cli/root.go`. Every subcommand uses `RunE`, writes
through `cmd.OutOrStdout()`, and never calls `os.Exit`.

```
tws space add <name> <path> --kind <kind> [--description <text>] [--feature <feature>]
tws space list [--feature <feature>] [--all] [--kind <kind>] [--json]
tws space show <name> [--feature <feature> | --workspace] [--json]
tws space remove <name> [--feature <feature> | --workspace]
```

`--kind` is required on `add`. Completion: `--kind` offers the five conventional kinds,
`--feature` offers `internal.ListFeatures()`, and `show`/`remove` offer registered space names,
consistent with `internal/cli/doctor.go` and `internal/cli/open.go`.

Every subcommand resolves `anchor, err := internal.ResolveSpacesAnchor()` once and passes
`anchor` (or `anchor.Root`) into every helper it calls. `list`, `show`, and the read-only parts of
`add`/`remove` read `spaces.yaml` exactly once per invocation; the mutators take one further
read **under the lock**, which is the authoritative one for the write (§9). No helper ever reads
a root of its own.

### 10.1 `add`

Order: resolve anchor → validate name/kind/description → load owners once with
`SpaceDirOwners(anchor.Root)` (fail per §8.3) → resolve `--feature` via
`AnchorFeaturePath(anchor, owners, feature)` → normalize path (§6.6) → existence and directory
check (§6.8) → feature-dir guard (§7.2) → create `anchor.Root` and, in external mode only,
`EnsureExternalWorkspaceMarker(anchor.Root)`
→ acquire lock → read under the lock (fail per §8.3) → duplicate rules (§6.9) → append with
`AddedAt = time.Now().UTC()` → sort → save → release lock.

The pre-lock `SpaceDirOwners` read is advisory (it only informs `--feature` resolution and the
§7.2 guard); the authoritative read for the write is the one taken under the lock (§9).

```
registered: learning (learning) -> /Users/me/Projects/acme-learning
registered: patching (patching) [feature: workspace-sibling-links] -> /Users/me/tws/acme.tws/workspace-sibling-links/patching
```

Idempotent repeat prints `already registered: learning` and exits 0. `add` is the only
subcommand — and the only command in `tws` — that creates anything for this feature, and it
creates only the spaces root, its `.tws-workspace` marker (external mode), `spaces.yaml`, and
`.spaces.lock`. Never the target.

### 10.2 `list`

Scope selection uses the single `SpaceOwners` value already loaded, plus an anchor-rooted cwd
walk; it never calls `DetectFeatureFromCwdE`:

- no flags, a feature `F` detected under `anchor.Root` → workspace-wide entries **plus** `F`'s;
- no flags, nothing detected → all entries;
- `--feature F` → workspace-wide plus `F`'s; overrides detection; unknown `F` is an error, and a
  name owned by a registered space is refused with the §7.3 conflict;
- `--all` → every entry; mutually exclusive with `--feature`;
- `--kind K` → additional filter, combinable with the above.

A first cwd segment owned by a registered space is never treated as a feature.

The core returns the metadata the CLI needs with the one read it already performed:

```go
type SpaceListResult struct {
    Views        []SpaceView // entries matching the active filters
    Total        int         // registered entries before filtering
    ScopeFeature string      // "" when the listing is not feature-scoped
}

// Scope renders "feature <name>" when feature filtering applies, else "all".
func (r SpaceListResult) Scope() string
```

`spaces.yaml` is never reread to build the header or the empty state.

The human header is printed **before the results in every case, including both empty states**:

```
Workspace: /Users/me/tws/acme.tws (mode: external, scope: all)

learning  learning   /Users/me/Projects/acme-learning
tickets   tickets    /Users/me/tws/acme.tws/tickets  (missing)
patching  patching   /Users/me/tws/acme.tws/workspace-sibling-links/patching  (feature: workspace-sibling-links)
```

`scope` is `feature <name>` whenever feature filtering applies — auto-detected or explicit — and
`all` otherwise (including `--all`). Name and kind are padded to the widest value. The
`(feature: ...)` suffix appears only for feature-scoped entries; `(<status>)` only when the
target status is not `ok`; `(feature missing)` is appended when `scope_status` is
`feature-missing`.

Two distinct empty states, both exit 0 and both after the header:

```
No spaces registered. Use 'tws space add <name> <path> --kind <kind>' to add one.
```

```
No spaces match the active filters (3 registered). Use 'tws space list --all' to see every entry.
```

`--json` never prints the header and stays the bare array of §10.4 (`[]` when empty).

The `Long` help documents the default scope explicitly: a bare list is workspace-wide plus the
auto-detected feature when inside one, otherwise already complete; `--all` is the complete
registry regardless of location.

### 10.3 `show`

`show <name>` resolves by name across all scopes:

- exactly one match → print it;
- more than one → exit 1:
  `space "learning" is ambiguous: workspace, feature "acme"; disambiguate with --feature <name> or --workspace`;
- none → exit 1: `no space named "learning"` (naming the scope when one was selected).

Scope is selected explicitly and threaded into the core as one selector value:

```go
type SpaceSelector struct {
    Name    string
    Scope   SpaceScopeSelector // "" (any) | "workspace" | "feature"
    Feature string
}

func NewSpaceSelector(name, feature string, workspace bool) (SpaceSelector, error)
```

- `show --feature F <name>` matches only `(F, name)`;
- `show --workspace <name>` matches only the workspace-wide entry, which is otherwise
  unreachable whenever a feature-scoped entry shares the name;
- `--feature` and `--workspace` are mutually exclusive on both `show` and `remove`;
- `--feature ""` is not supported — omitting both flags is the bare, any-scope path.

Scoped misses name their scope: `no space named "x" in the workspace scope` and
`no space named "x" in feature "acme"`.

```
Name:        patching
Kind:        patching
Scope:       feature workspace-sibling-links
Path:        workspace-sibling-links/patching
Resolved:    /Users/me/tws/acme.tws/workspace-sibling-links/patching
Status:      ok
Description: tpatch artifacts for this feature
Added:       2026-08-10 21:16:02
Updated:     2026-08-11 09:02:11
```

`Scope:` is `workspace` for workspace-wide entries and `feature <f> (missing)` when the feature
no longer exists. `Description:` and `Updated:` are omitted when empty. A `missing` or
`not-a-directory` status prints and exits **0** — `show` reports, it does not judge.

### 10.4 JSON output (settles B7)

`--json` on `list` encodes a `[]SpaceView` with `json.NewEncoder(cmd.OutOrStdout())` and
`SetIndent("", "  ")`, emitting `[]` when empty; `--json` on `show` encodes one `SpaceView`.
This matches the encoder settings, indentation, trailing newline, and empty-array convention of
`tws registry list --json` (`internal/cli/registry.go:79`). The field sets differ, so **no
byte-level equivalence with the global registry output is claimed**.

`list --json`:

```json
[
  {
    "name": "learning",
    "kind": "learning",
    "path": "/Users/me/Projects/acme-learning",
    "resolved_path": "/Users/me/Projects/acme-learning",
    "scope": "workspace",
    "scope_status": "ok",
    "status": "ok",
    "added_at": "2026-08-10T21:14:03Z"
  },
  {
    "name": "patching",
    "kind": "patching",
    "path": "workspace-sibling-links/patching",
    "resolved_path": "/Users/me/tws/acme.tws/workspace-sibling-links/patching",
    "description": "tpatch artifacts for this feature",
    "feature": "workspace-sibling-links",
    "scope": "feature",
    "scope_status": "ok",
    "status": "ok",
    "added_at": "2026-08-10T21:16:02Z",
    "updated_at": "2026-08-11T09:02:11Z"
  }
]
```

`show --json` emits the same object shape without the enclosing array. Empty `list --json`:

```json
[]
```

- Keys are snake_case, including every computed field (`resolved_path`, `scope`, `scope_status`,
  `status`).
- `description`, `feature`, and `updated_at` are omitted when unset; `updated_at` is a
  `*time.Time` so the key is genuinely absent rather than `"0001-01-01T00:00:00Z"`.
- `resolved_path`, `scope`, `scope_status`, and `status` are always present, computed per
  invocation, and never persisted.
- `scope_status` is `feature-missing` when the entry names a feature that no longer resolves
  under `anchor.Root`; existence is checked directly (`<root>/<f>` or `<root>/features/<f>`), not
  through `ResolveFeaturePath`, so the anchor stays the only root involved.
- JSON output carries no warnings and no human hints.

### 10.5 `remove`

`os.Lstat`s `<root>/spaces.yaml` first (§8.5, §9). When it does not exist the command fails
immediately with

```
no space named "learning"
```

and exit 1, creating **nothing**: no `<root>` directory, no `.tws-workspace` marker, no
`.spaces.lock`, no temp file, and no `spaces.yaml`. Only when the probe finds the file does it
acquire the lock, read under it (fail per §8.3), resolve the selector with the same rules as
`show`, drop the entry, and save. A file that vanished between the probe and the locked read
yields the same `no space named <name>` error. Never touches the target directory — the guarantee
`RegistryRemove` already makes.

```
removed space: learning
```

Removing an entry whose target is already missing succeeds; that is the intended repair path and
the reason no `prune` command exists (N2).

### 10.6 Exit codes

| Situation | Exit |
| --- | --- |
| any success, including empty list, `missing` target, and `feature-missing` scope | 0 |
| idempotent duplicate `add` | 0 |
| conflicting duplicate `add`, invalid name/kind/description/path, non-existent or non-directory target, feature-dir collision (§7.2) | 1 |
| unknown or ambiguous name on `show` / `remove`; unknown or ambiguous `--feature` | 1 |
| malformed / future-version / symlinked / unreadable `spaces.yaml` on **any** `space` subcommand, any guard, or any feature-listing command (§8.3) | 1 |
| feature name collides with a registered space (§7.3) | 1 |
| `tws delete` / `tws rename feature` blocked by registered spaces (§12) | 1 |
| anchor unresolvable (`RequireWorkspace` error with no usable `TwsRoot`) | 1 |

Errors are returned from `RunE`; `cli.Execute` prints them to stderr and returns 1.

## 11. Invocation and auto-detection matrix

`tws space <sub>` must work with no positional workspace argument from every location below.
This is the regression matrix required by `docs/retrospectives/v1.2.7-upgrade-operations.md`, and
it is why `ResolveSpacesAnchor()` builds on `RequireWorkspace()` rather than `MainRepoRoot()`.

| # | cwd | Anchor resolution | Auto-detected feature |
| --- | --- | --- | --- |
| 1 | external source repo root | `TwsRoot()`: env, else marker/config/`<repo>.tws` per §4.2 | none |
| 2 | external linked worktree root `<root>/<f>/worktrees/<b>` | marker walk-up → `<root>` | `f` |
| 3 | nested dir inside that worktree | marker walk-up → `<root>` | `f` |
| 4 | external workspace root `<root>` | marker walk-up → `<root>` | none |
| 5 | external feature dir `<root>/<f>` | `RequireWorkspace` marker fallback → `<root>` | `f` |
| 6 | nested dir inside an external feature dir | same | `f` |
| 7 | checkout repo root | `ws.MetadataRoot` = `<repo>/.tws` | none |
| 8 | checkout feature dir `<repo>/.tws/features/<f>` | `ws.MetadataRoot` | `f` |
| 9 | inside a registered top-level space `<root>/learning/...` | marker walk-up → `<root>` (both variants; see below) | none (§7.5 exclusion) |

**Row 9 — marker detection wins, whether or not the space is itself a Git repository.**

- **Not a Git repo** (plain directory, the common learning/tickets/docs case): `MainRepoRoot()`
  fails, `RequireWorkspace()` falls back to the `.tws-workspace` marker walk-up, and the anchor
  is the surrounding workspace root `<root>` — or, if `inferExternalRepoRoot` cannot name a
  source repository, `ResolveSpacesAnchor` step 4 supplies the same `<root>` from `TwsRoot()`.
- **A Git repo beneath the workspace marker** (e.g. a cloned tesseratickets store checked out at
  `<root>/tickets`): `MainRepoRoot()` succeeds and returns the *space's own* repository, so
  `RequireWorkspace()` resolves that repository — with `Mode = external`, because the space repo
  has no `.tws/config.yaml`. `ResolveSpacesAnchor` step 3 then takes `internal.TwsRoot()`, whose
  **tier-1 `DetectWorkspaceRoot` marker walk-up finds `<root>/.tws-workspace` before any
  `<space>.tws` sibling rule is consulted** (`internal/paths.go:56-71`). The anchor is therefore
  the **parent external workspace**, and `tws space list` keeps reading `<root>/spaces.yaml` and
  showing the parent workspace's entries. The sibling repo's own `<space>.tws` is never
  consulted, and no `spaces.yaml` is created in it.

In both variants scope detection (§10.2) drops the space-owned first segment, so no feature is
detected and `tws space list` shows every entry of `<root>/spaces.yaml`.

Two boundary cases follow from the same rule and are documented, not special-cased:

- `TWS_ROOT` still wins over the marker (§4.2), so an explicit env override retargets row 9 like
  every other row.
- A sibling repo that is **not** beneath any `.tws-workspace` marker, configured workspace path,
  or `~/tws` is outside this row: `TwsRoot()` falls through to tier 2 and the anchor is that
  repo's own external root. Likewise, a sibling repo that has enabled checkout mode is genuinely
  its own workspace and anchors on `<space>/.tws` via step 2.

Both row-9 variants are asserted by tests (§15.5) so the behaviour is deliberate rather than
accidental, and `README.md` documents the marker-wins rule (§13).

Feature detection affects **`list` only**. `add`, `show`, and `remove` never infer scope: writes
and selectors are explicit, matching the "add is explicit" contract of the global registry.
Detection failure is never an error; it degrades to "show everything".

## 12. Lifecycle interactions (settles B4, B5, B6)

The roadmap's optional feature-hub layout is preserved: spaces may live inside a feature
directory. The non-ownership guarantee is preserved by **refusing** feature-level destruction or
renaming that would touch a registered space, never by deleting or forgetting one.

### 12.0 Inclusive containment

```go
// containsPath is the lexical fast path: target is dir itself or a descendant,
// both canonicalized before comparison.
func containsPath(dir, target string) bool

// pathContains is what every destructive check uses. It starts from
// containsPath and then walks target's ancestor chain, comparing each ancestor
// with dir by os.SameFile where both paths exist.
func pathContains(dir, target string) bool
```

`containsPath(d, t)` is true when `t == d` or `filepath.Rel(d, t)` yields a path that is neither
`..` nor prefixed with `../`. Containment is therefore **inclusive**: a registered target that
*is* the feature directory blocks exactly like one nested beneath it.

`pathContains` adds the identity layer, so containment also holds when the same directory is
spelled differently — a different letter case on a case-insensitive volume, a symlinked
spelling, or an absolute stored path inside the root. A destructive operation therefore never
misses a registered target because of spelling.

### 12.1 `tws rename feature <old> <new>`

`renameFeatureCmd()` (`internal/cli/rename.go:23`) runs, in order, all rooted at
`root := ws.MetadataRoot` — the root whose feature directory it renames:

1. `oldPath, err := ws.ResolveFeaturePath(old)` (unguarded generic resolver, §7.3);
   `newPath := ws.FeaturePath(new)`; existing `os.Stat` checks (old exists, new does not).
2. `tx, err := internal.BeginSpacesFeatureRename(root, old, new, oldPath, newPath)`:
   - **If `<root>/spaces.yaml` does not exist** (`os.Lstat` probe taken **before** any lock
     attempt, §8.5): return a true no-op transaction. No lock is acquired, no lock file is
     created, no `spaces.yaml` is created, no root directory is created, and `Commit()`/`Release()`
     do nothing (§8.5).
   - Otherwise: **acquire the lock first**, then re-read the file **under the lock**. The
     pre-lock existence probe is advisory only — if the file disappeared in between, the
     transaction degrades to the no-op form and releases the lock. No invariant is claimed before
     acquisition (§9).
   - Under the lock, and against the re-read file only:
     a. `GuardFeatureName` semantics for **`old`** — refuse when the source name is a registered
        space's directory, with the §7.3 message. Checked first, because that is the destructive
        side.
     b. `GuardFeatureName` semantics for **`new`** — refuse a collision with the same message.
     c. Inclusive containment (§12.0): any entry that lives inside `oldPath` but that step (d)
        would **not** relocate — every absolute entry, and any relative entry whose first segment
        is not the literal `<old>` — aborts the transaction before anything is renamed:
        ```
        cannot rename feature "acme": 1 registered space is pinned inside <oldPath> (tickets (workspace)); run 'tws space remove tickets --workspace', or re-add it with a workspace-relative path under the new name, then retry
        ```
        Absolute paths are deliberately pinned, so `tws` rewrites nothing it was told to pin. This
        is the smallest safe contract: rewritable relative entries are rewritten, everything else
        blocks. Containment uses `pathContains`, so a spelling that differs only by case or
        symlink is still detected. Blocking entries are listed with their scope, and each removal
        command names its scope explicitly.
     d. **Stage** (do not write) the rewrite: `feature: <old>` → `feature: <new>` on every
        matching entry; every **relative** path whose first segment is `<old>` (external /
        checkout legacy) or whose first two segments are `features/<old>` (checkout new layout)
        gets the new prefix; `UpdatedAt = time.Now().UTC()` on each rewritten entry.
   - A file with no matching entry stages an empty rewrite and is a valid, committable no-op.
3. `os.Rename(oldPath, newPath)` — performed by the command while the lock is held.
4. `tx.Commit()` — writes via `saveSpaces` (§8.2) **only if** at least one entry actually changed;
   otherwise it is a no-op and leaves the file byte-identical. On write failure, the command
   attempts `os.Rename(newPath, oldPath)`:
   - rollback succeeds → return the spaces error; feature unchanged on disk and in metadata;
   - rollback fails → return a joined error stating that the feature is now `<new>` on disk while
     `spaces.yaml` still points at `<old>`, and that it must be repaired with
     `tws space remove` / `tws space add`.
5. `tx.Release()` — always deferred, error joined into the command result.

Because the read, both guards, the containment check, the rewrite, and the write all happen under
one lock, no concurrent `space add` can interleave between validation and commit.

### 12.2 `tws delete <feature>`

`tws delete` never rewrites `spaces.yaml` and never deletes a linked target. It uses a
lock-holding *delete transaction* purely to serialize against `space add`:

```go
func BeginSpacesFeatureDelete(root, feature, featurePath string) (*SpacesDeleteTx, error)
```

Both `deleteCheckout` (`internal/cli/delete.go:53`, `root = ws.MetadataRoot`) and
`deleteExternal` (`internal/cli/delete.go:137`, `root = internal.TwsRoot()`) call it **before any
branch validation or filesystem removal**, passing the exact path they are about to remove.

Order inside the transaction:

1. **If `<root>/spaces.yaml` does not exist** (`os.Lstat` probe taken **before** any lock attempt,
   §8.5) — true no-op: no lock acquired, no `.spaces.lock` created, no root directory created, no
   file written. Delete proceeds exactly as it does today.
2. Otherwise acquire the spaces lock, then re-read the file under it (advisory pre-lock probe
   only; a file that vanished degrades to the no-op form and releases the lock).
3. **Top-level name conflict first** — `GuardFeatureName(root, feature)` semantics evaluated
   against the re-read file. A hit returns the standard §7.3 conflict message, so
   `tws delete learning` on a registered space reports "it is the top-level directory of
   registered space …" rather than a nested-containment message. Exit 1; nothing deleted.
4. **Then inclusive nested-target protection** (§12.0) — refuse when **any** registered entry, of
   either scope and either stored form, satisfies `pathContains(featurePath, resolvedTarget)`:
   ```
   cannot delete feature "acme": 2 registered spaces live inside /Users/me/tws/acme.tws/acme (learning (workspace), patching (feature acme)); run 'tws space remove learning --workspace' and 'tws space remove patching --feature acme', or move the directories out of the feature, then retry
   ```
   Blocking names are listed sorted, workspace-wide entries first, each with its scope, and the
   guidance spells out the exact scope-qualified removal command for **each** blocker, so a name
   shared by two scopes is never ambiguous. Exit 1; nothing deleted.
5. On success the transaction returns holding the lock. The command performs its branch
   validation, branch deletion, worktree removal, and `os.RemoveAll(featurePath)` while the lock
   is held, then calls `tx.Release()` (deferred, error joined). **`Release` never rewrites the
   registry.**

Because steps 3–4 run under the same lock that is held through `os.RemoveAll`, a concurrent
`tws space add` cannot register a target inside the feature between validation and deletion.

After a successful delete there is **no** spaces mutation at all:

- no registered target could have been inside the deleted directory, so no linked content is lost;
- feature-scoped entries pointing *outside* the feature survive and are reported with
  `scope_status: feature-missing` in `list`/`show`, with guidance to re-add them workspace-wide
  or remove them. `tws` never silently forgets a user-registered location, and there is no
  post-destruction write that could fail open.

### 12.3 `tws import` and rename-target collisions

- `tws import` / `tws import --from-repo` are covered by the §7.6 guards installed in
  `recreateCheckout` (`ws.MetadataRoot`) and `recreateExternal` (`internal.TwsRoot()`), each
  applied to `export.Feature` before any directory is created.
- `tws rename feature <old> <new>` covers the **target** collision in step 2b of §12.1 and the
  **source** collision in step 2a.
- `tws rename branch` is covered by the `renameBranchCheckout` / `renameBranchExternal` guards of
  §7.6, which protect the enclosing feature name.

### 12.4 `tws migrate-layout`

`migrate-layout` moves checkout feature directories from `<root>/<name>` to
`<root>/features/<name>`. Its candidate discovery is a raw `os.ReadDir(ws.MetadataRoot)` filtered
only by `isReservedDir`, so a registered top-level space such as `<root>/learning` is exactly the
kind of directory it would otherwise pick up and **move**. It is therefore guarded, with
`root := ws.MetadataRoot` — the only root it touches.

**Single feature — `MigrateFeatureLayout(ws, feature)` (`internal/migrate.go:23`):**

1. `validateFeatureName(feature)` (unchanged).
2. `tx, err := internal.BeginSpacesLayoutMigration(ws.MetadataRoot, []LayoutMigrationTarget{{Feature: feature, Path: src}})`
   — inserted here, **before** the source `Lstat`, the destination collision check,
   `os.MkdirAll`, and `os.Rename`. It is the §12.0 lifecycle transaction of this command: the
   `os.Lstat`-before-lock probe of §8.5, then the lock, then the authoritative re-read, then
   `GuardFeatureName` semantics, then inclusive containment. A hit returns the verbatim §7.3
   conflict message (name ownership) or the §12.4 containment message below, and migrates
   nothing. A malformed, symlinked, future-version, or otherwise unreadable `spaces.yaml`
   returns the canonical `cannot verify registered spaces in <root>/spaces.yaml: <err>` (§8.3),
   so `tws migrate-layout <f>` exits 1 and moves nothing. With no `spaces.yaml` the transaction
   is a true no-op: no lock, no `.spaces.lock`, no `spaces.yaml`, no directory (§8.5).
3. Existing behaviour from the source `Lstat` onwards is unchanged, and now runs **while the
   lock is held**, through `os.Rename`. `tx.Release()` is deferred and its error is joined into
   the command result, so a concurrent `tws space add` cannot register a target inside the
   directory between validation and the move.

Because the transaction consults **both** `TopLevel` and `Features` (§7.3), it also refuses a
migration whose destination name is owned by a `features/<name>` space, which the existing
destination-collision check cannot see (the space directory need not exist yet).

**Nested targets — refusal, never a rewrite (this version).** A legacy feature directory that
carries a feature signal is deliberately *not* claimed as a top-level space name by §7.1, so a
registered target nested inside it — `<root>/acme/patching` under the legacy feature `acme` —
passes the name guard. Moving `acme` would silently invalidate that entry. The safe first
version therefore **refuses the migration** instead of rewriting the entry: whenever any
registered target of either scope and either stored form (relative or absolute, direct or
nested, including a filesystem-equivalent spelling) satisfies `pathContains(<legacy dir>,
resolvedTarget)` — the same identity-aware inclusive containment §12.0 uses — the migration is
blocked:

```
cannot migrate legacy feature "acme" to the new checkout layout: 1 registered space lives inside /Users/me/repo/.tws/acme (patching (feature acme)); run 'tws space remove patching --feature acme', then retry; that space can be re-added with 'tws space add' once the migration is done
```

Top-level name ownership is evaluated **first**, so a directly registered `<root>/learning`
still reports the §7.3 conflict message rather than a containment message. Blocking names are
listed sorted, workspace-wide entries first, each with its scope, and the guidance spells out
the exact scope-qualified `tws space remove <name> --workspace` / `--feature <feature>` command
for **each** blocker. `tws` rewrites no registered path during migration and makes no filesystem
change on refusal.

**All features — `MigrateAllFeatures(ws)` (`internal/migrate.go:59`):** the fail-closed preflight
is one `BeginSpacesLayoutMigration` call for the **whole batch**, executed **once**, after the
`ModeCheckout` check and the read-only candidate `ReadDir`, and always before `os.MkdirAll` and
the first `os.Rename`:

1. Candidate discovery (`os.ReadDir` filtered by `isReservedDir`) reads only; it creates and
   moves nothing, so it may precede the transaction.
2. `tx, err := BeginSpacesLayoutMigration(ws.MetadataRoot, targets)` with one
   `LayoutMigrationTarget` per candidate. On error, append the message to `result.Errors` and
   **return immediately** — no `features/` directory is created and nothing is moved.
   `internal/cli/migrate.go:49-53` already turns a non-empty `result.Errors` into
   `migration failed with N error(s)` and a nonzero exit, so malformed or future-version spaces
   metadata, a registered candidate name, and a nested registered target all make
   `migrate-layout --all` fail loudly.
3. A registered candidate is an **error**, not a skip: it is never added to `Skipped`, never
   added to `migrations`, and never moved. One blocked feature aborts **every** candidate, so
   `--all` is all-or-nothing with respect to spaces conflicts. The refusal names every blocking
   entry across the batch, sorted and de-duplicated, with the legacy directories it blocks.
4. The lock returned by the transaction is held across **all** moves and any rollback; the
   pre-existing all-or-nothing rollback is unchanged. `tx.Release()` is deferred and a release
   failure is reported as an additional `result.Errors` entry.
5. With no `spaces.yaml`, the transaction is a true no-op, no candidate is dropped, and `--all`
   behaves byte-for-byte as before (§8.5) — no `.spaces.lock` and no `spaces.yaml` are created.

`migrate-layout`'s `ValidArgsFunction` (`internal/cli/migrate.go:30`) keeps using
`LegacyFeatureNames`, which stays best-effort and yields no candidates when spaces metadata is
untrusted (§7.5, §8.3 completion row).

### 12.5 Nothing else changes

`tws stack`, `tws close`, `tws decide`, `tws decisions`, `tws inject`, `tws push`,
and all worktree creation are untouched beyond the §7.6 guard insertions, the §8.4 listing-API
migration, and the §12.4 migrate-layout guards.

## 13. Skills and documentation

User-facing behaviour changes, so per `docs/engineering-workflow.md` the embedded skills are
updated in the same change:

- `assets/skills/claude/tesseraworkspaces/SKILL.md` — a command-table row for
  `tws space add/list/show/remove` next to the `tws registry ...` row, plus a "Sibling spaces"
  section: run `tws space list --json` to discover learning/ticket/patching/research/docs
  locations; never hard-code a sibling path; an empty result is normal on a fresh clone; `tws`
  owns the location, the linked tool owns the content.
- `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` — one discovery bullet:
  resolve sibling spaces with `tws space list --json --feature <f>` before delegating.
- `assets/skills/copilot/tws.prompt.md` — matching bullet and short code block.
- `README.md` — the four subcommands in the command table, and a "Workspace sibling links"
  section covering `<spaces-root>/spaces.yaml`, the two path forms, the `0600` local-only nature,
  the feature-name protection (including `tws migrate-layout`, which also refuses while a
  registered target still lives inside a legacy feature directory, §12.4), the strict-failure contract of
  §8.3, and the row-9 rule of §11: **inside a sibling space the enclosing `.tws-workspace` marker
  wins, so `tws space list` keeps targeting the parent workspace even when the space is its own
  Git repository.** Any earlier draft wording claiming the sibling repo retargets the command
  must be replaced by this statement.
- `docs/roadmap.md` — move workspace sibling links from "Now / current target" to shipped.
- `docs/engineering-workflow.md` — add it to the shipped-slices list and name the next target.
- `CHANGELOG.md` — one entry under the next patch version, calling out that `tws template sync`
  and `tws hooks install` now report failures on stderr with a nonzero exit (§8.5.1), and that
  `tws migrate-layout` refuses to move a registered space directory, or any legacy feature
  directory that still contains a registered target (§12.4).

Skills are compiled in through `assets/skills/embed.go`; the change ships on rebuild and is
re-installed with `tws init --force`.

## 14. Acceptance criteria

Root and mode (B1)

1. `make build && ./bin/tws space --help` lists exactly `add`, `list`, `show`, `remove`.
2. External workspace, `TWS_ROOT` set, run from the source repo root:
   `tws space add learning "$TWS_ROOT/learning" --kind learning` creates `$TWS_ROOT/spaces.yaml`
   (not `<repo>.tws/spaces.yaml`) with mode `0600`, `version: 1`, and stored `path: learning`.
3. Checkout workspace with `TWS_ROOT` pointed at an unrelated directory:
   `tws space add docs ./notes --kind docs` writes `<repo>/.tws/spaces.yaml`, and `$TWS_ROOT` is
   untouched.
4. **Divergence test (corrected).** External mode, `TWS_ROOT=<other>` while `<repo>.tws` also
   exists and contains feature `alpha`. After
   `tws space add learning "$TWS_ROOT/learning" --kind learning`:
   - `tws space list --json` from the repo root, from `$TWS_ROOT`, and from a nested dir inside
     `$TWS_ROOT/learning` all read `$TWS_ROOT/spaces.yaml` and return the same single entry;
   - `tws list` scans `<repo>.tws`, still shows `alpha`, prints nothing extra, and its output is
     byte-identical to the pre-feature run — it must **not** read `$TWS_ROOT/spaces.yaml`;
   - `tws add learning` exits 1 with the §7.3 message (its root is `$TWS_ROOT`);
   - **`tws new learning wt` exits 1 with the §7.3 message and creates nothing**, because
     external `tws new` routes through `createWorktree` → `internal.FeaturePath` → `$TWS_ROOT`,
     the very root where `learning` is registered;
   - `tws delete learning` exits 1 with the §7.3 message for the same reason.
5. `tws space remove learning` under the same divergence deletes the entry from
   `$TWS_ROOT/spaces.yaml` and leaves `$TWS_ROOT/learning` on disk.
6. No production symbol named `SpacesAnchor.Legacy` exists; `rg -n 'Legacy\b' internal/spaces.go`
   returns nothing.

Paths and validation

7. `tws space add out /absolute/dir/outside/the/workspace --kind research` stores an absolute
   path; `tws space show out --json` reports `path == resolved_path`.
8. `tws space add tickets /path/to/plain-directory --kind tickets` succeeds for a non-Git directory.
9. `tws space add bad /does/not/exist --kind docs` exits 1 with `does not exist` and creates no file.
10. `tws space add bad /path/to/file.txt --kind docs` exits 1 with `is not a directory`.
11. `tws space add x <workspace-root> --kind docs` exits 1 with `refusing to register the workspace root itself`.
12. `--kind Learning`, `--kind ""`, and a 33-character kind exit 1; an unconventional but
    conforming `--kind ledger` succeeds — `tws` enumerates no closed set.
13. `--description` containing a newline or tab exits 1; a 201-character description exits 1.
14. Re-running an identical `tws space add` prints `already registered:` and exits 0; the same
    name with a different `--kind` exits 1 and leaves the file byte-identical.

Listing, status, and JSON (B7)

15. `tws space list --json` with no `spaces.yaml` prints exactly `[]` and exits 0; no
    `spaces.yaml` and no `.spaces.lock` are created. Under the same empty state,
    `tws space show learning` and `tws space remove learning` each exit **1** —
    `remove` with exactly `no space named "learning"` — and, asserted by a full recursive listing of
    the parent directory before and after, **neither creates the spaces root, a `.tws-workspace`
    marker, `.spaces.lock`, a temp file, nor `spaces.yaml`** (§8.5 Lstat-before-lock). Asserted in
    both external and checkout mode, and with the spaces root itself not yet existing.
16. After `rm -rf` of a registered target, `tws space list --json` reports `"status": "missing"`
    and exits 0; `spaces.yaml` is unchanged.
17. A workspace-wide entry with no description and no rename serializes **without** the
    `description` and `updated_at` keys, and with `scope`, `scope_status`, `status`, and
    `resolved_path` present; `updated_at` never appears as `0001-01-01T00:00:00Z`.
18. `list --json` output parses as an array, uses two-space indentation, and ends with a newline
    (same encoder settings as `tws registry list --json`); no test asserts byte equality with
    registry output.
18a. Human `tws space list` prints `Workspace: <root> (mode: <mode>, scope: <scope>)` before its
    results in **every** case, including both empty states; `scope` is `feature <name>` when
    feature filtering applies (auto-detected or explicit) and `all` otherwise. An empty registry
    says `No spaces registered. …`, while filters that hide every entry of a non-empty registry
    say `No spaces match the active filters (<N> registered). Use 'tws space list --all' to see
    every entry.` `--json` prints no header in either case.
18b. `tws space show <name> --workspace` and `tws space remove <name> --workspace` select the
    workspace-wide entry when a feature-scoped entry shares the name; `--feature` and
    `--workspace` are mutually exclusive; a bare ambiguous name reports
    `disambiguate with --feature <name> or --workspace`; a bare unique name still resolves.
18c. `tws space list --help` documents that a bare list is workspace-wide plus the auto-detected
    feature and that `--all` is the complete view.

Name-to-path protection (B2, B6)

19. With `learning` registered as a top-level relative space in external mode, `tws list` and
    `tws doctor` do not show `learning` as a feature; with `spaces.yaml` absent, `tws list`
    output is byte-identical to the pre-feature output.
20. With `TWS_ROOT` unset (so `TwsRoot()` and `ws.MetadataRoot` name the same root), each of
    `tws add learning`, `tws new learning wt`, `tws archive learning wt`, `tws delete learning`,
    `tws rename feature foo learning`, `tws rename feature learning foo`, `tws sync learning`,
    `tws export learning`, `tws open learning --feature-dir`, `tws open learning --all`, and
    `tws import` of an export whose feature is `learning` exits 1 with the exact §7.3 message and
    changes nothing on disk — asserted in **both** external and checkout mode (skipping the
    external-only forms in checkout mode). `tws open learning --all` and
    `tws open learning --feature-dir` are asserted to fail from the `open` `RunE` guard without
    reaching `openAll` / `openDirect` (§7.6), so neither `tmux` nor the agent is ever invoked.
21. `tws stack learning`, `tws inject learning`, `tws push learning`, `tws decide learning ...`,
    and `tws doctor learning` exit 1 with the same message via the `RequireFeaturePath` guard.
    `tws template sync learning` and `tws hooks install learning` also exit **1** with that exact
    message, printed on **stderr** by `cli.Execute`, via the §8.5.1 `RunE` guard — not via the
    void helpers, which would have printed to stdout and exited 0. Both are asserted to create no
    `inject/` directory, copy no template file, and write no
    `.claude/settings.local.json`. Companion assertions pinning the carve-out boundary, all with a
    conflicting or malformed `spaces.yaml` present unless stated:
    - **malformed metadata**: `tws template sync learning` and `tws hooks install learning` exit 1
      with `cannot verify registered spaces in <root>/spaces.yaml:` on stderr;
    - **unrelated failure preserved**: with **no** `spaces.yaml`, `tws template sync nosuch` and
      `tws hooks install nosuch` still print their `nosuch: <err>` / `  [x] nosuch: <err>` line on
      **stdout** and exit **0**, byte-identical to the pre-feature binary;
    - **workspace-resolution failure preserved**: with `RequireWorkspace()` failing,
      `tws template sync f` still falls through to `syncFeatureTemplate` with the same stdout line
      and exit 0 (only `hooks install`'s pre-existing `RequireWorkspace` row of §8.5.1 changes);
    - **`--all` unchanged**: with a conflicting `spaces.yaml`, `tws template sync --all` and
      `tws hooks install --all` still exit **0**, still print per-feature progress and per-feature
      best-effort error lines to stdout, and never print the §7.3 message, because the registered
      name was already excluded by `ListFeaturesE` and no per-feature guard exists in the loop;
    - **read count**: instrumented, each single-feature run performs at most one additional
      `spaces.yaml` read beyond the `RequireFeaturePath` guard, and each `--all` run performs
      exactly one (the listing's).
22. `Workspace.ResolveFeaturePath` and `Workspace.ResolveFeaturePathOrLegacy` contain no spaces
    call: `rg -n 'Guard|Space' internal/resolve.go` matches only `ListFeaturesResolved`,
    `LegacyFeatureNames`, and comments. A unit test calls `ResolveFeaturePath("learning")` with a
    conflicting `spaces.yaml` present and asserts it still returns the joined path and a `nil`
    error. **Recursion fence (§7.1)**: no read/derive helper in `internal/spaces.go`
    (`readSpaces`, `decodeSpaces`, `validateSpaces`, `ownersFrom`, `isFeatureDir`,
    `SpaceDirOwners`, `GuardFeatureName`, `guardFeatureNameIn`) references
    `ResolveFeaturePath`, `ListFeatures`, `ListFeaturesE`, `ListFeaturesResolved`,
    `LegacyFeatureNames`, `RequireFeaturePath`, `RequireWorkspace`, or `TwsRoot` — the only
    permitted mentions of the last two are inside `ResolveSpacesAnchor` (§4.1), which is called by
    no other spaces helper. A unit test with a `spaces.yaml` whose relative entry names an
    existing feature-signal directory calls `ListFeaturesResolved` and terminates (no unbounded
    recursion, no stack overflow).
23. `tws space add x <existing-feature-dir> --kind docs` exits 1 with the `it is the feature
    directory` message, while `tws space add patching <feature>/patching --kind patching
    --feature <feature>` succeeds (roadmap hub layout) and `<feature>` remains listed as a
    feature by `tws list`.
24. Row 9, both variants, with `TWS_ROOT` unset and the space registered under `<root>`:
    - from `<root>/learning/notes` (a **plain directory**), `tws space list` shows all entries of
      `<root>/spaces.yaml` and does not auto-scope to a phantom `learning` feature; shell
      completion for `tws open <TAB>` does not offer `learning`;
    - from `<root>/tickets` and a nested dir inside it, where `<root>/tickets` is **its own Git
      repository** (`git init`, one commit) still beneath `<root>/.tws-workspace`,
      `tws space list --json` reads `<root>/spaces.yaml` and returns the parent workspace's
      entries — asserted byte-identical to the same command run from `<root>` — and no
      `spaces.yaml`, `.tws`, or `<tickets>.tws` directory is created for the sibling repo.
25. A checkout-mode space stored as `features/scratch` is excluded from the new-layout listing
    branch and blocks `tws add scratch` with the §7.3 message.
26. Instrumented test: one `tws list` over five features performs exactly **one** read of
    `<root>/spaces.yaml`, not five.

Strict failure on untrusted metadata (B3)

27. **The malformed-file fixture set** used by criteria 27–30 is: bad YAML, `version: 0`,
    `version: 99`, an unknown field, a symlinked `spaces.yaml`, and a **directory** named
    `spaces.yaml` (a portable, root-independent way to force a read failure). Permission-based
    fixtures such as mode `0000` are deliberately **not** used: they are no-ops when the test
    process is root and vary across platforms. For every fixture, all four `space` subcommands
    exit 1 with the underlying error and leave the fixture byte-identical.
28. Under the same fixtures, `tws add f`, `tws new f wt`, `tws archive f wt`,
    `tws delete f`, `tws rename feature a b`, `tws sync f`, `tws export f`, `tws open f`,
    `tws migrate-layout f`, `tws migrate-layout --all`, and `tws import` each exit 1 with
    `cannot verify registered spaces in <root>/spaces.yaml:` present in their combined output,
    and create, rename, move, or delete nothing. For `migrate-layout --all` the canonical message
    is the reported `error:` line and the returned error is
    `migration failed with 1 error(s)` (`internal/cli/migrate.go:49-53`, unchanged); this is
    asserted with a legacy feature present, which stays at `<root>/<f>` with no `<root>/features/`
    directory created.
29. Under the same fixtures, `tws list`, `tws doctor`, `tws template sync --all`, and
    `tws hooks install --all` each exit **1**, print **no** feature listing, and print no
    `warning:` line — asserted in both external and checkout mode. `BuildCheckoutList` and
    `buildFeatureEntries` return the error rather than an empty or partial slice, and the
    `continue`-on-resolve-error branches at `internal/checkout_health.go:514` and `:882` are
    never reached.
30. Under the same fixtures, `tws open <TAB>` completion and `tws migrate-layout <TAB>`
    completion produce no candidates, print nothing, and exit 0.
31. With **no** `spaces.yaml`, for every command named in criteria 27–30 and 43–45, plus
    `tws space list`, `tws space show`, and `tws space remove` (§8.5 Lstat-before-lock):
    - **filesystem, absolutely:** no `spaces.yaml`, `.spaces.lock`, temp file, marker, or
      directory is created by any of them (§8.5);
    - **success paths:** stdout, stderr, and exit status are byte-for-byte identical to the
      pre-feature binary, including `tws template sync --all` and `tws hooks install --all` on a
      workspace with features and on one with none (`No features found.`, exit 0), and including
      `tws template sync <feature>` / `tws hooks install <feature>`, whose §8.5.1 guard returns
      `nil` when the registry is absent;
    - **the only permitted differences** are the eight §8.5.1 rows — of which the four marked
      "(new …)" are unreachable without a `spaces.yaml` and therefore cannot fire here — pinned by
      a table-driven test that asserts, for each, the new stream (stderr), the new exit status,
      and that the message text is preserved where §8.5.1 says it is. No other pre-existing
      failure path changes.

Lifecycle (B4, B5, B6)

32. `tws delete acme` exits 1 and deletes nothing when a workspace-wide space and a
    feature-scoped space both resolve inside `<root>/acme`; the message lists both names sorted
    with their scopes and gives a scope-qualified `tws space remove … --workspace` /
    `… --feature <f>` command for each blocker plus the move-the-directory guidance. After
    removing both entries, `tws delete acme` succeeds.
32a. Filesystem identity: with valid hand-edited metadata storing an **absolute** path inside the
    spaces root, the target is still excluded from every feature listing, `tws add` / `tws delete`
    / `tws migrate-layout` still refuse the name, nested containment still blocks `tws delete`,
    and the target's bytes survive. On a case-insensitive volume (probe-and-skip in tests), the
    same holds when the feature name differs from the registered directory only by letter case.
33. Inclusive containment: `tws delete acme` exits 1 when a registered target resolves to exactly
    `<root>/acme` (not merely beneath it). When that entry is a **top-level** relative space
    named `acme`, the message is the §7.3 top-level conflict message, not the nested-containment
    message — proving guard ordering.
34. `tws delete acme` succeeds when the only `acme`-scoped entry points outside the feature; the
    linked directory still exists afterwards, `spaces.yaml` is byte-identical (no post-delete
    rewrite), and `tws space list --json` reports that entry with `"scope_status": "feature-missing"`
    and `"status": "ok"`.
35. With **no** `spaces.yaml`, `tws delete acme` creates no `.spaces.lock` and no `spaces.yaml`,
    and its output is byte-identical to the pre-feature run.
36. Concurrency: a `tws space add` targeting `<root>/acme/notes` launched while `tws delete acme`
    holds the delete lock either blocks until the delete completes and then fails its own
    existence check, or is rejected by the delete guard — never interleaves to leave a registered
    entry pointing into a directory that was concurrently removed.
37. `tws rename feature acme acme2` rewrites `feature:` and the relative `path` prefix of every
    affected entry (including a workspace-wide entry stored as `acme/docs`), sets `updated_at`,
    and leaves absolute entries outside the feature untouched — verified in external mode
    (`acme/...`) and checkout new layout (`features/acme/...`).
38. `tws rename feature acme acme2` exits 1 and renames nothing when an **absolute** entry
    resolves inside `<root>/acme` (inclusive containment), naming that entry and the re-add
    guidance.
39. `tws rename feature learning foo` exits 1 with the §7.3 message (source is a registered
    space); `tws rename feature foo learning` exits 1 with the same message (target collides).
40. Rename with no matching entry commits **no** write: `spaces.yaml` is byte-identical after a
    successful rename, and its mtime is unchanged.
41. With **no** `spaces.yaml`, `tws rename feature acme acme2` acquires no lock, creates no
    `.spaces.lock` and no `spaces.yaml`, and behaves byte-for-byte as before this feature.
42. Rename rollback: with the write forced to fail deterministically after the transaction begins
    — via the exported test-only `internal.SpacesSaveHook` (§15.1), **not** a permission fixture,
    which criterion 27 bans and which `saveSpaces`'s temp-file-plus-rename shape would ignore
    anyway — `tws rename feature acme acme2` exits 1, the directory is back at `<root>/acme`, and
    `spaces.yaml` is byte-identical. The hook is cleared with `t.Cleanup`, exactly as
    `internal.StepHook` is in `internal/cli/checkout_sync_test.go:83-86`.

Checkout layout migration (§12.4)

43. Checkout mode, legacy layout, `learning` registered as a top-level relative space:
    `tws migrate-layout learning` exits 1 with the exact §7.3 message, `<root>/learning` is
    unmoved, and no `<root>/features/learning` and no `<root>/features/` directory is created.
    An unrelated legacy feature still migrates normally with `tws migrate-layout <other>`.
44. Same fixture, `tws migrate-layout --all` with legacy features `alpha`, `beta`, and the
    registered space `learning`: the command exits **nonzero**, reports the §7.3 message as an
    `error:` line (the returned error being `migration failed with 1 error(s)`), and moves
    **nothing** — `alpha` and `beta` are still at `<root>/alpha` and `<root>/beta`, `learning` is
    untouched, `learning` never appears in `Skipped`, and no `<root>/features/` directory is
    created. After `tws space remove learning`, a rerun migrates `alpha`, `beta`, **and**
    `learning` — once the registration is gone the directory is an ordinary legacy candidate,
    which pins the rule that protection tracks the registry, not the directory.
45. With legacy feature directory `<root>/scratch` present and a checkout space stored as
    `features/scratch`, `tws migrate-layout scratch` exits 1 with the §7.3 message and moves
    nothing, even though `<root>/features/scratch` does not exist yet — proving the guard
    consults `owners.Features` and not just the destination-collision check. `--all` fails the
    same way for the same candidate.
45a. **Nested target refusal (§12.4).** Checkout mode, legacy feature directory `<root>/acme`
    carrying a feature signal, with `patching` registered `--feature acme` at
    `<root>/acme/patching` — a target the §7.1 feature-hub exception deliberately leaves
    unclaimed as a name. Both `tws migrate-layout acme` and `tws migrate-layout --all` exit 1
    with the §12.4 containment message naming `patching (feature acme)` and the exact command
    `'tws space remove patching --feature acme'`; `<root>/acme` and the registered bytes are
    unmoved, a second legacy candidate is not moved either, no `<root>/features/` directory is
    created, and `tws space list --json` still reports the entry with `status: ok`. Running the
    printed command verbatim, then rerunning the migration, succeeds and moves the whole feature
    including the former target; afterwards `GuardFeatureName(<root>, "acme")` returns `nil` and
    `tws space add patching <root>/features/acme/patching --kind patching --feature acme`
    re-registers the link under the new layout.
45b. Same shape with a hand-edited **absolute** in-root entry at `<root>/acme/tickets`: both the
    single and the `--all` migration are refused with `'tws space remove tickets --workspace'`,
    proving containment is stored-form independent, and nothing moves.

Concurrency and gates

46. `tws space remove learning` deletes only the registry line; the target directory still exists.
47. Two concurrent `tws space add` invocations against the same root both succeed and both
    entries are present afterwards (no lost update).
48. All gates pass:
    ```bash
    gofmt -l internal assets
    go test ./... -count=1
    go vet ./...
    golangci-lint run ./...
    make build
    ```
49. Focused suites pass:
    ```bash
    go test ./internal -run 'TestSpaces|TestListFeatures|TestResolveFeaturePath|TestMigrate' -count=1 -v
    go test ./internal/cli -run 'TestSpace' -count=1 -v
    go test ./internal/cli -run 'TestExternalFeatureDirectoryCommandMatrix' -count=1
    go test ./internal/cli -run 'TestCheckout|TestMigrateLayout' -count=1
    ```
50. CLI smoke sequence in a scratch external workspace exits 0 at every step:
    ```bash
    make build
    ./bin/tws space list
    ./bin/tws space add learning ./learning --kind learning --description "notes"
    ./bin/tws space list --json | jq -e 'length == 1 and .[0].status == "ok"'
    ./bin/tws space show learning
    ./bin/tws space remove learning
    ./bin/tws space list --json | jq -e 'length == 0'
    ```

## 15. Implementation plan

### 15.1 New — `internal/spaces.go`

Constants `spacesVersion = 1`, `spacesFileName = "spaces.yaml"`, `spacesLockName = ".spaces.lock"`.
Types `SpacesAnchor`, `SpacesFile`, `SpaceEntry`, `SpaceStatus`, `SpaceScope`,
`SpaceScopeStatus`, `SpaceView`, `SpaceOwners`, `SpacesLock`, `SpacesRenameTx`,
`SpacesDeleteTx`, `ErrSpaceNameConflict`.

Every function that touches the file takes the root explicitly:

- `ResolveSpacesAnchor() (SpacesAnchor, error)` — §4.1.
- `spacesPath(root) string`, `spacesLockPath(root) string`.
- `acquireSpacesLock(root) (*SpacesLock, error)` / `(*SpacesLock) Release() error`.
- `readSpaces(root) (*SpacesFile, error)`, `decodeSpaces(data, path)`, `validateSpaces(f, path)`,
  `saveSpaces(root, f)`.
- `SpaceDirOwners(root) (SpaceOwners, error)`, `ownersFrom(root, f) SpaceOwners`,
  `isFeatureDir(dir) bool` — §7.1.
- `GuardFeatureName(root, feature) error` and its file-scoped form
  `guardFeatureNameIn(owners, root, feature) error` used by the locked transactions — §7.3.
- `BeginSpacesFeatureDelete(root, feature, featurePath) (*SpacesDeleteTx, error)`,
  `(*SpacesDeleteTx) Release() error` — §12.2.
- `BeginSpacesFeatureRename(root, old, new, oldPath, newPath) (*SpacesRenameTx, error)`,
  `(*SpacesRenameTx) Commit() error`, `(*SpacesRenameTx) Release() error` — §12.1.
- `BeginSpacesLayoutMigration(root, targets []LayoutMigrationTarget) (*SpacesLayoutMigrationTx, error)`,
  `(*SpacesLayoutMigrationTx) Release() error` — §12.4. Like the delete transaction it never
  rewrites the registry; it validates a whole batch of legacy feature directories and returns
  holding the lock. Deliberately its own type and message rather than a reuse of the
  delete-specific text.
- `containsPath(dir, target) bool` and `pathContains(dir, target) bool` — §12.0.
- `AnchorFeaturePath(anchor, owners, feature) (string, error)` — §7.4; takes the already-loaded
  owners and performs no file read.
- `normalizeSpacePath(anchor, input) (stored, resolved string, err error)` — §6.6.
- `SpaceStatusOf(resolved) SpaceStatus`, `spaceScopeStatus(anchor, feature) SpaceScopeStatus`.
- `SpaceAdd`, `SpaceList`, `SpaceShow`, `SpaceRemove` — each takes `SpacesAnchor`.
- `sortSpaces(f)` — `(feature, name)`, workspace-wide first.

Two test-only failure/instrumentation hooks, both `nil` in production and both cleared with
`t.Cleanup` in every test that sets them. Visibility follows the smallest clean rule — exported
only when a test outside package `internal` must set it:

- `var SpacesSaveHook func(root string) error` — **exported**, consulted at the top of
  `saveSpaces`. It is the deterministic write-failure injector for criterion 42, whose assertion
  is a `tws rename feature` run in package `internal/cli` (the rollback `os.Rename` lives in
  `internal/cli/rename.go`, not in the transaction), so an unexported hook could not reach it.
  Precedent and doc-comment style: `internal.StepHook` (`internal/checkout_sync.go:299-303`),
  which is likewise exported solely so `internal/cli` tests can inject failures.
- `var spacesReadHook func(path string)` — **unexported**, called at the top of `readSpaces`. It
  counts reads for criterion 26 and the criterion-21 read-count assertion, both of which are
  asserted entirely inside `internal/spaces_test.go` against `ListFeaturesResolved` and
  `GuardFeatureName`; nothing in `internal/cli` needs it, so it stays package-private.

### 15.2 Changed — `internal/resolve.go`, `internal/paths.go`

- Add `"spaces.yaml"` to `reservedDirs`.
- **No change to `ResolveFeaturePath` or `ResolveFeaturePathOrLegacy`** beyond comments stating
  that they are deliberately guard-free (§7.3).
- `ListFeaturesResolved` keeps its `([]string, error)` signature and calls `SpaceDirOwners(w.MetadataRoot)`
  **once**, applying `TopLevel` to the external and checkout-legacy branches and `Features` to the
  checkout new-layout branch, and returning the error unchanged.
- `LegacyFeatureNames` applies `TopLevel` best-effort (no error channel; §7.5).
- Add `GuardFeatureName` to `RequireFeaturePath` with `ws.MetadataRoot` (§7.6).
- Split `ListFeatures` into `ListFeaturesE() ([]string, error)` (exclusion applied in both
  branches with the root actually read) and the completion-only `ListFeatures() []string`
  wrapper (§8.4).
- `DetectFeatureFromCwd` and `DetectFeatureFromCwdE` are unchanged; add a short comment recording
  why (§7.5).

### 15.3 New — `internal/cli/space.go`

`spaceCmd()` with `spaceAddCmd()`, `spaceListCmd()`, `spaceShowCmd()`, `spaceRemoveCmd()`. All
`RunE`, all output via `cmd.OutOrStdout()`, `--json` on `list` and `show`, `ValidArgsFunction`
completion for names/kinds/features. Each resolves the anchor once, reads the file once outside
the lock (plus the authoritative locked read in the mutators, §10),
and passes both down.

### 15.4 Changed — CLI wiring, lifecycle, and `internal/migrate.go`

- `internal/cli/root.go` — add `spaceCmd()` to `rootCmd.AddCommand(...)`.
- Install the §7.6 guards, each with its stated root: `add.go:60` and `:112`, `new.go:60` and
  `:159`, `archive.go:46` and `:82`, `rename.go:102` and `:170`, `sync.go:38` (the `sync` `RunE`
  external branch — **not** `syncFeature`, which has no error channel and would lose the message),
  `importcmd.go:192` and `:252`, `open.go:56`, `:75` (before `openAll`), `:85` (before
  `openDirect`), `:101`, `open_checkout.go:19`, `export.go:45`. `openAll` and `openDirect` keep
  their signatures; the guard runs in the `open` `RunE` above them.
- `internal/cli/delete.go` — both `deleteCheckout` and `deleteExternal` open the §12.2 delete
  transaction before any branch or filesystem work and defer `Release`; no post-delete spaces
  mutation.
- `internal/cli/rename.go` — `renameFeatureCmd` runs the §12.1 transaction with rollback.
- `internal/cli/template.go` and `internal/cli/hooks.go` — migrate `Run:` → `RunE:` and move
  their `--all` listing to `internal.ListFeaturesE()`; their existing `fmt.Println("Error: …")`
  + `return` and `os.Exit(1)` paths become returned errors so they no longer exit 0 on failure.
  In the **single-feature branch only**, call `internal.GuardFeatureName(ws.MetadataRoot, feature)`
  before `syncFeatureTemplate` / `installHooksForFeature` and return its error; `templateSyncCmd`
  resolves `ws` itself and **skips** the guard when `RequireWorkspace()` fails, `hooksInstallCmd`
  reuses the `ws` it already holds. The `--all` loop bodies and both void helpers are otherwise
  untouched. The full before/after list is §8.5.1 and nothing outside it changes.
- `internal/migrate.go` — `MigrateFeatureLayout` opens the §12.4 `BeginSpacesLayoutMigration`
  transaction after `validateFeatureName` and before any filesystem work, and holds its lock
  through `os.Rename`, releasing with `errors.Join` on a named return; `MigrateAllFeatures`
  opens the same transaction **once** for the whole candidate batch after the read-only
  `os.ReadDir` and before `os.MkdirAll`/`os.Rename`, turns a blocked batch into a single
  `result.Errors` entry (never `Skipped`), holds the lock across every move and any rollback,
  and reports a release failure as an extra `result.Errors` entry (§12.4).
  `internal/cli/migrate.go` needs no source change: it already fails on
  `len(result.Errors) > 0`.
- `internal/cli/doctor.go:45` and `internal/cli/open.go:197` — move to `internal.ListFeaturesE()`
  and propagate.
- `internal/cli/list.go`, `internal/cli/open_checkout.go`, `internal/checkout_health.go` — no
  source change required; they already propagate the `ListFeaturesResolved` error.

### 15.5 Tests

New suites:

- `internal/spaces_test.go` — decode/validate/save/normalize units: traversal escape,
  non-canonical absolute path, symlinked-root relativization, unknown field, future version,
  malformed version, symlinked `spaces.yaml`, duplicate `(feature, name)`, duplicate path in a
  scope, `0600` mode, `SpaceDirOwners` feature-directory exception, `features/<x>` owner mapping,
  `containsPath` inclusive equality, `updated_at` omission, the recursion fence and read-count
  assertions via `spacesReadHook` (criteria 22, 26, 21 read-count), and the absent-file no-op
  contract (criteria 15, 31, 35, 41).
- `internal/cli/space_test.go` — real temporary Git repos via `setupGitRepo` /
  `setupGitRepoCheckout`, isolated with `t.Setenv("HOME"|"TWS_ROOT"|"XDG_DATA_HOME", ...)` and
  `t.TempDir()`. Covers both modes, the `TWS_ROOT` divergence matrix (criteria 2–6), human and
  JSON output, exit codes, empty state (including the criterion-15 `show`/`remove` no-create
  assertions with a full before/after directory walk), missing targets, and the concurrency case.
- `internal/cli/space_guard_test.go` — criteria 19–31: every guarded command in both modes, the
  guard-free resolver assertion, the strict-failure matrix over the criterion-27 fixture set, the
  completion best-effort rows, the no-file baseline, the §8.5.1 normalized-error-path table
  (now eight rows), and the criterion-21 `template sync` / `hooks install` single-feature guard
  table with its four carve-out-boundary cases (`--all` still exit 0, unrelated
  `RequireFeaturePath` failure still stdout/exit 0, failing `RequireWorkspace` still stdout/exit 0
  for `template sync`, malformed metadata exit 1 on stderr).
- `internal/cli/space_lifecycle_test.go` — criteria 32–42: delete refusal, guard ordering,
  inclusive containment, lock-held delete, rename rewrite in both layouts, absolute-entry
  refusal, no-write-when-unchanged, and rollback driven by `internal.SpacesSaveHook`
  (set and cleared with `t.Cleanup`, mirroring `clearStepHook` at
  `internal/cli/checkout_sync_test.go:83`).
- `internal/migrate_test.go` (extended) and `internal/cli/space_migrate_test.go` — criteria
  43–45b: single-feature guard, `--all` fail-closed batch preflight (registered space is an
  error, never a skip, and no candidate moves), `features/<x>` owner refusal, the §12.4 nested
  containment refusal in both stored forms with its executable scope-qualified guidance and the
  post-removal success path, the lock-held single migration, malformed-metadata failure of both
  forms, and the absent-file no-op baseline.
- `internal/cli/space_matrix_test.go` — the nine-row matrix of §11 using
  `internal/cli/external_feature_dir_test.go` as the template and the existing `createWorktree`
  helper for real linked worktrees, including both row-9 variants (space as a plain directory and
  space as its own Git repo beneath the marker, both anchored on the parent workspace) plus the
  marker-less sibling-repo boundary case. Every row also asserts the human header's scope
  annotation and that `--all` is the complete view from that location. The row-9 Git-repo variant
  is asserted twice: once with `TWS_ROOT` set and once with it **unset**
  (`TestSpaceMatrix_SiblingRepoWithoutTwsRootAnchorsOnParentWorkspace`), where the resolved
  anchor must still be the parent root and no `spaces.yaml`, `.spaces.lock`, `.tws`,
  `.tws-workspace`, or `<sibling>.tws` may appear for the sibling repo.
- `internal/cli/space_identity_test.go` — the filesystem-identity suite of §7.1 and §12.0:
  hand-edited absolute in-root entries are excluded from listings and refused by `tws add`,
  `tws delete`, `tws migrate-layout`, and `space list --feature`; nested absolute in-root targets
  block `tws delete` with the scope-qualified guidance; and a probe-and-skip case-insensitivity
  pair proves that a feature name differing only by letter case is refused by `tws add` and
  `tws delete` while the registered target's bytes and `spaces.yaml` stay untouched.

Existing tests that must be updated in the same change:

- `internal/resolve_test.go:133` `TestListFeaturesResolved_Sorted` and `:159`
  `TestListFeaturesResolved_ExcludesReserved` — extend with a space-owned name and with the
  malformed-file error case; the signature is unchanged so the existing assertions stay valid.
- `internal/resolve_test.go:208`, `:223` `TestDetectFeatureFromCwdE_*` — unchanged, but add an
  assertion that the detector is still exclusion-free so the §7.5 decision is pinned.
- `internal/cli/external_feature_dir_test.go:69` — the `doctor` row of
  `TestExternalFeatureDirectoryCommandMatrix` must keep passing after the
  `ListFeaturesE`/guard changes.
- `internal/cli/checkout_doctor_test.go:145` and `internal/checkout_health_test.go:1079` — assert
  that a malformed `spaces.yaml` now surfaces as an error rather than an empty feature list.
- `internal/migrate_test.go:73` `TestMigrateAllFeatures_PreflightCollision` and `:117`
  `TestMigrateAllFeatures_ReservedDirsUntouched` — keep passing unchanged (no `spaces.yaml`
  present), and gain sibling cases for the §12.4 preflight.
- Any test that constructs `templateCmd()` or `hooksCmd()` must account for the `Run:` → `RunE:`
  migration and the §8.5.1 exit-status changes.

Regression assertions: `tws list`, `tws doctor`, `tws stack`, and `tws sync` behave identically
before and after `spaces.yaml` exists in an unrelated root, and phantom features never appear.

### 15.6 Documentation

Per §13, in the same landed commit.
