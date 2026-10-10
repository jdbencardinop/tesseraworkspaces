package internal

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	TemplateScopeGlobal = "global"
	TemplateScopeRepo   = "repo"
)

const (
	templateManifestVersion     = 1
	templateRegistryIdentityDir = ".registry-identity"
)

type TemplateReference struct {
	Scope string
	Name  string
}

type ResolvedTemplate struct {
	Reference TemplateReference
	Path      string
	OriginID  string
}

type NamedTemplateEntry struct {
	Name      string
	Scope     string
	Path      string
	Effective bool
}

type TemplateManifest struct {
	Version  int                     `yaml:"version"`
	Guidance map[string]string       `yaml:"guidance,omitempty"`
	Skills   []TemplateManifestSkill `yaml:"skills,omitempty"`
}

type TemplateManifestSkill struct {
	Name   string   `yaml:"name"`
	Roles  []string `yaml:"roles"`
	Agents []string `yaml:"agents,omitempty"`
}

type TemplatePackageFile struct {
	Path string
	Data []byte
	Mode fs.FileMode
}

type TemplatePackage struct {
	Root     string
	Feature  []TemplatePackageFile
	Inject   []TemplatePackageFile
	Metadata []TemplatePackageFile
	Manifest TemplateManifest

	rootIdentity managedObjectIdentity
}

var templateRegistryAfterLockHook func(root string) error
var templatePackageAfterOpenHook func(root string) error

func ValidateTemplateName(name string) error {
	if name == "" {
		return errors.New("template name cannot be empty")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return fmt.Errorf("template name %q is hidden or reserved", name)
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" ||
		strings.ContainsAny(name, `/\:`) || filepath.Clean(name) != name {
		return fmt.Errorf("template name %q must be one path component without ':'", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("template name %q contains a control character", name)
		}
	}
	return nil
}

func ParseTemplateReference(value string) (TemplateReference, error) {
	if value == "" {
		return TemplateReference{}, errors.New("template reference cannot be empty")
	}
	ref := TemplateReference{Name: value}
	if before, after, ok := strings.Cut(value, ":"); ok {
		switch before {
		case TemplateScopeRepo, TemplateScopeGlobal:
			ref.Scope = before
		default:
			return TemplateReference{}, fmt.Errorf("template reference %q has unknown scope %q", value, before)
		}
		ref.Name = after
		if strings.Contains(after, ":") {
			return TemplateReference{}, fmt.Errorf("template reference %q contains multiple scope separators", value)
		}
	}
	if err := ValidateTemplateName(ref.Name); err != nil {
		return TemplateReference{}, err
	}
	return ref, nil
}

func ValidateTemplateReferenceSyntax(value string) error {
	_, err := ParseTemplateReference(value)
	return err
}

func GlobalTemplateRegistryRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("resolving home directory: empty home")
	}
	return filepath.Join(home, ".config", "tws", "template-registry"), nil
}

func RepoTemplateRegistryRoot(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", errors.New("repository template registry requires a repository")
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolving repository root: %w", err)
	}
	return filepath.Join(abs, ".tws", ".template-registry"), nil
}

func templateRegistryRoot(repoRoot, scope string) (string, error) {
	switch scope {
	case TemplateScopeGlobal:
		return GlobalTemplateRegistryRoot()
	case TemplateScopeRepo:
		return RepoTemplateRegistryRoot(repoRoot)
	default:
		return "", fmt.Errorf("unknown template registry scope %q", scope)
	}
}

