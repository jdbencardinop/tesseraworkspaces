# Specification — stack-status

Approved analysis: [`analysis.md`](analysis.md). Every decision below is settled; nothing is
deferred to implementation discretion. Where production-code inspection required a more precise
contract than the analysis prose, the precision is stated inline and marked **(sharpened)** with the
approved intent it preserves.

## 1. Problem statement

`tws stack <feature>` prints an ASCII dependency tree and nothing else. It answers "what is the
shape of this stack" and cannot answer any of the questions an operator or an orchestrating agent
actually has before syncing:

- where is each branch's local head, and where is its configured parent's head;
- what did the last sync record as the base (`last_base_sha`), and is the edge still `current`;
- is the branch materialized at all — a real linked worktree, a bare ref, archived, prunable, or
  gone;
- is that materialization dirty or mid-rebase;
- does the branch have an upstream, and is it equal / ahead / behind / diverged / gone;
- how far ahead and behind its *parent* is it.

The shipped `StackEdge` evaluator (`internal/stack_ancestry.go`) already owns branch/base identity,
ref probing, heads, merge base, recorded-base verdict, ancestry status, reason, severity, guidance,
notes, and repository source. Nothing in it is exposed as machine output: `tws doctor` and
`tws list` render prose, and `tws status --json` deliberately carries no ancestry key
(`agent-work-status-dashboard` spec §5.6).

`stack-status` is therefore a **projection and presentation** feature. It adds
`tws stack status <feature> [--json]`, joins the shipped ancestry projection with one branch-ref
inventory, one worktree inventory, nullable dirty/active-operation probes, and one parent
ahead/behind count per eligible row — and computes **no ancestry of its own**.

## 2. Goals

1. Add `tws stack status <feature> [--json]` without changing successful `tws stack <feature>`
   output bytes, exit code, cycle warning, or error strings.
2. Emit one versioned, stable-key JSON document (`schema_version: 1`) whose successful shape is
   fully specified here, with no `omitempty`, no generated timestamp, and no `stack_state`.
3. Project every `StackEdge` field verbatim — including `RefProbed`-gated ref existence — so that
   status can never contradict `tws doctor` for the same fixture.
4. Report upstream state from exactly one full-ref `for-each-ref` inventory that is fail-closed and
   immune to branch/tag name collisions.
5. Report materialization, checked-out branch, and detached state from exactly one complete,
   path-keyed `git worktree list --porcelain` inventory, extended additively so its existing
   production consumer (`BuildAgentStatus` → `tws status`, §9.1)
   keeps its behaviour on real, well-formed Git output — of any object format, SHA-1 or SHA-256
   (§8.3.1) — and on ordinary command failures, with one
   explicit, documented fail-closed hardening on malformed porcelain (§9.2).
6. Report dirty and active-Git-operation state through **error-returning** probes whose failure is
   `null`, never a fabricated clean/attached/no-operation value.
7. Be strictly read-only, local-only, and no-fetch; run `git status` with `GIT_OPTIONAL_LOCKS=0` so
   even the index is untouched.
8. Be deterministic: two runs over an unchanged repository produce byte-identical human and JSON
   output.
9. Stay within a stated process budget of `A + 2 + C + D` at the `BuildStackStatus` boundary, and
   `I + A + 2 + C + D` end-to-end once the CLI's own `RequireWorkspace`/`LoadConfig` prerequisites
   are counted (§13), so the command cannot silently grow a per-row Git probe.

## 3. Non-goals

Explicitly **not** in this feature, and no file, flag, test, or doc may introduce them:

- any `git fetch`, `ls-remote`, or remote freshness claim, and no `--fetch` flag;
- any mutation: no ref write, no index write, no `stack.yaml` save, no metadata write, no lock;
- any change to `StackEdge` semantics, the `AncestryStatus` enum, the reason/severity/guidance
  vocabulary, or `internal/stack_ancestry.go` classification;
- any ancestry recomputation, second ref-existence probe, or second repository resolution;
- patch identity, `git patch-id`, successor tracking, or any tesserapatch contract
  (`tpatch-patch-identity-research` stays a deferred child, §22);
- sync, restack, reparent, or rebase-plan behaviour;
- schema or behaviour changes to `tws status`, `tws doctor`, or `tws list` — with the single named
  exception of §9.2: on **malformed** worktree porcelain (shim-only; unreachable from real Git) the
  shared inventory now fails closed instead of returning partial maps, so its one production
  consumer — `BuildAgentStatus`, and therefore `tws status` (§9.1) — takes its
  existing unavailable-inventory branch. `tws doctor` and `tws list` do not read that inventory at
  all. No schema key, issue code, severity, message, or exit code
  changes anywhere;
- moving legacy `tws stack` output off `os.Stdout` onto Cobra writers;
- cycle detection, topological sorting, root grouping, alphabetical tie-breaks, or deduplication in
  the status report;
- unrelated cleanup or refactoring of `internal/checkout_health.go`, `internal/agent_status.go`, or
  `internal/stack.go` beyond the adaptations named in §17.

## 4. Command contract

### 4.1 Registration

`internal/cli/stack.go` keeps `stackCmd()` and its `RunE` **unchanged**, and gains exactly one line:

```go
cmd.AddCommand(stackStatusCmd())
```

`stackCmd()` therefore becomes a variable-returning constructor rather than a single composite
literal. Its `RunE` is **unchanged**, and exactly two command-construction statements change:

1. the added `cmd.AddCommand(stackStatusCmd())`;
2. the parent `ValidArgsFunction`, which drops an exact `status` element from the list it returns
   (§4.3).

Nothing else in `stackCmd()` changes. `internal/cli/root.go` is unchanged — `stackCmd()` is
already registered there (`internal/cli/root.go:28`).

`stackStatusCmd()` lives in a new file `internal/cli/stack_status.go`:

```go
Use:   "status <feature>"
Short: "Show stack ancestry, materialization, and upstream status"
```

Flag: `--json` (`BoolVar`, default false, usage `"Output as JSON"`) — identical registration style
to `statusCmd` (`internal/cli/status.go:92`).

### 4.2 `Args` — the exact validator

```go
Args: stackStatusArgs
```

```go
func stackStatusArgs(cmd *cobra.Command, args []string) error
```

Normative behaviour, in this order:

1. `len(args) == 1` → `nil`.
2. `len(args) > 1` → return `cobra.ExactArgs(1)(cmd, args)` **verbatim**, so the byte text
   (`accepts 1 arg(s), received 2`) is Cobra's own and can never drift.
3. `len(args) == 0`:
   a. call `internal.ListFeaturesE()`;
   b. if it returns an error, return `cobra.ExactArgs(1)(cmd, args)` verbatim. **(sharpened)** The
      user's actual mistake is the missing argument; argument validation must not convert a
      workspace/spaces fault into a usage-suppressed failure, and that fault surfaces immediately
      and with its real message as soon as an argument is supplied. The approved intent — "check
      feature names through the error-returning runtime listing path" — is preserved: the
      error-returning path is used, its error is simply not the answer to "you gave me no
      argument";
   c. if the returned list contains the exact string `status`, return this error, verbatim and as a
      single line:

      ```text
      accepts 1 arg(s), received 0: a feature named "status" exists; run "tws stack status status" for its stack status report, or "tws stack -- status" for its legacy dependency tree
      ```

   d. otherwise return `cobra.ExactArgs(1)(cmd, args)` verbatim.

The hint lives **only** here. Cobra selects the `status` child before the parent's `RunE`, so the
parent can never see the collision; the legacy `RunE` gains no collision-specific branch.

`tws stack -- status` remains the escape hatch and reaches the legacy tree unchanged (Cobra stops
flag/command parsing at `--`). `tws stack status status` is valid and reports the stack status of
the feature literally named `status`.

### 4.3 Completion

- **Child**: `ValidArgsFunction` returns `internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp`
  for `len(args) == 0`, and `nil, cobra.ShellCompDirectiveNoFileComp` otherwise. It does **not**
  filter the name `status`, so `tws stack status status` stays discoverable.
- **Parent**: the existing `ValidArgsFunction` is changed only to drop an exact `status` element
  from the returned list, because Cobra already contributes the `status` subcommand at that
  position and two candidates spelled identically with different meanings is worse than one.

Accepted, documented discoverability cost: at the parent position, ordinary completion no longer
suggests a legacy feature named `status`. §18 requires the help text, the cheatsheet, and the skills
to advertise `tws stack -- status`; typing it directly always works.

### 4.4 Help text (exact `Long`)

```text
Report, for every entry of a feature's stack in stack.yaml order: its logical
name and Git branch, local head, configured base and parent head, the recorded
last_base_sha verdict, ancestry state, materialization, dirty and in-progress
Git operation, upstream state, and ahead/behind counts against the parent.

Ancestry comes from the shared evaluator that 'tws doctor' and 'tws list' use,
so the same fixture always reports the same state. This command computes no
ancestry of its own.

Local-only. Nothing is fetched, written, or refreshed: upstream state describes
the configured upstream ref as it exists in this repository right now, and
parent counts compare local commits. A fact that cannot be established locally
is reported as unknown (null in JSON), never as clean, attached, zero, or
"no upstream".

Exit status is 0 whenever a report was produced, including for stale, divergent,
missing, cross-repo, or unevaluated edges and dirty worktrees. A non-zero exit
means no report was produced at all, and nothing is written to stdout.

A feature literally named 'status' is still reachable: 'tws stack -- status'
prints its legacy dependency tree and 'tws stack status status' reports its
stack status.

--json prints one versioned document with a stable key set; absent values are
null and lists are never null.
```

### 4.5 Accepted legacy drift

Adding a child command changes two Cobra-generated surfaces, and both changes are accepted and
snapshot-pinned rather than denied:

1. `tws stack --help` gains an `Available Commands:` section listing `status`, and its usage line
   becomes `tws stack [command]`-shaped.
2. The usage block printed for a parent argument-arity error changes the same way.

Everything else about the legacy command is byte-identical: successful tree output, the
`Warning: cycle detected in stack.yaml` line, the `no stack.yaml found for feature: <f>` error, its
exit code, and the fact that all of it is written with bare `fmt` to process `os.Stdout`
(`internal/stack.go:207-277`, `internal/cli/stack.go:33-36`).

### 4.6 Writers

- Legacy `tws stack <feature>` keeps writing to process `os.Stdout`. Its tests use the existing
  `captureStdout` helper (`internal/cli/space_guard_test.go:15-41`).
- `tws stack status` writes **only** through `cmd.OutOrStdout()`: `json.Encoder` with
  `SetIndent("", "  ")` for `--json`, `fmt.Fprint(cmd.OutOrStdout(), internal.FormatStackStatus(r))`
  otherwise — the same shape as `statusCmd` (`internal/cli/status.go:82-87`).
- Nothing in the status path uses bare `fmt.Print*`.

### 4.7 `SilenceUsage` boundary

`RunE` sets `cmd.SilenceUsage = true` as its **first statement**, before any resolution. Therefore:

- Cobra argument-arity and unknown-flag errors (including the zero-arg hint of §4.2) keep their
  usage block, because they are raised before `RunE` runs;
- every runtime failure (§5) prints only `Error: <message>` with no usage block.

## 5. Fatal versus reportable (strict boundary)

**Fatal** — non-zero exit, `Error:` on stderr, and **zero bytes on stdout** (no human report, no
JSON document, no partial object, no JSON error payload):

| Condition | Error text |
|---|---|
| workspace unresolvable / invalid `workspace_mode` / ambiguity | `internal.RequireWorkspace()` error, verbatim |
| unsafe feature name or a name owned by a registered space | `internal.GuardFeatureName` error, verbatim |
| feature path ambiguity (checkout legacy/new collision) | `ws.ResolveFeaturePath` error, verbatim |
| feature directory does not exist or is not a directory | `feature not found: <feature>` |
| `stack.yaml` missing | `no stack.yaml found for feature: <feature>` (deliberately the legacy string) |
| `stack.yaml` unreadable (any non-ENOENT read error) | `stack.yaml unreadable for feature <feature>: <err>` |
| `stack.yaml` invalid YAML | `stack.yaml invalid for feature <feature>: <err>` |
| structurally invalid stack (§5.1) | `invalid stack.yaml for feature <feature>: <detail>` |

`feature not found: <feature>` is the repository's **existing** user-facing vocabulary for a
resolved feature directory that does not exist, used verbatim by `tws delete`, `tws rename`,
`tws export`, `tws open`, `tws doctor`, and `tws status`
(`internal/cli/delete.go:70`, `internal/cli/rename.go:47`, `internal/cli/export.go:55`,
`internal/cli/open.go:101`, `internal/checkout_health.go:232`, `internal/agent_status.go:1790`).
This feature reuses that string exactly and invents no new `unknown feature` spelling.

**Reportable** — exit 0, full document:

- any ancestry status including `stale`, `divergent`, `missing`, `cross-repo-unsupported`, and the
  unevaluated `""`;
- an unresolvable source repository (every row unevaluated, every supplemental fact null);
- a failed, unavailable, or invalidated branch-ref or worktree inventory;
- a failed dirty or active-operation probe;
- dirty worktrees, detached worktrees, prunable worktrees, missing refs, archived entries,
  cross-repo entries;
- an empty `branches:` list.

There is **no `stack_state` key**. A successful report necessarily describes one loaded, valid
stack; the stable-key promise of §6 applies to successful reports only.

Stack cycles are neither fatal nor reported: the status report never topologically sorts, so a cycle
cannot affect it. `tws stack <feature>` keeps its own cycle warning.

### 5.1 Structural stack validation

`validateStackForStatus(stack Stack) error` is **unexported** in package `internal` and is never
called from package `cli`: the exported `LoadStackForStatus` calls it as its last step and only
returns a stack once it has passed, so "loaded" and "structurally valid" are the same condition and
no second validation step exists anywhere. It fails, in this order, on the first violation:

1. an entry with an empty `name` → `entry <index>: empty name`;
2. two entries with the same `Name` → `duplicate entry name <name>`;
3. a `Name` that is not `filepath.IsLocal` or cleans to `.` → `unsafe entry name <name>`.

`LoadStackForStatus` wraps that first violation as the fatal `invalid stack.yaml for feature
<feature>: <detail>` of the §5 table, where `<detail>` is the validator's message verbatim; the
fatal/zero-stdout boundary of §5 is unchanged by where the call lives.

An empty `branches:` list is **valid**. A duplicate `GitBranch()` across distinct `Name`s is
**valid** and must not be rejected or collapsed (§7.1). A cycle is **valid** for this command.

## 6. JSON contract

### 6.1 Envelope

```go
const stackStatusSchema = 1
```

It is this document's own version, independent of `agentStatusSchema`
(`internal/agent_status.go:18`). Adding a key or an enum value is additive; removing a key,
changing a type, or narrowing an enum bumps it.

```json
{
  "schema_version": 1,
  "workspace": { ... },
  "feature": "auth",
  "entries": [ ... ],
  "summary": { ... }
}
```

Exactly five top-level keys. No `generated_at`, no `stack_state`, no `errors`.

### 6.2 `workspace`

```json
"workspace": {
  "mode": "external" | "checkout",
  "stable_id": "3f2a1b0c9d8e7f60" | null,
  "metadata_root": "/Users/x/myapp.tws",
  "repository": {
    "dir": "/Users/x/myapp" | null,
    "source": "workspace" | "worktree" | "inferred" | "unavailable",
    "alternate": "/Users/x/other" | null
  },
  "external": { "worktrees_root": "/Users/x/myapp.tws/auth/worktrees" } | null,
  "checkout": {
    "path": "/Users/x/myapp" | null,
    "branch": "auth-api" | null,
    "detached": true | false | null,
    "dirty": true | false | null,
    "active_git_op": "none" | "rebase" | "merge" | "cherry-pick" | "revert" | "bisect" | null
  } | null
}
```

Rules:

1. Exactly one of `external` and `checkout` is non-null, selected by `ws.Mode`.
2. `repository.dir`, `.source`, `.alternate` come from the **single** `StackRepoResolution` returned
   by `FeatureStackEdges`; they are never independently rediscovered. `dir` is null iff
   `RepoDir == ""`; `alternate` is null iff `Alternate == ""`.
