# Analysis: divergent-stack-sync

## Summary

Divergent stack sync (DAG support) was already implemented correctly from the start. The data model (stack.yaml), topo sort (Kahn's algorithm), tree rendering (PrintTree), and two-pass sync all handle DAGs natively. This feature formalizes that with explicit tests and documentation.

## Verified Edge Cases

1. Linear chain: main → A → B → C — works
2. Divergent: A has children B and C — topo sort handles, sync processes both
3. Archived leaves in divergent stack — optimistic rebase on both
4. Archived root with active children — `--update-refs` from children updates root
5. Mostly archived (one active leaf) — `--update-refs` updates ancestry chain, optimistic rebase for other lineages
6. Fully archived — optimistic rebase in topo order
7. Cycle detection — TopoSort returns error

## Acceptance Criteria

1. `tws new` supports creating divergent branches (multiple children from same parent)
2. `tws stack` renders tree structure correctly for divergent stacks
3. `tws sync` processes divergent branches in correct topo order
4. Archived branches in divergent stacks sync correctly via `--update-refs` or optimistic rebase
5. Failure in one lineage doesn't skip branches in a sibling lineage
6. All scenarios covered by unit tests
