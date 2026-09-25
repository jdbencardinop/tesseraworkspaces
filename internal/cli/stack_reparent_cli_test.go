package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// Acceptance criteria carried by this file's cells: AC-002 and AC-003 (the
// command is named `reparent` and its help is a pinned golden), AC-004 through
// AC-010 (the §3.5 validation order, the arity table, the fresh-only rules and
// the shipped message parity), AC-005 (recovery verbs reject the guard flags),
// AC-006, AC-007 (flag domains and their exact sentences), AC-099 (the golden
// snapshots themselves).

// ---------------------------------------------------------------------------
// T-038 — §3.5's ordered ladder, §3.2's arity table, and §3.5's usage-block
// rule.
// T-085 — the help/golden snapshots and the feature literally named
// `reparent`.
//
// Every cell here is PURE: the ladder runs above any workspace probe, so none
// of these leaves spawns a Git child. That is what makes the real-Git leaf
// budget of §17.3 reachable.
// ---------------------------------------------------------------------------

// reparentMarkerRe is §13.2's marker grammar, anchored to one physical line.
var reparentMarkerRe = regexp.MustCompile(`(?m)^reparent: [a-z][a-z-]*: .*$`)

// runReparentBare drives `tws stack reparent …` with no workspace at all. It
// is the vehicle for every §3.5 cell: the ladder must refuse before the
// command ever looks for a workspace.
func runReparentBare(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	full := append([]string{"reparent"}, args...)
	stdout, stderr = syncCaptureStreams(t, func() {
		exit = syncExecute(stackCmd, full...)
	})
	return stdout, stderr, exit
}

// runTWSExecute drives the REAL root command exactly as cmd/tws does, so a
// captured snapshot carries production command paths. runSyncExecute is the
// same idea pinned to `tws sync`; this is its command-agnostic twin.
func runTWSExecute(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"tws"}, args...)
	stdout, stderr = syncCaptureStreams(t, func() { exit = Execute() })
	return stdout, stderr, exit
}

func assertReparentExternalPayloadSelectedArray(t *testing.T, stdout string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode plan JSON: %v\n%s", err, stdout)
	}
	state, ok := doc["state"].(map[string]any)
	if !ok {
		t.Fatalf("state is %T, want object", doc["state"])
	}
	files, ok := state["files"].(map[string]any)
	if !ok {
		t.Fatalf("state.files is %T, want object", state["files"])
	}
	payload, ok := files["external_run_payload"].(map[string]any)
	if !ok {
		t.Fatalf("state.files.external_run_payload is %T, want object", files["external_run_payload"])
	}
	selected, ok := payload["selected"].([]any)
	if !ok || len(selected) != 0 {
		t.Fatalf("state.files.external_run_payload.selected = %#v, want []", payload["selected"])
	}
}

