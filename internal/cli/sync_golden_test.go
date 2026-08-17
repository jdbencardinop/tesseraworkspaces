package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jdbencardinop/tesseraworkspaces/internal"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// sync-modes AC 1 / AC 2 — pre-change evidence for the frozen no-flag contract.
//
// This harness is written and captured against the UNMODIFIED production tree,
// before internal/cli/sync.go, internal/cli/sync_helpers.go, internal/syncstate.go,
// internal/checkout_sync.go, internal/cli/checkout_sync.go, internal/cli/push.go,
// internal/agent_status.go, and internal/cli/importcmd.go are touched.
//
// internal/cli/testdata/sync_noflag/** is therefore pre-change evidence: a
// regeneration that alters a committed output golden, or any pinned state
// field, ordering, or mode, IS the regression this criterion exists to catch.
// It must never be re-baselined after a production edit.
//
// Comparison rules (spec §4.1 rule 1, §17.1):
//   1. output goldens  — tws-owned bytes, after exactly one closed path→token
//      substitution table, compared byte for byte;
//   2. state files     — semantic yaml.Node comparison over a closed dynamic set;
//   3. argv sidecar    — ordered (verb, argv, exit-class) records under exactly
//      the three closed C4 carve-outs.
// ---------------------------------------------------------------------------

// syncGoldenRegenEnv gates regeneration of the committed pre-change goldens.
const syncGoldenRegenEnv = "TWS_REGEN_SYNC_GOLDENS"

// ---------------------------------------------------------------------------
// The test-only `git` PATH wrapper (§17.1 rule 1b)
// ---------------------------------------------------------------------------

// syncWrapperScript is a POSIX sh script with no bashisms, no GNU-only
// utilities, and no sed -i. It resolves the real git through an absolute path
// passed in the environment, records one argv/exit record per invocation, and
// selects divert or record-only mode from one environment variable.
//
// The three read-only shapes `rev-parse --show-toplevel`,
// `rev-parse --abbrev-ref origin/HEAD`, and `symbolic-ref --short HEAD` are
// TEED in BOTH modes: real git runs with stdout captured to a temporary file,
// the captured bytes are replayed verbatim to the wrapper's own stdout, stderr
// stays inherited, the real exit status is preserved, and the same bytes are
// recorded beside the argv record. That is a tee, never a diversion, and the
// wrapper MUST NOT exec them (§17.1, AC 51).
const syncWrapperScript = `#!/bin/sh
real="$TWS_GIT_REAL"
log="$TWS_GIT_LOG"
tmpd="$TWS_GIT_TMP"
divert="$TWS_GIT_DIVERT"
cwd=` + "`pwd`" + `

argvline=''
for a in "$@"; do
	argvline="$argvline	$a"
done

# Skip the global option forms tws actually emits and take the remainder.
tail=''
started=0
expectval=0
for a in "$@"; do
	if [ "$started" -eq 0 ]; then
		if [ "$expectval" -eq 1 ]; then
			expectval=0
			continue
		fi
		case "$a" in
			-C|-c)
				expectval=1
				continue
				;;
			--git-dir=*|--work-tree=*|--no-pager)
				continue
				;;
			-*)
				continue
				;;
			*)
				started=1
				;;
		esac
	fi
	if [ -z "$tail" ]; then
		tail="$a"
	else
		tail="$tail $a"
	fi
done
verb=${tail%% *}

outline='-'
case "$tail" in
'rev-parse --show-toplevel'|'rev-parse --abbrev-ref origin/HEAD'|'symbolic-ref --short HEAD')
	tmpf="$tmpd/tee.$$"
	"$real" "$@" > "$tmpf"
	status=$?
	cat "$tmpf"
	outline=` + "`tr '\\n' ' ' < \"$tmpf\"`" + `
	rm -f "$tmpf"
	;;
*)
	if [ "$divert" = "1" ]; then
		case "$verb" in
		rebase|fetch|push)
			"$real" "$@" >> "$TWS_GIT_SIDECAR_OUT" 2>> "$TWS_GIT_SIDECAR_ERR"
			status=$?
			;;
		*)
			"$real" "$@"
			status=$?
			;;
		esac
	else
		"$real" "$@"
		status=$?
	fi
	;;
esac

printf 'rec\t%s\t%s\t%s\n' "$status" "$cwd" "$outline" >> "$log"
printf 'argv%s\n' "$argvline" >> "$log"
exit $status
`

// gitRecord is one wrapper sidecar record.
type gitRecord struct {
	Verb string
	Argv []string
	Cwd  string
	Exit int
	Out  string
}

// Tail returns the argv after the global option forms the wrapper skips.
func (r gitRecord) Tail() []string {
	var tail []string
	started := false
	expectVal := false
	for _, a := range r.Argv {
		if !started {
			if expectVal {
				expectVal = false
				continue
			}
			switch {
			case a == "-C" || a == "-c":
				expectVal = true
				continue
			case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") || a == "--no-pager":
				continue
			case strings.HasPrefix(a, "-"):
				continue
			default:
				started = true
			}
		}
		tail = append(tail, a)
	}
	return tail
}

// ExitClass is the zero / non-zero class the comparator compares.
func (r gitRecord) ExitClass() string {
	if r.Exit == 0 {
		return "zero"
	}
	return "nonzero"
}

type syncGitWrapper struct {
	dir        string
	logPath    string
	sidecarOut string
	sidecarErr string
	divert     bool
	realGit    string
}

// newSyncGitWrapper resolves the real git BEFORE the wrapper directory is
// prepended to PATH, so the script never re-resolves git from PATH.
func newSyncGitWrapper(t *testing.T, divert bool) *syncGitWrapper {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("real git must be resolvable before the wrapper is installed: %v", err)
	}
	abs, err := filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w := &syncGitWrapper{
		dir:        dir,
		logPath:    filepath.Join(dir, "argv.log"),
		sidecarOut: filepath.Join(dir, "git-stdout.txt"),
		sidecarErr: filepath.Join(dir, "git-stderr.txt"),
		divert:     divert,
		realGit:    abs,
	}
	script := filepath.Join(dir, "git")
	if err := os.WriteFile(script, []byte(syncWrapperScript), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{w.logPath, w.sidecarOut, w.sidecarErr} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// around installs the wrapper immediately before fn and removes it immediately
// after. It is never installed around fixture construction or around snapshots.
func (w *syncGitWrapper) around(t *testing.T, fn func()) {
	t.Helper()
	oldPath := os.Getenv("PATH")
	oldReal := os.Getenv("TWS_GIT_REAL")
	oldLog := os.Getenv("TWS_GIT_LOG")
	oldTmp := os.Getenv("TWS_GIT_TMP")
	oldDivert := os.Getenv("TWS_GIT_DIVERT")
	oldSOut := os.Getenv("TWS_GIT_SIDECAR_OUT")
	oldSErr := os.Getenv("TWS_GIT_SIDECAR_ERR")
	restore := func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("TWS_GIT_REAL", oldReal)
		_ = os.Setenv("TWS_GIT_LOG", oldLog)
		_ = os.Setenv("TWS_GIT_TMP", oldTmp)
		_ = os.Setenv("TWS_GIT_DIVERT", oldDivert)
		_ = os.Setenv("TWS_GIT_SIDECAR_OUT", oldSOut)
		_ = os.Setenv("TWS_GIT_SIDECAR_ERR", oldSErr)
	}
	defer restore()

	divert := "0"
	if w.divert {
		divert = "1"
	}
	for k, v := range map[string]string{
		"PATH":                w.dir + string(os.PathListSeparator) + oldPath,
		"TWS_GIT_REAL":        w.realGit,
		"TWS_GIT_LOG":         w.logPath,
		"TWS_GIT_TMP":         w.dir,
		"TWS_GIT_DIVERT":      divert,
		"TWS_GIT_SIDECAR_OUT": w.sidecarOut,
		"TWS_GIT_SIDECAR_ERR": w.sidecarErr,
	} {
		if err := os.Setenv(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fn()
}

// records parses the sidecar argv log into ordered records.
func (w *syncGitWrapper) records(t *testing.T) []gitRecord {
	t.Helper()
	data, err := os.ReadFile(w.logPath)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	var out []gitRecord
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !strings.HasPrefix(line, "rec\t") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			t.Fatalf("malformed argv-log record %q", line)
		}
		exit, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("malformed exit status in %q: %v", line, err)
		}
		rec := gitRecord{
			Exit: exit,
			Cwd:  fields[2],
			Out:  strings.TrimSpace(strings.Join(fields[3:], "\t")),
		}
		if rec.Out == "-" {
			rec.Out = ""
		}
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "argv") {
			t.Fatalf("argv-log record %q has no argv line", line)
		}
		argvFields := strings.Split(strings.TrimPrefix(lines[i+1], "argv"), "\t")
		for _, a := range argvFields {
			if a == "" {
				continue
			}
			rec.Argv = append(rec.Argv, a)
		}
		i++
		tail := rec.Tail()
		if len(tail) > 0 {
			rec.Verb = tail[0]
		}
		out = append(out, rec)
	}
	return out
}

