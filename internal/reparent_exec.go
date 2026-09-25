package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ============================================================================
// runReparentGit — the ONLY process spawner inside the safe-reparent boundary
// (§3.6, §7.5b, §9.11)
//
// Every shipped Git helper fails at least one of this boundary's obligations:
//
//   - runGit / runGitRaw / runGitStreamLines (internal/rebase_plan_probe.go)
//     and mergeBaseIsAncestor (internal/rebase_plan_build.go) select their
//     directory with `git -C <dir>`, and a reparent-emitted argv may never
//     contain -C: the plan publishes a path-free argv template, and execution
//     must be able to prove that the argv it ran differs from that template in
//     at most the one placeholder operand.
//   - RunDirTo (internal/exec.go) does use cmd.Dir and does take writers, but
//     it sets cmd.Stdin = os.Stdin and returns only an error, so it can carry
//     neither the `update-ref --stdin` transaction payload nor the captured
//     bytes a refusal detail must quote.
//   - checkoutGitOutput (internal/checkout_sync.go) uses cmd.Dir but discards
//     stderr and cannot feed stdin.
//
// All of those keep every existing caller and are cited here as precedent
// only; none of them is ever called from reparent code.
//
// This file deliberately contains the runner and nothing else. Later slices
// grow it with the run/begin/continue/abort state machine, which must not
// exist before a spawner every probe-bearing reparent file can share.
// ============================================================================

// reparentGitResult is one reparent-owned Git child's complete observable
// outcome: both streams, captured into run-owned buffers, plus the exit code
// Git itself chose.
type reparentGitResult struct {
	// Stdout is every byte the child wrote to stdout. It is never this
	// process's stdout: the plan document is the only thing tws prints there.
	Stdout []byte

	// Stderr is every byte the child wrote to stderr, kept so a refusal can
	// quote Git's own sentence verbatim instead of paraphrasing it.
	Stderr []byte

	// ExitCode is the child's exit status, or -1 when git never ran at all
	// (binary missing, spawn failure) — the same trichotomy gitExitCode
	// already implements for the shipped probes.
	ExitCode int
}

// ReparentGitArgvHook is the documented argv-audit seam. It is nil in
// production and MUST NOT be reachable from the CLI. A test sets it to record
// every argv this boundary emits, so a forbidden-verb or `-C` audit is a
// cheap assertion over a log rather than a source scan. It is called before
// the child starts, with the directory the child will run in and the exact
// argument vector (the leading "git" is implicit and never included).
var ReparentGitArgvHook func(dir string, args []string)

// reparentGitFaultHook is a package-test seam for required-probe failures.
// handled=true returns the supplied result without spawning Git.
var reparentGitFaultHook func(dir string, args []string) (result reparentGitResult, err error, handled bool)

// runReparentGit runs `git <args...>` with exec.Cmd.Dir set to dir — never
// `git -C <dir>` — with stdin supplied by the caller (nil means the child
// gets no stdin at all, never os.Stdin, so no reparent child can ever block
// on a terminal), and with both output streams captured into buffers this run
// owns.
//
// The returned result is always populated, including on failure: a caller
// that needs to distinguish "exit 1" from "git could not run" reads
// result.ExitCode, and a caller that needs Git's own words reads
// result.Stderr. err is non-nil for every non-zero exit, so a caller that
// treats a specific exit code as a normal answer (see reparentIsAncestor)
// must inspect the code explicitly rather than ignoring the error.
func runReparentGit(dir string, stdin []byte, args ...string) (reparentGitResult, error) {
	if ReparentGitArgvHook != nil {
		recorded := make([]string, len(args))
		copy(recorded, args)
		ReparentGitArgvHook(dir, recorded)
	}
	if reparentGitFaultHook != nil {
		if result, err, handled := reparentGitFaultHook(dir, args); handled {
			return result, err
		}
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := reparentGitResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: 0,
	}
	if err != nil {
		result.ExitCode = gitExitCode(err)
	}
	return result, err
}

// ============================================================================
// The reparent execution machine (§9, §10, §11).
//
// Three entry points own the three routes — PlanReparent (read-only),
// BeginReparentRun + RunReparent (fresh execution) and ContinueReparent /
// AbortReparent (recovery). Every Git child goes through runReparentGit, so
// no argv this feature emits carries `-C`, no child inherits this process's
// streams, and no child can block on a terminal.
//
// Forbidden throughout, by construction and asserted by the argv audit:
// git reset --hard, git replay, rebase --update-refs / --autostash /
// --rebase-merges / --fork-point / --apply, git push, git worktree add in
// checkout mode, and every force/discard escape of the untracked gate.
// ============================================================================

// errReparentHoldersDeferred stops the forward machine at restoring-holders
// without failing the run. It never escapes RunReparent: the machine turns it
// into a successful return, because AC-096 makes a post-commit-point restore
// failure an ADVISORY. It exists only so the stage does not advance to
// cleanup, whose last step would delete the artifact a later --continue needs
// to re-attempt exactly those worktrees.
var errReparentHoldersDeferred = errors.New("reparent: holder restoration deferred")

// ReparentStepHook is the crash-injection seam, modelled on SyncStepHook. It
// is nil in production and MUST NOT be reachable from the CLI: a test sets it
// to fail at a named step and then asserts what a --continue or an --abort
// makes of the artifact that step left behind.
var ReparentStepHook func(stage ReparentStage, step string) error

func reparentStep(stage ReparentStage, step string) error {
	if ReparentStepHook == nil {
		return nil
	}
	return ReparentStepHook(stage, step)
}

// FetchReparentCheckoutRepo runs the checkout route's ONE policy-declared
// fetch by delegating to the shipped fetchCheckoutRepoTo, so reparent and
// sync perform byte-identical fetch prose and produce an identical
// PlanFetchOutcome shape. Re-implementing RunSilentDir(ctx.Root, "git",
// "fetch") here would be a second fetch ladder that could drift; delegating
// is what keeps the two routes provably the same.
//
// It is exported because the caller is the command route, which lives in
// package cli and cannot reach an unexported helper — the external arm's
// twin, fetchQuietTo, is likewise owned there. The route calls this exactly
// once, at §6.3 step 2a, on the plan route and on a fresh execution, and
// never on --continue or --abort; the measured outcome is then handed to
// PlanReparent through ReparentPlanInput.FetchOutcome, which is what keeps
// the builder pure and the fetch un-repeated by the post-lock re-snapshot.
func FetchReparentCheckoutRepo(w io.Writer, ctx PlanFetchContext) PlanFetchOutcome {
	return fetchCheckoutRepoTo(w, ctx)
}

// ReparentTargetRepository is the safely resolved repository a fresh plan or
// execution fetches and later plans against.
type ReparentTargetRepository struct {
	Root      string
	RepoToken string
	Source    string
}

func reparentMainRepoRootIn(path string) (string, error) {
	res, err := runReparentGit(path, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s", reparentGitMessage(res, err))
	}
	gitDir := strings.TrimSpace(string(res.Stdout))
	if gitDir == "" {
		return "", fmt.Errorf("git common directory is empty")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}
	return filepath.Dir(filepath.Clean(gitDir)), nil
}

// ResolveReparentWorkspace mirrors RequireWorkspace's repository-first then
// workspace-root/inference fallback while routing every Git probe through the
// reparent boundary's Cmd.Dir runner.
func ResolveReparentWorkspace(cwd string, cfg Config) (Workspace, error) {
	if repoRoot, err := reparentMainRepoRootIn(cwd); err == nil {
		return ResolveCurrentWorkspaceE(repoRoot, cfg)
	}
	metadataRoot := DetectWorkspaceRoot(cwd, cfg)
	if metadataRoot == "" || !metadataRootExists(metadataRoot) {
		return Workspace{}, fmt.Errorf("not inside a git repository or tws workspace")
	}
	repoRoot, err := inferExternalRepoRootWith(metadataRoot, cfg, reparentMainRepoRootIn)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{
		RepoRoot:     repoRoot,
		Mode:         ModeExternal,
		MetadataRoot: canonicalize(metadataRoot),
		StableID:     stableID(repoRoot),
		Caps:         capsFor(ModeExternal),
	}, nil
}

// ResolveReparentTargetRepository resolves the target entry before fetch so
// an external target in a secondary repository never fetches the workspace
// repository by accident. Planning receives the same Root and verifies every
// post-fetch fact there.
func ResolveReparentTargetRepository(mode WorkspaceMode, featurePath, workspaceRoot, target string) (ReparentTargetRepository, error) {
	data, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		return ReparentTargetRepository{}, reparentRefusal(ReparentRefusalPlanUnavailable,
			fmt.Sprintf("stack.yaml is unreadable: %v", err))
	}
	var stack Stack
	if err := yaml.Unmarshal(data, &stack); err != nil {
		return ReparentTargetRepository{}, reparentRefusal(ReparentRefusalPlanUnavailable,
			fmt.Sprintf("stack.yaml does not decode: %v", err))
	}
	if blockers := reparentIdentityBlockers(stack); len(blockers) > 0 {
		first := blockers[0]
		return ReparentTargetRepository{}, reparentRefusal(first.Kind, first.Detail)
	}
	entry, ok := reparentLookupEntry(stack, target)
	if !ok {
		return ReparentTargetRepository{}, reparentRefusal(ReparentRefusalTargetUnknown,
			fmt.Sprintf("%q is not an entry in this feature's stack.yaml", target))
	}
	if mode == ModeCheckout {
		for _, row := range stack.Branches {
			if strings.TrimSpace(row.Repo) != "" {
				return ReparentTargetRepository{}, reparentEntryRefusal(ReparentRefusalCrossRepoClosure, row.Name,
					fmt.Sprintf("checkout mode does not support repo %q; execution is fixed to workspace repository %s", row.Repo, workspaceRoot))
			}
		}
	}
	root := reparentRowRepoDir(workspaceRoot, entry.Repo)
	if reparentCommonDir(root) == "" {
		return ReparentTargetRepository{}, reparentEntryRefusal(ReparentRefusalRepoUnavailable, target,
			fmt.Sprintf("%q resolves to %s, which is not a repository this run can read", target, root))
	}
	source := "workspace-repo-root"
	if strings.TrimSpace(entry.Repo) != "" {
		source = "entry-repo"
	}
	return ReparentTargetRepository{Root: root, RepoToken: entry.Repo, Source: source}, nil
}

// ============================================================================
// PlanReparent — §6.3 steps 3-10, read-only
// ============================================================================

// ReparentPlanInput is everything a route hands the planner. Facts that can
// only be measured outside this package — the external fetch outcome and the
// session-liveness verdicts — arrive as inputs, so this file never imports
// package cli and the builder stays pure.
type ReparentPlanInput struct {
	Loc      ReparentLocation
	RepoRoot string
	// TargetRepoRoot is resolved before a policy-declared fetch. When set,
	// planning must use this same repository rather than deriving a second
	// post-fetch context from the workspace root.
	TargetRepoRoot string

	Route      string
	Invocation string

	Target      string
	OntoToken   string
	OntoKind    string
	CutoffToken string

	Guard CheckoutPlanGuard

	FetchPolicy         SyncFetchPolicy
	FetchDefaultApplied bool
	FetchOutcome        PlanFetchOutcome

	Validation PlanValidationIdentity

	Workspace PlanWorkspace

	// LiveSessions names affected entries with a live or unverifiable tws
	// session, as measured BEFORE the lock. It is only the seed value:
	// whenever SessionProbe is set, every measurement — including the
	// post-lock re-snapshot — re-runs the probe instead of reusing it.
	LiveSessions []string

	// SessionProbe re-measures session liveness at every seam. The probes
	// (GuardDirectSessionsFor, CheckoutSessionPreconditions) need workspace
	// identity the route already holds, so the route supplies the closure
	// rather than this package importing cli. Without it the post-lock
	// re-snapshot of §8.5 step 3 would re-publish a pre-lock verdict and a
	// session started inside the concurrency window would go unnoticed.
	SessionProbe func(affected []string) ([]string, error)

	// SessionIntentCleanup removes only provably dead launch intents. It is
	// never called by a plan route; fresh execution calls it after acquiring
	// the mutation lock, and recovery calls it after reclaiming that lock.
	SessionIntentCleanup func(affected []string) error

	// ScratchPath excludes a recovery run's own computation worktree from
	// every holder projection (§4.2 half B).
	ScratchPath string

	// ExclusionOwner is nil on an ordinary pre-lock plan. Immediately after
	// lock acquisition it carries the exact newly written lock bytes, and on
	// recovery it additionally carries the authoritative run ownership.
	ExclusionOwner *ReparentExclusionOwner

	ApprovedFingerprint string
	Resumable           bool

	Writers ReparentWriters
}

// PlanReparent measures the read-only facts of §6.3 steps 3-10 and hands them
// to the pure builder. It issues no mutating Git verb and takes no lock.
func PlanReparent(in ReparentPlanInput) (ReparentPlan, ReparentRequest, error) {
	stateLoad := LoadReparentState(in.Loc)
	featurePath := in.Loc.FeaturePath
	stackBytes, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		return ReparentPlan{}, ReparentRequest{}, reparentRefusal(ReparentRefusalPlanUnavailable,
			fmt.Sprintf("stack.yaml is unreadable: %v", err))
	}
	var stack Stack
	if err := yaml.Unmarshal(stackBytes, &stack); err != nil {
		return ReparentPlan{}, ReparentRequest{}, reparentRefusal(ReparentRefusalPlanUnavailable,
			fmt.Sprintf("stack.yaml does not decode: %v", err))
	}
	req := newReparentRequest(in, stack, stackBytes)
	if blocker := reparentAuthoritativeStateBlocker(stateLoad); blocker != nil {
		req.AuthoritativeStatePresent = true
		req.Blockers = append(req.Blockers, *blocker)
	}

	if len(reparentIdentityBlockers(stack)) > 0 {
		plan, buildErr := BuildReparentPlan(req)
		return finalizeUnavailableReparentPlan(plan, in, stack), req, buildErr
	}

	target, found := reparentLookupEntry(stack, in.Target)
	if !found {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind:   ReparentRefusalTargetUnknown,
			Detail: fmt.Sprintf("%q is not an entry in this feature's stack.yaml", in.Target),
		})
		plan, buildErr := BuildReparentPlan(req)
		return finalizeUnavailableReparentPlan(plan, in, stack), req, buildErr
	}
	return planReparentResolved(in, req, stack, target)
}

func newReparentRequest(in ReparentPlanInput, stack Stack, stackBytes []byte) ReparentRequest {
	sum := sha256.Sum256(stackBytes)
	req := ReparentRequest{
		Route:               in.Route,
		Invocation:          in.Invocation,
		Workspace:           in.Workspace,
		Mode:                in.Loc.Mode,
		Feature:             in.Loc.Feature,
		Stack:               stack,
		StackBytes:          append([]byte{}, stackBytes...),
		StackSHA256:         hex.EncodeToString(sum[:]),
		TargetName:          in.Target,
		OntoKindRequested:   reparentOntoKindOrAuto(in.OntoKind),
		RequestedToken:      in.OntoToken,
		CutoffSupplied:      in.CutoffToken != "",
		CutoffToken:         in.CutoffToken,
		FetchPolicy:         in.FetchPolicy,
		FetchDefaultApplied: in.FetchDefaultApplied,
		FetchOutcome:        in.FetchOutcome,
		Validation:          in.Validation,
		Guard:               in.Guard,
		Limits:              PlanGuardLimits{PerEntry: reparentLimit(in.Guard.MaxPerEntry), Total: reparentLimit(in.Guard.MaxTotal)},
		LiveSessions:        in.LiveSessions,
		ApprovedFingerprint: in.ApprovedFingerprint,
		Resumable:           in.Resumable,
		RemoteRecordPath:    ReparentRemoteRecordPath(in.Loc),
		Snapshot:            PlanStateSnapshot{TakenBeforeAcquisition: true, SelfPID: os.Getpid()},
	}
	req.Fetch = reparentFetchPlan(in.Route, in.FetchPolicy, in.FetchDefaultApplied, PlanFetchContext{})
	return req
}

// BuildUnavailableReparentPlan converts a plan-route measurement failure into
// a complete document. It performs only stack file IO/decoding and no Git
// probe, fetch, lock, or mutation.
func BuildUnavailableReparentPlan(in ReparentPlanInput, cause error) (ReparentPlan, error) {
	var stack Stack
	var stackBytes []byte
	if data, err := os.ReadFile(StackPath(in.Loc.FeaturePath)); err == nil {
		stackBytes = data
		_ = yaml.Unmarshal(data, &stack)
	}
	req := newReparentRequest(in, stack, stackBytes)
	if blocker := reparentAuthoritativeStateBlocker(LoadReparentState(in.Loc)); blocker != nil {
		req.AuthoritativeStatePresent = true
		req.Blockers = append(req.Blockers, *blocker)
	}
	var refusal *ReparentRefusalError
	if errors.As(cause, &refusal) {
		req.Blockers = append(req.Blockers, refusal.Blocker())
	} else {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind: ReparentRefusalPlanUnavailable, Detail: sanitizeReparentLine(cause.Error()),
		})
	}
	plan, err := BuildReparentPlan(req)
	if err != nil {
		return ReparentPlan{}, err
	}
	return finalizeUnavailableReparentPlan(plan, in, stack), nil
}

func reparentAuthoritativeStateBlocker(load ReparentStateLoad) *ReparentPlanBlocker {
	if load.Kind == ReparentStateAbsent {
		return nil
	}
	if load.Kind == ReparentStateOK && load.State != nil {
		return &ReparentPlanBlocker{
			Kind: ReparentRefusalStatePresent,
			Detail: fmt.Sprintf(
				"a reparent run (%s, stage %s) is already recorded for %q; finish it with: tws stack reparent %s --continue (or --abort)",
				load.State.RunID, load.State.Stage, load.State.Feature, load.State.Feature,
			),
		}
	}
	if refusal := load.Refusal(); refusal != nil {
		var typed *ReparentRefusalError
		if errors.As(refusal, &typed) {
			return &ReparentPlanBlocker{Kind: typed.Kind, Entry: typed.Entry, Detail: typed.Detail}
		}
	}
	return &ReparentPlanBlocker{Kind: ReparentRefusalStateCorrupt, Detail: load.Detail}
}

func finalizeUnavailableReparentPlan(plan ReparentPlan, in ReparentPlanInput, stack Stack) ReparentPlan {
	if in.FetchPolicy == SyncFetchEnabled && plan.Fetch.SuppressionCause == nil {
		cause := "not-refreshed-no-plan-subject"
		plan.Fetch.SuppressionCause = &cause
		plan.Freshness = cause
	}
	plan.Summary.Plannability = ReparentPlannabilityUnavailable
	plan.Summary.HasWork = false
	plan.Runnable = false
	plan.Approval = ReparentPlanApproval{
		Scope: ReparentApprovalScopeNone,
		Covers: ReparentPlanApprovalCovers{
			Scope: ReparentApprovalScopeNone, WaivedEvaluationIDs: []string{},
			WaivedKinds: []RefusalKind{}, RequiresLimits: in.Route != ReparentRouteContinue,
			EncodingSafe: len(plan.EncodingIssues) == 0,
		},
	}
	plan.Target.Role = ReparentRoleTarget
	plan.Target.Name = in.Target
	plan.Target.Order = 0
	plan.Target.Notes = ensureSlice(plan.Target.Notes)
	if entry, ok := reparentLookupEntry(stack, in.Target); ok {
		plan.Target.GitBranch = entry.GitBranch()
		plan.Target.Repo = entry.Repo
	}
	return plan
}

// PlanReparentContinue projects the authoritative run read-only. It never
// fetches, reclaims a lock, updates owner_pid, repairs compatibility files or
// calls the fresh planner with an empty target.
func PlanReparentContinue(in ReparentPlanInput) (ReparentPlan, error) {
	load := LoadReparentState(in.Loc)
	if refusal := load.Refusal(); refusal != nil {
		return ReparentPlan{}, refusal
	}
	if load.Kind == ReparentStateAbsent || load.State == nil {
		return ReparentPlan{}, reparentRefusal(ReparentRefusalStatePresent,
			fmt.Sprintf("no reparent run is recorded for %q; there is nothing to continue", in.Loc.Feature))
	}
	st := load.State
	if err := validateReparentStateInvocation(in, st); err != nil {
		return ReparentPlan{}, err
	}
	effectiveStage := st.Stage
	if st.Stage == ReparentStageFailed {
		effectiveStage = st.ResumeStage
	}
	repoRoot := st.RepoRoot
	if repoRoot == "" {
		repoRoot = in.RepoRoot
	}
	raw, err := base64.StdEncoding.DecodeString(st.StackBeforeBase64)
	if err != nil {
		return ReparentPlan{}, reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the persisted pre-image stack does not decode: %v", err))
	}
	var stack Stack
	if err := yaml.Unmarshal(raw, &stack); err != nil {
		return ReparentPlan{}, reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the persisted pre-image stack does not decode as stack.yaml: %v", err))
	}
	req := ReparentRequest{
		Route:               ReparentRouteContinue,
		Invocation:          ReparentInvocationPlanOnly,
		Workspace:           in.Workspace,
		Mode:                in.Loc.Mode,
		Feature:             st.Feature,
		Stack:               stack,
		StackBytes:          raw,
		StackSHA256:         st.StackSHA256Before,
		TargetName:          st.TargetName,
		OntoKindRequested:   reparentPersistedOntoKind(st.NewParentKind),
		RequestedToken:      st.NewParentRequestedToken,
		CutoffSupplied:      st.CutoffSuppliedToken != "",
		CutoffToken:         st.CutoffSuppliedToken,
		TargetRepoRoot:      repoRoot,
		FetchPolicy:         SyncFetchPolicy(st.FetchPolicy),
		Validation:          PlanValidationIdentity{Applies: st.ValidationCommandRaw != "", Command: st.ValidationCommandRaw, Source: st.ValidationSource, Digest: st.ValidationCommandDigest},
		CapsProbed:          false,
		RefBackend:          st.RefBackend,
		OIDWidth:            st.OIDWidth,
		ApprovedFingerprint: st.ApprovedFingerprint,
		Resumable:           st.Stage != ReparentStageAborting && st.Stage != ReparentStageAborted,
		RemoteRecordPath:    ReparentRemoteRecordPath(in.Loc),
		Snapshot:            PlanStateSnapshot{TakenBeforeAcquisition: false, SelfPID: os.Getpid()},
	}
	identityRoot := in.RepoRoot
	if identityRoot == "" {
		identityRoot = repoRoot
	}
	req.RepoIdentities = reparentRepoIdentityMap(identityRoot, stack)
	req.Guard = CheckoutPlanGuard{MaxPerEntry: st.MaxReplayPerEntry, MaxTotal: st.MaxReplayTotal}
	req.Limits = PlanGuardLimits{
		PerEntry: PlanGuardLimit{Value: st.MaxReplayPerEntry, Origin: "persisted-state"},
		Total:    PlanGuardLimit{Value: st.MaxReplayTotal, Origin: "persisted-state"},
	}
	req.OldParent = ReparentParent{
		RequestedToken: reparentOptionalString(st.OldParentRequestedToken, st.OldParentRequestedToken != ""),
		StoredToken:    reparentOptionalString(st.OldParentStoredToken, st.OldParentStoredToken != ""),
		Kind:           st.OldParentKind,
		Ref:            reparentOptionalString(st.OldParentRef, st.OldParentRef != ""),
		SHA:            reparentOptionalString(st.OldParentSHA, st.OldParentSHA != ""),
		Resolution:     reparentResolutionFromPersistedParent(st.OldParentKind, st.OldParentRef),
		Candidates:     []string{},
	}
	req.Destination = ReparentDestination{
		RequestedToken: st.NewParentRequestedToken,
		StoredToken:    st.NewParentStoredToken,
		Kind:           st.NewParentKind,
		Ref:            reparentOptionalString(st.NewParentRef, st.NewParentRef != ""),
		SHA:            st.NewParentSHA,
		Resolution:     reparentResolutionFromPersistedParent(st.NewParentKind, st.NewParentRef),
	}
	req.DestinationResolved = st.NewParentSHA != ""
	for _, row := range st.RowsInOrder() {
		count := row.CandidateCount
		digest := row.CandidateDigest
		probe := ReparentRowProbe{
			Name: row.Name, GitBranch: row.GitBranch, Repo: row.Repo,
			Role: row.Role, Order: row.Order, HeadSHA: row.PreimageSHA, HeadFound: row.PreimageSHA != "",
			Materialization: "materialized", CommonDir: st.RepoCommonDir,
			Cutoff: ReparentPlanCutoff{
				RecordedSHA:   reparentOptionalString(row.CutoffSHA, row.CutoffSHA != ""),
				RecordedState: ReparentCutoffRecordPresent,
				ResolvedSHA:   reparentOptionalString(row.CutoffSHA, row.CutoffSHA != ""),
				Provenance:    row.CutoffProvenance,
			},
			Replay: PlanEntryReplay{
				Determinacy: "exact", CandidateCount: &count,
				CandidateDigest: reparentOptionalString(digest, digest != ""),
				Commits:         []string{},
			},
			MergeCommitsInRange: reparentOptionalInt(0, true),
			Notes:               []string{"persisted row stage: " + string(row.Stage)},
		}
		req.Rows = append(req.Rows, probe)
	}
	for _, h := range st.ApprovedHolders {
		req.Holders = append(req.Holders, ReparentHolderProbe{
			GitBranch: h.GitBranch, HolderKind: h.HolderKind, HolderPath: h.Path,
			PreimageSHA: h.HeadSHA, Excluded: h.Excluded,
		})
	}
	if reparentStageNeedsHolderVerification(effectiveStage) && !reparentStateHolderRestorationComplete(st) {
		holderRun := &ReparentRun{Loc: in.Loc, RepoRoot: repoRoot, State: st}
		if err := holderRun.verifyPersistedHolderSnapshot(); err != nil {
			var refusal *ReparentRefusalError
			if errors.As(err, &refusal) {
				req.Blockers = append(req.Blockers, refusal.Blocker())
			} else {
				req.Blockers = append(req.Blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalProbeFailed, Detail: err.Error(),
				})
			}
		}
	}
	req.OriginalBranch, req.OriginalHead, req.OriginalDetached = st.OriginalBranch, st.OriginalHead, st.OriginalDetached
	owner := &ReparentExclusionOwner{
		RunID: st.RunID, OwnerToken: st.OwnerToken, OwnerPID: st.OwnerPID,
	}
	req.StateFiles, req.Exclusion = measureReparentStateFiles(in.Loc, owner)
	affected := make([]string, 0, len(st.Rows))
	for _, row := range st.RowsInOrder() {
		affected = append(affected, row.Name)
	}
	if in.SessionProbe != nil {
		live, probeErr := in.SessionProbe(affected)
		if probeErr != nil {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalSessionLive, Detail: fmt.Sprintf("session liveness could not be verified: %v", probeErr),
			})
		} else {
			for _, name := range live {
				entry := name
				req.Blockers = append(req.Blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalSessionLive, Entry: &entry,
					Detail: fmt.Sprintf("%q has a live or unverifiable tws session; close it before resuming", name),
				})
			}
		}
	}
	if st.OwnerPID != os.Getpid() && isProcessAlive(st.OwnerPID) {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind:   ReparentRefusalStatePresent,
			Detail: fmt.Sprintf("reparent run %s is still owned by live process %d", st.RunID, st.OwnerPID),
		})
	}
	if detail := ReparentForeignSyncStateOwned(in.Loc, owner); detail != "" {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{Kind: ReparentRefusalSyncStatePresent, Detail: detail})
	}
	// A missing owned compatibility envelope is automatic recovery work, not
	// an admission blocker. Foreign compatibility state was classified above.
	liveStackHash := ""
	if live, readErr := os.ReadFile(StackPath(in.Loc.FeaturePath)); readErr != nil {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind:   ReparentRefusalMetadataDrift,
			Detail: fmt.Sprintf("stack.yaml could not be read: %v", readErr),
		})
	} else {
		liveStackHash = hex.EncodeToString(sliceSHA256(live))
		known := liveStackHash == st.StackSHA256Before ||
			(st.StackSHA256AfterExpected != "" && liveStackHash == st.StackSHA256AfterExpected) ||
			(st.StackSHA256After != "" && liveStackHash == st.StackSHA256After)
		if !known {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind:   ReparentRefusalMetadataDrift,
				Detail: fmt.Sprintf("stack.yaml hashes to %s, which is not a state this run recorded", liveStackHash),
			})
		}
	}
	assessment, assessErr := assessReparentLiveRefs(repoRoot, st, liveStackHash)
	var resumeGuardRefusal *PlanGuardRefusalError
	var resumeGuardEntry *string
	if assessErr != nil {
		var refusal *ReparentRefusalError
		if errors.As(assessErr, &refusal) {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: refusal.Kind, Entry: refusal.Entry, Detail: refusal.Detail,
			})
		} else {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Detail: assessErr.Error(),
			})
		}
	} else {
		classByName := make(map[string]ReparentRefClassification, len(assessment.Classes))
		for _, class := range assessment.Classes {
			classByName[class.Name] = class
			if class.Class == ReparentRefForeign && !assessment.CommitPointProven {
				if row := st.Row(class.Name); row != nil && row.PlannedNewSHA == "" {
					continue
				}
				name := class.Name
				req.Blockers = append(req.Blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalRefForeignValue, Entry: &name, Detail: reparentForeignRefDetail(class),
				})
			}
		}
		for i := range req.Rows {
			class, ok := classByName[req.Rows[i].Name]
			if !ok {
				continue
			}
			if !st.CommitPointReached {
				req.Rows[i].HeadSHA = class.Live
				req.Rows[i].HeadFound = class.LiveFound
			}
			req.Rows[i].Notes = append(req.Rows[i].Notes, "live ref classification: "+string(class.Class))
			if assessment.Advanced[class.Name] {
				req.Rows[i].Notes = append(req.Rows[i].Notes, "live ref is a descendant of planned_new_sha")
			}
		}
	}
	activeConflict := false
	if effectiveStage == ReparentStageComputing || effectiveStage == ReparentStageConflictPaused {
		run := &ReparentRun{Loc: in.Loc, RepoRoot: repoRoot, State: st}
		active, probeErr := reparentRebaseInProgress(run.contextDir())
		if probeErr != nil {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Detail: probeErr.Error(),
			})
		} else if active {
			activeConflict = true
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind:   ReparentRefusalConflictUnresolved,
				Detail: fmt.Sprintf("a rebase is still in progress in %s; finish it before resuming", run.contextDir()),
			})
		} else if effectiveStage == ReparentStageConflictPaused {
			for _, candidate := range st.RowsInOrder() {
				row := st.Row(candidate.Name)
				if row.Stage != ReparentRowPending {
					continue
				}
				if _, conflictErr := run.assessConflictResolution(row); conflictErr != nil {
					var refusal *ReparentRefusalError
					if errors.As(conflictErr, &refusal) {
						req.Blockers = append(req.Blockers, ReparentPlanBlocker{
							Kind: refusal.Kind, Entry: refusal.Entry, Detail: refusal.Detail,
						})
					} else {
						req.Blockers = append(req.Blockers, ReparentPlanBlocker{
							Kind: ReparentRefusalProbeFailed, Detail: conflictErr.Error(),
						})
					}
				}
				break
			}
		}
	}
	jitStage := effectiveStage == ReparentStageInitializing || effectiveStage == ReparentStagePreflight ||
		effectiveStage == ReparentStagePinningPreimages || effectiveStage == ReparentStageComputing
	if jitStage && !activeConflict {
		run := &ReparentRun{Loc: in.Loc, RepoRoot: repoRoot, State: st}
		for _, candidate := range st.RowsInOrder() {
			row := st.Row(candidate.Name)
			if row.Stage != ReparentRowPending {
				continue
			}
			if _, revalidateErr := run.assessRowRevalidation(row, true); revalidateErr != nil {
				var guardErr *PlanGuardRefusalError
				var refusal *ReparentRefusalError
				switch {
				case errors.As(revalidateErr, &guardErr):
					resumeGuardRefusal = guardErr
					entry := row.Name
					resumeGuardEntry = &entry
				case errors.As(revalidateErr, &refusal):
					req.Blockers = append(req.Blockers, ReparentPlanBlocker{
						Kind: refusal.Kind, Entry: refusal.Entry, Detail: refusal.Detail,
					})
				default:
					req.Blockers = append(req.Blockers, ReparentPlanBlocker{
						Kind: ReparentRefusalProbeFailed, Detail: revalidateErr.Error(),
					})
				}
				break
			}
		}
	}
	if pending, loadErr := projectReparentRemotePending(in.Loc, repoRoot, &stack, st); loadErr != nil {
		var refusal *ReparentRefusalError
		if errors.As(loadErr, &refusal) {
			req.Blockers = append(req.Blockers, refusal.Blocker())
		} else {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalStateCorrupt, Detail: loadErr.Error(),
			})
		}
	} else {
		req.RemotePending = pending
	}
	req.Fetch = reparentFetchPlan(ReparentRouteContinue, req.FetchPolicy, false,
		reparentFetchContext(repoRoot, "", st.RepoCommonDir, "persisted-state"))

	plan, err := BuildReparentPlan(req)
	if err != nil {
		return ReparentPlan{}, err
	}
	plan.Guard.WouldRefuse = false
	plan.Guard.WouldRefuseWithoutApproval = false
	if resumeGuardRefusal != nil {
		kind := resumeGuardRefusal.Kind
		plan.Guard.WouldRefuse = true
		plan.Guard.WouldRefuseWithoutApproval = true
		plan.Guard.Evaluation = append(plan.Guard.Evaluation, PlanGuardEvaluation{
			ID: "resume-jit-revalidation", Basis: resumeGuardRefusal.Detail,
			Entry: resumeGuardEntry, Verdict: "refuse", UnknownKind: &kind,
		})
	}
	plan.Summary.HasWork = req.Resumable
	plan.Runnable = req.Resumable && len(plan.Blockers) == 0 && resumeGuardRefusal == nil
	plan.Approval = reparentApproval(req, plan, st.WaivedKinds, plan.Guard.Evaluation)
	plan.Approval.Covers.WaivedEvaluationIDs = append([]string{}, st.WaivedEvaluationIDs...)
	plan.MetadataDelta = reparentPersistedMetadataDelta(st)
	plan.Remote = reparentPersistedRemotePlan(in.Loc, st, req.RemotePending)
	return plan, nil
}

