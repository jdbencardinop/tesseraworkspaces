# Exploration — stack-ancestry-doctor

Implementation map for the approved `spec.md`. **Nothing here redesigns the five-state contract**
(`current`, `stale`, `divergent`, `missing`, `cross-repo-unsupported`, plus `Status == ""` for
"not evaluated"). Every path, symbol, and line number below was re-read in the working tree at
`49027ba` and re-verified against `git version 2.55.0`.

Scope of this artifact: where each spec clause lands in real code, in what order, with which tests,
and which spec clauses carry an implementation trap.

---

## 1. Verified anchor map

Every anchor the spec cites, confirmed present at the stated location.

| Symbol / block | Location (verified) | Role in this feature |
|---|---|---|
| `AncestryStatus` + 5 consts | `internal/checkout_health.go:143-152` (consts at 147-151) | unchanged; no sixth value |
| `CheckoutFeatureEntry` | `internal/checkout_health.go:155-168` | additive fields after `Severity` (line 167) |
| `CheckoutHealthReport.HasErrors` | `internal/checkout_health.go:188-208` | unchanged; unreachable from ancestry |
| `BuildCheckoutHealthReport` | `internal/checkout_health.go:253` | ≤1 `LoadConfig()` in body, before line 281 |
| `buildFeatureEntries` | `internal/checkout_health.go:551-586` | new `(ws, cfg)` signature; one `FeatureStackEdges` per feature |
| `buildOneFeatureEntry` | `internal/checkout_health.go:588-670` | line 599 legacy `gitRefExists` probe and classifier body deleted; `e.RefExists = edge.RefExists` (see §6) |
| base-resolution block | `internal/checkout_health.go:609-620` | deleted; moved to `stackBaseRef` |
| cross-repo short-circuit | `internal/checkout_health.go:622-627` | deleted |
| ancestry classification | `internal/checkout_health.go:629-667` | deleted; `return e` at 669 retained |
| `gitRefExists` | `internal/checkout_health.go:672-674` | definition **kept** for agent status; checkout-health/list ancestry callers removed |
| `gitShortSHA` | `internal/checkout_health.go:676-682` | **deleted** |
| `gitFullSHA` | `internal/checkout_health.go:684-690` | **deleted** |
| `gitMergeBase` | `internal/checkout_health.go:692-698` | **deleted** |
| `countIssues` | `internal/checkout_health.go:755-782` | unchanged |
| `FormatCheckoutHealth` feature loop | `internal/checkout_health.go:846-874` | `ancestry=` via display fn, `[ref-missing]` gating (858-860), ≤3 detail lines after 872 |
| indented-guidance precedent | `internal/checkout_health.go:819-821`, `840-842` (`"      %s\n"`) | exact format for the new detail lines |
| `severityIcon` | `internal/checkout_health.go:895-908` | reused by `HealthIssue.String()` (same package) |
| `CheckoutListEntry` | `internal/checkout_health.go:913-921` | **unchanged** |
| `BuildCheckoutList` | `internal/checkout_health.go:924-992` | ≤1 `LoadConfig()`; delegates to `buildCheckoutListEntries(ws, cfg)` |
| list private classifier | `internal/checkout_health.go:959-981` | deleted; replaced by edge lookup |
| `FormatCheckoutList` tag | `internal/checkout_health.go:1036-1038` | render `[unevaluated]` instead of suppressing `""` |
| `HealthIssue` + `String()` | `internal/health.go:11-15`, `17-23` | `Severity` field + `EffectiveSeverity()` + icon rewrite |
| `CheckWorktree*` | `internal/health.go:26,48,65` | **not modified** (zero-value severity rule) |
| `CheckFeatureHealth` | `internal/health.go:105-131` | **signature and body unchanged**; sole caller `internal/cli/doctor.go:84` |
| `doctorCmd` `RequireWorkspace` | `internal/cli/doctor.go:30` | kept single call; `wsErr` stays non-fatal |
| `checkFeatureE` call sites | `internal/cli/doctor.go:40,56` | thread `(ws, cfg)` |
| `checkFeatureE` | `internal/cli/doctor.go:73-105` | new signature + §10.3 body |
| `runCheckoutDoctor` | `internal/cli/doctor.go:107-122` | **unchanged** |
| `runCheckoutList` | `internal/cli/list.go:117-124` | **unchanged** (exported builder signature preserved) |
| `gitIsAncestor` | `internal/checkout_sync.go:365-377` | reused unchanged; already `cmd.Dir`, already exit-1-only |
| `gitResolveRef` (unpeeled writer) | `internal/checkout_sync.go:307-314` | source of unpeeled `LastBaseSHA` |
| checkout `--onto` predicate | `internal/checkout_sync.go:893` | quoted in AC 11 |
| literal-base planning gap | `internal/checkout_sync.go:447-466`, `891-896` | reported as note, never fixed (N4) |
| external `--onto` predicate | `internal/cli/sync_helpers.go:50` | quoted in AC 11 |
| `resolveEntryBase` / `resolveBase` | `internal/cli/sync_helpers.go:179-184`, `186-192` | source of the `origin/<default>` mismatch |
| `GetBranchSHA` (unpeeled writer) | `internal/stack.go:85-95` | second source of unpeeled `LastBaseSHA` |
| `StackEntry` / `GitBranch()` | `internal/stack.go:13-28` | identity contract |
| `GetBranch` first-match order | `internal/stack.go:64-72` | `stackBaseRef` must match this order |
| `LoadStack` / `SaveStack` | `internal/stack.go:116`, `128` | read only; never saved here |
| `MainRepoRootIn` | `internal/exec.go:27-42` | validation + candidate normalisation (`-C` shaped) |
| `DefaultBranchIn` | `internal/exec.go:69-88` | §9.1; up to 2 processes, memoised |
| `canonicalize` | `internal/workspace.go:205-210` | applied to every candidate root |
| `inferExternalRepoRoot` | `internal/workspace.go:339-394` | external candidate 3 |
| `Workspace` fields | `internal/workspace.go:39-56` | `RepoRoot`, `MetadataRoot`, `Mode` |
| `RequireWorkspace` | `internal/workspace.go:440-465` | already calls `LoadConfig`; persistent failure is returned again by `RequireFeaturePath` before ancestry |
| `RequireFeaturePath` | `internal/resolve.go:294-303` | **first** call in `checkFeatureE`, error text unchanged |
| `ListFeaturesResolved` | `internal/resolve.go:139` | fail-closed listing both checkout builders sit inside |
| `gitRefExists` retained callers | `internal/agent_status.go:1384`, `1433` | why the `agent-work-status-dashboard` edge stays soft |
| `LoadConfig` cwd dependency | `internal/config.go:35-40` (`repoConfigPath` → `RepoRoot`) → `internal/exec.go:44-50` | the reason `cfg` is threaded, never re-loaded |

### 1.1 Git facts re-verified for this exploration

Throwaway repo created and deleted under the project directory, `git version 2.55.0`:

| Probe | Observed |
|---|---|
| `rev-parse v1` (annotated) / `rev-parse v1^{commit}` | `9f54cde…` (tag object) / `761be92…` (commit) — different |
| `rev-parse --verify --quiet --end-of-options v1^{commit}` | exit 0, prints the commit |
| `rev-parse --verify --quiet --end-of-options <bogus 40-hex>` | exit **0** |
| `rev-parse --verify --quiet --end-of-options <bogus 40-hex>^{commit}` | exit **1** |
| `merge-base --is-ancestor --end-of-options <unknown-sha> main` | exit **128**, `fatal:` on stderr |
| `merge-base --end-of-options main orph` (unrelated) | exit **1**, empty stdout |
| `merge-base --is-ancestor --end-of-options <main> <orph>` | exit **1** |
| branch+tag both `dup`: `dup^{commit}` vs `refs/heads/dup^{commit}` | tag commit vs branch commit — **different** |
| `rev-parse --short <full-sha>` | 7-char abbreviation |

All §4.1 spec claims hold. No spec fact needed revision.

---

## 2. New file — `internal/stack_ancestry.go`

Package `internal`. **No symbol below collides**: all 29 proposed identifiers
(`EvaluateStackAncestry`, `StackEdge`, `ancestryGit`, `EffectiveSeverity`, `CountHealthIssues`,
`buildCheckoutListEntries`, …) currently return **0 hits** under `internal/`.

