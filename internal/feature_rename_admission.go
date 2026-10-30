package internal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const featureRenameJournalDir = "feature-renames"
const featureRenameLockDir = "feature-rename-locks"

// ExternalFeatureAdmission reserves names, including names with no directory.
// Ordinary commands hold shared locks through their effects/launch. Rename
// holds exclusive locks before checking or publishing its journal. Lock files
// are permanent: unlinking one would split the cooperating lock domain.
type ExternalFeatureAdmission struct {
	root               featureRenameDirectory
	alias              *featureRenameRootAlias
	directory          featureRenameDirectory
	files              []*os.File
	contextDirectories []featureRenameDirectory
}

type featureRenameJournalGuard struct {
	file *os.File
}

// Only journal file publication/inspection is serialized here. Unrelated
// feature commands never hold this lock through their effects or sessions.
// In particular, they cannot mistake a live, partially written event for a
// malformed pending operation in another feature.
func lockFeatureRenameJournals(root string, exclusive, create bool) (*featureRenameJournalGuard, error) {
	base, err := featureRenameOpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer base.Close() //nolint:errcheck
	parentName := filepath.Join(workspaceMarker, featureRenameLockDir)
	dir, identity, err := managedOpenPinnedDirectory(base, parentName, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close() //nolint:errcheck
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	}
	if err := featureRenameCheckLockType(dir, ".journals", create); err != nil {
		return nil, err
	}
	if err := featureRenameCheckpoint("before-lock-open", filepath.Join(root, parentName, ".journals")); err != nil {
		return nil, err
	}
	fd, err := featureRenameOpenLock(dir, ".journals", flags, unix.Openat)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(root, parentName, ".journals"))
	fail := func(cause error) (*featureRenameJournalGuard, error) {
		return nil, errors.Join(cause, file.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return fail(errors.New("invalid feature rename journal publication lock"))
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err := unix.Flock(fd, mode); err != nil {
		return fail(err)
	}
	id, err := managedIdentityFromInfo(info)
	if err != nil {
		return fail(err)
	}
	if err := managedVerifyPathIdentity(file.Name(), id); err != nil {
		return fail(err)
	}
	if err := managedVerifyPinnedDirectoryBinding(base, parentName, identity); err != nil {
		return fail(err)
	}
	if create {
		if err := errors.Join(file.Sync(), dir.Sync()); err != nil {
			return fail(err)
		}
	}
	return &featureRenameJournalGuard{file: file}, nil
}

func (guard *featureRenameJournalGuard) Release() error {
	return errors.Join(flockUnlock(guard.file), guard.file.Close())
}

func featureRenameJournalRoot(root string) string {
	return filepath.Join(ExternalMarkerDir(root), featureRenameJournalDir)
}

func featureRenameCheckLockType(parent *os.File, name string, create bool) error {
	var stat unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if create && errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size != 0 {
		return fmt.Errorf("invalid feature rename lock %s: expected an empty regular file", name)
	}
	return nil
}

// Darwin can return ENOENT for simultaneous O_CREAT|O_NOFOLLOW opens even
// when another creator has just published the regular file. Separate exclusive
// creation from opening an existing winner; never retry arbitrary ENOENT or
// replace a lock inode, which would split the cooperating lock domain.
func featureRenameOpenLock(
	parent *os.File,
	name string,
	flags int,
	openat func(int, string, int, uint32) (int, error),
) (int, error) {
	if flags&unix.O_CREAT == 0 {
		return openat(int(parent.Fd()), name, flags, 0o600)
	}
	fd, err := openat(int(parent.Fd()), name, flags|unix.O_EXCL, 0o600)
	if !errors.Is(err, unix.EEXIST) {
		return fd, err
	}
	if err := featureRenameCheckLockType(parent, name, false); err != nil {
		return -1, err
	}
	return openat(int(parent.Fd()), name, flags&^(unix.O_CREAT|unix.O_EXCL), 0o600)
}

func acquireFeatureRenameAdmission(root string, names []string, exclusive, registry, existingOnly bool) (*ExternalFeatureAdmission, error) {
	if root == "" {
		return nil, errors.New("feature rename admission requires a workspace root")
	}
	for _, name := range names {
		if err := validateFeatureName(name); err != nil {
			return nil, err
		}
	}
	binding, alias, err := resolveFeatureRenameRoot(root)
	if err != nil {
		return nil, err
	}
	return acquireFeatureRenameAdmissionAt(binding, alias, names, exclusive, registry, existingOnly)
}

func acquireFeatureRenameAdmissionAt(binding featureRenameDirectory, alias *featureRenameRootAlias, names []string, exclusive, registry, existingOnly bool) (*ExternalFeatureAdmission, error) {
	root := binding.Path
	held := &ExternalFeatureAdmission{root: binding, alias: alias}
	fail := func(cause error) (*ExternalFeatureAdmission, error) {
		return nil, errors.Join(cause, held.Release())
	}
	if err := alias.verify(); err != nil {
		return fail(err)
	}
	base, err := featureRenamePinnedRoot(binding)
	if err != nil {
		return fail(err)
	}
	defer base.Close() //nolint:errcheck
	lockDirectory := filepath.Join(workspaceMarker, featureRenameLockDir)
	dir, identity, err := managedOpenPinnedDirectory(base, lockDirectory, !existingOnly)
	if err != nil {
		return fail(fmt.Errorf("opening feature admission directory %s: %w", filepath.Join(root, lockDirectory), err))
	}
	defer dir.Close() //nolint:errcheck
	held.directory = featureRenameDirectory{Path: filepath.Join(root, lockDirectory), Identity: identity}
	names = append([]string(nil), names...)
	// Use actual name components, not hashes: the filesystem itself then
	// arbitrates case/normalization aliases exactly as for feature directories.
	sort.Strings(names)
	if registry {
		names = append([]string{".registry"}, names...)
	}
	seen := map[managedObjectIdentity]bool{}
	for _, name := range names {
		flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if !existingOnly {
			flags |= unix.O_CREAT
		}
		if err := featureRenameCheckLockType(dir, name, !existingOnly); err != nil {
			return fail(fmt.Errorf("checking feature admission lock %s: %w", name, err))
		}
		if err := featureRenameCheckpoint("before-lock-open", filepath.Join(root, lockDirectory, name)); err != nil {
			return fail(err)
		}
		fd, err := featureRenameOpenLock(dir, name, flags, unix.Openat)
		if err != nil {
			return fail(fmt.Errorf("opening feature admission lock %s: %w", name, err))
		}
		file := os.NewFile(uintptr(fd), filepath.Join(root, lockDirectory, name))
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			_ = file.Close()
			return fail(errors.Join(err, fmt.Errorf("invalid feature admission lock %s", name)))
		}
		id, err := managedIdentityFromInfo(info)
		if err != nil {
			_ = file.Close()
			return fail(err)
		}
		if seen[id] {
			_ = file.Close()
			if exclusive {
				return fail(errors.New("feature rename names alias the same filesystem binding"))
			}
			continue
		}
		seen[id] = true
		operation := unix.LOCK_SH
		if exclusive {
			operation = unix.LOCK_EX
		}
		if err := unix.Flock(fd, operation|unix.LOCK_NB); err != nil {
			_ = file.Close()
			return fail(fmt.Errorf("feature rename admission is busy for %q; retry after the current command exits: %w", name, err))
		}
		held.files = append(held.files, file)
		if err := managedVerifyPathIdentity(filepath.Join(root, lockDirectory, name), id); err != nil {
			return fail(err)
		}
		if err := file.Sync(); err != nil {
			return fail(err)
		}
	}
	if err := dir.Sync(); err != nil {
		return fail(err)
	}
	if err := managedVerifyPinnedDirectoryBinding(base, lockDirectory, identity); err != nil {
		return fail(err)
	}
	if err := held.verify(); err != nil {
		return fail(err)
	}
	return held, nil
}

