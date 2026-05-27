# Agent Hooks

tesseraworkspaces can install hooks into your coding agent so that decisions from sibling worktrees are automatically shown when you start or resume a session.

## Claude Code

### Install

```sh
# From inside the repo
tws hooks install auth          # install on all worktrees in the "auth" feature

# Or from inside a worktree (auto-detects feature)
cd ../myapp.tws/auth/worktrees/pr2
tws hooks install
```

This writes `.claude/settings.local.json` in each active worktree with:
- **SessionStart (startup)**: Shows unread decisions + ack instructions
- **SessionStart (resume)**: Shows unread decisions

### What you see

When Claude Code starts in a worktree:
```
#3 [breaking] Changed User.ID to uuid (pr2, 2026-05-27T...)
#4 [review] Review API surface (pr2 → pr3, 2026-05-27T...)
To acknowledge: tws decisions ack auth
```

### Remove

```sh
tws hooks remove auth
```

### How it works

The hook runs `tws decisions show <feature> --mine` on session start. Decisions are stored in the workspace (outside the repo), so they don't pollute git history. The `--mine` flag filters to only show decisions relevant to the current branch (broadcasts + targeted messages).

## No-arg commands

When running from inside a worktree, these commands auto-detect the feature:

```sh
tws decisions show          # no feature needed
tws decisions show --all    # show all including read
tws decisions ack           # mark as read for current branch
tws hooks install           # install hooks for auto-detected feature
```

## Other agents

Copilot CLI and Codex CLI hooks are planned. For now, you can:
- Use `tws decisions show` manually at the start of each session
- Add `tws decisions show --mine` to your project's `AGENTS.md` as a workflow instruction
