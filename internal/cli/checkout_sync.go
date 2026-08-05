package cli

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func runCheckoutSync(feature string, push bool, testCmd string, cont, abort, verbose bool) error {
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return err
	}

	// Resolve repo directory (the git working directory)
	repoDir, err := os.Getwd()
	if err != nil {
		return err
	}

	opts := internal.CheckoutSyncOpts{
		Feature:     feature,
		FeaturePath: featurePath,
		RepoDir:     repoDir,
		Push:        push,
		TestCommand: testCmd,
		Verbose:     verbose,
	}

	if abort {
		if err := internal.AbortCheckoutSync(opts); err != nil {
			return err
		}
		fmt.Println("Checkout sync aborted, original branch restored.")
		return nil
	}

	if cont {
		if err := internal.ContinueCheckoutSync(opts); err != nil {
			return err
		}
		fmt.Println("Checkout sync completed.")
		return nil
	}

	// Fresh sync
	if internal.HasCheckoutTransaction(featurePath) {
		return fmt.Errorf("previous checkout-sync incomplete; use --continue or --abort")
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		return err
	}
	fmt.Println("Checkout sync complete.")
	return nil
}
