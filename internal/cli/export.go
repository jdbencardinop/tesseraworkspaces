package cli

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"

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
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(cmd *cobra.Command, args []string) {
			feature := args[0]
			featurePath := internal.FeaturePath(feature)

			if _, err := os.Stat(featurePath); os.IsNotExist(err) {
				fmt.Printf("Feature not found: %s\n", feature)
				os.Exit(1)
			}

			stack, _ := internal.LoadStack(featurePath)
			decisions, _ := internal.LoadDecisions(featurePath)
			export := internal.NewWorkspaceExport(feature, stack, decisions)

			if toRepo {
				exportToRepo(feature, export)
				return
			}

			if full {
				exportTarball(feature, featurePath, export, output)
				return
			}

			exportYAML(export, output)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default: stdout)")
	cmd.Flags().BoolVar(&full, "full", false, "Include inject files in a tarball")
	cmd.Flags().BoolVar(&toRepo, "to-repo", false, "Save to .tws/workspaces/ in the repo")

	return cmd
}

func exportYAML(export internal.WorkspaceExport, output string) {
	data, err := internal.MarshalExport(export)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	if output == "" {
		fmt.Print(string(data))
	} else {
		if err := os.WriteFile(output, data, 0644); err != nil {
			fmt.Printf("Error writing %s: %v\n", output, err)
			os.Exit(1)
		}
		fmt.Printf("Exported to: %s\n", output)
	}
}

func exportToRepo(feature string, export internal.WorkspaceExport) {
	repoRoot, err := internal.MainRepoRoot()
	if err != nil {
		fmt.Println("Error: not inside a git repository")
		os.Exit(1)
	}

	dir := filepath.Join(repoRoot, ".tws", "workspaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	path := filepath.Join(dir, feature+".yaml")
	data, err := internal.MarshalExport(export)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Exported to: %s\n", path)
	fmt.Println("Commit and push to share with team members.")
}

func exportTarball(feature, featurePath string, export internal.WorkspaceExport, output string) {
	if output == "" {
		output = feature + "-workspace.tar.gz"
	}

	f, err := os.Create(output)
	if err != nil {
		fmt.Printf("Error creating %s: %v\n", output, err)
		os.Exit(1)
	}
	defer f.Close() //nolint:errcheck

	gw := gzip.NewWriter(f)
	defer gw.Close() //nolint:errcheck

	tw := tar.NewWriter(gw)
	defer tw.Close() //nolint:errcheck

	// Write workspace.yaml
	yamlData, _ := internal.MarshalExport(export)
	addToTar(tw, "workspace.yaml", yamlData)

	// Write inject files
	injectDir := internal.InjectPath(featurePath)
	if _, err := os.Stat(injectDir); err == nil {
		_ = filepath.Walk(injectDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			relPath, _ := filepath.Rel(featurePath, path)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			addToTar(tw, relPath, data)
			return nil
		})
	}

	fmt.Printf("Exported to: %s\n", output)
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
