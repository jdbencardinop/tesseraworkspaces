package internal

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ============================================================================
// GitVersion and the locked §16 rule 2 parser
// ============================================================================

// GitVersion is the locked §16 rule 2 probe result. Probed is always true
// once ProbeGitVersion has run — the act of asking is itself the fact — and
// OK is true only when a version could be parsed from the first line of
// `git --version`. Raw is that first line, verbatim, kept so a rank 5.9
// probe-failed detail can quote exactly what the host printed even when
// parsing fails.
type GitVersion struct {
	Probed bool
	OK     bool
	Raw    string
	Major  int
	Minor  int
	Patch  int
}

// gitVersionPattern is the locked parser. Git's own first line has always
// been "git version X.Y[.Z]" followed by an arbitrary packager suffix on the
// same line — "git version 2.39.5 (Apple Git-154)", "2.30.1.windows.1", a
// Debian/Homebrew build tag, or nothing at all. Only the leading numeric
// major.minor[.patch] token is consumed; anything after it is intentionally
// left unmatched rather than rejected, so a real host is never misclassified
// as unparseable merely because its packager appended free text.
var gitVersionPattern = regexp.MustCompile(`^git version (\d+)\.(\d+)(?:\.(\d+))?`)

// ProbeGitVersion runs `git --version` once and applies the locked parser to
// its first line. An exec failure or an unparseable first line both yield
// Probed: true, OK: false, every Major/Minor/Patch left at zero — the caller
// (§16 rule 1) is the one that raises the document-level rank 5.9
// probe-failed blocker and treats every capability gate as unknown; this
// function only reports what it observed and never performs that
// classification itself.
func ProbeGitVersion() (GitVersion, error) {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return GitVersion{Probed: true, OK: false}, err
	}

	text := string(out)
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	raw := strings.TrimRight(text, "\r")

	v := GitVersion{Probed: true, Raw: raw}
	m := gitVersionPattern.FindStringSubmatch(raw)
	if m == nil {
		return v, nil
	}

	major, majErr := strconv.Atoi(m[1])
	minor, minErr := strconv.Atoi(m[2])
	if majErr != nil || minErr != nil {
		// Unreachable for the pattern's own \d+ groups, but guarded anyway so
		// a malformed match can never silently masquerade as OK: true.
		return v, nil
	}
	patch := 0
	if m[3] != "" {
		if p, patchErr := strconv.Atoi(m[3]); patchErr == nil {
			patch = p
		}
	}

	v.OK = true
	v.Major = major
	v.Minor = minor
	v.Patch = patch
	return v, nil
}

// ============================================================================
// GitCapabilities — the closed §16 rule 3 six-gate table
// ============================================================================

// GitCapabilities is the closed §16 rule 3 six-gate table, one bool per gate,
// each named for the Git version at which this feature's own behaviour
// changes. An unknown GitVersion (Probed && !OK) yields every gate false via
// GitCapabilitiesForVersion; a caller MUST read that as "unknown", never as
// "absent on this host" — §16 rule 1 already raises the document-level rank
// 5.9 probe-failed blocker in that case, so no gate here is ever consulted
// for a real decision once that blocker has fired.
type GitCapabilities struct {
	// CapPruneTags is `fetch --prune-tags`, Git >= 2.17.
	CapPruneTags bool

	// CapConfigShowScope is `git config --list --show-scope`, Git >= 2.26.
	// Below this gate — or with an unknown version — the plan/guard route
	// cannot tell a key's scope apart from its value at all, so it refuses
	// with rank 5.9 probe-failed rather than publish a scope-less inventory
	// as though it were scoped, and rather than silently drop
	// config_issues[].source (§11.7).
	CapConfigShowScope bool

	// CapDefaultBackendMerge is Git >= 2.26, the version at which
	// rebase.backend — and the merge-backed default effective_backend row 3-5
	// model when it is set or absent — first exists. Kept as its own field,
	// never collapsed with CapConfigShowScope even though both gate at 2.26,
	// because the two have opposite consequences: one refuses the
	// invocation outright, the other only changes how an older host's
	// built-in apply-backend default is modelled (§16 rule 3a).
	CapDefaultBackendMerge bool

	// CapSoleRemoteFallback is Git's own "exactly one configured remote"
	// rung of the remote-resolution ladder (§11.4), Git >= 2.37.
	CapSoleRemoteFallback bool

	// CapRebaseUpdateRefs is `git rebase --update-refs`, Git >= 2.38. Its
	// predicate is argv-derived (§16 rule 3b): a row is gated on it only
	// where the argv THAT ROW publishes actually carries --update-refs, so a
	// host below this gate cannot run that specific argv at all and the
	// controlled route refuses with rank 5.9 probe-failed above any
	// effective_backend classification of it.
	CapRebaseUpdateRefs bool

	// CapFetchAll is `fetch.all`, Git >= 2.44.
	CapFetchAll bool
}

