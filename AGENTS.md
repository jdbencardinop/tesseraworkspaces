# Agent entrypoint

Read these files before making changes:

1. [`docs/engineering-workflow.md`](docs/engineering-workflow.md) — required implementation, review, test, tpatch land, and release process.
2. [`docs/roadmap.md`](docs/roadmap.md) — priorities, tool boundaries, and current workspace-mode program.
3. [`.tpatch/steering/local.md`](.tpatch/steering/local.md) — project-specific tpatch rules.
4. [`.claude/skills/tessera-patch/SKILL.md`](.claude/skills/tessera-patch/SKILL.md) — tpatch Path B schemas and commands.

## Core rules

- Preserve external multi-worktree behavior unless the feature explicitly changes it.
- Checkout mode is explicit, single-repository, and one physical checkout.
- Keep one logical tpatch feature per implementation boundary.
- Prefer Path B/manual artifacts; use `tpatch land` for one traceable feature commit.
- Run focused integration tests plus full tests, vet, golangci-lint, and build before landing.
- Use real temporary Git repos/remotes/worktrees for Git behavior tests.
- Do not push/tag until the landed commit is clean and approved.
