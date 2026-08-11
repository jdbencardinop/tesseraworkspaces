package internal

import "strings"

// SanitizeTmuxName maps characters tmux treats specially in session and window
// names. It is the single sanitizer for external tmux names; internal/cli
// delegates to it so every existing session name stays byte-identical.
//
// The mapping is deliberately lossy: feature "a" with branch "b" and the
// feature-wide name "a-b" both sanitize to "a-b". Callers that need proof of
// ownership must verify a pane path, never a name alone.
func SanitizeTmuxName(s string) string {
	r := strings.NewReplacer(".", "_", ":", "_", "/", "-")
	return r.Replace(s)
}

// ExternalTmuxSessionName returns the tmux session name used by external
// per-branch opens. name is the tws StackEntry.Name, not the Git branch.
func ExternalTmuxSessionName(feature, name string) string {
	return SanitizeTmuxName(feature + "/" + name)
}

// ExternalFeatureTmuxSessionName returns the tmux session name used by
// `tws open <feature> --all`.
func ExternalFeatureTmuxSessionName(feature string) string {
	return SanitizeTmuxName(feature)
}
