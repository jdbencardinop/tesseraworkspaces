package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type managedConflictFixture struct {
	ws      Workspace
	root    string
	path    string
	source  string
	request ManagedTemplatePlanRequest
}

func newManagedConflictFixture(t *testing.T, files map[string]string, agent, manifest string) managedConflictFixture {
	t.Helper()
	ws, root := managedTestCheckoutWorkspace(t)
	if files == nil {
		files = map[string]string{"inject/shared.md": "incoming\n", "feature/notes.md": "notes\n"}
	}
	if agent == "" {
		agent = "claude"
	}
	pkg := managedTestPackage(t, filepath.Join(root, "conflicts"), files, manifest)
	if err := AddNamedTemplate("", TemplateScopeGlobal, "conflicts", pkg); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveTemplateReference(ws.RepoRoot, "global:conflicts")
	if err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "conflict", FeaturePath: ws.FeaturePath("conflict"),
		Roles:        []ManagedTemplateRole{ManagedTemplateRoleWorktree, ManagedTemplateRoleOrchestrator},
		Worktree:     ManagedTemplateRoleRequest{Reference: "global:conflicts", ReferenceSet: true},
		Orchestrator: ManagedTemplateRoleRequest{Reference: "global:conflicts", ReferenceSet: true},
		Agent:        agent, AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}
	request.Worktree, request.Orchestrator = ManagedTemplateRoleRequest{}, ManagedTemplateRoleRequest{}
	request.AgentSet = false
	return managedConflictFixture{ws: ws, root: root, path: request.FeaturePath, source: resolved.Path, request: request}
}

func managedConflictWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
}

func managedConflictRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func managedConflictOwned(t *testing.T, state ManagedTemplateState, role ManagedTemplateRole, path string) ManagedTemplateOwnedFile {
	t.Helper()
	for _, file := range state.Roles[string(role)].Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("missing owned file %s/%s", role, path)
	return ManagedTemplateOwnedFile{}
}

func managedConflictApplyLocked(t *testing.T, plan *ManagedTemplatePlan) {
	t.Helper()
	lock, err := AcquireManagedTemplateLock(plan.Request.FeaturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	}()
	replanned, err := ReplanManagedTemplate(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedTemplateReplan(plan, replanned, lock); err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(replanned); err != nil {
		t.Fatal(err)
	}
}

func (f managedConflictFixture) keep(t *testing.T) *ManagedTemplatePlan {
	t.Helper()
	managedConflictWrite(t, filepath.Join(f.path, "inject", "shared.md"), []byte("local\n"))
	request := f.request
	request.ResolveConflicts = true
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", "keep", nil); err != nil {
		t.Fatal(err)
	}
	managedConflictApplyLocked(t, plan)
	return plan
}

