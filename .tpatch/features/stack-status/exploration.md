# Exploration — stack-status

Implementation map for the approved [`spec.md`](spec.md). **Nothing here renegotiates the contract.**
Where the spec and the current tree disagree, the disagreement is named and the exact implementation
consequence is stated; the contract is never silently changed.

Working tree read at `e11c4e4` (`chore(tpatch): define stack status`). Every Git behaviour below was
re-measured against `git version 2.55.0` on `darwin/arm64`, Go `1.26.5`, `github.com/spf13/cobra
v1.10.2`, in throwaway repositories that have been removed. Every path cited exists now unless
marked **NEW**.

---

## 0. Scope and ground truth

`stack-status` is a **projection** feature. It adds one command, one `internal` builder/formatter
file, three additive fields on an existing struct, and two wrapper rewrites. It computes no
ancestry: `internal/stack_ancestry.go` is byte-unchanged.

Baseline verified before any planning:

| Gate | Result |
|---|---|
| `go test ./... -count=1` **with the host `GIT_CONFIG_*` variables neutralized** | **green** (`internal` 30.8s, `internal/cli` 42.4s) |
| `go test ./... -count=1` invoked ordinarily, inheriting this shell's env | **fails** existing bare-remote tests in `internal/cli` — environment, not code; see R1 |
| `make build` | green, binary reports `v1.2.13-2-ge11c4e4` |
| `git status --short` | clean |

The baseline gate is therefore green, but only under an explicitly neutralized environment. Both
rows above describe the **unmodified** tree; neither is a defect introduced by this feature.

**R1 (environment, not code).** The agent shell injects, into the **test process** environment:

```
GIT_CONFIG_COUNT=2
GIT_CONFIG_KEY_0=safe.bareRepository   GIT_CONFIG_VALUE_0=explicit
GIT_CONFIG_KEY_1=credential.interactive GIT_CONFIG_VALUE_1=never
```

Every fixture helper in this repository builds its env as
`append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", …)`
(`internal/checkout_health_test.go:44-52`, `internal/cli/checkout_lifecycle_test.go:23-31`), and
`GIT_CONFIG_NOSYSTEM` does **not** strip `GIT_CONFIG_COUNT`. With that env present, **at least** the
bare-remote failures reproduced here occur in existing `internal/cli` tests — the exact set is not
pinned, because it depends on which fixtures touch a bare local remote — and they all pass once the
variables are neutralized. Measured directly:

```
$ git -C <bare> rev-parse --git-common-dir
fatal: cannot use bare repository '<bare>' (safe.bareRepository is 'explicit')
$ GIT_CONFIG_COUNT=0 git -C <bare> rev-parse --git-common-dir
.
```

`GIT_CONFIG_COUNT=0` alone is sufficient: Git reads only `GIT_CONFIG_KEY_n`/`VALUE_n` for
`n < GIT_CONFIG_COUNT`, so the already-exported `KEY_0`/`VALUE_0`/`KEY_1`/`VALUE_1` become inert
without being unset. Any new fixture that creates a bare remote (AC 21, AC 35 external matrix)
inherits this. See G7 for the **process-level** neutralization this feature's fixtures must apply.

### 0.1 Verified anchor map

Every anchor the spec cites, confirmed present (line numbers are current; prefer the symbol).

| Symbol / block | Location (verified) | Role here |
|---|---|---|
| `stackCmd` | `internal/cli/stack.go:10-41` — `Use` :12, `Args: cobra.ExactArgs(1)` :14, `ValidArgsFunction` :15-20, `RunE` :21-39 | +1 `AddCommand`, completion dedup only |
| legacy tree writer | `internal/cli/stack.go:34` (`fmt.Printf` cycle warning), `:37` `internal.PrintTree` | bare `fmt` → process `os.Stdout`; unchanged |
| root registration | `internal/cli/root.go:28` | unchanged |
| root error printer | `internal/cli/root.go:52-56` | prints the error a **second** time; see F1 |
| `statusCmd` writer/flag precedent | `internal/cli/status.go:79-87`, `:92` | shape copied verbatim |
| `PrintTree` / `TopoSort` / `LoadStack` / `StackPath` | `internal/stack.go:207`, `:138`, `:116`, `:112` | read-only; `LoadStack` untouched |
| `StackEntry` / `GitBranch()` | `internal/stack.go:13-28` | identity contract |
| `StackEdge` | `internal/stack_ancestry.go:147-181` | projected field-for-field |
| `StackRepoResolution` | `internal/stack_ancestry.go:184-189` | single source of `workspace.repository` |
| `FeatureStackEdges` | `internal/stack_ancestry.go:846-861` | called exactly once |
| `ResolveStackAncestryRepo` | `internal/stack_ancestry.go:784-844` | reached only through the above |
| `UnevaluatedStackEdges` | `internal/stack_ancestry.go:731-752` | reached only through `ancestryEdgesFor` |
| `ancestryEdgesFor` | `internal/checkout_health.go:610-616` | reused unchanged (same package) |
| `ancestryWorktreeCandidatePath` | `internal/stack_ancestry.go:767-782` | external path join guard |
| `ancestryDisplayStatus` | `internal/stack_ancestry.go:542-547` | only producer of `unevaluated` |
| `ancestrySanitize` | `internal/stack_ancestry.go:551-571` | human-output rule |
| `ancestryFullSHA` = `^[0-9a-f]{40}$` | `internal/stack_ancestry.go:127` | §12 preconditions **only** |
| `RepoSourceMismatchLabel` | `internal/stack_ancestry.go:108` (sole current use `internal/health.go:92`) | header alternate line |
| `canonicalize` | `internal/workspace.go:205-210` | `EvalSymlinks`, falls back to `Abs`+`Clean`; **unexported** |
| `stableID` | `internal/workspace.go:72-75` | `sha256(canonical path)[:8]` as 16 hex; **unexported** |
| `Workspace.StableID` (exported field) | `internal/workspace.go:50-52`, set at `:273`, `:431`, `:462` | the **only** package-`cli`-reachable form of the stable ID |
| `strPtr` / `intPtr` / `boolPtr` | `internal/agent_status.go:1925-1927` | nullability helpers |
| `WorktreeInventory` / `BuildWorktreeInventory` | `internal/agent_status.go:476-480` / `:485-522` | additively extended |
| inventory production consumers | `internal/agent_status.go:720` (build), `:1449` (`Prunable`) | exactly one: `BuildAgentStatus`. Grepped module-wide: `Prunable` is the **only** legacy inventory map with a production reader; `ByBranch` has **zero** production readers (sole read is `internal/agent_status_test.go:885`). Retaining `ByBranch` unchanged is still mandatory — the approved spec requires it (§11.4 rule 4 / AC 26) and the shipped test asserts it |
| `gitDirty` / `gitActiveOp` | `internal/checkout_health.go:352-358` / `:360-390` | become wrappers |
| `healthCurrentBranch` | `internal/checkout_health.go:335-350` | **not** changed, **not** used here |
| `buildHeader` | `internal/checkout_health.go:313-333` (`:327`, `:330`) | only route from this feature to `tws doctor` |
| `checkoutFeatureDetailLines` | `internal/checkout_health.go:884-906` | `base-record=` gating mirrored |
| `[ref-missing]` gating | `internal/checkout_health.go:838-841` | see F4 |
| `e.RefExists = edge.RefExists` (plain bool) | `internal/checkout_health.go:629` | see F4 |
| `feature not found: %s` | `internal/cli/delete.go:70`,`:164`, `rename.go:47`, `export.go:55`, `open.go:101`, `internal/checkout_health.go:232`, `internal/agent_status.go:1790` | reused verbatim |
| `RequireWorkspace` | `internal/workspace.go:440-461` | `I` class |
| `ResolveCurrentWorkspaceE` | `internal/workspace.go:401-433` | `MetadataRoot` producer |
| `resolveExternalRoot` (configured root returned **verbatim**) | `internal/workspace.go:281-291` | §6.2 rule 3 / AC 14a |
| `Workspace.ResolveFeaturePath` + `ErrAmbiguousFeature` | `internal/resolve.go:43-70`, `:59` | fatal ambiguity |
| `GuardFeatureName` | `internal/spaces.go:692` | fatal space-name guard |
| `ListFeaturesE` / `ListFeatures` | `internal/paths.go:145-172` / `:173-176` | `stackStatusArgs` / completion |
| `LoadConfig` (no error) | `internal/config.go:60-113`; `repoConfigPath`→`RepoRoot` `:34-40` | one `--show-toplevel` per call |
| `MainRepoRootIn` / `RepoRoot` / `DefaultBranchIn` | `internal/exec.go:26-41` / `:43-49` / `:66-87` | process-budget provenance |
| `gitIsAncestor` | `internal/checkout_sync.go:365` | inside `A`, untouched |
| `IsPrunableWorktree` | `internal/exec.go` (no `-C`) | used by `tws list` only; **not** used here |

