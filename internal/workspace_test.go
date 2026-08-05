package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- helpers ----------

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Initialize a bare-minimum repo structure (no actual git needed for resolution)
	return dir
}

func writeTwsConfig(t *testing.T, dir string, content string) {
	t.Helper()
	twsDir := filepath.Join(dir, ".tws")
	if err := os.MkdirAll(twsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twsDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ---------- ResolveCurrentWorkspace tests ----------

func TestResolveWorkspace_CheckoutMode(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: checkout\n")

	ws := ResolveCurrentWorkspace(repo, Config{})

	if ws.Mode != ModeCheckout {
		t.Errorf("expected checkout, got %s", ws.Mode)
	}
	want := filepath.Join(canonicalize(repo), ".tws")
	if ws.MetadataRoot != want {
		t.Errorf("MetadataRoot = %s, want %s", ws.MetadataRoot, want)
	}
}

func TestResolveWorkspace_CheckoutMetadataRootIsInsideRepo(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: checkout\n")

	ws := ResolveCurrentWorkspace(repo, Config{})

	canon := canonicalize(repo)
	// MetadataRoot must be <repo>/.tws, NOT the external sibling.
	if ws.MetadataRoot != filepath.Join(canon, ".tws") {
		t.Errorf("checkout MetadataRoot should be inside repo, got %s", ws.MetadataRoot)
	}
	// Verify it's NOT the sibling path
	if ws.MetadataRoot == canon+".tws" {
		t.Error("checkout MetadataRoot must not be the external sibling")
	}
}

func TestResolveWorkspace_ExplicitExternalMode(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: external\n")

	ws := ResolveCurrentWorkspace(repo, Config{})

	if ws.Mode != ModeExternal {
		t.Errorf("expected external, got %s", ws.Mode)
	}
	canon := canonicalize(repo)
	want := canon + ".tws"
	if ws.MetadataRoot != want {
		t.Errorf("MetadataRoot = %s, want %s", ws.MetadataRoot, want)
	}
}

func TestResolveWorkspace_LegacyConfigNoMode(t *testing.T) {
	// .tws/config.yaml exists but no workspace_mode -> defaults to external.
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "agent_command: claude\n")

	ws := ResolveCurrentWorkspace(repo, Config{})

	if ws.Mode != ModeExternal {
		t.Errorf("expected external for legacy config, got %s", ws.Mode)
	}
	canon := canonicalize(repo)
	if ws.MetadataRoot != canon+".tws" {
		t.Errorf("MetadataRoot = %s, want sibling", ws.MetadataRoot)
	}
}

func TestResolveWorkspace_InvalidMode(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: foobar\n")

	ws := ResolveCurrentWorkspace(repo, Config{})

	// Invalid mode falls back to external.
	if ws.Mode != ModeExternal {
		t.Errorf("expected external for invalid mode, got %s", ws.Mode)
	}
}

func TestResolveWorkspaceE_InvalidModeReturnsError(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: foobar\n")

	_, err := ResolveCurrentWorkspaceE(repo, Config{})
	if err == nil {
		t.Fatal("expected error for invalid workspace_mode, got nil")
	}
	if !strings.Contains(err.Error(), "foobar") {
		t.Errorf("error should mention the invalid mode, got: %v", err)
	}
}

func TestResolveWorkspace_NoTwsDir(t *testing.T) {
	// Repo with no .tws directory at all -> external.
	repo := setupTestRepo(t)

	ws := ResolveCurrentWorkspace(repo, Config{})

	if ws.Mode != ModeExternal {
		t.Errorf("expected external when no .tws exists, got %s", ws.Mode)
	}
}

func TestResolveWorkspace_TwsDirExistsNoConfig(t *testing.T) {
	// .tws directory exists but no config.yaml -> external (just a dir marker).
	repo := setupTestRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".tws"), 0755); err != nil {
		t.Fatal(err)
	}

	ws := ResolveCurrentWorkspace(repo, Config{})

	if ws.Mode != ModeExternal {
		t.Errorf("expected external when .tws exists without config, got %s", ws.Mode)
	}
}

func TestResolveWorkspace_ExternalWithConfigWorkspaces(t *testing.T) {
	repo := setupTestRepo(t)
	canon := canonicalize(repo)
	customRoot := t.TempDir()
	cfg := Config{
		Workspaces: map[string]string{
			canon: customRoot,
		},
	}

	ws := ResolveCurrentWorkspace(repo, cfg)

	if ws.MetadataRoot != customRoot {
		t.Errorf("MetadataRoot = %s, want %s (from config workspaces)", ws.MetadataRoot, customRoot)
	}
}

