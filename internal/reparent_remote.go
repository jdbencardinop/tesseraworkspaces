package internal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ============================================================================
// The pending remote follow-up record (§12) — core.
//
// This feature makes ZERO provider calls, never pushes, and never touches
// upstream configuration. What it does do is leave a local, versioned record
// saying "these branches were rewritten and their pull requests still point
// at the old base", so the next push can warn and strengthen its lease.
//
// This file owns the record, its durable IO, the §12.2 guidance, the §12.5
// clearing observations and the pure decision/preflight values rule R-PUSH
// consumes. It deliberately wires NOTHING into the shipped push paths: that
// integration is a later slice, and a record type that exists before the
// executor is exactly what stage `writing-remote-record` depends on.
// ============================================================================

// ReparentRemoteRecordVersion is the record's schema version. A greater value
// refuses reparent-state-unsupported, exactly as the state artifact does.
const ReparentRemoteRecordVersion = 1

// The two entry states of §12.3.
const (
	ReparentRemoteStatePending = "pending"
	ReparentRemoteStateCleared = "cleared"
)

// ReparentRemoteEntry is one recorded branch. remote is the constant "origin"
// because that is the only remote all three live push paths name.
type ReparentRemoteEntry struct {
	Name      string `yaml:"name"`
	GitBranch string `yaml:"git_branch"`
	Repo      string `yaml:"repo,omitempty"`
	Remote    string `yaml:"remote"`
	RemoteRef string `yaml:"remote_ref"`

	NewTipSHA        string `yaml:"new_tip_sha"`
	RemoteSHAAtWrite string `yaml:"remote_sha_at_write"`

	PRBaseBefore string `yaml:"pr_base_before"`
	PRBaseAfter  string `yaml:"pr_base_after"`

	State string `yaml:"state"`
}

// Pending reports whether this entry still constrains a push.
func (e ReparentRemoteEntry) Pending() bool { return e.State == ReparentRemoteStatePending }

// Published reports whether a remote branch was actually observed when the
// entry was written. An unpublished entry is written cleared, so a later bare
// lease against a concurrently appearing ref stays safe without this record
// having to guess.
func (e ReparentRemoteEntry) Published() bool { return strings.TrimSpace(e.RemoteSHAAtWrite) != "" }

// ReparentRemoteRecord is the whole file.
type ReparentRemoteRecord struct {
	RecordVersion int                   `yaml:"record_version"`
	Feature       string                `yaml:"feature"`
	RunID         string                `yaml:"run_id"`
	CreatedAt     string                `yaml:"created_at"`
	Entries       []ReparentRemoteEntry `yaml:"entries"`
}

// PendingEntries returns the still-pending entry names, in record order.
func (r ReparentRemoteRecord) PendingEntries() []string {
	var out []string
	for _, e := range r.Entries {
		if e.Pending() {
			out = append(out, e.Name)
		}
	}
	return out
}

// Entry finds one entry by logical name.
func (r ReparentRemoteRecord) Entry(name string) (ReparentRemoteEntry, bool) {
	for _, e := range r.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return ReparentRemoteEntry{}, false
}

// AllCleared reports whether every entry is cleared — the condition under
// which the file itself must be deleted.
func (r ReparentRemoteRecord) AllCleared() bool {
	for _, e := range r.Entries {
		if e.Pending() {
			return false
		}
	}
	return true
}

// reparentRemoteInitialState is §12.3's initial-state rule: an entry whose
// remote-tracking ref did not resolve, and whose observed remote value is
// therefore empty, is written CLEARED — no published branch was observed, so
// there is no remote history or PR-base relationship for this run to protect.
// Everything else is written pending.
func reparentRemoteInitialState(remoteSHAAtWrite string) string {
	if strings.TrimSpace(remoteSHAAtWrite) == "" {
		return ReparentRemoteStateCleared
	}
	return ReparentRemoteStatePending
}

// ============================================================================
// Durable IO
// ============================================================================

