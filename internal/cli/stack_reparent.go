package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// safe-reparent-restack — the package-cli half of the feature (spec §3).
//
// This file owns the `tws stack reparent` command surface and nothing else:
// the route-aware arity validator (§3.2), the closed 13-flag set (§3.4), the
// 14-rule validation ladder (§3.5), the completion functions (§3.3), the
// mode/layout resolution ladder, the one policy-declared fetch (§3.4), and
// the four route dispatches into package internal.
//
// Every fact that can only be measured in package cli — the external fetch,
// the external session inventory — is measured HERE and handed to
// internal.PlanReparent as an input, so package internal never imports
// package cli and the plan builder stays pure.
// ---------------------------------------------------------------------------

// reparentLongText is the command's Long help. It is pinned by
// internal/cli/testdata/reparent/reparent_help.txt.
const reparentLongText = `Move one stack entry onto a new parent and replay its descendants.

A reparent changes the recorded topology of a stack: the target entry's base
becomes the destination, every descendant is replayed onto its new parent, and
stack.yaml records the new base and the new cutoff for every moved row in one
atomic write.

--plan describes the reparent this invocation would perform and then exits. It
moves no branch, writes no tws runtime state, and makes no provider call. It is
not Git-write-free: a plan fetches exactly where the run it describes fetches —
an external plan fetches by default, a checkout plan only with --fetch.

A fresh execution is always guarded. It requires --approve-plan with the
fingerprint a --plan printed and at least one of --max-replay-per-entry or
--max-replay-total; there is no unguarded reparent route. The limits bound this
invocation only and are re-enforced against freshly measured counts immediately
before each row is computed.

--continue resumes the persisted run and --abort rolls it back. Both take the
target, destination, cutoff, policy, limits, validation command and closure
from persisted state; supplying any of them refuses before any lock or Git
command. Neither fetches.

tws changes no remote ref and no pull request. A reparented branch whose pull
request still points at the old base is recorded locally so the next push warns
and strengthens its lease.`

// ---------------------------------------------------------------------------
// §3.5 — the shared sentence constants this command reuses byte-identically.
// Rules 1-3, 5, 6, 10, 11 and 12 reuse literals hoisted in sync_plan_guard.go
// and sync_modes.go; rule 2 calls errSyncContinueAbort() directly. The
// sentences below are the ones §3.5 introduces and nothing else shares.
// ---------------------------------------------------------------------------
const (
	errMsgReparentOntoKind        = "--onto-kind must be one of: auto, entry, ref"
	errMsgReparentOntoRequired    = "--onto requires a destination"
	errMsgReparentCutoffRequired  = "--cutoff requires a ref"
	errMsgReparentNeedsApproval   = "tws stack reparent requires --approve-plan <fingerprint>; run with --plan first"
	errMsgReparentNeedsAnyLimit   = "tws stack reparent requires --max-replay-per-entry or --max-replay-total"
	reparentCompletionSuppression = "reparent"
)

// reparentRouteExclusiveFlags is §3.5 rule 4's ordered flag list. The order is
// the spec's own and is what makes "the first failure" deterministic.
var reparentRouteExclusiveFlags = []string{
	"onto", "onto-kind", "cutoff", "approve-plan",
	"max-replay-per-entry", "max-replay-total", "fetch", "no-fetch",
}

// reparentOntoKinds is the closed --onto-kind domain, also the completion set.
var reparentOntoKinds = []string{"auto", "entry", "ref"}

const reparentImplicitBool = "\x00"

type reparentPresenceBool struct {
	value        *bool
	everExplicit bool
}

func (v *reparentPresenceBool) String() string {
	if v == nil || v.value == nil {
		return "false"
	}
	return strconv.FormatBool(*v.value)
}

func (v *reparentPresenceBool) Set(raw string) error {
	if raw == reparentImplicitBool {
		*v.value = true
		return nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	*v.value = parsed
	v.everExplicit = true
	return nil
}

func (*reparentPresenceBool) Type() string { return "bool" }
func (v *reparentPresenceBool) Get() any   { return *v.value }

func reparentBoolWasExplicit(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		return false
	}
	value, ok := flag.Value.(*reparentPresenceBool)
	return ok && value.everExplicit
}

