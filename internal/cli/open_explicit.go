package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type explicitOpenOptions struct {
	feature, name         string
	printPath, featureDir bool
	executable            string
	args                  []string
	tmux, noTmux, tmuxSet bool
}

func explicitOpenSelectorCount(featureDir bool) int {
	if featureDir {
		return 1
	}
	return 2
}

// A marker before the required selector slots escapes options. Only a marker
// after those slots delimits child argv; a selector itself may be "--".
func splitExplicitOpenArgs(args []string, dash, count int) (selectors, child []string, childMarker bool, err error) {
	if dash < 0 {
		return args, nil, false, nil
	}
	if dash >= count {
		at := min(dash, len(args))
		return args[:at], args[at:], true, nil
	}
	if len(args) <= count {
		return args, nil, false, nil
	}
	if args[count] != "--" {
		return nil, nil, false, errors.New("child arguments require another -- immediately after option-escaped selectors")
	}
	return args[:count], args[count+1:], true, nil
}

func parseExplicitOpen(cmd *cobra.Command, args []string, printPath bool, executable string, featureDir, tmux, noTmux, noAgent, all bool) (explicitOpenOptions, error) {
	o := explicitOpenOptions{
		printPath: printPath, featureDir: featureDir, executable: executable,
		tmux: tmux, noTmux: noTmux, tmuxSet: cmd.Flags().Changed("tmux"),
	}
	commandSet := cmd.Flags().Changed("command")
	if printPath && commandSet {
		return o, errors.New("--print-path and --command are mutually exclusive")
	}
	if all || noAgent {
		return o, errors.New("--print-path and --command cannot be combined with --all or --no-agent")
	}
	if printPath && (cmd.Flags().Changed("tmux") || cmd.Flags().Changed("no-tmux")) {
		return o, errors.New("--print-path does not accept tmux launch flags")
	}
	if tmux && noTmux {
		return o, errors.New("--tmux and --no-tmux cannot both be enabled")
	}
	want := explicitOpenSelectorCount(featureDir)
	selectors, child, childMarker, err := splitExplicitOpenArgs(args, cmd.ArgsLenAtDash(), want)
	if err != nil {
		return o, err
	}
	if childMarker && !commandSet {
		return o, errors.New("only --command accepts a post-selector -- and child arguments")
	}
	o.args = append([]string(nil), child...)
	if len(selectors) != want {
		return o, errors.New("explicit open requires <feature> <logical-branch>, or <feature> --feature-dir; child arguments must follow --")
	}
	for _, value := range selectors {
		if value == "" || strings.ContainsRune(value, '\x00') {
			return o, errors.New("open selectors must be nonempty and contain no NUL")
		}
	}
	o.feature = selectors[0]
	if err := internal.GuardFeatureName("", o.feature); err != nil {
		return o, err
	}
	if !featureDir {
		o.name = selectors[1]
		if filepath.IsAbs(o.name) || filepath.Clean(o.name) != o.name ||
			o.name == "." || o.name == ".." || strings.HasPrefix(o.name, "../") ||
			strings.Contains(o.name, "\\") {
			return o, errors.New("logical branch must be a safe relative name")
		}
	}
	if commandSet {
		if executable == "" || strings.ContainsRune(executable, '\x00') {
			return o, errors.New("--command requires one nonempty executable without NUL")
		}
		for _, arg := range o.args {
			if strings.ContainsRune(arg, '\x00') {
				return o, errors.New("command arguments cannot contain NUL")
			}
		}
	}
	return o, nil
}

// Bind before admission or checkout activation. In particular, ErrDot is not
// permission to search the target directory instead of the caller's PATH.
func bindOpenExecutable(executable string) (string, error) {
	path, err := exec.LookPath(executable)
	if err != nil {
		return "", fmt.Errorf("resolve --command executable: %w", err)
	}
	return filepath.Abs(path)
}

type explicitOpenTarget struct {
	context resolvedFeatureContext
	path    string
	entry   internal.StackEntry
	info    os.FileInfo
}

