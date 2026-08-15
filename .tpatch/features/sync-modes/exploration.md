# Exploration — sync-modes

Status: Path B exploration (author: isolated exploration agent). Input of record:
`.tpatch/features/sync-modes/spec.md` (normative), `.tpatch/features/sync-modes/analysis.md`,
`.tpatch/features/sync-modes/request.md`, `.tpatch/features/sync-modes/status.json`,
`AGENTS.md`, `CLAUDE.md`, `docs/engineering-workflow.md`, `docs/roadmap.md`,
`.tpatch/steering/local.md`, `.claude/skills/tessera-patch/SKILL.md`.

This document is a **codebase map**, not a spec summary. Every existing path and symbol named here
was read in the working tree at `df390cd` (`git describe` = `v1.2.14-2-gdf390cd`). Paths that do not
exist yet are labelled **NEW**. Anchors are symbol names; line numbers appear only where they were
verified and are given as "at line N" so they can be re-derived with `grep -n`.

Where this exploration contradicted the spec, the contradiction was raised explicitly in §12
(**P1–P17**) rather than silently absorbed. **All of P1–P17 are now settled in the spec** (revision
of 2026-08-15): §12 records each one as *resolved*, with the decision that was taken, so nothing in
this document is left blocking. The decisions that changed the design are:

- **P1** — one external sync layout resolver, `resolveExternalSyncLayout` / `externalSyncLayout`,
  normative in spec §3.11, with candidate B (`filepath.Join(twsRoot, feature)`, today's
  `internal.FeaturePath` value) winning whenever it holds a readable `stack.yaml`; the resolver
  takes the caller's already-resolved `twsRoot` and is **Git-free** — see **P15**, which corrects the
  resolution accounting this document and the spec previously understated;
- **P15** — the workspace-root resolution *event* is a **pair** of Git records grouped by an
  **anchored** rule (anchor on `--git-common-dir`, never on a bare `--show-toplevel`, with unpaired
  bare `--show-toplevel` records kept as standalone `LoadConfig` probes), each external command
  resolves the root once, and the external push reduction is `1 + N` → **2**, not → 1 (§2.1,
  §12 P15);
- **P17** — the anchor is the **cwd-scoped** `--git-common-dir` record only:
  `inferExternalRepoRoot`'s sibling/configured/worktree probes (`RequireWorkspace`'s fallback arm,
  cwd cells 4–6) are ordinary **non-event** records, never grouped, compared verbatim in position,
  scaling with the workspace's materialized entries, and removed only together with a
  `RequireWorkspace` event this feature removes (push path only) (§2.1, §12 P17);
- **P16** — `cli.runCheckoutSync` keeps `internal.RequireFeaturePath` and therefore its **second**
  `RequireWorkspace` event, so the checkout argv log's resolution prefix is unchanged and the added
  containment probe stays the path's only argv difference (§2.2, §12 P16);
- **P2** — no second sort and no `TopoSort` change: parent-before-child is the only ordering
  guarantee, and multi-anchor output is asserted as an unordered set (spec §3.7);
- **P5** — `SyncClassifyOpts.Alive` as an injected liveness seam, with `tws status` passing its
  existing `AgentStatusOpts.Proc` prober (spec §11.1, §17.4).

---

## 1. Baseline and scope

### 1.1 Tree state

