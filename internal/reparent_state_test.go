package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2).
//
//	T-069 ReclaimCheckoutLock: absent / self / dead / live-foreign . AC-081
//	T-082 waived_kinds typing and failure_domain persistence ....... AC-095
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §11.1/§11.5 — paths, round-trip, version and classification.
// ---------------------------------------------------------------------------

// newReparentStateFixture builds an external and a checkout location over one
// temporary workspace, so a path test compares both without a second fixture.
func newReparentStateFixture(t *testing.T) (external, checkout ReparentLocation) {
	t.Helper()
	root := canonicalize(t.TempDir())
	metadata := filepath.Join(root, ".tws")
	featurePath := filepath.Join(metadata, "features", "customer")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: root, Mode: ModeExternal, MetadataRoot: metadata, StableID: "stable"}
	external = ReparentLocationFor(ws, "customer", featurePath)
	ws.Mode = ModeCheckout
	checkout = ReparentLocationFor(ws, "customer", featurePath)
	return external, checkout
}

func reparentStateFixture(loc ReparentLocation) *ReparentState {
	stack := Stack{Branches: []StackEntry{{
		Name: "pr2", Branch: "feature/pr2", Base: "refs/heads/main",
		LastBaseSHA: strings.Repeat("1", 40),
	}}}
	stackBytes, _ := yaml.Marshal(&stack)
	sum := sha256.Sum256(stackBytes)
	runID := strings.Repeat("a", 32)
	oldSHA := strings.Repeat("1", 40)
	newSHA := strings.Repeat("2", 40)
	st := &ReparentState{
		StateVersion:               ReparentStateVersion,
		RunID:                      runID,
		CreatedAt:                  reparentNow(),
		WorkspaceMode:              string(loc.Mode),
		WorkspaceStableID:          loc.StableID,
		Feature:                    loc.Feature,
		WorkspaceRepoRoot:          "/repo",
		RepoRoot:                   "/repo",
		RepoCommonDir:              "/repo/.git",
		RefBackend:                 ReparentRefBackendFiles,
		OIDWidth:                   40,
		Strategy:                   ReparentStateStrategy,
		Backend:                    ReparentStateBackend,
		Stage:                      ReparentStageInitializing,
		ResumeStage:                ReparentStageInitializing,
		OwnerPID:                   os.Getpid(),
		OwnerToken:                 strings.Repeat("b", 32),
		TargetName:                 "pr2",
		TargetGitBranch:            "feature/pr2",
		OldParentKind:              ReparentParentKindLiteralRef,
		OldParentStoredToken:       "refs/heads/main",
		OldParentRef:               "refs/heads/main",
		OldParentSHA:               oldSHA,
		NewParentKind:              ReparentParentKindLiteralRef,
		NewParentRequestedToken:    "main",
		NewParentStoredToken:       "refs/heads/main",
		NewParentRef:               "refs/heads/main",
		NewParentSHA:               newSHA,
		DestinationPinRef:          reparentDestPinRef(runID),
		ValidationSource:           "none",
		OriginalBranch:             "main",
		OriginalHead:               strings.Repeat("3", 40),
		StackBeforeBase64:          base64.StdEncoding.EncodeToString(stackBytes),
		StackSHA256Before:          hex.EncodeToString(sum[:]),
		RemoteRecordBeforeCaptured: true,
		ApprovedFingerprint:        strings.Repeat("f", 64),
		FetchPolicy:                string(SyncFetchDisabled),
		Rows: []ReparentStateRow{{
			Order: 0, Name: "pr2", GitBranch: "feature/pr2", Role: ReparentRoleTarget,
			PreimageSHA: strings.Repeat("4", 40),
			CutoffSHA:   oldSHA, CutoffProvenance: ReparentCutoffRecordedBySync,
			DestinationBinding: ReparentBindingPinned, DestinationSHA: newSHA,
			BaseBefore: "refs/heads/main", BaseAfter: "refs/heads/main",
			LastBaseSHABefore: oldSHA, LastBaseSHAAfter: newSHA,
			Argv: reparentRowArgv(newSHA, oldSHA), EffectiveBackend: ReparentRowBackend,
			OldPinRef: reparentOldPinRef(runID, ReparentEntryRefID(loc.Feature, "pr2")),
			NewPinRef: reparentNewPinRef(runID, ReparentEntryRefID(loc.Feature, "pr2")),
			Stage:     ReparentRowPending,
		}},
	}
	if loc.Mode == ModeExternal {
		st.ScratchPath = canonicalReparentScratchPath(loc, runID)
	}
	return st
}

