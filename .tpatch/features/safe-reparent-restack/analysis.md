# Analysis

## Summary

`safe-reparent-restack` adds an explicit stack-topology mutation that tws does
not currently expose: change one entry's configured parent and replay that
entry plus its affected descendants onto the new lineage without replaying the
old parent commits.

The feature is compatible with the current architecture, but it is not a thin
wrapper around `tws sync`. It changes both Git refs and `stack.yaml`, crosses
multiple worktrees or checkout branches, requires a new recovery transaction,
and must preserve the rebase-plan guard's plan/approval guarantees.

The central Git result is verified once a safe cutoff has been resolved:

```text
reparent(target) =
    git rebase --onto <pinned new destination SHA> <resolved old-parent cutoff>
```

For the required customer topology:

```text
master = A - B' - D
pr1    = A - B
pr2    = A - B - C
```

using cutoff `B` replays only `C` onto `D`. Using `merge-base` or a plain
`git rebase master pr2` attempts to replay both `B` and `C` and conflicts when
`B'` differs materially from `B`.

Three safety facts dominate the design:

1. New entries have no `LastBaseSHA`, so v1 must define whether a cutoff can be
   derived from the old parent's current tip, supplied explicitly, or must
   refuse.
2. Git accepts a resolvable cutoff that is not an ancestor of the branch and
   can silently replay the wrong commit set with exit status 0.
3. `git update-ref --stdin` is all-or-nothing against stale expected values,
   but the files ref backend is not crash-atomic across multiple refs; Git
   refs, worktree/index state, and `stack.yaml` cannot participate in one
   atomic operation.

The define phase must therefore decide what "atomic" means, which execution
strategy is used, how worktrees are resynchronized without destructive
shortcuts, and how a durable reparent transaction resumes or aborts across the
unavoidable refs-versus-metadata window.

No production code is changed by this analysis.

---

## 0. Evidence and terminology

### 0.1 Evidence classes

- **Current behavior**: read directly from the shipped tws code at commit
  `b2517048dce69817e7d50ec3605900eeb7c05122`.
- **Verified Git behavior**: reproduced with real temporary repositories,
  linked worktrees, bare remotes, normal and reftable ref backends, and Git
  2.55.0.
- **Recommendation**: an implementation option inferred from the evidence;
  it is not normative until `spec.md`.

### 0.2 Binding vocabulary for later phases

| Term | Meaning |
| --- | --- |
| parent | The logical `stack.yaml` edge stored in `StackEntry.Base` |
| base ref | The Git ref to which that logical parent resolves in one repository |
| destination | The pinned commit placed after `--onto` |
| cutoff | The exclusive lower replay boundary; normally recorded `LastBaseSHA`, or another explicitly resolved old-parent boundary chosen by the final contract |
| replay set | Commits in `cutoff..<git-branch>` that Git will reapply |
| reparent | Change the target entry's configured parent and replay the target |
| restack | Replay descendants so the affected closure is contiguous again |
| affected closure | The target plus transitive descendants selected for replay |
| pre-image | Snapshot of every affected branch tip and stack metadata before mutation |
| collateral ref | A ref outside the affected closure that broad Git behavior could move |
| plan | A document-producing route that moves no branch, rewrites no worktree, writes no tws runtime state, creates no pin ref or scratch worktree, and may fetch exactly where the described run fetches |
| apply | The executing transaction after admission |

Avoid using `merge-base` as a synonym for cutoff. It is a different Git fact
and is wrong in the motivating squash/amend case.

---

## 1. Current product behavior and feature gap

### 1.1 There is no reparent command

The CLI has no production `reparent` symbol and no `--onto` user flag.
`internal/cli/root.go` registers the current top-level commands. Stack topology
operations currently live under:

- `tws stack <feature>` for the dependency tree;
- `tws stack status <feature>` for read-only topology/ancestry facts;
- `tws sync <feature> --from <entry>` for replaying an unchanged descendant
  closure.

`tws new <feature> <existing-branch> --base <parent>` can register an existing
branch, but it adds a node and does not rewrite history. Arbitrary branch
import remains separate from changing an existing node's edge.

### 1.2 Current stack model

`internal/stack.go` defines:

```go
type StackEntry struct {
    Name        string
    Branch      string
    Archived    bool
    Base        string
    Repo        string
    LastBaseSHA string
}
```

Important current assumptions:

- `Name` is tws identity.
- `GitBranch()` is Git identity.
- `Base` is either another entry's logical name or a literal ref.
- `LastBaseSHA` is the recorded tip of the configured base used as the next
  amend-aware cutoff.
- `tws new` does not initialize `LastBaseSHA`; absent is the default for a
  newly registered entry.
- There is no field recording which historical base `LastBaseSHA` belonged
  to.
- `SaveStack` is a direct `os.WriteFile`, unlike the repository's
  temp+fsync+rename state writers.

Changing `Base` while preserving the old `LastBaseSHA` is necessary during a
reparent computation, but it temporarily violates the normal interpretation
that `LastBaseSHA` describes the current configured base. That old/new
distinction must live in the reparent transaction, plan, or an explicit model
extension; it cannot be inferred later.

### 1.3 Current graph invariants

| Invariant | Current enforcement |
| --- | --- |
| Acyclic logical graph | `TopoSort` rejects a cycle |
| Unique logical name | Not explicitly checked; duplicate names collapse in a map and can be reported as a cycle |
| Unique Git branch | Refused by new-mode selection, not by every legacy path |
| Literal/out-of-stack base | Allowed; it does not add an in-stack graph edge |
| Descendant closure | `Descendants` follows logical `Base` names but does not check repository identity |
| Checkout single-repo | New-mode selection refuses entries with `Repo` |

Reparent needs explicit duplicate-name, duplicate-Git-branch, cycle, and
repo-aware closure validation before any Git child process.

### 1.4 There are four base-resolution paths

External sync resolves an in-stack same-repository parent to
`parent.GitBranch()` and may rewrite a default-branch literal to
`origin/<default>`. Checkout resolves an in-stack parent to
`parent.GitBranch()` without consulting `Repo` and otherwise keeps the literal
base.

