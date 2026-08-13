package internal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------- Constants ----------

// stackStatusSchema versions the stack status document. It is this document's
// own version and is independent of agentStatusSchema. Adding a key or an enum
// value is additive; removing a key, changing a type, or narrowing an enum
// bumps it.
const stackStatusSchema = 1

// stackStatusSanitizeLimit bounds dynamic values echoed into human output.
// Paths, branch names, and upstream displays are routinely longer than a ref
// name, so the ancestry evaluator's 40-rune metadata limit is too small here.
const stackStatusSanitizeLimit = 120

// StackStatusOpNone is the only value that means "the Git directory was
// resolved, verified, and carries no operation marker". A probe failure is
// never reported as this value.
const StackStatusOpNone = "none"

// Materialization kinds.
const (
	StackStatusKindWorktree = "worktree"
	StackStatusKindRef      = "ref"
)

// Materialization states.
const (
	StackStatusMaterializedPresent         = "present"
	StackStatusMaterializedArchived        = "archived"
	StackStatusMaterializedMissing         = "missing"
	StackStatusMaterializedPrunableMissing = "prunable-missing"
	StackStatusMaterializedCrossRepo       = "cross-repo-unsupported"
	StackStatusMaterializedUnknown         = "unknown"
)

// Upstream states.
const (
	StackStatusUpstreamNone     = "none"
	StackStatusUpstreamEqual    = "equal"
	StackStatusUpstreamAhead    = "ahead"
	StackStatusUpstreamBehind   = "behind"
	StackStatusUpstreamDiverged = "diverged"
	StackStatusUpstreamGone     = "gone"
)

// stackStatusRefFormat is the branch-ref inventory format. Fields are
// NUL-separated so no printable delimiter can appear inside a value, and only
// full refs are requested so a same-named tag can never win a lookup.
const stackStatusRefFormat = "%(refname)%00%(objectname)%00%(upstream)%00%(upstream:track)%00%(upstream:trackshort)"

const stackStatusHeadsPrefix = "refs/heads/"

// stackStatusObjectID matches an inventory object ID. It is object-format
// neutral on purpose: a SHA-256 repository prints 64 hex characters where a
// SHA-1 repository prints 40, and neither inventory may invalidate merely
// because the repository's object format is not SHA-1. It is parser validation
// only — it gates no identity claim, comparison, truncation, or derivation.
var stackStatusObjectID = regexp.MustCompile(`^[0-9a-f]+$`)

// stackStatusDecimal matches a non-negative decimal integer with no sign and
// no surrounding whitespace.
var stackStatusDecimal = regexp.MustCompile(`^[0-9]+$`)

// ---------- Report types ----------
//
// No field below is ever omitted when empty: every key is present in every
// document, absent scalars are null, and lists are never null.

// StackStatusReport is one versioned stack status document.
type StackStatusReport struct {
	SchemaVersion int                  `json:"schema_version"`
	Workspace     StackStatusWorkspace `json:"workspace"`
	Feature       string               `json:"feature"`
	Entries       []StackStatusEntry   `json:"entries"`
	Summary       StackStatusSummary   `json:"summary"`
}

// StackStatusWorkspace describes the resolved workspace. Exactly one of
// External and Checkout is non-nil, selected by the workspace mode.
type StackStatusWorkspace struct {
	Mode         WorkspaceMode         `json:"mode"`
	StableID     *string               `json:"stable_id"`
	MetadataRoot string                `json:"metadata_root"`
	Repository   StackStatusRepository `json:"repository"`
	External     *StackStatusExternal  `json:"external"`
	Checkout     *StackStatusCheckout  `json:"checkout"`
}

// StackStatusRepository is the single repository resolution the shared
// ancestry evaluator already performed. It is never rediscovered here.
type StackStatusRepository struct {
	Dir       *string         `json:"dir"`
	Source    StackRepoSource `json:"source"`
	Alternate *string         `json:"alternate"`
}

// StackStatusExternal carries only the worktrees root: external mode has many
// linked worktrees and no single current checkout.
type StackStatusExternal struct {
	WorktreesRoot string `json:"worktrees_root"`
}

// StackStatusCheckout describes the single physical checkout of a checkout
// workspace. Every fact that could not be established locally is null.
type StackStatusCheckout struct {
	Path        *string `json:"path"`
	Branch      *string `json:"branch"`
	Detached    *bool   `json:"detached"`
	Dirty       *bool   `json:"dirty"`
	ActiveGitOp *string `json:"active_git_op"`
}

// StackStatusEntry is one row, one per stack.Branches element in slice order.
type StackStatusEntry struct {
	Name              string                     `json:"name"`
	GitBranch         string                     `json:"git_branch"`
	Archived          bool                       `json:"archived"`
	Repo              *string                    `json:"repo"`
	Base              StackStatusBase            `json:"base"`
	RefExists         *bool                      `json:"ref_exists"`
	Heads             StackStatusHeads           `json:"heads"`
	BaseRecord        StackStatusBaseRecord      `json:"base_record"`
	Ancestry          StackStatusAncestry        `json:"ancestry"`
	RepoSource        StackRepoSource            `json:"repo_source"`
	ParentCounts      StackStatusParentCounts    `json:"parent_counts"`
	Materialization   StackStatusMaterialization `json:"materialization"`
	IsCurrentCheckout *bool                      `json:"is_current_checkout"`
	Upstream          StackStatusUpstream        `json:"upstream"`
}

// StackStatusBase projects the evaluator's base selection.
type StackStatusBase struct {
	Name *string       `json:"name"`
	Kind StackBaseKind `json:"kind"`
	Ref  *string       `json:"ref"`
}

// StackStatusHeads projects the evaluator's peeled heads.
type StackStatusHeads struct {
	Local          *string `json:"local"`
	LocalShort     *string `json:"local_short"`
	Parent         *string `json:"parent"`
	ParentShort    *string `json:"parent_short"`
	MergeBase      *string `json:"merge_base"`
	MergeBaseShort *string `json:"merge_base_short"`
}