func resolveExplicitOpenTarget(ws internal.Workspace, o explicitOpenOptions) (explicitOpenTarget, error) {
	var target explicitOpenTarget
	context, err := resolveFeatureContext(ws, o.feature)
	if err != nil {
		return target, err
	}
	target.context = context
	if err := verifyOpenFeatureDirectory(context); err != nil {
		return target, err
	}
	target.path = context.FeaturePath
	if !o.featureDir {
		if context.Workspace.Mode == internal.ModeExternal {
			target.path, err = internal.ResolveInjectWorktree(context.FeaturePath, o.name)
			if err != nil {
				return target, fmt.Errorf("resolve worktree %q: %w", o.name, err)
			}
			target.entry = internal.StackEntry{Name: o.name, Branch: resolveDirectGitBranch(context.FeaturePath, o.name)}
		} else {
			target.entry, err = internal.CheckoutCommandEntry(context.Workspace, context.FeaturePath, o.name)
			if err != nil {
				return target, err
			}
			target.path = context.Workspace.RepoRoot
			if o.printPath {
				if err := internal.CheckoutCommandCurrentEntry(context.Workspace, target.entry); err != nil {
					return target, err
				}
			}
		}
	}
	target.path, err = filepath.Abs(target.path)
	if err != nil {
		return target, err
	}
	target.info, err = os.Lstat(target.path)
	if err != nil {
		return target, err
	}
	if target.info.Mode()&os.ModeSymlink != 0 || !target.info.IsDir() {
		return target, errors.New("open target must be a real directory, not a symlink")
	}
	return target, nil
}

func verifyOpenFeatureDirectory(context resolvedFeatureContext) error {
	rel, err := filepath.Rel(context.Workspace.MetadataRoot, context.FeaturePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("feature directory escapes its workspace")
	}
	path := context.Workspace.MetadataRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("feature directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("feature directory %s is not a real directory", path)
		}
	}
	return nil
}

func explicitOpenMutationGuard(context resolvedFeatureContext) error {
	if context.Workspace.Mode == internal.ModeCheckout {
		return internal.CheckoutSessionMutationPreconditions(context.Workspace)
	}
	if _, err := featureRenameRouteAdmission(internal.ModeExternal, context.Feature, context.FeaturePath, true); err != nil {
		return err
	}
	if err := refuseExternalOpenDuringMutation(context.Feature, context.FeaturePath); err != nil {
		return err
	}
	for _, path := range []string{internal.SyncStatePath(context.FeaturePath), internal.SyncRunStatePath(context.FeaturePath)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return fmt.Errorf("sync evidence could not be verified: %w", err)
			}
			return fmt.Errorf("sync recovery is pending for %q; use tws sync %s --continue (or --abort)", context.Feature, context.Feature)
		}
	}
	return nil
}

// OpenPrintPathBeforeOutputHook is a test-only seam before the final read-only
// namespace and target checks.
var OpenPrintPathBeforeOutputHook func() error

