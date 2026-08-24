package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// TestRebasePlanState — §4.5/§12.5/§12.5a: the five-artefact matrix
// (checkout_transaction, checkout_lock, external_legacy_state,
// external_payload, external_run_guard) through both InspectCheckoutPlanState
// and InspectExternalPlanState.
// ============================================================================

// ---------------------------------------------------------------------------
// Fixture helpers.
// ---------------------------------------------------------------------------

// rpsCheckoutFixture builds a real, minimal checkout-mode workspace (one real
// git repo, one commit, .tws/config.yaml) so InspectCheckoutPlanState's
// worktree/git_op/head probes exercise genuine git plumbing rather than an
// absent .git directory.
func rpsCheckoutFixture(t *testing.T) (repoDir string, ws Workspace) {
	t.Helper()
	ssNeutralizeGitConfig(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return setupHealthTestRepo(t)
}

func writeArtefact(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func makeDirAtArtefact(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// makeDanglingSymlinkArtefact points path at a target that does not exist.
// If ReadArtefactPath (or a caller) ever attempted to open/follow this
// symlink, the open would fail with ENOENT and surface as a generic io-error
// outcome rather than the clean IsSymlink/PlanPresenceSymlink outcome — so a
// dangling symlink is itself a "never opened" proof, not just a fixture.
func makeDanglingSymlinkArtefact(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	target := filepath.Join(filepath.Dir(path), "no-such-target-"+filepath.Base(path))
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink %s: %v", path, err)
	}
}

// oversizedContent returns YAML-shaped padding whose byte length exceeds
// capBytes, so the artefact trips the shared TooLarge/io-error branch before
// any decode is attempted.
func oversizedContent(capBytes int64) string {
	var b strings.Builder
	b.WriteString("# padding to exceed the artefact read cap\n")
	line := "padding: \"" + strings.Repeat("x", 200) + "\"\n"
	for int64(b.Len()) <= capBytes {
		b.WriteString(line)
	}
	return b.String()
}

// writeLockLikeFile writes the two-field {pid, created} shape LockInfo
// decodes; SyncRunGuard's richer {pid,created,token,state_version} shape is
// covered by the existing writeGuardFile helper (sync_run_state_test.go).
func writeLockLikeFile(t *testing.T, path string, pid int, created string) {
	t.Helper()
	body := "pid: " + itoa(pid) + "\ncreated: \"" + created + "\"\n"
	writeArtefact(t, path, body)
}

// assertPlanFilePresence checks the four PlanFilePresence members every
// state.files.* row shares. Err is checked only for non-nil-ness (its exact
// message is process/host-specific for genuine io errors); reason/presence
// content is checked precisely.
func assertPlanFilePresence(t *testing.T, label string, got PlanFilePresence, wantApplicable bool, wantPresence PlanPresence, wantReason *PlanUnreadableReason, wantErrNonNil bool) {
	t.Helper()
	if got.Applicable != wantApplicable {
		t.Errorf("%s: Applicable = %v, want %v", label, got.Applicable, wantApplicable)
	}
	if got.Presence != wantPresence {
		t.Errorf("%s: Presence = %q, want %q", label, got.Presence, wantPresence)
	}
	switch {
	case wantReason == nil && got.UnreadableReason != nil:
		t.Errorf("%s: UnreadableReason = %q, want nil", label, *got.UnreadableReason)
	case wantReason != nil && got.UnreadableReason == nil:
		t.Errorf("%s: UnreadableReason = nil, want %q", label, *wantReason)
	case wantReason != nil && *got.UnreadableReason != *wantReason:
		t.Errorf("%s: UnreadableReason = %q, want %q", label, *got.UnreadableReason, *wantReason)
	}
	if wantErrNonNil && got.Err == nil {
		t.Errorf("%s: Err = nil, want non-nil", label)
	}
	if !wantErrNonNil && got.Err != nil {
		t.Errorf("%s: Err = %v, want nil", label, got.Err)
	}
}

// artefactCase is one row of a per-artefact presence matrix. F is the
// concrete PlanXxxFile row type (e.g. PlanCheckoutTransactionFile).
type artefactCase[F any] struct {
	name           string
	setup          func(t *testing.T, featurePath string) // no-op for "absent"
	wantApplicable bool
	wantPresence   PlanPresence
	wantReason     *PlanUnreadableReason
	wantErrNonNil  bool
	extra          func(t *testing.T, file F)
}

// runArtefactCases drives one artefactCase table. newFeaturePath builds a
// fresh fixture root per case (so no case can leak state into another), and
// inspect runs the real production inspector and returns the one artefact
// row under test.
func runArtefactCases[F any](
	t *testing.T,
	cases []artefactCase[F],
	newFeaturePath func(t *testing.T) string,
	inspect func(t *testing.T, featurePath string) F,
	presenceOf func(F) PlanFilePresence,
) {
	t.Helper()
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			fp := newFeaturePath(t)
			c.setup(t, fp)
			file := inspect(t, fp)
			assertPlanFilePresence(t, c.name, presenceOf(file), c.wantApplicable, c.wantPresence, c.wantReason, c.wantErrNonNil)
			if c.extra != nil {
				c.extra(t, file)
			}
		})
	}
}

