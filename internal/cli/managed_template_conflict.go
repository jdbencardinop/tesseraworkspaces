package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

type managedTemplateCheckedWriter struct{ io.Writer }

func (writer managedTemplateCheckedWriter) Write(data []byte) (int, error) {
	n, err := writer.Writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

type managedTemplateEdits struct {
	accepted []*managedTemplateEditorScratch
}

func (edits *managedTemplateEdits) verify() error {
	for _, scratch := range edits.accepted {
		if err := scratch.verifyAll(); err != nil {
			return err
		}
	}
	return nil
}

func (edits *managedTemplateEdits) finish(success bool) error {
	var errs []error
	if success {
		if err := edits.verify(); err != nil {
			errs = append(errs, fmt.Errorf("managed template effects completed but staged edit verification failed: %w", err))
			success = false
		}
	}
	for _, scratch := range edits.accepted {
		retain := !success
		if success {
			if err := scratch.cleanup(); err != nil {
				retain = true
				errs = append(errs, fmt.Errorf("managed template effects completed but editor scratch cleanup failed: %w", err))
			}
		}
		if retain {
			errs = append(errs, fmt.Errorf("accepted staged edits retained at %q (rendered incoming reference: %q)",
				scratch.filePath("edit"), scratch.filePath("incoming")))
		}
		errs = append(errs, scratch.Close())
	}
	return errors.Join(errs...)
}

func confirmManagedTemplateChanges(cmd *cobra.Command, plans []*internal.ManagedTemplatePlan, flags managedTemplateFlags, edits *managedTemplateEdits) error {
	// Keep the reader across menus and removal consent. Never pass its buffered
	// confirmation bytes to an editor; the editor gets the original terminal.
	reader := bufio.NewReader(cmd.InOrStdin())
	if flags.resolveConflicts {
		for _, plan := range plans {
			if err := internal.PreflightManagedTemplateMutation(plan); err != nil {
				return fmt.Errorf("%s: %w", plan.Request.Feature, err)
			}
			if err := recheckManagedTemplateSessions(plan); err != nil {
				return fmt.Errorf("%s: %w", plan.Request.Feature, err)
			}
		}
		if err := resolveManagedTemplateConflicts(cmd, reader, plans, edits); err != nil {
			return err
		}
	}
	return confirmManagedTemplateRemovals(cmd, plans, flags.allowRemoval, reader)
}

func resolveManagedTemplateConflicts(cmd *cobra.Command, reader *bufio.Reader, plans []*internal.ManagedTemplatePlan, edits *managedTemplateEdits) error {
	sort.Slice(plans, func(i, j int) bool { return plans[i].Request.Feature < plans[j].Request.Feature })
	warned := false
	for _, plan := range plans {
		for _, conflict := range plan.PendingConflicts() {
			if !managedTemplateInputIsTTY(cmd.InOrStdin()) {
				return fmt.Errorf("feature %q role=%s path=%q: --resolve-conflicts requires terminal input for decisions; piped answers are not consent",
					conflict.Feature, conflict.Role, conflict.Path)
			}
			if !warned && plan.NewState.Version == 1 {
				if _, err := fmt.Fprintln(managedTemplateCheckedWriter{cmd.ErrOrStderr()},
					"Warning: keeping or editing a noncanonical customization upgrades ownership to v2. "+
						"Released v1.2.18-rc.5 refuses v2 in managed-state readers/writers and may also refuse workspace owner discovery. "+
						"This is not a universal disable switch: rc.5 checkout tws new may still create logical branch/stack metadata without reading the ledger, leaving template bytes and acknowledgements unchanged. "+
						"Use the newer CLI consistently in this workspace; no downgrade is provided."); err != nil {
					return err
				}
				warned = true
			}
			if err := resolveManagedTemplateConflict(cmd, reader, plans, plan, conflict, edits); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveManagedTemplateConflict(cmd *cobra.Command, reader *bufio.Reader, plans []*internal.ManagedTemplatePlan, plan *internal.ManagedTemplatePlan, conflict internal.ManagedTemplateConflict, edits *managedTemplateEdits) error {
	out := managedTemplateCheckedWriter{cmd.ErrOrStderr()}
	source := conflict.Source.Kind
	if conflict.Source.Kind == "registry" {
		source += ":" + conflict.Source.Scope + ":" + conflict.Source.Name + " origin=" + conflict.Source.OriginID
	}
	if _, err := fmt.Fprintf(out,
		"\nManaged text conflict: feature=%q role=%s path=%q\ndestination=%q\nsource=%q renderer=%q\ncurrent: sha256=%s mode=%04o\nincoming (rendered): sha256=%s mode=%04o\n",
		conflict.Feature, conflict.Role, conflict.Path, conflict.Destination, source, conflict.Agent,
		conflict.CurrentSHA256, conflict.CurrentMode, conflict.IncomingSHA256, conflict.IncomingMode); err != nil {
		return err
	}
	for {
		if _, err := fmt.Fprint(out, "keep / replace / diff / edit / cancel [k/r/d/e/C]: "); err != nil {
			return err
		}
		answer, err := readManagedTemplateAnswer(reader)
		if err != nil {
			return err
		}
		switch answer {
		case "", "c", "cancel":
			return errors.New("managed template update cancelled; no feature effects applied")
		case "k", "keep":
			return internal.ResolveManagedTemplateConflict(plan, conflict.Role, conflict.Path, "keep", nil)
		case "r", "replace":
			return internal.ResolveManagedTemplateConflict(plan, conflict.Role, conflict.Path, "replace", nil)
		case "d", "diff":
			if err := writeManagedTemplateDiff(out, "current/"+conflict.Destination, "incoming/"+conflict.Destination,
				conflict.CurrentContent(), conflict.IncomingContent()); err != nil {
				return err
			}
		case "e", "edit":
			data, accepted, err := editManagedTemplateConflict(cmd, reader, plans, conflict, edits)
			if err != nil {
				return err
			}
			if accepted {
				return internal.ResolveManagedTemplateConflict(plan, conflict.Role, conflict.Path, "edit", data)
			}
		default:
			if _, err := fmt.Fprintln(out, "Choose keep (k), replace (r), diff (d), edit (e), or cancel (c)."); err != nil {
				return err
			}
		}
	}
}

func readManagedTemplateAnswer(reader *bufio.Reader) (string, error) {
	answer, err := reader.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return "", errors.New("managed template update cancelled: end of input before a complete answer")
	}
	if err != nil {
		return "", fmt.Errorf("reading managed template decision: %w", err)
	}
	if strings.ContainsRune(answer, '\x03') {
		return "", errors.New("managed template update cancelled: interrupted")
	}
	return strings.ToLower(strings.TrimSpace(answer)), nil
}

// A single contiguous unified hunk avoids invoking Git, pagers, diff drivers,
// external diff, or textconv. Unchanged interior lines may be repeated when
// there are several separated edits; the two byte snapshots remain exact.
func writeManagedTemplateDiff(out io.Writer, beforeLabel, afterLabel string, before, after []byte) error {
	out = managedTemplateCheckedWriter{out}
	if bytes.Equal(before, after) {
		_, err := fmt.Fprintln(out, "No content differences (compare the displayed permission modes).")
		return err
	}
	oldLines, newLines := managedTemplateDiffLines(before), managedTemplateDiffLines(after)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && bytes.Equal(oldLines[prefix], newLines[prefix]) {
		prefix++
	}
	oldEnd, newEnd := len(oldLines), len(newLines)
	for oldEnd > prefix && newEnd > prefix && bytes.Equal(oldLines[oldEnd-1], newLines[newEnd-1]) {
		oldEnd--
		newEnd--
	}
	start := max(0, prefix-3)
	oldLimit, newLimit := min(len(oldLines), oldEnd+3), min(len(newLines), newEnd+3)
	oldCount, newCount := oldLimit-start, newLimit-start
	oldStart, newStart := start+1, start+1
	if oldCount == 0 {
		oldStart = start
	}
	if newCount == 0 {
		newStart = start
	}
	if _, err := fmt.Fprintf(out, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n",
		strconv.QuoteToASCII(beforeLabel), strconv.QuoteToASCII(afterLabel),
		oldStart, oldCount, newStart, newCount); err != nil {
		return err
	}
	writeLine := func(prefix byte, line []byte) error {
		if _, err := fmt.Fprintf(out, "%c%s", prefix, escapeManagedTemplateTerminal(string(line))); err != nil {
			return err
		}
		if !bytes.HasSuffix(line, []byte("\n")) {
			_, err := fmt.Fprintln(out, "\n\\ No newline at end of file")
			return err
		}
		return nil
	}
	for _, line := range oldLines[start:prefix] {
		if err := writeLine(' ', line); err != nil {
			return err
		}
	}
	for _, line := range oldLines[prefix:oldEnd] {
		if err := writeLine('-', line); err != nil {
			return err
		}
	}
	for _, line := range newLines[prefix:newEnd] {
		if err := writeLine('+', line); err != nil {
			return err
		}
	}
	for _, line := range oldLines[oldEnd:oldLimit] {
		if err := writeLine(' ', line); err != nil {
			return err
		}
	}
	return nil
}

func managedTemplateDiffLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func escapeManagedTemplateTerminal(text string) string {
	var safe strings.Builder
	for _, char := range text {
		if char != '\n' && char != '\t' && (!unicode.IsPrint(char) || unicode.Is(unicode.Cf, char)) {
			quoted := strconv.QuoteRuneToASCII(char)
			safe.WriteString(quoted[1 : len(quoted)-1])
		} else {
			safe.WriteRune(char)
		}
	}
	return safe.String()
}

func managedTemplateEditorCommand() string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if command := strings.TrimSpace(os.Getenv(name)); command != "" {
			return command
		}
	}
	return "vi"
}

func parseManagedTemplateEditor(command string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, char := range command {
		switch {
		case escaped:
			word.WriteRune(char)
			escaped, started = false, true
		case char == '\\' && quote != '\'':
			escaped, started = true, true
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote, started = char, true
		case unicode.IsSpace(char):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(char)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("malformed editor command: unmatched quote or trailing escape; use quoted argv, not shell syntax")
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("editor command has no executable")
	}
	return args, nil
}

// Tests replace only the runner, never a user's editor or shell configuration.
var managedTemplateRunEditor = func(command *exec.Cmd) error { return command.Run() }

func editManagedTemplateConflict(cmd *cobra.Command, reader *bufio.Reader, plans []*internal.ManagedTemplatePlan, conflict internal.ManagedTemplateConflict, edits *managedTemplateEdits) (data []byte, accepted bool, retErr error) {
	out := managedTemplateCheckedWriter{cmd.ErrOrStderr()}
	args, err := parseManagedTemplateEditor(managedTemplateEditorCommand())
	if err != nil {
		return nil, false, err
	}
	scratch, err := newManagedTemplateEditorScratch(plans, conflict.CurrentContent(), conflict.IncomingContent())
	if err != nil {
		return nil, false, err
	}
	heldForInvocation := false
	defer func() {
		if !heldForInvocation {
			retErr = errors.Join(retErr, scratch.Close())
		}
	}()
	retain := func(cause error) ([]byte, bool, error) {
		return nil, false, fmt.Errorf("%w; staged edits retained at %q (rendered incoming reference: %q); no feature effects applied",
			cause, scratch.filePath("edit"), scratch.filePath("incoming"))
	}
	if _, err := fmt.Fprintf(out,
		"Two-way editing starts from current content at %q.\nRendered incoming reference: %q\nEditor must wait for completion (for example: code --wait). Exiting the editor does not accept its output.\n",
		scratch.filePath("edit"), scratch.filePath("incoming")); err != nil {
		return retain(err)
	}
	command := exec.CommandContext(cmd.Context(), args[0], append(args[1:], scratch.filePath("edit"))...)
	command.Stdin, command.Stdout, command.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := scratch.validateDirectories(); err != nil {
		return retain(err)
	}
	if err := managedTemplateRunEditor(command); err != nil {
		return retain(fmt.Errorf("editor failed: %w", err))
	}
	candidate, err := scratch.readCandidate()
	if err != nil {
		return retain(err)
	}
	if err := internal.ValidateManagedTemplateConflictContent(conflict, candidate); err != nil {
		return retain(err)
	}
	if err := writeManagedTemplateDiff(out, "current/"+conflict.Destination, "edited/"+conflict.Destination,
		conflict.CurrentContent(), candidate); err != nil {
		return retain(err)
	}
	if _, err := fmt.Fprintf(out, "Candidate sha256=%x mode=%04o\nAccept these exact edited bytes? [y/N] ",
		sha256.Sum256(candidate), conflict.IncomingMode); err != nil {
		return retain(err)
	}
	answer, err := readManagedTemplateAnswer(reader)
	if err != nil {
		return retain(err)
	}
	accepted = answer == "y" || answer == "yes"
	if accepted {
		edits.accepted = append(edits.accepted, scratch)
		heldForInvocation = true
		return candidate, true, nil
	}
	if _, err := fmt.Fprintf(out, "Discarding unaccepted staged edit at %q; returning to the conflict menu.\n", scratch.filePath("edit")); err != nil {
		return retain(err)
	}
	if err := scratch.cleanup(); err != nil {
		return retain(err)
	}
	return candidate, accepted, nil
}