func assertCheckoutPlanSelectedAndCrossRepoPure(t *testing.T, checkout *reparentFixture) {
	t.Helper()
	original, err := os.ReadFile(internal.StackPath(checkout.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(internal.StackPath(checkout.FeaturePath), original, 0o644); err != nil {
			t.Fatal(err)
		}
	})
	checkoutJSON, _, checkoutExit := checkout.Run(checkout.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
	if checkoutExit != 0 {
		t.Fatalf("fresh checkout plan exit = %d", checkoutExit)
	}
	assertReparentExternalPayloadSelectedArray(t, checkoutJSON)

	checkoutStack := checkout.Stack()
	checkoutStack.Branches[0].Repo = filepath.Join(t.TempDir(), "secondary")
	if err := internal.SaveStack(checkout.FeaturePath, checkoutStack); err != nil {
		t.Fatal(err)
	}
	for _, fetchArgs := range [][]string{{"--fetch"}, {"--no-fetch"}} {
		args := append([]string{checkout.Feature, "pr2", "--onto", "main", "--plan", "--json"}, fetchArgs...)
		var stdout string
		var code int
		_, _, log := reparentPushCapture(t, func() {
			stdout, _, code = checkout.Run(args...)
		})
		if code != 0 {
			t.Fatalf("checkout cross-repo plan exit = %d", code)
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		assertReparentExternalPayloadSelectedArray(t, stdout)
		if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalCrossRepoClosure {
			t.Fatalf("checkout cross-repo plan = %+v", plan.Refusal)
		}
		for _, argv := range log.argv {
			joined := strings.Join(argv, " ")
			if !strings.Contains(joined, "rev-parse --show-toplevel") &&
				!strings.Contains(joined, "rev-parse --git-common-dir") {
				t.Fatalf("checkout cross-repo pure gate spawned planning/fetch Git: %v", log.argv)
			}
		}
	}
	if err := os.WriteFile(internal.StackPath(checkout.FeaturePath), original, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStackReparentValidate_LadderOrder walks §3.5 rule by rule, in the
// spec's own evaluation order, asserting the EXACT first failure. Several
// cells deliberately violate two rules at once, which is the only way to
// prove the order rather than the set (AC-009).
func TestStackReparentValidate_LadderOrder(t *testing.T) {
	_ = "asserts AC-004 AC-005 AC-006 AC-007 AC-008 AC-009 AC-010"
	token := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "rule 1 — json requires plan, even beside --abort",
			args: []string{"f", "--json", "--abort"},
			want: "--json requires --plan",
		},
		{
			name: "rule 2 — continue with abort",
			args: []string{"f", "--continue", "--abort"},
			want: "--continue and --abort are mutually exclusive",
		},
		{
			name: "rule 3 — plan with abort",
			args: []string{"f", "--plan", "--abort"},
			want: "--plan cannot be combined with --abort",
		},
		{
			name: "rule 4 — onto with continue",
			args: []string{"f", "--continue", "--onto", "main"},
			want: "--onto cannot be combined with --continue",
		},
		{
			name: "rule 4 — onto-kind with abort",
			args: []string{"f", "--abort", "--onto-kind", "ref"},
			want: "--onto-kind cannot be combined with --abort",
		},
		{
			name: "rule 4 — cutoff with continue",
			args: []string{"f", "--continue", "--cutoff", "main"},
			want: "--cutoff cannot be combined with --continue",
		},
		{
			name: "rule 4 — approve-plan with continue",
			args: []string{"f", "--continue", "--approve-plan", token},
			want: "--approve-plan cannot be combined with --continue",
		},
		{
			name: "rule 4 — per-entry limit with abort",
			args: []string{"f", "--abort", "--max-replay-per-entry", "3"},
			want: "--max-replay-per-entry cannot be combined with --abort",
		},
		{
			name: "rule 4 — total limit with continue",
			args: []string{"f", "--continue", "--max-replay-total", "3"},
			want: "--max-replay-total cannot be combined with --continue",
		},
		{
			name: "rule 4 — fetch with continue",
			args: []string{"f", "--continue", "--fetch"},
			want: "--fetch cannot be combined with --continue",
		},
		{
			name: "rule 4 — no-fetch with abort",
			args: []string{"f", "--abort", "--no-fetch"},
			want: "--no-fetch cannot be combined with --abort",
		},
		{
			name: "rule 5 — fetch with no-fetch",
			args: []string{"f", "e", "--onto", "main", "--fetch", "--no-fetch"},
			want: "--fetch and --no-fetch are mutually exclusive",
		},
		{
			name: "rule 6 — explicit false for --fetch",
			args: []string{"f", "e", "--onto", "main", "--fetch=false"},
			want: "--fetch does not take an explicit value; use --no-fetch to disable automatic fetch",
		},
		{
			name: "rule 6 — explicit true for --fetch",
			args: []string{"f", "e", "--onto", "main", "--fetch=true"},
			want: "--fetch does not take an explicit value; use --no-fetch to disable automatic fetch",
		},
		{
			name: "rule 6 — explicit false for --no-fetch",
			args: []string{"f", "e", "--onto", "main", "--no-fetch=false"},
			want: "--no-fetch does not take an explicit value; use --fetch to enable automatic fetch",
		},
		{
			name: "rule 6 — explicit true for --no-fetch",
			args: []string{"f", "e", "--onto", "main", "--no-fetch=true"},
			want: "--no-fetch does not take an explicit value; use --fetch to enable automatic fetch",
		},
		{
			name: "rule 6 — explicit fetch stays explicit when followed by bare fetch",
			args: []string{"f", "e", "--onto", "main", "--fetch=false", "--fetch"},
			want: "--fetch does not take an explicit value; use --no-fetch to disable automatic fetch",
		},
		{
			name: "rule 6 — bare fetch followed by explicit fetch is refused",
			args: []string{"f", "e", "--onto", "main", "--fetch", "--fetch=true"},
			want: "--fetch does not take an explicit value; use --no-fetch to disable automatic fetch",
		},
		{
			name: "rule 6 — explicit no-fetch stays explicit when followed by bare no-fetch",
			args: []string{"f", "e", "--onto", "main", "--no-fetch=false", "--no-fetch"},
			want: "--no-fetch does not take an explicit value; use --fetch to enable automatic fetch",
		},
		{
			name: "rule 6 — bare no-fetch followed by explicit no-fetch is refused",
			args: []string{"f", "e", "--onto", "main", "--no-fetch", "--no-fetch=true"},
			want: "--no-fetch does not take an explicit value; use --fetch to enable automatic fetch",
		},
		{
			name: "rule 6 — former sentinel literal is ordinary invalid argv",
			args: []string{"f", "e", "--onto", "main", "--fetch=__tws_implicit_bool__"},
			want: "invalid argument",
		},
		{
			name: "rule 6 — former no-fetch sentinel literal is ordinary invalid argv",
			args: []string{"f", "e", "--onto", "main", "--no-fetch=__tws_implicit_bool__"},
			want: "invalid argument",
		},
		{
			name: "rule 7 — onto-kind domain",
			args: []string{"f", "e", "--onto", "main", "--onto-kind", "branch"},
			want: "--onto-kind must be one of: auto, entry, ref",
		},
		{
			name: "rule 8 — whitespace-only --onto is empty",
			args: []string{"f", "e", "--onto", "   "},
			want: "--onto requires a destination",
		},
		{
			name: "rule 8 — the fresh route requires a destination at all",
			args: []string{"f", "e"},
			want: "--onto requires a destination",
		},
		{
			name: "rule 9 — empty --cutoff",
			args: []string{"f", "e", "--onto", "main", "--cutoff", ""},
			want: "--cutoff requires a ref",
		},
		{
			name: "rule 10 — negative per-entry limit",
			args: []string{"f", "e", "--onto", "main", "--max-replay-per-entry", "-1"},
			want: "--max-replay-per-entry must be zero or greater",
		},
		{
			name: "rule 10 — negative total limit",
			args: []string{"f", "e", "--onto", "main", "--max-replay-total", "-1"},
			want: "--max-replay-total must be zero or greater",
		},
		{
			name: "rule 11 — token shape",
			args: []string{"f", "e", "--onto", "main", "--approve-plan", "NOTHEX"},
			want: "--approve-plan requires a 64-character lowercase hex fingerprint",
		},
		{
			name: "rule 12 — token requires a limit",
			args: []string{"f", "e", "--onto", "main", "--approve-plan", token},
			want: "--approve-plan requires --max-replay-per-entry or --max-replay-total",
		},
		{
			name: "rule 13 — a fresh execution requires an approval",
			args: []string{"f", "e", "--onto", "main", "--max-replay-total", "5"},
			want: "tws stack reparent requires --approve-plan <fingerprint>; run with --plan first",
		},
		{
			name: "fully guarded fresh execution passes the ladder",
			args: []string{"f", "e", "--onto", "main", "--approve-plan", token, "--max-replay-total", "5"},
			want: "", // this combination PASSES §3.5; see the sibling assertion below
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := runReparentBare(t, tc.args...)
			if tc.want == "" {
				// §3.5 passed; the failure that follows is a workspace-level
				// one, which is exactly what proves the ladder let it through.
				if strings.Contains(stderr, "requires --approve-plan") || strings.Contains(stderr, "requires --max-replay") {
					t.Fatalf("a fully guarded fresh invocation must pass §3.5:\n%s", stderr)
				}
				return
			}
			if exit != 1 {
				t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("a §3.5 failure must write nothing to stdout, got %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
			if reparentMarkerRe.MatchString(stderr) {
				t.Fatalf("a §3.5 failure is a NATIVE error and must carry no reparent marker:\n%s", stderr)
			}
			if strings.Contains(stderr, "plan-guard: ") {
				t.Fatalf("a §3.5 failure must not carry a plan-guard marker either:\n%s", stderr)
			}
		})
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"f", "--onto", "main"}, "accepts 2 arg(s), received 1"},
		{[]string{"f", "e", "extra", "--onto", "main"}, "accepts 2 arg(s), received 3"},
		{[]string{"f", "e", "--continue"}, "accepts 1 arg(s), received 2"},
		{[]string{"f", "e", "--abort"}, "accepts 1 arg(s), received 2"},
	} {
		_, stderr, exit := runReparentBare(t, tc.args...)
		if exit != 1 || !strings.Contains(stderr, tc.want) {
			t.Fatalf("route-aware arity for %v = exit %d stderr %q", tc.args, exit, stderr)
		}
	}
	for _, verb := range []string{"--continue", "--abort"} {
		_, stderr, _ := runReparentBare(t, "f", verb)
		if strings.Contains(stderr, "requires --approve-plan") || strings.Contains(stderr, "requires --max-replay") {
			t.Fatalf("bare %s reached fresh-only rules 13/14: %s", verb, stderr)
		}
	}
	usageCmd := stackCmd()
	usageCmd.SetArgs([]string{"reparent", "f", "e", "--onto", "main"})
	usageCmd.SetOut(io.Discard)
	usageCmd.SetErr(io.Discard)
	if err := usageCmd.Execute(); err == nil {
		t.Fatal("a fresh execution with no approval must fail §3.5")
	}
	child, _, err := usageCmd.Find([]string{"reparent"})
	if err != nil {
		t.Fatal(err)
	}
	if child.SilenceUsage {
		t.Fatal("a §3.5 validation failure must preserve Cobra's usage block")
	}
	assertReparentMatrixBehavior(t, "T-038",
		"validation-arity-and-usage-contract",
	)
}

// TestStackReparentValidate_RecoveryVerbsSkipRules13And14 is §3.5's own
// explicit requirement: rules 13 and 14 are guarded by the fresh-execution
// predicate and are therefore UNREACHABLE on --continue and --abort. A bare
// recovery verb must pass the ladder without ever mentioning --approve-plan or
// either limit.
func TestStackReparentValidate_RecoveryVerbsSkipRules13And14(t *testing.T) {
	for _, verb := range []string{"--continue", "--abort"} {
		t.Run(verb, func(t *testing.T) {
			_, stderr, _ := runReparentBare(t, "f", verb)
			for _, forbidden := range []string{
				"requires --approve-plan",
				"requires --max-replay-per-entry or --max-replay-total",
			} {
				if strings.Contains(stderr, forbidden) {
					t.Fatalf("%s must never reach rules 13/14, got: %s", verb, stderr)
				}
			}
		})
	}
}

func TestReparentImplicitBoolSentinelCannotBeOSArgv(t *testing.T) {
	if reparentImplicitBool != "\x00" {
		t.Fatalf("implicit bool sentinel = %q, want one NUL byte", reparentImplicitBool)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gitPath, reparentImplicitBool)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("the implicit sentinel unexpectedly crossed the OS argv boundary")
	}
}

// TestStackReparentValidate_PlanRouteIsNotAFreshExecution proves the
// fresh-execution predicate excludes --plan: a bare plan needs neither an
// approval token nor a limit.
func TestStackReparentValidate_PlanRouteIsNotAFreshExecution(t *testing.T) {
	_, stderr, _ := runReparentBare(t, "f", "e", "--onto", "main", "--plan")
	for _, forbidden := range []string{"requires --approve-plan", "requires --max-replay"} {
		if strings.Contains(stderr, forbidden) {
			t.Fatalf("--plan is not a fresh EXECUTION and must skip rules 13/14, got: %s", stderr)
		}
	}
}

