package internal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

func readSyncStateFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("sync state path %s is not a regular non-symlink file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("sync state path %s changed while opening it", path)
	}
	return io.ReadAll(f)
}

func syncEnvelopeRoot(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not allowed")
		}
		return nil, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, errors.New("state must contain exactly one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("state document must be a mapping")
	}
	if err := validateSyncYAMLNode(root); err != nil {
		return nil, err
	}
	return root, nil
}

func validateSyncYAMLNode(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode {
				return errors.New("state document contains a non-scalar key")
			}
			if key.Value == "<<" {
				return errors.New("YAML merge keys are not allowed in state")
			}
			if seen[key.Value] {
				return fmt.Errorf("field %q appears more than once", key.Value)
			}
			seen[key.Value] = true
			if err := validateSyncYAMLNode(node.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateSyncYAMLNode(child); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return errors.New("YAML aliases are not allowed in state")
	}
	return nil
}

func syncEnvelopeField(root *yaml.Node, name string) (*yaml.Node, bool) {
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == name {
			return root.Content[i+1], true
		}
	}
	return nil, false
}

func syncEnvelopeVersion(root *yaml.Node) (int, error) {
	node, ok := syncEnvelopeField(root, "state_version")
	if !ok {
		return 0, nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, errors.New("state_version must be an integer")
	}
	var version int
	if err := node.Decode(&version); err != nil {
		return 0, fmt.Errorf("decode state_version: %w", err)
	}
	return version, nil
}

func syncEnvelopeString(root *yaml.Node, name string) string {
	node, ok := syncEnvelopeField(root, name)
	if !ok || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return ""
	}
	return node.Value
}

func decodeStrictSyncEnvelope(data []byte, out any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple YAML documents are not allowed")
		}
		return err
	}
	return nil
}

func rejectLegacyTransactionalFields(root *yaml.Node) error {
	for _, field := range []string{"transaction", "plan_guarded"} {
		if _, ok := syncEnvelopeField(root, field); ok {
			return fmt.Errorf("legacy sync state must not contain transactional field %q", field)
		}
	}
	return nil
}

func validateTransactionalFeaturePath(featurePath, feature string) error {
	if feature == "" {
		return errors.New("feature is absent")
	}
	if filepath.Base(filepath.Clean(featurePath)) != feature {
		return fmt.Errorf("feature %q does not match state path %q", feature, featurePath)
	}
	return nil
}

func validateTransactionalTimestamp(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is absent", field)
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%s is invalid: %w", field, err)
	}
	return nil
}

func validateTransactionalLimits(version, guardedVersion int, planGuarded bool, perEntry, total *int) error {
	guarded := version == guardedVersion
	if guarded != planGuarded {
		return errors.New("plan_guarded does not match state_version")
	}
	for name, limit := range map[string]*int{
		"max_replay_per_entry": perEntry,
		"max_replay_total":     total,
	} {
		if limit != nil && *limit < 0 {
			return fmt.Errorf("%s must be zero or greater", name)
		}
	}
	if guarded {
		if perEntry == nil && total == nil {
			return errors.New("guarded state has no replay limit")
		}
		return nil
	}
	if perEntry != nil || total != nil {
		return errors.New("unguarded state carries replay limits")
	}
	return nil
}

