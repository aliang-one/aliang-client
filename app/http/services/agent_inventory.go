package services

import (
	"bufio"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

const (
	agentVibeTranscriptMaxMessages       = 24
	agentVibeDetailTranscriptMaxMessages = 500
	agentVibeTranscriptMaxContentRunes   = 4000
)

type agentSyncSnapshot struct {
	DeviceName            string                    `json:"device_name"`
	Platform              string                    `json:"platform"`
	AgentVersion          string                    `json:"agent_version"`
	Host                  string                    `json:"host,omitempty"`
	Capabilities          []string                  `json:"capabilities"`
	Tools                 []models.AgentTool        `json:"tools"`
	History               []models.AgentHistoryRoot `json:"history"`
	Projects              []models.AgentProject     `json:"projects"`
	VibeSessions          []models.AgentVibeSession `json:"vibe_sessions"`
	AuthorizedDirectories []string                  `json:"authorized_directories,omitempty"`
	CollectedAt           string                    `json:"collected_at"`
}

func collectAgentSyncSnapshot() agentSyncSnapshot {
	history := collectAgentHistoryRoots()
	vibeSessions := collectAgentVibeSessions()
	projects := collectAgentProjects(vibeSessions)

	return agentSyncSnapshot{
		DeviceName:            defaultAgentDeviceName(),
		Platform:              agentPlatform(),
		AgentVersion:          agentVersion(),
		Host:                  defaultAgentDeviceName(),
		Capabilities:          agentCapabilities(),
		Tools:                 detectAgentTools(),
		History:               history,
		Projects:              projects,
		VibeSessions:          summarizeAgentVibeSessions(vibeSessions),
		AuthorizedDirectories: agentProjectPaths(projects),
		CollectedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}

func summarizeAgentVibeSessions(sessions []models.AgentVibeSession) []models.AgentVibeSession {
	summaries := make([]models.AgentVibeSession, 0, len(sessions))
	for _, session := range sessions {
		session.Transcript = nil
		summaries = append(summaries, session)
	}
	return summaries
}

func collectAgentProjects(sessions []models.AgentVibeSession) []models.AgentProject {
	byPath := make(map[string]*models.AgentProject)
	for _, session := range sessions {
		path := strings.TrimSpace(session.ProjectPath)
		if !isSafeAgentProjectPath(path) {
			continue
		}
		project := ensureAgentProject(byPath, path)
		if session.Branch != "" && project.Branch == "" {
			project.Branch = session.Branch
		}
		if source := firstNonEmpty(session.Tool, session.Provider); source != "" {
			addAgentProjectSource(project, source)
		}
		if project.LastActiveAt == "" || compareRFC3339(session.UpdatedAt, project.LastActiveAt) > 0 {
			project.LastActiveAt = session.UpdatedAt
		}
	}

	projects := make([]models.AgentProject, 0, len(byPath))
	for _, project := range byPath {
		enrichAgentProject(project)
		projects = append(projects, *project)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastActiveAt > projects[j].LastActiveAt
	})
	return projects
}

func agentProjectPaths(projects []models.AgentProject) []string {
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		if project.Path != "" {
			paths = append(paths, project.Path)
		}
	}
	return paths
}

func ensureAgentProject(byPath map[string]*models.AgentProject, path string) *models.AgentProject {
	path = cleanAgentProjectPath(path)
	if existing := byPath[path]; existing != nil {
		return existing
	}
	project := &models.AgentProject{
		ID:     stableAgentID("proj", path),
		Name:   agentProjectName(path),
		Path:   path,
		Status: "idle",
	}
	byPath[path] = project
	return project
}

func addAgentProjectSource(project *models.AgentProject, source string) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		return
	}
	for _, existing := range project.SourceTools {
		if existing == source {
			return
		}
	}
	project.SourceTools = append(project.SourceTools, source)
	sort.Strings(project.SourceTools)
}

