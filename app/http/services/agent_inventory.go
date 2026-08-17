package services

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

const (
	agentVibeTranscriptMaxMessages     = 24
	agentVibeDetailDefaultPageLimit    = 40
	agentVibeDetailMaxPageLimit        = 100
	agentVibeTranscriptMaxContentRunes = 4000
	agentVibeIndexMaxLines             = 240
	agentVibeIndexMaxBytes             = 2 * 1024 * 1024
	agentVibeSummaryMaxSessions        = 200
	agentVibeSessionFileScanLimit      = 120
	agentVibeIndexFileScanLimit        = 80
	agentVibeDetailCandidateFileLimit  = 24
	agentRecentFileWalkMaxEntries      = 6000
	agentRecentFileWalkMaxDuration     = 500 * time.Millisecond
)

type agentRecentFileCandidate struct {
	path    string
	modTime time.Time
}

type agentVibeSessionReadOptions struct {
	Limit           int
	BeforeMessageID string
	BeforeTimestamp string
	IncludePageMeta bool
	// ScanDirs, when non-empty, filters sessions early: a session whose project
	// path is not under any of these directories is dropped before its transcript
	// is read. Empty/nil = no filtering (current behavior).
	ScanDirs []string
}

type agentVibeTranscriptWindow struct {
	limit           int
	beforeMessageID string
	beforeTimestamp string
	foundBefore     bool
	messages        []models.AgentVibeMessage
}

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

func collectAgentSyncSnapshot(scanDirs []string) agentSyncSnapshot {
	history := collectAgentHistoryRoots()
	vibeSessions := collectAgentVibeSessions(scanDirs)
	projects := collectAgentProjects(vibeSessions)
	authorizedDirectories := agentProjectPaths(projects)
	setAgentAuthorizedExecutionDirectoriesCache(authorizedDirectories)

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
		AuthorizedDirectories: authorizedDirectories,
		CollectedAt:           time.Now().UTC().Format(time.RFC3339),
	}
}

