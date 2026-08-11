# Feature Request: Make the spaces absent-registry no-side-effect test ignore only transient Git lock files under .git (such as objects/maintenance.lock), while still comparing every other path — including the tws-owned .git/tws/workspace-id and .git/info/exclude — and explicitly asserting that spaces.yaml, .spaces.lock, and workspace markers are not created.

**Slug**: `fix-space-test-git-maintenance-race`
**Created**: 2026-08-11T14:30:06Z

## Description

Make the spaces absent-registry no-side-effect test ignore only transient Git lock files under .git (such as objects/maintenance.lock), while still comparing every other path — including the tws-owned .git/tws/workspace-id and .git/info/exclude — and explicitly asserting that spaces.yaml, .spaces.lock, and workspace markers are not created.

## Scope revision

The original request said "ignore transient `.git` metadata". Review found that `.git` is not
exclusively Git-owned in this project: tws writes `.git/tws/workspace-id` (`internal/workspace.go`)
and `.git/info/exclude` (`internal/enable.go`, `internal/cli/init.go`). Filtering the whole `.git`
subtree would hide exactly the tws-owned artifacts this no-side-effect test exists to catch, so the
exclusion is narrowed to **transient lock files** — a path with a `.git` segment whose base name
ends in `.lock`. Every other `.git` path is still compared.
