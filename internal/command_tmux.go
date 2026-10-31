package internal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommandTmuxRunner adds token verification to the existing transport. The
// token is the existing launch/session owner, not a command history or schema.
type CommandTmuxRunner interface {
	SessionTmuxRunner
	CommandSessionExists(name string) (bool, error)
	NewCommandSession(name, dir string, command []string, token string) error
	OwnedCommandSession(name, token string) (bool, error)
	AttachCommandSession(name, token string) error
	KillCommandSession(name, token string) error
}

func commandTmuxRunner(runner SessionTmuxRunner) (CommandTmuxRunner, error) {
	tmux, ok := runner.(CommandTmuxRunner)
	if !ok {
		return nil, errors.New("tmux transport cannot verify command session ownership")
	}
	return tmux, nil
}

func RefuseCommandTmuxCollision(runner SessionTmuxRunner, name string) error {
	tmux, err := commandTmuxRunner(runner)
	if err != nil {
		return err
	}
	present, err := tmux.CommandSessionExists(name)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("tmux session %q already exists; close or inspect it first (the supplied command was not run)", name)
	}
	return nil
}

func (RealSessionTmuxRunner) CommandSessionExists(name string) (bool, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return false, errors.New("tmux is unavailable; session state cannot be verified")
	}
	budget := NewStatusProbeBudget(context.Background())
	defer budget.Close()
	snapshot := (RealTmuxInventory{budget: budget}).Snapshot()
	return snapshot.Sessions[name], snapshot.Err
}

func (r RealSessionTmuxRunner) NewCommandSession(name, dir string, command []string, token string) error {
	if len(command) == 0 || !filepath.IsAbs(command[0]) || token == "" {
		return errors.New("tmux command requires a bound absolute executable and an owner token")
	}
	// Multiple argv elements force tmux's exec route even for a zero-argument
	// executable. Caller text appears only in positional args, never source.
	// Only these option values are tmux formats. Do not format-escape the
	// executable or child argv, which must reach exec byte-for-byte.
	args := []string{"new-session", "-d", "-P", "-F", "#{session_id}",
		"-s", strings.ReplaceAll(name, "#", "##"), "-c", strings.ReplaceAll(dir, "#", "##"),
		"--", "/bin/sh", "-c", `exec "$@"`, "tws-command"}
	args = append(args, command...)
	// tmux parses trailing semicolons as command separators before exec.
	// It removes exactly one backslash before a final semicolon, including
	// when the value already contains backslashes. This is not shell quoting.
	for i, arg := range args {
		if strings.HasSuffix(arg, ";") {
			args[i] = arg[:len(arg)-1] + `\;`
		}
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return fmt.Errorf("tmux command creation could not be confirmed: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if !validCommandTmuxID(id) {
		return errors.New("tmux did not return a verifiable command session identity")
	}
	// Address the returned id, never a possibly reoccupied name. A command
	// that already exited needs no marker; its checkout state predates spawn.
	if err := exec.Command("tmux", "set-option", "-t", id, "@tws-command-owner", token).Run(); err != nil {
		present, probeErr := r.CommandSessionExists(name)
		if probeErr == nil && !present {
			return nil
		}
		return errors.Join(errors.New("tmux command ownership could not be recorded; inspect the session"), err, probeErr)
	}
	return nil
}

func validCommandTmuxID(id string) bool {
	if len(id) < 2 || id[0] != '$' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (r RealSessionTmuxRunner) ownedCommandSessionID(name, token string) (string, error) {
	present, err := r.CommandSessionExists(name)
	if err != nil || !present {
		return "", err
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", "="+name+":",
		"#{session_id}\t#{@tws-command-owner}").Output()
	if err != nil {
		if present, probeErr := r.CommandSessionExists(name); probeErr == nil && !present {
			return "", nil
		}
		return "", fmt.Errorf("tmux command session identity is unverifiable: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "\t")
	if len(parts) != 2 || !validCommandTmuxID(parts[0]) || token == "" || parts[1] != token {
		return "", errors.New("tmux command session ownership changed or is uncertain; inspect it without killing it")
	}
	return parts[0], nil
}

func (r RealSessionTmuxRunner) OwnedCommandSession(name, token string) (bool, error) {
	id, err := r.ownedCommandSessionID(name, token)
	return id != "", err
}

func (r RealSessionTmuxRunner) AttachCommandSession(name, token string) error {
	id, err := r.ownedCommandSessionID(name, token)
	if err != nil || id == "" {
		return err
	}
	return r.AttachSession(id)
}

func (r RealSessionTmuxRunner) KillCommandSession(name, token string) error {
	id, err := r.ownedCommandSessionID(name, token)
	if err != nil || id == "" {
		return err
	}
	return r.KillSession(id)
}

func RunExternalTmuxCommand(runner SessionTmuxRunner, name, path string, command []string, token string) error {
	tmux, err := commandTmuxRunner(runner)
	if err != nil {
		return err
	}
	if err := RefuseCommandTmuxCollision(runner, name); err != nil {
		return err
	}
	if err := tmux.NewCommandSession(name, path, command, token); err != nil {
		return fmt.Errorf("command startup is uncertain; inspect tmux session %q before retrying (nothing was killed): %w", name, err)
	}
	present, err := tmux.OwnedCommandSession(name, token)
	if err != nil || !present {
		return err
	}
	attachErr := tmux.AttachCommandSession(name, token)
	present, probeErr := tmux.OwnedCommandSession(name, token)
	if probeErr != nil {
		return errors.Join(attachErr, probeErr)
	}
	if attachErr != nil {
		if present {
			return fmt.Errorf("tmux attach failed; command session %q remains active: %w", name, attachErr)
		}
		return fmt.Errorf("tmux attach failed and the command session ended; its exit status is unavailable: %w", attachErr)
	}
	return nil
}
