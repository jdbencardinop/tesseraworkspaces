---
name: tessera-patch
description: Customize open-source projects through natural-language patches while maintaining upstream compatibility.
---

# Tessera Patch — Claude Code Skill

## What This Is

Tessera Patch is a framework for customizing open-source projects through natural-language-driven patching. This skill teaches you the methodology.

## You Are the Provider

The tpatch CLI has two paths for every LLM-driven phase:

- **Path A — CLI-driven.** Run `tpatch <phase> <slug>`; the configured provider generates the artifact; tpatch advances feature state.
- **Path B — Agent-authored.** Author the artifact yourself under `.tpatch/features/<slug>/` following the schemas in this skill; run `tpatch <phase> <slug> --manual` to advance feature state without calling the provider.

**You are the provider** whenever any of these are true:

- No provider is configured (`tpatch provider check` fails).
- The provider returned an empty, truncated, or obviously insufficient response (e.g. a 1-operation `ensure-directory` recipe, missing spec sections, a `write-file` with empty `contents`).
- You have more context about the codebase than the provider does (larger context window, loaded files, recent edits).

Path B is **normal**, not exceptional. Prefer it over re-running Path A with different prompts when you already know what the artifact should contain. Do not wait for a better recipe.

When in doubt, `tpatch status <slug>` tells you what phase you are in. `tpatch next <slug>` tells you what command to run next. Then pick Path A or Path B.

## Invocation

`tpatch` is a compiled Go binary on PATH. Invoke it directly — do NOT wrap it:

- ✓ `tpatch <command>`
- ✗ `npx tpatch …` (not a Node package)
- ✗ `npm run tpatch …` (not an npm script)
- ✗ `python -m tpatch …` (not a Python module)

Always run from the repository root (where `.tpatch/` exists). Do not `cd` to speculative paths — use the current working directory.

## Phase Ordering

```
requested    → tpatch analyze    → analyzed
analyzed     → tpatch define     → defined
defined      → tpatch explore    → defined (exploration.md enriched)
defined      → tpatch implement  → implementing (apply-recipe.json ready)
implementing → tpatch apply --mode execute                          → applied
             OR tpatch apply --mode started / edit / --mode done    → applied
applied      → tpatch record     → active
active       → tpatch reconcile  → active | upstream_merged | blocked
```

Never skip a phase. Never go backwards without `tpatch reconcile`.

## Before You Run Anything

1. `tpatch status <slug>` — see current state and last command.
2. `tpatch next <slug>` — get the exact next command (add `--format harness-json` for structured output).
3. Only then proceed. Do not guess the next phase from file presence.
4. Run tpatch record <slug> BEFORE git commit. If you already committed, use tpatch record <slug> --from <base> — a clean working tree without --from is refused.
5. Run tpatch reconcile only on a CLEAN working tree at the target upstream state. Commit or stash first; reconcile refuses dirty trees, conflict markers, and .orig/.rej leftovers. See docs/reconcile.md for the workflow patterns.

## Phases — Path A and Path B

Each phase below has the same shape: purpose, artifact, Path A command, Path B author-and-advance, quality checklist.

### Phase: analyze

- **Purpose**: Compatibility + impact analysis. Is this feature already upstream? Is it compatible with the project?
- **Artifact**: `.tpatch/features/<slug>/analysis.md` (plus `artifacts/analysis.json`)
- **Path A**: `tpatch analyze <slug>`
- **Path B**: Write `analysis.md` yourself (summary, compatibility notes, risks). Then `tpatch analyze <slug> --manual` to advance state to `analyzed`.
- **Checklist**:
  - [ ] States clearly whether the feature is already present upstream.
  - [ ] Flags obvious compatibility blockers (language version, framework, license).
  - [ ] One-paragraph summary usable by the reviewer.

### Phase: define

