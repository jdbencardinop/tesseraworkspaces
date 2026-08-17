package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// runCheckoutSync is the checkout-mode arm of `tws sync`. It receives the
// workspace RunE already resolved — used only for ws.RepoRoot and the mode
// decision — and keeps its own internal.RequireFeaturePath call verbatim, so
// the guard, the layout resolution, and the error semantics are unchanged.
func runCheckoutSync(ws internal.Workspace, opts internal.CheckoutSyncOpts) error {
	feature := opts.Feature
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return err
	}

	// I19 containment: checkout sync operates on the single checkout, never on
	// a linked worktree that merely happens to be the current directory.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	top, topErr := internal.GitRepoRootIn(cwd)
	if topErr != nil || filepath.Clean(top) != filepath.Clean(ws.RepoRoot) {
		return fmt.Errorf("checkout sync operates on %s but the current directory belongs to working tree %s; run it from the repository checkout", ws.RepoRoot, top)
	}

	opts.FeaturePath = featurePath
	opts.RepoDir = ws.RepoRoot

	if opts.Abort {
		if err := internal.AbortCheckoutSync(opts); err != nil {
			return err
		}
		fmt.Println("Checkout sync aborted, original branch restored.")
		return nil
	}

	if opts.Continue {
		// I20 rule 0: trigger flags on --continue require v2 state.
		if opts.NewMode {
			tx, loadErr := internal.LoadCheckoutTransaction(featurePath)
			if loadErr != nil || tx == nil || tx.StateVersion < internal.CheckoutTransactionVersion {
				return fmt.Errorf("%s", errSyncModeFlagsNeedV2)
			}
		}
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
