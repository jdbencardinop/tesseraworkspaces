package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
)

// ============================================================================
// Reparent fingerprint (§7.13) — its own domain, over the shipped TLV framing
//
// The TLV machinery is REUSED, not copied: tlvEncoder and its writeField /
// null / boolValue / boolPtr / uintPtr / bytesValue / bytesPtr / arrayValue /
// arrayPtr methods, plus tlvElement and tlvConcat, are members of this
// package and are called directly from here.
//
// What is NOT reused is the domain. planFingerprintPrefix,
// planFingerprintEncodingVersion and planFingerprintTupleSchemaVersion stay
// exactly as they are, so every sync approval token minted before this
// feature remains valid, and no sync token can ever be accepted as a reparent
// approval or vice versa.
//
// Every deferred value of §7.5a is written with enc.null(...) — an explicit
// TLV NULL, never an omitted member and never a guess. That is precisely what
// makes the preimage a --plan produces and the preimage a fresh execution
// recomputes at admission byte-identical: both agree about what is not yet
// known.
// ============================================================================

const (
	reparentFingerprintPrefix             = "tws-reparent-fp\x00"
	reparentFingerprintEncodingVersion    = 0x0001
	reparentFingerprintTupleSchemaVersion = 0x0003

	// reparentRevalidationTupleSchemaVersion is the row-digest tuple's own
	// schema version. It deliberately differs from the document tuple's, so
	// a row digest and a document fingerprint cannot collide even though
	// both are framed under the same reparent prefix; and it is not the sync
	// tuple schema version, so a sync digest can never collide with a
	// reparent digest either.
	reparentRevalidationTupleSchemaVersion = 0x0101
)

// Document-level field-id table (§7.13). Exactly 33 members, in the fixed
// order the spec fixes them in; the order is the contract, so ids are never
// renumbered and a retired id would be held by an explicit NULL rather than
// reused.
const (
	fpReparentWorkspaceMode           uint16 = 1
	fpReparentWorkspaceStableID       uint16 = 2
	fpReparentFeature                 uint16 = 3
	fpReparentRoute                   uint16 = 4
	fpReparentTargetName              uint16 = 5
	fpReparentTargetGitBranch         uint16 = 6
	fpReparentTargetRepo              uint16 = 7
	fpReparentOldParentStoredToken    uint16 = 8
	fpReparentOldParentKind           uint16 = 9
	fpReparentOldParentRef            uint16 = 10
	fpReparentOldParentSHA            uint16 = 11
	fpReparentNewParentStoredToken    uint16 = 12
	fpReparentNewParentKind           uint16 = 13
	fpReparentNewParentRef            uint16 = 14
	fpReparentNewParentSHA            uint16 = 15
	fpReparentOntoKindRequested       uint16 = 16
	fpReparentOIDWidth                uint16 = 17
	fpReparentTargetCutoffSHA         uint16 = 18
	fpReparentTargetCutoffProvenance  uint16 = 19
	fpReparentClosure                 uint16 = 20
	fpReparentRowHeads                uint16 = 21
	fpReparentRowCutoffs              uint16 = 22
	fpReparentRowDestinations         uint16 = 23
	fpReparentRowArgv                 uint16 = 24
	fpReparentRowCandidates           uint16 = 25
	fpReparentMetadataDelta           uint16 = 26
	fpReparentStackSHA256Before       uint16 = 27
	fpReparentFetchPolicy             uint16 = 28
	fpReparentGuardLimitPerEntry      uint16 = 29
	fpReparentGuardLimitTotal         uint16 = 30
	fpReparentValidationCommandDigest uint16 = 31
	fpReparentStrategy                uint16 = 32
	fpReparentApprovalScope           uint16 = 33
)

// ReparentFingerprintFieldCount is the frozen size of the tuple above. A test
// asserts it against the id table so a 34th field cannot be added silently.
const ReparentFingerprintFieldCount = 33

// Closure-row element field ids (§7.13 field 20).
const (
	fpReparentClosureName      uint16 = 1
	fpReparentClosureGitBranch uint16 = 2
	fpReparentClosureOrder     uint16 = 3
	fpReparentClosureRepo      uint16 = 4
	fpReparentClosureContextID uint16 = 5
	fpReparentClosureRepoRoot  uint16 = 6
	fpReparentClosureSource    uint16 = 7
)

