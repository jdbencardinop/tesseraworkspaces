# Specification

1. `tws enable --mode checkout|external` and `tws mode` are wired and tested.
2. New checkout features live under `.tws/features/<feature>`; runtime remains `.tws/state`.
3. Feature listing/completions scan only the mode-appropriate feature directory.
4. Legacy `.tws/<feature>` directories remain discoverable and usable; collisions with new layout fail clearly.
5. Provide a safe idempotent migration command or automatic one-time move only when unambiguous and atomic.
6. Internal directories are never returned as features.
7. External mode paths/behavior remain unchanged.
8. Embedded skills and docs match actual commands/layout.
