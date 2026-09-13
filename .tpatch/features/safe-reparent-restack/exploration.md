# Exploration — safe-reparent-restack

Status: Path B exploration (author: isolated exploration agent; revision 2, integrating three
review reports). Inputs of record, all read before writing:
`.tpatch/features/safe-reparent-restack/spec.md` (**normative**, working tree, re-read after the
maintainer's §11.2 / §11.6b / AC-079 / T-067 corrections), `.../analysis.md`,
`.../request.md`, `.../status.json`, `AGENTS.md`, `CLAUDE.md`, `docs/engineering-workflow.md`,
`docs/roadmap.md`, `.tpatch/steering/local.md`, `.claude/skills/tessera-patch/SKILL.md`, and the
precedent `.tpatch/features/rebase-plan-guard/exploration.md`.

**Baseline**: working tree at `d39e934`, `git describe --tags` = `v1.2.16-7-gd39e934`. The only
other uncommitted path is `.tpatch/features/safe-reparent-restack/spec.md` (the maintainer's
corrections). Every path, symbol and line number below was read in that tree.
**A path or symbol with no `NEW` label exists today.** Line numbers are the verification anchor at
`d39e934`; re-derive with `grep -n '<symbol>' <file>` if they drift.

**What this document is**: an implementation map — the measured call graph, the exact insertion
point per route, the exact new/changed symbol per file, the smallest coherent edit per file, a
dependency-ordered build sequence in which **every step compiles**, and the test/probe ledgers.
**What it is not**: a second spec. Every decision here is the spec's; where this exploration found a
mismatch between spec prose (or an earlier revision of this document) and the tree at `d39e934`, it
is recorded in §11 as a *declared precision item with a resolved implementation consequence*.

---

## 0. Ground truth

### 0.1 Versions and baseline

| Fact | Value at `d39e934` |
|---|---|
| HEAD | `d39e934` — `chore(tpatch): define safe reparent restack` |
| `git describe --tags` | `v1.2.16-7-gd39e934` |
| Latest tag | `v1.2.16` (`v1.2.11`…`v1.2.16` all present locally) |
| Go toolchain | `go1.26.5 darwin/arm64` |
| `golangci-lint` | `2.12.2` (built with go1.26.5) |
| Working tree | clean except the normative `spec.md` edit and this file |
| CI on `main` | run `34708647386`, `2026-09-12T17:34:50Z`, workflow `CI`, conclusion **success** — the run for `d39e934` (`status.json.updated_at` = `2026-09-12T17:33:23Z`). The five most recent `main` runs are all `success`. |
| Local full baseline | `GIT_CONFIG_COUNT=0 go test ./... -count=1` **PASS**: `internal` 47.748s, `internal/cli` 307.826s, wall clock 309.83s |

### 0.2 Package sizes (measured, non-test, top level only)

| Scope | Files | Lines |
|---|---:|---:|
| `internal/*.go` excluding `_test.go` | 35 | **27 215** |
| `internal/cli/*.go` excluding `_test.go` | 36 | **10 326** |
| `internal/*_test.go` | 26 | — |
| `internal/cli/*_test.go` | 40 | — |

Command of record:
`find internal -maxdepth 1 -name '*.go' ! -name '*_test.go' -exec cat {} + | wc -l`.

Largest relevant non-test files: `rebase_plan_build.go` 3 778, `agent_status.go` 2 466,
`rebase_plan_probe.go` 2 030, `spaces.go` 1 881, `checkout_sync.go` 1 729, `sync.go` 1 625,
`rebase_plan_guard.go` 1 569, `rebase_planner.go` 1 387, `sync_plan_guard.go` 1 341,
`stack_status.go` 1 280, `sync_modes.go` 1 121, `rebase_plan.go` 1 099, `checkout_health.go` 1 029,
`stack_ancestry.go` 861, `session.go` 749, `rebase_plan_fingerprint.go` 701,
`sync_run_state.go` 657, `direct_session.go` 485, `workspace.go` 465,
`rebase_plan_render.go` 445, `syncstate.go` 432, `resolve.go` 335, `stack.go` 289,
`sync_selection.go` 263, `exec.go` 252, `paths.go` 207, `push.go` 199, `git_capability.go` 168,
`config.go` 149, `stack_status.go` (cli) 126, `checkout_sync.go` (cli) 95, `root.go` 58,
`stack.go` (cli) 56.

### 0.3 Frozen corpus and absent surface

- `internal/cli/testdata/sync_noflag/` — **15 top-level fixture directories**, **126 files** total.
  The 15 are `checkout-abort`, `checkout-clean`, `checkout-conflict`, `checkout-continue`,
  `declared_c1`, `declared_c2`, `declared_c3`, `declared_c4`, `external-abort-empty`,
  `external-abort-state`, `external-clean`, `external-conflict`, `external-continue`,
  `external-fallback`, `external-stale-edge`. The push goldens the spec cites live **inside**
  `declared_c2/` as `declared_c2/push/` and `declared_c2/sync-push/`; they are not top-level
  entries. Measurements of record: `ls -d internal/cli/testdata/sync_noflag/*/ | wc -l` = **15**;
  `find internal/cli/testdata/sync_noflag -type f | wc -l` = **126**. Both are §14.1 / AC-099
  frozen.
- `internal/cli/testdata/rebase_plan/sync_help.txt` — the single `tws sync --help` snapshot,
  §14.1 frozen. It is the only file in that directory.
- `internal/cli/testdata/existing_commands/` — the third testdata tree, untouched here.
- **No file matching `reparent*.go` exists anywhere in the repository.** Every
  `internal/reparent_*.go` and `internal/cli/stack_reparent.go` of §15 is genuinely new.
- The token `reparent` appears today only in `internal/cli/sync_plan_docs_test.go:121`, `:154-155`,
  `:179-180` — roadmap-prose assertions §14.4 deliberately retargets.

### 0.4 Spec checksum of record

| Artifact | SHA-256 | Size |
|---|---|---|
| `spec.md`, **working tree** (normative for this exploration) | `dbf248f3a094304da3b27769a1bde47762a139ed6e3c71dddb338b04aa81d24f` | **203 507 B / 3 892 lines** |
| `spec.md`, as committed at `d39e934` | `f75680ee4378db7990b4fe5fb6bb03c1e66f333f4eeb47f6307a7cd4fafd4077` | 203 066 B / 3 888 lines |
| `analysis.md` | — | 51 619 B / 1 275 lines |
| `request.md` | — | 1 961 B / 8 lines |

The two spec hashes differ by **26 insertions / 22 deletions** across four hunks:

1. **§11.2 legacy-sentinel rationale** — corrected: `SaveGuardedLegacySentinel`'s compare-and-swap
   "checks only the legacy sentinel path and does not reject a v4 payload beside it; that is not the
   reason for the new writer." The real reasons are reparent identity and `durableWriteFile`'s
   directory-fsync guarantee (§11 P-9).
2. **§11.2 sync precheck scope** — the check "precedes both plan dispatch and classification, it
   covers `tws sync --plan`, plain sync, `tws sync --continue`, and `tws sync --abort`".
3. **§11.6b `ReclaimCheckoutLock`** — the "steals unconditionally" claim removed (§11 P-7).
4. **AC-079 and T-067** — both now name `--plan` explicitly.

**The working-tree hash `dbf248f3…` is the one this exploration maps.** A tree hashing to
`f75680ee…` is pre-correction and §11 P-7 / P-9 / §2.5 will not match it.

This document's own SHA-256 cannot appear inside itself; it is reported in the delivery summary and
must be recorded in the landing record (§13.1).

---

## 1. Package boundary and plumbing, measured

### 1.1 `internal` vs `internal/cli` — the hard constraint

- `internal` imports **no** Cobra and **no** `internal/cli`; `internal/cli` imports `internal` and
  `github.com/spf13/cobra`. §15's closing rule is a *preservation* assertion. The nine new
  `internal/reparent_*.go` files must not import Cobra and **must never import `internal/cli`**;
  `internal/cli/stack_reparent.go` is the only new file allowed to import Cobra.
- This is not a style rule — it is a compile constraint that directly decides the fetch design
  (§3.8) and the test-package design (§7.1).
- `externalSyncLayout` (`internal/cli/sync_modes.go:199-202`), its `WorktreePath` (`:205-207`) and
  `resolveExternalSyncLayout` (`:215-231`) are **unexported members of package `cli`**.
  `internal.RebasePlanLayout` (`internal/rebase_plan.go:87`, fields `FeaturePath`, `WorktreesRoot`,
  `RepoRoot`) is the existing `internal`-side carrier, built by `planLayout(externalSyncLayout)`
  (`internal/cli/sync_plan_guard.go:283`). Reuse both; declare no second layout type.
- `internal.PlanWorkspace` is built by `externalWorkspace(ws)` (`internal/cli/sync_plan_guard.go:290`);
  §7.3 field 4 reuses `PlanWorkspace` verbatim, so reparent reuses that builder too.

### 1.2 Writer plumbing today

| Printer / carrier | Site | Shape |
|---|---|---|
| `printSyncModeHeaderTo` / `printSyncModeHeader` (cli) | `internal/cli/sync_modes.go:1073` / `:1079` | writer-taking + `os.Std*` wrapper |
| `printSyncModeHeaderTo` / `printSyncModeHeader` (internal) | `internal/checkout_sync.go:639` / `:644` | deliberate twin |
| `printLocalOnlyNoOp` | `internal/checkout_sync.go:651` | bare `fmt` |
| `fetchQuietTo` / `fetchQuiet` | `internal/cli/sync.go:1507` / `:1559` | writer-taking + wrapper, **package `cli`, unexported** |
| `fetchCheckoutRepoTo` / `fetchCheckoutRepo` | `internal/checkout_sync.go:777` / `:809` (direct/wrapper call sites `:912` / `:922`) | writer-taking + wrapper, **package `internal`, unexported** |
| `renderPlanDocumentTo` / `renderPlanDocument` | `internal/cli/sync_plan_guard.go:173` / `:161` | writer-taking + `cmd.OutOrStdout()`/`cmd.ErrOrStderr()` wrapper |
| `writePlanGuardMarker` | `internal/cli/sync_plan_guard.go:191` | writer-taking marker line |
| `planGuardRefusal(cmd, err)` | `internal/cli/sync_plan_guard.go:200` | prints the `plan-guard:` marker to `cmd.ErrOrStderr()`, returns the error unchanged |
| `internal.PlanWriters{Prose: io.Writer}` | `internal/rebase_plan_guard.go:134` | the `internal`-side writer carrier |

`renderPlanDocumentTo`, `writePlanGuardMarker` and `PlanWriters` are the exact precedents §3.6 and
§13.2 require. `planGuardRefusal` is **reused unchanged** for the `*internal.PlanGuardRefusalError`
values `EvaluateReparentGuard` produces (§13.2 keeps the `plan-guard:` line verbatim).

### 1.3 Git runners today — the exact reason a new one is required

| Helper | Site | `Dir` mechanism | stdout/stderr | stdin | returns |
|---|---|---|---|---|---|
| `Run` | `internal/exec.go:118` | none | `os.Std*` | `os.Stdin` | `error` |
| `RunDir` | `internal/exec.go:127` | `cmd.Dir` | `os.Std*` | `os.Stdin` | `error` |
| `RunTo` | `internal/exec.go:139` | none | caller `io.Writer` | `os.Stdin` | `error` |
| `RunDirTo` | `internal/exec.go:150` | `cmd.Dir` | caller `io.Writer` | **`os.Stdin` (`:154`)** | `error` |
| `RunDirClean` → `runWithFilteredStderr` | `internal/exec.go:162` / `:166` | `cmd.Dir` | filtered to `os.Stderr` | — | `error` |
| `RunSilent` / `RunSilentDir` | `internal/exec.go:212` / `:217` | — / `cmd.Dir` | discarded | — | `error` |
| `runGit` | `internal/rebase_plan_probe.go:32` | **`git -C <dir>`** | stdout captured; stderr to a `strings.Builder`, folded into the error | none set | `(string, error)` |
| `runGitRaw` | `internal/rebase_plan_probe.go:50` | **`git -C <dir>`** | stdout raw; stderr dropped | none set | `([]byte, error)` |
| `runGitStreamLines` | `internal/rebase_plan_probe.go:77` | `git -C <dir>` | streamed | none set | `error` |
| `gitExitCode` | `internal/rebase_plan_probe.go:63` | — | — | — | `int` (−1 when not an `*exec.ExitError`) |
| `checkoutGitOutput` | `internal/checkout_sync.go:530` | **`cmd.Dir`** | stdout trimmed; stderr dropped | none set | `(string, error)` |
| `gitResolveRef` | `internal/checkout_sync.go:422` | via `checkoutGitOutput` | — | — | `(string, error)` |
| `gitPush` | `internal/checkout_sync.go:540` | `cmd.Dir` | `CombinedOutput()` | none set | `error` |
| `mergeBaseIsAncestor` | `internal/rebase_plan_build.go:267` | **`git -C <dir>`** | — | — | `(bool, error)` via `asExitError` (`:282`) |

**Resolved: add one reparent-owned captured runner, and route every reparent Git child through it.**
§7.5b, AC-052 and AC-055 forbid `-C` in any argv this feature emits. `checkoutGitOutput` uses
`cmd.Dir` but discards stderr and cannot feed stdin. `RunDirTo` uses `cmd.Dir` and takes writers but
sets `cmd.Stdin = os.Stdin` (`internal/exec.go:154`) and returns only `error` — it cannot carry the
§9.11 `update-ref --stdin` payload and cannot return captured bytes for a refusal detail.

```go
// internal/reparent_exec.go
type reparentGitResult struct {
    Stdout   []byte
    Stderr   []byte
    ExitCode int // via the existing gitExitCode; -1 when git never ran
}

// runReparentGit runs git with exec.Cmd.Dir, never `-C`, with stdin supplied
// by the caller (nil == no stdin, never os.Stdin), and both streams captured
// into run-owned buffers that are never this process's stdout/stderr
// (§3.6, §9.11).
func runReparentGit(dir string, stdin []byte, args ...string) (reparentGitResult, error)
```

It reuses `gitExitCode` (same package). It records every argv into the run's `argv.log` hook so
forbidden-verb audits are cheap (§7.4).

**Resolved: do NOT reuse `mergeBaseIsAncestor`** (`internal/rebase_plan_build.go:267`). It emits
`git -C <dir> merge-base --is-ancestor …`, which would place a `-C` argv inside the reparent
boundary and violate AC-052/AC-055's emitted-argv audit. §5.4 and §5.2 need the same *semantics*,
not the same function. Implement in `internal/reparent_destination.go`:

```go
// reparentIsAncestor reproduces mergeBaseIsAncestor's exit-0/exit-1/error
// trichotomy through runReparentGit with Cmd.Dir:
//   exit 0 -> (true, nil); exit 1 -> (false, nil); anything else -> (_, err).
func reparentIsAncestor(dir, ancestor, descendant string) (bool, error)
```

`mergeBaseIsAncestor` and `asExitError` stay **untouched, with every existing caller**, and are cited
as the *pattern* only. This also removes the awkward AC-031 carve-out the previous revision needed:
`git merge-base` now appears in reparent-emitted argv **only** as `--is-ancestor`, and
`ancestryMergeBase` (`internal/stack_ancestry.go:297`) remains outside the audited boundary.

`runGit` / `runGitRaw` / `runGitStreamLines` / `checkoutGitOutput` remain **shipped read-only-probe
precedents only**, keep every existing caller, and are never called from reparent code.

### 1.4 Error plumbing today

- `cli.Execute()` (`internal/cli/root.go:16`) prints a returned error with
  `fmt.Fprintln(os.Stderr, err)` at `:52-55` and returns 1 — the only production site where a
  `RunE` error reaches stderr. `stackReparentCmd`'s `RunE` returns errors and lets `Execute` print
  them; every §13.2 marker must be emitted by a wrapper *before* the return, not duplicated in the
  message.
- `planGuardRefusal(cmd, err)` (`internal/cli/sync_plan_guard.go:200`) writes the marker and returns
  `err`. The `reparent:` wrapper copies that exact two-step shape, or §13.2's "exactly one refusal
  line" becomes two.
- `*internal.PlanGuardRefusalError` (`internal/rebase_plan_guard.go:108`, `Error()` `:118`) already
  composes `"<kind>: [state-preserved: ]<detail>"`, and its doc comment states it carries no
  `plan-guard: ` prefix in either field. `EvaluateReparentGuard` returns the same type, so the
  shipped rendering needs **zero** change.

---

## 2. Command surface — measured call graph and exact insertion points

### 2.1 `stackCmd` today (`internal/cli/stack.go`, whole file, 56 lines)

```
:10  func stackCmd() *cobra.Command
:11-52  &cobra.Command{ Use: "stack <feature>", Short: "Show branch dependency tree"
:14        Args: cobra.ExactArgs(1)
:15-32     ValidArgsFunction: len(args)==0 -> internal.ListFeatures() filtered
:21                          features := internal.ListFeatures()
:24                          `if name == "status" { continue }`   <-- the suppression precedent
:29                          return deduped, cobra.ShellCompDirectiveNoFileComp
:33-51     RunE: RequireFeaturePath(:35) -> LoadStack(:40) -> TopoSort(:45, warn-only) -> PrintTree(:49)
:54  cmd.AddCommand(stackStatusCmd())
:55  return cmd
```

**There is no `ArgsLenAtDash` handling anywhere in this file.** `tws stack -- status` reaches the
parent purely through Cobra's native `--` terminator. **Consequence (AC-003):** `tws stack --
reparent` requires **no production code at all** — only a test. Do not add `ArgsLenAtDash` logic.

| # | Edit | Exact position |
|---|---|---|
| 1 | second suppression arm | `:24` becomes `if name == "status" || name == "reparent" { continue }`; the comment at `:17-20` gains one clause naming `tws stack -- reparent` |
| 2 | registration | one line after `:54`: `cmd.AddCommand(stackReparentCmd())` |

Nothing else changes: `Args` at `:14` and `RunE` at `:33-51` stay byte-identical (AC-002).

### 2.2 `stackStatusCmd` — the arity/collision/stream precedent (`internal/cli/stack_status.go`, 126 lines)

```
:12   func stackStatusCmd() *cobra.Command ; var jsonOutput bool (:13)
:15-99  &cobra.Command{ Use: "status <feature>", Long: … }
:43      Args: stackStatusArgs
:44-51   ValidArgsFunction: deliberately UNFILTERED (`tws stack status status` must stay reachable)
:52-95   RunE:
:55        cmd.SilenceUsage = true                     <-- §3.5's "immediately after rule 14"
:57        ws, err := internal.RequireWorkspace()
:59-61     cfg := internal.LoadConfig()
:62-65     internal.GuardFeatureName(ws.MetadataRoot, feature)
:66-69     featurePath, err := ws.ResolveFeaturePath(feature)
:72-75     os.Stat(featurePath) + !IsDir -> "feature not found: %s"
:77-80     internal.LoadStackForStatus(featurePath, feature)
:82-85     report, err := internal.BuildStackStatus(ws, cfg, feature, featurePath, stack)
:86        internal.NormalizeStackStatus(report)
:89-91     json.NewEncoder(cmd.OutOrStdout()).SetIndent("", "  ").Encode(report)
:93        fmt.Fprint(cmd.OutOrStdout(), internal.FormatStackStatus(report))
:98   cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
:105  func stackStatusArgs(cmd *cobra.Command, args []string) error
:106-108   len(args) == 1 -> nil
:109-111   len(args) > 1  -> cobra.ExactArgs(1)(cmd, args)
:112-118   internal.ListFeaturesE() error -> fall back to cobra.ExactArgs(1) (never swallow a workspace fault)
:120-123   for name := range features { if name == "status" { return the collision sentence } }
:122       `accepts 1 arg(s), received 0: a feature named "status" exists; run "tws stack status status" for its stack status report, or "tws stack -- status" for its legacy dependency tree`
:124       cobra.ExactArgs(1)(cmd, args)
```

The collision sentence is produced **only** when `len(args) == 0`; an over-supply (`received 1` on a
continue route, `received 3` on a fresh route) falls through to plain `cobra.ExactArgs`. Reparent
copies that discipline exactly.

`stackStatusCmd`'s own help/`Long`/flags/`ValidArgsFunction` are **frozen** by §14.2 item 1. Do not
refactor `stackStatusArgs` into a shared generic helper; copy its discipline, not its body (§11 P-2).

### 2.3 `stackReparentCmd` — NEW, `internal/cli/stack_reparent.go`

**Registration:** `stackCmd()` `AddCommand` (§2.1 edit 2).

**Arity — `stackReparentArgs` (NEW).** Cobra parses flags **before** it calls `ValidateArgs`, so an
`Args` validator may read the route flags. This is what makes §3.2's per-route arity table
implementable in the `Args` phase, as §3.5's preamble requires.

```go
func stackReparentArgs(cmd *cobra.Command, args []string) error {
    want := 2
    if syncBoolFlag(cmd, "continue") || syncBoolFlag(cmd, "abort") {
        want = 1
    }
    if len(args) == want {
        return nil
    }
    if len(args) > want {
        return cobra.ExactArgs(want)(cmd, args)   // ordinary "received 1/3", no collision hint
    }
    if len(args) != 0 {                            // e.g. want==2, received 1
        return cobra.ExactArgs(want)(cmd, args)
    }
    // len(args) == 0 only: the collision hint, mirroring stackStatusArgs:112-124
    features, err := internal.ListFeaturesE()
    if err != nil {
        return cobra.ExactArgs(want)(cmd, args)
    }
    for _, name := range features {
        if name == "reparent" {
            return fmt.Errorf(
                `accepts %d arg(s), received %d: a feature named "reparent" exists; run "tws stack -- reparent" for its legacy dependency tree`,
                want, len(args))
        }
    }
    return cobra.ExactArgs(want)(cmd, args)
}
```

Three obligations, each verified against the tree:

1. **The collision hint fires only at `len(args) == 0`** (`stackStatusArgs:112-124` never reaches the
   `ListFeaturesE` loop otherwise). An ordinary `received 1` on the fresh route is Cobra's own arity
   message.
2. **Route detection uses `syncBoolFlag`'s *value*, not `cmd.Flags().Changed`.** `syncBoolFlag`
   already exists in package `cli` (`internal/cli/sync_modes.go:86`,
   `internal/cli/sync_plan_guard.go:98`, `:99`). Using `Changed` would classify
   `--continue=false` as a continuation and demand one positional argument; using the value keeps it
   a fresh route, which is the only reading consistent with §3.2's "Route" column.
3. **Both numbers are dynamic** (§11 P-2). The shipped literal at `stack_status.go:122` hardcodes
   `accepts 1 arg(s), received 0`; reparent's arity is route-dependent, so the sentence is built
   with `%d`/`%d`. §3.2's quoted example fixes the `want=2, received=0` spelling, which the golden
   pins.

**Flag registration (closed set of §3.4, 13 flags):** `--onto`, `--onto-kind` (default `auto`),
`--cutoff`, `--plan`, `--json`, `--max-replay-per-entry`, `--max-replay-total`, `--approve-plan`,
`--fetch`, `--no-fetch`, `--continue`, `--abort`, `--verbose`/`-v`. `--yes`, `--dry-run`, `--push`,
`--test`, `--root`, `--unparent`, `--only`, `--from`, `--full`, `--local-only`, `--remote` are
**not declared** (AC-008).

**Help determinism** comes from pflag, not from registration order: `pflag.FlagSet.SortFlags`
defaults to `true`, so Cobra renders flags **lexically sorted**. Registration order is therefore
irrelevant to the golden; do not claim otherwise and do not set `SortFlags=false` (that would make
`tws stack reparent --help` diverge in style from every other tws command).

**Completion (§3.3).**
- `--onto` → `reparentOntoCompletion` **NEW**: sibling stack entry names excluding the target and
  its descendants, then local branch names. `syncEntryCompletion` (registered at
  `internal/cli/sync.go:168-169`) is the shape precedent but is **not reusable**: it excludes no
  descendant closure and appends no branches.
- `--onto-kind` → a fixed `{auto, entry, ref}` completion.
- Parent suppression is §2.1 edit 1.

**Flag validation — `stackReparentValidate` (NEW), owning the 14-rule order of §3.5.**

Resolved: **`stackReparentValidate` MUST NOT call `resolvePlanGuardOptions`**
(`internal/cli/sync_plan_guard.go:80-152`). The shipped function evaluates a different order
(`--json`, the four `--abort` combinations, the limits, the token), knows nothing of `--continue`
combinations for eight flags, has no `--onto`/`--onto-kind`/`--cutoff` rules, and enforces
approve-requires-limits only when `!cont`. Calling it would produce the wrong *first* failure on
several fixtures and make §3.5's "return on the first failure" untestable.

**Resolved: hoist the shared sentence literals to package constants.** §3.5's rules 1–3, 5, 6, 10,
11 and 12 must reuse shipped `tws sync` sentences byte-identically. Measured inventory, with the
**corrected §3.5 rule numbers** (rule 10 covers *both* negative-limit checks; rule 11 is the token
shape; rule 12 is token-requires-limits):

| §3.5 rule | Sentence | Current site | Reparent reuse |
|---|---|---|---|
| 1 | `--json requires --plan` | `internal/cli/sync_plan_guard.go:95` | hoist to a const |
| 2 | `--continue and --abort are mutually exclusive` | `internal/cli/sync_modes.go:146`, inside `errSyncContinueAbort()` (`:145`) | **call `errSyncContinueAbort()`**; add no literal |
| 3 | `--plan cannot be combined with --abort` | `internal/cli/sync_plan_guard.go:105` | hoist to a const |
| 5 | `--fetch and --no-fetch are mutually exclusive` | `internal/cli/sync_modes.go:67` | hoist to a const |
| 6 | `--fetch does not take an explicit value; use --no-fetch to disable automatic fetch` | `internal/cli/sync_modes.go:81` (row 0 of the I4 table at `:78-89`) | hoist to a const |
| 6 | `--no-fetch does not take an explicit value; use --fetch to enable automatic fetch` | `internal/cli/sync_modes.go:82` (row 1) | hoist to a const |
| 10 | `--max-replay-per-entry must be zero or greater` | `internal/cli/sync_plan_guard.go:120` | hoist to a const |
| 10 | `--max-replay-total must be zero or greater` | `internal/cli/sync_plan_guard.go:127` | hoist to a const |
| 11 | `--approve-plan requires a 64-character lowercase hex fingerprint` | `internal/cli/sync_plan_guard.go:138` | hoist to a const |
| 12 | `--approve-plan requires --max-replay-per-entry or --max-replay-total` | `internal/cli/sync_plan_guard.go:142` | hoist to a const |

**Six** live in `internal/cli/sync_plan_guard.go` (`:95`, `:105`, `:120`, `:127`, `:138`, `:142`) —
these are "the six shared literals". **Three more** live in `internal/cli/sync_modes.go` (`:67`,
`:81`, `:82`) and are hoisted in the same edit under the same byte-identity obligation. Rule 2 is
not hoisted at all: `errSyncContinueAbort()` is already a function and is simply **called**.

Rows 2–3 of the I4 table (`internal/cli/sync_modes.go:83-84`, `--full` / `--local-only`) are not
reparent flags and stay inline. `internal/checkout_sync.go:1209` carries a second copy of rule 2's
sentence in package `internal`; it is out of boundary and MUST NOT be touched.

Both hoists replace `fmt.Errorf("<literal>")` with `fmt.Errorf("%s", <const>)`, changing no byte of
any `tws sync` output and no usage string — so `internal/cli/testdata/rebase_plan/sync_help.txt`
stays frozen by construction.

**`SilenceUsage`.** Set `cmd.SilenceUsage = true` as the **first** statement after
`stackReparentValidate` returns nil, mirroring `stack_status.go:55`. AC-010 asserts that a §3.5
failure keeps the usage block and a §13 refusal does not, which is only true if the assignment sits
strictly *after* rule 14.

**Streams (§3.6).** `cmd.OutOrStdout()` carries only `FormatReparentPlan`/`MarshalReparentPlan`
bytes; everything else uses `cmd.ErrOrStderr()`. Declare
`internal.ReparentWriters{Doc, Prose io.Writer}` (**NEW**, `internal/reparent_plan.go`) mirroring
`internal.PlanWriters` (`internal/rebase_plan_guard.go:134`) and pass it down, so no `internal`
reparent function touches `os.Stdout`. `renderReparentDocument` / `…To` copy
`renderPlanDocument`'s two-function shape (`internal/cli/sync_plan_guard.go:161`/`:173`).

### 2.4 Mode and layout resolution inside `stackReparentCmd`

```
 1  stackReparentValidate(cmd)                             # §3.5, pure, no Git, no workspace
 2  cmd.SilenceUsage = true
 3  ws, err := internal.RequireWorkspace()                 # internal/workspace.go:440
 4  cfg := internal.LoadConfig()                           # internal/config.go:60 (repo override :73-74)
 5  feature := args[0]
 6  internal.GuardFeatureName(ws.MetadataRoot, feature)    # internal/spaces.go:692
 7  mode split:
      external: twsRoot := internal.TwsRoot()              # internal/paths.go:77
                layout, err := resolveExternalSyncLayout(ws, twsRoot, feature)  # sync_modes.go:215
      checkout: featurePath, err := ws.ResolveFeaturePath(feature)              # internal/resolve.go:43
 8  lay := internal.RebasePlanLayout{FeaturePath: …, WorktreesRoot: …, RepoRoot: ws.RepoRoot}
      external: planLayout(layout)                         # sync_plan_guard.go:283
 9  entry := args[1]   (fresh route only)
10  fetch measurement (§3.8) happens HERE, in package cli for external
11  hand off to internal.PlanReparent / internal.BeginReparentRun + RunReparent / …Continue / …Abort
```

Rung 6 uses `ws.MetadataRoot` (as `stack_status.go:62` and `push.go:35` do), **not**
`internal.TwsRoot()` (as `sync.go:113` does): `stackStatusCmd` is the correct sibling precedent for
a `tws stack …` child, and `ws.MetadataRoot` is mode-correct in checkout mode where `TwsRoot()` is
not. Rung 7's external arm still needs `TwsRoot()` because `resolveExternalSyncLayout`'s contract
(`sync_modes.go:210-214`) is that `twsRoot` is the caller's own single `internal.TwsRoot()` value.

`ws.ResolveFeaturePath` (`internal/resolve.go:43`) is the checkout resolver;
`RequireFeaturePath` (`internal/resolve.go:294`) re-enters `RequireWorkspace` + `GuardFeatureName`
and is a second workspace probe — avoid it on this ladder, which already holds `ws`.

### 2.5 The sync reparent precheck — owned by `internal/cli/sync.go`

**Measured `syncCmd` `RunE` ladder (`internal/cli/sync.go:68-147`):**

```
:69   internal.RequireTool("git")
:76   guardOpts, err := resolvePlanGuardOptions(cmd)        # pure, zero git, zero state
:81   ws, err := internal.RequireWorkspace()
:88   policy, newMode, changed, err := resolveSyncPolicy(cmd, ws.Mode)     # sync_modes.go:49
:93   if ws.Mode == internal.ModeCheckout {
:94       return runCheckoutSync(cmd, ws, internal.CheckoutSyncOpts{…})    # cli/checkout_sync.go:16
:107  }
:108  feature := args[0]
:112  twsRoot := internal.TwsRoot()
:113  internal.GuardFeatureName(twsRoot, feature)
:116  layout, layoutErr := resolveExternalSyncLayout(ws, twsRoot, feature)
:121  state, stateErr := classifySyncState(layout.FeaturePath, newMode)
:132  if guardOpts.Plan {
:136      return runExternalPlan(cmd, feature, layout, ws, policy, newMode, push, verbose, changed, guardOpts, state, cont)
:139  }
:143  if stateErr != nil && (!syncStateSymlinkOnly(stateErr) || cont || abort) { return stateErr }
:147  return planGuardRefusal(cmd, dispatchClassifiedSync(…))
```

§11.2 (as corrected) requires the precheck **after `GuardFeatureName` and a mode-appropriate safe
path resolution, and before mode dispatch, plan dispatch and classification** — i.e. before
`runCheckoutSync` (`:94`), before `runExternalPlan` (`:136`) and before `classifySyncState`
(`:121`). The shipped ladder resolves the feature path only *after* the checkout dispatch, so the
check cannot simply be dropped at `:92`.

**Resolved insertion (exact):** immediately after `:88`, hoist the feature identity and its guard,
branch the path resolution per mode, run one precheck, then let the existing ladder continue.

```go
// NEW, between sync.go:88 (resolveSyncPolicy) and sync.go:93 (the checkout dispatch)
feature := args[0]
var twsRoot string
var reparentFeaturePath string
if ws.Mode == internal.ModeCheckout {
    if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil { return err }
    reparentFeaturePath, err = ws.ResolveFeaturePath(feature)     // internal/resolve.go:43
    if err != nil { return err }
} else {
    twsRoot = internal.TwsRoot()
    if err := internal.GuardFeatureName(twsRoot, feature); err != nil { return err }
    lay, lerr := resolveExternalSyncLayout(ws, twsRoot, feature)  // sync_modes.go:215
    if lerr != nil { return lerr }
    reparentFeaturePath = lay.FeaturePath
}
if err := internal.RefuseIfReparentActive(ws, feature, reparentFeaturePath); err != nil {
    return err
}
```

Obligations, each verified:

1. **Route coverage is all four verbs.** Because the block sits above `:93`, above `:121` and above
   `:132`, it covers `--plan`, plain, `--continue` and `--abort` in both modes — exactly the
   corrected AC-079 and T-067. A `tws sync --plan` on a feature under reparent must refuse, not
   describe a plan built over the v4/undecodable artifacts.
2. **Checkout arm spawns no Git event and preserves `runCheckoutSync`'s own resolve.**
   `ws.ResolveFeaturePath` (`internal/resolve.go:43`) is **filesystem-only** — it `Stat`s candidate
   layouts and can report ambiguity — and it spawns **no Git child process**. It is *not* pure, and
   this document does not claim it is; what matters for the goldens is the absence of a Git event.
   `runCheckoutSync` (`internal/cli/checkout_sync.go:16`) calls
   `internal.RequireFeaturePath(feature)` at `:18` and assigns `opts.FeaturePath` at `:22`; the
   file's header comment at `:12-15` records that it "keeps its own `internal.RequireFeaturePath`
   call verbatim, so the guard, the layout resolution, and the error semantics are unchanged."
   **That second call MUST remain exactly where it is.** Do not pass the precheck's path into
   `CheckoutSyncOpts` and do not delete `:18`.
3. **External arm reuses its existing path sequence.** The hoisted `twsRoot` is passed to the
   existing `:116` call, and `:108`/`:112`/`:113` are deleted **only because the hoisted statements
   are byte-identical to them**. `resolveExternalSyncLayout` is therefore called twice on the
   external path; its contract (`sync_modes.go:210-214`) is that it issues **no Git command** and
   performs "at most two ordinary `<path>/stack.yaml` reads, and there are zero of them whenever the
   two candidates agree". Keeping `:116` is what preserves the frozen external resolution sequence
   for every existing golden. (Threading the resolved `lay` down to `:116` is a strictly larger edit
   and is **not** the chosen one — §11 P-6.)
4. **No unguarded path joins.** The precheck never joins `feature` onto a root itself; both arms take
   their path from a resolver that ran *after* `GuardFeatureName`.
5. **No no-flag Git-process drift.** `RefuseIfReparentActive` (**NEW**,
   `internal/reparent_state.go`) MUST be `os.Stat` plus at most one `os.ReadFile` of the
   mode-appropriate artifact, and MUST spawn **zero** Git children. A no-flag `tws sync` on a
   feature with no reparent artifact therefore adds one `Stat` and no process, which is what keeps
   the 126 `sync_noflag` goldens and their argv sidecars byte-identical (AC-099). Its checkout path
   is `filepath.Join(ws.CheckoutStateDir(), feature+"-reparent.v1.yaml")`
   (`internal/resolve.go:285`), **not** `checkoutStateDir(featurePath)` — §11 P-5.
6. **Resolution failure propagates.** Both arms `return err`; the check is never silently skipped.

Refusal text (§11.2), one line:

```
a stack reparent is in progress for "<feature>" (run <run-id>, stage <stage>); finish it with: tws stack reparent <feature> --continue
```

`tws push` (`internal/cli/push.go:24-46`) gets **no** such precheck: §12.4/§12.4a give the push paths
their own record-driven behaviour, and §14.1 freezes push output absent a record.

### 2.6 Where the authoritative artifact wins over its own compatibility files

§11.2's matrix and §11.10 require the authoritative reparent artifact to be classified **before** the
generic sync-state/lock refusal, and the compatibility artifacts never to be reported as invalid or
corrupt. Concretely:

- `internal/cli/sync.go` — §2.5's precheck precedes `:94`, `:121` and `:132`.
- `internal/agent_status.go` — the reparent projection precedes `buildFeatureSync` (call `:1268`,
  decl `:1354`) and suppresses `IssueSyncStateInvalid` (`:113`) at `:1433`, `:1523` (with the
  `"inspect "+st.PayloadPath+" and remove it manually"` hint at `:1525`) and `:1559`.
- `internal/checkout_health.go` — `buildOneSyncReport` (`:402`) suppresses
  `"state file unreadable; manually remove "` (`:412`) and
  `"corrupt transaction state; manually remove "` (`:420`). A third hint,
  `"corrupt lock file; manually remove "` (`:453`), covers the checkout **lock** this feature also
  holds and MUST be suppressed on the same condition.

---

## 3. Plan pipeline — exact ownership, reuse, and what must be reparent-owned

### 3.1 Request and snapshot owner

| Concern | Owner | Existing analogue |
|---|---|---|
| CLI-side option envelope | `reparentOptions` **NEW**, `internal/cli/stack_reparent.go` | `planGuardOptions` (`internal/cli/sync_plan_guard.go:30`) |
| `internal`-side request | `ReparentRequest` **NEW**, `internal/reparent_plan_build.go` | `RebasePlanRequest` (`internal/rebase_plan.go:154`) |
| Pre-image snapshot | `ReparentPreImage` **NEW**, `internal/reparent_state.go` | none whole-closure; `CheckoutPlanEntry.PreSHA`/`PostSHA` is per-row |
| Plan document | `ReparentPlan` **NEW**, `internal/reparent_plan.go` | `RebasePlan` (`internal/rebase_plan.go:29`, 25 keys `:30-54`) |

**Resolved: never synthesize a `RebasePlanRequest`.** `RebasePlanRequest`
(`internal/rebase_plan.go:154`) is sync-bound by construction — `Route`, `RequestedRoute`,
`RouteTriggers`, `Policy SyncRunPolicy`, `Push`, `Selection SyncSelection`, `ExternalState`,
`CheckoutState`, `Gates` — and is consumed only by `BuildRebasePlan`
(`internal/rebase_plan_build.go:3409`), which produces the frozen 25-key document over the sync
`RefusalKind` domain (`internal/rebase_plan.go:975`, 30 members at `:1012`) that §13.1 replaces.
Filling it with placeholders would risk changing `RebasePlan` behaviour and make AC-042's
"unchanged by this feature" claim depend on a second caller. `ReparentRequest` is a fresh struct;
`BuildReparentPlan` is a fresh builder.

`ReparentRequest` carries, among the §11.5 inputs, the **already-measured** fetch outcome (§3.8) so
that `BuildReparentPlan` is a pure function of its inputs and spawns no fetch of its own.

`ReparentPreImage` is the single carrier for §11.4 step 5, shared by the plan builder (read-only)
and the executor: closure row SHAs, the exact `stack.yaml` bytes + SHA-256, holder
path/branch/HEAD triples, `original_branch`/`original_head`/`original_detached`, ref backend, OID
width, and the raw frozen validation command with source and digest. The plan **must not** publish
the raw command (§7.4); only its digest.

### 3.2 Destination and cutoff resolution

`internal/reparent_destination.go` **NEW** owns:

```go
func ResolveReparentDestination(repoDir string, stack Stack, target StackEntry,
    token, kind string, oidWidth int) (ReparentDestination, error)   // §4.1-§4.3b
func resolveReparentCutoff(...) (ReparentCutoff, error)              // §5.1-§5.3
func reparentOIDWidth(repoDir string) (int, error)                   // §4.3a step 1
func reparentRefCandidates(token string) [5]string                   // §4.3 step 3
func reparentIsAncestor(dir, ancestor, descendant string) (bool, error) // §1.3, replaces mergeBaseIsAncestor
```

Every probe goes through `runReparentGit` (§1.3) with `dir = repoRoot`. The five candidate refs of
§4.3 step 3 go to **one** `for-each-ref --format=%(refname)` invocation and are then filtered by
exact string equality (AC-014). `--disambiguate` (§4.3a step 3) and `--show-object-format`
(§4.3a step 1) have **no existing call site in the tree**, so both are new probes with no precedent.

**Resolver-agreement adapter (§4.3c) — the four resolvers, measured:**

| # | Resolver | Site | Returns |
|---|---|---|---|
| 1 | `ResolveReparentDestination` | **NEW** | already a peeled OID |
| 2 | `ResolveSyncBase` | `internal/rebase_planner.go:215` → `ResolveSyncBaseResult` | a ref/token spelling, with the `origin/<default>` rewrite |
| 3 | `stackBaseRef` | `internal/stack_ancestry.go:318` → `(string, StackBaseKind)` | `refs/heads/<parent.GitBranch()>` for an in-stack parent, else the literal token |
| 4 | checkout executor base pick | **inline**, `internal/checkout_sync.go:590-596`, inside `buildCheckoutPlanFrom` (`:573`) | a token, then `gitResolveRef` (`:598` → `:422` → `checkoutGitOutput` `:530`) |

Resolver 4 has no function to call. The shipped body, with exact lines:

```go
:589   branch := entry.GitBranch()            // NOT part of the extraction
:590   base := entry.Base
:591   if base == "" {
:592       continue // root base uses current default; skip
:593   }
:594   if parent := GetBranch(stack, entry.Base); parent.Name != "" {
:595       base = parent.GitBranch()
:596   }
:597
:598   newBaseSHA, err := gitResolveRef(repoDir, base)
```

**Resolved: extract a pure `checkoutBaseTokenFor` from `:590-596` only.** `:589`'s `branch`
assignment stays in `buildCheckoutPlanFrom` — it is used by the error messages at `:600` and by the
plan row, and pulling it into the extraction would change the caller's locals for no reason.

```go
// internal/checkout_sync.go, beside buildCheckoutPlanFrom
// checkoutBaseTokenFor is the checkout executor's base-token selection,
// extracted verbatim from buildCheckoutPlanFrom:590-596 so the shipped
// executor and the reparent resolver-agreement adapter read the identical
// rule. ok == false reproduces the `continue` at the old :592.
func checkoutBaseTokenFor(stack Stack, entry StackEntry) (base string, ok bool)
```

`:590-596` becomes `base, ok := checkoutBaseTokenFor(stack, entry); if !ok { continue }`, leaving
`:598`'s `gitResolveRef` exactly where it is. This is a **pure extraction with zero behaviour
change**, the smallest edit that keeps §4.3c's "existing resolvers remain unchanged" true while
still being callable, and it turns a future divergence into a compile error rather than a silent
plan/execution mismatch.

The adapter then normalizes answers 2–4 with
`git rev-parse --verify --quiet --end-of-options <answer>^{commit}` (through `runReparentGit`)
before comparison — load-bearing for annotated tags, because `gitResolveRef`
(`internal/checkout_sync.go:422`) does a bare `rev-parse` and observes the **tag object** OID while
the normalized answer must be the peeled commit. Divergence refuses
`destination-resolver-divergent` (rank 17). `ResolveSyncBase` and `stackBaseRef` are called
**unmodified**.

### 3.3 Stable closure order

**Resolved: `ReparentClosureOrder` lives in `internal/stack.go`, beside the frozen `TopoSort`.**

Measured `TopoSort` (`internal/stack.go:138-182`):

```
:141-158  build entryMap / children / inDegree; only edges whose base names a tracked branch (:151)
:157      // Kahn's algorithm
:158      var queue []string
:159-163  for name, deg := range inDegree { if deg == 0 { queue = append(queue, name) } }
:165-176  for len(queue) > 0 { pop front; append entryMap[name]; for _, child := range children[name] { … } }
:178-179  if len(sorted) != len(s.Branches) { return nil, fmt.Errorf("cycle detected in stack.yaml") }
:181      return sorted, nil
:182      }
```

The evidence that matters, corrected: **the ready queue is seeded by ranging over the Go map
`inDegree` at `:159-163`, so the initial root order is randomized per run**; only the *child
expansion* at `:170-175` follows `children[name]`, which is appended in `s.Branches` declaration
order. `TopoSort`'s sibling order is therefore not merely unspecified — it is nondeterministic for
multi-root stacks. Its only order-insensitive production consumer is
`internal/cli/stack.go:45`; the sync/planner callers consume the returned order, which is why
§14.1 freezes the existing function and §6.1a requires a separate deterministic
`ReparentClosureOrder`.

That is exactly why §6.1a needs its own function. `ReparentClosureOrder(stack Stack, target string)
([]StackEntry, error)` is added **after `:182` and before `Descendants` (`:185`)**. It MUST NOT call
`TopoSort`, MUST NOT modify it, MUST be pure, and MUST spawn no Git command (§6.1a, AC-037). Its
ready set is a **min-heap keyed by the entry's index in `Stack.Branches`** — declaration order is
the sole sibling tie-break — which is deterministic where `TopoSort:159-163` is not. Its edge rule
is §6.1 step 4: `child.Base == parent.Name` **and** `SameStackRepo(child.Repo, parent.Repo)`
(`internal/sync_selection.go:94`), stricter than `TopoSort:151`, which ignores `Repo` entirely.
`Descendants` (`:185`) and `DescendantsList` (`:282`) are likewise not reusable for the same reason.

Placement in `stack.go` is deliberate: a reader comparing the two orderings must see them adjacent,
and §14.2 item 10 states the pairing as a product fact. `internal/stack_test.go` already owns
`TopoSort` coverage and gains T-025's determinism/tie-break assertions.

### 3.4 Scope gates

| §6.2 condition | Kind | Existing symbol |
|---|---|---|
| archived target / closure row | `target-archived` / `affected-archived` | `StackEntry.Archived` (`internal/stack.go:16`) |
| duplicate name / git branch | `duplicate-entry-name` / `duplicate-git-branch` | `StackEntry.Name` (`:14`), `GitBranch()` (`:23`) — pure, before any Git child |
| cross-repo closure | `cross-repo-closure` | `git rev-parse --git-common-dir` per row via `runReparentGit`; `SameStackRepo` (`internal/sync_selection.go:94`) for the logical edge test |
| branch ref missing | `branch-ref-missing` | `rev-parse --verify --quiet --end-of-options` |
| git op in progress | `git-operation-in-progress` | `sessionGitOperation` (`internal/session.go:545`) is the precedent; reparent probes each consulted worktree via `runReparentGit` |
| dirty | `context-dirty` | `git status --porcelain` (`sessionDirty`, `internal/session.go:541`, is the precedent) |
| holder unsafe | `holder-unsafe` | `BuildWorktreeInventory` (`internal/agent_status.go:527`) — §4.9 |
| live session | `session-live` | `GuardDirectSessionsFor` (`internal/direct_session.go:352`) external; `CheckoutSessionPreconditions` (`internal/session.go:279`) + `HasCheckoutAgentSession` (`:173`) checkout |
| sync state present | `sync-state-present` | the path helpers of §4.10 |
| reparent state present | `reparent-state-present` | `internal/reparent_state.go` **NEW** |

`CheckoutSessionPreconditions` internally calls `anyCheckoutSyncActive(ws.MetadataRoot)` at
`internal/session.go:295` (declaration `:311`), so it is **both** the session gate and a second
sync-activity probe. Call it once per affected entry and attribute its two failure classes to the
right kinds (`session-live` vs `sync-state-present`); re-implement neither.

### 3.5 Schema and render

`internal/reparent_plan.go` **NEW** declares, in §7.3 order, `ReparentPlan` with **exactly 25**
fields; `ReparentPlanRow` **23**; `ReparentPlanSummary` **11**; `ReparentPlanApprovalCovers` **8**.
Counts are asserted by AC-041/T-031 with a reflect-based key-order test;
`internal/rebase_plan_test.go` already owns the analogous assertion for `RebasePlan` and is the
shape to copy.

**Reused verbatim (§7.2), resolved to declaring file:**

| Type | File:line |
|---|---|
| `PlanWorkspace`, `PlanContext`, `PlanRepository`, `PlanRepositoryConfig`, `PlanConfigSlot`, `PlanConfigIssue`, `PlanEncodingIssue` | `internal/rebase_plan.go` |
| `PlanFetch` `:328`, `PlanFetchRepo` `:344` (custom `MarshalJSON` `:372`, wire type `:359`), `PlanFetchCandidate` `:403`, `PlanFetchEffect` `:410`, `PlanFetchRemote` `:425` | `internal/rebase_plan.go` |
| `PlanFetchRepoResult` `:66`, `PlanFetchOutcome` `:81`, `PlanFetchContext` `:91` | `internal/rebase_plan_guard.go` |
| `PlanEntryHead`, `PlanReplayCandidate`, `PlanEntryReplay`, `PlanCollateralRef`, `PlanAncestry` | `internal/rebase_plan.go` |
| `PlanGuardBlock` + `PlanGuardLimitSet`, `PlanGuardLimit`, `PlanGuardLimitConflict`, `PlanGuardEvaluation` | `internal/rebase_plan.go` |
| `ControlledPathBlocker` `:1081` (`ControlledPathBlockers` `:1093`) | `internal/rebase_plan.go` |
| `PlanStateSnapshot`, `PlanStateFileBase`, `PlanStateFileCheckoutTransaction`, `PlanStateFileCheckoutLock`, `PlanStateFileExternalLegacyState`, `PlanStateFileExternalPayload`, `PlanStateFileExternalRunGuard`, `PlanStateWorktree`, `PlanStateGitOp`, `PlanStateHead` | `internal/rebase_plan.go` / `internal/rebase_plan_state.go` |
| `RefusalKind` `:975` (for `waived_kinds` only) | `internal/rebase_plan.go` |

**Explicitly not reused (§7.2):** `PlanEntry` (`internal/rebase_plan.go:930`, 27 sync-only keys),
`PlanEntryBase`, `PlanEntryDestination`, `PlanEntryCutoff`, `PlanBlocker`, `PlanRefusal`,
`PlanApproval`, `PlanSummary`, `PlanPolicy`, `PlanIntent`, `PlanPush`, `PlanRestore`, `PlanState`.

`internal/reparent_plan_render.go` **NEW**: `FormatReparentPlan` / `MarshalReparentPlan` mirror
`FormatRebasePlan` (`internal/rebase_plan_render.go:39`) and `MarshalRebasePlan` (`:383`) — one
compact JSON value plus exactly one `\n`, HTML-unescaped. **Reuse `ensureSlice[T]`**
(`internal/rebase_plan_render.go:440`) for array normalization. Deferred cells (§7.5a) render as the
literal `(computed at replay)`.

Goldens: `internal/cli/testdata/reparent/plan_human.txt`, `.../reparent_help.txt`,
`.../stack_help.txt` — three new files in a new fourth testdata tree.

### 3.6 Fingerprint TLV and the reparent revalidation digest

`internal/reparent_plan_fingerprint.go` **NEW**. The TLV machinery is **reused, not copied**:
`tlvEncoder` (`internal/rebase_plan_fingerprint.go:60`) and its methods `writeField` (`:64`),
`null` (`:80`), `boolValue` (`:83`), `boolPtr` (`:92`), `uintPtr` (`:103`), `bytesValue` (`:117`),
`bytesPtr` (`:122`), `arrayValue` (`:134`), `arrayPtr` (`:142`) are unexported members of package
`internal` and are directly callable from a new file in that package.

New constants (own domain, §7.13):

```go
reparentFingerprintPrefix             = "tws-reparent-fp\x00"
reparentFingerprintEncodingVersion    = 0x0001
reparentFingerprintTupleSchemaVersion = 0x0001
```

The sync domain (`planFingerprintPrefix = "tws-plan-fp\x00"`, encoding `0x0001`, tuple schema
`0x0004`, near `internal/rebase_plan_fingerprint.go:154`) MUST NOT change (AC-042, AC-048).
`planFingerprintPreimage` (`:317`) is the shape to mirror: prefix, two big-endian `uint16` version
fields, then a length-framed root STRUCT. The **33** field ids of §7.13 get their own table beside
the existing doc table at `:172`. `PlanFingerprint` (`:298`) and `PlanFingerprintPreimage` (`:313`)
stay untouched. `null` (`:80`) is what §7.5a's "explicit absence, never a guess" compiles to: every
deferred cell is written with `enc.null(fieldID)`, never omitted.

**Resolved: `RevalidationDigest` is NOT reusable; add a reparent-owned digest.** The shipped
signature is `func RevalidationDigest(entry PlanEntry) (string, error)`
(`internal/rebase_plan_fingerprint.go:520`) — it takes a **`PlanEntry`**, which §7.2 explicitly
forbids reusing, and it hashes `encodeRevalidationEntry(entry)` over sync-only row fields. Add, in
the same new file:

```go
// ReparentRevalidationDigest hashes a row's canonical candidate inputs —
// git_branch, cutoff sha, pre-image head sha, destination_binding,
// destination_parent, destination_sha (explicit NULL when deferred),
// replay candidate count and the ordered candidate OIDs — through the same
// TLV framing, under the reparent domain prefix.
func ReparentRevalidationDigest(row ReparentPlanRow) (string, error)
```

It reuses `tlvEncoder` and the reparent prefix/version constants; it must **not** reuse the sync
tuple schema version, so a sync digest can never collide with a reparent digest.

### 3.7 Guard, limits, approval, JIT

**Reused wholesale — no reparent copy:**

| Symbol | Site | Reparent use |
|---|---|---|
| `CheckoutPlanGuard` (`Armed()` `:51`, `Guarded()` `:58`) | `internal/rebase_plan_guard.go:33` | the limits/approve carrier |
| `PlanGuardLimits` | `internal/rebase_plan_guard.go:193` | frozen limits |
| `PlanGuardBlock`, `PlanGuardLimitSet`, `PlanGuardLimit`, `PlanGuardLimitConflict`, **`PlanGuardEvaluation`** | `internal/rebase_plan.go` | §7.3 field 23 and every evaluation row |
| `PlanGuardRefusalError` (`Error()` `:118`) | `internal/rebase_plan_guard.go:108` | every guard/limit/approval/revalidation refusal |
| `ControlledPathBlocker` / `ControlledPathBlockers` | `internal/rebase_plan.go:1081` / `:1093` | the blocker token vocabulary |
| `planGuardRefusal` | `internal/cli/sync_plan_guard.go:200` | the `plan-guard:` line, unchanged |

**Explicitly NOT reusable — all three are sync-typed:**

| Symbol | Measured signature | Why |
|---|---|---|
| `RevalidationDigest` | `func RevalidationDigest(entry PlanEntry) (string, error)` — `internal/rebase_plan_fingerprint.go:520` | takes `PlanEntry` (§3.6) |
| `RevalidatePlanGuardEntry` | `func RevalidatePlanGuardEntry(req RevalidatePlanGuardEntryRequest) (PlanGuardRevalidation, error)` — `internal/rebase_plan_guard.go:602`, delegating to `RevalidatePlanEntry(req.Request, req.Approved)` where `req.Request` is a `RebasePlanRequest` | requires a `RebasePlanRequest` and a `PlanEntry` |
| `EvaluatePlanGuard` | `func EvaluatePlanGuard(plan RebasePlan, g CheckoutPlanGuard) error` — `internal/rebase_plan_guard.go:467` | takes a `RebasePlan`; and it is **only the final three-rung evaluator** (refusal kind → `!Runnable` → first `ExecuteBlockedBy` token), not the limit arithmetic |

**The real limit algebra, located.** It is not inside `EvaluatePlanGuard` and it is not
`controlledPathBlockerKind`/`controlledPathBlockerDetail`
(`internal/rebase_plan_guard.go:496`/`:513` — those map a blocker *token* to a kind and a sentence).
It is four functions in `internal/rebase_plan_build.go`, all `PlanEntry`/`PlanSummary`/`PlanBlocker`
typed:

```
:2594  func guardEvaluationRows(limits PlanGuardLimits, entries []PlanEntry, summary PlanSummary, plannability string) []PlanGuardEvaluation
:2618  func perEntryEvaluationRow(limit int, e PlanEntry) PlanGuardEvaluation
:2657  func totalEvaluationRow(limit int, entries []PlanEntry, summary PlanSummary) PlanGuardEvaluation
:2750  func limitExceededBlockers(rows []PlanGuardEvaluation) []PlanBlocker
```

**Resolved: reparent-owned adapters over the same *shape*, producing reparent types.** In
`internal/reparent_plan_build.go`:

```go
func reparentGuardEvaluationRows(limits PlanGuardLimits, rows []ReparentPlanRow,
    summary ReparentPlanSummary, plannability string) []PlanGuardEvaluation