func runExplicitOpen(cmd *cobra.Command, o explicitOpenOptions) (retErr error) {
	var command []string
	if !o.printPath {
		path, err := bindOpenExecutable(o.executable)
		if err != nil {
			return err
		}
		command = append([]string{path}, o.args...)
	}
	ws, err := internal.RequireWorkspace()
	if err != nil {
		return err
	}
	if ws.Mode == internal.ModeCheckout && o.featureDir && o.tmux {
		return errors.New("--feature-dir --command does not support tmux in checkout mode; use --no-tmux or a logical branch")
	}
	selectedRoot := ""
	if ws.Mode == internal.ModeExternal {
		selectedRoot, err = filepath.Abs(internal.TwsRoot())
		if err != nil {
			return err
		}
		if _, err := featureRenameSelectionAdmission(selectedRoot, o.feature, true); err != nil {
			return err
		}
	}
	target, err := resolveExplicitOpenTarget(ws, o)
	if err != nil {
		return err
	}
	guard := func() error {
		if _, err := featureRenameSelectionAdmission(selectedRoot, o.feature, true); err != nil {
			return err
		}
		if err := explicitOpenMutationGuard(target.context); err != nil {
			return err
		}
		current, err := resolveExplicitOpenTarget(ws, o)
		if err != nil {
			return err
		}
		if current.path != target.path || current.context.FeaturePath != target.context.FeaturePath ||
			current.entry != target.entry || !os.SameFile(current.info, target.info) {
			return errors.New("open target changed during admission; retry after inspecting the workspace")
		}
		return nil
	}
	if err := guard(); err != nil {
		return err
	}
	if o.printPath {
		if strings.ContainsFunc(target.path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return errors.New("cannot print a path containing control characters")
		}
		if OpenPrintPathBeforeOutputHook != nil {
			if err := OpenPrintPathBeforeOutputHook(); err != nil {
				return err
			}
		}
		if err := guard(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), target.path)
		return err
	}

	cfg, err := resolveManagedConfig(target.context.Workspace)
	if err != nil {
		return err
	}
	useTmux := o.tmux
	if !o.featureDir && !o.tmuxSet && !o.noTmux && cfg.Config.UseTmux != nil {
		useTmux = *cfg.Config.UseTmux
	}
	if o.noTmux {
		useTmux = false
	}
	var tmux internal.SessionTmuxRunner
	if useTmux {
		if _, err := exec.LookPath("tmux"); err != nil {
			return errors.New("required tool \"tmux\" not found in PATH")
		}
		tmux = internal.RealSessionTmuxRunner{}
		name := explicitCommandSessionName(target, o)
		if err := internal.RefuseCommandTmuxCollision(tmux, name); err != nil {
			return err
		}
	}
	selection, err := featureRenameSelectionAdmission(selectedRoot, o.feature, false)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, selection.Release()) }()
	admission, err := featureRenameAdmissionAt(target.context.Workspace, o.feature)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, admission.Release()) }()
	finalGuard := func() error {
		return errors.Join(selection.Verify(), admission.Verify(), guard())
	}
	if target.context.Workspace.Mode == internal.ModeCheckout {
		if o.featureDir {
			return internal.WithCheckoutCommandLaunchIntent(target.context.Workspace, func() error {
				if err := finalGuard(); err != nil {
					return err
				}
				if err := internal.ValidateManagedOrchestratorContext(target.context.Workspace, o.feature, target.context.FeaturePath, cfg.AgentCommand); err != nil {
					return err
				}
				return internal.CheckoutFeatureDirSessionPreconditions(target.context.Workspace)
			}, func() error {
				return openDirectCommand(directCommandOptions{Path: target.path, Command: command})
			})
		}
		return internal.OpenCheckoutCommand(target.context.Workspace, o.feature, target.entry, command,
			checkoutDirectCommandRunner{}, tmux, cfg.Config.InjectInto, finalGuard)
	}
	return withExternalCommandIntent(target, o, func(token string) error {
		if err := finalGuard(); err != nil {
			return err
		}
		if err := verifyCommandIntent(target, o, token); err != nil {
			return err
		}
		if useTmux {
			if err := internal.RefuseCommandTmuxCollision(tmux, explicitCommandSessionName(target, o)); err != nil {
				return err
			}
		}
		if o.featureDir {
			if err := internal.ValidateManagedOrchestratorContext(target.context.Workspace, o.feature, target.context.FeaturePath, cfg.AgentCommand); err != nil {
				return err
			}
		} else if err := injectOpenCommand(target, o, cfg.Config.InjectInto, token); err != nil {
			return err
		}
		launchGuard := func() error {
			return errors.Join(finalGuard(), verifyCommandIntent(target, o, token))
		}
		if err := launchGuard(); err != nil {
			return err
		}
		if useTmux {
			return internal.RunExternalTmuxCommand(tmux, explicitCommandSessionName(target, o), target.path, command, token)
		}
		opts := directCommandOptions{Path: target.path, Command: command, FinalGuard: launchGuard}
		if !o.featureDir {
			opts.FeaturePath, opts.Feature, opts.Name = target.context.FeaturePath, o.feature, o.name
			opts.GitBranch = target.entry.Branch
		}
		return openDirectCommand(opts)
	})
}

func explicitCommandSessionName(target explicitOpenTarget, o explicitOpenOptions) string {
	if target.context.Workspace.Mode == internal.ModeCheckout {
		return internal.CheckoutAgentSessionName(target.context.Workspace.StableID, o.feature, o.name)
	}
	if o.featureDir {
		return internal.ExternalFeatureTmuxSessionName(o.feature)
	}
	return internal.ExternalTmuxSessionName(o.feature, o.name)
}

func withExternalCommandIntent(target explicitOpenTarget, o explicitOpenOptions, action func(string) error) (err error) {
	names := []string{o.name}
	token, err := internal.CreateExternalSessionIntent(target.context.FeaturePath, o.feature, names, commandIntentFeatureWide(o))
	if err != nil {
		return err
	}
	defer func() {
		if verifyErr := verifyCommandIntent(target, o, token); verifyErr != nil {
			err = errors.Join(err, verifyErr)
			return
		}
		err = errors.Join(err, internal.RemoveOwnedExternalSessionIntent(target.context.FeaturePath, token))
	}()
	if ExternalSessionLaunchIntentHook != nil {
		if err := ExternalSessionLaunchIntentHook(); err != nil {
			return err
		}
	}
	return action(token)
}

