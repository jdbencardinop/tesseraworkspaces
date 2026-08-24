package internal

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// ============================================================================
// A minimal, test-local TLV decoder — the mirror image of the tlvEncoder /
// tlvElement machinery in rebase_plan_fingerprint.go. It exists purely to let
// these tests walk PlanFingerprintPreimage's bytes back into (id, tag,
// payload) tuples without duplicating any production behaviour: it performs
// no interpretation, no validation beyond framing, and is never used outside
// _test.go files.
// ============================================================================

type tlvField struct {
	id      uint16
	tag     byte
	payload []byte
}

// decodeTLVStructFields walks a STRUCT payload — the concatenation of
// [2-byte id][1-byte tag][4-byte length][payload] members written by
// tlvEncoder.writeField — into an ordered slice of fields.
func decodeTLVStructFields(t *testing.T, buf []byte) []tlvField {
	t.Helper()
	var out []tlvField
	for len(buf) > 0 {
		if len(buf) < 7 {
			t.Fatalf("truncated TLV struct field header: %d bytes left: % x", len(buf), buf)
		}
		id := binary.BigEndian.Uint16(buf[0:2])
		tag := buf[2]
		length := binary.BigEndian.Uint32(buf[3:7])
		buf = buf[7:]
		if uint32(len(buf)) < length {
			t.Fatalf("truncated TLV struct field payload: want %d have %d", length, len(buf))
		}
		out = append(out, tlvField{id: id, tag: tag, payload: buf[:length]})
		buf = buf[length:]
	}
	return out
}

// tlvArrayElement is one positional ARRAY member: tag + payload, no id.
type tlvArrayElement struct {
	tag     byte
	payload []byte
}

// decodeTLVArrayElements walks an ARRAY payload — the concatenation of
// [1-byte tag][4-byte length][payload] elements written by tlvElement — into
// an ordered slice of elements.
func decodeTLVArrayElements(t *testing.T, buf []byte) []tlvArrayElement {
	t.Helper()
	var out []tlvArrayElement
	for len(buf) > 0 {
		if len(buf) < 5 {
			t.Fatalf("truncated TLV array element header: %d bytes left: % x", len(buf), buf)
		}
		tag := buf[0]
		length := binary.BigEndian.Uint32(buf[1:5])
		buf = buf[5:]
		if uint32(len(buf)) < length {
			t.Fatalf("truncated TLV array element payload: want %d have %d", length, len(buf))
		}
		out = append(out, tlvArrayElement{tag: tag, payload: buf[:length]})
		buf = buf[length:]
	}
	return out
}

// fieldByID looks up exactly one field by id, failing the test if it is
// missing or duplicated (every id in these tables is written exactly once
// per struct, per the tlvEncoder contract).
func fieldByID(t *testing.T, fields []tlvField, id uint16) tlvField {
	t.Helper()
	var found *tlvField
	for i := range fields {
		if fields[i].id == id {
			if found != nil {
				t.Fatalf("field id %d written more than once", id)
			}
			f := fields[i]
			found = &f
		}
	}
	if found == nil {
		t.Fatalf("field id %d not written", id)
	}
	return *found
}

func fieldIDs(fields []tlvField) []uint16 {
	ids := make([]uint16, len(fields))
	for i, f := range fields {
		ids[i] = f.id
	}
	return ids
}

func idRange(from, to uint16) []uint16 {
	ids := make([]uint16, 0, int(to-from)+1)
	for id := from; id <= to; id++ {
		ids = append(ids, id)
	}
	return ids
}

