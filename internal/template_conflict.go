package internal

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// An override acknowledges one rendered proposal, not every future update.
// The enclosing ledger supplies feature ownership; role/path are checked again
// here so an acknowledgement cannot be moved to another owned asset.
type ManagedTemplateOverride struct {
	Action               string                `json:"action"`
	Role                 ManagedTemplateRole   `json:"role"`
	Path                 string                `json:"path"`
	IncomingSHA256       string                `json:"incoming_sha256"`
	IncomingMode         uint32                `json:"incoming_mode"`
	Source               ManagedTemplateSource `json:"source"`
	Agent                string                `json:"agent"`
	AgentExplicit        bool                  `json:"agent_explicit,omitempty"`
	AgentCommandAtSelect string                `json:"agent_command_at_selection,omitempty"`
}

func (file *ManagedTemplateOwnedFile) UnmarshalJSON(data []byte) error {
	var value struct {
		Path     string          `json:"path"`
		SHA256   string          `json:"sha256"`
		Mode     uint32          `json:"mode"`
		Override json.RawMessage `json:"override"`
	}
	if err := decodeManagedConflictJSON(data, &value); err != nil {
		return err
	}
	*file = ManagedTemplateOwnedFile{Path: value.Path, SHA256: value.SHA256, Mode: value.Mode}
	if len(value.Override) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(value.Override), []byte("null")) {
		return errors.New("override must be an acknowledgement object, not null")
	}
	type acknowledgement ManagedTemplateOverride
	var override struct {
		acknowledgement
		IncomingMode *uint32 `json:"incoming_mode"`
	}
	if err := decodeManagedConflictJSON(value.Override, &override); err != nil {
		return err
	}
	if override.IncomingMode == nil {
		return errors.New("override is missing incoming_mode")
	}
	result := ManagedTemplateOverride(override.acknowledgement)
	result.IncomingMode = *override.IncomingMode
	file.Override = &result
	return nil
}

func decodeManagedConflictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON in managed file metadata")
	}
	return nil
}

func validateManagedTemplateOverride(version int, role ManagedTemplateRole, state ManagedTemplateRoleState, file ManagedTemplateOwnedFile) error {
	override := file.Override
	if override == nil {
		return nil
	}
	if version != managedTemplateOverrideVersion {
		return errors.New("version 1 cannot carry an override acknowledgement")
	}
	if override.Action != "keep" && override.Action != "edit" {
		return fmt.Errorf("invalid override action %q", override.Action)
	}
	if override.Role != role || override.Path != file.Path {
		return errors.New("override role/path binding does not match its owned file")
	}
	if override.Source != state.Source || override.Agent != state.Agent ||
		override.AgentExplicit != state.AgentExplicit ||
		override.AgentCommandAtSelect != state.AgentCommandAtSelect {
		return errors.New("override source/renderer binding does not match its role selection")
	}
	if len(override.IncomingSHA256) != 64 || strings.ToLower(override.IncomingSHA256) != override.IncomingSHA256 {
		return errors.New("override has an invalid incoming digest")
	}
	if _, err := hex.DecodeString(override.IncomingSHA256); err != nil {
		return errors.New("override has an invalid incoming digest")
	}
	if override.IncomingMode > 0o777 || (override.Action == "edit" && file.Mode != override.IncomingMode) {
		return errors.New("override has an invalid incoming or edited mode")
	}
	if file.SHA256 == override.IncomingSHA256 && file.Mode == override.IncomingMode {
		return errors.New("canonical content cannot carry an override acknowledgement")
	}
	return nil
}

func cloneManagedTemplateOwnedFile(file ManagedTemplateOwnedFile) ManagedTemplateOwnedFile {
	if file.Override != nil {
		copy := *file.Override
		file.Override = &copy
	}
	return file
}

func managedTemplateOverrideMatches(override *ManagedTemplateOverride, source ManagedTemplateSource, agent managedTemplateAgentSelection, incoming ManagedTemplateOwnedFile) bool {
	return override.Source == source && override.Agent == agent.Agent &&
		override.AgentExplicit == agent.Explicit && override.AgentCommandAtSelect == agent.AgentCommand &&
		override.IncomingSHA256 == incoming.SHA256 && override.IncomingMode == incoming.Mode
}

