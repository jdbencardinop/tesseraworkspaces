package internal

import (
	"encoding/binary"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Matrix ownership (§17.2).
//
//	T-034 fingerprint sensitivity and stability over 33 fields .... AC-047
//	T-035 cross-domain token rejection, both directions ........... AC-048
// ---------------------------------------------------------------------------

var reparentFingerprintShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

func reparentFingerprintOf(t *testing.T, plan ReparentPlan) string {
	t.Helper()
	fp, err := ReparentPlanFingerprint(plan)
	if err != nil {
		t.Fatalf("ReparentPlanFingerprint: %v", err)
	}
	if !reparentFingerprintShape.MatchString(fp) {
		t.Fatalf("fingerprint %q is not 64 lowercase hex characters", fp)
	}
	return fp
}

func TestReparentPlanFingerprint_ShapeAndStability(t *testing.T) {
	plan := reparentSamplePlan()
	first := reparentFingerprintOf(t, plan)
	for i := 0; i < 5; i++ {
		if again := reparentFingerprintOf(t, reparentSamplePlan()); again != first {
			t.Fatalf("run %d produced %s, want the stable %s", i, again, first)
		}
	}
}

// TestReparentPlanFingerprint_OwnDomain proves the sync approval domain is
// untouched: a different prefix and a different tuple schema mean no sync
// token is ever accepted here and no reparent token is ever accepted there.
func TestReparentPlanFingerprint_OwnDomain(t *testing.T) {
	_ = "asserts AC-048"
	preimage, err := ReparentPlanFingerprintPreimage(reparentSamplePlan())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(preimage), reparentFingerprintPrefix) {
		t.Fatalf("preimage does not start with the reparent prefix: %q", preimage[:32])
	}
	if strings.HasPrefix(string(preimage), planFingerprintPrefix) {
		t.Fatal("the reparent preimage must never carry the sync prefix")
	}
	if planFingerprintPrefix != "tws-plan-fp\x00" || planFingerprintTupleSchemaVersion != 0x0004 {
		t.Fatal("the sync fingerprint domain must not change with this feature")
	}

	offset := len(reparentFingerprintPrefix)
	if got := binary.BigEndian.Uint16(preimage[offset : offset+2]); got != reparentFingerprintEncodingVersion {
		t.Fatalf("encoding version = %d", got)
	}
	if got := binary.BigEndian.Uint16(preimage[offset+2 : offset+4]); got != reparentFingerprintTupleSchemaVersion {
		t.Fatalf("tuple schema version = %d", got)
	}
}

func TestReparentCrossDomainApprovalAdmissionRejectsBothDirections(t *testing.T) {
	_ = "asserts AC-048"
	syncPlan := fingerprintFixturePlan()
	syncFingerprint, err := PlanFingerprint(syncPlan)
	if err != nil {
		t.Fatal(err)
	}
	reparentPlan := reparentSamplePlan()
	reparentFingerprint := reparentFingerprintOf(t, reparentPlan)
	reparentPlan.Approval.Fingerprint = &reparentFingerprint
	limit := 10

	err = AdmitFreshReparent(reparentPlan, CheckoutPlanGuard{
		Approve:  syncFingerprint,
		MaxTotal: &limit,
	})
	var refusal *PlanGuardRefusalError
	if !errors.As(err, &refusal) || refusal.Kind != string(RefusalApprovalMismatch) {
		t.Fatalf("sync token admitted by reparent = %v", err)
	}
	if err := AdmitFreshReparent(reparentPlan, CheckoutPlanGuard{
		Approve:  reparentFingerprint,
		MaxTotal: &limit,
	}); err != nil {
		t.Fatalf("reparent rejected its own token: %v", err)
	}

	if blocker := approvalMismatchBlocker(true, &syncFingerprint, reparentFingerprint); blocker == nil ||
		blocker.Kind != RefusalApprovalMismatch {
		t.Fatalf("reparent token admitted by sync = %+v", blocker)
	}
	if blocker := approvalMismatchBlocker(true, &syncFingerprint, syncFingerprint); blocker != nil {
		t.Fatalf("sync rejected its own token: %+v", blocker)
	}
	assertReparentMatrixBehavior(t, "T-035",
		"cross-domain-admission-both-directions",
	)
}

