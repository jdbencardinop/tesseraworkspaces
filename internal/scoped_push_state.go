package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const ScopedPushStateVersion = 1
const scopedPushStateKind = "tws-scoped-publication"

// ScopedPushCheckpoint is a test-only fault/process-loss seam. Production
// cannot enable it with a flag, configuration setting, or environment variable.
var ScopedPushCheckpoint func(step string, index int) error

func scopedPushCheckpoint(step string, index int) error {
	if ScopedPushCheckpoint != nil {
		return ScopedPushCheckpoint(step, index)
	}
	return nil
}

type ScopedPushDirectory struct {
	Path     string                  `json:"path"`
	Spelling string                  `json:"spelling"`
	Identity managedObjectIdentity   `json:"identity"`
	Alias    *featureRenameRootAlias `json:"alias,omitempty"`
}

func scopedPushDirectoryAt(path string) (ScopedPushDirectory, error) {
	spelling, err := filepath.Abs(path)
	if err != nil {
		return ScopedPushDirectory{}, err
	}
	directory, alias, err := resolveFeatureRenameRoot(spelling)
	if err != nil {
		return ScopedPushDirectory{}, err
	}
	return ScopedPushDirectory{Path: directory.Path, Spelling: spelling, Identity: directory.Identity, Alias: alias}, nil
}

func (d ScopedPushDirectory) valid() bool {
	if !filepath.IsAbs(d.Path) || filepath.Clean(d.Path) != d.Path ||
		!filepath.IsAbs(d.Spelling) || filepath.Clean(d.Spelling) != d.Spelling ||
		d.Identity.Inode == 0 {
		return false
	}
	if d.Alias != nil {
		if d.Alias.Spelling != d.Spelling || d.Alias.Directory.Path != d.Path ||
			d.Alias.Directory.Identity != d.Identity || len(d.Alias.Links) == 0 {
			return false
		}
		for _, link := range d.Alias.Links {
			if !filepath.IsAbs(link.Path) || !filepath.IsAbs(link.Resolved) || link.Target == "" || link.Identity.Inode == 0 {
				return false
			}
		}
	}
	return true
}

func (d ScopedPushDirectory) sameBinding(other ScopedPushDirectory) bool {
	return d.Path == other.Path && d.Spelling == other.Spelling && d.Identity == other.Identity
}

func (d ScopedPushDirectory) verify() error {
	if err := d.Alias.verify(); err != nil {
		return err
	}
	got, err := scopedPushDirectoryAt(d.Spelling)
	if err != nil {
		return err
	}
	if !got.sameBinding(d) || (got.Alias == nil) != (d.Alias == nil) {
		return fmt.Errorf("scoped push directory identity changed at %s", d.Spelling)
	}
	return nil
}

type ScopedPushLocation struct {
	Mode          WorkspaceMode
	Feature       string
	FeaturePath   string
	MetadataRoot  string
	SelectionRoot string
}

func (loc ScopedPushLocation) ReparentLocation() ReparentLocation {
	return ReparentLocation{
		Mode: loc.Mode, Feature: loc.Feature, FeaturePath: loc.FeaturePath,
		CheckoutStateDir: filepath.Join(loc.MetadataRoot, "state"),
	}
}

func ScopedPushStatePath(loc ScopedPushLocation) string {
	if loc.Mode == ModeCheckout {
		return filepath.Join(loc.MetadataRoot, "state", loc.Feature+"-push.v1.json")
	}
	return filepath.Join(loc.FeaturePath, ".push-state.v1.json")
}

func LoadScopedPushStack(featurePath string) (Stack, error) {
	file, err := scopedPushReadFile(StackPath(featurePath))
	if err != nil {
		return Stack{}, err
	}
	var stack Stack
	if err := yaml.Unmarshal(file.data, &stack); err != nil {
		return Stack{}, err
	}
	return stack, nil
}

type ScopedPushEntry struct {
	Name        string               `json:"name"`
	GitBranch   string               `json:"git_branch"`
	RepoToken   string               `json:"repo_token"`
	Base        string               `json:"base"`
	Archived    bool                 `json:"archived"`
	Repository  ScopedPushRepository `json:"repository"`
	Source      string               `json:"source"`
	Destination string               `json:"destination"`
	TrackingRef string               `json:"tracking_ref"`
	Expected    string               `json:"expected"`
	Endpoint    string               `json:"endpoint_fingerprint"`
	Inclusion   *ScopedPushInclusion `json:"inclusion,omitempty"`
	Followup    *ReparentRemoteEntry `json:"reparent_followup,omitempty"`
}