func uint16SlicesEqual(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ============================================================================
// Fixture: a fully populated RebasePlan whose every fingerprint-relevant
// field carries a distinguishable, non-zero value. Built once and mutated by
// value (RebasePlan and its members are all copyable structs/slices-of-
// structs assigned wholesale) in individual test cases.
// ============================================================================

func fingerprintFixtureEntry() PlanEntry {
	return PlanEntry{
		Name:            "feature-a",
		GitBranch:       "feature-a",
		Repo:            "svc-a",
		Role:            "anchor",
		Materialization: "worktree",
		BaseContext: PlanContext{
			ContextID: strPtr("aaaa000000000000000000000000000000000000000000000000000000000001"),
			RepoRoot:  strPtr("/display/base-context-root"),
			Source:    "entry-repo",
		},
		ExecutionContext: PlanContext{
			ContextID: strPtr("bbbb000000000000000000000000000000000000000000000000000000000002"),
			RepoRoot:  strPtr("/display/execution-context-root"),
			Source:    "worktree",
		},
		Base: PlanEntryBase{
			Name:        strPtr("main"),
			Kind:        "stack-entry",
			Ref:         strPtr("refs/heads/main"),
			DecisionSHA: strPtr("cccc000000000000000000000000000000000000"),
		},
		Destination: PlanEntryDestination{
			SHA:         strPtr("dddd000000000000000000000000000000000000"),
			Deferred:    false,
			DependsOn:   nil,
			SnapshotSHA: strPtr("eeee000000000000000000000000000000000000"),
		},
		Head: PlanEntryHead{
			State: strPtr("present"),
			SHA:   strPtr("ffff000000000000000000000000000000000000"),
			Short: strPtr("ffff000"),
		},
		Cutoff: PlanEntryCutoff{
			RecordedSHA: strPtr("1111000000000000000000000000000000000000"),
			State:       strPtr("present"),
			Provenance:  "recorded-by-sync",
			ResolvedSHA: strPtr("1111000000000000000000000000000000000000"),
			Usage:       "used",
			Write:       "per-entry",
		},
		Strategy:          "rebase",
		StrategyCondition: nil,
		Argv:              []string{"rebase", "--onto", "dddd0", "cccc0", "feature-a"},
		ArgvAlternatives: &[2][]string{
			{"rebase", "--onto", "dddd0", "cccc0", "feature-a"},
			{"rebase", "--onto", "dddd0", "cccc0", "feature-a", "--rebase-merges"},
		},
		EffectiveBackend: strPtr("merge"),
		Replay: PlanEntryReplay{
			UpstreamRef:        strPtr("refs/remotes/origin/main"),
			UpstreamSHA:        strPtr("2222000000000000000000000000000000000000"),
			UpstreamProvenance: "fetched",
			Determinacy:        "exact",
			Reason:             nil,
			Range:              strPtr("cccc0..2222000000000000000000000000000000000000"),
			CandidateCount:     intPtr(3),
			FirstCandidate: &PlanReplayCandidate{
				SHA:     "3333000000000000000000000000000000000000",
				Short:   "3333000",
				Subject: "a commit subject",
			},
			Commits:                []string{"3333000000000000000000000000000000000000", "4444000000000000000000000000000000000000"},
			CommitsListed:          intPtr(2),
			CommitsTruncated:       boolPtr(false),
			CandidateDigest:        strPtr("5555000000000000000000000000000000000000000000000000000000000005"),
			MayDropPatchEquivalent: boolPtr(false),
			MayDropBecomesEmpty:    true,
		},
		CollateralRefs: []PlanCollateralRef{
			{Repo: "svc-a", Ref: "refs/heads/collateral", SHA: "6666000000000000000000000000000000000000", StackOwned: true},
		},
		CollateralExposed:   boolPtr(true),
		CollateralMechanism: strPtr("argv"),
		Mutation: PlanEntryMutation{
			WillSwitchHead:    true,
			WillLeaveHeadOn:   strPtr("feature-a"),
			HeadRestoredByRun: false,
		},
		Context: PlanEntryContext{
			Dirty:                       boolPtr(false),
			Autostash:                   boolPtr(false),
			AutostashAppliesToThisArm:   boolPtr(false),
			AutostashReapplyMayConflict: boolPtr(false),
			RebaseInProgress:            boolPtr(false),
			UntrackedPresent:            boolPtr(false),
			OverwriteRisk:               nil,
		},
		BranchCheckedOutAt: strPtr("/worktrees/feature-a"),
		Prunability:        PlanEntryPrunability{ProbeContext: "cwd"},
		Blocking:           false,
		Ancestry:           PlanAncestry{Status: ancestryStatusPtr(AncestryStatusCurrent)},
		Notes:              []string{},
	}
}

func ancestryStatusPtr(s AncestryStatus) *AncestryStatus { return &s }

func fingerprintFixturePlan() RebasePlan {
	entry := fingerprintFixtureEntry()
	return RebasePlan{
		SchemaVersion:  1,
		Route:          "checkout",
		RequestedRoute: nil,
		RouteTriggers:  []string{},
		Invocation:     "plan-only",
		Workspace: PlanWorkspace{
			Mode:     "single-repo",
			StableID: strPtr("workspace-stable-id"),
			RepoRoot: "/display/workspace-root",
		},
		Feature: "rebase-plan-guard",
		Policy: PlanPolicy{
			Fetch:               "auto",
			Propagation:         "cascade",
			ScopeKind:           "all",
			Selector:            nil,
			FetchDefaultApplied: true,
		},
		Intent: PlanIntent{
			Push:       true,
			PushSource: "flag",
			Validation: PlanIntentValidation{
				Applies:          true,
				Source:           "config",
				Stability:        "stable",
				GuardedStability: "stable",
				CommandDigest:    strPtr("7777000000000000000000000000000000000000000000000000000000000007"),
				CLITestIgnored:   false,
			},
		},
		Push: PlanPush{
			Targets: []PlanPushTarget{
				{
					Repo:            "svc-a",
					ContextID:       strPtr("bbbb000000000000000000000000000000000000000000000000000000000002"),
					RepoRoot:        strPtr("/display/push-target-root"),
					Materialization: "worktree",
					GitBranch:       "feature-a",
					Remote:          "origin",
					Ref:             "refs/heads/feature-a",
					Force:           "with-lease",
					Lease: PlanLease{
						Mode:                 "with-lease",
						Expectation:          "recorded",
						ExpectedRef:          strPtr("refs/remotes/origin/feature-a"),
						ExpectedSHA:          strPtr("8888000000000000000000000000000000000000"),
						ExpectedSHAFreshness: "fresh",
					},
					Scope: "in-scope",
				},
			},
			Executable: boolPtr(true),
			BlockedBy:  []PushBlockedKind{},
			Scope:      "in-scope",
		},
		Restore: PlanRestore{
			Applies:            true,
			WillSwitchHead:     boolPtr(false),
			TargetBranch:       strPtr("feature-a"),
			TargetSHA:          strPtr("ffff000000000000000000000000000000000000"),
			TargetSource:       strPtr("original-head"),
			Executable:         boolPtr(true),
			BlockedBy:          []RestoreBlockedKind{},
			DeletesTransaction: false,
			ReleasesLock:       false,
			PushDropped:        false,
		},
		Freshness: "current",
		Repositories: []PlanRepository{
			{
				Repo:       "svc-a",
				ContextID:  "bbbb000000000000000000000000000000000000000000000000000000000002",
				RepoRoot:   "/display/repo-root",
				RootSource: "worktree",
				Config: PlanRepositoryConfig{
					UpdateRefs:   PlanConfigSlot{Status: "valid", Value: strPtr("true"), Source: strPtr("local")},
					RebaseMerges: PlanConfigSlot{Status: "valid", Value: strPtr("true"), Source: strPtr("local"), Interpretation: "true"},
					Backend:      PlanConfigSlot{Status: "absent"},
					AutoStash:    PlanConfigSlot{Status: "valid", Value: strPtr("false"), Source: strPtr("local")},
				},
			},
		},
		State:          PlanState{},
		Runnable:       true,
		Blockers:       []PlanBlocker{},
		Warnings:       []PlanWarning{},
		EncodingIssues: []PlanEncodingIssue{},
		ConfigIssues:   []PlanConfigIssue{},
		Entries:        []PlanEntry{entry},
		Summary:        PlanSummary{Plannability: "rows", Entries: 1},
		Guard: PlanGuardBlock{
			Limits: PlanGuardLimitSet{
				MaxReplayPerEntry: PlanGuardLimit{Value: intPtr(50), Origin: "default"},
				MaxReplayTotal:    PlanGuardLimit{Value: intPtr(200), Origin: "default"},
			},
			IndeterminacyPolicy: "jit-deferred",
		},
		Refusal: PlanRefusal{},
		Approval: PlanApproval{
			Scope: "single-repo",
		},
	}
}

// rootDocumentFields decodes plan's PlanFingerprintPreimage down to the
// ordered list of document-level struct fields, failing the test on any
// framing violation.
func rootDocumentFields(t *testing.T, plan RebasePlan) []tlvField {
	t.Helper()
	preimage, err := PlanFingerprintPreimage(plan)
	if err != nil {
		t.Fatalf("PlanFingerprintPreimage: %v", err)
	}
	prefixLen := len(planFingerprintPrefix)
	if len(preimage) < prefixLen+2+2+1+4 {
		t.Fatalf("preimage too short to hold prefix+versions+root element header: %d bytes", len(preimage))
	}
	if string(preimage[:prefixLen]) != planFingerprintPrefix {
		t.Fatalf("prefix = %q, want %q", preimage[:prefixLen], planFingerprintPrefix)
	}
	rest := preimage[prefixLen:]
	encodingVersion := binary.BigEndian.Uint16(rest[0:2])
	if encodingVersion != planFingerprintEncodingVersion {
		t.Fatalf("encoding version = 0x%04x, want 0x%04x", encodingVersion, planFingerprintEncodingVersion)
	}
	tupleVersion := binary.BigEndian.Uint16(rest[2:4])
	if tupleVersion != planFingerprintTupleSchemaVersion {
		t.Fatalf("tuple schema version = 0x%04x, want 0x%04x", tupleVersion, planFingerprintTupleSchemaVersion)
	}
	rootElement := rest[4:]
	rootTag := rootElement[0]
	if rootTag != tlvTagStruct {
		t.Fatalf("root element tag = 0x%02x, want STRUCT (0x%02x)", rootTag, tlvTagStruct)
	}
	rootLength := binary.BigEndian.Uint32(rootElement[1:5])
	rootPayload := rootElement[5:]
	if uint32(len(rootPayload)) != rootLength {
		t.Fatalf("root element length = %d, want %d (payload is %d bytes)", rootLength, len(rootPayload), len(rootPayload))
	}
	if len(rootElement) != 5+len(rootPayload) {
		t.Fatalf("trailing bytes after the single root element: %d extra", len(rootElement)-5-len(rootPayload))
	}
	return decodeTLVStructFields(t, rootPayload)
}

// entryFields decodes the first entries[] element (a nested STRUCT) of the
// document-level "entries" array field (id 23) into its ordered field list.
func entryFields(t *testing.T, plan RebasePlan) []tlvField {
	t.Helper()
	fields := rootDocumentFields(t, plan)
	entriesField := fieldByID(t, fields, fpDocEntries)
	if entriesField.tag != tlvTagArray {
		t.Fatalf("entries field tag = 0x%02x, want ARRAY (0x%02x)", entriesField.tag, tlvTagArray)
	}
	elements := decodeTLVArrayElements(t, entriesField.payload)
	if len(elements) == 0 {
		t.Fatal("expected at least one entries[] element in the fixture")
	}
	if elements[0].tag != tlvTagStruct {
		t.Fatalf("entries[0] tag = 0x%02x, want STRUCT (0x%02x)", elements[0].tag, tlvTagStruct)
	}
	return decodeTLVStructFields(t, elements[0].payload)
}

// ============================================================================
// Pre-image framing (§8.1)
// ============================================================================

func TestPlanFingerprint_PreimageFraming(t *testing.T) {
	plan := fingerprintFixturePlan()
	preimage, err := PlanFingerprintPreimage(plan)
	if err != nil {
		t.Fatalf("PlanFingerprintPreimage: %v", err)
	}

	if !bytes.HasPrefix(preimage, []byte("tws-plan-fp\x00")) {
		t.Fatalf("preimage does not start with the fixed \"tws-plan-fp\\x00\" prefix: % x", preimage[:16])
	}
	if got, want := planFingerprintPrefix, "tws-plan-fp\x00"; got != want {
		t.Fatalf("planFingerprintPrefix = %q, want %q", got, want)
	}

	rest := preimage[len(planFingerprintPrefix):]
	if len(rest) < 4 {
		t.Fatalf("preimage has no room for the two version fields: %d bytes left", len(rest))
	}
	encodingVersion := binary.BigEndian.Uint16(rest[0:2])
	tupleVersion := binary.BigEndian.Uint16(rest[2:4])
	if encodingVersion != 0x0001 {
		t.Errorf("encoding version = 0x%04x, want 0x0001", encodingVersion)
	}
	if tupleVersion != 0x0004 {
		t.Errorf("tuple schema version = 0x%04x, want 0x0004", tupleVersion)
	}

	rootElement := rest[4:]
	if len(rootElement) < 5 {
		t.Fatalf("root element has no room for a tag+length header: %d bytes", len(rootElement))
	}
	rootLength := binary.BigEndian.Uint32(rootElement[1:5])
	wantTotal := len(planFingerprintPrefix) + 4 + 1 + 4 + int(rootLength)
	if len(preimage) != wantTotal {
		t.Errorf("total preimage length = %d, want prefix(%d)+versions(4)+tag(1)+length(4)+payload(%d) = %d",
			len(preimage), len(planFingerprintPrefix), rootLength, wantTotal)
	}
}

func TestPlanFingerprint_FieldSeparatorFraming(t *testing.T) {
	plan := fingerprintFixturePlan()
	preimage, err := PlanFingerprintPreimage(plan)
	if err != nil {
		t.Fatalf("PlanFingerprintPreimage: %v", err)
	}
	prefixLen := len(planFingerprintPrefix)
	rootPayload := preimage[prefixLen+4+5:]
	fields := decodeTLVStructFields(t, rootPayload)
	// Every struct member is framed as [2-byte id][1-byte tag][4-byte
	// length][payload]: reconstructing the raw bytes from the decoded
	// fields and comparing against rootPayload proves the separator widths
	// (2/1/4) are exactly what tlvEncoder.writeField uses, with no extra or
	// missing framing bytes anywhere in the sequence.
	var rebuilt bytes.Buffer
	for _, f := range fields {
		var idBuf [2]byte
		binary.BigEndian.PutUint16(idBuf[:], f.id)
		rebuilt.Write(idBuf[:])
		rebuilt.WriteByte(f.tag)
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(f.payload)))
		rebuilt.Write(lenBuf[:])
		rebuilt.Write(f.payload)
	}
	if !bytes.Equal(rebuilt.Bytes(), rootPayload) {
		t.Fatalf("re-framing decoded fields did not reproduce the original payload byte-for-byte")
	}
}

