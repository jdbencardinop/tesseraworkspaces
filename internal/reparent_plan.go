package internal

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ============================================================================
// ReparentPlan — the safe-reparent plan document, schema v1 (§7)
//
// This file declares the document, its closed refusal/warning domains, the
// primary-refusal selector and the guard evaluator. It declares no builder:
// BuildReparentPlan and its probes live in internal/reparent_plan_build.go.
//
// RebasePlan and every sync-owned type it publishes are untouched. Where a
// shipped type is exact (§7.2) it is embedded verbatim — PlanWorkspace,
// PlanContext, PlanEntryHead, PlanEntryReplay, PlanAncestry, PlanCollateralRef,
// PlanFetch, PlanRepository, PlanGuardBlock, the PlanState* file facts,
// PlanEncodingIssue, PlanConfigIssue. Where a shipped type carries sync-only
// semantics (PlanEntry, PlanBlocker, PlanRefusal, PlanSummary, PlanApproval,
// PlanPolicy, ...) it is NOT reused, because reusing it would drag the sync
// RefusalKind domain into a document that has its own.
// ============================================================================

// ReparentPlanSchemaVersion is this document's schema version. It is
// independent of RebasePlanSchemaVersion and never moves with it.
const ReparentPlanSchemaVersion = 1

// Closed domain values published by ReparentPlan. Each group is exhaustive:
// a value outside it is a defect, never an extension point.
const (
	ReparentRouteFresh    = "fresh"
	ReparentRouteContinue = "continue"

	ReparentInvocationPlanOnly = "plan-only"
	ReparentInvocationExecute  = "execute"

	ReparentRoleTarget     = "target"
	ReparentRoleDescendant = "descendant"

	ReparentBindingPinned         = "pinned"
	ReparentBindingParentComputed = "parent-computed"

	ReparentPlannabilityRows        = "rows"
	ReparentPlannabilityNoWork      = "no-work"
	ReparentPlannabilityUnavailable = "unavailable"

	ReparentApprovalScopeFresh  = "fresh-execution"
	ReparentApprovalScopeResume = "resume"
	ReparentApprovalScopeNone   = "none"

	ReparentParentKindStackEntry = "stack-entry"
	ReparentParentKindLiteralRef = "literal-ref"
	ReparentParentKindNone       = "none"

	ReparentResolutionEntry      = "entry"
	ReparentResolutionRefFull    = "ref-full"
	ReparentResolutionRefHead    = "ref-head"
	ReparentResolutionRefTag     = "ref-tag"
	ReparentResolutionRefRemote  = "ref-remote"
	ReparentResolutionRefOther   = "ref-other"
	ReparentResolutionRawOID     = "raw-oid"
	ReparentResolutionUnresolved = "unresolved"
	ReparentResolutionAmbiguous  = "ambiguous"

	ReparentCutoffRecordedBySync   = "recorded-by-sync"
	ReparentCutoffOperatorSupplied = "operator-supplied"
	ReparentCutoffOldParentTip     = "old-parent-tip"
	ReparentCutoffNone             = "none"

	ReparentCutoffRecordAbsent       = "absent"
	ReparentCutoffRecordPresent      = "present"
	ReparentCutoffRecordUnresolvable = "unresolvable"

	ReparentLimitsOriginFlags     = "flags"
	ReparentLimitsOriginPersisted = "persisted-state"
	ReparentLimitsOriginNone      = "none"

	// ReparentDeferredRendering is the literal every deferred cell (§7.5a)
	// renders as in the human document. In JSON the same cell is null; it is
	// never a blank, never a zero, and never an invented SHA.
	ReparentDeferredRendering = "(computed at replay)"
)

// ============================================================================
// §7.3 Top-level document — exactly 25 keys, in declaration order
// ============================================================================

// ReparentPlan is the safe-reparent plan document. Its JSON key order is its
// Go declaration order and both are frozen at exactly 25 keys.
type ReparentPlan struct {
	SchemaVersion  int                       `json:"schema_version"`
	Route          string                    `json:"route"`      // fresh | continue
	Invocation     string                    `json:"invocation"` // plan-only | execute
	Workspace      PlanWorkspace             `json:"workspace"`
	Feature        string                    `json:"feature"`
	Policy         ReparentPlanPolicy        `json:"policy"`
	Target         ReparentPlanRow           `json:"target"`
	Descendants    []ReparentPlanRow         `json:"descendants"`
	MetadataDelta  ReparentPlanMetadataDelta `json:"metadata_delta"`
	Strategy       ReparentPlanStrategy      `json:"strategy"`
	Holders        ReparentPlanHolders       `json:"holders"`
	Remote         ReparentPlanRemote        `json:"remote"`
	Fetch          PlanFetch                 `json:"fetch"`
	Freshness      string                    `json:"freshness"`
	Repositories   []PlanRepository          `json:"repositories"`
	State          ReparentPlanState         `json:"state"`
	Runnable       bool                      `json:"runnable"`
	Blockers       []ReparentPlanBlocker     `json:"blockers"`
	Warnings       []ReparentPlanWarning     `json:"warnings"`
	EncodingIssues []PlanEncodingIssue       `json:"encoding_issues"`
	ConfigIssues   []PlanConfigIssue         `json:"config_issues"`
	Summary        ReparentPlanSummary       `json:"summary"`
	Guard          PlanGuardBlock            `json:"guard"`
	Refusal        ReparentPlanRefusal       `json:"refusal"`
	Approval       ReparentPlanApproval      `json:"approval"`
}

// ============================================================================
// §7.4 Policy
// ============================================================================