func stackReparentCmd() *cobra.Command {
	var onto string
	var ontoKind string
	var cutoff string
	var plan bool
	var planJSON bool
	var maxPerEntry int
	var maxTotal int
	var approvePlan string
	var doFetch bool
	var noFetch bool
	var cont bool
	var abort bool
	var verbose bool

	cmd := &cobra.Command{
		Use:   "reparent <feature> <entry> --onto <dest>",
		Short: "Move a stack entry onto a new parent and replay its descendants",
		Long:  reparentLongText,
		Args:  stackReparentArgs,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return reparentEntryCandidates(args[0], ""), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// §6.3 step 1: the pure flag ladder runs before ANY workspace
			// probe, so a control error refuses with zero Git children and
			// zero state reads, and keeps its usage block.
			if err := stackReparentValidate(cmd, args); err != nil {
				return err
			}
			// §3.5: the assignment sits strictly AFTER rule 14, which is what
			// makes "a §3.5 failure keeps the usage block, a §13 refusal does
			// not" true.
			cmd.SilenceUsage = true
			cmd.Root().SilenceUsage = true
			cmd.Root().SilenceErrors = true

			return runStackReparent(cmd, args)
		},
	}

	cmd.Flags().StringVar(&onto, "onto", "", "Destination the target entry is reparented onto")
	cmd.Flags().StringVar(&ontoKind, "onto-kind", "auto", "How --onto is interpreted: auto, entry, or ref")
	cmd.Flags().StringVar(&cutoff, "cutoff", "", "Operator-authored replay boundary for the target entry")
	cmd.Flags().BoolVar(&plan, "plan", false, "Describe the reparent this invocation would perform, then exit; moves no branch and writes no tws state, but still fetches wherever the route it describes fetches (see --no-fetch)")
	cmd.Flags().BoolVar(&planJSON, "json", false, "Emit the plan document as JSON on stdout (requires --plan)")
	cmd.Flags().IntVar(&maxPerEntry, "max-replay-per-entry", 0, "Refuse before computing if any row of this invocation replays more than N candidates (this invocation only)")
	cmd.Flags().IntVar(&maxTotal, "max-replay-total", 0, "Refuse before computing if this invocation replays more than N candidates in total (this invocation only)")
	cmd.Flags().StringVar(&approvePlan, "approve-plan", "", "Approve the exact plan with the fingerprint printed by --plan (required on a fresh execution)")
	cmd.Flags().Var(&reparentPresenceBool{value: &doFetch}, "fetch", "Fetch before planning (external default)")
	cmd.Flags().Lookup("fetch").NoOptDefVal = reparentImplicitBool
	cmd.Flags().Var(&reparentPresenceBool{value: &noFetch}, "no-fetch", "Plan and compute from local refs only; no automatic network input (checkout default)")
	cmd.Flags().Lookup("no-fetch").NoOptDefVal = reparentImplicitBool
	cmd.Flags().BoolVar(&cont, "continue", false, "Resume the persisted reparent run")
	cmd.Flags().BoolVar(&abort, "abort", false, "Roll the persisted reparent run back")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show full git fetch output")

	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		restore := reparentBoolFlagsForDisplay(c)
		defer restore()
		defaultHelp(c, args)
	})
	defaultUsage := cmd.UsageFunc()
	cmd.SetUsageFunc(func(c *cobra.Command) error {
		restore := reparentBoolFlagsForDisplay(c)
		defer restore()
		return defaultUsage(c)
	})

	_ = cmd.RegisterFlagCompletionFunc("onto", reparentOntoCompletion)
	_ = cmd.RegisterFlagCompletionFunc("onto-kind", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return reparentOntoKinds, cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

func reparentBoolFlagsForDisplay(cmd *cobra.Command) func() {
	previous := map[string]string{}
	for _, name := range []string{"fetch", "no-fetch"} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			continue
		}
		previous[name] = flag.NoOptDefVal
		flag.NoOptDefVal = "true"
	}
	return func() {
		for name, value := range previous {
			cmd.Flags().Lookup(name).NoOptDefVal = value
		}
	}
}

// ---------------------------------------------------------------------------
// §3.2 — arity
// ---------------------------------------------------------------------------

// stackReparentArgs is the route-aware arity validator. Cobra parses flags
// BEFORE it calls ValidateArgs, so this validator may read the route flags and
// implement §3.2's per-route arity table in the Args phase.
//
// The collision hint fires only at len(args) == 0, exactly as stackStatusArgs
// does: an ordinary "received 1" on the fresh route is Cobra's own message.
// Route detection reads syncBoolFlag's VALUE, not Changed, so
// `--continue=false` stays a fresh route.
func stackReparentArgs(cmd *cobra.Command, args []string) error {
	want := 2
	if syncBoolFlag(cmd, "continue") || syncBoolFlag(cmd, "abort") {
		want = 1
	}
	if len(args) == want {
		return nil
	}
	if len(args) != 0 {
		return cobra.ExactArgs(want)(cmd, args)
	}
	features, err := internal.ListFeaturesE()
	if err != nil {
		// The user's actual mistake is the missing argument. Argument
		// validation must not convert a workspace fault into a
		// usage-suppressed failure.
		return cobra.ExactArgs(want)(cmd, args)
	}
	for _, name := range features {
		if name == reparentCompletionSuppression {
			return fmt.Errorf(
				`accepts %d arg(s), received %d: a feature named "reparent" exists; run "tws stack -- reparent" for its legacy dependency tree`,
				want, len(args))
		}
	}
	return cobra.ExactArgs(want)(cmd, args)
}

// ---------------------------------------------------------------------------
// §3.5 — the 14-rule ladder, in exactly this order, returning on the first
// failure. It deliberately does NOT call resolvePlanGuardOptions: that
// function evaluates a different order, knows nothing of --continue
// combinations, and has no --onto/--onto-kind/--cutoff rules, so calling it
// would produce the wrong FIRST failure on several fixtures.
// ---------------------------------------------------------------------------

