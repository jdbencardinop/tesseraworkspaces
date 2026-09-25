package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// This file owns no matrix cell: it is the runner's own unit suite, and the
// argv/source audit it feeds is owned by package cli (AC-031, AC-055).
// ---------------------------------------------------------------------------

// TestRunReparentGit_CapturesBothStreams proves the runner's whole contract
// on a success: stdout is returned to the caller rather than written to this
// process's stdout, stderr is captured too, and the exit code is git's own.
// The runner's whole contract in one leaf: both streams captured, the directory selected with cmd.Dir, stdin from the caller, the exit code reported, and a spawn failure reported as -1 (AC-031, AC-052, AC-055, AC-058).
//
// §17.3 counts one t.Run leaf per real-Git cell, so the assertions below are
// grouped into a single leaf. Each original test keeps its own scope, its own
// repository and its own assertions verbatim; only the leaf boundary moved.
func TestRunReparentGit_TheOnlySpawner(t *testing.T) {
	first := newReparentPrimitiveRepo(t)
	second := newReparentPrimitiveRepo(t)
	second.Commit("four.txt", "four")
	// --- TestRunReparentGit_CapturesBothStreams ---
	func(t *testing.T) {
		repo := first
		head := repo.RevParse("HEAD")

		res, err := runReparentGit(repo.Dir, nil, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse HEAD: %v (stderr %q)", err, res.Stderr)
		}
		if got := strings.TrimSpace(string(res.Stdout)); got != head {
			t.Fatalf("stdout = %q, want %q", got, head)
		}
		if len(res.Stderr) != 0 {
			t.Fatalf("stderr = %q, want empty", res.Stderr)
		}
		if res.ExitCode != 0 {
			t.Fatalf("exit code = %d, want 0", res.ExitCode)
		}
	}(t)

	// --- TestRunReparentGit_SelectsDirectoryWithCmdDir ---
	func(t *testing.T) {
		if first.RevParse("HEAD") == second.RevParse("HEAD") {
			t.Fatal("fixture repositories must have different heads")
		}

		log := reparentCaptureArgv(t)
		for _, repo := range []*reparentRepo{first, second} {
			res, err := runReparentGit(repo.Dir, nil, "rev-parse", "HEAD")
			if err != nil {
				t.Fatalf("rev-parse in %s: %v", repo.Dir, err)
			}
			if got := strings.TrimSpace(string(res.Stdout)); got != repo.RevParse("HEAD") {
				t.Fatalf("rev-parse in %s = %q, want %q", repo.Dir, got, repo.RevParse("HEAD"))
			}
		}
		if len(*log) != 2 {
			t.Fatalf("argv log has %d entries, want 2", len(*log))
		}
		reparentAssertNoDashC(t, *log)
		for _, argv := range *log {
			if len(argv) == 0 || argv[0] != "rev-parse" {
				t.Fatalf("argv %v does not start at the git subcommand", argv)
			}
		}
	}(t)

	// --- TestRunReparentGit_StdinComesFromCaller ---
	func(t *testing.T) {
		repo := first

		payload := []byte("reparent stdin payload\n")
		supplied, err := runReparentGit(repo.Dir, payload, "hash-object", "--stdin")
		if err != nil {
			t.Fatalf("hash-object with payload: %v (stderr %q)", err, supplied.Stderr)
		}
		expected := gitHashObjectOfBytes(t, repo, payload)
		if got := strings.TrimSpace(string(supplied.Stdout)); got != expected {
			t.Fatalf("hash of supplied payload = %q, want %q", got, expected)
		}

		empty, err := runReparentGit(repo.Dir, nil, "hash-object", "--stdin")
		if err != nil {
			t.Fatalf("hash-object with nil stdin: %v (stderr %q)", err, empty.Stderr)
		}
		emptyExpected := gitHashObjectOfBytes(t, repo, nil)
		if got := strings.TrimSpace(string(empty.Stdout)); got != emptyExpected {
			t.Fatalf("nil stdin hashed %q, want the empty-object hash %q", got, emptyExpected)
		}
	}(t)

	// --- TestRunReparentGit_NonZeroExitIsReported ---
	func(t *testing.T) {
		repo := first

		quiet, err := runReparentGit(repo.Dir, nil, "rev-parse", "--verify", "--quiet", "refs/heads/nope")
		if err == nil {
			t.Fatal("expected a non-zero exit for a missing ref")
		}
		if quiet.ExitCode != 1 {
			t.Fatalf("exit code = %d, want 1", quiet.ExitCode)
		}

		loud, err := runReparentGit(repo.Dir, nil, "cat-file", "-t", "0000000000000000000000000000000000000000")
		if err == nil {
			t.Fatal("expected a non-zero exit for a missing object")
		}
		if loud.ExitCode <= 0 {
			t.Fatalf("exit code = %d, want a positive git exit status", loud.ExitCode)
		}
		if len(loud.Stderr) == 0 {
			t.Fatal("stderr must be captured so a refusal can quote git's own sentence")
		}
	}(t)

	// --- TestRunReparentGit_SpawnFailureReportsMinusOne ---
	func(t *testing.T) {
		repo := first
		missing := filepath.Join(repo.Dir, "no-such-directory")

		res, err := runReparentGit(missing, nil, "rev-parse", "HEAD")
		if err == nil {
			t.Fatal("expected a spawn failure for a missing working directory")
		}
		if res.ExitCode != -1 {
			t.Fatalf("exit code = %d, want -1 when git never ran", res.ExitCode)
		}
	}(t)

	// Replay measurement excludes merge commits and binds the exact oldest-
	// first non-merge sequence into both count and digest.
	func(t *testing.T) {
		repo := first
		base := repo.RevParse("HEAD")
		repo.Git("switch", "-q", "-c", "replay-side")
		side := repo.Commit("side.txt", "side")
		repo.Git("switch", "-q", "main")
		mainline := repo.Commit("mainline.txt", "mainline")
		repo.Git("merge", "-q", "--no-ff", "-m", "merge replay-side", "replay-side")
		merge := repo.RevParse("HEAD")

		log := reparentCaptureArgv(t)
		replay, err := measureReparentReplay(repo.Dir, base, "refs/heads/main")
		if err != nil {
			t.Fatal(err)
		}
		wantCommits := []string{side, mainline}
		if strings.Join(replay.Commits, ",") != strings.Join(wantCommits, ",") {
			t.Fatalf("replay commits = %v, want non-merges %v (merge %s excluded)", replay.Commits, wantCommits, merge)
		}
		if replay.CandidateCount == nil || *replay.CandidateCount != len(wantCommits) {
			t.Fatalf("candidate count = %v", replay.CandidateCount)
		}
		sum := sha256.Sum256([]byte(strings.Join(wantCommits, "\n")))
		if replay.CandidateDigest == nil || *replay.CandidateDigest != hex.EncodeToString(sum[:]) {
			t.Fatalf("candidate digest = %v", replay.CandidateDigest)
		}
		found := false
		for _, argv := range *log {
			if strings.Join(argv, " ") ==
				"rev-list --no-merges --reverse "+base+"..refs/heads/main" {
				found = true
			}
		}
		if !found {
			t.Fatalf("exact replay argv missing from %v", *log)
		}
	}(t)
}

// TestRunReparentGit_SelectsDirectoryWithCmdDir proves the directory is
// chosen with exec.Cmd.Dir and never with a `-C` argv: two repositories, two
// different answers, and an argv log with no -C in it.

// TestRunReparentGit_StdinComesFromCaller proves the two stdin cases: a
// caller buffer is delivered verbatim, and a nil buffer is NOT os.Stdin — the
// child reads immediate EOF instead of blocking on this process's terminal.

// gitHashObjectOfBytes computes git's own hash of content through a file, so
// the expectation never depends on the code under test.
func gitHashObjectOfBytes(t *testing.T, repo *reparentRepo, content []byte) string {
	t.Helper()
	path := filepath.Join(repo.Dir, "stdin-fixture.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return repo.Git("hash-object", path)
}

// TestRunReparentGit_NonZeroExitIsReported proves a refusal can quote git's
// own sentence: exit code 1 with an error, and captured stderr for a fatal.

// TestRunReparentGit_SpawnFailureReportsMinusOne proves the -1 arm: git never
// ran at all, so there is no exit status git chose.