type ManagedTemplateConflict struct {
	Feature        string                `json:"feature"`
	Role           ManagedTemplateRole   `json:"role"`
	Path           string                `json:"path"`
	Destination    string                `json:"destination"`
	Source         ManagedTemplateSource `json:"source"`
	Agent          string                `json:"agent"`
	CurrentSHA256  string                `json:"current_sha256"`
	IncomingSHA256 string                `json:"incoming_sha256"`
	CurrentMode    uint32                `json:"current_mode"`
	IncomingMode   uint32                `json:"incoming_mode"`
	Reason         string                `json:"reason"`

	current  []byte
	incoming []byte
	before   managedFileObservation
	root     string
}

func (conflict ManagedTemplateConflict) CurrentContent() []byte {
	return append([]byte(nil), conflict.current...)
}

func (conflict ManagedTemplateConflict) IncomingContent() []byte {
	return append([]byte(nil), conflict.incoming...)
}

type managedTemplateDecision struct {
	Role   ManagedTemplateRole `json:"role"`
	Path   string              `json:"path"`
	Action string              `json:"action"`
	SHA256 string              `json:"sha256"`
	Mode   uint32              `json:"mode"`
	data   []byte
}

type managedTemplateDirectoryCheck struct {
	Path     string                `json:"path"`
	Identity managedObjectIdentity `json:"identity"`
}

type managedTemplateFileCheck struct {
	Role     ManagedTemplateRole             `json:"role"`
	Root     string                          `json:"root"`
	Relative string                          `json:"relative"`
	Before   managedFileObservation          `json:"before"`
	Parents  []managedTemplateDirectoryCheck `json:"parents"`
}

func newManagedTemplateFileCheck(role ManagedTemplateRole, root, relative string, before managedFileObservation) (managedTemplateFileCheck, error) {
	check := managedTemplateFileCheck{Role: role, Root: root, Relative: relative, Before: before}
	dir := root
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for i := 0; i < len(parts); i++ {
		identity, err := managedDirectoryIdentity(dir)
		if err != nil {
			return managedTemplateFileCheck{}, err
		}
		check.Parents = append(check.Parents, managedTemplateDirectoryCheck{Path: dir, Identity: identity})
		dir = filepath.Join(dir, parts[i])
	}
	return check, nil
}

func newManagedTemplateConflict(plan *ManagedTemplatePlan, role ManagedTemplateRole, root string, file managedDesiredFile, before managedFileObservation, source ManagedTemplateSource, agent managedTemplateAgentSelection) (ManagedTemplateConflict, error) {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return ManagedTemplateConflict{}, err
	}
	defer opened.Close() //nolint:errcheck
	current, err := readManagedConflictFile(opened, filepath.FromSlash(file.Path), before)
	if err != nil {
		return ManagedTemplateConflict{}, err
	}
	after, err := observeManagedFile(root, file.Path)
	if err != nil {
		return ManagedTemplateConflict{}, err
	}
	if before != after || digestBytes(current) != before.Hash {
		return ManagedTemplateConflict{}, errors.New("owned file changed while collecting conflict evidence")
	}
	if !managedTemplateText(current) || !managedTemplateText(file.Data) {
		return ManagedTemplateConflict{}, errors.New("only regular UTF-8 text without NUL is eligible; binary content is a hard conflict")
	}
	return ManagedTemplateConflict{
		Feature: plan.Request.Feature, Role: role, Path: file.Path,
		Destination: filepath.Join(root, filepath.FromSlash(file.Path)),
		Source:      source, Agent: agent.Agent, CurrentSHA256: before.Hash,
		IncomingSHA256: digestBytes(file.Data), CurrentMode: before.Mode, IncomingMode: uint32(file.Mode.Perm()),
		current: current, incoming: append([]byte(nil), file.Data...), before: before, root: root,
	}, nil
}

func managedTemplateText(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, '\x00')
}

