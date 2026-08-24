package internal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ============================================================================
// Generic exec helpers — every Git invocation in this file runs `-C <dir>`
// explicitly rather than via cmd.Dir, matching the literal command text the
// specification quotes (`git -C <execution_context> rev-list ...`, §5, §11.7).
// ============================================================================

// runGit runs `git -C dir <args...>`, discarding neither stream: stdout is
// captured and trimmed of exactly one trailing newline (never more, so a
// value that legitimately ends in blank lines is not mangled), and stderr is
// captured on the returned *exec.ExitError so a caller that needs Git's own
// sentence (never surfaced to a document, only to Go errors) can read it.
func runGit(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), exitErr)
		}
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// runGitRaw is runGit without any newline trimming, for callers that parse a
// NUL- or newline-delimited stream themselves and need every byte Git wrote.
func runGitRaw(dir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// gitExitCode extracts the process exit code from a git invocation's error,
// or -1 when err does not carry one (a genuine exec failure: binary not
// found, context cancelled, and so on — never a value git itself chose).
func gitExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// runGitStreamLines runs `git -C dir <args...>`, streaming stdout to onLine
// one newline-delimited record at a time and reading the pipe to EOF before
// waiting on the process — the pipe is never closed early, so no command
// this function drives can ever raise EPIPE/SIGPIPE against the child (§5
// rule 6). onLine's error, if any, is remembered but does not stop the read;
// the child is always drained and waited on before this function returns.
func runGitStreamLines(dir string, args []string, onLine func(line string) error) error {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var callbackErr error
	for scanner.Scan() {
		if callbackErr == nil {
			callbackErr = onLine(scanner.Text())
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()

	if waitErr != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	if scanErr != nil {
		return scanErr
	}
	return callbackErr
}

// ============================================================================
// Generic length-framed SHA-256 — the one hashing primitive context_id and
// issue_id both build on.
// ============================================================================

// lengthFramedSHA256 hashes an ordered tuple of raw byte strings as SHA-256
// over their length-prefixed concatenation: each element is framed as a
// 4-byte big-endian length followed by its raw bytes, with no delimiter
// byte and no type tag, so no two adjacent elements can ever be confused for
// each other regardless of the bytes either one contains. The specification
// fixes the length-framed, raw-byte property (§4.4, §18.6) but not a
// concrete byte width; this is this file's own choice of width, exactly as
// internal/rebase_plan_fingerprint.go documents making its own choice for the
// whole-document TLV pre-image.
func lengthFramedSHA256(parts ...string) [32]byte {
	h := sha256.New()
	var length [4]byte
	for _, p := range parts {
		binary.BigEndian.PutUint32(length[:], uint32(len(p)))
		h.Write(length[:])
		h.Write([]byte(p))
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// ============================================================================
// Context identity (§4.4, §14.1a) — the one generic context-identity
// function, never execution-scoped and with no empty-string fallback.
// ============================================================================

// MeasureContextRoots runs the two canonicalizing Git reads a context
// identity needs — `rev-parse --show-toplevel` and `rev-parse
// --git-common-dir` — and returns each canonicalized by the §18.3 rule
// (EvalSymlinks plus the macOS /private spelling, via the package's existing
// canonicalPath). It is the read half of context identity; the caller
// already knows why this directory was chosen (entry repo, worktree, process
// cwd or workspace repository root) and supplies that as root_source
// separately, because this function has no way to infer it.
func MeasureContextRoots(dir string) (repoRoot string, commonDir string, err error) {
	top, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("measure context roots: %w", err)
	}
	common, err := runGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", fmt.Errorf("measure context roots: %w", err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return canonicalPath(top), canonicalPath(filepath.Clean(common)), nil
}

// ContextIdentityFor computes the §4.4 context_id: SHA-256 over the
// length-framed raw tuple ("config-context/v1", canonical raw repo root,
// root_source, canonical raw common dir). Callers that lack a canonical
// common dir MUST NOT call this with an empty string — there is no
// empty-string fallback (§4.4).
func ContextIdentityFor(repoRoot, rootSource, commonDir string) PlanContextIdentity {
	sum := lengthFramedSHA256("config-context/v1", repoRoot, rootSource, commonDir)
	return PlanContextIdentity{
		ContextID: hex.EncodeToString(sum[:]),
		RepoRoot:  repoRoot,
		CommonDir: commonDir,
	}
}

// EstablishContextIdentity composes MeasureContextRoots and
// ContextIdentityFor for the common case: a directory and the root_source
// label the caller already knows for it.
func EstablishContextIdentity(dir, rootSource string) (PlanContextIdentity, error) {
	repoRoot, commonDir, err := MeasureContextRoots(dir)
	if err != nil {
		return PlanContextIdentity{}, err
	}
	return ContextIdentityFor(repoRoot, rootSource, commonDir), nil
}

// ContextIdentityRequest is one (directory, root_source) pair
// BuildContextIdentities measures.
type ContextIdentityRequest struct {
	Dir        string
	RootSource string
}

// BuildContextIdentities measures every requested directory exactly once and
// returns the accumulated PlanContextIdentities table keyed by context_id
// (§4.4, §14.1a). Two requests that canonicalize to the same tuple collapse
// onto the same map entry, which is the ordinary multi-worktree-stack case,
// not a violation. The first measurement failure is returned immediately;
// partial identity tables are not published because every join in the
// document is keyed off this map (§4.4).
func BuildContextIdentities(reqs []ContextIdentityRequest) (PlanContextIdentities, error) {
	ids := make(PlanContextIdentities, len(reqs))
	for _, req := range reqs {
		identity, err := EstablishContextIdentity(req.Dir, req.RootSource)
		if err != nil {
			return nil, fmt.Errorf("build context identities: %s: %w", req.Dir, err)
		}
		ids[identity.ContextID] = identity
	}
	return ids, nil
}

// ============================================================================
// Candidate probes (§5 rules 4-10)
// ============================================================================

// ProbeCandidateCount is §5 rule 4: `rev-list --count --no-merges
// <upstream>..<branch>`, run in the execution context's own directory.
func ProbeCandidateCount(execDir, upstream, branch string) (int, error) {
	out, err := runGit(execDir, "rev-list", "--count", "--no-merges", upstream+".."+branch)
	if err != nil {
		return 0, err
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, fmt.Errorf("probe candidate count: unparseable rev-list --count output %q: %w", out, convErr)
	}
	return count, nil
}

// ProbeFirstCandidate is §5 rule 5. For count <= 0 there is no first
// candidate and this returns (nil, nil) without running Git at all, per the
// rule's own "omitted for N == 0" text (generalised to any non-positive
// count, since a first candidate cannot exist without one). For N > 0 it
// runs `rev-list --skip=<N-1> --max-count=1 --no-merges --topo-order
// <upstream>..<branch>` — never `--reverse --max-count=1`, which would
// return the newest commit instead of the first replay candidate — and then
// reads that commit's raw, unsanitized subject with `git log -1
// --format=%s`. The subject is stored verbatim: sanitization is a
// render-time-only concern (internal/rebase_plan_render.go).
func ProbeFirstCandidate(execDir, upstream, branch string, count int) (*PlanReplayCandidate, error) {
	if count <= 0 {
		return nil, nil
	}
	sha, err := runGit(execDir, "rev-list",
		fmt.Sprintf("--skip=%d", count-1), "--max-count=1", "--no-merges", "--topo-order",
		upstream+".."+branch)
	if err != nil {
		return nil, err
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return nil, fmt.Errorf("probe first candidate: rev-list produced no commit for count %d", count)
	}
	subject, err := runGit(execDir, "log", "-1", "--format=%s", sha)
	if err != nil {
		return nil, err
	}
	return &PlanReplayCandidate{
		SHA:     sha,
		Short:   shortSHA(sha),
		Subject: subject,
	}, nil
}

// CandidateStreamResult is the outcome of the one required §5 rule 6 stream
// read: Commits retains at most the first 50 SHAs oldest-first (§5 rule 7),
// Listed is the true total observed on the stream (which may exceed
// len(Commits)), Truncated is Listed > len(Commits), and CandidateDigest is
// the SHA-256 (§5 rule 8) over every SHA the stream produced — not just the
// retained 50 — each followed by "\n", rendered as 64 lowercase hex.
type CandidateStreamResult struct {
	Commits         []string
	Listed          int
	Truncated       bool
	CandidateDigest string
}

// candidateStreamRetentionCap is the §5 rule 7 rendering cap. It bounds only
// what this process retains in memory; the digest still covers the entire
// stream (§5 rule 8).
const candidateStreamRetentionCap = 50

// ProbeCandidateStream is §5 rule 6: exactly one `rev-list --reverse
// --topo-order --no-merges <upstream>..<branch>` stream, read to EOF. The
// pipe is never closed early — a second, count-derived --skip/--max-count
// window probe MUST NOT be issued instead, and none is (§5 rule 6). A known
// empty sequence yields the SHA-256 of zero bytes for CandidateDigest, per
// sha256.New's own zero-write behaviour, with no special case needed here.
func ProbeCandidateStream(execDir, upstream, branch string) (CandidateStreamResult, error) {
	h := sha256.New()
	var result CandidateStreamResult
	err := runGitStreamLines(execDir,
		[]string{"rev-list", "--reverse", "--topo-order", "--no-merges", upstream + ".." + branch},
		func(line string) error {
			sha := strings.TrimSpace(line)
			if sha == "" {
				return nil
			}
			result.Listed++
			if len(result.Commits) < candidateStreamRetentionCap {
				result.Commits = append(result.Commits, sha)
			}
			h.Write([]byte(sha))
			h.Write([]byte("\n"))
			return nil
		})
	if err != nil {
		return CandidateStreamResult{}, err
	}
	result.Truncated = result.Listed > len(result.Commits)
	result.CandidateDigest = hex.EncodeToString(h.Sum(nil))
	return result, nil
}

// ProbeMayDropPatchEquivalent is §5 rule 10's may_drop_patch_equivalent probe:
// `git rev-list --count <branch>..<upstream>` — the reversed range from the
// count/first/stream probes above. An empty result yields false, a non-empty
// one true, and a probe failure yields (nil, err) so the caller can publish
// the null the rule requires without inventing a false negative.
func ProbeMayDropPatchEquivalent(execDir, upstream, branch string) (*bool, error) {
	out, err := runGit(execDir, "rev-list", "--count", branch+".."+upstream)
	if err != nil {
		return nil, err
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return nil, fmt.Errorf("probe may-drop-patch-equivalent: unparseable rev-list --count output %q: %w", out, convErr)
	}
	result := count > 0
	return &result, nil
}

// ============================================================================
// Ordered Git config inventory (§11.7)
// ============================================================================

// GitConfigEntry is one ordered `git config --list --show-scope -z` record,
// kept in inventory order exactly as Git printed it (lowest to highest
// precedence within a scope, scopes themselves in Git's own read order).
type GitConfigEntry struct {
	Scope        string // one of Git's --show-scope names (system|global|local|worktree|command|submodule|unknown)
	Key          string // section.[subsection.]variable; section/variable lowercased by Git, subsection case preserved verbatim
	Value        string // "" both for a valueless occurrence and an explicit empty value — ValuePresent disambiguates
	ValuePresent bool   // false ⇔ a valueless occurrence ("key" with no "=value")
}

// GitConfigInventory is the outcome of the one ordered `--list --show-scope
// -z` read of §11.7. A failed or unparseable read yields Available: false
// with Entries left nil — never a partial slice — so every consumer treats
// it as fully invalid rather than as an incomplete-but-usable one.
type GitConfigInventory struct {
	Available bool
	Entries   []GitConfigEntry
	Err       error
}

// ProbeGitConfigInventory runs the one ordered read of §11.7:
// `git -C root config --list --show-scope -z`. It always attempts the read;
// gating it on CapConfigShowScope (§16 rule 3a) is the caller's job, exactly
// as gating it on the route's config scope rule is.
func ProbeGitConfigInventory(root string) GitConfigInventory {
	raw, err := runGitRaw(root, "config", "--list", "--show-scope", "-z")
	if err != nil {
		return GitConfigInventory{Err: err}
	}
	entries, parseErr := parseGitConfigInventory(raw)
	if parseErr != nil {
		return GitConfigInventory{Err: parseErr}
	}
	return GitConfigInventory{Available: true, Entries: entries}
}

// parseGitConfigInventory decodes the NUL-delimited `--show-scope -z`
// stream. Git emits each entry as a PAIR of NUL-terminated records — the
// scope alone, then either "key" (valueless) or "key\nvalue" — never a
// single combined "scope\tkey" record, which this function relies on
// (empirically confirmed against Git 2.55) rather than guessing at a
// delimiter. A malformed stream — an odd record count, a final byte that is
// not NUL, or an empty key — invalidates the whole read rather than
// publishing a partial list, matching this package's other fail-closed
// inventories (WorktreeInventory, BranchRefInventory).
func parseGitConfigInventory(raw []byte) ([]GitConfigEntry, error) {
	if len(raw) == 0 {
		return []GitConfigEntry{}, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, fmt.Errorf("git config inventory: output does not end with NUL")
	}
	records := strings.Split(string(raw[:len(raw)-1]), "\x00")
	if len(records)%2 != 0 {
		return nil, fmt.Errorf("git config inventory: odd record count %d", len(records))
	}
	entries := make([]GitConfigEntry, 0, len(records)/2)
	for i := 0; i < len(records); i += 2 {
		scope := records[i]
		keyValue := records[i+1]
		key := keyValue
		value := ""
		present := false
		if idx := strings.IndexByte(keyValue, '\n'); idx >= 0 {
			key = keyValue[:idx]
			value = keyValue[idx+1:]
			present = true
		}
		if key == "" {
			return nil, fmt.Errorf("git config inventory: empty key at record %d", i)
		}
		entries = append(entries, GitConfigEntry{Scope: scope, Key: key, Value: value, ValuePresent: present})
	}
	return entries, nil
}

// entriesForKey returns every inventory entry for key (already in Git's own
// normalized section.variable lowercase, subsection case preserved), in
// inventory order.
func entriesForKey(inv GitConfigInventory, key string) []GitConfigEntry {
	var matches []GitConfigEntry
	for _, e := range inv.Entries {
		if e.Key == key {
			matches = append(matches, e)
		}
	}
	return matches
}

// finalOccurrence returns the last (highest-precedence) inventory entry for
// key — Git's own "last one wins" rule for a single-valued key.
func finalOccurrence(inv GitConfigInventory, key string) (GitConfigEntry, bool) {
	matches := entriesForKey(inv, key)
	if len(matches) == 0 {
		return GitConfigEntry{}, false
	}
	return matches[len(matches)-1], true
}

// parseGitConfigBool applies Git's own `--type=bool` parsing rule to one raw
// occurrence (empirically confirmed against Git 2.55): a valueless
// occurrence is true; the explicit empty string is false; "true"/"yes"/"on"
// (case-insensitive) are true and "false"/"no"/"off" are false; any other
// value that parses as a base-10 integer is true iff non-zero; anything else
// does not parse.
func parseGitConfigBool(value string, present bool) (result bool, ok bool) {
	if !present {
		return true, true
	}
	switch strings.ToLower(value) {
	case "true", "yes", "on":
		return true, true
	case "false", "no", "off", "":
		return false, true
	}
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return n != 0, true
	}
	return false, false
}

// firstInvalidBooleanOccurrence scans every occurrence of key, in inventory
// order, for the first one Git's own bool grammar would reject — the
// "ordered-callback keys" rule of §11.7, applied directly to a plain-boolean
// key that carries no typed read of its own (every fatal plain-boolean key
// except rebase.updateRefs and rebase.autoStash, which §11.7 caps at two
// typed reads total per context).
func firstInvalidBooleanOccurrence(inv GitConfigInventory, key string) (GitConfigEntry, bool) {
	for _, e := range entriesForKey(inv, key) {
		if _, ok := parseGitConfigBool(e.Value, e.ValuePresent); !ok {
			return e, true
		}
	}
	return GitConfigEntry{}, false
}

// rebaseMergesGrammar applies §11.7's rebase.rebaseMerges-specific parse
// table to the ordered inventory's final effective occurrence: this key has
// no typed read (a --type=bool --get would misreport the valid
// rebase-cousins/no-rebase-cousins tokens as a boolean error and would
// collapse a valueless occurrence), so its grammar is reimplemented here
// exactly as the table specifies. present distinguishes a valueless
// occurrence (recreates merges) from an explicit one.
func rebaseMergesGrammar(value string, present bool) (recreates bool, ok bool) {
	if !present {
		return true, true
	}
	switch value {
	case "rebase-cousins", "no-rebase-cousins": // exact, case-sensitive; both recreate merges
		return true, true
	}
	switch strings.ToLower(value) {
	case "true", "yes", "on":
		return true, true
	case "false", "no", "off", "":
		return false, true
	}
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		return n != 0, true
	}
	return false, false
}

// typedBoolStatus is the outcome of one `--type=bool --get` read.
type typedBoolStatus int

const (
	typedBoolAbsent typedBoolStatus = iota
	typedBoolValid
	typedBoolInvalid
)

// probeTypedBoolConfig runs one of §11.7's at-most-two typed boolean reads:
// `git -C root config --type=bool --get key`. Exit 1 is Git's own "key not
// found" (typedBoolAbsent); exit 128 is Git's own "bad boolean"
// (typedBoolInvalid) — reached whenever ANY occurrence in file order is
// unparseable, even one a later, valid occurrence would otherwise mask,
// because Git's config machinery aborts at the first one it cannot parse
// while walking to find the last (verified empirically against Git 2.55: a
// first-bad-then-valid pair and a valid-then-first-bad pair both exit 128).
// Any other outcome is a genuine exec error, not a config result, and is
// returned as such.
func probeTypedBoolConfig(root, key string) (typedBoolStatus, bool, error) {
	out, err := runGit(root, "config", "--type=bool", "--get", key)
	if err == nil {
		return typedBoolValid, strings.TrimSpace(out) == "true", nil
	}
	switch gitExitCode(err) {
	case 1:
		return typedBoolAbsent, false, nil
	case 128:
		return typedBoolInvalid, false, nil
	}
	return typedBoolAbsent, false, err
}

// deriveBooleanSlot derives one plain-boolean PlanConfigSlot (update_refs or
// auto_stash — the two keys §11.7 grants a typed read) using the typed read
// as the fast, authoritative path, falling back to a manual walk of the
// already-fetched ordered inventory — applying Git's own bool grammar — to
// find the first occurrence a typed read would have failed on, when the
// typed read itself reports invalid. invocationKey is the literal key Git
// expects on the command line (e.g. "rebase.updateRefs"); lookupKey is its
// Git-normalized, all-lowercase inventory form (e.g. "rebase.updaterefs").
func deriveBooleanSlot(inv GitConfigInventory, root, invocationKey, lookupKey string) (PlanConfigSlot, *GitConfigEntry) {
	status, value, err := probeTypedBoolConfig(root, invocationKey)
	if err != nil {
		return PlanConfigSlot{Status: "probe-failed"}, nil
	}
	switch status {
	case typedBoolAbsent:
		return PlanConfigSlot{Status: "absent"}, nil
	case typedBoolValid:
		v := strconv.FormatBool(value)
		source := "probed"
		return PlanConfigSlot{Status: "valid", Value: &v, Source: &source}, nil
	default: // typedBoolInvalid
		if entry, invalid := firstInvalidBooleanOccurrence(inv, lookupKey); invalid {
			return PlanConfigSlot{Status: "invalid", Source: &entry.Scope}, &entry
		}
		// The typed read reported invalid but the same-grammar manual walk
		// found nothing wrong. Unreachable against a real Git, but a
		// fabricated occurrence would be worse than an honest probe-failed.
		return PlanConfigSlot{Status: "probe-failed"}, nil
	}
}

// deriveRebaseMergesSlot derives repositories[].config.rebase_merges. There
// is no typed read for this key (§11.7): its value is the ordered
// inventory's final occurrence alone. Absence yields interpretation:"false"
// — the one documented exception where a not-present slot still carries an
// interpretation (§4.4).
func deriveRebaseMergesSlot(inv GitConfigInventory) (PlanConfigSlot, *GitConfigEntry) {
	entry, ok := finalOccurrence(inv, "rebase.rebasemerges")
	if !ok {
		return PlanConfigSlot{Status: "absent", Interpretation: "false"}, nil
	}
	recreates, valid := rebaseMergesGrammar(entry.Value, entry.ValuePresent)
	if !valid {
		return PlanConfigSlot{Status: "invalid", Source: &entry.Scope, Interpretation: "invalid"}, &entry
	}
	value := entry.Value // raw mode token as written; "" for a valueless occurrence is a known value, not null
	source := "probed"
	interpretation := "false"
	if recreates {
		interpretation = "true"
	}
	return PlanConfigSlot{Status: "valid", Value: &value, Source: &source, Interpretation: interpretation}, nil
}

// deriveBackendSlot derives repositories[].config.backend. rebase.backend
// has no typed read either (§11.7): a valueless occurrence is invalid
// wherever it occurs ("any occurrence"), detected by scanning every
// occurrence in order rather than only the final one; an unknown,
// non-valueless name is invalid only at the final occurrence, and only when
// nonForcingRebasePresent is true (an entry whose argv would actually
// consult it exists in this context) — the caller supplies that fact because
// it depends on argv this function never sees. Returning a nil issue while
// still marking the slot invalid is exactly the "out-of-scope invalid value
// is neither parsed nor disclosed" case (§11.7): the configuration is
// honestly reported, but no config_issues[] row is minted for a cell no
// entry's argv would ever consult.
func deriveBackendSlot(inv GitConfigInventory, nonForcingRebasePresent bool) (PlanConfigSlot, *GitConfigEntry, string) {
	entries := entriesForKey(inv, "rebase.backend")
	if len(entries) == 0 {
		return PlanConfigSlot{Status: "absent"}, nil, ""
	}
	for i := range entries {
		if !entries[i].ValuePresent {
			offending := entries[i]
			return PlanConfigSlot{Status: "invalid", Source: &offending.Scope}, &offending, "missing-backend-value"
		}
	}
	final := entries[len(entries)-1]
	switch final.Value {
	case "merge", "apply":
		value := final.Value
		source := "probed"
		return PlanConfigSlot{Status: "valid", Value: &value, Source: &source}, nil, ""
	default:
		slot := PlanConfigSlot{Status: "invalid", Source: &final.Scope}
		if nonForcingRebasePresent {
			return slot, &final, "unknown-rebase-backend"
		}
		return slot, nil, ""
	}
}

// RepositoryConfigScope tells ProbeRepositoryConfig / DeriveRepositoryConfig
// which parts of §11.7's closed v1 fatal-key domain the described route
// actually exercises for this execution context, and supplies the small set
// of argv/backend facts the fatal table gates on that a config read alone
// cannot determine — those depend on the entries this context executes and
// their published argv, which only the caller (a planner) has measured.
// Neither function ever infers any of these from the repository itself.
type RepositoryConfigScope struct {
	// Rebase must be true iff this context is an execution_context of at
	// least one real rebase (§11.7's scope rule). When false, all four
	// PlanRepositoryConfig slots are not-evaluated and no rebase-scoped
	// config_issues[] row is ever produced.
	Rebase bool

	// NonForcingRebasePresent must be true when at least one entry in this
	// context publishes an argv that effective_backend row 1 would NOT
	// already force to merge — i.e., an entry whose rebase.backend value
	// would actually be consulted. It gates only the "unknown rebase.backend
	// name" issue (§11.7 fatal table); the valueless case is unconditional.
	NonForcingRebasePresent bool

	// MergeBackendActive must be true when at least one entry in this
	// context runs under effective_backend: merge. It gates the
	// rebase.abbreviateCommands and rebase.maxLabelLength issues, both
	// declared "merge backend only" in the fatal table.
	MergeBackendActive bool
}

// PlanConfigResult is the composite outcome of the one ordered
// `git config --list --show-scope -z` read plus its at-most-two typed
// boolean reads for a single execution context (§11.7): the four
// repositories[].config slots, and every config_issues[] row the closed v1
// rebase-scoped fatal-key domain yields from that same inventory.
type PlanConfigResult struct {
	UpdateRefs   PlanConfigSlot
	RebaseMerges PlanConfigSlot
	Backend      PlanConfigSlot
	AutoStash    PlanConfigSlot
	Issues       []PlanConfigIssue // never nil
}

// notEvaluatedConfigResult is the total value of a rebase-out-of-scope
// context: all four slots not-evaluated (§11.7's scope rule: "a base-only or
// push-only context performs zero config reads and its four slots are
// not-evaluated").
func notEvaluatedConfigResult() PlanConfigResult {
	slot := PlanConfigSlot{Status: "not-evaluated"}
	return PlanConfigResult{
		UpdateRefs:   slot,
		RebaseMerges: PlanConfigSlot{Status: "not-evaluated", Interpretation: "unknown"},
		Backend:      slot,
		AutoStash:    slot,
		Issues:       []PlanConfigIssue{},
	}
}

// probeFailedConfigResult is the total value published when the ordered
// inventory itself could not be established — either CapConfigShowScope is
// false (§16, §11.7) or the read/parse failed for an independent reason.
func probeFailedConfigResult() PlanConfigResult {
	slot := PlanConfigSlot{Status: "probe-failed"}
	return PlanConfigResult{
		UpdateRefs:   slot,
		RebaseMerges: PlanConfigSlot{Status: "probe-failed", Interpretation: "unknown"},
		Backend:      slot,
		AutoStash:    slot,
		Issues:       []PlanConfigIssue{},
	}
}

// newConfigIssue mints one config_issues[] row (§2.12's eleven-member shape)
// from the offending inventory entry. issue_id is SHA-256 over the
// length-framed tuple ("config-issue/v1", context_id, key, the offending raw
// value's presence flag as "0"/"1", the raw value itself, route_command).
// The specification fixes no issue_id formula of its own (unlike
// context_id's, §14.1a), so this file reuses the same length-framed SHA-256
// primitive with its own distinct version tag: deterministic,
// collision-resistant across keys/contexts/values/route_commands and stable
// across runs, exactly what "sole sort/dedup key" (§2.12) requires.
func newConfigIssue(contextID string, repoRoot *string, key, source, routeCommand, errorKind string, entry GitConfigEntry) PlanConfigIssue {
	presence := "0"
	if entry.ValuePresent {
		presence = "1"
	}
	sum := lengthFramedSHA256("config-issue/v1", contextID, key, presence, entry.Value, routeCommand)
	issue := PlanConfigIssue{
		IssueID:         hex.EncodeToString(sum[:]),
		ContextID:       contextID,
		RepoRoot:        repoRoot,
		Key:             key,
		Source:          source,
		RouteCommand:    routeCommand,
		ErrorKind:       errorKind,
		RawValuePresent: entry.ValuePresent,
	}
	if entry.ValuePresent {
		raw := []byte(entry.Value)
		b64 := base64.StdEncoding.EncodeToString(raw)
		sha := sha256.Sum256(raw)
		shaHex := hex.EncodeToString(sha[:])
		sanitized := ancestrySanitize(entry.Value, ancestryDetailLimit)
		issue.RawValueBase64 = &b64
		issue.RawValueSHA256 = &shaHex
		issue.SanitizedValue = &sanitized
	}
	return issue
}

// ProbeRepositoryConfig performs §11.7's per-context work end to end: the one
// ordered inventory read (gated by CapConfigShowScope), followed by
// DeriveRepositoryConfig. A caller that already holds this context's
// inventory — a push mapping reusing an execution context's read, or a
// fetch-effect resolution that also needs these same four slots — calls
// DeriveRepositoryConfig directly instead, so the ordered read is never
// taken twice for one context (§11.7, §14.1a rule 7, §22.27a).
func ProbeRepositoryConfig(root string, caps GitCapabilities, scope RepositoryConfigScope, contextID string, repoRoot *string, routeCommand string) PlanConfigResult {
	if !scope.Rebase {
		return notEvaluatedConfigResult()
	}
	if !caps.CapConfigShowScope {
		return probeFailedConfigResult()
	}
	inv := ProbeGitConfigInventory(root)
	return DeriveRepositoryConfig(inv, root, scope, contextID, repoRoot, routeCommand)
}

// DeriveRepositoryConfig is ProbeRepositoryConfig's read-already-done half:
// given an ordered inventory this context already fetched, it runs the two
// typed boolean reads (only when scope.Rebase — never more than once per
// context, which is the caller's responsibility to arrange) and derives the
// four repositories[].config slots plus every config_issues[] row the closed
// v1 rebase-scoped fatal-key domain yields.
func DeriveRepositoryConfig(inv GitConfigInventory, root string, scope RepositoryConfigScope, contextID string, repoRoot *string, routeCommand string) PlanConfigResult {
	if !scope.Rebase {
		return notEvaluatedConfigResult()
	}
	if !inv.Available {
		return probeFailedConfigResult()
	}

	mint := func(key, source, errorKind string, entry GitConfigEntry) PlanConfigIssue {
		return newConfigIssue(contextID, repoRoot, key, source, routeCommand, errorKind, entry)
	}

	var issues []PlanConfigIssue

	updateRefsSlot, updateRefsIssue := deriveBooleanSlot(inv, root, "rebase.updateRefs", "rebase.updaterefs")
	if updateRefsIssue != nil {
		issues = append(issues, mint("rebase.updateRefs", updateRefsIssue.Scope, "bad-boolean", *updateRefsIssue))
	}

	autoStashSlot, autoStashIssue := deriveBooleanSlot(inv, root, "rebase.autoStash", "rebase.autostash")
	if autoStashIssue != nil {
		issues = append(issues, mint("rebase.autoStash", autoStashIssue.Scope, "bad-boolean", *autoStashIssue))
	}

	rebaseMergesSlot, rebaseMergesIssue := deriveRebaseMergesSlot(inv)
	if rebaseMergesIssue != nil {
		issues = append(issues, mint("rebase.rebaseMerges", rebaseMergesIssue.Scope, "unknown-rebase-merges-mode", *rebaseMergesIssue))
	}

	backendSlot, backendIssue, backendErrorKind := deriveBackendSlot(inv, scope.NonForcingRebasePresent)
	if backendIssue != nil {
		issues = append(issues, mint("rebase.backend", backendIssue.Scope, backendErrorKind, *backendIssue))
	}

	for _, plain := range []struct{ lookupKey, jsonKey string }{
		{"rebase.stat", "rebase.stat"},
		{"rebase.autosquash", "rebase.autoSquash"},
		{"rebase.reschedulefailedexec", "rebase.rescheduleFailedExec"},
		{"rebase.forkpoint", "rebase.forkPoint"},
	} {
		if entry, invalid := firstInvalidBooleanOccurrence(inv, plain.lookupKey); invalid {
			issues = append(issues, mint(plain.jsonKey, entry.Scope, "bad-boolean", entry))
		}
	}

	if scope.MergeBackendActive {
		if entry, ok := finalOccurrence(inv, "rebase.abbreviatecommands"); ok {
			if _, valid := parseGitConfigBool(entry.Value, entry.ValuePresent); !valid {
				issues = append(issues, mint("rebase.abbreviateCommands", entry.Scope, "bad-boolean", entry))
			}
		}
		if entry, ok := finalOccurrence(inv, "rebase.maxlabellength"); ok {
			valid := false
			if entry.ValuePresent {
				if _, convErr := strconv.Atoi(strings.TrimSpace(entry.Value)); convErr == nil {
					valid = true
				}
			}
			if !valid {
				issues = append(issues, mint("rebase.maxLabelLength", entry.Scope, "bad-numeric", entry))
			}
		}
	}

	if issues == nil {
		issues = []PlanConfigIssue{}
	}
	return PlanConfigResult{
		UpdateRefs:   updateRefsSlot,
		RebaseMerges: rebaseMergesSlot,
		Backend:      backendSlot,
		AutoStash:    autoStashSlot,
		Issues:       issues,
	}
}

// ============================================================================
// Branch holder inventory (§7.9, §14.4, §18.3)
// ============================================================================

// BranchHoldMechanism is the closed four-member domain a worktree can hold a
// branch under: a plain checkout, or one of the three mid-operation states
// BuildBranchHolderInventory discovers by reading that worktree's own
// operation-state files rather than the porcelain listing's `branch` line,
// which a detached mid-operation worktree never carries.
type BranchHoldMechanism string

const (
	HoldCheckedOut  BranchHoldMechanism = "checked-out"
	HoldRebaseMerge BranchHoldMechanism = "rebase-merge"
	HoldRebaseApply BranchHoldMechanism = "rebase-apply"
	HoldBisect      BranchHoldMechanism = "bisect"
)

// holderMarkerReadCap bounds the head-name/BISECT_START reads
// BuildBranchHolderInventory performs: a ref or branch name is never anywhere
// close to this size, so a file this large is either not the artefact it
// claims to be or hostile, and is refused rather than read in full.
const holderMarkerReadCap = 4096

// BranchHolderRecord is one held branch: the canonical worktree path holding
// it (§18.3's canonical/symlink/`/private`-aware comparison rule — this is
// exactly the path a caller compares against another canonicalized path to
// decide self versus foreign), the mechanism, and whether the holding
// worktree is itself administratively prunable — a prunable worktree still
// holds its branch until `git worktree prune` actually runs, so it is kept
// as a holder, never dropped.
type BranchHolderRecord struct {
	Worktree  string
	Mechanism BranchHoldMechanism
	Prunable  bool
}

// BranchHolderInventory is one repository's whole hold inventory (§7.9,
// §18.3): every branch any worktree holds, checked out or mid-operation,
// keyed by short branch name. A branch is held by at most one worktree at a
// time — Git itself refuses to check out a branch that is already checked
// out, mid-rebase or mid-bisect elsewhere — so ByBranch is single-valued.
// An unavailable inventory is a probe failure, never a "not held" answer,
// so BuildBranchHolderInventory fails closed for the whole repository
// rather than publish a partial map.
type BranchHolderInventory struct {
	Available bool
	ByBranch  map[string]BranchHolderRecord
	Err       error
}

// BuildBranchHolderInventory performs exactly one `git worktree list
// --porcelain` — through the shipped BuildWorktreeInventory — its only
// process, then classifies every record: a non-detached record holds its
// branch as HoldCheckedOut; a detached record is probed, via bounded
// symlink-free reads of that worktree's own Git directory, for a
// rebase-merge, rebase-apply or bisect state that names the branch it is
// operating on. A record this function cannot classify either way (neither
// a branch line nor a detectable operation) simply holds nothing, which is
// not a failure of the whole inventory.
func BuildBranchHolderInventory(repoRoot string) BranchHolderInventory {
	wt := BuildWorktreeInventory(repoRoot)
	if !wt.Available {
		return BranchHolderInventory{Err: wt.Err}
	}
	byBranch := make(map[string]BranchHolderRecord, len(wt.Records))
	for _, rec := range wt.Records {
		if rec.Bare {
			continue // a bare repository record holds no branch
		}
		if rec.BranchRef != nil {
			short := strings.TrimPrefix(*rec.BranchRef, "refs/heads/")
			byBranch[short] = BranchHolderRecord{
				Worktree:  rec.Path,
				Mechanism: HoldCheckedOut,
				Prunable:  rec.Prunable,
			}
			continue
		}
		if branch, mechanism, ok := detectHolderOperation(rec.Path); ok {
			byBranch[branch] = BranchHolderRecord{
				Worktree:  rec.Path,
				Mechanism: mechanism,
				Prunable:  rec.Prunable,
			}
		}
	}
	return BranchHolderInventory{Available: true, ByBranch: byBranch}
}

// detectHolderOperation resolves worktreePath's own Git directory — never
// the common dir, since rebase-merge/, rebase-apply/ and BISECT_START are
// strictly per-worktree (§18.3) — and reports the branch a merge-rebase,
// apply-rebase or bisect there is holding. A missing or unreadable Git
// directory, or none of the three markers present, yields ok == false
// without error: an undetected mid-operation state is simply "not held that
// way", never a failure of the whole-repository inventory (that failure is
// BuildWorktreeInventory's, already handled by the caller).
func detectHolderOperation(worktreePath string) (branch string, mechanism BranchHoldMechanism, ok bool) {
	gitDir, err := resolveWorktreeGitDir(worktreePath)
	if err != nil {
		return "", "", false
	}
	if name, ok := readOperationHeadName(gitDir, "rebase-merge"); ok {
		return name, HoldRebaseMerge, true
	}
	if name, ok := readOperationHeadName(gitDir, "rebase-apply"); ok {
		return name, HoldRebaseApply, true
	}
	if name, ok := readBisectStartBranch(gitDir); ok {
		return name, HoldBisect, true
	}
	return "", "", false
}

// resolveWorktreeGitDir resolves worktreePath's own Git directory: a plain
// directory for a main working tree, or the target of a `gitdir: <path>`
// pointer file for a linked worktree — the same shape the shipped
// probeActiveGitOp (internal/stack_status.go) resolves, reimplemented here
// symlink-free because this file needs the resolved directory path itself,
// not only an operation-kind classification.
func resolveWorktreeGitDir(worktreePath string) (string, error) {
	if worktreePath == "" {
		return "", errors.New("resolveWorktreeGitDir requires a non-empty path")
	}
	gitDir := filepath.Join(worktreePath, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to follow symlinked %s", gitDir)
	}
	if info.IsDir() {
		return gitDir, nil
	}
	data, err := os.ReadFile(gitDir)
	if err != nil {
		return "", err
	}
	after, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok || after == "" {
		return "", fmt.Errorf("malformed gitdir pointer in %s", gitDir)
	}
	if !filepath.IsAbs(after) {
		after = filepath.Join(worktreePath, after)
	}
	resolved := filepath.Clean(after)
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if resolvedInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to follow symlinked %s", resolved)
	}
	if !resolvedInfo.IsDir() {
		return "", fmt.Errorf("resolved git directory %s is not a directory", resolved)
	}
	return resolved, nil
}

// readSymlinkFreeFile Lstats path and reads it only when that Lstat shows a
// regular file no larger than maxBytes, refusing a symlink outright rather
// than following it. present is false, with a nil error, exactly for
// fs.ErrNotExist — every other failure, including "exists but is a
// symlink"/"not a regular file"/"too large", is reported as an error so a
// caller never silently treats a hostile or malformed artefact as absent.
func readSymlinkFreeFile(path string, maxBytes int64) (content string, present bool, err error) {
	info, statErr := os.Lstat(path)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("refusing to follow symlinked artefact %s", path)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("artefact %s is not a regular file", path)
	}
	if info.Size() > maxBytes {
		return "", false, fmt.Errorf("artefact %s exceeds %d bytes", path, maxBytes)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return "", false, readErr
	}
	return string(data), true, nil
}

