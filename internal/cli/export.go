package cli

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func exportCmd() *cobra.Command {
	var output string
	var full bool
	var toRepo bool

	cmd := &cobra.Command{
		Use:   "export <feature>",
		Short: "Export workspace metadata for portability",
		Long: `Export a feature's workspace metadata (stack, decisions) to YAML or tarball.

  tws export auth                          # YAML to stdout
  tws export auth -o auth.yaml             # YAML to file
  tws export auth --full -o auth.tar.gz    # tarball with inject files
  tws export auth --to-repo                # save to .tws/workspaces/ (committed)`,
		Args: cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				ws, err := internal.RequireWorkspace()
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				ws, err = managedTemplateExecutionWorkspace(ws)
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				features, err := ws.ListFeaturesResolved()
				if err != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				return features, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			feature := args[0]

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			context, err := resolveFeatureContext(ws, feature)
			if err != nil {
				return err
			}
			ws = context.Workspace
			featurePath := context.FeaturePath

			if _, err := os.Stat(featurePath); os.IsNotExist(err) {
				return fmt.Errorf("feature not found: %s", feature)
			}
			managed, err := internal.ManagedTemplateStatePresent(featurePath)
			if err != nil {
				return err
			}
			if managed {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Warning: export omits managed template selections, ownership metadata, external-link ownership, and orchestrator assets; imported inject files remain unmanaged."); err != nil {
					return err
				}
			}

			stack, _ := internal.LoadStack(featurePath)
			decisions, _ := internal.LoadDecisions(featurePath)
			export := internal.NewWorkspaceExport(feature, stack, decisions)

			if toRepo {
				if ws.Mode == internal.ModeCheckout {
					return fmt.Errorf("--to-repo is not supported in checkout mode; use '-o <tracked-path>' to write to a tracked file instead")
				}
				return exportToRepo(ws.RepoRoot, feature, export)
			}

			if full {
				return exportTarball(feature, featurePath, export, output)
			}

			return exportYAML(export, output)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default: stdout)")
	cmd.Flags().BoolVar(&full, "full", false, "Include inject files in a tarball")
	cmd.Flags().BoolVar(&toRepo, "to-repo", false, "Save to .tws/workspaces/ in the repo")

	return cmd
}

func exportYAML(export internal.WorkspaceExport, output string) error {
	data, err := internal.MarshalExport(export)
	if err != nil {
		return fmt.Errorf("marshaling export: %w", err)
	}

	if output == "" {
		fmt.Print(string(data))
	} else {
		if err := os.WriteFile(output, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", output, err)
		}
		fmt.Printf("Exported to: %s\n", output)
	}
	return nil
}

func exportToRepo(repoRoot, feature string, export internal.WorkspaceExport) error {
	dir := filepath.Join(repoRoot, ".tws", "workspaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, feature+".yaml")
	data, err := internal.MarshalExport(export)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	fmt.Printf("Exported to: %s\n", path)
	fmt.Println("Commit and push to share with team members.")
	return nil
}

// exportTarball creates a tarball with durable metadata and inject files.
// Runtime state (.tws/state) is structurally excluded.
func exportTarball(feature, featurePath string, export internal.WorkspaceExport, output string) error {
	if output == "" {
		output = feature + "-workspace.tar.gz"
	}

	f, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("creating %s: %w", output, err)
	}
	defer f.Close() //nolint:errcheck

	gw := gzip.NewWriter(f)
	defer gw.Close() //nolint:errcheck

	tw := tar.NewWriter(gw)
	defer tw.Close() //nolint:errcheck

	// Write workspace.yaml
	yamlData, _ := internal.MarshalExport(export)
	addToTar(tw, "workspace.yaml", yamlData)

	// Write inject files only (exclude runtime state structurally)
	injectDir := internal.InjectPath(featurePath)
	if _, err := os.Stat(injectDir); err == nil {
		_ = filepath.Walk(injectDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				if err == nil && info != nil && info.IsDir() &&
					filepath.Base(path) == ".tws-template-recovery" {
					return filepath.SkipDir
				}
				return nil
			}
			relPath, _ := filepath.Rel(featurePath, path)
			// Structurally exclude runtime state: only include inject/ prefix
			if !strings.HasPrefix(relPath, "inject/") && !strings.HasPrefix(relPath, "inject\\") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			addToTar(tw, relPath, data)
			return nil
		})
	}

	fmt.Printf("Exported to: %s\n", output)
	return nil
}

func addToTar(tw *tar.Writer, name string, data []byte) {
	header := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}
	_ = tw.WriteHeader(header)
	_, _ = tw.Write(data)
}
