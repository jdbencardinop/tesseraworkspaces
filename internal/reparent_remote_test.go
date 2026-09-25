package internal

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2).
//
//	T-075 remote guidance content and ordering; origin everywhere .. AC-087, AC-093
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// §12.3 — the record's paths, durable IO, and its initial-state rule.
// ---------------------------------------------------------------------------

func TestReparentRemoteRecordRoundTripAndInitialState(t *testing.T) {
	external, checkout := newReparentStateFixture(t)

	if !strings.HasSuffix(ReparentRemoteRecordPath(external), ".reparent-remote.v1.yaml") {
		t.Fatalf("external record path = %s", ReparentRemoteRecordPath(external))
	}
	if !strings.HasSuffix(ReparentRemoteRecordPath(checkout), "customer-reparent-remote.v1.yaml") {
		t.Fatalf("checkout record path = %s", ReparentRemoteRecordPath(checkout))
	}
	if !strings.HasPrefix(ReparentRemoteRecordPath(checkout), checkout.CheckoutStateDir) {
		t.Fatalf("the checkout record must live under the workspace state dir: %s", ReparentRemoteRecordPath(checkout))
	}

	// The initial-state rule: an entry with no observed remote value was
	// never published, so there is no remote history for this run to protect
	// and the row is written CLEARED.
	if got := reparentRemoteInitialState(""); got != ReparentRemoteStateCleared {
		t.Fatalf("never-published initial state = %q", got)
	}
	if got := reparentRemoteInitialState("abc"); got != ReparentRemoteStatePending {
		t.Fatalf("published initial state = %q", got)
	}

	rec := ReparentRemoteRecord{
		Feature:   "customer",
		RunID:     strings.Repeat("a", 32),
		CreatedAt: reparentNow(),
		Entries: []ReparentRemoteEntry{
			{Name: "pr2", GitBranch: "feature/pr2", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/feature/pr2", NewTipSHA: strings.Repeat("b", 40),
				RemoteSHAAtWrite: strings.Repeat("a", 40), PRBaseBefore: "pr1", PRBaseAfter: "master",
				State: ReparentRemoteStatePending},
			{Name: "pr3", GitBranch: "feature/pr3", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/feature/pr3", NewTipSHA: strings.Repeat("c", 40),
				State: ReparentRemoteStateCleared},
		},
	}
	if err := SaveReparentRemoteRecord(external, rec); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ReparentRemoteRecordPath(external))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("record mode = %o, want 0600", perm)
	}

	loaded, err := LoadReparentRemoteRecord(external)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.RecordVersion != ReparentRemoteRecordVersion {
		t.Fatalf("record_version = %d", loaded.RecordVersion)
	}
	if got := loaded.PendingEntries(); len(got) != 1 || got[0] != "pr2" {
		t.Fatalf("pending entries = %v", got)
	}
	if loaded.AllCleared() {
		t.Fatal("a pending entry means the record is not all-cleared")
	}

	// A record from a future release refuses rather than being half-read.
	if err := os.WriteFile(ReparentRemoteRecordPath(external), []byte("record_version: 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReparentRemoteRecord(external); err == nil {
		t.Fatal("record_version 99 must refuse")
	}

	valid := ReparentRemoteRecord{
		RecordVersion: 1, Feature: "customer", RunID: strings.Repeat("f", 32), CreatedAt: reparentNow(),
		Entries: []ReparentRemoteEntry{{
			Name: "pr2", GitBranch: "feature/pr2", Remote: ReparentRemoteName,
			RemoteRef: "refs/remotes/origin/feature/pr2",
			NewTipSHA: strings.Repeat("a", 40), RemoteSHAAtWrite: strings.Repeat("b", 40),
			State: ReparentRemoteStatePending,
		}},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ReparentRemoteRecord)
	}{
		{"version zero", func(r *ReparentRemoteRecord) { r.RecordVersion = 0 }},
		{"empty entries", func(r *ReparentRemoteRecord) { r.Entries = nil }},
		{"duplicate entries", func(r *ReparentRemoteRecord) { r.Entries = append(r.Entries, r.Entries[0]) }},
		{"malformed remote", func(r *ReparentRemoteRecord) { r.Entries[0].Remote = "upstream" }},
		{"malformed oid", func(r *ReparentRemoteRecord) { r.Entries[0].NewTipSHA = "xyz" }},
		{"malformed state", func(r *ReparentRemoteRecord) { r.Entries[0].State = "maybe" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := valid
			subject.Entries = append([]ReparentRemoteEntry{}, valid.Entries...)
			tc.mutate(&subject)
			data, err := yaml.Marshal(&subject)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ReparentRemoteRecordPath(external), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadReparentRemoteRecord(external); err == nil {
				t.Fatalf("%s record must fail closed", tc.name)
			}
		})
	}
}

