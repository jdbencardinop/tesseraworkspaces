package internal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ScopedPushOptions struct {
	Location     ScopedPushLocation
	Stack        Stack
	Selection    SyncSelection
	Repositories map[string]ScopedPushRepository // selected logical name -> proven owner
	DefaultRepo  string
	DryRun       bool
	Continue     bool
	Cancel       bool
	Out          io.Writer
	Err          io.Writer
	Verify       func() error // the caller retains both rename admissions
}

// ScopedPushWorktreePath validates only the selected logical spelling. It
// shares the slash-safe path rule with injection without discovering siblings.
func ScopedPushWorktreePath(featurePath, name string) (string, error) {
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return "", errors.New("logical publication entry contains control characters")
		}
	}
	return injectWorktreePath(featurePath, name)
}

func scopedPushCheckOtherState(loc ScopedPushLocation) error {
	var paths []string
	if loc.Mode == ModeExternal {
		paths = []string{SyncStatePath(loc.FeaturePath), SyncRunStatePath(loc.FeaturePath)}
	} else {
		paths = []string{CheckoutTransactionPath(loc.FeaturePath)}
	}
	paths = append(paths, ReparentStatePath(loc.ReparentLocation()))
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("existing sync/reparent recovery evidence at %s blocks standalone publication", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func BuildScopedPushPlan(opts ScopedPushOptions) (*ScopedPushState, error) {
	loc := opts.Location
	if err := scopedPushCheckOtherState(loc); err != nil {
		return nil, err
	}
	token, err := managedOperationID()
	if err != nil {
		return nil, err
	}
	root, err := scopedPushDirectoryAt(loc.MetadataRoot)
	if err != nil {
		return nil, err
	}
	feature, err := scopedPushDirectoryAt(loc.FeaturePath)
	if err != nil {
		return nil, err
	}
	p := ScopedPushPlan{
		Token: token, Created: time.Now().UTC().Format(time.RFC3339),
		Mode: loc.Mode, Feature: loc.Feature, Root: root, FeatureDir: feature,
		Scope: opts.Selection.Policy.ScopeKind, Selector: opts.Selection.Policy.Selector,
	}
	if loc.SelectionRoot != "" {
		p.SelectionRoot, err = filepath.Abs(loc.SelectionRoot)
		if err != nil {
			return nil, err
		}
		binding, bindErr := scopedPushDirectoryAt(p.SelectionRoot)
		if bindErr == nil {
			p.Selection = &binding
		} else if !errors.Is(bindErr, os.ErrNotExist) {
			return nil, bindErr
		}
	}
	env, err := PrepareLocalSelectedReparentPushEnvelope(loc.ReparentLocation(), opts.DefaultRepo, &opts.Stack, opts.Selection.Names)
	if err != nil {
		return nil, err
	}
	if refusal := env.LoadRefusal(); refusal != nil {
		return nil, AnchorReparentRefusal(refusal)
	}
	rawFollowup, err := LoadReparentRemoteRecord(loc.ReparentLocation())
	if err != nil {
		return nil, err
	}
	state := &ScopedPushState{Kind: scopedPushStateKind, Version: ScopedPushStateVersion, Phase: "preparing"}
	for _, selected := range opts.Selection.Entries {
		if _, err := ScopedPushWorktreePath(loc.FeaturePath, selected.Name); err != nil {
			return nil, err
		}
		entry := ScopedPushEntry{
			Name: selected.Name, GitBranch: selected.GitBranch, RepoToken: selected.Repo,
			Base: selected.Base, Archived: selected.Archived,
		}
		if selected.Archived {
			p.Entries = append(p.Entries, entry)
			state.Rows = append(state.Rows, "skipped")
			continue
		}
		repo, ok := opts.Repositories[selected.Name]
		if !ok {
			return nil, fmt.Errorf("cannot prove repository owner for selected entry %q", selected.Name)
		}
		if err := repo.verify(); err != nil {
			return nil, err
		}
		entry.Repository = repo
		entry.Destination = "refs/heads/" + selected.GitBranch
		entry.Source, err = scopedPushRef(repo.Directory.Path, entry.Destination)
		if err != nil {
			return nil, err
		}
		if entry.Source == "" {
			return nil, fmt.Errorf("selected Git branch %q has no local ref", selected.GitBranch)
		}
		if err := scopedPushCommit(repo.Directory.Path, entry.Source); err != nil {
			return nil, err
		}
		endpoint, err := scopedPushResolveEndpoint(repo.Directory.Path, entry.Destination)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", selected.Name, err)
		}
		entry.Endpoint, entry.TrackingRef = endpoint.Fingerprint, endpoint.TrackingRef
		entry.Expected, err = scopedPushRef(repo.Directory.Path, entry.TrackingRef)
		if err != nil {
			return nil, err
		}
		if entry.Expected != "" {
			if err := scopedPushCommit(repo.Directory.Path, entry.Expected); err != nil {
				return nil, err
			}
		}
		if rawFollowup != nil {
			if followup, found := rawFollowup.Entry(entry.Name); found && followup.Pending() {
				if followup.GitBranch != entry.GitBranch || followup.Repo != entry.RepoToken ||
					followup.RemoteRef != entry.TrackingRef || len(followup.NewTipSHA) != len(entry.Source) ||
					len(followup.RemoteSHAAtWrite) != len(entry.Source) {
					return nil, fmt.Errorf("%s: reparent follow-up no longer matches the selected publication identity", entry.Name)
				}
				entry.Followup = &followup
			}
		}
		if entry.Followup != nil {
			entry.Inclusion, err = scopedPushProveInclusion(repo.Directory.Path, entry.Destination, entry.TrackingRef, entry.Expected, entry.Source)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", entry.Name, err)
			}
		}
		p.Entries = append(p.Entries, entry)
		state.Rows = append(state.Rows, "pending")
	}
	p.Pins = scopedPushPlanPins(p.Token, p.Entries)
	state.Pins = make([]string, len(p.Pins))
	for i := range state.Pins {
		state.Pins[i] = "pending"
	}
	state.Plan = p
	return state, nil
}