func validateTransactionalPolicy(mode WorkspaceMode, route string, policy SyncRunPolicy, selected []string) error {
	if route != RouteLegacy && route != RouteNewMode {
		return fmt.Errorf("route %q is unsupported", route)
	}
	if policy.Fetch != SyncFetchEnabled && policy.Fetch != SyncFetchDisabled {
		return fmt.Errorf("fetch policy %q is unsupported", policy.Fetch)
	}
	if policy.Propagation != SyncPropagationFull && policy.Propagation != SyncPropagationLocalOnly {
		return fmt.Errorf("propagation policy %q is unsupported", policy.Propagation)
	}
	switch policy.ScopeKind {
	case SyncScopeAll:
		if policy.Selector != "" {
			return errors.New("scope all must not carry a selector")
		}
	case SyncScopeOne:
		if policy.Selector == "" {
			return errors.New("scope one requires a selector")
		}
		if len(selected) != 1 || selected[0] != policy.Selector {
			return errors.New("scope one selection does not match its selector")
		}
	case SyncScopeSubtree:
		if policy.Selector == "" {
			return errors.New("scope subtree requires a selector")
		}
		if !syncNamePresent(selected, policy.Selector) {
			return errors.New("scope subtree selection omits its selector")
		}
	default:
		return fmt.Errorf("scope kind %q is unsupported", policy.ScopeKind)
	}
	if route == RouteLegacy {
		expectedFetch := SyncFetchEnabled
		if mode == ModeCheckout {
			expectedFetch = SyncFetchDisabled
		}
		if policy.Fetch != expectedFetch || policy.Propagation != SyncPropagationFull ||
			policy.ScopeKind != SyncScopeAll || policy.Selector != "" {
			return errors.New("legacy route carries a non-legacy frozen policy")
		}
	}
	return nil
}

func validateTransactionalValidation(command, source string) error {
	switch source {
	case "none":
		if command != "" {
			return errors.New("validation source none carries a test command")
		}
	case "config", "flag":
		if command == "" {
			return fmt.Errorf("validation source %q has no test command", source)
		}
	default:
		return fmt.Errorf("validation source %q is unsupported", source)
	}
	return nil
}

func validateTransactionalNames(label string, names []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("%s contains an empty name", label)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s contains duplicate name %q", label, name)
		}
		seen[name] = true
	}
	return seen, nil
}

func validateTransactionalSubset(label string, names []string, selected map[string]bool) (map[string]bool, error) {
	seen, err := validateTransactionalNames(label, names)
	if err != nil {
		return nil, err
	}
	for name := range seen {
		if !selected[name] {
			return nil, fmt.Errorf("%s contains unselected name %q", label, name)
		}
	}
	return seen, nil
}

func validateTransactionalRepos(repos []string) error {
	seen := make(map[string]bool, len(repos))
	for _, repo := range repos {
		if seen[repo] {
			return fmt.Errorf("repos contains duplicate repository %q", repo)
		}
		seen[repo] = true
	}
	return nil
}

