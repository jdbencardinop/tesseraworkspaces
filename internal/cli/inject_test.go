package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

type injectFixture struct {
	root        string
	home        string
	repo        string
	workspace   string
	feature     string
	featurePath string
}

func newInjectFixture(t *testing.T, feature string) *injectFixture {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[user]\n\tname = TWS Inject Test\n\temail = inject@example.test\n\tuseConfigOnly = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")

	repo := createInjectRepo(t, root, "primary")
	workspace := filepath.Join(root, "workspace")
	if err := internal.EnsureExternalWorkspaceMarker(workspace); err != nil {
		t.Fatal(err)
	}
	writeInjectTWSConfig(t, home, repo, workspace, "")
	t.Setenv("TWS_ROOT", filepath.Join(root, "decoy-workspace"))

	featurePath := filepath.Join(workspace, feature)
	if err := os.MkdirAll(filepath.Join(featurePath, "inject"), 0755); err != nil {
		t.Fatal(err)
	}

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	return &injectFixture{
		root:        root,
		home:        home,
		repo:        repo,
		workspace:   workspace,
		feature:     feature,
		featurePath: featurePath,
	}
}

func createInjectRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	remote := filepath.Join(root, name+".git")
	injectGit(t, "", "init", "--bare", remote)
	injectGit(t, "", "init", "--initial-branch=main", repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(name+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	injectGit(t, repo, "add", "README.md")
	injectGit(t, repo, "commit", "-m", "initial")
	injectGit(t, repo, "remote", "add", "origin", remote)
	injectGit(t, repo, "push", "-u", "origin", "main")
	injectGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return repo
}

func injectGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{}, args...)
	if dir != "" {
		commandArgs = append([]string{"-C", dir}, commandArgs...)
	}
	cmd := exec.Command("git", commandArgs...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=TWS Inject Test",
		"GIT_AUTHOR_EMAIL=inject@example.test",
		"GIT_COMMITTER_NAME=TWS Inject Test",
		"GIT_COMMITTER_EMAIL=inject@example.test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", commandArgs, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeInjectTWSConfig(t *testing.T, home, repo, workspace, injectInto string) {
	t.Helper()
	configPath := filepath.Join(home, ".config", "tws", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("workspaces:\n  %q: %q\n", repo, workspace)
	if canonicalRepo, err := filepath.EvalSymlinks(repo); err == nil && canonicalRepo != repo {
		content += fmt.Sprintf("  %q: %q\n", canonicalRepo, workspace)
	}
	if injectInto != "" {
		content += fmt.Sprintf("inject_into: %q\n", injectInto)
	}
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *injectFixture) addWorktree(t *testing.T, repo, logicalName, gitBranch string) string {
	t.Helper()
	if repo == "" {
		repo = f.repo
	}
	injectGit(t, repo, "branch", gitBranch, "main")
	path := filepath.Join(f.featurePath, "worktrees", filepath.FromSlash(logicalName))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	injectGit(t, repo, "worktree", "add", path, gitBranch)
	return path
}

func (f *injectFixture) writeInjectFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(f.featurePath, "inject", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runInjectCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := injectCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	var runErr error
	out := captureStdout(t, func() {
		runErr = cmd.Execute()
	})
	return out, runErr
}

func assertRelativeInjectLink(t *testing.T, source, destination, wantContent string) {
	t.Helper()
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatalf("lstat %s: %v", destination, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", destination)
	}
	gotTarget, err := os.Readlink(destination)
	if err != nil {
		t.Fatal(err)
	}
	wantTarget, err := filepath.Rel(filepath.Dir(destination), source)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != wantTarget {
		t.Fatalf("symlink target = %q, want %q", gotTarget, wantTarget)
	}
	gotContent, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotContent) != wantContent {
		t.Fatalf("symlink content = %q, want %q", gotContent, wantContent)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s unexpectedly exists or returned a different error: %v", path, err)
	}
}

func copyInjectFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestInjectFeatureWideDiscoversNestedStackWorktrees(t *testing.T) {
	f := newInjectFixture(t, "customer")
	secondary := createInjectRepo(t, f.root, "secondary")

	plain := f.addWorktree(t, "", "plain", "flat-actual")
	nested := f.addWorktree(t, "", "review/team/pr-123", "feat-review/pr-123")
	multiRepo := f.addWorktree(t, secondary, "docs/api", "docs-actual")
	archived := f.addWorktree(t, "", "archive/old", "archive-actual")
	prunable := f.addWorktree(t, "", "stale/prunable", "stale-actual")
	if err := os.RemoveAll(prunable); err != nil {
		t.Fatal(err)
	}

	stack := internal.Stack{Branches: []internal.StackEntry{
		{Name: "missing/not-created", Branch: "missing-actual", Base: "main"},
		{Name: "review/team/pr-123", Branch: "feat-review/pr-123", Base: "plain"},
		{Name: "archive/old", Branch: "archive-actual", Base: "main", Archived: true},
		{Name: "plain", Branch: "flat-actual", Base: "main"},
		{Name: "docs/api", Branch: "docs-actual", Base: "main", Repo: secondary},
		{Name: "stale/prunable", Branch: "stale-actual", Base: "main"},
	}}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}

	contextSource := f.writeInjectFile(t, "AGENT_CONTEXT.txt", "shared context\n")
	guideSource := f.writeInjectFile(t, "guides/review.md", "review guide\n")
	if err := os.WriteFile(filepath.Join(plain, "AGENT_CONTEXT.txt"), []byte("local override\n"), 0644); err != nil {
		t.Fatal(err)
	}

	nestedHEAD := injectGit(t, nested, "rev-parse", "HEAD")
	repoSubdir := filepath.Join(f.repo, "nested", "cwd")
	if err := os.MkdirAll(repoSubdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoSubdir); err != nil {
		t.Fatal(err)
	}

	out, err := runInjectCommand(t, f.feature)
	if err != nil {
		t.Fatalf("feature-wide inject: %v\n%s", err, out)
	}
	if out != "Injected files into 3 worktree(s)\n" {
		t.Fatalf("stdout = %q", out)
	}
	if got := injectGit(t, nested, "rev-parse", "HEAD"); got != nestedHEAD {
		t.Fatalf("nested HEAD changed: %s -> %s", nestedHEAD, got)
	}

	if data, err := os.ReadFile(filepath.Join(plain, "AGENT_CONTEXT.txt")); err != nil || string(data) != "local override\n" {
		t.Fatalf("existing file was not preserved: data=%q err=%v", data, err)
	}
	assertRelativeInjectLink(t, guideSource, filepath.Join(plain, "guides", "review.md"), "review guide\n")
	assertRelativeInjectLink(t, contextSource, filepath.Join(nested, "AGENT_CONTEXT.txt"), "shared context\n")
	assertRelativeInjectLink(t, guideSource, filepath.Join(nested, "guides", "review.md"), "review guide\n")
	assertRelativeInjectLink(t, contextSource, filepath.Join(multiRepo, "AGENT_CONTEXT.txt"), "shared context\n")

	for _, intermediate := range []string{
		filepath.Join(f.featurePath, "worktrees", "review"),
		filepath.Join(f.featurePath, "worktrees", "review", "team"),
		filepath.Join(f.featurePath, "worktrees", "docs"),
	} {
		assertNotExists(t, filepath.Join(intermediate, "AGENT_CONTEXT.txt"))
		assertNotExists(t, filepath.Join(intermediate, "guides"))
	}
	assertNotExists(t, filepath.Join(archived, "AGENT_CONTEXT.txt"))
	assertNotExists(t, filepath.Join(f.root, "decoy-workspace", f.feature, "worktrees", "plain", "AGENT_CONTEXT.txt"))

	for _, branch := range []string{"archive/old", "missing/not-created"} {
		branchOut, branchErr := runInjectCommand(t, f.feature, branch)
		if branchErr == nil {
			t.Fatalf("explicit inject %q unexpectedly succeeded", branch)
		}
		if branchOut != "" {
			t.Fatalf("explicit inject %q printed success-shaped output %q", branch, branchOut)
		}
	}
}