func reparentPerEntryEvaluationRow(limit int, row ReparentPlanRow) PlanGuardEvaluation
func reparentTotalEvaluationRow(limit int, rows []ReparentPlanRow, summary ReparentPlanSummary) PlanGuardEvaluation
func reparentLimitExceededBlockers(rows []PlanGuardEvaluation) []ReparentPlanBlocker
func RevalidateReparentRow(row ReparentPlanRow, approvedDigest string,
    live ReparentRowProbe) (PlanGuardEvaluation, error)
```

The **output** type `PlanGuardEvaluation` is reused verbatim (§7.2 permits it and §7.3 field 23
requires it); only the **input** row type and the emitted blocker type are reparent-owned. A
four-row parity test (T-081) pins that
`reparentPerEntryEvaluationRow`/`reparentTotalEvaluationRow` produce the same
`PlanGuardEvaluation` values as their sync twins for equivalent inputs, so the two ladders cannot
drift.

`EvaluateReparentGuard(plan ReparentPlan, g CheckoutPlanGuard) error` (**NEW**,
`internal/reparent_plan.go`) reproduces `EvaluatePlanGuard`'s three-rung final ladder against the
reparent document, reusing `controlledPathBlockerKind`/`controlledPathBlockerDetail`
(`internal/rebase_plan_guard.go:496`/`:513`) for token mapping and returning the identical
`*PlanGuardRefusalError`. **No `internal/reparent_plan_guard.go` is created** (§11 P-3).

`SelectPrimaryReparentRefusal` (**NEW**, `internal/reparent_plan.go`) mirrors `SelectPrimaryRefusal`
(`internal/rebase_planner.go:1381`): sort by `(rank, entry nil-first, entry, detail)`, dedupe,
return the first blocker's kind. `ReparentRefusalKinds` has **49** members in §13.1 rank order;
`RefusalKinds` (`internal/rebase_plan.go:1012`, 30) is untouched.

`reparentGuardRun` (**NEW**) is the JIT seam carrier; `planGuardRun`/`newPlanGuardRun`/`revalidate`
(`internal/cli/sync_plan_guard.go:219`/`:237`/`:257`) are shape precedents only — they hold a
`RebasePlanRequest`.

### 3.8 Fetch construction — the package-boundary-correct design

**The constraint.** `fetchQuietTo` is
`func fetchQuietTo(out, errw io.Writer, repo, wtPath string, verbose bool, ctx internal.PlanFetchContext) internal.PlanFetchRepoResult`
at `internal/cli/sync.go:1507` — **package `cli`, unexported**. `internal` cannot call it, and
`internal` must never import `internal/cli` (§1.1). The previous revision's claim that "the external
arm calls `fetchQuietTo`" from an `internal` builder does not compile.

**Resolved design — measure in the right package, then pass the measurement in.**

1. **Construction is pure and lives in `internal`.** `internal/reparent_plan_build.go` declares

   ```go
   // reparentFetchContext builds the PlanFetchContext for this run's single
   // repository. Pure: no process, no network.
   func reparentFetchContext(repoRoot, repoToken string, caps GitCapabilities) PlanFetchContext

   // reparentFetchPlan projects the declared policy into the plan's PlanFetch
   // block. Pure: it describes what WILL happen (or a suppression), never
   // what happened.
   func reparentFetchPlan(policy SyncFetchPolicy, defaultApplied bool, ctx PlanFetchContext) PlanFetch

   // reparentFetchProjection folds a MEASURED outcome into the plan's
   // PlanFetch block. Pure.
   func reparentFetchProjection(plan PlanFetch, outcome PlanFetchOutcome) PlanFetch
   ```

   `ReparentRequest` gains a `Fetch PlanFetchOutcome` field. `BuildReparentPlan` therefore performs
   **no** fetch and spawns no process for one; it is a pure function of its inputs, which is what
   makes the plan/execution fingerprint parity of AC-047 mechanically checkable.

2. **External execution stays in `internal/cli/stack_reparent.go`.** The CLI arm owns the one
   policy-declared fetch and calls the shipped `fetchQuietTo`
   (`internal/cli/sync.go:1507`) with the run's own writers and a
   `reparentFetchContext(...)`-built `internal.PlanFetchContext`, then wraps the returned
   `internal.PlanFetchRepoResult` into an `internal.PlanFetchOutcome` and assigns it to
   `ReparentRequest.Fetch`. `fetchQuiet` (`:1559`) is the discarding wrapper and is not used here.

3. **Checkout execution gets an `internal`-side reparent adapter that delegates the shipped body.**
   The checkout fetch already has a writer-taking form in package `internal`:

   ```
   internal/checkout_sync.go:777  func fetchCheckoutRepoTo(w io.Writer, ctx PlanFetchContext) PlanFetchOutcome
   internal/checkout_sync.go:809  func fetchCheckoutRepo(repoDir string) PlanFetchOutcome   // os.Stdout wrapper
   internal/checkout_sync.go:922  the shipped call site inside RunCheckoutSync
   ```

   `fetchCheckoutRepoTo` is unexported but in package `internal`, so a new file in that package can
   call it directly. Declare in `internal/reparent_exec.go`:

   ```go
   // fetchReparentCheckoutRepo runs the checkout route's single fetch by
   // delegating to the shipped fetchCheckoutRepoTo, so reparent and sync
   // perform byte-identical fetch prose and identical PlanFetchOutcome shape.
   func fetchReparentCheckoutRepo(w io.Writer, ctx PlanFetchContext) PlanFetchOutcome {
       return fetchCheckoutRepoTo(w, ctx)
   }
   ```

   Do **not** re-implement `RunSilentDir(ctx.Root, "git", "fetch")`; delegating is what keeps the two
   routes provably identical. `fetchCheckoutRepoTo`, `fetchCheckoutRepo` and the `:912` / `:922` call sites are
   otherwise untouched.

4. **Policy default.** Fetch in external mode, no-fetch in checkout mode, matching
   `resolveSyncPolicy`'s mode-derived default exactly (`internal/cli/sync_modes.go:49`).
   `policy.fetch_default_applied` is true when neither flag was given. The fetch happens **exactly
   once**, at §6.3 step 2a, on the plan route and on a fresh execution; never on
   `--continue`/`--abort` (AC-051). Per-invocation outcome fields are **not** fingerprint inputs
   (§3.4 of the spec).

5. **Not reusable:** `externalFetchPlan(layout externalSyncLayout, stack Stack, sel SyncSelection, newMode bool, caps GitCapabilities)`
   (`internal/cli/sync_plan_guard.go:925`) requires a `SyncSelection` and an `externalSyncLayout`;
   reparent has neither. `ResolveFetchSuppression` (`internal/rebase_plan_guard.go:286`) and
   `FetchSuppression` (`:257`) **are** reused as-is for `suppression_cause`.

### 3.9 Config and capability construction

**Config / validation.** `internal.LoadConfig()` (`internal/config.go:60`) already applies the
per-repository override at `:73-74`, so `policy.validation.source`
(`config-repo` / `config-workspace` / `none`) is derived by comparing `LoadConfigFile`
(`internal/config.go:114`) results, not by re-reading YAML. `TestCommand` is `Config.TestCommand`
(`internal/config.go:16`, `yaml:"test_command"`). `ValidationDigest`
(`internal/rebase_plan_guard.go:219`) and `PlanValidationIdentity` (`:207`) are reused for
`command_digest`. This construction is pure and lives in `internal`.

**Capabilities.** `GitCapabilities` (`internal/git_capability.go:95`) is the closed six-gate table;
§9.10 and §14.1 forbid extending it. `CapRebaseUpdateRefs` (Git ≥ 2.38) is the required floor.
`GitCapabilitiesForVersion` (`:150`), `ProbeGitCapabilities` (`:165`), `ProbeGitVersion` (`:45`),
`GitVersion` (`:20`) and `atLeast` (`:136`, deliberately patch-blind) are reused unchanged.
`ReparentGitCapabilities` + `ReparentGitCapabilitiesForVersion` go **beside, not inside** (§14.2
item 8), after `GitCapabilitiesForVersion`. `CapabilityGates`
(`internal/rebase_plan_guard.go:954`) enumerates the six sync gates and is **not** reused.

---

## 4. Git and recovery pipeline — exact construction

### 4.0 `BeginReparentRun` — the owner of §11.4's begin-run order

§11.4 fixes a nine-step order before the first mutation, and §11.9 window 1 is precisely the gap
between its steps 6 and 7. **The authoritative state artifact is written before the compatibility
artifacts, never after and never the reverse.** Resolved ownership and call graph:

```
internal/cli/stack_reparent.go  RunE (fresh execution)
  |
  |-- (external only) measure the one policy fetch via fetchQuietTo        §3.8 step 2
  |
  |-- internal.PlanReparent(req) ................................ §11.4 steps 1-2
  |      internal/reparent_plan_build.go
  |      §6.3 steps 1-10: pure + read-only only, no mutating verb,
  |      builds ReparentPlan, evaluates EvaluateReparentGuard,
  |      compares --approve-plan against the built fingerprint
  |
  |-- internal.BeginReparentRun(req, plan) ...................... §11.4 steps 3-9
  |      internal/reparent_exec.go
  |      3  acquireReparentModeLock(mode, featurePath)
  |             external: ClaimSyncRunGuard (internal/sync_run_state.go:246)
  |             checkout: AcquireCheckoutLock (internal/checkout_sync.go:287)
  |                       -- fresh route only; recovery uses ReclaimCheckoutLock (§4.10)
  |      4  resnapshotAndCompareFingerprint(...)      §8.5 steps 1-5
  |             re-read stack.yaml + SHA-256; re-resolve every row tip,
  |             the pinned destination and every cutoff; re-run the
  |             session-liveness probes; rebuild the plan; re-compute and
  |             compare the fingerprint. Mismatch -> plan-guard:
  |             revalidation-mismatch: state-preserved:, release the lock,
  |             leave NOTHING behind. Newly live session -> session-live.
  |      5  captureReparentPreImage(...)              §11.4 step 5 / §11.5
  |             every closure row's branch SHA; the EXACT stack.yaml bytes
  |             + SHA-256; every holder path/branch/HEAD; original_branch,
  |             original_head, original_detached; ref backend; oid width;
  |             the raw frozen validation command + source + digest
  |      6  saveReparentState(stage=initializing)     durableWriteFile, 0600
  |      7  writeReparentCompatArtifacts(mode)        §11.2 EXACT order (§4.10)
  |      8  verifyReparentStateDecodes(...)           re-read; confirm version
  |      9  advanceReparentStage(preflight)           and only then mutate
  |
  |-- internal.RunReparent(...) ................................. first mutation onward