func PrintScopedPushPlan(out io.Writer, state *ScopedPushState) {
	p := state.Plan
	label := SyncRunPolicy{ScopeKind: p.Scope, Selector: p.Selector}.ScopeLabel()
	fmt.Fprintf(out, "Push mode: %s scope=%s\n", p.Mode, label)                                    //nolint:errcheck
	fmt.Fprintln(out, "Remote state: unverified (local-only preview; no fetch or remote contact)") //nolint:errcheck
	for i, entry := range p.Entries {
		if entry.Archived {
			fmt.Fprintf(out, "  [-] %s (git: %s; archived, skipped)\n", entry.Name, entry.GitBranch) //nolint:errcheck
			continue
		}
		expected := entry.Expected
		if expected == "" {
			expected = "absent"
		}
		fmt.Fprintf(out, "  [%d] %s (git: %s) %s -> origin:%s; lease=%s via %s; %s\n", //nolint:errcheck
			i+1, entry.Name, entry.GitBranch, entry.Source, entry.Destination, expected, entry.TrackingRef, state.Rows[i])
		if entry.Inclusion != nil {
			fmt.Fprintf(out, "      inclusion: witness=%s tracking-reflog-time=%d (frozen qualifying branch reflog range)\n", //nolint:errcheck
				entry.Inclusion.Witness, entry.Inclusion.TrackingTime)
		}
		if entry.Followup != nil {
			fmt.Fprintln(out, "      reparent-remote: retarget the pull request before publishing") //nolint:errcheck
		}
	}
	if state.Revision != 0 {
		fmt.Fprintf(out, "Frozen publication: phase=%s; durable successes are never republished; intent means uncertain remote effect\n", state.Phase) //nolint:errcheck
	}
}

