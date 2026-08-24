package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

type syncResult struct {
	Complete  bool
	Failed    string
	Completed []string
	// Refusal is non-nil exactly when a guarded route's mid-run replay-limit
	// revalidation refused this invocation: the executor prints its marker
	// through planGuardRefusal and never treats it as an ordinary failure.
	Refusal *internal.PlanGuardRefusalError
}

// syncRunContext carries the frozen decision of a new-mode external run through
// the executor. It is nil on every no-flag run, which is what structurally
// keeps the frozen path frozen.
type syncRunContext struct {
	Policy  internal.SyncRunPolicy
	Sel     internal.SyncSelection
	Payload *internal.SyncRunState

	// Route is "" on every shipped scoped run; a guarded legacy (full-stack)
	// run sets it to internal.RouteLegacy so scoped()/selects()/skipsAnchor()
	// answer exactly as the nil-guard, no-flag path answers today, even
	// though this context is non-nil (it still carries Payload/guard state).
	Route string
	// Validation is the frozen validation identity a guarded run measured at
	// birth (§15). Its zero value (Applies: false) defers to Payload's own
	// TestCommand exactly as before, so an unguarded run observes no change.
	Validation internal.PlanValidationIdentity
}

func (r *syncRunContext) scoped() bool {
	return r != nil && r.Route != internal.RouteLegacy && r.Policy.ScopeKind != internal.SyncScopeAll
}

func (r *syncRunContext) selects(name string) bool {
	if r == nil || r.Route == internal.RouteLegacy {
		return true
	}
	return r.Sel.Names[name]
}

func (r *syncRunContext) skipsAnchor(name string) bool {
	if r == nil || r.Route == internal.RouteLegacy || r.Policy.Propagation != internal.SyncPropagationLocalOnly {
		return false
	}
	return r.Sel.Role(name) == internal.SyncRoleAnchor
}

// validate runs the run's post-rebase validation. A no-flag run resolves
// today's config on every entry, exactly as it does now. A new-mode run uses
// the command frozen in its payload at start-up — including the empty command,
// which means the run was started with no validation configured and must not
// acquire one mid-run — so a --continue from another shell, or after a config
// edit, can never validate with a different command (§7.7). A guarded run's
// own frozen Validation identity, when present, is the command of record
// instead of Payload.TestCommand, so a guard born with --test never silently
// re-reads config either.
func (r *syncRunContext) validate(layout externalSyncLayout, worktreePath, name string) bool {
	if r == nil || r.Payload == nil {
		return runValidation(worktreePath, name)
	}
	command := r.Payload.TestCommand
	if r.Validation.Applies {
		command = r.Validation.Command
	}
	if command == "" {
		return true
	}
	resume := r.Payload.Stage
	r.Payload.Stage = internal.SyncStageValidating
	_ = internal.SaveSyncRunState(layout.FeaturePath, r.Payload)
	if !runValidationCommand(command, worktreePath, name) {
		// The failure path records stage `failed` through saveScopedSyncFailure.
		return false
	}
	r.Payload.Stage = resume
	_ = internal.SaveSyncRunState(layout.FeaturePath, r.Payload)
	return true
}

func syncWithStack(feature string, layout externalSyncLayout, stack internal.Stack, sorted []internal.StackEntry, guard *planGuardRun) syncResult {
	return syncWithStackFiltered(feature, layout, stack, sorted, nil, guard)
}

func syncWithStackFiltered(feature string, layout externalSyncLayout, stack internal.Stack, sorted []internal.StackEntry, alreadyDone map[string]bool, guard *planGuardRun) syncResult {
	return syncWithStackScoped(feature, layout, stack, sorted, alreadyDone, nil, guard)
}

