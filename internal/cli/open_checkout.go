package cli

import (
	"fmt"
	"sort"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func runCheckoutOpen(ws internal.Workspace, args []string, useTmux, noTmux, noAgent bool, cmdFlag interface{ Changed(string) bool }) error {
	return runCheckoutOpenResolved(ws, args, useTmux, noTmux, noAgent, cmdFlag, nil)
}

func runCheckoutOpenResolved(
	ws internal.Workspace,
	args []string,
	useTmux, noTmux, noAgent bool,
	cmdFlag interface{ Changed(string) bool },
	managedCfg *resolvedManagedConfig,
) error {
	// Guarded at the top: resolveCheckoutOpenArgs already resolves feature
	// paths and loads stacks for a named feature.
	if len(args) >= 1 {
		if err := internal.GuardFeatureName(ws.MetadataRoot, args[0]); err != nil {
			return err
		}
	}
	feature, name, err := resolveCheckoutOpenArgs(ws, args)
	if err != nil {
		return err
	}
	featurePath, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return err
	}
	managed, err := internal.ManagedTemplateStatePresent(featurePath)
	if err != nil {
		return err
	}
	cfg := internal.LoadConfig()
	target := managedInjectTarget{Value: internal.ResolveInjectInto("")}
	if managed {
		if managedCfg == nil {
			resolved, err := resolveManagedConfig(ws)
			if err != nil {
				return err
			}
			managedCfg = &resolved
		}
		cfg = managedCfg.Config
		target = managedInjectTarget{Value: cfg.InjectInto}
		if err := internal.ValidateManagedCheckoutContextResolved(
			ws, feature, featurePath, managedCfg.AgentCommand,
			target.Value, target.Explicit,
		); err != nil {
			return err
		}
	}
	if noAgent {
		fmt.Println(ws.RepoRoot)
		return nil
	}
	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		return err
	}
	entry := internal.GetBranch(stack, name)
	if entry.Name == "" {
		return fmt.Errorf("branch %q not found in feature %q", name, feature)
	}
	command := internal.CheckoutSessionAgentCommand(cfg.GetAgentCommand(), ws.RepoRoot)
	tmux := useTmux
	if !cmdFlag.Changed("tmux") && !noTmux && cfg.UseTmux != nil {
		tmux = *cfg.UseTmux
	}
	if noTmux {
		tmux = false
	}
	if tmux {
		return internal.OpenCheckoutTmux(ws, feature, entry, command, nil, target.Value)
	}
	return internal.OpenCheckoutDirect(ws, feature, entry, command, nil, nil, target.Value)
}

func resolveCheckoutOpenArgs(ws internal.Workspace, args []string) (string, string, error) {
	switch len(args) {
	case 2:
		return args[0], args[1], nil
	case 1:
		names, err := checkoutActiveNames(ws, args[0])
		if err != nil {
			return "", "", err
		}
		if len(names) == 1 {
			return args[0], names[0], nil
		}
		name, err := pick("Select branch:", names)
		return args[0], name, err
	case 0:
		features, err := ws.ListFeaturesResolved()
		if err != nil {
			return "", "", err
		}
		if len(features) == 0 {
			return "", "", fmt.Errorf("no features found")
		}
		feature := features[0]
		if len(features) > 1 {
			feature, err = pick("Select feature:", features)
			if err != nil {
				return "", "", err
			}
		}
		names, err := checkoutActiveNames(ws, feature)
		if err != nil {
			return "", "", err
		}
		if len(names) == 1 {
			return feature, names[0], nil
		}
		name, err := pick("Select branch:", names)
		return feature, name, err
	default:
		return "", "", fmt.Errorf("too many arguments")
	}
}
func checkoutActiveNames(ws internal.Workspace, feature string) ([]string, error) {
	path, err := ws.ResolveFeaturePath(feature)
	if err != nil {
		return nil, err
	}
	stack, err := internal.LoadStack(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range stack.Branches {
		if !entry.Archived && entry.Repo == "" {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no active branches in feature %q", feature)
	}
	return names, nil
}
