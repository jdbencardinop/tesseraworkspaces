# Changelog

## Unreleased

- **Sync modes** — `tws sync <feature>` gains three independent axes:
  `--fetch`/`--no-fetch` (input-ref policy), `--full`/`--local-only`
  (propagation policy), and `--only <entry>`/`--from <entry>` (selection scope,
  by logical `stack.yaml` name). External defaults to `fetch × full × all`,
  checkout to `no-fetch × full × all`, so **`tws sync <feature>` with no mode
  flag is unchanged in both workspace modes**
- **`no-fetch` is an input-ref policy, not an offline mode** — it forbids every
  automatic remote *input* (`fetch`, `ls-remote`, implicit remote probes) and
  reads only local and remote-tracking refs. An explicit `--push` is still
  allowed and is the only way such a run reaches the network. A base ref that
  does not resolve locally is a pre-flight refusal, never a mid-run failure
- **`local-only` never advances an anchor** — it replays selected same-repo
  parent tips into their children using the parent's current local tip, and
  never consults `origin/<default>` for an anchor. A selection that holds no
  propagation edge prints `Nothing to propagate.` and exits 0
- **A scoped run cannot move an unselected branch** — `git rebase
  --update-refs` is dropped whenever the scope is not `all`, and only selected
  entries' `last_base_sha` values are rewritten. Stale edges outside the scope
  are reported informationally and do not change the exit code. The amend-aware
  `--onto <base> <last_base_sha>` replay is preserved in every cell
- **Recovery carries the frozen decision** — external new-mode runs persist a
  v2 payload (`.sync-state.v2.yaml`, `0600`) plus a per-run guard
  (`.sync-run.lock`, `0600`), and write a legacy-shaped sentinel to
  `.sync-state.yaml` whose `failed_branch` is a nonce marker. `--continue`
  resumes the persisted scope, policy, push, and validation decision;
  incompatible flags on `--continue` are refused naming both values. Checkout
  transactions gain `state_version: 2` and the same policy keys, all additive
  and `omitempty`, so a legacy transaction round-trips unchanged
- **Incompatible combinations are refused before any side effect** — mutually
  exclusive axes, explicit `false` on a presence-only axis flag, an empty
  selector, `--continue` with `--abort`, `--abort` with a mode flag, an unknown
  or archived selector, a cross-repo entry in checkout mode, two selected
  entries sharing one Git branch, a marker collision, a live run guard, and a
  runtime-state path that is a symlink
- **A mode-flagged scoped `--push` is strict and resumable** — a `--only`/`--from`
  run pushes only the entries it selected and rebased, in selection order,
  records each accepted push in the payload before attempting the next one, and
  stops at the first rejected push with a non-zero exit while keeping its
  payload, sentinel, and guard on disk. `tws sync <feature> --continue` then
  retries exactly the entries that were never pushed: it re-rebases nothing and
  re-pushes nothing. A `scope=all` run (including `--local-only --push`), `tws
  push`, and the no-flag `tws sync --push` keep today's lenient whole-feature
  push, with its per-entry `[x] <name> (push failed)` line and exit 0
- **Checkout `--fetch` is a pre-plan refresh, not a transaction step** — it
  refreshes remote-tracking refs once, before the plan is built and before the
  transaction exists, and is deliberately not resumable: an interrupted refresh
  leaves no transaction behind, so the same command simply re-runs
- **Sync mode flags on `--continue` are refused without v2 state** — `tws sync
  <feature> --continue` carrying any of `--fetch`, `--no-fetch`, `--full`,
  `--local-only`, `--only`, or `--from` against legacy or absent state fails with
  `cannot use sync mode flags on --continue without v2 state; continue without
  them or abort and start a new run`, identically in both workspace modes. A
  trigger-free `--continue` is unchanged
- **`tws status` is marker-aware and still read-only** — it projects the real
  failed entry, the real pending and completed lists, and guard liveness through
  the prober it was already given. The marker never appears in output or JSON.
  No new issue code, no new key, and no `schema_version` bump
- **`tws import` filters the two new runtime-state files** —
  `.sync-state.v2.yaml` and `.sync-run.lock` join `.sync-state.yaml` and
  `.tws/state/`, so an imported archive can never plant foreign live state.
  Export was already allow-listed and is unchanged
