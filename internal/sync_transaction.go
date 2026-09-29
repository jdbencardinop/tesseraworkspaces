package internal

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const SyncTransactionEvidenceVersion = 1

const (
	SyncTxnPreparing   = "preparing"
	SyncTxnForward     = "forward"
	SyncTxnFailed      = "failed"
	SyncTxnPublished   = "published"
	SyncTxnRollingBack = "rolling-back"
	SyncTxnCleanup     = "cleanup"
)

const (
	SyncTxnActionIntent   = "intent"
	SyncTxnActionObserved = "observed"
	SyncTxnActionFailed   = "failed"
	SyncTxnActionMutated  = "mutated"
)

// SyncTransactionStepHook is a test-only crash seam around durable ordinary
// sync transitions. Production leaves it nil.
var SyncTransactionStepHook func(step string) error

// SyncTransaction is the rollback evidence shared by external and checkout
// ordinary sync. It is embedded only in transactional state versions.
type SyncTransaction struct {
	EvidenceVersion int    `yaml:"evidence_version"`
	RunID           string `yaml:"run_id"`
	CreatedAt       string `yaml:"created_at"`
	UpdatedAt       string `yaml:"updated_at"`
	WorkspaceMode   string `yaml:"workspace_mode"`
	Feature         string `yaml:"feature"`
	WorkspaceRoot   string `yaml:"workspace_repo_root"`
	Phase           string `yaml:"phase"`
	Ready           bool   `yaml:"ready"`
	Completion      string `yaml:"completion,omitempty"`

	Selected     []string                    `yaml:"selected"`
	Metadata     SyncTransactionMetadata     `yaml:"metadata"`
	Repositories []SyncTransactionRepo       `yaml:"repositories"`
	Actions      []SyncTransactionAction     `yaml:"actions"`
	Validations  []SyncTransactionValidation `yaml:"validations,omitempty"`
	Publication  SyncTransactionPublish      `yaml:"publication"`
	Rollback     SyncTransactionRollback     `yaml:"rollback"`
}

type SyncTransactionMetadata struct {
	Path           string `yaml:"path"`
	BeforeBase64   string `yaml:"before_base64"`
	BeforeSHA256   string `yaml:"before_sha256"`
	ExpectedSHA256 string `yaml:"expected_sha256"`
	IntentBase64   string `yaml:"intent_base64,omitempty"`
	IntentSHA256   string `yaml:"intent_sha256,omitempty"`
	IntentPending  bool   `yaml:"intent_pending,omitempty"`
	Restored       bool   `yaml:"restored,omitempty"`
}

type SyncTransactionRepo struct {
	Root         string                  `yaml:"root"`
	CommonDir    string                  `yaml:"common_dir"`
	OIDWidth     int                     `yaml:"oid_width"`
	Refs         []SyncTransactionRef    `yaml:"refs"`
	Holders      []SyncTransactionHolder `yaml:"holders"`
	RefsRestored bool                    `yaml:"refs_restored,omitempty"`
	PinsRemoved  bool                    `yaml:"pins_removed,omitempty"`
}

type SyncTransactionRef struct {
	Ref              string   `yaml:"ref"`
	PreimageSHA      string   `yaml:"preimage_sha"`
	ExpectedSHA      string   `yaml:"expected_sha"`
	PinRef           string   `yaml:"pin_ref"`
	Pinned           bool     `yaml:"pinned"`
	PinDeletePending bool     `yaml:"pin_delete_pending,omitempty"`
	SelectedNames    []string `yaml:"selected_names,omitempty"`
	Moved            bool     `yaml:"moved,omitempty"`
	Restored         bool     `yaml:"restored,omitempty"`
}

type SyncTransactionHolder struct {
	Path                 string                      `yaml:"path"`
	OriginalBranch       string                      `yaml:"original_branch,omitempty"`
	OriginalHEAD         string                      `yaml:"original_head"`
	OriginalDetached     bool                        `yaml:"original_detached"`
	ExpectedBranch       string                      `yaml:"expected_branch,omitempty"`
	ExpectedHEAD         string                      `yaml:"expected_head"`
	ExpectedDetached     bool                        `yaml:"expected_detached"`
	DetachPending        bool                        `yaml:"detach_pending,omitempty"`
	RestorePending       bool                        `yaml:"restore_pending,omitempty"`
	Detached             bool                        `yaml:"detached,omitempty"`
	Restored             bool                        `yaml:"restored,omitempty"`
	ForwardRestoreTarget *SyncTransactionHolderValue `yaml:"forward_restore_target,omitempty"`
	OriginalHeadPinned   bool                        `yaml:"original_head_pinned,omitempty"`
	HeadPinDeletePending bool                        `yaml:"head_pin_delete_pending,omitempty"`
}

type SyncTransactionRefValue struct {
	Ref string `yaml:"ref"`
	SHA string `yaml:"sha"`
}

type SyncTransactionHolderValue struct {
	Path     string `yaml:"path"`
	Branch   string `yaml:"branch,omitempty"`
	HEAD     string `yaml:"head"`
	Detached bool   `yaml:"detached"`
}

type SyncTransactionAction struct {
	Sequence            int                          `yaml:"sequence"`
	Kind                string                       `yaml:"kind"`
	Entry               string                       `yaml:"entry,omitempty"`
	RepoCommonDir       string                       `yaml:"repo_common_dir"`
	ContextPath         string                       `yaml:"context_path"`
	ContextBefore       SyncTransactionHolderValue   `yaml:"context_before"`
	ContextRef          string                       `yaml:"context_ref,omitempty"`
	AllowedRefs         []string                     `yaml:"allowed_refs"`
	BeforeRefs          []SyncTransactionRefValue    `yaml:"before_refs"`
	AfterRefs           []SyncTransactionRefValue    `yaml:"after_refs,omitempty"`
	BeforeHolders       []SyncTransactionHolderValue `yaml:"before_holders"`
	RefLogAnchors       map[string]string            `yaml:"ref_log_anchors"`
	ContextReflogAnchor string                       `yaml:"context_reflog_anchor"`
	AfterHolders        []SyncTransactionHolderValue `yaml:"after_holders,omitempty"`
	Status              string                       `yaml:"status"`
	Error               string                       `yaml:"error,omitempty"`
}

type SyncTransactionValidation struct {
	Sequence      int                          `yaml:"sequence"`
	Entry         string                       `yaml:"entry"`
	RepoCommonDir string                       `yaml:"repo_common_dir"`
	ContextPath   string                       `yaml:"context_path"`
	BeforeRefs    []SyncTransactionRefValue    `yaml:"before_refs"`
	AfterRefs     []SyncTransactionRefValue    `yaml:"after_refs,omitempty"`
	BeforeHolders []SyncTransactionHolderValue `yaml:"before_holders"`
	AfterHolders  []SyncTransactionHolderValue `yaml:"after_holders,omitempty"`
	Status        string                       `yaml:"status"`
	Error         string                       `yaml:"error,omitempty"`
}

type SyncTransactionPushResult struct {
	Entry   string `yaml:"entry"`
	Success bool   `yaml:"success"`
	Detail  string `yaml:"detail,omitempty"`
}

type SyncTransactionPushIntent struct {
	Entry          string `yaml:"entry"`
	RepoCommonDir  string `yaml:"repo_common_dir"`
	DestinationRef string `yaml:"destination_ref"`
	SourceSHA      string `yaml:"source_sha"`
}

type SyncTransactionPublish struct {
	IntentDurable bool                        `yaml:"intent_durable,omitempty"`
	StartedAt     string                      `yaml:"started_at,omitempty"`
	CurrentEntry  string                      `yaml:"current_entry,omitempty"`
	Current       *SyncTransactionPushIntent  `yaml:"current,omitempty"`
	Intents       []SyncTransactionPushIntent `yaml:"intents,omitempty"`
	Results       []SyncTransactionPushResult `yaml:"results,omitempty"`
}

type SyncTransactionRollback struct {
	StartedAt        string   `yaml:"started_at,omitempty"`
	Stage            string   `yaml:"stage,omitempty"`
	CompletedRepos   []string `yaml:"completed_repositories,omitempty"`
	MetadataRestored bool     `yaml:"metadata_restored,omitempty"`
	HoldersRestored  []string `yaml:"holders_restored,omitempty"`
	PinsRemovedRepos []string `yaml:"pins_removed_repositories,omitempty"`
}

type SyncTransactionBeginInput struct {
	FeaturePath       string
	Feature           string
	Mode              WorkspaceMode
	WorkspaceRepoRoot string
	Stack             Stack
	Selected          []string
	EntryRepoDirs     map[string]string
}

func newSyncTransactionRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint sync transaction run id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func syncTransactionHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func syncTransactionPinRef(runID, commonDir, ref string) string {
	sum := sha256.Sum256([]byte(commonDir + "\x00" + ref))
	return "refs/tws/sync/" + runID + "/pre/" + hex.EncodeToString(sum[:12])
}

func syncTransactionGit(dir string, stdin []byte, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
	}
	return stdout.Bytes(), stderr.Bytes(), code, err
}

func syncTransactionGitError(stderr []byte, err error) string {
	msg := strings.TrimSpace(string(stderr))
	if msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "git command failed"
}

func syncTransactionRepoIdentity(dir string) (root, common string, oidWidth int, err error) {
	rootBytes, stderr, _, runErr := syncTransactionGit(dir, nil, "rev-parse", "--show-toplevel")
	if runErr != nil {
		return "", "", 0, fmt.Errorf("resolve repository root from %s: %s", dir, syncTransactionGitError(stderr, runErr))
	}
	root = canonicalize(strings.TrimSpace(string(rootBytes)))
	commonBytes, stderr, _, runErr := syncTransactionGit(root, nil, "rev-parse", "--git-common-dir")
	if runErr != nil {
		return "", "", 0, fmt.Errorf("resolve repository common dir from %s: %s", root, syncTransactionGitError(stderr, runErr))
	}
	common = strings.TrimSpace(string(commonBytes))
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common = canonicalize(common)
	head, ok, resolveErr := syncTransactionResolveRef(root, "HEAD")
	if resolveErr != nil || !ok {
		if resolveErr == nil {
			resolveErr = errors.New("HEAD does not resolve")
		}
		return "", "", 0, resolveErr
	}
	oidWidth = len(head)
	if oidWidth != 40 && oidWidth != 64 {
		return "", "", 0, fmt.Errorf("unsupported object id width %d in %s", oidWidth, root)
	}
	return root, common, oidWidth, nil
}