func TestInjectFeatureWideLegacyFallbackUsesOnlyLinkedWorktreeRoots(t *testing.T) {
	f := newInjectFixture(t, "legacy")
	flat := f.addWorktree(t, "", "flat", "legacy-flat")
	nested := f.addWorktree(t, "", "review/deep/pr-9", "legacy-nested")
	source := f.writeInjectFile(t, "AGENT_CONTEXT.txt", "legacy context\n")

	standalone := filepath.Join(f.featurePath, "worktrees", "arbitrary", "repo")
	if err := os.MkdirAll(filepath.Dir(standalone), 0755); err != nil {
		t.Fatal(err)
	}
	injectGit(t, "", "init", "--initial-branch=main", standalone)
	nestedRepo := filepath.Join(nested, "vendor", "repo")
	if err := os.MkdirAll(filepath.Dir(nestedRepo), 0755); err != nil {
		t.Fatal(err)
	}
	injectGit(t, "", "init", "--initial-branch=main", nestedRepo)

	out, err := runInjectCommand(t, f.feature)
	if err != nil {
		t.Fatalf("legacy feature-wide inject: %v\n%s", err, out)
	}
	if out != "Injected files into 2 worktree(s)\n" {
		t.Fatalf("stdout = %q", out)
	}
	assertRelativeInjectLink(t, source, filepath.Join(flat, "AGENT_CONTEXT.txt"), "legacy context\n")
	assertRelativeInjectLink(t, source, filepath.Join(nested, "AGENT_CONTEXT.txt"), "legacy context\n")
	assertNotExists(t, filepath.Join(f.featurePath, "worktrees", "review", "AGENT_CONTEXT.txt"))
	assertNotExists(t, filepath.Join(f.featurePath, "worktrees", "review", "deep", "AGENT_CONTEXT.txt"))
	assertNotExists(t, filepath.Join(standalone, "AGENT_CONTEXT.txt"))
	assertNotExists(t, filepath.Join(nestedRepo, "AGENT_CONTEXT.txt"))
}

