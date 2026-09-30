package internal

import "testing"

func frozenCutoffRegressionFixture(t *testing.T) (*SyncTransaction, Stack, cutoffFixture) {
	t.Helper()
	f := newCutoffFixture(t)
	feature := t.TempDir()
	stack := Stack{Branches: []StackEntry{
		{Name: "new-parent", Branch: "new-parent", Base: "main"},
		{Name: "child", Branch: "current-child", Base: "new-parent", LastBaseSHA: f.newParent},
	}}
	if err := SaveStack(feature, stack); err != nil {
		t.Fatal(err)
	}
	tx, err := CaptureSyncTransaction(SyncTransactionBeginInput{
		FeaturePath: feature, Feature: "feature", Mode: ModeExternal,
		WorkspaceRepoRoot: f.repo, Stack: stack, Selected: []string{"child"},
		EntryRepoDirs: map[string]string{"child": f.repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := FreezeSyncCutoffs(tx, stack, []SyncCutoffFreezeInput{{
		Entry: "child", RepoDir: f.repo, ParentRef: "new-parent",
		ParentSHA: f.newParent, Applicable: true,
	}}, nil); err != nil {
		t.Fatal(err)
	}
	return tx, stack, f
}

func TestSyncCutoffEvidenceMustBindCapturedChild(t *testing.T) {
	tx, _, f := frozenCutoffRegressionFixture(t)
	tx.Cutoffs[0].ChildSHA = f.newParent
	if err := ValidateSyncTransaction(tx, "feature", ModeExternal); err == nil {
		t.Error("validator accepted a cutoff child different from the captured ref preimage")
	}
	if err := RevalidateSyncCutoffs(tx); err == nil {
		t.Error("revalidation accepted a cutoff child different from the captured ref preimage")
	}
}

func TestSyncCutoffEvidenceRejectsContradictoryCrossFields(t *testing.T) {
	mutations := map[string]func(*SyncCutoffDecision){
		"git-branch":      func(d *SyncCutoffDecision) { d.GitBranch = "other" },
		"child-ref":       func(d *SyncCutoffDecision) { d.ChildRef = "refs/heads/other" },
		"raw-record":      func(d *SyncCutoffDecision) { d.Recorded = "" },
		"configured-base": func(d *SyncCutoffDecision) { d.ConfiguredBase = "other-parent" },
		"parent-entry":    func(d *SyncCutoffDecision) { d.ParentEntry = "" },
		"parent-ref":      func(d *SyncCutoffDecision) { d.ParentRef = "other-parent" },
		"applicability":   func(d *SyncCutoffDecision) { d.Applicable = false },
		"repository":      func(d *SyncCutoffDecision) { d.RepoCommonDir += "-other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			tx, _, _ := frozenCutoffRegressionFixture(t)
			mutate(&tx.Cutoffs[0])
			if err := ValidateSyncTransaction(tx, "feature", ModeExternal); err == nil {
				t.Fatal("validator accepted contradictory cutoff evidence")
			}
			if err := RevalidateSyncCutoffs(tx); err == nil {
				t.Fatal("revalidation accepted contradictory cutoff evidence")
			}
		})
	}
}
