package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// safe-reparent-restack — the package-cli fixture ledger (§17.1, exploration
// §7.1).
//
// Go test files compile into the package they declare, so an `internal` test
// can never call a builder declared here and vice versa. The eight §17.1
// shapes are therefore CONCEPTUAL shapes with two package-local realizations;
// this file owns the CLI-level realization, which every end-to-end, customer
// topology, issue-#4, remote, recovery, exclusion and observability cell uses.
//
// Every builder sets GIT_CONFIG_COUNT=0 and GIT_CONFIG_NOSYSTEM=1 through the
// shipped helpers, so a hostile host `git config` can never leak into a
// measurement — which matters most for the cells that deliberately set hostile
// `rebase.*` config of their own.
// ---------------------------------------------------------------------------

// reparentFixture is one CLI-level reparent subject: the repository, the
// feature identity, the resolved feature path, and the SHAs the assertions
// name.
type reparentFixture struct {
	t *testing.T

	Mode        internal.WorkspaceMode
	Repo        string
	Feature     string
	FeaturePath string

	// SHAs the customer topology names. Empty on shapes that do not define
	// them.
	B string // the ORIGINAL, rewritten parent commit — the recorded cutoff
	D string // the destination tip
	C string // the single commit that genuinely belongs to the target
}

func reparentTestGitCommand(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	return testGitCommand(t, dir, true, args...)
}

func testGitCommand(t *testing.T, dir string, countReparent bool, args ...string) *exec.Cmd {
	t.Helper()
	if countReparent {
		reparentRecordGitArgv(t, args...)
	}
	t.Logf("reparent test git argv: git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1")
	return cmd
}