```

`--continue` and `--abort` enter through `internal.ContinueReparent` / `internal.AbortReparent`,
which **skip steps 4–7** (no token, no re-approval, no re-capture) and instead reclaim the lock,
load the authoritative artifact, and re-enter at `resume_stage` (§4.8). §11.2a is the only path that
may re-create a missing compatibility artifact, and only after its four proofs.

Crash-window mapping for this ladder (§11.9): **window 1** = crash between step 6 and step 7
(artifact at `initializing`, no compatibility artifacts, nothing mutated) → `--continue` completes
step 7 under §11.2a, `--abort` removes the artifact and any pins. **Window 2** = crash after step 7,
nothing computed. Both are exercised by T-061.

### 4.1 The buffered Dir-based runner

`runReparentGit` (§1.3) is the only process spawner in the reparent-owned
planning/execution files, apart from the one policy-declared external fetch
that remains in package `cli` and delegates to `fetchQuietTo`:
`exec.Cmd.Dir` (never `-C`, AC-052/AC-055); stdout/stderr into run-owned buffers, never inherited
(§3.6, §9.11, AC-058); stdin from a caller buffer or nil, never `os.Stdin` (required by §9.11's
`--stdin` payload and §18 item 13's no-TTY rule); exit code via the existing `gitExitCode`
(`internal/rebase_plan_probe.go:63`); an `argv.log` hook for cheap audits.
`reparentIsAncestor` (§1.3) replaces `mergeBaseIsAncestor` inside the boundary.

### 4.2 External scratch vs checkout single checkout

| Mode | Computation context | Creation | Teardown |
|---|---|---|---|
| external | tool-owned scratch **linked worktree** at `<featurePath>/.reparent/<run-id>/scratch` | `git worktree add --detach <scratch> <first row pre-image sha>`, `Dir = repoRoot` (§9.2) | `git worktree remove --force <scratch>` then `git worktree prune`, **only after** the §11.6a step 2 cleanliness proof |
| checkout | the one physical checkout | `git switch --detach <first row pre-image sha>` after recording `original_branch`/`original_head`/`original_detached` and running the §9.4b gate (§9.3) | `git switch <original_branch>` or `git switch --detach <original_head>`; **never** `git reset --hard`; runs on completion **and** abort (§11.8 step 6) |

`externalSyncLayout.WorktreePath` (`internal/cli/sync_modes.go:205`) derives only
`<featurePath>/worktrees/<name>`, so `.reparent/` sits beside it and cannot collide. Checkout mode
MUST NOT call `git worktree add` (§9.5).

**`.reparent*` filtering (§9.2, T-042) — two halves, both required.**

*Half A — directory-name enumeration:*

| Surface | Site | Edit |
|---|---|---|
| import/adoption filter | `isRuntimeState` — comment `internal/cli/importcmd.go:173-176`, decl `:177`, body `:178-184` | add a `.reparent` **prefix** arm **and rewrite the comment** (§11 P-13) |
| external worktree fallback listing | `ListBranches`' `wtDir := filepath.Join(featurePath, "worktrees")` (`internal/paths.go`) | already scoped to `worktrees/` — **assert, change nothing** |
| feature-dir detection | `internal/paths.go:130`, `internal/resolve.go:271` (`sub[0] == "worktrees"`) | both require a literal `worktrees` component — **assert, change nothing** |
| space/feature signals | `internal/spaces.go:668` (`"stack.yaml"`, `"worktrees"`, `"FEATURE.md"`) | **assert, change nothing** |

The shipped `isRuntimeState` comment reads: *"The list is exact-name only: no prefix matching, so no
user file is filtered accidentally. Any future runtime-state file MUST be added here explicitly."*
The body already carries one prefix (`.tws/state/`), and adding `.reparent` makes prefix matching a
second, deliberate part of the contract. **The comment MUST be updated in the same edit** to state
that exactly two prefixes are matched (`.tws/state/` and `.reparent`) and that every other entry
remains exact-name.

*Half B — Git `worktree list --porcelain`-derived consumers:* filtering directory names is
insufficient, because `git worktree add` registers the scratch in the repository's common dir and it
therefore appears in porcelain output. Every consumer of `BuildWorktreeInventory`
(`internal/agent_status.go:527`, parser `:538`) MUST exclude the run's **recorded `scratch_path`**:
the holder projection (§4.9), the `tws status` worktree rows, `tws doctor`/`tws list`, and any
adoption scan. The exclusion is applied by reparent-aware callers, never inside
`BuildWorktreeInventory` itself, which stays frozen.

### 4.3 JIT destinations and the untracked gate

**Destinations (§9.4a).** A `parent-computed` destination is read **only** from the parent row's
persisted `planned_new_sha`, after that row reached row-stage `validated`, and confirmed against the
parent's `new` pin `refs/tws/reparent/<run-id>/new/<entry-id>`. Never from
`refs/heads/<parent branch>` (which still holds the pre-image), never from `HEAD`, never from any
live ref. A mismatch or a not-yet-`validated` parent refuses `probe-failed` (rank 39). AC-057/T-043
inject the divergence.

**Untracked (§9.4b).** Deferred and just-in-time, never static: preflight inventories untracked
non-ignored paths per consulted worktree → warning `untracked-present` only; before **every**
`git switch`/`git checkout` this run performs **and** before every holder re-attachment,
`git ls-tree -r --name-only <target-tree>` is intersected with
`git ls-files --others --exclude-standard` and a non-empty result refuses `untracked-overwrite`
(rank 29) naming every colliding path and the working tree; `-f`, `--force`, `--discard-changes` and
`--ignore-other-worktrees` are never passed (AC-064); the gate does not cover the run's own
artifacts, pins or scratch creation.

### 4.4 Pins before validation

`internal/reparent_refs.go` **NEW** owns the namespace, the id, the CAS builder/executor and the
classifier.

```
refs/tws/reparent/<run-id>/dest
refs/tws/reparent/<run-id>/old/<entry-id>
refs/tws/reparent/<run-id>/new/<entry-id>
```

`<run-id>` = 32 lowercase hex from `crypto/rand`.
`ReparentEntryRefID(feature, name string) string` (**NEW**, exported) follows the shipped
`hashedSessionID` algorithm (`internal/session.go:127-135`): `sha256(identity)`, first 4 bytes hex,
a sanitized prefix, `prefix + "_" + suffix`, capped at 64 bytes via `max := 64 - len(suffix) - 1`.
`sanitizeSessionPart` (`internal/session.go:143-153`) keeps `[A-Za-z0-9-_]` and trims `_`, which is
**not** sufficient for a ref path component: §9.8 additionally forbids a leading `-` or `.`, `..`,
`@{` and a trailing `.lock`, and permits `.`. Declare `sanitizeReparentRefPart` **NEW** rather than
widening `sanitizeSessionPart`, which would change every existing tmux/session name. The raw
`StackEntry.Name` is never a ref component — `a` vs `a/b` would collide as a ref directory and file
(T-026; the same fact issue #1 records).

**Ordering (§9.8, AC-061):** old pins and the destination pin at stage `pinning-preimages`, before
the first computation; a row's **new pin immediately after that row's `git rev-parse HEAD`**
(§9.4 step 6) and **before** its validation (§9.4 step 7) and before any later row. Stage
`pinning-computed` is an **idempotent verify-all gate**: it confirms every new pin resolves to the
recorded `planned_new_sha`, re-creates missing pins and refuses `probe-failed` on a mismatch. All
pins survive until `cleanup`, across conflict pauses, validation failures, crashes and
`git gc --prune=now`.

### 4.5 The CAS transaction, including no-op `verify` lines

One invocation, `Dir = repoRoot`, stdin from a buffer:

```
git update-ref -m "tws reparent <feature> <target> <run-id>" --stdin
start
update refs/heads/<row 1 branch> <new 1> <old 1>
verify refs/heads/<no-op row branch> <old value>
...
prepare
commit
```

- rows in `ReparentClosureOrder`; every `update` carries the explicit expected old value;
- a row whose `planned_new_sha == preimage_sha` emits **`verify <ref> <preimage>`**, is recorded
  `applied = true, noop = true`, and is classified by §11.7 against the **live** ref; it MUST NOT
  emit `update <ref> X X`;
- an all-no-op closure still runs `start`/ordered `verify`s/`prepare`/`commit` — the verification is
  the race barrier;
- no `create`, no `delete`, no `option no-deref`, no ref outside the closure;
- **both** child streams captured — precisely why `RunDirTo` is insufficient (it would inherit
  `os.Stdin` and could not carry the payload);
- a failed `prepare` refuses `ref-transaction-mismatch` (rank 43) **after** attempting to re-attach
  every holder detached for this transaction; `state-preserved` only when all restores succeeded;
- a `reference-transaction` hook veto is an ordinary refusal, never a crash, and the hook is never
  disabled (AC-060).

### 4.6 Post-image bytes and the durable writer

`internal/stack.go` gains three functions (§10.3, §10.3a):

```go
func WriteStackBytesAtomic(featurePath string, data []byte) error  // exported primitive
func SaveStackAtomic(featurePath string, s Stack) error            // marshals exactly as SaveStack
func durableWriteFile(path string, data []byte, mode os.FileMode) error // unexported
```

Measured baseline, which is exactly why all three are needed:

- `SaveStack` (`internal/stack.go:128`) is `yaml.Marshal` + `os.WriteFile(StackPath, data, 0644)` —
  **not atomic, not durable, no temp file, no rename, no fsync**. §14.1 freezes it and every caller;
  §18 item 11 forbids converting them.
- `atomicWriteFile` (`internal/checkout_sync.go:151`) does `MkdirAll(dir, 0700)`, `os.CreateTemp`
  (`.tws-state-*`), `Chmod(mode)`, `Write`, **`tmp.Sync()`**, `Close`, `os.Rename` — it fsyncs the
  *file* but **never the parent directory**, so the renamed directory entry is outside a
  machine-crash durability claim. §14.1 freezes it and every caller; `durableWriteFile` is added
  **beside** it in `internal/stack.go`.
- `SaveSyncState` (`internal/syncstate.go:45`) and `SaveSyncRunState`
  (`internal/sync_run_state.go:154`) route through `atomicWriteFile` (`0644` / `0600`);
  `writeSyncGuardExclusive` (`internal/sync_run_state.go:375`) is `O_WRONLY|O_CREATE|O_EXCL`, `0600`,
  no rename, no explicit fsync.

Everything whose loss would strip this run's only record uses `durableWriteFile`: `stack.yaml`
(through `WriteStackBytesAtomic`), the reparent state artifact, both compatibility artifacts, and
the remote follow-up record.

**Post-image ordering (§9.12, §10.5, AC-070):** built and persisted at stage `building-post-image`,
**before** the CAS — `stack_after_base64` + `stack_sha256_after_expected` land durably first. At
`writing-metadata`, the live file's SHA-256 must equal `stack_sha256_before` (else `metadata-drift`,
rank 46) and the **persisted bytes are written verbatim**, never re-marshalled, because YAML
round-tripping can reorder keys, drop comments and change quoting.

**Fault injection (§10.3b).** `syncIOFault(op, path)` (`internal/syncstate.go:110`) consults the
injected seam; its tokens are a `const` block at `internal/syncstate.go:95-105`, eleven tokens
ending with `SyncIOReloadStack` (`:105`). **Append** exactly four after `:105`, never reorder:

```go
SyncIOWriteStack          = "write-stack"           // WriteStackBytesAtomic
SyncIOWriteReparentState  = "write-reparent-state"  // §11.1 artifact
SyncIOWriteReparentCompat = "write-reparent-compat" // §11.2 compatibility artifacts
SyncIOWriteReparentRemote = "write-reparent-remote" // §12.3 record
```

`durableWriteFile` must honour the seam at **two** points — before the rename, and after the rename
but before the directory fsync — so T-060/AC-072 can assert survival of the previous file and
recoverability respectively.

### 4.7 The 19-stage state machine, §11.5 capture, and the monotonic commit point

`internal/reparent_state.go` **NEW**: `ReparentStateVersion = 1`, the 19 stages of §11.3 as a closed
`ReparentStage` string type (modelled on `SyncRunStage`, `internal/sync_run_state.go:36`, and
`CheckoutStage`, `internal/checkout_sync.go:21-32`), the §11.5 payload, `RefuseIfReparentActive`,
artifact classification, the compatibility writers, and the `ReparentProjection` type (§11 P-11).

| Mode | Path | Mode bits |
|---|---|---|
| external | `<featurePath>/.reparent-state.v1.yaml` | `0600` |
| checkout | `<ws.CheckoutStateDir()>/<feature>-reparent.v1.yaml` — `internal/resolve.go:285`, **not** `checkoutStateDir(featurePath)` (§11 P-5) | `0600` |

**19 stages, exactly:** `initializing`, `preflight`, `pinning-preimages`, `computing`,
`conflict-paused`, `building-post-image`, `pinning-computed`, `writing-remote-record`,
`detaching-holders`, `committing-refs`, `refs-committed`, `writing-metadata`, `metadata-written`,
`restoring-holders`, `cleanup`, `completed`, `aborting`, `aborted`, `failed`. `fetching` and
`planning` are **not** stages — both happen before the lock and before the artifact exists, so no
artifact can ever be found at either. `metadata-written` is spelled identically in state, §11.3,
§11.6 and §11.9 (AC-074).

**§11.5 / §11.4 exact capture.** `captureReparentPreImage` (§4.0 step 5) must persist, before any
mutation: `state_version`, `run_id` (32 hex), `created_at`/`updated_at` RFC3339 UTC,
`workspace_mode`, `workspace_stable_id`, `feature`, `repo_root`, `repo_common_dir`, `ref_backend`,
`oid_width`, `strategy`, `backend`, `stage`, `resume_stage`, `owner_pid`, `owner_token`; the target
and both parent tuples; `destination_pin_ref`; `cutoff_supplied_token`; the full `rows[]` (order,
name, git_branch, repo, role, `preimage_sha`, `cutoff_sha`, `cutoff_provenance`,
`destination_binding`, `destination_parent`, `destination_sha`, `planned_new_sha`, `noop`,
`base_before`/`base_after`, `last_base_sha_before`/`after`, `argv[]` template,
`materialized_argv[]`, `effective_backend`, `candidate_count`, `candidate_digest`, `old_pin_ref`,
`new_pin_ref`, row `stage`); `validation_command_raw` (0600 artifact only, never a plan field),
`validation_source`, `validation_command_digest`; `scratch_path` (external);
`original_branch`/`original_head`/`original_detached`; `preimage_holder_excluded`;
`detached_holders[]`; `stack_before_base64` and `stack_sha256_before`; and the limits,
`approved_fingerprint` and `fetch_policy`. `stack_after_base64` and
`stack_sha256_after_expected` are added at `building-post-image`; `stack_sha256_after` after the
write; `cas_rows[]`, `abort_rows[]`, `remote_record_written` and `remote_followup_entries[]` as they
occur.

`stack_before_base64`/`stack_after_base64` carry the **exact bytes**, not a re-marshalled struct.

**Monotonic `commit_point_reached`.** Set durably the instant §11.8a's conjunction first holds —
the exact post-image bytes durable in `stack.yaml` (SHA-256 == `stack_sha256_after_expected`)
**and** every affected ref classifying `planned tip` or `no-op` — and **never cleared**, even if an
operator later advances a branch. A live ref that is a *descendant* of its planned tip is also
sufficient evidence and the movement is reported informationally; a ref *unrelated* to its planned
tip cannot establish the commit point and remains a pre-commit
`ref-foreign-value`/`abort-foreign-value` refusal. A crash after the metadata rename but before the
marker update is recovered by re-evaluating the conjunction from disk.

**Post-commit drift is forward cleanup, never a block.** At `metadata-written`,
`restoring-holders`, `cleanup` and `completed`, with `commit_point_reached == true`, later ref
movement is reported informationally and MUST NOT block holder restoration or cleanup; the stack
hash is classified by §11.6 rather than compared only with `stack_sha256_before`.

`resume_stage` is persisted in the **same durable write** as every `stage` transition and equals
`stage` except while `stage` is `failed`/`aborting`/`aborted`/`completed`.

**`writing-remote-record` is a hard dependency of the CAS.** The stage exists so that the record is
durable **before** any public ref moves (§12.3a): the executor MUST NOT enter `detaching-holders` or
`committing-refs` until `remote_record_written` is true (or the record is provably empty). This is
why `internal/reparent_remote.go` is built before the executor in §8.

### 4.8 Continue and abort

`--continue` re-enters at `resume_stage` per §11.6's table, which has **exactly 17 rows**
(`initializing`+`preflight` share one row; `aborting`+`aborted` share one row). It is idempotent at
every row and takes **no** approval token and **no** replay limit (AC-077); §3.5 rule 4 refuses both
before any lock or Git command. A `--continue` against `aborting`/`aborted` refuses rank 34
`reparent-state-present` with the re-run-abort detail.

`--abort` evaluates §11.8a from disk first, sets `aborting` before its first action, and is a
bounded rollback before the commit point / **forward completion only** afterwards. Its CAS
(§11.8 step 4) emits `update <ref> <preimage> <planned>` for each planned-tip row and
**`verify <ref> <preimage>`** for every pre-image **and** every no-op row, all in
`ReparentClosureOrder`, persisting each outcome into `abort_rows[]` before the next action so a
crashed abort resumes idempotently. The verify lines close the race between the §11.7 classification
(step 2) and commit without moving those refs.

Step 3's **re-detach of already-restored holders** is load-bearing and easy to miss: a holder
restored by a completed `restoring-holders` stage is attached to a branch the rollback is about to
move, and moving a ref out from under an attached worktree leaves its index and HEAD inconsistent.

**All 17 crash windows of §11.9** must be exercised for both verbs, each run twice (T-061/AC-074):
1 artifact-without-compat · 2 compat-written-nothing-computed · 3 scratch-created ·
4 mid-row conflict · 5 computed-not-pinned · 6 pinned-not-validated · 7 all-computed-no-post-image ·
8 post-image-no-record · 9 record-no-CAS · 10 CAS-prepare-failed · 11 files-backend-partial-commit ·
12 CAS-committed-metadata-unwritten · 13 metadata-written-holders-unrestored ·
14 holders-restored-cleanup-incomplete · 15 mid-abort · 16 operator ref move **before** the commit
point · 17 operator ref advance **after** the commit point.

### 4.9 Holders and the worktree inventory filter

`BuildWorktreeInventory(repoRoot string) WorktreeInventory` (`internal/agent_status.go:527`) runs
`git -C <repoRoot> worktree list --porcelain` and parses it with `parseWorktreeInventory` (`:538`).
It returns `WorktreeInventory` (`:497`): `Available bool`, `ByBranch map[string]string`,
`Prunable map[string]bool`, `Records []WorktreeRecord`, `ByPath map[string]WorktreeRecord`,
`Err error`; `WorktreeRecord` (`:476`) carries `Path`, `Head *string`, `BranchRef *string`,
`Detached *bool`, `Bare`, `Locked`, `LockReason *string`, `Prunable`, `PrunableReason *string`.
Prunable branches go to `Prunable` and non-prunable to `ByBranch` at `:575-579`; malformed porcelain
fails **closed**.

**Reuse it unchanged.** It supplies §6.2's `holder-unsafe` inputs directly: `Prunable` is the
prunable/stale test, and multiple holders of one branch are detectable from `Records`. Its internal
`-C` usage is a shipped read-only probe, not an argv this feature *emits* (§11 P-4). Two
reparent-specific filters layer **on top**, never inside it:

1. exclude the run's recorded `scratch_path` from every holder projection and from every
   porcelain-derived enumeration (§4.2 half B);
2. in checkout mode, exclude the **computation context** from the §9.9 still-attached drift check —
   it was deliberately detached by §9.3's initial switch, recorded once as
   `holder_kind = "computation-context"`, `action = "already-detached"`, `restored: false`, and
   published as `holders.preimage_holder_excluded`. Without this exclusion **every** checkout-mode
   run refuses `holder-unsafe`.

A post-commit-point restore failure warns `holder-restore-deferred` (never the refusal
`holder-unsafe`), is persisted, and is re-attempted by a later `--continue` (AC-096).

### 4.10 Compatibility artifacts, locks, and old-binary downgrade

**External**, in this exact order (§11.2), all after the authoritative artifact (§4.0 step 6):

1. `ClaimSyncRunGuard` (`internal/sync_run_state.go:246`) for `<featurePath>/.sync-run.lock`
   (`SyncRunGuardPath`, `:122`), released with `ReleaseSyncRunGuard`, reclaimed when stale with
   `ReclaimSyncRunGuard` (`:317`); (the lock itself is §11.4 step 3, taken before the artifact);
2. `<featurePath>/.sync-state.v2.yaml` (`SyncRunStatePath`, `:117`) **first**, as a raw YAML document
   with `state_version: 4`, `route: "reparent"`, `feature`, `stage: "rebasing"`, the frozen limits
   and `selected` = closure names, mode `0600`, via `durableWriteFile`;
3. the legacy sentinel `<featurePath>/.sync-state.yaml` (`SyncStatePath`,
   `internal/syncstate.go:26`) **second**, mode `0644`, via `durableWriteFile`.

Write order matters: with the v4 payload already present, no instant exists at which a concurrent
old binary observes a lone legacy sentinel and routes itself into a legacy-resume cell.

`writeReparentCompatSyncPayload` is a **reparent-owned raw YAML writer**. `SaveSyncRunState`
(`internal/sync_run_state.go:154`) MUST NOT be used or modified — it rejects any `state_version`
other than 2 (`:21`) or 3 (`:25`) by design, and `LoadSyncRunState` (`:131`) in every shipped release
accepts only those two, which is what makes an old binary emit
`unsupported scoped sync state version 4`.

**`SaveGuardedLegacySentinel` — corrected rationale (§11 P-9).** The sentinel is likewise serialized
and written by the reparent-owned writer, but **not** because
`SaveGuardedLegacySentinel`'s compare-and-swap rejects an adjacent v4 payload. Measured:
`SaveGuardedLegacySentinel(featurePath string, s *GuardedLegacySentinel, expect []byte) error`
(`internal/syncstate.go:243`) consults `syncIOFault(SyncIOWriteSentinel, path)` at `:245` and
CAS-checks **its own sentinel bytes** against `expect`; it never inspects
`.sync-state.v2.yaml`. The two real reasons, now stated in the corrected spec, are:

1. it emits the guarded **legacy-sync** shape, whereas this boundary needs **reparent identity**
   (the run's marker/token) in the sentinel; and
2. it writes through `atomicWriteFile`, which does not fsync the parent directory, whereas §10.3a
   requires `durableWriteFile(..., 0644)`.

The new writer reproduces the shipped sentinel marker shape exactly
(`GuardedLegacySentinelVersion = 3`, `internal/syncstate.go:182`, enforced at `:366`), adds the
reparent identity, and writes durably at mode `0644`.

**Checkout** (§11.2):

1. lock via `AcquireCheckoutLock(featurePath)` (`internal/checkout_sync.go:287`) for
   `CheckoutLockPath(featurePath)` (`:122`) on the **fresh** route;
2. `CheckoutTransactionPath(featurePath)` (`:118`) written as a **deliberately undecodable**
   compatibility transaction: `state_version: "4-reparent"` — a **string** where every shipped binary
   declares an `int` — plus `route`, `feature`, `reparent_run_id`,
   `reparent_marker: tws-reparent-compat-v1`, and both limits. Mode `0600`, `durableWriteFile`.

Verified against shipped control flow, which is exactly why a string is used:

- `HasCheckoutTransaction` (`:183`) is a bare `os.Stat` → plain `tws sync` still refuses as today;
- `LoadCheckoutTransaction` (`:128`) yields a YAML type error → **both** `ContinueCheckoutSync`
  (`:1065`) and `AbortCheckoutSync` (`:1200`) return `no transaction to continue/abort: …` before
  the lock, before `git rebase --abort`, before `restoreOriginal` (`:1661`) and before any deletion.
  `AbortCheckoutSync` performs **no version check at all**, so an integer `4` would let a prior
  binary "roll back" a reparent run it does not understand.

New reparent code MUST NOT parse this file as a `CheckoutTransaction` (`:64`); it detects its own run
by reading raw YAML and matching `reparent_marker`. The real `original_branch`/`original_head` live
**only** in the authoritative artifact (§11.5).

**`ReclaimCheckoutLock` — typed verdict, not a trivial wrapper (§11 P-7).** Measured
`forceAcquireCheckoutLock` (`internal/checkout_sync.go:364-389`): `MkdirAll` the lock dir →
`ReadFile`; `IsNotExist` → `writeLockExclusive` (`:324`, `O_CREATE|O_EXCL`, `0600`); other read error
→ error; `yaml.Unmarshal` into `LockInfo` (`:282`), invalid → error; `info.PID <= 0` → "being
initialized or is invalid"; **`if info.PID != os.Getpid() && isProcessAlive(info.PID)` (`:383`) →
`lock held by live process %d; cannot reclaim` (`:384`)**; else `removeLockIfUnchanged` (`:342`) →
`writeLockExclusive`. It already refuses a live foreign PID and already compares bytes before
removing.

A one-line wrapper is **insufficient**: reparent must map the live-foreign case to
`sync-state-present` with its own detail, and a caller cannot distinguish that error from
"invalid lock", "unreadable lock" or "mkdir failed" by string matching. Two acceptable designs, both
of which forbid a second pre-read ladder (which would be race-prone):

- **(a) typed sentinel, behaviour-preserving.** Introduce
  `type CheckoutLockLiveError struct{ PID int }` with
  `Error() string { return fmt.Sprintf("lock held by live process %d; cannot reclaim", e.PID) }` —
  **byte-identical to `:384`** — and return it at `:383-385`. Every existing caller that only
  inspects the message is unaffected; reparent uses `errors.As`. `ReclaimCheckoutLock` is then the
  exported wrapper.
- **(b) shared body, typed verdict.** Extract the ladder into
  `reclaimCheckoutLock(featurePath string) (checkoutLockVerdict, error)` returning
  `{acquired | liveForeign | invalid}`; `forceAcquireCheckoutLock` becomes a thin adapter that
  reproduces its current error strings exactly, and `ReclaimCheckoutLock` is the exported adapter
  that surfaces the typed verdict.

**(a) is the smaller edit and is the chosen one**; (b) is recorded as the acceptable alternative if
the implementer finds a second consumer. Either way `forceAcquireCheckoutLock` keeps every existing
caller and every existing message, and **nothing in code, comments, docs or tests may claim tws
currently steals live locks**. Route assignment: a **fresh** reparent execution uses
`AcquireCheckoutLock` (`:287`, whose own live-lock refusal at `:311` reads
`…cannot steal live lock`); **recovery** routes (`--continue`, `--abort`) use
`ReclaimCheckoutLock`. `ReleaseCheckoutLock` (`:353`) is a bare `os.Remove`.

### 4.11 Cleanup order

§11.6a, tolerant of any step already being done, artifact **last**:

1. delete every pin under `refs/tws/reparent/<run-id>/`;
2. prove the scratch worktree clean, then `git worktree remove --force` + `git worktree prune`
   (external only); residue refuses `context-dirty`, preserves state and the scratch path, and —
   after the commit point — says the topology change is already committed and only cleanup pends;
3. delete `.sync-state.yaml` (external);
4. delete `.sync-state.v2.yaml` (external) or the compatibility transaction (checkout);
5. release the lock (`ReleaseSyncRunGuard` / `ReleaseCheckoutLock`, `internal/checkout_sync.go:353`);
6. delete the reparent state artifact **last** — it is the only record of steps 1–5.

---

## 5. Remote, status, session and help surfaces

### 5.1 The three live push paths, measured

| # | Route | Entry point | Push argv site |
|---|---|---|---|
| 1 | external `tws push <feature>` | `pushCmd` (`internal/cli/push.go:11`) → `pushFeature` (`:57`) → `pushEntries` (`:123`) | `:144` `internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.GitBranch())` |
| 2 | external `tws sync <feature> --push` | `pushEntries` (`:123`) legacy/all scope, or `pushScoped` (`:70`) scoped | `:144` / `:103` (identical argv) |
| 3 | checkout `tws sync <feature> --push` | `gitPush` (`internal/checkout_sync.go:540`), called at **`:1628`** (normal) and **`:1645`** (retry) | `exec.Command("git","push","--force-with-lease","origin",branch)`, `cmd.Dir = repoDir`, `CombinedOutput()` |

`pushFeatureCheckout` (`internal/cli/push.go:157`) is **not a fourth path**: it calls
`internal.RequireWorktreePath` at **`:169`**, which fails with `ErrWorktreeUnsupported` before any
Git push, and must stay unsupported — including its dry-run branch at `:180-183` (§12.4, §14.2
item 6).

**Rule R-PUSH edits, per path (smallest coherent):**

- **`pushEntries` (`:123-153`)** — load the record once **before** the loop; run the invocation-wide
  preflight (§12.4 rule 2) before the first real push; inside the loop, for a pending entry print the
  `reparent-remote:` line on stderr and swap `:144`'s argv for
  `"git","push","--force-with-lease","--force-if-includes","origin",entry.GitBranch()`. The
  **dry-run branch at `:133-136` gets §12.4a**: the same warning line on stderr, the preview text
  `  [~] %s (would push --force-with-lease --force-if-includes)`, a
  `reparent-remote: would refuse: remote-followup-unsafe-lease: <detail>` diagnostic instead of a
  refusal, exit 0 and **no write**. `:134` currently uses `fmt.Printf` (stdout); the new warning goes
  to **stderr** while the `[~]` preview keeps its current stream, so the no-record golden is
  byte-identical.
- **`pushScoped` (`:70-121`)** — same record load and invocation-wide preflight before the loop; argv
  swap at `:103`. It has **no** dry-run branch and gains none.
- **`gitPush` (`internal/checkout_sync.go:540`) is edited directly** — §15 says
  "`internal/checkout_sync.go`  rule R-PUSH in gitPush", and that is the normative instruction. A
  sibling function would leave both shipped call sites (`:1628`, `:1645`) on the old argv and the
  rule would simply not fire in checkout mode. Resolved shape:

  ```go
  // gitPush gains one parameter carrying this entry's already-made decision.
  // The zero value reproduces today's argv and output byte-for-byte.
  type reparentPushDecision struct {
      ForceIfIncludes bool    // true only for a pending record entry
      Warning         string  // the reparent-remote: line, "" when absent
  }
  func gitPush(repoDir, branch string, d reparentPushDecision) error
  ```

  Both call sites pass a per-branch decision computed from the record, and the
  **invocation-wide preflight runs above each loop** — once before the normal push loop containing
  `:1628` and once before the retry path containing `:1645` — never per entry, because pushing half
  a stack and then discovering the lease cannot be strengthened is exactly what §12.4 rule 2
  prevents. With no record, both sites pass the zero value and the argv, the output and the exit
  status are unchanged.

Rule 1's "byte-identical without a record" is what preserves
`testdata/sync_noflag/declared_c2/push` and `declared_c2/sync-push` (AC-089, AC-099).

### 5.2 External dry-run only

§12.4a applies to **exactly one** reachable preview: external `tws push --dry-run` →
`pushEntries:133-136`. Checkout top-level `tws push --dry-run` is unreachable past
`RequireWorktreePath` (`internal/cli/push.go:169`) and gains nothing. `pushScoped` has no preview. A
dry-run exits **0**, runs no `git push`, and writes, marks or deletes nothing (AC-091).

### 5.3 Remote follow-up record

`internal/reparent_remote.go` **NEW**: `record_version 1`; paths
`<featurePath>/.reparent-remote.v1.yaml` (external) and
`<ws.CheckoutStateDir()>/<feature>-reparent-remote.v1.yaml` (checkout); mode `0600`;
`durableWriteFile`; written at stage `writing-remote-record` — **after** every row is computed,
validated and pinned, and **before** holder detachment and the CAS (§12.3a, AC-088). The executor
may not enter `detaching-holders` until `remote_record_written` is true (§4.7).

Clearing (§12.5) is observed locally with no fetch, and only **mutating** routes persist a clear:
every real push path, every push-enabled sync, and a fresh reparent execution or recovery route.
`tws doctor`, `tws list`, `tws status`, `tws stack status` MUST NOT evaluate the record at all; a
`ReparentPlan` may read it only to publish `remote-followup-pending`. That warning MUST NOT be added
to `RebasePlan`, whose warning domain is frozen at eight members (§14.1, AC-042).

### 5.4 Status/doctor/list/agent projections and forbidden compatibility hints

| Surface | Site | Edit |
|---|---|---|
| `tws stack status` (human + JSON) | `internal/stack_status.go:614` `BuildStackStatus`; `StackStatusReport` `:86` (`SchemaVersion` `:87`) | add `Reparent *ReparentProjection \`json:"reparent,omitempty"\`` — **absent**, not `null`, without state; `SchemaVersion` stays `1` (AC-085) |
| agent status JSON | `internal/agent_status.go:803` `BuildAgentStatus` | same omitempty-pointer key; projection computed **before** `buildFeatureSync` (call `:1268`) |
| external sync projection | `internal/agent_status.go:1354` `buildFeatureSync` (method on `*statusBuilder`; checkout arm delegates at `:1363`) | suppress `IssueSyncStateInvalid` (`:113`) at `:1433`, `:1523` (with the `remove it manually` hint at `:1525`) and `:1559` while a reparent artifact exists |
| checkout sync report | `internal/checkout_health.go:402` `buildOneSyncReport` | suppress `state file unreadable; manually remove ` (`:412`) and `corrupt transaction state; manually remove ` (`:420`); also suppress the `corrupt lock file; manually remove ` defensive branch (`:453`), although the undecodable transaction returns at `:420` before that branch in the normal compatibility-artifact fixture |
| `tws doctor` | `internal/cli/doctor.go:12` `doctorCmd`, `:94` `checkFeatureE`, `:139` `runCheckoutDoctor` | the §11.10 stderr line + guidance suppression |
| `tws list` | `internal/cli/list.go:12` `listCmd`, `:117` `runCheckoutList` | the §11.10 stderr line |
| ancestry guidance | `internal/stack_ancestry.go:625` `ancestryGuidance` | a **suppression seam only**, no ancestry logic change; `EvaluateStackAncestry` (`:706`), `FeatureStackEdges` (`:846`), `stackBaseRef` (`:318`) and `ancestryMergeBase` (`:297`) stay untouched |
| external doctor/list guidance | `internal/health.go:53` `AncestryHealthIssues` | consume the same seam (§11 P-10) |