// ---------------------------------------------------------------------------
// Resolution-event grouping (§3.11 anchored rule, §17.1 c4ResolutionCompression)
// ---------------------------------------------------------------------------

// Closed carve-out constants (§17.1 comparison mode 3). Declared as literal
// constants, never computed and never widened by a helper.
const (
	c4ContainmentProbe       = "rev-parse --show-toplevel"
	c4DefaultBranchProbeHead = "rev-parse --abbrev-ref origin/HEAD"
	c4DefaultBranchProbeSym  = "symbolic-ref --short HEAD"
	c4ResolutionShowToplevel = "rev-parse --show-toplevel"
	c4ResolutionCommonDir    = "rev-parse --git-common-dir"
)

// syncTeedShapes are the exactly three closed argv shapes whose stdout the
// wrapper tees in BOTH modes.
var syncTeedShapes = []string{
	c4ContainmentProbe,
	c4DefaultBranchProbeHead,
	c4DefaultBranchProbeSym,
}

type resolutionEventKind string

const (
	eventRequireWorkspace resolutionEventKind = "RequireWorkspace"
	eventTwsRoot          resolutionEventKind = "TwsRoot"
)

type workspaceRootResolutionEvent struct {
	Kind  resolutionEventKind
	First int // index of the first record of the event, in log order
	Cwd   string
}

// groupResolutionEvents implements the normative anchored algorithm of §3.11.
//
// It anchors ONLY on `--git-common-dir` records whose -C operand equals that
// record's own recorded process cwd, never on a bare `rev-parse
// --show-toplevel` and never on a foreign-operand `--git-common-dir`, and it
// visits the anchors in reverse log order. Forward pairing (common → show) is a
// TwsRoot event; otherwise it pairs backward (show → common) as a
// RequireWorkspace event. Neither holding fails the capture rather than
// guessing.
//
// It returns the events in log order and the indices of every ungrouped record
// (standalone LoadConfig probes and inferExternalRepoRoot probes), which the
// comparator compares verbatim and in position.
func groupResolutionEvents(t *testing.T, label string, recs []normRecord) ([]workspaceRootResolutionEvent, []int) {
	t.Helper()
	consumed := make([]bool, len(recs))
	kinds := make(map[int]resolutionEventKind)

	isBareShow := func(i int) bool {
		return recs[i].TailString() == c4ResolutionShowToplevel && recs[i].DashC() == ""
	}
	isAnchor := func(i int) bool {
		if recs[i].TailString() != c4ResolutionCommonDir {
			return false
		}
		dashC := recs[i].DashC()
		if dashC == "" {
			return false
		}
		return filepath.Clean(dashC) == filepath.Clean(recs[i].Cwd)
	}
	sameCwd := func(a, b int) bool {
		return filepath.Clean(recs[a].Cwd) == filepath.Clean(recs[b].Cwd)
	}

	var anchors []int
	for i := range recs {
		if isAnchor(i) {
			anchors = append(anchors, i)
		}
	}
	for idx := len(anchors) - 1; idx >= 0; idx-- {
		a := anchors[idx]
		if a+1 < len(recs) && !consumed[a+1] && isBareShow(a+1) && sameCwd(a, a+1) {
			consumed[a] = true
			consumed[a+1] = true
			kinds[a] = eventTwsRoot
			continue
		}
		if a-1 >= 0 && !consumed[a-1] && isBareShow(a-1) && sameCwd(a, a-1) {
			consumed[a] = true
			consumed[a-1] = true
			kinds[a-1] = eventRequireWorkspace
			continue
		}
		t.Fatalf("%s: workspace-root resolution grouping failed: anchor %d (%s) has no unconsumed bare show partner", label, a, recs[a])
	}

	var events []workspaceRootResolutionEvent
	var ungrouped []int
	for i := range recs {
		if kind, ok := kinds[i]; ok {
			events = append(events, workspaceRootResolutionEvent{Kind: kind, First: i, Cwd: recs[i].Cwd})
			continue
		}
		if consumed[i] {
			continue
		}
		ungrouped = append(ungrouped, i)
	}
	return events, ungrouped
}

// ---------------------------------------------------------------------------
// Semantic state comparison (§17.1 comparison mode 2)
// ---------------------------------------------------------------------------

type dynamicShape string

const (
	shapeRFC3339UTC  dynamicShape = "rfc3339UTC"
	shapePositiveInt dynamicShape = "positiveInt"
	shapeHex32       dynamicShape = "hex32"
	shapeMarker      dynamicShape = "markerPattern"
	shapeConflictMsg dynamicShape = "conflictFailureMsg"
)

// The closed dynamic sets of §4.1 rule 2, declared as literal constants.
var (
	syncStateDynamicKeys = map[string]dynamicShape{
		"started_at": shapeRFC3339UTC,
	}
	checkoutTxDynamicKeys = map[string]dynamicShape{
		"started_at":   shapeRFC3339UTC,
		"lock_pid":     shapePositiveInt,
		"lock_created": shapeRFC3339UTC,
	}
	syncRunStateDynamicKeys = map[string]dynamicShape{
		"started_at":  shapeRFC3339UTC,
		"updated_at":  shapeRFC3339UTC,
		"marker":      shapeMarker,
		"owner_token": shapeHex32,
	}
	syncRunGuardDynamicKeys = map[string]dynamicShape{
		"pid":     shapePositiveInt,
		"created": shapeRFC3339UTC,
		"token":   shapeHex32,
	}
)

// The single conditional rule, declared as a literal constant beside the
// closed dynamic sets (§4.1 rule 2).
const conflictFailureMsgPrefix = "rebase conflict: "

const conflictOutputToken = "<GIT-CONFLICT-OUTPUT>"

var (
	rfc3339UTCPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
	hex32Pattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	syncMarkerPattern = regexp.MustCompile(`^tws-scoped-sync-[0-9a-f]{32}\.lock$`)
)

type stateCompareSpec struct {
	// AdditiveKeys are removed from the NEW document before comparison. The
	// only entry this feature declares is the checkout plan's `name` key (C5).
	AdditiveKeys []string
	DynamicKeys  map[string]dynamicShape
	// ConflictFailureMsg enables the single conditional rule, and only for a
	// checkout transaction whose failure_kind is `conflict`.
	ConflictFailureMsg bool
}

// compareStateSemantic implements §17.1 comparison mode 2. The reference is
// carried as bytes plus its recorded mode, because Git preserves only the
// executable bit and a committed 0600 reference would come back 0644.
func compareStateSemantic(t *testing.T, label string, wantBytes, gotBytes []byte, wantMode, gotMode os.FileMode, spec stateCompareSpec) {
	t.Helper()
	if wantMode.Perm() != gotMode.Perm() {
		t.Fatalf("%s: file mode %v != reference %v", label, gotMode.Perm(), wantMode.Perm())
	}

	want := decodeYAMLDoc(t, label+" (reference)", wantBytes)
	got := decodeYAMLDoc(t, label, gotBytes)

	for _, key := range spec.AdditiveKeys {
		removeAdditiveKey(t, label, got, key)
	}
	if spec.ConflictFailureMsg {
		normalizeConflictFailureMsg(t, label, want)
		normalizeConflictFailureMsg(t, label, got)
	}
	compareYAMLValue(t, label, "", want, got, spec.DynamicKeys)
}

