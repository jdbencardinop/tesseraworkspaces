# Scoped push: implementation map

## Baseline and verified paths

Base: `38a7baf6981c305f2f3c1cbe23b1db3406ee90e0`, the validated
documentation-only rc.7 handoff over released `d38b442`. The following paths
and symbols exist at that base.

- `internal/cli/push.go`: `pushCmd`, `pushFeatureSerialized`, `pushEntries`,
  `pushScoped`, `externalPushDefaultRepo`, `pushRepoDir`, `pushArgv`,
  `transactionalPushArgv`, `pushFeatureCheckout`. Add new-route dispatch, not
  a filtered legacy loop; preserve no-selector behavior/checkout refusal.
- `internal/sync_selection.go`: `ResolveSyncSelection`,
  `ResolveSyncSelectionFromOrder`, `SyncSelection`, `SyncRunPolicy`.
  Reuse logical membership/validation. Rebase roles/completion do not gate
  standalone publication. Preserve existing sync diagnostics.
- `internal/cli/sync_modes.go`: `externalSyncLayout`,
  `resolveExternalSyncLayout`, selection admission and post-claim reparent
  recheck. Keep one layout and pre-fallback pending-name checks.
- `internal/workspace.go`: `RequireWorkspace`,
  `ResolveExternalWorkspaceAt`, `inferExternalRepoRootWith`. Owner inference
  can scan unrelated worktrees; new scoped resolution must be narrow,
  especially from feature-root cwd with absent selected worktrees.
- `internal/cli/feature_rename.go`,
  `internal/feature_rename_admission.go`: retain selected/actual-root
  admission, supported ambient aliases and current mutation ordering.
- `internal/sync_transaction.go`: `SyncPreparePublication`,
  `SyncBeginPublication`, `SyncFinishPublicationAttempt`,
  `SyncTransactionEntryPublished`. Durable ordered-publication reference
  behavior, not containers for a standalone operation.
- `internal/sync_run_state.go`: `ClaimSyncRunGuard`,
  `ReclaimSyncRunGuard`, `ReleaseOwnedSyncRunGuard`. A stale guard with no
  sync payload can be reclaimed; add real publication admission rather than
  forging a sync payload.
- `internal/checkout_sync.go`: `CheckoutMutationLock`,
  `AcquireCheckoutMutationLock`, `ReclaimCheckoutMutationLock`,
  `checkoutWorkspaceRecoverableStates`, `finalizeTransaction`,
  `finalizeCleanup`, `reparentCheckoutPushEnvelope`. Recognize typed push
  ownership without sync adopting it or reclaiming it as an orphan.
- `internal/feature_rename_plan.go`: whole-feature pending recovery inventory.
  Include this operation's real state where required, without generic eager
  workspace discovery around existing commands.
- `internal/feature_rename_fs.go`, `internal/feature_rename_state.go`:
  no-follow/no-replace/durability patterns. Reuse suitable primitives, not
  the rename schema or a copy of the complete protocol.
- `internal/reparent_remote.go`: `ObserveReparentRemoteClear`,
  `EvaluateReparentRemoteClears`, `BuildReparentRemoteClearTransition`,
  `ApplyReparentRemoteClears`, `PrepareReparentPushEnvelope`,
  `ReparentPushEnvelope.PersistClears`, `ReparentPushDecision`.
  Add selected-row projection/clearing over the full valid stack/record;
  preserve old all-row callers and avoid unrelated repository probes.
- `internal/reparent_state.go`: `ReparentPushMutationRefusal`; retain it and
  the under-reservation recheck. Existing external `pushScoped` and checkout
  `reparentCheckoutPushEnvelope` are directly dependent scoped-sync callers.

The standalone plan/state/executor and focused tests need new files under
`internal/` and `internal/cli/`; those are proposed additions, not existing
paths. Prefer a small cohesive implementation, not a generic transaction
framework. Document concrete state paths/version and lock ownership chosen
during implementation before freezing the reviewed candidate.

## Existing test evidence and required additions

- `internal/sync_selection_test.go`: membership, order, archived selector/
  closure, cross-repository rejection, duplicate refs and logical/Git identity.
