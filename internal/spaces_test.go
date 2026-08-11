package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func testAnchor(t *testing.T, root string, mode WorkspaceMode) SpacesAnchor {
	t.Helper()
	return SpacesAnchor{Root: root, Canon: canonicalize(root), Mode: mode}
}

func writeSpacesFixture(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, spacesFileName), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func countSpacesReads(t *testing.T) *int {
	t.Helper()
	count := 0
	spacesReadHook = func(string) { count++ }
	t.Cleanup(func() { spacesReadHook = nil })
	return &count
}

// walkTree returns a sorted listing of every path under dir, relative to dir.
func walkTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ---------------------------------------------------------------------------
// decode / validate
// ---------------------------------------------------------------------------

func TestSpacesDecodeRejectsMalformedFixtures(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"bad-yaml", "version: 1\nspaces: [\n", "parsing spaces file"},
		{"version-zero", "version: 0\nspaces: []\n", "missing or invalid schema version"},
		{"version-missing", "spaces: []\n", "missing or invalid schema version"},
		{"future-version", "version: 99\nspaces: []\n", "uses schema version 99"},
		{"unknown-field", "version: 1\nspaces: []\nextra: true\n", "field extra not found"},
		{
			"bad-name",
			"version: 1\nspaces:\n  - name: \"..\"\n    kind: docs\n    path: /tmp\n    added_at: 2026-01-01T00:00:00Z\n",
			"is reserved",
		},
		{
			"bad-kind",
			"version: 1\nspaces:\n  - name: x\n    kind: Docs\n    path: /tmp\n    added_at: 2026-01-01T00:00:00Z\n",
			"malformed",
		},
		{
			"traversal-path",
			"version: 1\nspaces:\n  - name: x\n    kind: docs\n    path: ../escape\n    added_at: 2026-01-01T00:00:00Z\n",
			"escapes the workspace root",
		},
		{
			"non-canonical-path",
			"version: 1\nspaces:\n  - name: x\n    kind: docs\n    path: /tmp/./a\n    added_at: 2026-01-01T00:00:00Z\n",
			"not canonical",
		},
		{
			"duplicate-scope-name",
			"version: 1\nspaces:\n  - name: x\n    kind: docs\n    path: a\n    added_at: 2026-01-01T00:00:00Z\n  - name: x\n    kind: docs\n    path: b\n    added_at: 2026-01-01T00:00:00Z\n",
			"duplicate space",
		},
		{
			"duplicate-scope-path",
			"version: 1\nspaces:\n  - name: x\n    kind: docs\n    path: a\n    added_at: 2026-01-01T00:00:00Z\n  - name: y\n    kind: docs\n    path: a\n    added_at: 2026-01-01T00:00:00Z\n",
			"is registered by both",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeSpacesFixture(t, root, tc.content)
			_, err := readSpaces(root)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestSpacesDecodeRejectsResolvedPathDuplicates pins that duplicate detection
// compares resolved paths, so a hand-edited absolute entry cannot shadow the
// workspace-relative entry for the same directory.
func TestSpacesDecodeRejectsResolvedPathDuplicates(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, "version: 1\nspaces:\n"+
		"  - name: x\n    kind: docs\n    path: notes\n    added_at: 2026-01-01T00:00:00Z\n"+
		"  - name: y\n    kind: docs\n    path: "+filepath.Join(root, "notes")+
		"\n    added_at: 2026-01-01T00:00:00Z\n")

	_, err := readSpaces(root)
	if err == nil || !strings.Contains(err.Error(), "is registered by both") {
		t.Fatalf("err = %v", err)
	}

	// The same paths in different scopes stay legal.
	writeSpacesFixture(t, root, "version: 1\nspaces:\n"+
		"  - name: x\n    kind: docs\n    path: notes\n    added_at: 2026-01-01T00:00:00Z\n"+
		"  - name: y\n    kind: docs\n    path: "+filepath.Join(root, "notes")+
		"\n    feature: acme\n    added_at: 2026-01-01T00:00:00Z\n")
	if _, err := readSpaces(root); err != nil {
		t.Fatalf("cross-scope duplicates must stay legal: %v", err)
	}
}

func TestSpacesReadRejectsSymlinkAndDirectory(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real.yaml")
		if err := os.WriteFile(target, []byte("version: 1\nspaces: []\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, spacesFileName)); err != nil {
			t.Fatal(err)
		}
		_, err := readSpaces(root)
		if err == nil || !strings.Contains(err.Error(), "refusing to follow symlinked") {
			t.Fatalf("expected symlink refusal, got %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, spacesFileName), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := readSpaces(root); err == nil {
			t.Fatal("expected error reading a directory named spaces.yaml")
		}
	})
}

func TestSpacesReadAbsentFileIsNoOp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")

	f, err := readSpaces(root)
	if err != nil || f != nil {
		t.Fatalf("readSpaces = (%v, %v), want (nil, nil)", f, err)
	}

	owners, err := SpaceDirOwners(root)
	if err != nil {
		t.Fatalf("SpaceDirOwners: %v", err)
	}
	if len(owners.TopLevel) != 0 || len(owners.Features) != 0 {
		t.Fatalf("expected zero owners, got %+v", owners)
	}
	if err := GuardFeatureName(root, "anything"); err != nil {
		t.Fatalf("GuardFeatureName: %v", err)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("readers created the spaces root")
	}
}

func TestSpacesSaveUsesRestrictivePermissions(t *testing.T) {
	root := t.TempDir()
	f := &SpacesFile{Version: spacesVersion, Spaces: []SpaceEntry{
		{Name: "learning", Kind: "learning", Path: "learning", AddedAt: time.Now().UTC()},
	}}
	if err := saveSpaces(root, f); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, spacesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}

	reread, err := readSpaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if reread == nil || len(reread.Spaces) != 1 {
		t.Fatalf("round trip failed: %+v", reread)
	}
}

