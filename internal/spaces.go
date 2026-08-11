package internal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Spaces schema version.
const spacesVersion = 1

const (
	spacesFileName = "spaces.yaml"
	spacesLockName = ".spaces.lock"
)

// SpacesSaveHook is a test-only failure injector consulted at the top of
// saveSpaces. It is nil in production. Tests that set it MUST clear it with
// t.Cleanup. It is exported because the rename rollback it exercises lives in
// package internal/cli (same precedent as StepHook).
var SpacesSaveHook func(root string) error

// spacesReadHook is a test-only instrumentation hook called at the top of
// readSpaces with the path being read. It is nil in production.
var spacesReadHook func(path string)

// SpacesAnchor is the single explicitly resolved root that one space command
// operates on. No spaces helper ever re-derives a root of its own.
type SpacesAnchor struct {
	Root  string        // absolute path that holds spaces.yaml; used verbatim for I/O
	Canon string        // canonicalize(Root); used only for containment comparisons
	Mode  WorkspaceMode // external | checkout
}

// SpacesFile is the on-disk structure for the workspace sibling-space registry.
type SpacesFile struct {
	Version int          `yaml:"version" json:"version"`
	Spaces  []SpaceEntry `yaml:"spaces" json:"spaces"`
}

// SpaceEntry is one registered sibling space. tws owns the location metadata
// only; it never owns the linked tool's schema, content, or lifecycle.
type SpaceEntry struct {
	Name        string     `yaml:"name" json:"name"`
	Kind        string     `yaml:"kind" json:"kind"`
	Path        string     `yaml:"path" json:"path"`
	Description string     `yaml:"description,omitempty" json:"description,omitempty"`
	Feature     string     `yaml:"feature,omitempty" json:"feature,omitempty"`
	AddedAt     time.Time  `yaml:"added_at" json:"added_at"`
	UpdatedAt   *time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

// SpaceStatus is the computed health of a space target.
type SpaceStatus string

const (
	SpaceStatusOK           SpaceStatus = "ok"
	SpaceStatusMissing      SpaceStatus = "missing"
	SpaceStatusNotDirectory SpaceStatus = "not-a-directory"
)

// SpaceScope reports whether an entry is workspace-wide or feature-scoped.
type SpaceScope string

const (
	SpaceScopeWorkspace SpaceScope = "workspace"
	SpaceScopeFeature   SpaceScope = "feature"
)

// SpaceScopeStatus reports whether a feature-scoped entry still resolves.
type SpaceScopeStatus string

const (
	SpaceScopeStatusOK             SpaceScopeStatus = "ok"
	SpaceScopeStatusFeatureMissing SpaceScopeStatus = "feature-missing"
)

// SpaceView is the only type ever serialized to JSON by the CLI. Computed
// fields are recomputed per invocation and never persisted.
type SpaceView struct {
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	Path         string           `json:"path"`
	ResolvedPath string           `json:"resolved_path"`
	Description  string           `json:"description,omitempty"`
	Feature      string           `json:"feature,omitempty"`
	Scope        SpaceScope       `json:"scope"`
	ScopeStatus  SpaceScopeStatus `json:"scope_status"`
	Status       SpaceStatus      `json:"status"`
	AddedAt      time.Time        `json:"added_at"`
	UpdatedAt    *time.Time       `json:"updated_at,omitempty"`
}

// SpaceOwners maps a directory name that a feature listing would surface to
// the name of the registered space entry that owns it.
//
// The maps are the exact-spelling fast path. TopLevelOwner, FeatureOwner, and
// OwnerOfDir additionally compare filesystem identity, so a registered target
// spelled differently but naming the same directory — an absolute stored path
// inside the root, a symlinked spelling, or a different letter case on a
// case-insensitive volume — is still recognised as owned.
type SpaceOwners struct {
	TopLevel map[string]string // "<seg1>" directly under root -> owning space name
	Features map[string]string // "<seg2>" under root/features -> owning space name

	// root is the one root these owners were derived from. Identity lookups
	// join candidate names onto it; they never resolve a root of their own.
	root string
	// targets holds every registered target resolved against root plus, for
	// each derived map claim, the directory that claim actually covers
	// ("<root>/<seg1>" or "<root>/features/<seg2>"). A nested target such as
	// "<root>/Learning/notes" therefore also contributes "<root>/Learning",
	// so the claimed parent is recognised through any other spelling of the
	// same directory. Each entry carries its stat result when it exists, for
	// filesystem-identity comparison.
	targets []spaceOwnerTarget
}

// spaceOwnerTarget is one registered target resolved against the owning root.
type spaceOwnerTarget struct {
	space string
	path  string      // cleaned absolute resolved path
	canon string      // canonicalize(path)
	info  os.FileInfo // nil when the target does not currently exist
}

func newSpaceOwnerTarget(space, resolved string) spaceOwnerTarget {
	t := spaceOwnerTarget{space: space, path: cleanAbsolute(resolved), canon: canonicalPath(resolved)}
	if info, err := os.Stat(resolved); err == nil {
		t.info = info
	}
	return t
}

// owns reports whether dir names this target. info is dir's stat result, or
// nil when dir does not exist.
func (t spaceOwnerTarget) owns(dir string, info os.FileInfo) bool {
	if t.path == "" || dir == "" {
		return false
	}
	if samePathSpelling(t.path, dir) || samePathSpelling(t.canon, dir) {
		return true
	}
	return info != nil && t.info != nil && os.SameFile(info, t.info)
}

// addTarget records one owned directory for space. A directory already
// recorded keeps its first owner, so identity lookups and the maps — which
// are also first-wins — can never disagree about who owns a directory.
func (o *SpaceOwners) addTarget(space, dir string) {
	t := newSpaceOwnerTarget(space, dir)
	if t.path == "" {
		return
	}
	for _, existing := range o.targets {
		if existing.path == t.path {
			return
		}
	}
	o.targets = append(o.targets, t)
}

// TopLevelOwner reports the registered space that owns "<root>/<name>".
func (o SpaceOwners) TopLevelOwner(name string) (string, bool) {
	if space, ok := o.TopLevel[name]; ok {
		return space, true
	}
	return o.OwnerOfDir(o.dirIn(name))
}

// FeatureOwner reports the registered space that owns "<root>/features/<name>".
func (o SpaceOwners) FeatureOwner(name string) (string, bool) {
	if space, ok := o.Features[name]; ok {
		return space, true
	}
	return o.OwnerOfDir(o.dirIn("features", name))
}

// OwnerOfDir reports the registered space that owns an actual directory path.
// A directory carrying a feature signal is never claimed, which preserves the
// feature-hub exception of §7.1 for identity lookups too.
func (o SpaceOwners) OwnerOfDir(dir string) (string, bool) {
	if dir == "" || len(o.targets) == 0 {
		return "", false
	}
	if isFeatureDir(dir) {
		return "", false
	}
	var info os.FileInfo
	if st, err := os.Stat(dir); err == nil {
		info = st
	}
	for _, t := range o.targets {
		if t.owns(dir, info) {
			return t.space, true
		}
	}
	return "", false
}

func (o SpaceOwners) dirIn(parts ...string) string {
	if o.root == "" || len(parts) == 0 {
		return ""
	}
	return filepath.Join(append([]string{o.root}, parts...)...)
}

// ErrSpaceNameConflict reports that a logical feature name collides with the
// directory of a registered space under Root.
type ErrSpaceNameConflict struct {
	Feature string
	Space   string
	Root    string
}

func (e *ErrSpaceNameConflict) Error() string {
	return fmt.Sprintf(
		"cannot use feature name %q: it is the top-level directory of registered space %q in %s; choose another feature name or run 'tws space remove %s'",
		e.Feature, e.Space, spacesPath(e.Root), e.Space)
}

var (
	spaceKindRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// SpaceConventionalKinds are documented conventions offered by shell
	// completion. tws enumerates no closed set; they are never used for
	// validation.
	SpaceConventionalKinds = []string{"learning", "tickets", "patching", "research", "docs"}
)

// ---------------------------------------------------------------------------
// Anchor resolution
// ---------------------------------------------------------------------------

// ResolveSpacesAnchor resolves the one root a space command operates on.
// This is the only spaces helper permitted to call RequireWorkspace or
// TwsRoot, and it is called by the `tws space` subcommands only.
func ResolveSpacesAnchor() (SpacesAnchor, error) {
	ws, err := RequireWorkspace()
	if err == nil {
		if ws.Mode == ModeCheckout {
			// TWS_ROOT is ignored in checkout mode, exactly as every other
			// checkout command ignores it.
			return newSpacesAnchor(ws.MetadataRoot, ModeCheckout), nil
		}
		// External feature mutations build from TwsRoot(); the spaces root
		// must be the root where this workspace's features actually live.
		return newSpacesAnchor(TwsRoot(), ModeExternal), nil
	}

	root := TwsRoot()
	if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
		return newSpacesAnchor(root, ModeExternal), nil
	}
	return SpacesAnchor{}, err
}

