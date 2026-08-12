# Analysis

## Summary

`stack-status` is a read-only projection and presentation feature, not a new ancestry
implementation. The shipped `StackEdge` evaluator remains authoritative for branch/base identity,
ref existence, heads, merge base, recorded-base state, ancestry status, reason, severity, guidance,
notes, and repository source (`internal/stack_ancestry.go:146-180,371-421,706-721,846-863`).
The new command adds only:

1. `tws stack status <feature> [--json]`, while preserving successful legacy
   `tws stack <feature>` output;
2. one full-ref branch inventory for upstream facts;
3. one additive worktree inventory for materialization and checked-out/detached facts;
4. nullable, read-only dirty and active-operation probes; and
5. parent ahead/behind counts from the already-resolved `StackEdge` heads.

There is no fetch, mutation, ancestry reclassification, patch identity, patch-id, successor
tracking, or child research work in this feature.

## Grounded current behavior

### Legacy `tws stack`

`stackCmd` is currently `cobra.ExactArgs(1)`, completes feature names, resolves with
`RequireFeaturePath`, loads `stack.yaml`, prints a cycle warning with bare `fmt.Printf`, and calls
`PrintTree` (`internal/cli/stack.go:10-39`). `PrintTree` also writes directly to process stdout
(`internal/stack.go:207-277`). Therefore:

- successful legacy output must remain byte-for-byte on `os.Stdout`;
- its tests must use the existing `captureStdout` helper
  (`internal/cli/space_guard_test.go:15-38`), not only `cmd.SetOut`;
- the new status command alone writes to `cmd.OutOrStdout()`;
- moving legacy output to Cobra writers is out of scope.

Adding a child command necessarily changes `tws stack --help` and usage shown for argument errors:
Cobra adds an `Available Commands` section and a command-form usage line. That help/usage drift is
accepted and must be snapshot-tested. It is not correct to claim the whole legacy help surface is
unchanged.

`TopoSort` seeds its queue from a map and `PrintTree` walks a map of bases
(`internal/stack.go:138-179,207-277`). Neither is a valid display-order source. The status report
uses **only `stack.yaml` slice order**, with no topological sort, root grouping, alphabetical
tie-break, or deduplication.

### Existing ancestry contract

`StackEdge` already distinguishes logical `Name` from `GitBranch`, fully qualifies the child as
`refs/heads/<git-branch>`, records whether that child probe ran, and caches ref resolutions
(`internal/stack_ancestry.go:146-180,237-277,371-421`). `FeatureStackEdges` performs the single
mode-aware repository resolution and returns unevaluated rows rather than false ancestry claims
when the repository is unavailable (`internal/stack_ancestry.go:769-863`).

The status builder calls `FeatureStackEdges` exactly once and keeps both returned values. It does
not call a second repository resolver and does not call a second per-entry ref-existence probe.

## Resolved command surface

### New child command

Use `tws stack status <feature> [--json]`.

The new child's `Args` is a custom validator wrapping the equivalent of `cobra.ExactArgs(1)`:

- with one argument, validation succeeds;
- with more than one, return the normal exact-arity error;
- with zero arguments, check feature names through the error-returning runtime listing path;
- if a feature literally named `status` exists, return a tailored error explaining both meanings:
  `tws stack status <feature>` requests the report, while `tws stack -- status` prints that
  feature's legacy tree;
- if no `status` feature exists, return the normal missing-argument error.

The hint belongs in the **new status subcommand's `Args`**, because Cobra selects that child before
the legacy parent `RunE`. The legacy `RunE` must not contain collision-specific behavior.

The escape hatch remains:

```text
tws stack -- status
```

`tws stack status status` is valid and reports stack status for the feature named `status`.

### Completion tradeoff

The new subcommand completes feature names and returns `ShellCompDirectiveNoFileComp`.
At the parent level Cobra already contributes the `status` subcommand. The parent
`ValidArgsFunction` therefore filters a feature named `status` to avoid a duplicate completion
candidate with two meanings.