func TestSpacesSaveHookInjectsFailure(t *testing.T) {
	root := t.TempDir()
	SpacesSaveHook = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { SpacesSaveHook = nil })

	err := saveSpaces(root, &SpacesFile{Version: spacesVersion})
	if err == nil {
		t.Fatal("expected injected save failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, spacesFileName)); statErr == nil {
		t.Fatal("spaces.yaml written despite injected failure")
	}
}

// ---------------------------------------------------------------------------
// path normalization
// ---------------------------------------------------------------------------

func TestSpacesNormalizePathForms(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)

	inside := filepath.Join(root, "learning")
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}
	stored, resolved, err := normalizeSpacePath(anchor, inside)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "learning" {
		t.Fatalf("stored = %q, want %q", stored, "learning")
	}
	if resolved != filepath.Join(root, "learning") {
		t.Fatalf("resolved = %q", resolved)
	}

	outside := t.TempDir()
	stored, resolved, err = normalizeSpacePath(anchor, outside)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(stored) || stored != resolved {
		t.Fatalf("expected absolute storage, got stored=%q resolved=%q", stored, resolved)
	}

	if _, _, err := normalizeSpacePath(anchor, root); err == nil ||
		!strings.Contains(err.Error(), "refusing to register the workspace root itself") {
		t.Fatalf("expected root refusal, got %v", err)
	}

	// The canonical form of the root must be refused too (macOS /var symlink).
	if _, _, err := normalizeSpacePath(anchor, canonicalize(root)); err == nil ||
		!strings.Contains(err.Error(), "refusing to register the workspace root itself") {
		t.Fatalf("expected canonical root refusal, got %v", err)
	}

	if _, _, err := normalizeSpacePath(anchor, ""); err == nil {
		t.Fatal("expected empty path rejection")
	}
}