### 2.1 File layout (write in this order)

| Block | Contents |
|---|---|
| 1. types | `StackBaseKind`, `StackBaseRecord`, `StackRepoSource`, `StackAncestryReason`, `StackNoteKind` (§5.1) |
| 2. consts | 13 reason constants; 3 base-kind; 3 base-record; 4 repo-source; **exactly 2** note kinds; `RepoSourceMismatchLabel = "repo-source-mismatch"`; `ancestryUnevaluatedToken = "unevaluated"` |
| 3. errors | `var ErrRepoUnavailable = errors.New("stack ancestry: source repository unavailable")` |
| 4. structs | `StackEdgeNote`, `StackEdge` (§5.2), `StackRepoResolution` (§5.3), unexported `refResolution{full, short string; ok bool}`, `ancestryEvaluator` |
| 5. runner | `ancestryGit(repoDir string, args ...string) *exec.Cmd` — `cmd.Dir = repoDir`, `cmd.Stderr = nil`, **no `-C`** |
| 6. primitives | `newAncestryEvaluator`, `resolveCommit`, `abbrev`, `ancestryMergeBase`, `defaultBranchName` |
| 7. classification | `(*ancestryEvaluator) edge(...)`, `stackBaseRef`, `ancestrySeverity`, `identityNotes` |
| 8. presentation | `ancestryGuidance`, `ancestryDisplayStatus`, `ancestrySanitize` |
| 9. API | `EvaluateStackAncestry`, `EvaluateStackEdge`, `UnevaluatedStackEdges` |
| 10. adapter | `ancestryRepoCandidate`, `ResolveStackAncestryRepo`, `FeatureStackEdges` |

Const declarations **must repeat the type on every line**
(`ReasonParentContained StackAncestryReason = "parent-contained"`), because AC 51 counts
`StackNoteKind = "` occurrences and AC 47 counts `AncestryStatus … AncestryStatus =` lines. Implicit
type inheritance inside a `const (…)` block would make both greps under-count. (Verified: the five
existing `AncestryStatus` consts at `internal/checkout_health.go:147-151` already spell the type on
each line, and the AC 47 ERE matches all five today.)

Naming constraint (§14.1): no new symbol may start with `gitMergeBase`/`gitFullSHA`/`gitShortSHA`.
Use `ancestryMergeBase`. AC 46's word-boundary ERE was run against the current tree and correctly
returns the 10 existing hits, so it will return nothing once they are deleted.

### 2.2 Result / nullability mapping (implementable form of §5.2)

| Case | `Status` | `Reason` | `RefProbed` / `RefExists` | `MergeBase` | Heads | `BaseRecord` |
|---|---|---|---|---|---|---|
| cross-repo (`se.Repo != ""`) | `cross-repo-unsupported` | `cross-repo` | false / false | nil | "" | `absent` |
| `se.Base == ""` | `""` | `base-unset` | false / false | nil | "" | `absent` |
| repo unavailable | `""` | `repo-unavailable` | false / false | nil | "" | `absent` |
| child ref unresolved | `missing` | `child-ref-missing` | true / false | nil | "" | as resolved (§4.2 rule 6 not reached ⇒ `absent`) |
| base ref unresolved | `missing` | `base-ref-missing` | true / **true** | nil | `LocalHead*` set, `ParentHead*` empty | `absent` |
| `P ⊆ C` | `current` | `parent-contained` | true / true | `&ParentHead` (no probe) | both | as resolved |
| no merge base | `divergent` | `unrelated-histories` | true / true | **nil** | both | as resolved |
| `L` unresolvable | `stale` | `base-record-unresolvable` | true / true | probe result | both | `unresolvable` |
| `L ⊄ P` | `divergent` | `base-rewritten` | true / true | probe result | both | `present` |
| `L ⊆ P` | `stale` | `parent-advanced` | true / true | probe result | both | `present` |
| `L` absent | `stale` | `parent-advanced-no-base-record` | true / true | probe result | both | `absent` |
| probe exit ∉ {0,1} on `P⊆C` or `merge-base` | `""` | `ancestry-probe-failed` | true / true | nil | both | as resolved |

`MergeBase` is the only pointer. `MergeBaseShort` is set **iff** `MergeBase != nil`. Never write
`"none"`/`"unknown"` into a SHA field.

Order-critical detail: resolve `C` **before** `P` (§4.2 rules 3 then 5). That is what produces the
deliberate delta §16.9 (a `base-ref-missing` edge now prints `head=`).

An earlier, pure ordering rule is equally mandatory: for **every** stack entry, first set
`BaseName = se.Base` and call `stackBaseRef(stack, se)` to set `BaseRef` and `BaseKind`. This happens
before cross-repo, repository-unavailable, base-unset, child-missing, or base-missing returns.
`stackBaseRef` performs zero Git and filesystem work. `UnevaluatedStackEdges` receives the full
`Stack` and applies the same rule, so no short-circuit loses the stack-entry/literal distinction.

### 2.3 Pure formatting helpers

```go
func ancestryDisplayStatus(s AncestryStatus) string   // "" -> ancestryUnevaluatedToken
func ancestryGuidance(e StackEdge) string             // pure; table-driven on e.Reason
func ancestrySanitize(s string, limit int) string     // control/non-printable -> '?', truncate + "…"
func ancestrySeverity(status AncestryStatus, archived bool) CheckoutSeverity
```

- `ancestryGuidance` must be **pure and total over the 13 reasons** so AC 57 can table-test it with
  synthesized `StackEdge` values and no repository. It reads only `e.Feature`, `e.Name`,
  `e.GitBranch`, `e.Archived`, `e.BaseRef`, `e.ParentHeadShort`, `e.LastBaseShort`,
  `e.LastBaseSHA`, `e.Reason`, and a detail string carried on the edge for
  `repo-unavailable` / `ancestry-probe-failed`.
- `<detail>` needs a carrier. `StackEdge` has no `Detail` field in §5.2, so **build guidance at
  construction time** and store the finished string in `Guidance`; `ancestryGuidance(e)` is then
  called with `e.Guidance` already empty and the detail passed via the reason-specific constructor.
  Simplest conforming shape: `ancestryGuidance(e StackEdge, detail string) string`. This is a
  helper-signature refinement, not a contract change; §14.1 lists the helper without a fixed
  signature.
- Sanitisation limits: **40 runes** for `se.Base`, `se.LastBaseSHA`, `se.Repo`, `GitBranch()`,
  entry names; **200 runes** for `<detail>`. Replacement rune `?` for anything failing
  `unicode.IsPrint`. Truncation marker `…`. Guidance must never contain `\n` — assert in AC 54/57.
- `<l>` is `abbrev(LastBaseCommit)` when `BaseRecord == present`, and `%q`-quoted
  `ancestrySanitize(se.LastBaseSHA, 40)` when `unresolvable`.
- Classification always uses the **raw** values; sanitisation is display-only.

### 2.4 Git runner, ref cache, probe functions

```go
type ancestryEvaluator struct {
    repoDir       string
    refs          map[string]refResolution // keyed by the exact ref string passed in
    shorts        map[string]string        // keyed by full SHA
    defaultBranch string
    defaultDone   bool
}
```

| Probe | argv after `git` | Runner | Cache |
|---|---|---|---|
| validation | `-C <repoDir> rev-parse --git-common-dir` | `MainRepoRootIn` | once per evaluation |
| resolve/peel | `rev-parse --verify --quiet --end-of-options <ref>^{commit}` | `ancestryGit` | `refs`, **negatives cached** |
| abbreviate | `rev-parse --short <full-sha>` | `ancestryGit` | `shorts`; fallback `full[:12]` |
| ancestry | `merge-base --is-ancestor <sha> <sha>` | `gitIsAncestor` (unchanged) | not cached |
| merge base | `merge-base <sha> <sha>` | `ancestryGit` | not cached |
| default branch | `rev-parse --abbrev-ref origin/HEAD`, then `symbolic-ref --short HEAD` | `DefaultBranchIn(repoDir)` | memoised (incl. the `"main"` fallback) |

Rules that must be enforced in code, not by convention:

1. `ancestryGit` never receives `-C`. `cmd.Dir` is the validated non-empty `repoDir`. `gitIsAncestor`
   already has the identical shape (`internal/checkout_sync.go:366-367`), which is why it is reusable
   verbatim.
2. `--end-of-options` on **every** command that takes a user-controlled string. `merge-base` and
   `--is-ancestor` receive only 40-hex peeled SHAs, so injection is structurally impossible there.
3. Branch identity is always `refs/heads/<GitBranch()>`; a bare branch name is never probed. Verified
   necessary: with a branch and tag both named `dup`, bare `dup^{commit}` resolves to the *tag*.
4. `cmd.Stderr = nil` + `cmd.Output()` so `warning: refname … is ambiguous` never reaches stdout.
   (`exec.Cmd.Output()` errors if `Stderr != nil`, so the two rules are mutually consistent.)
5. Exit-code discipline:
   - resolve/peel: any non-zero ⇒ unresolved (never an error);
   - `is-ancestor`: 0 ⇒ yes, 1 ⇒ no, **anything else ⇒ error**. `gitIsAncestor` already implements
     exactly this (`internal/checkout_sync.go:369-376`);
   - `merge-base`: 0 ⇒ SHA; 1 **with empty stdout** ⇒ no merge base; anything else ⇒ error. Verified
     exit 128 for an unknown SHA and exit 1 for unrelated histories.
6. No `fetch`/`ls-remote`/`--fork-point`/reflog/`status`/index refresh/`-c`/`--git-dir`/`--work-tree`.
   No probe ever names `se.Repo` or any path outside `repoDir`.

### 2.5 Process bounds

- Core evaluator: `≤ 10·E + 3` (1 validation + ≤2 default-branch + ≤10/edge).
- Cross-repo, `base-unset`, `repo-unavailable` edges: **zero ancestry probes each**.
- Note precisely: a stack of 5 cross-repo entries still incurs the **one** `MainRepoRootIn`
  validation process, which AC 41 classifies as a *non-ancestry invocation shape*
  (`rev-parse --git-common-dir`). AC 43's "zero" is about ancestry probes only. Do not attempt to
  make it literally zero `git` processes — that would break the §6.1 refuse-before-probe contract.

---

## 3. Repository resolution boundaries

`ResolveStackAncestryRepo(ws, cfg, featurePath, stack) StackRepoResolution` is the only
mode-aware function in the feature.

```go
func ancestryRepoCandidate(path string) (string, bool) {
    if path == "" { return "", false }            // hard guarantee: never `git -C ""`
    root, err := MainRepoRootIn(path)             // internal/exec.go:27
    if err != nil { return "", false }
    return canonicalize(root), true               // internal/workspace.go:205
}
```

| Mode selector | Branch |
|---|---|
| `ws.Mode == ModeCheckout` | checkout branch |
| anything else | external branch |