That deduplication has an explicit discoverability cost: ordinary completion no longer suggests
the legacy `status` feature at the parent position. Help and documentation must advertise
`tws stack -- status`; users can still type it directly. The new child's completion does not filter
the name, so `tws stack status status` remains discoverable.

## Strict success and exit contract

`tws stack status` is strict about its requested stack:

- unknown/unsafe feature, unresolvable or invalid workspace, missing `stack.yaml`, unreadable YAML,
  invalid YAML, or structurally invalid stack is fatal;
- a fatal error emits **no human or JSON status document** on stdout;
- runtime failures set `SilenceUsage = true`; Cobra argument-validation errors retain normal usage;
- ancestry states such as `stale`, `divergent`, `missing`, `cross-repo-unsupported`, or
  `unevaluated`, dirty worktrees, and nullable failed supplemental probes are successful reports
  with exit 0.

There is no `stack_state` key in the successful schema. A successful report necessarily describes
one loaded, valid stack. The stable-key promise applies to successful reports only, not to an error
payload. Legacy `tws stack <feature>` keeps its current missing-stack error and cycle-warning
behavior.

## Authoritative data joins

### Stack rows and duplicate Git branches

There is exactly one output row for every `stack.Branches` element, in that slice's order.
Duplicate `GitBranch()` values are not collapsed: the rows keep distinct logical names and
materialization paths while sharing the same full-ref and upstream facts. A grouping map may be
used only as a lookup; it may never drive output order.

For each row, ancestry fields come from the same-index `StackEdge`. If the evaluator ever returns a
slice of the wrong length, use the existing `ancestryEdgesFor` totality rule rather than indexing a
zero value (`internal/checkout_health.go:604-617`).

### Ref existence

`ref_exists` is nullable:

- `StackEdge.RefProbed == true` -> pointer to `StackEdge.RefExists`;
- `StackEdge.RefProbed == false` -> `null`.

This copies the evaluator's evidence and prevents a second ref probe from disagreeing with the
ancestry classification. `checkout_health` already follows this rule
(`internal/checkout_health.go:627-629`).

A base-unset row is intentionally unprobed by the evaluator
(`internal/stack_ancestry.go:374-383`; test contract at
`internal/stack_ancestry_test.go:745-751`). It therefore has `ref_exists: null`, null local/parent
heads, and null parent counts. A matching batched ref-inventory record may still supply upstream
configuration, but it does not retroactively rewrite `StackEdge.RefProbed`, `RefExists`, heads, or
ancestry.

### One full-ref upstream inventory

Run exactly one command, without a shell:

```text
git -C <repo-dir> for-each-ref \
  --format=%(refname)%00%(objectname)%00%(upstream)%00%(upstream:track)%00%(upstream:trackshort) \
  refs/heads/
```

The five fields, in order, are:

1. full branch ref, for example `refs/heads/feature/api`;
2. branch object ID;
3. full configured upstream ref, or empty;
4. long tracking text, or empty;
5. short tracking marker, or empty.

Fields are separated by NUL bytes emitted by `%00`. Records are separated by the newline that
`for-each-ref` appends for each format expansion. Newline is a safe record boundary because Git
refnames cannot contain newline. There is no printable delimiter such as `|`, and neither
`%(refname:short)` nor `%(upstream:short)` is used.

Parsing is fail-closed for this inventory:

1. split records on newline and allow only one final empty record;
2. split each non-empty record on NUL and require exactly five fields;
3. require field 1 to start with `refs/heads/` and have a non-empty suffix;
4. require field 2 to be a non-empty hexadecimal object ID;
5. require a non-empty upstream to be a full `refs/...` name;
6. validate the tracking pair against only the documented forms: no-upstream, equal, ahead,
   behind, diverged, or gone;
7. reject duplicate full branch-ref keys.

Any malformed record invalidates the whole supplemental inventory. The report remains successful,
but every upstream field whose truth depended on that inventory is `null`; it never publishes a
partially parsed map.