// ReparentPlanPolicy is ReparentPlan.Policy: the frozen decisions of this
// invocation, published so `auto` is never ambiguous after the fact.
type ReparentPlanPolicy struct {
	Fetch               string                 `json:"fetch"` // reuses SyncFetchPolicy's values
	FetchDefaultApplied bool                   `json:"fetch_default_applied"`
	OntoKindRequested   string                 `json:"onto_kind_requested"` // auto | entry | ref
	OIDWidth            *int                   `json:"oid_width"`           // 40 (sha1) | 64 (sha256) | null when unavailable
	CutoffSupplied      bool                   `json:"cutoff_supplied"`
	LimitsSupplied      bool                   `json:"limits_supplied"`
	LimitsOrigin        string                 `json:"limits_origin"` // flags | persisted-state | none
	Validation          ReparentPlanValidation `json:"validation"`
}

// ReparentPlanValidation is policy.validation. The raw frozen command is
// deliberately NOT a member: a plan document is printed and pasted, and a
// test command can carry credentials in an argument, so only its digest is
// published. The command itself lives solely in the 0600 state artifact.
type ReparentPlanValidation struct {
	Applies              bool    `json:"applies"`
	Source               string  `json:"source"`         // config-repo | config-workspace | none
	CommandDigest        *string `json:"command_digest"` // null iff !Applies
	RunsOn               string  `json:"runs_on"`        // constant "each-computed-row"
	RequiresCleanContext bool    `json:"requires_clean_context"`
}

// ReparentValidationRunsOn is policy.validation.runs_on's only value.
const ReparentValidationRunsOn = "each-computed-row"

// ============================================================================
// §7.5 Rows — exactly 23 keys, one type for the target and every descendant
// ============================================================================

// ReparentPlanRow is one closure member. The target has Role "target" and a
// pinned destination; every other row has Role "descendant" and a
// parent-computed destination whose SHA cannot exist yet (§7.5a).
type ReparentPlanRow struct {
	Role                string              `json:"role"`
	Order               int                 `json:"order"`
	Name                string              `json:"name"`
	GitBranch           string              `json:"git_branch"`
	Repo                string              `json:"repo"`
	Materialization     string              `json:"materialization"`
	ExecutionContext    PlanContext         `json:"execution_context"`
	Head                PlanEntryHead       `json:"head"`
	OldParent           ReparentParent      `json:"old_parent"`
	NewParent           ReparentParent      `json:"new_parent"`
	DestinationBinding  string              `json:"destination_binding"`
	DestinationParent   *string             `json:"destination_parent"`
	DestinationSHA      *string             `json:"destination_sha"`
	Cutoff              ReparentPlanCutoff  `json:"cutoff"`
	Strategy            string              `json:"strategy"`
	Argv                []string            `json:"argv"`
	EffectiveBackend    string              `json:"effective_backend"`
	Replay              PlanEntryReplay     `json:"replay"`
	MergeCommitsInRange *int                `json:"merge_commits_in_range"`
	Ancestry            PlanAncestry        `json:"ancestry"`
	Pins                ReparentRowPins     `json:"pins"`
	CollateralRefs      []PlanCollateralRef `json:"collateral_refs"`
	Notes               []string            `json:"notes"`
}

// ReparentRowStrategy is every row's strategy value, and ReparentRowBackend
// is every runnable row's effective_backend: v1 selects the merge rebase
// backend unconditionally and computes on a detached HEAD.
const (
	ReparentRowStrategy       = "detached-rebase-onto"
	ReparentRowBackend        = "merge"
	ReparentRowBackendUnknown = "unknown"
)

// ReparentParent is one side of a row's configured parent edge. The token the
// operator typed and the token stack.yaml stores are published separately,
// because a literal destination is always stored canonically (§4.3b) and a
// later run's old_parent is whatever the previous run stored.
type ReparentParent struct {
	RequestedToken    *string                   `json:"requested_token"`
	StoredToken       *string                   `json:"stored_token"`
	Kind              string                    `json:"kind"`
	Ref               *string                   `json:"ref"`
	SHA               *string                   `json:"sha"`
	Repo              *string                   `json:"repo"`
	Resolution        string                    `json:"resolution"`
	Candidates        []string                  `json:"candidates"`
	ResolverAgreement ReparentResolverAgreement `json:"resolver_agreement"`
}

// ReparentResolverAgreement is §4.3c's verdict: all four base resolvers,
// given the stored token, must normalize to the same pinned commit.
type ReparentResolverAgreement struct {
	Checked   bool                      `json:"checked"`
	Agreed    *bool                     `json:"agreed"` // null iff !Checked
	Resolvers []ReparentResolverVerdict `json:"resolvers"`
}

// ReparentResolverVerdict is one resolver's answer, normalized.
type ReparentResolverVerdict struct {
	Name   string  `json:"name"`
	SHA    *string `json:"sha"`
	Agrees bool    `json:"agrees"`
	Detail *string `json:"detail"`
}

// The four §4.3c resolver names, in the order the agreement block publishes
// them.
const (
	ReparentResolverDestination = "resolve-reparent-destination"
	ReparentResolverSyncBase    = "resolve-sync-base"
	ReparentResolverStackBase   = "stack-base-ref"
	ReparentResolverCheckout    = "checkout-base"
)

// ReparentPlanCutoff is one row's replay boundary and its provenance. It is
// also the resolver-side result type: there is exactly one shape for the
// boundary, so a resolved cutoff and a published cutoff can never diverge.
type ReparentPlanCutoff struct {
	RecordedSHA      *string `json:"recorded_sha"`
	RecordedState    string  `json:"recorded_state"` // absent | present | unresolvable
	SuppliedToken    *string `json:"supplied_token"`
	SuppliedSHA      *string `json:"supplied_sha"`
	ResolvedSHA      *string `json:"resolved_sha"`
	Provenance       string  `json:"provenance"`
	AncestorOfBranch *bool   `json:"ancestor_of_branch"`
	Conflict         *string `json:"conflict"`
}