func projectReparentRemotePending(loc ReparentLocation, repoRoot string, stack *Stack, st *ReparentState) ([]string, error) {
	var data []byte
	var present bool
	if st.RemoteClearPending {
		run := &ReparentRun{Loc: loc, RepoRoot: repoRoot, State: st}
		source, sourcePresent, target, targetPresent, err := run.remoteClearImages()
		if err != nil {
			return nil, err
		}
		live, livePresent, err := readReparentRemoteRecordRaw(ReparentRemoteRecordPath(loc))
		if err != nil {
			return nil, err
		}
		if !reparentRemoteImageMatches(live, livePresent, source, sourcePresent) &&
			!reparentRemoteImageMatches(live, livePresent, target, targetPresent) {
			return nil, reparentRefusal(ReparentRefusalStateForeign,
				"the remote follow-up record matches neither side of the journaled clear transition")
		}
		data, present = target, targetPresent
	} else {
		transition, err := BuildReparentRemoteClearTransition(loc, repoRoot, stack)
		if err != nil {
			return nil, err
		}
		data, present = transition.Target, transition.TargetPresent
	}
	if !present {
		return nil, nil
	}
	rec, err := decodeReparentRemoteRecordBytes(loc, data)
	if err != nil {
		return nil, err
	}
	return rec.PendingEntries(), nil
}

type reparentLiveRefAssessment struct {
	Classes           []ReparentRefClassification
	Advanced          map[string]bool
	CommitPointProven bool
}

// assessReparentLiveRefs is the read-only ref half shared by continuation
// plans and recovery execution. Before the commit point it enforces direct
// branch refs, classifies every row, and recognizes the same post-image plus
// distinct descendant-of-planned evidence execution uses to infer a committed
// run; an exact pre-image is never such evidence.
func assessReparentLiveRefs(repoRoot string, st *ReparentState, liveStackHash string) (reparentLiveRefAssessment, error) {
	out := reparentLiveRefAssessment{Advanced: map[string]bool{}}
	if st.CommitPointReached {
		out.CommitPointProven = true
		// Past the monotonic commit point execution deliberately accepts later
		// movement. Classify opportunistically for plan facts, but never turn a
		// read failure or symbolic ref into a blocker execution would not raise.
		if classes, err := ClassifyReparentRefs(repoRoot, st.RowsInOrder()); err == nil {
			out.Classes = classes
			for _, class := range classes {
				if class.Class == ReparentRefForeign && class.Planned != "" && class.LiveFound {
					if advanced, ancErr := reparentIsAncestor(repoRoot, class.Planned, class.Live); ancErr == nil && advanced {
						out.Advanced[class.Name] = true
					}
				}
			}
		}
		return out, nil
	}
	rows := st.RowsInOrder()
	if err := reparentRequireAllDirectBranchRefs(repoRoot, rows); err != nil {
		return out, err
	}
	classes, err := ClassifyReparentRefs(repoRoot, rows)
	if err != nil {
		return out, err
	}
	out.Classes = classes
	if st.StackSHA256AfterExpected == "" || liveStackHash != st.StackSHA256AfterExpected {
		return out, nil
	}
	for _, class := range classes {
		switch class.Class {
		case ReparentRefPlannedTip, ReparentRefNoop:
			continue
		case ReparentRefPreimage:
			// A planned tip may be an ancestor of the pre-image simply because
			// the forward CAS never ran. Ancestry cannot turn that exact
			// pre-image classification into post-commit evidence.
			return out, nil
		case ReparentRefForeign:
			// Only a distinct foreign value may be later operator work on top
			// of a tip this run actually landed.
		default:
			return out, nil
		}
		if class.Planned == "" || !class.LiveFound {
			return out, nil
		}
		advanced, err := reparentIsAncestor(repoRoot, class.Planned, class.Live)
		if err != nil {
			return out, err
		}
		if !advanced {
			return out, nil
		}
		out.Advanced[class.Name] = true
	}
	allNoop := len(st.Rows) > 0
	for _, row := range st.Rows {
		if row.PlannedNewSHA == "" || row.PlannedNewSHA != row.PreimageSHA {
			allNoop = false
			break
		}
	}
	if allNoop && !st.CASTransactionSucceeded {
		return out, nil
	}
	out.CommitPointProven = true
	return out, nil
}

func reparentPersistedOntoKind(kind string) string {
	if kind == ReparentParentKindStackEntry {
		return "entry"
	}
	return "ref"
}

func reparentResolutionFromPersistedParent(kind, ref string) string {
	if kind == ReparentParentKindStackEntry {
		return ReparentResolutionEntry
	}
	switch {
	case ref == "":
		return ReparentResolutionRawOID
	case strings.HasPrefix(ref, "refs/heads/"):
		return ReparentResolutionRefHead
	case strings.HasPrefix(ref, "refs/tags/"):
		return ReparentResolutionRefTag
	case strings.HasPrefix(ref, "refs/remotes/"):
		return ReparentResolutionRefRemote
	case strings.HasPrefix(ref, "refs/"):
		return ReparentResolutionRefOther
	default:
		return ReparentResolutionUnresolved
	}
}

func reparentPersistedMetadataDelta(st *ReparentState) ReparentPlanMetadataDelta {
	out := ReparentPlanMetadataDelta{
		Entries: []ReparentPlanMetadataEntry{}, StackSHA256Before: st.StackSHA256Before,
		Writer: ReparentMetadataWriter, WritePoint: ReparentMetadataWritePoint,
		PostImageKnown: st.StackSHA256AfterExpected != "", EntriesOutsideClosureChanged: false,
	}
	if st.StackSHA256AfterExpected != "" {
		hash := st.StackSHA256AfterExpected
		out.StackSHA256AfterExpected = &hash
	}
	for _, row := range st.RowsInOrder() {
		after := row.LastBaseSHAAfter
		source := ReparentLastBaseSourcePinned
		if row.Role == ReparentRoleDescendant {
			source = ReparentLastBaseSourceReplay
			if parent := st.Row(row.DestinationParent); parent != nil {
				after = parent.PlannedNewSHA
			}
		}
		out.Entries = append(out.Entries, ReparentPlanMetadataEntry{
			Name: row.Name, BaseBefore: row.BaseBefore, BaseAfter: row.BaseAfter,
			LastBaseSHABefore:      reparentOptionalString(row.LastBaseSHABefore, row.LastBaseSHABefore != ""),
			LastBaseSHAAfter:       reparentOptionalString(after, after != ""),
			LastBaseSHAAfterSource: source, Changed: row.BaseBefore != row.BaseAfter || row.LastBaseSHABefore != after,
		})
	}
	return out
}

func reparentPersistedRemotePlan(loc ReparentLocation, st *ReparentState, pending []string) ReparentPlanRemote {
	out := ReparentPlanRemote{Remote: ReparentRemoteName, Rows: []ReparentPlanRemoteRow{}}
	rec, _ := LoadReparentRemoteRecord(loc)
	pendingSet := make(map[string]bool, len(pending))
	for _, name := range pending {
		pendingSet[name] = true
	}
	for _, row := range st.RowsInOrder() {
		remote := ReparentPlanRemoteRow{
			Name: row.Name, GitBranch: row.GitBranch, Remote: ReparentRemoteName,
			Divergence: "unknown", ProviderHint: "unknown",
		}
		if rec != nil {
			if entry, ok := rec.Entry(row.Name); ok {
				remote.RemoteRef = reparentOptionalString(entry.RemoteRef, entry.RemoteRef != "")
				remote.RemoteSHA = reparentOptionalString(entry.RemoteSHAAtWrite, entry.RemoteSHAAtWrite != "")
				remote.PRBaseBefore = reparentOptionalString(entry.PRBaseBefore, entry.PRBaseBefore != "")
				remote.PRBaseAfter = reparentOptionalString(entry.PRBaseAfter, entry.PRBaseAfter != "")
			}
		}
		if pendingSet[row.Name] {
			remote.Divergence = "diverged"
		}
		if row.Role == ReparentRoleTarget && remote.PRBaseBefore == nil {
			before := reparentPRBaseParentToken(st.OldParentRef, st.OldParentStoredToken)
			after := reparentPRBaseParentToken(st.NewParentRef, st.NewParentStoredToken)
			remote.PRBaseBefore = reparentOptionalString(before, before != "")
			remote.PRBaseAfter = reparentOptionalString(after, after != "")
		} else if row.Role == ReparentRoleDescendant && remote.PRBaseBefore == nil {
			if parent := st.Row(row.DestinationParent); parent != nil && parent.GitBranch != "" {
				remote.PRBaseBefore = reparentOptionalString(parent.GitBranch, true)
				remote.PRBaseAfter = reparentOptionalString(parent.GitBranch, true)
			}
		}
		if !pendingSet[row.Name] {
			switch remote.RemoteSHA {
			case nil:
				remote.Divergence = "no-upstream"
			default:
				remote.Divergence = "none"
			}
		}
		out.Rows = append(out.Rows, remote)
	}
	out.Guidance = ReparentRemoteGuidance(out.Rows)
	out.FollowupRecord = ReparentPlanRemoteFollowup{
		WillWrite: st.RemoteRecordWritten, WritePoint: ReparentRemoteRecordWritePoint,
		Path:    reparentOptionalString(ReparentRemoteRecordPath(loc), st.RemoteRecordWritten),
		Entries: ensureSlice(st.RemoteFollowupEntries), ClearRule: reparentRemoteClearRule,
	}
	return out
}

func planReparentResolved(in ReparentPlanInput, req ReparentRequest, stack Stack, target StackEntry) (ReparentPlan, ReparentRequest, error) {
	var repoIdentities map[string]string
	if in.Loc.Mode == ModeExternal {
		repoIdentities = reparentRepoIdentityMap(in.RepoRoot, stack)
		req.RepoIdentities = repoIdentities
	}
	closure, err := ReparentClosureOrderByRepoIdentity(stack, in.Target, repoIdentities)
	if err != nil {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind:   ReparentRefusalStackUnsortable,
			Detail: err.Error(),
		})
		plan, buildErr := BuildReparentPlan(req)
		return finalizeUnavailableReparentPlan(plan, in, stack), req, buildErr
	}
	if in.Loc.Mode == ModeCheckout {
		rejected := false
		for _, row := range stack.Branches {
			if strings.TrimSpace(row.Repo) == "" {
				continue
			}
			name := row.Name
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalCrossRepoClosure, Entry: &name,
				Detail: fmt.Sprintf("checkout mode does not support repo %q; execution is fixed to workspace repository %s", row.Repo, in.RepoRoot),
			})
			rejected = true
		}
		if rejected {
			plan, buildErr := BuildReparentPlan(req)
			return finalizeUnavailableReparentPlan(plan, in, stack), req, buildErr
		}
	}
	if in.SessionProbe != nil {
		affected := make([]string, 0, len(closure))
		for _, entry := range closure {
			affected = append(affected, entry.Name)
		}
		live, probeErr := in.SessionProbe(affected)
		if probeErr != nil {
			// A session record that cannot be decoded counts as live (§6.2),
			// so a failed probe refuses rather than admitting the run.
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind:   ReparentRefusalSessionLive,
				Detail: fmt.Sprintf("session liveness could not be verified: %v", probeErr),
			})
		}
		req.LiveSessions = live
	}

	// Every probe from here on runs in the TARGET's repository context, not
	// blindly in the workspace root: an entry whose StackEntry.Repo names a
	// secondary repository is planned, and later executed, there.
	targetRepoDir := in.TargetRepoRoot
	if targetRepoDir == "" {
		targetRepoDir = reparentRowRepoDir(in.RepoRoot, target.Repo)
	}
	req.TargetRepoRoot = targetRepoDir
	targetCommonDir := repoIdentities[target.Name]
	if in.Loc.Mode == ModeCheckout || targetCommonDir == "" {
		targetCommonDir = reparentCommonDir(targetRepoDir)
	}
	if targetCommonDir == "" {
		name := target.Name
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind: ReparentRefusalRepoUnavailable, Entry: &name,
			Detail: fmt.Sprintf("%q resolves to %s, which is not a repository this run can read", target.Name, targetRepoDir),
		})
		plan, buildErr := BuildReparentPlan(req)
		return finalizeUnavailableReparentPlan(plan, in, stack), req, buildErr
	}

	version, _ := ProbeGitVersion()
	req.CapsProbed = true
	req.Caps = GitCapabilitiesForVersion(version)
	req.ReparentCaps = ReparentGitCapabilitiesForVersion(version)
	req.GitVersionLine = version.Raw
	req.RefBackend = probeReparentRefBackend(targetRepoDir, req.ReparentCaps)

	width, err := reparentOIDWidth(targetRepoDir)
	if err != nil {
		var refusal *ReparentRefusalError
		if asReparentRefusal(err, &refusal) {
			req.Blockers = append(req.Blockers, refusal.Blocker())
		} else {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Detail: err.Error(),
			})
		}
		req.OIDWidth = 0
		plan, buildErr := BuildReparentPlan(req)
		return finalizeUnavailableReparentPlan(plan, in, stack), req, buildErr
	}
	req.OIDWidth = width

	logicalStack := reparentStackWithRepoIdentities(stack, repoIdentities)
	logicalTarget := GetBranch(logicalStack, target.Name)
	req.OldParent = reparentEntryParent(stack, target.Name)
	if tip, ok, oldParentErr := resolveReparentOldParentTip(targetRepoDir, logicalStack, logicalTarget, width); oldParentErr != nil {
		var refusal *ReparentRefusalError
		if asReparentRefusal(oldParentErr, &refusal) {
			req.Blockers = append(req.Blockers, refusal.Blocker())
		} else {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Detail: oldParentErr.Error(),
			})
		}
	} else if ok {
		req.OldParentTipSHA = tip
		sha := tip
		req.OldParent.SHA = &sha
	}

	dest, destErr := ResolveReparentDestinationByRepoIdentity(
		targetRepoDir, stack, target, in.OntoToken, in.OntoKind, width, repoIdentities,
	)
	req.Destination = dest
	if destErr != nil {
		var refusal *ReparentRefusalError
		if asReparentRefusal(destErr, &refusal) {
			req.Blockers = append(req.Blockers, refusal.Blocker())
		} else {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{Kind: ReparentRefusalProbeFailed, Detail: destErr.Error()})
		}
	} else {
		req.DestinationResolved = true
		post := reparentPostImageStack(stack, target.Name, dest.StoredToken)
		logicalPost := reparentStackWithRepoIdentities(post, repoIdentities)
		// The agreement is asked about the POST-IMAGE edge — the graph the
		// very next `tws sync` and `tws stack status` read — so the entry
		// handed to the shipped resolvers must be the post-image one, whose
		// Base is already the stored token. Handing them the pre-image entry
		// would make every run refuse destination-resolver-divergent against
		// the parent it is moving away from.
		postTarget := GetBranch(logicalPost, target.Name)
		if agreement, err := resolveReparentAgreement(targetRepoDir, logicalPost, postTarget, dest.Kind, dest.StoredToken, dest.SHA); err == nil {
			req.DestinationAgreement = agreement
			if agreement.Agreed != nil && !*agreement.Agreed {
				req.Blockers = append(req.Blockers, ReparentPlanBlocker{
					Kind:   ReparentRefusalDestinationResolverDivergent,
					Detail: "the four base resolvers do not agree on the destination commit",
				})
			}
		} else {
			var refusal *ReparentRefusalError
			if asReparentRefusal(err, &refusal) {
				req.Blockers = append(req.Blockers, refusal.Blocker())
			} else {
				req.Blockers = append(req.Blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalProbeFailed, Detail: err.Error(),
				})
			}
		}
		if req.OldParentTipSHA != "" && dest.SHA != "" {
			if isAncestor, err := reparentIsAncestor(targetRepoDir, dest.SHA, req.OldParentTipSHA); err == nil {
				req.DestinationIsAncestorOfOldParent = isAncestor
			} else {
				req.Blockers = append(req.Blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalProbeFailed, Detail: err.Error(),
				})
			}
		}
	}
	req.DefaultBranch = reparentDefaultBranch(targetRepoDir)

	sameParentCandidate := req.DestinationResolved &&
		derefString(req.OldParent.StoredToken) == req.Destination.StoredToken &&
		derefString(req.OldParent.SHA) != "" &&
		derefString(req.OldParent.SHA) == req.Destination.SHA
	rows, rowBlockers := measureReparentRows(in, stack, logicalStack, target, closure, width, sameParentCandidate)
	req.Rows = rows
	req.Blockers = append(req.Blockers, rowBlockers...)
	holders, holderBlockers := measureReparentHolders(in, targetRepoDir, rows)
	req.Holders = holders
	req.Blockers = append(req.Blockers, holderBlockers...)
	if branch, head, detached, err := measureReparentOriginal(targetRepoDir); err != nil {
		req.Blockers = append(req.Blockers, ReparentPlanBlocker{
			Kind: ReparentRefusalProbeFailed, Detail: err.Error(),
		})
	} else {
		req.OriginalBranch, req.OriginalHead, req.OriginalDetached = branch, head, detached
	}
	req.UpdateRefsConfigured = reparentUpdateRefsConfigured(targetRepoDir)

	if rec, err := LoadReparentRemoteRecord(in.Loc); err != nil {
		var refusal *ReparentRefusalError
		if asReparentRefusal(err, &refusal) {
			req.Blockers = append(req.Blockers, refusal.Blocker())
		} else {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalStateCorrupt, Detail: err.Error(),
			})
		}
	} else if rec != nil {
		req.RemotePending = rec.PendingEntries()
	}
	req.StateFiles, req.Exclusion = measureReparentStateFiles(in.Loc, in.ExclusionOwner)
	if !req.AuthoritativeStatePresent {
		if detail := ReparentForeignSyncStateOwned(in.Loc, in.ExclusionOwner); detail != "" {
			req.Blockers = append(req.Blockers, ReparentPlanBlocker{
				Kind:   ReparentRefusalSyncStatePresent,
				Detail: detail,
			})
		}
	}

	source := "workspace-repo-root"
	if strings.TrimSpace(target.Repo) != "" {
		source = "entry-repo"
	}
	ctx := reparentFetchContext(targetRepoDir, target.Repo, targetCommonDir, source)
	req.Fetch = reparentFetchPlan(in.Route, in.FetchPolicy, in.FetchDefaultApplied, ctx)

	plan, buildErr := BuildReparentPlan(req)
	return plan, req, buildErr
}

func reparentOntoKindOrAuto(kind string) string {
	if kind == "" {
		return "auto"
	}
	return kind
}

func reparentLimit(v *int) PlanGuardLimit {
	if v == nil {
		return PlanGuardLimit{Origin: "none"}
	}
	out := *v
	return PlanGuardLimit{Value: &out, Origin: "flag"}
}

// measureReparentRows probes every closure row, in ReparentClosureOrder, and
// resolves its cutoff through the shipped §5.1/§5.2 ladders.
func measureReparentRows(in ReparentPlanInput, stack, logicalStack Stack, target StackEntry, closure []StackEntry, width int, sameParentCandidate bool) ([]ReparentRowProbe, []ReparentPlanBlocker) {
	var blockers []ReparentPlanBlocker
	rows := make([]ReparentRowProbe, 0, len(closure))
	strictNoWork := false
	for i, entry := range closure {
		rowDir := reparentRowRepoDir(in.RepoRoot, entry.Repo)
		probe := ReparentRowProbe{
			Name:            entry.Name,
			GitBranch:       entry.GitBranch(),
			Repo:            entry.Repo,
			Order:           i,
			Role:            ReparentRoleDescendant,
			Archived:        entry.Archived,
			Materialization: "materialized",
			ProviderHint:    reparentProviderHint(reparentOriginURL(rowDir)),
		}
		if i == 0 {
			probe.Role = ReparentRoleTarget
		}

		branchRef := "refs/heads/" + probe.GitBranch
		if err := reparentRequireDirectBranchRef(rowDir, branchRef); err != nil {
			name := entry.Name
			blockers = append(blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Entry: &name, Detail: err.Error(),
			})
		}
		if sha, ok, err := reparentResolveRef(rowDir, branchRef); err == nil {
			probe.HeadSHA, probe.HeadFound = sha, ok
		} else {
			name := entry.Name
			blockers = append(blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Entry: &name, Detail: err.Error(),
			})
		}
		probe.CommonDir = reparentCommonDir(rowDir)
		source := "workspace-repo-root"
		if strings.TrimSpace(entry.Repo) != "" {
			source = "entry-repo"
		}
		root := canonicalize(rowDir)
		probe.ExecutionContext = PlanContext{RepoRoot: &root, Source: source}
		if probe.CommonDir == "" {
			// The configured repository is not a Git repository this process
			// can resolve. Every later probe would silently measure nothing,
			// so the row refuses here instead.
			name := entry.Name
			blockers = append(blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalRepoUnavailable, Entry: &name,
				Detail: fmt.Sprintf("%q resolves to %s, which is not a repository this run can read", entry.Name, rowDir),
			})
			rows = append(rows, probe)
			continue
		}
		contextID := reparentExecutionContextID(root, probe.CommonDir)
		probe.ExecutionContext.ContextID = &contextID
		logicalEntry := GetBranch(logicalStack, entry.Name)
		ancestry, ancestryErr := measureReparentCanonicalAncestry(rowDir, logicalStack, logicalEntry)
		probe.Ancestry = ancestry
		if ancestryErr != nil {
			name := entry.Name
			blockers = append(blockers, ReparentPlanBlocker{
				Kind: ReparentRefusalProbeFailed, Entry: &name, Detail: ancestryErr.Error(),
			})
		}
		if i == 0 && sameParentCandidate && ancestry.Status != nil &&
			*ancestry.Status == AncestryStatusCurrent {
			strictNoWork = true
		}
		if strictNoWork {
			recordedState := ReparentCutoffRecordAbsent
			if entry.LastBaseSHA != "" {
				recordedState = ReparentCutoffRecordPresent
			}
			probe.Cutoff = ReparentPlanCutoff{
				RecordedSHA:   reparentOptionalString(entry.LastBaseSHA, entry.LastBaseSHA != ""),
				RecordedState: recordedState,
				Provenance:    ReparentCutoffNone,
			}
			probe.Replay = PlanEntryReplay{Determinacy: "not-applicable", Commits: []string{}}
			probe.RemoteRef, probe.RemoteSHA, probe.UpstreamConfigured, probe.Divergence =
				measureReparentRemote(rowDir, probe.GitBranch)
			rows = append(rows, probe)
			continue
		}

		var cutoff ReparentPlanCutoff
		var err error
		if i == 0 {
			cutoff, err = resolveReparentTargetCutoff(rowDir, logicalStack, logicalEntry, entry.LastBaseSHA, in.CutoffToken, width)
		} else {
			cutoff, err = resolveReparentDescendantCutoff(rowDir, logicalStack, logicalEntry, entry.LastBaseSHA, width)
		}

		probe.Cutoff = cutoff
		if err != nil {
			var refusal *ReparentRefusalError
			if asReparentRefusal(err, &refusal) {
				blockers = append(blockers, refusal.Blocker())
			} else {
				blockers = append(blockers, ReparentPlanBlocker{Kind: ReparentRefusalProbeFailed, Detail: err.Error()})
			}
			rows = append(rows, probe)
			continue
		}

		resolved := derefString(cutoff.ResolvedSHA)
		if resolved != "" && probe.HeadFound {
			if isAncestor, err := reparentIsAncestor(rowDir, resolved, branchRef); err == nil {
				probe.CutoffAncestorProbed = true
				probe.CutoffIsAncestor = isAncestor
				probe.Cutoff.AncestorOfBranch = &isAncestor
			} else {
				name := entry.Name
				blockers = append(blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalProbeFailed, Entry: &name, Detail: err.Error(),
				})
			}
			replay, replayErr := measureReparentReplay(rowDir, resolved, branchRef)
			probe.Replay = replay
			if replayErr != nil {
				name := entry.Name
				blockers = append(blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalProbeFailed, Entry: &name, Detail: replayErr.Error(),
				})
			}
			mergeCount, mergeErr := measureReparentMergeCount(rowDir, resolved, branchRef)
			probe.MergeCommitsInRange = mergeCount
			if mergeErr != nil {
				name := entry.Name
				blockers = append(blockers, ReparentPlanBlocker{
					Kind: ReparentRefusalProbeFailed, Entry: &name, Detail: mergeErr.Error(),
				})
			}
		}
		probe.RemoteRef, probe.RemoteSHA, probe.UpstreamConfigured, probe.Divergence =
			measureReparentRemote(rowDir, probe.GitBranch)
		rows = append(rows, probe)
	}

	// Collateral refs are enumerated ONCE per run — one for-each-ref over the
	// target repository's ref space — and then intersected with each row's
	// already-measured replay range. v1 is single-repository by construction
	// (§6.1 step 5 refuses anything else before mutation), so the target's
	// context is the closure's context.
	targetDir := in.RepoRoot
	if len(rows) > 0 {
		targetDir = reparentRowRepoDir(in.RepoRoot, closure[0].Repo)
	}
	attachReparentCollateralRefs(targetDir, rows)

	// §6.2's dirty / operation-in-progress probes are per consulted worktree,
	// not per row: one worktree serves every row of a single-repository
	// closure, so they are measured once, in the TARGET's context, and
	// attributed to the target.
	if len(rows) > 0 {
		dirty, err := measureReparentDirty(targetDir)
		if err != nil {
			blockers = append(blockers, ReparentPlanBlocker{Kind: ReparentRefusalProbeFailed, Detail: err.Error()})
		}
		rows[0].DirtyPaths = dirty
		untracked, err := measureReparentUntracked(targetDir)
		if err != nil {
			blockers = append(blockers, ReparentPlanBlocker{Kind: ReparentRefusalProbeFailed, Detail: err.Error()})
		}
		rows[0].UntrackedPaths = untracked
		op, opErr := measureReparentGitOperation(targetDir)
		if opErr != nil {
			blockers = append(blockers, ReparentPlanBlocker{Kind: ReparentRefusalProbeFailed, Detail: opErr.Error()})
		}
		rows[0].GitOperationInProgress = op
	}
	return rows, blockers
}

func reparentExecutionContextID(repoRoot, commonDir string) string {
	sum := sha256.Sum256([]byte(canonicalize(repoRoot) + "\x00" + canonicalize(commonDir)))
	return hex.EncodeToString(sum[:])
}

// attachReparentCollateralRefs publishes, per row, the refs OUTSIDE the
// closure whose tips lie inside that row's replay range. They are disclosure
// only: this feature never moves a collateral ref, which is precisely why an
// operator has to be told which ones a replay will strand.
func attachReparentCollateralRefs(repoDir string, rows []ReparentRowProbe) {
	res, err := runReparentGit(repoDir, nil, "for-each-ref", "--format=%(objectname) %(refname)",
		"refs/heads", "refs/tags", "refs/remotes")
	if err != nil {
		return
	}
	closure := map[string]bool{}
	for _, row := range rows {
		closure["refs/heads/"+row.GitBranch] = true
	}
	type refRow struct{ sha, name string }
	var refs []refRow
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || closure[fields[1]] {
			continue
		}
		refs = append(refs, refRow{sha: strings.ToLower(fields[0]), name: fields[1]})
	}
	if len(refs) == 0 {
		return
	}
	for i := range rows {
		inRange := map[string]bool{}
		for _, sha := range rows[i].Replay.Commits {
			inRange[sha] = true
		}
		if len(inRange) == 0 {
			continue
		}
		for _, ref := range refs {
			if !inRange[ref.sha] {
				continue
			}
			rows[i].CollateralRefs = append(rows[i].CollateralRefs, PlanCollateralRef{
				Repo:       rows[i].Repo,
				Ref:        ref.name,
				SHA:        ref.sha,
				StackOwned: false,
			})
		}
	}
}

// reparentRowRepoDir resolves ONE row's Git context: its configured
// StackEntry.Repo when set, else the workspace repository root. Every row
// probe — its tip, its cutoff, its replay range, its remote facts — must run
// there, or a stack whose entries live in different repositories would be
// measured against the wrong one, and the cross-repo and repo-unavailable
// gates could never fire.
func reparentRowRepoDir(workspaceRoot, entryRepo string) string {
	repo := strings.TrimSpace(entryRepo)
	if repo == "" {
		return workspaceRoot
	}
	return canonicalize(repo)
}

func reparentRepoIdentityMap(workspaceRoot string, stack Stack) map[string]string {
	identities := make(map[string]string, len(stack.Branches))
	byRoot := make(map[string]string)
	for _, entry := range stack.Branches {
		root := reparentRowRepoDir(workspaceRoot, entry.Repo)
		key := canonicalize(root)
		common, ok := byRoot[key]
		if !ok {
			common = reparentCommonDir(root)
			byRoot[key] = common
		}
		identities[entry.Name] = common
	}
	return identities
}

func reparentStackWithRepoIdentities(stack Stack, identities map[string]string) Stack {
	out := stack
	out.Branches = append([]StackEntry{}, stack.Branches...)
	for i := range out.Branches {
		if identity := identities[out.Branches[i].Name]; identity != "" {
			out.Branches[i].Repo = identity
		}
	}
	return out
}

func reparentCommonDir(repoDir string) string {
	res, err := runReparentGit(repoDir, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return ""
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(repoDir, out)
	}
	return canonicalize(out)
}

// reparentRequireDirectBranchRef rejects symbolic refs/heads entries. v1
// deliberately forbids `option no-deref`, so allowing one into update-ref
// would move the symbolic target — potentially an out-of-closure branch.
func reparentRequireDirectBranchRef(repoDir, ref string) error {
	res, err := runReparentGit(repoDir, nil, "symbolic-ref", "--quiet", ref)
	if err == nil {
		target := strings.TrimSpace(string(res.Stdout))
		return fmt.Errorf("%s is symbolic to %s; affected refs/heads entries must be direct refs", ref, target)
	}
	if res.ExitCode == 1 {
		return nil
	}
	return fmt.Errorf("verify direct-ref status for %s: %s", ref, reparentGitMessage(res, err))
}

func reparentRequireAllDirectBranchRefs(repoDir string, rows []ReparentStateRow) error {
	for _, row := range rows {
		ref := "refs/heads/" + row.GitBranch
		if err := reparentRequireDirectBranchRef(repoDir, ref); err != nil {
			return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
		}
	}
	return nil
}

