package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentWorkingTreeDiffPayload answers `file.working_tree_diff`: returns the
// project's current uncommitted changes (staged + unstaged + untracked) as a
// per-file list of {path, status, diff, added, removed}. The phone's change
// review screen renders these with its unified-diff viewer, so manual edits and
// uncommitted AI edits both show up (committed work does not — that's git log).
func agentWorkingTreeDiffPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	entries := workingTreeDiffEntries(projectPath)
	return map[string]interface{}{
		"type":       "file.working_tree_diff.result",
		"request_id": requestID,
		"path":       projectPath,
		"entries":    entries,
	}
}

// workingTreeDiffEntries collects per-file diffs for uncommitted changes under
// targetPath. Tracked changes come from `git diff HEAD`; untracked files are
// synthesized as all-added diffs. Non-git / git-missing → empty list.
func workingTreeDiffEntries(targetPath string) []map[string]interface{} {
	out := []map[string]interface{}{}
	seen := map[string]bool{}

	// 1. Tracked (staged + unstaged) changes vs HEAD.
	if raw, err := agentRunGit(targetPath, "diff", "HEAD", "--no-color", "--unified=3"); err == nil {
		for _, f := range parseGitDiffFiles(raw) {
			abs := filepath.Join(targetPath, f.relPath)
			if seen[abs] {
				continue
			}
			seen[abs] = true
			added, removed := summarizeFileDiff(f.diff)
			out = append(out, map[string]interface{}{
				"path":    abs,
				"status":  f.status,
				"diff":    f.diff,
				"added":   added,
				"removed": removed,
			})
		}
	}

	// 2. Untracked files (`??`) — `git diff HEAD` omits them; synthesize a full
	// addition diff from the file content. -uall lists individual files (not just
	// the containing dir), respecting .gitignore.
	status, err := agentRunGit(targetPath, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return out
	}
	parentPrefix := ".." + string(filepath.Separator)
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 3 || line[:2] != "??" {
			continue
		}
		rel := agentUnquoteGitPath(strings.TrimSpace(line[3:]))
		if rel == "" || strings.HasPrefix(rel, parentPrefix) {
			continue
		}
		abs := filepath.Join(targetPath, rel)
		if seen[abs] {
			continue
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || info.IsDir() {
			continue // untracked dir (individual files come as their own entries)
		}
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			continue
		}
		diff := synthesizeAddedDiff(rel, string(data))
		added, removed := summarizeFileDiff(diff)
		out = append(out, map[string]interface{}{
			"path":    abs,
			"status":  "added",
			"diff":    diff,
			"added":   added,
			"removed": removed,
		})
	}
	return out
}

// gitDiffFile is one file's chunk from `git diff` output.
type gitDiffFile struct {
	relPath string
	status  string // added | modified | deleted
	diff    string
}

// parseGitDiffFiles splits raw `git diff` output into one chunk per file. Each
// chunk begins with a "diff --git a/… b/…" line.
func parseGitDiffFiles(raw string) []gitDiffFile {
	out := []gitDiffFile{}
	if raw == "" {
		return out
	}
	chunks := strings.Split(raw, "diff --git ")
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		chunk = "diff --git " + chunk
		path, status := gitDiffFilePathAndStatus(chunk)
		if path == "" {
			continue
		}
		out = append(out, gitDiffFile{relPath: path, status: status, diff: chunk})
	}
	return out
}

// gitDiffFilePathAndStatus derives the (repo-relative) path and change status
// from one diff chunk via its `+++`/`---` headers:
//   - `+++ /dev/null` → deleted (path from `--- a/`)
//   - `--- /dev/null` → added (path from `+++ b/`)
//   - otherwise → modified (path from `+++ b/`)
func gitDiffFilePathAndStatus(chunk string) (path, status string) {
	var plusLine, minusLine string
	for _, line := range strings.Split(chunk, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			plusLine = line
		case strings.HasPrefix(line, "--- "):
			minusLine = line
		}
	}
	plusPath := strings.TrimPrefix(strings.TrimPrefix(plusLine, "+++ "), "b/")
	minusPath := strings.TrimPrefix(strings.TrimPrefix(minusLine, "--- "), "a/")
	if plusLine == "+++ /dev/null" {
		return minusPath, "deleted"
	}
	if minusLine == "--- /dev/null" {
		return plusPath, "added"
	}
	return plusPath, "modified"
}

// synthesizeAddedDiff renders a unified diff that adds `content` as a brand-new
// file (every line a `+`). Used for untracked files, which `git diff HEAD` skips.
func synthesizeAddedDiff(path, content string) string {
	trimmed := strings.TrimRight(content, "\n")
	var lines []string
	if trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	var b strings.Builder
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ b/" + path + "\n")
	b.WriteString(fmt.Sprintf("@@ -0,0 +%d @@\n", len(lines)))
	for _, l := range lines {
		b.WriteString("+")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}
