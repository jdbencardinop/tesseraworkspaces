package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdbencardinop/tesseraworkspaces/internal"

	"gopkg.in/yaml.v3"
)

type reparentTmuxProbe struct{ snapshot internal.TmuxSnapshot }

func (p reparentTmuxProbe) Snapshot() internal.TmuxSnapshot { return p.snapshot }

// Acceptance criteria carried by this file's cells: AC-038 (the scope
// refusals), AC-039 and AC-040 (same-parent no-work and the stale-edge
// guidance), AC-068 (the session launch exclusion), AC-079 (sync <-> reparent
// mutual exclusion in both modes), AC-080 (compat-artifact-missing and the
// §11.2a re-creation proofs), AC-083 (the external compat write order).

// ---------------------------------------------------------------------------
// T-027 — dirty / git-operation / holder refusals at the command boundary.
// T-029 — the no-work route.
// T-030 — the same-parent-stale route.
// T-056 — launch exclusion.
// T-067 — sync mutual exclusion on every verb.
// T-068 — the compatibility artifacts.
// T-071 — the compatibility write order and modes.
//
// These cells cover the two directions §11.2 and §14.2a define: a shipped verb
// must never reach a reparent run's compatibility artifacts, and a session must
// never start on top of a run that has detached holders and is about to move
// the very branches the session would sit on.
// ---------------------------------------------------------------------------

// reparentPlantState writes a realistic in-progress artifact for a feature
// without executing anything, so an exclusion cell costs one file write rather
// than a whole transaction. The stage is deliberately mid-run and the owner is
// this live process.
func reparentPlantState(t *testing.T, f *reparentFixture, stage internal.ReparentStage) *internal.ReparentState {
	t.Helper()
	loc := f.Loc()
	value := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	onto := "master"
	if loc.Mode == internal.ModeCheckout {
		onto = "main"
	}
	limit := 50
	route := &reparentRoute{
		Ws: ws, Loc: loc, Feature: f.Feature, Target: "pr2",
		Onto: onto, OntoKind: "auto", Policy: internal.SyncFetchDisabled,
		Guard: internal.CheckoutPlanGuard{
			MaxTotal: &limit, Present: map[string]bool{"max-replay-total": true},
		},
	}
	in := route.planInput(internal.ReparentRouteFresh, internal.ReparentInvocationPlanOnly, internal.PlanFetchOutcome{})
	owner := &internal.ReparentExclusionOwner{OwnerPID: os.Getpid()}
	if loc.Mode == internal.ModeExternal {
		if data, readErr := os.ReadFile(internal.SyncRunGuardPath(loc.FeaturePath)); readErr == nil {
			guard, guardErr := internal.ReadSyncRunGuard(loc.FeaturePath)
			if guardErr != nil {
				t.Fatal(guardErr)
			}
			owner.OwnerToken, owner.LockBytes = guard.Token, data
			in.ExclusionOwner = owner
		}
	} else {
		featureBytes, featureErr := os.ReadFile(internal.CheckoutLockPath(loc.FeaturePath))
		globalBytes, globalErr := os.ReadFile(internal.CheckoutMutationLockPath(loc.CheckoutStateDir))
		if featureErr == nil && globalErr == nil {
			lock, _, readErr := internal.ReadCheckoutMutationLock(loc.CheckoutStateDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			owner.OwnerToken = lock.Token
			owner.LockBytes = featureBytes
			owner.GlobalLockBytes = globalBytes
			in.ExclusionOwner = owner
		}
	}
	plan, req, err := internal.PlanReparent(in)
	if err != nil || plan.Approval.Fingerprint == nil {
		t.Fatalf("plant state plan: %v blockers=%+v", err, plan.Blockers)
	}
	stackBytes := req.StackBytes
	if len(stackBytes) == 0 {
		stackBytes, err = os.ReadFile(internal.StackPath(loc.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
	}
	stackHash := sha256.Sum256(stackBytes)
	runID := strings.Repeat("b", 32)
	repoRoot := req.TargetRepoRoot
	if repoRoot == "" {
		repoRoot = f.Repo
	}
	commonDir := req.Rows[0].CommonDir
	if commonDir == "" {
		commonDir = f.reparentGit(repoRoot, "rev-parse", "--git-common-dir")
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(repoRoot, commonDir)
		}
		commonDir, _ = filepath.Abs(commonDir)
	}
	st := &internal.ReparentState{
		StateVersion: internal.ReparentStateVersion, RunID: runID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		WorkspaceMode: string(loc.Mode), WorkspaceStableID: loc.StableID,
		Feature: f.Feature, WorkspaceRepoRoot: ws.RepoRoot,
		RepoRoot: repoRoot, RepoCommonDir: commonDir,
		RefBackend: req.RefBackend, OIDWidth: req.OIDWidth,
		Strategy: internal.ReparentStateStrategy, Backend: internal.ReparentStateBackend,
		OwnerPID: os.Getpid(), OwnerToken: strings.Repeat("c", 32),
		TargetName: plan.Target.Name, TargetGitBranch: plan.Target.GitBranch,
		OldParentRequestedToken: value(plan.Target.OldParent.RequestedToken),
		OldParentStoredToken:    value(plan.Target.OldParent.StoredToken),
		OldParentKind:           plan.Target.OldParent.Kind, OldParentRef: value(plan.Target.OldParent.Ref),
		OldParentSHA:            value(plan.Target.OldParent.SHA),
		NewParentRequestedToken: value(plan.Target.NewParent.RequestedToken),
		NewParentStoredToken:    value(plan.Target.NewParent.StoredToken),
		NewParentKind:           plan.Target.NewParent.Kind, NewParentRef: value(plan.Target.NewParent.Ref),
		NewParentSHA:      value(plan.Target.NewParent.SHA),
		DestinationPinRef: "refs/tws/reparent/" + runID + "/dest",
		ValidationSource:  "none", OriginalBranch: req.OriginalBranch,
		OriginalHead: req.OriginalHead, OriginalDetached: req.OriginalDetached,
		StackBeforeBase64:          base64.StdEncoding.EncodeToString(stackBytes),
		StackSHA256Before:          hex.EncodeToString(stackHash[:]),
		RemoteRecordBeforeCaptured: true,
		CompatArtifactsWritten:     true,
		ApprovedFingerprint:        *plan.Approval.Fingerprint,
		FetchPolicy:                string(internal.SyncFetchDisabled),
	}
	if st.OriginalDetached {
		st.OriginalHeadPinRef = "refs/tws/reparent/" + runID + "/original-head"
	}
	if loc.Mode == internal.ModeExternal {
		featureCanonical, _ := filepath.EvalSymlinks(loc.FeaturePath)
		st.ScratchPath = filepath.Join(featureCanonical, ".reparent", runID, "scratch")
	}
	rows := append([]internal.ReparentPlanRow{plan.Target}, plan.Descendants...)
	for _, row := range rows {
		id := internal.ReparentEntryRefID(f.Feature, row.Name)
		digest, _ := internal.ReparentRevalidationDigest(row)
		var metadata internal.ReparentPlanMetadataEntry
		for _, candidate := range plan.MetadataDelta.Entries {
			if candidate.Name == row.Name {
				metadata = candidate
				break
			}
		}
		count := 0
		if row.Replay.CandidateCount != nil {
			count = *row.Replay.CandidateCount
		}
		st.Rows = append(st.Rows, internal.ReparentStateRow{
			Order: row.Order, Name: row.Name, GitBranch: row.GitBranch, Repo: row.Repo, Role: row.Role,
			PreimageSHA: value(row.Head.SHA), CutoffSHA: value(row.Cutoff.ResolvedSHA),
			CutoffProvenance:   row.Cutoff.Provenance,
			DestinationBinding: row.DestinationBinding, DestinationParent: value(row.DestinationParent),
			DestinationSHA: value(row.DestinationSHA),
			BaseBefore:     metadata.BaseBefore, BaseAfter: metadata.BaseAfter,
			LastBaseSHABefore: value(metadata.LastBaseSHABefore),
			LastBaseSHAAfter:  value(metadata.LastBaseSHAAfter),
			Argv:              append([]string{}, row.Argv...), EffectiveBackend: row.EffectiveBackend,
			CandidateCount: count, CandidateDigest: value(row.Replay.CandidateDigest),
			RevalidationDigest: digest,
			OldPinRef:          "refs/tws/reparent/" + runID + "/old/" + id,
			NewPinRef:          "refs/tws/reparent/" + runID + "/new/" + id,
			Stage:              internal.ReparentRowPending,
		})
	}
	for _, holder := range req.Holders {
		kind := holder.HolderKind
		if holder.Excluded {
			kind = "computation-context"
		}
		common := f.reparentGit(holder.HolderPath, "rev-parse", "--git-common-dir")
		if !filepath.IsAbs(common) {
			common = filepath.Join(holder.HolderPath, common)
		}
		common, _ = filepath.EvalSymlinks(common)
		st.ApprovedHolders = append(st.ApprovedHolders, internal.ReparentApprovedHolder{
			Path: holder.HolderPath, RepoCommonDir: common,
			GitBranch: holder.GitBranch, HeadSHA: holder.PreimageSHA,
			HolderKind: kind, Excluded: holder.Excluded,
		})
	}
	advancePlantedReparentState(t, st, stackBytes, stage)
	if err := internal.SaveReparentState(loc, st); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = internal.RemoveReparentState(loc) })
	return st
}