type ScopedPushPin struct {
	Entry int    `json:"entry"`
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	OID   string `json:"oid"`
}

type ScopedPushPlan struct {
	Token          string               `json:"token"`
	Created        string               `json:"created"`
	Mode           WorkspaceMode        `json:"mode"`
	Feature        string               `json:"feature"`
	Root           ScopedPushDirectory  `json:"root"`
	FeatureDir     ScopedPushDirectory  `json:"feature_directory"`
	SelectionRoot  string               `json:"selection_root"`
	Selection      *ScopedPushDirectory `json:"selection_binding,omitempty"`
	StateDirectory ScopedPushDirectory  `json:"state_directory"`
	Scope          SyncScopeKind        `json:"scope"`
	Selector       string               `json:"selector"`
	Entries        []ScopedPushEntry    `json:"entries"`
	Pins           []ScopedPushPin      `json:"pins"`
}

type ScopedPushState struct {
	Kind              string            `json:"kind"`
	Version           int               `json:"version"`
	Revision          uint64            `json:"revision"`
	Plan              ScopedPushPlan    `json:"plan"`
	PlanHash          string            `json:"plan_hash"`
	Phase             string            `json:"phase"`
	Rows              []string          `json:"rows"`
	Pins              []string          `json:"pins"`
	FollowupDecisions map[string]string `json:"followup_decisions,omitempty"`
}

func (s *ScopedPushState) Terminal() bool {
	return s != nil && (s.Phase == "completed" || s.Phase == "canceled")
}

