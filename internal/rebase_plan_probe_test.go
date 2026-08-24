package internal

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// Shared fixture helpers for TestRebasePlanProbe*.
//
// rppNeutralize composes this package's two existing git-environment
// neutralization idioms — ssNeutralizeGitConfig (stack_status_test.go), which
// makes an injected host GIT_CONFIG_KEY_n/VALUE_n pair inert, and gitInTest's
// own explicit HOME override (checkout_health_test.go), which only applies
// inside one gitInTest call — with the third ingredient every DIRECT call
// into rebase_plan_probe.go's own runGit/runGitRaw/runGitStreamLines needs:
// those helpers set no cmd.Env at all (confirmed by reading runGit, runGitRaw
// and runGitStreamLines in internal/rebase_plan_probe.go — none of the three
// assigns cmd.Env) and therefore inherit this
// test process's real environment, including its real $HOME and therefore
// its real ~/.gitconfig, unless HOME is overridden at the process-env level
// for the whole test, not merely inside one gitInTest call. This mirrors the
// established pattern of the pre-existing HOME-overriding tests in
// internal/workspace_test.go, internal/registry_test.go and
// internal/session_test.go, each of which calls t.Setenv("HOME", ...) at the
// process-env level for the same reason.
// ============================================================================

func rppNeutralize(t *testing.T) {
	t.Helper()
	ssNeutralizeGitConfig(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
}

// rppRepo builds a fresh, fully neutralized Git repository with one empty
// commit on branch "main" and returns its working-tree root.
func rppRepo(t *testing.T) string {
	t.Helper()
	rppNeutralize(t)
	dir := t.TempDir()
	gitInTest(t, dir, "init", "--initial-branch=main")
	gitInTest(t, dir, "commit", "--allow-empty", "-m", "init")
	return dir
}

// rppNotARepo returns a fully neutralized, empty directory that is not a
// Git repository (or inside one) at all.
func rppNotARepo(t *testing.T) string {
	t.Helper()
	rppNeutralize(t)
	return t.TempDir()
}

// rppHeadSHA returns dir's current HEAD commit SHA.
func rppHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	return gitInTest(t, dir, "rev-parse", "HEAD")
}

// rppCommitN adds n more empty commits to dir, messages "<prefix>-0".."<prefix>-(n-1)".
func rppCommitN(t *testing.T, dir string, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		gitInTest(t, dir, "commit", "--allow-empty", "-m", fmt.Sprintf("%s-%d", prefix, i))
	}
}

// rppCommonDir returns repo's canonical (absolute, cleaned) common Git
// directory, exactly as MeasureContextRoots itself resolves it, so legacy
// remotes/branches fixture files can be written where readLegacyRemoteFile /
// readLegacyBranchFile will actually look for them.
func rppCommonDir(t *testing.T, repo string) string {
	t.Helper()
	out := gitInTest(t, repo, "rev-parse", "--git-common-dir")
	if filepath.IsAbs(out) {
		return filepath.Clean(out)
	}
	return filepath.Clean(filepath.Join(repo, out))
}

// rppWriteLegacyRemoteFileAt is rppWriteLegacyRemoteFile against an
// already-known commonDir (no repository required — ResolveFetchRemote and
// readLegacyRemoteFile themselves need no git process at all).
func rppWriteLegacyRemoteFileAt(t *testing.T, commonDir, name, url string, pulls []string) {
	t.Helper()
	dir := filepath.Join(commonDir, "remotes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", url)
	for _, p := range pulls {
		fmt.Fprintf(&b, "Pull: %s\n", p)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rppAddValuelessConfig appends a genuinely valueless occurrence of a
// dot-free (no-subsection) key directly to repo's local config file. The
// `git config --add` CLI always requires two arguments (empirically
// confirmed: "error: wrong number of arguments, should be 2") so a valueless
// occurrence — a bare "key" line with no "=value", which Git's own file
// grammar treats as boolean true — can only be produced by writing the INI
// text directly, exactly as a hand-edited .gitconfig would.
func rppAddValuelessConfig(t *testing.T, repo, key string) {
	t.Helper()
	dot := strings.IndexByte(key, '.')
	if dot <= 0 {
		t.Fatalf("rppAddValuelessConfig: key %q must be section.variable with no subsection", key)
	}
	section, variable := key[:dot], key[dot+1:]
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "[%s]\n\t%s\n", section, variable); err != nil {
		t.Fatal(err)
	}
}

// rppAddGitlink stages one gitlink (mode 160000) index entry at path,
// pointing at an arbitrary 40-hex SHA that never needs to resolve to a real
// object — empirically confirmed: `git update-index --add --cacheinfo
// 160000,<sha>,<path>` accepts and lists it via `ls-files --stage`
// regardless of whether that object exists.
func rppAddGitlink(t *testing.T, repo, sha, path string) {
	t.Helper()
	gitInTest(t, repo, "update-index", "--add", "--cacheinfo", fmt.Sprintf("160000,%s,%s", sha, path))
}

// rppFakeGitlinkSHA is a syntactically valid 40-hex object id used for
// gitlink fixtures whose target never needs to resolve to a real object.
const rppFakeGitlinkSHA = "1111111111111111111111111111111111111111"

// rppBulkAddGitlinks stages N gitlink entries in one `update-index
// --index-info` batch (mode SP sha TAB path per line, empirically
// confirmed), all sharing rppFakeGitlinkSHA, at paths fn(i) for i in [0,n).
func rppBulkAddGitlinks(t *testing.T, repo string, n int, fn func(i int) string) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "160000 %s\t%s\n", rppFakeGitlinkSHA, fn(i))
	}
	cmd := exec.Command("git", "-C", repo, "update-index", "--index-info")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update-index --index-info failed: %v\n%s", err, out)
	}
}

// rppInitNestedRepo `git init`s a real, independent repository at dir with
// one empty commit, so `git ls-files --stage` (which listPresentGitlinks
// shells out to) succeeds against it — a plain "mkdir .git" marker is NOT
// enough for that (only for submodulePopulated's own Lstat-only check).
func rppInitNestedRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, dir, "init", "-q", "--initial-branch=main")
	gitInTest(t, dir, "commit", "--allow-empty", "-m", "init")
}

// rppMarkPopulated fabricates the cheapest artefact submodulePopulated
// accepts as "populated": a plain, non-symlink ".git" directory. It does
// NOT make subRoot a working Git repository — callers that also need
// listPresentGitlinks to succeed against subRoot must use
// rppInitNestedRepo instead.
func rppMarkPopulated(t *testing.T, subRoot string) {
	t.Helper()
	if err := os.MkdirAll(subRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(subRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// rppBulkCreateBranches creates n local branches, all pointing at HEAD, via
// one `update-ref --stdin` batch (empirically confirmed: "create
// refs/heads/<name> <rev>" per line).
func rppBulkCreateBranches(t *testing.T, repo string, names []string) {
	t.Helper()
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "create refs/heads/%s HEAD\n", name)
	}
	cmd := exec.Command("git", "-C", repo, "update-ref", "--stdin")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo)
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update-ref --stdin failed: %v\n%s", err, out)
	}
}

// rppGitGlobal writes to the *global* config file of the neutralized $HOME
// that rppNeutralize's t.Setenv put in this test's process environment —
// deliberately NOT via gitInTest, whose own cmd.Env hardcodes "HOME=<the
// repo dir passed to it>" independent of, and unaffected by, rppNeutralize's
// t.Setenv override. rebase_plan_probe.go's own runGit/runGitRaw set no
// cmd.Env at all, so they inherit this process's environment (and therefore
// the rppNeutralize-configured HOME, not a gitInTest-local one) — a global
// occurrence written by gitInTest would be invisible to any subsequent
// production-code read, and this helper exists so the two agree.
func rppGitGlobal(t *testing.T, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--global", key, value)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config --global %s %s failed: %v\n%s", key, value, err, out)
	}
}

// rppWorktreeGitDir resolves and returns the linked worktree's own resolved
// Git directory by reading its ".git" pointer file directly (independent of
// the resolveWorktreeGitDir function under test, so tests of that function
// have an oracle to compare against).
func rppWorktreeGitDir(t *testing.T, wtPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wtPath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	after, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		t.Fatalf("malformed gitdir pointer file: %q", data)
	}
	if !filepath.IsAbs(after) {
		after = filepath.Join(wtPath, after)
	}
	return filepath.Clean(after)
}

// ============================================================================
// Context identity (§4.4, §14.1a)
// ============================================================================

func TestRebasePlanProbe_ContextIdentity(t *testing.T) {
	t.Run("DeterministicAndCanonicalAcrossCwd", func(t *testing.T) {
		repo := rppRepo(t)
		sub := filepath.Join(repo, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		fromRoot, err := EstablishContextIdentity(repo, "entry-repo")
		if err != nil {
			t.Fatalf("EstablishContextIdentity(root): %v", err)
		}
		fromSub, err := EstablishContextIdentity(sub, "entry-repo")
		if err != nil {
			t.Fatalf("EstablishContextIdentity(sub): %v", err)
		}
		if fromRoot != fromSub {
			t.Fatalf("identity from repo root %+v != identity from subdirectory %+v", fromRoot, fromSub)
		}
		if fromRoot.ContextID == "" || len(fromRoot.ContextID) != 64 {
			t.Fatalf("expected 64-hex context_id, got %q", fromRoot.ContextID)
		}
		if fromRoot.RepoRoot == "" || fromRoot.CommonDir == "" {
			t.Fatalf("expected non-empty canonical roots, got %+v", fromRoot)
		}
		// Calling it again must reproduce byte-identical output (determinism).
		again, err := EstablishContextIdentity(repo, "entry-repo")
		if err != nil {
			t.Fatal(err)
		}
		if again != fromRoot {
			t.Fatalf("second call produced a different identity: %+v vs %+v", again, fromRoot)
		}
	})

	t.Run("DistinguishesRootSource", func(t *testing.T) {
		repo := rppRepo(t)
		asEntry, err := EstablishContextIdentity(repo, "entry-repo")
		if err != nil {
			t.Fatal(err)
		}
		asWorktree, err := EstablishContextIdentity(repo, "worktree")
		if err != nil {
			t.Fatal(err)
		}
		if asEntry.ContextID == asWorktree.ContextID {
			t.Fatalf("expected different root_source labels to yield different context_id, both got %q", asEntry.ContextID)
		}
		if asEntry.RepoRoot != asWorktree.RepoRoot || asEntry.CommonDir != asWorktree.CommonDir {
			t.Fatalf("root_source must be the only difference; got %+v vs %+v", asEntry, asWorktree)
		}
	})

	t.Run("MeasureContextRootsErrorsOutsideRepo", func(t *testing.T) {
		notRepo := rppNotARepo(t)
		if _, _, err := MeasureContextRoots(notRepo); err == nil {
			t.Fatal("expected an error measuring context roots outside any Git repository")
		}
		if _, err := EstablishContextIdentity(notRepo, "entry-repo"); err == nil {
			t.Fatal("expected EstablishContextIdentity to propagate the same error")
		}
	})
}

func TestRebasePlanProbe_BuildContextIdentities(t *testing.T) {
	t.Run("CollapsesDuplicateResolvingRequests", func(t *testing.T) {
		repo := rppRepo(t)
		sub := filepath.Join(repo, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		ids, err := BuildContextIdentities([]ContextIdentityRequest{
			{Dir: repo, RootSource: "entry-repo"},
			{Dir: sub, RootSource: "entry-repo"}, // resolves to the same (repoRoot, source, commonDir) tuple
		})
		if err != nil {
			t.Fatalf("BuildContextIdentities: %v", err)
		}
		if len(ids) != 1 {
			t.Fatalf("expected two duplicate-resolving requests to collapse onto one map entry, got %d: %+v", len(ids), ids)
		}
	})

	t.Run("FailsClosedOnFirstMeasurementError", func(t *testing.T) {
		repo := rppRepo(t)
		notRepo := rppNotARepo(t)
		_, err := BuildContextIdentities([]ContextIdentityRequest{
			{Dir: repo, RootSource: "entry-repo"},
			{Dir: notRepo, RootSource: "entry-repo"},
		})
		if err == nil {
			t.Fatal("expected an error when any request cannot be measured")
		}
	})
}

// ============================================================================
// Candidate probes (§5 rules 4-10)
// ============================================================================

func TestRebasePlanProbe_CandidateCount(t *testing.T) {
	repo := rppRepo(t)
	base := rppHeadSHA(t, repo)
	gitInTest(t, repo, "checkout", "-q", "-b", "feature")
	rppCommitN(t, repo, 3, "feature")

	count, err := ProbeCandidateCount(repo, base, "feature")
	if err != nil {
		t.Fatalf("ProbeCandidateCount: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	zero, err := ProbeCandidateCount(repo, "feature", "feature")
	if err != nil {
		t.Fatalf("ProbeCandidateCount(empty range): %v", err)
	}
	if zero != 0 {
		t.Fatalf("count for an empty range = %d, want 0", zero)
	}
}

func TestRebasePlanProbe_FirstCandidate(t *testing.T) {
	t.Run("NilForNonPositiveCountWithoutRunningGit", func(t *testing.T) {
		// A directory that is not even a Git repository would make any git
		// invocation fail; ProbeFirstCandidate must return (nil, nil)
		// without attempting one, for both count == 0 and a negative count.
		notRepo := rppNotARepo(t)
		cand, err := ProbeFirstCandidate(notRepo, "upstream", "branch", 0)
		if err != nil || cand != nil {
			t.Fatalf("count==0: got (%v, %v), want (nil, nil) without running git", cand, err)
		}
		cand, err = ProbeFirstCandidate(notRepo, "upstream", "branch", -5)
		if err != nil || cand != nil {
			t.Fatalf("count==-5: got (%v, %v), want (nil, nil) without running git", cand, err)
		}
	})

	t.Run("ReturnsOldestCandidateNotNewest", func(t *testing.T) {
		repo := rppRepo(t)
		base := rppHeadSHA(t, repo)
		gitInTest(t, repo, "checkout", "-q", "-b", "feature")
		gitInTest(t, repo, "commit", "--allow-empty", "-m", "oldest-candidate")
		oldestSHA := rppHeadSHA(t, repo)
		gitInTest(t, repo, "commit", "--allow-empty", "-m", "newest-candidate")

		count, err := ProbeCandidateCount(repo, base, "feature")
		if err != nil {
			t.Fatal(err)
		}
		cand, err := ProbeFirstCandidate(repo, base, "feature", count)
		if err != nil {
			t.Fatalf("ProbeFirstCandidate: %v", err)
		}
		if cand == nil {
			t.Fatal("expected a non-nil first candidate")
		}
		if cand.SHA != oldestSHA {
			t.Fatalf("first candidate SHA = %s, want the OLDEST commit %s (not the newest)", cand.SHA, oldestSHA)
		}
		if cand.Short != shortSHA(oldestSHA) {
			t.Fatalf("first candidate Short = %s, want %s", cand.Short, shortSHA(oldestSHA))
		}
		if cand.Subject != "oldest-candidate" {
			t.Fatalf("first candidate Subject = %q, want %q", cand.Subject, "oldest-candidate")
		}
	})
}

func TestRebasePlanProbe_CandidateStream(t *testing.T) {
	t.Run("EmptyRangeDigestIsSHA256OfZeroBytes", func(t *testing.T) {
		repo := rppRepo(t)
		result, err := ProbeCandidateStream(repo, "main", "main")
		if err != nil {
			t.Fatalf("ProbeCandidateStream: %v", err)
		}
		if len(result.Commits) != 0 || result.Listed != 0 || result.Truncated {
			t.Fatalf("expected an empty result for an empty range, got %+v", result)
		}
		wantEmptyDigest := hex.EncodeToString(sha256.New().Sum(nil))
		if result.CandidateDigest != wantEmptyDigest {
			t.Fatalf("CandidateDigest = %s, want SHA-256 of zero bytes %s", result.CandidateDigest, wantEmptyDigest)
		}
	})

	t.Run("OrderedOldestFirstAndDigestCoversFullStream", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "checkout", "-q", "-b", "feature")
		var shas []string
		for i := 0; i < 5; i++ {
			gitInTest(t, repo, "commit", "--allow-empty", "-m", fmt.Sprintf("c%d", i))
			shas = append(shas, rppHeadSHA(t, repo))
		}
		result, err := ProbeCandidateStream(repo, "main", "feature")
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Commits) != 5 || result.Listed != 5 || result.Truncated {
			t.Fatalf("expected 5 untruncated commits, got %+v", result)
		}
		for i, sha := range shas {
			if result.Commits[i] != sha {
				t.Fatalf("Commits[%d] = %s, want %s (oldest-first order)", i, result.Commits[i], sha)
			}
		}
		h := sha256.New()
		for _, sha := range shas {
			h.Write([]byte(sha))
			h.Write([]byte("\n"))
		}
		want := hex.EncodeToString(h.Sum(nil))
		if result.CandidateDigest != want {
			t.Fatalf("CandidateDigest = %s, want %s", result.CandidateDigest, want)
		}
	})

	t.Run("TruncatesRetentionPast50ButDigestCoversEntireStream", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "checkout", "-q", "-b", "feature")
		const total = 55
		var shas []string
		for i := 0; i < total; i++ {
			gitInTest(t, repo, "commit", "--allow-empty", "-m", fmt.Sprintf("c%d", i))
			shas = append(shas, rppHeadSHA(t, repo))
		}
		result, err := ProbeCandidateStream(repo, "main", "feature")
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Commits) != 50 {
			t.Fatalf("expected retention capped at 50, got %d", len(result.Commits))
		}
		if result.Listed != total {
			t.Fatalf("Listed = %d, want the true total %d", result.Listed, total)
		}
		if !result.Truncated {
			t.Fatal("expected Truncated == true when Listed exceeds the retention cap")
		}
		for i := 0; i < 50; i++ {
			if result.Commits[i] != shas[i] {
				t.Fatalf("Commits[%d] = %s, want %s", i, result.Commits[i], shas[i])
			}
		}
		h := sha256.New()
		for _, sha := range shas { // every one of the 55, not just the retained 50
			h.Write([]byte(sha))
			h.Write([]byte("\n"))
		}
		want := hex.EncodeToString(h.Sum(nil))
		if result.CandidateDigest != want {
			t.Fatalf("CandidateDigest must cover the entire 55-commit stream, not just the retained 50: got %s, want %s", result.CandidateDigest, want)
		}
	})
}