func syncTransactionResolveRef(repoDir, ref string) (string, bool, error) {
	stdout, stderr, code, err := syncTransactionGit(repoDir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		if code == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("resolve %s: %s", ref, syncTransactionGitError(stderr, err))
	}
	sha := strings.ToLower(strings.TrimSpace(string(stdout)))
	if sha == "" {
		return "", false, nil
	}
	return sha, true, nil
}

func syncTransactionListHeads(repoDir string) ([]SyncTransactionRefValue, error) {
	stdout, stderr, _, err := syncTransactionGit(repoDir, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("list local branches in %s: %s", repoDir, syncTransactionGitError(stderr, err))
	}
	var refs []SyncTransactionRefValue
	for _, line := range bytes.Split(stdout, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed local branch inventory in %s", repoDir)
		}
		if len(parts[2]) != 0 {
			return nil, fmt.Errorf("symbolic local branch %s cannot be owned by a sync rollback transaction", parts[0])
		}
		refs = append(refs, SyncTransactionRefValue{Ref: string(parts[0]), SHA: strings.ToLower(string(parts[1]))})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Ref < refs[j].Ref })
	return refs, nil
}

func syncTransactionHolderValues(repoDir string) ([]SyncTransactionHolderValue, error) {
	inv := BuildWorktreeInventory(repoDir)
	if !inv.Available {
		return nil, fmt.Errorf("inspect worktrees for %s: %v", repoDir, inv.Err)
	}
	values := make([]SyncTransactionHolderValue, 0, len(inv.Records))
	for _, rec := range inv.Records {
		if rec.Bare || rec.Head == nil {
			continue
		}
		v := SyncTransactionHolderValue{Path: rec.Path, HEAD: strings.ToLower(*rec.Head)}
		if rec.BranchRef != nil {
			v.Branch = strings.TrimPrefix(*rec.BranchRef, "refs/heads/")
		} else {
			v.Detached = true
		}
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Path < values[j].Path })
	return values, nil
}