- **Fix: external `tws push` and `tws sync --push` push the Git branch**
  (`entry.GitBranch()`), not the logical `stack.yaml` name. *Behaviour note:* for
  a decoupled entry (`name: work`, `branch: user/work`) the pushed ref becomes
  `user/work` — the branch that actually exists — so the push argv, the per-entry
  line, and the updated remote ref all change on the no-flag path too. The exit
  code does not: the legacy push loop already prints `[x] <name> (push failed)`
  and exits 0. Checkout-mode `tws push` is unchanged and still exits non-zero with
  `linked worktrees are not supported in checkout mode`
- **Fix: one external sync layout** — external sync and push derive the feature
  directory, the worktrees root, the state paths, and the push context from a
  single resolver instead of mixing `TWS_ROOT` and the workspace metadata root.
  *Behaviour note:* under a divergent `TWS_ROOT` the shipped code ran
  split-brain (rebasing under one root while reading and writing sync state
  under the other); it now uses one root for the whole run. Where the two roots
  agree — every healthy layout — nothing changes
- **Fix: checkout sync operates on the repository checkout** — `RepoDir` is the
  resolved workspace repository root, and a cwd that belongs to a different
  working tree (a linked worktree of the same repository) is refused with a
  clean error instead of silently rebasing the wrong tree
- **Fix: corrupt external sync state fails closed** — a `.sync-state.yaml` that
  cannot be decoded now reports the file and exits 1 for plain sync,
  `--continue`, and `--abort`. Previously plain sync dereferenced a nil state,
  and `--abort` reported "nothing to abort" at exit 0 while leaving a possibly
  live rebase in place. Nothing is deleted
- **Fix: checkout `last_base_sha` is attributed by logical name** — plan entries
  now carry `name:`, so a stack with two entries sharing one Git branch is
  attributed correctly. This applies on the no-flag path too, and is the only
  no-flag checkout transaction difference
- **Known limitations, stated honestly** — two concurrent syncs against one
  feature are still unsafe: a scoped run is guarded, but a no-flag run takes no
  lock and does not consult the guard. Downgrading to an older tws *after* an
  explicit old `--abort` is unsupported, and an older tws must not be used to
  resume a scoped checkout sync — abort it instead. The legacy
  `.sync-state.yaml` path is still **followed** when it is a symlink on a
  no-flag run, because that read is frozen: only runs carrying a mode flag, and
  runs handling v2 state, refuse a symlinked runtime-state path. The two new
  files (`.sync-state.v2.yaml`, `.sync-run.lock`) are never followed through a
  symlink by any invocation

- **Stack status** — `tws stack status <feature> [--json]` reports, for every
  entry in `stack.yaml` order, its logical name and Git branch, local head,
  configured base and parent head, the recorded `last_base_sha` verdict,
  ancestry state, materialization, dirty and in-progress Git operation, upstream
  state, and ahead/behind counts against the parent. `--json` emits one
  versioned document (`schema_version: 1`) with a stable key set, no
  `stack_state` key and no generated timestamp, so two runs over an unchanged
  repository are byte-identical
- **Ancestry is projected, never recomputed** — the report consumes the shipped
  `StackEdge` evaluator that `tws doctor` and `tws list` already use, so stack
  status can never contradict doctor for the same fixture
- **Local-only and read-only** — nothing is fetched, written, or refreshed.
  Upstream state describes the configured upstream ref exactly as it exists in
  this repository right now, and parent counts compare local commits. A fact
  that cannot be established locally is reported as `null` — never as clean,
  attached, zero, or "no upstream" — so dirty state, in-progress operation,
  upstream state, and parent counts are all nullable
- **Exit status** — 0 whenever a report was produced, including for `stale`,
  `divergent`, `missing`, `cross-repo-unsupported`, and unevaluated edges and
  dirty worktrees. A non-zero exit means no report was produced at all and
  nothing is written to stdout
- **Legacy `tws stack <feature>` is unchanged** — the same tree bytes, the same
  cycle warning, the same error strings, and the same exit code. Adding the
  child command does change two Cobra-generated surfaces, both accepted:
  `tws stack --help` now lists an `Available Commands:` section, and the usage
  block printed for a parent arity error takes the `tws stack [command]` shape.
  A feature literally named `status` stays reachable: `tws stack -- status`
  prints its legacy dependency tree and `tws stack status status` reports its
  stack status. At the parent completion position the feature list drops an
  exact `status` element, because Cobra already contributes that subcommand
  there
