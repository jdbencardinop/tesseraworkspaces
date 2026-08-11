# Exploration — agent-work-status-dashboard

Implementation map for **one isolated implementer**. Every path, symbol and line below was read from
the working tree at `4f12d85` (baseline verified clean: `go build ./...`, `go vet ./...`,
`gofmt -l internal cmd assets` — all silent). Line numbers are *locators*; anchor on the symbol name,
they drift as edits land.

This does **not** redesign approved behaviour. `spec.md` is normative. Where the spec cites a line
number or a helper that does not exist as described, §14 records the correction; where a spec detail
is unsafe or untestable as written, §17 records it explicitly.

Net-new surface confirmed: `git grep -n 'statusCmd\|agent_status\|runtime_presence\|DirectSession'`
matches nothing outside `internal/session_test.go:131` (`TestDirectSessionRestores`, an unrelated
checkout-session test name). No `status` entry exists in `rootCmd.AddCommand`
(`internal/cli/root.go:23-50`).

---

## 1. Ground truth — verified symbol/line index

### 1.1 `internal/session.go` (741 lines)

| Symbol | Line | Note for this feature |
|---|---|---|
| `checkoutSessionSchema = 1` | 18 | precedent for `directSessionSchema`, `agentStatusSchema` |
| `CheckoutAgentSession` | 32-49 | `LockToken string \`json:"lock_token"\`` at :47 — **never serialize this struct** |
| `sessionLockOwner{Token,PID,CreatedAt}` | 51-55 | `Token` is the *same secret*; project `PID`/`CreatedAt` only. `Token` may be **read** solely to evaluate `Token == ""` for `session-lock-invalid` (§5.6, §7.1) |
| `SessionAgentRunner` / `SessionShellRunner` / `SessionTmuxRunner` | 57-66 | seam precedent for `directRunner` |
| `Real*Runner` impls | 77-113 | |
| `sessionStateDir` / `sessionStatePath` | 115 / 116 | `<MetadataRoot>/state/sessions/active.json` |
| `sessionLockDir` / `sessionLockOwnerPath` | 117-119 / 120-122 | `<MetadataRoot>/state/checkout-session.lock[/owner.json]` |
| `CheckoutAgentSessionName` | 124-133 | body to extract into `hashedSessionID` |
| `sanitizeSessionPart` | 135-146 | reuse verbatim; **not** the tmux sanitizer |
| `LoadCheckoutAgentSession` | 154-164 | collapses ENOENT and parse failure into one error |
| `atomicSessionWrite` | 169-195 | temp `.tmp-session-*` → Chmod → Write → Sync → Close → Rename |
| `acquireAgentSessionLock` | 197-247 | token scheme at :235-240 (`rand.Read(16)` + `hex`) |
| `processAlive` | 263-269 | bare `Signal(0)`; ESRCH and EPERM both → `false` |
| `sessionDirty` | 533-536 | `(bool, error)`, `cmd.Dir`-based |
| `OpenCheckoutDirect` | 549-606 | `Stage:"agent"` at :583, `Stage="shell"` at :599, `PID: os.Getpid()` |
| `OpenCheckoutTmux` | 608-672 | `Stage:"tmux"` at :657 |
| `CloseCheckoutSession` | 674-693 | refusal string `direct checkout session is still active` at :691 |
| `finishCheckoutSession` / `restoreCheckoutSession` | 695-702 / 704-730 | dirty tree blocks restore at :705-711 |

Complete stage vocabulary today: **`agent`**, **`shell`** (direct), **`tmux`**. Three values, bare
strings, no constants. `starting` is new and external-only.

### 1.2 `internal/checkout_health.go` (994 lines)

| Symbol | Line |
|---|---|
| `ProcessChecker` / `TmuxChecker` | 16-18 / 21-23 |
| `realProcessChecker.Alive` | 27 |
| `realTmuxChecker.HasSession` | 29-33 |
| `CheckoutSeverity` + `SeverityOK/Info/Warning/Error` | 38-45 |
| `CheckoutHealthReport.FilterFeature` (`feature not found: %s` at :164) | 156-179 |
| `CheckoutHealthOpts` / `defaultOpts` | 185-188 / 190-196 |
| `buildHeader` | 244-263 |
| `healthCurrentBranch` (detached ⇒ short SHA + `true`) | 266-281 |
| `gitDirty` | 283-289 |
| `gitActiveOp` (`rebase\|merge\|cherry-pick\|revert`) | 291-323 |
| `buildSyncReports` | 326-349 |
| `buildOneSyncReport` | 351-423 |
| `buildSessionReport` | 426-493 |
| `buildFeatureEntries` (skips a feature whose `LoadStack` fails, :523) | 496-531 |
| `buildOneFeatureEntry` (session-current at :546-550, base loop :552-563, cross-repo :566-571) | 533-615 |
| `gitRefExists` | 617-619 |
| `severityIcon` (`[ok] [i] [!] [E] [ ]`) | 840-853 |
| `BuildCheckoutList` | 869-937 |

### 1.3 `internal/cli/open.go` (385 lines)

| Anchor | Line |
|---|---|
| checkout `--all` rejection (`--all not supported in checkout mode`) | 45 |
| checkout `--feature-dir` guard (`ws.MetadataRoot`) | 56 |
| **call site 1** — checkout `--feature-dir` `openDirect(fp)` | 68 |
| external `--all` guard + `openAll(args[0])` | 81 / 84 |
| external `--feature-dir` guard + comment "Guard before openDirect, which has no error channel" | 95 (comment at 94) |
| **call site 2** — external `--feature-dir` `openDirect(path)` | 107 |
| external per-branch guard | 115 |
| `featurePath := internal.FeaturePath(feature)` (reuse for records) | 131 |
| tmux preference resolution (`--tmux` / `--no-tmux` / `cfg.UseTmux`) | 160-171 |
| stale-tmux warning + `session := sanitizeSessionName(feature+"/"+branch)` | 176-179 |
| **call site 3** — external per-branch `openDirect(path)` | 181 |
| `func openDirect(path string)` | 239-279 |
| claude `-c` continuation | 243-246 |
| `LookPath` failure → `fmt.Printf` + `os.Exit(1)` | 248-251 |
| `Opening: %s\nRunning: %s\n` | 253 |
| agent `exec.Command` block | 256-264 (`Agent exited: %v` at 263) |
| shell block, `Dropped into shell at: %s` | 266-279 (print at 272) |
| `openAll` (`featurePath` at :286, orchestrator window `-c featurePath` at :303, window names at :312) | 283-321 |
| `openWithTmux` | 323-348 |
| `sessionExists` | 350-354 |
| `sanitizeSessionName` (`.`→`_`, `:`→`_`, `/`→`-`) | 356-359 |
| `isClaudeAgent` (`strings.Fields(cmd)[0]`, panics on a blank command) | 361-364 |

**call site 4** — `internal/cli/add.go:105`, inside `addExternal`, `openDirect(path)` after
`createWorktree(feature, newBranch, base, "", force)`.

### 1.4 Other verified anchors

| Anchor | Location |
|---|---|
| `closeCmd` "Deliberately guard-free" comment | `internal/cli/close.go:38-43` (comment body starts :39) |
| external close two-arg check / tmux name / `no tmux session found` / kill / `Closed tmux session:` | `close.go:59-62 / 67 / 69-71 / 73-75 / 77` |
| `TmuxSessionName(feature, branch)` (package `cli`) | `close.go:83-86` |
| `runCheckoutClose` | `internal/cli/checkout_close.go:9-15` |
| `archiveExternal` guard | `internal/cli/archive.go:86`; `featurePath`/`path` :90-91; worktree-absent early branch (also writes `stack.yaml`) :93-107; `git worktree remove` :110; tmux kill :127-130 |
| `renameFeatureCmd` (mode-agnostic!) — `BeginSpacesFeatureRename` :57, collision check :63, `os.Rename` :65, rollback :70 | `internal/cli/rename.go:24-80` |
| `renameBranchCheckout` :123-193 · `renameBranchExternal` guard :196, `HasBranch` check :207-209, `git worktree remove` :231 | `rename.go:123 / 195-283` |
| `deleteExternal` — `BeginSpacesFeatureDelete(internal.TwsRoot(), …)` :150, existence check :156, branch deletion :167-188, worktree removal loop :190-195, `os.RemoveAll` :197 | `internal/cli/delete.go:147-205` |
| `Execute()` maps any `RunE` error to exit 1 | `internal/cli/root.go:52-56` |
| degraded `Workspace{MetadataRoot: wsRoot, Mode: ModeExternal}` (empty `RepoRoot` **and** `StableID`) | `internal/cli/list.go:20-29` |
| `No features found. Use 'tws add <feature>' to create one.` | `list.go:41` |
| `list.go` passes `entry.Name` to `CheckWorktreeBranch` (pre-existing bug, **N7 — do not fix**) | `list.go:81` |
| `RequireWorkspace` | `internal/workspace.go:440-465` |
| `inferExternalRepoRoot` (two error texts) | `workspace.go:339-393` |
| `canonicalize` / `metadataRootExists` / `stableID` / `Workspace.WorktreePath` (`""` in checkout) | `workspace.go:205 / 334 / 72 / 311-316` |
| `ListFeaturesResolved` (**swallows every `os.ReadDir` error**: :151, :163, :175) | `internal/resolve.go:139-186` |
| `isReservedDir` (skips any `.`-prefixed name) | `resolve.go:207-209` |
| `Workspace.CheckoutStateDir()` | `resolve.go:285-287` |
| `GuardFeatureName(root, feature)` → `validateFeatureName` → `SpaceDirOwners` → `ErrSpaceNameConflict` | `internal/spaces.go:676-707` |
| `UnreadDecisions(featurePath, branch) []Decision` | `internal/decisions.go:161-176` |
| `LoadSyncState` / `SyncStatePath` (`<featurePath>/.sync-state.yaml`) | `internal/syncstate.go:23-32 / 19-21` |
| `CheckoutTransaction`, `CheckoutStage`, `FailureKind`, `LockInfo`, `ReadCheckoutLock`, `isProcessAlive` | `internal/checkout_sync.go:62-88, 19-31, 34-45, 167-170, 277-287, 289-296` |
| `IsPrunableWorktree` (**no `-C`**, inherits cwd) | `internal/exec.go:201-221` |
| JSON convention (nil→`[]`, `NewEncoder(cmd.OutOrStdout())`, `SetIndent("", "  ")`) | `internal/cli/registry.go:74-83`; `internal/cli/space.go:166-175`; width-computed columns `space.go:192-205` |
| `tws export` walks only `injectDir` | `internal/cli/export.go:151-153` |

---

## 2. Package-direction constraints (non-negotiable)

- `internal` **cannot** import `internal/cli`. Therefore `cli.TmuxSessionName` / `cli.sanitizeSessionName`
  are unreachable from the builder ⇒ **§3 `internal/tmux_names.go` is mandatory**, not optional.
- The builder needs `sessionStatePath`, `sessionLockDir`, `sessionLockOwnerPath`, `gitRefExists`,
  `healthCurrentBranch`, `gitDirty`, `gitActiveOp`, `buildOneSyncReport`, `canonicalize` — **all
  unexported in package `internal`** ⇒ `agent_status.go` and `direct_session.go` live in `internal`.
- `internal/cli` writes the records (`openDirect`) and reads them (`close`, `rename`, `archive`,
  `delete`) through the **exported** `internal.*DirectSession*` API only.
- Do **not** fork a second liveness or sanitizer implementation. One `Probe`, one sanitizer.

---

## 3. `internal/tmux_names.go` (new, package `internal`)

**Exactly three functions, exactly these spellings.** No alternatives, no second sanitizer:

```go
// SanitizeTmuxName is the body of internal/cli/open.go:356-359, moved verbatim.
func SanitizeTmuxName(s string) string {
    r := strings.NewReplacer(".", "_", ":", "_", "/", "-")
    return r.Replace(s)
}
func ExternalTmuxSessionName(feature, name string) string { return SanitizeTmuxName(feature + "/" + name) }
func ExternalFeatureTmuxSessionName(feature string) string { return SanitizeTmuxName(feature) }
```