func ensureTemplateRegistryOriginID(root string) (string, error) {
	identityDir := filepath.Join(root, templateRegistryIdentityDir)
	info, err := os.Lstat(identityDir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(identityDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("creating template registry identity directory: %w", err)
		}
		info, err = os.Lstat(identityDir)
	}
	if err != nil {
		return "", fmt.Errorf("reading template registry identity directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("template registry identity directory %s is not a regular directory", identityDir)
	}
	identityPath := filepath.Join(identityDir, workspaceMarkerIDFile)
	if identityInfo, identityErr := os.Lstat(identityPath); identityErr == nil {
		if identityInfo.Mode()&os.ModeSymlink != 0 || !identityInfo.Mode().IsRegular() {
			return "", fmt.Errorf("template registry identity %s is not a regular file", identityPath)
		}
	} else if !errors.Is(identityErr, fs.ErrNotExist) {
		return "", fmt.Errorf("reading template registry identity %s: %w", identityPath, identityErr)
	}
	id, err := EnsureWorkspaceMarkerID(identityDir)
	if err != nil {
		return "", fmt.Errorf("ensuring template registry identity: %w", err)
	}
	return id, nil
}

func readTemplateRegistryOriginID(root string) (string, error) {
	identityDir := filepath.Join(root, templateRegistryIdentityDir)
	info, err := os.Lstat(identityDir)
	if err != nil {
		return "", fmt.Errorf("reading template registry identity directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("template registry identity directory %s is not a regular directory", identityDir)
	}
	identityPath := filepath.Join(identityDir, workspaceMarkerIDFile)
	identityInfo, err := os.Lstat(identityPath)
	if err != nil {
		return "", fmt.Errorf("reading template registry identity %s: %w", identityPath, err)
	}
	if identityInfo.Mode()&os.ModeSymlink != 0 || !identityInfo.Mode().IsRegular() {
		return "", fmt.Errorf("template registry identity %s is not a regular file", identityPath)
	}
	id, err := ReadWorkspaceMarkerID(identityDir)
	if err != nil {
		return "", fmt.Errorf("reading template registry identity: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("template registry identity %s is missing", identityPath)
	}
	return id, nil
}

func ResolveTemplateReference(repoRoot, value string) (ResolvedTemplate, error) {
	ref, err := ParseTemplateReference(value)
	if err != nil {
		return ResolvedTemplate{}, err
	}
	scopes := []string{ref.Scope}
	if ref.Scope == "" {
		scopes = []string{TemplateScopeRepo, TemplateScopeGlobal}
	}
	for _, scope := range scopes {
		root, rootErr := templateRegistryRoot(repoRoot, scope)
		if rootErr != nil {
			if ref.Scope != "" {
				return ResolvedTemplate{}, rootErr
			}
			continue
		}
		path := filepath.Join(root, ref.Name)
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return ResolvedTemplate{}, fmt.Errorf("reading %s template %q: %w", scope, ref.Name, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ResolvedTemplate{}, fmt.Errorf("%s template %q is not a regular directory", scope, ref.Name)
		}
		originID, err := readTemplateRegistryOriginID(root)
		if err != nil {
			return ResolvedTemplate{}, fmt.Errorf("reading %s template %q origin: %w", scope, ref.Name, err)
		}
		return ResolvedTemplate{
			Reference: TemplateReference{Scope: scope, Name: ref.Name},
			Path:      path,
			OriginID:  originID,
		}, nil
	}
	if ref.Scope != "" {
		return ResolvedTemplate{}, fmt.Errorf("%s template %q not found", ref.Scope, ref.Name)
	}
	return ResolvedTemplate{}, fmt.Errorf("template %q not found in the repository or global registry", ref.Name)
}

func ResolvePersistedTemplateReference(repoRoot string, ref TemplateReference) (ResolvedTemplate, error) {
	if ref.Scope != TemplateScopeRepo && ref.Scope != TemplateScopeGlobal {
		return ResolvedTemplate{}, fmt.Errorf("saved template %q has invalid scope %q", ref.Name, ref.Scope)
	}
	return ResolveTemplateReference(repoRoot, ref.Scope+":"+ref.Name)
}

func AddNamedTemplate(repoRoot, scope, name, source string) (retErr error) {
	if err := ValidateTemplateName(name); err != nil {
		return err
	}
	pkg, err := LoadTemplatePackage(source)
	if err != nil {
		return err
	}
	root, err := templateRegistryRoot(repoRoot, scope)
	if err != nil {
		return err
	}
	if err := ensureRegistryRoot(root, templateRegistryAnchor(repoRoot, scope)); err != nil {
		return err
	}
	initialIdentity, err := managedDirectoryIdentity(root)
	if err != nil {
		return err
	}
	if _, err := ensureTemplateRegistryOriginID(root); err != nil {
		return err
	}
	lock, err := acquireTemplateRegistryLock(root)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if lock.identity != initialIdentity {
		return fmt.Errorf("template registry root identity changed before lock acquisition: %s", root)
	}
	if templateRegistryAfterLockHook != nil {
		if err := templateRegistryAfterLockHook(root); err != nil {
			return err
		}
	}
	if err := lock.verifyPath(); err != nil {
		return err
	}
	if filepath.Clean(lock.root.Name()) != filepath.Clean(root) {
		return fmt.Errorf("template registry root identity changed at %s", root)
	}

	entries, err := listRegistryScopeRoot(lock.root, root, scope)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name, name) {
			return fmt.Errorf("%s template %q conflicts with existing entry %q", scope, name, entry.Name)
		}
	}

	target := filepath.Join(root, name)
	if _, err := lock.root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("%s template %q already exists", scope, name)
		}
		return fmt.Errorf("checking template destination %s: %w", target, err)
	}

	stage, err := createTemplateRegistryStage(lock.root, name)
	if err != nil {
		return fmt.Errorf("creating registry staging directory: %w", err)
	}
	defer func() {
		if cleanupErr := lock.root.RemoveAll(stage); cleanupErr != nil && !errors.Is(cleanupErr, fs.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("cleaning registry staging directory %s: %w", filepath.Join(root, stage), cleanupErr))
		}
	}()
	stageRoot, err := lock.root.OpenRoot(stage)
	if err != nil {
		return err
	}
	if err := copyTemplatePackageRoot(pkg, stageRoot); err != nil {
		_ = stageRoot.Close()
		return err
	}
	if err := stageRoot.Close(); err != nil {
		return err
	}
	if err := lock.verifyPath(); err != nil {
		return err
	}
	if filepath.Clean(lock.root.Name()) != filepath.Clean(root) {
		return fmt.Errorf("template registry root identity changed at %s", root)
	}
	if err := managedRenameNoReplace(lock.root, stage, name); err != nil {
		return fmt.Errorf("publishing %s template %q: %w", scope, name, err)
	}
	return nil
}