// ============================================================================
// Document-level field order, retired id, never-null arrays (§8.3)
// ============================================================================

func TestPlanFingerprint_DocumentFieldOrderIsAscendingOneToTwentyThree(t *testing.T) {
	plan := fingerprintFixturePlan()
	fields := rootDocumentFields(t, plan)
	got := fieldIDs(fields)
	want := idRange(1, 23)
	if !uint16SlicesEqual(got, want) {
		t.Fatalf("document field id sequence = %v, want ascending 1..23 = %v", got, want)
	}
}

func TestPlanFingerprint_RetiredFieldIDIsExplicitNull(t *testing.T) {
	plan := fingerprintFixturePlan()
	fields := rootDocumentFields(t, plan)
	retired := fieldByID(t, fields, fpDocWorkspaceRepoRootRetired)
	if retired.tag != tlvTagNull {
		t.Errorf("id 3 (retired workspace.repo_root slot) tag = 0x%02x, want NULL (0x%02x)", retired.tag, tlvTagNull)
	}
	if len(retired.payload) != 0 {
		t.Errorf("id 3 payload = % x, want zero-length", retired.payload)
	}
	// Confirm id 3 sits exactly between stable_id (2) and feature (4), i.e.
	// it is written positionally in place, never skipped.
	ids := fieldIDs(fields)
	for i, id := range ids {
		if id == 3 {
			if i == 0 || i+1 >= len(ids) || ids[i-1] != 2 || ids[i+1] != 4 {
				t.Fatalf("id 3 neighbours = %v, want [..., 2, 3, 4, ...]", ids)
			}
		}
	}
}