// commonUnreadableCases returns the six degenerate rows every artefact
// shares: absent, io-error (oversized), decode-error (invalid YAML syntax),
// empty-document (two spellings), not-regular-file (a directory in the
// artefact's place), and a dangling symlink. Artefact-specific "readable"
// and any bespoke decode-error variants are appended by each caller.
func commonUnreadableCases[F any](pathFor func(string) string, cap int64) []artefactCase[F] {
	ioError := UnreadableIOError
	decodeError := UnreadableDecodeError
	emptyDoc := UnreadableEmptyDocument
	notRegular := UnreadableNotRegularFile
	return []artefactCase[F]{
		{
			name:           "absent",
			setup:          func(t *testing.T, fp string) {},
			wantApplicable: true,
			wantPresence:   PlanPresenceAbsent,
		},
		{
			name:           "unreadable: io-error (oversized)",
			setup:          func(t *testing.T, fp string) { writeArtefact(t, pathFor(fp), oversizedContent(cap)) },
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &ioError,
			wantErrNonNil:  true,
		},
		{
			name:           "unreadable: decode-error (invalid YAML syntax)",
			setup:          func(t *testing.T, fp string) { writeArtefact(t, pathFor(fp), "bogus: [oops\n\t- broken\n") },
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &decodeError,
			wantErrNonNil:  true,
		},
		{
			name:           "unreadable: empty-document (zero bytes)",
			setup:          func(t *testing.T, fp string) { writeArtefact(t, pathFor(fp), "") },
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &emptyDoc,
		},
		{
			name:           "unreadable: empty-document (comment-only, no keys)",
			setup:          func(t *testing.T, fp string) { writeArtefact(t, pathFor(fp), "# nothing here\n") },
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &emptyDoc,
		},
		{
			name:           "unreadable: not-regular-file (directory)",
			setup:          func(t *testing.T, fp string) { makeDirAtArtefact(t, pathFor(fp)) },
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &notRegular,
		},
		{
			name:           "symlink (dangling, never opened)",
			setup:          func(t *testing.T, fp string) { makeDanglingSymlinkArtefact(t, pathFor(fp)) },
			wantApplicable: true,
			wantPresence:   PlanPresenceSymlink,
		},
	}
}

// ---------------------------------------------------------------------------
// Foundational: the shared ReadArtefactPath primitive.
// ---------------------------------------------------------------------------

func TestRebasePlanState_ReadArtefactPathPrimitive(t *testing.T) {
	t.Run("absent path is the pure zero value", func(t *testing.T) {
		dir := t.TempDir()
		out := ReadArtefactPath(filepath.Join(dir, "missing.yaml"), 1<<20)
		if out.Exists || out.IsSymlink || out.NotRegular || out.TooLarge || out.OK || out.Err != nil {
			t.Fatalf("absent path must be the zero value, got %+v", out)
		}
	})

	t.Run("readable regular file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.yaml")
		writeArtefact(t, path, "a: 1\n")
		out := ReadArtefactPath(path, 1<<20)
		if !out.Exists || !out.IsRegular || out.NotRegular || out.TooLarge || out.IsSymlink || !out.OK || out.Err != nil {
			t.Fatalf("readable regular file outcome wrong: %+v", out)
		}
		if out.Content != "a: 1\n" {
			t.Fatalf("Content = %q", out.Content)
		}
	})

	t.Run("dangling symlink is never opened", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "link.yaml")
		makeDanglingSymlinkArtefact(t, path)
		out := ReadArtefactPath(path, 1<<20)
		if !out.Exists || !out.IsSymlink || out.Err != nil || out.OK || out.Content != "" {
			t.Fatalf("dangling symlink must report a clean symlink outcome with no read attempt: %+v", out)
		}
	})

	t.Run("symlink to a real file is never followed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.yaml")
		writeArtefact(t, target, "a: 1\n")
		path := filepath.Join(dir, "link.yaml")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		out := ReadArtefactPath(path, 1<<20)
		if !out.IsSymlink || out.OK || out.Content != "" {
			t.Fatalf("a symlink to a real, readable file must still report IsSymlink and never surface the target's content: %+v", out)
		}
	})

	t.Run("not-regular directory", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "adir")
		makeDirAtArtefact(t, path)
		out := ReadArtefactPath(path, 1<<20)
		if !out.Exists || !out.NotRegular || out.OK || out.Err != nil {
			t.Fatalf("a directory must report NotRegular with no read attempt: %+v", out)
		}
	})

	t.Run("too large", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "big.yaml")
		writeArtefact(t, path, oversizedContent(300))
		out := ReadArtefactPath(path, 200)
		if !out.Exists || !out.IsRegular || !out.TooLarge || out.OK || out.Content != "" {
			t.Fatalf("an oversized file must report TooLarge with no content read: %+v", out)
		}
	})
}

