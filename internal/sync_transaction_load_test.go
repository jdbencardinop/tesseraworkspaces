package internal

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type transactionalLoadFixture struct {
	repo        string
	featurePath string
	mainSHA     string
	childSHA    string
	stack       Stack
	external    *SyncTransaction
	checkout    *SyncTransaction
}

func newTransactionalLoadFixture(t *testing.T) *transactionalLoadFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTransactionalLoadGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTransactionalLoadGit(t, repo, "add", "tracked.txt")
	runTransactionalLoadGit(t, repo,
		"-c", "user.name=TWS Loader Test",
		"-c", "user.email=tws-loader@example.test",
		"commit", "-m", "initial")
	mainSHA := transactionalLoadGitOutput(t, repo, "rev-parse", "main")
	runTransactionalLoadGit(t, repo, "branch", "child")
	childSHA := transactionalLoadGitOutput(t, repo, "rev-parse", "child")

	featurePath := filepath.Join(root, "workspace", ".tws", "features", "feature")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	stack := Stack{Branches: []StackEntry{{
		Name: "child", Branch: "child", Base: "main", LastBaseSHA: mainSHA,
	}}}
	if err := SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}
	input := SyncTransactionBeginInput{
		FeaturePath:       featurePath,
		Feature:           "feature",
		WorkspaceRepoRoot: repo,
		Stack:             stack,
		Selected:          []string{"child"},
		EntryRepoDirs:     map[string]string{"child": repo},
	}
	input.Mode = ModeExternal
	external, err := CaptureSyncTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Mode = ModeCheckout
	checkout, err := CaptureSyncTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	return &transactionalLoadFixture{
		repo: repo, featurePath: featurePath, mainSHA: mainSHA, childSHA: childSHA,
		stack: stack, external: external, checkout: checkout,
	}
}

func runTransactionalLoadGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func transactionalLoadGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func cloneTransactionalLoadValue[T any](t *testing.T, value *T) *T {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone T
	if err := yaml.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func (f *transactionalLoadFixture) externalState(t *testing.T) *SyncRunState {
	t.Helper()
	state := NewSyncRunState(
		"feature",
		"tws-scoped-sync-0123456789abcdef0123456789abcdef.lock",
		"0123456789abcdef0123456789abcdef",
		SyncRunPolicy{
			Fetch:       SyncFetchDisabled,
			Propagation: SyncPropagationFull,
			ScopeKind:   SyncScopeAll,
		},
	)
	state.StateVersion = SyncRunStateTransactionalVersion
	state.Route = RouteNewMode
	state.Selected = []string{"child"}
	state.Pending = []string{"child"}
	state.Repos = []string{""}
	state.ValidationSource = "none"
	state.Transaction = cloneTransactionalLoadValue(t, f.external)
	return state
}

func (f *transactionalLoadFixture) checkoutState(t *testing.T) *CheckoutTransaction {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	return &CheckoutTransaction{
		StateVersion:   CheckoutTransactionTransactionalVersion,
		Feature:        "feature",
		StartedAt:      now,
		LockPID:        os.Getpid(),
		LockCreated:    now,
		OriginalBranch: "main",
		OriginalHEAD:   f.mainSHA,
		Plan: []CheckoutPlanEntry{{
			Name: "child", Branch: "child", Base: "main", LastBaseSHA: f.mainSHA,
			NewBaseSHA: f.mainSHA, PreSHA: f.childSHA,
		}},
		CurrentIndex:      0,
		Stage:             StagePlanned,
		FetchPolicy:       string(SyncFetchDisabled),
		PropagationPolicy: string(SyncPropagationFull),
		ScopeKind:         string(SyncScopeAll),
		Selected:          []string{"child"},
		ValidationSource:  "none",
		Route:             RouteNewMode,
		Transaction:       cloneTransactionalLoadValue(t, f.checkout),
	}
}

func writeTransactionalLoadYAML(t *testing.T, path string, value any) {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionalStateGeneratedRoundTrip(t *testing.T) {
	f := newTransactionalLoadFixture(t)

	external := f.externalState(t)
	if external.Transaction.Ready {
		t.Fatal("fixture must exercise the valid pre-pin birth state")
	}
	if err := SaveSyncRunState(f.featurePath, external); err != nil {
		t.Fatalf("SaveSyncRunState: %v", err)
	}
	loadedExternal, err := LoadSyncRunState(f.featurePath)
	if err != nil {
		t.Fatalf("LoadSyncRunState: %v", err)
	}
	if !syncNamesEqual(loadedExternal.Selected, []string{"child"}) || loadedExternal.Transaction == nil {
		t.Fatalf("external round trip lost transactional evidence: %+v", loadedExternal)
	}

	checkout := f.checkoutState(t)
	if checkout.Transaction.Ready {
		t.Fatal("fixture must exercise the valid pre-pin birth state")
	}
	if err := SaveCheckoutTransaction(f.featurePath, checkout); err != nil {
		t.Fatalf("SaveCheckoutTransaction: %v", err)
	}
	loadedCheckout, err := LoadCheckoutTransaction(f.featurePath)
	if err != nil {
		t.Fatalf("LoadCheckoutTransaction: %v", err)
	}
	if len(loadedCheckout.Plan) != 1 || loadedCheckout.Transaction == nil {
		t.Fatalf("checkout round trip lost transactional evidence: %+v", loadedCheckout)
	}

	limit := 0
	guardedExternal := f.externalState(t)
	guardedExternal.StateVersion = SyncRunStateTransactionalGuardedVersion
	guardedExternal.PlanGuarded = true
	guardedExternal.MaxReplayTotal = &limit
	if err := SaveSyncRunState(f.featurePath, guardedExternal); err != nil {
		t.Fatalf("SaveSyncRunState guarded: %v", err)
	}
	if loaded, err := LoadSyncRunState(f.featurePath); err != nil || !loaded.PlanGuarded {
		t.Fatalf("LoadSyncRunState guarded: state=%+v err=%v", loaded, err)
	}

	guardedCheckout := f.checkoutState(t)
	guardedCheckout.StateVersion = CheckoutTransactionTransactionalGuardedVersion
	guardedCheckout.PlanGuarded = true
	guardedCheckout.MaxReplayPerEntry = &limit
	if err := SaveCheckoutTransaction(f.featurePath, guardedCheckout); err != nil {
		t.Fatalf("SaveCheckoutTransaction guarded: %v", err)
	}
	if loaded, err := LoadCheckoutTransaction(f.featurePath); err != nil || !loaded.PlanGuarded {
		t.Fatalf("LoadCheckoutTransaction guarded: state=%+v err=%v", loaded, err)
	}
}

func TestTransactionalLoadersRejectStrictYAMLCorruption(t *testing.T) {
	f := newTransactionalLoadFixture(t)
	cases := []struct {
		name      string
		path      string
		value     any
		stageLine string
		load      func() error
	}{
		{
			name: "external", path: SyncRunStatePath(f.featurePath), value: f.externalState(t),
			stageLine: "stage: initializing\n",
			load: func() error {
				_, err := LoadSyncRunState(f.featurePath)
				return err
			},
		},
		{
			name: "checkout", path: CheckoutTransactionPath(f.featurePath), value: f.checkoutState(t),
			stageLine: "stage: planned\n",
			load: func() error {
				_, err := LoadCheckoutTransaction(f.featurePath)
				return err
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			base, err := yaml.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			malformed := bytes.Replace(base, []byte(tc.stageLine), []byte("stage:\n    - malformed\n"), 1)
			if bytes.Equal(malformed, base) {
				t.Fatalf("fixture did not contain %q", tc.stageLine)
			}
			variants := []struct {
				name string
				data []byte
			}{
				{"trailing-document", append(append([]byte{}, base...), []byte("---\nstate_version: 4\n")...)},
				{"unknown-key", append(append([]byte{}, base...), []byte("unexpected_loader_key: true\n")...)},
				{"duplicate-key", append(append([]byte{}, base...), []byte("state_version: 4\n")...)},
				{"malformed-field", malformed},
			}
			for _, variant := range variants {
				variant := variant
				t.Run(variant.name, func(t *testing.T) {
					if err := os.MkdirAll(filepath.Dir(tc.path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(tc.path, variant.data, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := tc.load(); err == nil {
						t.Fatal("corrupt transactional YAML was accepted")
					}
				})
			}
		})
	}
}

func TestTransactionalLoadersRejectOuterCorruption(t *testing.T) {
	f := newTransactionalLoadFixture(t)
	foreignMetadata := filepath.Join(t.TempDir(), "foreign", "stack.yaml")
	one := 1

	externalCases := []struct {
		name   string
		mutate func(*SyncRunState)
	}{
		{"missing-evidence", func(state *SyncRunState) { state.Transaction = nil }},
		{"foreign-feature", func(state *SyncRunState) {
			state.Feature = "other"
			state.Transaction.Feature = "other"
		}},
		{"foreign-mode", func(state *SyncRunState) { state.Transaction.WorkspaceMode = string(ModeCheckout) }},
		{"foreign-metadata-path", func(state *SyncRunState) { state.Transaction.Metadata.Path = foreignMetadata }},
		{"invalid-stage", func(state *SyncRunState) { state.Stage = "sideways" }},
		{"invalid-route", func(state *SyncRunState) { state.Route = "sideways" }},
		{"selection-mismatch", func(state *SyncRunState) { state.Selected = append(state.Selected, "other") }},
		{"invalid-policy", func(state *SyncRunState) { state.FetchPolicy = "sometimes" }},
		{"unguarded-limits", func(state *SyncRunState) { state.MaxReplayTotal = &one }},
		{"plan-guarded-version-mismatch", func(state *SyncRunState) { state.PlanGuarded = true }},
		{"unknown-version", func(state *SyncRunState) { state.StateVersion = 6 }},
	}
	for _, tc := range externalCases {
		tc := tc
		t.Run("external/"+tc.name, func(t *testing.T) {
			state := f.externalState(t)
			tc.mutate(state)
			writeTransactionalLoadYAML(t, SyncRunStatePath(f.featurePath), state)
			if _, err := LoadSyncRunState(f.featurePath); err == nil {
				t.Fatal("corrupt external envelope was accepted")
			}
		})
	}

	checkoutCases := []struct {
		name   string
		mutate func(*CheckoutTransaction)
	}{
		{"missing-evidence", func(state *CheckoutTransaction) { state.Transaction = nil }},
		{"foreign-feature", func(state *CheckoutTransaction) {
			state.Feature = "other"
			state.Transaction.Feature = "other"
		}},
		{"foreign-mode", func(state *CheckoutTransaction) { state.Transaction.WorkspaceMode = string(ModeExternal) }},
		{"foreign-metadata-path", func(state *CheckoutTransaction) { state.Transaction.Metadata.Path = foreignMetadata }},
		{"invalid-stage", func(state *CheckoutTransaction) { state.Stage = "sideways" }},
		{"invalid-route", func(state *CheckoutTransaction) { state.Route = "sideways" }},
		{"selection-mismatch", func(state *CheckoutTransaction) { state.Selected = append(state.Selected, "other") }},
		{"invalid-policy", func(state *CheckoutTransaction) { state.ScopeKind = "some" }},
		{"unguarded-limits", func(state *CheckoutTransaction) { state.MaxReplayPerEntry = &one }},
		{"plan-guarded-version-mismatch", func(state *CheckoutTransaction) { state.PlanGuarded = true }},
		{"invalid-plan", func(state *CheckoutTransaction) { state.Plan[0].Name = "other" }},
		{"invalid-progress", func(state *CheckoutTransaction) { state.CurrentIndex = len(state.Plan) + 1 }},
		{"unknown-version", func(state *CheckoutTransaction) { state.StateVersion = 6 }},
	}
	for _, tc := range checkoutCases {
		tc := tc
		t.Run("checkout/"+tc.name, func(t *testing.T) {
			state := f.checkoutState(t)
			tc.mutate(state)
			writeTransactionalLoadYAML(t, CheckoutTransactionPath(f.featurePath), state)
			if _, err := LoadCheckoutTransaction(f.featurePath); err == nil {
				t.Fatal("corrupt checkout envelope was accepted")
			}
		})
	}
}

func TestTransactionalSaversRejectOuterCorruption(t *testing.T) {
	f := newTransactionalLoadFixture(t)

	externalCases := []struct {
		name   string
		mutate func(*SyncRunState)
	}{
		{"nil-evidence", func(state *SyncRunState) { state.Transaction = nil }},
		{"foreign-path", func(state *SyncRunState) {
			state.Transaction.Metadata.Path = filepath.Join(t.TempDir(), "stack.yaml")
		}},
		{"invalid-stage", func(state *SyncRunState) { state.Stage = "sideways" }},
		{"unknown-version", func(state *SyncRunState) { state.StateVersion = 6 }},
	}
	for _, tc := range externalCases {
		tc := tc
		t.Run("external/"+tc.name, func(t *testing.T) {
			state := f.externalState(t)
			tc.mutate(state)
			if err := SaveSyncRunState(f.featurePath, state); err == nil {
				t.Fatal("SaveSyncRunState accepted corrupt state")
			}
		})
	}

	checkoutCases := []struct {
		name   string
		mutate func(*CheckoutTransaction)
	}{
		{"nil-evidence", func(state *CheckoutTransaction) { state.Transaction = nil }},
		{"foreign-path", func(state *CheckoutTransaction) {
			state.Transaction.Metadata.Path = filepath.Join(t.TempDir(), "stack.yaml")
		}},
		{"invalid-progress", func(state *CheckoutTransaction) { state.CurrentIndex = 2 }},
		{"unknown-version", func(state *CheckoutTransaction) { state.StateVersion = 6 }},
	}
	for _, tc := range checkoutCases {
		tc := tc
		t.Run("checkout/"+tc.name, func(t *testing.T) {
			state := f.checkoutState(t)
			tc.mutate(state)
			if err := SaveCheckoutTransaction(f.featurePath, state); err == nil {
				t.Fatal("SaveCheckoutTransaction accepted corrupt state")
			}
		})
	}
}

func TestTransactionalLoadersRejectDowngradedEvidence(t *testing.T) {
	f := newTransactionalLoadFixture(t)

	external := f.externalState(t)
	external.StateVersion = SyncRunStateGuardedVersion
	writeTransactionalLoadYAML(t, SyncRunStatePath(f.featurePath), external)
	if _, err := LoadSyncRunState(f.featurePath); err == nil {
		t.Fatal("external transaction hidden under legacy state_version was accepted")
	}
	if err := SaveSyncRunState(f.featurePath, external); err == nil {
		t.Fatal("external saver accepted downgraded transactional evidence")
	}

	checkout := f.checkoutState(t)
	checkout.StateVersion = CheckoutTransactionGuardedVersion
	writeTransactionalLoadYAML(t, CheckoutTransactionPath(f.featurePath), checkout)
	if _, err := LoadCheckoutTransaction(f.featurePath); err == nil {
		t.Fatal("checkout transaction hidden under legacy state_version was accepted")
	}
	if err := SaveCheckoutTransaction(f.featurePath, checkout); err == nil {
		t.Fatal("checkout saver accepted downgraded transactional evidence")
	}

	legacyExternal := NewSyncRunState("feature", "legacy", "legacy", SyncRunPolicy{})
	legacyExternal.StateVersion = SyncRunStateVersion
	data, err := yaml.Marshal(legacyExternal)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("transaction: null\n")...)
	if err := os.WriteFile(SyncRunStatePath(f.featurePath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSyncRunState(f.featurePath); err == nil {
		t.Fatal("legacy external state carrying a null transaction field was accepted")
	}

	legacyCheckout := &CheckoutTransaction{StateVersion: 1, Feature: "feature", Stage: StagePlanned}
	data, err = yaml.Marshal(legacyCheckout)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("plan_guarded: false\n")...)
	if err := os.WriteFile(CheckoutTransactionPath(f.featurePath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckoutTransaction(f.featurePath); err == nil {
		t.Fatal("legacy checkout state carrying plan_guarded was accepted")
	}

	mergedDowngrade := []byte(`state_version: 3
defaults: &defaults
  transaction: null
<<: *defaults
feature: feature
`)
	if err := os.WriteFile(SyncRunStatePath(f.featurePath), mergedDowngrade, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSyncRunState(f.featurePath); err == nil {
		t.Fatal("legacy state hid downgraded evidence behind a YAML merge")
	}
}

func TestTransactionalLoadersRejectRelocatedState(t *testing.T) {
	f := newTransactionalLoadFixture(t)
	external := f.externalState(t)
	if err := SaveSyncRunState(f.featurePath, external); err != nil {
		t.Fatal(err)
	}
	externalData, err := os.ReadFile(SyncRunStatePath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}
	checkout := f.checkoutState(t)
	if err := SaveCheckoutTransaction(f.featurePath, checkout); err != nil {
		t.Fatal(err)
	}
	checkoutData, err := os.ReadFile(CheckoutTransactionPath(f.featurePath))
	if err != nil {
		t.Fatal(err)
	}

	relocated := filepath.Join(t.TempDir(), "moved", ".tws", "features", "feature")
	if err := os.MkdirAll(relocated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SyncRunStatePath(relocated), externalData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSyncRunState(relocated); err == nil {
		t.Fatal("relocated external state was accepted with its foreign metadata path")
	}
	if err := os.MkdirAll(filepath.Dir(CheckoutTransactionPath(relocated)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CheckoutTransactionPath(relocated), checkoutData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckoutTransaction(relocated); err == nil {
		t.Fatal("relocated checkout state was accepted with its foreign metadata path")
	}
}

func TestTransactionalLoadersPreserveLegacyRoundTrips(t *testing.T) {
	for _, version := range []int{SyncRunStateVersion, SyncRunStateGuardedVersion} {
		t.Run(fmt.Sprintf("external-v%d", version), func(t *testing.T) {
			featurePath := t.TempDir()
			state := NewSyncRunState("legacy", "legacy-marker", "legacy-token", SyncRunPolicy{})
			state.StateVersion = version
			if version == SyncRunStateGuardedVersion {
				limit := 3
				state.Route = RouteLegacy
				state.MaxReplayTotal = &limit
			}
			if err := SaveSyncRunState(featurePath, state); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadSyncRunState(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.StateVersion != version || loaded.Transaction != nil {
				t.Fatalf("legacy external state changed: %+v", loaded)
			}
		})
	}

	for _, version := range []int{0, 1, CheckoutTransactionVersion, CheckoutTransactionGuardedVersion} {
		version := version
		t.Run(fmt.Sprintf("checkout-v%d", version), func(t *testing.T) {
			featurePath := filepath.Join(t.TempDir(), ".tws", "features", "legacy")
			if err := os.MkdirAll(featurePath, 0o755); err != nil {
				t.Fatal(err)
			}
			state := &CheckoutTransaction{
				StateVersion: version,
				Feature:      "legacy",
				Stage:        StagePlanned,
				Plan:         []CheckoutPlanEntry{{Branch: "child", Base: "main"}},
			}
			if version == CheckoutTransactionGuardedVersion {
				limit := 4
				state.Route = RouteLegacy
				state.MaxReplayPerEntry = &limit
			}
			if err := SaveCheckoutTransaction(featurePath, state); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadCheckoutTransaction(featurePath)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.StateVersion != version || loaded.Transaction != nil {
				t.Fatalf("legacy checkout state changed: %+v", loaded)
			}
		})
	}
}

func TestTransactionalLoadersKeepReparentCompatibilityFailClosed(t *testing.T) {
	root := t.TempDir()
	featurePath := filepath.Join(root, ".tws", "features", "feature")
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTransactionalLoadYAML(t, SyncRunStatePath(featurePath), ReparentCompatSyncPayload{
		StateVersion:   ReparentCompatSyncStateVersion,
		Route:          ReparentCompatRoute,
		Feature:        "feature",
		ReparentRunID:  strings.Repeat("a", 32),
		ReparentMarker: ReparentCompatMarker,
	})
	if _, err := LoadSyncRunState(featurePath); err == nil || !strings.Contains(err.Error(), "reparent compatibility envelope") {
		t.Fatalf("external reparent compatibility state was not deliberately refused: %v", err)
	}

	writeTransactionalLoadYAML(t, CheckoutTransactionPath(featurePath), ReparentCheckoutCompatTransaction{
		StateVersion:   ReparentCheckoutCompatStateVersion,
		Route:          ReparentCompatRoute,
		Feature:        "feature",
		ReparentRunID:  strings.Repeat("a", 32),
		ReparentMarker: ReparentCompatMarker,
	})
	if _, err := LoadCheckoutTransaction(featurePath); err == nil {
		t.Fatal("checkout reparent compatibility state became decodable")
	}
}
