package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"unicode/utf8"
)

// ============================================================================
// TLV encoding (§8.2) — typed, length-prefixed, raw-byte.
//
// Wire shape of one struct member: [2-byte big-endian id][1-byte type tag]
// [4-byte big-endian length][length bytes of payload]. Wire shape of one
// array element (no id — array membership is positional): [1-byte type tag]
// [4-byte big-endian length][length bytes of payload]. This framing is this
// implementation's own choice (§8.2 mandates the TYPED, LENGTH-PREFIXED,
// RAW-BYTE properties and the six type tags below; it does not fix a byte
// width for ids/lengths, because — per §8.3 — no binary has ever emitted a
// tuple_schema_version 0x0004 pre-image before this implementation).
// ============================================================================

const (
	tlvTagNull   byte = 0x00
	tlvTagBool   byte = 0x01
	tlvTagUint   byte = 0x02
	tlvTagBytes  byte = 0x03
	tlvTagStruct byte = 0x04
	tlvTagArray  byte = 0x05
)

// tlvElement frames one array element or root value: tag + length + payload,
// with no id (array elements are positional; a root value belongs to no
// enclosing struct).
func tlvElement(tag byte, payload []byte) []byte {
	var out bytes.Buffer
	out.WriteByte(tag)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	out.Write(length[:])
	out.Write(payload)
	return out.Bytes()
}

func tlvConcat(elements [][]byte) []byte {
	var out bytes.Buffer
	for _, el := range elements {
		out.Write(el)
	}
	return out.Bytes()
}

// tlvEncoder accumulates one STRUCT's members. Callers MUST write every
// declared id for that struct, in ascending order, exactly once each — an
// absent or unknown member is written as NULL, never omitted (§8.2).
type tlvEncoder struct {
	buf bytes.Buffer
}

func (e *tlvEncoder) writeField(id uint16, tag byte, payload []byte) {
	var idBuf [2]byte
	binary.BigEndian.PutUint16(idBuf[:], id)
	e.buf.Write(idBuf[:])
	e.buf.WriteByte(tag)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	e.buf.Write(length[:])
	e.buf.Write(payload)
}

// payload returns the accumulated struct payload (the bytes that go inside a
// STRUCT-tagged tlvElement, or the top-level root's payload).
func (e *tlvEncoder) payload() []byte { return e.buf.Bytes() }

// null writes an explicit NULL member.
func (e *tlvEncoder) null(id uint16) { e.writeField(id, tlvTagNull, nil) }

// boolValue writes a non-nullable bool member.
func (e *tlvEncoder) boolValue(id uint16, v bool) {
	p := byte(0)
	if v {
		p = 1
	}
	e.writeField(id, tlvTagBool, []byte{p})
}

// boolPtr writes a nullable bool member: NULL when v is nil.
func (e *tlvEncoder) boolPtr(id uint16, v *bool) {
	if v == nil {
		e.null(id)
		return
	}
	e.boolValue(id, *v)
}

// uintPtr writes a nullable non-negative integer member: NULL when v is nil.
// UINT 0 and NULL are distinct TLV type tags, so a zero count never collapses
// into "absent" (§8.2).
func (e *tlvEncoder) uintPtr(id uint16, v *int) {
	if v == nil {
		e.null(id)
		return
	}
	var p [8]byte
	binary.BigEndian.PutUint64(p[:], uint64(*v))
	e.writeField(id, tlvTagUint, p[:])
}

// bytesValue writes a non-nullable string member. Raw bytes are bound
// verbatim: no UTF-8 replacement, no normalisation, no case folding (§8.2).
// An empty string still writes a zero-length BYTES payload, distinct from
// NULL by type tag.
func (e *tlvEncoder) bytesValue(id uint16, v string) {
	e.writeField(id, tlvTagBytes, []byte(v))
}

// bytesPtr writes a nullable string member: NULL when v is nil.
func (e *tlvEncoder) bytesPtr(id uint16, v *string) {
	if v == nil {
		e.null(id)
		return
	}
	e.bytesValue(id, *v)
}