func TestRebasePlanProbe_MayDropPatchEquivalent(t *testing.T) {
	t.Run("TrueWhenUpstreamHasExclusiveCommits", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "checkout", "-q", "-b", "feature")
		gitInTest(t, repo, "checkout", "-q", "main")
		gitInTest(t, repo, "commit", "--allow-empty", "-m", "upstream-only")

		result, err := ProbeMayDropPatchEquivalent(repo, "main", "feature")
		if err != nil {
			t.Fatalf("ProbeMayDropPatchEquivalent: %v", err)
		}
		if result == nil || !*result {
			t.Fatalf("expected true (upstream has exclusive commits), got %v", result)
		}
	})

	t.Run("FalseWhenUpstreamHasNoExclusiveCommits", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "checkout", "-q", "-b", "feature")
		gitInTest(t, repo, "commit", "--allow-empty", "-m", "feature-only")

		result, err := ProbeMayDropPatchEquivalent(repo, "main", "feature")
		if err != nil {
			t.Fatal(err)
		}
		if result == nil || *result {
			t.Fatalf("expected false (upstream has nothing feature lacks), got %v", result)
		}
	})
}

// ============================================================================
// Config inventory core (parse/decode primitives)
// ============================================================================

func TestRebasePlanProbe_GitConfigInventory(t *testing.T) {
	t.Run("CapturesScopeCaseAndValuelessEntries", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "--local", "rebase.autosquash", "true")
		rppAddValuelessConfig(t, repo, "rebase.stat")

		inv := ProbeGitConfigInventory(repo)
		if !inv.Available || inv.Err != nil {
			t.Fatalf("inventory = %+v, want Available=true Err=nil", inv)
		}
		var sawAutosquash, sawStat bool
		for _, e := range inv.Entries {
			if e.Key == "rebase.autosquash" {
				sawAutosquash = true
				if e.Scope != "local" {
					t.Fatalf("rebase.autosquash scope = %q, want local", e.Scope)
				}
				if !e.ValuePresent || e.Value != "true" {
					t.Fatalf("rebase.autosquash entry = %+v, want ValuePresent=true Value=true", e)
				}
			}
			if e.Key == "rebase.stat" {
				sawStat = true
				if e.ValuePresent {
					t.Fatalf("valueless rebase.stat entry reported ValuePresent=true: %+v", e)
				}
			}
		}
		if !sawAutosquash || !sawStat {
			t.Fatalf("expected both rebase.autosquash and rebase.stat entries, got %+v", inv.Entries)
		}
	})

	t.Run("SubsectionCasePreservedVariableLowercased", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "MyRemote", "https://example.invalid/repo.git")

		inv := ProbeGitConfigInventory(repo)
		if !inv.Available {
			t.Fatalf("inventory unavailable: %v", inv.Err)
		}
		var found bool
		for _, e := range inv.Entries {
			if strings.EqualFold(e.Key, "remote.MyRemote.url") {
				found = true
				if e.Key != "remote.MyRemote.url" {
					t.Fatalf("expected subsection case preserved verbatim as remote.MyRemote.url, got %q", e.Key)
				}
			}
		}
		if !found {
			t.Fatalf("expected a remote.MyRemote.url entry, got %+v", inv.Entries)
		}
	})

	t.Run("UnavailableForANonExistentDirectory", func(t *testing.T) {
		rppNeutralize(t)
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		inv := ProbeGitConfigInventory(missing)
		if inv.Available {
			t.Fatalf("expected Available=false for a directory git -C cannot enter, got %+v", inv)
		}
		if inv.Err == nil {
			t.Fatal("expected a non-nil Err")
		}
		if inv.Entries != nil {
			t.Fatalf("expected nil Entries on a failed read (never a partial slice), got %+v", inv.Entries)
		}
	})
}

func TestRebasePlanProbe_ParseGitConfigInventory(t *testing.T) {
	rec := func(fields ...string) string { return strings.Join(fields, "\x00") + "\x00" }

	t.Run("EmptyInputYieldsNonNilEmptySlice", func(t *testing.T) {
		entries, err := parseGitConfigInventory(nil)
		if err != nil {
			t.Fatal(err)
		}
		if entries == nil {
			t.Fatal("expected a non-nil empty slice for empty input")
		}
		if len(entries) != 0 {
			t.Fatalf("expected zero entries, got %d", len(entries))
		}
	})

	t.Run("ValuelessAndValuedRecordsBothDecode", func(t *testing.T) {
		raw := rec("local") + rec("rebase.stat") + rec("worktree") + rec("rebase.autosquash\ntrue")
		entries, err := parseGitConfigInventory([]byte(raw))
		if err != nil {
			t.Fatalf("parseGitConfigInventory: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
		}
		if entries[0].Key != "rebase.stat" || entries[0].ValuePresent {
			t.Fatalf("entry[0] = %+v, want key rebase.stat, valueless", entries[0])
		}
		if entries[1].Key != "rebase.autosquash" || !entries[1].ValuePresent || entries[1].Value != "true" {
			t.Fatalf("entry[1] = %+v, want key rebase.autosquash value true", entries[1])
		}
	})

	t.Run("FailsClosedWithoutTrailingNUL", func(t *testing.T) {
		raw := strings.TrimSuffix(rec("local")+rec("rebase.stat"), "\x00")
		if _, err := parseGitConfigInventory([]byte(raw)); err == nil {
			t.Fatal("expected an error when the stream does not end with NUL")
		}
	})

	t.Run("FailsClosedOnOddRecordCount", func(t *testing.T) {
		raw := rec("local") // a scope with no paired key/key-value record
		if _, err := parseGitConfigInventory([]byte(raw)); err == nil {
			t.Fatal("expected an error for an odd number of NUL-terminated records")
		}
	})

	t.Run("FailsClosedOnEmptyKey", func(t *testing.T) {
		raw := rec("local") + rec("")
		if _, err := parseGitConfigInventory([]byte(raw)); err == nil {
			t.Fatal("expected an error for an empty key")
		}
	})
}

func TestRebasePlanProbe_ParseGitConfigBool(t *testing.T) {
	cases := []struct {
		name         string
		value        string
		valuePresent bool
		wantValid    bool
		wantBool     bool
	}{
		{"Valueless", "", false, true, true},
		{"ExplicitEmpty", "", true, true, false},
		{"TrueWord", "true", true, true, true},
		{"YesWord", "yes", true, true, true},
		{"OnWord", "on", true, true, true},
		{"FalseWord", "false", true, true, false},
		{"NoWord", "no", true, true, false},
		{"OffWord", "off", true, true, false},
		{"MixedCaseTrue", "TrUe", true, true, true},
		{"NonZeroInt", "  7  ", true, true, true},
		{"ZeroInt", "0", true, true, false},
		{"NegativeInt", "-3", true, true, true},
		{"Unparseable", "sideways", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, ok := parseGitConfigBool(tc.value, tc.valuePresent)
			if ok != tc.wantValid {
				t.Fatalf("valid = %v, want %v", ok, tc.wantValid)
			}
			if ok && b != tc.wantBool {
				t.Fatalf("bool = %v, want %v", b, tc.wantBool)
			}
		})
	}
}

func TestRebasePlanProbe_FirstInvalidBooleanOccurrence(t *testing.T) {
	inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
		{Scope: "system", Key: "rebase.autosquash", ValuePresent: true, Value: "true"},
		{Scope: "global", Key: "rebase.autosquash", ValuePresent: true, Value: "sideways"},
		{Scope: "local", Key: "rebase.autosquash", ValuePresent: true, Value: "false"},
	}}
	entry, ok := firstInvalidBooleanOccurrence(inv, "rebase.autosquash")
	if !ok {
		t.Fatal("expected to find the invalid occurrence")
	}
	if entry.Scope != "global" {
		t.Fatalf("expected git's own first-invalid-wins semantics to report the GLOBAL occurrence (git aborts at the first unparseable value in file order, even though a later LOCAL occurrence is valid), got scope %q", entry.Scope)
	}

	t.Run("NoneWhenAllValid", func(t *testing.T) {
		valid := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "system", Key: "rebase.autosquash", ValuePresent: true, Value: "true"},
			{Scope: "local", Key: "rebase.autosquash", ValuePresent: true, Value: "false"},
		}}
		if _, ok := firstInvalidBooleanOccurrence(valid, "rebase.autosquash"); ok {
			t.Fatal("expected no invalid occurrence when every value parses")
		}
	})
}

func TestRebasePlanProbe_RebaseMergesGrammar(t *testing.T) {
	cases := []struct {
		name          string
		value         string
		valuePresent  bool
		wantValid     bool
		wantRecreates bool
	}{
		{"RebaseCousins", "rebase-cousins", true, true, true},
		{"NoRebaseCousins", "no-rebase-cousins", true, true, true},
		{"Valueless", "", false, true, true},
		{"True", "true", true, true, true},
		{"False", "false", true, true, false},
		{"Unparseable", "sideways", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recreates, ok := rebaseMergesGrammar(tc.value, tc.valuePresent)
			if ok != tc.wantValid {
				t.Fatalf("valid = %v, want %v", ok, tc.wantValid)
			}
			if ok && recreates != tc.wantRecreates {
				t.Fatalf("recreates = %v, want %v", recreates, tc.wantRecreates)
			}
		})
	}
}