// syncWithStackScoped is the one executor both the no-flag and every new-mode
// route call. guard is nil on every shipped call site: a nil guard leaves
// every byte and every process this function runs untouched, since both new
// JIT seams below are themselves gated on guard != nil.
func syncWithStackScoped(feature string, layout externalSyncLayout, stack internal.Stack, sorted []internal.StackEntry, alreadyDone map[string]bool, run *syncRunContext, guard *planGuardRun) syncResult {
	updatedByRef := make(map[string]bool)
	completed := completedNames(alreadyDone)
	if alreadyDone == nil {
		alreadyDone = make(map[string]bool)
	}
	scoped := run.scoped()
	rebased := 0
	anchorsSkipped := 0
	firstRevalidation := true

	if run != nil && run.Payload != nil {
		run.Payload.Stage = internal.SyncStageRebasing
		_ = internal.SaveSyncRunState(layout.FeaturePath, run.Payload)
	}

	for _, entry := range sorted {
		if alreadyDone[entry.Name] {
			continue
		}
		if !run.selects(entry.Name) {
			continue
		}
		if run.skipsAnchor(entry.Name) {
			fmt.Println(syncAnchorNoOpLine(entry.Name))
			alreadyDone[entry.Name] = true
			anchorsSkipped++
			continue
		}
		path := layout.WorktreePath(entry.Name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		if issue := checkSyncWorktreeBranch(path, entry); issue != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "wrong-branch"))
			fmt.Printf("    %s\n    %s\n", issue.Problem, issue.Hint)
			return syncFailure(layout, sorted, completed, entry.Name, run)
		}

		base := resolveEntryBase(stack, entry, syncRepoContext(layout, entry))
		gitContext := path
		if entry.Repo != "" {
			gitContext = entry.Repo
		}
		currentBaseSHA := internal.GetBranchSHA(gitContext, base)
		rebaseArgs := []string{"rebase", "--update-refs", base}
		if entry.LastBaseSHA != "" && currentBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA {
			rebaseArgs = []string{"rebase", "--update-refs", "--onto", base, entry.LastBaseSHA}
		}
		if scoped {
			// --update-refs rewrites refs outside the selection, which is
			// exactly the unrelated ref movement a scoped run must not cause.
			rebaseArgs = []string{"rebase", base}
			if entry.LastBaseSHA != "" && currentBaseSHA != "" && entry.LastBaseSHA != currentBaseSHA {
				rebaseArgs = []string{"rebase", "--onto", base, entry.LastBaseSHA}
			}
		}

		if guard != nil {
			if firstRevalidation {
				firstRevalidation = false
				if err := syncStepHook(internal.SyncStageRebasing, -1); err != nil {
					return syncFailure(layout, sorted, completed, entry.Name, run)
				}
			}
			if rerr := guard.revalidate(entry.Name); rerr != nil {
				return syncGuardFailure(layout, sorted, completed, entry.Name, run, rerr)
			}
			// §10.5 destination pinning: a guarded execution path passes the
			// planned and JIT-revalidated full SHA in the --onto operand
			// instead of the ref name Git would re-resolve. Only that operand
			// changes; the flag set, the recorded-cutoff operand, the branch
			// operand and the plain-rebase arm are untouched, and no unguarded
			// (golden-covered) invocation can reach this branch.
			rebaseArgs = pinGuardedOnto(rebaseArgs, currentBaseSHA)
		}

		if err := syncStepHook(internal.SyncStageRebasing, rebased); err != nil {
			return syncFailure(layout, sorted, completed, entry.Name, run)
		}

		if err := internal.RunDirClean(path, "git", rebaseArgs...); err != nil {
			fmt.Println(formatSyncStatus(entry.Name, "active", "conflict"))
			fmt.Printf("    Resolve conflicts in: %s\n", path)
			fmt.Println("    Then run: git add . && git rebase --continue")
			fmt.Printf("    Resume with: tws sync %s --continue\n", feature)
			return syncFailure(layout, sorted, completed, entry.Name, run)
		}
		if !run.validate(layout, path, entry.Name) {
			return syncFailure(layout, sorted, completed, entry.Name, run)
		}

		fmt.Println(formatSyncStatus(entry.Name, "active", "synced"))
		if !scoped {
			markUpdatedAncestors(stack, entry.Name, layout, updatedByRef)
		}
		completed = append(completed, entry.Name)
		alreadyDone[entry.Name] = true
		rebased++
		if currentBaseSHA != "" {
			internal.UpdateBaseSHA(&stack, entry.Name, currentBaseSHA)
			_ = internal.SaveStack(layout.FeaturePath, stack)
		}
	}

	for _, entry := range sorted {
		if alreadyDone[entry.Name] {
			continue
		}
		if !run.selects(entry.Name) {
			continue
		}
		if run.skipsAnchor(entry.Name) {
			fmt.Println(syncAnchorNoOpLine(entry.Name))
			alreadyDone[entry.Name] = true
			anchorsSkipped++
			continue
		}
		path := layout.WorktreePath(entry.Name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if internal.IsPrunableWorktree(entry.GitBranch()) {
			fmt.Printf("  [?] %s (missing — run: tws archive %s %s or tws new %s %s)\n", entry.Name, feature, entry.Name, feature, entry.Name)
			return syncFailure(layout, sorted, completed, entry.Name, run)
		}
		if updatedByRef[entry.Name] {
			fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
			completed = append(completed, entry.Name)
			alreadyDone[entry.Name] = true
			continue
		}

		base := resolveEntryBase(stack, entry, entry.Repo)
		rebaseDir := entry.Repo

		if guard != nil {
			if rerr := guard.revalidate(entry.Name); rerr != nil {
				return syncGuardFailure(layout, sorted, completed, entry.Name, run, rerr)
			}
		}

		var err error
		if rebaseDir != "" {
			err = internal.RunSilentDir(rebaseDir, "git", "rebase", base, entry.GitBranch())
		} else {
			err = internal.RunSilent("git", "rebase", base, entry.GitBranch())
		}
		if err != nil {
			if rebaseDir != "" {
				_ = internal.RunSilentDir(rebaseDir, "git", "rebase", "--abort")
			} else {
				_ = internal.RunSilent("git", "rebase", "--abort")
			}
			fmt.Println(formatSyncStatus(entry.Name, "archived", "conflict"))
			fmt.Printf("    Restore with: tws new %s %s\n", feature, entry.Name)
			return syncFailure(layout, sorted, completed, entry.Name, run)
		}
		fmt.Println(formatSyncStatus(entry.Name, "archived", "synced"))
		completed = append(completed, entry.Name)
		alreadyDone[entry.Name] = true
		rebased++
	}

	var selected map[string]bool
	if scoped {
		selected = run.Sel.Names
	}
	if stale := staleStackEdgesFiltered(layout.WorktreesRoot, stack, selected); len(stale) > 0 {
		fmt.Println("Sync incomplete; stale stack edges remain:")
		for _, edge := range stale {
			fmt.Printf("  [!] %s\n", edge)
		}
		return syncFailure(layout, sorted, completed, "", run)
	}
	if scoped {
		if outside := staleStackEdgesComplement(layout.WorktreesRoot, stack, run.Sel.Names); len(outside) > 0 {
			fmt.Println("Stale stack edges outside this scope (unchanged by this run):")
			for _, edge := range outside {
				fmt.Printf("  [i] %s\n", edge)
			}
		}
	}
	if anchorsSkipped > 0 && rebased == 0 {
		fmt.Println("Nothing to propagate.")
	}
	if run != nil && run.Payload != nil {
		// Teardown is deferred to the caller so the selected push can still
		// record progress in the payload (§8.5 steps 5-7). The stage stays
		// where the executor left it: `finalizing` is recorded by the caller
		// immediately before teardown, never before the push.
		run.Payload.Completed = append([]string(nil), completed...)
		run.Payload.Pending = nil
		run.Payload.FailedBranch = ""
		_ = internal.SaveSyncRunState(layout.FeaturePath, run.Payload)
		return syncResult{Complete: true, Completed: completed}
	}
	_ = clearSyncRunState(layout.FeaturePath, false)
	return syncResult{Complete: true, Completed: completed}
}

