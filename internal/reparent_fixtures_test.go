package internal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Minimal package-internal reparent fixtures.
//
// These are deliberately primitive: one real repository with a handful of
// commits, used only for focused Git primitives (ref enumeration, OID
// disambiguation, ancestor trichotomy, resolver agreement). Topology-shaped
// fixtures — the customer topology, the issue #4 closure, the checkout and
// external workspace layouts — belong to package cli, because a package
// internal test cannot reference an identifier declared in internal/cli.
// ---------------------------------------------------------------------------

// reparentRepo is one real temporary Git repository.
type reparentRepo struct {
	t   *testing.T
	Dir string
}

func reparentTestGitCommand(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	reparentRecordGitArgv(t, args...)
	t.Logf("reparent test git argv: git %s", strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1")
	return cmd
}

// newReparentPrimitiveRepo builds a real repository with three commits on
// main. GIT_CONFIG_COUNT=0 is set for the whole test process, because
// production probes set no cmd.Env and inherit this process: hardening only
// the fixture's own children would leave every probe running under whatever
// the host injected.
func newReparentPrimitiveRepo(t *testing.T) *reparentRepo {
	t.Helper()
	// Every real repository this suite builds is one §17.3 leaf, counted here
	// so the budget is MEASURED — including the leaves a table sub-case
	// creates, which no static scan of call sites can see.
	reparentCountGitLeafFor(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := canonicalize(t.TempDir())
	gitInTest(t, dir, "init", "-q", "-b", "main", ".")
	repo := &reparentRepo{t: t, Dir: dir}
	repo.Commit("one.txt", "one")
	repo.Commit("two.txt", "two")
	repo.Commit("three.txt", "three")
	return repo
}

// Commit writes a file and commits it, returning the new commit's OID.
func (r *reparentRepo) Commit(name, content string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(content+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	gitInTest(r.t, r.Dir, "add", name)
	gitInTest(r.t, r.Dir, "commit", "-q", "-m", "add "+name)
	return r.RevParse("HEAD")
}

// Git runs a fixture-side git command and returns its trimmed output.
func (r *reparentRepo) Git(args ...string) string {
	r.t.Helper()
	return gitInTest(r.t, r.Dir, args...)
}

// RevParse resolves a revision with a fixture-side git.
func (r *reparentRepo) RevParse(rev string) string {
	r.t.Helper()
	return strings.ToLower(gitInTest(r.t, r.Dir, "rev-parse", rev))
}

// Branch creates a branch at the given start point.
func (r *reparentRepo) Branch(name, start string) {
	r.t.Helper()
	gitInTest(r.t, r.Dir, "branch", name, start)
}

// reparentCaptureArgv installs the argv audit hook for the duration of a test
// and returns a pointer to the recorded log. Every entry is one emitted argv,
// without the implicit leading "git".
func reparentCaptureArgv(t *testing.T) *[][]string {
	t.Helper()
	log := &[][]string{}
	ReparentGitArgvHook = func(dir string, args []string) {
		_ = dir
		*log = append(*log, args)
	}
	t.Cleanup(func() { ReparentGitArgvHook = nil })
	return log
}

// reparentAssertNoDashC fails when any recorded argv selects its directory
// with `-C` instead of exec.Cmd.Dir.
func reparentAssertNoDashC(t *testing.T, log [][]string) {
	t.Helper()
	for _, argv := range log {
		for _, arg := range argv {
			if arg == "-C" {
				t.Fatalf("reparent emitted a -C argv: git %s", strings.Join(argv, " "))
			}
		}
	}
}

// reparentSpawnLivePID starts a genuinely live foreign process and returns
// its pid. A hard-coded pid cannot be used: signal 0 against a process this
// user does not own fails with EPERM, which isProcessAlive reads as dead.
func reparentSpawnLivePID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn long-lived process: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if !isProcessAlive(pid) {
		t.Fatalf("pid %d must be alive immediately after Start", pid)
	}
	return pid
}

// reparentSpawnDeadPID returns the pid of a process that has certainly exited.
func reparentSpawnDeadPID(t *testing.T) int {
	t.Helper()
	cmd := reparentTestGitCommand(t, "", "--version")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn short-lived process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("short-lived process exited with an error: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pid) {
			return pid
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d never became dead after Wait", pid)
	return 0
}

// ---------------------------------------------------------------------------
// A representative, fully-populated plan document.
//
// It carries both binding kinds — a pinned target and one parent-computed
// descendant whose destination cannot exist yet — so every cell that must be
// an explicit absence (§7.5a) is exercised by the render and fingerprint
// suites rather than assumed.
// ---------------------------------------------------------------------------

func reparentPlanFixtureString(v string) *string { return &v }

func reparentPlanFixtureInt(v int) *int { return &v }

func reparentPlanFixtureBool(v bool) *bool { return &v }

func reparentSamplePlan() ReparentPlan {
	targetCutoff := ReparentPlanCutoff{
		RecordedSHA:      reparentPlanFixtureString("1111111111111111111111111111111111111111"),
		RecordedState:    ReparentCutoffRecordPresent,
		ResolvedSHA:      reparentPlanFixtureString("1111111111111111111111111111111111111111"),
		Provenance:       ReparentCutoffRecordedBySync,
		AncestorOfBranch: reparentPlanFixtureBool(true),
	}
	descendantCutoff := ReparentPlanCutoff{
		RecordedSHA:      reparentPlanFixtureString("2222222222222222222222222222222222222222"),
		RecordedState:    ReparentCutoffRecordPresent,
		ResolvedSHA:      reparentPlanFixtureString("2222222222222222222222222222222222222222"),
		Provenance:       ReparentCutoffRecordedBySync,
		AncestorOfBranch: reparentPlanFixtureBool(true),
	}

	target := ReparentPlanRow{
		Role:            ReparentRoleTarget,
		Order:           0,
		Name:            "pr2",
		GitBranch:       "feature/pr2",
		Repo:            "",
		Materialization: "materialized",
		ExecutionContext: PlanContext{
			ContextID: reparentPlanFixtureString(strings.Repeat("1", 64)),
			RepoRoot:  reparentPlanFixtureString("/repo"),
			Source:    "workspace-repo-root",
		},
		Head: PlanEntryHead{
			State: reparentPlanFixtureString("present"),
			SHA:   reparentPlanFixtureString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		},
		OldParent: ReparentParent{
			RequestedToken: nil,
			StoredToken:    reparentPlanFixtureString("pr1"),
			Kind:           ReparentParentKindStackEntry,
			Ref:            reparentPlanFixtureString("refs/heads/feature/pr1"),
			SHA:            reparentPlanFixtureString("1111111111111111111111111111111111111111"),
			Resolution:     ReparentResolutionEntry,
		},
		NewParent: ReparentParent{
			RequestedToken: reparentPlanFixtureString("master"),
			StoredToken:    reparentPlanFixtureString("refs/heads/master"),
			Kind:           ReparentParentKindLiteralRef,
			Ref:            reparentPlanFixtureString("refs/heads/master"),
			SHA:            reparentPlanFixtureString("3333333333333333333333333333333333333333"),
			Resolution:     ReparentResolutionRefHead,
			ResolverAgreement: ReparentResolverAgreement{
				Checked: true,
				Agreed:  reparentPlanFixtureBool(true),
				Resolvers: []ReparentResolverVerdict{
					{Name: ReparentResolverDestination, SHA: reparentPlanFixtureString("3333333333333333333333333333333333333333"), Agrees: true},
					{Name: ReparentResolverSyncBase, SHA: reparentPlanFixtureString("3333333333333333333333333333333333333333"), Agrees: true},
					{Name: ReparentResolverStackBase, SHA: reparentPlanFixtureString("3333333333333333333333333333333333333333"), Agrees: true},
					{Name: ReparentResolverCheckout, SHA: reparentPlanFixtureString("3333333333333333333333333333333333333333"), Agrees: true},
				},
			},
		},
		DestinationBinding: ReparentBindingPinned,
		DestinationSHA:     reparentPlanFixtureString("3333333333333333333333333333333333333333"),
		Cutoff:             targetCutoff,
		Strategy:           ReparentRowStrategy,
		Argv: []string{
			"git", "-c", "rebase.backend=merge", "-c", "rebase.updateRefs=false",
			"-c", "rebase.autoStash=false", "-c", "rebase.forkPoint=false",
			"-c", "rebase.rebaseMerges=false", "rebase", "--merge", "--no-fork-point",
			"--no-update-refs", "--no-autostash", "--no-rebase-merges",
			"--onto", "3333333333333333333333333333333333333333",
			"1111111111111111111111111111111111111111",
		},
		EffectiveBackend: ReparentRowBackend,
		Replay: PlanEntryReplay{
			Determinacy:     "exact",
			CandidateCount:  reparentPlanFixtureInt(1),
			CandidateDigest: reparentPlanFixtureString("cafebabe"),
			Commits:         []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			FirstCandidate:  &PlanReplayCandidate{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Short: "aaaaaaaaaaaa", Subject: "add feature"},
		},
		MergeCommitsInRange: reparentPlanFixtureInt(0),
		Pins: ReparentRowPins{
			EntryID:       "abcd1234",
			OldRefPattern: "refs/tws/reparent/<run-id>/old/abcd1234",
			NewRefPattern: "refs/tws/reparent/<run-id>/new/abcd1234",
		},
	}

	descendant := ReparentPlanRow{
		Role:            ReparentRoleDescendant,
		Order:           1,
		Name:            "pr3",
		GitBranch:       "feature/pr3",
		Repo:            "",
		Materialization: "materialized",
		ExecutionContext: PlanContext{
			ContextID: reparentPlanFixtureString(strings.Repeat("2", 64)),
			RepoRoot:  reparentPlanFixtureString("/repo"),
			Source:    "workspace-repo-root",
		},
		Head: PlanEntryHead{
			State: reparentPlanFixtureString("present"),
			SHA:   reparentPlanFixtureString("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		},
		OldParent: ReparentParent{
			StoredToken: reparentPlanFixtureString("pr2"),
			Kind:        ReparentParentKindStackEntry,
			Ref:         reparentPlanFixtureString("refs/heads/feature/pr2"),
			SHA:         reparentPlanFixtureString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			Resolution:  ReparentResolutionEntry,
		},
		NewParent: ReparentParent{
			StoredToken: reparentPlanFixtureString("pr2"),
			Kind:        ReparentParentKindStackEntry,
			Ref:         reparentPlanFixtureString("refs/heads/feature/pr2"),
			// Deferred: the parent's post-replay tip does not exist yet.
			SHA:        nil,
			Resolution: ReparentResolutionEntry,
		},
		DestinationBinding: ReparentBindingParentComputed,
		DestinationParent:  reparentPlanFixtureString("pr2"),
		DestinationSHA:     nil,
		Cutoff:             descendantCutoff,
		Strategy:           ReparentRowStrategy,
		Argv: []string{
			"git", "-c", "rebase.backend=merge", "rebase", "--merge",
			"--onto", "<computed-tip:pr2>",
			"2222222222222222222222222222222222222222",
		},
		EffectiveBackend: ReparentRowBackend,
		Replay: PlanEntryReplay{
			Determinacy:     "exact",
			CandidateCount:  reparentPlanFixtureInt(2),
			CandidateDigest: reparentPlanFixtureString("deadbeef"),
			Commits:         []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccccccccccc"},
		},
		MergeCommitsInRange: reparentPlanFixtureInt(0),
		Pins: ReparentRowPins{
			EntryID:       "ef567890",
			OldRefPattern: "refs/tws/reparent/<run-id>/old/ef567890",
			NewRefPattern: "refs/tws/reparent/<run-id>/new/ef567890",
		},
	}

	return ReparentPlan{
		SchemaVersion: ReparentPlanSchemaVersion,
		Route:         ReparentRouteFresh,
		Invocation:    ReparentInvocationPlanOnly,
		Workspace:     PlanWorkspace{Mode: "checkout", StableID: reparentPlanFixtureString("stable-id")},
		Feature:       "customer",
		Policy: ReparentPlanPolicy{
			Fetch:             "no-fetch",
			OntoKindRequested: "ref",
			OIDWidth:          reparentPlanFixtureInt(40),
			LimitsSupplied:    true,
			LimitsOrigin:      ReparentLimitsOriginFlags,
			Validation: ReparentPlanValidation{
				Applies:              true,
				Source:               "config-workspace",
				CommandDigest:        reparentPlanFixtureString("digest"),
				RunsOn:               ReparentValidationRunsOn,
				RequiresCleanContext: true,
			},
		},
		Target:      target,
		Descendants: []ReparentPlanRow{descendant},
		MetadataDelta: ReparentPlanMetadataDelta{
			Entries: []ReparentPlanMetadataEntry{
				{
					Name:                   "pr2",
					BaseBefore:             "pr1",
					BaseAfter:              "refs/heads/master",
					LastBaseSHABefore:      reparentPlanFixtureString("1111111111111111111111111111111111111111"),
					LastBaseSHAAfter:       reparentPlanFixtureString("3333333333333333333333333333333333333333"),
					LastBaseSHAAfterSource: ReparentLastBaseSourcePinned,
					Changed:                true,
				},
				{
					Name:                   "pr3",
					BaseBefore:             "pr2",
					BaseAfter:              "pr2",
					LastBaseSHABefore:      reparentPlanFixtureString("2222222222222222222222222222222222222222"),
					LastBaseSHAAfter:       nil,
					LastBaseSHAAfterSource: ReparentLastBaseSourceReplay,
					Changed:                true,
				},
			},
			StackSHA256Before:            "4444444444444444444444444444444444444444444444444444444444444444",
			StackSHA256AfterExpected:     nil,
			Writer:                       ReparentMetadataWriter,
			WritePoint:                   ReparentMetadataWritePoint,
			PostImageKnown:               false,
			EntriesOutsideClosureChanged: false,
		},
		Strategy: ReparentPlanStrategy{
			Kind:                ReparentStrategyKind,
			RunID:               nil,
			ComputationContext:  ReparentComputationCheckout,
			ComputationPath:     nil,
			Backend:             ReparentRowBackend,
			ForbiddenOperations: ReparentForbiddenOperations,
			PinNamespace:        ReparentPinNamespaceShape,
			Atomicity: ReparentPlanAtomicity{
				RefBackend:                      ReparentRefBackendFiles,
				RefCommit:                       ReparentRefCommitKind,
				RefCommitRaceAtomic:             true,
				RefCommitCrashAtomic:            reparentPlanFixtureBool(false),
				MetadataWriteAtomic:             true,
				MetadataWriteDurable:            true,
				CombinedAtomic:                  false,
				PartialCommitRecovery:           ReparentRecoveryRowByRow,
				CommitPoint:                     ReparentCommitPoint,
				ReferenceTransactionHookMayVeto: true,
			},
		},
		Holders: ReparentPlanHolders{
			Applies:        true,
			OriginalBranch: reparentPlanFixtureString("feature/pr2"),
			OriginalHead:   reparentPlanFixtureString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			RestorePoint:   ReparentHolderRestorePoint,
			Rows: []ReparentPlanHolder{
				{
					GitBranch: "feature/pr2", HolderKind: "primary-checkout",
					HolderPath: reparentPlanFixtureString("/repo"), PreimageSHA: reparentPlanFixtureString(strings.Repeat("a", 40)),
					Action: "detach-and-restore", Safe: true,
				},
			},
		},
		Remote: ReparentPlanRemote{
			Remote:   ReparentRemoteName,
			Rows:     []ReparentPlanRemoteRow{{Name: "pr2", GitBranch: "feature/pr2", Remote: ReparentRemoteName, Divergence: "none", ProviderHint: "github"}},
			Guidance: []string{"retarget the pull request for feature/pr2 to master"},
			FollowupRecord: ReparentPlanRemoteFollowup{
				WillWrite:  true,
				WritePoint: ReparentRemoteRecordWritePoint,
				Entries:    []string{"pr2"},
				ClearRule:  "cleared when the recorded branch is pushed",
			},
		},
		Freshness: "fresh",
		Summary: ReparentPlanSummary{
			Plannability:       ReparentPlannabilityRows,
			HasWork:            true,
			Rows:               2,
			Descendants:        1,
			DeferredRows:       1,
			MaxEntryCandidates: reparentPlanFixtureInt(2),
			TotalCandidates:    reparentPlanFixtureInt(3),
		},
		Runnable: true,
		Guard: PlanGuardBlock{
			Limits: PlanGuardLimitSet{
				MaxReplayPerEntry: PlanGuardLimit{Value: reparentPlanFixtureInt(5), Origin: "flag"},
				MaxReplayTotal:    PlanGuardLimit{Value: reparentPlanFixtureInt(10), Origin: "flag"},
			},
			IndeterminacyPolicy: "jit-deferred",
		},
		Approval: ReparentPlanApproval{
			Fingerprint: reparentPlanFixtureString(strings.Repeat("a", 64)),
			Usable:      true,
			Scope:       ReparentApprovalScopeFresh,
			Covers: ReparentPlanApprovalCovers{
				Scope:          ReparentApprovalScopeFresh,
				HasWork:        true,
				RequiresLimits: true,
				EncodingSafe:   true,
				Note:           "approves exactly this plan",
			},
		},
	}
}