| Fact | Value |
|---|---|
| HEAD | `df390cd chore(tpatch): define sync modes` |
| `git describe --tags` | `v1.2.14-2-gdf390cd` (so §9's "old binary = v1.2.14" is the current released tag — verified against `git tag`) |
| Working tree | `M .tpatch/FEATURES.md`, `?? .tpatch/features/open-worktree-command/` — both pre-existing, **untouched by this exploration** |
| Go | `go1.26.5 darwin/arm64`; `go.mod` requires `go 1.26.1`, `github.com/spf13/cobra v1.10.2`, `gopkg.in/yaml.v3 v3.0.1` (indirect: `pflag v1.0.9`, `mousetrap v1.1.0`) |
| Git | `git version 2.55.0` |
| `golangci-lint` | present at `/Users/jdbencardinop/go/bin/golangci-lint` |
| Baseline gate | `GIT_CONFIG_COUNT=0 go test ./... -count=1` → **PASS** (internal 48.4 s, internal/cli 70.6 s) |

**Host hazard, verified.** This machine exports `GIT_CONFIG_COUNT=2` with
`safe.bareRepository=explicit` and `credential.interactive=never`. A probe test using the shipped
`setupGitRepo` helper failed with
`fatal: cannot use bare repository '…/remote.git' (safe.bareRepository is 'explicit')` until
`t.Setenv("GIT_CONFIG_COUNT", "0")` was added. `newGoldenBuilder`
(`internal/cli/stack_status_test.go`) already does this; `setupGitRepo`
(`internal/cli/new_integration_test.go`) and `setupGitRepoCheckout`
(`internal/cli/checkout_lifecycle_test.go`) do **not**. §17.6's `GIT_CONFIG_COUNT=0` rule is
therefore a real requirement for every new test, not boilerplate.

### 1.2 Package boundary

Two packages matter: `internal` (library) and `internal/cli` (Cobra surface). `internal/cli`
imports `internal`; the reverse never happens. `cmd/tws` only calls `cli.Execute()`.
`internal/cli/root.go` registers `syncCmd()` among 27 commands; no other command constructs a sync.

Everything the feature touches is in these two packages plus `assets/skills/**` (embedded, see
§9.4) and Markdown docs.

---

## 2. Verified call graphs

### 2.1 External sync — current control flow

```
cmd/tws → cli.Execute() → syncCmd().RunE
  │  internal.RequireTool("git")                      internal/exec.go  (os.Exit(1) on miss)
  │  internal.RequireWorkspace()                      internal/workspace.go
  │       ← resolution event #1: git rev-parse --show-toplevel  (LoadConfig → repoConfigPath → RepoRoot)
  │                              git -C <cwd> rev-parse --git-common-dir  (MainRepoRoot)
  ├─ ws.Mode == internal.ModeCheckout → runCheckoutSync(feature, push, testCmd, cont, abort, verbose)
  │                                                   internal/cli/checkout_sync.go
  │  internal.GuardFeatureName(internal.TwsRoot(), feature)
  │       ← resolution event #2 (TwsRoot shape): git -C <cwd> rev-parse --git-common-dir
  │                                              git rev-parse --show-toplevel   (reverse order)
  │  featurePath, _ := ws.ResolveFeaturePath(feature)   ← candidate A (ws.MetadataRoot)
  ├─ abort → handleSyncAbort(feature, featurePath)
  │            internal.LoadSyncState(featurePath)
  │            internal.WorktreePath(feature, state.FailedBranch)  ← candidate B (TwsRoot())
  │            isRebaseInProgress(path) → git rebase --abort (silent)
  │            internal.DeleteSyncState(featurePath) ; "Sync state cleared."
  ├─ cont  → handleSyncContinue(feature, featurePath, push)
  │            internal.LoadSyncState → rebase-in-progress refusal
  │            internal.LoadStack(featurePath)          ← candidate A
  │            internal.GetBranch(stack, state.FailedBranch)
  │            branchContainsConfiguredParent(feature, stack, failedEntry)   ← candidate B worktrees
  │            formatSyncStatus(failed,"active","resolved")
  │            internal.TopoSort(stack)  (whole stack)
  │            "Resuming sync with %d pending branch(es)"  (len(state.Pending))
  │            syncWithStackFiltered(feature, featurePath, stack, sorted, done)
  ├─ internal.HasSyncState(featurePath) → "previous sync incomplete (failed on: %s); …"
  │            ← state, _ := LoadSyncState(...) then state.FailedBranch  ⇒ NIL DEREF on corrupt file (C1)
  └─ syncFeature(feature, verbose)
       featurePath := internal.FeaturePath(feature)     ← candidate B  (C4 target; resolved, P1)
                                                       ← resolution event #3 (TwsRoot shape)
       internal.LoadStack(featurePath)
         err → fetchQuiet("", "", verbose); syncFallback(featurePath); Complete:true
       internal.UniqueRepos(stack, featurePath) → fetchQuiet per repo
       internal.TopoSort(stack)  → syncWithStack → syncWithStackFiltered(..., nil)
```

`syncWithStackFiltered` (`internal/cli/sync_helpers.go`), the **two passes**:

```
pass 1 — materialized entries, in `sorted` order
  skip alreadyDone
  path := internal.WorktreePath(feature, entry.Name)      ← candidate B
                                                         ← one MORE TwsRoot resolution event, per entry
  os.Stat(path) missing → continue  (falls to pass 2)
  checkSyncWorktreeBranch(path, entry) → internal.CheckWorktreeBranch(path, entry.GitBranch())
       issue → "  [?] NAME (active)" + Problem + Hint → saveIncompleteSync(...) → exit 1
  base := resolveEntryBase(stack, entry)
  gitContext := path, or entry.Repo when non-empty
  currentBaseSHA := internal.GetBranchSHA(gitContext, base)
  rebaseArgs = ["rebase","--update-refs",base]
      or      ["rebase","--update-refs","--onto",base,entry.LastBaseSHA] when LastBaseSHA moved
  internal.RunDirClean(path,"git",rebaseArgs...)
      err → "  [!] NAME (active)" + 3 hint lines → saveIncompleteSync → exit 1
  runValidation(path, entry.Name)         ← internal.LoadConfig().TestCommand, strings.Fields
                                          ← STANDALONE `git rev-parse --show-toplevel` record
                                            (LoadConfig → repoConfigPath → RepoRoot); NOT half of a
                                            resolution event, and immediately followed by the next
                                            entry's TwsRoot event ⇒ the measured
                                            `standalone show → common → show` adjacency (P15)
      false → saveIncompleteSync → exit 1
  "  [+] NAME (active)"
  markUpdatedAncestors(stack, entry.Name, featurePath, updatedByRef)
  internal.UpdateBaseSHA(&stack, entry.Name, currentBaseSHA); internal.SaveStack(featurePath, stack)

pass 2 — non-materialized entries, same `sorted` order
  internal.IsPrunableWorktree(entry.GitBranch()) → "  [?] NAME (missing — run: …)" → stop, exit 1
  updatedByRef[entry.Name] → "  [+] NAME (archived)" free-mark, continue
  base := resolveEntryBase(stack, entry)
  RunSilentDir(entry.Repo,…)/RunSilent "git rebase <base> <gitbranch>"  (no --update-refs)
      err → rebase --abort; "  [!] NAME (archived)" + "    Restore with: tws new …" → stop
  "  [+] NAME (archived)"     (LastBaseSHA NOT updated here)

completion gate
  staleStackEdges(feature, stack)   ← whole stack, unfiltered; internal.WorktreePath per child edge
                                      ⇒ one MORE TwsRoot resolution event per probed edge
      non-empty → "Sync incomplete; stale stack edges remain:" + "  [!] <edge>" per edge
                  saveIncompleteSync(featurePath, sorted, completed, "")  ⇒ empty FailedBranch
  else internal.DeleteSyncState(featurePath); Complete:true
```

Then back in `RunE`: `"Sync complete."`, and with `--push`: `"\nPushing..."` +
`pushFeature(feature, false)` — which resolves **candidate A** again through
`internal.RequireFeaturePath` / `internal.RequireWorktreePath` (`internal/cli/push.go:36,47`), so a
divergent workspace syncs under B and then fails its push half under A.

**`tws push` — current control flow** (`internal/cli/push.go`, verified line by line):

```
pushCmd().RunE
  internal.RequireTool("git")
  pushFeature(args[0], dryRun)
    internal.RequireFeaturePath(feature)            internal/resolve.go:294
        RequireWorkspace()                          ← resolution #1: git -C <cwd> rev-parse --git-common-dir
        GuardFeatureName(ws.MetadataRoot, feature)  ← THE ONLY sibling-space guard on this command
        ws.ResolveFeaturePath(feature)              ← candidate A
    internal.LoadStack(featurePath) → "no stack.yaml found for feature: %s"
    per entry:                                      ← N entries, archived ones included
      internal.RequireWorktreePath(feature, entry.Name)     internal/resolve.go:325
          RequireWorkspace()                        ← resolution #1+i: ANOTHER git -C <cwd> rev-parse --git-common-dir
          checkout mode ⇒ ws.WorktreePath() == "" ⇒ ErrWorktreeUnsupported
          "linked worktrees are not supported in checkout mode" (nonzero exit, first entry)
      os.Stat missing → "  [-] NAME (archived, skipped)"
      dryRun         → "  [~] NAME (would push --force-with-lease)"
      RunDirClean(repoDir,"git","push","--force-with-lease","origin", entry.Name)   ← C2/M14 defect
```

**Resolution accounting — measured, and previously understated twice (P15).** A workspace-root
resolution is **not** one Git record; it is an ordered **pair**, and there are two shapes
(spec §3.11):

| Event | Chain | Records, in order |
|---|---|---|
| `RequireWorkspace` event | `RequireWorkspace` → `LoadConfig` → `repoConfigPath` → `RepoRoot` (`internal/config.go:35-41`, `internal/exec.go:44-49`), then `MainRepoRoot` (`internal/exec.go:18-28`) | `git rev-parse --show-toplevel`, then `git -C <cwd> rev-parse --git-common-dir` |
| `TwsRoot` event | `TwsRoot` → `MainRepoRoot` first, then `LoadConfig` → `repoConfigPath` → `RepoRoot` (`internal/paths.go:74-79`) | `git -C <cwd> rev-parse --git-common-dir`, then `git rev-parse --show-toplevel` |

Measured on the built binary with a logging `git` wrapper on `PATH` (real Git delegated to, argv
appended to a log):

```
$ tws push nonexistent-feature          # RequireFeaturePath → RequireWorkspace, LoadStack fails
git rev-parse --show-toplevel
git -C <repo> rev-parse --git-common-dir            ⇒ exactly ONE RequireWorkspace event

$ tws sync nonexistent-feature-xyz --abort          # external arm, no state on disk
git rev-parse --show-toplevel
git -C <repo> rev-parse --git-common-dir            ⇒ RequireWorkspace event
git -C <repo> rev-parse --git-common-dir
git rev-parse --show-toplevel                       ⇒ TwsRoot event (guard), reversed order
```

Nothing else in either chain shells out: `ResolveCurrentWorkspaceE`, `resolveExternalRoot`,
`resolveWorkspaceMetadataRoot`, `DetectWorkspaceRoot`, `canonicalize`, `stableID`, and
`GuardFeatureName` (`internal/spaces.go:692`) are pure filesystem/string work; the only extra Git in
the chain is `inferExternalRepoRoot`'s candidate probes on `RequireWorkspace`'s **fallback** arm
(`internal/workspace.go:339-395`), reached whenever cwd is outside any repository and unchanged by
this feature.

**And a `--git-common-dir` record is not proof of a resolution event either (the fallback arm).**
`MainRepoRoot` is the *only* caller that passes the process cwd to `MainRepoRootIn`
(`internal/exec.go:18-24`); `inferExternalRepoRoot` calls `MainRepoRootIn(<other path>)` once per
candidate — configured workspace keys (Go **map** order), the `.tws` sibling repo, and every
materialized entry of every feature in the workspace. Measured against the built binary in a real
external workspace (`<root>/repo.tws`, feature `myfeat`, three materialized entries, no configured
workspaces), from the **workspace root** (cwd cell 4) and identically from the **feature directory**
and a nested subdirectory of it (cells 5–6):

```
$ tws sync myfeat --abort                              # cwd = <root>/repo.tws
git rev-parse --show-toplevel                          ┐ RequireWorkspace event
git -C <cwd> rev-parse --git-common-dir                ┘ (anchor; both records exit 128)
git -C <root>/repo                        rev-parse --git-common-dir   ← infer: sibling repo
git -C <ws>/myfeat/worktrees/feat-root    rev-parse --git-common-dir   ← infer: materialized entry
git -C <ws>/myfeat/worktrees/feat-a       rev-parse --git-common-dir   ← infer: materialized entry
git -C <ws>/myfeat/worktrees/feat-b       rev-parse --git-common-dir   ← infer: materialized entry
git -C <cwd> rev-parse --git-common-dir                ┐ TwsRoot event (guard)
git rev-parse --show-toplevel                          ┘ (anchor; both records exit 128)

$ tws sync myfeat --abort                              # cwd = <root>/repo  (cells 1–3)
show, common@cwd, common@cwd, show                     ⇒ two events, ZERO infer records
```

So the anchor test is on the **operand**, not the verb: a `--git-common-dir` record anchors an event
**iff** its `-C` operand is the record's own process cwd. Everything else is an
`inferExternalRepoRoot` probe — an ordinary non-event record, never grouped (spec §3.11). Three
measured properties matter for the comparator:

- **It scales with the fixture, not with the change**: `C + S + M` records (configured mappings +
  sibling + materialized entries across **all** features). Measured `0 + 1 + 3 = 4`, and `5` after
  materializing a second feature (whose probe sorts first — `os.ReadDir` order).
- **The configured-workspace class is unordered**: with four configured workspaces mapping to one
  metadata root, three consecutive runs emitted the four probes in three different orders. No such
  fixture may be byte-pinned.
- **The block is attached to its `RequireWorkspace` call** and always contiguous, immediately after
  that call's completed `show → common@cwd` pair — `TwsRoot`'s failing `MainRepoRoot` triggers no
  inference. Measured `tws push myfeat` from cell 4 with `N = 3` emits `1 + N = 4` `RequireWorkspace`
  events, **each** trailed by its own four-record block; the post-change path has one
  `RequireWorkspace`, hence one block. External **sync** has exactly one `RequireWorkspace` before
  and after, so its block is invariant and must compare verbatim in position.

The layout refactor cannot remove these records: `RequireWorkspace` still runs (and still falls back
from cells 4–6) **before** `internal.TwsRoot()` and `resolveExternalSyncLayout`, and
`internal/workspace.go` is untouched.

**But a bare `rev-parse --show-toplevel` record is *not* proof of a resolution event.**
`internal.LoadConfig` emits one on its own — `LoadConfig` → `repoConfigPath` → `RepoRoot`
(`internal/config.go:35-41`) — from every call site that is neither `RequireWorkspace` nor
`TwsRoot`, and one of those sites is on the external sync path itself: `runValidation`
(`internal/cli/sync_helpers.go:237-238`), called once per validated entry. Its record is a
**standalone `LoadConfig` probe**: a single bare `rev-parse --show-toplevel` with no
`--git-common-dir` partner. Grouping must therefore anchor on **cwd-scoped** `--git-common-dir`
records, never on show records and never on the fallback arm's foreign-path `--git-common-dir`
records (spec §3.11 grouping rule, P15, P17).

**Why nothing is left ungrouped once both distinctions hold.** Every surviving anchor comes from
`MainRepoRoot()` inside `RequireWorkspace` (show, then anchor) or inside `TwsRoot` (anchor, then
show), and in both chains the partner is **adjacent** — verified from cells 1–6, in both the success
(exit 0) and failure (exit 128) cases. An infer record can only appear *after* a completed
`RequireWorkspace` pair, so it can only make the forward test decline, which is the correct reading.
The "otherwise fail" rule therefore never fires on this tree; it is a fail-closed guard for a future
caller that interleaves Git between `LoadConfig` and `MainRepoRoot`, and for the degenerate fixture
whose cwd is itself an inference candidate.

**Therefore, today:**

- **External `tws sync`** performs `3 + N + E` resolution events for a clean plain run over `N`
  materialized entries with `E` stale-edge child probes: `RequireWorkspace`, the guard's `TwsRoot`,
  `syncFeature`'s `internal.FeaturePath`, one `internal.WorktreePath` per entry, and one per probed
  edge. Every one of them is a full pair of records.
- **External `tws push`** performs `1 + N` `RequireWorkspace` events: `RequireWorktreePath`
  re-enters `RequireWorkspace` on **every** iteration (`internal/resolve.go:323-334`), **before** the
  archived `os.Stat` skip, and the loop stops early only when a resolution itself errors. The same
  `1 + N` shape applies to the push half of `tws sync --push`, which calls `pushFeature` directly.

An earlier revision of this document and of spec §3.11 claimed external
`tws push` resolves the workspace once; a later revision claimed the fix leaves it resolving
**exactly once**, and counted only `--git-common-dir` records. **Both claims were false.** The
corrected statements are: each external command performs exactly **two** events after the fix (one
`RequireWorkspace` + one `TwsRoot`); external sync therefore drops `1 + N + E` events — which
**does** reach the frozen AC 1 external captures — and external push goes `1 + N` → **2**, unchanged
in count at `N = 1` and one event **larger** on an empty stack (spec §3.11, §4.1 rule 6c, AC 2,
AC 59).

Three facts about this graph are load-bearing; the first two were nearly lost in the first revision,
the third was stated incorrectly until this revision:

1. `internal.RequireFeaturePath` is the **only** place `tws push` guards the feature name against a
   registered sibling space, and `TestSpaceGuard_ExternalCommandMatrix/push`
   (`internal/cli/space_guard_test.go:270-276`) pins the resulting error, exit, and untouched-tree
   snapshot. Replacing `RequireFeaturePath` with `resolveExternalSyncLayout` — which guards nothing
   by design (spec §3.11) — would delete that guard silently. Hence the settled shape: `pushCmd`
   itself calls `internal.RequireWorkspace()` then
   `internal.GuardFeatureName(ws.MetadataRoot, feature)` **before** any layout work.
2. `internal.RequireWorktreePath` is what makes `tws push` **fail** in checkout mode
   (`ErrWorktreeUnsupported`, nonzero, on the first stack entry). Routing checkout push through the
   external layout resolver would replace that refusal with a worktree-less `os.Stat` miss per
   entry — i.e. `  [-] NAME (archived, skipped)` for every branch and **exit 0**: a silent success
   where the command used to say plainly that it cannot do the job. Hence the settled shape: the
   checkout arm keeps today's body verbatim as `pushFeatureCheckout`.
3. The per-entry `internal.RequireWorktreePath` call is also **the reason the pre-change external
   push path performs `1 + N` `RequireWorkspace` events**. Removing it (the external helper now
   receives the resolved `layout`) collapses that to **two** events per command invocation — the
   `RunE` `RequireWorkspace` event plus the one `TwsRoot` event the external arm makes to build the
   layout — i.e. `1 + N` → **2**, not → 1: unchanged in count at `N = 1`, one event larger on an
   empty stack, smaller for every `N ≥ 2`. Each removed event takes **both** of its records
   (`rev-parse --show-toplevel` *and* `-C <cwd> rev-parse --git-common-dir`) with it. It
   is a read-only change — no mutating argv, no output, no exit code, and no ref moves — and it
   is part of the third closed C4 argv carve-out of spec §4.1 rule 6 (rule 6c), asserted by AC 2 and
   AC 59.

**After the settled decision (spec §3.11)** the command becomes:

```
pushCmd().RunE
  internal.RequireTool("git")                                    (1) unchanged, first
  feature := args[0]                                             (2)
  ws, err := internal.RequireWorkspace()                         (3) error verbatim — resolution event #1
  internal.GuardFeatureName(ws.MetadataRoot, feature)            (4) same root, same position, error verbatim
  ws.Mode == internal.ModeCheckout                               (5) mode branch
    ├─ checkout → pushFeatureCheckout(feature, dryRun)           today's body, verbatim
    │                                                            NO internal.TwsRoot() on this arm
    │                                                            (+1 RequireWorkspace event, from step 3)
    └─ external → twsRoot := internal.TwsRoot()                  resolution event #2, ONCE, after the branch
                  layout, err := resolveExternalSyncLayout(ws, twsRoot, feature)
                                                                 error verbatim, no Git, no resolution
                  pushFeature(feature, layout, dryRun)           GitBranch() ref (C2)
                    per entry: worktree = filepath.Join(layout.WorktreesRoot, entry.Name)
                               ← NO RequireWorktreePath, NO per-entry RequireWorkspace
                    RunDirClean(repoDir,"git","push","--force-with-lease","origin", entry.GitBranch())
```

**Post-change resolution count: exactly two events, never one.** The external push path performs one
`RequireWorkspace` event (in `pushCmd.RunE` for `tws push`, in `syncCmd`'s `RunE` for
`tws sync --push`) and one `TwsRoot` event (the external arm's single `internal.TwsRoot()` call);
`pushFeature` / `pushSelected` resolve nothing of their own, and the resolver resolves nothing at
all. `1 + N` → **2**: events are removed for `N ≥ 2`, the count is **unchanged** for `N = 1` (only
the second event's shape, and therefore its two records' order, differs), and one event is **added**
for an empty stack. The ordered
mutating `git push --force-with-lease origin <GitBranch>` argv, the per-entry stdout lines, the exit
code, and the resulting refs are compared **separately** and are unchanged for coupled names (the
decoupled operand change is C2, spec §4.5, AC 33). Whole-log argv identity across the change is
impossible on this path and is asserted nowhere.

`resolveExternalSyncLayout` carries **no** guard of its own, and `syncCmd` keeps its own guard with
the same root **value** and position — the only textual change is that it now reads
`internal.GuardFeatureName(twsRoot, feature)`, where `twsRoot` comes from the `RunE`'s single
`internal.TwsRoot()` call — so the two commands still guard against
different roots and this feature preserves that difference rather than harmonising it
(spec §3.11, follow-up §18 item 10). The two mode arms move in opposite directions, and both are
read-only. **Checkout** resolves the workspace once more inside `RequireFeaturePath`: one extra
**`RequireWorkspace` event** — both `git rev-parse --show-toplevel` and
`git -C <cwd> rev-parse --git-common-dir` — plus one extra `spaces.yaml` read (today's checkout push
already performs two such events on a non-empty stack — `RequireFeaturePath`, then the first entry's
`RequireWorktreePath` before `ErrWorktreeUnsupported` — and one on an empty stack), and **no**
`TwsRoot` event, because that arm never calls `internal.TwsRoot()`. **External**
goes from `1 + N` events to two. Both are declared in spec
§3.11 and §4.1 rule 6, and both are asserted non-observable by AC 59 (stdout, exit code, refs, and
the ordered mutating push argv unchanged).

**After the settled P1 decision (spec §3.11)** every `candidate A` / `candidate B` derivation above
collapses into one `externalSyncLayout`, resolved once at `RunE` step 6 from the **one**
`twsRoot := internal.TwsRoot()` value the `RunE` already resolved at step 5 for the guard — so the
whole external sync run performs exactly **two** resolution events (one `RequireWorkspace`, one
`TwsRoot`) instead of `3 + N + E`, and `resolveExternalSyncLayout` itself issues no Git call — and
threaded explicitly into
`handleSyncAbort`, `handleSyncContinue`, `branchContainsConfiguredParent`, `syncFeature`,
`syncWithStackFiltered`, `staleStackEdgesFiltered`, `saveIncompleteSync`, `markUpdatedAncestors`,
`syncFallback`, `runValidation`, the three runtime-state paths, `pushFeature`/`pushSelected`, and
`syncEntryCompletion`. B wins when it holds a readable `stack.yaml` (including when both do), A
wins when only A does, B wins when neither does. The graph above is therefore the **pre-change**
graph, kept as the baseline the goldens capture.

### 2.2 Checkout sync — current control flow

```
cli.runCheckoutSync(feature, push, testCmd, cont, abort, verbose)     internal/cli/checkout_sync.go
  internal.RequireFeaturePath(feature)     → RequireWorkspace + GuardFeatureName(ws.MetadataRoot,…) + ResolveFeaturePath
                                           ← resolution event #2 of the run (RequireWorkspace shape);
                                             #1 is syncCmd.RunE's own RequireWorkspace
  repoDir, _ := os.Getwd()                 ← the C4 defect (cwd cell 9)
  opts := internal.CheckoutSyncOpts{Feature, FeaturePath, RepoDir, Push, TestCommand, Verbose}
  abort → internal.AbortCheckoutSync(opts) ; "Checkout sync aborted, original branch restored."
  cont  → internal.ContinueCheckoutSync(opts) ; "Checkout sync completed."
  internal.HasCheckoutTransaction(featurePath) → "previous checkout-sync incomplete; use --continue or --abort"
  internal.RunCheckoutSync(opts) ; "Checkout sync complete."
```

**Post-change (spec §10.1, §10.3, §13.4 rule 1):** the signature becomes
`(ws internal.Workspace, opts internal.CheckoutSyncOpts)`, but the
`internal.RequireFeaturePath(feature)` call above is **kept verbatim** — same position, same second
`RequireWorkspace` event, same guard, same layout resolution, same errors. `ws` is used **only** for
`RepoDir = ws.RepoRoot`, so no third resolver is introduced and none is removed. The single
`git -C <cwd> rev-parse --show-toplevel` containment probe is inserted **after** that resolution and
**before** `RunCheckoutSync`'s first preflight record, which makes it the **only** argv difference on
the checkout path (spec §4.1 rule 6a, AC 2). Replacing `RequireFeaturePath` with
`internal.ResolveFeaturePathFor` — the earlier wording — would have silently **removed** a
pre-change resolution event, an undeclared change on a frozen path, and is explicitly rejected.

`internal.RunCheckoutSync` (`internal/checkout_sync.go`), read-only prefix then first side effect:

```
1 HasCheckoutTransaction            → "checkout sync transaction already exists; use --continue or --abort"
2 gitOperationInProgress(RepoDir)   → "another Git operation is in progress; …"      [rev-parse --git-path rebase-merge …]
3 gitWorkingTreeDirty(RepoDir)      → "check working tree: %w" / "working tree is dirty; …"  [status --porcelain]
4 gitCurrentBranch(RepoDir)         → "cannot sync from detached HEAD: %w"           [symbolic-ref --short HEAD]
5 gitResolveRef(RepoDir,"HEAD")                                                       [rev-parse HEAD]
6 AcquireCheckoutLock(FeaturePath)  ← FIRST SIDE EFFECT
7 LoadStack(FeaturePath)            → release lock; "load stack: %w"
8 BuildCheckoutPlan(RepoDir, stack) → release lock; "build plan: %w"
9 len(plan)==0 → release lock, return nil
10 tx := &CheckoutTransaction{…, Stage: StagePlanned}
11 SaveCheckoutTransaction  ← persisted BEFORE the first git checkout
12 executeTransaction → processBranch* → finalizeTransaction → finalizeCleanup
```

`processBranch` → `gitResolveRef(entry.Base)` (re-resolve) → `StepHook(StagePlanned)` →
`StageSwitched` + save → `gitCheckout` → `StepHook(StageSwitched)` → `doRebase` → `gitIsAncestor` →
optional `runValidation`. `doRebase` → `StageRebasing` + save → `StepHook(StageRebasing)` →
`gitRebaseOnto(NewBaseSHA, LastBaseSHA)` when the recorded base moved, else
`gitPlainRebase(entry.Base)` → conflict ⇒ `StageConflict`/`FailConflict`/`FailureMsg =
(*RebaseConflictError).Error()` = `"rebase conflict: " + CombinedOutput` → `StageRebased` + save →
`StepHook(StageRebased)`.

`finalizeTransaction` → reload stack → **`stack.Branches[i].GitBranch() == pe.Branch` first-match**
`LastBaseSHA` write (the C3 defect) → `SaveStack` → per-entry final `gitIsAncestor` →
`StageRestoring` → `StepHook(StageRestoring)` → `restoreOriginal` → optional push loop
(`gitPush` = `git push --force-with-lease origin <pe.Branch>`) → `finalizeCleanup` (delete
transaction, release lock).

`ContinueCheckoutSync` → `LoadCheckoutTransaction` → `forceAcquireCheckoutLock` →
one-way push rule → `opts.Push/TestCommand = tx.*` → `resumeTransaction` (stage dispatch:
`StageConflict`, `StageSwitched`, `StageRebased`, `StageValidating`, `StageRestoring`,
`StagePlanned|StageRebasing`, `StageCompleted`, default `unknown stage %q`).

`AbortCheckoutSync` → `LoadCheckoutTransaction` → `forceAcquireCheckoutLock` → `git rebase --abort`
when `gitRebaseInProgress` → `restoreOriginal` → `DeleteCheckoutTransaction` →
`ReleaseCheckoutLock`.

### 2.3 The three `.sync-state.yaml` consumers (spec §1 claim — verified exhaustively)

`grep -rn "SyncStatePath\|LoadSyncState\|HasSyncState\|SaveSyncState\|DeleteSyncState\|NewSyncState\|\.sync-state"`
over non-test Go files returns exactly:

1. `internal/syncstate.go` (the type and its five accessors);
2. `internal/cli/sync.go` + `internal/cli/sync_helpers.go` (the command);
3. `internal/agent_status.go` `buildFeatureSync` (at line 1408–1412);
4. `internal/cli/importcmd.go` `isRuntimeState` (at line 174).

No other production consumer exists. `internal/cli/export.go` never names the file — it is
allow-listed to `workspace.yaml` + `inject/**` by construction.

---

## 3. Symbol and file map

### 3.1 Existing symbols the feature consumes or edits (all verified present)

**`internal/stack.go`** — unchanged by §13.5, consumed by the selector:

| Symbol | Signature / shape | Notes for the selector |
|---|---|---|
| `StackEntry` | `{Name, Branch, Archived, Base, Repo, LastBaseSHA}` with `yaml` tags | `Branch`, `Archived`, `Repo`, `LastBaseSHA` are `omitempty` |
| `(StackEntry) GitBranch()` | `string` | `Branch` when set, else `Name` |
| `Stack` | `{Branches []StackEntry}` | |
| `TopoSort(s Stack) ([]StackEntry, error)` | Kahn | **seeds its queue from a Go map** ⇒ order among in-degree-0 entries is random (**P2**, settled: parent-before-child is the only contract, and this function is **not** modified) |
| `Descendants(s Stack, branch string) map[string]bool` | BFS over `children[e.Base]` | includes cross-repo children; excludes the named entry (D2 adds it back) |
| `GetBranch(s, name) StackEntry` | first `Name` match, zero value when absent | I10/I11 source |
| `HasBranch(s, name) bool` | `Name` match | I10 source |
| `UniqueRepos(s Stack, featurePath) map[string]string` | repo → a worktree path (or `""`) | map ⇒ fetch order non-deterministic (already declared) |
| `UpdateBaseSHA(*Stack, name, sha)` | by `Name` | |
| `GetBranchSHA(gitContext, branch) string` | `git [-C ctx] rev-parse <branch>` | |
| `LoadStack/SaveStack/StackPath` | `stack.yaml`, `os.WriteFile(…, 0644)` | whole-file rewrite |

**`internal/cli/sync.go`** — `syncCmd`, `handleSyncAbort`, `handleSyncContinue`,
`branchContainsConfiguredParent`, `isRebaseInProgress`, `syncFeature`, `fetchQuiet`.
Flag registration block is the **last** statement group before `return cmd` (five
`cmd.Flags().*` lines, at lines 76–80). `ValidArgsFunction` returns `internal.ListFeatures()`.

**`internal/cli/sync_helpers.go`** — `syncResult`, `syncWithStack`, `syncWithStackFiltered`,
`saveIncompleteSync`, `completedNames`, `staleStackEdges`, `checkSyncWorktreeBranch`,
`resolveEntryBase`, `resolveBase`, `markUpdatedAncestors`, `formatSyncStatus`, `syncFallback`,
`runValidation`.

**`internal/syncstate.go`** — `SyncState{StartedAt, FailedBranch, Pending, Completed, Skipped}`
(no `omitempty` anywhere ⇒ a fresh sentinel marshals `pending: []`, `completed: []`, `skipped: []`,
exactly the legacy shape), `SyncStatePath`, `LoadSyncState`, `SaveSyncState` (`os.WriteFile`, `0644`),
`DeleteSyncState`, `HasSyncState`, `NewSyncState`.

**`internal/checkout_sync.go`** — enums `CheckoutStage` (`planned|switched|rebasing|conflict|
rebased|validating|completed|restoring`) and `FailureKind` (`""|conflict|validation|interruption|
switch|persistence|ancestry|restoration`); `CheckoutPlanEntry{Branch, Base, LastBaseSHA, NewBaseSHA,
PreSHA, PostSHA}`; `CheckoutTransaction{Feature, StartedAt, LockPID, LockCreated, Push, TestCommand,
OriginalBranch, OriginalHEAD, Plan, CurrentIndex, Stage, FailureKind, FailureMsg,
CompletedIndices}`; `checkoutStateDir`, `CheckoutTransactionPath`, `CheckoutLockPath`;
`LoadCheckoutTransaction`, `SaveCheckoutTransaction` (`atomicWriteFile`, `0600`),
`atomicWriteFile` (**unexported**, `MkdirAll(dir,0700)` + `CreateTemp(dir,".tws-state-*")` + `Chmod`
+ `Sync` + `Rename`), `DeleteCheckoutTransaction`, `HasCheckoutTransaction`; `LockInfo{PID,Created}`,
`AcquireCheckoutLock`, `writeLockExclusive` (`O_WRONLY|O_CREATE|O_EXCL`, `0600`),
`removeLockIfUnchanged`, `ReleaseCheckoutLock`, `HasCheckoutLock`, `forceAcquireCheckoutLock`,
`ReadCheckoutLock`, `isProcessAlive`; `var StepHook func(CheckoutStage, int) error`; the git helpers
`gitResolveRef`, `gitCurrentBranch`, `gitCheckout`, `gitRebaseOnto`, `gitPlainRebase`,
`gitIsAncestor`, `gitRebaseInProgress`, `gitOperationInProgress`, `gitPathExists`,
`gitWorkingTreeDirty`, `checkoutGitOutput`, `gitPush`; `RebaseConflictError`; `BuildCheckoutPlan`;
`CheckoutSyncOpts`; `RunCheckoutSync`, `ContinueCheckoutSync`, `AbortCheckoutSync`,
`executeTransaction`, `resumeTransaction`, `resumeFromSwitched`, `resumeFromRebased`,
`resumeFromValidating`, `resumeFromRestoring`, `processBranch`, `doRebase`, `finalizeTransaction`,
`finalizeCleanup`, `restoreOriginal`, `shortCheckoutSHA`, `runValidation`, `pidStr` (dead, kept
alive by `var _ = pidStr`), `TestIsAncestor`, `MarshalLockInfo`.
**Package `internal` currently contains zero `fmt.Print*` calls in this file** — verified; §3.10's
"exactly three new print paths" starts from zero.

**`internal/exec.go`** — `RequireTool`, `MainRepoRoot`, `MainRepoRootIn`, `RepoRoot`,
`BranchExists`, `DefaultBranch`, `DefaultBranchIn`, `VerifyGitRef`, `GitRepoRootIn`, `AbsPath`,
`Run`, `RunDir`, `RunDirClean`, `runWithFilteredStderr`, `RunSilent`, `RunSilentDir`,
`IsPrunableWorktree`, `Must`.

**`internal/workspace.go` / `internal/resolve.go`** — `WorkspaceMode`, `ModeExternal`,
`ModeCheckout`, `Workspace{RepoRoot, Mode, MetadataRoot, StableID, Caps}`, `ResolveCurrentWorkspace`,
`ResolveCurrentWorkspaceE`, `resolveExternalRoot`, `RequireWorkspace`, `DetectWorkspaceRoot`,
`(Workspace) FeaturePath`, `(Workspace) ResolveFeaturePath`, `(Workspace) WorktreePath`,
`(Workspace) CheckoutStateDir`, `RequireFeaturePath`, `RequireWorktreePath`, `GuardFeatureName`.

**`internal/agent_status.go`** — `agentStatusSchema = 1` (line 19); the closed issue-code block
(lines 82–131) including `IssueSyncInProgress`, `IssueSyncStale`, `IssueSyncInvalid`,
`IssueSyncFailed`, `IssueSyncStatePresent`, `IssueSyncStateInvalid`, `IssueSyncFailedBranch`,
`IssueSyncCurrentBranch`, `IssueWorktreeDirty`, `IssueWorktreeDirtyBlocking`;
`AgentStatusFeatureSync{Kind, Stage*, Liveness*, FailureReason*, CurrentBranch*, FailedBranch*,
LockPID*, LockLive*, Pending, Completed, Skipped}`; `AgentStatusOpts{Proc ProcessProber, Tmux, Now}`;
`buildFeatureSync` (line 1354) with the checkout branch then
`statePath := SyncStatePath(featurePath)` / `if _, err := os.Stat(statePath); err != nil { return nil, nil }`
(lines 1408–1410) — **this is the exact early return §11.1 must precede**;
`attributeSyncBranch` (line 1324); `syncWantsBranch` (line 1629).

**`internal/checkout_health.go`** — `ProcessChecker`, `ProcessProber`, `proberAsChecker`,
`realProcessChecker`, `NewProcessProber`, `buildOneSyncReport`. This is the **already-injected**
liveness seam status uses (`proberAsChecker{b.opts.Proc}` at line 1363) — the seam **P5** settles on:
`buildFeatureSync` forwards `proberAsChecker{b.opts.Proc}.Alive` as `SyncClassifyOpts.Alive`.

**`internal/cli/push.go`** — `pushCmd`, `pushFeature` (pushes `entry.Name`, the C2/M14 defect, via
`internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.Name)`, and
resolves candidate A internally through `internal.RequireFeaturePath` at line 36 and
`internal.RequireWorktreePath` at line 47). Those two calls carry **two behaviours the layout
resolver does not**: `RequireFeaturePath` is this command's only `GuardFeatureName` call, and
`RequireWorktreePath` is what makes checkout-mode `tws push` fail with `ErrWorktreeUnsupported`
(`internal/resolve.go:294-334`). P1 therefore replaces them **only on the external arm**: `pushCmd`
guards explicitly and branches on `ws.Mode`, and the checkout arm keeps today's body verbatim as
`pushFeatureCheckout` (spec §3.11, AC 59).

**`internal/cli/importcmd.go`** — `isRuntimeState` (line 174).

**`internal/cli/new.go`** — `sameStackRepo` (line 336). **Exactly four call sites** exist, all in
package `cli`: `internal/cli/sync.go:155`, `internal/cli/sync_helpers.go:161`,
`internal/cli/sync_helpers.go:180`, `internal/cli/new.go:325`. No test references it. The move to
`internal.SameStackRepo` + one-line delegation is a four-line diff.

### 3.2 New files (**NEW**)

| Path | Package | Contents (per §13.2) | Notes from the tree |
|---|---|---|---|
| `internal/sync_selection.go` **NEW** | `internal` | `SyncFetchPolicy`, `SyncPropagationPolicy`, `SyncScopeKind`, `SyncRunPolicy`, `SyncSelectionRole`, `SyncSelectedEntry`, `SyncSelection`, `SyncSelectionOpts`, `ResolveSyncSelection`, `SameStackRepo` | pure; imports only `fmt` (+ nothing else). No Git, no `os`. Unit-testable with no repo (AC 50) |
| `internal/sync_run_state.go` **NEW** | `internal` | `SyncRunStateVersion`, `CheckoutTransactionVersion`, `SyncRunStage`, `SyncRunState`, `SyncRunGuard`, `SyncStateCell`, `SyncExternalState`, `SyncClassifyOpts`, `ClassifyExternalSyncState`, `SyncRunStatePath`, `SyncRunGuardPath`, `LoadSyncRunState`, `SaveSyncRunState`, `DeleteSyncRunState`, `HasSyncRunState`, `ClaimSyncRunGuard`, `ReclaimSyncRunGuard`, `ReadSyncRunGuard`, `ReleaseSyncRunGuard`, `isSyncMarker`, `SyncStepHook`, `syncProcessAlive` | can reuse the unexported `atomicWriteFile`, `writeLockExclusive` idiom, `removeLockIfUnchanged` and `isProcessAlive` because they are in the **same package** |
| `internal/cli/sync_modes.go` **NEW** | `cli` | `resolveSyncPolicy`, `syncEntryCompletion`, `externalSyncLayout` + `resolveExternalSyncLayout(ws, twsRoot, feature)` (spec §3.11; Git-free, no `TwsRoot`/`FeaturePath`/`WorktreePath`/`RequireWorkspace` inside), `newSyncMarker`, `var syncMarkerFn = newSyncMarker`, `classifySyncState`, `saveScopedSyncFailure`, the single `errSyncModeFlagsNeedV2` I20 constant, the §8.7 message table | package `cli` cannot reach `atomicWriteFile`/`isSyncMarker`; every write goes through exported `internal` helpers, and classification comes back as `SyncExternalState.Cell` plus the recorded symlink facts. The layout resolver needs only `internal.FeaturePath`, `ws.ResolveFeaturePath`, `internal.LoadStack`, and `filepath` — all already imported or stdlib |
| `internal/cli/sync_golden_test.go` **NEW** | `cli` (test) | §17.1 harness | must be authored and captured **before** any production edit |
| `internal/cli/testdata/sync_noflag/**` **NEW** | testdata | frozen goldens + `declared_c1/`, `declared_c2/`, `declared_c3/`, `declared_c4/` | sibling of the existing `internal/cli/testdata/existing_commands/**`, which MUST NOT be re-baselined |

### 3.3 Changed files — smallest coherent edit per file

| File | Edit |
|---|---|
| `internal/cli/sync.go` | six `cmd.Flags()` lines appended after the existing five (line 80) and two `RegisterFlagCompletionFunc` lines; `RunE` gains the §3.6 order **including the step-6 layout resolution**; `handleSyncAbort`/`handleSyncContinue`/`branchContainsConfiguredParent` take the layout and gain cell dispatch; `syncFeature` gains `layout`/policy/selection parameters and loses its `internal.FeaturePath` call; `fetchQuiet` byte-identical |
| `internal/cli/sync_helpers.go` | `syncWithStackFiltered` gains `layout` + `sel` parameters and four guarded behaviours (membership skip, anchor skip, scoped rebase argv, no `markUpdatedAncestors`), deriving worktrees from `layout.WorktreesRoot`; `staleStackEdges` → `staleStackEdgesFiltered(worktreesRoot, stack, selected)` + the `nil` call site; `resolveBase(base, repoCtx)`; `resolveEntryBase` passes the repo context; `saveIncompleteSync` keeps its signature and is simply handed `layout.FeaturePath` |
| `internal/syncstate.go` | `SaveSyncState` body → `atomicWriteFile(SyncStatePath(featurePath), data, 0644)` (one line) |
| `internal/checkout_sync.go` | `CheckoutPlanEntry.Name` (first field); `CheckoutTransaction.StateVersion` + five policy fields; `CheckoutSyncOpts` +4 fields; `BuildCheckoutPlan` +`sel` param; `RunCheckoutSync` steps 6–8/10–12b/14; `ContinueCheckoutSync` version + symmetric push; `finalizeTransaction` `Name`-keyed; `AbortCheckoutSync` deferred I7; **new** `printSyncModeHeader`, `printLocalOnlyNoOp` |
| `internal/cli/checkout_sync.go` | positional args → `(ws internal.Workspace, opts internal.CheckoutSyncOpts)`; `internal.RequireFeaturePath(feature)` **kept verbatim as the first statement** (its second `RequireWorkspace` event, guard, layout resolution, and error semantics are all preserved — **not** replaced by `internal.ResolveFeaturePathFor`, spec §10.1), so the checkout resolution prefix in the argv log is unchanged and the C4 probe keeps its pinned position right after it; `ws` is used only for `RepoDir = ws.RepoRoot` + I19 probe; I20 rule 0 via the shared constant; pass `Continue` |
| `internal/cli/push.go` | `entry.Name` → `entry.GitBranch()` in the `push` argv (one token) on the **external** helper only; `pushFeature` takes the resolved layout instead of `RequireFeaturePath`/`RequireWorktreePath`; `pushCmd` gains `RequireWorkspace` + `GuardFeatureName(ws.MetadataRoot, feature)` **before** the resolver and then a `ws.Mode` branch; **new** `pushFeatureCheckout` holding today's body verbatim for checkout mode; **new** `pushSelected` |
| `internal/cli/new.go` | `sameStackRepo` becomes `return internal.SameStackRepo(a, b)` |
| `internal/agent_status.go` | `buildFeatureSync`: classifier call (`AlwaysReadGuard: true`, `Alive: proberAsChecker{b.opts.Proc}.Alive`) + cell dispatch inserted **before** line 1408; its own feature-path resolution is untouched — status is deliberately not re-rooted by the P1 resolver (spec §3.11, §18 item 10) |
| `internal/cli/importcmd.go` | two exact names added to `isRuntimeState` |

**Nine production files change**, not eleven — see **P4** (now corrected in spec §13.6).

---

## 4. Cobra / control-flow map (task item 1)

### 4.1 Flag registration and insertion point

Exact insertion point: `internal/cli/sync.go`, immediately after
`cmd.Flags().StringVar(&testCmd, "test", …)` and before `return cmd`. Six new
`cmd.Flags().BoolVar/StringVar` lines in the §3.1 order, then:

```go
_ = cmd.RegisterFlagCompletionFunc("only", syncEntryCompletion)
_ = cmd.RegisterFlagCompletionFunc("from", syncEntryCompletion)
```

Precedent for `RegisterFlagCompletionFunc` in this tree: `internal/cli/enable.go` (`mode`) and
`internal/cli/space.go` (`feature`, `kind`, twice). The `_ =` discard is the house style.

**Help ordering verified empirically.** `go run ./cmd/tws sync --help` today prints
`--abort, --continue, -h/--help, --push, --test, -v/--verbose` — alphabetical, although registration
order is `verbose, push, continue, abort, test`. §3.1/§3.9's claim that `SortFlags` stays `true` and
help is alphabetical is therefore **confirmed against the running binary**, and AC 3's snapshot must
be alphabetical. `grep -rn "SortFlags" internal/cli` returns nothing today (AC 51's grep gate is
already satisfied and must stay so).

### 4.2 Presence map and validation ordering

`resolveSyncPolicy(cmd) (internal.SyncRunPolicy, bool /*newMode*/, map[string]bool /*changed*/, error)`
lives in `internal/cli/sync_modes.go` **NEW** and is called from `RunE` **before** the mode dispatch
at `if ws.Mode == internal.ModeCheckout` (line 33), so both modes reject identically (§3.6 step 3).
The `changed` map keys are exactly `fetch, no-fetch, full, local-only, only, from, push`.

Ordering inside `RunE`, mapped onto today's statements:

| §3.6 step | Today's code | Change |
|---|---|---|
| 1–2 | `RequireTool`, `RequireWorkspace` (lines 28–32) | unchanged |
| 3 | — | **insert** `resolveSyncPolicy` (I1–I6, I7-with-trigger, I8) |
| 4 | `if ws.Mode == ModeCheckout { return runCheckoutSync(...) }` (line 33) | pass **`ws`** plus the options struct, so the checkout wrapper stops re-resolving the workspace (spec §10.1) |
| 5–6 | `GuardFeatureName` + `ws.ResolveFeaturePath` (lines 42–48) | **hoist** the existing `internal.TwsRoot()` call into a local `twsRoot` (same call site, same value), pass it to the otherwise unchanged `internal.GuardFeatureName(twsRoot, feature)`, keep `ws.ResolveFeaturePath` unchanged, then **insert** `resolveExternalSyncLayout(ws, twsRoot, feature)` (spec §3.11, P1/P15) — the run's single root **and** its last resolution event from here on |
| 7–8 | `if abort {…} if cont {…} if HasSyncState {…}` (lines 49–59) | **replace** with `classifySyncState(layout.FeaturePath, newMode)` + cell dispatch; I18 comes from the returned symlink facts, not a second `Lstat` |
| 9 | | I20 before the cells 1/7 `--continue` arms |
| 10–12 | | `LoadStack` → `ResolveSyncSelection` → `no-fetch` preflight → marker + I17 |
| 13–14 | | guard claim, sentinel, payload |
| 15 | | header |
| 16–17 | `syncFeature(feature, verbose)` (line 60) | selection-aware |

**Deferred I7 (`cont && abort`, no trigger flag)** has exactly one external home: inside
`classifySyncState`, after the legacy decode and payload `Lstat` (step 8e). Today's precedence
(`abort` wins, `cont` ignored) is the fall-through and is produced by the existing statement order
at lines 49–55 — keep those two `if` statements adjacent so the frozen order is visually obvious.
**Deferred I20** for checkout lives in `internal/cli/checkout_sync.go` `runCheckoutSync`, which must
load the transaction itself (`internal.LoadCheckoutTransaction`) before calling
`internal.ContinueCheckoutSync`; that is an extra read only on the `--continue`-with-trigger path.

### 4.3 Writers and process streams

Every sync byte comes from bare `fmt.Print*`/`fmt.Printf`/`fmt.Println` in package `cli` plus Git
children wired to the process streams:

- `internal.Run`/`RunDir` set `cmd.Stdout = os.Stdout`, `cmd.Stderr = os.Stderr`, `cmd.Stdin = os.Stdin`;
- `internal.RunDirClean` → `runWithFilteredStderr` sets `cmd.Stdout = os.Stdout` and pipes stderr
  through the filter (`hint:` / `Disable this message` dropped; `skipped previously applied commit`
  → `    (skipped duplicate commit)`; everything else `fmt.Fprintln(os.Stderr, line)`);
- `internal.RunSilent`/`RunSilentDir` wire **nothing** — this is the non-verbose `fetch` path, so
  §17.1's fetch diversion is inert exactly as the spec states.

Because these read `os.Stdout`/`os.Stderr` **at call time**, the `os.Pipe` swap used by the shipped
`captureStdout` (`internal/cli/space_guard_test.go`) captures child output too. The new harness needs
the same trick for **both** streams (today only stdout is captured anywhere in the tree).

`errors` reach the user through `cli.Execute()`'s `fmt.Fprintln(os.Stderr, err)` + exit 1
(`internal/cli/root.go`), so every `RunE` error string is a stderr line.

### 4.4 Header and no-op print ownership

- External header: printed by `RunE` between §3.6 steps 14 and 16 — i.e. between the payload write
  and `syncFeature`. It must **not** move into `syncFeature`, which is also reached by
  `--continue`.
- External `local-only` no-op block: printed by package `cli` (inside the scoped executor, from
  `syncWithStackFiltered`'s anchor arm), reusing `formatSyncStatus(name, mode, "skipped")` for the
  `[-]` symbol — note `formatSyncStatus` renders `  [-] NAME (mode)`, while §3.7 requires
  `  [-] NAME (no in-stack parent edge to propagate)`. The parenthetical is a **mode-slot**
  substitution: `formatSyncStatus(entry.Name, "no in-stack parent edge to propagate", "skipped")`
  produces the exact required bytes with no new formatter. This is the cheapest correct reuse, and
  spec §3.7/§3.10 now mandates it (P12 settled); `printLocalOnlyNoOp` in package `internal` repeats
  the same literal because it cannot call the package-`cli` helper, and the two must stay
  byte-identical. The block's line order is `TopoSort`'s and is asserted as a set, never as a
  sequence (P2).
- Checkout header and no-op block: `printSyncModeHeader` and `printLocalOnlyNoOp` in
  `internal/checkout_sync.go` (**the first two `fmt.Print*` in that file**), plus the inline
  `Fetching default repo... ` + `done`/`failed` line. `internal/cli/checkout_sync.go` keeps
  `Checkout sync complete.` / `Checkout sync completed.` / `Checkout sync aborted, original branch
  restored.` and prints nothing new.

---

## 5. Shared selection model (task item 2)

### 5.1 Mapping `SyncSelectedEntry` back to a real `StackEntry`

`SyncSelectedEntry` carries `Name, GitBranch, Repo, Base, Role, ParentName, Archived` — it
deliberately **omits `LastBaseSHA`**, which both executors need for the amend-aware `--onto` replay.
Consequently:

- **External**: keep iterating `sorted []internal.StackEntry` (the existing loop variable) and add
  one guard `if sel != nil && !sel.Names[entry.Name] { continue }`. Role is looked up by
  `sel.Role(entry.Name)` (a tiny accessor over `Entries`, or a `map[string]SyncSelectionRole` built
  once). This keeps `entry.LastBaseSHA`, `entry.Repo` and `entry.GitBranch()` exactly as today and
  makes the scoped diff four guards instead of a loop rewrite.
- **Checkout**: `BuildCheckoutPlan` already iterates `TopoSort(stack)` over real `StackEntry`
  values; the same membership guard applies, plus the anchor exclusion under `local-only` and
  `Base = parent.GitBranch()` rewriting for propagation edges.
- **Fetch boundary (§6.5)**: build `sub := internal.Stack{Branches: <the filtered real entries>}`
  from the same loop, then `internal.UniqueRepos(sub, layout.FeaturePath)`. No new walker.

Recording this explicitly is required because a naive implementation that rebuilds `StackEntry`
values from `SyncSelectedEntry` would silently drop `LastBaseSHA` and turn every scoped rebase into
a plain rebase — a silent amend-aware regression (`amend-aware-rebase` is a hard parent). **P11 is
settled**: spec §5.5 now carries this as a binding "membership and role only" rule, and §6.5 says
the subset is built from real entries.

The `Entries` slice is in `TopoSort` order and nothing more: parent before child, siblings
unspecified (**P2**). Neither executor may rely on a sibling sequence, and no tie-break is added.

### 5.2 Rule ownership, verified against existing predicates

| Rule | Existing truth it reuses |
|---|---|
| membership `all` | `TopoSort` |
| membership `one` | `HasBranch` + `GetBranch` |
| membership `subtree` | `Descendants` ∪ `{Selector}` |
| I11 archived | `GetBranch(stack, sel).Archived` |
| anchor vs propagated | the exact predicate in `resolveEntryBase`: `parent.Name != "" && sameStackRepo(parent.Repo, entry.Repo)` |
| I12 cross-repo | `!SameStackRepo(entry.Repo, "")` |
| I13 duplicate branch | `entry.GitBranch()` |

`SameStackRepo` is a two-line predicate; moving it to `internal/sync_selection.go` and delegating
from `internal/cli/new.go` keeps one definition and does not disturb `internal/cli/new.go:325`.

### 5.3 Package visibility

`ResolveSyncSelection` and all its types are **exported** because `internal/cli` calls them.
`isSyncMarker` and `syncProcessAlive` stay **unexported** in `internal` (single-package callers:
`ClassifyExternalSyncState`). `newSyncMarker` / `syncMarkerFn` stay unexported in `cli`. Nothing in
this design requires a new exported mutable global.

### 5.4 Pure unit-test seam

`ResolveSyncSelection(stack, policy, opts)` performs no I/O, so AC 50's tests are plain table tests
in a **new** `internal/sync_selection_test.go` **NEW** (package `internal`) with literal `Stack`
values — no `t.TempDir()`, no Git, no `t.Setenv`. This is the only genuinely fast test in the
feature; put every message-interpolation assertion there.

---

## 6. External execution map (task item 3)

| Concern | Symbol | Exact change |
|---|---|---|
| Layout | every helper below | takes `layout.FeaturePath` / `layout.WorktreesRoot` explicitly (spec §3.11), so the run's `3 + N + E` workspace-root resolution events collapse to **two** (P15); **no** `internal.FeaturePath`, `internal.WorktreePath`, `internal.RequireFeaturePath`, or `internal.RequireWorktreePath` call survives in `sync.go` or `sync_helpers.go`, and none survives in `push.go` **outside** the preserved checkout helper `pushFeatureCheckout` (one `RequireFeaturePath` + one `RequireWorktreePath`, never reached in external mode — AC 51's declared carve-out) |
| Selection filter | `syncWithStackFiltered` | one `continue` guard per pass, keyed on `sel.Names[entry.Name]`, over the **real** `StackEntry` values still being iterated (P11: never rebuild entries from `SyncSelectedEntry`, or `LastBaseSHA` is lost) |
| Anchor skip | pass 1 and pass 2 | under `local-only`, `Role == SyncRoleAnchor` ⇒ print the `[-]` line and `continue` (both passes, §7.2 item 3) |
| Rebase argv | pass 1 | `--update-refs` present iff **not** scoped: `scoped := newMode && policy.ScopeKind != SyncScopeAll` |
| Ancestor marking | `markUpdatedAncestors` | not called when `scoped` (leaves `updatedByRef` empty so pass 2 really rebases) |
| `LastBaseSHA` | `UpdateBaseSHA` + `SaveStack` | unchanged mechanism; only selected entries are reached |
| Completion gate | `staleStackEdges` → `staleStackEdgesFiltered(worktreesRoot string, stack, selected map[string]bool)` | the `feature` parameter disappears (it only fed `internal.WorktreePath`); add `if selected != nil && !selected[child.Name] { continue }` as the **first** statement of the loop body; keep the message `fmt.Sprintf("%s does not contain parent %s", child.Name, parent.Name)` byte-identical; the informational block reuses the same function with the complement set |
| Failure persistence | `saveIncompleteSync` | untouched, and **not** called on new-mode runs; new-mode failures go to `saveScopedSyncFailure` (package `cli`) which calls `internal.SaveSyncRunState` |
| Base resolution | `resolveEntryBase(stack, entry)` / `resolveBase(base)` | `resolveBase(base, repoCtx)`; `repoCtx = entry.Repo`, else the materialized worktree path when `os.Stat` succeeds, else `""` |
| Validation | `runValidation(worktreePath, branchName)` | unchanged; new-mode runs persist the resolved `cfg.TestCommand` string in the payload and pass it in on `--continue` |
| Fetch | `fetchQuiet(repo, wtPath, verbose)` | byte-identical; called over `UniqueRepos(sub, layout.FeaturePath)` |
| Push (external) | `pushFeature` | `entry.Name` → `entry.GitBranch()` in the argv (C2) **and** the layout threaded in from the caller (spec §3.11); `pushCmd`'s external arm resolves the same layout, so `tws push` and `tws sync --push` cannot diverge; `pushSelected` is a new sibling that filters by name set and reuses the same repo-context rule (`entry.Repo` when set, else the `layout.WorktreesRoot`-derived worktree path); dropping the per-entry `RequireWorktreePath` takes the helper's workspace resolutions from `1 + N` to **zero** (exactly one per command invocation overall), a read-only removal of `N` `rev-parse --git-common-dir` records (spec §4.1 rule 6c, AC 59) |
| Push (command entry) | `pushCmd` | `RequireTool` → `RequireWorkspace` → `GuardFeatureName(ws.MetadataRoot, feature)` → `ws.Mode` branch → (external arm only) one `internal.TwsRoot()` call → `resolveExternalSyncLayout(ws, twsRoot, feature)` (spec §3.11 binding order); the checkout arm calls `internal.TwsRoot()` **never**; the guard moves out of `RequireFeaturePath` and **into the command**, which is what keeps `TestSpaceGuard_ExternalCommandMatrix/push` green without editing `space_guard_test.go`; this `RequireWorkspace` is the external path's **one and only** workspace resolution |
| Push (checkout) | `pushFeatureCheckout` **NEW** | today's `pushFeature` body verbatim — `RequireFeaturePath`, `RequireWorktreePath`, `entry.Name` argv — so checkout push keeps failing with `ErrWorktreeUnsupported` and a nonzero exit instead of silently skipping every branch and exiting 0 (AC 59); it is the **only** exception to the "no legacy resolver in `push.go`" grep of AC 51 |

**C2/C4 and `RunDirClean` coverage.** `internal.RunDirClean` is used by exactly three production
call sites: `internal/cli/sync_helpers.go:54` (pass-1 rebase), `internal/cli/sync_helpers.go:232`
(`syncFallback`) and `internal/cli/push.go` (push). Nothing in the tree tests
`runWithFilteredStderr` today — verified by grep — so AC 2's "direct regression assertion outside the
wrapper" is genuinely **new**, and its natural home is a **NEW** `internal/exec_clean_test.go`
(package `internal`, so it can drive a plain child command with `os.Stderr` swapped). Two behaviours
of the current filter must be pinned exactly as they are, not as one might wish them: blank lines
are dropped (`trimmed == "" → continue`), and non-matching lines are forwarded **untrimmed** via
`fmt.Fprintln(os.Stderr, line)`.

---

## 7. External v2 state machine map (task item 4)

### 7.1 Paths, writers, and reusable helpers

| Artifact | Path | Writer | Mode |
|---|---|---|---|
| legacy / sentinel | `internal.SyncStatePath(featurePath)` = `<featurePath>/.sync-state.yaml` | `internal.SaveSyncState` (becomes `atomicWriteFile(…, 0644)`) | `0644` |
| payload | `SyncRunStatePath(featurePath)` **NEW** = `<featurePath>/.sync-state.v2.yaml` | `SaveSyncRunState` → `atomicWriteFile(…, 0600)` | `0600` |
| guard | `SyncRunGuardPath(featurePath)` **NEW** = `<featurePath>/.sync-run.lock` | `ClaimSyncRunGuard` → the `writeLockExclusive` idiom (`O_WRONLY|O_CREATE|O_EXCL`, `0600`) | `0600` |

All three are `filepath.Join(featurePath, <const>)` — no user input in the path.

**Reusable, same-package (no export needed):** `atomicWriteFile`, `writeLockExclusive` (copy its
five-line shape; do **not** call it, because it hard-codes `LockInfo` and the checkout lock path),
`removeLockIfUnchanged` (reusable verbatim — it takes a path and expected bytes),
`isProcessAlive`, and the `AcquireCheckoutLock`/`forceAcquireCheckoutLock` decision trees as the
model for §8.3 rules 2–4.

`atomicWriteFile`'s `os.MkdirAll(dir, 0700)` is a no-op on an existing directory, so writing the
sentinel through it does **not** re-chmod the feature directory — §8.1's requirement is satisfied by
construction. Its `os.CreateTemp(dir, ".tws-state-*")` does, however, place a transient file **inside
the feature directory** for `.sync-state.yaml` (see **P7**).

### 7.2 Marker split

- Generation (`newSyncMarker`, `syncMarkerFn`) — package `cli`, `internal/cli/sync_modes.go` **NEW**.
  Needs `crypto/rand` + `encoding/hex` (stdlib; no `go.mod` change).
- Recognition (`isSyncMarker`) — package `internal`, `internal/sync_run_state.go` **NEW**, one
  caller.
- **Verified with real Git 2.55.0** in a scratch repository:
  `git check-ref-format --branch tws-scoped-sync-<32hex>.lock` → exit **128**;
  `git check-ref-format refs/heads/<marker>` → exit 1;
  `git branch <marker>` → `fatal: '…' is not a valid branch name`, exit 128.
  §8.2 property 2 holds. (AC 29 must assert *non-zero*, not `== 1` — see **P6**.)

### 7.3 The 12 cells → code

Classification inputs come from three reads that already exist in shape — **one `os.Lstat` per
path, never two** (spec §3.6 step 8, §11.1):

```
legacy   : os.Lstat (→ LegacySymlink)  + os.ReadFile + yaml.Unmarshal   (LoadSyncState today; the
                                                       link is still FOLLOWED, frozen behaviour)
payload  : os.Lstat (→ PayloadSymlink) + os.ReadFile + yaml.Unmarshal   (NEW; a symlink is never
                                                       opened — Payload stays nil, PayloadErr set)
guard    : os.Lstat (→ GuardSymlink)   + os.ReadFile + yaml.Unmarshal   (NEW, gated by
                                                       SyncClassifyOpts.AlwaysReadGuard; a symlink
                                                       is never opened and never authoritative)
```

`SyncExternalState` therefore carries `LegacyPath`/`PayloadPath`/`GuardPath` and
`LegacySymlink`/`PayloadSymlink`/`GuardSymlink` as **facts**; package `cli` turns them into the I18
refusal and MUST NOT `Lstat` again. Liveness comes from `SyncClassifyOpts.Alive`
(nil ⇒ package `syncProcessAlive`), which `tws status` fills from its existing
`AgentStatusOpts.Proc` prober — the P5 resolution.

| Cell | legacy | payload | Owner of the message | Mutating verb reachable |
|---|---|---|---|---|
| 1 | absent | absent | today's code path (frozen) + I20 | plain run |
| 2 | absent | valid | `cli` §8.7 row 2 (+ live-guard row) | `--abort` §9.2 recovery, stale/absent guard only |
| 3 | absent | unreadable | `cli` unreadable-payload row | none |
| 4 | sentinel | absent | `cli` §8.7 row 4 | `--abort` deletes sentinel + guard |
| 5 | sentinel | valid | `cli` §8.7 row 5 | `--continue` resumes; `--abort` tears down payload→sentinel→guard |
| 6 | sentinel | unreadable | unreadable-payload row | none |
| 7 | real legacy | absent | today's strings (frozen) + I20 | today's plain/continue/abort |
| 8 | real legacy | valid | mixed-state rows | none |
| 9 | real legacy | unreadable | unreadable-payload row | none |
| 10 | unreadable | absent | cell-10 rows (C1) | none — deletes nothing |
| 11 | unreadable | valid | dedicated cell-11 rows | none |
| 12 | unreadable | unreadable | unreadable-payload row | none |

Guard precedence is applied by `cli` on `SyncExternalState.GuardLive`, over cells 2/4/5 only.

**State/message dispatch owners, binding for the implementer:**
`internal.ClassifyExternalSyncState` owns *cell computation, decoding, and the recorded symlink
facts* and nothing else (no symlink **policy**, no messages, no mutation).
`internal/cli/sync_modes.go` owns *the I18 refusal built from those facts, deferred I7, I20, the
§8.7 message table, and every mutation*. `internal/agent_status.go`
owns *projection only* and never mutates (AC 44).

### 7.4 Setup/teardown insertion points

Setup lands between §3.6 steps 12 and 15 in `RunE`; teardown lands in three places:
`syncWithStackFiltered`'s success exit (where `internal.DeleteSyncState` is called today, at
`internal/cli/sync_helpers.go:124`), the `--abort` handler, and the `--continue` success path. Since
`syncWithStackFiltered` currently calls `internal.DeleteSyncState(featurePath)` directly, the
cleanest seam is to make teardown a single package-`cli` helper
(`clearSyncRunState(featurePath, newMode)`) called from all three, keeping the no-flag branch as the
literal `internal.DeleteSyncState` call. `featurePath` is `layout.FeaturePath` in all three places
(P1), so setup and teardown can never target different roots.

`SyncStepHook` call sites (six, §17.3): after guard claim, after sentinel write, after payload
write, inside the rebase loop, after payload delete, after sentinel delete. Five of the six are in
package `cli`; the hook variable lives in `internal`, so `cli` calls
`if internal.SyncStepHook != nil { … }` — the same shape `internal/cli/checkout_sync_test.go` uses
for `internal.StepHook`.

### 7.5 Downgrade harness

- **Path 1** `TWS_DOWNGRADE_BINARY`: nothing in the tree provides it; treat as optional.
- **Path 2** offline tag build: **feasible, verified**. `git show v1.2.14:go.mod` is byte-identical
  to HEAD's, and `$(go env GOMODCACHE)` already contains `cobra@v1.10.2`, `pflag@v1.0.9`,
  `yaml.v3@v3.0.1`. `GOFLAGS=-mod=mod GOPROXY=off go build ./cmd/tws` in a detached worktree at the
  tag will resolve entirely from cache.
- **Path 3** replay harness: `git show v1.2.14:internal/cli/sync.go` confirms the three legacy paths
  are **identical to HEAD's** (same `HasSyncState` + `state.FailedBranch` interpolation, same
  `handleSyncAbort`, same `handleSyncContinue`). The harness is therefore a transcription of code
  that has not drifted — and the fidelity test can compare against a binary built from the tag.