// ReparentRowPins is the pin namespace this row's pre- and post-image tips
// are protected in for the duration of the run.
type ReparentRowPins struct {
	EntryID       string `json:"entry_id"`
	OldRefPattern string `json:"old_ref_pattern"`
	NewRefPattern string `json:"new_ref_pattern"`
}

// ============================================================================
// §7.6 Metadata delta
// ============================================================================

// ReparentPlanMetadataDelta is the exact stack.yaml change this run will make.
type ReparentPlanMetadataDelta struct {
	Entries                      []ReparentPlanMetadataEntry `json:"entries"`
	StackSHA256Before            string                      `json:"stack_sha256_before"`
	StackSHA256AfterExpected     *string                     `json:"stack_sha256_after_expected"`
	Writer                       string                      `json:"writer"`
	WritePoint                   string                      `json:"write_point"`
	PostImageKnown               bool                        `json:"post_image_known"`
	EntriesOutsideClosureChanged bool                        `json:"entries_outside_closure_changed"`
}

// The metadata delta's two constants: the writer that produces the bytes and
// the point in the run at which they are written.
const (
	ReparentMetadataWriter     = "durable-atomic-stack-writer"
	ReparentMetadataWritePoint = "after-ref-commit"
)

// ReparentPlanMetadataEntry is one closure row's metadata cell change.
type ReparentPlanMetadataEntry struct {
	Name                   string  `json:"name"`
	BaseBefore             string  `json:"base_before"`
	BaseAfter              string  `json:"base_after"`
	LastBaseSHABefore      *string `json:"last_base_sha_before"`
	LastBaseSHAAfter       *string `json:"last_base_sha_after"`
	LastBaseSHAAfterSource string  `json:"last_base_sha_after_source"`
	Changed                bool    `json:"changed"`
}

// The two last_base_sha_after sources: the target takes the pinned
// destination, every descendant takes its parent row's post-replay tip.
const (
	ReparentLastBaseSourcePinned    = "pinned-destination"
	ReparentLastBaseSourceReplay    = "post-replay-parent-tip"
	ReparentLastBaseSourceUnchanged = "unchanged"
)

// ============================================================================
// §7.7 Strategy and atomicity
// ============================================================================

// ReparentPlanStrategy is how the run computes and commits.
type ReparentPlanStrategy struct {
	Kind                string                `json:"kind"`
	RunID               *string               `json:"run_id"`
	ComputationContext  string                `json:"computation_context"`
	ComputationPath     *string               `json:"computation_path"`
	Backend             string                `json:"backend"`
	UsesGitReplay       bool                  `json:"uses_git_replay"`
	ForbiddenOperations []string              `json:"forbidden_operations"`
	PinNamespace        string                `json:"pin_namespace"`
	Atomicity           ReparentPlanAtomicity `json:"atomicity"`
}

// The strategy constants. PinNamespace is a SHAPE, not a resolved path: a
// plan mints no run id, so it can publish neither the namespace's resolved
// spelling nor the scratch path that embeds it.
const (
	ReparentStrategyKind        = "detached-scratch-cas"
	ReparentComputationExternal = "external-scratch-worktree"
	ReparentComputationCheckout = "checkout-detached-head"
	ReparentPinNamespaceShape   = "refs/tws/reparent/<run-id>"
)

// ReparentForbiddenOperations is strategy.forbidden_operations: the frozen
// literal list of verbs this feature never runs, published so an operator can
// see the guarantee rather than take it on trust.
var ReparentForbiddenOperations = []string{
	"git reset --hard",
	"git replay",
	"git rebase --update-refs",
	"git rebase --autostash",
	"git rebase --apply",
	"git push",
}

// ReparentPlanAtomicity is the honest statement of what "atomic" means here.
// CombinedAtomic is always false: refs, metadata and worktrees are three
// separate effects, and claiming otherwise would be the one thing an operator
// could not recover from being wrong about.
type ReparentPlanAtomicity struct {
	RefBackend                      string `json:"ref_backend"` // files | reftable | unknown
	RefCommit                       string `json:"ref_commit"`
	RefCommitRaceAtomic             bool   `json:"ref_commit_race_atomic"`
	RefCommitCrashAtomic            *bool  `json:"ref_commit_crash_atomic"`
	MetadataWriteAtomic             bool   `json:"metadata_write_atomic"`
	MetadataWriteDurable            bool   `json:"metadata_write_durable"`
	CombinedAtomic                  bool   `json:"combined_atomic"`
	PartialCommitRecovery           string `json:"partial_commit_recovery"`
	CommitPoint                     string `json:"commit_point"`
	ReferenceTransactionHookMayVeto bool   `json:"reference_transaction_hook_may_veto"`
}

// The atomicity constants and the three ref-backend values. An unknown
// backend is permitted and is treated exactly as files — the conservative
// choice — never as a refusal.
const (
	ReparentRefCommitKind         = "single-cas-transaction"
	ReparentCommitPoint           = "durable-post-image-metadata"
	ReparentRecoveryTransactional = "transactional"
	ReparentRecoveryRowByRow      = "row-by-row"

	ReparentRefBackendFiles    = "files"
	ReparentRefBackendReftable = "reftable"
	ReparentRefBackendUnknown  = "unknown"
)