func (plan *ManagedTemplatePlan) PendingConflicts() []ManagedTemplateConflict {
	var pending []ManagedTemplateConflict
	if plan == nil {
		return pending
	}
	for _, conflict := range plan.conflicts {
		resolved := false
		for _, decision := range plan.decisions {
			if decision.Role == conflict.Role && decision.Path == conflict.Path {
				resolved = true
				break
			}
		}
		if !resolved {
			copy := conflict
			copy.current = conflict.CurrentContent()
			copy.incoming = conflict.IncomingContent()
			pending = append(pending, copy)
		}
	}
	return pending
}

// ValidateManagedTemplateConflictContent never rewrites accepted native bytes.
func ValidateManagedTemplateConflictContent(conflict ManagedTemplateConflict, data []byte) error {
	if !managedTemplateText(data) {
		return errors.New("resolved content must be valid UTF-8 text without NUL")
	}
	if conflict.Role != ManagedTemplateRoleWorktree || conflict.Agent != "copilot" ||
		conflict.Path != managedNativePath(conflict.Role, conflict.Agent) {
		return nil
	}
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return errors.New("native Copilot worktree instructions require valid applyTo frontmatter")
	}
	rest := normalized[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 || (len(rest) > end+4 && rest[end+4] != '\n') {
		return errors.New("native Copilot worktree instructions require closed applyTo frontmatter")
	}
	var frontmatter map[string]any
	if err := yaml.Unmarshal(rest[:end], &frontmatter); err != nil {
		return fmt.Errorf("invalid Copilot applyTo frontmatter: %w", err)
	}
	applyTo, ok := frontmatter["applyTo"].(string)
	if !ok || strings.TrimSpace(applyTo) == "" {
		return errors.New("native Copilot worktree instructions require a nonempty string applyTo in frontmatter")
	}
	return nil
}