// readOperationHeadName reads <gitDir>/<opDir>/head-name — opDir one of
// rebase-merge, rebase-apply — and reports the branch the operation started
// from. Git writes either a full `refs/heads/<branch>` ref or the literal
// "detached HEAD" when the operation began on a detached HEAD, which itself
// held no branch; only the former reports a hold.
func readOperationHeadName(gitDir, opDir string) (string, bool) {
	dirInfo, err := os.Lstat(filepath.Join(gitDir, opDir))
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return "", false
	}
	content, present, err := readSymlinkFreeFile(filepath.Join(gitDir, opDir, "head-name"), holderMarkerReadCap)
	if err != nil || !present {
		return "", false
	}
	ref := strings.TrimSpace(content)
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok || branch == "" {
		return "", false
	}
	return branch, true
}

// readBisectStartBranch reads <gitDir>/BISECT_START, which Git writes as the
// short branch name `git bisect start` began from (never a full ref), and
// reports it as the branch bisect is holding.
func readBisectStartBranch(gitDir string) (string, bool) {
	content, present, err := readSymlinkFreeFile(filepath.Join(gitDir, "BISECT_START"), holderMarkerReadCap)
	if err != nil || !present {
		return "", false
	}
	branch := strings.TrimSpace(content)
	if branch == "" {
		return "", false
	}
	return branch, true
}