3. `metadata_root` is the **resolved workspace metadata root** `ws.MetadataRoot`, taken verbatim
   from the single `RequireWorkspace`/`ResolveCurrentWorkspaceE` call the §17.2 `RunE` sequence
   makes (`internal/workspace.go:402-433`), and emitted **as held**. It is never recomputed,
   re-canonicalized, or re-derived here.

   **(sharpened)** It must **not** be described as always canonical. In checkout mode it is
   `filepath.Join(canonicalize(repoRoot), ".tws")` and in the default external case it is
   `canonicalize(repoRoot) + ".tws"`, both canonical — but when the workspace root is configured,
   `resolveExternalRoot` returns the configured `cfg.Workspaces[<repo root>]` value **verbatim**,
   with no canonicalization (`internal/workspace.go:281-289`). A configured root may therefore be
   relative, symlinked, or otherwise non-canonical, and this document reports exactly what the
   workspace holds.

   Paths that are used as **authoritative joins** are canonicalized separately and independently of
   this field, so the emitted `metadata_root` never participates in a comparison: the worktree
   inventory's `ByPath` key and `Record.Path` are `canonicalize`d (§9.1), the external expected
   worktree path is produced by `ancestryWorktreeCandidatePath` and looked up as
   `canonicalize(candidate)` (§9.3), and `checkout.path` is `repository.dir` from the evaluator's
   own resolution (rule 6), not `ws.MetadataRoot`. `external.worktrees_root` (rule 5) is canonical
   because it is explicitly canonicalized there, not because `ws.MetadataRoot` was.
4. `stable_id` is null when `ws.StableID == ""`.
5. `external.worktrees_root` is `filepath.Join(featurePath, "worktrees")`, canonicalized. It is
   computable without a repository, so it is a non-null string even when `repository.dir` is null.
   The external block deliberately carries **no** branch, detached, dirty, or active-operation
   field: external mode has many linked worktrees and no single current checkout.
6. `checkout.path` is `repository.dir` (in checkout mode the one physical checkout *is* the
   canonical main repo root); null when that is null.
7. `checkout.branch` is non-null only when the worktree inventory (§9) holds a record whose
   canonical path equals `checkout.path` and that record carries a **valid full attached branch
   ref**; the value is that ref with `refs/heads/` stripped **after** validation.
8. `checkout.detached` is `true` for an explicit `detached` record, `false` for a valid branch
   record, and `null` when the inventory is unavailable or holds no record for that path.
9. `checkout.dirty` is `probeDirty(checkout.path)`; null on probe error or null path.
10. `checkout.active_git_op` is `probeActiveGitOp(checkout.path)`: `"none"` only after the Git
    directory was resolved **and verified to exist as a directory** and every marker was
    successfully inspected, a known operation name otherwise, `null` on probe error or null path.
11. No field here is derived from `buildHeader`, `healthCurrentBranch`, or `gitDirty`: those three
    collapse failures into attached/clean-looking zero values
    (`internal/checkout_health.go:313-358`), which this schema forbids.

### 6.3 `entries[]`

One element per `stack.Branches` element, in `stack.yaml` slice order (§7.1).

```json
{
  "name": "auth-api",
  "git_branch": "jd/api",
  "archived": false,
  "repo": "../wiki" | null,

  "base": { "name": "auth-models" | null, "kind": "stack-entry" | "literal-ref" | "none", "ref": "refs/heads/auth-models" | null },

  "ref_exists": true | false | null,

  "heads": {
    "local": "<40-hex>" | null,
    "local_short": "1a2b3c4" | null,
    "parent": "<40-hex>" | null,
    "parent_short": "9f8e7d6" | null,
    "merge_base": "<40-hex>" | null,
    "merge_base_short": "5c4b3a2" | null
  },

  "base_record": {
    "sha": "<recorded verbatim>" | null,
    "commit": "<40-hex peeled>" | null,
    "short": "abc1234" | null,
    "state": "absent" | "present" | "unresolvable" | null
  },

  "ancestry": {
    "status": "current" | "stale" | "divergent" | "missing" | "cross-repo-unsupported" | null,
    "reason": "parent-contained",
    "severity": "ok" | "info" | "warning",
    "guidance": "run: tws sync auth" | null,
    "notes": [ { "kind": "base-identity-remote-mismatch", "detail": "..." } ]
  },

  "repo_source": "workspace" | "worktree" | "inferred" | "unavailable",

  "parent_counts": { "ahead": 3 | null, "behind": 0 | null },

  "materialization": {
    "kind": "worktree" | "ref",
    "state": "present" | "archived" | "missing" | "prunable-missing" | "cross-repo-unsupported" | "unknown",
    "path": "/Users/x/myapp.tws/auth/worktrees/auth-api" | null,
    "checked_out_branch": "jd/api" | null,
    "detached": true | false | null,
    "dirty": true | false | null,
    "active_git_op": "none" | "rebase" | "merge" | "cherry-pick" | "revert" | "bisect" | null
  },

  "is_current_checkout": true | false | null,

  "upstream": {
    "configured": true | false | null,
    "ref": "refs/remotes/origin/api" | null,
    "display": "origin/api" | null,
    "state": "none" | "equal" | "ahead" | "behind" | "diverged" | "gone" | null,
    "ahead": 2 | null,
    "behind": 0 | null,
    "local_only": true
  }
}
```

Projection rules (all direct copies from the same-index `StackEdge`; no recomputation):

| JSON | Source | Null rule |
|---|---|---|
| `name` | `se.Name` | never null |
| `git_branch` | `se.GitBranch()` | never null |
| `archived` | `se.Archived` | never null |
| `repo` | `edge.Repo` (`se.Repo` verbatim) | null iff `""` |
| `base.name` | `edge.BaseName` | null iff `""` |
| `base.kind` | `edge.BaseKind` | never null |
| `base.ref` | `edge.BaseRef` | null iff `""` |
| `ref_exists` | `edge.RefProbed ? &edge.RefExists : nil` | null iff `!RefProbed` |
| `heads.local` / `local_short` | `edge.LocalHead` / `LocalHeadShort` | null iff `""` |
| `heads.parent` / `parent_short` | `edge.ParentHead` / `ParentHeadShort` | null iff `""` |
| `heads.merge_base` | `edge.MergeBase` (already `*string`) | copied pointer value |
| `heads.merge_base_short` | `edge.MergeBaseShort` | null iff `""` (⇔ `MergeBase == nil`) |
| `base_record.sha` / `.commit` / `.short` | `edge.LastBaseSHA` / `LastBaseCommit` / `LastBaseShort` | null iff `""` |
| `base_record.state` | `edge.BaseRecord` | **null iff `""`** — the record was never consulted; a verdict never formed is never published (`stack-ancestry-doctor` spec §5.2 rule 10) |
| `ancestry.status` | `edge.Status` | null iff `""` (rendered `unevaluated` in human output via `ancestryDisplayStatus`) |
| `ancestry.reason` | `edge.Reason` | never null on a returned edge |
| `ancestry.severity` | `edge.Severity` | never null; never `error` |
| `ancestry.guidance` | `edge.Guidance` | null iff `""` |
| `ancestry.notes` | `edge.Notes` | always an array, `[]` when empty, never null |
| `repo_source` | `edge.RepoSource` | never null |

The `<40-hex>` shapes shown for `heads.*` and `base_record.commit` in the §6.3 example describe what
the **unchanged** shipped evaluator publishes today (`internal/stack_ancestry.go:247`); they are
copied verbatim and are not re-validated here. They are unrelated to the object-format-neutral
inventory rule of §8.3.1, which governs the two supplemental inventories only. On a repository whose
object format is not SHA-1 these head fields are simply `null` (the evaluator leaves them empty),
while the inventory-derived fields remain fully populated (§8.3.1 rule 5).

`ref_exists` is **only** `RefProbed`/`RefExists`. No second ref probe exists anywhere in this
feature, so `ref_exists` can never disagree with `ancestry.status`. A base-unset row is
intentionally unprobed by the evaluator (`internal/stack_ancestry.go:374-383`; contract pinned at
`internal/stack_ancestry_test.go:745-751`) and therefore reports `ref_exists: null`, null heads,
null `base_record.state`, and null `parent_counts` — even when a branch-ref inventory record exists
for the same branch. Inventory evidence supplies upstream configuration only; it never retroactively
rewrites `RefProbed`, `RefExists`, heads, or ancestry.

**(sharpened)** The analysis cites `internal/checkout_health.go:627-629` as already following the
`RefProbed` rule. That call site actually assigns the plain bool `e.RefExists = edge.RefExists` and
gates its `[ref-missing]` tag on `AncestryStatus` instead (`internal/checkout_health.go:837-841`).
The approved intent — "copy the evaluator's evidence, never re-probe" — is preserved and made
strictly stronger here by modelling `ref_exists` as `*bool`. `CheckoutFeatureEntry.RefExists` and
its rendering are **not** changed by this feature.

### 6.4 `summary`

```json
"summary": {
  "entries": 5,
  "ancestry": { "current": 2, "stale": 1, "divergent": 0, "missing": 1, "cross_repo_unsupported": 0, "unevaluated": 1 },
  "materialization": { "present": 3, "archived": 0, "missing": 1, "prunable_missing": 0, "cross_repo_unsupported": 0, "unknown": 1 },
  "upstream": { "none": 1, "equal": 1, "ahead": 1, "behind": 0, "diverged": 1, "gone": 0, "unknown": 1 },
  "unknown": { "ref_exists": 1, "parent_counts": 2, "dirty": 1, "active_git_op": 1 },
  "local_only": true
}
```

- Every counter is a plain `int` and is always present, including zeros. Keys are fixed struct
  fields, so their encoded order is deterministic and no map is iterated.
- The three groups `ancestry`, `materialization`, and `upstream` each partition `entries` exactly:
  every group's counters sum to `entries`. `unknown` counts rows whose corresponding value is `null`
  and partitions nothing.
- `unknown.dirty` and `unknown.active_git_op` count rows whose `materialization.dirty` /
  `.active_git_op` is null. In checkout mode that necessarily includes every non-current row; this
  is honest and documented, not a defect.
- `local_only` restates the per-row invariant for consumers that read only the summary.

### 6.5 Null versus omission (normative)

- **No struct field in `internal/stack_status.go` carries `omitempty`.** Every key listed in §6.1-6.4
  is present in every document.
- Absent scalars are `null`, modelled as `*string`, `*bool`, `*int`. The encoder is never asked to
  distinguish zero from absent.
- Absent objects (`workspace.external`, `workspace.checkout`) are `null`.
- Lists (`entries`, `ancestry.notes`) are **never** `null`: `NormalizeStackStatus` replaces nil
  slices with empty slices before encoding, matching `NormalizeAgentStatus`
  (`internal/agent_status.go:1816-1830`).
- A zero-valued optional `StackEdge` string becomes `null` without changing its classification.

### 6.6 Ordering and determinism

| Collection | Order |
|---|---|
| `entries[]` | `stack.Branches` slice order, unchanged — no topo sort, no root grouping, no alphabetical tie-break, no deduplication |
| `ancestry.notes[]` | `StackEdge.Notes` order, unchanged |
| every summary counter | fixed struct field order |

`TopoSort` seeds its queue from a map and `PrintTree` walks a map of bases
(`internal/stack.go:138-179,207-277`); neither may influence this report. A grouping map keyed by
`refs/heads/<GitBranch()>` may be used as a **lookup only** and may never drive iteration that
reaches output.

No generated timestamp exists anywhere in the document or the human output, so two runs over an
unchanged repository are byte-identical.

## 7. Authoritative joins

### 7.1 Rows

There is exactly one row per `stack.Branches` element. Duplicate `GitBranch()` values across
distinct `Name`s are **retained as separate rows**: they keep distinct logical names, distinct
external expected worktree paths, and distinct `is_current_checkout`/materialization joins, while
sharing identical branch-ref and upstream facts (they name the same Git ref) and identical
`checked_out_branch`/dirty/active-op facts when they resolve to the same worktree record.

### 7.2 Ancestry projection

`BuildStackStatus` calls `FeatureStackEdges(ws, cfg, feature, featurePath, stack)` **exactly once**
and keeps both return values. It calls no second repository resolver, issues no ref probe, and reads
no `StackEdge` field through any transformation other than those tabulated in §6.3.

### 7.3 Totality

Row *i* uses `edges[i]`. If the evaluator ever returns a slice of the wrong length, the report
applies the existing totality rule rather than indexing a zero value:

```go
edges = ancestryEdgesFor(feature, stack, edges)   // internal/checkout_health.go:604-616
```

`ancestryEdgesFor` is reused unchanged (same package). It replaces a short slice with
`UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, ...)`, so a zero `StackEdge` — which
would read as an evaluated `current` verdict with no severity — can never reach output. When that
fallback fires, every row is unevaluated and the supplemental probes of §8, §11, and §12 are still
governed by their own preconditions (`repository.dir` may still be non-null, so the two inventories
may still run; heads are empty, so `C == 0`).

## 8. Branch-ref inventory (upstream truth)

### 8.1 The single command

Run once per invocation, only when `repository.dir != ""`, without a shell:

```text
git -C <repository.dir> for-each-ref --format=%(refname)%00%(objectname)%00%(upstream)%00%(upstream:track)%00%(upstream:trackshort) refs/heads/
```

- `-C <dir>` is used (not `cmd.Dir`) to match the existing `-C`-shaped status/health probe family
  and the shim shape table of `stack-ancestry-doctor` §7.1, which strips one optional leading
  `-C <dir>`. The directory argument is always the validated, non-empty `repository.dir`; **`git -C ""`
  is impossible** because the command is skipped when it is empty.
- Every argument is a compile-time constant except that directory. No user-controlled string is
  passed, so `--end-of-options` is unnecessary here — the same structural argument the shipped
  evaluator uses for `merge-base` with peeled SHAs (`stack-ancestry-doctor` §7.1 rule 2).
- stderr is discarded (`cmd.Output()`), so ambiguity warnings can never leak into output.

### 8.2 Field and record grammar

Five fields per record, NUL-separated by `%00`, in this order:

1. full branch ref, e.g. `refs/heads/feature/api`;
2. branch object ID;
3. full configured upstream ref, or empty;
4. `%(upstream:track)` long text, or empty;
5. `%(upstream:trackshort)` marker, or empty.

Records are separated by the newline `for-each-ref` appends per expansion. Newline is a safe record
boundary because a Git refname cannot contain one. There is **no printable delimiter** such as `|`,
and neither `%(refname:short)` nor `%(upstream:short)` is used anywhere.

### 8.3 Fail-closed parsing

`parseBranchRefInventory(out []byte) (map[string]BranchRefRecord, error)`:

1. split on `"\n"`; at most one trailing empty record is allowed, any other empty record is an
   error;
2. split each record on `"\x00"` and require **exactly five** fields;
3. field 1 must have prefix `refs/heads/` and a non-empty remainder;
4. field 2 must be a **non-empty lowercase hexadecimal string**, matched with the
   stack-status-specific `stackStatusObjectID` of §8.3.1 — **not** with `ancestryFullSHA`;
5. a non-empty field 3 must have prefix `refs/` and length > len(`refs/`);
6. the `(field 4, field 5)` pair must be one of the six accepted pairs of §8.4;
7. a duplicate field-1 key is an error.

Any violation makes the **entire** inventory unavailable: `BranchRefInventory{Available: false, Err: …}`.
A partially parsed map is never published. The report stays successful (exit 0) and every field whose
truth depended on the inventory becomes null (§8.5 row "unavailable").

### 8.3.1 Object-format-neutral inventory object IDs (normative)

Both supplemental inventories — the branch-ref inventory field 2 (§8.3 rule 4) and the worktree
porcelain `HEAD` line (§9.2) — validate object IDs with **one** stack-status-specific matcher:

```go
var stackStatusObjectID = regexp.MustCompile(`^[0-9a-f]+$`)
```

Rules, all normative:

1. An inventory object ID is well formed iff it is a **non-empty lowercase hexadecimal string**.
   Length is deliberately **not** constrained, so a repository initialized with
   `git init --object-format=sha256` (64-hex object IDs) is parsed exactly as a SHA-1 repository
   (40-hex) is. No inventory validation anywhere in this feature is SHA-1-only.
2. `ancestryFullSHA` (`^[0-9a-f]{40}$`, `internal/stack_ancestry.go:127`) is **not** reused for
   either inventory. It stays confined to the places that genuinely require a full peeled
   `StackEdge` head under the shipped evaluator's contract — the §12 parent-count preconditions —
   and `internal/stack_ancestry.go` semantics are unchanged by this feature (§3, §17.6).
3. `stackStatusObjectID` is **parser validation only**. It gates nothing else: no identity claim, no
   comparison, no truncation, and no derivation. The validated token is stored **verbatim**
   (`WorktreeRecord.Head` is the porcelain token as Git printed it; the branch-ref record's object
   ID is field 2 as Git printed it). This feature never re-abbreviates, re-peels, or re-formats an
   inventory object ID.
4. Fail-closed cases (§8.3, §9.2) call an object ID malformed when it is **empty or contains a
   character outside `[0-9a-f]`** — never merely because its length is not 40. A 64-hex `HEAD` or
   objectname is well formed and must keep the inventory `Available: true`.
