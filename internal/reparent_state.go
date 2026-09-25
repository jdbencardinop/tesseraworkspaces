package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ============================================================================
// The reparent run's authoritative state artifact (§11.1, §11.5), its stage
// machine (§11.3), its compatibility envelope (§11.2) and the read-only
// projection every observability surface consumes (§11.10).
//
// The artifact written here is AUTHORITATIVE for every fact about a run. The
// compatibility artifacts exist only to make other binaries fail closed; no
// reparent decision is ever read out of them, which is why this file never
// decodes them into a shipped type.
// ============================================================================

// ReparentStateVersion is the state artifact's schema version. An artifact
// whose recorded version is greater refuses reparent-state-unsupported; one
// that fails to decode refuses reparent-state-corrupt.
const ReparentStateVersion = 1

// ReparentStage is the closed, ordered stage domain of §11.3.
type ReparentStage string

// The 19 stages, in §11.3 order. `fetching` and `planning` are deliberately
// absent: both happen before the lock and before the artifact exists, so no
// artifact can ever be found at either.
const (
	ReparentStageInitializing        ReparentStage = "initializing"
	ReparentStagePreflight           ReparentStage = "preflight"
	ReparentStagePinningPreimages    ReparentStage = "pinning-preimages"
	ReparentStageComputing           ReparentStage = "computing"
	ReparentStageConflictPaused      ReparentStage = "conflict-paused"
	ReparentStageBuildingPostImage   ReparentStage = "building-post-image"
	ReparentStagePinningComputed     ReparentStage = "pinning-computed"
	ReparentStageWritingRemoteRecord ReparentStage = "writing-remote-record"
	ReparentStageDetachingHolders    ReparentStage = "detaching-holders"
	ReparentStageCommittingRefs      ReparentStage = "committing-refs"
	ReparentStageRefsCommitted       ReparentStage = "refs-committed"
	ReparentStageWritingMetadata     ReparentStage = "writing-metadata"
	ReparentStageMetadataWritten     ReparentStage = "metadata-written"
	ReparentStageRestoringHolders    ReparentStage = "restoring-holders"
	ReparentStageCleanup             ReparentStage = "cleanup"
	ReparentStageCompleted           ReparentStage = "completed"
	ReparentStageAborting            ReparentStage = "aborting"
	ReparentStageAborted             ReparentStage = "aborted"
	ReparentStageFailed              ReparentStage = "failed"
)

// ReparentStages is the complete stage domain, in §11.3 order. It has exactly
// 19 members and its order is the forward order of a run; the four terminal
// or reversing stages (completed, aborting, aborted, failed) sit at the end
// and are never "later" than a forward stage for resume purposes.
var ReparentStages = []ReparentStage{
	ReparentStageInitializing,
	ReparentStagePreflight,
	ReparentStagePinningPreimages,
	ReparentStageComputing,
	ReparentStageConflictPaused,
	ReparentStageBuildingPostImage,
	ReparentStagePinningComputed,
	ReparentStageWritingRemoteRecord,
	ReparentStageDetachingHolders,
	ReparentStageCommittingRefs,
	ReparentStageRefsCommitted,
	ReparentStageWritingMetadata,
	ReparentStageMetadataWritten,
	ReparentStageRestoringHolders,
	ReparentStageCleanup,
	ReparentStageCompleted,
	ReparentStageAborting,
	ReparentStageAborted,
	ReparentStageFailed,
}

// reparentStageKnown is the membership test the loader uses: an artifact whose
// stage is outside the closed domain is corrupt, never "probably fine".
func reparentStageKnown(stage ReparentStage) bool {
	for _, s := range ReparentStages {
		if s == stage {
			return true
		}
	}
	return false
}

func reparentStageRank(stage ReparentStage) int {
	for i, candidate := range ReparentStages {
		if candidate == stage {
			return i
		}
	}
	return -1
}

// ReparentRowStage is one row's own progress: the closed five-member domain
// of §11.5's rows[].stage.
type ReparentRowStage string

const (
	ReparentRowPending   ReparentRowStage = "pending"
	ReparentRowComputed  ReparentRowStage = "computed"
	ReparentRowPinned    ReparentRowStage = "pinned"
	ReparentRowValidated ReparentRowStage = "validated"
	ReparentRowCommitted ReparentRowStage = "committed"
)

// ReparentRowStages is the complete row-stage domain, in forward order.
var ReparentRowStages = []ReparentRowStage{
	ReparentRowPending,
	ReparentRowComputed,
	ReparentRowPinned,
	ReparentRowValidated,
	ReparentRowCommitted,
}

// reparentRowStageRank orders the row stages so "at least validated" is a
// comparison rather than a chain of equality tests.
func reparentRowStageRank(stage ReparentRowStage) int {
	for i, s := range ReparentRowStages {
		if s == stage {
			return i
		}
	}
	return -1
}

// The two constant state facts v1 publishes for every run: exactly one
// strategy and exactly one rebase backend (§9.1, §9.4).
const (
	ReparentStateStrategy = ReparentStrategyKind
	ReparentStateBackend  = ReparentRowBackend
)

// The failure domains of §11.5. A failure is attributed to exactly one.
const (
	ReparentFailureDomainReparent  = "reparent"
	ReparentFailureDomainPlanGuard = "plan-guard"
	ReparentFailureDomainNativeGit = "native-git"
	ReparentFailureDomainIO        = "io"
)

// ============================================================================
// Paths (§11.1, §11.2, P-5)
// ============================================================================

// ReparentLocation is where one feature's reparent artifacts live. It is
// resolved once, from the workspace, and carried everywhere, because the two
// checkout directories the tree offers are NOT interchangeable: the
// authoritative artifact uses Workspace.CheckoutStateDir() (MetadataRoot
// derived, correct for the legacy .tws/<feature> layout too), while the
// compatibility transaction and lock must keep the feature-path-derived
// CheckoutTransactionPath/CheckoutLockPath so shipped loaders still find
// them.
type ReparentLocation struct {
	// Mode is the resolved workspace mode; it selects both the artifact path
	// and the compatibility envelope.
	Mode WorkspaceMode

	// Feature is the logical feature name.
	Feature string

	// FeaturePath is the feature directory. External artifacts live directly
	// inside it; in checkout mode it is used only to derive the shipped
	// compatibility paths.
	FeaturePath string

	// CheckoutStateDir is Workspace.CheckoutStateDir(). It is consulted only
	// in checkout mode and is deliberately NOT checkoutStateDir(FeaturePath).
	CheckoutStateDir string

	// StableID is the workspace identity a foreign artifact is detected with.
	StableID string
}

// ReparentLocationFor resolves the artifact location for one feature of one
// workspace.
func ReparentLocationFor(ws Workspace, feature, featurePath string) ReparentLocation {
	return ReparentLocation{
		Mode:             ws.Mode,
		Feature:          feature,
		FeaturePath:      featurePath,
		CheckoutStateDir: ws.CheckoutStateDir(),
		StableID:         ws.StableID,
	}
}

// ReparentStatePath is the authoritative artifact's path: the feature
// directory in external mode, the workspace state directory in checkout mode.
func ReparentStatePath(loc ReparentLocation) string {
	if loc.Mode == ModeCheckout {
		return filepath.Join(loc.CheckoutStateDir, loc.Feature+"-reparent.v1.yaml")
	}
	return filepath.Join(loc.FeaturePath, ".reparent-state.v1.yaml")
}

// ReparentRemoteRecordPath is the pending follow-up record's path (§12.3). It
// is declared here beside the state path because the two share the same
// mode-derived directory rule, and a reader comparing them must see them
// together.
func ReparentRemoteRecordPath(loc ReparentLocation) string {
	if loc.Mode == ModeCheckout {
		return filepath.Join(loc.CheckoutStateDir, loc.Feature+"-reparent-remote.v1.yaml")
	}
	return filepath.Join(loc.FeaturePath, ".reparent-remote.v1.yaml")
}

// ReparentScratchRoot is the external computation worktree's parent
// directory. It sits beside <featurePath>/worktrees/, never inside it, which
// is what keeps externalSyncLayout.WorktreePath's derivation collision-free.
func ReparentScratchRoot(loc ReparentLocation) string {
	return filepath.Join(loc.FeaturePath, ".reparent")
}

// ReparentScratchPath is one run's scratch worktree path (§9.2).
func ReparentScratchPath(loc ReparentLocation, runID string) string {
	return filepath.Join(ReparentScratchRoot(loc), runID, "scratch")
}

func canonicalReparentScratchPath(loc ReparentLocation, runID string) string {
	return filepath.Join(canonicalize(loc.FeaturePath), ".reparent", runID, "scratch")
}

// ============================================================================
// §11.5 payload
// ============================================================================

// ReparentStateRow is one closure row's persisted facts. Every field that can
// only be known after a computation starts empty and is filled durably as it
// becomes true, so a crashed run is describable from this artifact alone.
type ReparentStateRow struct {
	Order     int    `yaml:"order"`
	Name      string `yaml:"name"`
	GitBranch string `yaml:"git_branch"`
	Repo      string `yaml:"repo,omitempty"`
	Role      string `yaml:"role"`

	PreimageSHA string `yaml:"preimage_sha"`

	CutoffSHA        string `yaml:"cutoff_sha"`
	CutoffProvenance string `yaml:"cutoff_provenance"`

	DestinationBinding string `yaml:"destination_binding"`
	DestinationParent  string `yaml:"destination_parent,omitempty"`
	DestinationSHA     string `yaml:"destination_sha,omitempty"`

	PlannedNewSHA string `yaml:"planned_new_sha,omitempty"`
	Noop          bool   `yaml:"noop"`

	BaseBefore string `yaml:"base_before"`
	BaseAfter  string `yaml:"base_after"`

	LastBaseSHABefore string `yaml:"last_base_sha_before,omitempty"`
	LastBaseSHAAfter  string `yaml:"last_base_sha_after,omitempty"`

	Argv             []string `yaml:"argv"`
	MaterializedArgv []string `yaml:"materialized_argv,omitempty"`
	EffectiveBackend string   `yaml:"effective_backend"`

	CandidateCount  int    `yaml:"candidate_count"`
	CandidateDigest string `yaml:"candidate_digest,omitempty"`

	// RevalidationDigest is the APPROVED row's §8.3 digest, frozen at
	// begin-run. The just-in-time seam recomputes the same digest over a
	// freshly re-probed row and compares; it never overwrites this value,
	// because the thing being compared against must be what was approved.
	RevalidationDigest string `yaml:"revalidation_digest,omitempty"`

	ConflictReflogAnchor       string `yaml:"conflict_reflog_anchor,omitempty"`
	ConflictCompletionRef      string `yaml:"conflict_completion_ref,omitempty"`
	ConflictCompletionEvidence string `yaml:"conflict_completion_evidence,omitempty"`

	OldPinRef string `yaml:"old_pin_ref"`
	NewPinRef string `yaml:"new_pin_ref"`

	Stage ReparentRowStage `yaml:"stage"`
}

// ReparentStateHolder is one worktree this run detached, and whether it has
// been put back. restore_reason is the operator-facing explanation a deferred
// restore carries (§9.9).
type ReparentStateHolder struct {
	Path            string                      `yaml:"path"`
	RepoCommonDir   string                      `yaml:"repo_common_dir"`
	GitBranch       string                      `yaml:"git_branch"`
	PreimageSHA     string                      `yaml:"preimage_sha"`
	DetachTargetSHA string                      `yaml:"detach_target_sha,omitempty"`
	HolderKind      string                      `yaml:"holder_kind,omitempty"`
	DetachStatus    string                      `yaml:"detach_status,omitempty"`
	Restored        bool                        `yaml:"restored"`
	RestoreReason   string                      `yaml:"restore_reason,omitempty"`
	SwitchIntent    *ReparentHolderSwitchIntent `yaml:"switch_intent,omitempty"`
}

// ReparentHolderSwitchIntent is the durable source→destination identity for
// one checkout/worktree switch. A recovery accepts exactly either side: the
// source means the switch never ran and may be retried; the destination means
// it ran and only the completion write was lost.
type ReparentHolderSwitchIntent struct {
	Action            string `yaml:"action"`
	SourceBranch      string `yaml:"source_branch,omitempty"`
	SourceSHA         string `yaml:"source_sha"`
	DestinationBranch string `yaml:"destination_branch,omitempty"`
	DestinationSHA    string `yaml:"destination_sha"`
}

type ReparentApprovedHolder struct {
	Path          string `yaml:"path"`
	RepoCommonDir string `yaml:"repo_common_dir"`
	GitBranch     string `yaml:"git_branch"`
	HeadSHA       string `yaml:"head_sha"`
	HolderKind    string `yaml:"holder_kind"`
	Excluded      bool   `yaml:"excluded,omitempty"`
}

const (
	ReparentHolderDetachIntent   = "detach-intent"
	ReparentHolderDetached       = "detached"
	ReparentHolderRedetachIntent = "redetach-intent"
	ReparentHolderRedetached     = "redetached"

	ReparentHolderSwitchDetach   = "detach"
	ReparentHolderSwitchCompute  = "compute"
	ReparentHolderSwitchRestore  = "restore"
	ReparentHolderSwitchRedetach = "redetach"
)