// ============================================================================
// §7.8 Holders
// ============================================================================

// ReparentPlanHolders describes every worktree whose HEAD is attached to an
// affected branch, and what the run will do about it.
type ReparentPlanHolders struct {
	Applies                bool                 `json:"applies"`
	OriginalBranch         *string              `json:"original_branch"`
	OriginalHead           *string              `json:"original_head"`
	OriginalDetached       bool                 `json:"original_detached"`
	PreimageHolderExcluded *string              `json:"preimage_holder_excluded"`
	RestorePoint           string               `json:"restore_point"`
	Rows                   []ReparentPlanHolder `json:"rows"`
}

// ReparentHolderRestorePoint is holders.restore_point's only value.
const ReparentHolderRestorePoint = "after-metadata-write"

// ReparentPlanHolder is one held branch.
type ReparentPlanHolder struct {
	GitBranch   string  `json:"git_branch"`
	HolderKind  string  `json:"holder_kind"` // linked-worktree | primary-checkout | computation-context | none
	HolderPath  *string `json:"holder_path"`
	PreimageSHA *string `json:"preimage_sha"`
	Action      string  `json:"action"` // detach-and-restore | already-detached | none | refuse
	Safe        bool    `json:"safe"`
	Reason      *string `json:"reason"`
}

// ============================================================================
// §7.9 Remote
// ============================================================================

// ReparentPlanRemote is everything this feature says about remotes, and
// everything it refuses to do to them: no provider call, no implicit push, no
// automatic PR retarget, and exactly one named remote.
type ReparentPlanRemote struct {
	ProviderCalls  int                        `json:"provider_calls"`
	ImplicitPush   bool                       `json:"implicit_push"`
	Remote         string                     `json:"remote"`
	Rows           []ReparentPlanRemoteRow    `json:"rows"`
	Guidance       []string                   `json:"guidance"`
	FollowupRecord ReparentPlanRemoteFollowup `json:"followup_record"`
}

// ReparentRemoteName is the only remote this feature ever names.
const ReparentRemoteName = "origin"

// ReparentPlanRemoteRow is one closure row's remote picture.
type ReparentPlanRemoteRow struct {
	Name               string  `json:"name"`
	GitBranch          string  `json:"git_branch"`
	Remote             string  `json:"remote"`
	RemoteRef          *string `json:"remote_ref"`
	RemoteSHA          *string `json:"remote_sha"`
	UpstreamConfigured bool    `json:"upstream_configured"`
	Divergence         string  `json:"divergence"`
	PRBaseBefore       *string `json:"pr_base_before"`
	PRBaseAfter        *string `json:"pr_base_after"`
	ProviderHint       string  `json:"provider_hint"`
}

// ReparentPlanRemoteFollowup is the pending-follow-up record's own plan.
type ReparentPlanRemoteFollowup struct {
	WillWrite  bool     `json:"will_write"`
	WritePoint string   `json:"write_point"`
	Path       *string  `json:"path"`
	Entries    []string `json:"entries"`
	ClearRule  string   `json:"clear_rule"`
}

// ReparentRemoteRecordWritePoint is followup_record.write_point's only value:
// the record lands before any public ref moves.
const ReparentRemoteRecordWritePoint = "after-new-pins-before-holder-detach"

// ============================================================================
// §7.10 State
// ============================================================================

// ReparentPlanState is the pre-acquisition state picture, reusing the shipped
// sync snapshot/worktree/git-op/head facts verbatim.
type ReparentPlanState struct {
	Snapshot            PlanStateSnapshot      `json:"snapshot"`
	Files               ReparentPlanStateFiles `json:"files"`
	Worktree            PlanStateWorktree      `json:"worktree"`
	GitOp               PlanStateGitOp         `json:"git_op"`
	Head                PlanStateHead          `json:"head"`
	Exclusion           ReparentPlanExclusion  `json:"exclusion"`
	ApprovedFingerprint *string                `json:"approved_fingerprint"`
}

// ReparentPlanStateFiles embeds the five shipped sync file facts verbatim and
// adds the two reparent-owned artifacts.
type ReparentPlanStateFiles struct {
	CheckoutTransaction  PlanStateFileCheckoutTransaction  `json:"checkout_transaction"`
	CheckoutLock         PlanStateFileCheckoutLock         `json:"checkout_lock"`
	ExternalLegacyState  PlanStateFileExternalLegacyState  `json:"external_legacy_state"`
	ExternalRunPayload   PlanStateFileExternalPayload      `json:"external_run_payload"`
	ExternalRunGuard     PlanStateFileExternalRunGuard     `json:"external_run_guard"`
	ReparentState        ReparentPlanStateFileState        `json:"reparent_state"`
	ReparentRemoteRecord ReparentPlanStateFileRemoteRecord `json:"reparent_remote_record"`
}

// ReparentPlanStateFileState is state.files.reparent_state.
type ReparentPlanStateFileState struct {
	PlanStateFileBase
	StateVersion *int    `json:"state_version"`
	RunID        *string `json:"run_id"`
	Stage        *string `json:"stage"`
	ResumeStage  *string `json:"resume_stage"`
	Feature      *string `json:"feature"`
	Mode         *string `json:"mode"`
}

// ReparentPlanStateFileRemoteRecord is state.files.reparent_remote_record.
type ReparentPlanStateFileRemoteRecord struct {
	PlanStateFileBase
	RecordVersion  *int `json:"record_version"`
	PendingEntries int  `json:"pending_entries"`
}