// measureReparentCanonicalAncestry projects the shipped StackEdge
// current/stale/divergent/missing ladder through reparent-owned probes. The
// current verdict is load-bearing for the strict same-parent split.
func measureReparentCanonicalAncestry(repoDir string, stack Stack, entry StackEntry) (PlanAncestry, error) {
	if entry.Base == "" {
		return PlanAncestry{Reason: ReasonBaseUnset}, nil
	}
	childRef := "refs/heads/" + entry.GitBranch()
	child, ok, err := reparentResolveRef(repoDir, childRef)
	if err != nil {
		return PlanAncestry{}, err
	}
	if !ok {
		status := AncestryStatusMissing
		return PlanAncestry{Status: &status, Reason: ReasonChildRefMissing}, nil
	}
	baseRef, _ := stackBaseRef(stack, entry)
	parent, ok, err := reparentResolveRef(repoDir, baseRef)
	if err != nil {
		return PlanAncestry{}, err
	}
	if !ok {
		status := AncestryStatusMissing
		return PlanAncestry{Status: &status, Reason: ReasonBaseRefMissing}, nil
	}
	contained, err := reparentIsAncestor(repoDir, parent, child)
	if err != nil {
		return PlanAncestry{}, err
	}
	if contained {
		status := AncestryStatusCurrent
		return PlanAncestry{Status: &status, Reason: ReasonParentContained}, nil
	}
	if entry.LastBaseSHA == "" {
		status := AncestryStatusStale
		return PlanAncestry{Status: &status, Reason: ReasonParentAdvancedNoBaseRecord}, nil
	}
	recorded, ok, err := reparentResolveRef(repoDir, entry.LastBaseSHA)
	if err != nil {
		return PlanAncestry{}, err
	}
	if !ok {
		status := AncestryStatusStale
		return PlanAncestry{Status: &status, Reason: ReasonBaseRecordUnresolvable}, nil
	}
	stillContainsRecord, err := reparentIsAncestor(repoDir, recorded, parent)
	if err != nil {
		return PlanAncestry{}, err
	}
	if stillContainsRecord {
		status := AncestryStatusStale
		return PlanAncestry{Status: &status, Reason: ReasonParentAdvanced}, nil
	}
	status := AncestryStatusDivergent
	return PlanAncestry{Status: &status, Reason: ReasonBaseRewritten}, nil
}

func measureReparentReplay(repoDir, cutoff, branchRef string) (PlanEntryReplay, error) {
	out := PlanEntryReplay{
		Determinacy:         "exact",
		UpstreamProvenance:  "reparent-cutoff",
		Commits:             []string{},
		MayDropBecomesEmpty: true,
	}
	upstream := cutoff
	out.UpstreamSHA = &upstream
	rangeSpec := cutoff + ".." + strings.TrimPrefix(branchRef, "refs/heads/")
	out.Range = &rangeSpec

	res, err := runReparentGit(repoDir, nil, "rev-list", "--no-merges", "--reverse", cutoff+".."+branchRef)
	if err != nil {
		out.Determinacy = "unknown"
		reason := "probe-failed"
		out.Reason = &reason
		return out, fmt.Errorf("git rev-list --no-merges --reverse %s..%s: %s", cutoff, branchRef, reparentGitMessage(res, err))
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out.Commits = append(out.Commits, strings.ToLower(line))
		}
	}
	count := len(out.Commits)
	out.CandidateCount = &count
	listed := count
	out.CommitsListed = &listed
	truncated := false
	out.CommitsTruncated = &truncated
	digest := sha256.Sum256([]byte(strings.Join(out.Commits, "\n")))
	d := hex.EncodeToString(digest[:])
	out.CandidateDigest = &d
	if count > 0 {
		first := out.Commits[0]
		subject := ""
		if res, err := runReparentGit(repoDir, nil, "log", "-1", "--format=%s", first); err == nil {
			subject = strings.TrimSpace(string(res.Stdout))
		}
		short := first
		if len(short) > 12 {
			short = short[:12]
		}
		out.FirstCandidate = &PlanReplayCandidate{SHA: first, Short: short, Subject: subject}
	}
	return out, nil
}

func measureReparentMergeCount(repoDir, cutoff, branchRef string) (*int, error) {
	res, err := runReparentGit(repoDir, nil, "rev-list", "--count", "--merges", cutoff+".."+branchRef)
	if err != nil {
		return nil, fmt.Errorf("git rev-list --count --merges %s..%s: %s", cutoff, branchRef, reparentGitMessage(res, err))
	}
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(string(res.Stdout)), "%d", &n); err != nil {
		return nil, fmt.Errorf("parse merge count for %s..%s: %w", cutoff, branchRef, err)
	}
	return &n, nil
}

func measureReparentRemote(repoDir, branch string) (ref, sha string, upstream bool, divergence string) {
	remoteRef := "refs/remotes/" + ReparentRemoteName + "/" + branch
	value, ok, err := reparentResolveRef(repoDir, remoteRef)
	if err != nil || !ok {
		return "", "", false, "no-upstream"
	}
	if res, err := runReparentGit(repoDir, nil, "config", "--get", "branch."+branch+".remote"); err == nil {
		upstream = strings.TrimSpace(string(res.Stdout)) != ""
	}
	res, err := runReparentGit(repoDir, nil, "rev-list", "--left-right", "--count", "refs/heads/"+branch+"..."+remoteRef)
	if err != nil {
		return remoteRef, value, upstream, "unknown"
	}
	var ahead, behind int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(res.Stdout)), "%d\t%d", &ahead, &behind); err != nil {
		return remoteRef, value, upstream, "unknown"
	}
	switch {
	case ahead == 0 && behind == 0:
		divergence = "none"
	case ahead > 0 && behind > 0:
		divergence = "diverged"
	case ahead > 0:
		divergence = "ahead"
	default:
		divergence = "behind"
	}
	return remoteRef, value, upstream, divergence
}

// measureReparentDirty lists the tracked modifications of one working tree.
//
// A failed probe returns an ERROR, never an empty list: "git could not tell
// us" and "the tree is clean" are different answers, and the second one is
// what authorises `git worktree remove --force`.
func measureReparentDirty(dir string) ([]string, error) {
	res, err := runReparentGit(dir, nil, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git status --porcelain in %s: %s", dir, reparentGitMessage(res, err))
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "??") {
			continue
		}
		if len(line) > 3 {
			out = append(out, strings.TrimSpace(line[3:]))
		}
	}
	return out, nil
}

// measureReparentUntracked lists the untracked, non-ignored paths of one
// working tree, and likewise reports a failed probe as an error.
func measureReparentUntracked(dir string) ([]string, error) {
	res, err := runReparentGit(dir, nil, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others in %s: %s", dir, reparentGitMessage(res, err))
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// measureReparentGitOperation reproduces sessionGitOperation's probe list
// through this boundary's own runner, and additionally reports WHICH
// operation is in progress so the refusal can name it.
func measureReparentGitOperation(dir string) (string, error) {
	for _, probe := range []struct{ path, name string }{
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"BISECT_LOG", "bisect"},
	} {
		res, err := runReparentGit(dir, nil, "rev-parse", "--git-path", probe.path)
		if err != nil {
			return "", fmt.Errorf("git rev-parse --git-path %s in %s: %s", probe.path, dir, reparentGitMessage(res, err))
		}
		path := strings.TrimSpace(string(res.Stdout))
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err == nil {
			return probe.name, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect Git operation path %s: %w", path, err)
		}
	}
	return "", nil
}

func reparentDefaultBranch(repoDir string) string {
	res, err := runReparentGit(repoDir, nil, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		out := strings.TrimSpace(string(res.Stdout))
		if idx := strings.LastIndex(out, "/"); idx >= 0 {
			return out[idx+1:]
		}
	}
	return ""
}

func reparentOriginURL(repoDir string) string {
	res, err := runReparentGit(repoDir, nil, "config", "--get", "remote."+ReparentRemoteName+".url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

func reparentUpdateRefsConfigured(repoDir string) bool {
	res, err := runReparentGit(repoDir, nil, "config", "--get", "rebase.updateRefs")
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(res.Stdout))) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

var reparentWorktreeInventoryHook func(repoRoot string) WorktreeInventory

func reparentWorktreeInventory(repoRoot string) WorktreeInventory {
	if reparentWorktreeInventoryHook != nil {
		return reparentWorktreeInventoryHook(repoRoot)
	}
	if repoRoot == "" {
		return worktreeInventoryUnavailable(errors.New("worktree inventory requires a non-empty repository root"))
	}
	res, err := runReparentGit(repoRoot, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return worktreeInventoryUnavailable(fmt.Errorf("%s", reparentGitMessage(res, err)))
	}
	return parseWorktreeInventory(res.Stdout)
}

// measureReparentHolders projects the shipped worktree inventory into holder
// probes. BuildWorktreeInventory itself is reused unchanged; the two
// reparent-specific filters layer on top of it, never inside it: the run's own
// scratch path is excluded, and in checkout mode the physical checkout is
// marked as the computation context so §9.9's drift check skips the HEAD the
// run deliberately detached itself.
func measureReparentHolders(in ReparentPlanInput, repoRoot string, rows []ReparentRowProbe) ([]ReparentHolderProbe, []ReparentPlanBlocker) {
	inv := reparentWorktreeInventory(repoRoot)
	if !inv.Available {
		detail := "the worktree inventory is unavailable"
		if inv.Err != nil {
			detail += ": " + inv.Err.Error()
		}
		return nil, []ReparentPlanBlocker{{
			Kind: ReparentRefusalHolderUnsafe, Detail: detail,
		}}
	}
	affected := map[string]ReparentRowProbe{}
	for _, row := range rows {
		affected[row.GitBranch] = row
	}
	byBranch := map[string][]WorktreeRecord{}
	var blockers []ReparentPlanBlocker
	for _, rec := range inv.Records {
		if in.ScratchPath != "" && canonicalize(rec.Path) == canonicalize(in.ScratchPath) {
			continue
		}
		op, opErr := measureReparentGitOperation(rec.Path)
		if opErr != nil {
			blockers = append(blockers, ReparentPlanBlocker{
				Kind:   ReparentRefusalProbeFailed,
				Detail: fmt.Sprintf("the Git-operation state of worktree %s could not be verified: %v", rec.Path, opErr),
			})
		}
		if rec.BranchRef == nil {
			if op != "" {
				blockers = append(blockers, ReparentPlanBlocker{
					Kind:   ReparentRefusalGitOperationInProgress,
					Detail: fmt.Sprintf("a %s is in progress in detached worktree %s; it cannot be safely associated with an unaffected branch", op, rec.Path),
				})
			}
			continue
		}
		branch := strings.TrimPrefix(*rec.BranchRef, "refs/heads/")
		if _, ok := affected[branch]; !ok {
			continue
		}
		byBranch[branch] = append(byBranch[branch], rec)
	}
	var out []ReparentHolderProbe
	for _, row := range rows {
		records := byBranch[row.GitBranch]
		for _, rec := range records {
			holder := ReparentHolderProbe{
				GitBranch:   row.GitBranch,
				HolderPath:  rec.Path,
				PreimageSHA: row.HeadSHA,
				Prunable:    rec.Prunable,
				Duplicate:   len(records) > 1,
				HolderKind:  "linked-worktree",
			}
			if canonicalize(rec.Path) == canonicalize(repoRoot) {
				holder.HolderKind = "primary-checkout"
			}
			if in.Loc.Mode == ModeCheckout && canonicalize(rec.Path) == canonicalize(repoRoot) {
				holder.HolderKind = "computation-context"
				holder.Excluded = true
			}
			if !rec.Prunable {
				dirty, dirtyErr := measureReparentDirty(rec.Path)
				if dirtyErr != nil {
					blockers = append(blockers, ReparentPlanBlocker{
						Kind:   ReparentRefusalProbeFailed,
						Detail: fmt.Sprintf("the tracked state of holder %s could not be verified: %v", rec.Path, dirtyErr),
					})
				}
				holder.DirtyPaths = dirty
				op, opErr := measureReparentGitOperation(rec.Path)
				if opErr != nil {
					blockers = append(blockers, ReparentPlanBlocker{
						Kind:   ReparentRefusalProbeFailed,
						Detail: fmt.Sprintf("the Git-operation state of holder %s could not be verified: %v", rec.Path, opErr),
					})
				}
				holder.GitOperationInProgress = op
			}
			out = append(out, holder)
		}
	}
	return out, blockers
}

func measureReparentOriginal(repoDir string) (branch, head string, detached bool, err error) {
	res, symErr := runReparentGit(repoDir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	switch {
	case symErr == nil:
		branch = strings.TrimSpace(string(res.Stdout))
		if branch == "" {
			return "", "", false, fmt.Errorf("symbolic HEAD branch is empty in %s", repoDir)
		}
	case res.ExitCode == 1:
		detached = true
	default:
		return "", "", false, fmt.Errorf("inspect symbolic HEAD in %s: %s", repoDir, reparentGitMessage(res, symErr))
	}
	sha, ok, resolveErr := reparentResolveRef(repoDir, "HEAD")
	if resolveErr != nil {
		return "", "", false, resolveErr
	}
	if !ok || strings.TrimSpace(sha) == "" {
		return "", "", false, fmt.Errorf("HEAD does not resolve to a non-empty commit in %s", repoDir)
	}
	return branch, sha, detached, nil
}

// measureReparentStateFiles publishes all seven state.files rows — the five
// shipped sync facts and the two reparent-owned ones — plus the exclusion
// verdict. Every one of them is measured through an API that already lives in
// package internal (ReadCheckoutLock, ReadSyncRunGuard, the path helpers and a
// raw read for the documents whose shipped decoders this feature deliberately
// breaks), so none of them is a hardcoded zero and none needs caller wiring.
//
// Each row is `applicable` only in the mode that owns it: publishing a
// checkout lock fact for an external run would be an invented answer, not a
// measured one.
func measureReparentStateFiles(loc ReparentLocation, owner *ReparentExclusionOwner) (ReparentPlanStateFiles, ReparentPlanExclusion) {
	files := ReparentPlanStateFiles{}
	exclusion := ReparentPlanExclusion{}
	if loc.Mode == ModeCheckout {
		files.CheckoutTransaction = measureReparentCheckoutTransactionFact(loc)
		files.CheckoutLock = measureReparentCheckoutLockFact(loc)
	} else {
		files.ExternalLegacyState = measureReparentLegacyStateFact(loc)
		files.ExternalRunPayload = measureReparentRunPayloadFact(loc)
		files.ExternalRunGuard = measureReparentRunGuardFact(loc)
	}

	load := LoadReparentState(loc)
	switch load.Kind {
	case ReparentStateAbsent:
		files.ReparentState.PlanStateFileBase = PlanStateFileBase{Applicable: true, Presence: "absent"}
	case ReparentStateCorrupt:
		reason := load.Detail
		files.ReparentState.PlanStateFileBase = PlanStateFileBase{Applicable: true, Presence: "unreadable", UnreadableReason: &reason}
		exclusion.ReparentActive = true
	default:
		files.ReparentState.PlanStateFileBase = PlanStateFileBase{Applicable: true, Presence: "readable"}
		exclusion.ReparentActive = true
		if st := load.State; st != nil {
			version := st.StateVersion
			runID := st.RunID
			stage := string(st.Stage)
			resume := string(st.ResumeStage)
			feature := st.Feature
			mode := st.WorkspaceMode
			files.ReparentState.StateVersion = &version
			files.ReparentState.RunID = &runID
			files.ReparentState.Stage = &stage
			files.ReparentState.ResumeStage = &resume
			files.ReparentState.Feature = &feature
			files.ReparentState.Mode = &mode
			pid := st.OwnerPID
			live := isProcessAlive(pid)
			exclusion.LockHolderPID = &pid
			exclusion.LockHolderLive = &live
			consistent := ClassifyReparentCompatArtifacts(loc, st.RunID).Complete
			exclusion.CompatArtifactsConsistent = &consistent
		}
	}

	rec, err := LoadReparentRemoteRecord(loc)
	switch {
	case err != nil:
		reason := err.Error()
		files.ReparentRemoteRecord.PlanStateFileBase = PlanStateFileBase{Applicable: true, Presence: "unreadable", UnreadableReason: &reason}
	case rec == nil:
		files.ReparentRemoteRecord.PlanStateFileBase = PlanStateFileBase{Applicable: true, Presence: "absent"}
	default:
		version := rec.RecordVersion
		files.ReparentRemoteRecord.PlanStateFileBase = PlanStateFileBase{Applicable: true, Presence: "readable"}
		files.ReparentRemoteRecord.RecordVersion = &version
		files.ReparentRemoteRecord.PendingEntries = len(rec.PendingEntries())
	}

	exclusion.SyncActive = ReparentForeignSyncStateOwned(loc, owner) != ""
	return files, exclusion
}

// reparentFileBase is the shared {applicable, presence, unreadable_reason}
// prefix, measured with one Stat.
func reparentFileBase(path string) (PlanStateFileBase, []byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanStateFileBase{Applicable: true, Presence: "absent"}, nil, false
		}
		reason := err.Error()
		return PlanStateFileBase{Applicable: true, Presence: "unreadable", UnreadableReason: &reason}, nil, false
	}
	return PlanStateFileBase{Applicable: true, Presence: "readable"}, data, true
}

// measureReparentCheckoutTransactionFact reads the checkout transaction as RAW
// YAML. It deliberately does NOT call LoadCheckoutTransaction: this feature's
// own compatibility transaction is required to fail that decoder, and calling
// it here would report the run's own correct guard as unreadable sync state —
// exactly the claim §11.10 forbids every surface from making.
func measureReparentCheckoutTransactionFact(loc ReparentLocation) PlanStateFileCheckoutTransaction {
	out := PlanStateFileCheckoutTransaction{}
	base, data, ok := reparentFileBase(CheckoutTransactionPath(loc.FeaturePath))
	out.PlanStateFileBase = base
	if !ok {
		return out
	}
	var probe struct {
		StateVersion   int    `yaml:"state_version"`
		Feature        string `yaml:"feature"`
		ReparentMarker string `yaml:"reparent_marker"`
	}
	// state_version is an int for every shipped writer and a string for this
	// feature's compatibility artifact, so the int decode failing is an
	// ANSWER — "this is the reparent guard" — not an error.
	if err := yaml.Unmarshal(data, &probe); err == nil && probe.StateVersion > 0 {
		version := probe.StateVersion
		out.StateVersion = &version
	}
	var identity struct {
		Feature        string `yaml:"feature"`
		ReparentMarker string `yaml:"reparent_marker"`
	}
	if err := yaml.Unmarshal(data, &identity); err == nil && identity.Feature != "" {
		feature := identity.Feature
		out.Feature = &feature
	}
	return out
}

func measureReparentCheckoutLockFact(loc ReparentLocation) PlanStateFileCheckoutLock {
	out := PlanStateFileCheckoutLock{}
	base, _, ok := reparentFileBase(CheckoutLockPath(loc.FeaturePath))
	out.PlanStateFileBase = base
	if !ok {
		return out
	}
	info, err := ReadCheckoutLock(loc.FeaturePath)
	if err != nil || info == nil {
		reason := "the checkout-sync lock does not decode"
		if err != nil {
			reason = err.Error()
		}
		out.Presence = "unreadable"
		out.UnreadableReason = &reason
		return out
	}
	pid := info.PID
	live := pid > 0 && isProcessAlive(pid)
	out.OwnerPID = &pid
	out.OwnerLive = &live
	if info.Created != "" {
		created := info.Created
		out.AcquiredAt = &created
	}
	// LockInfo carries no host, and §11.6b forbids adding one for this
	// feature, so owner_host stays the honest null.
	return out
}

func measureReparentLegacyStateFact(loc ReparentLocation) PlanStateFileExternalLegacyState {
	out := PlanStateFileExternalLegacyState{}
	base, data, ok := reparentFileBase(SyncStatePath(loc.FeaturePath))
	out.PlanStateFileBase = base
	if !ok {
		return out
	}
	var probe struct {
		Feature string `yaml:"feature"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		reason := err.Error()
		out.Presence = "unreadable"
		out.UnreadableReason = &reason
		return out
	}
	if probe.Feature != "" {
		feature := probe.Feature
		out.Feature = &feature
	}
	return out
}

// measureReparentRunPayloadFact reads the v2/v4 payload raw, for the same
// reason as the checkout transaction: LoadSyncRunState rejects state_version 4
// BY DESIGN, and that rejection is this feature's fail-closed guarantee, not a
// corruption to report.
func measureReparentRunPayloadFact(loc ReparentLocation) PlanStateFileExternalPayload {
	out := PlanStateFileExternalPayload{Selected: []string{}}
	base, data, ok := reparentFileBase(SyncRunStatePath(loc.FeaturePath))
	out.PlanStateFileBase = base
	if !ok {
		return out
	}
	var probe struct {
		Feature  string   `yaml:"feature"`
		Selected []string `yaml:"selected"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		reason := err.Error()
		out.Presence = "unreadable"
		out.UnreadableReason = &reason
		return out
	}
	if probe.Feature != "" {
		feature := probe.Feature
		out.Feature = &feature
	}
	out.Selected = ensureSlice(probe.Selected)
	return out
}

func measureReparentRunGuardFact(loc ReparentLocation) PlanStateFileExternalRunGuard {
	out := PlanStateFileExternalRunGuard{}
	base, _, ok := reparentFileBase(SyncRunGuardPath(loc.FeaturePath))
	out.PlanStateFileBase = base
	if !ok {
		return out
	}
	guard, err := ReadSyncRunGuard(loc.FeaturePath)
	if err != nil || guard == nil {
		reason := "the sync run guard does not decode"
		if err != nil {
			reason = err.Error()
		}
		out.Presence = "unreadable"
		out.UnreadableReason = &reason
		return out
	}
	pid := guard.PID
	live := pid > 0 && isProcessAlive(pid)
	out.OwnerPID = &pid
	out.OwnerLive = &live
	return out
}

// ============================================================================
// BeginReparentRun — §11.4's nine-step order
// ============================================================================

// ReparentConflictPause is a PAUSE, not a refusal: §9.7 gives it its own
// prose, no `reparent:` marker and no failure_kind, and §13.2's
// one-refusal-line rule explicitly excludes it.
type ReparentConflictPause struct {
	Entry     string
	GitBranch string
	Context   string
	Feature   string
}

func (e *ReparentConflictPause) Error() string {
	return fmt.Sprintf("conflict while computing %s (%s)", e.Entry, e.GitBranch)
}

// Message is the verbatim five-line block §9.7 requires on stderr. The
// `git -C <path>` inside it is OPERATOR GUIDANCE for a command the operator
// runs by hand; this executor never emits -C itself.
func (e *ReparentConflictPause) Message() string {
	return fmt.Sprintf(
		"reparent paused: conflict while computing %s (%s)\n"+
			"resolve in: %s\n"+
			"then run: git -C %s rebase --continue\n"+
			"then run: tws stack reparent %s --continue\n"+
			"to discard the whole reparent: tws stack reparent %s --abort\n",
		e.Entry, e.GitBranch, e.Context, e.Context, e.Feature, e.Feature)
}

// ReparentRun is one live run: its location, its authoritative state, the
// computation context it computes in, and the writers it speaks through.
type ReparentRun struct {
	Loc      ReparentLocation
	RepoRoot string
	State    *ReparentState
	Plan     ReparentPlan
	Input    ReparentPlanInput
	Writers  ReparentWriters

	ctxDir   string
	lockHeld bool

	// commitGateRewinds bounds requireCommittedRefs' self-rewinds within one
	// invocation.
	commitGateRewinds int

	// abortForwardOnly records which arm of §11.8a an --abort took. It is the
	// run's own answer, not a guess a caller can make from the outside: the
	// commit point is decided from disk inside AbortReparent, and a caller
	// that re-derived it could print "rolled back" for a run that was not.
	abortForwardOnly bool
}

// AbortWasForwardOnly reports that --abort found the run already committed and
// therefore moved no ref and rewrote no metadata: it restored holders, cleaned
// up artifacts, and left the topology change in place. It is false for a
// bounded pre-commit rollback and false for every other route.
func (r *ReparentRun) AbortWasForwardOnly() bool {
	if r == nil {
		return false
	}
	return r.abortForwardOnly
}

func (r *ReparentRun) HolderRestorationPending() bool {
	return r != nil && r.State != nil && len(r.State.HolderRestoreDeferred) > 0 &&
		HasReparentState(r.Loc)
}

// SuccessLines reconstructs every success-only fact from persisted state, so
// a resumed run prints the same remote guidance, actual argv and honest
// atomicity outcome as a fresh run without depending on an in-memory plan.
func (r *ReparentRun) SuccessLines() []string {
	if r == nil || r.State == nil {
		return nil
	}
	st := r.State
	lines := []string{fmt.Sprintf("reparent: %s is now based on %s (run %s)",
		st.TargetName, st.NewParentStoredToken, st.RunID)}
	for _, row := range st.RowsInOrder() {
		lines = append(lines, fmt.Sprintf("reparent: %s materialized argv: %q", row.Name, row.MaterializedArgv))
	}
	crash := "not crash-atomic"
	if reparentBackendCrashAtomic(st.RefBackend) {
		crash = "crash-atomic"
	}
	lines = append(lines,
		fmt.Sprintf("reparent: atomicity: the ref CAS was race-atomic and %s on the %s backend", crash, st.RefBackend),
		"reparent: atomicity: refs, stack.yaml, and worktree/index state were three separate effects",
		"reparent: atomicity: the one commit point required durable post-image stack.yaml and every affected ref at its planned tip or no-op",
		"reparent: atomicity: recovery is forward-only after that commit point",
	)
	var pending []string
	if rec, err := LoadReparentRemoteRecord(r.Loc); err == nil && rec != nil {
		pending = rec.PendingEntries()
	}
	remote := reparentPersistedRemotePlan(r.Loc, st, pending)
	lines = append(lines, remote.Guidance...)
	return lines
}

// ReparentBeginInput is BeginReparentRun's whole input: the measured plan and
// the request that produced it, so the post-lock re-snapshot compares like
// with like.
type ReparentBeginInput struct {
	Input   ReparentPlanInput
	Plan    ReparentPlan
	Request ReparentRequest
}

// BeginReparentRun runs §11.4 steps 3-9 in exactly that order and returns a
// run positioned at `preflight`, before any mutation.
//
// The authoritative artifact is written BEFORE the compatibility artifacts,
// never after and never the reverse: the gap between step 6 and step 7 is
// crash window 1, where --continue completes the envelope under §11.2a and
// --abort simply removes an artifact that describes nothing mutated.
func BeginReparentRun(in ReparentBeginInput) (*ReparentRun, error) {
	loc := in.Input.Loc
	if _, err := RefuseIfReparentActive(loc, ReparentRouteVerbFresh); err != nil {
		return nil, err
	}
	token, err := newReparentRunID()
	if err != nil {
		return nil, err
	}
	runID, err := newReparentRunID()
	if err != nil {
		return nil, err
	}

	// Step 3 — the mode lock.
	lockOwner, err := acquireReparentModeLock(loc, token, true)
	if err != nil {
		return nil, err
	}
	release := func() { _ = releaseReparentModeLock(loc, lockOwner) }
	if _, err := RefuseIfReparentActive(loc, ReparentRouteVerbFresh); err != nil {
		release()
		return nil, err
	}

	// Step 4 — the post-lock re-snapshot (§8.5). Everything the plan observed
	// was observed without this lock.
	if in.Input.SessionIntentCleanup != nil {
		affected := make([]string, 0, len(in.Request.Rows))
		for _, row := range in.Request.Rows {
			affected = append(affected, row.Name)
		}
		if err := in.Input.SessionIntentCleanup(affected); err != nil {
			release()
			return nil, reparentRefusal(ReparentRefusalSessionLive,
				fmt.Sprintf("stale session launch intents could not be reconciled after acquiring the mutation lock: %v", err))
		}
	}
	if err := reparentStep(ReparentStageInitializing, "post-lock-resnapshot"); err != nil {
		release()
		return nil, err
	}
	postLockInput := in.Input
	postLockInput.ExclusionOwner = lockOwner
	fresh, freshReq, err := PlanReparent(postLockInput)
	if err != nil {
		release()
		return nil, err
	}
	// §8.5 steps 3-4 just re-ran the session, dirty, git-operation and holder
	// probes under the lock. A fact that arose inside the concurrency window —
	// a newly live session, a worktree that just went dirty, a rebase someone
	// started, a second holder of an affected branch — is a BLOCKER on the
	// fresh document, and it is evaluated before the fingerprint comparison
	// deliberately: such a blocker also makes the fresh plan unapprovable,
	// which changes approval.scope and therefore the fingerprint, so comparing
	// first would report every one of these races as a generic
	// "the plan changed" instead of naming the hazard the operator has to fix.
	// §8.5's own wording requires the specific kind ("A newly live session
	// refuses session-live"), which is exactly this order.
	if refusal := reparentPlanRefusalError(fresh); refusal != nil {
		release()
		return nil, refusal
	}
	if !fresh.Runnable {
		release()
		return nil, reparentRefusal(ReparentRefusalPlanUnavailable,
			"the plan is no longer runnable after the lock was taken")
	}
	if err := ReparentLimitRefusal(fresh, in.Input.Guard); err != nil {
		release()
		return nil, err
	}
	if err := reparentComparePostLockIdentity(in.Request, freshReq); err != nil {
		release()
		return nil, err
	}
	// Only once the run is still admissible does a differing fingerprint mean
	// what it says: the approved DECISION moved.
	if err := reparentCompareFingerprints(in.Plan, fresh, in.Input.Guard.Approve); err != nil {
		release()
		return nil, err
	}
	if err := reparentStep(ReparentStageInitializing, "post-lock-plan-approved"); err != nil {
		release()
		return nil, err
	}

	// Step 5 — the pre-image capture.
	// The pre-image is captured from the POST-LOCK measurement, never from
	// the pre-lock plan: capturing the older one would persist tips that were
	// already stale when the lock was taken.
	pre, err := captureReparentPreImage(in.Input, freshReq)
	if err != nil {
		release()
		return nil, err
	}
	if pre.StackSHA256 != freshReq.StackSHA256 {
		release()
		return nil, &PlanGuardRefusalError{
			Kind:   string(RefusalRevalidationMismatch),
			Detail: "stack.yaml changed between the post-lock plan and pre-image capture; re-run --plan and approve the new fingerprint",
		}
	}
	remoteBefore, remoteBeforePresent, err := ReadReparentRemoteRecordBytes(loc)
	if err != nil {
		release()
		return nil, err
	}

	st := newReparentState(in.Input, fresh, pre, runID, token)
	st.RemoteRecordBeforeCaptured = true
	st.RemoteRecordBeforePresent = remoteBeforePresent
	if remoteBeforePresent {
		st.RemoteRecordBeforeBase64 = base64.StdEncoding.EncodeToString(remoteBefore)
	}
	run := &ReparentRun{
		Loc: loc,
		// The run mutates the TARGET's repository, which is the workspace
		// root only when no entry names another one.
		RepoRoot: st.RepoRoot,
		State:    st,
		Plan:     fresh,
		Input:    postLockInput,
		Writers:  in.Input.Writers,
		lockHeld: true,
	}

	// Step 6 — the authoritative artifact, at `initializing`.
	if err := validateReparentStateSemantics(loc, st); err != nil {
		release()
		return nil, reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the assembled reparent transaction is invalid: %v", err))
	}
	if err := SaveReparentState(loc, st); err != nil {
		release()
		return nil, err
	}
	if err := reparentStep(ReparentStageInitializing, "artifact-written"); err != nil {
		return run, err
	}

	// Step 7 — the compatibility envelope, in §11.2 order.
	selected := make([]string, 0, len(st.Rows))
	for _, row := range st.RowsInOrder() {
		selected = append(selected, row.Name)
	}
	if err := WriteReparentCompatArtifacts(loc, st, selected); err != nil {
		return run, err
	}
	st.CompatArtifactsWritten = true
	if err := SaveReparentState(loc, st); err != nil {
		return run, err
	}
	if err := reparentStep(ReparentStageInitializing, "compat-written"); err != nil {
		return run, err
	}

	// Step 8 — prove the artifact decodes at the expected version before a
	// single mutation depends on it.
	if load := LoadReparentState(loc); load.Kind != ReparentStateOK {
		if refusal := load.Refusal(); refusal != nil {
			return run, refusal
		}
		return run, reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the reparent state artifact at %s did not read back", load.Path))
	}

	// Step 9 — and only then mutate.
	if err := run.setStage(ReparentStagePreflight); err != nil {
		return run, err
	}
	// A local clear is a mutation too. It is deliberately ordered after the
	// authoritative artifact has read back, the compatibility envelope is
	// complete, and preflight is durable, but before scratch/checkout
	// computation begins. Crash window 1 therefore leaves the prior record
	// byte-identical and recovery can always reconstruct compatibility before
	// reconciling a journaled clear.
	if _, err := run.applyRemoteClearsJournaled(&freshReq.Stack); err != nil {
		return run, err
	}
	return run, nil
}

func reparentComparePostLockIdentity(approved, fresh ReparentRequest) error {
	mismatch := func(detail string) error {
		return &PlanGuardRefusalError{
			Kind:   string(RefusalRevalidationMismatch),
			Detail: detail + "; re-run --plan and approve the new fingerprint",
		}
	}
	if canonicalize(approved.TargetRepoRoot) != canonicalize(fresh.TargetRepoRoot) {
		return mismatch(fmt.Sprintf("the target repository changed from %s to %s",
			canonicalize(approved.TargetRepoRoot), canonicalize(fresh.TargetRepoRoot)))
	}
	approvedRows := reparentPostLockRowIdentities(approved.Rows)
	freshRows := reparentPostLockRowIdentities(fresh.Rows)
	if !equalStringSlices(approvedRows, freshRows) {
		return mismatch(fmt.Sprintf("repository/common-dir identity changed from %v to %v", approvedRows, freshRows))
	}
	approvedHolders := reparentPostLockHolderIdentities(approved.Holders)
	freshHolders := reparentPostLockHolderIdentities(fresh.Holders)
	if !equalStringSlices(approvedHolders, freshHolders) {
		return mismatch(fmt.Sprintf("holder identity changed from %v to %v", approvedHolders, freshHolders))
	}
	approvedOriginal := fmt.Sprintf("%s\x00%s\x00%t", approved.OriginalBranch, approved.OriginalHead, approved.OriginalDetached)
	freshOriginal := fmt.Sprintf("%s\x00%s\x00%t", fresh.OriginalBranch, fresh.OriginalHead, fresh.OriginalDetached)
	if approvedOriginal != freshOriginal {
		return mismatch("the original checkout branch/HEAD identity changed")
	}
	return nil
}

func reparentPostLockRowIdentities(rows []ReparentRowProbe) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		contextRoot := ""
		if row.ExecutionContext.RepoRoot != nil {
			contextRoot = filepath.Clean(*row.ExecutionContext.RepoRoot)
		}
		common := row.CommonDir
		if common != "" {
			common = filepath.Clean(common)
		}
		out = append(out, fmt.Sprintf("%06d\x00%s\x00%s\x00%s\x00%s\x00%s",
			row.Order, row.Name, row.GitBranch, row.Repo, contextRoot, common))
	}
	return out
}

