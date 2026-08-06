package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"gopkg.in/yaml.v3"
)

// ---------- Checkout doctor CLI ----------

func TestCheckoutDoctor_HealthyOutput(t *testing.T) {
	dir := setupGitRepoCheckout(t)
	// Prevent .tws from making repo dirty
	gitInDir(t, dir, "add", "-A")
	gitInDir(t, dir, "commit", "-m", "track tws")
	ws := requireWorkspaceForTest(t, dir)

	fp := ws.FeaturePath("myfeat")
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	stack := internal.Stack{
		Branches: []internal.StackEntry{
			{Name: "feat-branch", Base: "main"},
		},
	}
	if err := internal.SaveStack(fp, stack); err != nil {
		t.Fatal(err)
	}
	// Commit feature metadata so repo stays clean
	gitInDir(t, dir, "add", "-A")
	gitInDir(t, dir, "commit", "-m", "add feature")

	// Create feat-branch from current HEAD so it's current with main
	gitInDir(t, dir, "branch", "feat-branch")

	// Run doctor via CLI
	report, err := internal.BuildCheckoutHealthReport(ws, nil)
	if err != nil {
		t.Fatalf("build report failed: %v", err)
	}

	output := internal.FormatCheckoutHealth(report)
	if !strings.Contains(output, "All healthy") {
		t.Errorf("expected healthy output, got:\n%s", output)
	}
	if !strings.Contains(output, "checkout") {
		t.Errorf("expected checkout in output, got:\n%s", output)
	}
}

func TestCheckoutDoctor_ReturnsNilOnWarnings(t *testing.T) {
	dir := setupGitRepoCheckout(t)
	gitInDir(t, dir, "add", "-A")
	gitInDir(t, dir, "commit", "-m", "track tws")
	ws := requireWorkspaceForTest(t, dir)

	// Create a stale sync state (warning level, not error)
	stateDir := ws.CheckoutStateDir()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	tx := internal.CheckoutTransaction{
		Feature: "myfeat",
		Stage:   internal.StageSwitched,
		LockPID: 99999,
		Plan:    []internal.CheckoutPlanEntry{{Branch: "b", Base: "main"}},
	}
	txData, _ := yaml.Marshal(tx)
	if err := os.WriteFile(filepath.Join(stateDir, "myfeat-checkout-sync.yaml"), txData, 0600); err != nil {
		t.Fatal(err)
	}

	// Verify doctor does not return error for warnings
	err := runCheckoutDoctor(ws, "")
	if err != nil {
		t.Errorf("doctor should not return error for warnings, got: %v", err)
	}
}

// ---------- Checkout list CLI ----------

func TestCheckoutList_CLI(t *testing.T) {
	dir := setupGitRepoCheckout(t)
	gitInDir(t, dir, "add", "-A")
	gitInDir(t, dir, "commit", "-m", "track tws")
	ws := requireWorkspaceForTest(t, dir)

	gitInDir(t, dir, "branch", "feat-branch")
	fp := ws.FeaturePath("myfeat")
	if err := os.MkdirAll(fp, 0755); err != nil {
		t.Fatal(err)
	}
	stack := internal.Stack{
		Branches: []internal.StackEntry{
			{Name: "feat-branch", Base: "main"},
		},
	}
	if err := internal.SaveStack(fp, stack); err != nil {
		t.Fatal(err)
	}

	err := runCheckoutList(ws)
	if err != nil {
		t.Fatalf("runCheckoutList failed: %v", err)
	}
}

// ---------- External doctor/list regression ----------

func TestExternalDoctor_UnchangedBehavior(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withWorkspaceEnv(t, repo)

	// Create an external worktree feature
	if err := createWorktree("extfeat", "branch1", "main", repo, false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	issues, err := checkFeatureE("extfeat")
	if err != nil {
		t.Fatalf("checkFeatureE failed: %v", err)
	}
	if issues != 0 {
		t.Errorf("expected 0 issues for healthy external feature, got %d", issues)
	}
}

func TestExternalList_UnchangedBehavior(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withWorkspaceEnv(t, repo)

	if err := createWorktree("extfeat", "branch1", "main", repo, false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	// The list command should work for external mode without errors
	// We can't easily capture stdout in a test, so just verify no error
	ws := internal.Workspace{MetadataRoot: internal.TwsRoot(), Mode: internal.ModeExternal}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatalf("ListFeaturesResolved failed: %v", err)
	}
	if len(features) == 0 {
		t.Error("expected at least one feature for external mode")
	}
}

// ---------- External feature-dir doctor regression ----------

func TestExternalFeatureDir_DoctorRegression(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	if err := createWorktree("ext-feat", "branch1", "master", repo, false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	// Override TWS_ROOT to feature-dir style
	featureDir := filepath.Join(t.TempDir(), "alt-features")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	oldRoot := os.Getenv("TWS_ROOT")
	// Copy feature to alt location
	origFP := filepath.Join(internal.TwsRoot(), "ext-feat")
	altFP := filepath.Join(featureDir, "ext-feat")
	cmd := exec.Command("cp", "-r", origFP, altFP)
	if err := cmd.Run(); err != nil {
		t.Fatalf("copy feature: %v", err)
	}
	t.Setenv("TWS_ROOT", featureDir)
	defer func() {
		if oldRoot == "" {
			t.Setenv("TWS_ROOT", "")
		}
	}()

	// Doctor should still work on external features
	issues, err := checkFeatureE("ext-feat")
	if err != nil {
		t.Fatalf("checkFeatureE in feature-dir: %v", err)
	}
	_ = issues
}
