package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func runExternalFeatureRename(cmd *cobra.Command, ws internal.Workspace, oldName, newName, recovery string) (retErr error) {
	var rootSpellings []string
	var selectionAdmission *internal.ExternalFeatureAdmission
	selected := internal.TwsRoot()
	if resolved, err := filepath.EvalSymlinks(selected); err == nil && sameResolvedRepoRoot(resolved, ws.MetadataRoot) {
		rootSpellings = []string{selected}
	} else if recovery == "" {
		if _, err := os.Lstat(selected); err == nil {
			selectionAdmission, err = internal.AcquireExternalFeatureAdmission(selected, oldName, newName)
			if err != nil {
				return err
			}
			defer func() { retErr = errors.Join(retErr, selectionAdmission.Release()) }()
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	result, err := internal.RenameExternalFeature(ws, oldName, newName, recovery,
		func(feature, path string, names []string) error {
			if err := selectionAdmission.Verify(); err != nil {
				return err
			}
			targets, err := directRecordTargetsForFeature(path)
			if err != nil {
				return err
			}
			// Keep the established public IDs and redacted direct-session errors.
			if err := guardDirectRecords(cmd.OutOrStdout(), path, "rename feature", oldName, targets); err != nil {
				return err
			}
			blocking, err := reparentExternalLiveSessions(path, feature, names, managedTemplateTmuxProbe)
			if err != nil {
				return fmt.Errorf("external session state is unverifiable: %w", err)
			}
			if len(blocking) != 0 {
				return fmt.Errorf("active external session(s) block feature rename: %q", blocking)
			}
			_, stale, err := internal.LoadExternalSessionIntents(path, feature, names, nil)
			if err != nil {
				return err
			}
			if _, err := internal.RemoveStaleExternalSessionIntents(path, stale); err != nil {
				return err
			}
			return nil
		}, rootSpellings...)
	if err != nil {
		return err
	}
	if result.Aborted {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Aborted feature rename: restored %s\n", result.Path)
	} else if result.CleanupOnly {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Completed feature rename cleanup: %s\n", result.Path)
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Renamed feature: %s → %s\n", oldName, newName)
	}
	return err
}

// featureRenameAdmittedCommand holds name locks through the entire command,
// not just its initial existence check. The first argument is a feature.
// Interactive selection and --all use the corresponding selected-path helper.
func featureRenameAdmittedCommand(cmd *cobra.Command) *cobra.Command {
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) (retErr error) {
		if len(args) == 0 {
			return run(cmd, args)
		}
		ws, err := internal.RequireWorkspace()
		if err == nil && ws.Mode != internal.ModeExternal {
			return run(cmd, args)
		}
		root := filepath.Clean(internal.TwsRoot())
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
			// Creation routes take admission after creating their root. There
			// cannot yet be a rename of an existing feature in this namespace.
			return run(cmd, args)
		}
		if preview := cmd.Flags().Lookup("plan"); preview != nil && preview.Value.String() == "true" {
			if err := internal.CheckExternalFeatureRenamePending(root, args[0]); err != nil {
				return err
			}
			return run(cmd, args)
		}
		admission, err := internal.AcquireExternalFeatureAdmission(root, args[0])
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, admission.Release()) }()
		return run(cmd, args)
	}
	return cmd
}

func featureRenameAdmissionAt(ws internal.Workspace, features ...string) (*internal.ExternalFeatureAdmission, error) {
	if ws.Mode != internal.ModeExternal {
		return nil, nil
	}
	return internal.AcquireExternalFeatureAdmission(ws.MetadataRoot, features...)
}

// Validated routes supply their already-resolved feature path. In particular,
// sync/reparent must not repeat workspace probes or resolve paths before their
// pure flag ladders. Preview routes inspect pending evidence without creating
// admission files.
func featureRenameRouteAdmission(mode internal.WorkspaceMode, feature, featurePath string, preview bool) (*internal.ExternalFeatureAdmission, error) {
	if mode != internal.ModeExternal {
		return nil, nil
	}
	root := filepath.Dir(featurePath)
	if preview {
		return nil, internal.CheckExternalFeatureRenamePending(root, feature)
	}
	return internal.AcquireExternalFeatureAdmission(root, feature)
}

// A healthy legacy fallback may use the configured root, but an interrupted
// rename in the execution-selected namespace must not turn into that fallback.
// The resolver's read-only check is repeated under a shared name reservation.
func featureRenameSelectionAdmission(root, feature string, preview bool) (*internal.ExternalFeatureAdmission, error) {
	if root == "" {
		return nil, nil
	}
	if err := internal.CheckExternalFeatureRenamePending(root, feature); err != nil {
		return nil, err
	}
	if preview {
		return nil, nil
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return internal.AcquireExternalFeatureAdmission(root, feature)
}

func featureRenameContextCommand(cmd *cobra.Command) *cobra.Command {
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) (retErr error) {
		ws, err := internal.RequireWorkspace()
		if err == nil && ws.Mode != internal.ModeExternal {
			return run(cmd, args)
		}
		root := internal.TwsRoot()
		root, err = filepath.Abs(root)
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		admission, err := internal.AcquireExternalFeatureContextAdmission(root, cwd)
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, admission.Release()) }()
		return run(cmd, args)
	}
	return cmd
}