// TestRebasePlanState_ExactlyOneReadPerArtefactPath is a structural proof of
// the §12.5 "exactly one read per artefact path" rule: every one of the five
// inspectXxxFile functions funnels through probeArtefact exactly once (never
// twice, never bypassing it with an independent os.ReadFile/os.Open/os.Lstat
// call), and probeArtefact itself calls the shared ReadArtefactPath read
// primitive exactly once.
func TestRebasePlanState_ExactlyOneReadPerArtefactPath(t *testing.T) {
	src := readSourceFile(t, "rebase_plan_state.go")

	if n := strings.Count(src, "ReadArtefactPath("); n != 1 {
		t.Errorf("rebase_plan_state.go calls ReadArtefactPath( %d times, want exactly 1 (inside probeArtefact); a second call site would mean a second read of some artefact path", n)
	}
	if n := strings.Count(src, "probeArtefact("); n != 6 { // 1 declaration + 5 call sites (one per artefact inspector)
		t.Errorf("rebase_plan_state.go contains %d occurrences of probeArtefact(, want exactly 6 (1 func decl + 5 call sites, one per artefact inspector)", n)
	}
	for _, forbidden := range []string{"os.ReadFile(", "os.Open(", "ioutil.ReadFile("} {
		if strings.Contains(src, forbidden) {
			t.Errorf("rebase_plan_state.go must not call %s directly; every artefact read must go through the shared probeArtefact/ReadArtefactPath primitive", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// CheckoutTransaction (checkout-only).
// ---------------------------------------------------------------------------

func inspectCheckoutTransactionViaInspector(t *testing.T, ws Workspace, fp string) PlanCheckoutTransactionFile {
	t.Helper()
	state, _ := InspectCheckoutPlanState(
		CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
		CheckoutPlanStateOpts{},
	)
	return state.Files.CheckoutTransaction
}

func TestRebasePlanState_CheckoutTransactionMatrix(t *testing.T) {
	cases := commonUnreadableCases[PlanCheckoutTransactionFile](CheckoutTransactionPath, stateArtefactReadCap)

	// Patch in the artefact-specific assertion for the shared decode-error
	// (invalid syntax) row: since the bare state_version rescue probe also
	// fails to parse this content, no version must be rescued.
	for i := range cases {
		if cases[i].name == "unreadable: decode-error (invalid YAML syntax)" {
			cases[i].extra = func(t *testing.T, file PlanCheckoutTransactionFile) {
				if file.Transaction != nil {
					t.Error("Transaction must stay nil on decode-error")
				}
				if file.StateVersion != nil {
					t.Errorf("no rescuable version exists in syntactically-invalid content, got %v", *file.StateVersion)
				}
			}
		}
	}

	decodeError := UnreadableDecodeError
	cases = append([]artefactCase[PlanCheckoutTransactionFile]{
		{
			name: "readable",
			setup: func(t *testing.T, fp string) {
				tx := &CheckoutTransaction{
					StateVersion:   2,
					Feature:        "feature-x",
					StartedAt:      "2026-01-01T00:00:00Z",
					LockPID:        12345,
					LockCreated:    "2026-01-01T00:00:00Z",
					Push:           true,
					OriginalBranch: "main",
					OriginalHEAD:   strings.Repeat("a", 40),
					Plan:           []CheckoutPlanEntry{{Name: "child", Branch: "child", Base: "main"}},
					CurrentIndex:   0,
					Stage:          StagePlanned,
				}
				if err := SaveCheckoutTransaction(fp, tx); err != nil {
					t.Fatal(err)
				}
			},
			wantApplicable: true,
			wantPresence:   PlanPresenceReadable,
			extra: func(t *testing.T, file PlanCheckoutTransactionFile) {
				if file.Transaction == nil {
					t.Fatal("Transaction must be non-nil when readable")
				}
				if file.Transaction.Feature != "feature-x" || file.Transaction.Stage != StagePlanned {
					t.Errorf("decoded transaction mismatch: %+v", file.Transaction)
				}
				if file.StateVersion != nil {
					t.Errorf("rescue StateVersion must stay nil once Transaction decoded cleanly, got %v", *file.StateVersion)
				}
			},
		},
	}, cases...)

	cases = append(cases,
		artefactCase[PlanCheckoutTransactionFile]{
			name: "unreadable: decode-error with a rescuable state_version",
			setup: func(t *testing.T, fp string) {
				writeArtefact(t, CheckoutTransactionPath(fp), "state_version: 7\nplan: \"not-a-list\"\n")
			},
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &decodeError,
			wantErrNonNil:  true,
			extra: func(t *testing.T, file PlanCheckoutTransactionFile) {
				if file.Transaction != nil {
					t.Error("Transaction must stay nil on decode-error")
				}
				if file.StateVersion == nil || *file.StateVersion != 7 {
					t.Fatalf("StateVersion rescue = %v, want 7", file.StateVersion)
				}
			},
		},
		artefactCase[PlanCheckoutTransactionFile]{
			name: "unreadable: decode-error with explicit state_version 0 (rescue excluded)",
			setup: func(t *testing.T, fp string) {
				writeArtefact(t, CheckoutTransactionPath(fp), "state_version: 0\nplan: \"not-a-list\"\n")
			},
			wantApplicable: true,
			wantPresence:   PlanPresenceUnreadable,
			wantReason:     &decodeError,
			wantErrNonNil:  true,
			extra: func(t *testing.T, file PlanCheckoutTransactionFile) {
				if file.StateVersion != nil {
					t.Errorf("an explicit zero version must never be rescued (StateVersion stays nil), got %v", *file.StateVersion)
				}
			},
		},
	)

	runArtefactCases(t, cases,
		func(t *testing.T) string {
			_, ws := rpsCheckoutFixture(t)
			return ws.FeaturePath("feature-x")
		},
		func(t *testing.T, fp string) PlanCheckoutTransactionFile {
			_, ws := rpsCheckoutFixture(t) // repo dir is irrelevant here; only FeaturePath is read for this artefact
			return inspectCheckoutTransactionViaInspector(t, ws, fp)
		},
		func(f PlanCheckoutTransactionFile) PlanFilePresence { return f.PlanFilePresence },
	)
}

// ---------------------------------------------------------------------------
// CheckoutLock (checkout-only).
// ---------------------------------------------------------------------------

func TestRebasePlanState_CheckoutLockMatrix(t *testing.T) {
	cases := commonUnreadableCases[PlanCheckoutLockFile](CheckoutLockPath, lockArtefactReadCap)
	cases = append([]artefactCase[PlanCheckoutLockFile]{
		{
			name: "readable",
			setup: func(t *testing.T, fp string) {
				writeLockLikeFile(t, CheckoutLockPath(fp), 424242, "2026-01-01T00:00:00Z")
			},
			wantApplicable: true,
			wantPresence:   PlanPresenceReadable,
			extra: func(t *testing.T, file PlanCheckoutLockFile) {
				if file.Lock == nil || file.Lock.PID != 424242 {
					t.Fatalf("decoded lock mismatch: %+v", file.Lock)
				}
				if file.Alive == nil || *file.Alive {
					t.Errorf("Alive = %v, want false (this matrix's fixed opts always report dead)", file.Alive)
				}
				if file.Self == nil || *file.Self {
					t.Errorf("Self = %v, want false (PID 424242 != fixed SelfPID 1)", file.Self)
				}
			},
		},
	}, cases...)

	runArtefactCases(t, cases,
		func(t *testing.T) string {
			_, ws := rpsCheckoutFixture(t)
			return ws.FeaturePath("feature-x")
		},
		func(t *testing.T, fp string) PlanCheckoutLockFile {
			_, ws := rpsCheckoutFixture(t)
			state, _ := InspectCheckoutPlanState(
				CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
				CheckoutPlanStateOpts{SelfPID: 1, Alive: func(int) bool { return false }},
			)
			return state.Files.CheckoutLock
		},
		func(f PlanCheckoutLockFile) PlanFilePresence { return f.PlanFilePresence },
	)
}

// ---------------------------------------------------------------------------
// LegacyState (external-only): state.files.external_legacy_state.
// ---------------------------------------------------------------------------

func rpsExternalClassified(fp string) SyncExternalState {
	return SyncExternalState{
		LegacyPath:  SyncStatePath(fp),
		PayloadPath: SyncRunStatePath(fp),
		GuardPath:   SyncRunGuardPath(fp),
	}
}

func TestRebasePlanState_LegacyStateMatrix(t *testing.T) {
	cases := commonUnreadableCases[PlanLegacySyncStateFile](SyncStatePath, stateArtefactReadCap)
	cases = append([]artefactCase[PlanLegacySyncStateFile]{
		{
			name:           "readable",
			setup:          func(t *testing.T, fp string) { writeRealLegacy(t, fp, "some-real-branch") },
			wantApplicable: true,
			wantPresence:   PlanPresenceReadable,
			extra: func(t *testing.T, file PlanLegacySyncStateFile) {
				if file.State == nil {
					t.Fatal("State must be non-nil when readable")
				}
				if file.MarkerPresent {
					t.Error("MarkerPresent must be false for a real (non-sentinel) failed_branch")
				}
				if file.Marker != "" {
					t.Errorf("Marker = %q, want empty", file.Marker)
				}
			},
		},
	}, cases...)

	runArtefactCases(t, cases,
		func(t *testing.T) string { return t.TempDir() },
		func(t *testing.T, fp string) PlanLegacySyncStateFile {
			state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
			return state.Files.LegacyState
		},
		func(f PlanLegacySyncStateFile) PlanFilePresence { return f.PlanFilePresence },
	)
}

func TestRebasePlanState_LegacyStateSentinelMarkerIsDetected(t *testing.T) {
	fp := t.TempDir()
	writeLegacySentinel(t, fp)
	state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
	file := state.Files.LegacyState
	if file.Presence != PlanPresenceReadable {
		t.Fatalf("Presence = %q, want readable", file.Presence)
	}
	if !file.MarkerPresent {
		t.Fatal("MarkerPresent must be true for a sentinel failed_branch")
	}
	if file.Marker != cellMarker {
		t.Errorf("Marker = %q, want %q", file.Marker, cellMarker)
	}
	if file.CompletedLen != 0 || file.PendingLen != 0 {
		t.Errorf("a freshly-written sentinel must have zero completed/pending, got completed=%d pending=%d", file.CompletedLen, file.PendingLen)
	}
}

// ---------------------------------------------------------------------------
// Payload (external-only): state.files.external_payload.
// ---------------------------------------------------------------------------

func TestRebasePlanState_PayloadMatrix(t *testing.T) {
	cases := commonUnreadableCases[PlanSyncRunPayloadFile](SyncRunStatePath, stateArtefactReadCap)
	cases = append([]artefactCase[PlanSyncRunPayloadFile]{
		{
			name:           "readable",
			setup:          func(t *testing.T, fp string) { writeValidPayload(t, fp, "", "tok-123") },
			wantApplicable: true,
			wantPresence:   PlanPresenceReadable,
			extra: func(t *testing.T, file PlanSyncRunPayloadFile) {
				if file.Payload == nil {
					t.Fatal("Payload must be non-nil when readable")
				}
				if !file.OwnerTokenPresent {
					t.Error("OwnerTokenPresent must be true for a non-empty owner_token")
				}
				if file.SelectedLen != 2 || file.CompletedLen != 1 || file.PushedLen != 0 {
					t.Errorf("derived lengths = selected=%d completed=%d pushed=%d, want 2/1/0", file.SelectedLen, file.CompletedLen, file.PushedLen)
				}
			},
		},
	}, cases...)

	runArtefactCases(t, cases,
		func(t *testing.T) string { return t.TempDir() },
		func(t *testing.T, fp string) PlanSyncRunPayloadFile {
			state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
			return state.Files.Payload
		},
		func(f PlanSyncRunPayloadFile) PlanFilePresence { return f.PlanFilePresence },
	)
}

// TestRebasePlanState_PayloadFutureVersionPublishesVerbatimNeverRejected
// documents the inspector's deliberate divergence from LoadSyncRunState: the
// inspector decodes the payload with its own direct yaml.Unmarshal rather
// than the version-gated loader, so a state_version the loader's hard gate
// would reject still publishes verbatim as readable instead of unreadable.
func TestRebasePlanState_PayloadFutureVersionPublishesVerbatimNeverRejected(t *testing.T) {
	fp := t.TempDir()
	writeArtefact(t, SyncRunStatePath(fp), "state_version: 77\nfeature: auth\n")

	state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
	if state.Files.Payload.Presence != PlanPresenceReadable {
		t.Fatalf("Presence = %q, want readable: a state_version LoadSyncRunState's hard gate would reject must still publish verbatim", state.Files.Payload.Presence)
	}
	if state.Files.Payload.Payload == nil || state.Files.Payload.Payload.StateVersion != 77 {
		t.Fatalf("Payload = %+v, want a decoded value with StateVersion=77", state.Files.Payload.Payload)
	}

	// Corroborate the contrast this test documents: the version-gated loader
	// rejects this exact same on-disk document.
	if _, err := LoadSyncRunState(fp); err == nil {
		t.Fatal("LoadSyncRunState must reject state_version 77 (sanity check for the contrast this test documents)")
	}
}

// ---------------------------------------------------------------------------
// RunGuard (external-only): state.files.external_run_guard.
// ---------------------------------------------------------------------------

func TestRebasePlanState_RunGuardMatrix(t *testing.T) {
	cases := commonUnreadableCases[PlanSyncRunGuardFile](SyncRunGuardPath, lockArtefactReadCap)
	cases = append([]artefactCase[PlanSyncRunGuardFile]{
		{
			name:           "readable",
			setup:          func(t *testing.T, fp string) { writeGuardFile(t, fp, 424242, "tok") },
			wantApplicable: true,
			wantPresence:   PlanPresenceReadable,
			extra: func(t *testing.T, file PlanSyncRunGuardFile) {
				if file.Guard == nil || file.Guard.PID != 424242 || file.Guard.Token != "tok" {
					t.Fatalf("decoded guard mismatch: %+v", file.Guard)
				}
				if file.Alive == nil || *file.Alive {
					t.Errorf("Alive = %v, want false (this matrix's fixed opts always report dead)", file.Alive)
				}
				if file.Self == nil || *file.Self {
					t.Errorf("Self = %v, want false (PID 424242 != fixed SelfPID 1)", file.Self)
				}
				if file.TokenMatchesPayload != nil {
					t.Errorf("TokenMatchesPayload = %v, want nil: no payload is present in this isolated matrix", file.TokenMatchesPayload)
				}
			},
		},
	}, cases...)

	runArtefactCases(t, cases,
		func(t *testing.T) string { return t.TempDir() },
		func(t *testing.T, fp string) PlanSyncRunGuardFile {
			state := InspectExternalPlanState(fp, ExternalPlanStateOpts{
				Classified: rpsExternalClassified(fp),
				SelfPID:    1,
				Alive:      func(int) bool { return false },
			})
			return state.Files.RunGuard
		},
		func(f PlanSyncRunGuardFile) PlanFilePresence { return f.PlanFilePresence },
	)
}

// ---------------------------------------------------------------------------
// Alive/Self liveness logic, shared by CheckoutLock and RunGuard.
// ---------------------------------------------------------------------------

func TestRebasePlanState_LockAndGuardAliveSelfLogic(t *testing.T) {
	t.Run("checkout lock: self-held PID is never Alive, always Self", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 555, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 555, Alive: func(int) bool { return true }},
		)
		f := state.Files.CheckoutLock
		if f.Self == nil || !*f.Self {
			t.Error("Self must be true when lock.PID == SelfPID")
		}
		if f.Alive == nil || *f.Alive {
			t.Error("Alive must be false when lock.PID == SelfPID, regardless of what the Alive func would say")
		}
	})

	t.Run("checkout lock: live foreign PID is Alive, never Self", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 424242, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 1, Alive: func(pid int) bool { return pid == 424242 }},
		)
		f := state.Files.CheckoutLock
		if f.Alive == nil || !*f.Alive {
			t.Error("Alive must be true for a live, non-self PID")
		}
		if f.Self == nil || *f.Self {
			t.Error("Self must be false for a foreign PID")
		}
	})

	t.Run("checkout lock: dead foreign PID is neither Alive nor Self", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 424242, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 1, Alive: func(int) bool { return false }},
		)
		f := state.Files.CheckoutLock
		if f.Alive == nil || *f.Alive {
			t.Error("Alive must be false for a dead foreign PID")
		}
		if f.Self == nil || *f.Self {
			t.Error("Self must be false for a foreign PID")
		}
	})

	t.Run("checkout lock: PID 0 is neither Alive nor Self", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 0, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 1, Alive: func(int) bool { return true }},
		)
		f := state.Files.CheckoutLock
		if f.Alive == nil || *f.Alive {
			t.Error("Alive must be false for PID 0, even if the Alive func would say true (PID > 0 is required)")
		}
		if f.Self == nil || *f.Self {
			t.Error("Self must be false for PID 0 when SelfPID is 1")
		}
	})

	t.Run("run guard: self-held PID is never Alive, always Self", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 555, "tok")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{
			Classified: rpsExternalClassified(fp), SelfPID: 555, Alive: func(int) bool { return true },
		})
		f := state.Files.RunGuard
		if f.Self == nil || !*f.Self {
			t.Error("Self must be true when guard.PID == SelfPID")
		}
		if f.Alive == nil || *f.Alive {
			t.Error("Alive must be false when guard.PID == SelfPID")
		}
	})

	t.Run("run guard: live foreign PID is Alive, never Self", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 424242, "tok")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{
			Classified: rpsExternalClassified(fp), SelfPID: 1, Alive: func(pid int) bool { return pid == 424242 },
		})
		f := state.Files.RunGuard
		if f.Alive == nil || !*f.Alive {
			t.Error("Alive must be true for a live, non-self PID")
		}
		if f.Self == nil || *f.Self {
			t.Error("Self must be false for a foreign PID")
		}
	})
}

