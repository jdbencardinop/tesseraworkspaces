package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
)

func newNamedConflictFixture(t *testing.T, files map[string]string) (*managedCLIFixture, string, string) {
	t.Helper()
	f := newManagedCLIFixture(t)
	for key, value := range map[string]string{
		"GIT_AUTHOR_NAME": "Conflict Test", "GIT_AUTHOR_EMAIL": "conflict@example.test",
		"GIT_COMMITTER_NAME": "Conflict Test", "GIT_COMMITTER_EMAIL": "conflict@example.test",
	} {
		t.Setenv(key, value)
	}
	scratch := filepath.Join(f.root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", scratch)
	if files == nil {
		files = map[string]string{"inject/shared.md": "incoming\n", "feature/notes.md": "notes\n"}
	}
	source := f.replaceGlobalTemplate(t, "conflict", files)
	if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--template", "global:conflict"}, nil); err != nil {
		t.Fatal(err)
	}
	return f, f.ws.FeaturePath("feature"), source
}

func namedConflictWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func namedConflictRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func namedConflictOwned(t *testing.T, path string, role internal.ManagedTemplateRole, name string) (internal.ManagedTemplateState, internal.ManagedTemplateOwnedFile) {
	t.Helper()
	state, err := internal.LoadManagedTemplateState(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range state.Roles[string(role)].Files {
		if file.Path == name {
			return state, file
		}
	}
	t.Fatalf("missing owned file %s/%s", role, name)
	return state, internal.ManagedTemplateOwnedFile{}
}

func fakeNamedConflictTTY(t *testing.T) {
	t.Helper()
	prior := managedTemplateInputIsTTY
	managedTemplateInputIsTTY = func(io.Reader) bool { return true }
	t.Cleanup(func() { managedTemplateInputIsTTY = prior })
}

type namedConflictReader struct {
	reader io.Reader
	before func()
}

func (reader *namedConflictReader) Read(data []byte) (int, error) {
	if reader.before != nil {
		before := reader.before
		reader.before = nil
		before()
	}
	return reader.reader.Read(data)
}

type namedConflictFailWriter struct{}

func (namedConflictFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("decision output failed")
}

func TestNamedTemplateConflictFlagRoutingAndHelp(t *testing.T) {
	f, path, _ := newNamedConflictFixture(t, nil)
	for name, command := range map[string]*cobra.Command{"add": addCmd(), "sync": templateSyncCmd()} {
		out, _, err := runManagedCobra(t, command, []string{"--help"}, nil)
		if err != nil || !strings.Contains(out, "--resolve-conflicts") || !strings.Contains(out, "remembered") {
			t.Fatalf("%s help omits resolver contract: %q %v", name, out, err)
		}
	}
	for name, command := range map[string]*cobra.Command{"new": newCmd(), "open": openCmd(), "inject": injectCmd(), "rename": renameCmd(), "archive": archiveCmd(), "export": exportCmd()} {
		if command.Flags().Lookup("resolve-conflicts") != nil {
			t.Fatalf("resolver leaked to %s", name)
		}
	}
	legacy := f.ws.FeaturePath("legacy")
	if err := addCheckout(f.ws, "legacy", nil, "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, legacy)
	directory := managedCLIPackage(t, filepath.Join(f.root, "legacy-template"), map[string]string{"new.md": "legacy\n"}, "")
	for _, test := range []struct {
		command *cobra.Command
		args    []string
	}{
		{addCmd(), []string{"legacy", "--resolve-conflicts"}},
		{addCmd(), []string{"new-legacy", "--resolve-conflicts"}},
		{templateSyncCmd(), []string{"legacy", "--resolve-conflicts"}},
		{addCmd(), []string{"legacy", "--template", directory, "--resolve-conflicts"}},
		{templateSyncCmd(), []string{"--all", "--template", directory, "--resolve-conflicts"}},
	} {
		if _, _, err := runManagedCobra(t, test.command, test.args, strings.NewReader("keep\n")); err == nil {
			t.Fatalf("resolver selected/migrated legacy: %v", test.args)
		}
	}
	if after := snapshotTree(t, legacy); before != after {
		t.Fatal("resolver changed legacy files")
	}
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("")); err != nil {
		t.Fatalf("no-decision resolver run required a terminal: %v", err)
	}
	state, _ := namedConflictOwned(t, path, internal.ManagedTemplateRoleWorktree, "shared.md")
	if state.Version != 1 {
		t.Fatal("no-decision invocation eagerly converted v1")
	}
}

func TestNamedTemplateConflictChoicesRepeatAndExplicitReview(t *testing.T) {
	for _, choice := range []string{"k", "keep", "r", "replace", "d\nk", "diff\nkeep"} {
		t.Run(strings.ReplaceAll(choice, "\n", "-"), func(t *testing.T) {
			_, path, _ := newNamedConflictFixture(t, nil)
			file := filepath.Join(path, "inject", "shared.md")
			namedConflictWrite(t, file, "local\n")
			before := snapshotTree(t, path)
			if _, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, strings.NewReader("keep\n")); err == nil || !strings.Contains(err.Error(), "--resolve-conflicts") || strings.Contains(stderr, "[k/r") {
				t.Fatalf("absent flag prompted or did not explain eligible refusal: %v %q", err, stderr)
			}
			if snapshotTree(t, path) != before {
				t.Fatal("default refusal mutated feature")
			}
			fakeNamedConflictTTY(t)
			_, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader(choice+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"feature=\"feature\"", "role=worktree", "path=\"shared.md\"", "destination=", "source=",
				"current: sha256=", "incoming (rendered): sha256=", "mode=0644", "v1.2.18-rc.5", "workspace owner discovery",
				"managed-state readers/writers", "not a universal disable switch", "checkout tws new",
				"branch/stack metadata without reading the ledger", "template bytes and acknowledgements unchanged",
			} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("decision UI missing %q: %s", want, stderr)
				}
			}
			state, owned := namedConflictOwned(t, path, internal.ManagedTemplateRoleWorktree, "shared.md")
			if strings.HasPrefix(choice, "r") {
				if state.Version != 1 || owned.Override != nil || namedConflictRead(t, file) != "incoming\n" {
					t.Fatal("replace did not produce canonical v1 content")
				}
				return
			}
			if state.Version != 2 || owned.Override == nil || owned.Override.Action != "keep" || namedConflictRead(t, file) != "local\n" {
				t.Fatal("keep did not persist local override")
			}
			if strings.HasPrefix(choice, "d") && (!strings.Contains(stderr, "-local") || !strings.Contains(stderr, "+incoming") || strings.Count(stderr, "[k/r/d/e/C]") != 2) {
				t.Fatalf("diff failed to return to the unresolved menu: %s", stderr)
			}
			managedTemplateInputIsTTY = managedTemplateTerminalInput
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature"}, strings.NewReader("")); err != nil {
				t.Fatalf("ordinary repeat required terminal: %v", err)
			}
			before = snapshotTree(t, path)
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("replace\n")); err == nil || !strings.Contains(err.Error(), "terminal") {
				t.Fatalf("explicit override review accepted pipe/string input: %v", err)
			}
			if snapshotTree(t, path) != before {
				t.Fatal("refused explicit review changed state")
			}
			managedTemplateInputIsTTY = func(io.Reader) bool { return true }
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("replace\n")); err != nil {
				t.Fatal(err)
			}
			state, owned = namedConflictOwned(t, path, internal.ManagedTemplateRoleWorktree, "shared.md")
			if state.Version != 2 || owned.Override != nil || namedConflictRead(t, file) != "incoming\n" {
				t.Fatal("explicit review failed to clear override without downgrading")
			}
		})
	}
}

