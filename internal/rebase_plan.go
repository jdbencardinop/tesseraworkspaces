package internal

import (
	"encoding/json"
	"path/filepath"
)

// ============================================================================
// Schema version and route constants (§4.1, §13.1)
// ============================================================================

// RebasePlanSchemaVersion is the current, and so far only, RebasePlan.SchemaVersion.
const RebasePlanSchemaVersion = 1

// RouteLegacy and RouteNewMode are the two RebasePlan.Route / RequestedRoute values.
const (
	RouteLegacy  = "legacy"
	RouteNewMode = "new-mode"
)

// ============================================================================
// RebasePlan — the twenty-five top-level document keys (§4.1, §9.1a)
// ============================================================================

// RebasePlan is the document value. Its members are the twenty-five top-level
// keys of §4.1, in that order and no other: this struct IS the source model
// and the JSON projection, so there is no second enumeration to disagree with
// §4.1. The JSON encoder emits keys in this declaration order.
type RebasePlan struct {
	SchemaVersion  int                 `json:"schema_version"` // RebasePlanSchemaVersion == 1
	Route          string              `json:"route"`
	RequestedRoute *string             `json:"requested_route"` // null when equal to Route
	RouteTriggers  []string            `json:"route_triggers"`  // never nil
	Invocation     string              `json:"invocation"`      // "plan-only"
	Workspace      PlanWorkspace       `json:"workspace"`
	Feature        string              `json:"feature"`
	Policy         PlanPolicy          `json:"policy"`
	Intent         PlanIntent          `json:"intent"`
	Push           PlanPush            `json:"push"`
	Restore        PlanRestore         `json:"restore"`
	Fetch          PlanFetch           `json:"fetch"`
	Freshness      string              `json:"freshness"`    // twelve-value enum, §11.3
	Repositories   []PlanRepository    `json:"repositories"` // never nil
	State          PlanState           `json:"state"`
	Runnable       bool                `json:"runnable"`
	Blockers       []PlanBlocker       `json:"blockers"`        // never nil
	Warnings       []PlanWarning       `json:"warnings"`        // never nil
	EncodingIssues []PlanEncodingIssue `json:"encoding_issues"` // never nil
	ConfigIssues   []PlanConfigIssue   `json:"config_issues"`   // never nil
	Entries        []PlanEntry         `json:"entries"`         // never nil
	Summary        PlanSummary         `json:"summary"`
	Guard          PlanGuardBlock      `json:"guard"`
	Refusal        PlanRefusal         `json:"refusal"`
	Approval       PlanApproval        `json:"approval"`
}

// PlanWorkspace is RebasePlan.Workspace (§9.1a).
type PlanWorkspace struct {
	Mode     string  `json:"mode"`
	StableID *string `json:"stable_id"`
	RepoRoot string  `json:"repo_root"` // sanitized, display-only, never a join/sort/tuple input
}

// PlanPolicy is RebasePlan.Policy (§9.1a, §4.1).
type PlanPolicy struct {
	Fetch               string  `json:"fetch"`
	Propagation         string  `json:"propagation"`
	ScopeKind           string  `json:"scope_kind"` // all | one | subtree — never `from`
	Selector            *string `json:"selector"`
	FetchDefaultApplied bool    `json:"fetch_default_applied"`
}

// PlanRefusal is RebasePlan.Refusal — {kind, detail} (§9.1a, §4.6).
type PlanRefusal struct {
	Kind   *RefusalKind `json:"kind"`   // null when nothing refuses
	Detail *string      `json:"detail"` // null iff Kind is null
}

// ============================================================================
// RebasePlanLayout (§9.0) — the layout boundary; internal never learns a cli type
// ============================================================================

// RebasePlanLayout is the filesystem boundary BuildRebasePlan and its helpers
// need, expressed entirely in terms this package already owns. package cli
// projects its own layout type into this one at the call site; internal never
// imports a cli type.
type RebasePlanLayout struct {
	FeaturePath   string // the feature's metadata directory
	WorktreesRoot string // "" on checkout mode
	RepoRoot      string // "" unless a pass-2 process-cwd context applies (external); opts.RepoDir (checkout)
}

// WorktreePath mirrors externalSyncLayout.WorktreePath exactly (§9.0): it joins
// WorktreesRoot and name, and returns "" when WorktreesRoot is empty (checkout
// mode never has a worktree tree to join against).
func (l RebasePlanLayout) WorktreePath(name string) string {
	if l.WorktreesRoot == "" {
		return ""
	}
	return filepath.Join(l.WorktreesRoot, name)
}

// ============================================================================
// PlanBasePreflight (§10.7) — the I14 verdict, evaluated at most once per run
// ============================================================================

// PlanBasePreflight is RebasePlanRequest.BasePreflight (§10.7 rule 1a).
type PlanBasePreflight struct {
	Applies bool   // false on every route that cannot reach I14
	Failed  bool   // the I14 verdict; meaningless unless Applies
	Entry   string // the offending logical entry name; "" unless Failed
	Ref     string // the offending base ref; "" unless Failed
	Detail  string // the shipped sentence, verbatim; "" unless Failed
}

// ============================================================================
// PlanContinuationGate (§13.7a rule 9) — the projected shipped continuation verdict
// ============================================================================

// PlanContinuationGate is RebasePlanRequest.ContinuationGate: the checkout
// continuation-gate verdict, projected (never re-evaluated) from the shipped
// ladder into the document/request boundary.
type PlanContinuationGate struct {
	Applies bool   // false on a fresh run
	Failed  bool   // the shipped gate's verdict; meaningless unless Applies
	Axis    string // which shipped gate fired; "" unless Failed
	Detail  string // the shipped sentence, verbatim; "" unless Failed
}

// ============================================================================
// RebasePlanRequest (§9.1a) — the complete input of BuildRebasePlan
// ============================================================================