`SanitizeTmuxName` is **exported** (spec §13.3 spells it `sanitizeExternalSessionName`, unexported —
§14 records the correction) because `internal/cli/open.go:312` sanitizes a bare *branch* name for a
tmux **window** name, which is neither of the two semantic wrappers, and `cli` cannot reach an
unexported `internal` symbol.

`cli` delegations (byte-identical output is a hard requirement — 11 existing call sites depend on it):

| Existing caller | Change |
|---|---|
| `open.go:356-359 sanitizeSessionName` | body becomes `return internal.SanitizeTmuxName(s)` — one line, function kept so the 10 other `cli` call sites are untouched |
| `close.go:84-86 TmuxSessionName` | body becomes `return internal.ExternalTmuxSessionName(feature, branch)` |
| `open.go:176`, `open.go:292`, `open.go:312` (window name), `open.go:326`, `close.go:67`, `archive.go:127`, `list.go:74`, `new.go:156`, `rename.go:271` | unchanged source; they keep calling `sanitizeSessionName` |

The builder uses **only** `ExternalTmuxSessionName` / `ExternalFeatureTmuxSessionName` (§6.2); it
never re-implements the joining or the replacement set.

Golden test: `ExternalTmuxSessionName("a","b") == "a-b"` and `ExternalFeatureTmuxSessionName("a-b") == "a-b"`
— the documented collision (spec §6.4) must be *demonstrated*, not fixed. A second golden asserts
`cli.TmuxSessionName(f,b) == internal.ExternalTmuxSessionName(f,b)` for the table of existing inputs
(`.`, `:`, `/`, empty branch) so the delegation cannot drift.

---

## 4. `internal/direct_session.go` (new, package `internal`)

### 4.1 Constants, schema, layout

```go
const directSessionSchema = 1
// <featurePath>/.sessions/<branch-id>/<token>.json      0700 / 0700 / 0600
```

`DirectSessionRecord` exactly as spec §10.3 (on-disk `omitempty` is fine; it is not the public
contract). `GitBranch` comes from `StackEntry.GitBranch()` and is **not** derivable from the path.

### 4.2 Identity helper — extract, do not duplicate

`internal/session.go:124-133` becomes:

```go
func hashedSessionID(identity, prefix string) string {
    sum := sha256.Sum256([]byte(identity))
    suffix := hex.EncodeToString(sum[:4])            // 8 hex chars
    p := sanitizeSessionPart(prefix)
    if max := 64 - len(suffix) - 1; len(p) > max { p = p[:max] }   // max == 55
    return p + "_" + suffix
}
func CheckoutAgentSessionName(workspaceID, feature, name string) string {
    return hashedSessionID(workspaceID+"/"+feature+"/"+name, workspaceID+"_"+feature+"_"+name)
}
func DirectSessionBranchID(feature, name string) string {
    return hashedSessionID(feature+"/"+name, feature+"_"+name)
}
```

Golden test pins `CheckoutAgentSessionName("ws","feat","name")` to its pre-refactor value
(criterion 64). Capture the value **before** editing: `go test` a temporary assertion, or compute
`sanitizeSessionPart("ws_feat_name") + "_" + hex(sha256("ws/feat/name")[:4])`.

### 4.3 API surface (spec §10.4) — signatures to implement verbatim

```go
type DirectRecordState string // "ok" | "invalid" | "unsupported"
type DirectSessionIdentity struct{ Feature, Name string }
type LoadedDirectSession struct {
    Record   DirectSessionRecord
    File     string
    BranchID string
    State    DirectRecordState
    Problem  string
}
type DirectSessionTarget struct { BranchID string; Want *DirectSessionIdentity }

func DirectSessionsDir(featurePath string) string
func DirectSessionBranchID(feature, name string) string
func CreateDirectSession(featurePath string, rec DirectSessionRecord) (token string, err error)
func UpdateDirectSession(featurePath, branchID, token string, mutate func(*DirectSessionRecord)) error
func LoadDirectSessions(featurePath, branchID string, want *DirectSessionIdentity) ([]LoadedDirectSession, error)
func ListDirectSessions(featurePath string) (map[string][]LoadedDirectSession, error)
func RemoveOwnedDirectSession(featurePath, branchID, token string) error
func GuardDirectSessionsFor(featurePath string, targets []DirectSessionTarget, proc ProcessProber) (blocking, stale []LoadedDirectSession, err error)
func RemoveStaleDirectSessions(featurePath string, stale []LoadedDirectSession) (removed int, err error)
```

### 4.4 Write/validate rules

| Operation | Rule |
|---|---|
| `CreateDirectSession` | 16 random bytes → hex token (mirror `session.go:235-240`); `MkdirAll` both dirs `0700`; set `SchemaVersion`, `OwnerPID: os.Getpid()`, `Stage:"starting"`, `StartedAt == UpdatedAt == time.Now().UTC().Format(time.RFC3339)`; refuse if destination exists; write via `atomicSessionWrite(path, data, 0600)` |
| `UpdateDirectSession` | re-read, refuse on `Record.Token != token`, apply `mutate`, refresh `UpdatedAt`, atomic rewrite. **Return the raw `fs.ErrNotExist`-wrapping error unchanged** — `openDirect` step 7 branches on `errors.Is(err, fs.ErrNotExist)` |
| `RemoveOwnedDirectSession` | re-read; unlink only if recorded `token` matches; then best-effort `os.Remove(<branch-id>)` and `os.Remove(.sessions)`, ignoring `ENOTEMPTY`/`ENOENT`. Never `RemoveAll`, never a sweep |
| `atomicSessionWrite` reuse | it `MkdirAll(dir, 0700)` itself (`session.go:170`) — matches the required mode, so no extra chmod dance |

Read validation, in order (spec §10.5): `ReadDir` ENOENT → `(nil, nil)`; skip non-regular and any
name not matching `^[0-9a-f]{32}\.json$` (this also excludes `atomicSessionWrite`'s
`.tmp-session-*`); `ReadFile` ENOENT → skip silently; other read error → `invalid`; parse error →
`invalid`; `SchemaVersion > directSessionSchema` → `unsupported`; `Token != filename stem` →
`invalid("token mismatch")`; `want != nil` and identity differs → `invalid("identity mismatch")`;
`OwnerPID <= 0` → `invalid`. An invalid record never aborts sibling enumeration.

`ListDirectSessions` = for each `<branch-id>` dir, `LoadDirectSessions(fp, id, nil)`. It never
fabricates an identity.

`GuardDirectSessionsFor`: `blocking = Probe==live || Probe==unknown || State != ok`;
`stale = State == ok && Probe == dead`. This is deliberately stricter than `close` (§8.3).

### 4.5 Per-invocation token cleanup — ownership matrix (spec §10.7)

| Actor | Creates | Removes | Rule |
|---|---|---|---|
| `openDirect` owner | its token file | its token file | token match, one file |
| `tws status` | — | **nothing**, ever | strictly read-only, even for dead records |
| `tws close` (external) | — | provably dead only | token match, one at a time |
| `tws rename`/`archive`/`delete` (external) | — | provably dead only, after refusing on live/unknown/invalid | token match |

---

## 5. `internal/agent_status.go` (new, package `internal`)

### 5.1 Declaration order (one file, top to bottom)

1. `const agentStatusSchema = 1`
2. `type RuntimePresence string` + `PresencePresent/Absent/Stale/Unknown`
3. `type AgentState string` + `AgentStateWorking/Ready/Blocked/Done/Unknown` — **separate Go type**,
   with the doc comment stating that tss `ready` is *not* tws `idle`
4. `type AttentionStatus string` + `AttentionNeedsAttention/Active/Idle`
5. **Issue-code constants — one per row of spec §7.3, exhaustive and closed** (§5.2)
6. Report structs (§5.3)
7. `AgentStatusOpts{Proc ProcessProber; Tmux TmuxInventoryProbe; Now func() time.Time}` + defaulting
   mirroring `defaultOpts()` (`checkout_health.go:190-196`)
8. `ResolveStatusWorkspace`, `BuildAgentStatus`, `FilterFeature` (§5.7), `RollupAttention`,
   `RollupPresence`, `BuildWorktreeInventory`, `NormalizeAgentStatus`, `FormatAgentStatus`, plus the
   two unexported checkout-session phases `projectCheckoutSession` / `attributeCheckoutSession` (§7.1)

### 5.2 Exhaustive issue-code constants (47 table rows ⇒ **45 unique constants**)

Workspace (15 rows): `workspace-degraded` · `session-orphan-lock` · `session-state-invalid` ·
`session-state-unsupported` · `session-lock-missing` · `session-lock-invalid` ·
`session-unattributed` · `session-stage-unrecognized` · `session-workspace-id-mismatch` ·
`repo-dirty` · `repo-dirty-blocking` · `repo-detached` · `repo-git-op` · `tmux-missing` ·
`tmux-unverifiable`.

Feature (12 rows): `stack-missing` · `stack-invalid` · `sync-in-progress` · `sync-stale` ·
`sync-invalid` · `sync-failed` · `sync-state-present` · `sync-state-invalid` ·
`direct-record-orphan-branch` · `direct-record-dir-unreadable` · `tmux-path-mismatch` ·
`tmux-panes-unverified`.

Entry (20 rows, 18 unique constants): `worktree-missing` · `worktree-prunable-missing` ·
`worktree-unreadable` · `worktree-wrong-branch` · `worktree-dirty` · `worktree-dirty-blocking` ·
`ref-missing` · `ref-missing-archived` · `cross-repo-unsupported` · `direct-record-stale` ·
`direct-record-unknown` · `direct-record-invalid` · `direct-record-unsupported` ·
`session-owner-dead` · `session-owner-unknown` · `session-tmux-gone` · `sync-failed-branch` ·
`sync-current-branch` · plus `tmux-path-mismatch` and `tmux-panes-unverified`.

`tmux-path-mismatch` and `tmux-panes-unverified` are **one constant each**, emitted at feature scope
(the `--all` session) and entry scope (the per-branch session). Declare a
`var allAgentStatusIssueCodes = []string{…}` slice beside the constants so the closure test
(criterion 71) enumerates declarations rather than reflecting over strings.

Deleted-by-spec codes that must appear **nowhere** in `internal/`: `sync-lock-invalid`,
`feature-tmux-unknown`, `ancestry-*`.

### 5.3 Struct set and the null discipline (spec §8.5)

No `omitempty` anywhere in these structs. Nullable scalars are pointers (`*int`, `*bool`, `*string`).
Every slice is normalized to `[]` in `NormalizeAgentStatus` before encode *and* before format.

```
AgentStatusReport{ SchemaVersion int; GeneratedAt string; Workspace AgentStatusWorkspace;
                   Features []AgentStatusFeature; Issues []AgentStatusIssue; Summary AgentStatusSummary }
AgentStatusWorkspace{ Mode; StableID *string; RepoRoot *string; MetadataRoot string; Degraded bool;
                      DegradedReason *string; Branch *string; Detached *bool; Dirty *bool;
                      ActiveGitOp *string; Tmux TmuxStatus; CheckoutSession *SessionObservation;
                      RuntimePresence; AgentState; Attention AttentionRollup }
AgentStatusFeature{ Feature; Path; StackState; Sync *AgentStatusFeatureSync;
                    FeatureTmux *SessionObservation; Entries []AgentStatusEntry;
                    RuntimePresence; AgentState; Attention }
AgentStatusEntry{ Feature; Name; GitBranch; Base; BaseGitBranch; Repo *string; Archived bool;
                  IsCurrentCheckout *bool; Materialization EntryMaterialization;
                  Sessions []SessionObservation; SessionCounts; UnreadDecisions int;
                  RuntimePresence; AgentState; Attention; FeatureAttention bool }
AgentStatusIssue{ Code; Severity CheckoutSeverity; Scope; Feature *string; Name *string;
                  Message string; Guidance *string }
SessionObservation{ Kind; Presence; AgentState; Stage *string; StageRecognized bool;
                    OwnerPID *int; ChildPID *int; Liveness *string; TmuxSession *string;
                    Path *string; Agent *string; StartedAt *string; UpdatedAt *string;
                    RecordID *string; RecordState string; Detail *string }
```