- **Shared worktree inventory extended additively** — it gains complete
  per-block records (`Records`, a canonical-path `ByPath` map, and an `Err`
  cause) while `Available`, `ByBranch` (keys and raw porcelain path values), and
  `Prunable` keep their exact previous behaviour on real, well-formed Git
  output. Both supplemental inventories are object-format neutral: SHA-1
  (40-hex) and SHA-256 (64-hex) object IDs parse identically and are stored
  verbatim. This makes no claim that stack ancestry itself is SHA-256-ready
- **`tws status`, `tws doctor`, and `tws list` are unchanged** for real,
  well-formed Git output and for ordinary command failures. Two deliberate
  differences: their dirty probe now runs with `GIT_OPTIONAL_LOCKS=0` and
  therefore no longer refreshes the index, and the shared worktree inventory —
  read in production only by `tws status` — now fails closed on malformed
  porcelain instead of publishing a partial map, in which case `tws status`
  reports the worktree inventory as unavailable. No schema key, issue code,
  severity, message, or exit code changes anywhere

- **Stack ancestry doctor** — one mode-independent, read-only evaluator now
  classifies every configured parent-child edge. Checkout doctor, checkout
  list, and external `tws doctor` all consume it, so the same fixture produces
  the same answer everywhere. External doctor gains a per-edge ancestry section
  it never had
- **Correct stale vs. divergent** — the recorded `last_base_sha` is finally
  read, so a parent that merely advanced while the child holds unique commits
  is `stale` (was `divergent`), a true rewrite in `tws list` is `divergent`
  (list had no such arm), and an annotated-tag base that is an ancestor of the
  child is `current` (was permanently stale/divergent). A parent reset
  backwards to a commit still inside the child's history stays `current`; that
  was already the checkout answer and is now an explicitly pinned rule rather
  than a side effect of the merge-base comparison. `divergent` is phrased as
  "the recorded base commit is no longer in the parent's history", never as a
  claim about your change
- **Honest unevaluated state** — an entry with no configured `base` is reported
  as `unevaluated` + informational instead of `missing` + warning, and a
  feature whose source repository cannot be determined produces one collapsed
  informational issue instead of flooding the output per entry
- **Archived entries are informational** — archived entries with `missing`,
  `stale`, or `divergent` edges report `info` instead of `warning`, lowering
  the checkout issue count for workspaces containing them. Exit status is
  unchanged: no ancestry finding can produce a non-zero exit in either mode
- **Actionable detail lines** — checkout doctor adds up to three indented lines
  under an entry: the reason with `last-base`/`merge-base`, the guidance, and
  an informational note when the sync path for this workspace mode would
  resolve the base to a different ref than doctor probed. Rendered prose keeps
  abbreviated SHAs, while the `git rebase --onto` repair is printed as a
  complete, runnable command naming the full base ref, the full recorded base
  commit, and the target child branch explicitly (as a bare branch name, so
  the rebase actually moves that branch instead of detaching HEAD). It is
  offered as an
  *equivalent* manual repair: `tws sync` also replays such an edge with an
  `--onto` rebase, but the exact flags differ per workspace mode, and the
  guidance no longer claims otherwise
- **Honest recovery for a missing child branch** — an entry whose Git branch
  disappeared no longer suggests `tws new`, which cannot help because the stack
  entry still exists. The guidance now offers restoring the branch from its
  remote or from a known commit (with a complete, untruncated
  `git branch <branch> <known-commit>` example), or deliberately removing and
  recreating the stack entry when no work must be preserved
- **`tws doctor` still fails closed on unusable persisted config** — an invalid
  `workspace_mode` inside a repository aborts both `tws doctor` and
  `tws doctor <feature>` exactly as before; only a directory with no Git
  repository at all falls through to the repository-less external path
- **Mode-aware base identity notes** — external mode reports only the
  `origin/<default>` mismatch its sync performs, and checkout mode only the
  literal-name mismatch its sync performs; neither mode can emit the other's
  note. External `tws doctor` surfaces these notes as uncounted informational
  issues, including for edges that are otherwise `current`
- **External doctor no longer needs a source repository to run** — it resolves
  the feature from the workspace it already resolved, so it works from the
  external workspace root or a feature directory even when ancestry itself is
  unevaluated
- **Cross-repo entries no longer print a misleading `[ref-missing]` tag** — the
  old local-repository ref probe said nothing about the foreign repository, so
  it is gone; cross-repo entries start zero Git processes and are reported as
  `cross-repo-unsupported`