// Target-repository identity field ids (§7.13 field 7).
const (
	fpReparentTargetRepoToken      uint16 = 1
	fpReparentTargetContextID      uint16 = 2
	fpReparentTargetRepoRoot       uint16 = 3
	fpReparentTargetContextSource  uint16 = 4
	fpReparentTargetRequestedToken uint16 = 5
)

// Per-row cutoff element field ids (§7.13 field 22).
const (
	fpReparentRowCutoffSHA        uint16 = 1
	fpReparentRowCutoffProvenance uint16 = 2
	fpReparentRowCutoffSupplied   uint16 = 3
)

// Per-row destination element field ids (§7.13 field 23).
const (
	fpReparentRowDestinationBinding uint16 = 1
	fpReparentRowDestinationParent  uint16 = 2
	fpReparentRowDestinationSHA     uint16 = 3
)

// Per-row candidate element field ids (§7.13 field 25).
const (
	fpReparentRowCandidateDigest uint16 = 1
	fpReparentRowCandidateCount  uint16 = 2
)

// Metadata-delta element field ids (§7.13 field 26).
const (
	fpReparentMetaName              uint16 = 1
	fpReparentMetaBaseBefore        uint16 = 2
	fpReparentMetaBaseAfter         uint16 = 3
	fpReparentMetaLastBaseSHABefore uint16 = 4
	fpReparentMetaLastBaseSHAAfter  uint16 = 5
)

// Execution-identity element field ids (§7.13 field 32). The top-level field
// count stays 33 while schema v3 enriches existing structured fields.
const (
	fpReparentStrategyKind               uint16 = 1
	fpReparentStrategyComputationContext uint16 = 2
	fpReparentStrategyBackend            uint16 = 3
	fpReparentStrategyRefBackend         uint16 = 4
	fpReparentOriginalBranch             uint16 = 5
	fpReparentOriginalHead               uint16 = 6
	fpReparentOriginalDetached           uint16 = 7
	fpReparentHolderTuples               uint16 = 8
)

const (
	fpReparentHolderPath   uint16 = 1
	fpReparentHolderBranch uint16 = 2
	fpReparentHolderHead   uint16 = 3
	fpReparentHolderKind   uint16 = 4
	fpReparentHolderAction uint16 = 5
)

// Row-digest field ids (ReparentRevalidationDigest). The table starts fresh
// at 1 and is unrelated to the document table above.
const (
	fpReparentRevalGitBranch      uint16 = 1
	fpReparentRevalCutoffSHA      uint16 = 2
	fpReparentRevalHeadSHA        uint16 = 3
	fpReparentRevalBinding        uint16 = 4
	fpReparentRevalParent         uint16 = 5
	fpReparentRevalDestinationSHA uint16 = 6
	fpReparentRevalCandidateCount uint16 = 7
	fpReparentRevalCandidates     uint16 = 8
)

// ReparentPlanFingerprint computes approval.fingerprint: the SHA-256,
// rendered as exactly 64 lowercase hex characters, of the canonical tuple
// projected from plan. It trusts plan's arrays are already in closure order
// and never re-sorts them.
func ReparentPlanFingerprint(plan ReparentPlan) (string, error) {
	preimage, err := reparentFingerprintPreimage(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:]), nil
}

// ReparentPlanFingerprintPreimage returns the exact byte sequence
// ReparentPlanFingerprint hashes. It exists for tests, so the field-id table
// can be asserted directly; production code calls ReparentPlanFingerprint.
func ReparentPlanFingerprintPreimage(plan ReparentPlan) ([]byte, error) {
	return reparentFingerprintPreimage(plan)
}

func reparentFingerprintPreimage(plan ReparentPlan) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(reparentFingerprintPrefix)
	var version [2]byte
	binary.BigEndian.PutUint16(version[:], reparentFingerprintEncodingVersion)
	out.Write(version[:])
	binary.BigEndian.PutUint16(version[:], reparentFingerprintTupleSchemaVersion)
	out.Write(version[:])
	out.Write(tlvElement(tlvTagStruct, encodeReparentFingerprintDocument(plan)))
	return out.Bytes(), nil
}

// reparentFingerprintRows returns the closure in replay order: the target
// first, then every descendant, exactly as ReparentClosureOrder produced it.
func reparentFingerprintRows(plan ReparentPlan) []ReparentPlanRow {
	rows := make([]ReparentPlanRow, 0, 1+len(plan.Descendants))
	rows = append(rows, plan.Target)
	rows = append(rows, plan.Descendants...)
	return rows
}