func (j *scopedPushJournal) verifyBindings(loc ScopedPushLocation) error {
	if err := j.verifyLocation(loc); err != nil {
		return err
	}
	matches, err := ScopedPushRecoverySelectionMatches(loc, j.state)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("publication execution-selected root changed")
	}
	return j.snapshot.verify(j.path)
}

func (j *scopedPushJournal) verifyLocation(loc ScopedPushLocation) error {
	p := j.state.Plan
	if j.state.Terminal() && loc.Mode == ModeExternal {
		return j.verifyExternalTerminalLocation(loc)
	}
	if loc.Mode != p.Mode || loc.Feature != p.Feature {
		return errors.New("publication state belongs to another mode or feature")
	}
	root, err := scopedPushDirectoryAt(loc.MetadataRoot)
	if err != nil || root.Path != p.Root.Path || root.Identity != p.Root.Identity {
		return errors.Join(err, errors.New("publication workspace root changed"))
	}
	feature, err := scopedPushDirectoryAt(loc.FeaturePath)
	if err != nil || feature.Path != p.FeatureDir.Path || feature.Identity != p.FeatureDir.Identity {
		return errors.Join(err, errors.New("publication feature directory changed"))
	}
	if err := errors.Join(p.Root.verify(), p.FeatureDir.verify(), p.StateDirectory.verify()); err != nil {
		return err
	}
	if j.path != ScopedPushStatePath(ScopedPushLocation{
		Mode: p.Mode, Feature: p.Feature, FeaturePath: p.FeatureDir.Path, MetadataRoot: p.Root.Path,
	}) {
		return errors.New("publication state path changed")
	}
	return nil
}

// Supported external feature rename carries terminal bytes with the same
// feature inode. Historical names are not recovery/publication authority.
func (j *scopedPushJournal) verifyExternalTerminalLocation(loc ScopedPushLocation) error {
	p := j.state.Plan
	if !j.state.Terminal() || p.Mode != ModeExternal || validateFeatureName(loc.Feature) != nil {
		return errors.New("invalid external publication history")
	}
	root, err := scopedPushDirectoryAt(loc.MetadataRoot)
	if err != nil || root.Path != p.Root.Path || root.Identity != p.Root.Identity {
		return errors.Join(err, errors.New("publication history workspace identity changed"))
	}
	feature, err := scopedPushDirectoryAt(loc.FeaturePath)
	if err != nil || feature.Path != filepath.Join(root.Path, loc.Feature) ||
		feature.Identity != p.FeatureDir.Identity || feature.Identity != p.StateDirectory.Identity {
		return errors.Join(err, errors.New("publication history belongs to a different physical feature"))
	}
	if j.path != ScopedPushStatePath(ScopedPushLocation{Mode: ModeExternal, FeaturePath: feature.Path}) {
		return errors.New("publication history path changed")
	}
	return errors.Join(p.Root.verify(), feature.verify(), j.snapshot.verify(j.path))
}

// LoadScopedPushRecoveryCandidate validates the physical journal location
// without letting a terminal receipt decide which pending scope to recover.
func LoadScopedPushRecoveryCandidate(loc ScopedPushLocation) (*ScopedPushState, error) {
	snapshot, err := scopedPushReadFile(ScopedPushStatePath(loc))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state, err := scopedPushDecode(snapshot.data)
	if err != nil {
		return nil, err
	}
	feature, err := scopedPushDirectoryAt(loc.FeaturePath)
	if err != nil {
		return nil, err
	}
	root, err := scopedPushDirectoryAt(loc.MetadataRoot)
	if err != nil {
		return nil, err
	}
	loc.FeaturePath, loc.MetadataRoot = feature.Path, root.Path
	journal := scopedPushJournal{path: ScopedPushStatePath(loc), snapshot: snapshot, state: state}
	if err := errors.Join(journal.verifyLocation(loc), snapshot.verify(journal.path)); err != nil {
		return nil, err
	}
	return state, nil
}

