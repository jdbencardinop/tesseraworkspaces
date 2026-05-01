package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraspaces/internal"
)

func StackCmd(args []string) {
	if len(args) < 1 {
		println("Usage: ts stack <feature>")
		return
	}

	feature := args[0]
	featurePath := internal.FeaturePath(feature)

	stack, err := internal.LoadStack(featurePath)
	if err != nil {
		fmt.Println("No stack.yaml found for feature:", feature)
		os.Exit(1)
	}

	if _, err := internal.TopoSort(stack); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	internal.PrintTree(stack)
}