5. On a SHA-256 repository the shipped `StackEdge` ancestry may still be unevaluated or missing,
   because `internal/stack_ancestry.go` peels and matches 40-hex and is out of scope here. This
   feature makes **no** claim to fix that and changes nothing about it: `ancestry.*`, `heads.*`,
   `ref_exists`, `base_record.*`, and `parent_counts` may all be null there. What this rule
   guarantees is narrower and exact — the supplemental worktree and branch-ref inventories, and
   therefore `materialization.*`, `checked_out_branch`, `detached`, `upstream.*`, and
   `workspace.checkout.branch`, **must not** invalidate solely because the repository's object
   format is not SHA-1.

### 8.4 Accepted tracking pairs and upstream mapping

Verified against `git version 2.55.0` on a real repository with a real local bare remote (fixtures
covering all seven rows are required by AC 21):

| field 3 (upstream) | field 4 (track) | field 5 (trackshort) | `configured` | `state` | `ahead` | `behind` |
|---|---|---|---|---|---|---|
| `""` | `""` | `""` | `false` | `none` | null | null |
| non-empty | `""` | `=` | `true` | `equal` | `0` | `0` |
| non-empty | `[ahead N]` | `>` | `true` | `ahead` | `N` | `0` |
| non-empty | `[behind N]` | `<` | `true` | `behind` | `0` | `N` |
| non-empty | `[ahead N, behind M]` | `<>` | `true` | `diverged` | `N` | `M` |
| non-empty | `[gone]` | `""` | `true` | `gone` | null | null |
| inventory unavailable, invalid, skipped, or no record for the key | — | — | `null` | `null` | null | null |

Notes, all normative:

- `N` and `M` are parsed with `strconv.Atoi` from the exact bracketed forms above, including the
  literal `", "` separator in the diverged form. A non-numeric or negative value is a parse error
  (§8.3).
- `equal`, `none`, and `gone` are three distinct states and are never conflated. `[gone]` with an
  **empty** trackshort is the real Git output and is what the parser must accept — this is the one
  place the analysis table left the trackshort column implicit.
- A branch whose `branch.<n>.remote` names a non-existent remote yields an **empty** upstream and is
  therefore `none`; a branch whose upstream ref itself is missing yields `[gone]`. Both are
  documented, tested real-Git behaviours, not parser guesses.
- **Local-branch upstreams are preserved**: field 3 may be `refs/heads/main`. The mapping is
  identical; only `display` differs.
- `upstream.ref` is field 3 verbatim. `upstream.display` strips exactly one known prefix —
  `refs/remotes/` or `refs/heads/` — **after** validation; any other validated `refs/...` value is
  displayed verbatim.
- `upstream.local_only` is `true` in every successful row, unconditionally.

### 8.5 Join key

The map key is the **complete** field-1 value. The row's lookup key is:

```go
key := edge.ChildRef                       // "refs/heads/<GitBranch()>" when the evaluator probed
if key == "" { key = "refs/heads/" + se.GitBranch() }
```

**(sharpened)** The analysis says a row joins on `StackEdge.ChildRef` *and* that a base-unset row may
still receive upstream configuration — but `ChildRef` is `""` precisely for base-unset, cross-repo,
and repo-unavailable edges (`internal/stack_ancestry.go:371-386`). The derived fallback resolves the
contradiction without weakening anything: when both exist they are identical by construction, and an
implementation assertion (unit test) pins that equality.

Exception: **cross-repo rows perform no lookup at all** (`edge.Repo != ""`). Their branch lives in
another repository that this feature never probes, so their whole `upstream` object is null except
`local_only: true`.

Because both sides of the join are complete `refs/heads/...` names, a same-named tag can neither
change a key nor win a lookup. Prefix stripping happens only in `display` and only after parsing and
lookup.

## 9. Worktree inventory (materialization truth)

### 9.1 Additive extension

`WorktreeInventory` (`internal/agent_status.go:473-478`) keeps every existing field and gains three:

```go
type WorktreeRecord struct {
    Path          string   // canonical worktree path
    Head          *string  // HEAD-line object ID, stored verbatim (§8.3.1); nil when absent
    BranchRef     *string  // full "refs/heads/..." ref; nil when absent
    Detached      *bool    // true = explicit `detached`; false = valid branch line; nil = neither
    Bare          bool
    Locked        bool
    LockReason    *string
    Prunable      bool
    PrunableReason *string
}

type WorktreeInventory struct {
    Available bool                      // unchanged meaning
    ByBranch  map[string]string         // unchanged: short branch -> raw path, live entries
    Prunable  map[string]bool           // unchanged: short branch -> prunable
    Records   []WorktreeRecord          // NEW: every block, in Git's order
    ByPath    map[string]WorktreeRecord // NEW: canonical path -> record
    Err       error                     // NEW: non-nil iff Available == false and a cause exists
}
```

`BuildWorktreeInventory(repoRoot string) WorktreeInventory` keeps its signature and its single
`git -C <repoRoot> worktree list --porcelain` process.

**Who actually consumes it (sharpened).** In production it has exactly **one** caller:
`BuildAgentStatus` (`internal/agent_status.go:720`), which feeds `tws status`
(`internal/cli/status.go:71`) — its `Prunable` map is read at `internal/agent_status.go:1449` and
its `ByBranch` map by the same builder. `tws doctor` and `tws list` do **not** call it:
`tws doctor` goes through `BuildCheckoutHealthReport`/`CheckFeatureHealth`
(`internal/cli/doctor.go:109-149`) and `tws list` through `BuildCheckoutList` /
`IsPrunableWorktree` / `CheckWorktreeBranch` (`internal/cli/list.go:66-122`). Every
inventory-compatibility claim in this spec is therefore scoped to **`tws status`, direct inventory
callers, and the inventory's own tests** (`internal/agent_status_test.go:876-912`). `tws doctor` and
`tws list` are affected by this feature only through the shared dirty/active-op wrapper adaptation
of §11.3 (`gitDirty`/`gitActiveOp`, reached by `buildHeader`,
`internal/checkout_health.go:327-330`), and their regression coverage exists for that reason alone.

For **well-formed** porcelain, the existing caller and its `ByBranch`/`Prunable` consumers are
untouched and
must produce identical output: `ByBranch`/`Prunable` continue to be keyed by the **short** branch
name with `refs/heads/` stripped, a prunable branch continues to land in `Prunable` and **not** in
`ByBranch`, and `ByBranch` values continue to be the **raw porcelain path byte-for-byte**, exactly
as today (`internal/agent_status.go:498-520`).

**Path values are deliberately split (sharpened).** The new `WorktreeRecord.Path` and the `ByPath`
key are the **canonical** path (`canonicalize`), because they are the authoritative join surface of
§9.3 and a join must not fail on `/var` vs `/private/var`. The legacy `ByBranch` value stays the
**raw** porcelain string, because normalizing it would change bytes that `tws status`, `tws doctor`,
and `tws list` already emit. The parser therefore retains a per-block local raw path **solely** to
populate `ByBranch`; nothing else reads it, and no other field is derived from it.

For **malformed** porcelain the new parser is deliberately stricter than today's — see §9.2. That is
an intentional fail-closed hardening, not an equivalence claim, and §11.4/AC 35 scope the legacy
equivalence assertion accordingly.

### 9.2 Parser contract

Every block of `git worktree list --porcelain` is retained: main, linked, detached, locked, bare,
and prunable. Line handling:

- `worktree <path>` — starts a record; the path is stored canonicalized (`canonicalize`) in
  `Record.Path`/`ByPath`, and the **raw** string is kept in a block-local variable used only to
  populate the legacy `ByBranch` value (§9.1).
- `HEAD <oid>` — `Head`; must match `stackStatusObjectID` (non-empty lowercase hex, any length —
  §8.3.1) or the inventory fails. The validated token is stored **verbatim**, so a 40-hex SHA-1 and
  a 64-hex SHA-256 object ID are both retained unchanged. `ancestryFullSHA` is **not** used here.
- `branch <ref>` — `BranchRef`; must have prefix `refs/heads/` with a non-empty remainder or the
  inventory fails. Sets `Detached = false`.
- `detached` — sets `Detached = true`.
- `bare` — `Bare = true`.
- `locked` / `locked <reason>` — `Locked = true`, `LockReason` when a reason is present.
- `prunable` / `prunable <reason>` — `Prunable = true`, `PrunableReason` when a reason is present.
- an unrecognized line is ignored (forward compatibility with new porcelain attributes).
- a blank line flushes the current record; EOF flushes the final record.

The whole inventory is **unavailable** (`Available = false`, `Err` set, `Records`/`ByPath`/`ByBranch`/
`Prunable` empty) when: the command fails, a block has no `worktree` line, a canonical path repeats,
a branch ref is malformed, a `HEAD` object ID is malformed in the §8.3.1 sense (empty or containing
a character outside `[0-9a-f]` — **never** merely because its length is not 40), or a block carries
both `detached` and a `branch` line.

**This is a deliberate hardening, and it is the one place where an existing command can observe a
difference (sharpened).** The shipped parser is *tolerant*: it validates nothing, silently drops
unparsable lines, and still reports `Available = true` with whatever partial `ByBranch`/`Prunable`
it managed to build (`internal/agent_status.go:485-523`). The approved contract for the new
inventory intentionally invalidates the **whole** inventory instead, because a partially parsed
worktree map is exactly the input that would let stack status claim a false materialization. The
consequence is stated plainly rather than hidden, and scoped to the inventory's actual consumer
(§9.1: `BuildAgentStatus` → `tws status`; **not** `tws doctor` and **not** `tws list`):

- for real, well-formed `git worktree list --porcelain` output, and for ordinary
  command-unavailable/failure cases (`git` missing, non-zero exit, empty `repoRoot`), the new
  inventory is byte-for-byte the old one, so `tws status` is unchanged. `tws doctor` and `tws list`
  never read this inventory at all and are unchanged a fortiori;
- for **malformed** porcelain — reachable today only via a fabricated shim, never from real Git —
  the legacy inventory may still report `Available = true` with partial maps while the new one
  reports `Available = false`, a non-nil `Err`, and empty maps. `BuildAgentStatus` then takes its
  already-defined unavailable-inventory branch, which is a shape it handles today; it
  gains no new issue code, severity, message, or status value, and no other command is reached.

Object format is **not** a hardening trigger: a 64-hex `HEAD` from a SHA-256 repository keeps the
inventory `Available = true` (§8.3.1), so `tws status` materialization and prunable semantics are
identical on SHA-1 and SHA-256 repositories.

§11.4 rule 4 and AC 27/AC 35 scope the legacy-equivalence assertion to the first bullet and assert
the second bullet as an intentional divergence, so no test may claim field equality on malformed
fixtures.

`Detached` is `nil` when a block carried neither a `branch` nor a `detached` line (a bare main
worktree is the real case).

### 9.3 The external path join

For an external row that is not cross-repo, the expected path is computed with the **existing**
containment-validating helper rather than a raw join **(sharpened)**:

```go
candidate, err := ancestryWorktreeCandidatePath(featurePath, se.Name)  // internal/stack_ancestry.go:767-782
```

It rejects a non-local or escaping entry name before any path is used. On error the row's
materialization is `state: "unknown"`, `path: null`, and every dependent field is null. This
preserves the approved intent ("canonicalize `<feature-path>/worktrees/<StackEntry.Name>`") while
reusing the shipped traversal guard instead of duplicating it.

Lookup is `inv.ByPath[canonicalize(candidate)]`, and it is **authoritative** — including for
detached worktrees and duplicate Git branches. No per-row branch probe, directory `Stat`, or
`IsPrunableWorktree` call exists in this feature.

| Condition (evaluated in this order) | `state` | `path` |
|---|---|---|
| `edge.Repo != ""` | `cross-repo-unsupported` | null |
| unsafe entry name (helper error) | `unknown` | null |
| inventory unavailable (incl. `repository.dir == ""`) | `unknown` | null |
| matching record, `Prunable == false` | `present` | canonical candidate |
| matching record, `Prunable == true` | `prunable-missing` | canonical candidate |
| no matching record, `se.Archived == true` | `archived` | null |
| no matching record | `missing` | null |

**(sharpened, deliberate divergence)** `tws status` decides external materialization by `os.Stat` on
the expected directory (`agent-work-status-dashboard` spec §5.3). This feature decides it from the
porcelain path map, so a leftover directory that Git does not know about reports `missing` here and
`present` there. That is the approved intent ("this path join is authoritative … it avoids the
current branch-key limitation and requires no separate branch probe"); it is documented in §18 and
pinned by AC 30. `tws status`'s own materialization logic is not changed by this rule (its only
behaviour change anywhere is the malformed-porcelain fail-closed hardening of §9.2).

When a record matches, `checked_out_branch` is its `BranchRef` with `refs/heads/` stripped **after**
validation, and `detached` is its `Detached`. A `present` record whose branch differs from the row's
`git_branch` still reports `present` with the truthful `checked_out_branch`; this feature adds no
wrong-branch vocabulary.

## 10. Mode semantics

### 10.1 External mode

- `materialization.kind` is `"worktree"` for every row.
- `is_current_checkout` is **always `null`** for every row, in every situation. There is no single
  current checkout in external mode; `false` everywhere would imply a meaningful negative.
- `materialization.dirty` and `.active_git_op` are probed **at the matched worktree path**, never at
  `repository.dir`, and only when `state == "present"`. For `missing`, `archived`,
  `prunable-missing`, `cross-repo-unsupported`, `unknown`, or a failed probe they are `null`.
- `workspace.external` carries only `worktrees_root` (§6.2 rule 5).

### 10.2 Checkout mode

- `materialization.kind` is `"ref"` for every row; `materialization.path` is always `null` (the one
  physical path is `workspace.checkout.path`).
- `materialization.state` is derived **only** from the evaluator's evidence:

  | Condition | `state` |
  |---|---|
  | `edge.Repo != ""` | `cross-repo-unsupported` |
  | `edge.RefProbed && edge.RefExists` | `present` |
  | `edge.RefProbed && !edge.RefExists` | `missing` |
  | `!edge.RefProbed` (base-unset, repo-unavailable) | `unknown` |

  `archived` is reported by the row's own `archived` field; checkout mode never uses the `archived`
  materialization state, because a checkout entry has no worktree to be absent.
- `checked_out_branch`, `dirty`, and `active_git_op` are **copies of the workspace checkout facts**,
  written only onto **same-repository** rows (`edge.Repo == ""`) whose `git_branch` equals a known
  `workspace.checkout.branch`; every other row gets `null` for all three. Duplicate rows sharing that
  Git branch each receive the same copies and remain separate rows.
- A **cross-repo row** (`edge.Repo != ""`) is owned by another repository, so it reports
  `state: "cross-repo-unsupported"` and **nothing else**: `checked_out_branch`, `detached`, `dirty`,
  `active_git_op`, `path`, and `is_current_checkout` are all `null`, **even when its `git_branch` is
  byte-identical to `workspace.checkout.branch`**. A name collision across repositories is not
  evidence about this checkout, and no supplemental process is started for such a row.
- `is_current_checkout` is a bool **only** when the row is same-repository (`edge.Repo == ""`) **and**
  `workspace.checkout.branch != null` **and**
  `workspace.checkout.detached == false`; then it is `git_branch == branch`. In every other case —
  cross-repo row, detached checkout, unavailable inventory, unavailable repository — it is `null`.
- The current checkout branch is never inferred from a session record, from `HEAD` parsing, or from
  a ref merely existing. Only Git's worktree inventory may name it.

## 11. Read-only tri-state probes

Two new error-returning helpers in `internal/stack_status.go` (package `internal`):

```go
func probeDirty(path string) (bool, error)
func probeActiveGitOp(path string) (string, error)
```

### 11.1 `probeDirty`

```text
GIT_OPTIONAL_LOCKS=0 git -C <path> status --porcelain
```

- `path` must be non-empty; an empty path returns an error without starting a process.
- The environment is the inherited environment plus `GIT_OPTIONAL_LOCKS=0` (`cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")`).
- Exit 0 → `len(bytes.TrimSpace(stdout)) > 0`. Any non-zero exit or start error → `(false, err)`.
- Callers map the error to JSON `null`, never to `false`.

Measured justification (real repository, `git 2.55.0`): without the variable, `git status --porcelain`
rewrites `.git/index` (mtime changes); with it, the index bytes and mtime are unchanged and the dirty
verdict is identical for clean, touched-but-unchanged, and genuinely modified fixtures. This is the
acceptance proof of AC 34; asserting only that the command succeeded is insufficient.

### 11.2 `probeActiveGitOp`

```go
const StackStatusOpNone = "none"
```

