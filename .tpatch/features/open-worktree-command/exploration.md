# Exploration: explicit non-agent open

Explored the real source at `034126331db73522bb98d1f1a1fc08e146a68fe5`.
The public contract is in `spec.md`; this map identifies reuse points, not
permission to redesign their legacy callers.

## CLI routing and validation

- `internal/cli/open.go`: `openCmd` currently uses `RangeArgs(0, 2)`,
  completion by selector count, and separate checkout/all/feature-directory/
  ordinary-entry routes. Its returned `featureRenameAdmittedCommand` is
  stateful. Preserve the legacy `RunE` behind a new explicit-mode dispatch;
  validate new modes and split selector/child argv before stateful admission.
  Completion must detect the command separator rather than suggest selectors.
  Distinguish a first `--` escaping leading-dash selectors from the later
  post-selector delimiter; required selector positions make this unambiguous.
- `internal/cli/feature_rename.go`: `featureRenameSelectionAdmission` checks
  selected rename and publication evidence before fallback. Its preview mode
  and `featureRenameRouteAdmission` provide read-only checks; writer mode
  retains actual name reservations. Do not use the ordinary wrapper for the
  path query.
- `internal/cli/managed_template.go`: `resolveFeatureContext`,
  `managedTemplateWorkspaceForFeature`, `resolveManagedConfig` and
  `resolvedManagedConfig` carry the execution-selected root and renderer.
  Preserve configured-root fallback only after selected-root admission.
  Command argv must never replace the configured/saved renderer.
- `internal/cli/reparent_observability.go`: `refuseOpenDuringReparent` and
  `refuseExternalOpenDuringMutation` check the resolved feature's reparent,
  publication and shared mutation intent.
- `internal/workspace.go`: workspace/config discovery and
  `ResolveFeaturePath` already represent external versus explicit checkout
  routing and supported legacy layouts. Discovery is read-only; do not create
  markers or infer checkout mode from directory presence.
- `internal/inject.go`: `ResolveInjectWorktree` verifies a selected logical
  target's path and real Git relationship, supports slash names and verified
  legacy no-stack layouts, and refuses archived/unsafe/unregistered targets.
  Reuse it without materializing or traversing unrelated worktrees.

## Direct execution and observation

- `internal/cli/direct_open.go`: `directRunner` and `directProcess` already
  expose start/PID/wait/terminate; `directSessionStore` creates, updates and
  removes by token. `realDirectProcess` reaps once and bounds termination.
  A narrow command path can reuse those mechanisms. Do not inherit
  `strings.Fields(agent_command)`, Claude continuation, agent labels or the
  unconditional external agent-to-shell transition.
- `internal/direct_session.go`: schema 1, optional `Agent`, free-form stage
  and `.sessions/<branch-id>/<token>.json` persistence. Extend the existing
  stage constants/consumers rather than creating an argv/history protocol.
- `internal/agent_status.go`: `recognizedSessionStages` and
  `SessionObservation` are the narrow projection points. `agent_state` is
  deliberately unknown. A command stage is additive and requires no new
  document key or schema bump; external tmux observation is already
  transport-neutral.
- `internal/cli/open.go`: `withExternalSessionLaunchIntent` retains name
  admission, publishes entry/feature intent, runs the late hook/check, and
  releases owned intent after the action. Reuse its lifecycle for command
  injection/launch and feature-directory execution.

## Checkout lifecycle and tmux

- `internal/cli/open_checkout.go`: normal checkout open resolves selectors,
  configured tmux preference and managed injection, then calls the existing
  agent APIs. Keep that route's no-agent and agent behavior unchanged.
- `internal/session.go`: `CheckoutSessionPreconditions`,
  `CheckoutFeatureDirSessionPreconditions`, `WithCheckoutSessionLaunchIntent`,
  `anyCheckoutSyncActive` and `anyCheckoutReparentActive` supply the existing
  exclusions. The sync helper includes publication and retained metadata-move
  evidence. Extract a mutation-only helper for queries without weakening
  either existing launch precondition.
