# Spec: safe-reparent-restack

Normative definition. Every requirement is stated as MUST / MUST NOT / MAY.
No open design choice remains. Anything not decided here is listed in §18
**Out of scope** and is a refusal, not an inference.

This spec consumes `analysis.md` in this directory. Where the analysis lists an
option table, this spec selects exactly one option and freezes it.

Reading order for an implementation agent: §2 vocabulary, §3 CLI, §4–§6
resolution and scope, §7 plan document, §8 guard and the post-lock re-snapshot,
§9 execution, §10 metadata and writers, §11 recovery and the commit point,
§12 remote, §13 refusals, §14 compatibility, §15 file ledger,
§16 acceptance criteria, §17 test matrix.

Three invariants govern every other decision and are worth reading first:

1. **Nothing that cannot exist yet is ever guessed.** A descendant's
   destination, the post-image hash, the run id, and the scratch path are all
   published as `null` on a plan route and bound into the fingerprint as
   explicit absences (§7.5a, §7.13). This is what makes a plan's fingerprint
   and its execution's fingerprint agree.
2. **There is exactly one commit point** — durable post-image metadata **and**
   every affected ref at its planned tip (§11.8a). Before that conjunction,
   `--abort` rolls back; after it, `--abort` is forward completion only.
3. **The compatibility envelope gives a bounded downgrade guarantee.** Shipped
   v1.2.16 same-feature sync plain/continue/abort fails closed after the
   envelope exists. It cannot observe the artifact-before-envelope crash
   window, the new workspace-global lock, unrelated-feature checkout runs, or
   top-level push. Operators MUST NOT use an older tws while any reparent is
   active or recoverable (§11.2, §14.3).

---

## 0. Problem statement

`tws` can replay a stack whose topology is fixed (`tws sync`), but it cannot
change one entry's configured parent. The motivating case is a reviewed and
squashed parent:

```text
master = A - B' - D
pr1    = A - B
pr2    = A - B - C        (pr2.Base = "pr1", pr2.LastBaseSHA = B)
```

The operator wants `pr2` to depend on `master` (at `D`) and to replay only `C`.
`git rebase master pr2` and `git rebase --onto D $(git merge-base D pr2) pr2`
both replay `B` and conflict against `B'`. The only correct Git result is:

```text
git rebase --onto D B pr2
```

This feature adds one explicit, previewable, approvable, recoverable
transaction that performs exactly that for a target entry and its affected
descendant closure, and updates `stack.yaml` only after Git success.

---

## 1. Product contract (summary of frozen decisions)

1. One new subcommand: `tws stack reparent`. No `restack` alias. No sync flag.
2. One repository per run. Cross-repository closures refuse.
3. Destination is `--onto <token>` plus `--onto-kind auto|entry|ref`. The
   token the operator typed and the token `stack.yaml` stores are published
   separately; a literal destination is always stored **canonically**.
4. Cutoff is recorded-first, then operator-supplied, then a verified
   old-parent pre-image tip. Never `merge-base`.
5. A **fresh execution** (`!--plan && !--continue && !--abort`) requires an
   exact `--approve-plan` token **and** at least one replay limit.
   `--continue` and `--abort` never require, accept, or reuse a token.
6. Execution computes every row on a detached HEAD, away from public refs,
   then commits all public refs in one `git update-ref --stdin` CAS
   transaction, then writes `stack.yaml` once, atomically and durably.
7. Execution never runs `git reset --hard`, `git replay`,
   `rebase --update-refs`, or any autostash. v1 selects the **merge** rebase
   backend unconditionally.
8. Recovery state is a separate versioned artifact that additionally holds the
   current release's sync/global mutation locks and writes mode-appropriate
   compatibility artifacts. The current release provides complete mutual
   exclusion; v1.2.16 only fails closed for same-feature sync after the
   compatibility envelope exists.
9. No provider call, no implicit push, no automatic PR retarget. The only
   remote this feature ever names is `origin`.
10. `tws sync` flags, help, no-flag goldens, `RebasePlan` schema, and the sync
    fingerprint domain are unchanged.
11. Every value that cannot exist before the replay exists is published as
    `null` on the plan route and is bound into the fingerprint as an explicit
    absence, never as a guess.

---

## 2. Vocabulary (normative)

| Term | Definition |
| --- | --- |
| target | The single `StackEntry` whose `Base` this run re-points |
| old parent | The target's `Base` token as recorded in `stack.yaml` before the run |
| destination | The commit pinned after `--onto` |
| requested token | The exact `--onto` string the operator typed |
| stored token | The canonical string this run writes to `StackEntry.Base` (§10.2) |
| cutoff | The exclusive lower replay boundary passed as the `<upstream>` operand |
| replay set | `cutoff..<row branch>` as `git rev-list --no-merges` reports it |
| affected closure | The target plus its transitive descendants in the same repository, in **reparent closure order** (§6.1a) |
| row | One member of the affected closure; the target has `role = "target"`, every other row `role = "descendant"` |
| pinned destination | A destination whose commit exists before the run mutates anything: always the target's |
| parent-computed destination | A destination that is the computed new tip of an earlier row, and therefore does not exist at plan time |
| pre-image | The snapshot of every affected branch tip, the exact stack bytes, holder identities, and the original HEAD, taken before the first mutation |
| post-image | The exact `stack.yaml` bytes this run will write, computable only after every row has computed |
| pin | A `refs/tws/reparent/...` ref created by this tool to protect an object from GC |
| holder | A worktree (linked or primary) whose HEAD is attached to an affected branch |
| computation worktree | The detached working tree in which rows are computed |
| CAS | The single `git update-ref --stdin` `start`/`update`/`prepare`/`commit` transaction |
| plan route | An invocation carrying `--plan`; it produces a document and mutates nothing except a policy-declared fetch |
| fresh execution | An invocation with none of `--plan`, `--continue`, `--abort`; it is admitted only by §8 |
| recovery route | An invocation carrying `--continue` or `--abort` |
| execution route | A fresh execution or a recovery route; i.e. any invocation without `--plan` |
| commit point | The durable conjunction of the exact post-image bytes in `stack.yaml` and every affected ref at its planned tip or a no-op (§11.8a) |

`merge-base` MUST NOT be used as a cutoff anywhere in this feature.

Wherever this spec says an obligation applies to "the execution route", it
applies to fresh executions **and** recovery routes. Every obligation that
names a token or a replay limit is written as "fresh execution" and never
reaches `--continue` or `--abort`.

---

## 3. Command surface

### 3.1 Placement and grammar

`reparent` MUST be registered as a child of the existing `stackCmd`
(`internal/cli/stack.go`), beside `stackStatusCmd`.

```text
tws stack reparent <feature> <entry> --onto <dest> [flags]     # fresh route
tws stack reparent <feature> --continue                        # continuation
tws stack reparent <feature> --abort                           # abort
```

- The parent `tws stack <feature>` behavior MUST be unchanged.
- There MUST be no `tws restack`, no `tws reparent`, and no `tws sync
  --reparent`.

### 3.2 Arity

A new `stackReparentArgs` validator MUST enforce:

| Route | Positional args | Required flags | Forbidden flags |
| --- | --- | --- | --- |
| fresh | exactly 2 (`<feature> <entry>`) | `--onto` | `--continue`, `--abort` |
| continue | exactly 1 (`<feature>`) | `--continue` | `--onto`, `--onto-kind`, `--cutoff`, `--approve-plan`, `--max-replay-per-entry`, `--max-replay-total`, `--fetch`, `--no-fetch`, `--abort` |
| abort | exactly 1 (`<feature>`) | `--abort` | `--onto`, `--onto-kind`, `--cutoff`, `--approve-plan`, `--max-replay-per-entry`, `--max-replay-total`, `--fetch`, `--no-fetch`, `--plan`, `--continue` |

- Continuation and abort MUST take the target, destination, cutoff, policy,
  limits, validation command, and closure from persisted state. Supplying any
  of them MUST refuse before any lock or Git command.
- `--plan --continue` is permitted and describes only the remaining work.
- `--plan --abort` is forbidden (mirrors `--plan cannot be combined with
  --abort`).

Missing-argument diagnostics MUST name the collision escape when a feature
literally named `reparent` exists, in the exact shape already used by
`stackStatusCmd`:

```text
accepts 2 arg(s), received 0: a feature named "reparent" exists; run "tws stack -- reparent" for its legacy dependency tree
```

### 3.3 Collision escape and completion

- `tws stack -- reparent` MUST continue to reach a feature literally named
  `reparent` and print its dependency tree.
- `stackCmd.ValidArgsFunction` MUST additionally suppress the literal
  `reparent` from feature completion candidates, exactly as it already
  suppresses `status`.
- `--onto` MUST register a completion function offering, in order: the sibling
  stack entry names of the feature (excluding the target and its descendants),
  then local branch names.
- `--onto-kind` MUST complete to exactly `auto`, `entry`, `ref`.

### 3.4 Flags (closed set)

| Flag | Type | Default | Routes | Meaning |
| --- | --- | --- | --- | --- |
| `--onto` | string | `""` | fresh | Destination token |
| `--onto-kind` | string | `auto` | fresh | `auto` / `entry` / `ref` |
| `--cutoff` | string | `""` | fresh | Operator-authored replay boundary |
| `--plan` | bool | `false` | fresh, continue | Produce a document and exit |
| `--json` | bool | `false` | fresh, continue | Emit the document as JSON; requires `--plan` |
| `--max-replay-per-entry` | int | `0` | fresh | Refuse if any row replays more than N candidates |
| `--max-replay-total` | int | `0` | fresh | Refuse if the run replays more than N candidates in total |
| `--approve-plan` | string | `""` | fresh | Approve the exact plan by fingerprint |
| `--fetch` | bool | `false` | fresh | Force fetch on |
| `--no-fetch` | bool | `false` | fresh | Force fetch off |
| `--continue` | bool | `false` | continue | Resume the persisted run |
| `--abort` | bool | `false` | abort | Roll the persisted run back |
| `--verbose` / `-v` | bool | `false` | all | Show full fetch output |

v1 MUST NOT declare `--yes`, `--dry-run`, `--push`, `--test`, `--root`,
`--unparent`, `--only`, `--from`, `--full`, `--local-only`, `--remote`, or any
per-descendant cutoff flag.

Validation is **configured**, never a flag: the run freezes `Config.TestCommand`
(including any per-repository override) at admission and runs it on each
computed row (§9.6).

**Fetch policy (explicit, closed).**

- The default is **fetch** in external mode and **no-fetch** in checkout mode,
  matching `resolveSyncPolicy`'s mode-derived default exactly.
  `policy.fetch_default_applied` is true when neither flag was given.
- `--fetch` and `--no-fetch` are mutually exclusive and take no explicit
  boolean value.
- A fetch, when the policy calls for one, happens **exactly once**, on the
  plan route and on a fresh execution, at §6.3 step 2a: after safe
  workspace/feature resolution and before destination, cutoff, closure, or
  plan resolution. The plan a fresh execution recomputes at admission is
  therefore a post-fetch plan. Per-invocation fetch outcome fields may differ
  (the first fetch may move a tracking ref and the second may be a no-op) and
  are not fingerprint inputs; the post-fetch refs and every execution input
  are fingerprinted, so unchanged post-fetch state reproduces the token.
- `--continue` and `--abort` MUST NOT fetch and MUST NOT accept either flag.
  The persisted `fetch_policy` is recorded for audit only and is never
  re-applied.
- The reused `PlanFetch` semantics are exact. `policy_source` uses only
  `flag | route-default | persisted-transaction`; an effective no-fetch policy
  has `attempted: false`, `outcome: "skipped"`,
  `suppression_cause: null`, empty repos, false mutation facts, and
  `freshness: "local-only"`. A measured fetch copies every repository context,
  candidate, effect, attempted, and outcome fact. Any failed attempted row
  yields `outcome: "failed"`, `freshness: "possibly-stale"`, and unknown
  mutation facts when the failed fetch may have contacted its remote; it MUST
  never render as fetched.

### 3.5 Flag validation order (exact, before any workspace resolution)

The `stackReparentArgs` arity check of §3.2 is Cobra's `Args` phase and runs
before `RunE`; the ordered list below governs `RunE` after arity has
succeeded. Tests that intentionally violate both arity and a flag rule MUST
therefore expect the arity error.

The command MUST validate in exactly this order and return on the first
failure:

1. `--json` without `--plan` → `--json requires --plan`
2. `--continue` with `--abort` → `--continue and --abort are mutually exclusive`
3. `--plan` with `--abort` → `--plan cannot be combined with --abort`
4. each of `--onto`, `--onto-kind`, `--cutoff`, `--approve-plan`,
   `--max-replay-per-entry`, `--max-replay-total`, `--fetch`, `--no-fetch`
   combined with `--continue` or `--abort` →
   `<flag> cannot be combined with --continue`
   / `<flag> cannot be combined with --abort`
5. `--fetch` with `--no-fetch` → `--fetch and --no-fetch are mutually exclusive`
6. explicit boolean values for `--fetch` / `--no-fetch` →
   `--fetch does not take an explicit value; use --no-fetch to disable automatic fetch`
   and the mirrored `--no-fetch` message. The parser's implicit-value marker
   MUST be impossible to carry through OS argv (one NUL byte), and its
   `everExplicit` bit is monotonic: a later bare duplicate cannot erase an
   earlier `--fetch=true|false` / `--no-fetch=true|false`.
7. `--onto-kind` not in `{auto, entry, ref}` →
   `--onto-kind must be one of: auto, entry, ref`
8. fresh route with empty or whitespace-only `--onto` →
   `--onto requires a destination`
9. `--cutoff` present and empty → `--cutoff requires a ref`
10. negative `--max-replay-per-entry` / `--max-replay-total` →
    `--max-replay-per-entry must be zero or greater` /
    `--max-replay-total must be zero or greater`
11. `--approve-plan` whose value does not match `^[0-9a-f]{64}$` →
    `--approve-plan requires a 64-character lowercase hex fingerprint`
12. `--approve-plan` without any replay limit →
    `--approve-plan requires --max-replay-per-entry or --max-replay-total`
13. **fresh execution** (`!--plan && !--continue && !--abort`) without
    `--approve-plan` →
    `tws stack reparent requires --approve-plan <fingerprint>; run with --plan first`
14. **fresh execution** without any replay limit →
    `tws stack reparent requires --max-replay-per-entry or --max-replay-total`

Rules 13 and 14 are guarded by the fresh-execution predicate and are therefore
**unreachable** on `--continue` and on `--abort`. A test MUST assert that a
bare `tws stack reparent <feature> --continue` and a bare
`tws stack reparent <feature> --abort` pass §3.5 without mentioning
`--approve-plan` or either limit.

Rules 10 and 11 reuse the shipped `tws sync` sentences verbatim
(`sync_plan_guard.go`), because they test the identical condition on a sibling
command; rules 1–3, 5, 6 and 12 already do.

Rules 1–14 are **native** errors: they exit 1, print on stderr with no marker
prefix, and preserve Cobra usage output.

Immediately after rule 14 passes — that is, before any workspace resolution,
any Git child process, and any reparent-owned refusal — the command MUST set
`cmd.SilenceUsage = true`, exactly as `stackStatusCmd` does. Every failure
after that point therefore prints its message alone, which is what makes the
one-line contract of §13.2 achievable. A test MUST assert that a §3.5 failure
carries the usage block and that a §13 refusal does not.

### 3.6 Streams and exit codes

| Output | Stream |
| --- | --- |
| Plan human document | stdout |
| Plan JSON document | stdout, exactly one value plus exactly one `\n` |
| Mode header, fetch prose, progress, conflict instructions | stderr |
| `reparent-recovery:` and `reparent-remote:` lines | stderr |
| Every refusal and error | stderr |
| Success summary and remote guidance | stderr |

Stdout carries the plan document and nothing else, on every route. Every Git
child this feature spawns MUST have its stdout and stderr captured into the
run's own buffers and MUST NOT inherit this process's stdout (§9.11).

- A successfully produced plan MUST exit 0 even when it publishes a refusal.
  Exit status is never an admission predicate.
- Reparent MUST NOT call the process-terminating `RequireTool`. A missing
  `git` executable is a returned error written on stderr for execution routes.
  A plan route that can resolve enough workspace/feature identity MUST instead
  publish one unavailable human/JSON plan document on stdout and exit 0; it
  MUST remain in-process and write no partial non-document stdout.
- Every execution refusal and every execution error MUST exit 1.
- A successful execution MUST exit 0, including one that recovered a partial
  commit (§11.7) or deferred a holder restoration (§9.9).
- A conflict pause MUST exit 1 and MUST print the exact resume instructions of
  §9.7. It is a pause, not a refusal: it carries no marker (§13.2).
- `tws push --dry-run` MUST exit 0 even when it reports a would-refuse
  diagnostic (§12.4a).

---

## 4. Destination resolution

### 4.1 Kind selection

`--onto-kind` selects how the `--onto` token is interpreted:

- `auto` (default) — **entry-first**. If the token is exactly equal to some
  `StackEntry.Name` in the feature's `stack.yaml`, the kind is `stack-entry`.
  Otherwise the kind is `literal-ref`.
- `entry` — the token MUST equal a `StackEntry.Name`. Otherwise refuse
  `destination-kind-mismatch`.
- `ref` — stack entry names MUST NOT be consulted. The token is resolved only
  as a Git ref or object.

The plan MUST publish both `policy.onto_kind_requested` and the resolved
`target.new_parent.kind`, so `auto` is never ambiguous after the fact.

### 4.2 Entry destination

For `kind = stack-entry`:

- The resolved ref MUST be `refs/heads/<parent.GitBranch()>`, matching
  `stackBaseRef` in `internal/stack_ancestry.go`.
- The parent entry MUST be in the same repository as the target
  (`StackEntry.Repo` equal after normalization), otherwise refuse
  `cross-repo-closure`.
- The parent entry MUST NOT be archived, otherwise refuse `affected-archived`.
- The ref MUST exist, otherwise refuse `destination-unresolvable`.

### 4.3 Ref destination — namespace enumeration

For `kind = literal-ref`, resolution MUST be explicit and MUST NOT delegate
ambiguity to a bare `git rev-parse <token>`.

The operator token is byte-significant. CLI validation MAY trim only to decide
that an all-whitespace required value is empty; every non-empty `--onto` and
`--cutoff` value MUST otherwise flow unchanged into resolution,
`requested_token` / `supplied_token`, persisted state, and the fingerprint.
In particular `" main "` is not `main`: it is resolved as those exact bytes
and ordinarily refuses as unresolvable.

