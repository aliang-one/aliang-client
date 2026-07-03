package services

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

const (
	agentProjectDetailMaxFiles        = 80
	agentProjectDetailMaxScanEntries  = 5000
	agentProjectDetailMaxScanDuration = 750 * time.Millisecond
	agentProjectReadmeMaxBytes        = 24 * 1024
	agentProjectFileReadMaxByte       = 512 * 1024
)

var errAgentProjectDetailScanLimit = errors.New("agent project detail scan limit reached")

type agentProjectFileCandidate struct {
	path    string
	modTime time.Time
}

func handleAgentDetailMessage(msg map[string]interface{}, writeJSON func(interface{}) error) {
	switch remoteString(msg, "type") {
	case models.AgentEventProjectDetail:
		_ = writeJSON(agentProjectDetailPayload(msg))
	case models.AgentEventAISessionDetail:
		_ = writeJSON(agentVibeSessionDetailPayload(msg))
	case models.AgentEventFileList:
		_ = writeJSON(agentFileListPayload(msg))
	case models.AgentEventFileRead:
		_ = writeJSON(agentFileReadPayload(msg))
	case "file.working_tree_diff":
		_ = writeJSON(agentWorkingTreeDiffPayload(msg))
	case models.AgentEventSlashCommandsList:
		_ = writeJSON(agentSlashCommandsListPayload(msg))
	}
}

func agentProjectDetailPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	project := ensureAgentProject(map[string]*models.AgentProject{}, projectPath)
	enrichAgentProject(project)
	enrichAgentProjectDetail(project)
	return map[string]interface{}{
		"type":       models.AgentEventProjectDetailResult,
		"request_id": requestID,
		"project":    project,
	}
}

func enrichAgentProjectDetail(project *models.AgentProject) {
	if project == nil {
		return
	}
	project.DetailUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	files, fileCount, totalSize := summarizeAgentProjectFiles(project.Path, agentProjectDetailMaxFiles)
	project.Files = files
	project.FileCount = fileCount
	project.TotalSize = totalSize
	project.Readme = readAgentProjectReadme(project.Path)
}

func summarizeAgentProjectFiles(root string, limit int) ([]string, int, int64) {
	candidateCap := 0
	if limit > 0 {
		candidateCap = limit
	}
	candidates := make([]agentProjectFileCandidate, 0, candidateCap)
	fileCount := 0
	var totalSize int64
	visitedEntries := 0
	startedAt := time.Now()
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if path != root && d.IsDir() && shouldSkipAgentProjectDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			visitedEntries++
			if shouldStopAgentProjectDetailScan(startedAt, visitedEntries) {
				return errAgentProjectDetailScanLimit
			}
			return nil
		}
		visitedEntries++
		if shouldStopAgentProjectDetailScan(startedAt, visitedEntries) {
			return errAgentProjectDetailScanLimit
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fileCount++
		totalSize += info.Size()
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		addAgentProjectFileCandidate(&candidates, agentProjectFileCandidate{path: filepath.ToSlash(rel), modTime: info.ModTime()}, limit)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errAgentProjectDetailScanLimit) {
		return nil, fileCount, totalSize
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	files := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		files = append(files, candidate.path)
	}
	sort.Strings(files)
	return files, fileCount, totalSize
}

func addAgentProjectFileCandidate(candidates *[]agentProjectFileCandidate, next agentProjectFileCandidate, limit int) {
	if limit <= 0 || len(*candidates) < limit {
		*candidates = append(*candidates, next)
		return
	}
	oldest := 0
	for i := 1; i < len(*candidates); i++ {
		if (*candidates)[i].modTime.Before((*candidates)[oldest].modTime) {
			oldest = i
		}
	}
	if next.modTime.After((*candidates)[oldest].modTime) {
		(*candidates)[oldest] = next
	}
}

func shouldStopAgentProjectDetailScan(startedAt time.Time, visitedEntries int) bool {
	if agentProjectDetailMaxScanEntries > 0 && visitedEntries >= agentProjectDetailMaxScanEntries {
		return true
	}
	return agentProjectDetailMaxScanDuration > 0 && time.Since(startedAt) >= agentProjectDetailMaxScanDuration
}

func shouldSkipAgentProjectDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", ".next", "dist", "build", "target", ".cache", ".venv", "vendor",
		"coverage", ".turbo", ".parcel-cache", ".pytest_cache", ".mypy_cache", ".ruff_cache",
		".gradle", ".idea", ".dart_tool", ".serverless", ".terraform", ".pnpm-store", ".yarn",
		"deriveddata":
		return true
	default:
		return false
	}
}

func readAgentProjectReadme(root string) string {
	for _, name := range []string{"README.md", "readme.md", "README", "README.txt"} {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(raw) > agentProjectReadmeMaxBytes {
			raw = raw[:agentProjectReadmeMaxBytes]
		}
		return truncateAgentText(string(raw), agentProjectReadmeMaxBytes)
	}
	return ""
}

func agentVibeSessionDetailPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	sessionID := remoteString(msg, "session_id")
	sourceSessionID := remoteString(msg, "source_session_id")
	projectPath := remoteString(msg, "project_path")
	options := agentVibeSessionReadOptions{
		Limit:           normalizeAgentVibeDetailLimit(remoteInt(msg, "limit", agentVibeDetailDefaultPageLimit)),
		BeforeMessageID: remoteString(msg, "before_message_id"),
		BeforeTimestamp: remoteString(msg, "before_timestamp"),
		IncludePageMeta: true,
	}
	session := findAgentVibeSessionDetail(sessionID, sourceSessionID, projectPath, options)
	if session.ID == "" {
		return agentFileErrorPayload(requestID, errors.New("vibe session not found"))
	}
	return map[string]interface{}{
		"type":       models.AgentEventAISessionDetailResult,
		"request_id": requestID,
		"session":    session,
	}
}

func normalizeAgentVibeDetailLimit(limit int) int {
	if limit <= 0 {
		return agentVibeDetailDefaultPageLimit
	}
	if limit > agentVibeDetailMaxPageLimit {
		return agentVibeDetailMaxPageLimit
	}
	return limit
}

func findAgentVibeSessionDetail(sessionID string, sourceSessionID string, projectPath string, options agentVibeSessionReadOptions) models.AgentVibeSession {
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	sessionID = strings.TrimSpace(sessionID)
	projectPath = cleanAgentProjectPath(projectPath)
	for _, candidate := range candidateAgentVibeSessionFiles(sourceSessionID) {
		session := readAgentVibeSessionDetailFile(candidate, options)
		if agentVibeSessionMatches(session, sessionID, sourceSessionID, projectPath) {
			return session
		}
	}
	for _, candidate := range allAgentVibeSessionDetailFiles() {
		session := readAgentVibeSessionDetailFile(candidate, options)
		if agentVibeSessionMatches(session, sessionID, sourceSessionID, projectPath) {
			return session
		}
	}
	return models.AgentVibeSession{}
}

func candidateAgentVibeSessionFiles(sourceSessionID string) []string {
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	if sourceSessionID == "" {
		return nil
	}
	home := agentHome()
	if home == "" {
		return nil
	}
	var out []string
	for _, root := range []string{filepath.Join(home, ".codex", "sessions"), filepath.Join(home, ".codex", "archived_sessions")} {
		out = append(out, findRecentAgentFiles(root, sourceSessionID+".jsonl", agentVibeDetailCandidateFileLimit)...)
	}
	out = append(out, findRecentAgentFiles(filepath.Join(home, ".claude", "projects"), sourceSessionID+".jsonl", agentVibeDetailCandidateFileLimit)...)
	return out
}

func allAgentVibeSessionDetailFiles() []string {
	home := agentHome()
	if home == "" {
		return nil
	}
	files := append(
		findRecentAgentFiles(filepath.Join(home, ".codex", "sessions"), "*.jsonl", 100),
		findRecentAgentFiles(filepath.Join(home, ".codex", "archived_sessions"), "*.jsonl", 100)...,
	)
	files = append(files, findRecentAgentFiles(filepath.Join(home, ".claude", "projects"), "*.jsonl", 100)...)
	return files
}

func readAgentVibeSessionDetailFile(path string, options agentVibeSessionReadOptions) models.AgentVibeSession {
	options.IncludePageMeta = true
	if strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)) {
		return readCodexSessionMetaWithOptions(path, options)
	}
	if strings.Contains(path, string(filepath.Separator)+".claude"+string(filepath.Separator)) {
		return readClaudeSessionMetaWithOptions(path, options)
	}
	return models.AgentVibeSession{}
}

func agentVibeSessionMatches(session models.AgentVibeSession, sessionID string, sourceSessionID string, projectPath string) bool {
	if session.ID == "" {
		return false
	}
	ids := []string{session.ID, strings.TrimPrefix(session.ID, "codex_"), strings.TrimPrefix(session.ID, "claude_")}
	for _, id := range ids {
		if id != "" && (id == sessionID || id == sourceSessionID) {
			return projectPath == "" || cleanAgentProjectPath(session.ProjectPath) == projectPath
		}
	}
	return false
}

func agentFileListPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	targetPath, err := resolveAgentProjectContentPath(projectPath, remoteString(msg, "path"))
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	maxEntries := remoteInt(msg, "max_entries", 200)
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxEntries > 1000 {
		maxEntries = 1000
	}
	entries, truncated, err := readAgentDirectoryEntries(targetPath, maxEntries)
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	// Real per-file git status (clean/modified/added/deleted) for the browser's
	// status filters. Non-git dir → empty map → everything reads as 'clean'.
	gitStatus := loadGitStatusMap(targetPath)
	result := make([]map[string]interface{}, 0, minInt(len(entries), maxEntries))
	for _, entry := range entries {
		if len(result) >= maxEntries {
			break
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		itemPath := filepath.Join(targetPath, entry.Name())
		status := "clean"
		if entry.IsDir() {
			// Folder glows if any changed file lives anywhere beneath it.
			status = gitDirectoryStatus(itemPath, gitStatus)
		} else if s, ok := gitStatus[itemPath]; ok {
			status = s
		}
		result = append(result, map[string]interface{}{
			"name":        entry.Name(),
			"path":        itemPath,
			"kind":        kind,
			"size_bytes":  fileSizeForAgentEntry(info, entry.IsDir()),
			"modified_at": info.ModTime().UTC().Format(time.RFC3339),
			"language":    agentFileLanguage(entry.Name(), entry.IsDir()),
			"summary":     agentFileSummary(entry.IsDir()),
			"status":      status,
		})
	}
	return map[string]interface{}{
		"type":         models.AgentEventFileListResult,
		"request_id":   requestID,
		"path":         targetPath,
		"entries":      result,
		"truncated":    truncated,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}
}

