# Analysis

Checkout mode currently manages logical branches and transactional sync but rejects open/close/hooks. Agent sessions must safely own the one physical checkout: switching to a logical branch while another session or sync operation is active would corrupt user expectations. A persisted session record and exclusive ownership rules are required.

Compatibility: external open/close/tmux behavior remains unchanged. Checkout mode supports one branch-owning session at a time.

Risks: dirty-tree loss, stale tmux/session state, launching on the wrong branch, concurrent direct/tmux agents, context files polluting Git status, restoration failures, killing unrelated tmux sessions, and session IDs colliding across workspaces.