- **Purpose**: Acceptance criteria and an implementation plan.
- **Artifact**: `.tpatch/features/<slug>/spec.md`
- **Path A**: `tpatch define <slug>`
- **Path B**: Write `spec.md` yourself (problem statement, acceptance criteria as a numbered list, out-of-scope notes, phased plan). Then `tpatch define <slug> --manual`.
- **Checklist**:
  - [ ] At least one acceptance criterion is a command that can be run.
  - [ ] Out-of-scope is explicit to prevent scope creep.
  - [ ] Plan references files/modules, not invented paths.

### Phase: explore

- **Purpose**: Ground the implementation in real files and symbols.
- **Artifact**: `.tpatch/features/<slug>/exploration.md`
- **Path A**: `tpatch explore <slug>`
- **Path B**: Read the codebase with your tools. Write `exploration.md` yourself: relevant files (real paths, not hallucinated), key symbols, insertion points, test locations. Then `tpatch explore <slug> --manual`.
- **Checklist**:
  - [ ] Every file path referenced exists in the working tree.
  - [ ] Identifies the smallest changeset that satisfies the spec.
  - [ ] Cites tests that must pass (or must be added).

### Phase: implement

- **Purpose**: Produce a deterministic apply recipe.
- **Artifact**: `.tpatch/features/<slug>/artifacts/apply-recipe.json`
- **Path A**: `tpatch implement <slug>`
- **Path B**: Author the recipe yourself against the schema below. Then `tpatch implement <slug> --manual` (the flag validates the JSON before advancing state; malformed JSON is refused).
- **Checklist**:
  - [ ] Every `write-file.path` and `replace-in-file.path` exists in the plan OR is created by a prior op.
  - [ ] `replace-in-file.search` is unique enough to match exactly once.
  - [ ] No `path` escapes the repo root (`../`, absolute path, symlink target outside repo).

### Phase: reconcile

- **Purpose**: Re-evaluate feature against new upstream.
- **Artifact**: `.tpatch/features/<slug>/reconciliation/<commit-range>.md`
- **Path A**: `tpatch reconcile [slug...] --upstream-ref <ref>`
- **Path B**: See the 3WayConflicts playbook below — when the CLI returns `3WayConflicts` or `Blocked`, you resolve by hand. (`reconcile --manual` is reserved; the agent-driven reconcile path uses `apply` + `record` against the new upstream.)
- **Checklist**:
  - [ ] Working tree is clean before running reconcile.
  - [ ] Upstream commit the reconcile ran against is recorded in `upstream.lock`.

## apply-recipe.json schema

The `implement` phase produces a deterministic recipe that the `apply` phase consumes. When authoring manually, follow this schema exactly.

```json
{
  "version": 1,
  "operations": [
    { "type": "ensure-directory", "path": "src/feature/" },
    { "type": "write-file",
      "path": "src/feature/new.ts",
      "content": "export function greet(name: string) {\n  return `hello ${name}`;\n}\n"
    },
    { "type": "replace-in-file",
      "path": "src/index.ts",
      "search": "export * from \"./legacy\";\n",
      "replace": "export * from \"./legacy\";\nexport * from \"./feature/new\";\n"
    },
    { "type": "append-file",
      "path": "src/changelog.md",
      "content": "\n- added feature/new\n"
    }
  ]
}
```

### Operations

- **`ensure-directory`** `{ path }` — create the directory if missing. No-op if present.
- **`write-file`** `{ path, content }` — write the full file. Overwrites existing content.
- **`replace-in-file`** `{ path, search, replace }` — replace the first occurrence of `search` with `replace`. Errors if `search` is not found.
- **`append-file`** `{ path, content }` — append to an existing file. Errors if the file does not exist.

### Semantics