`severity` reuses `CheckoutSeverity` (`checkout_health.go:38-45`) minus `ok`; `error` is declared but
unreachable at this version (spec §7.2) ⇒ `summary.errors == 0` always.

### 5.4 Build pipeline — exact order inside `BuildAgentStatus(ws, degradedReason, opts)`

The checkout session is projected in **two separated phases**. Phase A (step 6) needs only workspace
state and therefore runs before any feature is loaded; phase B (step 8) needs the entry index and
therefore runs after every entry exists. Nothing in phase A may look at a stack entry, and nothing in
phase B may re-read the filesystem.

```
 0. default opts (Proc=realProcessChecker{}, Tmux=RealTmuxInventory{}, Now=time.Now)
 1. PRECONDITION (§4.4.1): os.Stat(ws.MetadataRoot) -> must be a dir; os.ReadDir(ws.MetadataRoot).
    Any failure => return nil, fmt.Errorf("workspace metadata root unreadable: %s: %w", root, err).
    No report object allocated. (Required because ListFeaturesResolved swallows ReadDir errors at
    resolve.go:151/163/175 and would return an empty successful list.)
 2. features, err := ws.ListFeaturesResolved()   // hard-fails on untrusted spaces.yaml -> fatal
 3. tmuxSnap := opts.Tmux.Snapshot()             // ONE snapshot per invocation
 4. wtInv := BuildWorktreeInventory(ws.RepoRoot) // ONE `git worktree list --porcelain`; skipped when RepoRoot == ""
 5. workspace header FIELDS, plus the two issues that depend on those fields alone: mode,
    StableID/RepoRoot as *string (nil when ""), MetadataRoot, Degraded/DegradedReason,
    workspace-degraded when degraded, and — only when RepoRoot != "" —
    healthCurrentBranch/gitDirty/gitActiveOp (the last one now also detects bisect, §6.5), from which
    repo-detached (info) and repo-git-op (warning) are emitted in BOTH modes.
    repo-dirty / repo-dirty-blocking is NOT decided here: it depends on whether a checkout session
    record exists (spec §7.4), so it is emitted in step 6.
 6. CHECKOUT SESSION, PHASE A — projection (§7.1), checkout mode only, feature data NOT yet loaded:
      a. the four ordered stats/reads of §7.1 (state file, lock dir, state parse, owner.json);
      b. build at most ONE SessionObservation (presence, stage, stage_recognized, owner_pid,
         child_pid, tmux_session, path, agent, timestamps, record_state, detail) and assign it to
         workspace.checkout_session;
      c. emit every WORKSPACE-scoped issue that this evidence alone determines:
         session-orphan-lock, session-state-invalid, session-state-unsupported,
         session-lock-missing, session-lock-invalid, session-stage-unrecognized,
         session-workspace-id-mismatch, tmux-unverifiable, and — now that record existence is
         known — repo-dirty (info) or repo-dirty-blocking (warning, iff workspace.dirty is true and
         a parseable state record exists);
      d. remember (state.Feature, state.Name) and the pending ENTRY-scoped verdict
         (session-owner-dead | session-owner-unknown | session-tmux-gone | none) as builder-local
         state, TOGETHER WITH an `attributable` flag. Emit NOTHING entry-scoped yet: no entry exists.
         The identity is read only AFTER the parse and the schema check succeed, and `attributable`
         is cleared for a tmux record no tmux inventory could verify: those observations are
         unknown-presence facts the WORKSPACE warning already owns, and homing them on an entry that
         owns no warning would make the entry read `idle` while its runtime is unverifiable
         (§6.7/§7.1 invariant).
    In external mode step 6 is skipped entirely, workspace.checkout_session stays null, and no
    repo-dirty* issue is emitted at all (external dirt is per worktree, §6.4/§7.4).
 7. for each feature (ascending):
      a. path, err := ws.ResolveFeaturePath(feature)  // ErrAmbiguousFeature => FATAL, no document
      b. stack, err := LoadStack(path) -> stack_state ok|missing(ENOENT)|invalid
      c. sync projection (§7.2/§7.3)
      d. external only: recs, _ := ListDirectSessions(path)   // map[branchID][]LoadedDirectSession
      e. external only: feature_tmux observation from ExternalFeatureTmuxSessionName(feature)
      f. for each StackEntry, in stack.yaml order (archived included) -> entry builder, which also
         records the entry pointer in an index keyed by (feature, name)
      g. external only: leftover <branch-id> keys matching no entry AND holding >=1 record -> one
         feature `direct-record-orphan-branch`. A key holding ZERO records is skipped: an empty
         directory is prune residue and can make nothing need attention. A failed
         ListDirectSessions is reported as one feature `direct-record-dir-unreadable`, never
         swallowed.
 8. CHECKOUT SESSION, PHASE B — attribution (§7.1), checkout mode only, after every entry exists:
      a. look up the index for (state.Feature, state.Name);
      b. MATCH  => append the SAME projected observation value to that entry's Sessions, and emit the
         pending entry-scoped issue from 6d on that entry;
      c. NO MATCH, or an EMPTY feature/name on a parsed record => the observation stays
         workspace-only and one workspace `warning` `session-unattributed` is emitted; the pending
         entry-scoped verdict is DISCARDED (there is no entry to home it on, and workspace scope
         already carries session-lock-*/state-* signals).
      c2. NOT ATTRIBUTABLE (unparseable, unsupported schema, unverifiable tmux) => phase B is
         skipped entirely: no entry gains a session, and NO `session-unattributed` is emitted
         either, because the workspace warning that describes the record already exists.
      d. Phase B performs no I/O: every probe result it needs was captured in phase A.
 9. bottom-up rollup: entries -> features -> workspace (RollupAttention, single pass)
10. summary counters
11. NormalizeAgentStatus (nil->[], sorting per §8.6)
```

Consequences the implementer must not "optimize" away: the observation value is built once (step 6b)
and referenced twice (`workspace.checkout_session` and, when attributed, `entry.sessions[]`), so the
two copies can never disagree; and `session-unattributed` is reachable only from step 8c, which is
the only place that knows the stack.

`AgentStatusOpts` has **no `Ancestry` field and no `Cwd` field**. `gitShortSHA`, `gitFullSHA`,
`gitMergeBase`, `gitIsAncestor` are **not** called anywhere in this file (spec §5.6 / criterion 19).

### 5.5 Issue construction — one helper, single home

```go
func (b *statusBuilder) issue(code string, sev CheckoutSeverity, scope string,
                              feature, name *string, message, guidance string)
```

appends to the **one** `report.Issues` slice. Levels never hold copies. `AttentionRollup{Status,
IssueCount, Codes}` is derived at rollup time by filtering `report.Issues` on
`scope + feature + name` equality — own-scope only, `IssueCount` counts `warning|error` only, `Codes`
sorted+deduped. A level with `needs_attention` and `issue_count: 0` is normal and required.

`RollupAttention` body is exactly:

```go
if childNeedsAttention || anyWarningOrError(own) { return AttentionNeedsAttention }
if presence == PresencePresent { return AttentionActive }
return AttentionIdle
```

### 5.6 JSON projection and redaction (spec §9 — enforce at the type level)

| Rule | Enforcement |
|---|---|
| `CheckoutAgentSession.LockToken` never serialized | never embed `CheckoutAgentSession`; project field-by-field into `SessionObservation` |
| `sessionLockOwner.Token` never serialized | when reading `owner.json`, decode into `sessionLockOwner`, immediately reduce it to `pid := owner.PID`, `createdAt := owner.CreatedAt`, `tokenEmpty := owner.Token == ""`, and drop the struct. The token **value** is read for exactly one purpose — the `Token == ""` test that raises `session-lock-invalid` (spec §6.5/§7.3) — and is never assigned to a `SessionObservation` field, never put in an issue `message` or `guidance`, never printed, never logged, and never carried past that statement |
| direct token → `record_id` only | `RecordID = token[:8]` |
| no argv/env/transcript | record stores `Agent = parts[0]` only; observation emits that token |
| `CheckoutAgentSession.Links` not emitted | no `links` key anywhere |
| paths **are** emitted | `repo_root`, `metadata_root`, `feature.path`, `materialization.path`, session `path` |

### 5.7 `FilterFeature(feature)` — what it drops and what it recomputes

Mirrors `CheckoutHealthReport.FilterFeature` (`checkout_health.go:156-179`) in shape, not in detail.
Exact body order:

1. Narrow `report.Features` to the single element whose `Feature == feature`. No match ⇒ return
   `fmt.Errorf("feature not found: %s", feature)` — the same string as `checkout_health.go:164` —
   and leave the report **unmodified** (the caller discards it; `status.go` returns the error).
2. **Drop unrelated scoped issues.** Keep an issue iff
   `iss.Scope == "workspace" || (iss.Feature != nil && *iss.Feature == feature)`. That keeps every
   workspace-scoped issue (spec §4.3: a workspace orphan lock must still be visible under a filter)
   and every feature- and entry-scoped issue of the surviving feature, and drops the rest. Issues
   are the single home (spec §7.2), so a stale `direct-record-stale` for another feature must not
   survive — it would make `summary.warnings` describe a feature that is not in the document.
3. **Recompute `summary`** from the surviving document (spec §7.6): `features == 1`; `entries` and
   the `needs_attention`/`active`/`idle` and `runtime_*` counters from the surviving entries only;
   `issues`/`warnings`/`errors` from the surviving `report.Issues`, workspace-scoped ones included.
4. **Re-derive `workspace.attention`** as
   `RollupAttention(workspace.runtime_presence, ownWorkspaceIssues, survivingFeature.attention.status == needs_attention)`,
   with `ownWorkspaceIssues` taken from the *filtered* slice, and recompute
   `workspace.attention.{issue_count, codes}` from those same issues. Feature- and entry-level
   rollups are **not** recomputed: their own-scope issues all survived step 2, so their values are
   already correct.
5. `workspace.checkout_session` is **kept unconditionally**, even when the session belongs to another
   feature — a deliberate divergence from `CheckoutHealthReport.FilterFeature`, which nils `Session`
   (`checkout_health.go:173-176`). It is workspace state, and spec §4.3 forbids dropping workspace
   state under a filter. The *entry* copy disappears naturally with its filtered-out feature.
6. `workspace.runtime_presence` is **not** recomputed. Presence is a runtime fact about the whole
   workspace, not a property of the document's scope; recomputing it would make `tws status auth`
   report a different runtime than `tws status` for the same instant. Documented consequence: a
   filtered document may read `workspace.runtime_presence: "present"` (and therefore `active`) on
   the strength of a feature that `features[]` no longer contains. A test asserts this exact
   behaviour so it is a decision, not a bug.
7. `NormalizeAgentStatus` runs **after** `FilterFeature`, never before, so the filtered slices are
   still `[]`-normalized and sorted on output.

---

## 6. Probe seams

### 6.1 `ProcessProber` — add to `internal/checkout_health.go` beside the existing seams (:16-33)

```go
type ProcessLiveness string
const ( ProcessLive ProcessLiveness = "live"; ProcessDead = "dead"; ProcessUnknown = "unknown" )
type ProcessProber interface{ Probe(pid int) ProcessLiveness }

func (realProcessChecker) Probe(pid int) ProcessLiveness {
    if pid <= 0 { return ProcessDead }
    p, err := os.FindProcess(pid); if err != nil { return ProcessDead }
    switch err := p.Signal(syscall.Signal(0)); {
    case err == nil:                                return ProcessLive
    case errors.Is(err, os.ErrProcessDone),
         errors.Is(err, syscall.ESRCH):             return ProcessDead
    case errors.Is(err, syscall.EPERM):             return ProcessUnknown
    default:                                        return ProcessUnknown
    }
}
func (r realProcessChecker) Alive(pid int) bool { return r.Probe(pid) == ProcessLive }
```