// RebasePlanRequest is the input BuildRebasePlan (internal/rebase_plan_build.go)
// consumes: every member is an internal-owned value produced above the
// builder, which derives and never re-measures.
//
// §9.1a's full member list is now complete. Group 5 carries all eleven
// members that section adds beyond this struct's pre-§9.1a shape: Version
// (GitVersion), Capabilities (GitCapabilities), ExternalState
// (ExternalPlanState) and CheckoutState (CheckoutPlanState) come from
// internal/git_capability.go and internal/rebase_plan_state.go; Fetch
// (PlanFetchOutcome), FetchPlan (PlanFetchPlan), Gates ([]PlanGateResult),
// Guard (CheckoutPlanGuard), Limits (PlanGuardLimits), Validation
// (PlanValidationIdentity) and StageFacts ([]PlanStageFact) come from
// internal/rebase_plan_guard.go. Every group-5 member not applicable to a
// given invocation is left at its Go zero value — buildFetch, buildGuard,
// buildApproval, buildIntent and buildFreshness all treat that zero value as
// "this route does not measure the fact", which they render exactly as the
// document's own pre-guard hardcoded defaults, so a caller that never
// populates these seven fields observes no change in BuildRebasePlan's
// output. Go struct literals used anywhere in this tree are field-keyed, so
// this was, and remains, an additive change.
type RebasePlanRequest struct {
	// 1 — identity and boundary
	Layout    RebasePlanLayout // §9.0; checkout fills RepoRoot only
	Mode      WorkspaceMode    // ModeExternal | ModeCheckout; never derived from Layout emptiness
	Feature   string           // resolved feature name (document key `feature`)
	Workspace PlanWorkspace    // {Mode, StableID, RepoRoot} — display-only RepoRoot (§8.3)

	// 2 — the effective run, resolved ONCE above the builder (§13.2, §13.6 rule 4, §15)
	Route                     string                   // RouteLegacy | RouteNewMode — the EFFECTIVE route
	RequestedRoute            string                   // "" when equal to Route ⇒ requested_route: null
	RouteTriggers             []string                 // sorted trigger names; never nil; [] on legacy
	Invocation                string                   // "plan-only" in v1 (§4.1)
	Policy                    SyncRunPolicy            // effective fetch/propagation/scope/selector
	PolicyFetchDefaultApplied bool                     // policy.fetch_default_applied (§4.1)
	Push                      bool                     // intent.push
	PushSource                string                   // intent.push_source (§4.6)
	Guard                     CheckoutPlanGuard        // raw checkout control-flag envelope, echoed for traceability
	Limits                    PlanGuardLimits          // the RECONCILED effective limits (never req.Guard) buildGuard keys off
	LimitConflicts            []PlanGuardLimitConflict // §4.6; never nil
	Validation                PlanValidationIdentity   // intent.validation identity inputs (§15); never the raw command
	Approve                   string                   // the supplied --approve-plan token; "" when absent

	// 3 — the subject, loaded and sorted EXACTLY ONCE by the caller (rule 3)
	Stack             *Stack       // nil iff the route could not read it ⇒ rows-less document
	Order             []StackEntry // the ONE TopoSort result; nil iff SortErr != nil
	SortErr           error        // the verbatim `cycle detected in stack.yaml`
	StackErr          error        // the verbatim LoadStack error
	Selection         SyncSelection
	SelectionResolved bool  // ResolveSyncSelectionFromOrder ran over Order and succeeded (§9.2a)
	SelectionErr      error // the shipped sentence, verbatim
	RowsAvailable     bool  // §13.7 rule 4 / §13.7a rule 4; the sole answer to "publish entries[]?"

	// 4 — continuation inputs (zero on a fresh run)
	Continue         bool
	Remaining        []string             // RemainingRebaseEntries output (§13.3); never re-derived
	StageFacts       []PlanStageFact      // one row per Remaining entry's next-command facts (§13.3, §13.7a); never re-derived
	Changed          map[string]bool      // verbatim resolveSyncPolicy output; read-only (§13.7a rule 9)
	ContinuationGate PlanContinuationGate // the projected shipped verdict (§13.7a rule 9)

	// 5 — what this invocation already measured; the builder consumes, never
	// probes, any of these thirteen §9.1a group-5 members. Version/Capabilities/
	// ExternalState/CheckoutState are produced exactly once per invocation by
	// ProbeGitVersion+GitCapabilitiesForVersion and by
	// InspectCheckoutPlanState/InspectExternalPlanState respectively;
	// Fetch/FetchPlan/Gates come from internal/rebase_plan_guard.go's own
	// pre-mutation measurement pass (checkoutFetchPlan and friends).
	Fetch         PlanFetchOutcome  // this invocation's OWN measured fetch outcome; zero value ⇒ "not measured"
	FetchPlan     PlanFetchPlan     // the pre-fetch context/effect enumeration (§11.1); zero value ⇒ "not measured"
	PushFacts     PlanPushFacts     // §14.1a rule 9: the ONE push-fact carrier
	BasePreflight PlanBasePreflight // I14, evaluated at most once per invocation (§10.7 rule 1a)
	Version       GitVersion        // ProbeGitVersion's verbatim result (internal/git_capability.go)
	Capabilities  GitCapabilities   // the six capability gates derived from Version
	ExternalState ExternalPlanState // Applicable exactly on an external route (§13.7a rule 8)
	CheckoutState CheckoutPlanState // Applicable exactly on a checkout route (§13.7a rule 8)
	Gates         []PlanGateResult  // every ordered §13.7/§13.7a gate's own verdict; never re-derived by the builder
}

// ============================================================================
// PlanGuardLimitConflict (§4.6, guard.limit_conflicts[] row)
// ============================================================================

// PlanGuardLimitConflict is one guard.limit_conflicts[] row: a continuation
// supplied a limit different from the one persisted at claim time.
type PlanGuardLimitConflict struct {
	Key             string `json:"key"`              // max_replay_per_entry | max_replay_total
	EffectiveValue  *int   `json:"effective_value"`  // the persisted value that stays effective
	EffectiveOrigin string `json:"effective_origin"` // guard.limits.*.origin domain
	SuppliedValue   *int   `json:"supplied_value"`   // the value this continuation supplied
}

// ============================================================================
// PlanIntent (§4.6 `intent` object, including nested `validation`)
// ============================================================================

// PlanIntent is RebasePlan.Intent.
type PlanIntent struct {
	Push       bool                 `json:"push"`
	PushSource string               `json:"push_source"` // intent.push_source domain, §3
	Validation PlanIntentValidation `json:"validation"`
}

// PlanIntentValidation is intent.validation (§15).
type PlanIntentValidation struct {
	Applies          bool    `json:"applies"`
	Source           string  `json:"source"`
	Stability        string  `json:"stability"`
	GuardedStability string  `json:"guarded_stability"`
	CommandDigest    *string `json:"command_digest"`
	CLITestIgnored   bool    `json:"cli_test_ignored"`
}

