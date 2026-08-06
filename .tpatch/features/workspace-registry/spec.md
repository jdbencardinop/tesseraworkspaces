# Specification

1. Registry lives at XDG data home or `~/.local/share/tws/registry.yaml`, directory 0700 and file 0600.
2. Entries have schema version, opaque stable ID, aliases, canonical path, kind, and identity hints.
3. Add is explicit, validates target kind/identity, rejects duplicate aliases, and is idempotent for the same target.
4. List/show support ID, alias, and exact canonical path selectors with deterministic output.
5. Alias/repair mutate atomically under an exclusive lock; concurrent writers do not lose updates.
6. Check classifies healthy, missing, moved/mismatched, and invalid marker/Git identity without mutating.
7. Remove deletes only registry metadata. Prune removes missing entries only with explicit flag/confirmation semantics suitable for non-TTY automation.
8. `tws init --register` enrolls only after successful init; no silent registration.
9. Existing behavior is unchanged when no registry exists.