- **Safer ref handling** — branch identity always goes through
  `refs/heads/<branch>`, so a same-named tag can never win; a syntactically
  valid but non-existent 40-hex base is now `missing`; recorded metadata is
  sanitized before display; and ancestry never fetches, writes, or runs Git
  outside the validated repository directory
- **Agent work status** — new `tws status [feature] [--json]` reports what tws
  knows about every logical branch: materialization, tws-launched runtimes, and
  whether anything needs attention. With no argument it always covers every
  feature in the resolved workspace, from any working directory; no field in the
  document is derived from the process working directory
- **Versioned two-axis schema** — `--json` emits `schema_version: 1` with a
  stable key set, absent values as `null` and lists never `null`.
  `runtime_presence` (`present|absent|stale|unknown`) and `agent_state`
  (`working|ready|blocked|done|unknown`) are permanently separate axes.
  `agent_state` is **always `unknown`** at this version: tws launches agents but
  does not observe their turns. Use `needs_attention`, which is authoritative
- **Hierarchical attention** — `entry`, `feature`, and `workspace` each carry
  `needs_attention > active > idle`. Attention inherits upward and never smears
  downward, while `issue_count` and `codes` stay own-scope, so a level may read
  `needs_attention` with `issue_count: 0` because a child does. Every issue has
  exactly one home in `report.issues[]`
- **Human output shows the whole verdict** — the header always carries
  `Attention: <status>` for the workspace, and every issue is printed in a block
  keyed by its own home (`Branch: <feature>/<name>`, `Feature: <feature>`,
  `Workspace:`) with its code, message, and guidance. A branch that reads
  `[!] attn` therefore never hides its remediation behind `--json`. The tail
  counts branches only
- **Exit 0 on attention** — unlike `tws doctor`, `tws status` exits 0 whenever a
  report was produced, including for stale or corrupt operational state. A
  non-zero exit means no report could be produced at all (unresolvable
  workspace, unreadable metadata root, untrusted `spaces.yaml`, ambiguous
  feature layout, unknown feature)
- **External direct session records** — external `tws open` (the default,
  non-tmux path) now writes one hidden per-invocation record under
  `<feature>/.sessions/<branch-id>/<token>.json` (`0700` directories, `0600`
  files, random ownership tokens). Records are created before the agent is
  spawned, updated at each stage transition, and removed by token on exit;
  concurrent opens on one branch are supported and individually observable.
  `tws status` never removes a record, not even a provably dead one
- **`tws close` (external) consults records before tmux** — a behaviour change.
  A live direct record refuses the close, names the live pids, kills no tmux
  session, and removes nothing. With only provably stale records, they are
  cleaned and the command then kills tmux, or exits 0 with a cleanup message
  where it previously reported `no tmux session found`. Records that are neither
  live nor provably dead (corrupt, or owned by another user) are never removed
  and never block, but are always listed before tmux is touched; when they are
  all that remains and there is no tmux session, the error names them and points
  at `tws status --json` instead of reporting a flat `no tmux session found`.
  With no records at all the behaviour is byte-for-byte unchanged
- **`tws close` (external) is now feature-name guarded** — it resolves a
  caller-supplied name under `TwsRoot()` and mutates files beneath it, so a
  registered space name is refused. Consequently a malformed or untrusted
  `spaces.yaml` now makes external `tws close` fail closed, where it previously
  succeeded
- **Feature-name guard now rejects unusable names** — `GuardFeatureName`
  validates the name before it reads the registry, so every guarded command
  (`close`, `status`, `open`, `add`, `new`, `archive`, `rename`, `delete`,
  `sync`, `export`, `import`, `template sync`, `hooks install`) refuses a path
  separator, a traversal segment such as `../outside`, or a reserved directory
  name before any path join, stat, record read, removal, or tmux call. The
  message is the resolver's existing one. `tws template sync <invalid-name>` and
  `tws hooks install <invalid-name>` therefore now exit nonzero instead of
  printing a per-feature line and exiting 0
- **External `rename`, `archive`, and `delete` refuse live records** — anything
  not provably dead (live, EPERM, or corrupt) blocks the operation before any
  Git command, rename, or worktree removal; provably stale records are cleaned
  first. tws never kills a direct process. Checkout mode never writes or reads
  direct records
- **`openDirect` no longer calls `os.Exit`** — a missing agent binary, a failed
  spawn, and a broken record store are returned as errors through `RunE` from
  all four call sites