func TestInjectRejectsCopiedStaleAndMismatchedGitMarkers(t *testing.T) {
	markerKinds := []string{"copied", "stale", "mismatched"}
	for _, markerKind := range markerKinds {
		for _, metadata := range []bool{true, false} {
			name := markerKind + "-fallback"
			if metadata {
				name = markerKind + "-metadata"
			}
			t.Run(name, func(t *testing.T) {
				f := newInjectFixture(t, "customer")
				source := f.writeInjectFile(t, "AGENT_CONTEXT.txt", "shared context\n")

				logicalName := "review/pr-123"
				target := filepath.Join(f.featurePath, "worktrees", filepath.FromSlash(logicalName))
				var controls []string
				switch markerKind {
				case "copied":
					actual := f.addWorktree(t, "", logicalName, "copied-actual")
					target = filepath.Join(f.featurePath, "worktrees", "review")
					logicalName = "review"
					copyInjectFile(t, filepath.Join(actual, ".git"), filepath.Join(target, ".git"))
					controls = append(controls, actual)
				case "stale":
					actual := f.addWorktree(t, "", logicalName, "stale-actual")
					marker, err := os.ReadFile(filepath.Join(actual, ".git"))
					if err != nil {
						t.Fatal(err)
					}
					injectGit(t, f.repo, "worktree", "remove", "--force", actual)
					if err := os.MkdirAll(actual, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(actual, ".git"), marker, 0644); err != nil {
						t.Fatal(err)
					}
				case "mismatched":
					actual := f.addWorktree(t, "", logicalName, "mismatched-actual")
					donor := f.addWorktree(t, "", "plain", "mismatched-donor")
					copyInjectFile(t, filepath.Join(donor, ".git"), filepath.Join(actual, ".git"))
					controls = append(controls, donor)
				default:
					t.Fatalf("unknown marker kind %q", markerKind)
				}

				if metadata {
					if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
						Name: logicalName, Branch: "decoupled-actual", Base: "main",
					}}}); err != nil {
						t.Fatal(err)
					}
				}

				for _, args := range [][]string{
					{f.feature},
					{f.feature, logicalName},
				} {
					out, err := runInjectCommand(t, args...)
					if err == nil {
						t.Fatalf("inject %v unexpectedly accepted %s marker", args, markerKind)
					}
					if out != "" {
						t.Fatalf("inject %v printed success-shaped output %q", args, out)
					}
					if markerKind != "stale" && !strings.Contains(err.Error(), "backpointer") {
						t.Fatalf("inject %v error = %v, want backpointer refusal", args, err)
					}
					assertNotExists(t, filepath.Join(target, filepath.Base(source)))
					for _, control := range controls {
						assertNotExists(t, filepath.Join(control, filepath.Base(source)))
					}
				}
			})
		}
	}
}