// arrayValue writes a non-nullable ARRAY member (the eighteen never-null
// document arrays, and the two never-nil per-document arrays of this table:
// repositories[], push.targets[], entries[]). elements are already-framed
// tlvElement values; nil/empty produces a zero-length, present ARRAY.
func (e *tlvEncoder) arrayValue(id uint16, elements [][]byte) {
	e.writeField(id, tlvTagArray, tlvConcat(elements))
}

// arrayPtr writes a nullable ARRAY member: NULL when present is false. Used
// for the "nullable array — exception" fields (§4.7), where the document
// itself distinguishes "probing failed" (null) from "known, possibly empty"
// ([]).
func (e *tlvEncoder) arrayPtr(id uint16, present bool, elements [][]byte) {
	if !present {
		e.null(id)
		return
	}
	e.arrayValue(id, elements)
}

// ============================================================================
// §8.1 pre-image framing (fixed)
// ============================================================================

const (
	planFingerprintPrefix             = "tws-plan-fp\x00"
	planFingerprintEncodingVersion    = 0x0001
	planFingerprintTupleSchemaVersion = 0x0004 // §8.3: unchanged by the workspace.repo_root removal
)

// ============================================================================
// Document-level field-id table (§8.3 "Included, document level").
//
// Id 3 is RETIRED: it is the position workspace.repo_root would have
// occupied immediately after mode (1) and stable_id (2), and it is never
// reused for a different member (§8.3) — rendered repo_root values MUST NOT
// appear as standalone tuple bytes (§8.2), so no repo_root VALUE is ever
// written there. The id itself is still emitted, as an explicit TLV NULL, so
// the pre-image keeps a fixed field sequence and a document minted before
// the retirement cannot collide with one minted after it.
// ============================================================================

const (
	fpDocWorkspaceMode            uint16 = 1
	fpDocWorkspaceStableID        uint16 = 2
	fpDocWorkspaceRepoRootRetired uint16 = 3 // retired; emitted as an explicit NULL, never a value
	fpDocFeature                  uint16 = 4
	fpDocRoute                    uint16 = 5
	fpDocPolicyFetch              uint16 = 6
	fpDocPolicyPropagation        uint16 = 7
	fpDocPolicyScopeKind          uint16 = 8
	fpDocPolicySelector           uint16 = 9
	fpDocGuardLimitPerEntry       uint16 = 10
	fpDocGuardLimitTotal          uint16 = 11
	fpDocIntentPush               uint16 = 12
	fpDocValidationApplies        uint16 = 13
	fpDocValidationCommandDigest  uint16 = 14
	fpDocRestoreApplies           uint16 = 15
	fpDocRestoreWillSwitchHead    uint16 = 16
	fpDocRestoreTargetBranch      uint16 = 17
	fpDocRestoreTargetSHA         uint16 = 18
	fpDocRestorePushDropped       uint16 = 19
	fpDocApprovalScope            uint16 = 20
	fpDocRepositories             uint16 = 21
	fpDocPushTargets              uint16 = 22
	fpDocEntries                  uint16 = 23
)

// Repository-row field-id table (§8.3: repo, context_id, each config slot's
// effective {status, value}, plus rebase_merges.interpretation).
const (
	fpRepoRepo                       uint16 = 1
	fpRepoContextID                  uint16 = 2
	fpRepoUpdateRefsStatus           uint16 = 3
	fpRepoUpdateRefsValue            uint16 = 4
	fpRepoRebaseMergesStatus         uint16 = 5
	fpRepoRebaseMergesValue          uint16 = 6
	fpRepoRebaseMergesInterpretation uint16 = 7
	fpRepoBackendStatus              uint16 = 8
	fpRepoBackendValue               uint16 = 9
	fpRepoAutoStashStatus            uint16 = 10
	fpRepoAutoStashValue             uint16 = 11
)

// Push-target-row field-id table (§8.3: repo, context_id, git_branch, remote,
// ref, force, scope, lease expectation/ref/sha).
const (
	fpPushRepo             uint16 = 1
	fpPushContextID        uint16 = 2
	fpPushGitBranch        uint16 = 3
	fpPushRemote           uint16 = 4
	fpPushRef              uint16 = 5
	fpPushForce            uint16 = 6
	fpPushScope            uint16 = 7
	fpPushLeaseExpectation uint16 = 8
	fpPushLeaseExpectedRef uint16 = 9
	fpPushLeaseExpectedSHA uint16 = 10
)