// LoadReparentRemoteRecord reads the record. A missing file is not an error:
// it is the ordinary "nothing pending" answer every push path needs.
func LoadReparentRemoteRecord(loc ReparentLocation) (*ReparentRemoteRecord, error) {
	path := ReparentRemoteRecordPath(loc)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	rec, err := decodeReparentRemoteRecordBytes(loc, data)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func decodeReparentRemoteRecordBytes(loc ReparentLocation, data []byte) (ReparentRemoteRecord, error) {
	path := ReparentRemoteRecordPath(loc)
	var rec ReparentRemoteRecord
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&rec); err != nil {
		return ReparentRemoteRecord{}, fmt.Errorf("the reparent remote follow-up record at %s does not decode: %w", path, err)
	}
	if rec.RecordVersion > ReparentRemoteRecordVersion {
		return ReparentRemoteRecord{}, reparentRefusal(ReparentRefusalStateUnsupported, fmt.Sprintf(
			"the reparent remote follow-up record at %s records record_version %d; this release supports %d",
			path, rec.RecordVersion, ReparentRemoteRecordVersion))
	}
	if err := validateReparentRemoteRecord(loc, rec); err != nil {
		return ReparentRemoteRecord{}, err
	}
	return rec, nil
}

func validReparentRemoteOID(oid string) bool {
	return (len(oid) == 40 || len(oid) == 64) && stackStatusObjectID.MatchString(oid)
}

func validateReparentRemoteRecord(loc ReparentLocation, rec ReparentRemoteRecord) error {
	invalid := func(detail string) error {
		return reparentRefusal(ReparentRefusalStateCorrupt, fmt.Sprintf(
			"the reparent remote follow-up record at %s is invalid: %s",
			ReparentRemoteRecordPath(loc), detail))
	}
	if rec.RecordVersion != ReparentRemoteRecordVersion {
		return invalid(fmt.Sprintf("record_version must equal %d, got %d", ReparentRemoteRecordVersion, rec.RecordVersion))
	}
	if rec.Feature == "" || rec.Feature != loc.Feature {
		return invalid(fmt.Sprintf("feature must equal %q, got %q", loc.Feature, rec.Feature))
	}
	if !reparentRunIDShape(rec.RunID) {
		return invalid("run_id must be exactly 32 lowercase hex characters")
	}
	if _, err := time.Parse(time.RFC3339, rec.CreatedAt); err != nil {
		return invalid("created_at must be RFC3339")
	}
	if len(rec.Entries) == 0 {
		return invalid("entries must be non-empty")
	}
	seen := map[string]bool{}
	for i, entry := range rec.Entries {
		label := fmt.Sprintf("entries[%d]", i)
		if entry.Name == "" || entry.GitBranch == "" {
			return invalid(label + " requires non-empty name and git_branch")
		}
		if seen[entry.Name] {
			return invalid(fmt.Sprintf("entry name %q is duplicated", entry.Name))
		}
		seen[entry.Name] = true
		if entry.Remote != ReparentRemoteName {
			return invalid(fmt.Sprintf("%s.remote must be %q", label, ReparentRemoteName))
		}
		wantRef := "refs/remotes/" + ReparentRemoteName + "/" + entry.GitBranch
		if entry.RemoteRef != wantRef {
			return invalid(fmt.Sprintf("%s.remote_ref must be %q", label, wantRef))
		}
		if !validReparentRemoteOID(entry.NewTipSHA) {
			return invalid(label + ".new_tip_sha must be a lowercase 40- or 64-hex object id")
		}
		if entry.RemoteSHAAtWrite != "" && !validReparentRemoteOID(entry.RemoteSHAAtWrite) {
			return invalid(label + ".remote_sha_at_write must be empty or a lowercase 40- or 64-hex object id")
		}
		switch entry.State {
		case ReparentRemoteStatePending:
			if entry.RemoteSHAAtWrite == "" {
				return invalid(label + " cannot be pending without remote_sha_at_write")
			}
		case ReparentRemoteStateCleared:
		default:
			return invalid(label + ".state must be pending or cleared")
		}
	}
	return nil
}

// SaveReparentRemoteRecord writes the record durably at 0600.
func SaveReparentRemoteRecord(loc ReparentLocation, rec ReparentRemoteRecord) error {
	data, err := marshalReparentRemoteRecord(loc, rec)
	if err != nil {
		return err
	}
	return WriteReparentRemoteRecordBytes(loc, data)
}

func marshalReparentRemoteRecord(loc ReparentLocation, rec ReparentRemoteRecord) ([]byte, error) {
	rec.RecordVersion = ReparentRemoteRecordVersion
	if rec.CreatedAt == "" {
		rec.CreatedAt = reparentNow()
	}
	if err := validateReparentRemoteRecord(loc, rec); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(&rec)
	if err != nil {
		return nil, fmt.Errorf("marshal reparent remote follow-up record: %w", err)
	}
	return data, nil
}

