package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The authoritative T -> requirement -> function mapping lives only in
// reparent_normative_matrix_test.go. This file owns runtime scenario counting
// and frozen-surface hygiene; it deliberately carries no second ownership
// ledger.

// ReparentCLILeafBudget is this package's share of §17.3's 120-leaf total.
// The internal package declares its own share; the sum is asserted below, so
// neither package can quietly spend the other's. It is the measured
// 44-scenario
// count; any new real-Git leaf requires an explicit consolidation or budget
// review.
const ReparentCLILeafBudget = 44

// reparentCLILeafNames records every repository-building scenario, including
// multiple anonymous closures under one testing.T.
var (
	reparentCLILeafMu    sync.Mutex
	reparentCLILeafNames []string
	reparentCLIPending   = map[string][]string{}
	reparentCLIArgv      = map[string][][]string{}
)

func reparentCountCLIGitLeaf(t *testing.T) {
	t.Helper()
	base := strings.SplitN(t.Name(), "/", 2)[0]
	owned := false
	for _, row := range reparentNormativeMatrix {
		if row.Function == base {
			owned = true
			break
		}
	}
	if !owned {
		return
	}
	reparentCLILeafMu.Lock()
	id := t.Name() + "#" + strconv.Itoa(len(reparentCLILeafNames)+1)
	reparentCLILeafNames = append(reparentCLILeafNames, id)
	reparentCLIPending[t.Name()] = append(reparentCLIPending[t.Name()], id)
	reparentCLILeafMu.Unlock()
}

func reparentRecordGitArgv(t *testing.T, args ...string) {
	t.Helper()
	argv := append([]string{"git"}, args...)
	reparentCLILeafMu.Lock()
	defer reparentCLILeafMu.Unlock()
	for name := t.Name(); ; {
		for _, id := range reparentCLIPending[name] {
			reparentCLIArgv[id] = append(reparentCLIArgv[id], append([]string{}, argv...))
		}
		delete(reparentCLIPending, name)
		i := strings.LastIndex(name, "/")
		if i < 0 {
			break
		}
		name = name[:i]
	}
}

