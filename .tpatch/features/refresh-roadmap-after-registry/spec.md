# Specification

## Acceptance criteria

1. `docs/roadmap.md` identifies checkout doctor observability and the global
   workspace registry as shipped.
2. `docs/roadmap.md` identifies `workspace-sibling-links` as the current target
   while retaining the P1 stack-safety backlog.
3. `docs/engineering-workflow.md` reports the same shipped capabilities and
   current target.
4. `rg -n "workspace sibling links|workspace registry|checkout doctor" docs/roadmap.md docs/engineering-workflow.md`
   shows consistent current-state guidance.

## Out of scope

- Product behavior or CLI changes.
- Implementing workspace sibling links.
- Repairing historical tpatch artifacts.

## Plan

Update the current-direction sections of `docs/roadmap.md` and
`docs/engineering-workflow.md`, then run documentation consistency checks and
the repository release gates.