// PlanHolderIndex is the per-invocation cache of branch holder inventories,
// keyed by context_id (§7.9, §18.3): every context_id that shares a
// canonical common dir shares one entry, and BuildPlanHolderIndex therefore
// calls BuildBranchHolderInventory once per distinct canonical common dir,
// however many context_ids resolve to it. Every consumer — the row fact,
// rank 5.4, §11.8's collateral exclusion, §11.6's held[] and the restore
// question — reads this shared table rather than probing again.
type PlanHolderIndex struct {
	ByContext map[string]BranchHolderInventory
}

// BuildPlanHolderIndex builds one BranchHolderInventory per distinct
// canonical common dir among the identities in need, and publishes it under
// every context_id in need that resolves to that common dir. ids is the full
// PlanContextIdentities table this invocation established; need lists the
// context_ids that actually require a holder fact — a context_id this
// invocation never asks about costs no process at all.
func BuildPlanHolderIndex(ids PlanContextIdentities, need []string) PlanHolderIndex {
	byCommonDir := make(map[string]BranchHolderInventory)
	byContext := make(map[string]BranchHolderInventory, len(need))
	for _, contextID := range need {
		identity, ok := ids[contextID]
		if !ok {
			continue
		}
		inv, cached := byCommonDir[identity.CommonDir]
		if !cached {
			inv = BuildBranchHolderInventory(identity.RepoRoot)
			byCommonDir[identity.CommonDir] = inv
		}
		byContext[contextID] = inv
	}
	return PlanHolderIndex{ByContext: byContext}
}

