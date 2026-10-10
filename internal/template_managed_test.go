package internal

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func managedTestEnvironment(t *testing.T) (root, home string) {
	t.Helper()
	root = t.TempDir()
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[user]\n\tname = Managed Template Test\n\temail = managed@example.test\n\tuseConfigOnly = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	return root, home
}

func managedTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{}, args...)
	if dir != "" {
		commandArgs = append([]string{"-C", dir}, commandArgs...)
	}
	cmd := exec.Command("git", commandArgs...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Managed Template Test",
		"GIT_AUTHOR_EMAIL=managed@example.test",
		"GIT_COMMITTER_NAME=Managed Template Test",
		"GIT_COMMITTER_EMAIL=managed@example.test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", commandArgs, err, out)
	}
	return strings.TrimSpace(string(out))
}

func managedTestCheckoutWorkspace(t *testing.T) (Workspace, string) {
	t.Helper()
	root, _ := managedTestEnvironment(t)
	repo := filepath.Join(root, "repo")
	managedTestGit(t, "", "init", "--initial-branch=main", repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, repo, "add", "README.md")
	managedTestGit(t, repo, "commit", "-m", "initial")
	if err := os.MkdirAll(filepath.Join(repo, ".tws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tws", "config.yaml"), []byte("workspace_mode: checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := ResolveCurrentWorkspaceE(repo, LoadConfigForRepo(repo))
	if err != nil {
		t.Fatal(err)
	}
	return ws, root
}

func managedTestPackage(t *testing.T, root string, files map[string]string, manifest string) string {
	t.Helper()
	pkg := filepath.Join(root, "package")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(pkg, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(pkg, "template.yaml"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return pkg
}

func TestNamedTemplateRegistryScopesCopyAndValidation(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	globalSource := managedTestPackage(t, filepath.Join(root, "global-source"), map[string]string{
		"inject/context.md": "global\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "review", globalSource); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalSource, "inject", "context.md"), []byte("changed source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveTemplateReference(repo, "global:review")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(resolved.Path, "inject", "context.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "global\n" {
		t.Fatalf("registry copy changed with source: %q", data)
	}

	repoSource := managedTestPackage(t, filepath.Join(root, "repo-source"), map[string]string{
		"feature/context.md": "repo\n",
	}, "")
	if err := AddNamedTemplate(repo, TemplateScopeRepo, "review", repoSource); err != nil {
		t.Fatal(err)
	}
	effective, err := ResolveTemplateReference(repo, "review")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Reference.Scope != TemplateScopeRepo {
		t.Fatalf("effective scope = %q, want repo", effective.Reference.Scope)
	}
	global, err := ResolveTemplateReference(repo, "global:review")
	if err != nil {
		t.Fatal(err)
	}
	if global.Reference.Scope != TemplateScopeGlobal {
		t.Fatalf("explicit global scope rebound to %q", global.Reference.Scope)
	}
	if err := AddNamedTemplate(repo, TemplateScopeRepo, "Review", repoSource); err == nil ||
		!strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("case-alias duplicate accepted: %v", err)
	}
	if err := AddNamedTemplate("", TemplateScopeGlobal, "../escape", globalSource); err == nil {
		t.Fatal("unsafe registry name was accepted")
	}

	symlinkSource := filepath.Join(root, "symlink-source")
	if err := os.MkdirAll(filepath.Join(symlinkSource, "inject"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(symlinkSource, "inject", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := AddNamedTemplate("", TemplateScopeGlobal, "unsafe", symlinkSource); err == nil ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked package accepted: %v", err)
	}
	skillBody := managedTestPackage(t, filepath.Join(root, "skill-body"), map[string]string{
		"inject/.claude/skills/review/SKILL.md": "canonical body\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "skill-body", skillBody); err == nil ||
		!strings.Contains(err.Error(), "skill") {
		t.Fatalf("managed package copied a skill implementation: %v", err)
	}

	entries, err := ListNamedTemplates(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[0].Effective || entries[0].Scope != TemplateScopeRepo {
		t.Fatalf("registry listing = %+v", entries)
	}
	if err := RemoveNamedTemplate(repo, TemplateScopeRepo, "review"); err != nil {
		t.Fatal(err)
	}
	fallback, err := ResolveTemplateReference(repo, "review")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Reference.Scope != TemplateScopeGlobal {
		t.Fatalf("unpersisted lookup did not expose global fallback: %+v", fallback.Reference)
	}
}

func TestNamedTemplatePinnedPackageAndRegistryRoots(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	source := managedTestPackage(t, filepath.Join(root, "pinned-source"), map[string]string{
		"inject/context.md": "original\n",
	}, "")
	replacement := managedTestPackage(t, filepath.Join(root, "replacement-source"), map[string]string{
		"inject/context.md": "replacement\n",
	}, "")
	originalPath := source
	movedPath := source + "-moved"
	templatePackageAfterOpenHook = func(opened string) error {
		if filepath.Clean(opened) != filepath.Clean(originalPath) {
			return nil
		}
		if err := os.Rename(originalPath, movedPath); err != nil {
			return err
		}
		return os.Rename(replacement, originalPath)
	}
	pkg, err := LoadTemplatePackage(source)
	templatePackageAfterOpenHook = nil
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Inject) != 1 || string(pkg.Inject[0].Data) != "original\n" {
		t.Fatalf("package enumeration/read escaped pinned source root: %+v", pkg.Inject)
	}

	if err := AddNamedTemplate("", TemplateScopeGlobal, "pinned", movedPath); err != nil {
		t.Fatal(err)
	}
	registryRoot, err := GlobalTemplateRegistryRoot()
	if err != nil {
		t.Fatal(err)
	}
	replacementRegistry := registryRoot + "-replacement"
	if err := os.MkdirAll(replacementRegistry, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(replacementRegistry, "sentinel")
	if err := os.WriteFile(sentinel, []byte("replacement registry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalRegistry := registryRoot + "-original"
	templateRegistryAfterLockHook = func(locked string) error {
		if filepath.Clean(locked) != filepath.Clean(registryRoot) {
			return nil
		}
		if err := os.Rename(registryRoot, originalRegistry); err != nil {
			return err
		}
		return os.Rename(replacementRegistry, registryRoot)
	}
	err = RemoveNamedTemplate("", TemplateScopeGlobal, "pinned")
	templateRegistryAfterLockHook = nil
	if err == nil || !strings.Contains(err.Error(), "root identity changed") {
		t.Fatalf("registry root replacement was not refused: %v", err)
	}
	replacementSentinel := filepath.Join(registryRoot, "sentinel")
	if data, readErr := os.ReadFile(replacementSentinel); readErr != nil || string(data) != "replacement registry\n" {
		t.Fatalf("replacement registry was touched: %q %v", data, readErr)
	}
}

func TestManagedTemplateAdvisoryLocksDoNotUnlinkOrSteal(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	featurePath := filepath.Join(root, "feature")
	first, err := AcquireManagedTemplateLock(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireManagedTemplateLock(featurePath); err == nil {
		t.Fatal("second managed writer acquired a live advisory lock")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(ManagedTemplateLockPath(featurePath)); err != nil {
		t.Fatalf("managed advisory lock name was unlinked on release: %v", err)
	}
	second, err := AcquireManagedTemplateLock(featurePath)
	if err != nil {
		t.Fatalf("released advisory lock could not be reacquired: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}

	registryRoot := filepath.Join(root, "registry")
	if err := os.MkdirAll(registryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	registryLock, err := acquireTemplateRegistryLock(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireTemplateRegistryLock(registryRoot); err == nil {
		t.Fatal("second registry writer acquired a live advisory lock")
	}
	if err := registryLock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(registryRoot, ".registry.lock")); err != nil {
		t.Fatalf("registry advisory lock name was unlinked on release: %v", err)
	}
	reacquired, err := acquireTemplateRegistryLock(registryRoot)
	if err != nil {
		t.Fatalf("released registry lock could not be reacquired: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedTemplatePayloadRootsRefuseRegistryPublication(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	registryRoot, err := GlobalTemplateRegistryRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"feature", "inject"} {
		t.Run(payload, func(t *testing.T) {
			source := managedTestPackage(t, filepath.Join(root, "invalid-"+payload), map[string]string{
				payload: "not a directory\n",
			}, "")
			name := "invalid-" + payload
			if err := AddNamedTemplate("", TemplateScopeGlobal, name, source); err == nil ||
				!strings.Contains(err.Error(), "must be a regular directory") {
				t.Fatalf("invalid %s payload root was accepted: %v", payload, err)
			}
			if _, err := os.Lstat(filepath.Join(registryRoot, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid package published registry entry %q: %v", name, err)
			}
		})
	}
}

func TestNamedTemplateEmptyNativeOverrideSuppressesBuiltin(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "empty-native"), map[string]string{
		"feature/AGENTS.md": "",
	}, `version: 1
guidance:
  orchestrator: Manifest guidance only.
`)
	if err := AddNamedTemplate("", TemplateScopeGlobal, "empty-native", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "empty-native", FeaturePath: ws.FeaturePath("empty-native"),
		Roles:        []ManagedTemplateRole{ManagedTemplateRoleOrchestrator},
		Orchestrator: ManagedTemplateRoleRequest{Reference: "global:empty-native", ReferenceSet: true},
		Agent:        "copilot", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(request.FeaturePath, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "Manifest guidance only.") {
		t.Fatalf("manifest guidance missing from empty native override: %q", body)
	}
	if strings.Contains(body, "TWS feature orchestrator guidance") ||
		strings.Contains(body, "Coordinate feature") {
		t.Fatalf("empty native override unexpectedly received built-in prose: %q", body)
	}
}

func TestNamedTemplateDesiredTreeRejectsAliasesAndAncestorConflicts(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{
			name: "native ancestor conflict",
			files: map[string]string{
				"feature/AGENTS.md/child": "two\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseRoot := filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-"))
			source := managedTestPackage(t, caseRoot, nil, "")
			for name, content := range tc.files {
				path := filepath.Join(source, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
					t.Fatal(err)
				}

				if err := validateManagedDesiredTree([]managedDesiredFile{
					{Path: "Guide.md"}, {Path: "B"}, {Path: "guide.md"},
				}); err == nil || !strings.Contains(err.Error(), "alias") {
					t.Fatalf("nonadjacent canonical aliases were accepted: %v", err)
				}
			}
			templateName := strings.ReplaceAll(tc.name, " ", "-")
			if err := AddNamedTemplate("", TemplateScopeGlobal, templateName, source); err != nil {
				t.Fatal(err)
			}
			featurePath := ws.FeaturePath(templateName)
			_, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
				Workspace: ws, Feature: templateName, FeaturePath: featurePath,
				Roles:        []ManagedTemplateRole{ManagedTemplateRoleOrchestrator},
				Orchestrator: ManagedTemplateRoleRequest{Reference: "global:" + templateName, ReferenceSet: true},
				Agent:        "copilot", AgentSet: true,
			})
			if err == nil || (!strings.Contains(err.Error(), "alias") &&
				!strings.Contains(err.Error(), "descendant") &&
				!strings.Contains(err.Error(), "conflicts with native file")) {
				t.Fatalf("invalid desired tree was accepted: %v", err)
			}
			if _, statErr := os.Lstat(featurePath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid plan wrote feature assets or state: %v", statErr)
			}
		})
	}
}

func TestNamedTemplateManifestStrictRoleFiltering(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	valid := managedTestPackage(t, filepath.Join(root, "valid"), map[string]string{
		"inject/readme.md": "payload\n",
	}, `version: 1
guidance:
  worktree: Stay in the selected worktree.
skills:
  - name: code-review
    roles: [worktree]
    agents: [claude]
`)
	pkg, err := LoadTemplatePackage(valid)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.Version != 1 || len(pkg.Manifest.Skills) != 1 {
		t.Fatalf("manifest = %+v", pkg.Manifest)
	}

	for name, body := range map[string]string{
		"future":      `version: 2`,
		"unknown-key": "version: 1\nextra: true\n",
		"duplicate-skill": `version: 1
skills:
  - name: review
    roles: [worktree]
  - name: review
    roles: [orchestrator]
`,
		"missing-role": `version: 1
skills:
  - name: review
`,
		"bad-agent": `version: 1
skills:
  - name: review
    roles: [worktree]
    agents: [wrapper-copilot]
`,
	} {
		t.Run(name, func(t *testing.T) {
			source := managedTestPackage(t, filepath.Join(root, name), nil, body)
			if _, err := LoadTemplatePackage(source); err == nil {
				t.Fatalf("invalid manifest %s was accepted", name)
			}
		})
	}
}

func TestManagedTemplateCheckoutNativeOwnershipAndScope(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "native"), map[string]string{
		"inject/.github/instructions/tws-worktree.instructions.md": "Custom worktree base.\n",
		"inject/shared.md":  "shared v1\n",
		"feature/AGENTS.md": "Custom orchestrator base.\n",
		"feature/notes.md":  "notes v1\n",
	}, `version: 1
guidance:
  worktree: Worktree-only manifest guidance.
  orchestrator: Orchestrator-only manifest guidance.
skills:
  - name: review-work
    roles: [worktree]
    agents: [copilot]
  - name: coordinate
    roles: [orchestrator]
    agents: [copilot]
  - name: claude-only
    roles: [worktree]
    agents: [claude]
`)
	if err := AddNamedTemplate("", TemplateScopeGlobal, "native", source); err != nil {
		t.Fatal(err)
	}
	feature := "feature"
	featurePath := ws.FeaturePath(feature)
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:        []ManagedTemplateRole{ManagedTemplateRoleWorktree, ManagedTemplateRoleOrchestrator},
		Worktree:     ManagedTemplateRoleRequest{Reference: "global:native", ReferenceSet: true},
		Orchestrator: ManagedTemplateRoleRequest{Reference: "global:native", ReferenceSet: true},
		Agent:        "copilot", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	worktreeNative := filepath.Join(featurePath, "inject", ".github", "instructions", "tws-worktree.instructions.md")
	worktreeBody, err := os.ReadFile(worktreeNative)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"applyTo: \"**\"", "Custom worktree base.", "Worktree-only manifest guidance.", "`review-work`",
	} {
		if !strings.Contains(string(worktreeBody), want) {
			t.Fatalf("worktree guidance missing %q:\n%s", want, worktreeBody)
		}
	}
	for _, unwanted := range []string{"coordinate", "claude-only"} {
		if strings.Contains(string(worktreeBody), unwanted) {
			t.Fatalf("worktree guidance copied filtered reference %q", unwanted)
		}
	}
	orchestratorBody, err := os.ReadFile(filepath.Join(featurePath, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Custom orchestrator base.", "Orchestrator-only manifest guidance.", "`coordinate`"} {
		if !strings.Contains(string(orchestratorBody), want) {
			t.Fatalf("orchestrator guidance missing %q:\n%s", want, orchestratorBody)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws.RepoRoot, ".github", "instructions", "tws-worktree.instructions.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout mode permanently injected into physical checkout: %v", err)
	}

	state, err := LoadManagedTemplateState(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Roles["worktree"].Source.Scope != TemplateScopeGlobal ||
		state.Roles["orchestrator"].Agent != "copilot" {
		t.Fatalf("state did not persist resolved source and agent: %+v", state)
	}

	if err := os.WriteFile(filepath.Join(featurePath, "inject", "shared.md"), []byte("operator edit\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(request); err == nil || !strings.Contains(err.Error(), "content digest changed") {
		t.Fatalf("modified owned file was accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(featurePath, "inject", "shared.md"), []byte("shared v1\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	orchestratorBefore, err := os.ReadFile(filepath.Join(featurePath, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	scoped := request
	scoped.Roles = []ManagedTemplateRole{ManagedTemplateRoleWorktree}
	scoped.Orchestrator = ManagedTemplateRoleRequest{}
	scopedPlan, err := PlanManagedTemplate(scoped)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(scopedPlan); err != nil {
		t.Fatal(err)
	}
	orchestratorAfter, err := os.ReadFile(filepath.Join(featurePath, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(orchestratorAfter) != string(orchestratorBefore) {
		t.Fatal("worktree-scoped update changed orchestrator assets")
	}

	unmanagedFeature := "unmanaged"
	unmanagedPath := ws.FeaturePath(unmanagedFeature)
	sameContent := filepath.Join(unmanagedPath, "inject", ".github", "instructions", "tws-worktree.instructions.md")
	if err := os.MkdirAll(filepath.Dir(sameContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sameContent, worktreeBody, 0o640); err != nil {
		t.Fatal(err)
	}
	unmanagedRequest := request
	unmanagedRequest.Feature = unmanagedFeature
	unmanagedRequest.FeaturePath = unmanagedPath
	unmanagedRequest.Roles = []ManagedTemplateRole{ManagedTemplateRoleWorktree}
	if _, err := PlanManagedTemplate(unmanagedRequest); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("same-content unmanaged file was adopted: %v", err)
	}
}

func TestManagedTemplateExternalLinksAndTrackedConflict(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	repo := filepath.Join(root, "repo")
	managedTestGit(t, "", "init", "--initial-branch=main", repo)
	for name, content := range map[string]string{
		"README.md":   "repo\n",
		"tracked.txt": "tracked\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	managedTestGit(t, repo, "add", "README.md", "tracked.txt")
	managedTestGit(t, repo, "commit", "-m", "initial")

	workspace := filepath.Join(root, "workspace")
	if err := EnsureExternalWorkspaceMarker(workspace); err != nil {
		t.Fatal(err)
	}
	feature := "links"
	featurePath := filepath.Join(workspace, feature)
	worktree := filepath.Join(featurePath, "worktrees", "root")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, repo, "branch", "managed-root", "main")
	managedTestGit(t, repo, "worktree", "add", worktree, "managed-root")
	if err := SaveStack(featurePath, Stack{Branches: []StackEntry{{Name: "root", Branch: "managed-root"}}}); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{
		RepoRoot: repo, Mode: ModeExternal, MetadataRoot: workspace,
		StableID: stableID(repo), Caps: capsFor(ModeExternal),
	}
	source := managedTestPackage(t, filepath.Join(root, "links-package"), map[string]string{
		"inject/context.txt": "managed\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "links", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:links", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"context.txt", "CLAUDE.local.md"} {
		path := filepath.Join(worktree, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a managed symlink", path)
		}
	}
	state, err := LoadManagedTemplateState(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Roles["worktree"].Links) != 2 {
		t.Fatalf("owned links = %+v", state.Roles["worktree"].Links)
	}

	contextLink := filepath.Join(worktree, "context.txt")
	if err := os.Remove(contextLink); err != nil {
		t.Fatal(err)
	}
	linkPlan, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:               []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		LinkMaterializeOnly: true, OnlyWorktree: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(linkPlan); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(contextLink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("missing managed link was not recreated: info=%v err=%v", info, err)
	}

	if err := RemoveNamedTemplate("", TemplateScopeGlobal, "links"); err != nil {
		t.Fatal(err)
	}
	emptySource := managedTestPackage(t, filepath.Join(root, "links-empty"), nil, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "links", emptySource); err != nil {
		t.Fatal(err)
	}
	retirement, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	var retiredFile, retiredLink bool
	for _, removal := range retirement.Removals {
		if strings.HasSuffix(removal.Path, "context.txt") && removal.Kind == "file" {
			retiredFile = true
		}
		if strings.HasSuffix(removal.Path, "context.txt") && removal.Kind == "link" {
			retiredLink = true
		}
	}
	if !retiredFile || !retiredLink {
		t.Fatalf("source retirement did not plan both source and external link: %+v", retirement.Removals)
	}
	if err := ApplyManagedTemplatePlan(retirement); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{contextLink, filepath.Join(featurePath, "inject", "context.txt")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired managed path remains %s: %v", path, err)
		}
	}
	if err := RemoveNamedTemplate("", TemplateScopeGlobal, "links"); err != nil {
		t.Fatal(err)
	}
	if err := AddNamedTemplate("", TemplateScopeGlobal, "links", source); err != nil {
		t.Fatal(err)
	}
	restore, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(restore); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(contextLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contextLink, []byte("managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:               []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		LinkMaterializeOnly: true, OnlyWorktree: "root",
	}); err == nil || !strings.Contains(err.Error(), "unmanaged or modified") {
		t.Fatalf("same-content external collision was adopted: %v", err)
	}

	trackedFeature := "tracked"
	trackedFeaturePath := filepath.Join(workspace, trackedFeature)
	trackedWorktree := filepath.Join(trackedFeaturePath, "worktrees", "root")
	if err := os.MkdirAll(filepath.Dir(trackedWorktree), 0o755); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, repo, "branch", "tracked-root", "main")
	managedTestGit(t, repo, "worktree", "add", trackedWorktree, "tracked-root")
	if err := SaveStack(trackedFeaturePath, Stack{Branches: []StackEntry{{Name: "root", Branch: "tracked-root"}}}); err != nil {
		t.Fatal(err)
	}
	trackedSource := managedTestPackage(t, filepath.Join(root, "tracked-package"), map[string]string{
		"inject/tracked.txt": "template\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "tracked", trackedSource); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: trackedFeature, FeaturePath: trackedFeaturePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:tracked", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}); err == nil || !strings.Contains(err.Error(), "tracked by Git") {
		t.Fatalf("tracked worktree destination was accepted: %v", err)
	}
}

func TestManagedTemplateSameTargetReplacementAndLiteralTrackedPathRefuse(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	repo := filepath.Join(root, "repo")
	managedTestGit(t, "", "init", "--initial-branch=main", repo)
	magic := ":(literal)managed.txt"
	for name, content := range map[string]string{
		"README.md": "repo\n",
		magic:       "tracked\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	managedTestGit(t, repo, "--literal-pathspecs", "add", "README.md", magic)
	managedTestGit(t, repo, "commit", "-m", "initial")
	if err := os.Remove(filepath.Join(repo, magic)); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(root, "workspace")
	if err := EnsureExternalWorkspaceMarker(workspace); err != nil {
		t.Fatal(err)
	}
	feature := "exact-links"
	featurePath := filepath.Join(workspace, feature)
	worktree := filepath.Join(featurePath, "worktrees", "root")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, repo, "branch", "exact-root", "main")
	managedTestGit(t, repo, "worktree", "add", worktree, "exact-root")
	if err := os.Remove(filepath.Join(worktree, magic)); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(featurePath, Stack{Branches: []StackEntry{{Name: "root", Branch: "exact-root"}}}); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: repo, Mode: ModeExternal, MetadataRoot: workspace}
	source := managedTestPackage(t, filepath.Join(root, "exact-source"), map[string]string{
		"inject/context.md": "context\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "exact-links", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:exact-links", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(worktree, "context.md")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(request); err == nil ||
		!strings.Contains(err.Error(), "changed link ownership") {
		t.Fatalf("same-target replacement link was accepted: %v", err)
	}

	magicSource := managedTestPackage(t, filepath.Join(root, "magic-source"), map[string]string{
		"inject/" + magic: "managed\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "magic", magicSource); err != nil {
		t.Fatal(err)
	}
	magicFeaturePath := filepath.Join(workspace, "magic")
	if err := os.MkdirAll(filepath.Join(magicFeaturePath, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	magicWorktree := filepath.Join(magicFeaturePath, "worktrees", "root")
	managedTestGit(t, repo, "branch", "magic-root", "main")
	managedTestGit(t, repo, "worktree", "add", magicWorktree, "magic-root")
	if err := os.Remove(filepath.Join(magicWorktree, magic)); err != nil {
		t.Fatal(err)
	}
	if err := SaveStack(magicFeaturePath, Stack{Branches: []StackEntry{{Name: "root", Branch: "magic-root"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "magic", FeaturePath: magicFeaturePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:magic", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}); err == nil || !strings.Contains(err.Error(), "tracked by Git") {
		t.Fatalf("literal Git pathspec name was accepted as untracked: %v", err)
	}
}

func TestManagedTemplateCorruptLinkMetadataFailsBeforeConsent(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	repo := filepath.Join(root, "repo")
	managedTestGit(t, "", "init", "--initial-branch=main", repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, repo, "add", "README.md")
	managedTestGit(t, repo, "commit", "-m", "initial")
	workspace := filepath.Join(root, "workspace")
	if err := EnsureExternalWorkspaceMarker(workspace); err != nil {
		t.Fatal(err)
	}
	featurePath := filepath.Join(workspace, "corrupt-link")
	worktree := filepath.Join(featurePath, "worktrees", "root")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, repo, "branch", "corrupt-root", "main")
	managedTestGit(t, repo, "worktree", "add", worktree, "corrupt-root")
	if err := SaveStack(featurePath, Stack{Branches: []StackEntry{{Name: "root", Branch: "corrupt-root"}}}); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{RepoRoot: repo, Mode: ModeExternal, MetadataRoot: workspace}
	plan, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "corrupt-link", FeaturePath: featurePath,
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Agent: "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	statePath := ManagedTemplateStatePath(featurePath)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state ManagedTemplateState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	role := state.Roles[string(ManagedTemplateRoleWorktree)]
	role.Links[0].Source = "not-owned.md"
	role.Links[0].Target = "../wrong"
	state.Roles[string(ManagedTemplateRoleWorktree)] = role
	corrupt, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, append(corrupt, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "corrupt-link", FeaturePath: featurePath,
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
	}); err == nil || !strings.Contains(err.Error(), "unowned source") {
		t.Fatalf("corrupt source/target metadata reached consent or mutation: %v", err)
	}
}

func TestManagedTemplatePersistedScopeAgentAndSafetyFailures(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	globalSource := managedTestPackage(t, filepath.Join(root, "global"), map[string]string{
		"inject/shared.md": "global v1\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "same", globalSource); err != nil {
		t.Fatal(err)
	}
	feature := "persisted"
	featurePath := ws.FeaturePath(feature)
	initial := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:same", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}

	repoSource := managedTestPackage(t, filepath.Join(root, "repo-shadow"), map[string]string{
		"inject/shared.md": "repo shadow\n",
	}, "")
	if err := AddNamedTemplate(ws.RepoRoot, TemplateScopeRepo, "same", repoSource); err != nil {
		t.Fatal(err)
	}
	globalResolved, err := ResolveTemplateReference(ws.RepoRoot, "global:same")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalResolved.Path, "inject", "shared.md"), []byte("global v2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	saved := initial
	saved.Worktree = ManagedTemplateRoleRequest{}
	saved.Agent = ""
	saved.AgentSet = false
	plan, err = PlanManagedTemplate(saved)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NewState.Roles["worktree"].Source.Scope != TemplateScopeGlobal {
		t.Fatalf("repo shadow rebound persisted global source: %+v", plan.NewState.Roles["worktree"].Source)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(featurePath, "inject", "shared.md"))
	if err != nil || string(data) != "global v2\n" {
		t.Fatalf("saved global source was not refreshed: %q %v", data, err)
	}

	globalCfg := Config{AgentCommand: "copilot"}
	if err := SaveConfigFile(ConfigPath(), globalCfg); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(saved); err == nil || !strings.Contains(err.Error(), "saved rendering agent") {
		t.Fatalf("configured agent mismatch silently rerendered: %v", err)
	}
	saved.Agent = "copilot"
	saved.AgentSet = true
	if _, err := PlanManagedTemplate(saved); err != nil {
		t.Fatalf("explicit agent reselection failed: %v", err)
	}
	if got, err := ResolveManagedTemplateAgent("cc --resume"); err != nil || got != "claude" {
		t.Fatalf("known Claude alias not preserved: got=%q err=%v", got, err)
	}
	if _, err := ResolveManagedTemplateAgent("my-copilot-wrapper"); err == nil {
		t.Fatal("substring wrapper was guessed as a supported agent")
	}

	if err := RemoveNamedTemplate("", TemplateScopeGlobal, "same"); err != nil {
		t.Fatal(err)
	}
	saved.Agent = "claude"
	saved.AgentSet = true
	if _, err := PlanManagedTemplate(saved); err == nil || !strings.Contains(err.Error(), "saved source is unavailable") {
		t.Fatalf("missing saved global source fell through to repo shadow: %v", err)
	}

	parentFeature := "parent-symlink"
	parentPath := ws.FeaturePath(parentFeature)
	if err := os.MkdirAll(filepath.Join(parentPath, "inject"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-parent")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parentPath, "inject", ".github")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: parentFeature, FeaturePath: parentPath,
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Agent: "copilot", AgentSet: true,
	}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink destination parent was accepted: %v", err)
	}
	leafFeature := "leaf-symlink"
	leafPath := ws.FeaturePath(leafFeature)
	if err := os.MkdirAll(filepath.Join(leafPath, "inject"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(root, "outside-file")
	if err := os.WriteFile(outsideFile, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(leafPath, "inject", "CLAUDE.local.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: leafFeature, FeaturePath: leafPath,
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Agent: "claude", AgentSet: true,
	}); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("symlink destination leaf was followed or adopted: %v", err)
	}

	reservedSource := managedTestPackage(t, filepath.Join(root, "reserved"), map[string]string{
		"feature/stack.yaml": "branches: []\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "reserved", reservedSource); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "reserved", FeaturePath: ws.FeaturePath("reserved"),
		Roles:        []ManagedTemplateRole{ManagedTemplateRoleOrchestrator},
		Orchestrator: ManagedTemplateRoleRequest{Reference: "global:reserved", ReferenceSet: true},
		Agent:        "claude", AgentSet: true,
	}); err == nil || !strings.Contains(err.Error(), "control data") {
		t.Fatalf("reserved orchestrator destination was accepted: %v", err)
	}

	malformedFeature := ws.FeaturePath("malformed")
	if err := os.MkdirAll(malformedFeature, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ManagedTemplateStatePath(malformedFeature), []byte(`{"version":99,"roles":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManagedTemplateState(malformedFeature); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("future ownership state was accepted: %v", err)
	}
}

func TestManagedTemplateSavedRepoOriginSurvivesRepositoryRelocation(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "repo-origin"), map[string]string{
		"inject/context.md": "before move\n",
	}, "")
	if err := AddNamedTemplate(ws.RepoRoot, TemplateScopeRepo, "relocatable", source); err != nil {
		t.Fatal(err)
	}
	feature := "repo-relocation"
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: ws.FeaturePath(feature),
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "repo:relocatable", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	state, err := LoadManagedTemplateState(request.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	originID := state.Roles[string(ManagedTemplateRoleWorktree)].Source.OriginID
	if !markerIDRegexp.MatchString(originID) {
		t.Fatalf("saved repo registry origin identity is invalid: %q", originID)
	}

	movedRepo := filepath.Join(root, "moved-repo")
	if err := os.Rename(ws.RepoRoot, movedRepo); err != nil {
		t.Fatal(err)
	}
	movedWS, err := ResolveCurrentWorkspaceE(movedRepo, LoadConfigForRepo(movedRepo))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveTemplateReference(movedRepo, "repo:relocatable")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.OriginID != originID {
		t.Fatalf("repo relocation changed registry origin identity: got %q want %q", resolved.OriginID, originID)
	}
	if err := os.WriteFile(filepath.Join(resolved.Path, "inject", "context.md"), []byte("after move\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	request.Workspace = movedWS
	request.FeaturePath = movedWS.FeaturePath(feature)
	request.Worktree = ManagedTemplateRoleRequest{}
	request.Agent, request.AgentSet = "", false
	refreshed, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatalf("same repo registry origin did not survive relocation: %v", err)
	}
	if err := ApplyManagedTemplatePlan(refreshed); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(request.FeaturePath, "inject", "context.md"))
	if err != nil || string(data) != "after move\n" {
		t.Fatalf("relocated repo registry did not refresh content: %q %v", data, err)
	}
	state, err = LoadManagedTemplateState(request.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !sameManagedTestPath(state.Owner.RepoRoot, movedWS.RepoRoot) {
		t.Fatalf("managed owner path was not refreshed after repository relocation: %+v", state.Owner)
	}
}

func sameManagedTestPath(a, b string) bool {
	resolve := func(path string) string {
		if evaluated, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Clean(evaluated)
		}
		return filepath.Clean(path)
	}
	return resolve(a) == resolve(b)
}

func TestManagedTemplateCheckpointFailureDoesNotAdoptOutputs(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "checkpoint"), map[string]string{
		"inject/a.md": "a\n",
		"inject/b.md": "b\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "checkpoint", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "checkpoint", FeaturePath: ws.FeaturePath("checkpoint"),
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:checkpoint", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "before-state" {
			return errors.New("state checkpoint failure")
		}
		return nil
	}
	err = ApplyManagedTemplatePlan(plan)
	ManagedTemplateApplyStepHook = nil
	if err == nil || !strings.Contains(err.Error(), "state checkpoint failure") {
		t.Fatalf("checkpoint failure not surfaced: %v", err)
	}
	if _, err := os.Lstat(ManagedTemplateStatePath(request.FeaturePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed state publication produced ownership evidence: %v", err)
	}
	if _, err := PlanManagedTemplate(request); err == nil || !strings.Contains(err.Error(), "recovery evidence exists") {
		t.Fatalf("retry discarded pending evidence or adopted partial outputs: %v", err)
	}
	recoveryRoot := filepath.Join(request.FeaturePath, managedTemplateRecoveryName)
	if entries, err := os.ReadDir(recoveryRoot); err != nil || len(entries) != 1 {
		t.Fatalf("checkpoint failure did not retain bounded recovery evidence: entries=%v err=%v", entries, err)
	}
}

func TestManagedTemplatePreserveBeforeMutateCleanupAndConflictRetention(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "preserve"), map[string]string{
		"inject/context.md": "v1\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "preserve", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "preserve", FeaturePath: ws.FeaturePath("preserve"),
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:preserve", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	initial, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(initial); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveTemplateReference(ws.RepoRoot, "global:preserve")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolved.Path, "inject", "context.md"), []byte("v2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	update, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: request.Feature, FeaturePath: request.FeaturePath,
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(update); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(request.FeaturePath, managedTemplateRecoveryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful verified publication did not clean recovery area: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(request.FeaturePath, "inject", "context.md"))
	if err != nil || string(data) != "v2\n" {
		t.Fatalf("replacement was not published: %q %v", data, err)
	}

	if err := os.WriteFile(filepath.Join(resolved.Path, "inject", "context.md"), []byte("v3\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	conflictPlan, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: request.Feature, FeaturePath: request.FeaturePath,
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
	})
	if err != nil {
		t.Fatal(err)
	}
	ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "before-state" {
			return errors.New("retain recovery evidence")
		}
		return nil
	}
	err = ApplyManagedTemplatePlan(conflictPlan)
	ManagedTemplateApplyStepHook = nil
	if err == nil || !strings.Contains(err.Error(), "preserved pending evidence") {
		t.Fatalf("failure did not report preserved evidence: %v", err)
	}
	recoveryPath := filepath.Join(request.FeaturePath, managedTemplateRecoveryName)
	if entries, readErr := os.ReadDir(recoveryPath); readErr != nil || len(entries) != 1 {
		t.Fatalf("failure did not retain one bounded recovery operation: entries=%v err=%v", entries, readErr)
	}
}

func TestManagedTemplateFinalLeafPublicationNeverClobbersConcurrentPath(t *testing.T) {
	ws, _ := managedTestCheckoutWorkspace(t)
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "leaf-race", FeaturePath: ws.FeaturePath("leaf-race"),
		Roles: []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Agent: "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(request.FeaturePath, "inject", "CLAUDE.local.md")
	ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "before-write" && filepath.Clean(path) == filepath.Clean(destination) {
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			return os.WriteFile(destination, []byte("concurrent user content\n"), 0o644)
		}
		return nil
	}
	err = ApplyManagedTemplatePlan(plan)
	ManagedTemplateApplyStepHook = nil
	if err == nil || !strings.Contains(err.Error(), "without replacement") {
		t.Fatalf("concurrent leaf was clobbered or not diagnosed: %v", err)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil || string(data) != "concurrent user content\n" {
		t.Fatalf("concurrent leaf content was overwritten: %q %v", data, readErr)
	}
	if _, statErr := os.Lstat(ManagedTemplateStatePath(request.FeaturePath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed leaf publication wrote ownership state: %v", statErr)
	}
	if residue, residueErr := ManagedTemplateRecoveryResidue(request.FeaturePath); residueErr != nil || residue == "" {
		t.Fatalf("failed leaf publication did not retain pending evidence: residue=%q err=%v", residue, residueErr)
	}
}

func TestManagedTemplateCheckoutSessionContextStaysTemporary(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "checkout-session"), map[string]string{
		"inject/context.md": "context\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "checkout-session", source); err != nil {
		t.Fatal(err)
	}
	feature := "session"
	featurePath := ws.FeaturePath(feature)
	plan, err := PlanManagedTemplate(ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:checkout-session", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigFile(ConfigPath(), Config{AgentCommand: "claude"}); err != nil {
		t.Fatal(err)
	}
	links, err := PlanCheckoutSessionLinks(ws, feature, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("checkout managed context links = %+v", links)
	}
	for _, link := range links {
		if _, err := os.Lstat(link.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checkout context became permanent before session apply: %s %v", link.Path, err)
		}
	}

	excludes, err := ApplyCheckoutSessionLinks(ws.RepoRoot, links)
	if err != nil {
		t.Fatal(err)
	}
	if err := CleanupCheckoutSessionLinks(ws.RepoRoot, links, excludes); err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if _, err := os.Lstat(link.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checkout context survived session cleanup: %s %v", link.Path, err)
		}
	}

	if err := SaveConfigFile(ConfigPath(), Config{AgentCommand: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCheckoutSessionLinks(ws, feature, ""); err == nil ||
		!strings.Contains(err.Error(), "saved worktree rendering agent") {
		t.Fatalf("checkout session silently launched with mismatched guidance: %v", err)
	}
	if err := SaveConfigFile(ConfigPath(), Config{AgentCommand: "claude", InjectInto: ".context"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCheckoutSessionLinks(ws, feature, ""); err == nil ||
		!strings.Contains(err.Error(), "repository-root injection") {
		t.Fatalf("checkout managed context accepted non-root injection: %v", err)
	}
}

func TestManagedTemplatePendingLedgerBlocksCheckoutContextExposure(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "pending-checkout-v1"), map[string]string{
		"inject/context.md": "v1\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "pending-checkout", source); err != nil {
		t.Fatal(err)
	}
	feature := "pending-checkout"
	featurePath := ws.FeaturePath(feature)
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: feature, FeaturePath: featurePath,
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:pending-checkout", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	initial, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(initial); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNamedTemplate("", TemplateScopeGlobal, "pending-checkout"); err != nil {
		t.Fatal(err)
	}
	source = managedTestPackage(t, filepath.Join(root, "pending-checkout-v2"), map[string]string{
		"inject/context.md": "v2\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "pending-checkout", source); err != nil {
		t.Fatal(err)
	}
	updateRequest := request
	updateRequest.Worktree = ManagedTemplateRoleRequest{}
	updateRequest.Agent, updateRequest.AgentSet = "", false
	update, err := PlanManagedTemplate(updateRequest)
	if err != nil {
		t.Fatal(err)
	}
	ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "after-state-preserve" {
			return errors.New("stop after checkout ledger preservation")
		}
		return nil
	}
	err = ApplyManagedTemplatePlan(update)
	ManagedTemplateApplyStepHook = nil
	if err == nil || !strings.Contains(err.Error(), "checkout ledger preservation") {
		t.Fatalf("checkout ledger checkpoint did not stop: %v", err)
	}
	if _, err := ManagedTemplateStatePresent(featurePath); err == nil ||
		!strings.Contains(err.Error(), "managed template recovery evidence exists") {
		t.Fatalf("pending checkout ledger was reported as legacy: %v", err)
	}
	if err := ValidateManagedCheckoutContext(ws, feature, featurePath, ""); err == nil ||
		!strings.Contains(err.Error(), "managed template recovery evidence exists") {
		t.Fatalf("checkout context validation exposed pending managed state: %v", err)
	}
	if _, err := PlanCheckoutSessionLinks(ws, feature, ""); err == nil ||
		!strings.Contains(err.Error(), "managed template recovery evidence exists") {
		t.Fatalf("checkout session planning exposed pending managed context: %v", err)
	}
	for _, path := range []string{
		filepath.Join(ws.RepoRoot, "context.md"),
		filepath.Join(ws.RepoRoot, "CLAUDE.local.md"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending checkout recovery exposed temporary context at %s: %v", path, err)
		}
	}

}

func TestManagedTemplateStatePresentRejectsEmptyRecoveryEvidence(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	featurePath := filepath.Join(root, "legacy-with-empty-recovery")
	if err := os.MkdirAll(filepath.Join(featurePath, managedTemplateRecoveryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if present, err := ManagedTemplateStatePresent(featurePath); err == nil ||
		present || !strings.Contains(err.Error(), "managed template recovery evidence exists") {
		t.Fatalf("empty unverifiable recovery evidence dispatched as legacy: present=%v err=%v", present, err)
	}
}

func TestManagedTemplatePublishPinsFinalParentAndCleanupIsBounded(t *testing.T) {
	t.Run("publish parent replaced by recovery symlink", func(t *testing.T) {
		ws, root := managedTestCheckoutWorkspace(t)
		source := managedTestPackage(t, filepath.Join(root, "parent-swap"), map[string]string{
			"inject/nested/context.md": "context\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "parent-swap", source); err != nil {
			t.Fatal(err)
		}
		request := ManagedTemplatePlanRequest{
			Workspace: ws, Feature: "parent-swap", FeaturePath: ws.FeaturePath("parent-swap"),
			Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
			Worktree: ManagedTemplateRoleRequest{Reference: "global:parent-swap", ReferenceSet: true},
			Agent:    "claude", AgentSet: true,
		}
		plan, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		var redirected string
		ManagedTemplateIntentDurableHook = func(effect, original, private, intentPath string) error {
			if effect != "publish-active" || original != "inject/nested/context.md" {
				return nil
			}
			parent := filepath.Join(request.FeaturePath, "inject", "nested")
			if err := os.RemoveAll(parent); err != nil {
				return err
			}
			opDir := filepath.Dir(intentPath)
			target, err := filepath.Rel(filepath.Dir(parent), opDir)
			if err != nil {
				return err
			}
			redirected = filepath.Join(opDir, "context.md")
			return os.Symlink(target, parent)
		}
		err = ApplyManagedTemplatePlan(plan)
		ManagedTemplateIntentDurableHook = nil
		if err == nil {
			t.Fatal("publish followed a replaced in-root parent symlink")
		}
		if redirected == "" {
			t.Fatal("publish-active boundary was not reached")
		}
		if _, err := os.Lstat(redirected); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("publish created a redirected object in recovery area: %v", err)
		}
		if residue, err := ManagedTemplateRecoveryResidue(request.FeaturePath); err != nil || residue == "" {
			t.Fatalf("parent-swap refusal did not retain recovery evidence: residue=%q err=%v", residue, err)
		}
	})

	t.Run("unexpected cleanup content retained", func(t *testing.T) {
		ws, root := managedTestCheckoutWorkspace(t)
		source := managedTestPackage(t, filepath.Join(root, "cleanup-v1"), map[string]string{
			"inject/context.md": "v1\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "cleanup-bounded", source); err != nil {
			t.Fatal(err)
		}
		request := ManagedTemplatePlanRequest{
			Workspace: ws, Feature: "cleanup-bounded", FeaturePath: ws.FeaturePath("cleanup-bounded"),
			Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
			Worktree: ManagedTemplateRoleRequest{Reference: "global:cleanup-bounded", ReferenceSet: true},
			Agent:    "claude", AgentSet: true,
		}
		initial, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyManagedTemplatePlan(initial); err != nil {
			t.Fatal(err)
		}
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, "cleanup-bounded"); err != nil {
			t.Fatal(err)
		}
		source = managedTestPackage(t, filepath.Join(root, "cleanup-v2"), map[string]string{
			"inject/context.md": "v2\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "cleanup-bounded", source); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		update, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		var unexpected string
		ManagedTemplateApplyStepHook = func(step, path string) error {
			if step != "before-cleanup" || unexpected != "" {
				return nil
			}
			unexpected = filepath.Join(filepath.Dir(path), "unexpected-user-file")
			return os.WriteFile(unexpected, []byte("keep\n"), 0o600)
		}
		err = ApplyManagedTemplatePlan(update)
		ManagedTemplateApplyStepHook = nil
		if err == nil || !strings.Contains(err.Error(), "unexpected") {
			t.Fatalf("cleanup deleted unrecorded recovery content: %v", err)
		}
		if data, readErr := os.ReadFile(unexpected); readErr != nil || string(data) != "keep\n" {
			t.Fatalf("unexpected recovery content was removed: %q %v", data, readErr)
		}
	})

	t.Run("active publication edit retains every backup", func(t *testing.T) {
		ws, root := managedTestCheckoutWorkspace(t)
		source := managedTestPackage(t, filepath.Join(root, "active-edit-v1"), map[string]string{
			"inject/context.md": "v1\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "active-edit", source); err != nil {
			t.Fatal(err)
		}
		request := ManagedTemplatePlanRequest{
			Workspace: ws, Feature: "active-edit", FeaturePath: ws.FeaturePath("active-edit"),
			Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
			Worktree: ManagedTemplateRoleRequest{Reference: "global:active-edit", ReferenceSet: true},
			Agent:    "claude", AgentSet: true,
		}
		initial, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyManagedTemplatePlan(initial); err != nil {
			t.Fatal(err)
		}
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, "active-edit"); err != nil {
			t.Fatal(err)
		}
		source = managedTestPackage(t, filepath.Join(root, "active-edit-v2"), map[string]string{
			"inject/context.md": "v2\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "active-edit", source); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		update, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		active := filepath.Join(request.FeaturePath, "inject", "context.md")
		edited := false
		ManagedTemplateApplyStepHook = func(step, path string) error {
			if step != "before-cleanup" || edited {
				return nil
			}
			edited = true
			return os.WriteFile(active, []byte("operator edit\n"), 0o644)
		}
		err = ApplyManagedTemplatePlan(update)
		ManagedTemplateApplyStepHook = nil
		if err == nil || !strings.Contains(err.Error(), "staging file changed") {
			t.Fatalf("cleanup accepted an in-place edit of the active hard-linked publication: %v", err)
		}
		recoveryRoot := filepath.Join(request.FeaturePath, managedTemplateRecoveryName)
		foundOld := false
		if walkErr := filepath.WalkDir(recoveryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			data, readErr := os.ReadFile(path)
			if readErr == nil && string(data) == "v1\n" {
				foundOld = true
			}
			return nil
		}); walkErr != nil {
			t.Fatal(walkErr)
		}
		if !foundOld {
			t.Fatal("cleanup deleted the preserved old file before validating staged and active mappings")
		}
		if residue, err := ManagedTemplateRecoveryResidue(request.FeaturePath); err != nil || residue == "" {
			t.Fatalf("active edit refusal did not retain pending evidence: residue=%q err=%v", residue, err)
		}
	})

	t.Run("all roots preflight before any cleanup deletion", func(t *testing.T) {
		root, _ := managedTestEnvironment(t)
		repo := filepath.Join(root, "repo")
		managedTestGit(t, "", "init", "--initial-branch=main", repo)
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("repo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		managedTestGit(t, repo, "add", "README.md")
		managedTestGit(t, repo, "commit", "-m", "initial")
		workspace := filepath.Join(root, "workspace")
		if err := EnsureExternalWorkspaceMarker(workspace); err != nil {
			t.Fatal(err)
		}
		feature := "all-roots-cleanup"
		featurePath := filepath.Join(workspace, feature)
		worktree := filepath.Join(featurePath, "worktrees", "root")
		if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
			t.Fatal(err)
		}
		managedTestGit(t, repo, "branch", feature+"-root", "main")
		managedTestGit(t, repo, "worktree", "add", worktree, feature+"-root")
		if err := SaveStack(featurePath, Stack{Branches: []StackEntry{{
			Name: "root", Branch: feature + "-root",
		}}}); err != nil {
			t.Fatal(err)
		}
		source := managedTestPackage(t, filepath.Join(root, "all-roots-v1"), map[string]string{
			"inject/old.md": "old\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, feature, source); err != nil {
			t.Fatal(err)
		}
		request := ManagedTemplatePlanRequest{
			Workspace: Workspace{RepoRoot: repo, Mode: ModeExternal, MetadataRoot: workspace},
			Feature:   feature, FeaturePath: featurePath,
			Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
			Worktree: ManagedTemplateRoleRequest{Reference: "global:" + feature, ReferenceSet: true},
			Agent:    "claude", AgentSet: true,
		}
		initial, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyManagedTemplatePlan(initial); err != nil {
			t.Fatal(err)
		}
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, feature); err != nil {
			t.Fatal(err)
		}
		source = managedTestPackage(t, filepath.Join(root, "all-roots-v2"), map[string]string{
			"inject/new.md": "new\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, feature, source); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		update, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		edited := false
		ManagedTemplateApplyStepHook = func(step, path string) error {
			if step != "before-cleanup" || edited {
				return nil
			}
			edited = true
			return os.WriteFile(filepath.Join(featurePath, "inject", "new.md"), []byte("operator edit\n"), 0o644)
		}
		err = ApplyManagedTemplatePlan(update)
		ManagedTemplateApplyStepHook = nil
		if err == nil || !strings.Contains(err.Error(), "staging file changed") {
			t.Fatalf("cross-root cleanup accepted active edit: %v", err)
		}
		for _, path := range []string{featurePath, worktree} {
			if residue, residueErr := ManagedTemplateRecoveryResidue(path); residueErr != nil || residue == "" {
				t.Fatalf("cleanup deleted another root before all-root preflight completed at %s: residue=%q err=%v",
					path, residue, residueErr)
			}
		}
	})

	t.Run("recreated private cleanup name survives", func(t *testing.T) {
		ws, root := managedTestCheckoutWorkspace(t)
		source := managedTestPackage(t, filepath.Join(root, "private-recreate-v1"), map[string]string{
			"inject/context.md": "v1\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "private-recreate", source); err != nil {
			t.Fatal(err)
		}
		request := ManagedTemplatePlanRequest{
			Workspace: ws, Feature: "private-recreate", FeaturePath: ws.FeaturePath("private-recreate"),
			Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
			Worktree: ManagedTemplateRoleRequest{Reference: "global:private-recreate", ReferenceSet: true},
			Agent:    "claude", AgentSet: true,
		}
		initial, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyManagedTemplatePlan(initial); err != nil {
			t.Fatal(err)
		}
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, "private-recreate"); err != nil {
			t.Fatal(err)
		}
		source = managedTestPackage(t, filepath.Join(root, "private-recreate-v2"), map[string]string{
			"inject/context.md": "v2\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "private-recreate", source); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		update, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		var removedPrivate string
		cleanupIntent := 0
		ManagedTemplateIntentDurableHook = func(effect, original, private, intentPath string) error {
			if effect != "cleanup-private" {
				return nil
			}
			cleanupIntent++
			if cleanupIntent == 1 {
				removedPrivate = filepath.Join(request.FeaturePath, private)
				return nil
			}
			if cleanupIntent == 2 {
				return os.WriteFile(removedPrivate, []byte("operator recreated bytes\n"), 0o600)
			}
			return nil
		}
		err = ApplyManagedTemplatePlan(update)
		ManagedTemplateIntentDurableHook = nil
		if err == nil || !strings.Contains(err.Error(), "unexpected recovery content") {
			t.Fatalf("cleanup accepted a recreated private basename: %v", err)
		}
		data, readErr := os.ReadFile(removedPrivate)
		if readErr != nil || string(data) != "operator recreated bytes\n" {
			t.Fatalf("cleanup deleted recreated private-name bytes: %q %v", data, readErr)
		}
		if residue, residueErr := ManagedTemplateRecoveryResidue(request.FeaturePath); residueErr != nil || residue == "" {
			t.Fatalf("recreated private-name refusal lost pending evidence: residue=%q err=%v", residue, residueErr)
		}
	})
}

func TestManagedTemplatePreservePinsSourceAndRecoveryParents(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "preserve-parent-v1"), map[string]string{
		"inject/nested/context.md": "v1\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "preserve-parent", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "preserve-parent", FeaturePath: ws.FeaturePath("preserve-parent"),
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:preserve-parent", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	initial, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(initial); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(ManagedTemplateStatePath(request.FeaturePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveNamedTemplate("", TemplateScopeGlobal, "preserve-parent"); err != nil {
		t.Fatal(err)
	}
	empty := managedTestPackage(t, filepath.Join(root, "preserve-parent-empty"), nil, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "preserve-parent", empty); err != nil {
		t.Fatal(err)
	}
	request.Worktree = ManagedTemplateRoleRequest{}
	request.Agent, request.AgentSet = "", false
	update, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(request.FeaturePath, "inject", "nested", "context.md")
	parent := filepath.Dir(active)
	movedParent := filepath.Join(request.FeaturePath, "inject", "moved-nested")
	swapped := false
	ManagedTemplateApplyStepHook = func(step, path string) error {
		if step != "before-remove" || filepath.Clean(path) != filepath.Clean(active) || swapped {
			return nil
		}
		swapped = true
		if err := os.Rename(parent, movedParent); err != nil {
			return err
		}
		target, err := filepath.Rel(filepath.Dir(parent), movedParent)
		if err != nil {
			return err
		}
		return os.Symlink(target, parent)
	}
	err = ApplyManagedTemplatePlan(update)
	ManagedTemplateApplyStepHook = nil
	if err == nil {
		t.Fatal("preservation followed an in-root parent symlink")
	}
	data, readErr := os.ReadFile(active)
	if readErr != nil || string(data) != "v1\n" {
		t.Fatalf("parent-swap refusal changed the active file: %q %v", data, readErr)
	}
	ledgerAfter, readErr := os.ReadFile(ManagedTemplateStatePath(request.FeaturePath))
	if readErr != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatalf("parent-swap refusal changed the ownership ledger: %v", readErr)
	}
	if residue, residueErr := ManagedTemplateRecoveryResidue(request.FeaturePath); residueErr != nil || residue == "" {
		t.Fatalf("parent-swap refusal did not retain evidence: residue=%q err=%v", residue, residueErr)
	}
}

func TestManagedTemplateLinkEffectsRecheckGitTracking(t *testing.T) {
	setup := func(t *testing.T, name string, files map[string]string) (
		Workspace, string, string, string, ManagedTemplatePlanRequest,
	) {
		t.Helper()
		root, _ := managedTestEnvironment(t)
		repo := filepath.Join(root, "repo")
		managedTestGit(t, "", "init", "--initial-branch=main", repo)
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("repo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		managedTestGit(t, repo, "add", "README.md")
		managedTestGit(t, repo, "commit", "-m", "initial")
		workspace := filepath.Join(root, "workspace")
		if err := EnsureExternalWorkspaceMarker(workspace); err != nil {
			t.Fatal(err)
		}
		featurePath := filepath.Join(workspace, name)
		worktree := filepath.Join(featurePath, "worktrees", "root")
		if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
			t.Fatal(err)
		}
		managedTestGit(t, repo, "branch", name+"-root", "main")
		managedTestGit(t, repo, "worktree", "add", worktree, name+"-root")
		if err := SaveStack(featurePath, Stack{Branches: []StackEntry{{
			Name: "root", Branch: name + "-root",
		}}}); err != nil {
			t.Fatal(err)
		}
		ws := Workspace{RepoRoot: repo, Mode: ModeExternal, MetadataRoot: workspace}
		source := managedTestPackage(t, filepath.Join(root, "source"), files, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, name, source); err != nil {
			t.Fatal(err)
		}
		request := ManagedTemplatePlanRequest{
			Workspace: ws, Feature: name, FeaturePath: featurePath,
			Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
			Worktree: ManagedTemplateRoleRequest{Reference: "global:" + name, ReferenceSet: true},
			Agent:    "claude", AgentSet: true,
		}
		initial, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyManagedTemplatePlan(initial); err != nil {
			t.Fatal(err)
		}
		return ws, root, featurePath, worktree, request
	}

	t.Run("obsolete link becomes tracked before removal", func(t *testing.T) {
		_, root, featurePath, worktree, request := setup(t, "track-remove", map[string]string{
			"inject/obsolete.md": "obsolete\n",
		})
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, "track-remove"); err != nil {
			t.Fatal(err)
		}
		empty := managedTestPackage(t, filepath.Join(root, "empty"), nil, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "track-remove", empty); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		plan, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(worktree, "obsolete.md")
		ManagedTemplateApplyStepHook = func(step, path string) error {
			if step == "before-remove" && filepath.Clean(path) == filepath.Clean(linkPath) {
				managedTestGit(t, worktree, "add", "-f", "obsolete.md")
			}
			return nil
		}
		err = ApplyManagedTemplatePlan(plan)
		ManagedTemplateApplyStepHook = nil
		if err == nil || !strings.Contains(err.Error(), "tracked by Git") {
			t.Fatalf("tracked obsolete link was removed: %v", err)
		}
		if _, err := os.Readlink(linkPath); err != nil {
			t.Fatalf("tracked obsolete link was not retained: %v", err)
		}
		if tracked, err := managedGitTracked(worktree, "obsolete.md"); err != nil || !tracked {
			t.Fatalf("tracked obsolete link evidence was lost: tracked=%v err=%v", tracked, err)
		}
		if _, err := os.Stat(ManagedTemplateStatePath(featurePath)); err != nil {
			t.Fatalf("ownership ledger was lost: %v", err)
		}
	})

	t.Run("new link gains tracked-deleted index entry", func(t *testing.T) {
		_, root, _, worktree, request := setup(t, "track-create", nil)
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, "track-create"); err != nil {
			t.Fatal(err)
		}
		updated := managedTestPackage(t, filepath.Join(root, "updated"), map[string]string{
			"inject/race.md": "race\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "track-create", updated); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		plan, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(worktree, "race.md")
		ManagedTemplateApplyStepHook = func(step, path string) error {
			if step == "before-link" && filepath.Clean(path) == filepath.Clean(linkPath) {
				if err := os.WriteFile(linkPath, []byte("index only\n"), 0o644); err != nil {
					return err
				}
				managedTestGit(t, worktree, "add", "-f", "race.md")
				return os.Remove(linkPath)
			}
			return nil
		}
		err = ApplyManagedTemplatePlan(plan)
		ManagedTemplateApplyStepHook = nil
		if err == nil || !strings.Contains(err.Error(), "tracked by Git") {
			t.Fatalf("tracked-deleted destination was linked: %v", err)
		}
		if _, err := os.Lstat(linkPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tracked-deleted destination was published: %v", err)
		}
		if tracked, err := managedGitTracked(worktree, "race.md"); err != nil || !tracked {
			t.Fatalf("tracked-deleted index evidence was lost: tracked=%v err=%v", tracked, err)
		}
	})

	t.Run("unavailable Git evidence", func(t *testing.T) {
		_, root, _, worktree, request := setup(t, "track-unavailable", nil)
		if err := RemoveNamedTemplate("", TemplateScopeGlobal, "track-unavailable"); err != nil {
			t.Fatal(err)
		}
		updated := managedTestPackage(t, filepath.Join(root, "updated"), map[string]string{
			"inject/race.md": "race\n",
		}, "")
		if err := AddNamedTemplate("", TemplateScopeGlobal, "track-unavailable", updated); err != nil {
			t.Fatal(err)
		}
		request.Worktree = ManagedTemplateRoleRequest{}
		request.Agent, request.AgentSet = "", false
		plan, err := PlanManagedTemplate(request)
		if err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(worktree, ".git")
		saved := marker + ".saved"
		ManagedTemplateApplyStepHook = func(step, path string) error {
			if step == "before-link" && strings.HasSuffix(path, "race.md") {
				return os.Rename(marker, saved)
			}
			return nil
		}
		err = ApplyManagedTemplatePlan(plan)
		ManagedTemplateApplyStepHook = nil
		if _, statErr := os.Lstat(saved); statErr == nil {
			if restoreErr := os.Rename(saved, marker); restoreErr != nil {
				t.Fatal(restoreErr)
			}
		}
		if err == nil || !strings.Contains(err.Error(), "checking whether") {
			t.Fatalf("unavailable Git evidence was treated as untracked: %v", err)
		}
	})
}