func TestResolveWorkspace_ExternalWithSymlinkConfigKey(t *testing.T) {
	realRepo := setupTestRepo(t)
	linkRoot := t.TempDir()
	linkRepo := filepath.Join(linkRoot, "repo-link")
	if err := os.Symlink(realRepo, linkRepo); err != nil {
		t.Fatal(err)
	}
	customRoot := t.TempDir()
	cfg := Config{Workspaces: map[string]string{cleanAbsolute(linkRepo): customRoot}}

	ws := ResolveCurrentWorkspace(linkRepo, cfg)
	if ws.MetadataRoot != customRoot {
		t.Errorf("MetadataRoot = %s, want %s from symlink config key", ws.MetadataRoot, customRoot)
	}
}

func TestResolveWorkspace_ExternalSiblingDefault(t *testing.T) {
	repo := setupTestRepo(t)
	canon := canonicalize(repo)

	ws := ResolveCurrentWorkspace(repo, Config{})

	want := canon + ".tws"
	if ws.MetadataRoot != want {
		t.Errorf("MetadataRoot = %s, want %s", ws.MetadataRoot, want)
	}
}

// ---------- Capabilities tests ----------

func TestCapabilities_ExternalMode(t *testing.T) {
	caps := capsFor(ModeExternal)
	if !caps.Stack {
		t.Error("external mode should support stacks")
	}
	if !caps.LinkedWorktrees {
		t.Error("external mode should support linked worktrees")
	}
}

func TestCapabilities_CheckoutMode(t *testing.T) {
	caps := capsFor(ModeCheckout)
	if !caps.Stack {
		t.Error("checkout mode should support stacks")
	}
	if caps.LinkedWorktrees {
		t.Error("checkout mode should NOT support linked worktrees")
	}
}

// ---------- StableID tests ----------

func TestStableID_Deterministic(t *testing.T) {
	id1 := stableID("/foo/bar")
	id2 := stableID("/foo/bar")
	if id1 != id2 {
		t.Errorf("stableID not deterministic: %s != %s", id1, id2)
	}
	if len(id1) != 16 { // 8 bytes = 16 hex chars
		t.Errorf("stableID length = %d, want 16", len(id1))
	}
}

func TestStableID_DifferentPaths(t *testing.T) {
	id1 := stableID("/foo/bar")
	id2 := stableID("/foo/baz")
	if id1 == id2 {
		t.Error("stableID should differ for different paths")
	}
}

func TestCanonicalize_AbsoluteClean(t *testing.T) {
	repo := setupTestRepo(t)
	// Create a subdirectory so EvalSymlinks can resolve the full path
	sub := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	dirty := sub + "/.."
	canon := canonicalize(dirty)
	want := canonicalize(repo)
	if canon != want {
		t.Errorf("canonicalize(%q) = %q, want %q", dirty, canon, want)
	}
}

// ---------- FeaturePath / WorktreePath through Workspace ----------

func TestWorkspace_FeaturePath(t *testing.T) {
	ws := Workspace{MetadataRoot: "/data/myproject.tws"}
	got := ws.FeaturePath("auth")
	want := "/data/myproject.tws/auth"
	if got != want {
		t.Errorf("FeaturePath = %s, want %s", got, want)
	}
}

func TestWorkspace_WorktreePath(t *testing.T) {
	ws := Workspace{MetadataRoot: "/data/myproject.tws"}
	got := ws.WorktreePath("auth", "fix-login")
	want := "/data/myproject.tws/auth/worktrees/fix-login"
	if got != want {
		t.Errorf("WorktreePath = %s, want %s", got, want)
	}
}

func TestWorkspace_CheckoutFeaturePath(t *testing.T) {
	ws := Workspace{
		MetadataRoot: "/repo/.tws",
		Mode:         ModeCheckout,
	}
	got := ws.FeaturePath("billing")
	want := "/repo/.tws/features/billing"
	if got != want {
		t.Errorf("FeaturePath = %s, want %s", got, want)
	}
}

func TestWorkspace_CheckoutWorktreePath(t *testing.T) {
	ws := Workspace{
		MetadataRoot: "/repo/.tws",
		Mode:         ModeCheckout,
	}
	got := ws.WorktreePath("billing", "add-stripe")
	if got != "" {
		t.Errorf("WorktreePath in checkout mode should be empty, got %s", got)
	}
}

func TestWorkspace_LegacyFeaturePath(t *testing.T) {
	ws := Workspace{
		MetadataRoot: "/repo/.tws",
		Mode:         ModeCheckout,
	}
	got := ws.LegacyFeaturePath("billing")
	want := "/repo/.tws/billing"
	if got != want {
		t.Errorf("LegacyFeaturePath = %s, want %s", got, want)
	}
}

