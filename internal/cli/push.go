package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func pushCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "push <feature>",
		Short: "Push all branches in a feature with --force-with-lease",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")
			feature := args[0]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}
			// The sibling-space guard used to arrive through
			// internal.RequireFeaturePath; the external arm no longer calls it,
			// so the command owns the guard explicitly, before any layout work.
			if err := internal.GuardFeatureName(ws.MetadataRoot, feature); err != nil {
				return err
			}
			if ws.Mode == internal.ModeCheckout {
				return pushFeatureCheckout(feature, dryRun)
			}
			layout, err := resolveExternalSyncLayout(ws, internal.TwsRoot(), feature)
			if err != nil {
				return err
			}
			return pushFeatureSerialized(feature, layout, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be pushed without pushing")

	return cmd
}

// externalReparentLocation is the external push paths' record location. It is
// derived from the already-resolved layout and needs NO workspace probe: in
// external mode both the reparent state artifact and the §12.3 remote
// follow-up record live directly inside the feature directory, so FeaturePath
// and the mode are the whole identity.
func externalReparentLocation(feature string, layout externalSyncLayout) internal.ReparentLocation {
	return internal.ReparentLocation{
		Mode:        internal.ModeExternal,
		Feature:     feature,
		FeaturePath: layout.FeaturePath,
	}
}

// pushFeature pushes every entry of an external feature. It takes the resolved
// layout so the push half of a run can never target a different root than the
// rebase half.
func pushFeature(feature string, layout externalSyncLayout, dryRun bool) error {
	stack, err := internal.LoadStack(layout.FeaturePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}
	return pushEntries(feature, layout, stack.Branches, dryRun)
}

