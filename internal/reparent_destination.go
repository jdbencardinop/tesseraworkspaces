package internal

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ============================================================================
// Destination and cutoff resolution (§4.1–§4.3c, §5.1–§5.3)
//
// Every probe in this file goes through runReparentGit, so every argv it
// emits selects its directory with exec.Cmd.Dir and never with `git -C`.
// reparentIsAncestor reproduces mergeBaseIsAncestor's exit-0/exit-1/error
// trichotomy for exactly that reason; mergeBaseIsAncestor itself stays
// untouched with every existing caller and is cited as the pattern only.
//
// One deliberate exception is recorded here rather than hidden: §4.3c
// requires the agreement adapter to call the shipped ResolveSyncBase
// UNMODIFIED, and that function reaches DefaultBranchIn, which does emit a
// `-C` argv. That argv belongs to the frozen shipped resolver the spec
// mandates consulting — the same way AC-031 scopes the frozen
// ancestryMergeBase out of this feature's boundary — and never to a
// reparent-emitted rebase, ref-transaction or push argv. Its answer cannot
// change the verdict either: §4.3b stores a full ref or a full OID, and
// neither can ever equal a short default-branch name, so the origin/<default>
// rewrite has nothing to rewrite.
// ============================================================================

// reparentHexToken is §4.3 step 2's candidate-object-id shape. Recognition is
// case-insensitive; the canonical form this feature stores and compares is
// always lowercase.
var reparentHexToken = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// reparentPseudoRefs is the closed set of tokens that are NOT a reparent
// destination and not a cutoff. A destination that moves whenever Git moves
// it is not a topology decision, which is also why the gitrevisions
// $GIT_DIR/<refname> rung is deliberately not enumerated below.
var reparentPseudoRefs = map[string]bool{
	"HEAD":             true,
	"FETCH_HEAD":       true,
	"ORIG_HEAD":        true,
	"MERGE_HEAD":       true,
	"CHERRY_PICK_HEAD": true,
	"REVERT_HEAD":      true,
	"REBASE_HEAD":      true,
	"AUTO_MERGE":       true,
	"BISECT_HEAD":      true,
}

// reparentPseudoRefDetail is the exact sentence a pseudo-ref token refuses
// with, for a destination and for a cutoff alike.
const reparentPseudoRefDetail = "pseudo refs are not a reparent destination; name a branch, a full ref, or an object id"

// ReparentDestination is ResolveReparentDestination's total output: the one
// source of the published destination facts. The plan, the executor, the CAS
// values, the metadata write and the post-condition all read this one result,
// so none of them can resolve the destination a second, differing way.
type ReparentDestination struct {
	// RequestedToken is exactly what the operator typed.
	RequestedToken string
	// StoredToken is the canonical token written to StackEntry.Base (§4.3b).
	// It is never the short token the operator typed.
	StoredToken string
	// Kind is stack-entry or literal-ref.
	Kind string
	// Ref is the full resolved ref; nil for an object-id destination.
	Ref *string
	// SHA is the pinned destination commit, canonical lowercase.
	SHA string
	// Repo is the normalized repo of a stack-entry parent; nil for a literal.
	Repo *string
	// Resolution is the §4.3b resolution token.
	Resolution string
	// Candidates carries every surviving enumerated candidate ref, sorted;
	// empty when the resolution was unambiguous.
	Candidates []string
	// EntryName is the destination stack entry's logical Name; "" for a
	// literal-ref destination.
	EntryName string
}

// Parent projects the destination into the document's new_parent block. The
// resolver-agreement verdict is filled in separately by the caller that runs
// §4.3c, so this projection never claims an agreement it did not check.
func (d ReparentDestination) Parent() ReparentParent {
	requested := d.RequestedToken
	stored := d.StoredToken
	sha := d.SHA
	parent := ReparentParent{
		RequestedToken: &requested,
		StoredToken:    &stored,
		Kind:           d.Kind,
		Ref:            d.Ref,
		SHA:            &sha,
		Repo:           d.Repo,
		Resolution:     d.Resolution,
		Candidates:     ensureSlice(d.Candidates),
	}
	return parent
}

// ============================================================================
// Primitive probes
// ============================================================================