func RemoveNamedTemplate(repoRoot, scope, name string) (retErr error) {
	if err := ValidateTemplateName(name); err != nil {
		return err
	}
	root, err := templateRegistryRoot(repoRoot, scope)
	if err != nil {
		return err
	}
	if err := ensureRegistryRoot(root, templateRegistryAnchor(repoRoot, scope)); err != nil {
		return err
	}
	initialIdentity, err := managedDirectoryIdentity(root)
	if err != nil {
		return err
	}
	lock, err := acquireTemplateRegistryLock(root)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	if lock.identity != initialIdentity {
		return fmt.Errorf("template registry root identity changed before lock acquisition: %s", root)
	}
	if templateRegistryAfterLockHook != nil {
		if err := templateRegistryAfterLockHook(root); err != nil {
			return err
		}
	}
	if err := lock.verifyPath(); err != nil {
		return err
	}

	path := filepath.Join(root, name)
	info, err := lock.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s template %q not found", scope, name)
	}
	if err != nil {
		return fmt.Errorf("reading %s template %q: %w", scope, name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s template %q is not a regular directory", scope, name)
	}
	expected, err := managedIdentityFromInfo(info)
	if err != nil {
		return err
	}
	quarantine, err := createTemplateRegistryQuarantineName(lock.root, name)
	if err != nil {
		return err
	}
	if err := lock.verifyPath(); err != nil {
		return err
	}
	if err := managedRenameNoReplace(lock.root, name, quarantine); err != nil {
		return fmt.Errorf("preserving %s template %q before removal: %w", scope, name, err)
	}
	captured, err := lock.root.Lstat(quarantine)
	if err != nil {
		return fmt.Errorf("inspecting preserved template %s: %w", filepath.Join(root, quarantine), err)
	}
	capturedIdentity, err := managedIdentityFromInfo(captured)
	if err != nil {
		return err
	}
	if capturedIdentity != expected || !captured.IsDir() {
		restoreErr := managedRenameNoReplace(lock.root, quarantine, name)
		if restoreErr != nil {
			return fmt.Errorf("template changed during removal; preserved paths: %s and %s: %w",
				path, filepath.Join(root, quarantine), restoreErr)
		}
		return fmt.Errorf("template changed during removal and was restored at %s", path)
	}
	if err := lock.root.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("removing %s template %q: %w", scope, name, err)
	}
	return nil
}