// ReparentStateCASRow is one line of the forward CAS transaction, persisted
// with its outcome. A no-op row carries Noop true and was represented by a
// `verify` line, never by `update <ref> X X`.
type ReparentStateCASRow struct {
	Ref      string `yaml:"ref"`
	OldValue string `yaml:"old_value"`
	NewValue string `yaml:"new_value"`
	Applied  bool   `yaml:"applied"`
	Noop     bool   `yaml:"noop"`
}

// ReparentStateAbortRow is one row's rollback outcome, persisted before the
// next action so a crashed abort resumes idempotently (§11.8).
type ReparentStateAbortRow struct {
	Ref            string `yaml:"ref"`
	Restored       bool   `yaml:"restored"`
	Classification string `yaml:"classification"`
	Detail         string `yaml:"detail,omitempty"`
}

// ReparentState is the §11.5 payload. It is the only place the raw frozen
// validation command is ever written, which is why the artifact is 0600 and
// why no plan field carries it.
type ReparentState struct {
	StateVersion int    `yaml:"state_version"`
	RunID        string `yaml:"run_id"`
	CreatedAt    string `yaml:"created_at"`
	UpdatedAt    string `yaml:"updated_at"`

	WorkspaceMode     string `yaml:"workspace_mode"`
	WorkspaceStableID string `yaml:"workspace_stable_id"`
	Feature           string `yaml:"feature"`
	WorkspaceRepoRoot string `yaml:"workspace_repo_root"`
	RepoRoot          string `yaml:"repo_root"`
	RepoCommonDir     string `yaml:"repo_common_dir"`

	RefBackend string `yaml:"ref_backend"`
	OIDWidth   int    `yaml:"oid_width"`
	Strategy   string `yaml:"strategy"`
	Backend    string `yaml:"backend"`

	Stage       ReparentStage `yaml:"stage"`
	ResumeStage ReparentStage `yaml:"resume_stage"`

	FailureDomain string `yaml:"failure_domain,omitempty"`
	FailureKind   string `yaml:"failure_kind,omitempty"`
	FailureDetail string `yaml:"failure_detail,omitempty"`

	OwnerPID   int    `yaml:"owner_pid"`
	OwnerToken string `yaml:"owner_token"`

	TargetName      string `yaml:"target_name"`
	TargetGitBranch string `yaml:"target_git_branch"`

	OldParentRequestedToken string `yaml:"old_parent_requested_token,omitempty"`
	OldParentStoredToken    string `yaml:"old_parent_stored_token,omitempty"`
	OldParentKind           string `yaml:"old_parent_kind"`
	OldParentRef            string `yaml:"old_parent_ref,omitempty"`
	OldParentSHA            string `yaml:"old_parent_sha,omitempty"`

	NewParentRequestedToken string `yaml:"new_parent_requested_token,omitempty"`
	NewParentStoredToken    string `yaml:"new_parent_stored_token,omitempty"`
	NewParentKind           string `yaml:"new_parent_kind"`
	NewParentRef            string `yaml:"new_parent_ref,omitempty"`
	NewParentSHA            string `yaml:"new_parent_sha,omitempty"`

	DestinationPinRef   string `yaml:"destination_pin_ref"`
	OriginalHeadPinRef  string `yaml:"original_head_pin_ref,omitempty"`
	CutoffSuppliedToken string `yaml:"cutoff_supplied_token,omitempty"`

	Rows []ReparentStateRow `yaml:"rows"`

	// ValidationCommandRaw is 0600-artifact-only and is NEVER a plan field.
	ValidationCommandRaw    string `yaml:"validation_command_raw,omitempty"`
	ValidationSource        string `yaml:"validation_source"`
	ValidationCommandDigest string `yaml:"validation_command_digest,omitempty"`

	ScratchPath      string `yaml:"scratch_path,omitempty"`
	OriginalBranch   string `yaml:"original_branch"`
	OriginalHead     string `yaml:"original_head"`
	OriginalDetached bool   `yaml:"original_detached"`

	PreimageHolderExcluded string                   `yaml:"preimage_holder_excluded,omitempty"`
	ApprovedHolders        []ReparentApprovedHolder `yaml:"approved_holders"`
	DetachedHolders        []ReparentStateHolder    `yaml:"detached_holders"`

	StackBeforeBase64        string `yaml:"stack_before_base64"`
	StackAfterBase64         string `yaml:"stack_after_base64,omitempty"`
	StackSHA256Before        string `yaml:"stack_sha256_before"`
	StackSHA256AfterExpected string `yaml:"stack_sha256_after_expected,omitempty"`
	StackSHA256After         string `yaml:"stack_sha256_after,omitempty"`

	// CommitPointReached is monotonic: once true it is never cleared, even if
	// an operator later advances a branch (§11.8a).
	CommitPointReached bool `yaml:"commit_point_reached"`

	// CASTransactionSucceeded is persisted only after update-ref returns
	// success. In particular, a crash before the child starts and a
	// reference-transaction hook veto leave it false, so an all-no-op run
	// re-issues its verify transaction on --continue.
	CASTransactionSucceeded bool `yaml:"cas_transaction_succeeded"`

	CASRows   []ReparentStateCASRow   `yaml:"cas_rows"`
	AbortRows []ReparentStateAbortRow `yaml:"abort_rows"`

	RemoteRecordWritten bool `yaml:"remote_record_written"`

	// RemoteRecordEmpty is the "provably empty" half of §12.3a's precondition:
	// every entry this run would have recorded was written cleared, so there
	// is no record to keep and none to clean up. The CAS gate accepts either
	// a durable record or this proof, never neither.
	RemoteRecordEmpty bool `yaml:"remote_record_empty"`

	// The exact record that existed before this run replaced any same-name
	// entries. A pre-commit abort restores these bytes verbatim, so an older
	// pending protection is never lost merely because a newer run began.
	RemoteRecordBeforeCaptured        bool   `yaml:"remote_record_before_captured"`
	RemoteRecordBeforePresent         bool   `yaml:"remote_record_before_present"`
	RemoteRecordBeforeBase64          string `yaml:"remote_record_before_base64,omitempty"`
	RemoteRecordRestorePending        bool   `yaml:"remote_record_restore_pending,omitempty"`
	RemoteRecordRestoreSourceCaptured bool   `yaml:"remote_record_restore_source_captured,omitempty"`
	RemoteRecordRestoreSourcePresent  bool   `yaml:"remote_record_restore_source_present,omitempty"`
	RemoteRecordRestoreSourceBase64   string `yaml:"remote_record_restore_source_base64,omitempty"`

	RemoteClearPending       bool   `yaml:"remote_clear_pending,omitempty"`
	RemoteClearSourcePresent bool   `yaml:"remote_clear_source_present,omitempty"`
	RemoteClearSourceBase64  string `yaml:"remote_clear_source_base64,omitempty"`
	RemoteClearTargetPresent bool   `yaml:"remote_clear_target_present,omitempty"`
	RemoteClearTargetBase64  string `yaml:"remote_clear_target_base64,omitempty"`

	RemoteFollowupEntries  []string `yaml:"remote_followup_entries"`
	HolderRestoreDeferred  []string `yaml:"holder_restore_deferred,omitempty"`
	CompatArtifactsWritten bool     `yaml:"compat_artifacts_written"`

	MaxReplayPerEntry   *int          `yaml:"max_replay_per_entry,omitempty"`
	MaxReplayTotal      *int          `yaml:"max_replay_total,omitempty"`
	ApprovedFingerprint string        `yaml:"approved_fingerprint,omitempty"`
	WaivedEvaluationIDs []string      `yaml:"waived_evaluation_ids,omitempty"`
	WaivedKinds         []RefusalKind `yaml:"waived_kinds,omitempty"`
	FetchPolicy         string        `yaml:"fetch_policy"`
}

// Row returns a pointer to the named row, so a caller mutates the persisted
// row itself rather than a copy that is silently dropped.
func (s *ReparentState) Row(name string) *ReparentStateRow {
	for i := range s.Rows {
		if s.Rows[i].Name == name {
			return &s.Rows[i]
		}
	}
	return nil
}

