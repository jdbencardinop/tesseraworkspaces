package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func List(args []string) {
	wsRoot := internal.TwsRoot()

	if _, err := os.Stat(wsRoot); os.IsNotExist(err) {
		fmt.Println("No workspace found at:", wsRoot)
		return
	}

	entries, err := os.ReadDir(wsRoot)
	if err != nil {
		fmt.Printf("Error reading workspace: %v\n", err)
		os.Exit(1)
	}

	// Filter to feature directories (skip .tws-workspace marker and files)
	var features []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".tws-workspace" {
			continue
		}
		features = append(features, e.Name())
	}

	if len(features) == 0 {
		fmt.Println("No features found. Use 'tws add <feature>' to create one.")
		return
	}

	fmt.Printf("Workspace: %s\n\n", wsRoot)

	for _, feature := range features {
		featurePath := filepath.Join(wsRoot, feature)
		fmt.Printf("%s\n", feature)

		stack, err := internal.LoadStack(featurePath)
		if err == nil && len(stack.Branches) > 0 {
			// Show branches from stack.yaml with base info
			for i, entry := range stack.Branches {
				wtPath := filepath.Join(featurePath, "worktrees", entry.Name)
				status := "active"
				if _, err := os.Stat(wtPath); os.IsNotExist(err) {
					status = "missing"
				}

				connector := "├──"
				if i == len(stack.Branches)-1 {
					connector = "└──"
				}
				fmt.Printf("  %s %s (base: %s) [%s]\n", connector, entry.Name, entry.Base, status)
			}
		} else {
			// No stack.yaml — list worktree dirs directly
			wtDir := filepath.Join(featurePath, "worktrees")
			wts, err := os.ReadDir(wtDir)
			if err != nil || len(wts) == 0 {
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
}
