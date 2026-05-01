package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type StackEntry struct {
	Name string `yaml:"name"`
	Base string `yaml:"base"`
}

type Stack struct {
	Branches []StackEntry `yaml:"branches"`
}

func StackPath(featurePath string) string {
	return filepath.Join(featurePath, "stack.yaml")
}

func LoadStack(featurePath string) (Stack, error) {
	data, err := os.ReadFile(StackPath(featurePath))
	if err != nil {
		return Stack{}, err
	}
	var s Stack
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Stack{}, err
	}
	return s, nil
}

func SaveStack(featurePath string, s Stack) error {
	data, err := yaml.Marshal(&s)
	if err != nil {
		return err
	}
	return os.WriteFile(StackPath(featurePath), data, 0644)
}

// TopoSort returns branches in dependency order (parents before children).
// Returns an error if the graph contains a cycle.
func TopoSort(s Stack) ([]StackEntry, error) {
	// Build adjacency: base → children
	entryMap := make(map[string]StackEntry)
	children := make(map[string][]string)
	inDegree := make(map[string]int)

	for _, e := range s.Branches {
		entryMap[e.Name] = e
		inDegree[e.Name] = 0
	}

	for _, e := range s.Branches {
		// Only count edges where the base is also a tracked branch
		if _, ok := entryMap[e.Base]; ok {
			children[e.Base] = append(children[e.Base], e.Name)
			inDegree[e.Name]++
		}
	}

	// Kahn's algorithm
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	var sorted []StackEntry
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, entryMap[name])
		for _, child := range children[name] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if len(sorted) != len(s.Branches) {
		return nil, fmt.Errorf("cycle detected in stack.yaml")
	}
	return sorted, nil
}

// Descendants returns all transitive children of the given branch.
func Descendants(s Stack, branch string) map[string]bool {
	children := make(map[string][]string)
	for _, e := range s.Branches {
		children[e.Base] = append(children[e.Base], e.Name)
	}

	result := make(map[string]bool)
	queue := []string{branch}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, child := range children[name] {
			if !result[child] {
				result[child] = true
				queue = append(queue, child)
			}
		}
	}
	return result
}

// PrintTree prints the stack as an indented dependency tree.
func PrintTree(s Stack) {
	children := make(map[string][]string)
	roots := make(map[string]bool)

	for _, e := range s.Branches {
		roots[e.Name] = true
	}
	for _, e := range s.Branches {
		children[e.Base] = append(children[e.Base], e.Name)
		// If base is a tracked branch, this entry is not a root
		for _, other := range s.Branches {
			if other.Name == e.Base {
				delete(roots, e.Name)
				break
			}
		}
	}

	var printNode func(name, prefix string, isLast bool)
	printNode = func(name, prefix string, isLast bool) {
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		fmt.Println(prefix + connector + name)

		childPrefix := prefix
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}

		kids := children[name]
		for i, child := range kids {
			printNode(child, childPrefix, i == len(kids)-1)
		}
	}

	// Print from each root's base (usually "main")
	bases := make(map[string]bool)
	for _, e := range s.Branches {
		if roots[e.Name] {
			bases[e.Base] = true
		}
	}

	for base := range bases {
		fmt.Printf("(%s)\n", base)
		kids := children[base]
		for i, child := range kids {
			printNode(child, "", i == len(kids)-1)
		}
	}

	if len(s.Branches) == 0 {
		fmt.Println("No branches tracked. Use 'ts new <feature> <branch>' to add branches.")
	}
}

// FormatBranchStatus formats a branch sync result for display.
func FormatBranchStatus(name, status string) string {
	symbols := map[string]string{
		"synced":  "+",
		"failed":  "x",
		"skipped": "-",
	}
	sym := symbols[status]
	if sym == "" {
		sym = "?"
	}
	return fmt.Sprintf("  [%s] %s", sym, name)
}

// DescendantsList returns Descendants as a sorted display string.
func DescendantsList(s Stack, branch string) string {
	descs := Descendants(s, branch)
	var names []string
	for name := range descs {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