func ListNamedTemplates(repoRoot string) ([]NamedTemplateEntry, error) {
	var entries []NamedTemplateEntry
	if repoRoot != "" {
		root, err := RepoTemplateRegistryRoot(repoRoot)
		if err != nil {
			return nil, err
		}
		repoEntries, err := listRegistryScope(root, TemplateScopeRepo)
		if err != nil {
			return nil, err
		}
		entries = append(entries, repoEntries...)
	}
	globalRoot, err := GlobalTemplateRegistryRoot()
	if err != nil {
		return nil, err
	}
	globalEntries, err := listRegistryScope(globalRoot, TemplateScopeGlobal)
	if err != nil {
		return nil, err
	}
	entries = append(entries, globalEntries...)

	repoNames := make(map[string]bool)
	for _, entry := range entries {
		if entry.Scope == TemplateScopeRepo {
			repoNames[strings.ToLower(entry.Name)] = true
		}
	}
	for i := range entries {
		entries[i].Effective = entries[i].Scope == TemplateScopeRepo ||
			!repoNames[strings.ToLower(entries[i].Name)]
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Scope == TemplateScopeRepo
	})
	return entries, nil
}

func ShowNamedTemplate(repoRoot, value string) (ResolvedTemplate, TemplatePackage, error) {
	resolved, err := ResolveTemplateReference(repoRoot, value)
	if err != nil {
		return ResolvedTemplate{}, TemplatePackage{}, err
	}
	pkg, err := LoadTemplatePackage(resolved.Path)
	if err != nil {
		return ResolvedTemplate{}, TemplatePackage{}, err
	}
	return resolved, pkg, nil
}

func listRegistryScope(root, scope string) ([]NamedTemplateEntry, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s template registry: %w", scope, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%s template registry %s is not a regular directory", scope, root)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("opening %s template registry: %w", scope, err)
	}
	defer rootHandle.Close() //nolint:errcheck
	return listRegistryScopeRoot(rootHandle, root, scope)
}