func pushFeatureSerialized(feature string, layout externalSyncLayout, dryRun bool) (err error) {
	if dryRun {
		return pushFeature(feature, layout, true)
	}
	if refusal := internal.ReparentPushMutationRefusal(externalReparentLocation(feature, layout)); refusal != nil {
		return internal.AnchorReparentRefusal(refusal)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("create push mutation token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	if err := internal.ClaimSyncRunGuard(layout.FeaturePath, token); err != nil {
		return fmt.Errorf("claim feature mutation lock for push: %w", err)
	}
	defer func() {
		err = errors.Join(err, internal.ReleaseOwnedSyncRunGuard(layout.FeaturePath, token))
	}()
	return pushFeature(feature, layout, false)
}

// TopLevelPushEntryBarrier is a test-only seam between invocation-wide
// preflight and each real push while the feature mutation lock is held.
var TopLevelPushEntryBarrier func(index int, entry internal.StackEntry) error

// pushScoped is the sync-side push of a new-mode run, for every scope. It is
// strict and payload-aware: it pushes only the entries this run selected AND
// successfully rebased, in selection order, records every success in the
// payload before the next push is attempted, and stops at the first failure so
// `--continue` can retry exactly the entries that were never pushed.
func pushScoped(feature string, layout externalSyncLayout, stack internal.Stack, sel internal.SyncSelection, completed []string, payload *internal.SyncRunState) error {
	if payload == nil {
		return fmt.Errorf("internal error: new-mode push without run state for %s", feature)
	}
	if payload.Transaction != nil {
		changed := false
		seen := make(map[string]bool, len(payload.Pushed))
		for _, name := range payload.Pushed {
			seen[name] = true
		}
		for _, result := range payload.Transaction.Publication.Results {
			if result.Success && !seen[result.Entry] {
				payload.Pushed = append(payload.Pushed, result.Entry)
				seen[result.Entry] = true
				changed = true
			}
		}
		if changed {
			if err := internal.SaveSyncRunState(layout.FeaturePath, payload); err != nil {
				return fmt.Errorf("reconcile durable publication results: %w", err)
			}
		}
	}
	rebased := make(map[string]bool, len(completed))
	for _, name := range completed {
		rebased[name] = true
	}
	alreadyPushed := make(map[string]bool, len(payload.Pushed))
	for _, name := range payload.Pushed {
		alreadyPushed[name] = true
	}

	loc := externalReparentLocation(feature, layout)
	if refusal := internal.ReparentPushMutationRefusal(loc); refusal != nil {
		return internal.AnchorReparentRefusal(refusal)
	}
	// Under the sync-owned mutation lock, clear local observations first,
	// reload the envelope, then preflight the entries this invocation pushes.
	defaultRepo := externalPushDefaultRepo(layout, stack)
	env, err := internal.PrepareReparentPushEnvelope(loc, defaultRepo, &stack, true)
	if err != nil {
		var anchored *internal.ReparentAnchoredError
		if errors.As(err, &anchored) {
			return anchored
		}
		return fmt.Errorf("evaluate reparent remote follow-up before push: %w", err)
	}
	var probes []internal.ReparentPushEntryProbe
	if env.Active() {
		for _, selected := range sel.Entries {
			if !rebased[selected.Name] || alreadyPushed[selected.Name] {
				continue
			}
			entry := internal.GetBranch(stack, selected.Name)
			if entry.Name == "" {
				continue
			}
			probes = append(probes, env.ProbeEntry(pushRepoDir(layout, entry), entry.Name))
		}
	}
	// The preflight is evaluated unconditionally: an envelope whose record
	// exists but could not be read is inactive AND untrusted, and that case
	// must refuse invocation-wide rather than fall through to today's argv.
	// pushScoped has no dry-run branch and gains none.
	if pre := env.PreflightProbes(probes); pre.Refuse {
		// The refusal escapes into `tws sync`'s own error rendering, so it
		// carries §13.2's marker with it: an operator must never see a bare
		// `remote-followup-unsafe-lease:` line with no idea which subsystem
		// refused.
		return internal.AnchorReparentRefusal(pre.Refusal())
	}

	pushed := false
	for index, selected := range sel.Entries {
		if !rebased[selected.Name] || alreadyPushed[selected.Name] {
			continue
		}
		entry := internal.GetBranch(stack, selected.Name)
		if entry.Name == "" {
			continue
		}
		if sel.Policy.ScopeKind == internal.SyncScopeAll && TopLevelPushEntryBarrier != nil {
			if err := TopLevelPushEntryBarrier(index, entry); err != nil {
				return err
			}
		}
		path := layout.WorktreePath(entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Same skip as the legacy loop: an archived entry has no worktree
			// and is never pushed by tws.
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		repoDir := path
		if entry.Repo != "" {
			repoDir = entry.Repo
		}
		decision := env.Decision(entry.Name)
		if decision.Applies && decision.WarnLine != "" {
			fmt.Fprintln(os.Stderr, decision.WarnLine) //nolint:errcheck
		}
		var publication internal.SyncTransactionPushIntent
		if payload.Transaction != nil {
			publication, err = internal.SyncPreparePublication(payload.Transaction, repoDir, entry.Name, entry.GitBranch())
			if err != nil {
				return err
			}
			if err := internal.SyncBeginPublication(payload.Transaction, publication, func() error {
				return internal.SaveSyncRunState(layout.FeaturePath, payload)
			}); err != nil {
				return err
			}
		}
		argv := pushArgv(decision, entry.GitBranch())
		if payload.Transaction != nil {
			argv = transactionalPushArgv(decision, publication)
		}
		pushErr := internal.RunDirClean(repoDir, "git", argv...)
		if payload.Transaction != nil {
			if err := internal.SyncFinishPublicationAttempt(payload.Transaction, entry.Name, pushErr, func() error {
				return internal.SaveSyncRunState(layout.FeaturePath, payload)
			}); err != nil {
				return err
			}
		}
		if pushErr != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
			if err := saveScopedPushFailure(layout.FeaturePath, payload, entry.Name); err != nil {
				return errors.Join(fmt.Errorf("push failed for %s", entry.Name), err)
			}
			return fmt.Errorf("push failed for %s; fix the remote problem, then resume with: tws sync %s --continue", entry.Name, feature)
		}
		fmt.Printf("  [+] %s (pushed)\n", entry.Name)
		pushed = true

		// The logical name is recorded only after Git succeeded, and the
		// payload is persisted before the next push is attempted.
		payload.Pushed = append(payload.Pushed, entry.Name)
		if err := internal.SaveSyncRunState(layout.FeaturePath, payload); err != nil {
			return fmt.Errorf("record pushed entry %s: %w", entry.Name, err)
		}
	}
	if env.Active() && pushed {
		if err := env.PersistClears(&stack); err != nil {
			return fmt.Errorf("persist reparent remote follow-up clear after push: %w", err)
		}
	}
	return nil
}

// pushEntries is the legacy push loop of `tws push` and of every no-flag run:
// it prints per-entry failures and returns nil, which is exactly the
// compatibility behaviour a new-mode run must not have.
func pushEntries(feature string, layout externalSyncLayout, entries []internal.StackEntry, dryRun bool) error {
	loc := externalReparentLocation(feature, layout)
	if refusal := internal.ReparentPushMutationRefusal(loc); refusal != nil {
		if !dryRun {
			return internal.AnchorReparentRefusal(refusal)
		}
		fmt.Fprintf(os.Stderr, "reparent-remote: would refuse: %s\n", refusal.Error()) //nolint:errcheck
	}
	// Real push persists clears under its invocation lock and reloads before
	// preflight. Dry-run projects the same clears in memory and writes nothing.
	stack := internal.Stack{Branches: append([]internal.StackEntry{}, entries...)}
	defaultRepo := externalPushDefaultRepo(layout, stack)
	env, err := internal.PrepareReparentPushEnvelope(loc, defaultRepo, &stack, !dryRun)
	if err != nil {
		var anchored *internal.ReparentAnchoredError
		if errors.As(err, &anchored) {
			return anchored
		}
		return fmt.Errorf("evaluate reparent remote follow-up before push: %w", err)
	}
	var probes []internal.ReparentPushEntryProbe
	if env.Active() {
		for _, entry := range entries {
			if _, err := os.Stat(layout.WorktreePath(entry.Name)); os.IsNotExist(err) {
				continue
			}
			probes = append(probes, env.ProbeEntry(pushRepoDir(layout, entry), entry.Name))
		}
	}
	preflight := env.PreflightProbes(probes)
	if preflight.Refuse && !dryRun {
		// The gate is invocation-wide: nothing is pushed at all, and the
		// refusal reaches `tws push`'s generic error printer anchored.
		return internal.AnchorReparentRefusal(preflight.Refusal())
	}
	if preflight.Refuse && dryRun {
		// §12.4a rule 2: in a dry run the same verdict is a diagnostic, never
		// a refusal, and the preview still exits 0.
		fmt.Fprintln(os.Stderr, preflight.DryRunLine()) //nolint:errcheck
	}

	pushed := false
	for index, entry := range entries {
		path := layout.WorktreePath(entry.Name)

		// Skip archived branches
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		decision := env.Decision(entry.Name)
		if decision.Applies && decision.WarnLine != "" {
			fmt.Fprintln(os.Stderr, decision.WarnLine) //nolint:errcheck
		}

		if dryRun {
			// §12.4a rule 1: a pending entry's preview names the argv the real
			// run would use; rule 4 keeps every other entry's exact line and
			// stream. A dry run writes, marks and deletes nothing.
			suffix := "(would push --force-with-lease)"
			if decision.Applies && decision.DryRunSuffix != "" {
				suffix = decision.DryRunSuffix
			}
			fmt.Printf("  [~] %s %s\n", entry.Name, suffix)
			continue
		}
		if TopLevelPushEntryBarrier != nil {
			if err := TopLevelPushEntryBarrier(index, entry); err != nil {
				return err
			}
		}

		// Determine repo context
		repoDir := pushRepoDir(layout, entry)
		runErr := internal.RunDirClean(repoDir, "git", pushArgv(decision, entry.GitBranch())...)
		if runErr != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
		} else {
			fmt.Printf("  [+] %s (pushed)\n", entry.Name)
			pushed = true
		}
	}
	if pushed && env.Active() {
		liveStack, err := internal.LoadStack(layout.FeaturePath)
		if err != nil {
			return fmt.Errorf("reload stack after push before clearing reparent follow-up: %w", err)
		}
		if err := env.PersistClears(&liveStack); err != nil {
			return fmt.Errorf("persist reparent remote follow-up clear after push: %w", err)
		}
	}
	return nil
}