func (held *ExternalFeatureAdmission) Release() error {
	if held == nil {
		return nil
	}
	var errs []error
	if err := held.alias.verify(); err != nil {
		errs = append(errs, err)
	}
	for _, directory := range held.contextDirectories {
		errs = append(errs, directory.verify())
	}
	for i := len(held.files) - 1; i >= 0; i-- {
		errs = append(errs, flockUnlock(held.files[i]), held.files[i].Close())
	}
	held.files = nil
	return errors.Join(errs...)
}

func (held *ExternalFeatureAdmission) verify() error {
	if held == nil {
		return nil
	}
	if err := held.alias.verify(); err != nil {
		return err
	}
	if err := held.root.verify(); err != nil {
		return err
	}
	if err := held.directory.verify(); err != nil {
		return err
	}
	for _, directory := range held.contextDirectories {
		if err := directory.verify(); err != nil {
			return err
		}
	}
	for _, file := range held.files {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		identity, err := managedIdentityFromInfo(info)
		if err != nil {
			return err
		}
		if err := managedVerifyPathIdentity(file.Name(), identity); err != nil {
			return err
		}
	}
	return nil
}

// Verify rechecks the admitted root/alias and lock identities without
// reacquiring the reservation or resolving a different workspace.
func (held *ExternalFeatureAdmission) Verify() error {
	return held.verify()
}

