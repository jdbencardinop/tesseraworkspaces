package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

// runCheckoutSync is the checkout-mode arm of `tws sync`. It receives the
// workspace RunE already resolved — used only for ws.RepoRoot and the mode
// decision — and keeps its own internal.RequireFeaturePath call verbatim, so
// the guard, the layout resolution, and the error semantics are unchanged.
func runCheckoutSync(cmd *cobra.Command, ws internal.Workspace, opts internal.CheckoutSyncOpts) error {
	feature := opts.Feature
	featurePath, err := internal.RequireFeaturePath(feature)
	if err != nil {
		return err
	}
	opts.FeaturePath = featurePath
	opts.RepoDir = ws.RepoRoot

	// --plan describes the run this invocation would perform and exits
	// (spec §3.3 step 6): it is dispatched below RequireFeaturePath but
	// above the I19 containment refusal, since a plan never mutates and so
	// never needs the single-checkout containment guarantee a MUTATING
	// checkout route requires.
	if opts.PlanGuard.Plan {
		plan, planErr := internal.PlanCheckoutRebase(opts, internal.PlanWriters{Prose: cmd.ErrOrStderr()})
		if planErr != nil {
			return planGuardRefusal(cmd, planErr)
		}
		return renderPlanDocument(cmd, plan, opts.PlanGuard.JSON)
	}

	// I19 containment: checkout sync operates on the single checkout, never on
	// a linked worktree that merely happens to be the current directory.
	if err := checkoutContainmentGate(ws); err != nil {
		return err
	}

	if opts.Abort {
		if err := internal.AbortCheckoutSync(opts); err != nil {
			return err
		}
		fmt.Println("Checkout sync aborted, original branch restored.")
		return nil
	}

	if opts.Continue {
		tx, loadErr := internal.LoadCheckoutTransaction(featurePath)
		// I20 rule 0: trigger flags on --continue require v2 state. This
		// mirrors internal.CheckoutTriggersNeedV2 exactly —
		// route-aware, not a bare StateVersion compare — so a persisted
		// route:legacy transaction still refuses a new-mode trigger flag
		// even though its StateVersion alone would already satisfy the old
		// naive check.
		if opts.NewMode && internal.CheckoutTriggersNeedV2(tx, loadErr) {
			return fmt.Errorf("%s", errSyncModeFlagsNeedV2)
		}
		opts.PlanGuard.PersistedGuarded = internal.TransactionGuarded(tx)
		if err := internal.ContinueCheckoutSync(opts); err != nil {
			return planGuardRefusal(cmd, err)
		}
		fmt.Println("Checkout sync completed.")
		return nil
	}

	// Fresh sync
	if internal.HasCheckoutTransaction(featurePath) {
		return fmt.Errorf("previous checkout-sync incomplete; use --continue or --abort")
	}

	if err := internal.RunCheckoutSync(opts); err != nil {
		return planGuardRefusal(cmd, err)
	}
	fmt.Println("Checkout sync complete.")
	return nil
}

// checkoutContainmentGate is the I19 containment refusal, extracted so the
// plan-only dispatch above can bypass it without duplicating it.
func checkoutContainmentGate(ws internal.Workspace) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	top, topErr := internal.GitRepoRootIn(cwd)
	if topErr != nil || filepath.Clean(top) != filepath.Clean(ws.RepoRoot) {
		return fmt.Errorf("checkout sync operates on %s but the current directory belongs to working tree %s; run it from the repository checkout", ws.RepoRoot, top)
	}
	return nil
}
