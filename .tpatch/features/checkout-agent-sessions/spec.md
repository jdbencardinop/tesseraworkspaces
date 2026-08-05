# Specification

## Acceptance Criteria

1. `tws open <feature> <name>` in checkout mode validates a clean attached checkout, no active sync transaction/lock, and no other live checkout agent session.
2. It resolves the logical stack entry, switches to `GitBranch()`, verifies HEAD/branch, and persists session ownership under `.tws/state/` before launching.
3. Direct mode runs/resumes the configured agent in the repo root, then offers the existing follow-up shell in the repo root; after that shell exits it restores the original branch and clears session state. Restoration failure retains state and prints recovery instructions.
4. Tmux mode creates a session namespaced with stable workspace ID and logical feature/name. The branch remains active while tmux owns it; `tws close` kills only that recorded session, restores the original branch, and clears state.
5. Stale session detection distinguishes dead direct PIDs and missing tmux sessions from live owners; recovery is explicit and never steals live ownership.
6. Feature inject/context is made available in a checkout-safe ignored location and tws cleans only links/files it created. Existing tracked files are never overwritten.
7. `--all`, multi-repo entries, dirty/detached repos, and concurrent branch sessions are rejected clearly.
8. Claude session continuation remains agent-aware; other configured agents launch without Claude flags.
9. External-mode open/close behavior and tests remain unchanged.

## Out of Scope

- Multiple simultaneous branch-owning agents.
- Checkout `open --all`.
- Automatic dirty-tree stashing.
- Multi-repo checkout mode.
- Copilot/Codex hook adapters.