// ============================================================================
// Fetch-effect resolution (§11.4-§11.7): remote ladder, refspecs, legacy
// remote/branch files, local-branch destinations and submodule recursion.
// ============================================================================

// configSubsectionByLastDot extracts the subsection of a normalized
// "prefix<subsection>.variable" config key (prefix already includes its own
// trailing dot, e.g. "remote.") by splitting on the LAST dot rather than the
// first. Git variable names are always dot-free, so the segment after the
// final dot is always the variable and everything between the fixed prefix
// and that final dot is the subsection — robust even when the subsection
// itself contains dots (a remote or URL-base name that does), which a
// first-dot split would truncate.
func configSubsectionByLastDot(key, prefix string) (string, bool) {
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	rest := key[len(prefix):]
	last := strings.LastIndexByte(rest, '.')
	if last <= 0 {
		return "", false
	}
	return rest[:last], true
}

// remoteConfigNames returns every distinct config-defined remote name —
// every subsection of remote.<name>.* in the inventory — sorted for
// deterministic all-remotes iteration (§11.4's `fetch.all` effect) and for
// remotes[]'s own required name order.
func remoteConfigNames(inv GitConfigInventory) []string {
	seen := make(map[string]bool)
	for _, e := range inv.Entries {
		if name, ok := configSubsectionByLastDot(e.Key, "remote."); ok {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// entriesUnderRemote returns every inventory entry whose key is
// remote.<name>.<anything>, in inventory order.
func entriesUnderRemote(inv GitConfigInventory, name string) []GitConfigEntry {
	var out []GitConfigEntry
	for _, e := range inv.Entries {
		if base, ok := configSubsectionByLastDot(e.Key, "remote."); ok && base == name {
			out = append(out, e)
		}
	}
	return out
}

// applyInsteadOf rewrites rawURL through every configured
// url.<base>.insteadOf entry, using the longest-matching insteadOf VALUE
// (the old prefix string) — confirmed against git-config(1): "Any URL that
// starts with this value will be rewritten to start, instead, with
// [the base]... When more than one insteadOf strings match, the longest
// match is used." Returns the rewritten URL and true when some entry
// matched, or rawURL unchanged and false otherwise.
func applyInsteadOf(inv GitConfigInventory, rawURL string) (string, bool) {
	bestLen := -1
	bestBase := ""
	for _, e := range inv.Entries {
		if !strings.HasSuffix(e.Key, ".insteadof") {
			continue
		}
		base, ok := configSubsectionByLastDot(e.Key, "url.")
		if !ok || !e.ValuePresent || e.Value == "" {
			continue
		}
		if !strings.HasPrefix(rawURL, e.Value) {
			continue
		}
		if len(e.Value) > bestLen {
			bestLen = len(e.Value)
			bestBase = base
		}
	}
	if bestLen < 0 {
		return rawURL, false
	}
	return bestBase + rawURL[bestLen:], true
}

// effectivePruneFlag resolves a per-remote boolean key (remote.<name>.prune
// or remote.<name>.pruneTags) with the ordinary Git fallback to its global
// counterpart (fetch.prune / fetch.pruneTags) when the per-remote key is
// unset or invalid — confirmed against git-config(1)'s PRUNING section.
func effectivePruneFlag(inv GitConfigInventory, perRemoteKey, globalKey string) bool {
	if entry, ok := finalOccurrence(inv, perRemoteKey); ok {
		if b, valid := parseGitConfigBool(entry.Value, entry.ValuePresent); valid {
			return b
		}
	}
	if entry, ok := finalOccurrence(inv, globalKey); ok {
		if b, valid := parseGitConfigBool(entry.Value, entry.ValuePresent); valid {
			return b
		}
	}
	return false
}

// effectiveTagOpt resolves remote.<name>.tagOpt, whose only two meaningful
// stored values are Git's own flag-string literals "--no-tags"/"--tags";
// any other value, or its absence, is the ordinary auto-following default.
// There is no global fallback key for tagOpt — it is exclusively a
// per-remote setting in Git itself.
func effectiveTagOpt(inv GitConfigInventory, name string) string {
	entry, ok := finalOccurrence(inv, "remote."+name+".tagopt")
	if !ok || !entry.ValuePresent {
		return "auto-follow"
	}
	switch strings.TrimSpace(entry.Value) {
	case "--no-tags":
		return "no-tags"
	case "--tags":
		return "tags"
	default:
		return "auto-follow"
	}
}

// decomposeRefspec parses one remote.<name>.fetch value into a PlanRefspec:
// an optional leading "^" marks it negative (never forced), an optional
// leading "+" marks it forced, and the remainder splits on the first ":"
// into src/dst — a refspec with no colon is its own src with an empty dst
// (Git's own shorthand for "fetch this ref, do not write a tracking ref").
func decomposeRefspec(raw string) PlanRefspec {
	r := PlanRefspec{Raw: raw}
	rest := raw
	switch {
	case strings.HasPrefix(rest, "^"):
		r.Negative = true
		rest = rest[1:]
	case strings.HasPrefix(rest, "+"):
		r.Forced = true
		rest = rest[1:]
	}
	if idx := strings.IndexByte(rest, ':'); idx >= 0 {
		r.Src = rest[:idx]
		r.Dst = rest[idx+1:]
	} else {
		r.Src = rest
	}
	return r
}

// refspecDestinationCovers reports whether a refspec destination pattern dst
// covers namespace ns (e.g. "refs/tags/" or "refs/heads/"), stripping Git's
// single trailing "*" wildcard to obtain a fixed prefix and treating
// coverage as a prefix relationship in either direction — refs/* covers
// refs/tags/, and refs/tags/v* also covers refs/tags/ as a narrower subset;
// both are legitimate "this refspec can write into ns" answers. Coverage is
// a predicate over the pattern, never a literal-prefix comparison.
func refspecDestinationCovers(dst, ns string) bool {
	prefix := strings.TrimSuffix(dst, "*")
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(prefix, ns) || strings.HasPrefix(ns, prefix)
}

// refMatchesPattern reports whether a single, fully-qualified ref (e.g.
// "refs/heads/main") falls under a fetch destination pattern (e.g.
// "refs/heads/*" or an exact "refs/heads/main"). Git refspec destinations
// carry at most one trailing "*"; an exact pattern with none matches only
// itself.
func refMatchesPattern(dst, ref string) bool {
	if !strings.HasSuffix(dst, "*") {
		return dst == ref
	}
	prefix := strings.TrimSuffix(dst, "*")
	return strings.HasPrefix(ref, prefix)
}

// legacyRemoteFilePath is $GIT_DIR/remotes/<name> — read from the common
// dir, shared across every worktree exactly as config is (§11.4).
func legacyRemoteFilePath(commonDir, name string) string {
	return filepath.Join(commonDir, "remotes", name)
}

// readLegacyRemoteFile parses a legacy remotes/<name> file's "URL:" and
// "Pull:" lines. Git's own format is line-oriented, one directive per line;
// only the first "URL:" line and every "Pull:" line (a legacy refspec each)
// are meaningful to this feature.
func readLegacyRemoteFile(path string) (url string, refspecs []string, ok bool) {
	content, present, err := readSymlinkFreeFile(path, holderMarkerReadCap)
	if err != nil || !present {
		return "", nil, false
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "URL:"):
			if url == "" {
				url = strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			}
		case strings.HasPrefix(line, "Pull:"):
			refspecs = append(refspecs, strings.TrimSpace(strings.TrimPrefix(line, "Pull:")))
		}
	}
	return url, refspecs, true
}

// legacyBranchFilePath is $GIT_DIR/branches/<name> — also read from the
// common dir.
func legacyBranchFilePath(commonDir, name string) string {
	return filepath.Join(commonDir, "branches", name)
}

// readLegacyBranchFile parses a legacy branches/<name> file: a single line
// "<url>[#<remote-branch>]". Fetching it implies a refspec that writes the
// LOCAL branch refs/heads/<name> — Git's own documented behaviour for this
// ancient mechanism — from the named remote branch, or HEAD when none is
// given.
func readLegacyBranchFile(path, name string) (url string, refspec string, ok bool) {
	content, present, err := readSymlinkFreeFile(path, holderMarkerReadCap)
	if err != nil || !present {
		return "", "", false
	}
	line := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	if line == "" {
		return "", "", false
	}
	remoteBranch := "HEAD"
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		remoteBranch = line[idx+1:]
		line = line[:idx]
	}
	url = strings.TrimSpace(line)
	refspec = fmt.Sprintf("%s:refs/heads/%s", remoteBranch, name)
	return url, refspec, true
}

