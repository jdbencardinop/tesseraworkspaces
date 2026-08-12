# Specification — stack-ancestry-doctor

First version. Every decision left open by `analysis.md` is settled here; nothing in this document is
"to be decided during implementation".

## 1. Problem statement

Parent-child ancestry classification exists three times, in two disagreeing dialects, in one mode
only:

- checkout doctor — `buildOneFeatureEntry` (`internal/checkout_health.go:588-670`);
- checkout list — a shorter, `divergent`-less variant in `BuildCheckoutList`
  (`internal/checkout_health.go:924-992`);
- `tws status` — a third base-resolution copy in `buildEntry` (`internal/agent_status.go:1339-1368`,
  which deliberately computes no ancestry).

External `tws doctor` has none: `CheckFeatureHealth` (`internal/health.go:105-131`) only checks
worktree-branch match, dirtiness, and inject symlinks, and `checkFeatureE`
(`internal/cli/doctor.go:73-105`) holds no repository handle at all.

Neither classifier reads `LastBaseSHA`, the only recorded fact that separates "the parent moved
forward" from "the parent's history was rewritten", so the most common stacked-diff state — parent
advanced while the child holds unique commits — is reported as `divergent`, the alarming label.
Four further demonstrable misclassifications are listed in `analysis.md` (annotated-tag bases can
never be `current`; a parent reset backwards inside the child's history reports `divergent`;
`gitRefExists` accepts non-existent 40-hex SHAs; bare `rev-parse` prefers a tag over a same-named
branch).

This feature extracts **one shared, mode-independent, read-only evaluator** and makes checkout
doctor, checkout list, and external doctor consume it. It is a correction plus an extraction, not
net-new machinery, plus the repository plumbing external mode has always lacked.

## 2. Goals

- **G1.** One evaluator, `EvaluateStackAncestry(repoDir, feature, stack)`, that is mode-independent
  by construction: it takes a validated repository directory and a `Stack`; never a `Workspace`,
  never a worktree path, never a mode flag.
- **G2.** Correct classification over `C` (child head), `P` (parent head), `L` (`LastBaseSHA`), using
  peeled commits, `refs/heads/` for stack-entry parents, and literal refs otherwise.
- **G3.** Exactly the five existing `AncestryStatus` values; unavailable evaluation is represented
  without inventing a sixth value and without pretending a Git state.
- **G4.** Integration into checkout doctor, checkout list, and external doctor, so the same fixture
  produces the same answer everywhere.
- **G5.** Strictly read-only: no fetch, no ref write, no metadata write, no `git -C ""`, no
  reflog-dependent probes, deterministic across repeated runs.
- **G6.** Preserved exit semantics in both modes; archived findings never inflate issue counts.
- **G7.** A reusable projection (`StackEdge`) rich enough for the child feature `stack-status` to
  render without recomputing anything.
- **G8.** Removal of the duplicated ancestry code and its unpeeled helpers, not a fourth copy.

## 3. Non-goals

- **N1.** No new command. No `--json` on `doctor` or `list`, and no JSON emitted by this feature at
  all. `stack-status` owns machine output.
- **N2.** No ahead/behind counts anywhere (`--count`, `rev-list --left-right`). `stack-status` owns
  them.
- **N3.** No `tws new` change: `LastBaseSHA` is still not written at creation. Recording it is a
  mutation and belongs to its own feature (§18.1).
