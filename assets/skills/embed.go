package skills

import _ "embed"

//go:embed claude/tesseraworkspaces/SKILL.md
var ClaudeSkill []byte

//go:embed copilot/tws.prompt.md
var CopilotSkill []byte
