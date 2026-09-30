package internal

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func syncTransactionAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}

func validateSyncTransactionRelationships(tx *SyncTransaction, before []byte) error {
	invalid := func(detail string) error { return fmt.Errorf("invalid sync transaction evidence: %s", detail) }
	if _, err := time.Parse(time.RFC3339, tx.CreatedAt); err != nil {
		return invalid("created_at")
	}
	if tx.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, tx.UpdatedAt); err != nil {
			return invalid("updated_at")
		}
	}
	if !syncTransactionAbsolutePath(tx.Metadata.Path) || filepath.Base(tx.Metadata.Path) != "stack.yaml" {
		return invalid("metadata path")
	}
	if tx.Metadata.IntentBase64 != "" || tx.Metadata.IntentSHA256 != "" || tx.Metadata.IntentPending {
		intent, err := base64.StdEncoding.DecodeString(tx.Metadata.IntentBase64)
		if err != nil || syncTransactionHash(intent) != tx.Metadata.IntentSHA256 {
			return invalid("metadata intent")
		}
	}
	if tx.Ready && tx.Phase == SyncTxnPreparing || !tx.Ready && len(tx.Actions) != 0 {
		return invalid("readiness disagrees with progress")
	}
	switch tx.EvidenceVersion {
	case SyncTransactionLegacyEvidenceVersion:
		if tx.ParentAttributionFrom != 0 {
			return invalid("legacy parent attribution boundary")
		}
	case SyncTransactionEvidenceVersion:
		if tx.ParentAttributionFrom < 1 || tx.ParentAttributionFrom > len(tx.Actions)+1 {
			return invalid("parent attribution boundary")
		}
	}
	rollingBack := tx.Rollback.StartedAt != ""
	if tx.Completion != "" {
		if tx.Completion != "forward-complete" && tx.Completion != "cancelled-before-mutation" {
			return invalid("unknown completion decision")
		}
		if tx.Phase != SyncTxnCleanup || rollingBack {
			return invalid("completion must be forward cleanup only")
		}
		if tx.Completion == "cancelled-before-mutation" && (len(tx.Actions) != 0 || tx.Publication.IntentDurable ||
			tx.Metadata.ExpectedSHA256 != tx.Metadata.BeforeSHA256) {
			return invalid("cancelled transaction contains effects")
		}
	} else if tx.Phase == SyncTxnCleanup && !rollingBack {
		return invalid("cleanup lacks a durable completion decision")
	}
	if rollingBack {
		if _, err := time.Parse(time.RFC3339, tx.Rollback.StartedAt); err != nil {
			return invalid("rollback timestamp")
		}
		if tx.Phase != SyncTxnRollingBack && tx.Phase != SyncTxnCleanup {
			return invalid("rollback must remain forward-disabled")
		}
	} else if tx.Phase == SyncTxnRollingBack || tx.Rollback.Stage != "" ||
		tx.Rollback.MetadataRestored || len(tx.Rollback.HoldersRestored) != 0 || len(tx.Rollback.CompletedRepos) != 0 {
		return invalid("rollback progress without rollback intent")
	}
	if tx.Publication.IntentDurable {
		if _, err := time.Parse(time.RFC3339, tx.Publication.StartedAt); err != nil || rollingBack {
			return invalid("publication and rollback are mutually exclusive")
		}
	} else if tx.Phase == SyncTxnPublished || tx.Publication.StartedAt != "" ||
		tx.Publication.CurrentEntry != "" || tx.Publication.Current != nil ||
		len(tx.Publication.Intents) != 0 || len(tx.Publication.Results) != 0 {
		return invalid("publication result without publication intent")
	}
	if (tx.Publication.Current == nil) != (tx.Publication.CurrentEntry == "") {
		return invalid("publication current intent")
	}
	if tx.Metadata.Restored != tx.Rollback.MetadataRestored ||
		tx.Metadata.Restored && tx.Metadata.ExpectedSHA256 != tx.Metadata.BeforeSHA256 {
		return invalid("metadata restoration progress")
	}
	var stack Stack
	if err := yaml.Unmarshal(before, &stack); err != nil {
		return invalid("metadata preimage is not a stack")
	}
	entries := make(map[string]string, len(stack.Branches))
	stackEntries := make(map[string]StackEntry, len(stack.Branches))
	for _, entry := range stack.Branches {
		if entry.Name == "" || entries[entry.Name] != "" {
			return invalid("ambiguous stack entry identity")
		}
		entries[entry.Name] = "refs/heads/" + entry.GitBranch()
		stackEntries[entry.Name] = entry
	}
	selected := make(map[string]bool, len(tx.Selected))
	for _, name := range tx.Selected {
		if name == "" || selected[name] || entries[name] == "" {
			return invalid("selected names")
		}
		selected[name] = true
	}
	repos := make(map[string]*SyncTransactionRepo, len(tx.Repositories))
	type selectedRefEvidence struct {
		commonDir string
		ref       string
		preimage  string
	}
	selectedRefs := make(map[string]selectedRefEvidence, len(selected))
	holders := map[string]bool{}
	attributed := map[string]bool{}
	for i := range tx.Repositories {
		repo := &tx.Repositories[i]
		repos[repo.CommonDir] = repo
		for _, ref := range repo.Refs {
			if ref.Moved != (ref.ExpectedSHA != ref.PreimageSHA) ||
				ref.Restored && (!rollingBack || ref.ExpectedSHA != ref.PreimageSHA) ||
				repo.RefsRestored && !ref.Restored {
				return invalid("ref movement/restoration progress")
			}
			if repo.PinsRemoved && (ref.Pinned || ref.PinDeletePending) ||
				ref.PinDeletePending && tx.Phase != SyncTxnCleanup {
				return invalid("pin deletion progress")
			}
			if tx.Ready && tx.Phase != SyncTxnCleanup && !ref.Pinned {
				return invalid("ready transaction lost a preimage pin")
			}
			for _, name := range ref.SelectedNames {
				if !selected[name] || attributed[name] || entries[name] != ref.Ref {
					return invalid("selected branch attribution")
				}
				attributed[name] = true
				selectedRefs[name] = selectedRefEvidence{commonDir: repo.CommonDir, ref: ref.Ref, preimage: ref.PreimageSHA}
			}
		}
		for _, holder := range repo.Holders {
			if !syncTransactionAbsolutePath(holder.Path) || holders[holder.Path] ||
				!reparentStateOID(holder.OriginalHEAD, repo.OIDWidth) ||
				!reparentStateOID(holder.ExpectedHEAD, repo.OIDWidth) {
				return invalid("holder identity")
			}
			holders[holder.Path] = true
			if !holder.OriginalDetached && (holder.OriginalHeadPinned || holder.HeadPinDeletePending) ||
				holder.HeadPinDeletePending && tx.Phase != SyncTxnCleanup ||
				repo.PinsRemoved && (holder.OriginalHeadPinned || holder.HeadPinDeletePending) {
				return invalid("original detached HEAD pin progress")
			}
			if tx.Ready && tx.Phase != SyncTxnCleanup && holder.OriginalDetached && !holder.OriginalHeadPinned {
				return invalid("ready transaction lost a detached HEAD pin")
			}
			if target := holder.ForwardRestoreTarget; target != nil {
				if target.Path != holder.Path || target.Branch != holder.OriginalBranch ||
					target.Detached != holder.OriginalDetached ||
					!reparentStateOID(target.HEAD, repo.OIDWidth) {
					return invalid("forward checkout restoration intent")
				}
			}
			for _, position := range []struct {
				branch   string
				detached bool
			}{{holder.OriginalBranch, holder.OriginalDetached}, {holder.ExpectedBranch, holder.ExpectedDetached}} {
				if position.detached != (position.branch == "") ||
					!position.detached && !reparentStateRef("refs/heads/"+position.branch) {
					return invalid("holder attachment")
				}
			}
			if (holder.DetachPending || holder.RestorePending || holder.Restored || holder.Detached) && !rollingBack {
				return invalid("holder rollback progress before rollback intent")
			}
		}
	}
	if len(attributed) != len(selected) {
		return invalid("selected branch lacks a preimage")
	}
	if !tx.CutoffsReady && len(tx.Cutoffs) != 0 {
		return invalid("cutoff rows exist before cutoff readiness")
	}
	cutoffByEntry := make(map[string]SyncCutoffDecision, len(tx.Cutoffs))
	if tx.CutoffsReady {
		if len(tx.Cutoffs) != len(selected) {
			return invalid("cutoff evidence does not cover the selection")
		}
		seenCutoff := map[string]bool{}
		for _, cutoff := range tx.Cutoffs {
			repo := repos[cutoff.RepoCommonDir]
			stackEntry := stackEntries[cutoff.Entry]
			refEvidence := selectedRefs[cutoff.Entry]
			if repo == nil || !selected[cutoff.Entry] || seenCutoff[cutoff.Entry] ||
				cutoff.GitBranch != stackEntry.GitBranch() || cutoff.ChildRef != entries[cutoff.Entry] ||
				cutoff.RepoCommonDir != refEvidence.commonDir || cutoff.ChildRef != refEvidence.ref ||
				cutoff.ChildSHA != refEvidence.preimage || cutoff.Recorded != stackEntry.LastBaseSHA ||
				cutoff.ConfiguredBase != stackEntry.Base ||
				!reparentStateOID(cutoff.ChildSHA, repo.OIDWidth) ||
				strings.ContainsAny(cutoff.Recorded, "\x00\r\n") || len(cutoff.Recorded) > 1024 {
				return invalid("cutoff identity")
			}
			seenCutoff[cutoff.Entry] = true
			cutoffByEntry[cutoff.Entry] = cutoff
			parentEntry := ""
			if parent := stackEntries[stackEntry.Base]; parent.Name != "" {
				if WorkspaceMode(tx.WorkspaceMode) == ModeCheckout || SameStackRepo(parent.Repo, stackEntry.Repo) {
					parentEntry = parent.Name
				}
			}
			if cutoff.ParentEntry != parentEntry {
				return invalid("cutoff parent identity")
			}
			if parentEntry != "" {
				if cutoff.ParentRef != stackEntries[parentEntry].GitBranch() {
					return invalid("cutoff parent ref")
				}
			} else if cutoff.Applicable && cutoff.ParentRef != cutoff.ConfiguredBase &&
				cutoff.ParentRef != "origin/"+cutoff.ConfiguredBase {
				return invalid("cutoff literal parent ref")
			}
			switch cutoff.Validity {
			case SyncCutoffValid:
				if !cutoff.Applicable {
					return invalid("applicable cutoff decision")
				}
				if cutoff.ParentRef == "" || strings.ContainsAny(cutoff.ParentRef, "\x00\r\n") ||
					len(cutoff.ParentRef) > 1024 ||
					!reparentStateOID(cutoff.ParentSHA, repo.OIDWidth) ||
					!reparentStateOID(cutoff.EffectiveSHA, repo.OIDWidth) {
					return invalid("cutoff object ids")
				}
				if cutoff.SharedSHA != "" && !reparentStateOID(cutoff.SharedSHA, repo.OIDWidth) {
					return invalid("cutoff shared history")
				}
				switch cutoff.Source {
				case SyncCutoffSourceRecorded:
					if cutoff.Recorded == "" || !reparentStateOID(cutoff.RecordedSHA, repo.OIDWidth) ||
						cutoff.EffectiveSHA != cutoff.RecordedSHA ||
						cutoff.Reason != SyncCutoffReasonValidRecorded {
						return invalid("recorded cutoff decision")
					}
				case SyncCutoffSourceParentTip:
					if cutoff.Recorded != "" || cutoff.RecordedSHA != "" ||
						cutoff.EffectiveSHA != cutoff.ParentSHA ||
						cutoff.Reason != SyncCutoffReasonValidParentTip {
						return invalid("missing cutoff decision")
					}
				default:
					return invalid("cutoff source")
				}
			case SyncCutoffNotApplicable:
				if cutoff.Applicable || cutoff.Source != SyncCutoffSourceNone || cutoff.EffectiveSHA != "" ||
					cutoff.Reason != SyncCutoffReasonNotApplicable {
					return invalid("not-applicable cutoff decision")
				}
			default:
				return invalid("cutoff validity")
			}
		}
	}
	if mode := WorkspaceMode(tx.WorkspaceMode); mode == ModeCheckout && len(repos) > 1 {
		return invalid("checkout transaction spans repositories")
	}
	validateRepoList := func(list []string, predicate func(*SyncTransactionRepo) bool) bool {
		seen := map[string]bool{}
		for _, common := range list {
			if repos[common] == nil || seen[common] || !predicate(repos[common]) {
				return false
			}
			seen[common] = true
		}
		return true
	}
	if !validateRepoList(tx.Rollback.CompletedRepos, func(repo *SyncTransactionRepo) bool { return repo.RefsRestored }) ||
		!validateRepoList(tx.Rollback.PinsRemovedRepos, func(repo *SyncTransactionRepo) bool { return repo.PinsRemoved }) {
		return invalid("repository cleanup progress")
	}
	for _, path := range tx.Rollback.HoldersRestored {
		if !holders[path] {
			return invalid("unknown restored holder")
		}
	}
	refHistory := make(map[string]map[string]string, len(tx.Repositories))
	for _, repo := range tx.Repositories {
		refHistory[repo.CommonDir] = map[string]string{}
		for _, ref := range repo.Refs {
			refHistory[repo.CommonDir][ref.Ref] = ref.PreimageSHA
		}
	}
	for i, action := range tx.Actions {
		repo := repos[action.RepoCommonDir]
		if action.Sequence != i+1 || action.Kind != "rebase" || repo == nil ||
			!selected[action.Entry] || !syncTransactionAbsolutePath(action.ContextPath) ||
			!holders[action.ContextPath] {
			return invalid("action identity")
		}
		if action.Status != SyncTxnActionIntent && action.Status != SyncTxnActionObserved && action.Status != SyncTxnActionFailed ||
			action.Status == SyncTxnActionIntent && i != len(tx.Actions)-1 {
			return invalid("action status")
		}
		if (action.DestinationSHA == "") != (action.CutoffSHA == "") ||
			action.DestinationSHA != "" && (!reparentStateOID(action.DestinationSHA, repo.OIDWidth) ||
				!reparentStateOID(action.CutoffSHA, repo.OIDWidth)) {
			return invalid("action replay boundary")
		}
		refs := map[string]bool{}
		for _, ref := range repo.Refs {
			refs[ref.Ref] = true
		}
		allowed := map[string]bool{}
		for _, ref := range action.AllowedRefs {
			if !refs[ref] || allowed[ref] || action.RefLogAnchors[ref] == "" {
				return invalid("action ref ownership")
			}
			allowed[ref] = true
		}
		if !allowed[entries[action.Entry]] || action.ContextReflogAnchor == "" {
			return invalid("action attribution evidence")
		}
		if action.ContextBefore.Path != action.ContextPath ||
			action.ContextBefore != syncTransactionHolderMap(action.BeforeHolders)[action.ContextPath] ||
			action.ContextRef != "" && !allowed[action.ContextRef] {
			return invalid("action context transition")
		}
		repoHolders := map[string]bool{}
		for _, holder := range repo.Holders {
			repoHolders[holder.Path] = true
		}
		if !repoHolders[action.ContextPath] {
			return invalid("action context belongs to another repository")
		}
		for imageIndex, image := range [][]SyncTransactionHolderValue{action.BeforeHolders, action.AfterHolders} {
			if imageIndex == 1 && image == nil && action.Status == SyncTxnActionIntent {
				continue
			}
			seen := map[string]bool{}
			for _, holder := range image {
				if !repoHolders[holder.Path] || seen[holder.Path] ||
					!reparentStateOID(holder.HEAD, repo.OIDWidth) ||
					holder.Detached != (holder.Branch == "") ||
					!holder.Detached && !reparentStateRef("refs/heads/"+holder.Branch) {
					return invalid("action holder image")
				}
				seen[holder.Path] = true
			}
			if len(seen) != len(repoHolders) {
				return invalid("incomplete action holder image")
			}
		}
		for imageIndex, image := range [][]SyncTransactionRefValue{action.BeforeRefs, action.AfterRefs} {
			if imageIndex == 1 && image == nil && action.Status == SyncTxnActionIntent {
				continue
			}
			seen := map[string]bool{}
			for _, ref := range image {
				if !refs[ref.Ref] || seen[ref.Ref] || !reparentStateOID(ref.SHA, repo.OIDWidth) {
					return invalid("action ref image")
				}
				seen[ref.Ref] = true
				if imageIndex == 0 && ref.SHA != refHistory[repo.CommonDir][ref.Ref] {
					return invalid("action preimage disagrees with prior progress")
				}
				if imageIndex == 1 {
					if ref.SHA != refHistory[repo.CommonDir][ref.Ref] && !allowed[ref.Ref] {
						return invalid("action postimage changes an unowned ref")
					}
					refHistory[repo.CommonDir][ref.Ref] = ref.SHA
				}
			}
			if len(seen) != len(refs) {
				return invalid("incomplete action ref image")
			}
		}
		attributionRequired := tx.EvidenceVersion == SyncTransactionEvidenceVersion &&
			action.Sequence >= tx.ParentAttributionFrom
		if action.ParentDestinationsReady != attributionRequired ||
			!action.ParentDestinationsReady && len(action.ParentDestinations) != 0 {
			return invalid("action parent attribution readiness")
		}
		if action.ParentDestinationsReady {
			expected := map[string]bool{}
			for _, cutoff := range tx.Cutoffs {
				if cutoff.Entry != action.Entry && cutoff.RepoCommonDir == action.RepoCommonDir && allowed[cutoff.ChildRef] {
					expected[cutoff.Entry] = true
				}
			}
			if len(action.ParentDestinations) != len(expected) {
				return invalid("action parent attribution coverage")
			}
			beforeValues := syncTransactionValuesMap(action.BeforeRefs)
			afterValues := syncTransactionValuesMap(action.AfterRefs)
			seenParents := map[string]bool{}
			for _, parent := range action.ParentDestinations {
				cutoff, ok := cutoffByEntry[parent.Entry]
				if !ok || !expected[parent.Entry] || seenParents[parent.Entry] ||
					parent.ParentEntry != cutoff.ParentEntry || parent.ParentRef != cutoff.ParentRef ||
					!reparentStateOID(parent.ActionSHA, repo.OIDWidth) ||
					strings.ContainsAny(parent.ParentRef, "\x00\r\n") || len(parent.ParentRef) > 1024 {
					return invalid("action parent attribution identity")
				}
				seenParents[parent.Entry] = true
				if cutoff.ParentEntry != "" {
					parentCutoff, ok := cutoffByEntry[cutoff.ParentEntry]
					if !ok || parent.Kind != "stack-entry" || parent.CanonicalRef != parentCutoff.ChildRef {
						return invalid("action stack parent attribution")
					}
				} else {
					if reparentStateRef(cutoff.ParentRef) && parent.CanonicalRef != cutoff.ParentRef {
						return invalid("action explicit parent ref attribution")
					}
					switch parent.Kind {
					case "object":
						if parent.CanonicalRef != "" {
							return invalid("action object parent attribution")
						}
						if reparentStateOID(cutoff.ParentRef, repo.OIDWidth) &&
							strings.ToLower(cutoff.ParentRef) != parent.ActionSHA {
							return invalid("action object parent identity")
						}
					case "local-branch":
						if !strings.HasPrefix(parent.CanonicalRef, "refs/heads/") {
							return invalid("action local parent attribution")
						}
					case "remote-tracking":
						if !strings.HasPrefix(parent.CanonicalRef, "refs/remotes/") {
							return invalid("action remote parent attribution")
						}
					case "tag":
						if !strings.HasPrefix(parent.CanonicalRef, "refs/tags/") {
							return invalid("action tag parent attribution")
						}
					case "ref":
						if !strings.HasPrefix(parent.CanonicalRef, "refs/") ||
							strings.HasPrefix(parent.CanonicalRef, "refs/heads/") ||
							strings.HasPrefix(parent.CanonicalRef, "refs/remotes/") ||
							strings.HasPrefix(parent.CanonicalRef, "refs/tags/") {
							return invalid("action literal ref attribution")
						}
					default:
						return invalid("action parent attribution kind")
					}
				}
				if strings.HasPrefix(parent.CanonicalRef, "refs/heads/") {
					if beforeValues[parent.CanonicalRef] != parent.ActionSHA {
						return invalid("action local parent preimage")
					}
				}
				expectedMove := strings.HasPrefix(parent.CanonicalRef, "refs/heads/") &&
					allowed[parent.CanonicalRef] &&
					(parent.CanonicalRef == action.ContextRef ||
						!syncActionRefHeld(parent.CanonicalRef, action.BeforeHolders))
				if parent.MovesWithAction != expectedMove {
					return invalid("action parent movement attribution")
				}
				if parent.MovesWithAction {
					if cutoff.EffectiveSHA != parent.ActionSHA {
						return invalid("action moving parent cutoff transition")
					}
				} else {
					if cutoff.EffectiveSHA != action.CutoffSHA {
						return invalid("action collateral cutoff transition")
					}
					switch parent.Kind {
					case "object", "tag":
					default:
						if parent.ActionSHA != action.DestinationSHA {
							return invalid("action nonmoving parent destination")
						}
					}
				}
				if action.Status == SyncTxnActionIntent {
					if parent.DestinationSHA != "" {
						return invalid("pending action parent destination")
					}
				} else {
					if !reparentStateOID(parent.DestinationSHA, repo.OIDWidth) {
						return invalid("action parent destination")
					}
					if parent.MovesWithAction {
						if afterValues[parent.CanonicalRef] != parent.DestinationSHA {
							return invalid("action local parent destination")
						}
					} else if parent.DestinationSHA != action.DestinationSHA {
						return invalid("action collateral transition destination")
					}
				}
			}
		}
	}
	for _, repo := range tx.Repositories {
		for _, ref := range repo.Refs {
			expected := refHistory[repo.CommonDir][ref.Ref]
			if ref.Restored {
				expected = ref.PreimageSHA
			}
			if ref.ExpectedSHA != expected {
				return invalid("expected ref does not follow recorded actions")
			}
		}
	}
	for i, validation := range tx.Validations {
		repo := repos[validation.RepoCommonDir]
		if validation.Sequence != i+1 || repo == nil || !selected[validation.Entry] ||
			!syncTransactionAbsolutePath(validation.ContextPath) || !holders[validation.ContextPath] {
			return invalid("validation identity")
		}
		switch validation.Status {
		case SyncTxnActionIntent:
			if i != len(tx.Validations)-1 {
				return invalid("pending validation is not last")
			}
		case SyncTxnActionObserved, SyncTxnActionFailed, SyncTxnActionMutated:
		default:
			return invalid("validation status")
		}
		for imageIndex, image := range [][]SyncTransactionRefValue{validation.BeforeRefs, validation.AfterRefs} {
			if imageIndex == 1 && image == nil && validation.Status == SyncTxnActionIntent {
				continue
			}
			seen := map[string]bool{}
			for _, value := range image {
				if seen[value.Ref] || !reparentStateOID(value.SHA, repo.OIDWidth) {
					return invalid("validation ref image")
				}
				known := false
				for _, ref := range repo.Refs {
					if ref.Ref == value.Ref {
						known = true
						break
					}
				}
				if !known {
					return invalid("validation ref image")
				}
				seen[value.Ref] = true
			}
			if len(seen) != len(repo.Refs) {
				return invalid("incomplete validation ref image")
			}
		}
		for imageIndex, image := range [][]SyncTransactionHolderValue{validation.BeforeHolders, validation.AfterHolders} {
			if imageIndex == 1 && image == nil && validation.Status == SyncTxnActionIntent {
				continue
			}
			seen := map[string]bool{}
			for _, holder := range image {
				if seen[holder.Path] || !holders[holder.Path] ||
					!reparentStateOID(holder.HEAD, repo.OIDWidth) ||
					holder.Detached != (holder.Branch == "") {
					return invalid("validation holder image")
				}
				seen[holder.Path] = true
			}
			if len(seen) != len(repo.Holders) {
				return invalid("incomplete validation holder image")
			}
		}
		if validation.Status == SyncTxnActionMutated &&
			reflect.DeepEqual(validation.BeforeRefs, validation.AfterRefs) &&
			reflect.DeepEqual(validation.BeforeHolders, validation.AfterHolders) {
			return invalid("validation mutation has no changed image")
		}
	}
	intents := map[string]SyncTransactionPushIntent{}
	for _, intent := range tx.Publication.Intents {
		repo := repos[intent.RepoCommonDir]
		if !selected[intent.Entry] || repo == nil || intents[intent.Entry].Entry != "" ||
			!reparentStateRef(intent.DestinationRef) ||
			!reparentStateOID(intent.SourceSHA, repo.OIDWidth) {
			return invalid("publication intent")
		}
		found := false
		for _, ref := range repo.Refs {
			if ref.Ref == intent.DestinationRef && ref.ExpectedSHA == intent.SourceSHA {
				found = true
				break
			}
		}
		if !found {
			return invalid("publication source")
		}
		intents[intent.Entry] = intent
	}
	if tx.Publication.Current != nil {
		if tx.Publication.Current.Entry != tx.Publication.CurrentEntry ||
			intents[tx.Publication.Current.Entry] != *tx.Publication.Current {
			return invalid("publication current intent")
		}
	}
	for _, result := range tx.Publication.Results {
		if intents[result.Entry].Entry == "" {
			return invalid("publication result without bound intent")
		}
	}
	return nil
}