// StackStatusBaseRecord projects the recorded last_base_sha verdict. State is
// null when the record was never consulted: a verdict never formed is never
// published.
type StackStatusBaseRecord struct {
	SHA    *string `json:"sha"`
	Commit *string `json:"commit"`
	Short  *string `json:"short"`
	State  *string `json:"state"`
}

// StackStatusNote projects one informational evaluator note.
type StackStatusNote struct {
	Kind   StackNoteKind `json:"kind"`
	Detail string        `json:"detail"`
}

// StackStatusAncestry projects the shared evaluator's verdict verbatim. This
// command computes no ancestry of its own.
type StackStatusAncestry struct {
	Status   *string             `json:"status"`
	Reason   StackAncestryReason `json:"reason"`
	Severity CheckoutSeverity    `json:"severity"`
	Guidance *string             `json:"guidance"`
	Notes    []StackStatusNote   `json:"notes"`
}

// StackStatusParentCounts holds local commit counts against the parent head.
// It is never a remote-freshness claim.
type StackStatusParentCounts struct {
	Ahead  *int `json:"ahead"`
	Behind *int `json:"behind"`
}

// StackStatusMaterialization describes whether and how the row exists on disk.
type StackStatusMaterialization struct {
	Kind             string  `json:"kind"`
	State            string  `json:"state"`
	Path             *string `json:"path"`
	CheckedOutBranch *string `json:"checked_out_branch"`
	Detached         *bool   `json:"detached"`
	Dirty            *bool   `json:"dirty"`
	ActiveGitOp      *string `json:"active_git_op"`
}

// StackStatusUpstream describes the configured upstream ref exactly as it
// exists in this repository right now.
type StackStatusUpstream struct {
	Configured *bool   `json:"configured"`
	Ref        *string `json:"ref"`
	Display    *string `json:"display"`
	State      *string `json:"state"`
	Ahead      *int    `json:"ahead"`
	Behind     *int    `json:"behind"`
	LocalOnly  bool    `json:"local_only"`
}

// StackStatusSummary counts the finished rows. Every counter is a plain int,
// always present, and its encoded order is a fixed struct field order, so no
// map is ever iterated.
type StackStatusSummary struct {
	Entries         int                              `json:"entries"`
	Ancestry        StackStatusAncestryCounts        `json:"ancestry"`
	Materialization StackStatusMaterializationCounts `json:"materialization"`
	Upstream        StackStatusUpstreamCounts        `json:"upstream"`
	Unknown         StackStatusUnknownCounts         `json:"unknown"`
	LocalOnly       bool                             `json:"local_only"`
}

// StackStatusAncestryCounts partitions entries by ancestry status.
type StackStatusAncestryCounts struct {
	Current              int `json:"current"`
	Stale                int `json:"stale"`
	Divergent            int `json:"divergent"`
	Missing              int `json:"missing"`
	CrossRepoUnsupported int `json:"cross_repo_unsupported"`
	Unevaluated          int `json:"unevaluated"`
}

// StackStatusMaterializationCounts partitions entries by materialization.
type StackStatusMaterializationCounts struct {
	Present              int `json:"present"`
	Archived             int `json:"archived"`
	Missing              int `json:"missing"`
	PrunableMissing      int `json:"prunable_missing"`
	CrossRepoUnsupported int `json:"cross_repo_unsupported"`
	Unknown              int `json:"unknown"`
}

// StackStatusUpstreamCounts partitions entries by upstream state.
type StackStatusUpstreamCounts struct {
	None     int `json:"none"`
	Equal    int `json:"equal"`
	Ahead    int `json:"ahead"`
	Behind   int `json:"behind"`
	Diverged int `json:"diverged"`
	Gone     int `json:"gone"`
	Unknown  int `json:"unknown"`
}

// StackStatusUnknownCounts counts rows whose corresponding value is null. It
// partitions nothing.
type StackStatusUnknownCounts struct {
	RefExists    int `json:"ref_exists"`
	ParentCounts int `json:"parent_counts"`
	Dirty        int `json:"dirty"`
	ActiveGitOp  int `json:"active_git_op"`
}

// ---------- Stack loading ----------

// LoadStackForStatus reads and classifies one feature's stack.yaml. LoadStack
// collapses missing, unreadable, and invalid YAML into one opaque error, which
// is why status needs its own loader. A successfully returned Stack is
// structurally valid by construction: validation runs as the last step here,
// so "loaded" and "structurally valid" are the same condition.
func LoadStackForStatus(featurePath, feature string) (Stack, error) {
	data, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Stack{}, fmt.Errorf("no stack.yaml found for feature: %s", feature)
		}
		return Stack{}, fmt.Errorf("stack.yaml unreadable for feature %s: %w", feature, err)
	}
	var stack Stack
	if err := yaml.Unmarshal(data, &stack); err != nil {
		return Stack{}, fmt.Errorf("stack.yaml invalid for feature %s: %w", feature, err)
	}
	if err := validateStackForStatus(stack); err != nil {
		return Stack{}, fmt.Errorf("invalid stack.yaml for feature %s: %w", feature, err)
	}
	return stack, nil
}

// validateStackForStatus rejects a structurally invalid stack. An empty
// branches list is valid, a duplicate GitBranch() across distinct names is
// valid, and a cycle is valid for this command.
func validateStackForStatus(stack Stack) error {
	for i, se := range stack.Branches {
		if se.Name == "" {
			return fmt.Errorf("entry %d: empty name", i)
		}
	}
	seen := make(map[string]bool, len(stack.Branches))
	for _, se := range stack.Branches {
		if seen[se.Name] {
			return fmt.Errorf("duplicate entry name %s", se.Name)
		}
		seen[se.Name] = true
	}
	for _, se := range stack.Branches {
		if !filepath.IsLocal(se.Name) || filepath.Clean(se.Name) == "." {
			return fmt.Errorf("unsafe entry name %s", se.Name)
		}
	}
	return nil
}

// ---------- Read-only tri-state probes ----------