func advancePlantedReparentState(
	t *testing.T,
	st *internal.ReparentState,
	stackBytes []byte,
	stage internal.ReparentStage,
) {
	t.Helper()
	rank := func(candidate internal.ReparentStage) int {
		for i, known := range internal.ReparentStages {
			if known == candidate {
				return i
			}
		}
		return -1
	}
	if rank(stage) >= rank(internal.ReparentStageBuildingPostImage) {
		for i := range st.Rows {
			row := &st.Rows[i]
			row.PlannedNewSHA = row.PreimageSHA
			row.Noop = true
			if row.DestinationBinding == internal.ReparentBindingParentComputed {
				for _, parent := range st.Rows {
					if parent.Name == row.DestinationParent {
						row.DestinationSHA = parent.PlannedNewSHA
						row.LastBaseSHAAfter = parent.PlannedNewSHA
						break
					}
				}
			}
			row.MaterializedArgv = append([]string{}, row.Argv...)
			for j, arg := range row.MaterializedArgv {
				if strings.HasPrefix(arg, "<computed-tip:") {
					row.MaterializedArgv[j] = row.DestinationSHA
				}
			}
			row.Stage = internal.ReparentRowValidated
		}
		var post internal.Stack
		if err := yaml.Unmarshal(stackBytes, &post); err != nil {
			t.Fatal(err)
		}
		for i := range post.Branches {
			for _, row := range st.Rows {
				if post.Branches[i].Name == row.Name {
					post.Branches[i].Base = row.BaseAfter
					post.Branches[i].LastBaseSHA = row.LastBaseSHAAfter
				}
			}
		}
		postBytes, err := yaml.Marshal(&post)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(postBytes)
		st.StackAfterBase64 = base64.StdEncoding.EncodeToString(postBytes)
		st.StackSHA256AfterExpected = hex.EncodeToString(sum[:])
	}
	if rank(stage) >= rank(internal.ReparentStageDetachingHolders) {
		st.RemoteRecordEmpty = true
	}
	if rank(stage) >= rank(internal.ReparentStageRefsCommitted) {
		st.CASTransactionSucceeded = true
		for i := range st.Rows {
			row := &st.Rows[i]
			row.Stage = internal.ReparentRowCommitted
			st.CASRows = append(st.CASRows, internal.ReparentStateCASRow{
				Ref: "refs/heads/" + row.GitBranch, OldValue: row.PreimageSHA,
				NewValue: row.PlannedNewSHA, Applied: true, Noop: row.Noop,
			})
		}
	}
	if rank(stage) >= rank(internal.ReparentStageWritingMetadata) {
		st.StackSHA256After = st.StackSHA256AfterExpected
	}
	if rank(stage) >= rank(internal.ReparentStageMetadataWritten) {
		st.CommitPointReached = true
	}
	if stage == internal.ReparentStageCompleted {
		st.ResumeStage = internal.ReparentStageCleanup
		st.Stage = stage
		return
	}
	st.SetStage(stage)
}

// TestReparentExclusion_SyncRefusesEveryVerb is T-067 and AC-079. The
// pre-check sits above mode dispatch, above plan dispatch and above state
// classification, so all four sync routes refuse in BOTH modes — and they
// refuse with the reparent-aware sentence, not with a version or decode error
// about the compatibility files the run deliberately wrote.
func TestReparentExclusion_SyncRefusesEveryVerb(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-067",
		"sync-reparent-exclusion",
		"external-sync-guard-handshake",
	)
	for _, mode := range []string{"external", "checkout"} {
		t.Run(mode, func(t *testing.T) {
			var f *reparentFixture
			if mode == "external" {
				f = newReparentCustomerExternal(t)
			} else {
				f = newReparentCustomerCheckout(t)
			}
			st := reparentPlantState(t, f, internal.ReparentStageComputing)

			for _, verb := range [][]string{
				{},
				{"--plan"},
				{"--continue"},
				{"--abort"},
			} {
				name := "plain"
				if len(verb) > 0 {
					name = verb[0]
				}
				t.Run(name, func(t *testing.T) {
					args := append([]string{f.Feature}, verb...)
					stdout, stderr := syncCaptureStreams(t, func() {
						if code := syncExecute(syncCmd, args...); code != 1 {
							t.Errorf("exit = %d, want 1", code)
						}
					})
					want := "a stack reparent is in progress for \"" + f.Feature + "\""
					if !strings.Contains(stderr, want) {
						t.Fatalf("stderr must carry the reparent-aware sentence, got:\n%s", stderr)
					}
					if !strings.Contains(stderr, "run "+st.RunID) || !strings.Contains(stderr, "stage "+string(st.Stage)) {
						t.Fatalf("the sentence must name the run and stage, got:\n%s", stderr)
					}
					if !strings.Contains(stderr, "tws stack reparent "+f.Feature+" --continue") {
						t.Fatalf("the sentence must name the recovery verb, got:\n%s", stderr)
					}
					for _, forbidden := range []string{
						"unsupported scoped sync state version",
						"no transaction to continue",
						"no transaction to abort",
					} {
						if strings.Contains(stderr, forbidden) {
							t.Fatalf("a lower-level compatibility error leaked: %q\n%s", forbidden, stderr)
						}
					}
					if stdout != "" {
						t.Fatalf("a refused sync must produce no document, got:\n%s", stdout)
					}
				})
			}
			if mode == "external" {
				if err := internal.RemoveReparentState(f.Loc()); err != nil {
					t.Fatal(err)
				}
				in, plan, req := reparentApprovedBeginInput(t, f, "pr2", "master")
				assertReparentBlocked := func(where string) {
					t.Helper()
					run, err := internal.BeginReparentRun(internal.ReparentBeginInput{
						Input: in, Plan: plan, Request: req,
					})
					var refusal *internal.ReparentRefusalError
					if run != nil || !errors.As(err, &refusal) ||
						refusal.Kind != internal.ReparentRefusalSyncStatePresent {
						t.Fatalf("%s reparent race = run %+v err %v", where, run, err)
					}
					if internal.HasReparentState(f.Loc()) {
						t.Fatalf("%s reparent race wrote authoritative state", where)
					}
				}
				assertGuardHeld := func(where string) {
					t.Helper()
					guard, err := internal.ReadSyncRunGuard(f.FeaturePath)
					if err != nil || guard.PID != os.Getpid() || guard.Token == "" {
						t.Fatalf("%s shared feature guard = %+v (%v)", where, guard, err)
					}
				}
				assertGuardReleased := func(where string) {
					t.Helper()
					if _, err := os.Lstat(internal.SyncRunGuardPath(f.FeaturePath)); !os.IsNotExist(err) {
						t.Fatalf("%s left the shared feature guard: %v", where, err)
					}
				}

				guardHookCalls := 0
				t.Cleanup(func() {
					ExternalSyncMutationGuardHook = nil
					TopLevelPushEntryBarrier = nil
				})

				refsBefore := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
				stackBefore, err := os.ReadFile(internal.StackPath(f.FeaturePath))
				if err != nil {
					t.Fatal(err)
				}
				ExternalSyncMutationGuardHook = func(featurePath string) error {
					if featurePath != f.FeaturePath {
						t.Fatalf("post-lock recheck guarded %s, want %s", featurePath, f.FeaturePath)
					}
					reparentPlantState(t, f, internal.ReparentStageInitializing)
					return nil
				}
				_, stderr := syncCaptureStreams(t, func() {
					if code := syncExecute(syncCmd, f.Feature); code != 1 {
						t.Fatalf("post-lock recheck exit = %d", code)
					}
				})
				ExternalSyncMutationGuardHook = nil
				if !strings.Contains(stderr, "a stack reparent is in progress") {
					t.Fatalf("post-lock recheck stderr = %q", stderr)
				}
				if got := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsBefore {
					t.Fatal("post-lock reparent recheck allowed a ref mutation")
				}
				if got, err := os.ReadFile(internal.StackPath(f.FeaturePath)); err != nil || string(got) != string(stackBefore) {
					t.Fatalf("post-lock reparent recheck changed stack.yaml: %v", err)
				}
				assertGuardReleased("post-lock refusal")
				if err := internal.RemoveReparentState(f.Loc()); err != nil {
					t.Fatal(err)
				}

				ExternalSyncMutationGuardHook = func(featurePath string) error {
					guardHookCalls++
					if featurePath != f.FeaturePath {
						t.Fatalf("ordinary sync guarded %s, want %s", featurePath, f.FeaturePath)
					}
					assertGuardHeld("ordinary sync")
					assertReparentBlocked("ordinary sync")
					return nil
				}
				_, _ = syncCaptureStreams(t, func() {
					if code := syncExecute(syncCmd, f.Feature); code != 0 {
						t.Fatalf("ordinary sync exit = %d", code)
					}
				})
				ExternalSyncMutationGuardHook = nil
				if guardHookCalls != 1 {
					t.Fatalf("ordinary sync guard hook calls=%d", guardHookCalls)
				}
				assertGuardReleased("ordinary sync")

				pushBarriers := 0
				TopLevelPushEntryBarrier = func(index int, entry internal.StackEntry) error {
					pushBarriers++
					assertGuardHeld(fmt.Sprintf("sync --push entry %d (%s)", index, entry.Name))
					if index == 0 {
						assertReparentBlocked("sync --push")
					}
					return nil
				}
				_, _ = syncCaptureStreams(t, func() {
					if code := syncExecute(syncCmd, f.Feature, "--push"); code != 0 {
						t.Fatalf("sync --push exit = %d", code)
					}
				})
				TopLevelPushEntryBarrier = nil
				if pushBarriers == 0 {
					t.Fatal("sync --push reached no protected push barrier")
				}
				assertGuardReleased("sync --push")
			}
		})
	}
}