func listRegistryScopeRoot(rootHandle *os.Root, rootPath, scope string) ([]NamedTemplateEntry, error) {
	dirEntries, err := fs.ReadDir(rootHandle.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("reading %s template registry: %w", scope, err)
	}
	var entries []NamedTemplateEntry
	seen := make(map[string]string)
	for _, entry := range dirEntries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := ValidateTemplateName(entry.Name()); err != nil {
			return nil, fmt.Errorf("invalid %s template registry entry %q: %w", scope, entry.Name(), err)
		}
		folded := strings.ToLower(entry.Name())
		if prior := seen[folded]; prior != "" {
			return nil, fmt.Errorf("%s template registry contains aliasing entries %q and %q", scope, prior, entry.Name())
		}
		seen[folded] = entry.Name()
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%s template registry entry %q is not a regular directory", scope, entry.Name())
		}
		path := filepath.Join(rootPath, entry.Name())
		child, err := rootHandle.OpenRoot(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("opening %s template %q: %w", scope, entry.Name(), err)
		}
		identity, identityErr := managedRootIdentity(child)
		if identityErr != nil {
			_ = child.Close()
			return nil, identityErr
		}
		_, loadErr := loadTemplatePackageRoot(child, path, identity)
		closeErr := child.Close()
		if loadErr != nil {
			return nil, fmt.Errorf("invalid %s template %q: %w", scope, entry.Name(), loadErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		entries = append(entries, NamedTemplateEntry{Name: entry.Name(), Scope: scope, Path: path})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func LoadTemplatePackage(root string) (TemplatePackage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return TemplatePackage{}, fmt.Errorf("resolving template package %s: %w", root, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return TemplatePackage{}, fmt.Errorf("reading template package %s: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return TemplatePackage{}, fmt.Errorf("template package %s must be a regular directory", abs)
	}

	rootHandle, err := os.OpenRoot(abs)
	if err != nil {
		return TemplatePackage{}, fmt.Errorf("opening template package %s: %w", abs, err)
	}
	defer rootHandle.Close() //nolint:errcheck
	identity, err := managedRootIdentity(rootHandle)
	if err != nil {
		return TemplatePackage{}, fmt.Errorf("reading template package identity %s: %w", abs, err)
	}
	if templatePackageAfterOpenHook != nil {
		if err := templatePackageAfterOpenHook(abs); err != nil {
			return TemplatePackage{}, err
		}
	}
	return loadTemplatePackageRoot(rootHandle, abs, identity)
}

func loadTemplatePackageRoot(rootHandle *os.Root, displayPath string, identity managedObjectIdentity) (TemplatePackage, error) {
	pkg := TemplatePackage{Root: displayPath, rootIdentity: identity}
	seen := make(map[string]string)
	err := fs.WalkDir(rootHandle.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		rel := filepath.ToSlash(path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("template package contains symbolic link %s", rel)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("template package contains special file %s", rel)
		}
		folded := strings.ToLower(rel)
		if prior := seen[folded]; prior != "" {
			return fmt.Errorf("template package paths %q and %q alias under case-insensitive filesystems", prior, rel)
		}
		seen[folded] = rel

		top := strings.SplitN(rel, "/", 2)[0]
		switch top {
		case "feature", "inject":
		case "README.md", "template.yaml":
			if strings.Contains(rel, "/") {
				return fmt.Errorf("unsupported template package entry %s", rel)
			}
		default:
			return fmt.Errorf("unsupported template package entry %s", rel)
		}
		if info.IsDir() {
			if top == "README.md" || top == "template.yaml" {
				return fmt.Errorf("template metadata %s must be a regular file", rel)
			}
			return nil
		}
		switch top {
		case "feature", "inject":
			parts := strings.SplitN(rel, "/", 2)
			if len(parts) != 2 || parts[1] == "" {
				return fmt.Errorf("template payload root %s must be a regular directory", rel)
			}
			if err := validateTemplateAssetPath(parts[1]); err != nil {
				return fmt.Errorf("invalid template asset %s: %w", rel, err)
			}
			data, err := rootHandle.ReadFile(path)
			if err != nil {
				return err
			}
			file := TemplatePackageFile{Path: parts[1], Data: data, Mode: info.Mode().Perm()}
			if top == "feature" {
				pkg.Feature = append(pkg.Feature, file)
			} else {
				pkg.Inject = append(pkg.Inject, file)
			}
		case "README.md", "template.yaml":
			data, err := rootHandle.ReadFile(path)
			if err != nil {
				return err
			}
			pkg.Metadata = append(pkg.Metadata, TemplatePackageFile{
				Path: rel, Data: data, Mode: info.Mode().Perm(),
			})
			if top == "template.yaml" {
				manifest, err := parseTemplateManifest(data)
				if err != nil {
					return fmt.Errorf("parsing template manifest: %w", err)
				}
				pkg.Manifest = manifest
			}
		}
		return nil
	})
	if err != nil {
		return TemplatePackage{}, fmt.Errorf("validating template package %s: %w", displayPath, err)
	}
	sort.Slice(pkg.Feature, func(i, j int) bool { return pkg.Feature[i].Path < pkg.Feature[j].Path })
	sort.Slice(pkg.Inject, func(i, j int) bool { return pkg.Inject[i].Path < pkg.Inject[j].Path })
	sort.Slice(pkg.Metadata, func(i, j int) bool { return pkg.Metadata[i].Path < pkg.Metadata[j].Path })
	return pkg, nil
}

func parseTemplateManifest(data []byte) (TemplateManifest, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var manifest TemplateManifest
	if err := decoder.Decode(&manifest); err != nil {
		return TemplateManifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return TemplateManifest{}, errors.New("manifest contains multiple YAML documents")
		}
		return TemplateManifest{}, err
	}
	if manifest.Version != templateManifestVersion {
		return TemplateManifest{}, fmt.Errorf("unsupported template manifest version %d", manifest.Version)
	}
	for role := range manifest.Guidance {
		if role != string(ManagedTemplateRoleWorktree) && role != string(ManagedTemplateRoleOrchestrator) {
			return TemplateManifest{}, fmt.Errorf("unknown guidance role %q", role)
		}
	}
	seenSkills := make(map[string]bool)
	for i, skill := range manifest.Skills {
		if err := validateSkillName(skill.Name); err != nil {
			return TemplateManifest{}, fmt.Errorf("skill %d: %w", i+1, err)
		}
		if seenSkills[skill.Name] {
			return TemplateManifest{}, fmt.Errorf("duplicate skill reference %q", skill.Name)
		}
		seenSkills[skill.Name] = true
		if len(skill.Roles) == 0 {
			return TemplateManifest{}, fmt.Errorf("skill %q must declare at least one role", skill.Name)
		}
		if err := validateManifestValues("role", skill.Name, skill.Roles,
			string(ManagedTemplateRoleWorktree), string(ManagedTemplateRoleOrchestrator)); err != nil {
			return TemplateManifest{}, err
		}
		if err := validateManifestValues("agent", skill.Name, skill.Agents, "claude", "copilot"); err != nil {
			return TemplateManifest{}, err
		}
	}
	return manifest, nil
}

func validateManifestValues(kind, skill string, values []string, allowed ...string) error {
	seen := make(map[string]bool)
	valid := make(map[string]bool, len(allowed))
	for _, value := range allowed {
		valid[value] = true
	}
	for _, value := range values {
		if !valid[value] {
			return fmt.Errorf("skill %q has unknown %s %q", skill, kind, value)
		}
		if seen[value] {
			return fmt.Errorf("skill %q repeats %s %q", skill, kind, value)
		}
		seen[value] = true
	}
	return nil
}

func validateSkillName(name string) error {
	if name == "" {
		return errors.New("skill name cannot be empty")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' || unicode.IsSpace(r) {
			return fmt.Errorf("skill name %q contains an unsafe character", name)
		}
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return fmt.Errorf("skill name %q is hidden or reserved", name)
	}
	return nil
}