**Pseudo refs are unsupported.** The tokens `HEAD`, `FETCH_HEAD`, `ORIG_HEAD`,
`MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `REBASE_HEAD`, `AUTO_MERGE`,
and `BISECT_HEAD` MUST refuse `destination-unresolvable` with the detail
`pseudo refs are not a reparent destination; name a branch, a full ref, or an object id`.
The gitrevisions `$GIT_DIR/<refname>` rung is therefore deliberately **not**
enumerated: a destination that moves whenever Git moves it is not a topology
decision. Other uppercase tokens remain ordinary short names and may resolve
to a real branch or tag. §5.3 applies the identical rule to `--cutoff`.

1. If the token matches `^refs/` it is a **full ref**. It MUST exist exactly
   — verified by `git for-each-ref --format='%(refname)' <token>` returning
   that exact string — otherwise refuse `destination-unresolvable`. No
   enumeration is performed.
2. Else if the token matches `^[0-9a-fA-F]{4,64}$` it is a candidate
   **object id**, resolved by §4.3a. Hex recognition is case-insensitive;
   the canonical form this feature stores and compares is always lowercase.
3. Else the token is a **short name**. The implementation MUST enumerate
   exactly these **five** candidate full refs, in this order, using
   `git for-each-ref` (never `rev-parse`):

   ```text
   refs/<token>
   refs/tags/<token>
   refs/heads/<token>
   refs/remotes/<token>
   refs/remotes/<token>/HEAD
   ```

   The five patterns MUST be passed to one `git for-each-ref
   --format='%(refname)'` invocation, and every returned line MUST be
   filtered by **exact string equality** against the five candidates before
   it counts. `for-each-ref` treats its operands as patterns, so an
   unfiltered result can contain refs the operator never named; this filter
   is the whole reason `rev-parse` is not used.
   - Zero surviving candidates → refuse `destination-unresolvable`.
   - More than one surviving candidate → refuse `destination-ambiguous`. The
     detail MUST list every candidate ref, sorted, and MUST tell the operator
     to re-run with the full ref.
   - Exactly one candidate → that ref is the resolved destination ref.

4. The resolved ref MUST then be peeled:
   `git rev-parse --verify --quiet --end-of-options <resolved>^{commit}`.
   Failure refuses `destination-unresolvable`. The result is the **pinned
   destination SHA**, in canonical lowercase OID form, and is the only value
   ever passed after `--onto`.

Tags, annotated tags, remote-tracking refs, and raw commits are all permitted
once pinned. A remote-tracking destination MUST raise warning
`destination-remote-tracking` disclosing that upstream tracking configuration
is not changed by this feature.

### 4.3a Object-id resolution (exact, no `--quiet` ambiguity loss)

`git rev-parse --verify --quiet <abbrev>` exits 1 silently for *both* "unknown"
and "ambiguous", so it can never distinguish them. Object ids MUST therefore be
resolved as follows:

1. Determine the repository's **canonical OID width** once per run from
   `git rev-parse --show-object-format` (available below this feature's Git
   2.38 floor): `sha1` means `40`, and `sha256` means `64`. Any other or
   unreadable value refuses `capability-unsupported`. A resolved plan publishes
   the width as `policy.oid_width` and freezes it in state; an unavailable plan
   publishes JSON `null` and human `unknown`, never `0`. No other width is ever
   assumed.
2. If `len(token) == oid_width`, the token is a **full OID**: run
   `git rev-parse --verify --quiet --end-of-options <lowercased token>^{commit}`.
   Failure refuses `destination-unresolvable`.
3. Otherwise the token is an **abbreviated OID**: run
   `git rev-parse --disambiguate=<lowercased token>` and count its lines.
   - `0` lines → the token is not an object id. Fall through to §4.3 step 3
     and resolve it as a short name.
   - `>1` lines → refuse `destination-ambiguous`, detail listing every
     returned OID, sorted, and naming the full-OID remedy.
   - `1` line → that full OID is the object. Peel it with
     `git rev-parse --verify --quiet --end-of-options <oid>^{commit}`; a
     non-committish object refuses `destination-unresolvable`.
   The count deliberately includes every object type. A prefix that matches
   one commit and one blob is refused as ambiguous even if a later
   `<oid>^{commit}` peel could select the commit; reparent requires a globally
   unambiguous object spelling.
4. Whenever step 2 or step 3 yields a commit, §4.3 step 3's short-name
   enumeration MUST **also** be run. If it yields any surviving candidate, the
   token is both an object id and a ref name: refuse `destination-ambiguous`,
   detail naming both the OID and every candidate ref.

§5.3 applies §4.3 and §4.3a unchanged to `--cutoff`.

### 4.3b Canonical stored form

The token this run writes to `StackEntry.Base` is **never** the short token
the operator typed:

| Resolved kind | `resolution` | Stored token |
| --- | --- | --- |
| stack entry | `entry` | the parent's `StackEntry.Name`, exactly |
| full ref given | `ref-full` | the given full ref, verbatim |
| short name → `refs/heads/<x>` | `ref-head` | `refs/heads/<x>` |
| short name → `refs/tags/<x>` | `ref-tag` | `refs/tags/<x>` |
| short name → `refs/remotes/...` | `ref-remote` | the full `refs/remotes/...` ref |
| short name → `refs/<x>` | `ref-other` | `refs/<x>` |
| object id | `raw-oid` | the full canonical lowercase OID |

Storing the full ref is what makes `--onto-kind ref` safe against an entry
name that happens to match, and what stops `ResolveSyncBase`'s
`origin/<default>` rewrite from ever re-interpreting a token this feature
chose. The plan MUST publish `requested_token` and `stored_token` separately
on both `old_parent` and `new_parent`; a subsequent run's `old_parent` is
whatever the previous run stored.

### 4.3c Resolver agreement (mandatory, all kinds)

Before admission, the run MUST build the **post-image graph** — the stack it
would write — for every destination kind, and MUST confirm that all four
current base resolvers, given the **stored token**, agree on the pinned
destination OID:

1. `ResolveReparentDestination` (§4.7);
2. `ResolveSyncBase` (`internal/rebase_planner.go`);
3. `stackBaseRef` (`internal/stack_ancestry.go`);
4. the checkout executor's base resolution (`internal/checkout_sync.go`).

Resolvers 2–4 currently return ref/token spellings rather than a uniformly
peeled OID. The agreement adapter MUST therefore take each resolver's answer
and normalize it with
`git rev-parse --verify --quiet --end-of-options <answer>^{commit}` before
comparison. This is load-bearing for annotated tags: the checkout resolver's
bare `rev-parse` observes the tag-object OID, while the normalized answer must
be the peeled commit OID. Shipped `ResolveSyncBase` behavior remains
unchanged: its token decision is extracted into a pure helper taking an
already measured default branch, while reparent measures that branch through
`runReparentGit` with `Cmd.Dir`. A PATH-level shim over the complete route
MUST observe no `-C` process.

A literal destination has one additional semantic gate before resolver
comparison: its canonical stored token (full ref or raw OID) MUST NOT equal
the `Name` of any stack entry. Existing readers are entry-first, so even when
that entry's branch currently resolves to the same OID, persisting the token
would change meaning when the entry later moved. Refuse
`destination-resolver-divergent`, naming the colliding stored token and entry;
OID equality never waives this ambiguity.

Any normalized resolver answer that differs from the pinned commit, or fails
to resolve, refuses
`destination-resolver-divergent`, detail naming the resolver, the stored
token, the expected OID, the raw answer, and the normalized value. This is the check that
guarantees the very next `tws sync` and `tws stack status` see exactly the
edge this feature wrote; it is not optional for any kind, and it is the
reason §10.2 forbids the `origin/<default>` rewrite rather than merely
discouraging it.

### 4.4 Root / unparent

There is no `--root` and no `--unparent` flag. Making an entry a stack root is
expressed by pointing it at a literal ref:

```text
tws stack reparent feat pr2 --onto master --onto-kind ref ...
```

- The requested token is `master`; the **stored** token is the canonical
  `refs/heads/master` (§4.3b), so the edge cannot later be re-read as a stack
  entry named `master` or rewritten to `origin/master`.
- An empty destination is rejected earlier by §3.5 rule 8. The
  `destination-unset` kind remains a defence-in-depth builder result for an
  internal caller that bypasses CLI validation; it is unreachable from the
  supported CLI.
- `Base` is never written empty by this feature.

### 4.5 Graph safety

Before any mutation the run MUST build the **post-image logical graph** (the
stack with the target's `Base` replaced by the stored token of §4.3b) and MUST
refuse when, using that graph and not Git ancestry:

- the destination entry is the target itself → `destination-self`;
- the destination entry is a transitive descendant of the target →
  `destination-descendant`;
- the post-image graph fails the closure sort of §6.1a →
  `destination-cycle`;
- the **current** stack already fails that sort → `stack-unsortable`.

A literal-ref destination cannot name a stack entry once stored canonically
(§4.3b), so `destination-self` and `destination-descendant` cannot fire for
it. The cycle check nevertheless MUST run on the post-image graph for **every**
kind, because it is cheap, because it is the only structural assertion that the
bytes this run writes are still sortable, and because §4.3c already built that
graph.

### 4.6 Same-parent cases

Let `sameParent` be true when the **stored** new `Base` token equals the
current `Base` token **and** the resolved destination SHA equals the resolved
current parent SHA.

- `sameParent` **and** the target's `StackEdge.Status` is
  `AncestryStatusCurrent` → **no work**. A plan route MUST emit
  `summary.plannability = "no-work"`, `summary.has_work = false`,
  `runnable = false`, `refusal.kind = null`, and `approval.fingerprint = null`
  (no usable token). Once this strict verdict is known, cutoff/replay/merge and
  capability-only blockers and guard evaluations are suppressed because no
  replay can run. An execution route MUST refuse only `no-work`.
- `sameParent` **and** the edge is not current (stale, divergent, missing,
  cross-repo, unevaluated) → refuse `destination-same-parent-stale` with the
  exact guidance:

  ```text
  the configured parent is unchanged; replay this edge with: tws sync <feature> --from <entry>
  ```

### 4.7 Resolver consistency

This feature MUST NOT rewrite `ResolveSyncBase`, `stackBaseRef`, `TopoSort`,
or either sync executor. It MUST instead own one resolver,
`ResolveReparentDestination`, which is the single source of the published
destination facts (`requested_token`, `stored_token`, `kind`, `ref`, `sha`,
`repo`, `resolution`, `candidates`), and the plan, executor, CAS values,
metadata write, and post-condition MUST all read that one result.

`ResolveReparentDestination` MUST NOT perform the `origin/<default>` rewrite
that `ResolveSyncBase` performs. A reparent destination is exactly what the
operator named, stored canonically. Because §4.3b stores a full ref, the
rewrite has nothing to rewrite and §4.3c's agreement check passes by
construction; the plan MUST still raise warning
`destination-not-remote-rewritten` when the **requested** token equals the
repository default branch, disclosing that `origin/<default>` was **not**
substituted, naming both refs, and stating the concrete consequence: future
syncs use the local `refs/heads/<default>` ref, not the fetched
`refs/remotes/origin/<default>` ref.

---

## 5. Cutoff resolution

### 5.1 Target cutoff ladder (closed, ordered)

Let `R` be the target's `LastBaseSHA` from the pre-image snapshot and `S` the
`--cutoff` value.

1. `R != ""` and `R` resolves to a commit → **`R` is authoritative**,
   `provenance = "recorded-by-sync"`.
   - If `S` is supplied, it MUST resolve and MUST resolve to the same commit
     as `R`. Otherwise refuse `cutoff-conflict`.
2. `R != ""` and `R` does not resolve → refuse `cutoff-unresolvable`. A
   supplied `S` MUST NOT override it; the detail MUST tell the operator to
   restore or fetch the missing object.
3. `R == ""` and `S` supplied → `S` is resolved by §5.3 and used,
   `provenance = "operator-supplied"`.
4. `R == ""` and `S` absent → resolve the **old parent pre-image tip**: the
   commit that the target's current `Base` resolves to via §4.2/§4.3 rules
   applied to the old token. It MAY be used **only if** it resolves **and**
   `git merge-base --is-ancestor <tip> <target branch>` succeeds.
   `provenance = "old-parent-tip"`.
5. Otherwise refuse `cutoff-absent`, with the detail naming both remedies:
   supply `--cutoff <ref>`, or establish a baseline with
   `tws sync <feature> --only <entry>`.

`merge-base` MUST NOT appear in any rung.

### 5.2 Descendant cutoff ladder (closed, ordered)

For each descendant row `D` with pre-image `LastBaseSHA` `RD`:

1. `RD != ""` and resolves → authoritative, `provenance = "recorded-by-sync"`.
2. `RD != ""` and does not resolve → refuse `cutoff-unresolvable`.
3. `RD == ""` → use the **pre-image tip of `D`'s configured parent branch**,
   and only when `git merge-base --is-ancestor <tip> <D branch>` succeeds.
   `provenance = "old-parent-tip"`.
4. Otherwise refuse `descendant-cutoff-absent`.

`--cutoff` applies to the target only. If the operator needs a different
descendant boundary, v1 refuses `descendant-cutoff-override-unsupported` with:

```text
per-descendant cutoffs are not supported; establish descendant baselines first with: tws sync <feature> --from <entry>
```

A descendant MUST use its own snapshotted cutoff. The run MUST NOT substitute
the target's old tip for a descendant that has its own recorded cutoff, and
MUST NOT use any live (post-replay) ref as a cutoff.

### 5.3 `--cutoff` token resolution

`--cutoff` MUST use the identical namespace enumeration of §4.3 and the
identical object-id resolution of §4.3a, including the pseudo-ref refusal.
Every ambiguity a cutoff token can exhibit — two surviving ref candidates, a
multi-match abbreviated OID, or a token that is both an OID and a ref —
refuses **`cutoff-unresolvable`**, with a detail listing every candidate,
sorted, and naming the full-ref or full-OID remedy. There is no
`cutoff-ambiguous` kind and `destination-ambiguous` is never raised for a
cutoff token: the cutoff is a boundary, not a destination, and one kind with a
complete candidate list is more useful than two kinds with split details.

### 5.4 Mandatory Git preflight

**Static preflight (every row, before any mutation).** In this order:

```text
git rev-parse --verify --quiet --end-of-options <cutoff>^{commit}
git rev-parse --verify --quiet --end-of-options <row branch ref>
git merge-base --is-ancestor <cutoff> <row branch ref>
```

**Static destination probe (target only).**

```text
git rev-parse --verify --quiet --end-of-options <pinned destination>^{commit}
```

The destination probe MUST NOT be attempted for a descendant. A descendant's
destination is `parent-computed` (§9.4): it is the computed new tip of an
earlier row and **does not exist** before that row runs, so probing it before
mutation could only ever fail. §9.4a instead runs the identical probe
just-in-time, against the pinned computed tip, immediately before that row's
rebase.

- A failed branch resolution refuses `branch-ref-missing`.
- A failed cutoff or destination resolution refuses `cutoff-unresolvable` or
  `destination-unresolvable`.
- A failed `--is-ancestor` refuses `cutoff-not-ancestor`. This is a hard
  refusal, never a warning, because Git accepts a non-ancestor cutoff with
  exit status 0 and silently replays a broader set.

### 5.5 Merge commits

For every row the run MUST evaluate
`git rev-list --count --merges <cutoff>..<row branch>`.
A non-zero count refuses `merge-commit-in-replay-set`. v1 always passes
`--no-rebase-merges`; silently flattening a merge is not an acceptable
outcome for a topology mutation.

### 5.6 Published provenance

`cutoff.provenance` is a closed domain:
`recorded-by-sync | operator-supplied | old-parent-tip | none`.
`cutoff.recorded_state` reuses the existing vocabulary
`absent | present | unresolvable`.

---

## 6. Affected closure and scope

### 6.1 Closure algorithm

1. Read and decode `stack.yaml`.
2. Validate identity immediately after decode, before target lookup, closure
   ordering, `TopoSort`, repository resolution, fetch, or any Git child:
   - duplicate `StackEntry.Name` → `duplicate-entry-name`;
   - two non-archived entries with the same `GitBranch()` → `duplicate-git-branch`.
   Both checks are pure.
3. Resolve the target by exact `Name`. Unknown → `target-unknown`.
4. Resolve each distinct entry repository path to a canonical root/common-dir
   identity before constructing reparent edges. A potential edge exists when
   `child.Base == parent.Name` and either the stored repo tokens are equal or
   the resolved common-dir identities are equal, so an alias child cannot
   silently disappear. v1 then **refuses** an edge whose common dir is equal
   but exact raw `Repo` tokens differ, including different canonical-path,
   symlink, `..`, or whitespace spellings, using
   `destination-resolver-divergent` and explaining that shipped sync readers
   still compare `SameStackRepo` by stored spelling. Exact equal raw tokens are
   accepted. An entry in a genuinely different repository whose `Base` merely
   happens to spell the same name is not a child; a selected destination whose
   identity genuinely differs refuses `cross-repo-closure`. Raw `Repo` strings
   remain unchanged in the plan, state, and metadata.
5. For every entry that *is* a genuine logical descendant, resolve its
   **execution common directory** (`git rev-parse --git-common-dir` from the
   row's repository context). If any genuine closure row's common-dir differs
   from the target's, the run refuses `cross-repo-closure`; it MUST NOT
   silently drop it. Comparing the common dir, rather than the configured
   `Repo` string, is what makes a symlinked or differently-spelled path resolve
   to the same repository instead of a false refusal.
6. Order the closure by §6.1a and keep only members of the closure. The target
   is always first. An unsortable closure refuses `stack-unsortable`.

### 6.1a Reparent closure order (stable, reparent-owned)

`TopoSort` is frozen (§14.1) and its sibling order is not specified. A
fingerprint and a replay sequence both need a total order that never changes
between a `--plan` and its execution, so this feature owns one:

```go
func ReparentClosureOrder(stack Stack, target string) ([]StackEntry, error)
func ReparentClosureOrderByRepoIdentity(stack Stack, target string, identities map[string]string) ([]StackEntry, error)
```

- Kahn's algorithm over the logical edges of §6.1 step 4. The first function
  preserves the pure raw-token helper; production uses the identity-aware
  variant after measuring/caching common dirs.
- The ready set is a **min-heap keyed by the entry's index in
  `Stack.Branches`** — that is, `stack.yaml` declaration order is the sole
  sibling tie-break. Declaration order is a stable, operator-visible,
  byte-durable property of the file this run also rewrites.
- If the ready set becomes empty before every vertex is emitted, the
  unprocessed vertices form a cycle. The function returns an error, which
  callers surface as `stack-unsortable` (current stack) or
  `destination-cycle` (post-image graph).
- The function MUST be pure, MUST issue no Git command, and MUST NOT modify
  or call `TopoSort`.

`ReparentClosureOrder` is the **only** ordering used by the closure, by
`row.order`, by the fingerprint's closure tuple (§7.13 field 19), by the
execution sequence (§9.4), by the CAS transaction line order (§9.11), by the
metadata delta, and by the abort sequence. A test MUST assert that two runs
over the same bytes produce the identical order and that reordering
`Stack.Branches` changes the fingerprint.

### 6.2 Hard scope refusals (all evaluated before mutation)

| Condition | Kind |
| --- | --- |
| Target `Archived == true` | `target-archived` |
| Any closure row `Archived == true` | `affected-archived` |
| A genuine closure row's execution common-dir differs from the target's | `cross-repo-closure` |
| A row's Git branch ref is missing | `branch-ref-missing` |
| The repository root cannot be resolved for a row | `repo-unavailable` |
| A rebase, merge, cherry-pick, revert, or bisect is in progress in any consulted worktree | `git-operation-in-progress` |
| Any consulted worktree has tracked modifications | `context-dirty` |
| A holder is a prunable/stale worktree, or the same branch is held by more than one worktree | `holder-unsafe` |
| Any affected branch has a live, or unverifiable, tws-owned direct or checkout agent session | `session-live` |
| Sync state or lock exists for this feature in this mode | `sync-state-present` |
| A reparent state artifact for another run/feature already exists | `reparent-state-present` |

`untracked-overwrite` is deliberately **absent** from this table: it is a
deferred, just-in-time gate, specified in §9.4b. A pre-mutation inventory
cannot know which tree a not-yet-computed row will check out, so a static
refusal would be either a guess or a false negative.

Archived rows are refused rather than computed: the shipped external pass-2
path that would otherwise handle unmaterialized entries executes
`git rebase <base> <branch>` without a cutoff and is explicitly forbidden here.

Session liveness MUST reuse `GuardDirectSessionsFor` (external) and
`CheckoutSessionPreconditions` / `HasCheckoutAgentSession` (checkout). A record
that cannot be decoded counts as live.

Checkout mode is stricter than the external same-common-dir rule: because it
has one physical checkout and one execution repository, **every non-empty
`StackEntry.Repo` in the consulted stack refuses before fetch, Git probing,
lock acquisition, or mutation**, including the target and a would-be
descendant. Checkout execution always uses the workspace repository. This is
a reparent rule and also remains the existing fresh explicit-new-mode checkout
sync preflight; it MUST NOT be added to the frozen legacy/no-flag checkout
sync path or to checkout `--continue`/`--abort`, because older transactions
that ignored `Repo` must remain recoverable.

### 6.3 Ordering of validation

The run MUST validate in this order, refusing at the first failure:

1. flags (§3.5), then `SilenceUsage = true`;
2. workspace/mode/feature resolution;
2a. the one policy-declared fetch, when enabled (§3.4);
3. stack load, identity, `ReparentClosureOrder` (§6.1 steps 1–3, §6.1a);
4. destination kind, canonical stored form, post-image graph safety
   (§4.1, §4.2, §4.3b, §4.5);
5. closure computation and scope refusals (§6.1 steps 4–6, §6.2);
6. same-parent evaluation (§4.6);
7. destination and cutoff Git resolution, resolver agreement, ancestry
   (§4.3, §4.3a, §4.3c, §5.1–§5.5);
8. capability probes (§9.10);
9. mutual exclusion and state classification (§11.2);
10. plan build, guard evaluation, approval match (§7, §8);
11. lock acquisition, **post-lock re-snapshot and fingerprint re-comparison**
    (§8.5), pre-image capture, state write, compatibility artifact write
    (§11.4);
12. mutation.

Steps 1–10 MUST issue no mutating Git verb. The only Git commands permitted
before step 11 are read-only (`rev-parse`, including `--show-object-format`, `rev-list`, `for-each-ref`,
`merge-base`, `worktree list`, `status --porcelain`, `config --list`,
`--git-common-dir`, `--show-ref-format`, `--disambiguate`, `--version`) plus a
policy-declared `fetch`.

---

## 7. Plan document — `ReparentPlan` schema v1

### 7.1 Identity

- New Go type `ReparentPlan` in `internal/reparent_plan.go`.
- `const ReparentPlanSchemaVersion = 1`.
- `RebasePlan`, `RebasePlanSchemaVersion`, and the 25-key sync document MUST
  NOT change in any way.

### 7.2 Reused types (byte-identical, no redefinition)

These existing types MUST be embedded as-is because they are exact:

`PlanWorkspace`, `PlanContext`, `PlanRepository`, `PlanRepositoryConfig`,
`PlanConfigSlot`, `PlanConfigIssue`, `PlanEncodingIssue`, `PlanFetch` and all
of its nested types, `PlanEntryHead`, `PlanReplayCandidate`,
`PlanEntryReplay`, `PlanCollateralRef`, `PlanAncestry`, `PlanGuardBlock` and
all of its nested types (`PlanGuardLimitSet`, `PlanGuardLimit`,
`PlanGuardLimitConflict`, `PlanGuardEvaluation`, `ControlledPathBlocker`),
`PlanStateSnapshot`, `PlanStateFileBase`,
`PlanStateFileCheckoutTransaction`, `PlanStateFileCheckoutLock`,
`PlanStateFileExternalLegacyState`, `PlanStateFileExternalPayload`,
`PlanStateFileExternalRunGuard`, `PlanStateWorktree`, `PlanStateGitOp`,
`PlanStateHead`.

`PlanEntry`, `PlanEntryBase`, `PlanEntryDestination`, `PlanEntryCutoff`,
`PlanBlocker`, `PlanRefusal`, `PlanApproval`, `PlanSummary`, `PlanPolicy`,
`PlanIntent`, `PlanPush`, `PlanRestore`, and `PlanState` MUST NOT be reused:
each either carries sync-only semantics or embeds the sync `RefusalKind`
domain.

### 7.3 Top-level key order (frozen, exactly 25 keys)

Emitted in declaration order:

| # | Go field | JSON key | Type |
| ---: | --- | --- | --- |
| 1 | `SchemaVersion` | `schema_version` | `int` |
| 2 | `Route` | `route` | `string` |
| 3 | `Invocation` | `invocation` | `string` |
| 4 | `Workspace` | `workspace` | `PlanWorkspace` |
| 5 | `Feature` | `feature` | `string` |
| 6 | `Policy` | `policy` | `ReparentPlanPolicy` |
| 7 | `Target` | `target` | `ReparentPlanRow` |
| 8 | `Descendants` | `descendants` | `[]ReparentPlanRow` |
| 9 | `MetadataDelta` | `metadata_delta` | `ReparentPlanMetadataDelta` |
| 10 | `Strategy` | `strategy` | `ReparentPlanStrategy` |
| 11 | `Holders` | `holders` | `ReparentPlanHolders` |
| 12 | `Remote` | `remote` | `ReparentPlanRemote` |
| 13 | `Fetch` | `fetch` | `PlanFetch` |
| 14 | `Freshness` | `freshness` | `string` |
| 15 | `Repositories` | `repositories` | `[]PlanRepository` |
| 16 | `State` | `state` | `ReparentPlanState` |
| 17 | `Runnable` | `runnable` | `bool` |
| 18 | `Blockers` | `blockers` | `[]ReparentPlanBlocker` |
| 19 | `Warnings` | `warnings` | `[]ReparentPlanWarning` |
| 20 | `EncodingIssues` | `encoding_issues` | `[]PlanEncodingIssue` |
| 21 | `ConfigIssues` | `config_issues` | `[]PlanConfigIssue` |
| 22 | `Summary` | `summary` | `ReparentPlanSummary` |
| 23 | `Guard` | `guard` | `PlanGuardBlock` |
| 24 | `Refusal` | `refusal` | `ReparentPlanRefusal` |
| 25 | `Approval` | `approval` | `ReparentPlanApproval` |

`route` domain: `fresh | continue`. (`abort` produces no plan.)
`invocation` domain: `plan-only | execute`. A `--plan` invocation always
publishes `plan-only`; an execution route that renders a document as part of a
refusal publishes `execute`.
`freshness` reuses the sync domain values verbatim.
Every array is normalized to `[]`, never `null`, except where a field is
explicitly documented as nullable below.

### 7.4 `ReparentPlanPolicy`

| JSON key | Type | Domain / nullability |
| --- | --- | --- |
| `fetch` | `string` | `fetch` / `no-fetch` (reuses `SyncFetchPolicy` values) |
| `fetch_default_applied` | `bool` | true when neither `--fetch` nor `--no-fetch` was given |
| `onto_kind_requested` | `string` | `auto` / `entry` / `ref` |
| `oid_width` | `*int` | `40` or `64`, probed per §4.3a step 1; `null` only when the plan is unavailable before the probe |
| `cutoff_supplied` | `bool` | — |
| `limits_supplied` | `bool` | fresh route: true when either replay limit flag was given. Continue route: true when the persisted run froze either limit |
| `limits_origin` | `string` | `flags` / `persisted-state` / `none` |
| `validation` | `ReparentPlanValidation` | — |

`ReparentPlanValidation`:

| JSON key | Type | Domain / nullability |
| --- | --- | --- |
| `applies` | `bool` | false when no configured validation exists |
| `source` | `string` | `config-repo` / `config-workspace` / `none` |
| `command_digest` | `*string` | SHA-256 lowercase hex of the frozen command, `null` when `applies == false` |
| `runs_on` | `string` | constant `"each-computed-row"` |
| `requires_clean_context` | `bool` | MUST always be `true` (§9.6) |

The raw frozen command string is deliberately **not** a plan field — a plan
document is printed and pasted, and a test command can carry credentials in an
argument. It lives only in the state artifact (§11.5), which is `0600`.

### 7.5 `ReparentPlanRow`

One type for the target and every descendant.

| JSON key | Type | Domain / nullability |
| --- | --- | --- |
| `role` | `string` | `target` / `descendant` |
| `order` | `int` | 0-based index in `ReparentClosureOrder` (§6.1a) |
| `name` | `string` | `StackEntry.Name` |
| `git_branch` | `string` | `StackEntry.GitBranch()` |
| `repo` | `string` | normalized `StackEntry.Repo`, `""` when unset |
| `materialization` | `string` | `materialized` / `unmaterialized` / `not-applicable` |
| `execution_context` | `PlanContext` | reused |
| `head` | `PlanEntryHead` | reused; pre-image tip |
| `old_parent` | `ReparentParent` | pre-run configured parent |
| `new_parent` | `ReparentParent` | post-run configured parent; for a descendant the configured parent is unchanged, so `token`/`stored_token`/`kind`/`ref`/`repo` equal `old_parent`'s and `sha` is `null` (§7.5a) |
| `destination_binding` | `string` | `pinned` (target) / `parent-computed` (descendant) |
| `destination_parent` | `*string` | the row name whose computed tip is this row's destination; `null` when `destination_binding == "pinned"` |
| `destination_sha` | `*string` | the pinned destination OID; `null` for every `parent-computed` row on every plan route |
| `cutoff` | `ReparentPlanCutoff` | — |
| `strategy` | `string` | constant `"detached-rebase-onto"` |
| `argv` | `[]string` | the canonical argv **template** (§7.5b); never `null`, never empty on a runnable row |
| `effective_backend` | `string` | constant `"merge"` when runnable (§9.4); `"unknown"` only on an unavailable plan |
| `replay` | `PlanEntryReplay` | reused |
| `merge_commits_in_range` | `*int` | `null` when the probe failed |
| `ancestry` | `PlanAncestry` | reused; pre-run edge verdict |
| `pins` | `ReparentRowPins` | — |
| `collateral_refs` | `[]PlanCollateralRef` | refs outside the closure whose tips lie in the replay range |
| `notes` | `[]string` | — |

Exactly 23 keys, in this order.

### 7.5a Deferred facts (normative)

A descendant's destination is the computed new tip of an earlier row. That
commit does not exist while the plan is being built, and it does not exist at
the moment a fresh execution re-computes the plan to compare fingerprints.
Publishing a guess would be a lie the fingerprint then froze. Therefore, on
**every** plan route and at admission time:

| Field | Value for a `parent-computed` row |
| --- | --- |
| `destination_sha` | `null` |
| `new_parent.sha` | `null` |
| `metadata_delta.entries[].last_base_sha_after` | `null` |
| `metadata_delta.stack_sha256_after_expected` | `null` whenever any row is `parent-computed` |
| `strategy.computation_path` | `null` on every plan route |
| `strategy.run_id` | `null` on every plan route |

These nulls are **not** an "unavailable plan" and MUST NOT set
`plannability = "unavailable"`, MUST NOT add a blocker, and MUST NOT make
`runnable` false. They are bound into the fingerprint as explicit absences
(§7.13), so a plan and its execution agree byte-for-byte about what is not yet
known. The real values are computed, persisted, and published by the success
output and by the state artifact (§11.5), never by a plan.

`summary.deferred_rows` counts them.

### 7.5b `row.argv` — canonical template

`row.argv` is the **run-id-free, path-free** argv template. It MUST NOT contain
`git -C <path>`, the scratch path, the run id, or any value that differs
between a plan and its execution. Execution selects the directory with
`exec.Cmd.Dir`, never with `-C`.

Target template:

```text
["git", "-c", "rebase.backend=merge", "-c", "rebase.updateRefs=false",
 "-c", "rebase.autoStash=false", "-c", "rebase.forkPoint=false",
 "-c", "rebase.rebaseMerges=false", "rebase", "--merge", "--no-fork-point",
 "--no-update-refs", "--no-autostash", "--no-rebase-merges",
 "--onto", "<pinned destination oid>", "<row cutoff oid>"]
```

Descendant template — byte-identical except that the `--onto` operand is the
literal placeholder:

```text
"<computed-tip:{parent-name}>"
```

where `{parent-name}` is that row's `destination_parent`. The placeholder is a
literal string in the document and in the fingerprint preimage.

At execution the run produces `materialized_argv` by replacing **exactly one**
element: the single placeholder operand. For a target row,
`materialized_argv == argv` byte-for-byte. `materialized_argv` is persisted in
state (§11.5) and printed in the success output; it is never a plan field and
is never fingerprinted. A test MUST assert that materialization changes at most
one element and that no element ever becomes `-C`.

`ReparentParent`:

| JSON key | Type | Domain / nullability |
| --- | --- | --- |
| `requested_token` | `*string` | exactly what the operator typed; `null` when this parent was not operator-authored |
| `stored_token` | `*string` | the canonical token written to `StackEntry.Base` (§4.3b); `null` only when `kind == "none"` |
| `kind` | `string` | `stack-entry` / `literal-ref` / `none` |
| `ref` | `*string` | full resolved ref; `null` for an object-id destination |
| `sha` | `*string` | pinned canonical OID; `null` when unresolved **or** deferred (§7.5a) |
| `repo` | `*string` | normalized repo of the parent, `null` when literal |
| `resolution` | `string` | `entry` / `ref-full` / `ref-head` / `ref-tag` / `ref-remote` / `ref-other` / `raw-oid` / `unresolved` / `ambiguous` |
| `candidates` | `[]string` | every surviving enumerated candidate ref, sorted; `[]` when unambiguous |
| `resolver_agreement` | `ReparentResolverAgreement` | §4.3c |

`ReparentResolverAgreement`:

| JSON key | Type | Notes |
| --- | --- | --- |
| `checked` | `bool` | — |
| `agreed` | `*bool` | `null` when `checked == false` |
| `resolvers` | `[]ReparentResolverVerdict` | one per §4.3c resolver, in §4.3c order |

`ReparentResolverVerdict`: `name` (`resolve-reparent-destination` /
`resolve-sync-base` / `stack-base-ref` / `checkout-base`), `sha` (`*string`),
`agrees` (`bool`), `detail` (`*string`).

`ReparentPlanCutoff`:

| JSON key | Type | Domain / nullability |
| --- | --- | --- |
| `recorded_sha` | `*string` | pre-image `LastBaseSHA`, `null` when absent |
| `recorded_state` | `string` | `absent` / `present` / `unresolvable` |
| `supplied_token` | `*string` | `--cutoff` value, `null` when not supplied or not this row |
| `supplied_sha` | `*string` | resolved `--cutoff`, `null` otherwise |
| `resolved_sha` | `*string` | the boundary actually used, `null` when refused |
| `provenance` | `string` | `recorded-by-sync` / `operator-supplied` / `old-parent-tip` / `none` |
| `ancestor_of_branch` | `*bool` | result of `merge-base --is-ancestor`, `null` when not probed |
| `conflict` | `*string` | populated only for `cutoff-conflict`, naming both SHAs |

`ReparentRowPins`:

| JSON key | Type | Notes |
| --- | --- | --- |
| `entry_id` | `string` | collision-safe hashed entry identity (§9.8) |
| `old_ref_pattern` | `string` | `refs/tws/reparent/<run-id>/old/<entry_id>` |
| `new_ref_pattern` | `string` | `refs/tws/reparent/<run-id>/new/<entry_id>` |

### 7.6 `ReparentPlanMetadataDelta`

| JSON key | Type | Notes |
| --- | --- | --- |
| `entries` | `[]ReparentPlanMetadataEntry` | one per closure row, in closure order |
| `stack_sha256_before` | `string` | SHA-256 of the exact `stack.yaml` bytes read |
| `stack_sha256_after_expected` | `*string` | SHA-256 of the exact bytes that will be written; `null` when the plan is not runnable, including strict no-work, **and** `null` whenever any row is `parent-computed` (§7.5a) |
| `writer` | `string` | constant `"durable-atomic-stack-writer"` |
| `write_point` | `string` | constant `"after-ref-commit"` |
| `post_image_known` | `bool` | `false` on every plan with a `parent-computed` row; `true` only in state, after every row computes |
| `entries_outside_closure_changed` | `bool` | MUST always be `false` |

`ReparentPlanMetadataEntry`:

| JSON key | Type | Notes |
| --- | --- | --- |
| `name` | `string` | — |
| `base_before` | `string` | — |
| `base_after` | `string` | the **stored** token (§4.3b) |
| `last_base_sha_before` | `*string` | `null` when absent |
| `last_base_sha_after` | `*string` | the pinned destination OID for the target; `null` for every descendant on every runnable plan route (§7.5a); exactly equal to `last_base_sha_before` on strict no-work |
| `last_base_sha_after_source` | `string` | `pinned-destination` (target) / `post-replay-parent-tip` (descendant) / `unchanged` (strict no-work) |
| `changed` | `bool` | — |

### 7.7 `ReparentPlanStrategy`

| JSON key | Type | Notes |
| --- | --- | --- |
| `kind` | `string` | constant `"detached-scratch-cas"` (only v1 value) |
| `run_id` | `*string` | `null` on any plan route; a plan mints no run id |
| `computation_context` | `string` | `external-scratch-worktree` / `checkout-detached-head` |
| `computation_path` | `*string` | **always `null` on a plan route**, because the external scratch path embeds the run id (§9.2); populated only in state and in the success/conflict output |
| `backend` | `string` | constant `"merge"` |
| `uses_git_replay` | `bool` | MUST always be `false` |
| `forbidden_operations` | `[]string` | frozen literal list: `git reset --hard`, `git replay`, `git rebase --update-refs`, `git rebase --autostash`, `git rebase --apply`, `git push` |
| `pin_namespace` | `string` | constant `"refs/tws/reparent/<run-id>"`, a shape, not a resolved path |
| `atomicity` | `ReparentPlanAtomicity` | — |

`ReparentPlanAtomicity` — this block is the honest statement of what "atomic"
means and MUST NOT overstate it:

| JSON key | Type | Notes |
| --- | --- | --- |
| `ref_backend` | `string` | `files` / `reftable` / `unknown` |
| `ref_commit` | `string` | constant `"single-cas-transaction"` |
| `ref_commit_race_atomic` | `bool` | MUST be `true` for all three backend values |
| `ref_commit_crash_atomic` | `*bool` | `true` for `reftable`, `false` for `files`, `false` for `unknown` |
| `metadata_write_atomic` | `bool` | MUST be `true` |
| `metadata_write_durable` | `bool` | MUST be `true` (§10.3a fsyncs file and parent directory) |
| `combined_atomic` | `bool` | MUST be `false`: refs, metadata, and worktrees are three separate effects |
| `partial_commit_recovery` | `string` | `transactional` for `reftable`, `row-by-row` for `files` and for `unknown` |
| `commit_point` | `string` | constant `"durable-post-image-metadata"` (§11.8a) |
| `reference_transaction_hook_may_veto` | `bool` | `true`; a hook veto is an ordinary refusal, never a crash |

An `unknown` ref backend is **permitted**, never a refusal: it is treated
exactly as `files` — the conservative choice — and raises warning
`files-backend-not-crash-atomic` with a detail saying the backend could not be
probed and the run assumed the weaker guarantee.

### 7.8 `ReparentPlanHolders`

| JSON key | Type | Notes |
| --- | --- | --- |
| `applies` | `bool` | — |
| `original_branch` | `*string` | checkout mode HEAD branch, `null` when detached or external |
| `original_head` | `*string` | canonical OID of the original HEAD |
| `original_detached` | `bool` | true when the checkout was already detached |
| `preimage_holder_excluded` | `*string` | checkout mode: the branch whose holder is the computation context itself, detached at the initial switch and therefore excluded from the §9.9 drift check; `null` in external mode |
| `restore_point` | `string` | constant `"after-metadata-write"` |
| `rows` | `[]ReparentPlanHolder` | one per affected branch that is held |

`ReparentPlanHolder`:

| JSON key | Type | Domain |
| --- | --- | --- |
| `git_branch` | `string` | — |
| `holder_kind` | `string` | `linked-worktree` / `primary-checkout` / `computation-context` / `none` |
| `holder_path` | `*string` | — |
| `preimage_sha` | `*string` | — |
| `action` | `string` | `detach-and-restore` / `already-detached` / `none` / `refuse` |
| `safe` | `bool` | — |
| `reason` | `*string` | populated when `safe == false` |

### 7.9 `ReparentPlanRemote`

| JSON key | Type | Notes |
| --- | --- | --- |
| `provider_calls` | `int` | MUST always be `0` |
| `implicit_push` | `bool` | MUST always be `false` |
| `remote` | `string` | constant `"origin"` (§12.1) |
| `rows` | `[]ReparentPlanRemoteRow` | one per closure row |
| `guidance` | `[]string` | ordered prose lines, exactly as printed |
| `followup_record` | `ReparentPlanRemoteFollowup` | — |

`ReparentPlanRemoteRow`:

| JSON key | Type | Domain |
| --- | --- | --- |
| `name` | `string` | — |
| `git_branch` | `string` | — |
| `remote` | `string` | constant `"origin"` |
| `remote_ref` | `*string` | `refs/remotes/origin/<branch>`, `null` when absent |
| `remote_sha` | `*string` | — |
| `upstream_configured` | `bool` | — |
| `divergence` | `string` | `none` / `ahead` / `behind` / `diverged` / `no-upstream` / `unknown` |
| `pr_base_before` | `*string` | old parent Git branch from `old_parent.ref`, never the logical stack-entry name |
| `pr_base_after` | `*string` | new parent Git branch from `new_parent.ref`, `null` for an object-id destination |
| `provider_hint` | `string` | `github` / `azure-devops` / `generic` / `unknown`, derived only from the local `origin` URL |

`ReparentPlanRemoteFollowup`:

| JSON key | Type | Notes |
| --- | --- | --- |
| `will_write` | `bool` | — |
| `write_point` | `string` | constant `"after-new-pins-before-holder-detach"` (§12.3a) |
| `path` | `*string` | §12.3 path, `null` when `will_write == false` |
| `entries` | `[]string` | logical names that would be recorded |
| `clear_rule` | `string` | constant text of §12.5 |

### 7.10 `ReparentPlanState`

| JSON key | Type | Notes |
| --- | --- | --- |
| `snapshot` | `PlanStateSnapshot` | reused |
| `files` | `ReparentPlanStateFiles` | — |
| `worktree` | `PlanStateWorktree` | reused |
| `git_op` | `PlanStateGitOp` | reused |
| `head` | `PlanStateHead` | reused |
| `exclusion` | `ReparentPlanExclusion` | — |
| `approved_fingerprint` | `*string` | persisted fresh-plan fingerprint on `route == "continue"`; `null` on fresh plan documents |

`ReparentPlanStateFiles` embeds the five existing sync file facts verbatim
(`checkout_transaction`, `checkout_lock`, `external_legacy_state`,
`external_run_payload`, `external_run_guard`) and adds:

`external_run_payload.selected` is always a JSON array. It MUST encode as
`[]`, never `null`, on fresh external plans, checkout plans where that file is
not applicable, and unavailable/partial plan documents.

`reparent_state` — `PlanStateFileBase` plus:

| JSON key | Type |
| --- | --- |
| `state_version` | `*int` |
| `run_id` | `*string` |
| `stage` | `*string` |
| `resume_stage` | `*string` |
| `feature` | `*string` |
| `mode` | `*string` |

`reparent_remote_record` — `PlanStateFileBase` plus:

| JSON key | Type |
| --- | --- |
| `record_version` | `*int` |
| `pending_entries` | `int` |

`ReparentPlanExclusion`:

| JSON key | Type | Notes |
| --- | --- | --- |
| `sync_active` | `bool` | any sync payload, sentinel, lock, or transaction present |
| `reparent_active` | `bool` | a reparent state artifact present |
| `lock_holder_pid` | `*int` | — |
| `lock_holder_live` | `*bool` | — |
| `compat_artifacts_consistent` | `*bool` | `false` when reparent state exists without its mode-appropriate compatibility artifacts |

### 7.11 Blockers, warnings, refusal, summary, approval

```go
type ReparentPlanBlocker struct {
    Kind   ReparentRefusalKind `json:"kind"`
    Entry  *string             `json:"entry"`
    Detail string              `json:"detail"`
}