**Checkout**: `ancestryRepoCandidate(ws.RepoRoot)` → `Source = workspace`. No worktree scan, no
inference, and `Alternate` is **never** set (this is what makes AC 51's "checkout output contains
`repo-source-mismatch` zero times" structurally true).

**External**, first candidate that yields a canonical root:

| # | Candidate | Source | Notes |
|---|---|---|---|
| 1 | `filepath.Join(featurePath, "worktrees", e.Name)` for each `e` in `stack.Branches` order with `e.Repo == ""` && `!e.Archived` | `worktree` | validate `e.Name` **before** joining; then use the same evidence `inferExternalRepoRoot` trusts (`internal/workspace.go:370-378`); matches `internal.WorktreePath` layout; **the only source correct under a wrong `TWS_ROOT`** |
| 2 | `ws.RepoRoot` | `workspace` | |
| 3 | `inferExternalRepoRoot(metadataRoot, cfg)` then `canonicalize` — `metadataRoot = ws.MetadataRoot` if non-empty else `filepath.Dir(featurePath)` | `inferred` | only reached when 1 and 2 both fail |
| 4 | none | `unavailable` | `Reason` = candidate-3 error text (it distinguishes "multiple default repositories" from "cannot determine", `internal/workspace.go:392-394`) else `"no source repository for feature <f>"` |

`Alternate`: when candidate 1 wins, candidate 2 is still evaluated **once**; if it canonicalizes to a
**different** main root, store it. Because both sides are `canonicalize(MainRepoRootIn(x))`, an
ordinary linked worktree normalises to the same main root and `Alternate` stays `""` (AC 55). Only
candidate 1 and candidate 2 ever contribute; candidate 3 never does.

Candidate 1 fails closed on a path-hostile `StackEntry.Name`. Before `filepath.Join`, require a
non-empty local relative path (`filepath.IsLocal(e.Name)`) whose clean form is not `"."`; nested
logical names remain allowed when they stay below `worktrees/`. After joining but before Git,
canonicalize both the `worktrees` root and candidate and require the candidate's `filepath.Rel` to
remain local and non-dot, which also rejects a symlink escape. An invalid eligible entry immediately
returns `Source = unavailable` with a sanitized reason; do not fall through to another candidate and
do not call Git. `FeatureStackEdges` therefore produces `repo-unavailable`/no-probe edges (or
propagates a loader-level `stack-invalid` result) and can never inspect a repository reached through
`worktrees/../…` or an escaping symlink.

**`cfg` is threaded, never re-loaded.** `internal/stack_ancestry.go` must contain **zero**
`LoadConfig` references (AC 53). Rationale is concrete, not stylistic: `LoadConfig` →
`repoConfigPath()` (`internal/config.go:35-40`) → `RepoRoot()` (`internal/exec.go:44-50`) runs
`git rev-parse --show-toplevel` **in the process working directory**. Loading it per feature or per
edge would make the number of cwd-dependent Git processes grow with the workspace.

Permitted `LoadConfig()` placement — at most one call per body, outside every loop:

| Body | Location | Threading |
|---|---|---|
| `BuildCheckoutHealthReport` | `internal/checkout_health.go:253`, before the `buildFeatureEntries` call at line 281 | `buildFeatureEntries(ws, cfg)` |
| `BuildCheckoutList` | `internal/checkout_health.go:924` | `buildCheckoutListEntries(ws, cfg)` |
| `doctorCmd` | `internal/cli/doctor.go` RunE, before the external `checkFeatureE` calls at 40 and 56 | `checkFeatureE(ws, cfg, feature)` |

These are ancestry-added direct call sites only. Pre-existing config loads remain:
`doctorCmd`'s `RequireWorkspace()` calls `LoadConfig()` internally, and every `checkFeatureE` starts
with `RequireFeaturePath`, which calls `RequireWorkspace()` and therefore loads config again. AC 53
must distinguish those existing transitive loads from the single new direct `LoadConfig()` allowed
in each body; the AC 41 shim must allow their existing repository-discovery invocation shapes.

`FeatureStackEdges` glue (§6.2), exactly:

```go
res := ResolveStackAncestryRepo(ws, cfg, featurePath, stack)
if res.RepoDir == "" {
    return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, res.Reason), res
}
edges, err := EvaluateStackAncestry(res.RepoDir, feature, stack)
if err != nil {
    return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, err.Error()),
        StackRepoResolution{Source: StackRepoUnavailable, Reason: err.Error()}
}
// stamp res.Source onto every edge, then return
```

`EvaluateStackAncestry` itself always leaves `RepoSource == ""`; only `FeatureStackEdges` stamps it.
`UnevaluatedStackEdges` issues zero Git processes and still returns `cross-repo-unsupported` for
`se.Repo != ""` and reason `base-unset` for `se.Base == ""`, so an entry never changes meaning just
because the repository was unresolvable.

### 3.1 Invocation-matrix coverage (§13) — how each cwd is satisfied

| cwd | Mechanism |
|---|---|
| repo root / nested subdir (checkout) | `RequireWorkspace` → `MainRepoRoot()` → `MainRepoRootIn(cwd)` returns the main root regardless of depth; candidate `ws.RepoRoot` is already canonical (`internal/workspace.go:428`, `canon := canonicalize(repoRoot)` at `:403`) |
| inside a linked worktree (external) | `MainRepoRootIn(<worktree>)` returns the **main** repo root; candidate 1 and candidate 2 agree ⇒ no mismatch |
| nested dir inside a linked worktree | same as above, `--git-common-dir` is depth-independent |
| workspace root `<repo>.tws` / feature dir | `MainRepoRoot()` fails ⇒ `RequireWorkspace` falls to `DetectWorkspaceRoot` + `inferExternalRepoRoot` (`internal/workspace.go:445-464`) and succeeds for the supported external fixture |
| wrong `TWS_ROOT` (other repo's metadata root) | candidate 1 (worktree evidence) wins **before** candidate 2, so ancestry is computed against the correct repo; candidate 2 yields the other repo ⇒ `Alternate` set ⇒ exactly one feature-scoped info issue |
| detached HEAD / dirty / rebase in progress | ancestry is ref-based only; no `HEAD`, no `status`, no index read |

Since ancestry probes only `refs/heads/*` and object SHAs, probing the **main** root rather than the
linked worktree is ref-identical.

---

## 4. Base-ref resolution and identity notes

`stackBaseRef(stack Stack, se StackEntry) (string, StackBaseKind)`:

| Condition | `BaseRef` | `BaseKind` |
|---|---|---|
| first `stack.Branches[i].Name == se.Base` (file order, matching `GetBranch`, `internal/stack.go:64-72`) | `"refs/heads/" + entry.GitBranch()` | `stack-entry` |
| otherwise, `se.Base != ""` | `se.Base` **verbatim** | `literal-ref` |
| `se.Base == ""` | `""` | `none` |

Both `(*ancestryEvaluator).edge` and `UnevaluatedStackEdges` call this pure helper at the start of
each edge and copy all three values before any return. The evaluator keeps the `stack Stack`
argument specifically for this resolution; the later checkout adapter may drop its own `stack`
parameter because the resolved identity is already carried by `StackEdge`.

A stack-entry match applies even when the matched parent is archived or has `Repo != ""` — the
**child's** `Repo` alone decides cross-repo, exactly as today (`internal/checkout_health.go:612-627`).
No note is emitted for either `Repo` asymmetry (§18.3 owns that).

`LastBaseSHA` peel: `rev-parse --verify --quiet --end-of-options <L>^{commit}`. Necessary because
both writers are unpeeled — `GetBranchSHA` (`internal/stack.go:85-95`) and `gitResolveRef`
(`internal/checkout_sync.go:307-314`) both use bare `rev-parse`, so an annotated-tag base records a
**tag-object** SHA.

Unrelated histories: `merge-base C P` exit 1 + empty stdout ⇒ `divergent`/`unrelated-histories`,
`MergeBase = nil`, and **never** `missing` — both refs resolved.

### 4.1 The two edge notes (only members of `StackEdgeNote`)

| Kind | Fires when | Mirrors |
|---|---|---|
| `base-identity-remote-mismatch` | `BaseKind == literal-ref` && `BaseName == DefaultBranchIn(repoDir)` && `refs/remotes/origin/<BaseName>^{commit}` resolves && differs from `P` | `resolveBase` mapping `base == DefaultBranch()` to `origin/<default>` (`internal/cli/sync_helpers.go:186-192`) and recording that SHA (`:50`, `:70`) |
| `base-identity-literal-mismatch` | `BaseKind == stack-entry` && parent `GitBranch() != BaseName` && bare `BaseName` also resolves && differs from `P` | `BuildCheckoutPlan` resolving `entry.Base` literally (`internal/checkout_sync.go:459,464`) and `gitPlainRebase(opts.RepoDir, entry.Base)` (`:896`) |

Both are info, never counted, never change `Status`/`Severity`, and are computed only when the edge
reached a classification (`Status != ""`, not cross-repo). `DefaultBranchIn` never returns an error
(it falls back to `"main"`, `internal/exec.go:87`), so `defaultBranchName()` memoises the value
including that fallback and runs at most once per evaluation — and only when at least one literal-ref
base exists.

The two note kinds are mutually exclusive on an edge: the remote mismatch requires
`BaseKind == literal-ref`, while the literal mismatch requires `BaseKind == stack-entry`.
Consequently `StackEdge.Notes` has length 0 or 1; producing both note kinds for one edge is a bug.

`repo-source-mismatch` is **not** a `StackNoteKind`. It is derived (`res.Alternate != ""`), lives in
`RepoSourceMismatchLabel`, and is emitted only by `AncestryHealthIssues` in `internal/health.go`.

---

## 5. Severity and counting

| State | Active | Archived |
|---|---|---|
| `current` | `ok` | `ok` |
| `stale` / `divergent` / `missing` | `warning` | `info` |
| `cross-repo-unsupported` | `info` | `info` |
| `""` (not evaluated) | `info` | `info` |

`SeverityError` is never produced (AC 28: `git grep -n 'SeverityError' internal/stack_ancestry.go`
must be empty). `countIssues` (`internal/checkout_health.go:755-782`) is **untouched** — it already
counts only `warning`/`error` feature entries, so archived and informational ancestry drop out
automatically.

---

## 6. `internal/checkout_health.go` — exact edits

| # | Edit | Detail |
|---|---|---|
| 1 | `CheckoutFeatureEntry` (155-168) | append after `Severity` (line 167): `LocalHeadFull`, `ParentHeadFull`, `BaseKind`, `BaseRef`, `LastBaseSHA`, `LastBaseShort`, `BaseRecord`, `MergeBase *string`, `MergeBaseShort`, `Reason`, `Guidance`, `Notes []StackEdgeNote`, `RepoSource`. **No `RefProbed` field** — the formatter derives "probed" from `AncestryStatus` (see edit 6) |
| 2 | `BuildCheckoutHealthReport` (253) | ≤1 `cfg := LoadConfig()` before the `buildFeatureEntries` call at line 281; `buildFeatureEntries(ws, cfg)` |
| 3 | `buildFeatureEntries` (551) | new signature `(ws Workspace, cfg Config)`; inside the existing feature loop (571-584) call `FeatureStackEdges(ws, cfg, feature, fp, stack)` **once per feature**, index the returned slice by position against `stack.Branches` (guaranteed 1:1, same order, §5.2 rule 1), pass each edge to `buildOneFeatureEntry`. Never call `LoadConfig` here |
| 4 | `buildOneFeatureEntry` (588) | new signature `(ws Workspace, feature string, se StackEntry, edge StackEdge, currentBranch, sessionFeature, sessionName string)` — **`stack Stack` parameter dropped only here**, after `stackBaseRef` has populated the edge. Keep the initializer fields at 589-596 and current/session logic at 601-607. Explicitly delete line 599's `e.RefExists = gitRefExists(ws.RepoRoot, gitBranch)` legacy probe and replace it with `e.RefExists = edge.RefExists`; delete 609-667; retain `return e` at 669. Assign every other field from the edge per the map below |
| 5 | delete | `gitShortSHA` (676-682), `gitFullSHA` (684-690), `gitMergeBase` (692-698). `exec` and `strings` imports stay live via `realTmuxChecker`, `healthCurrentBranch`, `gitDirty`, `gitRefExists` |
| 6 | `FormatCheckoutHealth` (846-874) | line 858-860: gate `[ref-missing]` on `!f.RefExists && f.AncestryStatus != AncestryStatusCrossRepo && f.AncestryStatus != ""`; this intentionally suppresses the previously printed tag for a cross-repo entry whose same-named local branch is missing. Line 865: `ancestry=%s` fed by `ancestryDisplayStatus(f.AncestryStatus)`; after line 872 emit ≤3 six-space detail lines (reason / guidance / notes) using the exact `"      %s\n"` style of lines 820 and 841 |
| 7 | `BuildCheckoutList` (924) | keep the exported signature; ≤1 `cfg := LoadConfig()`; body becomes `return buildCheckoutListEntries(ws, cfg)`. In the new unexported function, replace 959-981 with an edge lookup; keep 951-957 (`Current`) and 983-986 (`SessionActive`) verbatim. **Must still return `nil, err`** when `ListFeaturesResolved` fails (`TestCheckoutHealth_MalformedSpacesFailsClosed` asserts `entries != nil` is a failure) |
| 8 | `FormatCheckoutList` (1036-1038) | `CheckoutListEntry.AncestryStatus` remains a `string`; cast it at the formatter boundary and render ` [<ancestryDisplayStatus(AncestryStatus(e.AncestryStatus))>]` for everything except `current`; `""` now prints `[unevaluated]` |
| 9 | keep | `gitRefExists`, `healthCurrentBranch`, `gitDirty`, `gitActiveOp`, `countIssues`, `HasErrors`, `CheckoutListEntry` (no new fields) |

### 6.1 `StackEdge` → `CheckoutFeatureEntry` (full → short mapping)

| Entry field | Source | Note |
|---|---|---|
| `LocalHead` (existing) | `edge.LocalHeadShort` | keeps today's short-SHA meaning and JSON key |
| `ParentHead` (existing) | `edge.ParentHeadShort` | same |
| `LocalHeadFull` / `ParentHeadFull` (new) | `edge.LocalHead` / `edge.ParentHead` | 40-hex, never printed by this feature |
| `RefExists` (existing) | `edge.RefExists` | false whenever `!edge.RefProbed` |
| `BaseName` (existing) | `edge.BaseName` | `se.Base` verbatim |
| `BaseGitBranch` (existing) | `strings.TrimPrefix(edge.BaseRef, "refs/heads/")` for `stack-entry`; `edge.BaseName` for `literal-ref`; `""` for `none` | byte-identical to today's line 610-620 result; **never** `refs/heads/`-prefixed, and requires no dropped `stack` argument |
| `BaseRef` (new) | `edge.BaseRef` | fully qualified ref actually probed |
| `MergeBase` (new, `*string`) | `edge.MergeBase` | never printed |
| `MergeBaseShort` (new) | `edge.MergeBaseShort` | the token actually printed |
| `AncestryStatus`, `Severity`, `Reason`, `Guidance`, `Notes`, `BaseKind`, `BaseRecord`, `LastBaseSHA`, `LastBaseShort`, `RepoSource` | direct copies | — |
| `Feature`, `Name`, `GitBranch`, `Archived` | direct copies | — |
| `Current` | computed in `buildOneFeatureEntry` from `currentBranch`/session (lines 601-607) | **not** from the edge |

Every generated SHA token is abbreviated: `head=`, `parent=`, `last-base=`, `merge-base=`, and every
`<p>`/`<l>`/`<alt-short>` inside guidance and notes. AC 56's no-40-hex entry-block assertion is
scoped to `BaseRecord == present` and `BaseRecord == absent`. For `BaseRecord == unresolvable`,
`LastBaseSHA` is display-sanitized user metadata and may itself contain a 40-hex substring; AC 54,
not AC 56, governs that case.

`RefExists` now comes from the peeled child probe rather than `gitRefExists`, which is what fixes the
branch/tag-collision case and the bogus-40-hex case; `gitRefExists` itself is untouched.
For a cross-repo entry with no same-named local branch, output deliberately changes: the old line-599
probe made `RefExists == false` and printed `[ref-missing]`; the new no-probe edge also has
`RefExists == false`, but the formatter suppresses the tag because the status is cross-repo.

---

## 7. External doctor — `internal/health.go` + `internal/cli/doctor.go`

### 7.1 `internal/health.go`

```go
type HealthIssue struct {
    Branch   string
    Problem  string
    Hint     string
    Severity CheckoutSeverity // "" == warning
}

func (h HealthIssue) EffectiveSeverity() CheckoutSeverity   // "" -> SeverityWarning
func (h HealthIssue) String() string                        // severityIcon(EffectiveSeverity()) + 6-space hint
func CountHealthIssues(issues []HealthIssue) int            // warning + error only
func AncestryHealthIssues(res StackRepoResolution, edges []StackEdge) []HealthIssue
```

Zero-value compatibility is the whole point: the three existing producers
(`CheckWorktreeBranch`, `CheckWorktreeDirty`, `CheckWorktreeInjectLinks`) construct `HealthIssue`
literals with **field names** (`internal/health.go:29,38,54,95`), so adding a trailing field compiles
unchanged, and `EffectiveSeverity()` makes them render `[!]` and count exactly as today. Today's
`String()` hardcodes `"  [!] %s: %s"` (line 18) and `severityIcon(SeverityWarning)` returns `"[!]"`
(`internal/checkout_health.go:901-902`) — **byte-identical**. `checkSyncWorktreeBranch`
(`internal/cli/sync_helpers.go:175`) also stays untouched.

`AncestryHealthIssues` mapping:

| Input | Issue |
|---|---|
| `edge.Status == current` | none |
| any other edge | `Branch = edge.Name`, `Problem = "ancestry " + ancestryDisplayStatus(edge.Status) + ": " + string(edge.Reason)`, `Hint = edge.Guidance`, `Severity = edge.Severity` |
| any `repo-unavailable` edges | **collapse to one** issue per feature, `Branch = "stack"` |
| `res.Alternate != ""` | **exactly one** info issue: `Branch = "stack"`, `Problem = RepoSourceMismatchLabel + ": ancestry evaluated against <RepoDir> (source: worktree)"`, `Hint = "the workspace also resolves to <Alternate>; check TWS_ROOT or the configured workspace path"` |

`CheckFeatureHealth` (105-131) is **not modified**; ancestry is composed by the CLI layer.

### 7.2 `internal/cli/doctor.go`

`checkFeatureE(ws internal.Workspace, cfg internal.Config, feature string) (int, error)`:

1. `internal.RequireFeaturePath(feature)` — **first**, unchanged, still the source of the existing
   error text and ordering (line 74);
2. the `os.Stat` "not found" short-circuit (79-82) — unchanged;
3. `issues := internal.CheckFeatureHealth(featurePath)` (84) — unchanged;
4. `stack, err := internal.LoadStack(featurePath)`; when it loads **and has entries**,
   `edges, res := internal.FeatureStackEdges(ws, cfg, feature, featurePath, stack)` and append
   `internal.AncestryHealthIssues(res, edges)...`. A `LoadStack` failure adds nothing and is **not
   fatal** (matching `CheckFeatureHealth`'s own early return at `internal/health.go:107-110`);
5. `counted := internal.CountHealthIssues(issues)`;
6. `counted == 0` ⇒ print `healthy (%d active worktree(s))` (existing worktree count at 87-96) and
   then still print any info issues; otherwise `%s: %d issue(s)` with **`counted`** (replacing
   `len(issues)` at line 98, and `len(issues)` at the `return` on line 104) followed by all issues in order;
7. `return counted, nil`.

`doctorCmd`: keep the single `internal.RequireWorkspace()` at line 30 and keep `wsErr` non-fatal for
the external control flow; add at most one direct `internal.LoadConfig()` in the RunE body **before**
the feature loop; pass `(ws, cfg)` at both call sites (40, 56). A stable `RequireWorkspace` failure
is returned by `checkFeatureE`'s first `RequireFeaturePath` call before ancestry runs, so there is no
dedicated zero-`Workspace` ancestry fixture. `runCheckoutDoctor` (107-122) is untouched, so exit
semantics are literally unchanged.

**Fail-soft behaviour** to preserve: an unresolvable repository produces one info issue, a counted
total identical to the pre-feature value, the `healthy (…)` line still printed, and
`CheckWorktreeBranch`/`Dirty`/`InjectLinks` still running and still reporting.

---

## 8. Duplicated helpers removed vs. retained callers

| Symbol | Action | Evidence |
|---|---|---|
| `gitShortSHA` | delete | only callers at `internal/checkout_health.go:643,645,647` — all inside the deleted block |
| `gitFullSHA` | delete | only callers at `:658` and `:976` — both inside deleted blocks |
| `gitMergeBase` | delete | only callers at `:651` and `:974` — both inside deleted blocks |
| `gitRefExists` | **keep** | `internal/agent_status.go:1384` and `:1433` (checkout materialization); keeps `tws status` byte-identical and the `agent-work-status-dashboard` edge soft |
| `gitIsAncestor` | keep, reuse unchanged | `internal/checkout_sync.go:365`; also still used at the deleted `:661` site |
| `healthCurrentBranch`, `gitDirty`, `gitActiveOp` | keep | also used by `internal/agent_status.go:789,1468` |
| duplicate `if isParentEntry {…} else {…}` at `:644-648` | delete | both arms are identical — dead branching |
| `tws status` base-resolution copy (`internal/agent_status.go:1346,1352-1357`) | **do not touch** (N7) | keeps status output byte-identical |

Shared display token: `ancestryUnevaluatedToken = "unevaluated"` declared once in
`internal/stack_ancestry.go`; `ancestryDisplayStatus` is its only reader and has **exactly three**
call sites — `FormatCheckoutHealth`, `FormatCheckoutList`, `AncestryHealthIssues`. It is never
compared against, never assigned into `AncestryStatus`, and never written into
`CheckoutFeatureEntry.AncestryStatus` or `CheckoutListEntry.AncestryStatus`.

---

## 9. Documentation and skills (same commit — `assets/skills/**` is `go:embed`)

`assets/skills/embed.go` embeds all three files, so a stale asset ships in the binary.

| # | File:line (verified) | Change |
|---|---|---|
| 1 | `assets/skills/claude/tesseraworkspaces/SKILL.md:244` | external doctor bullet gains per-edge stack ancestry when the source repo is resolvable |
| 2 | `assets/skills/claude/tesseraworkspaces/SKILL.md:251` | ancestry set → `current/stale/divergent/missing/cross-repo`; add reason/last-base/merge-base detail line; archived entries informational |
| 3 | `assets/skills/claude/tesseraworkspaces/SKILL.md:262` | list ancestry set → `stale/divergent/missing/cross-repo/unevaluated` |
| 4 | same file, near 254 | short paragraph: `stale` ⇒ run `tws sync`; `divergent` ⇒ recorded base left the parent's history, `--onto` required and selected automatically; both exit 0; doctor never fetches |
| 5 | `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md:106` (doctor guidance bullet) | one line so orchestrators stop treating `divergent` as an emergency |
| 6 | `assets/skills/copilot/tws.prompt.md:33` | `tws doctor [feature]` description gains "including stack ancestry per configured parent-child edge" |
| 7 | `docs/cheatsheet.md:109` | doctor line gains "ancestry" |
| 8 | `docs/roadmap.md:58-64` | mark line 60 "Stack ancestry doctor" shipped; ahead/behind counts stay with line 63 "Stack status" |
| 9 | `docs/engineering-workflow.md:14-21` (list items) and `26-29` (next-feature paragraph) | add slice 9; point "Next roadmap feature" at `stack-status` |
| 10 | `CHANGELOG.md` under the current `## Unreleased` heading (line 3) | one entry listing the §16 user-visible changes, explicitly including that cross-repo entries no longer print the misleading local `[ref-missing]` tag |

`## Unreleased` is the concrete target in this tree: there is no next-version heading yet. The
spec's "under the next patch version" means this Unreleased bucket until release preparation creates
that heading; do not invent a version number. Roadmap ownership is cited from
`docs/roadmap.md:58-64`. The `divergent` wording constraint comes from `spec.md` §2 N5 / §4.5, not
the unrelated roadmap line 127: say "the recorded base commit is no longer in the parent's
history", never "your change diverged".

---

## 10. Tests

### 10.1 Reusable real-Git helpers (all verified present)

| Helper | Location | Use |
|---|---|---|
| `setupHealthTestRepo` | `internal/checkout_health_test.go:40-83` | checkout-mode repo + `.tws/config.yaml` + workspace |
| `gitInTest` | `internal/checkout_health_test.go:113-129` | git in a temp repo with isolated env |
| `addFeatureToRepo` / `addStackEntries` | `internal/checkout_health_test.go:85-99`, `101-111` | stack fixtures (`addStackEntries` accepts full `StackEntry`, so `Branch`, `Repo`, `Archived`, `LastBaseSHA` are all settable) |
| `fakeProcessChecker` / `fakeTmuxChecker` | `internal/checkout_health_test.go:16-36` | deterministic seams |
| `setupGitRepo(t, defaultBranch)` | `internal/cli/new_integration_test.go:135-149` | **real local bare remote**, `origin/HEAD` set — exactly what AC 35 needs |
| `withWorkspaceEnv` | `internal/cli/new_integration_test.go:152-160` | `HOME` + `TWS_ROOT` + chdir |
| `createWorktree` | `internal/cli/new.go:163` | production worktree constructor already driven by external tests |
| `gitRun` / `gitOutput` | `internal/cli/new_integration_test.go:180-199` | `cmd.Dir`-based git |
| `setupGitRepoCheckout` / `requireWorkspaceForTest` / `gitInDir` | `internal/cli/checkout_lifecycle_test.go:19,53,63` | checkout CLI fixtures |
| PATH-shim precedent | `internal/cli/status_test.go:22-67` (`withIdleTmuxOnPath`, `withoutTmuxOnPath`) | pattern for the AC 41 argv shim |

### 10.2 New / changed test files

| File | Status | Contents |
|---|---|---|
| `internal/stack_ancestry_test.go` | **new** | AC 1-30, 34-43, 47, 51-52, 54, 56-57 plus malformed worktree-name containment (package `internal`) |
| `internal/cli/doctor_ancestry_test.go` | **new** | AC 27, 31-33, 42 (external), 49, 55 (package `cli`) |
| `internal/cli/checkout_doctor_test.go` | **changed** | exactly 2 `checkFeatureE` call sites: lines **125** and **184** (re-verified; no other caller in the tree). No assertion changes; `TestExternalDoctor_UnchangedBehavior` still asserts **0** issues |
| `internal/checkout_health_test.go` | **unchanged** | AC 44 requires it passes verbatim |

### 10.3 Acceptance criterion → test placement

| AC group | Test file | Fixture notes |
|---|---|---|
| 1-11 classification | `internal/stack_ancestry_test.go` | `gitInTest` + `git branch -f`, `commit --amend`, `rebase`, `checkout --orphan` |
| 12-14 base record | same | `LastBaseSHA` set directly through `addStackEntries`; AC 14 uses `git tag -a` and records the **tag-object** SHA |
| 15-22 ref handling | same | AC 17 uses a bogus 40-hex (verified `--verify --quiet` exits 0 but `^{commit}` exits 1); AC 20 creates branch **and** tag `dup`; AC 21 uses `Branch:` fields |
| 23-24 cross-repo | same | second real repo; assert its `git rev-parse --all` + `.git` mtime unchanged and the shim never names its path |
| 25-28 severity/counting | same + `internal/cli/doctor_ancestry_test.go` (AC 27) | compare `report.Issues` with/without archived entries |
| 29-30 repo plumbing | same | AC 29 under the shim: **zero** `git` processes for `EvaluateStackAncestry("", …)`; AC 30 at most the one `--git-common-dir` validation; an external `StackEntry.Name: "../foreign"` returns repository unavailable before join/fallback and records zero Git processes |
| 31-33 external doctor | `internal/cli/doctor_ancestry_test.go` | `setupGitRepo` + `createWorktree`; AC 33 needs two repos and a `TWS_ROOT` pointed at the second repo's metadata root while the feature's worktree belongs to the first |
| 34, 52, 55 repo source / canonical roots | both files | assert `res.RepoDir == canonicalize(MainRepoRootIn(res.RepoDir))` |
| 35-37 identity notes | `internal/stack_ancestry_test.go` | AC 35 requires the **bare local remote** from `setupGitRepo`; AC 36 needs a real branch literally named like a parent's logical name |
| 38-40 agreement / output / determinism | `internal/stack_ancestry_test.go` | AC 38 compares `report.Features[i].AncestryStatus` with `BuildCheckoutList(...)[i].AncestryStatus` positionally |
| 41 read-only + argv shim | `internal/stack_ancestry_test.go` | see §10.4 |
| 42 exit semantics | both files | `runCheckoutDoctor` returns nil; external `checkFeatureE` returns `(n, nil)` |
| 43 process bounds | `internal/stack_ancestry_test.go` | 5-entry linear stack; positive/negative/abbrev caching; 5 cross-repo and 5 `base: ""` stacks ⇒ 0 ancestry probes |
| 44-45 regressions | existing files, run as-is | |
| 46-48, 50-51, 53 greps/gates | shell assertions inside Go tests or the gate script | patterns pre-tested, see §11.2 |
| 49 CLI surface | `internal/cli/doctor_ancestry_test.go` or smoke | `doctorCmd` has `Args: cobra.MaximumNArgs(1)` and **no flags**; `listCmd` has `Args: cobra.NoArgs` and no flags ⇒ `--json` yields `unknown flag: --json` |
| 54, 56, 57 sanitisation / mapping / guidance | `internal/stack_ancestry_test.go` | AC 56 runs its no-40-hex entry-block assertion only for `BaseRecord` present/absent; AC 54 owns unresolvable raw metadata; AC 57 is a pure table test over `ancestryGuidance`, no repo required |

### 10.4 The AC 41 argv/cwd shim — concrete traps

The shim is a `sh` script named `git` prepended to `PATH` that appends `argv` **and** its working
directory to a record file, then `exec`s the real git.

1. **Capture the real git path before overriding `PATH`** (`exec.LookPath("git")`), exactly as
   `withoutTmuxOnPath` does (`internal/cli/status_test.go:49-51`).
2. **Truncate the record file immediately before the measured call** and read it immediately after.
   Otherwise `gitInTest`/`gitRun` fixture commands — which also resolve `git` through `PATH` — land
   in the same file and break both the shape assertion and the process counts.
3. **cwd comparison must be symlink-stable.** `t.TempDir()` on macOS lives under `/var/folders/…`,
   which is a symlink to `/private/var/…`, while `repoDir` is the output of `canonicalize`
   (`filepath.EvalSymlinks`). Record `pwd -P` in the shim and compare against `canonicalize(repoDir)`,
   or the assertion fails only on macOS. This is precisely the class of breakage
   `fix-status-tmux-test-portability` already had to fix once.
4. **`t.Setenv` forbids `t.Parallel()`** — do not parallelise shim tests.
5. Classify each recorded invocation before asserting: strip an optional leading `-C <dir>`, then
   match the seven non-ancestry shapes (`rev-parse --abbrev-ref HEAD`, `rev-parse --short HEAD`,
   `status --porcelain`, `rev-parse --show-toplevel`, `rev-parse --git-common-dir`,
   `rev-parse --abbrev-ref origin/HEAD`, `symbolic-ref --short HEAD`). Only the remainder are ancestry
   probes and only they carry clauses (a)-(c). `status --porcelain` from `gitDirty`
   (`internal/checkout_health.go:337`) is expected and must not trip the forbidden-verb clause.
   The deleted line-599 `gitRefExists` call must not appear as a bare
   `rev-parse --verify <gitBranch>` shape; AC 46 separately proves its only remaining callers are
   agent status plus the retained definition.
6. Read-only snapshot set for AC 41: `git rev-parse --all`, `git reflog --all`,
   `git for-each-ref refs/remotes`, `FETCH_HEAD` presence, the recursive file tree of the repo and of
   `.tws/`, and `stack.yaml` bytes — before and after **both** doctor and list. Extends
   `TestCheckoutHealth_ReadOnly` (`internal/checkout_health_test.go:885-945`), which currently
   snapshots only branches, HEAD, transaction, and lock.

### 10.5 Existing tests — predicted outcomes (no edits required)

| Test | Outcome under the new rules |
|---|---|
| `TestCheckoutHealth_Healthy` (`:133`) | `feat-branch` == `main` ⇒ rule 1 ⇒ `current`, `ok`, "All healthy" |
| `TestCheckoutHealth_StaleChild` (`:189`) | child has **zero** unique commits and no base record ⇒ rule 6 ⇒ `stale` + `warning`, `Issues >= 1`. Parent entry itself ⇒ `current` |
| `TestCheckoutHealth_MissingRefs` (`:237`) | `child-ref-missing` ⇒ `missing`, `RefExists == false`; `[ref-missing]` still rendered (probed) |
| `TestCheckoutHealth_CrossRepo` (`:1103`) | existing assertion checks only `AncestryStatus`; add a focused formatter fixture with a missing same-named local branch to pin the intended removal of `[ref-missing]` |
| `TestCheckoutList_Output/_ArchivedEntry/_GitBranchDiffers/_SessionMarker` (`:947,996,1018,1041`) | all fixtures are `current` ⇒ no tag, no `[unevaluated]` |
| `TestCheckoutHealth_MalformedSpacesFailsClosed` (`:1173`) | requires `BuildCheckoutList` to return `nil, err` — preserve the early return before any edge work |
| `TestExternalDoctor_UnchangedBehavior` (`internal/cli/checkout_doctor_test.go:116`) | `branch1` from `main`, materialized ⇒ `current` ⇒ still **0** |
| `TestExternalFeatureDir_DoctorRegression` (`:156`) | the `cp -r` copy keeps the `.git` **file**, so `MainRepoRootIn(<copy>)` still resolves to the real repo; candidates 1 and 2 agree ⇒ no mismatch. The test only does `_ = issues` |
| `space_guard_test.go:552,559` | `ListFeaturesResolved` fails first; no ancestry runs |

---

## 11. Implementation sequence and commands

### 11.1 Minimal ordered sequence

1. `internal/stack_ancestry.go` — types, consts, `ErrRepoUnavailable`, `ancestryGit`,
   `newAncestryEvaluator`, evaluator +
   cache, `resolveCommit`/`abbrev`/`ancestryMergeBase`, `stackBaseRef`, classification,
   `ancestrySeverity`, `ancestrySanitize`, `ancestryGuidance`, `ancestryDisplayStatus`,
   `EvaluateStackEdge`/`EvaluateStackAncestry`/`UnevaluatedStackEdges`. Compiles standalone.
2. `internal/stack_ancestry_test.go` — AC 1-22, 28-30, 54, 57 (core classification, no consumers
   yet). Green before touching any consumer.
3. `ancestryRepoCandidate`, `ResolveStackAncestryRepo`, `FeatureStackEdges`, `identityNotes` — plus
   AC 23, 34-37, 52, 55.
4. `internal/checkout_health.go` — additive fields, `buildFeatureEntries`/`buildOneFeatureEntry`
   rewiring, delete the three helpers, formatter changes, `buildCheckoutListEntries`. Run
   `go test ./internal/... -run 'TestCheckoutHealth_|TestCheckoutList_' -count=1` — must pass with
   **no test edits** (AC 44).
5. `internal/health.go` — `Severity`, `EffectiveSeverity`, `String()`, `CountHealthIssues`,
   `AncestryHealthIssues`.
6. `internal/cli/doctor.go` — `doctorCmd` config load + threading, `checkFeatureE` rewrite; update
   the two test call sites at `internal/cli/checkout_doctor_test.go:125,184`.
7. `internal/cli/doctor_ancestry_test.go` — AC 27, 31-33, 42, 49, 55.
8. Remaining cross-cutting tests: AC 24-26, 38-43, 46-48, 50-51, 53, 56.
9. Docs and skills (§9) in the same change set.
10. Full gates, then `tpatch` execute/record per `.tpatch/steering/local.md`.

Steps 1-3 touch no existing file, so the diff stays reviewable and step 4 is a pure substitution.

### 11.2 Focused commands (progressively widening)

```bash
go build ./...
go test ./internal/ -run 'TestStackAncestry_' -count=1
go test ./internal/ -run 'TestCheckoutHealth_|TestCheckoutList_|TestAncestryDoctorListAgree' -count=1
go test ./internal/cli/ -run 'TestExternalDoctor_|TestExternalList_|TestExternalFeatureDir_|TestCheckoutDoctor_|TestCheckoutList_CLI' -count=1
gofmt -l internal cmd assets
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build && ./bin/tws doctor --help && ./bin/tws doctor --json; ./bin/tws list --json
git diff --check
tpatch feature deps --validate-all
```

Grep gates, **pre-tested in this tree** (patterns behave as the spec expects):

```bash
git grep -nE '(^|[^[:alnum:]_])(gitMergeBase|gitFullSHA|gitShortSHA)([^[:alnum:]_]|$)' -- internal/   # expect empty
git grep -nE '(^|[^[:alnum:]_])gitMergeBase[[:alnum:]_]+' -- internal/                                 # expect empty
git grep -n 'gitRefExists' -- internal/                                    # only agent_status.go + its definition
git grep -n '"unevaluated"' -- 'internal/**.go' ':!internal/**_test.go'    # exactly 1
git grep -n 'ancestryDisplayStatus(' -- 'internal/**.go' ':!internal/**_test.go'  # definition + 3 call sites
git grep -n 'evaluation-unavailable' -- internal/                          # empty
git grep -nE '^[[:space:]]*AncestryStatus[[:alnum:]_]*[[:space:]]+AncestryStatus[[:space:]]*=' -- internal/checkout_health.go  # exactly 5
git grep -nE 'StackNoteKind = "' internal/stack_ancestry.go                # exactly 2
git grep -n 'repo-source-mismatch' -- 'internal/**.go' ':!internal/**_test.go'    # label decl + 1 use in health.go, none in checkout_health.go
git grep -n 'rev-list\|--count\|--left-right\|patch-id\|fetch' -- internal/stack_ancestry.go   # empty
git grep -n 'SeverityError' internal/stack_ancestry.go                     # empty
git grep -n 'LoadConfig' -- internal/stack_ancestry.go                     # empty
```

Verified now: the `internal/**.go` pathspec **does** recurse into `internal/cli/` (checked with
`git grep -n 'checkFeatureE' -- 'internal/**.go' ':!internal/**_test.go'`, which returned the three
`internal/cli/doctor.go` hits and no test hits), and the AC 47 `AncestryStatus` ERE returns exactly
the five constant lines today. AC 46's ERE returns exactly the 10 hits that will be deleted.

---

## 12. Traps found during exploration

1. **`RefProbed` is not a `CheckoutFeatureEntry` field.** §10.1.1's map omits it deliberately, so
   `FormatCheckoutHealth` must gate `[ref-missing]` on `AncestryStatus` (not cross-repo, not `""`).
   This is sound: `Status == "" && !RefExists` occurs only for cross-repo, `base-unset`, and
   `repo-unavailable`; `ancestry-probe-failed` is only reachable **after** `C` resolved.
2. **`LoadConfig()` inside the checkout builders reads the *test process's* repo config**, because
   `repoConfigPath` uses the process cwd. Harmless here — `cfg` is consumed only by
   `inferExternalRepoRoot` (external candidate 3) — but it must not be repurposed for checkout logic.
3. **Repo resolution runs per feature.** In checkout mode each feature costs one
   `ancestryRepoCandidate(ws.RepoRoot)` plus one `EvaluateStackAncestry` validation — two
   `--git-common-dir` processes per feature. Within the §7.3 per-evaluation bounds and classified as
   non-ancestry shapes, but note it: no command-wide process-count guarantee is claimed, and none
   should be asserted.
4. **Candidate 3 is O(features²) in directory work** when it is reached: `inferExternalRepoRoot`
   rescans the whole metadata root and `LoadStack`s every feature, once per feature in the doctor
   loop. It is only reached when candidates 1 and 2 both fail (no materialized worktrees **and** no
   usable `ws.RepoRoot`), which is the AC 32 fixture. Spec-conformant; do not add a cross-feature
   cache in this feature.
5. **Const declarations must repeat their type** or AC 47/51 under-count (§2.1).
6. **`exec.Cmd.Output()` requires `Stderr == nil`** — consistent with the `cmd.Stderr = nil` rule,
   but a naive "capture stderr for `<detail>`" refactor would break both.
7. **Shim cwd on macOS** — compare `pwd -P` against `canonicalize(repoDir)` (§10.4.3).
8. **`BuildCheckoutList` must keep returning `nil` (not an empty slice) on the spaces failure path**
   (`TestCheckoutHealth_MalformedSpacesFailsClosed:1191-1196`).
9. **Do not justify a zero-`Workspace` ancestry test from `doctorCmd`.** Although the resolver's
   mode switch remains checkout-only (`ws.Mode == ModeCheckout`), `checkFeatureE` first calls
   `RequireFeaturePath`, which calls `RequireWorkspace` again. A stable workspace-resolution failure
   therefore returns before stack loading or ancestry; supported external cwd cases use a resolved
   external `Workspace`.
10. **`ancestryGuidance` needs a detail carrier** for `repo-unavailable` / `ancestry-probe-failed`;
    `StackEdge` has no `Detail` field. Pass it as a parameter (§2.3) — the alternative, adding a
    field, would change the §5.2 struct contract.
11. **Never join an untrusted `StackEntry.Name`.** Require `filepath.IsLocal` before
    `filepath.Join`, then verify canonical candidate containment under canonical `worktrees/` before
    Git; on failure return unavailable immediately, with no candidate fallback and no Git probe, so
    `worktrees/../foreign` and symlink escapes cannot select a foreign repository.

### 12.1 Spec items still impossible, unsafe, or requiring restatement

Nothing in the spec is **impossible**. Six items need the restatement above rather than a literal
reading:

| Item | Restatement |
|---|---|
| AC 43 "5 cross-repo entries produce **zero**" | zero *ancestry Git probes*; the single `MainRepoRootIn` validation still runs and is a non-ancestry shape (§2.5) |
| §12.1 "no probe touches …" + AC 41 clause (b) | applies to ancestry probes issued via `ancestryGit`/`gitIsAncestor`; `MainRepoRootIn` and `DefaultBranchIn` legitimately keep `-C` |
| §14.1 `ancestryGuidance(e StackEdge) string` | needs a `detail string` parameter (§2.3, trap 10) |
| AC 41 shim | must truncate its record file around the measured call, use `pwd -P`, account for pre-existing `RequireWorkspace`/config discovery shapes, and assert the deleted line-599 bare `gitRefExists` probe is absent (§10.4) |
| AC 56 "no 40-hex substring anywhere in the entry block" | applies to `BaseRecord == present` or `absent`; unresolvable recorded metadata is governed by AC 54 sanitization and may contain hex-looking user input |
| Cross-repo `RefExists` rendered output | when the same-named local branch was missing, `[ref-missing]` was previously printed; it is now intentionally suppressed because no local probe is meaningful. Pin this in a formatter test and list it in `CHANGELOG.md` |

No spec clause is unsafe: the feature writes nothing, starts no network process, and cannot emit
`SeverityError`.

---

## 13. Dependency recheck and recommendation

`tpatch feature deps --validate-all` → **`DAG: ok (0 violations)`** (run during this exploration).
All 11 hard and 4 soft parents are in state `applied`; the hard closure is 22 features and already
contains `workspace-mode-foundation`, `checkout-workspace-lifecycle`, `checkout-agent-sessions`,
`fix-checkout-feature-path-routing`, `worktree-health-check`, `multi-repo-workspaces`,
`workspace-sibling-links`, and `fix-default-base-branch`.

**Recommendation: add exactly one edge —**

```bash
tpatch feature deps stack-ancestry-doctor add skill-distribution --kind soft
```

Rationale: §15 requires editing all three `go:embed`-compiled assets
(`assets/skills/claude/tesseraworkspaces/SKILL.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
`assets/skills/copilot/tws.prompt.md`, all listed in `assets/skills/embed.go`) in the same commit.
`skill-distribution` (state `applied`) owns that distribution mechanism, and
`agent-work-status-dashboard` — the most recent comparable feature — already carries it as a **soft**
edge. Both parents are applied ancestors, so the addition cannot introduce a cycle.

No other change. In particular:

- `agent-work-status-dashboard` stays **soft** because `gitRefExists`, `healthCurrentBranch`, and
  `gitDirty` are all left untouched (verified: the only deletions are `gitShortSHA`, `gitFullSHA`,
  `gitMergeBase`, whose sole callers are inside the two deleted classifier blocks). **Upgrade to hard
  if any revision modifies those three shared helpers.**
- `checkout-doctor-observability`, `checkout-stack-safety`, `branch-name-decoupling`,
  `amend-aware-rebase`, `worktree-health-check`, `fix-external-feature-dir-resolution`,
  `multi-repo-workspaces`, `workspace-sibling-links`, `fix-default-base-branch`,
  `keep-track-of-stacked-diffs-and-dependencies` remain hard and are each exercised by a concrete
  anchor in §1.
- `stack-status` is a **child** (`state: requested`, hard-depends on this slug) and consumes
  `StackEdge` unchanged; it is not a dependency.
- **External dependencies: none.** `go.mod` is untouched. The only tool requirement is
  `git ≥ 2.24` for `--end-of-options`; the working environment has 2.55.0.

Re-run `tpatch feature deps --validate-all` after adding the edge and again before landing.