- **Secrets are never emitted** — no checkout `lock_token`, no lock-owner
  `token`, no session `links`, no transcript, prompt, argv, or environment. Only
  an 8-character `record_id` prefix of a direct record's ownership token appears.
  A direct record's file path is an ownership token in its basename, so every
  operator-facing message, refusal, guidance string, and error renders it
  redacted as `<dir>/<record-id>*.json`
- **`gitActiveOp` now also detects an in-progress `git bisect`**, so `tws doctor`
  and `tws status` report `active_git_op: bisect` where `doctor` previously
  reported nothing

- **Rebase plan guard** — `tws sync <feature>` gains `--plan` (preview the
  exact rebase this invocation would perform: old base, new base, and a
  `candidates` count per entry, which is an upper bound and never a promise of
  what gets applied), `--max-replay-per-entry <n>` and `--max-replay-total <n>`
  (refuse before rebasing if this invocation would replay more candidates than
  the bound, for one entry or in total), and `--approve-plan <fingerprint>`
  (re-supply the 64-hex fingerprint `--plan` printed to execute the exact plan
  it described; requires at least one of the two limits on every route,
  `--plan` included — a workflow that mints or presents a limitless
  fingerprint is a documentation bug, never a shipped one)
- **`--plan` is non-mutating but not fetch-free** — it moves no branch,
  rewrites no working tree, and writes no tws state, but a plan fetches
  exactly where the run it describes fetches: an external plan fetches by
  default, a checkout plan only under `--fetch`, and `--plan --continue` never
  fetches, so `--plan --no-fetch` previews a different, fully local route. No
  workflow may claim `--plan` mutates nothing without that qualification
- **Guard refusals carry a marker; shipped refusals never do** — a guard-owned
  refusal exits `1` and writes exactly one `plan-guard: <kind>: <detail>` line
  on stderr; a detail beginning `state-preserved: ` means something on disk
  outlives the refusal. A refusal tws already performs — a dirty tree, a held
  lock, an unresolvable base, an incomplete previous run — keeps its own
  wording, exits `1`, and is never marked
- **Fix: a decoupled in-stack parent resolves in checkout mode (D1-a)** — a
  checkout sync whose base names an in-stack parent with a decoupled
  `branch:` (tws identity `StackEntry.Name` differs from Git identity
  `StackEntry.GitBranch()`) no longer fails with `resolve base <base> for
  <entry>: <error>` before any rebase; it resolves the parent's real Git
  branch and proceeds, matching external
- **Behaviour change: the same fix silently changes a colliding destination
  (D1-b)** — where a real Git branch happens to share a decoupled parent's
  *logical* name, checkout sync used to rebase onto that same-named branch by
  coincidence; it now resolves through `StackEntry.GitBranch()`, the parent's
  recorded Git branch, so the rebase argv, the `--onto` SHA, and the landed
  destination change on this no-flag path. This is the only silent
  argv/destination change in this feature, and both cells are asserted in the
  test matrix
- **Guarded recovery state downgrades safely** — a guarded run (any run
  carrying a replay limit or `--approve-plan`) persists its limits in
  `state_version: 3` recovery state, in either workspace mode, so an older tws
  release cannot silently resume it without the guard it was given. An older
  release instead refuses an external `state_version: 3` payload with
  `scoped sync state at <path> is unreadable or uses an unsupported version
  (unsupported scoped sync state version 3); inspect it and remove it
  manually — tws will not guess`, for every verb including `--abort`, and
  refuses a `state_version: 3` checkout transaction on `--continue` with
  `checkout sync transaction state version 3 is newer than 2; upgrade tws or
  remove <path>`. A **fresh** old-binary checkout run over the same
  transaction refuses earlier and separately, with the shipped
  `previous checkout-sync incomplete; use --continue or --abort` — it never
  reaches the version comparison. Checkout `--abort` is the one declared
  exception: it still aborts, because abort rebases nothing and there is
  nothing left to protect
- **Fix: `tws sync <feature> --abort` now clears a stale guard-only lock**
  (recovery cell 1) — where a killed scoped setup left only
  `.sync-run.lock` behind, `--abort` previously reported `Nothing to abort —
  no sync in progress.` while the guard file survived; it now inspects and
  clears a provably stale guard, printing `Stale sync guard from PID <pid>
  cleared; no sync state was present.`
