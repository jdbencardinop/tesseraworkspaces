# Direct fixture review

Candidate:
`bd5784ff5415c21885c2675ef1c5176e891c87ef5471cefdb776a9c2c43d76d7`.

The one-file, test-only diff was reviewed directly. No blocking finding remains
in the candidate; full certification and new CI are still required.

- The original managed symlink remains in place for the archive operation.
- An explicit no-follow `unix.Linkat` retains that exact symlink inode outside
  the worktree. `Lstat`, symlink type and `SameFile` verify the precondition.
- All original archive, retirement, refresh, reopen and rename assertions are
  unchanged. In particular, the device/inode freshness assertion is not relaxed.
- The temporary fixture owns and cleans the retained link. No production code,
  dependency manifest, test deadline, skip, workflow, or published tag changes.
- The ordinary `os.Link` attempt failed the new macOS precondition and is not
  used. The corrected no-follow implementation passed the focused test once
  and then forty consecutive repetitions.
