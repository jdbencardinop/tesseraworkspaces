package internal

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Registry schema version.
const registryVersion = 1

type RegistryKind string

const (
	RegistryKindRepo              RegistryKind = "repo"
	RegistryKindExternalWorkspace RegistryKind = "external-workspace"
	RegistryKindCheckoutWorkspace RegistryKind = "checkout-workspace"
)

// validRegistryKind reports whether kind is a recognised registry kind.
func validRegistryKind(kind RegistryKind) bool {
	switch kind {
	case RegistryKindRepo, RegistryKindExternalWorkspace, RegistryKindCheckoutWorkspace:
		return true
	default:
		return false
	}
}

// RegistryFile is the on-disk structure for the workspace registry.
type RegistryFile struct {
	Version int             `yaml:"version" json:"version"`
	Entries []RegistryEntry `yaml:"entries" json:"entries"`
}

// RegistryEntry represents a single workspace in the registry.
type RegistryEntry struct {
	ID          string       `yaml:"id" json:"id"`
	Path        string       `yaml:"path" json:"path"`
	Aliases     []string     `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Kind        RegistryKind `yaml:"kind" json:"kind"`
	GitIdentity string       `yaml:"git_identity,omitempty" json:"git_identity,omitempty"`
	MarkerID    string       `yaml:"marker_id,omitempty" json:"marker_id,omitempty"`
	AddedAt     time.Time    `yaml:"added_at" json:"added_at"`
	UpdatedAt   time.Time    `yaml:"updated_at" json:"updated_at"`
}

// CheckStatus represents the health state of a registry entry.
type CheckStatus string

const (
	StatusOK         CheckStatus = "ok"
	StatusMissing    CheckStatus = "missing"
	StatusMismatched CheckStatus = "mismatched"
	StatusInvalid    CheckStatus = "invalid"
)

// CheckResult is the result of checking a registry entry.
type CheckResult struct {
	Entry  *RegistryEntry `json:"entry"`
	Status CheckStatus    `json:"status"`
	Detail string         `json:"detail,omitempty"`
}

// registryDir returns the directory for registry data.
// Uses XDG_DATA_HOME if set, else ~/.local/share/tesseraworkspaces.
func registryDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "tws")
}

// registryPath returns the file path for the registry YAML.
func registryPath() string {
	return filepath.Join(registryDir(), "registry.yaml")
}

// lockPath returns the file path for the registry lock file.
func lockPath() string {
	return filepath.Join(registryDir(), "registry.lock")
}

// RegistryLock holds an advisory POSIX file lock on the registry.
//
// Platform boundary: this implementation uses POSIX fcntl-style advisory
// locking via syscall.Flock. It is supported on macOS and Linux only.
// Windows is NOT supported; a build-tag gated stub would be needed for
// cross-platform use.
type RegistryLock struct {
	f *os.File
}

// AcquireRegistryLock obtains an exclusive advisory lock on the registry.
// Callers MUST call Release when done, and MUST check its returned error.
func AcquireRegistryLock() (*RegistryLock, error) {
	dir := registryDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating registry dir: %w", err)
	}

	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	if err := flockExclusive(f); err != nil {
		_ = f.Close() // best-effort; lock was never acquired
		return nil, fmt.Errorf("acquiring lock: %w", err)
	}

	return &RegistryLock{f: f}, nil
}

// Release releases the registry lock. The returned error MUST be checked
// by callers; combine it with any primary operation error using errors.Join
// so that neither is lost.
func (l *RegistryLock) Release() error {
	unlockErr := flockUnlock(l.f)
	closeErr := l.f.Close()
	return errors.Join(unlockErr, closeErr)
}

// readRegistry loads the registry file or returns nil if absent.
// Absence does NOT create any files on disk. The schema is validated before
// the result is handed to any caller, so a malformed or future-version
// registry can never be silently rewritten (and thereby truncated) by a
// mutator.
func readRegistry() (*RegistryFile, error) {
	data, err := os.ReadFile(registryPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	return decodeRegistry(data)
}

// decodeRegistry parses and validates registry bytes.
//
// Version handling happens first, using a permissive probe decode, so that a
// newer schema is reported as a version error rather than as an unknown-field
// error. Only a supported version is then decoded strictly, which guarantees
// unknown fields are never silently dropped on the next write.
func decodeRegistry(data []byte) (*RegistryFile, error) {
	var probe struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parsing registry %s: %w", registryPath(), err)
	}
	if probe.Version <= 0 {
		return nil, fmt.Errorf("registry %s is malformed: missing or invalid schema version", registryPath())
	}
	if probe.Version > registryVersion {
		return nil, fmt.Errorf("registry %s uses schema version %d but this tws supports version %d; upgrade tws instead of modifying the registry",
			registryPath(), probe.Version, registryVersion)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var reg RegistryFile
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("parsing registry %s: %w", registryPath(), err)
	}
	if err := validateRegistry(&reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

// validateRegistry enforces the core invariants that every reader and mutator
// depends on: unique non-empty IDs, unique absolute canonical paths, known
// kinds, well-formed aliases, and globally unambiguous selectors.
func validateRegistry(reg *RegistryFile) error {
	if reg.Version <= 0 || reg.Version > registryVersion {
		return fmt.Errorf("registry %s has unsupported schema version %d", registryPath(), reg.Version)
	}

	ids := make(map[string]bool, len(reg.Entries))
	paths := make(map[string]bool, len(reg.Entries))
	markers := make(map[string]string, len(reg.Entries))
	aliases := make(map[string]string, len(reg.Entries))

	for i := range reg.Entries {
		e := &reg.Entries[i]
		if e.ID == "" {
			return fmt.Errorf("registry %s is invalid: entry %d has an empty id", registryPath(), i)
		}
		if !aliasRegexp.MatchString(e.ID) {
			return fmt.Errorf("registry %s is invalid: entry id %q is malformed", registryPath(), e.ID)
		}
		if ids[e.ID] {
			return fmt.Errorf("registry %s is invalid: duplicate entry id %q", registryPath(), e.ID)
		}
		ids[e.ID] = true

		if e.Path == "" || !filepath.IsAbs(e.Path) || filepath.Clean(e.Path) != e.Path {
			return fmt.Errorf("registry %s is invalid: entry %s has a non-canonical path %q", registryPath(), e.ID, e.Path)
		}
		if paths[e.Path] {
			return fmt.Errorf("registry %s is invalid: duplicate entry path %q", registryPath(), e.Path)
		}
		paths[e.Path] = true

		if !validRegistryKind(e.Kind) {
			return fmt.Errorf("registry %s is invalid: entry %s has unknown kind %q", registryPath(), e.ID, e.Kind)
		}
		if e.MarkerID != "" {
			if !markerIDRegexp.MatchString(e.MarkerID) {
				return fmt.Errorf("registry %s is invalid: entry %s has malformed marker identity", registryPath(), e.ID)
			}
			if owner, seen := markers[e.MarkerID]; seen {
				return fmt.Errorf("registry %s is invalid: entries %s and %s share marker identity", registryPath(), owner, e.ID)
			}
			markers[e.MarkerID] = e.ID
		}

		for _, alias := range e.Aliases {
			if err := ValidateAlias(alias); err != nil {
				return fmt.Errorf("registry %s is invalid: entry %s: %w", registryPath(), e.ID, err)
			}
			if owner, seen := aliases[alias]; seen {
				return fmt.Errorf("registry %s is invalid: alias %q is used by both %s and %s", registryPath(), alias, owner, e.ID)
			}
			aliases[alias] = e.ID
		}
	}

	for alias, owner := range aliases {
		if ids[alias] {
			return fmt.Errorf("registry %s is invalid: alias %q on entry %s shadows an entry id", registryPath(), alias, owner)
		}
		if paths[alias] {
			return fmt.Errorf("registry %s is invalid: alias %q on entry %s shadows a registered path", registryPath(), alias, owner)
		}
	}
	return nil
}

// saveRegistry atomically writes the registry to disk.
// Write, fsync, and rename errors are never masked. Cleanup failures
// (temp close after error, temp remove) are joined so both the primary
// error and cleanup error are visible.
func saveRegistry(reg *RegistryFile) error {
	dir := registryDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating registry dir: %w", err)
	}

	data, err := yaml.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshaling registry: %w", err)
	}

	f, err := os.CreateTemp(dir, ".registry-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp registry: %w", err)
	}
	tmp := f.Name()

	if _, err := f.Write(data); err != nil {
		closeErr := f.Close()
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("writing registry: %w", err), closeErr, removeErr)
	}

	if err := f.Sync(); err != nil {
		closeErr := f.Close()
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("syncing registry: %w", err), closeErr, removeErr)
	}

	if err := f.Close(); err != nil {
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("closing temp registry: %w", err), removeErr)
	}

	if err := os.Chmod(tmp, 0600); err != nil {
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("setting registry permissions: %w", err), removeErr)
	}

	if err := os.Rename(tmp, registryPath()); err != nil {
		removeErr := os.Remove(tmp)
		return errors.Join(fmt.Errorf("renaming registry: %w", err), removeErr)
	}

	return nil
}

// generateID creates a short cryptographically-random hex ID.
func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate registry ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ComputeGitIdentity returns a stable fingerprint for a git repository.
// Uses the SHA-256 of the first commit hash + remote URL.
func ComputeGitIdentity(path string) string {
	var parts []string

	cmd := exec.Command("git", "-C", path, "rev-list", "--max-parents=0", "HEAD")
	if out, err := cmd.Output(); err == nil {
		parts = append(parts, strings.TrimSpace(string(out)))
	}

	cmd = exec.Command("git", "-C", path, "remote", "get-url", "origin")
	if out, err := cmd.Output(); err == nil {
		parts = append(parts, normalizeRemoteURL(strings.TrimSpace(string(out))))
	}

	if len(parts) == 0 {
		return ""
	}

	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:12])
}

func normalizeRemoteURL(raw string) string {
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			parsed.Host = strings.ToLower(parsed.Host)
			return parsed.String()
		}
	}
	// Normalize SCP-like SSH URLs (user@host:path) without retaining user info.
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	if cut := strings.IndexAny(raw, "?#"); cut >= 0 {
		raw = raw[:cut]
	}
	return raw
}

// registryTarget is the validated identity of a registry target on disk.
type registryTarget struct {
	Path        string
	Kind        RegistryKind
	GitIdentity string
	MarkerID    string
	MarkerDir   string
}

// inspectRegistryTarget validates path and derives its registry identity.
//
// Linked Git worktrees are normalized to the main repository root, matching
// `tws init --register`, so the same repository is never enrolled twice under
// different paths.
//
// When ensureMarker is true the caller is performing an explicit enrollment
// and a persistent marker identity is created for workspace kinds after the
// target has been validated. Read-only checks pass false. Repair also starts
// read-only and creates a replacement marker only after explicit identity-
// change consent.
func inspectRegistryTarget(path string, ensureMarker bool) (registryTarget, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return registryTarget{}, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absPath); resolveErr == nil {
		absPath = resolved
	}
	absPath = filepath.Clean(absPath)

	if info, statErr := os.Stat(filepath.Join(absPath, workspaceMarker)); statErr == nil && info.IsDir() {
		markerDir := ExternalMarkerDir(absPath)
		markerID, markerErr := resolveMarkerID(markerDir, ensureMarker)
		if markerErr != nil {
			return registryTarget{}, markerErr
		}
		return registryTarget{Path: absPath, Kind: RegistryKindExternalWorkspace, MarkerID: markerID, MarkerDir: markerDir}, nil
	}

	// Validate that this is a real (non-bare) working tree before normalizing.
	if _, repoErr := GitRepoRootIn(absPath); repoErr == nil {
		repoRoot, mainErr := MainRepoRootIn(absPath)
		if mainErr != nil {
			return registryTarget{}, fmt.Errorf("resolving main repository root for %s: %w", absPath, mainErr)
		}
		if resolved, resolveErr := filepath.EvalSymlinks(repoRoot); resolveErr == nil {
			repoRoot = resolved
		}
		repoRoot = filepath.Clean(repoRoot)

		cfg := LoadConfigFile(filepath.Join(repoRoot, ".tws", "config.yaml"))
		mode, ok := parseMode(cfg.WorkspaceMode)
		if cfg.WorkspaceMode != "" && !ok {
			return registryTarget{}, fmt.Errorf("invalid workspace_mode %q", cfg.WorkspaceMode)
		}

		markerDir, markerErr := GitMarkerDir(repoRoot)
		if markerErr != nil {
			return registryTarget{}, markerErr
		}
		target := registryTarget{
			Path:        repoRoot,
			Kind:        RegistryKindRepo,
			GitIdentity: ComputeGitIdentity(repoRoot),
			MarkerDir:   markerDir,
		}
		if mode == ModeCheckout && cfg.WorkspaceMode != "" {
			target.Kind = RegistryKindCheckoutWorkspace
		}
		markerID, markerErr := resolveMarkerID(target.MarkerDir, ensureMarker)
		if markerErr != nil {
			return registryTarget{}, markerErr
		}
		target.MarkerID = markerID
		return target, nil
	}
	return registryTarget{}, fmt.Errorf("%s is not a Git repository or tws external workspace", absPath)
}

// resolveMarkerID reads (or, for explicit enrollment, creates) the persistent
// marker identity of a tool-owned metadata directory.
func resolveMarkerID(markerDir string, ensure bool) (string, error) {
	if ensure {
		return EnsureWorkspaceMarkerID(markerDir)
	}
	return ReadWorkspaceMarkerID(markerDir)
}

// checkAliasCollision rejects an alias that would make selector resolution
// ambiguous: aliases must not duplicate another entry's alias, and must never
// shadow any entry ID or registered canonical path.
func checkAliasCollision(reg *RegistryFile, alias string, ownerID string) error {
	for i := range reg.Entries {
		e := &reg.Entries[i]
		if e.ID == alias {
			return fmt.Errorf("alias %q collides with the ID of registry entry %s", alias, e.ID)
		}
		if e.Path == alias {
			return fmt.Errorf("alias %q collides with the registered path of entry %s", alias, e.ID)
		}
		if e.ID != ownerID && containsAlias(e.Aliases, alias) {
			return fmt.Errorf("alias %q already in use by entry %s", alias, e.ID)
		}
	}
	return nil
}

// RegistryAdd adds or updates a workspace in the registry. If the workspace
// is already present (by path), it is idempotent: returns the existing entry
// with created=false. A proven identity mismatch at an already-registered
// path is rejected instead of silently overwriting the recorded identity.
// If an alias is provided and it already belongs to a different entry, or it
// would shadow an entry ID or registered path, an error is returned.
//
// Returns the entry pointer, whether it was newly created, and any error.
func RegistryAdd(path string, alias string) (*RegistryEntry, bool, error) {
	if alias != "" {
		if err := ValidateAlias(alias); err != nil {
			return nil, false, err
		}
	}

	target, err := inspectRegistryTarget(path, false)
	if err != nil {
		return nil, false, err
	}

	lock, err := AcquireRegistryLock()
	if err != nil {
		return nil, false, err
	}
	reg, err := readRegistry()
	if err != nil {
		releaseErr := lock.Release()
		return nil, false, errors.Join(err, releaseErr)
	}
	target, err = inspectRegistryTarget(path, false)
	if err != nil {
		return nil, false, errors.Join(err, lock.Release())
	}

	if reg == nil {
		reg = &RegistryFile{Version: registryVersion}
	}

	// Check for existing entry by path (idempotent add / duplicate alias).
	for i := range reg.Entries {
		if reg.Entries[i].Path != target.Path {
			continue
		}
		existing := &reg.Entries[i]
		if mismatch := identityMismatch(existing, target); mismatch != "" {
			releaseErr := lock.Release()
			return nil, false, errors.Join(
				fmt.Errorf("%s is already registered as %s with a different identity (%s); "+
					"rerun with 'tws registry repair %s %s --allow-identity-change' if this replacement is intentional",
					target.Path, existing.ID, mismatch, existing.ID, target.Path),
				releaseErr,
			)
		}

		if alias != "" && !containsAlias(existing.Aliases, alias) {
			if err := checkAliasCollision(reg, alias, existing.ID); err != nil {
				releaseErr := lock.Release()
				return nil, false, errors.Join(err, releaseErr)
			}
		}
		if existing.MarkerID == "" {
			target, err = inspectRegistryTarget(path, true)
			if err != nil {
				return nil, false, errors.Join(err, lock.Release())
			}
		}
		changed := refreshIdentityHints(existing, target)
		if alias != "" && !containsAlias(existing.Aliases, alias) {
			existing.Aliases = append(existing.Aliases, alias)
			changed = true
		}
		if changed {
			existing.UpdatedAt = time.Now().UTC()
			if err := saveRegistry(reg); err != nil {
				releaseErr := lock.Release()
				return nil, false, errors.Join(err, releaseErr)
			}
		}
		releaseErr := lock.Release()
		return existing, false, releaseErr
	}

	if target.MarkerID != "" {
		for i := range reg.Entries {
			if reg.Entries[i].MarkerID == target.MarkerID {
				return nil, false, errors.Join(
					fmt.Errorf("%s is already registered as %s at %s; run 'tws registry repair %s %s' if it moved",
						target.Path, reg.Entries[i].ID, reg.Entries[i].Path, reg.Entries[i].ID, target.Path),
					lock.Release(),
				)
			}
		}
	}

	// Check alias availability for the new entry.
	if alias != "" {
		if err := checkAliasCollision(reg, alias, ""); err != nil {
			releaseErr := lock.Release()
			return nil, false, errors.Join(err, releaseErr)
		}
	}

	target, err = inspectRegistryTarget(path, true)
	if err != nil {
		return nil, false, errors.Join(err, lock.Release())
	}
	for i := range reg.Entries {
		if target.MarkerID != "" && reg.Entries[i].MarkerID == target.MarkerID {
			return nil, false, errors.Join(
				fmt.Errorf("%s is already registered as %s at %s; run 'tws registry repair %s %s' if it moved",
					target.Path, reg.Entries[i].ID, reg.Entries[i].Path, reg.Entries[i].ID, target.Path),
				lock.Release(),
			)
		}
	}

	now := time.Now().UTC()
	id, err := generateID()
	if err != nil {
		return nil, false, errors.Join(err, lock.Release())
	}
	entry := RegistryEntry{
		ID:          id,
		Path:        target.Path,
		Kind:        target.Kind,
		GitIdentity: target.GitIdentity,
		MarkerID:    target.MarkerID,
		AddedAt:     now,
		UpdatedAt:   now,
	}

	if alias != "" {
		entry.Aliases = []string{alias}
	}

	reg.Entries = append(reg.Entries, entry)
	if err := saveRegistry(reg); err != nil {
		releaseErr := lock.Release()
		return nil, false, errors.Join(err, releaseErr)
	}

	releaseErr := lock.Release()
	return &entry, true, releaseErr
}

// identityMismatch reports a human-readable reason when the observed target
// provably contradicts the identity recorded for an entry. Persistent marker
// identity is authoritative; kind and Git fingerprint are refreshable metadata.
// The Git fingerprint is an identity fallback only for legacy entries without
// a marker.
func identityMismatch(entry *RegistryEntry, target registryTarget) string {
	if entry.MarkerID != "" {
		if target.MarkerID == "" {
			return "workspace marker file is missing"
		}
		if entry.MarkerID != target.MarkerID {
			return "workspace marker identity changed"
		}
		return ""
	}
	if entry.GitIdentity != "" && target.GitIdentity != "" && entry.GitIdentity != target.GitIdentity {
		return "git identity changed"
	}
	return ""
}

// refreshIdentityHints updates non-authoritative observed metadata and fills
// a previously unknown persistent marker. It never overwrites a marker.
func refreshIdentityHints(entry *RegistryEntry, target registryTarget) bool {
	changed := false
	if target.Kind != "" && entry.Kind != target.Kind {
		entry.Kind = target.Kind
		changed = true
	}
	if target.GitIdentity != "" && entry.GitIdentity != target.GitIdentity {
		entry.GitIdentity = target.GitIdentity
		changed = true
	}
	if entry.MarkerID == "" && target.MarkerID != "" {
		entry.MarkerID = target.MarkerID
		changed = true
	}
	return changed
}

// RegistryRemove removes a workspace from the registry by path, ID, or alias.
// It never deletes the target workspace directory or its marker files.
func RegistryRemove(selector string) error {
	lock, err := AcquireRegistryLock()
	if err != nil {
		return err
	}

	reg, err := readRegistry()
	if err != nil {
		return errors.Join(err, lock.Release())
	}
	if reg == nil {
		return errors.Join(fmt.Errorf("no entry matching %q", selector), lock.Release())
	}

	idx, err := resolveRegistryIndex(reg, selector)
	if err != nil {
		return errors.Join(err, lock.Release())
	}

	reg.Entries = append(reg.Entries[:idx], reg.Entries[idx+1:]...)
	saveErr := saveRegistry(reg)
	return errors.Join(saveErr, lock.Release())
}

// RegistryList returns all entries. Returns nil (not error) if registry absent.
func RegistryList() ([]RegistryEntry, error) {
	reg, err := readRegistry()
	if err != nil {
		return nil, err
	}
	if reg == nil {
		return nil, nil
	}
	return reg.Entries, nil
}

func resolveRegistryIndex(reg *RegistryFile, selector string) (int, error) {
	pathSelector := ""
	if abs, err := filepath.Abs(selector); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
			abs = resolved
		}
		pathSelector = filepath.Clean(abs)
	}
	var matches []int
	for i, entry := range reg.Entries {
		if entry.ID == selector || entry.Path == selector || (pathSelector != "" && entry.Path == pathSelector) || containsAlias(entry.Aliases, selector) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return -1, fmt.Errorf("no entry matching %q", selector)
	case 1:
		return matches[0], nil
	default:
		return -1, fmt.Errorf("ambiguous selector %q matches %d entries", selector, len(matches))
	}
}

// RegistryResolve finds an entry by selector (path, ID, or alias).
// Returns nil if not found. Returns error on ambiguity.
func RegistryResolve(selector string) (*RegistryEntry, error) {
	reg, err := readRegistry()
	if err != nil {
		return nil, err
	}
	if reg == nil {
		return nil, nil
	}

	index, err := resolveRegistryIndex(reg, selector)
	if err != nil {
		if strings.HasPrefix(err.Error(), "no entry matching") {
			return nil, nil
		}
		return nil, err
	}
	return &reg.Entries[index], nil
}

// RegistryCheck validates the health of a specific entry.
// It is strictly read-only: it never creates, repairs, or removes anything
// on disk, including workspace marker files.
func RegistryCheck(e *RegistryEntry) CheckResult {
	info, err := os.Stat(e.Path)
	if os.IsNotExist(err) {
		return CheckResult{Entry: e, Status: StatusMissing, Detail: "path does not exist"}
	}
	if err != nil || !info.IsDir() {
		return CheckResult{Entry: e, Status: StatusInvalid, Detail: "path is unreadable or not a directory"}
	}
	target, inspectErr := inspectRegistryTarget(e.Path, false)
	if inspectErr != nil {
		return CheckResult{Entry: e, Status: StatusInvalid, Detail: inspectErr.Error()}
	}
	if target.Path != e.Path {
		return CheckResult{Entry: e, Status: StatusMismatched, Detail: "canonical path identity changed"}
	}
	if e.MarkerID != "" {
		if target.MarkerID == "" {
			return CheckResult{Entry: e, Status: StatusMismatched, Detail: "workspace marker file is missing"}
		}
		if target.MarkerID != e.MarkerID {
			return CheckResult{Entry: e, Status: StatusMismatched, Detail: "workspace marker identity changed (target was replaced)"}
		}
	} else {
		if e.GitIdentity != "" && target.GitIdentity != e.GitIdentity {
			return CheckResult{Entry: e, Status: StatusMismatched, Detail: "git identity changed"}
		}
		if e.Kind != "" && target.Kind != e.Kind {
			return CheckResult{Entry: e, Status: StatusMismatched, Detail: fmt.Sprintf("kind changed from %s to %s", e.Kind, target.Kind)}
		}
	}
	return CheckResult{Entry: e, Status: StatusOK}
}

// RegistryRepair moves an entry to a validated new path while preserving its
// ID and aliases. A moved workspace that still carries its persistent marker
// identity and Git identity repairs without --allow-identity-change; a
// replaced target requires the explicit flag.
func RegistryRepair(selector, newPath string, allowIdentityChange bool) error {
	target, err := inspectRegistryTarget(newPath, false)
	if err != nil {
		return err
	}
	lock, err := AcquireRegistryLock()
	if err != nil {
		return err
	}
	reg, err := readRegistry()
	if err != nil {
		return errors.Join(err, lock.Release())
	}
	target, err = inspectRegistryTarget(newPath, false)
	if err != nil {
		return errors.Join(err, lock.Release())
	}
	if reg == nil {
		return errors.Join(fmt.Errorf("no entry matching %q", selector), lock.Release())
	}
	index, err := resolveRegistryIndex(reg, selector)
	if err != nil {
		return errors.Join(err, lock.Release())
	}
	for i, existing := range reg.Entries {
		if i != index && existing.Path == target.Path {
			return errors.Join(fmt.Errorf("path %s is already registered as %s", target.Path, existing.ID), lock.Release())
		}
		if i != index && target.MarkerID != "" && existing.MarkerID == target.MarkerID {
			return errors.Join(fmt.Errorf("target is already registered as %s at %s", existing.ID, existing.Path), lock.Release())
		}
	}
	entry := &reg.Entries[index]
	identityChanged := identityMismatch(entry, target) != ""
	if identityChanged && !allowIdentityChange {
		return errors.Join(fmt.Errorf("target identity differs; rerun with --allow-identity-change to repair intentionally"), lock.Release())
	}
	if identityChanged && allowIdentityChange && target.MarkerID == "" {
		target, err = inspectRegistryTarget(newPath, true)
		if err != nil {
			return errors.Join(err, lock.Release())
		}
	}
	entry.Path = target.Path
	entry.Kind = target.Kind
	entry.GitIdentity = target.GitIdentity
	entry.MarkerID = target.MarkerID
	entry.UpdatedAt = time.Now().UTC()
	return errors.Join(saveRegistry(reg), lock.Release())
}

// RegistryPrune removes entries whose paths no longer exist.
// It only removes registry metadata; targets and marker files are untouched.
// In non-TTY environments, requires force=true (enforced at CLI layer).
func RegistryPrune() ([]RegistryEntry, error) {
	lock, err := AcquireRegistryLock()
	if err != nil {
		return nil, err
	}

	reg, err := readRegistry()
	if err != nil {
		return nil, errors.Join(err, lock.Release())
	}
	if reg == nil {
		releaseErr := lock.Release()
		return nil, releaseErr
	}

	var removed []RegistryEntry
	var kept []RegistryEntry
	for _, e := range reg.Entries {
		if _, err := os.Stat(e.Path); os.IsNotExist(err) {
			removed = append(removed, e)
		} else {
			kept = append(kept, e)
		}
	}

	if len(removed) == 0 {
		return nil, lock.Release()
	}

	reg.Entries = kept
	saveErr := saveRegistry(reg)
	releaseErr := lock.Release()
	if err := errors.Join(saveErr, releaseErr); err != nil {
		return nil, err
	}
	return removed, nil
}

// RegistryAlias adds or removes an alias on an entry.
func RegistryAlias(selector string, alias string, remove bool) error {
	if err := ValidateAlias(alias); err != nil {
		return err
	}

	lock, err := AcquireRegistryLock()
	if err != nil {
		return err
	}

	reg, err := readRegistry()
	if err != nil {
		return errors.Join(err, lock.Release())
	}
	if reg == nil {
		return errors.Join(fmt.Errorf("no entry matching %q", selector), lock.Release())
	}

	idx, err := resolveRegistryIndex(reg, selector)
	if err != nil {
		return errors.Join(err, lock.Release())
	}

	if remove {
		reg.Entries[idx].Aliases = removeAlias(reg.Entries[idx].Aliases, alias)
	} else {
		if err := checkAliasCollision(reg, alias, reg.Entries[idx].ID); err != nil {
			return errors.Join(err, lock.Release())
		}
		if !containsAlias(reg.Entries[idx].Aliases, alias) {
			reg.Entries[idx].Aliases = append(reg.Entries[idx].Aliases, alias)
		}
	}
	reg.Entries[idx].UpdatedAt = time.Now().UTC()

	saveErr := saveRegistry(reg)
	return errors.Join(saveErr, lock.Release())
}

// ValidateAlias checks that an alias is well-formed.
var aliasRegexp = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func ValidateAlias(alias string) error {
	if alias == "" {
		return fmt.Errorf("alias cannot be empty")
	}
	if len(alias) > 64 {
		return fmt.Errorf("alias too long (max 64 chars)")
	}
	if !aliasRegexp.MatchString(alias) {
		return fmt.Errorf("alias %q contains invalid characters (allowed: a-z A-Z 0-9 . _ -)", alias)
	}
	return nil
}

// helpers

func containsAlias(aliases []string, alias string) bool {
	for _, a := range aliases {
		if a == alias {
			return true
		}
	}
	return false
}

func removeAlias(aliases []string, alias string) []string {
	result := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if a != alias {
			result = append(result, a)
		}
	}
	return result
}
