package internal

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// ---------- Enums ----------

// StackBaseKind describes how a stack entry's base was resolved to a Git ref.
type StackBaseKind string

// StackBaseRecord describes the state of the recorded LastBaseSHA.
type StackBaseRecord string

// StackRepoSource records which candidate produced the evaluated repository.
type StackRepoSource string

// StackAncestryReason is the precise cause behind a StackEdge status.
type StackAncestryReason string

// StackNoteKind enumerates informational per-edge notes.
type StackNoteKind string

// StackBasePolicy names the sync path whose base resolution the evaluator
// compares its own probe against when emitting identity notes. It is selected
// explicitly by the caller because the two sync implementations resolve a
// configured base differently, and reporting the wrong one is a false claim.
type StackBasePolicy string

const (
	StackBaseStackEntry StackBaseKind = "stack-entry"
	StackBaseLiteralRef StackBaseKind = "literal-ref"
	StackBaseNone       StackBaseKind = "none"
)

const (
	// StackBasePolicyNone makes no claim about any sync path and therefore
	// emits no identity note at all.
	StackBasePolicyNone StackBasePolicy = ""
	// StackBasePolicyRemoteDefault mirrors external sync, which maps a base
	// equal to the repository default branch to `origin/<default>`.
	StackBasePolicyRemoteDefault StackBasePolicy = "remote-default"
	// StackBasePolicyLiteralEntry mirrors checkout sync, which resolves
	// `entry.Base` literally instead of through the parent's GitBranch().
	StackBasePolicyLiteralEntry StackBasePolicy = "literal-entry"
)

// StackAncestryOptions carries the evaluator behaviour that depends on which
// sync path the caller's workspace mode would actually run. The zero value is
// safe: it emits no identity notes.
type StackAncestryOptions struct {
	BasePolicy StackBasePolicy
}

// StackBasePolicyForMode maps a workspace mode to the base resolution its sync
// path actually performs. External sync resolves the default branch through
// `origin/<default>`; checkout sync resolves `entry.Base` literally.
func StackBasePolicyForMode(mode WorkspaceMode) StackBasePolicy {
	if mode == ModeCheckout {
		return StackBasePolicyLiteralEntry
	}
	return StackBasePolicyRemoteDefault
}

const (
	StackBaseRecordAbsent       StackBaseRecord = "absent"
	StackBaseRecordPresent      StackBaseRecord = "present"
	StackBaseRecordUnresolvable StackBaseRecord = "unresolvable"
)

const (
	StackRepoWorkspace   StackRepoSource = "workspace"
	StackRepoWorktree    StackRepoSource = "worktree"
	StackRepoInferred    StackRepoSource = "inferred"
	StackRepoUnavailable StackRepoSource = "unavailable"
)

const (
	ReasonParentContained            StackAncestryReason = "parent-contained"
	ReasonParentAdvanced             StackAncestryReason = "parent-advanced"
	ReasonParentAdvancedNoBaseRecord StackAncestryReason = "parent-advanced-no-base-record"
	ReasonBaseRecordUnresolvable     StackAncestryReason = "base-record-unresolvable"
	ReasonBaseRewritten              StackAncestryReason = "base-rewritten"
	ReasonUnrelatedHistories         StackAncestryReason = "unrelated-histories"
	ReasonChildRefMissing            StackAncestryReason = "child-ref-missing"
	ReasonBaseRefMissing             StackAncestryReason = "base-ref-missing"
	ReasonCrossRepo                  StackAncestryReason = "cross-repo"
	ReasonBaseUnset                  StackAncestryReason = "base-unset"
	ReasonRepoUnavailable            StackAncestryReason = "repo-unavailable"
	ReasonAncestryProbeFailed        StackAncestryReason = "ancestry-probe-failed"
)

const (
	NoteBaseIdentityRemoteMismatch  StackNoteKind = "base-identity-remote-mismatch"
	NoteBaseIdentityLiteralMismatch StackNoteKind = "base-identity-literal-mismatch"
)

// RepoSourceMismatchLabel names the feature-level condition raised when the
// workspace and the feature's own worktree evidence resolve to two different
// main repository roots. It is derived from StackRepoResolution.Alternate and
// is never stored on a StackEdge.
const RepoSourceMismatchLabel = "repo-source-mismatch"