**`ReparentProjection` owner: `internal/reparent_state.go`** (§11 P-11). It is derived from the
authoritative artifact, it is the type both `stack_status.go` and `agent_status.go` embed, and
placing it in `reparent_state.go` keeps the artifact's decode logic and its projection adjacent.
Both embedders import nothing new — they are already in package `internal`.

The anchored line, on **stderr**, exactly once per affected feature, **before** any ancestry write to
stdout (§11.10 rule 1 is a command write-order requirement, not a terminal-interleaving claim):

```
reparent in progress: <feature> target <entry> run <run-id> stage <stage>; continue with: tws stack reparent <feature> --continue (or --abort)
```

All surfaces stay **strictly read-only**: no fetch, no ref write, no state repair, and no marking or
deletion of the remote record (§11.10 rule 3). The artifact is classified as exactly one of
`active | complete | unsupported | corrupt | foreign | stale` (rule 4).

**Forbidden compatibility hints (AC-084).** While the authoritative artifact exists, no surface may
emit `IssueSyncStateInvalid`, `state file unreadable`, `corrupt transaction state`,
`corrupt lock file`, `remove it manually` or `manually remove` **about the §11.2 compatibility
files or the checkout lock**. They are correct guards, not corrupt sync state; following such a hint
would bypass the transaction, and the sync verbs named are refused by §11.2 anyway. Ancestry
**status** and **reason** are still reported — only the guidance is replaced.