// reparentGit runs one git command in the fixture and returns trimmed stdout.
func (f *reparentFixture) reparentGit(dir string, args ...string) string {
	f.t.Helper()
	cmd := reparentTestGitCommand(f.t, dir, args...)
	cmd.Env = append(cmd.Env, "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// SHA resolves one ref in the fixture repository.
func (f *reparentFixture) SHA(ref string) string {
	f.t.Helper()
	return f.reparentGit(f.Repo, "rev-parse", ref)
}

// Stack reloads the feature's stack.yaml from disk.
func (f *reparentFixture) Stack() internal.Stack {
	f.t.Helper()
	stack, err := internal.LoadStack(f.FeaturePath)
	if err != nil {
		f.t.Fatalf("load stack: %v", err)
	}
	return stack
}

// Entry returns one logical entry by name.
func (f *reparentFixture) Entry(name string) internal.StackEntry {
	f.t.Helper()
	for _, e := range f.Stack().Branches {
		if e.Name == name {
			return e
		}
	}
	f.t.Fatalf("entry %q is not in stack.yaml", name)
	return internal.StackEntry{}
}

// Loc is the fixture's reparent artifact location.
func (f *reparentFixture) Loc() internal.ReparentLocation {
	f.t.Helper()
	ws, err := internal.RequireWorkspace()
	if err != nil {
		f.t.Fatalf("resolve workspace: %v", err)
	}
	return internal.ReparentLocationFor(ws, f.Feature, f.FeaturePath)
}

func reparentApprovedBeginInput(t *testing.T, f *reparentFixture, target, onto string) (internal.ReparentPlanInput, internal.ReparentPlan, internal.ReparentRequest) {
	t.Helper()
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	limit := 50
	route := &reparentRoute{
		Ws:             ws,
		Loc:            f.Loc(),
		Feature:        f.Feature,
		Target:         target,
		Onto:           onto,
		OntoKind:       "auto",
		Policy:         internal.SyncFetchDisabled,
		DefaultApplied: false,
		Guard: internal.CheckoutPlanGuard{
			MaxTotal: &limit,
			Present:  map[string]bool{"max-replay-total": true},
		},
	}
	in := route.planInput(internal.ReparentRouteFresh, internal.ReparentInvocationPlanOnly, internal.PlanFetchOutcome{})
	plan, req, err := internal.PlanReparent(in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Approval.Fingerprint == nil {
		t.Fatalf("race fixture did not mint an approval token: %+v", plan.Blockers)
	}
	in.Invocation = internal.ReparentInvocationExecute
	in.Guard.Approve = *plan.Approval.Fingerprint
	return in, plan, req
}

// Run drives one `tws stack reparent …` invocation through the production
// command tree and captures both process streams.
func (f *reparentFixture) Run(args ...string) (stdout, stderr string, exit int) {
	f.t.Helper()
	full := append([]string{"reparent"}, args...)
	stdout, stderr = syncCaptureStreams(f.t, func() {
		exit = syncExecute(stackCmd, full...)
	})
	return stdout, stderr, exit
}

// Plan runs the plan route and fails the test when it does not exit 0: §13.3
// makes exit status meaningless as an admission predicate, so a non-zero exit
// on a plan route is always a defect.
func (f *reparentFixture) Plan(args ...string) (stdout, stderr string) {
	f.t.Helper()
	stdout, stderr, exit := f.Run(append(args, "--plan")...)
	if exit != 0 {
		f.t.Fatalf("a produced plan must exit 0, got %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	return stdout, stderr
}

// reparentFingerprintShape is the token's exact syntactic domain, so an
// extractor can never hand a blocker detail or a truncated line to
// --approve-plan.
var reparentFingerprintShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// reparentFingerprint extracts the approval fingerprint from a human plan by
// reading the `approval` section's own `fingerprint:` line — an anchored,
// prefix-matched extraction, never a `tail -1` pipe, which would silently pick
// up a blocker detail.
func reparentFingerprint(t *testing.T, human string) string {
	t.Helper()
	const prefix = "  fingerprint: "
	for _, line := range strings.Split(human, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		token := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if reparentFingerprintShape.MatchString(token) {
			return token
		}
	}
	t.Fatalf("the plan minted no usable approval fingerprint:\n%s", human)
	return ""
}

// ---------------------------------------------------------------------------
// Shape 1/2 — the customer topology, extended with a real pr1
// ---------------------------------------------------------------------------

// newReparentCustomerExternal extends setupCustomerTopologyExternal with a
// real `pr1` entry and re-points `pr2` at it, which is what turns the shipped
// sync fixture into a REPARENT subject: `pr2`'s configured parent is now a
// sibling stack entry, and the reparent under test moves it back onto the
// default branch.
//
//	master = A - B' - D     (B rewritten into B'; B is NOT an ancestor of D)
//	pr1    = A - B  - P1
//	pr2    = A - B  - C     (configured parent pr1, recorded cutoff B)
func newReparentCustomerExternal(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	repo, bSHA, dSHA, cSHA := setupCustomerTopologyExternal(t)

	// pr1 forks at the ORIGINAL B, in its own worktree, and adds P1.
	pr1 := internal.WorktreePath("feature", "pr1")
	gitRun(t, repo, "worktree", "add", pr1, "-b", "feat-pr1", bSHA)
	writeAndCommit(t, pr1, "p1.txt", "P1\n", "P1")

	featurePath := internal.FeaturePath("feature")
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "pr1", Branch: "feat-pr1", Base: "master", LastBaseSHA: bSHA},
		{Name: "pr2", Branch: "feat-pr2", Base: "pr1", LastBaseSHA: bSHA},
	}}); err != nil {
		t.Fatal(err)
	}
	return &reparentFixture{
		t: t, Mode: internal.ModeExternal, Repo: repo,
		Feature: "feature", FeaturePath: featurePath,
		B: bSHA, D: dSHA, C: cSHA,
	}
}

// newReparentCustomerCheckout is the checkout twin of the shape above.
func newReparentCustomerCheckout(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	dir, featurePath, bSHA, dSHA, cSHA := setupCustomerTopologyCheckout(t)

	// pr1 forks at the ORIGINAL B and adds P1. The checkout is returned to
	// pr2 afterwards, for the default-branch-fallback reason
	// setupCustomerTopologyCheckout documents.
	gitRunCS(t, dir, "checkout", "-b", "pr1", bSHA)
	writeFileCS(t, dir, "p1.txt", "P1\n")
	gitRunCS(t, dir, "add", "p1.txt")
	gitRunCS(t, dir, "commit", "-m", "P1")
	gitRunCS(t, dir, "checkout", "pr2")

	saveTestStack(t, featurePath, []internal.StackEntry{
		{Name: "pr1", Base: "main", LastBaseSHA: bSHA},
		{Name: "pr2", Base: "pr1", LastBaseSHA: bSHA},
	})
	// Every checkout cell drives the PRODUCTION command tree, which resolves
	// its workspace from the current directory. Without this the run would
	// resolve the developer's own repository instead of the fixture.
	withCheckoutEnv(t, dir)
	return &reparentFixture{
		t: t, Mode: internal.ModeCheckout, Repo: dir,
		Feature: "test-feature", FeaturePath: featurePath,
		B: bSHA, D: dSHA, C: cSHA,
	}
}

// ---------------------------------------------------------------------------
// Shape 3/4 — issue #4's branching closure
// ---------------------------------------------------------------------------

// newReparentIssue4External builds issue #4's own stack shape:
//
//	master -> pr1 -> { pr2, pr3 -> pr5 -> pr6, pr4 }
//
// Reparenting pr1 moves a closure that BRANCHES twice and is three deep on one
// arm, which is the shape a linear fixture cannot exercise: it is the only way
// to prove the closure order is deterministic, that a descendant's cutoff is
// snapshotted per row rather than re-derived after its parent moved, and that
// one CAS transaction commits every branch of the fan-out together.
func newReparentIssue4External(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	masterTip := gitOutput(t, repo, "rev-parse", "HEAD")

	type node struct{ name, parent, base, lastBase string }
	order := []node{
		{"pr1", "", "master", masterTip},
		{"pr2", "pr1", "pr1", ""},
		{"pr3", "pr1", "pr1", ""},
		{"pr4", "pr1", "pr1", ""},
		{"pr5", "pr3", "pr3", ""},
		{"pr6", "pr5", "pr5", ""},
	}
	tips := map[string]string{"": masterTip}
	entries := make([]internal.StackEntry, 0, len(order))
	for i := range order {
		n := &order[i]
		branch := "feat-" + n.name
		wt := internal.WorktreePath("feature", n.name)
		gitRun(t, repo, "worktree", "add", wt, "-b", branch, tips[n.parent])
		writeAndCommit(t, wt, n.name+".txt", n.name+"\n", n.name)
		tips[n.name] = gitOutput(t, wt, "rev-parse", "HEAD")
		if n.lastBase == "" {
			n.lastBase = tips[n.parent]
		}
		entries = append(entries, internal.StackEntry{
			Name: n.name, Branch: branch, Base: n.base, LastBaseSHA: n.lastBase,
		})
	}

	// master advances, so reparenting pr1 onto master has real work to do.
	writeAndCommit(t, repo, "m2.txt", "M2\n", "M2")
	dSHA := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "push", "--force", "origin", "master")
	gitRun(t, repo, "fetch", "origin")

	featurePath := internal.FeaturePath("feature")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: entries}); err != nil {
		t.Fatal(err)
	}
	return &reparentFixture{
		t: t, Mode: internal.ModeExternal, Repo: repo,
		Feature: "feature", FeaturePath: featurePath,
		B: masterTip, D: dSHA,
	}
}