func TestMergeReparentRemoteRecordPreservesOtherRuns(t *testing.T) {
	existing := &ReparentRemoteRecord{
		Feature: "customer", RunID: "older",
		Entries: []ReparentRemoteEntry{
			{Name: "pr9", GitBranch: "feature/pr9", NewTipSHA: "999", RemoteSHAAtWrite: "888", State: ReparentRemoteStatePending},
			{Name: "pr2", GitBranch: "feature/pr2", NewTipSHA: "111", RemoteSHAAtWrite: "000", State: ReparentRemoteStatePending},
		},
	}
	incoming := ReparentRemoteRecord{
		Feature: "customer", RunID: "newer",
		Entries: []ReparentRemoteEntry{
			{Name: "pr2", GitBranch: "feature/pr2", NewTipSHA: "222", RemoteSHAAtWrite: "000", State: ReparentRemoteStatePending},
		},
	}
	merged := MergeReparentRemoteRecord(existing, incoming)
	if merged.RunID != "newer" {
		t.Fatalf("merged run id = %q", merged.RunID)
	}
	if len(merged.Entries) != 2 {
		t.Fatalf("merged entries = %+v", merged.Entries)
	}
	got, ok := merged.Entry("pr2")
	if !ok || got.NewTipSHA != "222" {
		t.Fatalf("the newer rewrite must replace the older row for the same branch: %+v", got)
	}
	if _, ok := merged.Entry("pr9"); !ok {
		t.Fatal("another branch's pending entry must be preserved verbatim")
	}

	incoming.Entries[0].NewTipSHA = "333"
	incoming.Entries[0].RemoteSHAAtWrite = ""
	incoming.Entries[0].State = ReparentRemoteStateCleared
	merged = MergeReparentRemoteRecord(existing, incoming)
	got, _ = merged.Entry("pr2")
	if got.NewTipSHA != "333" || !got.Pending() || got.RemoteSHAAtWrite != "000" {
		t.Fatalf("missing tracking ref discarded older publication evidence: %+v", got)
	}

	clearedExisting := *existing
	clearedExisting.Entries = append([]ReparentRemoteEntry{}, existing.Entries...)
	clearedExisting.Entries[1].State = ReparentRemoteStateCleared
	merged = MergeReparentRemoteRecord(&clearedExisting, incoming)
	got, _ = merged.Entry("pr2")
	if got.Pending() || got.RemoteSHAAtWrite != "" {
		t.Fatalf("positive prior clear must not resurrect pending evidence: %+v", got)
	}
}