- `internal/cli/sync_scoped_test.go`:
  `TestSyncScoped_SelectedPushUsesGitBranch`, conflict/recovery scope,
  preservation of unselected roots.
- `internal/cli/sync_push_resume_test.go`:
  `TestSyncPush_FailureAfterOneSuccessIsResumable`, real pre-receive rejection,
  durable selected success and remaining-only retry. Later tests supersede
  old comments describing scope-all leniency.
- `internal/cli/sync_transaction_test.go`,
  `internal/cli/sync_validation_test.go`,
  `internal/cli/checkout_sync_modes_test.go`: publication cutoff, integrity,
  global exclusion and unchanged legacy routes.
- `internal/reparent_remote_test.go`: existing follow-up/lease behavior.
  Extend selected-only projection/clearing and invalid whole-record refusal;
  use real remotes for actual inclusion/lease outcomes.
- `internal/cli/feature_rename_test.go` and related route tests: selected-root
  fallback and admission-through-effects regressions.
- `internal/cli/sync_plan_docs_test.go`,
  `internal/cli/named_template_conflict_docs_test.go`,
  `internal/cli/feature_rename_test.go`: release/roadmap prose, milestone
  ordering and forbidden sync flag literals in planning documents.

Parent evidence `scoped-push-lease-probe.json` in session files shows explicit
expected-OID leases disable the inclusion flag. Official Git `remote.c` at
v2.55.0 confirms qualifying reflog-range semantics; it was inspected, not copied
into the product. Implement a conservative independent proof with real-Git
accept/refuse comparisons, not an argv-only assertion.

## Implemented candidate protocol

The candidate is implemented directly from this base, with no provider-driven
recipe or historical artifact regeneration:

- `internal/cli/push_selection.go` owns pure flag validation, selected layout,
  silent logical completion and narrow owner proof. It never uses the
  cross-feature `RequireWorkspace`/external owner-inference fallback. Existing
  `ResolveSyncSelection` owns membership/order; recovery never resolves a
  selection against the live graph. Full selected metadata is compared without
  probing unselected repositories or worktrees.
  The shared layout resolver now checks the execution-selected namespace's
  pending/untrusted publication before any stack-existence fallback, including
  legacy push, sync and reparent preview/execution callers. Selected-name
  admission repeats that local-only check after acquisition; scoped publication
  repeats it through its effect verifier, and legacy push/sync recheck after
  claiming their actual feature guard. Own-journal standalone recovery retains
  its explicit journal-first path instead of being mistaken for a fresh run.
  Recovery now classifies at most the selected and configured candidates,
  validating physical journal locations before considering terminal status.
  Selected pending state keeps priority; matching pending fallback state beats
  a terminal selected receipt. Selection/alias bindings are still checked,
  and a terminal receipt grants no publication or scope-selection authority.
  Supported external rename may carry terminal bytes under a new feature name.
  Terminal-only validation accepts that history by exact physical workspace
  and feature/state-directory identities, without trusting dead historical
  feature paths. Pending bindings remain strict, and copied receipts beside a
  different feature inode refuse. Terminal durability uses the current verified
  parent while preserving historical bytes.
- `internal/scoped_push.go`, `scoped_push_git.go`, `scoped_push_state.go` and
  `scoped_push_guard.go` implement publication only, not a sync transaction.
  State kind is `tws-scoped-publication`, version **1**. External state is
  `<feature>/.push-state.v1.json`; checkout state is
  `<repo>/.tws/state/<feature>-push.v1.json`.
- The JSON snapshot contains an immutable plan plus a plan digest, monotonically
  increasing revision, ordered row outcomes and exact private-ref progress.
  Unknown/duplicate/trailing fields, unsupported versions, unsafe refs/paths,
  inconsistent phase/order, missing inclusion evidence and foreign identity
  refuse. Frozen topology is validated by the shared selector, not another walk.
  State/guard reads use no-follow/nonblocking regular-file opens and compare
  inode/content; atomic writes fsync the file and parent chain. Visible writes
  whose durability fails stop the run; recovery repeats those barriers.
