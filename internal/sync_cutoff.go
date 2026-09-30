package internal

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type SyncCutoffSource string

const (
	SyncCutoffSourceRecorded  SyncCutoffSource = "recorded-metadata"
	SyncCutoffSourceParentTip SyncCutoffSource = "parent-tip-ancestor"
	SyncCutoffSourceNone      SyncCutoffSource = "none"
)

type SyncCutoffValidity string

const (
	SyncCutoffValid         SyncCutoffValidity = "valid"
	SyncCutoffInvalid       SyncCutoffValidity = "invalid"
	SyncCutoffNotApplicable SyncCutoffValidity = "not-applicable"
)

type SyncCutoffReason string

const (
	SyncCutoffReasonValidRecorded             SyncCutoffReason = "recorded-cutoff-valid"
	SyncCutoffReasonValidParentTip            SyncCutoffReason = "missing-cutoff-parent-ancestor"
	SyncCutoffReasonNotApplicable             SyncCutoffReason = "not-applicable"
	SyncCutoffReasonParentUnresolvable        SyncCutoffReason = "parent-unresolvable"
	SyncCutoffReasonChildUnresolvable         SyncCutoffReason = "child-unresolvable"
	SyncCutoffReasonRecordedUnresolvable      SyncCutoffReason = "recorded-cutoff-unresolvable"
	SyncCutoffReasonRecordedNotAncestor       SyncCutoffReason = "recorded-cutoff-not-ancestor"
	SyncCutoffReasonRecordedPredatesShared    SyncCutoffReason = "recorded-cutoff-predates-shared-history"
	SyncCutoffReasonMissingParentNotAncestor  SyncCutoffReason = "missing-cutoff-parent-not-ancestor"
	SyncCutoffReasonAncestryProbeFailed       SyncCutoffReason = "ancestry-probe-failed"
	SyncCutoffReasonRepositoryIdentityChanged SyncCutoffReason = "repository-identity-changed"
	SyncCutoffReasonFrozenEvidenceChanged     SyncCutoffReason = "frozen-evidence-changed"
)

// SyncCutoffDecision is the one replay-boundary decision shared by execution,
// recovery, planning and readback. Recorded is the raw stack.yaml value;
// RecordedSHA and EffectiveSHA are peeled, frozen commit object IDs.
type SyncCutoffDecision struct {
	Entry          string             `yaml:"entry" json:"entry"`
	GitBranch      string             `yaml:"git_branch" json:"git_branch"`
	RepoCommonDir  string             `yaml:"repo_common_dir" json:"repo_common_dir"`
	ConfiguredBase string             `yaml:"configured_base,omitempty" json:"configured_base,omitempty"`
	ParentEntry    string             `yaml:"parent_entry,omitempty" json:"parent_entry,omitempty"`
	Applicable     bool               `yaml:"applicable" json:"applicable"`
	ParentRef      string             `yaml:"parent_ref,omitempty" json:"parent_ref,omitempty"`
	ParentSHA      string             `yaml:"parent_sha,omitempty" json:"parent_sha,omitempty"`
	ChildRef       string             `yaml:"child_ref" json:"child_ref"`
	ChildSHA       string             `yaml:"child_sha" json:"child_sha"`
	Recorded       string             `yaml:"recorded,omitempty" json:"recorded,omitempty"`
	RecordedSHA    string             `yaml:"recorded_sha,omitempty" json:"recorded_sha,omitempty"`
	EffectiveSHA   string             `yaml:"effective_sha,omitempty" json:"effective_sha,omitempty"`
	SharedSHA      string             `yaml:"shared_sha,omitempty" json:"shared_sha,omitempty"`
	Source         SyncCutoffSource   `yaml:"source" json:"source"`
	Validity       SyncCutoffValidity `yaml:"validity" json:"validity"`
	Reason         SyncCutoffReason   `yaml:"reason" json:"reason"`
	Detail         string             `yaml:"-" json:"detail,omitempty"`
}

type SyncCutoffResolveInput struct {
	RepoDir       string
	RepoCommonDir string
	Entry         string
	GitBranch     string
	ParentRef     string
	ParentSHA     string
	ChildSHA      string
	Recorded      string
	Applicable    bool
}