- Resolve the Git directory for `path`, in this order:
  1. `os.Stat(filepath.Join(path, ".git"))`. Any stat error — including `fs.ErrNotExist` for a
     missing `.git` — is an **error**.
  2. If that entry is a directory, it is the candidate Git directory.
  3. Otherwise it is read as a `.git` file; a read error is an **error**. The trimmed content must
     have the `gitdir: ` prefix with a non-empty remainder, or it is an **error**. A relative target
     is joined against `path`; the result is `filepath.Clean`ed (identical resolution to
     `gitActiveOp`, `internal/checkout_health.go:360-378`).
  4. The **resolved** Git directory from step 2 or step 3 is then itself `os.Stat`ed and must exist
     and be a directory. A missing target (`fs.ErrNotExist`), an unreadable target (any other stat
     error), and a target that is not a directory are each an **error**. No marker is inspected
     until this check passes.

  Step 4 is the whole point of this helper: without it, a `.git` file whose `gitdir:` target no
  longer exists makes every marker `os.Stat` below return `fs.ErrNotExist`, and the helper would
  fabricate `StackStatusOpNone` for a repository whose Git directory is gone — the exact fabricated
  value the tri-state contract forbids (AC 32).
- Inspect these markers in this fixed order, unchanged from the shipped helper:
  `rebase-merge` → `rebase`, `rebase-apply` → `rebase`, `MERGE_HEAD` → `merge`,
  `CHERRY_PICK_HEAD` → `cherry-pick`, `REVERT_HEAD` → `revert`, `BISECT_LOG` → `bisect`.
- The first marker whose `os.Stat` succeeds wins and its name is returned.
- `os.Stat` returning an error that is **not** `fs.ErrNotExist` (a permission error, an I/O error) is
  an **error**, not "no operation".
- `StackStatusOpNone` is returned only after the Git directory was resolved and verified per the
  four steps above **and** every marker was successfully inspected as absent.

The shipped `gitActiveOp` silently treats a missing `.git`, an unreadable or malformed gitdir
pointer, a vanished gitdir target, and a non-ENOENT stat error as "no operation"
(`internal/checkout_health.go:360-390`). This contract must not.

### 11.3 Behaviour-preserving adaptations

`gitDirty` and `gitActiveOp` become thin wrappers with **identical** observable behaviour, so
`tws status`, `tws doctor` (checkout and external), and `tws list` keep their exact output, issue
sets, counts, and exit codes. These two wrappers are the **only** way this feature can reach
`tws doctor` at all (`buildHeader`, `internal/checkout_health.go:327-330`), and — besides the
worktree inventory of §9.1 — the only way it reaches `tws status`
(`BuildAgentStatus`, `internal/agent_status.go:794-795,1484`). `tws list` reaches **neither**: its
checkout path uses `healthCurrentBranch` and `FeatureStackEdges`
(`internal/checkout_health.go:942-991`) and its external path uses `IsPrunableWorktree` /
`CheckWorktreeBranch` (`internal/cli/list.go:60-122`), so it is unaffected by construction and its
goldens exist only as a broad regression guard. `tws doctor`'s regression coverage is likewise
scoped to the wrapper adaptation and not to the worktree inventory (§9.1):

```go
func gitDirty(repo string) bool {
    dirty, err := probeDirty(repo)
    if err != nil { return false }   // preserved legacy collapse
    return dirty
}

func gitActiveOp(repo string) string {
    op, err := probeActiveGitOp(repo)
    if err != nil || op == StackStatusOpNone { return "" }   // preserved legacy collapse
    return op
}
```

- `healthCurrentBranch` is **not** changed and is **not** used by stack status.
- The stricter §11.2 errors are invisible to the wrappers by construction. Every case the shipped
  helpers collapsed still collapses to the same value: a missing `.git`, an unreadable or malformed
  `gitdir:` pointer, a vanished or non-directory gitdir target, and a non-ENOENT marker stat all
  made the shipped `gitActiveOp` return `""` (it found no marker), and they make the wrapper return
  `""` (the probe errors). Likewise a failed `git status` made the shipped `gitDirty` return
  `false`, and it still does.
- The observable differences for existing commands are exactly two, both accepted and both explicit:
  1. the dirty probe reached by `tws status` and `tws doctor` no longer
     refreshes the index (strictly more read-only); `tws list` calls neither wrapper and is
     unchanged for that reason as well;
  2. on **malformed** worktree porcelain — unreachable from real Git, producible only by a
     fabricated shim — `BuildWorktreeInventory` now fails closed instead of returning a partial
     inventory (§9.2), so its single production consumer `BuildAgentStatus` (→ `tws status`) takes
     its existing unavailable-inventory branch. This second difference cannot reach `tws doctor` or
     `tws list`, which never call that inventory (§9.1).

  For real, well-formed Git output and for ordinary command failures, human output and exit codes
  stay byte-identical and `tws status --json` stays identical apart from its wall-clock
  `generated_at`, proven by the baseline method of §11.4 (AC 35). These are explicit, accepted
  adaptations, not accidental ones.
- No existing caller gains a new issue code, severity, message, or status value from a probe error
  or from a fail-closed inventory.

### 11.4 Baseline method for the "existing commands unchanged" regression

`tws status --json` carries a wall-clock `generated_at` (`internal/agent_status.go:377,714`) and
every surface carries fixture-root absolute paths, so **raw byte equality of a before/after pair
cannot be implemented** for the JSON surface, and a "before" run cannot exist in a process that
already contains the change. AC 35 therefore uses this method, and no other:

1. **Pinned pre-change goldens.** The harness and the normalizer are test-only code and are written
   **first**, in a working tree whose `internal/checkout_health.go` and `internal/agent_status.go`
   are still unmodified. In that tree the fixture matrix below is run — under the pinned fixture
   environment of rule 2, which uses only helpers that already exist at that parent commit — and
   the results are written to
   `internal/cli/testdata/existing_commands/<fixture>/{status.txt,status.json,doctor.txt,list.txt}`,
   each golden's first line being `exit: <code>`. The repository has no `testdata` tree today; this
   feature introduces exactly this one, it holds pinned test output only, and it adds no production
   API. Those files are committed with the feature and are the pre-change evidence. Regeneration is
   gated behind `TWS_REGEN_EXISTING_GOLDENS=1`; a regeneration that alters a committed golden **is**
   the regression this criterion exists to catch and must fail review rather than be re-baselined.
2. **Deterministic, date-pinned, host-independent fixtures.** Every fixture repository is built by a
   **new, date-pinning test builder**, not by the existing helpers, and — because
   `TestStackStatus_ExistingCommandsUnchanged` lives in package `cli` (AC 35) — every AC 35 builder
   is written **in package `cli`** and may only use package-`cli` test code.

   *Checkout-mode fixtures.* The AC 35 checkout builder uses (or reproduces in place) the existing
   package-`cli` checkout setup pattern of `setupGitRepoCheckout`/`gitInDir`
   (`internal/cli/checkout_lifecycle_test.go:15-63`): a `t.TempDir()` repository, `git init
   --initial-branch=main`, an empty initial commit, a written `.tws/config.yaml` carrying
   `workspace_mode: checkout`, and a `cmd.Env` that pins `GIT_CONFIG_NOSYSTEM=1`, `HOME=<dir>`, and
   the four author/committer name/email variables. It must **not** call
   `setupHealthTestRepo`/`gitInTest` (`internal/checkout_health_test.go:40-131`): those are
   unexported package-`internal` test helpers and are unreachable from package `cli`.

   *External-mode fixtures.* The AC 35 external builder wraps the package-`cli` `setupGitRepo`
   (`internal/cli/new_integration_test.go:135-150`) plus `createWorktree`
   (`internal/cli/new.go:163`).

   *What the builders add.* `setupGitRepoCheckout`/`gitInDir` pin only
   `GIT_AUTHOR_NAME`/`GIT_AUTHOR_EMAIL`/`GIT_COMMITTER_NAME`/`GIT_COMMITTER_EMAIL`, and
   `setupGitRepo` pins only `user.name`/`user.email`; commits made
   through either therefore get a wall-clock timestamp and a different object ID on every run. The
   AC 35 builders therefore add an environment that additionally pins fixed
   `GIT_AUTHOR_DATE` and `GIT_COMMITTER_DATE` (a single constant such as
   `2020-01-01T00:00:00+00:00`, incremented deterministically per commit where distinct commits are
   required), so commit object IDs are byte-stable across runs and machines. Only with that pinning
   may SHAs be compared **verbatim** and never normalized away; a golden captured with a
   dynamic-date helper would fail on the next run and must not be produced.

   This constraint is scoped to AC 35 only. Package-`internal` tests in this feature
   (`internal/stack_status_test.go`, `internal/agent_status_test.go`) may keep using
   `setupHealthTestRepo`/`gitInTest` and every other package-internal helper as-is (§17.7).

   Date pinning alone is not sufficient, because the captured surfaces also read **host**
   state. `tws status` calls `BuildAgentStatus(ws, degradedReason, nil)`
   (`internal/cli/status.go:71`), and a nil opts falls back to `defaultAgentStatusOpts`, whose tmux
   seam is `RealTmuxInventory{}` (`internal/agent_status.go:394-399,675-683,427-429`), so on a
   host without tmux the builder emits the `tmux-missing` info issue
   (`internal/agent_status.go:99,810-821`) — changing
   the issue list, the summary counts, and the human issue block — while on a host with a running
   tmux server the session set, and therefore `tws list`'s ` [tmux]` tag
   (`internal/cli/list.go:73-77`, `internal/cli/open.go:315-319`) and `tws doctor`'s session report
   (`internal/checkout_health.go:83-86,537-546`), vary with whatever sessions the developer happens
   to have open. Independently, `HOME` reaches the golden through the global config file
   (`internal/config.go:29-32,60-63`) and the no-repo workspace-root fallback
   (`internal/paths.go:72-74`), `TWS_ROOT` overrides the workspace root outright
   (`internal/paths.go:77-81`), and `XDG_DATA_HOME` selects the global space registry
   (`internal/registry.go:79-89`). Every AC 35 fixture therefore installs, at **both** pre-change
   golden capture and post-change assertion, and in this order:
   - `withUnifiedWorkspaceEnv(t, repo)` (`internal/cli/space_guard_test.go:56-76`) — **not** the
     narrower `withWorkspaceEnv` (`internal/cli/new_integration_test.go:152-161`), which leaves
     `XDG_DATA_HOME` unset and would let the developer's real global registry and its spaces leak
     into `tws status`, `tws doctor`, and `tws list`. Precisely, `withUnifiedWorkspaceEnv` pins
     `HOME` and `XDG_DATA_HOME` to **fresh** `t.TempDir()` locations, **clears** any host `TWS_ROOT`
     (`t.Setenv("TWS_ROOT", "")` — it does **not** point `TWS_ROOT` at a fresh directory, so the
     workspace root is derived from the fixture repository rather than from an env override),
     `chdir`s into the fixture repository with cleanup, resolves the workspace via
     `internal.RequireWorkspace()`, asserts that `ws.MetadataRoot` and `internal.TwsRoot()` name the
     same root, and returns that metadata root;
   - `withIdleTmuxOnPath(t)` (`internal/cli/status_test.go:16-42`) — the existing package-`cli`
     helper that prepends a `tmux` stub to `PATH` and verifies through
     `RealTmuxInventory{}.Snapshot()` that the inventory is `Available == true`,
     `ServerRunning == false`, `Err == nil`, and
     session-free, so **no** `tmux-missing` (or `tmux-unverifiable`) issue is emitted and no session
     is ever observed. The stub exits non-zero for every argv, so the `tmux has-session` calls made
     by `tws list` and `tws doctor` are answered "absent" identically on every host.

   With both installed, a macOS runner without tmux, an Ubuntu runner with tmux, and a developer
   machine with a live tmux server and a populated global registry produce the same bytes: a real
   host tmux binary, a running tmux server, host tmux sessions, the host global registry, and the
   host global config and host `TWS_ROOT` are all incapable of affecting a golden. These are fixture
   **environment** requirements, not output rewriting; the normalization rules below remain exactly
   three.
3. **Normalization — exactly three rules, applied identically at capture and at assertion.**
   - *Paths (all surfaces).* The fixture's repository root, its resolved workspace metadata root
     (`ws.MetadataRoot` as held — under `withUnifiedWorkspaceEnv` no workspace root is configured,
     so it is derived from the canonical repository root, §6.2 rule 3), and the (external)
     worktrees root — each also in its `filepath.EvalSymlinks` form, so macOS `/var` vs
     `/private/var` cannot leak — are replaced by the fixed tokens `<REPO>`, `<META>`,
     `<WORKTREES>`, longest path first so nested roots normalize deterministically. Any residual
     absolute path rooted at `os.TempDir()` fails the test, so an unnormalized temp path can never
     be baked into a golden.
   - *Workspace stable ID (all surfaces).* `Workspace.StableID` is `sha256(canonical repo root)`
     truncated to 8 bytes and rendered as 16 hex characters (`internal/workspace.go:72-75`), so it
     is derived from the fixture's canonical temp path and changes on every run even with
     date-pinned commits. It reaches the goldens through JSON `stable_id`
     (`internal/agent_status.go:340,767-768`) and the human `ID:` lines of both `tws status`
     (`internal/agent_status.go:1970`) and `tws doctor`
     (`internal/checkout_health.go:106,771`), and it can also appear embedded in a derived name such
     as a checkout session name (`internal/session.go:652`). The normalizer computes the fixture's
     **exact** expected stable ID (`stableID(canonicalize(repoRoot))`, recomputed at capture and at
     assertion from the live fixture root) and replaces **only that exact string**, wherever it
     occurs in the artifact, with the fixed token `<STABLE_ID>`: the JSON `stable_id` value is
     replaced after decoding, and the human `ID:` occurrences are replaced by literal string match
     on that computed value. A blanket `[0-9a-f]{16}` regex is **forbidden** — it would also rewrite
     abbreviated or full commit SHAs, which rule 2 exists to compare verbatim. Any residual
     occurrence of the computed stable ID in a normalized artifact fails the test, exactly as a
     residual temp path does.
   - *Time (JSON only).* The document is decoded, `generated_at` is asserted present and then
     deleted, the remaining values are path- and stable-ID-normalized, and the result is re-encoded
     and compared against the golden. `generated_at` is the **only** key deleted; nothing else is
     dropped. This is exactly the shipped precedent at `internal/cli/status_test.go:205-241`;
     `internal/agent_status_test.go:1252-1262` pins the same "identical apart from `generated_at`"
     property at the builder level, where `AgentStatusOpts.Now`
     (`internal/agent_status.go:684-714`) supplies a fixed clock.
   Nothing else is rewritten. Human surfaces (`tws status`, `tws doctor` in both modes, `tws list`)
   contain no generated time, so after path and stable-ID normalization they are compared as raw
   bytes.
4. **Source-level equivalence, scoped to where equivalence is actually intended.** Surface goldens
   alone would not localize a `BuildWorktreeInventory` regression whose effect is masked by a
   fixture. The package-`internal` half of AC 35 therefore pins verbatim test-local copies of the
   parent commit's `gitDirty`, `gitActiveOp`, and `BuildWorktreeInventory` (as `legacyGitDirty`,
   `legacyGitActiveOp`, `legacyBuildWorktreeInventory`) and splits the assertion in two:
   - **Equivalence set — well-formed real-Git porcelain and ordinary failures.** Over every real-Git
     fixture of rule 5 (main/linked/detached/locked/prunable/bare/missing/duplicate-branch) and over
     the ordinary command-unavailable and command-failure cases (empty `repoRoot`, non-zero exit),
     the rewritten production functions must return results equal to the legacy copies:
     `Available`, the whole `ByBranch` map — keys **and** raw-path values (§9.1) — the whole
     `Prunable` map, the dirty bool, and the operation string. The probe half of this set also
     includes the §11.2 hardening fixtures (missing `.git`, malformed/empty `gitdir:` line, missing
     or non-directory gitdir target, failing `git status`), where the wrappers of §11.3 still
     collapse to the legacy `false`/`""`. This is the compatibility the contract intends, and it is
     where "unchanged" is claimed. The inventory half of this set additionally covers a **SHA-256**
     real-Git fixture where the binary supports it (AC 23a), asserting that the legacy and rewritten
     inventories agree there too — the object-format-neutral rule of §8.3.1 is a compatibility
     requirement, not a divergence.
   - **Divergence set — malformed parser fixtures.** For the fabricated malformed-porcelain shim
     fixtures (missing `worktree` line, duplicate canonical path, malformed branch ref, an empty or
     non-lowercase-hex `HEAD` object ID, `branch`+`detached`), field equality is **not** asserted
     and must not be: the new
     contract deliberately invalidates the whole inventory (§9.2). A **well-formed 64-hex `HEAD`**
     belongs to the equivalence set, never here. The test instead asserts the
     hardening explicitly — `legacyBuildWorktreeInventory` may still report `Available == true`
     with partial maps, while the new `BuildWorktreeInventory` reports `Available == false`,
     `Err != nil`, and empty `Records`/`ByPath`/`ByBranch`/`Prunable` — and then asserts that the
     inventory's single production consumer `BuildAgentStatus` follows its already-defined
     unavailable-inventory branch with no new issue
     code, severity, message, or status value. No byte-equality claim is made for these artificial
     cases, and no claim about `tws doctor` or `tws list` is made here at all: they do not read this
     inventory (§9.1).

   `legacyGitDirty` runs `git status --porcelain` without `GIT_OPTIONAL_LOCKS=0` by definition, so
   it runs only on its own fixtures and never inside the §16.7 read-only snapshot test.