// newReparentIssue4Checkout is the checkout twin of issue #4's shape.
func newReparentIssue4Checkout(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := setupCheckoutSyncRepo(t)
	masterTip := gitSHA(t, dir, "HEAD")

	type node struct{ name, parent, base string }
	order := []node{
		{"pr1", "", "main"},
		{"pr2", "pr1", "pr1"},
		{"pr3", "pr1", "pr1"},
		{"pr4", "pr1", "pr1"},
		{"pr5", "pr3", "pr3"},
		{"pr6", "pr5", "pr5"},
	}
	tips := map[string]string{"": masterTip}
	entries := make([]internal.StackEntry, 0, len(order))
	for _, n := range order {
		from := "main"
		if n.parent != "" {
			from = n.parent
		}
		gitRunCS(t, dir, "checkout", from)
		gitRunCS(t, dir, "checkout", "-b", n.name)
		writeFileCS(t, dir, n.name+".txt", n.name+"\n")
		gitRunCS(t, dir, "add", ".")
		gitRunCS(t, dir, "commit", "-m", n.name)
		tips[n.name] = gitSHA(t, dir, "HEAD")
		entries = append(entries, internal.StackEntry{
			Name: n.name, Base: n.base, LastBaseSHA: tips[n.parent],
		})
	}

	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "m2.txt", "M2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "M2")
	dSHA := gitSHA(t, dir, "HEAD")
	gitRunCS(t, dir, "checkout", "pr1")

	writeCheckoutModeMarker(t, dir)
	featurePath := setupFeaturePath(t, dir)
	saveTestStack(t, featurePath, entries)
	withCheckoutEnv(t, dir)
	return &reparentFixture{
		t: t, Mode: internal.ModeCheckout, Repo: dir,
		Feature: "test-feature", FeaturePath: featurePath,
		B: masterTip, D: dSHA,
	}
}

// ---------------------------------------------------------------------------
// Shapes 5-8 — the four variants
// ---------------------------------------------------------------------------

