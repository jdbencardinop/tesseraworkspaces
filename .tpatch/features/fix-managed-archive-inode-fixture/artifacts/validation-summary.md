# Fixture candidate validation

Baseline: `caee4aa4aaec7154d35c3e5ffe29faf6de3a146a`.
Candidate:
`bd5784ff5415c21885c2675ef1c5176e891c87ef5471cefdb776a9c2c43d76d7`.
Scope: one test file and four intent documents.

The actual Ubuntu failure is retained in
[tag run 41196873802](https://github.com/jdbencardinop/tesseraworkspaces/actions/runs/41196873802).
It was not dismissed by the forty successful local baseline repetitions and
the workflow was not rerun for luck.

The final explicit no-follow retention passed one targeted run and forty
repetitions. The earlier `os.Link` attempt failed the added macOS precondition;
that attempt is not the recorded candidate. All original behavioral assertions
remain intact.

`go test ./... -count=1 -timeout 40m` passed in 1309.191 seconds. Vet, full lint
with an isolated cache, build, formatting, diff and DAG checks passed. The
actual CLI test package also cross-compiled for Linux/amd64; this is a compile
check, not a claim of Linux runtime execution.

Canonical patch:
`cba120fe1c0d79ff5c7c98f4180fc78faafb47fc689121d30389c9830b3d0904`.
Automatically generated recipe:
`edf4b2ad6e4788b7c1b9c16b58da609f9148df376894454360d056948d9d46c5`.
Normalized exact-preimage recipe:
`6756cc195a29d12dd1bf69ee02896a3da3de4e1f9e0c5d7ac01f065808f2b40e`.
The original generation receipt remains unchanged.

Two fresh exact-base worktrees replayed the normalized recipe and each verified
the one existing preimage and final postimage. Canonical patch replay matched
the same postimage through a private index. The direct bounded review found no
blocking candidate issue.

Fresh verification of the already-published `named-feature-templates` parent
failed before this implementation was imported: its historical injection-parent
patch is absent from that verifier baseline, and later features have changed
its whole-file postimages. Preserve that inherited maintenance limitation;
do not rewrite shipped parent artifacts to hide it.

Post-land gates and fresh main/new-RC Ubuntu/macOS CI are still required.
Failed `v1.2.19-rc.1` remains immutable; neither this summary nor the successful
local checks declare the replacement RC published.