func enrichAgentProject(project *models.AgentProject) {
	if project == nil {
		return
	}
	if project.Status == "" {
		project.Status = "idle"
	}
	if project.Name == "" {
		project.Name = agentProjectName(project.Path)
	}
	if stat, err := os.Stat(project.Path); err == nil && stat.IsDir() {
		project.Description = firstNonEmpty(project.Description, "Discovered from local AI coding session history.")
		if project.LastActiveAt == "" {
			project.LastActiveAt = stat.ModTime().UTC().Format(time.RFC3339)
		}
	}
	if isDir(filepath.Join(project.Path, ".git")) {
		project.IsGitRepo = true
		if project.Branch == "" {
			project.Branch = readGitHeadBranch(project.Path)
		}
	}
	if project.Language == "" {
		project.Language = detectAgentProjectLanguage(project.Path)
	}
	if project.PackageManager == "" {
		project.PackageManager = detectAgentPackageManager(project.Path)
	}
}

func collectAgentVibeSessions() []models.AgentVibeSession {
	sessions := append(collectCodexVibeSessions(), collectClaudeVibeSessions()...)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	return sessions
}

func collectCodexVibeSessions() []models.AgentVibeSession {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	indexPath := filepath.Join(home, ".codex", "session_index.jsonl")
	var sessions []models.AgentVibeSession
	if file, err := os.Open(indexPath); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var row struct {
				ID         string `json:"id"`
				ThreadName string `json:"thread_name"`
				UpdatedAt  string `json:"updated_at"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				continue
			}
			if strings.TrimSpace(row.ID) == "" {
				continue
			}
			session := models.AgentVibeSession{
				ID:        "codex_" + row.ID,
				Provider:  "codex",
				Tool:      "codex",
				Title:     truncateAgentText(row.ThreadName, 200),
				Mode:      "vibe",
				Status:    "closed",
				UpdatedAt: normalizeAgentTime(row.UpdatedAt),
			}
			sessions = append(sessions, session)
		}
	}

	sessionFiles := append(
		findRecentAgentFiles(filepath.Join(home, ".codex", "sessions"), "*.jsonl", 0),
		findRecentAgentFiles(filepath.Join(home, ".codex", "archived_sessions"), "*.jsonl", 0)...,
	)
	metaByID := make(map[string]models.AgentVibeSession)
	for _, path := range sessionFiles {
		meta := readCodexSessionMeta(path)
		if meta.ID == "" {
			continue
		}
		metaByID[meta.ID] = meta
	}
	for i := range sessions {
		sourceID := strings.TrimPrefix(sessions[i].ID, "codex_")
		meta := metaByID[sourceID]
		if meta.ID == "" {
			continue
		}
		sessions[i].ProjectPath = firstNonEmpty(sessions[i].ProjectPath, meta.ProjectPath)
		sessions[i].Branch = firstNonEmpty(sessions[i].Branch, meta.Branch)
		sessions[i].Model = firstNonEmpty(sessions[i].Model, meta.Model)
		if len(meta.Transcript) > 0 {
			sessions[i].Transcript = meta.Transcript
			if sessions[i].MessageCount == 0 {
				sessions[i].MessageCount = len(meta.Transcript)
			}
		}
		sessions[i].CreatedAt = firstNonEmpty(sessions[i].CreatedAt, meta.CreatedAt)
		sessions[i].UpdatedAt = firstNonEmpty(sessions[i].UpdatedAt, meta.UpdatedAt)
		delete(metaByID, sourceID)
	}
	for _, meta := range metaByID {
		meta.ID = "codex_" + meta.ID
		meta.Status = "closed"
		meta.Mode = "vibe"
		sessions = append(sessions, meta)
	}
	return sessions
}

func readCodexSessionMeta(path string) models.AgentVibeSession {
	return readCodexSessionMetaWithLimit(path, agentVibeTranscriptMaxMessages)
}

func readCodexSessionMetaWithLimit(path string, maxMessages int) models.AgentVibeSession {
	file, err := os.Open(path)
	if err != nil {
		return models.AgentVibeSession{}
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lines := 0
	var session models.AgentVibeSession
	session.Provider = "codex"
	session.Tool = "codex"
	session.Mode = "vibe"
	session.Status = "closed"
	for scanner.Scan() {
		lines++
		var row struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				ID            string `json:"id"`
				CWD           string `json:"cwd"`
				Model         string `json:"model"`
				ModelProvider string `json:"model_provider"`
				Git           struct {
					Branch string `json:"branch"`
				} `json:"git"`
			} `json:"payload"`
		}
		line := scanner.Bytes()
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		if row.Type == "session_meta" {
			provider := firstNonEmpty(row.Payload.ModelProvider, "codex")
			session.ID = firstNonEmpty(session.ID, row.Payload.ID)
			session.Provider = strings.ToLower(provider)
			session.ProjectPath = firstNonEmpty(session.ProjectPath, cleanAgentProjectPath(row.Payload.CWD))
			session.Branch = firstNonEmpty(session.Branch, row.Payload.Git.Branch)
			session.Model = firstNonEmpty(session.Model, row.Payload.Model)
			session.CreatedAt = firstNonEmpty(session.CreatedAt, normalizeAgentTime(row.Timestamp))
			continue
		}
		if msg := parseCodexTranscriptMessage(line, len(session.Transcript)); msg.Content != "" {
			session.Transcript = append(session.Transcript, msg)
			session.MessageCount++
			if session.Title == "" && msg.Role == "user" {
				session.Title = truncateAgentText(msg.Content, 200)
			}
			if maxMessages > 0 && len(session.Transcript) > maxMessages {
				session.Transcript = session.Transcript[len(session.Transcript)-maxMessages:]
			}
		}
	}
	if session.ID == "" {
		return models.AgentVibeSession{}
	}
	if !isSafeAgentProjectPath(session.ProjectPath) {
		session.ProjectPath = ""
	}
	if session.UpdatedAt == "" {
		session.UpdatedAt = fileUpdatedAt(path)
	}
	if session.CreatedAt == "" && len(session.Transcript) > 0 {
		session.CreatedAt = session.Transcript[0].Timestamp
	}
	return session
}

func parseCodexTranscriptMessage(line []byte, index int) models.AgentVibeMessage {
	var row struct {
		Timestamp string          `json:"timestamp"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return models.AgentVibeMessage{}
	}
	role := ""
	content := ""
	switch row.Type {
	case "user", "assistant", "system":
		role = row.Type
		content = codexPayloadText(row.Payload)
	case "message":
		var payload struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
			Message struct {
				Role    string      `json:"role"`
				Content interface{} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return models.AgentVibeMessage{}
		}
		role = firstNonEmpty(payload.Role, payload.Message.Role)
		if payload.Content != nil {
			content = agentContentText(payload.Content)
		}
		if content == "" {
			content = agentContentText(payload.Message.Content)
		}
	case "response_item":
		var payload struct {
			Item struct {
				Type    string      `json:"type"`
				Role    string      `json:"role"`
				Content interface{} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return models.AgentVibeMessage{}
		}
		role = payload.Item.Role
		if role == "" && payload.Item.Type == "message" {
			role = "assistant"
		}
		content = agentContentText(payload.Item.Content)
	default:
		return models.AgentVibeMessage{}
	}
	role = normalizeAgentVibeRole(role)
	content = truncateAgentText(content, agentVibeTranscriptMaxContentRunes)
	if role == "" || content == "" {
		return models.AgentVibeMessage{}
	}
	return models.AgentVibeMessage{
		ID:        stableAgentID("msg", fmt.Sprintf("%s:%d:%s", row.Timestamp, index, content)),
		Role:      role,
		Content:   content,
		Timestamp: normalizeAgentTime(row.Timestamp),
	}
}

func codexPayloadText(raw json.RawMessage) string {
	var payload struct {
		Text    string      `json:"text"`
		Content interface{} `json:"content"`
		Message struct {
			Content interface{} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return firstNonEmpty(payload.Text, agentContentText(payload.Content), agentContentText(payload.Message.Content))
}

func collectClaudeVibeSessions() []models.AgentVibeSession {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	root := filepath.Join(home, ".claude", "projects")
	indexFiles := findRecentAgentFiles(root, "sessions-index.json", 0)
	var sessions []models.AgentVibeSession
	seen := make(map[string]bool)
	for _, indexPath := range indexFiles {
		raw, err := os.ReadFile(indexPath)
		if err != nil {
			continue
		}
		var index struct {
			OriginalPath string `json:"originalPath"`
			Entries      []struct {
				SessionID    string `json:"sessionId"`
				FirstPrompt  string `json:"firstPrompt"`
				Summary      string `json:"summary"`
				MessageCount int    `json:"messageCount"`
				Created      string `json:"created"`
				Modified     string `json:"modified"`
				GitBranch    string `json:"gitBranch"`
				ProjectPath  string `json:"projectPath"`
				IsSidechain  bool   `json:"isSidechain"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(raw, &index); err != nil {
			continue
		}
		for _, entry := range index.Entries {
			if entry.SessionID == "" || entry.IsSidechain {
				continue
			}
			projectPath := firstNonEmpty(entry.ProjectPath, index.OriginalPath)
			if !isSafeAgentProjectPath(projectPath) {
				continue
			}
			session := models.AgentVibeSession{
				ID:           "claude_" + entry.SessionID,
				Provider:     "claude",
				Tool:         "claude",
				ProjectPath:  cleanAgentProjectPath(projectPath),
				Title:        truncateAgentText(firstNonEmpty(entry.Summary, entry.FirstPrompt), 200),
				Summary:      truncateAgentText(entry.Summary, 500),
				Mode:         "vibe",
				Status:       "closed",
				MessageCount: entry.MessageCount,
				Branch:       entry.GitBranch,
				CreatedAt:    normalizeAgentTime(entry.Created),
				UpdatedAt:    normalizeAgentTime(entry.Modified),
			}
			sessions = append(sessions, session)
			seen[session.ID] = true
		}
	}
	for _, path := range findRecentAgentFiles(root, "*.jsonl", 0) {
		if strings.Contains(path, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
			continue
		}
		session := readClaudeSessionMeta(path)
		if session.ID == "" || seen[session.ID] {
			continue
		}
		sessions = append(sessions, session)
		seen[session.ID] = true
	}
	return sessions
}

func readClaudeSessionMeta(path string) models.AgentVibeSession {
	return readClaudeSessionMetaWithLimit(path, agentVibeTranscriptMaxMessages)
}

func readClaudeSessionMetaWithLimit(path string, maxMessages int) models.AgentVibeSession {
	file, err := os.Open(path)
	if err != nil {
		return models.AgentVibeSession{}
	}
	defer file.Close()

	var session models.AgentVibeSession
	session.Provider = "claude"
	session.Tool = "claude"
	session.Mode = "vibe"
	session.Status = "closed"
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var row struct {
			Timestamp   string      `json:"timestamp"`
			Type        string      `json:"type"`
			CWD         string      `json:"cwd"`
			SessionID   string      `json:"sessionId"`
			GitBranch   string      `json:"gitBranch"`
			IsSidechain bool        `json:"isSidechain"`
			Message     interface{} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row.IsSidechain {
			return models.AgentVibeSession{}
		}
		if session.ID == "" && strings.TrimSpace(row.SessionID) != "" {
			session.ID = "claude_" + row.SessionID
		}
		if session.ProjectPath == "" {
			session.ProjectPath = cleanAgentProjectPath(row.CWD)
		}
		if session.Branch == "" {
			session.Branch = row.GitBranch
		}
		if session.CreatedAt == "" {
			session.CreatedAt = normalizeAgentTime(row.Timestamp)
		}
		if row.Type == "user" || row.Type == "assistant" {
			session.MessageCount++
			if text := truncateAgentText(claudeMessageText(row.Message), agentVibeTranscriptMaxContentRunes); text != "" {
				session.Transcript = append(session.Transcript, models.AgentVibeMessage{
					ID:        stableAgentID("msg", fmt.Sprintf("%s:%d:%s", row.Timestamp, len(session.Transcript), text)),
					Role:      normalizeAgentVibeRole(row.Type),
					Content:   text,
					Timestamp: normalizeAgentTime(row.Timestamp),
				})
				if maxMessages > 0 && len(session.Transcript) > maxMessages {
					session.Transcript = session.Transcript[len(session.Transcript)-maxMessages:]
				}
			}
		}
		if session.Title == "" && row.Type == "user" {
			session.Title = truncateAgentText(claudeMessageText(row.Message), 200)
		}
	}
	if session.ID == "" {
		return models.AgentVibeSession{}
	}
	if !isSafeAgentProjectPath(session.ProjectPath) {
		return models.AgentVibeSession{}
	}
	session.UpdatedAt = fileUpdatedAt(path)
	return session
}

func claudeMessageText(value interface{}) string {
	raw, _ := json.Marshal(value)
	var msg struct {
		Content interface{} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	return agentContentText(msg.Content)
}

func agentContentText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []interface{}:
		var parts []string
		for _, item := range typed {
			if text := agentContentText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]interface{}:
		if text, ok := typed["text"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
		if text, ok := typed["content"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
		if nested, ok := typed["content"]; ok {
			return agentContentText(nested)
		}
	default:
		return ""
	}
	return ""
}

func normalizeAgentVibeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user", "human":
		return "user"
	case "assistant", "ai", "model":
		return "assistant"
	case "system":
		return "system"
	default:
		return ""
	}
}

func truncateAgentText(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return strings.TrimSpace(string(runes[:max]))
}

func isSafeAgentProjectPath(path string) bool {
	path = cleanAgentProjectPath(path)
	if path == "" {
		return false
	}
	if path == filepath.Clean(string(filepath.Separator)) {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && path == filepath.Clean(home) {
		return false
	}
	return true
}

func findRecentAgentFiles(root string, pattern string, limit int) []string {
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		ok, err := filepath.Match(pattern, filepath.Base(path))
		if err != nil || !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		candidates = append(candidates, candidate{path: path, modTime: info.ModTime()})
		return nil
	})
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.path)
	}
	return out
}

func cleanAgentProjectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if expanded, err := cache.ExpandHomePath(path); err == nil {
		path = expanded
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func agentProjectName(path string) string {
	if path = strings.TrimSpace(path); path == "" {
		return "Unknown"
	}
	if base := filepath.Base(path); base != "." && base != string(filepath.Separator) {
		return base
	}
	return path
}

func detectAgentProjectLanguage(path string) string {
	checks := []struct {
		file     string
		language string
	}{
		{"go.mod", "Go"},
		{"package.json", "JavaScript"},
		{"Cargo.toml", "Rust"},
		{"pyproject.toml", "Python"},
		{"pubspec.yaml", "Dart"},
		{"composer.json", "PHP"},
	}
	for _, check := range checks {
		if fileExists(filepath.Join(path, check.file)) {
			return check.language
		}
	}
	return ""
}

func detectAgentPackageManager(path string) string {
	checks := []struct {
		file    string
		manager string
	}{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"package-lock.json", "npm"},
		{"bun.lockb", "bun"},
		{"go.mod", "go"},
		{"Cargo.lock", "cargo"},
		{"poetry.lock", "poetry"},
		{"composer.lock", "composer"},
	}
	for _, check := range checks {
		if fileExists(filepath.Join(path, check.file)) {
			return check.manager
		}
	}
	return ""
}

func readGitHeadBranch(projectPath string) string {
	raw, err := os.ReadFile(filepath.Join(projectPath, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(text, prefix) {
		return strings.TrimPrefix(text, prefix)
	}
	return ""
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

func isDir(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.IsDir()
}

func stableAgentID(prefix string, value string) string {
	sum := sha1.Sum([]byte(value))
	return fmt.Sprintf("%s_%x", prefix, sum[:8])
}

func normalizeAgentTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return value
}

func fileUpdatedAt(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}

func compareRFC3339(a string, b string) int {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}
	if ta.After(tb) {
		return 1
	}
	if ta.Before(tb) {
		return -1
	}
	return 0
}
