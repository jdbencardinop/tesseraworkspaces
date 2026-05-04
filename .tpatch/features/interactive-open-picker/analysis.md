# Analysis: interactive-open-picker

## Summary

Make tws open work with 0, 1, or 2 args. With fewer args, present an interactive picker via fzf (if available) or a numbered list fallback. Add cobra ValidArgsFunction for shell tab completions.

## Behavior

- `tws open` → pick feature, then pick branch (fzf or numbered list)
- `tws open auth` → pick branch within auth
- `tws open auth auth-models` → direct open (current behavior)
- Tab completion suggests features for arg1, branches for arg2

## Affected Areas

- `internal/cli/open.go` — change Args, add picker logic
- New: `internal/cli/picker.go` — shared fzf/fallback picker logic (reusable for other commands)