- The same file supplies `PlanCheckoutSessionLinks`,
  `ApplyCheckoutSessionLinks`, `SaveCheckoutAgentSession`, `sessionSwitch`,
  `finishCheckoutSession`, and the token-owned lock snapshots. Add only the
  command-specific lifecycle needed to retain these invariants. Verify
  command ownership before cleanup/close effects; ordinary agent behavior
  must remain unchanged.
- `OpenCheckoutTmux` currently starts tmux before saving state and treats an
  early disappearance as failed startup with rollback. Do not copy that
  ordering. A command may already have edited the checkout when its tmux
  session disappears. Save owned state before child effects and use safe
  restoration or retained evidence for startup, attach and completion paths.
- `RealSessionTmuxRunner` preserves an argv slice, but tmux treats one command
  argument as shell source. The tested fixed positional `exec "$@"` bridge
  handles zero args and unusual executable paths as well as ordinary argv.
  No `send-keys` interpolation or silent existing-session attachment.
  The client also parses trailing semicolons and formats `-s`/`-c` values:
  escape those distinct transport boundaries without changing child argv.
- `internal/cli/close.go`: external close still requires feature and branch;
  a feature-directory tmux uses existing direct tmux controls. Checkout close
  consumes recorded identity/transport; couple only generic-command cleanup
  to the new ownership checks, not an unrelated close redesign.
- `internal/cli/root.go`: returned errors map to CLI failure. Preserve this
  instead of adding a global child-exit-code protocol.

## Test locations and independent evidence

`internal/cli/direct_open_test.go` provides fake runners backed by real
token-owned persistence and old ordering/resume/shell regressions.
`internal/cli/checkout_session_cli_test.go` provides `setupSessionCLI` and
legacy picker, feature-dir, no-switch/no-agent and close behavior.
`internal/session_test.go` provides `setupSessionRepo`, real Git operations
and controlled agent/shell/tmux runners. Use ordinary command-named tests and
do not edit the reserved reparent leaf-budget inventory.

`internal/cli/inject_test.go` provides `newInjectFixture`; named-template
regression tests and `internal/template_managed_test.go` cover ownership and
renderer rules. Existing status tests beside `internal/agent_status.go` cover
schema/redaction/liveness; add only command-specific assertions.
`internal/cli/sync_plan_docs_test.go` contains the coupled current-target prose
assertions and must follow the truthful handoff update.

CLI TestMain/helper Git calls reset `GIT_CONFIG_COUNT`. Use a temporary
`GIT_CONFIG_GLOBAL` when a real-repository test needs global config rather
than assuming injected config entries survive. Preserve original test
assertions and budgets. Mocks may control process failures, but Git behavior
must use real temporary repositories/remotes/worktrees.

The parent-owned independent harness uses the retained pre-open binary to
create fixtures, checks exact path stdout and byte/inode/path snapshots,
literal argv and caller-relative executable binding, generic live status,
mutation exclusion, direct failure/cleanup, dirty checkout retention and
actual isolated tmux/PTY execution. Baseline flag refusal and backend
mechanics are verified; candidate behavior still must pass.

## Documentation and planned footprint

Update `README.md`, `docs/cheatsheet.md`, `docs/configuration.md`,
`assets/skills/claude/tesseraworkspaces/SKILL.md`,
`assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md`, and
`assets/skills/copilot/tws.prompt.md` for the new explicit surfaces.
Update `CHANGELOG.md`, `docs/roadmap.md` and `docs/engineering-workflow.md`
to record the verified maintenance RC and current open boundary, keeping all
older safety/release assertions.

Prefer small new CLI and internal command helpers adjacent to the existing
open/session files, narrowly wired into their entry points. Do not refactor
unrelated agent code, add dependencies, modify the Node 24 workflow, or rewrite
older tpatch features. The source remains uncommitted until independent review,
full gates, exact recording/replays and `tpatch land`.

Hard parent remains `scoped-push-selection`; soft CI ordering remains
`update-github-actions-node24`. No new dependency surfaced during exploration.
