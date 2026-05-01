package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTopoSort_LinearChain(t *testing.T) {
	s := Stack{Branches: []StackEntry{
		{Name: "auth-routes", Base: "auth-middleware"},
		{Name: "auth-models", Base: "main"},
		{Name: "auth-middleware", Base: "auth-models"},
	}}

	sorted, err := TopoSort(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// auth-models must come before auth-middleware, which must come before auth-routes
	indexOf := func(name string) int {
		for i, e := range sorted {
			if e.Name == name {
				return i
			}
		}
		return -1
	}

	if indexOf("auth-models") > indexOf("auth-middleware") {
		t.Error("auth-models should come before auth-middleware")
	}
	if indexOf("auth-middleware") > indexOf("auth-routes") {
		t.Error("auth-middleware should come before auth-routes")
	}
}

func TestTopoSort_DAG(t *testing.T) {
	// A→B and A→C (divergent)
	s := Stack{Branches: []StackEntry{
		{Name: "branch-b", Base: "branch-a"},
		{Name: "branch-c", Base: "branch-a"},
		{Name: "branch-a", Base: "main"},
	}}

	sorted, err := TopoSort(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sorted) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(sorted))
	}

	// branch-a must come before both b and c
	indexOf := func(name string) int {
		for i, e := range sorted {
			if e.Name == name {
				return i
			}
		}
		return -1
	}

	if indexOf("branch-a") > indexOf("branch-b") {
		t.Error("branch-a should come before branch-b")
	}
	if indexOf("branch-a") > indexOf("branch-c") {
		t.Error("branch-a should come before branch-c")
	}
}

func TestTopoSort_CycleDetection(t *testing.T) {
	s := Stack{Branches: []StackEntry{
		{Name: "a", Base: "b"},
		{Name: "b", Base: "c"},
		{Name: "c", Base: "a"},
	}}

	_, err := TopoSort(s)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestTopoSort_SingleBranch(t *testing.T) {
	s := Stack{Branches: []StackEntry{
		{Name: "feature", Base: "main"},
	}}

	sorted, err := TopoSort(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 1 || sorted[0].Name != "feature" {
		t.Errorf("expected [feature], got %v", sorted)
	}
}

func TestTopoSort_Empty(t *testing.T) {
	s := Stack{}

	sorted, err := TopoSort(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 0 {
		t.Errorf("expected empty, got %v", sorted)
	}
}

func TestDescendants(t *testing.T) {
	s := Stack{Branches: []StackEntry{
		{Name: "a", Base: "main"},
		{Name: "b", Base: "a"},
		{Name: "c", Base: "b"},
		{Name: "d", Base: "a"}, // divergent from b
	}}

	descs := Descendants(s, "a")
	if !descs["b"] || !descs["c"] || !descs["d"] {
		t.Errorf("expected b, c, d as descendants of a, got %v", descs)
	}
	if descs["a"] {
		t.Error("a should not be its own descendant")
	}

	descsB := Descendants(s, "b")
	if !descsB["c"] {
		t.Error("c should be descendant of b")
	}
	if descsB["d"] {
		t.Error("d should not be descendant of b")
	}
}

func TestLoadSaveStack(t *testing.T) {
	tmp := t.TempDir()

	original := Stack{Branches: []StackEntry{
		{Name: "auth-models", Base: "main"},
		{Name: "auth-middleware", Base: "auth-models"},
	}}

	if err := SaveStack(tmp, original); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmp, "stack.yaml")); err != nil {
		t.Fatalf("stack.yaml not created: %v", err)
	}

	loaded, err := LoadStack(tmp)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if len(loaded.Branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(loaded.Branches))
	}
	if loaded.Branches[0].Name != "auth-models" || loaded.Branches[0].Base != "main" {
		t.Errorf("unexpected first entry: %v", loaded.Branches[0])
	}
}

func TestLoadStack_MissingFile(t *testing.T) {
	_, err := LoadStack("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