func reparentPostLockHolderIdentities(holders []ReparentHolderProbe) []string {
	out := make([]string, 0, len(holders))
	for _, holder := range holders {
		path := holder.HolderPath
		if path != "" {
			path = canonicalize(path)
		}
		action := "detach-and-restore"
		switch {
		case holder.Excluded:
			action = "already-detached"
		case holder.Prunable || holder.Duplicate:
			action = "refuse"
		}
		out = append(out, strings.Join([]string{
			path, holder.GitBranch, holder.PreimageSHA,
			holder.HolderKind, action,
		}, "\x00"))
	}
	sort.Strings(out)
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reparentCompareFingerprints is §8.5 step 5. A mismatch refuses in the sync
// guard's own rendering, names what moved, and leaves nothing behind.
func reparentCompareFingerprints(approvedPlan, fresh ReparentPlan, token string) error {
	freshFP, err := ReparentPlanFingerprint(fresh)
	if err != nil {
		return err
	}
	if token != "" && token != freshFP {
		return &PlanGuardRefusalError{
			Kind:   string(RefusalRevalidationMismatch),
			Detail: "the plan changed between approval and the lock; re-run --plan and approve the new fingerprint",
		}
	}
	if approvedPlan.Approval.Fingerprint != nil && *approvedPlan.Approval.Fingerprint != freshFP {
		return &PlanGuardRefusalError{
			Kind:   string(RefusalRevalidationMismatch),
			Detail: "the plan changed between approval and the lock; re-run --plan and approve the new fingerprint",
		}
	}
	return nil
}

// acquireReparentModeLock takes the mode-appropriate shared lock. A FRESH
// execution uses the shipped acquire path, which refuses a live lock; only
// recovery reclaims (§4.10).
func acquireReparentModeLock(loc ReparentLocation, token string, fresh bool) (*ReparentExclusionOwner, error) {
	if loc.Mode == ModeCheckout {
		var globalBytes []byte
		var globalErr error
		if fresh {
			globalBytes, globalErr = AcquireCheckoutMutationLock(loc.CheckoutStateDir, token, loc.Feature, "reparent",
				ReparentStatePath(loc), filepath.Dir(CheckoutTransactionPath(loc.FeaturePath)))
		} else {
			globalBytes, globalErr = ReclaimCheckoutMutationLock(loc.CheckoutStateDir, token, loc.Feature, "reparent",
				ReparentStatePath(loc), filepath.Dir(CheckoutTransactionPath(loc.FeaturePath)))
		}
		if globalErr != nil {
			return nil, reparentRefusal(ReparentRefusalSyncStatePresent, globalErr.Error())
		}
		var err error
		if fresh {
			err = AcquireCheckoutLock(loc.FeaturePath)
		} else {
			err = ReclaimCheckoutLock(loc.FeaturePath)
		}
		if err != nil {
			var live *CheckoutLockLiveError
			if errorsAsCheckoutLockLive(err, &live) {
				_ = ReleaseCheckoutMutationLock(loc.CheckoutStateDir, token)
				return nil, reparentRefusal(ReparentRefusalSyncStatePresent, fmt.Sprintf(
					"another reparent recovery (pid %d) currently owns the shared checkout-sync lock", live.PID))
			}
			_ = ReleaseCheckoutMutationLock(loc.CheckoutStateDir, token)
			return nil, reparentRefusal(ReparentRefusalSyncStatePresent, err.Error())
		}
		data, readErr := reparentLockBytes(loc)
		if readErr != nil {
			ReleaseCheckoutLock(loc.FeaturePath)
			_ = ReleaseCheckoutMutationLock(loc.CheckoutStateDir, token)
			return nil, reparentRefusal(ReparentRefusalSyncStatePresent,
				fmt.Sprintf("the newly acquired checkout sync lock could not be verified: %v", readErr))
		}
		return &ReparentExclusionOwner{
			OwnerToken: token, OwnerPID: os.Getpid(), LockBytes: data, GlobalLockBytes: globalBytes,
		}, nil
	}
	var err error
	if fresh {
		err = ClaimSyncRunGuard(loc.FeaturePath, token)
	} else {
		err = ReclaimSyncRunGuard(loc.FeaturePath, token)
	}
	if err != nil {
		return nil, reparentRefusal(ReparentRefusalSyncStatePresent, err.Error())
	}
	data, readErr := reparentLockBytes(loc)
	if readErr != nil {
		ReleaseSyncRunGuard(loc.FeaturePath)
		return nil, reparentRefusal(ReparentRefusalSyncStatePresent,
			fmt.Sprintf("the newly acquired sync run guard could not be verified: %v", readErr))
	}
	return &ReparentExclusionOwner{OwnerToken: token, OwnerPID: os.Getpid(), LockBytes: data}, nil
}

func releaseReparentModeLock(loc ReparentLocation, owner *ReparentExclusionOwner) error {
	path := SyncRunGuardPath(loc.FeaturePath)
	if loc.Mode == ModeCheckout {
		path = CheckoutLockPath(loc.FeaturePath)
	}
	data, err := os.ReadFile(path)
	present := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if present {
		if !reparentLockOwned(loc, data, owner) {
			return reparentRefusal(ReparentRefusalSyncStatePresent,
				fmt.Sprintf("refusing to release foreign shared lock at %s", path))
		}
		if loc.Mode == ModeExternal {
			if err := syncIOFault(SyncIORemoveSyncRunGuard, path); err != nil {
				return err
			}
		}
		if err := removeLockIfUnchanged(path, data); err != nil {
			return err
		}
	}
	if err := reparentRecoverySyncDir(filepath.Dir(path)); err != nil {
		return err
	}
	if loc.Mode == ModeCheckout {
		token := ""
		if owner != nil {
			token = owner.OwnerToken
		}
		if err := ReleaseCheckoutMutationLock(loc.CheckoutStateDir, token); err != nil {
			return err
		}
		return reparentRecoverySyncDir(filepath.Dir(CheckoutMutationLockPath(loc.CheckoutStateDir)))
	}
	return nil
}

// captureReparentPreImage is §11.4 step 5: every fact a rollback or a
// forward completion will need, captured before anything moves.
func captureReparentPreImage(in ReparentPlanInput, req ReparentRequest) (ReparentPreImage, error) {
	bytesOnDisk, err := os.ReadFile(StackPath(in.Loc.FeaturePath))
	if err != nil {
		return ReparentPreImage{}, err
	}
	sum := sha256.Sum256(bytesOnDisk)
	liveHash := hex.EncodeToString(sum[:])
	if req.StackSHA256 != "" && liveHash != req.StackSHA256 {
		return ReparentPreImage{}, &PlanGuardRefusalError{
			Kind:   string(RefusalRevalidationMismatch),
			Detail: "stack.yaml changed after the post-lock snapshot and before pre-image capture",
		}
	}
	stackBytes := bytesOnDisk
	if len(req.StackBytes) > 0 {
		stackBytes = append([]byte{}, req.StackBytes...)
	}
	repoRoot := req.TargetRepoRoot
	if repoRoot == "" {
		repoRoot = in.RepoRoot
	}
	workspaceRepoRoot := in.RepoRoot
	if workspaceRepoRoot == "" {
		workspaceRepoRoot = in.Workspace.RepoRoot
	}
	if workspaceRepoRoot == "" {
		workspaceRepoRoot = repoRoot
	}
	pre := ReparentPreImage{
		RowSHAs:           map[string]string{},
		StackBytes:        stackBytes,
		StackSHA256:       liveHash,
		RefBackend:        req.RefBackend,
		OIDWidth:          req.OIDWidth,
		RepoRoot:          repoRoot,
		RepoCommonDir:     reparentCommonDir(repoRoot),
		WorkspaceRepoRoot: canonicalize(workspaceRepoRoot),
		ValidationCommand: in.Validation.Command,
		ValidationSource:  reparentValidationSource(in.Validation),
		ValidationDigest:  in.Validation.Digest,
	}
	for _, row := range req.Rows {
		pre.RowSHAs[row.Name] = row.HeadSHA
	}
	for _, h := range req.Holders {
		pre.Holders = append(pre.Holders, ReparentHolderSnapshot{
			Path:          h.HolderPath,
			RepoCommonDir: reparentCommonDir(h.HolderPath),
			GitBranch:     h.GitBranch,
			HeadSHA:       h.PreimageSHA,
			Prunable:      h.Prunable,
			HolderKind:    h.HolderKind,
			Excluded:      h.Excluded,
		})
	}
	branch, head, detached, err := measureReparentOriginal(repoRoot)
	if err != nil {
		return ReparentPreImage{}, reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	pre.OriginalBranch, pre.OriginalHead, pre.OriginalDetached = branch, head, detached
	return pre, nil
}

// newReparentState assembles the §11.5 payload from the approved plan and the
// captured pre-image.
func newReparentState(in ReparentPlanInput, plan ReparentPlan, pre ReparentPreImage, runID, token string) *ReparentState {
	st := &ReparentState{
		StateVersion:      ReparentStateVersion,
		RunID:             runID,
		CreatedAt:         reparentNow(),
		WorkspaceMode:     string(in.Loc.Mode),
		WorkspaceStableID: in.Loc.StableID,
		Feature:           in.Loc.Feature,
		WorkspaceRepoRoot: pre.WorkspaceRepoRoot,
		RepoRoot:          pre.RepoRoot,
		RepoCommonDir:     pre.RepoCommonDir,
		RefBackend:        pre.RefBackend,
		OIDWidth:          pre.OIDWidth,
		Strategy:          ReparentStateStrategy,
		Backend:           ReparentStateBackend,
		Stage:             ReparentStageInitializing,
		ResumeStage:       ReparentStageInitializing,
		OwnerPID:          os.Getpid(),
		OwnerToken:        token,
		TargetName:        plan.Target.Name,
		TargetGitBranch:   plan.Target.GitBranch,

		OldParentRequestedToken: derefString(plan.Target.OldParent.RequestedToken),
		OldParentStoredToken:    derefString(plan.Target.OldParent.StoredToken),
		OldParentKind:           plan.Target.OldParent.Kind,
		OldParentRef:            derefString(plan.Target.OldParent.Ref),
		OldParentSHA:            derefString(plan.Target.OldParent.SHA),

		NewParentRequestedToken: derefString(plan.Target.NewParent.RequestedToken),
		NewParentStoredToken:    derefString(plan.Target.NewParent.StoredToken),
		NewParentKind:           plan.Target.NewParent.Kind,
		NewParentRef:            derefString(plan.Target.NewParent.Ref),
		NewParentSHA:            derefString(plan.Target.NewParent.SHA),

		DestinationPinRef:   reparentDestPinRef(runID),
		CutoffSuppliedToken: in.CutoffToken,

		ValidationCommandRaw:    pre.ValidationCommand,
		ValidationSource:        pre.ValidationSource,
		ValidationCommandDigest: pre.ValidationDigest,

		OriginalBranch:   pre.OriginalBranch,
		OriginalHead:     pre.OriginalHead,
		OriginalDetached: pre.OriginalDetached,

		StackBeforeBase64: base64.StdEncoding.EncodeToString(pre.StackBytes),
		StackSHA256Before: pre.StackSHA256,

		ApprovedHolders:       []ReparentApprovedHolder{},
		DetachedHolders:       []ReparentStateHolder{},
		CASRows:               []ReparentStateCASRow{},
		AbortRows:             []ReparentStateAbortRow{},
		RemoteFollowupEntries: []string{},

		MaxReplayPerEntry:   in.Guard.MaxPerEntry,
		MaxReplayTotal:      in.Guard.MaxTotal,
		ApprovedFingerprint: in.Guard.Approve,
		WaivedEvaluationIDs: append([]string{}, plan.Approval.Covers.WaivedEvaluationIDs...),
		WaivedKinds:         append([]RefusalKind{}, plan.Approval.Covers.WaivedKinds...),
		FetchPolicy:         string(in.FetchPolicy),
	}
	if pre.OriginalDetached && pre.OriginalHead != "" {
		st.OriginalHeadPinRef = reparentOriginalHeadPinRef(runID)
	}
	if in.Loc.Mode == ModeExternal {
		st.ScratchPath = canonicalReparentScratchPath(in.Loc, runID)
	}
	for _, holder := range pre.Holders {
		kind := holder.HolderKind
		if holder.Excluded {
			kind = "computation-context"
		}
		st.ApprovedHolders = append(st.ApprovedHolders, ReparentApprovedHolder{
			Path: holder.Path, RepoCommonDir: holder.RepoCommonDir,
			GitBranch: holder.GitBranch, HeadSHA: holder.HeadSHA,
			HolderKind: kind, Excluded: holder.Excluded,
		})
	}
	if in.Loc.Mode == ModeCheckout {
		found := false
		for _, holder := range st.ApprovedHolders {
			if canonicalize(holder.Path) == canonicalize(pre.RepoRoot) {
				found = true
				break
			}
		}
		if !found {
			st.ApprovedHolders = append(st.ApprovedHolders, ReparentApprovedHolder{
				Path:          pre.RepoRoot,
				RepoCommonDir: pre.RepoCommonDir,
				GitBranch:     pre.OriginalBranch,
				HeadSHA:       pre.OriginalHead,
				HolderKind:    "computation-context",
				Excluded:      true,
			})
		}
	}

	rows := append([]ReparentPlanRow{plan.Target}, plan.Descendants...)
	for _, row := range rows {
		entryID := ReparentEntryRefID(in.Loc.Feature, row.Name)
		cell := reparentMetadataCellFor(plan.MetadataDelta, row.Name)
		st.Rows = append(st.Rows, ReparentStateRow{
			Order:              row.Order,
			Name:               row.Name,
			GitBranch:          row.GitBranch,
			Repo:               row.Repo,
			Role:               row.Role,
			PreimageSHA:        pre.RowSHAs[row.Name],
			CutoffSHA:          derefString(row.Cutoff.ResolvedSHA),
			CutoffProvenance:   row.Cutoff.Provenance,
			DestinationBinding: row.DestinationBinding,
			DestinationParent:  derefString(row.DestinationParent),
			DestinationSHA:     derefString(row.DestinationSHA),
			BaseBefore:         cell.BaseBefore,
			BaseAfter:          cell.BaseAfter,
			LastBaseSHABefore:  derefString(cell.LastBaseSHABefore),
			LastBaseSHAAfter:   derefString(cell.LastBaseSHAAfter),
			Argv:               append([]string{}, row.Argv...),
			EffectiveBackend:   row.EffectiveBackend,
			CandidateCount:     reparentCandidateCount(row),
			CandidateDigest:    derefString(row.Replay.CandidateDigest),
			RevalidationDigest: reparentApprovedRevalidationDigest(row),
			OldPinRef:          reparentOldPinRef(runID, entryID),
			NewPinRef:          reparentNewPinRef(runID, entryID),
			Stage:              ReparentRowPending,
		})
	}
	return st
}

func reparentMetadataCellFor(delta ReparentPlanMetadataDelta, name string) ReparentPlanMetadataEntry {
	for _, e := range delta.Entries {
		if e.Name == name {
			return e
		}
	}
	return ReparentPlanMetadataEntry{Name: name}
}

// reparentApprovedRevalidationDigest freezes the §8.3 comparison value for one
// approved row. A digest that cannot be computed is stored empty, which makes
// the seam a no-op for that row rather than a refusal against nothing.
func reparentApprovedRevalidationDigest(row ReparentPlanRow) string {
	digest, err := ReparentRevalidationDigest(row)
	if err != nil {
		return ""
	}
	return digest
}

func reparentCandidateCount(row ReparentPlanRow) int {
	if row.Replay.CandidateCount == nil {
		return 0
	}
	return *row.Replay.CandidateCount
}

// errorsAsCheckoutLockLive is errors.As over the one typed lock verdict this
// boundary must classify, kept as a named helper so the intent — "is this the
// live-foreign case, and not invalid/unreadable/mkdir-failed?" — is legible at
// the call site.
func errorsAsCheckoutLockLive(err error, target **CheckoutLockLiveError) bool {
	for err != nil {
		if typed, ok := err.(*CheckoutLockLiveError); ok {
			*target = typed
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// ============================================================================
// The forward machine (§9, §10)
// ============================================================================

// setStage persists the stage and its resume_stage in ONE durable write.
func (r *ReparentRun) setStage(stage ReparentStage) error {
	r.State.SetStage(stage)
	return SaveReparentState(r.Loc, r.State)
}

// save persists the current state without moving the stage.
func (r *ReparentRun) save() error { return SaveReparentState(r.Loc, r.State) }

// fail records a terminal-for-this-attempt failure with the resume stage
// recovery proceeds from, then returns the error the caller surfaces.
func (r *ReparentRun) fail(domain, kind, detail string, resume ReparentStage, err error) error {
	r.State.FailureDomain = domain
	r.State.FailureKind = kind
	r.State.FailureDetail = detail
	r.State.ResumeStage = resume
	r.State.Stage = ReparentStageFailed
	if saveErr := SaveReparentState(r.Loc, r.State); saveErr != nil {
		return errors.Join(err, fmt.Errorf("persist failed reparent state: %w", saveErr))
	}
	return err
}

func (r *ReparentRun) prose(format string, args ...any) {
	if r.Writers.Prose == nil {
		return
	}
	fmt.Fprintf(r.Writers.Prose, format, args...) //nolint:errcheck
}

// RunReparent drives the run forward from its current stage to `completed`.
// It is the same machine a --continue re-enters, which is what makes every
// resume idempotent rather than a second implementation of the same ladder.
func RunReparent(run *ReparentRun) error {
	for {
		switch run.State.Stage {
		case ReparentStageInitializing:
			// §11.6 gives `initializing` and `preflight` ONE row: the
			// compatibility envelope has already been completed under §11.2a
			// by the recovery prelude, so the run simply advances.
			if err := run.setStage(ReparentStagePreflight); err != nil {
				return err
			}
		case ReparentStagePreflight:
			if err := run.stagePreflight(); err != nil {
				return err
			}
		case ReparentStagePinningPreimages:
			if err := run.stagePinningPreimages(); err != nil {
				return err
			}
		case ReparentStageComputing, ReparentStageConflictPaused:
			if err := run.stageComputing(); err != nil {
				return err
			}
		case ReparentStageBuildingPostImage:
			if err := run.stageBuildingPostImage(); err != nil {
				return err
			}
		case ReparentStagePinningComputed:
			if err := run.stagePinningComputed(); err != nil {
				return err
			}
		case ReparentStageWritingRemoteRecord:
			if err := run.stageWritingRemoteRecord(); err != nil {
				return err
			}
		case ReparentStageDetachingHolders:
			if err := run.stageDetachingHolders(); err != nil {
				return err
			}
		case ReparentStageCommittingRefs:
			if err := run.stageCommittingRefs(); err != nil {
				return err
			}
		case ReparentStageRefsCommitted:
			if err := run.stageRefsCommitted(); err != nil {
				return err
			}
		case ReparentStageWritingMetadata:
			if err := run.stageWritingMetadata(); err != nil {
				return err
			}
		case ReparentStageMetadataWritten:
			if err := run.stageMetadataWritten(); err != nil {
				return err
			}
		case ReparentStageRestoringHolders:
			if err := run.stageRestoringHolders(); err != nil {
				if errors.Is(err, errReparentHoldersDeferred) {
					// A deferred holder is a warning on a run that SUCCEEDED.
					return nil
				}
				return err
			}
		case ReparentStageCleanup:
			if err := run.stageCleanup(); err != nil {
				return err
			}
		case ReparentStageCompleted:
			return nil
		default:
			return reparentRefusal(ReparentRefusalStateCorrupt,
				fmt.Sprintf("reparent run %s cannot move forward from stage %s", run.State.RunID, run.State.Stage))
		}
	}
}

// contextDir is the computation worktree: the tool-owned scratch in external
// mode, the one physical checkout in checkout mode.
func (r *ReparentRun) contextDir() string {
	if r.ctxDir != "" {
		return r.ctxDir
	}
	if r.Loc.Mode == ModeCheckout {
		r.ctxDir = r.RepoRoot
	} else {
		r.ctxDir = r.State.ScratchPath
	}
	return r.ctxDir
}

// stagePreflight establishes the computation context. External mode adds ONE
// tool-owned scratch worktree; checkout mode reuses its single physical
// checkout and records the detachment as the computation-context holder.
func (r *ReparentRun) stagePreflight() error {
	rows := r.State.RowsInOrder()
	if len(rows) == 0 {
		return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalNoWork),
			"the closure is empty", ReparentStagePreflight,
			reparentRefusal(ReparentRefusalNoWork, "the closure is empty"))
	}
	first := rows[0].PreimageSHA

	if r.Loc.Mode == ModeCheckout {
		if err := r.ensureOriginalHeadPin(); err != nil {
			return err
		}
		if err := r.reconcileHolderDetachIntents(); err != nil {
			return err
		}
		if r.State.PreimageHolderExcluded == "" {
			if err := reparentUntrackedGate(r.RepoRoot, first); err != nil {
				return err
			}
			branch := r.State.OriginalBranch
			h := r.recordHolderDetachIntent(r.RepoRoot, branch, r.State.OriginalHead, first,
				"computation-context", ReparentHolderDetachIntent)
			if err := r.beginHolderSwitch(h, ReparentHolderSwitchDetach, "", first); err != nil {
				return err
			}
			if err := reparentStep(ReparentStagePreflight, "computation-detach-intent-written"); err != nil {
				return err
			}
			if res, err := runReparentGit(r.RepoRoot, nil, "switch", "--detach", first); err != nil {
				return r.fail(ReparentFailureDomainNativeGit, "switch-failed",
					reparentGitMessage(res, err), ReparentStagePreflight,
					reparentRefusal(ReparentRefusalProbeFailed, reparentGitMessage(res, err)))
			}
			r.State.PreimageHolderExcluded = r.RepoRoot
			if err := r.completeHolderSwitch(h); err != nil {
				return err
			}
			if err := reparentStep(ReparentStagePreflight, "holder-detached-before-complete:"+r.RepoRoot); err != nil {
				return err
			}
		}
	} else {
		scratch := r.State.ScratchPath
		if _, err := os.Stat(filepath.Join(scratch, ".git")); err != nil {
			if err := os.MkdirAll(filepath.Dir(scratch), 0o700); err != nil {
				return reparentRefusal(ReparentRefusalScratchWorktreeUnavailable, err.Error())
			}
			if res, err := runReparentGit(r.RepoRoot, nil, "worktree", "add", "--detach", scratch, first); err != nil {
				return r.fail(ReparentFailureDomainNativeGit, string(ReparentRefusalScratchWorktreeUnavailable),
					reparentGitMessage(res, err), ReparentStagePreflight,
					reparentRefusal(ReparentRefusalScratchWorktreeUnavailable, reparentGitMessage(res, err)))
			}
		}
		if err := r.setComputationDetachTarget(first); err != nil {
			return err
		}
	}
	if err := reparentStep(ReparentStagePreflight, "context-created"); err != nil {
		return err
	}
	return r.setStage(ReparentStagePinningPreimages)
}

// stagePinningPreimages writes every old pin and the destination pin before
// the first computation. It is idempotent: an existing pin at the recorded
// value is left alone, a missing one is re-created.
func (r *ReparentRun) stagePinningPreimages() error {
	for _, row := range r.State.RowsInOrder() {
		if _, err := reparentVerifyPin(r.RepoRoot, row.OldPinRef, row.PreimageSHA); err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
		}
	}
	if r.State.NewParentSHA != "" {
		if _, err := reparentVerifyPin(r.RepoRoot, r.State.DestinationPinRef, r.State.NewParentSHA); err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
		}
	}
	if err := reparentStep(ReparentStagePinningPreimages, "pins-written"); err != nil {
		return err
	}
	return r.setStage(ReparentStageComputing)
}

// stageComputing computes every pending row, in closure order.
func (r *ReparentRun) stageComputing() error {
	if r.State.Stage == ReparentStageComputing {
		if err := r.reconcileUnrecordedConflict(); err != nil {
			return err
		}
	}
	if r.State.Stage == ReparentStageConflictPaused {
		if err := r.resumeConflictedRow(); err != nil {
			return err
		}
	}
	for _, row := range r.State.RowsInOrder() {
		live := r.State.Row(row.Name)
		if reparentRowStageRank(live.Stage) >= reparentRowStageRank(ReparentRowValidated) {
			continue
		}
		if err := r.computeRow(live); err != nil {
			var guardErr *PlanGuardRefusalError
			if errors.As(err, &guardErr) {
				return r.fail(
					ReparentFailureDomainPlanGuard,
					guardErr.Kind,
					guardErr.Detail,
					ReparentStageComputing,
					err,
				)
			}
			return err
		}
	}
	return r.setStage(ReparentStageBuildingPostImage)
}

func (r *ReparentRun) reconcileUnrecordedConflict() error {
	inProgress, err := reparentRebaseInProgress(r.contextDir())
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	if !inProgress {
		return nil
	}
	var row *ReparentStateRow
	for _, candidate := range r.State.RowsInOrder() {
		live := r.State.Row(candidate.Name)
		if live.Stage == ReparentRowPending {
			row = live
			break
		}
	}
	if row == nil {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"a rebase is active in the computation context but no pending row can own it")
	}
	anchor, _ := reparentConflictReflogAnchor(r.contextDir())
	row.ConflictReflogAnchor = anchor
	if head, ok, headErr := reparentResolveRef(r.contextDir(), "HEAD"); headErr == nil && ok {
		if err := r.setComputationDetachTarget(head); err != nil {
			return err
		}
	}
	if err := r.installConflictCompletionMarker(row); err != nil {
		return err
	}
	row.ConflictCompletionEvidence = ""
	r.State.SetStage(ReparentStageConflictPaused)
	r.State.ResumeStage = ReparentStageConflictPaused
	if err := r.save(); err != nil {
		return err
	}
	return r.resumeConflictedRow()
}

// rowDestination resolves one row's --onto operand just in time (§9.4a). A
// parent-computed destination is read ONLY from the parent row's recorded
// planned_new_sha, after that row reached `validated`, and is confirmed
// against the parent's new pin. It is never read from refs/heads/<parent>,
// from HEAD, or from any live ref: public refs still hold pre-image values
// here, and reading one would silently replay onto the old parent.
func (r *ReparentRun) rowDestination(row *ReparentStateRow) (string, error) {
	if row.DestinationBinding == ReparentBindingPinned {
		if row.DestinationSHA == "" {
			row.DestinationSHA = r.State.NewParentSHA
		}
		peeled, ok, err := reparentPeelCommit(r.RepoRoot, row.DestinationSHA)
		if err != nil {
			return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
		}
		if !ok || peeled != row.DestinationSHA {
			return "", reparentEntryRefusal(ReparentRefusalDestinationUnresolvable, row.Name,
				fmt.Sprintf("the pinned destination %s does not resolve", row.DestinationSHA))
		}
		return row.DestinationSHA, nil
	}

	parent := r.State.Row(row.DestinationParent)
	if parent == nil {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("%q names destination parent %q, which is not a row of this run", row.Name, row.DestinationParent))
	}
	if reparentRowStageRank(parent.Stage) < reparentRowStageRank(ReparentRowValidated) {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("destination parent %q is at row-stage %s, not validated", parent.Name, parent.Stage))
	}
	pinned, ok, err := reparentResolveRef(r.RepoRoot, parent.NewPinRef)
	if err != nil {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if !ok || pinned != parent.PlannedNewSHA {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, fmt.Sprintf(
			"destination parent %q pins %s but recorded %s", parent.Name, pinned, parent.PlannedNewSHA))
	}
	peeled, ok, err := reparentPeelCommit(r.RepoRoot, parent.PlannedNewSHA)
	if err != nil {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if !ok || peeled != parent.PlannedNewSHA {
		return "", reparentEntryRefusal(ReparentRefusalDestinationUnresolvable, row.Name,
			fmt.Sprintf("the computed tip of %q does not resolve", parent.Name))
	}
	row.DestinationSHA = parent.PlannedNewSHA
	return parent.PlannedNewSHA, nil
}

// computeRow is §9.4 for one row: resolve, gate, switch, rebase, record the
// tip, PIN IT IMMEDIATELY, and only then validate.
func (r *ReparentRun) computeRow(row *ReparentStateRow) error {
	ctx := r.contextDir()

	if row.Stage == ReparentRowPending {
		if err := r.revalidateRow(row); err != nil {
			return err
		}
		dest, err := r.rowDestination(row)
		if err != nil {
			return err
		}
		argv, err := materializeReparentArgv(row.Argv, dest)
		if err != nil {
			return err
		}
		row.MaterializedArgv = argv
		if err := r.save(); err != nil {
			return err
		}

		if err := reparentUntrackedGate(ctx, row.PreimageSHA); err != nil {
			return err
		}
		h, err := r.beginComputationSwitch(row.PreimageSHA)
		if err != nil {
			return err
		}
		if err := reparentStep(ReparentStageComputing, "computation-switch-intent-written:"+row.Name); err != nil {
			return err
		}
		if res, err := runReparentGit(ctx, nil, "switch", "--detach", row.PreimageSHA); err != nil {
			return r.fail(ReparentFailureDomainNativeGit, "switch-failed", reparentGitMessage(res, err),
				ReparentStageComputing, reparentRefusal(ReparentRefusalProbeFailed, reparentGitMessage(res, err)))
		}
		if err := r.completeHolderSwitch(h); err != nil {
			return err
		}
		if err := reparentStep(ReparentStageComputing, "switched:"+row.Name); err != nil {
			return err
		}

		res, err := runReparentGit(ctx, nil, argv[1:]...)
		if err != nil {
			inProgress, probeErr := reparentRebaseInProgress(ctx)
			if probeErr != nil {
				return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalProbeFailed), probeErr.Error(),
					ReparentStageComputing, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, probeErr.Error()))
			}
			if inProgress {
				if err := reparentStep(ReparentStageComputing, "conflict-detected-before-state:"+row.Name); err != nil {
					return err
				}
				anchor, _ := reparentConflictReflogAnchor(ctx)
				row.ConflictReflogAnchor = anchor
				if head, ok, headErr := reparentResolveRef(ctx, "HEAD"); headErr == nil && ok {
					if holderErr := r.setComputationDetachTarget(head); holderErr != nil {
						return holderErr
					}
				}
				if markerErr := r.installConflictCompletionMarker(row); markerErr != nil {
					return markerErr
				}
				row.ConflictCompletionEvidence = ""
				r.State.SetStage(ReparentStageConflictPaused)
				r.State.ResumeStage = ReparentStageConflictPaused
				if saveErr := r.save(); saveErr != nil {
					return saveErr
				}
				pause := &ReparentConflictPause{
					Entry: row.Name, GitBranch: row.GitBranch, Context: ctx, Feature: r.Loc.Feature,
				}
				return pause
			}
			detail := reparentGitMessage(res, err)
			return r.fail(ReparentFailureDomainNativeGit, "rebase-failed", detail, ReparentStageComputing,
				fmt.Errorf("git rebase failed while computing %s: %s", row.Name, detail))
		}
		if err := r.recordComputedTip(row); err != nil {
			return err
		}
	}

	if row.Stage == ReparentRowComputed {
		if err := r.pinComputedTip(row); err != nil {
			return err
		}
	}
	if row.Stage == ReparentRowPinned {
		if err := r.validateRow(row); err != nil {
			return err
		}
	}
	return nil
}