### 5.5 Session launch exclusion

§14.2a: while a reparent artifact exists for a feature, `tws open <feature> …`
(`internal/cli/open.go:14` `openCmd`, `:203` `resolveOpenArgs`, `:248` `openAll`, `:288`
`openWithTmux`) and every checkout agent-session launch
(`internal/session.go:279` `CheckoutSessionPreconditions`, reached by `OpenCheckoutDirect` `:557`
and `OpenCheckoutTmux` `:616`) MUST refuse with the §11.2 sentence, start no session and attach no
terminal. The refusal lifts the instant the artifact is gone.

`CheckoutSessionPreconditions` is the correct single insertion point for the checkout half — both
open paths funnel through it and it already calls `anyCheckoutSyncActive(ws.MetadataRoot)` at `:295`
(declaration `:311`). `tws open`'s external half needs its own check in `openCmd`/`openAll`.

Symmetry worth a code comment: §6.2's `session-live` protects the reparent from sessions; §14.2a
protects sessions from the reparent, which has detached holders and is about to move the very
branches a session would sit on.

### 5.6 Help and completion

- `tws stack --help` gains one line; pinned by `internal/cli/testdata/reparent/stack_help.txt`.
- `tws stack status --help` MUST be unchanged (§14.2 item 1).
- `tws stack reparent --help` pinned by `internal/cli/testdata/reparent/reparent_help.txt`; AC-008's
  negative flag assertions read off it. Flag order in that golden is **pflag's lexical sort**
  (`SortFlags` defaults to `true`), not registration order.
- `internal/cli/testdata/rebase_plan/sync_help.txt` MUST be unchanged (AC-099) — guaranteed by the
  §2.3 constant hoist, which changes no usage string.
- `stackCmd.ValidArgsFunction` suppresses `reparent` (§2.1 edit 1); `stackStatusCmd`'s
  `ValidArgsFunction` (`internal/cli/stack_status.go:44-51`) stays deliberately unfiltered.

---

## 6. File ledger

### 6.1 NEW — 10 production files (§15, reconciled; no `reparent_plan_guard.go`)