// TestReparentExclusion_SyncIsUnaffectedWithoutAnArtifact is AC-099's process
// half at this seam: with no reparent artifact the pre-check adds one os.Stat
// and no Git child, so a plain sync behaves exactly as it always did.
func TestReparentExclusion_SyncIsUnaffectedWithoutAnArtifact(t *testing.T) {
	f := newReparentCustomerExternal(t)
	stdout, stderr := syncCaptureStreams(t, func() {
		_ = syncExecute(syncCmd, f.Feature, "--no-fetch")
	})
	if strings.Contains(stdout+stderr, "a stack reparent is in progress") {
		t.Fatalf("the pre-check must be silent without an artifact:\n%s\n%s", stdout, stderr)
	}
}

func TestReparentExternalSyncGuardSurvivesGuardedPushes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{name: "guarded-legacy"},
		{name: "guarded-scoped", flags: []string{"--no-fetch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReparentCustomerExternal(t)
			planArgs := append([]string{f.Feature}, tc.flags...)
			planArgs = append(planArgs, "--push", "--plan", "--json", "--max-replay-total", "50")
			stdout, stderr, exit := runSyncExecute(t, planArgs...)
			if exit != 0 {
				t.Fatalf("plan exit = %d stderr=%q", exit, stderr)
			}
			fingerprint := planFieldString(t, stdout, "approval", "fingerprint")
			if fingerprint == "" {
				t.Fatal("guarded push plan minted no fingerprint")
			}

			barriers := 0
			TopLevelPushEntryBarrier = func(index int, entry internal.StackEntry) error {
				barriers++
				if _, err := internal.ReadSyncRunGuard(f.FeaturePath); err != nil {
					t.Fatalf("push entry %d (%s) has no shared feature guard: %v", index, entry.Name, err)
				}
				if !internal.HasSyncRunState(f.FeaturePath) || !internal.HasSyncState(f.FeaturePath) {
					t.Fatalf("push entry %d (%s) ran after guarded state teardown", index, entry.Name)
				}
				return nil
			}
			t.Cleanup(func() { TopLevelPushEntryBarrier = nil })

			execArgs := append([]string{f.Feature}, tc.flags...)
			execArgs = append(execArgs, "--push", "--max-replay-total", "50", "--approve-plan", fingerprint)
			_, stderr, exit = runSyncExecute(t, execArgs...)
			TopLevelPushEntryBarrier = nil
			if exit != 0 {
				t.Fatalf("execute exit = %d stderr=%q", exit, stderr)
			}
			if barriers == 0 {
				t.Fatal("guarded sync reached no push barrier")
			}
			for _, path := range []string{
				internal.SyncRunGuardPath(f.FeaturePath),
				internal.SyncRunStatePath(f.FeaturePath),
				internal.SyncStatePath(f.FeaturePath),
			} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("guarded sync left %s after push and teardown: %v", path, err)
				}
			}
		})
	}
}

// TestReparentExclusion_FreshRouteRefusesAnExistingRun is §11.2's own matrix
// row: on the fresh route ANY artifact refuses, and the refusal names both
// recovery verbs.
// The fresh route refuses a recorded run, a recovery verb refuses without one, and a no-work plan is still a produced plan (AC-039, AC-040, AC-079).
//
// §17.3 counts one t.Run leaf per real-Git cell. These assertions were
// separate leaves; each keeps its own fixture, scope and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentExclusion_RouteRefusals(t *testing.T) {
	// --- TestReparentExclusion_FreshRouteRefusesAnExistingRun ---
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		reparentPlantState(t, f, internal.ReparentStageComputing)

		_, stderr, exit := f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch",
			"--max-replay-total", "5", "--approve-plan", strings.Repeat("a", 64))
		if exit != 1 {
			t.Fatalf("exit = %d, want 1: %s", exit, stderr)
		}
		if !reparentMarkerRe.MatchString(stderr) {
			t.Fatalf("an execution refusal must be one anchored line:\n%s", stderr)
		}
		if !strings.Contains(stderr, "reparent: reparent-state-present: ") {
			t.Fatalf("stderr = %q, want the state-present refusal", stderr)
		}
	}(t)
}