func ScopedPushRecoverySelectionMatches(loc ScopedPushLocation, state *ScopedPushState) (bool, error) {
	p := state.Plan
	selection, err := filepath.Abs(loc.SelectionRoot)
	if loc.SelectionRoot == "" {
		selection = ""
	}
	if err != nil {
		return false, err
	}
	if selection != p.SelectionRoot {
		if p.Selection == nil {
			return false, nil
		}
		binding, err := scopedPushDirectoryAt(selection)
		if err != nil || binding.Path != p.Selection.Path || binding.Identity != p.Selection.Identity {
			return false, nil
		}
	}
	if p.Selection != nil {
		if err := p.Selection.verify(); err != nil {
			return false, err
		}
	} else if p.SelectionRoot != "" {
		if _, err := os.Lstat(p.SelectionRoot); !errors.Is(err, os.ErrNotExist) {
			return false, errors.New("the originally absent selected root changed during publication")
		}
	}
	return true, nil
}

func (j *scopedPushJournal) verifyEntries(checkSources bool) error {
	stack, err := LoadScopedPushStack(j.state.Plan.FeatureDir.Path)
	if err != nil {
		return err
	}
	for i, entry := range j.state.Plan.Entries {
		current := GetBranch(stack, entry.Name)
		matches := 0
		for _, candidate := range stack.Branches {
			if candidate.Name == entry.Name {
				matches++
			}
		}
		if matches != 1 || current.Name != entry.Name || current.GitBranch() != entry.GitBranch || current.Repo != entry.RepoToken ||
			current.Base != entry.Base || current.Archived != entry.Archived {
			return fmt.Errorf("selected metadata changed for %s; cancel this frozen push rather than retargeting it", entry.Name)
		}
		if entry.Archived {
			continue
		}
		if err := entry.Repository.verify(); err != nil {
			return err
		}
		if !checkSources || j.state.Rows[i] == "succeeded" {
			continue
		}
		source, err := scopedPushRef(entry.Repository.Directory.Path, entry.Destination)
		if err != nil || source != entry.Source {
			return errors.Join(err, fmt.Errorf("unconfirmed source changed for %s; cancel rather than publishing a different source", entry.Name))
		}
		if err := scopedPushCommit(entry.Repository.Directory.Path, entry.Source); err != nil {
			return err
		}
		if entry.Inclusion != nil {
			ok, err := scopedPushAncestor(entry.Repository.Directory.Path, entry.Expected, entry.Inclusion.Witness)
			if err != nil || !ok {
				return errors.Join(err, errors.New("the frozen inclusion witness is unavailable or invalid"))
			}
		}
		endpoint, err := scopedPushResolveEndpoint(entry.Repository.Directory.Path, entry.Destination)
		if err != nil || endpoint.Fingerprint != entry.Endpoint || endpoint.TrackingRef != entry.TrackingRef {
			return errors.Join(err, fmt.Errorf("origin endpoint/tracking configuration changed for %s", entry.Name))
		}
	}
	return nil
}

func (j *scopedPushJournal) protectObjects() error {
	for i, pin := range j.state.Plan.Pins {
		repo := j.state.Plan.Entries[pin.Entry].Repository
		if err := repo.verify(); err != nil {
			return err
		}
		if err := scopedPushRefDirectories(repo, pin.Ref); err != nil {
			return err
		}
		current, err := scopedPushRef(repo.Directory.Path, pin.Ref)
		if err != nil {
			return err
		}
		switch j.state.Pins[i] {
		case "held":
			if current != pin.OID {
				return errors.New("owned publication protection ref changed or disappeared")
			}
			continue
		case "pending":
			if current != "" {
				return errors.New("publication protection ref collision; refusing to adopt it")
			}
			j.state.Pins[i] = "creating"
			if err := j.save(); err != nil {
				return err
			}
		case "creating":
			if current != "" && current != pin.OID {
				return errors.New("publication protection ref has a foreign value")
			}
		default:
			return errors.New("unexpected object protection phase")
		}
		if err := scopedPushCommit(repo.Directory.Path, pin.OID); err != nil {
			return err
		}
		if current == "" {
			if _, err := scopedPushGit(repo.Directory.Path, "-c", "core.fsync=reference", "update-ref", "--no-deref",
				pin.Ref, pin.OID, strings.Repeat("0", len(pin.OID))); err != nil {
				return err
			}
		}
		if err := scopedPushCheckpoint("after-protect-object", i); err != nil {
			return err
		}
		if err := scopedPushSyncRefStorage(repo, pin); err != nil {
			return err
		}
		j.state.Pins[i] = "held"
		if err := j.save(); err != nil {
			return err
		}
	}
	j.state.Phase = "publishing"
	return j.save()
}