func TestSpacesNormalizePathSymlinkedRootStaysRelative(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-root")
	if err := os.MkdirAll(filepath.Join(real, "tickets"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-root")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	// Anchor spelled through the symlink; input spelled through the real path.
	anchor := testAnchor(t, link, ModeExternal)
	stored, _, err := normalizeSpacePath(anchor, filepath.Join(real, "tickets"))
	if err != nil {
		t.Fatal(err)
	}
	if stored != "tickets" {
		t.Fatalf("stored = %q, want relative %q", stored, "tickets")
	}
}

func TestSpacesContainsPathIsInclusive(t *testing.T) {
	root := t.TempDir()
	feature := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(feature, "notes"), 0755); err != nil {
		t.Fatal(err)
	}

	if !containsPath(feature, feature) {
		t.Fatal("containsPath must be inclusive of the directory itself")
	}
	if !containsPath(feature, filepath.Join(feature, "notes")) {
		t.Fatal("containsPath must include descendants")
	}
	if containsPath(feature, root) {
		t.Fatal("containsPath must not include ancestors")
	}
	if containsPath(feature, filepath.Join(root, "acme-other")) {
		t.Fatal("containsPath must not match sibling prefixes")
	}
}

// ---------------------------------------------------------------------------
// ownership
// ---------------------------------------------------------------------------

func TestSpaceDirOwnersMapsAndExceptions(t *testing.T) {
	root := t.TempDir()
	// A real feature directory that a relative entry points into.
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "patching"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}

	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: learning
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
  - name: scratch
    kind: docs
    path: features/scratch
    added_at: 2026-01-01T00:00:00Z
  - name: outside
    kind: research
    path: /somewhere/else
    added_at: 2026-01-01T00:00:00Z
  - name: patching
    kind: patching
    path: acme/patching
    feature: acme
    added_at: 2026-01-01T00:00:00Z
`)

	owners, err := SpaceDirOwners(root)
	if err != nil {
		t.Fatal(err)
	}
	if owners.TopLevel["learning"] != "learning" {
		t.Fatalf("expected learning owned, got %+v", owners.TopLevel)
	}
	if owners.Features["scratch"] != "scratch" {
		t.Fatalf("expected features/scratch owned, got %+v", owners.Features)
	}
	if _, ok := owners.TopLevel["acme"]; ok {
		t.Fatal("a real feature directory must never be claimed as a space directory")
	}
	if len(owners.TopLevel) != 1 {
		t.Fatalf("an absolute entry outside the root must contribute nothing: %+v", owners.TopLevel)
	}
}

func TestSpaceDirOwnersClaimsAbsolutePathsInsideRoot(t *testing.T) {
	root := t.TempDir()
	// A real feature directory keeps the feature-hub exception observable.
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "patching"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"learning", filepath.Join("features", "scratch")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}

	writeSpacesFixture(t, root, "version: 1\nspaces:\n"+
		"  - name: learning\n    kind: learning\n    path: "+filepath.Join(root, "learning")+
		"\n    added_at: 2026-01-01T00:00:00Z\n"+
		"  - name: scratch\n    kind: docs\n    path: "+filepath.Join(root, "features", "scratch")+
		"\n    added_at: 2026-01-01T00:00:00Z\n"+
		"  - name: hub\n    kind: docs\n    path: "+featureDir+
		"\n    added_at: 2026-01-01T00:00:00Z\n")

	owners, err := SpaceDirOwners(root)
	if err != nil {
		t.Fatal(err)
	}
	if owners.TopLevel["learning"] != "learning" {
		t.Fatalf("an absolute in-root entry must own its top-level name: %+v", owners.TopLevel)
	}
	if owners.Features["scratch"] != "scratch" {
		t.Fatalf("an absolute in-root features/ entry must own its name: %+v", owners.Features)
	}
	if space, ok := owners.TopLevelOwner("acme"); ok {
		t.Fatalf("the feature-hub exception must survive absolute spellings: %q", space)
	}

	for _, feature := range []string{"learning", "scratch"} {
		if err := GuardFeatureName(root, feature); err == nil ||
			!strings.Contains(err.Error(), "top-level directory of registered space") {
			t.Fatalf("guard %q = %v", feature, err)
		}
	}
	if err := GuardFeatureName(root, "acme"); err != nil {
		t.Fatalf("a real feature must stay usable: %v", err)
	}
}

func TestSpaceOwnersIdentityMatchesSymlinkedSpelling(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	writeSpacesFixture(t, root, "version: 1\nspaces:\n  - name: notes\n    kind: docs\n    path: link\n"+
		"    added_at: 2026-01-01T00:00:00Z\n")

	owners, err := SpaceDirOwners(root)
	if err != nil {
		t.Fatal(err)
	}
	if space, ok := owners.TopLevelOwner("real"); !ok || space != "notes" {
		t.Fatalf("the identical directory must be owned through either spelling: %q %v", space, ok)
	}
	if err := GuardFeatureName(root, "real"); err == nil {
		t.Fatal("a feature name naming the same directory must be refused")
	}
}

func TestPathContainsWalksAncestorsByIdentity(t *testing.T) {
	root := t.TempDir()
	feature := filepath.Join(root, "acme")
	nested := filepath.Join(feature, "notes", "deep")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	if !pathContains(feature, feature) || !pathContains(feature, nested) {
		t.Fatal("pathContains must be inclusive and cover descendants")
	}
	if pathContains(feature, root) || pathContains(feature, filepath.Join(root, "acme-other")) {
		t.Fatal("pathContains must not match ancestors or sibling prefixes")
	}
	if !pathContains(feature, filepath.Join(feature, "missing", "child")) {
		t.Fatal("a not-yet-created descendant must still be contained")
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(feature, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !pathContains(feature, filepath.Join(link, "notes")) {
		t.Fatal("a symlinked spelling of the same ancestor must be contained")
	}
}

func TestNewSpaceSelectorScopes(t *testing.T) {
	if _, err := NewSpaceSelector("notes", "acme", true); err == nil ||
		!strings.Contains(err.Error(), "--feature and --workspace are mutually exclusive") {
		t.Fatalf("err = %v", err)
	}

	bare, err := NewSpaceSelector("notes", "", false)
	if err != nil || bare.Scope != SpaceScopeSelectorAny || bare.Feature != "" {
		t.Fatalf("bare selector = %+v (%v)", bare, err)
	}
	workspace, err := NewSpaceSelector("notes", "", true)
	if err != nil || workspace.Scope != SpaceScopeSelectorWorkspace {
		t.Fatalf("workspace selector = %+v (%v)", workspace, err)
	}
	feature, err := NewSpaceSelector("notes", "acme", false)
	if err != nil || feature.Scope != SpaceScopeSelectorFeature || feature.Feature != "acme" {
		t.Fatalf("feature selector = %+v (%v)", feature, err)
	}

	if err := (SpaceSelector{Name: "notes", Scope: SpaceScopeSelectorFeature}).validate(); err == nil {
		t.Fatal("a feature scope without a feature name must be rejected")
	}
	if err := (SpaceSelector{Name: "", Scope: SpaceScopeSelectorAny}).validate(); err == nil {
		t.Fatal("an empty name must be rejected")
	}
}

func TestSpaceDirOwnersWrapsUntrustedMetadata(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, "version: 99\nspaces: []\n")

	_, err := SpaceDirOwners(root)
	if err == nil || !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
		t.Fatalf("expected canonical wrap, got %v", err)
	}
	if err := GuardFeatureName(root, "anything"); err == nil ||
		!strings.Contains(err.Error(), "cannot verify registered spaces in ") {
		t.Fatalf("guard must propagate the canonical message, got %v", err)
	}
}

func TestGuardFeatureNameConflictMessage(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	err := GuardFeatureName(root, "learning")
	if err == nil {
		t.Fatal("expected conflict")
	}
	var conflict *ErrSpaceNameConflict
	if !asError(err, &conflict) {
		t.Fatalf("expected *ErrSpaceNameConflict, got %T", err)
	}
	want := `cannot use feature name "learning": it is the top-level directory of registered space "notes" in ` +
		filepath.Join(root, spacesFileName) +
		`; choose another feature name or run 'tws space remove notes'`
	if err.Error() != want {
		t.Fatalf("message =\n%s\nwant\n%s", err.Error(), want)
	}

	if err := GuardFeatureName(root, "other"); err != nil {
		t.Fatalf("unrelated name must pass: %v", err)
	}
}

// TestGuardFeatureNameRejectsUnsafeNames pins that the guard refuses a name no
// caller may join under a root. Every guarded command computes
// <root>/<feature> right after the guard, so a separator, a traversal segment,
// or a reserved directory must be refused here rather than at the resolver,
// which some callers (FeaturePath) never reach.
func TestGuardFeatureNameRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, "version: 1\nspaces: []\n")

	cases := []struct {
		feature string
		want    string
	}{
		{"../outside", `feature name "../outside" contains path separator`},
		{"..", `feature name ".." contains path traversal`},
		{`..\outside`, `feature name "..\\outside" contains path separator`},
		{"a/b", `feature name "a/b" contains path separator`},
		{"/abs", `feature name "/abs" contains path separator`},
		{`a\b`, `feature name "a\\b" contains path separator`},
		{"nested/../escape", `feature name "nested/../escape" contains path separator`},
		{".", `feature name "." is reserved`},
		{".hidden", `feature name ".hidden" conflicts with reserved directory`},
		{"features", `feature name "features" conflicts with reserved directory`},
		{"state", `feature name "state" conflicts with reserved directory`},
		{"spaces.yaml", `feature name "spaces.yaml" conflicts with reserved directory`},
	}
	for _, tc := range cases {
		err := GuardFeatureName(root, tc.feature)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("GuardFeatureName(%q) = %v, want %q", tc.feature, err, tc.want)
		}
		// The canonical message is the resolver's, so a caller sees one
		// refusal wording no matter which layer refused first.
		if resolved := validateFeatureName(tc.feature); resolved == nil || resolved.Error() != err.Error() {
			t.Fatalf("guard message diverged from validateFeatureName(%q): %v vs %v", tc.feature, err, resolved)
		}
	}

	// A legal name is unaffected.
	if err := GuardFeatureName(root, "my.feat"); err != nil {
		t.Fatalf("a valid feature name must still pass: %v", err)
	}
}

// TestGuardFeatureNameRefusesUnsafeNameBeforeAnyRead pins the ordering: the
// name refusal precedes the registry read, so it holds for a workspace with no
// spaces.yaml, an unreadable one, and an empty root alike.
func TestGuardFeatureNameRefusesUnsafeNameBeforeAnyRead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")

	count := countSpacesReads(t)
	if err := GuardFeatureName(root, "../outside"); err == nil ||
		!strings.Contains(err.Error(), "contains path separator") {
		t.Fatalf("guard err = %v", err)
	}
	if *count != 0 {
		t.Fatalf("the name refusal must precede the registry read, got %d read(s)", *count)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("a refused guard created the workspace root")
	}

	// Untrusted metadata cannot make an unsafe name pass.
	writeSpacesFixture(t, root, "version: 99\nspaces: []\n")
	if err := GuardFeatureName(root, "../outside"); err == nil ||
		!strings.Contains(err.Error(), "contains path separator") {
		t.Fatalf("guard err with untrusted metadata = %v", err)
	}

	// An empty root still validates the name: a caller with no root to read
	// still joins the name somewhere.
	if err := GuardFeatureName("", "../outside"); err == nil ||
		!strings.Contains(err.Error(), "contains path separator") {
		t.Fatalf("guard err with empty root = %v", err)
	}
	if err := GuardFeatureName("", ".."); err == nil ||
		!strings.Contains(err.Error(), "contains path traversal") {
		t.Fatalf("guard err with empty root = %v", err)
	}
	// An empty feature remains the documented no-op.
	if err := GuardFeatureName(root, ""); err != nil {
		t.Fatalf("an empty feature must stay a no-op: %v", err)
	}
}

func asError(err error, target **ErrSpaceNameConflict) bool {
	c, ok := err.(*ErrSpaceNameConflict)
	if ok {
		*target = c
	}
	return ok
}

// ---------------------------------------------------------------------------
// listing integration, recursion fence, read counting
// ---------------------------------------------------------------------------

func TestListFeaturesResolvedExcludesRegisteredSpaces(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "learning"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: learning
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "alpha" {
		t.Fatalf("expected [alpha], got %v", features)
	}
}

func TestListFeaturesResolvedCheckoutBranchesExcludeSpaces(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "features", "alpha"),
		filepath.Join(root, "features", "scratch"),
		filepath.Join(root, "legacy-space"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: scratch
    kind: docs
    path: features/scratch
    added_at: 2026-01-01T00:00:00Z
  - name: legacy
    kind: docs
    path: legacy-space
    added_at: 2026-01-01T00:00:00Z
`)

	ws := Workspace{MetadataRoot: root, Mode: ModeCheckout}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "alpha" {
		t.Fatalf("expected [alpha], got %v", features)
	}

	names := ws.LegacyFeatureNames()
	for _, n := range names {
		if n == "legacy-space" {
			t.Fatal("LegacyFeatureNames must exclude registered spaces")
		}
	}
}

