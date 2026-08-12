package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// doctorFeatureContext resolves the workspace and config the way doctorCmd
// does, so tests exercise checkFeatureE exactly as the command does.
func doctorFeatureContext(t *testing.T) (internal.Workspace, internal.Config) {
	t.Helper()
	cfg := internal.LoadConfig()
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return internal.Workspace{}, cfg
	}
	return ws, cfg
}

// withSiblingWorkspaceEnv points TWS_ROOT at the sibling metadata root that
// RequireWorkspace derives for an external repository, so the feature paths
// used by createWorktree and by RequireFeaturePath agree.
func withSiblingWorkspaceEnv(t *testing.T, repo string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	canonical := repo
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		canonical = filepath.Clean(resolved)
	}
	t.Setenv("TWS_ROOT", canonical+".tws")
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
}

func externalGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// canonicalMainRoot mirrors the evaluator's candidate normalisation.
func canonicalMainRoot(t *testing.T, path string) string {
	t.Helper()
	root, err := internal.MainRepoRootIn(path)
	if err != nil {
		t.Fatalf("MainRepoRootIn(%q): %v", path, err)
	}
	if resolved, rErr := filepath.EvalSymlinks(root); rErr == nil {
		return filepath.Clean(resolved)
	}
	abs, _ := filepath.Abs(root)
	return filepath.Clean(abs)
}

func countAncestryWarnings(issues []internal.HealthIssue) int {
	n := 0
	for _, issue := range issues {
		if issue.EffectiveSeverity() == internal.SeverityWarning {
			n++
		}
	}
	return n
}

// ---------- AC 31: external doctor reports ancestry ----------

func TestExternalDoctor_AncestryReported(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)

	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	// Advance the parent behind the child's back.
	externalGit(t, repo, "commit", "--allow-empty", "-m", "advance main")

	ws, cfg := doctorFeatureContext(t)
	featurePath, err := internal.RequireFeaturePath("extfeat")
	if err != nil {
		t.Fatal(err)
	}
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	edges, res := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, stack)
	if res.RepoDir == "" {
		t.Fatalf("expected a resolved repository, got %+v", res)
	}
	issues := internal.AncestryHealthIssues(res, edges)
	found := false
	for _, issue := range issues {
		if issue.Branch == "branch1" && strings.Contains(issue.Problem, "ancestry stale") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a stale ancestry issue naming branch1, got %+v (edges %+v)", issues, edges)
	}

	var counted int
	out := captureStdout(t, func() {
		counted, err = checkFeatureE(ws, cfg, "extfeat")
	})
	if err != nil {
		t.Fatalf("checkFeatureE: %v", err)
	}
	if counted != countAncestryWarnings(issues) {
		t.Errorf("printed total %d, want %d", counted, countAncestryWarnings(issues))
	}
	if !strings.Contains(out, "ancestry stale") {
		t.Errorf("expected the ancestry issue in output:\n%s", out)
	}
}

// ---------- AC 32: repository unavailable fails soft ----------