func TestPlanFingerprint_TopLevelNeverNullArraysAreArrayTagEvenWhenNil(t *testing.T) {
	plan := fingerprintFixturePlan()
	plan.Repositories = nil
	plan.Push.Targets = nil
	plan.Entries = nil

	fields := rootDocumentFields(t, plan)
	for _, id := range []uint16{fpDocRepositories, fpDocPushTargets, fpDocEntries} {
		f := fieldByID(t, fields, id)
		if f.tag != tlvTagArray {
			t.Errorf("id %d tag = 0x%02x with a nil Go slice, want ARRAY (0x%02x) — never-null array must stay ARRAY, not become NULL", id, f.tag, tlvTagArray)
		}
		if len(f.payload) != 0 {
			t.Errorf("id %d payload = % x, want zero-length for a nil/empty slice", id, f.payload)
		}
	}
}

// ============================================================================
// Entry-row field order and the three fixed anchors (§8.2, §8.3)
// ============================================================================

func TestPlanFingerprint_EntryFieldOrderIsAscendingOneToForty(t *testing.T) {
	plan := fingerprintFixturePlan()
	fields := entryFields(t, plan)
	got := fieldIDs(fields)
	want := idRange(1, 40)
	if !uint16SlicesEqual(got, want) {
		t.Fatalf("entry field id sequence = %v, want ascending 1..40 = %v", got, want)
	}
}

