# Feature Request: Fix GitHub issue #4 defect 1: make sync recovery closure-aware. Snapshot and durably protect every selected branch tip and relevant stack metadata before the first Git mutation; if --full or another sync mode advances an anchor and a later child fails, '--abort' must restore every ref moved by that run, including anchors, unless a branch gained later user work, in which case abort refuses without erasing it. Surface partial progress and recovery guidance in status/doctor, preserve no-flag success behavior, and use real multi-branch worktree tests.

**Slug**: `sync-transactional-abort`
**GitHub issue**: [#4, defect 1](https://github.com/jdbencardinop/tesseraworkspaces/issues/4)
**Created**: 2026-09-12T15:42:23Z

## Description

Fix GitHub issue #4 defect 1: make sync recovery closure-aware. Snapshot and durably protect every selected branch tip and relevant stack metadata before the first Git mutation; if --full or another sync mode advances an anchor and a later child fails, '--abort' must restore every ref moved by that run, including anchors, unless a branch gained later user work, in which case abort refuses without erasing it. Surface partial progress and recovery guidance in status/doctor, preserve no-flag success behavior, and use real multi-branch worktree tests.