func newSpacesAnchor(root string, mode WorkspaceMode) SpacesAnchor {
	return SpacesAnchor{Root: root, Canon: canonicalize(root), Mode: mode}
}

func spacesPath(root string) string { return filepath.Join(root, spacesFileName) }

// SpacesFilePath returns the spaces registry path for a root, for use in
// user-facing messages.
func SpacesFilePath(root string) string { return spacesPath(root) }

func spacesLockPath(root string) string { return filepath.Join(root, spacesLockName) }

// ---------------------------------------------------------------------------
// Locking
// ---------------------------------------------------------------------------

// SpacesLock holds an exclusive advisory POSIX lock on <root>/.spaces.lock.
//
// Platform boundary: POSIX advisory locking via syscall.Flock, supported on
// macOS and Linux only. Windows is NOT supported.
type SpacesLock struct {
	f *os.File
}

// acquireSpacesLock creates the spaces root and lock file if needed and takes
// an exclusive advisory lock. Because it creates both, every mutator except
// SpaceAdd MUST os.Lstat the registry path first and skip acquisition when the
// registry is absent.
func acquireSpacesLock(root string) (*SpacesLock, error) {
	if root == "" {
		return nil, fmt.Errorf("empty spaces root")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("creating spaces root: %w", err)
	}
	lockPath := spacesLockPath(root)
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to follow symlinked %s", lockPath)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening spaces lock file: %w", err)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close() // best-effort; lock was never acquired
		return nil, fmt.Errorf("acquiring spaces lock: %w", err)
	}
	return &SpacesLock{f: f}, nil
}

// Release releases the spaces lock. Callers MUST check the returned error and
// join it with any primary error.
func (l *SpacesLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := flockUnlock(l.f)
	closeErr := l.f.Close()
	l.f = nil
	return errors.Join(unlockErr, closeErr)
}

// ---------------------------------------------------------------------------
// Read / decode / validate / write
// ---------------------------------------------------------------------------

// readSpaces loads <root>/spaces.yaml or returns (nil, nil) when it is absent.
// Absence never creates anything on disk.
func readSpaces(root string) (*SpacesFile, error) {
	path := spacesPath(root)
	if spacesReadHook != nil {
		spacesReadHook(path)
	}

	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading spaces file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to follow symlinked %s", path)
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading spaces file %s: %w", path, err)
	}
	return decodeSpaces(data, path)
}

// decodeSpaces parses and validates spaces bytes. The version is probed
// permissively first so a future schema is reported as a version error rather
// than an unknown-field error, and is never silently truncated by a later write.
func decodeSpaces(data []byte, path string) (*SpacesFile, error) {
	var probe struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parsing spaces file %s: %w", path, err)
	}
	if probe.Version <= 0 {
		return nil, fmt.Errorf("spaces file %s is malformed: missing or invalid schema version", path)
	}
	if probe.Version > spacesVersion {
		return nil, fmt.Errorf("spaces file %s uses schema version %d but this tws supports version %d; upgrade tws instead of modifying the file",
			path, probe.Version, spacesVersion)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f SpacesFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing spaces file %s: %w", path, err)
	}
	if err := validateSpaces(&f, path); err != nil {
		return nil, err
	}
	return &f, nil
}

// validateSpaces enforces the invariants every reader and mutator depends on.
// path is <root>/spaces.yaml, so the owning root is its directory; stored
// paths are compared in resolved form, which also catches a hand-edited
// absolute duplicate of a workspace-relative entry.
func validateSpaces(f *SpacesFile, path string) error {
	if f.Version <= 0 || f.Version > spacesVersion {
		return fmt.Errorf("spaces file %s has unsupported schema version %d", path, f.Version)
	}
	root := filepath.Dir(path)

	type scopeKey struct{ feature, name string }
	names := make(map[scopeKey]bool, len(f.Spaces))
	paths := make(map[scopeKey]string, len(f.Spaces))

	for i := range f.Spaces {
		e := &f.Spaces[i]
		if err := validateSpaceName(e.Name); err != nil {
			return fmt.Errorf("spaces file %s is invalid: entry %d name: %w", path, i, err)
		}
		if err := validateSpaceKind(e.Kind); err != nil {
			return fmt.Errorf("spaces file %s is invalid: entry %d kind: %w", path, i, err)
		}
		if err := validateSpaceDescription(e.Description); err != nil {
			return fmt.Errorf("spaces file %s is invalid: entry %d description: %w", path, i, err)
		}
		if err := validateSpaceStoredPath(e.Path); err != nil {
			return fmt.Errorf("spaces file %s is invalid: entry %d path: %w", path, i, err)
		}
		if e.Feature != "" {
			if err := validateFeatureName(e.Feature); err != nil {
				return fmt.Errorf("spaces file %s is invalid: entry %d feature: %w", path, i, err)
			}
		}

		key := scopeKey{feature: e.Feature, name: e.Name}
		if names[key] {
			return fmt.Errorf("spaces file %s is invalid: duplicate space %q in the same scope", path, e.Name)
		}
		names[key] = true

		pathKey := scopeKey{feature: e.Feature, name: resolveSpacePathIn(root, e.Path)}
		if owner, seen := paths[pathKey]; seen {
			return fmt.Errorf("spaces file %s is invalid: path %q is registered by both %q and %q in the same scope",
				path, e.Path, owner, e.Name)
		}
		paths[pathKey] = e.Name
	}
	return nil
}

func validateSpaceName(name string) error {
	if name == "." || name == ".." {
		return fmt.Errorf("space name %q is reserved", name)
	}
	if err := ValidateAlias(name); err != nil {
		return err
	}
	return nil
}

func validateSpaceKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("kind cannot be empty")
	}
	if len(kind) > 32 {
		return fmt.Errorf("kind too long (max 32 chars)")
	}
	if !spaceKindRegexp.MatchString(kind) {
		return fmt.Errorf("kind %q is malformed (allowed: lowercase letters, digits, and '-', starting with a letter or digit)", kind)
	}
	return nil
}

func validateSpaceDescription(description string) error {
	if description == "" {
		return nil
	}
	if len([]rune(description)) > 200 {
		return fmt.Errorf("description too long (max 200 chars)")
	}
	for _, r := range description {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("description must not contain control characters")
		}
	}
	return nil
}

// validateSpaceStoredPath accepts the two legal stored forms: a clean absolute
// path, or a clean workspace-relative path that stays lexically under the root.
func validateSpaceStoredPath(p string) error {
	if p == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("path %q is not canonical", p)
	}
	if filepath.IsAbs(p) {
		return nil
	}
	if p == "." {
		return fmt.Errorf("path %q refers to the workspace root", p)
	}
	if p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes the workspace root", p)
	}
	return nil
}

// saveSpaces atomically writes the spaces file. It never creates the spaces
// root as a side effect; only SpaceAdd may create it.
func saveSpaces(root string, f *SpacesFile) error {
	if SpacesSaveHook != nil {
		if err := SpacesSaveHook(root); err != nil {
			return err
		}
	}

	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshaling spaces file: %w", err)
	}

	tmpFile, err := os.CreateTemp(root, ".spaces-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp spaces file: %w", err)
	}
	tmp := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		closeErr := tmpFile.Close()
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("writing spaces file: %w", err), closeErr, removeErr)
	}
	if err := tmpFile.Sync(); err != nil {
		closeErr := tmpFile.Close()
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("syncing spaces file: %w", err), closeErr, removeErr)
	}
	if err := tmpFile.Close(); err != nil {
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("closing temp spaces file: %w", err), removeErr)
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("setting spaces file permissions: %w", err), removeErr)
	}
	if err := os.Rename(tmp, spacesPath(root)); err != nil {
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("renaming spaces file: %w", err), removeErr)
	}
	return nil
}

// sortSpaces orders entries by (feature, name) with workspace-wide first, so
// the file has a stable diff and listing needs no re-sort.
func sortSpaces(f *SpacesFile) {
	sort.SliceStable(f.Spaces, func(i, j int) bool {
		a, b := f.Spaces[i], f.Spaces[j]
		if a.Feature != b.Feature {
			return a.Feature < b.Feature
		}
		return a.Name < b.Name
	})
}

// ---------------------------------------------------------------------------
// Ownership (name-to-path protection)
// ---------------------------------------------------------------------------

// SpaceDirOwners scans the given root and only that root. It never re-resolves
// a root of its own and never reads a second spaces.yaml.
//
// A nil/absent <root>/spaces.yaml yields a zero SpaceOwners and a nil error.
// Any other read/decode/validate failure is returned already wrapped so every
// caller propagates one identical message.
func SpaceDirOwners(root string) (SpaceOwners, error) {
	if root == "" {
		return SpaceOwners{}, nil
	}
	f, err := readSpaces(root)
	if err != nil {
		return SpaceOwners{}, fmt.Errorf("cannot verify registered spaces in %s: %w", spacesPath(root), err)
	}
	return ownersFrom(root, f), nil
}

// ownersFrom derives the ownership maps from an already-read file. It is pure
// apart from direct feature-signal probes and target stats under root.
//
// A stored path contributes an owned directory name whenever it resolves
// inside root, whether it was stored workspace-relative or — as only
// hand-edited metadata can produce — absolute but still inside the root.
//
// Every derived claim also records the directory it covers, which is the
// registered target itself for a direct entry but its ancestor under root for
// a nested one. Without that, a nested target would leave its claimed parent
// reachable through any other spelling of the same directory.
func ownersFrom(root string, f *SpacesFile) SpaceOwners {
	owners := SpaceOwners{root: root}
	if f == nil || root == "" {
		return owners
	}
	for _, e := range f.Spaces {
		resolved := resolveSpacePathIn(root, e.Path)
		owners.addTarget(e.Name, resolved)

		rel, ok := spaceRelUnderRoot(root, e.Path, resolved)
		if !ok {
			continue
		}
		segs := strings.Split(filepath.ToSlash(rel), "/")
		if len(segs) == 0 || segs[0] == "" || segs[0] == "." {
			continue
		}
		if segs[0] == "features" && len(segs) >= 2 {
			name := segs[1]
			claimed := filepath.Join(root, "features", name)
			if name == "" || isFeatureDir(claimed) {
				continue
			}
			if owners.Features == nil {
				owners.Features = make(map[string]string)
			}
			if _, exists := owners.Features[name]; !exists {
				owners.Features[name] = e.Name
				owners.addTarget(e.Name, claimed)
			}
			continue
		}
		name := segs[0]
		claimed := filepath.Join(root, name)
		if isFeatureDir(claimed) {
			continue
		}
		if owners.TopLevel == nil {
			owners.TopLevel = make(map[string]string)
		}
		if _, exists := owners.TopLevel[name]; !exists {
			owners.TopLevel[name] = e.Name
			owners.addTarget(e.Name, claimed)
		}
	}
	return owners
}

// spaceRelUnderRoot reports the root-relative path of a stored entry. Relative
// entries are root-relative by definition; absolute entries are reported when
// they resolve inside the root under either the literal or canonical spelling.
func spaceRelUnderRoot(root, stored, resolved string) (string, bool) {
	if !filepath.IsAbs(stored) {
		cleaned := filepath.Clean(stored)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", false
		}
		return cleaned, true
	}
	for _, base := range []string{cleanAbsolute(root), canonicalPath(root)} {
		for _, target := range []string{cleanAbsolute(resolved), canonicalPath(resolved)} {
			if rel, _, ok := relativeUnder(base, target); ok {
				return rel, true
			}
		}
	}
	return "", false
}

// isFeatureDir reports whether dir carries a tws feature signal.
// Signals: stack.yaml, worktrees/, FEATURE.md.
//
// This is a direct filesystem probe by design: the spaces layer must never
// call feature resolution or listing, because ListFeaturesResolved calls
// SpaceDirOwners and any such call would be unbounded recursion.
func isFeatureDir(dir string) bool {
	if dir == "" {
		return false
	}
	for _, signal := range []string{"stack.yaml", "worktrees", "FEATURE.md"} {
		if _, err := os.Stat(filepath.Join(dir, signal)); err == nil {
			return true
		}
	}
	return false
}

// GuardFeatureName fails closed when feature is not a usable logical feature
// name, or when it would collide with a registered space directory under the
// given root. root MUST be the root the calling operation actually reads from,
// writes to, or destroys under.
//
// The name check runs first and independently of root: every guarded command
// joins the caller-supplied name under a root, so a name carrying a separator,
// a traversal segment, or a reserved directory must be refused before any join,
// stat, read, or removal — including when the registry is absent and when the
// caller passes an empty root. It reuses validateFeatureName, so the refusal
// message is the same one the path resolvers already produce.
//
// An empty feature stays a no-op: callers that treat "no feature given" as an
// unfiltered scope guard emptiness themselves, and the resolvers report it.
//
// Absent <root>/spaces.yaml -> nil, with no file, lock, or directory created.
func GuardFeatureName(root, feature string) error {
	if feature == "" {
		return nil
	}
	if err := validateFeatureName(feature); err != nil {
		return err
	}
	if root == "" {
		return nil
	}
	owners, err := SpaceDirOwners(root)
	if err != nil {
		return err
	}
	return guardFeatureNameIn(owners, root, feature)
}