The primary map key is the complete field-1 value. A row joins with `StackEdge.ChildRef`, also a
complete `refs/heads/...` name. Only **after** parsing and exact-key lookup may presentation strip
the known `refs/heads/` or `refs/remotes/` prefix. A same-named tag can never alter the key or win a
lookup.

Upstream mapping preserves these distinct states:

| Evidence | `configured` | `state` | counts |
| --- | --- | --- | --- |
| empty upstream, empty track, empty trackshort | `false` | `none` | null |
| upstream set, empty track, `=` | `true` | `equal` | ahead 0, behind 0 |
| `[ahead N]` / `>` | `true` | `ahead` | N / 0 |
| `[behind N]` / `<` | `true` | `behind` | 0 / N |
| `[ahead N, behind M]` / `<>` | `true` | `diverged` | N / M |
| `[gone]` | `true` | `gone` | null |
| inventory unavailable or invalid | `null` | null | null |

Thus equal, no-upstream, and `[gone]` are never conflated. Local-branch upstreams remain valid
because their full `refs/heads/...` upstream is preserved.

### Additive worktree inventory

`BuildWorktreeInventory` currently keeps only branch-to-path and branch-prunable maps and drops
branch-less porcelain blocks (`internal/agent_status.go:473-518`). Extend it additively:

- retain `Available`, `ByBranch`, and `Prunable` for existing callers;
- add an ordered record slice and a map keyed by canonical worktree path;
- retain every `git worktree list --porcelain` block, including main, linked, detached, locked,
  bare, and prunable blocks;
- retain full branch refs until parsing is complete.

Each retained record contains:

- canonical `Path`;
- nullable `Head`;
- nullable full `BranchRef`;
- nullable `Detached` (`true` for an explicit `detached` line, `false` for a valid branch line,
  `null` if neither was present);
- `Bare`;
- `Locked` plus nullable lock reason;
- `Prunable` plus nullable prunable reason.

Blank lines are record boundaries and EOF flushes the final record. A block without a worktree
path, a duplicate canonical path, a malformed full branch ref, or contradictory branch/detached
markers makes the inventory unavailable and records an error; existing callers continue to see
`Available == false`.

For an external row, canonicalize its expected
`<feature-path>/worktrees/<StackEntry.Name>` and look it up in the path map:

- matching non-prunable block -> `present`;
- matching prunable block -> `prunable-missing`;
- no matching block with archived entry -> `archived`;
- no matching block -> `missing`;
- unavailable inventory -> `unknown`.

This path join is authoritative even for detached worktrees and duplicate Git branches. It avoids
the current branch-key limitation and requires no separate branch probe. `checked_out_branch`
comes from the matched record's full branch ref, stripped only after validation; detached records
produce `checked_out_branch: null` and `detached: true`.

## Mode-specific workspace and entry truth

The report uses explicit nullable mode blocks:

```text
workspace.mode
workspace.stable_id
workspace.metadata_root
workspace.repository.dir
workspace.repository.source
workspace.repository.alternate
workspace.external
workspace.checkout
```

Exactly one of `workspace.external` and `workspace.checkout` is non-null.
`repository.dir/source/alternate` come from the single `StackRepoResolution`; `dir` and `alternate`
are nullable when unavailable. They are not independently rediscovered.

### External block

`workspace.external` contains the canonical feature worktrees root. It does **not** contain a
single workspace branch, detached, dirty, or active-operation claim because external mode can have
many linked worktrees.

For every external entry:

- `is_current_checkout` is always `null`;
- materialization path/branch/detached comes from the expected-path worktree inventory join;
- dirty and active-operation probes target that matched external worktree path, never
  `StackRepoResolution.RepoDir`;
- dirty and active operation remain `null` for missing, archived, prunable, cross-repo, unknown,
  or probe-failed rows.

This intentionally differs from any schema that tries to name one global current checkout in
external mode. The schema drift is explicit and tested rather than hidden behind shared structs.

### Checkout block

`workspace.checkout` contains:

