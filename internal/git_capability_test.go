package internal

import (
	"os"
	"path/filepath"
	"testing"
)

// ============================================================================
// git_capability_test.go — TestGitCapability*
//
// The locked §16 rule 2 version parser over real-world version strings,
// ProbeGitVersion's Probed/OK semantics (successful probe, exec failure,
// unparseable output), and the six independent §16 rule 3 capability gates
// at their exact boundaries (2.17, 2.26, 2.26, 2.37, 2.38, 2.44).
// ============================================================================

// ---------------------------------------------------------------------------
// The locked parser (gitVersionPattern), exercised directly against
// real-world `git --version` first lines without shelling out — this is the
// pure-function half of "the locked parser over real-world version strings".
// ---------------------------------------------------------------------------

func TestGitCapability_VersionPatternRealWorldStrings(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantMatch bool
		wantMajor int
		wantMinor int
		wantPatch int
	}{
		{name: "macOS Xcode packager suffix", raw: "git version 2.39.5 (Apple Git-154)", wantMatch: true, wantMajor: 2, wantMinor: 39, wantPatch: 5},
		{name: "plain three-component", raw: "git version 2.43.0", wantMatch: true, wantMajor: 2, wantMinor: 43, wantPatch: 0},
		{name: "Git-for-Windows four-component", raw: "git version 2.30.1.windows.1", wantMatch: true, wantMajor: 2, wantMinor: 30, wantPatch: 1},
		{name: "minor-only, no patch component", raw: "git version 2.17", wantMatch: true, wantMajor: 2, wantMinor: 17, wantPatch: 0},
		{name: "Debian packager suffix", raw: "git version 2.30.2", wantMatch: true, wantMajor: 2, wantMinor: 30, wantPatch: 2},
		{name: "release-candidate patch suffix", raw: "git version 3.0.0-rc1", wantMatch: true, wantMajor: 3, wantMinor: 0, wantPatch: 0},
		{name: "future major version", raw: "git version 3.1.4", wantMatch: true, wantMajor: 3, wantMinor: 1, wantPatch: 4},
		{name: "empty string", raw: "", wantMatch: false},
		{name: "wrong case", raw: "GIT VERSION 2.30.0", wantMatch: false},
		{name: "non-numeric version", raw: "git version abc.def", wantMatch: false},
		{name: "unrelated program", raw: "some other program 2.3.4", wantMatch: false},
		{name: "missing minor component", raw: "git version 2", wantMatch: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := gitVersionPattern.FindStringSubmatch(tc.raw)
			if tc.wantMatch != (m != nil) {
				t.Fatalf("gitVersionPattern.FindStringSubmatch(%q) match = %v, want %v", tc.raw, m != nil, tc.wantMatch)
			}
			if !tc.wantMatch {
				return
			}
			gotMajor, gotMinor, gotPatch := parseCapturedVersionGroups(t, m)
			if gotMajor != tc.wantMajor || gotMinor != tc.wantMinor || gotPatch != tc.wantPatch {
				t.Errorf("%q parsed as %d.%d.%d, want %d.%d.%d", tc.raw, gotMajor, gotMinor, gotPatch, tc.wantMajor, tc.wantMinor, tc.wantPatch)
			}
		})
	}
}

// parseCapturedVersionGroups mirrors ProbeGitVersion's own group-to-int
// conversion (major/minor required, patch defaults to 0 when the optional
// third group did not participate), so this test exercises the same
// conversion rule the shipped function applies, not a reinvented one.
func parseCapturedVersionGroups(t *testing.T, m []string) (major, minor, patch int) {
	t.Helper()
	atoi := func(s string) int {
		if s == "" {
			return 0
		}
		n := 0
		for _, r := range s {
			n = n*10 + int(r-'0')
		}
		return n
	}
	return atoi(m[1]), atoi(m[2]), atoi(m[3])
}

