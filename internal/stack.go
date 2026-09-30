package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type StackEntry struct {
	Name        string `yaml:"name"`
	Branch      string `yaml:"branch,omitempty"`   // git branch name (defaults to Name if empty)
	Archived    bool   `yaml:"archived,omitempty"` // true when branch is archived (metadata only)
	Base        string `yaml:"base"`
	Repo        string `yaml:"repo,omitempty"`          // source repo path (empty = default/current repo)
	LastBaseSHA string `yaml:"last_base_sha,omitempty"` // SHA of base branch at last sync
}

// GitBranch returns the actual git branch name for this entry.
func (e StackEntry) GitBranch() string {
	if e.Branch != "" {
		return e.Branch
	}
	return e.Name
}

type Stack struct {
	Branches []StackEntry `yaml:"branches"`
}

// UniqueRepos returns a set of unique repo paths referenced in the stack.
// Empty string represents the default repo. Returns worktree paths for
// each repo (to use as git context) alongside the repo path.
func UniqueRepos(s Stack, featurePath string) map[string]string {
	repos := make(map[string]string)
	for _, e := range s.Branches {
		repo := e.Repo
		if _, ok := repos[repo]; !ok {
			// Find an active worktree from this repo to use as git context
			wtPath := filepath.Join(featurePath, "worktrees", e.Name)
			if _, err := os.Stat(wtPath); err == nil {
				repos[repo] = wtPath
			} else {
				repos[repo] = "" // no active worktree, will need fallback
			}
		}
	}
	return repos
}

// HasBranch checks if a branch name already exists in the stack.
func HasBranch(s Stack, name string) bool {
	for _, e := range s.Branches {
		if e.Name == name {
			return true
		}
	}
	return false
}

// GetBranch returns the StackEntry for a branch name. Returns empty entry if not found.
func GetBranch(s Stack, name string) StackEntry {
	for _, e := range s.Branches {
		if e.Name == name {
			return e
		}
	}
	return StackEntry{}
}

// UpdateBaseSHA updates the last_base_sha for a branch in the stack.
func UpdateBaseSHA(s *Stack, branchName, sha string) {
	for i := range s.Branches {
		if s.Branches[i].Name == branchName {
			s.Branches[i].LastBaseSHA = sha
			return
		}
	}
}

// GetBranchSHA returns the current HEAD SHA of a branch using git.
func GetBranchSHA(gitContext, branch string) string {
	args := []string{"rev-parse", branch}
	if gitContext != "" {
		args = []string{"-C", gitContext, "rev-parse", branch}
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RenameBranch renames a branch in the stack, updating both Name and Base references.
func RenameBranch(s *Stack, oldName, newName string) bool {
	found := false
	for i := range s.Branches {
		if s.Branches[i].Name == oldName {
			s.Branches[i].Name = newName
			found = true
		}
		if s.Branches[i].Base == oldName {
			s.Branches[i].Base = newName
		}
	}
	return found
}

func StackPath(featurePath string) string {
	return filepath.Join(featurePath, "stack.yaml")
}

func LoadStack(featurePath string) (Stack, error) {
	data, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		return Stack{}, err
	}
	var s Stack
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Stack{}, err
	}
	return s, nil
}

func SaveStack(featurePath string, s Stack) error {
	data, err := yaml.Marshal(&s)
	if err != nil {
		return err
	}
	return os.WriteFile(StackPath(featurePath), data, 0644)
}

// ============================================================================
// Byte-exact durable stack writers (safe-reparent §10.3, §10.3a)
//
// SaveStack above is deliberately untouched: it is not atomic, not durable,
// and every one of its callers keeps it. The three functions below are the
// reparent boundary's only metadata writers.
// ============================================================================