// atLeast reports whether v is a known version at or above major.minor.
// Patch is deliberately excluded: none of the six §16 gates are patch-level,
// so comparing it would let a distro's backport patch release
// (2.25.<huge>, say) masquerade as meeting a minor-level gate it does not.
func (v GitVersion) atLeast(major, minor int) bool {
	if !v.OK {
		return false
	}
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

// GitCapabilitiesForVersion derives the six §16 rule 3 gates from a probed
// GitVersion. An unknown version (Probed && !OK, or Probed: false) yields the
// zero value — every gate false — which callers must read as "unknown", not
// "confirmed absent".
func GitCapabilitiesForVersion(v GitVersion) GitCapabilities {
	return GitCapabilities{
		CapPruneTags:           v.atLeast(2, 17),
		CapConfigShowScope:     v.atLeast(2, 26),
		CapDefaultBackendMerge: v.atLeast(2, 26),
		CapSoleRemoteFallback:  v.atLeast(2, 37),
		CapRebaseUpdateRefs:    v.atLeast(2, 38),
		CapFetchAll:            v.atLeast(2, 44),
	}
}

// ============================================================================
// ReparentGitCapabilities — safe-reparent's additive gate pair (§9.10)
//
// The six-gate GitCapabilities table above is frozen and MUST NOT be
// extended. The two facts below sit BESIDE it because neither is part of
// safe-reparent's required minimum: that minimum is the existing
// GitCapabilities.CapRebaseUpdateRefs gate (Git >= 2.38), which is already a
// sufficient floor for every core requirement — update-ref --stdin's
// start/prepare/commit verbs (2.27), --end-of-options (2.24),
// --no-rebase-merges (2.34), --disambiguate (long-standing), git switch
// (2.23) and the merge backend default (2.26) are all older — so no new
// version gate is invented for them.
// ============================================================================

// ReparentGitCapabilities is safe-reparent's own two-fact table. Each field
// gates exactly one optional behaviour and never the run as a whole:
// CapForceIfIncludes gates only the remote follow-up lease rule, and
// CapRefBackendKnown gates only which probe the ref-backend question uses.
// An unknown GitVersion yields the zero value — both false — which callers
// MUST read as "unknown" and resolve by taking the conservative branch, never
// by assuming absence and never by refusing.
type ReparentGitCapabilities struct {
	// CapForceIfIncludes is `git push --force-if-includes`, Git >= 2.30.
	CapForceIfIncludes bool

	// CapRefBackendKnown is `git rev-parse --show-ref-format`, Git >= 2.45.
	// When it is false the ref backend is probed with
	// `git config --get extensions.refStorage` instead, and an unresolved
	// answer is reported as "unknown" and treated exactly as "files".
	CapRefBackendKnown bool
}

// ReparentGitCapabilitiesForVersion derives the two additive gates from a
// probed GitVersion, reusing the same deliberately patch-blind atLeast
// comparison the frozen table uses. It never consults, changes or shadows
// GitCapabilitiesForVersion.
func ReparentGitCapabilitiesForVersion(v GitVersion) ReparentGitCapabilities {
	return ReparentGitCapabilities{
		CapForceIfIncludes: v.atLeast(2, 30),
		CapRefBackendKnown: v.atLeast(2, 45),
	}
}

// ProbeGitCapabilities composes ProbeGitVersion and GitCapabilitiesForVersion,
// the one call a controlled route needs to obtain both the raw probe (for its
// own rank 5.9 probe-failed detail, §16 rule 1) and the derived gate table in
// a single step.
func ProbeGitCapabilities() (GitCapabilities, GitVersion, error) {
	v, err := ProbeGitVersion()
	return GitCapabilitiesForVersion(v), v, err
}