// newReparentNoCutoffExternal is the customer shape with `pr2`'s recorded
// cutoff cleared, which is §5.1's "no recorded base" rung.
func newReparentNoCutoffExternal(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	f := newReparentCustomerExternal(t)
	stack := f.Stack()
	for i := range stack.Branches {
		if stack.Branches[i].Name == "pr2" {
			stack.Branches[i].LastBaseSHA = ""
		}
	}
	if err := internal.SaveStack(f.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	return f
}

// newReparentStaleDescendantExternal is the customer shape with `pr2`'s cutoff
// deliberately OLDER than `pr1`'s current tip, so the descendant's own
// snapshotted cutoff and its parent's tip disagree — the exact condition issue
// #4's stale-cutoff half describes.
func newReparentStaleDescendantExternal(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	f := newReparentCustomerExternal(t)
	root := f.reparentGit(f.Repo, "rev-list", "--max-parents=0", "HEAD")
	stack := f.Stack()
	for i := range stack.Branches {
		if stack.Branches[i].Name == "pr2" {
			stack.Branches[i].LastBaseSHA = root
		}
	}
	if err := internal.SaveStack(f.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	return f
}

// newReparentDecoupledExternal is the branch-name-decoupling shape: every
// logical Name differs from its Git branch, and the names contain `/`, which
// is exactly what a ref-component or path-component derived from a raw Name
// would break on (issue #1).
func newReparentDecoupledExternal(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	masterTip := gitOutput(t, repo, "rev-parse", "HEAD")

	wt1 := internal.WorktreePath("feature", "team/api")
	gitRun(t, repo, "worktree", "add", wt1, "-b", "user/jd/api", masterTip)
	writeAndCommit(t, wt1, "api.txt", "api\n", "api")
	api := gitOutput(t, wt1, "rev-parse", "HEAD")

	wt2 := internal.WorktreePath("feature", "team/web")
	gitRun(t, repo, "worktree", "add", wt2, "-b", "user/jd/web", api)
	writeAndCommit(t, wt2, "web.txt", "web\n", "web")

	writeAndCommit(t, repo, "m2.txt", "M2\n", "M2")
	dSHA := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "push", "--force", "origin", "master")
	gitRun(t, repo, "fetch", "origin")

	featurePath := internal.FeaturePath("feature")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "team/api", Branch: "user/jd/api", Base: "master", LastBaseSHA: masterTip},
		{Name: "team/web", Branch: "user/jd/web", Base: "team/api", LastBaseSHA: api},
	}}); err != nil {
		t.Fatal(err)
	}
	return &reparentFixture{
		t: t, Mode: internal.ModeExternal, Repo: repo,
		Feature: "feature", FeaturePath: featurePath,
		B: masterTip, D: dSHA,
	}
}

// reparentReftableSupported reports whether this Git can create a reftable
// repository at all. Every reftable cell skips with a clear reason when it
// cannot, and its files-backend twin is mandatory and never skipped: CI pins
// no Git version, so a reftable-only assertion would silently vanish on the
// older matrix leg.
func reparentReftableSupported(t *testing.T) bool {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "reftable-probe")
	cmd := reparentTestGitCommand(t, "", "init", "--ref-format=reftable", dir)
	return cmd.Run() == nil
}