- `path`: the one physical checkout;
- `branch: *string`: populated only when a full attached branch ref is known;
- `detached: *bool`: true, false, or null when the inventory cannot decide;
- `dirty: *bool`: dirty, clean, or null on probe failure;
- `active_git_op: *string`: `"none"` or a known operation name on successful inspection, null on
  inspection failure.

The checkout path is matched against the same canonical-path worktree inventory. No
`buildHeader` boolean is reused: current `buildHeader`, `healthCurrentBranch`, and `gitDirty`
collapse failures into attached/clean-looking zero values
(`internal/checkout_health.go:313-358`), which is unsuitable for this schema.

For checkout entries:

- materialization kind is `ref`;
- `ref_exists` and present/missing state derive only from `StackEdge.RefProbed/RefExists`;
- a base-unset unprobed row has materialization state `unknown`;
- `is_current_checkout` is a bool only when the checkout branch and attached state are known;
  detached or unreadable checkout state yields null for every row;
- `checked_out_branch`, dirty, and active operation are copied only to row(s) whose
  `git_branch` equals the known checkout branch; all other rows are null;
- duplicate rows sharing that Git branch remain separate and receive the same checkout/ref facts.

The current checkout branch is never inferred from a session record or from a ref existing. It is
reported only when Git's worktree inventory identifies the attached branch.

## Real tri-state, read-only probes

Introduce error-returning read-only helpers rather than duplicating or preserving `gitDirty`'s
error-swallowing contract:

```text
probeDirty(path) (bool, error)
probeActiveGitOp(path) (string, error)
```

`probeDirty` runs:

```text
GIT_OPTIONAL_LOCKS=0 git -C <path> status --porcelain
```

Success maps to true/false; any command or parse failure maps to JSON null at the caller.
`probeActiveGitOp` resolves a directory `.git` or linked-worktree `.git` file, returns a known
operation name, returns `"none"` only after every marker was successfully inspected, and returns an
error for unreadable/malformed gitdir data or any filesystem error other than not-exist on a
specific marker. The current `gitActiveOp` silently treats such errors as no operation
(`internal/checkout_health.go:360-390`); the new contract must not.

Existing `tws status`, `tws doctor`, and `tws list` behavior is preserved by adapting their call
sites explicitly. They must not accidentally turn a probe error into a new user-visible status as
part of this feature.

Read-only proof uses real repositories and snapshots every probed checkout/worktree before and
after:

- `HEAD`, loose refs, `packed-refs`, and `FETCH_HEAD`;
- the main and linked-worktree index files, including content, mode, size, and mtime;
- stack/workspace metadata;
- active-operation marker files.

Tests cover clean, dirty, detached, linked-worktree, and failing probes. The index snapshot is the
acceptance proof for `GIT_OPTIONAL_LOCKS=0`; merely asserting that the command succeeded is
insufficient.

## Successful JSON schema

The report owns `stackStatusSchema = 1`, independent of the agent-status schema. No field uses
`omitempty`; absent scalars are null and lists are `[]`. Zero-valued optional `StackEdge` strings
are represented as null without changing their classification.

Top-level shape:

```text
schema_version
workspace
feature
entries
summary
```

Workspace shape:

```text
mode
stable_id
metadata_root
repository { dir, source, alternate }
external | null
checkout | null
```

Entry shape:

```text
name
git_branch
archived
repo
base { name, kind, ref }
ref_exists
heads { local, local_short, parent, parent_short, merge_base, merge_base_short }
base_record { sha, commit, short, state }
ancestry { status, reason, severity, guidance, notes }
repo_source
parent_counts { ahead, behind }
materialization {
  kind, state, path, checked_out_branch, detached, dirty, active_git_op
}
is_current_checkout
upstream {
  configured, ref, display, state, ahead, behind, local_only
}
```

Rules:

- `ancestry.status` is null when `StackEdge.Status == ""`; human output renders that as
  `unevaluated` through the existing presentation vocabulary
  (`internal/stack_ancestry.go:539-545`);
