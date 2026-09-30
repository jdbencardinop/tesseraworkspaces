package internal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type cutoffFixture struct {
	repo, root, shared, oldParent, newParent, child, currentChild string
}

func cutoffGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Cutoff Test", "GIT_AUTHOR_EMAIL=cutoff@example.com",
		"GIT_COMMITTER_NAME=Cutoff Test", "GIT_COMMITTER_EMAIL=cutoff@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return strings.TrimSpace(string(out))
}

func cutoffCommit(t *testing.T, repo, file, body, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cutoffGit(t, repo, "add", file)
	cutoffGit(t, repo, "commit", "-m", message)
	return cutoffGit(t, repo, "rev-parse", "HEAD")
}

func newCutoffFixture(t *testing.T) cutoffFixture {
	t.Helper()
	repo := t.TempDir()
	cutoffGit(t, repo, "init", "-b", "main")
	root := cutoffCommit(t, repo, "history.txt", "root\n", "root")
	shared := cutoffCommit(t, repo, "history.txt", "root\nshared\n", "shared")
	cutoffGit(t, repo, "switch", "-c", "old-parent")
	oldParent := cutoffCommit(t, repo, "parent-old.txt", "old\n", "old parent")
	cutoffGit(t, repo, "switch", "-c", "child")
	child := cutoffCommit(t, repo, "child.txt", "child\n", "child")
	cutoffGit(t, repo, "switch", "main")
	cutoffGit(t, repo, "switch", "-c", "new-parent", shared)
	newParent := cutoffCommit(t, repo, "parent-new.txt", "new\n", "new parent")
	cutoffGit(t, repo, "switch", "-c", "current-child")
	currentChild := cutoffCommit(t, repo, "current-child.txt", "current\n", "current child")
	return cutoffFixture{
		repo: repo, root: root, shared: shared, oldParent: oldParent,
		newParent: newParent, child: child, currentChild: currentChild,
	}
}

