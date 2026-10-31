# Specification: deterministic archive/reactivation identity fixture

## Acceptance criteria

1. Modify only `TestManagedTemplateArchiveReactivateRefreshesLinkIdentity` in
   `internal/cli/named_template_final_regression_test.go`.
2. Before archive, retain the actual `CLAUDE.local.md` symlink inode under the
   fixture's root, outside the feature/worktree being removed. Do not replace,
   rename, or otherwise alter the original link used by the archive command.
3. Use `Lstat`/`SameFile` to prove the retained object is the original symlink,
   not a followed target or a newly copied symlink. Fail clearly if that fixture
   precondition cannot be established; do not skip the test.
4. Preserve the existing strict device/inode freshness assertion and all
   archive-removal, link-retirement, reopen, and rename assertions.
5. Run repeated focused checks, including
   `go test ./internal/cli -run '^TestManagedTemplateArchiveReactivateRefreshesLinkIdentity$' -count=40 -timeout 10m`,
   and the full ordinary test/vet/lint/build/diff/DAG gates.
6. Record/replay exactly the one-file change at the declared baseline, with
   two independent exact-base recipe replays and canonical patch verification.
   Land one traceable test-only commit and repeat post-land gates.
7. Do not move or delete `v1.2.19-rc.1`, rerun its failed workflow to hide the
   result, or weaken production checks. Require fresh successful main and
   new-RC tag CI before closing #9 and #8 or starting the dependent feature.

## Plan and non-goals

Add a short fixture-retention block immediately before the archive call.
Reuse `injectFixture.root`, standard library identity checks, and the existing
Unix dependency's `Linkat` with zero flags for an explicit no-follow hard link.
Do not introduce a new shared fixture framework or dependencies.

No runtime code, metadata schema, Go dependency, test deadline, test skip,
relaxed assertion, historical artifact rewrite, or unrelated fixture sweep
belongs in this boundary.