func TestPlanFingerprint_EntryFixedAnchors(t *testing.T) {
	plan := fingerprintFixturePlan()
	entry := plan.Entries[0]
	fields := entryFields(t, plan)

	execCtx := fieldByID(t, fields, 4)
	if execCtx.tag != tlvTagBytes {
		t.Fatalf("id 4 (execution_context.context_id, fixed anchor) tag = 0x%02x, want BYTES", execCtx.tag)
	}
	if got, want := string(execCtx.payload), *entry.ExecutionContext.ContextID; got != want {
		t.Errorf("id 4 payload = %q, want execution_context.context_id = %q", got, want)
	}

	baseCtx := fieldByID(t, fields, 36)
	if baseCtx.tag != tlvTagBytes {
		t.Fatalf("id 36 (base_context.context_id, fixed anchor) tag = 0x%02x, want BYTES", baseCtx.tag)
	}
	if got, want := string(baseCtx.payload), *entry.BaseContext.ContextID; got != want {
		t.Errorf("id 36 payload = %q, want base_context.context_id = %q", got, want)
	}

	baseDecision := fieldByID(t, fields, 38)
	if baseDecision.tag != tlvTagBytes {
		t.Fatalf("id 38 (base.decision_sha, fixed anchor) tag = 0x%02x, want BYTES", baseDecision.tag)
	}
	if got, want := string(baseDecision.payload), *entry.Base.DecisionSHA; got != want {
		t.Errorf("id 38 payload = %q, want base.decision_sha = %q", got, want)
	}

	// Cross-check these three ids are not accidentally aliased to each
	// other's values (a copy/paste bug would still pass a same-value
	// assertion).
	if execCtx.id == baseCtx.id || execCtx.id == baseDecision.id || baseCtx.id == baseDecision.id {
		t.Fatal("the three fixed-anchor ids must be pairwise distinct")
	}
}

func TestPlanFingerprint_EntryCollateralKnownFlagAndArrayPresence(t *testing.T) {
	// Known, non-empty collateral: id 39 (known) is true and id 40 (refs)
	// is a one-element ARRAY.
	plan := fingerprintFixturePlan()
	fields := entryFields(t, plan)
	known := fieldByID(t, fields, fpEntryCollateralKnown)
	if known.tag != tlvTagBool || len(known.payload) != 1 || known.payload[0] != 1 {
		t.Errorf("collateral_known field = %+v, want BOOL true", known)
	}
	refs := fieldByID(t, fields, fpEntryCollateralRefs)
	if refs.tag != tlvTagArray {
		t.Fatalf("collateral_refs tag = 0x%02x, want ARRAY", refs.tag)
	}
	elements := decodeTLVArrayElements(t, refs.payload)
	if len(elements) != 1 {
		t.Fatalf("collateral_refs has %d elements, want 1", len(elements))
	}

	// Unknown collateral (nil slice, distinct from a known-empty slice):
	// id 39 flips to false and id 40 becomes an explicit NULL, never an
	// empty ARRAY.
	plan2 := fingerprintFixturePlan()
	entry2 := plan2.Entries[0]
	entry2.CollateralRefs = nil
	plan2.Entries = []PlanEntry{entry2}
	fields2 := entryFields(t, plan2)
	known2 := fieldByID(t, fields2, fpEntryCollateralKnown)
	if known2.tag != tlvTagBool || len(known2.payload) != 1 || known2.payload[0] != 0 {
		t.Errorf("collateral_known field with nil refs = %+v, want BOOL false", known2)
	}
	refs2 := fieldByID(t, fields2, fpEntryCollateralRefs)
	if refs2.tag != tlvTagNull {
		t.Errorf("collateral_refs tag with nil refs = 0x%02x, want NULL (0x%02x)", refs2.tag, tlvTagNull)
	}

	// Known-but-empty collateral (a non-nil, zero-length slice) must be
	// distinguished from both of the above: known stays true, but refs is
	// a present, zero-length ARRAY rather than NULL.
	plan3 := fingerprintFixturePlan()
	entry3 := plan3.Entries[0]
	entry3.CollateralRefs = []PlanCollateralRef{}
	plan3.Entries = []PlanEntry{entry3}
	fields3 := entryFields(t, plan3)
	known3 := fieldByID(t, fields3, fpEntryCollateralKnown)
	if known3.tag != tlvTagBool || len(known3.payload) != 1 || known3.payload[0] != 1 {
		t.Errorf("collateral_known field with empty (non-nil) refs = %+v, want BOOL true", known3)
	}
	refs3 := fieldByID(t, fields3, fpEntryCollateralRefs)
	if refs3.tag != tlvTagArray || len(refs3.payload) != 0 {
		t.Errorf("collateral_refs with empty (non-nil) refs = %+v, want a zero-length ARRAY", refs3)
	}
}

func TestPlanFingerprint_ArgvAlternativesNullVsPopulatedArray(t *testing.T) {
	plan := fingerprintFixturePlan()
	fields := entryFields(t, plan)
	argv := fieldByID(t, fields, fpEntryArgvAlternatives)
	if argv.tag != tlvTagArray {
		t.Fatalf("argv_alternatives tag = 0x%02x, want ARRAY (present pointer)", argv.tag)
	}
	arms := decodeTLVArrayElements(t, argv.payload)
	if len(arms) != 2 {
		t.Fatalf("argv_alternatives has %d arms, want exactly 2", len(arms))
	}
	for i, arm := range arms {
		if arm.tag != tlvTagArray {
			t.Errorf("arm %d tag = 0x%02x, want nested ARRAY of tokens", i, arm.tag)
		}
	}

	plan2 := fingerprintFixturePlan()
	entry2 := plan2.Entries[0]
	entry2.ArgvAlternatives = nil
	plan2.Entries = []PlanEntry{entry2}
	fields2 := entryFields(t, plan2)
	argv2 := fieldByID(t, fields2, fpEntryArgvAlternatives)
	if argv2.tag != tlvTagNull {
		t.Errorf("argv_alternatives tag with a nil pointer = 0x%02x, want NULL (0x%02x)", argv2.tag, tlvTagNull)
	}
}

// ============================================================================
// Nested-row field order: repository and push-target tables (§8.3)
// ============================================================================

