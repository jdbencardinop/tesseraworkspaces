package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// AC 12 / AC 13 — all twelve cells of fetch × propagation × scope, executed in
// both workspace modes against real repositories, real remotes, and (in
// external mode) real linked worktrees. Every stack entry's SHA is asserted in
// every cell, together with the scope invariants and, for every `no-fetch`
// cell, the untouched remote-tracking refs.
// ---------------------------------------------------------------------------

type syncCell struct {
	fetch internal.SyncFetchPolicy
	prop  internal.SyncPropagationPolicy
	scope internal.SyncScopeKind
}

func syncTwelveCells() []syncCell {
	var cells []syncCell
	for _, fetch := range []internal.SyncFetchPolicy{internal.SyncFetchEnabled, internal.SyncFetchDisabled} {
		for _, prop := range []internal.SyncPropagationPolicy{internal.SyncPropagationFull, internal.SyncPropagationLocalOnly} {
			for _, scope := range []internal.SyncScopeKind{internal.SyncScopeAll, internal.SyncScopeOne, internal.SyncScopeSubtree} {
				cells = append(cells, syncCell{fetch: fetch, prop: prop, scope: scope})
			}
		}
	}
	return cells
}

func (c syncCell) name() string {
	return fmt.Sprintf("%s_%s_%s", c.fetch, c.prop, c.scope)
}

// policy is the frozen decision this cell must produce, whichever mode runs it.
func (c syncCell) policy(selector string) internal.SyncRunPolicy {
	p := internal.SyncRunPolicy{Fetch: c.fetch, Propagation: c.prop, ScopeKind: c.scope}
	if c.scope != internal.SyncScopeAll {
		p.Selector = selector
	}
	return p
}

// flags renders the cell as the command line an operator would type. Every cell
// is spelled explicitly, so no cell can silently ride on a mode default.
func (c syncCell) flags(selector string) []string {
	args := []string{"--" + string(c.fetch)}
	switch c.prop {
	case internal.SyncPropagationLocalOnly:
		args = append(args, "--local-only")
	default:
		args = append(args, "--full")
	}
	switch c.scope {
	case internal.SyncScopeOne:
		args = append(args, "--only", selector)
	case internal.SyncScopeSubtree:
		args = append(args, "--from", selector)
	}
	return args
}

func (c syncCell) header(selector string) string {
	p := c.policy(selector)
	return fmt.Sprintf("Sync mode: fetch=%s propagation=%s scope=%s", p.Fetch, p.Propagation, p.ScopeLabel())
}

// cellEntryExpect is the complete expectation for one stack entry in one cell.
type cellEntryExpect struct {
	selected bool // the run considers the entry at all
	skipped  bool // selected, but a local-only anchor: a no-op success
	moved    bool // the branch SHA must differ from its pre-run value
}

// expect maps the three fixture entries onto their per-cell expectation. The
// fixtures are built so that every propagation edge has real work, so
// `selected && !skipped` implies `moved` for every non-anchor entry, and the
// anchor moves only when it is both selected under `full` and given a new base
// by a fetch.
//
// anchorMovesOnFetch is true for external mode, where the anchor's base is the
// remote-tracking ref a fetch advances, and false for checkout mode, where the
// anchor's base is a local branch and the fetch axis only refreshes
// remote-tracking refs (§10.3).
func (c syncCell) expect(anchor, mid, leaf string, anchorMovesOnFetch bool) map[string]cellEntryExpect {
	selected := map[string]bool{mid: true}
	switch c.scope {
	case internal.SyncScopeAll:
		selected[anchor] = true
		selected[leaf] = true
	case internal.SyncScopeSubtree:
		selected[leaf] = true
	}

	out := make(map[string]cellEntryExpect, 3)
	for _, name := range []string{anchor, mid, leaf} {
		e := cellEntryExpect{selected: selected[name]}
		switch {
		case !e.selected:
		case name == anchor && c.prop == internal.SyncPropagationLocalOnly:
			e.skipped = true
		case name == anchor:
			e.moved = !anchorMovesOnFetch || c.fetch == internal.SyncFetchEnabled
		default:
			e.moved = true
		}
		out[name] = e
	}
	return out
}

// ---------------------------------------------------------------------------
// External mode
// ---------------------------------------------------------------------------