---

## 1. Command call graph

### 1.1 Cobra routing — measured, not assumed

A standalone probe reproduced the exact parent/child shape (`stack` with `Args: ExactArgs(1)` +
`RunE`, plus a `status` child with `Args: ExactArgs(1)`) under cobra v1.10.2. Results:

| argv | Outcome | Consequence |
|---|---|---|
| `stack auth` | parent `RunE`, `args=["auth"]` | legacy tree unchanged |
| `stack status auth` | **child** `RunE`, `args=["auth"]` | the new report |
| `stack -- status` | parent `RunE`, `args=["status"]` | escape hatch works; Cobra stops command lookup at `--` |
| `stack status` | **child** `Args` error `accepts 1 arg(s), received 0` + usage | this is where the §4.2 hint lives |
| `stack` | **parent** `Args` error `accepts 1 arg(s), received 0` + usage | parent, not child — the hint must **not** appear here |
| `stack a b` | parent `accepts 1 arg(s), received 2` + usage | §4.5 drift |
| `stack --help` | usage gains `Available Commands: status` and a `tws stack [command]` usage line | §4.5 drift |

`legacyArgs` is **not** in play: it is Cobra's fallback only when `Args == nil`. `stackCmd` sets
`Args: cobra.ExactArgs(1)` explicitly (`internal/cli/stack.go:14`) and `stackStatusCmd` will set
`Args: stackStatusArgs`, so neither command ever reaches `legacyArgs`. Prefix matching is off
(`EnablePrefixMatching` is nowhere in this repo), so only the **exact** token `status` is shadowed at
the parent position; every other feature name still routes to the legacy tree.

### 1.2 Completion aggregation — measured