func stackReparentValidate(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	present := func(name string) bool { return flags.Changed(name) }

	plan := syncBoolFlag(cmd, "plan")
	cont := syncBoolFlag(cmd, "continue")
	abort := syncBoolFlag(cmd, "abort")

	// 1 — --json requires --plan.
	if present("json") && !plan {
		return fmt.Errorf("%s", errMsgJSONRequiresPlan)
	}
	// 2 — --continue with --abort.
	if cont && abort {
		return errSyncContinueAbort()
	}
	// 3 — --plan with --abort.
	if present("plan") && abort {
		return fmt.Errorf("%s", errMsgPlanWithAbort)
	}
	// 4 — the eight fresh-route flags are incompatible with either recovery
	// verb. --continue is named first because rule 2 already removed the
	// both-verbs case.
	if cont || abort {
		verb := "--continue"
		if abort && !cont {
			verb = "--abort"
		}
		for _, name := range reparentRouteExclusiveFlags {
			if present(name) {
				return fmt.Errorf("--%s cannot be combined with %s", name, verb)
			}
		}
	}
	// 5 — --fetch with --no-fetch.
	if present("fetch") && present("no-fetch") {
		return fmt.Errorf("%s", errMsgFetchNoFetchExclusive)
	}
	// 6 — the two axis selectors are presence-only.
	if present("fetch") && reparentBoolWasExplicit(cmd, "fetch") {
		return fmt.Errorf("%s", errMsgFetchExplicitValue)
	}
	if present("no-fetch") && reparentBoolWasExplicit(cmd, "no-fetch") {
		return fmt.Errorf("%s", errMsgNoFetchExplicitValue)
	}
	// 7 — --onto-kind's closed domain.
	kind := strings.TrimSpace(syncStringFlag(cmd, "onto-kind"))
	if present("onto-kind") && !reparentKindKnown(kind) {
		return fmt.Errorf("%s", errMsgReparentOntoKind)
	}
	fresh := !plan && !cont && !abort
	// 8 — the fresh ROUTE (which includes --plan) requires a destination.
	if !cont && !abort && strings.TrimSpace(syncStringFlag(cmd, "onto")) == "" {
		return fmt.Errorf("%s", errMsgReparentOntoRequired)
	}
	// 9 — --cutoff present and empty.
	if present("cutoff") && strings.TrimSpace(syncStringFlag(cmd, "cutoff")) == "" {
		return fmt.Errorf("%s", errMsgReparentCutoffRequired)
	}
	// 10 — both limits must be zero or greater.
	if present("max-replay-per-entry") {
		v, _ := flags.GetInt("max-replay-per-entry")
		if v < 0 {
			return fmt.Errorf("%s", errMsgMaxPerEntryNonNegative)
		}
	}
	if present("max-replay-total") {
		v, _ := flags.GetInt("max-replay-total")
		if v < 0 {
			return fmt.Errorf("%s", errMsgMaxTotalNonNegative)
		}
	}
	// 11 — the token's syntactic shape.
	if present("approve-plan") {
		if !planApproveTokenShape.MatchString(strings.TrimSpace(syncStringFlag(cmd, "approve-plan"))) {
			return fmt.Errorf("%s", errMsgApproveTokenShape)
		}
	}
	// 12 — a token requires a limit.
	if present("approve-plan") && !present("max-replay-per-entry") && !present("max-replay-total") {
		return fmt.Errorf("%s", errMsgApproveRequiresLimits)
	}
	// 13, 14 — the fresh EXECUTION route is always guarded. Both rules are
	// guarded by the fresh-execution predicate and are therefore unreachable
	// on --plan, --continue and --abort.
	if fresh && strings.TrimSpace(syncStringFlag(cmd, "approve-plan")) == "" {
		return fmt.Errorf("%s", errMsgReparentNeedsApproval)
	}
	if fresh && !present("max-replay-per-entry") && !present("max-replay-total") {
		return fmt.Errorf("%s", errMsgReparentNeedsAnyLimit)
	}
	_ = args
	return nil
}

func reparentKindKnown(kind string) bool {
	for _, k := range reparentOntoKinds {
		if kind == k {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// §3.3 — completion
// ---------------------------------------------------------------------------

// reparentOntoCompletion offers, in order, the sibling stack entry names of
// the feature (excluding the target and its descendants), then local branch
// names. syncEntryCompletion is the shape precedent but is NOT reusable: it
// excludes no descendant closure and appends no branches.
func reparentOntoCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	target := ""
	if len(args) > 1 {
		target = args[1]
	}
	out := reparentEntryCandidates(args[0], target)
	out = append(out, reparentBranchCandidates()...)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// reparentEntryCandidates lists the feature's stack entry names, excluding the
// target and every descendant of it. Completion failures are silent: an
// unresolvable workspace offers no candidates rather than an error.
func reparentEntryCandidates(feature, target string) []string {
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return nil
	}
	if internal.GuardFeatureName(ws.MetadataRoot, feature) != nil {
		return nil
	}
	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return nil
	}
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return nil
	}
	excluded := map[string]bool{}
	if target != "" {
		excluded[target] = true
		closure, cerr := internal.ReparentClosureOrder(stack, target)
		if cerr == nil {
			for _, e := range closure {
				excluded[e.Name] = true
			}
		}
	}
	out := make([]string, 0, len(stack.Branches))
	for _, e := range stack.Branches {
		if excluded[e.Name] {
			continue
		}
		out = append(out, e.Name)
	}
	return out
}