type ReparentPlanWarning struct {
    Kind   string  `json:"kind"`
    Entry  *string `json:"entry"`
    Detail string  `json:"detail"`
}

type ReparentPlanRefusal struct {
    Kind   *ReparentRefusalKind `json:"kind"`
    Detail *string              `json:"detail"`
}
```

Closed warning kind domain (exactly **11**):

```text
destination-remote-tracking
destination-not-remote-rewritten
destination-ancestor-of-old-parent
collateral-ref-in-replay-range
collateral-update-refs-config
holder-detach-required
holder-restore-deferred
untracked-present
remote-divergence-expected
remote-followup-pending
files-backend-not-crash-atomic
```

`destination-ancestor-of-old-parent` MUST be raised when the pinned
destination is an ancestor of the old parent tip: old-parent content then
leaves the subtree, which is legal but surprising.

`holder-restore-deferred` MUST be raised — never the refusal kind
`holder-unsafe` — whenever a holder cannot be re-attached **after** the commit
point (§9.9, §11.8a). Past the commit point the run has succeeded; a holder
left detached is an advisory, and reusing a refusal kind for an advisory is
exactly the category error §13.2 forbids.

`untracked-present` is the inventory warning of §9.4b; `untracked-overwrite`
remains the just-in-time refusal.

`ReparentPlanSummary`:

| JSON key | Type | Notes |
| --- | --- | --- |
| `plannability` | `string` | `rows` / `no-work` / `unavailable` |
| `has_work` | `bool` | — |
| `rows` | `int` | closure size including the target |
| `descendants` | `int` | closure size minus one |
| `deferred_rows` | `int` | rows whose `destination_binding == "parent-computed"` (§7.5a) |
| `max_entry_candidates` | `*int` | — |
| `total_candidates` | `*int` | `null` when any row is unknown |
| `total_candidates_lower_bound` | `*int` | — |
| `rows_with_unknown_candidates` | `int` | — |
| `collateral_refs` | `[]PlanCollateralRef` | union across rows; never `null` |
| `collateral_bound` | `*string` | — |

Exactly 11 keys, in this order.

`ReparentPlanApproval` mirrors `PlanApproval` exactly:

| JSON key | Type |
| --- | --- |
| `fingerprint` | `*string` |
| `usable` | `bool` |
| `scope` | `string` |
| `supplied` | `bool` |
| `accepted` | `*bool` |
| `covers` | `ReparentPlanApprovalCovers` |

`ReparentPlanApprovalCovers`: `scope` (string), `waived_evaluation_ids`
(`[]string`), `waived_kinds` (**`[]RefusalKind`** — the sync guard domain,
because the only values it ever carries are `limit-per-entry` and
`limit-total`, which are produced by the reused `EvaluatePlanGuard` limit
algebra and are deliberately **not** members of `ReparentRefusalKinds`),
`hard_blockers_waived` (`bool`, MUST always be `false`), `has_work` (`bool`),
`requires_limits` (`bool`), `encoding_safe` (`bool`), `note` (`string`).
Exactly 8 keys, in this order.

`approval.scope` domain: `fresh-execution` / `resume` / `none`.
`covers.requires_limits` is `true` on the fresh route and `false` on the
continue route (§7.12a).

### 7.12 Machine admission predicate — fresh route

For a document whose `route == "fresh"`, automation MUST decide admission with
exactly:

```text
runnable
  && guard.would_refuse == false
  && guard.execute_blocked_by == []
  && refusal.kind == null
  && approval.usable == true
```

- Exit status MUST NOT be used as an admission predicate.
- `runnable` MUST be `false` whenever `blockers` is non-empty.
- `approval.usable` MUST be `false` whenever `summary.has_work == false`.
- `approval.fingerprint` MUST be `null` when `policy.limits_supplied == false`.
  A plan without limits is a readable preview that mints no usable token.
- `approval.scope` is `fresh-execution` when a token is mintable, else `none`.
- Deferred nulls (§7.5a) MUST NOT affect any term of this predicate.

### 7.12a Machine admission predicate — continue route

A `--continue --plan` document describes a run that was already approved. It
carries no new token, and the fresh predicate MUST NOT be applied to it. For a
document whose `route == "continue"`, automation MUST decide admission with
exactly:

```text
runnable
  && guard.would_refuse == false
  && guard.execute_blocked_by == []
  && refusal.kind == null
  && approval.scope == "resume"