// reparentTLVMembers parses one TLV struct payload into its member ids, in
// order, so the 33-field table can be asserted against the actual bytes.
func reparentTLVMembers(t *testing.T, payload []byte) []uint16 {
	t.Helper()
	var ids []uint16
	for len(payload) > 0 {
		if len(payload) < 7 {
			t.Fatalf("truncated TLV member: %d bytes left", len(payload))
		}
		id := binary.BigEndian.Uint16(payload[0:2])
		length := binary.BigEndian.Uint32(payload[3:7])
		if uint32(len(payload)) < 7+length {
			t.Fatalf("TLV member %d claims %d bytes, only %d left", id, length, len(payload)-7)
		}
		ids = append(ids, id)
		payload = payload[7+length:]
	}
	return ids
}

func TestReparentPlanFingerprint_BindsExactlyThirtyThreeFieldsInOrder(t *testing.T) {
	preimage, err := ReparentPlanFingerprintPreimage(reparentSamplePlan())
	if err != nil {
		t.Fatal(err)
	}
	// prefix + two uint16 versions + root element header (tag + 4-byte length)
	header := len(reparentFingerprintPrefix) + 4
	rootLen := binary.BigEndian.Uint32(preimage[header+1 : header+5])
	root := preimage[header+5:]
	if uint32(len(root)) != rootLen {
		t.Fatalf("root struct is %d bytes, framed as %d", len(root), rootLen)
	}

	ids := reparentTLVMembers(t, root)
	if len(ids) != ReparentFingerprintFieldCount {
		t.Fatalf("tuple binds %d fields, want exactly %d", len(ids), ReparentFingerprintFieldCount)
	}
	for i, id := range ids {
		if int(id) != i+1 {
			t.Fatalf("field %d has id %d, want %d (ascending, never renumbered)", i, id, i+1)
		}
	}
}