// ReparentPlanExclusion is the mutual-exclusion verdict: sync and reparent are
// never both live for one feature in one mode.
type ReparentPlanExclusion struct {
	SyncActive                bool  `json:"sync_active"`
	ReparentActive            bool  `json:"reparent_active"`
	LockHolderPID             *int  `json:"lock_holder_pid"`
	LockHolderLive            *bool `json:"lock_holder_live"`
	CompatArtifactsConsistent *bool `json:"compat_artifacts_consistent"`
}

// ============================================================================
// §7.11 Blockers, warnings, refusal, summary, approval
// ============================================================================

// ReparentPlanBlocker is one blockers[] row. Entry is nil for a
// document-level fact.
type ReparentPlanBlocker struct {
	Kind   ReparentRefusalKind `json:"kind"`
	Entry  *string             `json:"entry"`
	Detail string              `json:"detail"`
}

// ReparentPlanWarning is one warnings[] row, over the closed eleven-member
// warning domain below.
type ReparentPlanWarning struct {
	Kind   string  `json:"kind"`
	Entry  *string `json:"entry"`
	Detail string  `json:"detail"`
}

// ReparentPlanRefusal is the document's single primary refusal.
type ReparentPlanRefusal struct {
	Kind   *ReparentRefusalKind `json:"kind"`
	Detail *string              `json:"detail"`
}

// ReparentPlanSummary is the document's eleven-key summary.
type ReparentPlanSummary struct {
	Plannability              string              `json:"plannability"`
	HasWork                   bool                `json:"has_work"`
	Rows                      int                 `json:"rows"`
	Descendants               int                 `json:"descendants"`
	DeferredRows              int                 `json:"deferred_rows"`
	MaxEntryCandidates        *int                `json:"max_entry_candidates"`
	TotalCandidates           *int                `json:"total_candidates"`
	TotalCandidatesLowerBound *int                `json:"total_candidates_lower_bound"`
	RowsWithUnknownCandidates int                 `json:"rows_with_unknown_candidates"`
	CollateralRefs            []PlanCollateralRef `json:"collateral_refs"`
	CollateralBound           *string             `json:"collateral_bound"`
}

// ReparentPlanApproval mirrors PlanApproval's shape exactly, over the
// reparent document's own scopes.
type ReparentPlanApproval struct {
	Fingerprint *string                    `json:"fingerprint"`
	Usable      bool                       `json:"usable"`
	Scope       string                     `json:"scope"`
	Supplied    bool                       `json:"supplied"`
	Accepted    *bool                      `json:"accepted"`
	Covers      ReparentPlanApprovalCovers `json:"covers"`
}

// ReparentPlanApprovalCovers is approval.covers, exactly 8 keys.
//
// WaivedKinds is typed []RefusalKind — the SYNC guard domain — deliberately:
// the only values it ever carries are limit-per-entry and limit-total, which
// are produced by the reused guard limit algebra and are therefore not
// members of ReparentRefusalKinds at all.
type ReparentPlanApprovalCovers struct {
	Scope               string        `json:"scope"`
	WaivedEvaluationIDs []string      `json:"waived_evaluation_ids"`
	WaivedKinds         []RefusalKind `json:"waived_kinds"`
	HardBlockersWaived  bool          `json:"hard_blockers_waived"`
	HasWork             bool          `json:"has_work"`
	RequiresLimits      bool          `json:"requires_limits"`
	EncodingSafe        bool          `json:"encoding_safe"`
	Note                string        `json:"note"`
}

// ============================================================================
// §7.11 warning domain — closed at exactly eleven members
// ============================================================================

const (
	ReparentWarnDestinationRemoteTracking      = "destination-remote-tracking"
	ReparentWarnDestinationNotRemoteRewritten  = "destination-not-remote-rewritten"
	ReparentWarnDestinationAncestorOfOldParent = "destination-ancestor-of-old-parent"
	ReparentWarnCollateralRefInReplayRange     = "collateral-ref-in-replay-range"
	ReparentWarnCollateralUpdateRefsConfig     = "collateral-update-refs-config"
	ReparentWarnHolderDetachRequired           = "holder-detach-required"
	ReparentWarnHolderRestoreDeferred          = "holder-restore-deferred"
	ReparentWarnUntrackedPresent               = "untracked-present"
	ReparentWarnRemoteDivergenceExpected       = "remote-divergence-expected"
	ReparentWarnRemoteFollowupPending          = "remote-followup-pending"
	ReparentWarnFilesBackendNotCrashAtomic     = "files-backend-not-crash-atomic"
)

// ReparentWarningKinds is the complete warning domain. It has exactly eleven
// members and is not rank-ordered: warnings never refuse, so they have no
// precedence.
var ReparentWarningKinds = []string{
	ReparentWarnDestinationRemoteTracking,
	ReparentWarnDestinationNotRemoteRewritten,
	ReparentWarnDestinationAncestorOfOldParent,
	ReparentWarnCollateralRefInReplayRange,
	ReparentWarnCollateralUpdateRefsConfig,
	ReparentWarnHolderDetachRequired,
	ReparentWarnHolderRestoreDeferred,
	ReparentWarnUntrackedPresent,
	ReparentWarnRemoteDivergenceExpected,
	ReparentWarnRemoteFollowupPending,
	ReparentWarnFilesBackendNotCrashAtomic,
}

// ============================================================================
// §13.1 refusal domain — closed at exactly 49 members, in rank order
// ============================================================================

// ReparentRefusalKind is the closed reparent refusal domain. It is a distinct
// type from the sync RefusalKind and no implicit conversion exists: a sync
// guard refusal keeps the plan-guard: marker and the sync domain, and a
// reparent-owned refusal keeps the reparent: marker and this one.
type ReparentRefusalKind string