// ============================================================================
// PlanContext (§2.15 Group A `base_context` / `execution_context` shape)
// ============================================================================

// PlanContext is the shared {context_id, repo_root, source} shape used for
// both entries[].base_context and entries[].execution_context.
type PlanContext struct {
	ContextID *string `json:"context_id"` // 64-lower-hex; null iff not established or source:not-applicable
	RepoRoot  *string `json:"repo_root"`  // sanitized display; null exactly with ContextID
	Source    string  `json:"source"`     // entry-repo | worktree | process-cwd | workspace-repo-root | not-applicable
}

// ============================================================================
// PlanRepository (§2.11 `repositories[]` row)
// ============================================================================

// PlanRepository is one repositories[] row.
type PlanRepository struct {
	Repo       string               `json:"repo"`        // "" sorts first, is a value not absence
	ContextID  string               `json:"context_id"`  // 64-lower-hex
	RepoRoot   string               `json:"repo_root"`   // sanitized display-only
	RootSource string               `json:"root_source"` // worktree | entry-repo | process-cwd | workspace-repo-root
	Config     PlanRepositoryConfig `json:"config"`
}

// PlanRepositoryConfig is repositories[].config (§2.11).
type PlanRepositoryConfig struct {
	UpdateRefs   PlanConfigSlot `json:"update_refs"`
	RebaseMerges PlanConfigSlot `json:"rebase_merges"`
	Backend      PlanConfigSlot `json:"backend"`
	AutoStash    PlanConfigSlot `json:"auto_stash"`
}

// PlanConfigSlot is the shared config-slot shape. Interpretation is the one
// declared arity exception (§2.11): it is set, to one of true|false|unknown|
// invalid, only on the rebase_merges slot, and the omitempty tag drops the key
// entirely (never null, never "") on the other three slots.
type PlanConfigSlot struct {
	Status         string  `json:"status"` // valid | absent | not-evaluated | probe-failed | invalid
	Value          *string `json:"value"`
	Source         *string `json:"source"`
	Interpretation string  `json:"interpretation,omitempty"`
}

// ============================================================================
// PlanConfigIssue (§2.12 `config_issues[]` row, 11 members)
// ============================================================================

// PlanConfigIssue is one config_issues[] row.
type PlanConfigIssue struct {
	IssueID         string  `json:"issue_id"`      // 64-lower-hex; sole sort/dedup key
	ContextID       string  `json:"context_id"`    // 64-lower-hex
	RepoRoot        *string `json:"repo_root"`     // sanitized display
	Key             string  `json:"key"`           // offending Git config key
	Source          string  `json:"source"`        // 8-member enum, §3
	RouteCommand    string  `json:"route_command"` // fetch | checkout | push | rebase
	ErrorKind       string  `json:"error_kind"`    // 6-member enum, §3
	RawValuePresent bool    `json:"raw_value_present"`
	RawValueBase64  *string `json:"raw_value_base64"` // null iff !RawValuePresent
	RawValueSHA256  *string `json:"raw_value_sha256"` // null iff !RawValuePresent
	SanitizedValue  *string `json:"sanitized_value"`  // null iff !RawValuePresent
}

// ============================================================================
// PlanEncodingIssue (§2.13 `encoding_issues[]` row, 6 members, ASCII-only)
// ============================================================================

// PlanEncodingIssue is one encoding_issues[] row.
type PlanEncodingIssue struct {
	FieldID         string  `json:"field_id"`
	PathLabelASCII  string  `json:"path_label_ascii"`
	OwnerEntryASCII *string `json:"owner_entry_ascii_or_null"`
	ByteLength      int     `json:"byte_length"`
	RawBase64       string  `json:"raw_base64"`
	SHA256          string  `json:"sha256"`
}

// ============================================================================
// PlanFetch and its nested fetch-effect/candidate/submodule/local-branch types
// (§4.4a, §11.x)
// ============================================================================

// PlanFetch is RebasePlan.Fetch (7 members, §4.4a).
type PlanFetch struct {
	Attempted                 bool            `json:"attempted"`
	Outcome                   string          `json:"outcome"`
	PolicySource              string          `json:"policy_source"`
	SuppressionCause          *string         `json:"suppression_cause"` // 6-cause domain
	MutatedRemoteTrackingRefs *bool           `json:"mutated_remote_tracking_refs"`
	MutatedLocalBranches      *bool           `json:"mutated_local_branches"`
	Repos                     []PlanFetchRepo `json:"repos"` // never nil
}

// PlanFetchRepo is one fetch.repos[] row (§2.5). ContextRoot, ContextCommonDir
// and ContextSource are empty-string sentinels in Go (so the zero value is
// cheap to construct) that MarshalJSON turns into JSON null; OK is likewise
// published as null exactly when Attempted is false. A custom MarshalJSON is
// required because encoding/json has no way to derive a null from a
// non-pointer "" or from a bool gated by a sibling field.
type PlanFetchRepo struct {
	RepoToken         string
	ContextRoot       string // "" ⇒ null
	ContextCommonDir  string // "" ⇒ null
	ContextSource     string // "" ⇒ null
	Effect            *PlanFetchEffect
	ContextCandidates []PlanFetchCandidate // never nil
	Attempted         bool
	OK                bool // published as null when !Attempted
}

// planFetchRepoWire mirrors PlanFetchRepo field-for-field but with nullable
// wire types, so MarshalJSON can build one value and delegate to the standard
// encoder without hand-rolling byte output (which would risk drifting from
// the rest of this package's JSON conventions).
type planFetchRepoWire struct {
	RepoToken         string               `json:"repo_token"`
	ContextRoot       *string              `json:"context_root"`
	ContextCommonDir  *string              `json:"context_common_dir"`
	ContextSource     *string              `json:"context_source"`
	Effect            *PlanFetchEffect     `json:"fetch_effect"`
	ContextCandidates []PlanFetchCandidate `json:"context_candidates"`
	Attempted         bool                 `json:"attempted"`
	OK                *bool                `json:"ok"`
}