func TestNamedTemplateConflictCancellationInputAndAddSideEffects(t *testing.T) {
	for _, answer := range []string{"", "\n", "c\n", "cancel\n", "keep", "\x03\n", "unknown\ncancel\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			f, path, _ := newNamedConflictFixture(t, nil)
			log := installManagedLaunchFakes(t)
			namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
			before := snapshotTree(t, path)
			fakeNamedConflictTTY(t)
			input := &namedConflictReader{reader: strings.NewReader(answer), before: func() {
				lock, err := internal.AcquireManagedTemplateLock(path)
				if err != nil {
					t.Fatalf("writer lock held while prompting: %v", err)
				}
				if err := lock.Release(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(internal.CheckoutMutationLockPath(f.ws.CheckoutStateDir())); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("checkout mutation reservation held while prompting: %v", err)
				}
			}}
			if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--resolve-conflicts", "--force", "-n", "uncreated", "--open"}, input); err == nil {
				t.Fatal("cancel/input failure succeeded")
			}
			if snapshotTree(t, path) != before {
				t.Fatal("cancellation changed feature assets/selections")
			}
			if gitInDir(t, f.repo, "branch", "--list", "uncreated") != "" {
				t.Fatal("cancelled add created branch")
			}
			if _, err := os.Lstat(log); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled add launched agent: %v", err)
			}
		})
	}
	t.Run("reader-error", func(t *testing.T) {
		_, path, _ := newNamedConflictFixture(t, nil)
		namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
		before := snapshotTree(t, path)
		fakeNamedConflictTTY(t)
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, managedTemplateReadError{}); err == nil || !strings.Contains(err.Error(), "confirmation read failed") {
			t.Fatalf("input error not propagated: %v", err)
		}
		if snapshotTree(t, path) != before {
			t.Fatal("reader error mutated feature")
		}
	})
	t.Run("writer-error", func(t *testing.T) {
		_, path, _ := newNamedConflictFixture(t, nil)
		namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
		before := snapshotTree(t, path)
		fakeNamedConflictTTY(t)
		cmd := templateSyncCmd()
		cmd.SetArgs([]string{"feature", "--resolve-conflicts"})
		cmd.SetIn(strings.NewReader("keep\n"))
		cmd.SetErr(namedConflictFailWriter{})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "decision output failed") {
			t.Fatalf("output failure not propagated: %v", err)
		}
		if snapshotTree(t, path) != before {
			t.Fatal("writer error mutated feature")
		}
	})
}