// applyRemoteURL fills a remote's three URL members from a raw configured
// URL (possibly empty), applying insteadOf rewriting uniformly regardless of
// where the raw URL itself came from, and publishing url_source:
// name-verbatim exactly where there is no URL at all (§11.4) — a remote
// name is never published as a URL.
func applyRemoteURL(remote *PlanFetchRemote, inv GitConfigInventory, rawURL, sourceIfNoRewrite string) {
	if rawURL == "" {
		remote.URLSource = "name-verbatim"
		return
	}
	remote.URLConfigured = rawURL
	effective, rewritten := applyInsteadOf(inv, rawURL)
	remote.URLEffective = effective
	if rewritten {
		remote.URLSource = "insteadOf-rewritten"
	} else {
		remote.URLSource = sourceIfNoRewrite
	}
}

// ResolveHeadBranch measures fetch_effect.head_branch (§11.4): HEAD's short
// branch name in dir, or nil when that HEAD is detached, unborn or
// unreadable. It reuses the shipped gitCurrentBranch
// (internal/checkout_sync.go) rather than re-issuing the same
// `symbolic-ref --short HEAD` read.
func ResolveHeadBranch(dir string) *string {
	branch, err := gitCurrentBranch(dir)
	if err != nil || branch == "" {
		return nil
	}
	return &branch
}