func TestManagedTemplateConflictRememberedDecisionsAndReview(t *testing.T) {
	for _, action := range []string{"keep", "edit"} {
		t.Run(action, func(t *testing.T) {
			f := newManagedConflictFixture(t, nil, "", "")
			path := filepath.Join(f.path, "inject", "shared.md")
			ledger := ManagedTemplateStatePath(f.path)
			v1 := managedConflictRead(t, ledger)
			managedConflictWrite(t, path, []byte("local\n"))
			if _, err := PlanManagedTemplate(f.request); err == nil || !strings.Contains(err.Error(), "--resolve-conflicts") {
				t.Fatalf("default refusal did not recommend eligible resolution: %v", err)
			}
			request := f.request
			request.ResolveConflicts = true
			plan, err := PlanManagedTemplate(request)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.PendingConflicts()) != 1 {
				t.Fatalf("pending conflicts = %+v", plan.PendingConflicts())
			}
			if err := ApplyManagedTemplatePlan(plan); err == nil || !strings.Contains(err.Error(), "unresolved") {
				t.Fatalf("unresolved plan executed: %v", err)
			}
			if !bytes.Equal(v1, managedConflictRead(t, ledger)) {
				t.Fatal("discovery published ownership")
			}
			want := []byte("local\n")
			if action == "edit" {
				want = []byte("accepted edit\n")
			}
			if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", action, want); err != nil {
				t.Fatal(err)
			}
			if action == "keep" && len(plan.Writes) != 0 {
				t.Fatal("keep must not rewrite the owned source")
			}
			managedConflictApplyLocked(t, plan)
			state, err := LoadManagedTemplateState(f.path)
			if err != nil {
				t.Fatal(err)
			}
			file := managedConflictOwned(t, state, ManagedTemplateRoleWorktree, "shared.md")
			if state.Version != 2 || file.Override == nil || file.Override.Action != action ||
				file.Override.IncomingSHA256 != digestBytes([]byte("incoming\n")) || file.SHA256 != digestBytes(want) {
				t.Fatalf("effective/proposed ownership not separated: %+v %+v", state, file)
			}
			if !bytes.Equal(want, managedConflictRead(t, path)) {
				t.Fatal("locked replanning discarded the decision")
			}
			plan, err = PlanManagedTemplate(f.request)
			if err != nil || plan.HasEffects() || len(plan.PendingConflicts()) != 0 {
				t.Fatalf("ordinary repeat did not preserve override: plan=%+v err=%v", plan, err)
			}
			if err := ApplyManagedTemplatePlan(plan); err != nil {
				t.Fatal(err)
			}
			managedConflictWrite(t, filepath.Join(f.source, "feature", "unrelated.md"), []byte("unrelated\n"))
			plan, err = PlanManagedTemplate(f.request)
			if err != nil {
				t.Fatalf("unrelated package change invalidated override: %v", err)
			}
			managedConflictApplyLocked(t, plan)
			if !bytes.Equal(want, managedConflictRead(t, path)) {
				t.Fatal("unrelated update replaced customized content")
			}
			plan, err = PlanManagedTemplate(request)
			if err != nil || len(plan.PendingConflicts()) != 1 {
				t.Fatalf("explicit review omitted unchanged override: %v %+v", err, plan)
			}
			if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", "replace", nil); err != nil {
				t.Fatal(err)
			}
			managedConflictApplyLocked(t, plan)
			state, err = LoadManagedTemplateState(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if state.Version != 2 || managedConflictOwned(t, state, ManagedTemplateRoleWorktree, "shared.md").Override != nil {
				t.Fatal("replace did not clear the override or silently downgraded v2")
			}
			if got := string(managedConflictRead(t, path)); got != "incoming\n" {
				t.Fatalf("replace bytes = %q", got)
			}
		})
	}
}

func TestManagedTemplateConflictCanonicalOutcomesDoNotUpgradeV1(t *testing.T) {
	for _, action := range []string{"keep", "replace", "edit"} {
		t.Run(action, func(t *testing.T) {
			f := newManagedConflictFixture(t, nil, "", "")
			path := filepath.Join(f.path, "inject", "shared.md")
			managedConflictWrite(t, filepath.Join(f.source, "inject", "shared.md"), []byte("next\n"))
			local := []byte("local\n")
			if action != "edit" {
				local = []byte("next\n")
			}
			managedConflictWrite(t, path, local)
			if _, err := PlanManagedTemplate(f.request); err == nil {
				t.Fatal("unacknowledged local content equal to incoming bypassed consent")
			}
			request := f.request
			request.ResolveConflicts = true
			plan, err := PlanManagedTemplate(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", action, []byte("next\n")); err != nil {
				t.Fatal(err)
			}
			if action != "edit" && len(plan.Writes) != 0 {
				t.Fatal("already-equal choice rewrote the file")
			}
			managedConflictApplyLocked(t, plan)
			state, err := LoadManagedTemplateState(f.path)
			if err != nil || state.Version != 1 || managedConflictOwned(t, state, ManagedTemplateRoleWorktree, "shared.md").Override != nil {
				t.Fatalf("canonical decision incorrectly upgraded: %+v %v", state, err)
			}
		})
	}
}