func TestExternalDoctor_RepoUnavailable(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)

	// An external workspace with no materialized worktree and no inferable
	// source repository. It is deliberately not a `<repo>.tws` sibling, so
	// nothing can infer a repository back from its path.
	isolated := filepath.Join(t.TempDir(), "detached-workspace")
	featurePath := filepath.Join(isolated, "orphanfeat")
	if err := os.MkdirAll(featurePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(isolated); err != nil {
		t.Fatal(err)
	}
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "a", Base: "main"},
		{Name: "b", Base: "a"},
		{Name: "c", Base: "b"},
	}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}

	ws := internal.Workspace{MetadataRoot: isolated, Mode: internal.ModeExternal}

	probe := internal.ResolveStackAncestryRepo(ws, internal.Config{}, featurePath, stack)
	if probe.RepoDir != "" {
		t.Fatalf("expected an unresolvable repository, got %+v", probe)
	}
	edges, res := internal.FeatureStackEdges(ws, internal.Config{}, "orphanfeat", featurePath, stack)
	issues := internal.AncestryHealthIssues(res, edges)

	if len(issues) != 1 {
		t.Fatalf("expected exactly one collapsed issue regardless of entry count, got %+v", issues)
	}
	if issues[0].Branch != "stack" || issues[0].EffectiveSeverity() != internal.SeverityInfo {
		t.Errorf("unexpected issue: %+v", issues[0])
	}
	if internal.CountHealthIssues(issues) != 0 {
		t.Error("an informational issue must not change the total")
	}

	var counted int
	var err error
	out := captureStdout(t, func() {
		counted, err = checkFeatureE(ws, internal.Config{}, "orphanfeat")
	})
	if err != nil {
		t.Fatalf("checkFeatureE: %v", err)
	}
	if counted != 0 {
		t.Errorf("counted = %d, want the pre-feature total of 0", counted)
	}
	if !strings.Contains(out, "healthy (") {
		t.Errorf("the healthy line must still be printed:\n%s", out)
	}
	if !strings.Contains(out, "repo-unavailable") {
		t.Errorf("the info issue must still be printed:\n%s", out)
	}

	// The non-Git health checks still run and still report when broken.
	if err := createWorktree("brokenfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	wt := internal.WorktreePath("brokenfeat", "branch1")
	externalGit(t, wt, "checkout", "-b", "unexpected")
	brokenIssues := internal.CheckFeatureHealth(internal.FeaturePath("brokenfeat"))
	if internal.CountHealthIssues(brokenIssues) == 0 {
		t.Error("worktree branch mismatch must still be reported")
	}
}

// ---------- AC 33: wrong TWS_ROOT ----------

func TestExternalDoctor_WrongTwsRoot(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	other := setupGitRepo(t, "main")
	otherRoot := filepath.Join(t.TempDir(), "other-workspace")
	if err := os.MkdirAll(otherRoot, 0755); err != nil {
		t.Fatal(err)
	}

	featurePath := internal.FeaturePath("extfeat")
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}

	// The metadata root belongs to a different repository than the feature's
	// own worktree evidence.
	ws := internal.Workspace{
		Mode:         internal.ModeExternal,
		RepoRoot:     other,
		MetadataRoot: otherRoot,
	}
	edges, res := internal.FeatureStackEdges(ws, internal.Config{}, "extfeat", featurePath, stack)

	if res.Source != internal.StackRepoWorktree {
		t.Fatalf("repo source = %q, want worktree (res %+v)", res.Source, res)
	}
	wantRoot := canonicalMainRoot(t, internal.WorktreePath("extfeat", "branch1"))
	if res.RepoDir != wantRoot {
		t.Errorf("repo dir = %q, want %q", res.RepoDir, wantRoot)
	}
	if res.Alternate != canonicalMainRoot(t, other) {
		t.Errorf("alternate = %q, want %q", res.Alternate, canonicalMainRoot(t, other))
	}
	for _, e := range edges {
		if e.Status == "" {
			t.Errorf("edge %s was not evaluated: %+v", e.Name, e)
		}
		for _, note := range e.Notes {
			if string(note.Kind) == internal.RepoSourceMismatchLabel {
				t.Error("repo-source-mismatch must never be an edge note")
			}
		}
	}

	issues := internal.AncestryHealthIssues(res, edges)
	mismatches := 0
	for _, issue := range issues {
		if strings.HasPrefix(issue.Problem, internal.RepoSourceMismatchLabel) {
			mismatches++
			if issue.Branch != "stack" {
				t.Errorf("mismatch issue branch = %q, want stack", issue.Branch)
			}
			if issue.EffectiveSeverity() != internal.SeverityInfo {
				t.Errorf("mismatch issue severity = %q, want info", issue.EffectiveSeverity())
			}
		}
	}
	if mismatches != 1 {
		t.Errorf("expected exactly one repo-source-mismatch issue, got %d", mismatches)
	}
	if internal.CountHealthIssues(issues) != countAncestryWarnings(issues) {
		t.Error("informational ancestry findings must never be counted")
	}
}