// WriteStackBytesAtomic writes exactly these bytes to the feature's
// stack.yaml, atomically and durably. It is the primitive both the forward
// metadata write and the abort restore use, because a pre-image restored by
// re-marshalling a decoded struct is not a restore: YAML round-tripping can
// reorder keys, drop comments and change quoting, so only the captured bytes
// are ever written back.
func WriteStackBytesAtomic(featurePath string, data []byte) error {
	return durableWriteFileFault(SyncIOWriteStack, StackPath(featurePath), data, 0644)
}

// SaveStackAtomic marshals s exactly as SaveStack does and delegates to
// WriteStackBytesAtomic, so a caller that holds a struct rather than bytes
// still gets the atomic, durable writer.
func SaveStackAtomic(featurePath string, s Stack) error {
	data, err := yaml.Marshal(&s)
	if err != nil {
		return err
	}
	return WriteStackBytesAtomic(featurePath, data)
}

// durableWriteFile writes data to path through a same-directory temp file
// that is fsynced, closed and renamed over the destination, after which the
// PARENT DIRECTORY is opened and fsynced too. The shipped atomicWriteFile
// (internal/checkout_sync.go) fsyncs the temp file but never the directory,
// so the renamed directory entry it produces is outside a machine-crash
// durability claim; this helper is added beside it, never inside it, and
// atomicWriteFile keeps every one of its callers.
//
// Every artifact whose loss would strip a reparent run of its only record of
// what it did is written through this helper: stack.yaml (via
// WriteStackBytesAtomic), the reparent state artifact, the compatibility
// artifacts, and the remote follow-up record.
func durableWriteFile(path string, data []byte, mode os.FileMode) error {
	return durableWriteFileFault("", path, data, mode)
}