- **N4.** No fix for the checkout-sync literal-base planning gap (`BuildCheckoutPlan` never resolving
  a parent entry's `GitBranch()`, `internal/checkout_sync.go:452-466,891-897`). This feature reports the
  disagreement and never copies it (§9, §18.2).
- **N5.** No same-repo optimization for cross-repo entries. Every `Repo != ""` entry stays
  unsupported, informational, and provokes **zero** Git processes (§18.3).
- **N6.** No tpatch integration, no `git patch-id`, no change-equivalence reasoning. `divergent` is
  phrased as "the recorded base commit is no longer in the parent's history", never as "your change
  diverged" (`docs/roadmap.md:127`).
- **N7.** No change to `gitRefExists`, `healthCurrentBranch`, `gitIsAncestor`, or any sync path, so
  `tws status` output stays byte-identical.
- **N8.** No fetch, no `--fork-point`, no remote probing, no cross-repo path probing, no
  `git status`/index refresh.
- **N9.** No fix for `internal/cli/list.go:63,66,81` passing `entry.Name` where `GitBranch()` is
  required (external list). Out of scope; already tracked.
- **N10.** No dirty/rebase-in-progress detection beyond what each doctor already does.

## 4. Semantic model (normative)

Per edge: `C` = child head, `P` = parent head, `L` = recorded `LastBaseSHA`, `M` = `merge-base(C,P)`.

### 4.1 Verified Git facts this model relies on

Re-verified for this spec in a throwaway repository under the project directory (deleted
afterwards), with `git version 2.55.0`:

| Probe | Result |
|---|---|
| `rev-parse <annotated-tag>` | tag-object SHA, **not** the commit |
| `rev-parse <annotated-tag>^{commit}` | the commit |
| `merge-base --is-ancestor <tag-object-sha> <branch>` | exit 0 (Git peels tag objects itself) |
| `merge-base --is-ancestor A B`, unrelated histories | exit **1** (a normal "no"), both directions |
| `merge-base A B`, unrelated histories | exit **1**, empty stdout |
| `rev-parse --verify --quiet <bogus 40-hex>` | exit **0** |
| `rev-parse --verify --quiet <bogus 40-hex>^{commit}` | exit **1** |
| `merge-base --is-ancestor <unknown-sha> <ref>` | exit **128** (fatal), not 1 |
| branch and tag both named `dup`: `rev-parse dup` | the **tag**; `rev-parse refs/heads/dup` the branch |
| `rev-parse --verify --quiet --end-of-options <ref>^{commit}` | supported |
| `merge-base --is-ancestor --end-of-options A B` | supported, exit codes unchanged |

Two consequences are normative: refs that resolve are **never** `missing`, and a non-zero
`--is-ancestor` exit is only a "no" when it is exactly 1.

### 4.2 Resolution (before any classification)

1. `se.Repo != ""` → **cross-repo-unsupported**. No Git process is started for this edge, against any
   path.
2. `se.Base == ""` → **not evaluated**, reason `base-unset`. No Git process is started for this edge.
3. `C := rev-parse --verify --quiet --end-of-options refs/heads/<se.GitBranch()>^{commit}`.
   Failure → `missing`, reason `child-ref-missing`.
4. Base ref selection (single rule, one place):
   - if some `stack.Branches[i].Name == se.Base` → `BaseRef = "refs/heads/" + that entry.GitBranch()`,
     `BaseKind = stack-entry`;
   - otherwise → `BaseRef = se.Base` verbatim, `BaseKind = literal-ref`.
   The first matching entry in `stack.Branches` order wins, matching `GetBranch`
   (`internal/stack.go:64-72`). A stack-entry match is used even when the parent entry is archived or
   has `Repo != ""` — the *child's* `Repo` alone decides cross-repo (rule 1), exactly as today.
   Consequence, stated explicitly: when the child has `Repo == ""` and the matched parent entry has
   `Repo != ""`, the edge is still evaluated against `repoDir` and the parent's foreign `Repo` is
   **ignored, unreported, and never probed**. The symmetric case (child `Repo != ""`, parent
   `Repo == ""` or equal) is `cross-repo-unsupported` regardless of whether the two `Repo` values
   agree; §18.3 (`stack-ancestry-same-repo`) is the only place where "parent and child share the
   same `Repo`" acquires meaning. This feature emits **no** note for either asymmetry.
5. `P := rev-parse --verify --quiet --end-of-options <BaseRef>^{commit}`.
   Failure → `missing`, reason `base-ref-missing`.
6. `L`: `se.LastBaseSHA == ""` → `BaseRecord = absent`. Otherwise
   `rev-parse --verify --quiet --end-of-options <L>^{commit}` → `BaseRecord = present` with the
   peeled commit, or `BaseRecord = unresolvable`. Peeling normalises the tag-object SHAs the unpeeled
   writers record (`internal/stack.go:85-95`, `internal/checkout_sync.go:307-314`).

`missing` is reserved for exactly one thing: **a ref that does not resolve to a commit**. It is never
inferred from a failed `merge-base`.

### 4.3 Classification — first match wins

| # | Condition | Status | Reason |
|---|---|---|---|
| 1 | `merge-base --is-ancestor P C` exits 0 | `current` | `parent-contained` |
| 2 | `merge-base C P` exits 1 with empty stdout | `divergent` | `unrelated-histories` |
| 3 | `BaseRecord = unresolvable` | `stale` | `base-record-unresolvable` |
| 4 | `BaseRecord = present` and `merge-base --is-ancestor L P` exits 1 | `divergent` | `base-rewritten` |
| 5 | `BaseRecord = present` and that probe exits 0 | `stale` | `parent-advanced` |
| 6 | `BaseRecord = absent` | `stale` | `parent-advanced-no-base-record` |

Order is normative and load-bearing:

- Rule 1 precedes every `L` test. A parent **reset or force-moved backwards to a commit still inside
  `C`'s history** is `current` even though `L` is then outside `P`'s history: there is nothing to
  replay. Only a parent that moved to a commit *outside* `C`'s history can be non-current.
- Rule 2 precedes the `L` tests so unrelated histories get their own reason instead of the generic
  `base-rewritten`.
- Rule 3 precedes rule 4 so a pruned `L` never produces a rewrite claim.

`M` is **reporting only**. No classification arm reads it. It is populated as:

- rule 1: `M = P` without a probe (`P ⊆ C` ⇒ `merge-base(C,P) = P`);
- rule 2: `M` is nil (no merge base exists);
- rules 3-6: `M` is the stdout of the single `merge-base C P` probe;
- `missing`, cross-repo, not-evaluated: nil (not computed).

A non-0/1 exit from `is-ancestor L P` degrades `BaseRecord` to `unresolvable` and re-enters at rule
3. A non-0/1 exit from `is-ancestor P C` or from `merge-base C P` — only reachable when a ref is
deleted between resolution and probe — yields **not evaluated**, reason `ancestry-probe-failed`,
severity info, uncounted. It is never silently read as "no".

### 4.4 Relationship to what sync actually does

Both sync paths choose `--onto` on **SHA inequality**, not ancestry:
`entry.LastBaseSHA != "" && currentBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA`
(`internal/cli/sync_helpers.go:48-52`) and
`entry.LastBaseSHA != "" && entry.LastBaseSHA != entry.NewBaseSHA`
(`internal/checkout_sync.go:893-896`). A pure fast-forward with a recorded base therefore **already
triggers `--onto`** while this evaluator reports `stale`. `divergent` is the strict subset in which
`--onto` is not merely chosen but *required*, because plain rebase would replay commits the parent
already rewrote.

Guidance must respect that split. Doctor never claims "sync will use a plain rebase" for a `stale`
edge that has a base record.

### 4.5 Guidance strings (exact templates)

`<f>` = feature, `<n>` = entry name, `<b>` = `GitBranch()`, `<r>` = `BaseRef`, `<p>` = parent short
SHA, `<l>` = the rendered recorded base (§4.6).

| Reason | Guidance |
|---|---|
| `parent-contained` | *(empty)* |
| `parent-advanced` | ``parent `<r>` advanced to <p>; run: tws sync <f>`` |
| `parent-advanced-no-base-record` | ``parent `<r>` advanced to <p>; no recorded base commit for this branch, so sync uses a plain rebase — verify the parent history was not rewritten; run: tws sync <f>`` |
| `base-record-unresolvable` | ``recorded base commit <l> is not present in this repository; the replay strategy cannot be verified — inspect before running: tws sync <f>`` |
| `base-rewritten` | ``recorded base commit <l> is no longer in `<r>` history; repair is `git rebase --onto <r> <l>`, which tws sync selects automatically; run: tws sync <f>`` |
| `unrelated-histories` | ``` `<b>` and `<r>` share no common history; check the configured base — a rebase would replay every commit ``` |
| `child-ref-missing` (active) | ``git branch `<b>` does not exist; run: tws new <f> <n>`` |
| `child-ref-missing` (archived) | ``archived branch `<b>` has no git ref`` |
| `base-ref-missing` | ``base ref `<r>` does not exist; restore it or update `base` in stack.yaml`` |
| `cross-repo` | ``entry targets repository <se.Repo>; cross-repo ancestry is not evaluated`` |
| `base-unset` | ``no base configured for this entry; ancestry is not evaluated`` |
| `repo-unavailable` | ``source repository could not be determined; ancestry is not evaluated (<detail>)`` |
| `ancestry-probe-failed` | ``ancestry probe failed (<detail>); refs may have changed during evaluation — the recorded base was not consulted; re-run: tws doctor <f>`` |

The three "the base record could not be used" states must stay distinguishable at a glance and are
never merged:

- `parent-advanced-no-base-record` — **nothing was recorded** (`LastBaseSHA == ""`). Sync will plain
  rebase; the user is asked to verify.
- `base-record-unresolvable` — **something was recorded but is not in this repository**. Sync's
  `--onto` argument would not resolve; the user is asked to inspect first.
- `ancestry-probe-failed` — **the evaluation itself did not complete**; no claim is made about the
  recorded base at all, and the guidance says so.

`<detail>` is the first line of the underlying error, sanitized per §4.6 and trimmed to 200 runes.
Guidance never contains a newline.

### 4.6 Rendering of recorded and untrusted strings (normative)

`se.Base`, `se.LastBaseSHA`, `se.Repo`, and Git error text are workspace-supplied and are never
echoed raw:

- `<l>` when `BaseRecord = present` is `abbrev(LastBaseCommit)` — a 7-12 hex short SHA, never the
  recorded string.
- `<l>` when `BaseRecord = unresolvable` is `sanitize(se.LastBaseSHA)`, rendered **quoted** (`%q`).
- `sanitize(s)`: replace every rune that is a control character (including `\n`, `\r`, `\t`, ESC) or
  is non-printable per `unicode.IsPrint` with `?`, then truncate to 40 runes appending `…` when
  truncated. `<detail>` uses the same replacement with a 200-rune limit.
- `<r>`, `<b>`, `<n>`, and `<se.Repo>` are sanitized the same way before interpolation.

This is a display rule only; classification always uses the raw value.

## 5. Type contract

New file `internal/stack_ancestry.go`, package `internal`.

### 5.1 Enums

```go
type StackBaseKind string          // "stack-entry" | "literal-ref" | "none"
type StackBaseRecord string        // "absent" | "present" | "unresolvable"
type StackRepoSource string        // "workspace" | "worktree" | "inferred" | "unavailable"
type StackAncestryReason string
type StackNoteKind string
```

`StackAncestryReason` constants, exactly and only these thirteen:
`parent-contained`, `parent-advanced`, `parent-advanced-no-base-record`, `base-record-unresolvable`,
`base-rewritten`, `unrelated-histories`, `child-ref-missing`, `base-ref-missing`, `cross-repo`,
`base-unset`, `repo-unavailable`, `ancestry-probe-failed`, and the zero value `""` (used only by a
zero `StackEdge`, never returned).

`StackNoteKind` constants, exactly and only these **two**: `base-identity-remote-mismatch`,
`base-identity-literal-mismatch`. `repo-source-mismatch` is deliberately **not** a `StackNoteKind`:
it is a feature-level condition derived from `StackRepoResolution.Alternate` and can never be stored
on an edge (§9.3). Its label lives in one place, `const RepoSourceMismatchLabel = "repo-source-mismatch"`.

`AncestryStatus` is **unchanged**: the five values at `internal/checkout_health.go:143-152` and no
more. The zero value `""` means *not evaluated* (§6.3).

### 5.2 `StackEdge`

```go
type StackEdge struct {
    Feature   string `json:"feature"`
    Name      string `json:"name"`
    GitBranch string `json:"git_branch"`
    Archived  bool   `json:"archived"`
    Repo      string `json:"repo,omitempty"`      // se.Repo verbatim; "" = default repo

    BaseName string        `json:"base_name"`     // se.Base verbatim, may be ""
    BaseKind StackBaseKind `json:"base_kind"`
    BaseRef  string        `json:"base_ref,omitempty"` // ref actually probed

    ChildRef  string `json:"child_ref,omitempty"` // "refs/heads/<GitBranch()>" when probed
    RefExists bool   `json:"ref_exists"`          // peeled child resolution succeeded
    RefProbed bool   `json:"ref_probed"`          // a child-ref probe was actually issued

    LocalHead       string  `json:"local_head,omitempty"`        // full SHA
    LocalHeadShort  string  `json:"local_head_short,omitempty"`
    ParentHead      string  `json:"parent_head,omitempty"`       // full SHA
    ParentHeadShort string  `json:"parent_head_short,omitempty"`
    MergeBase       *string `json:"merge_base"`                  // nullable, full SHA
    MergeBaseShort  string  `json:"merge_base_short,omitempty"`  // set iff MergeBase != nil

    LastBaseSHA    string          `json:"last_base_sha,omitempty"`    // recorded, verbatim
    LastBaseCommit string          `json:"last_base_commit,omitempty"` // peeled; set iff present
    LastBaseShort  string          `json:"last_base_short,omitempty"`
    BaseRecord     StackBaseRecord `json:"base_record"`

    Status   AncestryStatus       `json:"status"`   // "" = not evaluated
    Reason   StackAncestryReason  `json:"reason"`
    Severity CheckoutSeverity     `json:"severity"`
    Guidance string               `json:"guidance,omitempty"`
    Notes    []StackEdgeNote      `json:"notes,omitempty"`

    RepoSource StackRepoSource `json:"repo_source"`
}

type StackEdgeNote struct {
    Kind   StackNoteKind `json:"kind"`
    Detail string        `json:"detail"`
}
```

Normative field rules:

1. **One edge per `stack.Branches` element, in file order**, including archived, cross-repo,
   base-less, and unresolvable entries. Never fewer, never reordered, never deduplicated.
2. `MergeBase` is the **only** pointer field. Non-nil exactly when a merge base is known (§4.3);
   nil otherwise. `unrelated-histories` is the only *classified* state with nil `MergeBase`.
3. Every other absent value is the empty string / `false`. No sentinel strings such as `"none"` or
   `"unknown"` are ever written into a SHA field.
4. `Status == ""` ⇔ no Git ancestry conclusion exists ⇔ `Reason ∈ {base-unset, repo-unavailable,
   ancestry-probe-failed}`. Consumers must branch on `Status == ""` before reading heads.
5. `RefProbed` is true only when a child-ref probe was issued; it is false for cross-repo,
   `base-unset`, and `repo-unavailable` edges. `RefExists` is meaningful only when `RefProbed`.
6. `Severity` is always one of `ok`, `info`, `warning`; **never `error`** (§8.1).
7. `Reason` is always set on a returned edge; `Guidance` is empty only for `parent-contained`.
8. `Notes` never influences `Status`, `Reason`, `Severity`, or any count. `Notes` holds only the two
   `StackNoteKind` values of §5.1; a `repo-source-mismatch` entry in `Notes` is a bug (AC 51).
9. The JSON tags exist for `stack-status` only. **Nothing in this feature encodes a `StackEdge`**;
   no command gains JSON output. `stack-status` owns and may still refine that contract.

### 5.3 Repository resolution result

```go
type StackRepoResolution struct {
    RepoDir   string          // canonical main repo root; "" when unavailable
    Source    StackRepoSource
    Alternate string          // canonical main repo root of a validated, different candidate; "" when none
    Reason    string          // human detail; non-empty iff RepoDir == ""
}
```

`RepoDir` and `Alternate` are **always** `canonicalize(MainRepoRootIn(candidate))` — the canonical
**main repository root**, never a linked-worktree path and never an unresolved candidate path
(§6.2). Comparing them is therefore a plain string comparison of canonical main roots.

## 6. Evaluator API

### 6.1 Core (mode-independent)

```go
var ErrRepoUnavailable = errors.New("stack ancestry: source repository unavailable")

func EvaluateStackAncestry(repoDir, feature string, stack Stack) ([]StackEdge, error)
func EvaluateStackEdge(repoDir, feature string, se StackEntry, stack Stack) (StackEdge, error)
func UnevaluatedStackEdges(feature string, stack Stack, reason StackAncestryReason, detail string) []StackEdge
```

- `EvaluateStackAncestry` **refuses** an empty or unvalidated `repoDir` and returns
  `fmt.Errorf("%w: %s", ErrRepoUnavailable, detail)` with **nil** edges **before issuing any Git
  process other than the single validation probe**. Validation is `repoDir != ""` **and**
  `MainRepoRootIn(repoDir)` succeeding, performed once per evaluation. There is no `git -C ""`
  anywhere in this feature: an empty `-C` silently means the process working directory, which would
  make results depend on where the user is standing.
- `EvaluateStackEdge` is the single-edge form used by tests and future consumers; it allocates a
  fresh cache and applies the same validation.
- Errors are reserved for **evaluation-wide preconditions only** (bad `repoDir`). No per-edge
  condition ever produces an error: a missing ref, a pruned `L`, unrelated histories, a cross-repo
  entry, and a mid-run ref deletion are all *results*, expressed through `Status`/`Reason`.
- `UnevaluatedStackEdges` issues **zero** Git processes and returns one edge per entry with
  `RepoSource = unavailable`, `RefProbed = false`, `MergeBase = nil`, `Severity = info`, and the
  given reason — except that `se.Repo != ""` entries still get `cross-repo-unsupported` (a
  metadata-only decision) and `se.Base == ""` entries still get reason `base-unset`, so the same
  entry never changes meaning because the repository happened to be unresolvable.

### 6.2 Mode-aware adapter

```go
func ResolveStackAncestryRepo(ws Workspace, cfg Config, featurePath string, stack Stack) StackRepoResolution
func FeatureStackEdges(ws Workspace, cfg Config, feature, featurePath string, stack Stack) ([]StackEdge, StackRepoResolution)
```

`ResolveStackAncestryRepo` is the **only** mode-aware code in this feature.

**Candidate normalisation (normative, applies to every candidate without exception).** A candidate is
a filesystem path. It is accepted only through:

```go
func ancestryRepoCandidate(path string) (string, bool) {
    if path == "" { return "", false }
    root, err := MainRepoRootIn(path)   // one `git -C <path> rev-parse --git-common-dir`
    if err != nil { return "", false }
    return canonicalize(root), true      // canonicalize: internal/workspace.go:205
}
```

The value stored in `RepoDir` (and in `Alternate`) is **always** the returned canonical main
repository root — never the raw candidate path. This matters for candidate 1: a feature worktree is a
**linked worktree**, so `MainRepoRootIn(<worktree>)` returns the *main* repository root, which is
exactly what `ws.RepoRoot` normalises to in a healthy setup. Comparing raw paths would report a
`repo-source-mismatch` for every ordinary external workspace; comparing canonical main roots reports
it only when the workspace genuinely points at a different repository. Probing the main root instead
of the worktree is ref-identical (`refs/heads/*` are shared between a linked worktree and its main
repository) and this feature reads nothing that depends on `HEAD` or the working tree.

Candidate order:

- **Checkout mode**: `ancestryRepoCandidate(ws.RepoRoot)`; `ws.RepoRoot` is non-empty and
  repo-derived (`buildHeader` would have failed otherwise) → `Source = workspace`. No inference walk,
  no worktree scan, and **no `Alternate` is ever computed in checkout mode** (§9.3).
- **External mode**, first candidate that yields a canonical root:
  1. **Feature-scoped worktree evidence** — `ancestryRepoCandidate(filepath.Join(featurePath,
     "worktrees", e.Name))` for each `e` in `stack.Branches` order with `e.Repo == ""` and
     `!e.Archived` → `Source = worktree`. This is the same evidence `inferExternalRepoRoot` already
     trusts (`internal/workspace.go:360-379`) and the only source that is correct when `TWS_ROOT`
     points at a metadata root belonging to a different repository.
  2. `ancestryRepoCandidate(ws.RepoRoot)` when `ws.RepoRoot != ""` → `Source = workspace`.
  3. `inferExternalRepoRoot(metadataRoot, cfg)` where `metadataRoot = ws.MetadataRoot` when non-empty
     else `filepath.Dir(featurePath)`, then `canonicalize` of its result (it already returns a
     canonical main root) → `Source = inferred`.
  4. Otherwise `RepoDir = ""`, `Source = unavailable`, `Reason` = the `inferExternalRepoRoot` error
     text when it produced one (it distinguishes "multiple default repositories" from "cannot
     determine"), else `"no source repository for feature <f>"`.
- When candidate 1 wins, candidate 2 is still evaluated **once** and, if it yields a canonical root
  **different from the chosen one**, that root is recorded in `Alternate`. Equal roots — the normal
  worktree case — leave `Alternate` empty and produce **no** note, **no** issue, and no extra output.
  Candidate 3 is never evaluated when candidates 1 or 2 succeeded, and never contributes `Alternate`.

`FeatureStackEdges` is the glue every consumer uses:

```go
res := ResolveStackAncestryRepo(ws, cfg, featurePath, stack)
if res.RepoDir == "" {
    return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, res.Reason), res
}
edges, err := EvaluateStackAncestry(res.RepoDir, feature, stack)
if err != nil {
    return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, err.Error()), StackRepoResolution{Source: StackRepoUnavailable, Reason: err.Error()}
}
// stamp res.Source onto every edge
```

`EvaluateStackAncestry` itself never sets `RepoSource` to anything but `""`; `FeatureStackEdges`
stamps it. A direct caller of the core evaluator therefore cannot get a misleading source label.

**`cfg` is threaded, never re-loaded by the ancestry evaluator.** `ResolveStackAncestryRepo` and
`FeatureStackEdges` take `cfg Config` by value and **never** call `LoadConfig()`; `cfg` is read only
by candidate 3 (`inferExternalRepoRoot`). This is load-bearing: `LoadConfig` → `repoConfigPath` →
`RepoRoot` runs `git rev-parse --show-toplevel` **in the process working directory**
(`internal/config.go:35-40,60-62`), so loading it per feature or per edge would make the number of
cwd-dependent Git processes grow with the number of features and stack entries.

The enforceable guarantees are structural:

- `internal/stack_ancestry.go` contains **no** `LoadConfig` reference at all;
- each exported checkout builder (`BuildCheckoutHealthReport`, `BuildCheckoutList`) may add **at
  most one** ancestry config load in its own body and thread it down (§14.2);
- `doctorCmd` may add **at most one** ancestry config load in its own body and thread `(ws, cfg)` into every
  `checkFeatureE` call; `checkFeatureE` never loads it (§14.4);
- no `LoadConfig` call appears in `checkFeatureE`, a feature loop, or an edge loop.

No command-wide Git-process-count guarantee follows from these structural constraints. Refactoring
or deleting pre-existing config/workspace resolution is out of scope (§3).

### 6.3 Representing "evaluation unavailable" (decided)

`AncestryStatus` keeps **exactly five** values. Unavailable evaluation is `Status == ""` plus a
`Reason`, never `cross-repo-unsupported` (which would assert a cross-repo fact that is false) and
never a sixth enum value.

Reconciliation with the request text: the request lists "current, stale, divergent, missing, or
cross-repo" as the classification vocabulary and separately asks for actionable reporting. An
unevaluated edge makes **no** classification claim, so it is correctly outside that vocabulary; it is
still reported, with reason and guidance. `CheckoutFeatureEntry.AncestryStatus` has no `omitempty`,
so the empty value is representable in the existing struct without a schema change.

Human rendering never prints an empty token, and the token exists in **exactly one place**:

```go
// stack_ancestry.go — the single source of the display token.
const ancestryUnevaluatedToken = "unevaluated"

func ancestryDisplayStatus(s AncestryStatus) string {
    if s == "" {
        return ancestryUnevaluatedToken
    }
    return string(s)
}
```

`ancestryDisplayStatus` is the **only** producer of the `unevaluated` word and has exactly three call
sites: `FormatCheckoutHealth` (§10.1), `FormatCheckoutList` (§10.2), and `AncestryHealthIssues`
(§10.3). `unevaluated` is a **display token only** — it is not an `AncestryStatus` constant, never
appears in a Go comparison, and is never written into `CheckoutFeatureEntry.AncestryStatus`,
`CheckoutListEntry.AncestryStatus`, or `StackEdge.Status`. AC 47 pins both the literal count (one)
and the call-site count (three).

## 7. Git commands, safety, cache, and bounds

### 7.1 The complete command inventory

Every Git process this feature starts goes through **one** unexported runner:

```go
func ancestryGit(repoDir string, args ...string) *exec.Cmd {
    cmd := exec.Command("git", args...)
    cmd.Dir = repoDir   // non-empty and pre-validated; never `-C`, never ""
    cmd.Stderr = nil
    return cmd
}
```

`cmd.Dir` (not `-C`) is used deliberately so the reused `gitIsAncestor`
(`internal/checkout_sync.go:365-377`), which already sets `cmd.Dir = repoDir`, is invocation-shaped
identically to the new probes and can be called unchanged. Consequence: **ancestry invocations carry
no `-C` argument at all**; the "never `git -C \"\"`" guarantee becomes "`cmd.Dir` is always the
validated, non-empty `repoDir`", which the AC 41 shim verifies by recording each invocation's working
directory.

Only these five forms are ever issued by this feature:

| Purpose | Command (argv after `git`) | Runner | Accepted exits |
|---|---|---|---|
| repo validation | `-C <path> rev-parse --git-common-dir` (via `MainRepoRootIn`, `internal/exec.go:27`) | reused helper, uses `-C` | 0 = valid; any other = unavailable |
| resolve/peel | `rev-parse --verify --quiet --end-of-options <ref>^{commit}` | `ancestryGit` | 0 = SHA; any other = unresolved |
| abbreviate | `rev-parse --short <full-sha>` | `ancestryGit` | 0 = short; any other = fall back to `full[:12]` |
| ancestry | `merge-base --is-ancestor <sha-a> <sha-b>` (via existing `gitIsAncestor`) | `cmd.Dir`, same shape | 0 = yes; 1 = no; other = error |
| merge base | `merge-base <sha-a> <sha-b>` | `ancestryGit` | 0 = SHA; 1 + empty stdout = none; other = error |

`MainRepoRootIn` is the single `-C`-shaped exception; it is a reused, unchanged helper, it takes no
ref, and it is only ever called with a non-empty path (`ancestryRepoCandidate` returns early on
`""`). `DefaultBranchIn` (§9.1) is likewise reused unchanged and is `-C`-shaped.

Safety rules, all normative:

1. **`--end-of-options` is mandatory** on every command that receives a user-controlled string
   (`se.Base`, `se.LastBaseSHA`, `GitBranch()`). Verified supported.
2. `merge-base` and `--is-ancestor` receive **only already-peeled full SHAs**, never user strings, so
   argument injection is structurally impossible there. This is why the existing
   `gitIsAncestor` can be reused unchanged: it already returns `(false, nil)` only for exit 1 and an
   error for anything else.
3. Branch identity always goes through `refs/heads/<GitBranch()>`; a bare name is never used for a
   stack branch. Ambiguity between a branch and a same-named tag is therefore impossible.
4. Only the literal-ref arm passes an unqualified user string, and only to the peeling resolver.
5. **stderr is discarded** on every probe (`cmd.Output()` / explicit `Stderr = nil`), so
   `warning: refname 'x' is ambiguous` never leaks into doctor output.
6. No `-c`, no `-c core.*`, no environment mutation, no `--work-tree`, no `--git-dir`. The child
   process environment is inherited unchanged, exactly like the existing helpers.
7. No `fetch`, no `ls-remote`, no `--fork-point`, no reflog read, no `git status`, no index refresh,
   no write of any kind. No probe is ever executed against `se.Repo` or any path outside `repoDir`.
8. Only SHAs, ref names, and the already-recorded `se.Repo` string are printed, all sanitized per
   §4.6. No remote URLs, no credentials, no commit messages, no author data.

**Non-ancestry invocation shapes are out of this inventory.** Doctor and list also start Git
processes outside `internal/stack_ancestry.go`. The following shape set is used by AC 41 only to
separate those processes from ancestry Git probes:

| Form (argv after `git`, ignoring a leading `-C <dir>`) | Caller |
|---|---|
| `rev-parse --abbrev-ref HEAD` | `healthCurrentBranch` (`internal/checkout_health.go:320`), `CheckWorktreeBranch` (`internal/health.go:26`) |
| `rev-parse --short HEAD` | `healthCurrentBranch` detached arm |
| `status --porcelain` | `gitDirty` (`internal/checkout_health.go:337`), `CheckWorktreeDirty` (`internal/health.go:48`) |
| `rev-parse --show-toplevel` | `RepoRoot` via `LoadConfig`/`repoConfigPath` (`internal/config.go:35-40`) |
| `rev-parse --git-common-dir` | `MainRepoRootIn` (also used by this feature) |
| `rev-parse --abbrev-ref origin/HEAD`, `symbolic-ref --short HEAD` | `DefaultBranchIn` (`internal/exec.go:69-88`) |

The `--end-of-options` requirement, the forbidden-verb list, and the `cmd.Dir` requirement of AC 41
apply **only** to ancestry Git probes issued through `ancestryGit` or `gitIsAncestor`. The shape set
is not an assertion about call site, occurrence, or count; it includes repository/config helpers and
the deliberate builder/doctor config loads allowed by §6.2. `MainRepoRootIn` and `DefaultBranchIn`
are accounted for separately by their exact shapes.

### 7.2 Cache

One cache per evaluation, held in an unexported `ancestryEvaluator` struct, never global, never
persisted, never shared between calls:

- `refs map[string]refResolution` keyed by the exact ref string passed to the resolver, storing
  `{full, short string; ok bool}`. **Negative results are cached** so a repeated missing base costs
  one process, not N.
- `shorts map[string]string` keyed by full SHA.
- `defaultBranch` resolved at most once, lazily, only when §9.1 needs it; the resolved value
  (including the failure fallback) is memoised so `DefaultBranchIn` runs at most once per evaluation.
- `merge-base` / `--is-ancestor` results are **not** cached (each edge is a distinct pair).

Because the cache is per call, two evaluations never observe each other, and concurrent use of
separate evaluators is safe. The evaluator holds no mutex and no package-level state.

### 7.3 Performance bounds

All figures below are **absolute upper bounds on Git processes**, stated without reference to any
pre-feature behaviour.

Per evaluation of a stack with `E` entries:

| Stage | Bound | Note |
|---|---|---|
| repository resolution (§6.2) | `≤ E + 2` | one `MainRepoRootIn` per worktree candidate tried (at most one per active default-repo entry) + one for `ws.RepoRoot` + at most one `inferExternalRepoRoot`, which itself is a pre-existing helper with its own internal cost |
| `EvaluateStackAncestry` validation | `1` | one `MainRepoRootIn` |
| default-branch resolution | `≤ 2` | `DefaultBranchIn` issues `rev-parse --abbrev-ref origin/HEAD` and, only when that fails, `symbolic-ref --short HEAD` (`internal/exec.go:69-88`); memoised, so at most one *resolution* but up to **two processes** |
| per edge | `≤ 10` | ≤3 uncached resolutions (`C`, `P`, `L`) + ≤3 abbreviations (`C`, `P`, and `M` when it is a new SHA; under rule 1 `M = P` is a cache hit) + ≤3 ancestry probes (`P⊆C`, `merge-base`, `L⊆P`) + ≤1 alternate-identity resolution (§9) |

Total for the core evaluator: **`≤ 10·E + 3`** processes (validation + 2 default-branch probes),
plus the resolution stage above. Config loading is **not** counted here: the ancestry code issues no
`LoadConfig` of its own (§6.2). Builder and command-level config loading is governed by the static
placement constraints in §6.2 and AC 53, not by a command-wide Git-process count.

Cache-derived guarantees, which are what the tests assert:

- a parent ref shared by `k` edges is resolved **once**, not `k` times;
- an abbreviation of the same full SHA is computed **once**;
- an unresolvable base ref shared by `k` edges costs **one** process;
- cross-repo, `base-unset`, and `repo-unavailable` edges cost **zero** processes each.

Realistically a linear stack of `E` entries sharing one base costs `≈ 4·E` processes. **No test
compares this feature's process count against the deleted classifiers' cost**; only the fixed bounds
and the caching guarantees above are asserted (AC 43).

## 8. Severity, counting, and exit semantics

### 8.1 Severity table (decided, both modes)

| State | Active entry | Archived entry |
|---|---|---|
| `current` | `ok` | `ok` |
| `stale` (any reason) | `warning` | `info` |
| `divergent` (any reason) | `warning` | `info` |
| `missing` (child or base ref) | `warning` | `info` |
| `cross-repo-unsupported` | `info` | `info` |
| not evaluated (`""`) | `info` | `info` |

`divergent` is a **warning, never an error**, in both modes — including `unrelated-histories`.
Nothing in this feature can produce `SeverityError`, so `CheckoutHealthReport.HasErrors()`
(`internal/checkout_health.go:188-208`) is unreachable from ancestry and both doctors keep exit 0 for
every ancestry finding.

Ancestry is **always computed and always reported** when refs resolve, archived or not: the data is
identical, only the severity differs. This aligns checkout doctor with the existing
`IssueRefMissingArchived` / `IssueRefMissing` split in `tws status`
(`internal/agent_status.go:1384-1398`).

### 8.2 Counting

- **Checkout**: `countIssues` (`internal/checkout_health.go:755-782`) is unchanged — it already
  counts only `warning`/`error` feature entries. Archived and informational ancestry therefore never
  reach the count, and `Issues` drops for workspaces containing archived entries with missing refs
  (previously `warning`). This is the intended D7-aligned correction.
- **External**: `checkFeatureE` returns `internal.CountHealthIssues(issues)` — warnings and errors
  only — instead of `len(issues)`. Info issues print and never change
  `healthy (N active worktree(s))` / `N issue(s)`, never change the returned total, never change the
  exit status.
- `HealthIssue.Severity` is additive with a **zero-value rule**: `""` is treated as `warning`
  everywhere (`EffectiveSeverity()`), so every existing producer (`CheckWorktreeBranch`,
  `CheckWorktreeDirty`, `CheckWorktreeInjectLinks`) renders and counts exactly as today with no edit.

### 8.3 Exit semantics

- `runCheckoutDoctor` (`internal/cli/doctor.go:107-122`) still returns an error only on
  `HasErrors()`; unchanged code.
- External `doctorCmd` still returns `nil` and prints a total; unchanged control flow.
- A `stale`, `divergent`, `missing`, cross-repo, or unevaluated edge exits **0** in both modes.

## 9. Base identity: authority and mismatch reporting (D1, decided)

**The literal recorded base is authoritative.** The evaluator probes `BaseRef` as defined in §4.2 and
classifies from it. It never substitutes `origin/<base>`, never consults the network, and never
mutates `stack.yaml`. This keeps evaluation deterministic and offline.

Because both sync paths can target a *different* ref than the one doctor probes, the evaluator
surfaces the disagreement as an informational note instead of silently picking a side. Edge notes are
computed only when the edge reached a classification (`Status != ""`, not cross-repo).

### 9.1 `base-identity-remote-mismatch` (edge note)

Mirrors external sync's `resolveBase`
(`internal/cli/sync_helpers.go:186-192`), which maps `base == DefaultBranch()` to
`origin/<default>` and records **that** SHA into `LastBaseSHA`. Emitted when
`BaseKind == literal-ref` **and** `BaseName == DefaultBranchIn(repoDir)` **and**
`refs/remotes/origin/<BaseName>^{commit}` resolves **and** differs from `P`.
Detail: ``base "<BaseName>" is probed as <P-short>, but tws sync resolves it as origin/<BaseName> (<alt-short>)``.
The default branch is resolved at most once per evaluation and only when at least one literal-ref
base exists; that single resolution may cost up to two Git processes (§7.3).

### 9.2 `base-identity-literal-mismatch` (edge note)

Mirrors the checkout-sync planning gap
(`internal/checkout_sync.go:452-466,891-897`), which resolves `entry.Base` literally instead of
through `parent.GitBranch()`. Emitted when `BaseKind == stack-entry` **and** the parent's
`GitBranch()` differs from `BaseName` **and** the bare `BaseName` also resolves to a commit that
differs from `P`.
Detail: ``base name "<BaseName>" also resolves as a literal ref to <alt-short>, which differs from parent branch "<parent GitBranch>" (<P-short>); checkout sync resolves the literal name``.

These two are the **only** members of `StackEdgeNote`. Both are info, never counted, never change
`Status`/`Severity`, and are the whole of this feature's per-edge response to the mismatch. The actual
sync fix is out of scope (N4, §18.2).

### 9.3 `repo-source-mismatch` is feature-level, never an edge note

`repo-source-mismatch` is **derived, not stored**. It is a pure function of one field:

```
mismatch(res) ⇔ res.Alternate != ""
```

Normative consequences:

- It is **never** written into `StackEdge.Notes`, never given a `StackNoteKind`, and never duplicated
  per edge. `StackEdge` has no field that can carry it (§5.1, §5.2 rule 8).
- Only **external** doctor emits it, and emits **exactly one** `HealthIssue` per feature:
  `Branch = "stack"`, `Problem = RepoSourceMismatchLabel + ": ancestry evaluated against <RepoDir>
  (source: worktree)"`, `Hint = "the workspace also resolves to <Alternate>; check TWS_ROOT or the
  configured workspace path"`, `Severity = SeverityInfo` (§10.3).
- **Checkout doctor and checkout list cannot emit it at all**: checkout mode never evaluates a second
  candidate, so `Alternate` is always `""` there (§6.2), and neither formatter reads `Alternate`. No
  checkout output can contain the string `repo-source-mismatch` (AC 51).
- `Alternate != ""` changes no status, no severity, and no count in either mode.
- It fires only when the two canonical **main repository roots** differ. An ordinary external
  workspace whose feature worktrees are linked worktrees of `ws.RepoRoot` normalises to one root and
  produces nothing (§6.2, AC 52).

## 10. Output integration

### 10.1 Checkout doctor (`FormatCheckoutHealth`)

The existing entry line keeps **every existing token, in the existing order, with no inline
suffix**:

```
  [!] auth/api (git: jd-api) base=main ancestry=stale [current] head=1a2b3c4 parent=9f8e7d6
```

`ancestry=%s` prints `ancestryDisplayStatus(f.AncestryStatus)` (§6.3) — the status verbatim, or
`unevaluated` when it is empty. Tag rules:

- `[archived]`, `[current]` — unchanged;
- `[ref-missing]` is emitted when `!f.RefExists` **and** the edge was ref-probed
  (`f.AncestryStatus` is neither `cross-repo-unsupported` nor `""`). This removes a latent
  regression: cross-repo entries must not gain a `[ref-missing]` tag from a probe that never ran.

Guidance is **not** appended to that line. When present, additive **indented detail lines** follow it
at six spaces, matching the existing sync/session guidance style
(`internal/checkout_health.go:819-821,840-842`), in this exact order and at most three lines:

```
  [!] auth/api (git: jd-api) base=main ancestry=divergent head=1a2b3c4 parent=9f8e7d6
      reason: base-rewritten last-base=5c6d7e8 merge-base=3f2e1d0
      recorded base commit 5c6d7e8 is no longer in `refs/heads/jd-main` history; repair is `git rebase --onto refs/heads/jd-main 5c6d7e8`, which tws sync selects automatically; run: tws sync auth
      note: base name "main" also resolves as a literal ref to 7a7a7a7, …
```

- **reason line** — emitted when `AncestryStatus != current`. Format:
  `reason: <reason>` plus ` last-base=<short>` when a base record exists or was recorded,
  plus ` merge-base=<short>` when `MergeBase != nil`, plus ` base-record=<absent|unresolvable>` when
  the record is not `present`.
- **guidance line** — emitted when guidance is non-empty.
- **note line(s)** — one `note: <detail>` per `StackEdgeNote`, emitted for any status including
  `current`. `Notes` holds only the two edge-note kinds; `repo-source-mismatch` can never appear here
  (§9.3).

A `current` edge with no notes therefore produces exactly one line, as today. The compatibility claim
is precisely: *existing tokens on the existing line are unchanged and no token is removed*; an entry
block may gain up to three indented lines.

#### 10.1.1 `StackEdge` → `CheckoutFeatureEntry`: the exact, total map

`CheckoutFeatureEntry` (`internal/checkout_health.go:154-167`) keeps every existing field name, type,
JSON key, and position. New fields are appended after `Severity`. **Every** field is assigned exactly
as follows and nowhere else:

| `CheckoutFeatureEntry` field | Kind | Source | Rendering |
|---|---|---|---|
| `Feature`, `Name`, `GitBranch`, `Archived` | existing | `edge.Feature`, `edge.Name`, `edge.GitBranch`, `edge.Archived` | unchanged |
| `RefExists` | existing | `edge.RefExists` (false whenever `!edge.RefProbed`) | unchanged |
| `Current` | existing | computed by `buildOneFeatureEntry` from `currentBranch`/session, **not** from the edge | unchanged |
| `BaseName` | existing | `edge.BaseName` (= `se.Base` verbatim) | unchanged |
| `BaseGitBranch` | existing | **bare** name: the matched parent entry's `GitBranch()` for `BaseKind == stack-entry`, else `se.Base` verbatim. **No `refs/heads/` prefix, ever** — byte-identical to today's `baseGitBranch` (`internal/checkout_health.go:609-620`) | unchanged |
| `AncestryStatus` | existing | `edge.Status` (may be `""`) | display via `ancestryDisplayStatus` |
| `LocalHead` | existing | **`edge.LocalHeadShort`** — the abbreviated child head | printed as `head=<short>`; `""` suppresses the token, exactly as today |
| `ParentHead` | existing | **`edge.ParentHeadShort`** — the abbreviated parent head | printed as `parent=<short>`; `""` suppresses the token |
| `Severity` | existing | `edge.Severity` | unchanged |
| `LocalHeadFull` | **new** | `edge.LocalHead` (full 40-hex) | never printed by this feature |
| `ParentHeadFull` | **new** | `edge.ParentHead` (full 40-hex) | never printed by this feature |
| `BaseKind` | **new** | `edge.BaseKind` | never printed |
| `BaseRef` | **new** | `edge.BaseRef` — the **fully qualified** ref actually probed (`refs/heads/<parent GitBranch()>` for stack-entry bases, the literal string otherwise) | appears only inside guidance text as `<r>` |
| `LastBaseSHA` | **new** | `edge.LastBaseSHA` (recorded, verbatim) | never printed raw; §4.6 governs display |
| `LastBaseShort` | **new** | `edge.LastBaseShort` | printed as `last-base=<short>` on the reason line |
| `BaseRecord` | **new** | `edge.BaseRecord` | printed as `base-record=<absent\|unresolvable>` when not `present` |
| `MergeBase *string` | **new** | `edge.MergeBase` (full SHA, nullable) | never printed |
| `MergeBaseShort` | **new** | `edge.MergeBaseShort` — the evaluator's abbreviation of `*MergeBase`; `""` when `MergeBase == nil` | the `merge-base=<short>` token actually printed |
| `Reason` | **new** | `edge.Reason` | printed as `reason: <reason>` |
| `Guidance` | **new** | `edge.Guidance` | the guidance detail line |
| `Notes` | **new** | `edge.Notes` | one `note: <detail>` line each |
| `RepoSource` | **new** | `edge.RepoSource` | never printed |

Pinned rules, in force for both the entry line and the detail lines:

1. **Every SHA token printed by any formatter is the abbreviated form** — `head=`, `parent=`,
   `last-base=`, `merge-base=`, and every `<p>`/`<l>`/`<alt-short>` inside guidance and notes. The
   full-SHA fields (`LocalHeadFull`, `ParentHeadFull`, `LastBaseSHA`, `*MergeBase`) exist for
   `stack-status` and are never rendered by this feature.
2. Abbreviation is `rev-parse --short <full-sha>`, falling back to `full[:12]` (§7.1). The two
   pre-existing tokens `head=`/`parent=` therefore keep exactly their current shape and width, since
   today's `gitShortSHA` used the same `rev-parse --short`.
3. `LocalHead`/`ParentHead` keep their existing names, types, JSON keys, and short-SHA semantics;
   the full SHAs go to the **new** `*Full` fields. No existing key changes meaning.
4. `BaseGitBranch` (existing) and `BaseRef` (new) are **not** interchangeable: `BaseGitBranch` stays
   bare for compatibility with any consumer of today's JSON shape, `BaseRef` carries the qualified
   ref the probe actually used.

These fields exist to render §10.1 and to feed `stack-status`; **no command serializes them in this
feature**.

### 10.2 Checkout list (`BuildCheckoutList` / `FormatCheckoutList`)

`BuildCheckoutList` drops its private classifier (`internal/checkout_health.go:959-981`) and reads
`FeatureStackEdges`. `CheckoutListEntry` is **unchanged** (no new fields): it keeps
`AncestryStatus string`, which now can also hold `divergent` (previously unreachable there) and `""`.
`FormatCheckoutList` (`internal/checkout_health.go:1036-1038`) currently suppresses the tag for `""`;
it is changed to render ` [unevaluated]` — produced by `ancestryDisplayStatus` (§6.3), not by a local
literal — so a non-evaluated edge is never displayed as healthy:

```
auth
  ├── api (git: jd-api) * [divergent]
  └── db [archived] [stale]
```

The tag is suppressed only for `current`; every other value, including the display token, is shown.
Doctor and list now derive from one function and cannot disagree.

### 10.3 External doctor

`HealthIssue` (`internal/health.go:11-15`, `String()` at `internal/health.go:17-23`) gains one
additive field:

```go
type HealthIssue struct {
    Branch   string
    Problem  string
    Hint     string
    Severity CheckoutSeverity // "" == warning (zero-value rule, §8.2)
}
```

`String()` renders `  %s %s: %s` with `severityIcon(EffectiveSeverity())` — `[!]` for the zero value
and for warnings (byte-identical to today for every existing producer), `[i]` for info — and keeps
the six-space hint continuation line.

`AncestryHealthIssues(res StackRepoResolution, edges []StackEdge) []HealthIssue` maps edges to
issues:

- `current` → no issue;
- everything else → one issue with `Branch = edge.Name`,
  `Problem = "ancestry " + ancestryDisplayStatus(edge.Status) + ": " + string(edge.Reason)`
  (§6.3 — the `unevaluated` word is never written literally here), `Hint = edge.Guidance`,
  `Severity = edge.Severity`;
- **collapsing rule**: at most **one** `repo-unavailable` issue per feature (feature-scoped, not one
  per entry), with `Branch = "stack"`, so an unresolvable repository cannot flood the output;
- **exactly one** additional info issue when `res.Alternate != ""`, built from
  `RepoSourceMismatchLabel` per §9.3. It is emitted once per feature, is derived solely from `res`,
  and no `edge.Notes` entry participates. This is the **only** place in the codebase that can emit
  `repo-source-mismatch`; checkout doctor and checkout list cannot (§9.3, AC 51).

`checkFeatureE` becomes `checkFeatureE(ws internal.Workspace, cfg internal.Config, feature string) (int, error)`:

1. `internal.RequireFeaturePath(feature)` — **unchanged, first, and still the source of the existing
   error text and ordering**;
2. the existing `os.Stat` "not found" short-circuit — unchanged;
3. `issues := internal.CheckFeatureHealth(featurePath)` — unchanged call, unchanged function;
4. `stack, err := internal.LoadStack(featurePath)`; when it loads and has entries,
   `edges, res := internal.FeatureStackEdges(ws, cfg, feature, featurePath, stack)` and
   `issues = append(issues, internal.AncestryHealthIssues(res, edges)...)`;
   a `LoadStack` failure adds nothing and is not fatal (matching `CheckFeatureHealth`);
5. `counted := internal.CountHealthIssues(issues)`;
6. print `healthy (N active worktree(s))` when `counted == 0` (then still print any info issues
   below it), else `%s: %d issue(s)` with `counted`, then all issues in order;
7. return `counted, nil`.

`doctorCmd` keeps its single `internal.RequireWorkspace()` call, keeps `wsErr` non-fatal for the
external path (the zero `Workspace` simply makes candidate 2 fail), may add at most one
`internal.LoadConfig()` call in its own body, before the feature loop, and passes both values into every
`checkFeatureE` call (`internal/cli/doctor.go:40,56`). `checkFeatureE` itself never calls
`LoadConfig`; no config load is added inside either feature loop or any edge loop (§6.2).

`CheckFeatureHealth(featurePath)` keeps its exact signature and behaviour so any other caller stays
untouched; ancestry is composed by the CLI layer, not injected into it.

### 10.4 Additive guidance lines — summary

| Surface | Guidance carrier | New lines |
|---|---|---|
| checkout doctor | indented 6-space detail lines under the entry | ≤3 per entry |
| checkout list | tag only | 0 |
| external doctor | `HealthIssue.Hint` (existing 6-space continuation) | ≤1 issue per edge + ≤2 feature-scoped info (`repo-unavailable`, `repo-source-mismatch`) |

## 11. `RefExists` consistency and the cross-repo / missing-head deltas

`CheckoutFeatureEntry.RefExists` (`internal/checkout_health.go:599`) is populated from the
evaluator's peeled child resolution (`refs/heads/<GitBranch()>^{commit}`), so **one probe backs both
the flag and the classification** and they can never disagree.

Behaviour differences versus `gitRefExists`, both intended:
(a) a branch/tag name collision now resolves to the branch; (b) a 40-hex *branch name* would now be
resolved as a branch rather than as an abbreviated object — tws never creates such a branch.

Two further, deliberate field-level deltas follow from moving the probe into the evaluator. Both are
documented here and re-stated in §16 because they are observable in the JSON shape of
`CheckoutFeatureEntry`:

1. **Cross-repo entries lose a misleading `RefExists`.** Today `buildOneFeatureEntry` sets
   `e.RefExists = gitRefExists(ws.RepoRoot, gitBranch)` at line 599, *before* the cross-repo
   short-circuit at lines 622-627 — so a cross-repo entry reports `ref_exists: true` whenever a
   same-named branch happens to exist in the **local** repository, which says nothing about the
   foreign repo. After this feature, cross-repo edges have `RefProbed = false` and therefore
   `RefExists = false`, and no probe is issued (§4.2 rule 1, N5). Rendered output is unchanged
   because `[ref-missing]` is gated on `RefProbed` (§10.1); only the struct field changes. The same
   applies to `base-unset` and `repo-unavailable` edges, which previously were probed and now are
   not.
2. **`missing` edges caused by a missing *base* ref now report a head.** Today both classifiers
   return before `gitShortSHA`, so a `base-ref-missing` entry prints no `head=` token. The evaluator
   resolves `C` before `P` (§4.2 rules 3 and 5), so `LocalHead`/`LocalHeadShort` are populated and
   `FormatCheckoutHealth` now prints `head=<short>` for that entry
   (`internal/checkout_health.go:866-868` prints the token whenever `LocalHead != ""`). `parent=`
   stays absent. A `child-ref-missing` edge still prints neither token, since `C` did not resolve.

`gitRefExists` itself is **left untouched** because `tws status` shares it
(`internal/agent_status.go:1384,1433`); that keeps `agent-work-status-dashboard` byte-identical and
its dependency edge soft. If a later revision changes `gitRefExists`, that edge must be upgraded to
hard.

## 12. Read-only guarantees

1. Ancestry evaluation uses only the direct ancestry command forms in §7.1 plus the two whitelisted,
   read-only `-C`-shaped safety helpers: `MainRepoRootIn` (one process) and `DefaultBranchIn` (up to
   two processes). All take no Git lock. Pre-existing probe shapes and order remain, while their
   count may increase by at most one per exported checkout builder and by at most one in `doctorCmd`
   due to deliberate config loading (§6.2).
2. No file under `.tws/`, `.git/`, or any worktree is written, and `stack.yaml` is never saved.
3. `LastBaseSHA` is read, never written (N3).
4. Every direct ancestry process runs with `cmd.Dir` set to the validated, non-empty `repoDir`, and
   passes no `-C` at all, so results never depend on the caller's cwd. The only whitelisted
   `-C`-shaped safety probes are the reused `MainRepoRootIn` (one process) and `DefaultBranchIn`
   (up to two processes); both are always passed the validated, non-empty `repoDir`, never `""`.
5. No probe touches `se.Repo` or any path outside the validated `repoDir`.
6. Determinism: two consecutive runs over an unchanged repository produce byte-identical output in
   both modes (no timestamps, no map iteration order, edges in `stack.Branches` order).
7. Enforced by tests: a full recursive tree + `git rev-parse --all` + reflog snapshot before/after,
   and a `PATH`-shim `git` that fails the test if invoked on the no-Git paths (cross-repo,
   `base-unset`, empty `repoDir`), with the §7.1 non-ancestry shape class excluded from the
   assertion (AC 41).

## 13. Invocation matrix

Every cell must produce the same classification for the same fixture; only the repo source may
differ.

| # | Mode | cwd | Expected |
|---|---|---|---|
| 1 | checkout | repo root | `repo_source: workspace`, full ancestry |
| 2 | checkout | nested subdirectory of the repo | identical output to #1 |
| 3 | checkout | with `--feature` filter (`tws doctor <feature>`) | filtered, identical per-entry output |
| 4 | external | repo root | `worktree` (materialized) or `workspace`, full ancestry, **no** `repo-source-mismatch` |
| 5 | external | inside a linked worktree | identical classification to #4; the worktree candidate canonicalizes to the same main repo root, so no mismatch issue |
| 6 | external | nested dir inside a linked worktree | identical to #5 |
| 7 | external | workspace root (`<repo>.tws`) | identical classification |
| 8 | external | feature directory (`<repo>.tws/<feature>`) | identical classification |
| 9 | external | `TWS_ROOT` pointing at another repository's metadata root | `repo_source: worktree`, correct ancestry, exactly one feature-scoped `repo-source-mismatch` info issue |
| 10 | external | no worktrees, no inferable repo | one info `repo-unavailable` issue, unchanged issue total, worktree checks still run |
| 11 | either | detached HEAD | ancestry unaffected (ref-based); header unchanged |
| 12 | either | dirty tree | ancestry unaffected; existing dirty reporting unchanged |
| 13 | either | rebase in progress | ancestry unaffected; existing active-op reporting unchanged |
| 14 | either | no feature filter, multiple features | all features evaluated, order preserved |

## 14. Files and symbols plan

### 14.1 New — `internal/stack_ancestry.go`

Types: `StackBaseKind`, `StackBaseRecord`, `StackRepoSource`, `StackAncestryReason`, `StackNoteKind`,
`StackEdgeNote`, `StackEdge`, `StackRepoResolution`, unexported `ancestryEvaluator`, `refResolution`.

Vars/consts: `ErrRepoUnavailable`; `RepoSourceMismatchLabel`; the unexported
`ancestryUnevaluatedToken`; the reason, kind, record, source, and note constants of §5.1.

Exported functions: `EvaluateStackAncestry`, `EvaluateStackEdge`, `UnevaluatedStackEdges`,
`ResolveStackAncestryRepo`, `FeatureStackEdges`.

Unexported: `newAncestryEvaluator(repoDir string) (*ancestryEvaluator, error)`,
`ancestryGit(repoDir string, args ...string) *exec.Cmd` (§7.1, the single command runner),
`ancestryRepoCandidate(path string) (string, bool)` (§6.2),
`(*ancestryEvaluator) edge(feature string, se StackEntry, stack Stack) StackEdge`,
`(*ancestryEvaluator) resolveCommit(ref string) (full, short string, ok bool)`,
`(*ancestryEvaluator) abbrev(full string) string`,
`(*ancestryEvaluator) defaultBranchName() string`,
`(*ancestryEvaluator) identityNotes(...) []StackEdgeNote`,
`stackBaseRef(stack Stack, se StackEntry) (string, StackBaseKind)`,
`ancestryMergeBase(repoDir, a, b string) (sha string, exists bool, err error)`,
`ancestrySeverity(status AncestryStatus, archived bool) CheckoutSeverity`,
`ancestryGuidance(e StackEdge) string`,
`ancestryDisplayStatus(s AncestryStatus) string` (§6.3),
`ancestrySanitize(s string, limit int) string` (§4.6).

**Naming constraint (normative).** No new symbol may begin with `gitMergeBase`, `gitFullSHA`, or
`gitShortSHA`. The merge-base helper is therefore named `ancestryMergeBase`, **not**
`gitMergeBaseCommit`, so the deletion check of AC 46 stays a simple, unambiguous grep and cannot be
satisfied by a new symbol that merely shares a prefix with a deleted one. AC 46 additionally anchors
its pattern on word boundaries.

Reused unchanged: `gitIsAncestor` (`internal/checkout_sync.go:365-377`), `MainRepoRootIn`
(`internal/exec.go:27`), `DefaultBranchIn` (`internal/exec.go:69`), `canonicalize`
(`internal/workspace.go:205`), `inferExternalRepoRoot` (`internal/workspace.go:339`), `LoadStack`,
`StackEntry.GitBranch`.

### 14.2 Changed — `internal/checkout_health.go`

| Change | Detail |
|---|---|
| `CheckoutFeatureEntry` | additive fields of §10.1.1, appended after `Severity`; existing fields, keys, and short-SHA semantics untouched |
| `BuildCheckoutHealthReport` | signature **unchanged** (`(ws Workspace, opts *CheckoutHealthOpts)`); may add at most one `cfg := LoadConfig()` in its body, before `buildFeatureEntries`, and threads it down |
| `buildFeatureEntries` | new signature `buildFeatureEntries(ws Workspace, cfg Config) ([]CheckoutFeatureEntry, error)`; one `FeatureStackEdges(ws, cfg, feature, fp, stack)` call per feature; pass the matching edge into the entry builder; never calls `LoadConfig` |
| `buildOneFeatureEntry` | new signature `buildOneFeatureEntry(ws Workspace, feature string, se StackEntry, edge StackEdge, currentBranch, sessionFeature, sessionName string) CheckoutFeatureEntry` — the `stack Stack` parameter is dropped (base resolution moved into the evaluator); lines 609-667 (base resolution plus the whole ancestry block, up to and including the final `divergent` arm) deleted, leaving the `return e` at line 669; `RefExists`/heads/status/severity/new fields copied from the edge per §10.1.1 |
| `BuildCheckoutList` | exported signature **unchanged** (`BuildCheckoutList(ws Workspace) ([]CheckoutListEntry, error)`) so `internal/cli/list.go:118` is untouched; it may add at most one `cfg := LoadConfig()` in its body and delegates to a new unexported `buildCheckoutListEntries(ws Workspace, cfg Config) ([]CheckoutListEntry, error)`; lines 959-981 replaced by a `FeatureStackEdges` lookup |
| `FormatCheckoutHealth` | `ancestry=` via `ancestryDisplayStatus`, `[ref-missing]` gating, the ≤3 detail lines |
| `FormatCheckoutList` | ` [<ancestryDisplayStatus>]` tag, so the empty status renders `[unevaluated]` |
| **deleted** | `gitMergeBase`, `gitFullSHA`, `gitShortSHA` — all three become unused once both classifiers are gone (verified: no other caller in the tree) |
| **kept** | `gitRefExists` (still used by `internal/agent_status.go:1384,1433`), `healthCurrentBranch`, `gitDirty`, `gitActiveOp`, `countIssues`, `HasErrors` |

Any ancestry config loads added to this file appear only in the two exported builders, at most once
per builder, and never in a feature or edge loop (AC 53). Both builders keep their public signatures,
so no caller outside this file changes.

### 14.3 Changed — `internal/health.go`

Add `Severity` to `HealthIssue`; add `func (h HealthIssue) EffectiveSeverity() CheckoutSeverity`;
rewrite `String()` to use `severityIcon(EffectiveSeverity())`; add
`func CountHealthIssues(issues []HealthIssue) int` and
`func AncestryHealthIssues(res StackRepoResolution, edges []StackEdge) []HealthIssue`.
`CheckFeatureHealth` (`internal/health.go:105-131`) and the three `CheckWorktree*` functions are
**not** modified.

### 14.4 Changed — `internal/cli/doctor.go`

`doctorCmd` may add at most one `internal.LoadConfig()` call and threads `(ws, cfg)` into both
`checkFeatureE` call sites (`internal/cli/doctor.go:40,56`); `checkFeatureE`
(`internal/cli/doctor.go:73-105`) gains the new signature and the §10.3 body and never calls
`LoadConfig`; `runCheckoutDoctor` (`internal/cli/doctor.go:107-122`) is unchanged.

### 14.5 Changed — tests

`internal/cli/checkout_doctor_test.go` call sites of `checkFeatureE` updated for the new signature —
**exactly 2 call sites**, at `internal/cli/checkout_doctor_test.go:125` and
`internal/cli/checkout_doctor_test.go:184`; no other test in the tree calls it. New test files
`internal/stack_ancestry_test.go` and `internal/cli/doctor_ancestry_test.go` per §17.

### 14.6 Not changed

`internal/agent_status.go`, `internal/checkout_sync.go`, `internal/cli/sync_helpers.go`,
`internal/cli/new.go`, `internal/cli/list.go`, `internal/stack.go`, `internal/workspace.go`,
`internal/exec.go`, `internal/config.go`.

## 15. Documentation and skills

Same commit, because `assets/skills/**` is `go:embed`-compiled and would otherwise ship stale.

1. `assets/skills/claude/tesseraworkspaces/SKILL.md:244` — external doctor bullet gains stack
   ancestry ("wrong branch, uncommitted changes, missing inject symlinks, **and per-edge stack
   ancestry when the source repository is resolvable**").
2. `assets/skills/claude/tesseraworkspaces/SKILL.md:251` — ancestry set becomes
   `current/stale/divergent/missing/cross-repo`, plus the reason/last-base/merge-base detail line and
   the "archived entries are informational" rule.
3. `assets/skills/claude/tesseraworkspaces/SKILL.md:262` — list ancestry set becomes
   `stale/divergent/missing/cross-repo/unevaluated`.
4. Same file — one short paragraph: `stale` means "run `tws sync`"; `divergent` means "the recorded
   base commit left the parent's history, `--onto` is required and sync selects it"; both exit 0;
   doctor never fetches.
5. `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` — one line in the doctor guidance
   so orchestrators stop treating `divergent` as an emergency.
6. `assets/skills/copilot/tws.prompt.md:33` — `tws doctor [feature]` description gains "including
   stack ancestry per configured parent-child edge".
7. `docs/cheatsheet.md:109` — doctor line gains "ancestry".
8. `docs/roadmap.md:60` — mark "Stack ancestry doctor" shipped, leaving ahead/behind counts with
   "Stack status".
9. `docs/engineering-workflow.md:19-21,26-29` — add the shipped slice to the numbered list and point
   the "Next roadmap feature" paragraph at `stack-status`.
10. `CHANGELOG.md` — one entry under the next patch version listing the user-visible changes of §16.

## 16. Deliberate, user-visible reclassifications

Status/severity reclassifications:

1. Parent advanced while the child holds unique commits: `divergent` → **`stale`** (the headline
   correction).
2. Parent reset/force-moved backwards inside the child's history: `divergent` → **`current`**.
3. True rewrites in `tws list`: `stale` → **`divergent`** (list had no `divergent` arm).
4. Annotated-tag base at an ancestor of the child: `stale`/`divergent` forever → **`current`**.
5. Archived entries with `missing`/`stale`/`divergent` edges: `warning` → **`info`**, lowering the
   checkout issue count for workspaces containing them.
6. An entry with `base: ""`: `missing` + warning → **not evaluated** + info.
7. External doctor gains an ancestry section (intended scope change, stated in the request).

Field/output deltas that change no status and no count (§11):

8. Cross-repo, `base-unset`, and `repo-unavailable` entries report `ref_exists: false` instead of the
   previous local-repo probe result; rendered output is unchanged because `[ref-missing]` is gated on
   `RefProbed`.
9. A `missing` entry caused by a missing **base** ref now prints a `head=<short>` token, because the
   child ref resolved before the base ref failed. `child-ref-missing` entries still print neither
   head token.

No existing test asserts any of transitions 1-4, 6, 7, 8, or 9. `TestCheckoutHealth_StaleChild`
(`internal/checkout_health_test.go:189-235`) creates a child with **zero** unique commits and no base
record, so it keeps `stale` + warning under rule 6 of §4.3.
`TestCheckoutHealth_MissingRefs` (`internal/checkout_health_test.go:237-262`, a
`child-ref-missing` fixture, unaffected by delta 9), `TestCheckoutHealth_CrossRepo`
(`internal/checkout_health_test.go:1103-1123`, which asserts only `AncestryStatus`, not `RefExists`),
`TestCheckoutList_*`, `TestExternalDoctor_UnchangedBehavior`, and
`TestExternalFeatureDir_DoctorRegression` all keep their current assertions.

## 17. Acceptance criteria

Runnable. `make build` first; `tws` means `./bin/tws`. Go-level criteria run with
`go test ./internal/... -run <Name> -count=1`. Every Git fixture is a **real** temporary repository
built with `setupHealthTestRepo`/`gitInTest` (`internal/checkout_health_test.go:40-131`) or, for
external mode, `setupGitRepo`/`withWorkspaceEnv` (`internal/cli/new_integration_test.go:135-161`)
plus the production worktree constructor `createWorktree` (`internal/cli/new.go:163`) that the
external integration tests already drive; real local **bare remotes** are used where a remote is
needed. No mocks, no fake `git`, except the deliberate PATH shim of AC 41.

**Classification — core**

1. `TestStackAncestry_ParentContained`: parent is an ancestor of the child →
   `status=current`, `reason=parent-contained`, `severity=ok`, `guidance==""`,
   `*MergeBase == ParentHead`, and exactly **one** `--is-ancestor` probe path is taken (no
   `merge-base` call is needed for the answer).
2. `TestStackAncestry_ChildEqualsParent`: `C == P` → `current`.
3. `TestStackAncestry_ParentBackwardsInsideChild`: child branches from parent, child commits, then
   `git branch -f parent <older commit still in C>` while `LastBaseSHA` records the *newer* old tip →
   `status=current`, `reason=parent-contained`, **not** `divergent`, proving rule 1 precedes the `L`
   test.
4. `TestStackAncestry_ParentFastForwardWithChildWork`: parent advanced, child has unique commits,
   `LastBaseSHA` = old parent tip → `status=stale`, `reason=parent-advanced`, `severity=warning`,
   guidance contains `tws sync` and does **not** contain `--onto`. This is the headline regression.
5. `TestStackAncestry_ParentFastForwardNoChildWork`: same but the child has zero unique commits →
   still `stale`.
6. `TestStackAncestry_SidewaysRewrite`: parent force-moved to a sibling commit outside the child's
   history, `LastBaseSHA` = pre-move tip → `divergent`, `reason=base-rewritten`, guidance contains
   ``git rebase --onto`` and the short `L`.
7. `TestStackAncestry_ParentAmended`: `git commit --amend` on the parent after sync → `divergent`,
   `reason=base-rewritten`.
8. `TestStackAncestry_ParentRebased`: parent rebased onto a new root commit → `divergent`,
   `reason=base-rewritten`.
9. `TestStackAncestry_SidewaysRewriteNoRecord`: AC 6's fixture with `LastBaseSHA` empty →
   `status=stale`, `reason=parent-advanced-no-base-record`, `base_record=absent`, and guidance
   contains `verify the parent history was not rewritten` — the honest-uncertainty wording.
10. `TestStackAncestry_UnrelatedHistories`: child created with `git checkout --orphan` →
    `divergent`, `reason=unrelated-histories`, `MergeBase == nil`, `status != missing`, and both
    refs report as existing.
11. `TestStackAncestry_SyncTriggerNonEquivalence`: pure fast-forward with a recorded base asserts
    both `status == stale` **and** that the sync predicate
    `entry.LastBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA` is true — documenting that
    `--onto` selection is a superset of `divergent`.

**Base record states**

12. `TestStackAncestry_BaseRecordAbsent`: `LastBaseSHA == ""`, parent advanced → `stale`,
    `base_record=absent`, `LastBaseCommit == ""`, never `divergent`.
13. `TestStackAncestry_BaseRecordPruned`: `LastBaseSHA` set to a commit that exists nowhere in the
    repository → `status=stale`, `reason=base-record-unresolvable`, `base_record=unresolvable`,
    severity `warning`, guidance contains `cannot be verified` and no `--onto` recipe, and the
    evaluator returns **no error** (proving the exit-128 path is not read as "not an ancestor").
    The guidance is asserted **different** from the `parent-advanced-no-base-record` guidance of
    AC 9 and from the `ancestry-probe-failed` guidance of AC 57 (§4.5): pruned says "is not present
    in this repository", absent says "no recorded base commit for this branch", probe-failed says
    "ancestry probe failed" and mentions no recorded base at all.
14. `TestStackAncestry_BaseRecordAnnotatedTagObject`: `LastBaseSHA` is an **annotated tag object**
    SHA (as the unpeeled writers record) whose commit is in the parent's history → classified
    identically to its peeled commit (`stale`/`parent-advanced`), and `LastBaseCommit` is the peeled
    commit, not the tag object.

**Ref handling**

15. `TestStackAncestry_AnnotatedTagBaseCurrent`: base is an annotated tag `v1` whose commit is an
    ancestor of the child → `current` (peel regression; today impossible).
16. `TestStackAncestry_LiteralSHABase`: base is a literal 40-hex commit SHA present in the repo →
    classified normally (`current` when contained), `base_kind=literal-ref`.
17. `TestStackAncestry_BogusSHABase`: base is a syntactically valid but non-existent 40-hex SHA →
    `missing`, `reason=base-ref-missing` (today `gitRefExists` returns true for this).
18. `TestStackAncestry_DeletedBaseBranch`: base branch deleted → `missing`,
    `reason=base-ref-missing`, and the child's `RefExists` is still true.
19. `TestStackAncestry_MissingChildRefActive`: active entry whose branch does not exist → `missing`,
    `reason=child-ref-missing`, `severity=warning`, guidance contains `tws new`.
20. `TestStackAncestry_BranchTagCollision`: a branch **and** a tag both named `dup`, with the tag
    pointing elsewhere → the branch wins on both sides, `RefExists` agrees with the classification,
    and doctor output contains no `refname .* is ambiguous` warning text.
21. `TestStackAncestry_RenamedGitBranches`: child `{Name: api, Branch: jd/api}` and parent
    `{Name: core, Branch: jd/core}` with `base: core` → `BaseRef == "refs/heads/jd/core"`,
    `ChildRef == "refs/heads/jd/api"`, correct classification; renaming only `Name` does not change
    the probed refs.
22. `TestStackAncestry_BaseUnset`: `base: ""` → `status == ""`, `reason=base-unset`, `severity=info`,
    `RefProbed == false`, and the entry is uncounted.

**Cross-repo**

23. `TestStackAncestry_CrossRepoNoGit`: entry with `Repo: <path to a second real repo>` →
    `status=cross-repo-unsupported`, `severity=info`, `MergeBase == nil`, `RefProbed == false`; a
    filesystem-level assertion shows the foreign repository's `.git` mtime and `git rev-parse --all`
    output are unchanged, and the PATH shim of AC 41 confirms no `git` process names that path.
24. `TestStackAncestry_CrossRepoUncounted`: same fixture through `BuildCheckoutHealthReport` →
    `report.Issues` identical with and without the cross-repo entry; `HasErrors()` false.

**Severity and counting**

25. `TestStackAncestry_ArchivedSeverityInfo`: archived entries that are `missing`, `stale`, and
    `divergent` each report the correct `status` **and** `severity=info`; `report.Issues` is
    unchanged versus a workspace without them; `HasErrors()` is false.
26. `TestStackAncestry_ActiveSeverityWarning`: the identical fixtures with `Archived: false` report
    `severity=warning` and increase `report.Issues` by exactly one per edge.
27. `TestExternalDoctor_ArchivedUncounted`: the external returned total is identical for the archived
    and no-entry variants, while the info line is still printed.
28. `TestStackAncestry_NeverError`: over the full fixture corpus, no `StackEdge` ever has
    `severity == SeverityError`; `git grep -n 'SeverityError' internal/stack_ancestry.go` returns
    nothing.

**Repository plumbing**

29. `TestStackAncestry_EmptyRepoDirRefused`: `EvaluateStackAncestry("", "f", stack)` returns
    `errors.Is(err, ErrRepoUnavailable)`, nil edges, and — under the PATH shim of AC 41 — starts
    **zero** `git` processes.
30. `TestStackAncestry_NonRepoDirRefused`: a directory that is not a Git repository → same sentinel,
    nil edges, at most the single `rev-parse --git-common-dir` validation probe.
31. `TestExternalDoctor_AncestryReported`: external feature with a materialized worktree and an
    advanced parent → the issue list contains an ancestry warning naming the entry, and the printed
    total equals the warning count.
32. `TestExternalDoctor_RepoUnavailable`: external workspace with no materialized worktree and no
    inferable repository → **exactly one** info `repo-unavailable` issue regardless of entry count,
    the returned total is identical to the pre-feature total (0), the `healthy (N active worktree(s))`
    line is still printed, and `CheckWorktreeBranch`/`Dirty`/`InjectLinks` still run and still report
    when broken.
33. `TestExternalDoctor_WrongTwsRoot`: `TWS_ROOT` points at a metadata root belonging to a
    *different* repository while the feature has a materialized worktree of the correct repository →
    `repo_source == worktree`, `res.RepoDir` equals `canonicalize(MainRepoRootIn(<worktree>))`,
    ancestry is correct (not "everything missing"), `res.Alternate` equals the canonical main root of
    the other repository, and **exactly one** feature-scoped info `HealthIssue` whose `Problem`
    starts with `repo-source-mismatch` is emitted, with `Branch == "stack"`. No `StackEdge.Notes`
    entry is added, and the counted total is unchanged.
34. `TestStackAncestry_RepoSourceStamped`: checkout mode yields `repo_source == workspace` on every
    edge and `res.Alternate == ""` unconditionally; external with worktrees yields `worktree`;
    external with no worktrees but an inferable repo yields `inferred`. In every case `res.RepoDir`
    is a canonical **main repository root** (equal to `canonicalize(MainRepoRootIn(res.RepoDir))`),
    never a linked-worktree path.

**Base identity notes**

35. `TestStackAncestry_RemoteIdentityNote`: real local bare remote; `base: main`, local `main` behind
    `origin/main`, `origin/HEAD` set → a `base-identity-remote-mismatch` note is present, the status
    is still derived from the literal `main`, and neither severity nor `report.Issues` changes.
36. `TestStackAncestry_LiteralIdentityNote`: parent entry `{Name: core, Branch: jd/core}` plus an
    unrelated real branch literally named `core` at a different commit → a
    `base-identity-literal-mismatch` note is present and the classification still uses
    `refs/heads/jd/core`.
37. `TestStackAncestry_NoSpuriousNotes`: a plain fixture with no remote and no name collision emits
    zero notes.

**Doctor / list agreement, output, determinism**

38. `TestAncestryDoctorListAgree`: for a fixture containing `current`, `stale`, `divergent`,
    `missing`, cross-repo, and archived entries, `BuildCheckoutHealthReport(...).Features[i].AncestryStatus`
    equals `BuildCheckoutList(...)[i].AncestryStatus` for every entry, in order.
39. `TestCheckoutHealth_AncestryDetailLines`: `FormatCheckoutHealth` output contains
    `base=main ancestry=divergent` on the entry line, a following `      reason: base-rewritten`
    line, and a following guidance line containing `git rebase --onto`; a `current` entry with no
    notes produces exactly one line; and no entry line contains the guidance text inline.
40. `TestCheckoutHealth_Determinism`: two consecutive `BuildCheckoutHealthReport` +
    `FormatCheckoutHealth` runs over an unchanged repository are byte-identical; the same for
    `BuildCheckoutList` + `FormatCheckoutList`.
41. `TestStackAncestry_ReadOnly`: extends `TestCheckoutHealth_ReadOnly`
    (`internal/checkout_health_test.go:885-945`) — snapshot `git rev-parse --all`, `git reflog --all`,
    `git for-each-ref refs/remotes`, `FETCH_HEAD` presence, the recursive file tree of the repo and
    of `.tws/`, and `stack.yaml` bytes, against a real local bare remote whose branch was moved
    behind tws's back; all identical after doctor **and** list.

    A `PATH` shim `git` records, for every invocation, its full argv **and** its working directory.
    Each recorded invocation is classified by shape:

    - **non-ancestry invocation shape** — its argv, after stripping an optional leading
      `-C <dir>`, matches one of the seven exact forms tabulated at the end of §7.1 (`rev-parse
      --abbrev-ref HEAD`, `rev-parse --short HEAD`, `status --porcelain`, `rev-parse --show-toplevel`,
      `rev-parse --git-common-dir`, `rev-parse --abbrev-ref origin/HEAD`, `symbolic-ref --short
      HEAD`). This class is used only to exclude infrastructure probes from ancestry-probe
      assertions; it makes no claim that a matching invocation is pre-existing or ancestry-added.
    - **ancestry Git probe** — an invocation matching one of the resolve/peel, abbreviate, ancestry,
      or merge-base forms issued through `ancestryGit` or `gitIsAncestor` in §7.1.

    Every recorded invocation must match one of those classes. `MainRepoRootIn` and
    `DefaultBranchIn` match the non-ancestry shape class even when called during ancestry
    evaluation; this is intentional because clauses (a)-(c) specify the direct ancestry Git probes,
    not reused repository/default-branch helpers. For ancestry Git probes only:
    (a) no invocation contains `fetch`, `ls-remote`, `--fork-point`, `push`, `update-ref`, `reset`,
    `checkout`, `rebase`, `gc`, or `status`; (b) no invocation passes `-C` at all, and every
    invocation's recorded working directory equals the validated `repoDir`, which is non-empty;
    (c) every **ref-taking** invocation — i.e. every `rev-parse … <ref>^{commit}` form — passes
    `--end-of-options`, and every `merge-base`/`merge-base --is-ancestor` invocation passes only
    40-hex SHAs. `rev-parse --short <full-sha>` is ref-taking only in the 40-hex sense and is covered
    by the SHA clause. The pre-existing `status --porcelain` health probes are expected and must not
    trip clause (a).
42. `TestStackAncestry_ExitSemantics`: `tws doctor` exits 0 with a `stale` fixture, a `divergent`
    fixture, a `missing` fixture, and a cross-repo fixture, in **both** modes;
    `runCheckoutDoctor` returns nil in all four.
43. `TestStackAncestry_ProcessBound`: with the argv-recording shim of AC 41, restricted to the
    **ancestry Git probes** defined there, a 5-entry linear stack over one shared base satisfies all
    of the following **absolute** assertions. No assertion references, reconstructs, or compares
    against the deleted classifiers' cost.
    - total ancestry Git probes `≤ 10*5` (the §7.3 per-edge worst case);
    - the ref string `refs/heads/<shared parent>^{commit}` appears in **exactly one** ancestry
      invocation, however many edges name it (positive-resolution caching);
    - a base ref that resolves nowhere and is shared by all 5 entries appears in **exactly one**
      ancestry invocation (negative caching);
    - `rev-parse --short <same full sha>` appears **exactly once** for any given SHA (abbreviation
      caching);
    - a stack of 5 **cross-repo** entries, and a stack of 5 `base: ""` entries, each produce
      **zero** ancestry Git probes.

**Regressions that must keep passing verbatim**

44. `go test ./internal/... -run 'TestCheckoutHealth_StaleChild|TestCheckoutHealth_MissingRefs|TestCheckoutHealth_CrossRepo|TestCheckoutList_' -count=1`
    passes with **no test edits**.
45. `go test ./internal/cli/... -run 'TestExternalDoctor_UnchangedBehavior|TestExternalList_UnchangedBehavior|TestExternalFeatureDir_DoctorRegression|TestCheckoutDoctor_' -count=1`
    passes; only the `checkFeatureE` call-site signature changes — at exactly the two sites
    `internal/cli/checkout_doctor_test.go:125` and `internal/cli/checkout_doctor_test.go:184` — and
    `TestExternalDoctor_UnchangedBehavior` still asserts **0** issues.
46. `git grep -nE '(^|[^[:alnum:]_])(gitMergeBase|gitFullSHA|gitShortSHA)([^[:alnum:]_]|$)' --
    internal/` returns nothing. The portable ERE spells out Go identifier boundaries on both sides,
    so every declaration or usage of the three deleted symbols makes the criterion fail, while the
    new helper name `ancestryMergeBase` (§14.1) does not match because its preceding `y` is an
    identifier character. A companion assertion pins the prefix:
    `git grep -nE '(^|[^[:alnum:]_])gitMergeBase[[:alnum:]_]+' -- internal/` also returns nothing,
    proving no new symbol was introduced under a deleted symbol's prefix.
    `git grep -n 'gitRefExists' -- internal/` returns only `internal/agent_status.go` hits and its
    definition in `internal/checkout_health.go`.
47. The `unevaluated` literal is centralized:
    `git grep -n '"unevaluated"' -- 'internal/**.go' ':!internal/**_test.go'` returns **exactly one**
    line, the
    `ancestryUnevaluatedToken` constant in `internal/stack_ancestry.go` (§6.3);
    `git grep -n 'ancestryDisplayStatus(' -- 'internal/**.go' ':!internal/**_test.go'` returns the
    definition plus **exactly three** call sites — one in `FormatCheckoutHealth`, one in `FormatCheckoutList`, one in
    `AncestryHealthIssues`; `git grep -n 'evaluation-unavailable' -- internal/` returns nothing; and
    no sixth `AncestryStatus` constant exists
    (`git grep -nE '^[[:space:]]*AncestryStatus[[:alnum:]_]*[[:space:]]+AncestryStatus[[:space:]]*='
    -- internal/checkout_health.go` returns exactly five constant declaration lines, not usages).
    A runtime assertion complements the greps: for a `base-unset` fixture, checkout doctor prints
    `ancestry=unevaluated`, checkout list prints `[unevaluated]`, and external doctor prints
    `ancestry unevaluated: base-unset` — three surfaces, one literal.
48. `git grep -n 'rev-list\|--count\|--left-right\|patch-id\|fetch' -- internal/stack_ancestry.go`
    returns nothing (N2, N6, N8).
49. `./bin/tws doctor --help` shows no new flag; `./bin/tws doctor --json` exits non-zero with
    `unknown flag: --json`; `./bin/tws list --json` likewise (N1).
50. Full gates green: `gofmt -l` empty for changed files, `go test ./... -count=1`, `go vet ./...`,
    `golangci-lint run ./...`, `make build`, `git diff --check`,
    `tpatch feature deps --validate-all` reporting `DAG: ok (0 violations)`.

**Structural invariants introduced by this revision**

51. `TestStackAncestry_RepoSourceMismatchIsFeatureLevel`: over the whole fixture corpus, **no**
    `StackEdge.Notes` element has a `Kind` outside `{base-identity-remote-mismatch,
    base-identity-literal-mismatch}`; `git grep -nE 'StackNoteKind = "' internal/stack_ancestry.go`
    returns exactly **two** constants;
    `git grep -n 'repo-source-mismatch' -- 'internal/**.go' ':!internal/**_test.go'` returns only the
    single `RepoSourceMismatchLabel` declaration in `internal/stack_ancestry.go` and its single use in
    `AncestryHealthIssues` (`internal/health.go`), and **no** hit in `internal/checkout_health.go`; and `FormatCheckoutHealth` + `FormatCheckoutList` output for the
    AC 33 fixture, evaluated in checkout mode, contains the substring `repo-source-mismatch`
    **zero** times.
52. `TestStackAncestry_CanonicalRepoRoots`: for every resolution in the corpus (checkout, worktree,
    workspace, inferred), `res.RepoDir` is byte-equal to `canonicalize(MainRepoRootIn(res.RepoDir))`,
    and `res.Alternate` is either `""` or likewise canonical; a fixture where the feature worktree and
    `ws.RepoRoot` belong to the same repository yields `res.Alternate == ""` (see AC 55), and a
    fixture where they belong to different repositories yields a non-empty, canonical `res.Alternate`
    different from `res.RepoDir`.
53. `TestCheckoutBuilders_LoadConfigPlacement`: static inspection verifies that ancestry integration
    adds at most one `LoadConfig()` call in each of `BuildCheckoutHealthReport`,
    `BuildCheckoutList`, and `doctorCmd`, and that each such call is outside all feature and edge
    loops;
    `git grep -n 'LoadConfig' -- internal/stack_ancestry.go` returns nothing, and no `LoadConfig`
    call appears inside `checkFeatureE`, inside any feature loop, or inside any edge loop.
    Behaviourally, the exported signatures `BuildCheckoutHealthReport(ws Workspace, opts
    *CheckoutHealthOpts)` and `BuildCheckoutList(ws Workspace)` are unchanged, so
    `internal/cli/list.go:118` and `internal/cli/doctor.go:108` compile untouched
    (`internal/cli/list.go` remains in §14.6).

**Additional criteria introduced by this revision (cross-referenced above)**

54. `TestStackAncestry_RecordedBaseSanitized`: `LastBaseSHA` set to a string containing a newline,
    a tab, an ESC byte, and 200 filler runes → the edge is `base-record-unresolvable`, the emitted
    guidance is a **single line**, contains no control byte, is at most 40 runes of recorded content
    plus `…`, is quoted, and `FormatCheckoutHealth` output contains no raw ESC sequence. The same
    assertion runs for a `se.Base` and a `se.Repo` carrying control bytes (§4.6).
55. `TestStackAncestry_NormalWorktreeNoMismatch`: an ordinary external workspace whose feature
    worktree is a linked worktree of `ws.RepoRoot` → candidate 1 and candidate 2 canonicalize to the
    **same** main repository root, so `res.Alternate == ""`, **zero** `repo-source-mismatch` issues
    are emitted, the counted total is unchanged, and ancestry is fully evaluated. This is the
    regression guard for storing raw candidate paths instead of
    `canonicalize(MainRepoRootIn(candidate))`.
56. `TestCheckoutHealth_EdgeToEntryMapping`: for a fixture whose child and parent heads are known,
    the built `CheckoutFeatureEntry` satisfies **all** of: `LocalHead == edge.LocalHeadShort` and
    `ParentHead == edge.ParentHeadShort` (both matching `git rev-parse --short` output for the
    corresponding commit and both shorter than 40 characters); `LocalHeadFull == edge.LocalHead` and
    `ParentHeadFull == edge.ParentHead` (both exactly 40 hex characters);
    `BaseGitBranch == "jd-main"` for a stack-entry base with `{Name: main, Branch: jd-main}` — i.e.
    **bare**, with no `refs/heads/` prefix and byte-identical to the pre-feature value — while
    `BaseRef == "refs/heads/jd-main"`; for a literal base, `BaseGitBranch == BaseName == "v1"` and
    `BaseRef == "v1"`; `MergeBase` is the full SHA and `MergeBaseShort` is its abbreviation. The
    rendered line is asserted to contain `head=<short>` and `parent=<short>` with the **abbreviated**
    tokens and to contain **no** 40-hex substring anywhere in the entry block, including the reason,
    guidance, and note lines.
57. `TestStackAncestry_ProbeFailedGuidanceDistinct`: a Go-level table test over `ancestryGuidance`
    with synthesized `StackEdge` values asserts that the three "base record unusable" reasons produce
    three pairwise-different strings, that `ancestry-probe-failed` guidance contains
    `ancestry probe failed` and `re-run: tws doctor` and contains **neither** `no recorded base
    commit` nor `is not present in this repository`, and that every guidance string in §4.5 is a
    single line after §4.6 sanitization.

## 18. Follow-ups (explicitly not this feature)

1. **`persist-new-base-sha`** — make `tws new`/`tws add` record `LastBaseSHA` at creation so a
   never-synced branch starts with a base record. A mutation; separate feature (D8).
2. **`fix-checkout-sync-base-identity`** — `BuildCheckoutPlan` must resolve a parent stack entry via
   `parent.GitBranch()` instead of treating `entry.Base` as a literal ref
   (`internal/checkout_sync.go:452-466,891-897`). This feature only reports the disagreement
   (D9, §9.2).
3. **`stack-ancestry-same-repo`** — evaluate an edge whose child has `Repo != ""` when the matched
   parent entry carries the **same** `Repo` value and that path resolves as a repository (D3). Until
   then every `Repo != ""` child is `cross-repo-unsupported` regardless of the parent's `Repo`, no
   note is emitted for the parent/child `Repo` asymmetry, and no process is started (§4.2, N5).
4. **`stack-status`** — machine output, ahead/behind counts, dirty/rebase state, upstream; consumes
   `StackEdge` unchanged (D5, D6).
5. **`fix-list-wrong-branch-check`** — `internal/cli/list.go:63,66,81` passing `entry.Name` where
   `GitBranch()` is required (N9).

## 19. Dependencies

Already registered in `.tpatch/features/stack-ancestry-doctor/status.json`; **this spec adds no edge
and removes none**, and `tpatch feature deps --validate-all` currently reports `DAG: ok (0
violations)` (verified while writing this spec).

**Hard (11):** `keep-track-of-stacked-diffs-and-dependencies` (stack schema, `LastBaseSHA`),
`checkout-doctor-observability` (both checkout consumers), `checkout-stack-safety` (`gitIsAncestor`,
checkout sync plan), `branch-name-decoupling` (`GitBranch()`), `amend-aware-rebase` (`--onto`
semantics), `worktree-health-check` (`HealthIssue`, `CheckFeatureHealth`),
`fix-external-feature-dir-resolution` (external feature-dir invocation), `multi-repo-workspaces`
(`StackEntry.Repo`, the cross-repo arm), `workspace-sibling-links` (`ListFeaturesResolved` /
`GuardFeatureName` fail-closed listing both consumers sit inside), `fix-default-base-branch`
(`DefaultBranchIn`/`origin/HEAD`, the source of the D1 ambiguity settled in §9),
`workspace-mode-foundation` (transitively, via `checkout-doctor-observability`).

**Soft (5):** `agent-work-status-dashboard` — stays soft **only** because §11 adds a peel-correct
resolver instead of changing `gitRefExists`/`healthCurrentBranch`; upgrade to hard if that changes.
`divergent-stack-sync` (`TopoSort`/`Descendants` fixtures overlap), `fix-sync-branch-identity`,
`fix-sync-continue-descendants`, `post-rebase-validation` (future consumers of the evaluator that
this feature deliberately does not rewrite).

**External dependencies: none.** No new Go module, no new binary. The only tool requirement is `git`
supporting `--end-of-options` (Git ≥ 2.24, released 2019); `go.mod` is untouched.
