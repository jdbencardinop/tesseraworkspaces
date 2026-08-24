package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Flag presence, axis resolution, and the pure command-line refusals (I1-I8).
// ---------------------------------------------------------------------------

func parseSyncFlags(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := syncCmd()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v): %v", args, err)
	}
	return cmd
}

func TestSyncModes_TriggerSet(t *testing.T) {
	cases := []struct {
		args    []string
		newMode bool
	}{
		{nil, false},
		{[]string{"--push"}, false},
		{[]string{"-v"}, false},
		{[]string{"--continue"}, false},
		{[]string{"--abort"}, false},
		{[]string{"--test", "go test ./..."}, false},
		{[]string{"--fetch"}, true},
		{[]string{"--no-fetch"}, true},
		{[]string{"--full"}, true},
		{[]string{"--local-only"}, true},
		{[]string{"--only", "child"}, true},
		{[]string{"--from", "parent"}, true},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := parseSyncFlags(t, tc.args...)
			_, newMode, _, err := resolveSyncPolicy(cmd, internal.ModeExternal)
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if newMode != tc.newMode {
				t.Fatalf("newMode = %v, want %v", newMode, tc.newMode)
			}
		})
	}
}

func TestSyncModes_AxisDefaultsPerMode(t *testing.T) {
	cmd := parseSyncFlags(t, "--only", "child")
	ext, _, _, err := resolveSyncPolicy(cmd, internal.ModeExternal)
	if err != nil {
		t.Fatal(err)
	}
	if ext.Fetch != internal.SyncFetchEnabled || ext.Propagation != internal.SyncPropagationFull || ext.ScopeKind != internal.SyncScopeOne {
		t.Fatalf("external defaults = %+v", ext)
	}
	cmd = parseSyncFlags(t, "--only", "child")
	co, _, _, err := resolveSyncPolicy(cmd, internal.ModeCheckout)
	if err != nil {
		t.Fatal(err)
	}
	if co.Fetch != internal.SyncFetchDisabled || co.Propagation != internal.SyncPropagationFull {
		t.Fatalf("checkout defaults = %+v", co)
	}
}

func TestSyncModes_AllTwelveCellsAreSelectable(t *testing.T) {
	type cell struct {
		args        []string
		fetch       internal.SyncFetchPolicy
		propagation internal.SyncPropagationPolicy
		scope       internal.SyncScopeKind
	}
	cells := []cell{
		{[]string{"--fetch", "--full"}, internal.SyncFetchEnabled, internal.SyncPropagationFull, internal.SyncScopeAll},
		{[]string{"--fetch", "--local-only"}, internal.SyncFetchEnabled, internal.SyncPropagationLocalOnly, internal.SyncScopeAll},
		{[]string{"--no-fetch", "--full"}, internal.SyncFetchDisabled, internal.SyncPropagationFull, internal.SyncScopeAll},
		{[]string{"--no-fetch", "--local-only"}, internal.SyncFetchDisabled, internal.SyncPropagationLocalOnly, internal.SyncScopeAll},
		{[]string{"--fetch", "--full", "--only", "x"}, internal.SyncFetchEnabled, internal.SyncPropagationFull, internal.SyncScopeOne},
		{[]string{"--fetch", "--local-only", "--only", "x"}, internal.SyncFetchEnabled, internal.SyncPropagationLocalOnly, internal.SyncScopeOne},
		{[]string{"--no-fetch", "--full", "--only", "x"}, internal.SyncFetchDisabled, internal.SyncPropagationFull, internal.SyncScopeOne},
		{[]string{"--no-fetch", "--local-only", "--only", "x"}, internal.SyncFetchDisabled, internal.SyncPropagationLocalOnly, internal.SyncScopeOne},
		{[]string{"--fetch", "--full", "--from", "x"}, internal.SyncFetchEnabled, internal.SyncPropagationFull, internal.SyncScopeSubtree},
		{[]string{"--fetch", "--local-only", "--from", "x"}, internal.SyncFetchEnabled, internal.SyncPropagationLocalOnly, internal.SyncScopeSubtree},
		{[]string{"--no-fetch", "--full", "--from", "x"}, internal.SyncFetchDisabled, internal.SyncPropagationFull, internal.SyncScopeSubtree},
		{[]string{"--no-fetch", "--local-only", "--from", "x"}, internal.SyncFetchDisabled, internal.SyncPropagationLocalOnly, internal.SyncScopeSubtree},
	}
	if len(cells) != 12 {
		t.Fatalf("the matrix has 12 cells, not %d", len(cells))
	}
	for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
		for _, c := range cells {
			cmd := parseSyncFlags(t, c.args...)
			p, newMode, _, err := resolveSyncPolicy(cmd, mode)
			if err != nil {
				t.Fatalf("%v in %s: %v", c.args, mode, err)
			}
			if !newMode {
				t.Fatalf("%v must be a new-mode run", c.args)
			}
			if p.Fetch != c.fetch || p.Propagation != c.propagation || p.ScopeKind != c.scope {
				t.Fatalf("%v in %s resolved to %+v", c.args, mode, p)
			}
		}
	}
}