- `replace-in-file.search` is a **literal string match, not a regex**. Escape nothing; paste the exact text you want to replace including surrounding lines for uniqueness.
- `replace-in-file` replaces exactly one occurrence per operation. To replace multiple copies, emit multiple `replace-in-file` ops — each will target the next occurrence since prior ops rewrote the file.
- Include several surrounding lines in `search` when the same string appears more than once in the file, so the match is unique.
- All `path` values are repo-relative. tpatch enforces path safety via `EnsureSafeRepoPath`; any `../`, absolute path, or symlink target outside the repo aborts `apply --mode execute`.
- Operations are executed in the order they appear. Later ops may depend on earlier ops (e.g. `ensure-directory` before `write-file`).
- There is no `delete-file` or `rename-file` op in the current schema. To delete a file, use Path B: `apply --mode started`, `git rm <path>`, `apply --mode done`, `record`. Richer ops are tracked in `feat-recipe-schema-expansion`.

### Optional fields

- **`created_by`** — optional string on any operation. Value is the parent feature slug whose patch originally created this file. From v0.6.0 this is a **live apply-time gate**: `tpatch apply --mode execute` rejects an op whose `created_by` parent is missing from the recipe's `depends_on` (hard-parent miss is fatal in execute, warning in `--dry-run`). Omit when the feature has no DAG provenance to declare.

## Feature dependencies (v0.6.0+)

Tessera Patch tracks a dependency DAG between features. Declare parents in `status.json` `depends_on`, or via the CLI:

- `tpatch feature deps <slug>` — print depends_on + dependents.
- `tpatch feature deps <slug> add <parent>[:hard|:soft]` — add an edge (defaults to hard).
- `tpatch feature deps <slug> remove <parent>` — remove an edge (atomic).
- `tpatch amend <slug> --depends-on <parent>[:hard|:soft]` — same, in batch with other edits.
- `tpatch amend <slug> --remove-depends-on <parent>` — same, in batch.
- `tpatch feature deps --validate-all` — global validation (cycles, dangling, kind conflict).
- `tpatch status --dag` (add `--json` for harnesses) — render the DAG tree. Add a slug to scope to one feature's parents + children.

Edge kinds:

- **hard** (default) — `tpatch apply <child>` is blocked until every hard parent reaches state `applied` or `upstream_merged`.
- **soft** — ordering hint only; never gates apply.

Composable reconcile labels overlay on `Reconcile.Outcome`:

- `waiting-on-parent` — at least one hard parent has not yet been applied.
- `blocked-by-parent` — at least one hard parent is in a terminal-failure verdict.
- `stale-parent-applied` — a hard parent was updated after the child's last reconcile.
- Compound: when the child's own outcome is `blocked-requires-human` AND `blocked-by-parent` is set, `EffectiveOutcome` reports `blocked-by-parent-and-needs-resolution` (display-only — programmatic decisions still read `Outcome` and `Labels` separately).

Recipe operations may set `created_by: "<parent-slug>"` to declare DAG provenance. From v0.6.0 this is a **live apply-time gate**: `tpatch apply --mode execute` rejects an operation whose `created_by` parent is missing from `depends_on` (hard fail in execute, downgraded to a warning in `--dry-run` per PRD §4.3).

Removing a feature with downstream dependents requires `--cascade`:

- `tpatch remove <slug>` — refuses if any dependent exists.
- `tpatch remove <slug> --cascade` — TTY confirms, then removes leaves first (reverse-topological order).
- `tpatch remove <slug> --cascade --force` — required for non-TTY use.
- **`--force` alone never bypasses the dep-integrity gate** — it only suppresses the TTY confirm prompt (PRD §3.7, ADR-011 D7).

Toggle the whole feature with `features_dependencies: true|false` in `.tpatch/config.yaml` (default `true` from v0.6.0).

## Reconcile Phase 3.5 — Provider-assisted conflict resolution (v0.5.0)

On 3-way conflict, `tpatch reconcile --resolve` asks the provider to merge each conflicted file inside a **shadow worktree** (`.tpatch/shadow/<slug>-<ts>/`). The real working tree is never touched until you accept. See `docs/adrs/ADR-010-provider-conflict-resolver.md` for the full decision table.

