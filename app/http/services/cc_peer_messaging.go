package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// loadClaudePeerToken reads the peer auth token for a pid from
// ~/.claude/sessions/<pid>.<sha>.key (CC >= 2.1.224). Read on demand ONLY —
// never in the periodic scan. Missing/unreadable is not fatal: macOS/Linux
// accept token-less frames (spike-verified, see plan 附录 A), so callers
// degrade gracefully.
func loadClaudePeerToken(home string, pid int) string {
	if home = strings.TrimSpace(home); home == "" || pid <= 0 {
		return ""
	}
	files, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".*.key"))
	if err != nil || len(files) == 0 {
		return ""
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		return ""
	}
	var row struct {
		PeerToken string `json:"peerToken"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return ""
	}
	return strings.TrimSpace(row.PeerToken)
}