| # | File | Package | Contents | Smallest coherent edit |
|---:|---|---|---|---|
| 1 | `internal/reparent_plan.go` | `internal` | `ReparentPlan` (25 keys), `ReparentPlanRow` (23), `ReparentPlanPolicy`, `ReparentPlanValidation`, `ReparentParent`, `ReparentResolverAgreement`/`Verdict`, `ReparentPlanCutoff`, `ReparentRowPins`, `ReparentPlanMetadataDelta`/`Entry`, `ReparentPlanStrategy`/`Atomicity`, `ReparentPlanHolders`/`Holder`, `ReparentPlanRemote`/`Row`/`Followup`, `ReparentPlanState`/`StateFiles`/`Exclusion`, `ReparentPlanBlocker`/`Warning`/`Refusal`/`Summary` (11)/`Approval`/`ApprovalCovers` (8), `ReparentRefusalKind` + the **49**-member `ReparentRefusalKinds`, the **11**-member warning domain, `SelectPrimaryReparentRefusal`, `EvaluateReparentGuard`, `ReparentWriters` | types + two pure functions |
| 2 | `internal/reparent_plan_build.go` | `internal` | `ReparentRequest` (incl. `Fetch PlanFetchOutcome`), `BuildReparentPlan`, closure computation, scope gates, `reparentFetchContext`/`reparentFetchPlan`/`reparentFetchProjection`, the four reparent limit-algebra adapters, `RevalidateReparentRow`, validation-identity projection, `reparentGuardRun`, `PlanReparent` | pure builder; no fetch, no `RebasePlanRequest` |
| 3 | `internal/reparent_plan_fingerprint.go` | `internal` | the 33-field tuple, the three `reparentFingerprint*` constants, `ReparentPlanFingerprint`, `ReparentPlanFingerprintPreimage`, **`ReparentRevalidationDigest`** | reuses `tlvEncoder` (`rebase_plan_fingerprint.go:60`) and its 10 methods |
| 4 | `internal/reparent_plan_render.go` | `internal` | `FormatReparentPlan`, `MarshalReparentPlan` | reuses `ensureSlice` (`rebase_plan_render.go:440`) |
| 5 | `internal/reparent_destination.go` | `internal` | `ResolveReparentDestination`, the five-candidate enumeration, `--disambiguate` OID resolution, `reparentOIDWidth`, the cutoff ladders, **`reparentIsAncestor`**, the four-resolver agreement adapter | calls `ResolveSyncBase` (`rebase_planner.go:215`), `stackBaseRef` (`stack_ancestry.go:318`) and `checkoutBaseTokenFor` unmodified |
| 6 | `internal/reparent_state.go` | `internal` | `ReparentStateVersion = 1`, the 19 stages, the §11.5 payload, both path helpers, load/save/remove, `RefuseIfReparentActive`, artifact classification, the compatibility writers incl. `writeReparentCompatSyncPayload`, **`ReparentProjection`** | `RefuseIfReparentActive` = `Stat` + at most one `ReadFile`, **zero Git processes** |
| 7 | `internal/reparent_refs.go` | `internal` | pin namespace, `ReparentEntryRefID`, `sanitizeReparentRefPart`, the `update-ref --stdin` CAS builder/executor, the §11.7 four-way classifier | reuses `hashedSessionID`'s algorithm shape (`session.go:127`), **not** `sanitizeSessionPart` |
| 8 | `internal/reparent_exec.go` | `internal` | **`runReparentGit`** + `reparentGitResult`, `BeginReparentRun`, `RunReparent`, `ContinueReparent`, `AbortReparent`, scratch/checkout context, per-row rebase, holders, metadata write, cleanup, `fetchReparentCheckoutRepo` | reuses `gitExitCode` (`rebase_plan_probe.go:63`) and delegates to `fetchCheckoutRepoTo` (`checkout_sync.go:777`) |
| 9 | `internal/reparent_remote.go` | `internal` | the record type + durable IO, §12.2 guidance, §12.5 clearing, rule R-PUSH decision helpers | supplies `reparentPushDecision` inputs to all three push paths |
| 10 | `internal/cli/stack_reparent.go` | `cli` | `stackReparentCmd`, `stackReparentArgs`, `stackReparentValidate`, `reparentOntoCompletion`, `reparentOptions`, route dispatch, **the external fetch execution via `fetchQuietTo`**, stream selection, the `reparent:` / `reparent-recovery:` / `reparent-remote:` marker writers | the only new Cobra file |

Split note: the CAS **builder/executor** lives in `_refs.go`; the stage machine deciding *when* to
run it lives in `_exec.go`. That split is what makes the pure CAS-shape tests (T-044) runnable
without a stage machine.

### 6.2 CHANGED — production

| File | Edit | Anchor | Named by §15? |
|---|---|---|---|
| `internal/cli/stack.go` | `|| name == "reparent"` in the completion filter; `cmd.AddCommand(stackReparentCmd())` | `:24`; after `:54` | yes |
| `internal/stack.go` | `ReparentClosureOrder` after `TopoSort`'s close; `WriteStackBytesAtomic`, `SaveStackAtomic`, `durableWriteFile` | after `:182`, before `:185`; new funcs near `:128` | yes |
| `internal/syncstate.go` | append exactly four `SyncIO*` tokens | after `:105`, never reordered | yes |
| `internal/git_capability.go` | `ReparentGitCapabilities` + `ReparentGitCapabilitiesForVersion` **beside** the frozen table | after `:150` | yes |
| `internal/cli/sync.go` | the reparent precheck block; hoist `feature`/`twsRoot`/`GuardFeatureName` above the mode dispatch | between `:88` and `:93`; `:108`/`:112`/`:113` folded into the hoist; `:116` keeps its `twsRoot` argument | yes |
| `internal/checkout_sync.go` | extract `checkoutBaseTokenFor` from `:590-596`; add `ReclaimCheckoutLock` + the typed live-lock error at `:383-385`; **rule R-PUSH inside `gitPush` (`:540`)**, both call sites `:1628` and `:1645`; per-loop invocation-wide preflight | as listed | yes |
| `internal/cli/push.go` | rule R-PUSH in `pushEntries` (`:123`, argv `:144`) and `pushScoped` (`:70`, argv `:103`); §12.4a in `pushEntries`' dry-run branch (`:133-136`) | as listed | yes |
| `internal/cli/doctor.go` | reparent line + guidance suppression | `:12`, `:94`, `:139` | yes |
| `internal/cli/list.go` | reparent line | `:12`, `:117` | yes |
| `internal/cli/open.go` | §14.2a launch exclusion | `:14`, `:248` | yes |
| `internal/health.go` | consume the ancestry guidance seam | `:53` | yes |
| `internal/checkout_health.go` | suppress `:412` / `:420` / `:453` guidance under an artifact | `:402` | yes |
| `internal/stack_ancestry.go` | guidance **seam** only | `:625` | yes |
| `internal/stack_status.go` | `reparent` omitempty pointer + guidance suppression | `:86`, `:614` | yes |
| `internal/agent_status.go` | `reparent` omitempty pointer; suppress `IssueSyncStateInvalid` hints | `:113`, `:803`, `:1354`, `:1363`, `:1433`, `:1523`, `:1525`, `:1559` | yes |
| `internal/session.go` | §14.2a checkout session launch exclusion | `:279` | yes |
| **`internal/cli/sync_plan_guard.go`** | hoist the **six** shared sentence literals to package constants and reference them | `:95`, `:105`, `:120`, `:127`, `:138`, `:142` | **no — additive (§11 P-12)** |
| **`internal/cli/sync_modes.go`** | hoist **three** more shared sentence literals; `errSyncContinueAbort` (`:145`) is *called*, not copied | `:67`, `:81`, `:82` | **no — additive (§11 P-12)** |
| **`internal/cli/importcmd.go`** | `.reparent` prefix arm in `isRuntimeState` **and** the contract comment | comment `:173-176`, body `:178-184` | **no — additive (§11 P-8/P-13)** |

The three files marked **no** are additive consequences this exploration declares. The spec does not
require them by name, and nothing here should be read as claiming it does: §3.5's byte-identity
obligation and §9.2's "every runtime-artifact filter" obligation are what make them necessary.

### 6.3 CHANGED — documentation (8 surfaces, all verified present)