func TestRebasePlanState_RunGuardTokenMatchesPayload(t *testing.T) {
	t.Run("nil when no payload is present", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 424242, "tok")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp), Alive: func(int) bool { return false }})
		if state.Files.RunGuard.TokenMatchesPayload != nil {
			t.Errorf("TokenMatchesPayload = %v, want nil with no payload present", *state.Files.RunGuard.TokenMatchesPayload)
		}
	})

	t.Run("nil when the payload's owner_token is empty", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 424242, "tok")
		writeValidPayload(t, fp, "", "")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp), Alive: func(int) bool { return false }})
		if state.Files.RunGuard.TokenMatchesPayload != nil {
			t.Errorf("TokenMatchesPayload = %v, want nil with an empty payload owner_token", *state.Files.RunGuard.TokenMatchesPayload)
		}
	})

	t.Run("true when guard token equals the payload owner_token", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 424242, "shared-token")
		writeValidPayload(t, fp, "", "shared-token")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp), Alive: func(int) bool { return false }})
		if state.Files.RunGuard.TokenMatchesPayload == nil || !*state.Files.RunGuard.TokenMatchesPayload {
			t.Error("TokenMatchesPayload must be true when the tokens are equal")
		}
	})

	t.Run("false when guard token differs from the payload owner_token", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 424242, "guard-token")
		writeValidPayload(t, fp, "", "payload-token")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp), Alive: func(int) bool { return false }})
		if state.Files.RunGuard.TokenMatchesPayload == nil || *state.Files.RunGuard.TokenMatchesPayload {
			t.Error("TokenMatchesPayload must be false when the tokens differ")
		}
	})
}