type SyncCutoffFreezeInput struct {
	Entry      string
	RepoDir    string
	ParentRef  string
	ParentSHA  string
	Applicable bool
}

type SyncCutoffRefusalError struct {
	Decision SyncCutoffDecision
}

func SyncCutoffsRecoverableBeforeMutation(tx *SyncTransaction) bool {
	return tx != nil && !tx.CutoffsReady && len(tx.Actions) == 0 && len(tx.Validations) == 0 &&
		tx.Metadata.ExpectedSHA256 == tx.Metadata.BeforeSHA256 &&
		!tx.Metadata.IntentPending && tx.Metadata.IntentSHA256 == "" &&
		!tx.Publication.IntentDurable && tx.Rollback.StartedAt == "" &&
		(tx.Phase == SyncTxnPreparing || tx.Phase == SyncTxnForward)
}

func PreflightSyncCutoffRecovery(tx *SyncTransaction) error {
	if !SyncCutoffsRecoverableBeforeMutation(tx) {
		return errors.New("sync cutoff evidence cannot be reconstructed after mutation or publication")
	}
	if err := ValidateSyncTransaction(tx, tx.Feature, WorkspaceMode(tx.WorkspaceMode)); err != nil {
		return err
	}
	if err := verifySyncTransactionMetadataImage(tx, false); err != nil {
		return err
	}
	for i := range tx.Repositories {
		repo := &tx.Repositories[i]
		refs, err := syncTransactionListHeads(repo.Root)
		if err != nil {
			return err
		}
		if err := verifySyncTransactionRefImage(repo, refs); err != nil {
			return err
		}
		holders, err := syncTransactionHolderValues(repo.Root)
		if err != nil {
			return err
		}
		if err := verifySyncTransactionHolderImage(repo, holders); err != nil {
			return err
		}
	}
	return PreflightSyncTransactionBirth(tx)
}

func (e *SyncCutoffRefusalError) Error() string {
	d := e.Decision
	recorded := ancestrySanitize(d.Recorded, ancestrySanitizeLimit)
	if recorded == "" {
		recorded = "<absent>"
	}
	detail := ancestrySanitize(d.Detail, ancestryDetailLimit)
	if detail == "" {
		detail = ancestrySanitize(string(d.Reason), ancestryDetailLimit)
	}
	entry := ancestrySanitize(d.Entry, ancestrySanitizeLimit)
	branch := ancestrySanitize(d.GitBranch, ancestrySanitizeLimit)
	parentRef := ancestrySanitize(d.ParentRef, ancestrySanitizeLimit)
	if parentRef == "" {
		parentRef = "<unresolved>"
	}
	repo := ancestrySanitize(d.RepoCommonDir, ancestryPathLimit)
	if repo == "" {
		repo = "<unknown>"
	}
	return fmt.Sprintf(
		"sync cutoff refused for entry %q (git branch %q, repository %q): %s; recorded=%s parent-ref=%q frozen-parent=%s child=%s. "+
			"Inspect stack.yaml and the named commits in this repository, then repair last_base_sha only from known history or recreate the branch from a known base; tws will not guess a merge-base, fork-point, reflog entry, or arbitrary ancestor",
		entry, branch, repo, detail, recorded, parentRef,
		emptyCutoffValue(d.ParentSHA), emptyCutoffValue(d.ChildSHA),
	)
}

func emptyCutoffValue(value string) string {
	if value == "" {
		return "<unresolved>"
	}
	return ancestrySanitize(value, ancestrySanitizeLimit)
}

func syncCutoffResolveExact(repoDir, value string) (string, bool, error) {
	if strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	return syncTransactionResolveRef(repoDir, value)
}