func decodeYAMLDoc(t *testing.T, label string, data []byte) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: not valid YAML: %v", label, err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	return &doc
}

// removeAdditiveKey removes exactly one declared additive key. The only
// supported form is "plan[].name" (C5).
func removeAdditiveKey(t *testing.T, label string, doc *yaml.Node, key string) {
	t.Helper()
	if key != "plan[].name" {
		t.Fatalf("%s: unsupported additive key %q", label, key)
	}
	plan := mappingValue(doc, "plan")
	if plan == nil {
		return
	}
	for _, entry := range plan.Content {
		deleteMappingKey(entry, "name")
	}
}

func normalizeConflictFailureMsg(t *testing.T, label string, doc *yaml.Node) {
	t.Helper()
	kind := mappingValue(doc, "failure_kind")
	if kind == nil || kind.Value != "conflict" {
		return
	}
	stage := mappingValue(doc, "stage")
	if stage == nil || stage.Value != "conflict" {
		t.Fatalf("%s: a conflict transaction must pin stage=conflict, got %v", label, stage)
	}
	msg := mappingValue(doc, "failure_msg")
	if msg == nil {
		t.Fatalf("%s: a conflict transaction must carry failure_msg", label)
	}
	if !strings.HasPrefix(msg.Value, conflictFailureMsgPrefix) {
		t.Fatalf("%s: failure_msg must start with %q, got %q", label, conflictFailureMsgPrefix, msg.Value)
	}
	suffix := strings.TrimPrefix(msg.Value, conflictFailureMsgPrefix)
	if strings.TrimSpace(suffix) == "" {
		t.Fatalf("%s: the conflict bytes were lost — failure_msg suffix is empty", label)
	}
	msg.Value = conflictFailureMsgPrefix + conflictOutputToken
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func deleteMappingKey(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func compareYAMLValue(t *testing.T, label, path string, want, got *yaml.Node, dynamic map[string]dynamicShape) {
	t.Helper()
	if want.Kind != got.Kind {
		t.Fatalf("%s: %s kind %v != reference %v", label, path, got.Kind, want.Kind)
	}
	switch want.Kind {
	case yaml.MappingNode:
		if len(want.Content) != len(got.Content) {
			t.Fatalf("%s: %s key set differs: %v vs reference %v", label, path, mappingKeys(got), mappingKeys(want))
		}
		for i := 0; i+1 < len(want.Content); i += 2 {
			wk, gk := want.Content[i], got.Content[i]
			if wk.Value != gk.Value {
				t.Fatalf("%s: %s key order differs at %d: %q vs reference %q", label, path, i/2, gk.Value, wk.Value)
			}
			child := path + "/" + wk.Value
			if shape, ok := dynamic[wk.Value]; ok {
				assertDynamicShape(t, label, child, shape, got.Content[i+1].Value)
				continue
			}
			compareYAMLValue(t, label, child, want.Content[i+1], got.Content[i+1], dynamic)
		}
	case yaml.SequenceNode:
		if len(want.Content) != len(got.Content) {
			t.Fatalf("%s: %s sequence length %d != reference %d", label, path, len(got.Content), len(want.Content))
		}
		for i := range want.Content {
			compareYAMLValue(t, label, fmt.Sprintf("%s[%d]", path, i), want.Content[i], got.Content[i], dynamic)
		}
	default:
		if want.Value != got.Value {
			t.Fatalf("%s: %s = %q, reference %q", label, path, got.Value, want.Value)
		}
	}
}

func mappingKeys(node *yaml.Node) []string {
	var keys []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

func assertDynamicShape(t *testing.T, label, path string, shape dynamicShape, value string) {
	t.Helper()
	switch shape {
	case shapeRFC3339UTC:
		if !rfc3339UTCPattern.MatchString(value) {
			t.Fatalf("%s: %s = %q is not an RFC3339 UTC timestamp", label, path, value)
		}
	case shapePositiveInt:
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			t.Fatalf("%s: %s = %q is not a positive integer", label, path, value)
		}
	case shapeHex32:
		if !hex32Pattern.MatchString(value) {
			t.Fatalf("%s: %s = %q is not 32 lower-case hex characters", label, path, value)
		}
	case shapeMarker:
		if !syncMarkerPattern.MatchString(value) {
			t.Fatalf("%s: %s = %q does not match the marker grammar", label, path, value)
		}
	default:
		t.Fatalf("%s: %s has unknown dynamic shape %q", label, path, shape)
	}
}

// ---------------------------------------------------------------------------
// Capture plumbing
// ---------------------------------------------------------------------------

// syncCaptureStreams swaps os.Stdout and os.Stderr for pipes around fn and
// returns the two process streams separately. Sync output comes from bare
// fmt.Print* calls and from Git subprocesses wired to the process streams, so a
// Cobra output buffer captures neither.
func syncCaptureStreams(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { outCh <- readAllString(outR) }()
	go func() { errCh <- readAllString(errR) }()

	func() {
		defer func() {
			os.Stdout, os.Stderr = oldOut, oldErr
			_ = outW.Close()
			_ = errW.Close()
		}()
		fn()
	}()

	return <-outCh, <-errCh
}

func readAllString(r *os.File) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	_ = r.Close()
	return sb.String()
}

// ---------------------------------------------------------------------------
// Golden paths (sync-local; internal/cli/stack_status_test.go is NOT edited)
// ---------------------------------------------------------------------------

func syncGoldenPath(fixture, surface string) string {
	return filepath.Join(goldenPkgDir, "testdata", "sync_noflag", fixture, surface)
}

// syncStream stores or compares a captured process stream, with the shipped
// `exit: N\n<body>` artifact shape.
func syncStream(t *testing.T, fixture, surface, body string, exit int) {
	t.Helper()
	syncCompareOrWrite(t, fixture, surface, fmt.Sprintf("exit: %d\n%s", exit, body))
}

// syncGolden stores or compares a raw byte-compared golden (ref and stack
// snapshots), which carry no exit code.
func syncGolden(t *testing.T, fixture, surface, body string) {
	t.Helper()
	syncCompareOrWrite(t, fixture, surface, body)
}

func syncCompareOrWrite(t *testing.T, fixture, surface, artifact string) {
	t.Helper()
	path := syncGoldenPath(fixture, surface)
	if os.Getenv(syncGoldenRegenEnv) == "1" {
		goldenWrite(t, path, artifact)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing pinned golden %s: %v (regenerate with %s=1 only against the pre-change tree)", path, err, syncGoldenRegenEnv)
	}
	if string(want) != artifact {
		t.Fatalf("golden %s changed.\n--- want ---\n%s\n--- got ---\n%s", path, want, artifact)
	}
}

// syncBaseline stores a pre-change baseline that is compared semantically
// rather than byte for byte (the argv log under comparison mode 3, and state
// references under comparison mode 2). In regen mode it writes; otherwise it
// returns the committed pre-change content.
func syncBaseline(t *testing.T, fixture, surface, body string) string {
	t.Helper()
	path := syncGoldenPath(fixture, surface)
	if os.Getenv(syncGoldenRegenEnv) == "1" {
		goldenWrite(t, path, body)
		return body
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing pre-change baseline %s: %v (regenerate with %s=1 only against the pre-change tree)", path, err, syncGoldenRegenEnv)
	}
	return string(data)
}

// syncWriteEvidence stores declared-change evidence. Evidence directories are
// explicitly NOT goldens: they are the pre-change side of a reviewable diff.
func syncWriteEvidence(t *testing.T, fixture, surface, body string) {
	t.Helper()
	if os.Getenv(syncGoldenRegenEnv) != "1" {
		return
	}
	goldenWrite(t, syncGoldenPath(fixture, surface), body)
}

func syncReadEvidence(t *testing.T, fixture, surface string) string {
	t.Helper()
	data, err := os.ReadFile(syncGoldenPath(fixture, surface))
	if err != nil {
		t.Fatalf("missing declared-change evidence %s: %v", syncGoldenPath(fixture, surface), err)
	}
	return string(data)
}

// normRecord is one path-normalized argv record: the unit both the committed
// baseline and a post-change capture are compared as.
type normRecord struct {
	ExitClass string
	Cwd       string
	Argv      []string
	Out       string
}

func (r normRecord) Tail() []string {
	var tail []string
	started := false
	expectVal := false
	for _, a := range r.Argv {
		if !started {
			if expectVal {
				expectVal = false
				continue
			}
			switch {
			case a == "-C" || a == "-c":
				expectVal = true
				continue
			case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") || a == "--no-pager":
				continue
			case strings.HasPrefix(a, "-"):
				continue
			default:
				started = true
			}
		}
		tail = append(tail, a)
	}
	return tail
}

func (r normRecord) TailString() string { return strings.Join(r.Tail(), " ") }

func (r normRecord) DashC() string {
	for i := 0; i < len(r.Argv)-1; i++ {
		if r.Argv[i] == "-C" {
			return r.Argv[i+1]
		}
	}
	return ""
}

func (r normRecord) String() string {
	return fmt.Sprintf("%s [cwd=%s exit=%s]", strings.Join(r.Argv, " "), r.Cwd, r.ExitClass)
}

// Key is the verbatim comparison key: argv, cwd, and exit-status class.
func (r normRecord) Key() string {
	return strings.Join(r.Argv, " ") + "\t" + r.Cwd + "\t" + r.ExitClass
}

func normalizeRecords(recs []gitRecord, reps []goldenReplacement, stableID string) []normRecord {
	out := make([]normRecord, 0, len(recs))
	for _, r := range recs {
		n := normRecord{ExitClass: r.ExitClass(), Cwd: goldenApplyReplacements(reps, stableID, r.Cwd), Out: goldenApplyReplacements(reps, stableID, r.Out)}
		for _, a := range r.Argv {
			n.Argv = append(n.Argv, goldenApplyReplacements(reps, stableID, a))
		}
		out = append(out, n)
	}
	return out
}

// syncRenderRecords renders a normalized argv log for storage and comparison.
func syncRenderRecords(recs []normRecord) string {
	var sb strings.Builder
	for i, r := range recs {
		fmt.Fprintf(&sb, "%03d\t%s\t%s\t%s\t%s\n", i, r.ExitClass, r.Cwd, strings.Join(r.Argv, " "), r.Out)
	}
	return sb.String()
}

// parseRenderedRecords reads a committed argv-log baseline back into records.
func parseRenderedRecords(t *testing.T, label, body string) []normRecord {
	t.Helper()
	var out []normRecord
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("%s: malformed baseline argv record %q", label, line)
		}
		r := normRecord{ExitClass: fields[1], Cwd: fields[2], Out: fields[4]}
		if fields[3] != "" {
			r.Argv = strings.Split(fields[3], " ")
		}
		out = append(out, r)
	}
	return out
}