- **Fix: `--abort` also clears a stale guard beside real legacy sync state**
  (recovery cell 7) — where a real `.sync-state.yaml` sits beside a stale or
  self-recorded `.sync-run.lock`, `--abort` previously cleared only the sync
  state and left the guard on disk; it now clears both and says so with
  `Sync state cleared; stale sync guard from PID <pid> cleared.`
- **Fix: an interrupted guarded legacy setup is now a recoverable document,
  not a silent write-off** (recovery cell 4, the backup-sentinel residue of a
  guarded legacy setup interrupted mid-write) — `--continue` now **resumes**
  that setup under its guard; `--abort` still clears it, but now says so —
  `Sync state cleared; the interrupted guarded setup's backup of the previous
  sync state was discarded.` — instead of discarding it silently; and a plain
  `tws sync <feature>` refuses but names **both** recovery verbs (resume with
  `--continue`, discard with `--abort`) instead of refusing blindly. An older
  tws release still either refuses the same residue or discards the backup
  without mentioning it
- **Controlled-path stack read is hoisted above the lock, on one arm only** —
  a `--plan` or a guarded execution now reads and sorts `stack.yaml` before
  taking its lock or fetching, so on the **legacy** checkout arm a cyclic
  stack now refuses above the lock with the shipped `build plan: cycle
  detected in stack.yaml`, and an unreadable one with the shipped `load
  stack: <error>`, in both cases leaving no lock and no transaction behind.
  The **new-mode** checkout arm is unmoved (it already refused there, inside
  its own sort), and every unguarded `tws sync` keeps its shipped order in
  both arms

## v1.2.11

- **Workspace sibling links** — `tws space add/list/show/remove` maintains
  `<spaces-root>/spaces.yaml` (mode `0600`, per-workspace `flock`) as a
  discovery registry for tool-owned learning, ticket, patching, research, and
  documentation spaces. `tws` owns the location only; it never reads, writes, or
  deletes linked content, and `tws space remove` never deletes the target
- **Two path forms** — targets inside the workspace root are stored
  workspace-relative; targets outside are stored absolute. Targets must exist and
  be directories but need not be Git repositories
- **Feature-name protection** — a registered directory can never masquerade as a
  feature: feature listings exclude it and `tws add`, `new`, `delete`, `rename`,
  `archive`, `sync`, `export`, `import`, `open`, `stack`, `inject`, `push`,
  `decide`, `doctor`, `template sync`, and `hooks install` refuse it. Ownership
  is decided by filesystem identity, so a hand-edited absolute path inside the
  workspace root, a symlinked spelling, or a different letter case on a
  case-insensitive volume names the same protected directory. `tws delete` also
  refuses when a registered target lives inside the feature — naming the exact
  scope-qualified `tws space remove` command for each blocker — and
  `tws rename feature` rewrites relative entries while refusing pinned ones
- **Scoped listing and selectors** — `tws space list` always prints
  `Workspace: <root> (mode: <mode>, scope: <scope>)` before its results,
  distinguishes an empty registry from filters that hide every entry, and
  documents that a bare list is cwd-scoped while `--all` is the complete view.
  `tws space show` and `tws space remove` accept `--workspace` alongside
  `--feature` so an entry is always reachable when both scopes share a name
- **`tws migrate-layout` refuses to move a registered space directory**, or any
  legacy feature directory that still contains a registered target — the refusal
  names every blocker with its scope and its exact `tws space remove` command,
  and says the link can be re-added once the migration is done. Nothing is
  rewritten and nothing is moved; `--all` is all-or-nothing rather than
  producing a partial migration
- **Strict failure on untrusted spaces metadata** — an unreadable, symlinked,
  malformed, unknown-field, or future-version `spaces.yaml` makes every command
  that consults workspace features or spaces exit nonzero having changed
  nothing; only shell completion degrades to no candidates
- **Breaking (exit status):** `tws template sync` and `tws hooks install` now
  report failures on **stderr with a nonzero exit** instead of printing to stdout
  and exiting 0. Affected paths: `template sync` with no feature and no `--all`;
  `hooks install` when the workspace cannot be resolved, in checkout mode, or
  with no detectable feature. Success output, per-feature progress lines,
  `No features found.`, and the best-effort per-feature error lines of `--all`
  are unchanged
- **No-op when absent** — with no `spaces.yaml` nothing is created (no root, no
  marker, no lock, no temp file) and every pre-existing command keeps its exact
  behaviour