// ---------------------------------------------------------------------------
// Not-applicable rows: each inspector must never touch the other route's
// artefact paths, even when they exist and are poisoned.
// ---------------------------------------------------------------------------

func TestRebasePlanState_CheckoutRouteNeverTouchesExternalArtefactPaths(t *testing.T) {
	_, ws := rpsCheckoutFixture(t)
	fp := ws.FeaturePath("feature-x")

	// Poison the three external-only paths with directories: any accidental
	// read attempt on the checkout route would surface as an unexpected
	// Presence/Err instead of the hardcoded not-applicable header.
	makeDirAtArtefact(t, SyncStatePath(fp))
	makeDirAtArtefact(t, SyncRunStatePath(fp))
	makeDirAtArtefact(t, SyncRunGuardPath(fp))

	state, _ := InspectCheckoutPlanState(CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot}, CheckoutPlanStateOpts{})

	rows := []struct {
		label string
		got   PlanFilePresence
	}{
		{"LegacyState", state.Files.LegacyState.PlanFilePresence},
		{"Payload", state.Files.Payload.PlanFilePresence},
		{"RunGuard", state.Files.RunGuard.PlanFilePresence},
	}
	for _, r := range rows {
		if r.got.Applicable {
			t.Errorf("%s.Applicable = true, want false on the checkout route", r.label)
		}
		if r.got.Presence != PlanPresenceNotApplicable {
			t.Errorf("%s.Presence = %q, want not-applicable on the checkout route", r.label, r.got.Presence)
		}
		if r.got.Err != nil {
			t.Errorf("%s.Err = %v, want nil: a not-applicable row must never have touched the poisoned path", r.label, r.got.Err)
		}
	}
}