func encodeReparentFingerprintDocument(plan ReparentPlan) []byte {
	rows := reparentFingerprintRows(plan)
	e := &tlvEncoder{}

	e.bytesValue(fpReparentWorkspaceMode, plan.Workspace.Mode)
	e.bytesPtr(fpReparentWorkspaceStableID, plan.Workspace.StableID)
	e.bytesValue(fpReparentFeature, plan.Feature)
	e.bytesValue(fpReparentRoute, plan.Route)

	e.bytesValue(fpReparentTargetName, plan.Target.Name)
	e.bytesValue(fpReparentTargetGitBranch, plan.Target.GitBranch)
	e.writeField(fpReparentTargetRepo, tlvTagStruct, encodeReparentTargetRepository(plan.Target))

	e.bytesPtr(fpReparentOldParentStoredToken, plan.Target.OldParent.StoredToken)
	e.bytesValue(fpReparentOldParentKind, plan.Target.OldParent.Kind)
	e.bytesPtr(fpReparentOldParentRef, plan.Target.OldParent.Ref)
	e.bytesPtr(fpReparentOldParentSHA, plan.Target.OldParent.SHA)

	e.bytesPtr(fpReparentNewParentStoredToken, plan.Target.NewParent.StoredToken)
	e.bytesValue(fpReparentNewParentKind, plan.Target.NewParent.Kind)
	e.bytesPtr(fpReparentNewParentRef, plan.Target.NewParent.Ref)
	e.bytesPtr(fpReparentNewParentSHA, plan.Target.NewParent.SHA)

	e.bytesValue(fpReparentOntoKindRequested, plan.Policy.OntoKindRequested)
	e.uintPtr(fpReparentOIDWidth, plan.Policy.OIDWidth)

	e.bytesPtr(fpReparentTargetCutoffSHA, plan.Target.Cutoff.ResolvedSHA)
	e.bytesValue(fpReparentTargetCutoffProvenance, plan.Target.Cutoff.Provenance)

	closure := make([][]byte, 0, len(rows))
	heads := make([][]byte, 0, len(rows))
	cutoffs := make([][]byte, 0, len(rows))
	destinations := make([][]byte, 0, len(rows))
	argvs := make([][]byte, 0, len(rows))
	candidates := make([][]byte, 0, len(rows))
	for _, row := range rows {
		closure = append(closure, tlvElement(tlvTagStruct, encodeReparentClosureRow(row)))
		heads = append(heads, tlvOptionalBytesElement(row.Head.SHA))
		cutoffs = append(cutoffs, tlvElement(tlvTagStruct, encodeReparentRowCutoff(row)))
		destinations = append(destinations, tlvElement(tlvTagStruct, encodeReparentRowDestination(row)))
		argvs = append(argvs, tlvElement(tlvTagArray, tlvConcat(tlvStringElements(row.Argv))))
		candidates = append(candidates, tlvElement(tlvTagStruct, encodeReparentRowCandidates(row)))
	}
	e.arrayValue(fpReparentClosure, closure)
	e.arrayValue(fpReparentRowHeads, heads)
	e.arrayValue(fpReparentRowCutoffs, cutoffs)
	e.arrayValue(fpReparentRowDestinations, destinations)
	e.arrayValue(fpReparentRowArgv, argvs)
	e.arrayValue(fpReparentRowCandidates, candidates)

	metadata := make([][]byte, 0, len(plan.MetadataDelta.Entries))
	for _, entry := range plan.MetadataDelta.Entries {
		metadata = append(metadata, tlvElement(tlvTagStruct, encodeReparentMetadataEntry(entry)))
	}
	e.arrayValue(fpReparentMetadataDelta, metadata)
	e.bytesValue(fpReparentStackSHA256Before, plan.MetadataDelta.StackSHA256Before)

	e.bytesValue(fpReparentFetchPolicy, plan.Policy.Fetch)
	e.uintPtr(fpReparentGuardLimitPerEntry, plan.Guard.Limits.MaxReplayPerEntry.Value)
	e.uintPtr(fpReparentGuardLimitTotal, plan.Guard.Limits.MaxReplayTotal.Value)
	e.bytesPtr(fpReparentValidationCommandDigest, plan.Policy.Validation.CommandDigest)

	e.writeField(fpReparentStrategy, tlvTagStruct, encodeReparentExecutionIdentity(plan))
	e.bytesValue(fpReparentApprovalScope, plan.Approval.Scope)

	return e.payload()
}