// ---------- AC 55: an ordinary external workspace has no mismatch ----------

func TestStackAncestry_NormalWorktreeNoMismatch(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	ws, cfg := doctorFeatureContext(t)
	featurePath := internal.FeaturePath("extfeat")
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	edges, res := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, stack)

	if res.Alternate != "" {
		t.Errorf("alternate = %q, want empty for a linked worktree of the same repository", res.Alternate)
	}
	if res.RepoDir == "" {
		t.Fatal("expected a resolved repository")
	}
	for _, issue := range internal.AncestryHealthIssues(res, edges) {
		if strings.HasPrefix(issue.Problem, internal.RepoSourceMismatchLabel) {
			t.Errorf("unexpected mismatch issue: %+v", issue)
		}
	}
	for _, e := range edges {
		if e.Status == "" {
			t.Errorf("edge %s was not evaluated", e.Name)
		}
	}

	var counted int
	_ = captureStdout(t, func() {
		counted, err = checkFeatureE(ws, cfg, "extfeat")
	})
	if err != nil {
		t.Fatal(err)
	}
	if counted != 0 {
		t.Errorf("counted = %d, want 0 for a healthy external feature", counted)
	}
}

// ---------- AC 27: archived entries are uncounted ----------

func TestExternalDoctor_ArchivedUncounted(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	externalGit(t, repo, "commit", "--allow-empty", "-m", "advance main")

	ws, cfg := doctorFeatureContext(t)
	featurePath := internal.FeaturePath("extfeat")

	archived := internal.Stack{Branches: []internal.StackEntry{
		{Name: "branch1", Base: "main", Archived: true},
		{Name: "ghost", Base: "main", Archived: true},
	}}
	archivedEdges, archivedRes := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, archived)
	archivedIssues := internal.AncestryHealthIssues(archivedRes, archivedEdges)
	if internal.CountHealthIssues(archivedIssues) != 0 {
		t.Errorf("archived ancestry must not be counted: %+v", archivedIssues)
	}
	if len(archivedIssues) == 0 {
		t.Error("the informational lines must still be produced")
	}

	empty, emptyRes := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, internal.Stack{})
	if internal.CountHealthIssues(internal.AncestryHealthIssues(emptyRes, empty)) != 0 {
		t.Error("an empty stack contributes nothing")
	}
}

// ---------- AC 42: exit semantics ----------

func TestExternalDoctor_ExitSemantics(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	externalGit(t, repo, "commit", "--allow-empty", "-m", "advance main")

	ws, cfg := doctorFeatureContext(t)
	var err error
	_ = captureStdout(t, func() {
		_, err = checkFeatureE(ws, cfg, "extfeat")
	})
	if err != nil {
		t.Errorf("a stale ancestry edge must exit 0, got %v", err)
	}
}

// ---------- AC 49: no new CLI surface ----------

func TestAncestry_NoNewCLIFlags(t *testing.T) {
	doctor := doctorCmd()
	if doctor.Flags().HasFlags() {
		t.Errorf("doctor must gain no flags, got %v", doctor.Flags())
	}
	doctor.SetArgs([]string{"--json"})
	doctor.SetOut(&bytes.Buffer{})
	doctor.SetErr(&bytes.Buffer{})
	if err := doctor.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --json") {
		t.Errorf("doctor --json should be an unknown flag, got %v", err)
	}

	list := listCmd()
	if list.Flags().HasFlags() {
		t.Errorf("list must gain no flags, got %v", list.Flags())
	}
	list.SetArgs([]string{"--json"})
	list.SetOut(&bytes.Buffer{})
	list.SetErr(&bytes.Buffer{})
	if err := list.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --json") {
		t.Errorf("list --json should be an unknown flag, got %v", err)
	}
}