func validateTemplateAssetPath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return fmt.Errorf("asset path %q is not a safe relative slash path", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("asset path %q is not normalized", path)
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("asset path %q contains an unsafe component", path)
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return fmt.Errorf("asset path %q contains a control character", path)
			}
		}
		if strings.EqualFold(part, "SKILL.md") {
			return fmt.Errorf("asset path %q contains a skill implementation; use manifest skill references instead", path)
		}
		if strings.EqualFold(part, "skills") && i > 0 {
			parent := strings.ToLower(parts[i-1])
			if parent == ".claude" || parent == ".github" || parent == ".copilot" {
				return fmt.Errorf("asset path %q contains an agent skill store; use manifest skill references instead", path)
			}
		}
	}
	return nil
}

func copyTemplatePackageRoot(pkg TemplatePackage, destination *os.Root) error {
	for _, dir := range []string{"feature", "inject"} {
		if err := destination.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		prefix string
		files  []TemplatePackageFile
	}{
		{prefix: "feature", files: pkg.Feature},
		{prefix: "inject", files: pkg.Inject},
	} {
		for _, file := range group.files {
			path := filepath.Join(group.prefix, filepath.FromSlash(file.Path))
			if err := destination.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := writeTemplatePackageRootFile(destination, path, file.Data, file.Mode.Perm()); err != nil {
				return err
			}
		}
	}
	for _, file := range pkg.Metadata {
		if err := writeTemplatePackageRootFile(destination, file.Path, file.Data, file.Mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}

func writeTemplatePackageRootFile(root *os.Root, path string, data []byte, mode fs.FileMode) error {
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func createTemplateRegistryStage(root *os.Root, name string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		random := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", err
		}
		stage := "." + name + "-stage-" + fmt.Sprintf("%x", random)
		if err := root.Mkdir(stage, 0o700); err == nil {
			return stage, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not allocate template registry staging directory")
}

func createTemplateRegistryQuarantineName(root *os.Root, name string) (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		random := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", err
		}
		candidate := ".removed-" + name + "-" + fmt.Sprintf("%x", random)
		if _, err := root.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate template registry recovery name")
}

func templateRegistryAnchor(repoRoot, scope string) string {
	if scope == TemplateScopeRepo {
		return repoRoot
	}
	home, _ := os.UserHomeDir()
	return home
}

func ensureRegistryRoot(root, anchor string) error {
	if anchor == "" {
		return fmt.Errorf("template registry %s has no trusted anchor", root)
	}
	if err := ensureNoSymlinkParents(anchor, root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating template registry %s: %w", root, err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("template registry %s is not a regular directory", root)
	}
	return nil
}

func ensureNoSymlinkParents(anchor, target string) error {
	anchor, err := filepath.Abs(anchor)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(anchor, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s escapes %s", target, anchor)
	}
	anchorInfo, err := os.Lstat(anchor)
	if err != nil {
		return err
	}
	if anchorInfo.Mode()&os.ModeSymlink != 0 || !anchorInfo.IsDir() {
		return fmt.Errorf("trusted template registry anchor %s is not a regular directory", anchor)
	}
	current := anchor
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %s contains symbolic link component %s", target, current)
		}
	}
	return nil
}

type templateRegistryLock struct {
	root     *os.Root
	file     *os.File
	identity managedObjectIdentity
	path     string
}

func acquireTemplateRegistryLock(rootPath string) (*templateRegistryLock, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("opening template registry root: %w", err)
	}
	identity, err := managedRootIdentity(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	file, err := root.OpenFile(".registry.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("opening template registry lock: %w", err)
	}
	if err := flockTryExclusive(file); err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("template registry is locked by another writer: %w", err)
	}
	if err := managedVerifyPathIdentity(rootPath, identity); err != nil {
		releaseErr := errors.Join(flockUnlock(file), file.Close(), root.Close())
		return nil, errors.Join(err, releaseErr)
	}
	return &templateRegistryLock{
		root: root, file: file, identity: identity, path: rootPath,
	}, nil
}

func (lock *templateRegistryLock) verifyPath() error {
	if lock == nil {
		return errors.New("template registry lock is nil")
	}
	return managedVerifyPathIdentity(lock.path, lock.identity)
}

func (lock *templateRegistryLock) Release() error {
	if lock == nil {
		return nil
	}
	return errors.Join(flockUnlock(lock.file), lock.file.Close(), lock.root.Close())
}
