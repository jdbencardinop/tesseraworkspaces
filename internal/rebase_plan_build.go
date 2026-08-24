package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ============================================================================
// rebase_plan_build.go — the document assembler (§4, §9.1a, §14.4).
//
// BuildRebasePlan is the single assembler of the twenty-five-key RebasePlan
// document: it consumes RebasePlanRequest's already-measured facts (selection,
// order, state inspections, capabilities, config inventories, guard limits)
// and the pure decisions of internal/rebase_planner.go, and performs no
// filesystem probe of its own beyond the read-helpers it calls (the same
// internal/rebase_plan_probe.go primitives internal/rebase_planner.go already
// calls) — it never re-implements a probe, and it never re-implements §7.1/
// §7.2 precedence outside SelectPrimaryRefusal.
//
// RevalidatePlanEntry is the JIT re-probe entry point (§10.10): given one
// already-approved PlanEntry, it re-measures the entry's non-collateral
// mutable Git facts (destination/head SHA, upstream, candidates, strategy)
// and reports whether they still match the approved row via
// RevalidationDigest. Collateral-class re-measurement and the final
// admit/refuse judgment belong to the not-yet-existing
// internal/rebase_plan_guard.go's RevalidatePlanGuardEntry; this function
// only answers "did the mutable facts drift", never "should this run".
// ============================================================================

// ============================================================================
// planBuilder — the one per-invocation memoization scope. Every BuildRebasePlan
// call constructs exactly one of these; nothing here is shared across calls,
// and nothing here is a package-level cache.
// ============================================================================

type planBuilder struct {
	req RebasePlanRequest

	identities PlanContextIdentities // context_id -> identity, accumulated across every entry

	configByContext    map[string]PlanConfigResult      // context_id -> §11.7 config result
	holdersByCommonDir map[string]BranchHolderInventory // canonical common dir -> §7.9/§18.3 holder inventory
	refsByRoot         map[string]BranchRefInventory    // repoRoot -> for-each-ref inventory
	ancestryByRoot     map[string]map[string]StackEdge  // repoRoot -> entry name -> edge
	dirtyByRoot        map[string]*bool                 // repoRoot -> §18.3 tracked-only dirty probe
	untrackedByRoot    map[string]*bool                 // repoRoot -> §18.3 untracked-presence probe
	stackOwned         map[string]bool                  // short branch name -> in *req.Stack
	scopes             map[string]RepositoryConfigScope // context_id -> §11.7 scope, set once by aggregateScopes

	// jitSeamFor is the one entry name a JIT re-measurement is about, or ""
	// for an ordinary document build (§10.4).
	jitSeamFor string

	// §9's ResolveStackAncestryRepo ladder, resolved at most once.
	ancestryRepoResolved bool
	ancestryRepoDir      string
	ancestryRepoReason   string

	configIssues []PlanConfigIssue // accumulated, deduplicated by IssueID at the end
}

func newPlanBuilder(req RebasePlanRequest) *planBuilder {
	b := &planBuilder{
		req:                req,
		identities:         PlanContextIdentities{},
		configByContext:    map[string]PlanConfigResult{},
		holdersByCommonDir: map[string]BranchHolderInventory{},
		refsByRoot:         map[string]BranchRefInventory{},
		ancestryByRoot:     map[string]map[string]StackEdge{},
		dirtyByRoot:        map[string]*bool{},
		untrackedByRoot:    map[string]*bool{},
		stackOwned:         map[string]bool{},
	}
	if req.Stack != nil {
		for _, e := range req.Stack.Branches {
			b.stackOwned[e.GitBranch()] = true
		}
	}
	return b
}

// rememberIdentity folds one context identity into the document's one shared
// table (§14.1a), keyed by context_id. Re-measuring the same (repoRoot,
// rootSource, commonDir) tuple always collapses onto the same entry, which is
// the ordinary multi-worktree-stack case and never a violation.
func (b *planBuilder) rememberIdentity(identity PlanContextIdentity) {
	if identity.ContextID == "" {
		return
	}
	b.identities[identity.ContextID] = identity
}

// configFor returns the memoized §11.7 config result for one context,
// probing at most once per context_id per invocation.
func (b *planBuilder) configFor(dir, contextID string, repoRoot *string, scope RepositoryConfigScope) PlanConfigResult {
	if contextID == "" {
		return notEvaluatedConfigResult()
	}
	if res, ok := b.configByContext[contextID]; ok {
		return res
	}
	res := ProbeRepositoryConfig(dir, b.req.Capabilities, scope, contextID, repoRoot, "rebase")
	b.configByContext[contextID] = res
	b.configIssues = append(b.configIssues, res.Issues...)
	return res
}

// holdersFor returns the memoized worktree-holder inventory covering one
// repository root, keyed by the CANONICAL COMMON DIR that root belongs to
// (§7.9, §18.3, §14.4 rule 4) rather than by the root itself. Every linked
// worktree of one repository resolves to the same common dir and therefore
// shares ONE inventory: a three-row stack living in three linked worktrees of
// a single repository costs exactly one `git worktree list`, and three rows
// spread over two repositories cost exactly two. The inventory itself is
// always produced by BuildPlanHolderIndex — the single holder-index producer
// — so this cache and §14.4's index can never disagree about what "one
// repository" means.
func (b *planBuilder) holdersFor(repoRoot string) BranchHolderInventory {
	if repoRoot == "" {
		return BranchHolderInventory{}
	}
	ids, contextID := b.holderIdentityFor(repoRoot)
	commonDir := ids[contextID].CommonDir
	if inv, ok := b.holdersByCommonDir[commonDir]; ok {
		return inv
	}
	inv := BuildPlanHolderIndex(ids, []string{contextID}).ByContext[contextID]
	b.holdersByCommonDir[commonDir] = inv
	return inv
}

// holderIdentityFor picks the context identity this invocation ALREADY
// established for one repository root — never re-probing to learn a common
// dir — and falls back to a synthetic, root-keyed identity for a root no
// entry ever resolved, which keys that root on itself and so still costs at
// most one inventory.
func (b *planBuilder) holderIdentityFor(repoRoot string) (PlanContextIdentities, string) {
	canonical := canonicalPath(repoRoot)
	for id, identity := range b.identities {
		if identity.CommonDir == "" {
			continue
		}
		if identity.RepoRoot == repoRoot || identity.RepoRoot == canonical {
			return b.identities, id
		}
	}
	synthetic := "root:" + canonical
	return PlanContextIdentities{synthetic: {ContextID: synthetic, RepoRoot: repoRoot, CommonDir: canonical}}, synthetic
}

// branchRefsFor returns the memoized full local-branch-ref inventory for one
// repository root (§11.8's collateral_refs source), building it at most once
// per invocation.
func (b *planBuilder) branchRefsFor(repoRoot string) BranchRefInventory {
	if repoRoot == "" {
		return BranchRefInventory{}
	}
	if inv, ok := b.refsByRoot[repoRoot]; ok {
		return inv
	}
	inv := BuildBranchRefInventory(repoRoot)
	b.refsByRoot[repoRoot] = inv
	return inv
}

// dirtyFor returns the memoized §18.3 row-dirtiness probe for one execution
// directory: Git's own rebase precondition (tracked-only, submodule-
// ignoring), never the shipped whole-repo `state.worktree.dirty` gate, which
// stays a separate, document-level fact. nil on any probe failure.
func (b *planBuilder) dirtyFor(repoRoot string) *bool {
	if repoRoot == "" {
		return nil
	}
	if v, ok := b.dirtyByRoot[repoRoot]; ok {
		return v
	}
	v, err := probeRowDirty(repoRoot)
	if err != nil {
		v = nil
	}
	b.dirtyByRoot[repoRoot] = v
	return v
}

// untrackedFor returns the memoized §18.3 untracked-presence probe for one
// canonical worktree top level. nil on any probe failure.
func (b *planBuilder) untrackedFor(repoRoot string) *bool {
	if repoRoot == "" {
		return nil
	}
	if v, ok := b.untrackedByRoot[repoRoot]; ok {
		return v
	}
	v, err := probeUntrackedPresent(repoRoot)
	if err != nil {
		v = nil
	}
	b.untrackedByRoot[repoRoot] = v
	return v
}

// ancestryFor returns the memoized per-entry StackEdge table for one
// repository root, evaluating the whole stack's ancestry against it at most
// once per invocation (§9's Ancestry group). The evaluator fails soft
// (EvaluateStackAncestry's own contract): a repository that cannot be
// evaluated yields an empty map, and callers read that empty result as no
// ancestry fact for every entry rooted there, never as a false ancestry
// claim.
// ancestryRepo resolves §9's own ResolveStackAncestryRepo candidate ladder
// ONCE per invocation, from the pieces RebasePlanRequest really carries: the
// workspace mode and repository root, the feature's metadata directory (and
// therefore the metadata root), and the stack. It returns "" with the
// ladder's own reason where no source repository could be resolved.
func (b *planBuilder) ancestryRepo() (string, string) {
	if b.ancestryRepoResolved {
		return b.ancestryRepoDir, b.ancestryRepoReason
	}
	b.ancestryRepoResolved = true
	if b.req.Stack == nil {
		return "", "no stack was readable for this invocation"
	}
	repoRoot := b.req.Workspace.RepoRoot
	if repoRoot == "" {
		repoRoot = b.req.Layout.RepoRoot
	}
	metadataRoot := ""
	if b.req.Layout.FeaturePath != "" {
		metadataRoot = filepath.Dir(b.req.Layout.FeaturePath)
	}
	ws := Workspace{RepoRoot: repoRoot, Mode: b.req.Mode, MetadataRoot: metadataRoot, StableID: derefString(b.req.Workspace.StableID)}
	res := ResolveStackAncestryRepo(ws, LoadConfig(), b.req.Layout.FeaturePath, *b.req.Stack)
	b.ancestryRepoDir, b.ancestryRepoReason = res.RepoDir, res.Reason
	return b.ancestryRepoDir, b.ancestryRepoReason
}

func (b *planBuilder) ancestryFor(repoRoot string) map[string]StackEdge {
	if repoRoot == "" || b.req.Stack == nil {
		return nil
	}
	if edges, ok := b.ancestryByRoot[repoRoot]; ok {
		return edges
	}
	opts := StackAncestryOptions{BasePolicy: StackBasePolicyForMode(b.req.Mode)}
	edges, err := EvaluateStackAncestry(repoRoot, b.req.Feature, *b.req.Stack, opts)
	byName := map[string]StackEdge{}
	if err == nil {
		for _, e := range edges {
			byName[e.Name] = e
		}
	}
	b.ancestryByRoot[repoRoot] = byName
	return byName
}

