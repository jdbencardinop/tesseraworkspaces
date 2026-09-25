package internal

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// The measured real-Git leaf budget (§17.3)
// ---------------------------------------------------------------------------

// ReparentInternalLeafBudget is this package's share of §17.3's 120-leaf
// total. The CLI package declares its own share and asserts that the two sum
// to at most the total, so neither package can quietly spend the other's.
// The value is the MEASURED normative-scenario count (76 after T-061 began
// executing all 17 windows for both verbs), so a new cell
// is a deliberate decision rather than silent drift.
const ReparentInternalLeafBudget = 76

// reparentInternalLeafNames records every repository-building scenario,
// including multiple anonymous closures under one testing.T.
var (
	reparentInternalLeafMu    sync.Mutex
	reparentInternalLeafNames []string
	reparentInternalPending   = map[string][]string{}
	reparentInternalArgv      = map[string][][]string{}
	reparentOwnerOnce         sync.Once
	reparentOwnerFunctions    map[string]bool
)

func reparentCountGitLeafFor(t *testing.T) {
	t.Helper()
	if !reparentInternalNormativeOwner(t.Name()) {
		return
	}
	reparentInternalLeafMu.Lock()
	id := t.Name() + "#" + strconv.Itoa(len(reparentInternalLeafNames)+1)
	reparentInternalLeafNames = append(reparentInternalLeafNames, id)
	reparentInternalPending[t.Name()] = append(reparentInternalPending[t.Name()], id)
	reparentInternalLeafMu.Unlock()
}

func reparentRecordGitArgv(t *testing.T, args ...string) {
	t.Helper()
	argv := append([]string{"git"}, args...)
	reparentInternalLeafMu.Lock()
	defer reparentInternalLeafMu.Unlock()
	for name := t.Name(); ; {
		for _, id := range reparentInternalPending[name] {
			reparentInternalArgv[id] = append(reparentInternalArgv[id], append([]string{}, argv...))
		}
		delete(reparentInternalPending, name)
		i := strings.LastIndex(name, "/")
		if i < 0 {
			break
		}
		name = name[:i]
	}
}

func reparentInternalMissingArgvEvidence() []string {
	reparentInternalLeafMu.Lock()
	defer reparentInternalLeafMu.Unlock()
	var missing []string
	for _, id := range reparentInternalLeafNames {
		if len(reparentInternalArgv[id]) == 0 {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

func reparentInternalNormativeOwner(testName string) bool {
	reparentOwnerOnce.Do(func() {
		reparentOwnerFunctions = map[string]bool{}
		data, err := os.ReadFile("cli/reparent_normative_matrix_test.go")
		if err != nil {
			return
		}
		re := regexp.MustCompile(`matrixRow\(\d+,\s*"[^"]+",\s*"[^"]+",\s*"([^"]+)"`)
		for _, match := range re.FindAllStringSubmatch(string(data), -1) {
			reparentOwnerFunctions[match[1]] = true
		}
	})
	base := strings.SplitN(testName, "/", 2)[0]
	return reparentOwnerFunctions[base]
}

// ReparentInternalMeasuredLeaves is the count so far.
func ReparentInternalMeasuredLeaves() int {
	reparentInternalLeafMu.Lock()
	defer reparentInternalLeafMu.Unlock()
	return len(reparentInternalLeafNames)
}

func reparentInternalFullTestRun() bool {
	for _, name := range []string{"test.run", "test.skip", "test.list"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return false
		}
	}
	return true
}

func reparentInternalLeafBudgetError(used int, exact bool) error {
	if used > ReparentInternalLeafBudget {
		return fmt.Errorf("reparent: package internal spawned %d real-Git leaves, over its budget of %d", used, ReparentInternalLeafBudget)
	}
	if exact && used != ReparentInternalLeafBudget {
		return fmt.Errorf("reparent: package internal spawned %d real-Git leaves, want exactly %d on an unfiltered full run", used, ReparentInternalLeafBudget)
	}
	return nil
}

func assertReparentMatrixBehavior(t *testing.T, cell string, facts ...string) {
	t.Helper()
	if !regexp.MustCompile(`^T-[0-9]{3}$`).MatchString(cell) {
		t.Fatalf("invalid matrix cell marker %q", cell)
	}
	if len(facts) == 0 {
		t.Fatalf("%s declares no behavioral facts", cell)
	}
	seen := map[string]bool{}
	for _, fact := range facts {
		if strings.TrimSpace(fact) == "" || seen[fact] {
			t.Fatalf("%s has invalid behavioral fact %q", cell, fact)
		}
		seen[fact] = true
	}
}

func prepareInternalTestGitEnvironment() error {
	if err := os.Setenv("GIT_CONFIG_COUNT", "0"); err != nil {
		return err
	}
	return os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// TestMain requires the exact measured count on an unfiltered full run. A
// partial `-run`/`-skip` selection retains ceiling-only behavior.
func TestMain(m *testing.M) {
	if err := prepareInternalTestGitEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if os.Getenv("REPARENT_DUMP_LEAVES") != "" {
		reparentInternalLeafMu.Lock()
		names := append([]string{}, reparentInternalLeafNames...)
		reparentInternalLeafMu.Unlock()
		sort.Strings(names)
		_ = os.WriteFile(os.Getenv("REPARENT_DUMP_LEAVES"), []byte(strings.Join(names, "\n")+"\n"), 0o644)
	}
	if missing := reparentInternalMissingArgvEvidence(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "reparent: package internal counted real-Git leaves without argv evidence: %s\n", strings.Join(missing, ", "))
		if code == 0 {
			code = 1
		}
	}
	if err := reparentInternalLeafBudgetError(ReparentInternalMeasuredLeaves(), code == 0 && reparentInternalFullTestRun()); err != nil {
		println(err.Error())
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestInternalProcessGitEnvironmentIsHermetic(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	if err := prepareInternalTestGitEnvironment(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "0" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 0", got)
	}
	if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want 1", got)
	}
}

func TestReparentMatrixFullRunsRequireExactCountsAndPartialRunsUseCeilings(t *testing.T) {
	if err := reparentInternalLeafBudgetError(76, true); err != nil {
		t.Fatalf("exact full run was rejected: %v", err)
	}
	if err := reparentInternalLeafBudgetError(75, true); err == nil {
		t.Fatal("an under-counted full run must fail")
	}
	if err := reparentInternalLeafBudgetError(75, false); err != nil {
		t.Fatalf("a partial run below the ceiling must pass: %v", err)
	}
	if err := reparentInternalLeafBudgetError(77, false); err == nil {
		t.Fatal("a partial run above the ceiling must fail")
	}
}