func syncCutoffIsAncestor(repoDir, ancestor, descendant string) (bool, error) {
	_, stderr, code, err := syncTransactionGit(repoDir, nil,
		"merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("test ancestry %s..%s: %s", ancestor, descendant, syncTransactionGitError(stderr, err))
}

func syncCutoffMergeBases(repoDir, parent, child string) ([]string, error) {
	stdout, stderr, code, err := syncTransactionGit(repoDir, nil,
		"merge-base", "--all", parent, child)
	if err != nil {
		if code == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("measure shared ancestry: %s", syncTransactionGitError(stderr, err))
	}
	bases := strings.Fields(strings.ToLower(string(stdout)))
	sort.Strings(bases)
	return bases, nil
}

func invalidSyncCutoff(d SyncCutoffDecision, reason SyncCutoffReason, detail string) (SyncCutoffDecision, error) {
	d.Validity = SyncCutoffInvalid
	d.Reason = reason
	d.Detail = detail
	return d, &SyncCutoffRefusalError{Decision: d}
}

// ResolveSyncCutoff decides one cutoff from exact parent and child preimages.
// A merge-base is used only to prove that a recorded boundary is too old; it
// is never selected as a replacement boundary.
func ResolveSyncCutoff(in SyncCutoffResolveInput) (SyncCutoffDecision, error) {
	d := SyncCutoffDecision{
		Entry: in.Entry, GitBranch: in.GitBranch, RepoCommonDir: in.RepoCommonDir,
		ParentRef: in.ParentRef, ParentSHA: strings.ToLower(in.ParentSHA),
		ChildRef: "refs/heads/" + in.GitBranch, ChildSHA: strings.ToLower(in.ChildSHA),
		Recorded: in.Recorded, Source: SyncCutoffSourceNone,
	}
	if !in.Applicable {
		d.Validity = SyncCutoffNotApplicable
		d.Reason = SyncCutoffReasonNotApplicable
		return d, nil
	}

	parent, ok, err := syncCutoffResolveExact(in.RepoDir, in.ParentSHA)
	if err != nil {
		return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, err.Error())
	}
	if !ok || parent != d.ParentSHA {
		return invalidSyncCutoff(d, SyncCutoffReasonParentUnresolvable,
			fmt.Sprintf("frozen parent object %s does not resolve to that exact commit", emptyCutoffValue(d.ParentSHA)))
	}
	child, ok, err := syncCutoffResolveExact(in.RepoDir, in.ChildSHA)
	if err != nil {
		return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, err.Error())
	}
	if !ok || child != d.ChildSHA {
		return invalidSyncCutoff(d, SyncCutoffReasonChildUnresolvable,
			fmt.Sprintf("frozen child object %s does not resolve to that exact commit", emptyCutoffValue(d.ChildSHA)))
	}

	if in.Recorded == "" {
		ancestor, ancErr := syncCutoffIsAncestor(in.RepoDir, parent, child)
		if ancErr != nil {
			return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, ancErr.Error())
		}
		if !ancestor {
			return invalidSyncCutoff(d, SyncCutoffReasonMissingParentNotAncestor,
				"last_base_sha is absent and the frozen current parent tip is not an ancestor of the frozen child")
		}
		d.EffectiveSHA = parent
		d.Source = SyncCutoffSourceParentTip
		d.Validity = SyncCutoffValid
		d.Reason = SyncCutoffReasonValidParentTip
		return d, nil
	}

	recorded, ok, err := syncCutoffResolveExact(in.RepoDir, in.Recorded)
	if err != nil {
		return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, err.Error())
	}
	if !ok {
		return invalidSyncCutoff(d, SyncCutoffReasonRecordedUnresolvable,
			fmt.Sprintf("recorded cutoff %q does not resolve to a commit in the row repository", in.Recorded))
	}
	d.RecordedSHA = recorded
	ancestor, ancErr := syncCutoffIsAncestor(in.RepoDir, recorded, child)
	if ancErr != nil {
		return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, ancErr.Error())
	}
	if !ancestor {
		return invalidSyncCutoff(d, SyncCutoffReasonRecordedNotAncestor,
			fmt.Sprintf("recorded cutoff %s is not an ancestor of the frozen child", recorded))
	}

	shared, sharedErr := syncCutoffMergeBases(in.RepoDir, parent, child)
	if sharedErr != nil {
		return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, sharedErr.Error())
	}
	for _, candidate := range shared {
		if candidate == recorded {
			continue
		}
		beforeShared, probeErr := syncCutoffIsAncestor(in.RepoDir, recorded, candidate)
		if probeErr != nil {
			return invalidSyncCutoff(d, SyncCutoffReasonAncestryProbeFailed, probeErr.Error())
		}
		if beforeShared {
			d.SharedSHA = candidate
			return invalidSyncCutoff(d, SyncCutoffReasonRecordedPredatesShared,
				fmt.Sprintf("recorded cutoff %s strictly predates shared parent/child history at %s", recorded, candidate))
		}
	}

	d.EffectiveSHA = recorded
	d.Source = SyncCutoffSourceRecorded
	d.Validity = SyncCutoffValid
	d.Reason = SyncCutoffReasonValidRecorded
	return d, nil
}