func TestSyncModes_IncompatibilityMatrix(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"I1", []string{"--fetch", "--no-fetch"}, "--fetch and --no-fetch are mutually exclusive"},
		{"I2", []string{"--full", "--local-only"}, "--full and --local-only are mutually exclusive"},
		{"I3", []string{"--only", "a", "--from", "b"}, "--only and --from are mutually exclusive"},
		{"I4-fetch", []string{"--fetch=false"}, "--fetch does not take an explicit value; use --no-fetch to disable automatic fetch"},
		{"I4-no-fetch", []string{"--no-fetch=false"}, "--no-fetch does not take an explicit value; use --fetch to enable automatic fetch"},
		{"I4-full", []string{"--full=false"}, "--full does not take an explicit value; use --local-only to restrict propagation"},
		{"I4-local-only", []string{"--local-only=false"}, "--local-only does not take an explicit value; use --full to advance anchors"},
		{"I5", []string{"--only", ""}, "--only requires a stack entry name"},
		{"I6", []string{"--from", ""}, "--from requires a stack entry name"},
		{"I7", []string{"--continue", "--abort", "--local-only"}, "--continue and --abort are mutually exclusive"},
		{"I8", []string{"--abort", "--only", "a"}, "--abort cannot be combined with --only; abort is defined by the persisted run"},
		{"I8-multi", []string{"--abort", "--local-only", "--no-fetch"}, "--abort cannot be combined with --local-only, --no-fetch; abort is defined by the persisted run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []internal.WorkspaceMode{internal.ModeExternal, internal.ModeCheckout} {
				cmd := parseSyncFlags(t, tc.args...)
				_, _, _, err := resolveSyncPolicy(cmd, mode)
				if err == nil {
					t.Fatalf("%v must be refused in %s", tc.args, mode)
				}
				if err.Error() != tc.want {
					t.Fatalf("%v in %s: got %q, want %q", tc.args, mode, err.Error(), tc.want)
				}
			}
		})
	}
}

func TestSyncModes_I7BeforeI8(t *testing.T) {
	cmd := parseSyncFlags(t, "--continue", "--abort", "--only", "a")
	_, _, _, err := resolveSyncPolicy(cmd, internal.ModeExternal)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if err.Error() != "--continue and --abort are mutually exclusive" {
		t.Fatalf("I7 must win over I8; got %q", err.Error())
	}
	if strings.Contains(err.Error(), "--abort cannot be combined") {
		t.Fatal("I8 must not be evaluated when I7 fired")
	}
}

func TestSyncModes_ContinueAbortWithoutTriggerIsDeferred(t *testing.T) {
	cmd := parseSyncFlags(t, "--continue", "--abort")
	_, newMode, _, err := resolveSyncPolicy(cmd, internal.ModeExternal)
	if err != nil {
		t.Fatalf("without a trigger flag I7 is deferred to the state read; got %v", err)
	}
	if newMode {
		t.Fatal("--continue --abort is not a new-mode invocation")
	}
}