func TestListFeaturesResolvedPropagatesUntrustedMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alpha"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, "version: 1\nspaces: [\n")

	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	features, err := ws.ListFeaturesResolved()
	if err == nil {
		t.Fatal("expected strict failure")
	}
	if features != nil {
		t.Fatalf("no partial listing may be returned, got %v", features)
	}
	if !strings.Contains(err.Error(), "cannot verify registered spaces in ") {
		t.Fatalf("unexpected message: %v", err)
	}

	// Completion best-effort: no candidates, no panic.
	if names := ws.LegacyFeatureNames(); names != nil {
		t.Fatalf("expected no legacy candidates, got %v", names)
	}
}

func TestListFeaturesResolvedReadsSpacesFileOnce(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: learning
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	count := countSpacesReads(t)
	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 5 {
		t.Fatalf("expected 5 features, got %v", features)
	}
	if *count != 1 {
		t.Fatalf("expected exactly one spaces.yaml read, got %d", *count)
	}
}

func TestGuardFeatureNameReadsSpacesFileOnce(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, "version: 1\nspaces: []\n")

	count := countSpacesReads(t)
	if err := GuardFeatureName(root, "alpha"); err != nil {
		t.Fatal(err)
	}
	if *count != 1 {
		t.Fatalf("expected exactly one read, got %d", *count)
	}
}

