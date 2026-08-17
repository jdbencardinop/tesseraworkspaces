package internal

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ResolveSyncSelection is pure over (Stack, SyncRunPolicy, SyncSelectionOpts):
// no Git, no filesystem, no path resolution. Every test below is a literal
// table with no t.TempDir and no repository.
// ---------------------------------------------------------------------------

func syncTestStack() Stack {
	return Stack{Branches: []StackEntry{
		{Name: "root", Base: "master"},
		{Name: "parent", Base: "root"},
		{Name: "child", Base: "parent"},
		{Name: "sibling", Base: "root"},
		{Name: "old", Base: "root", Archived: true},
		{Name: "other-repo", Base: "root", Repo: "/repos/other"},
	}}
}

func policyAll() SyncRunPolicy {
	return SyncRunPolicy{Fetch: SyncFetchEnabled, Propagation: SyncPropagationFull, ScopeKind: SyncScopeAll}
}

func policyOne(name string) SyncRunPolicy {
	p := policyAll()
	p.ScopeKind = SyncScopeOne
	p.Selector = name
	return p
}

func policySubtree(name string) SyncRunPolicy {
	p := policyAll()
	p.ScopeKind = SyncScopeSubtree
	p.Selector = name
	return p
}

func newModeOpts(mode WorkspaceMode) SyncSelectionOpts {
	return SyncSelectionOpts{Mode: mode, NewMode: true, Feature: "auth"}
}

func TestSyncSelection_ScopeMembership(t *testing.T) {
	stack := syncTestStack()
	cases := []struct {
		name   string
		policy SyncRunPolicy
		want   []string
	}{
		{"all", policyAll(), []string{"root", "parent", "child", "sibling", "old", "other-repo"}},
		{"one", policyOne("parent"), []string{"parent"}},
		{"subtree-includes-root", policySubtree("parent"), []string{"parent", "child"}},
		{"subtree-whole", policySubtree("root"), []string{"root", "parent", "child", "sibling", "old", "other-repo"}},
		{"one-leaf", policyOne("child"), []string{"child"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := ResolveSyncSelection(stack, tc.policy, newModeOpts(ModeExternal))
			if err != nil {
				t.Fatalf("ResolveSyncSelection: %v", err)
			}
			if len(sel.Entries) != len(tc.want) {
				t.Fatalf("selected %v, want %v", sel.SelectedNames(), tc.want)
			}
			for _, name := range tc.want {
				if !sel.Names[name] {
					t.Fatalf("expected %q in selection %v", name, sel.SelectedNames())
				}
			}
		})
	}
}