// Collateral-ref-tuple field-id table (§8.3: {repo, ref, sha, stack_owned}).
const (
	fpCollateralRepo       uint16 = 1
	fpCollateralRef        uint16 = 2
	fpCollateralSHA        uint16 = 3
	fpCollateralStackOwned uint16 = 4
)

// Per-entry field-id table (§8.3 "Included, per entry"). This is a separate,
// entry-local namespace, freshly used for every entries[] element; it is not
// scoped by, or shared with, the document-level table above. The three fixed
// anchors §8.2 gives (execution_context.context_id at 4, base_context.
// context_id at 36, base.decision_sha at 38) are honoured exactly; every
// other id is this implementation's own assignment, since — per §8.3 — no
// binary has ever emitted a tuple_schema_version 0x0004 pre-image before this
// one.
const (
	fpEntryName                       uint16 = 1
	fpEntryGitBranch                  uint16 = 2
	fpEntryRepo                       uint16 = 3
	fpEntryExecutionContextID         uint16 = 4 // fixed anchor, §8.2
	fpEntryExecutionContextSource     uint16 = 5
	fpEntryRole                       uint16 = 6
	fpEntryMaterialization            uint16 = 7
	fpEntryHeadState                  uint16 = 8
	fpEntryHeadSHA                    uint16 = 9
	fpEntryBaseName                   uint16 = 10
	fpEntryBaseRef                    uint16 = 11
	fpEntryDestinationSHA             uint16 = 12
	fpEntryDestinationSnapshotSHA     uint16 = 13
	fpEntryDestinationDeferred        uint16 = 14
	fpEntryDestinationDependsOn       uint16 = 15
	fpEntryCutoffRecordedSHA          uint16 = 16
	fpEntryCutoffState                uint16 = 17
	fpEntryReplayDeterminacy          uint16 = 18
	fpEntryReplayReason               uint16 = 19
	fpEntryReplayUpstreamProvenance   uint16 = 20
	fpEntryReplayUpstreamSHA          uint16 = 21
	fpEntryReplayCandidateCount       uint16 = 22
	fpEntryReplayCandidateDigest      uint16 = 23
	fpEntryStrategy                   uint16 = 24
	fpEntryArgvAlternatives           uint16 = 25
	fpEntryEffectiveBackend           uint16 = 26
	fpEntryMutationWillSwitchHead     uint16 = 27
	fpEntryMutationWillLeaveHeadOn    uint16 = 28
	fpEntryMutationHeadRestoredByRun  uint16 = 29
	fpEntryContextDirty               uint16 = 30
	fpEntryContextAutostash           uint16 = 31
	fpEntryContextAutostashAppliesArm uint16 = 32
	fpEntryBranchCheckedOutAt         uint16 = 33
	fpEntryCollateralMechanism        uint16 = 34
	fpEntryCollateralExposed          uint16 = 35
	fpEntryBaseContextID              uint16 = 36 // fixed anchor, §8.2
	fpEntryBaseContextSource          uint16 = 37
	fpEntryBaseDecisionSHA            uint16 = 38 // fixed anchor, §8.2
	fpEntryCollateralKnown            uint16 = 39
	fpEntryCollateralRefs             uint16 = 40
)

// ============================================================================
// PlanFingerprint (§8.1) — the approval-scope, cross-invocation identity.
// ============================================================================

// PlanFingerprint computes approval.fingerprint: the SHA-256, rendered as 64
// lowercase hex, of the canonical invariant tuple projected from plan per
// §8.1-§8.3. It trusts that plan's canonically-ordered arrays (entries,
// repositories, push.targets and each entry's collateral_refs) are already in
// §4.8 order — the same trust MarshalRebasePlan and FormatRebasePlan place in
// their input — and does not re-sort them.
func PlanFingerprint(plan RebasePlan) (string, error) {
	preimage, err := planFingerprintPreimage(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:]), nil
}

