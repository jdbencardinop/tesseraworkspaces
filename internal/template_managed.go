package internal

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	managedTemplateStateVersion = 1
	managedTemplateStateName    = ".tws-templates.json"
	managedTemplateLockName     = ".tws-templates.lock"
)

type ManagedTemplateRole string

const (
	ManagedTemplateRoleWorktree     ManagedTemplateRole = "worktree"
	ManagedTemplateRoleOrchestrator ManagedTemplateRole = "orchestrator"
)

type ManagedTemplateSource struct {
	Kind     string `json:"kind"` // builtin | registry
	Scope    string `json:"scope,omitempty"`
	Name     string `json:"name,omitempty"`
	OriginID string `json:"origin_id,omitempty"`
}

func ValidateManagedTemplateReplan(
	original, replanned *ManagedTemplatePlan,
	lock *ManagedTemplateLock,
) error {
	if original == nil || replanned == nil || lock == nil {
		return errors.New("managed template replan identity validation requires plans and held lock")
	}
	if original.metadataIdentity != (managedObjectIdentity{}) &&
		original.metadataIdentity != replanned.metadataIdentity {
		return errors.New("workspace metadata root identity changed after consent")
	}
	if original.featureIdentity != (managedObjectIdentity{}) {
		if original.featureIdentity != replanned.featureIdentity ||
			original.featureIdentity != lock.rootIdentity {
			return errors.New("managed feature root identity changed after consent")
		}
	} else {
		if !lock.createdRoot {
			return errors.New("managed feature root appeared before this invocation acquired its reservation")
		}
		if replanned.featureIdentity != lock.rootIdentity {
			return errors.New("new managed feature root identity does not match the held reservation")
		}
	}
	normalized := *replanned
	normalized.metadataIdentity = original.metadataIdentity
	normalized.featureIdentity = original.featureIdentity
	fingerprint, err := managedTemplatePlanFingerprint(&normalized)
	if err != nil {
		return err
	}
	if fingerprint != original.Fingerprint {
		return errors.New("managed template plan drifted after consent or reservation")
	}
	return nil
}

type ManagedTemplateOwnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type ManagedTemplateOwnedLink struct {
	Worktree       string `json:"worktree"`
	Path           string `json:"path"`
	Source         string `json:"source"`
	Target         string `json:"target"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	WorktreeDevice uint64 `json:"worktree_device"`
	WorktreeInode  uint64 `json:"worktree_inode"`
	GitDirDevice   uint64 `json:"git_dir_device"`
	GitDirInode    uint64 `json:"git_dir_inode"`
}

type ManagedTemplateRoleState struct {
	Source               ManagedTemplateSource      `json:"source"`
	Agent                string                     `json:"agent"`
	AgentExplicit        bool                       `json:"agent_explicit,omitempty"`
	AgentCommandAtSelect string                     `json:"agent_command_at_selection,omitempty"`
	Files                []ManagedTemplateOwnedFile `json:"files"`
	Links                []ManagedTemplateOwnedLink `json:"links,omitempty"`
}

type ManagedTemplateState struct {
	Version int                                 `json:"version"`
	Owner   ManagedTemplateOwner                `json:"owner"`
	Roles   map[string]ManagedTemplateRoleState `json:"roles"`
}

type ManagedTemplateOwner struct {
	RepoRoot     string `json:"repo_root"`
	GitIdentity  string `json:"git_identity,omitempty"`
	GitDirDevice uint64 `json:"git_dir_device"`
	GitDirInode  uint64 `json:"git_dir_inode"`
}

type ManagedTemplateRoleRequest struct {
	Reference    string
	ReferenceSet bool
}

type ManagedTemplatePlanRequest struct {
	Workspace           Workspace
	Feature             string
	FeaturePath         string
	Roles               []ManagedTemplateRole
	Worktree            ManagedTemplateRoleRequest
	Orchestrator        ManagedTemplateRoleRequest
	Agent               string
	AgentSet            bool
	LinkMaterializeOnly bool
	OnlyWorktree        string
	RetireWorktree      string
	InjectInto          string
	InjectIntoSet       bool
}

type ManagedTemplateRemoval struct {
	Feature string
	Role    ManagedTemplateRole
	Path    string
	Kind    string // file | link

	root       string
	relative   string
	fileBefore managedFileObservation
	linkBefore managedLinkObservation
}

type ManagedTemplatePlan struct {
	Request           ManagedTemplatePlanRequest
	Removals          []ManagedTemplateRemoval
	Writes            []managedTemplateWrite
	LinkWrites        []managedTemplateLinkWrite
	NewState          ManagedTemplateState
	StateChanged      bool
	StateBeforeDigest string
	StateBefore       managedFileObservation
	AffectedWorktrees []string
	WorktreeEvidence  []string
	SourceEvidence    []ManagedTemplateSourceEvidence
	Fingerprint       string

	worktreeChecks     []injectWorktreeTarget
	worktreeIdentities map[string]managedWorktreeIdentity
	retiredLinks       []managedTemplateRetiredLink
	ownedSourceChecks  []ManagedTemplateOwnedFile
	metadataIdentity   managedObjectIdentity
	featureIdentity    managedObjectIdentity
}

type ManagedTemplateSourceEvidence struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type managedTemplateWrite struct {
	Role     ManagedTemplateRole
	Root     string
	Relative string
	Data     []byte
	Mode     fs.FileMode
	Before   managedFileObservation
}

type managedTemplateLinkWrite struct {
	Role         ManagedTemplateRole
	Worktree     string
	WorktreeRoot string
	Relative     string
	Source       string
	Target       string
	Before       managedLinkObservation
}

type managedTemplateRetiredLink struct {
	worktreeRoot string
	owned        ManagedTemplateOwnedLink
	before       managedLinkObservation
}

type managedFileObservation struct {
	Exists bool   `json:"exists"`
	Kind   string `json:"kind,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	Device uint64 `json:"device,omitempty"`
	Inode  uint64 `json:"inode,omitempty"`
}

type managedLinkObservation struct {
	Exists  bool   `json:"exists"`
	Kind    string `json:"kind,omitempty"`
	Target  string `json:"target,omitempty"`
	Tracked bool   `json:"tracked"`
	Device  uint64 `json:"device,omitempty"`
	Inode   uint64 `json:"inode,omitempty"`
}

type managedWorktreeIdentity struct {
	WorktreeDevice uint64
	WorktreeInode  uint64
	GitDirDevice   uint64
	GitDirInode    uint64
}

type managedDesiredFile struct {
	Path string
	Data []byte
	Mode fs.FileMode
}

type managedTemplateAgentSelection struct {
	Agent        string
	Explicit     bool
	AgentCommand string
}

type ManagedTemplateLock struct {
	root          *os.Root
	file          *os.File
	lifecycle     *ManagedFeatureLifecycleLock
	ownsLifecycle bool
	token         string
	rootIdentity  managedObjectIdentity
	createdRoot   bool
}

type ManagedFeatureLifecycleLock struct {
	path     string
	dir      *os.File
	identity managedObjectIdentity
}

// ManagedTemplateApplyStepHook is a test-only checkpoint seam.
var ManagedTemplateApplyStepHook func(step, path string) error

func ManagedTemplateStatePath(featurePath string) string {
	return filepath.Join(featurePath, managedTemplateStateName)
}

func ManagedTemplateLockPath(featurePath string) string {
	return filepath.Join(featurePath, managedTemplateLockName)
}

func ManagedTemplateStatePresent(featurePath string) (bool, error) {
	if residue, err := managedRecoveryResidue(featurePath); err != nil {
		return false, err
	} else if residue != "" {
		return false, fmt.Errorf("managed template recovery evidence exists at %s; inspect and preserve it before retrying", residue)
	}
	info, err := os.Lstat(ManagedTemplateStatePath(featurePath))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading managed template state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, fmt.Errorf("managed template state must be a regular file")
	}
	return true, nil
}

func LoadManagedTemplateState(featurePath string) (ManagedTemplateState, error) {
	state, _, err := loadManagedTemplateState(featurePath)
	return state, err
}