// ---------- resolveTwsRoot backward compat ----------

func TestResolveTwsRoot_ExternalBackwardCompat(t *testing.T) {
	// Ensure the refactored resolveTwsRoot produces byte-for-byte
	// identical results for existing external scenarios.

	t.Run("env_var_wins", func(t *testing.T) {
		got := resolveTwsRoot("/custom/root", "/somewhere", "/some/repo", nil, Config{})
		if got != "/custom/root" {
			t.Errorf("expected /custom/root, got %s", got)
		}
	})

	t.Run("config_workspace_map", func(t *testing.T) {
		cfg := Config{
			Workspaces: map[string]string{
				"/repos/myproject": "/data/workspaces/myproject",
			},
		}
		got := resolveTwsRoot("", "/somewhere", "/repos/myproject", nil, cfg)
		if got != "/data/workspaces/myproject" {
			t.Errorf("expected /data/workspaces/myproject, got %s", got)
		}
	})

	t.Run("sibling_default", func(t *testing.T) {
		got := resolveTwsRoot("", "/somewhere", "/repos/myproject", nil, Config{})
		if got != "/repos/myproject.tws" {
			t.Errorf("expected /repos/myproject.tws, got %s", got)
		}
	})
}

func TestResolveTwsRoot_CheckoutMode(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: checkout\n")
	canon := canonicalize(repo)

	got := resolveTwsRoot("", "/somewhere", canon, nil, Config{})
	want := filepath.Join(canon, ".tws")
	if got != want {
		t.Errorf("expected %s for checkout mode, got %s", want, got)
	}
}

func TestResolveTwsRoot_NoRepo(t *testing.T) {
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "tws")
	got := resolveTwsRoot("", "/somewhere", "", os.ErrNotExist, Config{})
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

// ---------- LinkedWorktreeInvocation ----------

func TestResolveWorkspace_LinkedWorktreeDisabledInCheckout(t *testing.T) {
	repo := setupTestRepo(t)
	writeTwsConfig(t, repo, "workspace_mode: checkout\n")

	ws := ResolveCurrentWorkspace(repo, Config{})

	if ws.Caps.LinkedWorktrees {
		t.Error("checkout mode must not claim LinkedWorktrees capability")
	}
	if !ws.Caps.Stack {
		t.Error("checkout mode must retain Stack capability")
	}
}

// ---------- parseMode ----------

func TestParseMode(t *testing.T) {
	tests := []struct {
		raw     string
		wantOK  bool
		wantVal WorkspaceMode
	}{
		{"external", true, ModeExternal},
		{"checkout", true, ModeCheckout},
		{"", false, ""},
		{"foobar", false, ""},
		{"EXTERNAL", false, ""},
	}
	for _, tt := range tests {
		mode, ok := parseMode(tt.raw)
		if ok != tt.wantOK {
			t.Errorf("parseMode(%q): ok=%v, want %v", tt.raw, ok, tt.wantOK)
		}
		if mode != tt.wantVal {
			t.Errorf("parseMode(%q): mode=%v, want %v", tt.raw, mode, tt.wantVal)
		}
	}
}

// ---------- metadataRootExists ----------

func TestMetadataRootExists(t *testing.T) {
	dir := t.TempDir()
	if !metadataRootExists(dir) {
		t.Error("existing dir should return true")
	}
	if metadataRootExists(filepath.Join(dir, "nonexistent")) {
		t.Error("nonexistent dir should return false")
	}
	// File, not dir
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if metadataRootExists(f) {
		t.Error("file should return false (must be dir)")
	}
}

// ---------- Config WorkspaceMode merge ----------

func TestConfigMerge_WorkspaceMode(t *testing.T) {
	dir := t.TempDir()

	globalCfg := "agent_command: claude\n"
	repoCfg := "workspace_mode: checkout\nbranch_prefix: ws/\n"

	globalPath := filepath.Join(dir, "global.yaml")
	repoPath := filepath.Join(dir, "repo.yaml")
	if err := os.WriteFile(globalPath, []byte(globalCfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoPath, []byte(repoCfg), 0644); err != nil {
		t.Fatal(err)
	}

	global := loadConfigFile(globalPath)
	repo := loadConfigFile(repoPath)

	// Simulate merge
	if repo.WorkspaceMode != "" {
		global.WorkspaceMode = repo.WorkspaceMode
	}
	if repo.BranchPrefix != "" {
		global.BranchPrefix = repo.BranchPrefix
	}
	if global.WorkspaceMode != "checkout" {
		t.Errorf("expected checkout, got %s", global.WorkspaceMode)
	}
	if global.BranchPrefix != "ws/" {
		t.Errorf("expected ws/, got %s", global.BranchPrefix)
	}
}