func overlayActiveAgentVibeSessions(snapshot agentSyncSnapshot, active []models.AgentVibeSession, scanDirs []string) agentSyncSnapshot {
	if len(active) == 0 {
		return snapshot
	}
	sessions := make([]models.AgentVibeSession, 0, len(snapshot.VibeSessions)+len(active))
	byID := make(map[string]int, len(snapshot.VibeSessions)+len(active))
	for _, session := range snapshot.VibeSessions {
		idx := len(sessions)
		sessions = append(sessions, session)
		if id := strings.TrimSpace(session.ID); id != "" {
			byID[id] = idx
		}
	}
	projects := append([]models.AgentProject(nil), snapshot.Projects...)
	for _, session := range active {
		session = normalizeActiveAgentVibeSession(session)
		if session.ID == "" {
			continue
		}
		if len(scanDirs) > 0 && session.ProjectPath != "" && !pathUnderAnyScanDir(session.ProjectPath, scanDirs) {
			continue
		}
		if idx, ok := byID[session.ID]; ok {
			sessions[idx] = mergeActiveAgentVibeSession(sessions[idx], session)
		} else {
			byID[session.ID] = len(sessions)
			sessions = append(sessions, session)
		}
		projects = upsertActiveAgentProject(projects, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	if len(sessions) > agentVibeSummaryMaxSessions {
		sessions = sessions[:agentVibeSummaryMaxSessions]
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastActiveAt > projects[j].LastActiveAt
	})
	snapshot.VibeSessions = summarizeAgentVibeSessions(sessions)
	snapshot.Projects = projects
	snapshot.AuthorizedDirectories = agentProjectPaths(projects)
	setAgentAuthorizedExecutionDirectoriesCache(snapshot.AuthorizedDirectories)
	return snapshot
}

func normalizeActiveAgentVibeSession(session models.AgentVibeSession) models.AgentVibeSession {
	session.ID = strings.TrimSpace(session.ID)
	session.Provider = strings.ToLower(strings.TrimSpace(firstNonEmpty(session.Provider, "auto")))
	session.Tool = strings.ToLower(strings.TrimSpace(firstNonEmpty(session.Tool, session.Provider)))
	session.ProjectPath = cleanAgentProjectPath(session.ProjectPath)
	session.Title = truncateAgentText(session.Title, 200)
	session.Summary = truncateAgentText(session.Summary, 500)
	session.Mode = strings.TrimSpace(firstNonEmpty(session.Mode, "vibe"))
	session.Status = "running"
	session.Model = strings.TrimSpace(session.Model)
	session.CreatedAt = normalizeAgentTime(session.CreatedAt)
	session.UpdatedAt = normalizeAgentTime(firstNonEmpty(session.UpdatedAt, time.Now().UTC().Format(time.RFC3339)))
	session.Transcript = nil
	session.TranscriptPage = nil
	if !isSafeAgentProjectPath(session.ProjectPath) {
		session.ProjectPath = ""
	}
	return session
}

func mergeActiveAgentVibeSession(existing models.AgentVibeSession, active models.AgentVibeSession) models.AgentVibeSession {
	active.Title = firstNonEmpty(active.Title, existing.Title)
	active.Summary = firstNonEmpty(active.Summary, existing.Summary)
	active.ProjectPath = firstNonEmpty(active.ProjectPath, existing.ProjectPath)
	active.Branch = firstNonEmpty(active.Branch, existing.Branch)
	active.Model = firstNonEmpty(active.Model, existing.Model)
	active.CreatedAt = firstNonEmpty(active.CreatedAt, existing.CreatedAt)
	if active.MessageCount == 0 {
		active.MessageCount = existing.MessageCount
	}
	return active
}

func upsertActiveAgentProject(projects []models.AgentProject, session models.AgentVibeSession) []models.AgentProject {
	path := cleanAgentProjectPath(session.ProjectPath)
	if !isSafeAgentProjectPath(path) {
		return projects
	}
	now := firstNonEmpty(session.UpdatedAt, time.Now().UTC().Format(time.RFC3339))
	for i := range projects {
		if cleanAgentProjectPath(projects[i].Path) != path {
			continue
		}
		projects[i].Status = "running"
		if projects[i].LastActiveAt == "" || compareRFC3339(now, projects[i].LastActiveAt) > 0 {
			projects[i].LastActiveAt = now
		}
		if source := firstNonEmpty(session.Tool, session.Provider); source != "" {
			addAgentProjectSource(&projects[i], source)
		}
		enrichAgentProject(&projects[i])
		projects[i].Status = "running"
		return projects
	}
	project := models.AgentProject{
		ID:           stableAgentID("proj", path),
		Name:         agentProjectName(path),
		Path:         path,
		Status:       "running",
		Branch:       session.Branch,
		LastActiveAt: now,
	}
	if source := firstNonEmpty(session.Tool, session.Provider); source != "" {
		addAgentProjectSource(&project, source)
	}
	enrichAgentProject(&project)
	project.Status = "running"
	return append(projects, project)
}

func summarizeAgentVibeSessions(sessions []models.AgentVibeSession) []models.AgentVibeSession {
	summaries := make([]models.AgentVibeSession, 0, len(sessions))
	for _, session := range sessions {
		session.Transcript = nil
		session.TranscriptPage = nil
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
		// Live dashboard metrics: report this run's project filesystem/git state on
		// every snapshot (the inventoryTicker re-emits the project list ~1/min), so
		// the mobile home card's Files/Changed reflect the real current project
		// state without depending on an active vibe run. git ls-files = tracked file
		// count (Files); git status --porcelain lines = uncommitted changes (Changed).
		if tracked := countGitTrackedFiles(project.Path); tracked > 0 {
			project.FileCount = tracked
		}
		project.GitChangedCount = countGitChanged(project.Path)
	}
	if project.Language == "" {
		project.Language = detectAgentProjectLanguage(project.Path)
	}
	if project.PackageManager == "" {
		project.PackageManager = detectAgentPackageManager(project.Path)
	}
}

func collectAgentVibeSessions(scanDirs []string) []models.AgentVibeSession {
	sessions := append(collectCodexVibeSessions(scanDirs), collectClaudeVibeSessions(scanDirs)...)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	if len(sessions) > agentVibeSummaryMaxSessions {
		sessions = sessions[:agentVibeSummaryMaxSessions]
	}
	return sessions
}

func collectCodexVibeSessions(scanDirs []string) []models.AgentVibeSession {
	home := agentHome()
	if home == "" {
		return nil
	}
	indexPath := filepath.Join(home, ".codex", "session_index.jsonl")
	var sessions []models.AgentVibeSession
	for _, line := range readRecentAgentJSONLLines(indexPath, agentVibeIndexMaxLines, agentVibeIndexMaxBytes) {
		var row struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
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

	sessionFiles := append(
		findRecentAgentFiles(filepath.Join(home, ".codex", "sessions"), "*.jsonl", agentVibeSessionFileScanLimit),
		findRecentAgentFiles(filepath.Join(home, ".codex", "archived_sessions"), "*.jsonl", agentVibeSessionFileScanLimit)...,
	)
	metaByID := make(map[string]models.AgentVibeSession)
	for _, path := range sessionFiles {
		meta := readCodexSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, ScanDirs: scanDirs})
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
	return readCodexSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: maxMessages})
}