// ---------------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------------

// syncSnapshotRefs renders every ref of a repository, sorted.
func syncSnapshotRefs(t *testing.T, repoDir string) string {
	t.Helper()
	out := gitOutput(t, repoDir, "for-each-ref", "--format=%(refname) %(objectname)")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// syncSnapshotFiles lists the feature directory, ignoring the transient
// .tws-state-* temporaries atomicWriteFile creates (§17.3). .sync-run.lock is
// deliberately NOT filtered.
func syncSnapshotFiles(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".tws-state-") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		lines = append(lines, fmt.Sprintf("%s %v", rel, info.Mode().Perm()))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Fixtures (§17.2)
// ---------------------------------------------------------------------------

const syncGoldenFeature = "auth"

// syncPinnedGitEnv pins Git identity and dates at the PROCESS level, so the
// Git commands the production code issues inherit them and every commit a
// measured run creates has a byte-stable object ID across runs and machines.
func syncPinnedGitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "tws test")
	t.Setenv("GIT_AUTHOR_EMAIL", "tws@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "tws test")
	t.Setenv("GIT_COMMITTER_EMAIL", "tws@example.test")
	t.Setenv("GIT_AUTHOR_DATE", goldenFixedDate)
	t.Setenv("GIT_COMMITTER_DATE", goldenFixedDate)
}

// syncGoldenEnv installs the pinned fixture environment and returns the
// resolved workspace. It uses the shipped withUnifiedWorkspaceEnv, which
// asserts internal.TwsRoot() and ws.MetadataRoot AGREE — every frozen capture
// is taken from an agreeing layout (§3.11, AC 1).
func syncGoldenEnv(t *testing.T, repo string) internal.Workspace {
	t.Helper()
	syncPinnedGitEnv(t)
	_ = withUnifiedWorkspaceEnv(t, repo)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatalf("fixture workspace must resolve: %v", err)
	}
	if ws.StableID == "" {
		t.Fatalf("fixture workspace has no stable ID: %+v", ws)
	}
	return ws
}

func syncReplacements(t *testing.T, fx *goldenFixture, ws internal.Workspace) []goldenReplacement {
	t.Helper()
	fx.addExtra(fx.featurePath, "<FEATURE>")
	return goldenReplacements(t, fx, ws)
}

// syncRepoBase creates a repository with a real local bare remote on `main`.
func syncRepoBase(b *goldenBuilder) (root, repo, remote string) {
	t := b.t
	root = t.TempDir()
	repo = filepath.Join(root, "repo")
	remote = filepath.Join(root, "remote.git")
	b.git(root, "init", "--bare", "--initial-branch=main", remote)
	b.git(root, "init", "--initial-branch=main", repo)
	goldenWrite(t, filepath.Join(repo, "README.md"), "base\n")
	b.git(repo, "add", "README.md")
	b.git(repo, "commit", "-m", "initial")
	b.git(repo, "remote", "add", "origin", remote)
	b.git(repo, "push", "-u", "origin", "main")
	b.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	b.git(repo, "remote", "set-head", "origin", "-a")
	return root, repo, remote
}

// syncExternalLinear builds the `linear` external fixture: root -> parent ->
// child in one repository, three linked worktrees, one bare remote.
// When conflict is true, parent and child touch the same file on divergent
// histories so child's rebase onto parent conflicts for real.
func syncExternalLinear(b *goldenBuilder, feature string, conflict bool) *goldenFixture {
	t := b.t
	root, repo, remote := syncRepoBase(b)

	metaRoot := repo + ".tws"
	featurePath := filepath.Join(metaRoot, feature)
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	if err := os.MkdirAll(filepath.Join(metaRoot, ".tws-workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	add := func(name, base, file string) string {
		b.git(repo, "branch", name, base)
		path := filepath.Join(worktreesRoot, name)
		b.git(repo, "worktree", "add", path, name)
		goldenWrite(t, filepath.Join(path, file), name+"\n")
		b.git(path, "add", file)
		b.git(path, "commit", "-m", name)
		return path
	}
	add("root", "main", "root.txt")
	parentPath := add("parent", "root", "parent.txt")
	childPath := add("child", "parent", "child.txt")

	if conflict {
		goldenWrite(t, filepath.Join(parentPath, "shared.txt"), "parent\n")
		b.git(parentPath, "add", "shared.txt")
		b.git(parentPath, "commit", "-m", "parent shared")
		goldenWrite(t, filepath.Join(childPath, "shared.txt"), "child\n")
		b.git(childPath, "add", "shared.txt")
		b.git(childPath, "commit", "-m", "child shared")
	}

	fx := &goldenFixture{
		mode:          internal.ModeExternal,
		root:          root,
		repo:          repo,
		remote:        remote,
		metaRoot:      metaRoot,
		featurePath:   featurePath,
		worktreesRoot: worktreesRoot,
		entries: []goldenStackEntry{
			{name: "root", base: "main"},
			{name: "parent", base: "root"},
			{name: "child", base: "parent"},
		},
	}
	fx.addExtra(remote, "<REMOTE>")
	goldenWrite(t, filepath.Join(featurePath, "stack.yaml"), goldenStackYAML(fx.entries))
	return fx
}

// syncCheckoutLinear builds the `checkout` fixture in the NEW
// .tws/features/<feature>/** layout only (§17.2).
func syncCheckoutLinear(b *goldenBuilder, feature string, conflict bool) *goldenFixture {
	t := b.t
	root, repo, remote := syncRepoBase(b)

	// A checkout workspace keeps its metadata inside the repository, so the
	// repository must ignore it or every run would refuse on a dirty tree.
	goldenWrite(t, filepath.Join(repo, ".gitignore"), ".tws/\n")
	b.git(repo, "add", ".gitignore")
	b.git(repo, "commit", "-m", "ignore tws metadata")
	b.git(repo, "push", "origin", "main")

	add := func(name, base, file string) {
		b.git(repo, "checkout", "-b", name, base)
		goldenWrite(t, filepath.Join(repo, file), name+"\n")
		b.git(repo, "add", file)
		b.git(repo, "commit", "-m", name)
	}
	add("root", "main", "root.txt")
	add("parent", "root", "parent.txt")
	add("child", "parent", "child.txt")

	if conflict {
		b.git(repo, "checkout", "parent")
		goldenWrite(t, filepath.Join(repo, "shared.txt"), "parent\n")
		b.git(repo, "add", "shared.txt")
		b.git(repo, "commit", "-m", "parent shared")
		b.git(repo, "checkout", "child")
		goldenWrite(t, filepath.Join(repo, "shared.txt"), "child\n")
		b.git(repo, "add", "shared.txt")
		b.git(repo, "commit", "-m", "child shared")
	}
	b.git(repo, "checkout", "main")

	metaRoot := filepath.Join(repo, ".tws")
	goldenWrite(t, filepath.Join(metaRoot, "config.yaml"), "workspace_mode: checkout\n")
	featurePath := filepath.Join(metaRoot, "features", feature)
	if err := os.MkdirAll(featurePath, 0o755); err != nil {
		t.Fatal(err)
	}

	fx := &goldenFixture{
		mode:        internal.ModeCheckout,
		root:        root,
		repo:        repo,
		remote:      remote,
		metaRoot:    metaRoot,
		featurePath: featurePath,
		entries: []goldenStackEntry{
			{name: "root", base: "main"},
			{name: "parent", base: "root"},
			{name: "child", base: "parent"},
		},
	}
	fx.addExtra(remote, "<REMOTE>")
	goldenWrite(t, filepath.Join(featurePath, "stack.yaml"), goldenStackYAML(fx.entries))
	return fx
}

// ---------------------------------------------------------------------------
// Measured invocation
// ---------------------------------------------------------------------------

// syncExecute runs one tws command exactly as cmd/tws does, minus pflag's usage
// block: usage drift on an argument error is accepted help drift (§3.9) and is
// snapshot-tested separately by AC 3, so pinning it inside every failure golden
// would make the frozen contract hostage to the help text.
func syncExecute(build func() *cobra.Command, args ...string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic: %v\n", r)
			code = 2
		}
	}()
	cmd := build()
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type syncCaptureResult struct {
	stdout  string
	stderr  string
	exit    int
	records []gitRecord
}

