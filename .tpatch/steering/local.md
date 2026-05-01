# Local Steering

The best approach for path B implementing (working witha coding agent) is to work on a feature normally for analyze, define, explore, then do not apply immediately.
We then move onto the execute phase with the --start and --stop flags, which will allow the agent to do the changes to the production code, then we can test if needed or commit the production code changes right away (without any .tpatch files).
After commiting the changes, we can move onto 'record' phase with the --from HEAD~1 flags, which will allow us to create a .tpatch file with the changes we just commited.
That will generate a patch-recipe.json for the feature, the agent can extend the "description" to showcase what each change means, then we can verify the recipe with "apply".
After that we can do a chore(tpatch) commit, with just the metadata of tpatch to have a clean separation of commits.

## Dependency Validation

After completing the analyze, define, and explore phases for any feature,
validate the dependency graph before proceeding to implementation.
New links or ordering constraints often surface during exploration —
register them immediately with `tpatch feature deps <slug> add <parent>`.
Run `tpatch feature deps --validate-all` to confirm the DAG is still acyclic
and free of dangling refs before moving on.

## Phase Ordering

```
requested    → tpatch analyze    → analyzed
analyzed     → tpatch define     → defined
defined      → tpatch explore    → defined (exploration.md enriched)
defined      → tpatch apply --mode started / edit / --mode done    → applied
applied      → tpatch record     → active
# Optional just for verification
active      → tpatch implement  → active (apply-recipe.json ready)
# When upstream changes happen, we can do a reconcile
active       → tpatch reconcile  → active | upstream_merged | blocked
```