The shared plan builder uses a third resolver, `ResolveSyncBase`, for both
modes. It mirrors the external same-repository check and
`origin/<default>` rewrite rather than checkout's executor. In checkout mode
the plan can therefore publish an `origin/main` destination while
`buildCheckoutPlanFrom` executes against local `main`; the fallback used to
derive the default can also depend on the current HEAD when no remote default
exists.

The ancestry/status resolver is a fourth path: `stackBaseRef` uses
`refs/heads/<parent.GitBranch()>` for an in-stack parent and the literal token
otherwise. It feeds doctor, list, stack status, and the ancestry facts consumed
by the plan. Existing identity notes describe some mode differences, but one
checkout note still reflects the pre-D1 rule that checkout uses a literal
logical parent name.

The shipped plan, status projection, external executor, and checkout executor
therefore do not yet consume one canonical base result. A
reparent destination must publish:

- configured token;
- configured kind (`stack-entry` or `literal-ref`);
- resolved Git ref;
- pinned destination SHA;
- repository context.

The plan, executor, postcondition, and status projection must consume or
explicitly relate the same resolved value. The define phase must decide whether
this feature also repairs the stale checkout identity note and shared
resolution divergence.

---

## 2. Verified Git semantics

### 2.1 Customer topology

The real Git fixture was:

```text
* D          master
* B'         reviewed/squashed parent result
| * E        pr3
| * C        pr2
| * B        pr1
|/
* A
```

Observed behavior:

| Command/boundary | Replay set | Result |
| --- | --- | --- |
| `git rebase --onto D B pr2` | C | Success; pr2 becomes D+C' |
| `git rebase D pr2` | B,C | Add/add conflict against B' |
| `git rebase --onto D $(git merge-base D pr2) pr2` | B,C | Same conflict |

The resulting target must retain the reviewed B' content from master and add
only C. The old parent `pr1` remains untouched. This fixture requires a real
`pr1` stack entry, `pr2.Base = "pr1"`, and either a recorded cutoff B or a
defined never-synced cutoff rule; the existing rebase-plan integration
fixtures contain the Git shape but currently model only a single entry already
based on master/main.

### 2.2 Target cutoff source is an open product decision

`LastBaseSHA` has three shipped states: present/resolvable,
present/unresolvable, and absent. The absent state is normal immediately after
`tws new`.

Evidence-backed v1 options:

| Option | Behavior | Risk |
| --- | --- | --- |
| Recorded-only | Require `LastBaseSHA`; otherwise refuse and direct the operator to establish a baseline | Safest, but blocks never-synced stacks and much of the motivating workflow |
| Old-parent fallback | If `LastBaseSHA` is absent, resolve the old configured parent's current tip and use it only when it is an ancestor of the target | Works for ordinary never-synced `pr1=B`, `pr2=B+C`; cannot recover a deleted or rewritten old-parent ref |
| Operator-authored cutoff | Add an explicit cutoff flag, resolve and bind it in the plan/fingerprint, and require ancestry | Most flexible; a wrong but ancestral cutoff can still intentionally broaden replay |
| Ordered combination | Recorded cutoff, else safe old-parent tip, else explicit operator cutoff/refusal | Best reach, largest contract |

The plan must publish cutoff state and provenance, reusing the existing
`absent|present|unresolvable` vocabulary and distinguishing at least
`recorded-by-sync`, `old-parent-tip`, and `operator-supplied`. It must never
silently fall back to `merge-base`.

### 2.3 Cutoff preflight is mandatory

Git returns 128 when cutoff, destination, or branch cannot be resolved.
However:

```text
git rebase --onto <dest> <resolvable-cutoff-not-ancestor-of-branch> <branch>
```

returned 0 and silently replayed a broader set.

Every affected row therefore needs, before mutation:

```text
git rev-parse --verify --quiet --end-of-options <cutoff>^{commit}
git merge-base --is-ancestor <cutoff> <branch>
git rev-parse --verify --quiet --end-of-options <destination>^{commit}
```

A non-ancestor cutoff is a hard refusal, not an indeterminate warning.

An operator-supplied ref needs the same unambiguous ref-resolution rules as the
destination. A recorded cutoff is already a full SHA.

### 2.4 Descendants require a complete pre-image snapshot

For target T and descendant D:

```text
T: git rebase --onto <new-destination> <T.old-cutoff>
D: git rebase --onto <T.new-tip>       <D.recorded-cutoff>
```

Each row uses its own pre-image-snapshotted `LastBaseSHA`. T's old tip is only
a possible fallback when D has no recorded cutoff and the old tip is verified
as an ancestor of D. A stale descendant commonly has a recorded cutoff older
than T's current pre-image tip; requiring the current tip to be an ancestor
would falsely refuse normal pending work.

Using T's live post-replay ref as D's cutoff duplicates old parent commits.
The operation must snapshot all affected tips and per-row cutoffs before the
first mutation.

Recommended post-success metadata:

| Entry | Base after | LastBaseSHA after |
| --- | --- | --- |
| Target | New configured parent token | Pinned new destination SHA |
| Descendant | Unchanged | Post-replay tip of its parent |
| Sibling/outside closure | Unchanged | Unchanged |

After success, `tws stack status` should report every affected edge as current.

### 2.5 Destination cases

Verified or graph-derived cases:

| Destination | Git result | Analysis disposition |
| --- | --- | --- |
| Different in-stack parent | Replay succeeds | Allow if graph and repo checks pass |
| Literal local branch | Replay succeeds | Allow |
| Tag | Replay succeeds | Allow after pinning commit SHA |
| Raw commit SHA | Replay succeeds | Allow |
| Remote-tracking ref | Replay succeeds; upstream tracking is unchanged | Allow with fetch/freshness disclosure |
| Ancestor of current parent | Old parent content can leave the subtree | Allow only with full descendant closure and explicit warning |
| Descendant of target | Git can invert the stack | Refuse as cycle |
| Target itself | Git behavior is nonsensical/collapsing | Refuse |
| Same current parent, current edge | No Git/ref/reflog change | Open: no-op success vs refusal |
| Same current parent, stale edge | Ordinary restack | Open: redirect to or perform `sync --from` |

