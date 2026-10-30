# Scoped standalone push selection

Address [#6](https://github.com/jdbencardinop/tesseraworkspaces/issues/6).
Add `tws push <feature> --only <entry>` and `--from <entry>` using the
existing logical selector and descendant closure. For `main -> A -> B -> D`
with sibling `C` under `A` and independent `main -> X -> Y`, selecting
`--from A` means exactly A/B/C/D in dependency order; `--from B` means B/D.
Ancestors and neighboring stacks are not publication targets.

The user selected support for the new scoped route in both external and
checkout modes, verified repository/ref publication without materialized
worktrees, and an independently typed durable publication plan with explicit
continuation. Preserve existing unscoped behavior. Preview is local-only,
shows exact selected intent, and writes nothing. Stop on failure; never drop
lease/inclusion protection, refresh lease authority silently, or fall back
to a feature-wide push. Cancellation abandons remaining publication, never
rolls back remote refs.

Scoped sync-plus-push already exists and keeps its recovery owner. Preserve
its selected publication and fix directly coupled selected-only remote
follow-up handling without creating another selector or fake sync transaction.
Preserve rename admission and unrelated dirty/unavailable worktrees and agents.

## Authorization

The subsequent overnight instruction authorizes autonomous Path B planning,
implementation, independent reviews, validation, exact-base replay, one
feature landing, push, and an RC after successful main and tag/release CI.
Record consequential decisions in the session's `overnight-decisions.md`.
No phase, failed gate, or review blocker may be skipped to meet the time window.
