package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFeaturePath_NewLayout(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create new layout feature.
	if err := os.MkdirAll(filepath.Join(dir, "features", "myfeature"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err := ws.ResolveFeaturePath("myfeature")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "features", "myfeature") {
		t.Errorf("got %s", got)
	}
}

func TestResolveFeaturePath_LegacyLayout(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create legacy feature.
	if err := os.MkdirAll(filepath.Join(dir, "myfeature"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err := ws.ResolveFeaturePath("myfeature")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "myfeature") {
		t.Errorf("got %s", got)
	}
}

func TestResolveFeaturePath_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create both.
	if err := os.MkdirAll(filepath.Join(dir, "features", "myfeature"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "myfeature"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := ws.ResolveFeaturePath("myfeature")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	var ambErr *ErrAmbiguousFeature
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguous error, got: %v", err)
	}
	_ = ambErr
}

func TestResolveFeaturePath_ReservedDirNotLegacy(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// "features" dir exists at root but is reserved -- should not be treated as legacy.
	if err := os.MkdirAll(filepath.Join(dir, "features"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err := ws.ResolveFeaturePath("newfeature")
	if err != nil {
		t.Fatal(err)
	}
	// Should default to new layout.
	if !strings.Contains(got, "features/newfeature") {
		t.Errorf("expected new layout path, got %s", got)
	}
}

func TestResolveFeaturePath_ExternalMode(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeExternal}

	got, err := ws.ResolveFeaturePath("feat")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "feat") {
		t.Errorf("got %s", got)
	}
}

func TestResolveFeaturePath_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	for _, name := range []string{"..", "../etc", "a/b", "state", ".hidden"} {
		_, err := ws.ResolveFeaturePath(name)
		if err == nil {
			t.Errorf("expected error for %q", name)
		}
	}
}

func TestResolveFeaturePath_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	// Create a symlink that looks like a feature.
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}

	got, err := ws.ResolveFeaturePath("linked")
	if err != nil {
		t.Fatal(err) // No error; symlink just not recognized as existing dir.
	}
	// Should default to new layout since symlink is not recognized.
	if !strings.Contains(got, "features/linked") {
		t.Errorf("expected new layout default, got %s", got)
	}
}

func TestListFeaturesResolved_Sorted(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	for _, path := range []string{
		filepath.Join(dir, "features", "beta"),
		filepath.Join(dir, "features", "alpha"),
		filepath.Join(dir, "legacy-feat"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 3 {
		t.Fatalf("expected 3 features, got %d: %v", len(features), features)
	}
	if features[0] != "alpha" || features[1] != "beta" || features[2] != "legacy-feat" {
		t.Errorf("not sorted: %v", features)
	}
}

func TestListFeaturesResolved_ExcludesReserved(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	if err := os.MkdirAll(filepath.Join(dir, "features"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "state"),
		filepath.Join(dir, "templates"),
		filepath.Join(dir, "real-feature"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 || features[0] != "real-feature" {
		t.Errorf("expected [real-feature], got %v", features)
	}
}

func TestValidateFeatureName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"valid-feature", false},
		{"my.feat", false},
		{"", true},
		{"..", true},
		{"../evil", true},
		{"a/b", true},
		{"features", true},
		{"state", true},
		{".hidden", true},
	}
	for _, tc := range cases {
		err := validateFeatureName(tc.name)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateFeatureName(%q): err=%v, wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

func TestDetectFeatureFromCwdE_NewLayout(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	cwd := filepath.Join(dir, "features", "myfeat", "worktrees", "branch1")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}

	feat, _ := ws.DetectFeatureFromCwdE(cwd)
	if feat != "myfeat" {
		t.Errorf("expected myfeat, got %q", feat)
	}
}

func TestDetectFeatureFromCwdE_LegacyLayout(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	cwd := filepath.Join(dir, "oldfeat", "worktrees", "br")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}

	feat, branch := ws.DetectFeatureFromCwdE(cwd)
	if feat != "oldfeat" {
		t.Errorf("expected oldfeat, got %q", feat)
	}
	if branch != "br" {
		t.Errorf("expected br, got %q", branch)
	}
}

func TestWorktreePath_CheckoutReturnsEmpty(t *testing.T) {
	ws := Workspace{MetadataRoot: "/repo/.tws", Mode: ModeCheckout}
	got := ws.WorktreePath("feat", "branch")
	if got != "" {
		t.Errorf("WorktreePath in checkout should return empty, got %q", got)
	}
}

func TestWorktreePath_ExternalReturnsPath(t *testing.T) {
	ws := Workspace{MetadataRoot: "/home/user/tws", Mode: ModeExternal}
	got := ws.WorktreePath("feat", "branch")
	want := "/home/user/tws/feat/worktrees/branch"
	if got != want {
		t.Errorf("WorktreePath external got %q, want %q", got, want)
	}
}

func TestFeaturePath_CheckoutNewLayout(t *testing.T) {
	ws := Workspace{MetadataRoot: "/repo/.tws", Mode: ModeCheckout}
	got := ws.FeaturePath("billing")
	want := "/repo/.tws/features/billing"
	if got != want {
		t.Errorf("FeaturePath checkout got %q, want %q", got, want)
	}
}

func TestFeaturePath_ExternalUnchanged(t *testing.T) {
	ws := Workspace{MetadataRoot: "/home/user/tws", Mode: ModeExternal}
	got := ws.FeaturePath("billing")
	want := "/home/user/tws/billing"
	if got != want {
		t.Errorf("FeaturePath external got %q, want %q", got, want)
	}
}

func TestErrWorktreeUnsupported(t *testing.T) {
	if ErrWorktreeUnsupported == nil {
		t.Fatal("ErrWorktreeUnsupported should not be nil")
	}
	if !strings.Contains(ErrWorktreeUnsupported.Error(), "checkout mode") {
		t.Errorf("error should mention checkout mode, got: %v", ErrWorktreeUnsupported)
	}
}

func TestCheckoutStateDir(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	got := ws.CheckoutStateDir()
	if got != filepath.Join(dir, "state") {
		t.Errorf("expected %s/state, got %s", dir, got)
	}
}

func TestListFeaturesResolved_SortedExcludesSpaceOwnedName(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{MetadataRoot: dir, Mode: ModeCheckout}

	for _, path := range []string{
		filepath.Join(dir, "features", "beta"),
		filepath.Join(dir, "features", "alpha"),
		filepath.Join(dir, "legacy-feat"),
		filepath.Join(dir, "learning"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSpacesFixture(t, dir, `version: 1
spaces:
  - name: notes
    kind: learning
    path: learning
    added_at: 2026-01-01T00:00:00Z
`)

	features, err := ws.ListFeaturesResolved()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "beta", "legacy-feat"}
	if len(features) != len(want) {
		t.Fatalf("expected %v, got %v", want, features)
	}
	for i := range want {
		if features[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, features)
		}
	}
}

func TestListFeaturesResolved_ReservedIncludesSpacesFile(t *testing.T) {
	if !isReservedDir("spaces.yaml") {
		t.Fatal("spaces.yaml must be a reserved name")
	}
	if err := validateFeatureName("spaces.yaml"); err == nil {
		t.Fatal("a feature may not be named spaces.yaml")
	}
}