// probeDirty reports whether a worktree has uncommitted changes. It runs with
// GIT_OPTIONAL_LOCKS=0 so even the index is left untouched, and it returns an
// error rather than a fabricated clean verdict when the probe fails.
func probeDirty(path string) (bool, error) {
	if path == "" {
		return false, errors.New("dirty probe requires a non-empty path")
	}
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// stackStatusOpMarkers is the fixed marker order shared with the shipped
// health helper. The first marker whose stat succeeds wins.
var stackStatusOpMarkers = []struct {
	marker string
	name   string
}{
	{"rebase-merge", "rebase"},
	{"rebase-apply", "rebase"},
	{"MERGE_HEAD", "merge"},
	{"CHERRY_PICK_HEAD", "cherry-pick"},
	{"REVERT_HEAD", "revert"},
	{"BISECT_LOG", "bisect"},
}

// probeActiveGitOp reports the in-progress Git operation for a worktree.
//
// It resolves the Git directory first and then stats the resolved directory
// itself: without that check, a `.git` file whose gitdir target no longer
// exists would make every marker stat return fs.ErrNotExist and the helper
// would fabricate "none" for a repository whose Git directory is gone. A
// missing `.git`, an unreadable or malformed gitdir pointer, a vanished or
// non-directory target, and a non-ENOENT marker stat are each an error.
func probeActiveGitOp(path string) (string, error) {
	if path == "" {
		return "", errors.New("active operation probe requires a non-empty path")
	}
	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		data, readErr := os.ReadFile(gitDir)
		if readErr != nil {
			return "", readErr
		}
		after, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if !ok || after == "" {
			return "", fmt.Errorf("malformed gitdir pointer in %s", gitDir)
		}
		if !filepath.IsAbs(after) {
			after = filepath.Join(path, after)
		}
		gitDir = filepath.Clean(after)
	}
	resolved, err := os.Stat(gitDir)
	if err != nil {
		return "", err
	}
	if !resolved.IsDir() {
		return "", fmt.Errorf("resolved git directory %s is not a directory", gitDir)
	}
	for _, check := range stackStatusOpMarkers {
		_, statErr := os.Stat(filepath.Join(gitDir, check.marker))
		if statErr == nil {
			return check.name, nil
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return "", statErr
		}
	}
	return StackStatusOpNone, nil
}

// ---------- Branch-ref inventory ----------

// BranchRefRecord is one parsed refs/heads/ record. Every field is stored
// verbatim as Git printed it.
type BranchRefRecord struct {
	Ref        string
	ObjectID   string
	Upstream   string
	Track      string
	TrackShort string
}

// BranchRefInventory is a single fail-closed `for-each-ref` result. A
// partially parsed map is never published: any violation makes the whole
// inventory unavailable and every dependent field null.
type BranchRefInventory struct {
	Available bool
	ByRef     map[string]BranchRefRecord
	Err       error
}

// BuildBranchRefInventory runs one `git -C <repoDir> for-each-ref` over
// refs/heads/. Every argument except the directory is a compile-time constant,
// and the directory is always the validated, non-empty repository dir.
func BuildBranchRefInventory(repoDir string) BranchRefInventory {
	inv := BranchRefInventory{ByRef: map[string]BranchRefRecord{}}
	if repoDir == "" {
		inv.Err = errors.New("branch ref inventory requires a non-empty repository directory")
		return inv
	}
	out, err := exec.Command("git", "-C", repoDir, "for-each-ref",
		"--format="+stackStatusRefFormat, stackStatusHeadsPrefix).Output()
	if err != nil {
		inv.Err = err
		return inv
	}
	records, parseErr := parseBranchRefInventory(out)
	if parseErr != nil {
		inv.Err = parseErr
		return inv
	}
	inv.Available = true
	inv.ByRef = records
	return inv
}

// parseBranchRefInventory is fail-closed: a violation invalidates the whole
// inventory rather than publishing a partial map, because a partial upstream
// map is exactly the input that would let status claim a false upstream state.
func parseBranchRefInventory(out []byte) (map[string]BranchRefRecord, error) {
	records := map[string]BranchRefRecord{}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if line == "" {
			// for-each-ref appends one newline per expansion, so exactly one
			// trailing empty record is expected.
			if i == len(lines)-1 {
				continue
			}
			return nil, fmt.Errorf("branch ref inventory: empty record at position %d", i)
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 5 {
			return nil, fmt.Errorf("branch ref inventory: record %d has %d fields, want 5", i, len(fields))
		}
		ref, objectID, upstream, track, trackShort := fields[0], fields[1], fields[2], fields[3], fields[4]
		if !strings.HasPrefix(ref, stackStatusHeadsPrefix) || len(ref) <= len(stackStatusHeadsPrefix) {
			return nil, fmt.Errorf("branch ref inventory: record %d has malformed branch ref %q", i, ref)
		}
		if !stackStatusObjectID.MatchString(objectID) {
			return nil, fmt.Errorf("branch ref inventory: record %d has malformed object id %q", i, objectID)
		}
		if upstream != "" && (!strings.HasPrefix(upstream, "refs/") || len(upstream) <= len("refs/")) {
			return nil, fmt.Errorf("branch ref inventory: record %d has malformed upstream ref %q", i, upstream)
		}
		if _, _, _, err := parseUpstreamTracking(upstream, track, trackShort); err != nil {
			return nil, fmt.Errorf("branch ref inventory: record %d: %w", i, err)
		}
		if _, dup := records[ref]; dup {
			return nil, fmt.Errorf("branch ref inventory: duplicate branch ref %q", ref)
		}
		records[ref] = BranchRefRecord{
			Ref:        ref,
			ObjectID:   objectID,
			Upstream:   upstream,
			Track:      track,
			TrackShort: trackShort,
		}
	}
	return records, nil
}