Logical cycle detection must use the stack graph. Git ancestry alone cannot see
archived or metadata-only cycles.

Short ref names can be ambiguous. `git rev-parse --verify --quiet
<token>^{commit}` suppresses ambiguity warnings and may choose a tag over a
branch. Destination resolution must enumerate exact matching heads, tags, and
remote-tracking refs (or require a full ref) and refuse when more than one
candidate matches. `--onto-kind entry|ref` resolves only the logical-entry
versus Git-ref ambiguity; the ref form still needs namespace-safe resolution.

### 2.6 `--update-refs` is unsafe for this operation

`git rebase --update-refs` moved non-stack refs whose tips were inside the
replayed range. It also silently skipped branches held by linked worktrees.

For external mode this is especially poor:

- materialized stack branches are held by their worktrees and can be skipped;
- unrelated free refs can still move.

The reparent executor should name every branch it intends to move and disable
implicit `rebase.updateRefs`.

### 2.7 User rebase config can reintroduce hidden behavior

Verified:

- `rebase.updateRefs=true` re-enables collateral ref movement even when argv
  omits `--update-refs`;
- `rebase.autoStash=true` can mutate a dirty worktree instead of preserving a
  dirty-tree refusal;
- explicit `--update-refs` forces the merge backend;
- `rebase.forkPoint=true` is harmless with a SHA cutoff, but the operation
  should still pin its boundary and not rely on config.

The define phase must choose an argv/config-hermetic policy, likely explicit
`--no-update-refs`, `--no-autostash`, and `--no-fork-point` or equivalent
`-c` overrides.

### 2.8 Worktree and unmaterialized-branch behavior

Verified:

- rebase state is per worktree Git directory;
- explicit branch-operand rebase refuses when the branch is held elsewhere;
- explicit branch-operand rebase from a free worktree can leave that
  worktree's HEAD on the target branch;
- `git update-ref` and `git replay` bypass checked-out-branch protection and
  can leave index/worktree state stale;
- one in-place rebase moves its branch ref only at finish, while conflicts
  leave a detached in-progress state and the branch ref unchanged.

The tool must either use each branch's own clean worktree or explicitly detach
and later restore/resynchronize holders. It must not use `git reset --hard` as
an undeclared shortcut.

The shipped external pass-2 path is not safe to reuse. It executes
`git rebase <base> <branch>` without the recorded cutoff, can replay the old
parent commits, refuses when the branch is held elsewhere, and can leave the
invoking worktree's HEAD on the explicit branch. V1 must either:

- refuse archived/unmaterialized affected entries; or
- compute them in an isolated scratch worktree/temp ref with explicit
  `--onto <destination> <cutoff>` semantics.

---

## 3. What "atomic" can and cannot mean

### 3.1 Verified compare-and-swap primitive

This is a genuine all-or-nothing compare-and-swap against stale expected ref
values:

```text
git update-ref -m "<reason>" --stdin
start
update refs/heads/T  <new-T>  <old-T>
update refs/heads/D1 <new-D1> <old-D1>
prepare
commit
```

A race on one expected old SHA aborts every ref update during prepare. This was
verified with both files and reftable ref backends.

That result does not prove crash atomicity on the files backend. The files
backend commits multiple lockfiles one by one; a process or machine crash
during commit can expose a subset of moved refs. Reftable is designed to
provide atomic multi-ref storage. Recovery must inspect every affected ref and
classify it as pre-image, planned tip, or foreign value rather than assume the
commit was all-or-nothing.

### 3.2 Inherently separate effects

No one transaction includes all of:

- creation of new Git objects;
- all branch ref updates;
- linked-worktree/index updates;
- checkout restoration;
- `stack.yaml`.

The strongest implementable guarantee is:

1. compute new commits without moving public refs;
2. pin both pre-images and planned new tips under tool-owned refs;
3. commit affected refs in one compare-and-swap transaction;
4. atomically write stack metadata from a durable transaction record;
5. restore/resynchronize worktrees;
6. resume forward after crashes, or CAS-rollback from pinned pre-images when
   still safe.

Calling the entire operation indivisible would be false. The spec must define
one CAS ref-commit point, its backend-specific crash guarantees, and durable
recovery across partial ref, metadata, and worktree tails.

### 3.3 Pre-image and planned-tip pins

Persisting bare SHAs is insufficient because aggressive GC can remove
unreachable commits. Tool-owned refs should protect both sides:

```text
refs/tws/reparent/<run-id>/old/<entry>
refs/tws/reparent/<run-id>/new/<entry>
```

Old pins were verified to preserve pre-images across reflog expiry and
`git gc --prune=now`. Planned commits produced by `git replay
--ref-action=print` were separately verified to disappear under aggressive GC
before the CAS when no ref protected them. New-tip pins must therefore be
created after computation and before public-ref commit. All pins remain until
the operation is irreversibly complete or explicitly abandoned.

`StackEntry.Name` can contain `/` and other path-sensitive structure. Pin ref
components must use a collision-resistant encoded/hashed entry identity, not
the raw logical name, or names such as `a` and `a/b` can collide as ref
directory/file paths.

### 3.4 Reference transaction hooks

A repository `reference-transaction` hook can veto the prepared transaction
and leave refs untouched. This is a distinct ordinary failure/refusal, not a
crash. The implementation must not disable or silently bypass it without a
separate security/compatibility decision. A crash during files-backend commit
is different and requires per-ref reconciliation.

---

## 4. Execution strategy options

### 4.1 Sequential in-place rebases

For each closure row:

```text
git -C <entry-worktree> rebase \
  --no-fork-point --no-update-refs --no-autostash \
  --onto <pinned-destination> <recorded-cutoff>
```

Pros:

- closest to shipped sync behavior;
- full conflict UI and native `git rebase --continue`;
- supports merges according to chosen backend;
- least new Git-version dependency.

Cons:

- one branch ref write per row;
- partial progress is externally observable;
- checkout mode repeatedly changes the user's checkout;
- rollback requires pins and compensating ref updates.

### 4.2 Detached scratch-worktree replay

Compute each row in a detached scratch worktree or tool-owned temporary ref,
chain the generated tips, then commit public refs in one CAS transaction.

Pros:

- no public ref moves during computation;
- full rebase diagnostics and conflict state;
- works on older Git than `git replay`;
- one atomic public-ref commit.

Cons:

- extra checkout/storage cost;
- conflict resolution happens in a tool scratch worktree;
- scratch worktree is new crash-visible state;
- affected checked-out worktrees must be detached/restored or otherwise
  resynchronized safely.

### 4.3 `git replay --ref-action=print`

Verified:

- with the explicit `--ref-action=print` flag, produces no
  ref/worktree/index mutation;
- emits native `update <ref> <new> <old>` CAS lines;
- produced the same new SHAs as sequential rebase in tested non-merge cases;
- works in a bare repository;
- explicit tip ranges touch only named tips.

Limitations:

- experimental and version-sensitive;
- recent Git versions default to updating refs when `--ref-action` is omitted;
  invoking `git replay` without the explicit print action is forbidden;
- older Git versions can have print-like default behavior without supporting
  the flag, so a version threshold alone is not a safe capability test;
- merge commits are unsupported;
- conflict exits 1 with no useful stdout/stderr and no resumable state;
- `--contained` reintroduces broad collateral and must never be used;
- applying its ref updates bypasses worktree checkout protection.

It is a valuable capability-gated planner/fast path, not a sufficient sole
executor.

The capability gate must probe for the `--ref-action=print` option itself and
the executor must always pass it. Unsupported or uncertain capability falls
back to another strategy; it never invokes bare `git replay`.

### 4.4 Composite option

An evidence-backed composite is:

1. use existing rebase-plan probes for user-facing candidate and hazard
   analysis;
2. when option support is proven, use `git replay --ref-action=print` with explicit tips to
   compute exact new refs for a conflict-free, no-merge operation;
3. pin every computed new tip before maintenance can prune it;
4. otherwise compute through detached scratch-worktree rebases;
5. commit public refs through one CAS transaction;
6. atomically write `stack.yaml`;
7. restore/resynchronize holders.

Every computation and pin above belongs to guarded execution, never to
`--plan`. A plan may perform its declared fetch but creates no scratch
worktree, state file, old/new pin ref, or Git object whose persistence it
depends on.

The define phase must decide whether v1 accepts this complexity or ships a
sequential, compensatable transaction first.

---

## 5. Current tws machinery

### 5.1 Reusable

The feature can reuse:

- `RebasePlan` row vocabulary for base, destination, cutoff, replay,
  determinacy, holders, collateral, push, and restore;
- plan renderers and JSON conventions;
- candidate and patch-equivalence probes;
- context identities and holder/ref inventories;
- capability gates;
- `EvaluatePlanGuard`, replay limits, approval, and JIT revalidation;
- `StackEdge` ancestry facts;
- `ResolveSyncSelectionFromOrder`;
- external run guard and checkout lock concepts;
- the temp+fsync+rename implementation behind checkout's unexported atomic
  state writer (reuse requires moving/exporting it or adding an internal
  owner);
- checkout `CheckoutPlanEntry.PreSHA`/`PostSHA` snapshots, although current
  abort does not consume them for rollback;
- real-Git integration fixtures, including the existing A-B'-D customer
  topology in both modes.

### 5.2 Assumptions that must be generalized

Current planner/executor code assumes:

- configured base and replay destination are the same logical base;
- `LastBaseSHA != current base SHA` means the same parent moved;
- checkout plan entries have no old/new parent distinction;
- external and checkout metadata writers update only `LastBaseSHA`;
- completion checks validate the current configured `Base`;
- descendant walking uses a fixed graph;
- sync state carries no pending metadata edit.

Reparent needs explicit old parent, new configured token, resolved destination,
old cutoff, affected closure, metadata delta, and transaction stage.

### 5.3 Plan document choices

| Option | Benefit | Cost |
| --- | --- | --- |
| Extend the 25-key `RebasePlan` v1 document | One schema | Violates the exact frozen key set |
| Bump `RebasePlan` to v2 for both sync and reparent | One future schema | Breaks existing sync consumers for an unrelated command |
| Add sibling `ReparentPlan` v1 reusing existing row types | Leaves sync schema and tokens untouched | Requires a second top-level document/renderer |

The sibling document is the lowest compatibility risk. It can reuse row types
without modifying the frozen 25-key sync document.

Whatever document shape is chosen, automation needs the same explicit
admission facts it uses for sync:

- `runnable`;
- guard limits/evaluation and `would_refuse`;
- controlled-path execution blockers;
- primary refusal kind/detail;
- one fingerprint usable only for the exact reparent operation.

A produced plan exits 0 even when it reports a refusal. Exit status is not an
admission predicate.

### 5.4 Fingerprint choices

A reparent token must bind:

- operation/scope;
- workspace and feature identity;
- target logical and Git branch;
- old configured parent and cutoff;
- new configured token/kind/ref/SHA;
- ordered descendant closure;
- every row's pre-image and replay facts;
- metadata delta;
- fetch policy and guard limits.

Using a separate reparent fingerprint domain/prefix would preserve all existing
sync approval tokens. Reusing and bumping the sync tuple would invalidate them.

### 5.5 Refusal domain

The sync `RefusalKind` domain is closed, ranked, and count-asserted. Reparent
introduces operation-specific failures such as:

- cycle/self/descendant destination;
- cutoff absent under a policy that requires an authored boundary;
- cutoff not ancestor;
- ambiguous destination;
- cross-repo closure;
- duplicate branch identity;
- unsafe holder/resync;
- transaction CAS mismatch;
- partial ref commit or foreign post-transaction ref;
- metadata pending after refs committed.