// MarshalJSON implements the empty-string/bool-gated-by-Attempted ⇒ null rules
// documented on PlanFetchRepo, preserving §2.5's declared key order.
func (r PlanFetchRepo) MarshalJSON() ([]byte, error) {
	candidates := r.ContextCandidates
	if candidates == nil {
		candidates = []PlanFetchCandidate{}
	}
	wire := planFetchRepoWire{
		RepoToken:         r.RepoToken,
		ContextRoot:       nonEmptyStringPtr(r.ContextRoot),
		ContextCommonDir:  nonEmptyStringPtr(r.ContextCommonDir),
		ContextSource:     nonEmptyStringPtr(r.ContextSource),
		Effect:            r.Effect,
		ContextCandidates: candidates,
		Attempted:         r.Attempted,
	}
	if r.Attempted {
		ok := r.OK
		wire.OK = &ok
	}
	return json.Marshal(wire)
}

// nonEmptyStringPtr returns nil for "" and &s otherwise, the shared rule
// PlanFetchRepo's three context fields use to become JSON null.
func nonEmptyStringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// PlanFetchCandidate is one fetch.repos[].context_candidates[] row.
type PlanFetchCandidate struct {
	ContextRoot   string  `json:"context_root"`
	ContextSource string  `json:"context_source"`
	FirstEntry    *string `json:"first_entry"`
}

// PlanFetchEffect is fetch.repos[].fetch_effect (11 members, §11.4).
type PlanFetchEffect struct {
	All                     bool                        `json:"all"`
	Contacted               bool                        `json:"contacted"`
	HeadBranch              *string                     `json:"head_branch"` // nullable, §11.4
	MayDeleteRefs           bool                        `json:"may_delete_refs"`
	MayDeleteTags           bool                        `json:"may_delete_tags"`
	MayClobberTags          bool                        `json:"may_clobber_tags"`
	MayUpdateLocalBranches  bool                        `json:"may_update_local_branches"`
	MayDeleteLocalBranches  bool                        `json:"may_delete_local_branches"`
	LocalBranchDestinations PlanLocalBranchDestinations `json:"local_branch_destinations"`
	Remotes                 []PlanFetchRemote           `json:"remotes"` // nullable array — exception
	SubmoduleRecursion      PlanSubmoduleRecursion      `json:"submodule_recursion"`
}

// PlanFetchRemote is one fetch_effect.remotes[] row.
type PlanFetchRemote struct {
	Name          string        `json:"name"`
	Source        string        `json:"source"`
	URLConfigured string        `json:"url_configured"`
	URLEffective  string        `json:"url_effective"`
	URLSource     string        `json:"url_source"`
	Refspecs      []PlanRefspec `json:"refspecs"` // nullable, configured order
	Prune         bool          `json:"prune"`
	PruneTags     bool          `json:"prune_tags"`
	TagOpt        string        `json:"tag_opt"`
}

// PlanSubmoduleRecursion is fetch_effect.submodule_recursion (§11.6).
type PlanSubmoduleRecursion struct {
	Mode       string               `json:"mode"`
	ModeSource string               `json:"mode_source"`
	Reach      string               `json:"reach"`
	Submodules []PlanSubmoduleEntry `json:"submodules"` // nullable array — exception
}

// PlanSubmoduleEntry is one submodule_recursion.submodules[] row.
type PlanSubmoduleEntry struct {
	Path string `json:"path"`
}

// PlanLocalBranchDestinations is fetch_effect.local_branch_destinations (§11.6).
type PlanLocalBranchDestinations struct {
	Patterns  []PlanFetchRefspecPattern `json:"patterns"` // nullable array — exception; never sorted
	Branches  []string                  `json:"branches"` // nullable array — exception; bytewise sorted
	Truncated bool                      `json:"truncated"`
	Held      []PlanBranchHold          `json:"held"` // nullable array — exception
}

// PlanFetchRefspecPattern is one local_branch_destinations.patterns[] row.
type PlanFetchRefspecPattern struct {
	Remote string `json:"remote"`
	Dst    string `json:"dst"`
	Forced bool   `json:"forced"`
}

// PlanBranchHold is one local_branch_destinations.held[] row.
type PlanBranchHold struct {
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	Hold     string `json:"hold"` // BranchHoldMechanism domain (internal/rebase_plan_probe.go)
}

// ============================================================================
// PlanPush, PlanPushTarget, PlanLease (§4.4a, 4 + 10 + 5 members)
// ============================================================================

// PlanPush is RebasePlan.Push (4 members).
type PlanPush struct {
	Targets    []PlanPushTarget  `json:"targets"` // never nil
	Executable *bool             `json:"executable"`
	BlockedBy  []PushBlockedKind `json:"blocked_by"` // never nil
	Scope      string            `json:"scope"`
}

// PlanPushTarget is one push.targets[] row — the ten §4.4a members
// (internal/rebase_plan.go, §14.1a, §19.1).
type PlanPushTarget struct {
	Repo            string    `json:"repo"`
	ContextID       *string   `json:"context_id"`
	RepoRoot        *string   `json:"repo_root"`
	Materialization string    `json:"materialization"`
	GitBranch       string    `json:"git_branch"`
	Remote          string    `json:"remote"`
	Ref             string    `json:"ref"`
	Force           string    `json:"force"`
	Lease           PlanLease `json:"lease"`
	Scope           string    `json:"scope"`
}

// PlanLease is push.targets[].lease — the five §4.4a members.
type PlanLease struct {
	Mode                 string  `json:"mode"`
	Expectation          string  `json:"expectation"`
	ExpectedRef          *string `json:"expected_ref"`
	ExpectedSHA          *string `json:"expected_sha"`
	ExpectedSHAFreshness string  `json:"expected_sha_freshness"`
}

// ============================================================================
// Push-fact plumbing (§14.1a) — internal-only carriers, never marshalled
// directly as part of the RebasePlan document (only PlanPushTarget is).
// ============================================================================

// PlanPushContext is one distinct push context PushTargets projects into rows.
type PlanPushContext struct {
	ContextID       *string
	RepoRoot        string
	Source          string
	Materialization string
}

// PlanContextIdentities is the one table every context_id in a document is
// drawn from (§14.1a).
type PlanContextIdentities map[string]PlanContextIdentity

// PlanContextIdentity is one PlanContextIdentities entry.
type PlanContextIdentity struct {
	ContextID string
	RepoRoot  string
	CommonDir string
}

// PlanRefspec is a single decomposed remote.<name>.fetch entry, in configured
// order (§14.1a, also used by fetch_effect.remotes[].refspecs).
type PlanRefspec struct {
	Raw      string
	Negative bool
	Forced   bool
	Src      string
	Dst      string
}