// reparentBranchCandidates lists local branch names through the shipped
// single-`for-each-ref` inventory. It is a completion-only probe and never
// participates in a plan or a run.
func reparentBranchCandidates() []string {
	ws, err := internal.RequireWorkspace()
	if err != nil || ws.RepoRoot == "" {
		return nil
	}
	inv := internal.BuildBranchRefInventory(ws.RepoRoot)
	if !inv.Available {
		return nil
	}
	names := make([]string, 0, len(inv.ByRef))
	for ref := range inv.ByRef {
		names = append(names, strings.TrimPrefix(ref, "refs/heads/"))
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// Mode / layout resolution and route dispatch (exploration §2.4)
// ---------------------------------------------------------------------------

// reparentRoute is the resolved subject of one invocation: everything the
// four route bodies need, resolved once on the ladder of §2.4.
type reparentRoute struct {
	Cmd     *cobra.Command
	Ws      internal.Workspace
	Cfg     internal.Config
	Loc     internal.ReparentLocation
	Feature string
	Target  string

	Plan     bool
	JSON     bool
	Continue bool
	Abort    bool
	Verbose  bool

	Onto     string
	OntoKind string
	Cutoff   string

	Guard internal.CheckoutPlanGuard

	Policy         internal.SyncFetchPolicy
	DefaultApplied bool
	TargetRepo     internal.ReparentTargetRepository
	TmuxProbe      internal.TmuxInventoryProbe

	Writers internal.ReparentWriters

	GitLookupErr error
}

func requireReparentWorkspace(cfg internal.Config) (internal.Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return internal.Workspace{}, err
	}
	return internal.ResolveReparentWorkspace(cwd, cfg)
}

func runStackReparent(cmd *cobra.Command, args []string) error {
	_, gitLookupErr := exec.LookPath("git")
	cfg := internal.LoadConfig()
	ws, err := requireReparentWorkspace(cfg)
	var fallbackErr error
	if err != nil && gitLookupErr != nil {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			var fallback internal.Workspace
			if fallback, fallbackErr = internal.ResolveReparentWorkspaceWithoutGit(cwd, cfg); fallbackErr == nil {
				ws = fallback
				err = nil
			}
		}
	}
	if err != nil {
		if gitLookupErr != nil {
			if syncBoolFlag(cmd, "plan") && fallbackErr != nil {
				return fallbackErr
			}
			return fmt.Errorf("required tool %q not found in PATH: %w", "git", gitLookupErr)
		}
		return err
	}

	feature := args[0]
	if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
		return err
	}

	var featurePath string
	if ws.Mode == internal.ModeCheckout {
		featurePath, err = ws.ResolveFeaturePath(feature)
		if err != nil {
			return err
		}
	} else {
		twsRoot := strings.TrimSpace(os.Getenv("TWS_ROOT"))
		if twsRoot == "" {
			twsRoot = ws.MetadataRoot
		}
		layout, lerr := resolveExternalSyncLayout(ws, twsRoot, feature)
		if lerr != nil {
			return lerr
		}
		featurePath = layout.FeaturePath
	}

	r := &reparentRoute{
		Cmd:      cmd,
		Ws:       ws,
		Cfg:      cfg,
		Loc:      internal.ReparentLocationFor(ws, feature, featurePath),
		Feature:  feature,
		Plan:     syncBoolFlag(cmd, "plan"),
		JSON:     syncBoolFlag(cmd, "json"),
		Continue: syncBoolFlag(cmd, "continue"),
		Abort:    syncBoolFlag(cmd, "abort"),
		Verbose:  syncBoolFlag(cmd, "verbose"),
		Onto:     syncStringFlag(cmd, "onto"),
		OntoKind: reparentResolvedKind(cmd),
		Cutoff:   syncStringFlag(cmd, "cutoff"),
		Writers: internal.ReparentWriters{
			Doc:   cmd.OutOrStdout(),
			Prose: cmd.ErrOrStderr(),
		},
		GitLookupErr: gitLookupErr,
	}
	if len(args) > 1 {
		r.Target = args[1]
	}
	r.Guard = reparentGuardOptions(cmd)
	r.Policy, r.DefaultApplied = reparentFetchPolicy(cmd, ws.Mode)
	if r.GitLookupErr != nil {
		r.GitLookupErr = fmt.Errorf("required tool %q not found in PATH: %w", "git", r.GitLookupErr)
		if !r.Plan {
			return reparentRefusalLine(cmd, r.GitLookupErr)
		}
	}

	// Route order is abort -> plan -> continue -> execute, and the plan rung
	// MUST sit above the continue rung. §3.2 forbids --plan --abort but
	// explicitly ADMITS --plan --continue, which is a read-only §7.12a
	// document describing the run a resume would finish. Matching --continue
	// first would make that combination EXECUTE the resume: a documented
	// preview would mutate refs, stack.yaml and state.
	switch {
	case r.Abort:
		return reparentRefusalLine(cmd, r.runAbort())
	case r.Plan:
		return reparentRefusalLine(cmd, r.runPlan())
	case r.Continue:
		return reparentRefusalLine(cmd, r.runContinue())
	default:
		return reparentRefusalLine(cmd, r.runExecute())
	}
}

// reparentResolvedKind reads --onto-kind, defaulting to "auto".
func reparentResolvedKind(cmd *cobra.Command) string {
	kind := strings.TrimSpace(syncStringFlag(cmd, "onto-kind"))
	if kind == "" {
		return "auto"
	}
	return kind
}

