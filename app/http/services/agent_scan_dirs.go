package services

import (
	"path/filepath"
	"strings"
)

// pathUnderAnyScanDir reports whether path is equal to or located under any of
// the given scan directories. It is a pure lexical comparison after cleaning
// (no symlink resolution). Empty / "." directories are skipped.
//
// filepath.Rel is used instead of a naive prefix match so that "/a" does not
// accidentally match "/ab" (a common prefix-collision bug).
func pathUnderAnyScanDir(path string, dirs []string) bool {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return false
	}
	for _, raw := range dirs {
		dir := filepath.Clean(strings.TrimSpace(raw))
		if dir == "" || dir == "." {
			continue
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		// rel == "." => path equals dir; rel without "../" prefix => path under dir
		if rel == "." || (!strings.HasPrefix(rel, "../") && rel != "..") {
			return true
		}
	}
	return false
}

// activeScanDirs returns dirs when the scan-directory filter is enabled and dirs
// is non-empty; otherwise nil (no filtering, current behavior).
func activeScanDirs(dirs []string, enabled bool) []string {
	if !enabled || len(dirs) == 0 {
		return nil
	}
	return dirs
}
