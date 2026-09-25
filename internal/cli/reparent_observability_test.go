package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// T-072 / T-073 — observability with and without a reparent artifact.
//
// The two halves are deliberately asymmetric:
//
//   - WITH an artifact: exactly one anchored line per affected feature on
//     stderr, and NO surface may emit an invalid/corrupt/manually-remove hint
//     about the §11.2 compatibility files or the checkout lock.
//   - WITHOUT one: every surface is byte-identical to today, the `reparent`
//     key is ABSENT (not null) from both JSON documents, and schema_version is
//     unchanged.
//
// Checkout mode has TWO distinct state directories, so a fixture that builds
// only one proves nothing: the authoritative artifact lives under
// ws.CheckoutStateDir() while the compatibility transaction and lock are
// derived from the FEATURE PATH. A legacy `.tws/<feature>` layout makes those
// two directories differ, so both layouts are exercised.
// ---------------------------------------------------------------------------

// reparentForbiddenHints is AC-084's closed list. No surface may say any of
// these about the §11.2 compatibility artifacts while the authoritative
// artifact exists.
var reparentForbiddenHints = []string{
	"state file unreadable",
	"corrupt transaction state",
	"corrupt lock file",
	"remove it manually",
	"manually remove",
	string(internal.IssueSyncStateInvalid),
}

// TestReparentObservability_AbsentStateIsByteIdentical is T-073 and AC-085:
// with no artifact the `reparent` key is absent from both documents — not
// null — and schema_version does not move.
func TestReparentObservability_AbsentStateIsByteIdentical(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-073",
		"absent-state-byte-identity",
		"ancestry-repo-resolution-unchanged",
	)
	t.Run("stack status --json", func(t *testing.T) {
		f := newReparentStatusExternal(t)
		ws, err := internal.RequireWorkspace()
		if err != nil {
			t.Fatal(err)
		}
		res := internal.ResolveStackAncestryRepo(ws, internal.LoadConfig(), f.FeaturePath, f.Stack())
		wantRoot, err := internal.MainRepoRootIn(f.Repo)
		if err != nil {
			t.Fatal(err)
		}
		wantRoot, _ = filepath.EvalSymlinks(wantRoot)
		gotRoot, _ := filepath.EvalSymlinks(res.RepoDir)
		if gotRoot != wantRoot {
			t.Fatalf("ancestry repository root = %q, want shipped resolver result %q", gotRoot, wantRoot)
		}
		stdout, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(stackCmd, "status", f.Feature, "--json"); code != 0 {
				t.Errorf("exit = %d", code)
			}
		})
		var doc map[string]any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("stack status --json is not one JSON value: %v\n%s", err, stdout)
		}
		if _, present := doc["reparent"]; present {
			t.Fatalf("the reparent key must be ABSENT, not null, without an artifact:\n%s", stdout)
		}
		if v, ok := doc["schema_version"].(float64); !ok || int(v) != 1 {
			t.Fatalf("schema_version = %v, want 1 (no bump)", doc["schema_version"])
		}
		if strings.Contains(stderr, "reparent in progress") {
			t.Fatalf("no anchored line may be written without an artifact:\n%s", stderr)
		}
	})

	t.Run("agent status --json", func(t *testing.T) {
		f := newReparentStatusExternal(t)
		stdout, stderr := syncCaptureStreams(t, func() {
			if code := syncExecute(statusCmd, f.Feature, "--json"); code != 0 {
				t.Errorf("exit = %d", code)
			}
		})
		var doc map[string]any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("status --json is not one JSON value: %v\n%s", err, stdout)
		}
		features, _ := doc["features"].([]any)
		if len(features) == 0 {
			t.Fatalf("the fixture must report at least one feature:\n%s", stdout)
		}
		for _, raw := range features {
			entry, _ := raw.(map[string]any)
			if _, present := entry["reparent"]; present {
				t.Fatalf("the reparent key must be ABSENT without an artifact:\n%s", stdout)
			}
		}
		if strings.Contains(stderr, "reparent in progress") {
			t.Fatalf("no anchored line may be written without an artifact:\n%s", stderr)
		}
	})
}

