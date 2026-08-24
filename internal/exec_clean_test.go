package internal

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr swaps os.Stderr for an os.Pipe around fn. runWithFilteredStderr
// writes through fmt.Fprintln(os.Stderr, ...) at call time, so the swap is
// observable.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestRunDirClean_StderrFilter pins the three behaviours of the shipped filter
// exactly as they are: hint/advice removal, the duplicate-commit reformat, and
// verbatim (untrimmed) forwarding of everything else. Blank lines are dropped.
func TestRunDirClean_StderrFilter(t *testing.T) {
	dir := t.TempDir()
	script := strings.Join([]string{
		`printf 'hint: this is advice\n' 1>&2`,
		`printf 'Disable this message with something\n' 1>&2`,
		`printf 'warning: skipped previously applied commit deadbee\n' 1>&2`,
		`printf '\n' 1>&2`,
		`printf '   \n' 1>&2`,
		`printf '  indented real error\n' 1>&2`,
		`printf 'plain real error\n' 1>&2`,
	}, "; ")

	out := captureStderr(t, func() {
		if err := RunDirClean(dir, "sh", "-c", script); err != nil {
			t.Fatalf("RunDirClean: %v", err)
		}
	})

	if strings.Contains(out, "hint: this is advice") {
		t.Fatalf("hint: lines must be dropped:\n%s", out)
	}
	if strings.Contains(out, "Disable this message") {
		t.Fatalf("advice lines must be dropped:\n%s", out)
	}
	if strings.Contains(out, "skipped previously applied commit") {
		t.Fatalf("the duplicate-commit line must be reformatted:\n%s", out)
	}
	if !strings.Contains(out, "    (skipped duplicate commit)\n") {
		t.Fatalf("expected the reformatted duplicate-commit line:\n%s", out)
	}
	// Non-matching lines are forwarded UNTRIMMED, leading whitespace included.
	if !strings.Contains(out, "  indented real error\n") {
		t.Fatalf("non-matching lines must be forwarded untrimmed:\n%q", out)
	}
	if !strings.Contains(out, "plain real error\n") {
		t.Fatalf("real errors must survive the filter:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.TrimSpace(line) == "" {
			t.Fatalf("blank lines must be dropped, got %q in:\n%q", line, out)
		}
	}
}

func TestRunDirClean_PropagatesExitStatus(t *testing.T) {
	dir := t.TempDir()
	out := captureStderr(t, func() {
		if err := RunDirClean(dir, "sh", "-c", "printf 'boom\\n' 1>&2; exit 3"); err == nil {
			t.Fatal("a non-zero child must produce an error")
		}
	})
	if !strings.Contains(out, "boom") {
		t.Fatalf("the failing child's stderr must reach the user:\n%s", out)
	}
}

// captureStdoutAndStderr swaps both os.Stdout and os.Stderr for os.Pipes
// around fn and returns each stream's captured bytes independently, mirroring
// captureStderr above but for functions (like RunDir/RunDirTo called with
// os.Stdout/os.Stderr) that write to both process-global streams at once.
func captureStdoutAndStderr(t *testing.T, fn func()) (stdout string, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW

	doneOut := make(chan string, 1)
	doneErr := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(outR)
		doneOut <- string(data)
	}()
	go func() {
		data, _ := io.ReadAll(errR)
		doneErr <- string(data)
	}()

	fn()

	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	stdout = <-doneOut
	stderr = <-doneErr
	_ = outR.Close()
	_ = errR.Close()
	return stdout, stderr
}

// TestRunDirTo proves RunDirTo, called with os.Stdout/os.Stderr exactly as
// RunDir hard-wires them internally, is observationally identical to RunDir
// (same captured stdout, same captured stderr, same error/exit-status
// behaviour for both a successful and a failing child) — and, separately,
// that RunDirTo's two writers are honoured independently: stdout content
// never leaks into the stderr writer or vice versa, and a caller-supplied
// pair of writers that are NOT os.Stdout/os.Stderr (plain *bytes.Buffer
// values) is exactly where the child's two streams land.
func TestRunDirTo(t *testing.T) {
	dir := t.TempDir()
	script := "printf 'out-line\\n'; printf 'err-line\\n' 1>&2"

	t.Run("observationally identical to RunDir on success", func(t *testing.T) {
		viaRunDirOut, viaRunDirErr := captureStdoutAndStderr(t, func() {
			if err := RunDir(dir, "sh", "-c", script); err != nil {
				t.Fatalf("RunDir: %v", err)
			}
		})
		viaRunDirToOut, viaRunDirToErr := captureStdoutAndStderr(t, func() {
			if err := RunDirTo(os.Stdout, os.Stderr, dir, "sh", "-c", script); err != nil {
				t.Fatalf("RunDirTo: %v", err)
			}
		})
		if viaRunDirOut != viaRunDirToOut {
			t.Errorf("stdout differs: RunDir=%q RunDirTo(os.Stdout,os.Stderr,...)=%q", viaRunDirOut, viaRunDirToOut)
		}
		if viaRunDirErr != viaRunDirToErr {
			t.Errorf("stderr differs: RunDir=%q RunDirTo(os.Stdout,os.Stderr,...)=%q", viaRunDirErr, viaRunDirToErr)
		}
		if viaRunDirOut != "out-line\n" || viaRunDirErr != "err-line\n" {
			t.Fatalf("test fixture assumption broken: stdout=%q stderr=%q", viaRunDirOut, viaRunDirErr)
		}
	})

	t.Run("observationally identical to RunDir on exit-status propagation", func(t *testing.T) {
		failScript := "printf 'boom\\n' 1>&2; exit 5"
		var runDirErr, runDirToErr error
		captureStdoutAndStderr(t, func() { runDirErr = RunDir(dir, "sh", "-c", failScript) })
		captureStdoutAndStderr(t, func() { runDirToErr = RunDirTo(os.Stdout, os.Stderr, dir, "sh", "-c", failScript) })
		if runDirErr == nil || runDirToErr == nil {
			t.Fatalf("both must report the non-zero exit: RunDir err=%v RunDirTo err=%v", runDirErr, runDirToErr)
		}
		if runDirErr.Error() != runDirToErr.Error() {
			t.Errorf("error text differs: RunDir=%q RunDirTo=%q", runDirErr.Error(), runDirToErr.Error())
		}
	})

	t.Run("both writers honoured independently", func(t *testing.T) {
		var stdoutBuf, stderrBuf bytes.Buffer
		if err := RunDirTo(&stdoutBuf, &stderrBuf, dir, "sh", "-c", script); err != nil {
			t.Fatalf("RunDirTo: %v", err)
		}
		if got := stdoutBuf.String(); got != "out-line\n" {
			t.Errorf("stdout writer = %q, want exactly \"out-line\\n\" (no stderr content must leak in)", got)
		}
		if got := stderrBuf.String(); got != "err-line\n" {
			t.Errorf("stderr writer = %q, want exactly \"err-line\\n\" (no stdout content must leak in)", got)
		}
	})

	t.Run("dir argument is honoured", func(t *testing.T) {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", dir, err)
		}
		var stdoutBuf, stderrBuf bytes.Buffer
		if err := RunDirTo(&stdoutBuf, &stderrBuf, dir, "pwd"); err != nil {
			t.Fatalf("RunDirTo pwd: %v", err)
		}
		gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(stdoutBuf.String()))
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", stdoutBuf.String(), err)
		}
		if gotDir != resolved {
			t.Errorf("RunDirTo ran in %q, want %q", gotDir, resolved)
		}
	})
}