// recordComputedTip reads HEAD and persists it as the row's planned tip.
func (r *ReparentRun) recordComputedTip(row *ReparentStateRow) error {
	head, ok, err := reparentResolveRef(r.contextDir(), "HEAD")
	if err != nil || !ok {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, "HEAD does not resolve after the rebase")
	}
	row.PlannedNewSHA = head
	row.Noop = head == row.PreimageSHA
	row.Stage = ReparentRowComputed
	if h := r.computationHolder(); h != nil {
		h.DetachTargetSHA = head
		h.DetachStatus = ReparentHolderDetached
		h.Restored = false
	}
	if err := r.save(); err != nil {
		return err
	}
	return reparentStep(ReparentStageComputing, "computed:"+row.Name)
}

// pinComputedTip pins the computed tip the instant it exists — before
// validation, which can run for minutes and can itself invoke `git gc`, and
// before any later row is computed.
func (r *ReparentRun) pinComputedTip(row *ReparentStateRow) error {
	if err := r.verifyComputedRowState(row, false, ReparentRefusalProbeFailed); err != nil {
		return err
	}
	if err := reparentWritePin(r.RepoRoot, row.NewPinRef, row.PlannedNewSHA); err != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if err := r.verifyComputedRowState(row, true, ReparentRefusalProbeFailed); err != nil {
		return err
	}
	row.Stage = ReparentRowPinned
	if err := r.save(); err != nil {
		return err
	}
	return reparentStep(ReparentStageComputing, "pinned:"+row.Name)
}

// validateRow runs the frozen validation command in the computation context
// and then proves the context is still clean. Residue the command itself
// created is never cleaned, stashed or reset: that would destroy operator
// data, so the run preserves state and stays resumable instead.
func (r *ReparentRun) validateRow(row *ReparentStateRow) error {
	if err := r.verifyComputedRowState(row, true, ReparentRefusalProbeFailed); err != nil {
		return err
	}
	command := strings.TrimSpace(r.State.ValidationCommandRaw)
	if command != "" {
		beforeUntracked, err := measureReparentUntracked(r.contextDir())
		if err != nil {
			return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalProbeFailed), err.Error(),
				ReparentStageComputing,
				reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error()))
		}
		stdout, stderr, code, err := runReparentValidation(r.contextDir(), command)
		if err != nil || code != 0 {
			detail := strings.TrimSpace(string(stderr))
			if detail == "" {
				detail = strings.TrimSpace(string(stdout))
			}
			return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalValidationFailed), detail,
				ReparentStageComputing,
				reparentEntryRefusal(ReparentRefusalValidationFailed, row.Name,
					fmt.Sprintf("the configured validation command exited %d: %s", code, detail)))
		}
		residue, residueErr := reparentValidationResidue(r.contextDir(), beforeUntracked)
		if residueErr != nil {
			return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalProbeFailed), residueErr.Error(),
				ReparentStageComputing,
				reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, residueErr.Error()))
		}
		if len(residue) > 0 {
			detail := fmt.Sprintf(
				"the validation command left %s in %s; Git did not produce this residue. Inspect and clean it yourself, then --continue",
				strings.Join(residue, ", "), r.contextDir())
			return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalValidationFailed), detail,
				ReparentStageComputing,
				reparentEntryRefusal(ReparentRefusalValidationFailed, row.Name, detail))
		}
	}
	if err := r.verifyComputedRowState(row, true, ReparentRefusalValidationFailed); err != nil {
		return r.fail(ReparentFailureDomainReparent, string(ReparentRefusalValidationFailed), err.Error(),
			ReparentStageComputing, err)
	}
	row.Stage = ReparentRowValidated
	if err := r.save(); err != nil {
		return err
	}
	return reparentStep(ReparentStageComputing, "validated:"+row.Name)
}

func (r *ReparentRun) verifyComputedRowState(row *ReparentStateRow, requirePin bool, kind ReparentRefusalKind) error {
	head, ok, err := reparentResolveRef(r.contextDir(), "HEAD")
	if err != nil {
		return &ReparentRefusalError{
			Kind: kind, Entry: reparentOptionalEntry(row.Name),
			Detail: fmt.Sprintf("the computation-context HEAD could not be verified: %v", err), StatePreserved: true,
		}
	}
	if !ok || head != row.PlannedNewSHA {
		return &ReparentRefusalError{
			Kind: kind, Entry: reparentOptionalEntry(row.Name),
			Detail:         fmt.Sprintf("the computation-context HEAD is %s, expected planned_new_sha %s", head, row.PlannedNewSHA),
			StatePreserved: true,
		}
	}
	if !requirePin {
		return nil
	}
	pinned, ok, err := reparentResolveRef(r.RepoRoot, row.NewPinRef)
	if err != nil {
		return &ReparentRefusalError{
			Kind: kind, Entry: reparentOptionalEntry(row.Name),
			Detail: fmt.Sprintf("the computed pin could not be verified: %v", err), StatePreserved: true,
		}
	}
	if !ok || pinned != row.PlannedNewSHA {
		return &ReparentRefusalError{
			Kind: kind, Entry: reparentOptionalEntry(row.Name),
			Detail:         fmt.Sprintf("the computed pin is %s, expected planned_new_sha %s", pinned, row.PlannedNewSHA),
			StatePreserved: true,
		}
	}
	return nil
}

func reparentOptionalEntry(name string) *string {
	if name == "" {
		return nil
	}
	return &name
}

// revalidateRow is §8.3's just-in-time seam, run immediately BEFORE a row is
// computed. It re-probes that row's candidate inputs, compares their digest
// against the approved one, and enforces the frozen limits against the freshly
// resolved counts — the per-entry limit for this row, and the cumulative total
// across every row this run has already computed plus this one.
//
// Every refusal here is a *PlanGuardRefusalError carrying state-preserved: the
// run has an artifact, pins and possibly computed rows, so it stays resumable
// with --continue and reversible with --abort.
func (r *ReparentRun) revalidateRow(row *ReparentStateRow) error {
	fresh, err := r.assessRowRevalidation(row, false)
	if err != nil {
		return err
	}
	// The freshly resolved count is what a later row's cumulative check must
	// accumulate, exactly as the shipped guard accumulates the count its own
	// seam resolved rather than the approved row's recorded value.
	row.CandidateCount = fresh
	return r.save()
}

func (r *ReparentRun) assessRowRevalidation(row *ReparentStateRow, allowDeferredDestination bool) (int, error) {
	live, err := r.probeRowLive(row, allowDeferredDestination)
	if err != nil {
		var refusal *ReparentRefusalError
		if asReparentRefusal(err, &refusal) {
			refusal.StatePreserved = true
		}
		return 0, err
	}

	approved := reparentApprovedRowShape(row)
	if row.RevalidationDigest != "" {
		if _, err := RevalidateReparentRow(approved, row.RevalidationDigest, live); err != nil {
			return 0, err
		}
	}

	fresh := 0
	if live.Replay.CandidateCount != nil {
		fresh = *live.Replay.CandidateCount
	}
	if limit := r.State.MaxReplayPerEntry; limit != nil && fresh > *limit {
		id := "max_replay_per_entry:" + row.Name
		if !r.State.waivesLimit(id, RefusalLimitPerEntry) {
			return 0, &PlanGuardRefusalError{
				Kind: string(RefusalLimitPerEntry),
				Detail: fmt.Sprintf("%s: %d candidate(s) exceeds the effective per-entry replay limit of %d",
					row.Name, fresh, *limit),
				StatePreserved: true,
			}
		}
	}
	if limit := r.State.MaxReplayTotal; limit != nil {
		total := fresh
		for _, other := range r.State.Rows {
			if other.Name == row.Name {
				continue
			}
			if reparentRowStageRank(other.Stage) >= reparentRowStageRank(ReparentRowComputed) {
				total += other.CandidateCount
			}
		}
		if total > *limit {
			if !r.State.waivesLimit("max_replay_total", RefusalLimitTotal) {
				return 0, &PlanGuardRefusalError{
					Kind: string(RefusalLimitTotal),
					Detail: fmt.Sprintf("%d candidate(s) exceeds the effective total replay limit of %d",
						total, *limit),
					StatePreserved: true,
				}
			}
		}
	}
	return fresh, nil
}

func (s *ReparentState) waivesLimit(id string, kind RefusalKind) bool {
	hasID := false
	for _, waived := range s.WaivedEvaluationIDs {
		if waived == id {
			hasID = true
			break
		}
	}
	if !hasID {
		return false
	}
	for _, waived := range s.WaivedKinds {
		if waived == kind {
			return true
		}
	}
	return false
}

// probeRowLive re-measures one row's candidate inputs: its live branch tip,
// its recorded cutoff re-peeled, and the replay range between them.
func (r *ReparentRun) probeRowLive(row *ReparentStateRow, allowDeferredDestination bool) (ReparentRowProbe, error) {
	branchRef := "refs/heads/" + row.GitBranch
	live := ReparentRowProbe{Name: row.Name, GitBranch: row.GitBranch}
	if err := reparentRequireDirectBranchRef(r.RepoRoot, branchRef); err != nil {
		return live, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	head, ok, err := reparentResolveRef(r.RepoRoot, branchRef)
	if err != nil {
		return live, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if !ok {
		return live, reparentEntryRefusal(ReparentRefusalBranchRefMissing, row.Name,
			fmt.Sprintf("%s no longer resolves", branchRef))
	}
	live.HeadSHA, live.HeadFound = head, true

	cutoff, ok, err := reparentPeelCommit(r.RepoRoot, row.CutoffSHA)
	if err != nil {
		return live, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if !ok {
		return live, reparentEntryRefusal(ReparentRefusalCutoffUnresolvable, row.Name,
			fmt.Sprintf("recorded cutoff %s no longer resolves", row.CutoffSHA))
	}
	resolved := cutoff
	live.Cutoff = ReparentPlanCutoff{
		RecordedSHA:   &resolved,
		RecordedState: ReparentCutoffRecordPresent,
		ResolvedSHA:   &resolved,
		Provenance:    row.CutoffProvenance,
	}
	ancestor, err := reparentIsAncestor(r.RepoRoot, resolved, branchRef)
	if err != nil {
		return live, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	live.CutoffAncestorProbed = true
	live.CutoffIsAncestor = ancestor
	if !ancestor {
		return live, reparentEntryRefusal(ReparentRefusalCutoffNotAncestor, row.Name, fmt.Sprintf(
			"%s is no longer an ancestor of %s", resolved, branchRef))
	}
	parent := r.State.Row(row.DestinationParent)
	deferredDestination := allowDeferredDestination && row.DestinationBinding == ReparentBindingParentComputed &&
		parent != nil && parent.PlannedNewSHA == ""
	if !deferredDestination {
		if _, err := r.rowDestination(row); err != nil {
			return live, err
		}
	}
	replay, err := measureReparentReplay(r.RepoRoot, resolved, branchRef)
	if err != nil {
		return live, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	live.Replay = replay
	mergeCount, err := measureReparentMergeCount(r.RepoRoot, resolved, branchRef)
	if err != nil {
		return live, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	live.MergeCommitsInRange = mergeCount
	if mergeCount != nil && *mergeCount > 0 {
		return live, reparentEntryRefusal(ReparentRefusalMergeCommitInReplaySet, row.Name, fmt.Sprintf(
			"%d merge commit(s) now lie in the replay range", *mergeCount))
	}
	return live, nil
}

// reparentApprovedRowShape rebuilds the STATIC half of the approved plan row
// from state, so the seam's digest is computed over the same tuple the plan
// bound. A parent-computed row's destination SHA is deliberately nil here even
// once the run has resolved it: the plan bound it as an explicit absence, and
// binding the resolved value would make every descendant mismatch.
func reparentApprovedRowShape(row *ReparentStateRow) ReparentPlanRow {
	out := ReparentPlanRow{
		Name:               row.Name,
		GitBranch:          row.GitBranch,
		DestinationBinding: row.DestinationBinding,
	}
	if row.DestinationParent != "" {
		parent := row.DestinationParent
		out.DestinationParent = &parent
	}
	if row.DestinationBinding == ReparentBindingPinned && row.DestinationSHA != "" {
		sha := row.DestinationSHA
		out.DestinationSHA = &sha
	}
	return out
}

// resumeConflictedRow is §11.6's conflict-paused row: a rebase still in
// progress refuses conflict-unresolved and names the native command; a
// resolved one records HEAD, pins it immediately and validates.
func (r *ReparentRun) resumeConflictedRow() error {
	ctx := r.contextDir()
	inProgress, probeErr := reparentRebaseInProgress(ctx)
	if probeErr != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, probeErr.Error())
	}
	if inProgress {
		return reparentRefusal(ReparentRefusalConflictUnresolved, fmt.Sprintf(
			"a rebase is still in progress in %s; finish it with: git -C %s rebase --continue", ctx, ctx))
	}
	for _, row := range r.State.RowsInOrder() {
		live := r.State.Row(row.Name)
		if live.Stage != ReparentRowPending {
			continue
		}
		if err := r.proveConflictResolution(live); err != nil {
			return err
		}
		if err := r.recordComputedTip(live); err != nil {
			return err
		}
		if err := r.pinComputedTip(live); err != nil {
			return err
		}
		if err := r.validateRow(live); err != nil {
			return err
		}
		break
	}
	return r.setStage(ReparentStageComputing)
}

// proveConflictResolution is the guard between "no rebase is in progress" and
// "HEAD is this row's computed tip".
//
// `git rebase --abort` also leaves no rebase in progress — and leaves HEAD
// back at the row's pre-image. Accepting that HEAD would record a replay that
// never happened, pin it, write it into stack.yaml and move a public ref to
// the value it already had, reporting success for an operation the operator
// explicitly cancelled. For a non-empty replay, completion therefore requires
// both HEAD != preimage and the resolved destination to be an ancestor of
// HEAD. Reflog advancement is persisted when available, but its prose and
// availability are not part of the proof because they vary across Git builds.
func (r *ReparentRun) proveConflictResolution(row *ReparentStateRow) error {
	if err := r.proveConflictCompletionEvidence(row); err != nil {
		return err
	}
	evidence, err := r.assessConflictResolution(row)
	if err != nil {
		if head, ok, headErr := reparentResolveRef(r.contextDir(), "HEAD"); headErr == nil && ok &&
			head == row.PreimageSHA {
			if h := r.computationHolder(); h != nil {
				h.DetachTargetSHA = head
				h.DetachStatus = ReparentHolderDetached
				h.Restored = false
				if saveErr := r.save(); saveErr != nil {
					return saveErr
				}
			}
		}
		return err
	}
	if h := r.computationHolder(); h != nil {
		h.DetachTargetSHA = strings.TrimPrefix(evidence, "native-completion-marker:")
		h.DetachStatus = ReparentHolderDetached
		h.Restored = false
	}
	row.ConflictCompletionEvidence = evidence
	return r.save()
}

func (r *ReparentRun) assessConflictResolution(row *ReparentStateRow) (string, error) {
	ctx := r.contextDir()
	residue, err := reparentContextResidue(ctx)
	if err != nil {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if len(residue) > 0 {
		return "", reparentEntryRefusal(ReparentRefusalConflictUnresolved, row.Name,
			fmt.Sprintf("the computation context is not clean after conflict handling: %s", strings.Join(residue, ", ")))
	}
	dest, err := r.rowDestination(row)
	if err != nil {
		return "", err
	}
	head, ok, err := reparentResolveRef(ctx, "HEAD")
	if err != nil || !ok {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("HEAD does not resolve in %s", ctx))
	}
	if row.CandidateCount > 0 && head == row.PreimageSHA {
		return "", reparentEntryRefusal(ReparentRefusalConflictUnresolved, row.Name,
			"HEAD returned to the row pre-image after a non-empty replay; native rebase --abort is not completion")
	}
	if row.ConflictCompletionRef == "" {
		return "", reparentEntryRefusal(ReparentRefusalConflictUnresolved, row.Name,
			"the conflicted replay has no persisted native completion marker")
	}
	completed, found, err := reparentResolveRef(r.RepoRoot, row.ConflictCompletionRef)
	if err != nil {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if !found || completed != head {
		return "", reparentEntryRefusal(ReparentRefusalConflictUnresolved, row.Name,
			"the conflicted replay did not reach its persisted native completion marker; git rebase --quit and git rebase --abort are not completion")
	}
	contains, err := reparentIsAncestor(ctx, dest, head)
	if err != nil {
		return "", reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
	}
	if contains {
		return "native-completion-marker:" + head, nil
	}
	return "", reparentEntryRefusal(ReparentRefusalConflictUnresolved, row.Name, fmt.Sprintf(
		"%s in %s is not a descendant of the destination %s, so the replay of %q was not completed — `git rebase --abort` cancels a conflicted replay rather than resolving it. Re-run the rebase in %s and finish it with `git -C %s rebase --continue`, or discard the whole reparent with: tws stack reparent %s --abort",
		head, ctx, dest, row.Name, ctx, ctx, r.Loc.Feature))
}

func (r *ReparentRun) installConflictCompletionMarker(row *ReparentStateRow) error {
	if row.ConflictCompletionRef == "" {
		row.ConflictCompletionRef = reparentConflictCompletionRef(
			r.State.RunID, ReparentEntryRefID(r.Loc.Feature, row.Name),
		)
	}
	res, err := runReparentGit(r.contextDir(), nil, "rev-parse", "--git-path", "rebase-merge/git-rebase-todo")
	if err != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("locate native rebase todo: %s", reparentGitMessage(res, err)))
	}
	path := strings.TrimSpace(string(res.Stdout))
	if path == "" {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			"native rebase todo path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.contextDir(), path)
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("inspect native rebase todo: %v", err))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			"native rebase todo is not a regular file")
	}
	line := "exec git update-ref " + row.ConflictCompletionRef + " HEAD"
	data, err := os.ReadFile(path)
	if err != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("read native rebase todo: %v", err))
	}
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(existing) == line {
			return nil
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
			fmt.Sprintf("open native rebase todo: %v", err))
	}
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	_, writeErr := file.WriteString(prefix + line + "\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, writeErr.Error())
	}
	if syncErr != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, syncErr.Error())
	}
	if closeErr != nil {
		return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, closeErr.Error())
	}
	return nil
}

// reparentRebaseInProgress reports whether the computation context is mid-rebase.
func reparentRebaseInProgress(dir string) (bool, error) {
	op, err := measureReparentGitOperation(dir)
	return op == "rebase", err
}

func reparentConflictReflog(dir string) ([]string, error) {
	res, err := runReparentGit(dir, nil, "reflog", "show", "--format=%H%x09%gs", "-n", "256", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read conflict reflog: %s", reparentGitMessage(res, err))
	}
	var lines []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("HEAD reflog is empty; conflict completion cannot be verified")
	}
	return lines, nil
}

func reparentConflictReflogAnchor(dir string) (string, error) {
	lines, err := reparentConflictReflog(dir)
	if err != nil {
		return "", err
	}
	return lines[0], nil
}

func (r *ReparentRun) proveConflictCompletionEvidence(row *ReparentStateRow) error {
	if row.ConflictReflogAnchor == "" {
		return nil
	}
	lines, err := reparentConflictReflog(r.contextDir())
	if err != nil {
		return nil
	}
	var newer []string
	found := false
	for _, line := range lines {
		if line == row.ConflictReflogAnchor {
			found = true
			break
		}
		newer = append(newer, line)
	}
	if !found {
		return nil
	}
	if len(newer) > 0 {
		row.ConflictCompletionEvidence = "reflog-advanced:" + newer[0]
		return r.save()
	}
	return nil
}

// runReparentValidation runs the frozen command through `sh -c`, exactly as
// the shipped validation runner does, but with exec.Cmd.Dir set to the
// computation worktree and both streams captured: no reparent child ever
// inherits this process's streams or blocks on a terminal.
func runReparentValidation(dir, command string) (stdout, stderr []byte, code int, err error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	code = 0
	if err != nil {
		code = gitExitCode(err)
	}
	return out.Bytes(), errBuf.Bytes(), code, err
}

// reparentContextResidue is the strict cleanliness probe used outside
// validation: any tracked modification or non-ignored untracked path.
func reparentContextResidue(dir string) ([]string, error) {
	dirty, err := measureReparentDirty(dir)
	if err != nil {
		return nil, err
	}
	untracked, err := measureReparentUntracked(dir)
	if err != nil {
		return nil, err
	}
	return append(dirty, untracked...), nil
}

// reparentValidationResidue refuses every tracked modification and only
// untracked paths that appeared after the validation command began.
func reparentValidationResidue(dir string, beforeUntracked []string) ([]string, error) {
	dirty, err := measureReparentDirty(dir)
	if err != nil {
		return nil, err
	}
	afterUntracked, err := measureReparentUntracked(dir)
	if err != nil {
		return nil, err
	}
	before := make(map[string]bool, len(beforeUntracked))
	for _, path := range beforeUntracked {
		before[path] = true
	}
	residue := append([]string{}, dirty...)
	for _, path := range afterUntracked {
		if !before[path] {
			residue = append(residue, path)
		}
	}
	return residue, nil
}

// reparentUntrackedGate is §9.4b, run before EVERY switch this run performs
// and before every holder re-attachment. `-f`, `--force`, `--discard-changes`
// and `--ignore-other-worktrees` are never passed to escape it.
func reparentUntrackedGate(dir, treeish string) error {
	res, err := runReparentGit(dir, nil, "ls-tree", "-r", "--name-only", treeish)
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, reparentGitMessage(res, err))
	}
	var tree []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			tree = append(tree, line)
		}
	}
	untracked, err := measureReparentUntracked(dir)
	if err != nil {
		// The gate cannot be evaluated, so the switch it guards must not run:
		// an unmeasured working tree is not a clean one.
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	var collisions []string
	seen := map[string]bool{}
	for _, untrackedPath := range untracked {
		for _, targetPath := range tree {
			if untrackedPath != targetPath &&
				!strings.HasPrefix(targetPath, untrackedPath+"/") &&
				!strings.HasPrefix(untrackedPath, targetPath+"/") {
				continue
			}
			pair := fmt.Sprintf("%s -> %s", untrackedPath, targetPath)
			if !seen[pair] {
				seen[pair] = true
				collisions = append(collisions, pair)
			}
		}
	}
	if len(collisions) == 0 {
		return nil
	}
	sort.Strings(collisions)
	return reparentRefusal(ReparentRefusalUntrackedOverwrite, fmt.Sprintf(
		"checking out %s in %s would overwrite untracked path(s) (untracked -> target): %s",
		treeish, dir, strings.Join(collisions, ", ")))
}

// stageBuildingPostImage builds and persists the EXACT post-image bytes
// before the CAS, so a crash between the CAS and the metadata write can
// always be completed forward from durable bytes.
func (r *ReparentRun) stageBuildingPostImage() error {
	for i := range r.State.Rows {
		row := &r.State.Rows[i]
		if row.Role == ReparentRoleTarget {
			row.LastBaseSHAAfter = r.State.NewParentSHA
			continue
		}
		parent := r.State.Row(row.DestinationParent)
		if parent == nil || parent.PlannedNewSHA == "" {
			return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
				"the parent row's computed tip is not recorded")
		}
		row.LastBaseSHAAfter = parent.PlannedNewSHA
	}
	data, err := r.buildPostImageBytes()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	r.State.StackAfterBase64 = base64.StdEncoding.EncodeToString(data)
	r.State.StackSHA256AfterExpected = hex.EncodeToString(sum[:])
	if err := r.save(); err != nil {
		return err
	}
	if err := reparentStep(ReparentStageBuildingPostImage, "post-image-persisted"); err != nil {
		return err
	}
	return r.setStage(ReparentStagePinningComputed)
}

// buildPostImageBytes applies §10.1's exact post-state to the captured
// pre-image bytes: the target's Base becomes the stored token and its
// LastBaseSHA the pinned destination; every descendant keeps its Base and
// takes its parent row's post-replay tip. No other field and no entry
// outside the closure is touched, and Stack.Branches keeps its order.
func (r *ReparentRun) buildPostImageBytes() ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(r.State.StackBeforeBase64)
	if err != nil {
		return nil, err
	}
	var stack Stack
	if err := yaml.Unmarshal(raw, &stack); err != nil {
		return nil, err
	}
	for i := range stack.Branches {
		row := r.State.Row(stack.Branches[i].Name)
		if row == nil {
			continue
		}
		if row.Role == ReparentRoleTarget {
			stack.Branches[i].Base = row.BaseAfter
			stack.Branches[i].LastBaseSHA = r.State.NewParentSHA
			continue
		}
		parent := r.State.Row(row.DestinationParent)
		if parent == nil || parent.PlannedNewSHA == "" {
			return nil, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name,
				"the parent row's computed tip is not recorded")
		}
		stack.Branches[i].LastBaseSHA = parent.PlannedNewSHA
	}
	return yaml.Marshal(&stack)
}

// stagePinningComputed is the idempotent verify-all gate of §9.8: every new
// pin must exist and resolve to its recorded planned tip. A missing pin is
// re-created; a pin that resolves elsewhere refuses probe-failed.
func (r *ReparentRun) stagePinningComputed() error {
	for _, row := range r.State.RowsInOrder() {
		if _, err := reparentVerifyPin(r.RepoRoot, row.NewPinRef, row.PlannedNewSHA); err != nil {
			return reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
		}
	}
	if err := reparentStep(ReparentStagePinningComputed, "pins-verified"); err != nil {
		return err
	}
	return r.setStage(ReparentStageWritingRemoteRecord)
}

// stageWritingRemoteRecord writes the pending follow-up record AFTER every
// row is computed, validated and pinned, and BEFORE holder detachment and the
// CAS. Writing it after a successful CAS would leave a window in which public
// refs have been rewritten and nothing records it; writing it first inverts
// the failure into a harmless one an abort cleans up.
func (r *ReparentRun) stageWritingRemoteRecord() error {
	if !r.State.RemoteRecordBeforeCaptured {
		before, present, err := ReadReparentRemoteRecordBytes(r.Loc)
		if err != nil {
			return err
		}
		r.State.RemoteRecordBeforeCaptured = true
		r.State.RemoteRecordBeforePresent = present
		if present {
			r.State.RemoteRecordBeforeBase64 = base64.StdEncoding.EncodeToString(before)
		}
		// Persist the prior protection BEFORE replacing it. A crash after this
		// write can always restore the exact previous record.
		if err := r.save(); err != nil {
			return err
		}
	}
	rec := ReparentRemoteRecord{
		RecordVersion: ReparentRemoteRecordVersion,
		Feature:       r.Loc.Feature,
		RunID:         r.State.RunID,
		CreatedAt:     reparentNow(),
	}
	for _, row := range r.State.RowsInOrder() {
		remoteRef := "refs/remotes/" + ReparentRemoteName + "/" + row.GitBranch
		observed, ok, err := reparentResolveRef(r.RepoRoot, remoteRef)
		if err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
		}
		if !ok {
			observed = ""
		}
		entry := ReparentRemoteEntry{
			Name:             row.Name,
			GitBranch:        row.GitBranch,
			Repo:             row.Repo,
			Remote:           ReparentRemoteName,
			RemoteRef:        remoteRef,
			NewTipSHA:        row.PlannedNewSHA,
			RemoteSHAAtWrite: observed,
			State:            reparentRemoteInitialState(observed),
		}
		if row.Role == ReparentRoleTarget {
			entry.PRBaseBefore = reparentPRBaseParentToken(r.State.OldParentRef, r.State.OldParentStoredToken)
			entry.PRBaseAfter = reparentPRBaseParentToken(r.State.NewParentRef, r.State.NewParentStoredToken)
		} else if parent := r.State.Row(row.DestinationParent); parent != nil {
			entry.PRBaseBefore = parent.GitBranch
			entry.PRBaseAfter = parent.GitBranch
		}
		rec.Entries = append(rec.Entries, entry)
	}

	existing, err := LoadReparentRemoteRecord(r.Loc)
	if err != nil {
		return err
	}
	merged := MergeReparentRemoteRecord(existing, rec)
	protected := reparentRemotePendingForRows(merged, r.State.RowsInOrder())
	if merged.AllCleared() {
		// Every entry — this run's and any older run's — was written cleared,
		// so there is nothing for a push to warn about. §12.3 requires the
		// file to be DELETED when every entry is cleared, and writing one
		// first would leave a permanent all-cleared record that every later
		// route would have to read only to delete. The CAS gate is satisfied
		// by the "provably empty" half of §12.3a instead.
		if err := RemoveReparentRemoteRecord(r.Loc); err != nil {
			return err
		}
		r.State.RemoteRecordWritten = false
		r.State.RemoteRecordEmpty = true
		r.State.RemoteFollowupEntries = []string{}
	} else {
		if err := SaveReparentRemoteRecord(r.Loc, merged); err != nil {
			return err
		}
		r.State.RemoteRecordWritten = len(protected) > 0
		r.State.RemoteRecordEmpty = len(protected) == 0
		r.State.RemoteFollowupEntries = protected
	}
	if err := r.save(); err != nil {
		return err
	}
	if err := reparentStep(ReparentStageWritingRemoteRecord, "record-written"); err != nil {
		return err
	}
	return r.setStage(ReparentStageDetachingHolders)
}

func reparentPRBaseToken(token string) string {
	if strings.HasPrefix(token, "refs/heads/") {
		return strings.TrimPrefix(token, "refs/heads/")
	}
	if strings.HasPrefix(token, "refs/remotes/") {
		parts := strings.SplitN(strings.TrimPrefix(token, "refs/remotes/"), "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	if strings.HasPrefix(token, "refs/") || reparentHexToken.MatchString(token) {
		return ""
	}
	return token
}

func reparentPRBaseParentToken(ref, storedToken string) string {
	if ref != "" {
		return reparentPRBaseToken(ref)
	}
	return reparentPRBaseToken(storedToken)
}

// stageDetachingHolders detaches every worktree that still holds an affected
// branch. Checkout mode's own computation context is excluded: §9.3 detached
// it at the start of the run, and re-checking it here would compare a
// deliberately detached HEAD against an attached-branch expectation and
// refuse holder-unsafe on every checkout-mode run.
func (r *ReparentRun) stageDetachingHolders() error {
	if len(r.State.Rows) > 0 && !r.State.RemoteRecordWritten && !r.State.RemoteRecordEmpty {
		return reparentRefusal(ReparentRefusalProbeFailed,
			"the remote follow-up record must be durable, or provably empty, before any public ref moves")
	}
	if err := r.detachAffectedHolders(); err != nil {
		return err
	}
	if err := reparentStep(ReparentStageDetachingHolders, "holders-detached"); err != nil {
		return err
	}
	return r.setStage(ReparentStageCommittingRefs)
}

// detachAffectedHolders detaches every worktree currently attached to an
// affected branch, skipping the scratch and checkout mode's own computation
// context. It is IDEMPOTENT and is deliberately re-run before every forward
// CAS: a holder this run already detached and then re-attached — which is
// exactly what a failed transaction's restore does — is attached again, and
// moving a ref out from under it would leave its index and HEAD inconsistent.
func (r *ReparentRun) detachAffectedHolders() error {
	if err := r.reconcileHolderDetachIntents(); err != nil {
		return err
	}
	if !reparentStateHolderRestorationComplete(r.State) {
		if err := r.verifyPersistedHolderSnapshot(); err != nil {
			return err
		}
	}
	inv := reparentWorktreeInventory(r.RepoRoot)
	if !inv.Available {
		detail := "the worktree inventory is unavailable immediately before the ref transaction"
		if inv.Err != nil {
			detail += ": " + inv.Err.Error()
		}
		return reparentRefusal(ReparentRefusalHolderUnsafe, detail)
	}
	for _, rec := range inv.Records {
		if r.State.ScratchPath != "" && canonicalize(rec.Path) == canonicalize(r.State.ScratchPath) {
			continue
		}
		if rec.BranchRef != nil {
			continue
		}
		op, err := measureReparentGitOperation(rec.Path)
		if err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
		}
		if op != "" {
			return reparentRefusal(ReparentRefusalGitOperationInProgress, fmt.Sprintf(
				"a %s is in progress in detached worktree %s; it cannot be safely associated before the ref transaction", op, rec.Path))
		}
	}
	for _, row := range r.State.RowsInOrder() {
		for _, rec := range inv.Records {
			if rec.BranchRef == nil || strings.TrimPrefix(*rec.BranchRef, "refs/heads/") != row.GitBranch {
				continue
			}
			if r.State.ScratchPath != "" && canonicalize(rec.Path) == canonicalize(r.State.ScratchPath) {
				continue
			}
			if r.State.PreimageHolderExcluded != "" && canonicalize(rec.Path) == canonicalize(r.State.PreimageHolderExcluded) {
				continue
			}
			if rec.Prunable {
				return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
					"%s is held by prunable worktree %s", row.GitBranch, rec.Path))
			}
			dirty, err := measureReparentDirty(rec.Path)
			if err != nil {
				return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
			}
			if len(dirty) > 0 {
				return reparentRefusal(ReparentRefusalContextDirty, fmt.Sprintf(
					"affected holder %s has tracked modifications: %s", rec.Path, strings.Join(dirty, ", ")))
			}
			op, err := measureReparentGitOperation(rec.Path)
			if err != nil {
				return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
			}
			if op != "" {
				return reparentRefusal(ReparentRefusalGitOperationInProgress, fmt.Sprintf(
					"a %s is in progress in affected holder %s", op, rec.Path))
			}
			head, ok, err := reparentResolveRef(rec.Path, "HEAD")
			if err != nil {
				return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
			}
			// The expectation depends on WHERE the run is. Before the
			// transaction a holder must still be at its pre-image; after a
			// failed attempt it may legitimately be back on the branch at
			// either the pre-image or the planned tip, because this run put
			// it there. Anything else is somebody else's work.
			if !ok || (head != row.PreimageSHA && head != row.PlannedNewSHA) {
				return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
					"%s holds %s at %s, not the pre-image %s", rec.Path, row.GitBranch, head, row.PreimageSHA))
			}
			if err := reparentUntrackedGate(rec.Path, head); err != nil {
				return err
			}
			h := r.recordHolderDetachIntent(rec.Path, row.GitBranch, row.PreimageSHA, head, "linked-worktree", ReparentHolderDetachIntent)
			if canonicalize(rec.Path) == canonicalize(r.RepoRoot) {
				h.HolderKind = "primary-checkout"
			}
			if err := r.beginHolderSwitch(h, ReparentHolderSwitchDetach, "", head); err != nil {
				return err
			}
			if err := reparentStep(ReparentStageDetachingHolders, "holder-detach-intent-written:"+rec.Path); err != nil {
				return err
			}
			if res, err := runReparentGit(rec.Path, nil, "switch", "--detach", head); err != nil {
				return reparentRefusal(ReparentRefusalHolderUnsafe, reparentGitMessage(res, err))
			}
			if err := r.completeHolderSwitch(h); err != nil {
				return err
			}
			if err := reparentStep(ReparentStageDetachingHolders, "holder-detached-before-complete:"+rec.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

type reparentHolderPosition struct {
	Branch string
	SHA    string
}

func measureReparentHolderPosition(path string) (reparentHolderPosition, error) {
	res, err := runReparentGit(path, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch := ""
	if err == nil {
		branch = strings.TrimSpace(string(res.Stdout))
	} else if res.ExitCode != 1 {
		return reparentHolderPosition{}, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("inspect holder attachment at %s: %s", path, reparentGitMessage(res, err)))
	}
	head, ok, resolveErr := reparentResolveRef(path, "HEAD")
	if resolveErr != nil || !ok {
		return reparentHolderPosition{}, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("holder %s has no resolvable HEAD", path))
	}
	return reparentHolderPosition{Branch: branch, SHA: head}, nil
}