// syncMeasure installs the wrapper immediately before the invocation, captures
// both process streams, and removes the wrapper immediately after.
func syncMeasure(t *testing.T, divert bool, build func() *cobra.Command, args ...string) syncCaptureResult {
	t.Helper()
	w := newSyncGitWrapper(t, divert)
	var exit int
	stdout, stderr := syncCaptureStreams(t, func() {
		w.around(t, func() {
			exit = syncExecute(build, args...)
		})
	})
	return syncCaptureResult{stdout: stdout, stderr: stderr, exit: exit, records: w.records(t)}
}

// syncUnmeasured runs a preparation invocation with no wrapper installed and
// with the process streams discarded, so only the measured invocation is
// captured.
func syncUnmeasured(t *testing.T, build func() *cobra.Command, args ...string) int {
	t.Helper()
	var exit int
	_, _ = syncCaptureStreams(t, func() { exit = syncExecute(build, args...) })
	return exit
}

// ---------------------------------------------------------------------------
// Frozen surfaces
// ---------------------------------------------------------------------------

type syncStateRef struct {
	surface string
	path    string
	spec    stateCompareSpec
}

// syncFreeze stores (regen mode) or compares (default) every frozen surface of
// one capture: the two process streams, the argv sidecar baseline, the ref
// snapshot, and stack.yaml.
func syncFreeze(t *testing.T, fixture string, fx *goldenFixture, ws internal.Workspace, res syncCaptureResult, refs []syncStateRef) {
	t.Helper()
	reps := syncReplacements(t, fx, ws)
	norm := func(label, s string) string {
		return goldenNormalizeText(t, fixture+"/"+label, reps, ws.StableID, s)
	}

	syncStream(t, fixture, "stdout.txt", norm("stdout.txt", res.stdout), res.exit)
	syncStream(t, fixture, "stderr.txt", norm("stderr.txt", res.stderr), res.exit)
	syncGolden(t, fixture, "refs.txt", norm("refs.txt", syncSnapshotRefs(t, fx.repo)))
	syncGolden(t, fixture, "remote-refs.txt", norm("remote-refs.txt", syncSnapshotRefs(t, fx.remote)))
	if data, err := os.ReadFile(filepath.Join(fx.featurePath, "stack.yaml")); err == nil {
		syncGolden(t, fixture, "stack.yaml", norm("stack.yaml", string(data)))
	}

	got := normalizeRecords(res.records, reps, ws.StableID)
	baseline := syncBaseline(t, fixture, "argv.log", norm("argv.log", syncRenderRecords(got)))
	if os.Getenv(syncGoldenRegenEnv) != "1" {
		want := parseRenderedRecords(t, fixture+"/argv.log", baseline)
		compareArgvLogs(t, fixture, fx.mode, want, got, "<REPO>")
		// Post-change criteria (§3.11, §10.9). They are deliberately not part
		// of the pre-change baseline: the baseline records what the shipped
		// tree did, these assert what this feature guarantees.
		if fx.mode == internal.ModeExternal {
			assertExternalResolutionBudget(t, fixture, got)
		} else {
			assertContainmentProbePresent(t, fixture, got, "<REPO>")
		}
	}

	for _, ref := range refs {
		data, err := os.ReadFile(ref.path)
		if err != nil {
			t.Fatalf("%s: expected state file %s: %v", fixture, ref.path, err)
		}
		info, err := os.Stat(ref.path)
		if err != nil {
			t.Fatal(err)
		}
		body := norm(ref.surface, string(data))
		want := syncBaseline(t, fixture, ref.surface, body)
		wantMode := syncBaseline(t, fixture, ref.surface+".mode", fmt.Sprintf("%04o\n", info.Mode().Perm()))
		if os.Getenv(syncGoldenRegenEnv) == "1" {
			continue
		}
		var mode os.FileMode
		if _, err := fmt.Sscanf(strings.TrimSpace(wantMode), "%o", &mode); err != nil {
			t.Fatalf("%s: unreadable recorded mode %q: %v", fixture, wantMode, err)
		}
		compareStateSemantic(t, fixture+"/"+ref.surface, []byte(want), []byte(body), mode, info.Mode(), ref.spec)
	}
}

// ---------------------------------------------------------------------------
// Comparison mode 3 — ordered argv comparison under exactly three closed
// C4 carve-outs (§17.1)
// ---------------------------------------------------------------------------

