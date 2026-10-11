# Template conflict resolution: grounded exploration

Baseline: `9c634f2` / `v1.2.18-rc.5`. These are insertion points and tests for
the next implementation, not claims that the resolver already exists.

## Core planning and ownership

`internal/template_managed.go` is the main seam:

- `ManagedTemplateOwnedFile`, `ManagedTemplateRoleState`, and
  `ManagedTemplateState` currently expose only v1 ownership and selections.
  Add the minimal optional per-file incoming-proposal acknowledgement and
  explicit v1/v2 validation here; keep effective ownership strict.
- `loadManagedTemplateState` uses `DisallowUnknownFields` and rejects unknown
  versions through `validateManagedTemplateState`. Preserve strict reads,
  v1 compatibility, and the release's fail-closed downgrade behavior.
- `planManagedTemplateRoles` resolves source/agent then
  `desiredManagedTemplateFiles` renders all native/manifest content.
  Conflict input is this final rendered output, not just registry file bytes.
- `planManagedRoleFiles` (baseline line 978) currently returns immediately
  for a changed owned destination. Separate immutable eligible conflict
  descriptions from hard failures, finish other file/role/link preflight,
  and only produce executable effects after all decisions are bound.
  Preserve the default refusal path for non-resolver callers.
- `matchOwnedFile` (baseline line 1784) is intentionally strict. Do not add
  an "allow modified" switch: accepted effective hashes must satisfy it,
  while unacknowledged changes must still fail in every context reader.
- `ReplanManagedTemplate`, `ValidateManagedTemplateReplan`, and
  `managedTemplatePlanFingerprint` must carry/validate exact decisions.
  `cloneManagedTemplateState` must not alias mutable new decision data or
  omit it when copying an unselected role.
- `planManagedExternalLinks`, `validateManagedOwnedSourceFiles`,
  `ValidateManagedOrchestratorContext`, and
  `ValidateManagedCheckoutContextResolved` consume the effective content.
  Keep exact link/root identities and existing scope/agent validation.

Ordinary missing-file recreation is existing behavior; missing remembered
customizations and obsolete customized paths need explicit refusal branches.
Do not let `oldByPath` cleanup discard a decision or treat a kept file as an
ordinary removable file solely because its effective hash matches.

## CLI orchestration

`internal/cli/add.go:addCmd/addManaged` already routes managed versus legacy
and performs removal consent before template effects, branch creation, or
launch. `internal/cli/template.go:templateSyncCmd` resolves/sorts all features,
collects managed plans and legacy contexts, confirms removals, runs managed
effects, then performs legacy work.

Wire the new flag to these two entry points, not to generic new/open/inject
or lifecycle commands. Preserve explicit selector/default handling. A
resolver flag is not a managed selector. Mixed batches must name legacy
members without applying their copier before managed preflight and consent.

`internal/cli/managed_template.go` owns shared flag decoding, workspace/config
resolution, `confirmManagedTemplateRemovals` (baseline line 395), and
`executeManagedTemplatePlansWithReservations`.
Insert shared conflict decision collection before removal consent; keep
terminal/editor activity outside writer reservations. Existing locked
replanning happens for every plan before the first apply, which is the right
place to revalidate evidence rather than recollect answers.

The removal helper currently creates its own `bufio.Reader`. A multi-prompt
flow must use one reader for the invocation, including removal confirmation,
so buffered answers are not lost or accidentally consumed by an editor.
An editor must receive the intended terminal streams, not a leftover
confirmation buffer. Keep Cobra output/error propagation explicit.

`managedTemplateInputIsTTY` is the existing test seam; its character-device
check alone is not proof of an actual terminal (for example `/dev/null`).
Use a real terminal check for the new resolver, test pipe/device refusal,
and do not broaden accepted removal input. `go.mod` already includes
`golang.org/x/sys`; no UI framework is required.

## Diff/editor support and prior art

The inspected `internal/` code has no reusable interactive editor or
two-way diff helper. Existing agent command splitting in
`internal/cli/direct_open.go` and `internal/session.go` uses whitespace fields,
which is not sufficient for quoted editor paths/arguments. Do not change
agent launch parsing as part of this feature.

Keep editor selection and a small quoted-argv parser in the CLI layer, with
explicit command/result seams. No shell interpolation of template content
or paths. A two-way diff can use the already-required Git executable with
no-index, external-diff/textconv disabled, no pager/color, and fixed private
input paths; treat exit 1 as "differences", not failure. Do not consult
repository diff drivers or execute user-configured diff helpers. Test escaped
path/payload presentation separately from byte-preserving staged content.

Use a private temporary directory outside workspace roots for the current,
incoming, and editor result. Validate editor output via a pinned directory;
allow normal regular-file atomic saves but reject symlink/hard-link outputs
and unsafe parent replacement. Candidate acceptance is separate from editor
exit. Error cleanup must not delete user edits or arbitrary editor residue.
This is not another recovery journal or a persistent history store.

## Preserve-before-mutate and lifecycle consumers