// CaptureSyncTransaction captures exact metadata plus every local branch and
// holder in each selected repository. It performs reads only.
func CaptureSyncTransaction(in SyncTransactionBeginInput) (*SyncTransaction, error) {
	runID, err := newSyncTransactionRunID()
	if err != nil {
		return nil, err
	}
	stackPath := StackPath(in.FeaturePath)
	stackBytes, err := os.ReadFile(stackPath)
	if err != nil {
		return nil, fmt.Errorf("capture stack metadata: %w", err)
	}
	var capturedStack Stack
	if err := yaml.Unmarshal(stackBytes, &capturedStack); err != nil {
		return nil, fmt.Errorf("decode captured stack metadata: %w", err)
	}
	if !reflect.DeepEqual(capturedStack, in.Stack) {
		return nil, errors.New("stack.yaml changed after sync admission; refusing to bind a stale selection or plan")
	}
	selectedSet := make(map[string]bool, len(in.Selected))
	for _, name := range in.Selected {
		selectedSet[name] = true
	}

	tx := &SyncTransaction{
		EvidenceVersion: SyncTransactionEvidenceVersion,
		RunID:           runID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		WorkspaceMode:   string(in.Mode),
		Feature:         in.Feature,
		WorkspaceRoot:   canonicalize(in.WorkspaceRepoRoot),
		Phase:           SyncTxnPreparing,
		Selected:        append([]string(nil), in.Selected...),
		Actions:         []SyncTransactionAction{},
		Metadata: SyncTransactionMetadata{
			Path:           canonicalize(stackPath),
			BeforeBase64:   base64.StdEncoding.EncodeToString(stackBytes),
			BeforeSHA256:   syncTransactionHash(stackBytes),
			ExpectedSHA256: syncTransactionHash(stackBytes),
		},
	}

	type repoSeed struct {
		root, common string
		oidWidth     int
		entries      []StackEntry
	}
	byCommon := map[string]*repoSeed{}
	for _, entry := range capturedStack.Branches {
		if !selectedSet[entry.Name] {
			continue
		}
		ctx := in.EntryRepoDirs[entry.Name]
		if ctx == "" {
			ctx = in.WorkspaceRepoRoot
		}
		root, common, width, idErr := syncTransactionRepoIdentity(ctx)
		if idErr != nil {
			return nil, fmt.Errorf("snapshot %s: %w", entry.Name, idErr)
		}
		seed := byCommon[common]
		if seed == nil {
			seed = &repoSeed{root: root, common: common, oidWidth: width}
			byCommon[common] = seed
		}
		seed.entries = append(seed.entries, entry)
	}
	if len(byCommon) == 0 && len(in.Selected) != 0 {
		return nil, errors.New("transaction selection has no repositories")
	}
	commons := make([]string, 0, len(byCommon))
	for common := range byCommon {
		commons = append(commons, common)
	}
	sort.Strings(commons)
	for _, common := range commons {
		seed := byCommon[common]
		heads, listErr := syncTransactionListHeads(seed.root)
		if listErr != nil {
			return nil, listErr
		}
		selectedByRef := map[string][]string{}
		for _, entry := range seed.entries {
			ref := "refs/heads/" + entry.GitBranch()
			selectedByRef[ref] = append(selectedByRef[ref], entry.Name)
		}
		for ref := range selectedByRef {
			found := false
			for _, head := range heads {
				if head.Ref == ref {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("selected branch ref %s does not exist in repository %s", ref, seed.root)
			}
		}
		holders, holderErr := syncTransactionHolderValues(seed.root)
		if holderErr != nil {
			return nil, holderErr
		}
		repo := SyncTransactionRepo{Root: seed.root, CommonDir: seed.common, OIDWidth: seed.oidWidth}
		for _, head := range heads {
			repo.Refs = append(repo.Refs, SyncTransactionRef{
				Ref: head.Ref, PreimageSHA: head.SHA, ExpectedSHA: head.SHA,
				PinRef:        syncTransactionPinRef(runID, seed.common, head.Ref),
				SelectedNames: append([]string(nil), selectedByRef[head.Ref]...),
			})
		}
		for _, holder := range holders {
			repo.Holders = append(repo.Holders, SyncTransactionHolder{
				Path: holder.Path, OriginalBranch: holder.Branch, OriginalHEAD: holder.HEAD,
				OriginalDetached: holder.Detached, ExpectedBranch: holder.Branch,
				ExpectedHEAD: holder.HEAD, ExpectedDetached: holder.Detached,
			})
		}
		tx.Repositories = append(tx.Repositories, repo)
	}
	return tx, ValidateSyncTransaction(tx, in.Feature, in.Mode)
}

func SyncTransactionCapturedStack(tx *SyncTransaction) (Stack, error) {
	if tx == nil {
		return Stack{}, errors.New("sync transaction is absent")
	}
	data, err := base64.StdEncoding.DecodeString(tx.Metadata.BeforeBase64)
	if err != nil || syncTransactionHash(data) != tx.Metadata.BeforeSHA256 {
		return Stack{}, errors.New("captured stack metadata is corrupt")
	}
	var stack Stack
	if err := yaml.Unmarshal(data, &stack); err != nil {
		return Stack{}, fmt.Errorf("decode captured stack metadata: %w", err)
	}
	return stack, nil
}

func syncTransactionReadMetadata(tx *SyncTransaction) ([]byte, error) {
	if tx == nil {
		return nil, errors.New("sync transaction is absent")
	}
	info, err := os.Lstat(tx.Metadata.Path)
	if err != nil {
		return nil, fmt.Errorf("inspect stack metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("stack metadata path %s is not a regular non-symlink file", tx.Metadata.Path)
	}
	return os.ReadFile(tx.Metadata.Path)
}

func verifySyncTransactionMetadataImage(tx *SyncTransaction, allowIntent bool) error {
	live, err := syncTransactionReadMetadata(tx)
	if err != nil {
		return err
	}
	liveHash := syncTransactionHash(live)
	if liveHash == tx.Metadata.ExpectedSHA256 {
		return nil
	}
	if allowIntent && tx.Metadata.IntentPending && liveHash == tx.Metadata.IntentSHA256 {
		return nil
	}
	return fmt.Errorf("stack.yaml changed outside this sync transaction; recovery evidence is preserved")
}

func reconcileSyncTransactionMetadataIntent(tx *SyncTransaction, save func() error) error {
	if tx == nil || !tx.Metadata.IntentPending {
		return verifySyncTransactionMetadataImage(tx, false)
	}
	live, err := syncTransactionReadMetadata(tx)
	if err != nil {
		return err
	}
	switch liveHash := syncTransactionHash(live); liveHash {
	case tx.Metadata.ExpectedSHA256:
		return nil
	case tx.Metadata.IntentSHA256:
		tx.Metadata.ExpectedSHA256 = liveHash
		tx.Metadata.IntentPending = false
		if err := save(); err != nil {
			return fmt.Errorf("persist reconciled stack metadata intent: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("stack.yaml matches neither side of the durable metadata intent; preserve the journal and recover manually")
	}
}

// PreflightSyncTransactionBirth rejects dirty or in-progress holders across
// every selected repository before any selected branch can move.
func PreflightSyncTransactionBirth(tx *SyncTransaction) error {
	if tx == nil {
		return errors.New("sync transaction is absent")
	}
	if err := verifySyncTransactionMetadataImage(tx, false); err != nil {
		return err
	}
	for i := range tx.Repositories {
		repo := &tx.Repositories[i]
		liveRefs, err := syncTransactionListHeads(repo.Root)
		if err != nil {
			return err
		}
		if err := verifySyncTransactionRefImage(repo, liveRefs); err != nil {
			return err
		}
		liveHolders, err := syncTransactionHolderValues(repo.Root)
		if err != nil {
			return err
		}
		if err := verifySyncTransactionHolderImage(repo, liveHolders); err != nil {
			return err
		}
		for _, holder := range repo.Holders {
			op, err := syncTransactionOperation(holder.Path)
			if err != nil {
				return err
			}
			if op != "" {
				return fmt.Errorf("a %s is in progress in holder %s; transactional sync refuses before moving any branch", op, holder.Path)
			}
			dirty, err := syncTransactionHolderDirty(holder.Path)
			if err != nil {
				return err
			}
			if dirty {
				return fmt.Errorf("holder %s has tracked modifications; transactional sync refuses before moving any branch", holder.Path)
			}
		}
	}
	return nil
}

// PinSyncTransaction creates and verifies every preimage pin. save must
// durably persist the enclosing recovery document after each successful pin.
func PinSyncTransaction(tx *SyncTransaction, save func() error) error {
	if tx == nil {
		return errors.New("sync transaction is absent")
	}
	for ri := range tx.Repositories {
		repo := &tx.Repositories[ri]
		for fi := range repo.Refs {
			ref := &repo.Refs[fi]
			live, found, err := syncTransactionResolveRef(repo.Root, ref.PinRef)
			if err != nil {
				return err
			}
			switch {
			case found && live != ref.PreimageSHA:
				return fmt.Errorf("sync snapshot pin %s resolves to %s, not %s", ref.PinRef, live, ref.PreimageSHA)
			case !found:
				_, stderr, _, runErr := syncTransactionGit(repo.Root, nil, "update-ref", "--no-deref", ref.PinRef, ref.PreimageSHA, strings.Repeat("0", repo.OIDWidth))
				if runErr != nil {
					return fmt.Errorf("pin %s: %s", ref.Ref, syncTransactionGitError(stderr, runErr))
				}
			}
			ref.Pinned = true
			if err := save(); err != nil {
				return fmt.Errorf("persist sync pin progress: %w", err)
			}
		}
	}
	if err := pinSyncDetachedHeads(tx, save); err != nil {
		return err
	}
	tx.Ready = true
	tx.Phase = SyncTxnForward
	if err := save(); err != nil {
		return fmt.Errorf("persist sync snapshot readiness: %w", err)
	}
	return syncTransactionStep("snapshot-ready")
}

func syncTransactionStep(step string) error {
	if SyncTransactionStepHook == nil {
		return nil
	}
	return SyncTransactionStepHook(step)
}

func ValidateSyncTransaction(tx *SyncTransaction, feature string, mode WorkspaceMode) error {
	if tx == nil {
		return errors.New("transactional sync evidence is absent")
	}
	if tx.EvidenceVersion != SyncTransactionEvidenceVersion {
		return fmt.Errorf("unsupported sync transaction evidence version %d", tx.EvidenceVersion)
	}
	if !reparentRunIDShape(tx.RunID) {
		return errors.New("sync transaction run_id is invalid")
	}
	if tx.Feature == "" || tx.Feature != feature || tx.WorkspaceMode != string(mode) ||
		(mode != ModeExternal && mode != ModeCheckout) || !syncTransactionAbsolutePath(tx.WorkspaceRoot) {
		return fmt.Errorf("sync transaction identity is foreign (feature=%q mode=%q)", tx.Feature, tx.WorkspaceMode)
	}
	if tx.Phase != SyncTxnPreparing && tx.Phase != SyncTxnForward && tx.Phase != SyncTxnFailed && tx.Phase != SyncTxnPublished && tx.Phase != SyncTxnRollingBack && tx.Phase != SyncTxnCleanup {
		return fmt.Errorf("sync transaction phase %q is unsupported", tx.Phase)
	}
	before, err := base64.StdEncoding.DecodeString(tx.Metadata.BeforeBase64)
	if err != nil || syncTransactionHash(before) != tx.Metadata.BeforeSHA256 || !filepath.IsAbs(tx.Metadata.Path) {
		return errors.New("sync transaction metadata preimage is invalid")
	}
	if !reparentStateHex(tx.Metadata.ExpectedSHA256, 64) {
		return errors.New("sync transaction metadata expected hash is absent")
	}
	seenCommon := map[string]bool{}
	for _, repo := range tx.Repositories {
		if !syncTransactionAbsolutePath(repo.Root) || !syncTransactionAbsolutePath(repo.CommonDir) || seenCommon[repo.CommonDir] || (repo.OIDWidth != 40 && repo.OIDWidth != 64) {
			return errors.New("sync transaction repository identity is invalid")
		}
		seenCommon[repo.CommonDir] = true
		seenRef := map[string]bool{}
		for _, ref := range repo.Refs {
			if !strings.HasPrefix(ref.Ref, "refs/heads/") || !reparentStateRef(ref.Ref) || seenRef[ref.Ref] ||
				!reparentStateOID(ref.PreimageSHA, repo.OIDWidth) || !reparentStateOID(ref.ExpectedSHA, repo.OIDWidth) ||
				ref.PinRef != syncTransactionPinRef(tx.RunID, repo.CommonDir, ref.Ref) {
				return fmt.Errorf("sync transaction ref evidence is invalid for %s", ref.Ref)
			}
			seenRef[ref.Ref] = true
		}
	}
	return validateSyncTransactionRelationships(tx, before)
}

func syncTransactionRepo(tx *SyncTransaction, repoDir string) (*SyncTransactionRepo, error) {
	_, common, _, err := syncTransactionRepoIdentity(repoDir)
	if err != nil {
		return nil, err
	}
	for i := range tx.Repositories {
		if tx.Repositories[i].CommonDir == common {
			return &tx.Repositories[i], nil
		}
	}
	return nil, fmt.Errorf("repository %s is outside this sync transaction", repoDir)
}

func syncTransactionValuesMap(values []SyncTransactionRefValue) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		out[value.Ref] = value.SHA
	}
	return out
}

func syncTransactionExpectedRefs(repo *SyncTransactionRepo) map[string]string {
	out := make(map[string]string, len(repo.Refs))
	for _, ref := range repo.Refs {
		out[ref.Ref] = ref.ExpectedSHA
	}
	return out
}

func verifySyncTransactionRefImage(repo *SyncTransactionRepo, live []SyncTransactionRefValue) error {
	expected := syncTransactionExpectedRefs(repo)
	got := syncTransactionValuesMap(live)
	if len(got) != len(expected) {
		return fmt.Errorf("local branch set changed in repository %s; tws will not claim concurrent ref changes", repo.Root)
	}
	for ref, want := range expected {
		have, ok := got[ref]
		if !ok || have != want {
			if !ok {
				have = "(missing)"
			}
			return fmt.Errorf("branch %s changed outside this sync transaction (expected %s, found %s)", ref, want, have)
		}
	}
	return nil
}

func syncTransactionHolderMap(values []SyncTransactionHolderValue) map[string]SyncTransactionHolderValue {
	out := make(map[string]SyncTransactionHolderValue, len(values))
	for _, value := range values {
		out[value.Path] = value
	}
	return out
}

func verifySyncTransactionHolderImage(repo *SyncTransactionRepo, live []SyncTransactionHolderValue) error {
	got := syncTransactionHolderMap(live)
	for _, holder := range repo.Holders {
		value, ok := got[holder.Path]
		if !ok {
			return fmt.Errorf("recorded worktree holder %s is missing", holder.Path)
		}
		if value.Branch != holder.ExpectedBranch || value.HEAD != holder.ExpectedHEAD || value.Detached != holder.ExpectedDetached {
			return fmt.Errorf("worktree holder %s changed outside this sync transaction", holder.Path)
		}
	}
	return nil
}

// SyncAllowedRebaseRefs returns the local branches Git may move for one rebase.
// A scoped/no-update-refs rebase owns only its target branch. With
// --update-refs, refs whose tips are replay candidates are included too.
func SyncAllowedRebaseRefs(tx *SyncTransaction, repoDir, branchRef, cutoff string, updateRefs bool) ([]string, error) {
	repo, err := syncTransactionRepo(tx, repoDir)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{branchRef: true}
	if updateRefs {
		stdout, stderr, _, runErr := syncTransactionGit(repo.Root, nil, "rev-list", cutoff+".."+branchRef)
		if runErr != nil {
			return nil, fmt.Errorf("measure --update-refs ownership for %s: %s", branchRef, syncTransactionGitError(stderr, runErr))
		}
		commits := map[string]bool{}
		for _, line := range strings.Fields(string(stdout)) {
			commits[strings.ToLower(line)] = true
		}
		for _, ref := range repo.Refs {
			if commits[ref.ExpectedSHA] {
				allowed[ref.Ref] = true
			}
		}
	}
	out := make([]string, 0, len(allowed))
	for ref := range allowed {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out, nil
}

// BeginSyncGitAction verifies the last recorded repository image and durably
// records intent before a rebase or other branch-affecting Git command.
func BeginSyncGitAction(tx *SyncTransaction, repoDir, kind, entry, contextPath string, allowedRefs []string, save func() error) error {
	return beginSyncGitAction(tx, repoDir, kind, entry, contextPath, "", allowedRefs, save)
}

func BeginSyncGitActionWithContextRef(tx *SyncTransaction, repoDir, kind, entry, contextPath, contextRef string, allowedRefs []string, save func() error) error {
	return beginSyncGitAction(tx, repoDir, kind, entry, contextPath, contextRef, allowedRefs, save)
}

func beginSyncGitAction(tx *SyncTransaction, repoDir, kind, entry, contextPath, contextRef string, allowedRefs []string, save func() error) error {
	if tx == nil || !tx.Ready {
		return errors.New("sync transaction snapshot is not ready")
	}
	if err := verifySyncTransactionMetadataImage(tx, false); err != nil {
		return err
	}
	if len(tx.Actions) > 0 {
		last := tx.Actions[len(tx.Actions)-1]
		if last.Status == SyncTxnActionIntent {
			return fmt.Errorf("sync action %d is still pending reconciliation", last.Sequence)
		}
	}
	repo, err := syncTransactionRepo(tx, repoDir)
	if err != nil {
		return err
	}
	refs, err := syncTransactionListHeads(repo.Root)
	if err != nil {
		return err
	}
	if err := verifySyncTransactionRefImage(repo, refs); err != nil {
		return err
	}
	holders, err := syncTransactionHolderValues(repo.Root)
	if err != nil {
		return err
	}
	if err := verifySyncTransactionHolderImage(repo, holders); err != nil {
		return err
	}
	contextPath = canonicalize(contextPath)
	contextBefore, ok := syncTransactionHolderMap(holders)[contextPath]
	if !ok {
		return fmt.Errorf("sync action context %s is not a recorded repository holder", contextPath)
	}
	if contextRef == "" {
		candidate := "refs/heads/" + contextBefore.Branch
		for _, ref := range allowedRefs {
			if ref == candidate {
				contextRef = candidate
				break
			}
		}
	} else {
		found := false
		for _, ref := range allowedRefs {
			if ref == contextRef {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("sync action context ref %s is outside its attributable ref set", contextRef)
		}
	}
	action := SyncTransactionAction{
		Sequence: len(tx.Actions) + 1, Kind: kind, Entry: entry,
		RepoCommonDir: repo.CommonDir, ContextPath: contextPath,
		ContextBefore: contextBefore, ContextRef: contextRef,
		AllowedRefs: append([]string(nil), allowedRefs...), BeforeRefs: refs,
		BeforeHolders: holders, Status: SyncTxnActionIntent,
		RefLogAnchors: make(map[string]string, len(allowedRefs)),
	}
	for _, ref := range allowedRefs {
		log, err := syncTransactionReflog(repo.Root, ref, 1)
		if err != nil {
			return err
		}
		if len(log) == 0 {
			return fmt.Errorf("branch %s has no reflog; sync cannot attribute a crashed rebase safely", ref)
		}
		action.RefLogAnchors[ref] = log[0]
	}
	log, err := syncTransactionReflog(contextPath, "HEAD", 1)
	if err != nil {
		return err
	}
	if len(log) == 0 {
		return fmt.Errorf("context %s has no HEAD reflog; sync cannot attribute a crashed rebase safely", contextPath)
	}
	action.ContextReflogAnchor = log[0]
	tx.Actions = append(tx.Actions, action)
	if err := save(); err != nil {
		return fmt.Errorf("persist sync action intent: %w", err)
	}
	return syncTransactionStep("action-intent:" + kind + ":" + entry)
}

func syncTransactionActionRepo(tx *SyncTransaction, action *SyncTransactionAction) (*SyncTransactionRepo, error) {
	for i := range tx.Repositories {
		if tx.Repositories[i].CommonDir == action.RepoCommonDir {
			return &tx.Repositories[i], nil
		}
	}
	return nil, fmt.Errorf("sync action names unknown repository %s", action.RepoCommonDir)
}

func completeSyncAction(tx *SyncTransaction, commandErr error, save func() error, allowActiveRebase bool) error {
	if tx == nil || len(tx.Actions) == 0 {
		return nil
	}
	action := &tx.Actions[len(tx.Actions)-1]
	if action.Status != SyncTxnActionIntent {
		return nil
	}
	repo, err := syncTransactionActionRepo(tx, action)
	if err != nil {
		return err
	}
	if gitRebaseInProgress(action.ContextPath) && allowActiveRebase {
		return nil
	}
	after, err := syncTransactionListHeads(repo.Root)
	if err != nil {
		return err
	}
	beforeMap := syncTransactionValuesMap(action.BeforeRefs)
	afterMap := syncTransactionValuesMap(after)
	allowed := make(map[string]bool, len(action.AllowedRefs))
	for _, ref := range action.AllowedRefs {
		allowed[ref] = true
	}
	if len(beforeMap) != len(afterMap) {
		return fmt.Errorf("local branch set changed during sync action %d; tws will not claim the new or deleted ref", action.Sequence)
	}
	for ref, before := range beforeMap {
		now, ok := afterMap[ref]
		if !ok {
			return fmt.Errorf("branch %s disappeared during sync action %d", ref, action.Sequence)
		}
		if now != before && !allowed[ref] {
			return fmt.Errorf("branch %s moved during sync action %d outside its attributable ref set; recovery evidence is preserved", ref, action.Sequence)
		}
		if now != before {
			if err := verifySyncRebaseRefEvidence(tx, action, repo, ref, now); err != nil {
				return err
			}
		}
	}
	holders, err := syncTransactionHolderValues(repo.Root)
	if err != nil {
		return err
	}
	beforeHolders := syncTransactionHolderMap(action.BeforeHolders)
	afterHolderMap := syncTransactionHolderMap(holders)
	if len(beforeHolders) != len(afterHolderMap) {
		return fmt.Errorf("worktree holder set changed during sync action %d", action.Sequence)
	}
	for _, holder := range holders {
		before, ok := beforeHolders[holder.Path]
		if !ok {
			return fmt.Errorf("worktree holder %s appeared during sync action %d", holder.Path, action.Sequence)
		}
		if before != holder && canonicalize(holder.Path) != action.ContextPath {
			return fmt.Errorf("worktree holder %s changed during sync action %d outside its execution context", holder.Path, action.Sequence)
		}
	}
	contextAfter, ok := afterHolderMap[action.ContextPath]
	if !ok {
		return fmt.Errorf("sync action context %s disappeared during action %d", action.ContextPath, action.Sequence)
	}
	if contextAfter != action.ContextBefore {
		started, err := syncTransactionHasActionStart(tx, action)
		if err != nil {
			return err
		}
		transitioned, err := syncTransactionHasContextTransition(tx, action, contextAfter)
		if err != nil {
			return err
		}
		targetSHA := afterMap[action.ContextRef]
		if action.ContextRef == "" || contextAfter.Detached ||
			"refs/heads/"+contextAfter.Branch != action.ContextRef ||
			contextAfter.HEAD != targetSHA || (!started && !transitioned) {
			return fmt.Errorf("sync action context %s changed without this run's native transition evidence; preserve the journal and recover the checkout manually", action.ContextPath)
		}
	}
	for i := range repo.Refs {
		if now, ok := afterMap[repo.Refs[i].Ref]; ok {
			repo.Refs[i].ExpectedSHA = now
			repo.Refs[i].Moved = now != repo.Refs[i].PreimageSHA
		}
	}
	holderMap := syncTransactionHolderMap(holders)
	for i := range repo.Holders {
		if now, ok := holderMap[repo.Holders[i].Path]; ok {
			repo.Holders[i].ExpectedBranch = now.Branch
			repo.Holders[i].ExpectedHEAD = now.HEAD
			repo.Holders[i].ExpectedDetached = now.Detached
		}
	}
	action.AfterRefs = after
	action.AfterHolders = holders
	action.Status = SyncTxnActionObserved
	if commandErr != nil {
		action.Status = SyncTxnActionFailed
		action.Error = commandErr.Error()
		tx.Phase = SyncTxnFailed
	}
	if err := save(); err != nil {
		return fmt.Errorf("persist sync action result: %w", err)
	}
	return syncTransactionStep("action-observed:" + action.Kind + ":" + action.Entry)
}

func syncTransactionActionMarker(tx *SyncTransaction, action *SyncTransactionAction) string {
	return "tws-sync:" + tx.RunID + ":" + strconv.Itoa(action.Sequence)
}

func syncTransactionRebaseEnv(tx *SyncTransaction) []string {
	if tx == nil || len(tx.Actions) == 0 {
		return nil
	}
	action := &tx.Actions[len(tx.Actions)-1]
	return append(os.Environ(), "GIT_REFLOG_ACTION="+syncTransactionActionMarker(tx, action))
}

func RunSyncContextSwitch(tx *SyncTransaction, dir, branch string) error {
	cmd := exec.Command("git", "checkout", branch)
	cmd.Dir = dir
	cmd.Env = syncTransactionRebaseEnv(tx)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("checkout %s: %s: %w", branch, strings.TrimSpace(string(output)), err)
	}
	return nil
}

func RunSyncRebaseClean(tx *SyncTransaction, dir string, args ...string) error {
	return runWithFilteredStderrEnv(dir, syncTransactionRebaseEnv(tx), "git", args...)
}

func RunSyncRebaseSilent(tx *SyncTransaction, dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = syncTransactionRebaseEnv(tx)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "CONFLICT") || strings.Contains(string(output), "could not apply") {
			return &RebaseConflictError{Output: string(output)}
		}
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return nil
}

func syncTransactionReflog(dir, ref string, count int) ([]string, error) {
	args := []string{"reflog", "show", "--format=%H%x00%gs"}
	if count > 0 {
		args = append(args, "-n", strconv.Itoa(count))
	}
	args = append(args, ref)
	output, stderr, _, err := syncTransactionGit(dir, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("read sync attribution reflog for %s: %s", ref, syncTransactionGitError(stderr, err))
	}
	if len(output) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(string(output), "\n"), "\n"), nil
}

func verifySyncRebaseRefEvidence(tx *SyncTransaction, action *SyncTransactionAction, repo *SyncTransactionRepo, ref, current string) error {
	fail := func() error {
		return fmt.Errorf("ref %s changed without matching this sync rebase's reflog evidence; refusing to adopt later or foreign work", ref)
	}
	log, err := syncTransactionReflog(repo.Root, ref, 2)
	if err != nil {
		return err
	}
	if len(log) != 2 || log[1] != action.RefLogAnchors[ref] {
		return fail()
	}
	tip, message, ok := strings.Cut(log[0], "\x00")
	if !ok || tip != current {
		return fail()
	}
	marker := syncTransactionActionMarker(tx, action)
	if !strings.HasPrefix(message, marker+" (finish): "+ref+" onto ") &&
		!strings.HasPrefix(message, "rebase (finish): "+ref+" onto ") &&
		message != "rewritten during rebase" {
		return fail()
	}
	started, err := syncTransactionHasActionStart(tx, action)
	if err != nil {
		return err
	}
	if started {
		return nil
	}
	return fail()
}

func syncTransactionHasActionStart(tx *SyncTransaction, action *SyncTransactionAction) (bool, error) {
	headLog, err := syncTransactionReflog(action.ContextPath, "HEAD", 0)
	if err != nil {
		return false, err
	}
	marker := syncTransactionActionMarker(tx, action)
	for _, event := range headLog {
		if event == action.ContextReflogAnchor {
			break
		}
		_, eventMessage, valid := strings.Cut(event, "\x00")
		if valid && strings.HasPrefix(eventMessage, marker+" (start): ") {
			return true, nil
		}
		if valid && (strings.Contains(eventMessage, " (start): ") ||
			strings.Contains(eventMessage, " (abort): ")) {
			return false, nil
		}
	}
	return false, nil
}

func syncTransactionHasContextTransition(tx *SyncTransaction, action *SyncTransactionAction, live SyncTransactionHolderValue) (bool, error) {
	headLog, err := syncTransactionReflog(action.ContextPath, "HEAD", 0)
	if err != nil {
		return false, err
	}
	marker := syncTransactionActionMarker(tx, action)
	for _, event := range headLog {
		if event == action.ContextReflogAnchor {
			break
		}
		tip, message, valid := strings.Cut(event, "\x00")
		if valid && tip == live.HEAD && strings.HasPrefix(message, marker) {
			return true, nil
		}
	}
	return false, nil
}

func CompleteSyncGitAction(tx *SyncTransaction, commandErr error, save func() error) error {
	return completeSyncAction(tx, commandErr, save, true)
}

// ReconcileSyncGitAction closes an intent left across a crash or a manually
// completed conflict using current refs and worktree evidence.
func ReconcileSyncGitAction(tx *SyncTransaction, save func() error) error {
	if err := reconcileSyncForwardRestorations(tx, save); err != nil {
		return err
	}
	if err := reconcileSyncTransactionMetadataIntent(tx, save); err != nil {
		return err
	}
	if err := ReconcileSyncValidation(tx, save); err != nil {
		return err
	}
	if _, _, active := SyncTransactionActiveRebase(tx); active {
		return nil
	}
	changed, err := SyncTransactionPendingActionChanged(tx)
	if err != nil {
		return err
	}
	if !changed {
		return completeSyncAction(tx, errors.New("interrupted action has no completed ref change; retry required"), save, true)
	}
	return completeSyncAction(tx, nil, save, true)
}

func syncTransactionValidationRepo(tx *SyncTransaction, validation *SyncTransactionValidation) (*SyncTransactionRepo, error) {
	for i := range tx.Repositories {
		if tx.Repositories[i].CommonDir == validation.RepoCommonDir {
			return &tx.Repositories[i], nil
		}
	}
	return nil, fmt.Errorf("sync validation names unknown repository %s", validation.RepoCommonDir)
}

func BeginSyncValidation(tx *SyncTransaction, repoDir, entry, contextPath string, save func() error) error {
	if tx == nil || !tx.Ready {
		return errors.New("sync transaction snapshot is not ready")
	}
	if err := verifySyncTransactionMetadataImage(tx, false); err != nil {
		return err
	}
	if len(tx.Validations) > 0 {
		last := tx.Validations[len(tx.Validations)-1]
		if last.Status == SyncTxnActionIntent {
			return fmt.Errorf("sync validation %d is still pending reconciliation", last.Sequence)
		}
		if last.Status == SyncTxnActionMutated {
			return fmt.Errorf("validator changed refs or checkout state during validation of %s; preserve the journal and work, then recover manually", last.Entry)
		}
	}
	repo, err := syncTransactionRepo(tx, repoDir)
	if err != nil {
		return err
	}
	refs, err := syncTransactionListHeads(repo.Root)
	if err != nil {
		return err
	}
	if err := verifySyncTransactionRefImage(repo, refs); err != nil {
		return err
	}
	holders, err := syncTransactionHolderValues(repo.Root)
	if err != nil {
		return err
	}
	if err := verifySyncTransactionHolderImage(repo, holders); err != nil {
		return err
	}
	contextPath = canonicalize(contextPath)
	if _, ok := syncTransactionHolderMap(holders)[contextPath]; !ok {
		return fmt.Errorf("validation context %s is not a recorded repository holder", contextPath)
	}
	tx.Validations = append(tx.Validations, SyncTransactionValidation{
		Sequence: len(tx.Validations) + 1, Entry: entry, RepoCommonDir: repo.CommonDir,
		ContextPath: contextPath, BeforeRefs: refs, BeforeHolders: holders,
		Status: SyncTxnActionIntent,
	})
	if err := save(); err != nil {
		return fmt.Errorf("persist validation intent: %w", err)
	}
	return syncTransactionStep("validation-intent:" + entry)
}

func completeSyncValidation(tx *SyncTransaction, commandErr error, save func() error) error {
	if tx == nil || len(tx.Validations) == 0 {
		return nil
	}
	validation := &tx.Validations[len(tx.Validations)-1]
	if validation.Status == SyncTxnActionMutated {
		return fmt.Errorf("validator changed refs or checkout state during validation of %s; preserve the journal and work, then recover manually", validation.Entry)
	}
	if validation.Status != SyncTxnActionIntent {
		return nil
	}
	repo, err := syncTransactionValidationRepo(tx, validation)
	if err != nil {
		return err
	}
	refs, err := syncTransactionListHeads(repo.Root)
	if err != nil {
		return err
	}
	holders, err := syncTransactionHolderValues(repo.Root)
	if err != nil {
		return err
	}
	validation.AfterRefs = refs
	validation.AfterHolders = holders
	mutated := !reflect.DeepEqual(validation.BeforeRefs, refs) || !reflect.DeepEqual(validation.BeforeHolders, holders)
	switch {
	case mutated:
		validation.Status = SyncTxnActionMutated
		validation.Error = "validator changed local refs or checkout attachment/HEAD"
		tx.Phase = SyncTxnFailed
	case commandErr != nil:
		validation.Status = SyncTxnActionFailed
		validation.Error = commandErr.Error()
		tx.Phase = SyncTxnFailed
	default:
		validation.Status = SyncTxnActionObserved
	}
	if err := save(); err != nil {
		return fmt.Errorf("persist validation result: %w", err)
	}
	if err := syncTransactionStep("validation-observed:" + validation.Entry); err != nil {
		return err
	}
	if mutated {
		return fmt.Errorf("validator changed refs or checkout state during validation of %s; tws will not adopt or erase those changes. Preserve the journal and work, then recover manually", validation.Entry)
	}
	return nil
}

func CompleteSyncValidation(tx *SyncTransaction, commandErr error, save func() error) error {
	return completeSyncValidation(tx, commandErr, save)
}

func ReconcileSyncValidation(tx *SyncTransaction, save func() error) error {
	if tx == nil || len(tx.Validations) == 0 {
		return nil
	}
	last := &tx.Validations[len(tx.Validations)-1]
	switch last.Status {
	case SyncTxnActionMutated:
		return fmt.Errorf("validator changed refs or checkout state during validation of %s; preserve the journal and work, then recover manually", last.Entry)
	case SyncTxnActionIntent:
		return completeSyncValidation(tx, errors.New("validation was interrupted before its result was durably recorded"), save)
	default:
		return nil
	}
}

func SyncWriteStackValue(featurePath string, tx *SyncTransaction, stack Stack, save func() error) error {
	data, err := yaml.Marshal(&stack)
	if err != nil {
		return err
	}
	return SyncWriteStack(featurePath, tx, data, save)
}

func SyncWriteStack(featurePath string, tx *SyncTransaction, target []byte, save func() error) error {
	if tx == nil {
		return errors.New("sync transaction is absent")
	}
	live, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		return err
	}
	liveHash := syncTransactionHash(live)
	if tx.Metadata.IntentPending {
		intent, decErr := base64.StdEncoding.DecodeString(tx.Metadata.IntentBase64)
		if decErr != nil || syncTransactionHash(intent) != tx.Metadata.IntentSHA256 {
			return errors.New("sync metadata intent is corrupt")
		}
		switch liveHash {
		case tx.Metadata.IntentSHA256:
			tx.Metadata.ExpectedSHA256 = liveHash
			tx.Metadata.IntentPending = false
			if err := save(); err != nil {
				return err
			}
		case tx.Metadata.ExpectedSHA256:
			// Retry the recorded write below.
		default:
			return fmt.Errorf("stack.yaml changed outside this sync transaction")
		}
	}
	if !tx.Metadata.IntentPending && liveHash != tx.Metadata.ExpectedSHA256 {
		return fmt.Errorf("stack.yaml changed outside this sync transaction")
	}
	targetHash := syncTransactionHash(target)
	if liveHash == targetHash {
		tx.Metadata.ExpectedSHA256 = targetHash
		return save()
	}
	tx.Metadata.IntentBase64 = base64.StdEncoding.EncodeToString(target)
	tx.Metadata.IntentSHA256 = targetHash
	tx.Metadata.IntentPending = true
	if err := save(); err != nil {
		return fmt.Errorf("persist stack metadata intent: %w", err)
	}
	if err := syncTransactionStep("metadata-intent"); err != nil {
		return err
	}
	if err := WriteStackBytesAtomic(featurePath, target); err != nil {
		return err
	}
	if err := syncTransactionStep("metadata-written"); err != nil {
		return err
	}
	check, err := os.ReadFile(StackPath(featurePath))
	if err != nil || syncTransactionHash(check) != targetHash {
		return errors.New("stack.yaml does not match the recorded sync post-image")
	}
	tx.Metadata.ExpectedSHA256 = targetHash
	tx.Metadata.IntentPending = false
	if err := save(); err != nil {
		return fmt.Errorf("persist stack metadata completion: %w", err)
	}
	return nil
}