// parseUpstreamTracking maps the six accepted (upstream, track, trackshort)
// shapes real Git emits. `equal`, `none`, and `gone` are three distinct states
// and are never conflated; `[gone]` really does come with an empty trackshort.
func parseUpstreamTracking(upstream, track, trackShort string) (string, *int, *int, error) {
	if upstream == "" {
		if track != "" || trackShort != "" {
			return "", nil, nil, fmt.Errorf("no upstream but tracking is (%q, %q)", track, trackShort)
		}
		return StackStatusUpstreamNone, nil, nil, nil
	}
	switch trackShort {
	case "=":
		if track != "" {
			return "", nil, nil, fmt.Errorf("unsupported tracking pair (%q, %q)", track, trackShort)
		}
		return StackStatusUpstreamEqual, intPtr(0), intPtr(0), nil
	case ">":
		n, err := stackStatusTrackCount(track, "[ahead ", "]")
		if err != nil {
			return "", nil, nil, err
		}
		return StackStatusUpstreamAhead, intPtr(n), intPtr(0), nil
	case "<":
		n, err := stackStatusTrackCount(track, "[behind ", "]")
		if err != nil {
			return "", nil, nil, err
		}
		return StackStatusUpstreamBehind, intPtr(0), intPtr(n), nil
	case "<>":
		inner, ok := strings.CutPrefix(track, "[ahead ")
		if !ok {
			return "", nil, nil, fmt.Errorf("malformed diverged tracking %q", track)
		}
		inner, ok = strings.CutSuffix(inner, "]")
		if !ok {
			return "", nil, nil, fmt.Errorf("malformed diverged tracking %q", track)
		}
		parts := strings.SplitN(inner, ", behind ", 2)
		if len(parts) != 2 {
			return "", nil, nil, fmt.Errorf("malformed diverged tracking %q", track)
		}
		ahead, err := stackStatusDecimalValue(parts[0])
		if err != nil {
			return "", nil, nil, fmt.Errorf("malformed diverged tracking %q: %w", track, err)
		}
		behind, err := stackStatusDecimalValue(parts[1])
		if err != nil {
			return "", nil, nil, fmt.Errorf("malformed diverged tracking %q: %w", track, err)
		}
		return StackStatusUpstreamDiverged, intPtr(ahead), intPtr(behind), nil
	case "":
		if track == "[gone]" {
			return StackStatusUpstreamGone, nil, nil, nil
		}
		return "", nil, nil, fmt.Errorf("unsupported tracking pair (%q, %q)", track, trackShort)
	default:
		return "", nil, nil, fmt.Errorf("unsupported tracking pair (%q, %q)", track, trackShort)
	}
}

func stackStatusTrackCount(track, prefix, suffix string) (int, error) {
	inner, ok := strings.CutPrefix(track, prefix)
	if !ok {
		return 0, fmt.Errorf("malformed tracking %q", track)
	}
	inner, ok = strings.CutSuffix(inner, suffix)
	if !ok {
		return 0, fmt.Errorf("malformed tracking %q", track)
	}
	n, err := stackStatusDecimalValue(inner)
	if err != nil {
		return 0, fmt.Errorf("malformed tracking %q: %w", track, err)
	}
	return n, nil
}

func stackStatusDecimalValue(s string) (int, error) {
	if !stackStatusDecimal.MatchString(s) {
		return 0, fmt.Errorf("not a non-negative decimal integer: %q", s)
	}
	return strconv.Atoi(s)
}

// ---------- Parent counts ----------