`checkout_health.go` currently imports `fmt os os/exec path/filepath strings yaml` — add
`errors` and `syscall`. `internal/checkout_sync.go:289-296 isProcessAlive` is **not** touched.
Behaviour of `tws doctor` is unchanged (EPERM was already `false`).

**Adapter needed:** `buildOneSyncReport` takes a `ProcessChecker`, not a `ProcessProber`. Add

```go
type proberAsChecker struct{ p ProcessProber }
func (a proberAsChecker) Alive(pid int) bool { return a.p.Probe(pid) == ProcessLive }
```

so the status builder can reuse `buildOneSyncReport` verbatim with its injected prober.

### 6.2 `TmuxInventoryProbe` — one snapshot per invocation (spec §6.4)

`TmuxPane{Session, Path}`, `TmuxSnapshot{Available, ServerRunning, Sessions map[string]bool, Panes,
PanesAvailable, Err}`, `RealTmuxInventory.Snapshot()`:

1. `exec.LookPath("tmux")` fails ⇒ `{Available:false}`, stop.
2. `tmux list-sessions -F '#{session_name}'` (`CombinedOutput`). Success ⇒ `ServerRunning:true` +
   `Sessions`. Failure whose output contains `no server running` or `error connecting to` ⇒
   `ServerRunning:false`, empty set, `Err:nil`. Any other failure ⇒ `Err` set.
3. `tmux list-panes -a -F '#{session_name}\t#{pane_current_path}'` ⇒ `PanesAvailable` + `Panes`;
   failure is non-fatal.

Path verification uses `canonicalize()` (`workspace.go:205`) on **both** sides; a pane path equal to
or nested under the target counts as a match.

| Session kind | Name source | Verified by |
|---|---|---|
| per-branch `external-tmux` | `ExternalTmuxSessionName(feature, entry.Name)` | name ∈ Sessions **and** a pane path under `<featurePath>/worktrees/<Name>` |
| feature `--all` | `ExternalFeatureTmuxSessionName(feature)` | name ∈ Sessions **and** a pane path under `<featurePath>` (created with `-c featurePath`, `open.go:303`) |
| `checkout-tmux` | recorded `CheckoutAgentSession.TmuxSession`, verbatim | name ∈ Sessions only (already hashed by `CheckoutAgentSessionName`) |

The two normative rules (spec §6.4): **no evidence ⇒ no observation** (missing tmux binary or a
broken inventory emits *zero* per-branch observations and exactly one workspace issue); **evidence we
cannot confirm ⇒ `unknown` + `warning`**.

### 6.3 `BuildWorktreeInventory(repoRoot)` — replaces `IsPrunableWorktree` for status

Single `git -C <repoRoot> worktree list --porcelain`, parsed block-wise (`worktree <path>`,
`branch refs/heads/<name>`, bare `prunable…` line). `RepoRoot == ""` or a git failure ⇒
`Available:false` ⇒ prunability is `null` and an absent non-archived worktree is `missing`
(never `prunable-missing`). `internal.IsPrunableWorktree` (`exec.go:201`) is **left untouched** for
`list.go:76`.

### 6.4 Per-worktree Git probes (external mode) — `healthCurrentBranch` / `gitDirty`

Both helpers are `-C <dir>`-based (`checkout_health.go:266-281`, `:283-289`) and both **swallow their
error**: `healthCurrentBranch` returns `("", false)` and `gitDirty` returns `false`. For external
entries the `dir` is the **worktree path** `wtPath = <featurePath>/worktrees/<Name>`, not
`ws.RepoRoot`. Exact sequence, run only when `materialization.state == "present"` **and**
`ws.RepoRoot != ""` **and** `entry.Repo == ""`:

```
branch, detached := healthCurrentBranch(wtPath)
case branch == "":                 // rev-parse failed inside the worktree
      checked_out_branch = null; dirty = null; state stays "present"
      issue: entry warning worktree-unreadable   (spec §7.3 lists "rev-parse in it failed")
      -> do NOT call gitDirty: its false is indistinguishable from clean
case detached == true:             // branch holds a SHORT SHA, not a branch name
      checked_out_branch = null                  // never compare a SHA to git_branch
      no worktree-wrong-branch                   // §17.3: never a fabricated false
      no detached issue exists at entry scope    // spec §7.3's table is closed; repo-detached is workspace-only
      dirty = gitDirty(wtPath)
default:
      checked_out_branch = branch
      dirty = gitDirty(wtPath)
      if entry.GitBranch() != "" && branch != entry.GitBranch():
            issue: entry warning worktree-wrong-branch
```

`dirty == true` ⇒ `worktree-dirty` (`info`), escalated to `worktree-dirty-blocking` (`warning`) only
when the feature's `.sync-state.yaml` names this entry's **`Name`** in `failed_branch` or `pending`
(§7.3 identity axis). `dirty == null` never produces either.

In **checkout** mode neither helper is ever called per entry: `dirty` and `checked_out_branch` are
`null` for every entry and the single physical checkout is described once by `workspace.branch` /
`workspace.detached` / `workspace.dirty` (spec §5.3, §7.4). In a **degraded** workspace
(`RepoRoot == ""`) neither is called at all — the entry's materialization state is `unknown`.

### 6.5 `gitActiveOp` — add `bisect` detection (one row)