- Phases are `preparing`, `publishing`, `cleaning`, `canceling`, `completed`,
  `canceled`. Row outcomes are `pending`, `intent`, `succeeded`, `skipped`.
  Intent is durable before every network attempt; success is durable before
  considering the next row. No selector is accepted on recovery.
- External publication holds the **existing `.sync-run.lock`**, using its real
  owner token/PID, and both selected-name and actual-root rename admissions.
  Checkout holds the **existing `checkout-mutation.lock`**, with the narrowly
  typed `operation: publication`, exact token, feature and state path.
  No sync/reparent compatibility payload or Git lock is fabricated. A
  validated pending publication journal blocks fresh/recovery sync, reparent,
  lifecycle/rename and launches even if the process/lock disappeared; checkout
  workspace scans recognize publication state. Ordinary create/delete mutations
  share the existing metadata reservation around branch/stack/removal effects.
  Unrelated external features and already-running agents are not inventoried or
  stopped.
- Physical roots, common directories and original root-alias observations are
  frozen and checked. Owner proof is restricted to explicit selected entry
  paths, matching configured workspace mappings, conventional owning roots,
  verified controlling context, or selected worktree evidence. Ambiguous
  default owners refuse; an unrelated caller's repository is never borrowed.
  A selected Git directory must also be the actual working-tree or bare
  execution root. Nested authored/imported `repo` contexts refuse rather than
  re-anchor a relative origin at the wrong directory. Normal, linked, bare
  and explicit separate-git-dir working-tree roots are covered; non-bare
  administrative directories refuse. The original root spelling and alias
  identity remain frozen; no new publication schema or broad repository
  normalization was introduced.
- Each eligible entry freezes one `refs/heads/<GitBranch()>` destination and a
  source commit OID. Supported origin tracking mappings are the conventional
  wildcard or an unambiguous exact ref mapping into `refs/remotes/origin/`.
  Multiple endpoints, negative/unsupported mappings or differing effective
  fetch/push URLs refuse. Only a SHA-256 endpoint/config fingerprint is stored;
  credential-bearing URLs and raw Git stderr are not journaled or previewed.
  The resolved URL must also be invariant under another Git URL expansion:
  `get-url` expands once, while passing its result to `push`/`ls-remote` could
  otherwise expand it again. Non-identity rewrite rules matching that pinned
  endpoint refuse before state publication; a real-Git regression covers the
  named-remote/resolved-URL double-rewrite case.
  A configured `remote.<resolved-endpoint>.*` section also refuses, since Git
  may treat that literal endpoint as a remote name with unrelated url/pushurl
  values. This is checked during planning and immediately before push or
  uncertainty reconciliation, without printing endpoints or credentials.
  Endpoint-named `remotes.<endpoint>` groups also refuse before each of those
  effects: Git can otherwise dispatch multiple targets and reinterpret an
  absence lease. Native `git config --get-all` key lookup matches Git's variable
  normalization, including `.GIT` endings, rather than a case-sensitive regexp.
  Local `..` paths are checked component-by-component before lexical cleanup:
  every traversed prefix must be a real directory, never a symlink or missing
  object. Both relative and absolute ambiguity refuse; ordinary
  `../remote.git` remains supported without contacting the remote.
- Publication sends exactly one pinned OID refspec and explicit expected-value
  lease. Missing tracking grants expected absence. Git argv/config overrides
  suppress mirror/all/tags/follow-tags/prune and recursive submodule publication
  while leaving user hooks enabled. No claim is made about arbitrary hook effects.
  Every local Git probe disables lazy fetch and replacement objects. The new
  standalone route requires Git **2.45+** and passes the global
  `--no-lazy-fetch` option, not only its environment-variable equivalent:
  older Git can ignore an unknown environment variable and lazily fetch.
  Public `git.c` sources at v2.43/v2.44 lack that option; v2.45, v2.48 and
  v2.55 have it. This was read-only research, not copied implementation.
  The selected follow-up seam has a separate enforced-local projection for
  standalone publication, leaving older legacy/sync Git paths unchanged.
