package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List features and branches",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				// Fallback to legacy TwsRoot for external mode compat.
				wsRoot := internal.TwsRoot()
				if _, serr := os.Stat(wsRoot); os.IsNotExist(serr) {
					fmt.Println("No workspace found. Use 'tws init --mode checkout' or 'tws enable --mode checkout'.")
					return nil
				}
				// Build a minimal workspace for listing.
				ws = internal.Workspace{MetadataRoot: wsRoot, Mode: internal.ModeExternal}
			}

			// Checkout mode dispatch
			if ws.Mode == internal.ModeCheckout {
				return runCheckoutList(ws)
			}

			features, listErr := ws.ListFeaturesResolved()
			if listErr != nil {
				return listErr
			}

			if len(features) == 0 {
				fmt.Println("No features found. Use 'tws add <feature>' to create one.")
				return nil
			}

			fmt.Printf("Workspace: %s (mode: %s)\n\n", ws.MetadataRoot, ws.Mode)

			// Deliberately guard-free: ListFeaturesResolved has already removed
			// space-owned names, and an untrusted spaces.yaml aborted above.
			for _, feature := range features {
				featurePath, resolveErr := ws.ResolveFeaturePath(feature)
				if resolveErr != nil {
					// Ambiguity error — report it inline.
					fmt.Printf("%s\n  ERROR: %v\n\n", feature, resolveErr)
					continue
				}

				fmt.Printf("%s\n", feature)

				stack, serr := internal.LoadStack(featurePath)
				if serr == nil && len(stack.Branches) > 0 {
					for i, entry := range stack.Branches {
						wtPath := filepath.Join(featurePath, "worktrees", entry.Name)
						status := "active"
						if _, err := os.Stat(wtPath); os.IsNotExist(err) {
							if internal.IsPrunableWorktree(entry.Name) {
								status = "missing"
							} else {
								status = "archived"
							}
						}

						tmuxTag := ""
						session := sanitizeSessionName(feature + "/" + entry.Name)
						if sessionExists(session) {
							tmuxTag = " [tmux]"
						}

						healthTag := ""
						if status == "active" {
							if issue := internal.CheckWorktreeBranch(wtPath, entry.Name); issue != nil {
								healthTag = " [wrong-branch!]"
							}
						}

						connector := "├──"
						if i == len(stack.Branches)-1 {
							connector = "└──"
						}
						fmt.Printf("  %s %s (base: %s) [%s]%s%s\n", connector, entry.Name, entry.Base, status, tmuxTag, healthTag)
					}
				} else {
					wtDir := filepath.Join(featurePath, "worktrees")
					wts, rErr := os.ReadDir(wtDir)
					if rErr != nil || len(wts) == 0 {
						fmt.Println("  (no branches)")
					} else {
						for i, wt := range wts {
							if !wt.IsDir() {
								continue
							}
							connector := "├──"
							if i == len(wts)-1 {
								connector = "└──"
							}
							fmt.Printf("  %s %s\n", connector, wt.Name())
						}
					}
				}
				fmt.Println()
			}
			return nil
		},
	}
}

func runCheckoutList(ws internal.Workspace) error {
	entries, err := internal.BuildCheckoutList(ws)
	if err != nil {
		return err
	}
	fmt.Print(internal.FormatCheckoutList(ws, entries))
	return nil
}