// TestReparentPlanFingerprint_Sensitivity is the load-bearing table: any
// change to the destination, a cutoff, a pre-image tip, the closure order or
// a known metadata cell MUST change the token.
func TestReparentPlanFingerprint_Sensitivity(t *testing.T) {
	_ = "asserts AC-047"
	assertReparentMatrixBehavior(t, "T-034", "fingerprint-sensitivity")
	base := reparentFingerprintOf(t, reparentSamplePlan())

	cases := []struct {
		name   string
		mutate func(*ReparentPlan)
	}{
		{"feature", func(p *ReparentPlan) { p.Feature = "other" }},
		{"route", func(p *ReparentPlan) { p.Route = ReparentRouteContinue }},
		{"workspace mode", func(p *ReparentPlan) { p.Workspace.Mode = "external" }},
		{"workspace stable id", func(p *ReparentPlan) { p.Workspace.StableID = reparentPlanFixtureString("другой") }},
		{"target name", func(p *ReparentPlan) { p.Target.Name = "renamed" }},
		{"target git branch", func(p *ReparentPlan) { p.Target.GitBranch = "feature/renamed" }},
		{"target repo", func(p *ReparentPlan) { p.Target.Repo = "/other" }},
		{"target context id", func(p *ReparentPlan) {
			p.Target.ExecutionContext.ContextID = reparentPlanFixtureString(strings.Repeat("9", 64))
		}},
		{"target canonical repo root", func(p *ReparentPlan) {
			p.Target.ExecutionContext.RepoRoot = reparentPlanFixtureString("/other/repo")
		}},
		{"old parent stored token", func(p *ReparentPlan) { p.Target.OldParent.StoredToken = reparentPlanFixtureString("pr0") }},
		{"new parent stored token", func(p *ReparentPlan) {
			p.Target.NewParent.StoredToken = reparentPlanFixtureString("refs/heads/other")
		}},
		{"new parent pinned sha", func(p *ReparentPlan) {
			p.Target.NewParent.SHA = reparentPlanFixtureString(strings.Repeat("9", 40))
		}},
		{"onto kind requested", func(p *ReparentPlan) { p.Policy.OntoKindRequested = "entry" }},
		{"oid width", func(p *ReparentPlan) { p.Policy.OIDWidth = reparentPlanFixtureInt(64) }},
		{"requested destination token", func(p *ReparentPlan) {
			p.Target.NewParent.RequestedToken = reparentPlanFixtureString(" refs/heads/master ")
		}},
		{"supplied cutoff token", func(p *ReparentPlan) {
			p.Target.Cutoff.SuppliedToken = reparentPlanFixtureString(" refs/heads/pr1 ")
		}},
		{"target cutoff sha", func(p *ReparentPlan) {
			p.Target.Cutoff.ResolvedSHA = reparentPlanFixtureString(strings.Repeat("8", 40))
		}},
		{"target cutoff provenance", func(p *ReparentPlan) { p.Target.Cutoff.Provenance = ReparentCutoffOperatorSupplied }},
		{"closure order", func(p *ReparentPlan) {
			extra := p.Descendants[0]
			extra.Name = "pr4"
			extra.GitBranch = "feature/pr4"
			extra.Order = 2
			p.Descendants = append(p.Descendants, extra)
		}},
		{"row order index", func(p *ReparentPlan) { p.Descendants[0].Order = 7 }},
		{"row execution context id", func(p *ReparentPlan) {
			p.Descendants[0].ExecutionContext.ContextID = reparentPlanFixtureString(strings.Repeat("8", 64))
		}},
		{"row execution context root", func(p *ReparentPlan) {
			p.Descendants[0].ExecutionContext.RepoRoot = reparentPlanFixtureString("/other/row")
		}},
		{"row execution context source", func(p *ReparentPlan) {
			p.Descendants[0].ExecutionContext.Source = "entry-repo"
		}},
		{"row pre-image head", func(p *ReparentPlan) {
			p.Descendants[0].Head.SHA = reparentPlanFixtureString(strings.Repeat("7", 40))
		}},
		{"row cutoff", func(p *ReparentPlan) {
			p.Descendants[0].Cutoff.ResolvedSHA = reparentPlanFixtureString(strings.Repeat("6", 40))
		}},
		{"row destination binding", func(p *ReparentPlan) { p.Descendants[0].DestinationBinding = ReparentBindingPinned }},
		{"row destination parent", func(p *ReparentPlan) {
			p.Descendants[0].DestinationParent = reparentPlanFixtureString("pr9")
		}},
		{"deferred destination becomes known", func(p *ReparentPlan) {
			p.Descendants[0].DestinationSHA = reparentPlanFixtureString(strings.Repeat("5", 40))
		}},
		{"argv template", func(p *ReparentPlan) { p.Target.Argv = append(p.Target.Argv, "--extra") }},
		{"candidate digest", func(p *ReparentPlan) {
			p.Target.Replay.CandidateDigest = reparentPlanFixtureString("changed")
		}},
		{"candidate count", func(p *ReparentPlan) { p.Target.Replay.CandidateCount = reparentPlanFixtureInt(9) }},
		{"metadata base after", func(p *ReparentPlan) { p.MetadataDelta.Entries[0].BaseAfter = "refs/heads/other" }},
		{"metadata last base sha after", func(p *ReparentPlan) {
			p.MetadataDelta.Entries[1].LastBaseSHAAfter = reparentPlanFixtureString(strings.Repeat("4", 40))
		}},
		{"stack sha before", func(p *ReparentPlan) { p.MetadataDelta.StackSHA256Before = strings.Repeat("3", 64) }},
		{"fetch policy", func(p *ReparentPlan) { p.Policy.Fetch = "fetch" }},
		{"guard per-entry limit", func(p *ReparentPlan) {
			p.Guard.Limits.MaxReplayPerEntry.Value = reparentPlanFixtureInt(99)
		}},
		{"guard total limit", func(p *ReparentPlan) { p.Guard.Limits.MaxReplayTotal.Value = nil }},
		{"validation digest", func(p *ReparentPlan) {
			p.Policy.Validation.CommandDigest = reparentPlanFixtureString("other")
		}},
		{"strategy context", func(p *ReparentPlan) { p.Strategy.ComputationContext = ReparentComputationExternal }},
		{"ref backend", func(p *ReparentPlan) { p.Strategy.Atomicity.RefBackend = ReparentRefBackendReftable }},
		{"original checkout branch", func(p *ReparentPlan) {
			p.Holders.OriginalBranch = reparentPlanFixtureString("feature/other")
		}},
		{"original checkout head", func(p *ReparentPlan) {
			p.Holders.OriginalHead = reparentPlanFixtureString(strings.Repeat("6", 40))
		}},
		{"original checkout detached", func(p *ReparentPlan) { p.Holders.OriginalDetached = true }},
		{"holder path", func(p *ReparentPlan) {
			p.Holders.Rows[0].HolderPath = reparentPlanFixtureString("/other/holder")
		}},
		{"holder branch", func(p *ReparentPlan) { p.Holders.Rows[0].GitBranch = "feature/other" }},
		{"holder head", func(p *ReparentPlan) {
			p.Holders.Rows[0].PreimageSHA = reparentPlanFixtureString(strings.Repeat("5", 40))
		}},
		{"holder action", func(p *ReparentPlan) { p.Holders.Rows[0].Action = "already-detached" }},
		{"approval scope", func(p *ReparentPlan) { p.Approval.Scope = ReparentApprovalScopeResume }},
	}

	seen := map[string]string{base: "unmodified"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := reparentSamplePlan()
			tc.mutate(&plan)
			got := reparentFingerprintOf(t, plan)
			if got == base {
				t.Fatalf("%s did not change the fingerprint", tc.name)
			}
			if other, clash := seen[got]; clash {
				t.Fatalf("%s collides with %s", tc.name, other)
			}
			seen[got] = tc.name
		})
	}
}