// reparentIsAncestor reproduces mergeBaseIsAncestor's trichotomy through
// runReparentGit with Cmd.Dir: exit 0 is (true, nil), exit 1 is (false, nil),
// and anything else — including a failure to spawn git at all — is an error a
// caller surfaces as probe-failed. It is the ONLY form in which this feature
// ever emits `git merge-base`.
func reparentIsAncestor(dir, ancestor, descendant string) (bool, error) {
	res, err := runReparentGit(dir, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if res.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("merge-base --is-ancestor %s %s: %w", ancestor, descendant, err)
}

// reparentOIDWidth determines the repository's canonical OID width once per
// run (§4.3a step 1). sha1 means 40 and sha256 means 64; any other or
// unreadable value refuses capability-unsupported, because no other width is
// ever assumed.
func reparentOIDWidth(repoDir string) (int, error) {
	res, err := runReparentGit(repoDir, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return 0, reparentRefusal(ReparentRefusalCapabilityUnsupported,
			fmt.Sprintf("git rev-parse --show-object-format failed: %s", reparentGitMessage(res, err)))
	}
	switch format := strings.TrimSpace(string(res.Stdout)); format {
	case "sha1":
		return 40, nil
	case "sha256":
		return 64, nil
	default:
		return 0, reparentRefusal(ReparentRefusalCapabilityUnsupported,
			fmt.Sprintf("unsupported object format %q; reparent supports sha1 and sha256 only", format))
	}
}

// reparentGitMessage renders a failed probe's own words for a refusal detail:
// Git's stderr when it wrote any, else the Go error.
func reparentGitMessage(res reparentGitResult, err error) string {
	if msg := strings.TrimSpace(string(res.Stderr)); msg != "" {
		return msg
	}
	return err.Error()
}

// reparentPeelCommit runs `git rev-parse --verify --quiet --end-of-options
// <spec>^{commit}`. It returns (sha, true, nil) when spec names a commit,
// ("", false, nil) when it does not, and an error only for a probe failure —
// --quiet exits 1 for both "unknown" and "ambiguous", so a caller that needs
// to tell those apart must have disambiguated first (§4.3a).
func reparentPeelCommit(repoDir, spec string) (string, bool, error) {
	res, err := runReparentGit(repoDir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", spec+"^{commit}")
	if err != nil {
		if res.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("git rev-parse --verify %s^{commit} failed: %s", spec, reparentGitMessage(res, err)))
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if sha == "" {
		return "", false, nil
	}
	return strings.ToLower(sha), true, nil
}

// reparentRefCandidates is §4.3 step 3's five candidate full refs, in the
// order they are enumerated. The order is part of the contract: it is the
// order a candidate list is built in before sorting for display.
func reparentRefCandidates(token string) [5]string {
	return [5]string{
		"refs/" + token,
		"refs/tags/" + token,
		"refs/heads/" + token,
		"refs/remotes/" + token,
		"refs/remotes/" + token + "/HEAD",
	}
}

// reparentForEachRef runs ONE `git for-each-ref --format=%(refname)`
// invocation over the given patterns and returns every line it printed.
func reparentForEachRef(repoDir string, patterns ...string) ([]string, error) {
	args := append([]string{"for-each-ref", "--format=%(refname)"}, patterns...)
	res, err := runReparentGit(repoDir, nil, args...)
	if err != nil {
		return nil, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("git for-each-ref failed: %s", reparentGitMessage(res, err)))
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// reparentSurvivingCandidates enumerates the five candidates of §4.3 step 3
// in one for-each-ref call and keeps only lines that are EXACTLY equal to one
// of them, sorted. for-each-ref treats its operands as patterns, so an
// unfiltered result can contain refs the operator never named; this filter is
// the whole reason rev-parse is not used here.
func reparentSurvivingCandidates(repoDir, token string) ([]string, error) {
	candidates := reparentRefCandidates(token)
	lines, err := reparentForEachRef(repoDir, candidates[:]...)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate] = true
	}
	seen := make(map[string]bool, len(lines))
	var surviving []string
	for _, line := range lines {
		if allowed[line] && !seen[line] {
			seen[line] = true
			surviving = append(surviving, line)
		}
	}
	sort.Strings(surviving)
	return surviving, nil
}

// reparentFullRefExists verifies a `^refs/` token exists EXACTLY, by asking
// for-each-ref for that one pattern and requiring the identical string back.
// No enumeration is performed for a full ref.
func reparentFullRefExists(repoDir, token string) (bool, error) {
	lines, err := reparentForEachRef(repoDir, token)
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		if line == token {
			return true, nil
		}
	}
	return false, nil
}

// reparentDisambiguate returns every object id whose name begins with prefix
// (§4.3a step 3). The count deliberately includes every object type: a prefix
// matching one commit and one blob is ambiguous even though a later ^{commit}
// peel could pick the commit, because reparent requires a globally
// unambiguous object spelling.
func reparentDisambiguate(repoDir, prefix string) ([]string, error) {
	res, err := runReparentGit(repoDir, nil, "rev-parse", "--disambiguate="+strings.ToLower(prefix))
	if err != nil {
		// An empty answer with a non-zero status is Git reporting "no such
		// object", which is a normal answer here, not a probe failure.
		if strings.TrimSpace(string(res.Stdout)) == "" && res.ExitCode > 0 {
			return nil, nil
		}
		return nil, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("git rev-parse --disambiguate=%s failed: %s", prefix, reparentGitMessage(res, err)))
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, strings.ToLower(line))
		}
	}
	sort.Strings(out)
	return out, nil
}

// ============================================================================
// Literal-token resolution (§4.3, §4.3a, §4.3b)
// ============================================================================

// reparentLiteral is one literal token's resolution.
type reparentLiteral struct {
	Ref         *string
	SHA         string
	Resolution  string
	StoredToken string
	Candidates  []string
}