func TestSyncSelection_ParentBeforeChild(t *testing.T) {
	sel, err := ResolveSyncSelection(syncTestStack(), policyAll(), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	pos := make(map[string]int, len(sel.Entries))
	for i, e := range sel.Entries {
		pos[e.Name] = i
	}
	// Only parent-before-child is contracted; sibling order is unspecified.
	for _, edge := range [][2]string{{"root", "parent"}, {"parent", "child"}, {"root", "sibling"}} {
		if pos[edge[0]] >= pos[edge[1]] {
			t.Fatalf("%s must precede %s in %v", edge[0], edge[1], sel.SelectedNames())
		}
	}
}

func TestSyncSelection_Roles(t *testing.T) {
	sel, err := ResolveSyncSelection(syncTestStack(), policyAll(), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]SyncSelectionRole{
		"root":       SyncRoleAnchor,     // literal base
		"parent":     SyncRolePropagated, // same-repo stack parent
		"child":      SyncRolePropagated,
		"sibling":    SyncRolePropagated,
		"other-repo": SyncRoleAnchor, // stack parent in a different repo
	}
	for name, role := range want {
		if got := sel.Role(name); got != role {
			t.Fatalf("role(%q) = %q, want %q", name, got, role)
		}
	}
	for _, e := range sel.Entries {
		if e.Role == SyncRolePropagated && e.ParentName == "" {
			t.Fatalf("propagated entry %q carries no parent name", e.Name)
		}
		if e.Role == SyncRoleAnchor && e.ParentName != "" {
			t.Fatalf("anchor %q carries parent name %q", e.Name, e.ParentName)
		}
	}
}

func TestSyncSelection_AnchorOnlySelectionIsNoOp(t *testing.T) {
	sel, err := ResolveSyncSelection(syncTestStack(), policyOne("root"), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	if !sel.IsAnchorSelection() {
		t.Fatal("a root-only selection holds no propagation edge")
	}
	if len(sel.Anchors()) != 1 {
		t.Fatalf("expected one anchor, got %v", sel.Anchors())
	}

	sel, err = ResolveSyncSelection(syncTestStack(), policyOne("child"), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	if sel.IsAnchorSelection() {
		t.Fatal("a child-only selection holds a propagation edge")
	}
}

func TestSyncSelection_UnknownSelectorI10(t *testing.T) {
	_, err := ResolveSyncSelection(syncTestStack(), policyOne("nope"), newModeOpts(ModeExternal))
	if err == nil {
		t.Fatal("expected I10")
	}
	want := `unknown stack entry "nope" in feature "auth"; run: tws stack status auth`
	if err.Error() != want {
		t.Fatalf("I10 = %q, want %q", err.Error(), want)
	}
	if _, err := ResolveSyncSelection(syncTestStack(), policySubtree("nope"), newModeOpts(ModeCheckout)); err == nil || err.Error() != want {
		t.Fatalf("I10 must be identical for --from and in checkout mode; got %v", err)
	}
}

func TestSyncSelection_ArchivedSelectorI11(t *testing.T) {
	_, err := ResolveSyncSelection(syncTestStack(), policyOne("old"), newModeOpts(ModeExternal))
	if err == nil {
		t.Fatal("expected I11")
	}
	want := `stack entry "old" is archived; restore it with: tws new auth old`
	if err.Error() != want {
		t.Fatalf("I11 = %q, want %q", err.Error(), want)
	}
}

func TestSyncSelection_ArchivedClosureMemberIsNotRefused(t *testing.T) {
	// `old` is archived but is only a closure member, never the named selector.
	sel, err := ResolveSyncSelection(syncTestStack(), policySubtree("root"), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatalf("closure members must not be filtered on Archived: %v", err)
	}
	if !sel.Names["old"] {
		t.Fatal("archived closure member must stay in the selection")
	}
}

func TestSyncSelection_CrossRepoI12CheckoutOnly(t *testing.T) {
	_, err := ResolveSyncSelection(syncTestStack(), policyAll(), newModeOpts(ModeCheckout))
	if err == nil {
		t.Fatal("expected I12 in checkout mode")
	}
	want := `stack entry "other-repo" belongs to repository "/repos/other"; checkout sync is single-repository (cross-repo-unsupported)`
	if err.Error() != want {
		t.Fatalf("I12 = %q, want %q", err.Error(), want)
	}
	if _, err := ResolveSyncSelection(syncTestStack(), policyAll(), newModeOpts(ModeExternal)); err != nil {
		t.Fatalf("external mode never refuses a cross-repo entry: %v", err)
	}
}

func TestSyncSelection_DuplicateGitBranchI13(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "root", Base: "master"},
		{Name: "work", Base: "root", Branch: "user/work"},
		{Name: "copy", Base: "root", Branch: "user/work"},
	}}
	_, err := ResolveSyncSelection(stack, policyAll(), newModeOpts(ModeExternal))
	if err == nil {
		t.Fatal("expected I13")
	}
	if !strings.Contains(err.Error(), `share Git branch "user/work"; select one of them with --only`) {
		t.Fatalf("I13 = %q", err.Error())
	}
	// The frozen path never calls the resolver, so NewMode false suppresses it.
	if _, err := ResolveSyncSelection(stack, policyAll(), SyncSelectionOpts{Mode: ModeExternal, Feature: "auth"}); err != nil {
		t.Fatalf("I13 is a new-mode rule only: %v", err)
	}
	// Selecting one of the two is accepted.
	if _, err := ResolveSyncSelection(stack, policyOne("work"), newModeOpts(ModeExternal)); err != nil {
		t.Fatalf("--only must resolve the duplicate: %v", err)
	}
}