// PlanPushRemoteFacts is the per-context remote-tracking facts measured in the
// two declared §14.1a phases (MeasurePushRemoteFacts / RefreshPushTrackingRefs).
type PlanPushRemoteFacts struct {
	ContextID      *string
	CommonDir      string
	RemoteName     string
	MappingReadOK  bool // distinguishes "read and empty" from "not read" (§14.1a rule 5a)
	RemoteExists   bool
	FetchRefspecs  []PlanRefspec
	TrackingRefs   map[string]string
	TrackingReadOK bool
	TrackingPhase  string // pre-fetch | post-fetch | plan-point | not-applicable
	FetchedThisRun bool
}

// PlanPushFacts is RebasePlanRequest.PushFacts — the ONE push-fact carrier
// (§14.1a rule 9).
type PlanPushFacts struct {
	Applies         bool
	Phase           string // pre-fetch | post-fetch | plan-point | not-applicable
	Identities      PlanContextIdentities
	Contexts        []PlanPushContext
	Materialization map[string]string
	Remotes         map[string]PlanPushRemoteFacts
}

// PlanPushRequest is PushTargets' sole parameter (§14.1a).
type PlanPushRequest struct {
	Mode          WorkspaceMode
	Route         string
	Layout        RebasePlanLayout
	Intent        bool
	Scope         string
	Stack         *Stack
	Order         []StackEntry
	Selection     SyncSelection
	Remaining     []string
	Transaction   *CheckoutTransaction
	Expected      map[string]bool
	AlreadyPushed map[string]bool
	Facts         PlanPushFacts
}

// ============================================================================
// PlanRestore (§14.4, 10 members)
// ============================================================================

// PlanRestore is RebasePlan.Restore.
type PlanRestore struct {
	Applies            bool                 `json:"applies"`
	WillSwitchHead     *bool                `json:"will_switch_head"`
	TargetBranch       *string              `json:"target_branch"`
	TargetSHA          *string              `json:"target_sha"`
	TargetSource       *string              `json:"target_source"`
	Executable         *bool                `json:"executable"`
	BlockedBy          []RestoreBlockedKind `json:"blocked_by"` // never nil
	DeletesTransaction bool                 `json:"deletes_transaction"`
	ReleasesLock       bool                 `json:"releases_lock"`
	PushDropped        bool                 `json:"push_dropped"`
}

// ============================================================================
// PlanState (§4.5) — the runtime-snapshot PROJECTION into the document.
//
// The runtime-snapshot PRODUCER types (PlanFilePresence, PlanSnapshotFacts,
// PlanCheckoutTransactionFile, PlanCheckoutLockFile, PlanLegacySyncStateFile,
// PlanSyncRunPayloadFile, PlanSyncRunGuardFile, PlanWorktreeFacts,
// PlanHeadFacts, PlanGitOp, ...) live in internal/rebase_plan_state.go
// (§12.5a); there is no PlanArtefactPresence anywhere. The types below are
// this file's own, distinctly-named document-shaped carriers for `state`'s
// six members, so BuildRebasePlan can render state.* from those producer
// values without this file importing — or re-declaring — their symbols.
// ============================================================================

// PlanState is RebasePlan.State (6 members, §4.5).
type PlanState struct {
	Snapshot     PlanStateSnapshot     `json:"snapshot"`
	Files        PlanStateFiles        `json:"files"`
	ExternalCell PlanStateExternalCell `json:"external_cell"`
	Worktree     PlanStateWorktree     `json:"worktree"`
	GitOp        PlanStateGitOp        `json:"git_op"`
	Head         PlanStateHead         `json:"head"`
}

// PlanStateSnapshot is state.snapshot.
type PlanStateSnapshot struct {
	TakenBeforeAcquisition bool `json:"taken_before_acquisition"`
	SelfPID                int  `json:"self_pid"`
}

// PlanStateFileBase is the common {applicable, presence, unreadable_reason}
// prefix every state.files.* row shares; it is embedded so its three keys
// come first in each row's JSON object, in this order.
type PlanStateFileBase struct {
	Applicable       bool    `json:"applicable"`
	Presence         string  `json:"presence"`          // absent | readable | unreadable
	UnreadableReason *string `json:"unreadable_reason"` // null unless Presence == unreadable
}

// PlanStateFileCheckoutTransaction is state.files.checkout_transaction.
type PlanStateFileCheckoutTransaction struct {
	PlanStateFileBase
	StateVersion *int    `json:"state_version"`
	Feature      *string `json:"feature"`
}

// PlanStateFileCheckoutLock is state.files.checkout_lock.
type PlanStateFileCheckoutLock struct {
	PlanStateFileBase
	OwnerPID   *int    `json:"owner_pid"`
	OwnerLive  *bool   `json:"owner_live"`
	OwnerHost  *string `json:"owner_host"`
	AcquiredAt *string `json:"acquired_at"`
}

// PlanStateFileExternalLegacyState is state.files.external_legacy_state.
type PlanStateFileExternalLegacyState struct {
	PlanStateFileBase
	Feature *string `json:"feature"`
}

// PlanStateFileExternalPayload is state.files.external_run_payload.
type PlanStateFileExternalPayload struct {
	PlanStateFileBase
	Feature  *string  `json:"feature"`
	Selected []string `json:"selected"`
}

// PlanStateFileExternalRunGuard is state.files.external_run_guard.
type PlanStateFileExternalRunGuard struct {
	PlanStateFileBase
	OwnerPID  *int  `json:"owner_pid"`
	OwnerLive *bool `json:"owner_live"`
}

// PlanStateFiles is state.files — the five-file-row group (§12.5).
type PlanStateFiles struct {
	CheckoutTransaction PlanStateFileCheckoutTransaction `json:"checkout_transaction"`
	CheckoutLock        PlanStateFileCheckoutLock        `json:"checkout_lock"`
	ExternalLegacyState PlanStateFileExternalLegacyState `json:"external_legacy_state"`
	ExternalRunPayload  PlanStateFileExternalPayload     `json:"external_run_payload"`
	ExternalRunGuard    PlanStateFileExternalRunGuard    `json:"external_run_guard"`
}

// PlanStateExternalCell is state.external_cell: the pre-acquisition
// SyncStateCell classification, published only where Applies (external mode).
type PlanStateExternalCell struct {
	Applies bool           `json:"applies"`
	Cell    *SyncStateCell `json:"cell"` // null unless Applies
}