// RowsInOrder returns the rows sorted by their persisted order field, which
// is ReparentClosureOrder's order and therefore also the CAS line order.
func (s *ReparentState) RowsInOrder() []ReparentStateRow {
	out := make([]ReparentStateRow, len(s.Rows))
	copy(out, s.Rows)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Order < out[j-1].Order; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// reparentStageIsTerminalish reports whether resume_stage must retain the
// last forward stage rather than track stage (§11.5).
func reparentStageIsTerminalish(stage ReparentStage) bool {
	switch stage {
	case ReparentStageFailed, ReparentStageAborting, ReparentStageAborted, ReparentStageCompleted:
		return true
	}
	return false
}

// SetStage moves the run to stage and keeps resume_stage correct in the same
// value, so the durable write that persists one persists both (§11.5).
// resume_stage equals stage except in the four terminal-or-reversing stages,
// where it retains the last forward stage recovery proceeds from.
func (s *ReparentState) SetStage(stage ReparentStage) {
	if !reparentStageIsTerminalish(stage) {
		s.ResumeStage = stage
	} else if s.ResumeStage == "" || reparentStageIsTerminalish(s.ResumeStage) {
		// A terminal stage reached with no recorded forward stage keeps the
		// stage it came from, never an empty resume target.
		if !reparentStageIsTerminalish(s.Stage) && s.Stage != "" {
			s.ResumeStage = s.Stage
		}
	}
	s.Stage = stage
}

// ============================================================================
// Durable load / save / remove
// ============================================================================

// ReparentStateLoadKind classifies why a load failed, so a caller maps the
// answer to exactly one refusal kind instead of string-matching an error.
type ReparentStateLoadKind string

const (
	ReparentStateAbsent      ReparentStateLoadKind = "absent"
	ReparentStateOK          ReparentStateLoadKind = "ok"
	ReparentStateUnsupported ReparentStateLoadKind = "unsupported"
	ReparentStateCorrupt     ReparentStateLoadKind = "corrupt"
	ReparentStateForeign     ReparentStateLoadKind = "foreign"
)

// ReparentStateLoad is one classification of the artifact on disk. State is
// non-nil only for ok, unsupported and foreign: a corrupt artifact has no
// trustworthy contents at all.
type ReparentStateLoad struct {
	Kind   ReparentStateLoadKind
	Path   string
	State  *ReparentState
	Detail string
}

// Refusal projects a non-ok load into the reparent refusal it always means.
// It returns nil for absent and ok, so a caller writes one `if err != nil`.
func (l ReparentStateLoad) Refusal() *ReparentRefusalError {
	switch l.Kind {
	case ReparentStateUnsupported:
		return reparentRefusal(ReparentRefusalStateUnsupported, l.Detail)
	case ReparentStateCorrupt:
		return reparentRefusal(ReparentRefusalStateCorrupt, l.Detail)
	case ReparentStateForeign:
		return reparentRefusal(ReparentRefusalStateForeign, l.Detail)
	}
	return nil
}

// LoadReparentState reads and classifies the authoritative artifact. An
// absent artifact is not an error: it is the ordinary "no run here" answer
// every fresh route needs.
func LoadReparentState(loc ReparentLocation) ReparentStateLoad {
	path := ReparentStatePath(loc)
	data, err := readReparentStateFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReparentStateLoad{Kind: ReparentStateAbsent, Path: path}
		}
		return ReparentStateLoad{
			Kind:   ReparentStateCorrupt,
			Path:   path,
			Detail: fmt.Sprintf("the reparent state artifact at %s could not be read: %v", path, err),
		}
	}

	// The version is read on its own first: an artifact from a future release
	// is unsupported, not corrupt, even when the rest of its shape is one this
	// binary cannot decode.
	var probe struct {
		StateVersion int `yaml:"state_version"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return ReparentStateLoad{
			Kind:   ReparentStateCorrupt,
			Path:   path,
			Detail: fmt.Sprintf("the reparent state artifact at %s does not decode: %v", path, err),
		}
	}
	if probe.StateVersion > ReparentStateVersion {
		return ReparentStateLoad{
			Kind:   ReparentStateUnsupported,
			Path:   path,
			Detail: fmt.Sprintf("the reparent state artifact at %s records state_version %d; this release supports %d", path, probe.StateVersion, ReparentStateVersion),
		}
	}

	var st ReparentState
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&st); err != nil {
		return ReparentStateLoad{
			Kind:   ReparentStateCorrupt,
			Path:   path,
			Detail: fmt.Sprintf("the reparent state artifact at %s does not decode: %v", path, err),
		}
	}
	if st.StateVersion <= 0 || st.RunID == "" || !reparentStageKnown(st.Stage) {
		return ReparentStateLoad{
			Kind:   ReparentStateCorrupt,
			Path:   path,
			Detail: fmt.Sprintf("the reparent state artifact at %s is incomplete: state_version %d, run %q, stage %q", path, st.StateVersion, st.RunID, st.Stage),
		}
	}
	if err := validateReparentStateSemantics(loc, &st); err != nil {
		return ReparentStateLoad{
			Kind:   ReparentStateCorrupt,
			Path:   path,
			Detail: fmt.Sprintf("the reparent state artifact at %s is invalid: %v", path, err),
		}
	}
	if st.Feature != loc.Feature {
		return ReparentStateLoad{
			Kind:   ReparentStateForeign,
			Path:   path,
			State:  &st,
			Detail: fmt.Sprintf("the reparent state artifact at %s belongs to feature %q, not %q", path, st.Feature, loc.Feature),
		}
	}
	if loc.StableID != "" && st.WorkspaceStableID != "" && st.WorkspaceStableID != loc.StableID {
		return ReparentStateLoad{
			Kind:   ReparentStateForeign,
			Path:   path,
			State:  &st,
			Detail: fmt.Sprintf("the reparent state artifact at %s belongs to workspace %s, not %s", path, st.WorkspaceStableID, loc.StableID),
		}
	}
	if st.WorkspaceMode != "" && st.WorkspaceMode != string(loc.Mode) {
		return ReparentStateLoad{
			Kind:   ReparentStateForeign,
			Path:   path,
			State:  &st,
			Detail: fmt.Sprintf("the reparent state artifact at %s was written in %s mode, not %s", path, st.WorkspaceMode, loc.Mode),
		}
	}
	return ReparentStateLoad{Kind: ReparentStateOK, Path: path, State: &st}
}

func readReparentStateFile(path string) ([]byte, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close() //nolint:errcheck
	name := filepath.Base(path)
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("state artifact is not a regular non-symlink file")
	}
	data, err := parent.ReadFile(name)
	if err != nil {
		return nil, err
	}
	after, err := parent.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(info, after) {
		return nil, fmt.Errorf("state artifact changed while being read")
	}
	return data, nil
}

func validateReparentStateSemantics(loc ReparentLocation, st *ReparentState) error {
	if st == nil {
		return fmt.Errorf("state is nil")
	}
	if st.StateVersion != ReparentStateVersion {
		return fmt.Errorf("state_version must equal %d", ReparentStateVersion)
	}
	if !reparentRunIDShape(st.RunID) || !reparentRunIDShape(st.OwnerToken) {
		return fmt.Errorf("run_id and owner_token must be 32 lowercase hex characters")
	}
	if _, err := time.Parse(time.RFC3339, st.CreatedAt); err != nil {
		return fmt.Errorf("created_at must be RFC3339")
	}
	if st.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, st.UpdatedAt); err != nil {
			return fmt.Errorf("updated_at must be RFC3339")
		}
	}
	if st.OwnerPID <= 0 || !reparentStageKnown(st.Stage) ||
		(st.ResumeStage != "" && !reparentStageKnown(st.ResumeStage)) {
		return fmt.Errorf("owner_pid and stage fields are invalid")
	}
	if err := validateReparentStateStage(st); err != nil {
		return err
	}
	if st.Strategy != ReparentStateStrategy || st.Backend != ReparentStateBackend ||
		(st.RefBackend != ReparentRefBackendFiles &&
			st.RefBackend != ReparentRefBackendReftable &&
			st.RefBackend != ReparentRefBackendUnknown) {
		return fmt.Errorf("strategy or ref backend is outside the supported domain")
	}
	if st.OIDWidth != 40 && st.OIDWidth != 64 {
		return fmt.Errorf("oid_width must be 40 or 64")
	}
	if !reparentCanonicalAbsolutePath(st.WorkspaceRepoRoot) ||
		!reparentCanonicalAbsolutePath(st.RepoRoot) ||
		!reparentCanonicalAbsolutePath(st.RepoCommonDir) {
		return fmt.Errorf("repository paths must be canonical absolute paths")
	}
	if loc.Mode == ModeExternal {
		if st.ScratchPath != canonicalReparentScratchPath(loc, st.RunID) {
			return fmt.Errorf("scratch_path is outside this run's feature namespace")
		}
	} else if st.ScratchPath != "" {
		return fmt.Errorf("checkout state cannot name a scratch worktree")
	}
	if st.OriginalHead == "" || !reparentStateOID(st.OriginalHead, st.OIDWidth) {
		return fmt.Errorf("original_head is not a valid object id")
	}
	if st.OriginalDetached {
		if st.OriginalHeadPinRef != reparentOriginalHeadPinRef(st.RunID) {
			return fmt.Errorf("original_head_pin_ref is not this run's pin")
		}
	} else if st.OriginalHeadPinRef != "" {
		return fmt.Errorf("attached original checkout cannot carry original_head_pin_ref")
	}
	if st.DestinationPinRef != reparentDestPinRef(st.RunID) {
		return fmt.Errorf("destination_pin_ref is not this run's destination pin")
	}
	if !validReparentStateName(st.TargetName) ||
		!reparentStateRef("refs/heads/"+st.TargetGitBranch) {
		return fmt.Errorf("target identity is invalid")
	}
	for label, kind := range map[string]string{
		"old_parent_kind": st.OldParentKind,
		"new_parent_kind": st.NewParentKind,
	} {
		if kind != ReparentParentKindStackEntry &&
			kind != ReparentParentKindLiteralRef &&
			kind != ReparentParentKindNone {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	for label, ref := range map[string]string{
		"old_parent_ref": st.OldParentRef,
		"new_parent_ref": st.NewParentRef,
	} {
		if ref != "" && !reparentStateRef(ref) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	if st.OriginalBranch != "" && !reparentStateRef("refs/heads/"+st.OriginalBranch) {
		return fmt.Errorf("original_branch is invalid")
	}
	for label, oid := range map[string]string{
		"old_parent_sha": st.OldParentSHA,
		"new_parent_sha": st.NewParentSHA,
	} {
		if oid != "" && !reparentStateOID(oid, st.OIDWidth) {
			return fmt.Errorf("%s is not a valid object id", label)
		}
	}
	if st.ApprovedFingerprint == "" || !reparentStateHex(st.ApprovedFingerprint, 64) {
		return fmt.Errorf("approved_fingerprint must be 64 lowercase hex characters")
	}
	if st.FetchPolicy != string(SyncFetchEnabled) && st.FetchPolicy != string(SyncFetchDisabled) {
		return fmt.Errorf("fetch_policy is invalid")
	}
	if err := validateReparentStateValidation(st); err != nil {
		return err
	}
	stackBytes, err := base64.StdEncoding.DecodeString(st.StackBeforeBase64)
	if err != nil {
		return fmt.Errorf("stack_before_base64 does not decode")
	}
	if got := hex.EncodeToString(reparentStateSHA256(stackBytes)); got != st.StackSHA256Before {
		return fmt.Errorf("stack_before_base64 does not match stack_sha256_before")
	}
	var stack Stack
	stackDecoder := yaml.NewDecoder(bytes.NewReader(stackBytes))
	stackDecoder.KnownFields(true)
	if err := stackDecoder.Decode(&stack); err != nil {
		return fmt.Errorf("stack_before_base64 is not valid stack.yaml: %w", err)
	}
	if err := validateReparentStateRows(loc, st, stack); err != nil {
		return err
	}
	if err := validateReparentStateMetadata(st, stack); err != nil {
		return err
	}
	if err := validateReparentStateHolders(st); err != nil {
		return err
	}
	if err := validateReparentStatePostImage(st); err != nil {
		return err
	}
	if err := validateReparentStateTransactions(st); err != nil {
		return err
	}
	if err := validateReparentStateRemote(loc, st); err != nil {
		return err
	}
	return nil
}

func reparentStateEffectiveStage(st *ReparentState) ReparentStage {
	if st == nil {
		return ""
	}
	if reparentStageIsTerminalish(st.Stage) {
		return st.ResumeStage
	}
	return st.Stage
}

func validateReparentStateStage(st *ReparentState) error {
	terminal := reparentStageIsTerminalish(st.Stage)
	if !terminal && st.ResumeStage != st.Stage {
		return fmt.Errorf("resume_stage must equal stage while the run is forward-moving")
	}
	if terminal && (st.ResumeStage == "" || reparentStageIsTerminalish(st.ResumeStage)) {
		return fmt.Errorf("terminal stage must retain a forward resume_stage")
	}
	anyFailure := st.FailureDomain != "" || st.FailureKind != "" || st.FailureDetail != ""
	if anyFailure {
		switch st.FailureDomain {
		case ReparentFailureDomainReparent, ReparentFailureDomainPlanGuard,
			ReparentFailureDomainNativeGit, ReparentFailureDomainIO:
		default:
			return fmt.Errorf("failure_domain is invalid")
		}
		if st.FailureKind == "" || st.FailureDetail == "" {
			return fmt.Errorf("failure fields must be complete")
		}
	}
	if st.Stage == ReparentStageFailed && !anyFailure {
		return fmt.Errorf("failed stage requires failure evidence")
	}
	if st.Stage != ReparentStageFailed &&
		st.Stage != ReparentStageAborting &&
		st.Stage != ReparentStageAborted &&
		anyFailure {
		return fmt.Errorf("failure evidence is only valid on failed or aborting state")
	}
	return nil
}

func validateReparentStateValidation(st *ReparentState) error {
	switch st.ValidationSource {
	case "none":
		if st.ValidationCommandRaw != "" || st.ValidationCommandDigest != "" {
			return fmt.Errorf("validation source none cannot carry a command")
		}
	case "config-workspace", "config-repo":
		if strings.TrimSpace(st.ValidationCommandRaw) == "" ||
			st.ValidationCommandDigest != ValidationDigest(st.ValidationCommandRaw) {
			return fmt.Errorf("validation command and digest do not agree")
		}
	default:
		return fmt.Errorf("validation_source is invalid")
	}
	return nil
}

func validateReparentStateRows(loc ReparentLocation, st *ReparentState, stack Stack) error {
	if len(st.Rows) == 0 {
		return fmt.Errorf("rows must be non-empty")
	}
	stackByName := make(map[string]StackEntry, len(stack.Branches))
	for _, entry := range stack.Branches {
		stackByName[entry.Name] = entry
	}
	seenNames := map[string]bool{}
	seenBranches := map[string]bool{}
	seenOrders := map[int]bool{}
	for i := range st.Rows {
		row := &st.Rows[i]
		if row.Order < 0 || row.Order >= len(st.Rows) || seenOrders[row.Order] {
			return fmt.Errorf("rows contain an invalid or duplicate order")
		}
		seenOrders[row.Order] = true
		if !validReparentStateName(row.Name) || !reparentStateRef("refs/heads/"+row.GitBranch) ||
			seenNames[row.Name] || seenBranches[row.GitBranch] {
			return fmt.Errorf("rows contain an invalid or duplicate identity")
		}
		seenNames[row.Name], seenBranches[row.GitBranch] = true, true
		entry, ok := stackByName[row.Name]
		if !ok || entry.GitBranch() != row.GitBranch || entry.Repo != row.Repo {
			return fmt.Errorf("row %q does not match the captured stack", row.Name)
		}
		if row.Order == 0 {
			if row.Role != ReparentRoleTarget || row.Name != st.TargetName || row.GitBranch != st.TargetGitBranch {
				return fmt.Errorf("row zero is not the recorded target")
			}
		} else if row.Role != ReparentRoleDescendant {
			return fmt.Errorf("non-target row %q has invalid role", row.Name)
		}
		if reparentRowStageRank(row.Stage) < 0 ||
			!reparentStateOID(row.PreimageSHA, st.OIDWidth) ||
			!reparentStateOID(row.CutoffSHA, st.OIDWidth) {
			return fmt.Errorf("row %q has invalid stage or object ids", row.Name)
		}
		if row.PlannedNewSHA != "" && !reparentStateOID(row.PlannedNewSHA, st.OIDWidth) {
			return fmt.Errorf("row %q has invalid planned_new_sha", row.Name)
		}
		if row.PlannedNewSHA == "" && row.Noop {
			return fmt.Errorf("row %q is marked noop without a planned tip", row.Name)
		}
		if row.PlannedNewSHA != "" && row.Noop != (row.PlannedNewSHA == row.PreimageSHA) {
			return fmt.Errorf("row %q noop does not match its planned tip", row.Name)
		}
		if row.Stage == ReparentRowPending && (row.PlannedNewSHA != "" || row.Noop) {
			return fmt.Errorf("pending row %q carries computed results", row.Name)
		}
		if reparentRowStageRank(row.Stage) >= reparentRowStageRank(ReparentRowComputed) &&
			row.PlannedNewSHA == "" {
			return fmt.Errorf("row %q reached %s without planned_new_sha", row.Name, row.Stage)
		}
		if reparentRowStageRank(row.Stage) >= reparentRowStageRank(ReparentRowComputed) &&
			len(row.MaterializedArgv) == 0 {
			return fmt.Errorf("row %q reached %s without materialized argv", row.Name, row.Stage)
		}
		if row.DestinationSHA != "" && !reparentStateOID(row.DestinationSHA, st.OIDWidth) {
			return fmt.Errorf("row %q has invalid destination_sha", row.Name)
		}
		if row.DestinationBinding != ReparentBindingPinned &&
			row.DestinationBinding != ReparentBindingParentComputed {
			return fmt.Errorf("row %q has invalid destination binding", row.Name)
		}
		if row.DestinationBinding == ReparentBindingPinned {
			if row.Order != 0 || row.DestinationSHA == "" || row.DestinationParent != "" {
				return fmt.Errorf("pinned row %q has inconsistent destination fields", row.Name)
			}
		} else {
			parent := st.Row(row.DestinationParent)
			if parent == nil || parent.Order >= row.Order {
				return fmt.Errorf("row %q has invalid destination parent", row.Name)
			}
		}
		if row.EffectiveBackend != ReparentRowBackend || row.CandidateCount < 0 {
			return fmt.Errorf("row %q has invalid backend or candidate count", row.Name)
		}
		if row.CandidateDigest != "" && !reparentStateHex(row.CandidateDigest, 64) {
			return fmt.Errorf("row %q has invalid candidate digest", row.Name)
		}
		if row.RevalidationDigest != "" && !reparentStateHex(row.RevalidationDigest, 64) {
			return fmt.Errorf("row %q has invalid revalidation digest", row.Name)
		}
		entryID := ReparentEntryRefID(loc.Feature, row.Name)
		if row.OldPinRef != reparentOldPinRef(st.RunID, entryID) ||
			row.NewPinRef != reparentNewPinRef(st.RunID, entryID) {
			return fmt.Errorf("row %q carries a pin outside this run", row.Name)
		}
		if row.ConflictCompletionRef != "" &&
			row.ConflictCompletionRef != reparentConflictCompletionRef(st.RunID, entryID) {
			return fmt.Errorf("row %q carries a foreign conflict completion ref", row.Name)
		}
		operand := row.DestinationSHA
		if row.DestinationBinding == ReparentBindingParentComputed {
			operand = reparentDescendantOperand(row.DestinationParent)
		}
		if !reflect.DeepEqual(row.Argv, reparentRowArgv(operand, row.CutoffSHA)) {
			return fmt.Errorf("row %q carries non-canonical rebase argv", row.Name)
		}
		if len(row.MaterializedArgv) > 0 {
			if row.DestinationSHA == "" {
				return fmt.Errorf("row %q materialized argv lacks a destination", row.Name)
			}
			want, err := materializeReparentArgv(row.Argv, row.DestinationSHA)
			if err != nil || !reflect.DeepEqual(row.MaterializedArgv, want) {
				return fmt.Errorf("row %q carries non-canonical materialized argv", row.Name)
			}
		}
		if row.DestinationBinding == ReparentBindingParentComputed &&
			reparentRowStageRank(row.Stage) >= reparentRowStageRank(ReparentRowComputed) {
			parent := st.Row(row.DestinationParent)
			if parent == nil || parent.PlannedNewSHA == "" ||
				row.DestinationSHA != parent.PlannedNewSHA {
				return fmt.Errorf("row %q computed against a tip other than its recorded parent", row.Name)
			}
		}
	}
	for order := range st.Rows {
		if !seenOrders[order] {
			return fmt.Errorf("rows orders are not contiguous")
		}
	}
	target := st.Row(st.TargetName)
	if target == nil {
		return fmt.Errorf("target row is missing")
	}
	expectedRepoRoot := canonicalize(reparentRowRepoDir(st.WorkspaceRepoRoot, target.Repo))
	if canonicalize(st.RepoRoot) != expectedRepoRoot {
		return fmt.Errorf("repo_root does not match the captured target repository")
	}
	return nil
}

func validateReparentStateMetadata(st *ReparentState, stack Stack) error {
	target := st.Row(st.TargetName)
	if target == nil {
		return fmt.Errorf("target row is missing")
	}
	capturedTarget := GetBranch(stack, st.TargetName)
	if capturedTarget.Name == "" ||
		target.BaseBefore != capturedTarget.Base ||
		target.LastBaseSHABefore != capturedTarget.LastBaseSHA ||
		target.BaseAfter != st.NewParentStoredToken ||
		target.DestinationBinding != ReparentBindingPinned ||
		target.DestinationSHA != st.NewParentSHA ||
		target.LastBaseSHAAfter != st.NewParentSHA ||
		st.OldParentStoredToken != capturedTarget.Base {
		return fmt.Errorf("target metadata and destination identity do not agree")
	}
	if st.NewParentStoredToken == "" || st.NewParentSHA == "" {
		return fmt.Errorf("new parent identity is incomplete")
	}
	if err := validateReparentCapturedParent(
		"old", stack, st.OldParentKind, st.OldParentStoredToken,
		st.OldParentRef, st.OldParentSHA,
	); err != nil {
		return err
	}
	if err := validateReparentCapturedParent(
		"new", stack, st.NewParentKind, st.NewParentStoredToken,
		st.NewParentRef, st.NewParentSHA,
	); err != nil {
		return err
	}
	postImageKnown := st.StackAfterBase64 != ""
	for _, row := range st.Rows {
		entry := GetBranch(stack, row.Name)
		if row.BaseBefore != entry.Base || row.LastBaseSHABefore != entry.LastBaseSHA {
			return fmt.Errorf("row %q metadata preimage does not match stack_before_base64", row.Name)
		}
		if row.Role == ReparentRoleTarget {
			continue
		}
		if row.BaseAfter != entry.Base || row.DestinationBinding != ReparentBindingParentComputed ||
			row.DestinationParent != entry.Base {
			return fmt.Errorf("descendant row %q metadata or destination parent is inconsistent", row.Name)
		}
		parent := st.Row(row.DestinationParent)
		if parent == nil {
			return fmt.Errorf("descendant row %q has no recorded parent row", row.Name)
		}
		if postImageKnown {
			if parent.PlannedNewSHA == "" || row.LastBaseSHAAfter != parent.PlannedNewSHA {
				return fmt.Errorf("descendant row %q metadata postimage does not match its parent tip", row.Name)
			}
		} else if row.LastBaseSHAAfter != "" {
			return fmt.Errorf("descendant row %q has postimage metadata before the postimage exists", row.Name)
		}
	}
	return nil
}

func validateReparentCapturedParent(label string, stack Stack, kind, stored, ref, sha string) error {
	switch kind {
	case ReparentParentKindNone:
		if stored != "" || ref != "" || sha != "" {
			return fmt.Errorf("%s parent kind none carries an identity", label)
		}
	case ReparentParentKindStackEntry:
		parent := GetBranch(stack, stored)
		if parent.Name == "" || ref != "refs/heads/"+parent.GitBranch() || sha == "" {
			return fmt.Errorf("%s stack-entry parent does not match the captured stack", label)
		}
	case ReparentParentKindLiteralRef:
		if stored == "" || sha == "" {
			return fmt.Errorf("%s literal parent identity is incomplete", label)
		}
		if ref == "" {
			if reparentStateOID(stored, len(sha)) && stored != sha {
				return fmt.Errorf("%s raw-object parent does not match its object id", label)
			}
		} else if stored != ref {
			return fmt.Errorf("%s literal parent ref and stored token disagree", label)
		}
	default:
		return fmt.Errorf("%s parent kind is invalid", label)
	}
	return nil
}

func validateReparentStateHolders(st *ReparentState) error {
	approved := map[string]ReparentApprovedHolder{}
	for _, holder := range st.ApprovedHolders {
		path := canonicalize(holder.Path)
		if !reparentCanonicalAbsolutePath(holder.Path) || !reparentCanonicalAbsolutePath(holder.RepoCommonDir) ||
			!reparentStateOID(holder.HeadSHA, st.OIDWidth) || approved[path].Path != "" {
			return fmt.Errorf("approved_holders contains an invalid or duplicate holder")
		}
		if canonicalize(holder.RepoCommonDir) != canonicalize(st.RepoCommonDir) {
			return fmt.Errorf("approved holder belongs to a different repository")
		}
		if holder.GitBranch != "" && !reparentStateRef("refs/heads/"+holder.GitBranch) {
			return fmt.Errorf("approved holder has invalid branch")
		}
		switch holder.HolderKind {
		case "linked-worktree":
			if holder.Excluded || holder.GitBranch == "" {
				return fmt.Errorf("linked approved holder has invalid exclusion or branch")
			}
		case "primary-checkout":
			if holder.Excluded || holder.GitBranch == "" ||
				path != canonicalize(st.RepoRoot) {
				return fmt.Errorf("primary approved holder does not match repo_root")
			}
		case "computation-context":
			if st.WorkspaceMode != string(ModeCheckout) || !holder.Excluded ||
				path != canonicalize(st.RepoRoot) {
				return fmt.Errorf("approved computation context is invalid")
			}
		default:
			return fmt.Errorf("approved holder has invalid kind")
		}
		approved[path] = holder
	}
	progress := map[string]bool{}
	for _, holder := range st.DetachedHolders {
		path := canonicalize(holder.Path)
		if !reparentCanonicalAbsolutePath(holder.Path) || !reparentCanonicalAbsolutePath(holder.RepoCommonDir) ||
			!reparentStateOID(holder.PreimageSHA, st.OIDWidth) ||
			(holder.DetachTargetSHA != "" && !reparentStateOID(holder.DetachTargetSHA, st.OIDWidth)) ||
			progress[path] {
			return fmt.Errorf("detached_holders contains an invalid or duplicate holder")
		}
		if canonicalize(holder.RepoCommonDir) != canonicalize(st.RepoCommonDir) {
			return fmt.Errorf("detached holder belongs to a different repository")
		}
		progress[path] = true
		if holder.HolderKind == "computation-context" {
			if path != canonicalize(st.RepoRoot) && path != canonicalize(st.ScratchPath) {
				return fmt.Errorf("computation-context holder path is foreign")
			}
			if st.WorkspaceMode == string(ModeCheckout) {
				evidence, ok := approved[path]
				if !ok || !evidence.Excluded ||
					canonicalize(evidence.RepoCommonDir) != canonicalize(holder.RepoCommonDir) ||
					evidence.GitBranch != holder.GitBranch ||
					evidence.HeadSHA != holder.PreimageSHA ||
					evidence.HolderKind != holder.HolderKind {
					return fmt.Errorf("checkout computation progress does not match approved-holder evidence: progress=%+v approved=%+v", holder, evidence)
				}
			}
		} else {
			evidence, ok := approved[path]
			if !ok ||
				canonicalize(evidence.RepoCommonDir) != canonicalize(holder.RepoCommonDir) ||
				evidence.GitBranch != holder.GitBranch ||
				evidence.HeadSHA != holder.PreimageSHA ||
				evidence.HolderKind != holder.HolderKind {
				return fmt.Errorf("detachment progress does not exactly match approved-holder evidence")
			}
		}
		switch holder.DetachStatus {
		case ReparentHolderDetachIntent, ReparentHolderDetached,
			ReparentHolderRedetachIntent, ReparentHolderRedetached:
		default:
			return fmt.Errorf("detached holder has invalid status")
		}
		if err := validateReparentHolderSwitchIntent(st, holder); err != nil {
			return err
		}
	}
	if st.PreimageHolderExcluded != "" {
		if !reparentCanonicalAbsolutePath(st.PreimageHolderExcluded) ||
			canonicalize(st.PreimageHolderExcluded) != canonicalize(st.RepoRoot) {
			return fmt.Errorf("preimage_holder_excluded is not the checkout computation context")
		}
	}
	deferred := make(map[string]bool, len(st.HolderRestoreDeferred))
	for _, path := range st.HolderRestoreDeferred {
		holderPath := canonicalize(path)
		if !reparentCanonicalAbsolutePath(path) || deferred[holderPath] || !progress[holderPath] {
			return fmt.Errorf("holder_restore_deferred names an unrecorded path")
		}
		deferred[holderPath] = true
		for _, holder := range st.DetachedHolders {
			if canonicalize(holder.Path) == holderPath && holder.Restored {
				return fmt.Errorf("holder_restore_deferred names an already-restored holder")
			}
		}
	}
	for _, holder := range st.DetachedHolders {
		isDeferred := deferred[canonicalize(holder.Path)]
		if isDeferred != (holder.RestoreReason != "") {
			return fmt.Errorf("holder restore reason and deferred set disagree")
		}
	}
	return nil
}

func validateReparentHolderSwitchIntent(st *ReparentState, holder ReparentStateHolder) error {
	intent := holder.SwitchIntent
	if intent == nil {
		return nil
	}
	switch intent.Action {
	case ReparentHolderSwitchDetach, ReparentHolderSwitchCompute,
		ReparentHolderSwitchRestore, ReparentHolderSwitchRedetach:
	default:
		return fmt.Errorf("holder switch intent has invalid action")
	}
	if !reparentStateOID(intent.SourceSHA, st.OIDWidth) ||
		!reparentStateOID(intent.DestinationSHA, st.OIDWidth) {
		return fmt.Errorf("holder switch intent has invalid source or destination")
	}
	for _, branch := range []string{intent.SourceBranch, intent.DestinationBranch} {
		if branch != "" && !reparentStateRef("refs/heads/"+branch) {
			return fmt.Errorf("holder switch intent has invalid branch")
		}
	}
	if intent.Action == ReparentHolderSwitchRestore {
		if holder.Restored {
			return fmt.Errorf("restored holder still carries a switch intent")
		}
		if holder.HolderKind != "computation-context" &&
			intent.DestinationBranch != holder.GitBranch {
			return fmt.Errorf("holder restore intent targets the wrong branch")
		}
	} else {
		if intent.DestinationBranch != "" {
			return fmt.Errorf("holder detach intent must target a detached HEAD")
		}
		switch intent.Action {
		case ReparentHolderSwitchDetach:
			if holder.DetachStatus != ReparentHolderDetachIntent {
				return fmt.Errorf("holder detach intent disagrees with holder progress")
			}
		case ReparentHolderSwitchCompute:
			if holder.HolderKind != "computation-context" ||
				holder.DetachStatus != ReparentHolderDetached || holder.Restored {
				return fmt.Errorf("computation switch intent disagrees with holder progress")
			}
		case ReparentHolderSwitchRedetach:
			if holder.DetachStatus != ReparentHolderRedetachIntent || !holder.Restored {
				return fmt.Errorf("holder redetach intent disagrees with holder progress")
			}
		}
	}
	return nil
}

func validateReparentStatePostImage(st *ReparentState) error {
	if st.StackAfterBase64 == "" {
		if st.StackSHA256AfterExpected != "" || st.StackSHA256After != "" {
			return fmt.Errorf("post-image hashes exist without post-image bytes")
		}
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(st.StackAfterBase64)
	if err != nil {
		return fmt.Errorf("stack_after_base64 does not decode")
	}
	hash := hex.EncodeToString(reparentStateSHA256(data))
	if hash != st.StackSHA256AfterExpected ||
		(st.StackSHA256After != "" && st.StackSHA256After != hash) {
		return fmt.Errorf("post-image bytes and hashes do not agree")
	}
	beforeData, err := base64.StdEncoding.DecodeString(st.StackBeforeBase64)
	if err != nil {
		return fmt.Errorf("stack_before_base64 does not decode")
	}
	var before, after Stack
	if err := yaml.Unmarshal(beforeData, &before); err != nil {
		return fmt.Errorf("stack_before_base64 is not valid stack.yaml")
	}
	if err := yaml.Unmarshal(data, &after); err != nil {
		return fmt.Errorf("stack_after_base64 is not valid stack.yaml")
	}
	if len(before.Branches) != len(after.Branches) {
		return fmt.Errorf("post-image changes stack entry count")
	}
	for i := range before.Branches {
		pre, post := before.Branches[i], after.Branches[i]
		if pre.Name != post.Name || pre.GitBranch() != post.GitBranch() ||
			pre.Archived != post.Archived || pre.Repo != post.Repo {
			return fmt.Errorf("post-image changes immutable metadata for row %d", i)
		}
		row := st.Row(pre.Name)
		if row == nil {
			if !reflect.DeepEqual(pre, post) {
				return fmt.Errorf("post-image changes entry %q outside the closure", pre.Name)
			}
			continue
		}
		if post.Base != row.BaseAfter || post.LastBaseSHA != row.LastBaseSHAAfter {
			return fmt.Errorf("post-image does not match recorded metadata for %q", row.Name)
		}
	}
	return nil
}

func validateReparentStateTransactions(st *ReparentState) error {
	rows := make(map[string]ReparentStateRow, len(st.Rows))
	for _, row := range st.Rows {
		rows["refs/heads/"+row.GitBranch] = row
	}
	seen := map[string]bool{}
	for _, row := range st.CASRows {
		stateRow, ok := rows[row.Ref]
		if !ok || seen[row.Ref] || row.OldValue != stateRow.PreimageSHA ||
			row.NewValue != stateRow.PlannedNewSHA || !row.Applied ||
			row.Noop != stateRow.Noop {
			return fmt.Errorf("cas_rows contains a foreign or inconsistent ref")
		}
		seen[row.Ref] = true
	}
	if st.CASTransactionSucceeded {
		if len(st.CASRows) != len(st.Rows) {
			return fmt.Errorf("successful CAS does not cover every row")
		}
		for _, row := range st.Rows {
			if row.Stage != ReparentRowCommitted {
				return fmt.Errorf("successful CAS has an uncommitted row")
			}
		}
	} else if len(st.CASRows) != 0 {
		return fmt.Errorf("cas_rows exist without a successful CAS")
	}
	seen = map[string]bool{}
	for _, row := range st.AbortRows {
		stateRow, ok := rows[row.Ref]
		class := ReparentRefClass(row.Classification)
		if !ok || seen[row.Ref] ||
			(class != ReparentRefNoop && class != ReparentRefPreimage && class != ReparentRefPlannedTip) ||
			row.Restored != (class == ReparentRefPlannedTip) ||
			(class == ReparentRefNoop && !stateRow.Noop) ||
			(class != ReparentRefNoop && stateRow.Noop) {
			return fmt.Errorf("abort_rows contains a foreign or duplicate ref")
		}
		seen[row.Ref] = true
	}
	if len(st.AbortRows) > 0 &&
		st.Stage != ReparentStageAborting && st.Stage != ReparentStageAborted {
		return fmt.Errorf("abort_rows exist outside abort recovery")
	}
	effectiveStage := reparentStateEffectiveStage(st)
	effectiveRank := reparentStageRank(effectiveStage)
	if effectiveRank < 0 || reparentStageIsTerminalish(effectiveStage) {
		return fmt.Errorf("resume_stage is not a forward stage")
	}
	if effectiveRank >= reparentStageRank(ReparentStagePreflight) && !st.CompatArtifactsWritten {
		return fmt.Errorf("forward stage %s requires the compatibility envelope", effectiveStage)
	}
	if st.CASTransactionSucceeded &&
		effectiveRank < reparentStageRank(ReparentStageCommittingRefs) {
		return fmt.Errorf("successful CAS exists before the committing-refs stage")
	}
	if st.StackSHA256After != "" &&
		effectiveRank < reparentStageRank(ReparentStageWritingMetadata) {
		return fmt.Errorf("durable metadata result exists before writing-metadata")
	}
	if st.CommitPointReached {
		if !st.CASTransactionSucceeded || st.StackSHA256AfterExpected == "" ||
			st.StackSHA256After != st.StackSHA256AfterExpected {
			return fmt.Errorf("commit_point_reached lacks durable CAS and metadata prerequisites")
		}
		if effectiveRank < reparentStageRank(ReparentStageWritingMetadata) {
			return fmt.Errorf("commit_point_reached precedes writing-metadata")
		}
	}
	for _, row := range st.Rows {
		switch {
		case effectiveRank < reparentStageRank(ReparentStageComputing):
			if row.Stage != ReparentRowPending {
				return fmt.Errorf("row %q progressed before computing", row.Name)
			}
		case effectiveStage == ReparentStageComputing || effectiveStage == ReparentStageConflictPaused:
			if row.Stage == ReparentRowCommitted {
				return fmt.Errorf("row %q committed while computation is active", row.Name)
			}
		case effectiveRank >= reparentStageRank(ReparentStageBuildingPostImage) &&
			effectiveRank < reparentStageRank(ReparentStageCommittingRefs):
			if row.Stage != ReparentRowValidated {
				return fmt.Errorf("row %q is not validated at stage %s", row.Name, effectiveStage)
			}
		case effectiveStage == ReparentStageCommittingRefs:
			if st.CASTransactionSucceeded && row.Stage != ReparentRowCommitted {
				return fmt.Errorf("row %q is not committed after successful CAS", row.Name)
			}
			if !st.CASTransactionSucceeded && row.Stage != ReparentRowValidated {
				return fmt.Errorf("row %q progressed without a successful CAS", row.Name)
			}
		case effectiveRank >= reparentStageRank(ReparentStageRefsCommitted):
			if row.Stage != ReparentRowCommitted {
				return fmt.Errorf("row %q is not committed at stage %s", row.Name, effectiveStage)
			}
		}
	}
	switch effectiveStage {
	case ReparentStagePinningComputed, ReparentStageWritingRemoteRecord,
		ReparentStageDetachingHolders, ReparentStageCommittingRefs,
		ReparentStageRefsCommitted, ReparentStageWritingMetadata,
		ReparentStageMetadataWritten, ReparentStageRestoringHolders,
		ReparentStageCleanup, ReparentStageCompleted:
		if st.StackAfterBase64 == "" || st.StackSHA256AfterExpected == "" {
			return fmt.Errorf("stage %s requires the persisted postimage", st.Stage)
		}
	}
	switch effectiveStage {
	case ReparentStageDetachingHolders, ReparentStageCommittingRefs,
		ReparentStageRefsCommitted, ReparentStageWritingMetadata,
		ReparentStageMetadataWritten, ReparentStageRestoringHolders,
		ReparentStageCleanup, ReparentStageCompleted:
		if !st.RemoteRecordWritten && !st.RemoteRecordEmpty {
			return fmt.Errorf("stage %s lacks remote-record proof", st.Stage)
		}
	}
	switch effectiveStage {
	case ReparentStageRefsCommitted, ReparentStageWritingMetadata,
		ReparentStageMetadataWritten, ReparentStageRestoringHolders,
		ReparentStageCleanup, ReparentStageCompleted:
		if !st.CASTransactionSucceeded {
			return fmt.Errorf("stage %s requires a successful CAS", st.Stage)
		}
	}
	if effectiveRank >= reparentStageRank(ReparentStageRestoringHolders) &&
		!st.CommitPointReached {
		return fmt.Errorf("post-commit holder restoration or cleanup requires the commit point")
	}
	return nil
}

func validateReparentStateRemote(loc ReparentLocation, st *ReparentState) error {
	if st.RemoteRecordWritten && st.RemoteRecordEmpty {
		return fmt.Errorf("remote record cannot be both written and empty")
	}
	if !st.RemoteRecordBeforeCaptured {
		return fmt.Errorf("remote record preimage was not captured before authoritative state")
	}
	seenFollowup := map[string]bool{}
	for _, name := range st.RemoteFollowupEntries {
		if st.Row(name) == nil || seenFollowup[name] {
			return fmt.Errorf("remote_followup_entries names an invalid or duplicate row")
		}
		seenFollowup[name] = true
	}
	if st.RemoteRecordWritten && len(st.RemoteFollowupEntries) == 0 {
		return fmt.Errorf("written remote record has no follow-up entries")
	}
	if st.RemoteRecordEmpty && len(st.RemoteFollowupEntries) != 0 {
		return fmt.Errorf("empty remote proof cannot name follow-up entries")
	}
	effectiveStage := reparentStateEffectiveStage(st)
	if reparentStageRank(effectiveStage) < reparentStageRank(ReparentStageWritingRemoteRecord) &&
		(st.RemoteRecordWritten || st.RemoteRecordEmpty || len(st.RemoteFollowupEntries) > 0) {
		return fmt.Errorf("remote-record proof exists before the writing-remote-record stage")
	}
	if !st.RemoteRecordBeforeCaptured {
		if st.RemoteRecordBeforePresent || st.RemoteRecordBeforeBase64 != "" ||
			st.RemoteRecordRestorePending || st.RemoteRecordRestoreSourceCaptured {
			return fmt.Errorf("remote restore fields exist without a captured preimage")
		}
	} else if st.RemoteRecordBeforePresent {
		data, err := base64.StdEncoding.DecodeString(st.RemoteRecordBeforeBase64)
		if err != nil {
			return fmt.Errorf("remote_record_before_base64 does not decode")
		}
		if _, err := decodeReparentRemoteRecordBytes(loc, data); err != nil {
			return fmt.Errorf("remote record preimage is invalid: %w", err)
		}
	} else if st.RemoteRecordBeforeBase64 != "" {
		return fmt.Errorf("absent remote preimage carries bytes")
	}
	if st.RemoteRecordRestorePending {
		if !st.RemoteRecordRestoreSourceCaptured {
			return fmt.Errorf("remote restore pending without source")
		}
		if st.RemoteRecordRestoreSourcePresent {
			data, err := base64.StdEncoding.DecodeString(st.RemoteRecordRestoreSourceBase64)
			if err != nil {
				return fmt.Errorf("remote restore source does not decode")
			}
			source, err := decodeReparentRemoteRecordBytes(loc, data)
			if err != nil {
				return fmt.Errorf("remote restore source is invalid: %w", err)
			}
			before, beforePresent, err := reparentStateRemoteBefore(loc, st)
			if err != nil {
				return err
			}
			if beforePresent {
				if !bytes.Equal(before, data) &&
					!reparentRemoteRecordIsClearProgress(loc, before, data) &&
					source.RunID != st.RunID {
					return fmt.Errorf("remote restore source is not prior bytes, clear progress, or this run")
				}
			} else if source.RunID != st.RunID {
				return fmt.Errorf("remote restore source is foreign to this run")
			}
		} else if st.RemoteRecordRestoreSourceBase64 != "" {
			return fmt.Errorf("absent remote restore source carries bytes")
		}
	} else if st.RemoteRecordRestoreSourceCaptured || st.RemoteRecordRestoreSourcePresent ||
		st.RemoteRecordRestoreSourceBase64 != "" {
		return fmt.Errorf("remote restore source exists without pending intent")
	}
	if st.RemoteClearPending {
		if reparentStageRank(effectiveStage) < reparentStageRank(ReparentStagePreflight) ||
			!st.CompatArtifactsWritten {
			return fmt.Errorf("remote clear intent exists before durable preflight")
		}
		if !st.RemoteClearSourcePresent || st.RemoteClearSourceBase64 == "" {
			return fmt.Errorf("remote clear pending without an exact source record")
		}
		source, err := base64.StdEncoding.DecodeString(st.RemoteClearSourceBase64)
		if err != nil {
			return fmt.Errorf("remote clear source does not decode")
		}
		if _, err := decodeReparentRemoteRecordBytes(loc, source); err != nil {
			return fmt.Errorf("remote clear source is invalid: %w", err)
		}
		if st.RemoteClearTargetPresent {
			target, err := base64.StdEncoding.DecodeString(st.RemoteClearTargetBase64)
			if err != nil {
				return fmt.Errorf("remote clear target does not decode")
			}
			if _, err := decodeReparentRemoteRecordBytes(loc, target); err != nil {
				return fmt.Errorf("remote clear target is invalid: %w", err)
			}
			if bytes.Equal(source, target) ||
				!reparentRemoteRecordIsClearProgress(loc, source, target) {
				return fmt.Errorf("remote clear target is not monotonic clear progress")
			}
		} else if st.RemoteClearTargetBase64 != "" {
			return fmt.Errorf("absent remote clear target carries bytes")
		} else {
			sourceRecord, err := decodeReparentRemoteRecordBytes(loc, source)
			if err != nil {
				return fmt.Errorf("remote clear source is invalid: %w", err)
			}
			hasPending := false
			for _, entry := range sourceRecord.Entries {
				if entry.Pending() {
					hasPending = true
					break
				}
			}
			if !hasPending {
				return fmt.Errorf("remote clear delete has no pending source entry to clear")
			}
		}
	} else if st.RemoteClearSourcePresent || st.RemoteClearSourceBase64 != "" ||
		st.RemoteClearTargetPresent || st.RemoteClearTargetBase64 != "" {
		return fmt.Errorf("remote clear images exist without pending intent")
	}
	return nil
}

func reparentStateRemoteBefore(loc ReparentLocation, st *ReparentState) ([]byte, bool, error) {
	if !st.RemoteRecordBeforeCaptured || !st.RemoteRecordBeforePresent {
		return nil, false, nil
	}
	data, err := base64.StdEncoding.DecodeString(st.RemoteRecordBeforeBase64)
	if err != nil {
		return nil, false, fmt.Errorf("remote record preimage does not decode")
	}
	if _, err := decodeReparentRemoteRecordBytes(loc, data); err != nil {
		return nil, false, fmt.Errorf("remote record preimage is invalid: %w", err)
	}
	return data, true, nil
}

func reparentCanonicalAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		canonicalize(path) == path
}

func reparentStateOID(value string, width int) bool {
	return len(value) == width && reparentStateHex(value, width)
}

func reparentStateHex(value string, width int) bool {
	if len(value) != width {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func reparentStateRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || strings.HasSuffix(ref, "/") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "//") ||
		strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range ref {
		if c <= ' ' || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func validReparentStateName(name string) bool {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if c == 0 || c == '\n' || c == '\r' {
			return false
		}
	}
	return true
}

func reparentStateSHA256(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// SaveReparentState writes the artifact durably at 0600, stamping updated_at
// in the same write. Every stage transition goes through here, so a stage and
// its resume_stage can never be persisted separately.
func SaveReparentState(loc ReparentLocation, st *ReparentState) error {
	if st == nil {
		return fmt.Errorf("refusing to write a nil reparent state")
	}
	st.StateVersion = ReparentStateVersion
	st.UpdatedAt = reparentNow()
	if st.ResumeStage == "" && !reparentStageIsTerminalish(st.Stage) {
		st.ResumeStage = st.Stage
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal reparent state: %w", err)
	}
	return durableWriteFileFault(SyncIOWriteReparentState, ReparentStatePath(loc), data, 0600)
}

// RemoveReparentState deletes the artifact. It is the LAST step of §11.6a,
// because it is the only record of the five that precede it. An absent file
// is success: cleanup must be repeatable.
func RemoveReparentState(loc ReparentLocation) error {
	path := ReparentStatePath(loc)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return reparentRecoverySyncDir(filepath.Dir(path))
}

// HasReparentState reports whether an artifact file exists at all, without
// decoding it. The mutual-exclusion pre-checks need presence, not contents.
func HasReparentState(loc ReparentLocation) bool {
	_, err := os.Stat(ReparentStatePath(loc))
	return err == nil
}

// reparentNow is the one timestamp format the artifacts use: RFC3339 in UTC.
func reparentNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ============================================================================
// Classification and the §11.10 projection
// ============================================================================

// The six artifact statuses §11.10 item 4 requires every observability
// surface to distinguish.
const (
	ReparentArtifactActive      = "active"
	ReparentArtifactComplete    = "complete"
	ReparentArtifactUnsupported = "unsupported"
	ReparentArtifactCorrupt     = "corrupt"
	ReparentArtifactForeign     = "foreign"
	ReparentArtifactStale       = "stale"
)

// ReparentProjection is the read-only picture of a reparent artifact that
// `tws status`, `tws stack status --json`, `tws doctor`, `tws list` and the
// two sync reports publish under their own `reparent` key. It is declared
// here, beside the artifact it projects, so no surface re-derives these facts
// from raw YAML.
//
// Computing it is strictly read-only: no fetch, no ref write, no state
// repair, and in particular no marking or deletion of the remote follow-up
// record.
type ReparentProjection struct {
	Status      string `json:"status"`
	Feature     string `json:"feature"`
	RunID       string `json:"run_id,omitempty"`
	Target      string `json:"target,omitempty"`
	Stage       string `json:"stage,omitempty"`
	ResumeStage string `json:"resume_stage,omitempty"`
	Mode        string `json:"mode,omitempty"`
	OwnerPID    int    `json:"owner_pid,omitempty"`
	OwnerLive   *bool  `json:"owner_live,omitempty"`
	CommitPoint bool   `json:"commit_point_reached"`
	Detail      string `json:"detail,omitempty"`
}

// ReparentObservabilityLine is the single anchored line §11.10 item 1
// requires on stderr, before any ancestry output is written on stdout.
func (p ReparentProjection) ObservabilityLine() string {
	feature := sanitizeReparentLine(p.Feature)
	target := sanitizeReparentLine(p.Target)
	runID := sanitizeReparentLine(p.RunID)
	stage := sanitizeReparentLine(p.Stage)
	detail := sanitizeReparentLine(p.Detail)
	base := fmt.Sprintf("reparent %s: %s", sanitizeReparentLine(p.Status), feature)
	if target != "" {
		base += " target " + target
	}
	if runID != "" {
		base += " run " + runID
	}
	if stage != "" {
		base += " stage " + stage
	}
	if detail != "" {
		base += "; " + detail
	}
	actionable := p.Status == ReparentArtifactStale ||
		(p.Status == ReparentArtifactComplete && (p.OwnerLive == nil || !*p.OwnerLive))
	if actionable {
		if p.Stage == string(ReparentStageAborting) || p.Stage == string(ReparentStageAborted) {
			base += fmt.Sprintf("; resume cleanup with: tws stack reparent %s --abort", feature)
		} else {
			base += fmt.Sprintf("; continue with: tws stack reparent %s --continue (or --abort)", feature)
		}
	} else if p.Status == ReparentArtifactActive && p.OwnerLive != nil && *p.OwnerLive && p.OwnerPID > 0 {
		base += fmt.Sprintf("; owner pid %d is live", p.OwnerPID)
	}
	return base
}

// BuildReparentProjection classifies whatever is on disk for this feature.
// It returns nil when no artifact exists at all, which is what keeps a
// no-reparent status document byte-identical to today: the key is ABSENT,
// never null.
func BuildReparentProjection(loc ReparentLocation) *ReparentProjection {
	load := LoadReparentState(loc)
	switch load.Kind {
	case ReparentStateAbsent:
		return nil
	case ReparentStateCorrupt:
		return &ReparentProjection{Status: ReparentArtifactCorrupt, Feature: loc.Feature, Detail: sanitizeReparentLine(load.Detail)}
	case ReparentStateUnsupported:
		p := &ReparentProjection{Status: ReparentArtifactUnsupported, Feature: loc.Feature, Detail: sanitizeReparentLine(load.Detail)}
		if load.State != nil {
			p.RunID = load.State.RunID
			p.Stage = string(load.State.Stage)
		}
		return p
	case ReparentStateForeign:
		p := &ReparentProjection{Status: ReparentArtifactForeign, Feature: loc.Feature, Detail: sanitizeReparentLine(load.Detail)}
		if load.State != nil {
			p.RunID = load.State.RunID
			p.Stage = string(load.State.Stage)
		}
		return p
	}

	st := load.State
	live := isProcessAlive(st.OwnerPID)
	p := &ReparentProjection{
		Status:      ReparentArtifactActive,
		Feature:     st.Feature,
		RunID:       st.RunID,
		Target:      st.TargetName,
		Stage:       string(st.Stage),
		ResumeStage: string(st.ResumeStage),
		Mode:        st.WorkspaceMode,
		OwnerPID:    st.OwnerPID,
		OwnerLive:   &live,
		CommitPoint: st.CommitPointReached,
	}
	switch {
	case st.Stage == ReparentStageCompleted || st.Stage == ReparentStageAborted:
		p.Status = ReparentArtifactComplete
		p.Detail = "terminal state is durable; artifact cleanup remains"
	case !live:
		p.Status = ReparentArtifactStale
		p.Detail = fmt.Sprintf("owner pid %d is not live; recovery may reclaim the run", st.OwnerPID)
	}
	return p
}

// ============================================================================
// Mutual exclusion (§11.2)
// ============================================================================

// ReparentRoute is which of the three verbs is asking. It decides whether an
// existing artifact is this invocation's own run or somebody else's.
type ReparentRoute string

const (
	ReparentRouteVerbFresh    ReparentRoute = "fresh"
	ReparentRouteVerbContinue ReparentRoute = "continue"
	ReparentRouteVerbAbort    ReparentRoute = "abort"
)

// RefuseIfReparentActive is the mutual-exclusion gate of §11.2, evaluated
// BEFORE the generic sync-state refusal: the authoritative artifact is
// classified first, so an operator sees "a reparent is in progress" rather
// than a version or decode error about this feature's own compatibility
// files.
//
// On the fresh route any artifact refuses. On --continue and --abort an
// artifact that decodes, matches this feature and workspace, and is not
// already finished is the run being resumed and is NOT a refusal; an
// aborting/aborted artifact refuses reparent-state-present with the §13.1
// abort detail, because a rollback that has begun is never resumed forward.
func RefuseIfReparentActive(loc ReparentLocation, route ReparentRoute) (*ReparentState, error) {
	load := LoadReparentState(loc)
	if refusal := load.Refusal(); refusal != nil {
		return nil, refusal
	}
	if load.Kind == ReparentStateAbsent {
		if route == ReparentRouteVerbFresh {
			return nil, nil
		}
		return nil, reparentRefusal(ReparentRefusalStatePresent, fmt.Sprintf(
			"no reparent run is recorded for %q; there is nothing to %s", loc.Feature, route))
	}

	st := load.State
	if route == ReparentRouteVerbFresh {
		return nil, reparentRefusal(ReparentRefusalStatePresent, fmt.Sprintf(
			"a reparent run (%s, stage %s) is already recorded for %q; finish it with: tws stack reparent %s --continue (or --abort)",
			st.RunID, st.Stage, loc.Feature, loc.Feature))
	}
	if route == ReparentRouteVerbContinue && (st.Stage == ReparentStageAborting || st.Stage == ReparentStageAborted) {
		return nil, reparentRefusal(ReparentRefusalStatePresent, fmt.Sprintf(
			"reparent run %s for %q is already aborting; re-run: tws stack reparent %s --abort",
			st.RunID, loc.Feature, loc.Feature))
	}
	return st, nil
}

// RefuseSyncIfReparentActive is `tws sync`'s ONE new pre-check (§11.2). It
// fires only while a reparent state artifact exists for this feature in this
// mode, and it is deliberately NOT RefuseIfReparentActive: sync is not a
// reparent verb, so it never adopts a run and it never distinguishes the
// three reparent routes — any artifact at all, including an undecodable or
// foreign one, means "do not let a shipped sync verb touch this feature's
// compatibility files".
//
// It is os.Stat plus at most one os.ReadFile and spawns ZERO Git children,
// which is what keeps a no-flag `tws sync` on a feature with no reparent
// artifact byte- and process-identical to today.
func RefuseSyncIfReparentActive(loc ReparentLocation) error {
	if !HasReparentState(loc) {
		return nil
	}
	runID := "unknown"
	stage := "unknown"
	if load := LoadReparentState(loc); load.State != nil {
		if load.State.RunID != "" {
			runID = load.State.RunID
		}
		if load.State.Stage != "" {
			stage = string(load.State.Stage)
		}
	}
	return fmt.Errorf(
		"a stack reparent is in progress for %q (run %s, stage %s); finish it with: tws stack reparent %s --continue",
		loc.Feature, runID, stage, loc.Feature)
}

// ReparentPushMutationRefusal blocks publication while a same-feature
// reparent's commit point is still unproven. Push must not clear or supersede
// the pending remote protection of a run that may still abort.
func ReparentPushMutationRefusal(loc ReparentLocation) *ReparentRefusalError {
	if loc.Feature == "" {
		return nil
	}
	load := LoadReparentState(loc)
	if load.Kind == ReparentStateAbsent {
		return nil
	}
	if refusal := load.Refusal(); refusal != nil {
		return refusal
	}
	if load.State == nil {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"the reparent state exists but cannot be classified for push safety")
	}
	if load.State.CommitPointReached {
		return nil
	}
	return reparentRefusal(ReparentRefusalStatePresent, fmt.Sprintf(
		"reparent run %s for %q has not proven its commit point (stage %s); finish it with --continue or --abort before pushing",
		load.State.RunID, loc.Feature, load.State.Stage))
}

// ============================================================================
// Compatibility artifacts (§11.2) — written AFTER the authoritative artifact,
// in this exact order, and never read back as a shipped type
// ============================================================================

// ReparentCompatSyncStateVersion is the state_version the external
// compatibility payload declares. Every shipped LoadSyncRunState accepts only
// 2 and 3, so 4 makes an old binary fail closed with its own sentence
// (`unsupported scoped sync state version 4`) instead of resuming a run it
// does not understand.
const ReparentCompatSyncStateVersion = 4

// ReparentCompatRoute is the route token both compatibility artifacts carry.
const ReparentCompatRoute = "reparent"

// ReparentCompatMarker is the marker a reparent run detects its OWN
// compatibility artifacts by. New reparent code never parses the checkout
// artifact as a CheckoutTransaction; it reads raw YAML and matches this.
const ReparentCompatMarker = "tws-reparent-compat-v1"

// ReparentCheckoutCompatStateVersion is deliberately a STRING where every
// shipped binary declares an int. That single type mismatch is what makes
// LoadCheckoutTransaction fail, which makes both ContinueCheckoutSync and
// AbortCheckoutSync return "no transaction to continue/abort" BEFORE taking
// the lock, before `git rebase --abort`, before restoreOriginal and before
// deleting anything. An integer 4 would let an older binary "roll back" a
// reparent run it cannot read.
const ReparentCheckoutCompatStateVersion = "4-reparent"

// ReparentCompatSyncPayload is the external v4 payload, written as a raw YAML
// document by this file. SaveSyncRunState is deliberately NOT used and
// deliberately NOT modified: it rejects any state_version other than 2 or 3
// by design, and relaxing that would weaken sync's own birth decision.
type ReparentCompatSyncPayload struct {
	StateVersion      int      `yaml:"state_version"`
	Route             string   `yaml:"route"`
	Feature           string   `yaml:"feature"`
	Stage             string   `yaml:"stage"`
	Marker            string   `yaml:"marker"`
	ReparentRunID     string   `yaml:"reparent_run_id"`
	ReparentMarker    string   `yaml:"reparent_marker"`
	OwnerToken        string   `yaml:"owner_token"`
	CreatedAt         string   `yaml:"created_at"`
	MaxReplayPerEntry *int     `yaml:"max_replay_per_entry,omitempty"`
	MaxReplayTotal    *int     `yaml:"max_replay_total,omitempty"`
	Selected          []string `yaml:"selected"`
}

// ReparentCompatSentinel is the legacy `.sync-state.yaml` sentinel. It
// reproduces the shipped guarded sentinel's marker shape exactly
// (guarded_state_version 3, failed_branch == marker) and adds the reparent
// identity this boundary needs, which is the first of the two reasons
// SaveGuardedLegacySentinel is not used; the second is that it writes through
// atomicWriteFile, which does not fsync the parent directory, while §10.3a
// requires durableWriteFile at the shipped 0644 mode.
type ReparentCompatSentinel struct {
	StartedAt    string   `yaml:"started_at"`
	FailedBranch string   `yaml:"failed_branch"`
	Pending      []string `yaml:"pending"`
	Completed    []string `yaml:"completed"`
	Skipped      []string `yaml:"skipped"`

	GuardedStateVersion int    `yaml:"guarded_state_version"`
	Route               string `yaml:"route"`
	Feature             string `yaml:"feature"`
	Marker              string `yaml:"marker"`
	OwnerToken          string `yaml:"owner_token"`
	CreatedAt           string `yaml:"created_at"`
	WriterPID           int    `yaml:"writer_pid"`

	ReparentRunID  string `yaml:"reparent_run_id"`
	ReparentMarker string `yaml:"reparent_marker"`
}

// ReparentCheckoutCompatTransaction is the deliberately undecodable checkout
// compatibility transaction. Its state_version is a string; nothing else
// about it is load-bearing, and the real original_branch/original_head live
// ONLY in the authoritative artifact so no other binary can act on them.
type ReparentCheckoutCompatTransaction struct {
	StateVersion      string `yaml:"state_version"`
	Route             string `yaml:"route"`
	Feature           string `yaml:"feature"`
	ReparentRunID     string `yaml:"reparent_run_id"`
	ReparentMarker    string `yaml:"reparent_marker"`
	MaxReplayPerEntry *int   `yaml:"max_replay_per_entry,omitempty"`
	MaxReplayTotal    *int   `yaml:"max_replay_total,omitempty"`
}

// MarshalYAML emits state_version as an explicitly QUOTED scalar. yaml.v3
// would otherwise render `4-reparent` bare: still a string, still undecodable
// into the shipped int field, but a reader inspecting the file by eye could
// mistake it for a typo'd number. The quotes make the deliberate type
// mismatch self-evident on disk, which is the whole point of the artifact.
func (t ReparentCheckoutCompatTransaction) MarshalYAML() (any, error) {
	doc := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, value *yaml.Node) {
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
	}
	add("state_version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: t.StateVersion})
	add("route", &yaml.Node{Kind: yaml.ScalarNode, Value: t.Route})
	add("feature", &yaml.Node{Kind: yaml.ScalarNode, Value: t.Feature})
	add("reparent_run_id", &yaml.Node{Kind: yaml.ScalarNode, Value: t.ReparentRunID})
	add("reparent_marker", &yaml.Node{Kind: yaml.ScalarNode, Value: t.ReparentMarker})
	if t.MaxReplayPerEntry != nil {
		add("max_replay_per_entry", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", *t.MaxReplayPerEntry)})
	}
	if t.MaxReplayTotal != nil {
		add("max_replay_total", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", *t.MaxReplayTotal)})
	}
	return doc, nil
}

// reparentCompatSentinelMarker is the sentinel's failed_branch/marker value.
//
// It MUST satisfy the shipped classifier's own marker pattern —
// `^tws-scoped-sync-[0-9a-f]{32}\.lock$` (syncMarkerRe,
// internal/sync_run_state.go) — because ClassifyExternalSyncState decides the
// legacy axis by testing exactly that regex against failed_branch. A marker
// outside it classifies as legacyReal, which is a RESUMABLE legacy run for
// every shipped binary; inside it, the same file is a per-run SENTINEL, and
// with the undecodable v4 payload beside it every shipped classifier lands in
// a cell whose only outcome is a refusal. The run id is already 32 lowercase
// hex, so it is the marker's body; the reparent identity travels in the
// sentinel's own reparent_run_id / reparent_marker fields, which no shipped
// decoder reads and no shipped classifier consults.
func reparentCompatSentinelMarker(runID string) string {
	return "tws-scoped-sync-" + runID + ".lock"
}

// writeReparentCompatSyncPayload writes <featurePath>/.sync-state.v2.yaml as
// the raw v4 document. It is the FIRST external compatibility write: with the
// payload already present, no instant exists at which a concurrent old binary
// observes a lone legacy sentinel and routes itself into a legacy-resume cell.
func writeReparentCompatSyncPayload(featurePath string, p ReparentCompatSyncPayload) error {
	data, err := yaml.Marshal(&p)
	if err != nil {
		return fmt.Errorf("marshal reparent compatibility payload: %w", err)
	}
	return durableWriteFileFault(SyncIOWriteReparentCompat, SyncRunStatePath(featurePath), data, 0600)
}

// writeReparentCompatSentinel writes <featurePath>/.sync-state.yaml SECOND,
// at the shipped 0644 mode.
func writeReparentCompatSentinel(featurePath string, s ReparentCompatSentinel) error {
	data, err := yaml.Marshal(&s)
	if err != nil {
		return fmt.Errorf("marshal reparent compatibility sentinel: %w", err)
	}
	return durableWriteFileFault(SyncIOWriteReparentCompat, SyncStatePath(featurePath), data, 0644)
}

// writeReparentCheckoutCompatTransaction writes the checkout compatibility
// transaction at CheckoutTransactionPath — the shipped, feature-path-derived
// location, so HasCheckoutTransaction still sees it and plain `tws sync`
// still refuses exactly as today.
func writeReparentCheckoutCompatTransaction(featurePath string, tx ReparentCheckoutCompatTransaction) error {
	data, err := yaml.Marshal(&tx)
	if err != nil {
		return fmt.Errorf("marshal reparent compatibility transaction: %w", err)
	}
	return durableWriteFileFault(SyncIOWriteReparentCompat, CheckoutTransactionPath(featurePath), data, 0600)
}

// WriteReparentCompatArtifacts writes this run's whole compatibility envelope
// in the exact §11.2 order for the run's mode. It is called only AFTER the
// authoritative artifact exists (§11.4 steps 6 then 7), never before and
// never the reverse.
func WriteReparentCompatArtifacts(loc ReparentLocation, st *ReparentState, selected []string) error {
	if status := ClassifyReparentCompatArtifacts(loc, st.RunID); status.Foreign {
		return reparentRefusal(ReparentRefusalSyncStatePresent,
			"foreign sync state exists at a compatibility-artifact path; tws will not overwrite it")
	}
	if loc.Mode == ModeCheckout {
		return writeReparentCheckoutCompatTransaction(loc.FeaturePath, ReparentCheckoutCompatTransaction{
			StateVersion:      ReparentCheckoutCompatStateVersion,
			Route:             ReparentCompatRoute,
			Feature:           st.Feature,
			ReparentRunID:     st.RunID,
			ReparentMarker:    ReparentCompatMarker,
			MaxReplayPerEntry: st.MaxReplayPerEntry,
			MaxReplayTotal:    st.MaxReplayTotal,
		})
	}

	marker := reparentCompatSentinelMarker(st.RunID)
	if err := writeReparentCompatSyncPayload(loc.FeaturePath, ReparentCompatSyncPayload{
		StateVersion:      ReparentCompatSyncStateVersion,
		Route:             ReparentCompatRoute,
		Feature:           st.Feature,
		Stage:             string(SyncStageRebasing),
		Marker:            marker,
		ReparentRunID:     st.RunID,
		ReparentMarker:    ReparentCompatMarker,
		OwnerToken:        st.OwnerToken,
		CreatedAt:         st.CreatedAt,
		MaxReplayPerEntry: st.MaxReplayPerEntry,
		MaxReplayTotal:    st.MaxReplayTotal,
		Selected:          append([]string{}, selected...),
	}); err != nil {
		return err
	}
	return writeReparentCompatSentinel(loc.FeaturePath, ReparentCompatSentinel{
		StartedAt:           st.CreatedAt,
		FailedBranch:        marker,
		Pending:             []string{},
		Completed:           []string{},
		Skipped:             []string{},
		GuardedStateVersion: GuardedLegacySentinelVersion,
		Route:               ReparentCompatRoute,
		Feature:             st.Feature,
		Marker:              marker,
		OwnerToken:          st.OwnerToken,
		CreatedAt:           st.CreatedAt,
		WriterPID:           st.OwnerPID,
		ReparentRunID:       st.RunID,
		ReparentMarker:      ReparentCompatMarker,
	})
}

// reparentCompatOwnership is what a raw compatibility file says about which
// run owns it. Present is false when the file is absent; Ours is true only
// when the file carries this feature's marker and the expected run id.
type reparentCompatOwnership struct {
	Present    bool
	Ours       bool
	Unreadable bool
	RunID      string
	Path       string
}

// readReparentCompatOwnership reads a compatibility artifact as RAW YAML and
// matches the reparent marker. It deliberately never decodes the checkout
// artifact into a CheckoutTransaction: that decode is required to fail, and
// depending on it succeeding here would couple this run to the very shape it
// made undecodable on purpose.
func readReparentCompatOwnership(path, runID string) reparentCompatOwnership {
	out := reparentCompatOwnership{Path: path}
	info, err := os.Lstat(path)
	if err != nil {
		return out
	}
	out.Present = true
	if !info.Mode().IsRegular() {
		out.Unreadable = true
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		out.Unreadable = true
		return out
	}
	var probe struct {
		ReparentMarker string `yaml:"reparent_marker"`
		ReparentRunID  string `yaml:"reparent_run_id"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		out.Unreadable = true
		return out
	}
	out.RunID = probe.ReparentRunID
	out.Ours = runID != "" && probe.ReparentMarker == ReparentCompatMarker && probe.ReparentRunID == runID
	return out
}