func TestRebasePlanState_ExternalRouteNeverTouchesCheckoutArtefactPaths(t *testing.T) {
	fp := t.TempDir()

	// Poison the two checkout-only paths (under their state/ sibling
	// directory) with directories.
	makeDirAtArtefact(t, CheckoutTransactionPath(fp))
	makeDirAtArtefact(t, CheckoutLockPath(fp))

	state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})

	rows := []struct {
		label string
		got   PlanFilePresence
	}{
		{"CheckoutTransaction", state.Files.CheckoutTransaction.PlanFilePresence},
		{"CheckoutLock", state.Files.CheckoutLock.PlanFilePresence},
	}
	for _, r := range rows {
		if r.got.Applicable {
			t.Errorf("%s.Applicable = true, want false on the external route", r.label)
		}
		if r.got.Presence != PlanPresenceNotApplicable {
			t.Errorf("%s.Presence = %q, want not-applicable on the external route", r.label, r.got.Presence)
		}
		if r.got.Err != nil {
			t.Errorf("%s.Err = %v, want nil: a not-applicable row must never have touched the poisoned path", r.label, r.got.Err)
		}
	}
}

// ---------------------------------------------------------------------------
// Verdict identity: CheckoutPlanStateVerdict must mirror Files.* verbatim.
// ---------------------------------------------------------------------------