func syncCutoffSelectedRef(tx *SyncTransaction, entry string) (*SyncTransactionRepo, SyncTransactionRef, error) {
	for i := range tx.Repositories {
		for _, ref := range tx.Repositories[i].Refs {
			for _, name := range ref.SelectedNames {
				if name == entry {
					return &tx.Repositories[i], ref, nil
				}
			}
		}
	}
	return nil, SyncTransactionRef{}, fmt.Errorf("sync cutoff entry %q has no selected ref preimage", entry)
}

// FreezeSyncCutoffs resolves all selected rows before the first branch action.
// It mutates tx only after every decision succeeds.
func FreezeSyncCutoffs(tx *SyncTransaction, stack Stack, inputs []SyncCutoffFreezeInput, save func() error) error {
	if tx == nil {
		return errors.New("sync transaction is absent")
	}
	if len(tx.Actions) != 0 || tx.Metadata.ExpectedSHA256 != tx.Metadata.BeforeSHA256 {
		return errors.New("sync cutoff evidence must be frozen before branch or metadata mutation")
	}
	if tx.CutoffsReady {
		return RevalidateSyncCutoffs(tx)
	}
	byEntry := make(map[string]SyncCutoffFreezeInput, len(inputs))
	for _, input := range inputs {
		if input.Entry == "" || byEntry[input.Entry].Entry != "" {
			return errors.New("sync cutoff freeze inputs contain an empty or duplicate entry")
		}
		byEntry[input.Entry] = input
	}
	if len(byEntry) != len(tx.Selected) {
		return fmt.Errorf("sync cutoff freeze has %d rows for %d selected entries", len(byEntry), len(tx.Selected))
	}

	decisions := make([]SyncCutoffDecision, 0, len(tx.Selected))
	for _, name := range tx.Selected {
		input, ok := byEntry[name]
		if !ok {
			return fmt.Errorf("sync cutoff freeze is missing selected entry %q", name)
		}
		entry := GetBranch(stack, name)
		if entry.Name == "" {
			return fmt.Errorf("sync cutoff freeze names missing stack entry %q", name)
		}
		repo, ref, err := syncCutoffSelectedRef(tx, name)
		if err != nil {
			return err
		}
		currentRepo, identityErr := syncTransactionRepo(tx, input.RepoDir)
		if identityErr != nil {
			return identityErr
		}
		if currentRepo.CommonDir != repo.CommonDir {
			return fmt.Errorf("sync cutoff repository for %q does not match its frozen child preimage", name)
		}
		parentSHA := strings.ToLower(input.ParentSHA)
		if input.Applicable && parentSHA == "" {
			resolved, found, resolveErr := syncCutoffResolveExact(repo.Root, input.ParentRef)
			if resolveErr != nil {
				return resolveErr
			}
			if !found {
				d := SyncCutoffDecision{
					Entry: name, GitBranch: entry.GitBranch(), RepoCommonDir: repo.CommonDir,
					ParentRef: input.ParentRef, ChildRef: ref.Ref, ChildSHA: ref.PreimageSHA,
					Recorded: entry.LastBaseSHA,
				}
				_, refusal := invalidSyncCutoff(d, SyncCutoffReasonParentUnresolvable,
					fmt.Sprintf("configured parent %q does not resolve to a commit", input.ParentRef))
				return refusal
			}
			parentSHA = resolved
		}
		decision, resolveErr := ResolveSyncCutoff(SyncCutoffResolveInput{
			RepoDir: repo.Root, RepoCommonDir: repo.CommonDir,
			Entry: name, GitBranch: entry.GitBranch(),
			ParentRef: input.ParentRef, ParentSHA: parentSHA,
			ChildSHA: ref.PreimageSHA, Recorded: entry.LastBaseSHA,
			Applicable: input.Applicable,
		})
		if resolveErr != nil {
			return resolveErr
		}
		decision.ConfiguredBase = entry.Base
		decision.Applicable = input.Applicable
		if parent := GetBranch(stack, entry.Base); parent.Name != "" {
			if WorkspaceMode(tx.WorkspaceMode) == ModeCheckout || SameStackRepo(parent.Repo, entry.Repo) {
				decision.ParentEntry = parent.Name
			}
		}
		decisions = append(decisions, decision)
	}

	tx.Cutoffs = decisions
	tx.CutoffsReady = true
	if save != nil {
		if err := save(); err != nil {
			tx.Cutoffs = nil
			tx.CutoffsReady = false
			return fmt.Errorf("persist sync cutoff evidence: %w", err)
		}
	}
	return nil
}