Spec §7.3 defines `repo-git-op` as "`gitActiveOp` reports an in-progress
rebase/merge/cherry-pick/**bisect**", but `gitActiveOp` (`checkout_health.go:291-323`) checks only
`rebase-merge`, `rebase-apply`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`. The trigger is kept
and the detection is added, because it is one row:

```go
{"BISECT_LOG", "bisect"},   // appended last in the existing checks slice, checkout_health.go:304-311
```

Ordering matters only for a repo that is simultaneously mid-rebase and mid-bisect; appending last
keeps every existing verdict byte-identical. This is the **one** intentional behaviour change to a
shared helper: `tws doctor` on a repo mid-bisect now reports `active_git_op: bisect` where it
previously reported nothing. It gets a `CHANGELOG.md` line (§10) and a test that plants
`.git/BISECT_LOG` and asserts both `tws doctor` and `tws status` report it (`revert` stays mapped to
`revert`, not `bisect`). No other `gitActiveOp` caller exists.

---

## 7. Checkout state / lock / sync projection (unexported helpers, no token leak)

### 7.1 Session read order and the two-phase split — mandatory, differs from `buildSessionReport`

**Phase A (pipeline step 6) — projection. Runs before any feature or stack is loaded.**

```
1. os.Stat(sessionStatePath(ws))       // ENOENT vs other
2. os.Stat(sessionLockDir(ws))
3. only if (1) exists: ReadFile + json.Unmarshal into CheckoutAgentSession
4. only if (2) exists: ReadFile(sessionLockOwnerPath(ws)) -> sessionLockOwner, reduced immediately to
   (pid, createdAt, tokenEmpty := owner.Token == ""); the token value goes no further (§5.6)
5. probe: opts.Proc.Probe(state.PID) for Mode=direct, tmuxSnap for Mode=tmux
6. build the single SessionObservation; assign to workspace.checkout_session
7. emit the workspace-scoped issues listed in §5.4 step 6c
8. stash (state.Feature, state.Name) + the pending entry-scoped verdict as builder-local state
```

Do **not** call `LoadCheckoutAgentSession` for the decision: it collapses ENOENT and parse failure
into one error (`session.go:154-164`), which is exactly the bug §12 forbids reproducing
(today a corrupt `active.json` with no lock reports *nothing*). It may still be used *after* the stat
succeeds, but the stat must gate it.

`session-lock-invalid` fires when the lock dir exists and (`owner.json` unreadable **or** `pid <= 0`
**or** `tokenEmpty`). `tokenEmpty` is the **only** use of the owner token anywhere in this feature;
it is a `bool`, and neither the token nor any prefix, hash, or length of it reaches an observation
field, an issue message, stdout, or stderr.

**Phase B (pipeline step 8) — attribution. Runs after every entry exists, performs no I/O.**

```
match := index[(state.Feature, state.Name)]
match != nil  -> append the SAME observation value to match.Sessions
              -> emit the pending entry-scoped issue on match:
                 session-owner-dead | session-owner-unknown | session-tmux-gone | (none)
match == nil  -> observation stays only at workspace.checkout_session
              -> workspace warning session-unattributed; the pending entry verdict is discarded
```

The split is required because attribution needs the stack, while every workspace-scoped row of the
spec §6.5 decision table (orphan lock, state invalid/unsupported, lock missing/invalid,
stage unrecognized, workspace-id mismatch, tmux-unverifiable) must be reportable even when
`LoadStack` fails for the owning feature or the feature no longer exists. Implement the 14-row table
as a single ordered sequence with no `switch` on a compound key, so every row stays reachable, and
keep phases A and B in two separate unexported functions
(`projectCheckoutSession` / `attributeCheckoutSession`) so a test can drive each one directly.

### 7.2 Checkout sync — path derivation trap

**Do not use `CheckoutTransactionPath(featurePath)` / `CheckoutLockPath(featurePath)`.**
`checkoutStateDir` (`checkout_sync.go:92-96`) computes `dirname(dirname(featurePath))/state`, which
is correct only for the new layout `.tws/features/<f>`; for a **legacy** `.tws/<f>` feature it yields
`<repoRoot>/state` — a path that does not exist. Use instead:

```go
stateDir := ws.CheckoutStateDir()                                  // resolve.go:285
txPath   := filepath.Join(stateDir, feature+"-checkout-sync.yaml")
if _, err := os.Stat(txPath); err == nil {
    rep := buildOneSyncReport(feature, txPath, stateDir, proberAsChecker{opts.Proc})
}
```

`buildOneSyncReport` already derives `<feature>-checkout-sync.lock` from `stateDir` internally.
Field mapping is spec §8.3; `failed_branch`, `pending`, `completed`, `skipped` are always
`null`/`[]` for `kind: "checkout"`.

Severity re-derivation: `buildOneSyncReport` sets `SeverityError` for its `invalid` cases; status
maps **every** invalid cause to the single `sync-invalid` code at **`warning`** severity and quotes
the helper's own `Guidance` in `message`. This is deliberate (spec §7.2/§7.3: no baseline `error`).

### 7.3 External sync (`.sync-state.yaml`) and the identity split — critical

| Source | Identity axis | Match against |
|---|---|---|
| external `SyncState.FailedBranch` / `Pending` / `Completed` / `Skipped` | **stack `Name`** (proved by `sync.go:92,117` using `WorktreePath(feature, state.FailedBranch)` and `GetBranch(stack, …)`) | `StackEntry.Name` |
| checkout `CheckoutPlanEntry.Branch` (⇒ `CheckoutSyncReport.CurrentBranch`) | **`GitBranch()`** (proved by `BuildCheckoutPlan`, `checkout_sync.go:459`) | `StackEntry.GitBranch()` |

Attributing either one with the wrong axis silently mis-fires `sync-failed-branch` /
`sync-current-branch` on decoupled entries. A decoupled fixture (`Name: api`, `Branch: jd/api`) must
cover both.

`LoadSyncState` returns an error for both ENOENT and parse failure ⇒ `os.Stat(SyncStatePath(path))`
first, then load; stat-ok + load-fail ⇒ `sync: {kind:"external", liveness:"invalid", …}` plus
`sync-state-invalid`. `SyncState.StartedAt` is deliberately **not** projected.

---

## 8. `internal/cli` — command wiring

### 8.1 `internal/cli/status.go` (new) and `root.go`

- `root.go:23-50`: insert `statusCmd(),` **immediately after `doctorCmd(),`** (currently line 41).
- `statusCmd()`: `Use: "status [feature]"`, `Args: cobra.MaximumNArgs(1)`,
  `ValidArgsFunction` identical in shape to `doctorCmd` (`doctor.go:21-27`) returning
  `internal.ListFeatures()` for position 0 with `cobra.ShellCompDirectiveNoFileComp`.
- Exactly one flag: `cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")`.
- `Short`/`Long` copied verbatim from spec §4.2 (criterion 1 greps for `always covers every feature`
  and `agent_state`).
- `RunE` order: `internal.ResolveStatusWorkspace()` → (filtered run only)
  `internal.GuardFeatureName(ws.MetadataRoot, feature)` → `internal.BuildAgentStatus(...)` →
  `report.FilterFeature(feature)` (§5.7) → encode (`json.NewEncoder(cmd.OutOrStdout())`,
  `SetIndent("", "  ")`) or `fmt.Fprint(cmd.OutOrStdout(), internal.FormatAgentStatus(report))`.
- Returns `nil` for every reportable case; only §9's fatal set returns an error (mapped to exit 1 by
  `root.go:52-56`). **Nothing is written to stdout on the error path.**

Human output (spec §8.8) reuses `severityIcon` (`checkout_health.go:840`) for both the attention
column and issue blocks, computes column widths like `space.go:192-205`, and prints the exact
empty-workspace string from `list.go:41`. Two rules the implementer must not soften: the workspace
`Attention: <glyph> <status>` header line is **unconditional**, and **every** issue in
`report.issues[]` is rendered in a block keyed by its own home — `Branch:` blocks first, then
`Feature:`, then `Workspace:`. Without the `Branch:` blocks an entry-scoped `guidance` would be
reachable only through `--json`, which makes a `[!] attn` row a dead end for a human. The issue
blocks and the branch-scoped tail are printed even for an empty workspace, because a
workspace-scoped fault has no table row to appear on.

### 8.2 `openDirect` refactor + injectable seams

New file `internal/cli/direct_open.go`:

```go
type directProcess interface { PID() int; Wait() error; Terminate() error }
type directRunner  interface { Start(dir string, command []string) (directProcess, error) }
type directSessionStore interface {
    Create(featurePath string, rec internal.DirectSessionRecord) (string, error)
    Update(featurePath, branchID, token string, mutate func(*internal.DirectSessionRecord)) error
    RemoveOwned(featurePath, branchID, token string) error
}
type directOpenOpts struct {
    Path, Feature, Name, GitBranch, FeaturePath string
    Runner, Shell directRunner
    LookPath func(string) (string, error)
    Store    directSessionStore
    Out      io.Writer
}
func openDirect(opts directOpenOpts) error
```

Real `directProcess` wraps `exec.Cmd` with `Stdin/Stdout/Stderr` wired to `os.*` exactly as today
(`open.go:256-263`, `:274-278`). `Terminate` = `SIGTERM` plus a bounded 5s escalation to `SIGKILL`,
armed in a goroutine that selects on a `done` channel `Wait` closes. `Terminate` never waits itself —
`exec.Cmd.Wait` is not safe to call twice, so a self-waiting `Terminate` would race the caller's
reap and a timer that fires after a successful `Wait` would signal a recycled pid. `Wait` is
idempotent (`sync.Once` + cached error), and **the caller reaps**: `openDirect` calls `Terminate()`
then `Wait()` explicitly at the one site that stops a child early (step 5).

Ordering (spec §10.6 steps 1-10) — the only clarifications the implementer needs:

- Step 1 resolves the agent command **in this exact statement order**, all of it before any write:

  ```go
  raw := cfg.GetAgentCommand()
  parts := strings.Fields(raw)
  if len(parts) == 0 {                       // MUST precede isClaudeAgent
      return fmt.Errorf("agent_command is empty; set agent_command in .tws/config.yaml")
  }
  if isClaudeAgent(raw) && hasClaudeSession(opts.Path) { raw += " -c"; parts = strings.Fields(raw) }
  if _, err := opts.LookPath(parts[0]); err != nil { return fmt.Errorf("agent %q not found in PATH", parts[0]) }
  ```

  The blank check must come **before** `isClaudeAgent`, not merely before `parts[0]`: today
  `openDirect` calls `isClaudeAgent(agentCmd)` first (`open.go:243`) and `isClaudeAgent` itself does
  `strings.Fields(cmd)[0]` (`open.go:361-364`), so a whitespace-only `agent_command` panics inside
  `isClaudeAgent` before `open.go:247` is ever reached. `GetAgentCommand` defaults to `"claude"`
  (`config.go:22-27`), so only an explicitly blank configured value reaches this guard.
  Recommended companion hardening (2 lines, behaviour-preserving for every non-blank input): give
  `isClaudeAgent` a `f := strings.Fields(cmd); if len(f) == 0 { return false }` prologue, which also
  disarms the identical trap in the untouched `openWithTmux` (`open.go:337`). The `openDirect` guard
  is normative; the hardening is optional and must not change any non-blank result.
- `os.Exit(1)` at `open.go:248-251` becomes `return fmt.Errorf("agent %q not found in PATH", parts[0])`.
- Printed lines are byte-identical and in the same order: `Opening: %s\nRunning: %s\n` (:253),
  `Agent exited: %v\n` (:263), `Dropped into shell at: %s\n` (:272) — but through `opts.Out`
  (default `os.Stdout`).
- Step 7 (`stage: shell`) has the **asymmetric** two-branch failure handling: `errors.Is(err,
  fs.ErrNotExist)` ⇒ warn, one `Create` retry preserving the original `StartedAt`, adopt the new
  token for step 10, continue **and start the shell either way**; any other error ⇒ do not start the
  shell, `RemoveOwned`, return.
- Step 9 (post-start shell `child_pid`) failure ⇒ **warn on stderr and return nil**. This is the one
  deliberate asymmetry with step 5.

**Tracked vs untracked opens:**

| Call site | File:line | Tracked? | Passes |
|---|---|---|---|
| checkout `--feature-dir` | `open.go:68` | **no** | `Path: fp`, `Feature/Name: ""` — must not touch external state at all |
| external `--feature-dir` | `open.go:107` | **no** | `Path: path`, `Feature/Name: ""` |
| external per-branch | `open.go:181` | **yes** | `Path`, `Feature`, `Name: branch`, `FeaturePath` (already computed at `open.go:131`), `GitBranch` per below |
| `tws add --open` | `add.go:105` | **yes** | `Path: internal.WorktreePath(feature, newBranch)`, `Feature: feature`, `Name: newBranch`, `FeaturePath: root` (`add.go:65`), `GitBranch` per below |

All four become `return openDirect(...)` / `if err := openDirect(...); err != nil { return err }`.
The comments at `open.go:80` and `open.go:94` are rewritten to
*"guarded because the feature name is joined under TwsRoot()"* — the guards themselves stay.

`GitBranch` resolution (spec §10.6, normative): `internal.LoadStack(featurePath)` →
`internal.GetBranch(stack, name).GitBranch()`; **on any failure or missing entry it is `""`** and the
open proceeds. Note `addExternal` does **not** have the entry in hand (`createWorktree`
(`new.go:163`) returns only an `error`), so `add.go` performs the same `LoadStack`+`GetBranch`
lookup — the spec's "has the entry in hand" phrasing is inaccurate but the fallback rule is identical.

Untracked opens skip record steps 2, 5, 7, 9, 10 entirely and still propagate every
`LookPath`/`Start`/`Wait` error.

### 8.3 `tws close` (external) — guard + record-first ordering

Insertion points in `internal/cli/close.go`:

| Step | Where |
|---|---|
| Rewrite the "Deliberately guard-free" comment (:38-43) | it now states what *is* guarded; checkout branch still needs none |
| `internal.GuardFeatureName(internal.TwsRoot(), feature)` | immediately after the two-arg check (:59-62), **before** any path join, stat, read, remove, or tmux name build |
| `GuardFeatureName` calls `validateFeatureName` before the registry read (`internal/spaces.go:681`) | shared boundary: `internal.FeaturePath` is a bare `filepath.Join`, so without it `close ../outside branch` escapes `TwsRoot()` and reaches a prepared outside `.sessions/<branch-id>/<token>.json`; every guarded command inherits the refusal |
| Extract `runExternalClose(out io.Writer, feature, branch string, proc internal.ProcessProber, tmux externalTmuxOps) error` | replaces :64-79; `closeCmd` supplies `sessionExists` / `internal.Run("tmux","kill-session",…)` via a small `externalTmuxOps{Exists(string) bool; Kill(string) error}`. **No package-level test globals.** |

Ordering inside `runExternalClose`: load **all** records
(`LoadDirectSessions(featurePath, DirectSessionBranchID(feature, branch), &DirectSessionIdentity{feature, branch})`)
→ classify into `live` / `stale` / `unverifiable` → refuse if any `Probe == live` (kill nothing,
remove nothing, including stale siblings) → remove provably dead records one file at a time by token
→ prune empty dirs tolerating `ENOTEMPTY` → **print the unverifiable records** → tmux kill when the
session exists (identical strings) → cleanup-only success message → otherwise the verbatim
`no tmux session found for %s/%s`, or its actionable variant when unverifiable records remain.

`State != ok` **and** `Probe == unknown` (EPERM) records are both *unverifiable*: never counted live,
never removed, never blocking the tmux kill — and never silently dropped. They are printed before
tmux is touched, one redacted `DescribeDirectSession` line each, so the operator reads what was left
behind before reading what was killed. With no tmux session and nothing removed, the flat
`no tmux session found` would be a false negative (close *did* find state, it just could not act on
it), so the error names the count and points at `tws status --json`. Both remain non-zero exits, so
no caller's success/failure contract changes.

Because `GuardFeatureName` calls `SpaceDirOwners`, a malformed `spaces.yaml` now makes external
`close` fail closed — a deliberate behaviour change requiring a `CHANGELOG.md` line and a test using
`malformedSpacesFixtures()`.

### 8.4 Rename / archive / delete — exact insertion and rollback points

All four are **external-only**; checkout branches keep their current code path unchanged and never
reach a `.sessions/` join, because the guard is inside an explicit `if ws.Mode == internal.ModeExternal`
(or inside an already external-only function). There is **no** store or filesystem seam in these
commands and none is added — the isolation is proved structurally (§11.2, §17.8): the mode gate is
visible in the source, and a checkout fixture with a hand-planted
`.tws/features/auth/.sessions/<branch-id>/<token>.json` must come out of checkout
`rename`/`archive`/`delete` with that subtree byte-identical. Pattern at every site:

```go
blocking, stale, err := internal.GuardDirectSessionsFor(featurePath, targets, proc)
if err != nil { return err }
if len(blocking) > 0 { return fmt.Errorf(<refusal naming record_id, owner_pid, stage, file>) }
if _, err := internal.RemoveStaleDirectSessions(featurePath, stale); err != nil { return err }
```

| Command | Function | Insert **after** | Insert **before** | Targets |
|---|---|---|---|---|
| `tws rename feature` | `renameFeatureCmd` `RunE` (`rename.go:24-80`) — **mode-agnostic today**, so wrap in `if ws.Mode == internal.ModeExternal` | the destination-collision check (:63) — i.e. inside the already-open `BeginSpacesFeatureRename` tx, which is the name guard here | `os.Rename(oldPath, newPath)` (:65) | every `<branch-id>` from `ListDirectSessions(oldPath)`, each `Want: nil` |
| `tws rename branch` | `renameBranchExternal` (`rename.go:195`) | the `HasBranch(stack, oldName)` check (:207-209) | `resolveGitDir`/`git worktree remove` (:231) | one: `DirectSessionBranchID(feature, oldName)`, `Want:{feature, oldName}` |
| `tws archive` | `archiveExternal` (`archive.go:85`) | `GuardFeatureName` (:86) and `featurePath`/`path` computation (:90-91) | the worktree-absent early branch (:93) **and** `git worktree remove` (:110) — the early branch also mutates `stack.yaml`, so the guard must precede both | one: `DirectSessionBranchID(feature, branch)`, `Want:{feature, branch}` |
| `tws delete` | `deleteExternal` (`delete.go:147`) | the `os.Stat(featurePath)` existence check (:156) — inside the open `BeginSpacesFeatureDelete` tx | branch deletion (:167-188), worktree removal (:190-195), `os.RemoveAll` (:197) | every `<branch-id>` from `ListDirectSessions(featurePath)`, each `Want: nil` |

Rollback: at all four sites the guard runs **before the first mutation**, so refusal needs no
rollback — the tree is byte-identical. The two spaces transactions (`rename feature`, `delete`) are
already released by their `defer func(){ retErr = errors.Join(retErr, tx.Release()) }()`, so an early
`return` inside them is safe and leaves no lock.

`blocking` here includes `unknown` and `invalid` (unlike `close`, §8.3): these verbs destroy or
relocate identity, so anything not provably dead must block. State that rationale in a code comment.

Unchanged and asserted so by tests: checkout `rename`/`archive`/`delete`, `tws migrate-layout`,
`tws sync`, `new`, `add` (beyond error propagation), `inject`, `push`, `export`, `import`.

---

## 9. Degraded workspace, fatal preconditions, and `ResolveStatusWorkspace`

`ResolveStatusWorkspace() (Workspace, string /*degradedReason*/, error)` mirrors `RequireWorkspace`
(`workspace.go:440-465`) with one difference: when `MainRepoRoot()` fails, `DetectWorkspaceRoot`
succeeds and `metadataRootExists` is true, but `inferExternalRepoRoot` fails, it returns

```go
Workspace{RepoRoot: "", Mode: ModeExternal, MetadataRoot: canonicalize(metadataRoot),
          StableID: "", Caps: capsFor(ModeExternal)}, inferErr.Error(), nil
