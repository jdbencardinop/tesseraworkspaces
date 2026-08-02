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
		RunE: func(cmd *cobra.Command, args []string) error {
			internal.RequireTool("git")

			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			if fromRepo != "" {
				return importFromRepoE(fromRepo, ws)
			}

			if len(args) == 0 {
				return fmt.Errorf("usage: tws import <file> or tws import --from-repo <feature>")
			}

			file := args[0]
			if strings.HasSuffix(file, ".tar.gz") || strings.HasSuffix(file, ".tgz") {
				return importTarballE(file, ws)
			}
			return importYAMLE(file, ws)
		},
	}

	cmd.Flags().StringVar(&fromRepo, "from-repo", "", "Import from .tws/workspaces/<feature>.yaml")

	return cmd
}

func importFromRepoE(feature string, ws internal.Workspace) error {
	repoRoot, err := internal.MainRepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repository")
	}

	path := filepath.Join(repoRoot, ".tws", "workspaces", feature+".yaml")
	return importYAMLE(path, ws)
}

func importYAMLE(file string, ws internal.Workspace) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}

	export, err := internal.UnmarshalExport(data)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", file, err)
	}

	return recreateWorkspaceE(export, "", ws)
}

func importTarballE(file string, ws internal.Workspace) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("opening %s: %w", file, err)
	}
	defer f.Close() //nolint:errcheck

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a valid gzip file: %w", err)
	}
	defer gr.Close() //nolint:errcheck

	tmpDir, err := os.MkdirTemp("", "tws-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tarball: %w", err)
		}

		if isRuntimeState(header.Name) {
			continue
		}
		target, err := safeArchiveTarget(tmpDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("creating archive directory %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("creating parent for %s: %w", header.Name, err)
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				return fmt.Errorf("creating extracted file %s: %w", header.Name, err)
			}
			if _, copyErr := io.Copy(outFile, tr); copyErr != nil {
				_ = outFile.Close()
				return fmt.Errorf("extracting %s: %w", header.Name, copyErr)
			}
			if err := outFile.Close(); err != nil {
				return fmt.Errorf("closing extracted file %s: %w", header.Name, err)
			}
		default:
			return fmt.Errorf("unsupported archive entry %q (type %d)", header.Name, header.Typeflag)
		}
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "workspace.yaml"))
	if err != nil {
		return fmt.Errorf("tarball missing workspace.yaml")
	}

	export, err := internal.UnmarshalExport(data)
	if err != nil {
		return fmt.Errorf("parsing workspace.yaml: %w", err)
	}

	injectSrc := filepath.Join(tmpDir, "inject")
	return recreateWorkspaceE(export, injectSrc, ws)
}

func safeArchiveTarget(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	root = filepath.Clean(root)
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	return target, nil
}

// isRuntimeState returns true for explicit runtime-state paths.
func isRuntimeState(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.HasPrefix(normalized, ".tws/state/") ||
		normalized == ".tws/state" ||
		normalized == ".sync-state.yaml"
}

func recreateWorkspaceE(export internal.WorkspaceExport, injectSrc string, ws internal.Workspace) error {
	if ws.Mode == internal.ModeCheckout {
		return recreateCheckout(export, injectSrc, ws)
	}
	return recreateExternal(export, injectSrc)
}

// recreateCheckout imports into checkout mode: restores under .tws/features/,
// creates/registers branches without linked worktrees, never imports runtime state.
func recreateCheckout(export internal.WorkspaceExport, injectSrc string, ws internal.Workspace) error {
	feature := export.Feature
	featurePath := ws.FeaturePath(feature)

	if err := os.MkdirAll(featurePath, 0755); err != nil {
		return fmt.Errorf("creating feature directory: %w", err)
	}

	// FEATURE.md
	featureMD := filepath.Join(featurePath, "FEATURE.md")
	if _, err := os.Stat(featureMD); os.IsNotExist(err) {
		_ = os.WriteFile(featureMD, []byte("# "+feature+"\n"), 0644)
	}

	// Stack
	if len(export.Stack.Branches) > 0 {
		if err := internal.SaveStack(featurePath, export.Stack); err != nil {
			return fmt.Errorf("writing stack.yaml: %w", err)
		}
		fmt.Printf("  Restored stack.yaml (%d branches)\n", len(export.Stack.Branches))
	}

	// Decisions
	if len(export.Decisions.Entries) > 0 {
		if err := internal.SaveDecisions(featurePath, export.Decisions); err != nil {
			fmt.Printf("Warning: could not write decisions.yaml: %v\n", err)
		} else {
			fmt.Printf("  Restored decisions.yaml (%d entries)\n", len(export.Decisions.Entries))
		}
	}

	// Inject files
	injectDir := internal.InjectPath(featurePath)
	if err := os.MkdirAll(injectDir, 0755); err != nil {
		return fmt.Errorf("creating inject directory: %w", err)
	}
	if injectSrc != "" {
		if _, err := os.Stat(injectSrc); err == nil {
			count, _ := copyDir(injectSrc, injectDir)
			if count > 0 {
				fmt.Printf("  Restored %d inject file(s)\n", count)
			}
		}
	}

	// Register branches (no worktrees in checkout mode)
	for _, entry := range export.Stack.Branches {
		gitBranch := entry.GitBranch()
		if internal.VerifyGitRef(ws.RepoRoot, gitBranch) == nil {
			fmt.Printf("  [=] %s (branch exists: %s)\n", entry.Name, gitBranch)
		} else {
			fmt.Printf("  [-] %s (branch %s not found — fetch from remote first)\n", entry.Name, gitBranch)
		}
	}

	fmt.Printf("Imported feature: %s (checkout mode — no worktrees created)\n", feature)
	return nil
}

// recreateExternal imports into external mode (original behavior).
func recreateExternal(export internal.WorkspaceExport, injectSrc string) error {
	feature := export.Feature
	featurePath := internal.FeaturePath(feature)

	wsRoot := internal.TwsRoot()
	os.MkdirAll(filepath.Join(wsRoot, ".tws-workspace"), 0755) //nolint:errcheck
	os.MkdirAll(filepath.Join(featurePath, "worktrees"), 0755) //nolint:errcheck

	if _, err := os.Stat(filepath.Join(featurePath, "FEATURE.md")); os.IsNotExist(err) {
		os.WriteFile(filepath.Join(featurePath, "FEATURE.md"), []byte("# "+feature+"\n"), 0644) //nolint:errcheck
	}

	if len(export.Stack.Branches) > 0 {
		if err := internal.SaveStack(featurePath, export.Stack); err != nil {
			fmt.Printf("Warning: could not write stack.yaml: %v\n", err)
		} else {
			fmt.Printf("  Restored stack.yaml (%d branches)\n", len(export.Stack.Branches))
		}
	}

	if len(export.Decisions.Entries) > 0 {
		if err := internal.SaveDecisions(featurePath, export.Decisions); err != nil {
			fmt.Printf("Warning: could not write decisions.yaml: %v\n", err)
		} else {
			fmt.Printf("  Restored decisions.yaml (%d entries)\n", len(export.Decisions.Entries))
		}
	}

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

	target := internal.ResolveInjectInto("")
	count, _ := internal.InjectFilesForFeature(featurePath, target)
	if count > 0 {
		fmt.Printf("  Injected files into %d worktree(s)\n", count)
	}

	fmt.Printf("Imported feature: %s\n", feature)
	return nil
}
