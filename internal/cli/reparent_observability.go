package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// ---------------------------------------------------------------------------
// §11.10 — the reparent observability line, owned by package cli
//
// While a reparent state artifact exists for a feature, `tws doctor`,
// `tws list`, `tws status` and `tws stack status` write EXACTLY ONE anchored
// line per affected feature on stderr, before any ancestry output is written
// to stdout. The requirement is a command WRITE ORDER, not a claim about how
// two streams interleave on a terminal.
//
// Every one of these surfaces stays strictly read-only: no fetch, no ref
// write, no state repair, and no marking or deletion of the §12.3 remote
// follow-up record. A pending remote record on its own changes NO output on
// any of them.
// ---------------------------------------------------------------------------

// reparentFeaturePathFor resolves the feature directory a reparent artifact
// would live in, using the SAME ladder `tws stack reparent` itself uses:
// ws.ResolveFeaturePath in checkout mode, and the external layout resolver in
// external mode — where a configured workspace root and TWS_ROOT may legitimately
// disagree and only the layout resolver reconciles them. Resolving it any
// other way would make a surface look in a directory the run never wrote to.
func reparentFeaturePathFor(ws internal.Workspace, feature string) (string, error) {
	if ws.Mode == internal.ModeCheckout {
		return ws.ResolveFeaturePath(feature)
	}
	root := strings.TrimSpace(os.Getenv("TWS_ROOT"))
	if root == "" || filepath.Clean(root) == filepath.Clean(ws.MetadataRoot) {
		return ws.ResolveFeaturePath(feature)
	}
	layout, err := resolveExternalSyncLayout(ws, root, feature)
	if err != nil {
		return "", err
	}
	return layout.FeaturePath, nil
}

func reparentProjectionFor(ws internal.Workspace, feature, ordinaryFeaturePath string) *internal.ReparentProjection {
	featurePath := ordinaryFeaturePath
	if ws.Mode == internal.ModeExternal || featurePath == "" {
		resolved, err := reparentFeaturePathFor(ws, feature)
		if err != nil {
			return nil
		}
		featurePath = resolved
	}
	return reparentProjectionAtPath(ws, feature, featurePath)
}

func reparentProjectionAtPath(ws internal.Workspace, feature, featurePath string) *internal.ReparentProjection {
	return internal.BuildReparentProjection(internal.ReparentLocationFor(ws, feature, featurePath))
}

// writeReparentNoticeFor writes the anchored line for one feature, if and only
// if that feature holds an artifact. It returns true when a line was written,
// so a caller can assert "exactly once".
func writeReparentNoticeFor(w io.Writer, ws internal.Workspace, feature, featurePath string) bool {
	p := reparentProjectionFor(ws, feature, featurePath)
	if p == nil {
		return false
	}
	_, _ = fmt.Fprintln(w, p.ObservabilityLine())
	return true
}

func writeReparentNoticeAtPath(w io.Writer, ws internal.Workspace, feature, featurePath string) bool {
	p := reparentProjectionAtPath(ws, feature, featurePath)
	if p == nil {
		return false
	}
	_, _ = fmt.Fprintln(w, p.ObservabilityLine())
	return true
}

// writeReparentNoticesForAll writes the anchored line for every feature of the
// workspace that holds an artifact, in sorted feature order, exactly once
// each. Resolution failures are skipped in silence: an unresolvable feature is
// already reported by the surface's own ladder.
func writeReparentNoticesForAll(w io.Writer, ws internal.Workspace, features []string) {
	names := make([]string, len(features))
	copy(names, features)
	sort.Strings(names)
	for _, feature := range names {
		featurePath, err := reparentFeaturePathFor(ws, feature)
		if err != nil {
			continue
		}
		writeReparentNoticeAtPath(w, ws, feature, featurePath)
	}
}

// writeReparentNoticesForWorkspace is the no-argument form used by `tws list`,
// `tws status` and `tws doctor` when they cover every feature.
func writeReparentNoticesForWorkspace(w io.Writer, ws internal.Workspace) {
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		return
	}
	writeReparentNoticesForAll(w, ws, features)
}

// ---------------------------------------------------------------------------
// §14.2a — launch exclusion, external half
//
// The checkout half lives in internal.CheckoutSessionPreconditions, which both
// checkout open paths funnel through. `tws open`'s EXTERNAL arm has no such
// funnel, so it owns its own check — one per arm, each immediately after the
// arm's own GuardFeatureName, so no unguarded path join ever precedes it.
// ---------------------------------------------------------------------------

// refuseOpenDuringReparent refuses `tws open <feature> …` while a reparent
// artifact exists for that feature. It starts no session and attaches no
// terminal, and the refusal lifts the instant the artifact is gone.
func refuseOpenDuringReparent(ws internal.Workspace, feature string) error {
	if ws.Mode == internal.ModeCheckout {
		owner, active, err := internal.AnyCheckoutReparentActive(ws)
		if err != nil {
			return fmt.Errorf("checkout reparent state could not be verified: %w", err)
		}
		if active {
			return internal.ReparentLaunchRefusal(owner)
		}
		return nil
	}
	featurePath, err := reparentFeaturePathFor(ws, feature)
	if err != nil {
		// An unresolvable feature is the arm's own error to report; the
		// exclusion has nothing to say about it.
		return nil
	}
	return refuseExternalOpenDuringMutation(feature, featurePath)
}

func refuseExternalOpenDuringMutation(feature, featurePath string) error {
	loc := internal.ReparentLocation{Mode: internal.ModeExternal, Feature: feature, FeaturePath: featurePath}
	if internal.HasReparentState(loc) {
		return internal.ReparentLaunchRefusal(feature)
	}
	if _, err := os.Lstat(internal.SyncRunGuardPath(featurePath)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("external feature mutation intent for %q could not be verified: %w", feature, err)
	}
	return fmt.Errorf("a sync or stack reparent mutation is being admitted or run for %q; wait for it to finish before opening a session", feature)
}