func reparentHolderPositionMatches(pos reparentHolderPosition, branch, sha string) bool {
	return pos.Branch == branch && pos.SHA == sha
}

// holderRestoreDestinationMatches classifies a restore destination without
// changing holder state. Before the commit point only the exact persisted
// destination is accepted. After it, an attached holder may be at the live
// tip of the expected branch because later branch movement is operator work
// that must not wedge forward cleanup. Detached restore destinations remain
// exact in both cases.
func (r *ReparentRun) holderRestoreDestinationMatches(h *ReparentStateHolder, pos reparentHolderPosition, branch, sha string) (bool, error) {
	if branch == "" {
		return reparentHolderPositionMatches(pos, branch, sha), nil
	}
	if pos.Branch != branch {
		return false, nil
	}
	live, found, err := reparentResolveRef(h.Path, "refs/heads/"+branch)
	if err != nil {
		return false, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("inspect live restore destination %s for holder %s: %v", branch, h.Path, err))
	}
	if !found || pos.SHA != live {
		return false, nil
	}
	if pos.SHA == sha {
		return true, nil
	}
	return r != nil && r.State != nil && r.State.CommitPointReached, nil
}

func (r *ReparentRun) holderManualRestoreMatches(h *ReparentStateHolder, pos reparentHolderPosition) (bool, error) {
	if pos.Branch != h.GitBranch {
		return false, nil
	}
	live, found, err := reparentResolveRef(h.Path, "refs/heads/"+h.GitBranch)
	if err != nil {
		return false, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("inspect live branch %s for holder %s: %v", h.GitBranch, h.Path, err))
	}
	if !found || pos.SHA != live {
		return false, nil
	}
	if r.State.CommitPointReached {
		return true, nil
	}
	return r.holderHeadWasRecorded(h, pos.SHA), nil
}

func (r *ReparentRun) verifyInferredHolderRestorationClean(h *ReparentStateHolder) error {
	dirty, err := measureReparentDirty(h.Path)
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	if len(dirty) > 0 {
		return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
			"manually restored holder %s has tracked modifications: %s",
			h.Path, strings.Join(dirty, ", ")))
	}
	op, err := measureReparentGitOperation(h.Path)
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	if op != "" {
		return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
			"manually restored holder %s has a %s in progress", h.Path, op))
	}
	return nil
}

func (r *ReparentRun) holderRestoreIsDeferred(path string) bool {
	want := canonicalize(path)
	for _, deferred := range r.State.HolderRestoreDeferred {
		if canonicalize(deferred) == want {
			return true
		}
	}
	return false
}

func (r *ReparentRun) beginHolderSwitch(h *ReparentStateHolder, action, destinationBranch, destinationSHA string) error {
	if h == nil || destinationSHA == "" {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"holder switch intent is missing its holder or destination")
	}
	if h.SwitchIntent != nil {
		if err := r.reconcileHolderSwitchIntents(); err != nil {
			return err
		}
		if h.SwitchIntent != nil {
			if h.SwitchIntent.Action == action &&
				h.SwitchIntent.DestinationBranch == destinationBranch &&
				h.SwitchIntent.DestinationSHA == destinationSHA {
				return nil
			}
			// The prior switch is still on its exact source side (otherwise
			// reconciliation would have completed or refused it). Recovery is
			// changing direction, most notably --abort cancelling a pending
			// computation detach, so replace that intent before doing anything.
			h.SwitchIntent = nil
		}
	}
	source, err := measureReparentHolderPosition(h.Path)
	if err != nil {
		return err
	}
	h.SwitchIntent = &ReparentHolderSwitchIntent{
		Action:            action,
		SourceBranch:      source.Branch,
		SourceSHA:         source.SHA,
		DestinationBranch: destinationBranch,
		DestinationSHA:    destinationSHA,
	}
	return r.save()
}

func applyReparentHolderSwitchCompletion(h *ReparentStateHolder) {
	intent := h.SwitchIntent
	if intent == nil {
		return
	}
	switch intent.Action {
	case ReparentHolderSwitchDetach, ReparentHolderSwitchCompute:
		h.DetachTargetSHA = intent.DestinationSHA
		h.DetachStatus = ReparentHolderDetached
		h.Restored = false
	case ReparentHolderSwitchRedetach:
		h.DetachTargetSHA = intent.DestinationSHA
		h.DetachStatus = ReparentHolderRedetached
		h.Restored = false
	case ReparentHolderSwitchRestore:
		h.Restored = true
		h.RestoreReason = ""
	}
	h.SwitchIntent = nil
}

func (r *ReparentRun) completeHolderSwitch(h *ReparentStateHolder) error {
	if h == nil || h.SwitchIntent == nil {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"holder switch completed without a durable intent")
	}
	pos, err := measureReparentHolderPosition(h.Path)
	if err != nil {
		return err
	}
	intent := h.SwitchIntent
	matches, err := r.holderRestoreDestinationMatches(h, pos, intent.DestinationBranch, intent.DestinationSHA)
	if err != nil {
		return err
	}
	if !matches {
		return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
			"holder %s is at branch %q HEAD %s after switch, expected branch %q HEAD %s",
			h.Path, pos.Branch, pos.SHA, intent.DestinationBranch, intent.DestinationSHA))
	}
	if intent.Action == ReparentHolderSwitchRestore {
		if err := r.verifyInferredHolderRestorationClean(h); err != nil {
			return err
		}
	}
	applyReparentHolderSwitchCompletion(h)
	return r.save()
}

func (r *ReparentRun) reconcileHolderSwitchIntents() error {
	changed := false
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if h.SwitchIntent == nil {
			continue
		}
		if common := reparentCommonDir(h.Path); common == "" || common != h.RepoCommonDir {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("holder %s changed repository identity while a switch was pending", h.Path))
		}
		pos, err := measureReparentHolderPosition(h.Path)
		if err != nil {
			return err
		}
		intent := h.SwitchIntent
		destinationMatches, matchErr := r.holderRestoreDestinationMatches(
			h, pos, intent.DestinationBranch, intent.DestinationSHA,
		)
		if matchErr != nil {
			return matchErr
		}
		switch {
		case destinationMatches:
			if intent.Action == ReparentHolderSwitchRestore {
				if err := r.verifyInferredHolderRestorationClean(h); err != nil {
					return err
				}
			}
			applyReparentHolderSwitchCompletion(h)
			changed = true
		case reparentHolderPositionMatches(pos, intent.SourceBranch, intent.SourceSHA):
			// The switch never ran. The owning stage retries it.
		default:
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"holder %s is at branch %q HEAD %s, neither switch source %q/%s nor destination %q/%s",
				h.Path, pos.Branch, pos.SHA,
				intent.SourceBranch, intent.SourceSHA,
				intent.DestinationBranch, intent.DestinationSHA))
		}
	}
	if changed {
		return r.save()
	}
	return nil
}

func (r *ReparentRun) reconcileManualHolderRestorations() error {
	if len(r.State.HolderRestoreDeferred) == 0 {
		return nil
	}
	deferred := make(map[string]bool, len(r.State.HolderRestoreDeferred))
	for _, path := range r.State.HolderRestoreDeferred {
		deferred[canonicalize(path)] = true
	}
	changed := false
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if h.Restored || h.SwitchIntent != nil || !deferred[canonicalize(h.Path)] {
			continue
		}
		if common := reparentCommonDir(h.Path); common == "" || common != h.RepoCommonDir {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("manually restored holder %s is in the wrong repository", h.Path))
		}
		pos, err := measureReparentHolderPosition(h.Path)
		if err != nil {
			return err
		}
		if pos.Branch == "" {
			continue
		}
		if pos.Branch != h.GitBranch {
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"manually restored holder %s is attached to %s, expected %s",
				h.Path, pos.Branch, h.GitBranch))
		}
		allowed, matchErr := r.holderManualRestoreMatches(h, pos)
		if matchErr != nil {
			return matchErr
		}
		if !allowed {
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"manually restored holder %s is at unexpected HEAD %s", h.Path, pos.SHA))
		}
		if err := r.verifyInferredHolderRestorationClean(h); err != nil {
			return err
		}
		h.Restored = true
		h.RestoreReason = ""
		delete(deferred, canonicalize(h.Path))
		changed = true
	}
	if !changed {
		return nil
	}
	kept := r.State.HolderRestoreDeferred[:0]
	for _, path := range r.State.HolderRestoreDeferred {
		if deferred[canonicalize(path)] {
			kept = append(kept, path)
		}
	}
	r.State.HolderRestoreDeferred = kept
	return r.save()
}

func (r *ReparentRun) reconcileHolderDetachIntents() error {
	if err := r.reconcileHolderSwitchIntents(); err != nil {
		return err
	}
	changed := false
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if h.SwitchIntent != nil {
			continue
		}
		if h.DetachStatus != ReparentHolderDetachIntent && h.DetachStatus != ReparentHolderRedetachIntent {
			continue
		}
		res, err := runReparentGit(h.Path, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
		switch {
		case err == nil:
			attached := strings.TrimSpace(string(res.Stdout))
			if h.GitBranch != "" && attached != h.GitBranch {
				return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
					"holder %s attached to %s while detachment of %s was pending", h.Path, attached, h.GitBranch))
			}
			// The switch did not happen. The owning stage retries it.
			continue
		case res.ExitCode != 1:
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("inspect holder attachment at %s: %s", h.Path, reparentGitMessage(res, err)))
		}
		head, ok, resolveErr := reparentResolveRef(h.Path, "HEAD")
		if resolveErr != nil || !ok {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("detached holder %s has no resolvable HEAD", h.Path))
		}
		expected := h.DetachTargetSHA
		if expected == "" {
			expected = h.PreimageSHA
		}
		if h.HolderKind == "computation-context" &&
			h.DetachStatus == ReparentHolderDetachIntent &&
			r.State.OriginalDetached &&
			r.State.OriginalHead != "" &&
			head == r.State.OriginalHead {
			// The intent was durable but the initial `switch --detach` had not
			// executed yet. The owning preflight stage retries it; abort may
			// restore the same detached HEAD idempotently.
			continue
		}
		if expected != "" && head != expected {
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"detached holder %s is at %s, not its persisted detachment target %s", h.Path, head, expected))
		}
		if h.DetachStatus == ReparentHolderRedetachIntent {
			h.DetachStatus = ReparentHolderRedetached
		} else {
			h.DetachStatus = ReparentHolderDetached
		}
		h.Restored = false
		changed = true
	}
	if changed {
		return r.save()
	}
	return nil
}

// recordDetachedHolder adds or REFRESHES a holder record. A holder this run
// restored and has now re-detached must go back to restored:false, or the
// restore stage would skip it and leave the operator's worktree detached.
func (r *ReparentRun) recordHolderDetachIntent(path, branch, preimage, target, kind, status string) *ReparentStateHolder {
	for i := range r.State.DetachedHolders {
		if canonicalize(r.State.DetachedHolders[i].Path) != canonicalize(path) {
			continue
		}
		h := &r.State.DetachedHolders[i]
		h.RepoCommonDir = reparentCommonDir(path)
		h.GitBranch = branch
		h.PreimageSHA = preimage
		h.DetachTargetSHA = target
		h.HolderKind = kind
		h.DetachStatus = status
		h.RestoreReason = ""
		return h
	}
	r.State.DetachedHolders = append(r.State.DetachedHolders, ReparentStateHolder{
		Path: path, RepoCommonDir: reparentCommonDir(path),
		GitBranch: branch, PreimageSHA: preimage, DetachTargetSHA: target,
		HolderKind: kind, DetachStatus: status,
	})
	return &r.State.DetachedHolders[len(r.State.DetachedHolders)-1]
}

func (r *ReparentRun) computationHolder() *ReparentStateHolder {
	for i := range r.State.DetachedHolders {
		if r.State.DetachedHolders[i].HolderKind == "computation-context" {
			return &r.State.DetachedHolders[i]
		}
	}
	return nil
}

func (r *ReparentRun) setComputationDetachTarget(target string) error {
	if target == "" {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"the computation context cannot record an empty detachment target")
	}
	h := r.computationHolder()
	if h == nil {
		branch := ""
		preimage := target
		if r.Loc.Mode == ModeCheckout {
			branch = r.State.OriginalBranch
			preimage = r.State.OriginalHead
		}
		h = r.recordHolderDetachIntent(
			r.contextDir(), branch, preimage, target,
			"computation-context", ReparentHolderDetached,
		)
	}
	h.RepoCommonDir = r.State.RepoCommonDir
	h.DetachTargetSHA = target
	h.DetachStatus = ReparentHolderDetached
	h.Restored = false
	h.RestoreReason = ""
	h.SwitchIntent = nil
	return r.save()
}

func (r *ReparentRun) beginComputationSwitch(target string) (*ReparentStateHolder, error) {
	if target == "" {
		return nil, reparentRefusal(ReparentRefusalStateCorrupt,
			"the computation context cannot switch to an empty target")
	}
	h := r.computationHolder()
	if h == nil {
		branch := ""
		preimage := target
		if r.Loc.Mode == ModeCheckout {
			branch = r.State.OriginalBranch
			preimage = r.State.OriginalHead
		}
		h = r.recordHolderDetachIntent(
			r.contextDir(), branch, preimage, target,
			"computation-context", ReparentHolderDetached,
		)
	}
	h.RepoCommonDir = r.State.RepoCommonDir
	if err := r.beginHolderSwitch(h, ReparentHolderSwitchCompute, "", target); err != nil {
		return nil, err
	}
	return h, nil
}

func (r *ReparentRun) approvedHolder(path string) *ReparentApprovedHolder {
	want := canonicalize(path)
	for i := range r.State.ApprovedHolders {
		if canonicalize(r.State.ApprovedHolders[i].Path) == want {
			return &r.State.ApprovedHolders[i]
		}
	}
	return nil
}

func (r *ReparentRun) detachedHolder(path string) *ReparentStateHolder {
	want := canonicalize(path)
	for i := range r.State.DetachedHolders {
		if canonicalize(r.State.DetachedHolders[i].Path) == want {
			return &r.State.DetachedHolders[i]
		}
	}
	return nil
}

func reparentStageNeedsHolderVerification(stage ReparentStage) bool {
	switch stage {
	case ReparentStageCleanup, ReparentStageCompleted, ReparentStageAborted:
		return false
	default:
		return true
	}
}

func reparentStateHolderRestorationComplete(st *ReparentState) bool {
	if st == nil || st.Stage != ReparentStageRestoringHolders || len(st.HolderRestoreDeferred) > 0 {
		return false
	}
	for _, holder := range st.DetachedHolders {
		if holder.HolderKind == "computation-context" && st.WorkspaceMode == string(ModeExternal) {
			continue
		}
		if !holder.Restored {
			return false
		}
	}
	return true
}

func (r *ReparentRun) verifyPersistedHolderSnapshot() error {
	inv := reparentWorktreeInventory(r.RepoRoot)
	if !inv.Available {
		detail := "the worktree inventory is unavailable while verifying the approved holder snapshot"
		if inv.Err != nil {
			detail += ": " + inv.Err.Error()
		}
		return reparentRefusal(ReparentRefusalHolderUnsafe, detail)
	}
	byPath := make(map[string]WorktreeRecord, len(inv.Records))
	for _, rec := range inv.Records {
		byPath[canonicalize(rec.Path)] = rec
	}
	for i := range r.State.ApprovedHolders {
		approved := &r.State.ApprovedHolders[i]
		rec, ok := byPath[canonicalize(approved.Path)]
		if !ok {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("approved holder %s is no longer registered", approved.Path))
		}
		if rec.Prunable {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("approved holder %s became prunable", approved.Path))
		}
		common := reparentCommonDir(rec.Path)
		if common == "" {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("approved holder %s repository identity could not be measured", approved.Path))
		}
		if common != approved.RepoCommonDir {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("approved holder %s changed repository identity", approved.Path))
		}
		if detached := r.detachedHolder(approved.Path); detached != nil {
			if err := r.verifyRecordedHolderPosition(rec, detached); err != nil {
				return err
			}
			continue
		}
		head, ok, err := reparentResolveRef(rec.Path, "HEAD")
		if err != nil || !ok {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("approved holder %s has no resolvable HEAD", approved.Path))
		}
		if approved.Excluded && r.State.OriginalDetached {
			if rec.BranchRef != nil || head != approved.HeadSHA {
				return reparentRefusal(ReparentRefusalHolderUnsafe,
					fmt.Sprintf("approved checkout context %s drifted from detached HEAD %s", approved.Path, approved.HeadSHA))
			}
			continue
		}
		if rec.BranchRef == nil ||
			strings.TrimPrefix(*rec.BranchRef, "refs/heads/") != approved.GitBranch ||
			head != approved.HeadSHA {
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"approved holder %s no longer holds %s at %s", approved.Path, approved.GitBranch, approved.HeadSHA))
		}
	}

	affected := make(map[string]bool, len(r.State.Rows))
	for _, row := range r.State.Rows {
		affected[row.GitBranch] = true
	}
	for _, rec := range inv.Records {
		if rec.BranchRef == nil {
			continue
		}
		branch := strings.TrimPrefix(*rec.BranchRef, "refs/heads/")
		if !affected[branch] {
			continue
		}
		if r.State.ScratchPath != "" && canonicalize(rec.Path) == canonicalize(r.State.ScratchPath) {
			continue
		}
		if r.approvedHolder(rec.Path) == nil {
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"%s acquired unapproved holder %s after the plan was approved", branch, rec.Path))
		}
	}
	if h := r.computationHolder(); h != nil {
		rec, ok := byPath[canonicalize(h.Path)]
		if !ok {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("computation context %s is no longer registered", h.Path))
		}
		if err := r.verifyRecordedHolderPosition(rec, h); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReparentRun) verifyRecordedHolderPosition(rec WorktreeRecord, h *ReparentStateHolder) error {
	common := reparentCommonDir(rec.Path)
	if common == "" {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("recorded holder %s repository identity could not be measured", h.Path))
	}
	if common != h.RepoCommonDir {
		return reparentRefusal(ReparentRefusalHolderUnsafe,
			fmt.Sprintf("recorded holder %s changed repository identity", h.Path))
	}
	head, ok, err := reparentResolveRef(rec.Path, "HEAD")
	if err != nil || !ok {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("recorded holder %s has no resolvable HEAD", h.Path))
	}
	if h.SwitchIntent != nil {
		branch := ""
		if rec.BranchRef != nil {
			branch = strings.TrimPrefix(*rec.BranchRef, "refs/heads/")
		}
		pos := reparentHolderPosition{Branch: branch, SHA: head}
		destinationMatches, matchErr := r.holderRestoreDestinationMatches(
			h, pos, h.SwitchIntent.DestinationBranch, h.SwitchIntent.DestinationSHA,
		)
		if matchErr != nil {
			return matchErr
		}
		if reparentHolderPositionMatches(pos, h.SwitchIntent.SourceBranch, h.SwitchIntent.SourceSHA) {
			return nil
		}
		if destinationMatches {
			if h.SwitchIntent.Action == ReparentHolderSwitchRestore {
				if err := r.verifyInferredHolderRestorationClean(h); err != nil {
					return err
				}
			}
			return nil
		}
		return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
			"holder %s is at branch %q HEAD %s, neither persisted switch side", h.Path, branch, head))
	}
	if h.HolderKind == "computation-context" &&
		h.DetachStatus == ReparentHolderDetachIntent &&
		head == h.PreimageSHA {
		if rec.BranchRef == nil ||
			strings.TrimPrefix(*rec.BranchRef, "refs/heads/") == h.GitBranch {
			return nil
		}
	}
	if !h.Restored {
		if rec.BranchRef == nil && h.DetachTargetSHA != "" && head == h.DetachTargetSHA {
			return nil
		}
		if h.HolderKind == "computation-context" &&
			(r.State.Stage == ReparentStageComputing || r.State.Stage == ReparentStageConflictPaused) &&
			rec.BranchRef == nil {
			if active, activeErr := reparentRebaseInProgress(h.Path); activeErr == nil && active {
				return nil
			}
			for _, row := range r.State.RowsInOrder() {
				live := r.State.Row(row.Name)
				if live.Stage == ReparentRowPending && head == live.PreimageSHA {
					return nil
				}
			}
		}
		if h.HolderKind == "computation-context" &&
			r.State.Stage == ReparentStageConflictPaused &&
			rec.BranchRef == nil {
			for _, row := range r.State.RowsInOrder() {
				live := r.State.Row(row.Name)
				if live.Stage != ReparentRowPending || live.ConflictCompletionRef == "" {
					continue
				}
				completed, found, markerErr := reparentResolveRef(r.RepoRoot, live.ConflictCompletionRef)
				if markerErr == nil && found && completed == head {
					return nil
				}
				break
			}
		}
		if r.holderRestoreIsDeferred(h.Path) && rec.BranchRef != nil {
			branch := strings.TrimPrefix(*rec.BranchRef, "refs/heads/")
			pos := reparentHolderPosition{Branch: branch, SHA: head}
			matches, matchErr := r.holderManualRestoreMatches(h, pos)
			if matchErr != nil {
				return matchErr
			}
			if matches {
				return r.verifyInferredHolderRestorationClean(h)
			}
		}
		return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
			"detached holder %s is at %s, not its recorded target %s", h.Path, head, h.DetachTargetSHA))
	}
	if (h.DetachStatus == ReparentHolderDetachIntent ||
		h.DetachStatus == ReparentHolderRedetachIntent) &&
		rec.BranchRef == nil && h.DetachTargetSHA != "" && head == h.DetachTargetSHA {
		return nil
	}
	if h.HolderKind == "computation-context" && r.State.OriginalDetached {
		if rec.BranchRef != nil || head != r.State.OriginalHead {
			return reparentRefusal(ReparentRefusalHolderUnsafe,
				fmt.Sprintf("restored computation context %s drifted from original detached HEAD %s", h.Path, r.State.OriginalHead))
		}
		return nil
	}
	if rec.BranchRef == nil || strings.TrimPrefix(*rec.BranchRef, "refs/heads/") != h.GitBranch {
		return reparentRefusal(ReparentRefusalHolderUnsafe,
			fmt.Sprintf("restored holder %s is not attached to recorded branch %s", h.Path, h.GitBranch))
	}
	pos := reparentHolderPosition{
		Branch: strings.TrimPrefix(*rec.BranchRef, "refs/heads/"),
		SHA:    head,
	}
	matches, matchErr := r.holderManualRestoreMatches(h, pos)
	if matchErr != nil {
		return matchErr
	}
	if !matches {
		return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
			"restored holder %s is at unrecorded branch head %s", h.Path, head))
	}
	return nil
}

func (r *ReparentRun) holderHeadWasRecorded(h *ReparentStateHolder, head string) bool {
	if head == h.PreimageSHA || head == r.State.OriginalHead {
		return true
	}
	for _, row := range r.State.Rows {
		if row.GitBranch == h.GitBranch && row.PlannedNewSHA != "" && head == row.PlannedNewSHA {
			return true
		}
	}
	return false
}

// stageCommittingRefs classifies EVERY affected ref first, then commits the
// remaining work in one CAS transaction.
func (r *ReparentRun) stageCommittingRefs() error {
	rows := r.State.RowsInOrder()
	if err := reparentRequireAllDirectBranchRefs(r.RepoRoot, rows); err != nil {
		return err
	}
	classes, err := ClassifyReparentRefs(r.RepoRoot, rows)
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	for _, c := range classes {
		if c.Class == ReparentRefForeign && !r.State.CommitPointReached {
			return reparentEntryRefusal(ReparentRefusalRefForeignValue, c.Name, reparentForeignRefDetail(c))
		}
	}
	// An all-planned/no-op classification is NOT by itself permission to skip
	// the transaction. A fresh closure whose every row computed to its own
	// pre-image classifies exactly that way while owing Git the whole
	// envelope: §9.11 requires start / one ordered verify per row / prepare /
	// commit even then, because the verification is the race barrier. Only
	// PERSISTED evidence that this run already issued the transaction lets it
	// be skipped.
	if r.State.CASTransactionSucceeded && reparentAllPlannedOrNoop(classes) {
		return r.setStage(ReparentStageRefsCommitted)
	}
	partial := false
	for _, c := range classes {
		if c.Class == ReparentRefPlannedTip {
			partial = true
		}
	}
	if partial {
		r.prose("reparent-recovery: %s: completing the rows whose update did not land\n", ReparentRecoveryPartialToken)
	}

	// The detachment invariant is re-asserted immediately before the child
	// runs, on EVERY attempt. A previous attempt that failed re-attached every
	// holder it had detached (so the operator was not left with a detached
	// worktree), which means a resumed run would otherwise move refs out from
	// under attached worktrees.
	if err := r.redetachRestoredHolders(); err != nil {
		return err
	}
	if err := r.detachAffectedHolders(); err != nil {
		return err
	}
	if err := r.verifyPersistedHolderSnapshot(); err != nil {
		return err
	}

	lines := reparentForwardCASLines(rows, ReparentRefClassMap(classes))
	if err := reparentStep(ReparentStageCommittingRefs, "before-cas"); err != nil {
		return err
	}
	// Symbolic status is rechecked at the last possible point. v1 forbids
	// no-deref, so a symbolic affected ref must never reach update-ref.
	if err := reparentRequireAllDirectBranchRefs(r.RepoRoot, rows); err != nil {
		return err
	}
	if err := r.verifyRemoteRecordProof(); err != nil {
		restored := r.restoreHoldersBestEffort()
		var refusal *ReparentRefusalError
		if errors.As(err, &refusal) {
			refusal.StatePreserved = restored
			if !restored {
				refusal.Detail += "; " + r.holderRestoreInstructions()
			}
		}
		return err
	}
	if err := r.verifyPersistedHolderSnapshot(); err != nil {
		return err
	}
	res, casErr := runReparentCAS(r.RepoRoot, reparentCASMessage(r.Loc.Feature, r.State.TargetName, r.State.RunID), lines)
	if casErr != nil {
		detail := reparentGitMessage(res, casErr)
		restored := r.restoreHoldersBestEffort()
		// Whatever the next invocation enters at, it must pass through
		// detachment again: resume_stage is rewound so even a route that
		// trusts it re-detaches before it re-issues the transaction.
		refusal := &ReparentRefusalError{
			Kind:           ReparentRefusalRefTransactionMismatch,
			Detail:         detail,
			StatePreserved: restored,
		}

		if !restored {
			refusal.Detail = detail + "; " + r.holderRestoreInstructions()
		}
		return r.fail(
			ReparentFailureDomainReparent,
			string(ReparentRefusalRefTransactionMismatch),
			refusal.Detail,
			ReparentStageDetachingHolders,
			refusal,
		)
	}
	// cas_rows[] records what is TRUE of each row after the transaction, not
	// which verb this particular attempt happened to emit: a row that a
	// previous attempt already moved is still `applied`, and `noop` stays the
	// property of the row (planned == pre-image), never of the line shape.
	r.State.CASRows = r.State.CASRows[:0]
	for _, row := range rows {
		r.State.CASRows = append(r.State.CASRows, ReparentStateCASRow{
			Ref:      "refs/heads/" + row.GitBranch,
			OldValue: row.PreimageSHA,
			NewValue: row.PlannedNewSHA,
			Applied:  true,
			Noop:     row.Noop || row.PlannedNewSHA == row.PreimageSHA,
		})
	}
	for i := range r.State.Rows {
		r.State.Rows[i].Stage = ReparentRowCommitted
	}
	r.State.CASTransactionSucceeded = true
	if err := r.save(); err != nil {
		return err
	}
	if err := reparentStep(ReparentStageCommittingRefs, "after-cas"); err != nil {
		return err
	}
	return r.setStage(ReparentStageRefsCommitted)
}

func (r *ReparentRun) verifyRemoteRecordProof() error {
	if len(r.State.Rows) == 0 {
		return nil
	}
	path := ReparentRemoteRecordPath(r.Loc)
	switch {
	case r.State.RemoteRecordWritten:
		rec, err := LoadReparentRemoteRecord(r.Loc)
		if err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("the required remote follow-up record at %s is unreadable: %v", path, err))
		}
		if rec == nil || rec.RunID != r.State.RunID {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("the required remote follow-up record at %s is missing or belongs to another run", path))
		}
		for _, name := range r.State.RemoteFollowupEntries {
			entry, ok := rec.Entry(name)
			row := r.State.Row(name)
			if !ok || !entry.Pending() || row == nil ||
				entry.GitBranch != row.GitBranch || entry.NewTipSHA != row.PlannedNewSHA {
				return reparentRefusal(ReparentRefusalProbeFailed,
					fmt.Sprintf("the remote follow-up proof for %q is missing or no longer matches this run", name))
			}
		}
		return nil
	case r.State.RemoteRecordEmpty:
		rec, err := LoadReparentRemoteRecord(r.Loc)
		if err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("the empty remote follow-up proof at %s cannot be verified: %v", path, err))
		}
		if rec == nil {
			return nil
		}
		if protected := reparentRemotePendingForRows(*rec, r.State.RowsInOrder()); len(protected) > 0 {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("the remote follow-up record at %s contains pending protection for this run: %s",
					path, strings.Join(protected, ", ")))
		}
		return nil
	default:
		return reparentRefusal(ReparentRefusalProbeFailed,
			"the remote follow-up record is neither durable nor proven empty")
	}
}

func (r *ReparentRun) applyRemoteClearsJournaled(stack *Stack) ([]ReparentRemoteClear, error) {
	if r.State.RemoteClearPending {
		if err := r.reconcileRemoteClearTransition(); err != nil {
			return nil, err
		}
	}
	transition, err := BuildReparentRemoteClearTransition(r.Loc, r.RepoRoot, stack)
	if err != nil || !transition.Changed {
		return transition.Clears, err
	}
	r.State.RemoteClearPending = true
	r.State.RemoteClearSourcePresent = transition.SourcePresent
	r.State.RemoteClearSourceBase64 = ""
	if transition.SourcePresent {
		r.State.RemoteClearSourceBase64 = base64.StdEncoding.EncodeToString(transition.Source)
	}
	r.State.RemoteClearTargetPresent = transition.TargetPresent
	r.State.RemoteClearTargetBase64 = ""
	if transition.TargetPresent {
		r.State.RemoteClearTargetBase64 = base64.StdEncoding.EncodeToString(transition.Target)
	}
	if err := r.save(); err != nil {
		return nil, err
	}
	if err := reparentStep(r.State.Stage, "remote-clear-intent-written"); err != nil {
		return nil, err
	}
	return transition.Clears, r.reconcileRemoteClearTransition()
}