func readCodexSessionMetaWithOptions(path string, options agentVibeSessionReadOptions) models.AgentVibeSession {
	file, err := os.Open(path)
	if err != nil {
		return models.AgentVibeSession{}
	}
	defer file.Close()

	options = normalizeAgentVibeSessionReadOptions(options)
	window := newAgentVibeTranscriptWindow(options)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var session models.AgentVibeSession
	session.Provider = "codex"
	session.Tool = "codex"
	session.Mode = "vibe"
	session.Status = "closed"
	for scanner.Scan() {
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
			// 早过滤：扫描目录限制开启时，cwd 不在任一目录内则丢弃整个会话，不读 transcript
			if len(options.ScanDirs) > 0 && session.ProjectPath != "" && !pathUnderAnyScanDir(session.ProjectPath, options.ScanDirs) {
				return models.AgentVibeSession{}
			}
			continue
		}
		if msg := parseCodexTranscriptMessage(line, session.MessageCount); msg.Content != "" {
			session.MessageCount++
			if session.Title == "" && msg.Role == "user" {
				session.Title = truncateAgentText(msg.Content, 200)
			}
			window.add(msg)
		}
	}
	if session.ID == "" {
		return models.AgentVibeSession{}
	}
	session.Transcript, session.TranscriptPage = window.page(session.MessageCount, options.IncludePageMeta)
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
		role = codexTranscriptRole(payload.Role, payload.Message.Role, payload.Content, payload.Message.Content)
		if payload.Content != nil {
			content = agentContentText(payload.Content)
		}
		if content == "" {
			content = agentContentText(payload.Message.Content)
		}
	case "response_item":
		var payload struct {
			Type    string      `json:"type"`
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
			Item    struct {
				Type    string      `json:"type"`
				Role    string      `json:"role"`
				Content interface{} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return models.AgentVibeMessage{}
		}
		role = codexTranscriptRole(payload.Role, payload.Item.Role, payload.Content, payload.Item.Content)
		content = agentContentText(payload.Content)
		if content == "" {
			content = agentContentText(payload.Item.Content)
		}
		if role == "" && (payload.Type == "message" || payload.Item.Type == "message") {
			role = "assistant"
		}
	case "event_msg":
		var payload struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return models.AgentVibeMessage{}
		}
		switch payload.Type {
		case "user_message":
			role = "user"
			content = payload.Message
		case "agent_message":
			role = "assistant"
			content = payload.Message
		default:
			return models.AgentVibeMessage{}
		}
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
		Index:     index,
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

func codexTranscriptRole(primaryRole string, nestedRole string, primaryContent interface{}, nestedContent interface{}) string {
	explicitRole := normalizeAgentVibeRole(firstNonEmpty(primaryRole, nestedRole))
	if explicitRole == "system" {
		return explicitRole
	}
	if contentRole := firstNonEmpty(inferAgentVibeRoleFromContent(primaryContent), inferAgentVibeRoleFromContent(nestedContent)); contentRole != "" {
		return contentRole
	}
	return explicitRole
}

func inferAgentVibeRoleFromContent(value interface{}) string {
	switch typed := value.(type) {
	case []interface{}:
		seen := make(map[string]bool)
		for _, item := range typed {
			if role := inferAgentVibeRoleFromContent(item); role != "" {
				seen[role] = true
			}
		}
		switch {
		case seen["assistant"]:
			return "assistant"
		case seen["user"]:
			return "user"
		case seen["system"]:
			return "system"
		default:
			return ""
		}
	case map[string]interface{}:
		switch strings.ToLower(strings.TrimSpace(fmt.Sprint(typed["type"]))) {
		case "output_text", "output", "assistant_message":
			return "assistant"
		case "input_text", "input", "user_message":
			return "user"
		case "system_text", "developer_text", "tool_result", "tool_use", "server_tool_use":
			return "system"
		}
		if nested, ok := typed["content"]; ok {
			return inferAgentVibeRoleFromContent(nested)
		}
	}
	return ""
}