func TestRebasePlanProbe_ProbeTypedBoolConfig(t *testing.T) {
	t.Run("AbsentKeyReportsAbsentNotError", func(t *testing.T) {
		repo := rppRepo(t)
		status, _, err := probeTypedBoolConfig(repo, "rebase.autoSquash")
		if err != nil {
			t.Fatalf("probeTypedBoolConfig: %v", err)
		}
		if status != typedBoolAbsent {
			t.Fatalf("status = %v, want typedBoolAbsent for an unset key", status)
		}
	})

	t.Run("ValidTrueValue", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "--local", "rebase.autoSquash", "true")
		status, value, err := probeTypedBoolConfig(repo, "rebase.autoSquash")
		if err != nil {
			t.Fatal(err)
		}
		if status != typedBoolValid || !value {
			t.Fatalf("status/value = %v/%v, want typedBoolValid/true", status, value)
		}
	})

	t.Run("InvalidValueReportsInvalidStatusNotError", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "--local", "rebase.autoSquash", "sideways")
		status, _, err := probeTypedBoolConfig(repo, "rebase.autoSquash")
		if err != nil {
			t.Fatalf("expected the bad-boolean exit(128) to be reported as typedBoolInvalid, not as a Go error: %v", err)
		}
		if status != typedBoolInvalid {
			t.Fatalf("status = %v, want typedBoolInvalid", status)
		}
	})

	t.Run("FirstBadOccurrenceWinsEvenBeforeALaterValidOne", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "--local", "--add", "rebase.autoSquash", "sideways")
		gitInTest(t, repo, "config", "--local", "--add", "rebase.autoSquash", "true")
		status, _, err := probeTypedBoolConfig(repo, "rebase.autoSquash")
		if err != nil {
			t.Fatal(err)
		}
		if status != typedBoolInvalid {
			t.Fatalf("status = %v, want typedBoolInvalid (git aborts at the first unparseable occurrence regardless of a later valid one)", status)
		}
	})
}

func TestRebasePlanProbe_DeriveBooleanSlot(t *testing.T) {
	t.Run("Absent", func(t *testing.T) {
		repo := rppRepo(t)
		inv := ProbeGitConfigInventory(repo)
		slot, issue := deriveBooleanSlot(inv, repo, "rebase.updateRefs", "rebase.updaterefs")
		if slot.Status != "absent" {
			t.Fatalf("Status = %q, want absent", slot.Status)
		}
		if issue != nil {
			t.Fatalf("expected a nil issue entry on the absent branch, got %+v", issue)
		}
	})

	t.Run("ValidSetsFormattedValueAndLiteralProbedSource", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "--local", "rebase.updateRefs", "true")
		inv := ProbeGitConfigInventory(repo)
		slot, issue := deriveBooleanSlot(inv, repo, "rebase.updateRefs", "rebase.updaterefs")
		if slot.Status != "valid" {
			t.Fatalf("Status = %q, want valid", slot.Status)
		}
		if slot.Value == nil || *slot.Value != "true" {
			t.Fatalf("Value = %v, want pointer to \"true\"", slot.Value)
		}
		if slot.Source == nil || *slot.Source != "probed" {
			t.Fatalf("Source = %v, want pointer to the literal \"probed\" (not the real scope)", slot.Source)
		}
		if issue != nil {
			t.Fatalf("expected a nil issue entry on the valid branch, got %+v", issue)
		}
	})

	t.Run("InvalidLeavesValueNilButSetsRealScopeSourceAndNonNilIssue", func(t *testing.T) {
		repo := rppRepo(t)
		rppGitGlobal(t, "rebase.updateRefs", "sideways")
		inv := ProbeGitConfigInventory(repo)
		slot, issue := deriveBooleanSlot(inv, repo, "rebase.updateRefs", "rebase.updaterefs")
		if slot.Status != "invalid" {
			t.Fatalf("Status = %q, want invalid", slot.Status)
		}
		if slot.Value != nil {
			t.Fatalf("Value = %v, want nil on the invalid branch", slot.Value)
		}
		if slot.Source == nil || *slot.Source != "global" {
			t.Fatalf("Source = %v, want pointer to the REAL scope \"global\" (not \"probed\")", slot.Source)
		}
		if issue == nil || issue.Value != "sideways" {
			t.Fatalf("expected a non-nil offending entry with Value=sideways, got %+v", issue)
		}
	})
}

// ============================================================================
// Slot derivation for keys with no typed read, issue minting, and the
// composite per-context repository-config result.
// ============================================================================

func TestRebasePlanProbe_DeriveRebaseMergesSlot(t *testing.T) {
	t.Run("AbsentInterpretsAsFalse", func(t *testing.T) {
		inv := GitConfigInventory{Available: true}
		slot, issue := deriveRebaseMergesSlot(inv)
		if slot.Status != "absent" || slot.Interpretation != "false" {
			t.Fatalf("slot = %+v, want Status=absent Interpretation=false", slot)
		}
		if issue != nil {
			t.Fatalf("expected nil issue on the absent branch, got %+v", issue)
		}
	})

	t.Run("ValidValuelessInterpretsAsTrueWithNonNilEmptyValue", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "local", Key: "rebase.rebasemerges", ValuePresent: false},
		}}
		slot, issue := deriveRebaseMergesSlot(inv)
		if slot.Status != "valid" || slot.Interpretation != "true" {
			t.Fatalf("slot = %+v, want Status=valid Interpretation=true", slot)
		}
		if slot.Value == nil || *slot.Value != "" {
			t.Fatalf("Value = %v, want a non-nil pointer to the empty string (a known valueless value, not null)", slot.Value)
		}
		if issue != nil {
			t.Fatalf("expected nil issue on the valid branch, got %+v", issue)
		}
	})

	t.Run("InvalidCarriesInvalidInterpretationAndNonNilIssue", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "worktree", Key: "rebase.rebasemerges", ValuePresent: true, Value: "sideways"},
		}}
		slot, issue := deriveRebaseMergesSlot(inv)
		if slot.Status != "invalid" || slot.Interpretation != "invalid" {
			t.Fatalf("slot = %+v, want Status=invalid Interpretation=invalid", slot)
		}
		if slot.Source == nil || *slot.Source != "worktree" {
			t.Fatalf("Source = %v, want pointer to worktree", slot.Source)
		}
		if issue == nil || issue.Value != "sideways" {
			t.Fatalf("expected a non-nil offending entry, got %+v", issue)
		}
	})
}

func TestRebasePlanProbe_DeriveBackendSlot(t *testing.T) {
	t.Run("Absent", func(t *testing.T) {
		inv := GitConfigInventory{Available: true}
		slot, issue, kind := deriveBackendSlot(inv, true)
		if slot.Status != "absent" || issue != nil || kind != "" {
			t.Fatalf("got slot=%+v issue=%v kind=%q, want Status=absent nil-issue empty-kind", slot, issue, kind)
		}
	})

	t.Run("ValuelessAtAnyPositionIsInvalidRegardlessOfALaterValidOccurrence", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "system", Key: "rebase.backend", ValuePresent: true, Value: "merge"},
			{Scope: "global", Key: "rebase.backend", ValuePresent: false}, // valueless, offending
			{Scope: "local", Key: "rebase.backend", ValuePresent: true, Value: "apply"},
		}}
		slot, issue, kind := deriveBackendSlot(inv, true)
		if slot.Status != "invalid" {
			t.Fatalf("Status = %q, want invalid", slot.Status)
		}
		if slot.Source == nil || *slot.Source != "global" {
			t.Fatalf("Source = %v, want pointer to the offending GLOBAL scope, ignoring the later valid local occurrence", slot.Source)
		}
		if issue == nil || issue.Scope != "global" {
			t.Fatalf("issue = %+v, want the global valueless occurrence", issue)
		}
		if kind != "missing-backend-value" {
			t.Fatalf("errorKind = %q, want missing-backend-value", kind)
		}
	})

	t.Run("UnknownNameAtFinalOccurrenceGatedByNonForcingRebasePresent", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "local", Key: "rebase.backend", ValuePresent: true, Value: "sideways"},
		}}

		slot, issue, kind := deriveBackendSlot(inv, true)
		if slot.Status != "invalid" {
			t.Fatalf("Status = %q, want invalid", slot.Status)
		}
		if issue == nil || kind != "unknown-rebase-backend" {
			t.Fatalf("issue/kind = %v/%q, want a non-nil issue and unknown-rebase-backend when a non-forcing rebase is present", issue, kind)
		}

		slot2, issue2, kind2 := deriveBackendSlot(inv, false)
		if slot2.Status != "invalid" {
			t.Fatalf("Status = %q, want invalid even when un-gated", slot2.Status)
		}
		if issue2 != nil || kind2 != "" {
			t.Fatalf("issue/kind = %v/%q, want nil issue and empty kind when no non-forcing rebase entry consults this key (honestly reported, but no issue minted)", issue2, kind2)
		}
	})

	t.Run("ValidNames", func(t *testing.T) {
		for _, name := range []string{"merge", "apply"} {
			inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
				{Scope: "local", Key: "rebase.backend", ValuePresent: true, Value: name},
			}}
			slot, issue, kind := deriveBackendSlot(inv, true)
			if slot.Status != "valid" || slot.Value == nil || *slot.Value != name {
				t.Fatalf("name=%s: slot=%+v, want Status=valid Value=%s", name, slot, name)
			}
			if issue != nil || kind != "" {
				t.Fatalf("name=%s: unexpected issue=%v kind=%q", name, issue, kind)
			}
		}
	})
}

func TestRebasePlanProbe_NewConfigIssue(t *testing.T) {
	t.Run("IssueIDMatchesTheDocumentedLengthFramedTuple", func(t *testing.T) {
		repoRoot := "/sanitized/repo"
		entry := GitConfigEntry{Scope: "local", Key: "rebase.backend", ValuePresent: true, Value: "sideways"}
		issue := newConfigIssue("ctx-id", &repoRoot, "rebase.backend", "local", "rebase", "unknown-rebase-backend", entry)

		wantSum := lengthFramedSHA256("config-issue/v1", "ctx-id", "rebase.backend", "1", "sideways", "rebase")
		wantID := hex.EncodeToString(wantSum[:])
		if issue.IssueID != wantID {
			t.Fatalf("IssueID = %s, want %s", issue.IssueID, wantID)
		}
		if issue.ContextID != "ctx-id" || issue.RepoRoot != &repoRoot || issue.Key != "rebase.backend" ||
			issue.Source != "local" || issue.RouteCommand != "rebase" || issue.ErrorKind != "unknown-rebase-backend" {
			t.Fatalf("issue = %+v, unexpected non-hash fields", issue)
		}
	})

	t.Run("ExcludesRepoRootSourceAndErrorKindFromTheHash", func(t *testing.T) {
		entry := GitConfigEntry{Value: "sideways", ValuePresent: true}
		rootA, rootB := "/root/a", "/root/b"
		issueA := newConfigIssue("ctx", &rootA, "rebase.backend", "local", "rebase", "unknown-rebase-backend", entry)
		issueB := newConfigIssue("ctx", &rootB, "rebase.backend", "global", "rebase", "missing-backend-value", entry)
		if issueA.IssueID != issueB.IssueID {
			t.Fatalf("IssueID changed despite only repo_root/source/error_kind differing (route_command held fixed, since route_command IS part of the hash tuple): %s vs %s", issueA.IssueID, issueB.IssueID)
		}
	})

	t.Run("ChangingRouteCommandChangesTheHash", func(t *testing.T) {
		entry := GitConfigEntry{Value: "sideways", ValuePresent: true}
		issueRebase := newConfigIssue("ctx", nil, "rebase.backend", "local", "rebase", "unknown-rebase-backend", entry)
		issueFetch := newConfigIssue("ctx", nil, "rebase.backend", "local", "fetch", "unknown-rebase-backend", entry)
		if issueRebase.IssueID == issueFetch.IssueID {
			t.Fatal("route_command IS part of the hash tuple per the documented formula; expected different IssueIDs")
		}
	})

	t.Run("RawValueFieldsNilIffValueAbsent", func(t *testing.T) {
		valueless := GitConfigEntry{ValuePresent: false}
		issue := newConfigIssue("ctx", nil, "rebase.backend", "local", "rebase", "missing-backend-value", valueless)
		if issue.RawValuePresent {
			t.Fatal("RawValuePresent should be false for a valueless offending entry")
		}
		if issue.RawValueBase64 != nil || issue.RawValueSHA256 != nil || issue.SanitizedValue != nil {
			t.Fatalf("expected all three raw-value fields nil for a valueless entry, got %+v", issue)
		}

		valued := GitConfigEntry{ValuePresent: true, Value: "sideways"}
		issue2 := newConfigIssue("ctx", nil, "rebase.backend", "local", "rebase", "unknown-rebase-backend", valued)
		if !issue2.RawValuePresent {
			t.Fatal("RawValuePresent should be true for a valued offending entry")
		}
		wantB64 := base64.StdEncoding.EncodeToString([]byte("sideways"))
		if issue2.RawValueBase64 == nil || *issue2.RawValueBase64 != wantB64 {
			t.Fatalf("RawValueBase64 = %v, want %s", issue2.RawValueBase64, wantB64)
		}
		wantSHA := sha256.Sum256([]byte("sideways"))
		wantSHAHex := hex.EncodeToString(wantSHA[:])
		if issue2.RawValueSHA256 == nil || *issue2.RawValueSHA256 != wantSHAHex {
			t.Fatalf("RawValueSHA256 = %v, want %s", issue2.RawValueSHA256, wantSHAHex)
		}
		wantSanitized := ancestrySanitize("sideways", ancestryDetailLimit)
		if issue2.SanitizedValue == nil || *issue2.SanitizedValue != wantSanitized {
			t.Fatalf("SanitizedValue = %v, want %s", issue2.SanitizedValue, wantSanitized)
		}
	})
}