func loadManagedTemplateState(featurePath string) (ManagedTemplateState, []byte, error) {
	path := ManagedTemplateStatePath(featurePath)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ManagedTemplateState{
			Version: managedTemplateStateVersion,
			Roles:   make(map[string]ManagedTemplateRoleState),
		}, nil, nil
	}
	if err != nil {
		return ManagedTemplateState{}, nil, fmt.Errorf("reading managed template state %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ManagedTemplateState{}, nil, fmt.Errorf("managed template state %s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ManagedTemplateState{}, nil, fmt.Errorf("reading managed template state %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state ManagedTemplateState
	if err := decoder.Decode(&state); err != nil {
		return ManagedTemplateState{}, nil, fmt.Errorf("parsing managed template state %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ManagedTemplateState{}, nil, fmt.Errorf("parsing managed template state %s: trailing JSON value", path)
		}
		return ManagedTemplateState{}, nil, fmt.Errorf("parsing managed template state %s: %w", path, err)
	}
	if err := validateManagedTemplateState(state, false); err != nil {
		return ManagedTemplateState{}, nil, fmt.Errorf("invalid managed template state %s: %w", path, err)
	}
	return state, data, nil
}

func validateManagedTemplateState(state ManagedTemplateState, allowPending bool) error {
	if state.Version != managedTemplateStateVersion {
		return fmt.Errorf("unsupported version %d", state.Version)
	}
	if state.Roles == nil {
		return errors.New("roles must be an object")
	}
	if err := validateManagedTemplateOwner(state.Owner); err != nil {
		return fmt.Errorf("owner: %w", err)
	}
	owned := make(map[string]string)
	for roleName, roleState := range state.Roles {
		role := ManagedTemplateRole(roleName)
		if role != ManagedTemplateRoleWorktree && role != ManagedTemplateRoleOrchestrator {
			return fmt.Errorf("unknown role %q", roleName)
		}

		if err := validateManagedSource(roleState.Source); err != nil {
			return fmt.Errorf("%s source: %w", role, err)
		}
		if err := ValidateManagedTemplateAgent(roleState.Agent); err != nil {
			return fmt.Errorf("%s agent: %w", role, err)
		}
		if roleState.AgentExplicit {
			if strings.TrimSpace(roleState.AgentCommandAtSelect) == "" {
				return fmt.Errorf("%s explicit agent selection is missing launch-command provenance", role)
			}
		} else if roleState.AgentCommandAtSelect != "" {
			return fmt.Errorf("%s inferred agent selection carries explicit launch-command provenance", role)
		}
		seenFiles := make(map[string]bool)
		for _, file := range roleState.Files {
			if err := validateManagedDestination(role, file.Path); err != nil {
				return fmt.Errorf("%s file %q: %w", role, file.Path, err)
			}
			if seenFiles[file.Path] {
				return fmt.Errorf("%s repeats owned file %q", role, file.Path)
			}
			seenFiles[file.Path] = true
			key := string(role) + "\x00" + strings.ToLower(file.Path)
			if prior := owned[key]; prior != "" {
				return fmt.Errorf("%s files %q and %q alias", role, prior, file.Path)
			}
			owned[key] = file.Path
			if len(file.SHA256) != 64 {
				return fmt.Errorf("%s file %q has invalid digest", role, file.Path)
			}
			if _, err := hex.DecodeString(file.SHA256); err != nil {
				return fmt.Errorf("%s file %q has invalid digest", role, file.Path)
			}
			if file.Mode > 0o777 {
				return fmt.Errorf("%s file %q has invalid mode", role, file.Path)
			}
		}
		if role != ManagedTemplateRoleWorktree && len(roleState.Links) > 0 {
			return fmt.Errorf("%s role cannot own external links", role)
		}
		seenLinks := make(map[string]bool)
		for _, link := range roleState.Links {
			if link.Worktree == "" {
				return errors.New("worktree link has empty worktree identity")
			}
			if _, err := injectWorktreePath("feature", link.Worktree); err != nil {
				return fmt.Errorf("worktree link %q: %w", link.Worktree, err)
			}
			if err := validateManagedDestination(ManagedTemplateRoleWorktree, link.Path); err != nil {
				return fmt.Errorf("worktree link %q: %w", link.Path, err)
			}
			if err := validateManagedDestination(ManagedTemplateRoleWorktree, link.Source); err != nil {
				return fmt.Errorf("worktree link source %q: %w", link.Source, err)
			}
			if !seenFiles[link.Source] {
				return fmt.Errorf("worktree link %q references unowned source %q", link.Path, link.Source)
			}
			if link.Path != link.Source {
				return fmt.Errorf("worktree link %q does not match owned source mapping %q", link.Path, link.Source)
			}
			if link.Target == "" || filepath.IsAbs(link.Target) {
				return fmt.Errorf("worktree link %q has unsafe target", link.Path)
			}
			if (link.Device == 0 || link.Inode == 0) && !allowPending {
				return fmt.Errorf("worktree link %q is missing creation identity", link.Path)
			}
			if link.WorktreeDevice == 0 || link.WorktreeInode == 0 ||
				link.GitDirDevice == 0 || link.GitDirInode == 0 {
				return fmt.Errorf("worktree link %q is missing exact Git-worktree identity", link.Path)
			}
			key := link.Worktree + "\x00" + strings.ToLower(link.Path)
			if seenLinks[key] {
				return fmt.Errorf("duplicate worktree link %q in %q", link.Path, link.Worktree)
			}
			seenLinks[key] = true
		}
	}
	return nil
}

func validateManagedTemplateOwner(owner ManagedTemplateOwner) error {
	if owner.RepoRoot == "" || !filepath.IsAbs(owner.RepoRoot) ||
		filepath.Clean(owner.RepoRoot) != owner.RepoRoot {
		return errors.New("owning repository path is missing or not canonical")
	}
	if owner.GitDirDevice == 0 || owner.GitDirInode == 0 {
		return errors.New("owning repository Git identity is missing")
	}
	return nil
}

func managedTemplateOwnerForRepo(repoRoot string) (ManagedTemplateOwner, error) {
	root, err := MainRepoRootIn(repoRoot)
	if err != nil {
		return ManagedTemplateOwner{}, fmt.Errorf("resolving managed template owning repository: %w", err)
	}
	markerDir, err := GitMarkerDir(root)
	if err != nil {
		return ManagedTemplateOwner{}, err
	}
	commonDir := filepath.Dir(markerDir)
	identity, err := managedDirectoryIdentity(commonDir)
	if err != nil {
		return ManagedTemplateOwner{}, fmt.Errorf("reading owning repository Git identity: %w", err)
	}
	return ManagedTemplateOwner{
		RepoRoot: root, GitIdentity: ComputeGitIdentity(root),
		GitDirDevice: identity.Device, GitDirInode: identity.Inode,
	}, nil
}

func validateManagedTemplateOwnerForWorkspace(state ManagedTemplateState, ws Workspace) error {
	current, err := managedTemplateOwnerForRepo(ws.RepoRoot)
	if err != nil {
		return err
	}
	if current.GitDirDevice != state.Owner.GitDirDevice ||
		current.GitDirInode != state.Owner.GitDirInode {
		return fmt.Errorf("managed feature belongs to owning repository %s, not %s",
			state.Owner.RepoRoot, ws.RepoRoot)
	}
	return nil
}

func validateManagedSource(source ManagedTemplateSource) error {
	switch source.Kind {
	case "builtin":
		if source.Scope != "" || source.Name != "" || source.OriginID != "" {
			return errors.New("builtin source cannot carry scope, name, or origin identity")
		}
	case "registry":
		if source.Scope != TemplateScopeRepo && source.Scope != TemplateScopeGlobal {
			return fmt.Errorf("registry source has invalid scope %q", source.Scope)
		}
		if err := ValidateTemplateName(source.Name); err != nil {
			return err
		}
		if !markerIDRegexp.MatchString(source.OriginID) {
			return errors.New("registry source has invalid origin identity")
		}
	default:
		return fmt.Errorf("unknown source kind %q", source.Kind)
	}
	return nil
}

func ValidateManagedTemplateAgent(agent string) error {
	switch agent {
	case "claude", "copilot":
		return nil
	default:
		return fmt.Errorf("unsupported rendering agent %q (supported: claude, copilot)", agent)
	}
}

func ResolveManagedTemplateAgent(agentCommand string) (string, error) {
	fields := strings.Fields(agentCommand)
	if len(fields) == 0 {
		return "", errors.New("agent_command is empty; pass --agent claude or --agent copilot")
	}
	base := filepath.Base(fields[0])
	switch base {
	case "claude", "claude-dev", "cc":
		return "claude", nil
	case "copilot":
		return "copilot", nil
	default:
		return "", fmt.Errorf("agent_command executable %q is not a recognized rendering agent; pass --agent claude or --agent copilot", base)
	}
}

func PlanManagedTemplate(request ManagedTemplatePlanRequest) (*ManagedTemplatePlan, error) {
	if err := validateManagedTemplateRequest(&request); err != nil {
		return nil, err
	}
	if residue, err := managedRecoveryResidue(request.FeaturePath); err != nil {
		return nil, err
	} else if residue != "" {
		return nil, fmt.Errorf("managed template recovery evidence exists at %s; inspect and preserve it before retrying", residue)
	}
	state, rawState, err := loadManagedTemplateState(request.FeaturePath)
	if err != nil {
		return nil, err
	}
	if len(rawState) == 0 {
		state.Owner, err = managedTemplateOwnerForRepo(request.Workspace.RepoRoot)
		if err != nil {
			return nil, err
		}
	} else if err := validateManagedTemplateOwnerForWorkspace(state, request.Workspace); err != nil {
		return nil, err
	} else {
		state.Owner, err = managedTemplateOwnerForRepo(request.Workspace.RepoRoot)
		if err != nil {
			return nil, err
		}
	}
	stateBefore, err := observeManagedFile(request.FeaturePath, managedTemplateStateName)
	if err != nil {
		return nil, err
	}
	globalCfg, repoCfg, err := LoadTemplateConfigLayersE(request.Workspace.RepoRoot)
	if err != nil {
		return nil, err
	}
	cfg := mergeConfig(globalCfg, repoCfg)

	plan := &ManagedTemplatePlan{
		Request:            request,
		NewState:           cloneManagedTemplateState(state),
		StateBeforeDigest:  digestBytes(rawState),
		StateBefore:        stateBefore,
		worktreeIdentities: make(map[string]managedWorktreeIdentity),
	}
	if identity, identityErr := managedDirectoryIdentity(request.Workspace.MetadataRoot); identityErr == nil {
		plan.metadataIdentity = identity
	} else if !errors.Is(identityErr, fs.ErrNotExist) {
		return nil, identityErr
	}
	if identity, identityErr := managedDirectoryIdentity(request.FeaturePath); identityErr == nil {
		plan.featureIdentity = identity
	} else if !errors.Is(identityErr, fs.ErrNotExist) {
		return nil, identityErr
	}
	if request.RetireWorktree != "" {
		if err := planManagedTemplateWorktreeRetirement(plan, state, cfg); err != nil {
			return nil, err
		}
	} else if request.LinkMaterializeOnly {
		if err := planManagedTemplateLinksOnly(plan, state, cfg); err != nil {
			return nil, err
		}
	} else {
		if err := planManagedTemplateRoles(plan, state, globalCfg, repoCfg, cfg); err != nil {
			return nil, err
		}
	}
	normalizeManagedTemplatePlan(plan)
	if err := validateManagedTemplateState(plan.NewState, true); err != nil {
		return nil, fmt.Errorf("generated managed template state is invalid: %w", err)
	}
	stateBytes, err := marshalManagedTemplateState(plan.NewState)
	if err != nil {
		return nil, err
	}
	plan.StateChanged = !bytes.Equal(bytes.TrimSpace(rawState), bytes.TrimSpace(stateBytes))
	if len(plan.LinkWrites) > 0 {
		plan.StateChanged = true
	}
	fingerprint, err := managedTemplatePlanFingerprint(plan)
	if err != nil {
		return nil, err
	}
	plan.Fingerprint = fingerprint
	return plan, nil
}

func validateManagedTemplateRequest(request *ManagedTemplatePlanRequest) error {
	if request == nil {
		return errors.New("managed template request is nil")
	}
	if request.Feature == "" || request.FeaturePath == "" {
		return errors.New("managed template request requires a feature and feature path")
	}
	if err := GuardFeatureName(request.Workspace.MetadataRoot, request.Feature); err != nil {
		return err
	}
	resolved, err := request.Workspace.ResolveFeaturePath(request.Feature)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(request.FeaturePath) {
		return fmt.Errorf("feature path %s does not match resolved path %s", request.FeaturePath, resolved)
	}
	if request.AgentSet {
		if err := ValidateManagedTemplateAgent(request.Agent); err != nil {
			return err
		}
	}
	seen := make(map[ManagedTemplateRole]bool)
	for _, role := range request.Roles {
		if role != ManagedTemplateRoleWorktree && role != ManagedTemplateRoleOrchestrator {
			return fmt.Errorf("unknown managed template role %q", role)
		}
		if seen[role] {
			return fmt.Errorf("managed template role %q was selected more than once", role)
		}
		seen[role] = true
	}
	if request.LinkMaterializeOnly {
		if len(request.Roles) != 1 || request.Roles[0] != ManagedTemplateRoleWorktree {
			return errors.New("link materialization requires only the worktree role")
		}
		if request.RetireWorktree != "" {
			return errors.New("link materialization cannot also retire a worktree")
		}
		return nil
	}
	if request.RetireWorktree != "" {
		if len(request.Roles) != 1 || request.Roles[0] != ManagedTemplateRoleWorktree {
			return errors.New("worktree retirement requires only the worktree role")
		}
		if _, err := injectWorktreePath(request.FeaturePath, request.RetireWorktree); err != nil {
			return err
		}
		return nil
	}
	if len(request.Roles) == 0 {
		return errors.New("managed template request selected no roles")
	}
	if request.Worktree.ReferenceSet {
		if _, err := ParseTemplateReference(request.Worktree.Reference); err != nil {
			return err
		}
	}
	if request.Orchestrator.ReferenceSet {
		if _, err := ParseTemplateReference(request.Orchestrator.Reference); err != nil {
			return err
		}
	}
	return nil
}

func planManagedTemplateRoles(
	plan *ManagedTemplatePlan,
	oldState ManagedTemplateState,
	globalCfg, repoCfg, cfg Config,
) error {
	for _, role := range plan.Request.Roles {
		oldRole, hadOldRole := oldState.Roles[string(role)]
		source, pkg, err := resolveManagedTemplateSource(plan.Request, role, oldRole, hadOldRole, globalCfg, repoCfg)
		if err != nil {
			return fmt.Errorf("%s template: %w", role, err)
		}
		if pkg != nil {
			addManagedSourceEvidence(plan, *pkg)
		}
		agentSelection, err := resolveManagedRoleAgent(plan.Request, oldRole, hadOldRole, cfg)
		if err != nil {
			return fmt.Errorf("%s template: %w", role, err)
		}
		if role == ManagedTemplateRoleWorktree {
			target := cfg.InjectInto
			if plan.Request.InjectIntoSet {
				target = plan.Request.InjectInto
			}
			if target != "" && target != "." {
				if plan.Request.InjectIntoSet {
					return fmt.Errorf("managed worktree guidance requires repository-root injection; explicit --into %q is incompatible (use --into . for a one-run root override)", target)
				}
				return fmt.Errorf("managed worktree guidance requires repository-root injection; configured inject_into %q is incompatible (use --into . on tws inject for a one-run root override, unset inject_into, or sync only the orchestrator role)", target)
			}
		}
		desired, err := desiredManagedTemplateFiles(plan.Request.Feature, role, agentSelection.Agent, pkg)
		if err != nil {
			return fmt.Errorf("%s template: %w", role, err)
		}
		if err := planManagedRoleFiles(plan, oldRole, hadOldRole, role, source, agentSelection, desired); err != nil {
			return err
		}
	}
	return nil
}

func resolveManagedTemplateSource(
	request ManagedTemplatePlanRequest,
	role ManagedTemplateRole,
	oldRole ManagedTemplateRoleState,
	hadOldRole bool,
	globalCfg, repoCfg Config,
) (ManagedTemplateSource, *TemplatePackage, error) {
	roleRequest := request.Worktree
	repoDefault := repoCfg.WorktreeTemplate
	globalDefault := globalCfg.WorktreeTemplate
	if role == ManagedTemplateRoleOrchestrator {
		roleRequest = request.Orchestrator
		repoDefault = repoCfg.OrchestratorTemplate
		globalDefault = globalCfg.OrchestratorTemplate
	}
	if roleRequest.ReferenceSet {
		resolved, err := ResolveTemplateReference(request.Workspace.RepoRoot, roleRequest.Reference)
		if err != nil {
			return ManagedTemplateSource{}, nil, err
		}
		pkg, err := LoadTemplatePackage(resolved.Path)
		if err != nil {
			return ManagedTemplateSource{}, nil, err
		}
		return ManagedTemplateSource{
			Kind: "registry", Scope: resolved.Reference.Scope, Name: resolved.Reference.Name,
			OriginID: resolved.OriginID,
		}, &pkg, nil
	}
	if hadOldRole {
		if oldRole.Source.Kind == "builtin" {
			return oldRole.Source, nil, nil
		}
		resolved, err := ResolvePersistedTemplateReference(request.Workspace.RepoRoot, TemplateReference{
			Scope: oldRole.Source.Scope,
			Name:  oldRole.Source.Name,
		})
		if err != nil {
			return ManagedTemplateSource{}, nil, fmt.Errorf("saved source is unavailable; explicitly reselect this role: %w", err)
		}
		if resolved.OriginID != oldRole.Source.OriginID {
			return ManagedTemplateSource{}, nil, errors.New("saved source origin no longer matches the selected registry; explicitly reselect this role")
		}
		pkg, err := LoadTemplatePackage(resolved.Path)
		if err != nil {
			return ManagedTemplateSource{}, nil, err
		}
		return oldRole.Source, &pkg, nil
	}
	defaultRef := repoDefault
	if defaultRef == "" {
		defaultRef = globalDefault
	}
	if defaultRef == "" {
		return ManagedTemplateSource{Kind: "builtin"}, nil, nil
	}
	resolved, err := ResolveTemplateReference(request.Workspace.RepoRoot, defaultRef)
	if err != nil {
		return ManagedTemplateSource{}, nil, fmt.Errorf("resolving configured default %q: %w", defaultRef, err)
	}
	pkg, err := LoadTemplatePackage(resolved.Path)
	if err != nil {
		return ManagedTemplateSource{}, nil, err
	}
	return ManagedTemplateSource{
		Kind: "registry", Scope: resolved.Reference.Scope, Name: resolved.Reference.Name,
		OriginID: resolved.OriginID,
	}, &pkg, nil
}

func resolveManagedRoleAgent(
	request ManagedTemplatePlanRequest,
	oldRole ManagedTemplateRoleState,
	hadOldRole bool,
	cfg Config,
) (managedTemplateAgentSelection, error) {
	currentCommand := cfg.GetAgentCommand()
	if request.AgentSet {
		return managedTemplateAgentSelection{
			Agent: request.Agent, Explicit: true, AgentCommand: currentCommand,
		}, nil
	}
	if hadOldRole && oldRole.AgentExplicit {
		if currentCommand != oldRole.AgentCommandAtSelect {
			return managedTemplateAgentSelection{}, fmt.Errorf(
				"saved rendering agent is %q from an explicit selection made while agent_command was %q, but agent_command is now %q; pass --agent to explicitly rerender",
				oldRole.Agent, oldRole.AgentCommandAtSelect, currentCommand,
			)
		}
		return managedTemplateAgentSelection{
			Agent: oldRole.Agent, Explicit: true, AgentCommand: oldRole.AgentCommandAtSelect,
		}, nil
	}
	current, err := ResolveManagedTemplateAgent(currentCommand)
	if err != nil {
		return managedTemplateAgentSelection{}, err
	}
	if hadOldRole && oldRole.Agent != current {
		return managedTemplateAgentSelection{}, fmt.Errorf("saved rendering agent is %q but configured agent resolves to %q; pass --agent to explicitly rerender", oldRole.Agent, current)
	}
	if hadOldRole {
		return managedTemplateAgentSelection{Agent: oldRole.Agent}, nil
	}
	return managedTemplateAgentSelection{Agent: current}, nil
}

func desiredManagedTemplateFiles(
	feature string,
	role ManagedTemplateRole,
	agent string,
	pkg *TemplatePackage,
) ([]managedDesiredFile, error) {
	native := managedNativePath(role, agent)
	nativeCanonical := strings.ToLower(filepath.ToSlash(native))
	var payload []TemplatePackageFile
	manifest := TemplateManifest{}
	if pkg != nil {
		manifest = pkg.Manifest
		if role == ManagedTemplateRoleWorktree {
			payload = pkg.Inject
		} else {
			payload = pkg.Feature
		}
	}
	desired := make([]managedDesiredFile, 0, len(payload)+1)
	var nativeBase []byte
	nativeMode := fs.FileMode(0o644)
	nativePresent := false
	nativePayloadPath := ""
	for _, file := range payload {
		if err := validateManagedDestination(role, file.Path); err != nil {
			return nil, fmt.Errorf("asset %q: %w", file.Path, err)
		}
		fileCanonical := strings.ToLower(filepath.ToSlash(file.Path))
		if fileCanonical == nativeCanonical {
			if file.Path != native {
				return nil, fmt.Errorf("assets %q and %q alias under case-insensitive filesystems", native, file.Path)
			}
			if nativePresent {
				return nil, fmt.Errorf("assets %q and %q alias under case-insensitive filesystems", nativePayloadPath, file.Path)
			}
			nativeBase = append([]byte(nil), file.Data...)
			nativeMode = file.Mode.Perm()
			nativePresent = true
			nativePayloadPath = file.Path
			continue
		}
		if strings.HasPrefix(fileCanonical, nativeCanonical+"/") {
			return nil, fmt.Errorf("asset %q conflicts with native file %q", file.Path, native)
		}
		desired = append(desired, managedDesiredFile{
			Path: file.Path,
			Data: append([]byte(nil), file.Data...),
			Mode: file.Mode.Perm(),
		})
	}
	rendered := renderManagedNativeGuidance(feature, role, agent, nativeBase, nativePresent, manifest)
	desired = append(desired, managedDesiredFile{Path: native, Data: rendered, Mode: nativeMode})
	sort.Slice(desired, func(i, j int) bool { return desired[i].Path < desired[j].Path })
	if err := validateManagedDesiredTree(desired); err != nil {
		return nil, err
	}
	return desired, nil
}

func validateManagedDesiredTree(desired []managedDesiredFile) error {
	byCanonical := make(map[string]string, len(desired))
	for _, file := range desired {
		canonical := strings.ToLower(filepath.ToSlash(file.Path))
		if prior := byCanonical[canonical]; prior != "" {
			return fmt.Errorf("assets %q and %q alias under case-insensitive filesystems", prior, file.Path)
		}
		parts := strings.Split(canonical, "/")
		for i := 1; i < len(parts); i++ {
			ancestor := strings.Join(parts[:i], "/")
			if prior := byCanonical[ancestor]; prior != "" {
				return fmt.Errorf("asset %q conflicts with descendant %q", prior, file.Path)
			}
		}
		for existing, prior := range byCanonical {
			if strings.HasPrefix(existing, canonical+"/") {
				return fmt.Errorf("asset %q conflicts with descendant %q", file.Path, prior)
			}
		}
		byCanonical[canonical] = file.Path
	}
	return nil
}

func managedNativePath(role ManagedTemplateRole, agent string) string {
	if role == ManagedTemplateRoleWorktree {
		if agent == "copilot" {
			return ".github/instructions/tws-worktree.instructions.md"
		}
		return "CLAUDE.local.md"
	}
	if agent == "copilot" {
		return "AGENTS.md"
	}
	return "CLAUDE.md"
}

func renderManagedNativeGuidance(
	feature string,
	role ManagedTemplateRole,
	agent string,
	base []byte,
	basePresent bool,
	manifest TemplateManifest,
) []byte {
	if !basePresent {
		if role == ManagedTemplateRoleWorktree {
			base = []byte("# TWS worktree guidance\n\nWork only in this worktree for feature `" + feature + "`. Implement and test the selected stack entry without editing unrelated stacks.\n")
		} else {
			base = []byte("# TWS feature orchestrator guidance\n\nCoordinate feature `" + feature + "` from this feature directory. Keep stack entries isolated and avoid editing unrelated features.\n")
		}
	}
	if role == ManagedTemplateRoleWorktree && agent == "copilot" {
		base = stripMarkdownFrontmatter(base)
		base = append([]byte("---\napplyTo: \"**\"\n---\n\n"), bytes.TrimSpace(base)...)
		base = append(base, '\n')
	}
	var out bytes.Buffer
	out.Write(bytes.TrimSpace(base))
	out.WriteByte('\n')
	if guidance := strings.TrimSpace(manifest.Guidance[string(role)]); guidance != "" {
		out.WriteString("\n## Template guidance\n\n")
		out.WriteString(guidance)
		out.WriteByte('\n')
	}
	var skills []string
	for _, skill := range manifest.Skills {
		if !managedContainsString(skill.Roles, string(role)) {
			continue
		}
		if len(skill.Agents) > 0 && !managedContainsString(skill.Agents, agent) {
			continue
		}
		skills = append(skills, skill.Name)
	}
	sort.Strings(skills)
	if len(skills) > 0 {
		out.WriteString("\n## Skill references\n\n")
		for _, skill := range skills {
			out.WriteString("- `")
			out.WriteString(skill)
			out.WriteString("` — reference only; invoke it through ")
			out.WriteString(agent)
			out.WriteString(" if installed.\n")
		}
	}
	return out.Bytes()
}

func stripMarkdownFrontmatter(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if !bytes.HasPrefix(trimmed, []byte("---\n")) {
		return trimmed
	}
	rest := trimmed[len("---\n"):]
	if index := bytes.Index(rest, []byte("\n---\n")); index >= 0 {
		return bytes.TrimSpace(rest[index+len("\n---\n"):])
	}
	return trimmed
}

func planManagedRoleFiles(
	plan *ManagedTemplatePlan,
	oldRole ManagedTemplateRoleState,
	hadOldRole bool,
	role ManagedTemplateRole,
	source ManagedTemplateSource,
	agentSelection managedTemplateAgentSelection,
	desired []managedDesiredFile,
) error {
	root := plan.Request.FeaturePath
	if role == ManagedTemplateRoleWorktree {
		root = InjectPath(plan.Request.FeaturePath)
	}
	oldByPath := make(map[string]ManagedTemplateOwnedFile)
	if hadOldRole {
		for _, file := range oldRole.Files {
			oldByPath[file.Path] = file
		}
	}
	newRole := ManagedTemplateRoleState{
		Source:               source,
		Agent:                agentSelection.Agent,
		AgentExplicit:        agentSelection.Explicit,
		AgentCommandAtSelect: agentSelection.AgentCommand,
		Files:                make([]ManagedTemplateOwnedFile, 0, len(desired)),
	}
	for _, file := range desired {
		observation, err := observeManagedFile(root, file.Path)
		if err != nil {
			return fmt.Errorf("%s destination %s: %w", role, filepath.Join(root, filepath.FromSlash(file.Path)), err)
		}
		old, owned := oldByPath[file.Path]
		if observation.Exists {
			if !owned {
				return fmt.Errorf("%s destination %s is unmanaged", role, filepath.Join(root, filepath.FromSlash(file.Path)))
			}
			if err := matchOwnedFile(observation, old); err != nil {
				return fmt.Errorf("%s destination %s is not unchanged owned content: %w", role, filepath.Join(root, filepath.FromSlash(file.Path)), err)
			}
		}
		if !observation.Exists || observation.Hash != digestBytes(file.Data) ||
			observation.Mode != uint32(file.Mode.Perm()) {
			plan.Writes = append(plan.Writes, managedTemplateWrite{
				Role: role, Root: root, Relative: file.Path,
				Data: append([]byte(nil), file.Data...), Mode: file.Mode.Perm(), Before: observation,
			})
		}
		newRole.Files = append(newRole.Files, ManagedTemplateOwnedFile{
			Path: file.Path, SHA256: digestBytes(file.Data), Mode: uint32(file.Mode.Perm()),
		})
		delete(oldByPath, file.Path)
	}
	for path, old := range oldByPath {
		observation, err := observeManagedFile(root, path)
		if err != nil {
			return err
		}
		if !observation.Exists {
			continue
		}
		if err := matchOwnedFile(observation, old); err != nil {
			return fmt.Errorf("%s obsolete destination %s is not unchanged owned content: %w", role, filepath.Join(root, filepath.FromSlash(path)), err)
		}
		plan.Removals = append(plan.Removals, ManagedTemplateRemoval{
			Feature: plan.Request.Feature, Role: role,
			Path: filepath.Join(root, filepath.FromSlash(path)), Kind: "file",
			root: root, relative: path, fileBefore: observation,
		})
	}
	if role == ManagedTemplateRoleWorktree {
		if err := planManagedExternalLinks(plan, oldRole, hadOldRole, &newRole); err != nil {
			return err
		}
	}
	plan.NewState.Roles[string(role)] = newRole
	return nil
}

func planManagedExternalLinks(
	plan *ManagedTemplatePlan,
	oldRole ManagedTemplateRoleState,
	hadOldRole bool,
	newRole *ManagedTemplateRoleState,
) error {
	if plan.Request.Workspace.Mode == ModeCheckout {
		newRole.Links = nil
		return nil
	}
	targets, err := discoverInjectWorktrees(plan.Request.FeaturePath)
	if err != nil {
		return err
	}
	for _, target := range targets {
		plan.AffectedWorktrees = append(plan.AffectedWorktrees, target.Name)
		if err := addManagedWorktreeEvidence(plan, target); err != nil {
			return err
		}
	}
	oldLinks := make(map[string]ManagedTemplateOwnedLink)
	if hadOldRole {
		for _, link := range oldRole.Links {
			oldLinks[managedLinkKey(link.Worktree, link.Path)] = link
		}
	}
	active := make(map[string]injectWorktreeTarget, len(targets))
	for _, target := range targets {
		active[target.Name] = target
		worktreeIdentity := plan.worktreeIdentities[target.Name]
		for _, file := range newRole.Files {
			targetPath := filepath.Join(target.Path, filepath.FromSlash(file.Path))
			relTarget, err := filepath.Rel(filepath.Dir(targetPath),
				filepath.Join(InjectPath(plan.Request.FeaturePath), filepath.FromSlash(file.Path)))
			if err != nil {
				return err
			}
			observation, err := observeManagedLink(target.Path, file.Path)
			if err != nil {
				return fmt.Errorf("worktree %q destination %s: %w", target.Name, targetPath, err)
			}
			if observation.Tracked {
				return fmt.Errorf("worktree %q destination %s is tracked by Git", target.Name, targetPath)
			}
			key := managedLinkKey(target.Name, file.Path)
			old, owned := oldLinks[key]
			if observation.Exists {
				if !owned {
					return fmt.Errorf("worktree %q destination %s is an unmanaged collision", target.Name, targetPath)
				}
				if err := matchOwnedLink(observation, old, worktreeIdentity, file.Path, relTarget); err != nil {
					return fmt.Errorf("worktree %q destination %s has changed link ownership", target.Name, targetPath)
				}
			} else if owned {
				if err := matchOwnedWorktreeIdentity(old, worktreeIdentity); err != nil {
					return fmt.Errorf("worktree %q destination %s has changed link ownership: %w",
						target.Name, targetPath, err)
				}
			}
			newLink := ManagedTemplateOwnedLink{
				Worktree: target.Name, Path: file.Path, Source: file.Path, Target: relTarget,
				WorktreeDevice: worktreeIdentity.WorktreeDevice,
				WorktreeInode:  worktreeIdentity.WorktreeInode,
				GitDirDevice:   worktreeIdentity.GitDirDevice,
				GitDirInode:    worktreeIdentity.GitDirInode,
			}
			if observation.Exists {
				newLink.Device = observation.Device
				newLink.Inode = observation.Inode
			}
			newRole.Links = append(newRole.Links, newLink)
			if !observation.Exists {
				plan.LinkWrites = append(plan.LinkWrites, managedTemplateLinkWrite{
					Role: ManagedTemplateRoleWorktree, Worktree: target.Name,
					WorktreeRoot: target.Path, Relative: file.Path, Source: file.Path,
					Target: relTarget, Before: observation,
				})
			}
			delete(oldLinks, key)
		}
	}
	for _, old := range oldLinks {
		target, ok := active[old.Worktree]
		if !ok {
			path, pathErr := injectWorktreePath(plan.Request.FeaturePath, old.Worktree)
			if pathErr != nil {
				return pathErr
			}
			if _, statErr := os.Lstat(path); errors.Is(statErr, fs.ErrNotExist) {
				return fmt.Errorf("recorded worktree %q is missing; archive it explicitly before retiring managed link ownership",
					old.Worktree)
			}
			return fmt.Errorf("recorded worktree %q is no longer a verified active worktree", old.Worktree)
		}
		observation, err := observeManagedLink(target.Path, old.Path)
		if err != nil {
			return err
		}
		if err := matchOwnedWorktreeIdentity(old, plan.worktreeIdentities[target.Name]); err != nil {
			return fmt.Errorf("worktree %q obsolete destination %s has changed link ownership: %w",
				old.Worktree, filepath.Join(target.Path, filepath.FromSlash(old.Path)), err)
		}
		if !observation.Exists {
			continue
		}
		if observation.Tracked {
			return fmt.Errorf("worktree %q obsolete destination %s is tracked by Git", old.Worktree, filepath.Join(target.Path, filepath.FromSlash(old.Path)))
		}
		expectedTarget, targetErr := filepath.Rel(
			filepath.Dir(filepath.Join(target.Path, filepath.FromSlash(old.Path))),
			filepath.Join(InjectPath(plan.Request.FeaturePath), filepath.FromSlash(old.Source)),
		)
		if targetErr != nil {
			return targetErr
		}
		if err := matchOwnedLink(observation, old, plan.worktreeIdentities[target.Name], old.Source, expectedTarget); err != nil {
			return fmt.Errorf("worktree %q obsolete destination %s has changed link ownership: %w",
				old.Worktree, filepath.Join(target.Path, filepath.FromSlash(old.Path)), err)
		}
		plan.Removals = append(plan.Removals, ManagedTemplateRemoval{
			Feature: plan.Request.Feature, Role: ManagedTemplateRoleWorktree,
			Path: filepath.Join(target.Path, filepath.FromSlash(old.Path)), Kind: "link",
			root: target.Path, relative: old.Path, linkBefore: observation,
		})
	}
	return nil
}

func planManagedTemplateWorktreeRetirement(
	plan *ManagedTemplatePlan,
	oldState ManagedTemplateState,
	cfg Config,
) error {
	roleState, ok := oldState.Roles[string(ManagedTemplateRoleWorktree)]
	if !ok {
		return nil
	}
	if err := validateManagedWorktreeContext(
		plan.Request.Feature, plan.Request.FeaturePath, roleState, cfg, "", false,
	); err != nil {
		return err
	}
	var retiring []ManagedTemplateOwnedLink
	newRole := roleState
	newRole.Links = nil
	for _, link := range roleState.Links {
		if link.Worktree == plan.Request.RetireWorktree {
			retiring = append(retiring, link)
			continue
		}
		newRole.Links = append(newRole.Links, link)
	}
	if len(retiring) == 0 {
		plan.NewState.Roles[string(ManagedTemplateRoleWorktree)] = newRole
		return nil
	}
	targets, err := discoverInjectWorktrees(plan.Request.FeaturePath)
	if err != nil {
		return err
	}
	targetByName := make(map[string]injectWorktreeTarget, len(targets))
	for i := range targets {
		targetByName[targets[i].Name] = targets[i]
	}
	needed := make(map[string]bool)
	for _, link := range roleState.Links {
		needed[link.Worktree] = true
	}
	for _, target := range targets {
		if !needed[target.Name] {
			continue
		}
		if err := addManagedWorktreeEvidence(plan, target); err != nil {
			return err
		}
	}
	for worktree := range needed {
		if _, ok := targetByName[worktree]; !ok {
			return fmt.Errorf("recorded worktree %q is not a verified active worktree; refusing to publish managed link retirement",
				worktree)
		}
	}
	target, ok := targetByName[plan.Request.RetireWorktree]
	if !ok {
		return fmt.Errorf("recorded worktree %q is not a verified active worktree; refusing to retire managed link ownership",
			plan.Request.RetireWorktree)
	}
	plan.AffectedWorktrees = append(plan.AffectedWorktrees, target.Name)
	worktreeIdentity := plan.worktreeIdentities[target.Name]
	for _, link := range retiring {
		observation, err := observeManagedLink(target.Path, link.Path)
		if err != nil {
			return err
		}
		if !observation.Exists {
			return fmt.Errorf("worktree %q recorded managed destination %s is missing; refusing ownership retirement",
				target.Name, filepath.Join(target.Path, filepath.FromSlash(link.Path)))
		}
		if observation.Tracked {
			return fmt.Errorf("worktree %q recorded managed destination %s is tracked by Git",
				target.Name, filepath.Join(target.Path, filepath.FromSlash(link.Path)))
		}
		expectedTarget, err := filepath.Rel(
			filepath.Dir(filepath.Join(target.Path, filepath.FromSlash(link.Path))),
			filepath.Join(InjectPath(plan.Request.FeaturePath), filepath.FromSlash(link.Source)),
		)
		if err != nil {
			return err
		}
		if err := matchOwnedLink(observation, link, worktreeIdentity, link.Source, expectedTarget); err != nil {
			return fmt.Errorf("worktree %q recorded managed destination %s changed before ownership retirement: %w",
				target.Name, filepath.Join(target.Path, filepath.FromSlash(link.Path)), err)
		}
		plan.retiredLinks = append(plan.retiredLinks, managedTemplateRetiredLink{
			worktreeRoot: target.Path,
			owned:        link,
			before:       observation,
		})
	}
	plan.NewState.Roles[string(ManagedTemplateRoleWorktree)] = newRole
	return nil
}

func planManagedTemplateLinksOnly(plan *ManagedTemplatePlan, oldState ManagedTemplateState, cfg Config) error {
	roleState, ok := oldState.Roles[string(ManagedTemplateRoleWorktree)]
	if !ok {
		return errors.New("feature has no managed worktree template selection")
	}
	into := ""
	if plan.Request.InjectIntoSet {
		into = plan.Request.InjectInto
	}
	if err := validateManagedWorktreeContext(plan.Request.Feature, plan.Request.FeaturePath, roleState, cfg, into, plan.Request.InjectIntoSet); err != nil {
		return err
	}
	targets, err := discoverInjectWorktrees(plan.Request.FeaturePath)
	if err != nil {
		return err
	}
	activeTargets := make(map[string]bool, len(targets))
	for _, target := range targets {
		activeTargets[target.Name] = true
		if err := addManagedWorktreeEvidence(plan, target); err != nil {
			return err
		}
	}
	if plan.Request.OnlyWorktree != "" {
		filtered := targets[:0]
		for _, target := range targets {
			if target.Name == plan.Request.OnlyWorktree {
				filtered = append(filtered, target)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("worktree %q is not a verified active worktree", plan.Request.OnlyWorktree)
		}
		targets = filtered
	}
	for _, target := range targets {
		plan.AffectedWorktrees = append(plan.AffectedWorktrees, target.Name)
	}
	oldLinks := make(map[string]ManagedTemplateOwnedLink)
	for _, link := range roleState.Links {
		oldLinks[managedLinkKey(link.Worktree, link.Path)] = link
	}
	newRole := roleState
	newRole.Links = nil
	for _, link := range roleState.Links {
		if activeTargets[link.Worktree] {
			newRole.Links = append(newRole.Links, link)
			continue
		}
		path, pathErr := injectWorktreePath(plan.Request.FeaturePath, link.Worktree)
		if pathErr != nil {
			return pathErr
		}
		if _, statErr := os.Lstat(path); errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("recorded worktree %q is missing; archive it explicitly before retiring managed link ownership",
				link.Worktree)
		}
		return fmt.Errorf("recorded worktree %q is no longer a verified active worktree", link.Worktree)
	}
	existingNew := make(map[string]bool)
	for _, link := range newRole.Links {
		existingNew[managedLinkKey(link.Worktree, link.Path)] = true
	}
	for _, target := range targets {
		worktreeIdentity := plan.worktreeIdentities[target.Name]
		for _, file := range roleState.Files {
			dest := filepath.Join(target.Path, filepath.FromSlash(file.Path))
			relTarget, err := filepath.Rel(filepath.Dir(dest),
				filepath.Join(InjectPath(plan.Request.FeaturePath), filepath.FromSlash(file.Path)))
			if err != nil {
				return err
			}
			observation, err := observeManagedLink(target.Path, file.Path)
			if err != nil {
				return err
			}
			if observation.Tracked {
				return fmt.Errorf("worktree %q destination %s is tracked by Git", target.Name, dest)
			}
			key := managedLinkKey(target.Name, file.Path)
			old, owned := oldLinks[key]
			if observation.Exists {
				if !owned || matchOwnedLink(observation, old, worktreeIdentity, file.Path, relTarget) != nil {
					return fmt.Errorf("worktree %q destination %s is unmanaged or modified", target.Name, dest)
				}
				continue
			}
			if owned {
				if err := matchOwnedWorktreeIdentity(old, worktreeIdentity); err != nil {
					return fmt.Errorf("worktree %q destination %s has changed link ownership: %w",
						target.Name, dest, err)
				}
			}
			if owned && old.Target != relTarget {
				return fmt.Errorf("worktree %q saved link target for %s no longer matches", target.Name, dest)
			}
			plan.LinkWrites = append(plan.LinkWrites, managedTemplateLinkWrite{
				Role: ManagedTemplateRoleWorktree, Worktree: target.Name,
				WorktreeRoot: target.Path, Relative: file.Path, Source: file.Path,
				Target: relTarget, Before: observation,
			})
			if !existingNew[key] {
				newRole.Links = append(newRole.Links, ManagedTemplateOwnedLink{
					Worktree: target.Name, Path: file.Path, Source: file.Path, Target: relTarget,
					WorktreeDevice: worktreeIdentity.WorktreeDevice,
					WorktreeInode:  worktreeIdentity.WorktreeInode,
					GitDirDevice:   worktreeIdentity.GitDirDevice,
					GitDirInode:    worktreeIdentity.GitDirInode,
				})
				existingNew[key] = true
			}
		}
	}
	plan.NewState.Roles[string(ManagedTemplateRoleWorktree)] = newRole
	return nil
}

// ValidateManagedCheckoutContext rechecks managed checkout context after the
// checkout session launch intent is held and before temporary links are made.
func ValidateManagedCheckoutContext(ws Workspace, feature, featurePath, into string) error {
	cfg, err := LoadConfigForRepoE(ws.RepoRoot)
	if err != nil {
		return err
	}
	target := cfg.InjectInto
	explicit := into != ""
	if explicit {
		target = into
	}
	return ValidateManagedCheckoutContextResolved(
		ws, feature, featurePath, cfg.GetAgentCommand(), target, explicit,
	)
}

func ValidateManagedCheckoutContextResolved(
	ws Workspace,
	feature, featurePath, agentCommand, into string,
	intoSet bool,
) error {
	present, err := ManagedTemplateStatePresent(featurePath)
	if err != nil || !present {
		return err
	}
	state, err := LoadManagedTemplateState(featurePath)
	if err != nil {
		return err
	}
	if err := validateManagedTemplateOwnerForWorkspace(state, ws); err != nil {
		return err
	}
	roleState, ok := state.Roles[string(ManagedTemplateRoleWorktree)]
	if !ok {
		return nil
	}
	return validateManagedWorktreeContext(
		feature, featurePath, roleState,
		Config{AgentCommand: agentCommand, InjectInto: into}, into, intoSet,
	)
}

func ValidateManagedOrchestratorContext(
	ws Workspace,
	feature, featurePath, agentCommand string,
) error {
	present, err := ManagedTemplateStatePresent(featurePath)
	if err != nil || !present {
		return err
	}
	state, err := LoadManagedTemplateState(featurePath)
	if err != nil {
		return err
	}
	if err := validateManagedTemplateOwnerForWorkspace(state, ws); err != nil {
		return err
	}
	roleState, ok := state.Roles[string(ManagedTemplateRoleOrchestrator)]
	if !ok {
		return nil
	}
	if err := validateManagedRoleAgentContext(
		feature, ManagedTemplateRoleOrchestrator, roleState, agentCommand,
	); err != nil {
		return err
	}
	for _, file := range roleState.Files {
		observation, err := observeManagedFile(featurePath, file.Path)
		if err != nil {
			return err
		}
		path := filepath.Join(featurePath, filepath.FromSlash(file.Path))
		if !observation.Exists {
			return fmt.Errorf("managed orchestrator source %s is missing; run 'tws template sync %s --scope orchestrator' first",
				path, feature)
		}
		if err := matchOwnedFile(observation, file); err != nil {
			return fmt.Errorf("managed orchestrator source %s is modified: %w", path, err)
		}
	}
	return nil
}

func validateManagedWorktreeContext(
	feature, featurePath string,
	roleState ManagedTemplateRoleState,
	cfg Config,
	into string,
	intoSet bool,
) error {
	currentCommand := cfg.GetAgentCommand()
	if err := validateManagedRoleAgentContext(
		feature, ManagedTemplateRoleWorktree, roleState, currentCommand,
	); err != nil {
		return err
	}
	target := into
	if !intoSet {
		target = cfg.InjectInto
	}
	if target != "" && target != "." {
		if intoSet {
			return fmt.Errorf("managed worktree guidance requires repository-root injection; explicit --into %q is incompatible (use --into . for a one-run root override)", target)
		}
		return fmt.Errorf("managed worktree guidance requires repository-root injection; configured inject_into %q is incompatible (use --into . on tws inject for a one-run root override, or unset inject_into)", target)
	}
	for _, file := range roleState.Files {
		observation, err := observeManagedFile(InjectPath(featurePath), file.Path)
		if err != nil {
			return err
		}
		if !observation.Exists {
			return fmt.Errorf("managed worktree source %s is missing; run 'tws template sync %s --scope worktree' first",
				filepath.Join(InjectPath(featurePath), filepath.FromSlash(file.Path)), feature)
		}
		if err := matchOwnedFile(observation, file); err != nil {
			return fmt.Errorf("managed worktree source %s is modified: %w",
				filepath.Join(InjectPath(featurePath), filepath.FromSlash(file.Path)), err)
		}
	}
	return nil
}

func validateManagedOwnedSourceFiles(
	featurePath string,
	files []ManagedTemplateOwnedFile,
) error {
	for _, file := range files {
		source := filepath.Join(InjectPath(featurePath), filepath.FromSlash(file.Path))
		observation, err := observeManagedFile(InjectPath(featurePath), file.Path)
		if err != nil {
			return fmt.Errorf("validating managed worktree source %s: %w", source, err)
		}
		if !observation.Exists {
			return fmt.Errorf("managed worktree source %s is missing", source)
		}
		if err := matchOwnedFile(observation, file); err != nil {
			return fmt.Errorf("managed worktree source %s is modified: %w", source, err)
		}
	}
	return nil
}

func validateManagedRoleAgentContext(
	feature string,
	role ManagedTemplateRole,
	roleState ManagedTemplateRoleState,
	currentCommand string,
) error {
	if roleState.AgentExplicit {
		if currentCommand != roleState.AgentCommandAtSelect {
			return fmt.Errorf("saved %s rendering agent is %q from an explicit selection made while agent_command was %q, but agent_command is now %q; run 'tws template sync %s --scope %s --agent %s' to explicitly rerender",
				role, roleState.Agent, roleState.AgentCommandAtSelect, currentCommand,
				feature, role, roleState.Agent)
		}
	} else {
		currentAgent, err := ResolveManagedTemplateAgent(currentCommand)
		if err != nil {
			return err
		}
		if currentAgent != roleState.Agent {
			return fmt.Errorf("saved %s rendering agent is %q but configured agent resolves to %q; run 'tws template sync %s --scope %s --agent %s' to explicitly rerender",
				role, roleState.Agent, currentAgent, feature, role, currentAgent)
		}
	}
	return nil
}

func validateManagedDestination(role ManagedTemplateRole, path string) error {
	if err := validateTemplateAssetPath(path); err != nil {
		return err
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		switch strings.ToLower(part) {
		case ".git", ".tws":
			return fmt.Errorf("path %q targets TWS or Git control data", path)
		}
	}
	first := parts[0]
	lower := strings.ToLower(first)
	reserved := map[string]bool{
		".git": true, ".tws": true, "worktrees": true,
		managedTemplateStateName: true, managedTemplateLockName: true,
		managedTemplateRecoveryName: true,
		"stack.yaml":                true, "decisions.yaml": true,
		"read-state.yaml": true, ".sync-state.v2.yaml": true,
		".sync-run.lock": true, ".sessions": true,
		".session-intents": true, ".tws-workspace": true,
	}
	if reserved[lower] {
		return fmt.Errorf("path %q targets TWS or Git control data", path)
	}
	if strings.HasPrefix(lower, ".sync-") || strings.HasPrefix(lower, ".reparent") {
		return fmt.Errorf("path %q targets TWS runtime state", path)
	}
	if role == ManagedTemplateRoleOrchestrator && lower == InjectDir {
		return fmt.Errorf("orchestrator asset %q cannot target inject/", path)
	}
	return nil
}

func observeManagedFile(rootPath, relative string) (managedFileObservation, error) {
	if err := validateTemplateAssetPath(relative); err != nil {
		return managedFileObservation{}, err
	}
	info, err := os.Lstat(rootPath)
	if errors.Is(err, fs.ErrNotExist) {
		return managedFileObservation{}, nil
	}
	if err != nil {
		return managedFileObservation{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return managedFileObservation{}, fmt.Errorf("root %s is not a regular directory", rootPath)
	}
	if err := rejectManagedParentSymlinks(rootPath, relative); err != nil {
		return managedFileObservation{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return managedFileObservation{}, err
	}
	defer root.Close() //nolint:errcheck
	info, err = root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return managedFileObservation{}, nil
	}
	if err != nil {
		return managedFileObservation{}, err
	}
	kind := managedFileKind(info.Mode())
	identity, err := managedIdentityFromInfo(info)
	if err != nil {
		return managedFileObservation{}, err
	}
	if kind != "file" {
		return managedFileObservation{
			Exists: true, Kind: kind, Device: identity.Device, Inode: identity.Inode,
		}, nil
	}
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return managedFileObservation{}, err
	}
	defer file.Close() //nolint:errcheck
	before, err := file.Stat()
	if err != nil {
		return managedFileObservation{}, err
	}
	beforeIdentity, err := managedIdentityFromInfo(before)
	if err != nil {
		return managedFileObservation{}, err
	}
	if beforeIdentity != identity || !before.Mode().IsRegular() {
		return managedFileObservation{}, errors.New("regular file identity changed while opening")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return managedFileObservation{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return managedFileObservation{}, err
	}
	afterIdentity, err := managedIdentityFromInfo(after)
	if err != nil {
		return managedFileObservation{}, err
	}
	if afterIdentity != beforeIdentity || after.Mode().Perm() != before.Mode().Perm() ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return managedFileObservation{}, errors.New("regular file changed while hashing")
	}
	return managedFileObservation{
		Exists: true, Kind: "file", Hash: digestBytes(data), Mode: uint32(before.Mode().Perm()),
		Device: identity.Device, Inode: identity.Inode,
	}, nil
}

func observeManagedLink(worktreeRoot, relative string) (managedLinkObservation, error) {
	if err := validateTemplateAssetPath(relative); err != nil {
		return managedLinkObservation{}, err
	}
	if err := rejectManagedParentSymlinks(worktreeRoot, relative); err != nil {
		return managedLinkObservation{}, err
	}
	tracked, err := managedGitTracked(worktreeRoot, relative)
	if err != nil {
		return managedLinkObservation{}, err
	}
	root, err := os.OpenRoot(worktreeRoot)
	if err != nil {
		return managedLinkObservation{}, err
	}
	defer root.Close() //nolint:errcheck
	info, err := root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return managedLinkObservation{Tracked: tracked}, nil
	}
	if err != nil {
		return managedLinkObservation{}, err
	}
	kind := managedFileKind(info.Mode())
	observation := managedLinkObservation{Exists: true, Kind: kind, Tracked: tracked}
	identity, err := managedIdentityFromInfo(info)
	if err != nil {
		return managedLinkObservation{}, err
	}
	observation.Device = identity.Device
	observation.Inode = identity.Inode
	if kind == "symlink" {
		target, err := root.Readlink(filepath.FromSlash(relative))
		if err != nil {
			return managedLinkObservation{}, err
		}
		observation.Target = target
	}
	return observation, nil
}

func matchOwnedLink(
	observation managedLinkObservation,
	owned ManagedTemplateOwnedLink,
	worktree managedWorktreeIdentity,
	source, target string,
) error {
	if observation.Kind != "symlink" {
		return fmt.Errorf("path type is %s, not a symbolic link", observation.Kind)
	}
	if observation.Target != owned.Target || owned.Target != target {
		return errors.New("symbolic link target changed")
	}
	if observation.Device != owned.Device || observation.Inode != owned.Inode {
		return errors.New("symbolic link creation identity changed")
	}
	if owned.Source != source || owned.Path != source {
		return errors.New("symbolic link source mapping changed")
	}
	return matchOwnedWorktreeIdentity(owned, worktree)
}

func matchOwnedWorktreeIdentity(
	owned ManagedTemplateOwnedLink,
	worktree managedWorktreeIdentity,
) error {
	if owned.WorktreeDevice != worktree.WorktreeDevice ||
		owned.WorktreeInode != worktree.WorktreeInode ||
		owned.GitDirDevice != worktree.GitDirDevice ||
		owned.GitDirInode != worktree.GitDirInode {
		return errors.New("Git worktree identity changed")
	}
	return nil
}

func managedGitTracked(worktreeRoot, relative string) (bool, error) {
	cmd := exec.Command("git", "-C", worktreeRoot, "--literal-pathspecs",
		"ls-files", "--error-unmatch", "--", filepath.ToSlash(relative))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = err.Error()
	}
	return false, fmt.Errorf("checking whether %s is tracked: %s", filepath.Join(worktreeRoot, filepath.FromSlash(relative)), message)
}