func SyncPreparePublication(tx *SyncTransaction, repoDir, entry, branch string) (SyncTransactionPushIntent, error) {
	if tx == nil {
		return SyncTransactionPushIntent{}, errors.New("sync transaction is absent")
	}
	if err := verifySyncTransactionMetadataImage(tx, false); err != nil {
		return SyncTransactionPushIntent{}, err
	}
	repo, err := syncTransactionRepo(tx, repoDir)
	if err != nil {
		return SyncTransactionPushIntent{}, err
	}
	refs, err := syncTransactionListHeads(repo.Root)
	if err != nil {
		return SyncTransactionPushIntent{}, err
	}
	if err := verifySyncTransactionRefImage(repo, refs); err != nil {
		return SyncTransactionPushIntent{}, err
	}
	holders, err := syncTransactionHolderValues(repo.Root)
	if err != nil {
		return SyncTransactionPushIntent{}, err
	}
	if err := verifySyncTransactionHolderImage(repo, holders); err != nil {
		return SyncTransactionPushIntent{}, err
	}
	destination := "refs/heads/" + branch
	for _, intent := range tx.Publication.Intents {
		if intent.Entry != entry {
			continue
		}
		if intent.RepoCommonDir != repo.CommonDir || intent.DestinationRef != destination {
			return SyncTransactionPushIntent{}, fmt.Errorf("publication identity for %s changed across retry", entry)
		}
		return intent, nil
	}
	for _, ref := range repo.Refs {
		if ref.Ref == destination {
			return SyncTransactionPushIntent{
				Entry: entry, RepoCommonDir: repo.CommonDir,
				DestinationRef: destination, SourceSHA: ref.ExpectedSHA,
			}, nil
		}
	}
	return SyncTransactionPushIntent{}, fmt.Errorf("publication destination %s has no recorded transaction ref", destination)
}