func TestRebasePlanProbe_ProbeRepositoryConfig(t *testing.T) {
	t.Run("OutOfScopeIsTotallyNotEvaluated", func(t *testing.T) {
		repo := rppRepo(t)
		result := ProbeRepositoryConfig(repo, GitCapabilities{CapConfigShowScope: true}, RepositoryConfigScope{Rebase: false}, "ctx", nil, "fetch")
		for name, slot := range map[string]PlanConfigSlot{"UpdateRefs": result.UpdateRefs, "Backend": result.Backend, "AutoStash": result.AutoStash} {
			if slot.Status != "not-evaluated" {
				t.Fatalf("%s.Status = %q, want not-evaluated", name, slot.Status)
			}
		}
		if result.RebaseMerges.Status != "not-evaluated" || result.RebaseMerges.Interpretation != "unknown" {
			t.Fatalf("RebaseMerges = %+v, want Status=not-evaluated Interpretation=unknown", result.RebaseMerges)
		}
		if result.Issues == nil || len(result.Issues) != 0 {
			t.Fatalf("Issues = %v, want a non-nil empty slice", result.Issues)
		}
	})

	t.Run("ProbeFailedWhenCapabilityGateIsOff", func(t *testing.T) {
		repo := rppRepo(t)
		result := ProbeRepositoryConfig(repo, GitCapabilities{CapConfigShowScope: false}, RepositoryConfigScope{Rebase: true}, "ctx", nil, "rebase")
		for name, slot := range map[string]PlanConfigSlot{"UpdateRefs": result.UpdateRefs, "Backend": result.Backend, "AutoStash": result.AutoStash} {
			if slot.Status != "probe-failed" {
				t.Fatalf("%s.Status = %q, want probe-failed", name, slot.Status)
			}
		}
		if result.RebaseMerges.Status != "probe-failed" || result.RebaseMerges.Interpretation != "unknown" {
			t.Fatalf("RebaseMerges = %+v, want Status=probe-failed Interpretation=unknown", result.RebaseMerges)
		}
	})

	t.Run("ReadsRealRepositoryConfigEndToEnd", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "--local", "rebase.updateRefs", "true")
		gitInTest(t, repo, "config", "--local", "rebase.backend", "merge")
		result := ProbeRepositoryConfig(repo, GitCapabilities{CapConfigShowScope: true}, RepositoryConfigScope{Rebase: true}, "ctx", nil, "rebase")
		if result.UpdateRefs.Status != "valid" || result.UpdateRefs.Value == nil || *result.UpdateRefs.Value != "true" {
			t.Fatalf("UpdateRefs = %+v", result.UpdateRefs)
		}
		if result.Backend.Status != "valid" || result.Backend.Value == nil || *result.Backend.Value != "merge" {
			t.Fatalf("Backend = %+v", result.Backend)
		}
		if result.AutoStash.Status != "absent" {
			t.Fatalf("AutoStash = %+v, want absent (never configured)", result.AutoStash)
		}
	})
}

func TestRebasePlanProbe_DeriveRepositoryConfig(t *testing.T) {
	t.Run("PlainBooleanKeysMintBadBooleanIssues", func(t *testing.T) {
		plainKeys := []struct{ lookupKey, jsonKey string }{
			{"rebase.stat", "rebase.stat"},
			{"rebase.autosquash", "rebase.autoSquash"},
			{"rebase.reschedulefailedexec", "rebase.rescheduleFailedExec"},
			{"rebase.forkpoint", "rebase.forkPoint"},
		}
		for _, pk := range plainKeys {
			t.Run(pk.jsonKey, func(t *testing.T) {
				inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
					{Scope: "local", Key: pk.lookupKey, ValuePresent: true, Value: "sideways"},
				}}
				result := DeriveRepositoryConfig(inv, "", RepositoryConfigScope{Rebase: true}, "ctx", nil, "rebase")
				var found bool
				for _, issue := range result.Issues {
					if issue.Key == pk.jsonKey {
						found = true
						if issue.ErrorKind != "bad-boolean" {
							t.Fatalf("%s: ErrorKind = %q, want bad-boolean", pk.jsonKey, issue.ErrorKind)
						}
					}
				}
				if !found {
					t.Fatalf("%s: expected a config_issues[] row for an unparseable occurrence, got %+v", pk.jsonKey, result.Issues)
				}
			})
		}
	})

	t.Run("MergeBackendGatedKeysOnlyCheckedWhenMergeBackendActive", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "local", Key: "rebase.abbreviatecommands", ValuePresent: true, Value: "sideways"},
			{Scope: "local", Key: "rebase.maxlabellength", ValuePresent: true, Value: "not-a-number"},
		}}
		inactive := DeriveRepositoryConfig(inv, "", RepositoryConfigScope{Rebase: true, MergeBackendActive: false}, "ctx", nil, "rebase")
		for _, issue := range inactive.Issues {
			if issue.Key == "rebase.abbreviateCommands" || issue.Key == "rebase.maxLabelLength" {
				t.Fatalf("expected merge-backend-gated keys to be skipped when MergeBackendActive is false, got %+v", issue)
			}
		}

		active := DeriveRepositoryConfig(inv, "", RepositoryConfigScope{Rebase: true, MergeBackendActive: true}, "ctx", nil, "rebase")
		var sawAbbrev, sawMaxLabel bool
		for _, issue := range active.Issues {
			if issue.Key == "rebase.abbreviateCommands" {
				sawAbbrev = true
				if issue.ErrorKind != "bad-boolean" {
					t.Fatalf("rebase.abbreviateCommands ErrorKind = %q, want bad-boolean", issue.ErrorKind)
				}
			}
			if issue.Key == "rebase.maxLabelLength" {
				sawMaxLabel = true
				if issue.ErrorKind != "bad-numeric" {
					t.Fatalf("rebase.maxLabelLength ErrorKind = %q, want bad-numeric", issue.ErrorKind)
				}
			}
		}
		if !sawAbbrev || !sawMaxLabel {
			t.Fatalf("expected both merge-backend-gated issues when MergeBackendActive is true, got %+v", active.Issues)
		}
	})

	t.Run("MaxLabelLengthValuelessOccurrenceIsAlsoBadNumeric", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "local", Key: "rebase.maxlabellength", ValuePresent: false},
		}}
		result := DeriveRepositoryConfig(inv, "", RepositoryConfigScope{Rebase: true, MergeBackendActive: true}, "ctx", nil, "rebase")
		var found bool
		for _, issue := range result.Issues {
			if issue.Key == "rebase.maxLabelLength" {
				found = true
				if issue.ErrorKind != "bad-numeric" {
					t.Fatalf("ErrorKind = %q, want bad-numeric for a valueless occurrence too", issue.ErrorKind)
				}
			}
		}
		if !found {
			t.Fatal("expected a bad-numeric issue for a valueless rebase.maxLabelLength occurrence")
		}
	})

	t.Run("NoIssuesWhenEverythingValid", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "local", Key: "rebase.stat", ValuePresent: true, Value: "true"},
		}}
		result := DeriveRepositoryConfig(inv, "", RepositoryConfigScope{Rebase: true}, "ctx", nil, "rebase")
		if len(result.Issues) != 0 {
			t.Fatalf("Issues = %+v, want none", result.Issues)
		}
	})
}

// ============================================================================
// Remote/refspec/legacy resolution (§11.4)
// ============================================================================

func TestRebasePlanProbe_ConfigSubsectionByLastDot(t *testing.T) {
	cases := []struct {
		name, key, prefix, wantSub string
		wantOK                     bool
	}{
		{"SimpleRemote", "remote.origin.url", "remote.", "origin", true},
		{"SubsectionContainingDots", "url.https://short/.insteadof", "url.", "https://short/", true},
		{"WrongPrefix", "branch.main.remote", "remote.", "", false},
		{"NoTrailingVariable", "remote.origin", "remote.", "", false}, // last <= 0: no variable after the subsection
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, ok := configSubsectionByLastDot(tc.key, tc.prefix)
			if ok != tc.wantOK || (ok && sub != tc.wantSub) {
				t.Fatalf("configSubsectionByLastDot(%q, %q) = (%q, %v), want (%q, %v)", tc.key, tc.prefix, sub, ok, tc.wantSub, tc.wantOK)
			}
		})
	}
}

func TestRebasePlanProbe_RemoteConfigNames(t *testing.T) {
	inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
		{Key: "remote.zeta.url", ValuePresent: true, Value: "u1"},
		{Key: "remote.alpha.url", ValuePresent: true, Value: "u2"},
		{Key: "remote.alpha.fetch", ValuePresent: true, Value: "f1"},
		{Key: "branch.main.remote", ValuePresent: true, Value: "alpha"},
	}}
	names := remoteConfigNames(inv)
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Fatalf("names = %v, want sorted [alpha zeta] (branch.main.remote must not be mistaken for a remote subsection)", names)
	}
}

func TestRebasePlanProbe_ApplyInsteadOf(t *testing.T) {
	t.Run("LongestMatchingValueWins", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "url.git@github.com:.insteadof", ValuePresent: true, Value: "https://"},
			{Key: "url.git@github.com:.insteadof", ValuePresent: true, Value: "https://github.com/"},
		}}
		rewritten, ok := applyInsteadOf(inv, "https://github.com/org/repo.git")
		if !ok {
			t.Fatal("expected a rewrite")
		}
		if rewritten != "git@github.com:org/repo.git" {
			t.Fatalf("rewritten = %q, want git@github.com:org/repo.git (longest insteadOf value must win)", rewritten)
		}
	})

	t.Run("SubsectionCasePreservedInRewriteBase", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "url.MyBase/.insteadof", ValuePresent: true, Value: "short://"},
		}}
		rewritten, ok := applyInsteadOf(inv, "short://x")
		if !ok || rewritten != "MyBase/x" {
			t.Fatalf("rewritten/ok = %q/%v, want MyBase/x/true (subsection case preserved)", rewritten, ok)
		}
	})

	t.Run("NoMatchLeavesURLUnchanged", func(t *testing.T) {
		inv := GitConfigInventory{Available: true}
		rewritten, ok := applyInsteadOf(inv, "https://example.invalid/repo.git")
		if ok || rewritten != "https://example.invalid/repo.git" {
			t.Fatalf("rewritten/ok = %q/%v, want unchanged/false", rewritten, ok)
		}
	})
}

func TestRebasePlanProbe_EffectivePruneFlag(t *testing.T) {
	t.Run("ValidPerRemoteWins", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "remote.origin.prune", ValuePresent: true, Value: "true"},
			{Key: "fetch.prune", ValuePresent: true, Value: "false"},
		}}
		if !effectivePruneFlag(inv, "remote.origin.prune", "fetch.prune") {
			t.Fatal("expected the valid per-remote value (true) to win over the global (false)")
		}
	})

	t.Run("InvalidPerRemoteFallsBackToGlobal", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "remote.origin.prune", ValuePresent: true, Value: "sideways"},
			{Key: "fetch.prune", ValuePresent: true, Value: "true"},
		}}
		if !effectivePruneFlag(inv, "remote.origin.prune", "fetch.prune") {
			t.Fatal("expected fall-back to the global value when the per-remote value is unparseable")
		}
	})

	t.Run("BothAbsentIsFalse", func(t *testing.T) {
		inv := GitConfigInventory{Available: true}
		if effectivePruneFlag(inv, "remote.origin.prune", "fetch.prune") {
			t.Fatal("expected false when neither key is configured")
		}
	})
}

func TestRebasePlanProbe_EffectiveTagOpt(t *testing.T) {
	cases := []struct {
		name, value string
		present     bool
		want        string
	}{
		{"NoTags", "--no-tags", true, "no-tags"},
		{"AllTags", "--tags", true, "tags"},
		{"UnrecognizedLiteral", "--something-else", true, "auto-follow"},
		{"Absent", "", false, "auto-follow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var entries []GitConfigEntry
			if tc.present {
				entries = []GitConfigEntry{{Key: "remote.origin.tagopt", ValuePresent: true, Value: tc.value}}
			}
			inv := GitConfigInventory{Available: true, Entries: entries}
			if got := effectiveTagOpt(inv, "origin"); got != tc.want {
				t.Fatalf("effectiveTagOpt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRebasePlanProbe_DecomposeRefspec(t *testing.T) {
	cases := []struct {
		name, raw           string
		wantNeg, wantForced bool
		wantSrc, wantDst    string
	}{
		{"PlainWithColon", "refs/heads/*:refs/remotes/origin/*", false, false, "refs/heads/*", "refs/remotes/origin/*"},
		{"Forced", "+refs/heads/*:refs/remotes/origin/*", false, true, "refs/heads/*", "refs/remotes/origin/*"},
		{"Negative", "^refs/heads/wip/*", true, false, "refs/heads/wip/*", ""},
		{"NoColonShorthand", "refs/heads/topic", false, false, "refs/heads/topic", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := decomposeRefspec(tc.raw)
			if r.Raw != tc.raw || r.Negative != tc.wantNeg || r.Forced != tc.wantForced || r.Src != tc.wantSrc || r.Dst != tc.wantDst {
				t.Fatalf("decomposeRefspec(%q) = %+v, want Negative=%v Forced=%v Src=%q Dst=%q", tc.raw, r, tc.wantNeg, tc.wantForced, tc.wantSrc, tc.wantDst)
			}
		})
	}
}

func TestRebasePlanProbe_RefspecDestinationCovers(t *testing.T) {
	cases := []struct {
		name, dst, ns string
		want          bool
	}{
		{"WildcardCoversNarrowerNamespace", "refs/*", "refs/tags/", true},
		{"NarrowerWildcardCoversWiderNamespaceQuery", "refs/tags/v*", "refs/tags/", true},
		{"NoOverlap", "refs/heads/*", "refs/tags/", false},
		{"EmptyPrefixAlwaysCovers", "*", "refs/tags/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refspecDestinationCovers(tc.dst, tc.ns); got != tc.want {
				t.Fatalf("refspecDestinationCovers(%q, %q) = %v, want %v", tc.dst, tc.ns, got, tc.want)
			}
		})
	}
}

func TestRebasePlanProbe_RefMatchesPattern(t *testing.T) {
	cases := []struct {
		name, dst, ref string
		want           bool
	}{
		{"WildcardPrefixMatch", "refs/heads/*", "refs/heads/main", true},
		{"WildcardNoMatch", "refs/heads/*", "refs/tags/v1", false},
		{"ExactMatch", "refs/heads/main", "refs/heads/main", true},
		{"ExactNoPartialMatch", "refs/heads/main", "refs/heads/main2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refMatchesPattern(tc.dst, tc.ref); got != tc.want {
				t.Fatalf("refMatchesPattern(%q, %q) = %v, want %v", tc.dst, tc.ref, got, tc.want)
			}
		})
	}
}