// ---------- Revision: doctor runs from every external location ----------

// runDoctorFrom changes into cwd, resolves the workspace exactly as doctorCmd
// does, and runs one feature check, returning the printed output.
func runDoctorFrom(t *testing.T, cwd, feature string) (string, int, error) {
	t.Helper()
	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir %s: %v", cwd, err)
	}
	ws, cfg := doctorFeatureContext(t)
	var counted int
	var err error
	out := captureStdout(t, func() {
		counted, err = checkFeatureE(ws, cfg, feature)
	})
	if chErr := os.Chdir(oldCWD); chErr != nil {
		t.Fatal(chErr)
	}
	return out, counted, err
}

// TestExternalDoctor_InvocationMatrix covers matrix rows 4-8: the same fixture
// must produce the same classification from the repository root, from inside a
// linked worktree, from a nested directory in it, from the workspace root, and
// from the feature directory.
func TestExternalDoctor_InvocationMatrix(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	externalGit(t, repo, "commit", "--allow-empty", "-m", "advance main")

	worktree := internal.WorktreePath("extfeat", "branch1")
	nested := filepath.Join(worktree, "nested", "deeper")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := internal.TwsRoot()
	featureDir := internal.FeaturePath("extfeat")
	nestedFeatureDir := filepath.Join(featureDir, "worktrees")

	cases := []struct {
		name string
		cwd  string
	}{
		{"repo root", repo},
		{"linked worktree", worktree},
		{"nested in worktree", nested},
		{"workspace root", workspaceRoot},
		{"feature directory", featureDir},
		{"nested feature directory", nestedFeatureDir},
	}

	var want string
	for i, tc := range cases {
		out, counted, err := runDoctorFrom(t, tc.cwd, "extfeat")
		if err != nil {
			t.Fatalf("%s: checkFeatureE: %v", tc.name, err)
		}
		if !strings.Contains(out, "ancestry stale") {
			t.Errorf("%s: expected the stale ancestry finding in:\n%s", tc.name, out)
		}
		if counted != 1 {
			t.Errorf("%s: counted = %d, want 1", tc.name, counted)
		}
		if strings.Contains(out, internal.RepoSourceMismatchLabel) {
			t.Errorf("%s: an ordinary workspace must produce no mismatch:\n%s", tc.name, out)
		}
		if i == 0 {
			want = out
			continue
		}
		if out != want {
			t.Errorf("%s: output differs from the repo-root invocation:\ngot:\n%s\nwant:\n%s", tc.name, out, want)
		}
	}
}

// TestExternalDoctor_RunsFromWorkspaceRootWithoutRepo covers matrix row 10 from
// a directory where no repository can be derived at all: doctor must still run,
// report evaluation as unavailable, and keep the non-Git checks working.
func TestExternalDoctor_RunsFromWorkspaceRootWithoutRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := filepath.Join(t.TempDir(), "detached-workspace")
	featurePath := filepath.Join(workspaceRoot, "orphanfeat")
	if err := os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "a", Base: "main"},
		{Name: "b", Base: "a"},
	}}
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", workspaceRoot)

	for _, cwd := range []string{workspaceRoot, featurePath, filepath.Join(featurePath, "worktrees")} {
		out, counted, err := runDoctorFrom(t, cwd, "orphanfeat")
		if err != nil {
			t.Fatalf("cwd %s: checkFeatureE must not fail when no repository exists: %v", cwd, err)
		}
		if counted != 0 {
			t.Errorf("cwd %s: counted = %d, want 0", cwd, counted)
		}
		if !strings.Contains(out, "healthy (") {
			t.Errorf("cwd %s: the regular checks must still report:\n%s", cwd, out)
		}
		if !strings.Contains(out, "repo-unavailable") {
			t.Errorf("cwd %s: expected the informational evaluation-unavailable line:\n%s", cwd, out)
		}
		if strings.Count(out, "repo-unavailable") != 1 {
			t.Errorf("cwd %s: the unavailable finding must collapse to one line:\n%s", cwd, out)
		}
	}
}