func syncNamePresent(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func syncNamesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateTransactionalMetadataPath(featurePath string, tx *SyncTransaction) error {
	expected := filepath.Join(canonicalize(featurePath), filepath.Base(StackPath(featurePath)))
	if tx.Metadata.Path != expected {
		return fmt.Errorf("transaction metadata path %q does not match %q", tx.Metadata.Path, expected)
	}
	return nil
}

func validateSyncRunStateEnvelope(featurePath string, state *SyncRunState) error {
	if state == nil {
		return errors.New("sync run state is nil")
	}
	if state.StateVersion != SyncRunStateTransactionalVersion &&
		state.StateVersion != SyncRunStateTransactionalGuardedVersion {
		return fmt.Errorf("unsupported scoped sync state version %d", state.StateVersion)
	}
	if err := validateTransactionalFeaturePath(featurePath, state.Feature); err != nil {
		return err
	}
	if err := validateTransactionalTimestamp("started_at", state.StartedAt); err != nil {
		return err
	}
	if err := validateTransactionalTimestamp("updated_at", state.UpdatedAt); err != nil {
		return err
	}
	if !isSyncMarker(state.Marker) {
		return errors.New("marker is invalid")
	}
	if !reparentRunIDShape(state.OwnerToken) {
		return errors.New("owner_token is invalid")
	}
	switch state.Stage {
	case SyncStageInitializing, SyncStageFetching, SyncStageRebasing, SyncStageValidating,
		SyncStagePushing, SyncStageFinalizing, SyncStageFailed:
	default:
		return fmt.Errorf("stage %q is unsupported", state.Stage)
	}
	selected, err := validateTransactionalNames("selected", state.Selected)
	if err != nil {
		return err
	}
	if err := validateTransactionalPolicy(ModeExternal, state.Route, state.Policy(), state.Selected); err != nil {
		return err
	}
	if err := validateTransactionalValidation(state.TestCommand, state.ValidationSource); err != nil {
		return err
	}
	if err := validateTransactionalLimits(
		state.StateVersion,
		SyncRunStateTransactionalGuardedVersion,
		state.PlanGuarded,
		state.MaxReplayPerEntry,
		state.MaxReplayTotal,
	); err != nil {
		return err
	}
	pending, err := validateTransactionalSubset("pending", state.Pending, selected)
	if err != nil {
		return err
	}
	completed, err := validateTransactionalSubset("completed", state.Completed, selected)
	if err != nil {
		return err
	}
	for name := range pending {
		if completed[name] {
			return fmt.Errorf("name %q is both pending and completed", name)
		}
	}
	if _, err := validateTransactionalSubset("pushed", state.Pushed, selected); err != nil {
		return err
	}
	if state.FailedBranch != "" && !selected[state.FailedBranch] {
		return fmt.Errorf("failed_branch %q is not selected", state.FailedBranch)
	}
	if err := validateTransactionalRepos(state.Repos); err != nil {
		return err
	}
	if err := ValidateSyncTransaction(state.Transaction, state.Feature, ModeExternal); err != nil {
		return err
	}
	if err := validateTransactionalMetadataPath(featurePath, state.Transaction); err != nil {
		return err
	}
	if !syncNamesEqual(state.Selected, state.Transaction.Selected) {
		return errors.New("outer selection does not match transaction selection")
	}
	return nil
}

func validCheckoutStage(stage CheckoutStage) bool {
	switch stage {
	case StagePlanned, StageSwitched, StageRebasing, StageConflict, StageRebased,
		StageValidating, StageCompleted, StageRestoring, StagePublishing:
		return true
	default:
		return false
	}
}

func validCheckoutFailure(kind FailureKind) bool {
	switch kind {
	case FailNone, FailConflict, FailValidation, FailInterruption, FailSwitch,
		FailPersistence, FailAncestry, FailRestoration:
		return true
	default:
		return false
	}
}

func validLowerHexOID(value string, width int) bool {
	if len(value) != width {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func validateCheckoutTransactionEnvelope(featurePath string, state *CheckoutTransaction) error {
	if state == nil {
		return errors.New("checkout transaction is nil")
	}
	if state.StateVersion != CheckoutTransactionTransactionalVersion &&
		state.StateVersion != CheckoutTransactionTransactionalGuardedVersion {
		return fmt.Errorf("unsupported checkout sync transaction state version %d", state.StateVersion)
	}
	if err := validateTransactionalFeaturePath(featurePath, state.Feature); err != nil {
		return err
	}
	if err := validateTransactionalTimestamp("started_at", state.StartedAt); err != nil {
		return err
	}
	if err := validateTransactionalTimestamp("lock_created", state.LockCreated); err != nil {
		return err
	}
	if state.LockPID <= 0 {
		return errors.New("lock_pid must be positive")
	}
	if state.OriginalBranch == "" {
		return errors.New("original_branch is absent")
	}
	if !validCheckoutStage(state.Stage) {
		return fmt.Errorf("stage %q is unsupported", state.Stage)
	}
	if !validCheckoutFailure(state.FailureKind) {
		return fmt.Errorf("failure_kind %q is unsupported", state.FailureKind)
	}
	selected, err := validateTransactionalNames("selected", state.Selected)
	if err != nil {
		return err
	}
	policy := SyncRunPolicy{
		Fetch:       SyncFetchPolicy(state.FetchPolicy),
		Propagation: SyncPropagationPolicy(state.PropagationPolicy),
		ScopeKind:   SyncScopeKind(state.ScopeKind),
		Selector:    state.ScopeSelector,
	}
	if err := validateTransactionalPolicy(ModeCheckout, state.Route, policy, state.Selected); err != nil {
		return err
	}
	if err := validateTransactionalValidation(state.TestCommand, state.ValidationSource); err != nil {
		return err
	}
	if err := validateTransactionalLimits(
		state.StateVersion,
		CheckoutTransactionTransactionalGuardedVersion,
		state.PlanGuarded,
		state.MaxReplayPerEntry,
		state.MaxReplayTotal,
	); err != nil {
		return err
	}
	if err := ValidateSyncTransaction(state.Transaction, state.Feature, ModeCheckout); err != nil {
		return err
	}
	if err := validateTransactionalMetadataPath(featurePath, state.Transaction); err != nil {
		return err
	}
	if !syncNamesEqual(state.Selected, state.Transaction.Selected) {
		return errors.New("outer selection does not match transaction selection")
	}
	if len(state.Transaction.Repositories) != 1 {
		return fmt.Errorf("checkout transaction has %d repositories, want 1", len(state.Transaction.Repositories))
	}
	repo := state.Transaction.Repositories[0]
	if state.Transaction.WorkspaceRoot != repo.Root {
		return errors.New("checkout workspace root does not match its transaction repository")
	}
	if !validLowerHexOID(state.OriginalHEAD, repo.OIDWidth) {
		return errors.New("original_head is invalid")
	}
	if len(state.Plan) == 0 {
		return errors.New("checkout transaction plan is empty")
	}
	if state.CurrentIndex < 0 || state.CurrentIndex > len(state.Plan) {
		return fmt.Errorf("current_index %d is outside the plan", state.CurrentIndex)
	}
	switch state.Stage {
	case StageSwitched, StageRebasing, StageConflict, StageRebased, StageValidating:
		if state.CurrentIndex >= len(state.Plan) {
			return fmt.Errorf("stage %q requires a current plan entry", state.Stage)
		}
	case StageRestoring, StagePublishing, StageCompleted:
		if state.CurrentIndex != len(state.Plan) {
			return fmt.Errorf("stage %q requires a completed plan", state.Stage)
		}
	}
	if len(state.CompletedIndices) != state.CurrentIndex {
		return errors.New("completed_indices do not match current_index")
	}
	for index, completed := range state.CompletedIndices {
		if completed != index {
			return errors.New("completed_indices are not the completed plan prefix")
		}
	}
	planNames := make(map[string]bool, len(state.Plan))
	for index, entry := range state.Plan {
		if entry.Name == "" || entry.Branch == "" || entry.Base == "" {
			return fmt.Errorf("plan entry %d has an incomplete identity", index)
		}
		if planNames[entry.Name] {
			return fmt.Errorf("plan contains duplicate logical name %q", entry.Name)
		}
		planNames[entry.Name] = true
		if !selected[entry.Name] {
			return fmt.Errorf("plan entry %q is not selected", entry.Name)
		}
		if !validLowerHexOID(entry.PreSHA, repo.OIDWidth) ||
			!validLowerHexOID(entry.NewBaseSHA, repo.OIDWidth) {
			return fmt.Errorf("plan entry %q has invalid required object ids", entry.Name)
		}
		if entry.LastBaseSHA != "" && !validLowerHexOID(entry.LastBaseSHA, repo.OIDWidth) {
			return fmt.Errorf("plan entry %q has invalid last_base_sha", entry.Name)
		}
		if entry.PostSHA != "" && !validLowerHexOID(entry.PostSHA, repo.OIDWidth) {
			return fmt.Errorf("plan entry %q has invalid post_sha", entry.Name)
		}
		postRequired := index < state.CurrentIndex ||
			(index == state.CurrentIndex && (state.Stage == StageRebased || state.Stage == StageValidating))
		if postRequired && entry.PostSHA == "" {
			return fmt.Errorf("plan entry %q is complete without post_sha", entry.Name)
		}
	}
	return nil
}