// TestReparentExclusion_DirtyGitOpAndHolderBoundaries owns T-027 / AC-038.
// Every affected holder is inspected at the command boundary, not only the
// repository root.
func TestReparentExclusion_DirtyGitOpAndHolderBoundaries(t *testing.T) {
	_ = "asserts AC-038 AC-039 AC-040"
	assertReparentMatrixBehavior(t, "T-027", "dirty-gitop-holder-boundaries")
	assertReparentMatrixBehavior(t, "T-029", "live-and-undecodable-sessions")
	assertReparentMatrixBehavior(t, "T-030", "same-parent-no-work-and-stale")
	f := newReparentCustomerExternal(t)
	holder := internal.WorktreePath(f.Feature, "pr2")
	stackBefore, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")

	assertBlocker := func(want internal.ReparentRefusalKind) {
		t.Helper()
		stdout, _, exit := f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("a refusing plan must still exit 0")
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, blocker := range plan.Blockers {
			if blocker.Kind == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("blockers = %+v, want %s", plan.Blockers, want)
		}
		if got := f.reparentGit(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != refsBefore {
			t.Fatal("a boundary refusal moved a ref")
		}
		after, err := os.ReadFile(internal.StackPath(f.FeaturePath))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(stackBefore) {
			t.Fatal("a boundary refusal rewrote stack.yaml")
		}
	}

	if err := os.WriteFile(filepath.Join(holder, "c.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertBlocker(internal.ReparentRefusalContextDirty)
	f.reparentGit(holder, "checkout", "--", "c.txt")

	gitPath := f.reparentGit(holder, "rev-parse", "--git-path", "rebase-merge")
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(holder, gitPath)
	}
	if err := os.MkdirAll(gitPath, 0o755); err != nil {
		t.Fatal(err)
	}
	assertBlocker(internal.ReparentRefusalGitOperationInProgress)
	if err := os.RemoveAll(gitPath); err != nil {
		t.Fatal(err)
	}

	detached := filepath.Join(t.TempDir(), "detached-operation")
	f.reparentGit(f.Repo, "worktree", "add", "--detach", detached, "master")
	detachedGitPath := f.reparentGit(detached, "rev-parse", "--git-path", "rebase-merge")
	if !filepath.IsAbs(detachedGitPath) {
		detachedGitPath = filepath.Join(detached, detachedGitPath)
	}
	if err := os.MkdirAll(detachedGitPath, 0o755); err != nil {
		t.Fatal(err)
	}
	assertBlocker(internal.ReparentRefusalGitOperationInProgress)
	if err := os.RemoveAll(detachedGitPath); err != nil {
		t.Fatal(err)
	}

	second := filepath.Join(t.TempDir(), "duplicate-holder")
	f.reparentGit(f.Repo, "worktree", "add", "--detach", second, "feat-pr2")
	f.reparentGit(second, "switch", "--ignore-other-worktrees", "feat-pr2")
	assertBlocker(internal.ReparentRefusalHolderUnsafe)

	_, stderr, exit := runTWSExecute(t,
		"stack", "reparent", f.Feature, "missing", "--onto", "master", "--no-fetch",
		"--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64))
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if exit != 1 || len(lines) != 1 || !strings.HasPrefix(lines[0], "reparent: target-unknown: ") {
		t.Fatalf("a production reparent refusal must be one anchored line:\n%s", stderr)
	}
	if strings.Contains(stderr, "Error:") || strings.Contains(stderr, "Usage:") {
		t.Fatalf("Cobra must not duplicate the owned refusal:\n%s", stderr)
	}

	testReparentExclusionLiveAndUndecodableSessions(t)
	testReparentExclusionSameParentCurrentAndStale(t)
}

// TestReparentExclusion_LiveAndUndecodableSessions owns T-029 / AC-038.
// External records are closure-scoped; checkout's one physical checkout is
// workspace-global.
func testReparentExclusionLiveAndUndecodableSessions(t *testing.T) {
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		pr1Token, err := internal.CreateDirectSession(f.FeaturePath, internal.DirectSessionRecord{
			Feature: f.Feature, Name: "pr1", GitBranch: "feat-pr1", Path: internal.WorktreePath(f.Feature, "pr1"),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = internal.RemoveOwnedDirectSession(f.FeaturePath, internal.DirectSessionBranchID(f.Feature, "pr1"), pr1Token)
		})
		stdout, _, _ := f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json")
		var allowed internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &allowed); err != nil {
			t.Fatal(err)
		}
		for _, blocker := range allowed.Blockers {
			if blocker.Kind == internal.ReparentRefusalSessionLive {
				t.Fatalf("an unrelated sibling session must not block external reparent: %+v", blocker)
			}
		}

		pr2Token, err := internal.CreateDirectSession(f.FeaturePath, internal.DirectSessionRecord{
			Feature: f.Feature, Name: "pr2", GitBranch: "feat-pr2", Path: internal.WorktreePath(f.Feature, "pr2"),
		})
		if err != nil {
			t.Fatal(err)
		}
		stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json")
		var blocked internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &blocked); err != nil {
			t.Fatal(err)
		}
		if blocked.Refusal.Kind == nil || *blocked.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("live affected session refusal = %+v", blocked.Refusal)
		}
		if err := internal.RemoveOwnedDirectSession(f.FeaturePath, internal.DirectSessionBranchID(f.Feature, "pr2"), pr2Token); err != nil {
			t.Fatal(err)
		}

		badDir := filepath.Join(internal.DirectSessionsDir(f.FeaturePath), internal.DirectSessionBranchID(f.Feature, "pr2"))
		if err := os.MkdirAll(badDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(badDir, strings.Repeat("d", 32)+".json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json")
		if err := json.Unmarshal([]byte(stdout), &blocked); err != nil {
			t.Fatal(err)
		}
		if blocked.Refusal.Kind == nil || *blocked.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("undecodable affected session refusal = %+v", blocked.Refusal)
		}
	}(t)

	func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		otherToken := strings.Repeat("9", 32)
		otherState := filepath.Join(ws.CheckoutStateDir(), "other-checkout-sync.yaml")
		if _, err := internal.AcquireCheckoutMutationLock(ws.CheckoutStateDir(), otherToken, "other", "sync", otherState); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
		var globalBlocked internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &globalBlocked); err != nil {
			t.Fatal(err)
		}
		foundGlobal := false
		for _, blocker := range globalBlocked.Blockers {
			if blocker.Kind == internal.ReparentRefusalSyncStatePresent {
				foundGlobal = true
			}
		}
		if !foundGlobal {
			t.Fatalf("another feature's checkout sync must block reparent: %+v", globalBlocked.Blockers)
		}
		if err := internal.ReleaseCheckoutMutationLock(ws.CheckoutStateDir(), otherToken); err != nil {
			t.Fatal(err)
		}
		if err := internal.SaveCheckoutAgentSession(ws, &internal.CheckoutAgentSession{
			SchemaVersion: 1, WorkspaceID: ws.StableID, Feature: "unrelated",
			Name: "sibling", GitBranch: "sibling", PID: os.Getpid(),
		}); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("unrelated checkout session must block globally: %+v", plan.Refusal)
		}
		active := filepath.Join(ws.MetadataRoot, "state", "sessions", "active.json")
		if err := os.WriteFile(active, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("undecodable checkout session must block: %+v", plan.Refusal)
		}
		if err := os.Remove(active); err != nil {
			t.Fatal(err)
		}

		intentDir := filepath.Join(ws.MetadataRoot, "state", "checkout-session.lock")
		if err := os.MkdirAll(intentDir, 0o700); err != nil {
			t.Fatal(err)
		}
		intent := fmt.Sprintf(`{"token":"%s","pid":%d,"created_at":"2026-09-23T00:00:00Z"}`,
			strings.Repeat("e", 32), os.Getpid())
		if err := os.WriteFile(filepath.Join(intentDir, "owner.json"), []byte(intent), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("checkout launch intent must block reparent: %+v", plan.Refusal)
		}
		if err := os.RemoveAll(intentDir); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(active, active); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("checkout session stat error must block as unverifiable: %+v", plan.Refusal)
		}
	}(t)

	writeDeadCheckoutIntent := func(t *testing.T, ws internal.Workspace) string {
		t.Helper()
		intentDir := internal.CheckoutSessionIntentDir(ws)
		if err := os.MkdirAll(intentDir, 0o700); err != nil {
			t.Fatal(err)
		}
		intent := fmt.Sprintf(`{"token":"%s","pid":%d,"created_at":"2026-09-23T00:00:00Z"}`,
			strings.Repeat("d", 32), spawnDeadPID(t))
		if err := os.WriteFile(filepath.Join(intentDir, "owner.json"), []byte(intent), 0o600); err != nil {
			t.Fatal(err)
		}
		return intentDir
	}

	func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		intentDir := writeDeadCheckoutIntent(t, ws)
		stdout, _, exit := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch",
			"--max-replay-total", "10", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("dead-intent plan exit = %d", exit)
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		sessionBlocked := false
		for _, blocker := range plan.Blockers {
			sessionBlocked = sessionBlocked || blocker.Kind == internal.ReparentRefusalSessionLive
		}
		if !plan.Runnable || plan.Approval.Fingerprint == nil || sessionBlocked {
			t.Fatalf("dead launcher intent remained a plan blocker: %+v", plan)
		}
		if _, err := os.Stat(intentDir); err != nil {
			t.Fatalf("read-only plan removed the stale intent: %v", err)
		}
		if _, stderr, exit := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch",
			"--max-replay-total", "10", "--approve-plan", *plan.Approval.Fingerprint); exit != 0 {
			t.Fatalf("fresh execution with stale intent = exit %d stderr %q", exit, stderr)
		}
		if _, err := os.Stat(intentDir); !os.IsNotExist(err) {
			t.Fatalf("fresh post-lock cleanup left the dead intent: %v", err)
		}
	}(t)

	func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		stdout, _, exit := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch",
			"--max-replay-total", "10", "--plan", "--json")
		if exit != 0 {
			t.Fatalf("recovery fixture plan exit = %d", exit)
		}
		var plan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatal(err)
		}
		internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
			if step == "before-cas" {
				return errors.New("stale-intent recovery fixture")
			}
			return nil
		}
		if _, _, exit := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch",
			"--max-replay-total", "10", "--approve-plan", *plan.Approval.Fingerprint); exit == 0 {
			t.Fatal("recovery fixture did not stop before CAS")
		}
		internal.ReparentStepHook = nil
		t.Cleanup(func() { internal.ReparentStepHook = nil })
		intentDir := writeDeadCheckoutIntent(t, ws)
		if _, stderr, exit := f.Run(f.Feature, "--continue"); exit != 0 {
			t.Fatalf("recovery with stale intent = exit %d stderr %q", exit, stderr)
		}
		if _, err := os.Stat(intentDir); !os.IsNotExist(err) {
			t.Fatalf("recovery post-lock cleanup left the dead intent: %v", err)
		}
	}(t)
}