const (
	ReparentRefusalPlanUnavailable                     ReparentRefusalKind = "plan-unavailable"
	ReparentRefusalStackUnsortable                     ReparentRefusalKind = "stack-unsortable"
	ReparentRefusalDuplicateEntryName                  ReparentRefusalKind = "duplicate-entry-name"
	ReparentRefusalDuplicateGitBranch                  ReparentRefusalKind = "duplicate-git-branch"
	ReparentRefusalTargetUnknown                       ReparentRefusalKind = "target-unknown"
	ReparentRefusalTargetArchived                      ReparentRefusalKind = "target-archived"
	ReparentRefusalAffectedArchived                    ReparentRefusalKind = "affected-archived"
	ReparentRefusalCrossRepoClosure                    ReparentRefusalKind = "cross-repo-closure"
	ReparentRefusalRepoUnavailable                     ReparentRefusalKind = "repo-unavailable"
	ReparentRefusalDestinationUnset                    ReparentRefusalKind = "destination-unset"
	ReparentRefusalDestinationUnresolvable             ReparentRefusalKind = "destination-unresolvable"
	ReparentRefusalDestinationAmbiguous                ReparentRefusalKind = "destination-ambiguous"
	ReparentRefusalDestinationKindMismatch             ReparentRefusalKind = "destination-kind-mismatch"
	ReparentRefusalDestinationSelf                     ReparentRefusalKind = "destination-self"
	ReparentRefusalDestinationDescendant               ReparentRefusalKind = "destination-descendant"
	ReparentRefusalDestinationCycle                    ReparentRefusalKind = "destination-cycle"
	ReparentRefusalDestinationResolverDivergent        ReparentRefusalKind = "destination-resolver-divergent"
	ReparentRefusalDestinationSameParentStale          ReparentRefusalKind = "destination-same-parent-stale"
	ReparentRefusalBranchRefMissing                    ReparentRefusalKind = "branch-ref-missing"
	ReparentRefusalCutoffAbsent                        ReparentRefusalKind = "cutoff-absent"
	ReparentRefusalCutoffUnresolvable                  ReparentRefusalKind = "cutoff-unresolvable"
	ReparentRefusalCutoffConflict                      ReparentRefusalKind = "cutoff-conflict"
	ReparentRefusalCutoffNotAncestor                   ReparentRefusalKind = "cutoff-not-ancestor"
	ReparentRefusalDescendantCutoffAbsent              ReparentRefusalKind = "descendant-cutoff-absent"
	ReparentRefusalDescendantCutoffOverrideUnsupported ReparentRefusalKind = "descendant-cutoff-override-unsupported"
	ReparentRefusalMergeCommitInReplaySet              ReparentRefusalKind = "merge-commit-in-replay-set"
	ReparentRefusalGitOperationInProgress              ReparentRefusalKind = "git-operation-in-progress"
	ReparentRefusalContextDirty                        ReparentRefusalKind = "context-dirty"
	ReparentRefusalUntrackedOverwrite                  ReparentRefusalKind = "untracked-overwrite"
	ReparentRefusalHolderUnsafe                        ReparentRefusalKind = "holder-unsafe"
	ReparentRefusalSessionLive                         ReparentRefusalKind = "session-live"
	ReparentRefusalScratchWorktreeUnavailable          ReparentRefusalKind = "scratch-worktree-unavailable"
	ReparentRefusalSyncStatePresent                    ReparentRefusalKind = "sync-state-present"
	ReparentRefusalStatePresent                        ReparentRefusalKind = "reparent-state-present"
	ReparentRefusalStateUnsupported                    ReparentRefusalKind = "reparent-state-unsupported"
	ReparentRefusalStateCorrupt                        ReparentRefusalKind = "reparent-state-corrupt"
	ReparentRefusalStateForeign                        ReparentRefusalKind = "reparent-state-foreign"
	// CompatArtifactMissing is reserved for an envelope whose safe ownership
	// or reconstruction cannot be proven. Owned absence is automatic recovery
	// work and is never a continuation-plan blocker.
	ReparentRefusalCompatArtifactMissing     ReparentRefusalKind = "compat-artifact-missing"
	ReparentRefusalProbeFailed               ReparentRefusalKind = "probe-failed"
	ReparentRefusalCapabilityUnsupported     ReparentRefusalKind = "capability-unsupported"
	ReparentRefusalValidationFailed          ReparentRefusalKind = "validation-failed"
	ReparentRefusalConflictUnresolved        ReparentRefusalKind = "conflict-unresolved"
	ReparentRefusalRefTransactionMismatch    ReparentRefusalKind = "ref-transaction-mismatch"
	ReparentRefusalRefForeignValue           ReparentRefusalKind = "ref-foreign-value"
	ReparentRefusalMetadataPending           ReparentRefusalKind = "metadata-pending"
	ReparentRefusalMetadataDrift             ReparentRefusalKind = "metadata-drift"
	ReparentRefusalAbortForeignValue         ReparentRefusalKind = "abort-foreign-value"
	ReparentRefusalRemoteFollowupUnsafeLease ReparentRefusalKind = "remote-followup-unsafe-lease"
	ReparentRefusalNoWork                    ReparentRefusalKind = "no-work"
)