// stackStatusParentCounts counts the symmetric difference between two local
// peeled commits. Both operands come from the shipped evaluator, which only
// publishes 40-hex peeled heads, so ancestryFullSHA is the correct precondition
// here and nowhere else in this feature. No process starts unless every
// precondition holds, and a failure yields two nulls, never zeros.
func stackStatusParentCounts(repoDir, localHead, parentHead string) (*int, *int) {
	if repoDir == "" || !ancestryFullSHA.MatchString(localHead) || !ancestryFullSHA.MatchString(parentHead) {
		return nil, nil
	}
	out, err := exec.Command("git", "-C", repoDir, "rev-list", "--left-right", "--count",
		localHead+"..."+parentHead).Output()
	if err != nil {
		return nil, nil
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\t")
	if len(fields) != 2 {
		return nil, nil
	}
	ahead, err := stackStatusDecimalValue(fields[0])
	if err != nil {
		return nil, nil
	}
	behind, err := stackStatusDecimalValue(fields[1])
	if err != nil {
		return nil, nil
	}
	return intPtr(ahead), intPtr(behind)
}

// ---------- Builder ----------

// BuildStackStatus projects one feature's stack. It calls FeatureStackEdges
// exactly once, computes no ancestry of its own, issues no second ref probe or
// repository resolution, and starts at most 2 + C + D Git processes of its own.
func BuildStackStatus(ws Workspace, cfg Config, feature, featurePath string, stack Stack) (*StackStatusReport, error) {
	edges, res := FeatureStackEdges(ws, cfg, feature, featurePath, stack)
	edges = ancestryEdgesFor(feature, stack, edges)
	repoDir := res.RepoDir

	report := &StackStatusReport{
		SchemaVersion: stackStatusSchema,
		Feature:       feature,
		Entries:       make([]StackStatusEntry, 0, len(stack.Branches)),
		Workspace: StackStatusWorkspace{
			Mode: ws.Mode,
			// Emitted as held: a configured external workspace root is stored
			// verbatim and may legitimately be non-canonical. Every
			// authoritative join is canonicalized independently of this field.
			MetadataRoot: ws.MetadataRoot,
			Repository: StackStatusRepository{
				Dir:       stackStatusOptString(res.RepoDir),
				Source:    res.Source,
				Alternate: stackStatusOptString(res.Alternate),
			},
		},
	}
	report.Workspace.StableID = stackStatusOptString(ws.StableID)

	var branchInv BranchRefInventory
	var worktreeInv WorktreeInventory
	if repoDir != "" {
		branchInv = BuildBranchRefInventory(repoDir)
		worktreeInv = BuildWorktreeInventory(repoDir)
	}

	var checkout *StackStatusCheckout
	if ws.Mode == ModeCheckout {
		checkout = stackStatusCheckoutFacts(repoDir, worktreeInv)
		report.Workspace.Checkout = checkout
	} else {
		report.Workspace.External = &StackStatusExternal{
			WorktreesRoot: canonicalize(filepath.Join(featurePath, "worktrees")),
		}
	}

	for i, se := range stack.Branches {
		edge := edges[i]
		entry := stackStatusProjectEdge(se, edge)
		entry.Upstream = stackStatusUpstreamFor(se, edge, branchInv)

		if ws.Mode == ModeCheckout {
			entry.Materialization = stackStatusCheckoutMaterialization(edge, checkout)
			entry.IsCurrentCheckout = stackStatusIsCurrentCheckout(edge, checkout)
		} else {
			entry.Materialization = stackStatusExternalMaterialization(featurePath, se, edge, worktreeInv)
		}

		if edge.Repo == "" {
			entry.ParentCounts.Ahead, entry.ParentCounts.Behind =
				stackStatusParentCounts(repoDir, edge.LocalHead, edge.ParentHead)
		}
		report.Entries = append(report.Entries, entry)
	}

	report.Summary = stackStatusSummarize(report.Entries)
	return report, nil
}

// stackStatusCheckoutFacts reads the single physical checkout. The current
// branch is never inferred from a session record, from HEAD parsing, or from a
// ref merely existing: only Git's worktree inventory may name it.
func stackStatusCheckoutFacts(repoDir string, inv WorktreeInventory) *StackStatusCheckout {
	checkout := &StackStatusCheckout{}
	if repoDir == "" {
		return checkout
	}
	checkout.Path = strPtr(repoDir)
	if inv.Available {
		if rec, ok := inv.ByPath[canonicalize(repoDir)]; ok {
			switch {
			case rec.Detached != nil && *rec.Detached:
				checkout.Detached = boolPtr(true)
			case rec.BranchRef != nil:
				checkout.Detached = boolPtr(false)
				checkout.Branch = strPtr(stackStatusStripHeads(*rec.BranchRef))
			}
		}
	}
	if dirty, err := probeDirty(repoDir); err == nil {
		checkout.Dirty = boolPtr(dirty)
	}
	if op, err := probeActiveGitOp(repoDir); err == nil {
		checkout.ActiveGitOp = strPtr(op)
	}
	return checkout
}

// stackStatusProjectEdge copies the evaluator's evidence field for field. No
// value is recomputed, re-probed, or reinterpreted here.
func stackStatusProjectEdge(se StackEntry, edge StackEdge) StackStatusEntry {
	entry := StackStatusEntry{
		Name:      se.Name,
		GitBranch: edge.GitBranch,
		Archived:  edge.Archived,
		Repo:      stackStatusOptString(edge.Repo),
		Base: StackStatusBase{
			Name: stackStatusOptString(edge.BaseName),
			Kind: edge.BaseKind,
			Ref:  stackStatusOptString(edge.BaseRef),
		},
		Heads: StackStatusHeads{
			Local:          stackStatusOptString(edge.LocalHead),
			LocalShort:     stackStatusOptString(edge.LocalHeadShort),
			Parent:         stackStatusOptString(edge.ParentHead),
			ParentShort:    stackStatusOptString(edge.ParentHeadShort),
			MergeBaseShort: stackStatusOptString(edge.MergeBaseShort),
		},
		BaseRecord: StackStatusBaseRecord{
			SHA:    stackStatusOptString(edge.LastBaseSHA),
			Commit: stackStatusOptString(edge.LastBaseCommit),
			Short:  stackStatusOptString(edge.LastBaseShort),
			State:  stackStatusOptString(string(edge.BaseRecord)),
		},
		Ancestry: StackStatusAncestry{
			Status:   stackStatusOptString(string(edge.Status)),
			Reason:   edge.Reason,
			Severity: edge.Severity,
			Guidance: stackStatusOptString(edge.Guidance),
			Notes:    make([]StackStatusNote, 0, len(edge.Notes)),
		},
		RepoSource: edge.RepoSource,
	}
	if edge.GitBranch == "" {
		entry.GitBranch = se.GitBranch()
	}
	// The evaluator's MergeBase points at a local in classify; copy the value
	// so a later mutation of the edge slice can never rewrite an emitted
	// document.
	if edge.MergeBase != nil {
		merge := *edge.MergeBase
		entry.Heads.MergeBase = &merge
	}
	// ref_exists is only RefProbed/RefExists: no second ref probe exists in
	// this feature, so it can never disagree with ancestry.status.
	if edge.RefProbed {
		entry.RefExists = boolPtr(edge.RefExists)
	}
	for _, note := range edge.Notes {
		entry.Ancestry.Notes = append(entry.Ancestry.Notes, StackStatusNote(note))
	}
	return entry
}

// stackStatusUpstreamFor joins one row against the branch-ref inventory. Both
// sides of the join are complete refs/heads/ names, so a same-named tag can
// neither change a key nor win a lookup.
func stackStatusUpstreamFor(se StackEntry, edge StackEdge, inv BranchRefInventory) StackStatusUpstream {
	up := StackStatusUpstream{LocalOnly: true}
	// A cross-repo row's branch lives in a repository this feature never
	// probes, so it performs no lookup at all.
	if edge.Repo != "" || !inv.Available {
		return up
	}
	key := edge.ChildRef
	if key == "" {
		key = stackStatusHeadsPrefix + se.GitBranch()
	}
	rec, ok := inv.ByRef[key]
	if !ok {
		return up
	}
	state, ahead, behind, err := parseUpstreamTracking(rec.Upstream, rec.Track, rec.TrackShort)
	if err != nil {
		return up
	}
	up.Configured = boolPtr(rec.Upstream != "")
	up.State = strPtr(state)
	up.Ahead = ahead
	up.Behind = behind
	if rec.Upstream != "" {
		up.Ref = strPtr(rec.Upstream)
		up.Display = strPtr(stackStatusUpstreamDisplay(rec.Upstream))
	}
	return up
}

// stackStatusUpstreamDisplay strips exactly one known prefix after validation.
// Any other validated refs/... value is displayed verbatim.
func stackStatusUpstreamDisplay(ref string) string {
	if after, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		return after
	}
	if after, ok := strings.CutPrefix(ref, stackStatusHeadsPrefix); ok {
		return after
	}
	return ref
}

