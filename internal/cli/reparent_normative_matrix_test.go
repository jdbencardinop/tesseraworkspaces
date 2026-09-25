package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type reparentMatrixRow struct {
	ID           string
	Requirements []string
	Dir          string
	File         string
	Function     string
}

func matrixRow(id int, dir, file, function string, requirements ...string) reparentMatrixRow {
	return reparentMatrixRow{
		ID: fmt.Sprintf("T-%03d", id), Requirements: requirements,
		Dir: dir, File: file, Function: function,
	}
}

// reparentNormativeMatrix is the executable form of spec §17.2. It maps each
// cell to its required AC/section and to the concrete test function that owns
// the behavioral assertion; comments and citation counts are not evidence.
var reparentNormativeMatrix = []reparentMatrixRow{
	matrixRow(1, ".", "reparent_e2e_test.go", "TestReparentE2E_CustomerTopologyExternal", "AC-001", "AC-032", "AC-033"),
	matrixRow(2, ".", "reparent_e2e_test.go", "TestReparentE2E_CustomerTopologyCheckout", "AC-032", "AC-033", "AC-043", "AC-056"),
	matrixRow(3, ".", "reparent_e2e_test.go", "TestReparentE2E_Issue4BranchingClosure", "AC-069", "AC-033"),
	matrixRow(4, "..", "reparent_destination_test.go", "TestResolveReparentCutoffLadders", "AC-022", "AC-023"),
	matrixRow(5, "..", "reparent_destination_test.go", "TestResolveReparentCutoffLadders", "AC-024", "AC-025", "AC-026"),
	matrixRow(6, "..", "reparent_destination_test.go", "TestResolveReparentCutoffLadders", "AC-027"),
	matrixRow(7, "..", "reparent_destination_test.go", "TestResolveReparentCutoffLadders", "AC-028", "AC-029"),
	matrixRow(8, "..", "reparent_destination_test.go", "TestResolveReparentCutoffLadders", "AC-030"),
	matrixRow(9, ".", "reparent_source_audit_test.go", "TestReparentSourceAudit_NoMergeBaseOutsideTheSharedLadder", "AC-031"),
	matrixRow(10, "..", "reparent_destination_test.go", "TestReparentDestinationPrimitives", "AC-015"),
	matrixRow(11, "..", "reparent_destination_test.go", "TestResolveReparentDestination_RefusalRungs", "AC-014"),
	matrixRow(12, "..", "reparent_destination_test.go", "TestResolveReparentDestination_RefusalRungs", "AC-015"),
	matrixRow(13, "..", "reparent_run_test.go", "TestReparentSHA256RepositoryEndToEnd", "AC-016"),
	matrixRow(14, "..", "reparent_destination_test.go", "TestResolveReparentDestination_RefusalRungs", "AC-017"),
	matrixRow(15, "..", "reparent_destination_test.go", "TestResolveReparentDestination_EntryAndRefKinds", "AC-011", "AC-012", "AC-013"),
	matrixRow(16, "..", "reparent_destination_test.go", "TestResolveReparentDestination_EntryAndRefKinds", "AC-013", "AC-020"),
	matrixRow(17, "..", "reparent_destination_test.go", "TestResolveReparentAgreement_FourResolvers", "AC-021"),
	matrixRow(18, "..", "reparent_destination_test.go", "TestResolveReparentDestination_EntryAndRefKinds", "AC-018"),
	matrixRow(19, "..", "reparent_plan_build_test.go", "TestBuildReparentPlanGraphSafety", "AC-019"),
	matrixRow(20, "..", "reparent_destination_test.go", "TestResolveReparentDestination_EntryAndRefKinds", "AC-020"),
	matrixRow(21, "..", "reparent_plan_build_test.go", "TestBuildReparentPlanClosureAndMetadataRecipe", "§7.11"),
	matrixRow(22, "..", "reparent_run_test.go", "TestReparentPerRowRepositoryContext", "AC-034"),
	matrixRow(23, "..", "reparent_plan_build_test.go", "TestBuildReparentPlanScopeGates", "AC-035"),
	matrixRow(24, ".", "stack_reparent_cli_test.go", "TestStackReparentCLI_PlanFailuresAreDocumentsAndIdentityGatesArePure", "AC-015", "AC-036", "AC-041", "AC-043"),
	matrixRow(25, "..", "stack_test.go", "TestReparentClosureOrder_DeclarationOrderIsTheSoleSiblingTieBreak", "AC-037"),
	matrixRow(26, "..", "reparent_refs_test.go", "TestReparentEntryRefIDIsRefSafeAndCollisionFree", "§9.8", "AC-036"),
	matrixRow(27, ".", "reparent_exclusion_test.go", "TestReparentExclusion_DirtyGitOpAndHolderBoundaries", "AC-038"),
	matrixRow(28, "..", "reparent_run_test.go", "TestReparentHolderLifecycle", "AC-038", "§9.9"),
	matrixRow(29, ".", "reparent_exclusion_test.go", "TestReparentExclusion_DirtyGitOpAndHolderBoundaries", "AC-038"),
	matrixRow(30, ".", "reparent_exclusion_test.go", "TestReparentExclusion_DirtyGitOpAndHolderBoundaries", "AC-039", "AC-040"),
	matrixRow(31, ".", "stack_reparent_cli_test.go", "TestStackReparentCLI_PlanFailuresAreDocumentsAndIdentityGatesArePure", "AC-016", "AC-041", "AC-043", "AC-044", "AC-045", "AC-046"),
	matrixRow(32, "..", "reparent_plan_render_test.go", "TestFormatReparentPlan_SectionsAndDeferredLiteral", "AC-046"),
	matrixRow(33, "..", "reparent_plan_test.go", "TestReparentFrozenConstants", "AC-042"),
	matrixRow(34, "..", "reparent_plan_fingerprint_test.go", "TestReparentPlanFingerprint_Sensitivity", "AC-047"),
	matrixRow(35, "..", "reparent_plan_fingerprint_test.go", "TestReparentCrossDomainApprovalAdmissionRejectsBothDirections", "AC-048"),
	matrixRow(36, "..", "reparent_plan_build_test.go", "TestReparentAdmissionPredicates", "AC-049", "AC-050"),
	matrixRow(37, ".", "reparent_transaction_test.go", "TestReparentIntegration_PlanIsSideEffectFree", "AC-051"),
	matrixRow(38, ".", "stack_reparent_cli_test.go", "TestStackReparentValidate_LadderOrder", "AC-004", "AC-005", "AC-006", "AC-007", "AC-008", "AC-009", "AC-010"),
	matrixRow(39, ".", "reparent_transaction_test.go", "TestReparentIntegration_ArgvShapeIsPathFree", "AC-052", "AC-053"),
	matrixRow(40, ".", "reparent_transaction_test.go", "TestReparentIntegration_HostileRebaseConfigCannotChangeTheReplay", "AC-054"),
	matrixRow(41, ".", "reparent_source_audit_test.go", "TestReparentSourceAudit_ForbiddenVerbsAreAbsent", "AC-055"),
	matrixRow(42, ".", "reparent_transaction_test.go", "TestReparentIntegration_ScratchLifecycle", "AC-056"),
	matrixRow(43, "..", "reparent_run_test.go", "TestReparentJITRevalidation", "AC-057"),
	matrixRow(44, "..", "reparent_run_test.go", "TestReparentTransactionEnvelope", "AC-058"),
	matrixRow(45, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-059"),
	matrixRow(46, "..", "reparent_run_test.go", "TestReparentCASRaceUnderReftableBackend", "AC-059"),
	matrixRow(47, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-060"),
	matrixRow(48, "..", "reparent_run_test.go", "TestReparentValidationRunsAfterPinningAndRefusesResidue", "AC-061"),
	matrixRow(49, "..", "reparent_plan_build_test.go", "TestBuildReparentPlanScopeGates", "AC-062"),
	matrixRow(50, "..", "reparent_run_test.go", "TestReparentValidationRunsAfterPinningAndRefusesResidue", "AC-063"),
	matrixRow(51, "..", "reparent_run_test.go", "TestReparentWorkingTreeProbes", "AC-064"),
	matrixRow(52, "..", "reparent_run_test.go", "TestReparentWaivedKindsAndFailureDomainPersistence", "AC-065", "AC-074"),
	matrixRow(53, "..", "reparent_run_test.go", "TestReparentWaivedKindsAndFailureDomainPersistence", "AC-076"),
	matrixRow(54, "..", "reparent_plan_build_test.go", "TestBuildReparentPlanUnprobedCapabilityPublishesNoVerdict", "AC-066"),
	matrixRow(55, "..", "reparent_run_test.go", "TestBeginReparentRunRefusesFactsThatAroseAfterThePlan", "AC-047", "AC-067"),
	matrixRow(56, ".", "reparent_exclusion_test.go", "TestReparentExclusion_SessionAndRuntimeState", "AC-068"),
	matrixRow(57, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-069", "AC-073"),
	matrixRow(58, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-070"),
	matrixRow(59, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-071"),
	matrixRow(60, "..", "reparent_metadata_test.go", "TestDurableWriteFile_FaultBeforeRenamePreservesPreviousFile", "AC-072"),
	matrixRow(61, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-074"),
	matrixRow(62, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-075"),
	matrixRow(63, "..", "reparent_run_test.go", "TestAbortReparentArms", "AC-076", "AC-092"),
	matrixRow(64, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-076"),
	matrixRow(65, ".", "reparent_transaction_test.go", "TestReparentRecovery_RecoveryVerbsRejectEveryFrozenFlag", "AC-005", "AC-077"),
	matrixRow(66, ".", "reparent_transaction_test.go", "TestReparentRecovery_CleanupIsRepeatable", "AC-078"),
	matrixRow(67, ".", "reparent_exclusion_test.go", "TestReparentExclusion_SyncRefusesEveryVerb", "AC-079"),
	matrixRow(68, "..", "reparent_state_test.go", "TestReparentCompatArtifactMissingIsDetected", "AC-080"),
	matrixRow(69, "..", "checkout_sync_plan_test.go", "TestReclaimCheckoutLock_ReusesTheShippedLadder", "AC-081"),
	matrixRow(70, ".", "sync_downgrade_test.go", "TestReparentDowngrade_PriorBinaryFailsClosed", "AC-082"),
	matrixRow(71, "..", "reparent_state_test.go", "TestReparentExternalCompatWriteOrderAndShape", "AC-083"),
	matrixRow(72, ".", "reparent_observability_test.go", "TestReparentObservability_AllSixStatusesAcrossHumanSurfaces", "AC-084"),
	matrixRow(73, ".", "reparent_observability_test.go", "TestReparentObservability_AbsentStateIsByteIdentical", "AC-085"),
	matrixRow(74, ".", "reparent_remote_test.go", "TestReparentRemote_NoProviderBinaryIsEverInvoked", "AC-086"),
	matrixRow(75, "..", "reparent_remote_test.go", "TestReparentRemoteGuidanceLines", "AC-087", "AC-093"),
	matrixRow(76, "..", "reparent_run_test.go", "TestReparentCrashWindowMatrixEveryVerbTwice", "AC-088"),
	matrixRow(77, ".", "reparent_remote_test.go", "TestReparentRemote_PushWarnsAndStrengthensTheLease", "AC-089"),
	matrixRow(78, ".", "reparent_remote_test.go", "TestReparentRemote_PreflightRefusesBeforeAnyPush", "AC-090", "AC-092"),
	matrixRow(79, ".", "reparent_remote_test.go", "TestReparentRemote_DryRunPreviewsWithoutWriting", "AC-091", "AC-092"),
	matrixRow(80, "..", "reparent_remote_test.go", "TestReparentRemoteClearingAgainstRealGit", "AC-092"),
	matrixRow(81, "..", "reparent_plan_test.go", "TestReparentRefusalKinds_ExactlyFortyNineInRankOrder", "AC-094", "AC-097"),
	matrixRow(82, "..", "reparent_run_test.go", "TestReparentWaivedKindsAndFailureDomainPersistence", "AC-095"),
	matrixRow(83, "..", "reparent_run_test.go", "TestReparentHolderLifecycle", "AC-096"),
	matrixRow(84, "..", "reparent_plan_build_test.go", "TestReparentValidationOrderPrecedence", "AC-098"),
	matrixRow(85, ".", "stack_reparent_cli_test.go", "TestStackReparent_LegacyStackAndHelpAreFrozen", "AC-002", "AC-003", "AC-008", "AC-099"),
	matrixRow(86, ".", "sync_plan_docs_test.go", "TestReparentDocs_NormativeClaimsAcrossEightSurfaces", "AC-100"),
	matrixRow(87, ".", "reparent_source_audit_test.go", "TestReparentSourceAudit_SoleBaseRewriter", "§10"),
}

var reparentMatrixExpectedFacts = map[string][]string{
	"T-001": {"customer-external-plan-execute"},
	"T-002": {"customer-checkout-plan-execute"},
	"T-003": {"issue4-branching-closure-both-modes"},
	"T-004": {"cutoff-recorded-conflict-unresolvable"},
	"T-005": {"cutoff-supplied-old-parent-absent"},
	"T-006": {"cutoff-nonancestor-refusal"},
	"T-007": {"descendant-cutoff-snapshot"},
	"T-008": {"cutoff-ambiguity-one-kind"},
	"T-009": {"merge-base-shared-ladder"},
	"T-010": {"destination-primitives"},
	"T-011": {"destination-ambiguous-short-ref"},
	"T-012": {"oid-disambiguation"},
	"T-013": {"sha256-end-to-end", "object-format-failure-stays-capability"},
	"T-014": {"pseudo-ref-rejected"},
	"T-015": {"onto-kind-matrix"},
	"T-016": {"canonical-stored-literal"},
	"T-017": {"resolver-agreement"},
	"T-018": {"literal-root"},
	"T-019": {"graph-cycle-safety"},
	"T-020": {"default-branch-token-preserved"},
	"T-021": {"ancestor-warning"},
	"T-022": {"per-row-repository-context", "repo-alias-resolver-refusal"},
	"T-023": {"archived-scope-gates"},
	"T-024": {"identity-gates-and-plan-array-totality", "raw-operator-token-preservation", "external-metadata-cwd-plan"},
	"T-025": {"sibling-declaration-order"},
	"T-026": {"collision-safe-entry-ref"},
	"T-027": {"dirty-gitop-holder-boundaries"},
	"T-028": {"holder-lifecycle"},
	"T-029": {"live-and-undecodable-sessions"},
	"T-030": {"same-parent-no-work-and-stale"},
	"T-031": {"unavailable-plan-cli-contract", "unavailable-null-oid-and-human-render"},
	"T-032": {"human-sections-and-deferred-literal"},
	"T-033": {"sync-domains-frozen"},
	"T-034": {"fingerprint-sensitivity"},
	"T-035": {"cross-domain-admission-both-directions"},
	"T-036": {"fresh-and-resume-admission-predicates"},
	"T-037": {"plan-side-effect-free", "fetch-projection-shared-semantics"},
	"T-038": {"validation-arity-and-usage-contract"},
	"T-039": {"argv-template-materialization-no-dash-c"},
	"T-040": {"hostile-rebase-config-neutralized"},
	"T-041": {"forbidden-verbs-source-and-argv"},
	"T-042": {"scratch-lifecycle-and-filtering"},
	"T-043": {"jit-destination-and-limit-revalidation"},
	"T-044": {"single-cas-transaction-envelope"},
	"T-045": {"pins-and-files-backend-cas"},
	"T-046": {"reftable-cas-race"},
	"T-047": {"reference-transaction-prepare-veto"},
	"T-048": {"pin-before-validation"},
	"T-049": {"merge-range-scope-gate"},
	"T-050": {"validation-and-residue", "preexisting-untracked-validation"},
	"T-051": {"working-tree-probes"},
	"T-052": {"conflict-and-native-failure-contract"},
	"T-053": {"conflict-abort"},
	"T-054": {"capability-floor-and-unknown-backend"},
	"T-055": {"post-lock-fact-revalidation"},
	"T-056": {"session-launch-and-runtime-exclusion", "checkout-intent-symlink-rooted", "checkout-feature-dir-intent"},
	"T-057": {"metadata-exactness"},
	"T-058": {"post-image-before-cas"},
	"T-059": {"metadata-before-hash-gate"},
	"T-060": {"durable-writer-faults"},
	"T-061": {"all-crash-windows-resume"},
	"T-062": {"partial-commit-reconciliation"},
	"T-063": {"abort-before-and-after-commit", "repeat-abort-original-detached", "postcommit-abort-preserves-remote-record", "abort-remote-restore-journal"},
	"T-064": {"postcommit-operator-advance"},
	"T-065": {"recovery-flags-rejected"},
	"T-066": {"cleanup-repeatable"},
	"T-067": {"sync-reparent-exclusion", "external-sync-guard-handshake"},
	"T-068": {"compat-missing-recovery", "owned-missing-is-not-fatal"},
	"T-069": {"checkout-lock-reclaim", "workspace-orphan-state-scan"},
	"T-070": {"v1216-downgrade-six-legs"},
	"T-071": {"external-compat-write-order"},
	"T-072": {"six-observability-statuses"},
	"T-073": {"absent-state-byte-identity", "ancestry-repo-resolution-unchanged"},
	"T-074": {"zero-provider-calls"},
	"T-075": {"remote-guidance-order"},
	"T-076": {"remote-record-before-cas"},
	"T-077": {"push-warning-and-strengthened-lease"},
	"T-078": {"preflight-before-any-push"},
	"T-079": {"dry-run-no-write"},
	"T-080": {"remote-clear-observations"},
	"T-081": {"refusal-domain-and-prefixes"},
	"T-082": {"waived-kinds-and-failure-domain"},
	"T-083": {"holder-restore-deferral"},
	"T-084": {"validation-precedence"},
	"T-085": {"legacy-stack-and-help-goldens"},
	"T-086": {"documentation-surfaces"},
	"T-087": {"sole-base-rewriter"},
}

func TestReparentNormativeMatrixMapsEveryCellAndAcceptanceCriterion(t *testing.T) {
	cells := map[string]reparentMatrixRow{}
	acs := map[string][]string{}
	for _, row := range reparentNormativeMatrix {
		if prior, exists := cells[row.ID]; exists {
			t.Fatalf("%s is mapped twice (%s and %s)", row.ID, prior.Function, row.Function)
		}
		cells[row.ID] = row
		if len(row.Requirements) == 0 {
			t.Errorf("%s has no AC/section requirement", row.ID)
		}
		for _, requirement := range row.Requirements {
			if strings.HasPrefix(requirement, "AC-") {
				n, err := strconv.Atoi(strings.TrimPrefix(requirement, "AC-"))
				if err != nil || n < 1 || n > 100 {
					t.Errorf("%s maps invalid requirement %q", row.ID, requirement)
					continue
				}
				key := fmt.Sprintf("AC-%03d", n)
				acs[key] = append(acs[key], row.ID)
			} else if !strings.HasPrefix(requirement, "§") {
				t.Errorf("%s requirement %q is neither AC-nnn nor a named section", row.ID, requirement)
			}
		}
	}
	for i := 1; i <= 87; i++ {
		id := fmt.Sprintf("T-%03d", i)
		if _, ok := cells[id]; !ok {
			t.Errorf("%s is absent from the normative matrix", id)
		}
	}
	for i := 1; i <= 100; i++ {
		id := fmt.Sprintf("AC-%03d", i)
		if len(acs[id]) == 0 {
			t.Errorf("%s maps to no behavioral cell", id)
		}
	}
	specRows := parseReparentSpecMatrix(t)
	for _, row := range reparentNormativeMatrix {
		want, ok := specRows[row.ID]
		if !ok {
			t.Errorf("%s is absent from the normative spec table", row.ID)
			continue
		}
		if !reflect.DeepEqual(row.Requirements, want) {
			t.Errorf("%s executable requirements = %v, spec requires exact %v", row.ID, row.Requirements, want)
		}
	}
}

func parseReparentSpecMatrix(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".tpatch", "features", "safe-reparent-restack", "spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	rowRE := regexp.MustCompile(`(?m)^\|\s*(T-[0-9]{3})\s*\|[^|]*\|\s*([^|]+?)\s*\|$`)
	rangeRE := regexp.MustCompile(`^AC-([0-9]{3})[–-](?:AC-)?([0-9]{3})$`)
	out := map[string][]string{}
	for _, match := range rowRE.FindAllStringSubmatch(string(data), -1) {
		id := match[1]
		if _, exists := out[id]; exists {
			t.Fatalf("spec matrix repeats %s", id)
		}
		for _, raw := range strings.Split(match[2], ",") {
			token := strings.Trim(strings.TrimSpace(raw), "`")
			if bounds := rangeRE.FindStringSubmatch(token); bounds != nil {
				start, _ := strconv.Atoi(bounds[1])
				end, _ := strconv.Atoi(bounds[2])
				for n := start; n <= end; n++ {
					out[id] = append(out[id], fmt.Sprintf("AC-%03d", n))
				}
				continue
			}
			if strings.HasPrefix(token, "§") {
				token = strings.Fields(token)[0]
			}
			out[id] = append(out[id], token)
		}
	}
	if len(out) != 87 {
		t.Fatalf("parsed %d normative spec rows, want 87", len(out))
	}
	return out
}

func TestReparentNormativeMatrixFunctionsExistAndAssert(t *testing.T) {
	type parsedFile struct {
		functions map[string]*ast.FuncDecl
	}
	cache := map[string]parsedFile{}
	type markerSet struct {
		facts      map[string][]string
		duplicates []string
	}
	behaviorMarkers := func(fn *ast.FuncDecl) markerSet {
		out := map[string][]string{}
		var duplicates []string
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "assertReparentMatrixBehavior" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			cell, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			var facts []string
			for _, arg := range call.Args[2:] {
				factLit, ok := arg.(*ast.BasicLit)
				if !ok || factLit.Kind != token.STRING {
					return true
				}
				fact, err := strconv.Unquote(factLit.Value)
				if err != nil {
					return true
				}
				facts = append(facts, fact)
			}
			if _, exists := out[cell]; exists {
				duplicates = append(duplicates, cell)
			} else {
				out[cell] = facts
			}
			return true
		})
		return markerSet{facts: out, duplicates: duplicates}
	}
	type ownerKey struct{ path, function string }
	expectedMarkers := map[ownerKey]map[string]bool{}
	for _, row := range reparentNormativeMatrix {
		key := ownerKey{filepath.Clean(filepath.Join(row.Dir, row.File)), row.Function}
		if expectedMarkers[key] == nil {
			expectedMarkers[key] = map[string]bool{}
		}
		expectedMarkers[key][row.ID] = true
	}
	checkedOwners := map[ownerKey]bool{}
	for _, row := range reparentNormativeMatrix {
		path := filepath.Clean(filepath.Join(row.Dir, row.File))
		parsed, ok := cache[path]
		if !ok {
			fset := token.NewFileSet()
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			parsed.functions = map[string]*ast.FuncDecl{}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					parsed.functions[fn.Name.Name] = fn
				}
			}
			cache[path] = parsed
		}
		fn := parsed.functions[row.Function]
		if fn == nil {
			t.Errorf("%s owner %s does not exist in %s", row.ID, row.Function, path)
			continue
		}
		want, ok := reparentMatrixExpectedFacts[row.ID]
		if !ok || len(want) == 0 {
			t.Errorf("%s has no canonical behavioral facts", row.ID)
			continue
		}
		markers := behaviorMarkers(fn)
		got := markers.facts[row.ID]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s owner %s in %s emitted facts %v, want exact canonical facts %v",
				row.ID, row.Function, path, got, want)
		}
		key := ownerKey{path, row.Function}
		if checkedOwners[key] {
			continue
		}
		checkedOwners[key] = true
		if len(markers.duplicates) > 0 {
			t.Errorf("%s in %s emits duplicate row marker(s) %v", row.Function, path, markers.duplicates)
		}
		gotIDs := make([]string, 0, len(markers.facts))
		for id := range markers.facts {
			gotIDs = append(gotIDs, id)
			if !expectedMarkers[key][id] {
				t.Errorf("%s in %s emits extra row marker %s", row.Function, path, id)
			}
		}
		sort.Strings(gotIDs)
		wantIDs := make([]string, 0, len(expectedMarkers[key]))
		for id := range expectedMarkers[key] {
			wantIDs = append(wantIDs, id)
			if _, ok := markers.facts[id]; !ok {
				t.Errorf("%s in %s is missing row marker %s", row.Function, path, id)
			}
		}
		sort.Strings(wantIDs)
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Errorf("%s marker ids = %v, want %v", row.Function, gotIDs, wantIDs)
		}
	}
}

func TestReparentNormativeMatrixFilesArePresent(t *testing.T) {
	var paths []string
	for _, row := range reparentNormativeMatrix {
		paths = append(paths, filepath.Clean(filepath.Join(row.Dir, row.File)))
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("matrix owner file %s is unavailable: %v", path, err)
		}
	}
}