`internal/template_preserve.go:applyManagedTemplatePlanPreserved` already
stages writes, preserves prior objects, publishes links/ledger, and verifies
cleanup. Reuse it for all accepted output. Keep-only decisions need explicit
unwritten-source observations at before-state and cleanup boundaries; the
existing write list does not protect an asset that is not being rewritten.

`internal/template_fs_unix.go` and the Linux/Darwin/unsupported exclusive
rename helpers are existing mechanisms, not targets for redesign. Preserve
the parent's no-replace publication and no-symlink descriptor protocol.

Trace all state copies/consumers while implementing: the shared managed
helpers, `internal/cli/new.go`, `internal/cli/open.go`,
`internal/cli/direct_open.go`, `internal/cli/open_checkout.go`,
`internal/cli/inject.go`, `internal/cli/rename.go`,
`internal/cli/archive.go`, `internal/cli/export.go`, and
`internal/session.go`. Prefer fixing shared state copying/validation rather
than giving each command a new resolver. Add regression coverage for their
existing admission semantics; expand production edits only if a demonstrated
consumer drops acknowledgements.

## Existing test anchors and required additions

| Existing file / anchors | Extension |
|---|---|
| `internal/template_managed_test.go`: `TestManagedTemplateCheckoutNativeOwnershipAndScope`, `TestManagedTemplatePersistedScopeAgentAndSafetyFailures` | Structured conflicts, strict v1/v2 parsing, per-file rendered-proposal keys, keep/edit/repeat/replace, missing and obsolete overrides, role isolation, native format. |
| `internal/template_managed_test.go`: `TestManagedTemplatePreserveBeforeMutateCleanupAndConflictRetention`, `TestManagedTemplatePublishPinsFinalParentAndCleanupIsBounded` | Keep-only state publication, accepted-output preservation, concurrent source/leaf swaps, editor-private object checks, bounded cleanup. |
| `internal/template_parent_regression_test.go` | Preserve existing parent/root/ownership regressions; use existing failure seams rather than weaker replacement mocks. |
| `internal/cli/named_template_test.go`: `newManagedCLIFixture`, `runManagedCobra`, `TestManagedTemplateConsentMatrixAndPostConsentDrift`, `TestManagedTemplateBatchPreflightBeforeLegacyEffects` | Flag routing, deterministic menu/diff, fake editors, shared reader, cancellation after earlier choices, separate removal consent, mixed-batch no-effect assertions. |
| `internal/cli/named_template_final_regression_test.go` | Accepted overrides across controlling/entry repositories, root drift, session reservations, injection, rename/archive/reactivation, pending/corrupt owner scans, v2 preservation. |
| `internal/cli/template_parent_regression_test.go` | No legacy/default/selector drift, no new launch prompts or changed existing refusals. |
| `internal/cli/sync_plan_docs_test.go` | Current planning handoff and shipped rc.5 assertions now; implemented flag/help guidance later without weakening sync/reparent prose checks. |

Use actual terminal/PTY input for acceptance smoke tests, plus deterministic
input/editor seams for exhaustive failures. Verify rc.5's real binary rejects
v2 through managed state readers/writers without changing template data; a
test that merely rejects v2 in the new reader is not a downgrade test.
The released checkout `new` route does not read the ledger: verify that its
logical branch creation leaves template bytes/acknowledgements unchanged,
not that every old command universally refuses. The user approved this
precise boundary after reproduction.

The pre-existing materialized external feature-rename/backpointer failure is
separately tracked as GitHub #5 by user request. Verify branch rename and
archive/reactivation with overrides, and feature metadata moves without
claiming to repair #5. Preserve export's existing omission of ledger data
from archives. Never exercise a person's installed editor, workspace, global
registry, Git configuration, or active agent session.

## Smallest change boundary and process

Core production changes should begin with `internal/template_managed.go` and
`internal/template_preserve.go`; CLI changes with the three add/sync/shared
files above. Add a focused helper file only if the actual code warrants it,
not a general conflict framework or new command hierarchy. Reuse the existing
fixtures and preservation protocol. Update `README.md`,
`docs/cheatsheet.md`, `docs/configuration.md`, `CHANGELOG.md`,
`assets/skills/claude/tesseraworkspaces/SKILL.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`, and
`assets/skills/copilot/tws.prompt.md` during implementation.

Remove the shipped parent's obsolete reverse soft dependency, add the hard
`named-feature-templates` parent, retain `template-sync-existing`, then
validate the DAG. Do not edit published recipe/patch/review evidence.

Current planning changes touch only handoff docs, their executable prose
assertions, and tpatch planning/dependency metadata. Validate with the focused
planning/documentation tests, formatting/lint for the changed test, diff
checks, and DAG validation. Before any later implementation lands, follow
the full gates and review loop in `docs/engineering-workflow.md`, using the
40-minute full-suite package timeout in this feature's spec. The
planning stage was completed at `defined`, with last command `explore`.
The user has now authorized Path B implementation in an isolated worktree;
publication still requires separate approval.