// ReparentCompatStatus is the run's whole compatibility envelope, classified.
type ReparentCompatStatus struct {
	// Complete is true when every artifact this mode requires is present and
	// carries this run's marker.
	Complete bool

	// Foreign is true when an artifact exists that is NOT this run's: that is
	// ordinary foreign sync state and refuses sync-state-present.
	Foreign bool

	// Missing names the artifacts that must be re-created under §11.2a.
	Missing []string
}

// ClassifyReparentCompatArtifacts answers §11.2's fourth matrix row: whether
// the run still has the exclusion envelope it wrote, or whether a foreign or
// older binary removed part of it while the authoritative artifact survived.
func ClassifyReparentCompatArtifacts(loc ReparentLocation, runID string) ReparentCompatStatus {
	var paths []string
	if loc.Mode == ModeCheckout {
		paths = []string{CheckoutTransactionPath(loc.FeaturePath)}
	} else {
		paths = []string{SyncRunStatePath(loc.FeaturePath), SyncStatePath(loc.FeaturePath)}
	}
	out := ReparentCompatStatus{Complete: true}
	for _, p := range paths {
		own := readReparentCompatOwnership(p, runID)
		switch {
		case !own.Present:
			out.Complete = false
			out.Missing = append(out.Missing, p)
		case own.Unreadable || !own.Ours:
			out.Complete = false
			out.Foreign = true
		}
	}
	return out
}

