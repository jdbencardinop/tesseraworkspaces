# Analysis: cobra-migration

## Summary

Replace manual os.Args switch/flag parsing in cmd/tws/main.go and all internal/cli/*.go files with cobra commands. This gives us auto-generated help, shell completions, proper flag parsing, and aliases for free.

## Approach

- Create a root command with version flag in `internal/cli/root.go`
- Each subcommand becomes a `*cobra.Command` in its own file
- Positional args use `cobra.ExactArgs()` / `cobra.MinimumNArgs()`
- Flags use cobra's flag system
- `cmd/tws/main.go` becomes a thin wrapper calling `cli.Execute()`
- Add `completion` subcommand (cobra provides this for free)

## Affected Areas

Every CLI file changes, but the internal logic stays the same — it's a mechanical migration of the arg/flag parsing layer.