// resolveReparentLiteral resolves a literal token — a destination's or a
// cutoff's — through the identical namespace enumeration and object-id rules.
//
// ambiguousKind is the caller's own ambiguity kind: a destination refuses
// destination-ambiguous, while a cutoff refuses cutoff-unresolvable for every
// ambiguity it can exhibit (§5.3), because the cutoff is a boundary, not a
// destination, and one kind with a complete candidate list is more useful
// than two kinds with split details.
func resolveReparentLiteral(repoDir, token string, oidWidth int, unresolvableKind, ambiguousKind ReparentRefusalKind) (reparentLiteral, error) {
	if reparentPseudoRefs[token] {
		return reparentLiteral{Resolution: ReparentResolutionUnresolved},
			reparentRefusal(unresolvableKind, reparentPseudoRefDetail)
	}

	// 1. A full ref must exist exactly; no enumeration is performed.
	if strings.HasPrefix(token, "refs/") {
		exists, err := reparentFullRefExists(repoDir, token)
		if err != nil {
			return reparentLiteral{}, err
		}
		if !exists {
			return reparentLiteral{Resolution: ReparentResolutionUnresolved},
				reparentRefusal(unresolvableKind, fmt.Sprintf("ref %q does not exist", token))
		}
		sha, ok, err := reparentPeelCommit(repoDir, token)
		if err != nil {
			return reparentLiteral{}, err
		}
		if !ok {
			return reparentLiteral{Resolution: ReparentResolutionUnresolved},
				reparentRefusal(unresolvableKind, fmt.Sprintf("ref %q does not name a commit", token))
		}
		ref := token
		return reparentLiteral{Ref: &ref, SHA: sha, Resolution: ReparentResolutionRefFull, StoredToken: token}, nil
	}

	// 2/3. A hex token may be an object id; §4.3a decides, and falls through
	// to the short-name rung when it is not an object at all. The object
	// rung's own partial facts travel with its refusal: a refused token still
	// publishes a resolution inside the closed domain, and an ambiguous one
	// still publishes what it was ambiguous between.
	if reparentHexToken.MatchString(token) {
		object, isObject, err := resolveReparentObjectID(repoDir, token, oidWidth, unresolvableKind, ambiguousKind)
		if err != nil {
			return object, err
		}
		if isObject {
			// §4.3a step 4: an object id that is ALSO a ref name is
			// ambiguous, whichever rung recognized it.
			surviving, err := reparentSurvivingCandidates(repoDir, token)
			if err != nil {
				return reparentLiteral{}, err
			}
			if len(surviving) > 0 {
				return reparentLiteral{Resolution: ReparentResolutionAmbiguous, Candidates: surviving},
					reparentRefusal(ambiguousKind,
						fmt.Sprintf("%q is both object %s and ref %s; name the full object id or the full ref",
							token, object.SHA, strings.Join(surviving, ", ")))
			}
			return object, nil
		}
	}

	// 3. Short name: exactly five candidates, exact-equality filtered.
	surviving, err := reparentSurvivingCandidates(repoDir, token)
	if err != nil {
		return reparentLiteral{}, err
	}
	switch len(surviving) {
	case 0:
		return reparentLiteral{Resolution: ReparentResolutionUnresolved},
			reparentRefusal(unresolvableKind, fmt.Sprintf("%q does not resolve to a ref or an object id", token))
	case 1:
		// resolved below
	default:
		return reparentLiteral{Resolution: ReparentResolutionAmbiguous, Candidates: surviving},
			reparentRefusal(ambiguousKind,
				fmt.Sprintf("%q matches %s; re-run with the full ref", token, strings.Join(surviving, ", ")))
	}

	ref := surviving[0]
	sha, ok, err := reparentPeelCommit(repoDir, ref)
	if err != nil {
		return reparentLiteral{}, err
	}
	if !ok {
		return reparentLiteral{Resolution: ReparentResolutionUnresolved, Candidates: surviving},
			reparentRefusal(unresolvableKind, fmt.Sprintf("ref %q does not name a commit", ref))
	}
	return reparentLiteral{
		Ref:         &ref,
		SHA:         sha,
		Resolution:  reparentRefResolution(ref),
		StoredToken: ref,
	}, nil
}

// reparentRefResolution maps a resolved full ref to its §4.3b resolution
// token. The order matters: refs/remotes/ and refs/tags/ and refs/heads/ are
// checked before the refs/<other> catch-all.
func reparentRefResolution(ref string) string {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return ReparentResolutionRefHead
	case strings.HasPrefix(ref, "refs/tags/"):
		return ReparentResolutionRefTag
	case strings.HasPrefix(ref, "refs/remotes/"):
		return ReparentResolutionRefRemote
	default:
		return ReparentResolutionRefOther
	}
}