// TestSpacesRecursionFenceTerminates pins that a spaces.yaml whose relative
// entry names an existing feature-signal directory does not recurse.
func TestSpacesRecursionFenceTerminates(t *testing.T) {
	root := t.TempDir()
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "worktrees"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: hub
    kind: patching
    path: acme
    added_at: 2026-01-01T00:00:00Z
`)

	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "acme" {
		t.Fatalf("real feature directory must stay listed, got %v", features)
	}
}

// TestSpacesSourceHasNoResolverCalls is the static recursion fence assertion.
func TestSpacesSourceHasNoResolverCalls(t *testing.T) {
	data, err := os.ReadFile("spaces.go")
	if err != nil {
		t.Fatal(err)
	}

	forbidden := []string{
		"ResolveFeaturePath",
		"ResolveFeaturePathOrLegacy",
		"ListFeaturesResolved",
		"LegacyFeatureNames",
		"ListFeaturesE",
		"ListFeatures(",
		"RequireFeaturePath",
		"RequireWorkspace",
		"TwsRoot",
	}

	inAnchorResolver := false
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(line, "func ResolveSpacesAnchor(") {
			inAnchorResolver = true
			continue
		}
		if inAnchorResolver {
			if line == "}" {
				inAnchorResolver = false
			}
			continue
		}
		for _, name := range forbidden {
			if strings.Contains(line, name) {
				t.Errorf("spaces.go:%d calls forbidden resolver %q: %s", i+1, name, trimmed)
			}
		}
	}
}

func TestResolveFeaturePathStaysGuardFree(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	got, err := ws.ResolveFeaturePath("learning")
	if err != nil {
		t.Fatalf("generic resolver must stay guard-free: %v", err)
	}
	if got != filepath.Join(root, "learning") {
		t.Fatalf("path = %q", got)
	}
}

func TestDetectFeatureFromCwdEStaysExclusionFree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "learning", "notes"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	feature, _ := ws.DetectFeatureFromCwdE(filepath.Join(root, "learning", "notes"))
	if feature != "learning" {
		t.Fatalf("DetectFeatureFromCwdE must remain exclusion-free, got %q", feature)
	}
}

// ---------------------------------------------------------------------------
// add / list / show / remove
// ---------------------------------------------------------------------------

func TestSpaceAddStoresRelativeAndAbsoluteForms(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)

	inside := filepath.Join(root, "learning")
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}
	entry, created, err := SpaceAdd(anchor, SpaceAddRequest{Name: "learning", Path: inside, Kind: "learning"})
	if err != nil || !created {
		t.Fatalf("add inside: created=%v err=%v", created, err)
	}
	if entry.Path != "learning" {
		t.Fatalf("stored path = %q", entry.Path)
	}
	if entry.UpdatedAt != nil {
		t.Fatal("add must never set updated_at")
	}

	outside := t.TempDir()
	entry, created, err = SpaceAdd(anchor, SpaceAddRequest{Name: "out", Path: outside, Kind: "research"})
	if err != nil || !created {
		t.Fatalf("add outside: created=%v err=%v", created, err)
	}
	if !filepath.IsAbs(entry.Path) {
		t.Fatalf("expected absolute storage, got %q", entry.Path)
	}
}

func TestSpaceAddIdempotentAndConflicting(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	target := filepath.Join(root, "learning")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}

	req := SpaceAddRequest{Name: "learning", Path: target, Kind: "learning"}
	if _, created, err := SpaceAdd(anchor, req); err != nil || !created {
		t.Fatalf("first add: %v", err)
	}
	if _, created, err := SpaceAdd(anchor, req); err != nil || created {
		t.Fatalf("repeat add must be an idempotent no-op: created=%v err=%v", created, err)
	}

	before, err := os.ReadFile(filepath.Join(root, spacesFileName))
	if err != nil {
		t.Fatal(err)
	}
	conflicting := req
	conflicting.Kind = "docs"
	if _, _, err := SpaceAdd(anchor, conflicting); err == nil ||
		!strings.Contains(err.Error(), "already exists in this scope") {
		t.Fatalf("expected scope conflict, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, spacesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("file must be byte-identical after a rejected add")
	}

	// Same resolved path, different name, same scope.
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "other", Path: target, Kind: "docs"}); err == nil ||
		!strings.Contains(err.Error(), "is already registered as") {
		t.Fatalf("expected duplicate-path rejection, got %v", err)
	}
}

func TestSpaceAddValidationErrors(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	target := filepath.Join(root, "learning")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		req  SpaceAddRequest
		want string
	}{
		{"empty-kind", SpaceAddRequest{Name: "a", Path: target, Kind: ""}, "kind cannot be empty"},
		{"upper-kind", SpaceAddRequest{Name: "a", Path: target, Kind: "Learning"}, "malformed"},
		{"long-kind", SpaceAddRequest{Name: "a", Path: target, Kind: strings.Repeat("k", 33)}, "too long"},
		{"dot-name", SpaceAddRequest{Name: ".", Path: target, Kind: "docs"}, "reserved"},
		{"dotdot-name", SpaceAddRequest{Name: "..", Path: target, Kind: "docs"}, "reserved"},
		{"newline-description", SpaceAddRequest{Name: "a", Path: target, Kind: "docs", Description: "a\nb"}, "control characters"},
		{"tab-description", SpaceAddRequest{Name: "a", Path: target, Kind: "docs", Description: "a\tb"}, "control characters"},
		{"long-description", SpaceAddRequest{Name: "a", Path: target, Kind: "docs", Description: strings.Repeat("d", 201)}, "too long"},
		{"missing-target", SpaceAddRequest{Name: "a", Path: filepath.Join(root, "nope"), Kind: "docs"}, "does not exist"},
		{"file-target", SpaceAddRequest{Name: "a", Path: file, Kind: "docs"}, "is not a directory"},
		{"root-target", SpaceAddRequest{Name: "a", Path: root, Kind: "docs"}, "refusing to register the workspace root itself"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := SpaceAdd(anchor, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(root, spacesFileName)); statErr == nil {
				t.Fatal("rejected add must not create spaces.yaml")
			}
		})
	}

	// An unconventional but conforming kind must succeed.
	if _, created, err := SpaceAdd(anchor, SpaceAddRequest{Name: "ledger", Path: target, Kind: "ledger"}); err != nil || !created {
		t.Fatalf("conforming kind must be accepted: %v", err)
	}
}

func TestSpaceAddRefusesFeatureDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "patching"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "x", Path: featureDir, Kind: "docs"}); err == nil ||
		!strings.Contains(err.Error(), `it is the feature directory "acme" in this workspace`) {
		t.Fatalf("expected feature-dir refusal, got %v", err)
	}

	// The roadmap hub layout stays allowed.
	entry, created, err := SpaceAdd(anchor, SpaceAddRequest{
		Name: "patching", Path: filepath.Join(featureDir, "patching"), Kind: "patching", Feature: "acme",
	})
	if err != nil || !created {
		t.Fatalf("hub layout must be allowed: %v", err)
	}
	if entry.Path != filepath.Join("acme", "patching") {
		t.Fatalf("stored path = %q", entry.Path)
	}

	// The feature itself must remain listed.
	ws := Workspace{MetadataRoot: root, Mode: ModeExternal}
	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "acme" {
		t.Fatalf("expected [acme], got %v", features)
	}
}

func TestSpaceAddRequiresExistingFeature(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	target := filepath.Join(root, "notes")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}

	_, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "n", Path: target, Kind: "docs", Feature: "nosuch"})
	if err == nil || !strings.Contains(err.Error(), `feature "nosuch" not found in this workspace`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSpaceListScopeAndFilters(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)

	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	otherFeature := filepath.Join(root, "beta")
	if err := os.MkdirAll(otherFeature, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherFeature, "FEATURE.md"), []byte("# beta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"learning", "acme/patching", "beta/patching"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}

	mustAdd := func(req SpaceAddRequest) {
		t.Helper()
		if _, _, err := SpaceAdd(anchor, req); err != nil {
			t.Fatal(err)
		}
	}
	mustAdd(SpaceAddRequest{Name: "learning", Path: filepath.Join(root, "learning"), Kind: "learning"})
	mustAdd(SpaceAddRequest{Name: "patching", Path: filepath.Join(featureDir, "patching"), Kind: "patching", Feature: "acme"})
	mustAdd(SpaceAddRequest{Name: "patching", Path: filepath.Join(otherFeature, "patching"), Kind: "patching", Feature: "beta"})

	all, err := SpaceList(anchor, SpaceListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Views) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all.Views))
	}
	if all.Total != 3 || all.Scope() != "all" {
		t.Fatalf("--all metadata = %d/%q", all.Total, all.Scope())
	}

	scoped, err := SpaceList(anchor, SpaceListOptions{Feature: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Views) != 2 {
		t.Fatalf("expected workspace-wide + acme, got %d", len(scoped.Views))
	}
	if scoped.Total != 3 || scoped.Scope() != "feature acme" {
		t.Fatalf("--feature metadata = %d/%q", scoped.Total, scoped.Scope())
	}

	kinded, err := SpaceList(anchor, SpaceListOptions{All: true, Kind: "patching"})
	if err != nil {
		t.Fatal(err)
	}
	if len(kinded.Views) != 2 {
		t.Fatalf("kind filter: got %d", len(kinded.Views))
	}
	if kinded.Total != 3 {
		t.Fatalf("a kind filter must still report the registry total: %d", kinded.Total)
	}

	// A filter that matches nothing still reports the registry total.
	empty, err := SpaceList(anchor, SpaceListOptions{All: true, Kind: "nosuchkind"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Views) != 0 || empty.Total != 3 {
		t.Fatalf("empty filter result = %d views / %d total", len(empty.Views), empty.Total)
	}

	// cwd-based detection under the feature directory.
	detected, err := SpaceList(anchor, SpaceListOptions{Cwd: filepath.Join(featureDir, "patching")})
	if err != nil {
		t.Fatal(err)
	}
	if len(detected.Views) != 2 {
		t.Fatalf("cwd detection: got %d entries", len(detected.Views))
	}
	if detected.Scope() != "feature acme" {
		t.Fatalf("auto-detected scope = %q", detected.Scope())
	}
	for _, v := range detected.Views {
		if v.Feature == "beta" {
			t.Fatal("cwd detection leaked another feature's entries")
		}
	}

	// cwd inside a registered top-level space must not auto-scope.
	inSpace, err := SpaceList(anchor, SpaceListOptions{Cwd: filepath.Join(root, "learning")})
	if err != nil {
		t.Fatal(err)
	}
	if len(inSpace.Views) != 3 {
		t.Fatalf("a space-owned first segment must never be treated as a feature, got %d", len(inSpace.Views))
	}
	if inSpace.Scope() != "all" {
		t.Fatalf("undetected scope = %q, want all", inSpace.Scope())
	}

	if _, err := SpaceList(anchor, SpaceListOptions{Feature: "nosuch"}); err == nil {
		t.Fatal("unknown --feature must be an error")
	}
	if _, err := SpaceList(anchor, SpaceListOptions{All: true, Feature: "acme"}); err == nil {
		t.Fatal("--all and --feature are mutually exclusive")
	}
}

func TestSpaceListReportsComputedStatus(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	target := filepath.Join(root, "learning")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "learning", Path: target, Kind: "learning"}); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(root, spacesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}

	result, err := SpaceList(anchor, SpaceListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	views := result.Views
	if len(views) != 1 || views[0].Status != SpaceStatusMissing {
		t.Fatalf("expected missing status, got %+v", views)
	}
	after, err := os.ReadFile(filepath.Join(root, spacesFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a missing target must never mutate the file")
	}
}

func TestSpaceViewJSONOmitsUnsetOptionalKeys(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	target := filepath.Join(root, "learning")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "learning", Path: target, Kind: "learning"}); err != nil {
		t.Fatal(err)
	}

	result, err := SpaceList(anchor, SpaceListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.Views[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"description", "feature", "updated_at"} {
		if _, ok := decoded[key]; ok {
			t.Fatalf("key %q must be omitted when unset: %s", key, data)
		}
	}
	for _, key := range []string{"resolved_path", "scope", "scope_status", "status", "added_at"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("key %q must always be present: %s", key, data)
		}
	}
	if strings.Contains(string(data), "0001-01-01T00:00:00Z") {
		t.Fatalf("zero timestamp leaked: %s", data)
	}
}

func TestSpaceShowSelectorRules(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)

	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"learning", "acme/learning"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "learning", Path: filepath.Join(root, "learning"), Kind: "learning"}); err != nil {
		t.Fatal(err)
	}

	view, err := SpaceShow(anchor, SpaceSelector{Name: "learning"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Scope != SpaceScopeWorkspace {
		t.Fatalf("scope = %q", view.Scope)
	}

	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{
		Name: "learning", Path: filepath.Join(featureDir, "learning"), Kind: "learning", Feature: "acme",
	}); err != nil {
		t.Fatal(err)
	}

	_, err = SpaceShow(anchor, SpaceSelector{Name: "learning"})
	if err == nil || !strings.Contains(err.Error(), "is ambiguous") {
		t.Fatalf("expected ambiguity, got %v", err)
	}
	if !strings.Contains(err.Error(), "disambiguate with --feature <name> or --workspace") {
		t.Fatalf("ambiguity must name both selectors: %v", err)
	}
	if _, err := SpaceShow(anchor, SpaceSelector{Name: "learning", Scope: SpaceScopeSelectorFeature, Feature: "acme"}); err != nil {
		t.Fatalf("--feature must disambiguate: %v", err)
	}
	workspaceView, err := SpaceShow(anchor, SpaceSelector{Name: "learning", Scope: SpaceScopeSelectorWorkspace})
	if err != nil {
		t.Fatalf("--workspace must disambiguate: %v", err)
	}
	if workspaceView.Scope != SpaceScopeWorkspace {
		t.Fatalf("--workspace selected %+v", workspaceView)
	}
	if _, err := SpaceShow(anchor, SpaceSelector{Name: "nosuch"}); err == nil || !strings.Contains(err.Error(), "no space named") {
		t.Fatalf("expected not-found, got %v", err)
	}
	if _, err := SpaceShow(anchor, SpaceSelector{Name: "nosuch", Scope: SpaceScopeSelectorWorkspace}); err == nil ||
		!strings.Contains(err.Error(), `no space named "nosuch" in the workspace scope`) {
		t.Fatalf("workspace-scoped not-found = %v", err)
	}
}

func TestSpaceScopeStatusFeatureMissing(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "notes", Path: outside, Kind: "docs", Feature: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(featureDir); err != nil {
		t.Fatal(err)
	}

	result, err := SpaceList(anchor, SpaceListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	views := result.Views
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if views[0].ScopeStatus != SpaceScopeStatusFeatureMissing {
		t.Fatalf("scope_status = %q", views[0].ScopeStatus)
	}
	if views[0].Status != SpaceStatusOK {
		t.Fatalf("status = %q, want ok", views[0].Status)
	}
}

func TestSpaceRemoveAbsentRegistryCreatesNothing(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	anchor := testAnchor(t, root, ModeExternal)

	before := walkTree(t, parent)
	err := SpaceRemove(anchor, SpaceSelector{Name: "learning"})
	if err == nil || err.Error() != `no space named "learning"` {
		t.Fatalf(`err = %v, want exactly 'no space named "learning"'`, err)
	}
	after := walkTree(t, parent)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("remove created files:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestSpaceRemoveKeepsTargetDirectory(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	target := filepath.Join(root, "learning")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "learning", Path: target, Kind: "learning"}); err != nil {
		t.Fatal(err)
	}

	if err := SpaceRemove(anchor, SpaceSelector{Name: "learning"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("remove must never delete the target directory")
	}
	result, err := SpaceList(anchor, SpaceListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Views) != 0 || result.Total != 0 {
		t.Fatalf("expected empty registry, got %+v", result)
	}
}

// ---------------------------------------------------------------------------
// transactions
// ---------------------------------------------------------------------------

func TestBeginSpacesTransactionsAbsentRegistryAreNoOps(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	before := walkTree(t, parent)

	deleteTx, err := BeginSpacesFeatureDelete(root, "acme", filepath.Join(root, "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteTx.Release(); err != nil {
		t.Fatal(err)
	}

	renameTx, err := BeginSpacesFeatureRename(root, "acme", "acme2", filepath.Join(root, "acme"), filepath.Join(root, "acme2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := renameTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := renameTx.Release(); err != nil {
		t.Fatal(err)
	}

	after := walkTree(t, parent)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("no-op transactions created files:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestBeginSpacesFeatureDeleteRefusesNestedTargets(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "learning"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(featureDir, "patching"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "learning", Path: filepath.Join(featureDir, "learning"), Kind: "learning"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{
		Name: "patching", Path: filepath.Join(featureDir, "patching"), Kind: "patching", Feature: "acme",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := BeginSpacesFeatureDelete(root, "acme", featureDir)
	if err == nil {
		t.Fatal("expected refusal")
	}
	msg := err.Error()
	for _, want := range []string{
		`cannot delete feature "acme"`, "2 registered spaces live inside",
		"learning (workspace)", "patching (feature acme)", "tws space remove",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}

	// After removing both entries the delete transaction opens.
	if err := SpaceRemove(anchor, SpaceSelector{Name: "learning"}); err != nil {
		t.Fatal(err)
	}
	if err := SpaceRemove(anchor, SpaceSelector{
		Name: "patching", Scope: SpaceScopeSelectorFeature, Feature: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := BeginSpacesFeatureDelete(root, "acme", featureDir)
	if err != nil {
		t.Fatalf("expected transaction to open: %v", err)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginSpacesFeatureDeleteGuardOrdering(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "acme", Path: filepath.Join(root, "acme"), Kind: "docs"}); err != nil {
		t.Fatal(err)
	}

	_, err := BeginSpacesFeatureDelete(root, "acme", filepath.Join(root, "acme"))
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "it is the top-level directory of registered space") {
		t.Fatalf("top-level conflict must win over nested containment: %v", err)
	}
}

func TestBeginSpacesFeatureRenameRewritesAndPins(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "patching"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(featureDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{
		Name: "patching", Path: filepath.Join(featureDir, "patching"), Kind: "patching", Feature: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: "docs", Path: filepath.Join(featureDir, "docs"), Kind: "docs"}); err != nil {
		t.Fatal(err)
	}

	oldPath := featureDir
	newPath := filepath.Join(root, "acme2")
	tx, err := BeginSpacesFeatureRename(root, "acme", "acme2", oldPath, newPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}

	f, err := readSpaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Spaces {
		if !strings.HasPrefix(e.Path, "acme2"+string(filepath.Separator)) {
			t.Fatalf("path not rewritten: %+v", e)
		}
		if e.UpdatedAt == nil {
			t.Fatalf("updated_at not set: %+v", e)
		}
		if e.Name == "patching" && e.Feature != "acme2" {
			t.Fatalf("feature scope not rewritten: %+v", e)
		}
	}
}

func TestBeginSpacesFeatureRenameRefusesAbsoluteEntries(t *testing.T) {
	root := t.TempDir()
	featureDir := filepath.Join(root, "acme")
	if err := os.MkdirAll(filepath.Join(featureDir, "tickets"), 0755); err != nil {
		t.Fatal(err)
	}
	// A real feature directory, so the entry is pinned inside a feature rather
	// than owning the feature's own name.
	if err := os.WriteFile(filepath.Join(featureDir, "FEATURE.md"), []byte("# acme\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, "version: 1\nspaces:\n  - name: tickets\n    kind: tickets\n    path: "+
		filepath.Join(featureDir, "tickets")+"\n    added_at: 2026-01-01T00:00:00Z\n")

	_, err := BeginSpacesFeatureRename(root, "acme", "acme2", featureDir, filepath.Join(root, "acme2"))
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{
		`cannot rename feature "acme"`,
		"1 registered space is pinned inside",
		"tws space remove tickets --workspace",
		"workspace-relative",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q missing %q", err, want)
		}
	}
}

func TestBeginSpacesFeatureRenameNoMatchingEntryWritesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "learning"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: learning
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)
	path := filepath.Join(root, spacesFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := BeginSpacesFeatureRename(root, "acme", "acme2", filepath.Join(root, "acme"), filepath.Join(root, "acme2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("file must be byte-identical after a no-op rename")
	}
	if !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("mtime must be unchanged after a no-op rename")
	}
}

func TestBeginSpacesFeatureRenameGuardsBothNames(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "learning"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	if _, err := BeginSpacesFeatureRename(root, "learning", "foo",
		filepath.Join(root, "learning"), filepath.Join(root, "foo")); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("source collision: %v", err)
	}
	if _, err := BeginSpacesFeatureRename(root, "foo", "learning",
		filepath.Join(root, "foo"), filepath.Join(root, "learning")); err == nil ||
		!strings.Contains(err.Error(), "top-level directory of registered space") {
		t.Fatalf("target collision: %v", err)
	}
}

func TestBeginSpacesTransactionsPropagateUntrustedMetadata(t *testing.T) {
	root := t.TempDir()
	writeSpacesFixture(t, root, "version: 42\nspaces: []\n")

	if _, err := BeginSpacesFeatureDelete(root, "acme", filepath.Join(root, "acme")); err == nil ||
		!strings.Contains(err.Error(), "cannot verify registered spaces in ") {
		t.Fatalf("delete tx: %v", err)
	}
	if _, err := BeginSpacesFeatureRename(root, "a", "b", filepath.Join(root, "a"), filepath.Join(root, "b")); err == nil ||
		!strings.Contains(err.Error(), "cannot verify registered spaces in ") {
		t.Fatalf("rename tx: %v", err)
	}
}

func TestSpaceAddConcurrentInvocationsBothPersist(t *testing.T) {
	root := t.TempDir()
	anchor := testAnchor(t, root, ModeExternal)
	for _, name := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}

	errs := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func(n string) {
			_, _, err := SpaceAdd(anchor, SpaceAddRequest{Name: n, Path: filepath.Join(root, n), Kind: "docs"})
			errs <- err
		}(name)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}

	result, err := SpaceList(anchor, SpaceListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Views) != 2 {
		t.Fatalf("lost update: %+v", result.Views)
	}
}

// TestInferExternalRepoRootIgnoresSiblingSpace pins the §7.5 decision that
// inferExternalRepoRoot needs no exclusion: it promotes a directory only when
// LoadStack succeeds and an active worktree path exists.
func TestInferExternalRepoRootIgnoresSiblingSpace(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "learning", "notes"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSpacesFixture(t, root, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	if _, err := inferExternalRepoRoot(root, Config{}); err == nil {
		t.Fatal("a sibling space must never be promoted to a source repository")
	}
}
