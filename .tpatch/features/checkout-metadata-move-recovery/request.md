# Feature Request: Add evidence-bound recovery UX for interrupted checkout rename and layout migration without discarding movement or registry evidence.

**Slug**: `checkout-metadata-move-recovery`
**Created**: 2026-10-30T22:20:06Z

## Description

Add evidence-bound recovery UX for interrupted checkout rename and layout migration without discarding movement or registry evidence.

## Current limitation

The shipped checkout rename and one/all layout migration paths retain
`.tws/state/.checkout-metadata-<token>` evidence when directory movement cannot
be safely completed or compensated. Pending or untrusted evidence blocks
mutations and session launches even if the shared mutation lock is missing.
Reconciliation is currently manual. This is a separate recovery-UX follow-up,
not an unfinished release or permission to delete the evidence.

## Requested scope

- Provide read-only inspection of the recorded operation, workspace identity,
  source/destination bindings, current directory ownership, registry evidence,
  and the reason recovery is blocked.
- Define explicit operator-approved recovery actions only where fresh evidence
  proves safe completion or restoration. Command names and supported recovery
  cases belong in the subsequent analysis and specification.
- Preserve the shared mutation/session exclusion through recovery. Revalidate
  exact owned objects and registry state before each effect; retain evidence
  on foreign changes, ambiguous ownership, unsupported state, or another
  interruption.
- Retire only exact owned recovery artifacts after coherent layout/registry
  completion or verified restoration. Deleting a marker or stale-looking lock
  must never substitute for proving recovery.
- Cover rename and single/batch migration with real temporary repositories,
  fault injection, repeat recovery, missing-lock cases, and foreign collisions.
  Update operator guidance and embedded skills when behavior is implemented.

## Boundaries and dependency

The hard parent `scoped-push-selection` introduced the independently typed
`tws-checkout-metadata-moves` evidence and its admission checks. Reuse that
ownership contract without treating metadata movement as publication, sync,
or reparent recovery. Publication continuation/cancellation is not the recovery
interface for these records.

Do not add automatic recovery to ordinary commands, weaken existing refusal
guards, discard foreign state, roll back remote refs, or repair arbitrary older
external layouts. Keep this feature separate from historical tpatch artifact
repair and from release-handoff documentation cleanup. Registration does not
start implementation; analysis, specification, exploration, and independent
review are still required.