---

## 8. Checkout transaction map (task item 5)

### 8.1 Signature changes and their blast radius

| Symbol | Old | New | Callers affected |
|---|---|---|---|
| `cli.runCheckoutSync` | `(feature string, push bool, testCmd string, cont, abort, verbose bool) error` | `(ws internal.Workspace, opts internal.CheckoutSyncOpts) error` — `ws` supplies `RepoRoot` and the mode decision only; the existing `internal.RequireFeaturePath(feature)` call **stays exactly as it is** (second `RequireWorkspace` event, guard, layout resolution, errors all preserved; `internal.ResolveFeaturePathFor` is **not** used, spec §10.1/§13.4 rule 1), and the C4 probe is added directly after it | one production caller: `internal/cli/sync.go:34`. **No test calls it** (verified) |
| `internal.BuildCheckoutPlan` | `(repoDir string, stack Stack) ([]CheckoutPlanEntry, error)` | `+ sel SyncSelection` | one production caller (`RunCheckoutSync`). **No test calls it** (verified: tests call `internal.RunCheckoutSync`) |
| `internal.CheckoutSyncOpts` | 6 fields | +`Policy`, `NewMode`, `Continue`, `Changed` | **23** keyed test constructions in `internal/cli/checkout_sync_test.go` ⇒ additive fields compile unchanged |
| `internal.CheckoutPlanEntry` | 6 fields | +`Name` first | YAML key order changes for new transactions only (C5) |
| `internal.CheckoutTransaction` | 14 fields | +`StateVersion` first, +5 policy fields | all `omitempty` ⇒ legacy transactions round-trip without gaining keys |

