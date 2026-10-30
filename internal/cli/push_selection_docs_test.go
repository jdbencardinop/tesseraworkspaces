package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestScopedPushHelpAndGuidance(t *testing.T) {
	cmd := pushCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, guidance := range []string{"--only", "--from", "--continue", "--cancel", "--dry-run", "local-only", "no remote rollback", "hooks remain enabled"} {
		if !strings.Contains(out.String(), guidance) {
			t.Errorf("push help omits %q", guidance)
		}
	}
	if cmd.Flags().Lookup("abort") != nil {
		t.Fatal("publication acquired a misleading remote-rollback abort verb")
	}
	for _, path := range []string{
		"README.md", "docs/cheatsheet.md", "CHANGELOG.md",
		"assets/skills/claude/tesseraworkspaces/SKILL.md",
		"assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md",
		"assets/skills/copilot/tws.prompt.md",
	} {
		t.Run(path, func(t *testing.T) {
			text := normalizeProse(readRepoDoc(t, path))
			for _, guidance := range []string{"--only", "--from", "--continue", "--cancel", "unverified", "inclusion", "released in v1.2.18-rc.8", "terminal receipt"} {
				if !strings.Contains(text, guidance) {
					t.Errorf("%s omits standalone publication guidance %q", path, guidance)
				}
			}
		})
	}
}