func managedFileKind(mode fs.FileMode) string {
	switch {
	case mode.IsRegular():
		return "file"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	default:
		return "special"
	}
}

func matchOwnedFile(observation managedFileObservation, owned ManagedTemplateOwnedFile) error {
	if observation.Kind != "file" {
		return fmt.Errorf("path type is %s, not a regular file", observation.Kind)
	}
	if observation.Hash != owned.SHA256 {
		return errors.New("content digest changed")
	}
	if observation.Mode != owned.Mode {
		return fmt.Errorf("mode changed from %04o to %04o", owned.Mode, observation.Mode)
	}
	return nil
}

func rejectManagedParentSymlinks(rootPath, relative string) error {
	info, err := os.Lstat(rootPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("root %s is not a regular directory", rootPath)
	}
	current := rootPath
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination parent is a symbolic link: %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination parent is not a directory: %s", current)
		}
	}
	return nil
}

func ApplyManagedTemplatePlan(plan *ManagedTemplatePlan) error {
	if plan == nil {
		return errors.New("managed template plan is nil")
	}
	if err := validateManagedOwnedSourceFiles(
		plan.Request.FeaturePath, plan.ownedSourceChecks,
	); err != nil {
		return err
	}
	for _, evidence := range plan.SourceEvidence {
		pkg, err := LoadTemplatePackage(evidence.Path)
		if err != nil {
			return fmt.Errorf("managed template source changed after planning: %w", err)
		}
		if templatePackageDigest(pkg) != evidence.Digest {
			return fmt.Errorf("managed template source %s changed after planning", evidence.Path)
		}
		if pkg.rootIdentity.Device != evidence.Device || pkg.rootIdentity.Inode != evidence.Inode {
			return fmt.Errorf("managed template source root identity changed after planning: %s", evidence.Path)
		}
	}
	for _, target := range plan.worktreeChecks {
		if err := verifyInjectWorktree(plan.Request.FeaturePath, target.Path); err != nil {
			return fmt.Errorf("worktree %q identity changed after planning: %w", target.Name, err)
		}
		marker := filepath.Join(target.Path, ".git")
		gitDir, err := resolveInjectGitDir(target.Path, marker)
		if err != nil {
			return fmt.Errorf("worktree %q identity changed after planning: %w", target.Name, err)
		}
		worktreeIdentity, err := managedPathIdentity(target.Path)
		if err != nil {
			return err
		}
		gitDirIdentity, err := managedPathIdentity(gitDir)
		if err != nil {
			return err
		}
		expected := plan.worktreeIdentities[target.Name]
		if worktreeIdentity.Device != expected.WorktreeDevice ||
			worktreeIdentity.Inode != expected.WorktreeInode ||
			gitDirIdentity.Device != expected.GitDirDevice ||
			gitDirIdentity.Inode != expected.GitDirInode {
			return fmt.Errorf("worktree %q exact filesystem or Git identity changed after planning", target.Name)
		}
	}
	if !plan.HasEffects() {
		return nil
	}
	return applyManagedTemplatePlanPreserved(plan)
}