// cellFixture prepares a linear external fixture in which every edge has real
// work: the anchor's remote base is ahead of the local remote-tracking ref
// (visible only through a fetch), the anchor is ahead of its child, and that
// child is ahead of the leaf.
type cellFixture struct {
	*scopedFixture
	trackingBefore string // refs/remotes/origin/master before the run
	remoteHead     string // refs/heads/master on the bare remote
}

func newCellFixture(t *testing.T) *cellFixture {
	t.Helper()
	f := newScopedFixture(t)

	// Local work: root ahead of parent, parent ahead of child.
	writeAndCommit(t, f.wt("root"), "root-v2.txt", "root-v2\n", "root v2")
	writeAndCommit(t, f.wt("parent"), "parent-v2.txt", "parent-v2\n", "parent v2")

	// Remote work the local repository cannot see without fetching: push a new
	// master commit, then rewind the remote-tracking ref by hand.
	tracking := gitOutput(t, f.repo, "rev-parse", "refs/remotes/origin/master")
	writeAndCommit(t, f.repo, "upstream.txt", "upstream\n", "upstream")
	gitRun(t, f.repo, "push", "origin", "master")
	remoteHead := gitOutput(t, f.remote, "rev-parse", "refs/heads/master")
	gitRun(t, f.repo, "update-ref", "refs/remotes/origin/master", tracking)

	return &cellFixture{scopedFixture: f, trackingBefore: tracking, remoteHead: remoteHead}
}

func (f *cellFixture) shas(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, 3)
	for _, name := range []string{"root", "parent", "child"} {
		out[name] = gitOutput(t, f.repo, "rev-parse", "refs/heads/"+name)
	}
	return out
}

func (f *cellFixture) remoteTrackingRefs(t *testing.T) string {
	t.Helper()
	return gitOutput(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes")
}

func (f *cellFixture) contains(t *testing.T, branch, sha string) bool {
	t.Helper()
	return internal.RunSilentDir(f.repo, "git", "merge-base", "--is-ancestor", sha, branch) == nil
}

func TestSyncCells_ExternalTwelveCells(t *testing.T) {
	for _, cell := range syncTwelveCells() {
		t.Run(cell.name(), func(t *testing.T) {
			f := newCellFixture(t)
			before := f.shas(t)
			trackingBefore := f.remoteTrackingRefs(t)
			stackBefore := readFileString(t, filepath.Join(f.featurePath, "stack.yaml"))

			args := append([]string{f.feature}, cell.flags("parent")...)
			if cell.fetch == internal.SyncFetchDisabled {
				// AC 14: a `no-fetch` run needs zero network input, so the
				// remote is taken away for the whole run.
				offline := f.remote + ".offline"
				if err := os.Rename(f.remote, offline); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Rename(offline, f.remote) }()
			}
			stdout, stderr, exit := runSync(t, args...)
			if exit != 0 {
				t.Fatalf("cell %s must succeed: exit=%d\nstdout:\n%s\nstderr:\n%s", cell.name(), exit, stdout, stderr)
			}
			if !strings.Contains(stdout, cell.header("parent")) {
				t.Fatalf("missing header %q:\n%s", cell.header("parent"), stdout)
			}

			after := f.shas(t)
			expect := cell.expect("root", "parent", "child", true)
			var mutated []string
			for _, name := range []string{"root", "parent", "child"} {
				e := expect[name]
				if e.moved && after[name] == before[name] {
					t.Fatalf("%s must move in cell %s (still %s)", name, cell.name(), after[name])
				}
				if !e.moved && after[name] != before[name] {
					t.Fatalf("%s must not move in cell %s: %s -> %s", name, cell.name(), before[name], after[name])
				}
				switch {
				case e.selected && e.skipped:
					if !strings.Contains(stdout, syncAnchorNoOpLine(name)) {
						t.Fatalf("a local-only anchor must print the no-op line:\n%s", stdout)
					}
				case e.selected:
					if !strings.Contains(stdout, formatSyncStatus(name, "active", "synced")) {
						t.Fatalf("%s must be reported synced in cell %s:\n%s", name, cell.name(), stdout)
					}
				default:
					if strings.Contains(stdout, "] "+name+" (") {
						t.Fatalf("%s is outside the scope of cell %s but was reported:\n%s", name, cell.name(), stdout)
					}
				}
				if e.selected && !e.skipped {
					mutated = append(mutated, name)
				}
			}

			// Propagation invariants: a selected child ends up on its parent's
			// final tip, and the anchor advances onto the remote base only when
			// this cell says it may.
			if expect["parent"].selected && !f.contains(t, "parent", after["root"]) {
				t.Fatalf("parent must contain the root tip in cell %s", cell.name())
			}
			if expect["child"].selected && !f.contains(t, "child", after["parent"]) {
				t.Fatalf("child must contain the parent tip in cell %s", cell.name())
			}
			anchorAdvanced := f.contains(t, "root", f.remoteHead)
			wantAnchorAdvanced := expect["root"].selected && !expect["root"].skipped && cell.fetch == internal.SyncFetchEnabled
			if anchorAdvanced != wantAnchorAdvanced {
				t.Fatalf("root contains the remote head = %v, want %v in cell %s", anchorAdvanced, wantAnchorAdvanced, cell.name())
			}

			// Fetch axis: `no-fetch` issues no automatic network input at all.
			trackingAfter := f.remoteTrackingRefs(t)
			switch cell.fetch {
			case internal.SyncFetchDisabled:
				if trackingAfter != trackingBefore {
					t.Fatalf("no-fetch moved a remote-tracking ref in cell %s:\n--- before ---\n%s\n--- after ---\n%s", cell.name(), trackingBefore, trackingAfter)
				}
				if strings.Contains(stdout, "Fetching") {
					t.Fatalf("no-fetch must not fetch:\n%s", stdout)
				}
			default:
				if !strings.Contains(stdout, "Fetching ") {
					t.Fatalf("a fetch cell must fetch:\n%s", stdout)
				}
				got := gitOutput(t, f.repo, "rev-parse", "refs/remotes/origin/master")
				if got != f.remoteHead {
					t.Fatalf("origin/master = %s, want the pushed head %s", got, f.remoteHead)
				}
			}

			// Scope invariant: nothing outside the mutated set may change in
			// stack.yaml, and no run may leave state behind.
			stackAfter := readFileString(t, filepath.Join(f.featurePath, "stack.yaml"))
			assertUnselectedStackEntriesUnchanged(t, stackBefore, stackAfter, mutated...)
			f.stateFilesGone(t)
		})
	}
}