// resolveReparentObjectID implements §4.3a steps 2 and 3.
//
// It returns a reparentLiteral in EVERY outcome, so no caller has to
// reconstruct what the rung learned:
//
//   - object id resolved: ({SHA, raw-oid, StoredToken}, true, nil);
//   - not an object at all: (zero literal, false, nil) — the caller falls
//     through to the short-name rung, where the empty resolution is replaced;
//   - multi-match abbreviation: ({ambiguous, sorted candidate OIDs}, false,
//     refusal);
//   - non-committish or unresolvable object: ({unresolved}, false, refusal).
//
// The second result is meaningful only when the error is nil. Publishing the
// partial literal matters because resolution and candidates are closed-domain
// document cells: a refused token must never publish an empty resolution.
func resolveReparentObjectID(repoDir, token string, oidWidth int, unresolvableKind, ambiguousKind ReparentRefusalKind) (reparentLiteral, bool, error) {
	lowered := strings.ToLower(token)

	if len(token) == oidWidth {
		sha, ok, err := reparentPeelCommit(repoDir, lowered)
		if err != nil {
			return reparentLiteral{}, false, err
		}
		if !ok {
			return reparentLiteral{Resolution: ReparentResolutionUnresolved}, false,
				reparentRefusal(unresolvableKind,
					fmt.Sprintf("object %q does not resolve to a commit", token))
		}
		return reparentLiteral{SHA: sha, Resolution: ReparentResolutionRawOID, StoredToken: sha}, true, nil
	}

	matches, err := reparentDisambiguate(repoDir, lowered)
	if err != nil {
		return reparentLiteral{}, false, err
	}
	switch len(matches) {
	case 0:
		// Not an object id: the short-name rung decides.
		return reparentLiteral{}, false, nil
	case 1:
		// resolved below
	default:
		// The candidate list carries the sorted full OIDs the prefix matched,
		// which is what "re-run with the full object id" refers to; a ref
		// ambiguity carries refs in the same cell.
		return reparentLiteral{Resolution: ReparentResolutionAmbiguous, Candidates: matches}, false,
			reparentRefusal(ambiguousKind,
				fmt.Sprintf("%q matches %d objects (%s); name the full object id",
					token, len(matches), strings.Join(matches, ", ")))
	}

	sha, ok, err := reparentPeelCommit(repoDir, matches[0])
	if err != nil {
		return reparentLiteral{}, false, err
	}
	if !ok {
		// A single, unambiguous match that is not a commit: unresolved, with
		// no candidate list, because nothing was ambiguous.
		return reparentLiteral{Resolution: ReparentResolutionUnresolved}, false,
			reparentRefusal(unresolvableKind,
				fmt.Sprintf("object %s does not resolve to a commit", matches[0]))
	}
	return reparentLiteral{SHA: sha, Resolution: ReparentResolutionRawOID, StoredToken: sha}, true, nil
}

// ============================================================================
// ResolveReparentDestination (§4.1, §4.2, §4.3, §4.3b)
// ============================================================================

// ResolveReparentDestination resolves --onto into the single pinned
// destination this run uses everywhere.
//
// kind selects how the token is read: auto is entry-first (a token equal to
// some StackEntry.Name is that entry, otherwise it is a literal), entry
// requires an entry, and ref never consults entry names at all. Storing the
// full ref is what makes --onto-kind ref safe against an entry name that
// happens to match, and what stops ResolveSyncBase's origin/<default> rewrite
// from ever re-interpreting a token this feature chose.
//
// It never performs that rewrite itself: a reparent destination is exactly
// what the operator named, stored canonically.
func ResolveReparentDestination(repoDir string, stack Stack, target StackEntry, token, kind string, oidWidth int) (ReparentDestination, error) {
	return resolveReparentDestination(repoDir, stack, target, token, kind, oidWidth,
		func(parent, target StackEntry) bool { return SameStackRepo(parent.Repo, target.Repo) })
}

func ResolveReparentDestinationByRepoIdentity(repoDir string, stack Stack, target StackEntry, token, kind string, oidWidth int, identities map[string]string) (ReparentDestination, error) {
	if kind != "ref" {
		if parent, ok := reparentLookupEntry(stack, token); ok &&
			!SameStackRepo(parent.Repo, target.Repo) &&
			identities[parent.Name] != "" &&
			identities[parent.Name] == identities[target.Name] {
			return ReparentDestination{
					RequestedToken: token,
					Kind:           ReparentParentKindStackEntry,
					EntryName:      parent.Name,
					Resolution:     ReparentResolutionEntry,
				}, reparentEntryRefusal(ReparentRefusalDestinationResolverDivergent, parent.Name,
					fmt.Sprintf("destination entry %q uses repository token %q while target %q uses %q; aliases of one repository are unsupported until every stack resolver compares canonical repository identity",
						parent.Name, parent.Repo, target.Name, target.Repo))
		}
	}
	return resolveReparentDestination(repoDir, stack, target, token, kind, oidWidth,
		func(parent, target StackEntry) bool {
			parentID, parentOK := identities[parent.Name]
			targetID, targetOK := identities[target.Name]
			if parentOK && targetOK && parentID != "" && targetID != "" {
				return parentID == targetID
			}
			return SameStackRepo(parent.Repo, target.Repo)
		})
}