// durableWriteFileFault is durableWriteFile with the §10.3b fault token
// bound. It consults syncIOFault(op, path) at exactly two points — before
// the rename, and after the rename but before the parent-directory fsync —
// so a test can assert that the previous file survives the first window and
// that a run stays recoverable after the second. op == "" disables the seam
// entirely, which is what the tokenless durableWriteFile passes.
func durableWriteFileFault(op, path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tws-durable-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if op != "" {
		// Window 1: the destination still holds its previous bytes.
		if err := syncIOFault(op, path); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if op != "" {
		// Window 2: the rename landed but the directory entry is not yet
		// durable. The caller MUST treat this as "may or may not have been
		// written", never as "not written".
		if err := syncIOFault(op, path); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// syncDir opens dir read-only and fsyncs it, which is what makes a completed
// rename durable across a machine crash. A directory that cannot be opened
// or synced is reported, never ignored.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

var reparentRecoverySyncDir = syncDir

func confirmReparentRenameDurable(path string) error {
	return reparentRecoverySyncDir(filepath.Dir(path))
}

// TopoSort returns branches in dependency order (parents before children).
// Returns an error if the graph contains a cycle.
func TopoSort(s Stack) ([]StackEntry, error) {
	// Build adjacency: base → children
	entryMap := make(map[string]StackEntry)
	children := make(map[string][]string)
	inDegree := make(map[string]int)

	for _, e := range s.Branches {
		entryMap[e.Name] = e
		inDegree[e.Name] = 0
	}

	for _, e := range s.Branches {
		// Only count edges where the base is also a tracked branch
		if _, ok := entryMap[e.Base]; ok {
			children[e.Base] = append(children[e.Base], e.Name)
			inDegree[e.Name]++
		}
	}

	// Kahn's algorithm
	var queue []string
	for _, entry := range s.Branches {
		if inDegree[entry.Name] == 0 {
			queue = append(queue, entry.Name)
		}
	}

	var sorted []StackEntry
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, entryMap[name])
		for _, child := range children[name] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if len(sorted) != len(s.Branches) {
		return nil, fmt.Errorf("cycle detected in stack.yaml")
	}
	return sorted, nil
}

// ReparentClosureOrder returns the target and its transitive logical
// descendants in the stable order safe-reparent replays them (§6.1a).
//
// It is deliberately NOT TopoSort, is never called by it, and never modifies
// it. Two differences are load-bearing:
//
//  1. TopoSort orders the whole graph with stack.yaml declaration order as
//     its sibling tie-break. This function applies that same stable,
//     operator-visible ordering only inside the target's reachable closure.
//  2. An edge exists only where child.Base == parent.Name AND the two entries
//     share a repository (SameStackRepo). TopoSort ignores Repo entirely, so
//     an unrelated stack in another repository whose Base merely spells the
//     same name would be pulled in; here it is not a child at all.
//
// The function is pure: it reads only stack, issues no Git command and
// touches no filesystem. An unknown target returns an error, as does a cycle
// among the reachable vertices — callers surface the latter as
// stack-unsortable (current stack) or destination-cycle (post-image graph).
// The target is always element 0.
func ReparentClosureOrder(stack Stack, target string) ([]StackEntry, error) {
	return reparentClosureOrder(stack, target, func(child, parent StackEntry) bool {
		return SameStackRepo(child.Repo, parent.Repo)
	})
}

// ReparentClosureOrderByRepoIdentity is the production reparent graph. The
// identity map is keyed by entry name and contains canonical Git common-dir
// identities measured before graph construction. Missing identities fall back
// to the raw comparison so an unreadable same-token row remains in scope and
// can surface its repo-unavailable blocker instead of disappearing.
func ReparentClosureOrderByRepoIdentity(stack Stack, target string, identities map[string]string) ([]StackEntry, error) {
	return reparentClosureOrder(stack, target, func(child, parent StackEntry) bool {
		if SameStackRepo(child.Repo, parent.Repo) {
			return true
		}
		childID, childOK := identities[child.Name]
		parentID, parentOK := identities[parent.Name]
		if childOK && parentOK && childID != "" && parentID != "" {
			return childID == parentID
		}
		return false
	})
}

func reparentClosureOrder(stack Stack, target string, sameRepo func(StackEntry, StackEntry) bool) ([]StackEntry, error) {
	index := make(map[string]int, len(stack.Branches))
	for i, e := range stack.Branches {
		if _, dup := index[e.Name]; dup {
			return nil, fmt.Errorf("duplicate stack entry %q", e.Name)
		}
		index[e.Name] = i
	}
	if _, ok := index[target]; !ok {
		return nil, fmt.Errorf("entry %q is not in stack.yaml", target)
	}

	children := make(map[string][]string, len(stack.Branches))
	for _, child := range stack.Branches {
		parentIdx, ok := index[child.Base]
		if !ok {
			continue
		}
		parent := stack.Branches[parentIdx]
		if !sameRepo(child, parent) {
			continue
		}
		children[parent.Name] = append(children[parent.Name], child.Name)
	}

	// Vertex set: the target plus everything reachable from it through those
	// edges. The closure is computed first so in-degrees count only edges
	// inside it — an ancestor of the target is not part of the replay.
	member := map[string]bool{target: true}
	stackQueue := []string{target}
	for len(stackQueue) > 0 {
		name := stackQueue[len(stackQueue)-1]
		stackQueue = stackQueue[:len(stackQueue)-1]
		for _, child := range children[name] {
			if member[child] {
				continue
			}
			member[child] = true
			stackQueue = append(stackQueue, child)
		}
	}

	inDegree := make(map[string]int, len(member))
	for name := range member {
		inDegree[name] = 0
	}
	for name := range member {
		for _, child := range children[name] {
			if member[child] {
				inDegree[child]++
			}
		}
	}

	ready := &stackIndexHeap{}
	for name := range member {
		if inDegree[name] == 0 {
			ready.push(index[name])
		}
	}

	ordered := make([]StackEntry, 0, len(member))
	for ready.len() > 0 {
		idx := ready.pop()
		entry := stack.Branches[idx]
		ordered = append(ordered, entry)
		for _, child := range children[entry.Name] {
			if !member[child] {
				continue
			}
			inDegree[child]--
			if inDegree[child] == 0 {
				ready.push(index[child])
			}
		}
	}

	if len(ordered) != len(member) {
		return nil, fmt.Errorf("cycle detected in stack.yaml")
	}
	if ordered[0].Name != target {
		// Unreachable while the closure is rooted at the target, which has
		// in-degree zero inside it; guarded so a future edge rule change can
		// never silently demote the target from row 0.
		return nil, fmt.Errorf("closure order did not start at %q", target)
	}
	return ordered, nil
}

// stackIndexHeap is ReparentClosureOrder's ready set: a min-heap of
// Stack.Branches indices, so the smallest declaration index is always emitted
// first. It is a local, dependency-free binary heap rather than container/heap
// so the ordering rule reads in one place.
type stackIndexHeap struct {
	items []int
}

func (h *stackIndexHeap) len() int { return len(h.items) }

func (h *stackIndexHeap) push(v int) {
	h.items = append(h.items, v)
	i := len(h.items) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[parent] <= h.items[i] {
			break
		}
		h.items[parent], h.items[i] = h.items[i], h.items[parent]
		i = parent
	}
}

func (h *stackIndexHeap) pop() int {
	top := h.items[0]
	last := len(h.items) - 1
	h.items[0] = h.items[last]
	h.items = h.items[:last]
	i := 0
	for {
		left, right := 2*i+1, 2*i+2
		smallest := i
		if left < len(h.items) && h.items[left] < h.items[smallest] {
			smallest = left
		}
		if right < len(h.items) && h.items[right] < h.items[smallest] {
			smallest = right
		}
		if smallest == i {
			break
		}
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
	return top
}

// Descendants returns all transitive children of the given branch.
func Descendants(s Stack, branch string) map[string]bool {
	children := make(map[string][]string)
	for _, e := range s.Branches {
		children[e.Base] = append(children[e.Base], e.Name)
	}

	result := make(map[string]bool)
	queue := []string{branch}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, child := range children[name] {
			if !result[child] {
				result[child] = true
				queue = append(queue, child)
			}
		}
	}
	return result
}

// PrintTree prints the stack as an indented dependency tree.
func PrintTree(s Stack) {
	children := make(map[string][]string)
	roots := make(map[string]bool)

	for _, e := range s.Branches {
		roots[e.Name] = true
	}
	for _, e := range s.Branches {
		children[e.Base] = append(children[e.Base], e.Name)
		// If base is a tracked branch, this entry is not a root
		for _, other := range s.Branches {
			if other.Name == e.Base {
				delete(roots, e.Name)
				break
			}
		}
	}

	var printNode func(name, prefix string, isLast bool)
	printNode = func(name, prefix string, isLast bool) {
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		fmt.Println(prefix + connector + name)

		childPrefix := prefix
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}

		kids := children[name]
		for i, child := range kids {
			printNode(child, childPrefix, i == len(kids)-1)
		}
	}

	// Print from each root's base (usually "main")
	bases := make(map[string]bool)
	for _, e := range s.Branches {
		if roots[e.Name] {
			bases[e.Base] = true
		}
	}

	for base := range bases {
		fmt.Printf("(%s)\n", base)
		kids := children[base]
		for i, child := range kids {
			printNode(child, "", i == len(kids)-1)
		}
	}

	if len(s.Branches) == 0 {
		fmt.Println("No branches tracked. Use 'ts new <feature> <branch>' to add branches.")
	}
}

// FormatBranchStatus formats a branch sync result for display.
func FormatBranchStatus(name, status string) string {
	symbols := map[string]string{
		"synced":  "+",
		"failed":  "x",
		"skipped": "-",
	}
	sym := symbols[status]
	if sym == "" {
		sym = "?"
	}
	return fmt.Sprintf("  [%s] %s", sym, name)
}

// DescendantsList returns Descendants as a sorted display string.
func DescendantsList(s Stack, branch string) string {
	descs := Descendants(s, branch)
	var names []string
	for name := range descs {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
