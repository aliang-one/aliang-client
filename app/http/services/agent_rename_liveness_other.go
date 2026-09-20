//go:build !unix && !windows

package services

// isPidAlive has no portable equivalent on this platform; reporting false is
// safe — zombie-pid records simply cannot win the rename competition and no
// session is marked running. Titles still resolve because a dead pid record
// may seed the cache on first sight. Windows has a real implementation in
// agent_rename_liveness_windows.go.
func isPidAlive(pid int) bool {
	return false
}