This is the decisive compatibility fact for the implementer: **every existing checkout test
constructs `CheckoutSyncOpts` with field names**, so the struct extension is source-compatible and
the 26 checkout tests need no edit.

### 8.2 Preflight, lock, fetch, plan

Insertion is surgical: today's steps 1–5 stay verbatim; the new-mode block (I9 `LoadStack`,
`ResolveSyncSelection`, `no-fetch` `VerifyGitRef` loop) is inserted between step 5 and
`AcquireCheckoutLock`; the header and the optional single `git fetch` go between the lock and
`BuildCheckoutPlan`; `printLocalOnlyNoOp` goes immediately after `BuildCheckoutPlan` returns; the
five policy fields join the existing `tx := &CheckoutTransaction{…}` literal.

The no-flag path must keep loading the stack **after** the lock with today's `load stack: %w`
wrapper — i.e. the new-mode preflight does not replace the existing `LoadStack` call, it precedes
it and short-circuits it (`if stack == nil { stack, err = LoadStack(...) }`), otherwise AC 55's
companion case (no-flag broken `stack.yaml` still acquires the lock first) fails.

**Containment probe.** `internal.GitRepoRootIn(cwd)` already exists and issues exactly
`git -C <cwd> rev-parse --show-toplevel`. The probe belongs in `cli.runCheckoutSync` after
`internal.RequireFeaturePath` — which **stays** (spec §10.1): the wrapper keeps its own
`RequireWorkspace` event, so the pre-change resolution prefix is untouched and the probe is a pure
addition — so the argv-log position §17.1 mode 3 demands
(directly before `rev-parse --git-path rebase-merge`) is produced naturally.

**C3 + C5 attribution.** `finalizeTransaction`'s loop becomes: match `stack.Branches[i].Name ==
pe.Name` when `pe.Name != ""`, else today's `GitBranch()` first match. `BuildCheckoutPlan` fills
`Name: entry.Name` on every run.

**I9–I14, I19, I20, I7 owners:** I9/I14 in `internal.RunCheckoutSync`; I10–I13 in
`internal.ResolveSyncSelection`; I19 in `cli.runCheckoutSync`; I20 in `cli.runCheckoutSync`;
deferred I7 in `internal.AbortCheckoutSync` (gated on `tx.StateVersion >= 2`).

**Old-transaction defaults:** absent `state_version` ⇒ 1 ⇒ `no-fetch × full × all`, legacy one-way
push rule, `GitBranch()` attribution fallback. Nothing is migrated or rewritten.

**Crash/lock semantics unchanged:** `AcquireCheckoutLock` still refuses a live lock and refuses a
stale lock that still has a transaction; `forceAcquireCheckoutLock` still refuses a live foreign
PID. The pre-transaction fetch window deliberately has no stage, so `resumeTransaction`'s switch is
untouched.

---

## 9. Status and import map (task item 6)

### 9.1 `buildFeatureSync` — exact integration point

```go
// internal/agent_status.go, buildFeatureSync
if b.ws.Mode == ModeCheckout { … return view, nil }        // line 1355-1407, untouched