// TestExternalDoctor_RejectsUnsafeFeatureNames pins that moving off
// RequireFeaturePath kept the strict feature-name and sibling-space guards.
func TestExternalDoctor_RejectsUnsafeFeatureNames(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	ws, cfg := doctorFeatureContext(t)

	for _, name := range []string{"", "../escape", "a/b", "..", ".hidden", "state"} {
		var err error
		_ = captureStdout(t, func() {
			_, err = checkFeatureE(ws, cfg, name)
		})
		if err == nil {
			t.Errorf("feature name %q must be refused", name)
		}
	}

	// The same guard applies with no resolvable workspace at all.
	empty := internal.Workspace{}
	for _, name := range []string{"../escape", "a/b"} {
		var err error
		_ = captureStdout(t, func() {
			_, err = checkFeatureE(empty, cfg, name)
		})
		if err == nil {
			t.Errorf("feature name %q must be refused without a workspace either", name)
		}
	}
}

// TestExternalDoctor_SpaceNameConflictStillRefused pins that the sibling-space
// guard survived the move off RequireFeaturePath.
func TestExternalDoctor_SpaceNameConflictStillRefused(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	root := internal.TwsRoot()
	spaceDir := filepath.Join(root, "notes")
	if err := os.MkdirAll(spaceDir, 0755); err != nil {
		t.Fatal(err)
	}
	spaces := "spaces:\n  - name: notes\n    path: notes\n    kind: docs\n"
	if err := os.WriteFile(filepath.Join(root, "spaces.yaml"), []byte(spaces), 0644); err != nil {
		t.Fatal(err)
	}

	ws, cfg := doctorFeatureContext(t)
	var err error
	_ = captureStdout(t, func() {
		_, err = checkFeatureE(ws, cfg, "notes")
	})
	if err == nil {
		t.Fatal("a feature name owned by a sibling space must be refused")
	}
}

// ---------- Revision: base-unset renders as unevaluated externally ----------

func TestExternalDoctor_BaseUnsetUnevaluated(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	featurePath := internal.FeaturePath("extfeat")
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	stack.Branches[0].Base = ""
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}

	ws, cfg := doctorFeatureContext(t)
	edges, res := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, stack)
	if edges[0].Status != "" || edges[0].Reason != internal.ReasonBaseUnset {
		t.Fatalf("edge = %+v, want an unevaluated base-unset edge", edges[0])
	}
	if edges[0].BaseRecord != "" {
		t.Errorf("base record = %q, want the zero value", edges[0].BaseRecord)
	}

	issues := internal.AncestryHealthIssues(res, edges)
	if len(issues) != 1 {
		t.Fatalf("expected exactly one informational issue, got %+v", issues)
	}
	if issues[0].Problem != "ancestry unevaluated: base-unset" {
		t.Errorf("problem = %q", issues[0].Problem)
	}
	if issues[0].EffectiveSeverity() != internal.SeverityInfo {
		t.Errorf("severity = %q, want info", issues[0].EffectiveSeverity())
	}
	if internal.CountHealthIssues(issues) != 0 {
		t.Error("a base-unset entry must never be counted")
	}

	var counted int
	out := captureStdout(t, func() {
		counted, err = checkFeatureE(ws, cfg, "extfeat")
	})
	if err != nil {
		t.Fatalf("checkFeatureE: %v", err)
	}
	if counted != 0 {
		t.Errorf("counted = %d, want 0", counted)
	}
	if !strings.Contains(out, "ancestry unevaluated: base-unset") {
		t.Errorf("expected the unevaluated rendering in:\n%s", out)
	}
	if !strings.Contains(out, "healthy (") {
		t.Errorf("the healthy line must still be printed:\n%s", out)
	}
	if strings.Contains(out, "base-record") {
		t.Errorf("an unevaluated edge must claim nothing about the base record:\n%s", out)
	}
}