// AcquireExternalFeatureAdmission must be held until the command's last
// effect, not merely used as a preflight query. Checkout callers skip it.
func AcquireExternalFeatureAdmission(root string, names ...string) (*ExternalFeatureAdmission, error) {
	// Preserve mutation-free space/metadata refusals. These probes are not
	// admission: both are repeated after the name reservation is held.
	if err := CheckExternalFeatureRenamePending(root, names...); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := GuardFeatureName(root, name); err != nil {
			return nil, err
		}
	}
	held, err := acquireFeatureRenameAdmission(root, names, false, false, false)
	if err != nil {
		return nil, err
	}
	if err := CheckExternalFeatureRenamePending(held.root.Path, names...); err != nil {
		return nil, errors.Join(err, held.Release())
	}
	for _, name := range names {
		if err := GuardFeatureName(held.root.Path, name); err != nil {
			return nil, errors.Join(err, held.Release())
		}
	}
	if err := held.verify(); err != nil {
		return nil, errors.Join(err, held.Release())
	}
	return held, nil
}

// AcquireExternalFeatureContextAdmission binds context writes to their actual
// containing external workspace, independently of an unrelated root override.
// An equivalent selected alias remains part of the reservation's evidence.
func AcquireExternalFeatureContextAdmission(root, cwd string) (*ExternalFeatureAdmission, error) {
	physicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	context, err := featureRenameDirectoryAt(physicalCWD)
	if err != nil {
		return nil, err
	}
	containingRoot, err := featureRenameContainingRoot(physicalCWD)
	if err != nil {
		return nil, err
	}
	var binding featureRenameDirectory
	var alias *featureRenameRootAlias
	if _, err := os.Lstat(root); err == nil {
		binding, alias, err = resolveFeatureRenameRoot(root)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if containingRoot != "" && containingRoot != binding.Path {
		binding, err = featureRenameDirectoryAt(containingRoot)
		if err != nil {
			return nil, err
		}
		alias = nil
	}
	if binding.Path == "" {
		return nil, nil
	}
	relative, err := filepath.Rel(binding.Path, physicalCWD)
	if err != nil {
		return nil, err
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.Join(binding.verify(), alias.verify())
	}
	feature := strings.SplitN(relative, string(filepath.Separator), 2)[0]
	if err := validateFeatureName(feature); err != nil {
		return nil, err
	}
	if err := CheckExternalFeatureRenamePending(binding.Path, feature); err != nil {
		return nil, err
	}
	if err := GuardFeatureName(binding.Path, feature); err != nil {
		return nil, err
	}
	directories := []featureRenameDirectory{context}
	for path := filepath.Dir(physicalCWD); path != binding.Path; path = filepath.Dir(path) {
		directory, err := featureRenameDirectoryAt(path)
		if err != nil {
			return nil, err
		}
		directories = append(directories, directory)
	}
	held, err := acquireFeatureRenameAdmissionAt(binding, alias, []string{feature}, false, false, false)
	if err != nil {
		return nil, err
	}
	held.contextDirectories = directories
	if err := CheckExternalFeatureRenamePending(binding.Path, feature); err != nil {
		return nil, errors.Join(err, held.Release())
	}
	if err := held.verify(); err != nil {
		return nil, errors.Join(err, held.Release())
	}
	return held, nil
}

func featureRenameContainingRoot(cwd string) (string, error) {
	for path := cwd; ; path = filepath.Dir(path) {
		marker := filepath.Join(path, workspaceMarker)
		info, err := os.Lstat(marker)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("invalid external workspace marker at %s", marker)
			}
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if parent := filepath.Dir(path); parent == path {
			return "", nil
		}
	}
}