// TestReparentObservability_PendingRemoteRecordAloneChangesNothing is §11.10's
// closing rule: a pending follow-up record, on its own, changes NO output on
// any read-only surface. It is consumed only by the push paths and by
// `tws stack reparent` itself.
func TestReparentObservability_PendingRemoteRecordAloneChangesNothing(t *testing.T) {
	f := newReparentStatusExternal(t)

	before, _ := syncCaptureStreams(t, func() {
		_ = syncExecute(stackCmd, "status", f.Feature, "--json")
	})
	reparentPlantRemoteRecord(t, f, "pr2", "feat-pr2", f.SHA("feat-pr2"), f.SHA("feat-pr2"))
	after, afterErr := syncCaptureStreams(t, func() {
		_ = syncExecute(stackCmd, "status", f.Feature, "--json")
	})
	if before != after {
		t.Fatalf("a pending remote record must change no status output:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if strings.Contains(afterErr, "reparent") {
		t.Fatalf("a pending record must produce no stderr notice:\n%s", afterErr)
	}
	// And it must still be on disk: a read-only surface never clears it.
	if _, err := os.Stat(internal.ReparentRemoteRecordPath(f.Loc())); err != nil {
		t.Fatalf("a read-only surface must not delete the record: %v", err)
	}
}

func TestReparentObservability_DivergentExternalRootUsesReparentLayout(t *testing.T) {
	f := newReparentStatusExternal(t)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	// Keep ordinary ancestry routed through MetadataRoot=A and make it stale.
	// The active reparent artifact below lives only under TWS_ROOT=B.
	writeAndCommit(t, filepath.Join(f.FeaturePath, "worktrees", "pr1"),
		"parent-advanced.txt", "later\n", "advance parent")

	alternateRoot := filepath.Join(filepath.Dir(ws.MetadataRoot), "reparent-observability-alt.tws")
	alternateFeature := filepath.Join(alternateRoot, f.Feature)
	if err := os.MkdirAll(alternateFeature, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(alternateFeature, f.Stack()); err != nil {
		t.Fatal(err)
	}
	alternate := *f
	alternate.FeaturePath = alternateFeature
	t.Setenv("TWS_ROOT", alternateRoot)
	reparentPlantState(t, &alternate, internal.ReparentStageComputing)

	for _, surface := range []struct {
		name string
		run  func() (string, string)
	}{
		{"doctor", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(doctorCmd, f.Feature) })
		}},
		{"stack status", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(stackCmd, "status", f.Feature, "--json") })
		}},
		{"stack status human", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(stackCmd, "status", f.Feature) })
		}},
		{"agent status", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(statusCmd, f.Feature, "--json") })
		}},
	} {
		t.Run(surface.name, func(t *testing.T) {
			stdout, stderr := surface.run()
			if n := strings.Count(stderr, "reparent active: "+f.Feature+" "); n != 1 {
				t.Fatalf("%s resolved the wrong external root (%d notices):\nstdout:\n%s\nstderr:\n%s",
					surface.name, n, stdout, stderr)
			}
			for _, forbidden := range []string{"tws sync ", "git rebase --onto"} {
				if strings.Contains(stdout, forbidden) {
					t.Fatalf("%s emitted repair guidance from MetadataRoot instead of suppressing it from TWS_ROOT:\n%s",
						surface.name, stdout)
				}
			}
			if surface.name == "doctor" && !strings.Contains(stdout, "ancestry stale") {
				t.Fatalf("doctor suppressed the ancestry status/reason instead of only its guidance:\n%s", stdout)
			}
			if surface.name == "stack status" {
				var doc map[string]any
				if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
					t.Fatal(err)
				}
				if _, ok := doc["reparent"].(map[string]any); !ok {
					t.Fatalf("stack status omitted divergent-root projection:\n%s", stdout)
				}
				entries, _ := doc["entries"].([]any)
				foundStale := false
				for _, raw := range entries {
					entry, _ := raw.(map[string]any)
					if entry["name"] != "pr2" {
						continue
					}
					ancestry, _ := entry["ancestry"].(map[string]any)
					reason, reasonOK := ancestry["reason"].(string)
					foundStale = ancestry["status"] == "stale" &&
						reasonOK && strings.TrimSpace(reason) != "" &&
						ancestry["guidance"] == nil
				}
				if !foundStale {
					t.Fatalf("stack status JSON lost stale status/reason or retained guidance:\n%s", stdout)
				}
			}
			if surface.name == "stack status human" {
				if !strings.Contains(stdout, "stale") || !strings.Contains(stdout, "reason:") {
					t.Fatalf("human stack status lost stale status/reason:\n%s", stdout)
				}
			}
			if surface.name == "agent status" {
				var doc map[string]any
				if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
					t.Fatal(err)
				}
				features, _ := doc["features"].([]any)
				found := false
				for _, raw := range features {
					entry, _ := raw.(map[string]any)
					if entry["feature"] == f.Feature {
						_, found = entry["reparent"].(map[string]any)
					}
				}
				if !found {
					t.Fatalf("agent status omitted divergent-root projection:\n%s", stdout)
				}
			}
		})
	}
}

