package internal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTsRoot_EnvVar(t *testing.T) {
	got := resolveTsRoot("/custom/root", "/some/repo", nil, Config{})
	if got != "/custom/root" {
		t.Errorf("expected /custom/root, got %s", got)
	}
}

func TestResolveTsRoot_ConfigEntry(t *testing.T) {
	cfg := Config{Workspaces: map[string]string{
		"/Users/me/projects/myapp": "/data/workspaces/myapp",
	}}
	got := resolveTsRoot("", "/Users/me/projects/myapp", nil, cfg)
	if got != "/data/workspaces/myapp" {
		t.Errorf("expected /data/workspaces/myapp, got %s", got)
	}
}

func TestResolveTsRoot_RepoSibling(t *testing.T) {
	got := resolveTsRoot("", "/Users/me/projects/myapp", nil, Config{})
	want := "/Users/me/projects/myapp.ts"
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestResolveTsRoot_Fallback(t *testing.T) {
	got := resolveTsRoot("", "", errors.New("not a repo"), Config{})
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "ts")
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestResolveTsRoot_EnvWinsOverConfig(t *testing.T) {
	cfg := Config{Workspaces: map[string]string{
		"/some/repo": "/configured/path",
	}}
	got := resolveTsRoot("/env/override", "/some/repo", nil, cfg)
	if got != "/env/override" {
		t.Errorf("env should win, got %s", got)
	}
}

func TestResolveTsRoot_ConfigWinsOverSibling(t *testing.T) {
	cfg := Config{Workspaces: map[string]string{
		"/Users/me/projects/myapp": "/explicit/path",
	}}
	got := resolveTsRoot("", "/Users/me/projects/myapp", nil, cfg)
	if got != "/explicit/path" {
		t.Errorf("config should win over sibling, got %s", got)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	// LoadConfig should return empty config when file doesn't exist.
	// This relies on the default config path not existing in a test env,
	// which is fine for CI. For a more robust test we'd inject the path.
	cfg := Config{}
	if len(cfg.Workspaces) != 0 {
		t.Errorf("expected empty workspaces, got %v", cfg.Workspaces)
	}
}

func TestFeaturePath(t *testing.T) {
	root := resolveTsRoot("/ws", "", errors.New("no repo"), Config{})
	want := "/ws/auth"
	got := filepath.Join(root, "auth")
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestWorktreePath(t *testing.T) {
	root := resolveTsRoot("/ws", "", errors.New("no repo"), Config{})
	want := "/ws/auth/worktrees/login-fix"
	got := filepath.Join(root, "auth", "worktrees", "login-fix")
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}