// ---------------------------------------------------------------------------
// ProbeGitVersion's Probed/OK semantics, end to end (real exec.Command, no
// regex bypass) — successful real-host probe, exec failure (git not on
// PATH), and unparseable-but-successful output (a shimmed fake git).
// ---------------------------------------------------------------------------

func TestGitCapability_ProbeGitVersion_RealHost(t *testing.T) {
	v, err := ProbeGitVersion()
	if err != nil {
		t.Fatalf("ProbeGitVersion on the real host git: %v", err)
	}
	if !v.Probed {
		t.Error("Probed = false after a successful invocation, want true")
	}
	if !v.OK {
		t.Errorf("OK = false for real host git, want true (Raw=%q)", v.Raw)
	}
	if v.Major < 2 {
		t.Errorf("Major = %d, want >= 2 for any git installation this suite can run against", v.Major)
	}
	wantPrefix := "git version "
	if len(v.Raw) < len(wantPrefix) || v.Raw[:len(wantPrefix)] != wantPrefix {
		t.Errorf("Raw = %q, want it to start with %q", v.Raw, wantPrefix)
	}
}

func TestGitCapability_ProbeGitVersion_ExecFailure(t *testing.T) {
	// An empty PATH directory has no git binary at all: exec.Command itself
	// fails to start the process, which must still yield Probed:true (the
	// act of asking is itself the fact) alongside OK:false and a non-nil
	// error.
	t.Setenv("PATH", t.TempDir())

	v, err := ProbeGitVersion()
	if err == nil {
		t.Fatal("ProbeGitVersion with no git on PATH: want a non-nil error")
	}
	if !v.Probed {
		t.Error("Probed = false after an exec failure, want true")
	}
	if v.OK {
		t.Error("OK = true after an exec failure, want false")
	}
	if v.Major != 0 || v.Minor != 0 || v.Patch != 0 {
		t.Errorf("Major/Minor/Patch = %d/%d/%d after an exec failure, want all zero", v.Major, v.Minor, v.Patch)
	}
}

// installFakeGit points PATH at a directory containing only a `git` shell
// script that prints firstLine (optionally followed by more lines) to
// stdout and exits 0 — a real external process (never a Go-level mock of
// exec.Command), used only to observe ProbeGitVersion's own line-splitting
// and \r-trimming against output the real host's git cannot be made to
// produce on demand (an unparseable line, or a CRLF line ending).
func installFakeGit(t *testing.T, output string) {
	t.Helper()
	binDir := t.TempDir()
	outFile := filepath.Join(binDir, "version-output.txt")
	if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + outFile + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend, never replace: the script's own shebang needs the rest of
	// PATH to resolve `cat`. Only the git *lookup* itself needs binDir to
	// win, which prepending guarantees.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGitCapability_ProbeGitVersion_UnparseableOutput(t *testing.T) {
	installFakeGit(t, "not a git version string at all\n")

	v, err := ProbeGitVersion()
	if err != nil {
		t.Fatalf("ProbeGitVersion: %v", err)
	}
	if !v.Probed {
		t.Error("Probed = false, want true (the invocation itself succeeded)")
	}
	if v.OK {
		t.Error("OK = true for an unparseable first line, want false")
	}
	if v.Raw != "not a git version string at all" {
		t.Errorf("Raw = %q, want the verbatim (newline-stripped) first line", v.Raw)
	}
	if v.Major != 0 || v.Minor != 0 || v.Patch != 0 {
		t.Errorf("Major/Minor/Patch = %d/%d/%d, want all zero when unparseable", v.Major, v.Minor, v.Patch)
	}
}

