# Independent review: open-worktree-command

Reviewed base: `034126331db73522bb98d1f1a1fc08e146a68fe5`.
Candidate manifest SHA-256:
`33d4d39e7b6601314efd3153e486bf92535eae15cace6f8266a493adbc6f7a2d`.
The adjacent `candidate.json` identifies 23 product and four intent images.

| Independent scope | Model | Final outcome |
|---|---|---|
| Code quality and safety | gpt-6-astra | Approved exact candidate |
| Architecture, correctness and tests | gpt-6-astra | Approved exact candidate |
| CLI contract and discoverability | gpt-6-astra | Approved exact candidate |

Each reviewer checked the exact base, all file hashes and modes, including
untracked product files, before and after review. All three reported no
remaining blocking findings on this candidate. They made no product edits.
Approval remains conditional on the separately recorded full test/static
gates, exact recipe/patch replay, landing checks and publication gates.

## Resolved findings

Parent audit corrected literal semicolon handling at tmux's parser boundary,
external intent deletion after rejected ownership, and canonical macOS
fixture path expectations while retaining exact stdout/argv assertions.

The first independent review round additionally found tmux name/cwd format
expansion, pre-publication checkout cleanup deleting changed-owner evidence,
and inaccessible legal leading-dash selectors. The final candidate separately
escapes tmux formats and separators, checks captured ownership before every
pre-publication release, and distinguishes option-escaped selector positions
from the post-selector child delimiter. The same reviewers re-examined these
corrections and the deliberately clarified intent.

Focused checks include ordinary default-temporary-directory tests, native
tmux integration, tagged vet/lint, and independent actual CLI fixtures for
both modes with real Git, direct children and isolated tmux/PTY execution.
No mock substitutes for the native transport's literal-argv/format checks.

## Scope limits

Existing ordinary-sync/checkout-archive admission gaps with already-live
legacy agents were independently reproduced before this feature. They are
separate follow-up work, not a new command-stage regression or a claim that
every current mutator respects an exclusive session reservation.

Historical tpatch parent-verifier limitations remain separately tracked.
No dependency or historical artifact was rewritten to conceal them; exact
candidate replay and all runtime gates remain mandatory.