// compareArgvLogs asserts the post-change log equals the committed pre-change
// baseline except for the closed carve-out set {c4ContainmentProbe,
// c4DefaultBranchProbe, c4ResolutionCompression} and nothing else.
func compareArgvLogs(t *testing.T, label string, mode internal.WorkspaceMode, want, got []normRecord, repoToken string) {
	t.Helper()

	wantEvents, wantRest := groupResolutionEvents(t, label+" (pre-change)", want)
	gotEvents, gotRest := groupResolutionEvents(t, label+" (post-change)", got)

	if mode == internal.ModeExternal {
		// c4ResolutionCompression removes whole events and adds none, so the
		// surviving list must be a prefix of the pre-change list by kind. The
		// post-change budget itself (exactly two events) is a separate,
		// post-change criterion — see assertExternalResolutionBudget.
		if len(gotEvents) > len(wantEvents) {
			t.Fatalf("%s: carve-out (c) removes resolution events and adds none; got %v vs pre-change %v", label, describeEvents(gotEvents), describeEvents(wantEvents))
		}
		for i := range gotEvents {
			if gotEvents[i].Kind != wantEvents[i].Kind {
				t.Fatalf("%s: surviving resolution event %d is %s, pre-change %s", label, i, gotEvents[i].Kind, wantEvents[i].Kind)
			}
		}
	} else {
		// The checkout path is outside carve-out (c): no event is removed and
		// none is added.
		if len(gotEvents) != len(wantEvents) {
			t.Fatalf("%s: checkout resolution events changed: %v vs pre-change %v", label, describeEvents(gotEvents), describeEvents(wantEvents))
		}
		for i := range gotEvents {
			if gotEvents[i].Kind != wantEvents[i].Kind {
				t.Fatalf("%s: checkout resolution event %d kind %s != pre-change %s", label, i, gotEvents[i].Kind, wantEvents[i].Kind)
			}
		}
	}

	wantNon := pickRecords(want, wantRest)
	gotNon := pickRecords(got, gotRest)

	// c4DefaultBranchProbe: the whole DefaultBranchIn logical event is compared
	// by RESOLVED VALUE, never by record count or exit-status class.
	wantNon, wantBranch := extractDefaultBranchEvent(t, label+" (pre-change)", wantNon)
	gotNon, gotBranch := extractDefaultBranchEvent(t, label+" (post-change)", gotNon)
	if wantBranch != gotBranch {
		t.Fatalf("%s: default-branch resolution differs: %q vs pre-change %q", label, gotBranch, wantBranch)
	}

	// c4ContainmentProbe: checkout may gain exactly one added record, and it
	// must be the containment probe at its pinned position.
	if mode == internal.ModeCheckout {
		gotNon = extractContainmentProbe(t, label, gotNon, wantNon, repoToken)
	}

	if len(gotNon) != len(wantNon) {
		t.Fatalf("%s: non-event argv records differ in count: %d vs pre-change %d\n--- want ---\n%s\n--- got ---\n%s",
			label, len(gotNon), len(wantNon), describeRecords(wantNon), describeRecords(gotNon))
	}
	for i := range wantNon {
		if wantNon[i].Key() != gotNon[i].Key() {
			t.Fatalf("%s: non-event argv record %d differs:\n  got  %s\n  want %s", label, i, gotNon[i], wantNon[i])
		}
	}
}

func pickRecords(recs []normRecord, idx []int) []normRecord {
	out := make([]normRecord, 0, len(idx))
	for _, i := range idx {
		out = append(out, recs[i])
	}
	return out
}

func describeEvents(events []workspaceRootResolutionEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, string(e.Kind))
	}
	return out
}

func describeRecords(recs []normRecord) string {
	var sb strings.Builder
	for i, r := range recs {
		fmt.Fprintf(&sb, "%03d %s\n", i, r)
	}
	return sb.String()
}

// extractDefaultBranchEvent removes the closed DefaultBranchIn logical event
// from the compared record list and returns its resolved value. The event is
// one successful `rev-parse --abbrev-ref origin/HEAD`; or a failed one followed
// by `symbolic-ref --short HEAD`; or both failing, in which case the resolution
// is the hard-coded `main` with no further record.
func extractDefaultBranchEvent(t *testing.T, label string, recs []normRecord) ([]normRecord, string) {
	t.Helper()
	resolved := ""
	found := false
	out := make([]normRecord, 0, len(recs))
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if r.TailString() != c4DefaultBranchProbeHead {
			out = append(out, r)
			continue
		}
		if found {
			// Later default-branch events resolve the same fixture constant;
			// the first one pins the value and the rest are elided with it.
			if r.ExitClass == "zero" {
				assertSameBranch(t, label, resolved, strings.TrimPrefix(strings.TrimSpace(r.Out), "origin/"))
			} else if i+1 < len(recs) && recs[i+1].TailString() == c4DefaultBranchProbeSym {
				i++
			}
			continue
		}
		found = true
		if r.ExitClass == "zero" {
			resolved = strings.TrimPrefix(strings.TrimSpace(r.Out), "origin/")
			continue
		}
		if i+1 < len(recs) && recs[i+1].TailString() == c4DefaultBranchProbeSym {
			i++
			if recs[i].ExitClass == "zero" {
				resolved = strings.TrimSpace(recs[i].Out)
				continue
			}
		}
		resolved = "main"
	}
	if !found {
		return out, ""
	}
	return out, resolved
}

func assertSameBranch(t *testing.T, label, want, got string) {
	t.Helper()
	if want != got {
		t.Fatalf("%s: two default-branch events resolved differently: %q and %q", label, want, got)
	}
}

// extractContainmentProbe asserts the post-change checkout log carries exactly
// one added `git -C <cwd> rev-parse --show-toplevel` record, exiting zero,
// resolving to ws.RepoRoot, positioned directly before the first
// RunCheckoutSync preflight record (`rev-parse --git-path rebase-merge`) or,
// for a --continue/--abort capture, before that path's own first record.
func extractContainmentProbe(t *testing.T, label string, got, want []normRecord, repoToken string) []normRecord {
	t.Helper()
	added := -1
	gi, wi := 0, 0
	for gi < len(got) {
		if wi < len(want) && got[gi].Key() == want[wi].Key() {
			gi++
			wi++
			continue
		}
		if added >= 0 {
			t.Fatalf("%s: more than one added checkout record; second is %s", label, got[gi])
		}
		added = gi
		gi++
	}
	if added < 0 {
		// No added record at all: the identity case. The probe's mandatory
		// presence is a post-change criterion (assertContainmentProbePresent).
		return got
	}
	probe := got[added]
	if probe.TailString() != c4ContainmentProbe || probe.DashC() == "" {
		t.Fatalf("%s: the added checkout record must be `git -C <cwd> %s`; got %s", label, c4ContainmentProbe, probe)
	}
	if probe.ExitClass != "zero" {
		t.Fatalf("%s: the containment probe must exit zero; got %s", label, probe)
	}
	if strings.TrimSpace(probe.Out) != repoToken {
		t.Fatalf("%s: the containment probe must resolve to ws.RepoRoot (%s); got %q", label, repoToken, probe.Out)
	}
	if added < len(got)-1 {
		next := got[added+1]
		if next.Verb() != "rev-parse" && next.Verb() != "symbolic-ref" && next.Verb() != "status" {
			t.Fatalf("%s: the containment probe must sit directly before the first checkout-sync record; next is %s", label, next)
		}
	}
	return append(append([]normRecord{}, got[:added]...), got[added+1:]...)
}

// Verb is the first argv token after the global option forms.
func (r normRecord) Verb() string {
	tail := r.Tail()
	if len(tail) == 0 {
		return ""
	}
	return tail[0]
}

// ---------------------------------------------------------------------------
// AC 1 / AC 2 — the frozen no-flag captures
// ---------------------------------------------------------------------------

func syncStateRefExternal(fx *goldenFixture) syncStateRef {
	return syncStateRef{
		surface: "state-sync-state.yaml",
		path:    internal.SyncStatePath(fx.featurePath),
		spec:    stateCompareSpec{DynamicKeys: syncStateDynamicKeys},
	}
}

func syncStateRefCheckout(fx *goldenFixture, feature string) syncStateRef {
	return syncStateRef{
		surface: "state-checkout-sync.yaml",
		path:    filepath.Join(fx.metaRoot, "state", feature+"-checkout-sync.yaml"),
		spec: stateCompareSpec{
			AdditiveKeys:       []string{"plan[].name"},
			DynamicKeys:        checkoutTxDynamicKeys,
			ConflictFailureMsg: true,
		},
	}
}

func TestSyncNoFlag_ExternalClean(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, false)
	// At least one frozen external fixture configures a test_command, so the
	// measured `standalone show -> common -> show` adjacency is exercised and a
	// show-anchored comparator fails (§3.11, AC 2).
	goldenWrite(t, filepath.Join(fx.repo, ".tws", "config.yaml"), "test_command: git --version\n")
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, true, syncCmd, syncGoldenFeature)
	syncFreeze(t, "external-clean", fx, ws, res, nil)
}

func TestSyncNoFlag_ExternalConflict(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, true)
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, true, syncCmd, syncGoldenFeature)
	syncFreeze(t, "external-conflict", fx, ws, res, []syncStateRef{syncStateRefExternal(fx)})
}