`README.md`, `docs/cheatsheet.md`, `CHANGELOG.md`,
`assets/skills/claude/tesseraworkspaces/SKILL.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
`assets/skills/copilot/tws.prompt.md`, `docs/roadmap.md`, `docs/engineering-workflow.md`.

`docs/roadmap.md`'s `Current target:` sentence moves into the shipped-foundations list and names the
**next** target; `docs/engineering-workflow.md` appends slice 13 and updates
`Next roadmap feature:`. `internal/cli/sync_plan_docs_test.go:154-155` and `:179-180` assert the
current wording and MUST be retargeted deliberately (AC-100).
`docs/retrospectives/v1.2.7-upgrade-operations.md` is historical and MUST NOT be edited.

### 6.4 CHANGED — test infrastructure

`internal/cli/sync_downgrade_test.go` — parameterize `acquireDowngradeBinary` (`:47`) by tag and add
`v1.2.16` **beside** the existing `downgradeTag` (`:22`, `"v1.2.15"`), keeping both reachable
(§17.4a, §7.3).

### 6.5 EXPLICITLY UNTOUCHED (asserted, §14.1)

`TopoSort` (`internal/stack.go:138-182`), `Descendants` (`:185`), `DescendantsList` (`:282`),
`SaveStack` (`:128`) and every caller; `atomicWriteFile` (`internal/checkout_sync.go:151`) and every
caller; `SaveSyncRunState` (`internal/sync_run_state.go:154`), `LoadSyncRunState` (`:131`),
`SyncRunStateVersion` (`:21`), `SyncRunStateGuardedVersion` (`:25`), `CheckoutTransactionVersion`
(`:28`), `CheckoutTransactionGuardedVersion` (`:33`), `SyncRunStage` (`:36`);
`SaveGuardedLegacySentinel` (`internal/syncstate.go:243`) and `GuardedLegacySentinelVersion`
(`:182`); `CheckoutTransaction` (`internal/checkout_sync.go:64`), `ContinueCheckoutSync` (`:1065`),
`AbortCheckoutSync` (`:1200`), `restoreOriginal` (`:1661`), `CheckoutTransactionPath` (`:118`),
`CheckoutLockPath` (`:122`), `HasCheckoutTransaction` (`:183`), `AcquireCheckoutLock` (`:287`);
`forceAcquireCheckoutLock` (`:364-389`) **callers and error strings** (its `:383-385` return type
may gain a typed sentinel with a byte-identical message, §4.10); `fetchCheckoutRepo` (`:809`) and
its `:922` call site; the direct `fetchCheckoutRepoTo` call at `:912` and
`fetchCheckoutRepoTo` itself (`:777`) are delegated to, never edited;
`GitCapabilities` (`internal/git_capability.go:95`); `RebasePlan` (`internal/rebase_plan.go:29`) and
its 25 keys, `RebasePlanSchemaVersion` (`:13`), `RefusalKind` (`:975`), `RefusalKinds` (`:1012`, 30
members), the eight-member `PlanWarning` domain, `PlanFingerprint`
(`internal/rebase_plan_fingerprint.go:298`), `RevalidationDigest` (`:520`),
`RevalidatePlanGuardEntry` (`internal/rebase_plan_guard.go:602`), `EvaluatePlanGuard` (`:467`), the
four sync limit-algebra functions (`internal/rebase_plan_build.go:2594`, `:2618`, `:2657`, `:2750`),
`mergeBaseIsAncestor` (`:267`) and `asExitError` (`:282`); `runGit`/`runGitRaw`/`runGitStreamLines`
(`internal/rebase_plan_probe.go:32`/`:50`/`:77`); `checkoutGitOutput`
(`internal/checkout_sync.go:530`); `BuildWorktreeInventory` (`internal/agent_status.go:527`) and
`parseWorktreeInventory` (`:538`); every `tws sync` flag (`internal/cli/sync.go:151-166`) and
`sync_help.txt`; all 126 `sync_noflag` fixtures; `pushFeatureCheckout` (`internal/cli/push.go:157`).

---

## 7. Test ledger

### 7.1 Fixtures — package ownership is a compile constraint, not a preference

Go test files are compiled into the package they declare. `internal/*_test.go` is package `internal`
(or `internal_test`) and **cannot reference an identifier declared in `internal/cli/*_test.go`**, in
either direction. Any plan in which an `internal` test calls a builder defined in
`internal/cli/reparent_fixtures_test.go` does not compile. The eight §17.1 shapes are therefore
**conceptual shapes with two package-local realizations**, not one shared helper.

| Fixture shape (§17.1) | `cli` realization | `internal` realization |
|---|---|---|
| `reparentCustomerExternal` | extend `setupCustomerTopologyExternal` (`internal/cli/sync_plan_integration_test.go:46`) with a real `pr1` entry and a re-pointed `pr2` | not needed; `internal` cells use the minimal primitive builder below |
| `reparentCustomerCheckout` | extend `setupCustomerTopologyCheckout` (`:108`) | same |
| `reparentIssue4External` | **new**: `master -> pr1 -> {pr2, pr3 -> pr5 -> pr6, pr4}` | same |
| `reparentIssue4Checkout` | **new**, checkout twin | same |
| `reparentNoCutoff` | variant: `LastBaseSHA` cleared on `pr2` | same |
| `reparentStaleDescendant` | variant: `pr2` cutoff older than `pr1`'s tip | same |
| `reparentReftable` | variant: `git init --ref-format=reftable`; `t.Skip` when unsupported | same |
| `reparentDecoupled` | variant: `Name != Branch`, names containing `/` | same |

- **`internal/cli/reparent_fixtures_test.go` (package `cli`) — NEW.** Owns all eight shapes as
  CLI-level fixtures by extending the two shipped helpers (which already `t.Setenv("GIT_CONFIG_COUNT", "0")`
  at `:48` and `:110`). Every **end-to-end** cell and every real-Git **CLI / customer-topology /
  issue-#4 / remote / recovery** cell lives in a `internal/cli/reparent_*_test.go` file and uses
  these builders.
- **`internal/reparent_fixtures_test.go` (package `internal`) — NEW, minimal.** Owns pure helpers
  plus one small package-local repository primitive (`newReparentPrimitiveRepo(t)`: `git init`,
  a handful of commits, `GIT_CONFIG_COUNT=0`/`GIT_CONFIG_NOSYSTEM=1`) for **focused, package-internal
  Git primitives only** — ref enumeration, OID disambiguation, CAS line shape, pin ids, ancestor
  trichotomy. It deliberately does **not** reproduce the eight topologies; topology-dependent
  behaviour is a `cli` concern.

### 7.2 AC/T group → owning test file (every T has exactly one owner)

| T cells | Owner | Package |
|---|---|---|
| **T-001** customer topology, external, plan → execute | `internal/cli/reparent_e2e_test.go` **NEW** | `cli` |
| **T-002** customer topology, checkout, plan → execute | `internal/cli/reparent_e2e_test.go` **NEW** | `cli` |
| **T-003** issue #4 branching closure, both modes, full success | `internal/cli/reparent_e2e_test.go` **NEW** | `cli` |
| T-004…T-008 cutoff ladders and ambiguity | `internal/reparent_destination_test.go` **NEW** | `internal` |
| **T-009** `merge-base` **source audit** (scoped to §15 files + emitted argv) | `internal/cli/reparent_source_audit_test.go` **NEW** — **sole owner** | `cli` |
| T-010…T-017 destination kinds, ambiguity, OID width, pseudo refs, `--onto-kind`, stored token, resolver agreement | `internal/reparent_destination_test.go` **NEW** | `internal` |
| T-018…T-021 root/cycle/default-branch/ancestor-warning | `internal/reparent_plan_build_test.go` **NEW** | `internal` |
| T-022…T-026 closure, archived, duplicates, order determinism, ref-id collisions | `internal/reparent_plan_build_test.go` **NEW**; `ReparentClosureOrder` determinism also extends `internal/stack_test.go` (**existing**) | `internal` |
| T-027…T-030 dirty/git-op/holder/session refusals, no-work, same-parent-stale | `internal/cli/reparent_exclusion_test.go` **NEW** | `cli` |
| T-031, T-032 plan schema, streams, deferred nulls | `internal/reparent_plan_test.go` + `internal/reparent_plan_render_test.go` **NEW** | `internal` |
| T-033 sync domains unchanged | `internal/rebase_plan_test.go`, `internal/rebase_plan_fingerprint_test.go` (**existing**, extended) | `internal` |
| T-034, T-035 fingerprint sensitivity/stability, cross-domain rejection | `internal/reparent_plan_fingerprint_test.go` **NEW** | `internal` |
| T-036 both admission predicates | `internal/reparent_plan_test.go` **NEW** | `internal` |
| T-037 plan side-effect audit, fetch placement, no fetch on continue/abort | `internal/cli/reparent_exec_integration_test.go` **NEW** | `cli` |
| T-038 §3.5 order, arity table, usage-block behaviour | `internal/cli/stack_reparent_cli_test.go` **NEW** | `cli` |
| T-039, T-040, T-042, T-043 argv template/materialization, hostile `rebase.*`, scratch lifecycle + `.reparent*` filtering, JIT destination | `internal/cli/reparent_exec_integration_test.go` **NEW** | `cli` |
| **T-041** forbidden-verb **source and argv audit** | `internal/cli/reparent_source_audit_test.go` **NEW** — **sole owner** | `cli` |
| T-044…T-051 CAS shape/race/hook, pins, merge commits, validation, untracked | `internal/cli/reparent_transaction_test.go` **NEW**; the pure CAS **line-shape** builder test also lives in `internal/reparent_refs_test.go` **NEW** (no Git) | `cli` / `internal` |
| T-052…T-056 conflicts, capability floor, post-lock race, launch exclusion | `internal/cli/reparent_transaction_test.go` **NEW** | `cli` |
| T-057…T-060 metadata exactness, post-image ordering, drift, durable writer | `internal/reparent_metadata_test.go` **NEW** | `internal` |
| T-061…T-066 17 crash windows, partial commit, abort ± commit point, flag rejection, cleanup | `internal/cli/reparent_recovery_test.go` **NEW** | `cli` |
| T-067…T-069, T-071 mutual exclusion (incl. `--plan`), compat artifacts, reclaim lock, compat write order | `internal/cli/reparent_exclusion_test.go` **NEW**; the `ReclaimCheckoutLock` unit half extends `internal/checkout_sync_plan_test.go` (**existing**) | `cli` / `internal` |
| T-070 downgrade | `internal/cli/sync_downgrade_test.go` (**existing**, extended) | `cli` |
| T-072, T-073 observability with/without state | `internal/cli/reparent_observability_test.go` **NEW**; byte-identity halves extend `internal/cli/stack_status_test.go`, `internal/cli/status_test.go`, `internal/cli/doctor_ancestry_test.go`, `internal/agent_status_test.go`, `internal/checkout_health_test.go` (**existing**) | `cli` / `internal` |
| T-074…T-080 provider-call audit, guidance, record write point, three push paths, lease preflight, dry-run, clearing | `internal/cli/reparent_remote_test.go` **NEW**; the no-record byte-identity half extends `internal/cli/push_layout_test.go` and `internal/cli/sync_golden_test.go` (**existing**) | `cli` |
| T-081…T-084 refusal domain + limit-algebra parity, waived kinds, deferred holders, prefixes, validation order | `internal/reparent_plan_test.go` **NEW** | `internal` |
| T-085 help/golden snapshots, feature named `reparent` | `internal/cli/stack_reparent_cli_test.go` **NEW** | `cli` |
| T-086 documentation and skills | `internal/cli/sync_plan_docs_test.go` (**existing**, retargeted) | `cli` |
| T-087 sole-`Base`-rewriter source assertion | `internal/cli/reparent_source_audit_test.go` **NEW** | `cli` |
| AC↔T bidirectional mapping + real-Git leaf counter | `internal/cli/reparent_matrix_test.go` **NEW** | `cli` |

**T-009 and T-041 are source-audit-owned only.** `internal/cli/reparent_source_audit_test.go` is
their sole owner. The argv-shape assertions in `reparent_exec_integration_test.go` and
`reparent_transaction_test.go` are *behaviour twins* and cite AC-052 / AC-055 / AC-031 directly;
they must **not** also cite T-009 or T-041, or the bidirectional mapping assertion will report two
owners for one cell.

The matrix file lives in package `cli` because the leaf counter must observe CLI-level real-Git
leaves, which are the majority.

In every row that also names an existing supporting test or a package-internal
pure half, the first named NEW test file is the sole owner of the `T-*` id.
Supporting tests cite the applicable `AC-*` directly and MUST NOT claim the
same matrix-cell id; this is how the "exactly one owner" assertion remains
mechanical.

### 7.3 Downgrade harness extension to v1.2.16 (§17.4a)

Shipped ladder in `internal/cli/sync_downgrade_test.go`: `acquireDowngradeBinary` (`:47`) tries
`TWS_DOWNGRADE_BINARY`, then verifies `refs/tags/<downgradeTag>` (`:65`), then adds a detached
worktree (`:72`), then builds offline (`:81-86`), then returns a `priorBinary` note (`:89`); it
**never skips entirely** — a frozen replay harness is the final fallback.

`downgradeTag` is `"v1.2.15"` at `:22`. §17.4a requires `v1.2.16` **beside** it, both reachable, so
`acquireDowngradeBinary` gains a tag parameter and a new `reparentDowngradeTag = "v1.2.16"` sits
next to the existing constant. Both tags exist locally (§0.1), so the offline build path is live for
each. The tag-interpolating comments and log lines at `:18`, `:66-67`, `:73`, `:86`, `:89` move with
the parameterization.

T-070 then exercises, **against a real prior binary**, plain / `--continue` / `--abort` in **both**
modes against a real reparent fixture, and asserts the checkout arm specifically: the prior
`AbortCheckoutSync` fails at `LoadCheckoutTransaction` on the non-integer `state_version`, leaving
the lock untaken, no `git rebase --abort` run, `restoreOriginal` uncalled, and every artifact still
on disk. The external arm asserts the **complete wrapper sentence** (or its stable
`unsupported scoped sync state version 4` substring), not the bare nested loader error. Asserting
the shipped loader functions directly is a necessary supplement and an insufficient substitute: the
defect guarded against is a prior binary's control flow reaching a mutation before its version
check, which only an executed prior binary demonstrates. Keep the existing fidelity comparison.

### 7.4 Budget, hygiene and frozen assertions

- **120 real-Git leaves.** A real-Git cell is one `t.Run` leaf that spawns at least one `git`
  process; table sub-cases each count as one leaf. The count is **measured by a helper the suite
  itself asserts**, not estimated. The counter lives in `internal/cli/reparent_matrix_test.go`
  beside the AC↔T mapping assertion, so one file owns both self-checks.
- **Pure-first.** Ladder selection, refusal ranking, key order, closure order, the 33-field tuple,
  `ReparentRevalidationDigest`, the four limit-algebra adapters, both admission predicates, the argv
  template and its materialization, `ReparentEntryRefID`, `stackReparentValidate`,
  `stackReparentArgs`, the CAS **line-shape** builder, and the §6.1 step 2 duplicate checks are
  tested as **pure functions**, never through a real-Git run. This is what makes the leaf budget
  reachable.
- **`GIT_CONFIG_COUNT=0`.** Established hygiene: `t.Setenv("GIT_CONFIG_COUNT", "0")` at
  `internal/cli/sync_plan_integration_test.go:48`, `:110`, `:601`, `:1798`, `:1954`, `:1975`,
  `:2104`, `:3297`, `:4696`; `internal/cli/push_layout_test.go:82`;
  `internal/stack_status_test.go:27`. Subprocess forms append
  `"GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1"` to `cmd.Env`
  (`internal/stack_status_test.go:2251`, `internal/cli/sync_modes_test.go:269`/`:274`,
  `internal/cli/sync_plan_integration_test.go:1266`). The git shim at
  `internal/cli/sync_plan_integration_test.go:3734` scrubs
  `GIT_DIR GIT_WORK_TREE GIT_CONFIG_COUNT GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT GIT_OPTIONAL_LOCKS`
  and `:3910` asserts the scrub. **Every new real-Git reparent test follows the same pattern**,
  because T-040 deliberately sets hostile `rebase.*` config and must not leak it.
- **Reftable skips.** Every reftable cell `t.Skip`s with a clear reason when
  `ReparentGitCapabilitiesForVersion(...).CapRefBackendKnown` is false or
  `git init --ref-format=reftable` fails. The **files-backend twin of every such cell is mandatory
  and never skipped** — CI pins no Git version, so a reftable-only assertion would silently vanish
  on the older matrix leg. T-045 (files CAS race) is mandatory; T-046 (reftable) is skippable.
- **`argv.log`.** Every real-Git test records and asserts against an argv log, so forbidden-verb
  audits are cheap. Precedents: `internal/cli/sync_golden_test.go` and
  `internal/checkout_sync_plan_test.go` already carry argv-log harnesses.
- **Status-surface fixtures must cover BOTH checkout layouts (T-072/T-073).** Checkout mode has two
  distinct state directories, so an observability fixture that builds only one proves nothing:
  1. the **authoritative** artifact at `ws.CheckoutStateDir()/<feature>-reparent.v1.yaml`
     (`internal/resolve.go:285`);
  2. the **compatibility** transaction at `CheckoutTransactionPath(featurePath)`
     (`internal/checkout_sync.go:118`, derived by `checkoutStateDir(featurePath)` `:112`) and the
     lock at `CheckoutLockPath(featurePath)` (`:122`).
  A legacy `.tws/<feature>` layout makes those two directories differ (`internal/agent_status.go:1355-1357`
  records why), so both a new-layout fixture and a legacy-layout fixture are required. T-072 then
  asserts that **no** surface emits `IssueSyncStateInvalid`, `state file unreadable`,
  `corrupt transaction state`, `corrupt lock file`, `remove it manually` or `manually remove` about
  any of those three files, while the reparent-in-progress line is present exactly once on stderr.
- **Frozen goldens and docs**, re-asserted after every step: the 126
  `internal/cli/testdata/sync_noflag/**` files byte-identical;
  `internal/cli/testdata/rebase_plan/sync_help.txt` byte-identical;
  `testdata/sync_noflag/declared_c2/push` and `declared_c2/sync-push` byte-identical;
  `tws stack status --help` byte-identical; the no-reparent `tws stack status --json` and agent
  status JSON byte-identical with the `reparent` key **absent** and `schema_version` still `1`.
- **Runtime as evidence.** The landing record carries a measured `go test ./... -count=1` wall clock
  immediately before and immediately after the change on the same machine, and states the delta. No
  unmeasurable "+N seconds on CI" claim is acceptable; CI runs a two-OS matrix with `-timeout 40m`
  and no recorded baseline exists.

---

## 8. Dependency-ordered implementation sequence

**Every step must compile and must leave `go build ./...` green.** Steps 1–7 add no behaviour to any
shipped command; steps 3 and 14 are the two that touch shipped files most invasively and both gate
on the 126 goldens.

| # | Step | Files | Checkpoint |
|---:|---|---|---|
| 1 | **Pure foundations.** `ReparentClosureOrder` after `TopoSort`'s close; `durableWriteFile`, `WriteStackBytesAtomic`, `SaveStackAtomic`; four appended `SyncIO*` tokens | `internal/stack.go`, `internal/syncstate.go` | `go build ./... && go test ./internal/ -run 'TestTopoSort|TestStack|TestSyncState' -count=1` |
| 2 | **Capabilities.** `ReparentGitCapabilities`, `ReparentGitCapabilitiesForVersion` beside the frozen six-gate table | `internal/git_capability.go` | `go test ./internal/ -run TestGitCapabilit -count=1` |
| 3 | **The runner, first.** `runReparentGit` + `reparentGitResult` in a new `internal/reparent_exec.go` containing only the runner. Nothing else in the reparent-owned planning/execution files may spawn a process, so this must exist before any probe-bearing file | `internal/reparent_exec.go` **NEW** | `go build ./... && go test ./internal/ -run TestRunReparentGit -count=1` (a focused leaf: `git --version` + one `rev-parse` in a primitive repo) |
| 4 | **Pure extractions, zero behaviour change.** `checkoutBaseTokenFor` out of `buildCheckoutPlanFrom:590-596`; the typed live-lock error at `:383-385` plus `ReclaimCheckoutLock`; hoist the six + three sentence constants | `internal/checkout_sync.go`, `internal/cli/sync_plan_guard.go`, `internal/cli/sync_modes.go` | `go test ./internal/... -count=1` — **the 126 goldens are the gate**, because this step touches shipped files |
| 5 | **Schema.** `ReparentPlan` and every nested type, the 49-member refusal domain, the 11-member warning domain, `SelectPrimaryReparentRefusal`, `EvaluateReparentGuard`, `ReparentWriters` | `internal/reparent_plan.go` **NEW** | `go vet ./internal/` + the key-count/order test |
| 6 | **Render + fingerprint + digest.** `FormatReparentPlan`, `MarshalReparentPlan`; the 33-field tuple over the reused `tlvEncoder`; `ReparentRevalidationDigest` | `internal/reparent_plan_render.go`, `internal/reparent_plan_fingerprint.go` **NEW** | fingerprint + digest stability/sensitivity tables (pure, zero Git) |
| 7 | **Destination + cutoff.** `ResolveReparentDestination`, the five-candidate enumeration, `--disambiguate`, `--show-object-format`, `reparentIsAncestor`, the four-resolver agreement adapter — all on top of step 3's runner | `internal/reparent_destination.go` **NEW** | T-010…T-017 focused leaves on the `internal` primitive repo |
| 8 | **State.** `ReparentStateVersion`, the 19 stages, the §11.5 payload, both paths, `RefuseIfReparentActive`, `ReparentProjection`, the compatibility writers | `internal/reparent_state.go` **NEW** | load/save round-trip + T-071 compat write order |
| 9 | **Refs.** pin namespace, `ReparentEntryRefID`, `sanitizeReparentRefPart`, the CAS builder, the §11.7 classifier | `internal/reparent_refs.go` **NEW** | T-026 + the pure CAS line-shape test; T-044's real-Git half follows in step 12 |
| 10 | **Remote record core.** the record type, durable IO, §12.2 guidance, §12.5 clearing, the `reparentPushDecision` helpers. **Before the executor**, because stage `writing-remote-record` is a hard dependency of the CAS (§4.7) | `internal/reparent_remote.go` **NEW** | record round-trip + clearing-observation table (pure + focused) |
| 11 | **Plan builder.** `ReparentRequest` (with `Fetch PlanFetchOutcome`), `BuildReparentPlan`, closure + scope gates, the pure fetch projections, the four limit-algebra adapters, `RevalidateReparentRow`, `PlanReparent` | `internal/reparent_plan_build.go` **NEW** | T-031, T-032, T-036, T-081 (pure); T-018…T-026 |
| 12 | **Executor.** add `fetchReparentCheckoutRepo`, then `BeginReparentRun` (§4.0), `RunReparent`, scratch/checkout context, per-row rebase, pins-before-validate, validation, conflicts, holders, the CAS drive, post-image, metadata write | `internal/reparent_exec.go` (grown) | T-044…T-052 |
| 13 | **CLI.** `stackReparentCmd`, `stackReparentArgs`, `stackReparentValidate`, completion, streams, the external fetch via `fetchQuietTo`, all four routes; register in `stack.go`; suppress the completion candidate | `internal/cli/stack_reparent.go` **NEW**, `internal/cli/stack.go` | T-038, T-085 + the three new goldens; **T-001/T-002 end-to-end become runnable here** |
| 14 | **Recovery.** `ContinueReparent`, `AbortReparent`, the 17 crash windows, cleanup order, `compat-artifact-missing`, §11.2a | `internal/reparent_exec.go`, `internal/reparent_state.go` | T-061…T-066, T-068, T-069, T-003 |
| 15 | **Sync exclusion.** the §2.5 precheck block | `internal/cli/sync.go` | T-067 (incl. `--plan`) **and** a full `TestSyncNoFlag_` re-run — the highest-risk shipped-file edit |
| 16 | **Push integrations.** rule R-PUSH inside `gitPush` (both call sites), `pushEntries`, `pushScoped`; §12.4a dry-run | `internal/checkout_sync.go`, `internal/cli/push.go` | T-074…T-080 + `declared_c2/{push,sync-push}` byte-identity |
| 17 | **Observability + launch exclusion.** the §11.10 line, guidance suppression, the two omitempty pointers, `.reparent*` filters (both halves), `tws open` / session refusal | `internal/cli/{doctor,list,open,importcmd}.go`, `internal/{health,checkout_health,stack_ancestry,stack_status,agent_status,session}.go` | T-072, T-073, T-056, T-042 |
| 18 | **Downgrade.** parameterize the harness; add `v1.2.16` | `internal/cli/sync_downgrade_test.go` | T-070, both modes, three verbs |
| 19 | **Docs + skills.** the eight surfaces; retarget the roadmap assertions | docs, skills, `internal/cli/sync_plan_docs_test.go` | T-086 |
| 20 | **Full gate.** | — | the block below |

Two ordering facts are load-bearing and were wrong in the previous revision:

- **The runner precedes destination/cutoff** (step 3 before step 7). `ResolveReparentDestination`
  cannot be written without a process spawner, and writing it against `runGitRaw` first and
  retrofitting `runReparentGit` later would put a `-C` argv in the boundary and break the
  emitted-argv audit.
- **The remote record precedes the executor** (step 10 before step 12), because
  `writing-remote-record` sits between `pinning-computed` and `detaching-holders` and the executor
  cannot reach the CAS without it.

```bash
# focused, while implementing
go test ./internal/ -run 'Reparent' -count=1
go test ./internal/cli/ -run 'Reparent|TestSyncNoFlag_' -count=1

# full gate, before landing
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Plus a manual CLI smoke in **both** workspace modes: plan → approve → execute →
`tws stack status` → simulated crash → `--continue` → `--abort` on a fresh run. Do not push or tag
until the landed commit is clean and approved.

---

## 9. Process-budget probe ledger

### 9.1 Once per invocation — cached, never re-issued

| Probe | Consumers |
|---|---|
| `git --version` → `ProbeGitVersion` (`internal/git_capability.go:45`) | `GitCapabilitiesForVersion` (`:150`), `ReparentGitCapabilitiesForVersion` (**NEW**) |
| `git rev-parse --show-object-format` | `policy.oid_width` and every OID-width branch (§4.3a step 1); frozen in state |
| `git rev-parse --show-ref-format` (else `git config --get extensions.refStorage`, else `unknown`) | `atomicity.ref_backend`, `partial_commit_recovery`, warning `files-backend-not-crash-atomic` |
| `git rev-parse --git-common-dir` per closure row | `cross-repo-closure` (§6.1 step 5) |
| `git worktree list --porcelain` via `BuildWorktreeInventory` (`internal/agent_status.go:527`) | holders, `holder-unsafe`, scratch exclusion |
| `git config --list` (scope-aware) | `collateral-update-refs-config`, hostile `rebase.*` detection |
| the one policy-declared `fetch` | §6.3 step 2a — plan route and fresh execution only, measured in the mode-appropriate package (§3.8) |

### 9.2 Re-measured at every JIT seam — never cached across seams

| Probe | Seam |
|---|---|
| per-row replay candidate count + `ReparentRevalidationDigest` | §8.3, before computing each row |
| `git ls-files --others --exclude-standard` ∩ `git ls-tree -r --name-only` | §9.4b, before **every** switch and every holder re-attachment |
| `git rev-parse --verify --quiet --end-of-options <oid>^{commit}` | §9.4a, per `parent-computed` destination |
| live ref values for every closure row | §11.7 classification, at every recovery entry and before every abort ref write |
| live `stack.yaml` SHA-256 | before **every** forward metadata write (§9.12 step 2, §10.5) |
| session liveness | §8.5 step 3, post-lock |

### 9.3 Ceilings

- The §8.5 post-lock re-snapshot (§4.0 step 4) re-issues its own list **once**; it is not a loop.
- `reparentIsAncestor` is issued at most once per row for the cutoff (§5.4) and once for the §7.11
  `destination-ancestor-of-old-parent` warning. It never derives a cutoff (AC-031).
- Stage `pinning-computed` is a verify-all gate: one `rev-parse` per row's new pin, not a
  re-computation.
- `RefuseIfReparentActive` on the `tws sync` path is `Stat` + at most one `ReadFile`: **zero** Git
  processes, which is why the no-flag argv sidecars stay byte-identical.

### 9.4 Explicit zero-budget cells

`ReparentClosureOrder`, `SelectPrimaryReparentRefusal`, `EvaluateReparentGuard`, the four
limit-algebra adapters, the 33-field tuple, `ReparentRevalidationDigest`, both admission predicates,
the argv template and its materialization, `ReparentEntryRefID`, the CAS **line-shape** builder,
`stackReparentValidate`, `stackReparentArgs`, the §6.1 step 2 duplicate checks, and the whole
key-order/count surface spawn **zero** processes and are tested as pure functions.

---

## 10. Risk notes (implementation traps; no open decisions)

1. **The `tws sync` precheck is the single highest-risk edit.** `internal/cli/sync.go:108`, `:112`,
   `:113` are hoisted, not duplicated; `:116` keeps its `twsRoot` argument. Any change to the order
   `RequireTool` → `resolvePlanGuardOptions` → `RequireWorkspace` → `resolveSyncPolicy` moves a
   refusal in the state matrix. Re-run `TestSyncNoFlag_` and the cell matrix after every touch.
2. **`resolveExternalSyncLayout` is called twice on the external sync path after the edit.**
   Deliberate and cheap (at most two `os.ReadFile`s, zero Git), and it is what preserves the frozen
   resolution sequence at `:116`.
3. **`ws.ResolveFeaturePath` is filesystem-only, not pure.** It `Stat`s candidate layouts and can
   report ambiguity. What matters for the goldens is that it spawns no Git child; do not describe it
   as side-effect free.
4. **Checkout mode has two state directories.** `checkoutStateDir(featurePath)`
   (`internal/checkout_sync.go:112`) is correct for `CheckoutTransactionPath`/`CheckoutLockPath`;
   `ws.CheckoutStateDir()` (`internal/resolve.go:285`) is the one the reparent artifacts use.
   `agent_status.go:1355-1356` records why. Getting it backwards strands artifacts in legacy layouts.
5. **`internal` cannot call `cli`.** Any fetch, render or refusal design that has an `internal`
   function reaching for `fetchQuietTo` does not compile. Measure in the right package and pass the
   measurement in (§3.8).
6. **A Go test file cannot cross the package boundary either.** An `internal` test cannot call a
   `cli` fixture builder (§7.1).
7. **`SaveStack` is not atomic and never was.** The reparent boundary uses
   `WriteStackBytesAtomic`/`SaveStackAtomic` exclusively, always with captured bytes on both the
   forward write and the abort restore.
8. **`atomicWriteFile` does not fsync the parent directory.** `durableWriteFile` is not optional.
9. **Checkout mode's own holder must be excluded from the §9.9 drift check**, or every
   checkout-mode run refuses `holder-unsafe` against the HEAD it deliberately detached.
10. **A descendant's destination must never be read from `refs/heads/<parent>`** — public refs hold
    pre-image values until the CAS.
11. **Pin before validate.** Validation can run for minutes and can invoke `git gc`.
12. **A no-op row must emit `verify`, not `update X X`** — in the forward CAS and in the abort CAS.
13. **An integer `state_version: 4` in the checkout compatibility transaction is unsafe**;
    `AbortCheckoutSync` (`internal/checkout_sync.go:1200`) performs no version check at all.
14. **External compatibility write order is load-bearing**: v2 payload first, legacy sentinel second.
15. **The authoritative artifact is written before the compatibility artifacts** (§11.4 steps 6→7).
    Presenting it the other way makes crash window 1 unrecoverable in the narrative and, if
    implemented that way, in fact.
16. **`--continue`/`--abort` must not mention the token or either limit.**
17. **Deferred nulls are not "unavailable".** `plannability` stays `rows`, `runnable` stays true.
18. **One refusal line per process.** `reparent-recovery:` and `reparent-remote:` are progress.
19. **`git -C` is forbidden in emitted argv.** This is why `mergeBaseIsAncestor` is *not* reused
    (§1.3) — a single borrowed helper would have failed the audit it was meant to satisfy.
20. **The collision escape and the completion suppression are different mechanisms**: suppression is
    an explicit filter (`stack.go:24`); the escape is Cobra-native `--` handling with no code at all.
21. **`--continue=false` must remain a fresh route**, which is why `stackReparentArgs` reads
    `syncBoolFlag`'s value rather than `Flags().Changed`.

---

## 11. Spec-to-code precision items (declared, each with a resolved consequence)

Nothing below amends the spec. Each item is a mismatch between spec prose, an earlier revision of
this document, or a task framing, and the tree at `d39e934`, recorded with the ruling to follow.

**P-1 (naming, no defect).** §15's "Touched (minimally)" list names symbols that do not exist today
and are **new declarations inside existing files**: `ReparentClosureOrder`, `WriteStackBytesAtomic`,
`SaveStackAtomic`, `durableWriteFile`, `ReparentGitCapabilities`,
`ReparentGitCapabilitiesForVersion`, `ReclaimCheckoutLock`, the four `SyncIO*` tokens,
`checkoutBaseTokenFor`, `RefuseIfReparentActive`, `ReparentProjection`, `reparentPushDecision`, and
both `reparent` projection fields. §6.2 marks each. **Ruling:** §15 is a file-ownership table, not
an existence claim.

**P-2 (resolved precision correction).** §3.2's diagnostic is quoted as
`accepts 2 arg(s), received 0: …`, while the shipped sibling `stackStatusArgs`
(`internal/cli/stack_status.go:105`) hardcodes `accepts 1 arg(s), received 0` at `:122` and reaches
the collision loop (`:120-123`) **only** when `len(args) == 0`. Reparent's arity is route-dependent.
**Ruling:** build the sentence with `%d`/`%d`; emit it **only** at `len(args) == 0`; let every other
mis-arity fall through to `cobra.ExactArgs(want)`. Detect the route from `syncBoolFlag`'s **value**,
not `Flags().Changed`, so `--continue=false` stays a fresh route. Cobra parses flags before
`ValidateArgs`, which is what makes the per-route table implementable in the `Args` phase. Copy
`stackStatusArgs:112-118`'s `ListFeaturesE` error-fallback discipline; do not generalize the shipped
function (§14.2 item 1 freezes `tws stack status`).

**P-3 (resolved, file ownership).** §8.1's `EvaluateReparentGuard` and §13.1's
`SelectPrimaryReparentRefusal` have no file in §15's ten-file ledger. **Ruling:** both live in
`internal/reparent_plan.go`, mirroring `EvaluatePlanGuard`'s placement beside `CheckoutPlanGuard`
(`internal/rebase_plan_guard.go:33`/`:467`). **Do not create `internal/reparent_plan_guard.go`.**
The ledger stays at ten.

**P-4 (resolved precision correction, argv boundary).** Measured: `runGit`
(`internal/rebase_plan_probe.go:32`) and `runGitRaw` (`:50`) use `git -C <dir>`;
`checkoutGitOutput` (`internal/checkout_sync.go:530`) uses `cmd.Dir`; `RunDirTo`
(`internal/exec.go:150`) uses `cmd.Dir` and caller writers but sets `cmd.Stdin = os.Stdin` (`:154`)
and returns only `error`; `mergeBaseIsAncestor` (`internal/rebase_plan_build.go:267`) uses
`git -C`. **Ruling:** `runReparentGit` is required (stdin from a buffer, returned captured bytes,
never `os.Stdin`), and **`mergeBaseIsAncestor` is NOT reused** — `reparentIsAncestor` reproduces its
exit-0/1/error trichotomy through `runReparentGit`. This removes the AC-031/AC-055 carve-out the
previous revision needed. Every shipped probe keeps every caller and is cited as a pattern only.

**P-5 (resolved precision correction, high impact).** §11.1 and §12.3 spell the checkout artifact
paths as `<metadataRoot>/state/…`, and two helpers produce such a directory:
`checkoutStateDir(featurePath)` (`internal/checkout_sync.go:112`, feature-path-derived, correct for
`CheckoutTransactionPath` `:118` and `CheckoutLockPath` `:122`) and `Workspace.CheckoutStateDir()`
(`internal/resolve.go:285`, `MetadataRoot`-derived). `internal/agent_status.go:1355-1357` records in
a shipped comment that `checkoutStateDir()` "is wrong for legacy `.tws/<feature>` layouts".
**Ruling:** reparent's own artifacts use `ws.CheckoutStateDir()`; the compatibility transaction and
lock keep `CheckoutTransactionPath`/`CheckoutLockPath` so shipped loaders find them.
`checkoutStateDir` is package-private and **directly reusable from any new file in package
`internal`** — it is reused where compatibility path agreement demands it, and deliberately not used
for the authoritative artifact. Do not export it and do not duplicate it. T-072/T-073 must build
both a new-layout and a legacy-layout fixture (§7.4).

**P-6 (declared, ordering).** §11.2 (as corrected) requires the sync precheck after
`GuardFeatureName` and a safe resolver but before **mode dispatch, plan dispatch and
classification**. The shipped ladder resolves the feature path only after the checkout dispatch
(`internal/cli/sync.go:94` vs `:116`) and dispatches `--plan` at `:132`. **Ruling:** hoist `feature`,
`twsRoot` and `GuardFeatureName` above `:93` and branch per mode (§2.5); keep `:116`'s call and
`runCheckoutSync`'s own `RequireFeaturePath` (`internal/cli/checkout_sync.go:18`) exactly where they
are. Threading the resolved values down is a strictly larger edit and is not chosen. §11.2's
"the detailed reparent artifact takes precedence over its own matching compatibility files" is
implemented as precedence in *classification order*, not as a second resolution.

**P-7 (resolved precision correction, already applied to `spec.md`).** §11.6b previously claimed
`forceAcquireCheckoutLock` "steals unconditionally". Measured (`internal/checkout_sync.go:364-389`),
it refuses a live foreign PID at `:383-385` with
`lock held by live process %d; cannot reclaim`, and compares bytes before removing (`:342`). The
working-tree spec now reads "its current PID/liveness ladder already refuses a live foreign
process … MUST reuse `forceAcquireCheckoutLock`'s existing `LockInfo`, PID/liveness,
compare-bytes-before-remove, and `O_EXCL` behavior rather than implement a second reclaim ladder".
**Ruling:** a *trivial* wrapper is nevertheless insufficient — reparent must map only the
live-foreign case to `sync-state-present`, and a caller cannot distinguish that error from
"invalid lock"/"unreadable lock"/"mkdir failed" by string matching. Implement design **(a)** of
§4.10: a typed `CheckoutLockLiveError` whose `Error()` is **byte-identical** to `:384`, returned at
`:383-385`, with `ReclaimCheckoutLock` as the exported wrapper; design **(b)** (shared body +
typed verdict) is the acceptable alternative. **No duplicated, race-prone pre-read ladder.** Fresh
executions use `AcquireCheckoutLock` (`:287`); recovery routes use `ReclaimCheckoutLock`. Nothing in
code, comments, docs or tests may claim tws currently steals live locks.

**P-8 (declared, additive).** `internal/cli/importcmd.go` is absent from §15's list, but §9.2's
"every runtime-artifact filter that enumerates a feature directory MUST skip any entry matching
`.reparent*`" and T-042 require it: `isRuntimeState` (`:177`) is the only such filter in the tree.
**Ruling:** it is an additive consequence of §9.2, declared here, not a spec change. Siblings that
are asserted and **not** edited: `internal/paths.go:130`, `ListBranches`' `worktrees` scope,
`internal/resolve.go:271`, `internal/spaces.go:668`.

**P-9 (resolved precision correction, already applied to `spec.md`).** §11.2 previously said
`SaveGuardedLegacySentinel` "MUST NOT be called after the v4 payload exists: its current safety gate
rejects that file order." Measured: `SaveGuardedLegacySentinel(featurePath string, s *GuardedLegacySentinel, expect []byte) error`
(`internal/syncstate.go:243`) consults `syncIOFault(SyncIOWriteSentinel, path)` at `:245` and
compare-and-swaps **its own sentinel bytes** against `expect`; it never reads or inspects
`.sync-state.v2.yaml`, and it rejects no file order. The corrected spec now states the two real
reasons the reparent-owned writer is required: (1) `SaveGuardedLegacySentinel` emits the guarded
**legacy-sync** shape, while this boundary needs **reparent identity** in the sentinel; and (2) it
writes through `atomicWriteFile`, which does not fsync the parent directory, while §10.3a requires
`durableWriteFile(..., 0644)`. **Ruling:** write the sentinel with the reparent-owned writer for
those two reasons, reproduce `GuardedLegacySentinelVersion = 3` (`internal/syncstate.go:182`,
enforced `:366`) exactly, and never justify the new writer by a nonexistent ordering gate.
`SaveGuardedLegacySentinel` stays untouched with every caller.

**P-10 (declared, low impact).** §15 names `internal/health.go` for "reparent-aware guidance
suppression (external doctor/list)", but the guidance strings originate in `ancestryGuidance`
(`internal/stack_ancestry.go:625`) and reach `health.go` through `AncestryHealthIssues` (`:53`).
**Ruling:** the seam is declared in `stack_ancestry.go` and *consumed* in `health.go`; both are
touched and neither changes ancestry logic.

**P-11 (declared, naming + ownership).** §11.10 and §15 attribute `buildFeatureSync` to
`internal/agent_status.go` and `buildOneSyncReport` to `internal/checkout_health.go`. Verified:
`buildFeatureSync` is a **method on `*statusBuilder`** at `internal/agent_status.go:1354` (called at
`:1268`), delegating the checkout arm to `buildOneSyncReport` (`internal/checkout_health.go:402`) at
`agent_status.go:1363`. **Ruling:** the §11.10 suppression is implemented in **both**;
`IssueSyncStateInvalid` (`internal/agent_status.go:113`) hints live at `:1433`, `:1523`/`:1525` and
`:1559`, and the `manually remove` hints live at `checkout_health.go:412`, `:420` and — for the
checkout lock this feature also holds — **`:453`**, which must be suppressed on the same condition.
Additionally, §11.10's new `reparent` key needs a declaring file: **`ReparentProjection` is owned by
`internal/reparent_state.go`**, which both `stack_status.go` and `agent_status.go` embed.

**P-12 (declared, additive + corrected §3.5 numbering).** The **six** literals §3.5 reuses that live
in `internal/cli/sync_plan_guard.go` are `:95` (rule 1), `:105` (rule 3), `:120` and `:127`
(**both rule 10**), `:138` (rule 11) and `:142` (rule 12). **Three more** live in
`internal/cli/sync_modes.go`: `:67` (rule 5) and `:81`/`:82` (rule 6). Rule 2's sentence is already a
function, `errSyncContinueAbort()` (`internal/cli/sync_modes.go:145-147`), and is **called**, not
hoisted. A second copy of rule 2's sentence exists in package `internal` at
`internal/checkout_sync.go:1209` and is out of boundary. **Ruling:** hoist the six plus the three;
call `errSyncContinueAbort()`; leave `:1209` alone. `internal/cli/sync_plan_guard.go` and
`internal/cli/sync_modes.go` are **not** named by §15 — they are additive consequences of §3.5's
byte-identity obligation, declared here, and nothing should imply the spec required them by name.
(The previous revision mis-numbered these as "rules 10/11 limits, 11 token shape"; the corrected
mapping is above.)

**P-13 (declared, contract change).** `isRuntimeState`'s doc comment
(`internal/cli/importcmd.go:173-176`) states: "The list is exact-name only: no prefix matching, so no
user file is filtered accidentally. Any future runtime-state file MUST be added here explicitly."
The body (`:178-184`) already carries one prefix (`.tws/state/`), and adding `.reparent` makes
prefix matching a second deliberate part of the contract. **Ruling:** update the comment in the same
edit to state that exactly two prefixes are matched (`.tws/state/` and `.reparent`) and that every
other entry remains exact-name. Directory-name filtering alone is **insufficient** for the scratch
worktree: `git worktree add` registers it in the common dir, so every
`git worktree list --porcelain`-derived consumer (`BuildWorktreeInventory`,
`internal/agent_status.go:527`) must also exclude the recorded `scratch_path` (§4.2 half B).

**P-14 (declared, baseline).** §17.4a says to add `v1.2.16` "beside the existing `v1.2.15`
constant, keeping both reachable". Verified: `downgradeTag` is `"v1.2.15"` at
`internal/cli/sync_downgrade_test.go:22`; both tags exist locally. **Ruling:** parameterize
`acquireDowngradeBinary` (`:47`) by tag rather than editing the constant in place, and move the
interpolating comments/log lines at `:18`, `:66-67`, `:73`, `:86`, `:89` with it. The harness never
skips entirely; keep the frozen replay fallback and the fidelity comparison.

**P-15 (declared, package boundary — corrects the previous revision).** `fetchQuietTo`
(`internal/cli/sync.go:1507`) is **package `cli`, unexported**, so no `internal` builder can call it;
the previous revision's "the external arm calls `fetchQuietTo`" inside `internal` would not compile.
`fetchCheckoutRepoTo` (`internal/checkout_sync.go:777`) and `fetchCheckoutRepo` (`:809`, call site
`:922`) **are** in package `internal` and are directly reusable. **Ruling:** §3.8's split — pure
construction in `internal`, external execution in `internal/cli/stack_reparent.go`, checkout
execution through an `internal` adapter delegating `fetchCheckoutRepoTo`, and the measured
`PlanFetchOutcome` passed into `ReparentRequest`. Never import `cli` from `internal`; never
synthesize a `RebasePlanRequest`.

**P-16 (declared, sync-typed reuse — corrects the previous revision).** `RevalidationDigest`
(`internal/rebase_plan_fingerprint.go:520`) takes a `PlanEntry`; `RevalidatePlanGuardEntry`
(`internal/rebase_plan_guard.go:602`) takes a `RebasePlanRequest` via
`RevalidatePlanEntry(req.Request, req.Approved)`; `EvaluatePlanGuard`
(`internal/rebase_plan_guard.go:467`) takes a `RebasePlan` and is **only** the final three-rung
evaluator. The real limit arithmetic is `guardEvaluationRows`
(`internal/rebase_plan_build.go:2594`), `perEntryEvaluationRow` (`:2618`), `totalEvaluationRow`
(`:2657`) and `limitExceededBlockers` (`:2750`), all `PlanEntry`/`PlanSummary`/`PlanBlocker`-typed;
`controlledPathBlockerKind`/`controlledPathBlockerDetail` (`internal/rebase_plan_guard.go:496`/`:513`)
are blocker-**token** mapping, not arithmetic. **Ruling:** add `ReparentRevalidationDigest`
(§3.6) and the four reparent-owned adapters plus `RevalidateReparentRow` (§3.7), reusing the TLV
framing and the `PlanGuardEvaluation` / `PlanGuardLimits` / `PlanGuardBlock` / `PlanGuardRefusalError`
types but **not** the sync-typed entry/request functions. A parity test (T-081) pins the two ladders
together.

**P-17 (declared, evidence correction).** The previous revision said `TopoSort`'s sibling order is
"FIFO over `s.Branches` iteration order". Measured (`internal/stack.go:138-182`): the ready queue is
seeded by ranging over the **Go map** `inDegree` at `:159-163`, so the initial root order is
**randomized per run**; only child expansion at `:170-175` follows `children[name]`, appended in
declaration order. **Ruling:** the conclusion is unchanged and strengthened — `TopoSort` is frozen,
is used by `internal/cli/stack.go:45` only as a warn-only cycle check, and cannot supply §6.1a's
stable order. `ReparentClosureOrder`'s declaration-index min-heap stands, and T-025 should assert
determinism across repeated runs precisely because the shipped function does not offer it.

**P-18 (declared, normative alignment).** §15 says "`internal/checkout_sync.go`  rule R-PUSH in
gitPush". The previous revision proposed a sibling `gitPushReparentAware`, which would have left
both shipped call sites (`internal/checkout_sync.go:1628` normal, `:1645` retry) on the old argv, so
the rule would never fire in checkout mode. **Ruling:** edit `gitPush` (`:540`) itself, giving it one
`reparentPushDecision` parameter whose zero value reproduces today's argv, output and exit status
byte-for-byte; both call sites pass a per-branch decision; the **invocation-wide** preflight runs
above each of the two loops, never per entry. `internal/checkout_sync.go` was already in §15's
touched list for exactly this. The previous revision's "gitPush untouched" claim is withdrawn.

---

## 12. Dependency verdict

**No new dependency edge was discovered.** The registered graph in `status.json` is sufficient and is
confirmed by measurement:

| Parent | Kind | What reparent actually consumes, verified |
|---|---|---|
| `rebase-plan-guard` | hard | `CheckoutPlanGuard` (`internal/rebase_plan_guard.go:33`), `PlanGuardBlock`, `PlanGuardLimits` (`:193`), `PlanGuardEvaluation`, `PlanGuardRefusalError` (`:108`), `controlledPathBlockerKind`/`Detail` (`:496`/`:513`), the TLV encoder (`internal/rebase_plan_fingerprint.go:60`), `planGuardRefusal` (`internal/cli/sync_plan_guard.go:200`), `renderPlanDocumentTo` (`:173`), `planLayout` (`:283`), `externalWorkspace` (`:290`), `PlanWriters` (`internal/rebase_plan_guard.go:134`), `PlanFetch*` carriers (`:66`/`:81`/`:91`), `ResolveFetchSuppression` (`:286`), `ensureSlice` (`internal/rebase_plan_render.go:440`), `fetchQuietTo` (`internal/cli/sync.go:1507`) and `fetchCheckoutRepoTo` (`internal/checkout_sync.go:777`) |
| `sync-modes` | hard | `SyncRunPolicy`/`SyncFetchPolicy` (`internal/sync_selection.go:42`/`:17`), `resolveSyncPolicy`'s mode-derived fetch default (`internal/cli/sync_modes.go:49`), `externalSyncLayout` + `resolveExternalSyncLayout` (`:199`/`:215`), `errSyncContinueAbort` (`:145`), `syncBoolFlag` (`:86`), the recovery-state conventions of `internal/sync_run_state.go` |
| `stack-status` | hard | `BuildStackStatus` (`internal/stack_status.go:614`), `StackStatusReport` (`:86`), `stackStatusCmd`'s arity/collision/stream discipline (`internal/cli/stack_status.go:55`, `:105`, `:122`) |
| `amend-aware-rebase` | hard | the cutoff algebra and `LastBaseSHA` (`internal/stack.go:19`) semantics §5 builds on |
| `stack-ancestry-doctor` | hard | `StackEdge`, `EvaluateStackAncestry` (`internal/stack_ancestry.go:706`), `stackBaseRef` (`:318`), `ancestryGuidance` (`:625`), `AncestryHealthIssues` (`internal/health.go:53`) |
| `branch-name-decoupling` | hard | `StackEntry.Name` vs `GitBranch()` (`internal/stack.go:14`/`:23`) throughout §9.8 and §10 |
| `skill-distribution`, `tiered-skill-system` | soft | only §14.4's three skill files |
| `sync-transactional-abort` | soft | **stays soft**: reparent implements its own stronger transaction contract (whole-closure pre-images, one CAS commit, monotonic `commit_point_reached`, bounded abort), and §18 item 10 excludes repairing ordinary sync rollback |
| `sync-cutoff-integrity` | soft | **stays soft**: §5's per-row snapshotted cutoffs plus the mandatory `--is-ancestor` preflight are a stronger contract than that feature will ship; spec §19 states reparent MUST NOT be blocked by it |

Issue dispositions unchanged: #1 stays with `fix-inject-slash-worktree-discovery` (honoured by never
using a raw `StackEntry.Name` as a path or ref component, §4.4); #3 stays with
`scoped-status-projection` (honoured by the omitempty-pointer projection, §5.4); #4 stays split
across the two soft parents, with its stack shape adopted as the mandatory
`reparentIssue4External`/`Checkout` fixtures.

Run `tpatch feature deps --validate-all` before implementation; nothing here requires a new
`tpatch feature deps <slug> add <parent>` registration.

---

## 13. Implementer handoff checklist and stop condition

### 13.1 Before writing any production code

- [ ] Confirm `shasum -a 256 .tpatch/features/safe-reparent-restack/spec.md` is
      **`dbf248f3a094304da3b27769a1bde47762a139ed6e3c71dddb338b04aa81d24f`** (3 892 lines,
      203 507 B). A tree hashing to `f75680ee…` is pre-correction: §11.2's sentinel rationale, the
      `--plan` precheck scope, §11.6b, AC-079 and T-067 will not match, and P-7 / P-9 do not apply.
- [ ] Record this exploration's own SHA-256 (reported in the delivery summary) in the landing
      record, so a later reader can tell which revision was implemented.
- [ ] Re-read spec §15 (file ledger), §16 (AC-001…AC-100) and §17 (T-001…T-087); they are the
      authoritative tables and this document is their verified index.
- [ ] Capture the pre-change baseline: `go test ./... -count=1` green **with wall clock recorded**
      (§17.3 requires a measured before/after pair), and
      `go test ./internal/cli/ -run 'TestSyncNoFlag_' -count=1` green.
- [ ] Capture the pre-change argv sidecar transcript of a no-flag `tws sync` in **both** modes — the
      control for "no new Git process on the sync path" (§2.5 obligation 5).
- [ ] Capture the pre-change byte transcript of external `tws push` and `tws push --dry-run`, and of
      a checkout `tws sync --push` — the controls for §12.4 rule 1 and §12.4a, including the two
      `gitPush` call sites (`internal/checkout_sync.go:1628`, `:1645`).
- [ ] `tpatch feature deps --validate-all`.

### 13.2 Invariants to re-assert after every step

- [ ] the step compiles: `go build ./...` and `go vet ./...` clean;
- [ ] the 126 `internal/cli/testdata/sync_noflag/**` files are byte-identical;
- [ ] `internal/cli/testdata/rebase_plan/sync_help.txt` is byte-identical;
- [ ] `golangci-lint run ./...` clean;
- [ ] `internal` imports neither Cobra nor `internal/cli`;
- [ ] no `internal` test references a `cli` test identifier, or vice versa;
- [ ] `TopoSort` (`internal/stack.go:138-182`) is neither modified nor called by
      `ReparentClosureOrder`;
- [ ] `SaveStack` (`:128`), `atomicWriteFile` (`internal/checkout_sync.go:151`),
      `SaveGuardedLegacySentinel` (`internal/syncstate.go:243`), `mergeBaseIsAncestor`
      (`internal/rebase_plan_build.go:267`), `fetchCheckoutRepoTo`
      (`internal/checkout_sync.go:777`) and the four sync limit-algebra functions keep every caller
      and every byte of behaviour;
- [ ] `forceAcquireCheckoutLock` keeps every caller and its `:384` message is byte-identical;
- [ ] `RefusalKinds` still has 30 members; `ReparentRefusalKinds` has 49; the reparent warning domain
      has 11; §11.6's continue table is covered by **17** resume rows and §11.9 by **17** crash
      windows;
- [ ] `RebasePlan` still emits 25 keys in order and `PlanFingerprint` still reproduces every existing
      sync approval token;
- [ ] `StackStatusReport.SchemaVersion` is still `1` and the `reparent` key is **absent**, not
      `null`, without state;
- [ ] no reparent-emitted argv contains `-C`, `reset`, `replay`, `--update-refs`, `--autostash`,
      `--apply` or `push`;
- [ ] the measured real-Git leaf count is ≤ **120** and the counter test is green.

### 13.3 Definition of done for the implementation phase

- [ ] all ten NEW production files of §6.1 exist, with **no** `internal/reparent_plan_guard.go`;
- [ ] every CHANGED file of §6.2 carries only the listed smallest coherent edit, including the three
      additive files (`sync_plan_guard.go`, `sync_modes.go`, `importcmd.go`);
- [ ] every AC-001…AC-100 has a named test and every T-001…T-087 has **exactly one** owning file per
      §7.2, with the bidirectional mapping asserted and no cell owned twice;
- [ ] all eight documentation surfaces of §6.3 are updated and the roadmap/workflow assertions in
      `internal/cli/sync_plan_docs_test.go` are retargeted;
- [ ] the full gate of §8 passes and the before/after runtime pair is recorded;
- [ ] `tpatch feature deps --validate-all` passes.

### 13.4 STOP CONDITION

**This exploration ends here. Do not begin implementation from this document.**

Per `.tpatch/steering/local.md` and `docs/engineering-workflow.md`, this agent owns **only**
`.tpatch/features/safe-reparent-restack/exploration.md`; it has edited no production, test,
documentation, specification or status file, run no `tpatch` phase command, created no commit, tag
or scratch artifact, and changed no dependency registration.

Before implementation begins, the maintainer must:

1. accept this exploration;
2. acknowledge P-1 … P-18, and in particular P-15 (the fetch package boundary), P-16 (sync-typed
   guard reuse), P-18 (`gitPush` edited directly), P-7 (typed live-lock error), P-9 (the corrected
   sentinel rationale), P-5 (two checkout state directories) and P-13 (the `isRuntimeState`
   contract change);
3. re-run `tpatch feature deps --validate-all`;
4. advance exploration with `tpatch explore safe-reparent-restack --manual`;
5. proceed to the execute phase with `--start` / `--stop` per the local steering, committing
   production changes separately from the `chore(tpatch)` metadata commit.

Spec decisions are **fixed**. Nothing in this document reopens one, and no micro-decision is left
open; where the tree and the spec (or an earlier revision of this document) disagreed, §11 records
the disagreement with a resolved implementation consequence.