// TestReparentExclusion_SameParentCurrentAndStale owns T-030 / AC-039 /
// AC-040 with strict, separate outcomes.
func testReparentExclusionSameParentCurrentAndStale(t *testing.T) {
	f := newReparentCustomerExternal(t)
	f.reparentGit(f.Repo, "update-ref", "refs/heads/feat-pr1", f.B)

	stdout, _, exit := f.Run(f.Feature, "pr2", "--onto", "pr1", "--onto-kind", "entry", "--no-fetch", "--plan", "--json")
	if exit != 0 {
		t.Fatalf("no-work plan exit = %d", exit)
	}
	var current internal.ReparentPlan
	if err := json.Unmarshal([]byte(stdout), &current); err != nil {
		t.Fatal(err)
	}
	if current.Summary.Plannability != internal.ReparentPlannabilityNoWork ||
		current.Summary.HasWork || current.Runnable || current.Refusal.Kind != nil ||
		current.Approval.Fingerprint != nil {
		t.Fatalf("strict no-work document = %+v", current)
	}
	if current.MetadataDelta.StackSHA256AfterExpected != nil || current.MetadataDelta.PostImageKnown {
		t.Fatalf("strict no-work metadata published a post-image: %+v", current.MetadataDelta)
	}
	for _, entry := range current.MetadataDelta.Entries {
		before, after := "", ""
		if entry.LastBaseSHABefore != nil {
			before = *entry.LastBaseSHABefore
		}
		if entry.LastBaseSHAAfter != nil {
			after = *entry.LastBaseSHAAfter
		}
		if entry.BaseBefore != entry.BaseAfter || before != after || entry.Changed ||
			entry.LastBaseSHAAfterSource != internal.ReparentLastBaseSourceUnchanged {
			t.Fatalf("strict no-work metadata entry changed: %+v", entry)
		}
	}
	human, _, humanExit := f.Run(f.Feature, "pr2", "--onto", "pr1", "--onto-kind", "entry", "--no-fetch", "--plan")
	if humanExit != 0 || !strings.Contains(human, "stack.yaml after:  unchanged") ||
		strings.Contains(human, "last_base_sha ") {
		t.Fatalf("strict no-work human metadata = exit %d\n%s", humanExit, human)
	}
	_, stderr, exit := f.Run(f.Feature, "pr2", "--onto", "pr1", "--onto-kind", "entry", "--no-fetch",
		"--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64))
	if exit != 1 || !strings.Contains(stderr, "reparent: no-work:") {
		t.Fatalf("fresh no-work execution = exit %d stderr %q", exit, stderr)
	}

	f.reparentGit(f.Repo, "update-ref", "refs/heads/feat-pr1", f.D)
	stdout, _, _ = f.Run(f.Feature, "pr2", "--onto", "pr1", "--onto-kind", "entry", "--no-fetch", "--plan", "--json")
	var stale internal.ReparentPlan
	if err := json.Unmarshal([]byte(stdout), &stale); err != nil {
		t.Fatal(err)
	}
	if stale.Refusal.Kind == nil || *stale.Refusal.Kind != internal.ReparentRefusalDestinationSameParentStale {
		t.Fatalf("strict stale document = %+v", stale.Refusal)
	}
	if stale.Refusal.Detail == nil || *stale.Refusal.Detail != "the configured parent is unchanged; replay this edge with: tws sync <feature> --from <entry>" {
		t.Fatalf("stale guidance = %+v", stale.Refusal.Detail)
	}
}
func TestReparentExclusion_RecoveryVerbsAndLegacyNoWork(t *testing.T) {
	// --- TestReparentExclusion_RecoveryVerbsRefuseWithoutARun ---
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		for _, verb := range []string{"--continue", "--abort"} {
			t.Run(verb, func(t *testing.T) {
				_, stderr, exit := f.Run(f.Feature, verb)
				if exit != 1 {
					t.Fatalf("exit = %d, want 1: %s", exit, stderr)
				}
				if !strings.Contains(stderr, "no reparent run is recorded") {
					t.Fatalf("stderr = %q, want the no-run refusal", stderr)
				}
			})
		}
	}(t)

	// --- TestReparentExclusion_NoWorkPlanIsAProducedPlan ---
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		human, prose := f.Plan(f.Feature, "pr2", "--onto", "pr1", "--onto-kind", "entry", "--no-fetch")
		combined := human + prose
		if !strings.Contains(combined, "plannability=no-work") && !strings.Contains(combined, "same-parent") {
			t.Fatalf("reparenting onto the current parent must be a no-work or same-parent plan:\n%s", combined)
		}
		if strings.Contains(human, "fingerprint: ") && reparentFingerprintShape.MatchString(reparentApprovalToken(human)) {
			t.Fatalf("a no-work plan must mint no usable fingerprint:\n%s", human)
		}
	}(t)
}

// TestReparentExclusion_RecoveryVerbsRefuseWithoutARun is the mirror row: a
// recovery verb with nothing recorded refuses rather than inventing a run.