```

with these obligations:

- `policy.limits_supplied` reflects the **persisted** limits and
  `policy.limits_origin == "persisted-state"`. The limits are re-published for
  audit; §8.4 forbids re-evaluating them as an admission gate.
- `approval.scope` is `"resume"`.
- `approval.supplied` is `false` and `approval.accepted` is `null`: no token
  was supplied, so nothing was accepted.
- `approval.usable` is `true` **iff** the persisted run is resumable and
  `blockers` is empty; otherwise `false`.
- `approval.fingerprint` is `null`: it always means the fingerprint of the
  document that contains it, and a continue document is not minting an
  approval. `state.approved_fingerprint` exposes the persisted fresh-plan
  fingerprint purely as **audit evidence**, so an operator can confirm which
  plan this run is finishing. It MUST NOT be accepted, re-checked, or reusable:
  passing it back as `--approve-plan` on a `--continue` is refused by §3.5
  rule 4, and passing it to a fresh execution is refused by §8.2 because a
  fresh plan of a half-finished run cannot reproduce it.
- `covers.requires_limits` is `false` and `covers.note` MUST say that a resume
  inherits its gate from the approved fresh run.

A test MUST assert both predicates as tables, including that a bare
`--continue --plan` of a healthy paused run is admissible under §7.12a and
inadmissible under §7.12.

The continue plan is read-only but not stale-state-only: it MUST classify the
live affected refs, live `stack.yaml` hash, owner liveness, compatibility
ownership, and shared lock ownership. It publishes the same blocker an
execution resume would raise and MUST NOT advertise an admissible resume when
execution would refuse. The ref assessment is one shared read-only primitive:
before the commit point it rejects symbolic affected branch refs, classifies
every row as `pre-image` / `planned tip` / `no-op` / `foreign`, and recognizes
the same post-image plus descendant-of-`planned_new_sha` evidence that execution
uses to infer a commit point. Thus the plan neither blocks a recovery execution
would accept nor advertises one execution would refuse. It performs no fetch,
lock reclaim, state repair, marker persistence, or write.

### 7.13 Fingerprint

New file `internal/reparent_plan_fingerprint.go`:

```go
func ReparentPlanFingerprint(plan ReparentPlan) (string, error)
func ReparentPlanFingerprintPreimage(plan ReparentPlan) ([]byte, error)
```

```go
reparentFingerprintPrefix             = "tws-reparent-fp\x00"
reparentFingerprintEncodingVersion    = 0x0001
reparentFingerprintTupleSchemaVersion = 0x0003
```

- Hash: SHA-256, lowercase hex, exactly 64 characters (`^[0-9a-f]{64}$`).
- Framing: the identical TLV scheme used by `rebase_plan_fingerprint.go`
  (2-byte big-endian field id, 1-byte type tag, 4-byte big-endian length,
  payload), wrapped in a length-framed root STRUCT.
- The sync prefix `"tws-plan-fp\x00"` MUST NOT be reused, so every existing
  sync approval token remains valid and no sync token is ever accepted here.

The tuple MUST bind, in this fixed field order:

1. workspace mode; 2. workspace stable id; 3. feature; 4. route;
5. target name; 6. target git branch; 7. target repository identity STRUCT:
   repo token, execution-context id, canonical repository root, source, and
   the raw new-parent `requested_token`.
   The context id is SHA-256 over canonical repository root + NUL + canonical
   common-dir, so both identities are bound without adding a display field;
8. old parent stored token; 9. old parent kind; 10. old parent ref;
11. old parent sha; 12. new parent **stored** token (§4.3b);
13. new parent kind; 14. new parent ref; 15. new parent pinned sha;
16. onto-kind requested; 17. oid width;
18. target cutoff resolved sha; 19. target cutoff provenance;
20. ordered closure from `ReparentClosureOrder` — (name, git branch, order,
    repo token, execution-context id, canonical repository root, source)
    tuples in that exact order;
21. per-row pre-image head sha; 22. per-row cutoff sha, provenance, and raw
    `supplied_token`;
23. per-row `destination_binding`, `destination_parent`, and
    `destination_sha` (the last is an explicit NULL for every
    `parent-computed` row);
24. per-row **argv template** (§7.5b), never the materialized argv;
25. per-row candidate digest and candidate count;
26. metadata delta (name, base_before, base_after, last_base_sha_before,
    last_base_sha_after) tuples, where `last_base_sha_after` is an explicit
    NULL for every descendant; 27. `stack_sha256_before`;
28. fetch policy; 29. guard `max_replay_per_entry`; 30. guard
    `max_replay_total`; 31. validation `command_digest`;
32. execution identity STRUCT: strategy kind, computation context, replay
    backend, ref backend, original checkout branch/head/detached, and holder
    tuples `(canonical path, git branch, HEAD, holder kind, action)` sorted by
    that tuple; 33. approval scope.

Exactly **33** fields. Every deferred value of §7.5a is written as an explicit
NULL member, never omitted and never guessed, so the preimage a `--plan`
produces and the preimage a fresh execution recomputes at admission are
byte-identical by construction. `run_id`, `computation_path`,
`materialized_argv`, and `stack_sha256_after_expected` are **never** bound:
they either do not exist at admission or embed a per-run path.

Fields 3, 7, 12, 15, 18, 20, 21, 22, 23, 24, 26, 27 and 32 are the
load-bearing ones: any destination/cutoff/pre-image change, repository or
common-dir identity change, row execution-context change, ref-backend change,
original checkout identity change, stable holder tuple change, closure-order
change, raw operator-token byte change, or known metadata change MUST change
the fingerprint.

### 7.14 Rendering

New file `internal/reparent_plan_render.go`:

```go
func FormatReparentPlan(plan ReparentPlan) ([]byte, error)
func MarshalReparentPlan(plan ReparentPlan) ([]byte, error)
```

- `MarshalReparentPlan` MUST emit exactly one compact JSON value followed by
  exactly one `\n`, HTML-unescaped, arrays normalized, mirroring
  `MarshalRebasePlan`.
- `FormatReparentPlan` MUST render these sections in this order, with the
  target visually separated from the descendants because only the target's
  configured parent changes:

  ```text
  reparent plan  (schema 1, route <route>)
  feature / workspace / mode
  target
    branch, old parent (requested/stored/kind/ref/sha), cutoff (sha/provenance/ancestry)
    new parent (requested/stored/kind/ref/pinned sha), resolver agreement
    replay: <count> candidate(s), first <short> <subject>, determinacy
  descendants (N)
    <one block per row: branch, cutoff, destination dependency
     ("onto computed tip of <parent>"), replay>
  metadata changes
    <one line per changed cell, old -> new; a descendant-derived deferred cell renders "(computed at replay)">
  strategy and atomicity
  holders
  remote follow-up
  guard
  approval
  ```

- A descendant-derived deferred value (§7.5a) MUST render as the literal
  `(computed at replay)` in the human document and as `null` in JSON. The
  phrase is reserved for parent-computed descendant facts. An unavailable
  target/destination, metadata post-image, run id, or computation path renders
  `unavailable` (and `policy.oid_width` renders `unknown`) in the human
  document and `null` in JSON; it MUST NOT pretend it will be computed during
  replay. A normal plan-only run id/path render `not assigned`.
- The human document and the JSON document MUST go to stdout. The mode header,
  fetch prose, and every refusal MUST go to stderr.
- A byte-exact golden MUST be pinned at
  `internal/cli/testdata/reparent/plan_human.txt` and the help snapshot at
  `internal/cli/testdata/reparent/reparent_help.txt`.

---

## 8. Guard, limits, approval

### 8.1 Reuse

The run MUST reuse `CheckoutPlanGuard`, `PlanGuardBlock`,
`PlanGuardLimits`, `EvaluatePlanGuard`'s limit algebra, and the
`ControlledPathBlocker` vocabulary. `guard.indeterminacy_policy` MUST remain
`"jit-deferred"`.

A reparent-specific evaluator `EvaluateReparentGuard(plan ReparentPlan, g
CheckoutPlanGuard) error` MUST produce `*PlanGuardRefusalError` values so the
CLI prints the identical anchored line:

```text
plan-guard: <kind>: <detail>
plan-guard: <kind>: state-preserved: <detail>
```

### 8.2 Mandatory limits on the fresh execution route

- A fresh execution MUST carry at least one of
  `--max-replay-per-entry` / `--max-replay-total`; §3.5 rule 14 refuses
  otherwise.
- A fresh execution MUST carry `--approve-plan`; §3.5 rule 13 refuses
  otherwise.
- The supplied fingerprint MUST equal the fingerprint of the plan the fresh
  execution recomputes at admission time. Because every deferred value is bound
  as an explicit NULL (§7.5a, §7.13) and the argv template is run-id-free
  (§7.5b), the two preimages are byte-identical whenever nothing observable
  changed. A mismatch refuses `plan-guard: approval-mismatch: ...` and MUST NOT
  mutate anything.
- A plan route MAY omit limits; §7.12 then makes the token unusable.
- A documented/approved workflow MUST put the same replay limit flag(s) and
  values on both preview and execution. A limitless preview has
  `approval.fingerprint = null` and cannot be approved.
- Neither obligation reaches `--continue` or `--abort` (§8.4).

### 8.3 Just-in-time revalidation

Before computing each row, the executor MUST re-probe that row's candidate
count and compare it against the approved row. A divergence refuses
`plan-guard: revalidation-mismatch: state-preserved: ...` at whatever stage it
occurs, and the run remains resumable (`--continue`) or reversible
(`--abort`).

When an exact approved plan exceeded a supplied limit, state persists both
`approval.covers.waived_evaluation_ids` and `waived_kinds`. JIT and resume
honor a limit exceedance only when its exact evaluation ID **and** matching
`limit-per-entry` / `limit-total` kind were persisted and the revalidation
digest is unchanged. A changed digest/count refuses
`revalidation-mismatch`; a newly resolved exceedance whose ID was not waived
refuses the corresponding limit kind.

### 8.4 Continue and abort

- `--continue` and `--abort` MUST NOT require, accept, or reuse an approval
  token, and MUST NOT re-evaluate replay limits as an admission gate. They
  inherit the frozen limits from state purely so that an older release refuses
  to resume the run and so that §7.12a can publish them as audit evidence.
- The persisted limits MUST be written into both the reparent state artifact
  and the mode-appropriate compatibility artifact (§11.2).
- Persisted waiver IDs/kinds are audit and JIT evidence from the already
  approved fresh run; recovery accepts no new waiver token.

### 8.5 Post-lock re-snapshot (concurrency window)

Everything §6.3 steps 3–10 observed was observed **without** the mode lock.
Between the last read and the lock, another process can move a ref, edit
`stack.yaml`, or start an agent session. Immediately after the lock is
acquired (§6.3 step 11) and **before** any pin, scratch worktree, state write,
or compatibility artifact, a fresh execution MUST:

1. re-read `stack.yaml` and re-compute its SHA-256;
2. re-resolve every closure row's branch tip, the pinned destination, and
   every cutoff;
3. re-run the session-liveness probes of §6.2;
4. compare canonical target repository/common-dir identity and every holder's
   canonical path, branch, HEAD and planned action with the approved snapshot;
5. re-build the plan and re-compute the fingerprint;
6. compare that fingerprint against `--approve-plan`.

A mismatch refuses `plan-guard: revalidation-mismatch: state-preserved: ...`,
naming the first field that moved, releases the lock, and leaves nothing
behind. A newly live session refuses `session-live`. This is the single
narrowest window the design can offer without holding the lock across an
operator's reading of the plan, and it is what makes the approved token a
statement about the state the run actually mutates.

The explicit identity comparison is required even where two repositories have
identical refs and therefore produce the same ordinary plan fingerprint. A
symlink retarget, equivalent clone, holder relocation, changed holder HEAD or
changed detach/refuse action is `revalidation-mismatch` before state birth.

A `--continue` performs the live portions as a **verification** against persisted
pre-image values rather than against a token while `resume_stage` is at or
before `refs-committed`: a drifted ref is classified by §11.7 and a drifted
`stack.yaml` refuses `metadata-drift`. At `writing-metadata`,
`metadata-written`, `restoring-holders`, `cleanup`, and `completed`, §11.8a
first determines whether the commit point was reached. After it was reached,
later ref movement is reported informationally and MUST NOT block forward
holder restoration or cleanup; the stack hash is classified by §11.6 rather
than compared only with `stack_sha256_before`.

`--continue --plan` uses the same read-only recovery assessment: current
session liveness, active/unresolved conflict, owner/compat ownership, refs and
metadata. Missing owned compatibility files are automatic repair work and do
not block; `completed` with residue is admissible cleanup work. Pending rows
without `planned_new_sha` are left to their JIT revalidation, so candidate
drift reports `plan-guard: revalidation-mismatch` rather than a premature
`ref-foreign-value`.

---

## 9. Execution model

### 9.1 Strategy

v1 MUST use **detached computation followed by one CAS ref transaction**.
v1 MUST NOT depend on `git replay` in any form: `strategy.uses_git_replay` is
always `false` and the string `replay` MUST NOT appear as a Git subcommand in
any argv this feature emits.

### 9.2 Computation worktree — external mode

- The run MUST create exactly one tool-owned scratch **linked worktree** in
  the target repository (`exec.Cmd.Dir = <repo root>`):

  ```text
  git worktree add --detach <scratch path> <first row pre-image sha>
  ```

- `<scratch path>` MUST be
  `<featurePath>/.reparent/<run-id>/scratch`, recorded in state as
  `scratch_path`. It sits beside, never inside, `<featurePath>/worktrees/`,
  which is the only directory `externalSyncLayout.WorktreePath` derives.
- The scratch worktree MUST NOT be reported by `tws list`, `tws status`,
  `tws doctor`, or any import/adoption scan as a feature worktree. Every
  runtime-artifact filter that enumerates a feature directory MUST skip any
  entry matching `.reparent*`, exactly as it already skips the other
  dot-prefixed runtime artifacts. Any surface that derives worktrees from
  `git worktree list --porcelain` MUST exclude **only** exact active
  `scratch_path` values loaded from authoritative reparent state; filtering
  arbitrary `.reparent*` path components is forbidden because unrelated
  worktrees such as `~/.reparent-lab/...` remain ordinary user worktrees when
  no state records them. Directory import/adoption filtering remains the
  `.reparent*` family rule.
- Failure to create it refuses `scratch-worktree-unavailable`.
- After the cleanliness proof in cleanup step 2 (§11.6a), it MUST be removed
  with `git worktree remove --force <scratch path>` followed by
  `git worktree prune`, and only then.
- Existing external multi-worktree behavior MUST be otherwise unchanged.

### 9.3 Computation worktree — checkout mode

- The run MUST reuse the one physical checkout. It MUST NOT create a second
  physical checkout and MUST NOT call `git worktree add`.
- Before the first computation it MUST record `original_branch`,
  `original_head`, and `original_detached`, then run the §9.4b untracked gate
  and `git switch --detach <first row pre-image sha>`.
- `git symbolic-ref --quiet --short HEAD` exit `1` means detached; any other
  error is `probe-failed`. `HEAD` MUST resolve to a non-empty commit before
  any mutation. Capture and restore never turn an unreadable or empty identity
  into a successful detached checkout.
- When the original checkout is detached, persist
  `original_head_pin_ref = refs/tws/reparent/<run-id>/original-head` and create
  that pin at `original_head` before the initial switch. Recovery verifies or
  recreates the pin under the owned lock. It survives aggressive reflog expiry
  and GC, remains until the detached checkout is restored on success or abort,
  and is removed with the run's other pins only afterward.
- The computation-holder detachment intent MUST persist the **actual switch
  target SHA** separately from `original_head`. Recovery reconciles the
  intent against that target, which may differ when the original branch is an
  affected descendant.
- When `original_detached` is true, an intent observed with HEAD still equal to
  `original_head` is the crash window before the initial switch executed, not
  `holder-unsafe`. `--continue` retries the switch and `--abort` restores the
  already-correct detached HEAD; both are idempotent.
- That initial switch **is** the detachment of the checkout's own holder. The
  run MUST record it at that moment as a holder with
  `holder_kind = "computation-context"`, `action = "already-detached"`, and
  `restored: false`, publish it as `holders.preimage_holder_excluded`, and
  exclude it from the §9.9 still-attached drift check. Without this exclusion
  every checkout-mode run would refuse `holder-unsafe` against the HEAD it
  deliberately detached itself.
- After `metadata-written` it MUST return the checkout to `original_branch`
  with `git switch`, or to `original_head` with `git switch --detach` when
  `original_detached` is true, never with `git reset --hard`. The same explicit
  restoration runs on the abort path (§11.8 step 6).
- If the original branch is itself an affected row, it is restored to its
  **new** tip; that is the intended result and MUST be stated in the success
  output.

### 9.4 Per-row computation

Rows are computed sequentially in `ReparentClosureOrder` (§6.1a). For row `i`:

1. resolve this row's destination (§9.4a);
2. run the just-in-time untracked gate for the tree about to be checked out
   (§9.4b);
3. `git switch --detach <row pre-image sha>` in the computation worktree;
4. run `materialized_argv`, which is `row.argv` (§7.5b) with its single
   placeholder resolved, executed with `exec.Cmd.Dir = <computation worktree>`:

   ```text
   git -c rebase.backend=merge \
       -c rebase.updateRefs=false \
       -c rebase.autoStash=false \
       -c rebase.forkPoint=false \
       -c rebase.rebaseMerges=false \
       rebase --merge --no-fork-point --no-update-refs --no-autostash \
              --no-rebase-merges \
              --onto <destination oid for this row> <row cutoff oid>
   ```

5. on success, the computed new tip is `git rev-parse HEAD`;
6. **pin the computed tip immediately** (§9.8), before anything else;
7. only then run validation (§9.6).

**Backend.** v1 selects the **merge** backend unconditionally and pins it two
ways: `-c rebase.backend=merge` neutralizes any user or repository config, and
the explicit `--merge` operand neutralizes any future change of Git's own
default. `effective_backend` is therefore the constant `"merge"` on every
runnable row, and a mid-run config edit provably cannot change a resumed run's
backend — which is the claim the plan makes. The apply backend is listed in
`strategy.forbidden_operations`.

### 9.4a Per-row destination resolution

| `destination_binding` | Resolution |
| --- | --- |
| `pinned` | the pinned destination OID of §4, already probed by §5.4 |
| `parent-computed` | the computed new tip of `destination_parent` |

A `parent-computed` destination MUST be read **only** from the parent row's
recorded `planned_new_sha` in state, after that row reached row-stage
`validated`, and MUST be confirmed to equal the parent's `new` pin
(`refs/tws/reparent/<run-id>/new/<entry-id>`). It MUST NOT be read from
`refs/heads/<parent branch>`, from `HEAD`, or from any live ref: public refs
still hold pre-image values at this point, and reading one would silently
replay onto the old parent. A parent row that is not yet `validated` is a
programming error and refuses `probe-failed`.

The resolved OID MUST then pass the identical probe §5.4 applies to the
target's destination:

```text
git rev-parse --verify --quiet --end-of-options <oid>^{commit}
```

Failure refuses `destination-unresolvable`.

### 9.4b Untracked overwrite — deferred, just-in-time gate

A pre-mutation scan cannot know which tree an uncomputed row will check out, so
`untracked-overwrite` is evaluated just-in-time, never statically:

- **Preflight (read-only).** The run inventories the untracked, non-ignored
  paths of every consulted worktree and publishes them. A non-empty inventory
  raises warning `untracked-present`; it is never a refusal by itself.
- **Before every `git switch`/`git checkout` this run performs**, and again
  **before every holder re-attachment** (§9.9), the run MUST compare the exact
  destination tree against the live untracked inventory of that working tree
  (`git switch` is invoked without any force option, so Git itself refuses; the
  run additionally pre-computes the collision set with
  `git ls-tree -r --name-only <target-tree>` intersected against
  `git ls-files --others --exclude-standard`). A non-empty intersection refuses
  `untracked-overwrite`, naming every colliding path and the working tree.
- The run MUST NEVER pass `-f`, `--force`, `--discard-changes`, or
  `--ignore-other-worktrees` to escape this gate.

The gate need only precede a mutation of a **user working tree or a public
ref**. It does not gate the run's own internal effects — the reparent state
artifact, compatibility artifacts, pins, and the scratch worktree's own
creation — because none of those can clobber operator content.

### 9.5 Forbidden operations

The executor MUST NOT, at any point, run:

- `git reset --hard` (or `--merge`, `--keep`) against any user branch;
- `git replay` in any form;
- `git rebase --update-refs`, `--autostash`, `--rebase-merges`,
  `--fork-point`, or `--apply`;
- `git push` (v1 has no `--push`);
- `git worktree add` in checkout mode;
- `git checkout -f` / `git switch --force` / `--discard-changes` /
  `--ignore-other-worktrees`.

`rebase.updateRefs=true` in user or repository config MUST be neutralized by
the explicit `-c` overrides of §9.4; a truthy configured value MUST
additionally raise warning `collateral-update-refs-config`. `rebase.backend`
is neutralized the same way (§9.4).

### 9.6 Validation

When `policy.validation.applies` is true, the frozen command MUST be executed
in the computation worktree immediately after each row's computed tip has been
**pinned** (§9.4 step 6), and before the next row is computed. A non-zero exit
refuses `validation-failed`, leaves the run resumable, and MUST NOT move any
public ref.

A validation command MUST leave the computation context clean. After the
command returns, the run MUST re-check tracked status and compare the
non-ignored untracked set against a snapshot taken immediately before the
command. Any tracked modification or any **new** untracked path refuses
`validation-failed`; modifying or retaining a preexisting untracked path is
allowed. The detail names the residue and the fact that the command, not Git,
produced it. The run MUST NOT clean, stash, or reset that residue: doing so
would destroy operator data the command created. State is preserved and the
run stays resumable, so the operator can inspect the worktree, clean it
themselves, and `--continue`.

`policy.validation.requires_clean_context` publishes this obligation and is
always `true`.

### 9.7 Conflicts

A non-zero `git rebase` exit that leaves a rebase in progress MUST:

1. set stage `conflict-paused`, persist the partially computed rows, and
   preserve every pin created so far;
2. print on stderr, verbatim:

   ```text
   reparent paused: conflict while computing <name> (<git branch>)
   resolve in: <computation worktree path>
   then run: git -C <computation worktree path> rebase --continue
   then run: tws stack reparent <feature> --continue
   to discard the whole reparent: tws stack reparent <feature> --abort
   ```

3. exit 1.

The `git -C <path>` in that text is **operator guidance**, not a command this
run executes; the executor itself never uses `-C` (§7.5b). A conflict pause is
not a refusal: it carries no `reparent:` marker, adds no `failure_kind`, and is
excluded from §13.2's one-refusal-line rule.

`tws stack reparent <feature> --continue` MUST refuse
`conflict-unresolved` when a rebase is still in progress in the computation
worktree, naming the same native command. It MUST NOT run
`git rebase --continue` on the operator's behalf.

At the conflict pause the run persists completion context (including a reflog
anchor when available). After the rebase directory disappears, proof is
version-independent: the destination must be an ancestor of live `HEAD`, and
for a non-empty replay `HEAD` MUST differ from `preimage_sha`. Successful
completion persists the proven HEAD; reflog movement may be recorded as
additional evidence but exact version-specific reflog subjects are never an
admission requirement. This rejects native `rebase --abort` even when the
destination was already an ancestor of the old parent.

`--abort` from `conflict-paused` MUST run `git rebase --abort` in the
computation worktree before restoring anything else.

A non-zero rebase exit that leaves **no** rebase in progress is a native Git
failure, not a conflict and not a `ReparentRefusalKind`. The run MUST persist
`stage: failed`, `resume_stage: computing`, `failure_domain: native-git`, and
a sanitized `failure_detail`; the current row remains `pending`, all pins and
artifacts remain, the captured native error is printed without a marker, and
the operator may fix the cause and run `--continue` or choose `--abort`.

### 9.8 Pins

Pin ref namespace, created with `git update-ref`:

```text
refs/tws/reparent/<run-id>/dest
refs/tws/reparent/<run-id>/old/<entry-id>
refs/tws/reparent/<run-id>/new/<entry-id>
```

- `<run-id>` MUST be 32 lowercase hex characters from `crypto/rand`.
- `<entry-id>` MUST be produced by a new exported helper

  ```go
  func ReparentEntryRefID(feature, name string) string
  ```

  built on the existing `hashedSessionID` pattern: a sanitized prefix plus
  `_` plus 8 hex characters of `sha256(feature + "/" + name)`. Sanitization
  MUST reduce the prefix to `[A-Za-z0-9._-]`, MUST NOT emit a leading `-` or
  `.`, MUST NOT emit `..`, `@{`, or a trailing `.lock`, and MUST keep the
  whole component at most 64 bytes. The raw `StackEntry.Name` MUST NOT be used
  as a ref path component: names such as `a` and `a/b` would otherwise collide
  as a ref directory and file.
- Old pins and the destination pin MUST be written at stage
  `pinning-preimages`, before the first computation.
- A row's **new pin MUST be created immediately after that row's
  `git rev-parse HEAD`** (§9.4 step 6) and **before** that row's validation and
  before any later row is computed. Validation can run for minutes and can
  itself invoke `git gc`; a computed tip that exists only as a detached HEAD in
  a scratch worktree is reachable, but a conflict, a crash, or a subsequent
  `switch --detach` during a long run can strand it. Pinning first makes the
  object durable at the instant it exists.
- Stage `pinning-computed` is therefore an **idempotent verify-all gate**, not
  the first creation of anything: on entry it confirms that every row's new pin
  exists and resolves to that row's recorded `planned_new_sha`, re-creating any
  pin that is missing and refusing `probe-failed` on any pin that resolves to a
  different object.
- All pins MUST survive until stage `cleanup`, i.e. until refs, metadata, and
  holder restoration are all complete, or until an `--abort` finishes. A
  conflict pause, a failed validation, and a crash all leave every pin created
  so far in place.

### 9.9 Holder detachment

Immediately before the CAS transaction (stage `detaching-holders`), for every
affected branch that is held by a worktree (linked or primary):

1. verify the holder's HEAD still equals the row pre-image SHA; a mismatch
   refuses `holder-unsafe` and MUST NOT enter the transaction;
2. run the §9.4b untracked gate for that working tree;
3. `git switch --detach <row pre-image sha>`, with `exec.Cmd.Dir` set to the
   holder path;
4. record the holder in state as `detached_holders[]` with path, branch,
   pre-image SHA, and `restored: false`.

**Checkout mode's own pre-image holder is excluded from step 1.** In checkout
mode the single physical checkout *is* the computation worktree, and §9.3
already detached it — at the very start of the run, at the first row's
pre-image SHA — long before this stage. Re-checking it here would compare a
deliberately detached HEAD against an attached-branch expectation and refuse
`holder-unsafe` on every single checkout-mode run. It is recorded once, at the
initial switch, as a holder with `holder_kind = "computation-context"` and
`action = "already-detached"`, published as
`holders.preimage_holder_excluded`, and skipped by the drift check. Every
*other* holder — including, in external mode, every linked worktree that is
still attached — is checked and detached here.

After `writing-metadata` (stage `restoring-holders`), each recorded holder MUST
be re-attached with `git switch <branch>` (`exec.Cmd.Dir` = holder path), after
its §9.4b untracked gate. The working tree is then updated by Git itself; no
reset is performed. In checkout mode the run additionally restores the
computation context explicitly: `git switch <original_branch>`, or
`git switch --detach <original_head>` when `original_detached` is true.

A holder that cannot be re-attached — because it acquired tracked
modifications while detached, or because its §9.4b gate refuses — MUST NOT be
forced. The run records `restored: false` with a reason, raises the **warning**
`holder-restore-deferred` (never the refusal kind `holder-unsafe`: the commit
point has passed, §11.8a), names the exact `git switch` the operator should
run, and still reports overall success because refs and metadata are already
consistent. Every such warning MUST also be persisted in state, so a later
`--continue` re-attempts exactly the unrestored holders and reports the same
list.

### 9.10 Capability probes

- The existing six-gate `GitCapabilities` table MUST NOT be extended.
- **Required minimum.** Before any mutation, the run MUST require the existing
  `GitCapabilities.CapRebaseUpdateRefs` gate (Git >= 2.38). That single gate is
  a sufficient floor for every core requirement of this feature — the
  `update-ref --stdin` `start`/`prepare`/`commit` transaction verbs (2.27),
  `--end-of-options` (2.24), `--no-rebase-merges` (2.34), `--disambiguate`
  (long-standing), `git switch` (2.23), and the merge backend default (2.26)
  are all older — so no new version gate is invented for them. A false or
  unknown `CapRebaseUpdateRefs` refuses `capability-unsupported`, naming 2.38
  and the observed `git --version` line.
- A new, separate type in `internal/git_capability.go`:

  ```go
  type ReparentGitCapabilities struct {
      CapForceIfIncludes bool // git push --force-if-includes, Git >= 2.30
      CapRefBackendKnown bool // `git rev-parse --show-ref-format` available, Git >= 2.45
  }
  func ReparentGitCapabilitiesForVersion(v GitVersion) ReparentGitCapabilities
  ```

  These are **separate facts**, not part of the required minimum:
  `CapForceIfIncludes` gates only rule R-PUSH (§12.4) and `CapRefBackendKnown`
  gates only the ref-backend probe.
- The ref backend MUST be probed with
  `git rev-parse --show-ref-format` when `CapRefBackendKnown` is true, else
  with `git config --get extensions.refStorage`, else reported as `unknown`.
  An `unknown` backend is **permitted** and MUST NOT refuse: it is treated as
  `files` throughout — conservative crash recovery, row-by-row reconciliation,
  and warning `files-backend-not-crash-atomic` (§7.7).
- An unparseable Git version yields all-false capabilities; that is "unknown",
  never "supported". Where a capability is **required** and unknown, the run
  refuses `capability-unsupported`; where it merely selects a strategy, the
  conservative branch is taken.

### 9.11 The CAS ref transaction

After every row has computed, validated, and been pinned, after the pending
remote follow-up record has been written (§12.3a), and after holders are
detached, the run MUST perform exactly one transaction, with
`exec.Cmd.Dir = <repo root>`:

```text
git update-ref --no-deref -m "tws reparent <feature> <target> <run-id>" --stdin
start
update refs/heads/<row 1 branch> <new 1> <old 1>
verify refs/heads/<no-op row branch> <old value>
update refs/heads/<row 3 branch> <new 3> <old 3>
...
prepare
commit
```

- Rows appear in `ReparentClosureOrder` (§6.1a).
- Every `update` MUST carry the explicit expected old value. A stale expected
  value aborts the whole transaction during `prepare`; no public ref has moved.
  Before refusing `ref-transaction-mismatch`, the run MUST attempt to
  re-attach every holder it detached for this transaction. If all restores
  succeed, the refusal carries `state-preserved`. If a restore cannot be
  completed without force, the same single refusal detail MUST name each
  detached holder and its manual `git switch` command; state and pins remain
  for `--continue` or `--abort`.
- A row whose `planned_new_sha` **equals** its `preimage_sha` is a **no-op
  row**: it MUST emit exactly
  `verify refs/heads/<branch> <preimage_sha>` in the transaction, be recorded
  with `cas_rows[].applied = true` and `noop = true`, and be classified by
  §11.7 using the **live** ref. It MUST NOT emit `update <ref> X X`: `verify`
  preserves race-atomic coverage without pretending a ref moved.
- The command-level `--no-deref` flag is REQUIRED so a direct affected ref
  swapped to a symbolic ref after preflight cannot redirect the transaction to
  an out-of-closure target. The run MUST NOT emit `create`, `delete`, or
  stdin `option no-deref` lines, and
  MUST NOT include any ref outside the affected closure. In the **forward**
  transaction, `verify` is permitted and required only for no-op rows;
  §11.8 step 4 separately requires it for unchanged rows in the rollback
  transaction.
- Collateral refs are never moved: nothing other than the listed
  `refs/heads/<row branch>` entries appears in the transaction.
- The child's **stdout and stderr MUST both be captured into the run's own
  buffers and MUST NOT be inherited**. `update-ref --stdin` is quiet on
  success, but a hook may print, and this process's stdout is reserved for the
  plan document alone (§3.6). Captured output is discarded on success and
  quoted in the refusal detail on failure.
- If every row is a no-op row, the run still issues
  `start` / one ordered `verify` per row / `prepare` / `commit`; it MUST NOT
  skip the transaction, because the verification is the race barrier for the
  whole closure.

### 9.12 Metadata write

The post-image is built **before** the CAS, not after, so a crash between the
CAS and the write can always be completed forward from durable bytes:

1. At stage `building-post-image` — after every row has computed and validated,
   before `pinning-computed` completes — build the post-image `Stack` per §10,
   marshal it to its exact bytes, compute their SHA-256, and persist **both**
   `stack_after_base64` and `stack_sha256_after_expected` into the state
   artifact with the durable writer (§10.3a). Set
   `metadata_delta.post_image_known = true` in state.
2. At stage `writing-metadata`, exactly once, immediately after
   `refs-committed`:
   - **re-read the live `stack.yaml` and require its SHA-256 to equal
     `stack_sha256_before`**. Any other value refuses `metadata-drift` and
     writes nothing: someone edited the file while this run held the lock, and
     overwriting their edit is never correct. This check MUST precede **every**
     forward metadata write, including one performed by `--continue`;
   - write the persisted `stack_after_base64` bytes verbatim with
     `WriteStackBytesAtomic` (§10.3). The bytes are never recomputed from live
     refs at this point;
   - if that write fails after refs are committed, persist `stage: failed`,
     `resume_stage: writing-metadata`, and
     `failure_kind: metadata-pending`, then refuse
     `reparent: metadata-pending: state-preserved: <detail>`; the next
     `--continue` retries the same persisted bytes;
   - record `stack_sha256_after` in state and set stage `metadata-written`.

Stage `metadata-written` is the exact spelling used by §11.3, §11.6, and
§11.9; no other spelling of it exists in this spec.

### 9.13 Atomicity statement (normative wording)

The implementation, the plan, the success output, and the documentation MUST
state exactly this and MUST NOT claim more:

- The CAS transaction is **race-atomic** on the files and reftable backends and
  on an unprobed backend: a concurrent change to any expected old value aborts
  every update during `prepare`.
- The CAS transaction is **crash-atomic only on reftable**. On the files
  backend — and on an `unknown` backend, which is treated as files — a process
  or machine crash during `commit` can leave a subset of the affected refs
  moved.
- Refs, `stack.yaml`, and worktree/index state are three separate effects and
  are never one indivisible operation.
- The run has exactly one **commit point** (§11.8a): the instant the exact
  post-image bytes are durably present in `stack.yaml` **and** every affected
  ref classifies as `planned tip` or `no-op`. Before that conjunction holds,
  `--abort` rolls back; after it, the operation is logically committed and
  `--abort` becomes forward completion only.
- Recovery therefore classifies every affected ref individually (§11.7) rather
  than assuming the commit was all-or-nothing, and continues forward.

A plan whose `atomicity.ref_backend` is `files` **or** `unknown` MUST raise
warning `files-backend-not-crash-atomic`.

---

## 10. Metadata contract

### 10.1 Exact post-state

A strict current same-parent no-work plan has **no post-state mutation**. Every
metadata row MUST publish `base_before == base_after`,
`last_base_sha_before == last_base_sha_after`,
`last_base_sha_after_source == "unchanged"`, and `changed == false`, including
descendants. It publishes no deferred descendant update, no expected
post-image hash, and `post_image_known == false`; the human document renders
`stack.yaml after: unchanged`.

| Entry | `Base` after | `LastBaseSHA` after |
| --- | --- | --- |
| target | the **stored token** (§4.3b, §10.2) | the pinned destination OID |
| descendant in closure | unchanged | the post-replay tip of its configured parent row |
| any entry outside the closure | unchanged | unchanged |

- No other `StackEntry` field is written. `Name`, `Branch`, `Archived`, and
  `Repo` MUST be preserved byte-identically, as MUST the order of
  `Stack.Branches`.
- After a successful run, `tws stack status <feature>` MUST report every
  affected edge as `current`.

### 10.2 Stored token

- `kind == stack-entry` → the parent's `StackEntry.Name`, exactly.
- `kind == literal-ref` → the canonical form of §4.3b: the **full resolved
  ref** (`refs/heads/main`, `refs/tags/v1`, `refs/remotes/origin/main`,
  `refs/<other>`) or the **full canonical lowercase OID**. The short token the
  operator typed is never stored, because a short token is exactly the thing
  that can start resolving to something else tomorrow. `ResolveSyncBase`'s
  `origin/<default>` rewrite MUST NOT be applied, and §4.3c proves it did not
  need to be.
- `Base` is never written empty (§4.4).

### 10.3 Byte-exact atomic writers

New functions in `internal/stack.go`:

```go
// WriteStackBytesAtomic writes exactly these bytes to stack.yaml, durably.
func WriteStackBytesAtomic(featurePath string, data []byte) error

// SaveStackAtomic marshals s and delegates to WriteStackBytesAtomic.
func SaveStackAtomic(featurePath string, s Stack) error
```

- `WriteStackBytesAtomic` is the primitive. Both the forward metadata write
  (§9.12) and the abort restore (§11.8) pass **exact captured bytes** through
  it: a pre-image restored by re-marshalling a decoded struct is not a
  restore, because YAML round-tripping can reorder keys, drop comments, and
  change quoting.
- `SaveStackAtomic` MUST marshal exactly as `SaveStack` does.
- The reparent boundary MUST use these two functions exclusively.
- `SaveStack` MUST remain behaviorally unchanged and MUST keep all of its
  existing callers; converting the rest of the codebase is owned by
  `sync-transactional-abort` and is out of scope here.

### 10.3a Durable write helper

New unexported helper in `internal/stack.go`:

```go
func durableWriteFile(path string, data []byte, mode os.FileMode) error
```

It MUST: write a temp file in the destination directory; `Sync()` that file;
close it; `Rename` it over the destination; then **open the parent directory
and `Sync()` it**. The existing `atomicWriteFile` (`internal/checkout_sync.go`)
already fsyncs the temp file but does not fsync the parent directory after the
rename, so the renamed directory entry is not covered by this feature's
machine-crash durability claim. Every artifact whose loss would strip this
run's only record of what it did MUST use `durableWriteFile`:

- `stack.yaml`, through `WriteStackBytesAtomic`;
- the reparent state artifact (§11.1);
- the mode-appropriate compatibility artifacts (§11.2);
- the remote follow-up record (§12.3).

`atomicWriteFile` MUST remain behaviorally unchanged and MUST keep all of its
existing callers; `durableWriteFile` is added beside it, not inside it.
`durableWriteFile` MUST honour the fault-injection seam of §10.3b.

### 10.3b Fault injection

The existing `syncIOFault` seam (`internal/syncstate.go`) MUST gain four
tokens, appended to its existing list and never reordered:

```text
write-stack              // WriteStackBytesAtomic
write-reparent-state     // the §11.1 artifact
write-reparent-compat    // the §11.2 compatibility artifacts
write-reparent-remote    // the §12.3 record
```

Each token MUST be able to fail **before** the rename and **after** the rename
but before the directory fsync, so T-044 can assert that the previous file
survives the first and that recovery survives the second. When recovery sees
the expected bytes from a post-rename failure but no durable success marker,
it MUST fsync the parent directory (or rewrite through the durable writer)
before recording success, the commit point, or cleanup. This applies to a
visible forward post-image, a visible abort pre-image, authoritative state,
and complete external or checkout compatibility artifacts.

### 10.4 No new persistent field

v1 MUST NOT add any field to `StackEntry` and MUST NOT add any key to
`stack.yaml`. The old-base/`LastBaseSHA` association that is temporarily
needed during the transaction lives only in the plan document and the reparent
state artifact, both of which record `base_before`, `last_base_sha_before`,
`base_after`, and `last_base_sha_after` per row.

### 10.5 Write point and ordering

- The post-image bytes MUST be built and persisted **before** the CAS, at stage
  `building-post-image` (§9.12 step 1).
- `stack.yaml` MUST be written exactly once per run, after `refs-committed`.
- The run MUST NOT write `stack.yaml` per row.
- Every forward metadata write, by a fresh run or by `--continue`, MUST first
  confirm that the live file still hashes to `stack_sha256_before`; any other
  value refuses `metadata-drift` (§9.12 step 2).
- A resumed run that finds stage `refs-committed` MUST write the persisted
  `stack_after_base64` bytes forward, never recompute them from live refs.

---

## 11. Recovery, state, and mutual exclusion

### 11.1 Artifact

New file `internal/reparent_state.go`.

```go
const ReparentStateVersion = 1
```

| Mode | Reparent state path | File mode |
| --- | --- | --- |
| external | `<featurePath>/.reparent-state.v1.yaml` | `0600` |
| checkout | `<metadataRoot>/state/<feature>-reparent.v1.yaml` | `0600` |

Both MUST be written with `durableWriteFile` (§10.3a). A state artifact
whose `state_version` is greater than `ReparentStateVersion` refuses
`reparent-state-unsupported`; one that fails to decode refuses
`reparent-state-corrupt`; one whose `feature` or workspace identity does not
match refuses `reparent-state-foreign`.

The reparent state artifact is **authoritative** for every fact about the run.
The compatibility artifacts of §11.2 exist only to make other binaries fail
closed; no reparent decision is ever read out of them.

### 11.2 Compatibility artifacts and locks (bounded downgrade contract)

A reparent run MUST additionally hold the existing mode-appropriate sync
exclusion and MUST write artifacts that make shipped v1.2.16 **same-feature
sync** plain/continue/abort fail closed once the complete envelope exists.
These artifacts do not make released binaries aware of new global locks or
new commands.

**External**

1. acquire `<featurePath>/.sync-run.lock` through `ClaimSyncRunGuard`
   (`internal/sync_run_state.go`), released with `ReleaseSyncRunGuard` and
   reclaimed, when stale, with `ReclaimSyncRunGuard`;
2. write `<featurePath>/.sync-state.v2.yaml` **first**, as a raw YAML document
   with `state_version: 4`, `route: "reparent"`, `feature`, `stage: "rebasing"`,
   the frozen `max_replay_per_entry` / `max_replay_total`, and `selected` set
   to the closure names;
3. write the legacy sentinel `<featurePath>/.sync-state.yaml` **second**.

The v4 payload MUST have mode `0600`; the legacy sentinel MUST retain its
shipped mode `0644`. Both writes use `durableWriteFile`.

The order matters. A shipped `tws sync` classifies state by inspecting both
files; writing the v2 payload first guarantees that at no instant does a
concurrent old binary observe a lone legacy sentinel and route itself into a
legacy-resume cell. With the payload already present, every shipped classifier
lands only in a cell whose outcome is a refusal.

The v4 payload MUST be produced by a **reparent-owned raw YAML writer**:

```go
func writeReparentCompatSyncPayload(featurePath string, p ReparentCompatSyncPayload) error
```

`SaveSyncRunState` MUST NOT be used and MUST NOT be modified: it rejects any
`state_version` other than 2 or 3 by design (`internal/sync_run_state.go`), and
relaxing that would weaken sync's own birth-decision invariant. `LoadSyncRunState`
in every shipped release accepts only 2 and 3, so an old binary fails closed
with its own sentence, `unsupported scoped sync state version 4`.

The legacy sentinel MUST likewise be serialized and written directly by the
reparent-owned compatibility writer. `SaveGuardedLegacySentinel` MUST NOT be
used: it emits the guarded **legacy-sync** shape through `atomicWriteFile`,
whereas this boundary needs reparent identity and the directory-fsync
guarantee of `durableWriteFile`. Its existing compare-and-swap checks only the
legacy sentinel path and does not reject a v4 payload beside it; that is not
the reason for the new writer. The new writer must reproduce the shipped
sentinel marker shape exactly, add the reparent identity required here, use
the run's marker/token, and write it through `durableWriteFile(..., 0644)`.

**Checkout**

1. acquire a workspace-global checkout mutation lock under
   `<metadataRoot>/state/`, shared with checkout sync across **all features**;
2. acquire the existing feature compatibility lock
   `CheckoutLockPath(featurePath)` through `AcquireCheckoutLock`;
3. write `CheckoutTransactionPath(featurePath)` as a **deliberately
   undecodable** compatibility transaction.

Fresh, `--continue`, and `--abort` take locks in that order. Recovery reclaims
the global lock only when its token, feature, operation, and authoritative
state path match; a live or foreign lock refuses. The feature lock and
compatibility transaction remain required for v1.2.16 same-feature sync
fail-closed behavior. Released binaries do not read the workspace-global lock,
so an unrelated-feature checkout command in an old binary is not excluded.

Fresh global-lock orphan classification probes the recorded owner state with
`Lstat`, never by following a symlink. Only `ENOENT` proves absence. A present
file, symlink, permission/I/O error, or any other unverifiable result refuses
without removing or rewriting the lock.

An absent workspace-global lock is not proof that the physical checkout is
free: before a fresh acquisition may proceed, the new reservation scans the
workspace state directory for every `*-checkout-sync.yaml` and
`*-reparent.v1.yaml` across all features. Any present or unverifiable record
refuses and the just-created reservation is removed by token ownership.
Recovery may reconstruct an absent global lock only while the scan contains
its exact authoritative state and, for reparent, its same-feature compatibility
transaction; any other feature's recoverable state refuses.

The feature lock contains only a PID. Recovery can therefore crash after
rewriting that lock to the recovering PID but before persisting the same PID
as authoritative `owner_pid`. A later recovery MUST treat that dead,
transferred feature lock as this run's only when the simultaneously present
workspace-global lock has the same PID and its token, feature, operation, and
authoritative state path exactly match this run. This proof is sufficient
despite the stale persisted PID; a live transferred PID, mismatched token or
identity, unreadable global lock, or any other feature lock remains foreign
and MUST NOT be removed or rewritten.

An integer `state_version: 4` is **unsafe** in checkout mode. `ContinueCheckoutSync`
does check `tx.StateVersion > CheckoutTransactionGuardedVersion`, but the
shipped `AbortCheckoutSync` (`internal/checkout_sync.go`) performs **no version
check at all**: it loads the transaction, force-acquires the lock, runs
`git rebase --abort`, calls `restoreOriginal`, and deletes state. An old
`tws sync <feature> --abort` would therefore happily "roll back" a reparent run
it does not understand, moving the checkout and deleting the artifacts this
feature depends on.

The compatibility transaction MUST therefore be written so that
`yaml.Unmarshal` into the shipped `CheckoutTransaction` **fails**:

```yaml
state_version: "4-reparent"    # a string where every shipped binary declares an int
route: reparent
feature: <feature>
reparent_run_id: <run-id>
reparent_marker: tws-reparent-compat-v1
max_replay_per_entry: <n>
max_replay_total: <n>
```

The compatibility transaction MUST have mode `0600` and use
`durableWriteFile`.

Consequences, all verified against shipped code:

- `HasCheckoutTransaction` is a bare `os.Stat` and still returns true, so plain
  `tws sync` refuses on an existing transaction exactly as today.
- `LoadCheckoutTransaction` returns a YAML type error, so **both**
  `ContinueCheckoutSync` and `AbortCheckoutSync` return their own
  `no transaction to continue: …` / `no transaction to abort: …` **before**
  taking the lock, before `git rebase --abort`, before `restoreOriginal`, and
  before deleting anything.
- New reparent code MUST NOT parse this file as a `CheckoutTransaction`. It
  detects its own run by reading the raw YAML and matching
  `reparent_marker: tws-reparent-compat-v1`.
- The real `original_branch` / `original_head` live **only** in the detailed
  reparent state artifact (§11.5). They are deliberately absent here, so no
  other binary can act on them.

`SyncRunStateVersion`, `SyncRunStateGuardedVersion`,
`CheckoutTransactionVersion`, `CheckoutTransactionGuardedVersion`, the
`CheckoutTransaction` struct, `AbortCheckoutSync`, and the existing state/lock
path helpers MUST NOT change.

**Mutual exclusion matrix (closed)**

| Existing state | `tws sync` | `tws stack reparent` |
| --- | --- | --- |
| none | runs | runs |
| sync payload/sentinel/lock/transaction | existing recovery semantics; every mutating external route must own the guard and an unreadable/live/foreign guard fails closed | refuse `sync-state-present` |
| reparent state + its compatibility artifacts | refuse with the reparent-aware message below | `--continue` / `--abort` only |
| reparent state, compatibility artifacts missing | refuse with the reparent-aware message below | fresh refuses `reparent-state-present`; `--plan --continue` reports automatic repair and remains admissible; `--continue` reconstructs owned files; `--abort` proceeds without reconstruction |

The authoritative reparent artifact is classified **before** the generic
sync-state/lock refusal. Compatibility artifacts whose marker and run id match
that artifact are this run's own exclusion envelope and MUST NOT cause its
`--continue` or `--abort` to refuse `sync-state-present`; a mismatched
compatibility artifact remains foreign sync state and does refuse. This
precedence is identical in both modes.

`tws sync` MUST gain exactly one new pre-check, which MUST run **after**
`GuardFeatureName` and the shared feature-path/layout resolver have produced a
safe path, but **before** mode dispatch in both modes — that is, before
`runCheckoutSync` and before `classifySyncState`. A resolution failure
propagates; it MUST NOT silently skip the check. This ordering ensures that
the reparent-aware message, not a version or decode error, is what the
operator sees:

```text
a stack reparent is in progress for "<feature>" (run <run-id>, stage <stage>); finish it with: tws stack reparent <feature> --continue
```

It fires only while a reparent state artifact exists for the feature in the
current mode, and it MUST NOT alter any existing sync golden. Because the check
precedes both plan dispatch and classification, it covers `tws sync --plan`,
plain sync, `tws sync --continue`, and `tws sync --abort`; those routes would
otherwise describe or reach the v4/undecodable artifacts and emit a confusing
lower-level result.

Every **mutating external sync** route also participates in the other half of
the handshake. Fresh legacy/no-flag, fresh scoped, guarded legacy/scoped,
`--continue`, and mutating `--abort` MUST claim or reclaim the same
feature-scoped `SyncRunGuard` used by external reparent, then immediately
recheck the authoritative reparent artifact. The guard remains owned through
all rebase and metadata work and through any optional push, remote follow-up
clear, and persisted sync teardown; it is released last. A legacy/no-flag run
uses a transient guard and leaves no new persistent bytes or output after
completion. A guarded legacy/scoped route MUST NOT clear its sentinel, payload,
or guard before its optional push. Thus whichever actor publishes the shared
guard first excludes the other, and no reparent can enter between sync
completion and a later push.

An owned missing compatibility envelope is an observable recovery condition,
not a fatal blocker. The authoritative artifact is sufficient for a
read-only continuation plan and for `--abort`; only `--continue` may
reconstruct the missing files under §11.2a. The
`compat-artifact-missing` vocabulary remains reserved for a missing envelope
whose ownership or reconstruction cannot be proven; ordinary owned absence
MUST NOT emit it.

### 11.2a Recovering from missing compatibility artifacts

A `--continue` MAY re-create the missing compatibility artifacts, but only
after it has proven the run is still the run the state describes:

1. the state artifact decodes, its version is supported, and its feature and
   workspace identity match;
2. its `owner_pid` is not live, or is this process;
3. before the commit point, every closure row's live ref equals that row's
   `preimage_sha` or its `planned_new_sha` (§11.7 classification; a single
   `foreign` row refuses `ref-foreign-value` and re-creates nothing). If the
   monotonic `commit_point_reached` marker is true, later ref movement is
   informational and compatibility artifacts need not be recreated merely to
   perform forward cleanup;
4. the live `stack.yaml` hashes to `stack_sha256_before`,
   `stack_sha256_after_expected`, or `stack_sha256_after`; anything else
   refuses `metadata-drift`.

Only then does it re-write the artifacts in §11.2 order and resume. An
`--abort` needs no re-creation: it can clean up and roll back entirely from the
detailed state artifact, and it MUST do so.

### 11.3 Stages (closed, ordered)

```text
initializing
preflight
pinning-preimages
computing
conflict-paused
building-post-image
pinning-computed
writing-remote-record
detaching-holders
committing-refs
refs-committed
writing-metadata
metadata-written
restoring-holders
cleanup
completed
aborting
aborted
failed
```

Exactly **19** stages.

- `fetching` and `planning` are **not** stages. Both happen on the plan side of
  §6.3, before the lock and before the artifact exists (§3.4), so no artifact
  can ever be found at either. Declaring them would create four unreachable
  rows in the resume table and two unreachable crash windows.
- `conflict-paused` is reachable only from `computing` and returns to
  `computing`.
- `building-post-image` produces and persists the exact post-image bytes
  (§9.12 step 1); it is the stage that makes a post-CAS crash completable.
- `writing-remote-record` writes the pending follow-up record (§12.3a) after
  the new tips are pinned and before any public ref moves.
- `metadata-written` is a real stage, not a flag. Reaching it through the
  normal forward path establishes the **commit-point conjunction** of §11.8a;
  recovery still proves both halves from disk rather than trusting the stage.
- `aborting` is entered by `--abort` before its first rollback action;
  `aborted` is terminal.
- `failed` is terminal-for-this-attempt and always carries `resume_stage`,
  `failure_domain`, and `failure_kind` (§11.5).

### 11.4 Capture / claim / artifact / state order

Before the first mutation, in exactly this order:

1. resolve workspace, mode, feature path, repository root;
2. run §6.3 steps 1–10 (pure and read-only only);
3. require authoritative reparent state absent, acquire the mode lock
   (§11.2), then require authoritative state absent again before any remote
   clear, plan revalidation, or write. A state artifact from crash window 1 is
   recoverable ownership even when its guard is dead and compatibility
   artifacts are absent; a fresh run MUST NOT reclaim that guard or overwrite
   the state/remote pre-image;
4. run the post-lock re-snapshot and fingerprint re-comparison (§8.5);
5. capture the pre-image: every closure row's branch SHA, the **exact
   `stack.yaml` bytes** plus their SHA-256, every holder path/branch/HEAD, the
   original branch and HEAD and whether it was already detached, the ref
   backend, and the raw frozen validation command with its source and digest;
6. write the reparent state artifact at stage `initializing` with
   `durableWriteFile`;
7. write the compatibility artifacts in §11.2 order;
8. re-read the state artifact and confirm it decodes at the expected version;
9. advance to `preflight` and only then mutate.

If the process dies between 6 and 7, the artifact exists at `initializing` with
nothing mutated; `--abort` MUST simply remove the artifact and any pins, and
`--continue` MAY complete step 7 under §11.2a.

### 11.5 State payload (minimum required fields)

```text
state_version              int
run_id                     string (32 hex)
created_at, updated_at     RFC3339 UTC
workspace_mode             external|checkout
workspace_stable_id        string
feature                    string
repo_root                  string
repo_common_dir            string
ref_backend                files|reftable|unknown
oid_width                  int (40|64)
strategy                   detached-scratch-cas
backend                    merge
stage                      one of §11.3
resume_stage               one of §11.3 (the stage a --continue re-enters)
failure_domain             reparent|plan-guard|native-git|io (omitempty)
failure_kind               string (omitempty)
failure_detail             string (omitempty)
owner_pid                  int
owner_token                string

target_name                string
target_git_branch          string
old_parent_requested_token/stored_token/kind/ref/sha
new_parent_requested_token/stored_token/kind/ref/sha
destination_pin_ref        string
original_head_pin_ref      string (omitempty) # checkout, originally detached
cutoff_supplied_token      string (omitempty)

rows[]:
  order, name, git_branch, repo, role
  preimage_sha
  cutoff_sha, cutoff_provenance
  destination_binding        pinned|parent-computed
  destination_parent         string (omitempty)
  destination_sha            (empty until resolved for a parent-computed row)
  planned_new_sha            (empty until computed)
  noop                       bool (planned_new_sha == preimage_sha)
  base_before, base_after
  last_base_sha_before, last_base_sha_after
  argv[]                     the canonical template (§7.5b)
  materialized_argv[]        the argv actually executed
  effective_backend          merge
  candidate_count, candidate_digest
  old_pin_ref, new_pin_ref
  stage                      pending|computed|pinned|validated|committed

validation_command_raw     string (omitempty)   # 0600 artifact only, never a plan field
validation_source          config-repo|config-workspace|none
validation_command_digest  string (omitempty)

scratch_path               string (external only)
original_branch            string
original_head              string
original_detached          bool
preimage_holder_excluded   string (omitempty)   # checkout computation context
detached_holders[]:        path, git_branch, preimage_sha, restored bool, restore_reason string

stack_before_base64        string   # exact pre-image bytes
stack_after_base64         string   # exact post-image bytes, set at building-post-image
stack_sha256_before        string
stack_sha256_after_expected string  # set at building-post-image
stack_sha256_after         string (omitempty)
commit_point_reached       bool     # monotonic; once true it never becomes false

cas_rows[]:                ref, old_value, new_value, applied bool, noop bool
abort_rows[]:              ref, restored bool, classification, detail
remote_record_written      bool
remote_followup_entries[]  string
remote_record_before_captured bool
remote_record_before_present  bool
remote_record_before_base64   string (omitempty)
remote_record_restore_pending bool
remote_record_restore_source_captured bool
remote_record_restore_source_present  bool
remote_record_restore_source_base64   string (omitempty)

max_replay_per_entry       *int
max_replay_total           *int
approved_fingerprint       string
waived_evaluation_ids[]    string
waived_kinds[]             limit-per-entry|limit-total
fetch_policy               fetch|no-fetch
```

`stack_before_base64` and `stack_after_base64` carry the **exact bytes**, not a
re-marshalled struct, because both a forward write and an abort restore must
reproduce the file byte-for-byte (§10.3).

Every mutation of stage or row stage MUST be persisted durably **before** the
Git action it describes when the action is irreversible, and immediately
after when the action is an observation.

`resume_stage` MUST be persisted in the same durable write as every `stage`
transition. It equals `stage` except while `stage` is `failed`, `aborting`,
`aborted`, or `completed`; in those four states it retains the last forward
stage from which recovery or cleanup proceeds.

### 11.6 `--continue` (resume forward)

`--continue` MUST resume forward, never restart, and MUST be idempotent at
every stage. It re-enters at `resume_stage`:

| Stage on entry | Action |
| --- | --- |
| `initializing`, `preflight` | re-verify the frozen decision against live refs; complete any missing compatibility artifact under §11.2a; advance |
| `pinning-preimages` | re-create any missing old pin; advance |
| `computing` | first detect an active rebase left by a crash after Git created a conflict but before `conflict-paused` was saved; durably reconcile the first pending row to `conflict-paused`. Otherwise re-verify completed rows against their `planned_new_sha` **and** new pins, then resume at the first pending row |
| `conflict-paused` | refuse `conflict-unresolved` while a rebase is in progress; otherwise record `HEAD` as that row's `planned_new_sha`, pin it immediately, validate, and advance |
| `building-post-image` | rebuild and re-persist the post-image bytes and hash; they are a pure function of state |
| `pinning-computed` | verify every new pin against its recorded `planned_new_sha`; re-create missing pins; refuse `probe-failed` on a mismatch |
| `writing-remote-record` | write or verify the pending record (§12.3a); advance |
| `detaching-holders` | re-detach any holder not recorded as detached, skipping the excluded computation context |
| `committing-refs` | classify **every** affected ref first (§11.7), then complete or refuse |
| `refs-committed` | reclassify every ref; while `commit_point_reached == false`, foreign refuses and pre-image falls back to `committing-refs`; then re-check the live stack hash equals `stack_sha256_before` and write `stack_after_base64` forward |
| `writing-metadata` | classify the live stack and refs under §11.8a: `after` plus planned/no-op refs establishes and persists the commit point; `before` writes forward; anything else follows the pre/post-commit drift rules |
| `metadata-written` | set/confirm the monotonic `commit_point_reached` marker, report any later ref movement informationally, restore holders, then cleanup |
| `restoring-holders` | with `commit_point_reached == true`, report later ref movement informationally and restore remaining holders; a holder that still cannot be restored warns `holder-restore-deferred` |
| `cleanup` | with `commit_point_reached == true`, run the §11.6a cleanup order without gating on later ref movement |
| `completed` | report already complete, report later ref movement informationally, remove residue, exit 0 |
| `aborting`, `aborted` | **refuse** `reparent-state-present` with the §13.1 abort detail, telling the operator to re-run `tws stack reparent <feature> --abort`, which resumes the abort idempotently |
| `failed` | re-evaluate the recorded `failure_kind`; if the cause is gone, resume from `resume_stage`; otherwise re-refuse |

A `--continue` MUST NOT require or accept an approval token (§8.4). A run that
has begun aborting is never resumed forward: the operator already declared the
direction, and reversing it mid-rollback would leave a half-restored closure.

### 11.6a Cleanup order (both routes)

Cleanup MUST release in this order, and MUST tolerate any step already being
done:

1. confirm the scratch worktree has no tracked or non-ignored untracked
   residue, then remove it (`git worktree remove --force`, then
   `git worktree prune`) — external only. If residue remains from conflict
   resolution or validation, refuse `context-dirty`, preserve the state and
   scratch path, and tell the operator to inspect and clean it before retrying;
   `--force` is used only after that cleanliness proof. When this happens
   after the commit point, the command MUST say that the topology change is
   already committed and that only cleanup is pending; the launch exclusion
   remains active deliberately until the artifact can be removed;
   only `ENOENT` means the scratch is absent. Any other `stat` error and any
   prune failure refuses `probe-failed`, preserving state, pins, and locks;
2. delete every pin under `refs/tws/reparent/<run-id>/`;
3. delete the legacy sentinel `.sync-state.yaml` (external);
4. delete the v4 payload `.sync-state.v2.yaml` (external) or the compatibility
   transaction (checkout);
5. release the sync lock (`ReleaseSyncRunGuard` / `ReleaseCheckoutLock`);
6. delete the reparent state artifact **last**.

Scratch cleanup precedes pin deletion specifically so an uncertain stat/remove/
prune result leaves every recovery anchor intact. The artifact is deleted last
because it is the only record of steps 1–5. A
crash at any point leaves an artifact whose stage is `cleanup`, and the next
`--continue` or `--abort` repeats the whole ordered list harmlessly. Deleting
it first would strand pins and a lock with nothing to describe them.

### 11.6b `ReclaimCheckoutLock`

`--continue` and `--abort` legitimately own a checkout lock written by a dead
predecessor. The shipped `forceAcquireCheckoutLock` is unexported and
sync-specific, but its current PID/liveness ladder already refuses a live
foreign process. This feature MUST expose that same safety model beside it:

```go
// ReclaimCheckoutLock takes the checkout lock only when it is absent, already
// ours, or held by a dead PID. It never steals a live foreign lock.
func ReclaimCheckoutLock(featurePath string) error
```

It MUST reuse `forceAcquireCheckoutLock`'s existing `LockInfo`,
PID/liveness, compare-bytes-before-remove, and `O_EXCL` behavior rather than
implement a second reclaim ladder. `LockInfo` has no owner token and MUST NOT
gain one merely for this feature; the detailed reparent artifact carries
`owner_token`. A live foreign holder returns an error the caller surfaces as
`sync-state-present`, with detail saying that another reparent recovery
currently owns the shared checkout-sync lock. `forceAcquireCheckoutLock` MUST
keep all of its existing callers and behavior. The transferred-lock crash
case in §11.2 is decided by the matching token-bound global lock plus the
authoritative run identity, never by treating every dead PID-only feature lock
as owned.

### 11.7 Per-ref classification after a partial commit

At `committing-refs` resume, and at every `--abort` before its first ref write,
compute the live value of `refs/heads/<git branch>` for each row and classify
it as exactly one of:

- **no-op** (`preimage_sha == planned_new_sha` **and** the live ref equals that
  value) — this row was represented by a `verify` line and needs no update.
  It MUST NOT be restored, because "restore" and "complete" are the same
  value;
- **pre-image** (equals `preimage_sha`, and the row is not no-op) — the update
  did not land; re-issue it in a new CAS transaction with the same expected old
  value;
- **planned tip** (equals `planned_new_sha`, and the row is not no-op) — the
  update landed; skip it;
- **foreign** (anything else, including a no-op row whose live ref no longer
  equals its shared pre-image/planned value) — refuse `ref-foreign-value`,
  naming the branch, both expected values, and the observed value. The run
  MUST NOT overwrite it.

Classification MUST run over **every** row before any ref is written, so a
single foreign row aborts the whole recovery before it changes anything.

If every row is `planned tip` or `no-op`, set stage `refs-committed` and
continue forward. If some are pre-image and some planned tip, report
`reparent-recovery: ref-transaction-partial: …` in the progress output (§13.2),
then complete the remaining rows by CAS; this is a recovery, not a failure, it
does not exit 1, and it MUST leave stage `refs-committed` on success.

### 11.8 `--abort`

`--abort` is a bounded, non-destructive rollback before the commit point and a
forward-cleanup command afterward. It first evaluates §11.8a from disk and
persists `commit_point_reached` when that event can be proven. It then sets
stage `aborting` before its first action and `aborted` when it finishes.

0. If the commit point has already been reached, skip steps 1–5 entirely:
   no ref or metadata is moved. Report every affected ref that advanced after
   its planned tip as informational operator work, preserve the remote record
   unchanged, restore holders, and run cleanup. A post-commit foreign ref can
   never wedge artifact cleanup.
1. Before the commit point, if a rebase is in progress in the computation worktree, run
   `git rebase --abort` there.
2. **Before the commit point, classify every row first** (§11.7). A single `foreign` row refuses
   `abort-foreign-value` immediately, changes nothing at all, preserves state
   and pins, and tells the operator exactly which branch carries unexpected
   work. Only when every row classifies as no-op, pre-image, or planned tip
   does the abort proceed.
3. **Re-detach any holder that has already been restored.** A holder restored
   by a completed `restoring-holders` stage is attached to the branch the
   rollback is about to move; moving a ref out from under an attached worktree
   leaves that worktree's index and HEAD inconsistent. Each such holder is
   re-detached at its current tip, with its §9.4b untracked gate, before any
   CAS, but only after `git symbolic-ref --short HEAD` proves it is still
   attached to the recorded `git_branch`. If the operator detached it or
   switched it cleanly to another branch after restoration, refuse
   `holder-unsafe` without changing that checkout or any affected ref. The
   computation-context exception is an originally detached checkout already
   restored to the exact recorded `original_head`: it is detached from every
   affected branch and is therefore already safe on a repeated abort.
4. Restore public refs in one CAS transaction. Emit
   `update <ref> <preimage> <planned>` for each planned-tip row and
   `verify <ref> <preimage>` for every pre-image or no-op row, all in
   `ReparentClosureOrder`. The verify rows close the race between step 2 and
   commit without moving those refs. Each row's outcome is persisted in
   `abort_rows[]` before the next action.
5. Restore `stack.yaml` per §11.8a.
6. Re-attach every recorded detached holder, and restore the checkout's
   `original_branch` or `original_head` explicitly (§9.9). A holder that cannot
   be re-attached warns `holder-restore-deferred`, leaves stage `aborting` and
   the authoritative artifact/locks intact, and stops before remote-record or
   artifact cleanup so a later `--abort` retries restoration. This applies
   before and after the commit point.
7. Process the pending remote follow-up entries this run created exactly as
   §11.8a permits (`remote_record_written` tells it whether there are any):
   before the commit point, delete those entries and delete the file when it
   becomes empty; after the commit point, preserve the record **unchanged**,
   including no-op target rows whose PR base still changed. Before restoring
   the captured prior bytes or prior absence, persist
   `remote_record_restore_pending: true` together with the exact live
   source image (bytes or absence) that the restore is allowed to replace.
   Before journaling, that source must be the prior image, an absence or
   monotonic clear produced by this run, or this run's current record. A crash
   or post-rename error leaves the source and intent durable. The next
   `--abort` accepts only the exact journaled source or exact captured target,
   completes/re-fsyncs and re-verifies the target idempotently, then clears the
   pending/source fields. Foreign or unreadable bytes are never overwritten or
   removed.
8. Run the §11.6a cleanup order.

`--abort` MUST NOT roll back any commit the operator created after the run,
MUST NOT delete objects it did not create, and MUST NOT touch a ref outside
the closure. It MUST name exactly what it cleared. Re-running `--abort` after
a crash MUST resume idempotently from `abort_rows[]`.

### 11.8a The commit point

The run has exactly one commit point: **the first instant both of these durable
facts hold**:

1. `stack.yaml` contains the exact post-image bytes, i.e. its SHA-256 equals
   `stack_sha256_after_expected`; and
2. every affected ref classifies as `planned tip` or `no-op` under §11.7.

Immediately after observing that conjunction, the run MUST durably set the
monotonic `commit_point_reached` state field. Once true it never becomes false,
even if an operator later advances a branch. A crash after the metadata rename
but before that state update is recovered by re-evaluating the conjunction. If
the post-image hash matches and a live ref is a descendant of its planned tip,
that row is also sufficient evidence that the planned tip landed and was
subsequently advanced; the run persists the marker and reports that movement
informationally **only when that live value is distinct from both the exact
pre-image and the planned tip**. An exact `pre-image` classification is never
commit evidence, even when the planned tip is its ancestor: that topology can
exist before the CAS runs. A ref unrelated to its planned tip cannot establish
the commit point and remains a pre-commit `ref-foreign-value` /
`abort-foreign-value` refusal.

When **every** row is a no-op, ref classification cannot prove that the
required verify transaction ran because pre-image and planned values are
identical. The commit point therefore additionally requires the persisted
post-success CAS marker. Forged post-image metadata before that marker remains
pre-commit: `--abort` rolls back, while `--continue` runs the verify
transaction.

| Position | `--abort` behaviour |
| --- | --- |
| before the commit point | full rollback: refs restored to pre-image, `stack.yaml` restored byte-exactly, holders re-attached, artifacts removed |
| at or after the commit point | **forward completion only**: the operation is logically committed. `--abort` MUST NOT move a ref back and MUST NOT rewrite `stack.yaml`. It preserves the remote record unchanged, restores holders, runs cleanup, reports any later ref movement informationally, and reports that the reparent had already committed, naming the run and telling the operator that undoing it is a new reparent in the opposite direction |

Choosing the conjunction of durable metadata and ref classification — rather
than trusting a possibly stale stage field — makes the rule decidable from
disk alone after a crash. A coincidental or manual metadata edit that matches
the post-image cannot commit a CAS that never landed.

`stack.yaml` restore (step 5 of §11.8) is therefore permitted only when the
live file hashes to `stack_sha256_before` (nothing needs restoring), or when
it hashes to `stack_sha256_after_expected` / `stack_sha256_after` while at
least one affected ref still classifies as `pre-image` (the CAS never fully
landed, so the commit-point conjunction is false). Any other hash refuses
`metadata-drift` and aborts the abort. When a restore is required it writes
`stack_before_base64` verbatim through `WriteStackBytesAtomic`.

### 11.9 Crash-window matrix (complete)

| # | Crash point | `--continue` | `--abort` |
| ---: | --- | --- | --- |
| 1 | artifact written, no compatibility artifacts | complete them under §11.2a and advance | remove artifact and pins |
| 2 | compatibility artifacts written, nothing computed | advance | remove artifact, compatibility artifacts, pins |
| 3 | scratch worktree created | reuse it | prove it clean, then `worktree remove --force` and prune; otherwise preserve it and refuse `context-dirty` |
| 4 | mid-row rebase conflict, including crash after Git creates rebase state but before `conflict-paused` persists | reconcile state, native `rebase --continue`, then reparent `--continue` | detect active rebase, `rebase --abort`, then §11.8 |
| 5 | row computed, pin not yet written | re-verify `HEAD`, re-pin, validate | §11.8 (no public ref moved) |
| 6 | row pinned, validation not run | re-validate that row | §11.8 |
| 7 | all rows computed, post-image not built | rebuild and persist the post-image | §11.8 |
| 8 | post-image persisted, remote record not written | write the record, then advance | §11.8; no record entries to remove |
| 9 | remote record written, CAS not started | verify the record, re-verify pins, commit | delete the record entries; §11.8 |
| 10 | CAS `prepare` failed | classify against live refs; refuse on foreign | §11.8 |
| 11 | files backend, partial commit | §11.7 classification, complete forward | restore only planned-tip rows; refuse on foreign |
| 12 | CAS committed, metadata not written | re-check the before hash, write the post-image forward | CAS-restore refs, then leave `stack.yaml` untouched (it still holds the pre-image) |
| 13 | metadata written (commit point passed), holders not restored | restore holders, cleanup | **forward only** (§11.8a): restore holders, cleanup, report already committed |
| 14 | holders restored, cleanup incomplete | run §11.6a from the top | same |
| 15 | mid-abort, some rows restored | refuse `reparent-state-present` (§13.1 abort detail); re-run `--abort` | resume from `abort_rows[]`, idempotently |
| 16 | operator moved an affected branch before the commit point | refuse `ref-foreign-value` and tell the operator to reconcile | refuse `abort-foreign-value` rather than erase new work |
| 17 | operator advanced an affected branch after the commit point | report the later movement, restore any remaining holders, and clean up without moving refs or metadata | same forward-only completion; preserve the remote record |

Exactly **17** windows. Each MUST be exercised by T-061 for both verbs.

### 11.10 Observability during recovery

While a reparent **state artifact** exists for a feature, `tws doctor`,
`tws list` (both modes), `tws status`, `tws stack status`, and the checkout and
external sync reports MUST:

1. write exactly one sanitized line per affected feature **on stderr** before
   issuing any ancestry output on stdout. Its prefix is
   `reparent <status>:` where status is one of the six values below. Detail is
   CR/LF-sanitized. Continue/abort guidance appears only for actionable stale
   or complete residue (and only `--abort` for aborting/aborted residue), never
   for live-active, unsupported, corrupt, or foreign state.

2. suppress ordinary repair guidance for every affected entry — specifically
   every `ancestryGuidance` string that recommends `tws sync <feature>` or a
   manual `git rebase --onto`, every sync `--continue` / `--abort` recovery
   suggestion, and every problem, issue, guidance, or hint about the
   compatibility artifacts of §11.2. This explicitly includes
   `agent_status.go`'s `IssueSyncStateInvalid` unsupported-payload issue and
   `checkout_health.go`'s `state file unreadable` /
   `corrupt transaction state; manually remove ...` diagnostics. While the
   authoritative reparent artifact exists, those compatibility files are
   correct guards, not corrupt sync state, and no surface may tell the operator
   to remove them. Following any of those messages would bypass the
   transaction, and the sync verbs they name are refused by §11.2 anyway. The
   ancestry **status** and **reason** MUST still be reported; only the guidance
   is replaced by the line above.
3. remain strictly read-only: no fetch, no ref write, no state repair, and no
   write of any kind — including no marking or deletion of the remote
   follow-up record (§12.5).
4. distinguish and report the artifact as exactly one of
   `active | complete | unsupported | corrupt | foreign | stale`, where
   `stale` means the recorded `owner_pid` is not live and the stage is
   resumable.

`tws stack status --json` and the agent status JSON (`internal/agent_status.go`)
MUST carry these facts under a new top-level `reparent` key declared
`json:"reparent,omitempty"` with a **pointer** value. The key is therefore
**absent** — not `null` — whenever no artifact exists, which is what keeps the
no-reparent document byte-identical to today and leaves
`StackStatusReport.SchemaVersion` at `1`. No `schema_version` bump.

Outside an active or recoverable reparent state, `tws doctor`, `tws list`,
`tws status`, `tws stack status` (human and JSON), and the agent status JSON
MUST be byte-identical to today.

A pending **remote follow-up record** (§12.3), on its own, changes **no**
output on any of these surfaces. It is consumed only by the push paths and by
`tws stack reparent` itself. A record can outlive its run by design, and a
doctor report that nagged about every past reparent would be noise that trains
operators to ignore it.

---

## 12. Remote and PR follow-up

### 12.1 Hard rules

- The run MUST make zero provider calls (`gh`, `az`, HTTP, or any adapter).
- The run MUST NOT push, and v1 declares no `--push`.
- The run MUST NOT modify branch upstream configuration.
- **The only remote this feature names is `origin`.** All three live push paths
  hard-code `origin` today (`internal/cli/push.go`, `internal/checkout_sync.go`),
  so a record that named a different remote could only produce a warning about
  a push that never happens, or tempt rule R-PUSH into changing the remote an
  existing invocation targets. Per-remote support is out of scope (§18).
- Provider wording MUST be inferred only from the local `origin` URL; anything
  else is `generic`.

### 12.2 Guidance published by plan and success

Both the plan document (`remote.guidance`) and the success output (stderr)
MUST publish, in this order, one line each:

1. `tws changed no remote ref and no pull request.`
2. `<entry>: PR base <old> -> <new>` for every row whose PR base changes
   (the target only, unless a descendant's parent branch name changed, which
   cannot happen in v1).
3. `<entry>: local and <remote-ref> have diverged; the remote still holds the
   pre-reparent history.` for every row with `divergence != none`.
4. `retarget the pull request first, then fetch and integrate remote work,
   then publish.`
5. `if you force publish, use: git push --force-with-lease --force-if-includes
   origin <branch>`.

For a stack-entry parent whose logical `Name` differs from `GitBranch()`,
`pr_base_before` / `pr_base_after`, the persisted remote record, and fresh or
resumed success guidance MUST use the Git branch from
`old_parent.ref` / `new_parent.ref` (`refs/heads/...`), never the logical name.

The guidance MUST NOT recommend a plain `git push`, MUST NOT recommend
`--force-with-lease=<ref>:<sha>` bound to a freshly observed remote tip, and
MUST NOT imply that `--force-if-includes` strengthens an explicit
`--force-with-lease=<ref>:<expect>` form. If an explicit lease is ever
documented, it MUST be bound only to the last remote tip the operator actually
integrated.

### 12.3 Pending follow-up record

The run MUST write a small versioned record:

| Mode | Path |
| --- | --- |
| external | `<featurePath>/.reparent-remote.v1.yaml` |
| checkout | `<metadataRoot>/state/<feature>-reparent-remote.v1.yaml` |

The record MUST be written with `durableWriteFile` at mode `0600`.

```text
record_version  1  (a greater value refuses reparent-state-unsupported)
feature         string
run_id          string
created_at      RFC3339 UTC
entries[]:
  name, git_branch, repo
  remote                      constant "origin"
  remote_ref
  new_tip_sha                 the computed tip this run publishes locally
  remote_sha_at_write         observed remote-tracking value, empty when none
  pr_base_before, pr_base_after
  state                       pending|cleared
```

The loader is strict: `record_version` MUST equal `1`; `feature` MUST match
the requested feature; `run_id` is exactly 32 lowercase hex; `entries` is
non-empty with unique non-empty names; every row has `remote: origin`, the
canonical `refs/remotes/origin/<git_branch>` ref, valid lowercase 40- or
64-hex OIDs, a valid state, and all required fields. Any present invalid,
empty, duplicate, unsupported, foreign, or malformed record fails closed.

`repo` carries the row's execution repository identity (empty means the
workspace repository). Clearing and post-push observation resolve each entry
in its own stack/record repository context; no invocation may reuse the first
pushed entry's repository for the rest.

An entry whose `refs/remotes/origin/<branch>` does not resolve and whose
`remote_sha_at_write` is empty is written `cleared`: no published branch was
observed, so there is no remote history or PR-base relationship for this run
to protect. A later bare `--force-with-lease` remains safe if the branch
appears concurrently because Git rejects the missing local lease information
rather than clobbering the newly appeared ref. An entry with a non-empty
`remote_sha_at_write` is written `pending`. When every entry is `cleared`, the
record file MUST be deleted by whichever mutating route observed it (§12.5).

Replacing an older record row with a newer run is transactional across runs.
If an older same-name row is pending with non-empty publication evidence and
the incoming row is `cleared` only because its tracking ref is temporarily
unresolvable, the merged row MUST keep the incoming topology and
`new_tip_sha` while carrying forward the older `remote_sha_at_write` and
remaining `pending`. Missing local tracking data is not a positive §12.5
clear. Only a positive local clear may discard that evidence. A pre-commit
abort of the newer run still restores the complete prior record bytes; a
committed newer run supersedes the topology without weakening the next
fetch/push lease protection.

### 12.3a Write point (before any public ref moves)

The record MUST be written at stage `writing-remote-record`: **after** every
row has computed, validated, and been pinned — so `new_tip_sha` is known and
durable — and **before** holder detachment and before the CAS.

Writing it after a successful CAS would leave a window in which public refs
have been rewritten but nothing records that fact. A crash in that window
would produce exactly the situation the record exists to prevent: branches
whose history no longer matches their open pull request, with no local trace
telling the next push to warn. Writing it before the CAS inverts the failure
mode into a harmless one — a record that describes a rewrite that never
happened — which §11.8 step 7 and §12.5 both clean up.

Concretely:

- If the CAS never commits, or the run aborts, `--abort` deletes this run's
  entries and removes the file when it empties (§11.8 step 7).
- If the CAS commits, every moved ref is already covered.
- A `--continue` that re-enters at `writing-remote-record` writes or verifies
  the record before proceeding to the CAS.
- `remote_record_written` in state says whether there is anything to clean up.
- Recovery validates compatibility ownership, the live stack hash, and every
  affected ref before any clear. The shared recovery prelude persists clears
  only for `--continue`; `--abort` decides its pre/post-commit arm without
  changing the record. A post-commit abort preserves the current remote record
  byte-for-byte, while a pre-commit abort restores the captured prior record.
  A removed/archived entry in drifted metadata MUST NOT clear protection.
  Immediately before the CAS it
  re-verifies that a required record exists, belongs to this run, and matches
  every protected row, or that the recorded empty proof still corresponds to
  an absent path. Missing, foreign, corrupt, or newly appeared evidence
  refuses before any ref moves.

### 12.4 Push-path rule (exact, covers all three live bare-lease paths)

The three live bare-`--force-with-lease` entry points are:

1. external `tws push <feature>` → `pushFeature` → `pushEntries`;
2. external `tws sync <feature> --push` → `pushEntries` (legacy/all scope) or
   `pushScoped` (scoped run);
3. checkout `tws sync <feature> --push` → `gitPush` in
   `internal/checkout_sync.go`.

(Top-level `tws push` in checkout mode reaches `pushFeatureCheckout`, which
fails on `RequireWorktreePath` with `ErrWorktreeUnsupported` before any Git
push. It is not a fourth path and MUST stay unsupported.)

A real top-level external `tws push <feature>` MUST hold the same token-bound
feature mutation guard used by external reparent/sync from before stack and
record preflight until every entry has finished and clearing has been
persisted. A reparent therefore cannot begin between invocation-wide preflight
and a later entry. Lock acquisition/release is silent, dry-run remains
write-free, and with no pending record argv, stdout, stderr, and exit status
remain byte-identical.

**Rule R-PUSH.** For every entry `E` a push invocation would push, given a
pending record `R` for that feature:

0. If the same feature has an authoritative reparent state whose monotonic
   `commit_point_reached` is not true, every real push path refuses
   `reparent-state-present` before Git and before any record clear. A dry run
   reports the same result as `reparent-remote: would refuse: ...` and writes
   nothing. Once the commit point is proven, the remaining rules apply.

1. Under the invocation's mutation lock, evaluate all §12.5 local clearing
   observations **before** capability/lease preflight, persist them, then
   reload the envelope. Removed, archived, already-published and otherwise
   no-longer-pending rows clear even when no entry will be pushed. Dry-run
   evaluates the identical transition in memory and does not write.
2. `E` not listed in the reloaded `R`, or `R` absent → **behavior is byte-identical to
   today**: same argv, same output, same exit status.
3. **Invocation-wide preflight, before the first real push of the
   invocation.** For every entry of this invocation that *is* listed in `R`
   with `state: pending`, the invocation MUST verify both:
   - `ReparentGitCapabilities.CapForceIfIncludes` is true;
   - `refs/remotes/origin/<git branch>` resolves.

   If either fails for any such **published** entry (its
   `remote_sha_at_write` is non-empty), the invocation MUST refuse
   `remote-followup-unsafe-lease` **before pushing any entry at all**, exit 1,
   and name the failing entries, the required Git version (2.30), the
   `git fetch origin` remedy for a missing tracking ref, and the manual
   alternative. The gate is invocation-wide, never per-entry: pushing half a
   stack and then discovering the lease cannot be strengthened is the outcome
   the gate exists to prevent.
4. For each pending entry, before pushing it, print exactly one anchored
   warning line on stderr:

   ```text
   reparent-remote: <name> was reparented (<pr_base_before> -> <pr_base_after>); retarget the pull request before publishing
   ```

5. For such an entry, the argv MUST become

   ```text
   git push --force-with-lease --force-if-includes origin <git branch>
   ```

   i.e. the **bare** lease plus `--force-if-includes`. An explicit
   `--force-with-lease=<ref>:<sha>` MUST NOT be used.
6. On success, the invocation MUST re-evaluate and persist clearing so
   push-produced tracking observations can satisfy §12.5 observation 3.
6. No provider call and no PR mutation is performed on any path.

Because rule 1 leaves unaffected invocations untouched, the frozen push
argv goldens (`testdata/sync_noflag/declared_c2/push`,
`declared_c2/sync-push`) MUST remain byte-identical.

### 12.4a Dry-run (`tws push --dry-run`)

External `tws push --dry-run` reaches `pushEntries`, which prints
`  [~] <name> (would push --force-with-lease)` instead of pushing. Checkout
top-level `tws push`, including its dry-run form, remains unsupported and MUST
still fail before `pushFeatureCheckout` can preview an entry. A reachable
preview that names an argv the real run would not use is a defect, so:

0. Evaluate §12.5 clears in memory before preflight and decisions. Never
   persist, mark, or delete the record.

1. For a pending entry, the dry-run MUST print the §12.4 rule 4 warning line on
   stderr, then render the preview as

   ```text
     [~] <name> (would push --force-with-lease --force-if-includes)
   ```

2. The rule 2 preflight MUST still be **evaluated**, but in dry-run it is
   reported as a diagnostic, not a refusal:

   ```text
   reparent-remote: would refuse: remote-followup-unsafe-lease: <detail>
   ```

3. A dry-run MUST exit **0**, MUST run no `git push`, and MUST NOT write,
   mark, or delete the record. A preview that fails is still a successful
   preview.
4. Entries absent from the record keep today's exact line and stream.

### 12.5 Clearing without provider access

A record entry MUST be considered `cleared` when **any** of the following is
observed locally, with no fetch and no network:

1. `refs/remotes/origin/<branch>` resolves to `new_tip_sha` (the rewritten
   branch has been published);
2. `new_tip_sha` is an ancestor of `refs/remotes/origin/<branch>` (the remote
   contains the rewritten history plus later commits);
3. a tws push of that entry succeeded and its post-push remote-tracking value
   satisfies observation 1 or 2;
4. the entry no longer exists in `stack.yaml`, or is archived.

A missing remote-tracking ref is an **initial-state rule**, not a fifth
clearing observation: it initializes the row as cleared only when
`remote_sha_at_write` was already empty. If `remote_sha_at_write` is
non-empty and the tracking ref later disappears, the entry remains pending
and rule R-PUSH refuses until the operator fetches or resolves the remote
state.

**Read-only surfaces never persist a clear.** `tws doctor`, `tws list`,
`tws status`, and `tws stack status` MUST NOT evaluate this record at all
(§11.10): a pending record changes none of their output, so there is nothing
for them to compute. Any other read-only consumer that wants the facts MAY
compute them **in memory** and MUST NOT write, mark, or delete the file.

Only these **mutating** routes may persist a clear and delete the emptied file:
every real (non-dry-run) push path, every `tws sync` that pushes, and a fresh
reparent execution or reparent recovery route. A `--plan` route MUST only
observe and report the record; except for its declared fetch it remains
non-mutating. Push and push-enabled sync routes evaluate before their first
push. A fresh reparent execution evaluates and persists clears only **after**
approval, lock acquisition, and the §8.5 post-lock re-snapshot; a refused
fresh execution therefore leaves no state change. Recovery routes evaluate
after reclaiming their lock. Each permitted mutating route persists resulting
`state: cleared` values with `durableWriteFile` and deletes the file when every
entry is `cleared`. A successful pre-commit `--abort` MUST delete the record
entries that run created (§11.8 step 7).

Warning `remote-followup-pending` MUST be published by any **`ReparentPlan`**
built while a record with pending entries exists. It MUST NOT be added to
`RebasePlan`, whose warning domain is frozen at exactly eight members (§14.1).

---

## 13. Refusal domain

### 13.1 Closed kind set

New type in `internal/reparent_plan.go`:

```go
type ReparentRefusalKind string
var ReparentRefusalKinds = []ReparentRefusalKind{ /* rank order below */ }
func SelectPrimaryReparentRefusal(blockers []ReparentPlanBlocker) (ReparentRefusalKind, []ReparentPlanBlocker)
```

`ReparentRefusalKinds` MUST contain exactly **49** members in this rank order
(rank 1 is reported first). A test MUST assert the count and the exact order.

| Rank | Kind | Raised when |
| ---: | --- | --- |
| 1 | `plan-unavailable` | the plan itself could not be built |
| 2 | `stack-unsortable` | `ReparentClosureOrder` fails on the current stack |
| 3 | `duplicate-entry-name` | two entries share a `Name` |
| 4 | `duplicate-git-branch` | two non-archived entries share a `GitBranch()` |
| 5 | `target-unknown` | `<entry>` is not in `stack.yaml` |
| 6 | `target-archived` | the target is archived |
| 7 | `affected-archived` | any closure row (or the destination entry) is archived |
| 8 | `cross-repo-closure` | a genuine closure row's execution common-dir differs from the target's |
| 9 | `repo-unavailable` | a row's repository root cannot be resolved |
| 10 | `destination-unset` | `--onto` is empty |
| 11 | `destination-unresolvable` | destination ref/object does not resolve, or names a pseudo ref |
| 12 | `destination-ambiguous` | more than one namespace candidate, or a multi-match abbreviated OID |
| 13 | `destination-kind-mismatch` | `--onto-kind entry` with a non-entry token |
| 14 | `destination-self` | destination entry is the target |
| 15 | `destination-descendant` | destination entry is a descendant of the target |
| 16 | `destination-cycle` | the post-image graph is cyclic |
| 17 | `destination-resolver-divergent` | the four §4.3c resolvers disagree on the stored token |
| 18 | `destination-same-parent-stale` | same parent, edge not current |
| 19 | `branch-ref-missing` | a row's Git branch does not exist |
| 20 | `cutoff-absent` | no rung of §5.1 produced a boundary |
| 21 | `cutoff-unresolvable` | a recorded, supplied, or enumerated cutoff does not resolve, is ambiguous, or names a pseudo ref |
| 22 | `cutoff-conflict` | `--cutoff` disagrees with an authoritative `LastBaseSHA` |
| 23 | `cutoff-not-ancestor` | `merge-base --is-ancestor` fails |
| 24 | `descendant-cutoff-absent` | no rung of §5.2 produced a boundary |
| 25 | `descendant-cutoff-override-unsupported` | a per-descendant boundary would be needed |
| 26 | `merge-commit-in-replay-set` | a row's replay range contains a merge |
| 27 | `git-operation-in-progress` | rebase/merge/cherry-pick/revert/bisect active |
| 28 | `context-dirty` | tracked modifications in a consulted worktree |
| 29 | `untracked-overwrite` | the §9.4b just-in-time gate found a colliding untracked path |
| 30 | `holder-unsafe` | a holder is prunable, multiply held, or drifted **before** the commit point |
| 31 | `session-live` | a live or unverifiable tws session holds an affected branch |
| 32 | `scratch-worktree-unavailable` | the scratch worktree cannot be created |
| 33 | `sync-state-present` | sync state or lock exists for this feature/mode |
| 34 | `reparent-state-present` | another reparent run is recorded |
| 35 | `reparent-state-unsupported` | artifact `state_version` / `record_version` is newer than supported |
| 36 | `reparent-state-corrupt` | artifact cannot be decoded |
| 37 | `reparent-state-foreign` | artifact belongs to another feature/workspace |
| 38 | `compat-artifact-missing` | compatibility reconstruction is required but ownership/safety cannot be proven; ordinary owned absence is automatic recovery work |
| 39 | `probe-failed` | a required read-only Git probe failed, or a pin resolves to an unexpected object |
| 40 | `capability-unsupported` | a required Git capability is absent or unknown (§9.10) |
| 41 | `validation-failed` | the frozen validation command exited non-zero or left residue |
| 42 | `conflict-unresolved` | `--continue` while a rebase is in progress |
| 43 | `ref-transaction-mismatch` | CAS `prepare` failed or a hook vetoed |
| 44 | `ref-foreign-value` | an affected ref holds neither pre-image nor planned tip |
| 45 | `metadata-pending` | refs committed but metadata could not be written |
| 46 | `metadata-drift` | `stack.yaml` no longer matches an expected hash |
| 47 | `abort-foreign-value` | `--abort` found operator work on an affected ref |
| 48 | `remote-followup-unsafe-lease` | rule R-PUSH 2 (capability or tracking ref) |
| 49 | `no-work` | fresh execution with `sameParent` and a current edge |

The count is exactly 49 and is preserved by a **one-for-one swap** against the
naive enumeration: `ref-transaction-partial` is removed and
`destination-resolver-divergent` (rank 17) takes its place. `sentinel-missing`
is renamed `compat-artifact-missing` (rank 38), which changes a spelling, not a
count, because checkout mode writes a transaction rather than a sentinel.

Three deliberate consequences:

- `ref-transaction-partial` is **not** a refusal kind. §11.7 reports it on a
  run that recovers and exits 0, and a refusal that does not refuse is a
  category error. It is a progress token with its own prefix (§13.2).
- A `--continue` against a run already at stage `aborting` or `aborted`
  refuses **rank 34 `reparent-state-present`** — the state is present and does
  not admit the requested direction — with the exact detail:

  ```text
  this run is already aborting (stage <stage>); finish it with: tws stack reparent <feature> --abort
  ```

  No separate kind is minted for it: the domain stays closed at 49, and the
  detail carries the whole story.
- `limit-per-entry`, `limit-total`, `guard-limit-mismatch`,
  `approval-without-limits`, `approval-mismatch`, and `revalidation-mismatch`
  are deliberately **absent**: they belong to the reused sync guard domain
  (`RefusalKind`), are produced by `EvaluatePlanGuard`, and are printed with
  the `plan-guard:` marker (§13.2). `ReparentPlanApprovalCovers.waived_kinds`
  is therefore typed `[]RefusalKind` (§7.11), and state records a
  `failure_domain` beside a string `failure_kind` (§11.5) so a guard, native
  Git, or I/O failure can be persisted without forging a reparent kind.

`SelectPrimaryReparentRefusal` MUST sort by `(rank, entry nil-first, entry,
detail)`, remove exact duplicates, and return the first blocker's kind,
mirroring `SelectPrimaryRefusal`.

### 13.2 Anchored output

Exactly three reparent-owned stderr prefixes exist, and no line carries two:

- Every **reparent-owned execution refusal** MUST be exactly one line:

  ```text
  reparent: <kind>: <detail>
  ```

  and, when public refs and metadata are untouched or already durably
  recorded:

  ```text
  reparent: <kind>: state-preserved: <detail>
  ```

  Tests MUST anchor it as `^reparent: [a-z][a-z-]*: .*$`.
  Every `ReparentRefusalError` detail is normalized at construction and render
  boundaries: CRLF, LF and CR become spaces and repeated whitespace collapses,
  so hook/Git/validation output can never create a second physical line.
- Every **recovery progress fact that is not a failure** MUST use:

  ```text
  reparent-recovery: <token>: <detail>
  ```

  The only v1 token is `ref-transaction-partial` (§11.7). A
  `reparent-recovery:` line never sets a non-zero exit status and never
  appears in `blockers`.
- Every **push-path follow-up line** MUST use `reparent-remote:` (§12.4,
  §12.4a).

Additionally:

- Guard, limit, approval, and revalidation refusals keep the existing
  `plan-guard: <kind>: <detail>` line verbatim, because they come from the
  reused guard machinery and the reused `RefusalKind` domain.
- Native Git errors, Cobra flag errors, the §3.5 rules, and the §9.7 conflict
  pause MUST remain marker-free.
- A refusal MUST be the process's only stderr **refusal** line; the run MUST
  NOT print both a `reparent:` and a `plan-guard:` refusal for the same
  failure. `reparent-recovery:` and `reparent-remote:` lines are progress, not
  refusals, and may coexist with exactly one refusal line.
- Holder restoration deferral uses the established
  `reparent: holder-restore-deferred: <detail>` warning/refusal contract on
  continue and abort. No generic `reparent: <count> holder(s)` fourth shape is
  permitted, and package cli MUST NOT append a second unanchored summary after
  the executor has emitted the owned warning line(s).

### 13.3 Cells

- **Plan route**: every plannability/measurement failure — including
  pre-fetch target repository resolution, unreadable or malformed stack,
  duplicate identity, unknown target, cross-repo closure and unavailable
  repository — is normalized into a `ReparentPlan`; every applicable kind appears in `blockers`;
  `refusal.kind` is `SelectPrimaryReparentRefusal(blockers)` or `null`;
  `runnable` is `false`; exit 0.
- **Unavailable plan**: `summary.plannability = "unavailable"`,
  `refusal.kind` is the ranked specific blocker (or `plan-unavailable` when no
  more specific kind exists), `target`/`descendants` populated as far
  as they could be resolved, every unknown cell `null`, and
  `policy.oid_width` is `null` rather than `0`; exit 0. Its human form uses
  `unknown`, `unavailable`, or `unresolved` and never `(computed at replay)`.
  A **deferred** cell (§7.5a) is not an unknown cell and never produces this
  state.
- **No-work plan**: `summary.plannability = "no-work"`,
  `summary.has_work = false`, `refusal.kind = null`,
  `approval.fingerprint = null`, `runnable = false`; exit 0.
- **Execution route**: the primary refusal is printed as §13.2 and the process
  exits 1.
- Only command-line/arity validation, or an actual output/render I/O failure
  before a document can be written, may make a plan invocation exit nonzero.
- **Recovery progress**: a `reparent-recovery:` line is printed and the run
  continues; it never changes the exit status.

---

## 14. Compatibility and documentation

### 14.1 Frozen surfaces (MUST NOT change)

- Every `tws sync` flag name, shorthand, default, usage string, and
  mutual-exclusion message.
- `internal/cli/testdata/rebase_plan/sync_help.txt` (the `tws sync --help`
  snapshot).
- All 126 files under `internal/cli/testdata/sync_noflag/` and every
  `TestSyncNoFlag_*` assertion.
- `RebasePlan`, its 25-key order, `RebasePlanSchemaVersion`, `RefusalKind` and
  its 30 members and ranking, the eight-member `PlanWarning` kind domain,
  `PlanFingerprint`, the `"tws-plan-fp\x00"` domain, and every existing sync
  approval token.
- `SyncRunStateVersion`, `SyncRunStateGuardedVersion`,
  `CheckoutTransactionVersion`, `CheckoutTransactionGuardedVersion`, the
  `CheckoutTransaction` struct, `LoadSyncRunState`, `SaveSyncRunState`,
  `ContinueCheckoutSync`, `AbortCheckoutSync`, `forceAcquireCheckoutLock`, and
  the existing state/lock path helpers.
- `SaveStack` and `atomicWriteFile` behavior and callers.
- `TopoSort` and `Descendants`.
- External multi-worktree behavior outside the new command.
- Checkout single-repository semantics and the unsupported top-level
  `tws push` in checkout mode.
- `GitCapabilities` (the closed six-gate table).
- `tws doctor`, `tws list`, `tws stack status` (human and JSON), `tws status`,
  and the agent status JSON whenever no reparent **state artifact** exists — a
  pending remote record alone changes nothing (§11.10).
- Absence of implicit pushes and provider calls.

### 14.2 Deliberate, tested changes

1. A new `reparent` subcommand under `tws stack`. `tws stack --help` gains one
   line; a new golden
   `internal/cli/testdata/reparent/stack_help.txt` MUST pin it, and the
   existing `tws stack status` help MUST be unchanged.
2. `stackCmd.ValidArgsFunction` suppresses the literal `reparent` candidate.
3. `tws sync` gains exactly one reparent-aware refusal, evaluated **before**
   mode dispatch in both modes, that fires only while a reparent artifact
   exists (§11.2).
4. `tws doctor` / `tws list` / `tws status` / `tws stack status` / the checkout
   and external sync reports gain the reparent-in-progress line and the
   guidance suppression of §11.10, only while an artifact exists.
5. `tws stack status --json` and the agent status JSON gain an **`omitempty`
   pointer** `reparent` key, absent without reparent state. No `schema_version`
   bump, and the absent-state documents stay byte-identical.
6. The three push paths gain rule R-PUSH, and the reachable external
   `tws push --dry-run` path gains §12.4a; all are no-ops without a pending
   record. Checkout top-level push remains unsupported, including dry-run.
7. `WriteStackBytesAtomic`, `SaveStackAtomic`, and `durableWriteFile` are
   added; `SaveStack` and `atomicWriteFile` are untouched.
8. `ReparentGitCapabilities` is added beside, not inside, `GitCapabilities`.
9. `ReclaimCheckoutLock` is added beside, not inside,
   `forceAcquireCheckoutLock` (§11.6b).
10. `ReparentClosureOrder` is added; `TopoSort` is untouched (§6.1a).
11. `syncIOFault` gains four appended tokens (§10.3b).
12. Runtime-artifact filters skip `.reparent*` (§9.2).
13. `tws open` and checkout agent-session launch refuse for a feature with a
    live reparent state artifact (§14.2a).

### 14.2a Launch exclusion

While a reparent state artifact exists for a feature, `tws open <feature> …`
and every checkout agent-session launch for that feature MUST refuse with the
§11.2 reparent-aware sentence and MUST NOT start a session or attach a
terminal. The run has detached holders and is about to move the very branches a
session would sit on; §6.2's `session-live` refusal protects the reparent from
sessions, and this protects sessions from the reparent. The refusal MUST be
lifted the instant the artifact is gone.

Launch exclusion is an atomic handshake, not two independent prechecks.
Checkout launch MUST acquire and publish its workspace-global session intent
before its final mutation/reparent check; reparent treats a live or
unverifiable checkout session intent/lock as `session-live`. External reparent
MUST publish its shared feature mutation guard before the final `SessionProbe`.
Every external direct, tmux, feature-directory, and `--all` launch MUST publish
its starting record or scoped launch intent before its final mutation check and
before spawning or attaching; that final check observes both authoritative
state and the shared guard. Reparent admission observes direct records, scoped
launch intents, per-branch tmux sessions, and the feature-wide `--all` session.
Recovery reruns the same probe after reclaim. Refusal removes the launcher's
own intent, and a mutating reparent admission removes only provably dead
crash-left intents after taking the guard. Neither direction waits while
holding the other lock.

Checkout `tws open <feature> --feature-dir` follows the same handshake: it
publishes the workspace-global checkout launch intent, performs the final
reparent/global-mutation/session check, then calls `openDirect` while retaining
the intent through the agent and shell. Every refusal/exit removes only its own
intent by rooted ownership checks.

A checkout launch intent is absent only on `ENOENT` or when its valid owner PID
is provably dead. Live and unverifiable PIDs, stat/read failures, malformed
owner data, symlink lock paths, and non-directory lock paths remain
`session-live`. The intent directory is inspected with `Lstat`; a symlink is
never followed, including when it points outside the workspace. Read-only
planning may classify a dead intent as non-blocking but MUST NOT delete it.
Fresh and recovery execution remove it only after acquiring the
workspace-global checkout mutation lock, re-reading and comparing the exact
directory identity and owner-file identity plus exact owner bytes immediately
before rooted, targeted removal; a changed, live, or unverifiable intent is
preserved and the final `SessionProbe` refuses.

### 14.3 Downgrade

The downgrade guarantee is intentionally narrow. After the compatibility
envelope exists, shipped v1.2.16 **same-feature `tws sync`** behaves as follows:

- external plain sync → refuses on the v2 payload / legacy sentinel pair,
  whose write order (§11.2) guarantees no lone-sentinel window;
- external `--continue` and `--abort` → the shipped cell-3/6/9/12 wrapper,
  `scoped sync state at <path> is unreadable or uses an unsupported version
  (unsupported scoped sync state version 4); inspect it and remove it manually
  — tws will not guess`, before any recovery work. Downgrade tests MUST assert
  this complete wrapper, or its stable unsupported-version substring, rather
  than treating the nested loader error as the whole operator-visible line;
- checkout plain sync → refuses on the existing transaction, because
  `HasCheckoutTransaction` is a bare `os.Stat` and is unaffected by the
  undecodable body;
- checkout `--continue` **and** `--abort` → `LoadCheckoutTransaction` fails to
  decode `state_version: "4-reparent"` into an `int`, so both return
  `no transaction to continue: …` / `no transaction to abort: …` **before**
  the lock, the `git rebase --abort`, `restoreOriginal`, or any deletion. This
  is precisely why an integer `4` is not used: shipped `AbortCheckoutSync`
  performs no version check at all (§11.2);
- the reparent artifact itself is at a path no older release reads.

Released v1.2.16 cannot observe crash window 1 between authoritative state and
compatibility-envelope birth, the workspace-global checkout mutation lock, a
checkout reparent for an unrelated feature, external launch intents, or the
top-level external push mutation lock. It also has no reparent-aware top-level
push path. Therefore operators **MUST NOT use an older tws while any reparent
is active or recoverable**, including crash window 1. Current tws remains
globally safe and is the only supported recovery binary. Writing an
old-readable sentinel before authoritative state would merely replace window 1
with an unowned sentinel-only crash window and violate §11.4 birth order, so
v1 does not pretend that workaround closes the released-code gap.

### 14.4 Documentation surfaces (all required to land)

| File | Required change |
| --- | --- |
| `README.md` | add `tws stack reparent <feature> <entry> --onto <dest>` to the `## All Commands` table, plus a short workflow showing plan → approve → execute |
| `docs/cheatsheet.md` | new `## Reparent a branch onto a new base` section, placed after `## Sync (rebase in dependency order)` and adjacent to `## Migrate an existing branch` |
| `CHANGELOG.md` | new bullet under `## Unreleased` describing the command, the plan/approval gate, the CAS transaction, the recovery contract, and the absence of implicit push |
| `assets/skills/claude/tesseraworkspaces/SKILL.md` | add the command to `### Commands` and a short "when to reparent vs when to sync" note |
| `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` | add a "Reparent before a wide sync" section and **both** machine admission predicates (§7.12 fresh, §7.12a continue) |
| `assets/skills/copilot/tws.prompt.md` | add the command to `## CLI Reference` and to `## Stacked & Divergent Branches` |
| `docs/roadmap.md` | move **safe reparent/restack** from "Current target" into the shipped foundations list, and name the next target |
| `docs/engineering-workflow.md` | append slice 13 describing this feature, and update "Next roadmap feature" |

`TestSyncPlanDocs_PlanningProseSurfacesCarryShippedTargetAndNoFlagLiteral`
currently asserts that `docs/roadmap.md` and `docs/engineering-workflow.md`
carry `safe reparent/restack` as the current/next target. That assertion MUST
be updated deliberately to assert the **new** next target, and must continue
to assert that planning prose carries no sync plan flag literals.
`docs/retrospectives/v1.2.7-upgrade-operations.md` is a historical record and
MUST NOT be edited.

Documentation prose MUST state the §9.13 atomicity wording verbatim in
substance: race-atomic ref commit, crash-atomic only on reftable, three
separate effects, one commit point requiring both durable post-image metadata
and planned/no-op refs, and forward recovery. It MUST also state that
`--continue` and `--abort` never take an approval token, and that a reparent
stores a **full ref** as the new base.

All eight documentation surfaces MUST describe a reparent preview with the
exact semantic sentence `moves no branch and writes no tws state; may fetch
according to policy`. A docs test MUST reject any reparent-plan paragraph that
instead says it “moves nothing”.

---

## 15. File ledger (new and touched)

**New**

```text
internal/reparent_plan.go              document types, warning domain, ReparentRefusalKind
internal/reparent_plan_build.go        BuildReparentPlan, destination/cutoff/closure resolution
internal/reparent_plan_fingerprint.go  ReparentPlanFingerprint + preimage
internal/reparent_plan_render.go       FormatReparentPlan, MarshalReparentPlan
internal/reparent_destination.go       ResolveReparentDestination, namespace enumeration
internal/reparent_state.go             ReparentState, versions, paths, load/save, stages
internal/reparent_refs.go              pin helpers, ReparentEntryRefID, CAS transaction, classification
internal/reparent_exec.go              computation worktree, per-row rebase, holders, metadata write
internal/reparent_remote.go            follow-up record, guidance, rule R-PUSH helpers
internal/external_session_intent.go     external direct/tmux/all launch-intent IO and liveness
internal/cli/stack_reparent.go         cobra command, routes, validation order, streams
```

**Touched (minimally)**

```text
internal/cli/stack.go        register stackReparentCmd(); suppress `reparent` completion
internal/stack.go            add WriteStackBytesAtomic, SaveStackAtomic, durableWriteFile, ReparentClosureOrder
internal/syncstate.go        append four syncIOFault tokens (§10.3b)
internal/sync_run_state.go   token-bound short-lived feature mutation guard release
internal/git_capability.go   add ReparentGitCapabilities
internal/cli/sync.go         one reparent-aware pre-check, before mode dispatch
internal/checkout_sync.go    rule R-PUSH in gitPush; add ReclaimCheckoutLock
internal/cli/push.go         top-level external push mutation guard; rule R-PUSH; §12.4a dry-run
internal/cli/doctor.go       reparent-in-progress line, guidance suppression
internal/cli/list.go         reparent-in-progress line
internal/cli/open.go         §14.2a direct/tmux/feature-dir/all launch handshake
internal/cli/importcmd.go    exclude token-named external launch intents from import
internal/health.go           reparent-aware guidance suppression (external doctor/list)
internal/checkout_health.go  suppress unreadable/corrupt compatibility-state guidance
internal/stack_ancestry.go   ancestryGuidance suppression seam (no ancestry logic change)
internal/stack_status.go     reparent projection (omitempty pointer) + guidance suppression
internal/agent_status.go     reparent projection + suppress IssueSyncStateInvalid compatibility hints
internal/session.go          §14.2a checkout session launch exclusion
internal/cli/sync_downgrade_test.go  extend prior-binary acquisition to v1.2.16 (§17.4a)
```

Every file that enumerates a feature directory for worktrees, import, or
adoption MUST filter `.reparent*` (§9.2); the audit that proves it is T-042.

Package boundary: all planning, resolution, state, and execution logic lives in
`internal`. `internal/cli` owns only flag parsing, routing, stream selection,
and the anchored refusal line. No new external module dependency is permitted.

---

## 16. Acceptance criteria

Each criterion is independently verifiable. IDs are stable and MUST be cited
by the tests that cover them.

**Command surface**

- **AC-001** `tws stack reparent <feature> <entry> --onto <dest> --plan`
  prints a plan on stdout and exits 0.
- **AC-002** `tws stack <feature>` output is byte-identical to before.
- **AC-003** `tws stack -- reparent` reaches a feature literally named
  `reparent` and prints its tree.
- **AC-004** Fresh route with 1 or 3 positional args refuses; continue/abort
  with 2 args refuses.
- **AC-005** Each of `--onto`, `--onto-kind`, `--cutoff`, `--approve-plan`,
  `--max-replay-per-entry`, `--max-replay-total`, `--fetch`, `--no-fetch`
  combined with `--continue` or `--abort` refuses with the exact §3.5 message.
- **AC-006** `--json` without `--plan` refuses `--json requires --plan`.
- **AC-007** A **fresh execution** without `--approve-plan` refuses; without
  any replay limit refuses; `--approve-plan` without a limit refuses. A bare
  `--continue` and a bare `--abort` pass §3.5 entirely, mentioning neither the
  token nor either limit.
- **AC-008** `--yes`, `--dry-run`, `--push`, `--test`, `--root`, `--unparent`,
  `--remote` are not declared (help snapshot).
- **AC-009** §3.5 rule messages for rules 10 and 11 are byte-identical to the
  shipped `tws sync` sentences for the same conditions.
- **AC-010** A §3.5 failure prints the Cobra usage block; every §13 refusal
  prints exactly one line and no usage block.

**Destination**

- **AC-011** `--onto-kind auto` resolves an existing entry name as an entry and
  an unknown token as a ref.
- **AC-012** `--onto-kind entry` with a non-entry token refuses
  `destination-kind-mismatch`.
- **AC-013** `--onto-kind ref` with a token that is also an entry name resolves
  as a ref and stores the **full ref**, not the short token.
- **AC-014** A short name matching both a branch and a tag refuses
  `destination-ambiguous` listing both candidate refs; the full ref succeeds.
  `for-each-ref` output is exact-equality filtered, so a pattern match that is
  not one of the five candidates never counts.
- **AC-015** Full refs, annotated tags, lightweight tags, remote-tracking refs,
  and full canonical OIDs all resolve. An abbreviated OID matching more than
  one object refuses `destination-ambiguous` via `git rev-parse --disambiguate`
  listing every OID; an abbreviated OID matching none falls through to
  short-name resolution; hex recognition is case-insensitive and the stored OID
  is lowercase. Non-empty destination and cutoff tokens preserve the exact
  operator bytes; surrounding whitespace is not silently trimmed into a
  different resolvable token.
- **AC-016** `policy.oid_width` is `40` in a SHA-1 repository and `64` in a
  SHA-256 repository, and no code path assumes 40. An unavailable plan
  publishes JSON `null` and human `unknown`, never `0`. A failed or unsupported
  `--show-object-format` probe retains the typed `capability-unsupported`
  refusal and is never rewritten as `probe-failed`.
- **AC-017** `HEAD`, `FETCH_HEAD`, and `ORIG_HEAD` each refuse
  `destination-unresolvable` as `--onto` and `cutoff-unresolvable` as
  `--cutoff`, with the pseudo-ref detail.
- **AC-018** A literal ref destination makes the entry a stack root; no
  `--root` flag exists and `Base` is never written empty.
- **AC-019** Destination equal to the target, to a descendant, or creating a
  cycle refuses with the matching kind; the cycle check runs on the post-image
  graph for **every** kind, including literal refs.
- **AC-020** A destination whose requested token equals the repository default
  branch is NOT rewritten to `origin/<default>`; warning
  `destination-not-remote-rewritten` is published and the stored token is the
  full `refs/heads/<default>`.
- **AC-021** All four §4.3c resolvers agree on the stored token for every
  destination kind; an injected divergence refuses
  `destination-resolver-divergent` naming the resolver. A canonical literal
  full-ref or raw-OID token that equals any stack entry `Name` refuses with
  that same kind even when both currently resolve to the same commit, and
  still refuses after the entry branch moves.

**Cutoff**

- **AC-022** Recorded `LastBaseSHA` is authoritative; a matching `--cutoff` is
  accepted; a differing `--cutoff` refuses `cutoff-conflict`.
- **AC-023** Present-but-unresolvable `LastBaseSHA` refuses
  `cutoff-unresolvable` even with `--cutoff`.
- **AC-024** Absent `LastBaseSHA` plus `--cutoff` uses the supplied boundary
  with provenance `operator-supplied`.
- **AC-025** Absent `LastBaseSHA`, no `--cutoff`, resolvable old-parent tip
  that is an ancestor → provenance `old-parent-tip`.
- **AC-026** Absent `LastBaseSHA`, no `--cutoff`, non-ancestor or unresolvable
  old-parent tip → refuses `cutoff-absent`.
- **AC-027** A resolvable but non-ancestor cutoff refuses
  `cutoff-not-ancestor` and MUST NOT run any rebase.
- **AC-028** Descendants use their own snapshotted `LastBaseSHA`, never the
  target's old tip, when they have one.
- **AC-029** A descendant with no recorded cutoff uses its parent pre-image tip
  only when it is an ancestor, else refuses `descendant-cutoff-absent`.
- **AC-030** An ambiguous cutoff — two ref candidates, or a multi-match
  abbreviated OID, or a token that is both — refuses `cutoff-unresolvable`
  with every candidate listed. `cutoff-ambiguous` does not exist and
  `destination-ambiguous` is never raised for a cutoff.
- **AC-031** In the reparent-owned source files of §15 and in every argv this
  feature emits, `git merge-base` appears only as an `--is-ancestor` check; no
  reparent code path derives a cutoff from a merge base. The assertion is
  scoped to those files, because the frozen `ancestryMergeBase`
  (`internal/stack_ancestry.go`) legitimately calls plain `merge-base` and is
  out of this feature's boundary.

**Customer topology**

- **AC-032** With `master = A-B'-D`, `pr1 = A-B`, `pr2 = A-B-C`,
  `pr2.Base = "pr1"`, `pr2.LastBaseSHA = B`, a reparent of `pr2` onto `master`
  replays exactly `C`, produces `D + C'`, conflicts with nothing, and leaves
  `pr1` untouched. Verified in both workspace modes.
- **AC-033** The same run records `pr2.Base = "refs/heads/master"` and
  `pr2.LastBaseSHA = D`, and the very next `tws stack status` reports the edge
  `current`.

**Scope**

- **AC-034** A genuine closure row in another repository refuses
  `cross-repo-closure` by execution common-dir; an unrelated entry in another
  repository whose `Base` merely spells the target's name is neither pulled
  into the closure nor refused. A symlink/`..` alias resolving to the same
  common dir remains visible in the candidate closure but is refused
  `destination-resolver-divergent` when its stored `Repo` spelling differs
  from its logical parent/target, because shipped sync resolvers are not yet
  common-dir-aware. Exact equal raw repo tokens remain accepted, and raw
  metadata is never rewritten.
- **AC-035** An archived target refuses `target-archived`; an archived closure
  row refuses `affected-archived`.
- **AC-036** Duplicate `Name` refuses `duplicate-entry-name`; duplicate
  `GitBranch()` refuses `duplicate-git-branch`; both before any Git process.
- **AC-037** `ReparentClosureOrder` is deterministic: the same bytes produce
  the same order twice, `stack.yaml` declaration order is the sibling
  tie-break, reordering `Stack.Branches` changes the fingerprint, and
  `TopoSort` is neither modified nor called by it.
- **AC-038** Dirty tree, active Git operation, unsafe holder, and
  live/unverifiable session each refuse before mutation.
- **AC-039** Same parent with a current edge yields a no-work plan with a null
  fingerprint and a fresh-execution refusal `no-work`; replay/cutoff/merge and
  capability-only blockers are suppressed once no-work is known. Its metadata
  delta is strictly unchanged for target and descendants, has no deferred
  `last_base_sha_after`, no post-image hash, and renders `stack.yaml after:
  unchanged`.
- **AC-040** Same parent with a stale edge refuses
  `destination-same-parent-stale` and names `tws sync <feature> --from
  <entry>`.

**Plan**

- **AC-041** `ReparentPlan` emits exactly the 25 keys of §7.3, in order;
  `ReparentPlanRow` exactly 23; `ReparentPlanSummary` exactly 11;
  `ReparentPlanApprovalCovers` exactly 8. Every plan-route plannability failure
  — including a missing Git executable when workspace identity can still be
  resolved — is a human/JSON document with exit 0; only CLI/arity or document
  render I/O without output may exit nonzero. Reparent uses an error-returning
  `exec.LookPath`, never `RequireTool`/`os.Exit`. Workspace resolution mirrors
  `RequireWorkspace`: plans run from a repository/worktree, the external
  workspace root, or a feature directory via `DetectWorkspaceRoot` plus the
  external repo inference fallback, while every Git probe uses `Cmd.Dir`.
- **AC-042** `RebasePlan` key order, `RefusalKind` count/order, the
  eight-member `PlanWarning` domain, and `PlanFingerprint` values are unchanged
  by this feature.
- **AC-043** `MarshalReparentPlan` emits exactly one JSON value plus exactly
  one newline on stdout; arrays are never `null` except where documented.
  In particular `state.files.external_run_payload.selected` is `[]` on fresh
  external, checkout, and unavailable documents.
- **AC-044** Human document and JSON go to stdout; mode header, fetch prose,
  progress, conflict text, and refusals go to stderr.
- **AC-045** A plan that publishes a refusal still exits 0.
- **AC-046** Every `parent-computed` row publishes `destination_sha`,
  `new_parent.sha`, and `last_base_sha_after` as `null`, and any plan with such
  a row publishes `stack_sha256_after_expected`, `strategy.run_id`, and
  `strategy.computation_path` as `null` — while remaining `runnable`, blocker
  free, and `plannability = "rows"`. The human render uses
  `(computed at replay)` only for descendant-derived facts; unavailable facts
  render `unavailable`/`unknown`, and an ordinary plan-only run id/path render
  `not assigned`.
- **AC-047** Fingerprint tuple schema v3 has exactly 33 top-level fields.
  Nested identity structures bind canonical target repository/common-dir,
  every row execution context, ref backend, original checkout
  branch/head/detached state, and stable ordered holder
  path/branch/HEAD/action tuples. The target repository structure also binds
  the raw new-parent `requested_token`, and every cutoff structure binds its
  raw `supplied_token`. Changing the
  destination, any cutoff, any pre-image tip, the closure order, any argv
  template, either limit, either raw operator token, or any **known** metadata cell changes the
  fingerprint; changing nothing reproduces it byte-for-byte; and a `--plan`
  immediately followed by a fresh execution over unchanged state produces
  identical fingerprints even though the execution mints a run id and a
  scratch path. The post-lock equality gate remains a second defense over the
  same identities and refuses `revalidation-mismatch` before state birth.
- **AC-048** A sync fingerprint is never accepted by `--approve-plan` here, and
  a reparent fingerprint is never accepted by `tws sync --approve-plan`.
  T-035 drives both real admission gates, not only the two encoding prefixes.
- **AC-049** The fresh predicate (§7.12) and the continue predicate (§7.12a)
  are asserted as tables, including that a healthy `--continue --plan` is
  admissible under §7.12a and inadmissible under §7.12, publishes
  `approval.scope = "resume"`, `supplied = false`, `accepted = null`,
  `approval.fingerprint = null`, `covers.requires_limits = false`, and exposes
  the persisted token only as `state.approved_fingerprint`, which §3.5 rule 4
  then refuses to accept back. Its live-ref assessment is shared with
  execution: symbolic refs refuse, the four ref classes agree, and post-image
  descendants of planned tips infer the same commit point read-only. Session
  liveness and conflict state agree; owned missing compatibility and completed
  residue are automatic recovery work, not fatal blockers.
- **AC-050** A plan route without limits publishes `approval.fingerprint =
  null` and `approval.usable = false`.
- **AC-051** A plan route creates no pin, no scratch worktree, no state file,
  and no compatibility artifact; it fetches exactly where the described
  execution fetches (external default fetch, checkout default no-fetch), and
  `--continue` / `--abort` never fetch. Its reused `PlanFetch` JSON uses the
  exact sync policy-source/outcome/freshness domains, leaves no-fetch
  unsuppressed and local-only, copies complete measured repo/effect facts, and
  reports a failed contacting fetch as failed/possibly-stale with unknown
  mutation facts rather than fetched.

**Execution**

- **AC-052** Every materialized row argv contains `-c rebase.backend=merge`,
  `--merge`, `--no-fork-point`, `--no-update-refs`, `--no-autostash`,
  `--no-rebase-merges`, `--onto <oid>`, `<oid>`; `effective_backend` is the
  constant `merge`; and no argv anywhere contains `-C`. A PATH-level shim
  audits every Git process in the route, including workspace, ancestry,
  inventory and resolver probes.
- **AC-053** `row.argv` is the canonical template; materialization replaces at
  most one element — the single `<computed-tip:{parent}>` placeholder — and for
  a target row `materialized_argv == argv` byte-for-byte. The fingerprint binds
  the template, never the materialization.
- **AC-054** `rebase.updateRefs=true`, `rebase.autoStash=true`,
  `rebase.forkPoint=true`, and `rebase.backend=apply` in config do not change
  behavior; no ref outside the closure moves.
- **AC-055** No argv emitted by the reparent execution paths contains `reset`,
  `replay`, `--update-refs`, `--autostash`, `--apply`, or `push`. The push argv
  of §12.4 belongs to the push paths and is asserted separately by AC-089.
- **AC-056** External mode creates exactly one scratch linked worktree at
  `<featurePath>/.reparent/<run-id>/scratch` and removes it at cleanup step 2;
  checkout mode creates no second checkout and restores the original branch or
  detached HEAD explicitly on **both** completion and abort. Directory
  import/adoption filters skip `.reparent*`; worktree/status inventories hide
  only the exact active recorded scratch path and retain unrelated
  `.reparent*` paths when no state owns them.
- **AC-057** A descendant's destination is read only from its parent row's
  persisted `planned_new_sha`, confirmed against the parent's `new` pin, and
  never from `refs/heads/<parent>`; an injected divergence between the pin and
  the record refuses `probe-failed`. Exact approved over-limit evaluations
  persist waiver IDs/kinds and succeed fresh and on resume while the digest is
  unchanged; changed inputs refuse revalidation and newly exceeded unwaived
  evaluations refuse their limit kind.
- **AC-058** Public refs move exactly once, in one `update-ref --stdin`
  `start/prepare/commit` transaction whose every `update` carries the expected
  old value, whose rows follow `ReparentClosureOrder`, and whose stdout and
  stderr are captured and never inherited. A row whose planned tip equals its
  pre-image is represented by an ordered `verify <ref> <preimage>` line,
  emits no update, and is recorded `noop`; an all-no-op closure still runs the
  verification transaction.
- **AC-059** A concurrent change to one expected old value aborts the whole
  transaction; no ref moves; the refusal is `ref-transaction-mismatch` with
  `state-preserved`. Mandatory on the files backend; the reftable variant skips
  when unsupported (§17.3).
- **AC-060** A `reference-transaction` hook veto refuses
  `ref-transaction-mismatch` naming the hook; the hook is never disabled.
- **AC-061** Each row's computed tip is pinned **before** its validation runs
  and before any later row is computed; old pins, the destination pin, and
  every computed pin survive `git gc --prune=now` and reflog expiry until
  cleanup, across a conflict pause and a validation failure. Stage
  `pinning-computed` verifies all pins idempotently and refuses `probe-failed`
  on a pin that resolves elsewhere.
- **AC-062** A merge commit in any replay range refuses
  `merge-commit-in-replay-set`.
- **AC-063** Configured validation runs once per computed row, after that row's
  pin; a non-zero exit refuses `validation-failed` with no public ref moved; a
  command that leaves tracked modifications or untracked files absent from the
  pre-command snapshot refuses `validation-failed`, names the residue, and
  neither cleans nor stashes it. Preexisting untracked paths may remain or be
  modified without becoming validation residue.
- **AC-064** `untracked-overwrite` is never raised statically: preflight only
  warns `untracked-present`. It is raised just-in-time before a switch and
  before a holder restoration whose destination tree would clobber an untracked
  path, naming every colliding path, and no force option is ever passed. It
  does not gate the run's own state, pins, or scratch creation.
- **AC-065** A conflict pauses with the exact §9.7 text, carries no marker,
  preserves every pin; resolving natively and running `--continue` completes
  the run; `--continue` while the rebase is still in progress refuses
  `conflict-unresolved`. A non-conflict rebase failure persists
  `failure_domain: native-git`, `resume_stage: computing`, keeps the row
  pending, and prints the captured native error without a marker.
- **AC-066** The required Git floor is the existing `CapRebaseUpdateRefs`
  (>= 2.38); below it, or on an unparseable version, the run refuses
  `capability-unsupported` before mutating. An `unknown` ref backend does not
  refuse: it is treated as `files` and warns
  `files-backend-not-crash-atomic`.
- **AC-067** After the lock is acquired and before any pin, scratch worktree,
  state write, or compatibility artifact, the run re-snapshots refs, stack
  bytes, and session liveness and re-compares the approved fingerprint; an
  injected concurrent ref move or stack edit refuses
  `plan-guard: revalidation-mismatch: state-preserved:` and leaves nothing
  behind, and a newly live session refuses `session-live`. Fresh execution
  requires authoritative state absent both before and immediately after mode
  lock acquisition; crash-window-1 state is recoverable ownership and is
  never overwritten even with a dead guard and missing compatibility.
  Checkout identity
  probing treats only symbolic-ref exit 1 as detached, fails closed on other
  errors or an empty/unresolvable HEAD, and restoration never accepts an empty
  recorded identity.
- **AC-068** Checkout and every external direct/tmux/all launch publish intent
  before their final mutation check. A visible reparent guard makes the launch
  refuse before spawn/attach; a visible launch intent makes fresh or recovered
  reparent refuse `session-live`; losing/refused intents are cleaned and a
  provably dead crash intent is non-blocking to a read-only plan and is removed
  only after the workspace-global mutation lock through rooted directory/file
  identity plus exact-owner-byte comparison. Symlink/non-directory intents are
  unverifiable/live, are never followed, and never expose or remove an outside
  target. Live/unverifiable or changed intents remain blocking and are never
  deleted. Checkout `--feature-dir` retains that same intent through
  `openDirect`, after a final global mutation/reparent/session check, and
  cleans it safely on refusal or exit.

**Metadata**

- **AC-069** Target `Base` (stored canonically) and `LastBaseSHA`, every
  descendant `LastBaseSHA`, and nothing else change; entries outside the
  closure are byte-identical; `Stack.Branches` order is preserved.
- **AC-070** The exact post-image bytes and their hash are persisted at
  `building-post-image`, **before** the CAS; `stack.yaml` is then written
  exactly once, after the CAS, through `WriteStackBytesAtomic`, from those
  persisted bytes and never recomputed from live refs.
- **AC-071** Every forward metadata write first requires the live file to hash
  to `stack_sha256_before`; an edit made while the run holds the lock refuses
  `metadata-drift` and writes nothing.
- **AC-072** `durableWriteFile` fsyncs the file and the parent directory after
  rename, and every §10.3a artifact uses it. An injected failure before the
  rename leaves the previous file intact; an injected failure after the rename
  but before the directory fsync is recoverable; recovery fsyncs the parent
  before accepting visible state, post-image, abort pre-image, or compatibility
  bytes. `atomicWriteFile` and
  `SaveStack` are unchanged.
- **AC-073** No new `StackEntry` field and no new `stack.yaml` key exist.

**Recovery**

- **AC-074** Each of the 17 crash windows of §11.9 resumes correctly with
  `--continue` and with `--abort`, and each is idempotent when its verb is run
  twice. Tests assert the concrete persisted stage/artifact/ref shape rather
  than assigning only an owner function: window 2 has the complete
  compatibility envelope with no scratch/context and every row pending;
  window 15 is `aborting` with one durable restored `abort_rows[]` entry and
  another ref still at its planned tip. Window 4 includes a crash after Git
  creates conflict state but before `conflict-paused` is persisted. An
  originally detached checkout has a run-owned `original-head` pin created
  before the first switch, verified/recreated on recovery, retained through
  restoration, and removed afterward; aggressive reflog expiry and GC cannot
  strand success or abort. Stage
  `metadata-written` is spelled
  identically in state, in §11.3, in §11.6, and in §11.9.
- **AC-075** A partial commit is reconciled row-by-row after classifying
  **every** row first: pre-image rows are completed, planned-tip rows are
  skipped, no-op rows are complete only while their live ref still equals the
  shared pre-image/planned value, and a single foreign row (including a
  drifted no-op row) refuses `ref-foreign-value` before anything is written.
  The partial case prints `reparent-recovery: ref-transaction-partial:` and
  exits 0.
- **AC-076** Before the commit-point conjunction, `--abort` restores refs and the exact
  pre-image `stack.yaml` bytes and re-detaches any already-restored holder
  first, but refuses `holder-unsafe` without touching anything if the operator
  switched that holder away from its recorded branch. An exact pre-image ref
  never proves the commit point merely because it descends from the planned
  tip. For an originally detached checkout, a repeated abort recognizes the
  computation context already detached at `original_head` after an earlier
  restore-before-cleanup crash as safe and completes idempotently. Once the
  exact post-image is durable **and** every affected ref is at
  its planned tip or is a no-op, `--abort` performs **forward completion
  only**: it moves no ref, rewrites no metadata, restores holders, cleans up,
  and reports that the reparent had already committed. Operator work refuses
  `abort-foreign-value` / `metadata-drift` and changes nothing further
  **before** the commit point. Later branch movement after the commit point is
  reported informationally and cannot block forward cleanup. A
  crashed abort resumes idempotently from `abort_rows[]`, and `--continue`
  against `aborting` / `aborted` refuses `reparent-state-present` with the
  re-run-abort detail.
  Remote pre-image restoration is likewise journaled before file mutation and
  resumes idempotently after post-rename or post-remove crashes.
- **AC-077** `--continue` and `--abort` never require or accept an approval
  token or a replay limit.
- **AC-078** Cleanup releases in the §11.6a order and deletes the state
  artifact **last**; a crash at any step is repaired by re-running either verb.
  Scratch stat/remove/prune precedes pin deletion; only ENOENT means absent,
  and any other stat or prune failure preserves state, pins and locks.
- **AC-079** Reparent and sync are mutually exclusive in both modes; the sync
  pre-check runs before mode dispatch, plan dispatch, and classification, so
  `--plan`, plain, `--continue`, and `--abort` all print the exact §11.2
  sentence rather than a plan, version error, or decode error. Every mutating
  external sync route, including legacy/no-flag and recovery, then owns the
  shared feature guard, rechecks authoritative reparent state, and holds that
  guard through rebase/metadata plus optional push/remote-clear, releasing it
  last.
- **AC-080** An owned missing compatibility envelope is visible as incomplete
  recovery work, not a fatal plan blocker. `--plan --continue` remains
  admissible, `--continue` re-creates missing files only after the four §11.2a
  proofs, and `--abort` cleans up entirely from authoritative state without
  re-creating anything. Foreign or unrepairable compatibility state still
  fails closed under its ownership/corruption kind.
- **AC-081** `ReclaimCheckoutLock` takes an absent, self-owned, or dead-PID
  lock and **never** steals a live foreign lock. A crash after transferring
  the PID-only feature lock but before saving `owner_pid` is recoverable only
  through the matching dead token-bound global lock and authoritative
  run/feature/operation/state identity; a mismatch remains foreign;
  `forceAcquireCheckoutLock` is unchanged and keeps its callers. Fresh
  workspace-global reclaim uses error-aware `Lstat`: only ENOENT means no owner
  state; symlink, permission and I/O uncertainty refuse without stealing.
  When the global lock is absent, fresh acquisition scans every feature's
  checkout-sync/reparent state and refuses any recoverable record; recovery
  reconstructs the reservation only around its exact own state (plus its
  same-feature reparent compatibility transaction) and refuses another
  feature's state.
- **AC-082** Shipped v1.2.16 same-feature sync fails closed after the complete
  compatibility envelope exists, in both modes, for plain, `--continue`, and
  `--abort`. In checkout mode the
  compatibility transaction's non-integer `state_version` makes the shipped
  `AbortCheckoutSync` — which has no version check — fail at
  `LoadCheckoutTransaction`, before the lock, the rebase abort, the original
  restoration, and any deletion. The downgrade test parses `tws --version`
  into an exact version token equal to `v1.2.16`, runs all six real-binary legs
  through a Git argv shim, snapshots checkout global/feature lock
  presence/bytes, asserts no `git rebase --abort`, and pins the exact shipped
  plain-checkout refusal sentence. Tests and documentation explicitly state
  that artifact-before-compat, unrelated-feature checkout mutation,
  workspace-global locks and top-level push are invisible to that old binary,
  and prohibit using it during any active/recoverable reparent.
- **AC-083** The external compatibility artifacts are written v2-payload-first,
  legacy-sentinel-second, and the v4 payload is produced by the reparent-owned
  raw writer; `SaveSyncRunState` is neither called nor modified.
- **AC-084** While reparent state exists, `tws doctor`, `tws list`,
  `tws status`, `tws stack status`, and the checkout/external sync reports
  print one sanitized status-specific line (`active|complete|unsupported|
  corrupt|foreign|stale`) on stderr, include recovery guidance only when
  actionable, and suppress both
  `tws sync` / `git rebase --onto` repair guidance and every sync
  `--continue` / `--abort` suggestion for affected entries. They also suppress
  every `IssueSyncStateInvalid`, `state file unreadable`,
  `corrupt transaction state`, `remove it manually`, and `manually remove`
  diagnostic caused by the compatibility artifacts, while remaining strictly
  read-only.
- **AC-085** Without reparent state, `tws doctor`, `tws list`,
  `tws stack status` (human and JSON), `tws status`, and the agent status JSON
  are byte-identical to before; the `reparent` key is **absent**, not `null`,
  and `schema_version` is still 1. Stack ancestry repository resolution keeps
  its pre-feature `MainRepoRootIn`/show-toplevel-visible behavior; this feature
  changes only guidance suppression while reparent state exists.

**Remote**

- **AC-086** No provider binary or HTTP call is made on any route.
- **AC-087** Plan and success publish old → new PR base and divergence for
  every affected row, and every guidance line names `origin`. Stack-entry
  parents use Git branch names from the canonical parent refs, not logical
  entry names, including persisted records and resumed success.
- **AC-088** The pending record is written at `writing-remote-record`, after
  the new tips are pinned and **before** holder detachment and the CAS, so
  every ref the CAS moves is already covered. A crash after the record but
  before the CAS leaves a record that a pre-commit `--abort` removes/restores.
  Recovery validates ownership, refs and live stack hash before applying local
  clears on `--continue`; post-commit `--abort` preserves the current record
  byte-for-byte. Recovery
  immediately pre-CAS re-proves the required owned record or absent empty
  proof.
- **AC-089** With a pending record, all three live push paths run the
  invocation-wide preflight, warn, and use
  `--force-with-lease --force-if-includes origin <branch>`; without a record,
  their argv and output are byte-identical to today and the
  `sync_noflag/declared_c2/{push,sync-push}` goldens are unchanged. A real
  top-level external push holds the shared feature mutation guard across the
  entire multi-entry invocation, so reparent cannot enter between preflight
  and a later bare-lease push.
- **AC-090** For a pending row whose `remote_sha_at_write` is non-empty, a
  missing `--force-if-includes` capability or an unresolvable
  `origin/<branch>` refuses `remote-followup-unsafe-lease` **before any entry
  of the invocation is pushed**, exit 1, naming the failing entries, 2.30, and
  the fetch remedy. A row with no remote-tracking ref at record creation is
  initialized cleared and retains today's bare-lease new-branch behavior.
- **AC-091** `tws push --dry-run` with a pending record prints the
  `reparent-remote:` warning and
  `[~] <name> (would push --force-with-lease --force-if-includes)`, reports a
  failed preflight as a `would refuse` diagnostic, exits **0**, runs no push,
  and writes nothing. Entries absent from the record keep today's exact line.
- **AC-092** The record clears by each of the four §12.5 observations. A
  row with no observed remote-tracking ref starts cleared; a row with a
  non-empty `remote_sha_at_write` remains pending if that ref later
  disappears. A newer same-name run whose tracking ref is temporarily absent
  inherits that older positive publication evidence and remains pending while
  taking the newer topology/new tip; only a positive clear may discard it.
  Every push path evaluates and persists clears under its
  invocation lock before capability/lease preflight, then reloads; dry-run
  performs the same transition in memory only. Only real push, push-enabled
  sync, and fresh/continue reparent routes persist a clear or delete the
  emptied file; abort does not. `tws doctor`, `tws list`,
  `tws status`, and
  `tws stack status` neither evaluate nor write it, while a reparent plan may
  read it only to publish `remote-followup-pending`; no read-only route writes
  it, and a pending record alone changes none of the status surfaces.
- **AC-093** Guidance never recommends a lease bound to a freshly observed
  unintegrated remote tip and never claims `--force-if-includes` strengthens an
  explicit lease.

**Refusals and compatibility**

- **AC-094** `ReparentRefusalKinds` has exactly 49 members in the §13.1 order;
  a test asserts both, asserts that `ref-transaction-partial` is **not** a
  member, and asserts that `destination-resolver-divergent` is.
- **AC-095** `waived_kinds` is typed `[]RefusalKind` and carries only
  `limit-per-entry` / `limit-total`; state persists a `failure_domain` of
  `reparent|plan-guard|native-git|io` beside a string `failure_kind`, so a
  guard or native failure is recorded without forging a reparent kind.
- **AC-096** `holder-restore-deferred` is a warning, the warning domain has
  exactly 11 members, and a post-commit-point restore failure warns rather than
  refusing `holder-unsafe`. Deferred holders and their reasons are persisted,
  and a later `--continue` re-attempts exactly those holders.
- **AC-097** The three reparent stderr prefixes are exactly `reparent:`,
  `reparent-recovery:`, and `reparent-remote:`; guard refusals keep
  `plan-guard:`; §3.5 rules, native Git errors, and the conflict pause carry no
  marker; at most one refusal line is printed per process. Refusal details
  sanitize CR/LF, and holder deferral uses only
  `reparent: holder-restore-deferred:`.
- **AC-098** Validation order §6.3 is asserted: a fixture violating several
  rules reports the highest-ranked kind only.
- **AC-099** The `tws sync --help` snapshot and all 126 `sync_noflag` fixtures
  are unchanged; the new `tws stack --help` golden and byte-frozen legacy
  `tws stack -- reparent` output are added deliberately.
- **AC-100** README, cheatsheet, CHANGELOG, the three embedded skills, roadmap,
  and engineering-workflow are updated. The orchestrator names reparent rows
  as `target` plus `descendants[]`, never sync's `entries[]`; README carries
  one unsplit “A refusal tws already performs” paragraph; every reparent
  preview example carries the same replay limit as execution and explains
  that a limitless preview has a null fingerprint; and a docs test executes
  the documented plan → extract fingerprint → execute workflow. All eight
  surfaces state `moves no branch and writes no tws state; may fetch according
  to policy`, and no reparent-plan paragraph claims it “moves nothing”.
---

## 17. Test matrix

All Git behavior MUST use real temporary repositories, real linked worktrees,
and real local bare remotes. Mocks are permitted only for process liveness,
clock, and I/O fault injection.

### 17.1 Fixtures

| Fixture | Shape | Built from |
| --- | --- | --- |
| `reparentCustomerExternal` | `master = A-B'-D`, `pr1 = A-B`, `pr2 = A-B-C`, `pr2.Base = "pr1"`, `pr2.LastBaseSHA = B` | extend `setupCustomerTopologyExternal` (`internal/cli/sync_plan_integration_test.go`) by adding a real `pr1` entry and re-pointing `pr2` |
| `reparentCustomerCheckout` | same, one physical checkout on `pr2` | extend `setupCustomerTopologyCheckout` |
| `reparentIssue4External` | `master -> pr1 -> {pr2, pr3 -> pr5 -> pr6, pr4}` | new, from GitHub issue #4 |
| `reparentIssue4Checkout` | same in checkout mode | new |
| `reparentNoCutoff` | customer shape with `LastBaseSHA` cleared on `pr2` | variant |
| `reparentStaleDescendant` | `pr2` recorded cutoff older than `pr1`'s current tip | variant |
| `reparentReftable` | any of the above initialized with `--ref-format=reftable` | variant, skipped when unsupported |
| `reparentDecoupled` | `Name != Branch` on every row, names containing `/` | variant |

The branching issue #4 fixture is mandatory: it proves rollback, resume, and
closure ordering over a branching graph, not only a linear chain.

### 17.2 Matrix cells

| ID | Cell | Covers |
| --- | --- | --- |
| T-001 | customer topology, external, plan then execute | AC-001, AC-032, AC-033 |
| T-002 | customer topology, checkout, plan then execute; checkout/non-applicable `external_run_payload.selected == []`; checkout cross-repo gate before planning Git | AC-032, AC-033, AC-043, AC-056 |
| T-003 | issue #4 branching closure, both modes, full success | AC-069, AC-033 |
| T-004 | cutoff ladder: recorded / conflict / unresolvable | AC-022, AC-023 |
| T-005 | cutoff ladder: supplied / old-parent-tip / absent | AC-024–AC-026 |
| T-006 | non-ancestor cutoff, resolvable, refuses without rebasing | AC-027 |
| T-007 | descendant cutoff snapshot vs live ref | AC-028, AC-029 |
| T-008 | cutoff ambiguity folds to one kind | AC-030 |
| T-009 | `merge-base` source audit, scoped to §15 files and emitted argv | AC-031 |
| T-010 | destination kinds: entry, branch, tag, annotated tag, remote-tracking, full ref, full OID | AC-015 |
| T-011 | destination ambiguity: branch+tag same short name; `for-each-ref` exact-equality filter | AC-014 |
| T-012 | abbreviated OID via `--disambiguate`: 0 / 1 / many; mixed OID-and-ref token; case-insensitive hex | AC-015 |
| T-013 | SHA-256 repository: `oid_width == 64` end to end; object-format failure remains typed capability-unsupported with nullable unavailable width | AC-016 |
| T-014 | pseudo refs rejected as destination and as cutoff | AC-017 |
| T-015 | `--onto-kind` auto/entry/ref matrix, including `ref` with an entry-name token | AC-011–AC-013 |
| T-016 | canonical stored token for every literal kind; short token never persisted | AC-013, AC-020 |
| T-017 | four-resolver agreement, injected divergence, and full-ref/raw-OID stored-token collision with entry names before and after entry movement | AC-021 |
| T-018 | literal-ref root, no `--root` flag | AC-018 |
| T-019 | cycle / self / descendant destinations; post-image cycle check for literal refs | AC-019 |
| T-020 | default-branch token is not rewritten | AC-020 |
| T-021 | destination is an ancestor of the old parent → warning, still runs | §7.11 warning domain |
| T-022 | canonical common-dir identity keeps symlink/`..` alias children visible but refuses their raw-token resolver mismatch (including a decoupled Git branch); exact raw token accepted; genuine cross-repo destination refuses; unrelated same-named entry ignored | AC-034 |
| T-023 | archived target and archived descendant | AC-035 |
| T-024 | duplicate name / duplicate git branch before Git; raw destination/cutoff operator-token preservation; external workspace-root/feature-dir cwd plans; fresh external/unavailable `external_run_payload.selected == []`; missing-Git default/no-fetch plan documents and in-process execution error | AC-015, AC-036, AC-041, AC-043 |
| T-025 | `ReparentClosureOrder` determinism, declaration-order tie-break, `TopoSort` untouched | AC-037 |
| T-026 | decoupled names with `/`, pin ref id collision (`a` vs `a/b`) | §9.8, AC-036 |
| T-027 | dirty / active Git op / holder / session refusals | AC-038 |
| T-028 | holder held elsewhere, prunable holder, holder drift | AC-038, §9.9 |
| T-029 | live and undecodable tws session refusals, both session kinds | AC-038 |
| T-030 | same parent current → strict no-work with replay/capability blockers suppressed; stale → sync guidance | AC-039, AC-040 |
| T-031 | plan schema plus default/no-fetch unavailable documents for malformed stack, unknown target, cross-repo and repo-unavailable; nullable OID width and unavailable human rendering; exit 0 | AC-016, AC-041, AC-043, AC-044, AC-045, AC-046 |
| T-032 | deferred descendant facts: nulls, `runnable`, `(computed at replay)` render | AC-046 |
| T-033 | sync schema/fingerprint/refusal/warning domains unchanged | AC-042 |
| T-034 | fingerprint schema-v3 sensitivity/stability across 33 top-level fields and nested raw-token/repository/context/backend/original-checkout/holder identities; holder order canonical; plan-then-execute parity | AC-047 |
| T-035 | cross-domain token rejection through both admission gates, both directions | AC-048 |
| T-036 | both admission predicates plus continue-plan/execution agreement for sessions, conflict, compat repair, completed cleanup, refs and metadata | AC-049, AC-050 |
| T-037 | plan side-effect audit: argv log has no mutating verb; fetch exactly where declared; no-fetch/failed-fetch JSON reuses policy/outcome/freshness/mutation semantics; no fetch on continue/abort | AC-051 |
| T-038 | §3.5 order, fresh-only rules 13/14, bare continue/abort pass, route-aware arity table, argv-impossible boolean sentinel + monotonic duplicate detection, shipped message parity, and direct Cobra usage-block behaviour | AC-004–AC-010 |
| T-039 | argv template vs materialization; PATH-level audit finds no `-C` process; guarded run equals its unguarded twin | AC-052, AC-053 |
| T-040 | hostile `rebase.*` config including `rebase.backend=apply` | AC-054 |
| T-041 | forbidden-verb source and argv audit | AC-055 |
| T-042 | external scratch lifecycle; checkout no second checkout; import `.reparent*` filtering vs exact active status scratch filtering | AC-056 |
| T-043 | descendant destination read from pin + record, never from `refs/heads`; injected divergence and JIT limit revalidation | AC-057 |
| T-044 | single CAS transaction shape, order, expected old values, captured output, no-op `verify` rows, all-no-op transaction | AC-058 |
| T-045 | CAS race under files backend (**mandatory**) | AC-059 |
| T-046 | CAS race under reftable backend (skipped when unsupported) | AC-059 |
| T-047 | `reference-transaction` hook veto | AC-060 |
| T-048 | pin-before-validate ordering; GC survival across conflict, validation failure, and `pinning-computed` re-verify | AC-061 |
| T-049 | merge commit in replay range | AC-062 |
| T-050 | validation pass, failure, tracked/new-untracked residue refusal, and preexisting-untracked allowance per row | AC-063 |
| T-051 | untracked: preflight warning only; JIT refusal before switch and before holder restore; no force option | AC-064 |
| T-052 | conflict pause/native continue/reparent continue; crash after Git conflict before state reconciliation; non-conflict native failure resume | AC-065, AC-074 |
| T-053 | conflict pause then `--abort` | AC-076 |
| T-054 | capability floor 2.38, unparseable version, unknown ref backend treated as files | AC-066 |
| T-055 | post-lock re-snapshot race: ref/stack/session plus fingerprint-bound canonical repo/common-dir and holder path/branch/HEAD/action identity changes; fresh window-1 state cannot be overwritten | AC-047, AC-067 |
| T-056 | checkout branch/feature-dir plus external direct/tmux/all launch handshakes in both race orders; recovery re-probe; dead checkout intent is plan-read-only and cleaned by rooted directory/file identity plus exact bytes only post-global-lock; symlink/non-directory intent never followed | AC-068 |
| T-057 | metadata exactness, including entries outside the closure and `Branches` order | AC-069, AC-073 |
| T-058 | post-image built and persisted before the CAS; written once from persisted bytes | AC-070 |
| T-059 | before-hash check on every forward write; concurrent edit refuses `metadata-drift` | AC-071 |
| T-060 | `durableWriteFile` fsync behaviour; injected pre-rename and post-rename failures; recovery directory-fsync for visible state/post-image/pre-image/compat bytes; frozen writers untouched | AC-072 |
| T-061 | all 17 crash windows of §11.9, each verb, each run twice; concrete setup/state-shape ledger; initially-detached intent plus original-HEAD pin recreation and aggressive-GC survival | AC-074 |
| T-062 | partial commit reconciliation, classify-all-first, no-op and drifted-no-op rows, `reparent-recovery:` exit 0 | AC-075 |
| T-063 | abort before vs after the commit point; exact pre-image ancestry cannot forge commit; re-detach only a holder still on its recorded branch; originally-detached restored context is repeat-abort safe; remote pre-image restore intent survives post-rename/post-remove crashes; post-commit abort preserves current remote record; exact byte restore; crashed abort resumes | AC-076, AC-092 |
| T-064 | operator branch commit before the commit point refuses; after the commit point both verbs report it and complete cleanup | AC-076 |
| T-065 | continue/abort reject approval and limit flags | AC-005, AC-077 |
| T-066 | cleanup order, artifact deleted last, crash at each step | AC-078 |
| T-067 | sync ↔ reparent mutual exclusion, both modes, plan/plain/continue/abort, pre-check before dispatch; every mutating external sync holds the shared guard through optional push and post-lock rechecks reparent state | AC-079 |
| T-068 | owned missing compatibility is a non-blocking continuation-plan repair; §11.2a re-creation proofs; abort without re-creation; foreign/unrepairable state still refuses | AC-080 |
| T-069 | feature/global reclaim: absent/self/dead/live-foreign, transferred PID crash, mismatched token, workspace-wide orphan state with no global lock, safe own-state reconstruction, and Lstat symlink/permission/I-O refusals without stealing | AC-081 |
| T-070 | downgrade: exact parsed v1.2.16; six real-binary/frozen legs through Git argv tracing; checkout lock byte/presence snapshots and exact plain refusal; explicit artifact-before-compat/global-lock/unrelated-feature/top-level-push limitations | AC-082 |
| T-071 | external compat write order and raw v4 writer | AC-083 |
| T-072 | all six artifact statuses across human surfaces, sanitized detail, actionable-only guidance, compatibility hint suppression | AC-084 |
| T-073 | all read-only surfaces and ancestry repository resolution unchanged without reparent; `reparent` key absent; schema still 1; only active-run guidance is suppressed | AC-085 |
| T-074 | zero provider calls (PATH shim asserting no `gh`/`az` invocation) | AC-086 |
| T-075 | remote guidance content and ordering; `origin` everywhere; decoupled stack-entry parents use Git branch PR bases in plan, record and resumed success | AC-087, AC-093 |
| T-076 | record write point before holder detach and CAS; crash between record and CAS | AC-088 |
| T-077 | all three push paths with and without a pending record; top-level multi-entry push/reparent barrier race | AC-089 |
| T-078 | clear/reload before invocation-wide lease preflight on all push paths; capability gate, disappeared tracking ref, never-published branch | AC-090, AC-092 |
| T-079 | `tws push --dry-run` with/without record; in-memory clear before preflight; would-refuse diagnostic; exit 0; no writes | AC-091, AC-092 |
| T-080 | record clearing by each observation; same-name second-run missing-ref replacement preserves prior publication evidence across abort/commit/fetch/push; fresh/continue/push persist clears; read-only surfaces write nothing | AC-092 |
| T-081 | refusal domain/order, anchored prefixes, CR/LF sanitization and holder-deferral prefix | AC-094, AC-097 |
| T-082 | dedicated actual-run owner: `waived_kinds` is `[]RefusalKind` and a non-conflict native Git failure persists `failure_domain: native-git` then resumes | AC-095 |
| T-083 | `holder-restore-deferred` warning, 11-member domain, persisted deferrals, re-attempt on continue | AC-096 |
| T-084 | validation-order precedence fixture | AC-098 |
| T-085 | byte-frozen legacy `tws stack -- reparent`, collision escape/completion, stack+reparent help goldens, and closed flag set | AC-002, AC-003, AC-008, AC-099 |
| T-086 | documentation and skill surfaces | AC-100 |
| T-087 | source assertion: reparent is the only path that re-points an existing entry's `Base` as a topology operation (creation, import, and rename remain legitimate writers) | §10 |

Exactly 87 cells. Every AC-001..AC-100 is cited by at least one cell, and every
cell cites at least one AC or a named section. The executable normative table
in `internal/cli/reparent_normative_matrix_test.go` is the **only**
T→requirement→owning-function ledger. Tests MUST verify that each named
function exists. The test MUST parse this §17.2 table, expand AC ranges, reduce
annotated section cells to their named section, and compare every executable
row's requirement list exactly; a missing, extra, duplicated, or relabeled
requirement fails. A canonical table supplies the exact behavioral fact list
for every T-001…T-087 row, and the mapped owner MUST emit that row ID exactly
once with exactly those facts through function-local
`assertReparentMatrixBehavior` calls. Missing, extra, duplicated, reordered,
or relabeled markers/facts fail. Citation strings and aggregate counts of
arbitrary `t.Fatal` calls are never evidence.

### 17.3 Process and runtime budgets

- The suite MUST group cells into the eight fixtures of §17.1; a cell MUST NOT
  build a new repository when an existing fixture shape suffices.
- Pure decisions (ladder selection, refusal ranking, key order, closure order,
  fingerprint tuple, both admission predicates, argv template construction,
  argv materialization) MUST be tested as pure functions, not through a
  real-Git run.
- A real-Git scenario is every independent repository-building scenario owned
  by a normative matrix function, including scenarios inside anonymous
  closures and table iterations. Every fixture-constructor invocation counts;
  scenarios that truly share one fixture count once. Supporting tests own no
  matrix row and are tracked separately from this normative budget. The feature
  MUST NOT exceed **120** normative real-Git scenarios in total; the runtime
  counters currently measure **76 internal + 44 CLI = 120** after T-061 was
  expanded to execute every crash window for both recovery verbs. An unfiltered
  full package run MUST equal those package counts exactly; a partial
  `-run`/`-skip` selection retains ceiling-only enforcement.
- Runtime MUST be reported as **evidence, not a prediction**. The landing
  record MUST carry a measured pair — `go test ./... -count=1` wall clock on
  the same machine immediately before and immediately after the change — and
  the delta MUST be stated. No unmeasurable "+N seconds on the CI runner"
  claim is acceptable, because CI already runs a two-OS matrix with
  `-timeout 40m` and no recorded baseline exists.
- Reftable cells MUST `t.Skip` with a clear reason when
  `ReparentGitCapabilitiesForVersion(...).CapRefBackendKnown` is false or
  `git init --ref-format=reftable` fails; the files-backend twin of every such
  cell is **mandatory** and never skipped. CI pins no Git version, so a
  reftable-only assertion would silently vanish on the older matrix leg.
- Every counted real-Git scenario MUST record a non-empty `argv.log`.
  Package-central counters register the exact scenario id; every fixture-side
  Git helper, including direct/special helpers, contributes argv evidence; and
  each package TestMain fails if any counted id has no recorded Git command.
  This is in addition to route-level PATH shims that audit production
  processes, and keeps forbidden-verb audits cheap.
- Package test entrypoints MUST set `GIT_CONFIG_COUNT=0` and
  `GIT_CONFIG_NOSYSTEM=1` before the first test can spawn Git, so an
  unqualified CLI suite is hermetic even when the parent environment injects
  `safe.bareRepository=explicit`. Individual tests may intentionally override
  those values after entrypoint sanitization.

### 17.4 Required gates before landing

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Plus a manual CLI smoke in both workspace modes: plan → approve → execute →
`tws stack status` → simulated crash → `--continue` → `--abort` on a fresh run.
Do not push or tag until the landed commit is clean and approved.

### 17.4a Downgrade harness (extend, never replace)

`internal/cli/sync_downgrade_test.go` already acquires a prior binary in a
fixed order — `TWS_DOWNGRADE_BINARY`, else an offline build of the local
`downgradeTag` worktree, else a frozen replay harness — and never skips
entirely. T-070 MUST **extend** that harness, not substitute a synthetic
loader test:

- add `v1.2.16` as the reparent-era downgrade tag beside the existing
  `v1.2.15` constant, keeping both reachable;
- accept a candidate binary only when parsed `tws --version` output equals
  `v1.2.16` exactly; substring matches and dirty/suffixed versions are not
  evidence;
- run the real prior binary against a real reparent fixture for **plain**,
  `--continue`, and `--abort`, in **both** workspace modes;
- always run an executable frozen v1.2.16 route over those same artifact bytes:
  it reads the real compatibility paths, decodes them through frozen
  v1.2.16-only YAML structs/version gates, follows the old plain/continue/abort
  decision order, executes under a Git PATH shim, and snapshots artifact and
  lock bytes before/after. Hard-coded expected strings are not a fallback;
- assert the checkout arm specifically: the prior `AbortCheckoutSync` must fail
  at `LoadCheckoutTransaction` on the non-integer `state_version`, leaving the
  global and feature locks byte/presence-identical, no `git rebase --abort`
  present in the shimmed argv, `restoreOriginal` uncalled, the exact shipped
  plain refusal preserved, and every artifact still on disk;
- compare the frozen route's classified outcome with the exact prior binary
  whenever that binary is available, so the fallback remains fidelity-proven.

The harness MUST also encode the boundary it cannot prove away: released
v1.2.16 has no observer for crash window 1, the workspace-global lock,
unrelated-feature checkout reparent, or top-level external push. A documentation
test MUST require the operator prohibition against using an older binary while
any reparent is active/recoverable.

Asserting the shipped loader functions directly is a **necessary** supplement
and an insufficient substitute: the defect this guards against is a prior
binary's control flow reaching a mutation before its version check, which only
an executed prior binary can demonstrate.

### 17.5 v6 recovery and state-certification amendments

The following rules are normative refinements of §§9–12:

1. A conflict-paused row appends one run-scoped `exec git update-ref
   refs/tws/reparent/<run-id>/conflict-complete/<entry-id> HEAD` command to the
   native merge-backend rebase todo. Recovery accepts completion only when the
   rebase is absent, the computation context is clean, and that marker resolves
   exactly to `HEAD`. `rebase --abort` and `rebase --quit` never satisfy it.
2. `approved_holders[]` is immutable admission evidence, separate from
   `detached_holders[]` progress. Every continuation plan and execution attempt
   rejects a new/missing holder, changed common-dir identity, or clean detached
   HEAD that differs from the persisted detachment target. The computation
   context is subject to the same rule.
3. A dead checkout launch intent is removable only when no checkout session
   state exists. A direct or tmux state—live, stale, or undecodable—must be
   closed/recovered before cleanup or a later launch can create a new intent.
4. Checkout-global mutation admission scans both
   `<metadata>/.tws/state`-style/current and pre-upgrade sibling `state`
   directories for sync and reparent recovery artifacts, independently of the
   target feature layout, and fsyncs the global reservation.
5. Supported-version reparent state uses known-field decoding and strict
   semantic validation before any Git command, lock reclamation, or cleanup.
   The artifact must be a regular non-symlink file; paths, object ids, hashes,
   pins, refs, rows, orders, stages, argv, base64 images, holder evidence, and
   remote journals must agree exactly with the run/location schema.
6. Fresh routes resolve the target repository before loading validation
   configuration. The frozen command is the global config overlaid by
   `<target-repo>/.tws/config.yaml`, never the caller repository's config.
7. Every remote clear/delete is a state-journaled exact source→target
   transition. A recovery or continuation plan accepts only either exact image;
   the plan classifies the target read-only, while execution reconciles and
   fsyncs it before clearing the journal.
8. Cleanup fsyncs every compatibility-artifact and feature/global-lock parent
   directory after removal, including a separate legacy checkout directory.
   It removes the authoritative state last and then fsyncs its state directory.
9. If cleanup released the mutation lock and then crashed, a later push may
   legitimately clear/delete the last remote record. Recovery accepts the
   absence only after proving every state-listed pending row is published
   (tracking ref equals or contains `planned_new_sha`) or left the stack, then
   durably records `remote_record_empty` before repeated cleanup.
10. Push envelopes retain the workspace/default repository for entries whose
    recorded `Repo` is empty. A non-empty per-entry `Repo` overrides that
    default; push order never rebinds it to the first pushed repository.
11. Replay candidates are measured with exactly
    `git rev-list --no-merges --reverse <cutoff>..<branch-ref>`; count and
    digest cover that exact oldest-first non-merge sequence.
12. Every descendant remote row/record stores its unchanged logical parent's
    **Git branch** as both PR-base-before and PR-base-after. Cleared rows with a
    missing tracking ref project `no-upstream`, and warnings never render empty
    `( -> )` base pairs.
13. The just-in-time untracked gate treats component-prefix collisions in both
    directions as overwrite hazards, including symlink components, and reports
    every complete `untracked -> target` path pair before switching.

---

## 18. Out of scope (explicit, each a refusal or an omission, never an inference)

1. Multi-parent composition. One `Base` per entry remains the model.
2. Cross-repository stacks and closures. Refused, not partially handled.
3. Patch-equivalence inference as a correctness input. The existing probe may
   inform a warning; it MUST NOT select a cutoff or drop a commit.
4. Arbitrary branch import. `tws new <feature> <branch> --base <parent>`
   remains the only way to register an existing branch, and remains separate
   from this feature.
5. Provider PR mutation or retargeting of any kind.
6. `workmux` / `tss` session integration and any semantic agent state.
7. Automatic force push, and any `--push` flag on this command.
8. A `git replay` fast path, capability-gated or otherwise.
9. Adoption of a foreign or non-tws session, lock, or transaction.
10. Repairing ordinary `tws sync` rollback (`sync-transactional-abort`) or
    ordinary sync cutoff attribution (`sync-cutoff-integrity`).
11. Converting existing `SaveStack` or `atomicWriteFile` callers to the new
    byte-exact and durable writers.
12. Per-descendant cutoff overrides.
13. Interactive prompts, TTY-dependent behavior, or a weaker `--yes` gate.
14. Any remote other than `origin`, and any per-remote flag (§12.1).
15. Pseudo refs (`HEAD`, `FETCH_HEAD`, `ORIG_HEAD`, …) as a destination or
    cutoff token (§4.3).
16. Changing `TopoSort`, `Descendants`, `SaveStack`, `atomicWriteFile`,
    `SaveSyncRunState`, `AbortCheckoutSync`, or `forceAcquireCheckoutLock`.
17. Undoing a committed reparent (§11.8a). Past the commit point the remedy is
    a new reparent in the opposite direction, never an `--abort`.

---

## 19. Issue and dependency disposition

- **#1** (feature-wide inject misses slash-containing logical names) stays with
  `fix-inject-slash-worktree-discovery`. This spec independently honors the
  same fact by never using a raw `StackEntry.Name` as a path or ref component
  (§9.8).
- **#3** (scoped status scans every feature) stays with
  `scoped-status-projection`. This spec adds only additive, read-only
  projection to status surfaces, and the projection key is `omitempty` so the
  no-reparent document is byte-identical (§11.10).
- **#4** (split-base abort, stale cutoff replay) stays split across
  `sync-transactional-abort` and `sync-cutoff-integrity`. Its stack shape is
  adopted here as a mandatory fixture (§17.1), and both defects are prevented
  **within this feature's own boundary** by §5 (per-row snapshotted cutoffs,
  mandatory ancestry) and §9/§11 (whole-closure pre-images, one CAS commit,
  bounded abort). This feature MUST NOT be blocked by, and MUST NOT silently
  absorb, those two features: they are soft parents and shared foundations,
  while safe reparent meets its own stronger transaction and cutoff contract
  regardless of their implementation state.
- Hard parents (`rebase-plan-guard`, `sync-modes`, `stack-status`,
  `amend-aware-rebase`, `stack-ancestry-doctor`, `branch-name-decoupling`)
  are consumed as: plan/approval/JIT semantics, frozen modes and recovery
  conventions, ancestry projection, cutoff algebra, and `Name` vs
  `GitBranch()` identity, respectively. No new hard dependency is introduced.
- Soft parents `skill-distribution` and `tiered-skill-system` are consumed only
  by §14.4's skill updates.
