# Analysis: cross-worktree-agent-context

## Summary

Add a `decisions.yaml` file at the feature level in the workspace directory for agents to broadcast design decisions, breaking changes, and new APIs to sibling worktrees. The file lives alongside `stack.yaml` in the workspace (already outside the repo, no .gitignore needed).

## Data Model

```yaml
# ../myapp.tws/auth/decisions.yaml
entries:
  - id: 1
    branch: auth-models
    timestamp: "2026-05-10T10:30:00Z"
    type: breaking           # breaking | info | deprecation
    summary: "Changed User.ID from string to uuid.UUID"
    details: "All code referencing User.ID needs to import uuid package"
```

## Commands

- `tws decide <feature> "<summary>"` — add a decision from the current branch
- `tws decide <feature> "<summary>" --type breaking` — with type
- `tws decide <feature> "<summary>" --details "longer explanation"` — with details
- `tws decisions <feature>` — list all decisions
- `tws decisions <feature> --branch <name>` — filter by source branch

## Integration Points

- `tws open` — print count of decisions since last open
- Agent skills — instruct agents to read decisions at session start, write after breaking changes

## Affected Areas

- New: `internal/decisions.go` — Decision struct, LoadDecisions, SaveDecision
- New: `internal/cli/decide.go` — tws decide command
- New: `internal/cli/decisions.go` — tws decisions command
- `internal/cli/open.go` — show decision count on open
- `internal/cli/root.go` — register commands
- `assets/skills/` — update skill files with decisions workflow

## Acceptance Criteria

1. `tws decide` appends an entry to decisions.yaml with auto-incremented ID and timestamp
2. `tws decisions` lists entries in chronological order
3. `tws decisions --branch` filters by source branch
4. `tws open` prints "N new decisions" when opening a worktree
5. Skills instruct agents to check decisions before starting work