func SyncBeginPublication(tx *SyncTransaction, intent SyncTransactionPushIntent, save func() error) error {
	if tx == nil {
		return errors.New("sync transaction is absent")
	}
	if intent.Entry == "" || intent.RepoCommonDir == "" || intent.DestinationRef == "" || intent.SourceSHA == "" {
		return errors.New("publication intent is incomplete")
	}
	found := false
	for _, recorded := range tx.Publication.Intents {
		if recorded.Entry == intent.Entry {
			if recorded != intent {
				return fmt.Errorf("publication intent for %s changed across retry", intent.Entry)
			}
			found = true
			break
		}
	}
	if !found {
		tx.Publication.Intents = append(tx.Publication.Intents, intent)
	}
	if !tx.Publication.IntentDurable {
		tx.Publication.IntentDurable = true
		tx.Publication.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	tx.Publication.CurrentEntry = intent.Entry
	current := intent
	tx.Publication.Current = &current
	tx.Phase = SyncTxnPublished
	if err := save(); err != nil {
		return fmt.Errorf("persist publication intent: %w", err)
	}
	return syncTransactionStep("publication-intent:" + intent.Entry)
}

func SyncFinishPublicationAttempt(tx *SyncTransaction, entry string, attemptErr error, save func() error) error {
	if tx == nil || tx.Publication.Current == nil || tx.Publication.Current.Entry != entry {
		return fmt.Errorf("publication result for %s has no matching durable intent", entry)
	}
	result := SyncTransactionPushResult{Entry: entry, Success: attemptErr == nil}
	if attemptErr != nil {
		result.Detail = attemptErr.Error()
	}
	tx.Publication.Results = append(tx.Publication.Results, result)
	tx.Publication.CurrentEntry = ""
	tx.Publication.Current = nil
	if err := save(); err != nil {
		return fmt.Errorf("persist publication result: %w", err)
	}
	return syncTransactionStep("publication-result:" + entry)
}

func SyncTransactionPublished(tx *SyncTransaction) bool {
	return tx != nil && tx.Publication.IntentDurable
}

func SyncTransactionEntryPublished(tx *SyncTransaction, entry string) bool {
	if tx == nil {
		return false
	}
	for _, result := range tx.Publication.Results {
		if result.Entry == entry && result.Success {
			return true
		}
	}
	return false
}

func SyncTransactionCleanupOnly(tx *SyncTransaction) bool {
	return tx != nil && tx.Phase == SyncTxnCleanup && tx.Completion != ""
}

func SyncTransactionRollingBack(tx *SyncTransaction) bool {
	return tx != nil && (tx.Phase == SyncTxnRollingBack || tx.Rollback.StartedAt != "")
}

func ReportSyncTransactionCleanup(tx *SyncTransaction, prose io.Writer) {
	if tx == nil || prose == nil {
		return
	}
	if tx.Completion == "cancelled-before-mutation" {
		fmt.Fprintln(prose, "Sync stopped before mutation; finished cancellation cleanup without changing refs or metadata.") //nolint:errcheck
		return
	}
	fmt.Fprintln(prose, "Sync already completed; finished cleanup only, without rolling back refs or metadata.") //nolint:errcheck
}

func ValidateSyncFetchDoesNotWriteLocalBranches(repoDir string) error {
	stdout, stderr, code, err := syncTransactionGit(repoDir, nil, "config", "--get-regexp", `^remote\..*\.fetch$`)
	if err != nil && code != 1 {
		return fmt.Errorf("inspect fetch refspecs in %s: %s", repoDir, syncTransactionGitError(stderr, err))
	}
	for _, line := range strings.Split(string(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		refspec := strings.TrimPrefix(fields[len(fields)-1], "+")
		if strings.HasPrefix(refspec, "^") {
			continue
		}
		parts := strings.SplitN(refspec, ":", 2)
		if len(parts) == 2 && parts[1] != "" &&
			!strings.HasPrefix(parts[1], "refs/remotes/") && !strings.HasPrefix(parts[1], "refs/tags/") {
			return fmt.Errorf("fetch refspec %q can update refs outside remote-tracking/tag inputs; transactional sync refuses before fetch", fields[len(fields)-1])
		}
	}
	return nil
}

func syncTransactionHolderDirty(path string) (bool, error) {
	stdout, stderr, _, err := syncTransactionGit(path, nil, "status", "--porcelain=v1", "--untracked-files=no")
	if err != nil {
		return false, fmt.Errorf("inspect holder %s: %s", path, syncTransactionGitError(stderr, err))
	}
	return len(bytes.TrimSpace(stdout)) != 0, nil
}

func syncTransactionOperation(path string) (string, error) {
	for _, op := range []struct{ name, gitPath string }{{"rebase", "rebase-merge"}, {"rebase", "rebase-apply"}, {"merge", "MERGE_HEAD"}, {"cherry-pick", "CHERRY_PICK_HEAD"}, {"revert", "REVERT_HEAD"}} {
		stdout, stderr, _, err := syncTransactionGit(path, nil, "rev-parse", "--git-path", op.gitPath)
		if err != nil {
			return "", fmt.Errorf("inspect Git operation in %s: %s", path, syncTransactionGitError(stderr, err))
		}
		p := strings.TrimSpace(string(stdout))
		if !filepath.IsAbs(p) {
			p = filepath.Join(path, p)
		}
		if _, statErr := os.Stat(p); statErr == nil {
			return op.name, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
	}
	return "", nil
}

func syncTransactionPreflight(tx *SyncTransaction) error {
	if len(tx.Validations) > 0 && tx.Validations[len(tx.Validations)-1].Status == SyncTxnActionMutated {
		last := tx.Validations[len(tx.Validations)-1]
		return fmt.Errorf("validator changed refs or checkout state during validation of %s; automatic rollback is refused. Preserve the journal and work, then recover manually", last.Entry)
	}
	liveMetadata, err := syncTransactionReadMetadata(tx)
	if err != nil {
		return fmt.Errorf("read stack.yaml for rollback: %w", err)
	}
	hash := syncTransactionHash(liveMetadata)
	if tx.Metadata.Restored && hash != tx.Metadata.BeforeSHA256 {
		return fmt.Errorf("stack.yaml changed after its rollback completed; tws will not overwrite or claim it restored")
	}
	if hash != tx.Metadata.BeforeSHA256 && hash != tx.Metadata.ExpectedSHA256 && hash != tx.Metadata.IntentSHA256 {
		return fmt.Errorf("stack.yaml changed after this sync; tws will not overwrite it")
	}
	activeContext := ""
	if len(tx.Actions) > 0 && tx.Actions[len(tx.Actions)-1].Status == SyncTxnActionIntent {
		activeContext = tx.Actions[len(tx.Actions)-1].ContextPath
	}
	for ri := range tx.Repositories {
		repo := &tx.Repositories[ri]
		_, common, width, identityErr := syncTransactionRepoIdentity(repo.Root)
		if identityErr != nil {
			return identityErr
		}
		if common != repo.CommonDir || width != repo.OIDWidth {
			return fmt.Errorf("repository identity changed at %s; rollback refused", repo.Root)
		}
		for fi := range repo.Refs {
			ref := &repo.Refs[fi]
			if !ref.Pinned || repo.PinsRemoved {
				continue
			}
			pin, found, pinErr := syncTransactionResolveRef(repo.Root, ref.PinRef)
			if pinErr == nil && !found && ref.PinDeletePending {
				continue
			}
			if pinErr != nil || !found || pin != ref.PreimageSHA {
				return fmt.Errorf("sync snapshot pin %s is missing or changed; rollback cannot prove its preimage", ref.PinRef)
			}
		}
		live, listErr := syncTransactionListHeads(repo.Root)
		if listErr != nil {
			return listErr
		}
		got := syncTransactionValuesMap(live)
		if len(got) != len(repo.Refs) {
			return fmt.Errorf("local branch set changed after this sync; tws will not erase new or deleted refs")
		}
		for _, ref := range repo.Refs {
			have, ok := got[ref.Ref]
			if !ok || (have != ref.PreimageSHA && have != ref.ExpectedSHA) {
				if !ok {
					have = "(missing)"
				}
				return fmt.Errorf("ref %s has later or foreign work (expected %s or %s, found %s); rollback refused", ref.Ref, ref.PreimageSHA, ref.ExpectedSHA, have)
			}
		}
		holders, holderErr := syncTransactionHolderValues(repo.Root)
		if holderErr != nil {
			return holderErr
		}
		byPath := syncTransactionHolderMap(holders)
		if len(byPath) != len(repo.Holders) {
			return fmt.Errorf("worktree holder set changed in %s; rollback refused", repo.Root)
		}
		for _, holder := range repo.Holders {
			liveHolder, ok := byPath[holder.Path]
			if !ok {
				return fmt.Errorf("recorded holder %s is missing; rollback refused", holder.Path)
			}
			_, common, _, identityErr := syncTransactionRepoIdentity(holder.Path)
			if identityErr != nil {
				return identityErr
			}
			if common != repo.CommonDir {
				return fmt.Errorf("holder %s changed repository identity; rollback refused", holder.Path)
			}
			if holder.OriginalDetached && holder.OriginalHeadPinned {
				pin := syncTransactionPinRef(tx.RunID, repo.CommonDir, "holder:"+holder.Path)
				sha, found, err := syncTransactionResolveRef(repo.Root, pin)
				if err != nil {
					return err
				}
				if (!found && !holder.HeadPinDeletePending) || found && sha != holder.OriginalHEAD {
					return fmt.Errorf("original detached HEAD pin %s is missing or changed", pin)
				}
			}
			op, opErr := syncTransactionOperation(holder.Path)
			if opErr != nil {
				return opErr
			}
			ownedRebase := op == "rebase" && canonicalize(holder.Path) == activeContext
			if op != "" && !ownedRebase {
				return fmt.Errorf("a %s is in progress in holder %s; rollback refused", op, holder.Path)
			}
			if ownedRebase {
				action := &tx.Actions[len(tx.Actions)-1]
				started, err := syncTransactionHasActionStart(tx, action)
				if err != nil {
					return err
				}
				if !started {
					return fmt.Errorf("active rebase in %s lacks this sync action's start evidence; rollback refused", holder.Path)
				}
			}
			if op == "" {
				dirty, dirtyErr := syncTransactionHolderDirty(holder.Path)
				if dirtyErr != nil {
					return dirtyErr
				}
				if dirty {
					return fmt.Errorf("holder %s has tracked modifications; rollback refused without erasing them", holder.Path)
				}
			}
			matchesExpected := liveHolder.Branch == holder.ExpectedBranch && liveHolder.HEAD == holder.ExpectedHEAD && liveHolder.Detached == holder.ExpectedDetached
			matchesOriginal := liveHolder.Branch == holder.OriginalBranch && liveHolder.HEAD == holder.OriginalHEAD && liveHolder.Detached == holder.OriginalDetached
			matchesDetachIntent := holder.DetachPending && liveHolder.Detached &&
				liveHolder.Branch == "" && liveHolder.HEAD == holder.ExpectedHEAD
			pendingContextPosition := false
			if canonicalize(holder.Path) == activeContext && liveHolder.Branch != "" {
				for _, ref := range repo.Refs {
					if ref.Ref == "refs/heads/"+liveHolder.Branch && liveHolder.HEAD == ref.ExpectedSHA {
						pendingContextPosition = true
						break
					}
				}
			}
			if !matchesExpected && !matchesOriginal && !matchesDetachIntent && !pendingContextPosition && !ownedRebase {
				return fmt.Errorf("holder %s changed after this sync; rollback refused", holder.Path)
			}
		}
	}
	return nil
}

func syncTransactionRunUpdateRef(repo *SyncTransactionRepo, lines []string, message string) error {
	if len(lines) == 0 {
		return nil
	}
	payload := "start\n" + strings.Join(lines, "\n") + "\nprepare\ncommit\n"
	_, stderr, _, err := syncTransactionGit(repo.Root, []byte(payload), "update-ref", "--no-deref", "-m", message, "--stdin")
	if err != nil {
		return fmt.Errorf("reference transaction failed in %s: %s", repo.Root, syncTransactionGitError(stderr, err))
	}
	return nil
}

func syncTransactionDetachAffectedHolders(tx *SyncTransaction, save func() error) error {
	for ri := range tx.Repositories {
		repo := &tx.Repositories[ri]
		moved := map[string]bool{}
		for _, ref := range repo.Refs {
			if ref.ExpectedSHA != ref.PreimageSHA {
				moved[strings.TrimPrefix(ref.Ref, "refs/heads/")] = true
			}
		}
		for hi := range repo.Holders {
			holder := &repo.Holders[hi]
			if holder.Detached || holder.ExpectedDetached || !moved[holder.ExpectedBranch] {
				continue
			}
			holder.DetachPending = true
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("rollback-holder-detach-intent:" + holder.Path); err != nil {
				return err
			}
			values, err := syncTransactionHolderValues(repo.Root)
			if err != nil {
				return err
			}
			position, ok := syncTransactionHolderMap(values)[holder.Path]
			if !ok || position.HEAD != holder.ExpectedHEAD {
				return fmt.Errorf("holder %s changed before detach", holder.Path)
			}
			if !position.Detached {
				if position.Branch != holder.ExpectedBranch {
					return fmt.Errorf("holder %s changed branch before detach", holder.Path)
				}
				_, stderr, _, switchErr := syncTransactionGit(holder.Path, nil, "switch", "--detach", holder.ExpectedHEAD)
				if switchErr != nil {
					return fmt.Errorf("detach holder %s: %s", holder.Path, syncTransactionGitError(stderr, switchErr))
				}
			}
			if err := syncTransactionStep("rollback-holder-detach-applied:" + holder.Path); err != nil {
				return err
			}
			holder.ExpectedBranch = ""
			holder.ExpectedDetached = true
			holder.Detached = true
			holder.DetachPending = false
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("rollback-holder-detached:" + holder.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncTransactionRestoreRefs(tx *SyncTransaction, save func() error) error {
	for ri := range tx.Repositories {
		repo := &tx.Repositories[ri]
		if repo.RefsRestored {
			continue
		}
		live, err := syncTransactionListHeads(repo.Root)
		if err != nil {
			return err
		}
		got := syncTransactionValuesMap(live)
		var lines []string
		for _, ref := range repo.Refs {
			have, ok := got[ref.Ref]
			if !ok {
				return fmt.Errorf("ref %s disappeared before rollback", ref.Ref)
			}
			switch have {
			case ref.PreimageSHA:
				lines = append(lines, fmt.Sprintf("verify %s %s", ref.Ref, ref.PreimageSHA))
			case ref.ExpectedSHA:
				lines = append(lines, fmt.Sprintf("update %s %s %s", ref.Ref, ref.PreimageSHA, ref.ExpectedSHA))
			default:
				return fmt.Errorf("ref %s changed before rollback (found %s); no refs were changed in repository %s", ref.Ref, have, repo.Root)
			}
		}
		tx.Rollback.Stage = "refs-intent:" + repo.CommonDir
		if err := save(); err != nil {
			return err
		}
		if err := syncTransactionStep("rollback-refs-intent:" + repo.CommonDir); err != nil {
			return err
		}
		if err := syncTransactionRunUpdateRef(repo, lines, "tws sync abort "+tx.Feature+" "+tx.RunID); err != nil {
			return err
		}
		if err := syncTransactionStep("rollback-refs-applied:" + repo.CommonDir); err != nil {
			return err
		}
		for i := range repo.Refs {
			repo.Refs[i].ExpectedSHA = repo.Refs[i].PreimageSHA
			repo.Refs[i].Moved = false
			repo.Refs[i].Restored = true
		}
		repo.RefsRestored = true
		tx.Rollback.CompletedRepos = appendUnique(tx.Rollback.CompletedRepos, repo.CommonDir)
		if err := save(); err != nil {
			return err
		}
		if err := syncTransactionStep("rollback-refs-restored:" + repo.CommonDir); err != nil {
			return err
		}
	}
	return nil
}

func syncTransactionRestoreMetadata(tx *SyncTransaction, save func() error) error {
	if tx.Rollback.MetadataRestored {
		return nil
	}
	before, err := base64.StdEncoding.DecodeString(tx.Metadata.BeforeBase64)
	if err != nil {
		return errors.New("saved stack.yaml preimage does not decode")
	}
	live, err := os.ReadFile(tx.Metadata.Path)
	if err != nil {
		return err
	}
	hash := syncTransactionHash(live)
	if hash != tx.Metadata.BeforeSHA256 {
		if hash != tx.Metadata.ExpectedSHA256 && hash != tx.Metadata.IntentSHA256 {
			return errors.New("stack.yaml changed before rollback; exact metadata restore refused")
		}
		tx.Rollback.Stage = "metadata-intent"
		if err := save(); err != nil {
			return err
		}
		if err := syncTransactionStep("rollback-metadata-intent"); err != nil {
			return err
		}
		if err := durableWriteFile(tx.Metadata.Path, before, 0644); err != nil {
			return err
		}
		if err := syncTransactionStep("rollback-metadata-applied"); err != nil {
			return err
		}
	}
	tx.Metadata.ExpectedSHA256 = tx.Metadata.BeforeSHA256
	tx.Metadata.IntentPending = false
	tx.Metadata.Restored = true
	tx.Rollback.MetadataRestored = true
	if err := save(); err != nil {
		return err
	}
	return syncTransactionStep("rollback-metadata-restored")
}

func syncTransactionRestoreHolders(tx *SyncTransaction, save func() error) error {
	for ri := range tx.Repositories {
		repo := &tx.Repositories[ri]
		for hi := range repo.Holders {
			holder := &repo.Holders[hi]
			if holder.Restored {
				continue
			}
			values, err := syncTransactionHolderValues(repo.Root)
			if err != nil {
				return err
			}
			live, ok := syncTransactionHolderMap(values)[holder.Path]
			if !ok {
				return fmt.Errorf("recorded holder %s is missing during restoration", holder.Path)
			}
			if live.Branch == holder.OriginalBranch && live.HEAD == holder.OriginalHEAD && live.Detached == holder.OriginalDetached {
				holder.ExpectedBranch = holder.OriginalBranch
				holder.ExpectedHEAD = holder.OriginalHEAD
				holder.ExpectedDetached = holder.OriginalDetached
				holder.RestorePending = false
				holder.Restored = true
				tx.Rollback.HoldersRestored = appendUnique(tx.Rollback.HoldersRestored, holder.Path)
				if err := save(); err != nil {
					return err
				}
				continue
			}
			if op, opErr := syncTransactionOperation(holder.Path); opErr != nil {
				return opErr
			} else if op != "" {
				return fmt.Errorf("a %s is in progress in holder %s during restoration", op, holder.Path)
			}
			if dirty, dirtyErr := syncTransactionHolderDirty(holder.Path); dirtyErr != nil {
				return dirtyErr
			} else if dirty {
				return fmt.Errorf("holder %s became dirty during rollback; restoration refused", holder.Path)
			}
			tx.Rollback.Stage = "holder-restore-intent:" + holder.Path
			holder.RestorePending = true
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("rollback-holder-restore-intent:" + holder.Path); err != nil {
				return err
			}
			args := []string{"switch", holder.OriginalBranch}
			if holder.OriginalDetached {
				args = []string{"switch", "--detach", holder.OriginalHEAD}
			}
			_, stderr, _, runErr := syncTransactionGit(holder.Path, nil, args...)
			if runErr != nil {
				return fmt.Errorf("restore holder %s: %s", holder.Path, syncTransactionGitError(stderr, runErr))
			}
			if err := syncTransactionStep("rollback-holder-restore-applied:" + holder.Path); err != nil {
				return err
			}
			holder.ExpectedBranch = holder.OriginalBranch
			holder.ExpectedHEAD = holder.OriginalHEAD
			holder.ExpectedDetached = holder.OriginalDetached
			holder.Restored = true
			holder.RestorePending = false
			tx.Rollback.HoldersRestored = appendUnique(tx.Rollback.HoldersRestored, holder.Path)
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("rollback-holder-restored:" + holder.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncTransactionDeletePins(tx *SyncTransaction, save func() error) error {
	for ri := range tx.Repositories {
		repo := &tx.Repositories[ri]
		if repo.PinsRemoved {
			continue
		}
		_, common, width, err := syncTransactionRepoIdentity(repo.Root)
		if err != nil {
			return err
		}
		if common != repo.CommonDir || width != repo.OIDWidth {
			return fmt.Errorf("repository identity changed before sync pin cleanup at %s", repo.Root)
		}
		if err := deleteSyncDetachedHeadPins(tx, repo, save); err != nil {
			return err
		}
		for fi := range repo.Refs {
			ref := &repo.Refs[fi]
			live, found, err := syncTransactionResolveRef(repo.Root, ref.PinRef)
			if err != nil {
				return err
			}
			if !found {
				if ref.Pinned && !ref.PinDeletePending {
					return fmt.Errorf("sync pin %s disappeared before cleanup intent", ref.PinRef)
				}
				ref.Pinned = false
				ref.PinDeletePending = false
				if err := save(); err != nil {
					return err
				}
				continue
			}
			if live != ref.PreimageSHA {
				return fmt.Errorf("refusing to delete changed sync pin %s", ref.PinRef)
			}
			ref.PinDeletePending = true
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("rollback-pin-delete-intent:" + ref.PinRef); err != nil {
				return err
			}
			_, stderr, _, runErr := syncTransactionGit(repo.Root, nil, "update-ref", "--no-deref", "-d", ref.PinRef, ref.PreimageSHA)
			if runErr != nil {
				return fmt.Errorf("delete sync pin %s: %s", ref.PinRef, syncTransactionGitError(stderr, runErr))
			}
			if err := syncTransactionStep("rollback-pin-deleted:" + ref.PinRef); err != nil {
				return err
			}
			ref.Pinned = false
			ref.PinDeletePending = false
			if err := save(); err != nil {
				return err
			}
		}
		repo.PinsRemoved = true
		tx.Rollback.PinsRemovedRepos = appendUnique(tx.Rollback.PinsRemovedRepos, repo.CommonDir)
		if err := save(); err != nil {
			return err
		}
	}
	return nil
}

// AbortSyncTransaction restores every run-owned local effect. The caller owns
// compatibility artifact and lock cleanup only after this returns nil.
func AbortSyncTransaction(tx *SyncTransaction, save func() error, prose io.Writer) error {
	if tx == nil {
		return errors.New("transactional sync evidence is absent")
	}
	if err := ValidateSyncTransaction(tx, tx.Feature, WorkspaceMode(tx.WorkspaceMode)); err != nil {
		return err
	}
	if SyncTransactionCleanupOnly(tx) {
		if err := syncTransactionDeletePins(tx, save); err != nil {
			return err
		}
		ReportSyncTransactionCleanup(tx, prose)
		return nil
	}
	if tx.Publication.IntentDurable {
		return fmt.Errorf("sync run %s crossed the publication boundary before push entry %q; local rollback is refused. Preserve this evidence and use --continue or recover remote/local refs manually", tx.RunID, tx.Publication.CurrentEntry)
	}
	if !SyncTransactionRollingBack(tx) {
		if err := ReconcileSyncGitAction(tx, save); err != nil {
			return err
		}
	}
	if err := syncTransactionPreflight(tx); err != nil {
		return err
	}
	if tx.Phase == SyncTxnCleanup && SyncTransactionRollingBack(tx) {
		return syncTransactionDeletePins(tx, save)
	}
	if tx.Rollback.StartedAt == "" {
		tx.Rollback.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	for i := range tx.Repositories {
		for j := range tx.Repositories[i].Holders {
			tx.Repositories[i].Holders[j].ForwardRestoreTarget = nil
		}
	}
	tx.Rollback.Stage = "preflight-complete"
	tx.Phase = SyncTxnRollingBack
	if err := save(); err != nil {
		return err
	}
	if err := syncTransactionStep("rollback-started"); err != nil {
		return err
	}

	if len(tx.Actions) > 0 {
		action := &tx.Actions[len(tx.Actions)-1]
		if action.Status == SyncTxnActionIntent && gitRebaseInProgress(action.ContextPath) {
			tx.Rollback.Stage = "rebase-abort-intent:" + action.Entry
			if err := save(); err != nil {
				return err
			}
			if err := syncTransactionStep("rollback-rebase-abort-intent:" + action.Entry); err != nil {
				return err
			}
			_, stderr, _, runErr := syncTransactionGit(action.ContextPath, nil, "rebase", "--abort")
			if runErr != nil {
				return fmt.Errorf("abort rebase in %s: %s", action.ContextPath, syncTransactionGitError(stderr, runErr))
			}
		}
		if err := completeSyncAction(tx, nil, save, false); err != nil {
			return err
		}
	}
	if err := syncTransactionPreflight(tx); err != nil {
		return err
	}
	if err := syncTransactionDetachAffectedHolders(tx, save); err != nil {
		return err
	}
	if err := syncTransactionRestoreRefs(tx, save); err != nil {
		return err
	}
	if err := syncTransactionRestoreMetadata(tx, save); err != nil {
		return err
	}
	if err := syncTransactionRestoreHolders(tx, save); err != nil {
		return err
	}
	tx.Phase = SyncTxnCleanup
	tx.Rollback.Stage = "cleanup"
	if err := save(); err != nil {
		return err
	}
	if err := syncTransactionDeletePins(tx, save); err != nil {
		return err
	}
	if prose != nil {
		fmt.Fprintf(prose, "Rollback restored %d repository ref set(s), exact stack metadata, and recorded checkout holders.\n", len(tx.Repositories)) //nolint:errcheck
	}
	return nil
}

// CompleteSyncTransaction removes GC pins after all forward effects have
// succeeded. The enclosing payload remains until this returns nil.
func CompleteSyncTransaction(tx *SyncTransaction, save func() error) error {
	return finishSyncTransaction(tx, "forward-complete", save)
}

func CancelSyncTransaction(tx *SyncTransaction, save func() error) error {
	if tx == nil || len(tx.Actions) != 0 || tx.Metadata.ExpectedSHA256 != tx.Metadata.BeforeSHA256 || tx.Publication.IntentDurable {
		return errors.New("cannot cancel a sync transaction that may have mutated state")
	}
	return finishSyncTransaction(tx, "cancelled-before-mutation", save)
}

func finishSyncTransaction(tx *SyncTransaction, completion string, save func() error) error {
	if tx == nil {
		return nil
	}
	if SyncTransactionRollingBack(tx) {
		return errors.New("rollback has started; forward cleanup is disabled, re-run --abort")
	}
	tx.Phase = SyncTxnCleanup
	if tx.Completion == "" {
		tx.Completion = completion
	}
	if err := save(); err != nil {
		return err
	}
	if err := syncTransactionStep("cleanup-started"); err != nil {
		return err
	}
	return syncTransactionDeletePins(tx, save)
}

// SyncTransactionActiveRebase reports an unfinished rebase owned by the last
// durable action intent.
func SyncTransactionActiveRebase(tx *SyncTransaction) (entry, path string, active bool) {
	if tx == nil || len(tx.Actions) == 0 {
		return "", "", false
	}
	action := &tx.Actions[len(tx.Actions)-1]
	if action.Status != SyncTxnActionIntent || action.ContextPath == "" {
		return "", "", false
	}
	return action.Entry, action.ContextPath, gitRebaseInProgress(action.ContextPath)
}

// SyncTransactionPendingActionChanged reports whether the pending action has
// produced an observable local-ref change. A false result is safe to retry.
func SyncTransactionPendingActionChanged(tx *SyncTransaction) (bool, error) {
	if tx == nil || len(tx.Actions) == 0 {
		return false, nil
	}
	action := &tx.Actions[len(tx.Actions)-1]
	if action.Status != SyncTxnActionIntent {
		return false, nil
	}
	repo, err := syncTransactionActionRepo(tx, action)
	if err != nil {
		return false, err
	}
	live, err := syncTransactionListHeads(repo.Root)
	if err != nil {
		return false, err
	}
	before := syncTransactionValuesMap(action.BeforeRefs)
	after := syncTransactionValuesMap(live)
	if len(before) != len(after) {
		return true, nil
	}
	for ref, sha := range before {
		if after[ref] != sha {
			return true, nil
		}
	}
	return false, nil
}

func SyncTransactionEntryObserved(tx *SyncTransaction, entry string) bool {
	if tx == nil {
		return false
	}
	for i := len(tx.Actions) - 1; i >= 0; i-- {
		if tx.Actions[i].Entry == entry && tx.Actions[i].Kind == "rebase" {
			return tx.Actions[i].Status == SyncTxnActionObserved
		}
	}
	return false
}