// reparentGuardOptions projects the three guard flags into the shared
// internal-owned guard value. It re-runs no validation: §3.5 already proved
// every value legal.
func reparentGuardOptions(cmd *cobra.Command) internal.CheckoutPlanGuard {
	g := internal.CheckoutPlanGuard{
		Plan:    syncBoolFlag(cmd, "plan"),
		JSON:    syncBoolFlag(cmd, "json"),
		Approve: strings.TrimSpace(syncStringFlag(cmd, "approve-plan")),
		Present: map[string]bool{},
	}
	for _, name := range planGuardControlFlags {
		g.Present[name] = cmd.Flags().Changed(name)
	}
	if cmd.Flags().Changed("max-replay-per-entry") {
		v, _ := cmd.Flags().GetInt("max-replay-per-entry")
		g.MaxPerEntry = &v
	}
	if cmd.Flags().Changed("max-replay-total") {
		v, _ := cmd.Flags().GetInt("max-replay-total")
		g.MaxTotal = &v
	}
	return g
}

// reparentFetchPolicy mirrors resolveSyncPolicy's mode-derived default
// exactly: fetch in external mode, no-fetch in checkout mode.
func reparentFetchPolicy(cmd *cobra.Command, mode internal.WorkspaceMode) (internal.SyncFetchPolicy, bool) {
	policy := internal.SyncFetchEnabled
	if mode == internal.ModeCheckout {
		policy = internal.SyncFetchDisabled
	}
	defaultApplied := !cmd.Flags().Changed("fetch") && !cmd.Flags().Changed("no-fetch")
	if cmd.Flags().Changed("fetch") {
		policy = internal.SyncFetchEnabled
	}
	if cmd.Flags().Changed("no-fetch") {
		policy = internal.SyncFetchDisabled
	}
	return policy, defaultApplied
}

// ---------------------------------------------------------------------------
// The one policy-declared fetch (§3.4, §6.3 step 2a, exploration §3.8)
// ---------------------------------------------------------------------------

// fetchReparentOnce performs the single fetch of a plan route or a fresh
// execution. It runs EXACTLY ONCE per invocation — the measured outcome is
// carried in ReparentPlanInput and re-used by the post-lock re-snapshot, which
// is what keeps a fresh execution from fetching twice — and never on
// --continue or --abort, which never reach it.
func (r *reparentRoute) fetchReparentOnce() (internal.PlanFetchOutcome, error) {
	if r.Policy != internal.SyncFetchEnabled {
		return internal.PlanFetchOutcome{Applies: false, Repos: []internal.PlanFetchRepoResult{}}, nil
	}
	if err := r.prepareFreshTarget(); err != nil {
		return internal.PlanFetchOutcome{}, err
	}
	ctx := internal.PlanFetchContext{
		RepoToken:  r.TargetRepo.RepoToken,
		Root:       r.TargetRepo.Root,
		Source:     r.TargetRepo.Source,
		Candidates: []internal.PlanFetchCandidate{},
	}
	errw := r.Cmd.ErrOrStderr()
	if r.Loc.Mode == internal.ModeCheckout {
		// The checkout twin delegates to the shipped fetchCheckoutRepoTo, so
		// reparent and sync emit byte-identical fetch prose.
		return internal.FetchReparentCheckoutRepo(errw, ctx), nil
	}
	// The external arm calls the shipped cli-owned fetch helper with BOTH
	// writers pointed at stderr: stdout carries the plan document and nothing
	// else (§3.6).
	row := fetchQuietTo(errw, errw, r.TargetRepo.RepoToken, r.TargetRepo.Root, r.Verbose, ctx)
	outcome := internal.PlanFetchOutcome{Applies: true, Repos: []internal.PlanFetchRepoResult{row}}
	if row.Attempted {
		outcome.Attempted = true
	}
	return outcome, nil
}

// ---------------------------------------------------------------------------
// Session liveness (§6.2) — measured FRESH at every seam
// ---------------------------------------------------------------------------

// reparentSessionProbe is the closure internal.PlanReparent re-runs at every
// measurement seam, including the post-lock re-snapshot. Returning it rather
// than a pre-measured slice is what makes a session started inside the
// concurrency window a blocker rather than an unnoticed hazard.
func (r *reparentRoute) reparentSessionProbe() func([]string) ([]string, error) {
	return func(affected []string) ([]string, error) {
		if r.Loc.Mode == internal.ModeCheckout {
			return reparentCheckoutLiveSessions(r.Ws, r.Feature)
		}
		return reparentExternalLiveSessions(r.Loc.FeaturePath, r.Feature, affected, r.TmuxProbe)
	}
}

func (r *reparentRoute) reparentSessionIntentCleanup() func([]string) error {
	if r.Loc.Mode == internal.ModeCheckout {
		return func([]string) error {
			return internal.CleanupStaleCheckoutSessionIntent(r.Ws)
		}
	}
	return func(affected []string) error {
		targets, _ := reparentExternalSessionTargets(r.Feature, affected)
		_, staleDirect, directErr := internal.GuardDirectSessionsFor(r.Loc.FeaturePath, targets, nil)
		if directErr != nil {
			return directErr
		}
		_, staleIntents, intentErr := internal.LoadExternalSessionIntents(
			r.Loc.FeaturePath, r.Feature, affected, nil)
		if intentErr != nil {
			return intentErr
		}
		_, removeDirectErr := internal.RemoveStaleDirectSessions(r.Loc.FeaturePath, staleDirect)
		_, removeIntentErr := internal.RemoveStaleExternalSessionIntents(r.Loc.FeaturePath, staleIntents)
		return errors.Join(removeDirectErr, removeIntentErr)
	}
}