`__complete stack ""` returned, in order: `status` (Cobra's subcommand contribution), then the
parent `ValidArgsFunction` list `auth`, `status`, `billing`, then `:4`
(`ShellCompDirectiveNoFileComp`). The duplicate `status` is real and is exactly what §4.3 removes.
`__complete stack status ""` returned only the feature list (`auth`, `status`, `billing`) and `:4` —
no subcommand names — so the child needs no filtering and `tws stack status status` stays
discoverable.

Implementation: in `stackCmd().ValidArgsFunction`, filter one exact `"status"` element out of
`internal.ListFeatures()`. Filtering must be exact-match and must not use `strings.Contains`.

### 1.3 Writers and error surfaces — **F1, a real discrepancy to encode**

Measured against the built binary, `tws stack a b`, streams separated:

```
stdout: (empty)
stderr: Error: accepts 1 arg(s), received 2
        Usage:
          tws stack <feature> [flags]
        ...
        accepts 1 arg(s), received 2
```

Three facts the spec does not spell out, all of which the tests must encode:

1. **In production the usage block goes to stderr, not stdout.** `cli.Execute()`
   (`internal/cli/root.go:17-57`) sets no writers, so Cobra's `c.Print*` resolves through
   `OutOrStderr()` → `os.Stderr`. In-process, `cmd.SetOut(&buf)` redirects the usage block **into
   that buffer**. AC 7 ("zero bytes reaching process `os.Stdout`") and AC 9 ("stdout is empty") are
   both satisfiable, but a test that asserts "the out buffer is empty" for an **arity** error will
   fail — the usage block lands there. Use `SetOut`+`SetErr` and assert per-stream.
2. **Every error is printed twice.** Cobra prints `Error: <msg>` (no `SilenceErrors` anywhere), then
   `internal/cli/root.go:53` prints the bare message again. This is pre-existing and must not be
   "fixed" here; a golden of full stderr must expect both lines.
3. **The legacy `stack` `RunE` does not set `SilenceUsage`.** Measured: `tws stack auth` with no
   `stack.yaml` prints `Error: no stack.yaml found for feature: auth`, **a usage block**, and the
   duplicate. §4.5 enumerates the accepted drift for "a parent argument-arity error"; in fact the
   same drift applies to the parent's **runtime** errors too, because the usage block is printed for
   them as well. **Consequence, not a contract change:** AC 1's assertion must stay scoped to the
   error *string* and exit code (which are unchanged), and any stderr-byte golden for the
   missing-`stack.yaml` case must be re-baselined for the new `Available Commands:` section. The
   successful tree output — the thing §4.5 actually pins — is untouched.

### 1.4 `RunE` sequence (`internal/cli/stack_status.go`, **NEW**)

```
cmd.SilenceUsage = true                       // first statement (§4.7)
ws,  err := internal.RequireWorkspace()        // I: 1×--show-toplevel + 1×--git-common-dir
cfg      := internal.LoadConfig()              // I: 1×--show-toplevel  (no error return)
err      := internal.GuardFeatureName(ws.MetadataRoot, feature)
fp,  err := ws.ResolveFeaturePath(feature)     // ErrAmbiguousFeature is fatal
info,err := os.Stat(fp); !info.IsDir() → "feature not found: <feature>"
stack,err:= internal.LoadStackForStatus(fp, feature)   // NEW; classifies + validates
rep, err := internal.BuildStackStatus(ws, cfg, feature, fp, stack)
internal.NormalizeStackStatus(rep)
--json ? json.NewEncoder(cmd.OutOrStdout()).SetIndent("","  ").Encode(rep)
       : fmt.Fprint(cmd.OutOrStdout(), internal.FormatStackStatus(rep))
```

**`stackStatusArgs` never runs `ListFeaturesE` on the measured path.** With one argument it returns
`nil` at step 1 (§4.2), so the zero-arg listing — which itself calls `RequireWorkspace` and would add
Git processes *before* `RunE` — is unreachable whenever a feature is supplied. This is what keeps
the end-to-end budget at exactly `I + A + 2 + C + D` (AC 36 rule 5). A test that measures the budget
must pass exactly one argument.

`LoadConfig()` returns `Config` and no error; the repo-wide convention for `RunE` bodies is verified
at `internal/cli/doctor.go:58`, `internal/cli/new.go:83`, `internal/cli/sync_helpers.go:238`.

### 1.5 Fatal-vs-reportable seam

Everything in the §5 table returns an error from `RunE` **before** anything is written, so
"zero bytes on stdout" is structural rather than asserted: `BuildStackStatus` is the first statement
that can produce output and it runs after every fatal check. `ErrAmbiguousFeature`
(`internal/resolve.go:59`) surfaces verbatim through `ResolveFeaturePath`.

---

## 2. Data and probe call graph

```
BuildStackStatus(ws, cfg, feature, featurePath, stack)
├── edges, res := FeatureStackEdges(ws, cfg, feature, featurePath, stack)   ← class A, exactly once
│   ├── ResolveStackAncestryRepo
│   │   ├── checkout : ancestryRepoCandidate(ws.RepoRoot)      → MainRepoRootIn  (git -C … --git-common-dir)
│   │   └── external : per non-archived, non-cross-repo entry
│   │                  ancestryWorktreeCandidatePath → ancestryRepoCandidate     (git -C … --git-common-dir)
│   │                  + one alternate probe on ws.RepoRoot     (git -C … --git-common-dir)
│   │                  ↓ fallbacks: ws.RepoRoot, then inferExternalRepoRoot
│   └── EvaluateStackAncestry (only when res.RepoDir != "")
│       ├── newAncestryEvaluator → MainRepoRootIn(repoDir)      (git -C … --git-common-dir)
│       ├── per edge: rev-parse --verify --quiet --end-of-options <ref>^{commit}   [cached]
│       │             rev-parse --short <sha>                                       [cached]
│       │             merge-base, gitIsAncestor
│       └── identityNotes → DefaultBranchIn(repoDir)  (rev-parse --abbrev-ref origin/HEAD,
│                                                      then symbolic-ref --short HEAD)
├── edges = ancestryEdgesFor(feature, stack, edges)             ← totality, 0 processes
├── repoDir := res.RepoDir ; if repoDir != "" :
│   ├── BuildBranchRefInventory(repoDir)      → 1 × for-each-ref  (status-added #1)
│   └── BuildWorktreeInventory(repoDir)       → 1 × worktree list (status-added #2)
├── per row (external, state==present) or once (checkout):
│   ├── probeDirty(path)                      → 1 × status --porcelain, GIT_OPTIONAL_LOCKS=0  (D)
│   └── probeActiveGitOp(path)                → 0 processes, filesystem only
└── per eligible row: stackStatusParentCounts → 1 × rev-list --left-right --count            (C)
```

**Budget accounting, verified from source, not assumed:**

| Class | Members (this tree) | Count |
|---|---|---|
| `I` | `RequireWorkspace`→`LoadConfig`→`RepoRoot` (`rev-parse --show-toplevel`); `RequireWorkspace`→`MainRepoRoot` (`rev-parse --git-common-dir`); explicit `LoadConfig` (`rev-parse --show-toplevel`) | **3** on the ordinary path |
| `A` | everything above inside `FeatureStackEdges`, incl. its infrastructure-shaped calls | fixture-dependent |
| status-added | `2 + C + D` | as tabulated |

**F2 — why shape-based classification provably cannot work.** `RequireWorkspace` → `MainRepoRoot()`
→ `MainRepoRootIn(cwd)` emits `git -C <cwd> rev-parse --git-common-dir`
(`internal/exec.go:18-28`), while `ResolveStackAncestryRepo` → `ancestryRepoCandidate(ws.RepoRoot)`
emits `git -C <repoRoot> rev-parse --git-common-dir`. When the command is run from the repository
root — the ordinary case — these two argv are **byte-identical, including the directory argument**.
Likewise `DefaultBranchIn` emits a `-C`-shaped `rev-parse --abbrev-ref origin/HEAD` from inside `A`.
AC 36's control-run provenance rule is therefore mandatory, not stylistic.

**F3 — the evaluator uses `cmd.Dir`, this feature uses `-C`.** `ancestryGit`
(`internal/stack_ancestry.go:213-219`) sets `cmd.Dir` and passes **no** `-C`; `MainRepoRootIn`,
`DefaultBranchIn`, `gitDirty`, `healthCurrentBranch` all use `-C`. §8.1 mandates `-C` for the new
probes, which is consistent with the probe family and with the shim shape table that strips one
optional leading `-C <dir>`. The recorder must capture **both** argv and `pwd -P`, because the
evaluator's own probes are only distinguishable by cwd.

---

## 3. Current-code findings

### F4 — `RefProbed` and `ref_exists`
`internal/checkout_health.go:629` assigns the plain bool `e.RefExists = edge.RefExists`, and the
`[ref-missing]` tag is gated on `AncestryStatus` at `:838-841`, not on `RefProbed`. The spec already
records this **(sharpened)** and models `ref_exists` as `*bool`. `CheckoutFeatureEntry.RefExists` and
its rendering stay untouched. The base-unset contract the projection relies on is pinned by
`internal/stack_ancestry_test.go` (`TestStackAncestry…BaseUnset`, the `if edge.RefProbed` assertion
at ~`:748`) and produced at `internal/stack_ancestry.go:377-380`.

### F5 — `StackEdge.GitBranch` is a **field**, `StackEntry.GitBranch()` is a **method**
`internal/stack_ancestry.go:149` vs `internal/stack.go:22`. §6.3 writes `se.GitBranch()`; inside
`BuildStackStatus` both are available and equal by construction
(`newStackEdge`, `internal/stack_ancestry.go:352`). Prefer `edge.GitBranch` for the row value and
`se.Name` for the logical name; use `se.GitBranch()` only in the §8.5 fallback key so the equality
assertion of AC 25 is meaningful.

### F6 — `heads.merge_base` aliasing
`StackEdge.MergeBase` is `*string` pointing at a local in `classify`
(`internal/stack_ancestry.go:437-439`, `:452-454`). "Copied pointer value" (§6.3) is safe because the
edge slice is local and read-only, but copy the **value** (`v := *edge.MergeBase; ptr = &v`) so a
later mutation of the edge slice can never rewrite an emitted document.

### F7 — worktree porcelain, measured line by line
Real `git worktree list --porcelain` output from a fixture with main + attached + detached + locked
+ prunable worktrees:

```
worktree <S>/repo\nHEAD 8005d5c5…\nbranch refs/heads/main\n\n
worktree <S>/wt-attached\nHEAD a0421e71…\nbranch refs/heads/ahead\n\n
worktree <S>/wt-detached\nHEAD 8005d5c5…\ndetached\n\n
worktree <S>/wt-gone\nHEAD 4d857a75…\nbranch refs/heads/div\nprunable gitdir file points to non-existent location\n\n
worktree <S>/wt-locked\nHEAD 8005d5c5…\nbranch refs/heads/noups\nlocked busy testing\n\n
```

Verified properties:
- output **ends with `\n\n`** (a blank line after the last block), so `strings.Split(out,"\n")`
  yields two trailing empty elements. **The flush must be a no-op for a block in which no line at
  all was seen**; only a block that saw ≥1 line but no `worktree` line is the §9.2 fail-closed
  "block has no `worktree` line". Getting this wrong turns every real repository into an
  unavailable inventory. The current parser survives this by accident (`flush` only acts when
  `curBranch != ""`, `internal/agent_status.go:495-505`).
- a **bare** repository emits exactly `worktree <path>\nbare\n\n` — **no `HEAD` line, no `branch`,
  no `detached`**. So `Head` and `Detached` are both `nil` for that record, which is the real case
  §9.2 names.
- `prunable <reason>` and `locked <reason>` both appear with reasons; a prunable block still carries
  a `branch` line, which is why the legacy parser routes it into `Prunable` and out of `ByBranch`
  (`internal/agent_status.go:498-505`).
- a **SHA-256** repository (`git init --object-format=sha256`, supported by 2.55.0) emits a 64-hex
  `HEAD` and a 64-hex `%(objectname)`. `ancestryFullSHA` would reject both — hence
  `stackStatusObjectID` (§8.3.1).

**Compatibility-preserving rewrite shape** (`internal/agent_status.go`):

```go
type WorktreeRecord struct{ Path string; Head, BranchRef *string; Detached *bool
                            Bare, Locked bool; LockReason *string
                            Prunable bool; PrunableReason *string }

type WorktreeInventory struct {
    Available bool                       // unchanged
    ByBranch  map[string]string          // unchanged: short branch → RAW porcelain path
    Prunable  map[string]bool            // unchanged
    Records   []WorktreeRecord           // NEW, Git order
    ByPath    map[string]WorktreeRecord  // NEW, canonicalize(path) keyed
    Err       error                      // NEW
}
```

Per block keep a `rawPath` local **used only** for `ByBranch`, and set `Record.Path` /
the `ByPath` key to `canonicalize(rawPath)`. On any §9.2 violation return a fresh
`WorktreeInventory{Available:false, Err: …, ByBranch: map[…]{}, Prunable: map[…]{}}` with empty maps
and nil slices, so the existing `b.wt.Available && b.wt.Prunable[…]` guard
(`internal/agent_status.go:1449`) short-circuits exactly as it does for a Git failure today.
Legacy ordering must be preserved: prunable-with-branch goes to `Prunable` and **not** to
`ByBranch`.

### F8 — dirty / active-op probes, measured
- `GIT_OPTIONAL_LOCKS=0` proof: same fixture, `touch` a tracked file, then
  `git status --porcelain` → `.git/index` mtime advanced `1786606557 → 1786606558`; with
  `GIT_OPTIONAL_LOCKS=0` → mtime unchanged, same verdict. This is the AC 34(a) control.
- `git -C <dir> status --porcelain` in a **bare** repository exits **128**
  ("this operation must be run in a work tree") → `probeDirty` errors → `dirty: null`. Good.
- `git -C <dir> status` **walks upward**: a non-repository directory nested inside a repository
  reports the *enclosing* repository's status and exits 0. Both the legacy `gitDirty` and
  `probeDirty` share this. It is safe here because every probed path comes from Git's own porcelain
  (`ByPath`) or from `repository.dir`; it is a fixture trap (see G3).
- linked-worktree `.git` is a **file** containing an **absolute** `gitdir: <common>/worktrees/<name>`
  (relative targets are Git-legal, hence the `filepath.Join(path, after)` + `Clean` branch that
  `gitActiveOp` already implements at `internal/checkout_health.go:365-373`).
- a conflicted rebase inside a linked worktree creates `rebase-merge` in the **per-worktree** gitdir
  (`…/.git/worktrees/<name>/rebase-merge`), **not** in the common dir. Marker resolution through the
  `.git` file is therefore load-bearing, and step 4 of §11.2 (stat the *resolved* dir) is what stops
  a vanished target from fabricating `"none"`. `git status --porcelain` still exits 0 mid-rebase and
  prints `UU <path>`.

Extraction shape (`internal/stack_status.go` **NEW**, wrappers in
`internal/checkout_health.go`):

```go
func probeDirty(path string) (bool, error)      // "" → error, no process
func probeActiveGitOp(path string) (string, error)

func gitDirty(repo string) bool   { d, err := probeDirty(repo); if err != nil { return false }; return d }
func gitActiveOp(repo string) string {
    op, err := probeActiveGitOp(repo)
    if err != nil || op == StackStatusOpNone { return "" }
    return op
}
```

Every case the shipped helpers collapsed still collapses identically: missing `.git`, malformed or
empty `gitdir:`, vanished/non-directory target, and a non-ENOENT marker stat all produced `""`
before (no marker found) and produce `""` now (probe error). `gitDirty("")` previously ran
`git -C "" status` and failed → `false`; now it errors before starting a process → `false`. Callers
(`internal/checkout_health.go:327,330`; `internal/agent_status.go:794-795,1484`) are untouched.

### F9 — branch-ref inventory, measured against real Git
`git for-each-ref --format=%(refname)%00%(objectname)%00%(upstream)%00%(upstream:track)%00%(upstream:trackshort) refs/heads/`
on a repository with a real local bare remote produced exactly the §8.4 table, byte-checked with
`od -c`:

| branch | `%(upstream)` | `track` | `trackshort` | → |
|---|---|---|---|---|
| `main` | `refs/remotes/origin/main` | `` | `=` | equal 0/0 |
| `ahead` | `refs/remotes/origin/ahead` | `[ahead 1]` | `>` | ahead |
| `behindb` | `refs/remotes/origin/behindb` | `[behind 1]` | `<` | behind |
| `div` | `refs/remotes/origin/div` | `[ahead 2, behind 1]` | `<>` | diverged |
| `gonebr` | `refs/remotes/origin/gonebr` | `[gone]` | **`` (empty)** | gone |
| `noups` | `` | `` | `` | none |
| `badremote` (`branch.badremote.remote=nosuchremote`) | `` | `` | `` | **none** |
| `localups` (`branch.<n>.merge=refs/heads/main`, remote `.`) | **`refs/heads/main`** | `[ahead 1]` | `>` | ahead, local-branch upstream |

Records are newline-separated and the output **ends with one trailing `\n`**, so §8.3 rule 1's
"at most one trailing empty record" is exactly right.

### F10 — parent counts, measured
`git rev-list --left-right --count A...B` → `"2\t1\n"`. Unrelated histories: `merge-base` exits 1
(no merge base, `heads.merge_base == null`) while `rev-list --left-right --count` still exits 0 and
returns the two real totals (`1\t2`). AC 20 is satisfiable exactly as written.

### F11 — `LoadStackForStatus` and structural validation
`LoadStack` (`internal/stack.go:116-127`) collapses "missing", "unreadable", and "invalid YAML" into
one opaque error, which is why §5 requires a separate loader. The new loader is
`os.ReadFile(StackPath(featurePath))` + `errors.Is(err, fs.ErrNotExist)` split +
`yaml.Unmarshal` + `validateStackForStatus`, returning the four classified strings of the §5 table.
`LoadStack` itself is **not** touched — it has 29 production (non-test) call sites across
`internal` and `internal/cli` (verified by grep, excluding the definition and `_test.go` files);
the conclusion is unchanged either way: far too many to re-classify safely, so the new loader is
additive.
`validateStackForStatus` stays unexported; its `filepath.IsLocal` rule is the same guard
`ancestryWorktreeCandidatePath` applies (`internal/stack_ancestry.go:769`), and `IsLocal("")` is
already `false`, so the empty-name rule fires first only because it is ordered first.

### F12 — `metadata_root` as held
`resolveExternalRoot` (`internal/workspace.go:281-291`) returns `cfg.Workspaces[<root>]`
**verbatim**, with no canonicalization; the sibling default and the checkout branch are canonical.
`RequireWorkspace`'s no-repository fallback (`internal/workspace.go:455-460`) canonicalizes. So
`metadata_root` may legitimately be non-canonical (AC 14a), while every authoritative join is
canonicalized independently: `ByPath` keys, `canonicalize(ancestryWorktreeCandidatePath(...))`, and
`repository.dir` (already `canonicalize`d by `ancestryRepoCandidate`,
`internal/stack_ancestry.go:754-765`).

### F13 — `tws doctor` and `tws list` write with bare `fmt`
`internal/cli/doctor.go:71,85,87,105,128,130,133,149` and `internal/cli/list.go:24,42,46,54,58,90,96,106,110,122`
all print to process `os.Stdout`; only `tws status` uses `cmd.OutOrStdout()`
(`internal/cli/status.go:82-87`). **Consequence for AC 35:** `doctor.txt` and `list.txt` goldens
must be captured with `captureStdout` (`internal/cli/space_guard_test.go:16-42`), while
`status.txt` / `status.json` are captured with `cmd.SetOut`. A single capture strategy will silently
produce empty goldens for two of the four surfaces.

### F14 — `tws status` vs stack status external materialization
`BuildAgentStatus` decides external materialization by `os.Stat` on the expected directory
(`internal/agent_status.go:1434-1441`) and only then probes the branch; a stray directory Git does
not know about is `present` there. Stack status decides from `ByPath`, so the same fixture is
`missing` here. This is §9.3's documented divergence and AC 30 — confirmed reachable in the current
code, no change to `tws status` required.

### F15 — determinism sources
`TopoSort` seeds from a map (`internal/stack.go:150-155`) and `PrintTree` iterates a `bases` map
(`internal/stack.go:262-268`); neither may reach this report. `inferExternalRepoRoot` iterates
`cfg.Workspaces` but sorts before reporting ambiguity (`internal/workspace.go:385-392`).
`ResolveStackAncestryRepo`'s external candidate loop walks `stack.Branches` in slice order. So the
only nondeterminism risk is a map used for iteration in the new code — §6.6 forbids it; use the
`refs/heads/<GitBranch()>` map for lookup only.

---

## 4. Exact file and symbol map

### 4.1 NEW — `internal/stack_status.go` (package `internal`)

Write in this order so the file compiles standalone at every step:

1. consts: `stackStatusSchema = 1`, `stackStatusSanitizeLimit = 120`, `StackStatusOpNone = "none"`,
   materialization states (`present|archived|missing|prunable-missing|cross-repo-unsupported|unknown`),
   upstream states (`none|equal|ahead|behind|diverged|gone`), kinds (`worktree|ref`);
2. `var stackStatusObjectID = regexp.MustCompile("^[0-9a-f]+$")` — unexported, parser-only;
3. report types (20 structs, **no `omitempty` anywhere**, `*string`/`*bool`/`*int` for nullables);
4. `LoadStackForStatus` + `validateStackForStatus`;
5. `probeDirty`, `probeActiveGitOp`;
6. `BranchRefRecord`, `BranchRefInventory`, `parseBranchRefInventory`, `parseUpstreamTracking`,
   `BuildBranchRefInventory`;
7. `stackStatusParentCounts`;
8. `BuildStackStatus`, `NormalizeStackStatus`;
9. `FormatStackStatus` + small unexported render helpers.

All 19 proposed identifiers were grepped across the module: **zero collisions**.

Reused unexported package-`internal` symbols — all verified reachable from this file:
`ancestryEdgesFor`, `ancestryWorktreeCandidatePath`, `ancestryDisplayStatus`, `ancestrySanitize`,
`ancestryFullSHA`, `canonicalize`, `strPtr`, `boolPtr`, `intPtr`, `stackBaseRef` (not needed —
`BaseKind`/`BaseRef` are already on the edge). Exported: `FeatureStackEdges`,
`UnevaluatedStackEdges`, `RepoSourceMismatchLabel`, `StackPath`, `Workspace`, `Config`, `Stack`.
**None of these unexported symbols is reachable from package `cli`** — which is precisely why
`FormatStackStatus` and `NormalizeStackStatus` must be exported from `internal`, and why the
package-`cli` AC 35 normalizer cannot call `stableID`/`canonicalize` either (§5.1.1).

Factoring that avoids duplicated truth:
- one `upstreamFor(edge, se, inv) StackStatusUpstream` that owns the §8.5 key derivation and the
  cross-repo short-circuit, so `display` prefix-stripping exists once;
- one `materializationFor(...)` with two arms (external `ByPath` join, checkout `RefProbed` table),
  so `checked_out_branch` stripping exists once;
- one `refHeadsStrip(ref string) string` used by `checked_out_branch`, `upstream.display`, and
  `workspace.checkout.branch` — all three strip **after** validation;
- summary counters derived by a single pass over the finished `entries[]`, never in parallel with
  row construction, so "the three groups partition `entries`" is true by construction.

### 4.2 NEW — `internal/cli/stack_status.go` (package `cli`)

`stackStatusCmd() *cobra.Command` (Use/Short/Long per §4.1/§4.4, `--json` `BoolVar` mirroring
`internal/cli/status.go:92`, child `ValidArgsFunction` unfiltered) and
`stackStatusArgs(*cobra.Command, []string) error` per §4.2. Both unexported → the test seam is
in-package (`internal/cli/stack_status_test.go`, **NEW**).

### 4.3 CHANGED — `internal/cli/stack.go`

`stackCmd()` becomes `cmd := &cobra.Command{…}; cmd.AddCommand(stackStatusCmd()); return cmd`.
The only other change is the exact-`status` filter inside `ValidArgsFunction` (`:15-20`).
`RunE` (`:21-39`) is byte-identical.

### 4.4 CHANGED — `internal/agent_status.go`

`WorktreeRecord` added next to `WorktreeInventory` (`:476-480`); three fields added;
`BuildWorktreeInventory` (`:485-522`) rewritten per §9.2/F7. **No other function changes**, and
`ancestryFullSHA` must not appear in this file (AC 43 grep).

### 4.5 CHANGED — `internal/checkout_health.go`

`gitDirty` (`:352-358`) and `gitActiveOp` (`:360-390`) become the wrappers of §11.3. Nothing else.

### 4.6 Docs / skills / changelog — exact insertion points (all verified)

| File | Anchor (verified) |
|---|---|
| `README.md` | `:129`, the command-table row for `tws stack <feature>` ("Show dependency tree") → new row directly after it |
| `docs/cheatsheet.md` | `:108` `tws stack auth               # dependency tree for a feature` |
| `docs/configuration.md` | `:122` "`tws stack <feature>` prints the dependency tree:" |
| `docs/roadmap.md` | shipped list "Now" (`:8-33`), P1 bullet "**Stack status**" (`:64`), and the "Current target" sentence (`:35-36`) |
| `docs/engineering-workflow.md` | shipped slices list (`:9-19`) + "Next roadmap feature: **stack status**" paragraph (`:24-27`) |
| `CHANGELOG.md` | `## Unreleased` at `:3` |
| `assets/skills/claude/tesseraworkspaces/SKILL.md` | table row `:31`; new section after the ancestry/agent-status material (`:275-300`, e.g. after `### Agent Work Status`) |
| `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` | view-state block `:20`; workflow step `:83` |
| `assets/skills/copilot/tws.prompt.md` | command list `:22`; workflow `:134` |

`go:embed` constraint verified: `assets/skills/embed.go` embeds the three SKILL/prompt files by
**exact relative path**. They are compiled into the binary, so a doc-only follow-up commit would
ship a stale binary — all nine files land in the same change set. No new embed directive and no new
file under `assets/skills/**` is required.

---

## 5. Test and helper map

### 5.1 Helper visibility — the constraint that decides test placement

| Helper | Location | Package | Reachable from `internal`? | from `cli`? |
|---|---|---|---|---|
| `setupHealthTestRepo`, `gitInTest`, `addStackEntries`, `addFeatureToRepo` | `internal/checkout_health_test.go:40,113,99,84` | `internal` | ✅ | ❌ |
| `setupExternalStatusWorkspace`, `addExternalWorktree`, `buildStatus`, `findEntry` | `internal/agent_status_test.go:44,73,80,89` | `internal` | ✅ | ❌ |
| `captureStdout` | `internal/cli/space_guard_test.go:16-42` | `cli` | ❌ | ✅ |
| `withUnifiedWorkspaceEnv` (pins `HOME`, `XDG_DATA_HOME`, clears `TWS_ROOT`, chdir, asserts `ws.MetadataRoot == TwsRoot()`) | `internal/cli/space_guard_test.go:58-75` | `cli` | ❌ | ✅ |
| `withWorkspaceEnv` (**no** `XDG_DATA_HOME`; sets `TWS_ROOT` to a temp dir) | `internal/cli/new_integration_test.go:152-161` | `cli` | ❌ | ✅ |
| `withIdleTmuxOnPath` (comment `:15-21`, func **`:22-43`**) | `internal/cli/status_test.go` | `cli` | ❌ | ✅ |
| `withoutTmuxOnPath` (real-`git`-preserving PATH shim pattern) | `internal/cli/status_test.go:46-67` | `cli` | ❌ | ✅ |
| `isTransientGitLockPath` `:92`, `collectStableTreePaths` `:118`, `snapshotTreeIgnoringGitLocks` `:150` | `internal/cli/space_test.go` | `cli` | ❌ | ✅ |
| `setupGitRepoCheckout` `:19-50`, `requireWorkspaceForTest` `:53`, `gitInDir` `:63-78` | `internal/cli/checkout_lifecycle_test.go` | `cli` | ❌ | ✅ |
| `setupGitRepo` `:135-150` (real bare remote + `origin/HEAD`), `gitRun`/`gitOutput`, `writeAndCommit` | `internal/cli/new_integration_test.go` | `cli` | ❌ | ✅ |
| `createWorktree` (production) | `internal/cli/new.go:163` | `cli` | ❌ | ✅ |
| `runStatus` (`SetArgs`/`SetOut`/`SetErr`/`Execute`) | `internal/cli/status_test.go:70-79` | `cli` | ❌ | ✅ |
| `generated_at`-delete normalization precedent | `internal/cli/status_test.go:216-241`; builder-level twin `internal/agent_status_test.go:1256-1261` | both | — | — |
| `stableID(canonicalPath) string` | `internal/workspace.go:72-75` | `internal` | ✅ | ❌ **unexported** |
| `canonicalize(path) string` | `internal/workspace.go:205-210` | `internal` | ✅ | ❌ **unexported** |
| `internal.Workspace.StableID` (field) | `internal/workspace.go:50-52` | `internal` (exported field) | ✅ | ✅ |

Spec §17.7's placement rules follow directly and are correct: `TestStackStatus_ReadOnlySnapshots`
and `TestStackStatus_ExistingCommandsUnchanged` **must** live in package `cli`; the AC 35 date-pinned
builders **must** be written in package `cli` on the `setupGitRepoCheckout`/`gitInDir` and
`setupGitRepo`/`createWorktree` patterns.

#### 5.1.1 AC 35 stable-ID normalizer — the visibility fact and the only compiling seam

**The fact.** `stableID` (`internal/workspace.go:72`) and `canonicalize`
(`internal/workspace.go:205`) are **unexported** identifiers in package `internal`.
`TestStackStatus_ExistingCommandsUnchanged` is in package **`cli`** (spec §17.7, AC 35, forced by
`captureStdout`/`withUnifiedWorkspaceEnv`/`withIdleTmuxOnPath`, all package `cli`). Therefore the
package-`cli` normalizer **cannot** call `internal.stableID` or `internal.canonicalize` — such a
reference does not compile. The spec's phrasing "`stableID(canonicalize(repoRoot))`, recomputed at
capture and at assertion from the live fixture root" (§11.4 rule 3) names the **value**, not a call
this test may make. Exporting either helper is forbidden (spec §17.7: reused in place, "neither
copied nor exported"), and re-deriving the digest in the test would duplicate production truth.

**The seam.** `internal.Workspace.StableID` is an **exported field**
(`internal/workspace.go:50-52`), and every constructor already sets it to exactly
`stableID(canon)` — the value derived from the canonical repository root
(`internal/workspace.go:273`, `:431`, `:462`). `internal.RequireWorkspace()` is already exported and
is already what `withUnifiedWorkspaceEnv` calls after chdir-ing into the fixture repository
(`internal/cli/space_guard_test.go:65-72`). So the normalizer takes the value from the resolved
fixture workspace and replaces **only that exact literal string**.

**A generic `[0-9a-f]{16}` regex is forbidden** (spec §11.4 rule 3): it would also rewrite
abbreviated or full commit SHAs, which the date-pinned builders exist to compare **verbatim**.

**Exact implementation sequence** (package `cli`, `internal/cli/stack_status_test.go` **NEW** —
`withUnifiedWorkspaceEnv` itself is **not** modified; it belongs to `workspace-sibling-links`):

```go
// 1. one fixture-environment helper, used identically at capture and at assertion
func stackStatusGoldenEnv(t *testing.T, repo string) internal.Workspace {
    t.Helper()
    t.Setenv("GIT_CONFIG_COUNT", "0")   // G7: process-level, see below
    withIdleTmuxOnPath(t)               // §11.4 rule 2
    _ = withUnifiedWorkspaceEnv(t, repo)  // pins HOME/XDG_DATA_HOME/TWS_ROOT, chdir, asserts roots agree
    ws, err := internal.RequireWorkspace() // same resolution the helper just performed
    if err != nil { t.Fatal(err) }
    if ws.StableID == "" { t.Fatalf("fixture workspace has no stable ID: %+v", ws) }
    return ws
}

// 2. the normalizer closes over that ws — no unexported internal symbol is referenced
func normalizeGolden(t *testing.T, ws internal.Workspace, worktreesRoot, s string) string {
    // paths first, longest-first (spec §11.4 rule 3, unchanged)
    // then, exactly one stable-ID rule:
    s = strings.ReplaceAll(s, ws.StableID, "<STABLE_ID>")
    // JSON: decode, delete generated_at, normalize values, re-encode (unchanged)
    // residual checks (unchanged): no os.TempDir()-rooted path, no ws.StableID occurrence
    return s
}
```

Properties this seam preserves, one for one:

- it compiles from package `cli` (only `internal.RequireWorkspace` and `internal.Workspace.StableID`,
  both exported);
- the replaced token is the **exact** value production derived from the canonical root, so the JSON
  `stable_id`, the human `ID:` lines of `tws status` (`internal/agent_status.go:1970`) and
  `tws doctor` (`internal/checkout_health.go:106,771`), and any embedded occurrence (e.g. a checkout
  session name, `internal/session.go:652`) are all covered by the same literal match;
- no regex, so the SHA-verbatim rule of §11.4 rule 2 is untouched;
- it is recomputed from the live fixture at **both** capture and assertion — the same helper call —
  so a golden can never bake in a run-specific ID;
- the residual assertion (any surviving `ws.StableID` occurrence fails the test) still has a value
  to look for.

For the JSON surface the replacement is applied after decoding — the `stable_id` value is compared
and replaced as a decoded string — and for the human surfaces by literal string match, exactly as
§11.4 rule 3 requires.

#### 5.1.2 Process-wide host Git config neutralization (R1 / G7 / AC 35 + legacy equivalence)

**The fact that makes a builder-local fix insufficient.** Production Git calls do **not** set
`cmd.Env`: `MainRepoRootIn` (`internal/exec.go:26-28`), `DefaultBranchIn` (`:66-87`), `gitDirty`
(`internal/checkout_health.go:352-358`), `BuildWorktreeInventory`
(`internal/agent_status.go:489`) and the ancestry evaluator (`internal/stack_ancestry.go:213-219`)
all use `exec.Command("git", …)` with a nil `Env`, which makes the child inherit the **test
process** environment verbatim. Appending `GIT_CONFIG_COUNT=0` to a fixture *builder*'s
`cmd.Env` (the `append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", …)` pattern) therefore hardens only
the fixture-construction commands and leaves every production probe still running under the host's
injected `safe.bareRepository=explicit`.

**The requirement.** In the shared AC 35 / legacy-equivalence fixture environment — the same helper
that installs `withUnifiedWorkspaceEnv` and `withIdleTmuxOnPath`, applied **identically at
pre-change capture and at post-change assertion** — set the variable at **process level**:

```go
t.Setenv("GIT_CONFIG_COUNT", "0")
// optional, only if a clean env is wanted for readability; not required for correctness:
t.Setenv("GIT_CONFIG_KEY_0", ""); t.Setenv("GIT_CONFIG_VALUE_0", "")
t.Setenv("GIT_CONFIG_KEY_1", ""); t.Setenv("GIT_CONFIG_VALUE_1", "")
```

`t.Setenv` mutates the test process environment, so it reaches, in one move:

- the fixture builders (they read `os.Environ()`);
- every production call that inherits — `MainRepoRootIn`, `BuildWorktreeInventory`, `probeDirty`,
  `BuildBranchRefInventory`, `stackStatusParentCounts`, `DefaultBranchIn`, the ancestry evaluator;
- the **bare** fixture specifically — the bare local remote of `setupGitRepo`
  (`internal/cli/new_integration_test.go:135-150`) and the bare-repository record of F7/§9.2, which
  are exactly the cases `safe.bareRepository=explicit` rejects.

`GIT_CONFIG_COUNT=0` alone is sufficient and is the minimal form (R1 measurement): with the count at
zero Git never reads `GIT_CONFIG_KEY_n`/`VALUE_n`, so the injected pairs go inert without being
unset. `t.Setenv` forbids `t.Parallel()` in these tests — already true for every helper here.

**Why this is load-bearing for goldens, not just for green tests.** The AC 35 goldens are
**permanent committed artifacts**. Captured under the inherited env, a bare-remote or bare-record
fixture would record this host's `safe.bareRepository` failure (`Available:false` inventories,
`dirty: null`, degraded `tws status`/`tws doctor` lines) as the pinned expectation, and CI — where
the variables are absent — would then fail against a golden that encodes a local misconfiguration.
Neutralization must therefore be inside the harness (step 0 below), not an invocation-time
`env -u …` wrapper, which only the developer running the command would apply.

#### 5.1.3 Remaining fixture-determinism note

`setupGitRepoCheckout`/`gitInDir` pin only the four author/committer name/email variables and
`setupGitRepo` pins only `user.name`/`user.email`; **neither pins dates**, so the AC 35 builders must
add `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE` (e.g. `2020-01-01T00:00:00+00:00`, incremented per
commit) — otherwise every golden containing a SHA rots on the next run.

`internal/cli/testdata/` does **not** exist today (verified: no `testdata` directory anywhere in the
tree). This feature creates exactly one: `internal/cli/testdata/existing_commands/**`.

### 5.2 Cobra/completion test seams

- `__complete` is exercised by building a root, `SetArgs([]string{"__complete", "stack", ""})`,
  `SetOut(&buf)`, `Execute()`; the directive arrives as a trailing `:4` line and the
  "Completion ended with directive" note goes to the **err** writer. No existing test in this repo
  does this — it is new but proven above.
- Legacy tree capture: `captureStdout(t, func(){ cmd := stackCmd(); cmd.SetArgs([]string{"auth"});
  _ = cmd.Execute() })`, matching `internal/cli/external_feature_dir_test.go:62`.
- Status capture: `cmd.SetOut(&out); cmd.SetErr(&errOut)` and assert per stream (F1).

### 5.3 Process recorder

Follow `withoutTmuxOnPath` (`internal/cli/status_test.go:46-67`): resolve the **real** `git` with
`exec.LookPath` *before* mutating `PATH`, then install a `sh` shim that appends
`pwd -P`, the full argv, and the relevant env (`GIT_OPTIONAL_LOCKS`) to a record file and `exec`s the
real binary. Truncate the record file immediately before the measured call — fixture helpers resolve
`git` through the same `PATH`. `t.Setenv` forbids `t.Parallel()`.

Proving `A + 2 + C + D` and `I + A + 2 + C + D` without shape ambiguity (F2):
1. build the fixture, resolve `ws`/`cfg`/`featurePath`/`stack`, **reset** the recorder;
2. run `FeatureStackEdges(...)` alone twice; require the two recordings to be equal (read-only
   stability) and take it as `A`;
3. reset; run `BuildStackStatus(...)`; multiset-subtract `A`; assert the surviving `A`-subsequence
   keeps its relative order and the remainder is exactly `{1× for-each-ref with the literal %00
   format, 1× worktree list --porcelain, C× rev-list --left-right --count <40hex>...<40hex>,
   D× status --porcelain whose recorded env contains GIT_OPTIONAL_LOCKS=0}`;
4. reset; run `RequireWorkspace()` + `LoadConfig()` alone → `I`; assert exactly
   2×`rev-parse --show-toplevel` + 1×`rev-parse --git-common-dir`;
5. reset; run the full `RunE` (package `cli`) → assert multiset `= I ⊎ A ⊎ remainder`.

A `status --porcelain` **without** `GIT_OPTIONAL_LOCKS=0` matches no remainder form and therefore
fails the test — which is also the §11.3 guard.

### 5.4 SHA-256 feature detection

`git init --object-format=sha256` works on 2.55.0 here (verified). AC 23a's real-Git half must still
feature-detect in a scratch dir and `t.Skip` with an explicit reason; the parser half is pure bytes
and never skips.

### 5.5 Legacy probe copies (AC 35, package `internal`)

`legacyGitDirty`, `legacyGitActiveOp`, `legacyBuildWorktreeInventory` are verbatim copies of
`internal/checkout_health.go:352-358`, `:360-390`, and `internal/agent_status.go:485-522` at the
parent commit. They live in `internal/stack_status_test.go` (**NEW**). `legacyGitDirty` runs
`git status --porcelain` **without** `GIT_OPTIONAL_LOCKS=0` by definition, so it must never run
inside the §16.7 read-only snapshot test.

These copies inherit the test-process environment exactly as their production twins do, so the
legacy-equivalence fixture must apply the same **process-level** `t.Setenv("GIT_CONFIG_COUNT","0")`
of §5.1.2. Without it, `legacyBuildWorktreeInventory` and the new `BuildWorktreeInventory` would
agree on this host only because *both* are broken on the bare fixture — an equivalence proof with no
content.

---

## 6. Implementation sequence, with compile/test checkpoints

| # | Step | Checkpoint |
|---|---|---|
| 0 | **Before touching production code**, write the AC 35 harness: the shared fixture-environment helper (`t.Setenv("GIT_CONFIG_COUNT","0")` **first**, then `withIdleTmuxOnPath`, then `withUnifiedWorkspaceEnv`, then `internal.RequireWorkspace()` for `ws.StableID` — §5.1.1/§5.1.2), the date-pinned builders, the `ws.StableID`-literal normalizer, then capture `internal/cli/testdata/existing_commands/**` with `TWS_REGEN_EXISTING_GOLDENS=1` | `go test ./internal/cli/... -run TestStackStatus_ExistingCommandsUnchanged -count=1` green against the **unmodified** tree, run with an *ordinary* inherited environment (the harness neutralizes `GIT_CONFIG_*` itself, so no `env -u` wrapper is needed or permitted for this run — that is the proof the goldens do not encode R1) |
| 1 | `internal/stack_status.go`: consts, `stackStatusObjectID`, report types, `LoadStackForStatus`, `validateStackForStatus` | `go build ./...` |
| 2 | probes + wrappers: `probeDirty`, `probeActiveGitOp` in `stack_status.go`; rewrite `gitDirty`/`gitActiveOp` in `checkout_health.go` | `go test ./internal/... -run 'TestCheckoutHealth_|TestGitActiveOp|TestStatus' -count=1` — **no test edits** |
| 3 | `internal/agent_status.go` inventory rewrite | `go test ./internal/... -run 'TestBuildWorktreeInventory|TestAgentStatus|TestStatus' -count=1` — existing `TestBuildWorktreeInventory` (`:876-912`) passes unchanged |
| 4 | branch-ref inventory + `parseUpstreamTracking` + `stackStatusParentCounts` | `go test ./internal/... -run TestStackStatus_ -count=1` |
| 5 | `BuildStackStatus` + `NormalizeStackStatus` (joins, summary) | schema/nullability/order tests |
| 6 | `FormatStackStatus` | human-grammar golden |
| 7 | `internal/cli/stack_status.go` + the two `internal/cli/stack.go` lines | `go test ./internal/cli/... -run 'TestStackStatus_|TestExternalFeatureDir' -count=1` |
| 8 | budget, read-only snapshot, no-fetch shim, AC 35 assertion half | full focused suite |
| 9 | docs + skills + changelog (same commit) | `make build`, smoke |
| 10 | full gates, then Path B execute/record per `.tpatch/steering/local.md` | — |

Steps 1 and 4-7 touch no existing behaviour; steps 2-3 are the only ones that can regress an
existing command, and each has its own regression checkpoint before the next step.

**Collision zones** (files another in-flight feature is likely to touch):
`internal/agent_status.go` (inventory + `strPtr` family), `internal/checkout_health.go` (two
functions only), `internal/cli/stack.go` (two statements), `docs/roadmap.md` and
`docs/engineering-workflow.md` (the "next feature" paragraph both this feature and the following one
rewrite), `CHANGELOG.md` `## Unreleased`.

---

## 7. Dependencies — verdict

`.tpatch/features/stack-status/status.json` now records **eight** edges (seven hard, one soft): the
six that existed at the start of this phase, plus the two registered during it (§7.1). Every one is
justified by real symbol provenance. The six pre-existing edges:

| Parent | Kind | Verified provenance |
|---|---|---|
| `stack-ancestry-doctor` | hard | `internal/stack_ancestry.go` in full: `StackEdge`, `FeatureStackEdges`, `ancestryWorktreeCandidatePath`, `ancestryDisplayStatus`, `ancestrySanitize`, `ancestryFullSHA`, `RepoSourceMismatchLabel`; `ancestryEdgesFor` (`internal/checkout_health.go:610`) |
| `agent-work-status-dashboard` | hard | `WorktreeInventory`/`BuildWorktreeInventory`, `strPtr`/`boolPtr`/`intPtr`, the nullable-probe and `Normalize*`/schema conventions |
| `cobra-migration` | hard | the whole command tree, `RunE`, `SilenceUsage`, `cmd.OutOrStdout()` |
| `fix-missing-completions` | hard | `ValidArgsFunction` + `ShellCompDirectiveNoFileComp` convention that both completion changes extend |
| `archive-worktree` | hard | `StackEntry.Archived` semantics used by external materialization |
| `skill-distribution` | soft | `assets/skills/**` + `assets/skills/embed.go` |

Transitive closure computed from the `.tpatch/features/*/status.json` graph (44 slugs). It already
contains `workspace-sibling-links` (hard) — the provider of `GuardFeatureName`, `ListFeaturesE`,
`captureStdout`, `withUnifiedWorkspaceEnv` (`git log -S`, commit `20e4ac8`) — plus
`branch-name-decoupling`, `checkout-doctor-observability`, `fix-checkout-feature-path-routing`,
`multi-repo-workspaces`, and `workspace-mode-foundation`. **No edge is needed for those.**

### 7.1 Two edges discovered by exploration — **registered during this explore phase**

Both providers were **absent** from the transitive closure above (recomputed over all 44
`.tpatch/features/*/status.json` graphs; neither slug appears anywhere in it, at any depth, via any
kind), and both supply symbols the approved spec makes mandatory and explicitly forbids copying
(§17.7: "reused in place … neither copied nor exported"). A direct edge is therefore required for
each — there is no existing path that could carry the ordering constraint implicitly:

| Parent | Kind | Symbols this feature must use | Provenance |
|---|---|---|---|
| `fix-space-test-git-maintenance-race` | **hard** | `isTransientGitLockPath`, `collectStableTreePaths`, `snapshotTreeIgnoringGitLocks` (`internal/cli/space_test.go:92,118,150`) — mandated by §16.7 and AC 34 | commit `66f1660`; a test artifact of this feature fails to **compile** without them |
| `fix-status-tmux-test-portability` | **hard** | `withIdleTmuxOnPath` (`internal/cli/status_test.go:22-43`) — mandated by §11.4 rule 2 and AC 35 | commit `a322c97`; the AC 35 fixture-environment helper of §5.1.1/§5.1.2 calls it directly |

Both were leaves before this registration (nothing depended on either), both are `applied`, and both
already sit above slugs that are in this closure (`workspace-sibling-links`,
`agent-work-status-dashboard` respectively), so adding them keeps the DAG acyclic and introduces no
ordering conflict — confirmed empirically below.

**Registered now, not deferred.** `.tpatch/steering/local.md` requires that links surfacing during
exploration be registered immediately — "register them immediately with
`tpatch feature deps <slug> add <parent>` … Run `tpatch feature deps --validate-all` to confirm the
DAG is still acyclic and free of dangling refs before moving on." Both edges were added during this
explore phase with the hard kind and the provenance above:

```bash
tpatch feature deps stack-status add fix-space-test-git-maintenance-race   # hard
tpatch feature deps stack-status add fix-status-tmux-test-portability      # hard
tpatch feature deps --validate-all
```

`tpatch feature deps --validate-all` now reports **`DAG: ok (0 violations)`**. Consequently
`.tpatch/features/stack-status/status.json` carries the two additional `hard` entries and is an
**expected metadata change in this explore checkpoint** — the two edges are already present in the
`dependencies` array, alongside the six of §7. No implementation-phase dependency action remains.

No new Go module dependency: `encoding/json`, `errors`, `io/fs`, `os/exec`, `regexp`, `strconv`,
`strings`, `path/filepath`, `unicode`, and `gopkg.in/yaml.v3` are all already imported in
package `internal`.

---

## 8. Risks and guards

| # | Risk | Guard |
|---|---|---|
| G1 | Blank-line/EOF flush turns every real repository's inventory unavailable (F7: output ends `\n\n`) | flush is a no-op for a block with **zero** lines; a block with lines but no `worktree` line is the only fail-closed case. Pin with a real-Git fixture in `TestBuildWorktreeInventory_Additive` |
| G2 | `ByBranch` values silently canonicalized → `tws status` bytes change | keep a per-block `rawPath` local used **only** for `ByBranch`; assert on a fixture whose temp root differs before/after `EvalSymlinks` (AC 26). Note: `ByBranch` currently has **zero** production readers (only `Prunable` is read, `internal/agent_status.go:1449`), so this guard is a compatibility/test requirement carried by the approved spec, not a live-behaviour risk |
| G3 | `git status` walks upward, so a probe on a stray path can report an unrelated repository's dirtiness | only probe `repository.dir` and `ByPath` paths. Fixture trap: an external metadata root created **inside** another Git repository makes AC 30's stray-directory case ambiguous — build fixtures under a `t.TempDir()` that is not nested in a repository |
| G4 | Shape-based argv classification double-counts `I` into `A` (F2: byte-identical argv) | control-run provenance + record `pwd -P` |
| G5 | `metadata_root` re-canonicalized "for consistency" | AC 14a compares byte-for-byte against `ws.MetadataRoot`; never pass it to `canonicalize` |
| G6 | A map used for iteration in summary/rows breaks determinism | fixed struct fields for counters; the `refs/heads/…` map is lookup-only (§6.6) |
| G7 | Host `GIT_CONFIG_COUNT`/`safe.bareRepository` breaks any bare-remote fixture **and can be baked into a permanent golden** (R1) | **process-level** `t.Setenv("GIT_CONFIG_COUNT", "0")` inside the shared AC 35 / legacy-equivalence fixture helper, next to `withUnifiedWorkspaceEnv` and `withIdleTmuxOnPath`, applied identically at capture and at assertion (§5.1.2). A builder-local `cmd.Env` append is **insufficient**: production `MainRepoRootIn`, `BuildWorktreeInventory` and `probeDirty` set no `Env` and inherit the test process. Fixture hardening only — no production change |
| G7a | AC 35 stable-ID normalizer fails to compile, or over-normalizes SHAs | `stableID`/`canonicalize` are unexported in package `internal` and unreachable from package `cli`; use the exported `ws.StableID` from `internal.RequireWorkspace()` and replace that exact literal. No `[0-9a-f]{16}` regex (§5.1.1, spec §11.4 rule 3) |
| G8 | AC 35 goldens captured with the wrong writer for `doctor`/`list` (F13) | `captureStdout` for `doctor.txt`/`list.txt`, `cmd.SetOut` for `status.*` |
| G9 | The `[gone]` row's **empty** trackshort is treated as a parse error | the six accepted pairs include `("[gone]", "")` — verified real output (F9) |
| G10 | `ancestryFullSHA` leaks into an inventory parser and breaks SHA-256 | AC 43 greps; `stackStatusObjectID` is the only inventory matcher |
| G11 | `probeActiveGitOp` fabricates `"none"` for a worktree whose gitdir target vanished | §11.2 step 4 stats the **resolved** dir before any marker; markers really do live in the per-worktree dir (F8) |
| G12 | `merge_base` pointer aliasing (F6) | copy the value, not the pointer |
| G13 | Legacy-tree byte assertions accidentally include the changed usage block (F1.3) | scope AC 1 to the success bytes, the exact error **string**, and the exit code |
| G14 | Test flake from Git background maintenance locks | use `collectStableTreePaths`/`snapshotTreeIgnoringGitLocks` in place; never filter a completed listing |

---

## 9. Validation commands

Focused, progressively widening (each was shape-checked against existing test names):

```bash
# 0. pre-change goldens (unmodified tree) — run with the ORDINARY inherited environment:
#    the harness itself neutralizes GIT_CONFIG_* (§5.1.2), which is what proves the committed
#    goldens cannot encode this host's safe.bareRepository=explicit failure.
TWS_REGEN_EXISTING_GOLDENS=1 go test ./internal/cli/... -run TestStackStatus_ExistingCommandsUnchanged -count=1
# and immediately re-run WITHOUT the regen flag, still with the ordinary environment, to confirm
# the goldens reproduce:
go test ./internal/cli/... -run TestStackStatus_ExistingCommandsUnchanged -count=1

# 1. regression checkpoints after steps 2 and 3
go test ./internal/... -run 'TestCheckoutHealth_|TestCheckoutList_|TestGitActiveOp|TestBuildWorktreeInventory' -count=1
go test ./internal/... -run 'TestAgentStatus|TestStatus' -count=1
go test ./internal/cli/... -run 'TestStatus|TestCheckoutDoctor|TestExternalDoctor' -count=1

# 2. the feature
go test ./internal/... ./internal/cli/... -run TestStackStatus_ -count=1

# 3. full gates
gofmt -l internal cmd
GIT_CONFIG_COUNT=0 go test ./... -count=1     # R1: neutralize the host env for PRE-EXISTING tests
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all            # expected: DAG: ok (0 violations) — already true, §7.1

# 4. boundary greps (§20 AC 43)
! grep -rnE '\b(fetch|ls-remote|update-ref|push)\b' internal/stack_status.go internal/cli/stack_status.go
! grep -rn 'omitempty' internal/stack_status.go
! grep -n 'ancestryFullSHA' internal/agent_status.go
git diff --stat -- internal/stack_ancestry.go     # must be empty

# 5. CLI smoke (real workspace)
./bin/tws stack status <feature>
./bin/tws stack status <feature> --json | jq -e '.schema_version == 1 and (has("stack_state") | not)'
./bin/tws stack <feature>
./bin/tws stack -- status
```

Note: on this host **pre-existing** tests that build a bare local remote need the shell-injected
`GIT_CONFIG_*` variables removed (R1), so the full-suite gate is invoked as
`env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 -u GIT_CONFIG_KEY_1 \
-u GIT_CONFIG_VALUE_1 go test ./... -count=1` (equivalently `GIT_CONFIG_COUNT=0 go test ./...
-count=1`). With that neutralization the baseline suite is **green**; invoked ordinarily, with the
injected env inherited, it fails existing bare-remote tests. This wrapper is a **developer
invocation** convenience for pre-existing tests only — this feature's own AC 35 / legacy-equivalence
tests must not depend on it, because they neutralize the environment inside the harness (§5.1.2,
G7), which is what keeps their goldens host-independent.

---

## 10. Explicit non-changes

Files and behaviours that must show **zero** diff (or only the two named statements):

- `internal/stack_ancestry.go` — byte-unchanged; `ancestryFullSHA` and every classification rule
  keep their 40-hex assumptions.
- `internal/stack.go` — `LoadStack`, `SaveStack`, `TopoSort`, `PrintTree`, `StackPath` untouched;
  the legacy empty-stack line stays `No branches tracked. Use 'ts new <feature> <branch>' to add
  branches.` (the new command's own empty line is the different string of §15.3).
- `internal/cli/stack.go` — only `AddCommand` + the completion filter; `RunE` byte-identical.
- `internal/cli/status.go`, `internal/cli/doctor.go`, `internal/cli/list.go`,
  `internal/cli/root.go` — unchanged.
- `internal/health.go`, `internal/checkout_sync.go`, `internal/session.go`, `internal/resolve.go`,
  `internal/workspace.go`, `internal/paths.go`, `internal/config.go`, `internal/spaces.go`,
  `internal/exec.go` — unchanged.
- `healthCurrentBranch`, `gitRefExists`, `IsPrunableWorktree`, `CheckWorktreeBranch`,
  `buildHeader`, `buildOneFeatureEntry`, `checkoutFeatureDetailLines`, `FormatCheckoutHealth`,
  `FormatCheckoutList`, `BuildAgentStatus`, `FormatAgentStatus`, `NormalizeAgentStatus` — unchanged.
- `agentStatusSchema`, every issue code, severity, message, and exit code — unchanged.
- `internal/agent_status_test.go:876-912` `TestBuildWorktreeInventory` — must pass **verbatim**.
- `internal/checkout_health_test.go` — no edits required.
- No `--fetch` flag, no fetch/ls-remote/push/update-ref/reset/checkout/switch/rebase/gc/prune verb,
  no ref write, no index write, no `stack.yaml` write, no lock.
- No `omitempty`, no `generated_at`, no `stack_state`, no patch-identity surface.

### Minimal file list

**New (4):** `internal/stack_status.go`, `internal/cli/stack_status.go`,
`internal/stack_status_test.go`, `internal/cli/stack_status_test.go`
(+ the generated tree `internal/cli/testdata/existing_commands/**`).

**Modified production (3):** `internal/cli/stack.go` (2 statements),
`internal/agent_status.go` (inventory only), `internal/checkout_health.go` (2 functions).

**Modified tests (1):** `internal/agent_status_test.go` (additive inventory cases).

**Docs / skills (9):** `README.md`, `docs/cheatsheet.md`, `docs/configuration.md`,
`docs/roadmap.md`, `docs/engineering-workflow.md`, `CHANGELOG.md`,
`assets/skills/claude/tesseraworkspaces/SKILL.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
`assets/skills/copilot/tws.prompt.md`.

**Metadata (already changed in this explore checkpoint, not an implementation-phase action):**
`.tpatch/features/stack-status/status.json` — the two `hard` edges of §7.1, registered during this
explore phase with `tpatch feature deps` per `.tpatch/steering/local.md`;
`tpatch feature deps --validate-all` reports `DAG: ok (0 violations)`. No production, test, or doc
file is touched by that change.