### Flags

- `--resolve` — enable phase 3.5 (off by default; no heuristic fallback — ADR-010 D9).
- `--apply` — auto-accept when every file is `resolved`. Requires `--resolve`.
- `--max-conflicts N` — abort before calling the provider if conflicts > N (default 10).
- `--model <name>` — override the resolver model for this run.
- `--accept <slug>` / `--reject <slug>` / `--shadow-diff <slug>` — terminal operations on a pending shadow session. Mutually exclusive; take `<slug>` as the flag value, not a positional arg.

### Verdicts beyond v0.4.x

| verdict | meaning |
|---|---|
| `shadow-awaiting` | All conflicted files resolved; shadow ready for `--accept`. Feature state: `reconciling-shadow`. |
| `blocked-requires-human` | At least one file failed validation (still has `<<<<<<<` markers, corrupted output, or no provider configured). |
| `blocked-too-many-conflicts` | Conflict count exceeded `--max-conflicts`; the provider was never called. |

### resolution-session.json

Each resolver run writes `.tpatch/features/<slug>/artifacts/resolution-session.json` (renamed from `reconcile-session.json` in v0.5.3 — that path is now the high-level reconcile summary). Shape:

```json
{
  "slug": "<slug>",
  "timestamp": "2026-04-22T12:00:00Z",
  "upstream_commit": "<sha>",
  "shadow_path": ".tpatch/shadow/<slug>-<ts>/",
  "files": [
    { "path": "src/a.ts", "status": "resolved", "model": "gpt-4o-mini", "validation": "ok" },
    { "path": "src/b.ts", "status": "failed",   "model": "gpt-4o-mini", "validation": "markers" }
  ],
  "verdict": "shadow-awaiting"
}
```

Agents acting as the provider (Path B) can read this file, edit the shadow files directly, then run `tpatch reconcile --accept <slug>` to commit the resolution.

### Accept flow (what `--accept` actually does)

1. Apply non-conflicting hunks of `post-apply.patch` to the real tree via 3-way merge (excluding the resolved files).
2. Copy the resolved files from the shadow worktree over the real tree.
3. Regenerate `artifacts/post-apply.patch` as `git diff <upstreamCommit> -- <touched files>`.
4. Snapshot the resolution delta as `patches/NNN-reconcile.patch` (audit trail).
5. Mark state `applied`; prune the shadow worktree.

`apply-recipe.json` is NOT auto-regenerated (lossy from a raw diff). Re-run `tpatch implement` or `tpatch record` if the recipe matters to you.

## If reconcile returns 3WayConflicts

When `tpatch reconcile` cannot forward-apply cleanly, it returns verdict `3WayConflicts` and stashes the pre-reconcile tree. In v0.5.0 the fastest path is `tpatch reconcile --resolve` (see Phase 3.5 above). The manual playbook below remains the fallback when you prefer to resolve by hand or when phase 3.5 returns `blocked-requires-human`.

1. **Never pop the stash.** The stash holds your pre-reconcile tree. Popping it destroys upstream's state.
2. Restore only the tpatch metadata so you can see the feature's intent:
   ```
   git checkout stash@{0}^3 -- .tpatch/
   ```
   This pulls `.tpatch/` from the third parent of the reconcile stash (the index that contains your feature artifacts) without touching any other file.
3. Read intent and diff:
   - `.tpatch/features/<slug>/spec.md` — what the feature must achieve.
   - `.tpatch/features/<slug>/artifacts/post-apply.patch` — the current canonical diff.
4. Read the new upstream version of each conflicted file.
5. Hand-author a resolution that preserves **both** intents: the feature's intent from `spec.md` AND the upstream change.
6. Forward-apply: edit the files directly in the working tree; tpatch does not need to drive this.
7. Once the tree is clean and the feature works, run:
   ```
   tpatch apply <slug> --mode execute         # or --mode started / --mode done if you authored ad-hoc
   tpatch record <slug>
   ```