func (r *ReparentRun) refreshRemoteRecordProof() error {
	if !r.State.RemoteRecordWritten && !r.State.RemoteRecordEmpty {
		return nil
	}
	rec, err := LoadReparentRemoteRecord(r.Loc)
	if err != nil {
		return err
	}
	if rec == nil {
		r.State.RemoteRecordWritten = false
		r.State.RemoteRecordEmpty = true
		r.State.RemoteFollowupEntries = []string{}
	} else {
		protected := reparentRemotePendingForRows(*rec, r.State.RowsInOrder())
		if len(protected) > 0 && rec.RunID != r.State.RunID {
			return reparentRefusal(ReparentRefusalStateForeign,
				"the remote follow-up record belongs to another run and cannot prove this run's protected rows")
		}
		r.State.RemoteRecordWritten = len(protected) > 0
		r.State.RemoteRecordEmpty = len(protected) == 0
		r.State.RemoteFollowupEntries = protected
	}
	return r.save()
}

func reparentRemotePendingForRows(rec ReparentRemoteRecord, rows []ReparentStateRow) []string {
	owned := make(map[string]bool, len(rows))
	for _, row := range rows {
		owned[row.Name] = true
	}
	var out []string
	for _, entry := range rec.Entries {
		if owned[entry.Name] && entry.Pending() {
			out = append(out, entry.Name)
		}
	}
	return out
}

func (r *ReparentRun) reconcileRemoteClearTransition() error {
	source, sourcePresent, target, targetPresent, err := r.remoteClearImages()
	if err != nil {
		return err
	}
	live, livePresent, err := readReparentRemoteRecordRaw(ReparentRemoteRecordPath(r.Loc))
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("inspect remote clear transition: %v", err))
	}
	switch {
	case reparentRemoteImageMatches(live, livePresent, target, targetPresent):
	case reparentRemoteImageMatches(live, livePresent, source, sourcePresent):
		if targetPresent {
			if err := WriteReparentRemoteRecordBytes(r.Loc, target); err != nil {
				return err
			}
		} else if err := RemoveReparentRemoteRecord(r.Loc); err != nil {
			return err
		}
		if err := reparentStep(r.State.Stage, "remote-clear-applied-before-complete"); err != nil {
			return err
		}
	default:
		return reparentRefusal(ReparentRefusalStateForeign,
			"the remote follow-up record changed after the clear transition was journaled")
	}
	if err := confirmReparentRemoteImage(r.Loc, target, targetPresent, "remote clear transition"); err != nil {
		return err
	}
	r.State.RemoteClearPending = false
	r.State.RemoteClearSourcePresent = false
	r.State.RemoteClearSourceBase64 = ""
	r.State.RemoteClearTargetPresent = false
	r.State.RemoteClearTargetBase64 = ""
	return r.save()
}

func (r *ReparentRun) remoteClearImages() (source []byte, sourcePresent bool, target []byte, targetPresent bool, err error) {
	if !r.State.RemoteClearPending {
		return nil, false, nil, false, reparentRefusal(ReparentRefusalStateCorrupt,
			"remote clear reconciliation was requested without a pending transition")
	}
	sourcePresent = r.State.RemoteClearSourcePresent
	if sourcePresent {
		source, err = base64.StdEncoding.DecodeString(r.State.RemoteClearSourceBase64)
		if err != nil {
			return nil, false, nil, false, reparentRefusal(ReparentRefusalStateCorrupt,
				"remote clear source does not decode")
		}
		if _, err = decodeReparentRemoteRecordBytes(r.Loc, source); err != nil {
			return nil, false, nil, false, err
		}
	}
	targetPresent = r.State.RemoteClearTargetPresent
	if targetPresent {
		target, err = base64.StdEncoding.DecodeString(r.State.RemoteClearTargetBase64)
		if err != nil {
			return nil, false, nil, false, reparentRefusal(ReparentRefusalStateCorrupt,
				"remote clear target does not decode")
		}
		if _, err = decodeReparentRemoteRecordBytes(r.Loc, target); err != nil {
			return nil, false, nil, false, err
		}
	}
	return source, sourcePresent, target, targetPresent, nil
}

// stageRefsCommitted re-classifies before moving on: a pre-image row means
// the transaction did not fully land and the run falls back to
// committing-refs; a foreign row before the commit point refuses.
func (r *ReparentRun) stageRefsCommitted() error {
	classes, err := ClassifyReparentRefs(r.RepoRoot, r.State.RowsInOrder())
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	for _, c := range classes {
		if c.Class == ReparentRefForeign && !r.State.CommitPointReached {
			return reparentEntryRefusal(ReparentRefusalRefForeignValue, c.Name, reparentForeignRefDetail(c))
		}
		if c.Class == ReparentRefPreimage {
			return r.setStage(ReparentStageCommittingRefs)
		}
	}
	return r.setStage(ReparentStageWritingMetadata)
}

// stageWritingMetadata writes stack.yaml exactly once, from the persisted
// post-image bytes, after proving BOTH halves of §11.8a's conjunction: the
// live file is still the one this run captured, and every affected ref
// actually carries its planned tip.
//
// The ref half is not a formality. stack.yaml's post-image says "pr2 is based
// on main at <sha>"; writing it while a ref still holds its pre-image would
// publish metadata for a topology Git does not have, and — because the hash
// then matches stack_sha256_after_expected — every later stage would read that
// as the commit point and refuse to roll back.
func (r *ReparentRun) stageWritingMetadata() error {
	rewound, err := r.requireCommittedRefs()
	if err != nil || rewound {
		return err
	}
	live, err := os.ReadFile(StackPath(r.Loc.FeaturePath))
	if err != nil {
		return reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml could not be read: %v", err))
	}
	sum := hex.EncodeToString(sliceSHA256(live))
	switch sum {
	case r.State.StackSHA256AfterExpected:
		// Already written: a crash between the rename and the state update.
		if err := confirmReparentRenameDurable(StackPath(r.Loc.FeaturePath)); err != nil {
			return reparentRefusal(ReparentRefusalMetadataPending,
				fmt.Sprintf("make the visible post-image durable: %v", err))
		}
		r.State.StackSHA256After = sum
		if err := r.save(); err != nil {
			return err
		}
		return r.finishMetadata()
	case r.State.StackSHA256Before:
	default:
		return reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml hashes to %s; this run captured %s and expects to write %s. It was edited while the run held the lock; tws will not overwrite that edit",
			sum, r.State.StackSHA256Before, r.State.StackSHA256AfterExpected))
	}

	data, err := base64.StdEncoding.DecodeString(r.State.StackAfterBase64)
	if err != nil {
		return reparentRefusal(ReparentRefusalMetadataPending, err.Error())
	}
	if err := reparentStep(ReparentStageWritingMetadata, "before-write"); err != nil {
		return err
	}
	if err := WriteStackBytesAtomic(r.Loc.FeaturePath, data); err != nil {
		detail := err.Error()
		r.State.FailureDomain = ReparentFailureDomainIO
		r.State.FailureKind = string(ReparentRefusalMetadataPending)
		r.State.FailureDetail = detail
		r.State.ResumeStage = ReparentStageWritingMetadata
		r.State.Stage = ReparentStageFailed
		_ = SaveReparentState(r.Loc, r.State)
		return &ReparentRefusalError{Kind: ReparentRefusalMetadataPending, Detail: detail, StatePreserved: true}
	}
	r.State.StackSHA256After = r.State.StackSHA256AfterExpected
	if err := r.save(); err != nil {
		return err
	}
	if err := reparentStep(ReparentStageWritingMetadata, "after-write"); err != nil {
		return err
	}
	return r.finishMetadata()
}

// finishMetadata advances to metadata-written and PROVES the commit point.
// The verdict is never discarded: if the conjunction does not hold — because
// something moved between the ref transaction and this write — the run is
// rewound or refused rather than walking into holder restoration and cleanup
// on the strength of a stage name.
func (r *ReparentRun) finishMetadata() error {
	if err := r.setStage(ReparentStageMetadataWritten); err != nil {
		return err
	}
	committed, err := r.evaluateCommitPoint()
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	_, err = r.requireCommittedRefs()
	return err
}

func sliceSHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// stageMetadataWritten confirms the monotonic commit-point marker and reports
// any later ref movement informationally, then restores holders. Without that
// confirmation it does NOT advance: holder restoration re-attaches worktrees
// to branches this run may still have to move.
func (r *ReparentRun) stageMetadataWritten() error {
	committed, err := r.evaluateCommitPoint()
	if err != nil {
		return err
	}
	if !committed {
		rewound, err := r.requireCommittedRefs()
		if err != nil || rewound {
			return err
		}
		// Refs are committed but the conjunction still failed, so the
		// metadata half is what is missing; requireCommittedRefs has rewound
		// the run to writing-metadata.
		return nil
	}
	return r.setStage(ReparentStageRestoringHolders)
}

// reparentMaxCommitGateRewinds bounds how often one invocation may rewind
// itself through the commit gate. A run that cannot make the conjunction hold
// after two complete passes is wedged by something outside this process, and
// spinning would be worse than refusing.
const reparentMaxCommitGateRewinds = 2

// requireCommittedRefs is the gate every post-transaction stage passes
// through. It never trusts a stage name and never trusts stack.yaml alone: it
// classifies every affected ref (§11.7) and answers what this run is allowed
// to do next.
//
//   - the monotonic marker is already true -> proceed; later movement is
//     informational and cleanup must not be blocked by it;
//   - any foreign ref -> refuse ref-foreign-value, change nothing;
//   - any pre-image ref -> the transaction did not fully land; rewind to
//     committing-refs and complete it forward;
//   - every ref planned/no-op but the marker still false -> the METADATA half
//     is missing; rewind to writing-metadata, or refuse metadata-drift when
//     the live file is neither this run's pre-image nor its post-image.
//
// rewound reports that the caller must return immediately and let the stage
// machine re-dispatch.
func (r *ReparentRun) requireCommittedRefs() (rewound bool, err error) {
	if r.State.CommitPointReached {
		return false, nil
	}
	classes, err := ClassifyReparentRefs(r.RepoRoot, r.State.RowsInOrder())
	if err != nil {
		return false, reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	for _, c := range classes {
		if c.Class == ReparentRefForeign {
			return false, reparentEntryRefusal(ReparentRefusalRefForeignValue, c.Name, reparentForeignRefDetail(c))
		}
	}
	for _, c := range classes {
		if c.Class != ReparentRefPreimage {
			continue
		}
		if err := r.countCommitGateRewind(); err != nil {
			return false, err
		}
		r.prose("reparent-recovery: %s: %s still holds its pre-image; completing the transaction before any metadata is written\n",
			ReparentRecoveryPartialToken, c.Name)
		return true, r.setStage(ReparentStageCommittingRefs)
	}

	// Every ref is where the plan wanted it. If the marker is still false the
	// missing half is stack.yaml itself.
	live, readErr := os.ReadFile(StackPath(r.Loc.FeaturePath))
	if readErr != nil {
		return false, reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml could not be read: %v", readErr))
	}
	sum := hex.EncodeToString(sliceSHA256(live))
	switch sum {
	case r.State.StackSHA256AfterExpected:
		// The bytes are there; the marker simply has not been evaluated yet.
		committed, err := r.evaluateCommitPoint()
		if err != nil {
			return false, err
		}
		if committed {
			return false, nil
		}
		return false, reparentRefusal(ReparentRefusalProbeFailed,
			"the post-image bytes and every planned ref are present, but the commit point could not be established")
	case r.State.StackSHA256Before:
		if r.State.Stage == ReparentStageWritingMetadata {
			// Already where the write happens; let it proceed.
			return false, nil
		}
		if err := r.countCommitGateRewind(); err != nil {
			return false, err
		}
		return true, r.setStage(ReparentStageWritingMetadata)
	default:
		return false, reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml hashes to %s, which is neither this run's pre-image %s nor its planned post-image %s; tws will not overwrite it",
			sum, r.State.StackSHA256Before, r.State.StackSHA256AfterExpected))
	}
}

func (r *ReparentRun) countCommitGateRewind() error {
	r.commitGateRewinds++
	if r.commitGateRewinds > reparentMaxCommitGateRewinds {
		return reparentRefusal(ReparentRefusalRefTransactionMismatch,
			"the affected refs and stack.yaml could not be brought into agreement; inspect them and re-run --continue or --abort")
	}
	return nil
}

// evaluateCommitPoint proves §11.8a's conjunction FROM DISK and persists the
// monotonic marker the first instant it holds. Once true it never becomes
// false, even if an operator later advances a branch.
func (r *ReparentRun) evaluateCommitPoint() (bool, error) {
	if r.State.CommitPointReached {
		return true, nil
	}
	if r.State.StackSHA256AfterExpected == "" {
		return false, nil
	}
	live, err := os.ReadFile(StackPath(r.Loc.FeaturePath))
	if err != nil {
		// An unreadable stack.yaml is NOT evidence that the commit point was
		// missed. Answering "false" here would tell --abort it is still
		// before the commit point, and a bounded rollback would then move
		// public refs back on the strength of a file it could not read.
		return false, reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml could not be read, so the commit point cannot be decided: %v", err))
	}
	sum := hex.EncodeToString(sliceSHA256(live))
	assessment, err := assessReparentLiveRefs(r.RepoRoot, r.State, sum)
	if err != nil {
		var refusal *ReparentRefusalError
		if errors.As(err, &refusal) {
			return false, refusal
		}
		return false, reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	if !assessment.CommitPointProven {
		return false, nil
	}
	for _, class := range assessment.Classes {
		if assessment.Advanced[class.Name] {
			r.prose("reparent: %s advanced past its planned tip after this run committed it\n", class.Name)
		}
	}
	r.State.CommitPointReached = true
	if err := r.save(); err != nil {
		return false, err
	}
	return true, nil
}

// stageRestoringHolders re-attaches every recorded holder and, in checkout
// mode, restores the computation context explicitly — with `git switch`,
// never with `git reset --hard`.
func (r *ReparentRun) stageRestoringHolders() error {
	// Re-attaching a holder to a branch this run may still have to move would
	// leave that worktree's index and HEAD inconsistent the moment the
	// transaction completes, so the same conjunction gates this stage.
	if rewound, err := r.requireCommittedRefs(); err != nil || rewound {
		return err
	}
	if !reparentStateHolderRestorationComplete(r.State) {
		if err := r.verifyPersistedHolderSnapshot(); err != nil {
			return err
		}
	}
	// The deferral list is re-derived on every attempt: a holder the operator
	// has since made restorable must drop off it, or the run could never
	// finish cleaning up.
	r.State.HolderRestoreDeferred = nil
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if h.HolderKind == "computation-context" {
			continue
		}
		if h.Restored {
			continue
		}
		if err := r.restoreHolder(h); err != nil {
			h.RestoreReason = err.Error()
			r.State.HolderRestoreDeferred = appendUnique(r.State.HolderRestoreDeferred, h.Path)
			r.prose("reparent: %s: %s could not be re-attached to %s: %s; re-attach it with: git -C %s switch %s\n",
				ReparentWarnHolderRestoreDeferred, h.Path, h.GitBranch, sanitizeReparentLine(err.Error()), h.Path, h.GitBranch)
		}
		if err := r.save(); err != nil {
			return err
		}
	}
	if r.Loc.Mode == ModeCheckout {
		if err := r.restoreCheckoutContext(); err != nil {
			if h := r.computationHolder(); h != nil {
				h.RestoreReason = err.Error()
			}
			r.State.HolderRestoreDeferred = appendUnique(r.State.HolderRestoreDeferred, r.RepoRoot)
			r.prose("reparent: %s: the checkout could not be returned to %s: %s\n",
				ReparentWarnHolderRestoreDeferred, r.State.OriginalBranch, sanitizeReparentLine(err.Error()))
			if err := r.save(); err != nil {
				return err
			}
		} else if h := r.computationHolder(); h != nil {
			h.RestoreReason = ""
		}
	}
	if err := reparentStep(ReparentStageRestoringHolders, "holders-restored"); err != nil {
		return err
	}
	if len(r.State.HolderRestoreDeferred) > 0 {
		// AC-096: a post-commit-point restore failure is an ADVISORY, not a
		// refusal — the run has succeeded and exits 0 — but the deferral is
		// persisted and "re-attempted by a later --continue", which is only
		// possible if the artifact survives. Cleanup therefore waits: the run
		// stays at restoring-holders with its deferrals recorded, and the
		// launch exclusion stays active until the operator's worktrees can be
		// re-attached.
		r.prose("reparent: %s: %d holder(s) could not be re-attached; the run is complete and its artifacts remain so `tws stack reparent %s --continue` can re-attempt exactly those worktrees\n",
			ReparentWarnHolderRestoreDeferred, len(r.State.HolderRestoreDeferred), r.Loc.Feature)
		return errReparentHoldersDeferred
	}
	return r.setStage(ReparentStageCleanup)
}

func (r *ReparentRun) restoreHolder(h *ReparentStateHolder) error {
	if err := r.reconcileHolderSwitchIntents(); err != nil {
		return err
	}
	if h.Restored {
		return nil
	}
	if err := r.verifyPersistedHolderSnapshot(); err != nil {
		return err
	}
	dirty, err := measureReparentDirty(h.Path)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return fmt.Errorf("tracked modifications are present: %s", strings.Join(dirty, ", "))
	}
	op, err := measureReparentGitOperation(h.Path)
	if err != nil {
		return err
	}
	if op != "" {
		return fmt.Errorf("a %s is in progress", op)
	}
	if err := reparentUntrackedGate(h.Path, "refs/heads/"+h.GitBranch); err != nil {
		return err
	}
	destination, ok, err := reparentResolveRef(h.Path, "refs/heads/"+h.GitBranch)
	if err != nil || !ok {
		return fmt.Errorf("recorded branch %s does not resolve", h.GitBranch)
	}
	if err := r.beginHolderSwitch(h, ReparentHolderSwitchRestore, h.GitBranch, destination); err != nil {
		return err
	}
	if err := reparentStep(r.State.Stage, "holder-restore-intent-written:"+h.Path); err != nil {
		return err
	}
	if res, err := runReparentGit(h.Path, nil, "switch", h.GitBranch); err != nil {
		return fmt.Errorf("%s", reparentGitMessage(res, err))
	}
	if err := r.completeHolderSwitch(h); err != nil {
		return err
	}
	return reparentStep(r.State.Stage, "holder-restored:"+h.Path)
}

// restoreCheckoutContext returns the one physical checkout to the branch or
// detached HEAD §9.3 recorded. If the original branch is itself an affected
// row it is restored to its NEW tip — that is the intended result.
func (r *ReparentRun) restoreCheckoutContext() error {
	if err := r.reconcileHolderSwitchIntents(); err != nil {
		return err
	}
	if err := r.verifyPersistedHolderSnapshot(); err != nil {
		return err
	}
	h := r.computationHolder()
	if h == nil {
		pos, err := measureReparentHolderPosition(r.RepoRoot)
		if err != nil {
			return err
		}
		wantBranch := r.State.OriginalBranch
		if r.State.OriginalDetached {
			wantBranch = ""
		}
		if reparentHolderPositionMatches(pos, wantBranch, r.State.OriginalHead) {
			return nil
		}
		return fmt.Errorf("the checkout computation context has no holder progress and is at branch %q HEAD %s, not its recorded origin",
			pos.Branch, pos.SHA)
	}
	if h.Restored {
		return nil
	}
	if r.State.OriginalDetached || r.State.OriginalBranch == "" {
		if r.State.OriginalHead == "" {
			return fmt.Errorf("recorded checkout identity has no original branch or detached HEAD")
		}
		if err := r.ensureOriginalHeadPin(); err != nil {
			return err
		}
		if err := reparentUntrackedGate(r.RepoRoot, r.State.OriginalHead); err != nil {
			return err
		}
		if err := r.beginHolderSwitch(h, ReparentHolderSwitchRestore, "", r.State.OriginalHead); err != nil {
			return err
		}
		if err := reparentStep(r.State.Stage, "checkout-restore-intent-written"); err != nil {
			return err
		}
		res, err := runReparentGit(r.RepoRoot, nil, "switch", "--detach", r.State.OriginalHead)
		if err != nil {
			return fmt.Errorf("%s", reparentGitMessage(res, err))
		}
		if err := r.completeHolderSwitch(h); err != nil {
			return err
		}
		return reparentStep(r.State.Stage, "checkout-restored")
	}
	if err := reparentUntrackedGate(r.RepoRoot, "refs/heads/"+r.State.OriginalBranch); err != nil {
		return err
	}
	destination, ok, err := reparentResolveRef(r.RepoRoot, "refs/heads/"+r.State.OriginalBranch)
	if err != nil || !ok {
		return fmt.Errorf("recorded checkout branch %s does not resolve", r.State.OriginalBranch)
	}
	if err := r.beginHolderSwitch(h, ReparentHolderSwitchRestore, r.State.OriginalBranch, destination); err != nil {
		return err
	}
	if err := reparentStep(r.State.Stage, "checkout-restore-intent-written"); err != nil {
		return err
	}
	res, err := runReparentGit(r.RepoRoot, nil, "switch", r.State.OriginalBranch)
	if err != nil {
		return fmt.Errorf("%s", reparentGitMessage(res, err))
	}
	if err := r.completeHolderSwitch(h); err != nil {
		return err
	}
	return reparentStep(r.State.Stage, "checkout-restored")
}

func (r *ReparentRun) ensureOriginalHeadPin() error {
	if !r.State.OriginalDetached {
		return nil
	}
	if r.State.OriginalHead == "" {
		return reparentRefusal(ReparentRefusalProbeFailed,
			"the originally detached checkout has no recorded HEAD to pin")
	}
	if r.State.OriginalHeadPinRef == "" {
		r.State.OriginalHeadPinRef = reparentOriginalHeadPinRef(r.State.RunID)
		if err := r.save(); err != nil {
			return err
		}
	}
	if _, err := reparentVerifyPin(r.RepoRoot, r.State.OriginalHeadPinRef, r.State.OriginalHead); err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("verify original checkout HEAD pin: %v", err))
	}
	return nil
}

// restoreHoldersBestEffort re-attaches every detached holder before a
// pre-commit refusal. It reports whether ALL of them were restored, which is
// what decides the `state-preserved:` marker on a ref-transaction-mismatch.
func (r *ReparentRun) restoreHoldersBestEffort() bool {
	all := true
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if h.HolderKind == "computation-context" {
			continue
		}
		if h.Restored {
			continue
		}
		if err := r.restoreHolder(h); err != nil {
			h.RestoreReason = err.Error()
			all = false
		}
	}
	_ = r.save()
	return all
}

func (r *ReparentRun) holderRestoreInstructions() string {
	var parts []string
	for _, h := range r.State.DetachedHolders {
		if h.HolderKind == "computation-context" {
			continue
		}
		if h.Restored {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: git -C %s switch %s", h.Path, h.Path, h.GitBranch))
	}
	if len(parts) == 0 {
		return ""
	}
	return "re-attach by hand: " + strings.Join(parts, "; ")
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// stageCleanup is the forward route's cleanup: §11.6a's ordered list with
// `completed` as its terminal stage.
//
// It is gated on the commit point because its last step DELETES the only
// record of the run. Cleaning up a run whose refs never reached their planned
// tips would strand a half-applied topology with nothing left to describe it,
// and would report success for it. The abort route reaches runCleanup
// directly and is deliberately NOT gated here: a bounded rollback ends with
// every ref back at its pre-image, which is the state this gate exists to
// refuse on the forward path.
func (r *ReparentRun) stageCleanup() error {
	if rewound, err := r.requireCommittedRefs(); err != nil || rewound {
		return err
	}
	return r.runCleanup(ReparentStageCompleted)
}

// runCleanup runs §11.6a's ordered list, tolerating any step already being
// done, and deletes the authoritative artifact LAST — it is the only record
// of the five steps before it. terminal is `completed` for a forward run and
// `aborted` for a rollback.
func (r *ReparentRun) runCleanup(terminal ReparentStage) error {
	// Scratch cleanup stays ahead of pin deletion so any stat/remove/prune
	// failure preserves every recovery anchor together with state and locks.
	if r.Loc.Mode == ModeExternal && r.State.ScratchPath != "" {
		if err := r.removeScratch(); err != nil {
			return err
		}
	}
	// Every pin under this run's namespace.
	if err := reparentDeletePins(r.RepoRoot, r.State.RunID); err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	// 3 and 4 — the compatibility artifacts, sentinel first.
	if err := RemoveReparentCompatArtifacts(r.Loc, r.State.RunID); err != nil {
		return err
	}
	// 5 — the shared mode lock.
	owner := r.Input.ExclusionOwner
	if owner == nil {
		owner = &ReparentExclusionOwner{
			RunID: r.State.RunID, OwnerToken: r.State.OwnerToken, OwnerPID: os.Getpid(),
		}
	}
	if err := releaseReparentModeLock(r.Loc, owner); err != nil {
		return err
	}
	r.lockHeld = false

	if err := reparentStep(ReparentStageCleanup, "artifacts-removed"); err != nil {
		return err
	}
	if err := r.setStage(terminal); err != nil {
		return err
	}
	// 6 — the artifact itself.
	return RemoveReparentState(r.Loc)
}

// removeScratch proves the scratch worktree clean, then removes and prunes
// it. Residue refuses context-dirty, preserves the state and the path, and —
// after the commit point — says the topology change is already committed and
// only cleanup pends.
func (r *ReparentRun) removeScratch() error {
	scratch := r.State.ScratchPath
	if _, err := os.Stat(scratch); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("inspect computation worktree %s: %v", scratch, err))
		}
		if res, pruneErr := runReparentGit(r.RepoRoot, nil, "worktree", "prune"); pruneErr != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, reparentGitMessage(res, pruneErr))
		}
		return nil
	}
	residue, residueErr := reparentContextResidue(scratch)
	if residueErr != nil {
		// §11.6a step 2 permits `--force` ONLY after a cleanliness proof. A
		// probe that failed is not a proof, so the scratch worktree and the
		// whole run state are preserved and the operator is told what to look
		// at rather than having the directory removed underneath them.
		detail := fmt.Sprintf(
			"the computation worktree %s could not be proven clean, so it was left in place: %v", scratch, residueErr)
		if r.State.CommitPointReached {
			detail += ". The topology change is already committed; only cleanup is pending"
		}
		return reparentRefusal(ReparentRefusalProbeFailed, detail)
	}
	if len(residue) > 0 {
		detail := fmt.Sprintf("the computation worktree %s still holds %s; inspect and clean it, then re-run --continue",
			scratch, strings.Join(residue, ", "))
		if r.State.CommitPointReached {
			detail += ". The topology change is already committed; only cleanup is pending"
		}
		return reparentRefusal(ReparentRefusalContextDirty, detail)
	}
	if res, err := runReparentGit(r.RepoRoot, nil, "worktree", "remove", "--force", scratch); err != nil {
		return reparentRefusal(ReparentRefusalContextDirty, reparentGitMessage(res, err))
	}
	if res, err := runReparentGit(r.RepoRoot, nil, "worktree", "prune"); err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed, reparentGitMessage(res, err))
	}
	return nil
}

// ============================================================================
// Recovery (§11.2a, §11.6, §11.8)
// ============================================================================

func validateReparentStateInvocation(in ReparentPlanInput, st *ReparentState) error {
	workspaceRoot := in.RepoRoot
	if workspaceRoot == "" {
		workspaceRoot = in.Workspace.RepoRoot
	}
	if canonicalize(st.WorkspaceRepoRoot) != canonicalize(workspaceRoot) {
		return reparentRefusal(ReparentRefusalStateCorrupt, fmt.Sprintf(
			"the persisted workspace repository %s does not match invocation repository %s",
			st.WorkspaceRepoRoot, workspaceRoot))
	}
	if workspaceRoot == "" {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"the recovery invocation has no workspace repository root")
	}
	raw, err := base64.StdEncoding.DecodeString(st.StackBeforeBase64)
	if err != nil {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"the captured stack preimage does not decode")
	}
	var stack Stack
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&stack); err != nil {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the captured stack preimage is invalid: %v", err))
	}
	target := GetBranch(stack, st.TargetName)
	if target.Name == "" {
		return reparentRefusal(ReparentRefusalStateCorrupt,
			"the captured stack does not contain the recorded target")
	}
	expectedRoot := reparentRowRepoDir(workspaceRoot, target.Repo)
	if canonicalize(st.RepoRoot) != canonicalize(expectedRoot) {
		return reparentRefusal(ReparentRefusalStateCorrupt, fmt.Sprintf(
			"the persisted target repository %s does not match captured stack repository %s",
			st.RepoRoot, expectedRoot))
	}
	return nil
}

