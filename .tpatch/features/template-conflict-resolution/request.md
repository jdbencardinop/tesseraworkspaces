# Feature Request: When applying templates and files conflict, prompt the user in apt/dpkg style: keep existing, use new, show diff, or edit merged. Interactive conflict resolution for tws add --template and tws template sync when inject/ files differ from template files.

**Slug**: `template-conflict-resolution`
**Originating issue**: [#2, completed in v1.2.18-rc.5](https://github.com/jdbencardinop/tesseraworkspaces/issues/2); this feature is a separate follow-up
**Created**: 2026-05-15T23:50:05Z

## Description

When applying templates and files conflict, prompt the user in apt/dpkg style: keep existing, use new, show diff, or edit merged. Interactive conflict resolution for tws add --template and tws template sync when inject/ files differ from template files.

## Approved planning scope after rc.5

The original description above records the request, not permission to change
legacy copying or overwrite unowned files. Issue #2 shipped safe refusal in
`named-feature-templates`; this separate feature adds explicit interactive
resolution for managed-owned regular text files in either selected role.

The user approved:

- an explicit interactive opt-in on add/sync, without unattended keep-all or
  replace-all policies;
- remembering keep/edit against the incoming rendered proposal, asking again
  when that proposal changes or local content changes without acknowledgement;
- versioned ownership metadata when a remembered decision is saved, with
  older rc.5 binaries refusing affected state rather than overwriting it
  (workspace owner discovery may also refuse);
- two-way diff and staged manual editing, without automatic three-way merging
  or a durable base-content history;
- preserving and refusing obsolete customized files; their deletion remains
  outside this feature.

Legacy directory templates, unmanaged paths, symlinks, permission drift,
invalid ownership, and unresolved recovery remain protected. Planning is
complete and Path B implementation is authorized; publication is not.
See `analysis.md`, `spec.md`, and `exploration.md` for the implementation contract.

During implementation, the user approved clarifying that older-CLI refusal
applies when managed state is read, not to every older command. The user also
requested separate backlog tracking for the pre-existing external feature
rename/backpointer defect, now [GitHub #5](https://github.com/jdbencardinop/tesseraworkspaces/issues/5).
Neither clarification permits overwriting customized files or discarding
their acknowledgements.