func scopedPushPlanHash(plan ScopedPushPlan) string {
	data, _ := json.Marshal(plan)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func scopedPushPinRef(token string, entry int, kind string) string {
	return fmt.Sprintf("refs/tws/push/%s/%d/%s", token, entry, kind)
}

func (s *ScopedPushState) validate() error {
	bad := func(detail string) error { return fmt.Errorf("invalid scoped push state: %s", detail) }
	p := s.Plan
	if s.Kind != scopedPushStateKind || s.Version != ScopedPushStateVersion {
		return bad("unsupported kind/version")
	}
	if s.Revision == 0 || !markerIDRegexp.MatchString(p.Token) || s.PlanHash != scopedPushPlanHash(p) {
		return bad("invalid token, revision, or frozen-plan digest")
	}
	if _, err := time.Parse(time.RFC3339, p.Created); err != nil {
		return bad("invalid creation time")
	}
	if err := validateFeatureName(p.Feature); err != nil {
		return bad("invalid feature")
	}
	if p.Mode != ModeExternal && p.Mode != ModeCheckout {
		return bad("invalid workspace mode")
	}
	if !p.Root.valid() || !p.FeatureDir.valid() || !p.StateDirectory.valid() {
		return bad("invalid physical directory binding")
	}
	if p.Mode == ModeExternal {
		if p.FeatureDir.Path != filepath.Join(p.Root.Path, p.Feature) || p.StateDirectory.Path != p.FeatureDir.Path {
			return bad("external state does not belong to the selected feature")
		}
	} else if (p.FeatureDir.Path != filepath.Join(p.Root.Path, "features", p.Feature) &&
		p.FeatureDir.Path != filepath.Join(p.Root.Path, p.Feature)) ||
		p.StateDirectory.Path != filepath.Join(p.Root.Path, "state") {
		return bad("checkout state does not belong to the workspace")
	}
	if p.SelectionRoot != "" && (!filepath.IsAbs(p.SelectionRoot) || filepath.Clean(p.SelectionRoot) != p.SelectionRoot) {
		return bad("invalid selected root")
	}
	if p.Selection != nil && (!p.Selection.valid() || p.Selection.Spelling != p.SelectionRoot) {
		return bad("invalid selected-root binding")
	}
	if p.Scope != SyncScopeOne && p.Scope != SyncScopeSubtree || p.Selector == "" {
		return bad("a standalone plan must have an explicit logical selection")
	}
	if len(p.Entries) == 0 || len(s.Rows) != len(p.Entries) || len(s.Pins) != len(p.Pins) {
		return bad("incomplete ordered outcomes")
	}
	switch s.Phase {
	case "preparing", "publishing", "cleaning", "canceling", "completed", "canceled":
	default:
		return bad("unknown phase")
	}
	names, branches := map[string]bool{}, map[string]bool{}
	selector, unfinished := false, false
	for i, entry := range p.Entries {
		if _, err := ScopedPushWorktreePath(p.FeatureDir.Path, entry.Name); err != nil {
			return bad("unsafe logical entry spelling")
		}
		if entry.Name == "" || strings.ContainsAny(entry.Name, "\x00\r\n") || names[entry.Name] ||
			!scopedPushValidRef("refs/heads/"+entry.GitBranch) || branches[entry.GitBranch] {
			return bad("invalid or duplicate logical/Git entry")
		}
		names[entry.Name], branches[entry.GitBranch] = true, true
		if entry.Name == p.Selector {
			selector = true
			if entry.Archived {
				return bad("the selector cannot be archived")
			}
		}
		if p.Mode == ModeCheckout && entry.RepoToken != "" {
			return bad("checkout publication is single-repository")
		}
		if entry.Archived {
			if s.Rows[i] != "skipped" || entry.Source != "" || entry.Expected != "" || entry.Endpoint != "" || entry.Inclusion != nil || entry.Followup != nil {
				return bad("archived entry has publication authority")
			}
			continue
		}
		if !entry.Repository.Directory.valid() || !entry.Repository.Common.valid() ||
			!validReparentRemoteOID(entry.Source) || entry.Destination != "refs/heads/"+entry.GitBranch ||
			!scopedPushValidRef(entry.TrackingRef) || !strings.HasPrefix(entry.TrackingRef, "refs/remotes/origin/") ||
			len(entry.Endpoint) != 64 || !stackStatusObjectID.MatchString(entry.Endpoint) ||
			(entry.Expected != "" && (!validReparentRemoteOID(entry.Expected) || len(entry.Expected) != len(entry.Source))) {
			return bad("invalid source, destination, endpoint, repository, or lease")
		}
		if entry.Followup != nil {
			if entry.Inclusion == nil {
				return bad("published reparent follow-up lacks frozen inclusion authority")
			}
			followup := ReparentRemoteRecord{
				RecordVersion: ReparentRemoteRecordVersion, Feature: p.Feature,
				RunID: p.Token, CreatedAt: p.Created, Entries: []ReparentRemoteEntry{*entry.Followup},
			}
			if err := validateReparentRemoteRecord(ReparentLocation{Feature: p.Feature}, followup); err != nil ||
				entry.Followup.Name != entry.Name || entry.Followup.GitBranch != entry.GitBranch || entry.Followup.Repo != entry.RepoToken ||
				len(entry.Followup.NewTipSHA) != len(entry.Source) || len(entry.Followup.RemoteSHAAtWrite) != len(entry.Source) {
				return bad("invalid frozen reparent follow-up")
			}
		}
		if proof := entry.Inclusion; proof != nil {
			if entry.Followup == nil || entry.Expected == "" || proof.TrackingTime <= 0 ||
				len(proof.Tips) == 0 || proof.Tips[0].OID != entry.Source || !validReparentRemoteOID(proof.Witness) ||
				len(proof.Witness) != len(entry.Source) {
				return bad("incomplete inclusion witness")
			}
			witness := false
			for j, tip := range proof.Tips {
				if !validReparentRemoteOID(tip.OID) || len(tip.OID) != len(entry.Source) ||
					tip.Time <= 0 || (tip.Time < proof.TrackingTime && j != len(proof.Tips)-1) {
					return bad("inclusion witness extends beyond the reflog cutoff")
				}
				witness = witness || tip.OID == proof.Witness
			}
			if !witness {
				return bad("inclusion witness is outside its qualifying reflog range")
			}
		}
		switch s.Rows[i] {
		case "succeeded":
			if unfinished || s.Phase == "preparing" {
				return bad("success is out of publication order")
			}
		case "intent":
			if unfinished || s.Phase == "preparing" {
				return bad("intent is out of publication order")
			}
			unfinished = true
		case "pending":
			unfinished = true
		default:
			return bad("unknown publication outcome")
		}
	}
	if !selector || (p.Scope == SyncScopeOne && len(p.Entries) != 1) ||
		((s.Phase == "cleaning" || s.Phase == "completed") && unfinished) {
		return bad("scope or terminal outcomes are inconsistent")
	}
	frozen := Stack{}
	for _, entry := range p.Entries {
		frozen.Branches = append(frozen.Branches, StackEntry{
			Name: entry.Name, Branch: entry.GitBranch, Repo: entry.RepoToken,
			Base: entry.Base, Archived: entry.Archived,
		})
	}
	selection, err := ResolveSyncSelection(frozen, SyncRunPolicy{ScopeKind: p.Scope, Selector: p.Selector},
		SyncSelectionOpts{Mode: p.Mode, NewMode: true, Feature: p.Feature})
	if err != nil || len(selection.Entries) != len(p.Entries) {
		return bad("frozen membership does not prove the recorded logical selection")
	}
	for i := range selection.Entries {
		if selection.Entries[i].Name != p.Entries[i].Name {
			return bad("frozen entries are not in dependency order")
		}
	}
	wantPins := scopedPushPlanPins(p.Token, p.Entries)
	legacyTerminal := false
	if s.Terminal() && len(s.FollowupDecisions) == 0 && len(wantPins) != len(p.Pins) {
		// Pre-release terminal receipts grant no publication authority. Keep
		// their exact historical bytes readable for verified retirement.
		var legacy []ScopedPushPin
		for _, pin := range wantPins {
			if pin.Kind != "followup" {
				legacy = append(legacy, pin)
			}
		}
		if len(legacy) == len(p.Pins) {
			wantPins, legacyTerminal = legacy, true
		}
	}
	if len(wantPins) != len(p.Pins) {
		return bad("incomplete object protection")
	}
	cleanupStarted := s.Terminal()
	for i, pin := range p.Pins {
		if pin != wantPins[i] {
			return bad("private ref is not owned by this plan")
		}
		switch s.Pins[i] {
		case "pending", "creating":
			if s.Phase != "preparing" && s.Phase != "canceling" {
				return bad("object creation outside preparation")
			}
		case "held":
			if s.Terminal() {
				return bad("terminal receipt still owns a private ref")
			}
		case "deleting", "removed":
			cleanupStarted = true
			if s.Phase != "cleaning" && s.Phase != "canceling" && !s.Terminal() {
				return bad("object cleanup before publication")
			}
			if s.Terminal() && s.Pins[i] != "removed" {
				return bad("terminal cleanup is incomplete")
			}
		default:
			return bad("unknown object-protection outcome")
		}
	}
	followupNames := map[string]int{}
	for i, entry := range p.Entries {
		if entry.Followup != nil {
			followupNames[entry.Name] = i
			if cleanupStarted && !legacyTerminal && s.FollowupDecisions[entry.Name] == "" {
				return bad("object cleanup requires a durable follow-up decision")
			}
		}
	}
	for name, decision := range s.FollowupDecisions {
		index, ok := followupNames[name]
		if !ok || (decision != "keep" && decision != "clear") ||
			(decision == "clear" && s.Rows[index] != "succeeded") ||
			s.Phase == "preparing" || s.Phase == "publishing" {
			return bad("invalid follow-up decision")
		}
	}
	return nil
}

func scopedPushPlanPins(token string, entries []ScopedPushEntry) []ScopedPushPin {
	var pins []ScopedPushPin
	for i, e := range entries {
		if e.Archived {
			continue
		}
		add := func(kind, oid string) {
			if oid != "" {
				pins = append(pins, ScopedPushPin{Entry: i, Kind: kind, Ref: scopedPushPinRef(token, i, kind), OID: oid})
			}
		}
		add("source", e.Source)
		add("lease", e.Expected)
		if e.Inclusion != nil {
			add("witness", e.Inclusion.Witness)
		}
		if e.Followup != nil {
			add("followup", e.Followup.NewTipSHA)
		}
	}
	return pins
}

type scopedPushFile struct {
	data []byte
	info os.FileInfo
}

// Read without following symlinks or blocking on FIFOs, then verify the public
// name still denotes the opened inode. Callers also bind the parent directory.
func scopedPushReadFile(path string) (scopedPushFile, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return scopedPushFile{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return scopedPushFile{}, err
	}
	if !info.Mode().IsRegular() {
		return scopedPushFile{}, fmt.Errorf("scoped push requires a regular file at %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		return scopedPushFile{}, errors.Join(err, errors.New("publication evidence is unreadable or oversized"))
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return scopedPushFile{}, errors.Join(err, errors.New("publication evidence changed while reading"))
	}
	return scopedPushFile{data: data, info: info}, nil
}

func (snapshot scopedPushFile) verify(path string) error {
	current, err := scopedPushReadFile(path)
	if snapshot.info == nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if snapshot.info == nil || !os.SameFile(snapshot.info, current.info) || !bytes.Equal(snapshot.data, current.data) {
		return errors.New("publication evidence was replaced or edited; refusing to overwrite it")
	}
	return nil
}

func scopedPushDecode(data []byte) (*ScopedPushState, error) {
	// encoding/json's ordinary decoder accepts duplicate keys. Publication
	// authority is stricter: no duplicate, unknown, or trailing fields.
	if err := scopedPushJSONKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return nil, err
	}
	var state ScopedPushState
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&state); err != nil {
		return nil, fmt.Errorf("untrusted scoped push state: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing scoped push state")
	}
	if err := state.validate(); err != nil {
		return nil, err
	}
	return &state, nil
}

func scopedPushJSONKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return nil
	}
	object := token == json.Delim('{')
	keys := map[string]bool{}
	for d.More() {
		if object {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("duplicate/invalid key in scoped push state")
			}
			keys[name] = true
		}
		if err := scopedPushJSONKeys(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func LoadScopedPushState(loc ScopedPushLocation) (*ScopedPushState, error) {
	return loadScopedPushStatePath(ScopedPushStatePath(loc))
}

func loadScopedPushStatePath(path string) (*ScopedPushState, error) {
	snapshot, err := scopedPushReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return scopedPushDecode(snapshot.data)
}

func RefusePendingScopedPush(loc ScopedPushLocation) error {
	state, err := LoadScopedPushState(loc)
	if err != nil {
		return fmt.Errorf("scoped push evidence cannot be trusted; preserve %s: %w", ScopedPushStatePath(loc), err)
	}
	if state != nil && !state.Terminal() {
		return fmt.Errorf("scoped push is pending for %q; run: tws push %s --continue (or --cancel); use the new tws binary while pending", loc.Feature, loc.Feature)
	}
	return nil
}

type scopedPushJournal struct {
	path     string
	snapshot scopedPushFile
	state    *ScopedPushState
	verify   func() error
}

func (j *scopedPushJournal) save() error {
	j.state.Revision++
	if err := j.state.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(j.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > 16<<20 {
		return errors.New("publication evidence exceeds the supported state size; existing journal preserved")
	}
	if err := scopedPushCheckpoint("before-state-write", int(j.state.Revision)); err != nil {
		return err
	}
	if j.verify != nil {
		if err := j.verify(); err != nil {
			return err
		}
	}
	if err := errors.Join(j.state.Plan.Root.verify(), j.state.Plan.FeatureDir.verify()); err != nil {
		return err
	}
	parent := j.state.Plan.StateDirectory
	if err := parent.verify(); err != nil {
		return err
	}
	if err := j.snapshot.verify(j.path); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent.Path, ".push-write-"+j.state.Plan.Token+"-")
	if err != nil {
		return err
	}
	name := file.Name()
	info, err := file.Stat()
	if err != nil {
		return errors.Join(err, file.Close())
	}
	defer func() {
		if current, err := os.Lstat(name); err == nil && os.SameFile(info, current) {
			_ = os.Remove(name)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := scopedPushCheckpoint("before-state-file-sync", int(j.state.Revision)); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := scopedPushCheckpoint("before-state-install", int(j.state.Revision)); err != nil {
		return err
	}
	if err := errors.Join(parent.verify(), j.snapshot.verify(j.path)); err != nil {
		return err
	}
	if j.snapshot.info == nil {
		err = os.Link(name, j.path)
	} else {
		err = os.Rename(name, j.path)
	}
	if err != nil {
		return err
	}
	j.snapshot, err = scopedPushReadFile(j.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(j.snapshot.data, data) {
		return errors.New("publication journal changed during installation")
	}
	if err := scopedPushCheckpoint("after-state-install", int(j.state.Revision)); err != nil {
		return err
	}
	return j.sync()
}

// Repeated on recovery even for an already-visible success or terminal
// receipt: rename visibility after a failed fsync is not durable completion.
func (j *scopedPushJournal) sync() error {
	if j.state.Terminal() && j.state.Plan.Mode == ModeExternal {
		parent, err := scopedPushDirectoryAt(filepath.Dir(j.path))
		if err != nil {
			return err
		}
		if parent.Identity != j.state.Plan.FeatureDir.Identity {
			return errors.New("terminal publication history feature identity changed")
		}
		return scopedPushSyncSnapshot(j.path, j.snapshot, parent, j.state.Plan.Root, j.state.Revision)
	}
	return scopedPushSyncSnapshot(j.path, j.snapshot, j.state.Plan.StateDirectory, j.state.Plan.Root, j.state.Revision)
}

func scopedPushSyncSnapshot(path string, snapshot scopedPushFile, parent, root ScopedPushDirectory, revision uint64) error {
	if err := errors.Join(parent.verify(), root.verify(), snapshot.verify(path)); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, snapshot.info) {
		return errors.Join(err, file.Close(), errors.New("publication journal changed before fsync"))
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := scopedPushCheckpoint("before-state-directory-sync", int(revision)); err != nil {
		return err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if err := syncDir(dir); err != nil {
			return err
		}
		if dir == root.Path {
			break
		}
		if filepath.Dir(dir) == dir {
			return errors.New("publication state directory is outside its root")
		}
	}
	return errors.Join(parent.verify(), root.verify(), snapshot.verify(path))
}