// openReparentRecovery is the shared prelude of both recovery verbs: classify
// the authoritative artifact, reclaim the mode lock a dead predecessor may
// still own, and build a run positioned at its recorded stage.
//
// Neither verb takes an approval token and neither re-evaluates the replay
// limits as an admission gate (§8.4): they inherit the frozen limits from
// state purely so an older release refuses to resume the run.
func openReparentRecovery(in ReparentPlanInput, route ReparentRoute) (*ReparentRun, error) {
	st, err := RefuseIfReparentActive(in.Loc, route)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, reparentRefusal(ReparentRefusalStatePresent,
			fmt.Sprintf("no reparent run is recorded for %q", in.Loc.Feature))
	}
	if err := validateReparentStateInvocation(in, st); err != nil {
		return nil, err
	}
	if st.OwnerPID != os.Getpid() && isProcessAlive(st.OwnerPID) {
		return nil, reparentRefusal(ReparentRefusalStatePresent, fmt.Sprintf(
			"reparent run %s is still owned by live process %d", st.RunID, st.OwnerPID))
	}

	// Compatibility ownership is classified before the lock or authoritative
	// state is rewritten. Abort uses this same prelude, so it can never reach
	// a ref or metadata CAS on the strength of foreign/unreadable guards.
	expectedOwner := &ReparentExclusionOwner{
		RunID: st.RunID, OwnerToken: st.OwnerToken, OwnerPID: st.OwnerPID,
	}
	if detail := ReparentForeignSyncStateOwned(in.Loc, expectedOwner); detail != "" {
		return nil, reparentRefusal(ReparentRefusalSyncStatePresent, detail)
	}
	lockOwner, err := acquireReparentModeLock(in.Loc, st.OwnerToken, false)
	if err != nil {
		return nil, err
	}
	lockOwner.RunID = st.RunID
	lockOwner.OwnerToken = st.OwnerToken
	if err := reparentStep(ReparentStageInitializing, "recovery-locks-reclaimed"); err != nil {
		// Deliberately leave both transferred locks in place: this is the exact
		// crash window the next recovery must classify from disk.
		return nil, err
	}
	if detail := ReparentForeignSyncStateOwned(in.Loc, lockOwner); detail != "" {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, reparentRefusal(ReparentRefusalSyncStatePresent, detail)
	}
	if err := confirmReparentRenameDurable(ReparentStatePath(in.Loc)); err != nil {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("make the visible authoritative reparent state durable: %v", err))
	}
	if compat := ClassifyReparentCompatArtifacts(in.Loc, st.RunID); compat.Complete {
		if err := confirmReparentCompatDurability(in.Loc); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("make the visible compatibility envelope durable: %v", err))
		}
	}
	if in.SessionIntentCleanup != nil {
		affected := make([]string, 0, len(st.Rows))
		for _, row := range st.RowsInOrder() {
			affected = append(affected, row.Name)
		}
		if err := in.SessionIntentCleanup(affected); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, reparentRefusal(ReparentRefusalSessionLive,
				fmt.Sprintf("stale session launch intents could not be reconciled after reclaiming the mutation lock: %v", err))
		}
	}
	if err := reparentProbeRecoverySessions(in, st.RowsInOrder()); err != nil {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, err
	}
	// The PERSISTED root wins: it is the repository this run has been
	// mutating, and a recovery route that re-derived it from the invocation
	// could resume a run for a secondary-repository target against the
	// workspace root instead.
	repoRoot := st.RepoRoot
	if repoRoot == "" {
		repoRoot = in.RepoRoot
	}
	run := &ReparentRun{
		Loc:      in.Loc,
		RepoRoot: repoRoot,
		State:    st,
		Input:    in,
		Writers:  in.Writers,
		lockHeld: true,
	}
	stack, validateErr := validateReparentRecoverySnapshot(in.Loc, repoRoot, st, route)
	if validateErr != nil {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, validateErr
	}
	if route == ReparentRouteVerbContinue {
		if err := run.recoverCompatArtifacts(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
		if st.Stage == ReparentStageInitializing {
			// Crash window 1/2 recovery completes the compatibility envelope
			// first, then durably enters preflight before reconciling or
			// applying any prior remote-record clear.
			if err := run.setStage(ReparentStagePreflight); err != nil {
				_ = releaseReparentModeLock(in.Loc, lockOwner)
				return nil, err
			}
		}
	} else {
		compat := ClassifyReparentCompatArtifacts(in.Loc, st.RunID)
		if compat.Foreign {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, reparentRefusal(ReparentRefusalSyncStatePresent,
				"foreign compatibility state exists beside this reparent run")
		}
		if compat.Complete {
			if err := confirmReparentCompatDurability(in.Loc); err != nil {
				_ = releaseReparentModeLock(in.Loc, lockOwner)
				return nil, reparentRefusal(ReparentRefusalProbeFailed,
					fmt.Sprintf("make the visible compatibility envelope durable: %v", err))
			}
		}
	}
	if reparentStageNeedsHolderVerification(st.Stage) && !reparentStateHolderRestorationComplete(st) {
		if err := run.reconcileHolderDetachIntents(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
		if err := run.reconcileManualHolderRestorations(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
		if err := run.verifyPersistedHolderSnapshot(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
	}
	if err := run.ensureOriginalHeadPin(); err != nil {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, err
	}
	if st.RemoteClearPending {
		if err := run.reconcileRemoteClearTransition(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
		if err := run.refreshRemoteRecordProof(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
	}
	if route == ReparentRouteVerbAbort && st.RemoteRecordRestorePending {
		if err := run.reconcileAbortRemoteRecordRestore(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
	}
	if err := run.reconcilePostCommitRemoteClearProof(&stack); err != nil {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, err
	}
	if st.RemoteRecordWritten || st.RemoteRecordEmpty {
		if proofErr := run.verifyRemoteRecordProof(); proofErr != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, proofErr
		}
	}
	if route == ReparentRouteVerbContinue {
		if _, clearErr := run.applyRemoteClearsJournaled(&stack); clearErr != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, clearErr
		}
		if err := run.refreshRemoteRecordProof(); err != nil {
			_ = releaseReparentModeLock(in.Loc, lockOwner)
			return nil, err
		}
	}
	st.OwnerPID = os.Getpid()
	in.ExclusionOwner = lockOwner
	run.Input = in
	if err := run.save(); err != nil {
		_ = releaseReparentModeLock(in.Loc, lockOwner)
		return nil, err
	}
	return run, nil
}

func (r *ReparentRun) reconcilePostCommitRemoteClearProof(stack *Stack) error {
	if !r.State.CommitPointReached || !r.State.RemoteRecordWritten {
		return nil
	}
	rec, err := LoadReparentRemoteRecord(r.Loc)
	if err != nil {
		return err
	}
	remaining := make([]string, 0, len(r.State.RemoteFollowupEntries))
	for _, name := range r.State.RemoteFollowupEntries {
		row := r.State.Row(name)
		if row == nil || row.PlannedNewSHA == "" {
			return reparentRefusal(ReparentRefusalStateCorrupt,
				fmt.Sprintf("remote clear proof names unknown row %q", name))
		}
		if rec != nil {
			if entry, ok := rec.Entry(name); ok {
				if entry.GitBranch != row.GitBranch || entry.NewTipSHA != row.PlannedNewSHA {
					return reparentRefusal(ReparentRefusalProbeFailed,
						fmt.Sprintf("the remote follow-up proof for %q no longer matches this run", name))
				}
				if entry.Pending() {
					remaining = append(remaining, name)
					continue
				}
				// A matching cleared row is the durable result of a
				// journaled §12.5 transition. Disjoint pending rows may keep
				// the shared record on disk, but they do not keep this run's
				// proof pending.
				continue
			}
		}
		if stack != nil {
			entry := GetBranch(*stack, name)
			if entry.Name == "" || entry.Archived {
				continue
			}
		}
		repoDir := r.State.WorkspaceRepoRoot
		if strings.TrimSpace(row.Repo) != "" {
			repoDir = canonicalize(row.Repo)
		}
		remoteRef := "refs/remotes/" + ReparentRemoteName + "/" + row.GitBranch
		live, ok, resolveErr := reparentResolveRef(repoDir, remoteRef)
		if resolveErr != nil || !ok {
			return reparentRefusal(ReparentRefusalProbeFailed, fmt.Sprintf(
				"the remote follow-up record disappeared but %s cannot prove publication in %s",
				remoteRef, repoDir))
		}
		if live == row.PlannedNewSHA {
			continue
		}
		contains, ancestorErr := reparentIsAncestor(repoDir, row.PlannedNewSHA, live)
		if ancestorErr != nil || !contains {
			return reparentRefusal(ReparentRefusalProbeFailed, fmt.Sprintf(
				"the remote follow-up record disappeared but %s does not contain planned tip %s",
				remoteRef, row.PlannedNewSHA))
		}
	}
	r.State.RemoteRecordWritten = len(remaining) > 0
	r.State.RemoteRecordEmpty = len(remaining) == 0
	r.State.RemoteFollowupEntries = remaining
	return r.save()
}

func validateReparentRecoverySnapshot(loc ReparentLocation, repoRoot string, st *ReparentState, route ReparentRoute) (Stack, error) {
	raw, err := os.ReadFile(StackPath(loc.FeaturePath))
	if err != nil {
		return Stack{}, reparentRefusal(ReparentRefusalMetadataDrift,
			fmt.Sprintf("stack.yaml could not be read before recovery: %v", err))
	}
	sum := hex.EncodeToString(sliceSHA256(raw))
	if sum != st.StackSHA256Before &&
		(st.StackSHA256AfterExpected == "" || sum != st.StackSHA256AfterExpected) &&
		(st.StackSHA256After == "" || sum != st.StackSHA256After) {
		return Stack{}, reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml hashes to %s, which is not a state this run recorded", sum))
	}
	var stack Stack
	if err := yaml.Unmarshal(raw, &stack); err != nil {
		return Stack{}, reparentRefusal(ReparentRefusalMetadataDrift,
			fmt.Sprintf("stack.yaml could not be decoded before recovery: %v", err))
	}
	assessment, err := assessReparentLiveRefs(repoRoot, st, sum)
	if err != nil {
		return Stack{}, err
	}
	if !assessment.CommitPointProven {
		for _, class := range assessment.Classes {
			if class.Class != ReparentRefForeign {
				continue
			}
			if row := st.Row(class.Name); row != nil && row.PlannedNewSHA == "" {
				continue
			}
			kind := ReparentRefusalRefForeignValue
			if route == ReparentRouteVerbAbort {
				kind = ReparentRefusalAbortForeignValue
			}
			return Stack{}, reparentEntryRefusal(kind, class.Name, reparentForeignRefDetail(class))
		}
	}
	return stack, nil
}

func reparentProbeRecoverySessions(in ReparentPlanInput, rows []ReparentStateRow) error {
	if in.SessionProbe == nil {
		return nil
	}
	affected := make([]string, 0, len(rows))
	for _, row := range rows {
		affected = append(affected, row.Name)
	}
	live, err := in.SessionProbe(affected)
	if err != nil {
		return reparentRefusal(ReparentRefusalSessionLive,
			fmt.Sprintf("session liveness could not be verified after reclaiming the mutation lock: %v", err))
	}
	if len(live) == 0 {
		return nil
	}
	sort.Strings(live)
	name := live[0]
	return reparentEntryRefusal(ReparentRefusalSessionLive, name,
		fmt.Sprintf("%q has a live or unverifiable tws session after the mutation lock was reclaimed; close it before resuming", name))
}

// ContinueReparent resumes forward. It never restarts, is idempotent at every
// stage, and re-enters at resume_stage.
func ContinueReparent(in ReparentPlanInput) (*ReparentRun, error) {
	run, err := openReparentRecovery(in, ReparentRouteVerbContinue)
	if err != nil {
		return nil, err
	}

	switch run.State.Stage {
	case ReparentStageCompleted:
		// "Already complete" is a claim about the world, not about a stage
		// name, so it is proven before any residue is removed: cleanup's last
		// step deletes the only record of the run.
		rewound, gateErr := run.requireCommittedRefs()
		if gateErr != nil {
			return run, gateErr
		}
		if rewound {
			return run, RunReparent(run)
		}
		run.prose("reparent: run %s for %q is already complete; removing residue\n", run.State.RunID, run.Loc.Feature)
		return run, run.runCleanup(ReparentStageCompleted)
	case ReparentStageFailed:
		// A failure is terminal for the previous ATTEMPT only: re-enter at
		// the stage it recorded and let the same ladder re-evaluate the cause.
		run.State.FailureDomain, run.State.FailureKind, run.State.FailureDetail = "", "", ""
		if err := run.setStage(run.State.ResumeStage); err != nil {
			return run, err
		}
	case ReparentStageConflictPaused:
		// handled by the forward machine's computing arm
	default:
		if run.State.ResumeStage != "" && run.State.ResumeStage != run.State.Stage {
			if err := run.setStage(run.State.ResumeStage); err != nil {
				return run, err
			}
		}
	}

	if err := run.verifyResumePreconditions(); err != nil {
		return run, err
	}
	return run, RunReparent(run)
}

// verifyResumePreconditions is §8.5's continue-route verification: the
// persisted pre-image values, not a token, are what a resume is checked
// against. Before the commit point a drifted ref is classified by §11.7 and a
// drifted stack.yaml refuses metadata-drift; after it, later movement is
// informational and must not block forward completion.
func (r *ReparentRun) verifyResumePreconditions() error {
	committed, err := r.evaluateCommitPoint()
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	switch r.State.Stage {
	case ReparentStageMetadataWritten, ReparentStageRestoringHolders, ReparentStageCleanup, ReparentStageCompleted:
		// These stages are only past the commit point when the CONJUNCTION
		// says so. Reaching them with the marker false means the run crashed
		// before it could be established, or something moved afterwards, so
		// they are gated rather than waved through: the gate refuses a
		// foreign ref and rewinds anything that is merely incomplete.
		_, err := r.requireCommittedRefs()
		return err
	}
	live, err := os.ReadFile(StackPath(r.Loc.FeaturePath))
	if err != nil {
		return reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml could not be read: %v", err))
	}
	sum := hex.EncodeToString(sliceSHA256(live))
	assessment, assessErr := assessReparentLiveRefs(r.RepoRoot, r.State, sum)
	if assessErr != nil {
		var refusal *ReparentRefusalError
		if errors.As(assessErr, &refusal) {
			return refusal
		}
		return reparentRefusal(ReparentRefusalProbeFailed, assessErr.Error())
	}
	for _, class := range assessment.Classes {
		if class.Class == ReparentRefForeign {
			if row := r.State.Row(class.Name); row != nil && row.PlannedNewSHA == "" {
				// Pending rows still own the stricter §8.3 JIT decision. Do
				// not collapse candidate/destination drift into a generic ref
				// classification before revalidateRow can name it.
				continue
			}
			return reparentEntryRefusal(ReparentRefusalRefForeignValue, class.Name, reparentForeignRefDetail(class))
		}
	}
	switch sum {
	case r.State.StackSHA256Before, r.State.StackSHA256AfterExpected, r.State.StackSHA256After:
		return nil
	}
	return reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
		"stack.yaml hashes to %s, which is neither this run's pre-image %s nor its planned post-image %s",
		sum, r.State.StackSHA256Before, r.State.StackSHA256AfterExpected))
}

// recoverCompatArtifacts is §11.2a. A --continue MAY re-create a missing
// compatibility artifact, but only after proving the run is still the run the
// state describes: the artifact decodes and matches (already proven by
// openReparentRecovery), its owner is not live (likewise), every closure row's
// live ref is pre-image or planned before the commit point, and the live
// stack.yaml hashes to one of the three values this run can account for.
func (r *ReparentRun) recoverCompatArtifacts() error {
	status := ClassifyReparentCompatArtifacts(r.Loc, r.State.RunID)
	if status.Foreign {
		return reparentRefusal(ReparentRefusalSyncStatePresent, fmt.Sprintf(
			"foreign sync state exists beside reparent run %s; resolve it before resuming", r.State.RunID))
	}
	if status.Complete {
		if err := confirmReparentCompatDurability(r.Loc); err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed,
				fmt.Sprintf("make the visible compatibility envelope durable: %v", err))
		}
		if !r.State.CompatArtifactsWritten {
			r.State.CompatArtifactsWritten = true
			return r.save()
		}
		return nil
	}
	committed, err := r.evaluateCommitPoint()
	if err != nil {
		return err
	}
	if !committed {
		classes, err := ClassifyReparentRefs(r.RepoRoot, r.State.RowsInOrder())
		if err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
		}
		for _, c := range classes {
			if c.Class == ReparentRefForeign {
				return reparentEntryRefusal(ReparentRefusalRefForeignValue, c.Name, reparentForeignRefDetail(c))
			}
		}
		if err := r.verifyResumePreconditions(); err != nil {
			return err
		}
	}
	selected := make([]string, 0, len(r.State.Rows))
	for _, row := range r.State.RowsInOrder() {
		selected = append(selected, row.Name)
	}
	r.prose("reparent: re-creating the missing compatibility artifacts for run %s\n", r.State.RunID)
	if err := WriteReparentCompatArtifacts(r.Loc, r.State, selected); err != nil {
		return err
	}
	r.State.CompatArtifactsWritten = true
	return r.save()
}

func confirmReparentCompatDurability(loc ReparentLocation) error {
	paths := []string{CheckoutTransactionPath(loc.FeaturePath)}
	if loc.Mode == ModeExternal {
		paths = []string{SyncRunStatePath(loc.FeaturePath), SyncStatePath(loc.FeaturePath)}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		dir := filepath.Dir(path)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if err := reparentRecoverySyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// AbortReparent is a bounded, non-destructive rollback before the commit
// point and a forward-cleanup command after it. It evaluates §11.8a from disk
// first and sets `aborting` before its first action.
func AbortReparent(in ReparentPlanInput) (*ReparentRun, error) {
	run, err := openReparentRecovery(in, ReparentRouteVerbAbort)
	if err != nil {
		return nil, err
	}
	cleanupResidue := run.State.Stage == ReparentStageCleanup ||
		run.State.Stage == ReparentStageCompleted ||
		run.State.Stage == ReparentStageAborted
	committed, err := run.evaluateCommitPoint()
	if err != nil {
		return run, err
	}
	if err := run.setStage(ReparentStageAborting); err != nil {
		return run, err
	}

	if committed {
		// Step 0 — forward completion only. No ref and no metadata moves, the
		// remote record is preserved unchanged, and a post-commit foreign ref
		// can never wedge artifact cleanup.
		run.abortForwardOnly = true
		run.prose("reparent: run %s had already committed; %s is now based on %s. Undoing it is a new reparent in the opposite direction\n",
			run.State.RunID, run.State.TargetName, run.State.NewParentStoredToken)
		if !cleanupResidue {
			if err := run.restoreHoldersForAbort(); err != nil {
				if errors.Is(err, errReparentHoldersDeferred) {
					return run, nil
				}
				return run, err
			}
		}
		return run, run.runCleanup(ReparentStageAborted)
	}

	ctx := run.contextDir()
	// Step 1 — a rebase in progress in the computation worktree.
	inProgress := false
	if ctx != "" {
		if _, statErr := os.Stat(ctx); statErr == nil {
			inProgress, err = reparentRebaseInProgress(ctx)
			if err != nil {
				return run, reparentRefusal(ReparentRefusalProbeFailed, err.Error())
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return run, reparentRefusal(ReparentRefusalProbeFailed, statErr.Error())
		}
	}
	if inProgress {
		for _, candidate := range run.State.RowsInOrder() {
			row := run.State.Row(candidate.Name)
			if row.Stage != ReparentRowPending {
				continue
			}
			if h := run.computationHolder(); h != nil {
				h.DetachTargetSHA = row.PreimageSHA
				h.DetachStatus = ReparentHolderRedetachIntent
				if err := run.save(); err != nil {
					return run, err
				}
			}
			break
		}
		if res, err := runReparentGit(ctx, nil, "rebase", "--abort"); err != nil {
			return run, reparentRefusal(ReparentRefusalProbeFailed, reparentGitMessage(res, err))
		}
		if h := run.computationHolder(); h != nil {
			h.DetachStatus = ReparentHolderRedetached
			if err := run.save(); err != nil {
				return run, err
			}
		}
	}
	if !inProgress {
		for _, candidate := range run.State.RowsInOrder() {
			row := run.State.Row(candidate.Name)
			if row.Stage != ReparentRowPending {
				continue
			}
			head, ok, headErr := reparentResolveRef(ctx, "HEAD")
			if headErr == nil && ok && head == row.PreimageSHA {
				if h := run.computationHolder(); h != nil {
					h.DetachTargetSHA = head
					h.DetachStatus = ReparentHolderDetached
					h.Restored = false
					if err := run.save(); err != nil {
						return run, err
					}
				}
			}
			break
		}
	}

	// Step 2 — classify every row BEFORE any ref is written.
	rows := run.State.RowsInOrder()
	if err := reparentRequireAllDirectBranchRefs(run.RepoRoot, rows); err != nil {
		return run, err
	}
	classes, err := ClassifyReparentRefs(run.RepoRoot, rows)
	if err != nil {
		return run, reparentRefusal(ReparentRefusalProbeFailed, err.Error())
	}
	for _, c := range classes {
		if c.Class == ReparentRefForeign {
			return run, reparentEntryRefusal(ReparentRefusalAbortForeignValue, c.Name, reparentForeignRefDetail(c))
		}
	}

	// Step 3 — re-detach any holder a completed restoring-holders stage put
	// back: moving a ref out from under an attached worktree would leave its
	// index and HEAD inconsistent.
	if err := run.redetachRestoredHolders(); err != nil {
		return run, err
	}

	// Step 4 — restore public refs in one CAS transaction.
	lines := reparentAbortCASLines(rows, ReparentRefClassMap(classes))
	if len(lines) > 0 {
		if err := reparentStep(ReparentStageAborting, "before-abort-cas"); err != nil {
			return run, err
		}
		if err := run.verifyPersistedHolderSnapshot(); err != nil {
			return run, err
		}
		if err := reparentRequireAllDirectBranchRefs(run.RepoRoot, rows); err != nil {
			return run, err
		}
		res, casErr := runReparentCAS(run.RepoRoot,
			reparentCASMessage(run.Loc.Feature, run.State.TargetName, run.State.RunID), lines)
		if casErr != nil {
			return run, &ReparentRefusalError{
				Kind:           ReparentRefusalRefTransactionMismatch,
				Detail:         reparentGitMessage(res, casErr),
				StatePreserved: true,
			}
		}
		run.State.AbortRows = run.State.AbortRows[:0]
		for _, c := range classes {
			run.State.AbortRows = append(run.State.AbortRows, ReparentStateAbortRow{
				Ref:            c.Ref,
				Restored:       c.Class == ReparentRefPlannedTip,
				Classification: string(c.Class),
			})
		}
		if err := run.save(); err != nil {
			return run, err
		}
	}

	// Step 5 — stack.yaml, per §11.8a.
	if err := run.restoreStackForAbort(); err != nil {
		return run, err
	}

	// Step 6 — re-attach holders and restore the checkout explicitly.
	if err := run.restoreHoldersForAbort(); err != nil {
		if errors.Is(err, errReparentHoldersDeferred) {
			return run, nil
		}
		return run, err
	}
	if err := reparentStep(ReparentStageAborting, "abort-holders-restored"); err != nil {
		return run, err
	}

	// Step 7 — restore the exact protection that existed before this run.
	if err := run.restoreRemoteRecordForAbort(); err != nil {
		return run, err
	}

	// Step 8 — the §11.6a cleanup order.
	return run, run.runCleanup(ReparentStageAborted)
}

func (r *ReparentRun) restoreRemoteRecordForAbort() error {
	if !r.State.RemoteRecordBeforeCaptured {
		return nil
	}
	if !r.State.RemoteRecordRestorePending {
		source, present, err := ReadReparentRemoteRecordBytes(r.Loc)
		if err != nil {
			return err
		}
		if err := r.validateAbortRemoteRecordSource(source, present); err != nil {
			return err
		}
		r.State.RemoteRecordRestoreSourceCaptured = true
		r.State.RemoteRecordRestoreSourcePresent = present
		r.State.RemoteRecordRestoreSourceBase64 = ""
		if present {
			r.State.RemoteRecordRestoreSourceBase64 = base64.StdEncoding.EncodeToString(source)
		}
		r.State.RemoteRecordRestorePending = true
		if err := r.save(); err != nil {
			return err
		}
		if err := reparentStep(ReparentStageAborting, "abort-remote-restore-intent-written"); err != nil {
			return err
		}
	}
	return r.reconcileAbortRemoteRecordRestore()
}

func (r *ReparentRun) reconcileAbortRemoteRecordRestore() error {
	path := ReparentRemoteRecordPath(r.Loc)
	expected, expectedPresent, err := r.abortRemoteRecordPreimage()
	if err != nil {
		return err
	}
	source, sourcePresent, err := r.abortRemoteRecordRestoreSource()
	if err != nil {
		return err
	}

	live, livePresent, err := readReparentRemoteRecordRaw(path)
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("inspect remote record before abort restore: %v", err))
	}
	switch {
	case reparentRemoteImageMatches(live, livePresent, expected, expectedPresent):
		if err := r.confirmAbortRemoteRecordImage(expected, expectedPresent); err != nil {
			return err
		}
	case reparentRemoteImageMatches(live, livePresent, source, sourcePresent):
		if expectedPresent {
			if err := WriteReparentRemoteRecordBytes(r.Loc, expected); err != nil {
				return err
			}
		} else if err := RemoveReparentRemoteRecord(r.Loc); err != nil {
			return err
		}
		if err := reparentStep(ReparentStageAborting, "abort-remote-restored-before-complete"); err != nil {
			return err
		}
		if err := r.confirmAbortRemoteRecordImage(expected, expectedPresent); err != nil {
			return err
		}
	default:
		return reparentRefusal(ReparentRefusalStateForeign,
			"the remote follow-up record changed after abort recorded its restore source")
	}

	r.State.RemoteRecordWritten = false
	r.State.RemoteRecordEmpty = false
	r.State.RemoteFollowupEntries = []string{}
	r.State.RemoteRecordRestorePending = false
	r.State.RemoteRecordRestoreSourceCaptured = false
	r.State.RemoteRecordRestoreSourcePresent = false
	r.State.RemoteRecordRestoreSourceBase64 = ""
	return r.save()
}

func (r *ReparentRun) abortRemoteRecordPreimage() ([]byte, bool, error) {
	if !r.State.RemoteRecordBeforePresent {
		return nil, false, nil
	}
	data, err := base64.StdEncoding.DecodeString(r.State.RemoteRecordBeforeBase64)
	if err != nil {
		return nil, false, reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the saved pre-run remote record does not decode: %v", err))
	}
	if _, err := decodeReparentRemoteRecordBytes(r.Loc, data); err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (r *ReparentRun) abortRemoteRecordRestoreSource() ([]byte, bool, error) {
	if !r.State.RemoteRecordRestoreSourceCaptured {
		return nil, false, reparentRefusal(ReparentRefusalStateCorrupt,
			"remote record restore is pending without a captured source image")
	}
	if !r.State.RemoteRecordRestoreSourcePresent {
		return nil, false, nil
	}
	data, err := base64.StdEncoding.DecodeString(r.State.RemoteRecordRestoreSourceBase64)
	if err != nil {
		return nil, false, reparentRefusal(ReparentRefusalStateCorrupt,
			fmt.Sprintf("the saved pre-restore remote record does not decode: %v", err))
	}
	if _, err := decodeReparentRemoteRecordBytes(r.Loc, data); err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (r *ReparentRun) validateAbortRemoteRecordSource(source []byte, present bool) error {
	expected, expectedPresent, err := r.abortRemoteRecordPreimage()
	if err != nil {
		return err
	}
	if reparentRemoteImageMatches(source, present, expected, expectedPresent) || !present {
		return nil
	}
	current, err := decodeReparentRemoteRecordBytes(r.Loc, source)
	if err != nil {
		return err
	}
	if current.RunID == r.State.RunID {
		return nil
	}
	if expectedPresent && reparentRemoteRecordIsClearProgress(r.Loc, expected, source) {
		return nil
	}
	return reparentRefusal(ReparentRefusalStateForeign,
		"the remote follow-up record changed ownership before abort could journal its restore")
}

func reparentRemoteRecordIsClearProgress(loc ReparentLocation, before, current []byte) bool {
	beforeRecord, err := decodeReparentRemoteRecordBytes(loc, before)
	if err != nil {
		return false
	}
	currentRecord, err := decodeReparentRemoteRecordBytes(loc, current)
	if err != nil || len(beforeRecord.Entries) != len(currentRecord.Entries) {
		return false
	}
	normalized := currentRecord
	normalized.Entries = append([]ReparentRemoteEntry(nil), currentRecord.Entries...)
	for i := range beforeRecord.Entries {
		switch {
		case reflect.DeepEqual(beforeRecord.Entries[i], normalized.Entries[i]):
		case beforeRecord.Entries[i].State == ReparentRemoteStatePending &&
			normalized.Entries[i].State == ReparentRemoteStateCleared:
			normalized.Entries[i].State = ReparentRemoteStatePending
			if !reflect.DeepEqual(beforeRecord.Entries[i], normalized.Entries[i]) {
				return false
			}
		default:
			return false
		}
	}
	return reflect.DeepEqual(beforeRecord, normalized)
}

func readReparentRemoteRecordRaw(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func reparentRemoteImageMatches(live []byte, livePresent bool, expected []byte, expectedPresent bool) bool {
	return livePresent == expectedPresent && (!livePresent || bytes.Equal(live, expected))
}

func (r *ReparentRun) confirmAbortRemoteRecordImage(expected []byte, present bool) error {
	action := "restored remote record"
	if !present {
		action = "removed remote record"
	}
	return confirmReparentRemoteImage(r.Loc, expected, present, action)
}

func confirmReparentRemoteImage(loc ReparentLocation, expected []byte, present bool, action string) error {
	path := ReparentRemoteRecordPath(loc)
	if err := confirmReparentRenameDurable(path); err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("make the %s durable: %v", action, err))
	}
	live, livePresent, err := readReparentRemoteRecordRaw(path)
	if err != nil {
		return reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("verify the %s: %v", action, err))
	}
	if !reparentRemoteImageMatches(live, livePresent, expected, present) {
		return reparentRefusal(ReparentRefusalStateForeign,
			fmt.Sprintf("the remote follow-up record changed while making the %s durable", action))
	}
	return nil
}

// restoreStackForAbort is §11.8a's metadata rule: a restore is permitted only
// when the live file is this run's pre-image (nothing to do) or its
// post-image while at least one ref still classifies pre-image (the CAS never
// fully landed). Any other hash refuses metadata-drift and aborts the abort.
func (r *ReparentRun) restoreStackForAbort() error {
	live, err := os.ReadFile(StackPath(r.Loc.FeaturePath))
	if err != nil {
		return reparentRefusal(ReparentRefusalMetadataDrift, err.Error())
	}
	sum := hex.EncodeToString(sliceSHA256(live))
	if sum == r.State.StackSHA256Before {
		if err := confirmReparentRenameDurable(StackPath(r.Loc.FeaturePath)); err != nil {
			return reparentRefusal(ReparentRefusalMetadataDrift,
				fmt.Sprintf("make the visible pre-image durable: %v", err))
		}
		return nil
	}
	if sum != r.State.StackSHA256AfterExpected && sum != r.State.StackSHA256After {
		return reparentRefusal(ReparentRefusalMetadataDrift, fmt.Sprintf(
			"stack.yaml hashes to %s, which is neither this run's pre-image %s nor its post-image %s; tws will not overwrite it",
			sum, r.State.StackSHA256Before, r.State.StackSHA256AfterExpected))
	}
	data, err := base64.StdEncoding.DecodeString(r.State.StackBeforeBase64)
	if err != nil {
		return reparentRefusal(ReparentRefusalMetadataDrift, err.Error())
	}
	if err := WriteStackBytesAtomic(r.Loc.FeaturePath, data); err != nil {
		return err
	}
	r.State.StackSHA256After = ""
	return r.save()
}

// redetachRestoredHolders is §11.8 step 3.
func (r *ReparentRun) redetachRestoredHolders() error {
	if err := r.reconcileHolderDetachIntents(); err != nil {
		return err
	}
	if err := r.verifyPersistedHolderSnapshot(); err != nil {
		return err
	}
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if !h.Restored {
			continue
		}
		sym, symErr := runReparentGit(h.Path, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
		if symErr != nil {
			if h.HolderKind == "computation-context" && r.State.OriginalDetached &&
				sym.ExitCode == 1 && r.State.OriginalHead != "" {
				head, ok, err := reparentResolveRef(h.Path, "HEAD")
				if err != nil {
					return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
				}
				if ok && head == r.State.OriginalHead {
					// A prior abort attempt already restored the originally
					// detached checkout and then crashed before cleanup.
					// It is not attached to any ref the rollback can move.
					continue
				}
			}
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"restored holder %s is no longer attached to recorded branch %s", h.Path, h.GitBranch))
		}
		if attached := strings.TrimSpace(string(sym.Stdout)); attached != h.GitBranch {
			return reparentRefusal(ReparentRefusalHolderUnsafe, fmt.Sprintf(
				"restored holder %s was switched from recorded branch %s to %s; tws will not overwrite the operator's checkout choice",
				h.Path, h.GitBranch, attached))
		}
		head, ok, err := reparentResolveRef(h.Path, "HEAD")
		if err != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, err.Error())
		}
		if !ok {
			continue
		}
		dirty, dirtyErr := measureReparentDirty(h.Path)
		if dirtyErr != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, dirtyErr.Error())
		}
		if len(dirty) > 0 {
			return reparentRefusal(ReparentRefusalContextDirty, fmt.Sprintf(
				"restored holder %s has tracked modifications: %s", h.Path, strings.Join(dirty, ", ")))
		}
		op, opErr := measureReparentGitOperation(h.Path)
		if opErr != nil {
			return reparentRefusal(ReparentRefusalProbeFailed, opErr.Error())
		}
		if op != "" {
			return reparentRefusal(ReparentRefusalGitOperationInProgress, fmt.Sprintf(
				"a %s is in progress in restored holder %s", op, h.Path))
		}
		if err := reparentUntrackedGate(h.Path, head); err != nil {
			return err
		}
		h.DetachTargetSHA = head
		h.DetachStatus = ReparentHolderRedetachIntent
		if err := r.beginHolderSwitch(h, ReparentHolderSwitchRedetach, "", head); err != nil {
			return err
		}
		if err := reparentStep(r.State.Stage, "holder-redetach-intent-written:"+h.Path); err != nil {
			return err
		}
		if res, err := runReparentGit(h.Path, nil, "switch", "--detach", head); err != nil {
			return reparentRefusal(ReparentRefusalHolderUnsafe, reparentGitMessage(res, err))
		}
		if err := r.completeHolderSwitch(h); err != nil {
			return err
		}
		if err := reparentStep(r.State.Stage, "holder-redetached-before-complete:"+h.Path); err != nil {
			return err
		}
	}
	return nil
}

// restoreHoldersForAbort re-attaches every recorded holder and restores the
// checkout's original branch or detached HEAD. A holder that cannot be
// re-attached warns holder-restore-deferred and is never forced.
func (r *ReparentRun) restoreHoldersForAbort() error {
	if err := r.verifyPersistedHolderSnapshot(); err != nil {
		return err
	}
	r.State.HolderRestoreDeferred = nil
	for i := range r.State.DetachedHolders {
		h := &r.State.DetachedHolders[i]
		if h.HolderKind == "computation-context" {
			continue
		}
		if h.Restored {
			continue
		}
		if err := r.restoreHolder(h); err != nil {
			h.RestoreReason = err.Error()
			r.State.HolderRestoreDeferred = appendUnique(r.State.HolderRestoreDeferred, h.Path)
			r.prose("reparent: %s: %s could not be re-attached to %s: %s\n",
				ReparentWarnHolderRestoreDeferred, h.Path, h.GitBranch, sanitizeReparentLine(err.Error()))
		}
	}
	if r.Loc.Mode == ModeCheckout {
		if err := r.restoreCheckoutContext(); err != nil {
			if h := r.computationHolder(); h != nil {
				h.RestoreReason = err.Error()
			}
			r.State.HolderRestoreDeferred = appendUnique(r.State.HolderRestoreDeferred, r.RepoRoot)
			r.prose("reparent: %s: the checkout could not be returned to %s: %s\n",
				ReparentWarnHolderRestoreDeferred, r.State.OriginalBranch, sanitizeReparentLine(err.Error()))
		} else if h := r.computationHolder(); h != nil {
			h.RestoreReason = ""
		}
	}
	if err := r.save(); err != nil {
		return err
	}
	if len(r.State.HolderRestoreDeferred) > 0 {
		r.prose("reparent: %s: %d holder(s) remain detached; authoritative state is preserved so `tws stack reparent %s --abort` can retry restoration\n",
			ReparentWarnHolderRestoreDeferred, len(r.State.HolderRestoreDeferred), r.Loc.Feature)
		return errReparentHoldersDeferred
	}
	return nil
}