```

instead of the error (compare `workspace.go:458-464`, which fills `RepoRoot`/`StableID` from the
successful inference). Everything else (workspace root undetectable, invalid `workspace_mode`) is
returned as an error ⇒ exit 1, empty stdout.

Degraded projection: `degraded: true`, `degraded_reason` verbatim, `repo_root`/`stable_id`
**`null` not `""`**, `branch`/`detached`/`dirty`/`active_git_op` `null`, every entry's Git-derived
field `null` with `materialization.state: "unknown"`, one workspace `warning` `workspace-degraded`,
exit 0. Topology, direct records and tmux inventory are still read.

**Every Git call site in the builder is guarded by `if ws.RepoRoot == "" { skip }`.** There is exactly
one place that could otherwise shell out with an empty `-C`: `gitRefExists`/`healthCurrentBranch`/
`gitDirty`/`gitActiveOp`/`BuildWorktreeInventory`.

Fatal set (exit 1, no document): unresolvable workspace · metadata root missing/not-a-dir/unreadable
(§5.4 step 1) · `ListFeaturesResolved` error · `ErrSpaceNameConflict` · `ErrAmbiguousFeature`
(filtered **and** unfiltered) · `feature not found: <feature>` · JSON encode failure.
Everything else is reportable at exit 0.

---

## 10. Documentation, skills, roadmap, changelog (same commit — `assets/skills` is `go:embed`ed)

| File | Exact edit |
|---|---|
| `assets/skills/claude/tesseraworkspaces/SKILL.md` | command table (rows :23-51) — add `\| tws status [feature] [--json] \| Agent work status per branch \|` next to the `tws doctor [feature]` row (:43); new `### Agent Work Status` section after "Checkout List" (:255-262) with the two axes, `needs_attention` authoritative, exit 0 on attention, PID caveat, upward-inheritance caveat |
| `assets/skills/claude/tesseraworkspaces-orchestrator/SKILL.md` | "### View state" block (:16-27) — add `tws status --json`; make it step 0 of "## Orchestration Workflow" (:70-78); add the three verbatim caveats |
| `assets/skills/copilot/tws.prompt.md` | command list — add the `tws status` line beside `tws doctor [feature]` (:33) and above `tws close` (:36); "## Workflow" block (:115-125) — poll `tws status --json`, act on `attention.status`, never on `agent_state` |
| `README.md` | command table — one row after `tws doctor [feature]` (:137) |
| `docs/cheatsheet.md` | new `## Check agent status` block between "## See what you have" (:104) and "## Sync (rebase in dependency order)" (:112), with `tws status`, `tws status auth`, `tws status --json \| jq '.issues[] \| select(.severity=="warning")'` |
| `docs/roadmap.md` | move "agent work status" out of `## Now — agent work status` (:5) and the P2 bullet (:56) into shipped foundations; record follow-ups `tss-agent-state-provider`, portable process birth identity, and that base ancestry stays with the existing P1 "Stack ancestry doctor"/"Stack status" items; state that "blocked (needs approval/input)" is deferred, not dropped |
| `docs/engineering-workflow.md` | add the slice to "Current shipped checkout slices" (:12-21); rewrite "Next roadmap feature" (:25-27) to the next target |
| `CHANGELOG.md` | new version section above `## v1.2.11` (:3) covering: new command; two-axis schema + `schema_version: 1`; `agent_state` always `unknown`; hierarchical attention with own-scope `issue_count`/`codes`; exit 0 on attention (contrast with `doctor`); external direct records; **external `close` ordering change + compatibility matrix**; **`close` feature-name guard and its new hard failure on malformed spaces metadata**; refuse-live in external `rename`/`archive`/`delete`; `openDirect` no longer calling `os.Exit`; **`gitActiveOp` now also detects an in-progress `git bisect`, so `tws doctor` and `tws status` report `active_git_op: bisect` where `doctor` previously reported nothing** |

The three verbatim skill caveats: *"a `present` from tws means a process with that PID exists, not
that that exact process exists"*; *"`agent_state` is always `unknown` at this version; use
`needs_attention`"*; *"`attention.status` inherits upward: a workspace or feature can be
`needs_attention` with `issue_count: 0` because a child is — read `report.issues[]` for the detail"*.

---

## 11. Tests — helpers to reuse (all verified) and new files

### 11.1 Reuse, do not re-create

| Helper | Location | Use for |
|---|---|---|
| `setupHealthTestRepo(t) (dir, Workspace)` | `internal/checkout_health_test.go:40-82` | package-`internal` checkout fixtures (real git, `.tws/config.yaml: workspace_mode: checkout`, `.gitignore` for `.tws/`) |
| `addFeatureToRepo` / `addStackEntries` / `gitInTest` | `checkout_health_test.go:85 / 101 / 113` | stack fixtures + real git in `internal` tests |
| `fakeProcessChecker{alive map[int]bool}` / `fakeTmuxChecker{sessions map[string]bool}` | `checkout_health_test.go:16-35` | extend with a `fakeProcessProber{probe map[int]ProcessLiveness}` beside them |
| `testAgent` / `testShell` / `testTmux` | `internal/session_test.go:13 / 27 / 34-56` | shape precedent for the new call-order-recording `directRunner`/`directProcess` fakes |
| `setupGitRepoCheckout(t)` / `requireWorkspaceForTest` / `gitInDir` | `internal/cli/checkout_lifecycle_test.go:19 / 53 / 62` | checkout-mode CLI tests |
| `setupGitRepo(t, defaultBranch)` (real repo **+ real bare remote**) / `withWorkspaceEnv` | `internal/cli/new_integration_test.go:135 / 152` | external CLI tests |
| **`withUnifiedWorkspaceEnv(t, repo) string`** | `internal/cli/space_guard_test.go:58-78` | **mandatory** for every test touching both `internal.FeaturePath()` (TwsRoot-based) and `ws.MetadataRoot`. `withWorkspaceEnv` sets `TWS_ROOT` to a temp dir, but `RequireWorkspace` ignores `TWS_ROOT`, so the two roots **diverge** there and records written by `openDirect` would be invisible to `status` |
| `snapshotTree` / `snapshotTreeIgnoringLock` | `space_test.go:62` / `space_guard_test.go:80-91` | **path-set** snapshots only — `snapshotTree` walks and joins relative paths (`space_test.go:62-79`), it does **not** hash contents or modes. Use it for "nothing was created or removed"; for the byte-identical assertions (criteria 8, 34, 44, 51 and §17.8) add one local helper `snapshotRecordTree(t, dir) map[string]string` mapping each relative path to `sha256(contents) + "\|" + fmt.Sprintf("%04o", perm)` (dirs: perm only), declared once in `internal/cli/close_records_test.go` and reused |
| `malformedSpacesFixtures()` | `space_guard_test.go:104-136` | fail-closed tests for `status` and `close` |
| `registeredLearningFixture(name)` / `writeSpaces` | `space_guard_test.go:92-101 / 46-53` | `ErrSpaceNameConflict` regressions |
| `captureStdout(t, fn)` | `space_guard_test.go:16-42` | commands that still print with bare `fmt` |
| guarded-lifecycle matrix | `space_guard_test.go:426-489` (external) and `:550…` (checkout) | **add `close` and `status` entries** (both absent today) |
| `setupCheckoutSyncRepo` / `setupFeaturePath` | `internal/cli/checkout_sync_test.go:16 / 57` | sync-transaction fixtures |
| `TestExternalCloseStillRequiresTwoArgs` | `internal/cli/checkout_session_cli_test.go:75` | existing external-close regression to keep green |

### 11.2 New test files and their required cases

| File | Cases |
|---|---|
| `internal/direct_session_test.go` | create/update/load/remove round-trip; `0700`/`0600` (`perm&0077==0`); two concurrent records under one `<branch-id>`; token-owned removal leaves siblings and tolerates `ENOTEMPTY`; non-matching-token removal is a no-op; all nine §10.5 validation rows including `deadbeef.json` and `.tmp-session-xyz` skipping; `want != nil` identity mismatch → `invalid` vs `want == nil` → `ok` (criterion 42); `Name` containing `/`; two prefix-colliding identities get distinct hash suffixes; `GuardDirectSessionsFor` blocking set (live/unknown/invalid) vs `stale`; `CheckoutAgentSessionName` golden (criterion 64) |
| `internal/agent_status_test.go` | issue-code closure vs the §7.3 table (criterion 71); `RollupAttention` truth table (criterion 70); presence rollup precedence `present>unknown>stale>absent`; the §6.7 invariant (`stale`/`unknown` ⇒ `needs_attention`); every §6.5 checkout-session row incl. stat-vs-parse, driven through `projectCheckoutSession` (phase A) and `attributeCheckoutSession` (phase B) separately (§7.1) plus one end-to-end `session-unattributed` case where the owning feature's `stack.yaml` is corrupt — phase A issues must still be emitted; the §6.6 record table; sync projection for both kinds with the **identity-axis** fixture; `BuildWorktreeInventory` parsing incl. `prunable`; external per-worktree probes (§6.4): detached worktree ⇒ `checked_out_branch == null` and **no** `worktree-wrong-branch`, `rev-parse` failure ⇒ `worktree-unreadable` with `dirty == null`; `gitActiveOp` bisect row (§6.5); degraded workspace; metadata-root precondition; secret-absence byte scans (criteria 26-29) including a seeded `owner.json` whose `token` is non-empty and a second whose `token` is `""` — the first must be absent from the bytes, the second must raise `session-lock-invalid`; `FilterFeature` semantics (§5.7): other-feature issues dropped, workspace issues kept, `summary` and `workspace.attention` recomputed, `workspace.runtime_presence` deliberately not; JSON key-set snapshot at every level, both modes, empty and populated; **checkout-mode record isolation (criterion 77, structural form §17.8)** |
| `internal/cli/status_test.go` | help text greps; `--ancestry` unknown flag; `feature not found`; empty workspace human + `--json`; the five-cwd byte-identical `features[]` matrix (criterion 7); `ErrSpaceNameConflict` + tree snapshot; malformed `spaces.yaml` exit 1 with empty stdout; two consecutive runs differ only in `generated_at`; encoder shape (2-space indent, trailing newline) |
| `internal/cli/direct_open_test.go` | full §10.6 ordering with a call-order-recording fake runner (criterion 31); `LookPath` failure ⇒ error and **no** `.sessions/` (36); `Start` failure (37); agent-stage `Update` failure ⇒ `Terminate`+`Wait`+own-record removal (38); shell-stage pre-start non-ENOENT failure (39); shell-stage post-start failure ⇒ nil return + stderr warning (40); shell-stage `fs.ErrNotExist` ⇒ recreate with preserved `started_at`, and the double-failure variant (76); untracked `--feature-dir` in both modes writes nothing (41); error propagation from all four call sites through `Execute()` |
| `internal/cli/close_records_test.go` | the five-row matrix (44-48) incl. the **row-4 golden control**, the `invalid`-record row (50), the space-name guard with a pre-seeded record inside the space dir (49), and the malformed-`spaces.yaml` refusal (75) |
| additions to existing files | `space_guard_test.go` matrix + `close`/`status`; rename/archive/delete refuse-live/clean-stale/no-record cases (51-54) and the **checkout-untouched case (55, structural form §17.8)** — put them in `internal/cli/close_records_test.go` or a new `direct_session_guard_test.go`, not scattered; one `internal/checkout_health_test.go` case for the `bisect` marker (§6.5) asserting `tws doctor` still matches its golden for every non-bisect fixture |