- Every pending raw reparent row requires a frozen independent inclusion witness,
  even if a local observation could otherwise clear it. This is intentionally
  conservative. Branch reflog NEW tips are considered newest-first through the
  first tip older than the latest tracking-ref reflog timestamp; the expected
  remote OID must be reachable from a tip in that range. The first older tip is
  included; unrestricted historical tips grant no authority. Missing evidence
  refuses. No inert extra `--force-if-includes` flag is used with the explicit
  lease, and old legacy/sync bare-lease argv is unchanged.
- Token-owned `refs/tws/push/<token>/<row>/{source,lease,witness,followup}` protect needed
  objects without branch/index/worktree edits. Private-ref creation/deletion
  intent and completion are journaled; collisions/mismatched values refuse.
  `followup` retains the record's rewritten tip even when an amend makes it
  unreachable from the source. Before releasing any protection, cleanup
  persists per-entry `followup_decisions` (`keep` or `clear`) and never repeats
  that ancestry decision after refs can be deleted. Replay therefore survives
  actual GC after a ref-delete/fsync interruption without requiring the old
  rewritten commit. The v1 schema stays independently typed; active pre-release
  plans missing required protection refuse, while fully validated historical
  terminal receipts remain readable for owned retirement.
  Successes are never republished. For an uncertain row only its recorded remote
  ref is read: source confirms success, original expectation permits the same
  retry, any other value refuses. New tracking evidence never refreshes authority.
- Cancellation performs no network operation and abandons remaining publication.
  It preserves confirmed/uncertain outcomes, clears only proven selected
  follow-up results, and removes only exact-owned private refs/reservations.
  **Terminal receipts remain** after completed/canceled cleanup, granting no
  publication authority. This avoids an unrepeatable final unlink/fsync
  gap; a continuation can repeat terminal durability/lock cleanup. A later fresh
  scoped run may supersede only a verified terminal receipt for the same
  physical feature. Checkout rename/delete and one/all layout migration retire
  verified receipts into `.tws/state/publication-history/<token>.json` before
  removing or relocating their feature. Exact bytes/inode are hard-linked into
  history and synced before the live name is unlinked and its parent synced.
  Interrupted retirement may leave two links to the same owned inode; a copied
  file, foreign source/destination, or history collision refuses. History is
  not rolled back if a later lifecycle effect fails. No foreign terminal file
  beside a newly created inode is accepted as authority.
- `internal/scoped_push_lifecycle.go` reuses the actual checkout mutation lock
  with the narrowly typed `operation: metadata`, plus existing per-feature
  lifecycle locks. Its owner-state path is an explicit metadata reservation
  hint; no sync/reparent payload is written. Workspace recoverable
  state is rechecked under the guard and at movement/removal, covering missing
  and stale publication guards as well as late state appearance. Source
  identities and destination absence are checked before exclusive, no-replace
  moves; migration holds exclusion through its complete one/all operation.
- Checkout directory moves additionally write narrow versioned intent to that
  owner path, `.tws/state/.checkout-metadata-<token>`, before the syscall:
  kind `tws-checkout-metadata-moves`, version **1**. The evidence binds physical
  workspace and source/destination parents, the exact moved feature inode,
  ordered move/reversal intent, and the registry preimage. Safe regular-file
  snapshot/identity checks and durable file/directory writes apply; physical
  state-directory aliases are normalized without changing caller input.
  `MoveFeature` returns actual reconciled movement separately from errors.
  A post-syscall/pre-fsync fault triggers repeated barriers and exact no-replace
  compensation under the held reservation, never an assumption of no effect
  or a swallowed failure. Batch migration includes the current moved item in
  reverse-order rollback. Only coherent registry/layout completion or exact
  restoration to every first source permits completion and owned cleanup.
  Both feature-parent barriers and the registry's workspace-root directory
  barrier precede publication of a completed movement receipt.
  If compensation/durability remains unresolved, the shared workspace guard
  and real movement evidence remain, including after process death; scanners
  refuse pending evidence even if its guard is missing/stale. Foreign paths
  are never overwritten. Operators must preserve/reconcile the recorded
  directory and registry evidence before further mutation. No generic metadata
  recovery framework or publication recovery verb was added for these moves.
  Failed evidence re-reads retain the last observed ownership snapshot instead
  of converting it to apparent absence; missing/FIFO replacement regressions
  prove that an already-moved item cannot silently release its reservation.
  Shared checkout launch preconditions inspect these same files with the
  existing movement-evidence validator, both for branch/feature-directory
  admission and at the final check under session intent. Pending/untrusted
  evidence blocks direct/tmux agents and shells even without the shared guard,
  before branch switching or context links. Validated completed receipts remain
  nonblocking. This adds no owner discovery or separate journal framework.