// PlanStateWorktree is state.worktree.
type PlanStateWorktree struct {
	Applies bool  `json:"applies"`
	Dirty   *bool `json:"dirty"`
	ProbeOK *bool `json:"probe_ok"`
}

// PlanStateGitOp is state.git_op.
type PlanStateGitOp struct {
	Applies    bool    `json:"applies"`
	InProgress *bool   `json:"in_progress"`
	Kind       *string `json:"kind"`
	KindSource *string `json:"kind_source"`
}

// PlanStateHead is state.head.
type PlanStateHead struct {
	Applies  bool    `json:"applies"`
	Detached *bool   `json:"detached"`
	Branch   *string `json:"branch"`
}

// ============================================================================
// PlanGuardBlock, PlanGuardLimit, PlanGuardEvaluation (§4.6)
// ============================================================================

// PlanGuardBlock is RebasePlan.Guard (7 members).
type PlanGuardBlock struct {
	Limits                     PlanGuardLimitSet        `json:"limits"`
	LimitConflicts             []PlanGuardLimitConflict `json:"limit_conflicts"`      // never nil
	Evaluation                 []PlanGuardEvaluation    `json:"evaluation"`           // never nil
	IndeterminacyPolicy        string                   `json:"indeterminacy_policy"` // constant "jit-deferred"
	WouldRefuseWithoutApproval bool                     `json:"would_refuse_without_approval"`
	WouldRefuse                bool                     `json:"would_refuse"`
	// ExecuteBlockedBy is guard.execute_blocked_by[]: closed at exactly five
	// sorted, de-duplicated, non-waivable tokens, own type (never a
	// []RefusalKind, never a []string).
	ExecuteBlockedBy []ControlledPathBlocker `json:"execute_blocked_by"` // never nil
}

// PlanGuardLimitSet is guard.limits: {max_replay_per_entry, max_replay_total}.
type PlanGuardLimitSet struct {
	MaxReplayPerEntry PlanGuardLimit `json:"max_replay_per_entry"`
	MaxReplayTotal    PlanGuardLimit `json:"max_replay_total"`
}

// PlanGuardLimit is one guard.limits.* member: {value, origin}.
type PlanGuardLimit struct {
	Value  *int   `json:"value"`
	Origin string `json:"origin"`
}

// PlanGuardEvaluation is one guard.evaluation[] row (8 members).
type PlanGuardEvaluation struct {
	ID             string  `json:"id"`
	Limit          int     `json:"limit"`
	Value          *int    `json:"value"`
	Basis          string  `json:"basis"`
	Entry          *string `json:"entry"`
	Verdict        string  `json:"verdict"`
	UnknownKind    *string `json:"unknown_kind"`
	UnknownEntries *int    `json:"unknown_entries"`
}

// ============================================================================
// PlanApproval (§4.6, 6 members + nested 8-member `covers`)
// ============================================================================

// PlanApproval is RebasePlan.Approval.
type PlanApproval struct {
	Fingerprint *string            `json:"fingerprint"`
	Usable      bool               `json:"usable"`
	Scope       string             `json:"scope"`
	Supplied    bool               `json:"supplied"`
	Accepted    *bool              `json:"accepted"`
	Covers      PlanApprovalCovers `json:"covers"`
}

// PlanApprovalCovers is approval.covers (8 members).
type PlanApprovalCovers struct {
	Scope               string        `json:"scope"`
	WaivedEvaluationIDs []string      `json:"waived_evaluation_ids"` // never nil
	WaivedKinds         []RefusalKind `json:"waived_kinds"`          // never nil; subset of {limit-per-entry, limit-total}
	HardBlockersWaived  bool          `json:"hard_blockers_waived"`  // always false
	HasWork             bool          `json:"has_work"`
	RequiresLimits      bool          `json:"requires_limits"`
	EncodingSafe        bool          `json:"encoding_safe"`
	Note                string        `json:"note"`
}

// ============================================================================
// PlanBlocker, PlanWarning (§2.14 `{kind, entry, detail}`)
// ============================================================================

// PlanBlocker is one blockers[] row.
type PlanBlocker struct {
	Kind   RefusalKind `json:"kind"`
	Entry  *string     `json:"entry"` // null for document-level facts
	Detail string      `json:"detail"`
}

// PlanWarning is one warnings[] row. Kind is the closed eight-member domain:
// collateral-update-refs-config, base-execution-context-split,
// fetch-context-divergent, autostash-across-switch, checkout-dirty-present,
// push-dropped-restoring, restore-head-collateral-risk, untracked-present.
type PlanWarning struct {
	Kind   string  `json:"kind"`
	Entry  *string `json:"entry"` // null for document-level facts
	Detail string  `json:"detail"`
}

// ============================================================================
// PlanSummary (§2.16, 13 members) and PlanCollateralRef
// ============================================================================

// PlanCollateralRef is one collateral_refs[] tuple, used both by
// entries[].collateral_refs and by summary.collateral_refs.
type PlanCollateralRef struct {
	Repo       string `json:"repo"`
	Ref        string `json:"ref"`
	SHA        string `json:"sha"`
	StackOwned bool   `json:"stack_owned"`
}

// PlanSummary is RebasePlan.Summary.
type PlanSummary struct {
	Plannability                 string              `json:"plannability"` // rows | empty | unavailable
	Entries                      int                 `json:"entries"`
	DeferredDestinations         int                 `json:"deferred_destinations"`
	ConditionalStrategies        int                 `json:"conditional_strategies"`
	MaxEntryCandidates           *int                `json:"max_entry_candidates"`
	TotalCandidates              *int                `json:"total_candidates"`
	TotalCandidatesLowerBound    *int                `json:"total_candidates_lower_bound"`
	EntriesWithUnknownCandidates int                 `json:"entries_with_unknown_candidates"`
	CollateralExposedEntries     int                 `json:"collateral_exposed_entries"`
	EntriesWithUnknownCollateral int                 `json:"entries_with_unknown_collateral"`
	CollateralRefs               []PlanCollateralRef `json:"collateral_refs"` // nullable array — exception
	CollateralBound              *string             `json:"collateral_bound"`
	CollateralRefsUnowned        *int                `json:"collateral_refs_unowned"`
}

// ============================================================================
// PlanEntry (§2.15, entries[] row) and its nested Group B–E carrier structs
// ============================================================================