func TestResolveSyncCutoffRecordedAndMissingPolicies(t *testing.T) {
	f := newCutoffFixture(t)
	common := cutoffGit(t, f.repo, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(f.repo, common)
	}

	t.Run("correct recorded rewritten parent cutoff", func(t *testing.T) {
		got, err := ResolveSyncCutoff(SyncCutoffResolveInput{
			RepoDir: f.repo, RepoCommonDir: common, Entry: "child", GitBranch: "child",
			ParentRef: "new-parent", ParentSHA: f.newParent, ChildSHA: f.child,
			Recorded: f.oldParent, Applicable: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Validity != SyncCutoffValid || got.Source != SyncCutoffSourceRecorded ||
			got.EffectiveSHA != f.oldParent || got.RecordedSHA != f.oldParent {
			t.Fatalf("decision = %+v", got)
		}
	})

	t.Run("missing cutoff uses exact ancestor parent", func(t *testing.T) {
		got, err := ResolveSyncCutoff(SyncCutoffResolveInput{
			RepoDir: f.repo, RepoCommonDir: common, Entry: "current-child", GitBranch: "current-child",
			ParentRef: "new-parent", ParentSHA: f.newParent, ChildSHA: f.currentChild,
			Applicable: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != SyncCutoffSourceParentTip || got.EffectiveSHA != f.newParent ||
			got.Reason != SyncCutoffReasonValidParentTip {
			t.Fatalf("decision = %+v", got)
		}
	})

	t.Run("missing cutoff refuses divergent parent", func(t *testing.T) {
		_, err := ResolveSyncCutoff(SyncCutoffResolveInput{
			RepoDir: f.repo, RepoCommonDir: common, Entry: "child", GitBranch: "child",
			ParentRef: "new-parent", ParentSHA: f.newParent, ChildSHA: f.child,
			Applicable: true,
		})
		var refusal *SyncCutoffRefusalError
		if !errors.As(err, &refusal) || refusal.Decision.Reason != SyncCutoffReasonMissingParentNotAncestor {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("stale ancestor before shared history refuses", func(t *testing.T) {
		_, err := ResolveSyncCutoff(SyncCutoffResolveInput{
			RepoDir: f.repo, RepoCommonDir: common, Entry: "child", GitBranch: "child",
			ParentRef: "new-parent", ParentSHA: f.newParent, ChildSHA: f.child,
			Recorded: f.root, Applicable: true,
		})
		var refusal *SyncCutoffRefusalError
		if !errors.As(err, &refusal) ||
			refusal.Decision.Reason != SyncCutoffReasonRecordedPredatesShared ||
			refusal.Decision.SharedSHA != f.shared {
			t.Fatalf("err = %v decision=%+v", err, refusal)
		}
	})

	t.Run("nonancestor and missing records refuse", func(t *testing.T) {
		for name, recorded := range map[string]string{
			"nonancestor": f.newParent,
			"missing":     strings.Repeat("f", len(f.root)),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := ResolveSyncCutoff(SyncCutoffResolveInput{
					RepoDir: f.repo, RepoCommonDir: common, Entry: "child", GitBranch: "child",
					ParentRef: "new-parent", ParentSHA: f.newParent, ChildSHA: f.child,
					Recorded: recorded, Applicable: true,
				})
				var refusal *SyncCutoffRefusalError
				if !errors.As(err, &refusal) {
					t.Fatalf("err = %v", err)
				}
				if name == "nonancestor" && refusal.Decision.Reason != SyncCutoffReasonRecordedNotAncestor {
					t.Fatalf("reason = %s", refusal.Decision.Reason)
				}
				if name == "missing" && refusal.Decision.Reason != SyncCutoffReasonRecordedUnresolvable {
					t.Fatalf("reason = %s", refusal.Decision.Reason)
				}
			})
		}
	})
}

func TestSyncLatestObservedRefChangesUsesActualImages(t *testing.T) {
	tx := &SyncTransaction{Actions: []SyncTransactionAction{{
		Status: SyncTxnActionObserved, RepoCommonDir: "/repo",
		BeforeRefs: []SyncTransactionRefValue{
			{Ref: "refs/heads/child", SHA: "a"},
			{Ref: "refs/heads/parent", SHA: "b"},
			{Ref: "refs/heads/untouched", SHA: "c"},
		},
		AfterRefs: []SyncTransactionRefValue{
			{Ref: "refs/heads/child", SHA: "d"},
			{Ref: "refs/heads/parent", SHA: "e"},
			{Ref: "refs/heads/untouched", SHA: "c"},
		},
	}}}
	got := SyncLatestObservedRefChanges(tx)
	if len(got) != 2 || got[0].Ref != "refs/heads/child" || got[1].Ref != "refs/heads/parent" {
		t.Fatalf("changes = %+v", got)
	}
}

func TestResolveSyncCutoffExaminesAllBestMergeBases(t *testing.T) {
	repo := t.TempDir()
	cutoffGit(t, repo, "init", "-b", "main")
	tree := cutoffGit(t, repo, "mktree")
	commitTree := func(message string, parents ...string) string {
		t.Helper()
		args := []string{"commit-tree", tree, "-m", message}
		for _, parent := range parents {
			args = append(args, "-p", parent)
		}
		return cutoffGit(t, repo, args...)
	}
	recorded := commitTree("recorded")
	sharedB := commitTree("shared-b", recorded)
	sharedA := commitTree("shared-a")
	parent := commitTree("parent", sharedA, sharedB)
	child := commitTree("child", sharedB, sharedA)
	if recorded == sharedA || sharedA == sharedB {
		t.Fatalf("fixture nodes collapsed: recorded=%s sharedA=%s sharedB=%s", recorded, sharedA, sharedB)
	}
	bases, err := syncCutoffMergeBases(repo, parent, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(bases) != 2 || !slices.Contains(bases, sharedA) || !slices.Contains(bases, sharedB) {
		t.Fatalf("merge bases = %v, want exactly [%s %s]", bases, sharedA, sharedB)
	}

	_, err = ResolveSyncCutoff(SyncCutoffResolveInput{
		RepoDir: repo, Entry: "child", GitBranch: "child",
		ParentRef: "parent", ParentSHA: parent, ChildSHA: child,
		Recorded: recorded, Applicable: true,
	})
	var refusal *SyncCutoffRefusalError
	if !errors.As(err, &refusal) || refusal.Decision.Reason != SyncCutoffReasonRecordedPredatesShared {
		t.Fatalf("criss-cross stale cutoff accepted: err=%v decision=%+v", err, refusal)
	}
	if refusal.Decision.SharedSHA != sharedB {
		t.Fatalf("shared proof = %s, want the best base descended from the record %s", refusal.Decision.SharedSHA, sharedB)
	}
}

func TestSyncCutoffRefusalIncludesSanitizedParentAndRepositoryContext(t *testing.T) {
	err := (&SyncCutoffRefusalError{Decision: SyncCutoffDecision{
		Entry: "logical\nentry", GitBranch: "physical/branch\rname",
		RepoCommonDir: "/repo/\tcommon", ParentRef: "refs/tags/base\n--inject",
		ParentSHA: "parent\nsha", ChildSHA: "child\rsha", Recorded: "recorded\tsha",
		Reason: SyncCutoffReasonRecordedNotAncestor,
	}}).Error()
	if strings.ContainsAny(err, "\n\r\t\x1b") {
		t.Fatalf("refusal is not sanitized: %q", err)
	}
	for _, want := range []string{
		`entry "logical?entry"`,
		`git branch "physical/branch?name"`,
		`repository "/repo/?common"`,
		`parent-ref="refs/tags/base?--inject"`,
		`frozen-parent=parent?sha`,
		`child=child?sha`,
		`recorded=recorded?sha`,
	} {
		if !strings.Contains(err, want) {
			t.Fatalf("refusal %q lacks %q", err, want)
		}
	}
}