// PlanFingerprintPreimage returns the exact byte sequence PlanFingerprint
// hashes: the "tws-plan-fp\0" prefix, the two fixed 2-byte version fields, and
// the length-framed TLV root STRUCT documented above. It exists for tests
// only, so the field-id tables above can be asserted against directly; it
// performs no work PlanFingerprint doesn't already perform, and production
// code MUST call PlanFingerprint, never this accessor, to obtain a token.
func PlanFingerprintPreimage(plan RebasePlan) ([]byte, error) {
	return planFingerprintPreimage(plan)
}

func planFingerprintPreimage(plan RebasePlan) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString(planFingerprintPrefix)
	var version [2]byte
	binary.BigEndian.PutUint16(version[:], planFingerprintEncodingVersion)
	out.Write(version[:])
	binary.BigEndian.PutUint16(version[:], planFingerprintTupleSchemaVersion)
	out.Write(version[:])
	out.Write(tlvElement(tlvTagStruct, encodeFingerprintDocument(plan)))
	return out.Bytes(), nil
}

func encodeFingerprintDocument(plan RebasePlan) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpDocWorkspaceMode, plan.Workspace.Mode)
	e.bytesPtr(fpDocWorkspaceStableID, plan.Workspace.StableID)
	e.null(fpDocWorkspaceRepoRootRetired) // retired id: an explicit NULL holds the position; repo_root is never a tuple VALUE
	e.bytesValue(fpDocFeature, plan.Feature)
	e.bytesValue(fpDocRoute, plan.Route)
	e.bytesValue(fpDocPolicyFetch, plan.Policy.Fetch)
	e.bytesValue(fpDocPolicyPropagation, plan.Policy.Propagation)
	e.bytesValue(fpDocPolicyScopeKind, plan.Policy.ScopeKind)
	e.bytesPtr(fpDocPolicySelector, plan.Policy.Selector)
	e.uintPtr(fpDocGuardLimitPerEntry, plan.Guard.Limits.MaxReplayPerEntry.Value)
	e.uintPtr(fpDocGuardLimitTotal, plan.Guard.Limits.MaxReplayTotal.Value)
	e.boolValue(fpDocIntentPush, plan.Intent.Push)
	e.boolValue(fpDocValidationApplies, plan.Intent.Validation.Applies)
	e.bytesPtr(fpDocValidationCommandDigest, plan.Intent.Validation.CommandDigest)
	e.boolValue(fpDocRestoreApplies, plan.Restore.Applies)
	e.boolPtr(fpDocRestoreWillSwitchHead, plan.Restore.WillSwitchHead)
	e.bytesPtr(fpDocRestoreTargetBranch, plan.Restore.TargetBranch)
	e.bytesPtr(fpDocRestoreTargetSHA, plan.Restore.TargetSHA)
	e.boolValue(fpDocRestorePushDropped, plan.Restore.PushDropped)
	e.bytesValue(fpDocApprovalScope, plan.Approval.Scope)

	repoElements := make([][]byte, 0, len(plan.Repositories))
	for _, repo := range plan.Repositories {
		repoElements = append(repoElements, tlvElement(tlvTagStruct, encodeFingerprintRepository(repo)))
	}
	e.arrayValue(fpDocRepositories, repoElements)

	targetElements := make([][]byte, 0, len(plan.Push.Targets))
	for _, target := range plan.Push.Targets {
		targetElements = append(targetElements, tlvElement(tlvTagStruct, encodeFingerprintPushTarget(target)))
	}
	e.arrayValue(fpDocPushTargets, targetElements)

	entryElements := make([][]byte, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		entryElements = append(entryElements, tlvElement(tlvTagStruct, encodeFingerprintEntry(entry)))
	}
	e.arrayValue(fpDocEntries, entryElements)

	return e.payload()
}

func encodeFingerprintRepository(repo PlanRepository) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpRepoRepo, repo.Repo)
	e.bytesValue(fpRepoContextID, repo.ContextID)
	e.bytesValue(fpRepoUpdateRefsStatus, repo.Config.UpdateRefs.Status)
	e.bytesPtr(fpRepoUpdateRefsValue, repo.Config.UpdateRefs.Value)
	e.bytesValue(fpRepoRebaseMergesStatus, repo.Config.RebaseMerges.Status)
	e.bytesPtr(fpRepoRebaseMergesValue, repo.Config.RebaseMerges.Value)
	e.bytesValue(fpRepoRebaseMergesInterpretation, repo.Config.RebaseMerges.Interpretation)
	e.bytesValue(fpRepoBackendStatus, repo.Config.Backend.Status)
	e.bytesPtr(fpRepoBackendValue, repo.Config.Backend.Value)
	e.bytesValue(fpRepoAutoStashStatus, repo.Config.AutoStash.Status)
	e.bytesPtr(fpRepoAutoStashValue, repo.Config.AutoStash.Value)
	return e.payload()
}