// reparentExternalLiveSessions reports the logical entry names with a live or
// unverifiable direct-session record. GuardDirectSessionsFor's own predicate is
// reused verbatim: blocking = live OR unknown(EPERM) OR State != ok.
func reparentExternalLiveSessions(featurePath, feature string, affected []string, tmux internal.TmuxInventoryProbe) ([]string, error) {
	targets, nameByID := reparentExternalSessionTargets(feature, affected)
	blocking, _, err := internal.GuardDirectSessionsFor(featurePath, targets, nil)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	affectedSet := make(map[string]bool, len(affected))
	for _, name := range affected {
		affectedSet[name] = true
	}
	var names []string
	for _, rec := range blocking {
		name := rec.Record.Name
		if name == "" {
			name = nameByID[rec.BranchID]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	intents, _, err := internal.LoadExternalSessionIntents(featurePath, feature, affected, nil)
	if err != nil {
		return nil, err
	}
	for _, intent := range intents {
		if intent.State != internal.DirectRecordOK || intent.Record.Scope == internal.ExternalSessionIntentFeature {
			for _, name := range affected {
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
			continue
		}
		for _, name := range intent.Record.Names {
			if affectedSet[name] && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	if tmux == nil {
		tmux = internal.RealTmuxInventory{}
	}
	snap := tmux.Snapshot()
	if snap.Err != nil {
		return nil, snap.Err
	}
	if snap.Available && snap.ServerRunning {
		if snap.Sessions[internal.ExternalFeatureTmuxSessionName(feature)] {
			for _, name := range affected {
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
		for _, name := range affected {
			if snap.Sessions[internal.ExternalTmuxSessionName(feature, name)] && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func reparentExternalSessionTargets(feature string, affected []string) ([]internal.DirectSessionTarget, map[string]string) {
	targets := make([]internal.DirectSessionTarget, 0, len(affected))
	nameByID := make(map[string]string, len(affected))
	for _, name := range affected {
		want := internal.DirectSessionIdentity{Feature: feature, Name: name}
		branchID := internal.DirectSessionBranchID(feature, name)
		nameByID[branchID] = name
		targets = append(targets, internal.DirectSessionTarget{
			BranchID: branchID,
			Want:     &want,
		})
	}
	return targets, nameByID
}

// reparentCheckoutLiveSessions reports the entry a checkout agent session is
// currently holding. Checkout mode admits at most one session per workspace,
// so the answer is a zero- or one-element slice.
func reparentCheckoutLiveSessions(ws internal.Workspace, feature string) ([]string, error) {
	if present, pid, err := internal.CheckoutSessionIntent(ws); err != nil {
		return nil, fmt.Errorf("checkout session launch intent is unverifiable: %w", err)
	} else if present {
		return []string{fmt.Sprintf("<checkout-launch-intent pid=%d>", pid)}, nil
	}
	present, err := internal.CheckoutAgentSessionPresence(ws)
	if err != nil {
		return nil, fmt.Errorf("checkout session state is unverifiable: %w", err)
	}
	if !present {
		return nil, nil
	}
	state, err := internal.LoadCheckoutAgentSession(ws)
	if err != nil {
		// An undecodable session record counts as live (§6.2): a failed probe
		// refuses rather than admitting the run.
		return nil, err
	}
	if state == nil {
		return nil, nil
	}
	name := state.Name
	if name == "" {
		name = state.Feature
	}
	if name == "" {
		name = "<workspace>"
	}
	return []string{name}, nil
}

// ---------------------------------------------------------------------------
// Validation identity (§9.6) — configured, frozen at admission, never a flag
// ---------------------------------------------------------------------------

func (r *reparentRoute) validationIdentity() internal.PlanValidationIdentity {
	command := strings.TrimSpace(r.Cfg.TestCommand)
	if command == "" {
		return internal.PlanValidationIdentity{Source: "none"}
	}
	source := "config-workspace"
	if r.TargetRepo.Root != "" &&
		strings.TrimSpace(internal.LoadConfigFile(internal.RepoConfigPathFor(r.TargetRepo.Root)).TestCommand) == command {
		source = "config-repo"
	}
	return internal.PlanValidationIdentity{
		Applies: true,
		Command: command,
		Source:  source,
		Digest:  internal.ValidationDigest(command),
	}
}

func (r *reparentRoute) prepareFreshTarget() error {
	if r.TargetRepo.Root != "" {
		return nil
	}
	target, err := internal.ResolveReparentTargetRepository(
		r.Loc.Mode, r.Loc.FeaturePath, r.Ws.RepoRoot, r.Target,
	)
	if err != nil {
		return err
	}
	r.TargetRepo = target
	r.Cfg = internal.LoadConfigForRepo(target.Root)
	return nil
}

// ---------------------------------------------------------------------------
// The four routes
// ---------------------------------------------------------------------------

// planInput assembles the read-only route input. invocation is either
// ReparentInvocationPlanOnly or ReparentInvocationExecute; the fetch outcome
// is measured by the caller, exactly once.
func (r *reparentRoute) planInput(route, invocation string, fetch internal.PlanFetchOutcome) internal.ReparentPlanInput {
	return internal.ReparentPlanInput{
		Loc:                  r.Loc,
		RepoRoot:             r.Ws.RepoRoot,
		TargetRepoRoot:       r.TargetRepo.Root,
		Route:                route,
		Invocation:           invocation,
		Target:               r.Target,
		OntoToken:            r.Onto,
		OntoKind:             r.OntoKind,
		CutoffToken:          r.Cutoff,
		Guard:                r.Guard,
		FetchPolicy:          r.Policy,
		FetchDefaultApplied:  r.DefaultApplied,
		FetchOutcome:         fetch,
		Validation:           r.validationIdentity(),
		Workspace:            reparentWorkspaceProjection(r.Ws),
		SessionProbe:         r.reparentSessionProbe(),
		SessionIntentCleanup: r.reparentSessionIntentCleanup(),
		ApprovedFingerprint:  r.Guard.Approve,
		Writers:              r.Writers,
	}
}

// reparentWorkspaceProjection mirrors externalWorkspace but is mode-aware:
// both reparent modes publish their own mode token.
func reparentWorkspaceProjection(ws internal.Workspace) internal.PlanWorkspace {
	var stableID *string
	if ws.StableID != "" {
		id := ws.StableID
		stableID = &id
	}
	return internal.PlanWorkspace{Mode: string(ws.Mode), StableID: stableID, RepoRoot: ws.RepoRoot}
}

// runPlan is the read-only route: fetch once, build, render, exit 0 — even
// when the document publishes a refusal (§13.3).
func (r *reparentRoute) runPlan() error {
	// A --continue --plan describes a run that was already approved and is
	// mid-flight. §3.4 and AC-051 give the one fetch to the fresh routes
	// only: refreshing remote-tracking refs here would mutate the repository
	// on a read-only preview of a resume, and the resume itself never
	// fetches either.
	route := internal.ReparentRouteFresh
	fetch := internal.PlanFetchOutcome{Applies: false, Repos: []internal.PlanFetchRepoResult{}}
	if r.GitLookupErr != nil {
		if r.Continue {
			route = internal.ReparentRouteContinue
		}
		in := r.planInput(route, internal.ReparentInvocationPlanOnly, fetch)
		unavailable, err := internal.BuildUnavailableReparentPlan(in, r.GitLookupErr)
		if err != nil {
			return err
		}
		return renderReparentDocument(r.Cmd, unavailable, r.JSON)
	}
	if r.Continue {
		in := r.planInput(internal.ReparentRouteContinue, internal.ReparentInvocationPlanOnly, fetch)
		plan, err := internal.PlanReparentContinue(in)
		if err != nil {
			unavailable, buildErr := internal.BuildUnavailableReparentPlan(in, err)
			if buildErr != nil {
				return buildErr
			}
			return renderReparentDocument(r.Cmd, unavailable, r.JSON)
		}
		return renderReparentDocument(r.Cmd, plan, r.JSON)
	} else {
		if err := r.prepareFreshTarget(); err != nil {
			in := r.planInput(route, internal.ReparentInvocationPlanOnly, fetch)
			unavailable, buildErr := internal.BuildUnavailableReparentPlan(in, err)
			if buildErr != nil {
				return buildErr
			}
			return renderReparentDocument(r.Cmd, unavailable, r.JSON)
		}
		var err error
		fetch, err = r.fetchReparentOnce()
		if err != nil {
			in := r.planInput(route, internal.ReparentInvocationPlanOnly, fetch)
			unavailable, buildErr := internal.BuildUnavailableReparentPlan(in, err)
			if buildErr != nil {
				return buildErr
			}
			return renderReparentDocument(r.Cmd, unavailable, r.JSON)
		}
	}
	in := r.planInput(route, internal.ReparentInvocationPlanOnly, fetch)
	plan, _, err := internal.PlanReparent(in)
	if err != nil {
		unavailable, buildErr := internal.BuildUnavailableReparentPlan(in, err)
		if buildErr != nil {
			return buildErr
		}
		return renderReparentDocument(r.Cmd, unavailable, r.JSON)
	}
	return renderReparentDocument(r.Cmd, plan, r.JSON)
}

// runExecute is the fresh execution route. §3.5 rules 13 and 14 already proved
// a token and a limit were supplied, so this body never has to re-derive the
// mandatory-guard policy.
func (r *reparentRoute) runExecute() error {
	// §6.3 step 9: mutual exclusion and state classification precede the plan
	// build. §11.2's matrix admits only --continue and --abort while a run is
	// recorded, so a fresh EXECUTION refuses here — before the fetch, before
	// the lock, and before anything is measured. The plan route deliberately
	// does not refuse: it DESCRIBES the artifact under state.exclusion.
	if _, err := internal.RefuseIfReparentActive(r.Loc, internal.ReparentRouteVerbFresh); err != nil {
		return err
	}
	if err := r.prepareFreshTarget(); err != nil {
		return err
	}
	fetch, err := r.fetchReparentOnce()
	if err != nil {
		return err
	}
	in := r.planInput(internal.ReparentRouteFresh, internal.ReparentInvocationExecute, fetch)
	plan, req, err := internal.PlanReparent(in)
	if err != nil {
		return err
	}
	if err := internal.AdmitFreshReparent(plan, r.Guard); err != nil {
		return err
	}
	run, err := internal.BeginReparentRun(internal.ReparentBeginInput{Input: in, Plan: plan, Request: req})
	if err != nil {
		return err
	}
	if err := internal.RunReparent(run); err != nil {
		return err
	}
	r.reportSuccess(run, plan)
	return nil
}

// runContinue resumes the persisted run. It NEVER fetches: the persisted
// fetch policy is recorded for audit only.
func (r *reparentRoute) runContinue() error {
	in := r.planInput(internal.ReparentRouteContinue, internal.ReparentInvocationExecute, internal.PlanFetchOutcome{})
	in.Resumable = true
	run, err := internal.ContinueReparent(in)
	if err != nil {
		return err
	}
	r.reportSuccess(run, internal.ReparentPlan{})
	return nil
}

// runAbort rolls the persisted run back. It NEVER fetches and never renders a
// document: §3.2 forbids --plan --abort.
func (r *reparentRoute) runAbort() error {
	in := r.planInput(internal.ReparentRouteContinue, internal.ReparentInvocationExecute, internal.PlanFetchOutcome{})
	in.Resumable = true
	run, err := internal.AbortReparent(in)
	if err != nil {
		return err
	}
	if run == nil || run.State == nil {
		return nil
	}
	if run.HolderRestorationPending() {
		return nil
	}
	// §11.8a has two arms and they are not interchangeable. Before the commit
	// point the run was rolled back; at or after it, --abort is forward
	// completion only — it moved no ref, rewrote no stack.yaml, and preserved
	// the remote record. Saying "was rolled back" there would tell the
	// operator their topology change had been undone when it is still live.
	// The arm is read from the run itself, which decided it from disk.
	if run.AbortWasForwardOnly() {
		fmt.Fprintf(r.Cmd.ErrOrStderr(), //nolint:errcheck
			"reparent: run %s for %q had already committed; no ref or metadata was changed and its artifacts were cleaned up. Undoing it is a new reparent in the opposite direction\n",
			run.State.RunID, r.Feature)
		return nil
	}
	fmt.Fprintf(r.Cmd.ErrOrStderr(), "reparent: run %s for %q was rolled back\n", run.State.RunID, r.Feature) //nolint:errcheck
	return nil
}

// reportSuccess prints the §3.6 success summary and the §12.2 remote guidance
// on stderr. Stdout stays reserved for the plan document on every route.
func (r *reparentRoute) reportSuccess(run *internal.ReparentRun, plan internal.ReparentPlan) {
	errw := r.Cmd.ErrOrStderr()
	_ = plan
	if run == nil {
		return
	}
	for _, line := range run.SuccessLines() {
		fmt.Fprintf(errw, "%s\n", line) //nolint:errcheck
	}
}

// ---------------------------------------------------------------------------
// §3.6 — render and §13.2 — the anchored refusal line
// ---------------------------------------------------------------------------

// renderReparentDocument is the one-line wrapper passing cmd.OutOrStdout() and
// cmd.ErrOrStderr() to renderReparentDocumentTo, mirroring renderPlanDocument.
func renderReparentDocument(cmd *cobra.Command, plan internal.ReparentPlan, jsonMode bool) error {
	return renderReparentDocumentTo(cmd.OutOrStdout(), cmd.ErrOrStderr(), plan, jsonMode)
}

// renderReparentDocumentTo is the writer-taking render dispatch: the sole
// caller of FormatReparentPlan/MarshalReparentPlan in package cli. It performs
// exactly one stdout.Write of the complete buffer and writes nothing at all
// when the renderer failed.
func renderReparentDocumentTo(stdout, stderr io.Writer, plan internal.ReparentPlan, jsonMode bool) error {
	_ = stderr
	var buf []byte
	var err error
	if jsonMode {
		buf, err = internal.MarshalReparentPlan(plan)
	} else {
		buf, err = internal.FormatReparentPlan(plan)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(buf)
	return err
}

type reparentRenderedError struct{ message string }

func (e *reparentRenderedError) Error() string { return e.message }

// reparentRefusalLine prints the anchored marker for a reparent-owned refusal
// and returns err unchanged, exactly as planGuardRefusal does for the guard
// domain. A conflict PAUSE prints its five-line block and carries no marker; a
// guard refusal keeps its own `plan-guard:` line and never gains a second one.
func reparentRefusalLine(cmd *cobra.Command, err error) error {
	_ = cmd
	if err == nil {
		return nil
	}
	var pause *internal.ReparentConflictPause
	if errors.As(err, &pause) {
		return &reparentRenderedError{message: strings.TrimSuffix(pause.Message(), "\n")}
	}
	var refusal *internal.ReparentRefusalError
	if errors.As(err, &refusal) {
		return internal.AnchorReparentRefusal(err)
	}
	var guard *internal.PlanGuardRefusalError
	if errors.As(err, &guard) {
		return &reparentRenderedError{message: "plan-guard: " + internal.SanitizeReparentLine(guard.Error())}
	}
	return &reparentRenderedError{message: internal.SanitizeReparentLine(err.Error())}
}

// reparentPathHasScratchComponent reports whether any component of a path is a
// reparent-owned runtime artifact. It delegates to the single internal-owned
// predicate so the import filter, the runtime inventories and the Git
// worktree-inventory filter can never drift apart (§14.2a).
func reparentPathHasScratchComponent(path string) bool {
	return internal.IsReparentRuntimePath(path)
}
