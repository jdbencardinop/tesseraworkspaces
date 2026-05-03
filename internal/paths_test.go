package internal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTwsRoot_EnvVar(t *testing.T) {
	got := resolveTwsRoot("/custom/root", "/somewhere", "/some/repo", nil, Config{})
	if got != "/custom/root" {
		t.Errorf("expected /custom/root, got %s", got)
	}
}

func TestResolveTwsRoot_ConfigEntry(t *testing.T) {
	cfg := Config{Workspaces: map[string]string{
		"/Users/me/projects/myapp": "/data/workspaces/myapp",
	}}
	got := resolveTwsRoot("", "/somewhere", "/Users/me/projects/myapp", nil, cfg)
	if got != "/data/workspaces/myapp" {
		t.Errorf("expected /data/workspaces/myapp, got %s", got)
	}
}

func TestResolveTwsRoot_RepoSibling(t *testing.T) {
	got := resolveTwsRoot("", "/somewhere", "/Users/me/projects/myapp", nil, Config{})
	want := "/Users/me/projects/myapp.tws"
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestResolveTwsRoot_Fallback(t *testing.T) {
	got := resolveTwsRoot("", "/somewhere", "", errors.New("not a repo"), Config{})
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "tws")
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestResolveTwsRoot_EnvWinsOverConfig(t *testing.T) {
	cfg := Config{Workspaces: map[string]string{
		"/some/repo": "/configured/path",
	}}
	got := resolveTwsRoot("/env/override", "/somewhere", "/some/repo", nil, cfg)
	if got != "/env/override" {
		t.Errorf("env should win, got %s", got)
	}
}

func TestResolveTwsRoot_ConfigWinsOverSibling(t *testing.T) {
	cfg := Config{Workspaces: map[string]string{
		"/Users/me/projects/myapp": "/explicit/path",
	}}
	got := resolveTwsRoot("", "/somewhere", "/Users/me/projects/myapp", nil, cfg)
	if got != "/explicit/path" {
		t.Errorf("config should win over sibling, got %s", got)
	}
}

func TestFeaturePath(t *testing.T) {
	root := resolveTwsRoot("/ws", "/somewhere", "", errors.New("no repo"), Config{})
	want := "/ws/auth"
	got := filepath.Join(root, "auth")
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

func TestWorktreePath(t *testing.T) {
	root := resolveTwsRoot("/ws", "/somewhere", "", errors.New("no repo"), Config{})
	want := "/ws/auth/worktrees/login-fix"
	got := filepath.Join(root, "auth", "worktrees", "login-fix")
	if got != want {
		t.Errorf("expected %s, got %s", want, got)
	}
}

// --- Workspace detection tests ---

func TestDetectWorkspaceRoot_MarkerFile(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := filepath.Join(tmp, "myapp.tws")
	nested := filepath.Join(wsRoot, "auth", "worktrees", "branch-a")
	os.MkdirAll(filepath.Join(wsRoot, ".tws-workspace"), 0755) //nolint:errcheck
	os.MkdirAll(nested, 0755)                                  //nolint:errcheck

	got := DetectWorkspaceRoot(nested, Config{})
	if got != wsRoot {
		t.Errorf("expected %s, got %s", wsRoot, got)
	}
}

func TestDetectWorkspaceRoot_ConfigMatch(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := filepath.Join(tmp, "custom-workspace")
	nested := filepath.Join(wsRoot, "feature", "worktrees", "branch")
	os.MkdirAll(nested, 0755) //nolint:errcheck

	cfg := Config{Workspaces: map[string]string{
		"/some/repo": wsRoot,
	}}
	got := DetectWorkspaceRoot(nested, cfg)
	if got != wsRoot {
		t.Errorf("expected %s, got %s", wsRoot, got)
	}
}

func TestDetectWorkspaceRoot_GlobalDefault(t *testing.T) {
	home, _ := os.UserHomeDir()
	globalDefault := filepath.Join(home, "tws")

	nested := filepath.Join(globalDefault, "some-feature")
	if _, err := os.Stat(globalDefault); os.IsNotExist(err) {
		os.MkdirAll(nested, 0755)         //nolint:errcheck
		defer os.RemoveAll(globalDefault) //nolint:errcheck
	} else {
		os.MkdirAll(nested, 0755) //nolint:errcheck
		defer os.Remove(nested)   //nolint:errcheck
	}

	got := DetectWorkspaceRoot(nested, Config{})
	if got != globalDefault {
		t.Errorf("expected %s, got %s", globalDefault, got)
	}
}

func TestDetectWorkspaceRoot_NoMatch(t *testing.T) {
	got := DetectWorkspaceRoot("/some/random/path", Config{})
	if got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestResolveTwsRoot_WorkspaceDetectionWins(t *testing.T) {
	tmp := t.TempDir()
	wsRoot := filepath.Join(tmp, "myapp.tws")
	cwd := filepath.Join(wsRoot, "auth", "worktrees", "branch-a")
	os.MkdirAll(filepath.Join(wsRoot, ".tws-workspace"), 0755) //nolint:errcheck
	os.MkdirAll(cwd, 0755)                                     //nolint:errcheck

	got := resolveTwsRoot("", cwd, "/some/repo", nil, Config{})
	if got != wsRoot {
		t.Errorf("workspace detection should win, expected %s, got %s", wsRoot, got)
	}
}
