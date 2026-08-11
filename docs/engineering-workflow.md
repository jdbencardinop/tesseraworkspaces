# Engineering workflow

This document is the durable implementation contract for human and coding-agent work on tesseraworkspaces.

## Current product direction

`tws` supports two explicit workspace modes:

- **external** — existing sibling workspace with linked Git worktrees; default and backward compatible.
- **checkout** — repository-local `.tws/features/` metadata and one physical Git checkout.

Current shipped checkout slices:

1. workspace mode foundation;
2. lifecycle and logical branches;
3. transactional stack sync;
4. namespaced feature layout with legacy migration;
5. single-owner direct/tmux agent sessions;
6. checkout doctor and list observability;
7. workspace sibling links (`<spaces-root>/spaces.yaml`, `tws space add/list/show/remove`).

The opt-in global workspace registry is also shipped for stable cross-repository
discovery, health checks, and moved-target repair.

Next roadmap feature: **agent work status** — surface materialized sessions,
idle agents, blocked approvals, and attention needs without replacing the agent
harness. See [`roadmap.md`](roadmap.md).

## Tpatch workflow

Use tpatch as structured patch management, not primarily as a generator.

1. Check `tpatch status <slug>` and `tpatch next <slug>`.
2. Register with an explicit clean slug.
3. Add hard/soft dependencies and run `tpatch feature deps --validate-all`.
4. Author concise Path B artifacts:
   - `analysis.md`
   - `spec.md`
   - `exploration.md`
5. Advance phases with `--manual` — the coding agent is the provider.
6. Mark implementation started.
7. Implement and test without committing.
8. Preview with `tpatch land <slug> --dry-run`.
9. Land with `tpatch land <slug>` for one commit with feature trailers.
10. Run `tpatch verify <slug>` and record known historical verifier limitations honestly.

Do not batch unrelated features into one record range. Override collisions only with a precise understood reason.

## Implementation and review loop

For each feature:

1. Dispatch one isolated Opus implementer with exact scope, files, tests, compatibility rules, and non-goals.
2. Parent agent audits the returned diff before importing it.
3. Run focused and full gates.
4. Dispatch three isolated Opus reviewers:
   - code quality/security;
   - architecture/correctness/tests;
   - CLI UX/discoverability.
5. Consolidate only blocking findings; non-blocking observations may become backlog items.
6. Dispatch a revision implementer with exact findings.
7. Repeat reviews until all blockers are approved.
8. Run final gates, land, push, and tag the next patch version.

Review agents never commit, push, tag, or modify unrelated files.

## Required gates

Before landing:

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Also run feature-specific CLI smoke tests. Git behavior requires real temporary repositories and local bare remotes; do not rely only on mocks.

After landing, rerun full gates and check a clean tree before push/tag.

## Coding conventions

- Prefer error-returning helpers and Cobra `RunE`; avoid `os.Exit` or fatal helpers inside reusable logic.
- Validate all system boundaries before mutation.
- Persist transaction/session state atomically before irreversible Git actions.
- Metadata changes happen only after Git success, with rollback where practical.
- Never use `git reset --hard`, force push, or destructive cleanup as a shortcut.
- Use `--force-with-lease` for rewritten branches.
- Keep external and checkout mode dispatch explicit; do not infer checkout mode merely from `.tws/` existence.
- Preserve external-mode paths and behavior with regression tests.
- In checkout mode, reject dirty/detached/concurrent operations instead of auto-stashing.
- Use `StackEntry.Name` for tws identity and `StackEntry.GitBranch()` for Git operations.
- Use repo-scoped base resolution. Explicit refs are literal; omitted base uses selected repo `origin/HEAD`.
- Agent skills/documentation must be updated when user-facing behavior changes.

## Release cadence

- Each approved landed feature gets the next patch tag unless it changes incompatible storage/semantics; use an RC in that case.
- Tag only after push-ready clean-tree audit and full gates.
- Verify the built binary reports the exact tag.
- Watch GitHub Actions for main and tag runs before starting dependent work when CI behavior changed.

## Known tpatch metadata maintenance

Some old bundled features have canonical patches that fail dependency-closure verification while recipes replay and product tests pass. This is tracked by `repair-historical-tpatch-artifacts` and intentionally deferred until tpatch is upgraded.

Do not distort dependencies merely to silence verification. Repair topologically and preserve append-only history.