// ResolveRemoteName resolves Git's own remote-resolution ladder against
// headBranch (§11.4): branch.<name>.remote for name == *headBranch, then
// the sole configured remote (gated by CapSoleRemoteFallback, Git >= 2.37),
// then "origin". remote.pushDefault is excluded — this is the FETCH ladder,
// never the push one. A nil headBranch (detached, unborn or unreadable
// HEAD) skips straight to the sole-remote rung, exactly as Git does for a
// detached HEAD.
func ResolveRemoteName(inv GitConfigInventory, headBranch *string, caps GitCapabilities) string {
	if headBranch != nil && *headBranch != "" {
		key := "branch." + *headBranch + ".remote"
		if entry, ok := finalOccurrence(inv, key); ok && entry.ValuePresent && entry.Value != "" {
			return entry.Value
		}
	}
	if caps.CapSoleRemoteFallback {
		names := remoteConfigNames(inv)
		if len(names) == 1 {
			return names[0]
		}
	}
	return "origin"
}

// buildConfigRemote builds one config-sourced fetch_effect.remotes[] row
// from every remote.<name>.* entry already isolated by entriesUnderRemote.
func buildConfigRemote(inv GitConfigInventory, name string, entries []GitConfigEntry) PlanFetchRemote {
	remote := PlanFetchRemote{Name: name, Source: "config"}
	var rawURL string
	for _, e := range entries {
		switch {
		case e.Key == "remote."+name+".url" && e.ValuePresent:
			rawURL = e.Value
		case e.Key == "remote."+name+".fetch" && e.ValuePresent:
			remote.Refspecs = append(remote.Refspecs, decomposeRefspec(e.Value))
		}
	}
	applyRemoteURL(&remote, inv, rawURL, "config")
	return remote
}

// ResolveFetchRemote resolves one remote definition by name (§11.4): a
// config-defined remote.<name>.* set, else a legacy $GIT_DIR/remotes/<name>
// file, else a legacy $GIT_DIR/branches/<name> file, else "no remote at
// all" (the second return is false). commonDir is where the legacy files
// live — shared across worktrees exactly as config is. prune/prune_tags/
// tag_opt are resolved uniformly regardless of which of the three sources
// answered, since Git's fetch.prune/fetch.pruneTags globals apply
// independently of how the remote itself was defined.
func ResolveFetchRemote(inv GitConfigInventory, commonDir, name string, caps GitCapabilities) (PlanFetchRemote, bool) {
	var remote PlanFetchRemote
	found := false

	switch {
	case len(entriesUnderRemote(inv, name)) > 0:
		remote = buildConfigRemote(inv, name, entriesUnderRemote(inv, name))
		found = true
	default:
		if url, refspecs, ok := readLegacyRemoteFile(legacyRemoteFilePath(commonDir, name)); ok {
			remote = PlanFetchRemote{Name: name, Source: "legacy-remotes"}
			for _, raw := range refspecs {
				remote.Refspecs = append(remote.Refspecs, decomposeRefspec(raw))
			}
			applyRemoteURL(&remote, inv, url, "legacy-file")
			found = true
		} else if url, refspec, ok := readLegacyBranchFile(legacyBranchFilePath(commonDir, name), name); ok {
			remote = PlanFetchRemote{Name: name, Source: "legacy-branches", Refspecs: []PlanRefspec{decomposeRefspec(refspec)}}
			applyRemoteURL(&remote, inv, url, "legacy-file")
			found = true
		}
	}
	if !found {
		return PlanFetchRemote{}, false
	}

	remote.TagOpt = effectiveTagOpt(inv, name)
	remote.Prune = effectivePruneFlag(inv, "remote."+name+".prune", "fetch.prune")
	if caps.CapPruneTags {
		remote.PruneTags = effectivePruneFlag(inv, "remote."+name+".prunetags", "fetch.prunetags")
	}
	return remote, true
}

// ============================================================================
// Local-branch destinations (§11.6)
// ============================================================================

// refspecsToPatterns extracts every positive (non-negative) refspec from
// refspecs whose destination covers refs/heads/**, in the refspecs' own
// configured order — local_branch_destinations.patterns[] never sorts
// across remotes, only groups by the remotes[] name-sorted iteration order
// its caller already fixes (§11.6).
func refspecsToPatterns(remoteName string, refspecs []PlanRefspec) []PlanFetchRefspecPattern {
	var patterns []PlanFetchRefspecPattern
	for _, r := range refspecs {
		if r.Negative {
			continue
		}
		if !refspecDestinationCovers(r.Dst, "refs/heads/") {
			continue
		}
		patterns = append(patterns, PlanFetchRefspecPattern{Remote: remoteName, Dst: r.Dst, Forced: r.Forced})
	}
	return patterns
}

