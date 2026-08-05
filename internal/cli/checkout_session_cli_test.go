package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func setupSessionCLI(t *testing.T) (string, internal.Workspace) {
	t.Helper()
	repo := setupGitRepoCheckout(t)
	ws := requireWorkspaceForTest(t, repo)
	fp := ws.FeaturePath("feature")
	if err := os.MkdirAll(filepath.Join(fp, "inject"), 0755); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, repo, "branch", "one")
	if err := internal.SaveStack(fp, internal.Stack{Branches: []internal.StackEntry{{Name: "one", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	return repo, ws
}
func TestCheckoutOpenResolveForms(t *testing.T) {
	_, ws := setupSessionCLI(t)
	for _, args := range [][]string{{"feature", "one"}, {"feature"}, nil} {
		f, n, err := resolveCheckoutOpenArgs(ws, args)
		if err != nil || f != "feature" || n != "one" {
			t.Fatalf("args=%v f=%s n=%s err=%v", args, f, n, err)
		}
	}
}
func TestCheckoutOpenFeatureDirNoBranches(t *testing.T) {
	_, ws := setupSessionCLI(t)
	empty := ws.FeaturePath("empty")
	if err := os.MkdirAll(empty, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := openCmd()
	cmd.SetArgs([]string{"empty", "--feature-dir", "--no-agent"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}
func TestCheckoutNoAgentDoesNotSwitch(t *testing.T) {
	repo, ws := setupSessionCLI(t)
	before := gitInDir(t, repo, "branch", "--show-current")
	if err := runCheckoutOpen(ws, []string{"feature", "one"}, false, false, true, nilFlagSet{}); err != nil {
		t.Fatal(err)
	}
	if after := gitInDir(t, repo, "branch", "--show-current"); after != before {
		t.Fatalf("%s -> %s", before, after)
	}
}
func TestCheckoutCloseResolveState(t *testing.T) {
	_, ws := setupSessionCLI(t)
	s := &internal.CheckoutAgentSession{SchemaVersion: 1, WorkspaceID: ws.StableID, Feature: "feature", Name: "one", GitBranch: "one", OriginalBranch: "main", OriginalHEAD: "x", Mode: internal.AgentSessionTmux, LockToken: "x"}
	if err := internal.SaveCheckoutAgentSession(ws, s); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"feature"}, {"feature", "one"}} {
		f, n, err := resolveCheckoutCloseArgs(ws, args)
		if err != nil || f != "feature" || n != "one" {
			t.Fatalf("%v %s %s %v", args, f, n, err)
		}
	}
}
func TestExternalCloseStillRequiresTwoArgs(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	if err := os.RemoveAll(filepath.Join(repo, ".tws")); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	_ = os.Chdir(repo)
	cmd := closeCmd()
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("%v", err)
	}
}

type nilFlagSet struct{}

func (nilFlagSet) Changed(string) bool { return false }