func TestInjectWorktreeIdentityPreservesWhitespacePaths(t *testing.T) {
	f := newInjectFixture(t, "whitespace")
	logicalNames := []string{
		"review/entry ",
		"review/\tentry",
		"review/line\nentry",
		"review/entry\r",
	}
	branches := []string{"actual-trailing-space", "actual-tab", "actual-newline", "actual-carriage-return"}
	worktrees := make([]string, 0, len(logicalNames))
	stack := internal.Stack{}
	for i, logicalName := range logicalNames {
		worktrees = append(worktrees, f.addWorktree(t, "", logicalName, branches[i]))
		stack.Branches = append(stack.Branches, internal.StackEntry{
			Name: logicalName, Branch: branches[i], Base: "main",
		})
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}

	firstSource := f.writeInjectFile(t, "AGENT_CONTEXT.txt", "whitespace context\n")
	for i, logicalName := range logicalNames {
		out, err := runInjectCommand(t, f.feature, logicalName)
		if err != nil {
			t.Fatalf("explicit inject %q: %v\n%s", logicalName, err, out)
		}
		assertRelativeInjectLink(t, firstSource, filepath.Join(worktrees[i], "AGENT_CONTEXT.txt"), "whitespace context\n")
	}

	secondSource := f.writeInjectFile(t, "SECOND_CONTEXT.txt", "second context\n")
	out, err := runInjectCommand(t, f.feature)
	if err != nil {
		t.Fatalf("feature-wide whitespace inject: %v\n%s", err, out)
	}
	if out != "Injected files into 4 worktree(s)\n" {
		t.Fatalf("stdout = %q", out)
	}
	for _, worktree := range worktrees {
		assertRelativeInjectLink(t, secondSource, filepath.Join(worktree, "SECOND_CONTEXT.txt"), "second context\n")
	}
	assertNotExists(t, filepath.Join(f.featurePath, "worktrees", "review", "AGENT_CONTEXT.txt"))
	assertNotExists(t, filepath.Join(f.featurePath, "worktrees", "review", "SECOND_CONTEXT.txt"))
}

