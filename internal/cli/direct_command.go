package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

type directCommandOptions struct {
	Path, FeaturePath, Feature, Name, GitBranch string
	Command                                     []string
	Runner                                      directRunner
	Store                                       directSessionStore
	FinalGuard                                  func() error
}

func openDirectCommand(opts directCommandOptions) (retErr error) {
	if len(opts.Command) == 0 || !filepath.IsAbs(opts.Command[0]) {
		return errors.New("command executable must be bound to an absolute path before launch")
	}
	for _, arg := range opts.Command {
		if strings.ContainsRune(arg, '\x00') {
			return errors.New("command argv cannot contain NUL")
		}
	}
	runner := opts.Runner
	if runner == nil {
		runner = realDirectRunner{}
	}
	store := opts.Store
	if store == nil {
		store = realDirectSessionStore{}
	}
	tracked := opts.FeaturePath != "" && opts.Feature != "" && opts.Name != ""
	var branchID, token string
	if tracked {
		branchID = internal.DirectSessionBranchID(opts.Feature, opts.Name)
		var err error
		token, err = store.Create(opts.FeaturePath, internal.DirectSessionRecord{
			Feature: opts.Feature, Name: opts.Name, GitBranch: opts.GitBranch,
			Path: opts.Path, Stage: internal.DirectStageCommand,
		})
		if err != nil {
			return err
		}
		defer func() {
			if err := store.Update(opts.FeaturePath, branchID, token, func(*internal.DirectSessionRecord) {}); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("command record cleanup refused: %w", err))
				return
			}
			retErr = errors.Join(retErr, store.RemoveOwned(opts.FeaturePath, branchID, token))
		}()
		if DirectSessionLaunchIntentHook != nil {
			if err := DirectSessionLaunchIntentHook(); err != nil {
				return err
			}
		}
	}
	if opts.FinalGuard != nil {
		if err := opts.FinalGuard(); err != nil {
			return err
		}
	}
	if tracked {
		if err := store.Update(opts.FeaturePath, branchID, token, func(*internal.DirectSessionRecord) {}); err != nil {
			return err
		}
	}
	child, err := runner.Start(opts.Path, opts.Command)
	if err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	if tracked {
		if err := store.Update(opts.FeaturePath, branchID, token, func(rec *internal.DirectSessionRecord) {
			rec.ChildPID = child.PID()
		}); err != nil {
			terminateErr := child.Terminate()
			waitErr := child.Wait()
			return errors.Join(err, terminateErr, waitErr)
		}
	}
	if err := child.Wait(); err != nil {
		return fmt.Errorf("command: %w", err)
	}
	return nil
}

type checkoutDirectCommandRunner struct{}

func (checkoutDirectCommandRunner) Run(path string, command []string) error {
	return openDirectCommand(directCommandOptions{Path: path, Command: command})
}