- `ancestry.reason`, severity, guidance, notes, heads, base record, `repo_source`, and identity
  fields are direct `StackEdge` projections;
- `upstream.local_only` is always true in successful rows;
- `parent_counts` uses full peeled `StackEdge.LocalHead` and `ParentHead`, never ambiguous names;
- parent counts are null for base-unset, cross-repo, missing/unresolved heads, or command failure;
- unrelated histories still return the real two rev-list totals while merge base remains null;
- no generated timestamp is included, so unchanged repositories can produce byte-identical output.

The stable key set applies only after the strict builder succeeds. There is no partial report
schema and no JSON error document.

## Parent counts

For each eligible row, run:

```text
git -C <repo-dir> rev-list --left-right --count <local-head>...<parent-head>
```

Both operands are full peeled SHAs copied from `StackEdge`. The left count is child-ahead and the
right count is child-behind. Do not run the command unless both heads are non-empty. A non-zero
exit or malformed output yields two nulls, not zeroes. No count is attempted for a base-unset row.

## Process budget

Let:

- `A` be the actual number of Git processes used by the already-shipped
  `FeatureStackEdges` repository resolution and ancestry evaluator for this exact stack;
- `C` be the number of rows with both copied local and parent heads, `0 <= C <= E`;
- `D` be the number of dirty-probe process invocations, whether they succeed or fail: present
  external worktrees in external mode, or at most one physical checkout in checkout mode.

The shipped ancestry cost `A` is unchanged. It includes its own repository validation, cached
`rev-parse`/abbreviation work, merge-base checks, and optional identity-note probes
(`internal/stack_ancestry.go:210-289,371-421,425-535,706-721`). It must not be described as a new
status cost or approximated as a fixed multiplier without accounting for those caches.

Status adds, for a resolvable repository:

| Added probe | Processes |
| --- | ---: |
| full-ref `for-each-ref` inventory | 1 |
| `worktree list --porcelain` inventory | 1 |
| parent `rev-list --left-right --count` | `C` |
| dirty probes | `D` |
| active-operation inspection | 0 |
| branch/detached probes | 0 |

Therefore the command's total is:

```text
A + 2 + C + D
```

Supplemental Git probes are skipped when the resolved repository is unavailable, and cross-repo
rows add none. Tests should classify captured Git argv into shipped ancestry versus status-added
categories and assert both the added formula and the overall total. This prevents accidentally
adding a per-row branch, ref-existence, or upstream process.

## No-fetch and local-only contract

- No path runs `git fetch`, writes refs, changes indexes, or accepts a `--fetch` flag.
- Upstream counts describe the configured upstream ref as it exists locally.
- Human output includes a no-fetch/local-only footer.
- JSON carries `upstream.local_only: true`.
- Parent counts compare local peeled commits and are not remote freshness claims.
- A PATH shim rejects any fetch and any unexpected mutating Git verb.

The read-only command may still report `unevaluated` ancestry or null supplemental facts when local
evidence is unavailable; it must not fabricate clean, attached, no-operation, zero-count, or
no-upstream values.

## Human output

Default output is one row per stack entry in `stack.yaml` order. It shows logical name, decoupled
Git branch when different, ancestry state, copied short heads, parent counts, upstream state,
materialization, dirty/operation markers when known, checkout marker when known, archived, and
cross-repo markers.

Reason, guidance, and notes reuse the shipped wording and sanitization. A summary counts ancestry
states and nullable probe outcomes, followed by the explicit local-only/no-fetch footer. No map
iteration may influence row order.

## Compatibility and deliberate schema differences

- Successful `tws stack <feature>` output, exit code, cycle warning, and error strings remain
  unchanged.
- `tws stack --help` and usage blocks change because a child command now exists; snapshots pin the
  accepted change.
- A feature named `status` uses `tws stack -- status` for the legacy tree and receives a custom
  zero-argument hint from the child command.
- Parent completion deduplicates the `status` token at the documented cost described above.
- New status output is capturable through `cmd.OutOrStdout`; legacy output remains captured from
  `os.Stdout`.