func encodeFingerprintPushTarget(target PlanPushTarget) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpPushRepo, target.Repo)
	e.bytesPtr(fpPushContextID, target.ContextID)
	e.bytesValue(fpPushGitBranch, target.GitBranch)
	e.bytesValue(fpPushRemote, target.Remote)
	e.bytesValue(fpPushRef, target.Ref)
	e.bytesValue(fpPushForce, target.Force)
	e.bytesValue(fpPushScope, target.Scope)
	e.bytesValue(fpPushLeaseExpectation, target.Lease.Expectation)
	e.bytesPtr(fpPushLeaseExpectedRef, target.Lease.ExpectedRef)
	e.bytesPtr(fpPushLeaseExpectedSHA, target.Lease.ExpectedSHA)
	return e.payload()
}

func encodeFingerprintCollateralRef(ref PlanCollateralRef) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpCollateralRepo, ref.Repo)
	e.bytesValue(fpCollateralRef, ref.Ref)
	e.bytesValue(fpCollateralSHA, ref.SHA)
	e.boolValue(fpCollateralStackOwned, ref.StackOwned)
	return e.payload()
}

func encodeFingerprintEntry(entry PlanEntry) []byte {
	e := &tlvEncoder{}
	e.bytesValue(fpEntryName, entry.Name)
	e.bytesValue(fpEntryGitBranch, entry.GitBranch)
	e.bytesValue(fpEntryRepo, entry.Repo)
	e.bytesPtr(fpEntryExecutionContextID, entry.ExecutionContext.ContextID)
	e.bytesValue(fpEntryExecutionContextSource, entry.ExecutionContext.Source)
	e.bytesValue(fpEntryRole, entry.Role)
	e.bytesValue(fpEntryMaterialization, entry.Materialization)
	e.bytesPtr(fpEntryHeadState, entry.Head.State)
	e.bytesPtr(fpEntryHeadSHA, entry.Head.SHA)
	e.bytesPtr(fpEntryBaseName, entry.Base.Name)
	e.bytesPtr(fpEntryBaseRef, entry.Base.Ref)
	e.bytesPtr(fpEntryDestinationSHA, entry.Destination.SHA)
	e.bytesPtr(fpEntryDestinationSnapshotSHA, entry.Destination.SnapshotSHA)
	e.boolValue(fpEntryDestinationDeferred, entry.Destination.Deferred)
	e.bytesPtr(fpEntryDestinationDependsOn, entry.Destination.DependsOn)
	e.bytesPtr(fpEntryCutoffRecordedSHA, entry.Cutoff.RecordedSHA)
	e.bytesPtr(fpEntryCutoffState, entry.Cutoff.State)
	e.bytesValue(fpEntryReplayDeterminacy, entry.Replay.Determinacy)
	e.bytesPtr(fpEntryReplayReason, entry.Replay.Reason)
	e.bytesValue(fpEntryReplayUpstreamProvenance, entry.Replay.UpstreamProvenance)
	e.bytesPtr(fpEntryReplayUpstreamSHA, entry.Replay.UpstreamSHA)
	e.uintPtr(fpEntryReplayCandidateCount, entry.Replay.CandidateCount)
	e.bytesPtr(fpEntryReplayCandidateDigest, entry.Replay.CandidateDigest)
	e.bytesValue(fpEntryStrategy, entry.Strategy)
	e.arrayPtr(fpEntryArgvAlternatives, entry.ArgvAlternatives != nil, tlvArgvAlternativesElements(entry.ArgvAlternatives))
	e.bytesPtr(fpEntryEffectiveBackend, entry.EffectiveBackend)
	e.boolValue(fpEntryMutationWillSwitchHead, entry.Mutation.WillSwitchHead)
	e.bytesPtr(fpEntryMutationWillLeaveHeadOn, entry.Mutation.WillLeaveHeadOn)
	e.boolValue(fpEntryMutationHeadRestoredByRun, entry.Mutation.HeadRestoredByRun)
	e.boolPtr(fpEntryContextDirty, entry.Context.Dirty)
	e.boolPtr(fpEntryContextAutostash, entry.Context.Autostash)
	e.boolPtr(fpEntryContextAutostashAppliesArm, entry.Context.AutostashAppliesToThisArm)
	e.bytesPtr(fpEntryBranchCheckedOutAt, entry.BranchCheckedOutAt)
	e.bytesPtr(fpEntryCollateralMechanism, entry.CollateralMechanism)
	e.boolPtr(fpEntryCollateralExposed, entry.CollateralExposed)
	e.bytesPtr(fpEntryBaseContextID, entry.BaseContext.ContextID)
	e.bytesValue(fpEntryBaseContextSource, entry.BaseContext.Source)
	e.bytesPtr(fpEntryBaseDecisionSHA, entry.Base.DecisionSHA)
	e.boolValue(fpEntryCollateralKnown, entry.CollateralRefs != nil)
	collateralElements := make([][]byte, 0, len(entry.CollateralRefs))
	for _, ref := range entry.CollateralRefs {
		collateralElements = append(collateralElements, tlvElement(tlvTagStruct, encodeFingerprintCollateralRef(ref)))
	}
	e.arrayPtr(fpEntryCollateralRefs, entry.CollateralRefs != nil, collateralElements)
	return e.payload()
}