A separate closed `ReparentRefusalKind` can avoid perturbing sync refusal
ranking while still reusing guard limit/approval failures.

---

## 6. Recovery state and crash windows

### 6.1 Existing sync state is insufficient

External sync state v2/v3 and checkout transaction v2/v3 do not carry:

- old/new parent tokens;
- target destination kind/SHA;
- an external whole-closure pre-image (checkout rows already persist
  `PreSHA`/`PostSHA`, but current rollback does not consume them);
- pin refs;
- planned new tips;
- metadata delta;
- ref-transaction stage;
- worktree detachment/restoration state.

Adding ignored `omitempty` fields without a version bump is unsafe: an older
binary could resume the run as an ordinary sync and silently discard the
reparent intent.

### 6.2 State options

| Option | Consequence |
| --- | --- |
| Extend sync/checkout state and bump to v4 | Older binaries fail closed; current sync routing/classification becomes more complex |
| Separate versioned reparent state artifact | Isolates semantics and downgrade behavior; needs mutual exclusion with sync |

A separate artifact is the cleaner boundary if it shares the existing
mode-appropriate mutation lock so sync and reparent cannot mutate the same
feature concurrently.

The current locks are mode-specific:

| Mode | Existing sync exclusion |
| --- | --- |
| External | `<feature>/.sync-run.lock` |
| Checkout | `<metadata>/state/<feature>-checkout-sync.lock` |

A reparent design must either reuse each existing lock or define one common
operation lock checked by both sync and reparent. Reusing only
`.sync-run.lock` does not exclude checkout sync.

### 6.3 Minimum transaction record

The record needs:

```text
schema_version
run_id
workspace mode
strategy
feature and target
old/new parent identity
pinned destination
ordered closure
per-row:
  logical name
  Git branch
  repository context
  pre-image SHA
  cutoff SHA
  planned new SHA
  old/new base
  old/new last_base_sha
  stage
pre-image pin refs
planned_tip_pin_refs
guard fingerprint and effective limits
checkout/worktree restoration plan
metadata pre-image and expected post-image hash
```

### 6.4 Crash windows

| Window | Resume | Abort |
| --- | --- | --- |
| State and pins written; no public ref moved | Revalidate/recompute | Remove state and pins |
| Scratch replay in progress | Resume or restart scratch computation | Remove scratch and pins |
| CAS prepare failed | Re-plan; public refs unchanged | Remove state and pins |
| Files-backend ref commit crashed partway | Classify each ref as pre-image, planned tip, or foreign; complete or restore by CAS | Restore only rows still equal to a planned tip; refuse on foreign values |
| Ref transaction committed; metadata not written | Write expected metadata forward | CAS rollback from pins if no affected ref advanced |
| Metadata written; holders not restored | Restore/resynchronize holders | Usually resume forward; metadata and refs are already consistent |
| Sequential fallback mid-rebase | Native rebase continue, then closure | Rebase abort, then CAS restore completed prior rows |
| Operator commits after ref transaction | Resume may continue if compatible | Abort must refuse rather than erase new work |

Pins remain until refs, metadata, and holder restoration are complete.

### 6.5 Read-only observability during recovery

The current ancestry surfaces know nothing about a reparent transaction.
During the refs-versus-metadata windows they can classify an edge as stale or
rewritten and print a manual `git rebase --onto` or `tws sync` repair. Following
that advice would bypass the transaction and can replay the new target back
onto its old parent.

Doctor, list, stack status, and agent-facing skills must recognize active or
recoverable reparent state. They should:

- report `reparent in progress` with run ID and stage;
- name the exact `--continue`/`--abort` command;
- suppress ordinary `--onto` and `tws sync` repair guidance for affected
  entries;
- remain read-only;
- distinguish a complete, unsupported, corrupt, foreign, and stale
  transaction artifact.

The define phase must decide whether this is a new ancestry reason/state or an
orthogonal operation block carried beside the existing ancestry verdict.

---

## 7. Workspace-mode differences

### 7.1 External

- One linked worktree normally holds each materialized branch.
- Rebase state and index are per worktree.
- Branch-operand rebases must not hijack another worktree's HEAD.
- Ref transactions can bypass holder safety and desynchronize many worktrees.
- Unmaterialized/archived entries need an explicit v1 policy.
- Current external metadata writes per entry; reparent likely needs one
  transaction-level metadata write.

### 7.2 Checkout

- One physical checkout must switch among closure branches if using in-place
  rebase.
- Dirty/detached/in-progress operations already refuse.
- Original branch/HEAD restoration exists, but already-rebased branch refs are
  not rolled back by current abort.
- A scratch/CAS strategy avoids repeated user-checkout churn but still needs
  safe checkout detachment/restoration around ref movement.

### 7.3 Live sessions

Any affected branch with a live or unverifiable tws-owned session should
refuse before mutation unless the selected strategy proves it does not disturb
that worktree. Reuse direct-session and checkout-session evidence; do not infer
semantic agent state.

---

## 8. CLI and operator workflow options

### 8.1 Command placement

| Option | Analysis |
| --- | --- |
| `tws stack reparent <feature> <entry> --onto <dest>` | Best semantic grouping; must extend the existing feature-name/subcommand collision escape |
| `tws reparent <feature> <entry> --onto <dest>` | Clean implementation boundary; adds another top-level verb |
| `tws sync ... --reparent` | Rejected: adds a topology mutation to frozen sync axes/state/help and threatens no-flag compatibility |

`restack` need not be a separate command; ordinary unchanged-parent descendant
repair is already `tws sync --from <entry>`.

### 8.2 Destination grammar

Options:

- `--onto <token>` with current entry-first/literal-ref-second precedence;
- add `--onto-kind entry|ref` for automation and ambiguity;
- mutually exclusive `--onto-entry`/`--onto-ref`;
- a separate `--unparent`/`--root` flag.