8. The `post-apply.patch` is rewritten; the recipe is regenerated on the next `implement`.

## Patch vs recipe — mental model

Two files describe your feature. They play different roles:

- `artifacts/post-apply.patch` — a git diff. This is the **authoritative description of what changed**. The patch captures intent.
- `artifacts/apply-recipe.json` — a deterministic script that produces the patch *against a specific upstream snapshot*.

When they disagree — e.g. the recipe's `replace-in-file` can no longer find its anchor because upstream edited the line — **trust the patch**. The recipe is one way to apply the change; the patch is what you want applied. During reconcile and manual conflict resolution, read the patch to understand intent; regenerate the recipe afterward.

## CLI Commands

| Command | Purpose |
|---------|---------|
| `tpatch init` | Initialize `.tpatch/` workspace and install skill formats |
| `tpatch add <description>` | Create a tracked feature request |
| `tpatch status` | Show feature status dashboard |
| `tpatch analyze <slug>` | Run analysis phase on a feature (add `--manual` for Path B) |
| `tpatch define <slug>` | Generate acceptance criteria and plan (add `--manual` for Path B) |
| `tpatch explore <slug>` | Read codebase, find minimal changeset (add `--manual` for Path B) |
| `tpatch implement <slug>` | Generate deterministic apply recipe (add `--manual` for Path B) |
| `tpatch apply <slug>` | Execute apply recipe or record an interactive session |
| `tpatch record <slug>` | Capture patches (tracked + untracked files) |
| `tpatch reconcile [slug...]` | Reconcile features against upstream |
| `tpatch provider check` | Validate LLM provider endpoint |
| `tpatch config show\|set` | Manage configuration |
| `tpatch cycle <slug>` | Run analyze→define→explore→implement→apply→record in sequence. Add `--interactive` to pause between phases |
| `tpatch test <slug>` | Run the configured `test_command` and record the pass/fail outcome |
| `tpatch next <slug>` | Emit the next logical action. `--format harness-json` for structured JSON |

## .tpatch/ Structure

```
.tpatch/
├── config.yaml          # Provider settings (secret-by-reference)
├── FEATURES.md          # Master feature index
├── upstream.lock        # Upstream commit tracking
├── steering/            # Local + upstream patching guidance
└── features/<slug>/     # Per-feature artifacts
    ├── status.json      # Machine-readable state
    ├── request.md       # Natural-language request
    ├── analysis.md      # Compatibility and impact analysis
    ├── spec.md          # Acceptance criteria + plan
    ├── exploration.md   # Codebase exploration log
    ├── record.md        # Implementation summary
    ├── reconciliation/  # Per-version reconciliation logs
    ├── patches/         # Append-only audit trail (NNN-<label>.patch)
    └── artifacts/
        ├── post-apply.patch     # Canonical diff (intent)
        └── apply-recipe.json    # Deterministic script (snapshot-specific)
```

## Feature States

```
requested → analyzed → defined → implementing → applied → active
                                                     ↓
                                               reconciling → active / upstream_merged / blocked
                                                     ↓ (with --resolve)
                                               reconciling-shadow → (accept) applied / (reject) active / blocked-requires-human
```

## Safety

- Path traversal protection on all file writes (`EnsureSafeRepoPath`)
- Secret-by-reference: config stores env var name, not the token
- Patches exclude `.tpatch/`, skill directories, and framework files
- Deterministic apply recipes can be reviewed before execution
- `reconcile` refuses dirty trees, conflict markers, and `*.orig|*.rej` leftovers

## Editable Sections

<!-- Add project-specific instructions below -->

### Project-Specific Notes

*(Add notes about the upstream project's build system, test commands, and patching quirks here)*

### Custom Acceptance Criteria

*(Add standard acceptance criteria that should apply to all features in this fork)*