func encodeReparentClosureRow(row ReparentPlanRow) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpReparentClosureName, row.Name)
	e.bytesValue(fpReparentClosureGitBranch, row.GitBranch)
	order := row.Order
	e.uintPtr(fpReparentClosureOrder, &order)
	e.bytesValue(fpReparentClosureRepo, row.Repo)
	e.bytesPtr(fpReparentClosureContextID, row.ExecutionContext.ContextID)
	e.bytesPtr(fpReparentClosureRepoRoot, canonicalPathPtr(row.ExecutionContext.RepoRoot))
	e.bytesValue(fpReparentClosureSource, row.ExecutionContext.Source)
	return e.payload()
}

func encodeReparentTargetRepository(row ReparentPlanRow) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpReparentTargetRepoToken, row.Repo)
	e.bytesPtr(fpReparentTargetContextID, row.ExecutionContext.ContextID)
	e.bytesPtr(fpReparentTargetRepoRoot, canonicalPathPtr(row.ExecutionContext.RepoRoot))
	e.bytesValue(fpReparentTargetContextSource, row.ExecutionContext.Source)
	e.bytesPtr(fpReparentTargetRequestedToken, row.NewParent.RequestedToken)
	return e.payload()
}

func encodeReparentRowCutoff(row ReparentPlanRow) []byte {
	e := &tlvEncoder{}
	e.bytesPtr(fpReparentRowCutoffSHA, row.Cutoff.ResolvedSHA)
	e.bytesValue(fpReparentRowCutoffProvenance, row.Cutoff.Provenance)
	e.bytesPtr(fpReparentRowCutoffSupplied, row.Cutoff.SuppliedToken)
	return e.payload()
}

func encodeReparentRowDestination(row ReparentPlanRow) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpReparentRowDestinationBinding, row.DestinationBinding)
	e.bytesPtr(fpReparentRowDestinationParent, row.DestinationParent)
	// An explicit NULL for every parent-computed row: the commit does not
	// exist yet, on a plan route or at admission, and a guess would be a lie
	// the fingerprint then froze.
	e.bytesPtr(fpReparentRowDestinationSHA, row.DestinationSHA)
	return e.payload()
}

func encodeReparentRowCandidates(row ReparentPlanRow) []byte {
	e := &tlvEncoder{}
	e.bytesPtr(fpReparentRowCandidateDigest, row.Replay.CandidateDigest)
	e.uintPtr(fpReparentRowCandidateCount, row.Replay.CandidateCount)
	return e.payload()
}

func encodeReparentMetadataEntry(entry ReparentPlanMetadataEntry) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpReparentMetaName, entry.Name)
	e.bytesValue(fpReparentMetaBaseBefore, entry.BaseBefore)
	e.bytesValue(fpReparentMetaBaseAfter, entry.BaseAfter)
	e.bytesPtr(fpReparentMetaLastBaseSHABefore, entry.LastBaseSHABefore)
	e.bytesPtr(fpReparentMetaLastBaseSHAAfter, entry.LastBaseSHAAfter)
	return e.payload()
}

func encodeReparentExecutionIdentity(plan ReparentPlan) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpReparentStrategyKind, plan.Strategy.Kind)
	e.bytesValue(fpReparentStrategyComputationContext, plan.Strategy.ComputationContext)
	e.bytesValue(fpReparentStrategyBackend, plan.Strategy.Backend)
	e.bytesValue(fpReparentStrategyRefBackend, plan.Strategy.Atomicity.RefBackend)
	e.bytesPtr(fpReparentOriginalBranch, plan.Holders.OriginalBranch)
	e.bytesPtr(fpReparentOriginalHead, plan.Holders.OriginalHead)
	e.boolValue(fpReparentOriginalDetached, plan.Holders.OriginalDetached)
	holders := append([]ReparentPlanHolder{}, plan.Holders.Rows...)
	sort.SliceStable(holders, func(i, j int) bool {
		return reparentHolderFingerprintKey(holders[i]) < reparentHolderFingerprintKey(holders[j])
	})
	elements := make([][]byte, 0, len(holders))
	for _, holder := range holders {
		elements = append(elements, tlvElement(tlvTagStruct, encodeReparentHolderIdentity(holder)))
	}
	e.arrayValue(fpReparentHolderTuples, elements)
	return e.payload()
}