Whatever grammar is chosen, the plan must publish configured token, kind,
resolved ref, and SHA.

### 8.3 Safety workflow

The existing operator model should carry over:

```text
plan -> inspect -> replay token through a plan -> guarded execution
```

Candidate flags:

```text
--plan
--json
--max-replay-per-entry
--max-replay-total
--approve-plan
--fetch / --no-fetch
--continue / --abort
--cutoff <ref>                 # only if define permits authored boundaries
```

There is no need for a weaker `--yes` prompt. The CLI has no TTY-dependent
behavior today and automation should receive the same safety contract.

`--push` should be omitted from v1. A rewritten branch should not be pushed
before the operator understands and retargets any remote PR.

The `--plan` route may perform its declared fetch, but it creates no reparent
state, scratch worktree, or old/new pin ref. Those belong only below guarded
execution admission.

### 8.4 Plan content

Human and JSON output need:

- old configured parent/kind/ref/SHA;
- recorded cutoff and ancestry validity;
- new configured parent token/kind/ref/pinned SHA;
- target replay set, count, first candidate, determinacy, patch-drop risk;
- ordered descendant closure with per-row replay facts;
- exact metadata delta for `Base` and `LastBaseSHA`;
- holder/worktree restoration effects;
- collateral refs and config effects;
- fetch side effects/freshness;
- guard limits/evaluation/fingerprint;
- remote/upstream divergence and PR guidance;
- transaction strategy and atomicity scope.

The target and descendants should be visually separate: only the target's
configured parent changes.

### 8.5 Streams and errors

- Human/JSON plan goes to stdout.
- Operational/fetch prose and refusals go to stderr.
- JSON is one complete value plus one newline.
- Existing native refusals remain unmarked.
- Replay-limit/approval refusals may retain `plan-guard:`.
- Reparent-specific refusals need a deliberately chosen namespace.
- Exit status remains 0 for a produced plan and 1 for execution refusal/error.

---

## 9. Remote and PR behavior

Reparent changes no provider-side PR base and no remote ref by default.

Verified after a local reparent:

- branch upstream configuration is unchanged;
- local and remote branches diverge;
- a normal push is rejected;
- bare `--force-with-lease` rejected when the local remote-tracking ref was
  stale relative to the remote;
- after `git fetch` refreshed the tracking ref to a teammate's unintegrated
  commit, bare `--force-with-lease` allowed that commit to be overwritten;
- an explicit lease bound to the freshly observed remote SHA likewise allowed
  the overwrite;
- `--force-with-lease --force-if-includes` rejected the unintegrated remote
  work in the tested case.

Guidance should:

- state that tws changed no remote or PR;
- show old base -> new base;
- detect GitHub/Azure wording from a local remote URL only, or fall back to
  generic text;
- recommend retargeting and integrating/acknowledging remote work before
  pushing;
- never recommend an explicit lease bound to a freshly observed but
  unintegrated remote tip;
- if an explicit lease is documented, bind it only to the last remote tip the
  operator actually integrated, not merely the latest observed SHA;
- prefer `--force-with-lease --force-if-includes` when the repository/user Git
  capability supports it, while explaining that the operator must still
  inspect remote divergence;
- do not imply that `--force-if-includes` adds protection to an explicit
  `--force-with-lease=<ref>:<expect>` form; Git documents that combination as
  having no effect;
- never execute provider commands in v1.

Three live operator paths currently reach bare `--force-with-lease`:

- external `tws push <feature>` through `pushFeature`/`pushEntries`;
- external `tws sync <feature> --push`, through `pushFeature`/`pushEntries` for
  legacy/all scope or `pushScoped` for a scoped run;
- checkout `tws sync <feature> --push` through checkout `gitPush`.

Top-level `tws push` in checkout mode is unsupported and exits before a Git
push, so it is not a fourth live path.

Omitting `--push` from the new command does not prevent an operator from using
those paths immediately afterwards. The define phase must decide whether
pending reparent state:

- makes all three live paths warn or refuse until guidance is acknowledged;
- records the last integrated/expected remote SHA for a safer push;
- or leaves push behavior unchanged and limits this feature to explicit
  post-success warnings.

The plan and success output must not describe a normal push as the usual tws
next step without this decision.

---

## 10. Compatibility boundaries

The feature must preserve:

- every no-flag `tws sync` golden;
- existing `tws sync --plan` JSON key set and fingerprint domain unless an
  explicit breaking change is chosen;
- external multi-worktree behavior outside the new command;
- checkout single-repository semantics;
- logical/Git branch decoupling;
- current stack/status/doctor outputs outside deliberate new command help;
- absence of implicit pushes or PR-provider calls;
- current Git minimum unless capability-gated;
- old binaries failing closed on reparent recovery state.

Known directly-coupled issue: `SaveStack` is not atomic. A reparent transaction
must not risk truncating `stack.yaml`; either make stack writes atomic in this
boundary or land a hard parent first.

### 10.1 Required documentation and skill surfaces

The user-facing command cannot ship without:

- `README.md` command table and workflow;
- `docs/cheatsheet.md`, including adjacency to existing-branch migration;
- `CHANGELOG.md`;
- `assets/skills/claude/tesseraworkspaces/SKILL.md`;
- `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`;
- `assets/skills/copilot/tws.prompt.md`;
- `docs/roadmap.md` and `docs/engineering-workflow.md`, moving safe
  reparent/restack from current target to shipped and naming the next target.

Existing documentation tests currently assert that safe reparent/restack is
unshipped. Landing this feature must update that assertion deliberately while
keeping planning prose free of flag-reference clutter.

Command completion and help goldens must cover the `stack reparent`
subcommand/feature-name collision if that command placement is selected.

---

## 11. GitHub issue audit

All open repository issues were reviewed before definition.