- `PrepareSelectedReparentPushEnvelope` and selected clear-transition helpers
  validate the whole follow-up record and use the whole stack for existence
  semantics, but probe/change selected rows only. Existing scoped external and
  checkout sync publication/retry/cleanup use this seam with their own frozen
  selection; unscoped/non-publication callers retain all-row behavior.
  External scoped sync now chooses an available verified selected worktree, or
  the corresponding transaction-bound repository with matching common dir,
  rather than blindly choosing a missing selected anchor. Selected follow-up
  without a repository context refuses instead of falling through to cwd.

No cross-ref/cross-repository atomicity, arbitrary-writer snapshot isolation,
portable inode-CAS, remote rollback, or universal old-binary blocking is claimed.
Use the newer binary consistently while a publication remains pending.

## Historical v1 implementer validation handoff

Implementer: OpenAI `gpt-6-astra`; exact base and branch remain unchanged.
Focused standalone/affected sync, reparent, rename, lifecycle, admission and
documentation tests passed. Final vet, golangci-lint (a new owned cache),
build, whitespace and dependency-DAG gates passed. No dependencies were
installed. No commit, record, land, push, tag or nested delegation occurred.

The complete final `go test ./... -count=1 -timeout 40m` ran from
08:01:22Z to 08:20:46Z on 2026-10-30. It failed only these five existing test
families, reproduced unchanged from an exact-base `git archive`:

- `TestRunReparentExternalSuccess` and `TestReparentHolderLifecycle` treat
  the word `push` in the mandated worktree-local fixture path as a forbidden
  Git verb, even for `git worktree add --detach`.
- `TestStackReparentCLI_PlanFailuresAreDocumentsAndIdentityGatesArePure` has
  a filesystem-only missing-Git fallback that sees the enclosing checkout.
- `TestStackReparent_HumanPlanGolden` and
  `TestStackStatus_ExistingCommandsUnchanged` encounter path truncation/
  normalization failures caused by the long mandatory fixture prefix.

