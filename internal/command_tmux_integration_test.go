//go:build tmux_integration

package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type commandTmuxCapture struct {
	Path       string   `json:"path"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

// Intercept the isolated child before testing's flag parser, so even zero
// arguments and option-like values reach the real executable unchanged.
func init() {
	if capture := os.Getenv("TWS_TEST_TMUX_ARGV_CAPTURE"); capture != "" {
		path, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		data, err := json.Marshal(commandTmuxCapture{
			Path: path, Executable: os.Args[0], Args: append([]string{}, os.Args[1:]...),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(capture, data, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(15 * time.Second)
		os.Exit(0)
	}
}

// Run with -tags=tmux_integration; this test requires the real optional tmux
// binary and never substitutes a mock for its command parser.
func TestCheckoutCommandTmuxLiteralTransport(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal("tmux_integration requires tmux:", err)
	}
	tmux, err = filepath.Abs(tmux)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	edges := []string{"", ";", "literal;", ";;", "literal;;", "-;", "; ", "middle;value", "line\n;", "*", "not-a-tmux-command"}
	for n := 1; n <= 6; n++ {
		backslashes := strings.Repeat(`\`, n)
		edges = append(edges, backslashes+";", "prefix"+backslashes+";", ";"+backslashes, backslashes+";middle")
	}
	for _, tc := range []struct {
		name, session, directory string
		args                     []string
	}{
		{"control", "literal-command;", `cwd\;`, []string{"literal", "ordinary"}},
		{"zero", "literal-command;", `cwd\;`, []string{}},
		{"semicolons-and-backslashes", "literal-command;", `cwd\;`, edges},
		{"format-variables", "feat#{pid}", "cwd#{pid}", []string{"#{pid}", "#", "##"}},
		{"format-jobs", "feat#(tws-format-job)", "cwd#(tws-format-job)", []string{"#(tws-format-job)", "#{pid}"}},
		{"literal-hashes", "feat###hash##", "cwd####hash#", []string{"#", "##", "###", "####"}},
		{"formats-and-semicolons", "feat##{pid}#(tws-format-job);", `cwd#{pid}#(tws-format-job)\;`, []string{"#(tws-format-job);", `\#{pid};`, "##;"}},
		{"formats-zero", "feat#{pid}#(tws-format-job);", "cwd#{pid}#(tws-format-job);", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			shim := "#!/bin/sh\nexec \"$TWS_TEST_REAL_TMUX\" -S \"$TWS_TEST_TMUX_SOCKET\" -f /dev/null \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(shim), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TWS_TEST_REAL_TMUX", tmux)
			t.Setenv("TWS_TEST_TMUX_SOCKET", "literal.sock")
			home := filepath.Join(root, "home")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			jobMarker := filepath.Join(root, "unexpected-format-job")
			t.Setenv("TWS_TEST_FORMAT_JOB_MARKER", jobMarker)
			if err := os.WriteFile(filepath.Join(bin, "tws-format-job"),
				[]byte("#!/bin/sh\nprintf job > \"$TWS_TEST_FORMAT_JOB_MARKER\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(root, "capture.json")
			t.Setenv("TWS_TEST_TMUX_ARGV_CAPTURE", capture)
			program := filepath.Join(root, "editor#{pid}#(tws-format-job) ' "+`\;`)
			if err := os.Symlink(executable, program); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, tc.directory)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			command := append([]string{program}, tc.args...)
			runner := RealSessionTmuxRunner{}
			name := tc.session
			const token = "literal-command-test-owner"
			if err := runner.NewCommandSession(name, dir, command, token); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := runner.KillCommandSession(name, token); err != nil {
					t.Error(err)
				}
			})
			if present, err := runner.OwnedCommandSession(name, token); err != nil || !present {
				t.Fatalf("command session ownership: present=%t err=%v", present, err)
			}
			names, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
			if err != nil || string(names) != name+"\n" {
				t.Fatalf("tmux changed the selected name: %q %v", names, err)
			}
			var got commandTmuxCapture
			deadline := time.Now().Add(5 * time.Second)
			for {
				data, err := os.ReadFile(capture)
				if err == nil && json.Unmarshal(data, &got) == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("child did not capture literal argv: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := commandTmuxCapture{Path: dir, Executable: program, Args: tc.args}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("tmux changed literal values:\n got: %#v\nwant: %#v", got, want)
			}
			// Format jobs are asynchronous; inspect after the command is
			// live as well as after ownership-verified cleanup.
			time.Sleep(100 * time.Millisecond)
			if _, err := os.Lstat(jobMarker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a selected name/directory or child argv launched a format job: %v", err)
			}
			if err := runner.KillCommandSession(name, token); err != nil {
				t.Fatal(err)
			}
			if present, err := runner.OwnedCommandSession(name, token); err != nil || present {
				t.Fatalf("owned cleanup failed: present=%t err=%v", present, err)
			}
			if _, err := os.Lstat(jobMarker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("format job marker appeared during cleanup: %v", err)
			}
		})
	}
}