// guardFeatureNameIn is the file-scoped guard form used by the locked
// transactions, which already hold an authoritative read. Both the top-level
// and the features/ layouts are consulted by filesystem identity, so a
// differently spelled but identical directory is still refused.
func guardFeatureNameIn(owners SpaceOwners, root, feature string) error {
	if space, ok := owners.TopLevelOwner(feature); ok {
		return &ErrSpaceNameConflict{Feature: feature, Space: space, Root: root}
	}
	if space, ok := owners.FeatureOwner(feature); ok {
		return &ErrSpaceNameConflict{Feature: feature, Space: space, Root: root}
	}
	return nil
}

// AnchorFeaturePath resolves feature under anchor.Root using the SpaceOwners
// value the caller already loaded. It performs NO file read of its own and
// never resolves a second root.
func AnchorFeaturePath(anchor SpacesAnchor, owners SpaceOwners, feature string) (string, error) {
	if err := validateFeatureName(feature); err != nil {
		return "", err
	}
	if err := guardFeatureNameIn(owners, anchor.Root, feature); err != nil {
		return "", err
	}

	if anchor.Mode != ModeCheckout {
		return filepath.Join(anchor.Root, feature), nil
	}

	newPath := filepath.Join(anchor.Root, "features", feature)
	legacyPath := filepath.Join(anchor.Root, feature)
	newExists := dirExists(newPath)
	legacyExists := dirExists(legacyPath) && !isReservedDir(feature)

	if newExists && legacyExists {
		return "", &ErrAmbiguousFeature{Feature: feature, LegacyPath: legacyPath, NewPath: newPath}
	}
	if newExists {
		return newPath, nil
	}
	if legacyExists {
		return legacyPath, nil
	}
	return newPath, nil
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// containsPath reports whether target is dir itself or a descendant of dir.
// Both arguments are canonicalized before comparison; the comparison is lexical.
// It is the fast path of pathContains, which every destructive check uses.
func containsPath(dir, target string) bool {
	if dir == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(canonicalize(dir), canonicalize(target))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pathContains reports whether target is dir itself or lives beneath it.
//
// It starts from the lexical answer and then walks target's ancestor chain,
// comparing each ancestor against dir with os.SameFile where both paths exist.
// That is what makes a destructive containment check correct when the same
// directory is spelled differently — a different letter case on a
// case-insensitive volume, or a symlinked or absolute in-root spelling.
func pathContains(dir, target string) bool {
	if dir == "" || target == "" {
		return false
	}
	if containsPath(dir, target) {
		return true
	}
	var dirInfo os.FileInfo
	if info, err := os.Stat(dir); err == nil {
		dirInfo = info
	}
	for _, start := range []string{cleanAbsolute(target), canonicalPath(target)} {
		if ancestorMatches(dir, dirInfo, start) {
			return true
		}
	}
	return false
}

// ancestorMatches walks from target up to the filesystem root looking for dir.
func ancestorMatches(dir string, dirInfo os.FileInfo, target string) bool {
	for cur := target; ; {
		if samePathSpelling(dir, cur) {
			return true
		}
		if dirInfo != nil {
			if info, err := os.Stat(cur); err == nil && os.SameFile(info, dirInfo) {
				return true
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// samePathSpelling compares two paths by cleaned absolute and canonical form.
// It is the lexical fallback used wherever a stat is unavailable.
func samePathSpelling(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if cleanAbsolute(a) == cleanAbsolute(b) {
		return true
	}
	return canonicalPath(a) == canonicalPath(b)
}

// canonicalPath canonicalizes p even when it does not exist yet: it resolves
// symlinks for the longest existing ancestor and re-appends the remainder.
// Plain canonicalize gives up on the whole path as soon as one segment is
// missing, which would make a not-yet-created target incomparable with an
// existing root reached through a symlink (macOS /var -> /private/var).
func canonicalPath(p string) string {
	if p == "" {
		return ""
	}
	abs := cleanAbsolute(p)
	rest := ""
	for cur := abs; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return filepath.Clean(resolved)
			}
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// sameTargetDir reports whether two resolved paths name the same directory,
// preferring filesystem identity and falling back to path spelling.
func sameTargetDir(a, b string) bool {
	if samePathSpelling(a, b) {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// errSpacesRootItself is returned when a user tries to register the spaces
// root as a space.
var errSpacesRootItself = errors.New("refusing to register the workspace root itself")

// normalizeSpacePath converts CLI input into the stored form. A relative CLI
// argument resolves against the current working directory (standard shell
// behaviour), never against the spaces root.
func normalizeSpacePath(anchor SpacesAnchor, input string) (stored, resolved string, err error) {
	if strings.TrimSpace(input) == "" {
		return "", "", fmt.Errorf("space path cannot be empty")
	}

	abs := cleanAbsolute(input)

	rel, isRoot, ok := relativeUnder(anchor.Canon, abs)
	if isRoot {
		return "", "", errSpacesRootItself
	}
	if !ok {
		// Retry against the canonical form so symlinked roots (macOS
		// /var -> /private/var) still produce portable relative entries.
		var canonRoot bool
		rel, canonRoot, ok = relativeUnder(anchor.Canon, canonicalize(abs))
		if canonRoot {
			return "", "", errSpacesRootItself
		}
	}
	if ok {
		return rel, filepath.Join(anchor.Root, rel), nil
	}
	return abs, abs, nil
}

// relativeUnder reports the relative path of target under base. isRoot is true
// when target is base itself.
func relativeUnder(base, target string) (rel string, isRoot bool, ok bool) {
	r, err := filepath.Rel(base, target)
	if err != nil {
		return "", false, false
	}
	if r == "." {
		return "", true, false
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false, false
	}
	return r, false, true
}

// resolveSpacePath applies the single resolution rule: absolute stored paths
// are used verbatim, relative ones join the anchor root.
func resolveSpacePath(anchor SpacesAnchor, stored string) string {
	return resolveSpacePathIn(anchor.Root, stored)
}

// resolveSpacePathIn is the root-scoped form used wherever only a root is in
// hand (owner derivation and the lifecycle transactions).
func resolveSpacePathIn(root, stored string) string {
	if filepath.IsAbs(stored) {
		return filepath.Clean(stored)
	}
	return filepath.Join(root, stored)
}

// SpaceResolvedPath resolves a stored space path against an anchor.
func SpaceResolvedPath(anchor SpacesAnchor, stored string) string {
	return resolveSpacePath(anchor, stored)
}

// SpaceStatusOf reports the computed health of a resolved space target.
// Existence and type follow symlinks: a symlink to a directory is valid.
func SpaceStatusOf(resolved string) SpaceStatus {
	info, err := os.Stat(resolved)
	if err != nil {
		return SpaceStatusMissing
	}
	if !info.IsDir() {
		return SpaceStatusNotDirectory
	}
	return SpaceStatusOK
}

// spaceScopeStatus reports whether a feature-scoped entry still resolves under
// the anchor root. Existence is probed directly; never through a resolver.
func spaceScopeStatus(anchor SpacesAnchor, feature string) SpaceScopeStatus {
	if feature == "" {
		return SpaceScopeStatusOK
	}
	if anchorFeatureExists(anchor, feature) {
		return SpaceScopeStatusOK
	}
	return SpaceScopeStatusFeatureMissing
}

func anchorFeatureExists(anchor SpacesAnchor, feature string) bool {
	if feature == "" {
		return false
	}
	if anchor.Mode == ModeCheckout && dirExists(filepath.Join(anchor.Root, "features", feature)) {
		return true
	}
	return dirExists(filepath.Join(anchor.Root, feature)) && !isReservedDir(feature)
}

// detectAnchorFeature performs the anchor-rooted cwd walk used by `space list`
// scope detection. It never calls DetectFeatureFromCwdE or any other resolver.
func detectAnchorFeature(anchor SpacesAnchor, owners SpaceOwners, cwd string) string {
	if cwd == "" || anchor.Root == "" {
		return ""
	}
	candidates := []string{cleanAbsolute(cwd), canonicalize(cwd)}
	bases := []string{anchor.Root, anchor.Canon}

	for _, base := range bases {
		if base == "" {
			continue
		}
		for _, candidate := range candidates {
			if feature := anchorFeatureFor(anchor, owners, base, candidate); feature != "" {
				return feature
			}
		}
	}
	return ""
}

func anchorFeatureFor(anchor SpacesAnchor, owners SpaceOwners, base, cwd string) string {
	if anchor.Mode == ModeCheckout {
		if name := firstSegmentUnder(filepath.Join(base, "features"), cwd); name != "" {
			if _, owned := owners.FeatureOwner(name); !owned && anchorFeatureExists(anchor, name) {
				return name
			}
		}
	}
	name := firstSegmentUnder(base, cwd)
	if name == "" || isReservedDir(name) {
		return ""
	}
	if _, owned := owners.TopLevelOwner(name); owned {
		return ""
	}
	if !anchorFeatureExists(anchor, name) {
		return ""
	}
	return name
}

func firstSegmentUnder(base, cwd string) string {
	rel, err := filepath.Rel(base, cwd)
	if err != nil {
		return ""
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." {
		return ""
	}
	return parts[0]
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

func spaceScopeOf(e SpaceEntry) SpaceScope {
	if e.Feature == "" {
		return SpaceScopeWorkspace
	}
	return SpaceScopeFeature
}

func spaceViewOf(anchor SpacesAnchor, e SpaceEntry) SpaceView {
	resolved := resolveSpacePath(anchor, e.Path)
	return SpaceView{
		Name:         e.Name,
		Kind:         e.Kind,
		Path:         e.Path,
		ResolvedPath: resolved,
		Description:  e.Description,
		Feature:      e.Feature,
		Scope:        spaceScopeOf(e),
		ScopeStatus:  spaceScopeStatus(anchor, e.Feature),
		Status:       SpaceStatusOf(resolved),
		AddedAt:      e.AddedAt,
		UpdatedAt:    e.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// space add / list / show / remove
// ---------------------------------------------------------------------------

// verifySpaceTarget enforces the existence, type, and feature-directory rules
// for an add target. Existence follows symlinks; Git-ness is never checked.
func verifySpaceTarget(resolved string) error {
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("space target %s does not exist", resolved)
		}
		return fmt.Errorf("space target %s cannot be inspected: %w", resolved, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("space target %s is not a directory", resolved)
	}
	if isFeatureDir(resolved) {
		return fmt.Errorf("cannot register %s: it is the feature directory %q in this workspace",
			resolved, filepath.Base(resolved))
	}
	return nil
}

// SpaceAddRequest carries the validated inputs of `tws space add`.
type SpaceAddRequest struct {
	Name        string
	Path        string
	Kind        string
	Description string
	Feature     string
}

// SpaceAdd registers a sibling space. It is the only operation in tws that
// creates the spaces root, its external marker, spaces.yaml, or .spaces.lock,
// and it never creates, moves, or deletes the target directory.
func SpaceAdd(anchor SpacesAnchor, req SpaceAddRequest) (entry SpaceEntry, created bool, err error) {
	if err := validateSpaceName(req.Name); err != nil {
		return SpaceEntry{}, false, err
	}
	if err := validateSpaceKind(req.Kind); err != nil {
		return SpaceEntry{}, false, err
	}
	if err := validateSpaceDescription(req.Description); err != nil {
		return SpaceEntry{}, false, err
	}

	// One advisory read: it informs --feature resolution and the feature-dir
	// guard only. The authoritative read for the write happens under the lock.
	owners, err := SpaceDirOwners(anchor.Root)
	if err != nil {
		return SpaceEntry{}, false, err
	}

	if req.Feature != "" {
		if _, ferr := AnchorFeaturePath(anchor, owners, req.Feature); ferr != nil {
			return SpaceEntry{}, false, ferr
		}
		if !anchorFeatureExists(anchor, req.Feature) {
			return SpaceEntry{}, false, fmt.Errorf("feature %q not found in this workspace", req.Feature)
		}
	}

	stored, resolved, err := normalizeSpacePath(anchor, req.Path)
	if err != nil {
		return SpaceEntry{}, false, err
	}

	if err := verifySpaceTarget(resolved); err != nil {
		return SpaceEntry{}, false, err
	}

	if err := os.MkdirAll(anchor.Root, 0755); err != nil {
		return SpaceEntry{}, false, fmt.Errorf("creating spaces root: %w", err)
	}
	if anchor.Mode != ModeCheckout {
		if err := EnsureExternalWorkspaceMarker(anchor.Root); err != nil {
			return SpaceEntry{}, false, err
		}
	}

	lock, err := acquireSpacesLock(anchor.Root)
	if err != nil {
		return SpaceEntry{}, false, err
	}
	defer func() {
		err = errors.Join(err, lock.Release())
	}()

	f, readErr := readSpaces(anchor.Root)
	if readErr != nil {
		return SpaceEntry{}, false, readErr
	}
	if f == nil {
		f = &SpacesFile{Version: spacesVersion}
	}

	// Re-verify the target under the lock. A concurrent feature delete holds
	// the same lock across its os.RemoveAll, so a pre-lock stat can be stale;
	// re-checking here guarantees no entry is ever registered into a directory
	// that was concurrently removed.
	if err := verifySpaceTarget(resolved); err != nil {
		return SpaceEntry{}, false, err
	}

	for _, existing := range f.Spaces {
		if existing.Feature != req.Feature {
			continue
		}
		existingResolved := resolveSpacePath(anchor, existing.Path)
		if existing.Name == req.Name {
			if existing.Kind == req.Kind && sameTargetDir(existingResolved, resolved) {
				return existing, false, nil
			}
			return SpaceEntry{}, false, fmt.Errorf("space %q already exists in this scope; remove it first", req.Name)
		}
		if sameTargetDir(existingResolved, resolved) {
			return SpaceEntry{}, false, fmt.Errorf("path %s is already registered as %q", resolved, existing.Name)
		}
	}

	entry = SpaceEntry{
		Name:        req.Name,
		Kind:        req.Kind,
		Path:        stored,
		Description: req.Description,
		Feature:     req.Feature,
		AddedAt:     time.Now().UTC(),
	}
	f.Version = spacesVersion
	f.Spaces = append(f.Spaces, entry)
	sortSpaces(f)

	if saveErr := saveSpaces(anchor.Root, f); saveErr != nil {
		return SpaceEntry{}, false, saveErr
	}
	return entry, true, nil
}

// SpaceListOptions selects which entries `tws space list` reports.
type SpaceListOptions struct {
	Feature string
	All     bool
	Kind    string
	Cwd     string
}

// SpaceListResult carries the matching views plus the metadata the CLI needs
// to render an honest header and empty state. Every field comes from the one
// read SpaceList already performed; nothing here rereads spaces.yaml.
type SpaceListResult struct {
	// Views are the entries matching the active filters.
	Views []SpaceView
	// Total is the number of registered entries before any filtering.
	Total int
	// ScopeFeature is the feature the active scope filters on, empty when the
	// listing is not feature-scoped.
	ScopeFeature string
}

// Scope renders the active scope for the human header: the auto-detected or
// explicitly requested feature, otherwise "all".
func (r SpaceListResult) Scope() string {
	if r.ScopeFeature == "" {
		return "all"
	}
	return "feature " + r.ScopeFeature
}

// SpaceList returns the views of the entries matching opts, plus the registry
// totals and the active scope.
func SpaceList(anchor SpacesAnchor, opts SpaceListOptions) (SpaceListResult, error) {
	if opts.All && opts.Feature != "" {
		return SpaceListResult{}, fmt.Errorf("--all and --feature are mutually exclusive")
	}
	if opts.Kind != "" {
		if err := validateSpaceKind(opts.Kind); err != nil {
			return SpaceListResult{}, err
		}
	}

	f, err := readSpaces(anchor.Root)
	if err != nil {
		return SpaceListResult{}, err
	}
	owners := ownersFrom(anchor.Root, f)

	scopeFeature := ""
	switch {
	case opts.All:
		// every entry
	case opts.Feature != "":
		if err := validateFeatureName(opts.Feature); err != nil {
			return SpaceListResult{}, err
		}
		// A registered space directory is never a feature, not even as a
		// read-only filter.
		if err := guardFeatureNameIn(owners, anchor.Root, opts.Feature); err != nil {
			return SpaceListResult{}, err
		}
		if !anchorFeatureExists(anchor, opts.Feature) {
			return SpaceListResult{}, fmt.Errorf("feature %q not found in this workspace", opts.Feature)
		}
		scopeFeature = opts.Feature
	default:
		scopeFeature = detectAnchorFeature(anchor, owners, opts.Cwd)
	}

	result := SpaceListResult{Views: []SpaceView{}, ScopeFeature: scopeFeature}
	if f == nil {
		return result, nil
	}
	result.Total = len(f.Spaces)
	for _, e := range f.Spaces {
		if !opts.All && scopeFeature != "" && e.Feature != "" && e.Feature != scopeFeature {
			continue
		}
		if opts.Kind != "" && e.Kind != opts.Kind {
			continue
		}
		result.Views = append(result.Views, spaceViewOf(anchor, e))
	}
	return result, nil
}

// SpaceScopeSelector states which scope a name lookup is restricted to.
type SpaceScopeSelector string

const (
	// SpaceScopeSelectorAny resolves a bare name across every scope.
	SpaceScopeSelectorAny SpaceScopeSelector = ""
	// SpaceScopeSelectorWorkspace restricts to workspace-wide entries.
	SpaceScopeSelectorWorkspace SpaceScopeSelector = "workspace"
	// SpaceScopeSelectorFeature restricts to one feature's entries.
	SpaceScopeSelectorFeature SpaceScopeSelector = "feature"
)

// SpaceSelector names one registry entry and, optionally, the exact scope it
// must live in. It is the single selector both `space show` and `space remove`
// thread into the core.
type SpaceSelector struct {
	Name    string
	Scope   SpaceScopeSelector
	Feature string
}

// NewSpaceSelector builds a selector from the `--feature` / `--workspace`
// flags, which are mutually exclusive.
func NewSpaceSelector(name, feature string, workspace bool) (SpaceSelector, error) {
	if feature != "" && workspace {
		return SpaceSelector{}, fmt.Errorf("--feature and --workspace are mutually exclusive")
	}
	switch {
	case workspace:
		return SpaceSelector{Name: name, Scope: SpaceScopeSelectorWorkspace}, nil
	case feature != "":
		return SpaceSelector{Name: name, Scope: SpaceScopeSelectorFeature, Feature: feature}, nil
	default:
		return SpaceSelector{Name: name}, nil
	}
}

func (s SpaceSelector) validate() error {
	if s.Name == "" {
		return fmt.Errorf("space name cannot be empty")
	}
	if s.Scope == SpaceScopeSelectorFeature && s.Feature == "" {
		return fmt.Errorf("feature scope requires a feature name")
	}
	if s.Scope != SpaceScopeSelectorFeature && s.Feature != "" {
		return fmt.Errorf("feature name requires the feature scope")
	}
	return nil
}

// matches reports whether e is in the scope this selector restricts to.
func (s SpaceSelector) matches(e SpaceEntry) bool {
	if e.Name != s.Name {
		return false
	}
	switch s.Scope {
	case SpaceScopeSelectorWorkspace:
		return e.Feature == ""
	case SpaceScopeSelectorFeature:
		return e.Feature == s.Feature
	default:
		return true
	}
}

func (s SpaceSelector) notFoundError() error {
	switch s.Scope {
	case SpaceScopeSelectorWorkspace:
		return fmt.Errorf("no space named %q in the workspace scope", s.Name)
	case SpaceScopeSelectorFeature:
		return fmt.Errorf("no space named %q in feature %q", s.Name, s.Feature)
	default:
		return fmt.Errorf("no space named %q", s.Name)
	}
}

// SpaceShow resolves one entry with the given selector.
func SpaceShow(anchor SpacesAnchor, sel SpaceSelector) (SpaceView, error) {
	if err := sel.validate(); err != nil {
		return SpaceView{}, err
	}
	f, err := readSpaces(anchor.Root)
	if err != nil {
		return SpaceView{}, err
	}
	entry, err := selectSpaceEntry(f, sel)
	if err != nil {
		return SpaceView{}, err
	}
	return spaceViewOf(anchor, entry), nil
}

// selectSpaceEntry implements the shared show/remove selector rules: a bare
// name resolves a unique match across scopes, an explicit scope restricts the
// candidate set, and an ambiguous bare name names both disambiguating flags.
func selectSpaceEntry(f *SpacesFile, sel SpaceSelector) (SpaceEntry, error) {
	var matches []SpaceEntry
	if f != nil {
		for _, e := range f.Spaces {
			if sel.matches(e) {
				matches = append(matches, e)
			}
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return SpaceEntry{}, sel.notFoundError()
	default:
		return SpaceEntry{}, fmt.Errorf("space %q is ambiguous: %s; disambiguate with --feature <name> or --workspace",
			sel.Name, strings.Join(spaceScopeLabels(matches), ", "))
	}
}

func spaceScopeLabels(entries []SpaceEntry) []string {
	labels := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Feature == "" {
			labels = append(labels, "workspace")
			continue
		}
		labels = append(labels, fmt.Sprintf("feature %q", e.Feature))
	}
	return labels
}

// SpaceRemove deletes one registry entry. It never touches the target
// directory, and it creates nothing when no registry exists.
func SpaceRemove(anchor SpacesAnchor, sel SpaceSelector) (err error) {
	if err := sel.validate(); err != nil {
		return err
	}

	// Lstat-before-lock: acquireSpacesLock would create the root and the lock
	// file, so an absent registry must never reach it.
	if _, statErr := os.Lstat(spacesPath(anchor.Root)); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return sel.notFoundError()
		}
		return fmt.Errorf("reading spaces file %s: %w", spacesPath(anchor.Root), statErr)
	}

	lock, err := acquireSpacesLock(anchor.Root)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, lock.Release())
	}()

	f, readErr := readSpaces(anchor.Root)
	if readErr != nil {
		return readErr
	}
	if f == nil {
		return sel.notFoundError()
	}

	entry, selErr := selectSpaceEntry(f, sel)
	if selErr != nil {
		return selErr
	}

	remaining := make([]SpaceEntry, 0, len(f.Spaces))
	for _, e := range f.Spaces {
		if e.Name == entry.Name && e.Feature == entry.Feature {
			continue
		}
		remaining = append(remaining, e)
	}
	f.Version = spacesVersion
	f.Spaces = remaining
	return saveSpaces(anchor.Root, f)
}

// ---------------------------------------------------------------------------
// Lifecycle transactions
// ---------------------------------------------------------------------------

// SpacesDeleteTx serializes `tws delete` against `tws space add`. It never
// rewrites the registry.
type SpacesDeleteTx struct {
	lock *SpacesLock
}

// Release releases the delete lock. It never writes.
func (t *SpacesDeleteTx) Release() error {
	if t == nil || t.lock == nil {
		return nil
	}
	lock := t.lock
	t.lock = nil
	return lock.Release()
}

// BeginSpacesFeatureDelete validates that deleting featurePath would not touch
// a registered space and returns holding the spaces lock. With no
// <root>/spaces.yaml it is a true no-op: no lock, no file, no directory.
func BeginSpacesFeatureDelete(root, feature, featurePath string) (*SpacesDeleteTx, error) {
	lock, f, err := beginSpacesTx(root)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return &SpacesDeleteTx{}, nil
	}

	owners := ownersFrom(root, f)
	if guardErr := guardFeatureNameIn(owners, root, feature); guardErr != nil {
		return nil, errors.Join(guardErr, lock.Release())
	}

	anchor := SpacesAnchor{Root: root, Canon: canonicalize(root)}
	var blocking []SpaceEntry
	for _, e := range f.Spaces {
		if pathContains(featurePath, resolveSpacePath(anchor, e.Path)) {
			blocking = append(blocking, e)
		}
	}
	if len(blocking) > 0 {
		return nil, errors.Join(
			fmt.Errorf("cannot delete feature %q: %s live%s inside %s (%s); run %s, or move the directories out of the feature, then retry",
				feature, pluralSpaces(len(blocking)), pluralVerbSuffix(len(blocking)), featurePath,
				describeBlockingSpaces(blocking), describeSpaceRemoveCommands(blocking)),
			lock.Release())
	}

	return &SpacesDeleteTx{lock: lock}, nil
}

// SpacesRenameTx stages the registry rewrite implied by a feature rename and
// holds the spaces lock across the on-disk rename.
type SpacesRenameTx struct {
	root    string
	lock    *SpacesLock
	file    *SpacesFile
	changed bool
}

// Commit writes the staged rewrite, but only when an entry actually changed.
func (t *SpacesRenameTx) Commit() error {
	if t == nil || t.lock == nil || !t.changed {
		return nil
	}
	return saveSpaces(t.root, t.file)
}

// Release releases the rename lock.
func (t *SpacesRenameTx) Release() error {
	if t == nil || t.lock == nil {
		return nil
	}
	lock := t.lock
	t.lock = nil
	return lock.Release()
}

// BeginSpacesFeatureRename validates both feature names against the registry,
// refuses entries pinned inside the renamed directory, and stages the
// relative-path/feature-scope rewrite. With no <root>/spaces.yaml it is a true
// no-op: no lock, no file, no directory.
func BeginSpacesFeatureRename(root, oldName, newName, oldPath, newPath string) (*SpacesRenameTx, error) {
	lock, f, err := beginSpacesTx(root)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return &SpacesRenameTx{}, nil
	}

	owners := ownersFrom(root, f)
	// The source name is checked first: it is the destructive side.
	if guardErr := guardFeatureNameIn(owners, root, oldName); guardErr != nil {
		return nil, errors.Join(guardErr, lock.Release())
	}
	if guardErr := guardFeatureNameIn(owners, root, newName); guardErr != nil {
		return nil, errors.Join(guardErr, lock.Release())
	}

	// An entry is pinned when it lives inside the renamed directory but the
	// relative rewrite below would not follow it there: absolute entries, and
	// relative entries whose first segment is spelled differently from the
	// feature being renamed.
	var pinned []SpaceEntry
	for _, e := range f.Spaces {
		if !filepath.IsAbs(e.Path) {
			if _, rewritable := rewriteSpaceRelPath(e.Path, oldName, newName); rewritable {
				continue
			}
		}
		if pathContains(oldPath, resolveSpacePathIn(root, e.Path)) {
			pinned = append(pinned, e)
		}
	}
	if len(pinned) > 0 {
		return nil, errors.Join(
			fmt.Errorf("cannot rename feature %q: %s %s pinned inside %s (%s); run %s, or re-add %s with a workspace-relative path under the new name, then retry",
				oldName, pluralSpaces(len(pinned)), pluralIsAre(len(pinned)), oldPath,
				describeBlockingSpaces(pinned), describeSpaceRemoveCommands(pinned),
				pluralItThem(len(pinned))),
			lock.Release())
	}

	staged := &SpacesFile{Version: spacesVersion, Spaces: make([]SpaceEntry, len(f.Spaces))}
	copy(staged.Spaces, f.Spaces)

	now := time.Now().UTC()
	changed := false
	for i := range staged.Spaces {
		e := &staged.Spaces[i]
		entryChanged := false

		if e.Feature == oldName {
			e.Feature = newName
			entryChanged = true
		}
		if !filepath.IsAbs(e.Path) {
			if rewritten, ok := rewriteSpaceRelPath(e.Path, oldName, newName); ok {
				e.Path = rewritten
				entryChanged = true
			}
		}
		if entryChanged {
			stamp := now
			e.UpdatedAt = &stamp
			changed = true
		}
	}
	if changed {
		sortSpaces(staged)
	}

	return &SpacesRenameTx{root: root, lock: lock, file: staged, changed: changed}, nil
}

// SpacesLayoutMigrationTx serializes a checkout layout migration against
// `tws space add`. Like the delete transaction it never rewrites the registry:
// this version of the feature refuses a migration that would move a registered
// target instead of rewriting the entry that points at it.
type SpacesLayoutMigrationTx struct {
	lock *SpacesLock
}

// Release releases the migration lock. It never writes.
func (t *SpacesLayoutMigrationTx) Release() error {
	if t == nil || t.lock == nil {
		return nil
	}
	lock := t.lock
	t.lock = nil
	return lock.Release()
}

// LayoutMigrationTarget is one legacy feature directory that a layout
// migration is about to move to <root>/features/<Feature>.
type LayoutMigrationTarget struct {
	Feature string // the legacy feature name being migrated
	Path    string // the legacy <root>/<feature> directory that will be renamed
}

// BeginSpacesLayoutMigration validates a whole layout-migration batch against
// the registry and returns holding the spaces lock, so the caller may perform
// every os.Rename (and any rollback) while no `tws space add` can interleave.
//
// With no <root>/spaces.yaml it is a true no-op: no lock, no file, no
// directory, and the caller's behaviour is byte-for-byte what it was before
// spaces existed.
//
// Under the lock, and against the re-read file only:
//
//  1. top-level name ownership is rejected first, for every target in the
//     batch, with the standard §7.3 conflict message;
//  2. then inclusive containment: any registered target of either scope and
//     either stored form that resolves to, or inside, a legacy feature
//     directory blocks the migration. Containment uses pathContains, so an
//     absolute in-root, symlinked, or case-variant spelling is caught too.
//
// A single blocked target blocks the whole batch, and nothing is moved.
func BeginSpacesLayoutMigration(root string, targets []LayoutMigrationTarget) (*SpacesLayoutMigrationTx, error) {
	lock, f, err := beginSpacesTx(root)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return &SpacesLayoutMigrationTx{}, nil
	}

	owners := ownersFrom(root, f)
	for _, target := range targets {
		if guardErr := guardFeatureNameIn(owners, root, target.Feature); guardErr != nil {
			return nil, errors.Join(guardErr, lock.Release())
		}
	}

	blocked, blocking := layoutMigrationBlockers(root, f, targets)
	if len(blocking) > 0 {
		return nil, errors.Join(errLayoutMigrationBlocked(blocked, blocking), lock.Release())
	}

	return &SpacesLayoutMigrationTx{lock: lock}, nil
}

// layoutMigrationBlockers reports the legacy targets that still contain a
// registered space, and the blocking entries themselves, de-duplicated by
// scope and name so one entry inside two targets is reported once.
func layoutMigrationBlockers(root string, f *SpacesFile, targets []LayoutMigrationTarget) ([]LayoutMigrationTarget, []SpaceEntry) {
	type entryKey struct{ feature, name string }
	seen := make(map[entryKey]bool)

	var blocked []LayoutMigrationTarget
	var blocking []SpaceEntry
	for _, target := range targets {
		hit := false
		for _, e := range f.Spaces {
			if !pathContains(target.Path, resolveSpacePathIn(root, e.Path)) {
				continue
			}
			hit = true
			key := entryKey{feature: e.Feature, name: e.Name}
			if seen[key] {
				continue
			}
			seen[key] = true
			blocking = append(blocking, e)
		}
		if hit {
			blocked = append(blocked, target)
		}
	}
	return blocked, blocking
}

// errLayoutMigrationBlocked renders the migration refusal. It names every
// blocking entry with its scope, spells out the scope-qualified removal
// command for each, and states that the links can be re-added afterwards —
// migration moves directories, so it never rewrites a registered path itself.
func errLayoutMigrationBlocked(blocked []LayoutMigrationTarget, blocking []SpaceEntry) error {
	names := make([]string, 0, len(blocked))
	dirs := make([]string, 0, len(blocked))
	for _, target := range blocked {
		names = append(names, fmt.Sprintf("%q", target.Feature))
		dirs = append(dirs, target.Path)
	}
	return fmt.Errorf(
		"cannot migrate legacy %s %s to the new checkout layout: %s live%s inside %s (%s); run %s, then retry; %s can be re-added with 'tws space add' once the migration is done",
		pluralFeatureWord(len(blocked)), strings.Join(names, ", "),
		pluralSpaces(len(blocking)), pluralVerbSuffix(len(blocking)),
		strings.Join(dirs, ", "), describeBlockingSpaces(blocking),
		describeSpaceRemoveCommands(blocking), pluralThatSpace(len(blocking)))
}

// rewriteSpaceRelPath re-prefixes a workspace-relative stored path whose first
// segment is oldName (external / checkout legacy) or whose first two segments
// are features/<oldName> (checkout new layout).
func rewriteSpaceRelPath(stored, oldName, newName string) (string, bool) {
	segs := strings.Split(filepath.ToSlash(filepath.Clean(stored)), "/")
	if len(segs) == 0 {
		return stored, false
	}
	if segs[0] == "features" && len(segs) >= 2 {
		if segs[1] != oldName {
			return stored, false
		}
		segs[1] = newName
		return filepath.FromSlash(strings.Join(segs, "/")), true
	}
	if segs[0] != oldName {
		return stored, false
	}
	segs[0] = newName
	return filepath.FromSlash(strings.Join(segs, "/")), true
}

// beginSpacesTx performs the shared Lstat-before-lock probe, acquires the lock
// when a registry exists, and takes the authoritative read under it.
// A nil lock with a nil error means "no registry; true no-op".
func beginSpacesTx(root string) (*SpacesLock, *SpacesFile, error) {
	if root == "" {
		return nil, nil, nil
	}
	if _, err := os.Lstat(spacesPath(root)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("cannot verify registered spaces in %s: %w", spacesPath(root), err)
	}

	lock, err := acquireSpacesLock(root)
	if err != nil {
		return nil, nil, err
	}

	f, readErr := readSpaces(root)
	if readErr != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("cannot verify registered spaces in %s: %w", spacesPath(root), readErr),
			lock.Release())
	}
	if f == nil {
		// The file vanished between the advisory probe and the locked read.
		if relErr := lock.Release(); relErr != nil {
			return nil, nil, relErr
		}
		return nil, nil, nil
	}
	return lock, f, nil
}

func pluralSpaces(n int) string {
	if n == 1 {
		return "1 registered space"
	}
	return fmt.Sprintf("%d registered spaces", n)
}

func pluralVerbSuffix(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}

func pluralIsAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func pluralItThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func pluralThatSpace(n int) string {
	if n == 1 {
		return "that space"
	}
	return "those spaces"
}

func pluralFeatureWord(n int) string {
	if n == 1 {
		return "feature"
	}
	return "features"
}

// describeBlockingSpaces renders blocking entries sorted, workspace-wide
// first, each with its scope.
func describeBlockingSpaces(entries []SpaceEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range sortedBlockingSpaces(entries) {
		if e.Feature == "" {
			parts = append(parts, fmt.Sprintf("%s (workspace)", e.Name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (feature %s)", e.Name, e.Feature))
	}
	return strings.Join(parts, ", ")
}

// describeSpaceRemoveCommands renders the exact scope-qualified command that
// clears each blocker, so the refusal is directly actionable.
func describeSpaceRemoveCommands(entries []SpaceEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range sortedBlockingSpaces(entries) {
		parts = append(parts, fmt.Sprintf("'%s'", spaceRemoveCommand(e)))
	}
	return strings.Join(parts, " and ")
}

// spaceRemoveCommand renders the removal command for one entry, always naming
// the scope so the command is unambiguous even when the name is shared.
func spaceRemoveCommand(e SpaceEntry) string {
	if e.Feature == "" {
		return fmt.Sprintf("tws space remove %s --workspace", e.Name)
	}
	return fmt.Sprintf("tws space remove %s --feature %s", e.Name, e.Feature)
}

func sortedBlockingSpaces(entries []SpaceEntry) []SpaceEntry {
	sorted := make([]SpaceEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Feature != sorted[j].Feature {
			return sorted[i].Feature < sorted[j].Feature
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}