func TestRebasePlanProbe_ReadLegacyRemoteFile(t *testing.T) {
	t.Run("FirstURLLineButEveryPullLine", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "old")
		content := "URL: https://example.invalid/first.git\n" +
			"Pull: refs/heads/a:refs/remotes/old/a\n" +
			"Pull: refs/heads/b:refs/remotes/old/b\n" +
			"URL: https://example.invalid/second.git\n" // a second URL line must be ignored
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		url, refspecs, ok := readLegacyRemoteFile(path)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if url != "https://example.invalid/first.git" {
			t.Fatalf("url = %q, want the FIRST URL: line, ignoring the second", url)
		}
		if len(refspecs) != 2 || refspecs[0] != "refs/heads/a:refs/remotes/old/a" || refspecs[1] != "refs/heads/b:refs/remotes/old/b" {
			t.Fatalf("refspecs = %v, want both Pull: lines in order", refspecs)
		}
	})

	t.Run("MissingFileIsNotOK", func(t *testing.T) {
		_, _, ok := readLegacyRemoteFile(filepath.Join(t.TempDir(), "absent"))
		if ok {
			t.Fatal("expected ok=false for a missing legacy remote file")
		}
	})
}

func TestRebasePlanProbe_ReadLegacyBranchFile(t *testing.T) {
	t.Run("DefaultsToHEADWhenNoHash", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "old")
		if err := os.WriteFile(path, []byte("https://example.invalid/repo.git\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		url, refspec, ok := readLegacyBranchFile(path, "old")
		if !ok || url != "https://example.invalid/repo.git" || refspec != "HEAD:refs/heads/old" {
			t.Fatalf("got url=%q refspec=%q ok=%v, want url unchanged, refspec HEAD:refs/heads/old, ok=true", url, refspec, ok)
		}
	})

	t.Run("HashSelectsARemoteBranch", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "old")
		if err := os.WriteFile(path, []byte("https://example.invalid/repo.git#topic\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		url, refspec, ok := readLegacyBranchFile(path, "old")
		if !ok || url != "https://example.invalid/repo.git" || refspec != "topic:refs/heads/old" {
			t.Fatalf("got url=%q refspec=%q ok=%v, want refspec topic:refs/heads/old", url, refspec, ok)
		}
	})
}

func TestRebasePlanProbe_ResolveHeadBranch(t *testing.T) {
	t.Run("OrdinaryBranch", func(t *testing.T) {
		repo := rppRepo(t)
		branch := ResolveHeadBranch(repo)
		if branch == nil || *branch != "main" {
			t.Fatalf("branch = %v, want main", branch)
		}
	})

	t.Run("DetachedHEADIsNil", func(t *testing.T) {
		repo := rppRepo(t)
		sha := rppHeadSHA(t, repo)
		gitInTest(t, repo, "checkout", "-q", sha)
		if branch := ResolveHeadBranch(repo); branch != nil {
			t.Fatalf("branch = %v, want nil for a detached HEAD", *branch)
		}
	})
}

func TestRebasePlanProbe_ResolveRemoteName(t *testing.T) {
	main := "main"

	t.Run("BranchRemoteKeyWins", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "branch.main.remote", ValuePresent: true, Value: "upstream"},
			{Key: "remote.origin.url", ValuePresent: true, Value: "u"},
		}}
		if got := ResolveRemoteName(inv, &main, GitCapabilities{CapSoleRemoteFallback: true}); got != "upstream" {
			t.Fatalf("ResolveRemoteName = %q, want upstream", got)
		}
	})

	t.Run("SoleRemoteFallbackGatedByCapability", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "remote.solo.url", ValuePresent: true, Value: "u"},
		}}
		if got := ResolveRemoteName(inv, nil, GitCapabilities{CapSoleRemoteFallback: true}); got != "solo" {
			t.Fatalf("with the capability on: ResolveRemoteName = %q, want solo", got)
		}
		if got := ResolveRemoteName(inv, nil, GitCapabilities{CapSoleRemoteFallback: false}); got != "origin" {
			t.Fatalf("with the capability off: ResolveRemoteName = %q, want the literal fallback origin", got)
		}
	})

	t.Run("MultipleRemotesNeverFallBackEvenWithCapability", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "remote.a.url", ValuePresent: true, Value: "u"},
			{Key: "remote.b.url", ValuePresent: true, Value: "u"},
		}}
		if got := ResolveRemoteName(inv, nil, GitCapabilities{CapSoleRemoteFallback: true}); got != "origin" {
			t.Fatalf("ResolveRemoteName = %q, want origin when more than one remote exists", got)
		}
	})

	t.Run("DefaultsToOrigin", func(t *testing.T) {
		inv := GitConfigInventory{Available: true}
		if got := ResolveRemoteName(inv, nil, GitCapabilities{}); got != "origin" {
			t.Fatalf("ResolveRemoteName = %q, want origin", got)
		}
	})
}

func TestRebasePlanProbe_ResolveFetchRemote(t *testing.T) {
	t.Run("ConfigDefinedWins", func(t *testing.T) {
		commonDir := t.TempDir()
		rppWriteLegacyRemoteFileAt(t, commonDir, "origin", "https://legacy.invalid/repo.git", nil)
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "remote.origin.url", ValuePresent: true, Value: "https://config.invalid/repo.git"},
			{Key: "remote.origin.fetch", ValuePresent: true, Value: "+refs/heads/*:refs/remotes/origin/*"},
		}}
		remote, found := ResolveFetchRemote(inv, commonDir, "origin", GitCapabilities{})
		if !found {
			t.Fatal("expected found=true")
		}
		if remote.Source != "config" || remote.URLConfigured != "https://config.invalid/repo.git" {
			t.Fatalf("remote = %+v, want the config-defined remote, not the legacy file", remote)
		}
	})

	t.Run("LegacyRemotesFileWinsOverLegacyBranchesFile", func(t *testing.T) {
		commonDir := t.TempDir()
		rppWriteLegacyRemoteFileAt(t, commonDir, "old", "https://legacy-remotes.invalid/repo.git", []string{"refs/heads/*:refs/remotes/old/*"})
		if err := os.MkdirAll(filepath.Join(commonDir, "branches"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(commonDir, "branches", "old"), []byte("https://legacy-branches.invalid/repo.git\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		inv := GitConfigInventory{Available: true}
		remote, found := ResolveFetchRemote(inv, commonDir, "old", GitCapabilities{})
		if !found || remote.Source != "legacy-remotes" {
			t.Fatalf("remote = %+v found=%v, want Source=legacy-remotes", remote, found)
		}
	})

	t.Run("LegacyBranchesFileIsTheLastRung", func(t *testing.T) {
		commonDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(commonDir, "branches"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(commonDir, "branches", "old"), []byte("https://legacy-branches.invalid/repo.git\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		inv := GitConfigInventory{Available: true}
		remote, found := ResolveFetchRemote(inv, commonDir, "old", GitCapabilities{})
		if !found || remote.Source != "legacy-branches" {
			t.Fatalf("remote = %+v found=%v, want Source=legacy-branches", remote, found)
		}
	})

	t.Run("NotFoundWhenNoRungAnswers", func(t *testing.T) {
		commonDir := t.TempDir()
		inv := GitConfigInventory{Available: true}
		_, found := ResolveFetchRemote(inv, commonDir, "ghost", GitCapabilities{})
		if found {
			t.Fatal("expected found=false")
		}
	})

	t.Run("PruneAndTagOptResolvedUniformlyRegardlessOfSource", func(t *testing.T) {
		commonDir := t.TempDir()
		rppWriteLegacyRemoteFileAt(t, commonDir, "old", "https://legacy.invalid/repo.git", nil)
		// Deliberately GLOBAL keys, not remote.old.* — any remote.old.* entry
		// (even just prune/tagOpt) would redirect ResolveFetchRemote to the
		// config-defined branch instead of exercising the legacy-remotes rung.
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "fetch.prune", ValuePresent: true, Value: "true"},
		}}
		remote, found := ResolveFetchRemote(inv, commonDir, "old", GitCapabilities{})
		if !found || remote.Source != "legacy-remotes" {
			t.Fatalf("remote = %+v found=%v, want the legacy-remotes rung", remote, found)
		}
		if !remote.Prune {
			t.Fatal("expected Prune=true via the GLOBAL fetch.prune fallback even though the remote itself was resolved from a legacy file, not config")
		}
		if remote.TagOpt != "auto-follow" {
			t.Fatalf("TagOpt = %q, want auto-follow (tagOpt has no global fallback key at all)", remote.TagOpt)
		}
	})
}

// ============================================================================
// Local-branch destinations (§11.6)
// ============================================================================

func TestRebasePlanProbe_LocalBranchDestinations(t *testing.T) {
	t.Run("ZeroRemotesIsTheZeroValue", func(t *testing.T) {
		repo := rppRepo(t)
		got := resolveLocalBranchDestinations(nil, repo, BranchHolderInventory{})
		want := PlanLocalBranchDestinations{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want the zero value %+v", got, want)
		}
	})

	t.Run("DefaultMirrorRefspecDoesNotCoverHeadsSoPatternsAreEmpty", func(t *testing.T) {
		repo := rppRepo(t)
		remotes := []PlanFetchRemote{{Name: "origin", Refspecs: []PlanRefspec{
			decomposeRefspec("+refs/heads/*:refs/remotes/origin/*"),
		}}}
		got := resolveLocalBranchDestinations(remotes, repo, BranchHolderInventory{})
		if got.Patterns == nil || len(got.Patterns) != 0 {
			t.Fatalf("Patterns = %v, want a non-nil empty slice (refs/remotes/* does not cover refs/heads/)", got.Patterns)
		}
		if got.Branches == nil || len(got.Branches) != 0 {
			t.Fatalf("Branches = %v, want a non-nil empty slice", got.Branches)
		}
	})

	t.Run("MirrorLikeRefspecCoversHeadsAndMatchesExistingBranches", func(t *testing.T) {
		repo := rppRepo(t)
		rppBulkCreateBranches(t, repo, []string{"alpha", "beta", "unrelated-topic"})
		remotes := []PlanFetchRemote{{Name: "origin", Refspecs: []PlanRefspec{
			decomposeRefspec("+refs/heads/*:refs/heads/*"), // an explicit mirror-style refspec
		}}}
		got := resolveLocalBranchDestinations(remotes, repo, BranchHolderInventory{})
		if len(got.Patterns) != 1 || got.Patterns[0].Remote != "origin" || got.Patterns[0].Dst != "refs/heads/*" || !got.Patterns[0].Forced {
			t.Fatalf("Patterns = %+v, want one forced refs/heads/* pattern for origin", got.Patterns)
		}
		wantBranches := []string{"alpha", "beta", "main", "unrelated-topic"}
		if !reflect.DeepEqual(got.Branches, wantBranches) {
			t.Fatalf("Branches = %v, want sorted %v", got.Branches, wantBranches)
		}
		if got.Truncated {
			t.Fatal("did not expect truncation with only 4 branches")
		}
	})

	t.Run("NegativeRefspecsExcludedFromPatterns", func(t *testing.T) {
		repo := rppRepo(t)
		remotes := []PlanFetchRemote{{Name: "origin", Refspecs: []PlanRefspec{
			decomposeRefspec("+refs/heads/*:refs/heads/*"),
			decomposeRefspec("^refs/heads/wip/*"),
		}}}
		got := resolveLocalBranchDestinations(remotes, repo, BranchHolderInventory{})
		if len(got.Patterns) != 1 {
			t.Fatalf("Patterns = %+v, want the negative refspec excluded, leaving exactly one", got.Patterns)
		}
	})

	t.Run("HeldComputedFromFullListBeforeThe200Cap", func(t *testing.T) {
		repo := rppRepo(t)
		names := make([]string, 0, 205)
		for i := 0; i < 205; i++ {
			names = append(names, fmt.Sprintf("br-%03d", i))
		}
		rppBulkCreateBranches(t, repo, names)
		remotes := []PlanFetchRemote{{Name: "origin", Refspecs: []PlanRefspec{decomposeRefspec("+refs/heads/*:refs/heads/*")}}}
		// Hold a branch that sorts AFTER the 200-cap boundary ("main" sorts
		// after "br-*" lexicographically) so it would be silently dropped
		// from Branches, but must still surface in Held.
		holders := BranchHolderInventory{Available: true, ByBranch: map[string]BranchHolderRecord{
			"main": {Worktree: repo, Mechanism: HoldCheckedOut},
		}}
		got := resolveLocalBranchDestinations(remotes, repo, holders)
		if !got.Truncated || len(got.Branches) != 200 {
			t.Fatalf("Branches truncated=%v len=%d, want truncated at 200", got.Truncated, len(got.Branches))
		}
		if len(got.Held) != 1 || got.Held[0].Branch != "main" || got.Held[0].Hold != string(HoldCheckedOut) {
			t.Fatalf("Held = %+v, want the checked-out main branch even though it is past the display cap", got.Held)
		}
	})

	t.Run("HeldNilSpecificallyWhenHoldersUnavailable", func(t *testing.T) {
		repo := rppRepo(t)
		remotes := []PlanFetchRemote{{Name: "origin", Refspecs: []PlanRefspec{decomposeRefspec("+refs/heads/*:refs/heads/*")}}}
		got := resolveLocalBranchDestinations(remotes, repo, BranchHolderInventory{Available: false})
		if got.Held != nil {
			t.Fatalf("Held = %v, want nil when holders.Available is false", got.Held)
		}
	})

	t.Run("HeldEmptyNotNilWhenHoldersAvailableButNothingHeld", func(t *testing.T) {
		repo := rppRepo(t)
		remotes := []PlanFetchRemote{{Name: "origin", Refspecs: []PlanRefspec{decomposeRefspec("+refs/heads/*:refs/heads/*")}}}
		got := resolveLocalBranchDestinations(remotes, repo, BranchHolderInventory{Available: true, ByBranch: map[string]BranchHolderRecord{}})
		if got.Held == nil || len(got.Held) != 0 {
			t.Fatalf("Held = %v, want a non-nil empty slice", got.Held)
		}
	})
}

// ============================================================================
// Submodule recursion (§11.5)
// ============================================================================

func TestRebasePlanProbe_ParseFetchRecurseSubmodulesValue(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		present   bool
		wantOK    bool
		wantValue string
	}{
		{"OnDemandExact", "on-demand", true, true, "on-demand"},
		{"OnDemandMixedCaseAndSpace", " On-Demand ", true, true, "on-demand"},
		{"BoolTrueBecomesYes", "true", true, true, "yes"},
		{"BoolFalseBecomesNo", "false", true, true, "no"},
		{"Valueless", "", false, true, "yes"},
		{"Unparseable", "sideways", true, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, ok := parseFetchRecurseSubmodulesValue(tc.value, tc.present)
			if ok != tc.wantOK || (ok && mode != tc.wantValue) {
				t.Fatalf("parseFetchRecurseSubmodulesValue(%q, %v) = (%q, %v), want (%q, %v)", tc.value, tc.present, mode, ok, tc.wantValue, tc.wantOK)
			}
		})
	}
}