func TestNamedTemplateConflictActualNonTerminalRefusal(t *testing.T) {
	f, path, _ := newNamedConflictFixture(t, nil)
	namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
	before := snapshotTree(t, path)
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close() //nolint:errcheck
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readPipe.Close() //nolint:errcheck
	if _, err := writePipe.WriteString("keep\n"); err != nil {
		t.Fatal(err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	answerPath := filepath.Join(f.root, "answers")
	namedConflictWrite(t, answerPath, "keep\n")
	regular, err := os.Open(answerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close() //nolint:errcheck
	for _, input := range []io.Reader{null, readPipe, regular, strings.NewReader("keep\n")} {
		if managedTemplateInputIsTTY(input) {
			t.Fatalf("nonterminal %T passed actual terminal check", input)
		}
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts", "--allow-managed-removal"}, input); err == nil || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("nonterminal consent accepted: %v", err)
		}
	}
	if snapshotTree(t, path) != before {
		t.Fatal("nonterminal attempts mutated feature")
	}
}

func TestNamedTemplateConflictEditorArgvSelectionAndSafeDiff(t *testing.T) {
	cases := []struct {
		command string
		want    []string
	}{
		{`"/a path/editor" --wait 'two words' empty="" escaped\ space`, []string{"/a path/editor", "--wait", "two words", "empty=", "escaped space"}},
		{`editor '$HOME' "$(touch sentinel)" a|b ';' '` + "`" + `literal` + "`" + `'`, []string{"editor", "$HOME", "$(touch sentinel)", "a|b", ";", "`literal`"}},
		{`editor "" 'a\b' "c\\d"`, []string{"editor", "", `a\b`, `c\d`}},
	}
	for _, tc := range cases {
		args, err := parseManagedTemplateEditor(tc.command)
		if err != nil || !reflect.DeepEqual(args, tc.want) {
			t.Fatalf("argv parse %q = %#v %v, want %#v", tc.command, args, err, tc.want)
		}
	}
	for _, invalid := range []string{"", "  ", `"" arg`, `editor 'missing`, `editor trailing\`} {
		if _, err := parseManagedTemplateEditor(invalid); err == nil {
			t.Fatalf("malformed editor command accepted: %q", invalid)
		}
	}
	t.Setenv("VISUAL", "visual --wait")
	t.Setenv("EDITOR", "editor")
	if managedTemplateEditorCommand() != "visual --wait" {
		t.Fatal("VISUAL precedence lost")
	}
	t.Setenv("VISUAL", "   ")
	if managedTemplateEditorCommand() != "editor" {
		t.Fatal("EDITOR fallback lost")
	}
	t.Setenv("EDITOR", "")
	if managedTemplateEditorCommand() != "vi" {
		t.Fatal("vi fallback lost")
	}
	var out bytes.Buffer
	if err := writeManagedTemplateDiff(&out, "local/\x1b]0;bad\a", "incoming/\nlabel", []byte("old\r\x1b[31m\a\n"), []byte("new\ttext\u202e\n")); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, unsafe := range []string{"\x1b", "\a", "\r", "\u202e"} {
		if strings.Contains(text, unsafe) {
			t.Fatalf("unsafe terminal control was presented verbatim: %q", text)
		}
	}
	if !strings.Contains(text, "@@ -1,1 +1,1 @@") || !strings.Contains(text, "new\ttext") {
		t.Fatalf("not a plain two-way unified diff: %q", text)
	}
}

func TestNamedTemplateConflictEditorAtomicSaveAcceptanceAndSharedReader(t *testing.T) {
	for _, answer := range []string{"e\nyes\n", "edit\n\nkeep\n", "edit\nno\ncancel\n"} {
		t.Run(strings.ReplaceAll(answer, "\n", "-"), func(t *testing.T) {
			f, path, source := newNamedConflictFixture(t, nil)
			file := filepath.Join(path, "inject", "shared.md")
			namedConflictWrite(t, file, "local\n")
			before := snapshotTree(t, path)
			fakeNamedConflictTTY(t)
			t.Setenv("VISUAL", `"/fake editor" --wait "argument with spaces" '$(no-shell)'`)
			t.Setenv("EDITOR", "must-not-run")
			oldRunner := managedTemplateRunEditor
			t.Cleanup(func() { managedTemplateRunEditor = oldRunner })
			var editedPath string
			input := strings.NewReader(answer)
			managedTemplateRunEditor = func(command *exec.Cmd) error {
				if !reflect.DeepEqual(command.Args[:4], []string{"/fake editor", "--wait", "argument with spaces", "$(no-shell)"}) {
					t.Fatalf("editor argv was shell-split/interpolated: %v", command.Args)
				}
				if command.Stdin != input {
					t.Fatal("editor was given buffered confirmation input")
				}
				if input.Len() != 0 {
					t.Fatal("test expected confirmation bytes to remain in the shared prompt reader, not editor stdin")
				}
				editedPath = command.Args[4]
				if !filepath.IsAbs(editedPath) || strings.HasPrefix(editedPath, path+string(filepath.Separator)) ||
					strings.HasPrefix(editedPath, source+string(filepath.Separator)) ||
					strings.HasPrefix(editedPath, f.repo+string(filepath.Separator)) {
					t.Fatalf("editor path was live/in-registry/relative: %q", editedPath)
				}
				info, err := os.Stat(filepath.Dir(editedPath))
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("scratch directory is not private: %v %v", info, err)
				}
				info, err = os.Stat(editedPath)
				if err != nil || info.Mode().Perm() != 0o600 || namedConflictRead(t, editedPath) != "local\n" {
					t.Fatalf("scratch edit did not start private/from current: %v %v", info, err)
				}
				if got := namedConflictRead(t, filepath.Join(filepath.Dir(editedPath), "incoming")); got != "incoming\n" {
					t.Fatalf("missing incoming reference: %q", got)
				}
				atomic := editedPath + ".atomic"
				if err := os.WriteFile(atomic, []byte("accepted manual bytes\n"), 0o600); err != nil {
					return err
				}
				return os.Rename(atomic, editedPath)
			}
			_, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, input)
			cancelled := strings.Contains(answer, "cancel")
			if (err != nil) != cancelled {
				t.Fatalf("editor acceptance/cancel result: err=%v stderr=%q", err, stderr)
			}
			if editedPath == "" {
				t.Fatal("explicit edit never invoked runner")
			}
			if _, err := os.Lstat(filepath.Dir(editedPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("verified editor scratch not cleaned: %v", err)
			}
			if cancelled {
				if snapshotTree(t, path) != before {
					t.Fatal("editor decline followed by cancel changed feature")
				}
				return
			}
			want, action := "accepted manual bytes\n", "edit"
			if strings.Contains(answer, "keep") {
				want, action = "local\n", "keep"
			}
			state, owned := namedConflictOwned(t, path, internal.ManagedTemplateRoleWorktree, "shared.md")
			if namedConflictRead(t, file) != want || state.Version != 2 || owned.Override == nil || owned.Override.Action != action {
				t.Fatalf("editor exit was treated as consent or bytes changed: %+v %q", owned, namedConflictRead(t, file))
			}
		})
	}
}

func TestNamedTemplateConflictEditorFailuresRetainPrivateEdits(t *testing.T) {
	for _, failure := range []string{"exit", "nul", "invalid-utf8", "symlink", "hardlink", "directory-swap", "reference-change", "permission", "residue", "accept-eof"} {
		t.Run(failure, func(t *testing.T) {
			f, path, _ := newNamedConflictFixture(t, nil)
			namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
			before := snapshotTree(t, path)
			fakeNamedConflictTTY(t)
			t.Setenv("VISUAL", "deterministic-fake-editor")
			oldRunner := managedTemplateRunEditor
			t.Cleanup(func() { managedTemplateRunEditor = oldRunner })
			outside := filepath.Join(f.root, "outside")
			if err := os.WriteFile(outside, []byte("outside sentinel\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var editPath, retained string
			managedTemplateRunEditor = func(command *exec.Cmd) error {
				editPath = command.Args[len(command.Args)-1]
				retained = editPath
				if err := os.WriteFile(editPath, []byte("scratch edits\n"), 0o600); err != nil {
					return err
				}
				switch failure {
				case "exit":
					return errors.New("fake editor exit 9")
				case "nul":
					return os.WriteFile(editPath, []byte("invalid\x00"), 0o600)
				case "invalid-utf8":
					return os.WriteFile(editPath, []byte{0xff}, 0o600)
				case "symlink", "hardlink":
					if err := os.Remove(editPath); err != nil {
						return err
					}
					if failure == "symlink" {
						return os.Symlink(outside, editPath)
					}
					return os.Link(outside, editPath)
				case "directory-swap":
					parent := filepath.Dir(editPath)
					if err := os.Rename(parent, parent+"-moved"); err != nil {
						return err
					}
					retained = filepath.Join(parent+"-moved", "edit")
					if err := os.Mkdir(parent, 0o700); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(parent, "sentinel"), []byte("new directory\n"), 0o600)
				case "reference-change":
					return os.WriteFile(filepath.Join(filepath.Dir(editPath), "incoming"), []byte("changed reference\n"), 0o600)
				case "permission":
					return os.Chmod(editPath, 0o644)
				case "residue":
					return os.WriteFile(filepath.Join(filepath.Dir(editPath), "user-backup"), []byte("do not delete\n"), 0o600)
				}
				return nil
			}
			input := "edit\nyes\n"
			if failure == "accept-eof" {
				input = "edit\n"
			}
			_, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), "staged edits retained") || !strings.Contains(err.Error(), editPath) {
				t.Fatalf("failed/unsafe editor output was not retained with location: %v", err)
			}
			if _, err := os.Lstat(retained); err != nil {
				t.Fatalf("editor edits were deleted: %v", err)
			}
			if namedConflictRead(t, outside) != "outside sentinel\n" || snapshotTree(t, path) != before {
				t.Fatal("failed editor changed live content or a hardlink target")
			}
			if failure == "residue" && namedConflictRead(t, filepath.Join(filepath.Dir(editPath), "user-backup")) != "do not delete\n" {
				t.Fatal("cleanup removed unrecognized editor residue")
			}
		})
	}
}

func TestNamedTemplateConflictMixedBatchConsentPreflightAndRemoval(t *testing.T) {
	for _, scenario := range []string{"late-hard", "cancel-second", "decline-removal", "accept", "allow-removal"} {
		t.Run(scenario, func(t *testing.T) {
			f := newManagedCLIFixture(t)
			source := f.replaceGlobalTemplate(t, "batch-conflict", map[string]string{"inject/shared.md": "incoming\n", "inject/obsolete.md": "obsolete\n"})
			for _, feature := range []string{"alpha", "zeta"} {
				if _, _, err := runManagedCobra(t, addCmd(), []string{feature, "--template", "global:batch-conflict"}, nil); err != nil {
					t.Fatal(err)
				}
				namedConflictWrite(t, filepath.Join(f.ws.FeaturePath(feature), "inject", "shared.md"), feature+" local\n")
			}
			if err := addCheckout(f.ws, "legacy", nil, "", "", false, false, false); err != nil {
				t.Fatal(err)
			}
			managedCLIPackage(t, filepath.Join(f.repo, ".tws", "templates"), map[string]string{"legacy-new.md": "legacy copied\n"}, "")
			if err := os.Remove(filepath.Join(source, "inject", "obsolete.md")); err != nil {
				t.Fatal(err)
			}
			if scenario == "late-hard" {
				if err := os.Chmod(filepath.Join(f.ws.FeaturePath("zeta"), "inject", "shared.md"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]string{}
			for _, feature := range []string{"alpha", "zeta", "legacy"} {
				before[feature] = snapshotTree(t, f.ws.FeaturePath(feature))
			}
			fakeNamedConflictTTY(t)
			input, args := "k\nk\nyes\n", []string{"--all", "--resolve-conflicts"}
			switch scenario {
			case "cancel-second":
				input = "keep\ncancel\n"
			case "decline-removal":
				input = "keep\nkeep\nno\n"
			case "allow-removal":
				input = "keep\nkeep\n"
				args = append(args, "--allow-managed-removal")
			}
			read := false
			reader := &namedConflictReader{reader: strings.NewReader(input), before: func() { read = true }}
			_, stderr, err := runManagedCobra(t, templateSyncCmd(), args, reader)
			success := scenario == "accept" || scenario == "allow-removal"
			if (err == nil) != success {
				t.Fatalf("batch consent result: scenario=%s err=%v stderr=%s", scenario, err, stderr)
			}
			if scenario == "late-hard" && (read || strings.Contains(stderr, "[k/r")) {
				t.Fatal("prompted before later hard preflight completed")
			}
			if !success {
				for feature, snapshot := range before {
					if snapshotTree(t, f.ws.FeaturePath(feature)) != snapshot {
						t.Fatalf("cancel/error after earlier decisions changed %s", feature)
					}
				}
				return
			}
			if !strings.Contains(stderr, "legacy copy-if-missing") || strings.Index(stderr, `feature="alpha"`) > strings.Index(stderr, `feature="zeta"`) {
				t.Fatalf("batch disclosure/order missing: %s", stderr)
			}
			for _, feature := range []string{"alpha", "zeta"} {
				path := f.ws.FeaturePath(feature)
				if namedConflictRead(t, filepath.Join(path, "inject", "shared.md")) != feature+" local\n" {
					t.Fatalf("batch lost %s decision", feature)
				}
				if _, err := os.Lstat(filepath.Join(path, "inject", "obsolete.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("approved obsolete removal did not run: %v", err)
				}
			}
			legacyNew := filepath.Join(f.ws.FeaturePath("legacy"), "inject", "legacy-new.md")
			if namedConflictRead(t, legacyNew) != "legacy copied\n" {
				t.Fatal("successful batch omitted legacy copying")
			}
		})
	}
}

func TestNamedTemplateConflictPromptDriftBeforeWholeBatchEffects(t *testing.T) {
	for _, drift := range []string{"local", "mode", "source", "ledger", "feature-root"} {
		t.Run(drift, func(t *testing.T) {
			f, path, source := newNamedConflictFixture(t, nil)
			namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
			fakeNamedConflictTTY(t)
			var expected string
			reader := &namedConflictReader{reader: strings.NewReader("keep\n"), before: func() {
				switch drift {
				case "local":
					namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "concurrent\n")
				case "mode":
					if err := os.Chmod(filepath.Join(path, "inject", "shared.md"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "source":
					namedConflictWrite(t, filepath.Join(source, "inject", "shared.md"), "source concurrent\n")
				case "ledger":
					ledger := internal.ManagedTemplateStatePath(path)
					namedConflictWrite(t, ledger, namedConflictRead(t, ledger)+"\n")
				case "feature-root":
					if err := os.Rename(path, path+"-moved"); err != nil {
						t.Fatal(err)
					}
					copyManagedTreeForTest(t, path+"-moved", path)
				}
				expected = snapshotTree(t, path)
			}}
			if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--resolve-conflicts", "-n", "uncreated"}, reader); err == nil {
				t.Fatal("decision survived prompt-time evidence drift")
			}
			if snapshotTree(t, path) != expected || gitInDir(t, f.repo, "branch", "--list", "uncreated") != "" {
				t.Fatal("drift refusal changed assets or add branch")
			}
		})
	}
}
func TestNamedTemplateConflictEditorRejectsProtectedScratchRoots(t *testing.T) {
	for _, root := range []string{"feature", "worktree", "registry"} {
		t.Run(root, func(t *testing.T) {
			f, path, source := newNamedConflictFixture(t, nil)
			namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
			base := path
			switch root {
			case "worktree":
				base = f.repo
			case "registry":
				base = source
			}
			t.Setenv("TMPDIR", base)
			t.Setenv("VISUAL", "must-not-launch")
			fakeNamedConflictTTY(t)
			old := managedTemplateRunEditor
			managedTemplateRunEditor = func(*exec.Cmd) error { t.Fatal("editor launched in a protected root"); return nil }
			t.Cleanup(func() { managedTemplateRunEditor = old })
			before := snapshotTree(t, path)
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("edit\n")); err == nil || !strings.Contains(err.Error(), "outside") {
				t.Fatalf("unsafe scratch root accepted: %v", err)
			}
			if snapshotTree(t, path) != before {
				t.Fatal("unsafe scratch root changed feature")
			}
		})
	}
}

func TestNamedTemplateConflictRealFakeEditorAndRenderedNativeDiff(t *testing.T) {
	t.Run("real-fake-editor", func(t *testing.T) {
		f, path, _ := newNamedConflictFixture(t, nil)
		namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
		editor := filepath.Join(f.root, "fake editor with spaces")
		script := "#!/bin/sh\n[ \"$1\" = '--wait' ] || exit 8\n[ \"$#\" -eq 2 ] || exit 9\nprintf 'real fake edited bytes\\n' > \"$2\"\n"
		if err := os.WriteFile(editor, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("VISUAL", fmt.Sprintf("%q --wait", editor))
		t.Setenv("EDITOR", "never-use-personal-editor")
		fakeNamedConflictTTY(t)
		if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("edit\nyes\n")); err != nil {
			t.Fatal(err)
		}
		if got := namedConflictRead(t, filepath.Join(path, "inject", "shared.md")); got != "real fake edited bytes\n" {
			t.Fatalf("quoted fake editor command did not run: %q", got)
		}
	})
	t.Run("native-rendering", func(t *testing.T) {
		f := newManagedCLIFixture(t)
		if err := internal.SaveConfigFile(internal.RepoConfigPathFor(f.repo), internal.Config{
			WorkspaceMode: "checkout", AgentCommand: "copilot",
		}); err != nil {
			t.Fatal(err)
		}
		native := ".github/instructions/tws-worktree.instructions.md"
		pkg := managedCLIPackage(t, filepath.Join(f.root, "native-source"), map[string]string{"inject/" + native: "raw native body\n"},
			"version: 1\nguidance:\n  worktree: FINAL MANIFEST GUIDANCE\n")
		if err := internal.AddNamedTemplate("", internal.TemplateScopeGlobal, "native", pkg); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--template", "global:native"}, nil); err != nil {
			t.Fatal(err)
		}
		path := f.ws.FeaturePath("feature")
		namedConflictWrite(t, filepath.Join(path, "inject", filepath.FromSlash(native)), "no native frontmatter\n")
		before := snapshotTree(t, path)
		fakeNamedConflictTTY(t)
		_, stderr, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("diff\nkeep\n"))
		if err == nil || !strings.Contains(err.Error(), "applyTo") || !strings.Contains(stderr, "+applyTo:") || !strings.Contains(stderr, "+FINAL MANIFEST GUIDANCE") {
			t.Fatalf("native UI compared raw source or accepted invalid keep: err=%v stderr=%s", err, stderr)
		}
		if snapshotTree(t, path) != before {
			t.Fatal("invalid native keep changed files")
		}
	})
}

func TestNamedTemplateConflictCustomizedDeletionAndRecoveryNeverPrompt(t *testing.T) {
	for _, blocker := range []string{"obsolete", "missing-custom", "removed-source", "orphan-checkout-recovery", "pending-template-recovery"} {
		t.Run(blocker, func(t *testing.T) {
			f, path, source := newNamedConflictFixture(t, nil)
			namedConflictWrite(t, filepath.Join(path, "inject", "shared.md"), "local\n")
			fakeNamedConflictTTY(t)
			if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{"feature", "--resolve-conflicts"}, strings.NewReader("keep\n")); err != nil {
				t.Fatal(err)
			}
			switch blocker {
			case "obsolete":
				if err := os.Remove(filepath.Join(source, "inject", "shared.md")); err != nil {
					t.Fatal(err)
				}
			case "missing-custom":
				if err := os.Remove(filepath.Join(path, "inject", "shared.md")); err != nil {
					t.Fatal(err)
				}
			case "removed-source":
				if err := os.Rename(source, source+"-moved"); err != nil {
					t.Fatal(err)
				}
			case "orphan-checkout-recovery":
				statePath := filepath.Join(f.ws.CheckoutStateDir(), "other-checkout-sync.yaml")
				if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
					t.Fatal(err)
				}
				namedConflictWrite(t, statePath, "unreadable-or-future-state\n")
			case "pending-template-recovery":
				if err := os.Mkdir(filepath.Join(path, ".tws-template-recovery"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotTree(t, path)
			reader := &namedConflictReader{reader: strings.NewReader("replace\nyes\n"), before: func() { t.Fatal("hard blocker became a menu") }}
			if _, _, err := runManagedCobra(t, addCmd(), []string{"feature", "--resolve-conflicts", "--allow-managed-removal", "--force"}, reader); err == nil {
				t.Fatal("consent flags bypassed hard blocker")
			}
			if snapshotTree(t, path) != before {
				t.Fatal("hard blocker changed custom files/metadata")
			}
		})
	}
}

func TestNamedTemplateConflictBatchPartialFailureReportsFeatureAndRecovery(t *testing.T) {
	f := newManagedCLIFixture(t)
	f.replaceGlobalTemplate(t, "partial", map[string]string{"inject/shared.md": "incoming\n"})
	for _, feature := range []string{"alpha", "zeta"} {
		if _, _, err := runManagedCobra(t, addCmd(), []string{feature, "--template", "global:partial"}, nil); err != nil {
			t.Fatal(err)
		}
		namedConflictWrite(t, filepath.Join(f.ws.FeaturePath(feature), "inject", "shared.md"), "local\n")
	}
	fakeNamedConflictTTY(t)
	old := internal.ManagedTemplateApplyStepHook
	t.Cleanup(func() { internal.ManagedTemplateApplyStepHook = old })
	internal.ManagedTemplateApplyStepHook = func(step, path string) error {
		if step == "before-state" && path == internal.ManagedTemplateStatePath(f.ws.FeaturePath("zeta")) {
			return errors.New("injected zeta publication failure")
		}
		return nil
	}
	stdout, _, err := runManagedCobra(t, templateSyncCmd(), []string{"--all", "--resolve-conflicts"}, strings.NewReader("keep\nkeep\n"))
	if err == nil || !strings.Contains(err.Error(), "zeta:") || !strings.Contains(err.Error(), "after 0/1 effect(s)") ||
		!strings.Contains(err.Error(), "preserved pending evidence") || !strings.Contains(stdout, "alpha: applying") {
		t.Fatalf("partial progress was not truthful: stdout=%s err=%v", stdout, err)
	}
	state, file := namedConflictOwned(t, f.ws.FeaturePath("alpha"), internal.ManagedTemplateRoleWorktree, "shared.md")
	if state.Version != 2 || file.Override == nil {
		t.Fatal("failure of later feature rolled back completed feature")
	}
	if _, err := internal.ManagedTemplateStatePresent(f.ws.FeaturePath("zeta")); err == nil {
		t.Fatal("failed feature retry was not blocked by recovery")
	}
}

func TestNamedTemplateConflictExternalOrchestratorScopePreservesLinks(t *testing.T) {
	f := newInjectFixture(t, "scoped-conflict")
	t.Setenv("TWS_ROOT", f.workspace)
	t.Chdir(f.repo)
	f.addWorktree(t, f.repo, "nested/entry", "scoped-conflict-entry")
	if err := internal.SaveStack(f.featurePath, internal.Stack{Branches: []internal.StackEntry{{Name: "nested/entry", Branch: "scoped-conflict-entry"}}}); err != nil {
		t.Fatal(err)
	}
	old := managedTemplateTmuxProbe
	managedTemplateTmuxProbe = managedTemplateTestTmuxProbe{}
	t.Cleanup(func() { managedTemplateTmuxProbe = old })
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "both", "--agent", "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	namedConflictWrite(t, filepath.Join(f.featurePath, "CLAUDE.md"), "orchestrator local\n")
	fakeNamedConflictTTY(t)
	if _, _, err := runManagedCobra(t, templateSyncCmd(), []string{f.feature, "--scope", "orchestrator", "--resolve-conflicts"}, strings.NewReader("keep\n")); err != nil {
		t.Fatalf("orchestrator-only decision could not preserve worktree links: %v", err)
	}
	after, err := internal.LoadManagedTemplateState(f.featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Roles["worktree"], after.Roles["worktree"]) {
		t.Fatal("orchestrator decision changed unselected link/ownership metadata")
	}
}