- `tws status`, `tws doctor`, and `tws list` schemas and behavior do not change.
- Stack-status intentionally does not reuse `AgentStatusEntry`:
  external `is_current_checkout` is always null, ref existence is tied to `StackEdge.RefProbed`,
  materialization retains detached/active-operation truth, and the mode blocks are explicit.
- `StackEdge`, ancestry enums, sync code, and patch identity remain unchanged.

## Edge-case matrix

| Case | Required result |
| --- | --- |
| base unset | ancestry status null/reason `base-unset`; `ref_exists`, heads, and parent counts null |
| duplicate `GitBranch()` | separate rows in YAML order; shared full-ref/upstream facts; separate expected worktree-path facts |
| cross-repo entry | shipped cross-repo ancestry; ref/materialization probes null or cross-repo as appropriate; no supplemental Git process |
| archived ref present | ancestry/ref/upstream facts reported; absent external worktree state `archived`; dirty/op null |
| archived ref missing | shipped missing ancestry; nullable counts; no fabricated clean state |
| external attached worktree | checked-out branch and `detached: false` from path inventory; dirty/op probed at that worktree |
| external detached worktree | present, checked-out branch null, `detached: true`; dirty/op still independently nullable |
| external prunable path | `prunable-missing` from the matched porcelain block; dirty/op null |
| checkout attached | checkout branch known; current-row bools and current-row dirty/op copies are allowed |
| checkout detached | checkout detached true and branch null; every row's `is_current_checkout` is null |
| checkout inventory failure | checkout branch/detached null; no attached/clean claim |
| no upstream | configured false, state none, counts null |
| equal upstream | configured true, state equal, counts 0/0 |
| gone upstream | configured true, state gone, counts null |
| unrelated histories | copied divergent ancestry with null merge base; real parent rev-list totals |
| repository unavailable | shipped unevaluated rows; supplemental inventories/counts skipped; successful report if workspace/stack were valid |

## Affected implementation boundaries

Prospective implementation remains one logical feature:

- `internal/stack_status.go`: report types, strict builder, normalizer, formatter, full-ref
  inventory parser, upstream parser, parent counts, and error-returning read-only probes;
- `internal/cli/stack_status.go` and `internal/cli/stack.go`: child attachment, custom `Args`,
  completion behavior, JSON flag, and help text;
- `internal/agent_status.go`: additive complete worktree inventory and explicit adaptation to the
  new dirty-probe contract;
- `internal/checkout_health.go`: explicit adaptation to error-returning probes without changing
  existing output;
- focused CLI/unit/integration tests and directly related docs/skills in the later implementation
  phase.

`internal/stack_ancestry.go`, sync implementations, and patch-identity code should have no semantic
change.

## Test plan

Use real temporary repositories, local bare remotes, and real linked worktrees.

### Command and legacy behavior

- capture successful legacy tree and cycle warning from process stdout before/after;
- `tws stack status <feature>` and `--json`;
- zero-arg child with and without a feature named `status`;
- `tws stack -- status`;
- `tws stack status status`;
- parent completion has one `status` candidate;
- child completion includes all feature names and disables file completion;
- help and argument-usage snapshots capture the accepted legacy help drift.

### Strict errors and schema

- unknown feature, unsafe/space-colliding feature, missing/unreadable/invalid stack, invalid
  workspace mode, and structurally invalid stack return non-zero with empty stdout;
- successful reports have no `stack_state`, no omitted keys, null absent scalars, and non-null
  lists;
- stale/divergent/missing/unevaluated/dirty rows still exit 0;
- two unchanged runs are byte-identical.

### Ref inventory

- branch/tag same-name collision proves the map key is the full branch ref;
- raw parser fixtures contain NUL fields, no printable delimiter, and newline records;
- malformed field count, ref prefix, object ID, upstream, tracking pair, and duplicate key invalidate
  the whole inventory;