// TestReparentPlanFingerprint_NeverBindsPerRunValues proves values that either
// do not exist at admission, embed a per-run path, or are output-only prose
// are not tuple members.
func TestReparentPlanFingerprint_NeverBindsPerRunValues(t *testing.T) {
	base := reparentFingerprintOf(t, reparentSamplePlan())

	cases := []struct {
		name   string
		mutate func(*ReparentPlan)
	}{
		{"run id", func(p *ReparentPlan) { p.Strategy.RunID = reparentPlanFixtureString("run-123") }},
		{"computation path", func(p *ReparentPlan) {
			p.Strategy.ComputationPath = reparentPlanFixtureString("/tmp-less/path/.reparent/run-123/scratch")
		}},
		{"expected post-image hash", func(p *ReparentPlan) {
			p.MetadataDelta.StackSHA256AfterExpected = reparentPlanFixtureString(strings.Repeat("2", 64))
		}},
		{"invocation", func(p *ReparentPlan) { p.Invocation = ReparentInvocationExecute }},
		{"warnings", func(p *ReparentPlan) {
			p.Warnings = []ReparentPlanWarning{{Kind: ReparentWarnUntrackedPresent, Detail: "noise"}}
		}},
		{"notes", func(p *ReparentPlan) { p.Target.Notes = []string{"a note"} }},
		{"freshness", func(p *ReparentPlan) { p.Freshness = "stale" }},
		{"remote guidance", func(p *ReparentPlan) { p.Remote.Guidance = []string{"different prose"} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := reparentSamplePlan()
			tc.mutate(&plan)
			if got := reparentFingerprintOf(t, plan); got != base {
				t.Fatalf("%s changed the fingerprint; a plan and its execution could never agree", tc.name)
			}
		})
	}
}

// TestReparentPlanFingerprint_NullIsNotEmpty proves the explicit-absence rule:
// a deferred cell and an empty-string cell are distinct TLV types and must
// never hash alike.
func TestReparentPlanFingerprint_NullIsNotEmpty(t *testing.T) {
	deferred := reparentSamplePlan()
	deferred.Descendants[0].DestinationSHA = nil

	empty := reparentSamplePlan()
	empty.Descendants[0].DestinationSHA = reparentPlanFixtureString("")

	if reparentFingerprintOf(t, deferred) == reparentFingerprintOf(t, empty) {
		t.Fatal("an explicit NULL must not hash like an empty string")
	}
}

func TestReparentPlanFingerprint_HolderOrderIsCanonical(t *testing.T) {
	plan := reparentSamplePlan()
	plan.Holders.Rows = append(plan.Holders.Rows, ReparentPlanHolder{
		GitBranch: "feature/pr3", HolderKind: "linked-worktree",
		HolderPath:  reparentPlanFixtureString("/repo/worktrees/pr3"),
		PreimageSHA: reparentPlanFixtureString(strings.Repeat("b", 40)),
		Action:      "detach-and-restore", Safe: true,
	})
	first := reparentFingerprintOf(t, plan)
	plan.Holders.Rows[0], plan.Holders.Rows[1] = plan.Holders.Rows[1], plan.Holders.Rows[0]
	if second := reparentFingerprintOf(t, plan); second != first {
		t.Fatalf("holder inventory order changed fingerprint: %s != %s", second, first)
	}
}

// ---------------------------------------------------------------------------
// ReparentRevalidationDigest
// ---------------------------------------------------------------------------