// ancestryUnevaluatedToken is the single source of the human display token for
// an edge that reached no ancestry conclusion. It is never an AncestryStatus.
const ancestryUnevaluatedToken = "unevaluated"

// ancestrySanitizeLimit bounds recorded metadata echoed back to the user.
const ancestrySanitizeLimit = 40

// ancestryPathLimit bounds filesystem paths echoed back to the user. Paths are
// routinely longer than a ref name and a 40-rune prefix identifies nothing.
const ancestryPathLimit = 200

// ancestryDetailLimit bounds error text echoed back to the user.
const ancestryDetailLimit = 200

// ancestryFullSHA matches a peeled object name exactly. rev-parse output is
// validated against it so a malformed or ambiguous answer is never carried
// into a comparison or into a command shown to the user.
var ancestryFullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ancestryPlainCommandToken matches the tokens that need no shell quoting when
// interpolated into a runnable command.
var ancestryPlainCommandToken = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)

// ErrRepoUnavailable is returned when ancestry evaluation cannot start because
// the source repository directory is empty or is not a Git repository.
var ErrRepoUnavailable = errors.New("stack ancestry: source repository unavailable")

// ---------- Structs ----------

// StackEdgeNote is an informational observation attached to one edge. It never
// changes status, severity, or any issue count.
type StackEdgeNote struct {
	Kind   StackNoteKind `json:"kind"`
	Detail string        `json:"detail"`
}

// StackEdge is the read-only projection of one configured parent-child edge.
type StackEdge struct {
	Feature   string `json:"feature"`
	Name      string `json:"name"`
	GitBranch string `json:"git_branch"`
	Archived  bool   `json:"archived"`
	Repo      string `json:"repo,omitempty"`

	BaseName string        `json:"base_name"`
	BaseKind StackBaseKind `json:"base_kind"`
	BaseRef  string        `json:"base_ref,omitempty"`

	ChildRef  string `json:"child_ref,omitempty"`
	RefExists bool   `json:"ref_exists"`
	RefProbed bool   `json:"ref_probed"`

	LocalHead       string  `json:"local_head,omitempty"`
	LocalHeadShort  string  `json:"local_head_short,omitempty"`
	ParentHead      string  `json:"parent_head,omitempty"`
	ParentHeadShort string  `json:"parent_head_short,omitempty"`
	MergeBase       *string `json:"merge_base"`
	MergeBaseShort  string  `json:"merge_base_short,omitempty"`

	LastBaseSHA    string          `json:"last_base_sha,omitempty"`
	LastBaseCommit string          `json:"last_base_commit,omitempty"`
	LastBaseShort  string          `json:"last_base_short,omitempty"`
	BaseRecord     StackBaseRecord `json:"base_record"`

	Status   AncestryStatus      `json:"status"`
	Reason   StackAncestryReason `json:"reason"`
	Severity CheckoutSeverity    `json:"severity"`
	Guidance string              `json:"guidance,omitempty"`
	Notes    []StackEdgeNote     `json:"notes,omitempty"`

	RepoSource StackRepoSource `json:"repo_source"`
}

// StackRepoResolution is the outcome of mode-aware repository selection.
type StackRepoResolution struct {
	RepoDir   string
	Source    StackRepoSource
	Alternate string
	Reason    string
}

type refResolution struct {
	full  string
	short string
	ok    bool
}

// ancestryEvaluator holds the per-evaluation caches. It is never global, never
// persisted, and never shared between calls.
type ancestryEvaluator struct {
	repoDir       string
	basePolicy    StackBasePolicy
	refs          map[string]refResolution
	shorts        map[string]string
	defaultBranch string
	defaultDone   bool
}

// ---------- Git runner ----------

// ancestryGit builds a read-only Git invocation rooted at a pre-validated,
// non-empty repository directory. cmd.Dir is used rather than -C so results
// never depend on the caller's working directory.
func ancestryGit(repoDir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Stderr = nil
	return cmd
}

// ---------- Primitives ----------