- no-upstream, equal, ahead, behind, diverged, gone, and local-branch upstream states;
- duplicate GitBranch rows share the same ref facts without row collapse;
- `ref_exists` exactly follows `RefProbed/RefExists`, including base-unset null.

### Worktrees and mode truth

- complete inventory retains main, linked, detached, locked, bare, and prunable blocks by canonical
  path while preserving existing maps;
- external expected-path lookup covers attached, detached, wrong branch, missing, archived,
  prunable, and duplicate-ref rows without a branch probe;
- external dirty/op probes run with `-C` at each matched worktree path;
- external `is_current_checkout` is always null;
- checkout branch is populated only for a known attached record;
- detached/unreadable checkout produces null row-current flags;
- duplicate current GitBranch rows remain separate and share current checkout facts.

### Read-only and process budget

- before/after snapshots prove no ref, `FETCH_HEAD`, index, marker, or metadata mutation;
- index content and mtime remain unchanged under `GIT_OPTIONAL_LOCKS=0`;
- dirty and active-op failures produce null, not false/none;
- PATH shim forbids fetch/mutating verbs;
- captured argv proves total `A + 2 + C + D`, with ancestry and status-added calls reported
  separately and no hidden per-row ref/branch/upstream calls.

### Regression

- existing `tws status`, checkout health/doctor, external doctor, and list output remain unchanged
  after helper adaptation;
- ancestry blocks compare field-for-field with the `StackEdge` returned for the same row.

## Dependencies and deferred work

The implementation consumes the shipped `stack-ancestry-doctor` and
`agent-work-status-dashboard` foundations. The direct dependency on the latter should be hard
because the complete worktree inventory and dirty-probe contract are shared. Cobra/completion
foundations remain relevant to the command surface. Dependency metadata changes belong to the
normal later tpatch phase; this analysis revision performs none.

`tpatch-patch-identity-research` remains a deferred child. This feature neither defines nor assumes
a patch-identity contract. The roadmap boundary remains intact: tws reports Git refs and commits
but does not reimplement tpatch reconciliation, patch theory, or logical change identity.

## Blocking-review mapping

| Blocking finding | Resolution in this analysis |
| --- | --- |
| full ref keys; no short-name collision | Exact full `%(refname)` key, exact `StackEdge.ChildRef` join, prefix stripping only after parse/lookup |
| safe single ref inventory | Five exact `%00` fields, newline records, fail-closed validation, no printable delimiter |
| feature named `status` | Custom child `Args` hint on zero args, preserved `tws stack -- status`, explicit completion-dedup tradeoff |
| `stack_state`/exit contradiction | Strict missing/invalid-stack failure with empty stdout; no `stack_state` in successful schema |
| nullable workspace truth | Explicit external/checkout blocks and nullable branch/detached/dirty/active-op evidence |
| complete materialization and budget | Additive path-keyed porcelain records including detached/prunable; no branch probe; total `A + 2 + C + D` |
| base-unset and ref reuse | Base-unset counts/ref existence null; `ref_exists` copied only from `RefProbed/RefExists` |
| duplicate Git branches and ordering | Separate YAML-order rows sharing ref facts; no sorting, topo ordering, or deduplication |
| upstream distinctions | Separate no-upstream, equal, ahead/behind/diverged, gone, and unknown mappings |
| legacy UX and capture | Accepted help/usage drift; successful legacy bytes remain on `os.Stdout`; new output uses Cobra writer |
| real read-only tri-state probes | Error-returning helpers, `GIT_OPTIONAL_LOCKS=0`, worktree-targeted probes, and index snapshot proof |
| no-fetch/no identity/deferred child | Explicit local-only contract, unchanged ancestry semantics, no patch identity, child research deferred |

## Viability

The feature is viable with bounded additive work: one ref inventory, one complete worktree
inventory, one parent-count process per eligible row, and nullable dirty probes at actual
materialized paths. Its correctness depends on preserving three boundaries: `StackEdge` owns
ancestry and ref evidence, porcelain path records own worktree identity, and failed supplemental
probes produce null rather than invented state.
