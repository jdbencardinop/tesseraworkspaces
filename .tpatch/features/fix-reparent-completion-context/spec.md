# Execution-aligned reparent completion

1. Fix positional entry and `--onto` completion for `tws stack reparent`
   using the same explicit workspace mode and selected feature layout as
   execution. Respect TWS_ROOT precedence, valid configured fallback and
   existing pending rename/publication refusal; never borrow another feature.
2. For an existing target, resolve its repository through
   `ResolveReparentTargetRepository`. Enumerate Git branch destinations from
   that repository using the existing branch inventory, not the caller's
   default repository. Retain actual Git versus logical entry identity.
3. Resolve one coherent completion context per callback. Use the complete
   selected stack and existing `ReparentClosureOrder`; do not add another
   graph walk/sort, owner-inference algorithm or reparent planner.
4. Preserve existing logical target/descendant exclusion, archive eligibility,
   declaration-order logical suggestions followed by sorted Git suggestions,
   and the fixed `--onto-kind` choices. Do not redesign literal-ref eligibility.
   Without a known target, logical entry suggestions may be returned, but do
   not guess a repository for Git-ref suggestions.
5. Unknown/unavailable/unsafe/ambiguous feature or target context returns no
   candidates silently. A failed feature context must not be followed by
   caller/default repository branch suggestions.
6. Completion is read-only/local-only: no fetch/network, writer lock, journal,
   state/metadata/ref/index mutation, session launch, repair or adoption.
   Use existing read-only refusal checks without acquiring reservations.
7. Support external and explicit single-repository checkout modes, including
   the existing legacy/namespaced layouts. Preserve all execution validation,
   flags/help snapshots, mutation semantics, no-flag behavior and process
   budgets. Do not fix unrelated executor or workspace-owner behavior.
8. Add focused real-Git tests through registered Cobra completion callbacks
   and, where practical, `tws __complete stack reparent <feature> <entry> --onto ""`.
   Cover differing selected/configured roots with distinct entry sets;
   an explicit secondary target repository with distinct refs; decoupled/slash
   names; target/descendant exclusion; checkout and legacy layouts; unknown or
   unreadable context; and no effects/remote contact. Retain exact existing
   help/flag assertions.
9. Update user/embedded-agent guidance where completion behavior is described,
   and the roadmap/workflow handoff. Do not claim rc.8 publication until the
   parent confirms successful tag/release CI. Once confirmed, mark #6 shipped
   and #7 current while retaining all earlier milestones and negative
   planning-prose flag contracts. Never rewrite #6's published tpatch artifacts.
10. Follow isolated implementation, parent audit and the same three scoped
    independent reviews. Focused tests, full ordinary tests, vet, lint, build,
    diff and DAG gates precede one scoped record/replay/land. Verify exact-base
    recipe and patch replay and landed behavior; push only a clean approved
    commit, await main CI, tag the next unused RC, verify exact binary/tag CI
    and publication, then close #7.

## Non-goals

No new execution flag, remote operation, graph model, journal, recovery API,
unscoped behavior change, archive policy, arbitrary repository inference,
provider integration or historical tpatch artifact repair.