// coveredLocalBranches enumerates repoRoot's own EXISTING local branches —
// never the remote's, which knowing would itself require the very fetch
// being planned — whose refs/heads/<branch> ref matches at least one
// pattern's destination, returning short names (refs/heads/ stripped,
// unsorted, uncapped). A read failure returns ok: false so the caller can
// publish the required null rather than a false empty list (§11.6, rank
// 5.06's "non-empty OR UNKNOWN" trigger).
func coveredLocalBranches(repoRoot string, patterns []PlanFetchRefspecPattern) (branches []string, ok bool) {
	if len(patterns) == 0 {
		return []string{}, true
	}
	out, err := runGit(repoRoot, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return nil, false
	}
	var matched []string
	if out != "" {
		for _, ref := range strings.Split(out, "\n") {
			for _, p := range patterns {
				if refMatchesPattern(p.Dst, ref) {
					matched = append(matched, strings.TrimPrefix(ref, "refs/heads/"))
					break
				}
			}
		}
	}
	if matched == nil {
		matched = []string{}
	}
	return matched, true
}

// resolveLocalBranchDestinations resolves fetch_effect.local_branch_destinations
// (§11.6) from a resolved remote set's own positive refspecs. Patterns/
// Branches are nil only when the underlying computation genuinely could not
// be established — no contacted remote at all, or the local for-each-ref
// read itself failed — the specific "unknown" state rank 5.06 also triggers
// on; otherwise both are always a non-nil, possibly-empty slice. Held is nil
// specifically when holders.Available is false (the holder inventory itself
// could not be built), never merely because nothing is held. Held is
// computed from the FULL, uncapped branch list before the 200-entry display
// cap is applied to Branches, so a held branch beyond that cap is never
// silently dropped — "held[] ... never truncated" is a separate rule from
// "branches[] ... capped at 200".
func resolveLocalBranchDestinations(remotes []PlanFetchRemote, repoRoot string, holders BranchHolderInventory) PlanLocalBranchDestinations {
	if len(remotes) == 0 {
		return PlanLocalBranchDestinations{}
	}

	var patterns []PlanFetchRefspecPattern
	for _, remote := range remotes {
		patterns = append(patterns, refspecsToPatterns(remote.Name, remote.Refspecs)...)
	}
	if patterns == nil {
		patterns = []PlanFetchRefspecPattern{}
	}

	full, ok := coveredLocalBranches(repoRoot, patterns)
	if !ok {
		return PlanLocalBranchDestinations{Patterns: patterns}
	}

	var held []PlanBranchHold
	if holders.Available {
		held = []PlanBranchHold{}
		for _, branch := range full {
			if rec, isHeld := holders.ByBranch[branch]; isHeld {
				held = append(held, PlanBranchHold{Branch: branch, Worktree: rec.Worktree, Hold: string(rec.Mechanism)})
			}
		}
		sort.Slice(held, func(i, j int) bool {
			if held[i].Branch != held[j].Branch {
				return held[i].Branch < held[j].Branch
			}
			if held[i].Worktree != held[j].Worktree {
				return held[i].Worktree < held[j].Worktree
			}
			return held[i].Hold < held[j].Hold
		})
	}

	sort.Strings(full)
	branches := full
	truncated := false
	if len(branches) > 200 {
		branches = branches[:200]
		truncated = true
	}

	return PlanLocalBranchDestinations{
		Patterns:  patterns,
		Branches:  branches,
		Truncated: truncated,
		Held:      held,
	}
}

// ============================================================================
// Submodule recursion (§11.5)
// ============================================================================

// parseFetchRecurseSubmodulesValue applies fetch.recurseSubmodules's own
// grammar (§11.5): the case-insensitive literal "on-demand" wins outright;
// otherwise Git's ordinary --type=bool rule applies. ok is false for a
// value that matches neither.
func parseFetchRecurseSubmodulesValue(value string, present bool) (mode string, ok bool) {
	if present && strings.EqualFold(strings.TrimSpace(value), "on-demand") {
		return "on-demand", true
	}
	b, boolOK := parseGitConfigBool(value, present)
	if !boolOK {
		return "", false
	}
	if b {
		return "yes", true
	}
	return "no", true
}

// lastRecurseSubmodulesOccurrence finds whichever of fetch.recurseSubmodules
// and submodule.recurse has the LATEST position in the ordered inventory —
// genuinely interleaved across both keys, never "last occurrence of a
// single key checked in a fixed key precedence" (§11.5). It returns the
// entry together with the JSON mode_source name it corresponds to.
func lastRecurseSubmodulesOccurrence(inv GitConfigInventory) (entry GitConfigEntry, source string, found bool) {
	for _, e := range inv.Entries {
		switch e.Key {
		case "fetch.recursesubmodules":
			entry, source, found = e, "fetch.recurseSubmodules", true
		case "submodule.recurse":
			entry, source, found = e, "submodule.recurse", true
		}
	}
	return entry, source, found
}

// resolveConfigRecurseSubmodulesMode applies the correct grammar for
// whichever of the two keys won lastRecurseSubmodulesOccurrence:
// fetch.recurseSubmodules accepts boolean-or-"on-demand", while
// submodule.recurse is a plain boolean.
func resolveConfigRecurseSubmodulesMode(entry GitConfigEntry, source string) (string, bool) {
	if source == "fetch.recurseSubmodules" {
		return parseFetchRecurseSubmodulesValue(entry.Value, entry.ValuePresent)
	}
	b, ok := parseGitConfigBool(entry.Value, entry.ValuePresent)
	if !ok {
		return "", false
	}
	if b {
		return "yes", true
	}
	return "no", true
}

// gitmodulesContextRecurseSubmodules reads .gitmodules's own context-level
// [fetch] recurseSubmodules key through Git's three-source ladder — working
// tree file, then the index blob :.gitmodules, then HEAD:.gitmodules —
// never hand-parsing .gitmodules's own INI syntax (§11.5). ok is false only
// where every rung is absent/unreadable; the ladder stops at the first rung
// that answers.
func gitmodulesContextRecurseSubmodules(root string) (string, bool) {
	rungs := [][]string{
		{"config", "-f", ".gitmodules", "--get", "fetch.recursesubmodules"},
		{"config", "--blob=:.gitmodules", "--get", "fetch.recursesubmodules"},
		{"config", "--blob=HEAD:.gitmodules", "--get", "fetch.recursesubmodules"},
	}
	for _, args := range rungs {
		out, err := runGit(root, args...)
		if err != nil || out == "" {
			continue
		}
		if mode, ok := parseFetchRecurseSubmodulesValue(out, true); ok {
			return mode, true
		}
	}
	return "", false
}

// resolveSubmoduleMode resolves fetch_effect.submodule_recursion's mode and
// mode_source (§11.5): the last occurrence across fetch.recurseSubmodules
// and submodule.recurse in the ordered config; where neither occurs at all,
// .gitmodules's own context-level [fetch] recurseSubmodules through Git's
// three-source ladder; where that too answers nothing, "on-demand" from
// "default-on-demand". PlanSubmoduleRecursion.Mode/ModeSource are plain,
// non-nullable strings, so every context resolves to a concrete pair.
func resolveSubmoduleMode(inv GitConfigInventory, root string) (mode string, source string) {
	if entry, keySource, found := lastRecurseSubmodulesOccurrence(inv); found {
		if m, ok := resolveConfigRecurseSubmodulesMode(entry, keySource); ok {
			return m, keySource
		}
	}
	if m, ok := gitmodulesContextRecurseSubmodules(root); ok {
		return m, "gitmodules-fetch.recurseSubmodules"
	}
	return "on-demand", "default-on-demand"
}

// submoduleReachStoreCap and submoduleReachDepthCap are §11.5's exact
// caps: 50 stores per context, depth 3. Hitting either, or an unreadable
// member, yields reach: unknown and stops the walk; the list never claims
// completeness in that case.
const (
	submoduleReachStoreCap = 50
	submoduleReachDepthCap = 3
)

// listPresentGitlinks lists every gitlink (mode 160000) tree entry in
// root's own index — every "present" submodule regardless of whether it is
// configured in .gitmodules or currently populated (§11.5's candidate set:
// "every present gitlink submodule, whether or not it is active").
//
// The inventory is read through `git ls-files --stage -z` and raw-byte
// plumbing. `-z` is not optional: without it Git applies core.quotePath and
// rewrites every non-ASCII or special byte into a C-quoted, backslash-
// escaped rendering, so a submodule at `libs/ünïcode` or one whose path
// contains a tab, a newline or a quote would either be silently dropped or
// recorded under a name that no longer names a directory on disk — turning
// a populated submodule into an invisible one and defeating the fail-closed
// reach walk that consumes this list. Records are NUL-delimited, so the
// only in-record separator is the single TAB that Git guarantees between
// the stage metadata and the path, and every path byte survives verbatim.
func listPresentGitlinks(root string) ([]string, error) {
	out, err := runGitRaw(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue // the trailing NUL terminator, never a real entry
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(string(record[:tab]))
		if len(meta) < 1 || meta[0] != "160000" {
			continue
		}
		path := string(record[tab+1:])
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// submodulePopulated reports whether subRoot's own .git artefact is present
// as a plain directory or a gitdir-pointer file — never following a
// symlinked .git, which is treated as unreadable (err != nil) rather than
// silently guessed at either way.
func submodulePopulated(subRoot string) (populated bool, err error) {
	gitPath := filepath.Join(subRoot, ".git")
	info, statErr := os.Lstat(gitPath)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return false, nil
		}
		return false, statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing to follow symlinked artefact %s", gitPath)
	}
	return true, nil
}