5. **Fixture matrix (named, and exercising every inventory and probe field the existing production
   consumers read).** checkout mode and
   external mode × {clean, dirty, detached HEAD, rebase in progress, prunable worktree, locked
   worktree with reason, bare main worktree, missing worktree directory, duplicate `GitBranch()`
   across entries, archived entry, cross-repo entry, repository unavailable}. Every one of these is
   built by the date-pinned builders of rule 2, under that rule's pinned fixture environment
   (`withUnifiedWorkspaceEnv` + `withIdleTmuxOnPath`, at capture and at assertion alike), and
   produces **real, well-formed** porcelain, so the
   whole matrix belongs to the equivalence set of rule 4. The malformed-porcelain fixtures are
   shim-only, live outside this matrix, produce no surface golden, and belong to the divergence set.
   The prunable, locked, bare, detached, missing, and duplicate-branch fixtures exist specifically
   because `ByBranch` and `Prunable` feed `BuildAgentStatus` and therefore `tws status`; they are
   what make the additive rewrite of §17.4 provably semantics-preserving for `tws status` on
   real Git output. The `tws doctor` surface is captured over the same matrix for a
   narrower reason: it consumes the §11.3 dirty/active-op wrappers (via `buildHeader`), not the
   worktree inventory (§9.1), so the clean, dirty, rebase-in-progress, and detached fixtures are the
   ones that actually bind it. `tws list` consumes neither and is captured purely as a broad
   regression guard (§11.3).

## 12. Parent ahead/behind counts

For each row, run at most one process:

```text
git -C <repository.dir> rev-list --left-right --count <local-head>...<parent-head>
```

- Preconditions, **all** required: `edge.Repo == ""`, `repository.dir != ""`,
  `edge.LocalHead` and `edge.ParentHead` both non-empty and both matching `ancestryFullSHA`
  (`^[0-9a-f]{40}$`). This is the one place `ancestryFullSHA` is reused, and deliberately so: both
  operands come from the shipped evaluator, which only ever publishes 40-hex peeled heads
  (`internal/stack_ancestry.go:247`), so the precondition mirrors that shipped contract rather than
  the inventory rule of §8.3.1. If any precondition fails, **no process is started** and both counts
  are `null` — which is also what a SHA-256 repository yields today, since the unchanged evaluator
  leaves such heads empty (§8.3.1 rule 5).
- Both operands are the full peeled SHAs copied from `StackEdge`; no ref name, no `HEAD`, no
  user-controlled string ever reaches this command. Because both are validated 40-hex they cannot be
  read as options, which is why no `--end-of-options` is required (same structural argument as
  `merge-base` in `stack-ancestry-doctor` §7.1 rule 2).
- Output must be exactly two non-negative decimal integers separated by a tab (optionally with a
  trailing newline). The **left** count is `parent_counts.ahead` (child-ahead), the **right** is
  `parent_counts.behind` (child-behind).
- A non-zero exit or any malformed output yields **two nulls**, never zeros.
- Unrelated histories still return the two real totals while `heads.merge_base` stays `null` — the
  three-dot form counts symmetric difference and does not require a merge base.
- Base-unset, cross-repo, missing-child, missing-base, and repo-unavailable rows never run it.

## 13. Process budget

The budget has **two boundaries**, and every statement in this spec names which one it uses:

- the **builder boundary** — `BuildStackStatus(ws, cfg, feature, featurePath, stack)`, entered once
  `ws`, `cfg`, the feature path, and the stack are already supplied. This is the boundary the
  approved `A + 2 + C + D` formula describes;
- the **end-to-end CLI boundary** — the whole `RunE` of §17.2, which additionally pays the
  prerequisite processes that produce `ws` and `cfg` in the first place.

Let:

- `A` = **all** Git processes the **already-shipped** `FeatureStackEdges` spends on this exact stack
  when called with these arguments. `A` is defined **by provenance, not by argv shape**: it is
  everything reachable from that one call, including
  - `ResolveStackAncestryRepo`'s repository-candidate resolution (`MainRepoRootIn` →
    `rev-parse --git-common-dir`, `internal/exec.go:26-41`, per candidate it tries),
  - `EvaluateStackAncestry`'s own repository validation (`MainRepoRootIn(repoDir)`,
    `internal/stack_ancestry.go:226`),
  - its default-branch helper `DefaultBranchIn(ev.repoDir)` (`internal/stack_ancestry.go:286`),
    which emits `rev-parse --abbrev-ref origin/HEAD` and, on failure, `symbolic-ref --short HEAD`
    (`internal/exec.go:69-87`),
  - its cached `rev-parse --verify --quiet --end-of-options <ref>^{commit}` and
    `rev-parse --short <sha>` work, merge-base checks, and optional identity-note probes
    (`internal/stack_ancestry.go:210-289,371-421,425-535,706-721`).

  Several of those are **infrastructure-shaped** (`rev-parse --git-common-dir`,
  `rev-parse --abbrev-ref origin/HEAD`, `symbolic-ref --short HEAD`). They are nevertheless part of
  `A`, because they are emitted *inside* `FeatureStackEdges`. Classifying them as `I` would
  double-count them and make the classes overlap. `A` is unchanged by this feature and must never be
  described as a status cost or approximated as a fixed multiplier.
- `I` = the **pre-builder infrastructure** processes the CLI spends before `BuildStackStatus` is
  entered: everything started by `RequireWorkspace()` and by the explicit `internal.LoadConfig()`
  of §17.2. `I` is not status-added and is not part of `A`; it exists so the end-to-end total can be
  stated honestly. Its members today are:
  - `RequireWorkspace()` → its own internal `LoadConfig()` → `repoConfigPath()` → `RepoRoot()` →
    one `rev-parse --show-toplevel` (`internal/workspace.go:440-441`,
    `internal/config.go:35-40,60-62`);
  - `RequireWorkspace()` → `MainRepoRoot()` → `MainRepoRootIn(cwd)` → one
    `rev-parse --git-common-dir` (`internal/workspace.go:442`, `internal/exec.go:18-41`);
  - the explicit `cfg := internal.LoadConfig()` in `RunE` → a **second**
    `rev-parse --show-toplevel` (`internal/config.go:35-40`);
  - on the fallback path only (no Git repository at the cwd), `MainRepoRoot()` fails and
    `RequireWorkspace` falls through to `inferExternalRepoRoot`, which starts one
    `rev-parse --git-common-dir` per candidate path it tries (`internal/workspace.go:339-360`,
    `MainRepoRootIn`). `I` is therefore workspace-shape-dependent on that path, which is exactly why
    it is measured rather than assumed.

  `I` is therefore a small constant for a given workspace shape — today two
  `rev-parse --show-toplevel` and one `rev-parse --git-common-dir` on the ordinary path, plus one
  `rev-parse --git-common-dir` per inference candidate on the fallback path — and every member is
  outside every loop over stack entries, so `I` never grows with `E`. `I` is **measured, not
  assumed**, by AC 36 rule 5.
- `E` = number of stack entries.
- `C` = number of rows meeting **every** precondition of §12, `0 ≤ C ≤ E`.
- `D` = number of dirty-probe invocations, successful or not: in external mode one per `present`
  row; in checkout mode at most one (the single physical checkout, probed once regardless of `E`).

Status-added processes, when `repository.dir != ""`:

| Added probe | Processes |
|---|---:|
| branch-ref `for-each-ref` inventory (§8) | 1 |
| `worktree list --porcelain` inventory (§9) | 1 |
| parent `rev-list --left-right --count` (§12) | `C` |
| dirty probes (§11.1) | `D` |
| active-operation inspection (§11.2) | 0 — filesystem only |
| branch / detached / ref-existence / upstream per row | 0 |

**Status-added total: `2 + C + D`.**
**Builder total (`BuildStackStatus`): `A + 2 + C + D`** — the approved formula, unchanged.
**End-to-end CLI total: `I + A + 2 + C + D`.**

When `repository.dir == ""`, every supplemental probe is skipped: the builder total is exactly `A`
(`C = D = 0`, both inventories skipped) and the end-to-end total is exactly `I + A`. Cross-repo rows
add zero processes of any kind.

Two `rev-parse --show-toplevel` calls on the ordinary path are an accepted, measured cost, not a
defect to be optimized away in this feature: removing the duplicate would mean changing
`RequireWorkspace` to return the config it already loaded, which is a production refactor of a
function every command depends on and is explicitly out of scope (§3). The contract here is that
both calls are outside every loop, that neither grows with `E`, and that both are **accounted for**
rather than hidden.

Tests classify every captured `git` argv **by provenance, not by shape**, using the control-run
procedure of AC 36: a builder-level control run measures `A` exactly, a CLI-prefix control run
measures `I` exactly, and the full run must equal the control plus exactly the four status-added
forms. Any invocation left unclassified fails the test. This is what prevents a future per-row
branch, ref-existence, or upstream process from being added silently.

## 14. Local-only / no-fetch contract

1. No code path in this feature runs `fetch`, `ls-remote`, `remote update`, `push`, `update-ref`,
   `reset`, `checkout`, `switch`, `rebase`, `gc`, `prune`, or `worktree prune`. There is no
   `--fetch` flag and no configuration that enables one.
2. `upstream.*` describes the configured upstream ref exactly as it exists locally right now.
3. `parent_counts` compares two local peeled commits and is never a remote-freshness claim.
4. Every successful row carries `upstream.local_only: true`; the summary carries
   `local_only: true`.
5. Human output ends with the footer of §15.5.
6. A PATH shim in tests rejects every fetch and every unexpected mutating verb (AC 33).
7. When local evidence is unavailable, the command reports `unevaluated`/`unknown`/`null`. It must
   never fabricate `clean`, attached, `none` (operation), zero counts, or "no upstream".

## 15. Human output

All output goes to `cmd.OutOrStdout()` through `FormatStackStatus(*StackStatusReport) string`.
Sections in fixed order: header, table, per-row detail lines, summary, footer.

### 15.1 Header

External:

```text
Stack status: auth (mode: external)
  Workspace:  /Users/x/myapp.tws
  Repository: /Users/x/myapp (source: worktree)
  Worktrees:  /Users/x/myapp.tws/auth/worktrees
```

Checkout:

```text
Stack status: auth (mode: checkout)
  Workspace:  /Users/x/myapp/.tws
  Repository: /Users/x/myapp (source: workspace)
  Checkout:   /Users/x/myapp (branch: auth-api, detached: no, dirty: no, op: none)
```

- Labels are padded to a common width (`Workspace:`, `Repository:`, `Worktrees:`, `Checkout:`).
- `Repository:` prints `unavailable (source: unavailable)` when `repository.dir` is null. When
  `repository.alternate` is non-null an extra line follows:
  `  Alternate:  <path> (repo-source-mismatch)`, reusing `RepoSourceMismatchLabel`.
- Every nullable header value renders as `?`: `branch: ?`, `detached: ?`, `dirty: ?`, `op: ?`. A
  null `checkout.path` renders the `Checkout:` line as `  Checkout:   unavailable`.
- Booleans render `yes`/`no`.

### 15.2 Table

One row per entry, in `entries[]` order, with a header line. Column widths are computed from the
widest value per column (like `space list`, `internal/cli/space.go:192-205`); nothing is truncated
to a fixed width.

```text
BRANCH                      ANCESTRY     HEAD     PARENT   A/B    UPSTREAM              MATERIALIZATION  FLAGS
auth-models                 current      1a2b3c4  1a2b3c4  0/0    equal:origin/models   present          -
auth-api (git: jd/api)      stale        9f8e7d6  4c3b2a1  3/1    ahead+2:origin/api    present          dirty
auth-legacy                 missing      -        4c3b2a1  -      ?                     missing          ref-missing
auth-wiki                   cross-repo-unsupported  -  -   -      ?                     cross-repo-unsupported  cross-repo,ref?
auth-orphan                 unevaluated  -        -        -      gone:origin/orphan    unknown          ref?
```

`auth-legacy` shows why `?` and `none` are not interchangeable: its branch ref does not exist, so the
branch-ref inventory holds **no record** for it, `upstream.configured` is `null`, and the cell is `?`.
`none` is reserved for a branch that exists and has no configured upstream (`configured: false`).
The summary block quoted in §15.4 illustrates the counter grammar with a different fixture; its
counters are not the counters of this table.

Cell grammar — every cell is a single space-free token, **except** `BRANCH`, which appends the
literal ` (git: <git_branch>)` when the logical name and the Git branch differ (see `auth-api`
above). Columns stay column-aligned and greppable; no other column ever emits a space inside a
value:

| Column | Value |
|---|---|
| `BRANCH` | `<name>`, plus ` (git: <git_branch>)` only when they differ — identical to `FormatCheckoutHealth`/`FormatCheckoutList` |
| `ANCESTRY` | `ancestryDisplayStatus(status)` — the shipped vocabulary, `unevaluated` for `null` |
| `HEAD` | `heads.local_short` or `-` |
| `PARENT` | `heads.parent_short` or `-` |
| `A/B` | `<ahead>/<behind>`, or `-` when either is null |
| `UPSTREAM` | `?` (configured null) · `none` (configured false) · `equal:<display>` · `ahead+N:<display>` · `behind-N:<display>` · `diverged+N-M:<display>` · `gone:<display>` |
| `MATERIALIZATION` | the `state` value verbatim |
| `FLAGS` | comma-joined tokens, or `-` when empty |

`FLAGS` tokens, emitted in this fixed order and never re-ordered:
`current` (`is_current_checkout == true`), `archived`, `cross-repo` (`repo != null`),
`detached` (`materialization.detached == true`), `dirty` (`dirty == true`),
`dirty?` (`dirty == null` and `state == present`), `op=<name>` (`active_git_op` non-null and not
`none`), `op?` (`active_git_op == null` and `state == present`), `ref-missing`
(`ref_exists == false`), `ref?` (`ref_exists == null`).

### 15.3 Detail lines

Indented six spaces under their row, in this fixed order, each printed only when it applies:

1. `reason: <reason>[ last-base=<short>][ merge-base=<short>][ base-record=<state>]` — printed when
   `ancestry.status != "current"`. The `base-record=` token is printed only when
   `base_record.state` is non-null **and** not `present`, exactly mirroring
   `checkoutFeatureDetailLines` (`internal/checkout_health.go:886-905`) so an edge that never
   consulted the record cannot claim a verdict about it.
2. the guidance string verbatim, when non-null.
3. `note: <detail>` per note, in `notes[]` order.
4. `path: <materialization.path>` when non-null.
5. `checked-out: <checked_out_branch>` when non-null.

An empty stack prints no table and no detail lines, only
`No branches tracked in stack.yaml.` between header and summary.

### 15.4 Summary

```text
Summary:
  entries: 5
  ancestry: current=2 stale=1 divergent=0 missing=1 cross-repo-unsupported=0 unevaluated=1
  materialization: present=3 archived=0 missing=1 prunable-missing=0 cross-repo-unsupported=0 unknown=1
  upstream: none=1 equal=1 ahead=1 behind=0 diverged=1 gone=0 unknown=1
  unknown: ref-exists=1 parent-counts=2 dirty=1 active-op=1
```

Counters and their order come from the summary struct fields (§6.4); no map is iterated.

### 15.5 Footer

Always the last line, unconditionally:

```text
Local-only report: no fetch was performed. Upstream and parent counts describe local refs only.
```

### 15.6 Rendering safety

- Every dynamic value that originates in `stack.yaml` or the filesystem (`name`, `git_branch`,
  `base.name`, `repo`, paths, `checked_out_branch`, `upstream.display`) is passed through
  `ancestrySanitize(v, stackStatusSanitizeLimit)` with `const stackStatusSanitizeLimit = 120`
  before printing. Non-printable runes become `?` and over-long values are truncated with `…`.