func newAncestryEvaluator(repoDir string, opts StackAncestryOptions) (*ancestryEvaluator, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("%w: %s", ErrRepoUnavailable, "empty repository directory")
	}
	if _, err := MainRepoRootIn(repoDir); err != nil {
		return nil, fmt.Errorf("%w: %s is not a git repository", ErrRepoUnavailable, ancestrySanitize(repoDir, ancestryPathLimit))
	}
	return &ancestryEvaluator{
		repoDir:    repoDir,
		basePolicy: opts.BasePolicy,
		refs:       make(map[string]refResolution),
		shorts:     make(map[string]string),
	}, nil
}

// resolveCommit peels ref to a commit. Negative results are cached, so a
// repeated unresolvable ref costs one process for the whole evaluation. Output
// that is not structurally a full object name is treated as no answer at all.
func (ev *ancestryEvaluator) resolveCommit(ref string) (string, string, bool) {
	if cached, ok := ev.refs[ref]; ok {
		return cached.full, cached.short, cached.ok
	}
	var res refResolution
	out, err := ancestryGit(ev.repoDir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}").Output()
	if err == nil {
		if full := strings.TrimSpace(string(out)); ancestryFullSHA.MatchString(full) {
			res.full = full
			res.ok = true
		}
	}
	if res.ok {
		res.short = ev.abbrev(res.full)
	}
	ev.refs[ref] = res
	return res.full, res.short, res.ok
}

func (ev *ancestryEvaluator) abbrev(full string) string {
	if full == "" {
		return ""
	}
	if cached, ok := ev.shorts[full]; ok {
		return cached
	}
	short := ""
	if out, err := ancestryGit(ev.repoDir, "rev-parse", "--short", full).Output(); err == nil {
		short = strings.TrimSpace(string(out))
	}
	if short == "" {
		if len(full) > 12 {
			short = full[:12]
		} else {
			short = full
		}
	}
	ev.shorts[full] = short
	return short
}

func (ev *ancestryEvaluator) defaultBranchName() string {
	if ev.defaultDone {
		return ev.defaultBranch
	}
	ev.defaultDone = true
	branch, err := DefaultBranchIn(ev.repoDir)
	if err != nil {
		branch = ""
	}
	ev.defaultBranch = branch
	return branch
}