// syncFailure persists an incomplete run. New-mode runs write the payload only:
// saveIncompleteSync would overwrite the sentinel with a resolvable name and
// hand an old --continue exactly the broad resume the sentinel prevents.
func syncFailure(layout externalSyncLayout, sorted []internal.StackEntry, completed []string, failed string, run *syncRunContext) syncResult {
	if run != nil && run.Payload != nil {
		saveScopedSyncFailure(layout.FeaturePath, run.Payload, failed, completed)
		return syncResult{Failed: failed, Completed: completed}
	}
	return saveIncompleteSync(layout.FeaturePath, sorted, completed, failed)
}

// syncGuardFailure is syncFailure's guard-refusal twin: a mid-run replay-limit
// revalidation refused entry, so the result carries the *PlanGuardRefusalError
// the executor must hand to planGuardRefusal instead of an ordinary failure
// sentence. State is persisted exactly as syncFailure persists it, since a
// refusal this late still leaves the run resumable at the same failed entry.
func syncGuardFailure(layout externalSyncLayout, sorted []internal.StackEntry, completed []string, failed string, run *syncRunContext, rerr error) syncResult {
	result := syncFailure(layout, sorted, completed, failed, run)
	var refusal *internal.PlanGuardRefusalError
	if errors.As(rerr, &refusal) {
		result.Refusal = refusal
	}
	return result
}