The initial full run also exposed normal Git discovery escaping scratch repos;
a `GIT_CEILING_DIRECTORIES` limited to the owned scratch root removed those
extra-context failures without changing Git config, tests or execution budgets.
The two coupled refusal regressions (symlink diagnostic compatibility and
checkout delete's sibling-space guard ordering) were fixed and revalidated.
No unrelated status timeout, golden, forbidden-verb assertion or invariant was
weakened to obtain a green result.

The worktree-only filesystem constraint prevented using the short, non-repository
fixture root these baseline tests assume. Full-suite green was therefore a
v1 implementer handoff blocker pending parent validation in its ordinary
environment. State was left `implementing`; `apply --mode done` did not falsely
record successful local full validation.
The complete report, source hashes, gate logs and exact-base reproduction are
in sibling `scoped-push-implementer-evidence/`, especially
`candidate-report.json`, `candidate-manifest.json`, `full-tests-final.log`,
`validation-blockers.json` and `exact-base-fixture-blockers.log`.

## Parent v1 validation and authorized v2 revision

The parent resolved that environment-only blocker without source/test changes:
ordinary-environment v1 full tests passed in **1131.295 seconds**, and vet,
lint, build, whitespace and DAG gates were green with manifest
`e1e7053d4ce82e15fed4707c089ac2f03b904a8b36f79b83ac917be9130575ef`
unchanged. The historical nested-scratch failure receipt above remains true;
it is not an outstanding product or baseline-test defect.

This revision addresses only parent audit P1/P2/P3:

1. Selected pending/untrusted publication cannot be bypassed by removing the
   selected stack and falling back to another same-named feature. Permanent
   cases cover genuine partial publication, missing metadata, unsafe journals,
   late state appearance during admission, read-only routes and own recovery.
2. Publication proves Git's execution root before accepting a repository
   binding. The relative-origin regression observes both the actual remote
   and a distinct decoy; nested contexts refuse, then canonical-root publication
   succeeds. Root aliases, linked roots, bare roots and separate-git-dir
   working-tree roots retain focused coverage.
3. The actual sync push flag now says it publishes the selected scope, or all
   feature branches when unscoped. The exact help fixture is updated; its
   byte-for-byte assertion and execution behavior are unchanged.

Only focused/affected tests and vet/lint/build/diff/DAG are run by the v2
implementer. No second constrained full suite, baseline relaxation, production
budget change, record, commit or release is authorized. Parent v2 full
validation/reviews remain pending; state stays `implementing`.

## Parent v2 review and authorized v3 revision

All three reviewers verified manifest
`0f8e835f2f0eeff22845b44486b54e6b6c42a08d57c63a517d75fb840c0aa20f`
before and after review. UX approved scope; state/integration and parent audit
requested P4, S1/S2/S3 and I1/I2/I3, addressed by the protocol changes above.
Parent's ordinary v2 full suite failed only G1, a new test fixture assumption:
its late-admission hook compared physical `/private/var` with raw `/var`.
The expected fixture root is now resolved for matching, while original alias
inputs, `restored == true`, refusal, and no-effects assertions remain intact.
No production comparison, timeout, budget or baseline assertion was relaxed.

V3 adds real amend/reflog-expiry/GC and cleanup-interruption coverage; named
remote and symlink/parent-traversal endpoint controls; pending one/all migration,
stale/missing/late state exclusion; verified terminal history, name reuse,
foreign-copy and durability cases; available selected sync context and retry;
and terminal-receipt shadow recovery/preview/cancellation. The parent independently
reproduced the original issues through its expanded 33-case CLI harness.
Only focused/affected tests and vet/lint/build/diff/DAG are implementer gates.
Parent owns fresh ordinary full validation, independent smoke and re-review;
state remains `implementing`, with no record, commit, landing or publication.

The v3 implementer (`gpt-6-astra`) passed the broad affected CLI selection
including no-flag goldens (278.850s), final scoped/docs selection (167.412s),
and focused internal migration/follow-up/guard selection (14.778s). Vet,
golangci-lint with a new owned cache, build, whitespace and DAG gates passed.
Receipts and exact changed paths are in `candidate-report-v3.json` and
`candidate-manifest-v3.json` under the implementer evidence directory.
No v3 full-suite result is claimed; parent ordinary-environment gates and the
same three independent re-reviewers remain authoritative before landing.

## Parent v3 gates and authorized v4 revision

The parent subsequently ran ordinary-environment v3 full gates **green**:
`go test ./... -count=1 -timeout 40m` completed in **1211.213 seconds**,
vet/lint/build/diff/DAG passed, and **33/33** independent actual CLI cases passed.
The reviewed parent manifest was
`7eadb8138c56bb84b172a4009e84746fc9f656c50a217af0daed78d653550300`.
These results resolve the preceding pending-gate statement historically; no
nested-scratch baseline relaxation or new full-suite run was needed.

V4 is bounded to the three consolidated remaining findings:

1. Reject endpoint-named remote groups with native Git config-key semantics,
   during local planning and every pre-effect/recovery check. Fresh, late-change
   and uppercase-variable cases protect even an expected-absence lease without
   depending on which installed Git version dispatches multi-remote pushes.
2. Distinguish directory movement from durability. Checkout rename and one/all
   migration reconcile exact inode location, compensate under exclusion and
   retain original failures. Deterministic post-syscall/pre-fsync tests cover
   registry coherence, current-item batch rollback, successful retry and a
   foreign source preventing compensation. Unresolved effects retain typed
   movement evidence and the actual workspace guard, not a fake sync payload.
3. Recognize externally renamed terminal history only by verified physical
   workspace/feature identity. It cannot hide matching pending fallback
   continuation, read-only recovery preview or cancellation; copied foreign
   history remains rejected.

The implementer uses `gpt-6-astra` on the same base, leaves state `implementing`,
and runs focused regressions plus short gates only. Parent owns the final
ordinary-environment full suite, expanded 38-case CLI harness and exact-manifest
re-review. No record, commit, land, push, tag or nested delegation is performed.

V4 implementer gates passed: the broad affected selection, including no-flag
goldens, completed in 190.229s (internal) and 359.157s (CLI). After final movement
persistence and documentation changes, the targeted revision/lifecycle/help
selection passed in 1.884s and 89.027s. `go vet ./...`, `golangci-lint run ./...`
with a new owned cache (zero issues), `make build`, whitespace and DAG validation
passed. Missing/FIFO evidence replacement and registry-root durability ordering
retain conservative exclusion and do not weaken baseline assertions or budgets.
The complete report, exact paths and receipts are in `candidate-report-v4.json`
and `candidate-manifest-v4.json` in the implementer evidence directory. No v4
ordinary full-suite or independent parent-harness result is claimed.

## Parent v4 gates and minimal v5 revision

The parent subsequently passed ordinary v4 full gates: full tests in
**1191.321 seconds**, vet/lint/build/diff/DAG, and **38/38** actual CLI cases.
All three reviewers verified parent manifest
`df049edcd4b07927789a4378c8f06c56af5f706a9517f72aef53f1f9d124b2f5`;
state and UX approved. Integration requested only V4-1: retained movement
evidence must also exclude checkout launches when the shared guard is absent.

V5 changes the existing shared session-state scan to reuse the strict movement
validator. It preserves existing launch ordering and final intent checks,
completed-receipt behavior, other state handling, budgets and legacy flags.
Focused real-Git regressions create genuine pending movement and completed
receipts, remove the guard, and exercise direct, tmux and feature-directory
launches. Corrupt/FIFO evidence refuses without effects; late pending/corrupt
evidence is restored under a proved session intent. HEAD/index/reflog/exclude
bytes, context creation, launch counters and evidence identity/bytes are checked.
The tests reproduce the unsafe V4 launches and pass after the correction.

The `gpt-6-astra` implementer leaves state `implementing`, with no record,
commit, land, push, tag or delegation. Parent retains final ordinary full
validation, independent CLI verification and exact-manifest approval.

V5 validation passed: all 18 new route/scenario cases (3.013s), then affected
scoped/session/lifecycle/no-flag/help tests (internal 11.207s; CLI 266.093s).
The red receipt records 15 unsafe V4 launch cases while all three completed
controls remained valid. Vet, lint with a new owned cache (zero issues), build,
whitespace and DAG checks passed. Reports and hashes are in
`candidate-report-v5.json` and `candidate-manifest-v5.json` in the implementer
evidence directory; no V5 ordinary full-suite or parent-harness result is claimed.

## Approved delivery order

1. Complete manual analyze, define, explore in order, validate hard-parent
   DAG, and create an isolated worktree.
2. One permitted-model implementer builds new routing, narrow owner proof,
   typed publication state, guarded executor, selected-only follow-up, tests
   and user/agent docs. No agent commits, pushes or tags.
3. Parent audits diff and real CLI behavior. Run focused tests; freeze the
   manifest for three scoped independent reviews. Send blockers to the
   original implementer until all approve the same candidate.
4. Full gates: `go test ./... -count=1 -timeout 40m`, `go vet ./...`,
   `golangci-lint run ./...` with an owned cache, `make build`,
   `git diff --check`, `tpatch feature deps --validate-all`.
5. Record canonical patch and normalized exact-preimage recipe, prove new-path
   absence, independently replay at the exact base, and `tpatch land` one
   feature commit with required trailers.
6. Verify landed behavior and clean tree, push, wait for Ubuntu/macOS main CI,
   tag the next unused RC, verify exact version and tag/release CI, then close
   #6. Preserve known historical verifier limitations honestly.
