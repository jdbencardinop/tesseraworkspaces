package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedTemplateParentSavedRegistryOriginDoesNotRebind(t *testing.T) {
	ws, root := managedTestCheckoutWorkspace(t)
	source := managedTestPackage(t, filepath.Join(root, "original"), map[string]string{
		"inject/context.md": "original registry\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "shared", source); err != nil {
		t.Fatal(err)
	}
	request := ManagedTemplatePlanRequest{
		Workspace: ws, Feature: "origin", FeaturePath: ws.FeaturePath("origin"),
		Roles:    []ManagedTemplateRole{ManagedTemplateRoleWorktree},
		Worktree: ManagedTemplateRoleRequest{Reference: "global:shared", ReferenceSet: true},
		Agent:    "claude", AgentSet: true,
	}
	plan, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedTemplatePlan(plan); err != nil {
		t.Fatal(err)
	}

	otherHome := filepath.Join(root, "other-home")
	if err := os.MkdirAll(otherHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", otherHome)
	otherSource := managedTestPackage(t, filepath.Join(root, "substitute"), map[string]string{
		"inject/context.md": "different registry\n",
	}, "")
	if err := AddNamedTemplate("", TemplateScopeGlobal, "shared", otherSource); err != nil {
		t.Fatal(err)
	}
	request.Worktree = ManagedTemplateRoleRequest{}
	request.Agent, request.AgentSet = "", false
	if refreshed, err := PlanManagedTemplate(request); err == nil {
		for _, write := range refreshed.Writes {
			if write.Relative == "context.md" && string(write.Data) != "original registry\n" {
				t.Error("ordinary sync rebound the saved global source to a different registry origin")
			}
		}
	}
	request.Worktree = ManagedTemplateRoleRequest{Reference: "global:shared", ReferenceSet: true}
	reselected, err := PlanManagedTemplate(request)
	if err != nil {
		t.Fatalf("explicit reselection of the new registry failed: %v", err)
	}
	found := false
	for _, write := range reselected.Writes {
		if write.Relative == "context.md" && string(write.Data) == "different registry\n" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit reselection did not use the newly selected registry")
	}
}

func TestNamedTemplateParentPayloadRootsMustBeDirectories(t *testing.T) {
	root, _ := managedTestEnvironment(t)
	for _, name := range []string{"feature", "inject"} {
		t.Run(name, func(t *testing.T) {
			source := managedTestPackage(t, filepath.Join(root, name), map[string]string{
				name: "not a directory\n",
			}, "")
			if _, err := LoadTemplatePackage(source); err == nil {
				t.Fatalf("package accepted and silently dropped regular-file payload root %q", name)
			}
		})
	}
}