func stackStatusStripHeads(ref string) string {
	return strings.TrimPrefix(ref, stackStatusHeadsPrefix)
}

// stackStatusExternalMaterialization decides external materialization from
// Git's own porcelain path map rather than from a directory stat, so a
// leftover directory Git does not know about reports `missing`. There is no
// per-row branch probe, stat, or prunability call.
func stackStatusExternalMaterialization(featurePath string, se StackEntry, edge StackEdge, inv WorktreeInventory) StackStatusMaterialization {
	m := StackStatusMaterialization{Kind: StackStatusKindWorktree, State: StackStatusMaterializedUnknown}
	if edge.Repo != "" {
		m.State = StackStatusMaterializedCrossRepo
		return m
	}
	candidate, err := ancestryWorktreeCandidatePath(featurePath, se.Name)
	if err != nil {
		return m
	}
	if !inv.Available {
		return m
	}
	canonical := canonicalize(candidate)
	rec, ok := inv.ByPath[canonical]
	if !ok {
		if se.Archived {
			m.State = StackStatusMaterializedArchived
		} else {
			m.State = StackStatusMaterializedMissing
		}
		return m
	}
	if rec.Prunable {
		m.State = StackStatusMaterializedPrunableMissing
	} else {
		m.State = StackStatusMaterializedPresent
	}
	m.Path = strPtr(canonical)
	if rec.BranchRef != nil {
		m.CheckedOutBranch = strPtr(stackStatusStripHeads(*rec.BranchRef))
	}
	if rec.Detached != nil {
		m.Detached = boolPtr(*rec.Detached)
	}
	if m.State == StackStatusMaterializedPresent {
		if dirty, dErr := probeDirty(canonical); dErr == nil {
			m.Dirty = boolPtr(dirty)
		}
		if op, oErr := probeActiveGitOp(canonical); oErr == nil {
			m.ActiveGitOp = strPtr(op)
		}
	}
	return m
}

// stackStatusCheckoutMaterialization derives state only from the evaluator's
// evidence. Checkout mode never uses the `archived` materialization state:
// a checkout entry has no worktree to be absent, and the row's own archived
// field already reports it. A cross-repo row is owned by another repository,
// so it reports the unsupported state and nothing else: this checkout's branch,
// dirtiness, and active operation are not facts about it, however its Git
// branch name happens to collide with the branch checked out here.
func stackStatusCheckoutMaterialization(edge StackEdge, checkout *StackStatusCheckout) StackStatusMaterialization {
	m := StackStatusMaterialization{Kind: StackStatusKindRef, State: StackStatusMaterializedUnknown}
	if edge.Repo != "" {
		m.State = StackStatusMaterializedCrossRepo
		return m
	}
	switch {
	case edge.RefProbed && edge.RefExists:
		m.State = StackStatusMaterializedPresent
	case edge.RefProbed:
		m.State = StackStatusMaterializedMissing
	}
	if checkout == nil || checkout.Branch == nil || edge.GitBranch != *checkout.Branch {
		return m
	}
	m.CheckedOutBranch = strPtr(*checkout.Branch)
	if checkout.Dirty != nil {
		m.Dirty = boolPtr(*checkout.Dirty)
	}
	if checkout.ActiveGitOp != nil {
		m.ActiveGitOp = strPtr(*checkout.ActiveGitOp)
	}
	return m
}

// stackStatusIsCurrentCheckout is a bool only when a branch is really checked
// out. A detached checkout, an unavailable inventory, an unavailable
// repository, and a cross-repo row each yield null.
func stackStatusIsCurrentCheckout(edge StackEdge, checkout *StackStatusCheckout) *bool {
	if edge.Repo != "" {
		return nil
	}
	if checkout == nil || checkout.Branch == nil || checkout.Detached == nil || *checkout.Detached {
		return nil
	}
	return boolPtr(edge.GitBranch == *checkout.Branch)
}

// stackStatusSummarize counts the finished rows in one pass, so "the three
// groups partition entries" is true by construction.
func stackStatusSummarize(entries []StackStatusEntry) StackStatusSummary {
	summary := StackStatusSummary{Entries: len(entries), LocalOnly: true}
	for _, e := range entries {
		switch stackStatusDeref(e.Ancestry.Status) {
		case string(AncestryStatusCurrent):
			summary.Ancestry.Current++
		case string(AncestryStatusStale):
			summary.Ancestry.Stale++
		case string(AncestryStatusDivergent):
			summary.Ancestry.Divergent++
		case string(AncestryStatusMissing):
			summary.Ancestry.Missing++
		case string(AncestryStatusCrossRepo):
			summary.Ancestry.CrossRepoUnsupported++
		default:
			summary.Ancestry.Unevaluated++
		}

		switch e.Materialization.State {
		case StackStatusMaterializedPresent:
			summary.Materialization.Present++
		case StackStatusMaterializedArchived:
			summary.Materialization.Archived++
		case StackStatusMaterializedMissing:
			summary.Materialization.Missing++
		case StackStatusMaterializedPrunableMissing:
			summary.Materialization.PrunableMissing++
		case StackStatusMaterializedCrossRepo:
			summary.Materialization.CrossRepoUnsupported++
		default:
			summary.Materialization.Unknown++
		}

		switch stackStatusDeref(e.Upstream.State) {
		case StackStatusUpstreamNone:
			summary.Upstream.None++
		case StackStatusUpstreamEqual:
			summary.Upstream.Equal++
		case StackStatusUpstreamAhead:
			summary.Upstream.Ahead++
		case StackStatusUpstreamBehind:
			summary.Upstream.Behind++
		case StackStatusUpstreamDiverged:
			summary.Upstream.Diverged++
		case StackStatusUpstreamGone:
			summary.Upstream.Gone++
		default:
			summary.Upstream.Unknown++
		}

		if e.RefExists == nil {
			summary.Unknown.RefExists++
		}
		if e.ParentCounts.Ahead == nil || e.ParentCounts.Behind == nil {
			summary.Unknown.ParentCounts++
		}
		if e.Materialization.Dirty == nil {
			summary.Unknown.Dirty++
		}
		if e.Materialization.ActiveGitOp == nil {
			summary.Unknown.ActiveGitOp++
		}
	}
	return summary
}