func reparentStateFixtureAtCommitPoint(t *testing.T, loc ReparentLocation) *ReparentState {
	t.Helper()
	st := reparentStateFixture(loc)
	row := &st.Rows[0]
	row.PlannedNewSHA = row.PreimageSHA
	row.Noop = true
	row.MaterializedArgv = append([]string{}, row.Argv...)
	row.Stage = ReparentRowCommitted

	beforeData, err := base64.StdEncoding.DecodeString(st.StackBeforeBase64)
	if err != nil {
		t.Fatal(err)
	}
	var after Stack
	if err := yaml.Unmarshal(beforeData, &after); err != nil {
		t.Fatal(err)
	}
	after.Branches[0].Base = row.BaseAfter
	after.Branches[0].LastBaseSHA = row.LastBaseSHAAfter
	afterData, err := yaml.Marshal(&after)
	if err != nil {
		t.Fatal(err)
	}
	afterHash := sha256.Sum256(afterData)
	st.StackAfterBase64 = base64.StdEncoding.EncodeToString(afterData)
	st.StackSHA256AfterExpected = hex.EncodeToString(afterHash[:])
	st.StackSHA256After = st.StackSHA256AfterExpected
	st.CASTransactionSucceeded = true
	st.CASRows = []ReparentStateCASRow{{
		Ref: "refs/heads/" + row.GitBranch, OldValue: row.PreimageSHA,
		NewValue: row.PlannedNewSHA, Applied: true, Noop: true,
	}}
	st.RemoteRecordEmpty = true
	st.CompatArtifactsWritten = true
	st.CommitPointReached = true
	st.SetStage(ReparentStageMetadataWritten)
	return st
}

func TestReparentStatePathsAreModeSpecific(t *testing.T) {
	external, checkout := newReparentStateFixture(t)

	wantExternal := filepath.Join(external.FeaturePath, ".reparent-state.v1.yaml")
	if got := ReparentStatePath(external); got != wantExternal {
		t.Fatalf("external state path = %q, want %q", got, wantExternal)
	}

	t.Run("cleanup removal durability", func(t *testing.T) {
		t.Cleanup(func() { reparentRecoverySyncDir = syncDir })
		external, checkout := newReparentStateFixture(t)
		runID := strings.Repeat("a", 32)
		st := reparentStateFixture(external)

		t.Run("compat and state removals fsync parents", func(t *testing.T) {
			if err := WriteReparentCompatArtifacts(external, st, []string{"pr2"}); err != nil {
				t.Fatal(err)
			}
			var synced []string
			reparentRecoverySyncDir = func(dir string) error {
				synced = append(synced, filepath.Clean(dir))
				return nil
			}
			if err := RemoveReparentCompatArtifacts(external, runID); err != nil {
				t.Fatal(err)
			}
			wantCompatDir := filepath.Clean(external.FeaturePath)
			if len(synced) != 1 || synced[0] != wantCompatDir {
				t.Fatalf("compat fsync dirs = %v, want %s", synced, wantCompatDir)
			}

			if err := SaveReparentState(external, st); err != nil {
				t.Fatal(err)
			}
			synced = nil
			injected := errors.New("state directory fsync fault")
			reparentRecoverySyncDir = func(dir string) error {
				synced = append(synced, filepath.Clean(dir))
				return injected
			}
			if err := RemoveReparentState(external); !errors.Is(err, injected) {
				t.Fatalf("state remove fsync fault = %v", err)
			}
			if _, err := os.Lstat(ReparentStatePath(external)); !os.IsNotExist(err) {
				t.Fatalf("state remove did not precede fsync: %v", err)
			}
			if len(synced) != 1 || synced[0] != filepath.Dir(ReparentStatePath(external)) {
				t.Fatalf("state fsync dirs = %v", synced)
			}
			reparentRecoverySyncDir = syncDir
		})

		t.Run("legacy feature and global locks fsync separate dirs", func(t *testing.T) {
			legacy := checkout
			legacy.FeaturePath = filepath.Join(filepath.Dir(legacy.CheckoutStateDir), legacy.Feature)
			if err := os.MkdirAll(legacy.FeaturePath, 0o755); err != nil {
				t.Fatal(err)
			}
			token := strings.Repeat("b", 32)
			if _, err := AcquireCheckoutMutationLock(
				legacy.CheckoutStateDir, token, legacy.Feature, "reparent", ReparentStatePath(legacy),
				filepath.Dir(CheckoutTransactionPath(legacy.FeaturePath)),
			); err != nil {
				t.Fatal(err)
			}
			if err := AcquireCheckoutLock(legacy.FeaturePath); err != nil {
				t.Fatal(err)
			}
			featureBytes, err := os.ReadFile(CheckoutLockPath(legacy.FeaturePath))
			if err != nil {
				t.Fatal(err)
			}
			globalBytes, err := os.ReadFile(CheckoutMutationLockPath(legacy.CheckoutStateDir))
			if err != nil {
				t.Fatal(err)
			}
			var synced []string
			reparentRecoverySyncDir = func(dir string) error {
				synced = append(synced, filepath.Clean(dir))
				return nil
			}
			if err := releaseReparentModeLock(legacy, &ReparentExclusionOwner{
				OwnerToken: token, LockBytes: featureBytes, GlobalLockBytes: globalBytes,
			}); err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{
				filepath.Dir(CheckoutLockPath(legacy.FeaturePath)):              true,
				filepath.Dir(CheckoutMutationLockPath(legacy.CheckoutStateDir)): true,
			}
			for _, dir := range synced {
				delete(want, dir)
			}
			if len(want) != 0 {
				t.Fatalf("cleanup did not fsync lock dirs %v; synced %v", want, synced)
			}
			reparentRecoverySyncDir = syncDir
		})
	})

	// P-5: the authoritative checkout artifact lives under
	// Workspace.CheckoutStateDir(), which is MetadataRoot-derived, NOT under
	// the feature-path-derived checkoutStateDir the compatibility helpers use.
	wantCheckout := filepath.Join(checkout.CheckoutStateDir, "customer-reparent.v1.yaml")
	if got := ReparentStatePath(checkout); got != wantCheckout {
		t.Fatalf("checkout state path = %q, want %q", got, wantCheckout)
	}
	if got := ReparentStatePath(checkout); got == CheckoutTransactionPath(checkout.FeaturePath) {
		t.Fatal("the authoritative artifact must not share the compatibility transaction's path")
	}
	// The compatibility paths stay exactly where every shipped loader looks.
	if !strings.HasSuffix(CheckoutTransactionPath(checkout.FeaturePath), "customer-checkout-sync.yaml") {
		t.Fatalf("compatibility transaction path moved: %s", CheckoutTransactionPath(checkout.FeaturePath))
	}
	if !strings.HasSuffix(CheckoutLockPath(checkout.FeaturePath), "customer-checkout-sync.lock") {
		t.Fatalf("compatibility lock path moved: %s", CheckoutLockPath(checkout.FeaturePath))
	}
}

