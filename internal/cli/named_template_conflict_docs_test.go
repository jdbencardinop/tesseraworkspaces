package cli

import (
	"strings"
	"testing"
)

func TestNamedTemplateConflictCandidateDocumentation(t *testing.T) {
	for _, surface := range []string{
		"README.md", "docs/cheatsheet.md", "docs/configuration.md", "CHANGELOG.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
	} {
		t.Run(surface, func(t *testing.T) {
			content := normalizeProse(readRepoDoc(t, surface))
			for _, claim := range []string{
				"--resolve-conflicts", "implementation candidate", "not released",
				"keep", "replace", "diff", "edit", "cancel", "terminal", "unmanaged",
				"two-way", "rendered", "code --wait", "applyto", "v2",
				"v1.2.18-rc.5 refuses v2", "workspace owner discovery",
				"--allow-managed-removal", "custom", "noncanonical",
			} {
				if !strings.Contains(content, claim) {
					t.Errorf("%s omits implemented conflict contract %q", surface, claim)
				}
			}

		})
	}
}

func TestNamedTemplateConflictInheritedBoundaryDocumentation(t *testing.T) {
	for _, surface := range []string{
		"README.md", "docs/cheatsheet.md", "docs/configuration.md", "CHANGELOG.md",
		"docs/roadmap.md", "docs/engineering-workflow.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
	} {
		t.Run(surface, func(t *testing.T) {
			content := normalizeProse(readRepoDoc(t, surface))
			for _, claim := range []string{
				"managed-state readers/writers", "not a universal disable switch",
				"tws new", "branch/stack metadata", "without reading the ledger",
				"template bytes and acknowledgements unchanged", "backpointers",
				"https://github.com/jdbencardinop/tesseraworkspaces/issues/5",
			} {
				if !strings.Contains(content, claim) {
					t.Errorf("%s must state the approved inherited boundary %q", surface, claim)
				}
			}
		})
	}
}
