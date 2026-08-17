package internal

import (
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// sync-modes — the shared selector and plan model (spec §5).
//
// One model, one resolution function, shared by both workspace modes. There is
// no second topological sort, no second descendant walker, no second parent
// lookup, and no second ancestry rule.
// ---------------------------------------------------------------------------

// SyncFetchPolicy is axis F.
type SyncFetchPolicy string

const (
	SyncFetchEnabled  SyncFetchPolicy = "fetch"
	SyncFetchDisabled SyncFetchPolicy = "no-fetch"
)

// SyncPropagationPolicy is axis P.
type SyncPropagationPolicy string

const (
	SyncPropagationFull      SyncPropagationPolicy = "full"
	SyncPropagationLocalOnly SyncPropagationPolicy = "local-only"
)

// SyncScopeKind is axis S.
type SyncScopeKind string

const (
	SyncScopeAll     SyncScopeKind = "all"
	SyncScopeOne     SyncScopeKind = "one"
	SyncScopeSubtree SyncScopeKind = "subtree"
)

// SyncRunPolicy is the frozen decision of one run. It is persisted verbatim.
type SyncRunPolicy struct {
	Fetch       SyncFetchPolicy       `yaml:"fetch_policy"`
	Propagation SyncPropagationPolicy `yaml:"propagation_policy"`
	ScopeKind   SyncScopeKind         `yaml:"scope_kind"`
	Selector    string                `yaml:"scope_selector,omitempty"` // logical StackEntry.Name
}

// SyncSelectionRole classifies one selected entry.
type SyncSelectionRole string

const (
	// SyncRoleAnchor: base is a literal ref, empty, or a stack entry in a
	// different repo. Never rebased under local-only.
	SyncRoleAnchor SyncSelectionRole = "anchor"
	// SyncRolePropagated: base names another stack entry in the same repo.
	SyncRolePropagated SyncSelectionRole = "propagated"
)

// SyncSelectedEntry is one resolved member of the selection, in the order
// TopoSort returned (parent before child; sibling order unspecified, §3.7).
//
// It deliberately carries no LastBaseSHA: no executor may reconstruct
// StackEntry values from the selection, because that would silently drop
// LastBaseSHA and destroy the amend-aware `--onto <base> <LastBaseSHA>` replay.
type SyncSelectedEntry struct {
	Name       string
	GitBranch  string
	Repo       string
	Base       string
	Role       SyncSelectionRole
	ParentName string
	Archived   bool
}

// SyncSelection is the whole resolved plan-independent selection.
type SyncSelection struct {
	Policy  SyncRunPolicy
	Entries []SyncSelectedEntry
	Names   map[string]bool
	Repos   []string
}

// SyncSelectionOpts carries everything the validator needs that the policy does
// not. It is deliberately tiny and value-only: resolution stays pure.
type SyncSelectionOpts struct {
	Mode    WorkspaceMode // ModeExternal or ModeCheckout; selects the checkout-only rules
	NewMode bool          // any trigger flag was Changed; selects the new-mode-only rules
	Feature string        // resolved feature name, interpolated into the I10/I11 messages
}

// SameStackRepo is the single cross-repo predicate. An empty string names the
// default repository.
func SameStackRepo(a, b string) bool {
	if a == "" && b == "" {
		return true
	}
	return a == b
}

// IsAnchorSelection reports whether the selection holds no propagation edge at
// all, which is the local-only no-op condition (D3).
func (s SyncSelection) IsAnchorSelection() bool {
	for _, e := range s.Entries {
		if e.Role == SyncRolePropagated {
			return false
		}
	}
	return true
}

// Anchors returns the selected anchors in selection order.
func (s SyncSelection) Anchors() []SyncSelectedEntry {
	var out []SyncSelectedEntry
	for _, e := range s.Entries {
		if e.Role == SyncRoleAnchor {
			out = append(out, e)
		}
	}
	return out
}

// Role reports the role of one selected entry. Unselected names report the
// zero value, which no executor may treat as a role.
func (s SyncSelection) Role(name string) SyncSelectionRole {
	for _, e := range s.Entries {
		if e.Name == name {
			return e.Role
		}
	}
	return ""
}

// SelectedNames returns the selection's logical names in selection order. It is
// what the v2 payload persists as `selected`.
func (s SyncSelection) SelectedNames() []string {
	out := make([]string, 0, len(s.Entries))
	for _, e := range s.Entries {
		out = append(out, e.Name)
	}
	return out
}

// Scoped reports whether the run restricts the selection below the whole stack.
func (p SyncRunPolicy) Scoped() bool { return p.ScopeKind != SyncScopeAll }

// ScopeLabel renders the scope for the §3.7 header.
func (p SyncRunPolicy) ScopeLabel() string {
	switch p.ScopeKind {
	case SyncScopeOne:
		return "only:" + p.Selector
	case SyncScopeSubtree:
		return "subtree:" + p.Selector
	default:
		return string(SyncScopeAll)
	}
}

// ResolveSyncSelection is the one resolution function. It owns I10, I11, I12,
// and I13 — every selection-validity rule — and no caller re-implements,
// re-checks, or supplements them.
//
// It performs no Git command, no filesystem access, no path resolution, and no
// I/O: opts.Feature is consumed as a message value only.
func ResolveSyncSelection(stack Stack, policy SyncRunPolicy, opts SyncSelectionOpts) (SyncSelection, error) {
	sorted, err := TopoSort(stack)
	if err != nil {
		return SyncSelection{}, err
	}

	members := make(map[string]bool, len(sorted))
	switch policy.ScopeKind {
	case SyncScopeAll:
		for _, e := range sorted {
			members[e.Name] = true
		}
	case SyncScopeOne:
		if !HasBranch(stack, policy.Selector) {
			return SyncSelection{}, unknownStackEntryError(policy.Selector, opts.Feature)
		}
		members[policy.Selector] = true
	case SyncScopeSubtree:
		if !HasBranch(stack, policy.Selector) {
			return SyncSelection{}, unknownStackEntryError(policy.Selector, opts.Feature)
		}
		members[policy.Selector] = true
		for name := range Descendants(stack, policy.Selector) {
			members[name] = true
		}
	default:
		return SyncSelection{}, fmt.Errorf("unknown sync scope %q", policy.ScopeKind)
	}

	if policy.ScopeKind != SyncScopeAll {
		if GetBranch(stack, policy.Selector).Archived {
			return SyncSelection{}, archivedStackEntryError(policy.Selector, opts.Feature)
		}
	}

	sel := SyncSelection{Policy: policy, Names: make(map[string]bool, len(members))}
	repoSet := make(map[string]bool)
	for _, entry := range sorted {
		if !members[entry.Name] {
			continue
		}
		selected := SyncSelectedEntry{
			Name:      entry.Name,
			GitBranch: entry.GitBranch(),
			Repo:      entry.Repo,
			Base:      entry.Base,
			Archived:  entry.Archived,
			Role:      SyncRoleAnchor,
		}
		if parent := GetBranch(stack, entry.Base); parent.Name != "" && SameStackRepo(parent.Repo, entry.Repo) {
			selected.Role = SyncRolePropagated
			selected.ParentName = parent.Name
		}
		sel.Entries = append(sel.Entries, selected)
		sel.Names[entry.Name] = true
		repoSet[entry.Repo] = true
	}

	if opts.Mode == ModeCheckout {
		for _, e := range sel.Entries {
			if !SameStackRepo(e.Repo, "") {
				return SyncSelection{}, fmt.Errorf("stack entry %q belongs to repository %q; checkout sync is single-repository (cross-repo-unsupported)", e.Name, e.Repo)
			}
		}
	}

	if opts.NewMode {
		seen := make(map[string]string, len(sel.Entries))
		for _, e := range sel.Entries {
			if first, ok := seen[e.GitBranch]; ok {
				return SyncSelection{}, fmt.Errorf("stack entries %q and %q share Git branch %q; select one of them with --only", first, e.Name, e.GitBranch)
			}
			seen[e.GitBranch] = e.Name
		}
	}

	for repo := range repoSet {
		sel.Repos = append(sel.Repos, repo)
	}
	sort.Strings(sel.Repos)
	return sel, nil
}

// unknownStackEntryError is the single I10 format string.
func unknownStackEntryError(selector, feature string) error {
	return fmt.Errorf("unknown stack entry %q in feature %q; run: tws stack status %s", selector, feature, feature)
}

// archivedStackEntryError is the single I11 format string.
func archivedStackEntryError(selector, feature string) error {
	return fmt.Errorf("stack entry %q is archived; restore it with: tws new %s %s", selector, feature, selector)
}