// ReparentRefusalKinds is the complete domain, in rank order: rank 1 is
// reported first. It has exactly 49 members.
//
// Three absences are deliberate. ref-transaction-partial is NOT here: §11.7
// reports it on a run that recovers and exits 0, and a refusal that does not
// refuse is a category error — it is a reparent-recovery: progress token
// instead. limit-per-entry, limit-total, guard-limit-mismatch,
// approval-without-limits, approval-mismatch and revalidation-mismatch are
// NOT here either: they belong to the reused sync guard domain and print with
// the plan-guard: marker. And a --continue against an aborting run refuses
// reparent-state-present rather than minting a 50th kind.
var ReparentRefusalKinds = []ReparentRefusalKind{
	ReparentRefusalPlanUnavailable,
	ReparentRefusalStackUnsortable,
	ReparentRefusalDuplicateEntryName,
	ReparentRefusalDuplicateGitBranch,
	ReparentRefusalTargetUnknown,
	ReparentRefusalTargetArchived,
	ReparentRefusalAffectedArchived,
	ReparentRefusalCrossRepoClosure,
	ReparentRefusalRepoUnavailable,
	ReparentRefusalDestinationUnset,
	ReparentRefusalDestinationUnresolvable,
	ReparentRefusalDestinationAmbiguous,
	ReparentRefusalDestinationKindMismatch,
	ReparentRefusalDestinationSelf,
	ReparentRefusalDestinationDescendant,
	ReparentRefusalDestinationCycle,
	ReparentRefusalDestinationResolverDivergent,
	ReparentRefusalDestinationSameParentStale,
	ReparentRefusalBranchRefMissing,
	ReparentRefusalCutoffAbsent,
	ReparentRefusalCutoffUnresolvable,
	ReparentRefusalCutoffConflict,
	ReparentRefusalCutoffNotAncestor,
	ReparentRefusalDescendantCutoffAbsent,
	ReparentRefusalDescendantCutoffOverrideUnsupported,
	ReparentRefusalMergeCommitInReplaySet,
	ReparentRefusalGitOperationInProgress,
	ReparentRefusalContextDirty,
	ReparentRefusalUntrackedOverwrite,
	ReparentRefusalHolderUnsafe,
	ReparentRefusalSessionLive,
	ReparentRefusalScratchWorktreeUnavailable,
	ReparentRefusalSyncStatePresent,
	ReparentRefusalStatePresent,
	ReparentRefusalStateUnsupported,
	ReparentRefusalStateCorrupt,
	ReparentRefusalStateForeign,
	ReparentRefusalCompatArtifactMissing,
	ReparentRefusalProbeFailed,
	ReparentRefusalCapabilityUnsupported,
	ReparentRefusalValidationFailed,
	ReparentRefusalConflictUnresolved,
	ReparentRefusalRefTransactionMismatch,
	ReparentRefusalRefForeignValue,
	ReparentRefusalMetadataPending,
	ReparentRefusalMetadataDrift,
	ReparentRefusalAbortForeignValue,
	ReparentRefusalRemoteFollowupUnsafeLease,
	ReparentRefusalNoWork,
}

// ReparentRecoveryPartialToken is the only v1 reparent-recovery: token. It is
// progress, not a refusal: it never sets a non-zero exit status and never
// appears in blockers[].
const ReparentRecoveryPartialToken = "ref-transaction-partial"

// reparentRefusalRank is ReparentRefusalKinds' own index, computed once so
// SelectPrimaryReparentRefusal's comparisons are O(1).
var reparentRefusalRank = buildReparentRefusalRank()

func buildReparentRefusalRank() map[ReparentRefusalKind]int {
	m := make(map[ReparentRefusalKind]int, len(ReparentRefusalKinds))
	for i, k := range ReparentRefusalKinds {
		m[k] = i
	}
	return m
}

// SelectPrimaryReparentRefusal sorts blockers by (rank, entry nil-first,
// entry, detail), removes exact duplicates, and returns the first blocker's
// kind together with the ordered slice every caller must publish verbatim as
// ReparentPlan.Blockers. It mirrors SelectPrimaryRefusal's contract over the
// reparent domain; the sync selector is untouched and keeps every caller.
func SelectPrimaryReparentRefusal(blockers []ReparentPlanBlocker) (kind ReparentRefusalKind, ordered []ReparentPlanBlocker) {
	ordered = sortAndDedupReparentBlockers(blockers)
	if len(ordered) == 0 {
		return "", ordered
	}
	return ordered[0].Kind, ordered
}