func TestRebasePlanState_VerdictMirrorsFilesFieldsVerbatim(t *testing.T) {
	_, ws := rpsCheckoutFixture(t)
	fp := ws.FeaturePath("feature-x")

	// Symlinked transaction: TransactionPresent must be true (Presence !=
	// absent) even though the artefact is unreadable-by-design, and
	// TransactionErr must stay nil (no Err is attached to a symlink outcome).
	makeDanglingSymlinkArtefact(t, CheckoutTransactionPath(fp))
	// Oversized lock: LockErr must be the exact same error value recorded on
	// Files.CheckoutLock.Err, never re-derived or re-read.
	writeArtefact(t, CheckoutLockPath(fp), oversizedContent(lockArtefactReadCap))

	state, verdict := InspectCheckoutPlanState(CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot}, CheckoutPlanStateOpts{})

	if !verdict.TransactionPresent {
		t.Error("TransactionPresent must be true for a symlinked (non-absent) transaction artefact")
	}
	if verdict.TransactionErr != nil {
		t.Errorf("TransactionErr = %v, want nil for a symlink outcome", verdict.TransactionErr)
	}
	if verdict.LockErr == nil {
		t.Fatal("LockErr must be non-nil for an oversized lock artefact")
	}
	if verdict.LockErr != state.Files.CheckoutLock.Err {
		t.Error("verdict.LockErr must be the exact same error value as Files.CheckoutLock.Err, never re-derived")
	}
	if verdict.LockPresence != state.Files.CheckoutLock.Presence {
		t.Errorf("LockPresence = %q, want it to echo Files.CheckoutLock.Presence = %q", verdict.LockPresence, state.Files.CheckoutLock.Presence)
	}
	if verdict.LockPresence != PlanPresenceUnreadable {
		t.Errorf("LockPresence = %q, want unreadable", verdict.LockPresence)
	}

	// A fresh feature: the transaction artefact is genuinely absent, so
	// TransactionPresent must be false (the only case it is).
	fp2 := ws.FeaturePath("feature-fresh")
	state2, verdict2 := InspectCheckoutPlanState(CheckoutSyncOpts{FeaturePath: fp2, RepoDir: ws.RepoRoot}, CheckoutPlanStateOpts{})
	if verdict2.TransactionPresent {
		t.Error("TransactionPresent must be false when the transaction artefact is absent")
	}
	if state2.Files.CheckoutTransaction.Presence != PlanPresenceAbsent {
		t.Fatalf("expected absent, got %q", state2.Files.CheckoutTransaction.Presence)
	}
	if verdict2.TransactionErr != nil {
		t.Errorf("TransactionErr = %v, want nil for an absent artefact", verdict2.TransactionErr)
	}
}

// ---------------------------------------------------------------------------
// Classifier reuse: a non-nil Classified.{Legacy,Payload,Guard} value is used
// verbatim, and the inspector's own decode is skipped entirely — proved by
// pairing garbage on-disk bytes (which fail an independent decode, per the
// negative control) with a classified value that decodes cleanly regardless.
// ---------------------------------------------------------------------------