func resolveReparentDestination(repoDir string, stack Stack, target StackEntry, token, kind string, oidWidth int, sameRepo func(StackEntry, StackEntry) bool) (ReparentDestination, error) {
	if strings.TrimSpace(token) == "" {
		return ReparentDestination{}, reparentRefusal(ReparentRefusalDestinationUnset, "--onto requires a destination")
	}

	entry, isEntry := reparentLookupEntry(stack, token)
	switch kind {
	case "entry":
		if !isEntry {
			return ReparentDestination{}, reparentRefusal(ReparentRefusalDestinationKindMismatch,
				fmt.Sprintf("--onto-kind entry requires a stack entry name; %q is not an entry in this feature's stack.yaml", token))
		}
		return resolveReparentEntryDestination(repoDir, target, entry, token, sameRepo)
	case "ref":
		isEntry = false
	case "auto", "":
		// entry-first, decided by isEntry above
	default:
		return ReparentDestination{}, reparentRefusal(ReparentRefusalDestinationKindMismatch,
			fmt.Sprintf("--onto-kind must be one of: auto, entry, ref (got %q)", kind))
	}

	if isEntry {
		return resolveReparentEntryDestination(repoDir, target, entry, token, sameRepo)
	}

	literal, err := resolveReparentLiteral(repoDir, token, oidWidth,
		ReparentRefusalDestinationUnresolvable, ReparentRefusalDestinationAmbiguous)
	if err != nil {
		// The partially-resolved facts travel with the refusal so an
		// unavailable plan can publish resolution and candidates instead of
		// dropping what the run did learn. resolution is a closed-domain
		// document cell, so a rung that learned nothing at all — a failed
		// read-only probe — still publishes `unresolved` rather than an
		// empty string outside the domain.
		resolution := literal.Resolution
		if resolution == "" {
			resolution = ReparentResolutionUnresolved
		}
		return ReparentDestination{
			RequestedToken: token,
			Kind:           ReparentParentKindLiteralRef,
			Resolution:     resolution,
			Candidates:     literal.Candidates,
		}, err
	}
	return ReparentDestination{
		RequestedToken: token,
		StoredToken:    literal.StoredToken,
		Kind:           ReparentParentKindLiteralRef,
		Ref:            literal.Ref,
		SHA:            literal.SHA,
		Resolution:     literal.Resolution,
		Candidates:     literal.Candidates,
	}, nil
}

// reparentLookupEntry finds an entry by exact Name.
func reparentLookupEntry(stack Stack, name string) (StackEntry, bool) {
	for _, e := range stack.Branches {
		if e.Name == name {
			return e, true
		}
	}
	return StackEntry{}, false
}

// resolveReparentEntryDestination is §4.2: an in-stack parent resolves
// through refs/heads/<parent.GitBranch()>, matching stackBaseRef exactly.
func resolveReparentEntryDestination(repoDir string, target, parent StackEntry, requested string, sameRepo func(StackEntry, StackEntry) bool) (ReparentDestination, error) {
	if !sameRepo(parent, target) {
		return ReparentDestination{}, reparentEntryRefusal(ReparentRefusalCrossRepoClosure, parent.Name,
			fmt.Sprintf("destination entry %q is configured in repo %q but the target is in repo %q", parent.Name, parent.Repo, target.Repo))
	}
	if parent.Archived {
		return ReparentDestination{}, reparentEntryRefusal(ReparentRefusalAffectedArchived, parent.Name,
			fmt.Sprintf("destination entry %q is archived", parent.Name))
	}

	ref := "refs/heads/" + parent.GitBranch()
	exists, err := reparentFullRefExists(repoDir, ref)
	if err != nil {
		return ReparentDestination{}, err
	}
	if !exists {
		return ReparentDestination{}, reparentEntryRefusal(ReparentRefusalDestinationUnresolvable, parent.Name,
			fmt.Sprintf("ref %q does not exist", ref))
	}
	sha, ok, err := reparentPeelCommit(repoDir, ref)
	if err != nil {
		return ReparentDestination{}, err
	}
	if !ok {
		return ReparentDestination{}, reparentEntryRefusal(ReparentRefusalDestinationUnresolvable, parent.Name,
			fmt.Sprintf("ref %q does not name a commit", ref))
	}

	repo := parent.Repo
	return ReparentDestination{
		RequestedToken: requested,
		StoredToken:    parent.Name,
		Kind:           ReparentParentKindStackEntry,
		Ref:            &ref,
		SHA:            sha,
		Repo:           &repo,
		Resolution:     ReparentResolutionEntry,
		EntryName:      parent.Name,
	}, nil
}

// ============================================================================
// Cutoff ladders (§5.1, §5.2, §5.3)
//
// merge-base is never a cutoff. Every rung below either reads a recorded
// boundary, resolves an operator-supplied one, or takes the OLD PARENT's
// pre-image tip and proves it is an ancestor of the row's branch first.
// ============================================================================