func TestReparentStateRoundTripAndMode(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	st := reparentStateFixture(external)
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}

	t.Run("strict semantic validation before recovery", func(t *testing.T) {
		external, _ := newReparentStateFixture(t)
		write := func(t *testing.T, st *ReparentState, suffix string) []byte {
			t.Helper()
			data, err := yaml.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, suffix...)
			if err := os.MkdirAll(filepath.Dir(ReparentStatePath(external)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ReparentStatePath(external), data, 0o600); err != nil {
				t.Fatal(err)
			}
			return data
		}
		journalRecord := ReparentRemoteRecord{
			RecordVersion: ReparentRemoteRecordVersion,
			Feature:       external.Feature,
			RunID:         strings.Repeat("9", 32),
			CreatedAt:     reparentNow(),
			Entries: []ReparentRemoteEntry{{
				Name: "pr2", GitBranch: "feature/pr2", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/feature/pr2",
				NewTipSHA: strings.Repeat("2", 40), RemoteSHAAtWrite: strings.Repeat("1", 40),
				State: ReparentRemoteStatePending,
			}},
		}
		journalBytes, err := marshalReparentRemoteRecord(external, journalRecord)
		if err != nil {
			t.Fatal(err)
		}

		cases := []struct {
			name   string
			mutate func(*ReparentState)
			suffix string
		}{
			{name: "unknown yaml field", suffix: "future_unsafe_path: /outside\n"},
			{name: "scratch escapes run", mutate: func(st *ReparentState) {
				st.ScratchPath = "/outside/scratch"
			}},
			{name: "repo root disagrees with captured stack", mutate: func(st *ReparentState) {
				st.RepoRoot = "/outside/repo"
				st.RepoCommonDir = "/outside/repo/.git"
			}},
			{name: "argv injection", mutate: func(st *ReparentState) {
				st.Rows[0].Argv = append(st.Rows[0].Argv, "-C", "/outside")
			}},
			{name: "foreign pin", mutate: func(st *ReparentState) {
				st.Rows[0].OldPinRef = "refs/heads/main"
			}},
			{name: "bad stack hash", mutate: func(st *ReparentState) {
				st.StackSHA256Before = strings.Repeat("0", 64)
			}},
			{name: "target metadata disagrees with captured stack", mutate: func(st *ReparentState) {
				st.Rows[0].BaseBefore = "refs/heads/other"
			}},
			{name: "destination identity disagrees with target row", mutate: func(st *ReparentState) {
				st.Rows[0].DestinationSHA = strings.Repeat("7", 40)
			}},
			{name: "resume stage disagrees with forward stage", mutate: func(st *ReparentState) {
				st.ResumeStage = ReparentStageComputing
			}},
			{name: "computed row lacks planned tip", mutate: func(st *ReparentState) {
				st.Rows[0].Stage = ReparentRowComputed
				st.Rows[0].MaterializedArgv = append([]string{}, st.Rows[0].Argv...)
			}},
			{name: "noop relationship is forged", mutate: func(st *ReparentState) {
				st.Rows[0].PlannedNewSHA = strings.Repeat("7", 40)
				st.Rows[0].Noop = true
			}},
			{name: "successful cas lacks rows", mutate: func(st *ReparentState) {
				st.CASTransactionSucceeded = true
			}},
			{name: "committed row lacks successful cas", mutate: func(st *ReparentState) {
				st.Rows[0].PlannedNewSHA = st.Rows[0].PreimageSHA
				st.Rows[0].Noop = true
				st.Rows[0].MaterializedArgv = append([]string{}, st.Rows[0].Argv...)
				st.Rows[0].Stage = ReparentRowCommitted
			}},
			{name: "refs committed stage lacks successful cas", mutate: func(st *ReparentState) {
				st.CompatArtifactsWritten = true
				st.SetStage(ReparentStageRefsCommitted)
			}},
			{name: "commit point lacks metadata and cas", mutate: func(st *ReparentState) {
				st.CommitPointReached = true
			}},
			{name: "commit point precedes metadata written", mutate: func(st *ReparentState) {
				committed := reparentStateFixtureAtCommitPoint(t, external)
				*st = *committed
				st.Stage = ReparentStageCommittingRefs
				st.ResumeStage = ReparentStageCommittingRefs
			}},
			{name: "metadata result precedes writing stage", mutate: func(st *ReparentState) {
				committed := reparentStateFixtureAtCommitPoint(t, external)
				*st = *committed
				st.CommitPointReached = false
				st.Stage = ReparentStagePinningComputed
				st.ResumeStage = ReparentStagePinningComputed
			}},
			{name: "remote proof precedes record stage", mutate: func(st *ReparentState) {
				st.RemoteRecordEmpty = true
			}},
			{name: "duplicate row order", mutate: func(st *ReparentState) {
				dup := st.Rows[0]
				dup.Name = "other"
				dup.GitBranch = "other"
				st.Rows = append(st.Rows, dup)
			}},
			{name: "remote restore without source", mutate: func(st *ReparentState) {
				st.RemoteRecordBeforeCaptured = true
				st.RemoteRecordRestorePending = true
			}},
			{name: "arbitrary approved holder path", mutate: func(st *ReparentState) {
				st.ApprovedHolders = []ReparentApprovedHolder{{
					Path: "../outside", RepoCommonDir: "/repo/.git",
					GitBranch: "feature/pr2", HeadSHA: strings.Repeat("4", 40),
				}}
			}},
			{name: "holder progress disagrees with approved evidence", mutate: func(st *ReparentState) {
				st.ApprovedHolders = []ReparentApprovedHolder{{
					Path: "/repo/worktree", RepoCommonDir: "/repo/.git",
					GitBranch: "feature/pr2", HeadSHA: strings.Repeat("4", 40),
					HolderKind: "linked-worktree",
				}}
				st.DetachedHolders = []ReparentStateHolder{{
					Path: "/repo/worktree", RepoCommonDir: "/repo/.git",
					GitBranch: "other", PreimageSHA: strings.Repeat("4", 40),
					DetachTargetSHA: strings.Repeat("4", 40),
					HolderKind:      "linked-worktree", DetachStatus: ReparentHolderDetached,
				}}
			}},
			{name: "remote clear target is not monotonic", mutate: func(st *ReparentState) {
				st.RemoteClearPending = true
				st.RemoteClearSourcePresent = true
				st.RemoteClearSourceBase64 = base64.StdEncoding.EncodeToString(journalBytes)
				st.RemoteClearTargetPresent = true
				st.RemoteClearTargetBase64 = base64.StdEncoding.EncodeToString(journalBytes)
			}},
			{name: "remote clear intent precedes durable preflight", mutate: func(st *ReparentState) {
				st.RemoteClearPending = true
				st.RemoteClearSourcePresent = true
				st.RemoteClearSourceBase64 = base64.StdEncoding.EncodeToString(journalBytes)
			}},
			{name: "old parent kind none carries identity", mutate: func(st *ReparentState) {
				st.OldParentKind = ReparentParentKindNone
			}},
			{name: "abort row classification disagrees with row", mutate: func(st *ReparentState) {
				st.Stage = ReparentStageAborting
				st.ResumeStage = ReparentStageInitializing
				st.AbortRows = []ReparentStateAbortRow{{
					Ref: "refs/heads/feature/pr2", Restored: true,
					Classification: string(ReparentRefPreimage),
				}}
			}},
			{name: "forward cleanup claim lacks commit point", mutate: func(st *ReparentState) {
				st.Stage = ReparentStageCompleted
				st.ResumeStage = ReparentStageCleanup
				st.CompatArtifactsWritten = true
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				st := reparentStateFixture(external)
				if tc.mutate != nil {
					tc.mutate(st)
				}
				before := write(t, st, tc.suffix)
				log := reparentCaptureArgv(t)
				in := ReparentPlanInput{Loc: external, RepoRoot: "/repo"}
				if _, err := ContinueReparent(in); err == nil {
					t.Fatal("corrupt state was accepted by continue")
				} else {
					var refusal *ReparentRefusalError
					if !errors.As(err, &refusal) || refusal.Kind != ReparentRefusalStateCorrupt {
						t.Fatalf("continue refusal = %v", err)
					}
				}
				if len(*log) != 0 {
					t.Fatalf("corrupt state spawned Git: %v", *log)
				}
				after, err := os.ReadFile(ReparentStatePath(external))
				if err != nil || !bytes.Equal(after, before) {
					t.Fatalf("corrupt state was changed or cleaned: %v", err)
				}
			})
		}

		t.Run("symlink artifact is never followed", func(t *testing.T) {
			st := reparentStateFixture(external)
			data, err := yaml.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside-state.yaml")
			if err := os.WriteFile(outside, data, 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(ReparentStatePath(external))
			if err := os.Symlink(outside, ReparentStatePath(external)); err != nil {
				t.Fatal(err)
			}
			load := LoadReparentState(external)
			if load.Kind != ReparentStateCorrupt {
				t.Fatalf("symlink state kind = %s", load.Kind)
			}
			after, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatalf("symlink target changed: %v", err)
			}
		})
	})

	info, err := os.Stat(ReparentStatePath(external))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state artifact mode = %o, want 0600", perm)
	}

	load := LoadReparentState(external)
	if load.Kind != ReparentStateOK {
		t.Fatalf("load kind = %s (%s)", load.Kind, load.Detail)
	}
	if load.State.RunID != st.RunID || load.State.TargetName != "pr2" {
		t.Fatalf("round-trip lost identity: %+v", load.State)
	}
	if load.State.UpdatedAt == "" {
		t.Fatal("updated_at must be stamped by the writer")
	}
	if load.State.Rows[0].Stage != ReparentRowPending {
		t.Fatalf("row stage = %q", load.State.Rows[0].Stage)
	}

	if err := RemoveReparentState(external); err != nil {
		t.Fatal(err)
	}
	if LoadReparentState(external).Kind != ReparentStateAbsent {
		t.Fatal("remove must leave the artifact absent")
	}
	// Cleanup is repeatable: §11.6a tolerates any step already being done.
	if err := RemoveReparentState(external); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestReparentStateClassification(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	path := ReparentStatePath(external)

	if err := os.WriteFile(path, []byte("state_version: 99\nrun_id: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	load := LoadReparentState(external)
	if load.Kind != ReparentStateUnsupported {
		t.Fatalf("future version kind = %s", load.Kind)
	}
	if refusal := load.Refusal(); refusal == nil || refusal.Kind != ReparentRefusalStateUnsupported {
		t.Fatalf("unsupported refusal = %v", refusal)
	}

	if err := os.WriteFile(path, []byte("state_version: [not, an, int]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadReparentState(external); got.Kind != ReparentStateCorrupt {
		t.Fatalf("corrupt kind = %s", got.Kind)
	}

	st := reparentStateFixture(external)
	st.Feature = "somebody-else"
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	if got := LoadReparentState(external); got.Kind != ReparentStateForeign {
		t.Fatalf("foreign feature kind = %s", got.Kind)
	}

	st = reparentStateFixture(external)
	st.WorkspaceStableID = "another-workspace"
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	if got := LoadReparentState(external); got.Kind != ReparentStateForeign {
		t.Fatalf("foreign workspace kind = %s", got.Kind)
	}
}

func TestReparentStagesAreClosedAndOrdered(t *testing.T) {
	if len(ReparentStages) != 19 {
		t.Fatalf("stage domain has %d members, want exactly 19", len(ReparentStages))
	}
	seen := map[ReparentStage]bool{}
	for _, s := range ReparentStages {
		if seen[s] {
			t.Fatalf("stage %q is declared twice", s)
		}
		seen[s] = true
	}
	for _, absent := range []ReparentStage{"fetching", "planning"} {
		if seen[absent] {
			t.Fatalf("%q is not a stage: it happens before the lock and before the artifact exists", absent)
		}
	}
	if string(ReparentStageMetadataWritten) != "metadata-written" {
		t.Fatalf("metadata-written is spelled %q", ReparentStageMetadataWritten)
	}
	if len(ReparentRowStages) != 5 {
		t.Fatalf("row-stage domain has %d members, want 5", len(ReparentRowStages))
	}
}

func TestReparentResumeStageTracksForwardStages(t *testing.T) {
	st := &ReparentState{}
	st.SetStage(ReparentStageComputing)
	if st.ResumeStage != ReparentStageComputing {
		t.Fatalf("resume_stage = %q, want computing", st.ResumeStage)
	}
	st.SetStage(ReparentStageRefsCommitted)
	st.SetStage(ReparentStageFailed)
	if st.Stage != ReparentStageFailed {
		t.Fatalf("stage = %q", st.Stage)
	}
	if st.ResumeStage != ReparentStageRefsCommitted {
		t.Fatalf("resume_stage = %q, want the last forward stage refs-committed", st.ResumeStage)
	}
	st.SetStage(ReparentStageAborting)
	if st.ResumeStage != ReparentStageRefsCommitted {
		t.Fatalf("aborting must retain resume_stage, got %q", st.ResumeStage)
	}
}

func TestReparentCommitPointMarkerIsMonotonicInState(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	st := reparentStateFixtureAtCommitPoint(t, external)
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	load := LoadReparentState(external)
	if load.Kind != ReparentStateOK || load.State == nil {
		t.Fatalf("committed state did not round-trip: %+v", load)
	}
	if !load.State.CommitPointReached {
		t.Fatal("commit_point_reached must survive a round trip")
	}
}

// ---------------------------------------------------------------------------
// §11.2 — the compatibility envelope, its write order and its fail-closed
// shape.
// ---------------------------------------------------------------------------

func TestReparentExternalCompatWriteOrderAndShape(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-071", "external-compat-write-order")
	_ = "asserts AC-083"
	external, _ := newReparentStateFixture(t)
	st := reparentStateFixture(external)
	st.MaxReplayPerEntry = reparentPlanFixtureInt(5)

	payloadPath := SyncRunStatePath(external.FeaturePath)
	sentinelPath := SyncStatePath(external.FeaturePath)

	// The write ORDER is what matters: with the v4 payload already present,
	// no instant exists at which a concurrent old binary sees a lone legacy
	// sentinel and routes itself into a legacy-resume cell.
	var order []string
	SyncStateIOFault = func(op, path string) error {
		if op == SyncIOWriteReparentCompat {
			order = append(order, filepath.Base(path))
		}
		return nil
	}
	t.Cleanup(func() { SyncStateIOFault = nil })

	if err := WriteReparentCompatArtifacts(external, st, []string{"pr2"}); err != nil {
		t.Fatal(err)
	}
	// durableWriteFile consults the seam twice per write, so the first
	// occurrence of each base name is the ordering evidence.
	var firstSeen []string
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true
		firstSeen = append(firstSeen, name)
	}
	want := []string{filepath.Base(payloadPath), filepath.Base(sentinelPath)}
	if len(firstSeen) != 2 || firstSeen[0] != want[0] || firstSeen[1] != want[1] {
		t.Fatalf("compatibility write order = %v, want %v", firstSeen, want)
	}

	// The v4 payload must be rejected by every shipped loader.
	if _, err := LoadSyncRunState(external.FeaturePath); err == nil {
		t.Fatal("the shipped scoped-sync loader must fail closed on state_version 4")
	} else if !strings.Contains(err.Error(), "4") {
		t.Fatalf("old-loader failure does not name the version: %v", err)
	}

	payloadInfo, err := os.Stat(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := payloadInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("v4 payload mode = %o, want 0600", perm)
	}
	sentinelInfo, err := os.Stat(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := sentinelInfo.Mode().Perm(); perm != 0o644 {
		t.Fatalf("legacy sentinel mode = %o, want the shipped 0644", perm)
	}

	raw, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state_version: 4", "route: reparent", "reparent_marker: " + ReparentCompatMarker} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("v4 payload lacks %q:\n%s", want, raw)
		}
	}
	sentinelRaw, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sentinelRaw), "guarded_state_version: 3") {
		t.Fatalf("sentinel must reproduce the shipped marker shape:\n%s", sentinelRaw)
	}
	if !strings.Contains(string(sentinelRaw), "reparent_run_id: "+st.RunID) {
		t.Fatalf("sentinel must carry reparent identity:\n%s", sentinelRaw)
	}

	status := ClassifyReparentCompatArtifacts(external, st.RunID)
	if !status.Complete || status.Foreign {
		t.Fatalf("own artifacts classified as %+v", status)
	}
	if err := RemoveReparentCompatArtifacts(external, st.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(payloadPath); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the v4 payload")
	}
	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the legacy sentinel")
	}
}