func ResolveManagedTemplateConflict(plan *ManagedTemplatePlan, role ManagedTemplateRole, path, action string, edited []byte) error {
	if plan == nil || !plan.Request.ResolveConflicts {
		return errors.New("managed template conflict decisions require explicit resolver planning")
	}
	var selected *ManagedTemplateConflict
	for _, conflict := range plan.PendingConflicts() {
		if conflict.Role == role && conflict.Path == path {
			selected = &conflict
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("no unresolved managed conflict for %s/%q", role, path)
	}
	conflict := *selected
	mode := conflict.IncomingMode
	var data []byte
	switch action {
	case "keep":
		data, mode = conflict.current, conflict.CurrentMode
	case "replace":
		data = conflict.incoming
	case "edit":
		data = edited
	default:
		return fmt.Errorf("invalid managed conflict decision %q", action)
	}
	if err := ValidateManagedTemplateConflictContent(conflict, data); err != nil {
		return err
	}
	hash := digestBytes(data)
	next := cloneManagedTemplateState(plan.NewState)
	newRole := next.Roles[string(role)]
	for i := range newRole.Files {
		if newRole.Files[i].Path != path {
			continue
		}
		file := &newRole.Files[i]
		file.SHA256, file.Mode, file.Override = hash, mode, nil
		if hash != conflict.IncomingSHA256 || mode != conflict.IncomingMode {
			file.Override = &ManagedTemplateOverride{
				Action: action, Role: role, Path: path,
				IncomingSHA256: conflict.IncomingSHA256, IncomingMode: conflict.IncomingMode,
				Source: newRole.Source, Agent: newRole.Agent, AgentExplicit: newRole.AgentExplicit,
				AgentCommandAtSelect: newRole.AgentCommandAtSelect,
			}
			next.Version = managedTemplateOverrideVersion
		}
		break
	}
	next.Roles[string(role)] = newRole
	if err := validateManagedTemplateState(next, true); err != nil {
		return err
	}
	plan.NewState = next
	if hash != conflict.CurrentSHA256 || mode != conflict.CurrentMode {
		plan.Writes = append(plan.Writes, managedTemplateWrite{
			Role: role, Root: conflict.root, Relative: path,
			Data: append([]byte(nil), data...), Mode: fs.FileMode(mode), Before: conflict.before,
		})
	}
	plan.decisions = append(plan.decisions, managedTemplateDecision{
		Role: role, Path: path, Action: action, SHA256: hash, Mode: mode,
		data: append([]byte(nil), data...),
	})
	normalizeManagedTemplatePlan(plan)
	stateBytes, err := marshalManagedTemplateState(plan.NewState)
	if err != nil {
		return err
	}
	plan.StateChanged = digestBytes(stateBytes) != plan.StateBeforeDigest || len(plan.LinkWrites) > 0
	plan.Fingerprint, err = managedTemplatePlanFingerprint(plan)
	return err
}

func revalidateManagedTemplateFileChecks(plan *ManagedTemplatePlan, featureRoot *os.Root, unwrittenOnly bool) error {
	for _, check := range plan.fileChecks {
		if unwrittenOnly && managedTemplateFileIsWritten(plan, check.Role, check.Relative) {
			continue
		}
		if plan.featureIdentity != (managedObjectIdentity{}) {
			if err := managedVerifyPathIdentity(plan.Request.FeaturePath, plan.featureIdentity); err != nil {
				return err
			}
		}
		if plan.metadataIdentity != (managedObjectIdentity{}) {
			if err := managedVerifyPathIdentity(plan.Request.Workspace.MetadataRoot, plan.metadataIdentity); err != nil {
				return err
			}
		}
		for _, parent := range check.Parents {
			got, err := managedDirectoryIdentity(parent.Path)
			if err != nil {
				return err
			}
			if got != parent.Identity {
				return fmt.Errorf("managed destination parent identity changed at %s", parent.Path)
			}
		}
		var got managedFileObservation
		var err error
		if featureRoot == nil {
			got, err = observeManagedFile(check.Root, check.Relative)
		} else {
			_, err = readManagedConflictFile(featureRoot, managedFeatureRelative(check.Role, check.Relative), check.Before)
			got = check.Before
		}
		if err != nil {
			return err
		}
		if got != check.Before {
			return fmt.Errorf("managed observed file changed after consent at %s", filepath.Join(check.Root, filepath.FromSlash(check.Relative)))
		}
	}
	return nil
}

func managedTemplateFileIsWritten(plan *ManagedTemplatePlan, role ManagedTemplateRole, path string) bool {
	for _, write := range plan.Writes {
		if write.Role == role && write.Relative == path {
			return true
		}
	}
	for _, removal := range plan.Removals {
		if removal.Kind == "file" && removal.Role == role && removal.relative == path {
			return true
		}
	}
	return false
}

// This read-only admission check precedes human interaction. Execution still
// takes and rechecks the existing reservations after every decision is made.
func PreflightManagedTemplateMutation(plan *ManagedTemplatePlan) error {
	if plan == nil {
		return errors.New("managed template plan is nil")
	}
	ws := plan.Request.Workspace
	if ws.Mode == ModeCheckout {
		stateDir := ws.CheckoutStateDir()
		lock, _, err := ReadCheckoutMutationLock(stateDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("invalid checkout mutation lock: %w", err)
		}
		if err == nil {
			if isProcessAlive(lock.PID) {
				return fmt.Errorf("checkout mutation lock held by live %s for feature %q", lock.Operation, lock.Feature)
			}
			present, err := checkoutMutationOwnerStateExists(stateDir, lock)
			if err != nil {
				return err
			}
			if present {
				return errors.New("checkout mutation lock belongs to recoverable state")
			}
		}
		return verifyCheckoutMutationReservation(stateDir, filepath.Join(plan.Request.FeaturePath, ".tws-template-operation"), nil, nil)
	}
	if err := RefuseSyncIfReparentActive(ReparentLocationFor(ws, plan.Request.Feature, plan.Request.FeaturePath)); err != nil {
		return err
	}
	state := ClassifyExternalSyncState(plan.Request.FeaturePath, SyncClassifyOpts{AlwaysReadGuard: true})
	if state.GuardErr != nil {
		return state.GuardErr
	}
	if state.Guard != nil && isProcessAlive(state.Guard.PID) {
		return errors.New("active external mutation guard blocks managed template changes")
	}
	if state.Cell != 1 {
		return fmt.Errorf("external sync recovery evidence at %s or %s blocks managed template changes", state.LegacyPath, state.PayloadPath)
	}
	return nil
}