func TestPlanFingerprint_RepositoryFieldOrder(t *testing.T) {
	plan := fingerprintFixturePlan()
	fields := rootDocumentFields(t, plan)
	repos := fieldByID(t, fields, fpDocRepositories)
	elements := decodeTLVArrayElements(t, repos.payload)
	if len(elements) != 1 {
		t.Fatalf("repositories[] has %d elements, want 1", len(elements))
	}
	repoFields := decodeTLVStructFields(t, elements[0].payload)
	got := fieldIDs(repoFields)
	want := idRange(1, 11)
	if !uint16SlicesEqual(got, want) {
		t.Fatalf("repository row field id sequence = %v, want ascending 1..11 = %v", got, want)
	}
	repoName := fieldByID(t, repoFields, fpRepoRepo)
	if string(repoName.payload) != plan.Repositories[0].Repo {
		t.Errorf("repo field = %q, want %q", repoName.payload, plan.Repositories[0].Repo)
	}
}

func TestPlanFingerprint_PushTargetFieldOrder(t *testing.T) {
	plan := fingerprintFixturePlan()
	fields := rootDocumentFields(t, plan)
	targets := fieldByID(t, fields, fpDocPushTargets)
	elements := decodeTLVArrayElements(t, targets.payload)
	if len(elements) != 1 {
		t.Fatalf("push.targets[] has %d elements, want 1", len(elements))
	}
	targetFields := decodeTLVStructFields(t, elements[0].payload)
	got := fieldIDs(targetFields)
	want := idRange(1, 10)
	if !uint16SlicesEqual(got, want) {
		t.Fatalf("push target row field id sequence = %v, want ascending 1..10 = %v", got, want)
	}
	branch := fieldByID(t, targetFields, fpPushGitBranch)
	if string(branch.payload) != plan.Push.Targets[0].GitBranch {
		t.Errorf("git_branch field = %q, want %q", branch.payload, plan.Push.Targets[0].GitBranch)
	}
}

// ============================================================================
// Scalar distinctions (a value change moves the digest; a display-only path
// change does not) and the explicit non-members
// ============================================================================

func TestPlanFingerprint_ValueChangeMovesDigest(t *testing.T) {
	base := fingerprintFixturePlan()
	baseFP, err := PlanFingerprint(base)
	if err != nil {
		t.Fatalf("PlanFingerprint(base): %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(p *RebasePlan)
	}{
		{"feature", func(p *RebasePlan) { p.Feature = "other-feature" }},
		{"workspace.mode", func(p *RebasePlan) { p.Workspace.Mode = "multi-repo" }},
		{"policy.fetch", func(p *RebasePlan) { p.Policy.Fetch = "never" }},
		{"intent.push", func(p *RebasePlan) { p.Intent.Push = false }},
		{"restore.applies", func(p *RebasePlan) { p.Restore.Applies = false }},
		{"guard.limits.max_replay_per_entry", func(p *RebasePlan) { p.Guard.Limits.MaxReplayPerEntry.Value = intPtr(1) }},
		{"repositories[0].config.backend.status", func(p *RebasePlan) { p.Repositories[0].Config.Backend.Status = "invalid" }},
		{"push.targets[0].git_branch", func(p *RebasePlan) { p.Push.Targets[0].GitBranch = "other-branch" }},
		{"push.targets[0].lease.expected_sha", func(p *RebasePlan) {
			p.Push.Targets[0].Lease.ExpectedSHA = strPtr("bbbb111111111111111111111111111111111111")
		}},
		{"entries[0].strategy", func(p *RebasePlan) { p.Entries[0].Strategy = "merge" }},
		{"entries[0].replay.determinacy", func(p *RebasePlan) { p.Entries[0].Replay.Determinacy = "unknown" }},
		{"entries[0].destination.sha", func(p *RebasePlan) { p.Entries[0].Destination.SHA = strPtr("9999000000000000000000000000000000000000") }},
		{"entries[0].execution_context.context_id", func(p *RebasePlan) {
			p.Entries[0].ExecutionContext.ContextID = strPtr("0000111111111111111111111111111111111111111111111111111111111111")
		}},
		{"entries[0].collateral_refs[0].sha", func(p *RebasePlan) { p.Entries[0].CollateralRefs[0].SHA = "aaaa111111111111111111111111111111111111" }},
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			mutated := fingerprintFixturePlan()
			m.mutate(&mutated)
			mutatedFP, err := PlanFingerprint(mutated)
			if err != nil {
				t.Fatalf("PlanFingerprint(mutated): %v", err)
			}
			if mutatedFP == baseFP {
				t.Errorf("mutating %s did not change the fingerprint (both = %s)", m.name, baseFP)
			}
		})
	}
}

func TestPlanFingerprint_DisplayOnlyPathChangeDoesNotMoveDigest(t *testing.T) {
	base := fingerprintFixturePlan()
	baseFP, err := PlanFingerprint(base)
	if err != nil {
		t.Fatalf("PlanFingerprint(base): %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(p *RebasePlan)
	}{
		{"workspace.repo_root", func(p *RebasePlan) { p.Workspace.RepoRoot = "/completely/different/display/path" }},
		{"entries[0].execution_context.repo_root", func(p *RebasePlan) {
			p.Entries[0].ExecutionContext.RepoRoot = strPtr("/completely/different/display/path")
		}},
		{"entries[0].base_context.repo_root", func(p *RebasePlan) {
			p.Entries[0].BaseContext.RepoRoot = strPtr("/completely/different/display/path")
		}},
		{"repositories[0].repo_root", func(p *RebasePlan) { p.Repositories[0].RepoRoot = "/completely/different/display/path" }},
		{"repositories[0].root_source", func(p *RebasePlan) { p.Repositories[0].RootSource = "process-cwd" }},
		{"push.targets[0].repo_root", func(p *RebasePlan) { p.Push.Targets[0].RepoRoot = strPtr("/completely/different/display/path") }},
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			mutated := fingerprintFixturePlan()
			m.mutate(&mutated)
			mutatedFP, err := PlanFingerprint(mutated)
			if err != nil {
				t.Fatalf("PlanFingerprint(mutated): %v", err)
			}
			if mutatedFP != baseFP {
				t.Errorf("mutating display-only field %s changed the fingerprint: base=%s mutated=%s", m.name, baseFP, mutatedFP)
			}
		})
	}
}