func (j *scopedPushJournal) cleanObjects() error {
	for i, pin := range j.state.Plan.Pins {
		repo := j.state.Plan.Entries[pin.Entry].Repository
		if err := repo.verify(); err != nil {
			return err
		}
		if err := scopedPushRefDirectories(repo, pin.Ref); err != nil {
			return err
		}
		current, err := scopedPushRef(repo.Directory.Path, pin.Ref)
		if err != nil {
			return err
		}
		if j.state.Pins[i] == "removed" {
			if current != "" {
				return errors.New("a removed publication ref was reoccupied; preserving foreign ref")
			}
			continue
		}
		if j.state.Pins[i] == "pending" && current != "" {
			return errors.New("publication private ref was never created by this run")
		}
		if current != "" && current != pin.OID {
			return errors.New("publication private ref value changed; cleanup refused")
		}
		if current == "" && j.state.Pins[i] == "held" {
			return errors.New("publication private ref disappeared without cleanup intent")
		}
		j.state.Pins[i] = "deleting"
		if err := j.save(); err != nil {
			return err
		}
		if err := scopedPushCheckpoint("before-remove-object", i); err != nil {
			return err
		}
		if err := errors.Join(repo.verify(), scopedPushRefDirectories(repo, pin.Ref)); err != nil {
			return err
		}
		rechecked, err := scopedPushRef(repo.Directory.Path, pin.Ref)
		if err != nil || rechecked != current {
			return errors.Join(err, errors.New("publication private ref changed before cleanup"))
		}
		if current != "" {
			if _, err := scopedPushGit(repo.Directory.Path, "-c", "core.fsync=reference", "update-ref", "--no-deref", "-d", pin.Ref, pin.OID); err != nil {
				return err
			}
		}
		if err := scopedPushCheckpoint("after-remove-object", i); err != nil {
			return err
		}
		if err := scopedPushSyncRefStorage(repo, pin); err != nil {
			return err
		}
		j.state.Pins[i] = "removed"
		if err := j.save(); err != nil {
			return err
		}
	}
	return nil
}

