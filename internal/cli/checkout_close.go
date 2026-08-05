package cli

import (
	"fmt"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func runCheckoutClose(ws internal.Workspace, args []string) error {
	feature, name, err := resolveCheckoutCloseArgs(ws, args)
	if err != nil {
		return err
	}
	return internal.CloseCheckoutSession(ws, feature, name, nil)
}

func resolveCheckoutCloseArgs(ws internal.Workspace, args []string) (string, string, error) {
	state, err := internal.LoadCheckoutAgentSession(ws)
	if err != nil {
		return "", "", fmt.Errorf("no checkout session found")
	}
	feature, name := state.Feature, state.Name
	switch len(args) {
	case 0:
	case 1:
		if args[0] != feature {
			return "", "", fmt.Errorf("active checkout session is %s/%s", feature, name)
		}
	case 2:
		if args[0] != feature || args[1] != name {
			return "", "", fmt.Errorf("active checkout session is %s/%s", feature, name)
		}
	default:
		return "", "", fmt.Errorf("too many arguments")
	}
	return feature, name, nil
}