func commandIntentFeatureWide(o explicitOpenOptions) bool {
	// Existing branch intents trim edge whitespace. Broaden the reservation
	// rather than changing a verified legacy logical identity.
	return o.featureDir || strings.TrimSpace(o.name) != o.name
}

func verifyCommandIntent(target explicitOpenTarget, o explicitOpenOptions, token string) error {
	intents, _, err := internal.LoadExternalSessionIntents(target.context.FeaturePath, o.feature, []string{o.name}, nil)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if intent.State == internal.DirectRecordOK && intent.Record.Token == token &&
			intent.Record.OwnerPID == os.Getpid() &&
			((commandIntentFeatureWide(o) && intent.Record.Scope == internal.ExternalSessionIntentFeature) ||
				(!commandIntentFeatureWide(o) && intent.Record.Scope == internal.ExternalSessionIntentBranches &&
					len(intent.Record.Names) == 1 && intent.Record.Names[0] == o.name)) {
			return nil
		}
	}
	return errors.New("command launch intent ownership changed; preserve the remaining evidence")
}

func injectOpenCommand(target explicitOpenTarget, o explicitOpenOptions, into, token string) (retErr error) {
	fp := target.context.FeaturePath
	managed, err := internal.ManagedTemplateStatePresent(fp)
	if err != nil {
		return err
	}
	if !managed {
		return internal.InjectFiles(fp, target.path, into)
	}
	state, err := internal.LoadManagedTemplateState(fp)
	if err != nil {
		return err
	}
	if _, ok := state.Roles[string(internal.ManagedTemplateRoleWorktree)]; !ok {
		return internal.InjectFiles(fp, target.path, into)
	}
	plan, err := internal.PlanManagedTemplate(internal.ManagedTemplatePlanRequest{
		Workspace: target.context.Workspace, Feature: o.feature, FeaturePath: fp,
		Roles:               []internal.ManagedTemplateRole{internal.ManagedTemplateRoleWorktree},
		LinkMaterializeOnly: true, OnlyWorktree: o.name, InjectInto: into,
	})
	if err != nil {
		return err
	}
	if noop, err := verifiedManagedInjectNoop(plan, []managedInjectDestination{{path: target.path}}); err != nil || noop {
		return err
	}
	held, err := acquireManagedTemplateReservations([]*internal.ManagedTemplatePlan{plan}, nil, nil)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, releaseManagedTemplateReservations(held)) }()
	// The entry intent is ours, but every other live/unverifiable runtime
	// still excludes a managed update. The direct record is created later.
	if err := verifyCommandIntent(target, o, token); err != nil {
		return err
	}
	blocking, _, err := internal.GuardDirectSessionsFor(fp, directRecordTargetForBranch(o.feature, o.name), nil)
	if err != nil {
		return err
	}
	intents, _, err := internal.LoadExternalSessionIntents(fp, o.feature, []string{o.name}, nil)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if intent.State != internal.DirectRecordOK || intent.Record.Token != token {
			return errors.New("another session intent blocks managed command injection")
		}
	}
	if len(blocking) > 0 {
		return errors.New("live or unverifiable direct sessions block managed command injection")
	}
	probe := managedTemplateTmuxProbe
	if probe == nil {
		probe = internal.RealTmuxInventory{}
	}
	snapshot := probe.Snapshot()
	if snapshot.Err != nil {
		return snapshot.Err
	}
	if snapshot.Sessions[internal.ExternalTmuxSessionName(o.feature, o.name)] ||
		snapshot.Sessions[internal.ExternalFeatureTmuxSessionName(o.feature)] {
		return errors.New("an existing tmux session blocks managed command injection")
	}
	replanned, err := internal.ReplanManagedTemplate(plan)
	if err != nil {
		return err
	}
	if err := internal.ValidateManagedTemplateReplan(plan, replanned, held.template[0]); err != nil {
		return err
	}
	if err := internal.ApplyManagedTemplatePlan(replanned); err != nil {
		return err
	}
	excluded, err := internal.ManagedTemplateOwnedWorktreePaths(fp)
	if err != nil {
		return err
	}
	return internal.InjectFilesExcluding(fp, target.path, "", excluded)
}