func TestInjectIntoConfigAndExplicitFlag(t *testing.T) {
	f := newInjectFixture(t, "targets")
	worktree := f.addWorktree(t, "", "review/pr-44", "target-actual")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "review/pr-44", Branch: "target-actual", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	source := f.writeInjectFile(t, "AGENT_CONTEXT.txt", "target context\n")

	writeInjectTWSConfig(t, f.home, f.repo, f.workspace, ".configured")
	out, err := runInjectCommand(t, f.feature)
	if err != nil {
		t.Fatalf("configured inject: %v\n%s", err, out)
	}
	if out != "Injected files into 1 worktree(s)\n" {
		t.Fatalf("stdout = %q", out)
	}
	assertRelativeInjectLink(t, source, filepath.Join(worktree, ".configured", "AGENT_CONTEXT.txt"), "target context\n")
	assertNotExists(t, filepath.Join(f.featurePath, "worktrees", "review", ".configured"))

	explicitPath := filepath.Join(worktree, ".explicit", "AGENT_CONTEXT.txt")
	if err := os.MkdirAll(filepath.Dir(explicitPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(explicitPath, []byte("keep me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err = runInjectCommand(t, f.feature, "review/pr-44", "--into", ".explicit")
	if err != nil {
		t.Fatalf("explicit inject: %v\n%s", err, out)
	}
	if out != "Injected files into: targets/review/pr-44/.explicit\n" {
		t.Fatalf("stdout = %q", out)
	}
	if data, readErr := os.ReadFile(explicitPath); readErr != nil || string(data) != "keep me\n" {
		t.Fatalf("explicit existing file was not preserved: data=%q err=%v", data, readErr)
	}
	assertNotExists(t, filepath.Join(f.featurePath, "worktrees", "review", ".explicit"))
}

func TestInjectDiscoveryRejectsCorruptAndUntrustedMetadata(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, featurePath string)
		wantErr string
	}{
		{
			name: "malformed",
			setup: func(t *testing.T, featurePath string) {
				t.Helper()
				if err := os.WriteFile(internal.StackPath(featurePath), []byte("branches: ["), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "reading",
		},
		{
			name: "unreadable-shape",
			setup: func(t *testing.T, featurePath string) {
				t.Helper()
				if err := os.MkdirAll(internal.StackPath(featurePath), 0755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "reading",
		},
		{
			name: "unsafe-logical-path",
			setup: func(t *testing.T, featurePath string) {
				t.Helper()
				if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
					Name: "../escape", Base: "main",
				}}}); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "safe relative path",
		},
		{
			name: "existing-non-git-directory",
			setup: func(t *testing.T, featurePath string) {
				t.Helper()
				if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
					Name: "plain", Base: "main",
				}}}); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(featurePath, "worktrees", "plain"), 0755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "not a linked Git worktree",
		},
		{
			name: "symlink-component",
			setup: func(t *testing.T, featurePath string) {
				t.Helper()
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.MkdirAll(outside, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(featurePath, "worktrees", "link")); err != nil {
					t.Fatal(err)
				}
				if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
					Name: "link", Base: "main",
				}}}); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "symlink component",
		},
		{
			name: "deterministic-logical-order",
			setup: func(t *testing.T, featurePath string) {
				t.Helper()
				if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{
					{Name: "zeta", Base: "main"},
					{Name: "alpha", Base: "main"},
				}}); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"zeta", "alpha"} {
					if err := os.MkdirAll(filepath.Join(featurePath, "worktrees", name), 0755); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantErr: `worktree "alpha"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			featurePath := t.TempDir()
			if err := os.MkdirAll(filepath.Join(featurePath, "inject"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(featurePath, "inject", "AGENT_CONTEXT.txt"), []byte("context\n"), 0644); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, featurePath)

			count, err := internal.InjectFilesForFeature(featurePath, "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("count=%d err=%v, want error containing %q", count, err, tc.wantErr)
			}
			if count != 0 {
				t.Fatalf("count = %d, want 0", count)
			}
		})
	}
}

func TestInjectRejectsSymlinkedStackMetadata(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		t.Run(map[bool]string{false: "readable-link", true: "dangling-link"}[dangling], func(t *testing.T) {
			f := newInjectFixture(t, "customer")
			nested := f.addWorktree(t, "", "review/pr-123", "actual-branch")
			flat := f.addWorktree(t, "", "plain", "flat-branch")
			source := f.writeInjectFile(t, "AGENT_CONTEXT.txt", "shared context\n")
			target := filepath.Join(f.featurePath, "referenced-stack.yaml")
			if !dangling {
				if err := os.WriteFile(target, []byte("branches: []\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, internal.StackPath(f.featurePath)); err != nil {
				t.Fatal(err)
			}
			if count, err := internal.InjectFilesForFeature(f.featurePath, ""); err == nil || count != 0 {
				t.Fatalf("symlinked metadata should refuse before discovery: count=%d err=%v", count, err)
			}
			for _, args := range [][]string{{f.feature}, {f.feature, "review/pr-123"}} {
				out, err := runInjectCommand(t, args...)
				if err == nil || !strings.Contains(err.Error(), "stack.yaml") {
					t.Fatalf("inject %v should identify unusable stack metadata: %v", args, err)
				}
				if out != "" {
					t.Fatalf("metadata refusal printed success: %q", out)
				}
				assertNotExists(t, filepath.Join(nested, filepath.Base(source)))
				assertNotExists(t, filepath.Join(flat, filepath.Base(source)))
			}
			info, err := os.Lstat(internal.StackPath(f.featurePath))
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("metadata refusal changed the symlink: %v", err)
			}
		})
	}
}

func TestInjectCheckoutModeRefusesWideAndSpecific(t *testing.T) {
	repo := setupGitRepoCheckout(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TWS_ROOT", "")
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	featurePath := filepath.Join(repo, ".tws", "features", "customer")
	if err := os.MkdirAll(filepath.Join(featurePath, "inject"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featurePath, "inject", "AGENT_CONTEXT.txt"), []byte("context\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "review/pr-123", Branch: "feat-review/pr-123", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	before := gitInDir(t, repo, "rev-parse", "HEAD")

	for _, args := range [][]string{
		{"customer"},
		{"customer", "review/pr-123"},
	} {
		out, runErr := runInjectCommand(t, args...)
		if !errors.Is(runErr, internal.ErrWorktreeUnsupported) {
			t.Fatalf("inject %v err = %v", args, runErr)
		}
		if out != "" {
			t.Fatalf("inject %v printed success-shaped output %q", args, out)
		}
	}
	if after := gitInDir(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatalf("checkout HEAD changed: %s -> %s", before, after)
	}
	assertNotExists(t, filepath.Join(repo, "AGENT_CONTEXT.txt"))
}

func TestTemplateAndImportUseNestedWorktreeDiscovery(t *testing.T) {
	f := newInjectFixture(t, "templated")
	flat := f.addWorktree(t, "", "flat", "template-flat")
	nested := f.addWorktree(t, "", "review/pr-77", "template-nested")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{
		{Name: "review/pr-77", Branch: "template-nested", Base: "flat"},
		{Name: "flat", Branch: "template-flat", Base: "main"},
	}}); err != nil {
		t.Fatal(err)
	}
	templateDir := filepath.Join(f.root, "template")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "TEMPLATE_CONTEXT.md"), []byte("template context\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		syncFeatureTemplate(f.feature, []string{templateDir})
	})
	if !strings.Contains(out, "Synced inject to 2 worktree(s)") {
		t.Fatalf("template stdout = %q", out)
	}
	templateSource := filepath.Join(f.featurePath, "inject", "TEMPLATE_CONTEXT.md")
	assertRelativeInjectLink(t, templateSource, filepath.Join(flat, "TEMPLATE_CONTEXT.md"), "template context\n")
	assertRelativeInjectLink(t, templateSource, filepath.Join(nested, "TEMPLATE_CONTEXT.md"), "template context\n")

	t.Setenv("TWS_ROOT", f.workspace)
	importFeature := "imported"
	importFeaturePath := filepath.Join(f.workspace, importFeature)
	if err := os.MkdirAll(filepath.Join(importFeaturePath, "inject"), 0755); err != nil {
		t.Fatal(err)
	}
	importWorktree := filepath.Join(importFeaturePath, "worktrees", "review", "pr-88")
	injectGit(t, f.repo, "branch", "import-nested", "main")
	if err := os.MkdirAll(filepath.Dir(importWorktree), 0755); err != nil {
		t.Fatal(err)
	}
	injectGit(t, f.repo, "worktree", "add", importWorktree, "import-nested")
	if err := internal.SaveStack(importFeaturePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "review/pr-88", Branch: "import-nested", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	importSource := filepath.Join(f.root, "import-source")
	if err := os.MkdirAll(importSource, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importSource, "IMPORTED_CONTEXT.md"), []byte("imported context\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out = captureStdout(t, func() {
		if err := recreateExternal(internal.WorkspaceExport{Feature: importFeature}, importSource); err != nil {
			t.Errorf("recreateExternal: %v", err)
		}
	})
	if !strings.Contains(out, "Injected files into 1 worktree(s)") {
		t.Fatalf("import stdout = %q", out)
	}
	importedSource := filepath.Join(importFeaturePath, "inject", "IMPORTED_CONTEXT.md")
	assertRelativeInjectLink(t, importedSource, filepath.Join(importWorktree, "IMPORTED_CONTEXT.md"), "imported context\n")
	assertNotExists(t, filepath.Join(importFeaturePath, "worktrees", "review", "IMPORTED_CONTEXT.md"))
}

func TestTemplateAndImportSurfaceDiscoveryErrors(t *testing.T) {
	f := newInjectFixture(t, "bad-template")
	if err := os.MkdirAll(filepath.Join(f.featurePath, "worktrees", "plain"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{
		Name: "plain", Base: "main",
	}}}); err != nil {
		t.Fatal(err)
	}
	templateDir := filepath.Join(f.root, "template")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "CONTEXT.md"), []byte("context\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		syncFeatureTemplate(f.feature, []string{templateDir})
	})
	if !strings.Contains(out, "Warning: worktree inject failed:") {
		t.Fatalf("template error was not surfaced: %q", out)
	}

	t.Setenv("TWS_ROOT", f.workspace)
	badImportPath := filepath.Join(f.workspace, "bad-import")
	if err := os.MkdirAll(filepath.Join(badImportPath, "inject"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internal.StackPath(badImportPath), []byte("branches: ["), 0644); err != nil {
		t.Fatal(err)
	}
	err := recreateExternal(internal.WorkspaceExport{Feature: "bad-import"}, "")
	if err == nil || !strings.Contains(err.Error(), "injecting restored files") {
		t.Fatalf("import error = %v", err)
	}
}
