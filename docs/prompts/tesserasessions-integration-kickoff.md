# Prompt for the tesserasessions integration review

Use this prompt with an agent working in the tesserasessions repository.

---

You are the tesserasessions (`tss`) representative in a joint design review
with tesseraworkspaces (`tws`).

## Why this review exists

tws owns workspace, worktree, branch, and stack topology. tss owns historical
coding-agent inventory, live runtime observation, semantic agent state, and
provider-specific control.

The workmux research showed strong session UX patterns: durable
worktree-to-terminal identity, agent lifecycle hooks, layouts, attention
routing, `send`/`capture`/`wait`/`run`, and crash resurrection. We do not want
to copy that session subsystem into tws. We want tws and tss to cooperate while
remaining independently useful.

## Existing facts to preserve

1. `docs/runtime-status-contract.md` in tesserasessions is implemented and is
   the normative status-provider contract.
2. `tss status --json` is side-effect-free, bounded, versioned, and separate
   from inventory scanning.
3. tws keeps topology, tws-owned liveness, failures, attention rollups, and
   final authority.
4. tss already provides `attach`, `open`, `send`, `read`, and `run`, with
   Herdr preferred and tmux as fallback.
5. Neither project reads another tool's private database or transcript store.
6. `runtime_presence` and `agent_state` stay separate.
7. Native Herdr/tmux behavior must not depend on workmux.

## Reference material

- tws workmux research:
  `../tesseraworkspaces/docs/research/workmux-market-research.md`
  or the equivalent local tesseraworkspaces path;
- tws collaboration plan:
  `../tesseraworkspaces/docs/integrations/tesserasessions-collaboration.md`;
- tss status contract:
  `docs/runtime-status-contract.md`;
- tss roadmap:
  `docs/roadmap.md`;
- pinned workmux source:
  https://github.com/raine/workmux/tree/0c6bedc55ceac499997438cca57a72139f116047
- workmux multiplexer abstraction:
  https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/multiplexer/mod.rs
- workmux token identity:
  https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/git/worktree.rs#L400-L410
- workmux tmux ownership:
  https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/multiplexer/tmux.rs#L841-L887
- workmux runtime state:
  https://github.com/raine/workmux/blob/0c6bedc55ceac499997438cca57a72139f116047/src/state/types.rs#L73-L142

## Shared coordination space

Read:

```text
.gitignored/tws-tesserasessions/README.md
.gitignored/tws-tesserasessions/status/tws.yaml
```

Write only:

```text
.gitignored/tws-tesserasessions/status/tss.yaml
```

Send replies as new immutable files under:

```text
.gitignored/tws-tesserasessions/messages/
```

Do not edit the tws status file or an existing message.

## Review tasks

1. Confirm or revise the proposed tws/tss ownership boundary.
2. Compare the proposed desired-session and observation fields to the current
   tss core model and status schema.
3. Identify which existing `attach`, `open`, `send`, `read`, and `run`
   behavior can be exposed through a versioned control contract without
   shell-string ambiguity.
4. Propose the minimum durable provider session reference needed by tws.
5. Define capability negotiation for Herdr, tmux, and later providers.
6. Define timeout, unavailable, stale, incompatible-schema, and partial-error
   behavior.
7. Define process/pane birth identity and PID-reuse protection.
8. Define agent-state provenance and confidence.
9. Decide whether resurrection belongs in control schema v1 or a later
   additive capability.
10. Evaluate workmux only as an optional future tss provider. State what value
    it adds beyond native Herdr/tmux and what stable public contract would be
    required.
11. Identify security risks: shell quoting, tmux target ownership, ambiguous
    path matching, stale provider state, foreign session adoption, and
    destructive close behavior.
12. Recommend repository ownership and tpatch slugs for the ADR, PRD, and
    implementations.

## Expected response

Create one message file with:

- verdict on the boundary;
- accepted points;
- changes requested;
- proposed schema deltas;
- security concerns;
- recommended artifact ownership;
- proposed tpatch feature slugs and dependencies;
- whether a workmux provider should remain deferred;
- the next action and owner.

Update `status/tss.yaml` with the same verdict and the artifact hashes reviewed.

Do not implement code yet. Do not change the delivered status schema during
this review. Use tpatch only after the joint ADR/PRD scope is accepted.

---