func externalPushDefaultRepo(layout externalSyncLayout, stack internal.Stack) string {
	for _, entry := range stack.Branches {
		if entry.Repo != "" {
			continue
		}
		path := layout.WorktreePath(entry.Name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	for _, entry := range stack.Branches {
		if entry.Repo != "" {
			return entry.Repo
		}
	}
	return ""
}

// pushRepoDir is the per-entry Git context both external loops already use.
func pushRepoDir(layout externalSyncLayout, entry internal.StackEntry) string {
	if entry.Repo != "" {
		return entry.Repo
	}
	return layout.WorktreePath(entry.Name)
}

// pushArgv materializes rule R-PUSH step 4: the BARE lease, plus
// --force-if-includes for a pending published entry and nothing else. The zero
// decision reproduces the shipped argv exactly.
func pushArgv(d internal.ReparentPushDecision, branch string) []string {
	args := []string{"push", "--force-with-lease"}
	if d.ForceIfIncludes {
		args = append(args, "--force-if-includes")
	}
	return append(args, "origin", branch)
}

func transactionalPushArgv(d internal.ReparentPushDecision, intent internal.SyncTransactionPushIntent) []string {
	args := []string{"push", "--force-with-lease"}
	if d.ForceIfIncludes {
		args = append(args, "--force-if-includes")
	}
	return append(args, "origin", intent.SourceSHA+":"+intent.DestinationRef)
}

// pushFeatureCheckout holds the pre-sync-modes push body verbatim. Checkout
// mode keeps failing with ErrWorktreeUnsupported and a nonzero exit; neither
// the layout resolver nor the GitBranch() ref fix reaches it.
func pushFeatureCheckout(feature string, dryRun bool) error {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return err
	}

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return fmt.Errorf("no stack.yaml found for feature: %s", feature)
	}

	for _, entry := range stack.Branches {
		path, err := internal.RequireWorktreePath(feature, entry.Name)
		if err != nil {
			return err
		}

		// Skip archived branches
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  [-] %s (archived, skipped)\n", entry.Name)
			continue
		}

		if dryRun {
			fmt.Printf("  [~] %s (would push --force-with-lease)\n", entry.Name)
			continue
		}

		// Determine repo context
		repoDir := path
		if entry.Repo != "" {
			repoDir = entry.Repo
		}

		runErr := internal.RunDirClean(repoDir, "git", "push", "--force-with-lease", "origin", entry.Name)
		if runErr != nil {
			fmt.Printf("  [x] %s (push failed)\n", entry.Name)
		} else {
			fmt.Printf("  [+] %s (pushed)\n", entry.Name)
		}
	}
	return nil
}