// ---------------------------------------------------------------------------
// Checkout mode
// ---------------------------------------------------------------------------

type checkoutCellFixture struct {
	dir            string
	featurePath    string
	remote         string
	trackingBefore string
	remoteHead     string
}

// newCheckoutCellFixture builds the checkout twin of newCellFixture: one
// repository, one bare remote, three stacked local branches, and real work on
// every edge.
func newCheckoutCellFixture(t *testing.T) *checkoutCellFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clearStepHook(t)
	dir := setupCheckoutSyncRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRunCS(t, dir, "init", "--bare", remote)
	gitRunCS(t, dir, "remote", "add", "origin", remote)
	gitRunCS(t, dir, "push", "-u", "origin", "main")
	featurePath := setupFeaturePath(t, dir)

	createStackBranch(t, dir, "feat-root", "main", "root.txt", "root\n")
	createStackBranch(t, dir, "feat-a", "feat-root", "a.txt", "a\n")
	createStackBranch(t, dir, "feat-b", "feat-a", "b.txt", "b\n")

	// Real work on every edge: feat-root ahead of feat-a, feat-a ahead of
	// feat-b, and main ahead of feat-root.
	gitRunCS(t, dir, "checkout", "feat-root")
	writeFileCS(t, dir, "root-v2.txt", "root-v2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "feat-root v2")
	gitRunCS(t, dir, "checkout", "feat-a")
	writeFileCS(t, dir, "a-v2.txt", "a-v2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "feat-a v2")
	gitRunCS(t, dir, "checkout", "main")
	writeFileCS(t, dir, "main2.txt", "main2\n")
	gitRunCS(t, dir, "add", ".")
	gitRunCS(t, dir, "commit", "-m", "main advance")

	// Remote work only a fetch can observe.
	tracking := gitSHA(t, dir, "refs/remotes/origin/main")
	gitRunCS(t, dir, "push", "origin", "main")
	remoteHead := gitSHA(t, dir, "refs/remotes/origin/main")
	gitRunCS(t, dir, "update-ref", "refs/remotes/origin/main", tracking)

	saveTestStack(t, featurePath, []internal.StackEntry{
		{Name: "feat-root", Base: "main"},
		{Name: "feat-a", Base: "feat-root"},
		{Name: "feat-b", Base: "feat-a"},
	})
	return &checkoutCellFixture{dir: dir, featurePath: featurePath, remote: remote, trackingBefore: tracking, remoteHead: remoteHead}
}

func (f *checkoutCellFixture) shas(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, 3)
	for _, name := range []string{"feat-root", "feat-a", "feat-b"} {
		out[name] = gitSHA(t, f.dir, "refs/heads/"+name)
	}
	return out
}

