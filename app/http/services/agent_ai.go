package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"aliang.one/nursorgate/common/cache"
)

type agentAIManager struct {
	mu       sync.Mutex
	sessions map[string]*agentAISession
}

type agentAISession struct {
	id          string
	mode        string
	projectPath string
	cancel      context.CancelFunc
	runSeq      int
}

type agentAITool struct {
	id   string
	path string
	args []string
}

func newAgentAIManager() *agentAIManager {
	return &agentAIManager{
		sessions: make(map[string]*agentAISession),
	}
}

func (m *agentAIManager) create(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.create missing session_id")))
		return
	}
	projectPath, err := resolveAgentAICWD(remoteString(msg, "project_path"))
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", err))
		return
	}
	mode := remoteString(msg, "mode")
	if mode == "" {
		mode = "vibe"
	}

	m.mu.Lock()
	m.sessions[sessionID] = &agentAISession{
		id:          sessionID,
		mode:        mode,
		projectPath: projectPath,
	}
	m.mu.Unlock()

	_ = writeJSON(map[string]interface{}{
		"type":         "ai.session.created",
		"session_id":   sessionID,
		"mode":         mode,
		"project_path": projectPath,
	})
}

func (m *agentAIManager) message(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	messageID := remoteString(msg, "message_id")
	content := strings.TrimSpace(remoteString(msg, "content"))
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", messageID, errors.New("ai.message missing session_id")))
		return
	}
	if messageID == "" {
		messageID = sessionID
	}
	if content == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, errors.New("ai.message content is empty")))
		return
	}

	session := m.get(sessionID)
	if session == nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session not found: %s", sessionID)))
		return
	}

	m.mu.Lock()
	if session.cancel != nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session is already running: %s", sessionID)))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	session.runSeq++
	runSeq := session.runSeq
	m.mu.Unlock()

	go m.runCLI(ctx, session, runSeq, messageID, content, writeJSON)
}

func (m *agentAIManager) stop(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.stop missing session_id")))
		return
	}

	session := m.get(sessionID)
	if session == nil {
		_ = writeJSON(map[string]interface{}{
			"type":       "ai.status",
			"session_id": sessionID,
			"status":     "stopped",
		})
		return
	}

	m.mu.Lock()
	cancel := session.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = writeJSON(map[string]interface{}{
		"type":       "ai.status",
		"session_id": sessionID,
		"status":     "stopping",
	})
}

func (m *agentAIManager) closeAll() {
	m.mu.Lock()
	sessions := make([]*agentAISession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*agentAISession)
	m.mu.Unlock()

	for _, session := range sessions {
		if session.cancel != nil {
			session.cancel()
		}
	}
}

func (m *agentAIManager) get(sessionID string) *agentAISession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

func (m *agentAIManager) clearRunning(sessionID string, runSeq int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session != nil && session.runSeq == runSeq {
		session.cancel = nil
	}
}

func (m *agentAIManager) runCLI(ctx context.Context, session *agentAISession, runSeq int, messageID string, content string, writeJSON agentTerminalWriter) {
	defer m.clearRunning(session.id, runSeq)

	tool, err := resolveAgentAITool(content)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}

	cmd := exec.CommandContext(ctx, tool.path, tool.args...)
	cmd.Dir = session.projectPath
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}
	if err := cmd.Start(); err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}

	_ = writeJSON(map[string]interface{}{
		"type":       "ai.delta",
		"session_id": session.id,
		"message_id": agentAssistantMessageID(messageID),
		"delta":      fmt.Sprintf("Running %s in %s\n", tool.id, session.projectPath),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyAIDelta(stdout, session.id, messageID, writeJSON)
	}()
	go func() {
		defer wg.Done()
		copyAIDelta(stderr, session.id, messageID, writeJSON)
	}()
	waitErr := cmd.Wait()
	wg.Wait()

	if ctx.Err() != nil {
		_ = writeJSON(map[string]interface{}{
			"type":       "ai.status",
			"session_id": session.id,
			"status":     "stopped",
		})
		return
	}
	if waitErr != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, waitErr))
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":       "ai.done",
		"session_id": session.id,
		"message_id": agentAssistantMessageID(messageID),
	})
}

func copyAIDelta(reader io.Reader, sessionID string, messageID string, writeJSON agentTerminalWriter) {
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		text := scanner.Text()
		if text != "" {
			text += "\n"
		}
		_ = writeJSON(map[string]interface{}{
			"type":       "ai.delta",
			"session_id": sessionID,
			"message_id": agentAssistantMessageID(messageID),
			"delta":      text,
		})
	}
}

func resolveAgentAITool(prompt string) (*agentAITool, error) {
	if path, err := exec.LookPath("codex"); err == nil {
		return &agentAITool{
			id:   "codex",
			path: path,
			args: []string{"exec", "--skip-git-repo-check", "--color", "never", prompt},
		}, nil
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return &agentAITool{
			id:   "claude",
			path: path,
			args: []string{"--print", "--output-format", "text", prompt},
		}, nil
	}
	if path, err := exec.LookPath("claudecode"); err == nil {
		return &agentAITool{
			id:   "claudecode",
			path: path,
			args: []string{"--print", "--output-format", "text", prompt},
		}, nil
	}
	return nil, errors.New("no supported AI CLI found in PATH: codex, claude, or claudecode")
}

func resolveAgentAICWD(raw string) (string, error) {
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home, nil
		}
		return os.Getwd()
	}
	if expanded, err := cache.ExpandHomePath(cwd); err == nil {
		cwd = expanded
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	stat, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", abs)
	}
	return abs, nil
}

func agentAIErrorPayload(sessionID string, messageID string, err error) map[string]interface{} {
	message := "ai error"
	if err != nil {
		message = err.Error()
	}
	payload := map[string]interface{}{
		"type":       "ai.error",
		"session_id": sessionID,
		"error":      message,
	}
	if messageID != "" {
		payload["message_id"] = agentAssistantMessageID(messageID)
	}
	return payload
}

func agentAssistantMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if strings.HasPrefix(messageID, "assistant_") {
		return messageID
	}
	return "assistant_" + messageID
}