func TestStackReparentValidate_Rule14MissingLimitFailure(t *testing.T) {
	cmd := stackReparentCmd()
	if err := cmd.Flags().Set("onto", "main"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("approve-plan", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	// Rule 12 owns the ordinary CLI spelling. Clearing Changed models the
	// final fresh-route invariant directly so rule 14 itself is not left as
	// untested dead code.
	cmd.Flags().Lookup("approve-plan").Changed = false
	err := stackReparentValidate(cmd, []string{"f", "e"})
	if err == nil || err.Error() != "tws stack reparent requires --max-replay-per-entry or --max-replay-total" {
		t.Fatalf("rule 14 = %v", err)
	}
}

func TestStackReparentCLI_RefusalRenderingIsOneSanitizedLine(t *testing.T) {
	for _, source := range []string{"hook", "git", "validation"} {
		err := reparentRefusalLine(nil, &internal.ReparentRefusalError{
			Kind:   internal.ReparentRefusalValidationFailed,
			Detail: source + " failed\r\nline one\nline two",
		})
		got := err.Error()
		if strings.Count(got, "reparent:") != 1 || strings.ContainsAny(got, "\r\n") {
			t.Fatalf("%s refusal rendered as %q", source, got)
		}
	}
	generic := reparentRefusalLine(nil, errors.New("native git\nfailed\r\nwith detail"))
	if got := generic.Error(); got != "native git failed with detail" {
		t.Fatalf("generic reparent error rendered as %q", got)
	}
}

// TestStackReparentCLI_PlanFailuresAreDocumentsAndIdentityGatesArePure owns
// T-024 / AC-036 at the command boundary.
func TestStackReparentCLI_PlanFailuresAreDocumentsAndIdentityGatesArePure(t *testing.T) {
	_ = "asserts AC-036 AC-041 AC-043"
	assertReparentMatrixBehavior(t, "T-024",
		"identity-gates-and-plan-array-totality",
		"raw-operator-token-preservation",
		"external-metadata-cwd-plan",
	)
	f := newReparentCustomerExternal(t)
	freshJSON, _, freshExit := f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json")
	if freshExit != 0 {
		t.Fatalf("fresh external plan exit = %d", freshExit)
	}
	assertReparentExternalPayloadSelectedArray(t, freshJSON)
	if err := os.MkdirAll(filepath.Join(internal.TwsRoot(), ".tws-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{internal.TwsRoot(), f.FeaturePath} {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit := f.Run(
			f.Feature, "pr2", "--onto", "master", "--no-fetch",
			"--plan", "--json", "--max-replay-total", "50",
		)
		if restoreErr := os.Chdir(originalCWD); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if exit != 0 || stderr != "" {
			t.Fatalf("plan from %s = exit %d stderr %q", cwd, exit, stderr)
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatalf("plan from %s emitted no document: %v\n%s", cwd, err, stdout)
		}
		if plan.Target.Name != "pr2" {
			t.Fatalf("plan from %s target = %q", cwd, plan.Target.Name)
		}
	}
	workspaceStack := f.Stack()
	secondary := filepath.Join(t.TempDir(), "secondary")
	f.reparentGit(filepath.Dir(secondary), "clone", "-q", "--no-hardlinks", f.Repo, secondary)
	for _, entry := range workspaceStack.Branches {
		f.reparentGit(secondary, "branch", entry.GitBranch(), f.SHA(entry.GitBranch()))
	}
	secondaryStack := internal.Stack{Branches: append([]internal.StackEntry{}, workspaceStack.Branches...)}
	for i := range secondaryStack.Branches {
		secondaryStack.Branches[i].Repo = secondary
	}
	if err := internal.SaveStack(f.FeaturePath, secondaryStack); err != nil {
		t.Fatal(err)
	}
	cwdCfgPath := filepath.Join(f.Repo, ".tws", "config.yaml")
	cwdCfgBefore, cwdCfgErr := os.ReadFile(cwdCfgPath)
	cwdCfgPresent := cwdCfgErr == nil
	if cwdCfgErr != nil && !os.IsNotExist(cwdCfgErr) {
		t.Fatal(cwdCfgErr)
	}
	cwdCfg := internal.LoadConfigFile(cwdCfgPath)
	cwdCfg.TestCommand = "printf cwd-config"
	if err := internal.SaveConfigFile(cwdCfgPath, cwdCfg); err != nil {
		t.Fatal(err)
	}
	targetCfgPath := internal.RepoConfigPathFor(secondary)
	targetCfg := internal.LoadConfigFile(targetCfgPath)
	targetCfg.TestCommand = "printf target-config"
	if err := internal.SaveConfigFile(targetCfgPath, targetCfg); err != nil {
		t.Fatal(err)
	}
	targetJSON, targetErr, targetExit := f.Run(
		f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json",
	)
	if targetExit != 0 || targetErr != "" {
		t.Fatalf("secondary target config plan = exit %d stderr %q", targetExit, targetErr)
	}
	var targetPlan internal.ReparentPlan
	if err := json.Unmarshal([]byte(targetJSON), &targetPlan); err != nil {
		t.Fatal(err)
	}
	if targetPlan.Policy.Validation.Source != "config-repo" ||
		targetPlan.Policy.Validation.CommandDigest == nil ||
		*targetPlan.Policy.Validation.CommandDigest != internal.ValidationDigest("printf target-config") {
		t.Fatalf("secondary target validation identity = %+v", targetPlan.Policy.Validation)
	}
	if cwdCfgPresent {
		if err := os.WriteFile(cwdCfgPath, cwdCfgBefore, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Remove(cwdCfgPath); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(f.FeaturePath, workspaceStack); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.WriteFile(internal.StackPath(f.FeaturePath), original, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	assertJSON := func(args []string, want internal.ReparentRefusalKind, zeroGit bool) internal.ReparentPlan {
		t.Helper()
		var stdout, stderr string
		var exit int
		_, _, log := reparentPushCapture(t, func() {
			stdout, stderr, exit = f.Run(args...)
		})
		if exit != 0 {
			t.Fatalf("plan failure must exit 0: args=%v exit=%d stderr=%q", args, exit, stderr)
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatalf("plan failure emitted no JSON document: %v\n%s", err, stdout)
		}
		assertReparentExternalPayloadSelectedArray(t, stdout)
		if plan.Summary.Plannability != internal.ReparentPlannabilityUnavailable ||
			plan.Runnable || plan.Refusal.Kind == nil || *plan.Refusal.Kind != want {
			t.Fatalf("unavailable plan for %v = %+v", args, plan)
		}
		if plan.Policy.OIDWidth != nil {
			t.Fatalf("unavailable plan oid_width = %v, want null", plan.Policy.OIDWidth)
		}
		if plan.Target.Name != args[1] {
			t.Fatalf("partial target identity = %q, want %q", plan.Target.Name, args[1])
		}
		if zeroGit {
			var planningArgv [][]string
			for _, argv := range log.argv {
				joined := strings.Join(argv, " ")
				if strings.Contains(joined, "rev-parse --show-toplevel") ||
					strings.Contains(joined, "rev-parse --git-common-dir") {
					continue
				}
				planningArgv = append(planningArgv, argv)
			}
			if len(planningArgv) != 0 {
				t.Fatalf("pure plan gate %v spawned planning/fetch Git: %v", args, planningArgv)
			}
		}
		return plan
	}
	assertUnavailableHuman := func(label, human string, exit int) {
		t.Helper()
		for _, want := range []string{
			"plannability=unavailable",
			"oid-width=unknown",
			"pinned=unavailable",
			"stack.yaml after:  unavailable",
			"run id: unavailable, computation path: unavailable",
		} {
			if exit != 0 || !strings.Contains(human, want) {
				t.Fatalf("%s unavailable human plan = exit %d, missing %q\n%s", label, exit, want, human)
			}
		}
		if strings.Contains(human, internal.ReparentDeferredRendering) {
			t.Fatalf("%s unavailable human plan must not advertise replay-computable facts:\n%s", label, human)
		}
	}

	for _, fetchArgs := range [][]string{{}, {"--no-fetch"}} {
		args := append([]string{f.Feature, "missing", "--onto", "master", "--plan", "--json"}, fetchArgs...)
		assertJSON(args, internal.ReparentRefusalTargetUnknown, true)
	}
	unknownHuman, _, unknownExit := f.Run(f.Feature, "missing", "--onto", "master", "--plan", "--no-fetch")
	assertUnavailableHuman("unknown-target", unknownHuman, unknownExit)

	if err := os.WriteFile(internal.StackPath(f.FeaturePath), []byte("branches: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, fetchArgs := range [][]string{{}, {"--no-fetch"}} {
		args := append([]string{f.Feature, "pr2", "--onto", "master", "--plan", "--json"}, fetchArgs...)
		assertJSON(args, internal.ReparentRefusalPlanUnavailable, true)
	}
	malformedHuman, _, malformedExit := f.Run(f.Feature, "pr2", "--onto", "master", "--plan", "--no-fetch")
	assertUnavailableHuman("malformed-stack", malformedHuman, malformedExit)
	restore()

	if err := os.Remove(internal.StackPath(f.FeaturePath)); err != nil {
		t.Fatal(err)
	}
	human, _, exit := f.Run(f.Feature, "pr2", "--onto", "master", "--plan", "--no-fetch")
	assertUnavailableHuman("unreadable-stack", human, exit)
	if !strings.Contains(human, string(internal.ReparentRefusalPlanUnavailable)) {
		t.Fatalf("unreadable-stack human plan = exit %d\n%s", exit, human)
	}
	restore()

	stack := f.Stack()
	dupName := stack.Branches[0]
	dupName.Branch = "duplicate-name-branch"
	stack.Branches = append(stack.Branches, dupName)
	if err := internal.SaveStack(f.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	for _, fetchArgs := range [][]string{{}, {"--no-fetch"}} {
		args := append([]string{f.Feature, "pr2", "--onto", "master", "--plan", "--json"}, fetchArgs...)
		assertJSON(args, internal.ReparentRefusalDuplicateEntryName, true)
	}
	restore()

	stack = f.Stack()
	dupBranch := stack.Branches[0]
	dupBranch.Name = "duplicate-git-branch-entry"
	stack.Branches = append(stack.Branches, dupBranch)
	if err := internal.SaveStack(f.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	for _, fetchArgs := range [][]string{{}, {"--no-fetch"}} {
		args := append([]string{f.Feature, "pr2", "--onto", "master", "--plan", "--json"}, fetchArgs...)
		assertJSON(args, internal.ReparentRefusalDuplicateGitBranch, true)
	}
	restore()

	stack = f.Stack()
	for i := range stack.Branches {
		if stack.Branches[i].Name == "pr2" {
			stack.Branches[i].Repo = filepath.Join(t.TempDir(), "missing-repo")
		}
	}
	if err := internal.SaveStack(f.FeaturePath, stack); err != nil {
		t.Fatal(err)
	}
	for _, fetchArgs := range [][]string{{}, {"--no-fetch"}} {
		args := append([]string{f.Feature, "pr2", "--onto", "master", "--plan", "--json"}, fetchArgs...)
		assertJSON(args, internal.ReparentRefusalRepoUnavailable, false)
	}
	restore()

	rawOnto := " master "
	rawOntoJSON, _, rawOntoExit := f.Run(
		f.Feature, "pr2", "--onto", rawOnto, "--no-fetch", "--plan", "--json",
	)
	if rawOntoExit != 0 {
		t.Fatalf("raw destination plan exit = %d", rawOntoExit)
	}
	var rawOntoPlan internal.ReparentPlan
	if err := json.Unmarshal([]byte(rawOntoJSON), &rawOntoPlan); err != nil {
		t.Fatal(err)
	}
	if rawOntoPlan.Target.NewParent.RequestedToken == nil ||
		*rawOntoPlan.Target.NewParent.RequestedToken != rawOnto ||
		rawOntoPlan.Refusal.Kind == nil ||
		*rawOntoPlan.Refusal.Kind != internal.ReparentRefusalDestinationUnresolvable {
		t.Fatalf("raw destination token was normalized: %+v", rawOntoPlan.Target.NewParent)
	}

	rawCutoff := " HEAD "
	rawCutoffJSON, _, rawCutoffExit := f.Run(
		f.Feature, "pr2", "--onto", "master", "--cutoff", rawCutoff,
		"--no-fetch", "--plan", "--json",
	)
	if rawCutoffExit != 0 {
		t.Fatalf("raw cutoff plan exit = %d", rawCutoffExit)
	}
	var rawCutoffPlan internal.ReparentPlan
	if err := json.Unmarshal([]byte(rawCutoffJSON), &rawCutoffPlan); err != nil {
		t.Fatal(err)
	}
	if rawCutoffPlan.Target.Cutoff.SuppliedToken == nil ||
		*rawCutoffPlan.Target.Cutoff.SuppliedToken != rawCutoff ||
		rawCutoffPlan.Refusal.Kind == nil ||
		*rawCutoffPlan.Refusal.Kind != internal.ReparentRefusalCutoffUnresolvable {
		t.Fatalf("raw cutoff token was normalized: %+v", rawCutoffPlan.Target.Cutoff)
	}

	t.Setenv("PATH", t.TempDir())
	missingGitCWDs := []struct {
		name string
		path string
	}{
		{name: "repository-root", path: f.Repo},
		{name: "external-workspace-root", path: internal.TwsRoot()},
		{name: "feature-directory", path: f.FeaturePath},
	}
	for _, tc := range missingGitCWDs {
		if err := os.Chdir(tc.path); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit := f.Run(
			f.Feature, "pr2", "--onto", "master", "--plan", "--json", "--no-fetch",
		)
		if exit != 0 || stderr != "" {
			t.Fatalf("missing-Git plan from %s = exit %d stderr %q", tc.name, exit, stderr)
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatalf("missing-Git plan from %s emitted no document: %v\n%s", tc.name, err, stdout)
		}
		wantRepo, _ := filepath.EvalSymlinks(f.Repo)
		gotRepo, _ := filepath.EvalSymlinks(plan.Workspace.RepoRoot)
		if gotRepo != wantRepo {
			t.Fatalf("missing-Git plan from %s fabricated repo_root %q, want proven %q", tc.name, gotRepo, wantRepo)
		}
	}
	if err := os.Chdir(originalCWD); err != nil {
		t.Fatal(err)
	}
	for _, fetchArgs := range [][]string{nil, {"--no-fetch"}} {
		args := append([]string{f.Feature, "pr2", "--onto", "master", "--plan", "--json"}, fetchArgs...)
		missingGitJSON, missingGitErr, missingGitExit := f.Run(args...)
		if missingGitExit != 0 || missingGitErr != "" {
			t.Fatalf("missing-Git plan %v = exit %d stderr %q", fetchArgs, missingGitExit, missingGitErr)
		}
		var missingGitPlan internal.ReparentPlan
		if err := json.Unmarshal([]byte(missingGitJSON), &missingGitPlan); err != nil {
			t.Fatalf("missing-Git plan emitted no document: %v\n%s", err, missingGitJSON)
		}
		if missingGitPlan.Summary.Plannability != internal.ReparentPlannabilityUnavailable ||
			missingGitPlan.Refusal.Kind == nil ||
			*missingGitPlan.Refusal.Kind != internal.ReparentRefusalPlanUnavailable {
			t.Fatalf("missing-Git plan = %+v", missingGitPlan)
		}
		if missingGitPlan.Policy.OIDWidth != nil {
			t.Fatalf("missing-Git oid_width = %v, want null", missingGitPlan.Policy.OIDWidth)
		}
		if len(fetchArgs) == 0 {
			if missingGitPlan.Fetch.PolicySource != "route-default" ||
				missingGitPlan.Fetch.SuppressionCause == nil ||
				*missingGitPlan.Fetch.SuppressionCause != "not-refreshed-no-plan-subject" ||
				missingGitPlan.Freshness != "not-refreshed-no-plan-subject" {
				t.Fatalf("default-fetch unavailable projection = %+v freshness=%q",
					missingGitPlan.Fetch, missingGitPlan.Freshness)
			}
		} else if missingGitPlan.Fetch.PolicySource != "flag" ||
			missingGitPlan.Fetch.SuppressionCause != nil ||
			missingGitPlan.Fetch.Outcome != "skipped" ||
			missingGitPlan.Freshness != "local-only" {
			t.Fatalf("no-fetch unavailable projection = %+v freshness=%q",
				missingGitPlan.Fetch, missingGitPlan.Freshness)
		}
		assertReparentExternalPayloadSelectedArray(t, missingGitJSON)
	}
	missingGitHuman, missingGitHumanErr, missingGitHumanExit := f.Run(
		f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan",
	)
	assertUnavailableHuman("missing-Git", missingGitHuman, missingGitHumanExit)
	if missingGitHumanErr != "" {
		t.Fatalf("missing-Git human plan = exit %d stderr %q\n%s",
			missingGitHumanExit, missingGitHumanErr, missingGitHuman)
	}
	if stdout, stderr, exit := f.Run(
		f.Feature, "pr2", "--onto", "master", "--no-fetch",
		"--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64),
	); exit != 1 || stdout != "" || !strings.Contains(stderr, `required tool "git" not found in PATH`) {
		t.Fatalf("missing-Git execution = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	outside := filepath.Join(filepath.Dir(f.Repo), "outside-workspace")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	previousRoot := os.Getenv("TWS_ROOT")
	if err := os.Unsetenv("TWS_ROOT"); err != nil {
		t.Fatal(err)
	}
	outsideStdout, outsideStderr, outsideExit := runTWSExecute(
		t, "stack", "reparent", f.Feature, "pr2", "--onto", "master", "--plan", "--json", "--no-fetch",
	)
	if err := os.Setenv("TWS_ROOT", previousRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(originalCWD); err != nil {
		t.Fatal(err)
	}
	if outsideExit != 1 || outsideStdout != "" ||
		!strings.Contains(outsideStderr, "filesystem-proven git repository or tws workspace") {
		t.Fatalf("missing-Git outside workspace = exit %d stdout %q stderr %q",
			outsideExit, outsideStdout, outsideStderr)
	}
	assertReparentMatrixBehavior(t, "T-031",
		"unavailable-plan-cli-contract",
		"unavailable-null-oid-and-human-render",
	)
}

// TestStackReparentArgs_RouteAwareArity covers §3.2's whole table, including
// the fact that route detection reads the flag VALUE (so `--continue=false`
// stays a fresh route needing two positionals).
func TestStackReparentArgs_RouteAwareArity(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"fresh route wants two", []string{"f", "--onto", "main"}, "accepts 2 arg(s), received 1"},
		{"fresh route rejects three", []string{"f", "e", "x", "--onto", "main"}, "accepts 2 arg(s), received 3"},
		{"continue wants one", []string{"f", "e", "--continue"}, "accepts 1 arg(s), received 2"},
		{"abort wants one", []string{"f", "e", "--abort"}, "accepts 1 arg(s), received 2"},
		{"continue=false is a fresh route", []string{"f", "--continue=false", "--onto", "main"}, "accepts 2 arg(s), received 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, exit := runReparentBare(t, tc.args...)
			if exit != 1 {
				t.Fatalf("exit = %d, want 1 (%s)", exit, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
		})
	}
}

// TestStackReparentArgs_ArityBeatsFlagRules is §3.5's own preamble: the
// Args phase runs before RunE, so an invocation violating BOTH arity and a
// flag rule reports the arity error.
func TestStackReparentArgs_ArityBeatsFlagRules(t *testing.T) {
	_, stderr, exit := runReparentBare(t, "--json")
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if strings.Contains(stderr, "--json requires --plan") {
		t.Fatalf("arity runs first; got the flag rule instead:\n%s", stderr)
	}
	if !strings.Contains(stderr, "accepts 2 arg(s), received 0") {
		t.Fatalf("stderr = %q, want the arity message", stderr)
	}
}

// TestStackReparentValidate_UsageBlockBoundary is AC-010: a §3.5 failure keeps
// Cobra's usage block; everything after SilenceUsage = true prints its message
// alone. The boundary is asserted through the command object directly, because
// syncExecute silences usage globally by design.
func TestStackReparentValidate_UsageBlockBoundary(t *testing.T) {
	// A §3.5 failure leaves SilenceUsage false.
	cmd := stackCmd()
	cmd.SetArgs([]string{"reparent", "f", "e", "--onto", "main"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("a fresh execution with no approval must fail §3.5")
	}
	child, _, findErr := cmd.Find([]string{"reparent"})
	if findErr != nil {
		t.Fatal(findErr)
	}
	if child.SilenceUsage {
		t.Fatal("a §3.5 failure must keep the usage block: SilenceUsage was already set")
	}
}

func TestStackReparent_LegacyStackAndHelpAreFrozen(t *testing.T) {
	_ = "asserts AC-002 AC-003 AC-008 AC-099"
	sourceDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_ = newReparentCustomerExternal(t)
	assertStackReparentLegacyCollision(t, sourceDir)
	assertStackReparentHelpGoldens(t, sourceDir)
	assertReparentMatrixBehavior(t, "T-085",
		"legacy-stack-and-help-goldens",
	)
}

// assertStackReparentLegacyCollision is T-085's collision half: a feature
// literally named `reparent` stays reachable through `tws stack -- reparent`,
// the missing-argument diagnostic names that escape, and `stackCmd`'s own
// completion suppresses the literal.
func assertStackReparentLegacyCollision(t *testing.T, sourceDir string) {
	t.Helper()
	// The feature is created under the workspace's OWN metadata root, which is
	// what ListFeaturesE and RequireFeaturePath both resolve against — not the
	// bare TWS_ROOT env value, which an external workspace may legitimately
	// override.
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	featurePath := ws.FeaturePath("reparent")
	if err := os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "only", Base: "master"},
	}}); err != nil {
		t.Fatal(err)
	}

	t.Run("missing-argument diagnostic names the escape", func(t *testing.T) {
		_, stderr, exit := runReparentBare(t)
		if exit != 1 {
			t.Fatalf("exit = %d, want 1", exit)
		}
		want := `accepts 2 arg(s), received 0: a feature named "reparent" exists; run "tws stack -- reparent" for its legacy dependency tree`
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
	})

	t.Run("tws stack -- reparent still prints the legacy tree", func(t *testing.T) {
		stdout, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(stackCmd, "--", "reparent"); code != 0 {
				t.Errorf("exit = %d", code)
			}
		})
		want, err := os.ReadFile(filepath.Join(sourceDir, "testdata/reparent/legacy_stack_feature.txt"))
		if err != nil {
			t.Fatalf("%v\ncandidate golden:\n%s", err, stdout)
		}
		if stdout != string(want) || stderr != "" {
			t.Fatalf("legacy tws stack bytes changed:\n--- stdout ---\n%s\n--- stderr ---\n%s\n--- want ---\n%s", stdout, stderr, want)
		}
	})

	t.Run("completion suppresses the literal", func(t *testing.T) {
		cmd := stackCmd()
		names, _ := cmd.ValidArgsFunction(cmd, nil, "")
		for _, name := range names {
			if name == "reparent" || name == "status" {
				t.Fatalf("stackCmd completion must suppress %q; got %v", name, names)
			}
		}
	})
}

// assertStackReparentHelpGoldens is T-085's snapshot half. `tws stack --help`
// gains exactly one line and `tws stack reparent --help` is pinned whole; the
// negative flag assertions of AC-008 are read straight off the latter.
func assertStackReparentHelpGoldens(t *testing.T, sourceDir string) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		args   []string
		golden string
	}{
		{"stack", []string{"stack", "--help"}, "testdata/reparent/stack_help.txt"},
		{"reparent", []string{"stack", "reparent", "--help"}, "testdata/reparent/reparent_help.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Captured through production Execute(), so the golden carries the
			// real `tws stack …` command paths an operator sees rather than a
			// detached subtree's own bare names.
			stdout, stderr, exit := runTWSExecute(t, tc.args...)
			if exit != 0 {
				t.Fatalf("--help must exit 0, got %d (stderr=%q)", exit, stderr)
			}
			if stderr != "" {
				t.Fatalf("--help must write nothing to stderr, got %q", stderr)
			}
			want, err := os.ReadFile(filepath.Join(sourceDir, tc.golden))
			if err != nil {
				t.Fatalf("reading %s: %v", tc.golden, err)
			}
			if stdout != string(want) {
				t.Fatalf("%s drifted from %s:\n--- got ---\n%s\n--- want ---\n%s", tc.name, tc.golden, stdout, string(want))
			}
		})
	}
}

// TestStackReparent_UndeclaredFlagsAreRejected is AC-008's negative half: v1
// declares a CLOSED set, and every flag the spec names as undeclared must be
// an unknown flag, not a silently accepted one.
func TestStackReparent_UndeclaredFlagsAreRejected(t *testing.T) {
	for _, flag := range []string{
		"--yes", "--dry-run", "--push", "--test", "--root",
		"--unparent", "--only", "--from", "--full", "--local-only", "--remote",
	} {
		t.Run(flag, func(t *testing.T) {
			_, stderr, exit := runReparentBare(t, "f", "e", "--onto", "main", flag)
			if exit != 1 {
				t.Fatalf("exit = %d, want 1", exit)
			}
			if !strings.Contains(stderr, "unknown flag") {
				t.Fatalf("%s must be an unknown flag, got: %s", flag, stderr)
			}
		})
	}

	golden, err := os.ReadFile("testdata/reparent/reparent_help.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--yes", "--dry-run", "--push", "--test=", "--root", "--unparent", "--only", "--from", "--full", "--local-only", "--remote"} {
		if strings.Contains(string(golden), flag) {
			t.Fatalf("the help golden must not advertise %q", flag)
		}
	}
	for _, flag := range []string{
		"--abort", "--approve-plan", "--continue", "--cutoff", "--fetch",
		"--json", "--max-replay-per-entry", "--max-replay-total",
		"--no-fetch", "--onto", "--onto-kind", "--plan", "--verbose",
	} {
		if !strings.Contains(string(golden), flag) {
			t.Fatalf("the help golden must advertise %q", flag)
		}
	}
}

// TestStackReparent_OntoKindCompletion pins the fixed completion domain.
func TestStackReparent_OntoKindCompletion(t *testing.T) {
	got, _ := reparentOntoKindCompletionForTest(t)
	want := []string{"auto", "entry", "ref"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("--onto-kind completion = %v, want %v", got, want)
	}
}

// reparentOntoKindCompletionForTest reads the registered completion function
// through the command tree, so the test asserts what a shell would actually
// receive rather than a package-local slice.
func reparentOntoKindCompletionForTest(t *testing.T) ([]string, bool) {
	t.Helper()
	cmd := stackCmd()
	child, _, err := cmd.Find([]string{"reparent"})
	if err != nil {
		t.Fatal(err)
	}
	fn, ok := child.GetFlagCompletionFunc("onto-kind")
	if !ok || fn == nil {
		t.Fatal("--onto-kind must register a completion function")
	}
	values, _ := fn(child, nil, "")
	return values, true
}

// ---------------------------------------------------------------------------
// The human plan golden (§7.14, AC-013)
// ---------------------------------------------------------------------------

// reparentNormalizeHumanPlan replaces every run-varying token with a stable
// placeholder, so a golden pins the document's SHAPE — section order, key
// order, the deferred literal, every label — without pinning a SHA, a
// temporary path or a fingerprint that legitimately changes per run.
func reparentNormalizeHumanPlan(t *testing.T, f *reparentFixture, human string) string {
	t.Helper()
	out := human
	// Longest-first, so a 40-hex SHA is never partially replaced by the
	// abbreviated form of another token.
	out = regexp.MustCompile(`\b[0-9a-f]{64}\b`).ReplaceAllString(out, "<fingerprint>")
	out = regexp.MustCompile(`\b[0-9a-f]{40}\b`).ReplaceAllString(out, "<sha>")
	out = regexp.MustCompile(`\b[0-9a-f]{12}\b`).ReplaceAllString(out, "<short>")
	out = regexp.MustCompile(`\b[0-9a-f]{7,11}\b`).ReplaceAllString(out, "<short>")
	// macOS resolves TMPDIR through /private, so the same directory can appear
	// with and without that prefix in one document. Both spellings are
	// normalized, longest first, so the golden is portable to a Linux CI leg.
	out = strings.ReplaceAll(out, "/private"+f.FeaturePath, "<feature-path>")
	out = strings.ReplaceAll(out, f.FeaturePath, "<feature-path>")
	out = strings.ReplaceAll(out, "/private"+f.Repo, "<repo>")
	out = strings.ReplaceAll(out, f.Repo, "<repo>")
	out = regexp.MustCompile(`/[^\s:]*/workspace/[^\s,]*`).ReplaceAllString(out, "<path>")
	out = regexp.MustCompile(`/(private/)?var/folders/[^\s,]*`).ReplaceAllString(out, "<path>")
	out = regexp.MustCompile(`/tmp/[^\s,]*`).ReplaceAllString(out, "<path>")
	out = regexp.MustCompile(`(?m)^(\s*workspace: mode=\S+ )stable-id=\S+`).ReplaceAllString(out, "${1}stable-id=<id>")
	return out
}

// TestStackReparent_HumanPlanGolden pins the human document's whole shape for
// the customer topology, with run-varying tokens normalized. It is the
// strongest available form of §7.14's "every section, in this order" clause:
// exact identity of the whole rendered stream rather than a substring check.
func TestStackReparent_HumanPlanGolden(t *testing.T) {
	// The fixture chdirs into its repository, so the golden path is resolved
	// to an absolute one BEFORE it is built.
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(pkgDir, "testdata", "reparent", "plan_human.txt")

	f := newReparentCustomerExternal(t)
	human, prose := f.Plan(f.Feature, "pr2", "--onto", "master", "--no-fetch",
		"--max-replay-per-entry", "10", "--max-replay-total", "50")
	if strings.Contains(human, "Fetching") {
		t.Fatalf("operational prose belongs on stderr:\n%s", prose)
	}
	got := reparentNormalizeHumanPlan(t, f, human)

	if os.Getenv("TWS_REGEN_REPARENT_GOLDENS") != "" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading %s: %v", golden, err)
	}
	if got != string(want) {
		t.Fatalf("the human plan drifted from %s:\n--- got ---\n%s\n--- want ---\n%s", golden, got, string(want))
	}
}

// TestStackStatusHelp_StaysFrozen is §14.2 item 1. `tws stack status --help`
// is a frozen surface: this feature adds a sibling command and a projection
// key, never a word to that command's own help. The assertion is written
// against the rendered stream rather than a new golden, so it cannot drift
// into a second snapshot that has to be maintained alongside the shipped one.
func TestStackStatusHelp_StaysFrozen(t *testing.T) {
	stdout, stderr, exit := runTWSExecute(t, "stack", "status", "--help")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit = %d, stderr = %q", exit, stderr)
	}
	for _, forbidden := range []string{"reparent", "--onto", "--approve-plan", "--cutoff"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("tws stack status --help must not mention %q:\n%s", forbidden, stdout)
		}
	}
	// Its flag set is exactly the shipped one. Only the rendered `Flags:`
	// block is counted: the Long text legitimately begins a paragraph with
	// "--json prints one versioned document ...", which is prose, not a flag.
	idx := strings.LastIndex(stdout, "\nFlags:\n")
	if idx < 0 {
		t.Fatalf("tws stack status --help must render a Flags: block:\n%s", stdout)
	}
	flagLines := 0
	for _, line := range strings.Split(stdout[idx:], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "-h,") {
			flagLines++
		}
	}
	if flagLines != 2 {
		t.Fatalf("tws stack status must still declare exactly --json and --help, found %d flag lines:\n%s", flagLines, stdout)
	}
}