**Structural form of criteria 55 and 77 (both replace an unimplementable seam claim — §17.8).**
Neither `BuildAgentStatus` nor `rename`/`archive`/`delete` has (or gains) a filesystem or store seam,
so "a fake store that fails if called" and "a filesystem-call-recording fake" cannot be written
without inventing one. Both are proved instead by a **mode gate plus a planted-state, byte-identical
tree** assertion:

```
fixture (both tests):
  setupGitRepoCheckout(t) + requireWorkspaceForTest(t)          // checkout mode, single checkout
  addFeatureToRepo(t, repo, "auth"); addStackEntries(t, ..., "api")
  plant by hand (no API call, records are external-only):
      <repo>/.tws/features/auth/.sessions/<any-branch-id>/<32-hex>.json   0600
      with a syntactically valid record: schema_version 1, feature auth, name api,
      owner_pid = os.Getpid() (deliberately LIVE, so any accidental read would be loud)
  before := snapshotRecordTree(t, <repo>/.tws/features/auth/.sessions)     // §11.1 helper
```

| Criterion | Act | Assert |
|---|---|---|
| 77 | `tws status --json` (and the human form) in that checkout workspace | (a) `features[0].entries[]` for `api` has `sessions == []` and `session_counts.total == 0`; (b) `[.issues[] \| select(.code \| startswith("direct-record"))] \| length == 0`, and no `direct-record-orphan-branch`; (c) the planted `<token>` and `<branch-id>` strings appear nowhere in the raw stdout bytes; (d) `snapshotRecordTree(...) == before`; (e) a source-level assertion that the only `.sessions` join in the builder is inside the external branch — `grep` the built report for a `sessions` observation of `kind: "direct"`, which is unreachable in checkout mode |
| 55 | checkout `tws rename feature auth auth2`, `tws rename branch auth api api2`, `tws archive auth api`, `tws delete auth` — each in its own fresh fixture | (a) exit status and stdout byte-identical to the pre-feature golden captured per §17.7; (b) for the three verbs that keep the tree, `snapshotRecordTree` of the (possibly relocated) `.sessions` subtree equals `before`; for `delete`, the tree is removed exactly as it is today with no separate record pass; (c) the live planted record never blocks any of them — a checkout verb must not consult records at all |

Optional portable hardening, run only when `os.Geteuid() != 0` and `runtime.GOOS != "windows"`
(`t.Skip` otherwise): `chmod 0000` the planted `<branch-id>` directory for the duration of the act
step. Any traversal then fails loudly with `EACCES` instead of passing silently; the checkout paths
must still exit exactly as in the readable-tree case. Restore the mode in `t.Cleanup` so the temp dir
can be removed.

### 11.3 Failure hooks / determinism

No real `tmux`, no real agent, no real spawned process, no `os.Exit` anywhere in the tested paths.

| Injection | Vehicle |
|---|---|
| PID liveness | `fakeProcessProber` returning `live`/`dead`/`unknown` per PID |
| tmux states | `fakeTmuxInventory` returning each `TmuxSnapshot` shape (missing binary, no server, `Err` set, panes unavailable, path mismatch) |
| record persistence failures | `directSessionStore` fake failing at a chosen step, plus one returning `fs.ErrNotExist` at the shell stage. **This seam exists only in `openDirect` (§8.2)** — it is a write path the CLI owns. It is *not* available to `BuildAgentStatus` or to `rename`/`archive`/`delete`, which call `internal` record helpers directly |
| checkout-mode record isolation | **no seam** — structural: mode gate in the source + planted `.sessions/` state + `snapshotRecordTree` equality + secret-string absence in stdout (§11.2, §17.8). Do not add an FS or store seam to the builder or to the lifecycle verbs to satisfy criteria 55/77 |
| process lifecycle | `directRunner`/`directProcess` fakes recording `Start`/`Wait`/`Terminate` order |
| corrupt state | seeded byte fixtures for `active.json`, `owner.json`, transaction, lock, record; `schema_version: 99` for the `*-unsupported` codes; `owner.json` with `token: ""` for `session-lock-invalid` |
| untrusted metadata | `malformedSpacesFixtures()` |
| unreadable metadata root | remove or `chmod 000` after resolution |
| unreadable planted `.sessions/` dir | `chmod 0000`, guarded by `os.Geteuid() != 0 && runtime.GOOS != "windows"`, else `t.Skip` (§11.2) |
| in-progress git operation | plant `.git/BISECT_LOG` (and `MERGE_HEAD`, `REVERT_HEAD`) directly — no real bisect run (§6.5) |
| git absent / tmux absent | `PATH` shim dir containing only a `git` symlink (criterion 60) |

Real temporary Git repos, bare remotes and linked worktrees come from `setupGitRepo` (which already
creates `remote.git`, pushes, and sets `origin/HEAD`) plus `createWorktree` for real worktrees.

---

## 12. Minimal ordered implementation sequence

1. `internal/tmux_names.go` + delegate `cli.sanitizeSessionName` / `cli.TmuxSessionName`. Run
   `go test ./internal/... ./internal/cli/... -count=1` — session names must be unchanged.
2. `internal/session.go`: extract `hashedSessionID`; `CheckoutAgentSessionName` delegates. Add the
   golden test **first**.
3. `internal/checkout_health.go`: `ProcessLiveness`, `ProcessProber`, `realProcessChecker.Probe`,
   `Alive` redefined, `proberAsChecker`, and the one `{"BISECT_LOG", "bisect"}` row in `gitActiveOp`
   (§6.5). `tws doctor` output must be unchanged for every existing fixture; the single intended
   difference is a repo mid-bisect, which now reports `active_git_op: bisect`.
4. `internal/direct_session.go` + `internal/direct_session_test.go` (pure filesystem, no CLI).
5. `internal/agent_status.go` — types, constants, `RollupAttention`/`RollupPresence`,
   `BuildWorktreeInventory`, `TmuxSnapshot`/`RealTmuxInventory`, `ResolveStatusWorkspace`, then
   `projectCheckoutSession` / `attributeCheckoutSession` (§7.1), `BuildAgentStatus`, `FilterFeature`
   (§5.7), `NormalizeAgentStatus`, `FormatAgentStatus`.
6. `internal/agent_status_test.go` (builder-level, no CLI).
7. `internal/cli/status.go` + `root.go` registration + `internal/cli/status_test.go`.
8. `internal/cli/direct_open.go` + `openDirect` refactor + the four call sites +
   `internal/cli/direct_open_test.go`.
9. `internal/cli/close.go` guard, comment rewrite, `runExternalClose`, ordering +
   `internal/cli/close_records_test.go` + the `space_guard_test.go` matrix rows.
10. `rename.go` / `archive.go` / `delete.go` record guards + tests.
11. Docs, skills, README, cheatsheet, roadmap, engineering-workflow, CHANGELOG (§10).
12. Full gates.

Steps 1-3 are behaviour-preserving refactors and should be verified green before step 4.

### Focused commands

```bash
go test ./internal/ -run 'DirectSession|AgentStatus|CheckoutAgentSessionName|Rollup' -count=1
go test ./internal/cli/ -run 'Status|DirectOpen|Close|SpaceGuard|Rename|Archive|Delete' -count=1
```

### Full gates (`docs/engineering-workflow.md`)

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
golangci-lint run ./...
make build
git diff --check
tpatch feature deps --validate-all
```

Baseline at `4f12d85`: `go build ./...`, `go vet ./...`, `gofmt -l internal cmd assets` all clean.
`golangci-lint` is on PATH (`~/go/bin/golangci-lint`); there is **no** `.golangci.yml`, so it runs
its default linter set — `errcheck` in particular will flag unchecked `os.Remove`/`Fprintf` returns,
so use `_ =` or `//nolint:errcheck` consistently with the existing code
(`session.go:172`, `registry.go`, `space.go`).

---

## 13. Dependency recheck

`.tpatch/features/agent-work-status-dashboard/status.json` already records **15 hard**
(`checkout-agent-sessions`, `checkout-doctor-observability`, `workspace-mode-foundation`,
`checkout-workspace-lifecycle`, `tmux-session-management`, `fix-external-feature-dir-resolution`,
`workspace-sibling-links`, `checkout-stack-safety`, `sync-continue`, `branch-name-decoupling`,
`worktree-health-check`, `fix-open-cwd-after-exit`, `tmux-free-mode`, `open-feature-dir`,
`quick-start-add-and-open`) and **5 soft** (`decision-read-tracking`, `workspace-registry`,
`list-features-branches`, `persist-agent-workflow-guidance`, `skill-distribution`).

Exploration surfaced **no new coupling**: every file this feature touches is owned by an already
registered parent. `decision-read-tracking` correctly stays soft (§7.5 emits an inert integer).

Recommended (not performed): `tpatch feature deps --validate-all` before implementation, per
`.tpatch/steering/local.md`. No mutation expected.

### Go dependency changes — recommendation only

**None.** Everything needed is in the standard library plus the two existing direct requires
(`spf13/cobra v1.10.2`, `gopkg.in/yaml.v3 v3.0.1`, Go 1.26.1). New stdlib imports only:
`crypto/rand`, `crypto/sha256`, `encoding/hex`, `encoding/json`, `errors`, `io`, `io/fs`, `os/exec`,
`sort`, `syscall`, `time`. Do **not** add a JSON-schema, table-writer, or process-inspection module.

---

## 14. Spec corrections (line references that differ from the tree)