// PlanEntryBase is entries[].base (Group B).
type PlanEntryBase struct {
	Name        *string `json:"name"` // null iff configured base is empty
	Kind        string  `json:"kind"` // stack-entry | literal-ref | none
	Ref         *string `json:"ref"`
	DecisionSHA *string `json:"decision_sha"`
}

// PlanEntryDestination is entries[].destination (Group B).
type PlanEntryDestination struct {
	SHA         *string `json:"sha"`
	Deferred    bool    `json:"deferred"`
	DependsOn   *string `json:"depends_on"`
	SnapshotSHA *string `json:"snapshot_sha"`
}

// PlanEntryHead is entries[].head (Group B).
type PlanEntryHead struct {
	State *string `json:"state"` // present | missing | unresolvable
	SHA   *string `json:"sha"`
	Short *string `json:"short"`
}

// PlanEntryCutoff is entries[].cutoff (Group C).
type PlanEntryCutoff struct {
	RecordedSHA *string `json:"recorded_sha"`
	State       *string `json:"state"`      // absent | present | unresolvable
	Provenance  string  `json:"provenance"` // recorded-by-sync | none
	ResolvedSHA *string `json:"resolved_sha"`
	Usage       string  `json:"usage"` // used | not_used
	Write       string  `json:"write"` // per-entry | at-finalization | never
}

