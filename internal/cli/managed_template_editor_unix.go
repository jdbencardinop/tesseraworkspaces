//go:build !windows

package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"golang.org/x/sys/unix"
)

func managedTemplateTerminalInput(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	return err == nil
}

type managedEditorSnapshot struct {
	info fs.FileInfo
	hash [32]byte
}

type managedEditorDirectory struct {
	path string
	info fs.FileInfo
}

type managedTemplateEditorScratch struct {
	path    string
	name    string
	base    *os.Root
	root    *os.Root
	dir     *os.File
	parents []managedEditorDirectory
	files   map[string]managedEditorSnapshot
}

func newManagedTemplateEditorScratch(plans []*internal.ManagedTemplatePlan, current, incoming []byte) (*managedTemplateEditorScratch, error) {
	basePath, err := canonicalManagedEditorPath(os.TempDir())
	if err != nil {
		return nil, err
	}
	globalRegistry, err := internal.GlobalTemplateRegistryRoot()
	if err != nil {
		return nil, err
	}
	roots := []string{globalRegistry}
	for _, plan := range plans {
		roots = append(roots, plan.Request.FeaturePath, plan.Request.Workspace.MetadataRoot, plan.Request.Workspace.RepoRoot)
		for _, evidence := range plan.SourceEvidence {
			roots = append(roots, filepath.Dir(evidence.Path))
		}
	}
	parents, err := managedEditorDirectoryChain(basePath)
	if err != nil {
		return nil, err
	}
	if err := validateManagedEditorScratchRoots(basePath, parents, roots); err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	opened, err := base.Open(".")
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	baseInfo, statErr := opened.Stat()
	if err := errors.Join(statErr, opened.Close()); err != nil {
		_ = base.Close()
		return nil, err
	}
	if !os.SameFile(baseInfo, parents[0].info) {
		_ = base.Close()
		return nil, errors.New("editor scratch parent changed while opening")
	}
	if err := validateManagedEditorParents(parents); err != nil {
		_ = base.Close()
		return nil, err
	}
	if err := validateManagedEditorScratchRoots(basePath, parents, roots); err != nil {
		_ = base.Close()
		return nil, err
	}
	name := "tws-template-edit-" + rand.Text()
	if err := base.Mkdir(name, 0o700); err != nil {
		_ = base.Close()
		return nil, err
	}
	path := filepath.Join(basePath, name)
	root, err := base.OpenRoot(name)
	if err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("opening private editor directory %q: %w", path, err)
	}
	dir, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		_ = base.Close()
		return nil, fmt.Errorf("opening private editor directory %q: %w", path, err)
	}
	scratch := &managedTemplateEditorScratch{
		path: path, name: name, base: base, root: root, dir: dir, parents: parents,
		files: make(map[string]managedEditorSnapshot),
	}
	fail := func(cause error) (*managedTemplateEditorScratch, error) {
		return nil, errors.Join(fmt.Errorf("%w; editor scratch retained at %q", cause, path), scratch.Close())
	}
	if err := scratch.validateDirectories(); err != nil {
		return fail(err)
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"current", current}, {"incoming", incoming}, {"edit", current}} {
		if err := scratch.writePrivate(file.name, file.data); err != nil {
			return fail(err)
		}
	}
	return scratch, nil
}

func validateManagedEditorScratchRoots(basePath string, parents []managedEditorDirectory, roots []string) error {
	for _, protected := range roots {
		root, err := canonicalManagedEditorPath(protected)
		if err != nil {
			return err
		}
		outsideError := fmt.Errorf("editor scratch must be outside feature, worktree, and registry roots; choose a safe TMPDIR outside %q", protected)
		relative, err := filepath.Rel(root, basePath)
		if err != nil {
			return err
		}
		if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return outsideError
		}
		info, err := os.Lstat(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspecting protected editor root %q: %w", protected, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("protected editor root %q must be a regular directory", protected)
		}
		// EvalSymlinks does not normalize filesystem case or Unicode aliases.
		// Compare every physical ancestor, including the scratch base itself.
		for _, parent := range parents {
			if os.SameFile(info, parent.info) {
				return outsideError
			}
		}
	}
	return nil
}

func canonicalManagedEditorPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolvedParent, err := canonicalManagedEditorPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(absolute)), nil
}

func managedEditorDirectoryChain(path string) ([]managedEditorDirectory, error) {
	var directories []managedEditorDirectory
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe editor scratch parent %q", path)
		}
		directories = append(directories, managedEditorDirectory{path: path, info: info})
		parent := filepath.Dir(path)
		if parent == path {
			return directories, nil
		}
		path = parent
	}
}

func (scratch *managedTemplateEditorScratch) filePath(name string) string {
	return filepath.Join(scratch.path, name)
}