// TestReparentExclusion_CompatArtifactsMakeShippedBinariesFailClosed is T-068
// and T-071. It asserts the WRITE ORDER (§11.2) and the exact fail-closed
// shapes: the external v4 payload is a version a shipped loader rejects, and
// the checkout transaction is deliberately undecodable so AbortCheckoutSync —
// which performs no version check at all — cannot act on it.
func TestReparentExclusion_CompatArtifactsMakeShippedBinariesFailClosed(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		st := reparentPlantState(t, f, internal.ReparentStageComputing)
		if err := internal.WriteReparentCompatArtifacts(f.Loc(), st, []string{"pr2"}); err != nil {
			t.Fatal(err)
		}

		func(t *testing.T) {
			func(t *testing.T) {
				f := newReparentCustomerExternal(t)
				legacy := []byte("started_at: foreign\nfailed_branch: child\n")
				payload := []byte("state_version: 2\nfeature: foreign\nstage: rebasing\n")
				if err := os.WriteFile(internal.SyncStatePath(f.FeaturePath), legacy, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(internal.SyncRunStatePath(f.FeaturePath), payload, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(internal.SyncRunGuardPath(f.FeaturePath)); !os.IsNotExist(err) {
					t.Fatal("the fixture must have orphan state and no lock")
				}
				stdout, _, _ := f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch", "--plan", "--json")
				var plan internal.ReparentPlan
				if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
					t.Fatal(err)
				}
				if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalSyncStatePresent {
					t.Fatalf("plan refusal = %+v", plan.Refusal)
				}
				_, stderr, exit := f.Run(f.Feature, "pr2", "--onto", "master", "--no-fetch",
					"--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64))
				if exit != 1 || !strings.Contains(stderr, "reparent: sync-state-present:") {
					t.Fatalf("execution = exit %d stderr %q", exit, stderr)
				}
				if got, _ := os.ReadFile(internal.SyncStatePath(f.FeaturePath)); string(got) != string(legacy) {
					t.Fatal("foreign legacy bytes changed")
				}
				if got, _ := os.ReadFile(internal.SyncRunStatePath(f.FeaturePath)); string(got) != string(payload) {
					t.Fatal("foreign payload bytes changed")
				}
				if internal.HasReparentState(f.Loc()) {
					t.Fatal("a refused run wrote authoritative state")
				}
				if _, err := os.Stat(internal.SyncRunGuardPath(f.FeaturePath)); !os.IsNotExist(err) {
					t.Fatal("a refused run must not create a lock")
				}
			}(t)

			func(t *testing.T) {
				f := newReparentCustomerCheckout(t)
				tx := []byte("state_version: 3\nfeature: foreign\nstage: rebasing\n")
				if err := os.MkdirAll(filepath.Dir(internal.CheckoutTransactionPath(f.FeaturePath)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(internal.CheckoutTransactionPath(f.FeaturePath), tx, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(internal.CheckoutLockPath(f.FeaturePath)); !os.IsNotExist(err) {
					t.Fatal("the fixture must have an orphan transaction and no lock")
				}
				stdout, _, _ := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
				var plan internal.ReparentPlan
				if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
					t.Fatal(err)
				}
				if plan.Refusal.Kind == nil || *plan.Refusal.Kind != internal.ReparentRefusalSyncStatePresent {
					t.Fatalf("plan refusal = %+v", plan.Refusal)
				}
				_, stderr, exit := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch",
					"--max-replay-total", "10", "--approve-plan", strings.Repeat("a", 64))
				if exit != 1 || !strings.Contains(stderr, "reparent: sync-state-present:") {
					t.Fatalf("execution = exit %d stderr %q", exit, stderr)
				}
				if got, _ := os.ReadFile(internal.CheckoutTransactionPath(f.FeaturePath)); string(got) != string(tx) {
					t.Fatal("foreign checkout transaction bytes changed")
				}
				if internal.HasReparentState(f.Loc()) {
					t.Fatal("a refused run wrote authoritative state")
				}
				if _, err := os.Stat(internal.CheckoutLockPath(f.FeaturePath)); !os.IsNotExist(err) {
					t.Fatal("a refused run must not create a lock")
				}
			}(t)
		}(t)

		payload, err := os.ReadFile(internal.SyncRunStatePath(f.FeaturePath))
		if err != nil {
			t.Fatalf("the v4 payload must exist: %v", err)
		}
		var probe struct {
			StateVersion int    `yaml:"state_version"`
			Route        string `yaml:"route"`
		}
		if err := yaml.Unmarshal(payload, &probe); err != nil {
			t.Fatal(err)
		}
		if probe.StateVersion != 4 || probe.Route != "reparent" {
			t.Fatalf("payload = version %d route %q, want 4/reparent", probe.StateVersion, probe.Route)
		}
		// A shipped loader must refuse it, which is the whole point.
		if _, loadErr := internal.LoadSyncRunState(f.FeaturePath); loadErr == nil {
			t.Fatal("a shipped LoadSyncRunState must refuse state_version 4")
		}
		if info, statErr := os.Stat(internal.SyncRunStatePath(f.FeaturePath)); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the v4 payload must be 0600, got %v (%v)", info.Mode().Perm(), statErr)
		}
		if info, statErr := os.Stat(internal.SyncStatePath(f.FeaturePath)); statErr != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("the legacy sentinel must keep its shipped 0644, got %v (%v)", info.Mode().Perm(), statErr)
		}
	})

	t.Run("checkout", func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		st := reparentPlantState(t, f, internal.ReparentStageComputing)
		if err := internal.WriteReparentCompatArtifacts(f.Loc(), st, []string{"pr2"}); err != nil {
			t.Fatal(err)
		}
		// HasCheckoutTransaction is a bare stat and must still see it.
		if !internal.HasCheckoutTransaction(f.FeaturePath) {
			t.Fatal("a shipped plain sync must still observe the transaction")
		}
		// LoadCheckoutTransaction must FAIL, so both ContinueCheckoutSync and
		// AbortCheckoutSync return before taking the lock or touching Git.
		if _, err := internal.LoadCheckoutTransaction(f.FeaturePath); err == nil {
			t.Fatal("the compatibility transaction must be deliberately undecodable")
		}
		if info, statErr := os.Stat(internal.CheckoutTransactionPath(f.FeaturePath)); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the compatibility transaction must be 0600, got %v (%v)", info.Mode().Perm(), statErr)
		}
	})
}

