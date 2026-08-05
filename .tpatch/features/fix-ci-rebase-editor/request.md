# Feature Request: Make checkout sync conflict-continuation integration tests deterministic in noninteractive CI by setting a no-op Git editor for test Git commands. GitHub Actions currently fails `git rebase --continue` on Linux and macOS with `Terminal is dumb, but EDITOR unset`, while local shells pass. Product behavior is unaffected. Add regression coverage and restore green main/tag CI before continuing checkout-agent-sessions.

**Slug**: `fix-ci-rebase-editor`
**Created**: 2026-08-05T01:41:15Z

## Description

Make checkout sync conflict-continuation integration tests deterministic in noninteractive CI by setting a no-op Git editor for test Git commands. GitHub Actions currently fails `git rebase --continue` on Linux and macOS with `Terminal is dumb, but EDITOR unset`, while local shells pass. Product behavior is unaffected. Add regression coverage and restore green main/tag CI before continuing checkout-agent-sessions.