// ---------- Revision: external mode emits only its own identity note ----------

func TestExternalDoctor_ModeSpecificIdentityNotes(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	// Move origin/main ahead of local main, which is exactly the ref external
	// sync would resolve and record.
	externalGit(t, repo, "checkout", "-b", "pusher", "main")
	externalGit(t, repo, "commit", "--allow-empty", "-m", "remote advance")
	externalGit(t, repo, "push", "origin", "pusher:main")
	externalGit(t, repo, "checkout", "main")
	externalGit(t, repo, "fetch", "origin")

	ws, cfg := doctorFeatureContext(t)
	featurePath := internal.FeaturePath("extfeat")
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	edges, res := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, stack)

	kinds := map[internal.StackNoteKind]int{}
	for _, e := range edges {
		for _, note := range e.Notes {
			kinds[note.Kind]++
		}
	}
	if kinds[internal.NoteBaseIdentityRemoteMismatch] != 1 {
		t.Fatalf("expected exactly one remote mismatch note in external mode, got %v (edges %+v)", kinds, edges)
	}
	if kinds[internal.NoteBaseIdentityLiteralMismatch] != 0 {
		t.Error("external mode must never emit the checkout-sync note")
	}

	issues := internal.AncestryHealthIssues(res, edges)
	notes := 0
	for _, issue := range issues {
		if strings.HasPrefix(issue.Problem, "ancestry note:") {
			notes++
			if issue.EffectiveSeverity() != internal.SeverityInfo {
				t.Errorf("note issue severity = %q, want info", issue.EffectiveSeverity())
			}
			if issue.Branch != "branch1" {
				t.Errorf("note issue branch = %q, want the edge name", issue.Branch)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("expected exactly one projected note issue, got %d: %+v", notes, issues)
	}

	var counted int
	out := captureStdout(t, func() {
		counted, err = checkFeatureE(ws, cfg, "extfeat")
	})
	if err != nil {
		t.Fatalf("checkFeatureE: %v", err)
	}
	if !strings.Contains(out, "ancestry note: base-identity-remote-mismatch") {
		t.Errorf("expected the projected note in:\n%s", out)
	}
	if counted != internal.CountHealthIssues(issues) {
		t.Errorf("counted = %d, want %d — notes never change the total", counted, internal.CountHealthIssues(issues))
	}
}

// TestExternalDoctor_NoCheckoutNoteInExternalMode uses a stack whose parent
// entry name also resolves literally: only checkout sync behaves that way, so
// external doctor must stay silent about it.
func TestExternalDoctor_NoCheckoutNoteInExternalMode(t *testing.T) {
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "core", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	featurePath := internal.FeaturePath("extfeat")
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	// Decouple the logical name from the Git branch and create a same-named
	// literal ref pointing somewhere else.
	externalGit(t, repo, "branch", "-m", "core", "jd/core")
	externalGit(t, repo, "commit", "--allow-empty", "-m", "advance main")
	externalGit(t, repo, "branch", "core", "main")
	externalGit(t, repo, "branch", "jd/api", "jd/core")

	stack.Branches[0].Branch = "jd/core"
	stack.Branches = append(stack.Branches, internal.StackEntry{Name: "api", Branch: "jd/api", Base: "core"})
	if err := internal.SaveStack(featurePath, stack); err != nil {
		t.Fatal(err)
	}

	ws, cfg := doctorFeatureContext(t)
	edges, _ := internal.FeatureStackEdges(ws, cfg, "extfeat", featurePath, stack)
	for _, e := range edges {
		for _, note := range e.Notes {
			if note.Kind == internal.NoteBaseIdentityLiteralMismatch {
				t.Errorf("external mode emitted the checkout-sync note on edge %s: %+v", e.Name, note)
			}
		}
	}
}

// ---------- Revision: honest help text ----------

// TestDoctorHelp_AncestryWordingIsHonest pins that the command help does not
// promise a repair command for every ancestry state. Several states (cross-repo,
// base-unset, repo-unavailable, probe-failed, unrelated histories) have no
// command at all, and child-ref-missing has only an example.
func TestDoctorHelp_AncestryWordingIsHonest(t *testing.T) {
	long := doctorCmd().Long
	if strings.Contains(long, "exact command that repairs it") {
		t.Errorf("help must not promise an exact repair command per state:\n%s", long)
	}
	for _, want := range []string{
		"reason and actionable guidance when available",
		"cross-repo-unsupported",
		"unevaluated",
		"strictly\nread-only",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("help must mention %q:\n%s", want, long)
		}
	}
}