func collectClaudeVibeSessions(scanDirs []string) []models.AgentVibeSession {
	home := agentHome()
	if home == "" {
		return nil
	}
	root := filepath.Join(home, ".claude", "projects")
	indexFiles := findRecentAgentFiles(root, "sessions-index.json", agentVibeIndexFileScanLimit)
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
				CustomTitle  string `json:"customTitle"`
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
			// 早过滤：扫描目录限制开启时，projectPath 不在目录内则跳过（连 session 文件都不读）
			if len(scanDirs) > 0 && !pathUnderAnyScanDir(cleanAgentProjectPath(projectPath), scanDirs) {
				continue
			}
			session := models.AgentVibeSession{
				ID:           "claude_" + entry.SessionID,
				Provider:     "claude",
				Tool:         "claude",
				ProjectPath:  cleanAgentProjectPath(projectPath),
				Title:        truncateAgentText(firstNonEmpty(entry.CustomTitle, entry.Summary, entry.FirstPrompt), 200),
				Summary:      truncateAgentText(firstNonEmpty(entry.Summary, entry.CustomTitle), 500),
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
	for _, path := range findRecentAgentFiles(root, "*.jsonl", agentVibeSessionFileScanLimit) {
		if strings.Contains(path, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
			continue
		}
		session := readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: agentVibeTranscriptMaxMessages, ScanDirs: scanDirs})
		if session.ID == "" || seen[session.ID] {
			continue
		}
		sessions = append(sessions, session)
		seen[session.ID] = true
	}
	// Claude Code persists the conversation title the user set via /rename in two
	// places: sessions-index.json entries ("customTitle", durable — observed
	// retaining months-old renames, but only written for projects Claude Code has
	// indexed) and ~/.claude/sessions/<pid>.json ("name", retained only for
	// recent/active processes). The index pass above already prefers customTitle
	// over summary/firstPrompt; this overlay runs last so the freshest rename of
	// a live process still wins, and renamed conversations in projects without a
	// sessions-index.json keep working.
	applyClaudeRenameNames(sessions, loadClaudeRenameNames(home))
	return sessions
}

// loadClaudeRenameNames reads ~/.claude/sessions/*.json and returns a map from
// native Claude Code sessionId to the conversation title the user set via
// /rename (the "name" field of each per-process session record). Files are
// named by PID and are only retained for recent/active processes, so this
// overlay covers the freshest renames; historical renamed conversations are
// covered by the durable "customTitle" in sessions-index.json, which the index
// pass already prefers over summary/firstPrompt.
func loadClaudeRenameNames(home string) map[string]string {
	out := map[string]string{}
	if home = strings.TrimSpace(home); home == "" {
		return out
	}
	files, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	if err != nil || len(files) == 0 {
		return out
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var row struct {
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			continue
		}
		if name := strings.TrimSpace(row.Name); row.SessionID != "" && name != "" {
			out[row.SessionID] = name
		}
	}
	return out
}

// applyClaudeRenameNames overlays user-set /rename titles onto the collected
// Claude sessions, keyed by native sessionId (the "claude_"-stripped session ID).
func applyClaudeRenameNames(sessions []models.AgentVibeSession, renameNames map[string]string) {
	if len(renameNames) == 0 {
		return
	}
	for index := range sessions {
		if !strings.HasPrefix(sessions[index].ID, "claude_") {
			continue
		}
		nativeID := strings.TrimPrefix(sessions[index].ID, "claude_")
		name := strings.TrimSpace(renameNames[nativeID])
		if name == "" {
			continue
		}
		sessions[index].Title = truncateAgentText(name, 200)
		if sessions[index].Summary == "" {
			sessions[index].Summary = truncateAgentText(name, 500)
		}
	}
}

func readClaudeSessionMeta(path string) models.AgentVibeSession {
	return readClaudeSessionMetaWithLimit(path, agentVibeTranscriptMaxMessages)
}

func readClaudeSessionMetaWithLimit(path string, maxMessages int) models.AgentVibeSession {
	return readClaudeSessionMetaWithOptions(path, agentVibeSessionReadOptions{Limit: maxMessages})
}

