package cli

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func importCmd() *cobra.Command {
	var fromRepo string

	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Import workspace from YAML or tarball",
		Long: `Recreate a workspace from an exported YAML or tarball.

  tws import auth-workspace.yaml           # from YAML file
  tws import auth-workspace.tar.gz         # from tarball (includes inject files)
  tws import --from-repo auth              # from .tws/workspaces/auth.yaml`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			internal.RequireTool("git")

			if fromRepo != "" {
				importFromRepo(fromRepo)
				return
			}

			if len(args) == 0 {
				fmt.Println("Usage: tws import <file> or tws import --from-repo <feature>")
				os.Exit(1)
			}

			file := args[0]
			if strings.HasSuffix(file, ".tar.gz") || strings.HasSuffix(file, ".tgz") {
				importTarball(file)
			} else {
				importYAML(file)
			}
		},
	}

	cmd.Flags().StringVar(&fromRepo, "from-repo", "", "Import from .tws/workspaces/<feature>.yaml")

	return cmd
}

func importFromRepo(feature string) {
	repoRoot, err := internal.MainRepoRoot()
	if err != nil {
		fmt.Println("Error: not inside a git repository")
		os.Exit(1)
	}

	path := filepath.Join(repoRoot, ".tws", "workspaces", feature+".yaml")
	importYAML(path)
}

func importYAML(file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", file, err)
		os.Exit(1)
	}

	export, err := internal.UnmarshalExport(data)
	if err != nil {
		fmt.Printf("Error parsing %s: %v\n", file, err)
		os.Exit(1)
	}

	recreateWorkspace(export, "")
}

func importTarball(file string) {
	f, err := os.Open(file)
	if err != nil {
		fmt.Printf("Error opening %s: %v\n", file, err)
		os.Exit(1)
	}
	defer f.Close() //nolint:errcheck

	gr, err := gzip.NewReader(f)
	if err != nil {
		fmt.Printf("Error: not a valid gzip file: %v\n", err)
		os.Exit(1)
	}
	defer gr.Close() //nolint:errcheck

	// Extract to temp dir first, find workspace.yaml
	tmpDir, err := os.MkdirTemp("", "tws-import-*")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Printf("Error reading tarball: %v\n", err)
			os.Exit(1)
		}

		target := filepath.Join(tmpDir, header.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			continue
		}

		outFile, err := os.Create(target)
		if err != nil {
			continue
		}
		if _, err := io.Copy(outFile, tr); err != nil {
			outFile.Close() //nolint:errcheck
			continue
		}
		outFile.Close() //nolint:errcheck
	}

	// Read workspace.yaml
	data, err := os.ReadFile(filepath.Join(tmpDir, "workspace.yaml"))
	if err != nil {
		fmt.Printf("Error: tarball missing workspace.yaml\n")
		os.Exit(1)
	}

	export, err := internal.UnmarshalExport(data)
	if err != nil {
		fmt.Printf("Error parsing workspace.yaml: %v\n", err)
		os.Exit(1)
	}

	// Pass tmpDir so inject files can be copied
	injectSrc := filepath.Join(tmpDir, "inject")
	recreateWorkspace(export, injectSrc)
}

func recreateWorkspace(export internal.WorkspaceExport, injectSrc string) {
	feature := export.Feature
	featurePath := internal.FeaturePath(feature)

	// Create feature directory
	wsRoot := internal.TwsRoot()
	os.MkdirAll(filepath.Join(wsRoot, ".tws-workspace"), 0755) //nolint:errcheck
	os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0755) //nolint:errcheck

	if _, err := os.Stat(filepath.Join(featurePath, "FEATURE.md")); os.IsNotExist(err) {
		os.WriteFile(filepath.Join(featurePath, "FEATURE.md"), []byte("# "+feature+"\n"), 0644) //nolint:errcheck
	}

	// Write stack.yaml
	if len(export.Stack.Branches) > 0 {
		if err := internal.SaveStack(featurePath, export.Stack); err != nil {
			fmt.Printf("Warning: could not write stack.yaml: %v\n", err)
		} else {
			fmt.Printf("  Restored stack.yaml (%d branches)\n", len(export.Stack.Branches))
		}
	}

	// Write decisions.yaml
	if len(export.Decisions.Entries) > 0 {
		if err := internal.SaveDecisions(featurePath, export.Decisions); err != nil {
			fmt.Printf("Warning: could not write decisions.yaml: %v\n", err)
		} else {
			fmt.Printf("  Restored decisions.yaml (%d entries)\n", len(export.Decisions.Entries))
		}
	}

	// Copy inject files from tarball if available
	injectDir := internal.InjectPath(featurePath)
	os.MkdirAll(injectDir, 0755) //nolint:errcheck
	if injectSrc != "" {
		if _, err := os.Stat(injectSrc); err == nil {
			count, _ := copyDir(injectSrc, injectDir)
			if count > 0 {
				fmt.Printf("  Restored %d inject file(s)\n", count)
			}
		}
	}

	// Check out branches
	for _, entry := range export.Stack.Branches {
		path := internal.WorktreePath(feature, entry.Name)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("  [=] %s (already exists)\n", entry.Name)
			continue
		}

		if internal.BranchExists(entry.Name) {
			err := internal.RunSilent("git", "worktree", "add", "--force", path, entry.Name)
			if err != nil {
				fmt.Printf("  [x] %s (checkout failed)\n", entry.Name)
			} else {
				fmt.Printf("  [+] %s (checked out)\n", entry.Name)
			}
		} else {
			fmt.Printf("  [-] %s (branch not found — fetch from remote first)\n", entry.Name)
		}
	}

	// Inject files into worktrees
	target := internal.ResolveInjectInto("")
	count, _ := internal.InjectFilesForFeature(featurePath, target)
	if count > 0 {
		fmt.Printf("  Injected files into %d worktree(s)\n", count)
	}

	fmt.Printf("Imported feature: %s\n", feature)
}