func TestReparentCheckoutCompatTransactionIsDeliberatelyUndecodable(t *testing.T) {
	_, checkout := newReparentStateFixture(t)
	st := reparentStateFixture(checkout)

	if err := WriteReparentCompatArtifacts(checkout, st, []string{"pr2"}); err != nil {
		t.Fatal(err)
	}
	path := CheckoutTransactionPath(checkout.FeaturePath)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("compatibility transaction mode = %o, want 0600", perm)
	}

	// HasCheckoutTransaction is a bare os.Stat, so plain `tws sync` still
	// refuses on an existing transaction exactly as today...
	if !HasCheckoutTransaction(checkout.FeaturePath) {
		t.Fatal("HasCheckoutTransaction must still see the compatibility transaction")
	}
	// ...while LoadCheckoutTransaction must FAIL, which is what makes both
	// ContinueCheckoutSync and AbortCheckoutSync bail out before the lock,
	// before `git rebase --abort`, before restoreOriginal and before deleting
	// anything.
	if _, err := LoadCheckoutTransaction(checkout.FeaturePath); err == nil {
		t.Fatal("the compatibility transaction must not decode as a CheckoutTransaction")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `state_version: "4-reparent"`) {
		t.Fatalf("state_version must be a STRING where every shipped binary declares an int:\n%s", raw)
	}
	for _, forbidden := range []string{"original_branch", "original_head"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("%q must live only in the authoritative artifact:\n%s", forbidden, raw)
		}
	}

	// Its own run detects it by raw marker, never by decoding it.
	if status := ClassifyReparentCompatArtifacts(checkout, st.RunID); !status.Complete {
		t.Fatalf("own checkout artifact classified as %+v", status)
	}
	if status := ClassifyReparentCompatArtifacts(checkout, "0000"); !status.Foreign {
		t.Fatal("an artifact from another run must classify foreign")
	}
}