func TestGitCapability_ProbeGitVersion_FakeRealWorldStrings(t *testing.T) {
	cases := []struct {
		name      string
		output    string // full fake stdout, CRLF/multi-line included
		wantRaw   string
		wantMajor int
		wantMinor int
		wantPatch int
	}{
		{
			name:      "CRLF line ending is trimmed from Raw",
			output:    "git version 2.40.0\r\n",
			wantRaw:   "git version 2.40.0",
			wantMajor: 2, wantMinor: 40, wantPatch: 0,
		},
		{
			name:      "only the first line is consulted",
			output:    "git version 2.41.0\nsome other trailer line\n",
			wantRaw:   "git version 2.41.0",
			wantMajor: 2, wantMinor: 41, wantPatch: 0,
		},
		{
			name:      "Apple packager suffix survives end to end",
			output:    "git version 2.39.5 (Apple Git-154)\n",
			wantRaw:   "git version 2.39.5 (Apple Git-154)",
			wantMajor: 2, wantMinor: 39, wantPatch: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installFakeGit(t, tc.output)
			v, err := ProbeGitVersion()
			if err != nil {
				t.Fatalf("ProbeGitVersion: %v", err)
			}
			if !v.Probed || !v.OK {
				t.Fatalf("Probed/OK = %v/%v, want true/true (Raw=%q)", v.Probed, v.OK, v.Raw)
			}
			if v.Raw != tc.wantRaw {
				t.Errorf("Raw = %q, want %q", v.Raw, tc.wantRaw)
			}
			if v.Major != tc.wantMajor || v.Minor != tc.wantMinor || v.Patch != tc.wantPatch {
				t.Errorf("parsed %d.%d.%d, want %d.%d.%d", v.Major, v.Minor, v.Patch, tc.wantMajor, tc.wantMinor, tc.wantPatch)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The six independent §16 rule 3 gate calculations, at their exact
// major.minor boundaries. Constructed GitVersion values are used directly
// (no exec needed) so every boundary — including ones this host's own
// installed git may not straddle — is covered deterministically.
// ---------------------------------------------------------------------------

func TestGitCapability_GateBoundaries(t *testing.T) {
	below := func(major, minor int) GitVersion {
		return GitVersion{Probed: true, OK: true, Major: major, Minor: minor}
	}

	cases := []struct {
		name string
		v    GitVersion
		gate func(GitCapabilities) bool
		want bool
	}{
		{"prune-tags below 2.17", below(2, 16), func(c GitCapabilities) bool { return c.CapPruneTags }, false},
		{"prune-tags at 2.17", below(2, 17), func(c GitCapabilities) bool { return c.CapPruneTags }, true},

		{"config-show-scope below 2.26", below(2, 25), func(c GitCapabilities) bool { return c.CapConfigShowScope }, false},
		{"config-show-scope at 2.26", below(2, 26), func(c GitCapabilities) bool { return c.CapConfigShowScope }, true},

		{"default-backend-merge below 2.26", below(2, 25), func(c GitCapabilities) bool { return c.CapDefaultBackendMerge }, false},
		{"default-backend-merge at 2.26", below(2, 26), func(c GitCapabilities) bool { return c.CapDefaultBackendMerge }, true},

		{"sole-remote-fallback below 2.37", below(2, 36), func(c GitCapabilities) bool { return c.CapSoleRemoteFallback }, false},
		{"sole-remote-fallback at 2.37", below(2, 37), func(c GitCapabilities) bool { return c.CapSoleRemoteFallback }, true},

		{"rebase-update-refs below 2.38", below(2, 37), func(c GitCapabilities) bool { return c.CapRebaseUpdateRefs }, false},
		{"rebase-update-refs at 2.38", below(2, 38), func(c GitCapabilities) bool { return c.CapRebaseUpdateRefs }, true},

		{"fetch-all below 2.44", below(2, 43), func(c GitCapabilities) bool { return c.CapFetchAll }, false},
		{"fetch-all at 2.44", below(2, 44), func(c GitCapabilities) bool { return c.CapFetchAll }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.gate(GitCapabilitiesForVersion(tc.v))
			if got != tc.want {
				t.Errorf("gate(%d.%d) = %v, want %v", tc.v.Major, tc.v.Minor, got, tc.want)
			}
		})
	}
}

func TestGitCapability_GateBoundaries_ConfigShowScopeAndDefaultBackendAreIndependentFields(t *testing.T) {
	// Both CapConfigShowScope and CapDefaultBackendMerge gate at the same
	// 2.26 boundary, but the doc comment on GitCapabilities is explicit that
	// they must never be collapsed into a shared field: assert both are
	// present and both actually equal true at 2.26, as two distinct
	// observations, not one.
	caps := GitCapabilitiesForVersion(GitVersion{Probed: true, OK: true, Major: 2, Minor: 26})
	if !caps.CapConfigShowScope {
		t.Error("CapConfigShowScope = false at 2.26, want true")
	}
	if !caps.CapDefaultBackendMerge {
		t.Error("CapDefaultBackendMerge = false at 2.26, want true")
	}
}

func TestGitCapability_GateBoundaries_PatchLevelNeverConsulted(t *testing.T) {
	// atLeast compares major.minor only: a huge patch on a version one minor
	// below a gate's threshold must not let that gate fire (no distro
	// backport can masquerade as meeting a minor-level gate).
	v := GitVersion{Probed: true, OK: true, Major: 2, Minor: 16, Patch: 999}
	if GitCapabilitiesForVersion(v).CapPruneTags {
		t.Errorf("CapPruneTags = true for 2.16.999, want false (patch must never bridge a minor gate)")
	}
}

func TestGitCapability_GateBoundaries_UnknownVersionYieldsAllGatesFalse(t *testing.T) {
	// An unknown version (Probed && !OK, or the zero value) yields every
	// gate false, which callers must read as "unknown", never "confirmed
	// absent" — GitCapabilitiesForVersion itself has no way to encode that
	// distinction, so this test only proves the all-false shape.
	for _, v := range []GitVersion{
		{},                                  // never probed
		{Probed: true, OK: false},           // probed, unparseable
		{Probed: true, OK: false, Major: 9}, // OK false must dominate even if Major/Minor were somehow set
	} {
		caps := GitCapabilitiesForVersion(v)
		if caps != (GitCapabilities{}) {
			t.Errorf("GitCapabilitiesForVersion(%+v) = %+v, want the all-false zero value", v, caps)
		}
	}
}

func TestGitCapability_GateBoundaries_VersionWellAboveAllGates(t *testing.T) {
	caps := GitCapabilitiesForVersion(GitVersion{Probed: true, OK: true, Major: 3, Minor: 0})
	want := GitCapabilities{
		CapPruneTags:           true,
		CapConfigShowScope:     true,
		CapDefaultBackendMerge: true,
		CapSoleRemoteFallback:  true,
		CapRebaseUpdateRefs:    true,
		CapFetchAll:            true,
	}
	if caps != want {
		t.Errorf("GitCapabilitiesForVersion(3.0) = %+v, want every gate true (%+v)", caps, want)
	}
}

// ---------------------------------------------------------------------------
// ProbeGitCapabilities composes ProbeGitVersion and GitCapabilitiesForVersion.
// ---------------------------------------------------------------------------

func TestGitCapability_ProbeGitCapabilities_ComposesProbeAndDerive(t *testing.T) {
	caps, v, err := ProbeGitCapabilities()
	if err != nil {
		t.Fatalf("ProbeGitCapabilities on the real host git: %v", err)
	}
	if !v.Probed || !v.OK {
		t.Fatalf("ProbeGitCapabilities returned Probed=%v OK=%v for the real host git, want true/true", v.Probed, v.OK)
	}
	want := GitCapabilitiesForVersion(v)
	if caps != want {
		t.Errorf("ProbeGitCapabilities capabilities = %+v, want GitCapabilitiesForVersion(returned version) = %+v", caps, want)
	}
}