func TestSyncModes_PushKeepsBooleanMeaning(t *testing.T) {
	cmd := parseSyncFlags(t, "--push=false")
	_, _, changed, err := resolveSyncPolicy(cmd, internal.ModeExternal)
	if err != nil {
		t.Fatalf("--push=false is a legal, distinct input: %v", err)
	}
	if !changed["push"] {
		t.Fatal("Changed(push) must distinguish explicit false from omitted")
	}
	cmd = parseSyncFlags(t)
	_, _, changed, _ = resolveSyncPolicy(cmd, internal.ModeExternal)
	if changed["push"] {
		t.Fatal("an omitted --push is not Changed")
	}
}

func TestSyncModes_ChangedMapKeySet(t *testing.T) {
	cmd := parseSyncFlags(t)
	_, _, changed, err := resolveSyncPolicy(cmd, internal.ModeExternal)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"fetch": true, "no-fetch": true, "full": true, "local-only": true, "only": true, "from": true, "push": true}
	if len(changed) != len(want) {
		t.Fatalf("presence map keys = %v", changed)
	}
	for key := range want {
		if _, ok := changed[key]; !ok {
			t.Fatalf("presence map is missing %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Help drift (§3.9)
// ---------------------------------------------------------------------------

func TestSyncModes_HelpFlagBlockIsAlphabetical(t *testing.T) {
	cmd := syncCmd()
	usage := cmd.LocalFlags().FlagUsages()
	var names []string
	re := regexp.MustCompile(`--([a-z-]+)`)
	for _, line := range strings.Split(usage, "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		names = append(names, m[1])
	}
	want := []string{"abort", "approve-plan", "continue", "fetch", "from", "full", "json", "local-only", "max-replay-per-entry", "max-replay-total", "no-fetch", "only", "plan", "push", "test", "verbose"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("help flag block = %v, want %v", names, want)
	}
	if !cmd.Flags().SortFlags {
		t.Fatal("SortFlags must stay at its default true")
	}
}

// ---------------------------------------------------------------------------
// Marker generation and the I17 pre-flight (§8.2)
// ---------------------------------------------------------------------------

func TestSyncModes_MarkerGrammar(t *testing.T) {
	re := regexp.MustCompile(`^tws-scoped-sync-[0-9a-f]{32}\.lock$`)
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		marker, err := newSyncMarker()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(marker) {
			t.Fatalf("marker %q does not match the grammar", marker)
		}
		if len(marker) != 53 {
			t.Fatalf("marker length = %d, want 53", len(marker))
		}
		if seen[marker] {
			t.Fatalf("marker %q repeated: it must be a per-run nonce", marker)
		}
		seen[marker] = true
	}
}

func TestSyncModes_MarkerIsRejectedByGit(t *testing.T) {
	marker, err := newSyncMarker()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", marker)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1")
	if runErr := cmd.Run(); runErr == nil {
		t.Fatalf("git must refuse %q as a branch name", marker)
	}
	cmd = exec.Command("git", "check-ref-format", "refs/heads/"+marker)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1")
	if runErr := cmd.Run(); runErr == nil {
		t.Fatalf("git must refuse refs/heads/%s", marker)
	}
}

func TestSyncModes_MarkerCollisionPreflight(t *testing.T) {
	marker := "tws-scoped-sync-0123456789abcdef0123456789abcdef.lock"
	byName := internal.Stack{Branches: []internal.StackEntry{{Name: marker, Base: "master"}}}
	err := syncMarkerCollision(byName, marker)
	if err == nil || !strings.Contains(err.Error(), "refusing to start: generated sync marker") {
		t.Fatalf("I17 must fire on a Name collision; got %v", err)
	}
	byBranch := internal.Stack{Branches: []internal.StackEntry{{Name: "work", Branch: marker, Base: "master"}}}
	if err := syncMarkerCollision(byBranch, marker); err == nil {
		t.Fatal("I17 must fire on a GitBranch collision too")
	}
	clean := internal.Stack{Branches: []internal.StackEntry{{Name: "work", Base: "master"}}}
	if err := syncMarkerCollision(clean, marker); err != nil {
		t.Fatalf("no collision must pass: %v", err)
	}
}

func TestSyncModes_MarkerSeamIsOverridable(t *testing.T) {
	old := syncMarkerFn
	t.Cleanup(func() { syncMarkerFn = old })
	syncMarkerFn = func() (string, error) {
		return "tws-scoped-sync-ffffffffffffffffffffffffffffffff.lock", nil
	}
	got, err := syncMarkerFn()
	if err != nil || got != "tws-scoped-sync-ffffffffffffffffffffffffffffffff.lock" {
		t.Fatalf("the package-cli seam must be overridable; got %q %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// The layout resolver (§3.11)
// ---------------------------------------------------------------------------

func writeStackYAML(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "branches:\n  - name: root\n    base: master\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncModes_LayoutAgreeingCandidatesProbeNothing(t *testing.T) {
	root := t.TempDir()
	ws := internal.Workspace{Mode: internal.ModeExternal, MetadataRoot: root}
	layout, err := resolveExternalSyncLayout(ws, root, "auth")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "auth")
	if layout.FeaturePath != want {
		t.Fatalf("FeaturePath = %q, want %q", layout.FeaturePath, want)
	}
	if layout.WorktreesRoot != filepath.Join(want, "worktrees") {
		t.Fatalf("WorktreesRoot = %q", layout.WorktreesRoot)
	}
	if layout.WorktreePath("root") != filepath.Join(want, "worktrees", "root") {
		t.Fatalf("WorktreePath = %q", layout.WorktreePath("root"))
	}
}

func TestSyncModes_LayoutCandidateBWinsWithReadableStack(t *testing.T) {
	base := t.TempDir()
	twsRoot := filepath.Join(base, "b-root")
	metaRoot := filepath.Join(base, "a-root")
	writeStackYAML(t, filepath.Join(twsRoot, "auth"))
	writeStackYAML(t, filepath.Join(metaRoot, "auth"))
	ws := internal.Workspace{Mode: internal.ModeExternal, MetadataRoot: metaRoot}

	layout, err := resolveExternalSyncLayout(ws, twsRoot, "auth")
	if err != nil {
		t.Fatal(err)
	}
	// B wins even when A also has a readable stack: TWS_ROOT priority and
	// today's execution root are both preserved.
	if layout.FeaturePath != filepath.Join(twsRoot, "auth") {
		t.Fatalf("candidate B must win; got %q", layout.FeaturePath)
	}
	if layout.WorktreesRoot != filepath.Join(twsRoot, "auth", "worktrees") {
		t.Fatal("a run can never load a stack from one root and probe worktrees under the other")
	}
}

func TestSyncModes_LayoutFallsBackToAThenB(t *testing.T) {
	base := t.TempDir()
	twsRoot := filepath.Join(base, "b-root")
	metaRoot := filepath.Join(base, "a-root")
	writeStackYAML(t, filepath.Join(metaRoot, "auth"))
	ws := internal.Workspace{Mode: internal.ModeExternal, MetadataRoot: metaRoot}

	layout, err := resolveExternalSyncLayout(ws, twsRoot, "auth")
	if err != nil {
		t.Fatal(err)
	}
	if layout.FeaturePath != filepath.Join(metaRoot, "auth") {
		t.Fatalf("candidate A must win when only it has a readable stack; got %q", layout.FeaturePath)
	}

	// Neither root has a stack: B wins, so the frozen syncFallback path keeps
	// today's root.
	empty := internal.Workspace{Mode: internal.ModeExternal, MetadataRoot: filepath.Join(base, "nothing")}
	layout, err = resolveExternalSyncLayout(empty, twsRoot, "auth")
	if err != nil {
		t.Fatal(err)
	}
	if layout.FeaturePath != filepath.Join(twsRoot, "auth") {
		t.Fatalf("with no stack anywhere candidate B must win; got %q", layout.FeaturePath)
	}
}

// ---------------------------------------------------------------------------
// I18 symlink refusals built from the classifier's recorded facts
// ---------------------------------------------------------------------------

func TestSyncModes_SymlinkRefusals(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte("state_version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, internal.SyncRunStatePath(dir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := classifySyncState(dir, false); err == nil ||
		!strings.Contains(err.Error(), "runtime state path is a symlink") {
		t.Fatalf("a payload symlink must be refused on every run; got %v", err)
	}
}

func TestSyncModes_LegacySymlinkAllowedOnFrozenPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "legacy.yaml")
	if err := os.WriteFile(target, []byte("started_at: \"x\"\nfailed_branch: parent\npending: []\ncompleted: []\nskipped: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, internal.SyncStatePath(dir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	st, err := classifySyncState(dir, false)
	if err != nil {
		t.Fatalf("an ordinary legacy symlink is not refused on a no-flag run: %v", err)
	}
	if !st.LegacySymlink {
		t.Fatal("the symlink fact must still be recorded")
	}
	if _, err := classifySyncState(dir, true); err == nil {
		t.Fatal("a new-mode run must refuse the same path")
	}
}

// ---------------------------------------------------------------------------
// The §8.7 message table
// ---------------------------------------------------------------------------

func TestSyncModes_CellMessages(t *testing.T) {
	layout := externalSyncLayout{FeaturePath: "/f/auth", WorktreesRoot: "/f/auth/worktrees"}
	payload := &internal.SyncRunState{StateVersion: 2, FailedBranch: "child"}
	guard := &internal.SyncRunGuard{PID: 4242, Created: "2026-01-01T00:00:00Z"}

	cases := []struct {
		name    string
		state   internal.SyncExternalState
		verb    syncVerb
		wantNil bool
		want    string
	}{
		{"cell1-plain", internal.SyncExternalState{Cell: 1}, syncVerbPlain, true, ""},
		{"cell7-abort", internal.SyncExternalState{Cell: 7}, syncVerbAbort, true, ""},
		{
			"cell2-plain",
			internal.SyncExternalState{Cell: 2, Payload: payload},
			syncVerbPlain, false,
			"a scoped sync record survives without its state file for \"auth\": it failed on child (worktree /f/auth/worktrees/child) and that rebase was never aborted. Resolve or abort it there, then run: tws sync auth --abort",
		},
		{"cell2-abort", internal.SyncExternalState{Cell: 2, Payload: payload}, syncVerbAbort, true, ""},
		{
			"cell4-plain",
			internal.SyncExternalState{Cell: 4},
			syncVerbPlain, false,
			"a scoped sync left partial state for \"auth\": it was interrupted either while starting up or while finishing, and this cannot be distinguished on disk; work may or may not have been done. Inspect the worktrees, then run: tws sync auth --abort",
		},
		{"cell4-abort", internal.SyncExternalState{Cell: 4}, syncVerbAbort, true, ""},
		{
			"cell5-plain",
			internal.SyncExternalState{Cell: 5, Payload: payload},
			syncVerbPlain, false,
			"a scoped sync is incomplete (failed on: child); use --continue or --abort",
		},
		{"cell5-continue", internal.SyncExternalState{Cell: 5, Payload: payload}, syncVerbContinue, true, ""},
		{"cell5-abort", internal.SyncExternalState{Cell: 5, Payload: payload}, syncVerbAbort, true, ""},
		{
			"cell5-live-plain",
			internal.SyncExternalState{Cell: 5, Payload: payload, Guard: guard, GuardLive: true},
			syncVerbPlain, false,
			"a scoped sync is already running for \"auth\" (pid 4242, started 2026-01-01T00:00:00Z); wait for it or use --continue/--abort after it exits",
		},
		{
			"cell5-live-abort",
			internal.SyncExternalState{Cell: 5, Payload: payload, Guard: guard, GuardLive: true},
			syncVerbAbort, false,
			"a scoped sync is running for \"auth\" (pid 4242); wait for it to exit before --abort",
		},
		{
			"cell10-plain",
			internal.SyncExternalState{Cell: 10, LegacyPath: "/f/auth/.sync-state.yaml", LegacyErr: errText("broken")},
			syncVerbPlain, false,
			"sync state at /f/auth/.sync-state.yaml is unreadable: broken",
		},
		{
			"cell10-abort",
			internal.SyncExternalState{Cell: 10, LegacyPath: "/f/auth/.sync-state.yaml", LegacyErr: errText("broken")},
			syncVerbAbort, false,
			"sync state at /f/auth/.sync-state.yaml is unreadable: broken; inspect and remove it manually",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := syncCellRefusal(tc.verb, "auth", layout, tc.state)
			if tc.wantNil {
				if err != nil {
					t.Fatalf("expected the verb to proceed; got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if err.Error() != tc.want {
				t.Fatalf("got  %q\nwant %q", err.Error(), tc.want)
			}
		})
	}
}

func TestSyncModes_UnreadablePayloadCellsShareOneMessage(t *testing.T) {
	layout := externalSyncLayout{FeaturePath: "/f/auth", WorktreesRoot: "/f/auth/worktrees"}
	for _, cell := range []internal.SyncStateCell{3, 6, 9, 12} {
		state := internal.SyncExternalState{Cell: cell, PayloadPath: "/f/auth/.sync-state.v2.yaml", PayloadErr: errText("bad version")}
		for _, verb := range []syncVerb{syncVerbPlain, syncVerbContinue, syncVerbAbort} {
			err := syncCellRefusal(verb, "auth", layout, state)
			want := "scoped sync state at /f/auth/.sync-state.v2.yaml is unreadable or uses an unsupported version (bad version); inspect it and remove it manually — tws will not guess"
			if err == nil || err.Error() != want {
				t.Fatalf("cell %d / %s: got %v", cell, verb, err)
			}
		}
	}
}

func TestSyncModes_MixedAndCorruptCells(t *testing.T) {
	layout := externalSyncLayout{FeaturePath: "/f/auth", WorktreesRoot: "/f/auth/worktrees"}
	mixed := internal.SyncExternalState{
		Cell:        8,
		Legacy:      &internal.SyncState{FailedBranch: "parent"},
		Payload:     &internal.SyncRunState{FailedBranch: "child"},
		LegacyPath:  "/f/auth/.sync-state.yaml",
		PayloadPath: "/f/auth/.sync-state.v2.yaml",
	}
	err := syncCellRefusal(syncVerbPlain, "auth", layout, mixed)
	want := "two unfinished syncs are recorded for \"auth\": a legacy sync failed on parent and a scoped sync failed on child; resolve both before syncing (inspect /f/auth/.sync-state.yaml and /f/auth/.sync-state.v2.yaml)"
	if err == nil || err.Error() != want {
		t.Fatalf("cell 8 plain:\n got %v\nwant %q", err, want)
	}
	err = syncCellRefusal(syncVerbAbort, "auth", layout, mixed)
	want = "refusing to clear two unfinished syncs at once for \"auth\": a legacy sync failed on parent and a scoped sync failed on child; inspect /f/auth/.sync-state.yaml and /f/auth/.sync-state.v2.yaml and remove them explicitly"
	if err == nil || err.Error() != want {
		t.Fatalf("cell 8 abort:\n got %v\nwant %q", err, want)
	}

	cell11 := internal.SyncExternalState{
		Cell:        11,
		Payload:     &internal.SyncRunState{FailedBranch: "child"},
		LegacyPath:  "/f/auth/.sync-state.yaml",
		PayloadPath: "/f/auth/.sync-state.v2.yaml",
	}
	err = syncCellRefusal(syncVerbPlain, "auth", layout, cell11)
	want = "sync state at /f/auth/.sync-state.yaml is unreadable, and a scoped sync record beside it failed on child (worktree /f/auth/worktrees/child); resolve or abort that rebase, then remove /f/auth/.sync-state.v2.yaml manually — tws will not guess"
	if err == nil || err.Error() != want {
		t.Fatalf("cell 11 plain:\n got %v\nwant %q", err, want)
	}
	err = syncCellRefusal(syncVerbAbort, "auth", layout, cell11)
	want = "refusing to clear unreadable sync state at /f/auth/.sync-state.yaml while a scoped sync record beside it is still unfinished: it failed on child (worktree /f/auth/worktrees/child); inspect both and remove /f/auth/.sync-state.v2.yaml explicitly"
	if err == nil || err.Error() != want {
		t.Fatalf("cell 11 abort:\n got %v\nwant %q", err, want)
	}
}

func TestSyncModes_NoOpLineMatchesFormatSyncStatus(t *testing.T) {
	got := syncAnchorNoOpLine("root")
	want := "  [-] root (no in-stack parent edge to propagate)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got != formatSyncStatus("root", "no in-stack parent edge to propagate", "skipped") {
		t.Fatal("the no-op line must reuse formatSyncStatus, not a second formatter")
	}
}

type errText string

func (e errText) Error() string { return string(e) }