// TestReparentObservability_ActiveStateProjectsAndSuppresses is T-072. It runs
// in BOTH checkout layouts, because the compatibility artifacts and the
// authoritative artifact live in different directories in the legacy one.
func TestReparentObservability_ActiveStateProjectsAndSuppresses(t *testing.T) {
	for _, layout := range []string{"features-layout", "legacy-layout"} {
		t.Run(layout, func(t *testing.T) {
			f := newReparentCustomerCheckout(t)
			if layout == "legacy-layout" {
				f = reparentRelocateToLegacyLayout(t, f)
			}
			st := reparentPlantState(t, f, internal.ReparentStageComputing)
			if err := internal.WriteReparentCompatArtifacts(f.Loc(), st, []string{"pr2"}); err != nil {
				t.Fatal(err)
			}
			// The two checkout directories must genuinely differ in the legacy
			// layout, or the fixture proves nothing.
			authoritative := internal.ReparentStatePath(f.Loc())
			compat := internal.CheckoutTransactionPath(f.FeaturePath)
			if layout == "legacy-layout" && filepath.Dir(authoritative) == filepath.Dir(compat) {
				t.Fatalf("the legacy fixture must separate the two state directories: %s vs %s", authoritative, compat)
			}
			for _, path := range []string{authoritative, compat} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("the fixture must create %s: %v", path, err)
				}
			}

			for _, surface := range []struct {
				name string
				run  func() (string, string)
			}{
				{"stack status --json", func() (string, string) {
					return syncCaptureStreams(t, func() { _ = syncExecute(stackCmd, "status", f.Feature, "--json") })
				}},
				{"status --json", func() (string, string) {
					return syncCaptureStreams(t, func() { _ = syncExecute(statusCmd, "--json") })
				}},
				{"doctor", func() (string, string) {
					return syncCaptureStreams(t, func() { _ = syncExecute(doctorCmd) })
				}},
				{"list", func() (string, string) {
					return syncCaptureStreams(t, func() { _ = syncExecute(listCmd) })
				}},
			} {
				t.Run(surface.name, func(t *testing.T) {
					stdout, stderr := surface.run()
					combined := stdout + stderr
					for _, hint := range reparentForbiddenHints {
						if strings.Contains(combined, hint) {
							t.Fatalf("%s must not say %q about the compatibility artifacts:\nstdout:\n%s\nstderr:\n%s",
								surface.name, hint, stdout, stderr)
						}
					}
					if n := strings.Count(stderr, "reparent active: "+f.Feature+" "); n != 1 {
						t.Fatalf("%s must write the anchored line exactly once on stderr, got %d:\n%s", surface.name, n, stderr)
					}
					if strings.Contains(stdout, "reparent active: ") {
						t.Fatalf("%s must write the anchored line on stderr, never stdout:\n%s", surface.name, stdout)
					}
					if strings.Contains(stderr, "--continue") || strings.Contains(stderr, "--abort") {
						t.Fatalf("%s must not offer recovery while the owner pid is live:\n%s", surface.name, stderr)
					}
				})
			}

			t.Run("projection is published under the reparent key", func(t *testing.T) {
				stdout, _ := syncCaptureStreams(t, func() {
					_ = syncExecute(stackCmd, "status", f.Feature, "--json")
				})
				var doc map[string]any
				if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
					t.Fatalf("not one JSON value: %v\n%s", err, stdout)
				}
				projection, ok := doc["reparent"].(map[string]any)
				if !ok {
					t.Fatalf("the reparent projection must be present while an artifact exists:\n%s", stdout)
				}
				if projection["status"] != internal.ReparentArtifactActive {
					t.Fatalf("status = %v, want %q", projection["status"], internal.ReparentArtifactActive)
				}
				if projection["run_id"] != st.RunID {
					t.Fatalf("run_id = %v, want %s", projection["run_id"], st.RunID)
				}
				if v, ok := doc["schema_version"].(float64); !ok || int(v) != 1 {
					t.Fatalf("schema_version = %v, want 1 — the projection is additive", doc["schema_version"])
				}
			})

			t.Run("surfaces stay strictly read-only", func(t *testing.T) {
				beforeState, err := os.ReadFile(authoritative)
				if err != nil {
					t.Fatal(err)
				}
				beforeCompat, err := os.ReadFile(compat)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = syncCaptureStreams(t, func() { _ = syncExecute(doctorCmd) })
				_, _ = syncCaptureStreams(t, func() { _ = syncExecute(listCmd) })
				_, _ = syncCaptureStreams(t, func() { _ = syncExecute(statusCmd, "--json") })
				_, _ = syncCaptureStreams(t, func() { _ = syncExecute(stackCmd, "status", f.Feature, "--json") })
				afterState, err := os.ReadFile(authoritative)
				if err != nil {
					t.Fatalf("a read-only surface removed the artifact: %v", err)
				}
				afterCompat, err := os.ReadFile(compat)
				if err != nil {
					t.Fatalf("a read-only surface removed the compatibility transaction: %v", err)
				}
				if string(beforeState) != string(afterState) || string(beforeCompat) != string(afterCompat) {
					t.Fatal("no observability surface may repair or rewrite reparent state")
				}
			})
		})
	}
}