| Spec citation | Actual |
|---|---|
| `internal/session.go:115-122` for `sessionStatePath`/`sessionLockDir` | `115`, `116`, `117-119`, `120-122` — correct in substance |
| `internal/session.go:690-692` refusal | `:691` |
| `internal/session.go:704-712` restore-dirty guard | `restoreCheckoutSession` at `:704`, dirty check `:705-711` |
| `internal/cli/close.go:38-43` comment | comment body `:39-43` (`:38` is the blank/`},` line) |
| `internal/cli/open.go:161-181` tmux resolution | `:160-171`; `openDirect` call at `:181` ✓ |
| `internal/cli/open.go:239-279` `openDirect` | ✓ |
| `internal/cli/rename.go:117-230` rename branch | `renameBranchCheckout` `:123-193`, `renameBranchExternal` `:195-283` |
| `internal/cli/archive.go:63-132` | `archiveExternal` `:85-133` |
| `internal/cli/space_guard_test.go:425-444` matrix | `runs := map[...]` at `:426`, external block `:426-489`; checkout block starts `:550` |
| `internal/checkout_health.go:155-179` `FilterFeature` | `:156-179` |
| `internal/cli/registry.go:74-83` nil-normalization | `:70-83` |
| `internal/cli/space.go:170-175` encoder | `:166-175` |
| `internal/cli/list.go:20-29` degraded fallback | `:20-29` ✓ (empty `RepoRoot` **and** `StableID`) |
| `internal/workspace.go:311-316` `WorktreePath` | `:311-316` ✓ |
| §10.6 "`tws add --open` has the entry in hand" | it does **not**; `createWorktree` (`new.go:163`) returns only `error` ⇒ same `LoadStack`+`GetBranch` fallback as `open.go:181` |
| §11.3 "`tws rename feature` (external)" | `renameFeatureCmd` is **mode-agnostic**; gate the record guard on `ws.Mode == internal.ModeExternal` |
| §11.3 "`GuardFeatureName` already runs first in … `BeginSpacesFeatureDelete`/`BeginSpacesFeatureRename`" | true in effect: `deleteExternal` and `renameFeatureCmd` have **no direct** `GuardFeatureName`; the transactions perform the name validation |
| §7.4 `sessionDirty(ws.RepoRoot)` vs §8.2 `gitDirty` | use **`gitDirty(ws.RepoRoot)`** (`checkout_health.go:283`, `-C`-based, matches doctor); `sessionDirty` is `cmd.Dir`-based and returns an error that would need a new classification. Note the spec states both |
| §13.3 `sanitizeExternalSessionName` (unexported) | must be **exported** as `internal.SanitizeTmuxName`: `cli` still needs the bare sanitizer for the tmux *window* name at `open.go:312`, which is neither `ExternalTmuxSessionName` nor `ExternalFeatureTmuxSessionName` (§3) |
| §7.3 `repo-git-op` trigger names `bisect` | `gitActiveOp` (`checkout_health.go:291-323`) checks only `rebase-merge`, `rebase-apply`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD` — no bisect. The trigger is kept and one `{"BISECT_LOG", "bisect"}` row is added (§6.5) |
| §6.5 attribution and §5.4/§13.1 pipeline order | attribution cannot happen where the spec's prose places it (before features are loaded): it needs the stack. Implemented as two phases, projection at step 6 and attribution at step 8 (§5.4, §7.1); no observable behaviour differs, only the ordering the implementer must follow |
| criterion 55 "asserted with a fake store that fails if called" and criterion 77 "a filesystem-call-recording fake" | no such seam exists in `BuildAgentStatus` or in `rename`/`archive`/`delete`, and none is added; both are replaced by the structural form in §11.2 / §17.8 |
| §5.3 external `checked_out_branch`/`dirty` | the spec does not say which directory the probes run in, nor what happens for a detached or unreadable worktree: `-C wtPath`, `checked_out_branch: null` when detached (no fabricated `worktree-wrong-branch`), `dirty: null` plus `worktree-unreadable` when `rev-parse` fails (§6.4) |
| §11.1 `snapshotTree` as a "byte-identical tree" assertion | `snapshotTree` (`space_test.go:62-79`) compares **path sets** only; content/mode equality needs the local `snapshotRecordTree` helper (§11.1, §11.2) |

---

## 15. Traps found during exploration (read before writing code)

1. **`CheckoutTransactionPath` is wrong for legacy checkout features.** `checkoutStateDir`
   (`checkout_sync.go:92-96`) is `dirname(dirname(featurePath))/state`; for `.tws/<f>` that is
   `<repoRoot>/state`. Use `ws.CheckoutStateDir()` (`resolve.go:285`, §7.2).
2. **Two different sync identity axes** — external `FailedBranch` is a stack `Name`, checkout
   `Plan[].Branch` is a `GitBranch()` (§7.3).
3. **`TWS_ROOT` divergence in tests.** `withWorkspaceEnv` makes `internal.FeaturePath()` and
   `ws.MetadataRoot` name different roots; use `withUnifiedWorkspaceEnv` for anything that writes a
   record in one and reads it in the other (§11.1).
4. **`buildOneSyncReport` wants a `ProcessChecker`, not a `ProcessProber`** — adapter required (§6.1).
5. **`archiveExternal` mutates `stack.yaml` in its worktree-absent early branch** (`archive.go:93-107`);
   the record guard must precede that branch, not just `git worktree remove` (`:110`).
6. **`strings.Fields(agentCmd)[0]` can panic** on a whitespace-only configured `agent_command` —
   and the first panic site is `isClaudeAgent` (`open.go:361-364`), reached from `open.go:243`
   *before* `open.go:247`. The `len(parts) == 0` guard must therefore precede the `isClaudeAgent`
   call, not just the `LookPath` (§8.2). `openWithTmux` (`open.go:337`) has the same trap and is
   otherwise untouched.
7. **`buildFeatureEntries` silently `continue`s on a `LoadStack` failure** (`checkout_health.go:523`).
   Status must **not** copy that: `stack-missing` (info, ENOENT) / `stack-invalid` (warning) with
   `entries: []` (spec §8.3).
8. **`internal.RequireTool`, `internal.Must` call `os.Exit`.** They are used by `openAll`/`openWithTmux`
   (untouched) — do not introduce them into any new code path.
9. **`healthCurrentBranch` and `gitDirty` swallow their errors** (`checkout_health.go:266-289`):
   a failed `rev-parse` reads as `("", false)` and a failed `git status` reads as "clean". For an
   external worktree that would silently report a broken checkout as clean and on the wrong branch,
   so the dirty probe is gated on a successful branch probe (§6.4).
10. **Checkout-session attribution cannot run where the projection runs.** The workspace-scoped rows
    of spec §6.5 must be emitted even when the owning feature's `stack.yaml` is corrupt or gone,
    while attribution needs the entry index — hence the two phases at pipeline steps 6 and 8
    (§5.4, §7.1). Emitting the entry-scoped liveness issue during projection would either lose it or
    force a second pass over `report.Issues`.
11. **`snapshotTree` is a path-set snapshot, not a content snapshot** (`space_test.go:62-79`); the
    "byte-identical tree" criteria need the `snapshotRecordTree` helper (§11.1).

---

## 16. Smallest changeset

**New (5 source, 5 test):** `internal/tmux_names.go`, `internal/direct_session.go`,
`internal/agent_status.go`, `internal/cli/status.go`, `internal/cli/direct_open.go`;
`internal/direct_session_test.go`, `internal/agent_status_test.go`, `internal/cli/status_test.go`,
`internal/cli/direct_open_test.go`, `internal/cli/close_records_test.go`.

**Changed (9 source, 2 test, 8 docs):** `internal/session.go` (extract `hashedSessionID`),
`internal/checkout_health.go` (prober seam + `gitActiveOp` bisect row), `internal/cli/root.go`,
`open.go`, `add.go`, `close.go`, `rename.go`, `archive.go`, `delete.go`;
`internal/cli/space_guard_test.go` (matrix rows), `internal/checkout_health_test.go` (bisect case);
three skills, `README.md`, `docs/cheatsheet.md`, `docs/roadmap.md`,
`docs/engineering-workflow.md`, `CHANGELOG.md`.

**Explicitly untouched:** `internal/exec.go` (`IsPrunableWorktree`), `internal/checkout_sync.go`
(`isProcessAlive`), `internal/cli/list.go:81` (N7), `internal/cli/doctor.go`, `sync.go`, `stack.go`,
`inject.go`, `push`, `export`, `import`, `migrate.go`, and every checkout-mode lifecycle branch.

---

## 17. Spec details that remain impossible, unsafe, or need a weaker form

1. **Criterion 61 — "no `git` command executed with an empty `-C` (asserted by a `PATH`-shim `git`
   that fails the test if invoked)".** Unachievable as literally written: `ResolveStatusWorkspace`
   itself runs `git rev-parse` via `MainRepoRoot()` before the builder exists, so a fail-on-invoke
   shim fires during resolution. **Weaker form to implement:** a *recording* shim that logs argv and
   an assertion that no recorded invocation contains an empty `-C` argument; or call
   `BuildAgentStatus` directly with a hand-built degraded `Workspace{RepoRoot: ""}`.
2. **Criterion 7 — byte-identical documents from all five cwds.** `features[].path` and
   `workspace.metadata_root` derive from `ws.MetadataRoot`, which is
   `canonicalize(repo) + ".tws"` when resolved from the repo (`workspace.go:281-290`) but
   `canonicalize(metadataRoot)` when resolved from the workspace root (`workspace.go:461`). These
   agree only when the `.tws` directory itself is not a symlink. **Recommendation:** have
   `ResolveStatusWorkspace` apply `canonicalize()` to `MetadataRoot` **and** `RepoRoot` before
   returning, so the property holds by construction — a new function may do this; `RequireWorkspace`
   must not be changed. Assert the property in the test rather than assuming it.
3. **`is_current_checkout` when HEAD is detached.** `healthCurrentBranch` returns a short SHA with
   `detached == true` (`checkout_health.go:266-281`). Comparing that SHA to `git_branch` yields
   `false` for every entry, which spec §5.5 forbids ("never a fabricated `false`"). **Implement:**
   when `detached == true` and no session record answers, emit `null`, not `false`.
4. **PID reuse and EPERM remain unmitigated by design** (spec §6.3, N8). `Signal(0)` proves only that
   *a* process with that PID exists. Do not add mtime/`started_at` cross-checking and do not let
   record age change a verdict — both are explicitly forbidden and criterion 35 greps for `ModTime`.
5. **`agent_state` has no producer.** Any code path that can emit `working|ready|blocked|done` is a
   spec violation (criteria 10-11). The constants exist solely as the `tss` extension point.
6. **Multi-user `.sessions/` trees.** A tree created by another user makes a second user's
   `openDirect` fail with `EACCES` at `CreateDirectSession`. Spec §10.2 requires this to be a hard,
   guidance-carrying failure — not a silent unrecorded open. There is no safe way to soften it.
7. **`close`'s row-4 byte-for-byte control.** Capture the golden stdout from the **pre-change**
   binary (`git stash` / `make build` before step 9, or hard-code the two exact lines) — it cannot be
   generated after the refactor and still prove compatibility.
8. **Criteria 55 and 77 name seams that do not exist and must not be built.** Criterion 55 says
   checkout `rename`/`archive`/`delete` are "asserted with a fake store that fails if called" and
   criterion 77 says "a filesystem-call-recording fake asserts no path containing `.sessions` was
   ever opened". Neither is implementable without inventing new indirection: `BuildAgentStatus` reads
   the filesystem through `os` directly and deliberately has only three seams (`Proc`, `Tmux`, `Now`,
   §5.4 step 0); the lifecycle verbs call `internal.GuardDirectSessionsFor` /
   `internal.ListDirectSessions` directly and take no store parameter. Adding a filesystem or store
   seam to either would enlarge the public surface of this feature for a test alone, contradicting
   the "no second implementation, no new seam" rule of §2, and a package-level test global is
   explicitly banned by §8.3. **Implement instead** the structural form given in §11.2: the mode gate
   is visible in the source (`if ws.Mode == internal.ModeExternal` / an external-only function), a
   hand-planted live `.tws/features/auth/.sessions/<branch-id>/<32-hex>.json` is present in a real
   checkout fixture, and the assertions are (a) zero direct-session observations and zero
   `direct-record-*` / `direct-record-orphan-branch` issues, (b) the planted token and branch-id
   strings absent from the raw output bytes, (c) `snapshotRecordTree` byte-identical before and
   after, and (d) for criterion 55, stdout and exit status equal to the pre-feature golden. Where
   portable (`os.Geteuid() != 0`, non-Windows) the planted directory is additionally `chmod 0000` for
   the duration, turning any accidental traversal into a loud `EACCES` instead of a silent pass; the
   test `t.Skip`s elsewhere and restores the mode in `t.Cleanup`. This proves the same property
   (checkout mode never consumes direct records) with strictly fewer moving parts and no production
   code written for tests.