// tlvArgvAlternativesElements frames the two argv_alternatives arms, each as
// its own nested ARRAY-of-BYTES element. Returns nil when v is nil; the
// caller's arrayPtr call already turns that into a NULL member.
func tlvArgvAlternativesElements(v *[2][]string) [][]byte {
	if v == nil {
		return nil
	}
	elements := make([][]byte, 0, 2)
	for _, argv := range v {
		tokens := make([][]byte, 0, len(argv))
		for _, tok := range argv {
			tokens = append(tokens, tlvElement(tlvTagBytes, []byte(tok)))
		}
		elements = append(elements, tlvElement(tlvTagArray, tlvConcat(tokens)))
	}
	return elements
}

// ============================================================================
// RevalidationDigest (§10.3, §10.4) — the intra-run JIT identity.
//
// This binds exactly the "mutable Git plan facts" §10.3 names as rank 9
// revalidation-mismatch's domain: destination and head SHAs, upstream,
// candidate identity and digest, collateral membership, strategy shape,
// holders, in-progress rebases. This set is deliberately independent of
// PlanFingerprint's §8.3 tuple — e.g. context.rebase_in_progress is a
// revalidation member but a fingerprint non-member, and name/git_branch/
// role/materialization are fingerprint members but never drift at a JIT seam
// so they are not revalidation members — so it has its own field-id table,
// starting fresh at 1, entirely unrelated to the fingerprint tables above.
// ============================================================================

const (
	fpRevalDestinationSHA         uint16 = 1
	fpRevalDestinationSnapshotSHA uint16 = 2
	fpRevalDestinationDeferred    uint16 = 3
	fpRevalDestinationDependsOn   uint16 = 4
	fpRevalHeadState              uint16 = 5
	fpRevalHeadSHA                uint16 = 6
	fpRevalUpstreamSHA            uint16 = 7
	fpRevalDeterminacy            uint16 = 8
	fpRevalCandidateCount         uint16 = 9
	fpRevalFirstCandidateSHA      uint16 = 10
	fpRevalCandidateDigest        uint16 = 11
	fpRevalStrategy               uint16 = 12
	fpRevalArgvAlternatives       uint16 = 13
	fpRevalCollateralMechanism    uint16 = 14
	fpRevalCollateralExposed      uint16 = 15
	fpRevalCollateralRefs         uint16 = 16
	fpRevalBranchCheckedOutAt     uint16 = 17
	fpRevalRebaseInProgress       uint16 = 18
)