func (j *scopedPushJournal) clearPublishedFollowup(loc ScopedPushLocation) error {
	if err := j.prepareFollowupDecisions(); err != nil {
		return err
	}
	path := ReparentRemoteRecordPath(loc.ReparentLocation())
	snapshot, err := scopedPushReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return syncDir(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	decoded, err := decodeReparentRemoteRecordBytes(loc.ReparentLocation(), snapshot.data)
	if err != nil {
		return err
	}
	record := &decoded
	var stack Stack
	for _, decision := range j.state.FollowupDecisions {
		if decision == "clear" {
			stack, err = LoadScopedPushStack(loc.FeaturePath)
			if err != nil {
				return err
			}
			break
		}
	}
	changed := false
	for i, entry := range j.state.Plan.Entries {
		if j.state.Rows[i] != "succeeded" || entry.Followup == nil || j.state.FollowupDecisions[entry.Name] != "clear" {
			continue
		}
		current := GetBranch(stack, entry.Name)
		if current.Name == "" || current.GitBranch() != entry.GitBranch || current.Repo != entry.RepoToken {
			return errors.New("selected reparent follow-up mapping changed")
		}
		for r := range record.Entries {
			row := &record.Entries[r]
			if row.Name != entry.Name || !row.Pending() {
				continue
			}
			if *row != *entry.Followup {
				return errors.New("selected reparent follow-up changed; preserving it")
			}
			row.State = ReparentRemoteStateCleared
			changed = true
		}
	}
	if !changed {
		return errors.Join(scopedPushSyncRegular(path), syncDir(filepath.Dir(path)))
	}
	if err := scopedPushCheckpoint("before-followup-clear", 0); err != nil {
		return err
	}
	if err := snapshot.verify(path); err != nil {
		return err
	}
	if record.AllCleared() {
		return RemoveReparentRemoteRecord(loc.ReparentLocation())
	}
	return SaveReparentRemoteRecord(loc.ReparentLocation(), *record)
}

func (j *scopedPushJournal) prepareFollowupDecisions() error {
	decisions := make(map[string]string, len(j.state.FollowupDecisions))
	for name, decision := range j.state.FollowupDecisions {
		decisions[name] = decision
	}
	changed := false
	for i, entry := range j.state.Plan.Entries {
		if entry.Followup == nil || decisions[entry.Name] != "" {
			continue
		}
		decision := "keep"
		if j.state.Rows[i] == "succeeded" {
			if err := entry.Repository.verify(); err != nil {
				return err
			}
			for p, pin := range j.state.Plan.Pins {
				if pin.Entry != i || (pin.Kind != "source" && pin.Kind != "followup") {
					continue
				}
				value, err := scopedPushRef(entry.Repository.Directory.Path, pin.Ref)
				if err != nil || j.state.Pins[p] != "held" || value != pin.OID {
					return errors.Join(err, errors.New("follow-up decision requires its owned source and rewrite-tip protection"))
				}
			}
			contains, err := scopedPushAncestor(entry.Repository.Directory.Path, entry.Followup.NewTipSHA, entry.Source)
			if err != nil {
				return err
			}
			if contains {
				decision = "clear"
			}
		}
		decisions[entry.Name] = decision
		changed = true
	}
	if !changed {
		return nil
	}
	if err := scopedPushCheckpoint("before-followup-decisions", 0); err != nil {
		return err
	}
	j.state.FollowupDecisions = decisions
	return j.save()
}

func (j *scopedPushJournal) publish(opts ScopedPushOptions) error {
	for i, entry := range j.state.Plan.Entries {
		if entry.Archived {
			fmt.Fprintf(opts.Out, "  [-] %s (archived, skipped)\n", entry.Name) //nolint:errcheck
			continue
		}
		if j.state.Rows[i] == "succeeded" {
			fmt.Fprintf(opts.Out, "  [=] %s (already confirmed; not republished)\n", entry.Name) //nolint:errcheck
			continue
		}
		if err := scopedPushCheckpoint("before-entry", i); err != nil {
			return err
		}
		if err := j.verifyBeforeEffect(opts); err != nil {
			return err
		}
		if err := j.verifyEntries(true); err != nil {
			return err
		}
		endpoint, err := scopedPushResolveEndpoint(entry.Repository.Directory.Path, entry.Destination)
		if err != nil || endpoint.Fingerprint != entry.Endpoint {
			return errors.Join(err, errors.New("publication endpoint changed before network attempt"))
		}
		if j.state.Rows[i] == "intent" {
			actual, err := scopedPushRemoteValue(entry.Repository.Directory.Path, endpoint.URL, entry.Destination)
			if err != nil {
				return err
			}
			switch actual {
			case entry.Source:
				j.state.Rows[i] = "succeeded"
				if err := j.save(); err != nil {
					return err
				}
				fmt.Fprintf(opts.Out, "  [+] %s (confirmed recorded source; not republished)\n", entry.Name) //nolint:errcheck
				continue
			case entry.Expected:
			default:
				return fmt.Errorf("%s: recorded remote destination differs from both source and original lease; refusing to overwrite later work", entry.Name)
			}
		} else {
			j.state.Rows[i] = "intent"
		}
		// Also repeat this durable barrier on a retry. A remote read never
		// replaces the original expected-value lease.
		if err := j.save(); err != nil {
			return err
		}
		if entry.Followup != nil {
			fmt.Fprintln(opts.Err, ReparentPushDecisionFor(&ReparentRemoteRecord{Entries: []ReparentRemoteEntry{*entry.Followup}}, entry.Name).WarnLine) //nolint:errcheck
		}
		if err := j.verifyBeforeEffect(opts); err != nil {
			return err
		}
		if err := scopedPushCheckpoint("before-push", i); err != nil {
			return err
		}
		if err := j.verifyBeforeEffect(opts); err != nil {
			return err
		}
		if err := j.verifyEntries(true); err != nil {
			return err
		}
		currentEndpoint, err := scopedPushResolveEndpoint(entry.Repository.Directory.Path, entry.Destination)
		if err != nil || currentEndpoint.Fingerprint != entry.Endpoint {
			return errors.Join(err, errors.New("publication endpoint changed immediately before push"))
		}
		if err := scopedPushPublish(entry.Repository.Directory.Path, endpoint.URL, entry); err != nil {
			return fmt.Errorf("%s: %w; run: tws push %s --continue (or --cancel)", entry.Name, err, opts.Location.Feature)
		}
		if err := scopedPushCheckpoint("after-push", i); err != nil {
			return err
		}
		j.state.Rows[i] = "succeeded"
		if err := j.save(); err != nil {
			return fmt.Errorf("record accepted push for %s: %w", entry.Name, err)
		}
		fmt.Fprintf(opts.Out, "  [+] %s (pushed)\n", entry.Name) //nolint:errcheck
	}
	j.state.Phase = "cleaning"
	return j.save()
}

func (j *scopedPushJournal) verifyBeforeEffect(opts ScopedPushOptions) error {
	if opts.Verify != nil {
		if err := opts.Verify(); err != nil {
			return err
		}
	}
	if err := j.verifyBindings(opts.Location); err != nil {
		return err
	}
	if j.state.Phase == "publishing" {
		for i, pin := range j.state.Plan.Pins {
			repo := j.state.Plan.Entries[pin.Entry].Repository
			if err := repo.verify(); err != nil {
				return err
			}
			current, err := scopedPushRef(repo.Directory.Path, pin.Ref)
			if err != nil || j.state.Pins[i] != "held" || current != pin.OID {
				return errors.Join(err, errors.New("frozen publication object protection changed"))
			}
		}
	}
	return nil
}

// RunScopedPush never constructs a sync transaction. A completed/canceled
// journal remains a strict terminal receipt, with no private refs or recovery
// authority. Retaining it makes the final durability barrier replayable.
func RunScopedPush(opts ScopedPushOptions) (retErr error) {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Err == nil {
		opts.Err = io.Discard
	}
	loc := opts.Location
	path := ScopedPushStatePath(loc)
	if loc.Mode == ModeCheckout {
		if info, err := os.Lstat(filepath.Dir(path)); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("checkout publication state must use a physical state directory, not a symlink")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	snapshot, err := scopedPushReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var prior *ScopedPushState
	if snapshot.info != nil {
		prior, err = scopedPushDecode(snapshot.data)
		if err != nil {
			return err
		}
	}
	var state *ScopedPushState
	recovery := opts.Continue || opts.Cancel
	if recovery {
		if prior == nil {
			return errors.New("no matching standalone publication journal to recover")
		}
		state = prior
		if state.Phase == "canceling" || state.Phase == "canceled" {
			if !opts.Cancel && !opts.DryRun {
				return errors.New("this publication was canceled; use --cancel to finish owned cleanup")
			}
		}
	} else {
		if prior != nil && !prior.Terminal() {
			return RefusePendingScopedPush(loc)
		}
		state, err = BuildScopedPushPlan(opts)
		if err != nil {
			return err
		}
	}
	journal := &scopedPushJournal{path: path, snapshot: snapshot, state: state, verify: opts.Verify}
	if recovery {
		if err := journal.verifyBindings(loc); err != nil {
			return err
		}
	}
	if opts.DryRun {
		PrintScopedPushPlan(opts.Out, state)
		return nil
	}
	verifyGuard, release, err := acquireScopedPushGuard(loc, state.Plan.Token, recovery)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, release()) }()
	verifyAdmission := opts.Verify
	opts.Verify = func() error {
		if verifyAdmission != nil {
			if err := verifyAdmission(); err != nil {
				return err
			}
		}
		return verifyGuard()
	}
	journal.verify = opts.Verify
	if err := scopedPushCheckpoint("after-guard", 0); err != nil {
		return err
	}
	if err := scopedPushCheckOtherState(loc); err != nil {
		return err
	}
	if recovery {
		if err := journal.sync(); err != nil {
			return err
		}
	} else {
		if err := snapshot.verify(path); err != nil {
			return err
		}
		if prior != nil {
			if prior.Plan.FeatureDir.Identity != state.Plan.FeatureDir.Identity || prior.Plan.Mode != state.Plan.Mode {
				return errors.New("refusing to replace a foreign terminal publication receipt")
			}
			// A terminal receipt has no publication authority. Its feature
			// may have been legitimately renamed since completion.
			parent, err := scopedPushDirectoryAt(filepath.Dir(path))
			if err != nil {
				return err
			}
			if err := scopedPushSyncSnapshot(path, snapshot, parent, state.Plan.Root, prior.Revision); err != nil {
				return err
			}
		}
		state.Plan.StateDirectory, err = scopedPushDirectoryAt(filepath.Dir(path))
		if err != nil {
			return err
		}
		state.PlanHash = scopedPushPlanHash(state.Plan)
		if err := journal.verifyEntries(true); err != nil {
			return err
		}
		if err := journal.save(); err != nil {
			return err
		}
	}
	if err := journal.verifyBeforeEffect(opts); err != nil {
		return err
	}
	if state.Terminal() {
		fmt.Fprintf(opts.Out, "Publication %s; owned cleanup verified. No remote ref was rolled back.\n", state.Phase) //nolint:errcheck
		return nil
	}
	if opts.Cancel {
		state.Phase = "canceling"
		if err := journal.save(); err != nil {
			return err
		}
		for i, entry := range state.Plan.Entries {
			fmt.Fprintf(opts.Out, "  %s: %s\n", entry.Name, state.Rows[i]) //nolint:errcheck
		}
	} else {
		if err := journal.verifyEntries(true); err != nil {
			return err
		}
		if state.Phase == "preparing" {
			if err := journal.protectObjects(); err != nil {
				return err
			}
		}
		if state.Phase == "publishing" {
			if err := journal.publish(opts); err != nil {
				return err
			}
		}
	}
	if err := journal.verifyBeforeEffect(opts); err != nil {
		return err
	}
	// Cancellation may clear confirmed selected results, but never uncertain
	// effects or another row. It performs no remote observation or mutation.
	if err := journal.clearPublishedFollowup(loc); err != nil {
		return err
	}
	if err := journal.cleanObjects(); err != nil {
		return err
	}
	state.Phase = "completed"
	if opts.Cancel {
		state.Phase = "canceled"
	}
	if err := journal.save(); err != nil {
		return err
	}
	if opts.Cancel {
		fmt.Fprintln(opts.Out, "Publication canceled: remaining entries abandoned; confirmed/uncertain remote effects were not rolled back.") //nolint:errcheck
	} else {
		fmt.Fprintln(opts.Out, "Selected publication completed; local branches and checkout unchanged.") //nolint:errcheck
	}
	return nil
}