func SyncCutoffForEntry(tx *SyncTransaction, entry string) (SyncCutoffDecision, error) {
	if tx == nil || !tx.CutoffsReady {
		return SyncCutoffDecision{}, errors.New("sync recovery has no frozen cutoff evidence; preserve the state and use --abort or inspect it manually — continuation will not widen the replay")
	}
	for _, decision := range tx.Cutoffs {
		if decision.Entry == entry {
			if decision.Validity != SyncCutoffValid {
				return SyncCutoffDecision{}, fmt.Errorf("sync cutoff for %q is %s", entry, decision.Validity)
			}
			return decision, nil
		}
	}
	return SyncCutoffDecision{}, fmt.Errorf("sync cutoff evidence is missing selected entry %q", entry)
}

func revalidateSyncCutoffDecision(tx *SyncTransaction, d SyncCutoffDecision) error {
	repo := (*SyncTransactionRepo)(nil)
	for i := range tx.Repositories {
		if tx.Repositories[i].CommonDir == d.RepoCommonDir {
			repo = &tx.Repositories[i]
			break
		}
	}
	if repo == nil {
		return &SyncCutoffRefusalError{Decision: SyncCutoffDecision{
			Entry: d.Entry, GitBranch: d.GitBranch, Recorded: d.Recorded,
			ParentSHA: d.ParentSHA, ChildSHA: d.ChildSHA,
			Validity: SyncCutoffInvalid, Reason: SyncCutoffReasonRepositoryIdentityChanged,
			Detail: "the frozen cutoff repository is no longer part of this transaction",
		}}
	}
	recomputed, err := ResolveSyncCutoff(SyncCutoffResolveInput{
		RepoDir: repo.Root, RepoCommonDir: repo.CommonDir,
		Entry: d.Entry, GitBranch: d.GitBranch,
		ParentRef: d.ParentRef, ParentSHA: d.ParentSHA,
		ChildSHA: d.ChildSHA, Recorded: d.Recorded,
		Applicable: d.Applicable,
	})
	if err != nil {
		return err
	}
	recomputed.ConfiguredBase = d.ConfiguredBase
	recomputed.ParentEntry = d.ParentEntry
	recomputed.Applicable = d.Applicable
	recomputed.Detail = ""
	want := d
	want.Detail = ""
	if recomputed != want {
		recomputed.Validity = SyncCutoffInvalid
		recomputed.Reason = SyncCutoffReasonFrozenEvidenceChanged
		recomputed.Detail = "the frozen cutoff decision no longer reproduces from its durable parent and child preimages"
		return &SyncCutoffRefusalError{Decision: recomputed}
	}
	return nil
}

// RevalidateSyncCutoffs replays every frozen proof without consulting moving
// parent refs. It is used at recovery admission.
func RevalidateSyncCutoffs(tx *SyncTransaction) error {
	if tx == nil || !tx.CutoffsReady {
		return errors.New("sync recovery has no frozen cutoff evidence; preserve the state and use --abort or inspect it manually — continuation will not widen the replay")
	}
	if err := ValidateSyncTransaction(tx, tx.Feature, WorkspaceMode(tx.WorkspaceMode)); err != nil {
		return err
	}
	for _, decision := range tx.Cutoffs {
		if decision.Validity == SyncCutoffNotApplicable {
			continue
		}
		if err := revalidateSyncCutoffDecision(tx, decision); err != nil {
			return err
		}
	}
	return nil
}

