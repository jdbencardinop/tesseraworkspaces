# Analysis

`tws` can resolve only the current repo/workspace. Open-anywhere and cross-root discovery require a durable global index, but it must not become an authority over workspace metadata or messages. The registry should be opt-in, secure, atomic, and resilient to moved/missing paths without filesystem-wide scanning.

Compatibility: no registry is created unless explicitly requested. Existing commands and workspace resolution remain unchanged.

Risks: path/alias ambiguity, symlink identity drift, concurrent writers, leaked sensitive paths, accidental target deletion, stale entries, and ID churn after moves.