| Issue | Relationship | Backlog disposition |
| --- | --- | --- |
| [#1](https://github.com/jdbencardinop/tesseraworkspaces/issues/1) - feature-wide inject misses slash-containing logical names | Not reparent behavior, but confirms that logical names cannot be treated as one path component | Registered `fix-inject-slash-worktree-discovery`, hard-dependent on worktree context injection and branch-name decoupling |
| [#2](https://github.com/jdbencardinop/tesseraworkspaces/issues/2) - role-aware worktree/orchestrator templates | Already substantially covered by `named-feature-templates`, template conflict handling, tiered skills, and Copilot/Codex work | Appended the missing independent-role/default/scope/ownership requirements to `named-feature-templates`; added soft ordering edges instead of a duplicate feature |
| [#3](https://github.com/jdbencardinop/tesseraworkspaces/issues/3) - scoped status scans every feature | Independent status performance/failure-isolation bug; relevant to future transaction observability but not reparent execution | Registered `scoped-status-projection`, hard-dependent on agent work status |
| [#4](https://github.com/jdbencardinop/tesseraworkspaces/issues/4) - split-base abort and stale cutoff replay | Directly validates the transaction and cutoff risks in this analysis, but both defects also affect ordinary sync and must not be hidden inside a new command | Registered `sync-transactional-abort` and `sync-cutoff-integrity`; added both as soft ordering references for safe reparent |

### 11.1 Issue #4 defect 1: partial sync rollback

The reported six-branch `--full` run advanced the anchor to a new master,
failed on a child, and left the anchor advanced after `--abort`. That is the
existing sync behavior described in this analysis: current abort clears state
or the active rebase but does not restore every ref already moved by the run.

Safe reparent must not repeat this failure. Its transaction requirements cover
the target and complete affected closure. However, fixing only reparent would
leave ordinary sync broken, so full sync rollback is tracked separately in
`sync-transactional-abort`.

The issue's real stack shape should be reused as a safe-reparent acceptance
fixture:

```text
master
`-- pr1
    |-- pr2
    |-- pr3
    |   `-- pr5
    |       `-- pr6
    `-- pr4
```

It proves rollback and recovery over branching, not only a linear chain.

### 11.2 Issue #4 defect 2: wrong cutoff provenance

The reported rebase replayed 34 master commits rather than the child's own
commit. The manual repair used:

```text
git rebase --onto <new-parent-tip> <old-parent-tip>
```

This confirms the per-row cutoff model, but safe reparent alone cannot repair
ordinary `tws sync`. `sync-cutoff-integrity` owns:

- auditing how `LastBaseSHA` was attributed in the failing run;
- validating cutoff existence/ancestry in ordinary full/local-only sync;
- preventing broad plain-rebase fallback in stale-parent cases;
- defining absence/fallback behavior for ordinary sync;
- exposing the chosen cutoff/provenance in plans and status.

The safe-reparent define phase may reuse its pure cutoff validator or state
model later, but does not silently broaden its implementation boundary.

### 11.3 Definition impact

Issue #4 adds no new reparent command surface. It strengthens these existing
requirements:

- whole-closure pre-images and rollback/resume evidence;
- branch-shaped as well as linear real-Git fixtures;
- per-row cutoff provenance and ancestry;
- status/doctor reporting during partial progress;
- explicit separation between reparent recovery and ordinary sync bug fixes.

---

## 12. Dependency analysis

Registered hard parents:

- `rebase-plan-guard`;
- `sync-modes`;
- `stack-status`;
- `amend-aware-rebase`;
- `stack-ancestry-doctor`;
- `branch-name-decoupling`.

Registered soft parents:

- `skill-distribution`;
- `tiered-skill-system`;
- `sync-transactional-abort`;
- `sync-cutoff-integrity`.

The hard set is sufficient and intentionally explicit:

- plan/approval/JIT behavior comes from rebase-plan-guard;
- frozen modes/selection/recovery conventions come from sync-modes;
- status and shared ancestry facts come from stack-status and
  stack-ancestry-doctor;
- cutoff algebra comes from amend-aware-rebase;
- `Name` versus `GitBranch()` comes from branch-name-decoupling.

Some edges are transitively redundant, but preserve direct semantic ownership.
No dependency on patch-equivalence research, branch import, session-provider
work, or PR adapters is warranted.

---

## 13. Test strategy

All Git behavior must use real temporary repositories, bare remotes, and
linked worktrees.

High-value existing fixtures:

- `setupCustomerTopologyExternal` and `setupCustomerTopologyCheckout` create
  the required Git commit topology, but currently use a single stack entry
  already based on master/main. Reparent tests must add a real `pr1` entry,
  set `pr2.Base = "pr1"`, and add no-cutoff and stale-descendant variants;
- external scoped fixtures and checkout transaction fixtures;
- crash hooks and I/O fault seams;
- no-flag sync goldens;
- existing command/help goldens;
- stack ancestry/status fixtures.

Required test groups:

1. customer cutoff algebra in both modes;
2. recorded, old-parent-fallback, operator-supplied, absent, missing, and
   cutoff-not-ancestor cases;
3. every destination kind and ambiguity;
4. metadata cycles including unmaterialized entries;
5. closure snapshot versus live-ref regression;
6. siblings and outside-closure refs unchanged;
7. exact `Base`/`LastBaseSHA` post-state;
8. decoupled names and duplicate Git branches;
9. cross-repo refusal;
10. dirty, holder, live-session, and in-progress operation refusals;
11. config-hermetic no-update-refs/no-autostash behavior;
12. plan/JSON/fingerprint/approval;
13. CAS race all-or-nothing under files and reftable;
14. pre-image pins surviving aggressive GC;
15. every crash window and idempotent resume;
16. files-backend partial ref commit reconciliation;
17. rollback refusing after new user work;
18. sequential conflict continue/abort if retained;
19. worktree/check-out restoration without destructive reset shortcuts;
20. archived/unmaterialized pass-2 rows never using plain `git rebase <base>
    <branch>`;
21. atomic stack-write failure;
22. doctor/list/stack status suppressing ordinary repair guidance while a
    reparent transaction exists;
23. safe remote guidance, all three live push entry points (including scoped
    external sync), checkout top-level-push refusal, and zero provider calls;
24. downgrade refusal;
25. unchanged no-flag sync and existing status/doctor behavior outside active
    reparent state;
26. help/completion collision for a feature literally named `reparent`;
27. source-level assertion that reparent is the only path that **re-points an
    existing entry's** `Base` as a topology operation; creation, import, and
    rename remain legitimate other writers.

The suite is already expensive. The define phase should group matrix cells
into shared real-Git fixtures and avoid duplicating the full sync matrix when
a source/pure-function assertion is sufficient.

---

## 14. Ranked risks

1. **Silent wrong replay set** if cutoff ancestry is not checked.
2. **No safe cutoff for a never-synced target** unless the fallback/override
   contract is explicit.
3. **False atomicity claim** across refs, worktrees, and metadata, including
   files-backend partial commit after a crash.
4. **Worktree desynchronization** from direct ref transactions.
5. **Old/new base ambiguity** around `LastBaseSHA`.
6. **Downgrade fail-open** if reparent state is only additive.
7. **Partial-progress rollback loss** if old and planned tips are not pinned
   against GC.
8. **Collateral ref movement** through argv or user config.
9. **Repo-blind descendant closure** in multi-repo stacks.
10. **Plan/status/executor destination divergence** across the four base
    resolvers.
11. **Sync schema/token breakage** if reparent extends the frozen document.
12. **Non-atomic `SaveStack`** corruption window.
13. **Misleading doctor/status repair guidance** while reparent recovery state
    exists.
14. **Remote damage** if freshly fetched but unintegrated remote work is used
    as a lease expectation.
15. **Conflict UX complexity** in detached scratch worktrees.
16. **Experimental Git replay dependency**, default-update behavior, and
    missing merge/conflict support.
17. **CI runtime growth** from another large real-Git matrix.

---

## 15. Open decisions for `define`

1. Command: `tws stack reparent` or top-level `tws reparent`.
2. Destination grammar and explicit entry/ref disambiguation.
3. Ref namespace resolution and ambiguity detection for short Git refs.
4. Target cutoff resolution when `LastBaseSHA` is absent: refuse,
   old-parent-tip fallback, explicit operator cutoff, or ordered combination.
5. Operator cutoff flag spelling and fingerprint binding, if allowed.
6. Descendant no-cutoff fallback policy, independently of the target.
7. Same-parent current edge: no-op success or refusal.
8. Same-parent stale edge: redirect to sync or execute restack.
9. Root/unparent representation.
10. Archived target and archived/unmaterialized descendant policy.
11. Cross-repo closure policy; analysis recommends refusal in v1.
12. Affected closure algorithm and repository filtering.
13. Execution strategy: sequential, scratch+CAS, replay+CAS, or composite.
14. Capability probe for `git replay --ref-action=print`; never a version-only
    or default-behavior gate.
15. Merge-commit strategy and `rebase.rebaseMerges`.
16. Conflict workflow and location of conflict resolution.
17. Safe holder detachment/resynchronization without `reset --hard`.
18. Exact atomicity guarantee, including files-backend partial commit.
19. Old/new planned-tip pin namespaces, collision-resistant entry-ID encoding,
    lifecycle, and cleanup.
20. Separate reparent state artifact versus v4 shared state.
21. External `.sync-run.lock`, checkout sync lock, and reparent
    mutual-exclusion matrix.
22. Resume-forward and abort-after-ref-commit rules.
23. Per-ref classification after a files-backend partial commit.
24. Operator-new-work detection before rollback.
25. Atomic `stack.yaml` writer ownership and package placement.
26. `LastBaseSHA` and any old-base field semantics.
27. How active reparent state appears in doctor/list/stack status and suppresses
    ordinary repair advice.
28. Sibling `ReparentPlan` versus changing `RebasePlan`.
29. Machine-readable admission fields for the reparent plan.
30. Reparent-specific fingerprint domain versus sync tuple bump.
31. Reparent refusal domain/ranking and anchored stderr namespace.
32. Exact no-mutation scope for `--plan`; pins and scratch are forbidden.
33. Fetch defaults and plan fetch side-effect disclosure.
34. Whether guard limits/approval are mandatory or optional.
35. Whether `--push` is excluded from v1.
36. Behavior of external `tws push`, external legacy/all/scoped
    `sync --push`, and checkout `sync --push` after an unpushed reparent; keep
    checkout top-level `tws push` unsupported, and decide how
    `--force-if-includes` is capability-gated.
37. Remote URL detection and structured versus prose PR guidance.
38. Consistency among external executor resolution, checkout executor
    resolution, shared-plan `ResolveSyncBase`, and ancestry/status
    `stackBaseRef`.
39. Package boundary and shared planner ownership.
40. Help/completion behavior for a feature named `reparent`.
41. Documentation/skill surfaces and next-roadmap-target update.
42. Test matrix/process-budget limits.
43. Versioning/release posture if state or JSON contracts change.

---

## 16. Analysis verdict

The feature is feasible and fills the next P1 gap, but it must be treated as a
new transactional operation rather than a sync flag.

The safest direction suggested by the evidence is:

- a dedicated reparent command under the stack noun;
- a sibling, versioned reparent plan that reuses existing row/probe types;
- the same preview/limit/approval safety vocabulary in a separate fingerprint
  domain;
- a separate reparent recovery artifact sharing the sync run guard;
- an explicit recorded/fallback/authored cutoff policy plus mandatory ancestry,
  cycle, ambiguity, holder, dirty, duplicate, and repo checks;
- computation away from public refs, followed by one CAS ref transaction when
  possible;
- old/new object pins, per-ref recovery for partial files-backend commits,
  atomic stack writing, and durable forward recovery across the unavoidable
  refs-versus-metadata window;
- mode-specific mutual exclusion with sync and transaction-aware
  doctor/list/stack-status guidance;
- no implicit push or provider PR mutation;
- explicit remote retarget and force-if-includes-aware guidance after success.

The define phase must resolve the atomic execution and worktree restoration
strategy before any implementation or file ledger is considered stable.