func acquireFeatureRenameSpacesAdmission(root string) (*ExternalFeatureAdmission, error) {
	pending, err := featureRenamePendingPlans(root)
	if err != nil {
		return nil, fmt.Errorf("checking pending renames before spaces admission: %w", err)
	}
	if len(pending) > 0 {
		return nil, featureRenamePendingError(pending[0])
	}
	if _, err := readSpaces(root); err != nil {
		return nil, fmt.Errorf("reading registry before spaces admission: %w", err)
	}
	held, err := acquireFeatureRenameAdmission(root, nil, false, true, false)
	if err != nil {
		return nil, fmt.Errorf("claiming spaces rename admission: %w", err)
	}
	pending, err = featureRenamePendingPlans(held.root.Path)
	if err == nil && len(pending) > 0 {
		err = featureRenamePendingError(pending[0])
	}
	if err != nil {
		return nil, errors.Join(err, held.Release())
	}
	if err := held.verify(); err != nil {
		return nil, errors.Join(err, held.Release())
	}
	return held, nil
}

func featureRenamePendingError(plan *featureRenamePlan) error {
	return fmt.Errorf("external feature rename %q -> %q is pending at %s; use 'tws rename feature %s %s --continue' or '--abort' with this version of tws",
		plan.OldName, plan.NewName, plan.Operation.Path, plan.OldName, plan.NewName)
}

// CheckExternalFeatureRenamePending is also useful for read-only diagnostics.
// It is not a substitute for the lock/recheck protocol above.
func CheckExternalFeatureRenamePending(root string, names ...string) error {
	pending, err := featureRenamePendingPlans(root)
	if err != nil {
		return err
	}
	for _, plan := range pending {
		for _, name := range names {
			if featureRenameNamesAlias(root, name, plan.OldName) || featureRenameNamesAlias(root, name, plan.NewName) {
				return featureRenamePendingError(plan)
			}
		}
	}
	return nil
}

func featureRenameNamesAlias(root, left, right string) bool {
	if left == right {
		return true
	}
	base := filepath.Join(ExternalMarkerDir(root), featureRenameLockDir)
	a, aErr := os.Lstat(filepath.Join(base, left))
	b, bErr := os.Lstat(filepath.Join(base, right))
	return aErr == nil && bErr == nil && os.SameFile(a, b)
}

func featureRenamePendingPlans(root string) (plans []*featureRenamePlan, retErr error) {
	if root == "" {
		return nil, errors.New("empty feature rename root")
	}
	// The ordinary no-journal path needs one scoped filesystem probe, not
	// another workspace/root discovery. Existing evidence still receives the
	// full alias, lock, type, version, and identity validation below.
	if _, err := os.Lstat(featureRenameJournalRoot(cleanAbsolute(root))); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	binding, alias, err := resolveFeatureRenameRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, alias.verify()) }()
	root = binding.Path
	journalRoot := featureRenameJournalRoot(root)
	if _, err := os.Lstat(journalRoot); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	guard, err := lockFeatureRenameJournals(root, false, false)
	if err != nil {
		return nil, fmt.Errorf("cannot safely inspect feature rename evidence at %s: %w", journalRoot, err)
	}
	defer func() { retErr = errors.Join(retErr, guard.Release()) }()
	pinned, err := featureRenameOpenRoot(journalRoot)
	if err != nil {
		return nil, err
	}
	defer pinned.Close() //nolint:errcheck
	entries, err := fs.ReadDir(pinned.FS(), ".")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !markerIDRegexp.MatchString(entry.Name()) {
			return nil, fmt.Errorf("unknown feature rename evidence at %s", filepath.Join(journalRoot, entry.Name()))
		}
		tx, err := loadFeatureRename(root, entry.Name())
		if err != nil {
			return nil, err
		}
		if !tx.has("cleaned", "done") {
			plans = append(plans, tx.plan)
		}
	}
	return plans, nil
}
