package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type openSelectorFixture struct {
	root, repo, featurePath, path string
}

func newOpenSelectorFixture(t *testing.T, checkout bool, feature, logical string) openSelectorFixture {
	t.Helper()
	if !checkout {
		f := newOpenInjectFixture(t, feature)
		path := f.addWorktree(t, "", logical, "actual")
		if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{
			{Name: logical, Branch: "actual", Base: "main"},
		}}); err != nil {
			t.Fatal(err)
		}
		return openSelectorFixture{root: f.root, repo: f.repo, featurePath: f.featurePath, path: path}
	}
	repo, ws := setupOpenCheckoutCLI(t)
	path := ws.FeaturePath(feature)
	if feature != "feature" {
		if err := os.Rename(ws.FeaturePath("feature"), path); err != nil {
			t.Fatal(err)
		}
	}
	gitInDir(t, repo, "branch", "actual")
	if err := internal.SaveStack(path, internal.Stack{Branches: []internal.StackEntry{
		{Name: logical, Branch: "actual", Base: "main"},
	}}); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, repo, "switch", "actual")
	return openSelectorFixture{root: repo, repo: repo, featurePath: path, path: repo}
}

func TestOpenCommandOptionEscapedSelectors(t *testing.T) {
	for _, checkout := range []bool{false, true} {
		for _, tc := range []struct {
			name, feature, logical string
			partial                bool
		}{
			{"leading-dash", "-feature", "-logical", false},
			{"literal-markers", "--", "--", false},
			{"partial-selectors", "feature", "-logical", true},
		} {
			t.Run(fmt.Sprintf("checkout=%t/%s", checkout, tc.name), func(t *testing.T) {
				f := newOpenSelectorFixture(t, checkout, tc.feature, tc.logical)
				makeArgs := func(flags []string, featureDir, childMarker bool, child ...string) []string {
					args := append([]string(nil), flags...)
					if tc.partial && !featureDir {
						args = append([]string{tc.feature}, args...)
						args = append(args, "--", tc.logical)
					} else {
						args = append(args, "--", tc.feature)
						if !featureDir {
							args = append(args, tc.logical)
						}
					}
					if childMarker {
						args = append(args, "--")
					}
					return append(args, child...)
				}
				for _, featureDir := range []bool{false, true} {
					flags := []string{"--print-path"}
					want := f.path
					if featureDir {
						flags = append(flags, "--feature-dir")
						want = f.featurePath
					}
					before := snapshotOpenPaths(t, f.root)
					out, err := runOpenModeCommand(t, makeArgs(flags, featureDir, false)...)
					if err != nil || out != want+"\n" {
						t.Fatalf("query: out=%q err=%v want=%q", out, err, want)
					}
					assertOpenPathsUnchanged(t, f.root, before)
				}
				out, err := runOpenModeCommand(t, "--feature-dir", "--no-agent", "--", tc.feature)
				if err != nil || out != "cd "+f.featurePath+"\n" {
					t.Fatalf("legacy option escaping changed: out=%q err=%v", out, err)
				}

				support := canonicalOpenFixturePath(t, t.TempDir())
				program := filepath.Join(support, "capture")
				capture := filepath.Join(support, "argv")
				branchCapture := filepath.Join(support, "branch")
				writeOpenProgram(t, program, `printf '%s\000' "$PWD" "$@" > "$OPEN_CAPTURE"
if [ -n "$OPEN_BRANCH_CAPTURE" ]; then git symbolic-ref --short HEAD > "$OPEN_BRANCH_CAPTURE"; fi`)
				t.Setenv("OPEN_CAPTURE", capture)
				child := []string{"", "--", "README.md", "--print-path", "literal;"}
				for _, featureDir := range []bool{false, true} {
					flags := []string{"--command", program, "--no-tmux"}
					wantPath := f.path
					if featureDir {
						flags = append(flags, "--feature-dir")
						wantPath = f.featurePath
						t.Setenv("OPEN_BRANCH_CAPTURE", "")
					} else {
						t.Setenv("OPEN_BRANCH_CAPTURE", branchCapture)
						if checkout {
							gitInDir(t, f.repo, "switch", "main")
						}
					}
					original := gitInDir(t, f.repo, "symbolic-ref", "--short", "HEAD")
					out, err := runOpenModeCommand(t, makeArgs(flags, featureDir, true, child...)...)
					if err != nil || out != "" {
						t.Fatalf("command: out=%q err=%v", out, err)
					}
					data, err := os.ReadFile(capture)
					want := strings.Join(append([]string{wantPath}, child...), "\x00") + "\x00"
					if err != nil || string(data) != want {
						t.Fatalf("literal cwd/argv changed: got=%q want=%q err=%v", data, want, err)
					}
					if !featureDir {
						data, err := os.ReadFile(branchCapture)
						if err != nil || string(data) != "actual\n" {
							t.Fatalf("logical selector did not activate actual branch: %q %v", data, err)
						}
					}
					if got := gitInDir(t, f.repo, "symbolic-ref", "--short", "HEAD"); got != original {
						t.Fatalf("caller branch changed: %q -> %q", original, got)
					}
					flags = []string{"--command", "/usr/bin/true", "--no-tmux"}
					if featureDir {
						flags = append(flags, "--feature-dir")
					}
					if out, err := runOpenModeCommand(t, makeArgs(flags, featureDir, false)...); err != nil || out != "" {
						t.Fatalf("zero-argument command requires no second marker: %q %v", out, err)
					}
				}

				for _, args := range [][]string{
					makeArgs([]string{"--print-path"}, false, true, "README.md"),
					makeArgs([]string{"--print-path", "--feature-dir"}, true, true, "--"),
					makeArgs([]string{"--command", program, "--no-tmux"}, false, false, "README.md"),
					makeArgs([]string{"--command", program, "--no-tmux", "--feature-dir"}, true, false, "README.md"),
				} {
					before := snapshotOpenPaths(t, f.root)
					beforeSupport := snapshotOpenPaths(t, support)
					if out, err := runOpenModeCommand(t, args...); err == nil || out != "" {
						t.Fatalf("invalid post-selector arguments accepted: args=%q out=%q err=%v", args, out, err)
					}
					assertOpenPathsUnchanged(t, f.root, before)
					assertOpenPathsUnchanged(t, support, beforeSupport)
				}

				complete := func(args []string) string {
					t.Helper()
					root := &cobra.Command{Use: "tws"}
					root.AddCommand(openCmd())
					out, _, err := runManagedCobra(t, root, append([]string{"__complete", "open"}, args...), nil)
					if err != nil {
						t.Fatal(err)
					}
					return out
				}
				if !checkout {
					// Feature completion, unlike a named route, cannot
					// choose a configured fallback using a known feature.
					t.Setenv("TWS_ROOT", filepath.Dir(f.featurePath))
				}
				before := snapshotOpenPaths(t, f.root)
				beforeSupport := snapshotOpenPaths(t, support)
				if out := complete([]string{"--feature-dir", "--print-path", "--", ""}); !strings.Contains(out, tc.feature+"\n") {
					t.Fatalf("escaped feature completion missing: %q", out)
				}
				if strings.HasPrefix(tc.feature, "-") {
					if out := complete([]string{"--feature-dir", "--print-path", "--", "-"}); !strings.Contains(out, tc.feature+"\n") {
						t.Fatalf("leading-dash feature completion missing: %q", out)
					}
				}
				branchArgs := []string{"--command", program, "--", tc.feature, ""}
				if tc.partial {
					branchArgs = []string{tc.feature, "--command", program, "--", ""}
				}
				if out := complete(branchArgs); !strings.Contains(out, tc.logical+"\n") {
					t.Fatalf("escaped logical branch completion missing: %q", out)
				}
				branchArgs[len(branchArgs)-1] = "-"
				if out := complete(branchArgs); !strings.Contains(out, tc.logical+"\n") {
					t.Fatalf("leading-dash branch completion missing: %q", out)
				}
				if out := complete(makeArgs([]string{"--command", program}, false, true, "")); out != ":4\n" {
					t.Fatalf("child arguments offered tws candidates: %q", out)
				}
				assertOpenPathsUnchanged(t, f.root, before)
				assertOpenPathsUnchanged(t, support, beforeSupport)
			})
		}
	}
}