// resolveReparentTargetCutoff walks §5.1's closed ladder for the target row.
//
// recordedSHA is the target's LastBaseSHA as the PRE-IMAGE snapshot captured
// it, never a live re-read: a cutoff that changes underneath a run is exactly
// the failure this ladder exists to prevent. suppliedToken is --cutoff's raw
// value, "" when absent.
func resolveReparentTargetCutoff(repoDir string, stack Stack, target StackEntry, recordedSHA, suppliedToken string, oidWidth int) (ReparentPlanCutoff, error) {
	cutoff := ReparentPlanCutoff{
		RecordedState: ReparentCutoffRecordAbsent,
		Provenance:    ReparentCutoffNone,
	}
	if recordedSHA != "" {
		recorded := recordedSHA
		cutoff.RecordedSHA = &recorded
	}
	if suppliedToken != "" {
		supplied := suppliedToken
		cutoff.SuppliedToken = &supplied
	}
	branchRef := "refs/heads/" + target.GitBranch()

	// Rungs 1 and 2: a recorded boundary is authoritative, and an
	// unresolvable one is fatal — a supplied token MUST NOT override it.
	if recordedSHA != "" {
		resolved, ok, err := reparentPeelCommit(repoDir, recordedSHA)
		if err != nil {
			return cutoff, err
		}
		if !ok {
			cutoff.RecordedState = ReparentCutoffRecordUnresolvable
			return cutoff, reparentEntryRefusal(ReparentRefusalCutoffUnresolvable, target.Name,
				fmt.Sprintf("recorded cutoff %s no longer resolves; restore or fetch the missing object before reparenting", recordedSHA))
		}
		cutoff.RecordedState = ReparentCutoffRecordPresent
		cutoff.ResolvedSHA = &resolved
		cutoff.Provenance = ReparentCutoffRecordedBySync

		if suppliedToken != "" {
			literal, err := resolveReparentLiteral(repoDir, suppliedToken, oidWidth,
				ReparentRefusalCutoffUnresolvable, ReparentRefusalCutoffUnresolvable)
			if err != nil {
				return cutoff, reparentAttributeEntry(err, target.Name)
			}
			suppliedSHA := literal.SHA
			cutoff.SuppliedSHA = &suppliedSHA
			if suppliedSHA != resolved {
				conflict := fmt.Sprintf("--cutoff resolves to %s but the recorded boundary is %s", suppliedSHA, resolved)
				cutoff.Conflict = &conflict
				return cutoff, reparentEntryRefusal(ReparentRefusalCutoffConflict, target.Name, conflict)
			}
		}
		return cutoff, nil
	}

	// Rung 3: an operator-supplied boundary, resolved by §5.3.
	if suppliedToken != "" {
		literal, err := resolveReparentLiteral(repoDir, suppliedToken, oidWidth,
			ReparentRefusalCutoffUnresolvable, ReparentRefusalCutoffUnresolvable)
		if err != nil {
			return cutoff, reparentAttributeEntry(err, target.Name)
		}
		suppliedSHA := literal.SHA
		cutoff.SuppliedSHA = &suppliedSHA
		cutoff.ResolvedSHA = &suppliedSHA
		cutoff.Provenance = ReparentCutoffOperatorSupplied
		return cutoff, nil
	}

	// Rung 4: the old parent's pre-image tip, usable only when it resolves
	// AND is an ancestor of the target's branch.
	tip, ok, err := resolveReparentOldParentTip(repoDir, stack, target, oidWidth)
	if err != nil {
		return cutoff, err
	}
	if ok {
		isAncestor, err := reparentIsAncestor(repoDir, tip, branchRef)
		if err != nil {
			return cutoff, reparentEntryRefusal(ReparentRefusalProbeFailed, target.Name, err.Error())
		}
		cutoff.AncestorOfBranch = &isAncestor
		if isAncestor {
			resolved := tip
			cutoff.ResolvedSHA = &resolved
			cutoff.Provenance = ReparentCutoffOldParentTip
			return cutoff, nil
		}
	}

	// Rung 5.
	return cutoff, reparentEntryRefusal(ReparentRefusalCutoffAbsent, target.Name,
		fmt.Sprintf("no replay boundary is recorded for %q; supply --cutoff <ref>, or establish a baseline with: tws sync <feature> --only %s", target.Name, target.Name))
}

// resolveReparentDescendantCutoff walks §5.2's closed ladder. --cutoff never
// applies to a descendant: a descendant MUST use its own snapshotted
// boundary, and the target's old tip is never substituted for it.
func resolveReparentDescendantCutoff(repoDir string, stack Stack, row StackEntry, recordedSHA string, oidWidth int) (ReparentPlanCutoff, error) {
	cutoff := ReparentPlanCutoff{
		RecordedState: ReparentCutoffRecordAbsent,
		Provenance:    ReparentCutoffNone,
	}
	if recordedSHA != "" {
		recorded := recordedSHA
		cutoff.RecordedSHA = &recorded
	}
	branchRef := "refs/heads/" + row.GitBranch()

	if recordedSHA != "" {
		resolved, ok, err := reparentPeelCommit(repoDir, recordedSHA)
		if err != nil {
			return cutoff, err
		}
		if !ok {
			cutoff.RecordedState = ReparentCutoffRecordUnresolvable
			return cutoff, reparentEntryRefusal(ReparentRefusalCutoffUnresolvable, row.Name,
				fmt.Sprintf("recorded cutoff %s for %q no longer resolves; restore or fetch the missing object before reparenting", recordedSHA, row.Name))
		}
		cutoff.RecordedState = ReparentCutoffRecordPresent
		cutoff.ResolvedSHA = &resolved
		cutoff.Provenance = ReparentCutoffRecordedBySync
		return cutoff, nil
	}

	tip, ok, err := resolveReparentOldParentTip(repoDir, stack, row, oidWidth)
	if err != nil {
		return cutoff, err
	}
	if ok {
		isAncestor, err := reparentIsAncestor(repoDir, tip, branchRef)
		if err != nil {
			return cutoff, reparentEntryRefusal(ReparentRefusalProbeFailed, row.Name, err.Error())
		}
		cutoff.AncestorOfBranch = &isAncestor
		if isAncestor {
			resolved := tip
			cutoff.ResolvedSHA = &resolved
			cutoff.Provenance = ReparentCutoffOldParentTip
			return cutoff, nil
		}
	}

	return cutoff, reparentEntryRefusal(ReparentRefusalDescendantCutoffAbsent, row.Name,
		fmt.Sprintf("no replay boundary is recorded for descendant %q; establish descendant baselines first with: tws sync <feature> --from %s", row.Name, row.Name))
}

