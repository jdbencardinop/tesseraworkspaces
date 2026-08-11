package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func closeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close [feature] [branch]",
		Short: "Close a worktree session (tmux or checkout)",
		Args:  cobra.RangeArgs(0, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			ws, err := internal.RequireWorkspace()
			if err == nil && ws.Mode == internal.ModeCheckout {
				state, loadErr := internal.LoadCheckoutAgentSession(ws)
				if loadErr != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				if len(args) == 0 {
					return []string{state.Feature}, cobra.ShellCompDirectiveNoFileComp
				}
				if len(args) == 1 && args[0] == state.Feature {
					return []string{state.Name}, cobra.ShellCompDirectiveNoFileComp
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			switch len(args) {
			case 0:
				return internal.ListFeatures(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return internal.ListBranches(args[0]), cobra.ShellCompDirectiveNoFileComp
			default:
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		},
		// The external branch resolves a caller-supplied feature name to a
		// path under TwsRoot() and mutates files beneath it (direct session
		// records), so it is guarded like every other external path-joining
		// command. The checkout branch still resolves its identity from
		// active.json rather than from a caller-supplied name and needs no
		// guard.
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := internal.RequireWorkspace()
			if err != nil {
				return err
			}

			// Checkout mode: delegate to checkout close
			if ws.Mode == internal.ModeCheckout {
				if cerr := runCheckoutClose(ws, args); cerr != nil {
					return cerr
				}
				fmt.Println("Checkout session closed.")
				return nil
			}

			// External mode: requires exactly 2 args
			if len(args) != 2 {
				return fmt.Errorf("usage: tws close <feature> <branch>")
			}

			feature := args[0]
			branch := args[1]

			// Guard first: the refusal must be the first observable action,
			// before any path is computed, statted, read, or removed, and
			// before the tmux name is built.
			if gerr := internal.GuardFeatureName(internal.TwsRoot(), feature); gerr != nil {
				return gerr
			}

			return runExternalClose(cmd.OutOrStdout(), feature, branch, nil, realExternalTmuxOps{})
		},
	}
}

// externalTmuxOps is the tmux seam of the external close path.
type externalTmuxOps interface {
	Exists(name string) bool
	Kill(name string) error
}

type realExternalTmuxOps struct{}

func (realExternalTmuxOps) Exists(name string) bool { return sessionExists(name) }
func (realExternalTmuxOps) Kill(name string) error {
	return internal.Run("tmux", "kill-session", "-t", name)
}

// runExternalClose implements the external close ordering: records are
// consulted before tmux, because refusing to disturb a live direct session
// outranks killing a tmux session.
//
// Three record classes exist and each is handled differently:
//
//	live         — refuse; kill nothing, remove nothing.
//	stale        — provably dead; removed by token, one file at a time.
//	unverifiable — State != ok, or Probe == unknown (EPERM). Never removed and
//	               never blocking, but always reported: silently dropping them
//	               would make a close that changed nothing look like a close
//	               that cleaned up.
func runExternalClose(out io.Writer, feature, branch string, proc internal.ProcessProber, tmux externalTmuxOps) error {
	if proc == nil {
		proc = internal.NewProcessProber()
	}
	featurePath := internal.FeaturePath(feature)
	branchID := internal.DirectSessionBranchID(feature, branch)
	records, err := internal.LoadDirectSessions(featurePath, branchID,
		&internal.DirectSessionIdentity{Feature: feature, Name: branch})
	if err != nil {
		return err
	}

	var live, stale, unverifiable []internal.LoadedDirectSession
	for _, rec := range records {
		if rec.State != internal.DirectRecordOK {
			// Not provably stale: never counted live, never removed, and it
			// does not by itself block the tmux kill.
			unverifiable = append(unverifiable, rec)
			continue
		}
		switch proc.Probe(rec.Record.OwnerPID) {
		case internal.ProcessLive:
			live = append(live, rec)
		case internal.ProcessDead:
			stale = append(stale, rec)
		default:
			// EPERM: the pid exists but belongs to another user, so it is not
			// provably dead and must not be removed.
			unverifiable = append(unverifiable, rec)
		}
	}

	if len(live) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s/%s has %d live direct session record(s); tws never kills a direct process",
			feature, branch, len(live))
		for _, rec := range live {
			fmt.Fprintf(&b, "\n  %s", internal.DescribeDirectSession(rec))
		}
		b.WriteString("\nexit the session, then run tws close again")
		return errors.New(b.String())
	}

	removed := 0
	if len(stale) > 0 {
		n, remErr := internal.RemoveStaleDirectSessions(featurePath, stale)
		removed = n
		if remErr != nil {
			_, _ = fmt.Fprintf(out, "Warning: could not remove every stale record: %v\n", remErr)
		}
	}

	// Reported before tmux is touched, so the operator reads what was left
	// behind before reading what was killed. DescribeDirectSession renders the
	// record path and ownership token redacted.
	if len(unverifiable) > 0 {
		_, _ = fmt.Fprintf(out, "%d direct session record(s) for %s/%s could not be verified and were left in place:\n",
			len(unverifiable), feature, branch)
		for _, rec := range unverifiable {
			_, _ = fmt.Fprintf(out, "  %s\n", internal.DescribeDirectSession(rec))
		}
	}

	session := internal.ExternalTmuxSessionName(feature, branch)
	if tmux.Exists(session) {
		if err := tmux.Kill(session); err != nil {
			return fmt.Errorf("error killing session: %w", err)
		}
		if removed > 0 {
			_, _ = fmt.Fprintf(out, "Removed %d stale direct session record(s) for %s/%s.\n", removed, feature, branch)
		}
		_, _ = fmt.Fprintf(out, "Closed tmux session: %s\n", session)
		return nil
	}

	if removed > 0 {
		_, _ = fmt.Fprintf(out, "Removed %d stale direct session record(s) for %s/%s.\n", removed, feature, branch)
		return nil
	}
	if len(unverifiable) > 0 {
		// The flat "no tmux" error would be a false negative here: close did
		// find state for this branch, it just could not act on any of it.
		return fmt.Errorf("no tmux session found for %s/%s, and %d direct session record(s) could not be verified, so nothing was removed; inspect them with: tws status --json",
			feature, branch, len(unverifiable))
	}
	return fmt.Errorf("no tmux session found for %s/%s", feature, branch)
}

// TmuxSessionName returns the tmux session name for a feature/branch pair.
func TmuxSessionName(feature, branch string) string {
	return internal.ExternalTmuxSessionName(feature, branch)
}