func readAgentDirectoryEntries(path string, maxEntries int) ([]os.DirEntry, bool, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	truncated := len(entries) > maxEntries
	if truncated {
		entries = entries[:maxEntries]
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, truncated, nil
}

func agentFileReadPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	targetPath, err := resolveAgentProjectContentPath(projectPath, remoteString(msg, "path"))
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	maxBytes := remoteInt(msg, "max_bytes", 128*1024)
	if maxBytes < 1 {
		maxBytes = 1
	}
	if maxBytes > agentProjectFileReadMaxByte {
		maxBytes = agentProjectFileReadMaxByte
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	if info.IsDir() {
		return agentFileErrorPayload(requestID, errors.New("path is a directory"))
	}
	file, err := os.Open(targetPath)
	if err != nil {
		return agentFileErrorPayload(requestID, err)
	}
	defer file.Close()
	buffer := make([]byte, maxBytes+1)
	n, _ := io.ReadFull(file, buffer)
	if n == 0 {
		if read, err := file.Read(buffer); err == nil {
			n = read
		}
	}
	raw := buffer[:minInt(n, maxBytes)]
	binary := isLikelyAgentBinary(raw)
	content := string(raw)
	encoding := "utf8"
	if binary {
		content = base64.StdEncoding.EncodeToString(raw)
		encoding = "base64"
	}
	return map[string]interface{}{
		"type":        models.AgentEventFileReadResult,
		"request_id":  requestID,
		"path":        targetPath,
		"encoding":    encoding,
		"content":     content,
		"mime_type":   agentMimeType(targetPath, raw, binary),
		"size_bytes":  info.Size(),
		"modified_at": info.ModTime().UTC().Format(time.RFC3339),
		"truncated":   info.Size() > int64(maxBytes),
	}
}

// resolveAgentProjectPath resolves the project/base directory for a request.
// Operator policy: authorized-directory confinement REMOVED — only validates the
// path is a real directory.
func resolveAgentProjectPath(raw string) (string, error) {
	return cleanExistingAgentDirectory(raw)
}

func resolveAgentProjectContentPath(projectPath string, raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		target = projectPath
	}
	if expanded, err := cache.ExpandHomePath(target); err == nil {
		target = expanded
	}
	// Relative paths are expressed relative to the project (cwd == projectPath per
	// the commandGen tool contract). Join first so they don't resolve against the
	// agent daemon's process cwd.
	if !filepath.IsAbs(target) {
		target = filepath.Join(projectPath, target)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	// Operator policy: project/authorized-directory confinement REMOVED — any path
	// the agent's OS user can reach is readable. projectPath is still the base for
	// relative paths above.
	return abs, nil
}

func agentFileErrorPayload(requestID string, err error) map[string]interface{} {
	message := "agent file request failed"
	if err != nil {
		message = err.Error()
	}
	return map[string]interface{}{
		"type":       models.AgentEventFileError,
		"request_id": requestID,
		"error":      message,
	}
}

func fileSizeForAgentEntry(info os.FileInfo, isDir bool) interface{} {
	if isDir || info == nil {
		return nil
	}
	return info.Size()
}

func agentFileSummary(isDir bool) string {
	if isDir {
		return "Directory"
	}
	return "File from local desktop Agent"
}

func agentFileLanguage(name string, isDir bool) string {
	if isDir {
		return "Folder"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "Go"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".json":
		return "JSON"
	case ".md", ".markdown":
		return "Markdown"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".kt", ".kts":
		return "Kotlin"
	case ".swift":
		return "Swift"
	case ".html", ".htm":
		return "HTML"
	case ".css", ".scss", ".sass":
		return "CSS"
	default:
		return ""
	}
}

func agentMimeType(path string, raw []byte, binary bool) string {
	if binary {
		return "application/octet-stream"
	}
	if mime := http.DetectContentType(raw); mime != "application/octet-stream" {
		return mime
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "application/json"
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".mjs", ".cjs":
		return "text/javascript"
	case ".ts", ".tsx":
		return "text/plain"
	default:
		return "text/plain"
	}
}

func isLikelyAgentBinary(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return true
	}
	nonPrintable := 0
	for _, b := range raw {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 32 || b > 126 {
			nonPrintable++
		}
	}
	return nonPrintable > len(raw)/3
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
