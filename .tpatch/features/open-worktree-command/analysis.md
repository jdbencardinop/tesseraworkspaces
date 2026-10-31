# Analysis: explicit non-agent open surfaces

## Baseline and compatibility

Analyze against `034126331db73522bb98d1f1a1fc08e146a68fe5`, the verified
`v1.2.19-rc.2` maintenance baseline. Main CI `41204870523` and tag/release CI
`41207441379` passed on Ubuntu and macOS, including exact versions and the
native Node 24 release action. The existing request is still `requested`;
`tpatch next` selects analysis. The current CLI has neither new flag. An
independent real-repository probe confirmed that both workspace modes reject
`--print-path` without effects.

This is additive opt-in behavior, not a replacement for ordinary open.
Go 1.26.1, existing dependencies and supported Unix execution paths suffice.
No storage-version, provider, configuration-default or licensing change is
needed. Keep the configured agent and saved template renderer intact.

## Chosen boundary

Add `--print-path` and `--command <executable>`. Both require explicit feature
and logical-branch selectors, or one named feature with `--feature-dir`.
Only command mode consumes arguments after `--` as literal child argv.
Reject ambiguous or incompatible combinations before writer admission.
Preserve legal leading-dash selectors: a `--` before all required selectors
escapes options, then command arguments require a second `--` after those
selectors. A normal post-selector `--` remains the child-argument delimiter.

A path query returns one verified absolute path plus newline. It performs no
injection, activation, recovery, command launch, session persistence or writer
reservation. Read-only local Git probes are allowed. A checkout branch query
must describe the current attached logical entry, never implicitly activate a
different branch. Dirty work and a valid session alone do not invalidate an
observation; mutation/recovery evidence still refuses it.

A direct command runs one child with inherited stdio, then returns using the
existing CLI failure convention. It never invokes the configured agent, adds
resume flags, or opens a follow-up shell. Resolve PATH or an explicit
caller-relative executable before effects and bind its absolute path; do not
silently opt into Go's rejected implicit-current-directory lookup. Binding a
path is not a promise of immutable executable contents.

Support branch commands direct/tmux in both workspace modes. Feature-directory
commands support direct execution in both modes and explicit tmux in external
mode. Refuse checkout feature-directory tmux: the baseline has no durable
branchless session model. Preserve branch tmux preferences; feature-directory
commands default direct like the existing route.

## Existing mechanisms and risks

`internal/cli/open.go` currently wraps every named open in
`featureRenameAdmittedCommand`, which can create admission files before the
route runs. The new query and pure validation must branch before that wrapper.
The read-only selection/route admission helpers in `feature_rename.go` allow
both selected and resolved namespaces to be checked without creating locks.
Check selected publication/rename evidence before configured-root fallback.

`ResolveInjectWorktree` already verifies a selected external logical path,
including Git registration/backpointers, safe slash names, archived refusal and
verified legacy no-stack layouts. Reuse it rather than inventing filesystem
heuristics or probing unrelated worktrees. Commands retain existing managed
ownership/refusal and renderer-aware injection behavior.

External direct records and checkout session state already support free-form
stages. A recognized `command` stage with no generic `Agent` label needs no new
schema. Runtime presence is observable; semantic `agent_state` remains unknown.
Do not persist argv, environment, prompts or command history.

The direct runner already provides start/PID/wait and token-owned persistence.
Reuse those mechanisms without the legacy agent-to-shell transition. Keep
entry/feature launch intent and final mutation checks through command effects.
Checkout retains its one-owner lock, activation, context links and safe
restoration. Retained metadata-move evidence must still block launches/query
admission even if the shared mutation lock was removed.

Legacy checkout tmux saves session state after spawning and can roll back on
an early exit. Do not copy that window into generic commands, which can finish
or dirty the checkout immediately. Persist owned state before child effects,
verify ownership before restoration/close effects, and retain dirty or
uncertain evidence. Do not kill a preexisting or ambiguously created session.
Neither this feature nor the existing session protocol isolates arbitrary
external writers or closes the deferred process-birth/PID-reuse window.

Tmux interprets a single shell-command argument. A fixed
`/bin/sh -c 'exec "$@"' tws-command <absolute-executable> <argv...>` bridge
preserves literal arguments without placing caller text in shell source.
Protect tmux's own command-separator parsing and format expansion separately:
selected names/directories must remain literal, and caller argv must not
become additional tmux commands or format jobs.
An isolated real tmux 3.7b probe verified zero arguments, whitespace, quotes,
shell tokens, newlines and an unusual executable filename; an independent PTY
probe verified attachment/completion. Actual application tests remain required.

## Dependencies and validation

Hard parent: `scoped-push-selection`, which transitively supplies the applied
session, status, reparent, rename, template and verified-injection foundations.
The relevant open/session/context source is unchanged by the later completion
and maintenance commits. Soft parent: `update-github-actions-node24`, recording
the verified CI ordering without pretending it is a runtime dependency.

Add focused real-Git CLI/session tests and preserve existing open regressions.
Use ordinary fixtures rather than expanding the reserved reparent leaf matrix.
Run the independent actual-CLI harness, full unfiltered tests, vet, lint, build,
format/diff/DAG checks, two exact-base recipe replays and canonical patch
replay. Repeat full gates and actual-binary checks after landing.

The existing scoped-push parent's verifier has a recorded inherited
history-incomplete diagnostic despite non-shallow/non-partial repository
metadata and present probed objects. Other historical parent replay limits
are already deferred. Preserve that evidence, do not invent dependencies or
rewrite history to silence it, and independently verify this exact candidate.

## Exclusions

No global editor setting, command history, command fan-out through `--all`,
provider/session-control integration, new session schema, automatic
materialization, historical-layout repair, metadata-move recovery UX, or
historical tpatch repair. GUI commands must use a wait option; self-daemonized
descendants are not adopted. A child cannot change the parent shell directory.
Keep every legacy route unchanged when the new modes are absent.