- `ancestry.guidance` and note details are printed **verbatim**: the evaluator already sanitized them
  (`internal/stack_ancestry.go:551-570`), and re-sanitizing would double-truncate.
- JSON emits every value verbatim — machine consumers need the truth, and `encoding/json` escapes
  control characters — so sanitization is a human-output rule only, exactly as in
  `stack-ancestry-doctor` §4.6.

## 16. Safety, read-only guarantees, and proof

1. Every Git process started by this feature is one of exactly four forms: the `for-each-ref`
   inventory (§8.1), the `worktree list --porcelain` inventory (§9.1), the
   `rev-list --left-right --count` parent count (§12), and `status --porcelain` with
   `GIT_OPTIONAL_LOCKS=0` (§11.1). No other verb is reachable.
2. No user-controlled string is ever passed to a Git process: the inventories take constants, the
   parent count takes validated 40-hex SHAs, and the dirty probe takes a validated non-empty
   directory path via `-C`. `git -C ""` is structurally impossible: every call site is guarded by a
   non-empty check.
3. No `-c`, no `--git-dir`, no `--work-tree`, no environment mutation other than the additive
   `GIT_OPTIONAL_LOCKS=0`.
4. stderr is discarded on every probe, so ambiguity or advice warnings can never leak into output.
5. Nothing is written: no ref, no index, no `FETCH_HEAD`, no `stack.yaml`, no `.tws/` file, no lock,
   no operation marker. `last_base_sha` is read, never written.
6. No probe touches `se.Repo` or any path outside `repository.dir` and the matched worktree paths of
   the current feature.
