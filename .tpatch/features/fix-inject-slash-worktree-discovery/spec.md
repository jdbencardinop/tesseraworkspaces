# Specification

## Problem

`tws inject <feature>` enumerates only immediate directories beneath
`worktrees/`. Slash-containing logical names therefore cause injection into an
intermediate directory instead of the materialized Git worktree. The same
feature-wide helper is reused by template synchronization and external import.
Checkout feature-wide injection also returns a successful zero count even
though linked worktrees are unsupported.

## Acceptance criteria

1. In an external feature containing both `plain` and
   `review/team/pr-123`, feature-wide injection discovers exactly those two
   materialized Git worktree roots in deterministic logical-name order.
2. The nested logical name may map to a decoupled Git branch such as
   `feat-review/pr-123`; discovery never uses Git branch spelling to construct
   the path.
3. No injected file or configured target directory is created in
   `worktrees/review`, `worktrees/review/team`, or any other intermediate
   directory.
4. Each injected file is a relative symlink to the exact source under
   `<feature>/inject`; nested source paths retain their layout and content.
5. Existing destination files or symlinks remain unchanged. Explicit
   `--into` and configured `inject_into` retain existing behavior.
6. `stack.yaml`, when present and readable, is authoritative:
   - archived entries are skipped;
   - missing or pruned active worktrees are skipped and not counted;
   - unsafe logical names, duplicate destinations, symlink escapes, and
     existing non-Git destinations fail with contextual errors;
   - malformed, unreadable, symlinked, or non-regular metadata fails without
     recursive fallback; only a genuinely absent `stack.yaml` directory entry
     enables compatibility discovery.
7. When `stack.yaml` is absent, recursive compatibility discovery accepts only
   verified linked Git worktree roots beneath the selected `worktrees/` root,
   does not follow symlinks, stops at each accepted root, and ignores
   intermediate or arbitrary directories.
   Verification binds the exact selected directory to both its Git admin
   `gitdir` backpointer and a byte-safe registered worktree inventory; copied,
   stale, or mismatched `.git` markers fail before any target is written.
8. Branch-specific injection validates the same destination identity and
   scope as feature-wide injection while preserving its existing not-found
   message.
9. `tws inject <feature>` and `tws inject <feature> <branch>` both return the
   existing checkout unsupported error before any write or success-shaped
   output. Feature-path ambiguity and registered sibling-space exclusion keep
   precedence.
10. Feature selection is anchored to the feature path returned by the guarded
    resolver. Discovery does not re-resolve through `TWS_ROOT`, cwd, or a
    different `Workspace.MetadataRoot`.
11. Feature-directory, nested-cwd, and external multi-repository operation
    continue to work.
12. Template synchronization and external import use the corrected shared
    discovery and surface its errors through their existing warning/error
    conventions.
13. Directly related CLI help, README, cheatsheet, embedded agent skills, and
    roadmap and changelog text document nested external discovery and explicit
    checkout refusal.
14. The implementer candidate passes focused verification:

    ```sh
    go test ./internal ./internal/cli -count=1 -run 'Inject|ResolveFeature|RequireWorktree|ExternalFeatureDirectory|SpaceGuard|Template|Import'
    go vet ./internal ./internal/cli
    golangci-lint run ./internal/...
    make build
    git diff --check
    tpatch feature deps --validate-all
    ```
15. Before landing, the parent delivery workflow still requires the complete
    repository gates:

    ```sh
    go test ./... -count=1
    go vet ./...
    golangci-lint run ./...
    make build
    git diff --check
    tpatch feature deps --validate-all
    ```

## Implementation plan

1. Add safe, deterministic worktree target discovery and destination
   verification in `internal/inject.go`.
2. Route branch-specific and feature-wide CLI injection through the same
   verified target boundary and explicitly reject checkout mode in
   `internal/cli/inject.go`.
3. Propagate shared helper errors from `internal/cli/template.go` and
   `internal/cli/importcmd.go` without redesigning either workflow.
4. Add real temporary repository/remote/worktree tests in focused internal and
   CLI test files, including metadata, fallback, safety, root-selection,
   template/import, and checkout cases.
5. Update the directly related user documentation and advance the roadmap
   target from released issue #4 to issue #1 while preserving the approved
   `#4, #1, #3, #2` order and Lane B material.

## Out of scope

- Checkout injection, branch switching, fake checkout worktrees, or any new
  checkout injection semantics.
- Issue #2 named templates or issue #3 scoped status projection.
- Sync, reparent, session, mailbox, Lane B, release, publication, or lifecycle
  completion work.
- A final tpatch recipe, `apply --mode done`, record, land, commit, push, tag,
  or release.