func reparentCLIMissingArgvEvidence() []string {
	reparentCLILeafMu.Lock()
	defer reparentCLILeafMu.Unlock()
	var missing []string
	for _, id := range reparentCLILeafNames {
		if len(reparentCLIArgv[id]) == 0 {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

func reparentCLIMeasuredLeaves() int {
	reparentCLILeafMu.Lock()
	defer reparentCLILeafMu.Unlock()
	return len(reparentCLILeafNames)
}

func prepareCLITestGitEnvironment() error {
	if err := os.Setenv("GIT_CONFIG_COUNT", "0"); err != nil {
		return err
	}
	return os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func reparentFullTestRun() bool {
	for _, name := range []string{"test.run", "test.skip", "test.list"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return false
		}
	}
	return true
}

func reparentLeafBudgetError(pkg string, used, want int, exact bool) error {
	if used > want {
		return fmt.Errorf("reparent: package %s spawned %d real-Git leaves, over its budget of %d", pkg, used, want)
	}
	if exact && used != want {
		return fmt.Errorf("reparent: package %s spawned %d real-Git leaves, want exactly %d on an unfiltered full run", pkg, used, want)
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

// TestMain sanitizes inherited Git config before the first test can spawn Git,
// then enforces an exact count on an unfiltered run and a ceiling on -run/-skip
// selections.
func TestMain(m *testing.M) {
	if err := prepareCLITestGitEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if dump := os.Getenv("REPARENT_DUMP_LEAVES_CLI"); dump != "" {
		reparentCLILeafMu.Lock()
		names := append([]string{}, reparentCLILeafNames...)
		reparentCLILeafMu.Unlock()
		sort.Strings(names)
		_ = os.WriteFile(dump, []byte(strings.Join(names, "\n")+"\n"), 0o644)
	}
	if missing := reparentCLIMissingArgvEvidence(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "reparent: package cli counted real-Git leaves without argv evidence: %s\n", strings.Join(missing, ", "))
		if code == 0 {
			code = 1
		}
	}
	if err := reparentLeafBudgetError("cli", reparentCLIMeasuredLeaves(), ReparentCLILeafBudget, code == 0 && reparentFullTestRun()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestCLIProcessGitEnvironmentIsHermetic(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	if err := prepareCLITestGitEnvironment(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "0" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 0", got)
	}
	if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want 1", got)
	}
}

func TestReparentMatrix_FullRunsRequireExactCountsAndPartialRunsUseCeilings(t *testing.T) {
	if err := reparentLeafBudgetError("cli", 44, 44, true); err != nil {
		t.Fatalf("exact full run was rejected: %v", err)
	}
	if err := reparentLeafBudgetError("cli", 43, 44, true); err == nil {
		t.Fatal("an under-counted full run must fail")
	}
	if err := reparentLeafBudgetError("cli", 43, 44, false); err != nil {
		t.Fatalf("a partial run below the ceiling must pass: %v", err)
	}
	if err := reparentLeafBudgetError("cli", 45, 44, false); err == nil {
		t.Fatal("a partial run above the ceiling must fail")
	}
}

// TestReparentMatrix_RealGitLeafBudgetIsSharedAcrossBothPackages is §17.3's
// total. Each package measures its own leaves at runtime and fails its own
// ceiling in TestMain; this assertion pins the two ceilings to the one budget
// the spec sets, so the suite as a whole can never exceed it.
func TestReparentMatrix_RealGitLeafBudgetIsSharedAcrossBothPackages(t *testing.T) {
	const budget = 120
	total := ReparentCLILeafBudget + reparentInternalLeafBudget(t)
	if total > budget {
		t.Fatalf("the declared package budgets sum to %d, over §17.3's %d", total, budget)
	}
	if used := reparentCLIMeasuredLeaves(); used > ReparentCLILeafBudget {
		t.Fatalf("package cli already measured %d leaves, over its share of %d", used, ReparentCLILeafBudget)
	}
	t.Logf("leaf budget: cli<=%d + internal<=%d = %d of %d",
		ReparentCLILeafBudget, total-ReparentCLILeafBudget, total, budget)
}

// reparentInternalLeafBudget reads the sibling package's declared share from
// its own ledger file. A test-only constant is not importable across packages,
// and duplicating the number here would let the two drift apart silently; the
// regex fails loudly if that ledger is renamed or its constant removed.
func reparentInternalLeafBudget(t *testing.T) int {
	t.Helper()
	const ledger = "../reparent_matrix_test.go"
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("the package-internal matrix ledger must exist at %s: %v", ledger, err)
	}
	m := regexp.MustCompile(`(?m)^const ReparentInternalLeafBudget = (\d+)$`).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s must declare `const ReparentInternalLeafBudget = <n>`", ledger)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReparentMatrix_EveryRealGitLeafPinsGitConfigCount is §17.4's hygiene
// rule. A leaf that inherits a hostile host `git config` is not measuring what
// it claims, and T-040 deliberately sets hostile `rebase.*` values of its own.
func TestReparentMatrix_EveryRealGitLeafPinsGitConfigCount(t *testing.T) {
	for _, name := range []string{
		"reparent_fixtures_test.go",
		"../reparent_fixtures_test.go",
		"../reparent_run_test.go",
	} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		if !strings.Contains(src, `t.Setenv("GIT_CONFIG_COUNT", "0")`) {
			t.Fatalf("%s must pin GIT_CONFIG_COUNT=0 for every fixture it builds", name)
		}
		if filepath.Base(name) == "reparent_fixtures_test.go" && !strings.Contains(src, `"GIT_CONFIG_COUNT=0"`) {
			t.Fatalf("%s must scrub GIT_CONFIG_COUNT in every subprocess form too", name)
		}
		if !strings.Contains(src, `GIT_CONFIG_NOSYSTEM`) {
			t.Fatalf("%s must disable system Git config for every process form", name)
		}
		if strings.HasPrefix(name, "../") {
			if !strings.Contains(src, "reparentCountGitLeafFor(t)") {
				t.Fatalf("%s must count every repository-building leaf", name)
			}
		} else if !strings.Contains(src, "reparentCountCLIGitLeaf(t)") {
			t.Fatalf("%s must count every repository-building leaf", name)
		}
	}

	for _, pattern := range []string{"*reparent*_test.go", "../*reparent*_test.go"} {
		directGitSpawn := `exec.Command(` + `"git"`
		paths, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count := strings.Count(string(data), directGitSpawn)
			if count == 0 {
				continue
			}
			if filepath.Base(path) != "reparent_fixtures_test.go" || count != 1 {
				t.Errorf("%s has %d direct git spawns; reparent tests must route every spawn through the counted fixture command", path, count)
			}
		}
	}

	for _, special := range []struct {
		path    string
		counter string
		log     string
	}{
		{"../session_test.go", "reparentCountGitLeafFor(t)", "session test git argv"},
		{"checkout_sync_modes_test.go", "reparentCountCLIGitLeaf(t)", "checkout sync test git argv"},
	} {
		data, err := os.ReadFile(special.path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		for _, required := range []string{
			special.counter,
			special.log,
			"reparentRecordGitArgv(t, args...)",
			`"GIT_CONFIG_COUNT=0"`,
			`"GIT_CONFIG_NOSYSTEM=1"`,
		} {
			if !strings.Contains(src, required) {
				t.Errorf("%s must contain %q for its reparent real-Git cell", special.path, required)
			}
		}
	}
}

// TestReparentMatrix_FrozenSurfacesStillExist re-asserts the frozen corpus is
// present and unmoved in shape: 126 no-flag artifacts, the sync help snapshot,
// and the two push goldens rule R-PUSH must leave byte-identical.
func TestReparentMatrix_FrozenSurfacesStillExist(t *testing.T) {
	count := 0
	err := filepath.WalkDir("testdata/sync_noflag", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 126 {
		t.Fatalf("the frozen no-flag corpus must hold exactly 126 files, found %d", count)
	}
	for _, rel := range []string{
		"testdata/rebase_plan/sync_help.txt",
		"testdata/sync_noflag/declared_c2/push",
		"testdata/sync_noflag/declared_c2/sync-push",
		"testdata/reparent/stack_help.txt",
		"testdata/reparent/reparent_help.txt",
		"testdata/reparent/legacy_stack_feature.txt",
	} {
		if _, err := os.Stat(rel); err != nil {
			t.Fatalf("the frozen surface %s must exist: %v", rel, err)
		}
	}
}
