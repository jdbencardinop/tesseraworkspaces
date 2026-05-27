# Case Study: tesseraworkspaces v1.0.0 Development — tpatch as a Development Workflow

**Study ID**: `tws-dev-2026-05`
**Date**: 2026-05-23 to 2026-05-27
**Project**: tesseraworkspaces (tws) — feature-scoped workspace manager for parallel agent workflows
**Repo**: https://github.com/jdbencardinop/tesseraworkspaces
**tpatch version**: 0.6.1
**Workflow**: Path B (agent-assisted implementation)

## Context

This case study evaluates tpatch as a **development workflow tool** rather than its original use case of upstream reconciliation. The project (tws) was built from an empty `main.go` to a v1.0.0 release over ~5 days, using tpatch to track features, manage dependencies, and record patches throughout.

The developer worked with a Claude Code agent (Opus 4.6) as the primary implementation partner, using tpatch's analyze → define → explore → implement → record cycle for each feature.

## Scale

- **42 features** registered (35 implemented, 7 remaining)
- **18 tagged releases** (v0.1.0 through v1.0.0)
- **~3,500 lines** of Go code
- **22 CLI commands** in the final tool
- **25 unit tests** + smoke tests per feature
- **35 production commits** + 35 tpatch metadata commits

## What Worked Well

### 1. Feature tracking as a backlog

tpatch features served as an excellent lightweight backlog. `tpatch add --slug <name>` was fast, and `tpatch status` gave an instant overview. The convention of registering features during design discussions (before implementation) meant no ideas were lost.

**Verdict**: Strong. Better than a separate issue tracker for a solo/small-team project because the backlog lives with the code.

### 2. Dependency graph

`tpatch feature deps` was valuable for ordering work. The DAG validation (`--validate-all`) caught potential issues early. Features naturally formed chains (user-defined-workdir → auto-detect → lightweight-worktree → stacked-diffs) and the graph made the implementation order obvious.

**Verdict**: Strong. The dependency edges were consulted before every implementation to ensure prerequisites were met.

### 3. Path B workflow (agent-assisted)

The analyze → define → explore → implement → record cycle provided good structure for agent collaboration. The analysis phase built shared understanding, define/explore narrowed the scope, and the agent could implement with full context.

**Key discipline**: The steering file rule — "validate dependencies after analyze, define, explore" — was useful. We discovered new dependency links during exploration that wouldn't have been caught otherwise.

**Verdict**: Strong. The phased approach prevented scope creep and ensured each feature was well-understood before coding.

### 4. Clean commit separation

The convention of "production commit first, then `tpatch record --from HEAD~1`, then chore(tpatch) commit" kept the git history clean. Production code and tpatch metadata never mixed in the same commit.

**Verdict**: Strong. One discipline violation (committing to wrong branch) was caught and corrected via cherry-pick.

### 5. Record as documentation

`tpatch record` captured the exact diff for each feature. The apply-recipe.json files serve as a structured record of what changed and why. This is valuable for the case study itself — we can reconstruct exactly what each feature touched.

**Verdict**: Moderate to strong. The recipes are useful as documentation but rarely needed for replay (greenfield project, no upstream to reconcile against).

## What Didn't Work Well

### 1. Provider reliability

The copilot provider (gpt-5.5 via localhost:4141) was unreliable — falling back to heuristic mode on most invocations with 500 errors. This meant ~90% of analysis, define, and explore artifacts were written manually.

**Impact**: Low — manual artifact writing was fast and often better quality than provider-generated content, since the agent had full codebase context. But it adds friction and defeats the purpose of having a provider.

**Mitigation used**: `--manual` flag for all phases. The agent wrote artifacts directly into the tpatch feature directory.

**Verdict**: Weak. The provider path needs reliability improvements. The `--manual` fallback saved the workflow.

### 2. Heuristic mode artifacts are useless

When the provider fell back to heuristic mode, the generated analysis was always the same boilerplate: "Manual review recommended." These had to be overwritten every time.

**Recommendation**: Don't generate heuristic artifacts at all — let the user know no artifact was created and they should write one manually or retry.

### 3. Phase ceremony for simple features

For small features (one-file changes, bug fixes), the full analyze → define → explore cycle was excessive. We often fast-tracked by writing all three artifacts at once and advancing phases in a single command chain.

**Recommendation**: A `tpatch fast-track <slug>` command that creates minimal artifacts and advances directly to implementing.

### 4. Record collision on bundled commits

When multiple features were implemented in a single commit (e.g., quiet-fetch + clean-output + validation + push), `tpatch record` detected collisions since all features had the same patch. Required `--allow-collision` with a reason.

**Recommendation**: This is correct behavior for upstream reconciliation (where collisions indicate problems). For development workflow, a `--bundled` flag or automatic collision acceptance for same-commit features would reduce friction.

### 5. Patch round-trip warnings

Some recorded patches triggered "corrupt patch" warnings during round-trip validation, typically from Go files with tab indentation or markdown with special characters. The patches still worked but the warnings were noisy.

**Recommendation**: The `--lenient` flag exists but requires manual invocation. Consider auto-lenient for known-safe file types (`.go`, `.md`).

## Observations

### Feature count growth

Started with 4 features, ended with 42. Features were added organically during design discussions — every "what about..." conversation produced a `tpatch add`. This is healthy (captures intent) but means the backlog grows faster than implementation.

**Pattern**: Features clustered around user feedback. Two user retrospectives generated 6 features each. The backlog is a living document of user needs.

### Tpatch as conversation artifact

The tpatch features served as conversation artifacts between the developer and the agent. Each feature's analysis captured design decisions, and the exploration captured what was learned about the codebase. This is more structured than chat history and more actionable than meeting notes.

### Steering file evolution

The local steering file (`.tpatch/steering/local.md`) was updated during the project to add the dependency validation clause. Steering files are underused — they could contain more workflow rules (e.g., "always run smoke tests", "update skills after new features").

### Skills update frequency

The embedded agent skills needed updating after every 2-3 features. We did 4 skills update passes. This suggests skills should be auto-generated from the CLI's help output or feature list rather than manually maintained.

## Metrics Summary

| Metric | Value |
|--------|-------|
| Features registered | 42 |
| Features implemented | 35 |
| Tags released | 18 |
| Provider success rate | ~10% |
| Manual artifact rate | ~90% |
| Avg feature cycle | ~30 min |
| Dependency edges | 18 |
| DAG depth | 4 |
| Commit violations | 1 |
| Round-trip warnings | 3 |
| Record collisions | 4 |

## Recommendations for tpatch

1. **Fast-track mode** for small features (skip ceremony)
2. **Bundled commit support** for multi-feature commits
3. **Don't generate heuristic artifacts** — fail explicitly
4. **Auto-lenient** for Go/Markdown files
5. **Provider retry** with backoff before falling back to heuristic
6. **Skills auto-generation** from CLI metadata
7. **Steering file templates** for common workflows (Path A vs Path B)

## Conclusion

tpatch works well as a development workflow tool, even outside its original upstream-reconciliation use case. The feature tracking, dependency graph, and phased workflow provide structure that scales from a single developer to a team. The main friction points are provider reliability and ceremony overhead for small changes — both addressable with targeted improvements.

The Path B workflow (agent-assisted with manual recording) is the practical default when providers are unreliable. The `--manual` flag and direct artifact writing saved the workflow, but the tool should optimize for this path rather than treating it as a fallback.

**Overall verdict**: tpatch is a net positive for structured development. The overhead of recording patches is justified by the documentation value and the ability to replay features on different codebases. For greenfield projects, the feature-tracking and dependency management alone are worth the workflow.
