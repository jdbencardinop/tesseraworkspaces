# External feature rename backpointers: analysis

## Summary

Prepare GitHub #5 as `fix-external-feature-rename-backpointers` against
`e57845104086e1e6416111424d15a359023cd289`. The published product baseline is
`v1.2.18-rc.6` at `5b531a8`; subsequent commits only refreshed documentation
and stabilized an inode-identity test fixture. The rename defect is still
present. Planning is complete and Path B implementation is now authorized.
Publication still requires separate approval.

The implementation base is now
`6819a4e7a641396bce206022110b3b63351ac73d`, after two separately
user-approved test-only prerequisites over the planning baseline.
`d8e821b` removes an unrelated status fixture's assumption that a started shell
reaches its first log write within 250 ms. `6819a4e` initializes newly created
shell fixtures before timing their probe behavior. Production status behavior,
deadlines, and all rename requirements are unchanged. Neither prerequisite
belongs in this feature's patch.

The current whole-feature rename moves a directory and updates `spaces.yaml`
but never relocates Git's linked-worktree backpointers. This leaves the
worktrees physically under the new feature while their administrative
registration still names the old path. A later verified operation, such as
archive or injection, refuses. The released rc.5 binary already reproduced
this defect independently of template conflict resolution.

## Grounded baseline

- `internal/cli/rename.go:renameFeatureCmd` resolves the execution-selected
  feature context, takes a lifecycle lock and mode reservation, stages a
  spaces rewrite, guards external direct records, calls `os.Rename`, then
  commits the registry. Registry failure currently causes a directory-only
  rollback. There is no Git backpointer update or restartable feature-rename
  journal.
- `AcquireManagedFeatureLifecycleLock` locks the existing feature directory
  inode. That lock follows the directory move; it does not by itself reserve
  the now-vacant old pathname or prevent a competing destination creation.
- `internal/cli/managed_template.go` already resolves controlling repository,
  execution root, mode reservations, sessions, and moved reservation paths.
  Preserve this authority instead of independently resolving a different
  workspace from each entry's repository.
- `internal/inject.go` verifies regular `.git` markers, the worktree
  administrative directory, its `gitdir` backpointer and `commondir`, Git's
  top-level root, and NUL-delimited worktree inventory.
  `discoverInjectWorktrees` is an active injection projection: it skips
  archived/missing entries and is not a complete inventory of everything a
  whole-directory move could relocate.
- `internal/spaces.go:BeginSpacesFeatureRename` guards names, refuses pinned
  absolute targets, and stages relative-path/feature-scope rewrites under a
  registry lock. With no registry it is deliberately a no-op. `Commit`
  delegates to `saveSpaces`, which syncs a temporary file and renames it but
  does not itself sync the parent directory or expose an owned rollback
  result. Feature-rename durability must account for that boundary.
- Branch rename is a different implementation: it may remove/re-add a
  worktree and move branch refs. That is unsuitable for this operation,
  especially when dirty work must survive.

## Why not simply run Git repair

The official Git documentation describes `repair` as reestablishing
administrative connections after manual moves and notes that relative-path
settings can rewrite otherwise valid linking files:
<https://git-scm.com/docs/git-worktree>.

Source inspection at Git commit
`6de20f6092dcf9bdb1c8efe03db4b70c82b423dd` established that
`builtin/worktree.c:repair` processes supplied paths and then invokes
`worktree.c:repair_worktrees` for the repository's linked worktrees:
<https://github.com/git/git/blob/6de20f6092dcf9bdb1c8efe03db4b70c82b423dd/builtin/worktree.c#L1491-L1512>
and
<https://github.com/git/git/blob/6de20f6092dcf9bdb1c8efe03db4b70c82b423dd/worktree.c#L717-L730>.

An isolated Git 2.55.0 probe, with both absolute and relative worktree
configuration, moved one selected feature and corrupted an unrelated sibling's
forward `.git` pointer. Repairing only the selected new path also rewrote the
sibling. The result rules out repository-wide repair as a hidden side effect,
not Git's read-only discovery/validation commands.

Use exact verified pointer files for this feature only, backed by preserved
preimages and the existing rooted/no-replace filesystem primitives. Do not
rewrite unrelated Git metadata, normalize all repository worktrees, or change
path-style configuration as an incidental effect.

## User decisions and limits

The user approved targeted pointer updates, future-renames-only scope,
preservation of ordinary dirty external worktrees, and verified automatic
rollback when safe. They additionally approved explicit journal-bound
continue/abort for interruptions created by this implementation.

"No dangling references" is a success/recovery postcondition: every affected
worktree's forward pointer, backpointer, common directory, registration, and
HEAD relationship must validate at the chosen final location. Branch refs
must not be rewritten or pruned. There can be a temporary mismatch between
the directory move and pointer publication; it must never be reported as
success and must be covered by durable operation evidence and recovery.

Do not expand this into arbitrary historical repair, moving primary Git
repositories/common directories, populated submodule relocation, branch
rename, or changes to valid checkout-mode behavior. Unsupported path-bearing
Git configuration or nested repository layouts must refuse before movement
when the complete relocation relationship cannot be proved.

## Dependencies and principal risks

Hard parents are `rename-feature-worktree` for the command boundary,
`workspace-sibling-links` for coordinated registry behavior, and
`template-conflict-resolution` for the current strict ownership/reservation
contract, including v1/v2 preservation. The latter transitively includes the
verified slash-worktree discovery and named-template foundations.
`fix-atomic-rename` is related branch-rename prior art, not a mutation helper
to copy or an artificial extra dependency.

Key risks are omitted physical worktrees, unselected Git repair side effects,
dirty data loss from remove/re-add, destination/path-identity races, rewriting
foreign administrative files, losing the actual location after a returned
rename/fsync error, and treating a partially published registry as untouched.
Recovery must be reachable when the old feature path no longer exists.

Native guidance can embed the feature name. Rename must preserve its current
bytes and any acknowledgement, not silently render or accept a different
proposal. A later explicit template sync may therefore require an ordinary
new conflict decision when the renamed feature changes rendered content.
That is existing template policy, not a dangling Git pointer or lost override.

No toolchain upgrade is inherently required. The existing historical tpatch
parent-artifact verification limitation remains separately tracked under
`repair-historical-tpatch-artifacts`; do not change real dependencies or
published artifacts merely to hide it.