func TestRebasePlanProbe_LastRecurseSubmodulesOccurrence(t *testing.T) {
	t.Run("TrueInterleavingAcrossBothKeys", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "system", Key: "fetch.recursesubmodules", ValuePresent: true, Value: "on-demand"},
			{Scope: "global", Key: "submodule.recurse", ValuePresent: true, Value: "true"},
			{Scope: "local", Key: "fetch.recursesubmodules", ValuePresent: true, Value: "false"},
		}}
		entry, source, found := lastRecurseSubmodulesOccurrence(inv)
		if !found || source != "fetch.recurseSubmodules" || entry.Value != "false" {
			t.Fatalf("got entry=%+v source=%q found=%v, want the LOCAL fetch.recurseSubmodules occurrence (the true last-in-file-order across both keys)", entry, source, found)
		}
	})

	t.Run("SubmoduleRecurseCanWinWhenItComesLast", func(t *testing.T) {
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Scope: "system", Key: "fetch.recursesubmodules", ValuePresent: true, Value: "on-demand"},
			{Scope: "local", Key: "submodule.recurse", ValuePresent: true, Value: "true"},
		}}
		_, source, found := lastRecurseSubmodulesOccurrence(inv)
		if !found || source != "submodule.recurse" {
			t.Fatalf("source = %q found=%v, want submodule.recurse to win when it is the true last occurrence", source, found)
		}
	})

	t.Run("NotFoundWhenNeitherKeyPresent", func(t *testing.T) {
		inv := GitConfigInventory{Available: true}
		if _, _, found := lastRecurseSubmodulesOccurrence(inv); found {
			t.Fatal("expected found=false")
		}
	})
}

func TestRebasePlanProbe_ResolveConfigRecurseSubmodulesMode(t *testing.T) {
	t.Run("FetchKeyAcceptsOnDemand", func(t *testing.T) {
		mode, ok := resolveConfigRecurseSubmodulesMode(GitConfigEntry{ValuePresent: true, Value: "on-demand"}, "fetch.recurseSubmodules")
		if !ok || mode != "on-demand" {
			t.Fatalf("got (%q, %v), want (on-demand, true)", mode, ok)
		}
	})

	t.Run("SubmoduleRecurseKeyRejectsOnDemand", func(t *testing.T) {
		_, ok := resolveConfigRecurseSubmodulesMode(GitConfigEntry{ValuePresent: true, Value: "on-demand"}, "submodule.recurse")
		if ok {
			t.Fatal("expected submodule.recurse (a plain boolean key) to reject the on-demand literal")
		}
	})

	t.Run("SubmoduleRecurseKeyAcceptsPlainBoolean", func(t *testing.T) {
		mode, ok := resolveConfigRecurseSubmodulesMode(GitConfigEntry{ValuePresent: true, Value: "true"}, "submodule.recurse")
		if !ok || mode != "yes" {
			t.Fatalf("got (%q, %v), want (yes, true)", mode, ok)
		}
	})
}

func TestRebasePlanProbe_GitmodulesContextRecurseSubmodules(t *testing.T) {
	t.Run("WorkingTreeFileRung", func(t *testing.T) {
		repo := rppRepo(t)
		if err := os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte("[fetch]\n\trecurseSubmodules = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mode, ok := gitmodulesContextRecurseSubmodules(repo)
		if !ok || mode != "yes" {
			t.Fatalf("got (%q, %v), want (yes, true) via the working-tree .gitmodules file", mode, ok)
		}
	})

	t.Run("IndexBlobRungSurvivesWorkingTreeDeletion", func(t *testing.T) {
		repo := rppRepo(t)
		gitmodulesPath := filepath.Join(repo, ".gitmodules")
		if err := os.WriteFile(gitmodulesPath, []byte("[fetch]\n\trecurseSubmodules = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, repo, "add", ".gitmodules")
		if err := os.Remove(gitmodulesPath); err != nil {
			t.Fatal(err)
		}
		mode, ok := gitmodulesContextRecurseSubmodules(repo)
		if !ok || mode != "yes" {
			t.Fatalf("got (%q, %v), want (yes, true) via the staged INDEX blob, even with the working-tree file gone", mode, ok)
		}
	})

	t.Run("NotFoundWhenNoGitmodulesAnywhere", func(t *testing.T) {
		repo := rppRepo(t)
		if _, ok := gitmodulesContextRecurseSubmodules(repo); ok {
			t.Fatal("expected ok=false when .gitmodules never existed in any rung")
		}
	})
}

func TestRebasePlanProbe_ResolveSubmoduleMode(t *testing.T) {
	t.Run("ValidConfigOccurrenceWinsOutright", func(t *testing.T) {
		repo := rppRepo(t)
		// Even with a contradicting .gitmodules ladder answer present, the
		// valid config occurrence must win without ever consulting it.
		if err := os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte("[fetch]\n\trecurseSubmodules = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "fetch.recursesubmodules", ValuePresent: true, Value: "on-demand"},
		}}
		mode, source := resolveSubmoduleMode(inv, repo)
		if mode != "on-demand" || source != "fetch.recurseSubmodules" {
			t.Fatalf("got mode=%q source=%q, want on-demand/fetch.recurseSubmodules", mode, source)
		}
	})

	t.Run("InvalidConfigOccurrenceFallsThroughToGitmodulesLadder", func(t *testing.T) {
		repo := rppRepo(t)
		if err := os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte("[fetch]\n\trecurseSubmodules = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "fetch.recursesubmodules", ValuePresent: true, Value: "sideways"},
		}}
		mode, source := resolveSubmoduleMode(inv, repo)
		if mode != "yes" || source != "gitmodules-fetch.recurseSubmodules" {
			t.Fatalf("got mode=%q source=%q, want yes/gitmodules-fetch.recurseSubmodules (invalid config must fall through)", mode, source)
		}
	})

	t.Run("DefaultsToOnDemandWhenNothingAnswers", func(t *testing.T) {
		repo := rppRepo(t)
		inv := GitConfigInventory{Available: true}
		mode, source := resolveSubmoduleMode(inv, repo)
		if mode != "on-demand" || source != "default-on-demand" {
			t.Fatalf("got mode=%q source=%q, want on-demand/default-on-demand", mode, source)
		}
	})
}

func TestRebasePlanProbe_ListPresentGitlinks(t *testing.T) {
	repo := rppRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "regular.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(repo, "exec.sh")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitInTest(t, repo, "add", "regular.txt", "exec.sh")
	rppAddGitlink(t, repo, rppFakeGitlinkSHA, "sub-only")

	paths, err := listPresentGitlinks(repo)
	if err != nil {
		t.Fatalf("listPresentGitlinks: %v", err)
	}
	if len(paths) != 1 || paths[0] != "sub-only" {
		t.Fatalf("paths = %v, want exactly [sub-only] (regular/executable files must be excluded)", paths)
	}
}

// TestRebasePlanProbe_ListPresentGitlinksPreservesSpecialPaths drives the
// raw-byte, NUL-record contract of listPresentGitlinks. `git ls-files
// --stage` WITHOUT `-z` applies core.quotePath and rewrites every non-ASCII
// or special byte into a C-quoted, backslash-escaped rendering: a submodule
// at `libs/ünïcode` would come back as `"libs/\303\274n\303\257code"`,
// which names no directory on disk. This test therefore uses paths that are
// mangled by the quoting path and left verbatim by the NUL path, and asserts
// the verbatim spelling survives — including a path containing a TAB, which
// the record separator (`-z`) makes unambiguous but a line-oriented parser
// cannot.
func TestRebasePlanProbe_ListPresentGitlinksPreservesSpecialPaths(t *testing.T) {
	repo := rppRepo(t)
	// core.quotePath defaults to true; set it explicitly so the fixture is
	// not at the mercy of an inherited configuration.
	gitInTest(t, repo, "config", "core.quotePath", "true")

	want := []string{
		"libs/ünïcode",
		"libs/日本語",
		"plain/ascii",
		"weird\tname",
	}
	for _, p := range want {
		rppAddGitlink(t, repo, rppFakeGitlinkSHA, p)
	}

	got, err := listPresentGitlinks(repo)
	if err != nil {
		t.Fatalf("listPresentGitlinks: %v", err)
	}
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if !reflect.DeepEqual(got, sortedWant) {
		t.Fatalf("listPresentGitlinks = %q, want %q — a quoted/mangled spelling here means the probe is not reading NUL records through raw-byte plumbing", got, sortedWant)
	}
	for _, p := range got {
		if strings.Contains(p, "\\") {
			t.Fatalf("path %q carries a backslash escape: core.quotePath leaked into the inventory", p)
		}
	}
}

// TestRebasePlanProbe_SubmoduleReachWithNonASCIIPopulatedPath drives the
// fail-closed reach walk over a POPULATED submodule whose path is non-ASCII:
// the walk must find it, recurse into its real working tree, and report a
// bounded reach — which is only possible if the path it joins is the
// verbatim on-disk spelling.
func TestRebasePlanProbe_SubmoduleReachWithNonASCIIPopulatedPath(t *testing.T) {
	repo := rppRepo(t)
	gitInTest(t, repo, "config", "core.quotePath", "true")

	const subPath = "libs/ünïcode"
	rppAddGitlink(t, repo, rppFakeGitlinkSHA, subPath)
	rppInitNestedRepo(t, filepath.Join(repo, subPath))

	submodules, reach := walkSubmoduleReach(repo)
	if reach != "bounded" {
		t.Fatalf("reach = %q, want bounded: the walk must resolve a populated non-ASCII submodule rather than failing closed on a mangled path", reach)
	}
	if len(submodules) != 1 || submodules[0].Path != subPath {
		t.Fatalf("submodules = %+v, want exactly one entry at %q", submodules, subPath)
	}
}

// TestRebasePlanProbe_SubmoduleReachFailsClosedOnUnreadableNonASCIIPath
// asserts the fail-closed half: a present, non-ASCII gitlink whose own
// artefact cannot be read (a symlinked .git, which submodulePopulated
// refuses to follow) stops the walk and reports reach: unknown — never a
// silent "none".
func TestRebasePlanProbe_SubmoduleReachFailsClosedOnUnreadableNonASCIIPath(t *testing.T) {
	repo := rppRepo(t)
	gitInTest(t, repo, "config", "core.quotePath", "true")

	const subPath = "libs/ünïcode"
	rppAddGitlink(t, repo, rppFakeGitlinkSHA, subPath)
	subRoot := filepath.Join(repo, subPath)
	if err := os.MkdirAll(subRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(subRoot, ".git")); err != nil {
		t.Skipf("symlinks are unavailable on this filesystem: %v", err)
	}

	_, reach := walkSubmoduleReach(repo)
	if reach != "unknown" {
		t.Fatalf("reach = %q, want unknown: an unreadable present gitlink must fail closed", reach)
	}
}

func TestRebasePlanProbe_SubmodulePopulated(t *testing.T) {
	t.Run("Absent", func(t *testing.T) {
		sub := filepath.Join(t.TempDir(), "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		populated, err := submodulePopulated(sub)
		if err != nil || populated {
			t.Fatalf("got (%v, %v), want (false, nil)", populated, err)
		}
	})

	t.Run("PlainDirectory", func(t *testing.T) {
		sub := t.TempDir()
		if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		populated, err := submodulePopulated(sub)
		if err != nil || !populated {
			t.Fatalf("got (%v, %v), want (true, nil)", populated, err)
		}
	})

	t.Run("PointerFile", func(t *testing.T) {
		sub := t.TempDir()
		if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		populated, err := submodulePopulated(sub)
		if err != nil || !populated {
			t.Fatalf("got (%v, %v), want (true, nil)", populated, err)
		}
	})

	t.Run("SymlinkRefused", func(t *testing.T) {
		sub := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(sub, ".git")); err != nil {
			t.Fatal(err)
		}
		populated, err := submodulePopulated(sub)
		if err == nil || populated {
			t.Fatalf("got (%v, %v), want (false, non-nil) — a symlinked .git must be refused, not followed", populated, err)
		}
	})
}

func TestRebasePlanProbe_WalkSubmoduleReach(t *testing.T) {
	t.Run("NoneWhenNoGitlinksAtAll", func(t *testing.T) {
		repo := rppRepo(t)
		submodules, reach := walkSubmoduleReach(repo)
		if reach != "none" {
			t.Fatalf("reach = %q, want none", reach)
		}
		if submodules == nil || len(submodules) != 0 {
			t.Fatalf("submodules = %v, want a non-nil empty slice", submodules)
		}
	})

	t.Run("BoundedWithUnpopulatedSiblings", func(t *testing.T) {
		repo := rppRepo(t)
		rppAddGitlink(t, repo, rppFakeGitlinkSHA, "sub-b")
		rppAddGitlink(t, repo, rppFakeGitlinkSHA, "sub-a")
		submodules, reach := walkSubmoduleReach(repo)
		if reach != "bounded" {
			t.Fatalf("reach = %q, want bounded", reach)
		}
		if len(submodules) != 2 || submodules[0].Path != "sub-a" || submodules[1].Path != "sub-b" {
			t.Fatalf("submodules = %+v, want sorted [sub-a sub-b]", submodules)
		}
	})

	t.Run("DepthCapUnknownButSameEntriesAsTheBoundedCase", func(t *testing.T) {
		buildChain := func(t *testing.T) (repoRoot string) {
			t.Helper()
			repoRoot = rppRepo(t)
			l1 := filepath.Join(repoRoot, "l1")
			rppInitNestedRepo(t, l1)
			rppAddGitlink(t, repoRoot, rppFakeGitlinkSHA, "l1")
			l2 := filepath.Join(l1, "l2")
			rppInitNestedRepo(t, l2)
			rppAddGitlink(t, l1, rppFakeGitlinkSHA, "l2")
			rppAddGitlink(t, l2, rppFakeGitlinkSHA, "l3") // l3 itself is left unpopulated in the caller
			return repoRoot
		}

		t.Run("L3UnpopulatedCompletesBounded", func(t *testing.T) {
			repoRoot := buildChain(t)
			submodules, reach := walkSubmoduleReach(repoRoot)
			if reach != "bounded" {
				t.Fatalf("reach = %q, want bounded when the depth-3 gitlink is unpopulated", reach)
			}
			want := []string{"l1", "l1/l2", "l1/l2/l3"}
			if len(submodules) != len(want) {
				t.Fatalf("submodules = %+v, want %v", submodules, want)
			}
			for i, w := range want {
				if submodules[i].Path != w {
					t.Fatalf("submodules[%d] = %q, want %q", i, submodules[i].Path, w)
				}
			}
		})

		t.Run("L3PopulatedTriggersUnknownWithTheSameEntries", func(t *testing.T) {
			repoRoot := buildChain(t)
			rppMarkPopulated(t, filepath.Join(repoRoot, "l1", "l2", "l3"))
			submodules, reach := walkSubmoduleReach(repoRoot)
			if reach != "unknown" {
				t.Fatalf("reach = %q, want unknown: a populated gitlink discovered AT the depth cap must stop the walk without recursing into it", reach)
			}
			want := []string{"l1", "l1/l2", "l1/l2/l3"}
			if len(submodules) != len(want) {
				t.Fatalf("submodules = %+v, want the SAME %d entries as the bounded case (the depth-3 entry is recorded before the cap fires)", submodules, len(want))
			}
			for i, w := range want {
				if submodules[i].Path != w {
					t.Fatalf("submodules[%d] = %q, want %q", i, submodules[i].Path, w)
				}
			}
		})
	})

	t.Run("StoreCapBoundary", func(t *testing.T) {
		t.Run("Exactly50IsBounded", func(t *testing.T) {
			repo := rppRepo(t)
			rppBulkAddGitlinks(t, repo, 50, func(i int) string { return fmt.Sprintf("s%02d", i) })
			submodules, reach := walkSubmoduleReach(repo)
			if reach != "bounded" {
				t.Fatalf("reach = %q, want bounded at exactly the 50-store cap", reach)
			}
			if len(submodules) != 50 {
				t.Fatalf("len(submodules) = %d, want 50 (all retained)", len(submodules))
			}
		})

		t.Run("51stTriggersUnknownRetainingOnlyFirst50", func(t *testing.T) {
			repo := rppRepo(t)
			rppBulkAddGitlinks(t, repo, 51, func(i int) string { return fmt.Sprintf("s%02d", i) })
			submodules, reach := walkSubmoduleReach(repo)
			if reach != "unknown" {
				t.Fatalf("reach = %q, want unknown past the 50-store cap", reach)
			}
			if len(submodules) != 50 {
				t.Fatalf("len(submodules) = %d, want exactly 50 retained (the 51st checked-but-failing entry is never appended)", len(submodules))
			}
			if submodules[0].Path != "s00" || submodules[49].Path != "s49" {
				t.Fatalf("submodules = %+v, want the first 50 (s00..s49), not s01..s50", submodules)
			}
		})
	})
}

func TestRebasePlanProbe_ResolveSubmoduleRecursion(t *testing.T) {
	t.Run("ModeNoShortCircuitsWithoutRunningGitAtAll", func(t *testing.T) {
		notRepo := t.TempDir() // not even a Git repository
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "fetch.recursesubmodules", ValuePresent: true, Value: "false"},
		}}
		t.Setenv("PATH", "") // if a git process were ever attempted, it could not even be found
		got := ResolveSubmoduleRecursion(inv, notRepo)
		want := PlanSubmoduleRecursion{Mode: "no", ModeSource: "fetch.recurseSubmodules", Reach: "none", Submodules: []PlanSubmoduleEntry{}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v (a git invocation attempt with PATH broken would have produced reach:\"unknown\", not \"none\")", got, want)
		}
	})

	t.Run("OtherModesDoWalk", func(t *testing.T) {
		repo := rppRepo(t)
		rppAddGitlink(t, repo, rppFakeGitlinkSHA, "sub")
		inv := GitConfigInventory{Available: true, Entries: []GitConfigEntry{
			{Key: "fetch.recursesubmodules", ValuePresent: true, Value: "on-demand"},
		}}
		got := ResolveSubmoduleRecursion(inv, repo)
		if got.Mode != "on-demand" || got.Reach != "bounded" || len(got.Submodules) != 1 || got.Submodules[0].Path != "sub" {
			t.Fatalf("got %+v, want Mode=on-demand Reach=bounded with one submodule \"sub\"", got)
		}
	})
}