func reparentDigestOf(t *testing.T, row ReparentPlanRow) string {
	t.Helper()
	digest, err := ReparentRevalidationDigest(row)
	if err != nil {
		t.Fatalf("ReparentRevalidationDigest: %v", err)
	}
	if !reparentFingerprintShape.MatchString(digest) {
		t.Fatalf("digest %q is not 64 lowercase hex characters", digest)
	}
	return digest
}

func TestReparentRevalidationDigest_StabilityAndSensitivity(t *testing.T) {
	plan := reparentSamplePlan()
	base := reparentDigestOf(t, plan.Target)
	if again := reparentDigestOf(t, reparentSamplePlan().Target); again != base {
		t.Fatalf("digest is not stable: %s vs %s", again, base)
	}

	cases := []struct {
		name   string
		mutate func(*ReparentPlanRow)
	}{
		{"git branch", func(r *ReparentPlanRow) { r.GitBranch = "feature/other" }},
		{"cutoff", func(r *ReparentPlanRow) { r.Cutoff.ResolvedSHA = reparentPlanFixtureString(strings.Repeat("e", 40)) }},
		{"head", func(r *ReparentPlanRow) { r.Head.SHA = reparentPlanFixtureString(strings.Repeat("d", 40)) }},
		{"binding", func(r *ReparentPlanRow) { r.DestinationBinding = ReparentBindingParentComputed }},
		{"destination parent", func(r *ReparentPlanRow) { r.DestinationParent = reparentPlanFixtureString("pr1") }},
		{"destination sha", func(r *ReparentPlanRow) { r.DestinationSHA = nil }},
		{"candidate count", func(r *ReparentPlanRow) { r.Replay.CandidateCount = reparentPlanFixtureInt(42) }},
		{"candidate order", func(r *ReparentPlanRow) {
			r.Replay.Commits = []string{strings.Repeat("b", 40), strings.Repeat("a", 40)}
		}},
		{"candidates probed but empty", func(r *ReparentPlanRow) { r.Replay.Commits = []string{} }},
		{"candidates not probed", func(r *ReparentPlanRow) { r.Replay.Commits = nil }},
	}

	seen := map[string]string{base: "unmodified"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := reparentSamplePlan().Target
			tc.mutate(&row)
			got := reparentDigestOf(t, row)
			if got == base {
				t.Fatalf("%s did not change the digest", tc.name)
			}
			if other, clash := seen[got]; clash {
				t.Fatalf("%s collides with %s", tc.name, other)
			}
			seen[got] = tc.name
		})
	}

	// Row identity members the digest deliberately ignores: a JIT seam
	// re-measures Git facts, not names or prose.
	ignored := reparentSamplePlan().Target
	ignored.Name = "renamed"
	ignored.Role = ReparentRoleDescendant
	ignored.Notes = []string{"noise"}
	ignored.Order = 12
	if reparentDigestOf(t, ignored) != base {
		t.Fatal("the digest must bind mutable Git facts only")
	}
}

// TestReparentRevalidationDigest_SeparateDomainFromThePlanTuple proves the
// row digest cannot be confused with a document fingerprint, and that the
// sync-typed RevalidationDigest is untouched and still takes a PlanEntry.
func TestReparentRevalidationDigest_SeparateDomainFromThePlanTuple(t *testing.T) {
	row := reparentSamplePlan().Target
	preimage := reparentRevalidationPreimage(row)
	if !strings.HasPrefix(string(preimage), reparentFingerprintPrefix) {
		t.Fatal("the row digest must be framed under the reparent domain")
	}
	offset := len(reparentFingerprintPrefix)
	schema := binary.BigEndian.Uint16(preimage[offset+2 : offset+4])
	if schema == reparentFingerprintTupleSchemaVersion {
		t.Fatal("the row digest must not reuse the document tuple's schema version")
	}
	if schema != reparentRevalidationTupleSchemaVersion {
		t.Fatalf("row digest schema = %d", schema)
	}

	// The shipped sync digest keeps its own signature and its own inputs.
	syncDigest, err := RevalidationDigest(PlanEntry{Name: "pr2", GitBranch: "feature/pr2"})
	if err != nil {
		t.Fatal(err)
	}
	if syncDigest == reparentDigestOf(t, row) {
		t.Fatal("a sync digest must never equal a reparent digest")
	}
}