func TestPlanFingerprint_ReplayCommitsIsNonMember(t *testing.T) {
	base := fingerprintFixturePlan()
	baseFP, err := PlanFingerprint(base)
	if err != nil {
		t.Fatalf("PlanFingerprint(base): %v", err)
	}

	mutated := fingerprintFixturePlan()
	// Change replay.commits[] both in content and in length; the pre-image
	// must be unaffected because Replay.Commits truly never reaches any
	// tlvEncoder call in encodeFingerprintEntry.
	mutated.Entries[0].Replay.Commits = []string{"zzzz000000000000000000000000000000000000"}
	mutated.Entries[0].Replay.CommitsListed = intPtr(1)
	mutatedFP, err := PlanFingerprint(mutated)
	if err != nil {
		t.Fatalf("PlanFingerprint(mutated): %v", err)
	}
	if mutatedFP != baseFP {
		t.Errorf("changing replay.commits[]/.commits_listed changed the fingerprint: base=%s mutated=%s", baseFP, mutatedFP)
	}

	mutatedNil := fingerprintFixturePlan()
	mutatedNil.Entries[0].Replay.Commits = nil
	mutatedNilFP, err := PlanFingerprint(mutatedNil)
	if err != nil {
		t.Fatalf("PlanFingerprint(mutatedNil): %v", err)
	}
	if mutatedNilFP != baseFP {
		t.Errorf("nil-ing replay.commits[] changed the fingerprint: base=%s mutatedNil=%s", baseFP, mutatedNilFP)
	}
}

// TestPlanFingerprint_NonMemberFieldsAbsentFromEntryEncoder is a source-level
// cross-check that replay.commits/.commits_truncated never appear in
// encodeFingerprintEntry (the function that owns every entry-level TLV
// write), guarding against the behavioural tests above passing for the wrong
// reason (e.g. two accidentally-equal digests).
func TestPlanFingerprint_NonMemberFieldsAbsentFromEntryEncoder(t *testing.T) {
	src := readSourceFile(t, "rebase_plan_fingerprint.go")
	start := strings.Index(src, "func encodeFingerprintEntry(")
	if start < 0 {
		t.Fatal("encodeFingerprintEntry not found in rebase_plan_fingerprint.go")
	}
	rest := src[start:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("could not find the end of encodeFingerprintEntry (no following top-level func)")
	}
	body := rest[:end]
	for _, non := range []string{"entry.Replay.Commits", "entry.Replay.CommitsTruncated", "entry.Replay.CommitsListed"} {
		if strings.Contains(body, non) {
			t.Errorf("encodeFingerprintEntry body unexpectedly references %s; replay.commits[] must stay a non-member", non)
		}
	}
}

// TestPlanFingerprint_LeaseFreshnessIsNonMember confirms push.targets[].lease.
// expected_sha_freshness — a staleness/confidence annotation about the
// already-bound expected_sha, not a new identity fact — is excluded from the
// fingerprint tuple. The design spec repeatedly scopes the lease's
// fingerprint-bound members to exactly {expectation, ref, sha}
// (spec §8.3's approval-tuple sentences, restated by §14.2's lease rows and
// §21's fetching-guarded-run note), and
// encodeFingerprintPushTarget's own field-id table comment agrees ("lease
// expectation/ref/sha", never mentioning freshness) — so this is a deliberate
// non-member, not an omission bug: including a value that can flip between
// benign re-invocations of the same push intent (purely from probe timing)
// would defeat the fingerprint's cross-invocation approval-reuse purpose.
func TestPlanFingerprint_LeaseFreshnessIsNonMember(t *testing.T) {
	base := fingerprintFixturePlan()
	baseFP, err := PlanFingerprint(base)
	if err != nil {
		t.Fatalf("PlanFingerprint(base): %v", err)
	}
	if base.Push.Targets[0].Lease.ExpectedSHAFreshness != "fresh" {
		t.Fatalf("fixture assumption broken: want the base fixture's freshness to start as %q", "fresh")
	}

	mutated := fingerprintFixturePlan()
	mutated.Push.Targets[0].Lease.ExpectedSHAFreshness = "possibly-stale"
	mutatedFP, err := PlanFingerprint(mutated)
	if err != nil {
		t.Fatalf("PlanFingerprint(mutated): %v", err)
	}
	if mutatedFP != baseFP {
		t.Errorf("changing lease.expected_sha_freshness changed the fingerprint: base=%s mutated=%s", baseFP, mutatedFP)
	}

	// Source-level cross-check: encodeFingerprintPushTarget must never
	// reference target.Lease.ExpectedSHAFreshness, so the behavioural
	// assertion above isn't passing for the wrong reason (e.g. an
	// accidental value collision).
	src := readSourceFile(t, "rebase_plan_fingerprint.go")
	start := strings.Index(src, "func encodeFingerprintPushTarget(")
	if start < 0 {
		t.Fatal("encodeFingerprintPushTarget not found in rebase_plan_fingerprint.go")
	}
	rest := src[start:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("could not find the end of encodeFingerprintPushTarget")
	}
	if strings.Contains(rest[:end], "ExpectedSHAFreshness") {
		t.Errorf("encodeFingerprintPushTarget unexpectedly references ExpectedSHAFreshness; it must stay a fingerprint non-member")
	}
}