// ============================================================================
// Branch holder inventory (§7.9, §14.4, §18.3)
// ============================================================================

func TestRebasePlanProbe_BranchHolderInventory(t *testing.T) {
	t.Run("CheckedOutWorktreesHoldTheirOwnBranch", func(t *testing.T) {
		repo := rppRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt")
		gitInTest(t, repo, "branch", "feature")
		gitInTest(t, repo, "worktree", "add", wtPath, "feature")

		inv := BuildBranchHolderInventory(repo)
		if !inv.Available {
			t.Fatalf("inventory unavailable: %v", inv.Err)
		}
		wantRepoRoot, err := filepath.EvalSymlinks(repo)
		if err != nil {
			t.Fatal(err)
		}
		wantWT, err := filepath.EvalSymlinks(wtPath)
		if err != nil {
			t.Fatal(err)
		}
		main, ok := inv.ByBranch["main"]
		if !ok || main.Mechanism != HoldCheckedOut || main.Worktree != filepath.Clean(wantRepoRoot) {
			t.Fatalf("ByBranch[main] = %+v ok=%v, want HoldCheckedOut at %s", main, ok, wantRepoRoot)
		}
		feature, ok := inv.ByBranch["feature"]
		if !ok || feature.Mechanism != HoldCheckedOut || feature.Worktree != filepath.Clean(wantWT) {
			t.Fatalf("ByBranch[feature] = %+v ok=%v, want HoldCheckedOut at %s", feature, ok, wantWT)
		}
	})

	t.Run("BareRepositoryRecordSkipped", func(t *testing.T) {
		rppNeutralize(t)
		bare := t.TempDir()
		gitInTest(t, bare, "init", "-q", "--bare")
		inv := BuildBranchHolderInventory(bare)
		if !inv.Available {
			t.Fatalf("inventory unavailable: %v", inv.Err)
		}
		if len(inv.ByBranch) != 0 {
			t.Fatalf("ByBranch = %+v, want empty (the bare record must be skipped, not misreported as holding some branch)", inv.ByBranch)
		}
	})

	t.Run("MidOperationWorktreesHoldViaTheirOwnMarkers", func(t *testing.T) {
		repo := rppRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("A\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, repo, "add", "file.txt")
		gitInTest(t, repo, "commit", "-m", "base")
		for _, branch := range []string{"feat-merge", "feat-apply", "feat-bisect"} {
			gitInTest(t, repo, "branch", branch)
		}
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("C-main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInTest(t, repo, "commit", "-am", "main diverges")

		writeAndCommit := func(branch, content string) {
			gitInTest(t, repo, "checkout", "-q", branch)
			if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			gitInTest(t, repo, "commit", "-am", "diverge-"+branch)
		}
		writeAndCommit("feat-merge", "B-merge\n")
		writeAndCommit("feat-apply", "B-apply\n")
		gitInTest(t, repo, "checkout", "-q", "feat-bisect")
		baseSHA := rppHeadSHA(t, repo)
		for _, content := range []string{"B-bisect-1\n", "B-bisect-2\n", "B-bisect-3\n"} {
			if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			gitInTest(t, repo, "commit", "-am", "step")
		}
		gitInTest(t, repo, "checkout", "-q", "main")

		wtMerge := filepath.Join(t.TempDir(), "wt-merge")
		gitInTest(t, repo, "worktree", "add", wtMerge, "feat-merge")
		if _, err := runGitConflict(wtMerge, "rebase", "--merge", "main"); err == nil {
			t.Fatal("expected the rebase to conflict and pause")
		}

		wtApply := filepath.Join(t.TempDir(), "wt-apply")
		gitInTest(t, repo, "worktree", "add", wtApply, "feat-apply")
		if _, err := runGitConflict(wtApply, "rebase", "--apply", "main"); err == nil {
			t.Fatal("expected the rebase to conflict and pause")
		}

		wtBisect := filepath.Join(t.TempDir(), "wt-bisect")
		gitInTest(t, repo, "worktree", "add", wtBisect, "feat-bisect")
		gitInTest(t, wtBisect, "bisect", "start")
		gitInTest(t, wtBisect, "bisect", "bad")
		gitInTest(t, wtBisect, "bisect", "good", baseSHA)

		inv := BuildBranchHolderInventory(repo)
		if !inv.Available {
			t.Fatalf("inventory unavailable: %v", inv.Err)
		}
		if rec, ok := inv.ByBranch["feat-merge"]; !ok || rec.Mechanism != HoldRebaseMerge {
			t.Fatalf("ByBranch[feat-merge] = %+v ok=%v, want HoldRebaseMerge", rec, ok)
		}
		if rec, ok := inv.ByBranch["feat-apply"]; !ok || rec.Mechanism != HoldRebaseApply {
			t.Fatalf("ByBranch[feat-apply] = %+v ok=%v, want HoldRebaseApply", rec, ok)
		}
		if rec, ok := inv.ByBranch["feat-bisect"]; !ok || rec.Mechanism != HoldBisect {
			t.Fatalf("ByBranch[feat-bisect] = %+v ok=%v, want HoldBisect", rec, ok)
		}
	})

	t.Run("PrunableDeletedWorktreeStillHoldsItsBranch", func(t *testing.T) {
		repo := rppRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt")
		gitInTest(t, repo, "branch", "feature")
		gitInTest(t, repo, "worktree", "add", wtPath, "feature")
		rawPorcelain := rppWorktreePorcelainPath(t, repo, wtPath)
		if err := os.RemoveAll(wtPath); err != nil {
			t.Fatal(err)
		}

		inv := BuildBranchHolderInventory(repo)
		if !inv.Available {
			t.Fatalf("inventory unavailable: %v", inv.Err)
		}
		rec, ok := inv.ByBranch["feature"]
		if !ok {
			t.Fatal("expected the deleted-but-not-yet-pruned worktree to still hold its branch")
		}
		if !rec.Prunable {
			t.Fatal("expected Prunable=true for a worktree whose directory no longer exists")
		}
		if rec.Worktree != filepath.Clean(rawPorcelain) {
			t.Fatalf("Worktree = %q, want %q (canonicalize falls back to a plain clean once EvalSymlinks can no longer resolve the deleted directory)", rec.Worktree, filepath.Clean(rawPorcelain))
		}
	})

	t.Run("UnavailableOutsideAnyRepository", func(t *testing.T) {
		notRepo := rppNotARepo(t)
		inv := BuildBranchHolderInventory(notRepo)
		if inv.Available {
			t.Fatalf("expected Available=false, got %+v", inv)
		}
		if inv.Err == nil {
			t.Fatal("expected a non-nil Err")
		}
	})
}

// runGitConflict runs `git -C dir <args...>`, tolerating (indeed usually
// expecting) a non-zero exit — a conflicting rebase itself always exits
// non-zero — while still neutralizing the environment identically to
// gitInTest, so callers that need to assert on the FAILURE itself (a paused
// conflict) can inspect it instead of gitInTest's t.Fatalf on any error.
func runGitConflict(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+dir,
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// rppWorktreePorcelainPath returns git's own raw `git worktree list
// --porcelain` path string for wtPath, exactly as Git reports it (BEFORE any
// canonicalization this test's production code under test performs) — the
// oracle a post-deletion Worktree field is compared against, since
// canonicalize can no longer call EvalSymlinks once wtPath stops existing.
func rppWorktreePorcelainPath(t *testing.T, repo, wtPath string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		t.Fatal(err)
	}
	out := gitInTest(t, repo, "worktree", "list", "--porcelain")
	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			if filepath.Clean(after) == filepath.Clean(resolved) {
				return after
			}
		}
	}
	t.Fatalf("worktree %s not found in %q", wtPath, out)
	return ""
}