func TestSyncNoFlag_ExternalContinue(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, true)
	ws := syncGoldenEnv(t, fx.repo)

	if exit := syncUnmeasured(t, syncCmd, syncGoldenFeature); exit == 0 {
		t.Fatal("the conflict fixture must fail the preparation sync")
	}
	childPath := filepath.Join(fx.worktreesRoot, "child")
	goldenWrite(t, filepath.Join(childPath, "shared.txt"), "resolved\n")
	b.git(childPath, "add", "shared.txt")
	b.git(childPath, "-c", "core.editor=true", "rebase", "--continue")

	res := syncMeasure(t, true, syncCmd, syncGoldenFeature, "--continue")
	syncFreeze(t, "external-continue", fx, ws, res, nil)
}

func TestSyncNoFlag_ExternalAbortWithState(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, true)
	ws := syncGoldenEnv(t, fx.repo)

	if exit := syncUnmeasured(t, syncCmd, syncGoldenFeature); exit == 0 {
		t.Fatal("the conflict fixture must fail the preparation sync")
	}
	res := syncMeasure(t, true, syncCmd, syncGoldenFeature, "--abort")
	syncFreeze(t, "external-abort-state", fx, ws, res, nil)
}

func TestSyncNoFlag_ExternalAbortWithoutState(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, false)
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, true, syncCmd, syncGoldenFeature, "--abort")
	syncFreeze(t, "external-abort-empty", fx, ws, res, nil)
}

func TestSyncNoFlag_ExternalFallback(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, false)
	// A feature that genuinely has no readable stack.yaml under either
	// derivation takes today's frozen syncFallback path (§4.2 item 7).
	if err := os.Remove(filepath.Join(fx.featurePath, "stack.yaml")); err != nil {
		t.Fatal(err)
	}
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, true, syncCmd, syncGoldenFeature)
	syncFreeze(t, "external-fallback", fx, ws, res, nil)
}

func TestSyncNoFlag_ExternalStaleEdge(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncExternalLinear(b, syncGoldenFeature, false)
	// Advance parent past child so the parent->child edge is genuinely stale,
	// and mark child done so the resumed run cannot repair it.
	parentPath := filepath.Join(fx.worktreesRoot, "parent")
	goldenWrite(t, filepath.Join(parentPath, "later.txt"), "later\n")
	b.git(parentPath, "add", "later.txt")
	b.git(parentPath, "commit", "-m", "parent later")

	ws := syncGoldenEnv(t, fx.repo)
	state := internal.NewSyncState()
	state.Completed = []string{"child"}
	state.Pending = []string{"root", "parent"}
	if err := internal.SaveSyncState(fx.featurePath, state); err != nil {
		t.Fatal(err)
	}

	res := syncMeasure(t, true, syncCmd, syncGoldenFeature, "--continue")
	syncFreeze(t, "external-stale-edge", fx, ws, res, []syncStateRef{syncStateRefExternal(fx)})
}

func TestSyncNoFlag_CheckoutClean(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncCheckoutLinear(b, syncGoldenFeature, false)
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, false, syncCmd, syncGoldenFeature)
	syncFreeze(t, "checkout-clean", fx, ws, res, nil)
}

func TestSyncNoFlag_CheckoutConflict(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncCheckoutLinear(b, syncGoldenFeature, true)
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, false, syncCmd, syncGoldenFeature)
	syncFreeze(t, "checkout-conflict", fx, ws, res, []syncStateRef{syncStateRefCheckout(fx, syncGoldenFeature)})
}

func TestSyncNoFlag_CheckoutContinue(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncCheckoutLinear(b, syncGoldenFeature, true)
	ws := syncGoldenEnv(t, fx.repo)

	if exit := syncUnmeasured(t, syncCmd, syncGoldenFeature); exit == 0 {
		t.Fatal("the conflict fixture must fail the preparation sync")
	}
	goldenWrite(t, filepath.Join(fx.repo, "shared.txt"), "resolved\n")
	b.git(fx.repo, "add", "shared.txt")
	b.git(fx.repo, "-c", "core.editor=true", "rebase", "--continue")

	res := syncMeasure(t, false, syncCmd, syncGoldenFeature, "--continue")
	syncFreeze(t, "checkout-continue", fx, ws, res, nil)
}

func TestSyncNoFlag_CheckoutAbort(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncCheckoutLinear(b, syncGoldenFeature, true)
	ws := syncGoldenEnv(t, fx.repo)

	if exit := syncUnmeasured(t, syncCmd, syncGoldenFeature); exit == 0 {
		t.Fatal("the conflict fixture must fail the preparation sync")
	}
	res := syncMeasure(t, false, syncCmd, syncGoldenFeature, "--abort")
	syncFreeze(t, "checkout-abort", fx, ws, res, nil)
}

// ---------------------------------------------------------------------------
// Post-change C4 budget assertions (AC 2, AC 46, AC 58, AC 59)
// ---------------------------------------------------------------------------

// assertExternalResolutionBudget asserts §3.11's post-change budget: exactly
// two workspace-root resolution events per external command invocation, one
// RequireWorkspace event followed by one TwsRoot event. Each event is an
// ordered PAIR of records, never a single --git-common-dir record.
func assertExternalResolutionBudget(t *testing.T, label string, recs []normRecord) {
	t.Helper()
	events, _ := groupResolutionEvents(t, label, recs)
	if len(events) != 2 || events[0].Kind != eventRequireWorkspace || events[1].Kind != eventTwsRoot {
		t.Fatalf("%s: external run must perform exactly two workspace-root resolution events (one RequireWorkspace, then one TwsRoot); got %v", label, describeEvents(events))
	}
}

// assertContainmentProbePresent asserts the mandatory C4 containment probe of
// every checkout run: exactly one `git -C <cwd> rev-parse --show-toplevel`
// record, exiting zero and resolving to ws.RepoRoot.
func assertContainmentProbePresent(t *testing.T, label string, recs []normRecord, repoToken string) {
	t.Helper()
	found := 0
	for _, r := range recs {
		if r.TailString() != c4ContainmentProbe || r.DashC() == "" {
			continue
		}
		found++
		if r.ExitClass != "zero" {
			t.Fatalf("%s: the containment probe must exit zero; got %s", label, r)
		}
		if strings.TrimSpace(r.Out) != repoToken {
			t.Fatalf("%s: the containment probe must resolve to ws.RepoRoot (%s); got %q", label, repoToken, r.Out)
		}
	}
	if found != 1 {
		t.Fatalf("%s: expected exactly one checkout containment probe, found %d", label, found)
	}
}

// ---------------------------------------------------------------------------
// Declared-change evidence (C1, C2, C3, C4) — captured in the same pre-change
// run, and explicitly NOT goldens (§4.1 rules 4-7, §13.1).
// ---------------------------------------------------------------------------

func syncChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

// syncDivergentEnv installs a layout where internal.TwsRoot() and
// ws.MetadataRoot DISAGREE — the documented, supported TWS_ROOT override.
func syncDivergentEnv(t *testing.T, repo, twsRoot, cwd string) internal.Workspace {
	t.Helper()
	syncPinnedGitEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("TWS_ROOT", twsRoot)
	syncChdir(t, cwd)
	ws, err := internal.RequireWorkspace()
	if err != nil {
		t.Fatalf("divergent fixture workspace must resolve: %v", err)
	}
	if filepath.Clean(ws.MetadataRoot) == filepath.Clean(internal.TwsRoot()) {
		t.Fatalf("divergent fixture must diverge: both roots are %s", ws.MetadataRoot)
	}
	return ws
}

// syncEvidence stores every surface of a declared-change capture.
func syncEvidence(t *testing.T, fixture string, fx *goldenFixture, ws internal.Workspace, res syncCaptureResult, extras map[string]string) {
	t.Helper()
	reps := syncReplacements(t, fx, ws)
	norm := func(label, s string) string {
		return goldenApplyReplacements(reps, ws.StableID, s)
	}
	syncWriteEvidence(t, fixture, "stdout.txt", fmt.Sprintf("exit: %d\n%s", res.exit, norm("stdout", res.stdout)))
	syncWriteEvidence(t, fixture, "stderr.txt", fmt.Sprintf("exit: %d\n%s", res.exit, norm("stderr", res.stderr)))
	syncWriteEvidence(t, fixture, "argv.log", norm("argv", syncRenderRecords(normalizeRecords(res.records, reps, ws.StableID))))
	syncWriteEvidence(t, fixture, "refs.txt", norm("refs", syncSnapshotRefs(t, fx.repo)))
	syncWriteEvidence(t, fixture, "remote-refs.txt", norm("remote-refs", syncSnapshotRefs(t, fx.remote)))
	for name, body := range extras {
		syncWriteEvidence(t, fixture, name, norm(name, body))
	}
}

func syncReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// C1 — an unreadable .sync-state.yaml. All three verbs change (§8.6 row 10).
func TestSyncDeclaredC1_CorruptExternalState(t *testing.T) {
	for _, verb := range []struct {
		name string
		args []string
	}{
		{name: "plain", args: []string{syncGoldenFeature}},
		{name: "continue", args: []string{syncGoldenFeature, "--continue"}},
		{name: "abort", args: []string{syncGoldenFeature, "--abort"}},
	} {
		t.Run(verb.name, func(t *testing.T) {
			b := newGoldenBuilder(t)
			fx := syncExternalLinear(b, syncGoldenFeature, false)
			goldenWrite(t, internal.SyncStatePath(fx.featurePath), "pending: [oops\n\t- broken\n")
			ws := syncGoldenEnv(t, fx.repo)

			res := syncMeasure(t, true, syncCmd, verb.args...)
			syncEvidence(t, filepath.Join("declared_c1", verb.name), fx, ws, res, map[string]string{
				"sync-state.yaml": syncReadFile(t, internal.SyncStatePath(fx.featurePath)),
			})
		})
	}
}

// syncExternalDecoupled builds the `decoupled` fixture: name work, branch
// user/work (§17.2). It is a declared C2 fixture and never a frozen golden.
func syncExternalDecoupled(b *goldenBuilder, feature string) *goldenFixture {
	t := b.t
	root, repo, remote := syncRepoBase(b)
	metaRoot := repo + ".tws"
	featurePath := filepath.Join(metaRoot, feature)
	worktreesRoot := filepath.Join(featurePath, "worktrees")
	for _, dir := range []string{filepath.Join(metaRoot, ".tws-workspace"), worktreesRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.git(repo, "branch", "user/work", "main")
	path := filepath.Join(worktreesRoot, "work")
	b.git(repo, "worktree", "add", path, "user/work")
	goldenWrite(t, filepath.Join(path, "work.txt"), "work\n")
	b.git(path, "add", "work.txt")
	b.git(path, "commit", "-m", "work")

	fx := &goldenFixture{
		mode:          internal.ModeExternal,
		root:          root,
		repo:          repo,
		remote:        remote,
		metaRoot:      metaRoot,
		featurePath:   featurePath,
		worktreesRoot: worktreesRoot,
		entries:       []goldenStackEntry{{name: "work", branch: "user/work", base: "main"}},
	}
	fx.addExtra(remote, "<REMOTE>")
	goldenWrite(t, filepath.Join(featurePath, "stack.yaml"), goldenStackYAML(fx.entries))
	return fx
}

// C2 — the decoupled-name push defect, on the no-flag path (§4.5 C2).
func TestSyncDeclaredC2_DecoupledPush(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func() *cobra.Command
		args  []string
	}{
		{name: "sync-push", build: syncCmd, args: []string{syncGoldenFeature, "--push"}},
		{name: "push", build: pushCmd, args: []string{syncGoldenFeature}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newGoldenBuilder(t)
			fx := syncExternalDecoupled(b, syncGoldenFeature)
			ws := syncGoldenEnv(t, fx.repo)

			res := syncMeasure(t, true, c.build, c.args...)
			syncEvidence(t, filepath.Join("declared_c2", c.name), fx, ws, res, nil)
		})
	}
}

// C3 — duplicate GitBranch() metadata attribution on a no-flag checkout run.
func TestSyncDeclaredC3_DuplicateBranchCheckout(t *testing.T) {
	b := newGoldenBuilder(t)
	fx := syncCheckoutLinear(b, syncGoldenFeature, false)
	fx.entries = append(fx.entries, goldenStackEntry{name: "child-mirror", branch: "child", base: "parent"})
	goldenWrite(t, filepath.Join(fx.featurePath, "stack.yaml"), goldenStackYAML(fx.entries))
	ws := syncGoldenEnv(t, fx.repo)

	res := syncMeasure(t, false, syncCmd, syncGoldenFeature)
	syncEvidence(t, filepath.Join("declared_c3", "no-flag"), fx, ws, res, map[string]string{
		"stack.yaml": syncReadFile(t, filepath.Join(fx.featurePath, "stack.yaml")),
	})
}

// C4 — divergent external layouts, both directions, plus checkout cell 9.
func TestSyncDeclaredC4_DivergentLayoutAndCwd(t *testing.T) {
	t.Run("cwd-disagree-b", func(t *testing.T) {
		b := newGoldenBuilder(t)
		fx := syncExternalLinear(b, syncGoldenFeature, false)
		// Move the whole feature under TWS_ROOT: the stack lives under
		// internal.FeaturePath only (the shipped split-brain shape).
		alt := filepath.Join(fx.root, "altroot")
		if err := os.MkdirAll(alt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fx.featurePath, filepath.Join(alt, syncGoldenFeature)); err != nil {
			t.Fatal(err)
		}
		fx.featurePath = filepath.Join(alt, syncGoldenFeature)
		fx.worktreesRoot = filepath.Join(fx.featurePath, "worktrees")
		fx.addExtra(alt, "<ALTROOT>")
		b.git(fx.repo, "worktree", "repair", fx.worktreesRoot+"/root", fx.worktreesRoot+"/parent", fx.worktreesRoot+"/child")
		ws := syncDivergentEnv(t, fx.repo, alt, fx.repo)

		res := syncMeasure(t, true, syncCmd, syncGoldenFeature)
		syncEvidence(t, filepath.Join("declared_c4", "cwd-disagree-b"), fx, ws, res, map[string]string{
			"workspace-state.yaml": syncReadFile(t, internal.SyncStatePath(filepath.Join(ws.MetadataRoot, syncGoldenFeature))),
			"layout.txt":           fmt.Sprintf("tws_root=%s\nmetadata_root=%s\n", internal.TwsRoot(), ws.MetadataRoot),
		})
	})

	t.Run("cwd-disagree-a", func(t *testing.T) {
		b := newGoldenBuilder(t)
		fx := syncExternalLinear(b, syncGoldenFeature, false)
		// The stack stays under ws.ResolveFeaturePath while TWS_ROOT points at
		// an empty root, so today's run falls into syncFallback.
		alt := filepath.Join(fx.root, "altroot")
		if err := os.MkdirAll(filepath.Join(alt, ".tws-workspace"), 0o755); err != nil {
			t.Fatal(err)
		}
		ws := syncDivergentEnv(t, fx.repo, alt, fx.repo)
		fx.addExtra(alt, "<ALTROOT>")

		res := syncMeasure(t, true, syncCmd, syncGoldenFeature)
		syncEvidence(t, filepath.Join("declared_c4", "cwd-disagree-a"), fx, ws, res, map[string]string{
			"layout.txt": fmt.Sprintf("tws_root=%s\nmetadata_root=%s\n", internal.TwsRoot(), ws.MetadataRoot),
		})
	})

	t.Run("checkout-cell9", func(t *testing.T) {
		b := newGoldenBuilder(t)
		fx := syncCheckoutLinear(b, syncGoldenFeature, false)
		linked := filepath.Join(fx.root, "linked")
		b.git(fx.repo, "branch", "side", "main")
		b.git(fx.repo, "worktree", "add", linked, "side")
		fx.addExtra(linked, "<LINKED>")
		ws := syncGoldenEnv(t, fx.repo)
		syncChdir(t, linked)

		before := syncSnapshotRefs(t, fx.repo)
		res := syncMeasure(t, false, syncCmd, syncGoldenFeature)
		syncEvidence(t, filepath.Join("declared_c4", "checkout-cell9"), fx, ws, res, map[string]string{
			"refs-before.txt": before,
			"linked-head.txt": gitOutput(t, linked, "rev-parse", "--abbrev-ref", "HEAD"),
			"stack.yaml":      syncReadFile(t, filepath.Join(fx.featurePath, "stack.yaml")),
		})
	})
}
