# Specification: explicit non-agent open surfaces

Issue [#10](https://github.com/jdbencardinop/tesseraworkspaces/issues/10).
Base: `034126331db73522bb98d1f1a1fc08e146a68fe5` (`v1.2.19-rc.2`).

## Problem and public surface

An editor or shell integration should not require changing `agent_command`,
parsing legacy human guidance, launching an agent, or starting a trailing
shell. Keep normal open behavior intact and add:

```sh
tws open <feature> <logical-branch> --print-path
tws open <feature> --feature-dir --print-path
tws open <feature> <logical-branch> --command <executable> -- <literal-args...>
tws open <feature> --feature-dir --command <executable> -- <literal-args...>
```

The separator is optional when the command has no arguments. It is required
for child arguments; options after it belong to the child, not tws.
The executable value is one executable, not a shell command line.

Legal leading-dash selectors use an option-escape delimiter before selectors.
When that first `--` precedes all required selectors, consume those selectors
first; command arguments then require a second `--`:

```sh
tws open --feature-dir --print-path -- -feature
tws open --command nvim -- -feature -logical-branch -- README.md
```

All tws flags precede the option-escape delimiter. A selector that is itself
`--` occupies its required selector position; it is not a child delimiter.

| Target | Path query | Direct command | Tmux command |
|---|---|---|---|
| External logical entry | Verified materialized worktree | Supported | Supported |
| Checkout logical entry | Current attached entry only | Activate, run, safely restore | Activate and own recorded session |
| External feature directory | Resolved feature metadata directory | Supported | Explicit opt-in |
| Checkout feature directory | Resolved feature metadata directory | Supported without branch activation | Refused |

Branch commands retain explicit/global tmux preference resolution.
Feature-directory commands default direct, matching their legacy route.

## Acceptance criteria

1. **Pure mode/argument validation.** New modes require exactly two explicit
   selectors, or exactly one feature with `--feature-dir`. No picker runs.
   `--print-path` and an explicitly supplied `--command` are exclusive and
   incompatible with enabled `--all`/`--no-agent`. A path query rejects explicit
   tmux launch flags. Command mode rejects contradictory enabled
   `--tmux`/`--no-tmux`, empty executable values and NULs. Only command mode
   accepts child argv after the post-selector `--`; an earlier `--` can escape
   legal leading-dash selectors in either mode. Do not lose that distinction
   in completion. Fail before writer admission, injection,
   activation, session persistence or command launch; emit no success stdout.

2. **Executable binding.** A PATH token or explicit caller-relative path is
   resolved and bound to an absolute executable before writer effects and any
   checkout switch. Do not split the executable on whitespace, re-resolve it
   in the target directory, or ignore `exec.LookPath` errors such as `ErrDot`.
   An unavailable configured agent does not prevent a valid explicit program.
   This binds a path, not immutable executable contents.

3. **Machine-readable observation.** A successful `--print-path` writes
   exactly one absolute verified path and one newline to stdout, with no
   `cd`, banners, decisions, warnings or agent guidance. Errors produce no
   stdout. Refuse ASCII control characters, including DEL, in an output path
   rather than emitting an ambiguous line. The command may use read-only local
   Git probes, but must not fetch, refresh the index, inject, create metadata,
   acquire writer reservations, leave new lock files, recover state, spawn a
   user command, materialize a worktree or switch branches.

4. **Execution-selected namespace.** Resolve the same selected external or
   explicit checkout workspace as execution. Check selected external
   rename/publication evidence before configured-root fallback; check the
   resolved namespace as well. Recheck relevant guards before output/launch.
   A healthy fallback must not hide pending state under the selected root.

5. **Verified external targets.** Reuse `ResolveInjectWorktree` for a selected
   branch, including verified legacy no-stack layouts. Preserve logical/Git
   branch separation, slash names, secondary repositories and safe Git
   registration/backpointer checks. Missing, archived, unsafe or unverified
   targets refuse, without automatic materialization or unrelated worktree
   probes. Ordinary external dirt or detached/manual work is not itself a new
   refusal when existing target and mutation checks permit it.

6. **Honest checkout queries.** Branch queries require an active single-repo
   logical entry whose Git branch is the current attached checkout; return
   the repository root without activation. Inactive, archived, missing or
   cross-repository entries refuse. Feature-directory queries use the resolved
   metadata layout, including supported legacy layouts. A valid current
   session or ordinary dirt alone does not block either pure observation.
   Success is an observation, not a lease or future readiness guarantee.

7. **Retained mutation evidence.** Relevant sync, reparent, rename and scoped
   publication admission remains mandatory. Checkout-wide metadata-move
   evidence still refuses even when the shared mutation lock is absent.
   Extract/reuse the existing mutation-only checks for queries rather than
   imposing launch-only dirty/session restrictions. Never clear evidence to
   make a query or command succeed.

8. **Exactly one direct child.** Direct command mode uses inherited stdio,
   runs in the selected directory, waits for that child and returns. No
   configured agent, Claude continuation flags or subsequent interactive
   shell runs. A nonzero exit uses the existing CLI failure convention
   (normally exit 1), not a global exit-code redesign. Preserve literal empty
   arguments, whitespace, quotes, glob characters, shell tokens, newlines and
   option-like arguments. Never echo or persist raw argv.

9. **Renderer and injection compatibility.** Commands preserve the existing
   managed/legacy injection contract and saved renderer selections. Pass the
   configured agent/renderer to managed validation, never the editor command.
   Preserve unmanaged-file/symlink refusal, owned-file safeguards, and pending
   recovery behavior. Config bytes and the next normal agent launch stay
   unchanged. Path queries bypass injection entirely.

10. **External launch ownership.** Retain selected/actual rename admission
    and an appropriate entry or feature-wide launch intent through the final
    mutation check, injection/launch effects and the direct child lifetime or
    tmux attachment. Keep token-owned direct records where the target is a
    logical branch. Late admission failures must prevent child execution and
    must not remove foreign records or intent.

11. **Truthful observation without a schema bump.** Add a recognized
    `command` stage using the existing session schemas. Generic external
    direct records omit `Agent`; generic checkout state and public
    observations must not imply a configured agent ran. Live presence remains
    visible to status and lifecycle guards; semantic `agent_state` stays
    `unknown`. Preserve redaction and all existing document keys. A
    feature-directory direct launch remains protected by its feature/workspace
    intent rather than inventing a branch identity.

12. **Checkout admission and activation.** Branch commands preserve one-owner
    exclusion, clean/attached/in-progress Git preconditions, branch
    activation, context links and safe restoration. Feature-directory direct
    commands do not switch branches and use the existing feature-directory
    preconditions, not an unrelated clean-checkout requirement.

13. **Durability and cleanup.** Persist owned checkout command state before
    the child can produce effects, including tmux commands that exit quickly.
    Verify generic-command record/lock ownership before restoration or close
    effects and every pre-publication failure release, not only after deleting
    state. Changed ownership is retained even when its token is unchanged.
    Safe completion restores the
    caller and removes only owned state. Dirty, detached, changed-branch,
    changed-ownership or otherwise unsafe restoration retains work/evidence
    and returns actionable failure. Report cleanup errors; never discard work,
    silently replay the command, auto-stash, or remove foreign state.

14. **Literal tmux launch.** Support the matrix above without `send-keys`
    interpolation. A fixed `/bin/sh -c 'exec "$@"' tws-command` bridge is
    permitted with executable and all user values only in positional argv.
    Preserve unusual executable paths and zero-argument invocations as well
    as multi-argument commands. This is not permission to build shell source
    from user input. Escape tmux's command separators and its `-s`/`-c` format
    expansion separately: legal names/directories containing `#{...}` or
    `#(...)` must not change identity/cwd or launch a format shell job. Keep
    child argv literal rather than applying name/directory format escaping to it.

15. **Existing and uncertain tmux sessions.** A supplied command must not
    silently attach to, replace, or kill an existing session. Reject obvious
    collisions before activation/injection and recheck at admission. After an
    uncertain creation failure, do not assume ownership of a remaining
    session. Retain evidence and require explicit inspection when necessary.
    An attach failure with a live owned command retains its session. A command
    that finishes before/during attachment uses only safe owned restoration;
    never perform an unjournaled rollback under possible dirty work. A detach
    keeps a live checkout session owned. Do not claim reliable forwarding of
    an eventual tmux child exit code.

16. **Lifetime limitations.** Track the direct child/existing tmux transport,
    not self-daemonized descendants. Document GUI wait flags and the existing
    lack of portable process-birth identity. Do not claim arbitrary-writer
    isolation or parent-shell cwd changes.

17. **Legacy and completion preservation.** Without enabled new modes, keep
    all prior picker, positional parsing, `--all`, `--feature-dir`,
    `--no-agent`, configured tmux, agent continuation, direct agent-to-shell
    and existing tmux-attach behavior. Completion after the command separator
    must not offer tws feature/branch candidates or execute a program.
    Ordinary completion remains compatible.

18. **Documentation and delivery.** Update help, README, cheatsheet,
    configuration guidance and embedded worktree/orchestrator/Copilot skills
    with machine query versus legacy guidance, literal argv, cwd, mode matrix,
    wait flags and recovery limitations. Record verified Node 24/fixture
    maintenance in the changelog/handoff and make this the current independent
    target, without claiming this feature's future RC already passed.

## Implementation plan

Keep legacy `open.go` routing intact behind an explicit-mode dispatch and pure
validation. Add a small CLI helper beside it for target resolution, read-only
queries and per-invocation command routing. Reuse `feature_rename.go`,
`managed_template.go`, `direct_open.go` and `ResolveInjectWorktree`.

Add only the narrow direct/checkout command lifecycle helpers needed around
`internal/session.go` and the existing direct persistence/runner interfaces.
Keep old agent methods compatible. Add the command stage to
`internal/direct_session.go` and `internal/agent_status.go` as needed; do not
introduce a separate session protocol.

Use focused tests alongside the existing direct-open, checkout-session,
injection and status tests, with ordinary real temporary repositories.
Do not consume reserved reparent matrix leaf budgets or weaken old assertions.
The parent-owned CLI harness independently checks both workspace modes,
literal argv, actual child lifetime/status, no-write path snapshots and an
isolated real tmux server/PTY. Focused failure injection covers late guards,
state persistence/ownership, early exits and safe cleanup.

## Required validation

After focused tests and actual CLI checks, certify the frozen candidate with:

```sh
go test ./... -count=1 -timeout 40m
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Format changed Go files. Independently review code quality/safety,
architecture/correctness/tests, and CLI UX against the same candidate.
Record the exact product paths; normalize exact recipe preimages; replay in
two fresh exact-base worktrees and independently replay the canonical patch.
Land one feature commit with binding trailers, rerun full gates and actual
committed-binary checks, then require main CI before an immutable new RC tag.
Verify both tag versions, release-action success and non-draft prerelease
metadata before closing #10. Preserve stable v1.2.18 and all earlier tags.

Known inherited tpatch parent-verifier limitations must remain explicit;
they are not permission to omit exact candidate replay or runtime validation.

## Non-goals

No provider or session-control integration, schema redesign, birth-identity
guarantee, global editor setting, command history, new command `--all` fan-out,
automatic materialization, historical layout repair, checkout metadata-move
recovery UX, historical tpatch repair, or new stable release.