// resolveReparentOldParentTip resolves an entry's CONFIGURED parent to its
// pre-image commit, applying §4.2's entry rule and §4.3's literal rules to
// the old token. An unresolvable or ambiguous old token is not an error here:
// it simply means this rung produced no boundary and the ladder moves on. A
// genuine probe failure is still returned.
func resolveReparentOldParentTip(repoDir string, stack Stack, entry StackEntry, oidWidth int) (string, bool, error) {
	if entry.Base == "" {
		return "", false, nil
	}
	if parent, found := reparentLookupEntry(stack, entry.Base); found && SameStackRepo(parent.Repo, entry.Repo) {
		return reparentPeelCommit(repoDir, "refs/heads/"+parent.GitBranch())
	}
	literal, err := resolveReparentLiteral(repoDir, entry.Base, oidWidth,
		ReparentRefusalCutoffUnresolvable, ReparentRefusalCutoffUnresolvable)
	if err != nil {
		var refusal *ReparentRefusalError
		if asReparentRefusal(err, &refusal) && refusal.Kind != ReparentRefusalProbeFailed {
			return "", false, nil
		}
		return "", false, err
	}
	return literal.SHA, true, nil
}

// reparentAttributeEntry stamps an entry name onto a document-level reparent
// refusal, so a cutoff refusal raised by the shared literal resolver is
// reported against the row that asked for it.
func reparentAttributeEntry(err error, entry string) error {
	var refusal *ReparentRefusalError
	if !asReparentRefusal(err, &refusal) {
		return err
	}
	if refusal.Entry != nil {
		return err
	}
	name := entry
	return &ReparentRefusalError{Kind: refusal.Kind, Entry: &name, Detail: refusal.Detail, StatePreserved: refusal.StatePreserved}
}

// asReparentRefusal is errors.As for *ReparentRefusalError, kept as one
// helper so every call site spells the assertion identically.
func asReparentRefusal(err error, target **ReparentRefusalError) bool {
	return errors.As(err, target)
}

// ============================================================================
// Resolver agreement (§4.3c)
// ============================================================================

// resolveReparentAgreement confirms that all four base resolvers, given the
// STORED token, agree on the pinned destination commit.
//
// postImage is the stack this run would write — the target's Base already
// replaced by the stored token — because that is the graph the very next
// `tws sync` and `tws stack status` will read. Resolvers 2-4 return ref or
// token spellings rather than a uniformly peeled OID, so each answer is
// normalized with `rev-parse --verify --quiet --end-of-options <answer>^{commit}`
// before comparison. That normalization is load-bearing for annotated tags:
// the checkout resolver's bare rev-parse observes the TAG OBJECT's id, while
// the normalized answer must be the peeled commit.
//
// The shipped resolvers themselves are called unmodified.
func reparentSyncDefaultBranch(repoDir string) string {
	if res, err := runReparentGit(repoDir, nil, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "origin/")
	}
	if res, err := runReparentGit(repoDir, nil, "symbolic-ref", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(string(res.Stdout))
	}
	return "main"
}

