# Analysis: archive/reactivation inode fixture

## Evidence

Ubuntu tag CI run `41196873802` failed only
`TestManagedTemplateArchiveReactivateRefreshesLinkIdentity`, at
`internal/cli/named_template_final_regression_test.go:872`. Main CI passed and
the tag macOS job passed; release publication was skipped.

The test records the original symlink device/inode, archives the worktree
(freeing that symlink), recreates it, and requires a different device/inode.
The allocator is allowed to recycle the freed inode. The test therefore does
not guarantee the physical distinction that its regression assertion intends
to measure. The failing test and production template code were unchanged by
the Node 24 feature.

Forty local repetitions of the unmodified test passed; that does not erase the
actual Ubuntu failure or justify rerunning CI for luck. A local filesystem
probe confirmed that retaining a hard link to the original symlink preserves
its inode and prevents same-target replacement from reusing it.

Prior commit `e57845104086e1e6416111424d15a359023cd289` fixed the same kind of
invalid fixture assumption in a different template replacement test by
retaining its original symlink. Here the original path must remain present
until archive runs, so a same-filesystem hard link is the appropriate retention
mechanism.

The first implementation used `os.Link`, but its new retained-inode
precondition failed on macOS. Use the already-declared Unix dependency's
`Linkat` with zero flags to request the symlink itself explicitly, and keep
the `Lstat`/`SameFile` check. Do not weaken or skip that portability check.

## Compatibility and scope

Keep the original managed symlink in place, retain its inode outside the
removed worktree, and verify that the retained object is the same symlink
inode. Leave every archive, reactivation, freshness, rename and retirement
assertion intact. If production really returns stale ownership, the existing
strict freshness assertion must still fail.

This is one test-file change with no production, timeout, retry, or CI-policy
change. Preserve the failed rc.1 tag and the already-landed Node 24 artifacts.
Use a new RC for certification after the separate test-only landing.