func TestRebasePlanProbe_ResolveWorktreeGitDir(t *testing.T) {
	t.Run("MainWorktreePlainDirectory", func(t *testing.T) {
		repo := rppRepo(t)
		gitDir, err := resolveWorktreeGitDir(repo)
		if err != nil {
			t.Fatalf("resolveWorktreeGitDir: %v", err)
		}
		if gitDir != filepath.Join(repo, ".git") {
			t.Fatalf("gitDir = %q, want %q verbatim (a plain directory .git is returned unresolved)", gitDir, filepath.Join(repo, ".git"))
		}
	})

	t.Run("LinkedWorktreePointerFile", func(t *testing.T) {
		repo := rppRepo(t)
		wtPath := filepath.Join(t.TempDir(), "wt")
		gitInTest(t, repo, "branch", "feature")
		gitInTest(t, repo, "worktree", "add", wtPath, "feature")

		want := rppWorktreeGitDir(t, wtPath)
		gitDir, err := resolveWorktreeGitDir(wtPath)
		if err != nil {
			t.Fatalf("resolveWorktreeGitDir: %v", err)
		}
		if gitDir != want {
			t.Fatalf("gitDir = %q, want %q", gitDir, want)
		}
		if info, statErr := os.Stat(filepath.Join(gitDir, "HEAD")); statErr != nil || info.IsDir() {
			t.Fatalf("resolved gitDir %q does not look like a real per-worktree git directory", gitDir)
		}
	})

	t.Run("EmptyPathIsAnError", func(t *testing.T) {
		if _, err := resolveWorktreeGitDir(""); err == nil {
			t.Fatal("expected an error for an empty path")
		}
	})

	t.Run("SymlinkedDotGitRefused", func(t *testing.T) {
		sub := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(sub, ".git")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveWorktreeGitDir(sub); err == nil {
			t.Fatal("expected an error: a symlinked .git must never be followed")
		}
	})

	t.Run("SymlinkedResolvedTargetRefused", func(t *testing.T) {
		sub := t.TempDir()
		realGitDir := t.TempDir()
		symlinkedGitDir := filepath.Join(t.TempDir(), "gitdir-symlink")
		if err := os.Symlink(realGitDir, symlinkedGitDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+symlinkedGitDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveWorktreeGitDir(sub); err == nil {
			t.Fatal("expected an error: the pointer file's OWN resolved target must also be refused when it is itself a symlink")
		}
	})

	t.Run("MalformedPointerFile", func(t *testing.T) {
		sub := t.TempDir()
		if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("not-a-gitdir-line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveWorktreeGitDir(sub); err == nil {
			t.Fatal("expected an error for a .git file lacking the gitdir: prefix")
		}
	})
}

func TestRebasePlanProbe_BuildPlanHolderIndex(t *testing.T) {
	t.Run("SharesOneInventoryAcrossContextsWithTheSameCommonDir", func(t *testing.T) {
		repo := rppRepo(t)
		idEntry, err := EstablishContextIdentity(repo, "entry-repo")
		if err != nil {
			t.Fatal(err)
		}
		idWorktree, err := EstablishContextIdentity(repo, "worktree")
		if err != nil {
			t.Fatal(err)
		}
		if idEntry.ContextID == idWorktree.ContextID {
			t.Fatal("test setup error: expected distinct context_ids to actually exercise the sharing logic")
		}
		ids := PlanContextIdentities{idEntry.ContextID: idEntry, idWorktree.ContextID: idWorktree}

		index := BuildPlanHolderIndex(ids, []string{idEntry.ContextID, idWorktree.ContextID})
		invEntry, ok1 := index.ByContext[idEntry.ContextID]
		invWorktree, ok2 := index.ByContext[idWorktree.ContextID]
		if !ok1 || !ok2 {
			t.Fatalf("expected both context_ids present, got %+v", index.ByContext)
		}
		if reflect.ValueOf(invEntry.ByBranch).Pointer() != reflect.ValueOf(invWorktree.ByBranch).Pointer() {
			t.Fatal("expected the SAME underlying ByBranch map to be shared across two context_ids resolving to the same common dir (one BuildBranchHolderInventory call, not two)")
		}
	})

	t.Run("SkipsANeedEntryAbsentFromIDs", func(t *testing.T) {
		index := BuildPlanHolderIndex(PlanContextIdentities{}, []string{"ghost-context-id"})
		if len(index.ByContext) != 0 {
			t.Fatalf("ByContext = %+v, want empty: a need entry absent from ids must be silently skipped", index.ByContext)
		}
	})
}

// ============================================================================
// Fetch-effect resolution integration tests (§11.4-§11.7)
// ============================================================================

func TestRebasePlanProbe_ResolveFetchEffect(t *testing.T) {
	realCaps, _, err := ProbeGitCapabilities()
	if err != nil {
		t.Fatalf("ProbeGitCapabilities: %v", err)
	}

	t.Run("NotContactedStillResolvesSubmoduleRecursionUnconditionally", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "config", "submodule.recurse", "true")
		inv := ProbeGitConfigInventory(repo)
		if !inv.Available {
			t.Fatalf("inventory unavailable: %v", inv.Err)
		}
		holders := BuildBranchHolderInventory(repo)

		effect := ResolveFetchEffect(inv, repo, rppCommonDir(t, repo), realCaps, holders)
		if effect.Contacted {
			t.Fatalf("expected Contacted=false with no configured remote, got %+v", effect)
		}
		if effect.All {
			t.Fatal("expected All=false")
		}
		if len(effect.Remotes) != 0 {
			t.Fatalf("expected zero remotes, got %+v", effect.Remotes)
		}
		if effect.MayDeleteRefs || effect.MayDeleteTags || effect.MayClobberTags || effect.MayUpdateLocalBranches || effect.MayDeleteLocalBranches {
			t.Fatalf("expected every May* flag false when not contacted, got %+v", effect)
		}
		if effect.HeadBranch == nil || *effect.HeadBranch != "main" {
			t.Fatalf("HeadBranch = %v, want main (resolved before the contacted check)", effect.HeadBranch)
		}
		if effect.SubmoduleRecursion.Mode != "yes" {
			t.Fatalf("SubmoduleRecursion.Mode = %q, want yes: submodule recursion must resolve UNCONDITIONALLY, even when fetch never contacts a remote", effect.SubmoduleRecursion.Mode)
		}
	})

	t.Run("SingleRemoteDefaultRefspecDoesNotCoverLocalBranches", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/repo.git")
		inv := ProbeGitConfigInventory(repo)
		holders := BuildBranchHolderInventory(repo)

		effect := ResolveFetchEffect(inv, repo, rppCommonDir(t, repo), realCaps, holders)
		if !effect.Contacted {
			t.Fatal("expected Contacted=true with a sole configured remote (sole-remote fallback)")
		}
		if len(effect.Remotes) != 1 || effect.Remotes[0].Name != "origin" {
			t.Fatalf("Remotes = %+v, want exactly [origin]", effect.Remotes)
		}
		if effect.MayUpdateLocalBranches || effect.MayDeleteLocalBranches {
			t.Fatalf("default `git remote add` refspec targets refs/remotes/*, not refs/heads/*: expected both local-branch flags false, got %+v", effect)
		}
		if effect.MayDeleteRefs || effect.MayDeleteTags || effect.MayClobberTags {
			t.Fatalf("expected no prune/tag flags without any prune configuration, got %+v", effect)
		}
	})

	t.Run("FetchAllGatedByCapability", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/origin.git")
		gitInTest(t, repo, "remote", "add", "upstream", "https://example.invalid/upstream.git")
		gitInTest(t, repo, "config", "branch.main.remote", "origin")
		gitInTest(t, repo, "config", "fetch.all", "true")
		inv := ProbeGitConfigInventory(repo)
		holders := BuildBranchHolderInventory(repo)
		commonDir := rppCommonDir(t, repo)

		withAll := ResolveFetchEffect(inv, repo, commonDir, realCaps, holders)
		if !withAll.All {
			t.Fatal("expected All=true: fetch.all is set and this capability set includes CapFetchAll")
		}
		if len(withAll.Remotes) != 2 || withAll.Remotes[0].Name != "origin" || withAll.Remotes[1].Name != "upstream" {
			t.Fatalf("Remotes = %+v, want [origin upstream] sorted by name", withAll.Remotes)
		}

		gatedCaps := realCaps
		gatedCaps.CapFetchAll = false
		withoutCap := ResolveFetchEffect(inv, repo, commonDir, gatedCaps, holders)
		if withoutCap.All {
			t.Fatal("expected All=false when CapFetchAll is false, regardless of the fetch.all config value")
		}
		if len(withoutCap.Remotes) != 1 || withoutCap.Remotes[0].Name != "origin" {
			t.Fatalf("Remotes = %+v, want exactly [origin] (falls back to the single resolved remote name)", withoutCap.Remotes)
		}
	})

	t.Run("PruneAndForcedTagAndHeadCoveringRefspecEffects", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/repo.git")
		gitInTest(t, repo, "config", "remote.origin.prune", "true")
		gitInTest(t, repo, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*")
		gitInTest(t, repo, "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
		inv := ProbeGitConfigInventory(repo)
		holders := BuildBranchHolderInventory(repo)

		effect := ResolveFetchEffect(inv, repo, rppCommonDir(t, repo), realCaps, holders)
		if !effect.MayDeleteRefs {
			t.Fatal("expected MayDeleteRefs=true: remote.origin.prune is set")
		}
		if !effect.MayDeleteTags {
			t.Fatal("expected MayDeleteTags=true: prune is set and an explicit refspec covers refs/tags/")
		}
		if !effect.MayClobberTags {
			t.Fatal("expected MayClobberTags=true: the tag-covering refspec is forced (+)")
		}
		if !effect.MayUpdateLocalBranches {
			t.Fatal("expected MayUpdateLocalBranches=true: an explicit refspec covers refs/heads/")
		}
		if !effect.MayDeleteLocalBranches {
			t.Fatal("expected MayDeleteLocalBranches=true: prune is set and a refspec covers refs/heads/")
		}
	})

	t.Run("NoTagCoveringRefspecLeavesTagFlagsFalseEvenWithPrune", func(t *testing.T) {
		repo := rppRepo(t)
		gitInTest(t, repo, "remote", "add", "origin", "https://example.invalid/repo.git")
		gitInTest(t, repo, "config", "remote.origin.prune", "true")
		inv := ProbeGitConfigInventory(repo)
		holders := BuildBranchHolderInventory(repo)

		effect := ResolveFetchEffect(inv, repo, rppCommonDir(t, repo), realCaps, holders)
		if !effect.MayDeleteRefs {
			t.Fatal("expected MayDeleteRefs=true: prune is set")
		}
		if effect.MayDeleteTags {
			t.Fatal("expected MayDeleteTags=false: prune alone does not cover tags without a tag-covering refspec or PruneTags")
		}
		if effect.MayClobberTags {
			t.Fatal("expected MayClobberTags=false: no forced tag-covering refspec is configured")
		}
	})
}

// ============================================================================
// Remote-tracking ref reads (§14.1a)
// ============================================================================

func TestRebasePlanProbe_ReadRemoteTrackingRefs(t *testing.T) {
	t.Run("EmptyNonNilMapWhenNoneExist", func(t *testing.T) {
		repo := rppRepo(t)
		refs, err := ReadRemoteTrackingRefs(repo, "origin")
		if err != nil {
			t.Fatalf("ReadRemoteTrackingRefs: %v", err)
		}
		if refs == nil {
			t.Fatal("expected a non-nil empty map")
		}
		if len(refs) != 0 {
			t.Fatalf("expected zero entries, got %+v", refs)
		}
	})

	t.Run("PopulatedAndKeyedByShortNameFilteredByRemote", func(t *testing.T) {
		repo := rppRepo(t)
		mainSHA := rppHeadSHA(t, repo)
		rppCommitN(t, repo, 1, "extra")
		featureSHA := rppHeadSHA(t, repo)
		gitInTest(t, repo, "update-ref", "refs/remotes/origin/main", mainSHA)
		gitInTest(t, repo, "update-ref", "refs/remotes/origin/feature", featureSHA)
		gitInTest(t, repo, "update-ref", "refs/remotes/upstream/main", featureSHA)

		refs, err := ReadRemoteTrackingRefs(repo, "origin")
		if err != nil {
			t.Fatalf("ReadRemoteTrackingRefs: %v", err)
		}
		if len(refs) != 2 {
			t.Fatalf("refs = %+v, want exactly 2 entries (upstream's ref must be excluded)", refs)
		}
		if refs["main"] != mainSHA {
			t.Fatalf("refs[main] = %q, want %q", refs["main"], mainSHA)
		}
		if refs["feature"] != featureSHA {
			t.Fatalf("refs[feature] = %q, want %q", refs["feature"], featureSHA)
		}
		if _, ok := refs["upstream"]; ok {
			t.Fatal("did not expect any key derived from the upstream remote's own refs")
		}
	})

	t.Run("ErrorOutsideAnyRepository", func(t *testing.T) {
		notRepo := rppNotARepo(t)
		if _, err := ReadRemoteTrackingRefs(notRepo, "origin"); err == nil {
			t.Fatal("expected an error outside any repository")
		}
	})
}