func sortAndDedupReparentBlockers(blockers []ReparentPlanBlocker) []ReparentPlanBlocker {
	cp := make([]ReparentPlanBlocker, len(blockers))
	copy(cp, blockers)
	sort.SliceStable(cp, func(i, j int) bool {
		a, b := cp[i], cp[j]
		if ra, rb := reparentRefusalRank[a.Kind], reparentRefusalRank[b.Kind]; ra != rb {
			return ra < rb
		}
		if (a.Entry == nil) != (b.Entry == nil) {
			return a.Entry == nil
		}
		if a.Entry != nil && b.Entry != nil && *a.Entry != *b.Entry {
			return *a.Entry < *b.Entry
		}
		return a.Detail < b.Detail
	})
	out := make([]ReparentPlanBlocker, 0, len(cp))
	for i, b := range cp {
		if i > 0 {
			p := cp[i-1]
			if p.Kind == b.Kind && blockerEntryEqual(p.Entry, b.Entry) && p.Detail == b.Detail {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// ============================================================================
// ReparentRefusalError — the typed carrier of a reparent-owned refusal
// ============================================================================

// ReparentRefusalError is how a reparent-owned refusal travels as a Go error
// before a document exists to publish it as a blocker. Its Error() composes
// exactly "<kind>: [state-preserved: ]<detail>", mirroring
// PlanGuardRefusalError, and carries NO "reparent: " prefix in either field:
// that marker is the CLI wrapper's own framing, applied once, so §13.2's
// one-refusal-line contract cannot be violated by double-prefixing.
type ReparentRefusalError struct {
	Kind           ReparentRefusalKind
	Entry          *string
	Detail         string
	StatePreserved bool
}

func (e *ReparentRefusalError) Error() string {
	detail := sanitizeReparentLine(e.Detail)
	if e.StatePreserved {
		return fmt.Sprintf("%s: state-preserved: %s", e.Kind, detail)
	}
	return fmt.Sprintf("%s: %s", e.Kind, detail)
}

// Blocker projects the error into the blockers[] row a plan publishes for it,
// so a refusal and its document row can never disagree about kind, entry or
// detail.
func (e *ReparentRefusalError) Blocker() ReparentPlanBlocker {
	return ReparentPlanBlocker{Kind: e.Kind, Entry: e.Entry, Detail: sanitizeReparentLine(e.Detail)}
}

// ReparentAnchoredError carries a reparent-owned refusal with §13.2's marker
// already applied, so an error that escapes into a SHIPPED command's generic
// `fmt.Fprintln(os.Stderr, err)` path still reaches the operator as one
// anchored line.
//
// The reparent command itself does NOT use this type: it applies the marker
// once, in its own writer, and returns the bare refusal. The push paths are
// different — they belong to `tws push` and `tws sync`, whose error rendering
// this feature must not change — so the marker travels inside the error there.
type ReparentAnchoredError struct {
	Refusal *ReparentRefusalError
}

func (e *ReparentAnchoredError) Error() string {
	return ReparentRefusalMarker + " " + e.Refusal.Error()
}

// Unwrap keeps errors.As(&ReparentRefusalError) working for every caller that
// classifies the refusal rather than printing it.
func (e *ReparentAnchoredError) Unwrap() error { return e.Refusal }

// ReparentRefusalMarker is §13.2's anchor, declared once so no call site can
// spell it differently.
const ReparentRefusalMarker = "reparent:"

// AnchorReparentRefusal wraps a reparent refusal exactly once. A nil error, an
// already-anchored error, and an error from another domain are all returned
// unchanged, so a push path that has no record emits byte-identical output.
func AnchorReparentRefusal(err error) error {
	if err == nil {
		return nil
	}
	var anchored *ReparentAnchoredError
	if errors.As(err, &anchored) {
		return err
	}
	var refusal *ReparentRefusalError
	if errors.As(err, &refusal) {
		return &ReparentAnchoredError{Refusal: refusal}
	}
	return err
}

// reparentRefusal builds a document-level refusal error.
func reparentRefusal(kind ReparentRefusalKind, detail string) *ReparentRefusalError {
	return &ReparentRefusalError{
		Kind: kind, Detail: sanitizeReparentLine(detail),
		StatePreserved: kind == ReparentRefusalHolderUnsafe,
	}
}

// reparentEntryRefusal builds an entry-scoped refusal error.
func reparentEntryRefusal(kind ReparentRefusalKind, entry, detail string) *ReparentRefusalError {
	name := entry
	return &ReparentRefusalError{
		Kind: kind, Entry: &name, Detail: sanitizeReparentLine(detail),
		StatePreserved: kind == ReparentRefusalHolderUnsafe,
	}
}

func sanitizeReparentLine(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

// SanitizeReparentLine is the render-boundary form used by package cli.
func SanitizeReparentLine(value string) string { return sanitizeReparentLine(value) }

// ============================================================================
// EvaluateReparentGuard (§8.1) — the pre-mutation admission verdict
// ============================================================================

// EvaluateReparentGuard is the reparent document's pre-mutation gate. It
// reproduces EvaluatePlanGuard's three-rung final ladder — refusal.kind, then
// runnable, then guard.execute_blocked_by[] — against ReparentPlan, and
// returns the identical *PlanGuardRefusalError, so the shipped plan-guard:
// rendering needs no change at all.
//
// It re-measures nothing: every verdict it reads was decided by the builder.
// g is consulted only to make this a safe no-op for an invocation that was
// never guarded; a plan-only invocation is never guarded, by CheckoutPlanGuard's
// own Guarded() definition.
func EvaluateReparentGuard(plan ReparentPlan, g CheckoutPlanGuard) error {
	if !g.Guarded() {
		return nil
	}
	if plan.Refusal.Kind != nil {
		detail := ""
		if plan.Refusal.Detail != nil {
			detail = *plan.Refusal.Detail
		}
		return &PlanGuardRefusalError{Kind: string(*plan.Refusal.Kind), Detail: detail}
	}
	if !plan.Runnable {
		return &PlanGuardRefusalError{Kind: string(RefusalStateRefused), Detail: "the described run is not runnable"}
	}
	if len(plan.Guard.ExecuteBlockedBy) > 0 {
		token := plan.Guard.ExecuteBlockedBy[0]
		return &PlanGuardRefusalError{
			Kind:   string(controlledPathBlockerKind(token)),
			Detail: controlledPathBlockerDetail(token),
		}
	}
	return nil
}

// ============================================================================
// ReparentWriters (§3.6) — the boundary's only writer surface
// ============================================================================

// ReparentWriters is the writer carrier every reparent entry point takes, so
// no function in package internal ever touches os.Stdout or os.Stderr for
// this feature. Doc carries the plan document and nothing else; Prose carries
// the mode header, fetch prose, progress, conflict instructions and every
// marker line. Package cli decides where each is ultimately connected.
//
// It mirrors PlanWriters, which has only Prose because the sync plan route
// renders its document in package cli; the reparent document is rendered in
// package internal, so the document stream must cross the boundary too.
type ReparentWriters struct {
	Doc   io.Writer
	Prose io.Writer
}