func TestRemoveReparentRemoteEntriesDeletesOnlyThisRun(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	rec := ReparentRemoteRecord{
		Feature: "customer", RunID: strings.Repeat("d", 32),
		Entries: []ReparentRemoteEntry{
			{Name: "pr2", GitBranch: "feature/pr2", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/feature/pr2", NewTipSHA: strings.Repeat("2", 40),
				RemoteSHAAtWrite: strings.Repeat("1", 40), State: ReparentRemoteStatePending},
			{Name: "pr9", GitBranch: "feature/pr9", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/feature/pr9", NewTipSHA: strings.Repeat("9", 40),
				RemoteSHAAtWrite: strings.Repeat("8", 40), State: ReparentRemoteStatePending},
		},
	}
	if err := SaveReparentRemoteRecord(external, rec); err != nil {
		t.Fatal(err)
	}
	if err := RemoveReparentRemoteEntries(external, []string{"pr2"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReparentRemoteRecord(external)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].Name != "pr9" {
		t.Fatalf("entries after removal = %+v", loaded.Entries)
	}
	if err := RemoveReparentRemoteEntries(external, []string{"pr9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ReparentRemoteRecordPath(external)); !os.IsNotExist(err) {
		t.Fatal("an emptied record file must be deleted")
	}
}

// ---------------------------------------------------------------------------
// §12.2 — guidance.
// ---------------------------------------------------------------------------

func TestReparentRemoteGuidanceLines(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-075", "remote-guidance-order")
	_ = "asserts AC-087 AC-093"
	before, after := "pr1", "master"
	ref := "refs/remotes/origin/feature/pr2"
	rows := []ReparentPlanRemoteRow{
		{Name: "pr2", GitBranch: "feature/pr2", Remote: ReparentRemoteName, RemoteRef: &ref,
			Divergence: "diverged", PRBaseBefore: &before, PRBaseAfter: &after},
		{Name: "pr3", GitBranch: "feature/pr3", Remote: ReparentRemoteName, Divergence: "no-upstream"},
	}
	got := ReparentRemoteGuidance(rows)
	want := []string{
		"tws changed no remote ref and no pull request.",
		"pr2: PR base pr1 -> master",
		"pr2: local and refs/remotes/origin/feature/pr2 have diverged; the remote still holds the pre-reparent history.",
		"retarget the pull request first, then fetch and integrate remote work, then publish.",
		"if you force publish, use: git push --force-with-lease --force-if-includes origin feature/pr2",
	}
	if len(got) != len(want) {
		t.Fatalf("guidance = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("guidance[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "--force-with-lease=") {
		t.Fatal("guidance must never bind an explicit lease to a freshly observed remote tip")
	}
	for _, line := range got {
		if strings.HasPrefix(line, "git push") && !strings.Contains(line, "--force-with-lease") {
			t.Fatalf("guidance must never recommend a plain git push: %q", line)
		}
	}
}

func TestReparentProviderHint(t *testing.T) {
	for token, want := range map[string]string{
		"":                                     "unknown",
		"git@github.com:acme/repo.git":         "github",
		"https://dev.azure.com/acme/_git/x":    "azure-devops",
		"https://acme.visualstudio.com/_git/x": "azure-devops",
		"https://git.example.com/acme/x.git":   "generic",
	} {
		if got := reparentProviderHint(token); got != want {
			t.Fatalf("provider hint for %q = %q, want %q", token, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// §12.5 — clearing, locally, with no fetch and no provider call.
// ---------------------------------------------------------------------------

// The clearing observations: every §12.5 observation, evaluated read-only and then persisted by a mutating route (AC-092).
//
// §17.3 counts one t.Run leaf per real-Git cell. The assertions below were
// separate leaves; each keeps its own scope, fixture and assertions verbatim
// inside its own closure — only the leaf boundary moved.
func TestReparentRemoteClearingAgainstRealGit(t *testing.T) {
	assertReparentMatrixBehavior(t, "T-080", "remote-clear-observations")
	repo := newReparentPrimitiveRepo(t)
	// --- TestObserveReparentRemoteClearAgainstRealGit ---
	func(t *testing.T) {
		log := reparentCaptureArgv(t)

		tip := repo.RevParse("HEAD")
		repo.Git("update-ref", "refs/remotes/origin/topic", tip)
		entry := ReparentRemoteEntry{
			Name: "topic", GitBranch: "topic", Remote: ReparentRemoteName,
			RemoteRef: "refs/remotes/origin/topic", NewTipSHA: tip,
			RemoteSHAAtWrite: tip, State: ReparentRemoteStatePending,
		}

		// Observation 1: the tracking ref equals the recorded new tip.
		obs, err := ObserveReparentRemoteClear(repo.Dir, entry, nil)
		if err != nil || obs != ReparentRemoteObservationPublished {
			t.Fatalf("observation = %q (%v)", obs, err)
		}

		// Observation 2: the remote contains the rewritten history plus later work.
		later := repo.Commit("later.txt", "later")
		repo.Git("update-ref", "refs/remotes/origin/topic", later)
		obs, err = ObserveReparentRemoteClear(repo.Dir, entry, nil)
		if err != nil || obs != ReparentRemoteObservationContains {
			t.Fatalf("contains observation = %q (%v)", obs, err)
		}

		// An unrelated tracking value clears nothing.
		unrelated := repo.RevParse("HEAD~2")
		repo.Git("update-ref", "refs/remotes/origin/topic", unrelated)
		obs, err = ObserveReparentRemoteClear(repo.Dir, entry, nil)
		if err != nil || obs != ReparentRemoteObservationNone {
			t.Fatalf("unrelated observation = %q (%v)", obs, err)
		}

		// A MISSING tracking ref is not a clearing observation: the initial-state
		// rule already cleared never-published rows, and a published row whose
		// ref later disappears stays pending until the operator resolves it.
		repo.Git("update-ref", "-d", "refs/remotes/origin/topic")
		obs, err = ObserveReparentRemoteClear(repo.Dir, entry, nil)
		if err != nil || obs != ReparentRemoteObservationNone {
			t.Fatalf("missing-ref observation = %q (%v)", obs, err)
		}

		// Observation 4: the entry left stack.yaml, or is archived.
		empty := Stack{}
		obs, err = ObserveReparentRemoteClear(repo.Dir, entry, &empty)
		if err != nil || obs != ReparentRemoteObservationEntryGone {
			t.Fatalf("absent-entry observation = %q (%v)", obs, err)
		}
		archived := Stack{Branches: []StackEntry{{Name: "topic", Archived: true}}}
		obs, err = ObserveReparentRemoteClear(repo.Dir, entry, &archived)
		if err != nil || obs != ReparentRemoteObservationEntryGone {
			t.Fatalf("archived observation = %q (%v)", obs, err)
		}

		reparentAssertNoDashC(t, *log)
	}(t)

	// --- TestApplyReparentRemoteClearsPersistsAndDeletes ---
	func(t *testing.T) {
		external, _ := newReparentStateFixture(t)
		tip := repo.RevParse("HEAD")
		repo.Git("update-ref", "refs/remotes/origin/topic", tip)

		rec := ReparentRemoteRecord{
			Feature: "customer", RunID: strings.Repeat("e", 32),
			Entries: []ReparentRemoteEntry{{
				Name: "topic", GitBranch: "topic", Remote: ReparentRemoteName,
				RemoteRef: "refs/remotes/origin/topic", NewTipSHA: tip,
				RemoteSHAAtWrite: repo.RevParse("HEAD~1"), State: ReparentRemoteStatePending,
			}},
		}
		if err := SaveReparentRemoteRecord(external, rec); err != nil {
			t.Fatal(err)
		}

		// The read-only half computes the same verdict and writes NOTHING.
		clears, err := EvaluateReparentRemoteClears(repo.Dir, rec, nil)
		if err != nil || len(clears) != 1 || !clears[0].Cleared {
			t.Fatalf("in-memory evaluation = %+v (%v)", clears, err)
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(external)); err != nil {
			t.Fatal("evaluation must not delete the record")
		}

		// The mutating half persists the clear and deletes the emptied file.
		if _, err := ApplyReparentRemoteClears(external, repo.Dir, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(ReparentRemoteRecordPath(external)); !os.IsNotExist(err) {
			t.Fatal("a fully cleared record must be deleted")
		}
	}(t)

	func(t *testing.T) {
		repoA := repo
		repoBDir := canonicalize(t.TempDir())
		clone := reparentTestGitCommand(t, "", "clone", "-q", repoA.Dir, repoBDir)
		if out, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("clone related repo: %v\n%s", err, out)
		}
		repoB := &reparentRepo{t: t, Dir: repoBDir}
		tip := repoA.RevParse("HEAD")
		older := repoA.RevParse("HEAD~1")
		repoA.Git("update-ref", "refs/remotes/origin/topic", tip)
		repoB.Git("update-ref", "refs/remotes/origin/topic", older)

		external, _ := newReparentStateFixture(t)
		rec := ReparentRemoteRecord{
			Feature: "customer", RunID: strings.Repeat("7", 32),
			Entries: []ReparentRemoteEntry{
				{Name: "b", GitBranch: "topic", Repo: repoB.Dir, Remote: ReparentRemoteName,
					RemoteRef: "refs/remotes/origin/topic", NewTipSHA: tip,
					RemoteSHAAtWrite: older, State: ReparentRemoteStatePending},
				{Name: "a", GitBranch: "topic", Repo: repoA.Dir, Remote: ReparentRemoteName,
					RemoteRef: "refs/remotes/origin/topic", NewTipSHA: tip,
					RemoteSHAAtWrite: older, State: ReparentRemoteStatePending},
				{Name: "default", GitBranch: "topic", Remote: ReparentRemoteName,
					RemoteRef: "refs/remotes/origin/topic", NewTipSHA: tip,
					RemoteSHAAtWrite: older, State: ReparentRemoteStatePending},
			},
		}
		if err := SaveReparentRemoteRecord(external, rec); err != nil {
			t.Fatal(err)
		}
		if err := LoadReparentPushEnvelope(external, repoA.Dir).PersistClears(nil); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadReparentRemoteRecord(external)
		if err != nil || loaded == nil {
			t.Fatalf("load multi-repo record: %v", err)
		}
		a, _ := loaded.Entry("a")
		b, _ := loaded.Entry("b")
		defaultEntry, _ := loaded.Entry("default")
		if a.State != ReparentRemoteStateCleared ||
			b.State != ReparentRemoteStatePending ||
			defaultEntry.State != ReparentRemoteStateCleared {
			t.Fatalf("secondary-first clearing = a:%s b:%s default:%s",
				a.State, b.State, defaultEntry.State)
		}

		before, _ := os.ReadFile(ReparentRemoteRecordPath(external))
		bad := *loaded
		bad.Entries = append([]ReparentRemoteEntry{}, loaded.Entries...)
		for i := range bad.Entries {
			if bad.Entries[i].Name == "b" {
				bad.Entries[i].Repo = filepath.Join(t.TempDir(), "missing")
			}
		}
		if err := SaveReparentRemoteRecord(external, bad); err != nil {
			t.Fatal(err)
		}
		written, _ := os.ReadFile(ReparentRemoteRecordPath(external))
		if _, err := ApplyReparentRemoteClears(external, repoA.Dir, nil); err == nil {
			t.Fatal("an unavailable per-entry repository must fail the clearing pass")
		}
		after, _ := os.ReadFile(ReparentRemoteRecordPath(external))
		if string(after) != string(written) || string(before) == string(written) {
			t.Fatal("a failed per-entry clearing pass must preserve the current record bytes")
		}
	}(t)

	testReparentRemoteReplacementAbortRestoresPriorRun(t)
}

// ---------------------------------------------------------------------------
// §12.4 / §12.4a — rule R-PUSH's decision and invocation-wide preflight.
// ---------------------------------------------------------------------------

func TestReparentPushDecisionAndPreflight(t *testing.T) {
	rec := &ReparentRemoteRecord{
		Feature: "customer", RunID: "run",
		Entries: []ReparentRemoteEntry{
			{Name: "pr2", GitBranch: "feature/pr2", RemoteRef: "refs/remotes/origin/feature/pr2",
				RemoteSHAAtWrite: "aaa", PRBaseBefore: "pr1", PRBaseAfter: "master",
				State: ReparentRemoteStatePending},
			{Name: "pr3", GitBranch: "feature/pr3", State: ReparentRemoteStateCleared},
		},
	}

	// Rule 1: an entry absent from the record, or a cleared one, keeps the
	// zero decision — today's argv, output and exit status, byte for byte.
	if got := ReparentPushDecisionFor(rec, "absent"); got.Applies || got.ForceIfIncludes {
		t.Fatalf("absent entry decision = %+v", got)
	}
	if got := ReparentPushDecisionFor(rec, "pr3"); got.Applies {
		t.Fatalf("cleared entry decision = %+v", got)
	}
	if got := ReparentPushDecisionFor(nil, "pr2"); got.Applies {
		t.Fatalf("absent record decision = %+v", got)
	}

	d := ReparentPushDecisionFor(rec, "pr2")
	if !d.Applies || !d.ForceIfIncludes {
		t.Fatalf("pending entry decision = %+v", d)
	}
	want := "reparent-remote: pr2 was reparented (pr1 -> master); retarget the pull request before publishing"
	if d.WarnLine != want {
		t.Fatalf("warn line = %q, want %q", d.WarnLine, want)
	}
	if d.DryRunSuffix != "(would push --force-with-lease --force-if-includes)" {
		t.Fatalf("dry-run suffix = %q", d.DryRunSuffix)
	}

	// The preflight is INVOCATION-WIDE: it refuses before the first push.
	probes := []ReparentPushEntryProbe{{Name: "pr2", TrackingResolves: true}}
	if got := EvaluateReparentPushPreflight(rec, ReparentGitCapabilities{CapForceIfIncludes: true}, probes); got.Refuse {
		t.Fatalf("a capable git with a resolving ref must pass: %+v", got)
	}
	old := EvaluateReparentPushPreflight(rec, ReparentGitCapabilities{}, probes)
	if !old.Refuse {
		t.Fatal("a git that cannot pass --force-if-includes must refuse")
	}
	if refusal := old.Refusal(); refusal == nil || refusal.Kind != ReparentRefusalRemoteFollowupUnsafeLease {
		t.Fatalf("preflight refusal = %v", refusal)
	}
	for _, want := range []string{"pr2", "2.30", "git fetch origin", "--force-if-includes"} {
		if !strings.Contains(old.Detail, want) {
			t.Fatalf("preflight detail must name %q: %s", want, old.Detail)
		}
	}
	// A dry run reports the same verdict as a diagnostic and still exits 0.
	if line := old.DryRunLine(); !strings.HasPrefix(line, "reparent-remote: would refuse: remote-followup-unsafe-lease: ") {
		t.Fatalf("dry-run diagnostic = %q", line)
	}

	missing := EvaluateReparentPushPreflight(rec, ReparentGitCapabilities{CapForceIfIncludes: true},
		[]ReparentPushEntryProbe{{Name: "pr2", TrackingResolves: false}})
	if !missing.Refuse || len(missing.Failing) != 1 {
		t.Fatalf("a missing tracking ref must refuse: %+v", missing)
	}

	// An UNPUBLISHED pending row can never reach the gate: the initial-state
	// rule wrote it cleared.
	unpublished := &ReparentRemoteRecord{Entries: []ReparentRemoteEntry{
		{Name: "pr4", State: ReparentRemoteStatePending},
	}}
	if got := EvaluateReparentPushPreflight(unpublished, ReparentGitCapabilities{}, []ReparentPushEntryProbe{{Name: "pr4"}}); got.Refuse {
		t.Fatalf("an unpublished entry must not gate a push: %+v", got)
	}

	_, checkout := newReparentStateFixture(t)
	st := reparentStateFixture(checkout)
	st.CommitPointReached = false
	if err := SaveReparentState(checkout, st); err != nil {
		t.Fatal(err)
	}
	opts := CheckoutSyncOpts{Feature: checkout.Feature, FeaturePath: checkout.FeaturePath, Reparent: checkout}
	tx := &CheckoutTransaction{Feature: checkout.Feature, Plan: []CheckoutPlanEntry{{Name: "pr2", Branch: "pr2"}}}
	if _, err := reparentCheckoutPushEnvelope(opts, tx); err == nil || !strings.Contains(err.Error(), "reparent-state-present") {
		t.Fatalf("checkout push must refuse before the reparent commit point: %v", err)
	}
	before, err := base64.StdEncoding.DecodeString(st.StackBeforeBase64)
	if err != nil {
		t.Fatal(err)
	}
	var post Stack
	if err := yaml.Unmarshal(before, &post); err != nil {
		t.Fatal(err)
	}
	post.Branches[0].LastBaseSHA = st.NewParentSHA
	postBytes, err := yaml.Marshal(&post)
	if err != nil {
		t.Fatal(err)
	}
	postHash := hex.EncodeToString(sliceSHA256(postBytes))
	st.Rows[0].PlannedNewSHA = st.Rows[0].PreimageSHA
	st.Rows[0].Noop = true
	st.Rows[0].MaterializedArgv = append([]string{}, st.Rows[0].Argv...)
	st.Rows[0].Stage = ReparentRowCommitted
	st.StackAfterBase64 = base64.StdEncoding.EncodeToString(postBytes)
	st.StackSHA256AfterExpected = postHash
	st.StackSHA256After = postHash
	st.CASTransactionSucceeded = true
	st.CASRows = []ReparentStateCASRow{{
		Ref:      "refs/heads/" + st.Rows[0].GitBranch,
		OldValue: st.Rows[0].PreimageSHA, NewValue: st.Rows[0].PlannedNewSHA,
		Applied: true, Noop: true,
	}}
	st.RemoteRecordEmpty = true
	st.CompatArtifactsWritten = true
	st.SetStage(ReparentStageMetadataWritten)
	st.CommitPointReached = true
	if err := SaveReparentState(checkout, st); err != nil {
		t.Fatal(err)
	}
	if _, err := reparentCheckoutPushEnvelope(opts, tx); err != nil {
		t.Fatalf("committed reparent should fall through to remote-record rules: %v", err)
	}
}

// ---------------------------------------------------------------------------
// §12.4 — an untrusted record fails CLOSED. Only "no record at all" is inert.
// ---------------------------------------------------------------------------

func TestReparentPushEnvelopeFailsClosedOnAnUntrustedRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want ReparentRefusalKind
	}{
		{"unsupported record_version", "record_version: 99\nfeature: customer\n", ReparentRefusalStateUnsupported},
		{"a document that does not decode", "record_version: 1\nentries: [oops\n", ReparentRefusalStateCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			external, _ := newReparentStateFixture(t)
			if err := os.WriteFile(ReparentRemoteRecordPath(external), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}

			env := LoadReparentPushEnvelope(external, "")
			if !env.Failed() {
				t.Fatal("a record that exists and cannot be read is never `no record`")
			}
			if env.Active() {
				t.Fatal("an untrusted record has no pending entries to act on")
			}
			refusal := env.LoadRefusal()
			if refusal == nil {
				t.Fatal("the verdict must be a typed refusal")
			}

			// Every real push path runs the invocation-wide preflight, so the
			// refusal reaches all three of them without a per-path branch.
			pre := env.PreflightProbes(nil)
			if !pre.Refuse || !pre.LoadFailure {
				t.Fatalf("preflight = %+v", pre)
			}
			got := pre.Refusal()
			if got == nil || got.Kind != tc.want {
				t.Fatalf("an unreadable record must carry the loader's own verdict %s, got %v", tc.want, got)
			}
			if got.Kind == ReparentRefusalRemoteFollowupUnsafeLease {
				t.Fatal("an unreadable record is not a lease problem")
			}
			if refusal.Kind != tc.want {
				t.Fatalf("LoadRefusal kind = %s, want %s", refusal.Kind, tc.want)
			}
			if named := env.Preflight([]string{"pr2"}); !named.Refuse || !named.LoadFailure {
				t.Fatalf("named preflight = %+v", named)
			}
			// §12.4a: the dry run reports the same verdict as a diagnostic.
			if line := pre.DryRunLine(); !strings.Contains(line, "reparent-remote: would refuse: "+string(tc.want)+": ") {
				t.Fatalf("dry-run diagnostic = %q, want the %s verdict", line, tc.want)
			}
			// And nothing is ever persisted from an untrusted record.
			if err := env.PersistClears(nil); err == nil {
				t.Fatal("an untrusted record must return its load refusal")
			}
			raw, err := os.ReadFile(ReparentRemoteRecordPath(external))
			if err != nil || string(raw) != tc.body {
				t.Fatalf("the record must be left exactly as found: %q (%v)", raw, err)
			}
		})
	}
}

func TestReparentPushEnvelopeAbsentRecordStaysInert(t *testing.T) {
	external, _ := newReparentStateFixture(t)
	env := LoadReparentPushEnvelope(external, "")
	if env.Failed() || env.LoadRefusal() != nil {
		t.Fatalf("os.ErrNotExist is the ONE inert verdict: %+v", env.Err)
	}
	if env.Active() {
		t.Fatal("no record means nothing pending")
	}
	if pre := env.PreflightProbes(nil); pre.Refuse {
		t.Fatalf("rule 1 keeps today's behaviour byte-identical: %+v", pre)
	}
	if d := env.Decision("pr2"); d.Applies {
		t.Fatalf("decision = %+v", d)
	}
}