// newReparentReftableCheckout is the reftable variant of the issue-#4 checkout
// shape. It returns nil when the backend is unavailable, so the caller skips
// with a reason rather than failing.
func newReparentReftableCheckout(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	if !reparentReftableSupported(t) {
		return nil
	}
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	init := reparentTestGitCommand(t, "", "init", "--ref-format=reftable", "--initial-branch=main", dir)
	if out, err := init.CombinedOutput(); err != nil {
		t.Logf("reftable init failed: %v\n%s", err, out)
		return nil
	}
	gitRunCS(t, dir, "config", "user.email", "test@test.com")
	gitRunCS(t, dir, "config", "user.name", "Test")
	writeFileCS(t, dir, ".gitignore", ".tws/\n")
	writeFileCS(t, dir, "README.md", "# root\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "initial")
	masterTip := gitSHA(t, dir, "HEAD")

	gitRunCS(t, dir, "checkout", "-b", "pr1")
	writeFileCS(t, dir, "pr1.txt", "pr1\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "pr1")
	pr1 := gitSHA(t, dir, "HEAD")

	gitRunCS(t, dir, "checkout", "-b", "pr2")
	writeFileCS(t, dir, "pr2.txt", "pr2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "pr2")

	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "m2.txt", "M2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "M2")
	dSHA := gitSHA(t, dir, "HEAD")
	gitRunCS(t, dir, "checkout", "pr1")

	writeCheckoutModeMarker(t, dir)
	featurePath := setupFeaturePath(t, dir)
	saveTestStack(t, featurePath, []internal.StackEntry{
		{Name: "pr1", Base: "main", LastBaseSHA: masterTip},
		{Name: "pr2", Base: "pr1", LastBaseSHA: pr1},
	})
	withCheckoutEnv(t, dir)
	return &reparentFixture{
		t: t, Mode: internal.ModeCheckout, Repo: dir,
		Feature: "test-feature", FeaturePath: featurePath,
		B: masterTip, D: dSHA,
	}
}

// ---------------------------------------------------------------------------
// Argv audit
// ---------------------------------------------------------------------------

// reparentArgvLog records every argv the reparent boundary emits, through the
// documented ReparentGitArgvHook seam. It is the cheap substrate every
// forbidden-verb and `-C` audit reads.
type reparentArgvLog struct {
	Dirs []string
	Argv [][]string
}

// reparentCaptureArgv installs the hook for the duration of one test.
func reparentCaptureArgv(t *testing.T) *reparentArgvLog {
	t.Helper()
	log := &reparentArgvLog{}
	internal.ReparentGitArgvHook = func(dir string, args []string) {
		log.Dirs = append(log.Dirs, dir)
		log.Argv = append(log.Argv, args)
	}
	t.Cleanup(func() { internal.ReparentGitArgvHook = nil })
	return log
}

// Lines renders the log as one space-joined argv per line, for substring
// assertions that do not care about the directory.
func (l *reparentArgvLog) Lines() []string {
	out := make([]string, 0, len(l.Argv))
	for _, args := range l.Argv {
		out = append(out, strings.Join(args, " "))
	}
	return out
}

// Contains reports whether any emitted argv contains the given token.
func (l *reparentArgvLog) Contains(token string) bool {
	for _, args := range l.Argv {
		for _, a := range args {
			if a == token {
				return true
			}
		}
	}
	return false
}

// ContainsPair reports whether any emitted argv contains two adjacent tokens.
func (l *reparentArgvLog) ContainsPair(first, second string) bool {
	for _, args := range l.Argv {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == first && args[i+1] == second {
				return true
			}
		}
	}
	return false
}

// reparentRunAllowFail runs one git command and returns its combined output
// together with the error, so a caller can treat a non-zero exit as an answer
// rather than a failure — which is exactly what an ancestry question needs.
func reparentRunAllowFail(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := reparentTestGitCommand(t, dir, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// newReparentStatusExternal builds an external feature under the workspace's
// OWN metadata root, which is what every status surface resolves against.
//
// It exists beside the customer builders deliberately: those extend the
// shipped sync fixtures, which place the feature under TWS_ROOT and rely on
// resolveExternalSyncLayout to reconcile the two candidate roots. The status
// surfaces have no such reconciliation — they take the path
// ws.ResolveFeaturePath produced — so an observability cell must build the
// feature where those surfaces look.
func newReparentStatusExternal(t *testing.T) *reparentFixture {
	t.Helper()
	reparentCountCLIGitLeaf(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)

	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	masterTip := gitOutput(t, repo, "rev-parse", "HEAD")

	featurePath := ws.FeaturePath("feature")
	wt := filepath.Join(featurePath, "worktrees", "pr1")
	gitRun(t, repo, "worktree", "add", wt, "-b", "feat-pr1", masterTip)
	writeAndCommit(t, wt, "pr1.txt", "pr1\n", "pr1")
	pr1 := gitOutput(t, wt, "rev-parse", "HEAD")

	wt2 := filepath.Join(featurePath, "worktrees", "pr2")
	gitRun(t, repo, "worktree", "add", wt2, "-b", "feat-pr2", pr1)
	writeAndCommit(t, wt2, "pr2.txt", "pr2\n", "pr2")

	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "pr1", Branch: "feat-pr1", Base: "master", LastBaseSHA: masterTip},
		{Name: "pr2", Branch: "feat-pr2", Base: "pr1", LastBaseSHA: pr1},
	}}); err != nil {
		t.Fatal(err)
	}
	return &reparentFixture{
		t: t, Mode: internal.ModeExternal, Repo: repo,
		Feature: "feature", FeaturePath: featurePath,
		B: masterTip,
	}
}

// argvVerb returns the Git verb an emitted argv actually runs, skipping the
// leading `-c <key>=<value>` overrides the replay pins its configuration with.
// Comparing argv[0] directly would silently classify every pinned rebase as
// "not a rebase".
func argvVerb(argv []string) string {
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "-c":
			i++
		case strings.HasPrefix(argv[i], "-"):
			continue
		default:
			return argv[i]
		}
	}
	return ""
}
