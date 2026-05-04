# Analysis: skill-distribution

## Summary

Add `tws init` to install agent skill files into the current project, teaching coding agents how to use tws commands. Embeds skill content in the Go binary via `go:embed`. Supports Claude Code and GitHub Copilot initially.

## Target Locations

- **Claude Code**: `.claude/skills/tesseraworkspaces/SKILL.md` (frontmatter: name, description)
- **GitHub Copilot**: `.github/prompts/tws.prompt.md` (frontmatter: mode, description, tools)

## Implementation

1. **`assets/skills/claude/tesseraworkspaces/SKILL.md`** — embedded skill teaching Claude how to use tws
2. **`assets/skills/copilot/tws.prompt.md`** — embedded prompt for Copilot agent mode
3. **`internal/skills/embed.go`** — `go:embed` directives for skill files
4. **`internal/cli/init.go`** — `Init()` function: copy embedded skills to project dirs
5. **`cmd/tws/main.go`** — register `init` subcommand

## Acceptance Criteria

1. `tws init` installs skills for all supported agents
2. `tws init --agent claude` installs only Claude skills
3. `tws init --agent copilot` installs only Copilot skills
4. Doesn't overwrite existing files without `--force`
5. Skills accurately describe tws commands, stack.yaml format, and workflow