func TestSyncSelection_GitBranchIdentity(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "root", Base: "master"},
		{Name: "work", Base: "root", Branch: "user/work"},
	}}
	sel, err := ResolveSyncSelection(stack, policyOne("work"), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	if sel.Entries[0].GitBranch != "user/work" {
		t.Fatalf("GitBranch = %q, want user/work", sel.Entries[0].GitBranch)
	}
	if sel.Entries[0].Name != "work" {
		t.Fatalf("selector identity is the logical Name, got %q", sel.Entries[0].Name)
	}
}

func TestSyncSelection_ReposSortedWithDefaultFirst(t *testing.T) {
	stack := Stack{Branches: []StackEntry{
		{Name: "root", Base: "master"},
		{Name: "b", Base: "root", Repo: "/repos/zed"},
		{Name: "a", Base: "root", Repo: "/repos/alpha"},
	}}
	sel, err := ResolveSyncSelection(stack, policyAll(), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"", "/repos/alpha", "/repos/zed"}
	if len(sel.Repos) != len(want) {
		t.Fatalf("repos = %v, want %v", sel.Repos, want)
	}
	for i := range want {
		if sel.Repos[i] != want[i] {
			t.Fatalf("repos = %v, want %v", sel.Repos, want)
		}
	}
}

func TestSyncSelection_ScopeLabelAndScoped(t *testing.T) {
	cases := []struct {
		policy SyncRunPolicy
		label  string
		scoped bool
	}{
		{policyAll(), "all", false},
		{policyOne("parent"), "only:parent", true},
		{policySubtree("parent"), "subtree:parent", true},
	}
	for _, tc := range cases {
		if got := tc.policy.ScopeLabel(); got != tc.label {
			t.Fatalf("ScopeLabel = %q, want %q", got, tc.label)
		}
		if got := tc.policy.Scoped(); got != tc.scoped {
			t.Fatalf("Scoped(%s) = %v, want %v", tc.label, got, tc.scoped)
		}
	}
}

func TestSameStackRepo(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"/x", "/x", true},
		{"", "/x", false},
		{"/x", "", false},
		{"/x", "/y", false},
	}
	for _, tc := range cases {
		if got := SameStackRepo(tc.a, tc.b); got != tc.want {
			t.Fatalf("SameStackRepo(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSyncSelection_TopoSortErrorPropagates(t *testing.T) {
	cyclic := Stack{Branches: []StackEntry{
		{Name: "a", Base: "b"},
		{Name: "b", Base: "a"},
	}}
	if _, err := ResolveSyncSelection(cyclic, policyAll(), newModeOpts(ModeExternal)); err == nil {
		t.Fatal("a cyclic stack must propagate the TopoSort error verbatim")
	}
}

func TestSyncSelection_SelectedNamesMirrorEntries(t *testing.T) {
	sel, err := ResolveSyncSelection(syncTestStack(), policySubtree("parent"), newModeOpts(ModeExternal))
	if err != nil {
		t.Fatal(err)
	}
	names := sel.SelectedNames()
	if len(names) != len(sel.Entries) {
		t.Fatalf("SelectedNames %v does not mirror Entries", names)
	}
	for i := range names {
		if names[i] != sel.Entries[i].Name {
			t.Fatalf("SelectedNames order diverges from Entries at %d", i)
		}
	}
	if sel.Role("not-selected") != "" {
		t.Fatal("Role of an unselected name must be the zero value")
	}
}
