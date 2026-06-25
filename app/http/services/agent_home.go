package services

import (
	"strings"

	"aliang.one/nursorgate/internal/runtimepath"
)

// agentHome returns the home directory whose ~/.claude and ~/.codex should be
// scanned for agent (Claude Code / Codex) detection.
//
// When the process runs as a normal user this is just the user's home. When it
// runs as root — e.g. the Linux systemd Core daemon, whose $HOME is /root —
// runtimepath.EffectiveAgentHome resolves the active desktop user's home so
// detection keeps working under the systemd deployment. Returns "" if it cannot
// be determined.
func agentHome() string {
	home, _ := runtimepath.EffectiveAgentHome()
	return strings.TrimSpace(home)
}