// WriteReparentRemoteRecordBytes restores or writes exact record bytes through
// the same durable boundary as the normal encoder.
func WriteReparentRemoteRecordBytes(loc ReparentLocation, data []byte) error {
	return durableWriteFileFault(SyncIOWriteReparentRemote, ReparentRemoteRecordPath(loc), data, 0600)
}

// ReadReparentRemoteRecordBytes validates and returns the exact bytes on disk.
// A missing record is (nil, false, nil).
func ReadReparentRemoteRecordBytes(loc ReparentLocation) ([]byte, bool, error) {
	path := ReparentRemoteRecordPath(loc)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := LoadReparentRemoteRecord(loc); err != nil {
		return nil, true, err
	}
	return data, true, nil
}

// RemoveReparentRemoteRecord deletes the record file. An absent file is
// success.
func RemoveReparentRemoteRecord(loc ReparentLocation) error {
	path := ReparentRemoteRecordPath(loc)
	if err := syncIOFault(SyncIORemoveReparentRemote, path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncIOFault(SyncIORemoveReparentRemote, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// MergeReparentRemoteRecord folds this run's entries into whatever record
// already exists. A record can outlive its run by design, so a previous run's
// still-pending entry for ANOTHER branch is preserved verbatim. For the same
// branch, the new rewrite replaces topology fields, but a temporarily missing
// tracking ref cannot erase older positive publication evidence.
func MergeReparentRemoteRecord(existing *ReparentRemoteRecord, incoming ReparentRemoteRecord) ReparentRemoteRecord {
	incoming.Entries = append([]ReparentRemoteEntry(nil), incoming.Entries...)
	if existing == nil {
		incoming.RecordVersion = ReparentRemoteRecordVersion
		return incoming
	}
	out := ReparentRemoteRecord{
		RecordVersion: ReparentRemoteRecordVersion,
		Feature:       incoming.Feature,
		RunID:         incoming.RunID,
		CreatedAt:     incoming.CreatedAt,
	}
	incomingByName := make(map[string]int, len(incoming.Entries))
	for i := range incoming.Entries {
		incomingByName[incoming.Entries[i].Name] = i
	}
	for _, e := range existing.Entries {
		index, replaced := incomingByName[e.Name]
		if replaced {
			next := &incoming.Entries[index]
			if e.Pending() && e.Published() && !next.Pending() && !next.Published() {
				next.RemoteSHAAtWrite = e.RemoteSHAAtWrite
				next.State = ReparentRemoteStatePending
			}
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	out.Entries = append(out.Entries, incoming.Entries...)
	return out
}

// RemoveReparentRemoteEntries deletes exactly the named entries — the ones a
// pre-commit --abort created (§11.8 step 7) — and deletes the file when it
// empties. The names come from the state artifact's remote_followup_entries,
// which is the only authoritative statement of what THIS run wrote.
func RemoveReparentRemoteEntries(loc ReparentLocation, names []string) error {
	rec, err := LoadReparentRemoteRecord(loc)
	if err != nil || rec == nil {
		return err
	}
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	kept := rec.Entries[:0]
	for _, e := range rec.Entries {
		if drop[e.Name] {
			continue
		}
		kept = append(kept, e)
	}
	rec.Entries = kept
	if len(rec.Entries) == 0 {
		return RemoveReparentRemoteRecord(loc)
	}
	return SaveReparentRemoteRecord(loc, *rec)
}

// ============================================================================
// §12.2 guidance
// ============================================================================

// The fixed guidance sentences. They never recommend a plain `git push`,
// never bind --force-with-lease to a freshly observed remote tip, and never
// imply that --force-if-includes strengthens an explicit lease.
const (
	ReparentGuidanceNoRemoteChange = "tws changed no remote ref and no pull request."
	ReparentGuidanceOrder          = "retarget the pull request first, then fetch and integrate remote work, then publish."
	ReparentGuidanceForcePrefix    = "if you force publish, use: git push --force-with-lease --force-if-includes origin "
)

// ReparentRemoteGuidance builds remote.guidance — the same ordered lines the
// success output prints.
//
// Items 1 and 4 are single lines; items 2 and 3 repeat per qualifying row.
// Item 5 names a branch, so it repeats once per row that actually has a
// remote ref to publish to (and, when none does, once for the target's own
// branch): a single line naming one branch of many would tell an operator to
// force-publish the wrong one.
func ReparentRemoteGuidance(rows []ReparentPlanRemoteRow) []string {
	out := []string{ReparentGuidanceNoRemoteChange}
	for _, r := range rows {
		if r.PRBaseBefore == nil || r.PRBaseAfter == nil || *r.PRBaseBefore == *r.PRBaseAfter {
			continue
		}
		out = append(out, fmt.Sprintf("%s: PR base %s -> %s", r.Name, *r.PRBaseBefore, *r.PRBaseAfter))
	}
	for _, r := range rows {
		if r.Divergence == "none" || r.RemoteRef == nil {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s: local and %s have diverged; the remote still holds the pre-reparent history.", r.Name, *r.RemoteRef))
	}
	out = append(out, ReparentGuidanceOrder)

	published := false
	for _, r := range rows {
		if r.RemoteRef == nil {
			continue
		}
		published = true
		out = append(out, ReparentGuidanceForcePrefix+r.GitBranch)
	}
	if !published && len(rows) > 0 {
		out = append(out, ReparentGuidanceForcePrefix+rows[0].GitBranch)
	}
	return out
}

// reparentProviderHint derives §7.9's provider_hint from the local origin URL
// and from nothing else. No provider is contacted, and an unrecognized URL is
// "generic", never a guess at a vendor.
func reparentProviderHint(originURL string) string {
	u := strings.ToLower(strings.TrimSpace(originURL))
	switch {
	case u == "":
		return "unknown"
	case strings.Contains(u, "github.com"):
		return "github"
	case strings.Contains(u, "dev.azure.com"), strings.Contains(u, "visualstudio.com"):
		return "azure-devops"
	default:
		return "generic"
	}
}

// ============================================================================
// §12.5 clearing, without provider access
// ============================================================================

// ReparentRemoteObservation is which of §12.5's local observations cleared an
// entry. It is published so a mutating route can say why it cleared a row.
type ReparentRemoteObservation string

const (
	ReparentRemoteObservationNone      ReparentRemoteObservation = ""
	ReparentRemoteObservationPublished ReparentRemoteObservation = "remote-tip-equals-new-tip"
	ReparentRemoteObservationContains  ReparentRemoteObservation = "remote-contains-new-tip"
	ReparentRemoteObservationEntryGone ReparentRemoteObservation = "entry-absent-or-archived"
)

// ObserveReparentRemoteClear evaluates §12.5 observations 1, 2 and 4 for one
// entry, locally, with no fetch and no network. Observation 3 (a tws push
// succeeded) is the push path's own follow-up: it re-reads the tracking ref
// after its push and re-enters here, so there is exactly one clearing ladder.
//
// A missing remote-tracking ref is deliberately NOT a clearing observation:
// §12.5's initial-state rule already cleared the never-published rows, and a
// published row whose tracking ref later disappears stays pending until the
// operator resolves the remote state.
func ObserveReparentRemoteClear(repoDir string, e ReparentRemoteEntry, stack *Stack) (ReparentRemoteObservation, error) {
	if stack != nil {
		found := false
		for _, entry := range stack.Branches {
			if entry.Name != e.Name {
				continue
			}
			found = true
			if entry.Archived {
				return ReparentRemoteObservationEntryGone, nil
			}
		}
		if !found {
			return ReparentRemoteObservationEntryGone, nil
		}
	}
	ref := e.RemoteRef
	if ref == "" {
		ref = "refs/remotes/" + ReparentRemoteName + "/" + e.GitBranch
	}
	live, ok, err := reparentResolveRef(repoDir, ref)
	if err != nil {
		return ReparentRemoteObservationNone, err
	}
	if !ok {
		return ReparentRemoteObservationNone, nil
	}
	if live == e.NewTipSHA {
		return ReparentRemoteObservationPublished, nil
	}
	if e.NewTipSHA == "" {
		return ReparentRemoteObservationNone, nil
	}
	contains, err := reparentIsAncestor(repoDir, e.NewTipSHA, live)
	if err != nil {
		return ReparentRemoteObservationNone, err
	}
	if contains {
		return ReparentRemoteObservationContains, nil
	}
	return ReparentRemoteObservationNone, nil
}

// ReparentRemoteClear is one evaluated row of a clearing pass.
type ReparentRemoteClear struct {
	Name        string
	Observation ReparentRemoteObservation
	Cleared     bool
}

type ReparentRemoteClearTransition struct {
	Clears        []ReparentRemoteClear
	Source        []byte
	SourcePresent bool
	Target        []byte
	TargetPresent bool
	Changed       bool
}

// EvaluateReparentRemoteClears computes, IN MEMORY, which pending entries are
// now clear. It writes nothing, so a read-only surface that wants the facts
// may call it; §11.10 and §12.5 forbid those surfaces from persisting the
// result.
func EvaluateReparentRemoteClears(repoDir string, rec ReparentRemoteRecord, stack *Stack) ([]ReparentRemoteClear, error) {
	out := make([]ReparentRemoteClear, 0, len(rec.Entries))
	for _, e := range rec.Entries {
		if !e.Pending() {
			out = append(out, ReparentRemoteClear{Name: e.Name, Cleared: true})
			continue
		}

		entryRepo := repoDir
		repoToken := e.Repo
		if stack != nil {
			if current := GetBranch(*stack, e.Name); current.Name != "" {
				repoToken = current.Repo
			}
		}
		if strings.TrimSpace(repoToken) != "" {
			entryRepo = canonicalize(repoToken)
		}
		obs, err := ObserveReparentRemoteClear(entryRepo, e, stack)
		if err != nil {
			return nil, err
		}
		out = append(out, ReparentRemoteClear{
			Name:        e.Name,
			Observation: obs,
			Cleared:     obs != ReparentRemoteObservationNone,
		})
	}
	return out, nil
}

func BuildReparentRemoteClearTransition(loc ReparentLocation, repoDir string, stack *Stack) (ReparentRemoteClearTransition, error) {
	source, present, err := ReadReparentRemoteRecordBytes(loc)
	if err != nil || !present {
		return ReparentRemoteClearTransition{SourcePresent: present, TargetPresent: present}, err
	}
	rec, err := decodeReparentRemoteRecordBytes(loc, source)
	if err != nil {
		return ReparentRemoteClearTransition{}, err
	}
	clears, err := EvaluateReparentRemoteClears(repoDir, rec, stack)
	if err != nil {
		return ReparentRemoteClearTransition{}, err
	}
	byName := make(map[string]ReparentRemoteClear, len(clears))
	for _, clear := range clears {
		byName[clear.Name] = clear
	}
	changed := false
	for i := range rec.Entries {
		if rec.Entries[i].Pending() && byName[rec.Entries[i].Name].Cleared {
			rec.Entries[i].State = ReparentRemoteStateCleared
			changed = true
		}
	}
	transition := ReparentRemoteClearTransition{
		Clears: clears, Source: source, SourcePresent: true,
		Target: source, TargetPresent: true, Changed: changed,
	}
	if !changed {
		return transition, nil
	}
	if rec.AllCleared() {
		transition.Target = nil
		transition.TargetPresent = false
		return transition, nil
	}
	target, err := marshalReparentRemoteRecord(loc, rec)
	if err != nil {
		return ReparentRemoteClearTransition{}, err
	}
	transition.Target = target
	return transition, nil
}

// ApplyReparentRemoteClears is the MUTATING half: it persists the cleared
// states and deletes the file once every entry is cleared. Only a real push
// path, a push-enabled sync, a fresh reparent execution or a reparent
// recovery route may call it; every read-only surface must use
// EvaluateReparentRemoteClears instead.
func ApplyReparentRemoteClears(loc ReparentLocation, repoDir string, stack *Stack) ([]ReparentRemoteClear, error) {
	transition, err := BuildReparentRemoteClearTransition(loc, repoDir, stack)
	if err != nil || !transition.Changed {
		return transition.Clears, err
	}
	if transition.TargetPresent {
		err = WriteReparentRemoteRecordBytes(loc, transition.Target)
	} else {
		err = RemoveReparentRemoteRecord(loc)
	}
	return transition.Clears, err
}

// ============================================================================
// Rule R-PUSH decision values (§12.4, §12.4a) — data only
// ============================================================================

// ReparentPushDecision is what rule R-PUSH says about ONE entry of a push
// invocation. Its zero value is "this entry is not in any pending record",
// which MUST reproduce today's argv, output and exit status byte-for-byte —
// that is why the push-path integration can pass a zero value and change
// nothing.
type ReparentPushDecision struct {
	// Applies is true only for an entry listed pending in the record.
	Applies bool

	// WarnLine is the single anchored stderr line printed before the push.
	WarnLine string

	// ForceIfIncludes is true exactly when the argv must become
	// `--force-with-lease --force-if-includes` — the BARE lease plus the
	// inclusion check, never an explicit --force-with-lease=<ref>:<sha>.
	ForceIfIncludes bool

	// DryRunSuffix is what `tws push --dry-run` renders instead of pushing.
	DryRunSuffix string
}

// ReparentPushDecisionFor computes the decision for one entry. A record that
// is absent, or that does not list the entry, produces the zero value.
func ReparentPushDecisionFor(rec *ReparentRemoteRecord, name string) ReparentPushDecision {
	if rec == nil {
		return ReparentPushDecision{}
	}
	e, ok := rec.Entry(name)
	if !ok || !e.Pending() {
		return ReparentPushDecision{}
	}
	baseChange := ""
	if e.PRBaseBefore != "" && e.PRBaseAfter != "" {
		baseChange = fmt.Sprintf(" (%s -> %s)", e.PRBaseBefore, e.PRBaseAfter)
	}
	return ReparentPushDecision{
		Applies: true,
		WarnLine: fmt.Sprintf(
			"reparent-remote: %s was reparented%s; retarget the pull request before publishing",
			e.Name, baseChange),
		ForceIfIncludes: true,
		DryRunSuffix:    "(would push --force-with-lease --force-if-includes)",
	}
}

// ReparentPushPreflight is rule R-PUSH's INVOCATION-WIDE gate, evaluated once
// before the first real push. Pushing half a stack and only then discovering
// the lease cannot be strengthened is precisely the outcome it exists to
// prevent, which is why it is never per-entry.
type ReparentPushPreflight struct {
	// Refuse is true when at least one published, pending entry of this
	// invocation cannot be pushed with a strengthened lease, or when the
	// record itself could not be trusted.
	Refuse bool

	// LoadFailure distinguishes "the record could not be read" from "a lease
	// cannot be strengthened", and LoadKind carries the loader's OWN verdict
	// so every push path reports the same kind for the same file: a greater
	// record_version is reparent-state-unsupported, a document that does not
	// decode is reparent-state-corrupt.
	LoadFailure bool
	LoadKind    ReparentRefusalKind

	// Failing lists the entry names that failed, in invocation order.
	Failing []string

	// Detail is the refusal sentence: it names the failing entries, the
	// required Git version, the fetch remedy and the manual alternative.
	Detail string
}

// Refusal projects the preflight into the typed refusal a real push route
// returns. A dry run reports the same verdict as a diagnostic instead
// (§12.4a rule 2) and still exits 0.
func (p ReparentPushPreflight) Refusal() *ReparentRefusalError {
	if !p.Refuse {
		return nil
	}
	if p.LoadFailure {
		return reparentRefusal(p.loadKindOrCorrupt(), p.Detail)
	}
	return reparentRefusal(ReparentRefusalRemoteFollowupUnsafeLease, p.Detail)
}

// DryRunLine is §12.4a rule 2's diagnostic form of the same verdict.
func (p ReparentPushPreflight) DryRunLine() string {
	if !p.Refuse {
		return ""
	}
	kind := ReparentRefusalRemoteFollowupUnsafeLease
	if p.LoadFailure {
		kind = p.loadKindOrCorrupt()
	}
	return fmt.Sprintf("reparent-remote: would refuse: %s: %s", kind, p.Detail)
}

// loadKindOrCorrupt defaults an unrecorded load verdict to the conservative
// corrupt kind rather than to a lease problem it is not.
func (p ReparentPushPreflight) loadKindOrCorrupt() ReparentRefusalKind {
	if p.LoadKind != "" {
		return p.LoadKind
	}
	return ReparentRefusalStateCorrupt
}

// ReparentPushEntryProbe is one entry's measured input to the preflight: the
// name the invocation would push and whether its remote-tracking ref resolves
// right now.
type ReparentPushEntryProbe struct {
	Name             string
	TrackingResolves bool
}

// EvaluateReparentPushPreflight is rule R-PUSH step 2. It fails for a
// PUBLISHED pending entry (one whose remote_sha_at_write is non-empty) when
// either the Git binary cannot strengthen the lease or the tracking ref does
// not resolve. An unpublished entry was already written cleared, so it can
// never reach this gate.
func EvaluateReparentPushPreflight(rec *ReparentRemoteRecord, caps ReparentGitCapabilities, probes []ReparentPushEntryProbe) ReparentPushPreflight {
	if rec == nil {
		return ReparentPushPreflight{}
	}
	var failing []string
	var reasons []string
	for _, p := range probes {
		e, ok := rec.Entry(p.Name)
		if !ok || !e.Pending() || !e.Published() {
			continue
		}
		switch {
		case !caps.CapForceIfIncludes:
			failing = append(failing, p.Name)
			reasons = append(reasons, fmt.Sprintf("%s: this git cannot pass --force-if-includes", p.Name))
		case !p.TrackingResolves:
			failing = append(failing, p.Name)
			reasons = append(reasons, fmt.Sprintf("%s: %s does not resolve", p.Name, e.RemoteRef))
		}
	}
	if len(failing) == 0 {
		return ReparentPushPreflight{}
	}
	return ReparentPushPreflight{
		Refuse:  true,
		Failing: failing,
		Detail: fmt.Sprintf(
			"%s; a reparented branch is published only with git >= 2.30's --force-if-includes and a resolving remote-tracking ref. Run `git fetch %s` and retry, or publish by hand with: git push --force-with-lease --force-if-includes %s <branch>",
			strings.Join(reasons, "; "), ReparentRemoteName, ReparentRemoteName),
	}
}

// ============================================================================
// ReparentPushEnvelope (§12.4, §12.4a) — the push-path integration seam
//
// Every one of the three live bare-lease push paths loads ONE envelope per
// invocation, before its first real push, and then asks it two questions: the
// invocation-wide preflight and the per-entry decision. An invocation with no
// record holds an envelope whose Record is nil, whose Preflight never refuses
// and whose Decision is always the zero value — which is exactly rule 1's
// "behaviour is byte-identical to today".
// ============================================================================

// ReparentPushEnvelope is one push invocation's whole rule R-PUSH state.
type ReparentPushEnvelope struct {
	Loc     ReparentLocation
	RepoDir string
	Record  *ReparentRemoteRecord

	// Err is the typed load verdict. It is nil for "no record at all" — the
	// only inert absence — and non-nil for a record that EXISTS and could not
	// be trusted: an unsupported record_version, a document that does not
	// decode, or an unreadable file.
	Err error
}

// LoadReparentPushEnvelope reads the record ONCE and keeps the verdict.
//
// A record that exists but cannot be read is NOT "no record". Treating it as
// one would silently restore the bare `--force-with-lease` argv for branches
// this tool knows were rewritten, which is the precise outcome rule R-PUSH
// exists to prevent — and it would do so on the strength of a file the tool
// failed to parse. Only os.ErrNotExist is inert; every other verdict makes
// the invocation refuse before its first push.
func LoadReparentPushEnvelope(loc ReparentLocation, repoDir string) ReparentPushEnvelope {
	env := ReparentPushEnvelope{Loc: loc, RepoDir: repoDir}
	if loc.Feature == "" {
		return env
	}
	rec, err := LoadReparentRemoteRecord(loc)
	if err != nil {
		env.Err = err
		return env
	}
	env.Record = rec
	return env
}

// PrepareReparentPushEnvelope applies §12.5 observations before lease
// capability preflight. Real push routes persist and reload under their
// invocation lock; dry-run callers request an in-memory projection only.
func PrepareReparentPushEnvelope(loc ReparentLocation, repoDir string, stack *Stack, persist bool) (ReparentPushEnvelope, error) {
	env := LoadReparentPushEnvelope(loc, repoDir)
	if env.Err != nil || env.Record == nil {
		return env, nil
	}
	if persist {
		if _, err := ApplyReparentRemoteClears(loc, repoDir, stack); err != nil {
			return env, err
		}
		reloaded := LoadReparentPushEnvelope(loc, repoDir)
		if refusal := reloaded.LoadRefusal(); refusal != nil {
			return reloaded, AnchorReparentRefusal(refusal)
		}
		return reloaded, nil
	}
	clears, err := EvaluateReparentRemoteClears(repoDir, *env.Record, stack)
	if err != nil {
		return env, err
	}
	rec := *env.Record
	rec.Entries = append([]ReparentRemoteEntry{}, env.Record.Entries...)
	byName := make(map[string]ReparentRemoteClear, len(clears))
	for _, clear := range clears {
		byName[clear.Name] = clear
	}
	for i := range rec.Entries {
		if rec.Entries[i].Pending() && byName[rec.Entries[i].Name].Cleared {
			rec.Entries[i].State = ReparentRemoteStateCleared
		}
	}
	env.Record = &rec
	return env, nil
}

// Failed reports whether the record exists and could not be trusted.
func (e ReparentPushEnvelope) Failed() bool { return e.Err != nil }

// LoadRefusal projects an untrusted record into the refusal every real push
// path returns before pushing anything. It is nil when the record loaded, or
// when there is none at all.
func (e ReparentPushEnvelope) LoadRefusal() *ReparentRefusalError {
	if e.Err == nil {
		return nil
	}
	var typed *ReparentRefusalError
	if errors.As(e.Err, &typed) {
		return typed
	}
	return reparentRefusal(ReparentRefusalStateCorrupt, fmt.Sprintf(
		"the reparent remote follow-up record at %s could not be read, so tws cannot tell which branches were reparented: %v. Inspect or remove it, then retry",
		ReparentRemoteRecordPath(e.Loc), e.Err))
}

// Active reports whether this invocation has any pending record at all.
func (e ReparentPushEnvelope) Active() bool {
	return e.Record != nil && len(e.Record.PendingEntries()) > 0
}

// Decision is rule R-PUSH steps 3 and 4 for one entry.
func (e ReparentPushEnvelope) Decision(name string) ReparentPushDecision {
	return ReparentPushDecisionFor(e.Record, name)
}

// Preflight is rule R-PUSH step 2, evaluated ONCE over every entry this
// invocation would push, before the first of them is pushed. names is the
// invocation's own push order, so Failing reports in that order.
func (e ReparentPushEnvelope) Preflight(names []string) ReparentPushPreflight {
	if refusal := e.LoadRefusal(); refusal != nil {
		return ReparentPushPreflight{Refuse: true, Detail: refusal.Detail, LoadFailure: true, LoadKind: refusal.Kind}
	}
	if !e.Active() {
		return ReparentPushPreflight{}
	}
	probes := make([]ReparentPushEntryProbe, 0, len(names))
	for _, name := range names {
		probes = append(probes, e.ProbeEntry(e.RepoDir, name))
	}
	return e.PreflightProbes(probes)
}

// ProbeEntry measures ONE entry's remote-tracking ref in the Git context that
// entry would actually be pushed from. External push loops choose that context
// per entry (a worktree, or StackEntry.Repo), so the probe takes it explicitly
// rather than assuming the envelope's own directory.
func (e ReparentPushEnvelope) ProbeEntry(repoDir, name string) ReparentPushEntryProbe {
	probe := ReparentPushEntryProbe{Name: name}
	if e.Record == nil {
		return probe
	}
	entry, ok := e.Record.Entry(name)
	if !ok || !entry.Pending() {
		return probe
	}
	ref := entry.RemoteRef
	if ref == "" {
		ref = "refs/remotes/" + ReparentRemoteName + "/" + entry.GitBranch
	}
	dir := repoDir
	if dir == "" {
		dir = e.RepoDir
	}
	_, resolves, err := reparentResolveRef(dir, ref)
	probe.TrackingResolves = resolves && err == nil
	return probe
}

// PreflightProbes is the pure half of step 2 over already-measured probes. An
// untrusted record refuses here too, so every caller that runs the
// invocation-wide preflight — which is every real push path — fails closed
// without needing its own branch for it.
func (e ReparentPushEnvelope) PreflightProbes(probes []ReparentPushEntryProbe) ReparentPushPreflight {
	if refusal := e.LoadRefusal(); refusal != nil {
		return ReparentPushPreflight{Refuse: true, Detail: refusal.Detail, LoadFailure: true, LoadKind: refusal.Kind}
	}
	if !e.Active() {
		return ReparentPushPreflight{}
	}
	version, _ := ProbeGitVersion()
	return EvaluateReparentPushPreflight(e.Record, ReparentGitCapabilitiesForVersion(version), probes)
}

// PersistClears re-observes after successful pushes so observation 3 can clear
// a row whose tracking ref advanced during the push. The mandatory preflight
// clearing pass already ran through PrepareReparentPushEnvelope. RepoDir stays
// the workspace/default repository; each record entry with Repo set overrides
// it inside EvaluateReparentRemoteClears.
func (e ReparentPushEnvelope) PersistClears(stack *Stack) error {
	if e.Record == nil || e.Err != nil {
		return e.Err
	}
	_, err := ApplyReparentRemoteClears(e.Loc, e.RepoDir, stack)
	return err
}