// walkSubmoduleReach enumerates every present gitlink submodule reachable
// from repoRoot — active or not — recursing into each one's own working
// tree up to depth 3 and 50 total stores visited (§11.5). Hitting either
// cap, or failing to read a present gitlink's own submodule set or
// populated-ness, stops the walk and reports reach: unknown; the returned
// list is then a floor on what is present, never a claim of completeness.
// reach: none is reported exactly when the walk completes and finds zero
// present gitlinks; reach: bounded when it completes having found at least
// one.
func walkSubmoduleReach(repoRoot string) ([]PlanSubmoduleEntry, string) {
	var submodules []PlanSubmoduleEntry
	visited := 0
	unknown := false

	var walk func(root, prefix string, depth int)
	walk = func(root, prefix string, depth int) {
		if unknown {
			return
		}
		gitlinks, err := listPresentGitlinks(root)
		if err != nil {
			unknown = true
			return
		}
		for _, gl := range gitlinks {
			if visited >= submoduleReachStoreCap {
				unknown = true
				return
			}
			visited++
			path := gl
			if prefix != "" {
				path = prefix + "/" + gl
			}
			submodules = append(submodules, PlanSubmoduleEntry{Path: path})

			subRoot := filepath.Join(root, gl)
			populated, popErr := submodulePopulated(subRoot)
			if popErr != nil {
				unknown = true
				return
			}
			if !populated {
				continue // nothing further reachable from an unpopulated submodule
			}
			if depth >= submoduleReachDepthCap {
				// A populated submodule at the depth cap may itself contain
				// further gitlinks this walk is not permitted to examine.
				unknown = true
				return
			}
			walk(subRoot, path, depth+1)
			if unknown {
				return
			}
		}
	}
	walk(repoRoot, "", 1)

	if submodules == nil {
		submodules = []PlanSubmoduleEntry{}
	}
	sort.Slice(submodules, func(i, j int) bool { return submodules[i].Path < submodules[j].Path })

	switch {
	case unknown:
		return submodules, "unknown"
	case len(submodules) == 0:
		return submodules, "none"
	default:
		return submodules, "bounded"
	}
}

// ResolveSubmoduleRecursion resolves fetch_effect.submodule_recursion in
// full (§11.5): the context mode/mode_source, then — for every mode except
// "no", which never recurses into anything — the bounded reach walk over
// every present gitlink, whether or not it is active. A mode: no context
// short-circuits to reach: none with an empty submodules[] and issues no
// git process at all: nothing would be visited by a real fetch under it.
func ResolveSubmoduleRecursion(inv GitConfigInventory, root string) PlanSubmoduleRecursion {
	mode, source := resolveSubmoduleMode(inv, root)
	if mode == "no" {
		return PlanSubmoduleRecursion{Mode: mode, ModeSource: source, Reach: "none", Submodules: []PlanSubmoduleEntry{}}
	}
	submodules, reach := walkSubmoduleReach(root)
	return PlanSubmoduleRecursion{Mode: mode, ModeSource: source, Reach: reach, Submodules: submodules}
}

// ============================================================================
// ResolveFetchEffect — the §11.4 orchestrator
// ============================================================================

// ResolveFetchEffect resolves fetch_effect (§11.4-§11.7) in full for one
// execution context: head_branch, the remote set (a single resolved remote,
// or — where fetch.all is effectively configured true, gated by
// CapFetchAll, §16 — every configured remote), the five may_* booleans,
// local-branch destinations and submodule recursion. commonDir is where
// legacy remote/branch files and per-worktree hold state live; holders is
// this context's already-built BranchHolderInventory (§7.9), never rebuilt
// here. It serves both workspace modes identically: a caller in either mode
// supplies its own resolved repoRoot/commonDir/holders for the context it
// is evaluating.
func ResolveFetchEffect(inv GitConfigInventory, repoRoot, commonDir string, caps GitCapabilities, holders BranchHolderInventory) PlanFetchEffect {
	headBranch := ResolveHeadBranch(repoRoot)

	all := false
	if caps.CapFetchAll {
		if entry, ok := finalOccurrence(inv, "fetch.all"); ok {
			if b, valid := parseGitConfigBool(entry.Value, entry.ValuePresent); valid {
				all = b
			}
		}
	}

	var remotes []PlanFetchRemote
	if all {
		for _, name := range remoteConfigNames(inv) {
			if remote, ok := ResolveFetchRemote(inv, commonDir, name, caps); ok {
				remotes = append(remotes, remote)
			}
		}
	} else if name := ResolveRemoteName(inv, headBranch, caps); name != "" {
		if remote, ok := ResolveFetchRemote(inv, commonDir, name, caps); ok {
			remotes = append(remotes, remote)
		}
	}
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].Name < remotes[j].Name })
	contacted := len(remotes) > 0

	effect := PlanFetchEffect{
		All:                all,
		Contacted:          contacted,
		HeadBranch:         headBranch,
		SubmoduleRecursion: ResolveSubmoduleRecursion(inv, repoRoot),
		Remotes:            remotes,
	}

	if !contacted {
		return effect
	}

	for _, remote := range remotes {
		if remote.Prune {
			effect.MayDeleteRefs = true
		}
		tagCovering := false
		forcedTagCovering := false
		headCovering := false
		for _, r := range remote.Refspecs {
			if r.Negative {
				continue
			}
			if refspecDestinationCovers(r.Dst, "refs/tags/") {
				tagCovering = true
				if r.Forced {
					forcedTagCovering = true
				}
			}
			if refspecDestinationCovers(r.Dst, "refs/heads/") {
				headCovering = true
			}
		}
		if remote.Prune && (remote.PruneTags || tagCovering) {
			effect.MayDeleteTags = true
		}
		if forcedTagCovering {
			effect.MayClobberTags = true
		}
		if headCovering {
			effect.MayUpdateLocalBranches = true
		}
		if remote.Prune && headCovering {
			effect.MayDeleteLocalBranches = true
		}
	}

	effect.LocalBranchDestinations = resolveLocalBranchDestinations(remotes, repoRoot, holders)
	return effect
}

// ============================================================================
// Push read-helpers (§14.1a) — reused by the (separately owned) push-fact
// producers MeasurePushRemoteFacts / RefreshPushTrackingRefs, which resolve
// a remote and its refspecs exactly as fetch-effect resolution does and
// therefore share this file's ladder rather than re-deriving it.
// ============================================================================

// ReadRemoteTrackingRefs reads every refs/remotes/<remoteName>/* ref via one
// `for-each-ref`, keyed by the short name after that prefix — the read
// MeasurePushRemoteFacts / RefreshPushTrackingRefs use for
// PlanPushRemoteFacts.TrackingRefs (§14.1a). A repository with no such refs
// yet returns an empty, non-nil map, never an error.
func ReadRemoteTrackingRefs(root, remoteName string) (map[string]string, error) {
	prefix := "refs/remotes/" + remoteName + "/"
	out, err := runGit(root, "for-each-ref", "--format=%(refname)%00%(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	if out == "" {
		return refs, nil
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\x00", 2)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[0], prefix)
		if name == "" {
			continue
		}
		refs[name] = fields[1]
	}
	return refs, nil
}

// ============================================================================
// Lstat-first artefact helpers (§12.5/§12.5a) — shared by both
// rebase_plan_state.go inspectors (InspectCheckoutPlanState,
// InspectExternalPlanState), which own the PlanPresence/PlanUnreadableReason
// enums this outcome maps onto; kept independent of those types here so
// this file has no forward reference to the one written after it.
// ============================================================================

// ArtefactReadOutcome is the one-Lstat-then-at-most-one-read classification
// every state artefact read reduces to (§12.5's "exactly one read per
// artefact path" rule): Exists/IsSymlink/IsRegular are the raw Lstat facts —
// a symlinked path is reported here and MUST NEVER be opened or followed by
// a caller; NotRegular/TooLarge distinguish the two ways a real, non-symlink
// path can still fail to be a readable document; Content/OK report a
// completed read; Err carries any other Lstat/Open/Read failure (an
// io-error in the caller's own vocabulary).
type ArtefactReadOutcome struct {
	Exists     bool
	IsSymlink  bool
	IsRegular  bool
	NotRegular bool
	TooLarge   bool
	Content    string
	OK         bool
	Err        error
}

// ReadArtefactPath performs the one Lstat-first, symlink-refusing,
// size-capped read every rebase_plan_state.go inspector needs for one
// artefact path, reporting every distinction its PlanFilePresence/
// PlanUnreadableReason mapping requires directly rather than making the
// caller re-parse an error string. A missing path (fs.ErrNotExist) is the
// zero value — Exists: false, no error — distinguishing "absent" from every
// other outcome without the caller needing errors.Is at every call site.
func ReadArtefactPath(path string, maxBytes int64) ArtefactReadOutcome {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ArtefactReadOutcome{}
		}
		return ArtefactReadOutcome{Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ArtefactReadOutcome{Exists: true, IsSymlink: true}
	}
	if !info.Mode().IsRegular() {
		return ArtefactReadOutcome{Exists: true, NotRegular: true}
	}
	if info.Size() > maxBytes {
		return ArtefactReadOutcome{Exists: true, IsRegular: true, TooLarge: true}
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return ArtefactReadOutcome{Exists: true, IsRegular: true, Err: readErr}
	}
	return ArtefactReadOutcome{Exists: true, IsRegular: true, Content: string(data), OK: true}
}

// ProbeCommitExists is §13.3a rule 6's ONE existence probe: whether sha
// still names a commit object in dir. It is a single
// `rev-parse --verify <sha>^{commit}` process and nothing else — never a
// second resolution of a ref the executor will not resolve.
func ProbeCommitExists(dir, sha string) (bool, error) {
	if dir == "" || sha == "" {
		return false, fmt.Errorf("probe commit exists: empty dir or sha")
	}
	if _, err := runGit(dir, "rev-parse", "--verify", sha+"^{commit}"); err != nil {
		return false, nil
	}
	return true, nil
}