// TestReparentExclusion_OpenRefusesWhileARunIsRecorded is T-056 / §14.2a's
// external half. `tws open` starts no session and attaches no terminal, and
// the refusal lifts the instant the artifact is gone.
// The launch exclusion, the checkout session preconditions, the runtime-state filters and the scratch's exclusion from every worktree inventory (AC-068, AC-056).
//
// §17.3 counts one t.Run leaf per real-Git cell. These assertions were
// separate leaves; each keeps its own fixture, scope and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentExclusion_SessionAndRuntimeState(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-056",
		"session-launch-and-runtime-exclusion",
		"checkout-intent-symlink-rooted",
		"checkout-feature-dir-intent",
	)
	// --- TestReparentExclusion_OpenRefusesWhileARunIsRecorded ---
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		reparentPlantState(t, f, internal.ReparentStageComputing)

		_, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(openCmd, f.Feature, "pr2", "--no-agent"); code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
		})
		if !strings.Contains(stderr, "a stack reparent is in progress for \""+f.Feature+"\"") {
			t.Fatalf("tws open must refuse with the §11.2 sentence, got:\n%s", stderr)
		}
		for _, args := range [][]string{
			{f.Feature, "--feature-dir", "--no-agent"},
			{f.Feature, "--feature-dir"},
		} {
			stdout, stderr := syncCaptureStreams(t, func() {
				if code := syncExecute(openCmd, args...); code != 1 {
					t.Errorf("feature-dir route %v exit = %d, want 1", args, code)
				}
			})
			if stdout != "" {
				t.Fatalf("feature-dir refusal %v wrote success output before exclusion: %q", args, stdout)
			}
			if strings.Contains(stdout+stderr, "Opening feature dir:") {
				t.Fatalf("feature-dir refusal %v announced an opening before exclusion:\n%s\n%s", args, stdout, stderr)
			}
			if !strings.Contains(stderr, "a stack reparent is in progress for \""+f.Feature+"\"") {
				t.Fatalf("feature-dir refusal %v = %q", args, stderr)
			}
		}

		// The refusal lifts as soon as the artifact is gone.
		if err := internal.RemoveReparentState(f.Loc()); err != nil {
			t.Fatal(err)
		}
		stdout, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(openCmd, f.Feature, "pr2", "--no-agent"); code != 0 {
				t.Errorf("exit = %d, want 0 once the artifact is gone (%s)", code, stderr)
			}
		})
		if strings.Contains(stdout+stderr, "a stack reparent is in progress") {
			t.Fatalf("the refusal must lift with the artifact:\n%s\n%s", stdout, stderr)
		}
	}(t)

	// --- TestReparentExclusion_CheckoutSessionPreconditionsRefuse ---
	func(t *testing.T) {
		f := newReparentCustomerCheckout(t)
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		entry := f.Entry("pr2")

		if err := internal.CheckoutSessionPreconditions(ws, f.Feature, entry); err != nil {
			t.Fatalf("the fixture must be launchable before the artifact exists: %v", err)
		}
		reparentPlantState(t, f, internal.ReparentStageComputing)
		err = internal.CheckoutSessionPreconditions(ws, f.Feature, entry)
		if err == nil {
			t.Fatal("a checkout session must refuse while a reparent is recorded")
		}
		if !strings.Contains(err.Error(), "a stack reparent is in progress") {
			t.Fatalf("refusal = %q, want the §11.2 sentence", err)
		}
		if err := internal.CheckoutSessionPreconditions(ws, "unrelated", entry); err == nil ||
			!strings.Contains(err.Error(), "a stack reparent is in progress") {
			t.Fatalf("checkout reparent exclusion is workspace-global, got %v", err)
		}
		if err := refuseOpenDuringReparent(ws, "unrelated"); err == nil ||
			!strings.Contains(err.Error(), "a stack reparent is in progress") {
			t.Fatalf("tws open must apply the checkout-global exclusion before launch, got %v", err)
		}
		if err := internal.RemoveReparentState(f.Loc()); err != nil {
			t.Fatal(err)
		}
		intentDir := internal.CheckoutSessionIntentDir(ws)
		if err := os.MkdirAll(filepath.Dir(intentDir), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		outsideOwner := filepath.Join(outside, "owner.json")
		outsideBytes := []byte(`{"token":"outside-secret","pid":7,"created_at":"x"}`)
		if err := os.WriteFile(outsideOwner, outsideBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, intentDir); err != nil {
			t.Fatal(err)
		}
		stdout, _, _ := f.Run(f.Feature, "pr2", "--onto", "main", "--no-fetch", "--plan", "--json")
		var symlinkPlan internal.ReparentPlan
		if err := json.Unmarshal([]byte(stdout), &symlinkPlan); err != nil {
			t.Fatal(err)
		}
		if symlinkPlan.Refusal.Kind == nil || *symlinkPlan.Refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("symlink checkout intent must block reparent: %+v", symlinkPlan.Refusal)
		}
		if err := internal.CleanupStaleCheckoutSessionIntent(ws); err == nil {
			t.Fatal("reparent cleanup must refuse a symlinked checkout intent")
		}
		if after, err := os.ReadFile(outsideOwner); err != nil || string(after) != string(outsideBytes) {
			t.Fatalf("checkout intent cleanup changed outside target: %q (%v)", after, err)
		}
		if err := os.Remove(intentDir); err != nil {
			t.Fatal(err)
		}
		if err := internal.CheckoutSessionPreconditions(ws, f.Feature, entry); err != nil {
			t.Fatalf("the refusal must lift with the artifact: %v", err)
		}

		var mutationToken string
		internal.CheckoutSessionLaunchIntentHook = func() error {
			present, _, err := internal.CheckoutSessionIntent(ws)
			if err != nil || !present {
				t.Fatalf("feature-dir final-check hook did not observe its intent: present=%v err=%v", present, err)
			}
			mutationToken = strings.Repeat("f", 32)
			_, err = internal.AcquireCheckoutMutationLock(
				ws.CheckoutStateDir(), mutationToken, f.Feature, "reparent",
				internal.ReparentStatePath(f.Loc()),
			)
			return err
		}
		t.Cleanup(func() { internal.CheckoutSessionLaunchIntentHook = nil })
		_, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(openCmd, f.Feature, "--feature-dir"); code != 1 {
				t.Fatalf("feature-dir race exit = %d", code)
			}
		})
		internal.CheckoutSessionLaunchIntentHook = nil
		if !strings.Contains(stderr, "checkout sync is active") {
			t.Fatalf("feature-dir final mutation guard = %q", stderr)
		}
		if present, _, err := internal.CheckoutSessionIntent(ws); err != nil || present {
			t.Fatalf("refused feature-dir open left its intent: present=%v err=%v", present, err)
		}
		if err := internal.ReleaseCheckoutMutationLock(ws.CheckoutStateDir(), mutationToken); err != nil {
			t.Fatal(err)
		}
	}(t)

	// --- TestReparentExclusion_ExternalLaunchHandshakeBothDirections ---
	func(t *testing.T) {
		t.Cleanup(func() {
			internal.ReparentStepHook = nil
			DirectSessionLaunchIntentHook = nil
		})
		// Reparent wins the shared mutation intent first. The direct launch
		// publishes its starting record, observes the guard, removes the
		// record, and starts no process.
		f := newReparentCustomerExternal(t)
		in, plan, req := reparentApprovedBeginInput(t, f, "pr2", "master")
		staleToken, err := internal.CreateExternalSessionIntent(f.FeaturePath, f.Feature, []string{"pr2"}, false)
		if err != nil {
			t.Fatal(err)
		}
		stalePath := filepath.Join(internal.ExternalSessionIntentsDir(f.FeaturePath), staleToken+".json")
		staleBytes, err := os.ReadFile(stalePath)
		if err != nil {
			t.Fatal(err)
		}
		var staleIntent internal.ExternalSessionIntent
		if err := json.Unmarshal(staleBytes, &staleIntent); err != nil {
			t.Fatal(err)
		}
		staleIntent.OwnerPID = 999999
		staleBytes, _ = json.MarshalIndent(staleIntent, "", "  ")
		if err := os.WriteFile(stalePath, staleBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		var calls []string
		internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
			if step != "post-lock-resnapshot" {
				return nil
			}
			if _, statErr := os.Stat(stalePath); !os.IsNotExist(statErr) {
				t.Fatalf("post-lock admission did not clean a crashed launch intent: %v", statErr)
			}
			opts := directOpenOpts{
				Path:        internal.WorktreePath(f.Feature, "pr2"),
				Feature:     f.Feature,
				Name:        "pr2",
				GitBranch:   f.Entry("pr2").GitBranch(),
				FeaturePath: f.FeaturePath,
				LookPath:    func(string) (string, error) { return "/usr/bin/claude", nil },
				Runner:      &fakeDirectRunner{calls: &calls, label: "agent", pid: 7001},
				Shell:       &fakeDirectRunner{calls: &calls, label: "shell", pid: 7002},
				FinalGuard: func() error {
					return refuseExternalOpenDuringMutation(f.Feature, f.FeaturePath)
				},
				Out: io.Discard,
				Err: io.Discard,
			}
			openErr := openDirect(opts)
			if openErr == nil || !strings.Contains(openErr.Error(), "mutation is being admitted") {
				t.Fatalf("launch must lose to the visible reparent intent: %v", openErr)
			}
			if strings.Contains(strings.Join(calls, ","), ".start") {
				t.Fatal("a process started after the launch lost the handshake")
			}
			if _, statErr := os.Stat(internal.DirectSessionsDir(f.FeaturePath)); !os.IsNotExist(statErr) {
				t.Fatalf("refused launch left a starting record: %v", statErr)
			}
			return nil
		}
		run, err := internal.BeginReparentRun(internal.ReparentBeginInput{Input: in, Plan: plan, Request: req})
		internal.ReparentStepHook = nil
		if err != nil {
			t.Fatalf("reparent should proceed after the losing launch cleaned its intent: %v", err)
		}
		if _, err := internal.AbortReparent(in); err != nil {
			t.Fatal(err)
		}
		_ = run

		// Session wins by publishing first. Reparent takes the mutation lock,
		// reruns SessionProbe, refuses session-live and releases the lock; the
		// launch's final guard then passes and it may start safely.
		f = newReparentCustomerExternal(t)
		in, plan, req = reparentApprovedBeginInput(t, f, "pr2", "master")
		calls = nil
		DirectSessionLaunchIntentHook = func() error {
			_, beginErr := internal.BeginReparentRun(internal.ReparentBeginInput{Input: in, Plan: plan, Request: req})
			var refusal *internal.ReparentRefusalError
			if !errors.As(beginErr, &refusal) || refusal.Kind != internal.ReparentRefusalSessionLive {
				t.Fatalf("reparent must lose to the published session intent: %v", beginErr)
			}
			if internal.HasReparentState(f.Loc()) {
				t.Fatal("a session-live admission refusal wrote authoritative state")
			}
			if _, statErr := os.Stat(internal.SyncRunGuardPath(f.FeaturePath)); !os.IsNotExist(statErr) {
				t.Fatalf("a session-live admission refusal left the mutation intent: %v", statErr)
			}
			return nil
		}
		opts := directOpenOpts{
			Path:        internal.WorktreePath(f.Feature, "pr2"),
			Feature:     f.Feature,
			Name:        "pr2",
			GitBranch:   f.Entry("pr2").GitBranch(),
			FeaturePath: f.FeaturePath,
			LookPath:    func(string) (string, error) { return "/usr/bin/claude", nil },
			Runner:      &fakeDirectRunner{calls: &calls, label: "agent", pid: 7101},
			Shell:       &fakeDirectRunner{calls: &calls, label: "shell", pid: 7102},
			FinalGuard: func() error {
				return refuseExternalOpenDuringMutation(f.Feature, f.FeaturePath)
			},
			Out: io.Discard,
			Err: io.Discard,
		}
		if err := openDirect(opts); err != nil {
			t.Fatalf("the winning launch should proceed after reparent releases its intent: %v", err)
		}
		DirectSessionLaunchIntentHook = nil
		if !strings.Contains(strings.Join(calls, ","), "agent.start") {
			t.Fatal("the winning launch never started")
		}
	}(t)

	// --- TestReparentExclusion_TmuxAndAllPublishBeforeFinalGuard ---
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		sentinel := errors.New("final mutation guard refused")
		assertIntent := func(featureWide bool) func() error {
			return func() error {
				blocking, _, err := internal.LoadExternalSessionIntents(f.FeaturePath, f.Feature, []string{"pr2"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(blocking) != 1 {
					t.Fatalf("final guard observed %d launch intents, want one", len(blocking))
				}
				if (blocking[0].Record.Scope == internal.ExternalSessionIntentFeature) != featureWide {
					t.Fatalf("intent scope = %q, featureWide=%v", blocking[0].Record.Scope, featureWide)
				}
				return sentinel
			}
		}
		err := openWithTmux(f.Feature, "pr2", internal.WorktreePath(f.Feature, "pr2"), f.FeaturePath, assertIntent(false))
		if !errors.Is(err, sentinel) {
			t.Fatalf("tmux final guard = %v", err)
		}
		if _, statErr := os.Stat(internal.ExternalSessionIntentsDir(f.FeaturePath)); !os.IsNotExist(statErr) {
			t.Fatalf("tmux refusal left its launch intent: %v", statErr)
		}
		err = openAll(f.Feature, f.FeaturePath, assertIntent(true))
		if !errors.Is(err, sentinel) {
			t.Fatalf("--all final guard = %v", err)
		}
		if _, statErr := os.Stat(internal.ExternalSessionIntentsDir(f.FeaturePath)); !os.IsNotExist(statErr) {
			t.Fatalf("--all refusal left its launch intent: %v", statErr)
		}

		names, err := reparentExternalLiveSessions(f.FeaturePath, f.Feature, []string{"pr2", "pr3"}, reparentTmuxProbe{
			snapshot: internal.TmuxSnapshot{
				Available:     true,
				ServerRunning: true,
				Sessions: map[string]bool{
					internal.ExternalTmuxSessionName(f.Feature, "pr2"): true,
				},
			},
		})
		if err != nil || strings.Join(names, ",") != "pr2" {
			t.Fatalf("per-branch tmux session probe = %v (%v)", names, err)
		}
		names, err = reparentExternalLiveSessions(f.FeaturePath, f.Feature, []string{"pr2", "pr3"}, reparentTmuxProbe{
			snapshot: internal.TmuxSnapshot{
				Available:     true,
				ServerRunning: true,
				Sessions: map[string]bool{
					internal.ExternalFeatureTmuxSessionName(f.Feature): true,
				},
			},
		})
		if err != nil || strings.Join(names, ",") != "pr2,pr3" {
			t.Fatalf("feature-wide tmux session probe = %v (%v)", names, err)
		}
	}(t)

	// --- TestReparentExclusion_RecoveryReprobesSessionsAfterReclaim ---
	func(t *testing.T) {
		t.Cleanup(func() { internal.ReparentStepHook = nil })
		f := newReparentCustomerExternal(t)
		in, plan, req := reparentApprovedBeginInput(t, f, "pr2", "master")
		crash := errors.New("pause after authoritative state")
		internal.ReparentStepHook = func(stage internal.ReparentStage, step string) error {
			if step == "artifact-written" {
				return crash
			}
			return nil
		}
		if _, err := internal.BeginReparentRun(internal.ReparentBeginInput{Input: in, Plan: plan, Request: req}); !errors.Is(err, crash) {
			t.Fatalf("setup = %v", err)
		}
		internal.ReparentStepHook = nil
		probes := 0
		in.SessionProbe = func(affected []string) ([]string, error) {
			probes++
			return []string{"pr2"}, nil
		}
		_, err := internal.ContinueReparent(in)
		var refusal *internal.ReparentRefusalError
		if !errors.As(err, &refusal) || refusal.Kind != internal.ReparentRefusalSessionLive {
			t.Fatalf("recovery session probe = %v", err)
		}
		if probes != 1 {
			t.Fatalf("recovery session probes = %d, want one after reclaim", probes)
		}
		if _, statErr := os.Stat(internal.SyncRunGuardPath(f.FeaturePath)); !os.IsNotExist(statErr) {
			t.Fatalf("recovery session refusal left the reclaimed guard: %v", statErr)
		}
		in.SessionProbe = func([]string) ([]string, error) { return nil, nil }
		if _, err := internal.AbortReparent(in); err != nil {
			t.Fatal(err)
		}
	}(t)

	// --- TestReparentExclusion_ReparentArtifactsAreRuntimeState ---
	func(t *testing.T) {
		for _, path := range []string{
			".reparent-state.v1.yaml",
			".reparent-remote.v1.yaml",
			".reparent",
			".reparent/abc123/scratch/file.txt",
			".session-intents/0123456789abcdef0123456789abcdef.json",
			"worktrees/x/.reparent/deep",
		} {
			if !isRuntimeState(path) {
				t.Fatalf("%q is reparent runtime state and must be filtered from an import", path)
			}
		}
		for _, path := range []string{
			"stack.yaml",
			"inject/CLAUDE.md",
			"worktrees/api/main.go",
			"my.reparenting/notes.md",
			"reparent-notes.md",
		} {
			if isRuntimeState(path) {
				t.Fatalf("%q is user content and must NOT be filtered", path)
			}
		}
	}(t)

	// --- TestReparentExclusion_ScratchIsExcludedFromWorktreeInventories ---
	func(t *testing.T) {
		f := newReparentCustomerExternal(t)
		scratch := filepath.Join(f.FeaturePath, ".reparent", "run-1", "scratch")
		f.reparentGit(f.Repo, "worktree", "add", "--detach", scratch, "master")
		lab := filepath.Join(t.TempDir(), ".reparent-lab", "work")
		if err := os.MkdirAll(filepath.Dir(lab), 0o755); err != nil {
			t.Fatal(err)
		}
		f.reparentGit(f.Repo, "worktree", "add", "--detach", lab, "master")

		raw := internal.BuildWorktreeInventory(f.Repo)
		if !raw.Available {
			t.Fatalf("the porcelain parser must stay available: %v", raw.Err)
		}
		found := false
		for _, rec := range raw.Records {
			if strings.Contains(filepath.ToSlash(rec.Path), "/.reparent/") {
				found = true
			}
		}
		if !found {
			t.Fatal("the unfiltered parser must still report the scratch worktree verbatim")
		}

		withoutState := internal.ExcludeReparentScratchWorktrees(raw)
		if len(withoutState.Records) != len(raw.Records) {
			t.Fatal("status without authoritative state must not hide any .reparent-like path")
		}

		filtered := internal.ExcludeReparentScratchWorktrees(raw, scratch)
		labFound := false
		for _, rec := range filtered.Records {
			if filepath.Clean(rec.Path) == filepath.Clean(scratch) {
				t.Fatalf("the scratch worktree must be excluded from observability inventories: %s", rec.Path)
			}
			if strings.HasSuffix(filepath.ToSlash(rec.Path), "/.reparent-lab/work") {
				labFound = true
			}
		}
		if len(filtered.Records) != len(raw.Records)-1 {
			t.Fatalf("exactly one record must be filtered, got %d of %d", len(filtered.Records), len(raw.Records))
		}
		if !labFound {
			t.Fatal("the unrelated .reparent-lab worktree was hidden")
		}
	}(t)
}

// TestReparentExclusion_CheckoutSessionPreconditionsRefuse is §14.2a's
// checkout half, asserted at the single insertion point both checkout open
// paths funnel through.

// TestReparentExclusion_NoWorkPlanIsAProducedPlan is T-029: reparenting an
// entry onto the parent it already has is a no-work plan, not a refusal. It
// exits 0, publishes no refusal kind, and mints no fingerprint.

// reparentApprovalToken reads the approval line without failing the test, so a
// negative assertion can be written about its absence.
func reparentApprovalToken(human string) string {
	const prefix = "  fingerprint: "
	for _, line := range strings.Split(human, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// TestReparentExclusion_ReparentArtifactsAreRuntimeState is §14.2a's
// import/runtime filter: every `.reparent*` artifact is runtime state that
// must never be recreated by an import, and no user path is filtered by
// accident.

// TestReparentExclusion_ScratchIsExcludedFromWorktreeInventories proves the
// third half of the same filter: a reparent scratch worktree never appears as
// a materialization of a logical branch on an observability surface.