func encodeReparentHolderIdentity(holder ReparentPlanHolder) []byte {
	e := &tlvEncoder{}
	e.bytesPtr(fpReparentHolderPath, canonicalPathPtr(holder.HolderPath))
	e.bytesValue(fpReparentHolderBranch, holder.GitBranch)
	e.bytesPtr(fpReparentHolderHead, holder.PreimageSHA)
	e.bytesValue(fpReparentHolderKind, holder.HolderKind)
	e.bytesValue(fpReparentHolderAction, holder.Action)
	return e.payload()
}

func reparentHolderFingerprintKey(holder ReparentPlanHolder) string {
	return strings.Join([]string{
		derefString(canonicalPathPtr(holder.HolderPath)),
		holder.GitBranch,
		derefString(holder.PreimageSHA),
		holder.HolderKind,
		holder.Action,
	}, "\x00")
}

func canonicalPathPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clean := filepath.Clean(*value)
	return &clean
}

// tlvStringElements frames each string as a positional BYTES array element.
func tlvStringElements(values []string) [][]byte {
	out := make([][]byte, 0, len(values))
	for _, v := range values {
		out = append(out, tlvElement(tlvTagBytes, []byte(v)))
	}
	return out
}

// tlvOptionalBytesElement frames a nullable string as one positional array
// element: BYTES when present, an explicit NULL element when absent.
func tlvOptionalBytesElement(v *string) []byte {
	if v == nil {
		return tlvElement(tlvTagNull, nil)
	}
	return tlvElement(tlvTagBytes, []byte(*v))
}

// ============================================================================
// ReparentRevalidationDigest — the reparent-owned JIT row identity
//
// The shipped RevalidationDigest takes a PlanEntry, which §7.2 forbids
// reusing, and hashes sync-only row members. It stays untouched with every
// caller; this is the reparent twin, over reparent row facts only.
// ============================================================================

// ReparentRevalidationDigest hashes one row's canonical candidate inputs —
// git branch, cutoff SHA, pre-image head SHA, destination binding,
// destination parent, destination SHA (an explicit NULL when deferred),
// replay candidate count and the ordered candidate OIDs — through the same
// TLV framing, under the reparent domain.
//
// A just-in-time seam computes it once over the approved row and once over a
// freshly re-probed row; any difference is a revalidation mismatch. The
// deferred destination SHA is bound as NULL on both sides rather than
// omitted, so a descendant row is comparable before its parent has computed.
func ReparentRevalidationDigest(row ReparentPlanRow) (string, error) {
	sum := sha256.Sum256(reparentRevalidationPreimage(row))
	return hex.EncodeToString(sum[:]), nil
}

func reparentRevalidationPreimage(row ReparentPlanRow) []byte {
	var out bytes.Buffer
	out.WriteString(reparentFingerprintPrefix)
	var version [2]byte
	binary.BigEndian.PutUint16(version[:], reparentFingerprintEncodingVersion)
	out.Write(version[:])
	binary.BigEndian.PutUint16(version[:], reparentRevalidationTupleSchemaVersion)
	out.Write(version[:])
	out.Write(tlvElement(tlvTagStruct, encodeReparentRevalidationRow(row)))
	return out.Bytes()
}

func encodeReparentRevalidationRow(row ReparentPlanRow) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpReparentRevalGitBranch, row.GitBranch)
	e.bytesPtr(fpReparentRevalCutoffSHA, row.Cutoff.ResolvedSHA)
	e.bytesPtr(fpReparentRevalHeadSHA, row.Head.SHA)
	e.bytesValue(fpReparentRevalBinding, row.DestinationBinding)
	e.bytesPtr(fpReparentRevalParent, row.DestinationParent)
	e.bytesPtr(fpReparentRevalDestinationSHA, row.DestinationSHA)
	e.uintPtr(fpReparentRevalCandidateCount, row.Replay.CandidateCount)
	// The candidate list keeps the shipped nullable-array distinction:
	// "not probed" (NULL) is not "probed, empty" ([]).
	e.arrayPtr(fpReparentRevalCandidates, row.Replay.Commits != nil, tlvStringElements(row.Replay.Commits))
	return e.payload()
}