func TestManagedTemplateConflictProposalAndLocalInvalidation(t *testing.T) {
	for _, change := range []string{"incoming-bytes", "incoming-mode", "local-edit", "local-equals-incoming", "source-selection", "renderer-selection"} {
		t.Run(change, func(t *testing.T) {
			f := newManagedConflictFixture(t, nil, "", "")
			f.keep(t)
			request := f.request
			switch change {
			case "incoming-bytes":
				managedConflictWrite(t, filepath.Join(f.source, "inject", "shared.md"), []byte("new incoming\n"))
			case "incoming-mode":
				if err := os.Chmod(filepath.Join(f.source, "inject", "shared.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "local-edit":
				managedConflictWrite(t, filepath.Join(f.path, "inject", "shared.md"), []byte("unacknowledged\n"))
			case "local-equals-incoming":
				managedConflictWrite(t, filepath.Join(f.path, "inject", "shared.md"), []byte("incoming\n"))
			case "source-selection":
				pkg := managedTestPackage(t, filepath.Join(f.root, "another"), map[string]string{"inject/shared.md": "incoming\n"}, "")
				if err := AddNamedTemplate("", TemplateScopeGlobal, "another", pkg); err != nil {
					t.Fatal(err)
				}
				request.Worktree = ManagedTemplateRoleRequest{Reference: "global:another", ReferenceSet: true}
			case "renderer-selection":
				request.Agent, request.AgentSet = "copilot", true
			}
			if _, err := PlanManagedTemplate(request); err == nil || !strings.Contains(err.Error(), "--resolve-conflicts") {
				t.Fatalf("changed proposal/local observation reused acknowledgement: %v", err)
			}
			request.ResolveConflicts = true
			plan, err := PlanManagedTemplate(request)
			if err != nil || len(plan.PendingConflicts()) != 1 {
				t.Fatalf("changed proposal not resolvable: %+v %v", plan, err)
			}
			if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", "keep", nil); err != nil {
				t.Fatal(err)
			}
			managedConflictApplyLocked(t, plan)
			request.ResolveConflicts = false
			plan, err = PlanManagedTemplate(request)
			if err != nil || plan.HasEffects() {
				t.Fatalf("new acknowledgement was not reusable: %+v %v", plan, err)
			}
		})
	}
}

func TestManagedTemplateConflictHardEligibilityAndDeletion(t *testing.T) {
	cases := []string{
		"local-nul", "local-invalid-utf8", "incoming-nul", "incoming-invalid-utf8",
		"permission", "special-permission", "symlink", "dangling-symlink", "directory", "parent-symlink",
		"unmanaged-equal", "source-missing", "source-symlink", "future-state", "corrupt-state",
		"missing-override", "obsolete-override", "missing-obsolete-override",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newManagedConflictFixture(t, nil, "", "")
			path := filepath.Join(f.path, "inject", "shared.md")
			sourcePath := filepath.Join(f.source, "inject", "shared.md")
			managedConflictWrite(t, path, []byte("local\n"))
			switch name {
			case "local-nul":
				managedConflictWrite(t, path, []byte("local\x00binary"))
			case "local-invalid-utf8":
				managedConflictWrite(t, path, []byte{0xff, 0xfe})
			case "incoming-nul":
				managedConflictWrite(t, sourcePath, []byte("incoming\x00binary"))
			case "incoming-invalid-utf8":
				managedConflictWrite(t, sourcePath, []byte{0xff})
			case "permission":
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "special-permission":
				if err := os.Chmod(path, 0o640|os.ModeSetuid); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling-symlink", "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if name == "directory" {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				} else {
					target := sourcePath
					if name == "dangling-symlink" {
						target += "-missing"
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			case "parent-symlink":
				inject := filepath.Join(f.path, "inject")
				if err := os.Rename(inject, inject+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(inject+"-saved", inject); err != nil {
					t.Fatal(err)
				}
			case "unmanaged-equal":
				managedConflictWrite(t, filepath.Join(f.source, "inject", "z-unmanaged.md"), []byte("same\n"))
				managedConflictWrite(t, filepath.Join(f.path, "inject", "z-unmanaged.md"), []byte("same\n"))
			case "source-missing":
				if err := os.Rename(f.source, f.source+"-saved"); err != nil {
					t.Fatal(err)
				}
			case "source-symlink":
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, sourcePath); err != nil {
					t.Fatal(err)
				}
			case "future-state":
				raw := managedConflictRead(t, ManagedTemplateStatePath(f.path))
				managedConflictWrite(t, ManagedTemplateStatePath(f.path), bytes.Replace(raw, []byte(`"version": 1`), []byte(`"version": 999`), 1))
			case "corrupt-state":
				managedConflictWrite(t, ManagedTemplateStatePath(f.path), []byte("{"))
			default:
				f.keep(t)
				if strings.Contains(name, "missing") {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				if strings.Contains(name, "obsolete") {
					if err := os.Remove(sourcePath); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := managedConflictRead(t, ManagedTemplateStatePath(f.path))
			request := f.request
			request.ResolveConflicts = true
			plan, err := PlanManagedTemplate(request)
			if err == nil || plan != nil {
				t.Fatalf("hard conflict became an executable/menu plan: %+v %v", plan, err)
			}
			if strings.Contains(err.Error(), "use --resolve-conflicts") {
				t.Fatalf("hard conflict recommended the resolver: %v", err)
			}
			if !bytes.Equal(before, managedConflictRead(t, ManagedTemplateStatePath(f.path))) {
				t.Fatal("hard preflight changed ledger")
			}
		})
	}
}

func TestManagedTemplateConflictBinaryCompatibilityAndEmptyText(t *testing.T) {
	for _, contents := range []string{"binary\x00data", string([]byte{0xff, 0xfe}), ""} {
		t.Run(digestBytes([]byte(contents))[:8], func(t *testing.T) {
			f := newManagedConflictFixture(t, map[string]string{"inject/shared.md": contents}, "", "")
			request := f.request
			request.ResolveConflicts = true
			updated := contents + "new"
			managedConflictWrite(t, filepath.Join(f.source, "inject", "shared.md"), []byte(updated))
			plan, err := PlanManagedTemplate(request)
			if err != nil || len(plan.PendingConflicts()) != 0 {
				t.Fatalf("canonical binary update changed compatibility: %+v %v", plan, err)
			}
			managedConflictApplyLocked(t, plan)
			if got := string(managedConflictRead(t, filepath.Join(f.path, "inject", "shared.md"))); got != updated {
				t.Fatalf("canonical update = %q", got)
			}
		})
	}
	f := newManagedConflictFixture(t, nil, "", "")
	path := filepath.Join(f.path, "inject", "shared.md")
	managedConflictWrite(t, path, nil)
	request := f.request
	request.ResolveConflicts = true
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", "keep", nil); err != nil {
		t.Fatalf("empty nonnative text was rejected: %v", err)
	}
	managedConflictApplyLocked(t, plan)
	if len(managedConflictRead(t, path)) != 0 {
		t.Fatal("empty override was rewritten")
	}
	if err := os.Remove(filepath.Join(f.path, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanManagedTemplate(f.request)
	if err != nil || len(plan.Writes) != 1 {
		t.Fatalf("ordinary missing file was not recreated: %+v %v", plan, err)
	}
}

func TestManagedTemplateConflictStrictMetadataAndCloning(t *testing.T) {
	f := newManagedConflictFixture(t, nil, "", "")
	f.keep(t)
	ledger := ManagedTemplateStatePath(f.path)
	valid := managedConflictRead(t, ledger)
	mutations := map[string]func(map[string]any, map[string]any, map[string]any){
		"v1-decision":        func(doc, _, _ map[string]any) { doc["version"] = float64(1) },
		"future-version":     func(doc, _, _ map[string]any) { doc["version"] = float64(3) },
		"unknown-top":        func(doc, _, _ map[string]any) { doc["unknown"] = true },
		"unknown-file":       func(_, file, _ map[string]any) { file["unknown"] = true },
		"null-override":      func(_, file, _ map[string]any) { file["override"] = nil },
		"unknown-override":   func(_, _, ack map[string]any) { ack["unknown"] = true },
		"wrong-role":         func(_, _, ack map[string]any) { ack["role"] = "orchestrator" },
		"wrong-path":         func(_, _, ack map[string]any) { ack["path"] = "CLAUDE.local.md" },
		"wrong-source":       func(_, _, ack map[string]any) { ack["source"] = map[string]any{"kind": "builtin"} },
		"wrong-renderer":     func(_, _, ack map[string]any) { ack["agent"] = "copilot" },
		"wrong-provenance":   func(_, _, ack map[string]any) { ack["agent_command_at_selection"] = "different" },
		"bad-action":         func(_, _, ack map[string]any) { ack["action"] = "replace" },
		"missing-action":     func(_, _, ack map[string]any) { delete(ack, "action") },
		"short-digest":       func(_, _, ack map[string]any) { ack["incoming_sha256"] = "abc" },
		"invalid-digest":     func(_, _, ack map[string]any) { ack["incoming_sha256"] = strings.Repeat("x", 64) },
		"uppercase-digest":   func(_, _, ack map[string]any) { ack["incoming_sha256"] = strings.Repeat("A", 64) },
		"missing-mode":       func(_, _, ack map[string]any) { delete(ack, "incoming_mode") },
		"null-mode":          func(_, _, ack map[string]any) { ack["incoming_mode"] = nil },
		"bad-mode":           func(_, _, ack map[string]any) { ack["incoming_mode"] = float64(0o1000) },
		"edit-wrong-mode":    func(_, _, ack map[string]any) { ack["action"], ack["incoming_mode"] = "edit", float64(0o777) },
		"canonical-override": func(_, file, ack map[string]any) { file["sha256"] = ack["incoming_sha256"] },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(valid, &document); err != nil {
				t.Fatal(err)
			}
			role := document["roles"].(map[string]any)["worktree"].(map[string]any)
			var owned map[string]any
			for _, item := range role["files"].([]any) {
				file := item.(map[string]any)
				if file["path"] == "shared.md" {
					owned = file
				}
			}
			mutate(document, owned, owned["override"].(map[string]any))
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			managedConflictWrite(t, ledger, data)
			if _, err := LoadManagedTemplateState(f.path); err == nil {
				t.Fatal("invalid ownership state was accepted")
			}
		})
	}
	managedConflictWrite(t, ledger, valid)
	state, err := LoadManagedTemplateState(f.path)
	if err != nil {
		t.Fatal(err)
	}
	clone := cloneManagedTemplateState(state)
	copyFile := managedConflictOwned(t, clone, ManagedTemplateRoleWorktree, "shared.md")
	copyFile.Override.Action = "edit"
	if managedConflictOwned(t, state, ManagedTemplateRoleWorktree, "shared.md").Override.Action != "keep" {
		t.Fatal("state clone aliases acknowledgements")
	}
	v1Null := bytes.Replace(valid, []byte(`"version": 2`), []byte(`"version": 1`), 1)
	var document map[string]any
	if err := json.Unmarshal(v1Null, &document); err != nil {
		t.Fatal(err)
	}
	role := document["roles"].(map[string]any)["worktree"].(map[string]any)
	for _, item := range role["files"].([]any) {
		item.(map[string]any)["override"] = nil
	}
	data, _ := json.Marshal(document)
	managedConflictWrite(t, ledger, data)
	if _, err := LoadManagedTemplateState(f.path); err == nil {
		t.Fatal("v1 carried hidden null v2 fields")
	}
}

func TestManagedTemplateConflictNativeRenderedProposalAndExactEdit(t *testing.T) {
	native := ".github/instructions/tws-worktree.instructions.md"
	f := newManagedConflictFixture(t, map[string]string{"inject/" + native: "Raw native body.\n"}, "copilot",
		"version: 1\nguidance:\n  worktree: Rendered manifest guidance.\nskills:\n  - name: review\n    roles: [worktree]\n    agents: [copilot]\n")
	path := filepath.Join(f.path, "inject", filepath.FromSlash(native))
	managedConflictWrite(t, path, []byte("Missing frontmatter\n"))
	request := f.request
	request.ResolveConflicts = true
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	conflict := plan.PendingConflicts()[0]
	for _, want := range []string{"applyTo:", "Raw native body.", "Rendered manifest guidance.", "`review`"} {
		if !strings.Contains(string(conflict.IncomingContent()), want) {
			t.Fatalf("conflict compared raw registry instead of rendered output: missing %q", want)
		}
	}
	if err := ResolveManagedTemplateConflict(plan, conflict.Role, conflict.Path, "keep", nil); err == nil {
		t.Fatal("invalid native keep accepted")
	}
	for _, invalid := range []string{"", "---\n---\n", "---\napplyTo: []\n---\n", "---\napplyTo: ''\n---\n", "---\napplyTo: '**'\n", "---\napplyTo: '**'\napplyTo: '*.go'\n---\n"} {
		if err := ValidateManagedTemplateConflictContent(conflict, []byte(invalid)); err == nil {
			t.Fatalf("invalid applyTo accepted: %q", invalid)
		}
	}
	accepted := []byte("---\r\napplyTo: \"**/*.go\"\r\n---\r\nExact custom bytes.\r\n")
	if err := ResolveManagedTemplateConflict(plan, conflict.Role, conflict.Path, "edit", accepted); err != nil {
		t.Fatal(err)
	}
	managedConflictApplyLocked(t, plan)
	if !bytes.Equal(accepted, managedConflictRead(t, path)) {
		t.Fatal("accepted native edit was silently normalized or rerendered")
	}
	if err := ValidateManagedCheckoutContext(f.ws, f.request.Feature, f.path, "."); err != nil {
		t.Fatalf("saved native override cannot be used as context: %v", err)
	}
}

func TestManagedTemplateConflictUnwrittenDriftPreservesRecovery(t *testing.T) {
	for _, choice := range []string{"keep", "replace"} {
		for _, checkpoint := range []string{"before-state", "after-state-preserve", "publish-intent", "before-cleanup", "cleanup-intent"} {
			t.Run(choice+"/"+checkpoint, func(t *testing.T) {
				f := newManagedConflictFixture(t, nil, "", "")
				path := filepath.Join(f.path, "inject", "shared.md")
				local := []byte("local\n")
				if choice == "replace" {
					local = []byte("next\n")
					managedConflictWrite(t, filepath.Join(f.source, "inject", "shared.md"), local)
				}
				managedConflictWrite(t, path, local)
				request := f.request
				request.ResolveConflicts = true
				plan, err := PlanManagedTemplate(request)
				if err != nil {
					t.Fatal(err)
				}
				if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", choice, nil); err != nil {
					t.Fatal(err)
				}
				if len(plan.Writes) != 0 || !plan.StateChanged {
					t.Fatalf("test requires a ledger-only decision: %+v", plan)
				}
				fired := false
				mutate := func() error {
					if fired {
						return nil
					}
					fired = true
					return os.WriteFile(path, []byte("concurrent user bytes\n"), 0o640)
				}
				oldStep, oldIntent := ManagedTemplateApplyStepHook, ManagedTemplateIntentDurableHook
				t.Cleanup(func() { ManagedTemplateApplyStepHook, ManagedTemplateIntentDurableHook = oldStep, oldIntent })
				ManagedTemplateApplyStepHook = func(step, _ string) error {
					if step == checkpoint {
						return mutate()
					}
					return nil
				}
				ManagedTemplateIntentDurableHook = func(effect, original, _, _ string) error {
					if filepath.Base(original) == managedTemplateStateName &&
						((checkpoint == "publish-intent" && effect == "publish-active") ||
							(checkpoint == "cleanup-intent" && effect == "cleanup-private")) {
						return mutate()
					}
					return nil
				}
				err = ApplyManagedTemplatePlan(plan)
				if !fired || err == nil || !strings.Contains(err.Error(), "preserved pending evidence") {
					t.Fatalf("unwritten %s drift was not preserved at %s: fired=%v err=%v", choice, checkpoint, fired, err)
				}
				if got := string(managedConflictRead(t, path)); got != "concurrent user bytes\n" {
					t.Fatalf("concurrent unwritten bytes were lost: %q", got)
				}
				if present, err := ManagedTemplateStatePresent(f.path); err == nil || present {
					t.Fatalf("pending recovery did not block context/legacy dispatch: %v %v", present, err)
				}
			})
		}
	}
}

func TestManagedTemplateConflictReplanEvidenceAndRoleScope(t *testing.T) {
	for _, change := range []string{"local-content", "local-mode", "local-inode", "ledger-content", "ledger-inode", "source-content", "source-root", "feature-root", "parent-root"} {
		t.Run(change, func(t *testing.T) {
			f := newManagedConflictFixture(t, nil, "", "")
			path := filepath.Join(f.path, "inject", "shared.md")
			managedConflictWrite(t, path, []byte("local\n"))
			request := f.request
			request.ResolveConflicts = true
			plan, err := PlanManagedTemplate(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleWorktree, "shared.md", "keep", nil); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "local-content":
				managedConflictWrite(t, path, []byte("later\n"))
			case "local-mode":
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "local-inode", "ledger-inode":
				if change == "ledger-inode" {
					path = ManagedTemplateStatePath(f.path)
				}
				data := managedConflictRead(t, path)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
			case "ledger-content":
				path = ManagedTemplateStatePath(f.path)
				managedConflictWrite(t, path, append(managedConflictRead(t, path), '\n'))
			case "source-content":
				managedConflictWrite(t, filepath.Join(f.source, "inject", "shared.md"), []byte("later incoming\n"))
			case "source-root", "feature-root", "parent-root":
				root := f.source
				switch change {
				case "feature-root":
					root = f.path
				case "parent-root":
					root = filepath.Join(f.path, "inject")
				}
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(root, os.DirFS(root+"-old")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReplanManagedTemplate(plan); err == nil {
				t.Fatal("evidence-bound decision survived drift")
			}
		})
	}
	f := newManagedConflictFixture(t, nil, "", "")
	f.keep(t)
	before, err := LoadManagedTemplateState(f.path)
	if err != nil {
		t.Fatal(err)
	}
	request := f.request
	request.Roles = []ManagedTemplateRole{ManagedTemplateRoleOrchestrator}
	request.ResolveConflicts = true
	managedConflictWrite(t, filepath.Join(f.path, "notes.md"), []byte("orchestrator local\n"))
	plan, err := PlanManagedTemplate(request)
	if err != nil || len(plan.PendingConflicts()) != 1 || plan.PendingConflicts()[0].Role != ManagedTemplateRoleOrchestrator {
		t.Fatalf("scope included unselected worktree override: %+v %v", plan, err)
	}
	if err := ResolveManagedTemplateConflict(plan, ManagedTemplateRoleOrchestrator, "notes.md", "keep", nil); err != nil {
		t.Fatal(err)
	}
	managedConflictApplyLocked(t, plan)
	after, err := LoadManagedTemplateState(f.path)
	if err != nil {
		t.Fatal(err)
	}
	beforeWorktree, _ := json.Marshal(before.Roles["worktree"])
	afterWorktree, _ := json.Marshal(after.Roles["worktree"])
	if !bytes.Equal(beforeWorktree, afterWorktree) {
		t.Fatal("orchestrator decision changed unselected role metadata")
	}
	if err := ValidateManagedOrchestratorContext(f.ws, f.request.Feature, f.path, "claude"); err != nil {
		t.Fatal(err)
	}
	managedConflictWrite(t, filepath.Join(f.path, "notes.md"), []byte("unacknowledged orchestrator\n"))
	if err := ValidateManagedOrchestratorContext(f.ws, f.request.Feature, f.path, "claude"); err == nil {
		t.Fatal("nonresolver context caller accepted an unacknowledged edit")
	}
	if _, err := os.Lstat(filepath.Join(f.path, managedTemplateRecoveryName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("successful decisions left recovery evidence: %v", err)
	}
}