func saveIncompleteSync(featurePath string, sorted []internal.StackEntry, completed []string, failed string) syncResult {
	completedSet := make(map[string]bool, len(completed))
	for _, name := range completed {
		completedSet[name] = true
	}
	pending := make([]string, 0)
	for _, entry := range sorted {
		if entry.Name != failed && !completedSet[entry.Name] {
			pending = append(pending, entry.Name)
		}
	}
	state := internal.NewSyncState()
	state.FailedBranch = failed
	state.Pending = pending
	state.Completed = append([]string(nil), completed...)
	_ = internal.SaveSyncState(featurePath, state)
	return syncResult{Failed: failed, Completed: completed}
}

func completedNames(done map[string]bool) []string {
	var names []string
	for name, ok := range done {
		if ok {
			names = append(names, name)
		}
	}
	return names
}

// staleStackEdgesFiltered is the single completion-gate predicate. A nil
// selection means "the whole stack", which is the frozen full-scope behaviour.
func staleStackEdgesFiltered(worktreesRoot string, stack internal.Stack, selected map[string]bool) []string {
	var stale []string
	for _, child := range stack.Branches {
		if selected != nil && !selected[child.Name] {
			continue
		}
		parent := internal.GetBranch(stack, child.Base)
		if parent.Name == "" || !sameStackRepo(parent.Repo, child.Repo) {
			continue
		}
		childPath := filepath.Join(worktreesRoot, child.Name)
		if _, err := os.Stat(childPath); err != nil {
			continue
		}
		if internal.RunSilentDir(childPath, "git", "merge-base", "--is-ancestor", parent.GitBranch(), child.GitBranch()) != nil {
			stale = append(stale, fmt.Sprintf("%s does not contain parent %s", child.Name, parent.Name))
		}
	}
	return stale
}

// staleStackEdgesComplement reports the stale edges outside the selection. It
// is informational only and never changes the exit code.
func staleStackEdgesComplement(worktreesRoot string, stack internal.Stack, selected map[string]bool) []string {
	complement := make(map[string]bool, len(stack.Branches))
	for _, child := range stack.Branches {
		if !selected[child.Name] {
			complement[child.Name] = true
		}
	}
	if len(complement) == 0 {
		return nil
	}
	return staleStackEdgesFiltered(worktreesRoot, stack, complement)
}

func checkSyncWorktreeBranch(worktreePath string, entry internal.StackEntry) *internal.HealthIssue {
	return internal.CheckWorktreeBranch(worktreePath, entry.GitBranch())
}