func TestRebasePlanState_ClassifierValueReusedSkipsRedundantDecode(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		fp := t.TempDir()
		writeArtefact(t, SyncStatePath(fp), "pending: [oops\n\t- broken\n")
		classifiedLegacy := &SyncState{FailedBranch: "real-branch", Pending: []string{"a"}, Completed: []string{"b", "c"}}
		withClassified := rpsExternalClassified(fp)
		withClassified.Legacy = classifiedLegacy

		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: withClassified})
		if state.Files.LegacyState.Presence != PlanPresenceReadable {
			t.Fatalf("Presence = %q, want readable: the classified value must be used instead of failing to decode the on-disk garbage", state.Files.LegacyState.Presence)
		}
		if state.Files.LegacyState.State != classifiedLegacy {
			t.Error("State must be the exact classified pointer, not a freshly (and here, unsuccessfully) re-decoded value")
		}
		if state.Files.LegacyState.PendingLen != 1 || state.Files.LegacyState.CompletedLen != 2 {
			t.Errorf("derived lengths must come from the classified value: pending=%d completed=%d", state.Files.LegacyState.PendingLen, state.Files.LegacyState.CompletedLen)
		}

		// Negative control: identical on-disk bytes, no classified value.
		stateNil := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
		if stateNil.Files.LegacyState.Presence != PlanPresenceUnreadable || stateNil.Files.LegacyState.UnreadableReason == nil || *stateNil.Files.LegacyState.UnreadableReason != UnreadableDecodeError {
			t.Fatalf("negative control: expected decode-error without a classified value, got %+v", stateNil.Files.LegacyState.PlanFilePresence)
		}
	})

	t.Run("payload", func(t *testing.T) {
		fp := t.TempDir()
		writeArtefact(t, SyncRunStatePath(fp), "selected: [oops\n\t- broken\n")
		classifiedPayload := &SyncRunState{StateVersion: 2, Feature: "auth", OwnerToken: "tok", Selected: []string{"a", "b"}, Completed: []string{"a"}}
		withClassified := rpsExternalClassified(fp)
		withClassified.Payload = classifiedPayload

		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: withClassified})
		if state.Files.Payload.Presence != PlanPresenceReadable {
			t.Fatalf("Presence = %q, want readable", state.Files.Payload.Presence)
		}
		if state.Files.Payload.Payload != classifiedPayload {
			t.Error("Payload must be the exact classified pointer")
		}
		if !state.Files.Payload.OwnerTokenPresent || state.Files.Payload.SelectedLen != 2 || state.Files.Payload.CompletedLen != 1 {
			t.Errorf("derived fields must come from the classified value: %+v", state.Files.Payload)
		}

		stateNil := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
		if stateNil.Files.Payload.Presence != PlanPresenceUnreadable || stateNil.Files.Payload.UnreadableReason == nil || *stateNil.Files.Payload.UnreadableReason != UnreadableDecodeError {
			t.Fatalf("negative control: expected decode-error without a classified value, got %+v", stateNil.Files.Payload.PlanFilePresence)
		}
	})

	t.Run("guard", func(t *testing.T) {
		fp := t.TempDir()
		writeArtefact(t, SyncRunGuardPath(fp), "token: [oops\n\t- broken\n")
		classifiedGuard := &SyncRunGuard{PID: 999, Created: "2026-01-01T00:00:00Z", Token: "tok", StateVersion: 2}
		withClassified := rpsExternalClassified(fp)
		withClassified.Guard = classifiedGuard

		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: withClassified, SelfPID: 1, Alive: func(int) bool { return false }})
		if state.Files.RunGuard.Presence != PlanPresenceReadable {
			t.Fatalf("Presence = %q, want readable", state.Files.RunGuard.Presence)
		}
		if state.Files.RunGuard.Guard != classifiedGuard {
			t.Error("Guard must be the exact classified pointer")
		}

		stateNil := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp), SelfPID: 1, Alive: func(int) bool { return false }})
		if stateNil.Files.RunGuard.Presence != PlanPresenceUnreadable || stateNil.Files.RunGuard.UnreadableReason == nil || *stateNil.Files.RunGuard.UnreadableReason != UnreadableDecodeError {
			t.Fatalf("negative control: expected decode-error without a classified value, got %+v", stateNil.Files.RunGuard.PlanFilePresence)
		}
	})
}

// ---------------------------------------------------------------------------
// LiveForeignLock / LiveForeignOwner: the §11.2 cause-3 / §7.1 rank-3
// convenience predicates.
// ---------------------------------------------------------------------------

func TestRebasePlanState_LiveForeignLockAndLiveForeignOwner(t *testing.T) {
	t.Run("checkout: live foreign lock", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 424242, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 1, Alive: func(pid int) bool { return pid == 424242 }},
		)
		if !state.LiveForeignLock() {
			t.Error("a live, non-self lock PID must report LiveForeignLock() == true")
		}
	})

	t.Run("checkout: self-held lock is never foreign", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 555, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 555, Alive: func(int) bool { return true }},
		)
		if state.LiveForeignLock() {
			t.Error("a self-held lock must never report LiveForeignLock() == true")
		}
	})

	t.Run("checkout: dead foreign lock is not live", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-x")
		writeLockLikeFile(t, CheckoutLockPath(fp), 424242, "2026-01-01T00:00:00Z")
		state, _ := InspectCheckoutPlanState(
			CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot},
			CheckoutPlanStateOpts{SelfPID: 1, Alive: func(int) bool { return false }},
		)
		if state.LiveForeignLock() {
			t.Error("a dead foreign PID must never report LiveForeignLock() == true")
		}
	})

	t.Run("checkout: absent lock is not live", func(t *testing.T) {
		_, ws := rpsCheckoutFixture(t)
		fp := ws.FeaturePath("feature-fresh")
		state, _ := InspectCheckoutPlanState(CheckoutSyncOpts{FeaturePath: fp, RepoDir: ws.RepoRoot}, CheckoutPlanStateOpts{})
		if state.LiveForeignLock() {
			t.Error("an absent lock must never report LiveForeignLock() == true")
		}
	})

	t.Run("external: live foreign owner", func(t *testing.T) {
		fp := t.TempDir()
		writeGuardFile(t, fp, 424242, "tok")
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{
			Classified: rpsExternalClassified(fp), SelfPID: 1, Alive: func(pid int) bool { return pid == 424242 },
		})
		if !state.LiveForeignOwner() {
			t.Error("a live, non-self run-guard PID must report LiveForeignOwner() == true")
		}
	})

	t.Run("external: absent guard is not live", func(t *testing.T) {
		fp := t.TempDir()
		state := InspectExternalPlanState(fp, ExternalPlanStateOpts{Classified: rpsExternalClassified(fp)})
		if state.LiveForeignOwner() {
			t.Error("an absent run guard must never report LiveForeignOwner() == true")
		}
	})
}
