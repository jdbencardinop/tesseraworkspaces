# Analysis

Checkout mode can create logical branches but currently rejects sync. A one-checkout sync must switch branches sequentially, so interruption or dirty state can leave the user's repository on the wrong branch or with an ambiguous rebase. A persisted transaction and exclusive operation lock are required before enabling sync.

Compatibility: external sync is unchanged. Checkout sync is one repository, one physical checkout, and one operation/session owner.

Risks: dirty-tree loss, detached HEAD, stale locks, original-branch restoration failures, conflict continuation skipping descendants, validation failure after branch switching, and process interruption between Git and state writes.