7. **Read-only proof (test contract).** Snapshots are taken before and after each of
   `tws stack status <f>` and `tws stack status <f> --json`, over the main repository and every
   linked worktree, and must be identical:
   - `git rev-parse --all`, `git for-each-ref refs/heads refs/remotes refs/tags`, `git reflog --all`;
   - `packed-refs` bytes and the presence/bytes of `FETCH_HEAD`;
   - the main index **and every linked-worktree index**, compared on content bytes, mode, size, and
     **mtime** — this is the `GIT_OPTIONAL_LOCKS=0` proof;
   - the recursive file tree of the repository, of every linked worktree, and of the metadata root,
     collected with the shipped `collectStableTreePaths`/`isTransientGitLockPath` rule
     (`internal/cli/space_test.go:87-138`), plus `stack.yaml` bytes compared separately;
   - the presence/absence of every active-operation marker (`rebase-merge`, `rebase-apply`,
     `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `BISECT_LOG`) in the common Git dir and in each
     per-worktree Git dir.

   The tree comparison is **not** raw recursive equality. Transient Git lock files under `.git` —
   any path with a `.git` segment whose base name ends in `.lock`, such as
   `.git/objects/maintenance.lock` or `.git/index.lock` — are excluded **during traversal**, and a
   not-exist walk error for such a path is tolerated. macOS Git background maintenance creates and
   removes those files on its own schedule, so including them reintroduces exactly the flake fixed
   by `fix-space-test-git-maintenance-race`, and a lock that vanishes between the directory read and
   the stat of its entry surfaces as a *walk error*, not as a listed path — which is why filtering a
   completed result set is insufficient and is forbidden here. Every other path is retained, so the
   exclusion can never hide a side effect: refs, `packed-refs`, `FETCH_HEAD`, indexes, operation
   markers, `.git/tws/*`, `.git/info/exclude`, and all worktree and metadata content are still
   compared, and none of those paths ends in `.lock`. The ref, index, `FETCH_HEAD`, metadata, and
   operation-marker assertions above are unaffected and remain byte-level.
8. Cross-repo isolation is proven at the filesystem level: the foreign repository's tree and
   `git rev-parse --all` are unchanged, and the PATH shim records no invocation naming its path.

## 17. Implementation plan — files and symbols

### 17.1 New — `internal/stack_status.go` (package `internal`)

Consts: `stackStatusSchema = 1`, `stackStatusSanitizeLimit = 120`, `StackStatusOpNone = "none"`, the
materialization/upstream state string constants.

Package-level var: `stackStatusObjectID = regexp.MustCompile("^[0-9a-f]+$")` — the
object-format-neutral inventory object-ID matcher of §8.3.1, used by `parseBranchRefInventory`
(field 2) and by the `HEAD` line of the §9.2 worktree parser, and by nothing else. It is
**unexported**, is parser validation only, and deliberately constrains no length, so SHA-1 (40-hex)
and SHA-256 (64-hex) repositories parse identically.

Types: `StackStatusReport`, `StackStatusWorkspace`, `StackStatusRepository`, `StackStatusExternal`,
`StackStatusCheckout`, `StackStatusEntry`, `StackStatusBase`, `StackStatusHeads`,
`StackStatusBaseRecord`, `StackStatusAncestry`, `StackStatusNote`, `StackStatusParentCounts`,
`StackStatusMaterialization`, `StackStatusUpstream`, `StackStatusSummary`,
`StackStatusAncestryCounts`, `StackStatusMaterializationCounts`, `StackStatusUpstreamCounts`,
`StackStatusUnknownCounts`, `BranchRefInventory`, `BranchRefRecord`.

Functions:

- `BuildStackStatus(ws Workspace, cfg Config, feature, featurePath string, stack Stack) (*StackStatusReport, error)`
  — the strict builder; the single caller of `FeatureStackEdges`;
- `NormalizeStackStatus(*StackStatusReport)` — nil-slice normalization;
- `FormatStackStatus(*StackStatusReport) string` — §15;
- `LoadStackForStatus(featurePath, feature string) (Stack, error)` — the classified loader of §5
  (`os.ReadFile(StackPath(...))` + `yaml.Unmarshal`; `LoadStack` is **not** modified). It is the
  only stack-loading entry point package `cli` uses: after unmarshalling it calls
  `validateStackForStatus` (§5.1) and, on failure, returns
  `fmt.Errorf("invalid stack.yaml for feature %s: %w", feature, err)`, so a successfully
  returned `Stack` is structurally valid by construction;
- `validateStackForStatus(Stack) error` — §5.1, **unexported** and called only from
  `LoadStackForStatus`; it adds no package API surface for package `cli` to consume;
- `BuildBranchRefInventory(repoDir string) BranchRefInventory` and
  `parseBranchRefInventory([]byte) (map[string]BranchRefRecord, error)` — §8;
- `parseUpstreamTracking(upstream, track, trackShort string) (state string, ahead, behind *int, err error)` — §8.4;
- `stackStatusParentCounts(repoDir, localHead, parentHead string) (*int, *int)` — §12;
- `probeDirty(path string) (bool, error)`, `probeActiveGitOp(path string) (string, error)` — §11.

Reused unchanged: `FeatureStackEdges`, `ancestryEdgesFor`, `ancestryWorktreeCandidatePath`,
`ancestryDisplayStatus`, `ancestrySanitize`, `canonicalize`, `strPtr`, `boolPtr`,
`intPtr`, `RepoSourceMismatchLabel`, and `ancestryFullSHA` — the last **only** in the §12
parent-count preconditions, where the shipped evaluator's full-peeled-head contract genuinely
requires it. It is never used for inventory parsing (§8.3.1 rule 2).

### 17.2 New — `internal/cli/stack_status.go`

`stackStatusCmd() *cobra.Command`, `stackStatusArgs(*cobra.Command, []string) error`. `RunE`, in
this order:
`SilenceUsage` → `RequireWorkspace` → `cfg := internal.LoadConfig()` → `GuardFeatureName` →
`ResolveFeaturePath` → feature-dir stat (`feature not found: <feature>` when it does not exist or is
not a directory, §5) → `LoadStackForStatus` →
`BuildStackStatus(ws, cfg, feature, featurePath, stack)` → `NormalizeStackStatus` → encode or
format.

There is no separate validation call in this sequence: `validateStackForStatus` is unexported in
package `internal` (§5.1) and runs inside `LoadStackForStatus`, so `RunE` cannot reach a
structurally invalid stack. The `invalid stack.yaml for feature <feature>: <detail>` error therefore
arrives through the same `LoadStackForStatus` error return as the missing/unreadable/invalid-YAML
classifications, and `RunE` handles all four identically — fatal, non-zero exit, zero bytes on
stdout (§5).

`cfg Config` has exactly one source: `internal.LoadConfig()`, called **once** in `RunE`, outside
every loop, and threaded into `BuildStackStatus` as a parameter. `BuildStackStatus`,
`FeatureStackEdges`, and every helper below them never call `LoadConfig` themselves. `LoadConfig`
returns a `Config` and **no error**: it silently merges the global and per-repo files and falls back
to zero-valued defaults when either is missing or unreadable (`internal/config.go:60-113`). That
error/fallback convention is the repository's existing one for command `RunE` bodies — verified at
`internal/cli/doctor.go:58`, `internal/cli/new.go:83`, and `internal/cli/sync_helpers.go:238`, all of
which write `cfg := internal.LoadConfig()` with no error handling — and this feature neither changes
it nor adds a config-failure error path.

Per-repo config discovery calls `RepoRoot()`, which starts one `rev-parse --show-toplevel`
(`internal/config.go:35-40`), so **each** `LoadConfig()` costs one such process.
`RequireWorkspace()` already calls `LoadConfig()` internally (`internal/workspace.go:440-441`), so
the explicit `cfg := internal.LoadConfig()` in `RunE` is the **second** call: on the ordinary path
this command pays `rev-parse --show-toplevel` **twice**, plus the single `rev-parse --git-common-dir`
that `RequireWorkspace` → `MainRepoRoot` starts. Calling `LoadConfig` exactly once *in this `RunE`*
keeps that count at two and outside every loop; it does **not** reduce it to one, and no statement in
this spec may claim a single `rev-parse --show-toplevel`. All three are class `I` under §13 and
AC 36 — pre-builder infrastructure, never status-added probes — and are measured by the CLI-prefix
control run of AC 36 rule 5. Collapsing the duplicate would require changing `RequireWorkspace` to
hand back the config it already loaded: a production refactor of a function every command depends
on, and deliberately **not** attempted here (§3).

### 17.3 Changed — `internal/cli/stack.go`

One `AddCommand(stackStatusCmd())`; the parent `ValidArgsFunction` drops an exact `status` element.
`RunE` is untouched.

### 17.4 Changed — `internal/agent_status.go`

`WorktreeRecord` added; `WorktreeInventory` gains `Records`, `ByPath`, `Err`;
`BuildWorktreeInventory` rewritten to the §9.2 parser while preserving `Available`, `ByBranch` (keys
**and** raw-path values), and `Prunable` semantics byte-for-byte for its **one production consumer**
`BuildAgentStatus` (→ `tws status`) and for its direct callers and tests (§9.1) **on
well-formed real-Git porcelain, on SHA-256 repositories (§8.3.1), and on ordinary
command-unavailable/failure cases**. `tws doctor` and `tws list` never call it, so no claim about
them is made or needed here. On malformed
porcelain the rewrite is deliberately stricter and fails closed (§9.2); that divergence is asserted
as intentional, never as equality. Both halves are proven at the source by the scoped
legacy-equivalence assertion of §11.4 rule 4 and at the surfaces by §11.4 rules 1-3 (AC 35).
No other function changes.

### 17.5 Changed — `internal/checkout_health.go`

`gitDirty` and `gitActiveOp` become the wrappers of §11.3. No other function, string, severity, or
count changes.

### 17.6 Not changed

`internal/stack_ancestry.go`, `internal/health.go`, `internal/checkout_sync.go`, `internal/stack.go`
(`LoadStack`, `TopoSort`, `PrintTree`), `internal/cli/status.go`, `internal/cli/doctor.go`,
`internal/cli/list.go`, every sync implementation, and every patch-identity-adjacent file.
In particular `ancestryFullSHA` and every ancestry classification rule in
`internal/stack_ancestry.go` keep their exact current semantics, including their 40-hex assumptions
(§8.3.1 rules 2 and 5): this feature makes the supplemental inventories object-format neutral and
makes **no** change to the ancestry evaluator.

### 17.7 Tests

- `internal/stack_status_test.go` — builder, parsers, probes, formatter, schema, determinism, the
  direct unit tests of the unexported `validateStackForStatus` and of `LoadStackForStatus`'s
  classification and `invalid stack.yaml for feature <feature>: <detail>` wrapping (both reachable
  because the test is in package `internal`), the builder-boundary process budget of AC 36
  rules 1-4 (`TestStackStatus_ProcessBudget`), the parser half of
  `TestStackStatus_InventoryObjectFormatNeutral` (AC 23a, deterministic and never skipped), and the
  source-level legacy-equivalence half of AC 35 (§11.4 rule 4).
- `internal/cli/stack_status_test.go` — command surface, legacy preservation, completion, help and
  usage snapshots, fatal/exit behaviour, the read-only snapshot proof of §16.7 (AC 34), the
  end-to-end process budget of AC 36 rule 5 (`TestStackStatus_ProcessBudgetEndToEnd`, which must
  execute `RunE`), the real-Git (feature-detected) half of AC 23a, and the surface-golden half of
  AC 35 (§11.4 rules 1-3).
- `internal/agent_status_test.go` — extended for the additive inventory, including the 40-hex and
  64-hex `HEAD` parser cases of AC 23a/AC 27; existing
  `TestBuildWorktreeInventory` (`internal/agent_status_test.go:876-912`) must keep passing unchanged.
- `internal/cli/testdata/existing_commands/**` — the pinned pre-change goldens of §11.4 rule 1.

`TestStackStatus_ReadOnlySnapshots` lives in package `cli` because the lock-tolerant traversal it
must use — `isTransientGitLockPath`, `collectStableTreePaths`, and `snapshotTreeIgnoringGitLocks`
(`internal/cli/space_test.go:87-155`) — is a package-`cli` **test** helper set. It is reused
in-place: it is neither copied into package `internal` nor promoted into production API, because
duplicating the rule is how the `fix-space-test-git-maintenance-race` fix would silently drift, and
exporting it would pollute the production surface for a test-only concern. If a future test needs
the same traversal from package `internal`, the helpers move to a shared internal test package in
that feature — not this one.

Fixtures use `setupHealthTestRepo`/`gitInTest` (`internal/checkout_health_test.go:40-131`) for
package-`internal` checkout-mode tests and `setupGitRepo`/`withWorkspaceEnv`
(`internal/cli/new_integration_test.go:135-161`)
plus `createWorktree` (`internal/cli/new.go:163`) for external mode, with real local bare remotes.
Package-`cli` checkout-mode tests use the package-`cli` pattern `setupGitRepoCheckout`/`gitInDir`
(`internal/cli/checkout_lifecycle_test.go:15-63`) instead, since `setupHealthTestRepo`/`gitInTest`
are unexported package-`internal` helpers.
No mocks and no fake `git` except the deliberate PATH shim of AC 33/36. The `tmux` stub that
`withIdleTmuxOnPath` installs is not an exception to that rule — it fakes no Git behaviour; it only
pins the tmux inventory, which is host state rather than fixture state (§11.4 rule 2).

**Exception — AC 35 goldens.** Those helpers leave `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE` unset, so
commit object IDs differ on every run. The pinned-golden fixtures of §11.4 rule 1 therefore use the
date-pinning builders of §11.4 rule 2, which live in package `cli` and are built on the
package-`cli` setup patterns only — `setupGitRepoCheckout`/`gitInDir`
(`internal/cli/checkout_lifecycle_test.go:15-63`) for checkout mode and
`setupGitRepo`/`createWorktree` for external mode — wrapped with fixed author/committer dates,
so SHAs baked into `internal/cli/testdata/existing_commands/**` stay stable across runs and
machines. The package-`cli` half of AC 35 (`TestStackStatus_ExistingCommandsUnchanged`) never calls
a package-`internal` test helper; its package-`internal` half
(`TestStackStatus_LegacyProbeEquivalence`) is unconstrained and may use them freely. Those fixtures
additionally replace
`withWorkspaceEnv` with `withUnifiedWorkspaceEnv`
(`internal/cli/space_guard_test.go:56-76`) and add `withIdleTmuxOnPath`
(`internal/cli/status_test.go:16-42`), per §11.4 rule 2, so `HOME`, `XDG_DATA_HOME`, a host
`TWS_ROOT`, and
the tmux inventory cannot vary by host. Both are package-`cli` test helpers, which is a second
reason `TestStackStatus_ExistingCommandsUnchanged` lives in package `cli`; like the traversal
helpers above they are reused in place and are neither copied nor exported. Every other test in this
feature — including every package-`internal` test — may keep using the existing helpers as-is.

## 18. Documentation and skills

All in the same commit, because `assets/skills/**` is `go:embed`-compiled and would otherwise ship
stale:

1. `README.md` command table — add `| tws stack status <feature> [--json] | Stack ancestry, materialization, and upstream status |` after the `tws stack` row (~line 129).
2. `docs/cheatsheet.md` — in "See what you have" (~line 107) add `tws stack status auth` and
   `tws stack status auth --json | jq '.entries[] | select(.ancestry.status!="current")'`, plus a
   short paragraph: local-only/no-fetch, null means unknown, exit 0 on any reportable state, and
   `tws stack -- status` for a feature literally named `status`.
3. `docs/configuration.md` (~line 122) — after the `tws stack <feature>` tree example, note that
   `tws stack status <feature>` reports per-entry ancestry, materialization, upstream, and parent
   counts, and that the legacy tree is unchanged.
4. `docs/roadmap.md` — move "Stack status" from the P1 backlog into the shipped foundations list of
   "Now", keep the `StackEdge`-consumption boundary sentence, and keep patch identity in
   stretch/research.
5. `docs/engineering-workflow.md` — update the "Current shipped checkout slices" list and replace
   the "Next roadmap feature: **stack status**" paragraph (~lines 20-30) with the next target.
6. `CHANGELOG.md` (`## Unreleased`) — the new command; `schema_version: 1` with no `stack_state` and
   no timestamp; ancestry projected from the shipped evaluator; local-only/no-fetch; nullable
   dirty/active-op/upstream/parent counts; the accepted `tws stack --help` drift and the
   `tws stack -- status` escape hatch; the parent completion dedup; the additive worktree inventory;
   and the note that `tws status`, `tws doctor`, and `tws list` output is unchanged for real,
   well-formed Git output and ordinary command failures, while their dirty probe no longer refreshes
   the index, and that the shared worktree inventory — read in production only by `tws status` —
   now fails closed on malformed porcelain (previously tolerated
   as a partial inventory), in which case `tws status` reports the worktree inventory as
   unavailable. The entry must also state that the supplemental worktree and branch-ref inventories
   are object-format neutral (SHA-1 and SHA-256 object IDs alike), and must **not** claim that stack
   ancestry itself is SHA-256-ready.
7. `assets/skills/claude/tesseraworkspaces/SKILL.md` — a command-table row next to
   `tws stack <feature>` (~line 31) and a short section after the ancestry material (~line 284)
   covering: consumes the shipped ancestry projection, local-only, `null` means unknown,
   `is_current_checkout` is always null in external mode, and exit 0 on any reportable state.
8. `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` — add `tws stack status <feature> --json`
   to the "View state" block (~line 18) and to the pre-sync step of the workflow (~line 83): check
   ancestry and dirty state before `tws sync`.
9. `assets/skills/copilot/tws.prompt.md` — the command list (~line 22) and the workflow (~line 134).
10. All three skills carry the same verbatim caveats: *"`tws stack status` never fetches; upstream and
    parent counts describe local refs only"*, *"a null field means tws could not establish the fact
    locally — it never means clean, attached, zero, or no upstream"*, and *"`tws stack -- status`
    prints the legacy tree for a feature literally named `status`"*.

## 19. Dependencies

Already registered in `.tpatch/features/stack-status/status.json` and requiring **no** metadata
change in this phase:

| Parent | Kind | Why |
|---|---|---|
| `stack-ancestry-doctor` | hard | owns `StackEdge`, `FeatureStackEdges`, `ancestryEdgesFor`, `ancestryWorktreeCandidatePath`, the display vocabulary |
| `agent-work-status-dashboard` | hard | owns `WorktreeInventory` (extended additively here) and the nullable-probe/materialization conventions |
| `cobra-migration` | hard | command tree, `RunE`, `SilenceUsage`, writers |
| `fix-missing-completions` | hard | `ValidArgsFunction` conventions the parent/child completion changes extend |
| `archive-worktree` | hard | archived-entry semantics used by external materialization |
| `skill-distribution` | soft | embedded skills updated in §18 |

Run `tpatch feature deps --validate-all` before implementation and again before landing.

No new Go module dependency is introduced: `encoding/json`, `os/exec`, `strconv`, `strings`,
`path/filepath`, and `gopkg.in/yaml.v3` are already in use.

## 20. Acceptance criteria

Runnable. `make build` first; `tws` means `./bin/tws`. Go-level criteria run with
`go test ./internal/... ./internal/cli/... -run <Name> -count=1`. Every Git fixture is a real
temporary repository with real local bare remotes and real linked worktrees.

**Command surface and legacy preservation**

1. `TestStackStatus_LegacyTreeUnchanged`: capture `tws stack <f>` stdout with `captureStdout` on a
   three-entry stack before and after this feature's command is registered (the latter in-process,
   the former from a pinned golden string); the bytes, the trailing newline, and exit 0 are
   identical. A cycle fixture additionally produces the exact line
   `Warning: cycle detected in stack.yaml` on stdout, and a feature with no `stack.yaml` produces
   exactly `no stack.yaml found for feature: <f>` with a non-zero exit.
2. `TestStackStatus_HelpDrift`: `tws stack --help` contains an `Available Commands:` section listing
   `status`; the snapshot is pinned in the test. `tws stack a b` prints Cobra's
   `accepts 1 arg(s), received 2` **with** a usage block. Both are the accepted drift of §4.5.
3. `TestStackStatus_ZeroArgHint_NoStatusFeature`: with no feature named `status`,
   `tws stack status` exits non-zero with exactly `accepts 1 arg(s), received 0` and prints a usage
   block.
4. `TestStackStatus_ZeroArgHint_StatusFeatureExists`: with a feature named `status`,
   `tws stack status` exits non-zero and its error contains all three of `tws stack status status`,
   `tws stack -- status`, and `accepts 1 arg(s), received 0`.
5. `TestStackStatus_LiteralStatusFeature`: `tws stack -- status` prints the legacy tree of the
   feature named `status`; `tws stack status status` prints its status report; both exit 0.
6. `TestStackStatus_Completion`: the parent `ValidArgsFunction` output contains **exactly one**
   `status` candidate when a feature named `status` exists (contributed by Cobra, not by the
   feature list); the child's output contains every feature name **including** `status`; both return
   `ShellCompDirectiveNoFileComp`.
7. `TestStackStatus_Writers`: the whole status report (human and `--json`) is captured through
   `cmd.SetOut(&buf)` with **zero** bytes reaching process `os.Stdout`; the legacy tree is captured
   only through `captureStdout`.
8. `TestStackStatus_SilenceUsage`: a runtime failure (a missing feature directory, which reports
   `feature not found: <feature>`) prints no `Usage:` block; an
   arity error does.

**Fatal boundary and exit semantics**

9. `TestStackStatus_FatalEmptyStdout`: for each of a missing feature directory, unsafe feature name
   (`../evil`), a feature name owned by a registered space, missing `stack.yaml`, unreadable
   `stack.yaml` (mode `0000`), invalid YAML, a duplicate entry name, and an empty entry name —
   exit is non-zero, stdout is **empty** in both human and `--json` mode, and stderr contains the
   §5 message **verbatim**, including the exact string `feature not found: <feature>` for the
   missing-directory case.
10. `TestStackStatus_ReportableExitZero`: `stale`, `divergent`, `missing`, `cross-repo`,
    base-unset/unevaluated, dirty-worktree, prunable-worktree, and repo-unavailable fixtures each
    exit 0 and emit a full document.
11. `TestStackStatus_NoStackStateKey`: the decoded document has exactly the five top-level keys of
    §6.1 and contains no key named `stack_state` at any depth.
12. `TestStackStatus_EmptyStack`: `branches: []` exits 0, emits `"entries": []` (never `null`),
    `summary.entries == 0`, and the human view prints `No branches tracked in stack.yaml.`

**Schema**

13. `TestStackStatus_KeySet`: decoding into `map[string]any` asserts the **exact** key set at every
    level of §6.1-6.4, in both modes, for an empty stack and a fully populated one. A `grep -n
    'omitempty' internal/stack_status.go` finds nothing (asserted in-test by reading the file).
14. `TestStackStatus_NullsNotZeros`: for a fixture with base-unset, cross-repo, missing-ref, and
    failed-probe rows, every affected scalar decodes as JSON `null` — not `""`, `0`, or `false` —
    and every list decodes as `[]`.

    **14a. `TestStackStatus_MetadataRootAsHeld`** — with an external workspace root **configured
    verbatim**
    in `cfg.Workspaces` as a non-canonical (relative or symlinked) path, `workspace.metadata_root`
    equals `ws.MetadataRoot` **byte-for-byte** — the report re-canonicalizes nothing (§6.2 rule 3) —
    while the authoritative joins are unaffected: `external.worktrees_root` is still canonical and
    the external materialization lookup still resolves through `canonicalize(candidate)` against the
    canonical `ByPath` keys, so materialization states are identical to the canonical-root twin
    fixture.
15. `TestStackStatus_Deterministic`: two consecutive `--json` runs and two human runs over an
    unchanged repository produce byte-identical output; no `generated_at`-like key exists.
16. `TestStackStatus_RowOrder`: a stack whose YAML order differs from topological order, contains a
    cycle, and contains two entries with the same `GitBranch()` emits rows in exact YAML order, with
    both duplicate rows present, distinct `name`s, identical `upstream` objects, and no cycle
    warning.

**Ancestry projection**

17. `TestStackStatus_AncestryFieldForField`: for a mixed fixture, every row's `base`, `ref_exists`,
    `heads`, `base_record`, `ancestry`, and `repo_source` is compared field-for-field against the
    `StackEdge` that `FeatureStackEdges` returns for the same stack in the same workspace.
18. `TestStackStatus_RefExistsFollowsRefProbed`: `ref_exists` is `null` exactly for rows with
    `RefProbed == false` (base-unset, cross-repo, repo-unavailable) and equals `RefExists`
    otherwise. A base-unset row additionally has null heads, null `base_record.state`, and null
    `parent_counts` **even though** a branch-ref inventory record exists for its branch.
19. `TestStackStatus_EdgeSliceTotality`: with a stub that returns a short edge slice,
    `ancestryEdgesFor` produces one unevaluated row per entry, and no row renders as `current`.
20. `TestStackStatus_UnrelatedHistories`: an orphan-branch fixture reports `divergent`,
    `heads.merge_base == null`, and **real** non-zero `parent_counts.ahead`/`.behind`.

**Branch-ref inventory**

21. `TestStackStatus_UpstreamStatesRealGit`: a real repository plus a real local bare remote produces
    all seven states — no-upstream, equal, ahead, behind, diverged, gone, and a **local-branch**
    upstream (`branch.<n>.merge = refs/heads/main`) — and each maps to the §8.4 row, with `equal`
    reporting `0/0`, `none` and `gone` reporting null counts, and `gone` never conflated with either.
22. `TestStackStatus_RefInventoryRawFormat`: the captured argv contains the exact format string with
    `%00` separators and the literal `refs/heads/` pattern, and contains no `|`, no
    `%(refname:short)`, and no `%(upstream:short)`. The raw fixture bytes fed to the parser contain
    NUL bytes and newline record separators.
23. `TestStackStatus_RefInventoryFailClosed`: table-driven over wrong field count, a ref without the
    `refs/heads/` prefix, an empty branch remainder, an **empty** object ID, an object ID containing
    a character outside `[0-9a-f]` (including an uppercase-hex one), an upstream
    lacking the `refs/` prefix, each unaccepted `(track, trackshort)` pair, a non-numeric ahead
    count, an interior empty record, and a duplicate key — each invalidates the **whole** inventory;
    the report still exits 0 and **every** row's `upstream.configured`/`state`/counts are null while
    ancestry fields are unaffected. The table **must not** contain any "wrong length" case: a
    well-formed hex object ID of any length — 40, 64, or otherwise — is valid by §8.3.1 and is
    covered as a positive case by criterion 23a, never as a failure here.

    **23a. `TestStackStatus_InventoryObjectFormatNeutral`** — the object-format-neutrality contract
    of §8.3.1, in two halves:
    - **Parser half (mandatory, deterministic, no Git process).** Fixture byte slices are fed
      directly to `parseBranchRefInventory` and to the §9.2 worktree parser with object IDs of
      **40 hex** and of **64 hex** characters. Both yield a parsed record, `Available == true`, and
      a `Head`/objectname stored **verbatim** — byte-for-byte the input token, not truncated,
      re-cased, or re-abbreviated. The same fixtures with an empty token and with a
      non-lowercase-hex token fail closed. A grep-style in-test assertion pins that
      `internal/stack_status.go` contains no `[0-9a-f]{40}` literal outside the §12 parent-count
      precondition and never passes `ancestryFullSHA` to either inventory parser.
    - **Real-Git half (feature-detected).** A fixture created with
      `git init --object-format=sha256` (plus a real commit and a real linked worktree in external
      mode) yields `BuildWorktreeInventory(...).Available == true` with a 64-hex `Head`, a
      branch-ref inventory whose `Available == true`, and a `tws stack status` report whose
      `materialization`, `checked_out_branch`, `detached`, and `upstream.*` fields are populated
      exactly as on the SHA-1 twin fixture. The same fixture is asserted **not** to change
      `tws status` materialization or prunable semantics: `BuildAgentStatus` over it reports the
      same `ByBranch`/`Prunable`-derived materialization and prunable values as the SHA-1 twin.
      Ancestry fields are asserted **only** as "unchanged from the current evaluator's behaviour"
      (they may be unevaluated/null); this criterion makes **no** claim that SHA-256 ancestry works
      (§8.3.1 rule 5). If the CI Git binary cannot create a SHA-256 repository, this half
      feature-detects (`git init --object-format=sha256` in a scratch dir) and `t.Skip`s with an
      explicit reason; the parser half never skips.
24. `TestStackStatus_BranchTagCollision`: a branch `dup` and a tag `dup` pointing elsewhere; the row
    joins on `refs/heads/dup`, its upstream facts match the branch, and no output contains
    `is ambiguous`.
25. `TestStackStatus_JoinKeyEqualsChildRef`: for every probed row, the derived key
    `"refs/heads/" + GitBranch()` equals `StackEdge.ChildRef`; cross-repo rows perform no lookup and
    report an all-null `upstream` except `local_only: true`.

**Worktree inventory and mode truth**

26. `TestBuildWorktreeInventory_Additive`: a repository with main, attached linked, detached linked,
    locked (with reason), and prunable worktrees yields one `Records` entry per block keyed by
    canonical path, with correct `Head`, `BranchRef`, `Detached`, `Locked`/`LockReason`,
    `Prunable`/`PrunableReason`; `Available`, `ByBranch` (keys **and** raw porcelain path values),
    and `Prunable` keep exactly their pre-feature values (the existing `TestBuildWorktreeInventory`
    passes unchanged), and `Record.Path`/`ByPath` are canonical while the matching `ByBranch` value
    is the raw string — asserted on a fixture whose temp root differs before and after
    `filepath.EvalSymlinks` (§9.1).
27. `TestBuildWorktreeInventory_FailClosed`: a block with no `worktree` line, a duplicate canonical
    path, a malformed branch ref, a malformed `HEAD` object ID (empty, and containing a character
    outside `[0-9a-f]`), and a block with both `branch` and `detached`
    each yield `Available == false`, a non-nil `Err`, and empty `Records`/`ByPath`/`ByBranch`/
    `Prunable` — the shape `BuildAgentStatus`, the inventory's only production consumer (§9.1),
    already handles. A `HEAD` line of **64 lowercase hex**
    characters is asserted in the same table to be **valid** (`Available == true`, `Head` stored
    verbatim), so the malformed-`HEAD` case can never be re-tightened into a 40-length rule
    (§8.3.1). The same fixtures are fed to
    `legacyBuildWorktreeInventory`, and the test asserts the **divergence** is deliberate: the
    legacy parser may report `Available == true` with partial maps on exactly these inputs, so this
    is a documented hardening (§9.2) and **not** an equivalence case. These fixtures are shim-only;
    no real-Git fixture can produce them.
28. `TestStackStatus_ExternalMaterializationMatrix`: attached, detached, wrong-branch, prunable,
    missing, archived-missing, unsafe-name, and duplicate-branch rows map to §9.3 exactly, with
    `checked_out_branch`/`detached` from the porcelain record and **no** per-row branch probe in the
    captured argv.
29. `TestStackStatus_ExternalIsCurrentCheckoutAlwaysNull`: every external row in every fixture has
    `is_current_checkout == null`, and the human `FLAGS` column never contains `current`.
30. `TestStackStatus_ExternalStrayDirectory`: a directory at the expected worktree path that Git does
    not list reports `missing` here, while `tws status --json` for the same fixture still reports
    `present` — the deliberate, documented divergence of §9.3.
31. `TestStackStatus_CheckoutModeTruth`: attached checkout populates `workspace.checkout.branch`,
    `detached:false`, and copies `checked_out_branch`/`dirty`/`active_git_op` onto **only** the
    matching same-repository row(s), including both duplicate rows sharing that branch; a cross-repo
    row whose `git_branch` **equals** the checked-out branch reports
    `state: "cross-repo-unsupported"` with `checked_out_branch`, `detached`, `dirty`,
    `active_git_op`, and `is_current_checkout` all `null` and starts no process naming the foreign
    repository; detached checkout yields
    `branch: null`, `detached: true`, and `is_current_checkout == null` on **every** row; an
    unavailable inventory yields null branch/detached and no attached/clean claim; a base-unset row
    has `materialization.state == "unknown"`.

**Probes and read-only**

32. `TestStackStatus_ProbesAreTriState` (package `internal`): `probeActiveGitOp` returns an
    **error** — never `StackStatusOpNone` — for each of: a path with no `.git`; a `.git` file with
    no `gitdir: ` prefix; a `.git` file with an empty `gitdir:` remainder; a `.git` file whose
    resolved target does not exist; a `.git` file whose resolved target exists but is a regular
    file; and a marker whose `os.Stat` fails with a non-`fs.ErrNotExist` error. A healthy clean
    worktree yields `(StackStatusOpNone, nil)`, and a repository with a real interrupted rebase
    yields `("rebase", nil)`. `probeDirty` returns an error for an empty path and for a directory
    where `git status` fails. At the report level, the missing-gitdir worktree and the failing
    `git status` directory each yield `dirty: null` and `active_git_op: null`, never `false` or
    `"none"`, while a healthy worktree yields `false`/`"none"`. The §11.3 wrappers are asserted on
    the same fixtures to still return `false` and `""`, so no existing command changes.
33. `TestStackStatus_NoFetchShim`: with a PATH shim `git` recording argv, no invocation contains
    `fetch`, `ls-remote`, `push`, `update-ref`, `reset`, `checkout`, `switch`, `rebase`, `gc`,
    `prune`, or `worktree prune`, and no invocation names the cross-repo fixture's path. `grep -rn
    -- "--fetch" internal/stack_status.go internal/cli/stack_status.go` finds nothing.
34. `TestStackStatus_ReadOnlySnapshots` (package `cli`, `internal/cli/stack_status_test.go`, so the
    `internal/cli/space_test.go:87-155` traversal helpers are in scope — §17.7): the full §16.7
    snapshot set is identical before and after both output modes, over the main repository and every
    linked worktree. File-tree snapshots are taken with `collectStableTreePaths`/
    `snapshotTreeIgnoringGitLocks`, which drop transient `.git` lock files **during** the walk and
    tolerate a not-exist walk error for such a path; no filtering of a completed listing is
    permitted. Three control assertions keep the proof sharp: (a) the index comparison includes
    **mtime**, and the same fixture's index mtime **does** change when `git status --porcelain` runs
    without `GIT_OPTIONAL_LOCKS=0`; (b) writing a non-lock file under `.git` (for example
    `.git/tws/probe`) between two snapshots **is** detected, proving the lock exclusion did not
    blind the traversal; (c) creating and removing a `.git/objects/maintenance.lock` between two
    snapshots is **not** detected, proving the exclusion actually covers the maintenance race.
35. `TestStackStatus_ExistingCommandsUnchanged` (package `cli`) and
    `TestStackStatus_LegacyProbeEquivalence` (package `internal`) implement the baseline method of
    §11.4 and nothing weaker. `tws status`, `tws status --json`, `tws doctor` (checkout and
    external), and `tws list` are run over the §11.4 rule 5 fixture matrix — all real, well-formed
    Git output, built with the **date-pinned** builders of §11.4 rule 2 — and compared against the
    goldens captured at the parent commit before any production edit, with identical exit codes.
    Every fixture, at golden capture and at assertion alike, installs the pinned fixture environment
    of §11.4 rule 2: `withUnifiedWorkspaceEnv(t, repo)`
    (`internal/cli/space_guard_test.go:56-76`), which pins `HOME` and `XDG_DATA_HOME` to fresh
    temporary directories and **clears** the host `TWS_ROOT`
    — the narrower `withWorkspaceEnv` is **not** accepted, since it leaves `XDG_DATA_HOME`
    unset and lets the host global registry leak — and `withIdleTmuxOnPath(t)`
    (`internal/cli/status_test.go:16-42`), which makes `RealTmuxInventory` resolve as available with
    no server and no sessions, so the `tmux-missing` issue is never emitted and `tmux has-session`
    always answers "absent". A host with or without tmux, with or without a running tmux server, and
    with any global registry, global config, or host `TWS_ROOT` must produce identical goldens; this
    is a fixture-environment requirement and adds **no** fourth normalization rule.
    Human surfaces are compared as raw bytes after the three normalization rules of §11.4 rule 3
    (fixture-root paths, the fixture's exact computed workspace stable ID, and — JSON only —
    `generated_at` deletion); the JSON surface is decoded, `generated_at` is asserted present and
    deleted, paths and the stable ID are normalized, and the documents are compared semantically —
    raw byte equality of a wall-clock document is impossible and is **not** claimed. Commit SHAs are
    pinned by the date-pinned fixtures and are compared **verbatim**; the stable-ID rule replaces
    only that one computed 16-hex value by literal match and never a `[0-9a-f]{16}` pattern, so no
    SHA can be normalized away. A residual `os.TempDir()`-rooted path or a residual occurrence of
    the fixture's computed stable ID in any normalized artifact **fails** the test.

    `TestStackStatus_LegacyProbeEquivalence` pins verbatim copies of the parent commit's `gitDirty`,
    `gitActiveOp`, and `BuildWorktreeInventory` and splits its assertion exactly as §11.4 rule 4
    requires:
    - **equivalence set** — the whole real-Git matrix plus the ordinary command-unavailable/failure
      and probe-hardening cases: field-for-field equality with the rewritten production functions
      (`Available`, the full `ByBranch` map including its raw-path values, the full `Prunable` map,
      the dirty bool, the operation string). This is what proves the additive inventory rewrite
      changes no `tws status` semantics — the inventory's only production consumer (§9.1) — and that
      the wrapper adaptation changes no `tws status` or `tws doctor` dirty/active-op semantics,
      rather than merely changing
      none of them on the surfaces sampled;
    - **divergence set** — the shim-only malformed-porcelain fixtures: equality is **not** asserted;
      the test asserts instead that the legacy copy may still report `Available == true` with
      partial maps while the new inventory reports `Available == false`, `Err != nil`, and empty
      `Records`/`ByPath`/`ByBranch`/`Prunable`, and that `BuildAgentStatus` takes its
      already-defined unavailable-inventory branch with no new issue code, severity, message, or
      status value (§9.2, §11.3). A well-formed 64-hex `HEAD` is **not** in this set (§8.3.1).

**Process budget**

36. `TestStackStatus_ProcessBudget`: the argv-recording shim records every invocation's argv,
    working directory, and environment, in order, into a recorder that the test can reset. Classes
    are assigned by **provenance measured with control runs**, never by argv shape, because the
    shipped evaluator itself emits infrastructure-shaped argv (`rev-parse --git-common-dir` from
    `MainRepoRootIn`, and `rev-parse --abbrev-ref origin/HEAD` / `symbolic-ref --short HEAD` from
    `DefaultBranchIn`, §13). Shape-based classification would misattribute those to `I` and
    double-count them.

    1. **Fixture preconditions.** The fixture is prepared, and `ws`, `cfg`, `featurePath`, and
       `stack` are resolved, with the recorder **reset afterwards**, so no preparation process is
       counted. Every run below is read-only (§16), so repeating a run over the unchanged fixture
       records an identical argv sequence; the test asserts that stability by running the builder
       control twice and requiring equal recordings before using it as a control.
    2. **`A` is measured by a builder-level control run.** With the recorder reset, the test calls
       `FeatureStackEdges(ws, cfg, feature, featurePath, stack)` alone — the exact arguments the
       builder will pass, no CLI prefix, no status projection — and captures the recorded sequence
       as `A`. `A` is used as an **ordered sequence and as a multiset**, and by construction it
       already contains every infrastructure-shaped ancestry helper invocation.
    3. **The builder run must equal `A` plus exactly the status-added forms.** With the recorder
       reset again, the test calls `BuildStackStatus(ws, cfg, feature, featurePath, stack)` with
       those same arguments and takes the **multiset difference** of its recording minus `A`
       (removing one occurrence per element of `A`). The subtraction must succeed with no missing
       element, the surviving `A`-subsequence must appear in the builder recording in the same
       relative order, and the remainder must consist of **exactly** the four status-added forms of
       §16.1 and nothing else:
       - the `for-each-ref` inventory with the literal `%00` format string — exactly 1;
       - `worktree list --porcelain` — exactly 1;
       - `rev-list --left-right --count <40-hex>...<40-hex>` — exactly `C`;
       - `status --porcelain` **whose recorded environment contains `GIT_OPTIONAL_LOCKS=0`** —
         exactly `D`.

       The remainder therefore totals exactly `2 + C + D`, with `C` and `D` computed from the
       fixture, and the builder total is exactly `A + 2 + C + D` — the approved formula. **Any
       unclassified remainder fails the test**, which is what forbids a future per-row branch,
       ref-existence, or upstream process. A `status --porcelain` **without**
       `GIT_OPTIONAL_LOCKS=0` cannot be matched by the remainder rule and therefore fails the test,
       which also guards §11.3.
    4. **No hidden per-row probe.** For a 5-entry stack the remainder contains **zero** invocations
       naming a row's branch, a row's ref, an upstream ref, or a per-row worktree path, and the
       remainder count does not change when `E` grows while `C` and `D` are held fixed. A
       cross-repo-only stack and a repo-unavailable workspace each yield an **empty** remainder
       (builder total exactly `A`), and no invocation names the cross-repo fixture's path.
    5. **`I` is measured by a separate CLI-prefix control run, and only when the end-to-end total is
       asserted.** With the recorder reset, the test executes only the pre-builder prefix —
       `RequireWorkspace()` followed by the explicit `LoadConfig()` of §17.2 — and captures the
       recorded sequence as `I`. `I` must contain zero status-added forms, and for the ordinary
       fixture (a real Git repository at the cwd, so `MainRepoRoot()` succeeds and no inference
       candidate is probed) it is asserted to be exactly two `rev-parse --show-toplevel` and one
       `rev-parse --git-common-dir` (§13, §17.2); that count is pinned so a future extra
       config/workspace Git call cannot appear silently. The full `RunE` run under the same shim and
       fixture must then record exactly `I + A + 2 + C + D` invocations, and its multiset must equal
       `I` ⊎ `A` ⊎ the four status-added forms.

    Rules 1-4 pin the approved builder formula `A + 2 + C + D` and the no-hidden-per-row-probe
    guarantee on their own, and run in package `internal`
    (`TestStackStatus_ProcessBudget`, §17.7). Rule 5 is required only when the end-to-end CLI total
    `I + A + 2 + C + D` is asserted; because it executes `RunE`, its full-command half runs in
    package `cli` as `TestStackStatus_ProcessBudgetEndToEnd`, reusing the same shim, the same
    fixture, and the same `I`/`A` control procedure.

**Parent counts**

37. `TestStackStatus_ParentCounts`: a fixture with a fast-forwarded parent reports the exact
    ahead/behind pair, verified against an independent `git rev-list --left-right --count` run;
    both operands in the captured argv are 40-hex; a base-unset row, a cross-repo row, a
    missing-child row, and a missing-base row start **no** count process and report two nulls; a
    forced command failure reports two nulls, not zeros.

**Human output**

38. `TestStackStatus_HumanGrammar`: a golden snapshot over a fixture containing every ancestry state,
    every materialization state, every upstream state, an archived row, a cross-repo row, a
    decoupled `git:` name, and a duplicate branch asserts: header labels and `?` tokens, column
    header order, per-cell token grammar (§15.2) — including that ` (git: <branch>)` is the only
    space-carrying cell content and that a row with no branch-ref inventory record renders `?` and
    never `none` in `UPSTREAM` — `FLAGS` token order, detail-line order, the exact summary block,
    and the exact footer line as the final line.
39. `TestStackStatus_HumanSanitization`: an entry name containing a control character and a 300-rune
    base name render sanitized and truncated with `…` in human output, while `--json` carries the
    raw values; the encoded JSON bytes contain no raw control byte.
40. `TestStackStatus_UnevaluatedVocabulary`: a `Status == ""` row renders `unevaluated` in the
    `ANCESTRY` column via `ancestryDisplayStatus`, and no row renders a `base-record=` token for a
    null `base_record.state`.

**Gates**

41. Full gates pass from a clean tree:

    ```bash
    gofmt -l internal cmd     # must print nothing
    go test ./... -count=1
    go vet ./...
    golangci-lint run ./...
    make build
    git diff --check
    tpatch feature deps --validate-all
    ```

42. CLI smoke test against a real workspace:

    ```bash
    ./bin/tws stack status <feature>
    ./bin/tws stack status <feature> --json | jq -e '.schema_version == 1 and (.entries | type == "array") and (has("stack_state") | not)'
    ./bin/tws stack status <feature> --json | jq -e '[.entries[].upstream.local_only] | all'
    ./bin/tws stack <feature>            # legacy tree, unchanged
    ./bin/tws stack status <feature> --json > a.json && ./bin/tws stack status <feature> --json > b.json && cmp a.json b.json
    ```

43. Boundary greps (robust, not brittle) confirm the non-goals:

    ```bash
    ! grep -rnE '\b(fetch|ls-remote|update-ref|push)\b' internal/stack_status.go internal/cli/stack_status.go
    ! grep -rn 'omitempty' internal/stack_status.go
    ! grep -rn 'patch-id\|patch_id' internal/stack_status.go internal/cli/stack_status.go
    ! grep -n 'ancestryFullSHA' internal/agent_status.go   # the worktree parser is object-format neutral
    git diff --stat -- internal/stack_ancestry.go   # empty: no semantic change to the evaluator
    ```

    In `internal/stack_status.go`, `ancestryFullSHA` may appear **only** in the §12 parent-count
    preconditions; AC 23a's parser half asserts that neither inventory parser is passed it and that
    no `[0-9a-f]{40}` literal exists outside that one precondition (§8.3.1 rule 2).

## 21. Test matrix

| Dimension | Cases |
|---|---|
| Mode | external, checkout |
| cwd | repo root, nested repo dir, linked worktree, workspace root, feature dir |
| Repository | resolvable (workspace/worktree/inferred), unavailable, alternate present |
| Entry kind | active, archived, cross-repo, base-unset, decoupled `Branch`, duplicate `GitBranch()`, unsafe name |
| Ancestry | current, stale, divergent, missing (child), missing (base), cross-repo, unevaluated, unrelated histories |
| Materialization | present, detached, wrong-branch, locked, prunable-missing, missing, archived, unknown, stray directory |
| Upstream | none, equal, ahead, behind, diverged, gone, local-branch upstream, inventory invalid, inventory skipped |
| Object format | SHA-1 (40-hex) and SHA-256 (64-hex) inventory records — parser fixtures always, real `git init --object-format=sha256` where the CI Git supports it (AC 23a) |
| Probes | clean, dirty, rebase in progress, missing `.git`, unreadable `.git` file, malformed/empty `gitdir:` line, missing gitdir target, non-directory gitdir target, non-ENOENT stat error, command failure |
| Output | human, `--json`, empty stack, repeated run |
| Failure injection | short edge slice, malformed porcelain, malformed `for-each-ref`, failing `rev-list`, unreadable `stack.yaml`, invalid YAML |

Every cwd row must produce byte-identical output for the same fixture — the report is a projection of
workspace and repository state and contains no cwd-derived field.

## 22. Follow-ups (explicitly not this feature)

- **`tpatch-patch-identity-research`** — remains a deferred child. This feature neither defines nor
  assumes a patch-identity contract; it reports Git refs and commits only. tws must not reimplement
  tpatch reconciliation, patch theory, or logical change identity.
- **Fetch-aware freshness** — any `--fetch`, `origin/HEAD` freshness, or remote-comparison mode is a
  separate feature with its own network, timeout, and failure semantics.
- **Sync modes, rebase plan guard, safe reparent/restack** — the remaining P1 roadmap items; this
  feature only reports the facts they will act on.
- **Wrong-branch / attention vocabulary** — stack status reports facts, not issues. Any issue-code
  projection belongs to `tws status` or `tws doctor`.
- **Legacy `tws stack` writer migration** — moving `PrintTree` onto Cobra writers is deliberately
  out of scope so the legacy bytes stay pinned.