// PlanReplayCandidate is entries[].replay.first_candidate.
type PlanReplayCandidate struct {
	SHA     string `json:"sha"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
}

// PlanEntryReplay is entries[].replay (Group D, 14 members).
type PlanEntryReplay struct {
	UpstreamRef            *string              `json:"upstream_ref"`
	UpstreamSHA            *string              `json:"upstream_sha"`
	UpstreamProvenance     string               `json:"upstream_provenance"`
	Determinacy            string               `json:"determinacy"` // exact | snapshot | unknown | not-applicable
	Reason                 *string              `json:"reason"`      // null iff exact; 11-token precedence list
	Range                  *string              `json:"range"`
	CandidateCount         *int                 `json:"candidate_count"`
	FirstCandidate         *PlanReplayCandidate `json:"first_candidate"`
	Commits                []string             `json:"commits"` // nullable array — exception; capped at 50
	CommitsListed          *int                 `json:"commits_listed"`
	CommitsTruncated       *bool                `json:"commits_truncated"`
	CandidateDigest        *string              `json:"candidate_digest"`
	MayDropPatchEquivalent *bool                `json:"may_drop_patch_equivalent"`
	MayDropBecomesEmpty    bool                 `json:"may_drop_becomes_empty"` // always true
}

// PlanEntryMutation is entries[].mutation (Group E).
type PlanEntryMutation struct {
	WillSwitchHead    bool    `json:"will_switch_head"`
	WillLeaveHeadOn   *string `json:"will_leave_head_on"`
	HeadRestoredByRun bool    `json:"head_restored_by_run"`
}

// PlanEntryContext is entries[].context (Group E).
type PlanEntryContext struct {
	Dirty                       *bool   `json:"dirty"`
	Autostash                   *bool   `json:"autostash"`
	AutostashAppliesToThisArm   *bool   `json:"autostash_applies_to_this_arm"`
	AutostashReapplyMayConflict *bool   `json:"autostash_reapply_may_conflict"`
	RebaseInProgress            *bool   `json:"rebase_in_progress"`
	UntrackedPresent            *bool   `json:"untracked_present"`
	OverwriteRisk               *string `json:"overwrite_risk"` // null iff UntrackedPresent null
}

// PlanEntryPrunability is entries[].prunability (Group E; one in-range field).
type PlanEntryPrunability struct {
	ProbeContext string `json:"probe_context"` // constant "cwd"
}

// PlanAncestry is entries[].ancestry: {status, reason}, consumed verbatim from
// StackEdge.Status / StackEdge.Reason (internal/stack_ancestry.go). No new
// enum is declared here; the value domain is out of range for this file.
type PlanAncestry struct {
	Status *AncestryStatus     `json:"status"` // null when unevaluated
	Reason StackAncestryReason `json:"reason"`
}

// PlanEntry is one entries[] row (§2.15). Fields are declared in the exact
// Group A → B → C → D → E order the schema presents them in, which is also
// the JSON key order.
type PlanEntry struct {
	// Group A — identity and context
	Name             string      `json:"name"`
	GitBranch        string      `json:"git_branch"`
	Repo             string      `json:"repo"`
	Role             string      `json:"role"` // anchor | propagated
	Materialization  string      `json:"materialization"`
	BaseContext      PlanContext `json:"base_context"`
	ExecutionContext PlanContext `json:"execution_context"`

	// Group B — base, destination, head
	Base        PlanEntryBase        `json:"base"`
	Destination PlanEntryDestination `json:"destination"`
	Head        PlanEntryHead        `json:"head"`

	// Group C — cutoff, strategy, argv
	Cutoff            PlanEntryCutoff `json:"cutoff"`
	Strategy          string          `json:"strategy"`
	StrategyCondition *string         `json:"strategy_condition"`
	Argv              []string        `json:"argv"` // nullable array — exception
	ArgvAlternatives  *[2][]string    `json:"argv_alternatives"`
	EffectiveBackend  *string         `json:"effective_backend"` // merge | apply

	// Group D — replay
	Replay PlanEntryReplay `json:"replay"`

	// Group E — collateral, mutation, preconditions, ancestry
	CollateralRefs      []PlanCollateralRef  `json:"collateral_refs"` // nullable array — exception
	CollateralExposed   *bool                `json:"collateral_exposed"`
	CollateralMechanism *string              `json:"collateral_mechanism"` // argv | config | none
	Mutation            PlanEntryMutation    `json:"mutation"`
	Context             PlanEntryContext     `json:"context"`
	BranchCheckedOutAt  *string              `json:"branch_checked_out_at"`
	Prunability         PlanEntryPrunability `json:"prunability"`
	Blocking            bool                 `json:"blocking"`
	Ancestry            PlanAncestry         `json:"ancestry"`
	Notes               []string             `json:"notes"` // never nil; verbatim order, not sorted
}

// ============================================================================
// RefusalKind (§7.1, §7.5) — the closed rank-ordered refusal domain
// ============================================================================

// RefusalKind is the closed §7.1 refusal.kind / blockers[].kind enum, in rank
// order. len(RefusalKinds) == 30 (schema-asserted).
type RefusalKind string

const (
	RefusalPlanUnavailable             RefusalKind = "plan-unavailable"
	RefusalStackUnsortable             RefusalKind = "stack-unsortable"
	RefusalSelectionRefused            RefusalKind = "selection-refused"
	RefusalStateRefused                RefusalKind = "state-refused"
	RefusalPreflightRefused            RefusalKind = "preflight-refused"
	RefusalRestoreBlocked              RefusalKind = "restore-blocked"
	RefusalFetchContextIndeterminate   RefusalKind = "fetch-context-indeterminate"
	RefusalFetchSubmoduleReach         RefusalKind = "fetch-submodule-reach-indeterminate"
	RefusalFetchLocalBranchCheckedOut  RefusalKind = "fetch-local-branch-checked-out"
	RefusalInvalidGitConfig            RefusalKind = "invalid-git-config"
	RefusalIdentityNotUTF8             RefusalKind = "identity-not-utf8"
	RefusalRepoUnavailable             RefusalKind = "repo-unavailable"
	RefusalBaseExecutionStoreDivergent RefusalKind = "base-execution-store-divergent"
	RefusalHeadRefMissing              RefusalKind = "head-ref-missing"
	RefusalPrunableWorktree            RefusalKind = "prunable-worktree"
	RefusalBranchCheckedOutElsewhere   RefusalKind = "branch-checked-out-elsewhere"
	RefusalExternalRebaseInProgress    RefusalKind = "external-rebase-in-progress"
	RefusalContextDirty                RefusalKind = "context-dirty"
	RefusalBaseUnset                   RefusalKind = "base-unset"
	RefusalBaseRefMissing              RefusalKind = "base-ref-missing"
	RefusalCutoffUnresolvable          RefusalKind = "cutoff-unresolvable"
	RefusalProbeFailed                 RefusalKind = "probe-failed"
	RefusalSelectionHazard             RefusalKind = "selection-hazard"
	RefusalGuardLimitMismatch          RefusalKind = "guard-limit-mismatch"
	RefusalApprovalWithoutLimits       RefusalKind = "approval-without-limits"
	RefusalApprovalMismatch            RefusalKind = "approval-mismatch"
	RefusalRevalidationMismatch        RefusalKind = "revalidation-mismatch"
	RefusalIndeterminateEntry          RefusalKind = "indeterminate-entry"
	RefusalLimitPerEntry               RefusalKind = "limit-per-entry"
	RefusalLimitTotal                  RefusalKind = "limit-total"
)

// RefusalKinds is the complete §7.1 domain, in rank order. It has exactly
// thirty members.
var RefusalKinds = []RefusalKind{
	RefusalPlanUnavailable,
	RefusalStackUnsortable,
	RefusalSelectionRefused,
	RefusalStateRefused,
	RefusalPreflightRefused,
	RefusalRestoreBlocked,
	RefusalFetchContextIndeterminate,
	RefusalFetchSubmoduleReach,
	RefusalFetchLocalBranchCheckedOut,
	RefusalInvalidGitConfig,
	RefusalIdentityNotUTF8,
	RefusalRepoUnavailable,
	RefusalBaseExecutionStoreDivergent,
	RefusalHeadRefMissing,
	RefusalPrunableWorktree,
	RefusalBranchCheckedOutElsewhere,
	RefusalExternalRebaseInProgress,
	RefusalContextDirty,
	RefusalBaseUnset,
	RefusalBaseRefMissing,
	RefusalCutoffUnresolvable,
	RefusalProbeFailed,
	RefusalSelectionHazard,
	RefusalGuardLimitMismatch,
	RefusalApprovalWithoutLimits,
	RefusalApprovalMismatch,
	RefusalRevalidationMismatch,
	RefusalIndeterminateEntry,
	RefusalLimitPerEntry,
	RefusalLimitTotal,
}

// ============================================================================
// PushBlockedKind, RestoreBlockedKind (§7.5) — composed surface domains
// ============================================================================

// PushBlockedKind is the domain of push.blocked_by[]: RefusalKind ∪
// {push-dropped-restoring}. It is a distinct type from RefusalKind — no
// implicit conversion exists between them.
type PushBlockedKind string

// PushBlockedDroppedRestoring is the one PushBlockedKind member that is not
// also a RefusalKind: the shipped `restoring` arm dropped a push this run
// really intended.
const PushBlockedDroppedRestoring PushBlockedKind = "push-dropped-restoring"

// RestoreBlockedKind is the domain of restore.blocked_by[]: RefusalKind ∪ the
// three probed restore cells. push-dropped-restoring is explicitly NOT a
// member (a dropped push is a consequence of restoring, never a reason
// restore itself cannot run).
type RestoreBlockedKind string

const (
	RestoreBlockedTargetMissing RestoreBlockedKind = "restore-target-missing"
	RestoreBlockedTargetHeld    RestoreBlockedKind = "restore-target-held"
	RestoreBlockedHeadMoved     RestoreBlockedKind = "restore-head-moved"
)

// ============================================================================
// ControlledPathBlocker (§4.6, §7.5) — declared here, verbatim, and nowhere
// else. guard.execute_blocked_by[] uses []ControlledPathBlocker, never
// []RefusalKind, never []string.
// ============================================================================

// ControlledPathBlocker is the closed token domain of guard.execute_blocked_by[].
// It is deliberately NOT RefusalKind (§4.6): these five tokens name controlled-
// path preconditions the guard itself owns, they are non-waivable, and three of
// them also exist as RefusalKinds with the same spelling but a different job.
type ControlledPathBlocker string

const (
	ControlledFetchContextIndeterminate ControlledPathBlocker = "fetch-context-indeterminate"
	ControlledFetchLocalBranchHeld      ControlledPathBlocker = "fetch-local-branch-checked-out"
	ControlledFetchSubmoduleReach       ControlledPathBlocker = "fetch-submodule-reach-indeterminate"
	ControlledLiveOwnerConcurrency      ControlledPathBlocker = "live-owner-concurrency"
	ControlledOwnerArtefactUndecodable  ControlledPathBlocker = "owner-artefact-undecodable"
)

// ControlledPathBlockers is the complete domain, in the SORTED order the
// document publishes (§4.8). It has exactly five members.
var ControlledPathBlockers = []ControlledPathBlocker{
	ControlledFetchContextIndeterminate,
	ControlledFetchLocalBranchHeld,
	ControlledFetchSubmoduleReach,
	ControlledLiveOwnerConcurrency,
	ControlledOwnerArtefactUndecodable,
}
