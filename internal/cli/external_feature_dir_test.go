package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestExternalFeatureDirectoryCommandMatrix(t *testing.T) {
	repo := setupGitRepo(t, "master")
	withWorkspaceEnv(t, repo)
	if err := createWorktree("feature", "root", "master", "", false); err != nil {
		t.Fatal(err)
	}
	if err := createWorktree("feature", "child", "root", "", false); err != nil {
		t.Fatal(err)
	}
	featurePath := internal.FeaturePath("feature")
	if err := os.MkdirAll(filepath.Join(featurePath, "inject"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featurePath, "inject", "CONTEXT.md"), []byte("context\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("TWS_ROOT"), ".tws-workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(featurePath, "docs", "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.AddDecision(featurePath, "root", "", "shared context", "info", ""); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	wantRepo, err := internal.MainRepoRootIn(repo)
	if err != nil {
		t.Fatal(err)
	}
	wantRepo, err = filepath.EvalSymlinks(wantRepo)
	if err != nil {
		t.Fatal(err)
	}
	wantWorkspace, err := filepath.EvalSymlinks(os.Getenv("TWS_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	if ws.Mode != internal.ModeExternal || ws.MetadataRoot != filepath.Clean(wantWorkspace) || ws.RepoRoot != filepath.Clean(wantRepo) {
		t.Fatalf("unexpected workspace: %+v", ws)
	}

	commands := []*cobraCommandCase{
		{name: "stack", run: func() error { cmd := stackCmd(); cmd.SetArgs([]string{"feature"}); return cmd.Execute() }},
		{name: "list", run: func() error { cmd := listCmd(); cmd.SetArgs(nil); return cmd.Execute() }},
		{name: "decisions", run: func() error {
			cmd := decisionsCmd()
			cmd.SetArgs([]string{"show", "feature", "--all"})
			return cmd.Execute()
		}},
		{name: "doctor", run: func() error { cmd := doctorCmd(); cmd.SetArgs([]string{"feature"}); return cmd.Execute() }},
		{name: "inject", run: func() error { cmd := injectCmd(); cmd.SetArgs([]string{"feature"}); return cmd.Execute() }},
		{name: "sync", run: func() error { cmd := syncCmd(); cmd.SetArgs([]string{"feature"}); return cmd.Execute() }},
	}
	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Fatalf("%s from feature dir: %v", tc.name, err)
			}
		})
	}

	if err := os.Chdir(os.Getenv("TWS_ROOT")); err != nil {
		t.Fatal(err)
	}
	if _, err := internal.RequireWorkspace(); err != nil {
		t.Fatalf("workspace root resolution: %v", err)
	}
	rootList := listCmd()
	if err := rootList.Execute(); err != nil {
		t.Fatalf("list from workspace root: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}

	state := internal.NewSyncState()
	state.FailedBranch = "root"
	if err := internal.SaveSyncState(featurePath, state); err != nil {
		t.Fatal(err)
	}
	cmd := syncCmd()
	cmd.SetArgs([]string{"feature", "--abort"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --abort from feature dir: %v", err)
	}
	if internal.HasSyncState(featurePath) {
		t.Fatal("sync state remains after abort")
	}
}

type cobraCommandCase struct {
	name string
	run  func() error
}