func resolveReparentAgreement(repoDir string, postImage Stack, target StackEntry, destinationKind, storedToken, pinnedSHA string) (ReparentResolverAgreement, error) {
	agreement := ReparentResolverAgreement{Checked: true}
	if destinationKind != ReparentParentKindStackEntry {
		for _, entry := range postImage.Branches {
			if entry.Name != storedToken {
				continue
			}
			return agreement, reparentRefusal(ReparentRefusalDestinationResolverDivergent, fmt.Sprintf(
				"canonical literal destination %q is also the logical stack entry name %q; sync and status resolve stack entry names first, so this stored token would change meaning when that entry moves",
				storedToken, entry.Name))
		}
	}
	agreed := true

	record := func(name, rawAnswer string, normalized string, ok bool, failure string) {
		verdict := ReparentResolverVerdict{Name: name}
		if ok {
			sha := normalized
			verdict.SHA = &sha
			verdict.Agrees = normalized == pinnedSHA
			if !verdict.Agrees {
				detail := fmt.Sprintf("resolver %s answered %q for stored token %q, which normalizes to %s, not the pinned %s",
					name, rawAnswer, storedToken, normalized, pinnedSHA)
				verdict.Detail = &detail
			}
		} else {
			detail := failure
			verdict.Detail = &detail
		}
		if !verdict.Agrees {
			agreed = false
		}
		agreement.Resolvers = append(agreement.Resolvers, verdict)
	}

	// 1. ResolveReparentDestination — already a peeled OID by construction.
	record(ReparentResolverDestination, storedToken, pinnedSHA, true, "")

	// 2. ResolveSyncBase's pure half, fed by the default branch measured
	// through reparent's Dir-based runner so this boundary never emits `-C`.
	syncBase := ResolveSyncBaseWithDefaultBranch(postImage, target, reparentSyncDefaultBranch(repoDir))
	if syncBase.Base == "" {
		record(ReparentResolverSyncBase, "", "", false,
			fmt.Sprintf("resolver %s produced no base for stored token %q (kind %s); expected %s",
				ReparentResolverSyncBase, storedToken, syncBase.Kind, pinnedSHA))
	} else {
		normalized, ok, err := reparentPeelCommit(repoDir, syncBase.Base)
		if err != nil {
			return agreement, err
		}
		record(ReparentResolverSyncBase, syncBase.Base, normalized, ok,
			fmt.Sprintf("resolver %s answered %q for stored token %q, which does not resolve to a commit; expected %s",
				ReparentResolverSyncBase, syncBase.Base, storedToken, pinnedSHA))
	}

	// 3. stackBaseRef.
	baseRef, baseKind := stackBaseRef(postImage, target)
	if baseKind == StackBaseNone || baseRef == "" {
		record(ReparentResolverStackBase, "", "", false,
			fmt.Sprintf("resolver %s produced no base ref for stored token %q; expected %s",
				ReparentResolverStackBase, storedToken, pinnedSHA))
	} else {
		normalized, ok, err := reparentPeelCommit(repoDir, baseRef)
		if err != nil {
			return agreement, err
		}
		record(ReparentResolverStackBase, baseRef, normalized, ok,
			fmt.Sprintf("resolver %s answered %q for stored token %q, which does not resolve to a commit; expected %s",
				ReparentResolverStackBase, baseRef, storedToken, pinnedSHA))
	}

	// 4. The checkout executor's base pick, through the extracted selector so
	// a future divergence is a compile error rather than a silent mismatch.
	checkoutToken, hasToken := checkoutBaseTokenFor(postImage, target)
	if !hasToken {
		record(ReparentResolverCheckout, "", "", false,
			fmt.Sprintf("resolver %s produced no base token for stored token %q; expected %s",
				ReparentResolverCheckout, storedToken, pinnedSHA))
	} else {
		// The shipped executor resolves its token with a bare rev-parse, so
		// the raw answer is deliberately the unpeeled object id.
		raw, rawOK, err := reparentRevParse(repoDir, checkoutToken)
		if err != nil {
			return agreement, err
		}
		if !rawOK {
			record(ReparentResolverCheckout, checkoutToken, "", false,
				fmt.Sprintf("resolver %s answered %q for stored token %q, which does not resolve; expected %s",
					ReparentResolverCheckout, checkoutToken, storedToken, pinnedSHA))
		} else {
			normalized, ok, err := reparentPeelCommit(repoDir, raw)
			if err != nil {
				return agreement, err
			}
			record(ReparentResolverCheckout, raw, normalized, ok,
				fmt.Sprintf("resolver %s answered %q (%s) for stored token %q, which does not peel to a commit; expected %s",
					ReparentResolverCheckout, checkoutToken, raw, storedToken, pinnedSHA))
		}
	}

	agreement.Agreed = &agreed
	if !agreed {
		for _, verdict := range agreement.Resolvers {
			if verdict.Agrees {
				continue
			}
			detail := fmt.Sprintf("resolver %s disagrees about stored token %q", verdict.Name, storedToken)
			if verdict.Detail != nil {
				detail = *verdict.Detail
			}
			return agreement, reparentRefusal(ReparentRefusalDestinationResolverDivergent, detail)
		}
	}
	return agreement, nil
}

// reparentRevParse runs a bare `git rev-parse <token>` — the exact shape the
// shipped checkout executor uses — and reports whether it resolved. It is
// used only to capture that resolver's RAW answer for the agreement block;
// every comparison is made on the normalized, peeled value.
func reparentRevParse(repoDir, token string) (string, bool, error) {
	res, err := runReparentGit(repoDir, nil, "rev-parse", token)
	if err != nil {
		if res.ExitCode > 0 {
			return "", false, nil
		}
		return "", false, reparentRefusal(ReparentRefusalProbeFailed,
			fmt.Sprintf("git rev-parse %s failed: %s", token, reparentGitMessage(res, err)))
	}
	answer := strings.TrimSpace(string(res.Stdout))
	if answer == "" {
		return "", false, nil
	}
	return strings.ToLower(answer), true, nil
}
