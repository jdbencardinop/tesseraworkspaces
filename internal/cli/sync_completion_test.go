package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// §3.8 — completion. `--only` and `--from` complete the feature's non-archived
// logical stack entry names, in stack.yaml order. Every failure degrades to
// "no candidates": completion never errors, never prints, and never exits.
// ---------------------------------------------------------------------------

// completionFuncs returns the two functions actually registered on the command,
// so the test cannot pass while the wiring is missing.
func completionFuncs(t *testing.T) map[string]cobra.CompletionFunc {
	t.Helper()
	cmd := syncCmd()
	out := make(map[string]cobra.CompletionFunc, 2)
	for _, flag := range []string{"only", "from"} {
		fn, ok := cmd.GetFlagCompletionFunc(flag)
		if !ok || fn == nil {
			t.Fatalf("--%s has no registered completion function", flag)
		}
		out[flag] = fn
	}
	return out
}

// completeQuietly runs a completion function and fails if it wrote anything to
// stdout or stderr.
func completeQuietly(t *testing.T, fn cobra.CompletionFunc, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	var names []string
	var directive cobra.ShellCompDirective
	stdout, stderr := syncCaptureStreams(t, func() {
		names, directive = fn(syncCmd(), args, toComplete)
	})
	if stdout != "" || stderr != "" {
		t.Fatalf("completion must be silent; stdout=%q stderr=%q", stdout, stderr)
	}
	return names, directive
}

func assertNoCandidates(t *testing.T, names []string, directive cobra.ShellCompDirective, context string) {
	t.Helper()
	if names != nil {
		t.Fatalf("%s: names = %v, want nil", context, names)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("%s: directive = %v, want NoFileComp", context, directive)
	}
}

// TestSyncCompletion_RegisteredFuncsOfferStackEntries drives both registered
// functions over a real feature: they offer the non-archived logical names in
// stack.yaml order, never the Git branches.
func TestSyncCompletion_RegisteredFuncsOfferStackEntries(t *testing.T) {
	f := newScopedFixture(t)
	stack, err := internal.LoadStack(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stack.Branches {
		switch stack.Branches[i].Name {
		case "parent":
			stack.Branches[i].Branch = "user/parent"
		case "child":
			stack.Branches[i].Archived = true
		}
	}
	if err := internal.SaveStack(f.featurePath, stack); err != nil {
		t.Fatal(err)
	}

	for flag, fn := range completionFuncs(t) {
		names, directive := completeQuietly(t, fn, []string{f.feature}, "")
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Fatalf("--%s directive = %v, want NoFileComp", flag, directive)
		}
		if strings.Join(names, ",") != "root,parent" {
			t.Fatalf("--%s names = %v, want the non-archived entries in stack order [root parent]", flag, names)
		}
		for _, name := range names {
			if strings.Contains(name, "/") {
				t.Fatalf("--%s offered a Git branch, not a logical name: %q", flag, name)
			}
		}
	}
}

// TestSyncCompletion_DegradesToNoCandidates covers every failure the function
// can meet: no feature argument yet, an unresolvable workspace, a feature name
// that cannot be turned into a path, and a feature with no readable stack.
func TestSyncCompletion_DegradesToNoCandidates(t *testing.T) {
	t.Run("no-feature-argument", func(t *testing.T) {
		newScopedFixture(t)
		for flag, fn := range completionFuncs(t) {
			names, directive := completeQuietly(t, fn, nil, "")
			assertNoCandidates(t, names, directive, "--"+flag+" without args")
		}
	})

	t.Run("workspace-failure", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("TWS_ROOT", "")
		dir := t.TempDir()
		old, _ := os.Getwd()
		t.Cleanup(func() { _ = os.Chdir(old) })
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := internal.RequireWorkspace(); err == nil {
			t.Skip("this platform resolves a workspace outside any repository")
		}
		for flag, fn := range completionFuncs(t) {
			names, directive := completeQuietly(t, fn, []string{"feature"}, "")
			assertNoCandidates(t, names, directive, "--"+flag+" without a workspace")
		}
	})

	t.Run("feature-path-failure", func(t *testing.T) {
		newScopedFixture(t)
		for flag, fn := range completionFuncs(t) {
			names, directive := completeQuietly(t, fn, []string{"../escape"}, "")
			assertNoCandidates(t, names, directive, "--"+flag+" with an unresolvable feature name")
		}
	})

	t.Run("missing-stack", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.Remove(filepath.Join(f.featurePath, "stack.yaml")); err != nil {
			t.Fatal(err)
		}
		for flag, fn := range completionFuncs(t) {
			names, directive := completeQuietly(t, fn, []string{f.feature}, "")
			assertNoCandidates(t, names, directive, "--"+flag+" with no stack")
		}
	})

	t.Run("unreadable-stack", func(t *testing.T) {
		f := newScopedFixture(t)
		if err := os.WriteFile(filepath.Join(f.featurePath, "stack.yaml"), []byte("branches: [oops\n\t- broken\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		names, directive := completeQuietly(t, syncEntryCompletion, []string{f.feature}, "")
		assertNoCandidates(t, names, directive, "syncEntryCompletion with a corrupt stack")
	})
}

// TestSyncCompletion_EndToEndThroughCobra proves the registration is wired into
// the command, not merely present as a function: cobra's own completion entry
// point produces the candidates and the NoFileComp directive.
func TestSyncCompletion_EndToEndThroughCobra(t *testing.T) {
	f := newScopedFixture(t)

	stdout, stderr := syncCaptureStreams(t, func() {
		if code := syncExecute(func() *cobra.Command {
			root := &cobra.Command{Use: "tws"}
			root.AddCommand(syncCmd())
			return root
		}, "__complete", "sync", f.feature, "--only", ""); code != 0 {
			t.Errorf("completion must exit 0, got %d", code)
		}
	})
	if stderr != "" && !strings.Contains(stderr, "Completion ended with directive") {
		t.Fatalf("unexpected completion stderr: %q", stderr)
	}
	var offered []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		offered = append(offered, strings.Fields(line)[0])
	}
	sort.Strings(offered)
	if strings.Join(offered, ",") != "child,parent,root" {
		t.Fatalf("cobra offered %v, want every non-archived entry", offered)
	}
	if !strings.Contains(stdout, ":4") {
		t.Fatalf("missing the NoFileComp directive line:\n%s", stdout)
	}
}