func ManagedTemplatePlanRetiresWorktreeLinks(plan *ManagedTemplatePlan) bool {
	return plan != nil && len(plan.retiredLinks) > 0
}

func ApplyManagedTemplateRetirementAfterRemoval(plan *ManagedTemplatePlan) error {
	if plan == nil || plan.Request.RetireWorktree == "" {
		return errors.New("managed worktree retirement plan is required")
	}
	if !ManagedTemplatePlanRetiresWorktreeLinks(plan) {
		return nil
	}
	retiredPath, err := injectWorktreePath(plan.Request.FeaturePath, plan.Request.RetireWorktree)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(retiredPath); err == nil {
		return fmt.Errorf("retired worktree path reappeared before ownership publication: %s", retiredPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	frozen := *plan
	frozen.worktreeChecks = nil
	for _, target := range plan.worktreeChecks {
		if target.Name != plan.Request.RetireWorktree {
			frozen.worktreeChecks = append(frozen.worktreeChecks, target)
		}
	}
	frozen.worktreeIdentities = make(map[string]managedWorktreeIdentity, len(plan.worktreeIdentities))
	for name, identity := range plan.worktreeIdentities {
		if name != plan.Request.RetireWorktree {
			frozen.worktreeIdentities[name] = identity
		}
	}
	frozen.retiredLinks = nil
	return ApplyManagedTemplatePlan(&frozen)
}

func RestoreManagedTemplateRetirementAfterRollback(plan *ManagedTemplatePlan) error {
	if plan == nil || plan.Request.RetireWorktree == "" ||
		!ManagedTemplatePlanRetiresWorktreeLinks(plan) {
		return errors.New("prepared managed worktree retirement is required for rollback restoration")
	}
	state, rawState, err := loadManagedTemplateState(plan.Request.FeaturePath)
	if err != nil {
		return err
	}
	if digestBytes(rawState) != plan.StateBeforeDigest {
		return errors.New("managed ownership ledger changed after rename preparation")
	}
	stateBefore, err := observeManagedFile(plan.Request.FeaturePath, managedTemplateStateName)
	if err != nil {
		return err
	}
	if stateBefore != plan.StateBefore {
		return errors.New("managed ownership ledger filesystem identity changed after rename preparation")
	}
	worktreePath, err := injectWorktreePath(
		plan.Request.FeaturePath, plan.Request.RetireWorktree,
	)
	if err != nil {
		return err
	}
	if err := verifyInjectWorktree(plan.Request.FeaturePath, worktreePath); err != nil {
		return err
	}

	request := plan.Request
	request.RetireWorktree = ""
	restoration := &ManagedTemplatePlan{
		Request:            request,
		NewState:           cloneManagedTemplateState(state),
		StateChanged:       true,
		StateBeforeDigest:  plan.StateBeforeDigest,
		StateBefore:        stateBefore,
		AffectedWorktrees:  []string{plan.Request.RetireWorktree},
		SourceEvidence:     append([]ManagedTemplateSourceEvidence(nil), plan.SourceEvidence...),
		worktreeIdentities: make(map[string]managedWorktreeIdentity, len(plan.worktreeIdentities)),
		metadataIdentity:   plan.metadataIdentity,
		featureIdentity:    plan.featureIdentity,
	}
	for _, target := range plan.worktreeChecks {
		if target.Name == plan.Request.RetireWorktree {
			continue
		}
		restoration.worktreeChecks = append(restoration.worktreeChecks, target)
		restoration.worktreeIdentities[target.Name] = plan.worktreeIdentities[target.Name]
	}
	target := injectWorktreeTarget{Name: plan.Request.RetireWorktree, Path: worktreePath}
	if err := addManagedWorktreeEvidence(restoration, target); err != nil {
		return err
	}
	currentIdentity := restoration.worktreeIdentities[target.Name]
	role := restoration.NewState.Roles[string(ManagedTemplateRoleWorktree)]
	restoration.ownedSourceChecks = append(
		[]ManagedTemplateOwnedFile(nil), role.Files...,
	)
	if err := validateManagedOwnedSourceFiles(
		plan.Request.FeaturePath, restoration.ownedSourceChecks,
	); err != nil {
		return err
	}
	for _, retired := range plan.retiredLinks {
		observation, err := observeManagedLink(worktreePath, retired.owned.Path)
		if err != nil {
			return err
		}
		if observation.Exists {
			return fmt.Errorf("rollback worktree destination %s is no longer absent; refusing managed adoption",
				filepath.Join(worktreePath, filepath.FromSlash(retired.owned.Path)))
		}
		if observation.Tracked {
			return fmt.Errorf("rollback worktree destination %s is tracked by Git",
				filepath.Join(worktreePath, filepath.FromSlash(retired.owned.Path)))
		}
		destination := filepath.Join(worktreePath, filepath.FromSlash(retired.owned.Path))
		targetPath, err := filepath.Rel(
			filepath.Dir(destination),
			filepath.Join(InjectPath(plan.Request.FeaturePath), filepath.FromSlash(retired.owned.Source)),
		)
		if err != nil {
			return err
		}
		found := false
		for i := range role.Links {
			if role.Links[i].Worktree != retired.owned.Worktree ||
				role.Links[i].Path != retired.owned.Path {
				continue
			}
			if role.Links[i] != retired.owned {
				return fmt.Errorf("managed ownership for %s changed after rename preparation",
					retired.owned.Path)
			}
			role.Links[i].Target = targetPath
			role.Links[i].Device = 0
			role.Links[i].Inode = 0
			role.Links[i].WorktreeDevice = currentIdentity.WorktreeDevice
			role.Links[i].WorktreeInode = currentIdentity.WorktreeInode
			role.Links[i].GitDirDevice = currentIdentity.GitDirDevice
			role.Links[i].GitDirInode = currentIdentity.GitDirInode
			found = true
			break
		}
		if !found {
			return fmt.Errorf("prepared managed ownership for %s is missing from the active ledger",
				retired.owned.Path)
		}
		restoration.LinkWrites = append(restoration.LinkWrites, managedTemplateLinkWrite{
			Role: ManagedTemplateRoleWorktree, Worktree: target.Name,
			WorktreeRoot: worktreePath, Relative: retired.owned.Path,
			Source: retired.owned.Source, Target: targetPath, Before: observation,
		})
	}
	restoration.NewState.Roles[string(ManagedTemplateRoleWorktree)] = role
	normalizeManagedTemplatePlan(restoration)
	return ApplyManagedTemplatePlan(restoration)
}

func managedTemplateProgressError(completed, total int, cause error) error {
	remaining := total - completed
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Errorf("managed template apply failed after %d/%d effect(s); %d remaining: %w",
		completed, total, remaining, cause)
}

func marshalManagedTemplateState(state ManagedTemplateState) ([]byte, error) {
	normalizeManagedTemplateState(&state)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func cloneManagedTemplateState(state ManagedTemplateState) ManagedTemplateState {
	out := ManagedTemplateState{
		Version: state.Version, Owner: state.Owner,
		Roles: make(map[string]ManagedTemplateRoleState, len(state.Roles)),
	}
	for role, roleState := range state.Roles {
		roleState.Files = append([]ManagedTemplateOwnedFile(nil), roleState.Files...)
		roleState.Links = append([]ManagedTemplateOwnedLink(nil), roleState.Links...)
		out.Roles[role] = roleState
	}
	return out
}

func normalizeManagedTemplateState(state *ManagedTemplateState) {
	if state.Version == 0 {
		state.Version = managedTemplateStateVersion
	}
	if state.Roles == nil {
		state.Roles = make(map[string]ManagedTemplateRoleState)
	}
	for role, roleState := range state.Roles {
		sort.Slice(roleState.Files, func(i, j int) bool { return roleState.Files[i].Path < roleState.Files[j].Path })
		sort.Slice(roleState.Links, func(i, j int) bool {
			if roleState.Links[i].Worktree != roleState.Links[j].Worktree {
				return roleState.Links[i].Worktree < roleState.Links[j].Worktree
			}
			return roleState.Links[i].Path < roleState.Links[j].Path
		})
		state.Roles[role] = roleState
	}
}

func normalizeManagedTemplatePlan(plan *ManagedTemplatePlan) {
	normalizeManagedTemplateState(&plan.NewState)
	sort.Slice(plan.Removals, func(i, j int) bool {
		if plan.Removals[i].Kind != plan.Removals[j].Kind {
			return plan.Removals[i].Kind == "link"
		}
		return plan.Removals[i].Path < plan.Removals[j].Path
	})
	sort.Slice(plan.Writes, func(i, j int) bool {
		if plan.Writes[i].Role != plan.Writes[j].Role {
			return plan.Writes[i].Role < plan.Writes[j].Role
		}
		return plan.Writes[i].Relative < plan.Writes[j].Relative
	})
	sort.Slice(plan.LinkWrites, func(i, j int) bool {
		if plan.LinkWrites[i].Worktree != plan.LinkWrites[j].Worktree {
			return plan.LinkWrites[i].Worktree < plan.LinkWrites[j].Worktree
		}
		return plan.LinkWrites[i].Relative < plan.LinkWrites[j].Relative
	})
	sort.Slice(plan.retiredLinks, func(i, j int) bool {
		if plan.retiredLinks[i].owned.Worktree != plan.retiredLinks[j].owned.Worktree {
			return plan.retiredLinks[i].owned.Worktree < plan.retiredLinks[j].owned.Worktree
		}
		return plan.retiredLinks[i].owned.Path < plan.retiredLinks[j].owned.Path
	})
	sort.Strings(plan.AffectedWorktrees)
	sort.Strings(plan.WorktreeEvidence)
	sort.Slice(plan.SourceEvidence, func(i, j int) bool {
		return plan.SourceEvidence[i].Path < plan.SourceEvidence[j].Path
	})
}

func managedTemplatePlanFingerprint(plan *ManagedTemplatePlan) (string, error) {
	type fileFP struct {
		Role     ManagedTemplateRole    `json:"role"`
		Root     string                 `json:"root"`
		Relative string                 `json:"relative"`
		Hash     string                 `json:"hash"`
		Mode     uint32                 `json:"mode"`
		Before   managedFileObservation `json:"before"`
	}
	type linkFP struct {
		Worktree string                 `json:"worktree"`
		Root     string                 `json:"root"`
		Relative string                 `json:"relative"`
		Source   string                 `json:"source"`
		Target   string                 `json:"target"`
		Before   managedLinkObservation `json:"before"`
	}
	type retiredLinkFP struct {
		Worktree string                   `json:"worktree"`
		Root     string                   `json:"root"`
		Owned    ManagedTemplateOwnedLink `json:"owned"`
		Before   managedLinkObservation   `json:"before"`
	}
	doc := struct {
		Feature     string                          `json:"feature"`
		MetadataID  managedObjectIdentity           `json:"metadata_identity"`
		FeatureID   managedObjectIdentity           `json:"feature_identity"`
		StateDigest string                          `json:"state_digest"`
		Removals    []ManagedTemplateRemoval        `json:"removals"`
		Files       []fileFP                        `json:"files"`
		Links       []linkFP                        `json:"links"`
		Retired     []retiredLinkFP                 `json:"retired_links"`
		NewState    ManagedTemplateState            `json:"new_state"`
		Worktrees   []string                        `json:"worktrees"`
		Evidence    []string                        `json:"worktree_evidence"`
		Sources     []ManagedTemplateSourceEvidence `json:"source_evidence"`
	}{
		Feature: plan.Request.Feature, MetadataID: plan.metadataIdentity,
		FeatureID: plan.featureIdentity, StateDigest: plan.StateBeforeDigest,
		NewState: plan.NewState, Worktrees: plan.AffectedWorktrees,
		Evidence: plan.WorktreeEvidence, Sources: plan.SourceEvidence,
	}
	for _, removal := range plan.Removals {
		doc.Removals = append(doc.Removals, ManagedTemplateRemoval{
			Feature: removal.Feature, Role: removal.Role, Path: removal.Path, Kind: removal.Kind,
		})
	}
	for _, write := range plan.Writes {
		doc.Files = append(doc.Files, fileFP{
			Role: write.Role, Root: write.Root, Relative: write.Relative,
			Hash: digestBytes(write.Data), Mode: uint32(write.Mode.Perm()), Before: write.Before,
		})
	}
	for _, link := range plan.LinkWrites {
		doc.Links = append(doc.Links, linkFP{
			Worktree: link.Worktree, Root: link.WorktreeRoot, Relative: link.Relative,
			Source: link.Source, Target: link.Target, Before: link.Before,
		})
	}
	for _, retired := range plan.retiredLinks {
		doc.Retired = append(doc.Retired, retiredLinkFP{
			Worktree: retired.owned.Worktree,
			Root:     retired.worktreeRoot,
			Owned:    retired.owned,
			Before:   retired.before,
		})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func addManagedWorktreeEvidence(plan *ManagedTemplatePlan, target injectWorktreeTarget) error {
	if residue, err := managedRecoveryResidue(target.Path); err != nil {
		return err
	} else if residue != "" {
		return fmt.Errorf("managed template recovery evidence exists in worktree %q at %s; inspect and preserve it before retrying",
			target.Name, residue)
	}
	marker := filepath.Join(target.Path, ".git")
	gitDir, err := resolveInjectGitDir(target.Path, marker)
	if err != nil {
		return err
	}
	worktreeIdentity, err := managedPathIdentity(target.Path)
	if err != nil {
		return err
	}
	gitDirIdentity, err := managedPathIdentity(gitDir)
	if err != nil {
		return err
	}
	identity := managedWorktreeIdentity{
		WorktreeDevice: worktreeIdentity.Device,
		WorktreeInode:  worktreeIdentity.Inode,
		GitDirDevice:   gitDirIdentity.Device,
		GitDirInode:    gitDirIdentity.Inode,
	}
	plan.worktreeChecks = append(plan.worktreeChecks, target)
	plan.worktreeIdentities[target.Name] = identity
	plan.WorktreeEvidence = append(plan.WorktreeEvidence,
		fmt.Sprintf("%s\x00%s\x00%d:%d\x00%s\x00%d:%d",
			target.Name, canonicalize(target.Path),
			identity.WorktreeDevice, identity.WorktreeInode,
			canonicalize(gitDir), identity.GitDirDevice, identity.GitDirInode))
	return nil
}

func addManagedSourceEvidence(plan *ManagedTemplatePlan, pkg TemplatePackage) {
	for _, evidence := range plan.SourceEvidence {
		if evidence.Path == pkg.Root {
			return
		}
	}
	plan.SourceEvidence = append(plan.SourceEvidence, ManagedTemplateSourceEvidence{
		Path: pkg.Root, Digest: templatePackageDigest(pkg),
		Device: pkg.rootIdentity.Device, Inode: pkg.rootIdentity.Inode,
	})
}

func templatePackageDigest(pkg TemplatePackage) string {
	type file struct {
		Group string `json:"group"`
		Path  string `json:"path"`
		Hash  string `json:"hash"`
		Mode  uint32 `json:"mode"`
	}
	var files []file
	for _, group := range []struct {
		name  string
		files []TemplatePackageFile
	}{
		{name: "feature", files: pkg.Feature},
		{name: "inject", files: pkg.Inject},
		{name: "metadata", files: pkg.Metadata},
	} {
		for _, item := range group.files {
			files = append(files, file{
				Group: group.name, Path: item.Path,
				Hash: digestBytes(item.Data), Mode: uint32(item.Mode.Perm()),
			})
		}
	}
	data, _ := json.Marshal(files)
	return digestBytes(data)
}

func AcquireManagedTemplateLock(featurePath string) (*ManagedTemplateLock, error) {
	_, beforeErr := os.Lstat(featurePath)
	createdRoot := errors.Is(beforeErr, fs.ErrNotExist)
	if beforeErr != nil && !createdRoot {
		return nil, beforeErr
	}
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		return nil, err
	}
	lifecycle, err := AcquireManagedFeatureLifecycleLock(featurePath)
	if err != nil {
		return nil, err
	}
	lock, err := acquireManagedTemplateLockWithLifecycle(featurePath, lifecycle)
	if err != nil {
		return nil, errors.Join(err, lifecycle.Release())
	}
	lock.ownsLifecycle = true
	lock.createdRoot = createdRoot
	return lock, nil
}

func AcquireManagedTemplateLockWithLifecycle(
	featurePath string,
	lifecycle *ManagedFeatureLifecycleLock,
) (*ManagedTemplateLock, error) {
	if lifecycle == nil {
		return nil, errors.New("managed feature lifecycle lock is required")
	}
	return acquireManagedTemplateLockWithLifecycle(featurePath, lifecycle)
}

func acquireManagedTemplateLockWithLifecycle(
	featurePath string,
	lifecycle *ManagedFeatureLifecycleLock,
) (*ManagedTemplateLock, error) {
	info, err := os.Lstat(featurePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("managed template feature root %s is not a regular directory", featurePath)
	}
	if !sameResolvedRepoRootForManagedPath(featurePath, lifecycle.path) {
		return nil, fmt.Errorf("managed lifecycle lock belongs to %s, not %s", lifecycle.path, featurePath)
	}
	root, err := os.OpenRoot(featurePath)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(managedTemplateLockName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("opening managed template lock: %w", err)
	}
	if err := flockTryExclusive(file); err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("managed template writer is already active for %s: %w", featurePath, err)
	}
	rootIdentity, err := managedRootIdentity(root)
	if err != nil {
		_ = flockUnlock(file)
		_ = file.Close()
		_ = root.Close()
		return nil, err
	}
	if rootIdentity != lifecycle.identity {
		_ = flockUnlock(file)
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("managed feature root identity changed while acquiring writer lock")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = flockUnlock(file)
		_ = file.Close()
		_ = root.Close()
		return nil, err
	}
	return &ManagedTemplateLock{
		root: root, file: file, lifecycle: lifecycle,
		token: hex.EncodeToString(tokenBytes), rootIdentity: rootIdentity,
	}, nil
}

func AcquireManagedFeatureLifecycleLock(featurePath string) (*ManagedFeatureLifecycleLock, error) {
	info, err := os.Lstat(featurePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("managed lifecycle root %s is not a regular directory", featurePath)
	}
	dir, err := os.Open(featurePath)
	if err != nil {
		return nil, err
	}
	if err := flockTryExclusive(dir); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("feature lifecycle operation is already active for %s: %w", featurePath, err)
	}
	identity, err := managedIdentityFromInfo(info)
	if err != nil {
		_ = flockUnlock(dir)
		_ = dir.Close()
		return nil, err
	}
	return &ManagedFeatureLifecycleLock{
		path: featurePath, dir: dir, identity: identity,
	}, nil
}

func (lock *ManagedFeatureLifecycleLock) Identity() managedObjectIdentity {
	if lock == nil {
		return managedObjectIdentity{}
	}
	return lock.identity
}

func (lock *ManagedFeatureLifecycleLock) Release() error {
	if lock == nil {
		return nil
	}
	return errors.Join(flockUnlock(lock.dir), lock.dir.Close())
}

func sameResolvedRepoRootForManagedPath(a, b string) bool {
	clean := func(path string) string {
		if evaluated, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Clean(evaluated)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		return filepath.Clean(absolute)
	}
	return clean(a) == clean(b)
}

func (lock *ManagedTemplateLock) Token() string {
	if lock == nil {
		return ""
	}
	return lock.token
}

func (lock *ManagedTemplateLock) Release() error {
	if lock == nil {
		return nil
	}
	var lifecycleErr error
	if lock.ownsLifecycle {
		lifecycleErr = lock.lifecycle.Release()
	}
	return errors.Join(flockUnlock(lock.file), lock.file.Close(), lock.root.Close(), lifecycleErr)
}

func ManagedTemplateOwnedWorktreePaths(featurePath string) ([]string, error) {
	state, err := LoadManagedTemplateState(featurePath)
	if err != nil {
		return nil, err
	}
	role, ok := state.Roles[string(ManagedTemplateRoleWorktree)]
	if !ok {
		return nil, nil
	}
	paths := make([]string, 0, len(role.Files))
	for _, file := range role.Files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (plan *ManagedTemplatePlan) HasEffects() bool {
	return plan != nil && (len(plan.Removals) > 0 || len(plan.Writes) > 0 ||
		len(plan.LinkWrites) > 0 || plan.StateChanged)
}

func InjectFilesExcludingWouldChange(featurePath, worktreePath, injectInto string, excluded []string) (bool, error) {
	exclude := make(map[string]bool, len(excluded))
	for _, path := range excluded {
		exclude[filepath.ToSlash(path)] = true
	}
	injectDir := InjectPath(featurePath)
	info, err := os.Lstat(injectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("inject source %s is not a regular directory", injectDir)
	}
	targetBase := worktreePath
	if injectInto != "" && injectInto != "." {
		targetBase = filepath.Join(worktreePath, injectInto)
	}
	wouldChange := false
	err = filepath.Walk(injectDir, func(srcPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(injectDir, srcPath)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		if strings.Split(filepath.ToSlash(relPath), "/")[0] == managedTemplateRecoveryName {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && exclude[filepath.ToSlash(relPath)] {
			return nil
		}
		destPath := filepath.Join(targetBase, relPath)
		destInfo, err := os.Lstat(destPath)
		if errors.Is(err, fs.ErrNotExist) {
			wouldChange = true
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		if info.IsDir() && !destInfo.IsDir() {
			wouldChange = true
			return filepath.SkipAll
		}
		return nil
	})
	return wouldChange, err
}

func InjectFilesExcluding(featurePath, worktreePath, injectInto string, excluded []string) error {
	exclude := make(map[string]bool, len(excluded))
	for _, path := range excluded {
		exclude[filepath.ToSlash(path)] = true
	}
	injectDir := InjectPath(featurePath)
	if _, err := os.Stat(injectDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	targetBase := worktreePath
	if injectInto != "" && injectInto != "." {
		targetBase = filepath.Join(worktreePath, injectInto)
		if err := os.MkdirAll(targetBase, 0o755); err != nil {
			return err
		}
	}
	return filepath.Walk(injectDir, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(injectDir, srcPath)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		relSlash := filepath.ToSlash(relPath)
		if strings.Split(relSlash, "/")[0] == managedTemplateRecoveryName {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(targetBase, relPath), 0o755)
		}
		if exclude[relSlash] {
			return nil
		}
		destPath := filepath.Join(targetBase, relPath)
		if _, err := os.Lstat(destPath); err == nil {
			return nil
		}
		relTarget, err := filepath.Rel(filepath.Dir(destPath), srcPath)
		if err != nil {
			return err
		}
		return os.Symlink(relTarget, destPath)
	})
}

func ReplanManagedTemplate(plan *ManagedTemplatePlan) (*ManagedTemplatePlan, error) {
	if plan == nil {
		return nil, errors.New("managed template plan is nil")
	}
	return PlanManagedTemplate(plan.Request)
}

func ResolveManagedTemplateOwnerWorkspace(metadataRoot, candidateRepo string) (Workspace, bool, error) {
	root := cleanAbsolute(metadataRoot)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	var owner *ManagedTemplateOwner
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		featurePath := filepath.Join(root, entry.Name())
		present, err := ManagedTemplateStatePresent(featurePath)
		if err != nil {
			return Workspace{}, false, fmt.Errorf("%s: managed owner evidence is pending or unreadable: %w",
				entry.Name(), err)
		}
		if !present {
			continue
		}
		state, err := LoadManagedTemplateState(featurePath)
		if err != nil {
			return Workspace{}, false, fmt.Errorf("%s: managed owner state is unreadable or malformed: %w",
				entry.Name(), err)
		}
		if owner == nil {
			value := state.Owner
			owner = &value
			continue
		}
		if owner.GitDirDevice != state.Owner.GitDirDevice ||
			owner.GitDirInode != state.Owner.GitDirInode {
			return Workspace{}, false, fmt.Errorf(
				"external workspace %s contains managed features with conflicting owning repositories",
				root,
			)
		}
	}
	if owner == nil {
		return Workspace{}, false, nil
	}
	if candidateRepo != "" {
		candidate, candidateErr := managedTemplateOwnerForRepo(candidateRepo)
		if candidateErr == nil &&
			(candidate.GitDirDevice != owner.GitDirDevice || candidate.GitDirInode != owner.GitDirInode) &&
			owner.GitIdentity != "" && candidate.GitIdentity == owner.GitIdentity {
			return Workspace{}, false, fmt.Errorf(
				"managed external workspace %s belongs to repository %s; repository %s has the same auxiliary Git fingerprint but a different Git common-directory identity",
				root, owner.RepoRoot, candidate.RepoRoot,
			)
		}
	}
	candidates := []string{owner.RepoRoot}
	if candidateRepo != "" && filepath.Clean(candidateRepo) != filepath.Clean(owner.RepoRoot) {
		candidates = append(candidates, candidateRepo)
	}
	var evidenceErrs []error
	for _, candidate := range candidates {
		current, err := managedTemplateOwnerForRepo(candidate)
		if err != nil {
			evidenceErrs = append(evidenceErrs, fmt.Errorf("%s: %w", candidate, err))
			continue
		}
		if current.GitDirDevice != owner.GitDirDevice ||
			current.GitDirInode != owner.GitDirInode {
			evidenceErrs = append(evidenceErrs, fmt.Errorf("%s: Git common-directory identity does not match persisted evidence", candidate))
			continue
		}
		return Workspace{
			RepoRoot: current.RepoRoot, Mode: ModeExternal, MetadataRoot: canonicalize(root),
			StableID: stableID(current.RepoRoot), Caps: capsFor(ModeExternal),
		}, true, nil
	}
	return Workspace{}, false, fmt.Errorf(
		"managed owning repository identity cannot be verified: %w",
		errors.Join(evidenceErrs...),
	)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func managedLinkKey(worktree, path string) string {
	return worktree + "\x00" + path
}

func managedContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