// NormalizeStackStatus converts nil slices to empty slices. It is idempotent
// and must run before encoding and before formatting.
func NormalizeStackStatus(r *StackStatusReport) {
	if r == nil {
		return
	}
	if r.Entries == nil {
		r.Entries = []StackStatusEntry{}
	}
	for i := range r.Entries {
		if r.Entries[i].Ancestry.Notes == nil {
			r.Entries[i].Ancestry.Notes = []StackStatusNote{}
		}
	}
}

// ---------- Human output ----------

// FormatStackStatus renders the report deterministically: header, table,
// per-row detail lines, summary, footer. It contains no generated timestamp,
// so two runs over an unchanged repository are byte-identical.
func FormatStackStatus(r *StackStatusReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	stackStatusWriteHeader(&b, r)
	b.WriteString("\n")
	if len(r.Entries) == 0 {
		b.WriteString("No branches tracked in stack.yaml.\n")
	} else {
		stackStatusWriteTable(&b, r.Entries)
	}
	b.WriteString("\n")
	stackStatusWriteSummary(&b, r.Summary)
	b.WriteString("\n")
	b.WriteString("Local-only report: no fetch was performed. Upstream and parent counts describe local refs only.\n")
	return b.String()
}

func stackStatusLabel(label, value string) string {
	return fmt.Sprintf("  %-11s %s\n", label+":", value)
}

func stackStatusWriteHeader(b *strings.Builder, r *StackStatusReport) {
	fmt.Fprintf(b, "Stack status: %s (mode: %s)\n",
		ancestrySanitize(r.Feature, stackStatusSanitizeLimit), r.Workspace.Mode)
	b.WriteString(stackStatusLabel("Workspace",
		ancestrySanitize(r.Workspace.MetadataRoot, stackStatusSanitizeLimit)))

	repo := "unavailable"
	if r.Workspace.Repository.Dir != nil {
		repo = ancestrySanitize(*r.Workspace.Repository.Dir, stackStatusSanitizeLimit)
	}
	b.WriteString(stackStatusLabel("Repository", fmt.Sprintf("%s (source: %s)", repo, r.Workspace.Repository.Source)))
	if r.Workspace.Repository.Alternate != nil {
		b.WriteString(stackStatusLabel("Alternate", fmt.Sprintf("%s (%s)",
			ancestrySanitize(*r.Workspace.Repository.Alternate, stackStatusSanitizeLimit), RepoSourceMismatchLabel)))
	}

	if r.Workspace.External != nil {
		b.WriteString(stackStatusLabel("Worktrees",
			ancestrySanitize(r.Workspace.External.WorktreesRoot, stackStatusSanitizeLimit)))
	}
	if r.Workspace.Checkout != nil {
		c := r.Workspace.Checkout
		if c.Path == nil {
			b.WriteString(stackStatusLabel("Checkout", "unavailable"))
			return
		}
		fmt.Fprintf(b, "  %-11s %s (branch: %s, detached: %s, dirty: %s, op: %s)\n",
			"Checkout:", ancestrySanitize(*c.Path, stackStatusSanitizeLimit),
			stackStatusOptDisplay(c.Branch, stackStatusSanitizeLimit),
			stackStatusBoolDisplay(c.Detached),
			stackStatusBoolDisplay(c.Dirty),
			stackStatusOptDisplay(c.ActiveGitOp, stackStatusSanitizeLimit))
	}
}

var stackStatusColumns = []string{"BRANCH", "ANCESTRY", "HEAD", "PARENT", "A/B", "UPSTREAM", "MATERIALIZATION", "FLAGS"}

func stackStatusWriteTable(b *strings.Builder, entries []StackStatusEntry) {
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, stackStatusRowCells(e))
	}
	widths := make([]int, len(stackStatusColumns))
	for i, head := range stackStatusColumns {
		widths[i] = len(head)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	b.WriteString(stackStatusJoinCells(stackStatusColumns, widths))
	for i, row := range rows {
		b.WriteString(stackStatusJoinCells(row, widths))
		for _, line := range stackStatusDetailLines(entries[i]) {
			fmt.Fprintf(b, "      %s\n", line)
		}
	}
}

func stackStatusJoinCells(cells []string, widths []int) string {
	var b strings.Builder
	for i, cell := range cells {
		if i == len(cells)-1 {
			b.WriteString(cell)
			break
		}
		fmt.Fprintf(&b, "%-*s  ", widths[i], cell)
	}
	b.WriteString("\n")
	return b.String()
}

func stackStatusRowCells(e StackStatusEntry) []string {
	branch := ancestrySanitize(e.Name, stackStatusSanitizeLimit)
	if e.GitBranch != e.Name {
		branch += fmt.Sprintf(" (git: %s)", ancestrySanitize(e.GitBranch, stackStatusSanitizeLimit))
	}
	return []string{
		branch,
		ancestryDisplayStatus(AncestryStatus(stackStatusDeref(e.Ancestry.Status))),
		stackStatusOrDash(e.Heads.LocalShort),
		stackStatusOrDash(e.Heads.ParentShort),
		stackStatusCountsCell(e.ParentCounts),
		stackStatusUpstreamCell(e.Upstream),
		e.Materialization.State,
		stackStatusFlagsCell(e),
	}
}

func stackStatusCountsCell(c StackStatusParentCounts) string {
	if c.Ahead == nil || c.Behind == nil {
		return "-"
	}
	return fmt.Sprintf("%d/%d", *c.Ahead, *c.Behind)
}