func (f *checkoutCellFixture) remoteTrackingRefs(t *testing.T) string {
	t.Helper()
	return gitRunCS(t, f.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes")
}

func (f *checkoutCellFixture) contains(t *testing.T, branch, sha string) bool {
	t.Helper()
	return internal.RunSilentDir(f.dir, "git", "merge-base", "--is-ancestor", sha, branch) == nil
}

func TestSyncCells_CheckoutTwelveCells(t *testing.T) {
	for _, cell := range syncTwelveCells() {
		t.Run(cell.name(), func(t *testing.T) {
			f := newCheckoutCellFixture(t)
			before := f.shas(t)
			trackingBefore := f.remoteTrackingRefs(t)

			opts := newModeOpts(f.dir, f.featurePath, cell.policy("feat-a"))
			if cell.fetch == internal.SyncFetchDisabled {
				offline := f.remote + ".offline"
				if err := os.Rename(f.remote, offline); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Rename(offline, f.remote) }()
			}
			out, err := captureRun(t, func() error { return internal.RunCheckoutSync(opts) })
			if err != nil {
				t.Fatalf("cell %s must succeed: %v\n%s", cell.name(), err, out)
			}
			if !strings.Contains(out, cell.header("feat-a")) {
				t.Fatalf("missing header %q:\n%s", cell.header("feat-a"), out)
			}

			after := f.shas(t)
			expect := cell.expect("feat-root", "feat-a", "feat-b", false)
			for _, name := range []string{"feat-root", "feat-a", "feat-b"} {
				e := expect[name]
				if e.moved && after[name] == before[name] {
					t.Fatalf("%s must move in cell %s (still %s)", name, cell.name(), after[name])
				}
				if !e.moved && after[name] != before[name] {
					t.Fatalf("%s must not move in cell %s: %s -> %s", name, cell.name(), before[name], after[name])
				}
				if e.selected && e.skipped && !strings.Contains(out, "  [-] "+name+" (no in-stack parent edge to propagate)") {
					t.Fatalf("a local-only anchor must print the no-op line:\n%s", out)
				}
			}

			if expect["feat-a"].selected && !f.contains(t, "feat-a", after["feat-root"]) {
				t.Fatalf("feat-a must contain the feat-root tip in cell %s", cell.name())
			}
			if expect["feat-b"].selected && !f.contains(t, "feat-b", after["feat-a"]) {
				t.Fatalf("feat-b must contain the feat-a tip in cell %s", cell.name())
			}
			mainSHA := gitSHA(t, f.dir, "refs/heads/main")
			anchorAdvanced := f.contains(t, "feat-root", mainSHA)
			wantAnchorAdvanced := expect["feat-root"].selected && !expect["feat-root"].skipped
			if anchorAdvanced != wantAnchorAdvanced {
				t.Fatalf("feat-root contains main = %v, want %v in cell %s", anchorAdvanced, wantAnchorAdvanced, cell.name())
			}

			trackingAfter := f.remoteTrackingRefs(t)
			switch cell.fetch {
			case internal.SyncFetchDisabled:
				if trackingAfter != trackingBefore {
					t.Fatalf("no-fetch moved a remote-tracking ref in cell %s:\n--- before ---\n%s\n--- after ---\n%s", cell.name(), trackingBefore, trackingAfter)
				}
				if strings.Contains(out, "Fetching") {
					t.Fatalf("no-fetch must not fetch:\n%s", out)
				}
			default:
				if !strings.Contains(out, "Fetching default repo... ") {
					t.Fatalf("a fetch cell must refresh remote-tracking refs:\n%s", out)
				}
				if got := gitSHA(t, f.dir, "refs/remotes/origin/main"); got != f.remoteHead {
					t.Fatalf("origin/main = %s, want the pushed head %s", got, f.remoteHead)
				}
			}

			// The transaction is the checkout half of "no run may leave state
			// behind", and the original branch is restored.
			if internal.HasCheckoutTransaction(f.featurePath) {
				t.Fatal("a completed transaction must be deleted")
			}
			if internal.HasCheckoutLock(f.featurePath) {
				t.Fatal("a completed run must release the lock")
			}
			if got := gitRunCS(t, f.dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
				t.Fatalf("HEAD = %s, want the original branch main", got)
			}
		})
	}
}