// TestReparentObservability_AllSixStatusesAcrossHumanSurfaces owns
// T-072 / AC-084's six-status human rendering contract.
func TestReparentObservability_AllSixStatusesAcrossHumanSurfaces(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-072", "six-observability-statuses")
	f := newReparentStatusExternal(t)
	base := reparentPlantState(t, f, internal.ReparentStageComputing)
	base.OwnerPID = os.Getpid()
	stackBytes, err := os.ReadFile(internal.StackPath(f.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}

	writeState := func(st *internal.ReparentState) {
		t.Helper()
		data, err := yaml.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(internal.ReparentStatePath(f.Loc()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	surfaces := []struct {
		name string
		run  func() (string, string)
	}{
		{"stack status", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(stackCmd, "status", f.Feature) })
		}},
		{"status", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(statusCmd, f.Feature) })
		}},
		{"doctor", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(doctorCmd, f.Feature) })
		}},
		{"list", func() (string, string) {
			return syncCaptureStreams(t, func() { _ = syncExecute(listCmd) })
		}},
	}
	for _, status := range []string{
		internal.ReparentArtifactActive,
		internal.ReparentArtifactUnsupported,
		internal.ReparentArtifactCorrupt,
		internal.ReparentArtifactForeign,
		internal.ReparentArtifactStale,
		internal.ReparentArtifactComplete,
	} {
		st := *base
		switch status {
		case internal.ReparentArtifactActive:
			st.OwnerPID = os.Getpid()
			st.SetStage(internal.ReparentStageComputing)
			writeState(&st)
		case internal.ReparentArtifactComplete:
			st.OwnerPID = 999999
			advancePlantedReparentState(t, &st, stackBytes, internal.ReparentStageCompleted)
			writeState(&st)
		case internal.ReparentArtifactUnsupported:
			st.StateVersion = internal.ReparentStateVersion + 1
			writeState(&st)
		case internal.ReparentArtifactCorrupt:
			if err := os.WriteFile(internal.ReparentStatePath(f.Loc()), []byte("state_version: [broken\nsecond line\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		case internal.ReparentArtifactForeign:
			st.Feature = "foreign-feature"
			writeState(&st)
		case internal.ReparentArtifactStale:
			st.OwnerPID = 999999
			st.SetStage(internal.ReparentStageComputing)
			writeState(&st)
		}

		for _, surface := range surfaces {
			_, stderr := surface.run()
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			var notices []string
			for _, line := range lines {
				if strings.HasPrefix(line, "reparent ") {
					notices = append(notices, line)
				}
			}
			if len(notices) != 1 || !strings.HasPrefix(notices[0], "reparent "+status+":") {
				t.Fatalf("%s/%s notice = %v\n%s", status, surface.name, notices, stderr)
			}
			if strings.ContainsAny(notices[0], "\r\n") {
				t.Fatalf("%s/%s notice is multiline: %q", status, surface.name, notices[0])
			}
			actionable := status == internal.ReparentArtifactStale || status == internal.ReparentArtifactComplete
			hasGuidance := strings.Contains(notices[0], "tws stack reparent")
			if hasGuidance != actionable {
				t.Fatalf("%s/%s actionable guidance=%v, want %v: %s", status, surface.name, hasGuidance, actionable, notices[0])
			}
		}
	}
}

// TestReparentObservability_AncestryGuidanceIsSuppressedNotTheStatus is
// §11.10 rule 2's precise boundary: the ancestry STATUS and REASON are still
// reported; only the repair guidance — which names verbs §11.2 refuses — is
// withheld.
func TestReparentObservability_AncestryGuidanceIsSuppressedNotTheStatus(t *testing.T) {
	f := newReparentCustomerCheckout(t)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	cfg := internal.LoadConfig()
	stack := f.Stack()

	before, _ := internal.FeatureStackEdges(ws, cfg, f.Feature, f.FeaturePath, stack)
	guided := false
	for _, e := range before {
		if e.Guidance != "" {
			guided = true
		}
	}
	if !guided {
		t.Fatal("the deterministic stale/divergent fixture must publish guidance before suppression")
	}

	reparentPlantState(t, f, internal.ReparentStageComputing)
	report, err := internal.BuildStackStatus(ws, cfg, f.Feature, f.FeaturePath, stack)
	if err != nil {
		t.Fatal(err)
	}
	statuses := 0
	for _, entry := range report.Entries {
		if entry.Ancestry.Status != nil && *entry.Ancestry.Status != "" {
			statuses++
		}
	}
	if statuses == 0 {
		t.Fatal("ancestry status must still be reported while a reparent is recorded")
	}
	after := internal.SuppressReparentAncestryGuidance(before)
	for _, e := range after {
		if e.Guidance != "" {
			t.Fatalf("the seam must clear every guidance string, %s still carries %q", e.Name, e.Guidance)
		}
		if e.Status == "" || e.Reason == "" {
			t.Fatalf("the seam must not touch status or reason, %s lost one", e.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// reparentRelocateToLegacyLayout moves the feature from `.tws/features/<f>` to
// the legacy `.tws/<f>` layout, which is what makes ws.CheckoutStateDir() and
// checkoutStateDir(featurePath) resolve to different directories.
func reparentRelocateToLegacyLayout(t *testing.T, f *reparentFixture) *reparentFixture {
	t.Helper()
	legacy := filepath.Join(f.Repo, ".tws", f.Feature)
	if err := os.Rename(f.FeaturePath, legacy); err != nil {
		t.Fatalf("relocating to the legacy layout: %v", err)
	}
	f.FeaturePath = legacy
	return f
}

// reparentPlantRemoteRecord writes a pending §12.3 record without running a
// transaction, so a push or observability cell costs one file write.
func reparentPlantRemoteRecord(t *testing.T, f *reparentFixture, name, branch, newTip, remoteSHA string) internal.ReparentRemoteRecord {
	t.Helper()
	rec := internal.ReparentRemoteRecord{
		RecordVersion: internal.ReparentRemoteRecordVersion,
		Feature:       f.Feature,
		RunID:         strings.Repeat("c", 32),
		Entries: []internal.ReparentRemoteEntry{{
			Name:             name,
			GitBranch:        branch,
			Remote:           internal.ReparentRemoteName,
			RemoteRef:        "refs/remotes/" + internal.ReparentRemoteName + "/" + branch,
			NewTipSHA:        newTip,
			RemoteSHAAtWrite: remoteSHA,
			PRBaseBefore:     "feat-pr1",
			PRBaseAfter:      "master",
			State:            internal.ReparentRemoteStatePending,
		}},
	}
	if err := internal.SaveReparentRemoteRecord(f.Loc(), rec); err != nil {
		t.Fatalf("planting the remote record: %v", err)
	}
	t.Cleanup(func() { _ = internal.RemoveReparentRemoteRecord(f.Loc()) })
	return rec
}
