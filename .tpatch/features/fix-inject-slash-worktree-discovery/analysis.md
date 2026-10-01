# Analysis

## Summary

GitHub issue #1 is present in released `v1.2.18-rc.2`
(`03cdd3d3a3be4defe9017372f5f7fcbcbf2176b1`). Feature-wide injection reads
only the immediate children of `<feature>/worktrees`, so a logical entry such
as `review/pr-123` is misidentified as the intermediate directory `review`.
The released reproduction records a false count, writes into that intermediate
directory, and misses the actual nested worktree. Branch-specific injection
finds the nested path because it joins the complete logical name.

The fix is compatible with external workspaces and the existing relative
symlink, existing-file skip, and `inject_into` behavior. Checkout mode has no
linked worktrees; by explicit product policy both feature-wide and
branch-specific `tws inject` must return `ErrWorktreeUnsupported` without
writing files or printing a success-shaped count. Ambiguous feature layouts
and registered sibling-space guards must still fail before that mode refusal.

## Compatibility and boundaries

- Keep `stack.yaml` as the authoritative list of logical entries when it is
  present. Resolve each active logical name beneath the already selected
  feature's `worktrees/` root; never derive a target from its first segment,
  Git branch spelling, process cwd, or a newly resolved workspace root.
- Logical names and Git branch names remain decoupled. Discovery uses
  `StackEntry.Name`, not `StackEntry.GitBranch()`.
- Skip archived entries and active entries whose worktree directory is absent
  or already pruned. They are not counted as successful injections.
- Reject unsafe logical paths, symlink escapes, duplicate targets, and
  existing non-Git destinations. Verify every destination is exactly a Git
  worktree root before writing by binding its marker to the Git admin
  backpointer and an exact registered worktree inventory entry.
- If `stack.yaml` is absent, retain legacy compatibility with a deterministic
  recursive scan for verified linked-worktree `.git` files. Do not follow
  symlink directories, do not accept arbitrary `.git` directories as fallback
  worktrees, and stop descending once a worktree root is found so injected
  nested repositories cannot become additional targets.
- A malformed, unreadable, or otherwise corrupt `stack.yaml` is a hard error;
  it must not activate permissive recursive fallback.
- Discovery and per-target failures must be returned with target context.
  Shared template and external-import callers must report rather than discard
  those errors.
- Preserve feature-directory and nested-cwd operation, external multi-repo
  worktrees, exact relative symlinks, existing destinations, and configured or
  explicit `--into`.
- Do not change sync, reparent, session, checkout switching, template
  semantics, import semantics, or any issue #2/#3 behavior.

## Risks

- Recursive compatibility discovery could otherwise select an unrelated
  nested repository. Restricting fallback to verified linked-worktree marker
  files and pruning traversal at each accepted root closes that path.
- A lexical containment check alone is insufficient because path components
  may be symlinks. Destination verification must compare canonical paths and
  reject symlinked targets before mutation.
- A `.git` pointer and successful `rev-parse` are not registration evidence:
  copying a real child marker to an intermediate directory makes Git trust the
  copied location. The admin `gitdir` backpointer and NUL-delimited worktree
  inventory must both bind to the selected directory.
- Git pathname output may contain meaningful trailing spaces, tabs, or embedded
  newlines. Strip only the protocol terminator and keep stderr separate from
  successful stdout.
- Partial success can hide invalid metadata or corrupt targets. The helper may
  report completed targets, but any discovery or target failure remains an
  error and the CLI must not print an unconditional success count.
- Existing tests run with inherited Git configuration scrubbed. New real-Git
  fixtures must use a private `GIT_CONFIG_GLOBAL`, disable system config, and
  pass explicit author/committer identity to every commit subprocess.

## Evidence

- Released reproduction:
  `files/inject-slash-released-rc2-reproduction.json`.
- Issue #4 release verification:
  `files/sync-cutoff-release-verified.json` confirms `v1.2.18-rc.2`, main and
  tag CI, exact binary version, and a clean released tree.
- Hard dependencies `worktree-context-injection` and
  `branch-name-decoupling` are both in `applied` state.
