# Feature Request: Add an opt-in global registry of active tws repositories/workspaces at `${XDG_DATA_HOME:-~/.local/share}/tws/registry.yaml`. Store schema version, stable opaque workspace ID, aliases, canonical path, kind (repo, external-workspace, checkout-workspace), and Git/marker identity hints. Provide `tws registry add/list/show/alias/check/repair/remove/prune --missing`, atomic locked 0600 writes in a 0700 directory, stale/moved/mismatched classification, and explicit enrollment only (`tws init --register` or registry add). The registry is discovery-only: removing an entry never deletes targets and it never stores messages/tool-owned content. Preserve existing workspace behavior.

**Slug**: `workspace-registry`
**Created**: 2026-08-06T01:09:45Z

## Description

Add an opt-in global registry of active tws repositories/workspaces at `${XDG_DATA_HOME:-~/.local/share}/tws/registry.yaml`. Store schema version, stable opaque workspace ID, aliases, canonical path, kind (repo, external-workspace, checkout-workspace), and Git/marker identity hints. Provide `tws registry add/list/show/alias/check/repair/remove/prune --missing`, atomic locked 0600 writes in a 0700 directory, stale/moved/mismatched classification, and explicit enrollment only (`tws init --register` or registry add). The registry is discovery-only: removing an entry never deletes targets and it never stores messages/tool-owned content. Preserve existing workspace behavior.