// ancestryMergeBase reports the merge base of two peeled commits. Exit 1 with
// empty stdout means "no merge base"; any other non-zero exit is an error and
// is never read as a normal answer.
func ancestryMergeBase(repoDir, a, b string) (string, bool, error) {
	out, err := ancestryGit(repoDir, "merge-base", a, b).Output()
	trimmed := strings.TrimSpace(string(out))
	if err == nil {
		if trimmed == "" {
			return "", false, nil
		}
		return trimmed, true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && trimmed == "" {
		return "", false, nil
	}
	return "", false, err
}

// ---------- Classification ----------

// stackBaseRef selects the ref actually probed for an entry's base. A logical
// stack-entry name wins over a literal ref, matching GetBranch's first-match
// order.
func stackBaseRef(stack Stack, se StackEntry) (string, StackBaseKind) {
	if se.Base == "" {
		return "", StackBaseNone
	}
	for _, parent := range stack.Branches {
		if parent.Name == se.Base {
			return "refs/heads/" + parent.GitBranch(), StackBaseStackEntry
		}
	}
	return se.Base, StackBaseLiteralRef
}

func ancestrySeverity(status AncestryStatus, archived bool) CheckoutSeverity {
	switch status {
	case AncestryStatusCurrent:
		return SeverityOK
	case AncestryStatusStale, AncestryStatusDivergent, AncestryStatusMissing:
		if archived {
			return SeverityInfo
		}
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// newStackEdge fills the identity and base-selection fields that every edge
// carries, whatever happens afterwards. BaseRecord is deliberately left at its
// zero value: it is a statement about an evaluation that has not run yet, and
// claiming `absent` here would be a claim the evaluator cannot support.
func newStackEdge(feature string, se StackEntry, stack Stack) StackEdge {
	baseRef, baseKind := stackBaseRef(stack, se)
	return StackEdge{
		Feature:     feature,
		Name:        se.Name,
		GitBranch:   se.GitBranch(),
		Archived:    se.Archived,
		Repo:        se.Repo,
		BaseName:    se.Base,
		BaseKind:    baseKind,
		BaseRef:     baseRef,
		LastBaseSHA: se.LastBaseSHA,
	}
}

func finishStackEdge(e StackEdge, status AncestryStatus, reason StackAncestryReason, detail string) StackEdge {
	e.Status = status
	e.Reason = reason
	e.Severity = ancestrySeverity(status, e.Archived)
	e.Guidance = ancestryGuidance(e, detail)
	return e
}

func (ev *ancestryEvaluator) edge(feature string, se StackEntry, stack Stack) StackEdge {
	e := newStackEdge(feature, se, stack)

	// Rule 1: a cross-repo entry is a metadata-only decision. No Git process
	// is started for it, against any path.
	if se.Repo != "" {
		return finishStackEdge(e, AncestryStatusCrossRepo, ReasonCrossRepo, "")
	}
	// Rule 2: nothing to compare against.
	if se.Base == "" {
		return finishStackEdge(e, "", ReasonBaseUnset, "")
	}

	// Rule 3: the child ref, always through refs/heads/ so a same-named tag
	// can never win.
	e.ChildRef = "refs/heads/" + e.GitBranch
	e.RefProbed = true
	childFull, childShort, childOK := ev.resolveCommit(e.ChildRef)
	if !childOK {
		return finishStackEdge(e, AncestryStatusMissing, ReasonChildRefMissing, "")
	}
	e.RefExists = true
	e.LocalHead = childFull
	e.LocalHeadShort = childShort

	// Rule 5: the base ref, exactly as selected above.
	parentFull, parentShort, parentOK := ev.resolveCommit(e.BaseRef)
	if !parentOK {
		return finishStackEdge(e, AncestryStatusMissing, ReasonBaseRefMissing, "")
	}
	e.ParentHead = parentFull
	e.ParentHeadShort = parentShort

	// Rule 6: the recorded base commit, peeled because both writers record an
	// unpeeled rev-parse result. This is the first point at which the record
	// is actually consulted, so it is also the first point at which any
	// BaseRecord verdict — including `absent` — can be stated truthfully.
	if se.LastBaseSHA == "" {
		e.BaseRecord = StackBaseRecordAbsent
	} else if lastFull, lastShort, ok := ev.resolveCommit(se.LastBaseSHA); ok {
		e.BaseRecord = StackBaseRecordPresent
		e.LastBaseCommit = lastFull
		e.LastBaseShort = lastShort
	} else {
		e.BaseRecord = StackBaseRecordUnresolvable
	}

	e = ev.classify(e)
	if e.Status != "" && e.Status != AncestryStatusCrossRepo {
		e.Notes = ev.identityNotes(e)
	}
	return e
}

// classify applies the first-match classification table. Order is normative.
func (ev *ancestryEvaluator) classify(e StackEdge) StackEdge {
	// 1. The parent is already contained in the child: nothing to replay,
	// whichever way the parent moved.
	contained, err := gitIsAncestor(ev.repoDir, e.ParentHead, e.LocalHead)
	if err != nil {
		return finishStackEdge(e, "", ReasonAncestryProbeFailed, err.Error())
	}
	if contained {
		merge := e.ParentHead
		e.MergeBase = &merge
		e.MergeBaseShort = ev.abbrev(merge)
		return finishStackEdge(e, AncestryStatusCurrent, ReasonParentContained, "")
	}

	// 2. Unrelated histories get their own reason instead of a rewrite claim.
	mergeBase, exists, mbErr := ancestryMergeBase(ev.repoDir, e.LocalHead, e.ParentHead)
	if mbErr != nil {
		return finishStackEdge(e, "", ReasonAncestryProbeFailed, mbErr.Error())
	}
	if !exists {
		return finishStackEdge(e, AncestryStatusDivergent, ReasonUnrelatedHistories, "")
	}
	merge := mergeBase
	e.MergeBase = &merge
	e.MergeBaseShort = ev.abbrev(merge)

	// 4/5. A resolvable record separates "the parent advanced" from "the
	// parent's history was rewritten".
	if e.BaseRecord == StackBaseRecordPresent {
		recorded, recErr := gitIsAncestor(ev.repoDir, e.LastBaseCommit, e.ParentHead)
		switch {
		case recErr != nil:
			// Degrade to rule 3 rather than reading a fatal exit as "no".
			e.BaseRecord = StackBaseRecordUnresolvable
			e.LastBaseCommit = ""
			e.LastBaseShort = ""
		case !recorded:
			return finishStackEdge(e, AncestryStatusDivergent, ReasonBaseRewritten, "")
		default:
			return finishStackEdge(e, AncestryStatusStale, ReasonParentAdvanced, "")
		}
	}

	// 3. A recorded base that is not in this repository can support no claim.
	if e.BaseRecord == StackBaseRecordUnresolvable {
		return finishStackEdge(e, AncestryStatusStale, ReasonBaseRecordUnresolvable, "")
	}

	// 6. Nothing was ever recorded.
	return finishStackEdge(e, AncestryStatusStale, ReasonParentAdvancedNoBaseRecord, "")
}

// identityNotes reports, without acting on it, that the caller's sync path
// would resolve this edge's base to a different commit than the one doctor
// probed. Only the policy the caller selected can be reported: emitting the
// other mode's note would describe a sync path that will never run here.
func (ev *ancestryEvaluator) identityNotes(e StackEdge) []StackEdgeNote {
	if e.ParentHead == "" {
		return nil
	}
	switch ev.basePolicy {
	case StackBasePolicyRemoteDefault:
		return ev.remoteMismatchNotes(e)
	case StackBasePolicyLiteralEntry:
		return ev.literalMismatchNotes(e)
	default:
		return nil
	}
}

// remoteMismatchNotes mirrors external sync, which maps a base equal to the
// default branch to origin/<default> and records that SHA.
func (ev *ancestryEvaluator) remoteMismatchNotes(e StackEdge) []StackEdgeNote {
	if e.BaseKind != StackBaseLiteralRef {
		return nil
	}
	if e.BaseName == "" || e.BaseName != ev.defaultBranchName() {
		return nil
	}
	altFull, altShort, ok := ev.resolveCommit("refs/remotes/origin/" + e.BaseName)
	if !ok || altFull == e.ParentHead {
		return nil
	}
	return []StackEdgeNote{{
		Kind: NoteBaseIdentityRemoteMismatch,
		Detail: fmt.Sprintf("base %q is probed as %s, but tws sync resolves it as origin/%s (%s)",
			ancestrySanitize(e.BaseName, ancestrySanitizeLimit), e.ParentHeadShort,
			ancestrySanitize(e.BaseName, ancestrySanitizeLimit), altShort),
	}}
}

// literalMismatchNotes mirrors checkout sync, which resolves entry.Base
// literally instead of through the parent entry's GitBranch().
func (ev *ancestryEvaluator) literalMismatchNotes(e StackEdge) []StackEdgeNote {
	if e.BaseKind != StackBaseStackEntry {
		return nil
	}
	parentBranch := strings.TrimPrefix(e.BaseRef, "refs/heads/")
	if parentBranch == e.BaseName {
		return nil
	}
	altFull, altShort, ok := ev.resolveCommit(e.BaseName)
	if !ok || altFull == e.ParentHead {
		return nil
	}
	return []StackEdgeNote{{
		Kind: NoteBaseIdentityLiteralMismatch,
		Detail: fmt.Sprintf("base name %q also resolves as a literal ref to %s, which differs from parent branch %q (%s); checkout sync resolves the literal name",
			ancestrySanitize(e.BaseName, ancestrySanitizeLimit), altShort,
			ancestrySanitize(parentBranch, ancestrySanitizeLimit), e.ParentHeadShort),
	}}
}

// ---------- Presentation ----------

// ancestryDisplayStatus is the only producer of the unevaluated display token.
func ancestryDisplayStatus(s AncestryStatus) string {
	if s == "" {
		return ancestryUnevaluatedToken
	}
	return string(s)
}

// ancestrySanitize replaces non-printable runes and bounds length. It is a
// display rule only; classification always uses raw values.
func ancestrySanitize(s string, limit int) string {
	var b strings.Builder
	count := 0
	truncated := false
	for _, r := range s {
		if count >= limit {
			truncated = true
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
		count++
	}
	if truncated {
		b.WriteRune('…')
	}
	return b.String()
}

// ancestryCommandToken renders a Git-validated ref or object name for
// interpolation into a runnable command. Control characters are replaced, but
// the token is never truncated: a shortened ref or SHA yields a command that
// silently targets the wrong thing or does not run at all. Anything outside
// the portable unquoted set is single-quoted so the command stays pasteable.
func ancestryCommandToken(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	token := b.String()
	if ancestryPlainCommandToken.MatchString(token) {
		return token
	}
	return "'" + strings.ReplaceAll(token, "'", `'\''`) + "'"
}

// ancestryChildCommandToken is the child branch the repair command must name
// explicitly, so the rebase can never take whatever happens to be checked out
// as its target.
//
// It is the bare branch name, not `refs/heads/<branch>`: `git rebase`'s third
// argument is checked out, and a fully-qualified ref does not resolve to a
// branch there — Git would detach HEAD, replay onto the detached HEAD, and
// leave the child branch untouched. A bare name resolves to the branch even
// when a tag shares the name, which is exactly the target intended here.
func ancestryChildCommandToken(e StackEdge) string {
	if e.GitBranch == "" {
		return ""
	}
	return ancestryCommandToken(e.GitBranch)
}

// ancestryDetail reduces arbitrary error text to a single sanitized line.
func ancestryDetail(detail string) string {
	line := detail
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = line[:idx]
	}
	return ancestrySanitize(strings.TrimSpace(line), ancestryDetailLimit)
}

// ancestryGuidance is pure and total over every reason. It never returns a
// string containing a newline. Prose interpolations stay abbreviated for
// readability; every token inside a backticked command is complete.
func ancestryGuidance(e StackEdge, detail string) string {
	feature := ancestrySanitize(e.Feature, ancestrySanitizeLimit)
	name := ancestrySanitize(e.Name, ancestrySanitizeLimit)
	branch := ancestrySanitize(e.GitBranch, ancestrySanitizeLimit)
	baseRef := ancestrySanitize(e.BaseRef, ancestrySanitizeLimit)
	parent := e.ParentHeadShort

	recorded := e.LastBaseShort
	if e.BaseRecord != StackBaseRecordPresent {
		recorded = fmt.Sprintf("%q", ancestrySanitize(e.LastBaseSHA, ancestrySanitizeLimit))
	}

	switch e.Reason {
	case ReasonParentContained:
		return ""
	case ReasonParentAdvanced:
		return fmt.Sprintf("parent `%s` advanced to %s; run: tws sync %s", baseRef, parent, feature)
	case ReasonParentAdvancedNoBaseRecord:
		return fmt.Sprintf("parent `%s` advanced to %s; no recorded base commit for this branch, so sync uses a plain rebase — verify the parent history was not rewritten; run: tws sync %s", baseRef, parent, feature)
	case ReasonBaseRecordUnresolvable:
		return fmt.Sprintf("recorded base commit %s is not present in this repository; the replay strategy cannot be verified — inspect before running: tws sync %s", recorded, feature)
	case ReasonBaseRewritten:
		return fmt.Sprintf("recorded base commit %s is no longer in `%s` history;%s run: tws sync %s",
			recorded, baseRef, ancestryRebaseRepair(e), feature)
	case ReasonUnrelatedHistories:
		return fmt.Sprintf("`%s` and `%s` share no common history; check the configured base — a rebase would replay every commit", branch, baseRef)
	case ReasonChildRefMissing:
		if e.Archived {
			return fmt.Sprintf("archived branch `%s` has no git ref", branch)
		}
		// The stack entry already exists, so no creation command applies here.
		// The only honest recoveries are restoring the Git branch or
		// deliberately changing the recorded stack.
		return fmt.Sprintf("git branch `%s` does not exist while stack entry %q of feature %s is still configured; restore the branch from its remote or from a known commit, for example `git branch %s <known-commit>`, or deliberately remove and recreate the stack entry if no work must be preserved",
			branch, name, feature, ancestryCommandToken(e.GitBranch))
	case ReasonBaseRefMissing:
		return fmt.Sprintf("base ref `%s` does not exist; restore it or update `base` in stack.yaml", baseRef)
	case ReasonCrossRepo:
		return fmt.Sprintf("entry targets another repository (%s); cross-repo ancestry is not evaluated, so this edge is reported as cross-repo-unsupported", ancestrySanitize(e.Repo, ancestryPathLimit))
	case ReasonBaseUnset:
		return "no base configured for this entry; ancestry is not evaluated"
	case ReasonRepoUnavailable:
		return fmt.Sprintf("source repository could not be determined; ancestry is not evaluated (%s)", ancestryDetail(detail))
	case ReasonAncestryProbeFailed:
		return fmt.Sprintf("ancestry probe failed (%s); refs may have changed during evaluation — the recorded base was not consulted; re-run: tws doctor %s", ancestryDetail(detail), feature)
	default:
		return ""
	}
}

// ancestryRebaseRepair renders the complete `--onto` repair command, including
// the explicit target child branch. It is emitted only when every token is a
// value the evaluator itself resolved, so the command is always complete and
// pasteable, never a truncated fragment; otherwise the guidance simply omits
// it. It carries the same precondition tws sync's own `--onto` invocation
// carries — the recorded base is used as `<upstream>` in both — so it is
// offered as an equivalent repair, not as a guarantee about the child's own
// history.
//
// The wording deliberately stops at "an --onto rebase": the two sync paths do
// not run the same command. External sync adds `--update-refs` and rebases the
// checked-out worktree branch; checkout sync adds `--no-fork-point` and rebases
// onto the resolved new base SHA. Promising an identical strategy would be a
// claim this evaluator cannot support.
func ancestryRebaseRepair(e StackEdge) string {
	child := ancestryChildCommandToken(e)
	base := ancestryCommandToken(e.BaseRef)
	if child == "" || base == "" || !ancestryFullSHA.MatchString(e.LastBaseCommit) {
		return ""
	}
	return fmt.Sprintf(" an equivalent manual repair is `git rebase --onto %s %s %s`; tws sync also replays this edge with an --onto rebase, using the flags its own workspace mode requires;",
		base, e.LastBaseCommit, child)
}

// ---------- Core API ----------

// EvaluateStackAncestry classifies every configured edge of one feature's
// stack against a validated repository directory. It is mode-independent,
// strictly read-only, and never writes Git or metadata state. The caller
// states which sync path's base policy the identity notes should describe;
// the zero options value emits no notes.
func EvaluateStackAncestry(repoDir, feature string, stack Stack, opts StackAncestryOptions) ([]StackEdge, error) {
	ev, err := newAncestryEvaluator(repoDir, opts)
	if err != nil {
		return nil, err
	}
	edges := make([]StackEdge, 0, len(stack.Branches))
	for _, se := range stack.Branches {
		edges = append(edges, ev.edge(feature, se, stack))
	}
	return edges, nil
}

// EvaluateStackEdge is the single-edge form. It allocates a fresh cache and
// applies the same repository validation and base policy.
func EvaluateStackEdge(repoDir, feature string, se StackEntry, stack Stack, opts StackAncestryOptions) (StackEdge, error) {
	ev, err := newAncestryEvaluator(repoDir, opts)
	if err != nil {
		return StackEdge{}, err
	}
	return ev.edge(feature, se, stack), nil
}

// UnevaluatedStackEdges projects a stack without starting any Git process.
// Cross-repo and base-less entries keep their own meaning so an entry never
// changes classification merely because the repository was unresolvable.
func UnevaluatedStackEdges(feature string, stack Stack, reason StackAncestryReason, detail string) []StackEdge {
	edges := make([]StackEdge, 0, len(stack.Branches))
	for _, se := range stack.Branches {
		e := newStackEdge(feature, se, stack)
		e.RepoSource = StackRepoUnavailable
		switch {
		case se.Repo != "":
			e = finishStackEdge(e, AncestryStatusCrossRepo, ReasonCrossRepo, "")
		case se.Base == "":
			e = finishStackEdge(e, "", ReasonBaseUnset, "")
		default:
			e = finishStackEdge(e, "", reason, detail)
		}
		edges = append(edges, e)
	}
	return edges
}

// ---------- Mode-aware adapter ----------

// ancestryRepoCandidate normalises a filesystem path to a canonical main
// repository root, or reports that it is not usable. It never runs Git on an
// empty path.
func ancestryRepoCandidate(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	root, err := MainRepoRootIn(path)
	if err != nil {
		return "", false
	}
	return canonicalize(root), true
}

// ancestryWorktreeCandidatePath contains an untrusted logical entry name below
// the feature's worktrees directory before any Git process is started.
func ancestryWorktreeCandidatePath(featurePath, name string) (string, error) {
	if name == "" || !filepath.IsLocal(name) || filepath.Clean(name) == "." {
		return "", fmt.Errorf("unsafe stack entry name %q", ancestrySanitize(name, ancestrySanitizeLimit))
	}
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	candidate := filepath.Join(worktreesRoot, name)
	canonRoot := canonicalize(worktreesRoot)
	canonCandidate := canonicalize(candidate)
	rel, err := filepath.Rel(canonRoot, canonCandidate)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("stack entry %q resolves outside the feature worktrees directory", ancestrySanitize(name, ancestrySanitizeLimit))
	}
	return candidate, nil
}

// ResolveStackAncestryRepo is the only mode-aware code in this feature. It
// returns a canonical main repository root, never a linked-worktree path.
func ResolveStackAncestryRepo(ws Workspace, cfg Config, featurePath string, stack Stack) StackRepoResolution {
	if ws.Mode == ModeCheckout {
		if root, ok := ancestryRepoCandidate(ws.RepoRoot); ok {
			return StackRepoResolution{RepoDir: root, Source: StackRepoWorkspace}
		}
		return StackRepoResolution{
			Source: StackRepoUnavailable,
			Reason: "checkout workspace repository root is not a git repository",
		}
	}

	// Candidate 1 — feature-scoped worktree evidence. It is the only source
	// that stays correct when TWS_ROOT points at another repository.
	for _, se := range stack.Branches {
		if se.Repo != "" || se.Archived {
			continue
		}
		candidate, err := ancestryWorktreeCandidatePath(featurePath, se.Name)
		if err != nil {
			return StackRepoResolution{Source: StackRepoUnavailable, Reason: err.Error()}
		}
		root, ok := ancestryRepoCandidate(candidate)
		if !ok {
			continue
		}
		res := StackRepoResolution{RepoDir: root, Source: StackRepoWorktree}
		if alt, altOK := ancestryRepoCandidate(ws.RepoRoot); altOK && alt != root {
			res.Alternate = alt
		}
		return res
	}

	// Candidate 2 — the resolved workspace repository root.
	if root, ok := ancestryRepoCandidate(ws.RepoRoot); ok {
		return StackRepoResolution{RepoDir: root, Source: StackRepoWorkspace}
	}

	// Candidate 3 — inference from the metadata root.
	metadataRoot := ws.MetadataRoot
	if metadataRoot == "" {
		metadataRoot = filepath.Dir(featurePath)
	}
	if metadataRoot != "" {
		inferred, err := inferExternalRepoRoot(metadataRoot, cfg)
		if err == nil && inferred != "" {
			return StackRepoResolution{RepoDir: canonicalize(inferred), Source: StackRepoInferred}
		}
		if err != nil {
			return StackRepoResolution{Source: StackRepoUnavailable, Reason: err.Error()}
		}
	}
	return StackRepoResolution{
		Source: StackRepoUnavailable,
		Reason: fmt.Sprintf("no source repository for feature %s", ancestrySanitize(filepath.Base(featurePath), ancestrySanitizeLimit)),
	}
}

// FeatureStackEdges resolves the repository for one feature and evaluates its
// stack. It fails soft: an unusable repository yields unevaluated edges, never
// an error and never a false ancestry claim. The identity-note base policy is
// selected from the workspace mode, so an edge only ever reports the base
// resolution the sync path that would actually run performs.
func FeatureStackEdges(ws Workspace, cfg Config, feature, featurePath string, stack Stack) ([]StackEdge, StackRepoResolution) {
	res := ResolveStackAncestryRepo(ws, cfg, featurePath, stack)
	if res.RepoDir == "" {
		return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, res.Reason), res
	}
	opts := StackAncestryOptions{BasePolicy: StackBasePolicyForMode(ws.Mode)}
	edges, err := EvaluateStackAncestry(res.RepoDir, feature, stack, opts)
	if err != nil {
		failed := StackRepoResolution{Source: StackRepoUnavailable, Reason: err.Error()}
		return UnevaluatedStackEdges(feature, stack, ReasonRepoUnavailable, err.Error()), failed
	}
	for i := range edges {
		edges[i].RepoSource = res.Source
	}
	return edges, res
}