## v1.2.0-rc.1

- **Configured-base checkout** — new branches now start from the requested local branch, remote ref, tag, or commit SHA; omitted bases use the selected repo's `origin/HEAD`
- **Feature-directory repo inference** — `tws new` can infer a single source repo from existing feature metadata/worktrees and fails clearly on multi-repo ambiguity
- **Correct sync continuation** — `tws sync --continue` resumes deferred descendants, preserves state on later failures, and only reports completion after stack ancestry is current
- **Decoupled branch validation** — sync validates the actual Git branch (`StackEntry.GitBranch`) while retaining short tws names for paths and output
- **Feature-level open** — `tws open <feature> --feature-dir` opens the orchestrator directory; `--all` creates a tmux session for the feature and its worktrees
- **P0 integration tests** — real temporary Git repos, remotes, worktrees, explicit refs, conflict continuation, and prefixed branch names

## v1.1.1

- **Feature-level open** — orchestrator directory and tmux `--all` mode

## v1.1.0

- **Directory-mirrored inject routing** — `inject/dev/file` lands at `worktree/dev/file`
- **Branch-name decoupling** — short worktree names may map to policy-compliant Git branches via `branch_prefix`
- **Default branch detection** — omitted bases detect `origin/HEAD`
- **Atomic rename** — Git operations succeed before stack metadata is mutated

## v1.0.0

- **Auto-read decision hooks** — Claude Code SessionStart hooks with optional `auto_hooks`
- **Tiered skills** — global, feature-orchestrator, and worktree guidance
- **Cross-worktree decisions** — broadcast, targeted messages, and read tracking

## v0.7.3

- **Decision read tracking** — `tws decisions show` defaults to unread only, `tws decisions ack` marks all as read, `tws open` shows unread count

## v0.7.2

- **Tab completions** — all commands now provide feature/branch suggestions via `tws completion zsh/bash/fish`

## v0.7.1

- **Template `--template` flag** — `tws add --template <dir>` copies external templates into inject/
- **`tws template sync`** — backfill templates into existing features
- **Quick start** — registered for next release

## v0.7.0

- **Targeted messaging** — `tws decide --to <branch>` for directed decisions, `tws decisions show --mine`
- **Cross-worktree decisions** — `tws decide` and `tws decisions` for broadcasting design decisions

## v0.6.0

- **Context injection** — `inject/` directory with symlinks into worktrees, `tws inject` command
- **Health checks** — `tws doctor` for branch consistency, dirty worktree, and inject validation
- **Workspace templates** — `~/.config/tws/templates/` auto-populate inject/ on `tws add`
- **Rename** — `tws rename feature/branch` with stack.yaml ref updates

## v0.5.0

- **Config command** — `tws config show/set/get` with `--repo` flag
- **Close command** — `tws close` kills tmux sessions, stale session warnings
- **Bug fixes** — cwd after agent exit, auto-select messaging

## v0.4.0

- **Pluggable agent** — `agent_command` in config, Claude/OpenCode/Aider/Codex support
- **tmux-free mode** — direct exec by default, `--tmux` opt-in
- **Archive** — `tws archive` with smart sync, archived vs missing detection

## v0.3.0

- **Cobra migration** — proper flag parsing, auto-generated help, shell completions
- **Skill distribution** — `tws init` installs Claude + Copilot agent skills
- **Interactive picker** — `tws open` with fzf/numbered list, 0/1/2 arg modes
- **Divergent stacks** — DAG support formalized with tests
- **Per-repo config** — `.tws/config.yaml` overrides global config

## v0.2.0

- **Delete** — `tws delete` removes feature and worktrees
- **Versioned builds** — `--version`, `--help`, ldflags from git tags
- **Fix claude session** — auto-detect existing sessions for `-c` flag
- **List** — `tws list/ls` shows features and branches
- **Checkout existing** — `tws new` with existing branches, `--force` for checked-out
- **Lint fixes** — golangci-lint errcheck compliance

## v0.1.0

- **Initial release** — `tws add`, `tws new`, `tws open`, `tws sync`, `tws stack`
- **Configurable workspace** — TWS_ROOT env, global config, repo-relative sibling default
- **Auto-detect** — workspace and worktree detection with `.tws-workspace` marker
- **Git worktree** — direct `git worktree add`, no worktrunk dependency
- **Stacked diffs** — `stack.yaml` with dependency tracking and topo-sorted sync