// RevalidationDigest computes revalidation.digest for one entry: the SHA-256,
// rendered as 64 lowercase hex, of the canonical TLV encoding of that row's
// mutable Git plan facts (§10.3). A JIT seam calls this once over the
// approved entry and once over a freshly re-probed entry; any difference is
// rank 9 revalidation-mismatch.
func RevalidationDigest(entry PlanEntry) (string, error) {
	sum := sha256.Sum256(encodeRevalidationEntry(entry))
	return hex.EncodeToString(sum[:]), nil
}

func encodeRevalidationEntry(entry PlanEntry) []byte {
	e := &tlvEncoder{}
	e.bytesPtr(fpRevalDestinationSHA, entry.Destination.SHA)
	e.bytesPtr(fpRevalDestinationSnapshotSHA, entry.Destination.SnapshotSHA)
	e.boolValue(fpRevalDestinationDeferred, entry.Destination.Deferred)
	e.bytesPtr(fpRevalDestinationDependsOn, entry.Destination.DependsOn)
	e.bytesPtr(fpRevalHeadState, entry.Head.State)
	e.bytesPtr(fpRevalHeadSHA, entry.Head.SHA)
	e.bytesPtr(fpRevalUpstreamSHA, entry.Replay.UpstreamSHA)
	e.bytesValue(fpRevalDeterminacy, entry.Replay.Determinacy)
	e.uintPtr(fpRevalCandidateCount, entry.Replay.CandidateCount)
	var firstCandidateSHA *string
	if entry.Replay.FirstCandidate != nil {
		sha := entry.Replay.FirstCandidate.SHA
		firstCandidateSHA = &sha
	}
	e.bytesPtr(fpRevalFirstCandidateSHA, firstCandidateSHA)
	e.bytesPtr(fpRevalCandidateDigest, entry.Replay.CandidateDigest)
	e.bytesValue(fpRevalStrategy, entry.Strategy)
	e.arrayPtr(fpRevalArgvAlternatives, entry.ArgvAlternatives != nil, tlvArgvAlternativesElements(entry.ArgvAlternatives))
	e.bytesPtr(fpRevalCollateralMechanism, entry.CollateralMechanism)
	e.boolPtr(fpRevalCollateralExposed, entry.CollateralExposed)
	collateralElements := make([][]byte, 0, len(entry.CollateralRefs))
	for _, ref := range entry.CollateralRefs {
		collateralElements = append(collateralElements, tlvElement(tlvTagStruct, encodeFingerprintCollateralRef(ref)))
	}
	e.arrayPtr(fpRevalCollateralRefs, entry.CollateralRefs != nil, collateralElements)
	e.bytesPtr(fpRevalBranchCheckedOutAt, entry.BranchCheckedOutAt)
	e.boolPtr(fpRevalRebaseInProgress, entry.Context.RebaseInProgress)
	return e.payload()
}

// ============================================================================
// encoding_issues[] (§4.4, §7.1 rank 5.08) — the identity-encoding probe
// ============================================================================

// encodingCandidate is one fingerprint-BOUND identity or path member the
// encoding probe inspects. Only bound members are inspected: rank 5.08 is a
// fingerprint-encoding fact ("bound identity bytes are invalid UTF-8 on a
// document that would mint or was given a token"), so a member no tuple
// binds cannot make a token unmintable and must not manufacture a row.
type encodingCandidate struct {
	fieldID    uint16
	pathLabel  string
	ownerEntry *string
	value      string
}

