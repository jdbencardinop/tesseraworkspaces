package internal

import (
	"bytes"
	"testing"
)

func TestManagedTemplateConflictLifecycleScopeRefusesWithoutAgentSelection(t *testing.T) {
	for _, lifecycle := range []string{"links-only", "retirement"} {
		t.Run(lifecycle, func(t *testing.T) {
			f := newManagedConflictFixture(t, nil, "", "")
			before := managedConflictRead(t, ManagedTemplateStatePath(f.path))
			request := f.request
			request.ResolveConflicts = true
			request.AgentSet = false
			request.Roles = []ManagedTemplateRole{ManagedTemplateRoleWorktree}
			if lifecycle == "links-only" {
				request.LinkMaterializeOnly = true
			} else {
				request.RetireWorktree = "nested/entry"
			}
			const refusal = "conflict resolution is only available for managed add/template sync"
			if plan, err := PlanManagedTemplate(request); err == nil || err.Error() != refusal || plan != nil {
				t.Fatalf("lifecycle resolver scope was not refused independently of --agent: plan=%+v err=%v", plan, err)
			}
			if !bytes.Equal(before, managedConflictRead(t, ManagedTemplateStatePath(f.path))) {
				t.Fatal("invalid lifecycle request changed the ownership ledger")
			}
		})
	}
}
