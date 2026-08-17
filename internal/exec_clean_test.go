package internal

import (
	"io"
	"os"
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