// CollectPlanEncodingIssues inspects every fingerprint-bound identity/path
// member of an assembled document for invalid UTF-8 and returns the closed
// six-member encoding_issues[] rows §4.4 defines, sorted by
// (field_id, path_label_ascii, owner_entry [null first], sha256) with exact
// duplicate rows removed. The array is never nil.
//
// The bytes are bound verbatim by the TLV encoder — no replacement, no
// normalisation — so a branch, ref, logical name, repo token or sanitized
// path carrying invalid UTF-8 really does enter the pre-image, which is
// exactly the condition §8.4 clause 3 refuses to mint a token over. Row
// members are ASCII-only by construction: path_label_ascii and
// owner_entry_ascii_or_null are ASCII-sanitized, and the offending bytes
// travel only as raw_base64 plus their SHA-256.
func CollectPlanEncodingIssues(plan RebasePlan) []PlanEncodingIssue {
	var cands []encodingCandidate
	add := func(id uint16, label string, owner *string, v string) {
		cands = append(cands, encodingCandidate{fieldID: id, pathLabel: label, ownerEntry: owner, value: v})
	}
	addPtr := func(id uint16, label string, owner *string, v *string) {
		if v != nil {
			add(id, label, owner, *v)
		}
	}

	add(fpDocFeature, "feature", nil, plan.Feature)
	addPtr(fpDocPolicySelector, "policy.selector", nil, plan.Policy.Selector)
	addPtr(fpDocRestoreTargetBranch, "restore.target_branch", nil, plan.Restore.TargetBranch)

	for _, repo := range plan.Repositories {
		add(fpRepoRepo, "repositories[].repo", nil, repo.Repo)
	}
	for i := range plan.Push.Targets {
		t := plan.Push.Targets[i]
		add(fpPushRepo, "push.targets[].repo", nil, t.Repo)
		add(fpPushGitBranch, "push.targets[].git_branch", nil, t.GitBranch)
		add(fpPushRemote, "push.targets[].remote", nil, t.Remote)
		add(fpPushRef, "push.targets[].ref", nil, t.Ref)
		addPtr(fpPushLeaseExpectedRef, "push.targets[].lease.expected_ref", nil, t.Lease.ExpectedRef)
	}
	for i := range plan.Entries {
		e := plan.Entries[i]
		owner := e.Name
		ownerPtr := &owner
		add(fpEntryName, "entries[].name", ownerPtr, e.Name)
		add(fpEntryGitBranch, "entries[].git_branch", ownerPtr, e.GitBranch)
		add(fpEntryRepo, "entries[].repo", ownerPtr, e.Repo)
		addPtr(fpEntryBaseName, "entries[].base.name", ownerPtr, e.Base.Name)
		addPtr(fpEntryBaseRef, "entries[].base.ref", ownerPtr, e.Base.Ref)
		addPtr(fpEntryDestinationDependsOn, "entries[].destination.depends_on", ownerPtr, e.Destination.DependsOn)
		addPtr(fpEntryBranchCheckedOutAt, "entries[].branch_checked_out_at", ownerPtr, e.BranchCheckedOutAt)
		for _, ref := range e.CollateralRefs {
			add(fpCollateralRepo, "entries[].collateral_refs[].repo", ownerPtr, ref.Repo)
			add(fpCollateralRef, "entries[].collateral_refs[].ref", ownerPtr, ref.Ref)
		}
	}

	seen := make(map[string]bool, len(cands))
	out := make([]PlanEncodingIssue, 0)
	for _, c := range cands {
		if c.value == "" || utf8.ValidString(c.value) {
			continue
		}
		raw := []byte(c.value)
		sum := sha256.Sum256(raw)
		row := PlanEncodingIssue{
			FieldID:         strconv.Itoa(int(c.fieldID)),
			PathLabelASCII:  asciiSanitize(c.pathLabel),
			OwnerEntryASCII: asciiSanitizePtr(c.ownerEntry),
			ByteLength:      len(raw),
			RawBase64:       base64.StdEncoding.EncodeToString(raw),
			SHA256:          hex.EncodeToString(sum[:]),
		}
		key := row.FieldID + "\x00" + row.PathLabelASCII + "\x00" + derefOr(row.OwnerEntryASCII, "\x01") + "\x00" + row.SHA256
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FieldID != b.FieldID {
			return a.FieldID < b.FieldID
		}
		if a.PathLabelASCII != b.PathLabelASCII {
			return a.PathLabelASCII < b.PathLabelASCII
		}
		ao, bo := a.OwnerEntryASCII, b.OwnerEntryASCII
		if (ao == nil) != (bo == nil) {
			return ao == nil // null sorts first
		}
		if ao != nil && *ao != *bo {
			return *ao < *bo
		}
		return a.SHA256 < b.SHA256
	})
	return out
}

// asciiSanitize renders a label as printable ASCII: every byte outside
// 0x20-0x7e becomes '?', so an encoding_issues[] row can never itself carry
// the bytes it reports (§4.4's "ASCII-only members").
func asciiSanitize(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			b = append(b, '?')
			continue
		}
		b = append(b, c)
	}
	return string(b)
}

func asciiSanitizePtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := asciiSanitize(*s)
	return &v
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