statePath := SyncStatePath(featurePath)                    // line 1408  ← INSERT BEFORE THIS
if _, err := os.Stat(statePath); err != nil {              // line 1409
    return nil, nil                                        // line 1410  ← the blind spot
}
state, loadErr := LoadSyncState(featurePath)               // line 1412
if loadErr != nil || state == nil { … IssueSyncStateInvalid … }   // cell 10, keep verbatim
view := &AgentStatusFeatureSync{Kind: "external", Pending…, Completed…, Skipped…}
if state.FailedBranch != "" { view.FailedBranch = strPtr(...) } else { IssueSyncStatePresent }
return view, state
```

The classifier call goes immediately above `statePath := …`. Cell 7 must **delegate to the block
below it unchanged** so `internal/cli/testdata/existing_commands/external-*/status.{txt,json}` and
the `internal/agent_status_test.go` expectations keep passing. Cell 10 must route into the existing
`IssueSyncStateInvalid` branch (the same branch three shipped tests already assert:
`internal/agent_status_test.go` around lines 315, 1540, 2148).

### 9.2 Fields, projections, and downstream consumers

`AgentStatusFeatureSync` already has `Liveness`, `LockPID`, `LockLive`, `Stage`, `FailureReason`,
`CurrentBranch`, `FailedBranch`, `Pending`, `Completed`, `Skipped` — all nullable. External state
populates only `Kind`, `Pending`, `Completed`, `Skipped`, `FailedBranch` today, so §11.1's guard
liveness is a **population of existing keys**: no struct change, no `agentStatusSchema` bump.

The returned `*SyncState` is consumed by exactly two functions, both of which must keep working
unchanged on the projected value:

- `attributeSyncBranch(view, featurePath, external)` — matches `e.Name == external.FailedBranch`
  and emits `IssueSyncFailedBranch` with the hint
  `resolve the conflict in <featurePath>/worktrees/<name>, then: tws sync <feature> --continue`;
- `syncWantsBranch(external, entry.Name)` — matches `FailedBranch` or any `Pending` member and
  upgrades a dirty worktree to `IssueWorktreeDirtyBlocking`.

So the marker-aware projection must return a `*SyncState` whose `FailedBranch`/`Pending` are the
**payload's real logical names**. That single decision satisfies §11.1 rules 1–2 with zero changes to
either consumer.

### 9.3 `isRuntimeState` and its committed test

`internal/cli/importcmd.go` `isRuntimeState` is a four-branch exact/prefix predicate. The test that
pins it is `TestExport_RuntimeStateExcluded` at `internal/cli/checkout_lifecycle_test.go:617` — and,
despite the name, it is a **pure table test over `isRuntimeState`**, not a tarball test (rows include
`{".sync-state.yaml", true}` at line 625). AC 45's "gains `.sync-state.v2.yaml` and `.sync-run.lock`
(both `true`)" is therefore a two-row table edit; AC 45's *end-to-end* import/export assertions are
**new tests**, not an extension of that one.

`internal/cli/export.go`'s `exportTarball` writes only `workspace.yaml` and paths under `inject/`
(explicit `strings.HasPrefix(relPath, "inject/")` filter) — the allow-list is structural and needs
**no change**; the AC 45 insurance assertion is additive.

---

## 10. No-flag evidence harness (task item 7)

### 10.1 Capturing pre-change goldens before any production edit

The harness is a **test-only** file, so it can be authored, run, and committed against the unmodified
tree — exactly as `internal/cli/stack_status_test.go` was for `existing_commands/**` (its header
comment states this contract explicitly). Sequence: write
`internal/cli/sync_golden_test.go` **NEW** → run it with `TWS_REGEN_SYNC_GOLDENS=1` (mirroring the
shipped `goldenRegenEnv = "TWS_REGEN_EXISTING_GOLDENS"` gate) → commit
`internal/cli/testdata/sync_noflag/**` → only then touch production.

### 10.2 Helper visibility

Everything the harness needs is in **package `cli`** already:

| Helper | Location | Reuse |
|---|---|---|
| `goldenBuilder`, `newGoldenBuilder`, `env()`, `git`, `tryGit`, `goldenWrite` | `internal/cli/stack_status_test.go` | date-pinned fixtures (`goldenFixedDate = 2020-01-01T00:00:00+00:00`) and the `GIT_CONFIG_COUNT=0` neutralization |
| `goldenFixture`, `addExtra`, `goldenExternalBase`, `goldenCheckoutBase`, `goldenStackYAML` | same file | fixture shapes with a real bare remote, `remote set-head`, linked worktrees |
| `goldenReplacement`, `goldenReplacements`, `goldenApplyReplacements`, `goldenAssertNoResidual`, `goldenTempRoots`, `goldenNormalizeText` | same file | the closed §4.1 rule 1a table, longest-first, `EvalSymlinks` variants, stable-ID literal rule |
| `goldenExitCode`, `goldenPkgDir` | same file | `exit: N\n<body>` artifact shape; `goldenPkgDir` is captured at init **before** fixtures chdir — reuse it. `goldenCompareOrWrite`/`goldenGoldenPath` are **read for their shape only**: they are not called with a new root and not edited (spec §17.1); the sync harness declares sync-local equivalents |
| `captureStdout` | `internal/cli/space_guard_test.go` | `os.Pipe` swap of `os.Stdout` only |
| `snapshotTree`, `snapshotTreeIgnoringGitLocks`, `collectStableTreePaths`, `isTransientGitLockPath` | `internal/cli/space_test.go` | side-effect snapshots for AC 46/55/57 |
| `setupGitRepo`, `withWorkspaceEnv`, `writeAndCommit`, `gitRun`, `gitOutput`, `createLinearStack` | `internal/cli/new_integration_test.go`, `internal/cli/sync_continue_integration_test.go` | behavioural (non-golden) fixtures |
| `setupGitRepoCheckout`, `gitInDir`, `requireWorkspaceForTest`, `branchExistsInDir` | `internal/cli/checkout_lifecycle_test.go` | checkout fixtures |
| `setupCheckoutSyncRepo`, `setupFeaturePath`, `createStackBranch`, `saveTestStack`, `gitSHA`, `clearStepHook` | `internal/cli/checkout_sync_test.go` | checkout sync fixtures + `StepHook` reset |

**Missing and therefore new:** a two-stream process capture. `captureStdout` swaps only
`os.Stdout`. The harness needs `captureStreams(t, fn) (stdout, stderr string)` **NEW** that swaps
both with two `os.Pipe`s and two drain goroutines — the same shape, doubled. This matters because
`RunDirClean` writes to `os.Stderr` and every `RunE` error is printed to `os.Stderr` by
`cli.Execute()`; a single-stream capture would silently merge or lose them.

### 10.3 The `git` PATH wrapper

Concrete constraints from this tree:

- Production Git calls set **no** `cmd.Env` (`exec.Command("git", …)` in `internal/exec.go`,
  `internal/stack.go`, `internal/checkout_sync.go`), so they inherit the test process environment
  and the test process `PATH`. Prepending a wrapper directory with `t.Setenv("PATH", …)` around the
  measured invocation therefore works for in-process runs **and** for a built-binary subprocess.
- The wrapper must resolve the real Git **before** it is on `PATH` (`exec.LookPath("git")`) and pass
  it in an env var; §17.6 forbids re-resolution.
- Verb detection must skip `-C <path>` (used by `GetBranchSHA`, `DefaultBranchIn`, `VerifyGitRef`,
  `GitRepoRootIn`, `IsPrunableWorktree`, and several test helpers) — that is the only global option
  form production actually emits; `-c`, `--git-dir=`, `--work-tree=`, `--no-pager` do not appear in
  production argv today but the skip list is cheap insurance.
- **Three tee shapes**, identical in both modes: `rev-parse --show-toplevel` (from
  `internal.GitRepoRootIn`), `rev-parse --abbrev-ref origin/HEAD` and `symbolic-ref --short HEAD`
  (both from `internal.DefaultBranchIn`). Note `symbolic-ref --short HEAD` is **also** emitted by
  `internal.gitCurrentBranch` in checkout preflight step 4 — the tee is observationally inert, so
  this overlap is harmless, but the comparator must not assume every `symbolic-ref --short HEAD`
  record belongs to a `DefaultBranchIn` event. Anchor the event window by the record's **cwd**
  (which the sidecar already records) and by adjacency to the `rev-parse --abbrev-ref origin/HEAD`
  record.
- **Divert set** (external captures only): `rebase`, `fetch`, `push`. Verified inertness for
  `fetch`: the non-verbose path uses `internal.RunSilentDir`/`RunSilent`, which wire no streams at
  all, so diverting `fetch` changes nothing on the frozen path; only `--verbose` runs would notice.
- **Record-only for every checkout capture** is mandatory: `gitRebaseOnto`/`gitPlainRebase` classify
  `CONFLICT` / `could not apply` out of `cmd.CombinedOutput()` and persist
  `"rebase conflict: " + output` into `failure_msg`. Emptying that stream would flip
  `failure_kind: conflict` to `switch` — verified by reading `doRebase`'s error branches.

### 10.4 Comparators

- **Output**: `goldenApplyReplacements` + `goldenAssertNoResidual`, then byte compare, then the
  `exit: N` prefix. `goldenGoldenPath` and `goldenCompareOrWrite` hard-code the `existing_commands`
  segment and are **left untouched** — spec §17.1 forbids parameterizing them, because that would
  make `internal/cli/stack_status_test.go` a second changed test file. The sync harness declares its
  own sibling helpers in `internal/cli/sync_golden_test.go` (a `testdata/sync_noflag/`-rooted path
  builder plus a local compare-or-write) reusing the shipped `goldenExitCode` artifact shape and the
  `goldenPkgDir`/regen-env conventions by **calling** them.
- **State**: `compareStateSemantic` **NEW** over `yaml.Node`. Closed dynamic sets:
  `.sync-state.yaml` → `{started_at}`; `<feature>-checkout-sync.yaml` → `{started_at, lock_pid,
  lock_created}` + conditional `failure_msg`; payload → `{started_at, updated_at, marker,
  owner_token}`; guard → `{pid, created, token}`. Additive key set: `{plan[].name}`.
- **Argv**: ordered `(verb, argv, cwd, exit-class)` records with exactly three carve-out constants
  (`c4ContainmentProbe`, `c4DefaultBranchProbe`, `c4ResolutionCompression`). The third is
  **external-only** and covers **both** external paths, and its unit is the
  `workspaceRootResolutionEvent` — an ordered **pair** of records, either
  `rev-parse --show-toplevel` + `-C <cwd> rev-parse --git-common-dir` (a `RequireWorkspace` event)
  or those two reversed (a `TwsRoot` event). The comparator groups records into events **before**
  comparing, using the **anchored** rule of spec §3.11: anchor on each
  `--git-common-dir` record whose `-C` operand equals that record's own recorded process cwd (never
  a bare `rev-parse --show-toplevel`, never a foreign-path `--git-common-dir`), walk
  the anchors in **reverse log order**, pair forward (`common → show`, `TwsRoot`) when the next
  record is an unconsumed bare show in the same cwd, otherwise pair backward (`show → common`,
  `RequireWorkspace`) with the immediately preceding unconsumed bare show, else fail the capture. Every leftover bare `rev-parse --show-toplevel` is a **standalone `LoadConfig`
  probe** (`runValidation`, `internal/cli/sync_helpers.go:237-238`) and stays in the ordered
  non-event log, compared verbatim and in position — it is neither added nor removed by this
  feature. Every foreign-operand `--git-common-dir` record is an **`inferExternalRepoRoot` probe**
  (`RequireWorkspace`'s fallback arm, cwd cells 4–6): also an ordinary non-event record, compared
  verbatim and in position, `C + S + M` of them per `RequireWorkspace` call, never counted as an
  event, never order-compared in a fixture with two or more configured workspaces mapping to the
  metadata root (map order), and removed **only** together with a `RequireWorkspace` event this
  carve-out removes (push path: `1 + N` blocks → 1; sync path: one block, invariant).
  Counts: external sync `3 + N + E` → 2 (this reaches the frozen AC 1 external captures)
  and external push `1 + N` → 2, with the mutating
  `push` argv, output, exit code, and refs compared separately (spec §4.1 rule 6c / §17.1). Counting
  bare `--git-common-dir` records, anchoring the pairing on show records, or encoding `1 + N` → 1,
  is a comparator bug: the first two miss half of every removed event and mis-read the measured
  `standalone show → common → show` adjacency, and the third fails on the `N = 1` and empty-stack
  boundaries.

### 10.5 Declared-change evidence directories (**NEW**, all under `internal/cli/testdata/sync_noflag/`)

`declared_c1/` (corrupt legacy state, three verbs), `declared_c2/` (decoupled-name `--push`),
`declared_c3/` (duplicate-`GitBranch()` checkout), `declared_c4/` (**both** divergent-layout
directions — `cwd-disagree-b`, the shipped split-brain shape, and `cwd-disagree-a`, the
`syncFallback` shape — plus cwd cell 9 checkout and the multi-repo default-base half). None is a
golden; each is a before/after pair.

### 10.6 `GIT_CONFIG_COUNT` neutralization

Two levels, both required and both already modelled by `newGoldenBuilder`:
`t.Setenv("GIT_CONFIG_COUNT", "0")` at test scope (covers production Git calls, which inherit the
process env) **and** `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_COUNT=0`, `HOME=<temp>` in every
`cmd.Env` the fixture builder sets. Without the first, this machine's
`safe.bareRepository=explicit` breaks bare-remote fixtures — reproduced in §1.1.

---

## 11. Test and helper map (task item 8)

### 11.1 Existing test surfaces that must keep passing (and why they are at risk)

| Test file | What it pins | Risk from this feature |
|---|---|---|
| `internal/cli/sync_continue_integration_test.go` | `handleSyncContinue` resumes descendants; retains state on later failure | calls `handleSyncContinue("feature", featurePath, false)` **directly** at lines 34 and 64 ⇒ the P1 signature change (`(feature, layout, push)`) **does** break it. **Forced edit 2 of 3** (§14.1), and the edit is mechanical: build the layout with the same path the test already derives via `internal.FeaturePath` and pass `externalSyncLayout{FeaturePath: p, WorktreesRoot: filepath.Join(p, "worktrees")}`. Because the fixture's two derivations agree, the assertions are unchanged. Call it out in the commit message rather than discovering it at compile time |
| `internal/cli/sync_branch_identity_test.go` | `checkSyncWorktreeBranch` | none if the helper keeps its signature |
| `internal/cli/external_feature_dir_test.go` | `tws sync` and `tws sync --abort` run from `<feature>/docs/nested` (cwd cells 5–6, *agreeing* layout) | C4 rule 2 must keep this green **unchanged**: the layout resolver returns the same path there and probes nothing. It is the ready-made `cwd-agree` fixture; the divergent halves are new fixtures (`cwd-disagree-a`, `cwd-disagree-b`, AC 58) |
| `internal/cli/checkout_sync_test.go` (33 KB, **26** `func Test…` functions, counted with `grep -c '^func Test'`) | every checkout stage, lock, restoration, abort, validation, `StepHook` interruption — **and, despite the file name, one external run**: `TestCheckoutSync_ExternalSyncUnchanged` (line 1167) builds an external fixture with `setupGitRepo` + `withWorkspaceEnv` + `createWorktree` and calls `syncFeature("external-feature", false)` **directly** at line 1187 | its **23** `internal.CheckoutSyncOpts{…}` literals all use **keyed** fields ⇒ additive struct fields are safe; `internal.RunCheckoutSync`/`ContinueCheckoutSync`/`AbortCheckoutSync` signatures must not change. **7** of the 26 assign `internal.StepHook` (`TestCheckoutSync_Abort`, `_InterruptionAfterRebase`, `_InterruptionAfterSwitch`, `_InterruptionAfterValidation`, `_PersistedContext`, `_RejectPushOnContinue`, `_RestorationRetry`). The direct `syncFeature` call **does** break on the P1 signature change (`syncFeature(feature, layout, …)`): this is **forced edit 3 of 3** (§14.1), mechanical and assertion-free — the test already derives its worktrees through `internal.WorktreePath("external-feature", …)`, so the agreeing layout is `p := internal.FeaturePath("external-feature")` with `externalSyncLayout{FeaturePath: p, WorktreesRoot: filepath.Join(p, "worktrees")}`, and both `result.Complete` and the `merge-base --is-ancestor` assertion stay exactly as they are |
| `internal/cli/checkout_lifecycle_test.go` | `TestExport_RuntimeStateExcluded` table (line 617) | two additive rows |
| `internal/cli/space_guard_test.go` | `syncCmd()` refusal under sibling-space guards (lines 226, 439) **and `pushCmd()` refusal** (`TestSpaceGuard_ExternalCommandMatrix/push`, lines 270-276), each asserting the `top-level directory of registered space` error, a nonzero exit, an unchanged `snapshotTreeIgnoringLock`, and an untouched space directory | `syncCmd`'s `RunE` reordering must keep `GuardFeatureName(internal.TwsRoot(), …)` where it is. **`pushCmd` is the real hazard**: its guard arrives today only through `internal.RequireFeaturePath`, which P1 removes from the external push path, so the command must call `internal.RequireWorkspace()` + `internal.GuardFeatureName(ws.MetadataRoot, feature)` itself, before `resolveExternalSyncLayout` (spec §3.11, AC 59). This file MUST NOT be edited — it is the regression detector for exactly that mistake |
| `internal/cli/stack_status_test.go` + `internal/cli/testdata/existing_commands/**` | `status`/`doctor`/`list` goldens for 24 fixtures, including `external-duplicate-branch` and `checkout-duplicate-branch` | MUST NOT be re-baselined; C3 changes `stack.yaml` only during a checkout **sync**, which these fixtures never run |
| `internal/agent_status_test.go` | external sync-state projection, `IssueSyncStateInvalid` (3 sites), dirty-blocking attribution | the classifier insertion must be cell-7/cell-10 transparent |
| `internal/stack_test.go`, `internal/stack_ancestry_test.go`, `internal/stack_status_test.go` | `TopoSort`, `Descendants`, `StackEdge` | untouched by §13.5 |

### 11.2 Real-repo / remote / worktree helpers available

- **External**: `setupGitRepo(t, defaultBranch)` builds `root/repo` + `root/remote.git` (bare), sets
  `origin`, pushes, sets the remote `HEAD` symref and `remote set-head origin -a` — everything
  `resolveBase`'s `origin/<default>` rewrite and `DefaultBranchIn` need. `withWorkspaceEnv(t, repo)`
  sets `HOME`, `TWS_ROOT`, and chdirs into the repo. `createLinearStack(t, repo, feature)` builds
  `root → parent → child` with real worktrees via the production `createWorktree`.
- **Checkout**: `setupGitRepoCheckout(t)` writes `.tws/config.yaml` with `workspace_mode: checkout`;
  `setupCheckoutSyncRepo`, `setupFeaturePath`, `createStackBranch`, `saveTestStack` build the
  feature and stack; `clearStepHook(t)` resets `internal.StepHook` with `t.Cleanup`.
- **Bare-remote removal** for the "no network" assertions: `os.RemoveAll(remote)` — the fixture
  root is a `t.TempDir()`, so this is safe and observable (`git fetch` then fails).
- **Linked worktree of the checkout repo** (cwd cell 9): `git -C <repo> worktree add <dir> <branch>`
  using `gitInDir`; nothing in the tree does this yet for a checkout fixture — it is new fixture
  code, not a new helper.

### 11.3 Seams

| Seam | Where | Status |
|---|---|---|
| `internal.StepHook` | `internal/checkout_sync.go` | exists; assigned by **7** of the 26 tests in `internal/cli/checkout_sync_test.go` (see §11.1), reset through `clearStepHook(t)` |
| `internal.SyncStepHook` **NEW** | `internal/sync_run_state.go` **NEW** | six external ordering points |
| `syncProcessAlive` **NEW** | `internal/sync_run_state.go` **NEW** | package-level `var`, but only the **default** for the injected `SyncClassifyOpts.Alive`; sync-path tests substitute it, status tests must not (**P5**, settled) |
| `SyncClassifyOpts.Alive` **NEW** | `internal/sync_run_state.go` **NEW** | the injected liveness seam; `buildFeatureSync` passes `proberAsChecker{b.opts.Proc}.Alive`, `internal/cli` passes nil |
| `AgentStatusOpts.Proc` (`ProcessProber`) | `internal/agent_status.go`, `internal/checkout_health.go` | already injected per builder; the *preferred* seam for status |
| `syncMarkerFn` **NEW** | `internal/cli/sync_modes.go` **NEW** | package-`cli` tests only |
| `goldenRegenEnv` pattern | `internal/cli/stack_status_test.go` | copy for the sync goldens |

### 11.4 Package placement and portability hazards

- `ResolveSyncSelection` tests → `internal/sync_selection_test.go` **NEW** (package `internal`,
  no Git).
- `isSyncMarker` tests → `internal/sync_run_state_test.go` **NEW** (package `internal`, in-package
  because the predicate is unexported).
- `newSyncMarker` / grammar / collision tests → `internal/cli/sync_modes_test.go` **NEW**.
- `RunDirClean` filter regression → `internal/exec_clean_test.go` **NEW** (package `internal`).
- Everything CLI-behavioural → `internal/cli/*_test.go`.

Hazards observed in this tree: macOS `/var` → `/private/var` symlinks (handled by the
`EvalSymlinks` entries in `goldenReplacements`); `t.TempDir()` paths appearing in output (handled by
`goldenAssertNoResidual`); Git background maintenance locks (handled by
`snapshotTreeIgnoringGitLocks`); host `GIT_CONFIG_*` injection (§10.6); map-seeded ordering in
`UniqueRepos` **and** `TopoSort` (**P2**, settled: never assert a sibling sequence, and never
capture a fixture with two or more mutually unordered output lines as a byte-compared golden);
`sh -c` used by checkout validation (POSIX `sh`, not
bash) — new wrapper scripts must obey the same rule.

Note that `.sync-run.lock` ends in `.lock` but lives in the **feature directory**, not under
`.git`, so `isTransientGitLockPath` (which requires a `.git` path segment) will **not** filter it
out of a snapshot. Verified by reading the predicate.

### 11.5 Incremental focused commands

```bash
go test ./internal/ -run 'SyncSelection|SyncRunState|SyncMarker|RunDirClean' -count=1
go test ./internal/cli/ -run 'SyncModes|SyncGolden|SyncScoped|SyncStateMatrix' -count=1
go test ./internal/cli/ -run 'CheckoutSync' -count=1
go test ./internal/cli/ -run 'ExternalFeatureDirectoryCommandMatrix|SyncContinue|SyncBranchIdentity' -count=1
go test ./internal/cli/ -run 'SpaceGuard|Push' -count=1
go test ./internal/cli/ -run 'TestStackStatus_ExistingCommandsUnchanged|TestStackStatus_HelpDrift' -count=1
go test ./internal/ -run 'AgentStatus' -count=1
```

---

## 12. Spec-to-code precision corrections (P1–P17) — all resolved

Raised here rather than absorbed. **Every item below is now settled in the spec** (revision of
2026-08-15); each section states the finding as it was measured and then the decision that was
taken, so the record of *why* survives with the *what*.

### P1 — RESOLVED. One external sync layout resolver, `internal.FeaturePath` first

**What the spec said originally** (§4.1 rule 5, §4.2 item 7, §4.5 C4, §13.4 rule 2): when
`ws.ResolveFeaturePath(feature)` and `internal.FeaturePath(feature)` disagree, "today's code loads
**no** stack and falls into `syncFallback`'s hard-coded `origin/main` rebase"; after the fix the run
"loads the resolved feature's `stack.yaml` and performs the ordinary stack sync".

**What the tree does.** The two derivations disagree exactly when `ws.MetadataRoot != TwsRoot()`:

- `internal.TwsRoot()` → `resolveTwsRoot` → **`TWS_ROOT` wins first**, then `DetectWorkspaceRoot(cwd)`,
  then the repo-derived root.
- `ws.MetadataRoot` (via `RequireWorkspace` → `MainRepoRoot()` → `ResolveCurrentWorkspaceE`) →
  `resolveExternalRoot` → `cfg.Workspaces[repoRoot]` else `<canonical-repo>.tws`. It **never reads
  `TWS_ROOT`**, and nothing in the codebase writes `cfg.Workspaces` automatically (only
  `tws config` via `SaveConfigFile`).

So with `TWS_ROOT` set — a documented priority-1 override (`docs/configuration.md` line 7,
`README.md` line 210, `docs/cheatsheet.md` line 193) — and cwd inside the repository, the **resolved**
path is the empty one and the **re-derived** path is the real one. Measured directly with a scratch
probe using the shipped `setupGitRepo` + `withWorkspaceEnv` + `createLinearStack` helpers:

```
ws.Mode      = external
ws.MetaRoot  = …/001/repo.tws
TwsRoot()    = …/003/workspace
resolvedPath = …/001/repo.tws/feature            LoadStack(resolved)   FAILED (no such file)
FeaturePath  = …/003/workspace/feature           LoadStack(FeaturePath) ok
```

**Why the original wording was unsafe.** Implementing "route `syncFeature` at the resolved path"
verbatim would make `internal.LoadStack` fail on that configuration, enter
`syncFallback(<repo>.tws/feature)`, read a non-existent `worktrees/` directory ⇒ zero entries ⇒
**nothing printed, nothing rebased, `Complete: true`, `Sync complete.`, exit 0**: a silent no-op on
the **no-flag** path in cwd cell 1, which §12.11 declares frozen.

**Corroborating split-brain in the tree** (all by inspection, same divergence):
`syncCmd` reads/writes sync state at candidate A (`ws.ResolveFeaturePath`) while `saveIncompleteSync`
writes it at candidate B (the path `syncFeature` derived), so a failed sync's state is written where
the next plain run does not look; `pushFeature` resolves candidate A through
`internal.RequireFeaturePath` (`internal/cli/push.go:36`) and, once per stack entry, through
`internal.RequireWorktreePath` (line 47, itself `RequireWorkspace` + `ws.WorktreePath`, i.e. `1 + N`
`RequireWorkspace` events per invocation, each a **pair** of Git records, P15), so `tws sync --push` today syncs via B and then fails its push
half with `no stack.yaml found for feature: <f>`; `runCheckoutSync` resolves candidate A through
`internal.RequireFeaturePath` as well (checkout mode has no linked worktrees, so `ws.WorktreePath`
returns `""` there).

**Decision taken (spec §3.11, normative; §13.4 rule 2).** One resolver for the whole external sync
path — `resolveExternalSyncLayout(ws internal.Workspace, twsRoot, feature string)
(externalSyncLayout{FeaturePath, WorktreesRoot}, error)` in package `cli`, taking the caller's
already-resolved `twsRoot` and issuing **no Git command and no resolution of its own** (P15) — with
this rule:

1. equal candidates (every healthy layout, every AC 1 fixture) ⇒ that path, **no probe at all**;
2. else **candidate B = `filepath.Join(twsRoot, feature)` (today's `internal.FeaturePath(feature)`
   value) wins when it holds a readable
   `stack.yaml`, including when candidate A also does** — this preserves the documented `TWS_ROOT`
   priority and today's execution root;
3. else candidate A wins when only A holds one;
4. else candidate B, so the frozen no-stack `syncFallback` path keeps today's root.

Both the stack path **and** the worktrees root come from the winner, and the resolved values are
threaded explicitly into `syncFeature`, `syncWithStackFiltered`, `staleStackEdgesFiltered`,
`branchContainsConfiguredParent`, `handleSyncAbort`, `handleSyncContinue`, `saveIncompleteSync`,
`markUpdatedAncestors`, `syncFallback`, `runValidation`, the legacy/v2/guard state paths,
`pushFeature`/`pushSelected` (and `pushCmd`'s **external** arm), completion
(`syncEntryCompletion`), and the classifier
call — **no mixed roots**, asserted by the AC 51 grep that no `internal.FeaturePath` /
`internal.WorktreePath` / `internal.RequireFeaturePath` / `internal.RequireWorktreePath` call
survives in `sync.go` or `sync_helpers.go`, and none survives in `push.go` outside the single
checkout helper. Unsafe/symlink ambiguity is refused by the
existing guards (`GuardFeatureName`, `*ErrAmbiguousFeature`, I18), unchanged.

**Two corrections folded into P1 by the iteration-3 review, both about `tws push`.** Threading the
layout into `pushCmd` naively would have deleted two behaviours that `internal.RequireFeaturePath` /
`internal.RequireWorktreePath` carry today and the resolver does not:

- **The sibling-space guard.** `RequireFeaturePath` is `tws push`'s only `GuardFeatureName` call
  (`internal/resolve.go:294-303`), pinned by `TestSpaceGuard_ExternalCommandMatrix/push`
  (`internal/cli/space_guard_test.go:270-276`). Settled: `pushCmd` calls
  `internal.RequireWorkspace()` then `internal.GuardFeatureName(ws.MetadataRoot, feature)` —
  same root, same position relative to workspace resolution — **before** the mode branch, before its
  single `internal.TwsRoot()` call, and before
  `resolveExternalSyncLayout`, which itself stays guard-free so `syncCmd`'s own
  `GuardFeatureName(twsRoot, …)` is neither duplicated nor re-rooted (its root **value** and call
  site are unchanged). `space_guard_test.go`
  is therefore **not** edited (spec §3.11, §13.3, AC 51, AC 59).
- **The checkout refusal.** `RequireWorktreePath` returns `ErrWorktreeUnsupported` in checkout mode
  (`internal/resolve.go:323-334`), so `tws push <f>` there fails loudly on the first stack entry.
  Routing checkout push through the external layout would have turned that into
  `  [-] NAME (archived, skipped)` per entry and **exit 0** — a silent success. Settled: `pushCmd`
  branches on `ws.Mode` and checkout keeps today's body verbatim as `pushFeatureCheckout`; C2's
  `GitBranch()` fix and the layout stay external-scoped, and the CHANGELOG/doc claims are scoped
  the same way (spec §3.11, §7.6, §14, AC 59, follow-up spec §18 item 11).

Option 2 of the original list — making `ResolveCurrentWorkspaceE` honour `TWS_ROOT` — was
**rejected**: it re-roots every command and would add a workspace-resolution hard parent, so
`internal/workspace.go`, `internal/resolve.go`, and `internal/paths.go` stay untouched and the
dependency DAG is unchanged (spec §13.5, §15, §18 item 10). Option 3 (declare the divergent
configuration as a fifth declared change without fixing it) was rejected as a silent success.

**Consequences recorded in the spec:** C4 now covers **both** disagreement directions and **all**
external cwd cells 1–6 (the trigger is `TWS_ROOT`/workspace detection, not cwd), plus checkout cell
9; §4.1 rule 5, §4.2 items 3/4/7/11/12/13, §4.3 items 1–2, §4.5 C4, §12.11, §13.4, §14, §17.2, AC 1,
AC 46, and the new **AC 58** (a `TWS_ROOT`-divergent regression covering no-flag, scoped,
`--continue`, `--abort`, `--push`/`tws push`, completion, and the state round-trip) all restate it.
Completion and the classifier use the same resolver; `tws status` deliberately does not (declared
out of scope, spec §3.11 and §18 item 10).

### P2 — RESOLVED. `TopoSort` order is not deterministic among siblings, so only parent-before-child is contracted

`internal.TopoSort` seeds its Kahn queue by ranging over the `inDegree` **map**, so every entry with
in-degree 0 — i.e. **every anchor** — can appear in any relative order. Measured: a stack with three
literal-base entries plus one child produced **3 distinct orders in 200 runs**
(`a,b,c,d` ×152, `c,a,b,d` ×24, `b,c,a,d` ×24).

Affected requirements: §3.7 and §10.3 step 12b ("one `[-]` line per selected anchor, **in
topological order**"), `printLocalOnlyNoOp`, AC 24, and AC 18's `literal-root` fixture (three
anchors by construction). §17.1 already admits "no golden pins … sibling ordering: `UniqueRepos` and
`TopoSort` both seed from maps", which contradicts the §3.7/AC 24 wording.

**Decision taken (spec §3.7, §5.2 step 1, §3.10 path 3, §10.3 step 12b, AC 18, AC 24, AC 50, §17.1,
§21 item 14).** Option (c) — a stable tie-break inside `ResolveSyncSelection` — was **rejected**: it
is still a second ordering rule layered on `TopoSort`'s output, it buys only cosmetics, and
`internal/stack.go` is explicitly untouched (§13.5). The settled contract is:

- **no second sort anywhere**, and `TopoSort` is unmodified (grep-asserted in AC 51);
- output, the checkout plan, and the persisted `selected` list simply **preserve the `TopoSort`
  result**;
- the only guaranteed property is **parent-before-child**; sibling and independent-anchor order is
  **unspecified** and may vary between runs;
- every multi-anchor `[-]` block and every multi-anchor assertion compares an **unordered set**
  (AC 18's three-anchor `literal-root` fixture, AC 24, AC 50's repeated-run property test), while
  **one-anchor** fixtures may stay byte-pinned goldens;
- §17.1 additionally forbids capturing any fixture with two or more mutually unordered output lines
  (multi-repo `Fetching …`, multi-anchor `[-]`) as a byte-compared golden.

The shipped goldens are safe: the `checkout`/external golden fixtures have exactly one
in-degree-0 entry (`models`, base `main`).

### P3 — RESOLVED. `internal.DefaultBranchIn` never returns a non-nil error, so the fallback is declared defensive and unreachable

`DefaultBranchIn` ends with `return "main", nil`; both probe failures fall through to the hard-coded
value. `DefaultBranch()`'s `if err != nil { return "main" }` is therefore already unreachable.
§13.4 rule 3's "on error, fall back to today's `internal.DefaultBranch()` value" cannot execute.
**Decision taken (spec §13.4 rule 3):** `repoCtx != ""` ⇒ `branch, _ := internal.DefaultBranchIn(repoCtx)`,
with the error ignored **by construction** (the `main` fallback is built into the callee). An
`err != nil` arm may be written as defensive depth, but the spec declares it **unreachable in the
shipped tree** and forbids any test from claiming to exercise it.

### P4 — RESOLVED. "eleven changed files" is **nine** production files and **seven** documentation files

§13.3's table lists exactly nine production files: `internal/cli/sync.go`,
`internal/cli/sync_helpers.go`, `internal/syncstate.go`, `internal/checkout_sync.go`,
`internal/cli/checkout_sync.go`, `internal/cli/push.go`, `internal/cli/new.go`,
`internal/agent_status.go`, `internal/cli/importcmd.go`. The documentation set of §14 adds
**seven**: `README.md`, `docs/cheatsheet.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`,
`assets/skills/copilot/tws.prompt.md`, `CHANGELOG.md`, **`docs/roadmap.md`** (move sync modes into
the shipped list, retarget "Current target"), and **`docs/engineering-workflow.md`** (append the
shipped item and update the "Next roadmap feature" line) — the last two are in §14's table and were
missed by the earlier count of five. The test set adds the new files of §14.1 plus **three** edited
existing ones: `internal/cli/checkout_lifecycle_test.go` (two `isRuntimeState` rows),
`internal/cli/sync_continue_integration_test.go` (the `handleSyncContinue` call sites forced by P1),
and `internal/cli/checkout_sync_test.go` (the one direct `syncFeature` call, added by the
iteration-3 revision — see §13 R21 and §14.1).

**Decision taken:** spec §13.6 now reads "two new `internal` files, one new `cli` file, **nine**
changed production files, **seven** documentation files, and the test files of §13.1/§17 — new
`*_test.go` files plus exactly three edited existing test files". The rest of §13.6 is verified
correct: no new command
(`internal/cli/root.go` unchanged), no new package, no new dependency, no `go.mod` change —
`crypto/rand`, `encoding/hex` and `regexp` are stdlib.

### P5 — RESOLVED. The liveness seam becomes an injected option, `SyncClassifyOpts.Alive`

§17.4 specifies a package-level `var syncProcessAlive = isProcessAlive` in `internal`. But
`buildFeatureSync` already resolves liveness through an **injected** seam,
`proberAsChecker{b.opts.Proc}` (`internal/agent_status.go` line 1363, `AgentStatusOpts.Proc
ProcessProber`, `internal/checkout_health.go`). If `ClassifyExternalSyncState` consults a package
global, `tws status` tests must mutate process-global state (not parallel-safe, and inconsistent
with how every existing status test injects a prober).

**Decision taken (spec §11.1 rules 3 and 8, §17.4, AC 43, §21 item 15):** the classifier options
become `SyncClassifyOpts{AlwaysReadGuard bool; Alive func(pid int) bool}`, with `Alive == nil`
defaulting to the package-level `syncProcessAlive`. `buildFeatureSync` passes
`Alive: proberAsChecker{b.opts.Proc}.Alive` — the prober it is already given, the same seam its
checkout half uses at `internal/agent_status.go:1363` — and `internal/cli` passes nil. Status tests
inject through `AgentStatusOpts.Proc` and **MUST NOT** mutate `syncProcessAlive`, so they stay
parallel-safe; §17.4's global remains the sync-path seam and its default.

### P6 — RESOLVED. `git check-ref-format --branch <marker>` exits 128, not 1

Verified with Git 2.55.0 in a scratch repository: `--branch` form exits **128**,
`git check-ref-format refs/heads/<marker>` exits 1, `git branch <marker>` exits 128 with
`fatal: '…' is not a valid branch name`. **Decision taken (spec AC 29):** rejection is asserted as a
**non-zero** exit status and asserting `== 1` is explicitly forbidden as platform- and
version-fragile; the measured codes are recorded in the criterion as evidence, not as expectations.

### P7 — RESOLVED. `atomicWriteFile` puts a transient `.tws-state-*` file inside the feature directory

Making `SaveSyncState` atomic reuses `atomicWriteFile`, which calls
`os.CreateTemp(filepath.Dir(path), ".tws-state-*")`. For `.sync-state.yaml` that directory is the
**feature directory**, whose file *set* §4.1 freezes. Post-run the set is unchanged (rename), but
(a) a crash mid-write leaves a `.tws-state-XXXXX` residue where today's `os.WriteFile` leaves
nothing, and (b) any snapshot taken from a `SyncStepHook` will observe it. **Decision taken (spec
AC 40, §17.3):** AC 40's claim is now scoped to "never leaves a partial **`.sync-state.yaml`**" with
the residue stated explicitly; every file-set snapshot helper ignores `.tws-state-*` (a rule §17.3
now carries, together with the reminder that `isTransientGitLockPath` does **not** cover it and that
`.sync-run.lock` MUST still appear in snapshots); and `isRuntimeState` is **not** extended (import
plants only what the archive contains, and export is allow-listed).

### P8 — RESOLVED. `syncEntryCompletion` uses the same layout resolver as the run

§3.8 step 2 uses `internal.RequireWorkspace()` + `ws.ResolveFeaturePath(feature)`. Under P1's
divergence that resolves the *empty* directory, so `--only <TAB>` would offer nothing for a feature
`--only <name>` can actually sync. **Decision taken (spec §3.8 step 2):** completion resolves
through `resolveExternalSyncLayout(ws, twsRoot, feature)` — the run's own resolver, fed by one
`internal.RequireWorkspace()` and one `internal.TwsRoot()` call (two resolution events, never more,
P15) — and loads
`stack.yaml` from `layout.FeaturePath`, still degrading every error to "no candidates". §3.8 also
now records that `internal.ListBranches(feature)` MUST NOT be reused: it returns `stack.yaml` names
in file order but does **not** filter `Archived` and falls back to listing `worktrees/`, both of
which §3.8 forbids. AC 58 asserts completion on a divergent layout.

### P9 — RESOLVED. The I20 literal is declared once as `errSyncModeFlagsNeedV2`

AC 51 asks for "exactly once per mode-owning call site". External raises it in
`internal/cli/sync_modes.go` **NEW**, checkout in `internal/cli/checkout_sync.go`; the two strings
must be byte-identical. **Decision taken (spec §13.2, §13.3, AC 51):** the string is declared
**exactly once** in the repository as the unexported package-`cli` constant `errSyncModeFlagsNeedV2`
in `internal/cli/sync_modes.go`, referenced by both call sites, and the grep gate now asserts one
literal rather than "one per call site".

### P10 — RESOLVED (as a fixture rule). `checkoutStateDir` and `ws.CheckoutStateDir()` disagree on the legacy checkout layout

`internal.checkoutStateDir(featurePath)` walks two directories up from the feature path
(`<repo>/.tws/features/<f>` → `<repo>/.tws/state` ✓; legacy `<repo>/.tws/<f>` → `<repo>/state` ✗),
while `internal/agent_status.go` deliberately uses `b.ws.CheckoutStateDir()`
(`<MetadataRoot>/state`) with a comment naming this defect. This is pre-existing and out of scope,
but every new checkout fixture and every state-file assertion must use the **new** layout
(`.tws/features/<feature>`), or the transaction will be written outside `.tws/` and the status
projection will not find it. **Decision taken (spec §17.2):** the `checkout` fixture row now states
"new layout `.tws/features/<feature>/**` only — never the legacy `.tws/<feature>` shape", with the
reason recorded inline. The underlying `checkoutStateDir` defect stays out of scope.

### P11 — RESOLVED. `SyncSelectedEntry` cannot drive the executors alone

It has no `LastBaseSHA`. Any implementation that rebuilds `StackEntry` values from the selection
silently disables the amend-aware `--onto` replay. **Decision taken (spec §5.5, §6.5, §13.3):**
§5.5 now carries a binding "membership and role only" paragraph — both executors keep iterating real
`StackEntry` values (external over its existing `sorted []internal.StackEntry`, checkout over
`TopoSort(stack)` inside `BuildCheckoutPlan`) and consult the selection **only** through `Names` and
`Role`; the `UniqueRepos` subset in §6.5 is likewise built from the real entries.

### P12 — RESOLVED. `formatSyncStatus` already produces the `[-]` line shape

§3.7 requires `  [-] NAME (no in-stack parent edge to propagate)`. `formatSyncStatus(name, mode,
"skipped")` renders `  [%s] %s (%s)` with `-` for `skipped`, so passing the sentence as the *mode*
argument produces those exact bytes. **Decision taken (spec §3.7, §3.10 path 3):** the reuse is now
mandated in the no-op paragraph itself — `formatSyncStatus(entry.Name, "no in-stack parent edge to
propagate", "skipped")` — so no second formatter is introduced, and `printLocalOnlyNoOp` in package
`internal` repeats the same literal (it cannot call the package-`cli` helper) with a binding
requirement that the two stay byte-identical.

### P13 — RESOLVED. `TestExport_RuntimeStateExcluded` is a unit table, not an export test

At `internal/cli/checkout_lifecycle_test.go:617` it only calls `isRuntimeState(tc.path)`. AC 45's
tarball/import assertions are new tests; the wording "gains `.sync-state.v2.yaml` and
`.sync-run.lock` (both `true`), keeping every existing row unchanged" is exact for the table part
only. **Decision taken (spec AC 45):** the criterion now says so explicitly — the table half is a
two-row edit to a pure unit table that builds no tarball, and the end-to-end import/export
assertions are new tests.

### P15 — RESOLVED. A workspace-root resolution is a *pair* of Git records, and the external push reduction is `1 + N` → **2**

**What the spec said** (§3.11, §4.1 rule 6c, AC 2, AC 59, in the revision before this one): the
external push path "resolves the workspace **exactly once** per command invocation", the carve-out
is "the removal of the `N` per-entry `git -C <cwd> rev-parse --git-common-dir` records", and the
carve-out "reaches **no** AC 1 golden".

**What the tree does.** Measured with a logging `git` wrapper on `PATH` around the built binary:

```
$ tws push nonexistent-feature
git rev-parse --show-toplevel                       ← LoadConfig → repoConfigPath → RepoRoot
git -C <repo> rev-parse --git-common-dir            ← MainRepoRoot          (RequireWorkspace event)

$ tws sync nonexistent-feature-xyz --abort
git rev-parse --show-toplevel
git -C <repo> rev-parse --git-common-dir                                     (RequireWorkspace event)
git -C <repo> rev-parse --git-common-dir            ← MainRepoRoot first
git rev-parse --show-toplevel                       ← then LoadConfig        (TwsRoot event)
```

And, on a stack whose config sets `test_command`, each validated entry of a plain external sync
emits this window — the adjacency that breaks any show-anchored grouping:

```
git rev-parse --show-toplevel                       ← runValidation → LoadConfig  (STANDALONE probe)
git -C <repo> rev-parse --git-common-dir            ┐ next entry's internal.WorktreePath
git rev-parse --show-toplevel                       ┘ → internal.TwsRoot          (TwsRoot event)
```

Four facts follow, and all four contradicted the earlier wording:

1. **A resolution is two records, not one.** `RequireWorkspace` emits `rev-parse --show-toplevel`
   (via `LoadConfig` → `repoConfigPath` → `RepoRoot`, `internal/config.go:35-41`) **before**
   `MainRepoRoot`'s `-C <cwd> rev-parse --git-common-dir`; `TwsRoot` emits the same two in the
   **reverse** order, because it calls `MainRepoRoot` first (`internal/paths.go:74-79`). A carve-out
   phrased in `--git-common-dir` records would silently ignore half of every removed resolution.
2. **The old resolver signature hid a resolution.** `resolveExternalSyncLayout(ws, feature)`
   computing candidate B as `internal.FeaturePath(feature)` would call `internal.TwsRoot()` **inside
   the resolver**, i.e. the "Git-free" resolver would have emitted a full `TwsRoot` event — the very
   thing §4.1 rule 6 says it never does.
3. **The carve-out does reach the AC 1 external goldens.** External `tws sync` re-derives paths
   repeatedly (`syncFeature`'s `internal.FeaturePath`, per-entry `internal.WorktreePath` in both
   passes of `syncWithStackFiltered`, `staleStackEdges`, `handleSyncAbort`/`handleSyncContinue`), so
   a clean plain run performs `3 + N + E` resolution events today and **two** after the fix. Those
   removals appear in every frozen external no-flag capture.
4. **A bare `rev-parse --show-toplevel` record is not proof of a resolution event, so the grouping
   cannot anchor on it.** `internal.LoadConfig` emits exactly one such record on its own, from every
   call site that is neither `RequireWorkspace` nor `TwsRoot`, and `runValidation`
   (`internal/cli/sync_helpers.go:237-238`) is one of those sites **on the external sync path**. The
   measured `standalone show → common → show` window above is the result. A left-to-right greedy
   grouping anchored on show records pairs the standalone probe with the following
   `--git-common-dir`, reports a `RequireWorkspace` event that never happened, and orphans the real
   `TwsRoot` event's second record — corrupting the event counts on **both** sides of every AC 2 /
   AC 58 / AC 59 comparison for any fixture that configures a test command.

**Decision taken (spec §3.11 "Workspace-root resolution happens once per external command", §3.6
steps 5–6, §3.8 step 2, §4.1 rule 6c, §13.4 rule 2, AC 2, AC 51, AC 58, AC 59, §17.1 mode 3).**

- `resolveExternalSyncLayout(ws, twsRoot, feature)` takes the root as a parameter, builds candidate B
  with `filepath.Join(twsRoot, feature)`, and contains **no** `internal.TwsRoot` /
  `internal.FeaturePath` / `internal.WorktreePath` / `RequireWorkspace` / `LoadConfig` / `exec`
  call — grep-asserted by AC 51.
- `syncCmd.RunE` calls `internal.TwsRoot()` **once**, at today's guard position, and feeds that one
  value to both `internal.GuardFeatureName(twsRoot, feature)` (identical root value and position)
  and the resolver. `pushCmd.RunE` keeps `GuardFeatureName(ws.MetadataRoot, …)` first, then branches;
  only the **external** arm calls `internal.TwsRoot()`, once, after the branch, so the checkout arm
  gains no `TwsRoot` event. `syncEntryCompletion` does the same, once each.
- The declared arithmetic is stated honestly everywhere: external sync `3 + N + E` → **2**, external
  push `1 + N` → **2** (count unchanged at `N = 1`; one event **added** on an empty stack), checkout
  push **+1** `RequireWorkspace` event. The claims "exactly one workspace resolution" and "only `N`
  `--git-common-dir` records" are removed from the spec and are forbidden in the tests.
- `internal/paths.go`, `internal/workspace.go`, and `internal/resolve.go` stay byte-identical
  (spec §13.5, AC 51): only the *call sites* change, so no workspace-resolution parent becomes a
  directly-modified hard dependency (spec §15).
- **The sidecar grouping is anchored on `--git-common-dir`, never on a bare `--show-toplevel`**
  (spec §3.11 grouping rule, §4.1 rule 6c, §17.1 `c4ResolutionCompression`, AC 2, AC 51, AC 58,
  AC 59). Anchors are the `--git-common-dir` records **whose `-C` operand is the record's own process
  cwd** (P17 narrows this from "every `--git-common-dir` record"), visited in **reverse log
  order**: pair **forward** (`common → show`, `TwsRoot`) when the immediately following record is an
  unconsumed bare `rev-parse --show-toplevel` in the same recorded cwd; otherwise pair **backward**
  (`show → common`, `RequireWorkspace`) with the immediately preceding unconsumed bare show record;
  otherwise **fail** the capture. The reverse-order walk is what keeps the forward preference safe:
  today's external push log is `show → common` repeated `1 + N` times and still groups into `1 + N`
  `RequireWorkspace` events, while the ambiguous `show → common → show` run resolves to the
  `TwsRoot` reading. Verified shapes: `show common common show` → RW + TwsRoot;
  `show common show common …` → `1 + N` RW; `common show common show` → two TwsRoot;
  `show common show` → one TwsRoot + one ungrouped probe;
  `show common foreign×(1+M) common show` (cells 4–6) → RW + TwsRoot with the foreign records
  ungrouped (P17).
- **Every unpaired bare `rev-parse --show-toplevel` is a standalone `LoadConfig` probe and stays in
  the ordered non-event log**, compared verbatim and in position on both sides; it may not be
  removed, absorbed into an event, reordered, or normalized. This is exactly what makes the measured
  `standalone show → common → show` adjacency resolve correctly: the anchor takes the forward pair
  (one `TwsRoot` event) and leaves the leading probe ungrouped, where it belongs. `runValidation`
  emits that probe before and after the change alike (it merely receives a `layout`-derived worktree
  path), so the verbatim non-event comparison holds. The spec must **not** claim, and no comparator
  may assume, that no other bare `rev-parse --show-toplevel` record exists on these paths.

### P17 — RESOLVED. `inferExternalRepoRoot` probes are ordinary non-event records; anchors are **cwd-scoped** `--git-common-dir` records

**What the spec said** (§3.11 grouping rule, §4.1 rule 6c, §17.1 mode 3, AC 2/46/58, in the revision
before this one): grouping "anchors on **each** `-C <cwd> rev-parse --git-common-dir` record", and
"a `--git-common-dir` record is never left ungrouped" — otherwise the capture fails. The fallback
arm's extra records were dismissed as belonging to "a `RequireWorkspace` event that exists on both
sides".

**What the tree does.** `MainRepoRoot` is the only caller that passes the process cwd to
`MainRepoRootIn` (`internal/exec.go:18-24`). `inferExternalRepoRoot` (`internal/workspace.go:339-395`)
calls `MainRepoRootIn(<other path>)` once per candidate: configured workspace keys whose root
canonicalizes to the metadata root (Go **map** iteration), the `.tws`-stripped **sibling repo**, and
every **materialized** entry (non-`Repo`, non-archived, `worktrees/<name>` present) of **every**
feature under the metadata root. It runs on `RequireWorkspace`'s fallback arm — cwd outside any
repository, i.e. cwd cells 4, 5, 6 of spec §12.11, which are *frozen* cells for external sync and
required coverage for AC 46/AC 58. Measured against the built binary (external workspace
`<root>/repo.tws`, feature `myfeat` with three materialized entries):

```
$ tws sync myfeat --abort              # cwd = workspace root (cell 4); identical from cells 5–6
show                                   ┐ RequireWorkspace event (both records exit 128)
common@cwd                             ┘
common@<root>/repo                     ← infer: sibling repo
common@<ws>/myfeat/worktrees/feat-root ← infer: materialized entry
common@<ws>/myfeat/worktrees/feat-a    ← infer: materialized entry
common@<ws>/myfeat/worktrees/feat-b    ← infer: materialized entry
common@cwd                             ┐ TwsRoot event (guard)
show                                   ┘
$ tws sync myfeat --abort              # cwd = repository root (cells 1–3): 4 records, 0 infer
$ tws push myfeat                      # cwd = cell 4, N = 3: 1+N = 4 RequireWorkspace events,
                                       #   EACH trailed by its own 4-record infer block
```

Under the old rule the four infer records were anchors with no show partner, so rules 1–2 fail and
rule 3 **fails the capture** — on the frozen cell-4 external sync goldens and on every AC 58
feature-directory run. Three further measured facts:

1. **The block scales with the fixture**: `C + S + M` records — measured `0 + 1 + 3 = 4`, and `5`
   after materializing a second feature (whose probe sorts first, `os.ReadDir` order; within a
   feature the order is `stack.Branches` order). It is a property of the workspace, not of the
   change under test, so it may not be pinned as a constant.
2. **The configured-workspace class is unordered**: with four configured workspaces mapping to one
   metadata root, three consecutive runs emitted the probes in three different orders (`r3 r4 r1 r2`,
   `r4 r1 r2 r3`, `r1 r2 r3 r4`). Such a fixture can never be byte-pinned or ordered-compared.
3. **The block is attached to its `RequireWorkspace` call**, always contiguous and immediately after
   that call's completed `show → common@cwd` pair; `TwsRoot`'s failing `MainRepoRoot` triggers **no**
   inference (`resolveTwsRoot` uses the Git-free `DetectWorkspaceRoot`).

**Decision taken (spec §3.11 grouping rules and the new `inferExternalRepoRoot` subsection, §4.1
rule 6c, §17.1 mode 3 `c4ResolutionCompression`, §17.2 fixture rule, AC 2, AC 46, AC 51, AC 58).**

- **Anchor = operand test.** A `--git-common-dir` record anchors an event **iff**
  `filepath.Clean(<-C operand>)` equals the `filepath.Clean` of its own recorded process cwd. Those
  are the `MainRepoRoot` records of `RequireWorkspace` / `TwsRoot`. The pair algorithm is otherwise
  **unchanged**: reverse-order walk, forward → `TwsRoot`, backward → `RequireWorkspace`, else fail;
  an unpaired bare show is still a standalone `LoadConfig` probe.
- **Foreign-operand records are ordinary non-event records**: never anchors, never grouped, never
  counted toward the `3 + N + E` / `1 + N` / `2` budgets, compared verbatim and in position in the
  ordered non-event log, and never normalized away.
- **One licensed removal, push-path only.** A `RequireWorkspace` event removed by carve-out (c) takes
  the block emitted inside that same call with it (`1 + N` blocks → 1 from cells 4–6). External sync
  keeps its single `RequireWorkspace`, so its single block is invariant and must compare verbatim.
- **The refactor does not remove the inference**: `RequireWorkspace` still runs before
  `internal.TwsRoot()` and `resolveExternalSyncLayout`, and `internal/workspace.go` stays
  byte-identical (spec §13.5, AC 51).
- **No anchor is left ungrouped after the distinction.** In both chains the anchor's `LoadConfig`
  partner is adjacent with nothing between them (measured in cells 1–6, exit 0 and exit 128 alike),
  and a foreign record can only follow a *completed* `RequireWorkspace` pair, so it can only make the
  forward test decline — the correct reading. Rule 3 becomes a fail-closed guard for two degenerate
  inputs only: a future caller that interleaves Git between `LoadConfig` and `MainRepoRoot`, and a
  fixture whose cwd is itself an inference candidate (excluded by the §17.2 fixture rule).

### P16 — RESOLVED. `runCheckoutSync` keeps `internal.RequireFeaturePath`, second resolution included

**What the spec said** (§3.6 step 4, §4.3, §10.1, §10.3, §13.3, §13.4 rule 1, in the revision before
this one): `runCheckoutSync` receives the resolved `ws` and therefore "MUST NOT call
`internal.RequireFeaturePath`", using `internal.ResolveFeaturePathFor(ws, feature)` instead "so the
workspace is resolved once and the C4 probe keeps its pinned argv position".

**Why that was wrong.** `internal.RequireFeaturePath` (`internal/resolve.go:294-303`) is
`RequireWorkspace` + `GuardFeatureName(ws.MetadataRoot, …)` + `ws.ResolveFeaturePath`, and it is the
**first statement** of today's `runCheckoutSync` (`internal/cli/checkout_sync.go:11-14`). Dropping
it would **remove** a whole pre-change `RequireWorkspace` event — both of its records — from a path
whose only declared argv difference is the **one added** containment probe (spec §4.1 rule 6a).
That removal is nowhere declared, it is not part of carve-out (c) (which is external-only), and it
would make the checkout captures differ from the pre-change log by an addition *and* a deletion
while AC 2 asserts a verbatim resolution prefix. `ResolveFeaturePathFor` also has a different
fallback arm (`ws.MetadataRoot == ""` → guarded `TwsRoot()`, `internal/resolve.go:305-321`), so the
error semantics on a broken workspace are not identical either.

**Decision taken (spec §3.6 step 4, §4.3 items 1–2, §10.1, §10.3, §10.9, §13.3, §13.4 rule 1, AC 2,
AC 46, AC 51).** `runCheckoutSync` gains the `(ws internal.Workspace, opts internal.CheckoutSyncOpts)`
signature but **keeps `internal.RequireFeaturePath(feature)` verbatim, in place**: its second
`RequireWorkspace` event, its guard, its layout resolution, and its error semantics are all
preserved. `ws` exists on that call only to supply `ws.RepoRoot` for `RepoDir` and the I19
containment refusal **without introducing a third resolver**. The containment probe is issued after
that unchanged resolution and before `RunCheckoutSync`'s first preflight record, so the checkout
pre-change resolution prefix survives verbatim and the **only** argv difference on the whole
checkout path remains the one added probe. AC 51's grep is inverted accordingly:
`internal/cli/checkout_sync.go` must contain **exactly one** unchanged
`internal.RequireFeaturePath(feature)` call and **no** `internal.ResolveFeaturePathFor` call.

### P14 — confirmations (no change needed)

`v1.2.14` is the current tag and `git show v1.2.14:internal/cli/sync.go` is behaviourally identical
to HEAD for the three legacy paths (§9.1 accurate); offline tag builds are feasible from the warm
module cache; `SortFlags` default ordering is confirmed empirically; the "three consumers of
`.sync-state.yaml`" claim is exhaustively verified; external `--test` really is inert
(`testCmd` is passed only to `runCheckoutSync`); `internal/checkout_sync.go` contains **zero**
`fmt.Print*` today; every spec line reference spot-checked (`sync_helpers.go:157`, `sync.go:173-174`,
`importcmd.go:173-179`, `agent_status.go:1408-1440`, `exec.go:138-186`, `checkout_sync.go:298-303`)
resolves to the cited symbol.

---

## 13. Risks and guards

| # | Risk | Where it bites | Structural guard / test |
|---|---|---|---|
| R1 | Mixed-root run (P1, settled) | external, every verb, no-flag included | **one** resolver (`resolveExternalSyncLayout`, spec §3.11) for stack **and** worktrees **and** state **and** push **and** completion; AC 51 grep proving no `internal.FeaturePath`/`WorktreePath`/`RequireFeaturePath`/`RequireWorktreePath` call survives in `sync.go`/`sync_helpers.go`/`push.go`; AC 58 regression with `TWS_ROOT` diverging from `<repo>.tws`, in **both** directions, asserting real rebases, a state file the next run can see, and a working `--push` |
| R1b | A resolution hidden inside the resolver, or an argv claim that counts records instead of events (P15) | frozen external goldens, AC 2 / AC 59 comparators, changelog and docs | resolver takes `twsRoot` and is grep-asserted free of `TwsRoot`/`FeaturePath`/`WorktreePath`/`RequireWorkspace`/`LoadConfig`/`exec` (AC 51); exactly one `internal.TwsRoot()` call in `sync.go` and one in `push.go`'s external arm (AC 51); §17.1 mode 3 groups records into `workspaceRootResolutionEvent` pairs before comparing, and AC 59 pins the `N = 1` (count unchanged) and empty-stack (one event added) boundaries so `1 + N` → 1 can never be encoded |
| R1c | A comparator that anchors on **every** `--git-common-dir` record (P17) | every capture from cwd cells 4–6 — i.e. the frozen cell-4 external sync goldens, AC 46's feature-directory cells, and AC 58's divergent runs | anchors are restricted by the **operand test** (`-C` operand == that record's own process cwd, spec §3.11) and AC 51 greps the single grouping helper for it; `inferExternalRepoRoot` probes are asserted as ordinary non-event records — verbatim and in position, excluded from every event budget (AC 2, AC 46, AC 58); their `C + S + M` count may not be pinned as a constant and the §17.2 fixture rule forbids byte-pinning any fixture with ≥ 2 configured workspaces mapping to one metadata root (map order measured non-deterministic) |
| R2 | Non-deterministic anchor order (P2, settled) | `[-]` block, plan order, goldens | contract reduced to parent-before-child; unordered-set assertions for multi-anchor blocks; byte-pinned goldens only for one-anchor/one-repo fixtures; **no** second sort and **no** `TopoSort` change (AC 51 grep) |
| R3 | `saveIncompleteSync` called on a new-mode failure | overwrites the sentinel with a resolvable name ⇒ old `--continue` broad-resumes | AC 51 grep (`saveIncompleteSync` has exactly one caller); a state-shape assertion after every scoped failure |
| R4 | Sentinel written before the guard, or payload before sentinel | produces `{absent, valid}` from a healthy crash, breaking §8.6's reachability argument | `SyncStepHook` crash matrix (AC 36) asserting the six crash points map to the tabulated cells |
| R5 | Teardown in the wrong order | leaves a payload whose sentinel is gone | same hook matrix; teardown helper with a single implementation |
| R6 | Old binary resolving the marker as a branch | broad resume | per-run `crypto/rand` marker + I17 collision preflight + `.lock` suffix (verified unusable as a ref) |
| R7 | `--update-refs` left on in a scoped run | moves unselected refs | `git for-each-ref` before/after snapshot (AC 19/21) and the argv log |
| R8 | `markUpdatedAncestors` still called when scoped | free-marks entries nothing moved | assert pass-2 entries are genuinely rebased in a scoped run |
| R9 | Checkout preflight after the lock | leaves a lock file behind on a refused run | AC 55: inspect the lock path *and* prove a subsequent `AcquireCheckoutLock` succeeds immediately |
| R10 | Diverting checkout Git streams | `CombinedOutput` empties ⇒ conflict misclassified as `switch` | record-only wrapper mode for every checkout capture + AC 6's non-empty `rebase conflict: ` suffix assertion |
| R11 | Symlinked runtime path redirecting a `0600` write, or a symlinked payload/guard being *read* as authoritative state | privilege/path escape, forged cell | one `os.Lstat` per path inside `ClassifyExternalSyncState`, recorded as `LegacySymlink`/`PayloadSymlink`/`GuardSymlink`; payload and guard symlinks are never opened or trusted; package `cli` refuses (I18) from those facts without a second `Lstat`, scoped exactly per spec §3.6 steps 7–8 |
| R11b | Status mutating a process global to test liveness | flaky/parallel-unsafe status suite | liveness injected via `SyncClassifyOpts.Alive` from `AgentStatusOpts.Proc` (P5); AC 43 forbids touching `syncProcessAlive` |
| R12 | Guard read on the frozen no-flag path | changes the declared read set | AC 38's unreadable-mode `.sync-run.lock` beside a payload-absent run reproducing the golden |
| R13 | Import planting foreign runtime state | a freshly imported feature refuses `tws sync` | `isRuntimeState` two exact names + end-to-end import test |
| R14 | Status mutating or hiding state | false alarms, byte drift | classifier is read-only; AC 44 byte comparison; cell 7/10 delegate to today's code |
| R15 | Network reached under `no-fetch` | breaks the core promise | `os.RemoveAll(remote)` fixtures + argv log with zero `fetch`/`ls-remote` records |
| R16 | Host `GIT_CONFIG_*` baked into a permanent golden | goldens rot / fail on CI | `t.Setenv("GIT_CONFIG_COUNT","0")` at test scope **and** in every `cmd.Env` (§10.6) |
| R17 | Transient `.tws-state-*` in the feature directory (P7) | snapshot flakes | ignore-pattern in every state/file-set snapshot (spec §17.3); AC 40 scoped to "no partial `.sync-state.yaml`" |
| R21 | A further existing test file silently edited | review surprise, drifting minimality claim | spec §13.6 names the **three** allowed edits (`checkout_lifecycle_test.go`, `sync_continue_integration_test.go`, `checkout_sync_test.go`) and §17.1 forbids parameterizing the `stack_status_test.go` golden helpers |
| R18 | Legacy checkout layout in a new fixture (P10) | transaction written outside `.tws/` | fixtures use `.tws/features/<feature>` only |
| R19 | Concurrent no-flag + new-mode run | documented, not closed | README/skill note; never described as fixed (§8.8) |
| R20 | `RunDirClean` filter left untested | the one tws-owned rebase transformation is unpinned | new `internal/exec_clean_test.go` **NEW** outside the wrapper |

---

## 14. Files, docs, skills, and untouched zones (task item 9)

### 14.1 Complete file ledger

**New production (3):** `internal/sync_selection.go`, `internal/sync_run_state.go`,
`internal/cli/sync_modes.go` — all **NEW**.

**Changed production (9):** as listed in §3.3.

**New tests (6, all NEW):** `internal/sync_selection_test.go`, `internal/sync_run_state_test.go`,
`internal/exec_clean_test.go`, `internal/cli/sync_modes_test.go`,
`internal/cli/sync_golden_test.go`, `internal/cli/sync_scoped_test.go` (behavioural matrix; may be
split further, e.g. `sync_state_matrix_test.go`, `sync_downgrade_test.go`,
`checkout_sync_modes_test.go`, and `push_layout_test.go` for AC 59's guard/mode-branch regressions,
including the `1 + N` → **2** workspace-root resolution **event** count assertion, its `N = 1`
(count unchanged) and empty-stack (one event added) boundary cases, and the separate mutating-push
argv/ref/output parity assertion of spec §4.1 rule 6c).

**Changed tests (3, and exactly three):**

1. `internal/cli/checkout_lifecycle_test.go` — two rows in `TestExport_RuntimeStateExcluded`;
2. `internal/cli/sync_continue_integration_test.go` — the two direct
   `handleSyncContinue("feature", featurePath, false)` calls at lines 34 and 64;
3. `internal/cli/checkout_sync_test.go` — the one direct `syncFeature("external-feature", false)`
   call in `TestCheckoutSync_ExternalSyncUnchanged` at line 1187.

Edits 2 and 3 are forced by the P1 signature changes and are **mechanical**: construct the agreeing
layout from the path the test already derives (`p := internal.FeaturePath(<feature>)`,
`externalSyncLayout{FeaturePath: p, WorktreesRoot: filepath.Join(p, "worktrees")}`) and pass it; no
assertion, fixture, or expectation changes in either file. `internal/cli/stack_status_test.go` is
**not** changed: its golden helpers are called, never parameterized (spec §17.1).
`internal/cli/space_guard_test.go` is **not** changed either — it is the regression detector for the
`pushCmd` guard (§11.1, AC 59), so any need to edit it means the guard was moved or dropped.
Existing status/agent tests should need **no** edit if cell 7 and cell 10 delegate.

**New testdata (NEW):** `internal/cli/testdata/sync_noflag/**` including `declared_c1..c4/`.

**Changed docs (7):** `README.md` (sync example block near line 64, command table row near line 127),
`docs/cheatsheet.md` (sync section near line 148),
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` (the "Manage the stack" block near
line 50), `assets/skills/copilot/tws.prompt.md` (signature line 18, checkout paragraph line 80),
`CHANGELOG.md` (`## Unreleased`, currently owned by `stack-status`), **`docs/roadmap.md`** (move
sync modes from the P1 backlog into the shipped list and retarget the "Current target" line), and
**`docs/engineering-workflow.md`** (append sync modes as the next shipped item and update the "Next
roadmap feature" line). The last two are required by spec §14 and were omitted from the earlier
count of five (P4).

### 14.2 `go:embed` constraint

`assets/skills/embed.go` embeds exactly three files:
`claude/tesseraworkspaces/SKILL.md`, `claude/tesseraworkspaces-orchestrator/SKILL.md`,
`copilot/tws.prompt.md`. Two of the three are edited by §14, so the embedded bytes change. No test
asserts skill content or a hash (verified by grep), and `//go:embed` paths are literal filenames, so
no directive changes. `assets/skills/claude/tesseraworkspaces/SKILL.md` is **not** in §14's list and
should stay untouched unless its sync section is found to be wrong.

### 14.3 Collision zones

- `## Unreleased` in `CHANGELOG.md` already contains the `stack-status` block — append a new
  section rather than editing that one.
- `.tpatch/FEATURES.md` has an **uncommitted** modification and
  `.tpatch/features/open-worktree-command/` is an **untracked**, newly registered feature. Neither
  belongs to `sync-modes`; the implementation commit must not absorb them.
- `internal/cli/testdata/existing_commands/**` is a frozen golden tree in the same `testdata`
  parent as the new `sync_noflag/**`. `goldenCompareOrWrite`/`goldenGoldenPath` hard-code the
  `existing_commands` segment and MUST NOT be parameterized (spec §17.1): the sync harness declares
  its own `sync_noflag/`-rooted path builder and compare-or-write, so `stack_status_test.go` stays
  unchanged.
- `internal/cli/checkout_sync_test.go` and the new checkout mode tests both toggle
  `internal.StepHook`; use the existing `clearStepHook(t)` helper so `t.Cleanup` restores it.

### 14.4 Explicitly untouched (from §13.5, all verified to exist)

`internal/cli/export.go`, `internal/stack_ancestry.go`, `internal/stack_status.go`,
`internal/cli/stack_status.go`, `internal/cli/doctor.go`, `internal/checkout_health.go`,
`internal/cli/list.go`, `internal/cli/archive.go`, `internal/health.go`, `internal/stack.go`,
`internal/cli/root.go`, `internal/paths.go`, `internal/workspace.go`, `internal/resolve.go`,
`internal/config.go`, and every `space`/`registry`/`session`/`template` file.
`internal/cli/new.go` changes by exactly one delegating line.

**Confirmed by the settled P1/P15 decisions:** the resolver lives in package `cli`, calls **none**
of `internal.TwsRoot` / `internal.FeaturePath` / `internal.WorktreePath` / `RequireWorkspace`
itself, and consumes only the `ws` and `twsRoot` values its callers already resolved (the commands
still call `internal.TwsRoot` and `ws.ResolveFeaturePath` unchanged, just once each), so
`internal/workspace.go`,
`internal/resolve.go`, and `internal/paths.go` stay in this list — byte-identical, grep-asserted by
AC 51, and adding no dependency edge. The rejected option 2 (teaching
`ResolveCurrentWorkspaceE` about `TWS_ROOT`) would have removed them and added a
workspace-resolution hard parent — which is exactly why it was rejected (spec §15, §21 item 13).

---

## 15. Dependency verification (task item 10)

Verdict first: **every one of the 16 hard and 9 soft edges in `status.json` is justified by a real
symbol this feature touches or consumes, no edge is missing, and no edge should be added or
re-kinded.** I registered nothing. **The settled P1/P2/P5 decisions do not change this verdict**:
the layout resolver is new package-`cli` code over unchanged `internal` helpers; the ordering
decision explicitly avoids touching `TopoSort`; and the liveness seam extends a struct this feature
already introduces while consuming `ProcessProber` from `checkout-stack-safety` /
`agent-work-status-dashboard`, both already hard parents.

`worktree-health-check` — the one soft parent not named in `status.json`'s obvious order — exists as
`.tpatch/features/worktree-health-check/` and is `applied` per `.tpatch/FEATURES.md` line 93, so the
edge is not dangling.

**Hard edges, verified against provenance:**

| Parent | Symbol this feature actually modifies or extends |
|---|---|
| `keep-track-of-stacked-diffs-and-dependencies` | `internal/stack.go`: `TopoSort`, `Descendants`, `GetBranch`, `Stack`, `StackEntry` — filtered by `ResolveSyncSelection` |
| `sync-continue` | `internal/syncstate.go` `SyncState` + `handleSyncContinue`/`handleSyncAbort` — extended by the sentinel/payload contract |
| `amend-aware-rebase` | `StackEntry.LastBaseSHA` + the `--onto <base> <LastBaseSHA>` branch in `syncWithStackFiltered` and `doRebase` — preserved in every cell |
| `checkout-stack-safety` | `CheckoutTransaction`, `CheckoutPlanEntry`, `BuildCheckoutPlan`, `AcquireCheckoutLock`, `restoreOriginal`, stage enum — all extended |
| `branch-name-decoupling` | `StackEntry.Branch` / `GitBranch()` — drives selector identity, `pushSelected`, C2, C3, I13 |
| `multi-repo-workspaces` | `StackEntry.Repo`, `UniqueRepos`, `sameStackRepo` (moved to `internal.SameStackRepo`) |
| `fix-default-base-branch` | `DefaultBranch`/`DefaultBranchIn` + `resolveBase`'s `origin/<default>` rewrite — re-scoped by C4 |
| `archive-worktree` | pass 2 of `syncWithStackFiltered`, `IsPrunableWorktree`, `StackEntry.Archived` |
| `quiet-fetch-output` | `fetchQuiet`'s `Fetching …/done/failed` bytes and fetch tolerance |
| `cobra-migration` | `syncCmd` `RunE`, `cmd.Flags().Changed`, `ValidArgsFunction` |
| `fix-sync-continue-descendants` | `staleStackEdges` (→ `staleStackEdgesFiltered`) and `branchContainsConfiguredParent` |
| `push-branches` | `pushFeature` argv (C2) + new `pushSelected` |
| `fix-checkout-feature-path-routing` | `cli.runCheckoutSync`'s `RequireFeaturePath` routing under the new `RepoDir`/I19 rules |
| `fix-external-feature-dir-resolution` | `RequireWorkspace`'s external fallback + `internal/cli/external_feature_dir_test.go` — the external half of C4, now the single layout resolver of spec §3.11 |
| `agent-work-status-dashboard` | `buildFeatureSync`, `attributeSyncBranch`, `syncWantsBranch`, `AgentStatusFeatureSync` |
| `checkout-workspace-lifecycle` | `isRuntimeState` and its committed table test |

**Soft edges, verified as consumed-unchanged:** `fix-sync-branch-identity`
(`checkSyncWorktreeBranch` → `internal.CheckWorktreeBranch`), `post-rebase-validation`
(`runValidation`, both variants), `divergent-stack-sync` (fixtures), `stack-ancestry-doctor`
(`StackBasePolicy`, `StackBasePolicyForMode`, `StackEdge` — cited, not modified),
`stack-status` (read-only contract untouched), `worktree-health-check`
(`internal.CheckWorktreeBranch`, `internal/health.go`), `clean-git-output` (`RunDirClean` /
`runWithFilteredStderr` consumed verbatim), `fix-missing-completions`
(`RegisterFlagCompletionFunc` alongside `ValidArgsFunction`), `skill-distribution`
(`assets/skills/**` embedded set).

**Newly proven missing direct edge: none.** Two near-misses examined and rejected:

- `workspace-sibling-links` — `GuardFeatureName` is consumed unchanged in `syncCmd`, in `pushCmd`,
  and inside the checkout wrapper's **unchanged** `internal.RequireFeaturePath` call; it
  becomes hard only if the guard or the sibling-space listing is modified, which §13.5 forbids.
  The settled P1 decision keeps it soft-by-omission: because the resolver lives in package `cli` and
  changes no workspace-resolution symbol, **no** new edge is required (spec §15 records this
  explicitly). Option 2 would have touched `ResolveCurrentWorkspaceE` and made a
  workspace-resolution parent (`workspace-mode-foundation` /
  `fix-external-feature-dir-resolution`) a directly-modified parent — one of the reasons it was
  rejected.
- `versioned-builds` / release tooling — the downgrade harness *reads* `refs/tags/v1.2.14` in tests
  but modifies nothing the feature owns.

`tpatch feature deps --validate-all` must still be run by the parent agent before implementation
(AC 52); I did not run any tpatch command.

---

## 16. Implementation sequence (task item 11)

Each step ends with a compile/test checkpoint. Steps 1 and 2 must not be reordered.

| # | Step | Files | Checkpoint |
|---|---|---|---|
| 0 | **DONE** — P1–P17 settled in writing; the spec sections named in §12 were amended (§3.11 layout resolver, §3.7/§5.2 ordering, §11.1/§17.4 liveness seam, §11.1 symlink facts, §13.6 counts, AC 58). The iteration-3 review then amended §3.11 (the `pushCmd` guard-first / mode-branch order and `pushFeatureCheckout`), §7.6, §13.3, §13.4, §13.6, §14, §17.2, §18 item 11, AC 33, AC 45, AC 51, and added **AC 59**. The latest revision added §3.11's **anchored** resolution-event grouping rule with its standalone-`LoadConfig`-probe rule (P15) and restored `runCheckoutSync`'s `internal.RequireFeaturePath` call (P16), amending §3.6 step 4, §4.1 rule 6a/6c, §4.3, §10.1, §10.3, §10.9, §13.3, §13.4, §17.1 mode 3, AC 2, AC 46, AC 51, AC 58, AC 59 | `.tpatch/features/sync-modes/spec.md` | reviewer sign-off; no code yet |
| 1 | **Pre-change goldens** — harness + fixtures + captures + declared-change evidence, against the untouched production tree | `internal/cli/sync_golden_test.go` **NEW**, `internal/cli/testdata/sync_noflag/**` **NEW** | `go test ./internal/cli/ -run SyncGolden -count=1` green twice in a row; `git status` shows **only** test/testdata additions |
| 2 | Lowest-level types: `internal/sync_selection.go` + `SameStackRepo` delegation | + `internal/cli/new.go` | `go test ./internal/ -run SyncSelection`; `go build ./...` |
| 3 | State machine: `internal/sync_run_state.go` (types, paths, load/save/delete, guard, `isSyncMarker`, classifier, `SyncStepHook`, liveness seam) + atomic `SaveSyncState` | + `internal/syncstate.go` | `go test ./internal/ -run 'SyncRunState|SyncMarker'`; existing `internal` suite green |
| 4 | CLI plumbing: `internal/cli/sync_modes.go` (policy/presence, I1–I8, marker, `classifySyncState`, message table, completion) with flags registered but `RunE` still on the old path | + `internal/cli/sync.go` (flags + completions only) | help snapshot (AC 3); `go test ./internal/cli/ -run SyncModes` |
| 4b | **Layout resolver first, alone**: `externalSyncLayout` + `resolveExternalSyncLayout(ws, twsRoot, feature)`, the single hoisted `twsRoot := internal.TwsRoot()` in `syncCmd.RunE` / `pushCmd`'s external arm, and the mechanical threading of `featurePath`/`worktreesRoot` through `syncFeature`, `syncWithStackFiltered`, `staleStackEdgesFiltered`, `branchContainsConfiguredParent`, `handleSyncAbort`, `handleSyncContinue`, `saveIncompleteSync`, `markUpdatedAncestors`, `syncFallback`, and completion; **plus the `pushCmd` restructure** — `RequireWorkspace` + `GuardFeatureName(ws.MetadataRoot, feature)` before the resolver, then the `ws.Mode` branch, with `pushFeatureCheckout` holding today's body verbatim and `pushFeature` taking the layout (spec §3.11) — no policy, no selection, no state machine, and **no C2 ref change** in this step | `internal/cli/sync_modes.go`, `sync.go`, `sync_helpers.go`, `push.go`, + the **two** forced call-site edits in `internal/cli/sync_continue_integration_test.go` (`handleSyncContinue`, lines 34 and 64) and `internal/cli/checkout_sync_test.go` (`syncFeature` in `TestCheckoutSync_ExternalSyncUnchanged`, line 1187) | step-1 **output** goldens, exit codes, state files, and refs **unchanged** (agreeing layouts are a no-op); the argv sidecars differ **only** by the §4.1 rule 6c resolution-event compression (`3 + N + E` → 2), which this step is expected to produce and which the §17.1 mode-3 comparator must group into events, not records; new AC 58 divergent-layout test green; AC 59 green with `internal/cli/space_guard_test.go` **unedited**; `-run 'SpaceGuard|SyncContinue|SyncBranchIdentity|ExternalFeatureDirectory|CheckoutSync_ExternalSyncUnchanged'` |
| 5 | External execution: `RunE` ordering, cell dispatch, scoped `syncFeature`/`syncWithStackFiltered`, `staleStackEdgesFiltered` selection filter, `resolveBase(base, repoCtx)`, `pushSelected`, `pushFeature` C2 (external helper only — `pushFeatureCheckout` keeps `entry.Name`) | `internal/cli/sync.go`, `sync_helpers.go`, `push.go` | golden suite from step 1 **unchanged**; AC 59 still green; `-run 'SpaceGuard|SyncContinue|SyncBranchIdentity|ExternalFeatureDirectory'` |
| 6 | Checkout: `runCheckoutSync(ws, opts)` (`internal.RequireFeaturePath` kept verbatim, second `RequireWorkspace` event included — P16), opts/plan/transaction fields, preflight I9–I14, header, `--fetch` window, `printLocalOnlyNoOp`, C3+C5, I19/I20/deferred I7 | `internal/checkout_sync.go`, `internal/cli/checkout_sync.go` | `-run CheckoutSync` (all 26 existing tests) + new checkout mode tests |
| 7 | Status + import | `internal/agent_status.go`, `internal/cli/importcmd.go`, `internal/cli/checkout_lifecycle_test.go` | `go test ./internal/ -run AgentStatus`; `existing_commands` goldens unchanged |
| 8 | Remaining tests: state matrix, downgrade, cwd matrix, axes/scopes, `RunDirClean` regression | new `*_test.go` | full `go test ./... -count=1` |
| 9 | Docs, skills, CHANGELOG, roadmap, engineering workflow | 7 files (§14.1) | `make build`; manual `tws sync --help` read-through |
| 10 | Full gates, then Path B land | — | §17 below |

Steps 4b and 5 are deliberately separate: 4b is a pure plumbing change that must leave every step-1
golden byte-identical, so a golden diff there is unambiguously a routing bug rather than a scoped-run
bug.

**Keeping one logical feature commit (per `.tpatch/steering/local.md`).** Implement steps 1–9
without committing; when green, commit the production + test + doc changes as **one** commit with
no `.tpatch/` files; then `tpatch record sync-modes --from HEAD~1` to generate
`patch-recipe.json`, extend its descriptions, verify with `apply`, and commit the `.tpatch/`
metadata separately as `chore(tpatch): …`. The exploration phase itself is advanced with
`tpatch explore sync-modes --manual` — **not run by this agent**.

---

## 17. Validation commands

```bash
# focused, during implementation (see §11.5 for the per-step selectors)
go test ./internal/ -run 'SyncSelection|SyncRunState|SyncMarker|RunDirClean' -count=1
go test ./internal/cli/ -run 'SyncModes|SyncGolden|SyncScoped|CheckoutSync' -count=1

# full gates (docs/engineering-workflow.md)
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Baseline measured on this machine: `GIT_CONFIG_COUNT=0 go test ./... -count=1` → PASS,
`internal` 48 s, `internal/cli` 71 s. Because production Git calls inherit the process environment,
run the suite with `GIT_CONFIG_COUNT=0` on any host that injects Git config (this one does).

Feature smoke tests worth running by hand against a scratch workspace:
`tws sync <f> --only <e>`, `tws sync <f> --from <e> --local-only`, `tws sync <f> --no-fetch`,
`tws sync <f> --continue` with and without trigger flags, `tws sync <f> --abort` in each residue
shape, and `tws status <f> --json` while a payload is on disk.

---

## 18. Explicit non-changes

- No new command, no new package, no new dependency, no `go.mod`/`go.sum` change.
- `internal/cli/root.go`'s command list is untouched.
- `agentStatusSchema` stays `1`; no new key and no new enum value in `AgentStatusFeatureSync` or the
  issue-code block.
- `internal/stack.go` (`TopoSort`, `Descendants`, `GetBranch`, `HasBranch`, `UniqueRepos`,
  `UpdateBaseSHA`, `PrintTree`) is unchanged, unconditionally: **P2 is settled without any sort
  change** — no tie-break inside `ResolveSyncSelection` either (spec §3.7, §5.2, AC 51).
- `internal/workspace.go`, `internal/resolve.go`, and `internal/paths.go` are unchanged — **byte for
  byte**, grep-asserted by AC 51: `internal.TwsRoot`, `internal.FeaturePath`, and
  `internal.WorktreePath` keep their bodies, signatures, and Git behaviour, and only their *call
  sites* change (one `internal.TwsRoot()` call per external command, in package `cli`). The P1/P15
  layout resolver lives in package `cli`, takes the resolved root as a parameter, does pure
  filesystem probing, and calls none of them, so no workspace-resolution parent
  becomes a hard dependency and no dependency edge is added (spec §13.5, §15).
- `tws status`'s feature-path resolution is unchanged: the layout resolver is scoped to the sync
  path, and aligning the remaining workspace-rooted readers is a follow-up (spec §18 item 10).
- `internal/cli/export.go`'s allow-list is unchanged (one additive test assertion only).
- `internal/stack_ancestry.go` / `StackEdge` is cited as the M2/M3 authority and **not** collapsed
  into `staleStackEdges` (D6 defers it).
- `internal/cli/testdata/existing_commands/**` is not re-baselined.
- External mode gains no dirty/detached/lock guard (M15); external `--test` stays inert (D16);
  external `--abort` still performs no branch rollback (M10).
- **Checkout-mode `tws push` is unchanged**, failure included: it keeps `internal.RequireFeaturePath`
  + `internal.RequireWorktreePath` inside `pushFeatureCheckout` and still exits nonzero with
  `linked worktrees are not supported in checkout mode`. Neither the layout resolver nor the C2 ref
  fix reaches it, and `internal/cli/space_guard_test.go` is not edited (spec §3.11, AC 59).
- `.github/skills/tessera-patch/SKILL.md` and `.claude/skills/tessera-patch/SKILL.md` are tpatch
  skills, not tws documentation — unchanged.
- **This exploration authored exactly one file**, `.tpatch/features/sync-modes/exploration.md`. The
  pre-existing uncommitted `.tpatch/FEATURES.md` modification and the untracked
  `.tpatch/features/open-worktree-command/` registration were read but not modified. No tpatch
  lifecycle command, commit, push, tag, or land was run. All scratch artifacts created during
  probing (`.explore-scratch/`, `internal/zz_topo_probe_test.go`,
  `internal/cli/zz_probe_test.go`) were removed.
- **The revision of 2026-08-15 that folded the independent review's corrections into P1–P14 touched
  exactly two files**, `.tpatch/features/sync-modes/spec.md` and this document. No metadata,
  production, test, documentation, or `open-worktree-command` file was modified, and no tpatch
  lifecycle, commit, push, or tag command was run.
- **The iteration-3 revision touched the same two files and nothing else.** It resolved three
  blockers: the standalone-push sibling-space guard (`pushCmd` guards with
  `internal.GuardFeatureName(ws.MetadataRoot, feature)` before `resolveExternalSyncLayout`), the
  checkout-mode `tws push` refusal (preserved verbatim in `pushFeatureCheckout`, with C2/C4 claims
  and the changelog rescoped to external mode), and the third forced mechanical test edit
  (`internal/cli/checkout_sync_test.go`'s direct `syncFeature` call), bringing the forced existing
  test edits to exactly three. No lifecycle, commit, push, tag, or land command was run.
- **The `1 + N` revision touched the same two files and nothing else.** It corrected one verified
  call-graph fact and every compatibility assertion that depended on it: today's external
  `pushFeature` performs **`1 + N`** workspace resolutions over a stack of `N` entries (one
  `internal.RequireFeaturePath`, plus one `internal.RequireWorktreePath` per entry, each re-entering
  `RequireWorkspace`), not one. Its claim that the post-change path resolves "exactly once" was
  itself wrong and is superseded below.
- **The hidden-resolution revision (P15) touched the same two files and nothing else.** It fixed
  three coupled defects in the previous wording. (i) `resolveExternalSyncLayout(ws, feature)` would
  have called `internal.TwsRoot()` internally to build candidate B, so the "Git-free" resolver
  hid a full resolution; it now takes `twsRoot` as a parameter, builds candidate B with
  `filepath.Join(twsRoot, feature)`, and is grep-asserted free of every root/workspace resolver and
  of `exec` (spec §3.11, AC 51). (ii) A workspace-root resolution is an ordered **pair** of records
  (`rev-parse --show-toplevel` + `-C <cwd> rev-parse --git-common-dir`, reversed for `TwsRoot`),
  verified by running the built binary under a logging `git` wrapper, so the carve-out is now
  defined over `workspaceRootResolutionEvent`s and the counts are stated honestly: external sync
  `3 + N + E` → **2** (which **does** reach the frozen AC 1 external captures), external push
  `1 + N` → **2** (unchanged in count at `N = 1`, one event added on an empty stack), checkout push
  **+1** `RequireWorkspace` event and never a `TwsRoot` event. (iii) The false claims "exactly one
  workspace resolution" and "only `N` `--git-common-dir` records" were removed from §3.11, §4.1
  rule 6c, §4.5 C4, §12.11, §13.4, §14, AC 2, AC 33, AC 46, AC 51, AC 58, AC 59, and §17.1 mode 3
  (whose carve-out constant is now `c4ResolutionCompression`), and each external command now
  resolves `RequireWorkspace` + `internal.TwsRoot()` exactly once, with `pushCmd`'s guard ordering
  and the checkout arm's zero-`TwsRoot` property preserved. No behaviour design changed, and
  `internal/paths.go` / `internal/workspace.go` / `internal/resolve.go` stay untouched, so no
  dependency edge is added. No lifecycle, commit, push, tag, or land command was run.
- **The grouping/second-resolution revision touched the same two files and nothing else.** It fixed
  two blockers. (i) **Robust event grouping (P15).** The sidecar grouping is now anchored on each
  `git -C <cwd> rev-parse --git-common-dir` record and never on a bare `rev-parse --show-toplevel`,
  with the anchors visited in reverse log order: forward-pair to an immediately following unconsumed
  bare show in the same cwd (a `TwsRoot` event), otherwise backward-pair with the immediately
  preceding unconsumed bare show (a `RequireWorkspace` event), otherwise fail. Every unpaired bare
  `rev-parse --show-toplevel` is a **standalone `internal.LoadConfig` probe** — `runValidation`
  emits one per validated entry — which stays in the ordered non-event log and is compared verbatim
  and in position; it may not be removed or normalized. This is what makes the measured
  `standalone show → common → show` adjacency group correctly, and the claim that no other bare
  `rev-parse --show-toplevel` exists on these paths was removed (spec §3.11, §4.1 rule 6c,
  §17.1 mode 3, AC 2, AC 51, AC 58, AC 59). (ii) **Checkout's second resolution is preserved
  (P16).** `cli.runCheckoutSync` keeps `internal.RequireFeaturePath(feature)` exactly as it ships —
  second `RequireWorkspace` event, guard, layout resolution, and error semantics included — and is
  **not** rerouted through `internal.ResolveFeaturePathFor`; the outer `ws` supplies only
  `ws.RepoRoot` (and the mode decision) so no third resolver appears. The containment probe follows
  that unchanged resolution and precedes `RunCheckoutSync`'s preflight, so the checkout pre-change
  resolution prefix survives verbatim and the one added probe remains the path's only argv
  difference; every claim of avoiding a second `RequireWorkspace` was removed (spec §3.6 step 4,
  §4.1 rule 6a, §4.3, §10.1, §10.3, §10.9, §13.3, §13.4 rule 1, AC 2, AC 46, AC 51). R21 and P4 were
  also corrected from two to **three** allowed existing test edits, matching spec §13.6 and §14.1.
  No behaviour design changed. No lifecycle, commit, push, tag, or land command was run.
- **The inference-probe revision (P17) touched the same two files and nothing else.** It fixed one
  blocker: the grouping rule anchored on *every* `git -C <path> rev-parse --git-common-dir` record
  and declared that such a record is never left ungrouped, which would have failed every capture
  taken from cwd cells 4–6 — including the frozen cell-4 external sync goldens. Measured against the
  built binary in a real external workspace, `RequireWorkspace`'s fallback arm emits one
  `MainRepoRootIn` probe per `inferExternalRepoRoot` candidate (configured workspace keys in Go map
  order, the `.tws` sibling repo, and every materialized entry of every feature): four such records
  on a three-entry feature from the workspace root, the feature directory, and a nested subdirectory
  alike, and **zero** from the repository root or a linked worktree. The anchor is now defined by the
  **operand** — a `--git-common-dir` record anchors an event iff its `-C` operand is that record's
  own process cwd — and all other `--git-common-dir` records are ordinary **non-event** records:
  never grouped, never counted toward the `3 + N + E` / `1 + N` / `2` budgets, compared verbatim and
  in position, `C + S + M` in number (so never pinned as a constant), unordered whenever two or more
  configured workspaces map to one metadata root (three runs, three orders — such fixtures may not be
  byte-pinned), and removable **only** together with a `RequireWorkspace` event that §4.1 rule 6c
  removes, which happens on the push path alone (`1 + N` blocks → 1; external sync keeps its single
  invariant block). The layout refactor does not remove them, because `RequireWorkspace` still runs
  before layout resolution and `internal/workspace.go` stays byte-identical. The pair algorithm is
  unchanged (reverse-order walk, forward `TwsRoot`, backward `RequireWorkspace`, unpaired bare show =
  standalone `LoadConfig` probe), and the "otherwise fail" rule is now explicitly a fail-closed guard
  rather than a reachable state (spec §3.11, §4.1 rule 6c, §17.1 mode 3, §17.2, AC 2, AC 46, AC 51,
  AC 58; exploration §2.1, §10.4, §12 P17, §13 R1c). No behaviour design changed. No lifecycle,
  commit, push, tag, or land command was run.