// TestReparentRoute_PlanContinueIsReadOnly is the route-order assertion of
// §3.2 and §3.5, and AC-004/AC-051. `--plan --continue` is an ADMITTED
// combination — it renders the §7.12a resume document — so the dispatch must
// match --plan above --continue. Matching --continue first turns a documented
// preview into an executed resume.
func TestReparentRoute_PlanContinueIsReadOnly(t *testing.T) {
	for _, mode := range []string{"external", "checkout"} {
		t.Run(mode, func(t *testing.T) {
			t.Cleanup(func() { internal.ReparentStepHook = nil })
			var f *reparentFixture
			if mode == "external" {
				f = newReparentCustomerExternal(t)
			} else {
				f = newReparentCustomerCheckout(t)
			}
			human, _ := f.Plan(f.Feature, "pr2", "--onto", map[string]string{"external": "master", "checkout": "main"}[mode],
				"--no-fetch", "--max-replay-total", "10")
			token := reparentFingerprint(t, human)
			crash := errors.New("pause detailed run for continue plan")
			internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
				if step == "pins-written" {
					return crash
				}
				return nil
			}
			_, _, execExit := f.Run(f.Feature, "pr2", "--onto", map[string]string{"external": "master", "checkout": "main"}[mode],
				"--no-fetch", "--max-replay-total", "10", "--approve-plan", token)
			internal.ReparentStepHook = nil
			if execExit != 1 {
				t.Fatalf("the fixture must pause a real run, exit=%d", execExit)
			}
			load := internal.LoadReparentState(f.Loc())
			if load.State == nil {
				t.Fatalf("the paused run must have authoritative state: %+v", load)
			}
			st := load.State
			if st.Rows[len(st.Rows)-1].RevalidationDigest == "" {
				t.Fatal("paused continuation row is missing its approved revalidation digest")
			}
			if st.Stage != internal.ReparentStagePinningPreimages {
				t.Fatalf("paused continuation stage = %q, want pinning-preimages", st.Stage)
			}
			if st.Rows[len(st.Rows)-1].Stage != internal.ReparentRowPending {
				t.Fatalf("paused row stage = %q, want pending", st.Rows[len(st.Rows)-1].Stage)
			}

			refsBefore := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/")
			stackBefore, err := os.ReadFile(internal.StackPath(f.FeaturePath))
			if err != nil {
				t.Fatal(err)
			}
			stateBefore, err := os.ReadFile(internal.ReparentStatePath(f.Loc()))
			if err != nil {
				t.Fatal(err)
			}

			stdout, stderr, exit := f.Run(f.Feature, "--plan", "--json", "--continue")
			if exit != 0 {
				t.Fatalf("--plan --continue exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
			}
			var plan internal.ReparentPlan
			if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Route != internal.ReparentRouteContinue {
				t.Fatalf("route = %q, want continue", plan.Route)
			}
			if plan.Target.Name != st.TargetName || plan.Summary.Rows != len(st.Rows) {
				t.Fatalf("continue plan did not project the persisted target/closure: target=%q rows=%d state=%+v",
					plan.Target.Name, plan.Summary.Rows, st.Rows)
			}
			if plan.Policy.LimitsOrigin != internal.ReparentLimitsOriginPersisted ||
				plan.Policy.LimitsSupplied != (st.MaxReplayPerEntry != nil || st.MaxReplayTotal != nil) {
				t.Fatalf("persisted limits were not projected: %+v", plan.Policy)
			}
			if plan.Policy.Validation.Source != st.ValidationSource ||
				(plan.Policy.Validation.CommandDigest != nil && *plan.Policy.Validation.CommandDigest != st.ValidationCommandDigest) {
				t.Fatalf("persisted validation identity was not projected: %+v vs %+v", plan.Policy.Validation, st)
			}
			if plan.State.ApprovedFingerprint == nil || *plan.State.ApprovedFingerprint != st.ApprovedFingerprint {
				t.Fatalf("approved fingerprint audit evidence = %v, want %q", plan.State.ApprovedFingerprint, st.ApprovedFingerprint)
			}
			if plan.Approval.Scope != internal.ReparentApprovalScopeResume || plan.Approval.Fingerprint != nil ||
				plan.Approval.Supplied || plan.Approval.Accepted != nil {
				t.Fatalf("resume approval = %+v", plan.Approval)
			}
			if !plan.Runnable || !plan.Approval.Usable {
				t.Fatalf("a healthy paused run must be admissible under §7.12a: %+v", plan.Blockers)
			}

			if after := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/"); after != refsBefore {
				t.Fatalf("a preview moved a ref:\n--- before ---\n%s\n--- after ---\n%s", refsBefore, after)
			}
			stackAfter, err := os.ReadFile(internal.StackPath(f.FeaturePath))
			if err != nil {
				t.Fatal(err)
			}
			if string(stackAfter) != string(stackBefore) {
				t.Fatal("a preview rewrote stack.yaml")
			}
			stateAfter, err := os.ReadFile(internal.ReparentStatePath(f.Loc()))
			if err != nil {
				t.Fatal(err)
			}
			if string(stateAfter) != string(stateBefore) {
				t.Fatalf("a preview mutated the run state:\n--- before ---\n%s\n--- after ---\n%s",
					stateBefore, stateAfter)
			}

			assertBlocked := func(want internal.ReparentRefusalKind) {
				t.Helper()
				doc, _, code := f.Run(f.Feature, "--plan", "--json", "--continue")
				if code != 0 {
					t.Fatalf("blocked continue plan exit = %d", code)
				}
				var blocked internal.ReparentPlan
				if err := json.Unmarshal([]byte(doc), &blocked); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, blocker := range blocked.Blockers {
					if blocker.Kind == want {
						found = true
					}
				}
				if !found || blocked.Runnable || blocked.Approval.Usable {
					t.Fatalf("continue plan did not mirror execution refusal %s: %+v", want, blocked)
				}
			}

			if mode == "external" {
				sessionToken, err := internal.CreateDirectSession(f.FeaturePath, internal.DirectSessionRecord{
					Feature: f.Feature, Name: "pr2", GitBranch: f.Entry("pr2").GitBranch(),
					Path: internal.WorktreePath(f.Feature, "pr2"), Stage: internal.DirectStageStarting,
				})
				if err != nil {
					t.Fatal(err)
				}
				assertBlocked(internal.ReparentRefusalSessionLive)
				_, sessionErr, sessionExit := f.Run(f.Feature, "--continue")
				if sessionExit != 1 || !strings.Contains(sessionErr, string(internal.ReparentRefusalSessionLive)) {
					t.Fatalf("plan/execution session assessment differs: exit=%d stderr=%q", sessionExit, sessionErr)
				}
				if err := internal.RemoveOwnedDirectSession(f.FeaturePath,
					internal.DirectSessionBranchID(f.Feature, "pr2"), sessionToken); err != nil {
					t.Fatal(err)
				}
			}

			row := st.Rows[len(st.Rows)-1]
			ref := "refs/heads/" + row.GitBranch
			f.reparentGit(f.Repo, "update-ref", ref, f.SHA(map[string]string{"external": "master", "checkout": "main"}[mode]))
			wantCutoffKind := internal.ReparentRefusalCutoffNotAncestor
			if mode == "external" {
				wantCutoffKind = internal.ReparentRefusalHolderUnsafe
			}
			assertBlocked(wantCutoffKind)
			_, cutoffErr, cutoffExit := f.Run(f.Feature, "--continue")
			if cutoffExit != 1 || !strings.Contains(cutoffErr, string(wantCutoffKind)) {
				t.Fatalf("plan/execution cutoff assessment differs: exit=%d stderr=%q", cutoffExit, cutoffErr)
			}
			f.reparentGit(f.Repo, "update-ref", ref, row.PreimageSHA)

			f.reparentGit(f.Repo, "symbolic-ref", ref, "refs/heads/"+map[string]string{"external": "master", "checkout": "main"}[mode])
			assertBlocked(internal.ReparentRefusalProbeFailed)
			_, symbolicErr, symbolicExit := f.Run(f.Feature, "--continue")
			if symbolicExit != 1 || !strings.Contains(symbolicErr, string(internal.ReparentRefusalProbeFailed)) {
				t.Fatalf("execution and continuation plan disagree on a symbolic affected ref: exit=%d stderr=%q", symbolicExit, symbolicErr)
			}
			f.reparentGit(f.Repo, "update-ref", "--no-deref", ref, row.PreimageSHA)

			drifted := append(append([]byte{}, stackBefore...), []byte("# drift\n")...)
			if err := os.WriteFile(internal.StackPath(f.FeaturePath), drifted, 0o644); err != nil {
				t.Fatal(err)
			}
			assertBlocked(internal.ReparentRefusalMetadataDrift)
			if err := os.WriteFile(internal.StackPath(f.FeaturePath), stackBefore, 0o644); err != nil {
				t.Fatal(err)
			}

			live := exec.Command("sleep", "30")
			if err := live.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = live.Process.Kill()
				_ = live.Wait()
			})
			st.OwnerPID = live.Process.Pid
			if err := internal.SaveReparentState(f.Loc(), st); err != nil {
				t.Fatal(err)
			}
			assertBlocked(internal.ReparentRefusalStatePresent)
			st.OwnerPID = os.Getpid()
			if err := internal.SaveReparentState(f.Loc(), st); err != nil {
				t.Fatal(err)
			}

			// A post-image plus a live descendant of planned_new_sha is the
			// same commit-point proof on the read-only plan and execution
			// routes, even when the persisted monotonic marker is still false.
			var advanced *reparentFixture
			if mode == "external" {
				advanced = newReparentCustomerExternal(t)
			} else {
				advanced = newReparentCustomerCheckout(t)
			}
			human, _ = advanced.Plan(advanced.Feature, "pr2", "--onto", map[string]string{"external": "master", "checkout": "main"}[mode],
				"--no-fetch", "--max-replay-total", "10")
			token = reparentFingerprint(t, human)
			pauseAfterMetadata := errors.New("pause after metadata")
			internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
				if step == "after-write" {
					return pauseAfterMetadata
				}
				return nil
			}
			if _, _, code := advanced.Run(advanced.Feature, "pr2", "--onto", map[string]string{"external": "master", "checkout": "main"}[mode],
				"--no-fetch", "--max-replay-total", "10", "--approve-plan", token); code != 1 {
				t.Fatalf("advanced fixture setup exit = %d", code)
			}
			internal.ReparentStepHook = nil
			advancedState := internal.LoadReparentState(advanced.Loc()).State
			if advancedState == nil || advancedState.CommitPointReached {
				t.Fatalf("advanced fixture must have post-image bytes without a persisted marker: %+v", advancedState)
			}
			advancedRow := advancedState.Rows[len(advancedState.Rows)-1]
			tree := advanced.SHA(advancedRow.PlannedNewSHA + "^{tree}")
			cmd := reparentTestGitCommand(t, advanced.Repo, "commit-tree", tree, "-p", advancedRow.PlannedNewSHA)
			cmd.Stdin = strings.NewReader("operator advance\n")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("create advanced descendant: %v\n%s", err, out)
			}
			advancedTip := strings.TrimSpace(string(out))
			advanced.reparentGit(advanced.Repo, "update-ref", "refs/heads/"+advancedRow.GitBranch, advancedTip)

			doc, _, code := advanced.Run(advanced.Feature, "--plan", "--json", "--continue")
			if code != 0 {
				t.Fatalf("advanced continuation plan exit = %d", code)
			}
			var advancedPlan internal.ReparentPlan
			if err := json.Unmarshal([]byte(doc), &advancedPlan); err != nil {
				t.Fatal(err)
			}
			if !advancedPlan.Runnable || !advancedPlan.Approval.Usable {
				t.Fatalf("plan blocked commit evidence execution accepts: %+v", advancedPlan.Blockers)
			}
			for _, blocker := range advancedPlan.Blockers {
				if blocker.Kind == internal.ReparentRefusalRefForeignValue {
					t.Fatalf("descendant-of-planned was misclassified as foreign: %+v", blocker)
				}
			}
			advancedState.CommitPointReached = true
			advancedState.Stage = internal.ReparentStageMetadataWritten
			advancedState.ResumeStage = internal.ReparentStageMetadataWritten
			if err := internal.SaveReparentState(advanced.Loc(), advancedState); err != nil {
				t.Fatal(err)
			}
			doc, _, code = advanced.Run(advanced.Feature, "--plan", "--json", "--continue")
			if code != 0 {
				t.Fatalf("post-commit advanced plan exit = %d", code)
			}
			advancedPlan = internal.ReparentPlan{}
			if err := json.Unmarshal([]byte(doc), &advancedPlan); err != nil {
				t.Fatal(err)
			}
			if !advancedPlan.Runnable || !advancedPlan.Approval.Usable {
				t.Fatalf("a persisted post-commit advance must remain admissible: %+v", advancedPlan.Blockers)
			}
			if _, stderr, code := advanced.Run(advanced.Feature, "--continue"); code != 0 {
				t.Fatalf("execution refused its own continuation-plan evidence: exit=%d stderr=%q", code, stderr)
			}
			if got := advanced.SHA("refs/heads/" + advancedRow.GitBranch); got != advancedTip {
				t.Fatalf("continue moved operator advance %s to %s", advancedTip, got)
			}

			if mode == "external" {
				repair := newReparentCustomerExternal(t)
				repairHuman, _ := repair.Plan(repair.Feature, "pr2", "--onto", "master",
					"--no-fetch", "--max-replay-total", "10")
				repairToken := reparentFingerprint(t, repairHuman)
				internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
					if step == "pins-written" {
						return errors.New("pause for compatibility repair")
					}
					return nil
				}
				if _, _, code := repair.Run(repair.Feature, "pr2", "--onto", "master", "--no-fetch",
					"--max-replay-total", "10", "--approve-plan", repairToken); code != 1 {
					t.Fatalf("compat repair setup exit = %d", code)
				}
				internal.ReparentStepHook = nil
				if err := os.Remove(internal.SyncRunStatePath(repair.FeaturePath)); err != nil {
					t.Fatal(err)
				}
				doc, _, code := repair.Run(repair.Feature, "--plan", "--json", "--continue")
				if code != 0 {
					t.Fatalf("compat repair plan exit = %d", code)
				}
				var repairPlan internal.ReparentPlan
				if err := json.Unmarshal([]byte(doc), &repairPlan); err != nil {
					t.Fatal(err)
				}
				if !repairPlan.Runnable || !repairPlan.Approval.Usable {
					t.Fatalf("owned missing compatibility is automatic repair, not a blocker: %+v", repairPlan.Blockers)
				}
				for _, blocker := range repairPlan.Blockers {
					if blocker.Kind == internal.ReparentRefusalCompatArtifactMissing {
						t.Fatalf("repairable compatibility was published fatal: %+v", blocker)
					}
				}
				if _, stderr, code := repair.Run(repair.Feature, "--continue"); code != 0 {
					t.Fatalf("compat repair execution exit=%d stderr=%q", code, stderr)
				}

				completed := newReparentCustomerExternal(t)
				completedHuman, _ := completed.Plan(completed.Feature, "pr2", "--onto", "master",
					"--no-fetch", "--max-replay-total", "10")
				completedToken := reparentFingerprint(t, completedHuman)
				internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
					if step == "artifacts-removed" {
						return errors.New("pause before terminal artifact removal")
					}
					return nil
				}
				if _, _, code := completed.Run(completed.Feature, "pr2", "--onto", "master", "--no-fetch",
					"--max-replay-total", "10", "--approve-plan", completedToken); code != 1 {
					t.Fatalf("completed residue setup exit = %d", code)
				}
				internal.ReparentStepHook = nil
				completedState := internal.LoadReparentState(completed.Loc()).State
				completedState.SetStage(internal.ReparentStageCompleted)
				if err := internal.SaveReparentState(completed.Loc(), completedState); err != nil {
					t.Fatal(err)
				}
				doc, _, code = completed.Run(completed.Feature, "--plan", "--json", "--continue")
				if code != 0 {
					t.Fatalf("completed residue plan exit = %d", code)
				}
				var completedPlan internal.ReparentPlan
				if err := json.Unmarshal([]byte(doc), &completedPlan); err != nil {
					t.Fatal(err)
				}
				if !completedPlan.Runnable || !completedPlan.Approval.Usable {
					t.Fatalf("completed residue cleanup must be admissible: %+v", completedPlan.Blockers)
				}
				if _, stderr, code := completed.Run(completed.Feature, "--continue"); code != 0 {
					t.Fatalf("completed cleanup execution exit=%d stderr=%q", code, stderr)
				}
			}
		})
	}
}

// TestReparentRoute_PlanContinueNeverFetches is AC-051's never-fetch column
// for the combination the dispatch fix made reachable: a resume never fetches,
// so its preview must not either.
func TestReparentRoute_PlanContinueNeverFetches(t *testing.T) {
	for _, mode := range []string{"external", "checkout"} {
		t.Run(mode, func(t *testing.T) {
			var f *reparentFixture
			if mode == "external" {
				f = newReparentCustomerExternal(t)
			} else {
				f = newReparentCustomerCheckout(t)
			}
			reparentPlantState(t, f, internal.ReparentStageComputing)

			// --fetch itself is refused on a recovery verb by §3.5 rule 4, so
			// the suppression is asserted on the DEFAULT policy: external mode
			// defaults to fetch, and this route must still not perform one.
			_, _, log := reparentPushCapture(t, func() {
				if _, _, exit := f.Run(f.Feature, "--plan", "--continue"); exit != 0 {
					t.Errorf("--plan --continue exit = %d", exit)
				}
			})
			for _, argv := range log.argv {
				if argvHas(argv, "fetch") {
					t.Fatalf("a resume preview must never fetch, saw %v", argv)
				}
			}
		})
	}
}