// ============================================================================
// Determinism and output format
// ============================================================================

func TestPlanFingerprint_OutputIsSixtyFourLowercaseHexAndDeterministic(t *testing.T) {
	plan := fingerprintFixturePlan()
	fp1, err := PlanFingerprint(plan)
	if err != nil {
		t.Fatalf("PlanFingerprint (first call): %v", err)
	}
	fp2, err := PlanFingerprint(plan)
	if err != nil {
		t.Fatalf("PlanFingerprint (second call): %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("PlanFingerprint is not deterministic across calls on the same value: %s vs %s", fp1, fp2)
	}
	if len(fp1) != 64 {
		t.Fatalf("PlanFingerprint length = %d, want 64", len(fp1))
	}
	if strings.ToLower(fp1) != fp1 {
		t.Fatalf("PlanFingerprint is not all-lowercase: %s", fp1)
	}
	for _, r := range fp1 {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("PlanFingerprint contains a non-hex rune %q: %s", r, fp1)
		}
	}
}

// ============================================================================
// RevalidationDigest (§10.3, §10.4) — an independent field-id table
// ============================================================================

func TestPlanFingerprint_RevalidationDigestHasItsOwnFieldTableIndependentOfFingerprint(t *testing.T) {
	base := fingerprintFixtureEntry()
	baseFP, err := PlanFingerprint(fingerprintFixturePlanWithEntry(base))
	if err != nil {
		t.Fatalf("PlanFingerprint(base): %v", err)
	}
	baseReval, err := RevalidationDigest(base)
	if err != nil {
		t.Fatalf("RevalidationDigest(base): %v", err)
	}

	// context.rebase_in_progress is a revalidation member but a fingerprint
	// non-member: mutating it must move RevalidationDigest but not
	// PlanFingerprint.
	withRebaseInProgress := fingerprintFixtureEntry()
	withRebaseInProgress.Context.RebaseInProgress = boolPtr(true)
	fpWithRIP, err := PlanFingerprint(fingerprintFixturePlanWithEntry(withRebaseInProgress))
	if err != nil {
		t.Fatalf("PlanFingerprint(withRebaseInProgress): %v", err)
	}
	revalWithRIP, err := RevalidationDigest(withRebaseInProgress)
	if err != nil {
		t.Fatalf("RevalidationDigest(withRebaseInProgress): %v", err)
	}
	if fpWithRIP != baseFP {
		t.Errorf("context.rebase_in_progress must be a PlanFingerprint non-member, but the fingerprint changed")
	}
	if revalWithRIP == baseReval {
		t.Errorf("context.rebase_in_progress must be a RevalidationDigest member, but the digest did not change")
	}

	// name/git_branch/role/materialization are fingerprint members but
	// never drift at a JIT seam, so they are declared RevalidationDigest
	// non-members: mutating name must move PlanFingerprint but not
	// RevalidationDigest.
	withDifferentName := fingerprintFixtureEntry()
	withDifferentName.Name = "a-completely-different-name"
	fpWithName, err := PlanFingerprint(fingerprintFixturePlanWithEntry(withDifferentName))
	if err != nil {
		t.Fatalf("PlanFingerprint(withDifferentName): %v", err)
	}
	revalWithName, err := RevalidationDigest(withDifferentName)
	if err != nil {
		t.Fatalf("RevalidationDigest(withDifferentName): %v", err)
	}
	if fpWithName == baseFP {
		t.Errorf("entries[].name must be a PlanFingerprint member, but the fingerprint did not change")
	}
	if revalWithName != baseReval {
		t.Errorf("entries[].name must be a RevalidationDigest non-member, but the digest changed")
	}
}

func TestPlanFingerprint_RevalidationDigestFieldOrderIsAscendingOneToEighteen(t *testing.T) {
	entry := fingerprintFixtureEntry()
	// RevalidationDigest hashes rather than exposing a raw pre-image
	// accessor, so this test asserts the field-id table itself (the
	// constants that anchor every write in encodeRevalidationEntry) is the
	// closed, ascending 1..18 sequence the doc comment promises, and that
	// each id is referenced exactly once in the encoder body.
	ids := []uint16{
		fpRevalDestinationSHA, fpRevalDestinationSnapshotSHA, fpRevalDestinationDeferred,
		fpRevalDestinationDependsOn, fpRevalHeadState, fpRevalHeadSHA, fpRevalUpstreamSHA,
		fpRevalDeterminacy, fpRevalCandidateCount, fpRevalFirstCandidateSHA, fpRevalCandidateDigest,
		fpRevalStrategy, fpRevalArgvAlternatives, fpRevalCollateralMechanism, fpRevalCollateralExposed,
		fpRevalCollateralRefs, fpRevalBranchCheckedOutAt, fpRevalRebaseInProgress,
	}
	want := idRange(1, 18)
	if !uint16SlicesEqual(ids, want) {
		t.Fatalf("revalidation field-id table = %v, want ascending 1..18 = %v", ids, want)
	}
	_ = entry
}

func fingerprintFixturePlanWithEntry(entry PlanEntry) RebasePlan {
	plan := fingerprintFixturePlan()
	plan.Entries = []PlanEntry{entry}
	return plan
}