func TestReparentCompatArtifactMissingIsDetected(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-068",
		"compat-missing-recovery",
		"owned-missing-is-not-fatal",
	)
	_ = "asserts AC-080"
	external, _ := newReparentStateFixture(t)
	st := reparentStateFixture(external)
	if err := WriteReparentCompatArtifacts(external, st, []string{"pr2"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(SyncStatePath(external.FeaturePath)); err != nil {
		t.Fatal(err)
	}
	status := ClassifyReparentCompatArtifacts(external, st.RunID)
	if status.Complete {
		t.Fatal("a removed sentinel must make the envelope incomplete")
	}
	if len(status.Missing) != 1 || filepath.Base(status.Missing[0]) != ".sync-state.yaml" {
		t.Fatalf("missing set = %v", status.Missing)
	}
	owner := &ReparentExclusionOwner{
		RunID: st.RunID, OwnerToken: st.OwnerToken, OwnerPID: st.OwnerPID,
	}
	if detail := ReparentForeignSyncStateOwned(external, owner); detail != "" {
		t.Fatalf("owned missing compatibility must be automatic recovery work, got %q", detail)
	}
}

func TestRemoveReparentCompatArtifactsRefusesForeignState(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	if err := os.WriteFile(SyncRunStatePath(external.FeaturePath), []byte("state_version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveReparentCompatArtifacts(external, strings.Repeat("a", 32)); err == nil {
		t.Fatal("foreign sync state is somebody else's exclusion, never this run's residue")
	}
}

// ---------------------------------------------------------------------------
// §11.2 — mutual exclusion precedence.
// ---------------------------------------------------------------------------

func TestRefuseIfReparentActive(t *testing.T) {
	external, _ := newReparentStateFixture(t)

	if st, err := RefuseIfReparentActive(external, ReparentRouteVerbFresh); err != nil || st != nil {
		t.Fatalf("a clean feature must admit a fresh run: %v", err)
	}
	if _, err := RefuseIfReparentActive(external, ReparentRouteVerbContinue); err == nil {
		t.Fatal("--continue with no artifact must refuse")
	}

	st := reparentStateFixture(external)
	st.CompatArtifactsWritten = true
	st.SetStage(ReparentStageComputing)
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	_, err := RefuseIfReparentActive(external, ReparentRouteVerbFresh)
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalStatePresent {
		t.Fatalf("fresh route refusal = %v", err)
	}
	if !strings.Contains(refusal.Detail, "--continue") {
		t.Fatalf("refusal must name the recovery verb: %s", refusal.Detail)
	}
	if got, err := RefuseIfReparentActive(external, ReparentRouteVerbContinue); err != nil || got == nil {
		t.Fatalf("--continue must adopt its own run: %v", err)
	}

	st.SetStage(ReparentStageAborting)
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	_, err = RefuseIfReparentActive(external, ReparentRouteVerbContinue)
	if !asReparentRefusal(err, &refusal) || refusal.Kind != ReparentRefusalStatePresent {
		t.Fatalf("--continue against an aborting run = %v", err)
	}
	if !strings.Contains(refusal.Detail, "--abort") {
		t.Fatalf("an aborting run must tell the operator to re-run --abort: %s", refusal.Detail)
	}
	if _, err := RefuseIfReparentActive(external, ReparentRouteVerbAbort); err != nil {
		t.Fatalf("--abort must resume its own rollback: %v", err)
	}
}

// ---------------------------------------------------------------------------
// §11.10 — the read-only projection.
// ---------------------------------------------------------------------------

func TestReparentProjectionStatuses(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	if BuildReparentProjection(external) != nil {
		t.Fatal("no artifact must project ABSENT, so the key stays absent from every document")
	}

	st := reparentStateFixture(external)
	st.CompatArtifactsWritten = true
	st.SetStage(ReparentStageComputing)
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	p := BuildReparentProjection(external)
	if p == nil || p.Status != ReparentArtifactActive {
		t.Fatalf("live-owner projection = %+v", p)
	}
	line := p.ObservabilityLine()
	for _, want := range []string{"reparent active: customer", "target pr2", "run " + st.RunID, "stage computing", "owner pid"} {
		if !strings.Contains(line, want) {
			t.Fatalf("observability line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "--continue") || strings.Contains(line, "--abort") {
		t.Fatalf("live-owner projection must not offer recovery guidance: %s", line)
	}

	st.OwnerPID = reparentSpawnDeadPID(t)
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	if p := BuildReparentProjection(external); p == nil || p.Status != ReparentArtifactStale {
		t.Fatalf("dead-owner projection = %+v", p)
	}

	st = reparentStateFixtureAtCommitPoint(t, external)
	st.OwnerPID = os.Getpid()
	st.ResumeStage = ReparentStageCleanup
	st.Stage = ReparentStageCompleted
	if err := SaveReparentState(external, st); err != nil {
		t.Fatal(err)
	}
	if p := BuildReparentProjection(external); p == nil || p.Status != ReparentArtifactComplete {
		t.Fatalf("completed projection = %+v", p)
	}

	if err := os.WriteFile(ReparentStatePath(external), []byte("::not yaml::"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := BuildReparentProjection(external); p == nil || p.Status != ReparentArtifactCorrupt {
		t.Fatalf("corrupt projection = %+v", p)
	}
}

// ---------------------------------------------------------------------------
// §6.2 — foreign sync state, and this run's own envelope not counting as one.
// ---------------------------------------------------------------------------

func TestReparentForeignSyncState(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	if detail := ReparentForeignSyncState(external, ""); detail != "" {
		t.Fatalf("clean feature reported %q", detail)
	}
	if err := os.WriteFile(SyncStatePath(external.FeaturePath), []byte("started_at: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if detail := ReparentForeignSyncState(external, ""); detail == "" {
		t.Fatal("a legacy sync sentinel must refuse a fresh reparent")
	}
	if err := os.Remove(SyncStatePath(external.FeaturePath)); err != nil {
		t.Fatal(err)
	}

	st := reparentStateFixture(external)
	if err := WriteReparentCompatArtifacts(external, st, []string{"pr2"}); err != nil {
		t.Fatal(err)
	}
	if detail := ReparentForeignSyncState(external, st.RunID); detail != "" {
		t.Fatalf("a run's OWN compatibility envelope is not foreign state: %s", detail)
	}
	if detail := ReparentForeignSyncState(external, "ffff"); detail == "" {
		t.Fatal("another run's envelope must still refuse")
	}
}

// ---------------------------------------------------------------------------
// §11.6b — the reclaim ladder's typed verdict (lock fresh/recovery/liveness).
// ---------------------------------------------------------------------------

func TestReclaimCheckoutLockLivenessLadder(t *testing.T) {
	_ = "asserts AC-081"
	_, checkout := newReparentStateFixture(t)
	featurePath := checkout.FeaturePath

	if err := ReclaimCheckoutLock(featurePath); err != nil {
		t.Fatalf("an absent lock must be takeable: %v", err)
	}
	if err := ReclaimCheckoutLock(featurePath); err != nil {
		t.Fatalf("our own lock must be reclaimable: %v", err)
	}

	dead := reparentSpawnDeadPID(t)
	if err := os.WriteFile(CheckoutLockPath(featurePath),
		[]byte("pid: "+itoaForTest(dead)+"\nhost: test\nacquired_at: now\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReclaimCheckoutLock(featurePath); err != nil {
		t.Fatalf("a dead PID's lock must be reclaimable: %v", err)
	}

	live := reparentSpawnLivePID(t)
	if err := os.WriteFile(CheckoutLockPath(featurePath),
		[]byte("pid: "+itoaForTest(live)+"\nhost: test\nacquired_at: now\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ReclaimCheckoutLock(featurePath)
	if err == nil {
		t.Fatal("a LIVE foreign lock is never stolen")
	}
	var typed *CheckoutLockLiveError
	if !errorsAsCheckoutLockLive(err, &typed) {
		t.Fatalf("the live-foreign case must be classifiable by type, got %T", err)
	}
	if typed.PID != live {
		t.Fatalf("typed verdict pid = %d, want %d", typed.PID, live)
	}
	// The message is byte-identical to the sentence that arm has always
	// produced, so every existing caller sees exactly the same bytes.
	if got, want := err.Error(), "lock held by live process "+itoaForTest(live)+"; cannot reclaim"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}

}

func itoaForTest(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// ---------------------------------------------------------------------------
// §11.2 — the sentinel must be classified as a per-run SENTINEL by every
// shipped binary, not as a resumable legacy run.
// ---------------------------------------------------------------------------

func TestReparentSentinelSatisfiesShippedMarkerPattern(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	st := reparentStateFixture(external)
	if err := WriteReparentCompatArtifacts(external, st, []string{"pr2"}); err != nil {
		t.Fatal(err)
	}

	marker := reparentCompatSentinelMarker(st.RunID)
	if !isSyncMarker(marker) {
		t.Fatalf("marker %q does not satisfy the shipped syncMarkerRe", marker)
	}

	// Cell 6 = legacySentinel(4) + payloadUnreadable(2): the sentinel is
	// recognised as a marker AND the v4 payload refuses to decode. Every
	// shipped verb lands in a refusing cell rather than a legacy resume.
	state := ClassifyExternalSyncState(external.FeaturePath, SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 6 {
		t.Fatalf("payload + sentinel classified as cell %d, want 6", state.Cell)
	}
	if state.Marker != marker {
		t.Fatalf("classifier marker = %q, want %q", state.Marker, marker)
	}
	if state.Legacy == nil || state.Legacy.FailedBranch != marker {
		t.Fatalf("legacy sentinel = %+v", state.Legacy)
	}
	if state.PayloadErr == nil {
		t.Fatal("the v4 payload must be unreadable to every shipped loader")
	}

	// Cell 4 = legacySentinel(4) + payloadAbsent(0): a foreign or older binary
	// removed the payload and left the sentinel. It is STILL a sentinel, never
	// a legacy run with pending work.
	if err := os.Remove(SyncRunStatePath(external.FeaturePath)); err != nil {
		t.Fatal(err)
	}
	state = ClassifyExternalSyncState(external.FeaturePath, SyncClassifyOpts{AlwaysReadGuard: true})
	if state.Cell != 4 {
		t.Fatalf("sentinel-only classified as cell %d, want 4", state.Cell)
	}
	if state.Legacy == nil || len(state.Legacy.Pending) != 0 {
		t.Fatalf("the sentinel must carry no pending work: %+v", state.Legacy)
	}

	// The sentinel's reparent identity is preserved beside the shipped shape.
	raw, err := os.ReadFile(SyncStatePath(external.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reparent_marker: " + ReparentCompatMarker, "reparent_run_id: " + st.RunID} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("sentinel lost %q:\n%s", want, raw)
		}
	}
	// And the run still recognises the sentinel as its own.
	if status := ClassifyReparentCompatArtifacts(external, st.RunID); status.Foreign {
		t.Fatal("the run's own sentinel must never classify foreign")
	}
}