// RevalidateSyncCutoffEntry additionally proves that the effective cutoff is
// still an ancestor of the row's current branch tip immediately before rebase.
func RevalidateSyncCutoffEntry(tx *SyncTransaction, repoDir, entry string) (SyncCutoffDecision, error) {
	d, err := SyncCutoffForEntry(tx, entry)
	if err != nil {
		return SyncCutoffDecision{}, err
	}
	if err := revalidateSyncCutoffDecision(tx, d); err != nil {
		return SyncCutoffDecision{}, err
	}
	repo, err := syncTransactionRepo(tx, repoDir)
	if err != nil {
		return SyncCutoffDecision{}, err
	}
	if repo.CommonDir != d.RepoCommonDir {
		d.Validity = SyncCutoffInvalid
		d.Reason = SyncCutoffReasonRepositoryIdentityChanged
		d.Detail = "the execution context resolves to a different Git object store than the frozen cutoff"
		return SyncCutoffDecision{}, &SyncCutoffRefusalError{Decision: d}
	}
	current, found, resolveErr := syncCutoffResolveExact(repo.Root, d.ChildRef)
	if resolveErr != nil {
		return SyncCutoffDecision{}, resolveErr
	}
	if !found {
		d.Validity = SyncCutoffInvalid
		d.Reason = SyncCutoffReasonChildUnresolvable
		d.Detail = "the selected child ref no longer resolves"
		return SyncCutoffDecision{}, &SyncCutoffRefusalError{Decision: d}
	}
	ancestor, ancErr := syncCutoffIsAncestor(repo.Root, d.EffectiveSHA, current)
	if ancErr != nil {
		return SyncCutoffDecision{}, ancErr
	}
	if !ancestor {
		d.Validity = SyncCutoffInvalid
		d.Reason = SyncCutoffReasonFrozenEvidenceChanged
		d.Detail = fmt.Sprintf("effective cutoff %s is no longer an ancestor of current child %s", d.EffectiveSHA, current)
		return SyncCutoffDecision{}, &SyncCutoffRefusalError{Decision: d}
	}
	return d, nil
}

// VerifySyncDestination resolves the destination parent and proves the
// successful child contains it before metadata records that destination.
func VerifySyncDestination(repoDir, parentRef, childRef string) (string, error) {
	parent, found, err := syncCutoffResolveExact(repoDir, parentRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("destination parent %q no longer resolves", parentRef)
	}
	return VerifySyncDestinationSHA(repoDir, parent, childRef)
}

func VerifySyncDestinationSHA(repoDir, parentSHA, childRef string) (string, error) {
	parent, found, err := syncCutoffResolveExact(repoDir, parentSHA)
	if err != nil {
		return "", err
	}
	if !found || parent != strings.ToLower(parentSHA) {
		return "", fmt.Errorf("executed destination %q no longer resolves to that exact commit", parentSHA)
	}
	child, found, err := syncCutoffResolveExact(repoDir, childRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("successful child %q no longer resolves", childRef)
	}
	ok, err := syncCutoffIsAncestor(repoDir, parent, child)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("successful child %s does not contain executed destination %s", child, parent)
	}
	return parent, nil
}

type SyncObservedRefChange struct {
	RepoCommonDir string
	Ref           string
	BeforeSHA     string
	AfterSHA      string
}

func SyncLatestObservedRefChanges(tx *SyncTransaction) []SyncObservedRefChange {
	if tx == nil || len(tx.Actions) == 0 {
		return nil
	}
	action := tx.Actions[len(tx.Actions)-1]
	if action.Status != SyncTxnActionObserved {
		return nil
	}
	before := syncTransactionValuesMap(action.BeforeRefs)
	var changes []SyncObservedRefChange
	for _, after := range action.AfterRefs {
		if old := before[after.Ref]; old != "" && old != after.SHA {
			changes = append(changes, SyncObservedRefChange{
				RepoCommonDir: action.RepoCommonDir,
				Ref:           after.Ref, BeforeSHA: old, AfterSHA: after.SHA,
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Ref < changes[j].Ref })
	return changes
}