func readClaudeSessionMetaWithOptions(path string, options agentVibeSessionReadOptions) models.AgentVibeSession {
	file, err := os.Open(path)
	if err != nil {
		return models.AgentVibeSession{}
	}
	defer file.Close()

	options = normalizeAgentVibeSessionReadOptions(options)
	window := newAgentVibeTranscriptWindow(options)
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
		// 早过滤：扫描目录限制开启时，cwd 不在任一目录内则丢弃整个会话，不读 transcript
		if len(options.ScanDirs) > 0 && session.ProjectPath != "" && !pathUnderAnyScanDir(session.ProjectPath, options.ScanDirs) {
			return models.AgentVibeSession{}
		}
		if session.Branch == "" {
			session.Branch = row.GitBranch
		}
		if session.CreatedAt == "" {
			session.CreatedAt = normalizeAgentTime(row.Timestamp)
		}
		if row.Type == "user" || row.Type == "assistant" {
			if text := truncateAgentText(claudeMessageText(row.Message), agentVibeTranscriptMaxContentRunes); text != "" {
				messageIndex := session.MessageCount
				session.MessageCount++
				msg := models.AgentVibeMessage{
					ID:        stableAgentID("msg", fmt.Sprintf("%s:%d:%s", row.Timestamp, messageIndex, text)),
					Role:      firstNonEmpty(inferAgentVibeRoleFromClaudeMessage(row.Message), normalizeAgentVibeRole(row.Type)),
					Content:   text,
					Timestamp: normalizeAgentTime(row.Timestamp),
					Index:     messageIndex,
				}
				window.add(msg)
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
	session.Transcript, session.TranscriptPage = window.page(session.MessageCount, options.IncludePageMeta)
	session.UpdatedAt = fileUpdatedAt(path)
	return session
}

func normalizeAgentVibeSessionReadOptions(options agentVibeSessionReadOptions) agentVibeSessionReadOptions {
	if options.Limit <= 0 {
		options.Limit = agentVibeTranscriptMaxMessages
	}
	if options.Limit > agentVibeDetailMaxPageLimit {
		options.Limit = agentVibeDetailMaxPageLimit
	}
	options.BeforeMessageID = strings.TrimSpace(options.BeforeMessageID)
	options.BeforeTimestamp = normalizeAgentTime(options.BeforeTimestamp)
	return options
}

func newAgentVibeTranscriptWindow(options agentVibeSessionReadOptions) *agentVibeTranscriptWindow {
	return &agentVibeTranscriptWindow{
		limit:           options.Limit,
		beforeMessageID: options.BeforeMessageID,
		beforeTimestamp: options.BeforeTimestamp,
		messages:        make([]models.AgentVibeMessage, 0, options.Limit+1),
	}
}

func (window *agentVibeTranscriptWindow) add(message models.AgentVibeMessage) {
	if window == nil || window.limit <= 0 {
		return
	}
	if window.beforeMessageID != "" || window.beforeTimestamp != "" {
		if window.foundBefore {
			return
		}
		if window.beforeMessageID != "" && message.ID == window.beforeMessageID {
			window.foundBefore = true
			return
		}
		if window.beforeTimestamp != "" && compareRFC3339(message.Timestamp, window.beforeTimestamp) >= 0 {
			window.foundBefore = true
			return
		}
	}
	window.messages = append(window.messages, message)
	maxBuffered := window.limit + 1
	if len(window.messages) > maxBuffered {
		window.messages = window.messages[len(window.messages)-maxBuffered:]
	}
}

func (window *agentVibeTranscriptWindow) page(totalCount int, includeMeta bool) ([]models.AgentVibeMessage, *models.AgentVibeTranscriptPage) {
	if window == nil {
		return nil, nil
	}
	messages := window.messages
	hasMore := len(messages) > window.limit
	if hasMore {
		messages = messages[len(messages)-window.limit:]
	}
	out := append([]models.AgentVibeMessage(nil), messages...)
	if !includeMeta {
		return out, nil
	}
	page := &models.AgentVibeTranscriptPage{
		Limit:      window.limit,
		Count:      len(out),
		TotalCount: totalCount,
		HasMore:    hasMore,
		Order:      "asc",
	}
	if hasMore && len(out) > 0 {
		page.NextBeforeMessageID = out[0].ID
	}
	return out, page
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

func inferAgentVibeRoleFromClaudeMessage(value interface{}) string {
	raw, _ := json.Marshal(value)
	var msg struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	explicitRole := normalizeAgentVibeRole(msg.Role)
	contentRole := inferAgentVibeRoleFromContent(msg.Content)
	if explicitRole == "assistant" && contentRole == "system" && agentContentHasPlainText(msg.Content) {
		return "assistant"
	}
	return firstNonEmpty(contentRole, explicitRole)
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

func agentContentHasPlainText(value interface{}) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []interface{}:
		for _, item := range typed {
			if agentContentHasPlainText(item) {
				return true
			}
		}
	case map[string]interface{}:
		itemType := strings.ToLower(strings.TrimSpace(fmt.Sprint(typed["type"])))
		switch itemType {
		case "text", "input_text", "output_text":
			if text, ok := typed["text"].(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
			if content, ok := typed["content"]; ok {
				return agentContentHasPlainText(content)
			}
		case "tool_result", "tool_use", "server_tool_use":
			return false
		default:
			if content, ok := typed["content"]; ok {
				return agentContentHasPlainText(content)
			}
		}
	}
	return false
}

func normalizeAgentVibeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user", "human":
		return "user"
	case "assistant", "ai", "model":
		return "assistant"
	case "system", "developer", "tool", "function":
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
	if home := agentHome(); home != "" {
		homeCleaned := filepath.Clean(home)
		if path == homeCleaned {
			return false
		}
		// ~/Library holds per-app data containers (design tools, IDEs, storage
		// backends) rather than code. Apps like Open Design drive Claude Code with
		// cwd pointing at their internal per-document folders under here, which would
		// otherwise surface as bogus UUID-named projects.
		libraryRoot := filepath.Join(homeCleaned, "Library")
		if path == libraryRoot || strings.HasPrefix(path, libraryRoot+string(filepath.Separator)) {
			return false
		}
	}
	// Anything inside (or equal to) a macOS app bundle — a component whose name ends
	// in ".app" — is the application's own resource tree, not a standalone project.
	if isAgentPathInsideAppBundle(path) {
		return false
	}
	return true
}

// isAgentPathInsideAppBundle reports whether any component of path is a macOS app
// bundle (a directory whose name ends in ".app"). Such paths live inside the app's
// bundled resource tree (e.g. /Applications/Foo.app/Contents/Resources/...), not a
// user project, so they are excluded from reported projects.
func isAgentPathInsideAppBundle(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part != "" && strings.HasSuffix(part, ".app") {
			return true
		}
	}
	return false
}

func findRecentAgentFiles(root string, pattern string, limit int) []string {
	if limit <= 0 {
		limit = agentVibeSessionFileScanLimit
	}
	candidates := make([]agentRecentFileCandidate, 0, minInt(limit, 64))
	stack := []string{root}
	startedAt := time.Now()
	visitedEntries := 0
	for len(stack) > 0 {
		if shouldStopAgentRecentFileWalk(startedAt, visitedEntries) {
			break
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, entry := range entries {
			visitedEntries++
			if shouldStopAgentRecentFileWalk(startedAt, visitedEntries) {
				break
			}
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if shouldSkipAgentRecentFileDir(entry.Name()) {
					continue
				}
				stack = append(stack, path)
				continue
			}
			ok, err := filepath.Match(pattern, entry.Name())
			if err != nil || !ok {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			addAgentRecentFileCandidate(&candidates, agentRecentFileCandidate{path: path, modTime: info.ModTime()}, limit)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.path)
	}
	return out
}

func addAgentRecentFileCandidate(candidates *[]agentRecentFileCandidate, next agentRecentFileCandidate, limit int) {
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

func shouldStopAgentRecentFileWalk(startedAt time.Time, visitedEntries int) bool {
	if agentRecentFileWalkMaxEntries > 0 && visitedEntries >= agentRecentFileWalkMaxEntries {
		return true
	}
	return agentRecentFileWalkMaxDuration > 0 && time.Since(startedAt) >= agentRecentFileWalkMaxDuration
}

func shouldSkipAgentRecentFileDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", ".next", "dist", "build", "target", ".cache", ".venv", "vendor",
		"coverage", ".turbo", ".parcel-cache", ".pytest_cache", ".mypy_cache", ".ruff_cache",
		".gradle", ".idea", ".dart_tool", ".serverless", ".terraform", ".pnpm-store", ".yarn",
		"deriveddata", "tmp", "temp":
		return true
	default:
		return false
	}
}

func readRecentAgentJSONLLines(path string, maxLines int, maxBytes int64) [][]byte {
	if maxLines <= 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = agentVibeIndexMaxBytes
	}
	start := int64(0)
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	if start > 0 {
		if newline := bytes.IndexByte(raw, '\n'); newline >= 0 {
			raw = raw[newline+1:]
		}
	}
	parts := bytes.Split(raw, []byte("\n"))
	lines := make([][]byte, 0, minInt(maxLines, len(parts)))
	for i := len(parts) - 1; i >= 0 && len(lines) < maxLines; i-- {
		line := bytes.TrimSpace(parts[i])
		if len(line) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
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