func stackStatusUpstreamCell(u StackStatusUpstream) string {
	if u.Configured == nil || u.State == nil {
		return "?"
	}
	if !*u.Configured {
		return StackStatusUpstreamNone
	}
	display := ancestrySanitize(stackStatusDeref(u.Display), stackStatusSanitizeLimit)
	switch *u.State {
	case StackStatusUpstreamEqual:
		return "equal:" + display
	case StackStatusUpstreamAhead:
		return fmt.Sprintf("ahead+%d:%s", stackStatusDerefInt(u.Ahead), display)
	case StackStatusUpstreamBehind:
		return fmt.Sprintf("behind-%d:%s", stackStatusDerefInt(u.Behind), display)
	case StackStatusUpstreamDiverged:
		return fmt.Sprintf("diverged+%d-%d:%s", stackStatusDerefInt(u.Ahead), stackStatusDerefInt(u.Behind), display)
	case StackStatusUpstreamGone:
		return "gone:" + display
	default:
		return "?"
	}
}

// stackStatusFlagsCell emits its tokens in one fixed order that is never
// re-ordered.
func stackStatusFlagsCell(e StackStatusEntry) string {
	var flags []string
	if e.IsCurrentCheckout != nil && *e.IsCurrentCheckout {
		flags = append(flags, "current")
	}
	if e.Archived {
		flags = append(flags, "archived")
	}
	if e.Repo != nil {
		flags = append(flags, "cross-repo")
	}
	if e.Materialization.Detached != nil && *e.Materialization.Detached {
		flags = append(flags, "detached")
	}
	present := e.Materialization.State == StackStatusMaterializedPresent
	switch {
	case e.Materialization.Dirty != nil && *e.Materialization.Dirty:
		flags = append(flags, "dirty")
	case e.Materialization.Dirty == nil && present:
		flags = append(flags, "dirty?")
	}
	switch {
	case e.Materialization.ActiveGitOp != nil && *e.Materialization.ActiveGitOp != StackStatusOpNone:
		flags = append(flags, "op="+ancestrySanitize(*e.Materialization.ActiveGitOp, stackStatusSanitizeLimit))
	case e.Materialization.ActiveGitOp == nil && present:
		flags = append(flags, "op?")
	}
	switch {
	case e.RefExists != nil && !*e.RefExists:
		flags = append(flags, "ref-missing")
	case e.RefExists == nil:
		flags = append(flags, "ref?")
	}
	if len(flags) == 0 {
		return "-"
	}
	return strings.Join(flags, ",")
}

// stackStatusDetailLines mirrors the shipped checkout detail-line grammar, so
// an edge that never consulted the recorded base cannot claim a verdict about
// it. Guidance and note details are printed verbatim: the evaluator already
// sanitized them and re-sanitizing would double-truncate.
func stackStatusDetailLines(e StackStatusEntry) []string {
	var lines []string
	if stackStatusDeref(e.Ancestry.Status) != string(AncestryStatusCurrent) {
		reason := fmt.Sprintf("reason: %s", e.Ancestry.Reason)
		if e.BaseRecord.Short != nil {
			reason += fmt.Sprintf(" last-base=%s", *e.BaseRecord.Short)
		}
		if e.Heads.MergeBase != nil {
			reason += fmt.Sprintf(" merge-base=%s", stackStatusDeref(e.Heads.MergeBaseShort))
		}
		if e.BaseRecord.State != nil && *e.BaseRecord.State != string(StackBaseRecordPresent) {
			reason += fmt.Sprintf(" base-record=%s", *e.BaseRecord.State)
		}
		lines = append(lines, reason)
	}
	if e.Ancestry.Guidance != nil {
		lines = append(lines, *e.Ancestry.Guidance)
	}
	for _, note := range e.Ancestry.Notes {
		lines = append(lines, fmt.Sprintf("note: %s", note.Detail))
	}
	if e.Materialization.Path != nil {
		lines = append(lines, fmt.Sprintf("path: %s",
			ancestrySanitize(*e.Materialization.Path, stackStatusSanitizeLimit)))
	}
	if e.Materialization.CheckedOutBranch != nil {
		lines = append(lines, fmt.Sprintf("checked-out: %s",
			ancestrySanitize(*e.Materialization.CheckedOutBranch, stackStatusSanitizeLimit)))
	}
	return lines
}

func stackStatusWriteSummary(b *strings.Builder, s StackStatusSummary) {
	b.WriteString("Summary:\n")
	fmt.Fprintf(b, "  entries: %d\n", s.Entries)
	fmt.Fprintf(b, "  ancestry: current=%d stale=%d divergent=%d missing=%d cross-repo-unsupported=%d unevaluated=%d\n",
		s.Ancestry.Current, s.Ancestry.Stale, s.Ancestry.Divergent, s.Ancestry.Missing,
		s.Ancestry.CrossRepoUnsupported, s.Ancestry.Unevaluated)
	fmt.Fprintf(b, "  materialization: present=%d archived=%d missing=%d prunable-missing=%d cross-repo-unsupported=%d unknown=%d\n",
		s.Materialization.Present, s.Materialization.Archived, s.Materialization.Missing,
		s.Materialization.PrunableMissing, s.Materialization.CrossRepoUnsupported, s.Materialization.Unknown)
	fmt.Fprintf(b, "  upstream: none=%d equal=%d ahead=%d behind=%d diverged=%d gone=%d unknown=%d\n",
		s.Upstream.None, s.Upstream.Equal, s.Upstream.Ahead, s.Upstream.Behind,
		s.Upstream.Diverged, s.Upstream.Gone, s.Upstream.Unknown)
	fmt.Fprintf(b, "  unknown: ref-exists=%d parent-counts=%d dirty=%d active-op=%d\n",
		s.Unknown.RefExists, s.Unknown.ParentCounts, s.Unknown.Dirty, s.Unknown.ActiveGitOp)
}

// ---------- small helpers ----------

func stackStatusOptString(s string) *string {
	if s == "" {
		return nil
	}
	return strPtr(s)
}

func stackStatusDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func stackStatusDerefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func stackStatusOrDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return ancestrySanitize(*s, stackStatusSanitizeLimit)
}

func stackStatusOptDisplay(s *string, limit int) string {
	if s == nil {
		return "?"
	}
	return ancestrySanitize(*s, limit)
}

func stackStatusBoolDisplay(b *bool) string {
	if b == nil {
		return "?"
	}
	if *b {
		return "yes"
	}
	return "no"
}