func resolveEntryBase(stack internal.Stack, entry internal.StackEntry, repoCtx string) string {
	if parent := internal.GetBranch(stack, entry.Base); parent.Name != "" && sameStackRepo(parent.Repo, entry.Repo) {
		return parent.GitBranch()
	}
	return resolveBase(entry.Base, repoCtx)
}

// resolveBase rewrites a base that names the repository's default branch to its
// remote-tracking form. repoCtx == "" keeps today's cwd-scoped resolution
// exactly; a non-empty repoCtx runs the same resolution inside the repository.
func resolveBase(base, repoCtx string) string {
	var defaultBranch string
	if repoCtx == "" {
		defaultBranch = internal.DefaultBranch()
	} else {
		defaultBranch, _ = internal.DefaultBranchIn(repoCtx)
	}
	if base == defaultBranch {
		return "origin/" + defaultBranch
	}
	return base
}

func markUpdatedAncestors(stack internal.Stack, branch string, layout externalSyncLayout, updated map[string]bool) {
	entryMap := make(map[string]internal.StackEntry)
	for _, e := range stack.Branches {
		entryMap[e.Name] = e
	}
	current := branch
	for {
		entry, ok := entryMap[current]
		if !ok {
			break
		}
		parent, ok := entryMap[entry.Base]
		if !ok {
			break
		}
		parentPath := layout.WorktreePath(parent.Name)
		if _, err := os.Stat(parentPath); os.IsNotExist(err) {
			updated[parent.Name] = true
		}
		current = parent.Name
	}
}

func formatSyncStatus(name, mode, status string) string {
	symbols := map[string]string{"synced": "+", "failed": "x", "skipped": "-", "conflict": "!", "resolved": "~", "wrong-branch": "?", "validation-failed": "x"}
	sym := symbols[status]
	if sym == "" {
		sym = "?"
	}
	return fmt.Sprintf("  [%s] %s (%s)", sym, name, mode)
}

func syncFallback(layout externalSyncLayout) {
	entries, _ := os.ReadDir(layout.WorktreesRoot)
	for _, e := range entries {
		if e.IsDir() {
			path := filepath.Join(layout.WorktreesRoot, e.Name())
			fmt.Printf("Syncing worktree: %s\n", path)
			internal.Must(internal.RunDirClean(path, "git", "rebase", "--update-refs", "origin/main"))
		}
	}
}

func runValidation(worktreePath, branchName string) bool {
	return runValidationCommand(internal.LoadConfig().TestCommand, worktreePath, branchName)
}

// runValidationCommand runs one already-resolved validation command. An empty
// command means "no validation": it is a real, frozen decision, not a signal to
// go looking for one.
func runValidationCommand(command, worktreePath, branchName string) bool {
	if command == "" {
		return true
	}
	fmt.Printf("    validating %s: %s... ", branchName, command)
	parts := strings.Fields(command)
	if err := internal.RunSilentDir(worktreePath, parts[0], parts[1:]...); err != nil {
		fmt.Println("FAILED")
		fmt.Println(formatSyncStatus(branchName, "active", "validation-failed"))
		return false
	}
	fmt.Println("ok")
	return true
}

// pinGuardedOnto applies §10.5 destination pinning to one already-built rebase
// argv. It rewrites exactly the operand that follows `--onto` — and only when
// the arm really has one and the destination resolved — leaving a plain
// `git rebase [--update-refs] <base>` arm, the recorded-cutoff operand and the
// branch operand exactly as the unguarded twin builds them.
func pinGuardedOnto(argv []string, destinationSHA string) []string {
	if destinationSHA == "" {
		return argv
	}
	for i, tok := range argv {
		if tok != "--onto" || i+1 >= len(argv) {
			continue
		}
		pinned := append([]string(nil), argv...)
		pinned[i+1] = destinationSHA
		return pinned
	}
	return argv
}
