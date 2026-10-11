package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

func TestNamedTemplateConflictAcceptedEditsSurviveLaterFailures(t *testing.T) {
	for _, failure := range []string{"source-drift", "scratch-drift", "later-cancel", "removal-decline", "apply-failure"} {
		t.Run(failure, func(t *testing.T) {
			_, path, source := newNamedConflictFixture(t, map[string]string{
				"inject/shared.md": "incoming\n", "inject/obsolete.md": "obsolete\n", "feature/notes.md": "notes\n",
			})
			namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
			answer := "edit\nyes\n"
			switch failure {
			case "later-cancel":
				namedConflictWrite(t, filepath.Join(path, "notes.md"), "local orchestrator\n")
				answer += "cancel\n"
			case "removal-decline":
				if err := os.Remove(filepath.Join(source, "inject", "obsolete.md")); err != nil {
					t.Fatal(err)
				}
				answer += "no\n"
			}
			before := snapshotTree(t, path)
			fakeNamedConflictTTY(t)
			t.Setenv("VISUAL", "deterministic-fake-editor")
			oldRun, oldStep := managedTemplateRunEditor, internal.ManagedTemplateApplyStepHook
			t.Cleanup(func() { managedTemplateRunEditor, internal.ManagedTemplateApplyStepHook = oldRun, oldStep })
			var editPath string
			managedTemplateRunEditor = func(command *exec.Cmd) error {
				editPath = command.Args[len(command.Args)-1]
				if failure == "source-drift" {
					namedConflictWrite(t, filepath.Join(source, "inject", "shared.md"), "changed incoming\n")
				}
				return os.WriteFile(editPath, []byte("accepted scratch work\n"), 0o600)
			}
			if failure == "scratch-drift" {
				managedTemplatePostConsentHook = func() error {
					return os.WriteFile(editPath, []byte("later scratch work\n"), 0o600)
				}
			}
			if failure == "apply-failure" {
				internal.ManagedTemplateApplyStepHook = func(step, _ string) error {
					if step == "before-state" {
						return errors.New("injected ledger publication failure")
					}
					return nil
				}
			}
			_, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader(answer))
			if err == nil || editPath == "" || !strings.Contains(err.Error(), "staged edits retained") || !strings.Contains(err.Error(), editPath) {
				t.Fatalf("later error lost accepted scratch location: %v", err)
			}
			want := "accepted scratch work\n"
			if failure == "scratch-drift" {
				want = "later scratch work\n"
			}
			if namedConflictRead(t, editPath) != want {
				t.Fatal("later failure discarded private editor work")
			}
			if failure != "apply-failure" && snapshotTree(t, path) != before {
				t.Fatal("pre-apply failure changed feature effects")
			}
			if failure == "apply-failure" {
				if !strings.Contains(err.Error(), "after 1/2 effect(s)") || !strings.Contains(err.Error(), "preserved pending evidence") {
					t.Fatalf("partial apply did not report progress/recovery: %v", err)
				}
				if namedConflictRead(t, filepath.Join(path, "inject", "shared.md")) != "accepted scratch work\n" {
					t.Fatal("partial apply was silently rolled back")
				}
			}
		})
	}
}

type namedConflictShortWriter struct{}

func (namedConflictShortWriter) Write([]byte) (int, error) { return 0, nil }

func TestNamedTemplateConflictOutputShortWritesFailClosed(t *testing.T) {
	if err := writeManagedTemplateDiff(namedConflictShortWriter{}, "old", "new", []byte("old\n"), []byte("new\n")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short diff output was accepted: %v", err)
	}
	_, path, _ := newNamedConflictFixture(t, nil)
	namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
	before := snapshotTree(t, path)
	fakeNamedConflictTTY(t)
	cmd := templateSyncCmd()
	cmd.SetArgs([]string{"feature", "--resolve-conflicts"})
	cmd.SetIn(strings.NewReader("keep\n"))
	cmd.SetErr(namedConflictShortWriter{})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short prompt output was accepted: %v", err)
	}
	if snapshotTree(t, path) != before {
		t.Fatal("partial prompt output changed feature")
	}
}
