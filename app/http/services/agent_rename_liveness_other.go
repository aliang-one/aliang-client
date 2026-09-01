//go:build !unix

package services

// isPidAlive has no portable equivalent here (Windows would need
// OpenProcess); reporting false is safe — zombie-pid records simply cannot
// win the rename competition and no session is marked running. Titles still
// resolve because a dead pid record may seed the cache on first sight.
func isPidAlive(pid int) bool {
	return false
}
