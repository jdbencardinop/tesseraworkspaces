package internal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// ============================================================================
// The reparent pin namespace, its collision-safe identities, and the ONE CAS
// ref transaction (§9.8, §9.11, §11.7, §11.8 step 4).
//
// Every ref this feature writes lives under refs/tws/reparent/<run-id>/ until
// cleanup, and every public ref it moves moves inside a single
// `update-ref --stdin` transaction whose expected old values make a
// concurrent change abort the whole thing during `prepare`.
// ============================================================================

// ReparentRunIDBytes is the run id's entropy: 16 random bytes rendered as 32
// lowercase hex characters (§9.8).
const ReparentRunIDBytes = 16

// newReparentRunID mints a run id from crypto/rand. A failure is returned,
// never silently downgraded to a time-derived id: a colliding run id would
// alias two runs' pins.
func newReparentRunID() (string, error) {
	buf := make([]byte, ReparentRunIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint reparent run id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// reparentRunIDShape reports whether s is exactly 32 lowercase hex characters.
func reparentRunIDShape(s string) bool {
	if len(s) != ReparentRunIDBytes*2 {
		return false
	}
	for _, r := range s {
		hex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !hex {
			return false
		}
	}
	return true
}

// sanitizeReparentRefPart reduces an arbitrary logical name to something that
// is legal as ONE ref path component.
//
// The shipped sanitizeSessionPart (internal/session.go) keeps [A-Za-z0-9-_]
// and trims "_", which is not sufficient here: §9.8 additionally forbids a
// leading "-" or ".", forbids "..", "@{" and a trailing ".lock", and permits
// ".". Widening sanitizeSessionPart would change every existing tmux and
// session name, so this is a separate function and that one is untouched.
//
// "_" is deliberately NOT in the kept set: it is the joiner
// ReparentEntryRefID puts between the prefix and the hash, so a prefix can
// never contain it and the two parts can never be confused.
func sanitizeReparentRefPart(s string) string {
	var b strings.Builder
	prevDot := false
	for _, r := range s {
		keep := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-'
		if !keep {
			b.WriteByte('-')
			prevDot = false
			continue
		}
		if r == '.' && prevDot {
			// ".." is forbidden in a ref component; the second dot becomes a
			// dash rather than being dropped, so distinct names stay distinct.
			b.WriteByte('-')
			prevDot = false
			continue
		}
		b.WriteRune(r)
		prevDot = r == '.'
	}
	return trimReparentRefPart(b.String())
}

// trimReparentRefPart applies the leading/trailing rules of §9.8. It is a
// separate step because truncation to the 64-byte cap can re-introduce a
// trailing "." or a trailing ".lock" that the full string did not have.
func trimReparentRefPart(s string) string {
	for strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") {
		s = s[1:]
	}
	for strings.HasSuffix(s, ".") {
		s = s[:len(s)-1]
	}
	if strings.HasSuffix(s, ".lock") {
		s = s[:len(s)-len(".lock")] + "-lock"
	}
	if s == "" {
		return "entry"
	}
	return s
}

// ReparentEntryRefID is the collision-safe identity one stack entry gets
// inside the pin namespace (§9.8). It follows the shipped hashedSessionID
// pattern — a sanitized prefix, "_", and 8 hex characters of a SHA-256 over
// the full identity, capped at 64 bytes — over sha256(feature + "/" + name).
//
// The raw StackEntry.Name is never used as a ref path component: "a" and
// "a/b" would otherwise collide as a ref directory and a ref file, and Git
// would refuse the second pin of a run that is already half-created.
func ReparentEntryRefID(feature, name string) string {
	sum := sha256.Sum256([]byte(feature + "/" + name))
	suffix := hex.EncodeToString(sum[:4])
	p := sanitizeReparentRefPart(name)
	if max := 64 - len(suffix) - 1; len(p) > max {
		p = trimReparentRefPart(p[:max])
	}
	return p + "_" + suffix
}

// The pin namespace of §9.8. ReparentPinNamespaceShape (internal/reparent_plan.go)
// is the run-id-free spelling a plan publishes; these are the resolved ones.
func reparentPinNamespace(runID string) string {
	return "refs/tws/reparent/" + runID
}

func reparentDestPinRef(runID string) string {
	return reparentPinNamespace(runID) + "/dest"
}

func reparentOriginalHeadPinRef(runID string) string {
	return reparentPinNamespace(runID) + "/original-head"
}

func reparentOldPinRef(runID, entryID string) string {
	return reparentPinNamespace(runID) + "/old/" + entryID
}

func reparentNewPinRef(runID, entryID string) string {
	return reparentPinNamespace(runID) + "/new/" + entryID
}

func reparentConflictCompletionRef(runID, entryID string) string {
	return reparentPinNamespace(runID) + "/conflict-complete/" + entryID
}

// ============================================================================
// Ref reads and pin writes
// ============================================================================

// reparentResolveRef reads one ref's current value. ok is false when the ref
// does not exist, which is an ANSWER (a missing pin is re-created, a missing
// branch is branch-ref-missing), never an error.
func reparentResolveRef(repoDir, ref string) (string, bool, error) {
	res, err := runReparentGit(repoDir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	out := strings.ToLower(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		if res.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("resolve %s: %s", ref, reparentGitMessage(res, err))
	}
	if out == "" {
		return "", false, nil
	}
	return out, true, nil
}

// reparentWritePin creates or moves one pin. Pins are the run's own refs, so
// they are written directly rather than through the CAS transaction, which is
// reserved for public branches.
func reparentWritePin(repoDir, ref, sha string) error {
	res, err := runReparentGit(repoDir, nil, "update-ref", ref, sha)
	if err != nil {
		return fmt.Errorf("pin %s at %s: %s", ref, sha, reparentGitMessage(res, err))
	}
	return nil
}

// reparentVerifyPin confirms a pin resolves to want, re-creating it when it is
// missing. A pin that resolves to a DIFFERENT object is never silently
// rewritten: that is the probe-failed case of §9.8's verify-all gate, and the
// caller decides how to refuse it.
func reparentVerifyPin(repoDir, ref, want string) (recreated bool, err error) {
	have, ok, err := reparentResolveRef(repoDir, ref)
	if err != nil {
		return false, err
	}
	if !ok {
		if err := reparentWritePin(repoDir, ref, want); err != nil {
			return false, err
		}
		return true, nil
	}
	if have != want {
		return false, fmt.Errorf("pin %s resolves to %s, not the recorded %s", ref, have, want)
	}
	return false, nil
}

// reparentListPins enumerates every ref under this run's namespace, sorted,
// so cleanup is deterministic and testable.
func reparentListPins(repoDir, runID string) ([]string, error) {
	res, err := runReparentGit(repoDir, nil, "for-each-ref", "--format=%(refname)", reparentPinNamespace(runID)+"/**")
	if err != nil {
		return nil, fmt.Errorf("list reparent pins: %s", reparentGitMessage(res, err))
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, nil
}

// reparentDeletePins removes every pin this run created. It is cleanup step 1
// of §11.6a and MUST tolerate having already been done.
func reparentDeletePins(repoDir, runID string) error {
	refs, err := reparentListPins(repoDir, runID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		res, err := runReparentGit(repoDir, nil, "update-ref", "-d", ref)
		if err != nil {
			// A pin that vanished between the enumeration and the delete is
			// exactly the state the delete wanted.
			if _, ok, resolveErr := reparentResolveRef(repoDir, ref); resolveErr == nil && !ok {
				continue
			}
			return fmt.Errorf("delete pin %s: %s", ref, reparentGitMessage(res, err))
		}
	}
	return nil
}

// ============================================================================
// The CAS transaction (§9.11)
// ============================================================================

// The two transaction verbs this feature is allowed to emit. `create`,
// `delete` and the stdin command `option no-deref` are forbidden. The
// transaction itself uses update-ref's command-level --no-deref flag so a ref
// swapped to symbolic after preflight cannot redirect an update outside the
// closure.
const (
	reparentCASUpdateVerb = "update"
	reparentCASVerifyVerb = "verify"
)

// reparentCASLine is one line of a transaction payload.
type reparentCASLine struct {
	Verb string
	Ref  string
	New  string
	Old  string
}

// reparentCASUpdate is `update <ref> <new> <old>`: every update carries its
// explicit expected old value, which is what makes a concurrent change abort
// the whole transaction during `prepare` with no public ref moved.
func reparentCASUpdate(ref, newSHA, oldSHA string) reparentCASLine {
	return reparentCASLine{Verb: reparentCASUpdateVerb, Ref: ref, New: newSHA, Old: oldSHA}
}

// reparentCASVerify is `verify <ref> <value>`: the no-op row's line. A row
// whose planned tip equals its pre-image MUST NOT emit `update <ref> X X` —
// verify preserves race-atomic coverage without pretending a ref moved.
func reparentCASVerify(ref, value string) reparentCASLine {
	return reparentCASLine{Verb: reparentCASVerifyVerb, Ref: ref, Old: value}
}

// buildReparentCASPayload renders the transaction: start, the ordered lines,
// prepare, commit. Even an all-no-op closure gets the full envelope, because
// the verification IS the race barrier for the whole closure.
func buildReparentCASPayload(lines []reparentCASLine) []byte {
	var b strings.Builder
	b.WriteString("start\n")
	for _, l := range lines {
		switch l.Verb {
		case reparentCASUpdateVerb:
			fmt.Fprintf(&b, "update %s %s %s\n", l.Ref, l.New, l.Old)
		case reparentCASVerifyVerb:
			fmt.Fprintf(&b, "verify %s %s\n", l.Ref, l.Old)
		}
	}
	b.WriteString("prepare\n")
	b.WriteString("commit\n")
	return []byte(b.String())
}

// reparentCASMessage is the reflog message every transaction carries.
func reparentCASMessage(feature, target, runID string) string {
	return fmt.Sprintf("tws reparent %s %s %s", feature, target, runID)
}

// runReparentCAS executes exactly one transaction with exec.Cmd.Dir set to
// the repository root and the payload fed from a buffer. BOTH child streams
// are captured: update-ref is quiet on success, but a reference-transaction
// hook may print, and this process's stdout is reserved for the plan
// document. Captured output is discarded on success and quoted in the refusal
// detail on failure.
var ReparentCASBarrier func() error

func runReparentCAS(repoRoot, message string, lines []reparentCASLine) (reparentGitResult, error) {
	if ReparentCASBarrier != nil {
		if err := ReparentCASBarrier(); err != nil {
			return reparentGitResult{ExitCode: -1}, err
		}
	}
	return runReparentGit(repoRoot, buildReparentCASPayload(lines), "update-ref", "--no-deref", "-m", message, "--stdin")
}

// reparentForwardCASLines builds the forward transaction's lines from the
// persisted rows AND their live §11.7 classification, in ReparentClosureOrder.
//
// The classification is not an optimisation: on the files backend a crash
// during `commit` can leave a SUBSET of the affected refs moved, and
// re-issuing `update <ref> <planned> <preimage>` for a row that already holds
// its planned tip would carry a stale expected old value and abort the entire
// recovery transaction during `prepare` — the partial commit could then never
// be completed forward. Each row therefore emits:
//
//   - pre-image   -> update <ref> <planned> <preimage>   (the update did not land)
//   - planned tip -> verify <ref> <planned>              (it did; hold it still)
//   - no-op       -> verify <ref> <preimage>             (nothing to move, ever)
//
// A fresh run classifies every row pre-image (or no-op), so its payload is
// byte-identical to the one a classification-free builder would produce.
// A foreign row never reaches here: the caller refuses before any write.
func reparentForwardCASLines(rows []ReparentStateRow, class map[string]ReparentRefClass) []reparentCASLine {
	out := make([]reparentCASLine, 0, len(rows))
	for _, row := range rows {
		ref := "refs/heads/" + row.GitBranch
		switch class[row.Name] {
		case ReparentRefPlannedTip:
			out = append(out, reparentCASVerify(ref, row.PlannedNewSHA))
		case ReparentRefNoop:
			out = append(out, reparentCASVerify(ref, row.PreimageSHA))
		case ReparentRefPreimage:
			out = append(out, reparentCASUpdate(ref, row.PlannedNewSHA, row.PreimageSHA))
		}
	}
	return out
}

// reparentAbortCASLines builds §11.8 step 4's rollback transaction from an
// already-computed classification: a planned-tip row is moved back with
// `update <ref> <preimage> <planned>`, and every pre-image or no-op row emits
// `verify <ref> <preimage>` so the window between the classification and the
// commit cannot be raced without aborting the transaction.
func reparentAbortCASLines(rows []ReparentStateRow, class map[string]ReparentRefClass) []reparentCASLine {
	out := make([]reparentCASLine, 0, len(rows))
	for _, row := range rows {
		ref := "refs/heads/" + row.GitBranch
		switch class[row.Name] {
		case ReparentRefPlannedTip:
			out = append(out, reparentCASUpdate(ref, row.PreimageSHA, row.PlannedNewSHA))
		case ReparentRefPreimage, ReparentRefNoop:
			out = append(out, reparentCASVerify(ref, row.PreimageSHA))
		}
	}
	return out
}

// ============================================================================
// §11.7 per-ref classification
// ============================================================================

// ReparentRefClass is the closed four-member classification of one affected
// ref after a partial commit.
type ReparentRefClass string

const (
	ReparentRefNoop       ReparentRefClass = "no-op"
	ReparentRefPreimage   ReparentRefClass = "pre-image"
	ReparentRefPlannedTip ReparentRefClass = "planned tip"
	ReparentRefForeign    ReparentRefClass = "foreign"
)

// ReparentRefClassification is one row's classification, with the values that
// produced it so a refusal can name the branch, both expected values, and the
// observed one.
type ReparentRefClassification struct {
	Name      string
	GitBranch string
	Ref       string
	Live      string
	LiveFound bool
	Preimage  string
	Planned   string
	Class     ReparentRefClass
}

// classifyReparentRefValue is the pure half of §11.7: given the three values,
// which class is it? A no-op row whose live ref no longer equals its shared
// pre-image/planned value is foreign, exactly like any other unexpected
// value.
func classifyReparentRefValue(live, preimage, planned string) ReparentRefClass {
	noop := planned != "" && planned == preimage
	switch {
	case noop && live == preimage:
		return ReparentRefNoop
	case !noop && live == planned && planned != "":
		return ReparentRefPlannedTip
	case !noop && live == preimage:
		return ReparentRefPreimage
	default:
		return ReparentRefForeign
	}
}

// ClassifyReparentRefs classifies EVERY row before any ref is written, which
// is what makes a single foreign row abort a whole recovery before it changes
// anything. It performs only reads.
func ClassifyReparentRefs(repoDir string, rows []ReparentStateRow) ([]ReparentRefClassification, error) {
	out := make([]ReparentRefClassification, 0, len(rows))
	for _, row := range rows {
		ref := "refs/heads/" + row.GitBranch
		live, found, err := reparentResolveRef(repoDir, ref)
		if err != nil {
			return nil, err
		}
		c := ReparentRefClassification{
			Name:      row.Name,
			GitBranch: row.GitBranch,
			Ref:       ref,
			Live:      live,
			LiveFound: found,
			Preimage:  row.PreimageSHA,
			Planned:   row.PlannedNewSHA,
		}
		if !found {
			c.Class = ReparentRefForeign
		} else {
			c.Class = classifyReparentRefValue(live, row.PreimageSHA, row.PlannedNewSHA)
		}
		out = append(out, c)
	}
	return out, nil
}

// ReparentRefClassMap projects a classification slice into the name-keyed map
// the CAS builders consume.
func ReparentRefClassMap(rows []ReparentRefClassification) map[string]ReparentRefClass {
	m := make(map[string]ReparentRefClass, len(rows))
	for _, r := range rows {
		m[r.Name] = r.Class
	}
	return m
}

// reparentForeignRefDetail is the refusal sentence a foreign row produces. It
// names the branch, both expected values and the observed value, because the
// operator has to be able to reconcile it by hand.
func reparentForeignRefDetail(c ReparentRefClassification) string {
	observed := c.Live
	if !c.LiveFound {
		observed = "(missing)"
	}
	planned := c.Planned
	if planned == "" {
		planned = "(not computed)"
	}
	return fmt.Sprintf(
		"%s (%s) holds %s; this run expected either its pre-image %s or its planned tip %s. Reconcile that branch by hand; tws will not overwrite it",
		c.Name, c.GitBranch, observed, c.Preimage, planned)
}

// reparentAllPlannedOrNoop reports whether every row classifies as planned tip
// or no-op — half of §11.8a's commit-point conjunction.
func reparentAllPlannedOrNoop(rows []ReparentRefClassification) bool {
	for _, r := range rows {
		if r.Class != ReparentRefPlannedTip && r.Class != ReparentRefNoop {
			return false
		}
	}
	return true
}

// ============================================================================
// Ref backend probe (§9.10)
// ============================================================================

// probeReparentRefBackend answers files | reftable | unknown. An unknown
// backend is PERMITTED and never refuses: it is treated exactly as files —
// conservative crash recovery, row-by-row reconciliation, and the
// files-backend-not-crash-atomic warning.
func probeReparentRefBackend(repoDir string, caps ReparentGitCapabilities) string {
	if caps.CapRefBackendKnown {
		res, err := runReparentGit(repoDir, nil, "rev-parse", "--show-ref-format")
		if err == nil {
			switch strings.TrimSpace(string(res.Stdout)) {
			case ReparentRefBackendFiles:
				return ReparentRefBackendFiles
			case ReparentRefBackendReftable:
				return ReparentRefBackendReftable
			}
		}
	}
	res, err := runReparentGit(repoDir, nil, "config", "--get", "extensions.refStorage")
	if err == nil {
		switch strings.TrimSpace(string(res.Stdout)) {
		case ReparentRefBackendFiles:
			return ReparentRefBackendFiles
		case ReparentRefBackendReftable:
			return ReparentRefBackendReftable
		}
	}
	return ReparentRefBackendUnknown
}

// reparentBackendCrashAtomic reports whether a crash during `commit` is
// atomic on this backend. Only reftable is; files and unknown are not, and
// unknown is treated as files.
func reparentBackendCrashAtomic(backend string) bool {
	return backend == ReparentRefBackendReftable
}