// ---------- Revision: workspace resolution stays fail-closed ----------

// invalidModeRepo builds a Git repository whose repo-local config carries an
// unparsable workspace_mode, which is exactly what RequireWorkspace rejects.
func invalidModeRepo(t *testing.T) string {
	t.Helper()
	repo := setupGitRepo(t, "main")
	withSiblingWorkspaceEnv(t, repo)
	if err := createWorktree("extfeat", "branch1", "main", "", false); err != nil {
		t.Fatalf("createWorktree: %v", err)
	}
	twsDir := filepath.Join(repo, ".tws")
	if err := os.MkdirAll(twsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twsDir, "config.yaml"), []byte("workspace_mode: bogus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.RequireWorkspace(); err == nil {
		t.Fatal("fixture is wrong: RequireWorkspace must fail on an invalid workspace_mode")
	}
	return repo
}

// TestDoctor_InvalidWorkspaceModeFailsClosed is the baseline-vs-new regression.
// Before this feature, checkFeatureE resolved its path through
// RequireFeaturePath -> RequireWorkspace, so an invalid workspace_mode aborted
// both the filtered and the unfiltered form. Moving to ResolveFeaturePathFor
// dropped that transitive guard, so doctorCmd must re-assert it: a cwd inside a
// Git repository whose persisted config is unusable is an error, never a silent
// external-mode fallback.
func TestDoctor_InvalidWorkspaceModeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"unfiltered", nil},
		{"filtered", []string{"extfeat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalidModeRepo(t)

			cmd := doctorCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			var err error
			_ = captureStdout(t, func() { err = cmd.Execute() })
			if err == nil {
				t.Fatal("doctor must fail closed on an invalid workspace_mode")
			}
			if !strings.Contains(err.Error(), "invalid workspace_mode") {
				t.Errorf("error = %v, want the original workspace resolution error", err)
			}
		})
	}
}

// TestDoctor_RepoUnavailableStillTolerated is the other half: when the cwd is
// in no Git repository at all, a RequireWorkspace failure must still fall
// through so the non-Git checks and the unevaluated ancestry line keep working.
func TestDoctor_RepoUnavailableStillTolerated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := filepath.Join(t.TempDir(), "detached-workspace")
	featurePath := filepath.Join(workspaceRoot, "orphanfeat")
	if err := os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := internal.EnsureExternalWorkspaceMarker(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "a", Base: "main"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWS_ROOT", workspaceRoot)

	oldCWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.MainRepoRoot(); err == nil {
		t.Skip("temporary directory is inside a git repository; the no-repo case cannot be built here")
	}

	cmd := doctorCmd()
	cmd.SetArgs([]string{"orphanfeat"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatalf("doctor must still run without any repository: %v", err)
	}
	if !strings.Contains(out, "repo-unavailable") {
		t.Errorf("expected the unevaluated ancestry line:\n%s", out)
	}
}