func (scratch *managedTemplateEditorScratch) validateDirectories() error {
	if err := validateManagedEditorParents(scratch.parents); err != nil {
		return err
	}
	held, err := scratch.dir.Stat()
	if err != nil {
		return err
	}
	current, err := scratch.base.Lstat(scratch.name)
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		current.Mode() != fs.ModeDir|0o700 || !os.SameFile(held, current) {
		return fmt.Errorf("private editor directory identity or permissions changed at %q", scratch.path)
	}
	return nil
}

func validateManagedEditorParents(parents []managedEditorDirectory) error {
	for _, parent := range parents {
		info, err := os.Lstat(parent.path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(info, parent.info) {
			return fmt.Errorf("editor scratch parent identity changed at %q", parent.path)
		}
	}
	return nil
}

func (scratch *managedTemplateEditorScratch) writePrivate(name string, data []byte) error {
	file, err := scratch.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Chmod(0o600), file.Sync(), file.Close()); err != nil {
		return err
	}
	_, snapshot, err := scratch.readPrivate(name)
	if err != nil {
		return err
	}
	scratch.files[name] = snapshot
	return nil
}

func managedEditorPrivateFile(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode() == 0o600 && stat.Nlink == 1
}

func (scratch *managedTemplateEditorScratch) readPrivate(name string) ([]byte, managedEditorSnapshot, error) {
	if err := scratch.validateDirectories(); err != nil {
		return nil, managedEditorSnapshot{}, err
	}
	// O_NONBLOCK also prevents a swapped FIFO from hanging before fstat.
	fd, err := unix.Openat(int(scratch.dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, managedEditorSnapshot{}, fmt.Errorf("opening staged editor file %q without following links: %w", scratch.filePath(name), err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close() //nolint:errcheck
	before, err := file.Stat()
	if err != nil {
		return nil, managedEditorSnapshot{}, err
	}
	if !managedEditorPrivateFile(before) {
		return nil, managedEditorSnapshot{}, fmt.Errorf("staged editor file %q must be regular, private (0600), and not hard-linked", scratch.filePath(name))
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, managedEditorSnapshot{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, managedEditorSnapshot{}, err
	}
	pathInfo, err := scratch.root.Lstat(name)
	if err != nil {
		return nil, managedEditorSnapshot{}, err
	}
	if !managedEditorPrivateFile(after) || !managedEditorPrivateFile(pathInfo) ||
		!os.SameFile(before, after) || !os.SameFile(after, pathInfo) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, managedEditorSnapshot{}, fmt.Errorf("staged editor file %q changed while being read", scratch.filePath(name))
	}
	if err := scratch.validateDirectories(); err != nil {
		return nil, managedEditorSnapshot{}, err
	}
	return data, managedEditorSnapshot{info: after, hash: sha256.Sum256(data)}, nil
}

func (scratch *managedTemplateEditorScratch) verifySnapshot(name string, expected managedEditorSnapshot) error {
	_, current, err := scratch.readPrivate(name)
	if err != nil {
		return err
	}
	if !os.SameFile(expected.info, current.info) || current.hash != expected.hash {
		return fmt.Errorf("staged editor reference or accepted output changed at %q", scratch.filePath(name))
	}
	return nil
}

func (scratch *managedTemplateEditorScratch) readCandidate() ([]byte, error) {
	for _, name := range []string{"current", "incoming"} {
		if err := scratch.verifySnapshot(name, scratch.files[name]); err != nil {
			return nil, err
		}
	}
	data, snapshot, err := scratch.readPrivate("edit")
	if err != nil {
		return nil, err
	}
	scratch.files["edit"] = snapshot
	return data, nil
}

func (scratch *managedTemplateEditorScratch) verifyAll() error {
	if err := scratch.validateDirectories(); err != nil {
		return err
	}
	entries, err := fs.ReadDir(scratch.root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, known := scratch.files[entry.Name()]; !known {
			return fmt.Errorf("unexpected editor residue at %q prevents automatic cleanup", scratch.filePath(entry.Name()))
		}
	}
	for name, expected := range scratch.files {
		if err := scratch.verifySnapshot(name, expected); err != nil {
			return err
		}
	}
	return nil
}

func (scratch *managedTemplateEditorScratch) cleanup() error {
	if err := scratch.verifyAll(); err != nil {
		return err
	}
	names := make([]string, 0, len(scratch.files))
	for name := range scratch.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := scratch.verifySnapshot(name, scratch.files[name]); err != nil {
			return err
		}
		if err := scratch.root.Remove(name); err != nil {
			return err
		}
	}
	if err := scratch.validateDirectories(); err != nil {
		return err
	}
	return scratch.base.Remove(scratch.name)
}

func (scratch *managedTemplateEditorScratch) Close() error {
	return errors.Join(scratch.dir.Close(), scratch.root.Close(), scratch.base.Close())
}