// RemoveReparentCompatArtifacts deletes this run's compatibility artifacts in
// the §11.6a order — legacy sentinel first, then the v4 payload or the
// checkout transaction — and tolerates any of them already being gone. It
// refuses to delete an artifact that is not ours, because a foreign sync
// artifact is somebody else's exclusion, not residue.
func RemoveReparentCompatArtifacts(loc ReparentLocation, runID string) error {
	var paths []string
	if loc.Mode == ModeCheckout {
		paths = []string{CheckoutTransactionPath(loc.FeaturePath)}
	} else {
		paths = []string{SyncStatePath(loc.FeaturePath), SyncRunStatePath(loc.FeaturePath)}
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		own := readReparentCompatOwnership(p, runID)
		if !own.Present {
			dirs[filepath.Dir(p)] = true
			continue
		}
		if !own.Ours {
			return fmt.Errorf("refusing to remove %s: it is not this reparent run's compatibility artifact", p)
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		dirs[filepath.Dir(p)] = true
	}
	orderedDirs := make([]string, 0, len(dirs))
	for dir := range dirs {
		orderedDirs = append(orderedDirs, dir)
	}
	sort.Strings(orderedDirs)
	for _, dir := range orderedDirs {
		if err := reparentRecoverySyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// Foreign sync state (§6.2 sync-state-present)
// ============================================================================

// ReparentExclusionOwner is the narrow ownership envelope a post-lock or
// recovery probe may exclude. A fresh pre-lock plan passes nil, so every
// existing sync artifact and lock is foreign. After acquisition, LockBytes
// are the exact bytes this invocation just wrote; recovery additionally
// carries the authoritative run id, owner token and prior PID.
type ReparentExclusionOwner struct {
	RunID           string
	OwnerToken      string
	OwnerPID        int
	LockBytes       []byte
	GlobalLockBytes []byte
}

func reparentLockBytes(loc ReparentLocation) ([]byte, error) {
	path := SyncRunGuardPath(loc.FeaturePath)
	if loc.Mode == ModeCheckout {
		path = CheckoutLockPath(loc.FeaturePath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func reparentLockOwned(loc ReparentLocation, data []byte, owner *ReparentExclusionOwner) bool {
	if owner == nil {
		return false
	}
	if len(owner.LockBytes) > 0 && string(data) == string(owner.LockBytes) {
		return true
	}
	if loc.Mode == ModeCheckout {
		var info LockInfo
		if yaml.Unmarshal(data, &info) != nil || info.PID <= 0 {
			return false
		}
		if info.PID == owner.OwnerPID {
			return true
		}
		// Recovery may crash after transferring the PID-only feature lock but
		// before persisting owner_pid. The token-bound global lock plus the
		// authoritative run identity makes that dead intermediate owner
		// decidable without making arbitrary dead feature locks ours.
		global, _, err := ReadCheckoutMutationLock(loc.CheckoutStateDir)
		if err != nil || global.PID != info.PID || isProcessAlive(info.PID) {
			return false
		}
		return owner.OwnerToken != "" && global.Token == owner.OwnerToken &&
			global.Feature == loc.Feature && global.Operation == "reparent" &&
			global.StatePath == ReparentStatePath(loc)
	}
	var guard SyncRunGuard
	if yaml.Unmarshal(data, &guard) != nil || guard.Token == "" {
		return false
	}
	return owner.OwnerToken != "" && guard.Token == owner.OwnerToken
}

func reparentGlobalLockOwned(loc ReparentLocation, data []byte, owner *ReparentExclusionOwner) bool {
	if owner == nil {
		return false
	}
	if len(owner.GlobalLockBytes) > 0 && string(data) == string(owner.GlobalLockBytes) {
		return true
	}
	var info CheckoutMutationLock
	if yaml.Unmarshal(data, &info) != nil {
		return false
	}
	return owner.OwnerToken != "" && info.Token == owner.OwnerToken &&
		info.Feature == loc.Feature && info.Operation == "reparent" &&
		info.StatePath == ReparentStatePath(loc)
}

// ReparentForeignSyncState reports whether sync state or a sync lock exists
// for this feature in this mode that does NOT belong to the reparent run
// identified by runID (pass "" for a fresh route, where nothing is ours yet).
// It returns the refusal detail, or "" when the path is clear.
func ReparentForeignSyncState(loc ReparentLocation, runID string) string {
	return ReparentForeignSyncStateOwned(loc, &ReparentExclusionOwner{RunID: runID})
}

// ReparentForeignSyncStateOwned is the ownership-aware form used immediately
// after lock acquisition and by recovery. It excludes only exact matching
// reparent compatibility files and the exact lock this run owns.
func ReparentForeignSyncStateOwned(loc ReparentLocation, owner *ReparentExclusionOwner) string {
	type probe struct {
		path   string
		what   string
		lock   bool
		global bool
	}
	var probes []probe
	if loc.Mode == ModeCheckout {
		probes = []probe{
			{path: CheckoutTransactionPath(loc.FeaturePath), what: "a checkout sync transaction"},
			{path: CheckoutLockPath(loc.FeaturePath), what: "a checkout sync lock", lock: true},
			{path: CheckoutMutationLockPath(loc.CheckoutStateDir), what: "a workspace checkout mutation lock", lock: true, global: true},
		}
	} else {
		probes = []probe{
			{path: SyncRunStatePath(loc.FeaturePath), what: "a scoped sync payload"},
			{path: SyncStatePath(loc.FeaturePath), what: "a legacy sync state file"},
			{path: SyncRunGuardPath(loc.FeaturePath), what: "a sync run guard", lock: true},
		}
	}
	for _, pr := range probes {
		info, err := os.Lstat(pr.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Sprintf("%s at %s cannot be inspected: %v", pr.what, pr.path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Sprintf("%s exists at %s but is not a regular file; finish or clean up that run first", pr.what, pr.path)
		}
		if pr.lock {
			data, readErr := os.ReadFile(pr.path)
			owned := readErr == nil && reparentLockOwned(loc, data, owner)
			if pr.global {
				owned = readErr == nil && reparentGlobalLockOwned(loc, data, owner)
			}
			if owned {
				continue
			}
			return fmt.Sprintf("%s exists at %s; finish or clean up that run first", pr.what, pr.path)
		}
		runID := ""
		if owner != nil {
			runID = owner.RunID
		}
		if own := readReparentCompatOwnership(pr.path, runID); own.Ours && !own.Unreadable {
			continue
		}
		return fmt.Sprintf("%s exists at %s; finish or clean up that run first", pr.what, pr.path)
	}
	return ""
}

// ============================================================================
// Pre-image capture (§11.4 step 5, §11.5)
// ============================================================================

// ReparentHolderSnapshot is one worktree that holds an affected branch, as
// observed before any mutation.
type ReparentHolderSnapshot struct {
	Path          string
	RepoCommonDir string
	GitBranch     string
	HeadSHA       string
	Detached      bool
	Prunable      bool
	HolderKind    string
	Excluded      bool
}

// ReparentPreImage is the single carrier of §11.4 step 5. The plan builder
// reads it (read-only) and the executor persists it, so a plan and the run it
// approves can never disagree about what the pre-image was.
type ReparentPreImage struct {
	// RowSHAs is every closure row's branch tip, keyed by logical name.
	RowSHAs map[string]string

	// StackBytes are the EXACT stack.yaml bytes, never a re-marshalled
	// struct: both a forward write and an abort restore must reproduce the
	// file byte-for-byte.
	StackBytes  []byte
	StackSHA256 string

	Holders []ReparentHolderSnapshot

	OriginalBranch   string
	OriginalHead     string
	OriginalDetached bool

	RefBackend string
	OIDWidth   int

	RepoRoot          string
	RepoCommonDir     string
	WorkspaceRepoRoot string

	// ValidationCommand is the raw frozen command. It reaches the 0600
	// artifact and NOTHING else; the plan publishes only ValidationDigest.
	ValidationCommand string
	ValidationSource  string
	ValidationDigest  string
}

// ReparentPreImageSHA returns one row's captured pre-image tip.
func (p ReparentPreImage) ReparentPreImageSHA(name string) string {
	return p.RowSHAs[name]
}

// ReparentActiveInStateDir answers "does a checkout-mode reparent artifact
// exist for this feature in this state directory?" without a Workspace. The
// checkout observability surfaces walk the state directory itself and have no
// workspace to hand; the authoritative artifact's checkout path is derived
// from exactly those two values, so nothing else is needed.
//
// It is a bare os.Stat: strictly read-only, no decode, no Git child.
func ReparentActiveInStateDir(stateDir, feature string) bool {
	if stateDir == "" || feature == "" {
		return false
	}
	return HasReparentState(ReparentLocation{
		Mode:             ModeCheckout,
		Feature:          feature,
		CheckoutStateDir: stateDir,
	})
}

// ============================================================================
// §14.2a — the `.reparent*` runtime filter
// ============================================================================

// ReparentScratchDirName is the external computation worktree's parent
// directory name and the prefix of the feature-directory runtime artifacts.
// Import filtering uses this family; worktree status filtering is stricter and
// uses only exact scratch_path values from authoritative active state.
const ReparentScratchDirName = ".reparent"

// IsReparentRuntimeName reports whether one path component is a reparent-owned
// runtime artifact: `.reparent`, `.reparent-state.v1.yaml`,
// `.reparent-remote.v1.yaml` and every future `.reparent*` sibling. The family
// is dot-prefixed and tool-owned, so no user file can collide with it.
func IsReparentRuntimeName(name string) bool {
	return strings.HasPrefix(name, ReparentScratchDirName)
}

// IsReparentRuntimePath reports whether ANY component of a path belongs to the
// family. Paths are compared component-wise, never as a substring, so a
// directory legitimately named `my.reparenting` is never filtered.
func IsReparentRuntimePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if IsReparentRuntimeName(part) {
			return true
		}
	}
	return false
}

// ActiveReparentScratchPath returns the one exact registered scratch path an
// authoritative external run currently owns.
func ActiveReparentScratchPath(loc ReparentLocation) string {
	if loc.Mode != ModeExternal {
		return ""
	}
	load := LoadReparentState(loc)
	if load.Kind != ReparentStateOK || load.State == nil || load.State.ScratchPath == "" {
		return ""
	}
	return canonicalize(load.State.ScratchPath)
}

// ExcludeReparentScratchWorktrees drops only exact active scratch_path
// records, and re-derives ByBranch, Prunable and ByPath from what remains.
//
// It layers ON TOP of BuildWorktreeInventory and never inside it: the parser
// stays a faithful projection of Git's porcelain — which the executor's own
// holder machinery depends on — while every OBSERVABILITY surface sees only
// worktrees that materialize a logical branch. An unavailable inventory passes
// through untouched, errors and all.
func ExcludeReparentScratchWorktrees(inv WorktreeInventory, scratchPaths ...string) WorktreeInventory {
	if !inv.Available || len(scratchPaths) == 0 {
		return inv
	}
	active := map[string]bool{}
	for _, path := range scratchPaths {
		if strings.TrimSpace(path) != "" {
			active[canonicalize(path)] = true
		}
	}
	if len(active) == 0 {
		return inv
	}
	dropped := false
	for _, rec := range inv.Records {
		if active[canonicalize(rec.Path)] {
			dropped = true
			break
		}
	}
	if !dropped {
		// Nothing to filter: the caller keeps the parser's own value, so a
		// workspace with no reparent scratch is byte-identical to today.
		return inv
	}

	out := WorktreeInventory{
		Available: true,
		ByBranch:  map[string]string{},
		Prunable:  map[string]bool{},
		ByPath:    map[string]WorktreeRecord{},
		Err:       inv.Err,
	}
	// Retained records keep the parser's OWN derived values verbatim: the
	// filter removes rows, it never re-derives them, so a retained entry's
	// ByBranch path stays exactly the string Git reported.
	for _, rec := range inv.Records {
		if active[canonicalize(rec.Path)] {
			continue
		}
		out.Records = append(out.Records, rec)
		out.ByPath[rec.Path] = rec
	}
	retained := func(path string) bool {
		_, ok := out.ByPath[path]
		return ok || !active[canonicalize(path)]
	}
	for short, path := range inv.ByBranch {
		if retained(path) {
			out.ByBranch[short] = path
		}
	}
	for short, v := range inv.Prunable {
		out.Prunable[short] = v
	}
	return out
}