// mergeBaseIsAncestor runs `git merge-base --is-ancestor <ancestor> <descendant>`
// and distinguishes its two meaningful exit codes (0 == true, 1 == false)
// from a genuine probe failure (any other exit status or exec error), which
// runGit's own convention cannot do since it treats every non-zero exit as an
// error. It is the one primitive internal/rebase_plan_build.go adds beyond
// internal/rebase_plan_probe.go's own helpers, needed only for §11.8's
// collateral-range membership test.
func mergeBaseIsAncestor(dir, ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// asExitError is errors.As's *exec.ExitError specialisation, kept as its own
// tiny helper so mergeBaseIsAncestor reads as a single expression above.
func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

// probeRowDirty is §18.3's row-dirtiness predicate: Git's own rebase
// precondition — tracked-only, submodule-ignoring — run with
// GIT_OPTIONAL_LOCKS=0 so it never writes a refreshed index. The
// diff-files/diff-index pair MUST NOT be used (it marks stat-dirty,
// content-identical files as dirty); this is deliberately its own, separate
// probe from stack_status.go's probeDirty, which answers the shipped
// whole-repo `state.worktree.dirty` question with different flags.
func probeRowDirty(repoRoot string) (*bool, error) {
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain", "--untracked-files=no", "--ignore-submodules=all")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	dirty := len(strings.TrimSpace(string(out))) > 0
	return &dirty, nil
}

// probeUntrackedPresent is §18.3's untracked-presence predicate: one
// `ls-files --others --exclude-standard -z` per canonical worktree top
// level, warning-only and never a dry-run overwrite simulation (§18.3, §24).
func probeUntrackedPresent(repoRoot string) (*bool, error) {
	cmd := exec.Command("git", "-C", repoRoot, "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	present := len(out) > 0
	return &present, nil
}

// ============================================================================
// Collateral (§11.8) — collateral_refs, collateral_mechanism and the
// document-level collateral_exposed post-pass.
// ============================================================================

// entryCollateralInput bundles one entry's already-resolved facts
// computeCollateral needs; it performs no probing of its own beyond the
// memoized branch-ref/holder inventories and the range membership test.
type entryCollateralInput struct {
	RepoRoot               string // execution context repo root; "" when unusable
	OwnGitBranch           string
	UpstreamSHA            string // "" when replay.upstream_sha is null
	BranchSHA              string // this row's own head.sha; "" when unresolved
	ArgvHasUpdateRefs      bool
	RebaseUpdateRefsConfig *bool   // effective rebase.updateRefs, decoded; nil when not evaluated/invalid
	EffectiveBackend       *string // merge | apply | nil
	Repo                   string  // StackEntry.Repo token, for PlanCollateralRef.Repo
}

// computeCollateral is §11.8's algorithm: collateral_mechanism is answered
// from argv/config/backend alone (argv > config > none), independent of
// whether the branch-ref inventory below can be read; collateral_refs is the
// complete local branch-ref inventory whose tips lie inside the candidate
// range, minus worktree-held refs, sorted (repo, ref) — null on any probe
// failure. Both members collapse to null together in the one case §11.8
// names explicitly: rebase.updateRefs configured true but effective_backend
// unresolvable.
func (b *planBuilder) computeCollateral(in entryCollateralInput) ([]PlanCollateralRef, *string) {
	if in.RebaseUpdateRefsConfig != nil && *in.RebaseUpdateRefsConfig && in.EffectiveBackend == nil {
		return nil, nil
	}

	mech := "none"
	switch {
	case in.ArgvHasUpdateRefs:
		mech = "argv"
	case in.RebaseUpdateRefsConfig != nil && *in.RebaseUpdateRefsConfig &&
		in.EffectiveBackend != nil && *in.EffectiveBackend == "merge":
		mech = "config"
	}
	mechanism := &mech

	if in.RepoRoot == "" || in.UpstreamSHA == "" || in.BranchSHA == "" {
		return nil, mechanism
	}
	refInv := b.branchRefsFor(in.RepoRoot)
	if !refInv.Available {
		return nil, mechanism
	}
	holders := b.holdersFor(in.RepoRoot)
	ownRef := "refs/heads/" + in.OwnGitBranch

	refs := make([]string, 0, len(refInv.ByRef))
	for ref := range refInv.ByRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	out := []PlanCollateralRef{}
	for _, ref := range refs {
		if ref == ownRef {
			continue
		}
		rec := refInv.ByRef[ref]
		short := strings.TrimPrefix(ref, "refs/heads/")
		if holders.Available {
			if _, held := holders.ByBranch[short]; held {
				continue // "minus refs held by any worktree"
			}
		}
		// §11.8's membership test is RANGE membership — the tip is inside
		// `<upstream>..<branch>` — which is exactly
		// `isAncestor(tip, branch) && !isAncestor(tip, upstream)`, the same
		// predicate `git rev-list <upstream>..<branch>` itself applies.
		//
		// The reverse spelling (`isAncestor(upstream, tip)`) is NOT the same
		// question and gets two cells wrong: it OMITS a ref whose tip is
		// reachable from the branch but not descended from the upstream (a
		// merged side branch — a ref `--update-refs` really does move), and it
		// INCLUDES the recorded cutoff itself, which is the range's excluded
		// left endpoint and is never replayed. The process budget is
		// unchanged: at most two `merge-base --is-ancestor` per ref, with the
		// cheap short-circuit first.
		belowBranch, err := mergeBaseIsAncestor(in.RepoRoot, rec.ObjectID, in.BranchSHA)
		if err != nil {
			return nil, mechanism
		}
		if !belowBranch {
			continue
		}
		atOrBelowUpstream, err := mergeBaseIsAncestor(in.RepoRoot, rec.ObjectID, in.UpstreamSHA)
		if err != nil {
			return nil, mechanism
		}
		if atOrBelowUpstream {
			continue // at or below the range's excluded left endpoint
		}
		out = append(out, PlanCollateralRef{
			Repo:       in.Repo,
			Ref:        ref,
			SHA:        rec.ObjectID,
			StackOwned: b.stackOwned[short],
		})
	}
	return out, mechanism
}

// ============================================================================
// entryPrep — pass A of the two-pass per-entry build: every fact that does
// not depend on repositories[].config, gathered once per selected entry so
// pass B (finishEntry) never re-measures Git state a second time and so
// per-context RepositoryConfigScope (§11.7) can be aggregated between the
// passes.
// ============================================================================

type entryPrep struct {
	Entry          StackEntry
	OE             OrderedExecution
	CtxResult      EntryContextResult
	BaseResult     ResolveSyncBaseResult
	Skip           string
	ContextUsable  bool
	CurrentBaseSHA string
	HeadSHA        string
	HeadState      string // present | missing | unresolvable
	CheckoutOnto   bool
	Role           string
	ParentName     string
	Deferred       bool
	ProbeStrategy  RebaseStrategyResult // config-independent; realness/backend-shape only

	// §13.3a's `switched`-stage pinned-destination arm. SwitchedPinned is
	// true only for the one checkout row resuming exactly at that stage,
	// whose destination is the PERSISTED NewBaseSHA and whose upstream is
	// the PERSISTED LastBaseSHA — neither re-resolved, because the executor
	// itself consumes the pinned values (§10.3's "where that command
	// consumes a value pinned in persisted state, the seam compares against
	// THAT pinned value").
	// DirtEndangersThisArm is true exactly where this row's own remaining
	// work really runs a rebase or a standalone checkout over the working
	// tree, which is every external row, every fresh checkout row, and the
	// switched/planned/rebasing continuation stages (§13.3b).
	DirtEndangersThisArm bool

	SwitchedPinned  bool
	PinnedObjGone   bool   // the pinned NewBaseSHA no longer names a commit (rank 5.7)
	HeadIdentity    string // the branch HEAD is really on, "" when unmeasurable
	HeadIdentityBad bool   // §13.3a rule 5: HEAD is not on the persisted branch
}

// determineSkip resolves three of the four skipped-* tokens from
// stack/selection/order facts alone (§9.3): skipped-anchor (a local-only
// anchor), skipped-archived (checkout's BuildCheckoutPlan excludes an
// archived entry outright) and skipped-updated-ref (a non-materialized
// external row whose ref already moves transitively via an earlier pass-1
// --update-refs rebase). skipped-no-base is deliberately NOT decided here:
// it depends on base resolution, and only checkout skips on it at all
// (external still executes plain/plain-explicit-branch over the empty base
// token) — RebaseStrategy itself owns that mode-gated decision (§9.3).
func determineSkip(mode WorkspaceMode, entry StackEntry, oe OrderedExecution) string {
	if oe.SkipAnchor {
		return "skipped-anchor"
	}
	if entry.Archived {
		if mode == ModeCheckout {
			return "skipped-archived"
		}
		if oe.UpdatedByRef {
			return "skipped-updated-ref"
		}
		return ""
	}
	if mode == ModeExternal && (oe.Materialization == "worktree-missing" || oe.Materialization == "prunable") {
		if oe.UpdatedByRef {
			return "skipped-updated-ref"
		}
		return ""
	}
	return ""
}

// isRealStrategy reports whether a Group C strategy token actually invokes
// `git rebase` (§11.7's scope rule): every token except the four skipped-*
// ones and unknown.
func isRealStrategy(strategy string) bool {
	return strategy != "unknown" && !strings.HasPrefix(strategy, "skipped-")
}

// prepareEntry is pass A for one selected entry: it measures every fact
// RebaseStrategy/ReplayUpstream/collateral need except repositories[].config,
// which is not yet known (config scope is only decided once every entry's
// realness has been measured, §11.7).
func (b *planBuilder) prepareEntry(oe OrderedExecution, sel SyncSelectedEntry, execution []OrderedExecution) entryPrep {
	entry := oe.Entry
	ctx := EntryContexts(EntryContextInput{Entry: entry, Mode: b.req.Mode, Layout: b.req.Layout, Selection: b.req.Selection})
	if ctx.ExecutionErr == nil {
		b.rememberIdentity(ctx.ExecutionIdentity)
	}
	contextUsable := ctx.ExecutionErr == nil && ctx.ExecutionDir != ""

	baseResult := ResolveSyncBase(*b.req.Stack, entry, ctx.ExecutionDir)
	skip := determineSkip(b.req.Mode, entry, oe)

	// §13.3a: decide the pinned-destination arm FIRST, because on it
	// entry.Base is never resolved at all — the executor consumes the
	// persisted NewBaseSHA, so re-resolving the ref here and then refusing
	// because it moved would be a spurious revalidation-mismatch (§10.3).
	switchedRow, switchedPinned := b.switchedPinnedRow(entry)

	var currentBaseSHA, headSHA string
	if contextUsable {
		if baseResult.Kind != "none" && !switchedPinned {
			currentBaseSHA = GetBranchSHA(ctx.ExecutionDir, baseResult.Base)
		}
		headSHA = GetBranchSHA(ctx.ExecutionDir, "refs/heads/"+entry.GitBranch())
	}
	headState := "unresolvable"
	if contextUsable {
		if headSHA != "" {
			headState = "present"
		} else {
			headState = "missing"
		}
	}

	// The pinned arm's own facts: the destination is the persisted
	// NewBaseSHA, checked for existence with exactly ONE
	// `rev-parse --verify <sha>^{commit}` probe (rule 6); the upstream is the
	// persisted LastBaseSHA; and rule 5's HEAD identity is measured here.
	pinnedGone := false
	headIdentity, headIdentityBad := "", false
	if switchedPinned {
		if switchedRow.NewBaseSHA != "" {
			// The destination stays the persisted SHA even when the object is
			// gone: §10.1 decides source range, destination and strategy
			// SEPARATELY, so a destroyed destination is a rank 5.7 fact about
			// the destination alone and never turns the row's own recorded
			// cutoff into an unknown source.
			currentBaseSHA = switchedRow.NewBaseSHA
			if exists, err := ProbeCommitExists(b.req.Layout.RepoRoot, switchedRow.NewBaseSHA); err == nil && !exists {
				pinnedGone = true
			}
		}
		entry.LastBaseSHA = switchedRow.LastBaseSHA
		if hb := ResolveHeadBranch(b.req.Layout.RepoRoot); hb != nil {
			headIdentity = *hb
		}
		headIdentityBad = headIdentity != switchedRow.Branch
	}

	checkoutOnto := false
	if b.req.Mode == ModeCheckout {
		checkoutOnto = entry.LastBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA
	}

	// §10.4's `jit-deferred` indeterminacy policy: a deferred destination is
	// deferred only for a row an EARLIER row of this run has not yet
	// rewritten. At the JIT seam of the row itself, every earlier row has
	// already executed, so the destination really does resolve and the count
	// really is measurable — which is the entire point of deferring it
	// rather than refusing up front. jitSeamFor names that one row.
	deferred := DestinationDeferred(string(sel.Role), sel.ParentName, execution)
	if b.jitSeamFor != "" && entry.Name == b.jitSeamFor {
		deferred = false
	}
	if switchedPinned {
		// A pinned destination cannot be deferred: it is already decided, and
		// no earlier row of this run can rewrite a recorded SHA.
		deferred = false
	}

	probe := RebaseStrategy(RebaseStrategyInput{
		Mode: b.req.Mode, Skip: skip, Pass: oe.Pass, GitBranch: entry.GitBranch(),
		BaseResolved: baseResult.Kind != "none", Base: baseResult.Base,
		LastBaseSHA: entry.LastBaseSHA, CurrentBaseSHA: currentBaseSHA,
		HeadUsable: headState == "present", Scoped: b.req.Selection.Policy.Scoped(),
		BaseMayMoveBeforeExecution:  baseResult.IsRemoteTracking && b.req.Policy.Fetch == SyncFetchEnabled,
		ContextUsable:               contextUsable,
		CheckoutOnto:                checkoutOnto,
		CapDefaultBackendMerge:      b.req.Capabilities.CapDefaultBackendMerge,
		CapDefaultBackendMergeKnown: b.req.Version.OK,
		// The PREPARATION pass runs above the per-context config inventory
		// (which the scope aggregation this pass feeds is itself an input
		// to), so no inventory has been read yet. Row 2 therefore applies
		// here by construction, and this probe's EffectiveBackend is used
		// only to aggregate the argv-derived facts of rows 1/6/7 — the
		// final, inventory-aware value is computed in the build pass below.
		BackendConfigReadable: false,
	})

	return entryPrep{
		Entry: entry, OE: oe, CtxResult: ctx, BaseResult: baseResult, Skip: skip,
		ContextUsable: contextUsable, CurrentBaseSHA: currentBaseSHA, HeadSHA: headSHA,
		HeadState: headState, CheckoutOnto: checkoutOnto, Role: string(sel.Role),
		ParentName: sel.ParentName, Deferred: deferred, ProbeStrategy: probe,
		DirtEndangersThisArm: b.dirtEndangersArm(entry),
		SwitchedPinned:       switchedPinned, PinnedObjGone: pinnedGone,
		HeadIdentity: headIdentity, HeadIdentityBad: headIdentityBad,
	}
}

// aggregateScopes turns every prepared entry's context and probed strategy
// into the closed per-context RepositoryConfigScope (§11.7) the config probe
// needs. All three members are derived from the row's OWN published argv and
// from the version capability alone — never from the inventory this scope is
// itself an input to, which would be circular:
//
//   - Rebase: any real strategy sharing that context_id.
//   - NonForcingRebasePresent: at least one entry whose published argv
//     effective_backend row 1 would NOT already force to merge — i.e. an
//     entry whose rebase.backend value would actually be consulted. That is
//     exactly !argvForcesMergeBackend over the row's own argv, so a
//     `scope=all` run's pass-2 rows (no --update-refs) set it while its
//     pass-1 rows do not.
//   - MergeBackendActive: at least one entry that runs under
//     effective_backend: merge INDEPENDENTLY of the inventory. Rows 1 and 5
//     are the two inventory-independent merge answers: a merge-forcing argv
//     forces merge outright, and an entry above the 2.26 gate defaults to
//     merge unless a configured rebase.backend says otherwise. A configured
//     `apply` can therefore only ever REMOVE this flag, and the two issues
//     it gates (rebase.abbreviateCommands, rebase.maxLabelLength) are
//     collected for any context that may run under merge, so an issue the
//     merge backend would really hit is never dropped.
func (b *planBuilder) aggregateScopes(preps []entryPrep) map[string]RepositoryConfigScope {
	type agg struct {
		rebase, nonForcing, mergeActive bool
	}
	byContext := map[string]agg{}
	for _, p := range preps {
		if p.CtxResult.ExecutionErr != nil || p.CtxResult.ExecutionIdentity.ContextID == "" {
			continue
		}
		cid := p.CtxResult.ExecutionIdentity.ContextID
		a := byContext[cid]
		if isRealStrategy(p.ProbeStrategy.Strategy) {
			a.rebase = true
			forcing := argvForcesMergeBackend(publishedArgv(p.ProbeStrategy))
			if !forcing {
				a.nonForcing = true
			}
			if forcing || (b.req.Capabilities.CapDefaultBackendMerge && b.req.Version.OK) {
				a.mergeActive = true
			}
		}
		byContext[cid] = a
	}
	out := make(map[string]RepositoryConfigScope, len(byContext))
	for cid, a := range byContext {
		out[cid] = RepositoryConfigScope{Rebase: a.rebase, NonForcingRebasePresent: a.nonForcing, MergeBackendActive: a.mergeActive}
	}
	return out
}

// ============================================================================
// Small shared conversions.
// ============================================================================

// derefString returns "" for a nil pointer, the pointed-to value otherwise.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// argvHasFlag reports whether one already-tokenized argv carries flag as an
// exact token (§16 rule 3b's own argv-derived predicate).
func argvHasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// boolConfigValue decodes a PlanConfigSlot's "true"/"false" string value
// (§2.11's PlanConfigSlot.Value convention) into a *bool, nil whenever the
// slot is not a validly-read boolean.
func boolConfigValue(slot PlanConfigSlot) *bool {
	if slot.Status != "valid" || slot.Value == nil {
		return nil
	}
	v := *slot.Value == "true"
	return &v
}

// ============================================================================
// Per-entry Group C/E sub-builders.
// ============================================================================

// buildCutoff is entries[].cutoff (Group C). State stays null exactly in the
// two cases §10.1/§4.2 name: the context was never usable, or
// this row is skipped-updated-ref (its cutoff was never probed because the
// row never runs and its ref moves by another row's --update-refs instead).
// Every other row — including the other three skipped-* tokens — still has
// its recorded cutoff verified, independent of Usage, which answers a
// different question (did the computed strategy actually consult it).
func (b *planBuilder) buildCutoff(p entryPrep, strategy, usage string) PlanEntryCutoff {
	write := "per-entry"
	if b.req.Mode == ModeCheckout {
		write = "at-finalization"
	}
	if !isRealStrategy(strategy) {
		write = "never"
	}
	c := PlanEntryCutoff{Provenance: "none", Usage: usage, Write: write}
	if p.Entry.LastBaseSHA == "" {
		state := "absent"
		c.State = &state
		return c
	}
	recorded := p.Entry.LastBaseSHA
	c.RecordedSHA = &recorded
	c.Provenance = "recorded-by-sync"
	if !p.ContextUsable || p.Skip == "skipped-updated-ref" {
		return c
	}
	if err := VerifyGitRef(p.CtxResult.ExecutionDir, recorded); err != nil {
		state := "unresolvable"
		c.State = &state
		return c
	}
	state := "present"
	c.State = &state
	resolved := recorded
	c.ResolvedSHA = &resolved
	return c
}

// buildMutation is entries[].mutation (Group E), spec.md §4.2 Group E
// row for will_switch_head/will_leave_head_on/head_restored_by_run: external
// pass 1 never switches HEAD (the worktree is already dedicated to that
// branch), external pass 2 does (`git rebase <base> <branch>` implicitly
// checks the named branch out first); checkout switches on every row except
// the one row resuming exactly at StageSwitched (its own standalone checkout
// already ran in the invocation that was interrupted, so this run's very
// next command is the rebase, not another checkout). head_restored_by_run is
// an unconditional per-mode fact — checkout's whole-transaction
// restoreOriginal always runs at finalization, external never switches the
// shared/main repository's HEAD at all — independent of any one row's own
// skip/strategy status.
func (b *planBuilder) buildMutation(p entryPrep, strategy string) PlanEntryMutation {
	m := PlanEntryMutation{
		WillSwitchHead:    b.willSwitchHead(p, strategy),
		HeadRestoredByRun: b.req.Mode == ModeCheckout,
	}
	if m.WillSwitchHead {
		branch := p.Entry.GitBranch()
		m.WillLeaveHeadOn = &branch
	}
	return m
}

// willSwitchHead is the real, mode/pass/stage-gated rule above; see
// buildMutation's doc comment for its justification.
func (b *planBuilder) willSwitchHead(p entryPrep, strategy string) bool {
	if !isRealStrategy(strategy) {
		return false
	}
	if b.req.Mode == ModeExternal {
		return p.OE.Pass == 2
	}
	if idx, tx, ok := b.currentIndexEntry(p); ok && tx.Stage == StageSwitched {
		_ = idx
		return false
	}
	return true
}

// checkoutTransaction returns the persisted continuation transaction, nil on
// a fresh run or whenever it could not be read (both cases fall back to
// treating every row as an ordinary, not-yet-started arm).
func (b *planBuilder) checkoutTransaction() *CheckoutTransaction {
	if !b.req.Continue {
		return nil
	}
	f := b.req.CheckoutState.Files.CheckoutTransaction
	if f.Presence != PlanPresenceReadable {
		return nil
	}
	return f.Transaction
}

// currentIndexEntry reports whether p is the transaction's own
// currently-resuming row (tx.Plan[tx.CurrentIndex]), matched by name.
// dirtEndangersArm is §13.3b's own stage rule for the context-dirty verdict:
// only an arm that really runs a rebase or a standalone checkout over the
// working tree can be endangered by tracked dirt.
func (b *planBuilder) dirtEndangersArm(entry StackEntry) bool {
	if b.req.Mode == ModeExternal || !b.req.Continue {
		return true
	}
	tx := b.checkoutTransaction()
	if tx == nil || tx.CurrentIndex < 0 || tx.CurrentIndex >= len(tx.Plan) {
		return true
	}
	if tx.Plan[tx.CurrentIndex].Name != entry.Name {
		return true // a later, not-yet-reached index still runs its own checkout
	}
	return checkoutStageAutostashApplies(tx.Stage)
}

// switchedPinnedRow returns the persisted plan row for the ONE entry resuming
// exactly at Stage: switched, and whether this entry is that row.
func (b *planBuilder) switchedPinnedRow(entry StackEntry) (CheckoutPlanEntry, bool) {
	tx := b.checkoutTransaction()
	if tx == nil || tx.Stage != StageSwitched {
		return CheckoutPlanEntry{}, false
	}
	if tx.CurrentIndex < 0 || tx.CurrentIndex >= len(tx.Plan) {
		return CheckoutPlanEntry{}, false
	}
	row := tx.Plan[tx.CurrentIndex]
	if row.Name != entry.Name {
		return CheckoutPlanEntry{}, false
	}
	return row, true
}

func (b *planBuilder) currentIndexEntry(p entryPrep) (int, *CheckoutTransaction, bool) {
	tx := b.checkoutTransaction()
	if tx == nil || tx.CurrentIndex < 0 || tx.CurrentIndex >= len(tx.Plan) {
		return 0, tx, false
	}
	if tx.Plan[tx.CurrentIndex].Name != p.Entry.Name {
		return 0, tx, false
	}
	return tx.CurrentIndex, tx, true
}

// buildEntryContext is entries[].context (Group E). dirty/untracked_present/
// overwrite_risk are real, dedicated per-row probes over the execution
// context's own directory (§18.3: tracked-only/submodule-ignoring status for
// dirty, never the shipped whole-repo state.worktree.dirty gate, which stays
// a separate document-level fact); rebase_in_progress is external-only
// (checkout's own equivalent is the document-level state.git_op); autostash/
// autostash_applies_to_this_arm/autostash_reapply_may_conflict are the real
// per-context config value and the §18.4 mode/pass/stage rule (the latter two
// share one nullability and one value — "equals applicability").
func (b *planBuilder) buildEntryContext(p entryPrep, strategy string, cfg PlanConfigResult) PlanEntryContext {
	c := PlanEntryContext{}
	if !p.ContextUsable {
		return c
	}
	dir := p.CtxResult.ExecutionDir

	if dirty := b.dirtyFor(dir); dirty != nil {
		c.Dirty = dirty
	}

	c.Autostash = boolConfigValue(cfg.AutoStash)
	if applies := b.autostashApplies(p, strategy, cfg.AutoStash); applies != nil {
		c.AutostashAppliesToThisArm = applies
		conflict := *applies
		c.AutostashReapplyMayConflict = &conflict
	}

	if b.req.Mode == ModeExternal {
		holders := b.holdersFor(dir)
		if holders.Available {
			rec, held := holders.ByBranch[p.Entry.GitBranch()]
			inProgress := held && (rec.Mechanism == HoldRebaseMerge || rec.Mechanism == HoldRebaseApply)
			c.RebaseInProgress = &inProgress
		}
	}

	if untracked := b.untrackedFor(dir); untracked != nil {
		c.UntrackedPresent = untracked
		risk := "none-known"
		if *untracked {
			risk = "unknown"
		}
		c.OverwriteRisk = &risk
	}
	return c
}

// autostashApplies is §18.4's mode/pass/stage table, evaluated only for rows
// that actually run a rebase (a skipped/unknown row has no arm at all, so
// autostash structurally cannot apply to it — a known false, not an
// unknown). External pass 1/2 are unconditionally true; a checkout run that
// is not a continuation at all is unconditionally false (the shipped
// whole-repo dirty gate would already have refused the run before any lock,
// fetch or rebase); a continuation's current-index row applies exactly at
// StageSwitched/StagePlanned/StageRebasing (every other stage's arm runs no
// new rebase for that row) and every later index applies (its own standalone
// checkout has not run yet, so the row behaves like an ordinary fresh arm).
func (b *planBuilder) autostashApplies(p entryPrep, strategy string, autoStash PlanConfigSlot) *bool {
	if !isRealStrategy(strategy) {
		f := false
		return &f
	}
	// §22.13e: an INVALID rebase.autoStash publishes null for both autostash
	// fields and no verdict at all — the question was asked and could not be
	// answered, which is not the same as "false".
	if autoStash.Status == "invalid" || autoStash.Status == "probe-failed" {
		return nil
	}
	var applies bool
	switch {
	case b.req.Mode == ModeExternal:
		applies = true
	case !b.req.Continue:
		applies = false
	default:
		if _, tx, ok := b.currentIndexEntry(p); ok {
			switch tx.Stage {
			case StageSwitched, StagePlanned, StageRebasing:
				applies = true
			default:
				applies = false
			}
		} else {
			applies = true // a later, not-yet-reached index
		}
	}
	// The arm can only autostash where Git itself would: an unset or false
	// rebase.autoStash means the shipped rebase carries no autostash, so no
	// arm of this run is covered by one (§18.4).
	if configured := boolConfigValue(autoStash); configured == nil || !*configured {
		applies = false
	}
	return &applies
}

// branchCheckedOutAt is entries[].branch_checked_out_at: the foreign
// worktree path holding this entry's own branch checked out, nil on
// self-exclusion (this entry's own expected location holding its own branch
// is not "elsewhere") and nil whenever the holder inventory could not be
// read at all.
func (b *planBuilder) branchCheckedOutAt(p entryPrep) *string {
	repoRoot := p.CtxResult.ExecutionDir
	if repoRoot == "" {
		return nil
	}
	holders := b.holdersFor(repoRoot)
	if !holders.Available {
		return nil
	}
	rec, held := holders.ByBranch[p.Entry.GitBranch()]
	if !held || rec.Mechanism != HoldCheckedOut {
		return nil
	}
	ownLocation := repoRoot
	if b.req.Mode == ModeExternal {
		ownLocation = b.req.Layout.WorktreePath(p.Entry.Name)
	}
	if rec.Worktree == ownLocation {
		return nil
	}
	wt := rec.Worktree
	return &wt
}

// buildAncestry is entries[].ancestry: {status, reason}, consumed verbatim
// from the memoized per-repository StackEdge table (EvaluateStackAncestry,
// internal/stack_ancestry.go), evaluated in the repository §9's own
// ResolveStackAncestryRepo candidate ladder selects — feature-scoped
// worktree evidence, then the resolved workspace repository root, then the
// metadata-root inference — never in this entry's execution directory,
// which would answer a different question for a multi-repository stack.
// The ladder is resolved ONCE per invocation and memoized.
func (b *planBuilder) buildAncestry(p entryPrep) PlanAncestry {
	root, reason := b.ancestryRepo()
	if root == "" {
		_ = reason
		return PlanAncestry{Reason: ReasonRepoUnavailable}
	}
	edges := b.ancestryFor(root)
	edge, ok := edges[p.Entry.Name]
	if !ok {
		return PlanAncestry{Reason: ReasonAncestryProbeFailed}
	}
	status := edge.Status
	return PlanAncestry{Status: &status, Reason: edge.Reason}
}

// ============================================================================
// finishEntry — pass B: assembles one full PlanEntry from its entryPrep plus
// the now-known, memoized per-context config result.
// ============================================================================

func (b *planBuilder) finishEntry(p entryPrep) (PlanEntry, []PlanBlocker) {
	entry := p.Entry

	cfg := notEvaluatedConfigResult()
	if p.CtxResult.ExecutionErr == nil && p.CtxResult.ExecutionIdentity.ContextID != "" {
		scope := b.scopes[p.CtxResult.ExecutionIdentity.ContextID]
		var repoRootPtr *string
		if p.CtxResult.ExecutionContext.RepoRoot != nil {
			rr := *p.CtxResult.ExecutionContext.RepoRoot
			repoRootPtr = &rr
		}
		cfg = b.configFor(p.CtxResult.ExecutionDir, p.CtxResult.ExecutionIdentity.ContextID, repoRootPtr, scope)
	}

	strategyIn := RebaseStrategyInput{
		Mode: b.req.Mode, Skip: p.Skip, Pass: p.OE.Pass, GitBranch: entry.GitBranch(),
		BaseResolved: p.BaseResult.Kind != "none", Base: p.BaseResult.Base,
		LastBaseSHA: entry.LastBaseSHA, CurrentBaseSHA: p.CurrentBaseSHA,
		HeadUsable: p.HeadState == "present", Scoped: b.req.Selection.Policy.Scoped(),
		BaseMayMoveBeforeExecution: p.BaseResult.IsRemoteTracking && b.req.Policy.Fetch == SyncFetchEnabled,
		ContextUsable:              p.ContextUsable,
		CheckoutOnto:               p.CheckoutOnto,
		// Row 2's input: "the ordered config inventory for this execution
		// context could not be read". probe-failed is exactly that cell;
		// not-evaluated is the rebase-out-of-scope context, which likewise
		// has no inventory to consult.
		BackendConfigReadable:       cfg.Backend.Status != "probe-failed" && cfg.Backend.Status != "not-evaluated",
		BackendConfigValid:          cfg.Backend.Status == "valid",
		CapDefaultBackendMerge:      b.req.Capabilities.CapDefaultBackendMerge,
		CapDefaultBackendMergeKnown: b.req.Version.OK,
	}
	if strategyIn.BackendConfigValid && cfg.Backend.Value != nil {
		strategyIn.BackendConfigValue = *cfg.Backend.Value
	}
	strat := RebaseStrategy(strategyIn)

	rebaseMergesValid := cfg.RebaseMerges.Status == "valid"
	rebaseMergesRecreates := rebaseMergesValid && cfg.RebaseMerges.Interpretation == "true"
	// cutoff.usage names whether this arm really hands the recorded cutoff to
	// Git: only the `--onto <base> <cutoff>` arm and the conditional arm whose
	// alternatives include it do. Every plain arm — external pass 2 among them
	// — is `not_used`.
	cutoffUsage := "not_used"
	if strat.Strategy == "onto" || strat.Strategy == "conditional" {
		cutoffUsage = "used"
	}
	cutoff := b.buildCutoff(p, strat.Strategy, cutoffUsage)

	replay := ReplayUpstream(ReplayUpstreamInput{
		Skipped:                 !isRealStrategy(strat.Strategy),
		RebaseMergesConfigValid: rebaseMergesValid,
		RebaseMergesRecreates:   rebaseMergesRecreates,
		ContextUsable:           p.ContextUsable,
		ExecDir:                 p.CtxResult.ExecutionDir,
		HeadUsable:              p.HeadState == "present",
		GitBranch:               entry.GitBranch(),
		BaseUnset:               p.BaseResult.Kind == "none",
		BaseRefMissing:          p.BaseResult.Kind != "none" && p.CurrentBaseSHA == "",
		UpstreamRef:             p.BaseResult.Base,
		UpstreamSHA:             p.CurrentBaseSHA,
		Deferred:                p.Deferred,
		CutoffUsage:             cutoffUsage,
		CutoffState:             derefString(cutoff.State),
		CutoffResolvedSHA:       derefString(cutoff.ResolvedSHA),
	})

	// destination.sha is the commit the replay lands on. A deferred
	// destination has none yet — the earlier row rewrites it — so the row
	// publishes only the explicit snapshot.
	dest := PlanEntryDestination{Deferred: p.Deferred}
	if p.CurrentBaseSHA != "" {
		snap := p.CurrentBaseSHA
		dest.SnapshotSHA = &snap
		if !p.Deferred {
			sha := p.CurrentBaseSHA
			dest.SHA = &sha
		}
	}
	if p.Deferred {
		parent := p.ParentName
		dest.DependsOn = &parent
	}

	head := PlanEntryHead{}
	headState := p.HeadState
	head.State = &headState
	if p.HeadSHA != "" {
		sha := p.HeadSHA
		head.SHA = &sha
		short := shortSHA(p.HeadSHA)
		head.Short = &short
	}

	base := PlanEntryBase{Kind: p.BaseResult.Kind}
	if entry.Base != "" {
		name := entry.Base
		base.Name = &name
	}
	if p.BaseResult.Kind != "none" {
		ref := p.BaseResult.Base
		base.Ref = &ref
	}
	if p.CurrentBaseSHA != "" && !p.Deferred && p.Skip != "skipped-updated-ref" {
		sha := p.CurrentBaseSHA
		base.DecisionSHA = &sha
	}

	// §16 rule 3b: the 2.38 CapRebaseUpdateRefs gate is argv-DERIVED. A row
	// whose OWN published argv carries --update-refs cannot run at all on a
	// host below that gate (or on a host whose version could not be
	// established), so the controlled route refuses with rank 5.9
	// probe-failed ABOVE the config inventory; the row's effective_backend is
	// then null by §11.7 row 2 and its collateral facts are null (§11.8) —
	// no document may publish a row-1 verdict from an argv the host would
	// reject.
	hostRunsUpdateRefs := b.req.Version.OK && b.req.Capabilities.CapRebaseUpdateRefs
	updateRefsGated := argvHasFlag(publishedArgv(strat), "--update-refs") && !hostRunsUpdateRefs
	if updateRefsGated {
		strat.EffectiveBackend = nil
	}

	var collRefs []PlanCollateralRef
	var collMech *string
	if !updateRefsGated {
		collRefs, collMech = b.computeCollateral(entryCollateralInput{
			RepoRoot:               p.CtxResult.ExecutionDir,
			OwnGitBranch:           entry.GitBranch(),
			UpstreamSHA:            derefString(replay.UpstreamSHA),
			BranchSHA:              p.HeadSHA,
			ArgvHasUpdateRefs:      argvHasFlag(strat.Argv, "--update-refs"),
			RebaseUpdateRefsConfig: boolConfigValue(cfg.UpdateRefs),
			EffectiveBackend:       strat.EffectiveBackend,
			Repo:                   entry.Repo,
		})
	}

	out := PlanEntry{
		Name:             entry.Name,
		GitBranch:        entry.GitBranch(),
		Repo:             entry.Repo,
		Role:             p.Role,
		Materialization:  p.OE.Materialization,
		BaseContext:      p.CtxResult.BaseContext,
		ExecutionContext: p.CtxResult.ExecutionContext,

		Base:        base,
		Destination: dest,
		Head:        head,

		Cutoff:            cutoff,
		Strategy:          strat.Strategy,
		StrategyCondition: strat.Condition,
		Argv:              strat.Argv,
		ArgvAlternatives:  strat.ArgvAlternatives,
		EffectiveBackend:  strat.EffectiveBackend,

		Replay: PlanEntryReplay(replay),

		CollateralRefs:      collRefs,
		CollateralMechanism: collMech,
		Mutation:            b.buildMutation(p, strat.Strategy),
		Context:             b.buildEntryContext(p, strat.Strategy, cfg),
		BranchCheckedOutAt:  b.branchCheckedOutAt(p),
		Prunability:         PlanEntryPrunability{ProbeContext: "cwd"},
		Ancestry:            b.buildAncestry(p),
		Notes:               []string{},
	}
	// §16 rule 3b has NO per-row variant: the 2.38 gate is a DOCUMENT-LEVEL
	// (entry: null) rank 5.9 fact, raised above the fetch by CapabilityGates
	// (internal/rebase_plan_guard.go). This row still nulls its own
	// effective_backend and collateral facts, which is what `updateRefsGated`
	// governs here — it never mints a second blocker.
	return out, entryBlockers(p, out, isRealStrategy(strat.Strategy))
}

// entryBlockers is the entry-scoped subset of §7.1's precedence table
// (ranks 5.1-5.8: repo-unavailable, base-execution-store-divergent,
// head-ref-missing, prunable-worktree, branch-checked-out-elsewhere,
// external-rebase-in-progress, context-dirty, base-unset, base-ref-missing,
// cutoff-unresolvable). Every other RefusalKind is evaluated at the document
// level (ranks 1-4, 5.07, 5.08, 5.9, 6-12). Raw facts only: sort order and
// cross-rank collapse both live solely in SelectPrimaryRefusal, never here.
func entryBlockers(p entryPrep, entry PlanEntry, real bool) []PlanBlocker {
	name := entry.Name
	var out []PlanBlocker
	add := func(kind RefusalKind, detail string) {
		out = append(out, PlanBlocker{Kind: kind, Entry: &name, Detail: detail})
	}

	if !p.ContextUsable {
		add(RefusalRepoUnavailable, "execution context for "+name+" could not be established")
		return out // no context ⇒ every other row-level probe below is meaningless
	}
	if p.CtxResult.BaseErr != nil {
		// A base context that fails to establish is rank 5.1 too: §4.2 nulls
		// only that context's own members, and the row is still blocking.
		add(RefusalRepoUnavailable, "base context for "+name+" could not be established")
	}
	if p.CtxResult.StoreDivergent {
		add(RefusalBaseExecutionStoreDivergent, "base and execution contexts for "+name+" resolve to different object stores")
	}
	if p.SwitchedPinned && p.PinnedObjGone {
		// §13.3a rule 6: `--onto <persisted NewBaseSHA>` fails outright if the
		// object is gone. That is rank 5.7 on the row — the SAME kind an
		// unresolvable base takes — and never a new refusal kind, and it is
		// raised even though the row's own strategy is `unknown` without a
		// resolvable destination.
		add(RefusalBaseRefMissing, "the persisted destination for "+name+" no longer names a commit")
	}
	if !real {
		return out // a row that structurally cannot execute contributes no further hazard
	}
	if entry.Head.State == nil || *entry.Head.State != "present" {
		add(RefusalHeadRefMissing, "branch "+entry.GitBranch+" has no resolvable HEAD")
	}
	if entry.Materialization == "prunable" {
		add(RefusalPrunableWorktree, "worktree for "+name+" is prunable")
	}
	if entry.BranchCheckedOutAt != nil && entry.Mutation.WillSwitchHead {
		add(RefusalBranchCheckedOutElsewhere, "branch "+entry.GitBranch+" is checked out at "+*entry.BranchCheckedOutAt)
	}
	if entry.Context.RebaseInProgress != nil && *entry.Context.RebaseInProgress {
		add(RefusalExternalRebaseInProgress, "a rebase is already in progress in "+name+"'s execution directory")
	}
	dirty := entry.Context.Dirty != nil && *entry.Context.Dirty
	autostashOK := entry.Context.AutostashAppliesToThisArm != nil && *entry.Context.AutostashAppliesToThisArm
	// §22.13e: the stages whose remaining work runs NO rebase or checkout over
	// the dirty tree — conflict, rebased, validating, completed — publish no
	// context-dirty blocker of their own, and an unreadable rebase.autoStash
	// (a null applies field) publishes no verdict at all.
	unreadableAutostash := entry.Context.AutostashAppliesToThisArm == nil
	if dirty && !autostashOK && !unreadableAutostash && p.DirtEndangersThisArm {
		add(RefusalContextDirty, "tracked changes are present in "+name+"'s execution directory and no autostash covers this arm")
	}
	if entry.Base.Kind == "none" {
		add(RefusalBaseUnset, name+" has no configured base")
	} else if !entry.Destination.Deferred && entry.Destination.SHA == nil {
		// A deferred destination is not a missing ref: an earlier row of this
		// same run rewrites it, and destination.sha is null by construction
		// (§4.2). Only an undeferred row with no resolution is rank 5.7.
		add(RefusalBaseRefMissing, "configured base for "+name+" does not resolve")
	}
	if entry.Cutoff.State != nil && *entry.Cutoff.State == "unresolvable" {
		add(RefusalCutoffUnresolvable, "recorded cutoff for "+name+" does not resolve")
	}
	if p.SwitchedPinned && p.HeadIdentityBad {
		// §13.3a rule 5: the `switched` arm's next command is the rebase of
		// whatever HEAD is on, so a HEAD that is not the persisted branch
		// would rebase the wrong thing. Rank 4, on the row, naming both.
		measured := p.HeadIdentity
		if measured == "" {
			measured = "a detached HEAD"
		}
		add(RefusalPreflightRefused, "resuming "+name+" requires HEAD on the persisted branch "+entry.GitBranch+", but HEAD is on "+measured)
	}
	return out
}

// ============================================================================
// buildEntries — orchestrates both passes over the run's execution order,
// then the two document-wide post-passes §11.8/§4.8 require: collateral_
// exposed (execution-ordered, global) and the final alphabetical publication
// sort (§4.1).
// ============================================================================

func (b *planBuilder) buildEntries() ([]PlanEntry, []OrderedExecution, []PlanBlocker, []entryPrep) {
	// §13.7 rule 4 / §13.7a rule 4: RowsAvailable is the sole answer to "may
	// this document publish entries[]?", and RowsAvailable == true is the
	// ONLY case in which req.Stack/req.Order are guaranteed non-nil (fresh
	// arms require Stack != nil && SortErr == nil; the continuation arm
	// requires a decoded transaction). A rows-less document must never
	// dereference a possibly-nil Stack/Order, so it short-circuits here to
	// the empty, entries-less shape instead.
	if !b.req.RowsAvailable {
		return nil, nil, nil, nil
	}

	// ExecutionOrder always runs over the FULL selection (never Remaining):
	// its own ancestor-chain UpdatedByRef walk (§10) needs the complete
	// topology, including rows a continuation has already finished, to
	// decide which still-pending ref an in-range --update-refs rebase really
	// carries along. req.Remaining — §13.3's own already-computed answer,
	// never re-derived here (rule 3) — then narrows which of those rows this
	// invocation actually publishes and treats as "still to run" for every
	// downstream, run-scoped decision (DestinationDeferred among them).
	full := ExecutionOrder(b.req.Mode, *b.req.Stack, b.req.Order, b.req.Layout, b.req.Selection)
	execution := full
	if b.req.Continue {
		remaining := make(map[string]bool, len(b.req.Remaining))
		for _, n := range b.req.Remaining {
			remaining[n] = true
		}
		filtered := make([]OrderedExecution, 0, len(full))
		for _, oe := range full {
			if remaining[oe.Entry.Name] {
				filtered = append(filtered, oe)
			}
		}
		execution = filtered
	}

	selByName := make(map[string]SyncSelectedEntry, len(b.req.Selection.Entries))
	for _, s := range b.req.Selection.Entries {
		selByName[s.Name] = s
	}

	preps := make([]entryPrep, 0, len(execution))
	for _, oe := range execution {
		preps = append(preps, b.prepareEntry(oe, selByName[oe.Entry.Name], execution))
	}

	b.scopes = b.aggregateScopes(preps)

	entries := make([]PlanEntry, 0, len(preps))
	var blockers []PlanBlocker
	for _, p := range preps {
		entry, eb := b.finishEntry(p)
		entries = append(entries, entry)
		blockers = append(blockers, eb...)
	}

	// collateral_exposed (§11.8): a global pass in EXECUTION order — this
	// row's own tip is checked against every STRICTLY EARLIER row's
	// contributed collateral_refs before this row's own refs join the set.
	byName := make(map[string]int, len(entries))
	for i, e := range entries {
		byName[e.Name] = i
	}
	seen := map[string]bool{}
	for _, p := range preps {
		idx, ok := byName[p.Entry.Name]
		if !ok {
			continue
		}
		e := &entries[idx]
		if e.Head.SHA != nil {
			exposed := seen[*e.Head.SHA]
			e.CollateralExposed = &exposed
		}
		for _, ref := range e.CollateralRefs {
			seen[ref.SHA] = true
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, execution, blockers, preps
}

// ============================================================================
// buildState — the producer→document state.* projection (§2.20, §12.5a).
// ExternalPlanState/CheckoutPlanState (internal/rebase_plan_state.go) are the
// measured producer-side snapshots this run already inspected exactly once
// (InspectExternalPlanState / InspectCheckoutPlanState); PlanState
// (internal/rebase_plan.go) is the smaller document-facing shape every route
// publishes, regardless of mode — the inapplicable side's rows are already
// {applicable:false, presence:not-applicable} on the producer, so no mode
// branch is needed inside each row projector, only in the top-level pick of
// which producer struct's five rows to read.
//
// NOTE: spec.md §4.5's decoded-member tables for the five state.files.*
// rows describe richer shapes (e.g. nine members for checkout_transaction)
// than PlanStateFileCheckoutTransaction and its four siblings actually
// declare in rebase_plan.go. Those Go structs are pre-existing and are not
// redeclared here; every projector below fills exactly the fields the real
// struct has, nothing more.
// ============================================================================

// projectFileBase converts the producer's three-member {Applicable,
// Presence, UnreadableReason} header into the document's string-typed twin.
func projectFileBase(p PlanFilePresence) PlanStateFileBase {
	var reason *string
	if p.UnreadableReason != nil {
		r := string(*p.UnreadableReason)
		reason = &r
	}
	// §12.5a rule 5: the INAPPLICABLE side's rows are
	// {applicable:false, presence:not-applicable}. A producer struct this
	// route never measured arrives as its zero value, whose Presence is the
	// empty string — never a published value — so it is normalized here, at
	// the one projection every row passes through.
	presence := string(p.Presence)
	if presence == "" {
		presence = string(PlanPresenceNotApplicable)
	}
	return PlanStateFileBase{Applicable: p.Applicable, Presence: presence, UnreadableReason: reason}
}

// projectCheckoutTransactionFile projects state.files.checkout_transaction.
func projectCheckoutTransactionFile(f PlanCheckoutTransactionFile) PlanStateFileCheckoutTransaction {
	out := PlanStateFileCheckoutTransaction{PlanStateFileBase: projectFileBase(f.PlanFilePresence)}
	switch {
	case f.Transaction != nil:
		v := f.Transaction.StateVersion
		out.StateVersion = &v
		feat := f.Transaction.Feature
		out.Feature = &feat
	case f.StateVersion != nil:
		// The §12.5a rescue case: a future-version or partially-corrupt
		// document that still yielded a bare state_version.
		out.StateVersion = f.StateVersion
	}
	return out
}

// projectCheckoutLockFile projects state.files.checkout_lock. OwnerHost
// stays permanently nil: LockInfo (internal/checkout_sync.go) records only
// pid and created, never a host.
func projectCheckoutLockFile(f PlanCheckoutLockFile) PlanStateFileCheckoutLock {
	out := PlanStateFileCheckoutLock{PlanStateFileBase: projectFileBase(f.PlanFilePresence), OwnerLive: f.Alive}
	if f.Lock != nil {
		pid := f.Lock.PID
		out.OwnerPID = &pid
		created := f.Lock.Created
		out.AcquiredAt = &created
	}
	return out
}

// projectLegacyStateFile projects state.files.external_legacy_state.
// Feature stays permanently nil: SyncState (internal/syncstate.go) has no
// feature member — the legacy sentinel predates per-feature tracking, and
// the feature name is implicit from the directory the file was read from.
func projectLegacyStateFile(f PlanLegacySyncStateFile) PlanStateFileExternalLegacyState {
	return PlanStateFileExternalLegacyState{PlanStateFileBase: projectFileBase(f.PlanFilePresence)}
}

// projectPayloadFile projects state.files.external_run_payload.
func projectPayloadFile(f PlanSyncRunPayloadFile) PlanStateFileExternalPayload {
	out := PlanStateFileExternalPayload{PlanStateFileBase: projectFileBase(f.PlanFilePresence), Selected: []string{}}
	if f.Payload != nil {
		feat := f.Payload.Feature
		out.Feature = &feat
		if len(f.Payload.Selected) > 0 {
			out.Selected = append([]string{}, f.Payload.Selected...)
		}
	}
	return out
}

// projectRunGuardFile projects state.files.external_run_guard.
func projectRunGuardFile(f PlanSyncRunGuardFile) PlanStateFileExternalRunGuard {
	out := PlanStateFileExternalRunGuard{PlanStateFileBase: projectFileBase(f.PlanFilePresence), OwnerLive: f.Alive}
	if f.Guard != nil {
		pid := f.Guard.PID
		out.OwnerPID = &pid
	}
	return out
}

// buildState projects RebasePlanRequest.CheckoutState/ExternalState (exactly
// one of which is Applicable per §13.7a rule 8) into RebasePlan.State. It
// performs no filesystem probe of its own: every value is already sitting on
// the request, measured once by InspectCheckoutPlanState/
// InspectExternalPlanState above this builder.
func (b *planBuilder) buildState() PlanState {
	cs, es := b.req.CheckoutState, b.req.ExternalState

	files := PlanStateFiles{
		ExternalLegacyState: projectLegacyStateFile(es.Files.LegacyState),
		ExternalRunPayload:  projectPayloadFile(es.Files.Payload),
		ExternalRunGuard:    projectRunGuardFile(es.Files.RunGuard),
		CheckoutTransaction: projectCheckoutTransactionFile(cs.Files.CheckoutTransaction),
		CheckoutLock:        projectCheckoutLockFile(cs.Files.CheckoutLock),
	}

	cell := PlanStateExternalCell{Applies: es.Applicable}
	if es.Applicable {
		c := es.Cell
		cell.Cell = &c
	}

	worktree := PlanStateWorktree{Applies: cs.Worktree.Applies, Dirty: cs.Worktree.Dirty}
	if cs.Worktree.Applies {
		ok := cs.Worktree.ProbeOK
		worktree.ProbeOK = &ok
	}

	gitOp := PlanStateGitOp{Applies: cs.GitOp.Applies}
	if cs.GitOp.Applies {
		inProgress := cs.GitOp.InProgress
		gitOp.InProgress = &inProgress
		kind := cs.GitOp.Kind
		gitOp.Kind = &kind
		if cs.GitOp.KindSource != "" {
			source := cs.GitOp.KindSource
			gitOp.KindSource = &source
		}
	}

	head := PlanStateHead{Applies: cs.Head.Applies}
	if cs.Head.Applies {
		detached := cs.Head.Detached
		head.Detached = &detached
		if !cs.Head.Detached && cs.Head.Branch != "" {
			branch := cs.Head.Branch
			head.Branch = &branch
		}
	}

	return PlanState{
		Snapshot:     b.stateSnapshot(),
		Files:        files,
		ExternalCell: cell,
		Worktree:     worktree,
		GitOp:        gitOp,
		Head:         head,
	}
}

// stateSnapshot picks whichever inspector actually ran (§2.20: "whichever
// inspector ran") since exactly one of CheckoutState/ExternalState is
// Applicable per invocation.
func (b *planBuilder) stateSnapshot() PlanStateSnapshot {
	snap := b.req.ExternalState.Snapshot
	if b.req.Mode == ModeCheckout {
		snap = b.req.CheckoutState.Snapshot
	}
	return PlanStateSnapshot(snap)
}

// ============================================================================
// buildFetch — RebasePlan.Fetch (§2.4/§2.5).
//
// checkout mode consumes the checkout-plan-only route's own pre-fetch
// enumeration (req.FetchPlan) and measured attempt outcome (req.Fetch),
// producing the real fetch/freshness facts §11 describes. External mode has
// no analogous producer in this tree (BuildRebasePlan's only two production
// callers, BuildCheckoutRebasePlan/BuildCheckoutContinuationPlan, are both
// checkout-only), so it keeps the pre-existing hardcoded "never attempted,
// local-only" shape verbatim — a caller that never populates
// req.Fetch/req.FetchPlan observes no change in this builder's output.
// ============================================================================

// buildFetch publishes the fetch object of §4.4a for both workspace modes from
// this invocation's own fetch outcome. A route whose effective policy performs
// no fetch at all is not a suppression: it publishes `attempted: false`,
// `suppression_cause: null` and `repos: []`, which §11.3 renders as
// `freshness: local-only`.
func (b *planBuilder) buildFetch(preps []entryPrep) PlanFetch {
	_ = preps
	return b.buildFetchObject()
}

// buildCheckoutFetch is buildFetch's checkout-mode arm (§2.4-§2.6, §11).
// cause is the full six-cause §11.2 precedence chain (never just
// req.FetchPlan's own narrower cause-4/5/6 signal, which cannot see
// continuation/rows/gate/live-lock facts); repos is the exact per-cause
// shape §4.4a fixes: [] for causes 1-3, the whole measured checkout row for
// causes 5-6 (the specification itself declares cause 4 unreachable on this
// mode: checkout resolves exactly one candidate, and a single candidate is
// always trivially equal to itself, §11.1/§11.4), and either
// [] (never attempted) or the real measured row for the unsuppressed case.
func (b *planBuilder) buildFetchObject() PlanFetch {
	cause := b.fetchSuppressionCause()
	repos := b.fetchDocRepoRows(cause)

	attempted := false
	for _, r := range repos {
		if r.Attempted {
			attempted = true
			break
		}
	}

	var suppressionPtr *string
	if cause != "" {
		c := cause
		suppressionPtr = &c
	}

	return PlanFetch{
		Attempted:                 attempted,
		Outcome:                   fetchOutcomeString(repos),
		PolicySource:              b.fetchPolicySource(),
		SuppressionCause:          suppressionPtr,
		MutatedRemoteTrackingRefs: fetchMutatedRemoteTrackingRefs(repos),
		MutatedLocalBranches:      fetchMutatedLocalBranches(repos),
		Repos:                     repos,
	}
}

// fetchSuppressionCause is the full, six-cause §11.2 precedence chain: cause
// 1 (any --continue) outranks cause 2 (no plannable subject, or any
// pre-fetch gate the described executor raises above its own fetch —
// checkoutPreconditionGates' own three rows and I14's own BasePreflight),
// which outranks cause 3 (a pre-existing live foreign lock, the same
// LiveForeignLock fact gateVerdicts' own rank-3 row reads), which outranks
// causes 4-6 (already fully disambiguated by checkoutFetchPlan's own
// Suppressed field — cause 4 never fires for checkout's single-candidate
// route, §11.1). "" means none of the six causes applies: the caller still
// decides between a genuine fetch, local-only, and not-refreshed-no-fetch-targets.
func (b *planBuilder) fetchSuppressionCause() string {
	switch {
	case b.req.Continue:
		return "not-refreshed-continuation"
	case b.fetchPolicyEnabled() && (!b.req.RowsAvailable || gatesSuppressFetch(b.req.Gates) || b.req.BasePreflight.Failed):
		// §11.2 cause 2 is a SUPPRESSION: it says a fetch this route's own
		// policy called for could not be reached. On a route whose effective
		// policy is `no-fetch` nothing was suppressed at all — that document
		// is `local-only` with a null cause — so the whole clause is gated on
		// the effective policy rather than on the gate alone (§11.2, §16).
		return "not-refreshed-no-plan-subject"
	case b.liveForeignOwner():
		return "not-refreshed-live-run"
	default:
		return b.req.FetchPlan.Suppressed
	}
}

// fetchPolicySource is fetch.policy_source (§2.4): checkout's own
// PolicyFetchDefaultApplied already distinguishes an explicit --fetch/
// --no-fetch flag from the route's own default; a continuation resumes the
// persisted transaction's own policy rather than reading a fresh flag.
// persisted-payload is external-only (never returned here, mirroring
// intent.push_source's own domain carve-out, §4.6).
// liveForeignOwner reports the mode-appropriate live foreign owner fact: the
// checkout lock in checkout mode, the external run guard in external mode.
// fetchPolicyEnabled is this document's own effective fetch policy: the one
// question that decides whether an unreachable fetch is a suppression at all.
func (b *planBuilder) fetchPolicyEnabled() bool {
	return b.req.Policy.Fetch == SyncFetchEnabled
}

func (b *planBuilder) liveForeignOwner() bool {
	if b.req.Mode == ModeCheckout {
		return b.req.CheckoutState.LiveForeignLock()
	}
	return b.req.ExternalState.LiveForeignOwner()
}

func (b *planBuilder) fetchPolicySource() string {
	if b.req.Continue {
		return "persisted-transaction"
	}
	if b.req.PolicyFetchDefaultApplied {
		return "route-default"
	}
	return "flag"
}

// fetchDocRepoRows builds fetch.repos[] for a checkout-mode request per
// §4.4a's own per-cause shape rule: [] for causes 1-3; cause 4 (unreachable
// here) would keep tokens/candidates only; causes 5-6 keep the WHOLE
// pre-measured row from FetchPlan.Contexts (measured before the decision,
// §11.1); the unsuppressed case is [] when never attempted (local-only /
// no-fetch-targets) or the real measured row from req.Fetch.Repos.
func (b *planBuilder) fetchDocRepoRows(cause string) []PlanFetchRepo {
	switch cause {
	case "not-refreshed-continuation", "not-refreshed-no-plan-subject", "not-refreshed-live-run":
		return []PlanFetchRepo{}
	case "not-refreshed-submodule-reach-indeterminate", "not-refreshed-local-branch-checked-out":
		if len(b.req.FetchPlan.Contexts) == 0 {
			return []PlanFetchRepo{}
		}
		ctx := b.req.FetchPlan.Contexts[0]
		return []PlanFetchRepo{{
			RepoToken:         ctx.RepoToken,
			ContextRoot:       ctx.Root,
			ContextCommonDir:  ctx.CommonDir,
			ContextSource:     ctx.Source,
			Effect:            ctx.Effect,
			ContextCandidates: ensureSlice(ctx.Candidates),
			Attempted:         false,
			OK:                false,
		}}
	}
	if !b.req.Fetch.Attempted {
		return []PlanFetchRepo{}
	}
	out := make([]PlanFetchRepo, 0, len(b.req.Fetch.Repos))
	for _, r := range b.req.Fetch.Repos {
		out = append(out, PlanFetchRepo{
			RepoToken:         r.RepoToken,
			ContextRoot:       r.ContextRoot,
			ContextCommonDir:  r.ContextCommonDir,
			ContextSource:     r.ContextSource,
			Effect:            r.Effect,
			ContextCandidates: ensureSlice(r.ContextCandidates),
			Attempted:         r.Attempted,
			OK:                r.OK,
		})
	}
	return out
}

// fetchOutcomeString is fetch.outcome (§2.4): skipped when no row attempted
// a fetch; ok/failed/partial by the attempted rows' own OK verdicts.
func fetchOutcomeString(repos []PlanFetchRepo) string {
	attempted, ok := 0, 0
	for _, r := range repos {
		if !r.Attempted {
			continue
		}
		attempted++
		if r.OK {
			ok++
		}
	}
	switch {
	case attempted == 0:
		return "skipped"
	case ok == attempted:
		return "ok"
	case ok == 0:
		return "failed"
	default:
		return "partial"
	}
}

// fetchMutatedRemoteTrackingRefs is fetch.mutated_remote_tracking_refs
// (§11.3): true when a successful attempted row has a known contacting
// effect; null when a relevant effect is unknown or a contacting fetch
// failed (may have partially written); false otherwise, including every
// suppressed/un-attempted fetch.
func fetchMutatedRemoteTrackingRefs(repos []PlanFetchRepo) *bool {
	return aggregateFetchMutation(repos, func(e PlanFetchEffect) bool { return e.Contacted })
}

// fetchMutatedLocalBranches is fetch.mutated_local_branches (§11.3): the
// same three-valued rule over may_update_local_branches/may_delete_local_branches.
func fetchMutatedLocalBranches(repos []PlanFetchRepo) *bool {
	return aggregateFetchMutation(repos, func(e PlanFetchEffect) bool {
		return e.MayUpdateLocalBranches || e.MayDeleteLocalBranches
	})
}

// aggregateFetchMutation implements the shared tri-state rule both mutation
// fields follow: true if any attempted, OK row's effect is known and
// positive per predicate; else null if any attempted row is unknown-effect
// or failed-while-contacting (may have partially written); else false.
func aggregateFetchMutation(repos []PlanFetchRepo, positive func(PlanFetchEffect) bool) *bool {
	unknown := false
	for _, r := range repos {
		if !r.Attempted {
			continue
		}
		if r.Effect == nil {
			unknown = true
			continue
		}
		if r.OK && positive(*r.Effect) {
			v := true
			return &v
		}
		if !r.OK && r.Effect.Contacted {
			unknown = true
		}
	}
	if unknown {
		return nil
	}
	v := false
	return &v
}

// ============================================================================
// buildIntent, buildPolicy — the small direct-projection document members.
// ============================================================================

// buildIntent builds RebasePlan.Intent from req.Validation (§15). A caller
// that never populates Validation (its zero value has Applies false) gets
// exactly the prior "not-applicable" shape back — this function needs no
// mode branch. §15.5's table publishes checkout unconditionally
// stability:"frozen"/guarded_stability:"frozen-at-plan" whenever validation
// applies, fresh or continuation alike: the effective command is resolved
// once, above the plan, and carried as a value from there on, never
// re-read. command_digest is req.Validation's own precomputed digest —
// the raw command is never available to this builder to publish by
// mistake (PlanValidationIdentity.Command is deliberately not read here).
// cli_test_ignored is §15.7's own unguarded-legacy-external label (external
// always ignores --test in favour of a fresh per-entry config read); no
// external producer populates req.Validation in this tree, so it stays
// false here rather than inventing an unevidenced checkout rule.
func (b *planBuilder) buildIntent() PlanIntent {
	v := b.req.Validation
	stability, guardedStability := "not-applicable", "not-applicable"
	var digest *string
	if v.Applies {
		stability, guardedStability = "frozen", "frozen-at-plan"
		d := v.Digest
		digest = &d
	}
	source := v.Source
	if source == "" {
		source = "none"
	}
	return PlanIntent{
		Push:       b.req.Push,
		PushSource: b.req.PushSource,
		Validation: PlanIntentValidation{
			Applies:          v.Applies,
			Source:           source,
			Stability:        stability,
			GuardedStability: guardedStability,
			CommandDigest:    digest,
			CLITestIgnored:   false,
		},
	}
}

// buildPolicy builds RebasePlan.Policy, a direct projection of the effective
// SyncRunPolicy this invocation already resolved (§9.1a group 2): Fetch,
// Propagation and ScopeKind's Go string values are themselves the exact
// document domain tokens (SyncFetchPolicy/SyncPropagationPolicy/SyncScopeKind
// underlie string; internal/sync_selection.go:17-38), so no remapping table
// is needed.
func (b *planBuilder) buildPolicy() PlanPolicy {
	p := b.req.Policy
	var selector *string
	if p.ScopeKind != SyncScopeAll {
		s := p.Selector
		selector = &s
	}
	return PlanPolicy{
		Fetch:               string(p.Fetch),
		Propagation:         string(p.Propagation),
		ScopeKind:           string(p.ScopeKind),
		Selector:            selector,
		FetchDefaultApplied: b.req.PolicyFetchDefaultApplied,
	}
}

// ============================================================================
// plannability, blocked-token ordering shared by push/restore/guard/summary.
// ============================================================================

// plannabilityFor is summary.plannability (§2.16): "unavailable" when the
// run never reached a state where entries[] could even be attempted
// (RebasePlanRequest.RowsAvailable is "the sole answer to 'publish
// entries[]?'"), else "empty"/"rows" by the published row count.
func plannabilityFor(rowsAvailable bool, entryCount int) string {
	switch {
	case !rowsAvailable:
		return "unavailable"
	case entryCount == 0:
		return "empty"
	default:
		return "rows"
	}
}

// blockedRank orders a PushBlockedKind/RestoreBlockedKind token for the
// composed domains' "sorted" rule: a member that is also a RefusalKind sorts
// by its §7.1 precedence rank; the handful of extra, non-RefusalKind members
// each domain adds (push-dropped-restoring, restore-target-missing,
// restore-target-held, restore-head-moved) sort after every RefusalKind,
// then bytewise among themselves.
func blockedRank(token string) int {
	if r, ok := refusalRank[RefusalKind(token)]; ok {
		return r
	}
	return len(RefusalKinds)
}

// sortBlockedTokens applies blockedRank, then a bytewise tie-break.
func sortBlockedTokens[T ~string](tokens []T) {
	sort.SliceStable(tokens, func(i, j int) bool {
		if ri, rj := blockedRank(string(tokens[i])), blockedRank(string(tokens[j])); ri != rj {
			return ri < rj
		}
		return tokens[i] < tokens[j]
	})
}

// ============================================================================
// buildPush — RebasePlan.Push (§2.7-§2.9).
// ============================================================================

// pushScope decides push.scope / PlanPushRequest.Scope: "none" exactly when
// intent.push is false; else the route-shaped token every emitted row's own
// scope re-publishes (§2.8 row 10 — row-level scope has no "none").
func (b *planBuilder) pushScope() string {
	switch {
	case !b.req.Push:
		return "none"
	case b.req.Mode == ModeCheckout:
		return "transaction-plan"
	case b.req.Selection.Policy.Scoped():
		return "selected-rebased"
	default:
		return "stack-all"
	}
}

// alreadyPushed reads the one real per-entry "already pushed" tracking
// source on this request surface: SyncRunState.Pushed, the new-mode
// .sync-state.v2.yaml payload (§14.1a). Legacy SyncState
// (internal/syncstate.go) predates push tracking and has no Pushed member;
// checkout has no per-entry push tracking either — CheckoutTransaction.Push
// is a whole-run intent flag, and checkout pushes exactly once at
// transaction finalization, never per row. Both cases return an empty,
// non-nil map.
func (b *planBuilder) alreadyPushed() map[string]bool {
	out := map[string]bool{}
	if b.req.Mode != ModeExternal || b.req.Route != RouteNewMode {
		return out
	}
	payload := b.req.ExternalState.Files.Payload
	if payload.Presence != PlanPresenceReadable || payload.Payload == nil {
		return out
	}
	for _, n := range payload.Payload.Pushed {
		out[n] = true
	}
	return out
}

// remainingNames is the run's "still to do" name set every downstream
// decision shares (buildEntries' own filter, PlanPushRequest.Remaining): the
// full selection on a fresh run, req.Remaining verbatim — never re-derived —
// on a continuation (rule 3).
func (b *planBuilder) remainingNames() []string {
	if !b.req.Continue {
		out := make([]string, 0, len(b.req.Selection.Entries))
		for _, e := range b.req.Selection.Entries {
			out = append(out, e.Name)
		}
		return out
	}
	return b.req.Remaining
}

// pushMembership is §14.1a rule 10's own membership set for push.targets[],
// which is NOT the rebase remaining set: on a continuation it is
// `payload.Completed ∪ {FailedBranch} ∪ E_resume`, so the entries an earlier
// invocation already rebased and never pushed stay in the blast radius even
// on a ROWS-LESS push-only resume (rule 10d). On a fresh run it is the
// selection itself.
func (b *planBuilder) pushMembership() []string {
	remaining := b.remainingNames()
	if !b.req.Continue || b.req.Mode != ModeExternal {
		return remaining
	}
	payload := b.req.ExternalState.Files.Payload
	if payload.Presence != PlanPresenceReadable || payload.Payload == nil {
		return remaining
	}
	seen := make(map[string]bool, len(remaining))
	out := make([]string, 0, len(remaining))
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, n := range payload.Payload.Completed {
		add(n)
	}
	add(payload.Payload.FailedBranch)
	for _, n := range remaining {
		add(n)
	}
	return out
}

// checkoutRestoringWithPush is the push-dropped-restoring trigger: a
// continuation whose persisted transaction is currently at StageRestoring
// with Push:true — the original run intended to push, but restoring means
// the whole invocation is unwinding, so that push will never happen.
func (b *planBuilder) checkoutRestoringWithPush() bool {
	if b.req.Mode != ModeCheckout || !b.req.Continue {
		return false
	}
	f := b.req.CheckoutState.Files.CheckoutTransaction
	return f.Presence == PlanPresenceReadable && f.Transaction != nil &&
		f.Transaction.Stage == StageRestoring && f.Transaction.Push
}

// pushBlockedFromRefusals converts the document's final, deduplicated
// blockers[] (every RefusalKind stopping the controlled invocation, per
// §2.7 row 3) into push.blocked_by[]'s own token type.
func pushBlockedFromRefusals(blockers []PlanBlocker) []PushBlockedKind {
	seen := map[PushBlockedKind]bool{}
	out := make([]PushBlockedKind, 0, len(blockers))
	for _, bl := range blockers {
		tok := PushBlockedKind(bl.Kind)
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

// buildPush builds RebasePlan.Push. PushTargets is the one of the six §14.1a
// push producers BuildRebasePlan calls directly (rebase_planner.go's own doc
// comment on PushTargets); the other five already populated req.PushFacts
// before this invocation ran.
func (b *planBuilder) buildPush(finalBlockers []PlanBlocker, plannability string) PlanPush {
	scope := b.pushScope()
	targets := PushTargets(PlanPushRequest{
		Mode:          b.req.Mode,
		Route:         b.req.Route,
		Layout:        b.req.Layout,
		Intent:        b.req.Push,
		Scope:         scope,
		Stack:         b.req.Stack,
		Order:         b.req.Order,
		Selection:     b.req.Selection,
		Remaining:     b.pushMembership(),
		AlreadyPushed: b.alreadyPushed(),
		Facts:         b.req.PushFacts,
	})

	// §2.7 row 2: null in exactly 2 cells.
	if !b.req.Push || plannability == "unavailable" {
		return PlanPush{Targets: ensureSlice(targets), Executable: nil, BlockedBy: []PushBlockedKind{}, Scope: scope}
	}

	blocked := pushBlockedFromRefusals(finalBlockers)
	if b.checkoutRestoringWithPush() {
		if !containsPushBlocked(blocked, PushBlockedDroppedRestoring) {
			blocked = append(blocked, PushBlockedDroppedRestoring)
		}
	}
	sortBlockedTokens(blocked)
	executable := len(blocked) == 0
	if executable {
		blocked = []PushBlockedKind{}
	}
	return PlanPush{Targets: ensureSlice(targets), Executable: &executable, BlockedBy: blocked, Scope: scope}
}

func containsPushBlocked(tokens []PushBlockedKind, tok PushBlockedKind) bool {
	for _, t := range tokens {
		if t == tok {
			return true
		}
	}
	return false
}

// ============================================================================
// buildRestore — RebasePlan.Restore (§14.4, 10 members).
//
// Split into a probe phase (every fact decidable before the document's
// blockers[] are finalized) and a finalize phase (executable/blocked_by,
// decided from the post-SelectPrimaryRefusal list) — the same two-phase
// shape buildPush uses for push.blocked_by[]. The probe phase additionally
// surfaces its own rank-4.5 restore-blocked candidate via restoreBlocker,
// since — unlike push — that fact must be able to become refusal.kind
// itself, not merely a read-only reflection of the already-decided list.
// ============================================================================

// restoreProbeResult carries every restore.* fact decidable before
// blockers[] is finalized.
type restoreProbeResult struct {
	Applies                                       bool
	CompletedStage                                bool
	WillSwitchHead                                *bool
	TargetBranch, TargetSHA, TargetSource         *string
	DeletesTransaction, ReleasesLock, PushDropped bool
	ProbeFailed                                   bool
	ProbeFailedDetail                             string // set iff ProbeFailed; the specific unmeasurable fact
	TargetMissing, TargetHeld, HeadMoved          bool
	CollateralRisk                                bool // §7.4 fresh-run restore-head-collateral-risk warning signal
}

// restoreApplies is restore.applies: checkout-only, true for a fresh run iff
// the built plan is non-empty, true for every continuation whose persisted
// transaction actually decoded (a continuation with no readable transaction
// never reaches this question — it refuses earlier, at state-refused).
func (b *planBuilder) restoreApplies(execution []OrderedExecution, tx *CheckoutTransaction) bool {
	if b.req.Mode != ModeCheckout {
		return false
	}
	if !b.req.Continue {
		return len(execution) > 0
	}
	return tx != nil
}

// restoreTarget is restore.target_branch/target_source: the persisted
// OriginalBranch on any continuation (completed-stage included — it still
// names the branch the transaction recorded), else the fresh run's own
// probed HEAD branch. source is never empty when applies is true: a fresh
// run whose HEAD could not be resolved still reports "probed-head", just
// with an empty branch, which is exactly §14.4's probe-failure cell.
func (b *planBuilder) restoreTarget(tx *CheckoutTransaction) (branch, source string) {
	if tx != nil {
		return tx.OriginalBranch, "transaction"
	}
	head := b.req.CheckoutState.Head
	if head.Applies && !head.Detached && head.Branch != "" {
		return head.Branch, "probed-head"
	}
	return "", "probed-head"
}

// restoreBlockedFromRefusals converts the document's final blockers[] into
// RestoreBlockedKind tokens (§7.5's RefusalKind half of the domain).
func restoreBlockedFromRefusals(blockers []PlanBlocker) []RestoreBlockedKind {
	seen := map[RestoreBlockedKind]bool{}
	out := make([]RestoreBlockedKind, 0, len(blockers))
	for _, bl := range blockers {
		tok := RestoreBlockedKind(bl.Kind)
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

func dedupRestoreBlocked(tokens []RestoreBlockedKind) []RestoreBlockedKind {
	seen := map[RestoreBlockedKind]bool{}
	out := make([]RestoreBlockedKind, 0, len(tokens))
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// probeRestore measures every restore.* fact this builder can decide before
// the document's blockers[] are finalized (§14.4). execution is
// buildEntries' own (unsorted, invocation-scoped) execution order — needed
// here, and only here, to find which branch HEAD is really on immediately
// before restoreOriginal, i.e. the last row in THIS run whose own mutation
// actually switched HEAD.
func (b *planBuilder) probeRestore(execution []OrderedExecution, entries []PlanEntry) restoreProbeResult {
	tx := b.checkoutTransaction()
	if !b.restoreApplies(execution, tx) {
		f := false
		return restoreProbeResult{Applies: false, WillSwitchHead: &f}
	}

	completedStage := tx != nil && tx.Stage == StageCompleted
	targetBranch, targetSource := b.restoreTarget(tx)
	targetSourcePtr := &targetSource
	var targetBranchPtr, targetSHA *string
	if targetBranch != "" {
		tb := targetBranch
		targetBranchPtr = &tb
		if sha := GetBranchSHA(b.req.Layout.RepoRoot, "refs/heads/"+targetBranch); sha != "" {
			s := sha
			targetSHA = &s
		}
	}

	deletesTransaction, releasesLock := true, true
	pushDropped := tx != nil && tx.Stage == StageRestoring && tx.Push

	var willSwitch *bool
	probeFailed := false
	var probeFailedDetail string
	switch {
	case completedStage:
		f := false
		willSwitch = &f
	case targetBranch == "":
		// Fresh run whose own HEAD could not be resolved: §14.4's declared
		// probe-failure cell.
		probeFailed = true
		probeFailedDetail = "the restore target branch could not be resolved"
	default:
		switchByName := make(map[string]bool, len(entries))
		for _, e := range entries {
			switchByName[e.Name] = e.Mutation.WillSwitchHead
		}
		pre := targetBranch // default: nothing in this run switched away from it
		for i := len(execution) - 1; i >= 0; i-- {
			name := execution[i].Entry.Name
			if switchByName[name] {
				pre = execution[i].Entry.GitBranch()
				break
			}
		}
		v := pre != targetBranch
		willSwitch = &v
	}

	// The three probed cells (§14.4): evaluated on every applies:true
	// document whose route really calls restoreOriginal, i.e. every
	// non-completed-stage row here, fresh and continuation alike.
	targetMissing, targetHeld, headMoved := false, false, false
	if !completedStage {
		targetMissing = targetSHA == nil
		if targetBranch != "" {
			inv := b.holdersFor(b.req.Layout.RepoRoot)
			if !inv.Available {
				// Available == false means "not measured" (§14.4's holder
				// producer doc comment): fail closed into the probe-failure
				// cell rather than read an empty map as "not held".
				probeFailed = true
				probeFailedDetail = "the restore target branch's holder inventory could not be measured"
			} else if rec, ok := inv.ByBranch[targetBranch]; ok {
				targetHeld = canonicalize(rec.Worktree) != canonicalize(b.req.Layout.RepoRoot)
			}
		}
		headMoved = tx != nil && tx.OriginalHEAD != "" && targetSHA != nil && tx.OriginalHEAD != *targetSHA
	}

	// §7.4: on a fresh run the OriginalHEAD rule can't yet be violated (no
	// transaction persisted it), but the resembling hazard — the restore
	// target sitting inside this run's own collateral upper bound, i.e. a
	// branch this run's own execution will itself move — is real and
	// visible, so it is surfaced as a non-blocking warning rather than
	// silently dropped.
	collateralRisk := false
	if tx == nil && targetBranch != "" {
		for _, oe := range execution {
			if oe.Entry.GitBranch() == targetBranch {
				collateralRisk = true
				break
			}
		}
	}

	return restoreProbeResult{
		Applies: true, CompletedStage: completedStage, WillSwitchHead: willSwitch,
		TargetBranch: targetBranchPtr, TargetSHA: targetSHA, TargetSource: targetSourcePtr,
		DeletesTransaction: deletesTransaction, ReleasesLock: releasesLock, PushDropped: pushDropped,
		ProbeFailed: probeFailed, ProbeFailedDetail: probeFailedDetail,
		TargetMissing: targetMissing, TargetHeld: targetHeld, HeadMoved: headMoved,
		CollateralRisk: collateralRisk,
	}
}

// restoreBlocker projects a probe's own three target cells into the
// document's rank-4.5 restore-blocked candidate, BEFORE SelectPrimaryRefusal
// runs: the one restore fact that must be eligible to become refusal.kind
// itself, unlike restore.blocked_by[] as a whole, which is a read-only
// reflection of the already-decided blockers[] (§7.1 rank 4.5, §7.5). A
// probe failure never mints this blocker (probe-failed, rank 5.9, already
// owns that cell) and a completed-stage document never reaches the
// restore-target question at all (§14.4).
func restoreBlocker(p restoreProbeResult) *PlanBlocker {
	if !p.Applies || p.CompletedStage || p.ProbeFailed {
		return nil
	}
	if !p.TargetMissing && !p.TargetHeld && !p.HeadMoved {
		return nil
	}
	detail := "the recorded original HEAD is no longer the restore target's tip"
	switch {
	case p.TargetMissing:
		detail = "the checkout restore target branch could not be resolved"
	case p.TargetHeld:
		detail = "the checkout restore target branch is checked out in another worktree"
	}
	return &PlanBlocker{Kind: RefusalRestoreBlocked, Detail: detail}
}

// buildRestore assembles the final ten-member RebasePlan.Restore from a
// probe plus the document's already-selected final blockers[].
func buildRestore(p restoreProbeResult, finalBlockers []PlanBlocker, plannability string) PlanRestore {
	if !p.Applies {
		return PlanRestore{Applies: false, WillSwitchHead: p.WillSwitchHead, BlockedBy: []RestoreBlockedKind{}}
	}
	if plannability == "unavailable" || p.ProbeFailed {
		return PlanRestore{
			Applies: true, WillSwitchHead: p.WillSwitchHead,
			TargetBranch: p.TargetBranch, TargetSHA: p.TargetSHA, TargetSource: p.TargetSource,
			Executable: nil, BlockedBy: []RestoreBlockedKind{},
			DeletesTransaction: p.DeletesTransaction, ReleasesLock: p.ReleasesLock, PushDropped: p.PushDropped,
		}
	}

	var blocked []RestoreBlockedKind
	if !p.CompletedStage {
		// completed-stage: no restore-target question at all, decided by
		// the finalize preconditions alone (the refusal-derived tokens
		// appended below), never one of the three restore-target tokens.
		if p.TargetMissing {
			blocked = append(blocked, RestoreBlockedTargetMissing)
		}
		if p.TargetHeld {
			blocked = append(blocked, RestoreBlockedTargetHeld)
		}
		if p.HeadMoved {
			blocked = append(blocked, RestoreBlockedHeadMoved)
		}
	}
	blocked = append(blocked, restoreBlockedFromRefusals(finalBlockers)...)
	blocked = dedupRestoreBlocked(blocked)
	sortBlockedTokens(blocked)
	executable := len(blocked) == 0
	if executable {
		blocked = []RestoreBlockedKind{}
	}

	return PlanRestore{
		Applies: true, WillSwitchHead: p.WillSwitchHead,
		TargetBranch: p.TargetBranch, TargetSHA: p.TargetSHA, TargetSource: p.TargetSource,
		Executable: &executable, BlockedBy: blocked,
		DeletesTransaction: p.DeletesTransaction, ReleasesLock: p.ReleasesLock, PushDropped: p.PushDropped,
	}
}

// ============================================================================
// buildRepositories — RebasePlan.Repositories (§2.11).
// ============================================================================

// repositoryConfig projects one memoized PlanConfigResult into the
// repositories[].config shape.
func repositoryConfig(cfg PlanConfigResult) PlanRepositoryConfig {
	return PlanRepositoryConfig{
		UpdateRefs:   cfg.UpdateRefs,
		RebaseMerges: cfg.RebaseMerges,
		Backend:      cfg.Backend,
		AutoStash:    cfg.AutoStash,
	}
}

// configForContext returns the memoized config result for a context this
// invocation already probed (via an entry), without ever issuing a new
// probe: repositories[] rows contributed solely by a push target or a
// config-issue cross-reference must never re-probe with a guessed
// directory, so an unmemoized context here honestly reports not-evaluated.
func (b *planBuilder) configForContext(contextID string) PlanConfigResult {
	if contextID == "" {
		return notEvaluatedConfigResult()
	}
	if res, ok := b.configByContext[contextID]; ok {
		return res
	}
	return notEvaluatedConfigResult()
}

// buildRepositories builds RebasePlan.Repositories: one row per distinct
// (repo, context_id) established anywhere in the document — an entry's
// base_context/execution_context (always equal, §9.1a), a push target's
// context, a config_issues[] row's context, or the restore context when it
// was never independently established by any entry this invocation (a
// continuation whose Remaining set is already empty, §14.4).
func (b *planBuilder) buildRepositories(entries []PlanEntry, restore PlanRestore) []PlanRepository {
	type key struct{ repo, contextID string }
	byKey := make(map[key]PlanRepository)

	add := func(repo, contextID, repoRoot, rootSource string) {
		if contextID == "" {
			return
		}
		k := key{repo, contextID}
		if _, ok := byKey[k]; ok {
			return
		}
		byKey[k] = PlanRepository{
			Repo: repo, ContextID: contextID, RepoRoot: repoRoot, RootSource: rootSource,
			Config: repositoryConfig(b.configForContext(contextID)),
		}
	}

	for _, e := range entries {
		if e.ExecutionContext.ContextID != nil {
			add(e.Repo, *e.ExecutionContext.ContextID, derefString(e.ExecutionContext.RepoRoot), e.ExecutionContext.Source)
		}
		if e.BaseContext.ContextID != nil {
			add(e.Repo, *e.BaseContext.ContextID, derefString(e.BaseContext.RepoRoot), e.BaseContext.Source)
		}
	}

	repoByContextID := make(map[string]string, len(b.req.PushFacts.Remotes))
	for repoToken, facts := range b.req.PushFacts.Remotes {
		if facts.ContextID != nil {
			repoByContextID[*facts.ContextID] = repoToken
		}
	}
	for _, pc := range b.req.PushFacts.Contexts {
		if pc.ContextID == nil {
			continue
		}
		add(repoByContextID[*pc.ContextID], *pc.ContextID, pc.RepoRoot, pc.Source)
	}

	for _, issue := range b.configIssues {
		add("", issue.ContextID, derefString(issue.RepoRoot), "")
	}

	if b.req.Mode == ModeCheckout && restore.Applies {
		if id, err := EstablishContextIdentity(b.req.Layout.RepoRoot, "workspace-repo-root"); err == nil {
			add("", id.ContextID, id.RepoRoot, "workspace-repo-root")
		}
	}

	out := make([]PlanRepository, 0, len(byKey))
	for _, r := range byKey {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].ContextID < out[j].ContextID
	})
	return out
}

// ============================================================================
// buildGuard — RebasePlan.Guard (§2.17).
// ============================================================================

// gateVerdicts constructs the only two ControlledPathBlocker preconditions
// this tree can measure without req.Fetch/FetchPlan (§9.1a's group-5 doc
// comment defers both to internal/rebase_plan_guard.go, which does not
// exist yet): live-owner-concurrency, from this mode's own live-lock/
// live-guard read, and owner-artefact-undecodable, from that identical
// artefact's own presence when it could not be decoded at all. The three
// fetch-scoped controlled tokens can never fire on this request shape and
// are honestly never produced.
//
// OnGuardedRoute follows §7.1's rank-3 rule precisely, per mode: checkout's
// ladder only ever "meets" a foreign lock while acquiring it fresh
// (AcquireCheckoutLock/forceAcquireCheckoutLock are never re-entered on a
// continuation, which already owns the lock), so its live/undecodable
// verdicts are on-route exactly when !Continue; external's ladder is
// consulted only on the new-mode route. A symlinked artefact is always
// Fired (a real, published controlled-path fact) but never OnGuardedRoute:
// the native ladder reads straight through a symlink and never refuses on
// it (§12.5), only this plan's own Lstat-based probe distinguishes it.
func (b *planBuilder) gateVerdicts() []PlanGateVerdict {
	switch b.req.Mode {
	case ModeCheckout:
		lockFile := b.req.CheckoutState.Files.CheckoutLock
		onRoute := !b.req.Continue
		return []PlanGateVerdict{
			{
				Fired: b.req.CheckoutState.LiveForeignLock(), Kind: RefusalStateRefused,
				Controlled: ControlledLiveOwnerConcurrency, Detail: "a live process holds the checkout lock",
				OnGuardedRoute: onRoute,
			},
			{
				Fired: lockFile.Presence == PlanPresenceUnreadable, Kind: RefusalStateRefused,
				Controlled: ControlledOwnerArtefactUndecodable, Detail: "the checkout lock could not be decoded",
				OnGuardedRoute: onRoute,
			},
			{
				// §22.13m (iv)/(v): the symlink cell is the guard seam's own
				// refusal on ANY guarded route — the seam is the refusing
				// party — while the unguarded native ladder reads straight
				// through the link and is untouched (EvaluatePlanGuard is a
				// no-op on an unguarded invocation).
				Fired: lockFile.Presence == PlanPresenceSymlink, Kind: RefusalStateRefused,
				Controlled: ControlledOwnerArtefactUndecodable, Detail: "the checkout lock is a symlink",
				OnGuardedRoute: false,
			},
		}
	case ModeExternal:
		guardFile := b.req.ExternalState.Files.RunGuard
		onRoute := b.req.Route == RouteNewMode
		return []PlanGateVerdict{
			{
				Fired: b.req.ExternalState.LiveForeignOwner(), Kind: RefusalStateRefused,
				Controlled: ControlledLiveOwnerConcurrency, Detail: "a live process holds the run guard",
				OnGuardedRoute: onRoute,
			},
			{
				Fired: guardFile.Presence == PlanPresenceUnreadable, Kind: RefusalStateRefused,
				Controlled: ControlledOwnerArtefactUndecodable, Detail: "the run guard could not be decoded",
				OnGuardedRoute: onRoute,
			},
			{
				// See the checkout twin above: on a guarded route the seam is
				// the refusing party; the unguarded ladder is unchanged.
				Fired: guardFile.Presence == PlanPresenceSymlink, Kind: RefusalStateRefused,
				Controlled: ControlledOwnerArtefactUndecodable, Detail: "the run guard is a symlink",
				OnGuardedRoute: false,
			},
		}
	}
	return nil
}

// buildGuard builds RebasePlan.Guard (§2.17): limits is req.Limits verbatim
// (the guard's own already-reconciled effective limit pair, resolved above
// this builder); evaluation is the caller's own already-computed row set —
// pre-waiver for would_refuse_without_approval, post-waiver for
// would_refuse (§8.5) — this function does not itself decide waiving, only
// assembles what it is given. execute_blocked_by merges every controlled-
// token producer this tree has: fired PlanGateVerdict rows
// (GateControlledTokens), req.Gates' own rows (checkoutGateControlledTokens)
// and req.FetchPlan's fetch-scoped rows — sorted/de-duplicated in the one
// canonical order (§4.8).
func (b *planBuilder) buildGuard(verdicts []PlanGateVerdict, evaluation []PlanGuardEvaluation, wouldRefuseWithoutApproval, wouldRefuse bool) PlanGuardBlock {
	return PlanGuardBlock{
		Limits:                     PlanGuardLimitSet{MaxReplayPerEntry: b.req.Limits.PerEntry, MaxReplayTotal: b.req.Limits.Total},
		LimitConflicts:             ensureSlice(b.req.LimitConflicts),
		Evaluation:                 ensureSlice(evaluation),
		IndeterminacyPolicy:        "jit-deferred",
		WouldRefuseWithoutApproval: wouldRefuseWithoutApproval,
		WouldRefuse:                wouldRefuse,
		ExecuteBlockedBy: mergeControlledTokens(
			GateControlledTokens(verdicts),
			checkoutGateControlledTokens(b.req.Gates),
			b.req.FetchPlan.Controlled,
		),
	}
}

// mergeControlledTokens merges any number of ControlledPathBlocker sets into
// guard.execute_blocked_by[]'s own canonical sorted, de-duplicated order
// (§4.8) — the same technique GateControlledTokens itself uses internally,
// generalised over every controlled-token producer this tree has.
func mergeControlledTokens(sets ...[]ControlledPathBlocker) []ControlledPathBlocker {
	present := make(map[ControlledPathBlocker]bool)
	for _, set := range sets {
		for _, tok := range set {
			present[tok] = true
		}
	}
	out := make([]ControlledPathBlocker, 0, len(present))
	for _, tok := range ControlledPathBlockers {
		if present[tok] {
			out = append(out, tok)
		}
	}
	return out
}

// gatesSuppressFetch is §11.2 cause 2's gate half: ANY failed pre-fetch gate
// the described executor raises above its own fetch, the two §16 capability
// gates included — `config --show-scope` at 2.26 and `--update-refs` at 2.38
// (§11.2 cause 2's own row names them explicitly). A capability the host
// lacks is exactly a reason the described run "cannot reach its fetch at
// all", so the document publishes `suppression_cause:
// not-refreshed-no-plan-subject` with `attempted: false` and `outcome:
// skipped` rather than the local-only shape of a route that simply has no
// fetch policy.
//
// It suppresses the fetch only: the document's own ROWS are decided by
// RowsAvailable alone, so a refusing capability document still publishes its
// rows with `effective_backend: null` and unknown collateral, and an
// operator can still see exactly which argv the host would reject.
func gatesSuppressFetch(gates []PlanGateResult) bool {
	for _, g := range gates {
		if g.Applies && g.Failed {
			return true
		}
	}
	return false
}

// checkoutGateBlockers is req.Gates' own candidate-blocker contribution —
// one blocker per failed pre-fetch/pre-mutation gate
// (checkoutPreconditionGates' transaction-exists/op-in-progress/dirty-tree
// rows). It is a separate mechanism from GateBlockers(verdicts): req.Gates
// and the pre-existing PlanGateVerdict rows measure disjoint facts (§4
// ownership table).
func checkoutGateBlockers(gates []PlanGateResult) []PlanBlocker {
	var out []PlanBlocker
	for _, g := range gates {
		if !g.Applies || !g.Failed {
			continue
		}
		var entry *string
		if g.Entry != "" {
			e := g.Entry
			entry = &e
		}
		out = append(out, PlanBlocker{Kind: g.Kind, Entry: entry, Detail: g.Detail})
	}
	return out
}

// checkoutGateControlledTokens folds every applicable req.Gates row's own
// Controlled tokens into guard.execute_blocked_by[], mirroring
// GateControlledTokens' treatment of PlanGateVerdict: a gate contributes
// its tokens whenever it Applies and Failed, even where its own Kind stays
// the zero value (§4.6's independence rule). None of
// checkoutPreconditionGates' three rows currently sets Controlled, so this
// is presently a no-op in practice but keeps the fold correct if that ever
// changes.
func checkoutGateControlledTokens(gates []PlanGateResult) []ControlledPathBlocker {
	var out []ControlledPathBlocker
	for _, g := range gates {
		if !g.Applies || !g.Failed {
			continue
		}
		out = append(out, g.Controlled...)
	}
	return out
}

// ============================================================================
// Guard evaluation engine (§2.17.2/§2.17.3) — per-entry and aggregate rows.
// ============================================================================

// guardEvaluationRows builds guard.evaluation[] (§2.17.2/§2.17.3): one
// per-entry row for every entry once max_replay_per_entry is effective,
// followed by the single aggregate row once max_replay_total is effective.
// Neither axis contributes a row when its own limit is unarmed (nil Value)
// — an evaluation row only ever exists for an "effective" limit. No row is
// produced once plannability is "unavailable": there is then no entries[]
// or summary left to evaluate.
func guardEvaluationRows(limits PlanGuardLimits, entries []PlanEntry, summary PlanSummary, plannability string) []PlanGuardEvaluation {
	if plannability == "unavailable" {
		return []PlanGuardEvaluation{}
	}
	var out []PlanGuardEvaluation
	if limits.PerEntry.Value != nil {
		for _, e := range entries {
			out = append(out, perEntryEvaluationRow(*limits.PerEntry.Value, e))
		}
	}
	if limits.Total.Value != nil {
		out = append(out, totalEvaluationRow(*limits.Total.Value, entries, summary))
	}
	return ensureSlice(out)
}

// perEntryEvaluationRow is one guard.evaluation[] row keyed
// "max_replay_per_entry:<entry>" (§2.17.2): an unknown replay.determinacy
// publishes value:null with the two-way unknown_kind rule (deferred iff its
// own reason is exactly "upstream-deferred"); every other entry — including
// a real "skip" strategy row, whose own candidate_count is always exactly
// 0 — publishes its own candidate count as value and exceeded/within-limit
// by simple comparison, which is also precisely the schema's own documented
// "skip row: value 0, within-limit" special case, with no separate branch.
func perEntryEvaluationRow(limit int, e PlanEntry) PlanGuardEvaluation {
	name := e.Name
	row := PlanGuardEvaluation{
		ID:    "max_replay_per_entry:" + name,
		Limit: limit,
		Basis: "per-entry",
		Entry: &name,
	}
	if e.Replay.Determinacy == "unknown" {
		kind := "not-resolvable"
		if e.Replay.Reason != nil && *e.Replay.Reason == "upstream-deferred" {
			kind = "deferred-resolvable"
		}
		row.Verdict = "unknown"
		row.UnknownKind = &kind
		return row
	}
	value := 0
	if e.Replay.CandidateCount != nil {
		value = *e.Replay.CandidateCount
	}
	row.Value = &value
	if value > limit {
		row.Verdict = "exceeded"
	} else {
		row.Verdict = "within-limit"
	}
	return row
}

// totalEvaluationRow is the single "max_replay_total" aggregate row
// (§2.17.3): with every entry resolved (U == 0) it is a plain basis:"total"
// comparison of the exact total against the limit; once any entry is
// unknown (U > 0), the true total is only known to be at least
// summary.total_candidates_lower_bound, so the row is basis:"lower-bound"
// and already exceeds on that lower bound alone whenever it clears the
// limit by itself, or else reports "unknown" — resolvable
// (deferred-resolvable) only once every contributing unknown entry is
// itself upstream-deferred, otherwise not-resolvable.
func totalEvaluationRow(limit int, entries []PlanEntry, summary PlanSummary) PlanGuardEvaluation {
	lowerBound := 0
	if summary.TotalCandidatesLowerBound != nil {
		lowerBound = *summary.TotalCandidatesLowerBound
	}
	value := lowerBound
	row := PlanGuardEvaluation{ID: "max_replay_total", Limit: limit, Basis: "total", Value: &value}

	unknown := summary.EntriesWithUnknownCandidates
	if unknown == 0 {
		if value > limit {
			row.Verdict = "exceeded"
		} else {
			row.Verdict = "within-limit"
		}
		return row
	}

	row.Basis = "lower-bound"
	u := unknown
	row.UnknownEntries = &u
	if value > limit {
		row.Verdict = "exceeded"
		return row
	}
	row.Verdict = "unknown"
	kind := "deferred-resolvable"
	for _, e := range entries {
		if e.Replay.Determinacy != "unknown" {
			continue
		}
		if e.Replay.Reason == nil || *e.Replay.Reason != "upstream-deferred" {
			kind = "not-resolvable"
			break
		}
	}
	row.UnknownKind = &kind
	return row
}

// guardWouldRefuse computes would_refuse_without_approval / would_refuse
// (§2.17.4) from one evaluation[] snapshot: true when any row's own verdict
// is "exceeded", or "unknown" with unknown_kind "not-resolvable" — a
// deferred-resolvable unknown row alone never forces a refusal (the JIT seam
// may still resolve it favourably at its own entry). The same function
// evaluates both flags: the caller passes pre-waiver rows for
// would_refuse_without_approval and post-waiver rows for would_refuse.
func guardWouldRefuse(rows []PlanGuardEvaluation) bool {
	for _, r := range rows {
		if r.Verdict == "exceeded" {
			return true
		}
		if r.Verdict == "unknown" && r.UnknownKind != nil && *r.UnknownKind == "not-resolvable" {
			return true
		}
	}
	return false
}

// indeterminateEntryBlockers is rank 10 indeterminate-entry: one row per
// entry whose own replay could not be resolved to a concrete candidate
// count (Determinacy "unknown", and not itself waiting on a later stack
// destination via an "upstream-deferred" reason) while at least one guard
// limit is effective — an unarmed guard never needs a concrete count, so no
// entry can be "indeterminate" against nothing armed.
func indeterminateEntryBlockers(entries []PlanEntry, limits PlanGuardLimits) []PlanBlocker {
	if limits.PerEntry.Value == nil && limits.Total.Value == nil {
		return nil
	}
	var out []PlanBlocker
	for _, e := range entries {
		if e.Replay.Determinacy != "unknown" {
			continue
		}
		if e.Replay.Reason != nil && *e.Replay.Reason == "upstream-deferred" {
			continue
		}
		name := e.Name
		out = append(out, PlanBlocker{
			Kind:   RefusalIndeterminateEntry,
			Entry:  &name,
			Detail: fmt.Sprintf("%s: replay candidate count cannot be resolved before execution while a replay limit is in force", name),
		})
	}
	return out
}

// limitExceededBlockers is ranks 11/12 limit-per-entry/limit-total: one
// blocker per evaluation[] row whose own verdict is "exceeded" — never for
// an "unknown" row (rank 10 owns that case) — over whatever row set the
// caller passes; passing the post-waiver rows naturally excludes any row a
// matching approval has already waived (§8.5), since a waived row's own
// verdict is no longer "exceeded".
func limitExceededBlockers(rows []PlanGuardEvaluation) []PlanBlocker {
	var out []PlanBlocker
	for _, r := range rows {
		if r.Verdict != "exceeded" {
			continue
		}
		if r.Basis == "per-entry" {
			out = append(out, PlanBlocker{
				Kind:   RefusalLimitPerEntry,
				Entry:  r.Entry,
				Detail: fmt.Sprintf("%s: %s candidate(s) exceeds the effective per-entry replay limit of %d", derefString(r.Entry), intOrNone(r.Value), r.Limit),
			})
			continue
		}
		out = append(out, PlanBlocker{
			Kind:   RefusalLimitTotal,
			Detail: fmt.Sprintf("%s candidate(s) exceeds the effective total replay limit of %d", intOrNone(r.Value), r.Limit),
		})
	}
	return out
}

// approvalMismatchBlocker is rank 8 approval-mismatch (§8, §15.2): fires
// only once a usable fingerprint exists (usable false, or a nil fingerprint,
// means there is nothing to mismatch, never a blocker) and a non-empty
// token was actually supplied that does not equal it — an empty
// --approve-plan is "no approval offered", never a mismatch.
func approvalMismatchBlocker(usable bool, fingerprint *string, supplied string) *PlanBlocker {
	if !usable || fingerprint == nil || supplied == "" || supplied == *fingerprint {
		return nil
	}
	return &PlanBlocker{Kind: RefusalApprovalMismatch, Detail: "the supplied approval token does not match this plan's own fingerprint"}
}

// ============================================================================
// computeRunnable — RebasePlan.Runnable (§7.3's total rule).
// ============================================================================

// computeRunnable implements §7.3 exactly: runnable is true iff the document
// carries no effective, unwaived blocker whose rank is 1, 2, 3, 4, 4.5, or
// any sub-rank of 5, and no rank-6 selection-hazard. blockers must already
// be SelectPrimaryRefusal's own (sorted, exact-tuple-deduplicated) second
// return value — a waived guard.evaluation[] row never becomes a blockers[]
// entry in the first place (§7.2 rule 2), so no separate waiver check is
// needed here. RefusalKinds' own rank order (reused via the package-level
// refusalRank map SelectPrimaryRefusal itself builds from) already places
// every rank-1..5.9 kind at or before RefusalSelectionHazard's own index and
// every rank-7..12 kind after it, so a single index cutoff implements the
// whole rule without re-declaring rank numbers.
func computeRunnable(blockers []PlanBlocker, plannability string) bool {
	// There is NO plannability short-circuit here, deliberately: `runnable`
	// is a function of the published blockers alone, so a document can never
	// claim `plannability: unavailable` with an empty blockers[] and a null
	// refusal — a shape that would tell the operator a run is impossible
	// while naming no cause at all. Every rows-less profile is instead
	// required to publish its own effective rank 1-5 cause (the stack/sort/
	// selection errors of stackBlockers, or the state gate/continuation gate
	// of a continuation with no persisted subject), and it is that blocker,
	// not the plannability token, that makes the document non-runnable.
	_ = plannability
	cutoff := refusalRank[RefusalSelectionHazard]
	for _, bl := range blockers {
		if r, ok := refusalRank[bl.Kind]; ok && r <= cutoff {
			return false
		}
	}
	return true
}

// ============================================================================
// buildSummary — RebasePlan.Summary (§2.16, 13 members).
// ============================================================================

// buildSummary aggregates entries[] (already in their final, sorted-by-name
// form) into the 13-member summary object, publishing the exact rows-less
// profile spec.md §4.3 tabulates for the two no-entries
// plannability values.
func buildSummary(entries []PlanEntry, plannability string) PlanSummary {
	if plannability == "unavailable" {
		return PlanSummary{Plannability: plannability}
	}
	if plannability == "empty" {
		zero := 0
		upper := "upper"
		return PlanSummary{
			Plannability: plannability, Entries: 0,
			TotalCandidates: &zero, TotalCandidatesLowerBound: &zero,
			CollateralRefs: []PlanCollateralRef{}, CollateralBound: &upper, CollateralRefsUnowned: &zero,
		}
	}

	s := PlanSummary{Plannability: plannability, Entries: len(entries)}
	var maxCandidates *int
	lowerBound := 0
	refMap := make(map[[2]string]PlanCollateralRef)
	for _, e := range entries {
		if e.Destination.Deferred {
			s.DeferredDestinations++
		}
		if e.Strategy == "conditional" {
			s.ConditionalStrategies++
		}
		if e.Replay.Determinacy == "unknown" {
			s.EntriesWithUnknownCandidates++
		}
		if cc := e.Replay.CandidateCount; cc != nil {
			if maxCandidates == nil || *cc > *maxCandidates {
				v := *cc
				maxCandidates = &v
			}
			lowerBound += *cc
		}
		if e.CollateralExposed != nil {
			if *e.CollateralExposed {
				s.CollateralExposedEntries++
			}
		} else {
			s.EntriesWithUnknownCollateral++
		}
		for _, ref := range e.CollateralRefs {
			refMap[[2]string{ref.Repo, ref.Ref}] = ref
		}
	}
	s.MaxEntryCandidates = maxCandidates
	lb := lowerBound
	s.TotalCandidatesLowerBound = &lb
	if s.EntriesWithUnknownCandidates == 0 {
		tc := lowerBound
		s.TotalCandidates = &tc
	}

	refs := make([]PlanCollateralRef, 0, len(refMap))
	for _, ref := range refMap {
		refs = append(refs, ref)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Repo != refs[j].Repo {
			return refs[i].Repo < refs[j].Repo
		}
		return refs[i].Ref < refs[j].Ref
	})
	s.CollateralRefs = refs
	upper := "upper"
	s.CollateralBound = &upper
	unowned := 0
	for _, ref := range refs {
		if !ref.StackOwned {
			unowned++
		}
	}
	s.CollateralRefsUnowned = &unowned
	return s
}

// ============================================================================
// buildApproval — RebasePlan.Approval (§8.4, §2.19).
// ============================================================================

// approvalNote returns covers.note's fixed sanitized sentence, choosing the
// first of §8.4's three clauses (in the order the spec itself states them)
// that fails, or a positive sentence when all three hold.
func approvalNote(hasWork, hasLimits, encodingSafe bool) string {
	switch {
	case !hasWork:
		return "no rebase, push, or restore work is described"
	case !hasLimits:
		return "no effective replay limit is in force"
	case !encodingSafe:
		return "a bound identity is not valid UTF-8"
	default:
		return "eligible for a usable approval fingerprint"
	}
}

// buildApproval builds RebasePlan.Approval. fingerprint and accepted are
// BuildRebasePlan's own — only it can mint the fingerprint (PlanFingerprint
// takes the fully-assembled skeleton, §8.2) and only it knows whether a
// supplied token matched it; this function recomputes hasWork/hasLimits/
// usable independently (the same trivial formula, over the same
// entries/push/restore/req.Limits the caller used to decide whether to
// mint), so usable here always agrees with "fingerprint != nil" by
// construction. waivedIDs/waivedKinds are the caller's own §8.5 waiver
// result — [] on every unaccepted or limitless run.
func (b *planBuilder) buildApproval(entries []PlanEntry, push PlanPush, restore PlanRestore, fingerprint *string, accepted bool, waivedIDs []string, waivedKinds []RefusalKind, encodingSafe bool) PlanApproval {
	scope := "full-run"
	if b.req.Continue {
		scope = "remaining-work"
	}

	hasWork := len(entries) > 0 || len(push.Targets) > 0 || restore.Applies
	hasLimits := b.req.Limits.PerEntry.Value != nil || b.req.Limits.Total.Value != nil
	usable := hasWork && hasLimits && encodingSafe

	var acceptedPtr *bool
	supplied := b.req.Approve != ""
	if supplied {
		a := accepted
		acceptedPtr = &a
	}

	return PlanApproval{
		Fingerprint: fingerprint,
		Usable:      usable,
		Scope:       scope,
		Supplied:    supplied,
		Accepted:    acceptedPtr,
		Covers: PlanApprovalCovers{
			Scope:               scope,
			WaivedEvaluationIDs: ensureSlice(waivedIDs),
			WaivedKinds:         ensureSlice(waivedKinds),
			HardBlockersWaived:  false,
			HasWork:             hasWork,
			RequiresLimits:      !hasLimits,
			EncodingSafe:        encodingSafe,
			Note:                approvalNote(hasWork, hasLimits, encodingSafe),
		},
	}
}

// applyApprovalWaiver returns evaluation's own post-waiver copy plus the IDs
// and RefusalKinds it waived (§8.5): once accepted, every row whose own
// pre-waiver verdict is "exceeded" is copied with verdict rewritten to
// "waived" — the row itself, and its own limit/value/basis/entry, are never
// removed or otherwise altered. Rank 10 rows (verdict "unknown") and every
// rank 1-9 blocker are never touched here: only ranks 11/12 are ever
// waivable, and only by a matching, accepted approval.
func applyApprovalWaiver(evaluation []PlanGuardEvaluation, accepted bool) (out []PlanGuardEvaluation, waivedIDs []string, waivedKinds []RefusalKind) {
	out = make([]PlanGuardEvaluation, len(evaluation))
	copy(out, evaluation)
	if !accepted {
		return out, []string{}, []RefusalKind{}
	}
	kindSeen := make(map[RefusalKind]bool, 2)
	for i, row := range out {
		if row.Verdict != "exceeded" {
			continue
		}
		out[i].Verdict = "waived"
		waivedIDs = append(waivedIDs, row.ID)
		kind := RefusalLimitTotal
		if row.Basis == "per-entry" {
			kind = RefusalLimitPerEntry
		}
		kindSeen[kind] = true
	}
	for _, k := range []RefusalKind{RefusalLimitPerEntry, RefusalLimitTotal} {
		if kindSeen[k] {
			waivedKinds = append(waivedKinds, k)
		}
	}
	return ensureSlice(out), ensureSlice(waivedIDs), ensureSlice(waivedKinds)
}

// ============================================================================
// Document-level blockers[] assembly (§7.1 ranks 1-2, 3 (continuation-gate
// half), 4, 5.07, 5.9, 6, 7, 7.5) — every rank buildEntries' own
// entryBlockers cannot see because the underlying fact is document-scoped,
// not row-scoped. GateBlockers supplies the other rank-3 half.
// ============================================================================

// stackBlockers is ranks 1-2: plan-unavailable, stack-unsortable,
// selection-refused. All three read a verbatim, already-sanitized error the
// caller measured exactly once (§9.1a rule 3).
func stackBlockers(req RebasePlanRequest) []PlanBlocker {
	var out []PlanBlocker
	if req.StackErr != nil {
		out = append(out, PlanBlocker{Kind: RefusalPlanUnavailable, Detail: req.StackErr.Error()})
	}
	if req.SortErr != nil {
		out = append(out, PlanBlocker{Kind: RefusalStackUnsortable, Detail: req.SortErr.Error()})
	}
	if req.SelectionErr != nil {
		out = append(out, PlanBlocker{Kind: RefusalSelectionRefused, Detail: req.SelectionErr.Error()})
	}
	return out
}

// continuationGateBlocker is rank 3's continuation-gate half of state-refused
// (the live-owner/undecodable-artefact half comes from GateBlockers).
func continuationGateBlocker(req RebasePlanRequest) *PlanBlocker {
	if !req.ContinuationGate.Applies || !req.ContinuationGate.Failed {
		return nil
	}
	return &PlanBlocker{Kind: RefusalStateRefused, Detail: req.ContinuationGate.Detail}
}

// preflightBlocker is rank 4's shipped I14 base-preflight half (document-
// level, marker-free; the offending entry name is already part of its own
// verbatim sentence, so no Entry pointer is set here).
func preflightBlocker(req RebasePlanRequest) *PlanBlocker {
	if !req.BasePreflight.Applies || !req.BasePreflight.Failed {
		return nil
	}
	return &PlanBlocker{Kind: RefusalPreflightRefused, Detail: req.BasePreflight.Detail}
}

// configIssueBlockers is rank 5.07 invalid-git-config: one document-level
// blocker per distinct config_issues[] row this invocation actually
// accumulated (§11.7's closed fatal-key domain already restricts issues to
// route-applicable, Git-fatal keys, so every issue here is in scope by
// construction).
func configIssueBlockers(issues []PlanConfigIssue) []PlanBlocker {
	seen := make(map[string]bool, len(issues))
	out := make([]PlanBlocker, 0, len(issues))
	for _, issue := range issues {
		if seen[issue.IssueID] {
			continue
		}
		seen[issue.IssueID] = true
		out = append(out, PlanBlocker{
			Kind:   RefusalInvalidGitConfig,
			Detail: fmt.Sprintf("invalid git config %s in %s", issue.Key, derefString(issue.RepoRoot)),
		})
	}
	return out
}

// probeFailedBlockers is rank 5.9's two document-level causes this tree can
// measure: an unparseable/unobtainable `git --version` on the plan path, and
// a push context whose remote-mapping half could not be read at all
// (MappingReadOK: false, §14.1a rule 5a) — one row per affected context,
// ordered/de-duplicated by context_id.
func (b *planBuilder) probeFailedBlockers() []PlanBlocker {
	var out []PlanBlocker
	if !b.req.Version.OK {
		out = append(out, PlanBlocker{Kind: RefusalProbeFailed, Detail: "git --version could not be parsed or obtained"})
	}
	type row struct {
		contextID string
		detail    string
	}
	var rows []row
	for _, facts := range b.req.PushFacts.Remotes {
		if facts.MappingReadOK || facts.ContextID == nil {
			continue
		}
		rows = append(rows, row{contextID: *facts.ContextID, detail: "the remote mapping for this push context could not be read"})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].contextID < rows[j].contextID })
	for _, r := range rows {
		out = append(out, PlanBlocker{Kind: RefusalProbeFailed, Detail: r.detail})
	}
	return out
}

// selectionHazardHazard is rank 6: a duplicate GitBranch across the
// published entries[] (two rows the executor would each treat as "the"
// mutation target for the same branch).
func selectionHazardBlocker(entries []PlanEntry) *PlanBlocker {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.GitBranch] {
			return &PlanBlocker{Kind: RefusalSelectionHazard, Detail: fmt.Sprintf("duplicate git branch %s across selected entries", e.GitBranch)}
		}
		seen[e.GitBranch] = true
	}
	return nil
}

// limitConflictBlockers is rank 7: one document-level, non-hard blocker per
// guard.limit_conflicts[] row this continuation's own supplied limit
// disagreed with the persisted one.
func limitConflictBlockers(conflicts []PlanGuardLimitConflict) []PlanBlocker {
	out := make([]PlanBlocker, 0, len(conflicts))
	for _, c := range conflicts {
		out = append(out, PlanBlocker{
			Kind: RefusalGuardLimitMismatch,
			Detail: fmt.Sprintf("%s: continuation supplied %s but the persisted (%s) value %s stays effective",
				c.Key, intOrNone(c.SuppliedValue), c.EffectiveOrigin, intOrNone(c.EffectiveValue)),
		})
	}
	return out
}

// intOrNone renders a nullable guard-limit value for a blocker detail
// sentence: the decimal value, or "none" when the pointer is nil.
func intOrNone(v *int) string {
	if v == nil {
		return "none"
	}
	return strconv.Itoa(*v)
}

// approvalWithoutLimitsBlocker is rank 7.5: a continuation supplied
// --approve-plan while both effective limits are absent — an approval token
// is meaningless with no armed limit for it to waive.
func approvalWithoutLimitsBlocker(req RebasePlanRequest, hasLimits bool) *PlanBlocker {
	if !req.Continue || req.Approve == "" || hasLimits {
		return nil
	}
	return &PlanBlocker{Kind: RefusalApprovalWithoutLimits, Detail: "the continuation supplied --approve-plan with no effective replay limit in force"}
}

// identityEncodingBlocker is rank 5.08 identity-not-utf8 (§7.1, §7.4's
// conditional row): bound identity bytes are invalid UTF-8 on a document
// that WOULD MINT (it has work and at least one effective limit) or WAS
// GIVEN a token. A limitless plan with invalid bound identity publishes
// encoding_issues[], mints no token, emits no blocker and stays runnable —
// the same rule, not an exception.
func identityEncodingBlocker(issues []PlanEncodingIssue, hasWork, hasLimits bool, approve string) *PlanBlocker {
	if len(issues) == 0 {
		return nil
	}
	wouldMint := hasWork && hasLimits
	if !wouldMint && approve == "" {
		return nil
	}
	return &PlanBlocker{
		Kind:   RefusalIdentityNotUTF8,
		Detail: fmt.Sprintf("%d bound identity value(s) are not valid UTF-8; no approval token can be minted for this plan", len(issues)),
	}
}

// restoreProbeFailedBlocker is the restore probe's own contribution to rank
// 5.9 probe-failed (§14.4): a fresh run whose own HEAD could not be
// resolved, or whose target-branch holder inventory could not be measured.
// Document-level: restore is a single document-scoped object, not a
// per-entry row.
func restoreProbeFailedBlocker(p restoreProbeResult) *PlanBlocker {
	if !p.Applies || p.CompletedStage || !p.ProbeFailed {
		return nil
	}
	return &PlanBlocker{Kind: RefusalProbeFailed, Detail: p.ProbeFailedDetail}
}

// ============================================================================
// warnings[] assembly (§7.4, §7.5, §2.14, §11.7-§11.8, §18.3-§18.4). Of the
// closed eight-member domain, this tree wires six from real, already-
// measured data: push-dropped-restoring (a persisted intent to push that
// restoring drops), restore-head-collateral-risk (§7.4's fresh-run
// collateral-upper-bound hazard, probeRestore's own CollateralRisk signal),
// autostash-across-switch and checkout-dirty-present (§18.4, joined from
// req.StageFacts and entries[].context.dirty), collateral-update-refs-config
// (§11.7/§11.8, from each entry's own already-measured
// CollateralMechanism/EffectiveBackend), and untracked-present (§18.3, from
// each entry's own already-measured context.untracked_present). The
// remaining two are wired from the facts that really produce them:
// base-execution-context-split from EntryContexts' own same-store split
// (two DISTINCT contexts sharing one common dir, §4.2), and
// fetch-context-divergent from the bare EXTERNAL fallback's own process-cwd
// fetch context (§11.4) — never on the checkout route, whose sole
// context_source is workspace-repo-root.
func (b *planBuilder) buildWarnings(p restoreProbeResult, entries []PlanEntry, preps []entryPrep) []PlanWarning {
	var out []PlanWarning
	for _, prep := range preps {
		if !prep.CtxResult.SameStoreSplit {
			continue
		}
		name := prep.Entry.Name
		out = append(out, PlanWarning{
			Kind:   "base-execution-context-split",
			Entry:  &name,
			Detail: "base and execution contexts differ but share one object store",
		})
	}
	if b.req.Mode == ModeExternal {
		for _, ctx := range b.req.FetchPlan.Contexts {
			if ctx.Source != "process-cwd" {
				continue
			}
			out = append(out, PlanWarning{
				Kind:   "fetch-context-divergent",
				Detail: "the fetch would run in the process working directory, which may be a repository outside this workspace",
			})
			break
		}
	}
	if b.checkoutRestoringWithPush() {
		out = append(out, PlanWarning{
			Kind:   string(PushBlockedDroppedRestoring),
			Detail: "the persisted transaction intended to push, but restoring drops it",
		})
	}
	if p.CollateralRisk {
		out = append(out, PlanWarning{
			Kind:   "restore-head-collateral-risk",
			Detail: fmt.Sprintf("restore target %s falls within this run's own collateral upper bound", derefString(p.TargetBranch)),
		})
	}
	out = append(out, b.checkoutStageWarnings(entries)...)
	out = append(out, collateralUpdateRefsConfigWarnings(entries)...)
	out = append(out, untrackedPresentWarnings(entries)...)
	return ensureSlice(out)
}

// collateralUpdateRefsConfigWarnings is collateral-update-refs-config
// (§11.7/§11.8): one entry-scoped warning per row whose own collateral
// facts land in the collateral_mechanism:"config" cell — a
// rebase.updateRefs config value re-enabling ref movement on a row whose
// own argv omits --update-refs — which requires effective_backend=="merge"
// (ref-updating collateral is itself merge-backend-only behaviour: the same
// row under effective_backend "apply" publishes mechanism "none" instead,
// never this cell). Never a blocker, never a refusal.kind, never moves
// runnable.
func collateralUpdateRefsConfigWarnings(entries []PlanEntry) []PlanWarning {
	var out []PlanWarning
	for _, e := range entries {
		if e.CollateralMechanism == nil || *e.CollateralMechanism != "config" {
			continue
		}
		if e.EffectiveBackend == nil || *e.EffectiveBackend != "merge" {
			continue
		}
		name := e.Name
		out = append(out, PlanWarning{
			Kind:   "collateral-update-refs-config",
			Entry:  &name,
			Detail: fmt.Sprintf("%s: rebase.updateRefs re-enables collateral ref movement this row's own argv omits", name),
		})
	}
	return out
}

// untrackedPresentWarnings is untracked-present (§18.3): one entry-scoped
// warning per row whose own once-per-worktree untracked-files enumeration
// found untracked files at its execution directory. Warning-only by the
// spec's own rule: never a blocker, never a runnable consequence, and never
// a fingerprint member — purely a disclosure alongside that same row's own
// context.untracked_present/overwrite_risk facts.
func untrackedPresentWarnings(entries []PlanEntry) []PlanWarning {
	var out []PlanWarning
	for _, e := range entries {
		if e.Context.UntrackedPresent == nil || !*e.Context.UntrackedPresent {
			continue
		}
		name := e.Name
		out = append(out, PlanWarning{
			Kind:   "untracked-present",
			Entry:  &name,
			Detail: fmt.Sprintf("%s: untracked files are present at this entry's own execution directory", name),
		})
	}
	return out
}

// checkoutStageWarnings joins req.StageFacts (by entry name) with
// entries[].context.dirty to derive the two continuation-only,
// stage-position warnings of the closed eight-member domain (§18.4):
// autostash-across-switch fires whenever this entry's own working tree is
// dirty and its stage fact says an autostash will actually cover this arm;
// checkout-dirty-present fires whenever it is dirty and the entry's own
// stage is one of the three that always precede a standalone `git checkout`
// of that branch (planned/rebasing/restoring — checkoutStageFacts already
// defaults every later index's own Stage to "planned", so this one string
// check alone covers "the current index and every later index alike",
// exactly §18.4's own rule). Both are entry-scoped: unlike the two
// document-level warnings above, they set Entry.
func (b *planBuilder) checkoutStageWarnings(entries []PlanEntry) []PlanWarning {
	if len(b.req.StageFacts) == 0 {
		return nil
	}
	dirty := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Context.Dirty != nil && *e.Context.Dirty {
			dirty[e.Name] = true
		}
	}
	var out []PlanWarning
	for _, sf := range b.req.StageFacts {
		if !dirty[sf.Entry] {
			continue
		}
		name := sf.Entry
		if sf.AutostashApplies != nil && *sf.AutostashApplies {
			out = append(out, PlanWarning{
				Kind: "autostash-across-switch", Entry: &name,
				Detail: fmt.Sprintf("%s: an autostash will be created and reapplied across this checkout switch", name),
			})
		}
		if sf.Stage == "planned" || sf.Stage == "rebasing" || sf.Stage == "restoring" {
			out = append(out, PlanWarning{
				Kind: "checkout-dirty-present", Entry: &name,
				Detail: fmt.Sprintf("%s: the working tree is dirty ahead of a standalone checkout of this branch", name),
			})
		}
	}
	return out
}

// ============================================================================
// buildFreshness — RebasePlan.Freshness (§11.3).
// ============================================================================

// buildFreshness derives the top-level freshness token from the already-
// assembled fetch object (§11.3): a suppressed fetch (fetch.suppression_cause
// non-nil) reports that cause verbatim — the same 6-token vocabulary,
// §2.4's "no second vocabulary" rule; an un-attempted, unsuppressed fetch
// is "local-only" (policy said no fetch) unless repos[] resolved zero
// targets, in which case it is not-refreshed-no-fetch-targets; otherwise
// every attempted row is ranked by the one total table — never overlapping
// existential clauses — and the worst (highest-precedence) rank across all
// attempted rows wins: any ok:false ⇒ possibly-stale; else any unknown
// effect/reach ⇒ unknown-fetch-effect; else any contacted:false ⇒
// not-refreshed-no-resolved-remote; else fetched.
func buildFreshness(fetch PlanFetch) string {
	if fetch.SuppressionCause != nil {
		return *fetch.SuppressionCause
	}
	if !fetch.Attempted {
		if len(fetch.Repos) == 0 {
			return "local-only"
		}
		return "not-refreshed-no-fetch-targets"
	}

	sawUnknown, sawNotResolved := false, false
	for _, r := range fetch.Repos {
		if !r.Attempted {
			continue
		}
		if !r.OK {
			return "possibly-stale"
		}
		if r.Effect == nil {
			sawUnknown = true
			continue
		}
		if !r.Effect.Contacted {
			sawNotResolved = true
		}
	}
	switch {
	case sawUnknown:
		return "unknown-fetch-effect"
	case sawNotResolved:
		return "not-refreshed-no-resolved-remote"
	default:
		return "fetched"
	}
}

// ============================================================================
// BuildRebasePlan — the twenty-five-key document assembler (§4, §9.1a).
// ============================================================================

// BuildRebasePlan consumes an already-measured RebasePlanRequest (selection,
// order, state inspections, capabilities, config inventories) and assembles
// the full RebasePlan document. It performs no filesystem probe of its own
// beyond the read-helpers internal/rebase_plan_probe.go already exposes (the
// same ones internal/rebase_planner.go's pure decisions call), and it never
// re-implements §7.1/§7.2 precedence outside the one SelectPrimaryRefusal
// call below.
//
// Three-phase assembly. Phase 1 computes every document member the §8.2
// fingerprint tuple reads — push.targets, restore.applies/*, repositories,
// intent, policy, state, summary, guard.limits, approval.scope — all of
// which are decided independently of blockers[] (only push.executable/
// blocked_by and restore.executable/blocked_by are not, and neither is a
// tuple member), so a draft push/restore built with no blockers at all is
// final for these fields; the fingerprint is minted once, here, from a
// skeleton carrying exactly the tuple (§8.4's usable predicate gates
// whether minting happens at all). Phase 2 assembles every blocker
// candidate — entry-level (buildEntries), document-level
// (stack/selection/config/gate/preflight/restore/fetch-context/probe/
// selection-hazard/limit-conflict/approval facts) and the guard-introduced
// ranks 8 and 10-12 the freshly-minted fingerprint and evaluation rows make
// reachable — and calls SelectPrimaryRefusal exactly once over the union.
// Phase 3 finalizes runnable, push/restore's blocked-half, warnings, fetch,
// freshness and guard/approval against that one authoritative blockers[].
func BuildRebasePlan(req RebasePlanRequest) (RebasePlan, error) {
	b := newPlanBuilder(req)

	entries, execution, entryBlks, preps := b.buildEntries()
	plannability := plannabilityFor(req.RowsAvailable, len(entries))

	restoreProbe := b.probeRestore(execution, entries)
	verdicts := b.gateVerdicts()

	// ---- Phase 1: tuple members, independent of blockers[]. ----
	draftPush := b.buildPush(nil, plannability)
	draftRestore := buildRestore(restoreProbe, nil, plannability)
	repositories := b.buildRepositories(entries, draftRestore)
	intent := b.buildIntent()
	policy := b.buildPolicy()
	state := b.buildState()
	summary := buildSummary(entries, plannability)

	var requestedRoute *string
	if req.RequestedRoute != "" {
		v := req.RequestedRoute
		requestedRoute = &v
	}

	scope := "full-run"
	if req.Continue {
		scope = "remaining-work"
	}

	hasWork := len(entries) > 0 || len(draftPush.Targets) > 0 || draftRestore.Applies
	hasLimits := req.Limits.PerEntry.Value != nil || req.Limits.Total.Value != nil

	skeleton := RebasePlan{
		SchemaVersion:  RebasePlanSchemaVersion,
		Route:          req.Route,
		RequestedRoute: requestedRoute,
		RouteTriggers:  ensureSlice(req.RouteTriggers),
		Invocation:     req.Invocation,
		Workspace:      req.Workspace,
		Feature:        req.Feature,
		Policy:         policy,
		Intent:         intent,
		Push:           draftPush,
		Restore:        draftRestore,
		Repositories:   repositories,
		Entries:        ensureSlice(entries),
		Guard: PlanGuardBlock{
			Limits: PlanGuardLimitSet{MaxReplayPerEntry: req.Limits.PerEntry, MaxReplayTotal: req.Limits.Total},
		},
		Approval: PlanApproval{Scope: scope},
	}

	// §8.4 clause 3: the tuple binds identity/path bytes verbatim, so a
	// bound member that is not valid UTF-8 makes the token unmintable. The
	// probe runs over the SAME skeleton the tuple would be minted from, so
	// "what is bound" and "what is inspected" cannot diverge.
	encodingIssues := CollectPlanEncodingIssues(skeleton)
	encodingSafe := len(encodingIssues) == 0
	usable := hasWork && hasLimits && encodingSafe

	var fingerprint *string
	if usable {
		fp, err := PlanFingerprint(skeleton)
		if err != nil {
			return RebasePlan{}, fmt.Errorf("mint plan fingerprint: %w", err)
		}
		fingerprint = &fp
	}

	accepted := usable && fingerprint != nil && req.Approve != "" && req.Approve == *fingerprint

	// ---- Guard evaluation (§2.17.2-§2.17.4): pre-waiver rows drive
	// would_refuse_without_approval and the rank 11/12 candidates below; an
	// accepted approval waives every "exceeded" row (§8.5), which both
	// removes it from the rank 11/12 candidate set and recomputes
	// would_refuse from the post-waiver rows.
	preWaiverRows := guardEvaluationRows(req.Limits, entries, summary, plannability)
	wouldRefuseWithoutApproval := guardWouldRefuse(preWaiverRows)
	postWaiverRows, waivedIDs, waivedKinds := applyApprovalWaiver(preWaiverRows, accepted)
	wouldRefuse := guardWouldRefuse(postWaiverRows)

	// ---- Phase 2: full blocker candidate assembly. ----
	var candidates []PlanBlocker
	candidates = append(candidates, entryBlks...)
	candidates = append(candidates, stackBlockers(req)...)
	if bl := continuationGateBlocker(req); bl != nil {
		candidates = append(candidates, *bl)
	}
	candidates = append(candidates, GateBlockers(verdicts)...)
	candidates = append(candidates, checkoutGateBlockers(req.Gates)...)
	if bl := preflightBlocker(req); bl != nil {
		candidates = append(candidates, *bl)
	}
	if bl := restoreBlocker(restoreProbe); bl != nil {
		candidates = append(candidates, *bl)
	}
	candidates = append(candidates, req.FetchPlan.Blockers...)
	if bl := restoreProbeFailedBlocker(restoreProbe); bl != nil {
		candidates = append(candidates, *bl)
	}
	candidates = append(candidates, configIssueBlockers(b.configIssues)...)
	candidates = append(candidates, b.probeFailedBlockers()...)
	if bl := selectionHazardBlocker(entries); bl != nil {
		candidates = append(candidates, *bl)
	}
	candidates = append(candidates, limitConflictBlockers(req.LimitConflicts)...)
	if bl := approvalWithoutLimitsBlocker(req, hasLimits); bl != nil {
		candidates = append(candidates, *bl)
	}
	if bl := approvalMismatchBlocker(usable, fingerprint, req.Approve); bl != nil {
		candidates = append(candidates, *bl)
	}
	candidates = append(candidates, indeterminateEntryBlockers(entries, req.Limits)...)
	candidates = append(candidates, limitExceededBlockers(postWaiverRows)...)
	// Rank 5.08 is CONDITIONAL (§7.1): it is emitted exactly where the
	// document would mint or was given a token. A limitless rows-less plan
	// publishes encoding_issues[], emits no blocker and stays runnable.
	if bl := identityEncodingBlocker(encodingIssues, hasWork, hasLimits, req.Approve); bl != nil {
		candidates = append(candidates, *bl)
	}

	// SelectPrimaryRefusal is the SOLE §7.1/§7.2 implementation: it both
	// selects the single highest-precedence effective kind and produces the
	// final, ordered, exact-tuple-deduplicated blockers[] every downstream
	// projection below reads.
	refusalKind, blockers := SelectPrimaryRefusal(candidates)
	blockers = ensureSlice(blockers)

	var refusal PlanRefusal
	if refusalKind != "" {
		k := refusalKind
		d := blockers[0].Detail
		refusal = PlanRefusal{Kind: &k, Detail: &d}
	}

	// ---- Phase 3: finalize against the authoritative blockers[]. ----
	runnable := computeRunnable(blockers, plannability)

	push := b.buildPush(blockers, plannability)
	restore := buildRestore(restoreProbe, blockers, plannability)
	guard := b.buildGuard(verdicts, postWaiverRows, wouldRefuseWithoutApproval, wouldRefuse)
	approval := b.buildApproval(entries, push, restore, fingerprint, accepted, waivedIDs, waivedKinds, encodingSafe)
	warnings := b.buildWarnings(restoreProbe, entries, preps)
	fetch := b.buildFetch(preps)
	freshness := buildFreshness(fetch)

	plan := RebasePlan{
		SchemaVersion:  RebasePlanSchemaVersion,
		Route:          req.Route,
		RequestedRoute: requestedRoute,
		RouteTriggers:  ensureSlice(req.RouteTriggers),
		Invocation:     req.Invocation,
		Workspace:      req.Workspace,
		Feature:        req.Feature,
		Policy:         policy,
		Intent:         intent,
		Push:           push,
		Restore:        restore,
		Fetch:          fetch,
		Freshness:      freshness,
		Repositories:   repositories,
		State:          state,
		Runnable:       runnable,
		Blockers:       blockers,
		Warnings:       warnings,
		EncodingIssues: ensureSlice(encodingIssues),
		ConfigIssues:   ensureSlice(b.configIssues),
		Entries:        ensureSlice(entries),
		Summary:        summary,
		Guard:          guard,
		Refusal:        refusal,
		Approval:       approval,
	}

	return plan, nil
}

// ============================================================================
// RevalidatePlanEntry — the JIT re-probe entry point (§10.3/§10.10).
// ============================================================================

// PlanEntryRevalidation is RevalidatePlanEntry's report: whether one
// already-approved entry's non-collateral mutable Git facts still match a
// freshly re-probed measurement of the same logical row.
type PlanEntryRevalidation struct {
	Entry          PlanEntry    // the freshly re-probed row; zero value iff !Found
	Found          bool         // false iff no row named approved.Name exists in a fresh measurement
	ApprovedDigest string       // RevalidationDigest(approved), collateral-neutralized
	CurrentDigest  string       // RevalidationDigest(Entry), collateral-neutralized; "" iff !Found
	Drifted        bool         // true iff the digests differ, or the row disappeared entirely
	Blocker        *PlanBlocker // rank 9 revalidation-mismatch, set iff Drifted

	// ProbeFailed reports that a collateral-class seam could not MEASURE one
	// of §25.102's inputs — the branch-ref inventory this row's collateral
	// set is derived from. An unmeasured input is never read as "no
	// collateral": the caller refuses rank 5.9 probe-failed before any Git
	// mutation instead of comparing an unmeasured set against the approved
	// one (§22.33i (v-e-1)).
	ProbeFailed       bool
	ProbeFailedDetail string
}

// neutralizeRevalidationCollateral clears the three collateral-class members
// RevalidationDigest itself encodes (CollateralMechanism, CollateralExposed,
// CollateralRefs) before a JIT comparison: a single-row re-probe cannot
// correctly recompute them — §11.8's exposure accumulator needs every
// STRICTLY EARLIER row of the SAME run, in execution order, which re-probing
// one named entry in isolation does not reconstruct. They are instead
// re-verified by RevalidatePlanGuardEntry's own collateralDrifted comparison
// (internal/rebase_plan_guard.go), which compares the approved snapshot
// against the freshly re-measured row rather than re-deriving the
// accumulator.
func neutralizeRevalidationCollateral(e PlanEntry) PlanEntry {
	e.CollateralMechanism = nil
	e.CollateralExposed = nil
	e.CollateralRefs = nil
	return e
}

// neutralizeDeferredResolution drops exactly the facts a row with a deferred
// destination is DECLARED to resolve only at its JIT seam (§10, guard
// indeterminacy policy `jit-deferred`): the destination an earlier row of this
// same run rewrites, the base decision it follows, and the whole replay block
// that is measured against it. Their movement is the plan's own prediction
// coming true, never drift — the freshly measured values are still enforced
// against the effective limits by newlyExceededLimit.
func neutralizeDeferredResolution(e PlanEntry) PlanEntry {
	e.Destination.SHA = nil
	e.Destination.SnapshotSHA = nil
	// The deferral FLAG itself is one of the declared-deferred facts: the
	// approved row says "an earlier row of this run will rewrite this", and
	// at this row's own seam that has already happened, so the fresh
	// measurement legitimately reports false. Comparing the two would refuse
	// every deferred row for the plan's own prediction coming true.
	e.Destination.Deferred = false
	// depends_on names the earlier row that will rewrite this destination —
	// a declared-deferred fact that is null the moment the deferral has been
	// discharged, exactly as the flag itself is.
	e.Destination.DependsOn = nil
	e.Base.DecisionSHA = nil
	e.Replay = PlanEntryReplay{
		UpstreamProvenance:  e.Replay.UpstreamProvenance,
		Determinacy:         e.Replay.Determinacy,
		MayDropBecomesEmpty: e.Replay.MayDropBecomesEmpty,
	}
	e.Replay.UpstreamProvenance = ""
	e.Replay.Determinacy = ""
	return e
}

// RevalidatePlanEntry re-measures one already-approved entry's non-collateral
// mutable Git facts (§10.3's JIT re-probe list minus the collateral-class
// tuple: destination/head SHA, upstream, candidate identity and digest,
// strategy shape, in-progress rebase presence, holder-derived
// branch_checked_out_at) against a fresh run of the same read-only pipeline
// buildEntries itself uses, and reports whether they still match via
// RevalidationDigest. It answers only "did the mutable facts drift", never
// "should this run": the admit/refuse judgment, and the collateral-class
// re-measurement RevalidationDigest also encodes, belong to
// internal/rebase_plan_guard.go's RevalidatePlanGuardEntry. req
// must describe the SAME subject (layout, mode, stack, selection) the
// approved entry itself was built from; the caller owns re-establishing any
// input this invocation should re-read live (e.g. a freshly probed
// GitVersion or config inventory) before calling this function.
func RevalidatePlanEntry(req RebasePlanRequest, approved PlanEntry) (PlanEntryRevalidation, error) {
	b := newPlanBuilder(req)
	b.jitSeamFor = approved.Name

	// §25.102 input 3: a COLLATERAL-CLASS seam reloads the stack-owned
	// identity mapping from stack.yaml (a file read, zero Git processes) so
	// a `stack_owned` flip between admission and execution is seen rather
	// than carried over from the admission-time mapping. Only the mapping is
	// rebuilt: no TopoSort, no re-selection, no change of the approved
	// remaining set. A switch-only row — one whose published mechanism is
	// absent or `none` — issues no reload at all, exactly as it issues no
	// `for-each-ref`. §23.1 item 4 must be able to fail exactly this read,
	// which is why the seam is taken before the mapping is consulted.
	if seamIsCollateralClass(approved) {
		if err := syncIOFault(SyncIOReloadStack, StackPath(req.Layout.FeaturePath)); err != nil {
			return PlanEntryRevalidation{}, fmt.Errorf("reload stack identity: %w", err)
		}
		stack, err := LoadStack(req.Layout.FeaturePath)
		if err != nil {
			return PlanEntryRevalidation{}, fmt.Errorf("reload stack identity: %w", err)
		}
		b.stackOwned = map[string]bool{}
		for _, e := range stack.Branches {
			b.stackOwned[e.GitBranch()] = true
		}
	}

	entries, _, _, _ := b.buildEntries()

	// A row whose approved destination was deferred resolves that destination
	// — and everything measured against it — at this seam by design, so both
	// sides are digested with those declared-deferred facts neutralized.
	deferredRow := approved.Destination.Deferred
	normalize := func(e PlanEntry) PlanEntry {
		e = neutralizeRevalidationCollateral(e)
		if deferredRow {
			e = neutralizeDeferredResolution(e)
		}
		return e
	}

	approvedDigest, err := RevalidationDigest(normalize(approved))
	if err != nil {
		return PlanEntryRevalidation{}, fmt.Errorf("digest approved entry %s: %w", approved.Name, err)
	}

	var fresh PlanEntry
	found := false
	for _, e := range entries {
		if e.Name == approved.Name {
			fresh = e
			found = true
			break
		}
	}

	if !found {
		name := approved.Name
		return PlanEntryRevalidation{
			Found: false, ApprovedDigest: approvedDigest, Drifted: true,
			Blocker: &PlanBlocker{
				Kind: RefusalRevalidationMismatch, Entry: &name,
				Detail: fmt.Sprintf("entry %s is no longer present in a fresh measurement", name),
			},
		}, nil
	}

	currentDigest, err := RevalidationDigest(normalize(fresh))
	if err != nil {
		return PlanEntryRevalidation{}, fmt.Errorf("digest current entry %s: %w", approved.Name, err)
	}

	result := PlanEntryRevalidation{
		Entry: fresh, Found: true,
		ApprovedDigest: approvedDigest, CurrentDigest: currentDigest,
		Drifted: approvedDigest != currentDigest,
	}
	// A collateral-class row whose mechanism survived but whose refs came
	// back null did not measure the ref inventory: computeCollateral returns
	// a non-nil (possibly empty) slice on every successful measurement, so
	// null here means "unmeasured", never "no collateral".
	if seamIsCollateralClass(fresh) && fresh.CollateralRefs == nil {
		result.ProbeFailed = true
		result.ProbeFailedDetail = fmt.Sprintf(
			"entry %s's collateral ref inventory could not be measured at the revalidation seam", fresh.Name)
	}
	if result.Drifted {
		name := approved.Name
		result.Blocker = &PlanBlocker{
			Kind: RefusalRevalidationMismatch, Entry: &name,
			Detail: fmt.Sprintf("entry %s's mutable git facts changed since approval", name),
		}
	}
	return result, nil
}

// seamIsCollateralClass answers "does this approved row's JIT seam owe the
// two extra §25.102 measurements?" — the `for-each-ref` ref inventory and
// the stack.yaml reload. A row is collateral-class exactly when the document
// published a real collateral mechanism for it (`argv` or `config`); an
// absent mechanism (the row was `--update-refs`-gated) and the explicit
// `none` mechanism of a switch-only row both answer no, so such a seam
// issues neither input.
func seamIsCollateralClass(approved PlanEntry) bool {
	return approved.CollateralMechanism != nil && *approved.CollateralMechanism != "none"
}
