package cli

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
)

// TestExport_RuntimeStateNeverEntersAnArchive is insurance against the
// exportTarball allow-list regressing: a feature directory holding all three
// runtime-state files must still export only workspace.yaml and inject/**.
func TestExport_RuntimeStateNeverEntersAnArchive(t *testing.T) {
	featurePath := t.TempDir()
	injectDir := filepath.Join(featurePath, "inject")
	if err := os.MkdirAll(injectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(featurePath, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("inject/CLAUDE.local.md", "keep me\n")
	write("stack.yaml", "branches: []\n")
	write(".sync-state.yaml", "failed_branch: api\n")
	write(".sync-state.v2.yaml", "state_version: 2\n")
	write(".sync-run.lock", "pid: 1\n")

	out := filepath.Join(t.TempDir(), "auth.tar.gz")
	_, _ = syncCaptureStreams(t, func() {
		if err := exportTarball("auth", featurePath, internal.WorkspaceExport{}, out); err != nil {
			t.Errorf("exportTarball: %v", err)
		}
	})

	names := tarballEntries(t, out)
	sort.Strings(names)
	want := []string{"inject/CLAUDE.local.md", "workspace.yaml"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("archive contents = %v, want %v", names, want)
	}
	for _, name := range names {
		if isRuntimeState(name) {
			t.Fatalf("runtime state %q entered the archive", name)
		}
	}
}

// TestImport_RuntimeStateIsFilteredOnExtraction pins that a hand-crafted
// archive carrying planted runtime state never writes it to disk.
func TestImport_RuntimeStateIsFilteredOnExtraction(t *testing.T) {
	for _, name := range []string{".sync-state.yaml", ".sync-state.v2.yaml", ".sync-run.lock", ".tws/state/x.yaml"} {
		if !isRuntimeState(name) {
			t.Fatalf("%q must be filtered on import: foreign live state would otherwise be planted", name)
		}
	}
	for _, name := range []string{"stack.yaml", "workspace.yaml", "inject/foo.txt", "state/x", ".sync-state.v2.yaml.bak"} {
		if isRuntimeState(name) {
			t.Fatalf("%q must not be filtered: the list is exact-name only", name)
		}
	}
}

func tarballEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close() //nolint:errcheck
	var names []string
	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	return names
}
