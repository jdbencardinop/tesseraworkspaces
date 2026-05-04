package cli

import (
	"fmt"
	"os"
	"path/filepath"

	skills "github.com/jdbencardinop/tesseraworkspaces/assets/skills"
)

func Init(args []string) {
	agent := ""
	force := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 < len(args) {
				agent = args[i+1]
				i++
			}
		case "--force":
			force = true
		}
	}

	installed := 0

	if agent == "" || agent == "claude" {
		if installFile(".claude/skills/tesseraworkspaces/SKILL.md", skills.ClaudeSkill, force) {
			installed++
		}
	}

	if agent == "" || agent == "copilot" {
		if installFile(".github/prompts/tws.prompt.md", skills.CopilotSkill, force) {
			installed++
		}
	}

	if agent != "" && agent != "claude" && agent != "copilot" {
		fmt.Printf("Unknown agent: %s (supported: claude, copilot)\n", agent)
		os.Exit(1)
	}

	if installed == 0 {
		fmt.Println("No files installed (already exist, use --force to overwrite)")
	} else {
		fmt.Printf("Installed %d skill file(s)\n", installed)
	}
}

func installFile(relPath string, content []byte, force bool) bool {
	if _, err := os.Stat(relPath); err == nil && !force {
		fmt.Printf("  skip: %s (exists, use --force)\n", relPath)
		return false
	}

	dir := filepath.Dir(relPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("  error: could not create %s: %v\n", dir, err)
		return false
	}

	if err := os.WriteFile(relPath, content, 0644); err != nil {
		fmt.Printf("  error: could not write %s: %v\n", relPath, err)
		return false
	}

	fmt.Printf("  installed: %s\n", relPath)
	return true
}
