package services

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

type agentAIManager struct {
	mu                 sync.Mutex
	sessions           map[string]*agentAISession
	approvals          map[string]*agentAIApprovalWaiter
	completedApprovals map[string]*agentAICompletedApproval
}

type agentAISession struct {
	id              string
	mode            string
	projectPath     string
	provider        string
	model           string
	resumeSessionID string
	initialContext  string
	cancel          context.CancelFunc
	activeWriter    agentTerminalWriter
	approvalToken   string
	runSeq          int
	history         []agentAIMessage
}

type agentAIMessage struct {
	Role      string
	MessageID string
	Content   string
	CreatedAt time.Time
}

type agentAIRun struct {
	sessionID       string
	messageID       string
	runSeq          int
	mode            string
	projectPath     string
	provider        string
	model           string
	resumeSessionID string
	prompt          string
	cancel          context.CancelFunc
	approvalToken   string
}

type agentAITool struct {
	id           string
	path         string
	args         []string
	env          []string
	outputFormat agentAIOutputFormat
}

type agentAIApprovalRequest struct {
	ID                 string
	SessionID          string
	MessageID          string
	Provider           string
	Kind               string
	Title              string
	Reason             string
	Command            string
	CWD                string
	ToolName           string
	ToolInput          json.RawMessage
	FileChanges        json.RawMessage
	AvailableDecisions []string
	Raw                json.RawMessage
	respond            chan agentAIApprovalResponse
}

type agentAIApprovalResponse struct {
	Decision string
	Scope    string
	Raw      json.RawMessage
}

type agentAIApprovalWaiter struct {
	sessionID string
	runSeq    int
	request   agentAIApprovalRequest
}

type agentAICompletedApproval struct {
	sessionID string
	runSeq    int
	response  agentAIApprovalResponse
	createdAt time.Time
}

var (
	agentAIApprovalHookBaseURLMu sync.RWMutex
	agentAIApprovalHookBaseURL   = UserAgentBaseURL()
)

type agentAIOutputFormat string

const (
	agentAIOutputText             agentAIOutputFormat = "text"
	agentAIOutputCodexJSON        agentAIOutputFormat = "codex_json"
	agentAIOutputClaudeStreamJSON agentAIOutputFormat = "claude_stream_json"
)

func newAgentAIManager() *agentAIManager {
	return &agentAIManager{
		sessions:           make(map[string]*agentAISession),
		approvals:          make(map[string]*agentAIApprovalWaiter),
		completedApprovals: make(map[string]*agentAICompletedApproval),
	}
}

func (s *AgentService) HandleAIApprovalHook(ctx context.Context, sessionID string, messageID string, token string, payload map[string]interface{}) (map[string]interface{}, error) {
	if s == nil || s.ai == nil {
		return claudeApprovalHookDecision(false, "Aliang AI approval service is not ready."), errors.New("AI approval service is not ready")
	}
	return s.ai.handleClaudeApprovalHook(ctx, sessionID, messageID, token, payload)
}

func SetAgentAIApprovalHookBaseURL(raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return
	}
	agentAIApprovalHookBaseURLMu.Lock()
	agentAIApprovalHookBaseURL = raw
	agentAIApprovalHookBaseURLMu.Unlock()
}

func agentAIApprovalHookURL(sessionID string, messageID string, token string) string {
	agentAIApprovalHookBaseURLMu.RLock()
	base := agentAIApprovalHookBaseURL
	agentAIApprovalHookBaseURLMu.RUnlock()
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = UserAgentBaseURL()
	}
	values := url.Values{}
	values.Set("session_id", sessionID)
	values.Set("message_id", messageID)
	values.Set("token", token)
	return base + "/api/agent/ai/approval-hook?" + values.Encode()
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
	provider := strings.TrimSpace(remoteString(msg, "provider"))
	if provider == "" {
		provider = strings.TrimSpace(remoteString(msg, "tool"))
	}
	provider, err = normalizeAgentAIProvider(provider)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", err))
		return
	}
	model := strings.TrimSpace(remoteString(msg, "model"))
	resumeSessionID := firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id"))
	initialContext := strings.TrimSpace(remoteString(msg, "initial_context"))
	history := remoteAgentAIHistory(msg)
	if initialContext != "" {
		history = append([]agentAIMessage{{
			Role:      "system",
			MessageID: "initial_context",
			Content:   initialContext,
			CreatedAt: time.Now().UTC(),
		}}, history...)
	}

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		existing.mode = mode
		existing.projectPath = projectPath
		existing.provider = provider
		existing.model = model
		existing.resumeSessionID = resumeSessionID
		existing.initialContext = initialContext
		if len(history) > 0 {
			existing.history = trimAgentAIHistory(history)
		}
		m.mu.Unlock()
		_ = writeJSON(agentAISessionCreatedPayload(existing))
		return
	}
	if len(m.sessions) >= agentMaxAISessions {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, "", fmt.Errorf("ai session limit reached: %d", agentMaxAISessions)))
		return
	}
	session := &agentAISession{
		id:              sessionID,
		mode:            mode,
		projectPath:     projectPath,
		provider:        provider,
		model:           model,
		resumeSessionID: resumeSessionID,
		initialContext:  initialContext,
		history:         trimAgentAIHistory(history),
	}
	m.sessions[sessionID] = session
	m.mu.Unlock()

	_ = writeJSON(agentAISessionCreatedPayload(session))
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
	if len(content) > agentAIMessageLimitBytes {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai.message exceeds %d bytes", agentAIMessageLimitBytes)))
		return
	}

	now := time.Now().UTC()
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session not found: %s", sessionID)))
		return
	}
	if session.cancel != nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session is already running: %s", sessionID)))
		return
	}
	provider, err := normalizeAgentAIProvider(firstNonEmpty(strings.TrimSpace(remoteString(msg, "provider")), strings.TrimSpace(remoteString(msg, "tool")), session.provider))
	if err != nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	approvalToken, err := newAgentAIApprovalToken()
	if err != nil {
		m.mu.Unlock()
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentAIRunTimeout)
	session.cancel = cancel
	session.activeWriter = writeJSON
	session.approvalToken = approvalToken
	session.runSeq++
	resumeSessionID := strings.TrimSpace(session.resumeSessionID)
	session.history = append(session.history, agentAIMessage{
		Role:      "user",
		MessageID: messageID,
		Content:   content,
		CreatedAt: now,
	})
	prompt := buildAgentAIPrompt(session, content)
	if resumeSessionID != "" {
		prompt = content
	}
	run := agentAIRun{
		sessionID:       session.id,
		messageID:       messageID,
		runSeq:          session.runSeq,
		mode:            session.mode,
		projectPath:     session.projectPath,
		provider:        provider,
		model:           session.model,
		resumeSessionID: resumeSessionID,
		prompt:          prompt,
		cancel:          cancel,
		approvalToken:   approvalToken,
	}
	m.mu.Unlock()

	go m.runCLI(ctx, run, writeJSON)
}

func agentAISessionCreatedPayload(session *agentAISession) map[string]interface{} {
	payload := map[string]interface{}{
		"type":         models.AgentEventAISessionCreated,
		"session_id":   session.id,
		"mode":         session.mode,
		"project_path": session.projectPath,
		"provider":     session.provider,
		"model":        session.model,
		"state":        "idle",
	}
	if session.resumeSessionID != "" {
		payload["resume_session_id"] = session.resumeSessionID
	}
	return payload
}

func remoteAgentAIHistory(msg map[string]interface{}) []agentAIMessage {
	raw, ok := msg["transcript"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	history := make([]agentAIMessage, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		content := strings.TrimSpace(remoteString(row, "content"))
		if content == "" {
			continue
		}
		history = append(history, agentAIMessage{
			Role:      remoteString(row, "role"),
			MessageID: remoteString(row, "id"),
			Content:   content,
			CreatedAt: time.Now().UTC(),
		})
	}
	return history
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

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": sessionID,
			"status":     "stopped",
		})
		return
	}
	cancel := session.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIStatus,
		"session_id": sessionID,
		"status":     "stopping",
	})
}

func (m *agentAIManager) approval(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	sessionID := remoteString(msg, "session_id")
	approvalID := remoteString(msg, "approval_id")
	decision := normalizeAgentAIApprovalDecision(remoteString(msg, "decision"))
	if sessionID == "" || approvalID == "" {
		if writeJSON != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), errors.New("ai.approval.response missing session_id or approval_id")))
		}
		return
	}
	if decision == "" {
		if writeJSON != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), fmt.Errorf("unsupported approval decision: %s", remoteString(msg, "decision"))))
		}
		return
	}

	raw := marshalAgentAIRaw(msg["raw"])
	response := agentAIApprovalResponse{
		Decision: decision,
		Scope:    normalizeAgentAIApprovalScope(remoteString(msg, "scope")),
		Raw:      raw,
	}

	approvalKey := agentAIApprovalMapKey(sessionID, approvalID)
	m.mu.Lock()
	if completed := m.completedApprovals[approvalKey]; completed != nil && completed.sessionID == sessionID {
		m.mu.Unlock()
		return
	}
	waiter := m.approvals[approvalKey]
	if waiter != nil && waiter.sessionID == sessionID {
		delete(m.approvals, approvalKey)
		m.rememberCompletedApprovalLocked(approvalID, sessionID, waiter.runSeq, response)
	}
	m.mu.Unlock()
	if waiter == nil || waiter.sessionID != sessionID {
		if writeJSON != nil {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIStatus,
				"session_id": sessionID,
				"status":     "approval_not_found",
			})
		}
		return
	}

	waiter.request.respond <- response
}

func (m *agentAIManager) close(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.close missing session_id")))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session != nil {
		delete(m.sessions, sessionID)
		m.clearPendingApprovalsLocked(sessionID, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	var cancel context.CancelFunc
	if session != nil {
		cancel = session.cancel
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAISessionClosed,
		"session_id": sessionID,
	})
}

func (m *agentAIManager) closeAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session.cancel != nil {
			cancels = append(cancels, session.cancel)
		}
		m.clearPendingApprovalsLocked(session.id, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	m.sessions = make(map[string]*agentAISession)
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

func (m *agentAIManager) requestApproval(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, req agentAIApprovalRequest) (agentAIApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if writeJSON == nil {
		return agentAIApprovalResponse{}, errors.New("approval writer is unavailable")
	}
	req.SessionID = run.sessionID
	req.MessageID = agentAssistantMessageID(run.messageID)
	req.Provider = firstNonEmpty(req.Provider, run.provider)
	if req.ID == "" {
		req.ID = newAgentAIApprovalID(run.sessionID, run.runSeq)
	}
	if req.Kind == "" {
		req.Kind = models.AgentAIApprovalKindTool
	}
	if len(req.AvailableDecisions) == 0 {
		req.AvailableDecisions = []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		}
	}
	req.respond = make(chan agentAIApprovalResponse, 1)

	m.mu.Lock()
	if m.approvals == nil {
		m.approvals = make(map[string]*agentAIApprovalWaiter)
	}
	approvalKey := agentAIApprovalMapKey(run.sessionID, req.ID)
	m.approvals[approvalKey] = &agentAIApprovalWaiter{
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		request:   req,
	}
	m.mu.Unlock()

	_ = writeJSON(agentAIApprovalRequestPayload(req))

	select {
	case response := <-req.respond:
		_ = writeJSON(map[string]interface{}{
			"type":        models.AgentEventAIApprovalRequest,
			"session_id":  run.sessionID,
			"message_id":  agentAssistantMessageID(run.messageID),
			"approval_id": req.ID,
			"provider":    req.Provider,
			"kind":        req.Kind,
			"status":      "resolved",
			"decision":    response.Decision,
		})
		return response, nil
	case <-ctx.Done():
		m.mu.Lock()
		if waiter := m.approvals[approvalKey]; waiter != nil && waiter.sessionID == run.sessionID && waiter.runSeq == run.runSeq {
			delete(m.approvals, approvalKey)
			m.rememberCompletedApprovalLocked(req.ID, run.sessionID, run.runSeq, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionCancel})
		}
		m.mu.Unlock()
		return agentAIApprovalResponse{}, ctx.Err()
	}
}

func (m *agentAIManager) handleClaudeApprovalHook(ctx context.Context, sessionID string, messageID string, token string, raw map[string]interface{}) (map[string]interface{}, error) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	token = strings.TrimSpace(token)
	if sessionID == "" || token == "" {
		return claudeApprovalHookDecision(false, "Aliang approval hook is missing session or token."), errors.New("approval hook missing session or token")
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil || session.approvalToken == "" || session.approvalToken != token || session.activeWriter == nil || session.cancel == nil {
		m.mu.Unlock()
		return claudeApprovalHookDecision(false, "Aliang could not match this permission request to a running AI session."), fmt.Errorf("approval hook session mismatch: %s", sessionID)
	}
	run := agentAIRun{
		sessionID:     session.id,
		messageID:     firstNonEmpty(messageID, session.id),
		runSeq:        session.runSeq,
		mode:          session.mode,
		projectPath:   session.projectPath,
		provider:      session.provider,
		model:         session.model,
		cancel:        session.cancel,
		approvalToken: session.approvalToken,
	}
	writeJSON := session.activeWriter
	m.mu.Unlock()

	req := buildClaudeApprovalRequest(run, raw)
	approvalCtx, cancel := context.WithTimeout(ctx, 9*time.Minute)
	defer cancel()
	response, err := m.requestApproval(approvalCtx, run, writeJSON, req)
	if err != nil {
		return claudeApprovalHookDecision(false, "Aliang approval timed out or was cancelled."), err
	}
	switch response.Decision {
	case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
		return claudeApprovalHookDecision(true, "Approved in Aliang."), nil
	default:
		return claudeApprovalHookDecision(false, "Denied in Aliang."), nil
	}
}

func buildClaudeApprovalRequest(run agentAIRun, raw map[string]interface{}) agentAIApprovalRequest {
	rawJSON := marshalAgentAIRaw(raw)
	toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
	toolInput := marshalAgentAIRaw(raw["tool_input"])
	if len(toolInput) == 0 {
		toolInput = marshalAgentAIRaw(raw["toolInput"])
	}
	command := claudeApprovalCommand(raw)
	kind := models.AgentAIApprovalKindTool
	if strings.EqualFold(toolName, "bash") || command != "" {
		kind = models.AgentAIApprovalKindCommand
	}
	reason := firstNonEmpty(
		remoteString(raw, "permission_prompt"),
		remoteString(raw, "permissionPrompt"),
		remoteString(raw, "reason"),
	)
	title := "Approve Claude Code tool use"
	if kind == models.AgentAIApprovalKindCommand {
		title = "Approve Claude Code command"
	}
	return agentAIApprovalRequest{
		ID:        newAgentAIApprovalID(run.sessionID, run.runSeq),
		Provider:  run.provider,
		Kind:      kind,
		Title:     title,
		Reason:    reason,
		Command:   command,
		CWD:       firstNonEmpty(remoteString(raw, "cwd"), run.projectPath),
		ToolName:  toolName,
		ToolInput: toolInput,
		Raw:       rawJSON,
		AvailableDecisions: []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		},
	}
}

func claudeApprovalCommand(raw map[string]interface{}) string {
	for _, key := range []string{"command", "cmd"} {
		if value := strings.TrimSpace(remoteString(raw, key)); value != "" {
			return value
		}
	}
	for _, key := range []string{"tool_input", "toolInput", "input"} {
		row, ok := raw[key].(map[string]interface{})
		if !ok {
			continue
		}
		if value := firstNonEmpty(remoteString(row, "command"), remoteString(row, "cmd")); value != "" {
			return value
		}
	}
	return ""
}

func claudeApprovalHookDecision(allow bool, reason string) map[string]interface{} {
	decision := "deny"
	if allow {
		decision = "allow"
	}
	return map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":             "PermissionRequest",
			"permissionDecision":        decision,
			"permissionDecisionReason":  reason,
			"suppressOutputForApprover": false,
		},
	}
}

func (m *agentAIManager) clearPendingApprovalsLocked(sessionID string, runSeq int, decision string) {
	for approvalKey, waiter := range m.approvals {
		if waiter == nil || waiter.sessionID != sessionID || waiter.runSeq != runSeq {
			continue
		}
		delete(m.approvals, approvalKey)
		response := agentAIApprovalResponse{Decision: decision}
		m.rememberCompletedApprovalLocked(waiter.request.ID, sessionID, runSeq, response)
		waiter.request.respond <- response
	}
}

func (m *agentAIManager) rememberCompletedApprovalLocked(approvalID string, sessionID string, runSeq int, response agentAIApprovalResponse) {
	if approvalID == "" || sessionID == "" {
		return
	}
	if m.completedApprovals == nil {
		m.completedApprovals = make(map[string]*agentAICompletedApproval)
	}
	if len(m.completedApprovals) > 1024 {
		m.completedApprovals = make(map[string]*agentAICompletedApproval)
	}
	m.completedApprovals[agentAIApprovalMapKey(sessionID, approvalID)] = &agentAICompletedApproval{
		sessionID: sessionID,
		runSeq:    runSeq,
		response:  response,
		createdAt: time.Now().UTC(),
	}
}

func agentAIApprovalMapKey(sessionID string, approvalID string) string {
	return sessionID + "\x00" + approvalID
}

func (m *agentAIManager) clearRunning(sessionID string, runSeq int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session != nil && session.runSeq == runSeq {
		session.cancel = nil
		session.activeWriter = nil
		session.approvalToken = ""
		session.history = trimAgentAIHistory(session.history)
	}
	m.clearPendingApprovalsLocked(sessionID, runSeq, models.AgentAIApprovalDecisionCancel)
}

func (m *agentAIManager) appendAssistantHistory(sessionID string, runSeq int, messageID string, output string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		return
	}
	session.history = append(session.history, agentAIMessage{
		Role:      "assistant",
		MessageID: agentAssistantMessageID(messageID),
		Content:   output,
		CreatedAt: time.Now().UTC(),
	})
	session.history = trimAgentAIHistory(session.history)
}

// agentAIRunOutcome reports how a single CLI pass finished.
const (
	agentAIRunDone agentAIRunOutcome = iota
	// agentAIRunResumeMissing means the CLI could not find the requested
	// --resume session locally (it is absent, or filed under a different
	// project path than the run's cwd). The pass emitted no assistant output;
	// the caller should retry the run fresh, without --resume.
	agentAIRunResumeMissing
)

type agentAIRunOutcome int

func (m *agentAIManager) runCLI(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) {
	defer m.clearRunning(run.sessionID, run.runSeq)
	if run.cancel != nil {
		defer run.cancel()
	}

	if agentAIUseCodexAppServer(run.provider) {
		_ = m.runCodexAppServer(ctx, run, writeJSON)
		return
	}

	// Try to resume the prior Claude/Codex session when one is referenced. If
	// that session does not exist locally (an imported/foreign session, or one
	// filed under a different project path than this run's cwd), the CLI exits
	// with "No conversation found with session ID" before emitting any output.
	// Retry the run fresh in that case so the conversation still streams,
	// rather than surfacing a hard error to the user.
	if strings.TrimSpace(run.resumeSessionID) != "" {
		if m.runCLIPass(ctx, run, writeJSON, true) != agentAIRunResumeMissing {
			return
		}
	}
	_ = m.runCLIPass(ctx, run, writeJSON, false)
}

func agentAIUseCodexAppServer(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "codex" {
		return true
	}
	if provider == "auto" {
		_, err := exec.LookPath("codex")
		return err == nil
	}
	return false
}

func (m *agentAIManager) runCodexAppServer(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) agentAIRunOutcome {
	path, err := exec.LookPath("codex")
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI CLI %q was not found in PATH", "codex")))
		return agentAIRunDone
	}
	cmd := exec.CommandContext(ctx, path, "app-server", "--stdio")
	cmd.Dir = run.projectPath
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if err := cmd.Start(); err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     "codex",
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	})

	var stderrBuf strings.Builder
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		captureAgentAIStderr(stderr, &stderrBuf)
	}()

	var writeMu sync.Mutex
	send := func(payload map[string]interface{}) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := stdin.Write(append(raw, '\n')); err != nil {
			return err
		}
		return nil
	}

	_ = send(map[string]interface{}{
		"method": "initialize",
		"id":     0,
		"params": map[string]interface{}{
			"clientInfo": map[string]interface{}{
				"name":    "alianggate",
				"title":   "Aliang Agent",
				"version": "0.1.0",
			},
			"capabilities": nil,
		},
	})
	_ = send(map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}})
	threadParams := map[string]interface{}{
		"cwd":                   run.projectPath,
		"runtimeWorkspaceRoots": []string{run.projectPath},
		"approvalPolicy":        "on-request",
		"approvalsReviewer":     "user",
		"sandbox":               "workspace-write",
	}
	threadMethod := "thread/start"
	if strings.TrimSpace(run.resumeSessionID) != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = strings.TrimSpace(run.resumeSessionID)
	}
	if model := normalizeAgentAIModel(run.model); model != "" {
		threadParams["model"] = model
	}
	_ = send(map[string]interface{}{"method": threadMethod, "id": 1, "params": threadParams})

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	limiter := &agentAIOutputLimiter{meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes))}
	var outMu sync.Mutex
	var output strings.Builder
	capture := func(text string) {
		outMu.Lock()
		appendAgentAIHistoryCapture(&output, text)
		outMu.Unlock()
	}
	threadID := ""
	turnStarted := false
	completed := false
	for scanner.Scan() {
		var msg map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if method := remoteString(msg, "method"); method != "" {
			if _, hasID := msg["id"]; hasID {
				id := msg["id"]
				params, _ := msg["params"].(map[string]interface{})
				result, err := m.codexAppServerApprovalResult(ctx, run, writeJSON, method, params)
				if err != nil {
					_ = send(map[string]interface{}{
						"id":    id,
						"error": map[string]interface{}{"code": -32000, "message": err.Error()},
					})
				} else {
					_ = send(map[string]interface{}{"id": id, "result": result})
				}
				continue
			}
			switch method {
			case "item/agentMessage/delta":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if !emitAIDelta(remoteString(params, "delta"), run, "assistant", writeJSON, limiter, capture) {
						if run.cancel != nil {
							run.cancel()
						}
					}
				}
			case "turn/completed":
				completed = true
				goto done
			case "error":
				if params, ok := msg["params"].(map[string]interface{}); ok {
					_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("%s", firstNonEmpty(remoteString(params, "message"), remoteString(params, "error")))))
				}
			}
			continue
		}
		if fmt.Sprint(msg["id"]) == "1" && threadID == "" {
			if result, ok := msg["result"].(map[string]interface{}); ok {
				if thread, ok := result["thread"].(map[string]interface{}); ok {
					threadID = remoteString(thread, "id")
				}
			}
			if threadID != "" {
				turnStarted = true
				_ = send(map[string]interface{}{
					"method": "turn/start",
					"id":     2,
					"params": map[string]interface{}{
						"threadId": threadID,
						"input": []map[string]interface{}{
							{"type": "text", "text": run.prompt, "text_elements": []interface{}{}},
						},
						"approvalPolicy":    "on-request",
						"approvalsReviewer": "user",
					},
				})
			}
		}
	}

done:
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	stderrWG.Wait()
	if ctx.Err() != nil {
		status := "stopped"
		if limiter.Exceeded() {
			status = "output_limited"
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI output exceeded rate limit (%d bytes per %s) or lifetime cap (%d bytes)", agentAIOutputRateBytes, agentAIOutputRateWindow, agentAIOutputCapBytes)))
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "timeout"
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI run exceeded timeout %s", agentAIRunTimeout)))
		}
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		})
		return agentAIRunDone
	}
	if !completed {
		if err := scanner.Err(); err != nil {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
			return agentAIRunDone
		}
		if waitErr != nil && !turnStarted {
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("%w: %s", waitErr, strings.TrimSpace(stderrBuf.String()))))
			return agentAIRunDone
		}
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
	})
	return agentAIRunDone
}

func (m *agentAIManager) codexAppServerApprovalResult(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, method string, params map[string]interface{}) (map[string]interface{}, error) {
	req := buildCodexApprovalRequest(run, method, params)
	approvalCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	response, err := m.requestApproval(approvalCtx, run, writeJSON, req)
	if err != nil {
		return nil, err
	}
	return codexApprovalResponseResult(method, params, response)
}

func buildCodexApprovalRequest(run agentAIRun, method string, params map[string]interface{}) agentAIApprovalRequest {
	rawJSON := marshalAgentAIRaw(params)
	req := agentAIApprovalRequest{
		ID:       firstNonEmpty(remoteString(params, "approvalId"), remoteString(params, "itemId"), remoteString(params, "callId"), newAgentAIApprovalID(run.sessionID, run.runSeq)),
		Provider: "codex",
		Kind:     models.AgentAIApprovalKindTool,
		Title:    "Approve Codex action",
		Reason:   remoteString(params, "reason"),
		CWD:      firstNonEmpty(remoteString(params, "cwd"), run.projectPath),
		Raw:      rawJSON,
		AvailableDecisions: []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionAcceptForSession,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		},
	}
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		req.Kind = models.AgentAIApprovalKindCommand
		req.Title = "Approve Codex command"
		req.Command = codexApprovalCommand(params)
		req.ToolName = "command"
	case "item/fileChange/requestApproval", "applyPatchApproval":
		req.Kind = models.AgentAIApprovalKindFileChange
		req.Title = "Approve Codex file change"
		req.FileChanges = marshalAgentAIRaw(firstNonNil(params["fileChanges"], params["file_changes"]))
	case "item/permissions/requestApproval":
		req.Kind = models.AgentAIApprovalKindPermissions
		req.Title = "Approve Codex permissions"
		req.ToolName = "permissions"
		req.ToolInput = marshalAgentAIRaw(params["permissions"])
	default:
		req.ToolName = method
	}
	if decisions := codexAvailableDecisions(params["availableDecisions"]); len(decisions) > 0 {
		req.AvailableDecisions = decisions
	}
	return req
}

func codexApprovalCommand(params map[string]interface{}) string {
	if value := strings.TrimSpace(remoteString(params, "command")); value != "" {
		return value
	}
	if raw, ok := params["command"].([]interface{}); ok {
		parts := make([]string, 0, len(raw))
		for _, item := range raw {
			part := strings.TrimSpace(fmt.Sprint(item))
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func codexAvailableDecisions(raw interface{}) []string {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case string:
			if decision := normalizeAgentAIApprovalDecision(value); decision != "" {
				out = append(out, decision)
			}
		case map[string]interface{}:
			for key := range value {
				if decision := normalizeAgentAIApprovalDecision(key); decision != "" {
					out = append(out, decision)
				}
			}
		}
	}
	return out
}

func codexApprovalResponseResult(method string, params map[string]interface{}, response agentAIApprovalResponse) (map[string]interface{}, error) {
	switch method {
	case "execCommandApproval", "applyPatchApproval":
		return map[string]interface{}{"decision": codexLegacyReviewDecision(response.Decision)}, nil
	case "item/commandExecution/requestApproval":
		return map[string]interface{}{"decision": codexCommandDecision(response.Decision)}, nil
	case "item/fileChange/requestApproval":
		return map[string]interface{}{"decision": codexFileChangeDecision(response.Decision)}, nil
	case "item/permissions/requestApproval":
		switch response.Decision {
		case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
			permissions, _ := params["permissions"].(map[string]interface{})
			if permissions == nil {
				permissions = map[string]interface{}{}
			}
			scope := normalizeAgentAIApprovalScope(response.Scope)
			if response.Decision == models.AgentAIApprovalDecisionAcceptForSession {
				scope = "session"
			}
			return map[string]interface{}{"permissions": permissions, "scope": scope}, nil
		default:
			return nil, errors.New("permission request denied by user")
		}
	default:
		if response.Decision == models.AgentAIApprovalDecisionAccept || response.Decision == models.AgentAIApprovalDecisionAcceptForSession {
			return map[string]interface{}{}, nil
		}
		return nil, errors.New("approval denied by user")
	}
}

func codexLegacyReviewDecision(decision string) interface{} {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "approved"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "approved_for_session"
	case models.AgentAIApprovalDecisionCancel:
		return "abort"
	default:
		return "denied"
	}
}

func codexCommandDecision(decision string) interface{} {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "accept"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "acceptForSession"
	case models.AgentAIApprovalDecisionCancel:
		return "cancel"
	default:
		return "decline"
	}
}

func codexFileChangeDecision(decision string) string {
	switch decision {
	case models.AgentAIApprovalDecisionAccept:
		return "accept"
	case models.AgentAIApprovalDecisionAcceptForSession:
		return "acceptForSession"
	case models.AgentAIApprovalDecisionCancel:
		return "cancel"
	default:
		return "decline"
	}
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (m *agentAIManager) runCLIPass(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, allowResume bool) agentAIRunOutcome {
	resumeID := run.resumeSessionID
	if !allowResume {
		resumeID = ""
	}

	tool, err := resolveAgentAITool(run.prompt, run.provider, run.model, resumeID)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if tool.outputFormat == agentAIOutputClaudeStreamJSON {
		tool = withClaudeApprovalHook(tool, run)
	}

	cmd := exec.CommandContext(ctx, tool.path, tool.args...)
	cmd.Dir = run.projectPath
	cmd.Env = os.Environ()
	if len(tool.env) > 0 {
		cmd.Env = append(cmd.Env, tool.env...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}
	if err := cmd.Start(); err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return agentAIRunDone
	}

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     tool.id,
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	})

	var wg sync.WaitGroup
	var outMu sync.Mutex
	var output strings.Builder
	var stderrBuf strings.Builder
	limiter := &agentAIOutputLimiter{meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes))}
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamAgentAIStdout(stdout, tool.outputFormat, run, writeJSON, limiter, func(text string) {
			outMu.Lock()
			appendAgentAIHistoryCapture(&output, text)
			outMu.Unlock()
		})
	}()
	go func() {
		defer wg.Done()
		// Capture the head of stderr (capped) so a stale/missing --resume
		// session can be detected and the run retried fresh; the remainder is
		// drained to keep memory bounded.
		captureAgentAIStderr(stderr, &stderrBuf)
	}()
	waitErr := cmd.Wait()
	wg.Wait()

	if ctx.Err() != nil {
		status := "stopped"
		if limiter.Exceeded() {
			status = "output_limited"
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI output exceeded rate limit (%d bytes per %s) or lifetime cap (%d bytes)", agentAIOutputRateBytes, agentAIOutputRateWindow, agentAIOutputCapBytes)))
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "timeout"
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI run exceeded timeout %s", agentAIRunTimeout)))
		}
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		})
		return agentAIRunDone
	}
	if waitErr != nil {
		// The referenced --resume session is not resolvable in this cwd (e.g.
		// an imported session, or one created under a different project path).
		// Signal the caller to retry without --resume instead of erroring.
		if allowResume && strings.Contains(stderrBuf.String(), "No conversation found with session ID") {
			return agentAIRunResumeMissing
		}
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, waitErr))
		return agentAIRunDone
	}
	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
	})
	return agentAIRunDone
}

// captureAgentAIStderr reads everything from reader, storing the first cap
// bytes into buf and discarding the rest. It never blocks the run on stderr
// volume and bounds memory usage.
func captureAgentAIStderr(reader io.Reader, buf *strings.Builder) {
	const cap = 8 * 1024
	b := make([]byte, 4096)
	for {
		n, err := reader.Read(b)
		if n > 0 && buf.Len() < cap {
			room := cap - buf.Len()
			if room > n {
				room = n
			}
			buf.Write(b[:room])
		}
		if err != nil {
			return
		}
	}
}

// agentAIOutputLimiter bounds AI streamed output with the shared outputMeter.
// Reserve keeps the original (n int) int signature so the streaming call sites
// are unchanged: it returns n when the window has not tripped (emit the full
// chunk) and 0 once a sustained burst exceeds the rate window or the lifetime
// cap, which the caller treats as "stop the run" (it then calls run.cancel()).
type agentAIOutputLimiter struct {
	mu       sync.Mutex
	meter    *outputMeter
	exceeded bool
}

func (l *agentAIOutputLimiter) Reserve(n int) int {
	if l == nil {
		return n
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exceeded {
		return 0
	}
	if l.meter == nil {
		return n
	}
	if l.meter.add(n, time.Now()) {
		l.exceeded = true
		return 0
	}
	return n
}

func (l *agentAIOutputLimiter) Exceeded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exceeded
}

func streamAgentAIStdout(reader io.Reader, format agentAIOutputFormat, run agentAIRun, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) {
	switch format {
	case agentAIOutputCodexJSON, agentAIOutputClaudeStreamJSON:
		streamStructuredAIDelta(reader, format, run, writeJSON, limiter, capture)
	default:
		streamAIDelta(reader, run, "assistant", writeJSON, limiter, capture)
	}
}

func streamStructuredAIDelta(reader io.Reader, format agentAIOutputFormat, run agentAIRun, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) {
	decoder := json.NewDecoder(reader)
	emitted := false
	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			return
		}
		for _, text := range extractStructuredAITexts(format, event, !emitted) {
			if strings.TrimSpace(text) == "" {
				continue
			}
			if !emitAIDelta(text, run, "assistant", writeJSON, limiter, capture) {
				return
			}
			emitted = true
		}
	}
}

func extractStructuredAITexts(format agentAIOutputFormat, event map[string]interface{}, allowFinal bool) []string {
	switch format {
	case agentAIOutputClaudeStreamJSON:
		return extractClaudeStreamTexts(event, allowFinal)
	case agentAIOutputCodexJSON:
		return extractCodexJSONTexts(event, allowFinal)
	default:
		return nil
	}
}

func extractClaudeStreamTexts(event map[string]interface{}, allowFinal bool) []string {
	if remoteString(event, "type") == "stream_event" {
		if streamEvent, ok := event["event"].(map[string]interface{}); ok && remoteString(streamEvent, "type") == "content_block_delta" {
			if delta, ok := streamEvent["delta"].(map[string]interface{}); ok && remoteString(delta, "type") == "text_delta" {
				if text := remoteString(delta, "text"); text != "" {
					return []string{text}
				}
			}
		}
	}
	if !allowFinal {
		return nil
	}
	switch remoteString(event, "type") {
	case "assistant":
		if message, ok := event["message"].(map[string]interface{}); ok {
			if text := claudeMessageContentText(message["content"]); text != "" {
				return []string{text}
			}
		}
	case "result":
		if text := remoteString(event, "result"); text != "" {
			return []string{text}
		}
	}
	return nil
}

func claudeMessageContentText(raw interface{}) string {
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok || remoteString(row, "type") != "text" {
			continue
		}
		builder.WriteString(remoteString(row, "text"))
	}
	return builder.String()
}

func extractCodexJSONTexts(event map[string]interface{}, allowFinal bool) []string {
	eventType := remoteString(event, "type")
	if strings.Contains(eventType, "delta") {
		if text := firstNonEmpty(remoteString(event, "delta"), remoteString(event, "text")); text != "" {
			return []string{text}
		}
		if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
			if text := firstNonEmpty(remoteString(item, "delta"), remoteString(item, "text")); text != "" {
				return []string{text}
			}
		}
	}
	if !allowFinal {
		return nil
	}
	if item, ok := event["item"].(map[string]interface{}); ok && remoteString(item, "type") == "agent_message" {
		if text := remoteString(item, "text"); text != "" {
			return []string{text}
		}
	}
	if eventType == "agent_message" {
		if text := remoteString(event, "text"); text != "" {
			return []string{text}
		}
	}
	return nil
}

func streamAIDelta(reader io.Reader, run agentAIRun, channel string, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			allowed := n
			if limiter != nil {
				allowed = limiter.Reserve(n)
			}
			if allowed <= 0 {
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
			text := string(buf[:allowed])
			emitAIDelta(text, run, channel, writeJSON, nil, capture)
			if allowed < n {
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// captureAgentAIStderr (defined above runCLIPass) is used for AI runs so a
// stale --resume session ("No conversation found with session ID") can be
// detected and the run retried fresh.
func emitAIDelta(text string, run agentAIRun, channel string, writeJSON agentTerminalWriter, limiter *agentAIOutputLimiter, capture func(string)) bool {
	if text == "" {
		return true
	}
	if limiter != nil {
		allowed := limiter.Reserve(len(text))
		if allowed <= 0 {
			if run.cancel != nil {
				run.cancel()
			}
			return false
		}
		if allowed < len(text) {
			text = text[:allowed]
			if run.cancel != nil {
				run.cancel()
			}
		}
	}
	if capture != nil {
		capture(text)
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDelta,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"channel":    channel,
		"delta":      text,
	})
	if limiter != nil && limiter.Exceeded() {
		return false
	}
	return true
}

func buildAgentAIPrompt(session *agentAISession, latestContent string) string {
	history := trimAgentAIHistory(append([]agentAIMessage(nil), session.history...))
	var builder strings.Builder
	builder.WriteString("You are continuing an Aliang remote agent AI chat session.\n")
	builder.WriteString("Use the existing conversation as context and answer the latest user message.\n")
	builder.WriteString("Project path: ")
	builder.WriteString(session.projectPath)
	builder.WriteString("\nMode: ")
	builder.WriteString(session.mode)
	builder.WriteString("\n\nConversation:\n")
	for _, item := range history {
		builder.WriteString(agentAIRoleLabel(item.Role))
		builder.WriteString(": ")
		builder.WriteString(strings.TrimSpace(item.Content))
		builder.WriteString("\n\n")
	}
	if len(history) == 0 {
		builder.WriteString("User: ")
		builder.WriteString(strings.TrimSpace(latestContent))
		builder.WriteString("\n\n")
	}
	builder.WriteString("Assistant:")
	return builder.String()
}

func agentAIRoleLabel(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	case "":
		return "Message"
	default:
		return strings.ToUpper(role[:1]) + role[1:]
	}
}

func trimAgentAIHistory(history []agentAIMessage) []agentAIMessage {
	const maxMessages = 16
	const maxChars = 64000
	if len(history) > maxMessages {
		history = history[len(history)-maxMessages:]
	}
	total := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		total += len(history[i].Content)
		if total > maxChars {
			break
		}
		start = i
	}
	if start > 0 && start < len(history) {
		history = history[start:]
	}
	return history
}

func resolveAgentAITool(prompt string, preferred string, model string, resumeSessionID string) (*agentAITool, error) {
	preferred, err := normalizeAgentAIProvider(preferred)
	if err != nil {
		return nil, err
	}
	if preferred != "auto" {
		return resolveNamedAgentAITool(preferred, prompt, model, resumeSessionID)
	}
	for _, candidate := range []string{"codex", "claude", "claudecode"} {
		if tool, err := resolveNamedAgentAITool(candidate, prompt, model, resumeSessionID); err == nil {
			return tool, nil
		}
	}
	return nil, errors.New("no supported AI CLI found in PATH: codex, claude, or claudecode")
}

func resolveNamedAgentAITool(name string, prompt string, model string, resumeSessionID string) (*agentAITool, error) {
	model = normalizeAgentAIModel(model)
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	switch name {
	case "codex":
		if path, err := exec.LookPath("codex"); err == nil {
			args := []string{"exec"}
			if resumeSessionID != "" {
				args = append(args, "resume")
			}
			args = append(args, "--json", "--skip-git-repo-check")
			if model != "" {
				args = append(args, "--model", model)
			}
			if resumeSessionID != "" {
				args = append(args, resumeSessionID)
			}
			args = append(args, prompt)
			return &agentAITool{
				id:           "codex",
				path:         path,
				args:         args,
				outputFormat: agentAIOutputCodexJSON,
			}, nil
		}
	case "claude":
		if path, err := exec.LookPath("claude"); err == nil {
			return newClaudeCodeAITool("claude", path, prompt, model, resumeSessionID), nil
		}
	case "claudecode":
		if path, err := exec.LookPath("claudecode"); err == nil {
			return newClaudeCodeAITool("claudecode", path, prompt, model, resumeSessionID), nil
		}
		if path, err := exec.LookPath("claude"); err == nil {
			return newClaudeCodeAITool("claudecode", path, prompt, model, resumeSessionID), nil
		}
	default:
		return nil, fmt.Errorf("unsupported AI provider: %s", name)
	}
	return nil, fmt.Errorf("AI CLI %q was not found in PATH", name)
}

func newClaudeCodeAITool(id string, path string, prompt string, model string, resumeSessionID string) *agentAITool {
	args := []string{"--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages"}
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)
	return &agentAITool{
		id:           id,
		path:         path,
		args:         args,
		outputFormat: agentAIOutputClaudeStreamJSON,
	}
}

func withClaudeApprovalHook(tool *agentAITool, run agentAIRun) *agentAITool {
	if tool == nil || strings.TrimSpace(run.approvalToken) == "" {
		return tool
	}
	settingsRaw, err := json.Marshal(map[string]interface{}{
		"hooks": map[string]interface{}{
			"PermissionRequest": []interface{}{
				map[string]interface{}{
					"matcher": "*",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "http",
							"url":     agentAIApprovalHookURL(run.sessionID, agentAssistantMessageID(run.messageID), run.approvalToken),
							"timeout": 600,
						},
					},
				},
			},
		},
	})
	if err != nil {
		return tool
	}
	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	copied.args = append([]string{"--permission-mode", "default", "--settings", string(settingsRaw)}, copied.args...)
	return &copied
}

func normalizeAgentAIModel(model string) string {
	model = strings.TrimSpace(model)
	switch strings.ToLower(model) {
	case "", "auto", "codex", "claude", "claudecode":
		return ""
	default:
		return model
	}
}

func agentAICapabilities() []string {
	caps := []string{"ai_chat", "ai_chat_context", "ai_stream", "ai_approval", "vibe_session"}
	for _, candidate := range []string{"codex", "claude", "claudecode"} {
		if _, err := exec.LookPath(candidate); err == nil {
			caps = append(caps, "ai_provider_"+candidate)
		}
	}
	if _, err := exec.LookPath("claude"); err == nil && !agentAIStringSliceContains(caps, "ai_provider_claudecode") {
		caps = append(caps, "ai_provider_claudecode")
	}
	return caps
}

func agentAIStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func resolveAgentAICWD(raw string) (string, error) {
	return resolveAgentAuthorizedCWD(raw, "project path")
}

func agentAIErrorPayload(sessionID string, messageID string, err error) map[string]interface{} {
	message := "ai error"
	if err != nil {
		message = err.Error()
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIError,
		"session_id": sessionID,
		"error":      message,
	}
	if messageID != "" {
		payload["message_id"] = agentAssistantMessageID(messageID)
	}
	return payload
}

func agentAIApprovalRequestPayload(req agentAIApprovalRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"type":        models.AgentEventAIApprovalRequest,
		"session_id":  req.SessionID,
		"message_id":  req.MessageID,
		"approval_id": req.ID,
		"provider":    req.Provider,
		"kind":        req.Kind,
		"status":      "pending",
	}
	if req.Title != "" {
		payload["title"] = req.Title
	}
	if req.Reason != "" {
		payload["reason"] = req.Reason
	}
	if req.Command != "" {
		payload["command"] = req.Command
	}
	if req.CWD != "" {
		payload["cwd"] = req.CWD
	}
	if req.ToolName != "" {
		payload["tool_name"] = req.ToolName
	}
	if len(req.ToolInput) > 0 {
		payload["tool_input"] = json.RawMessage(req.ToolInput)
	}
	if len(req.FileChanges) > 0 {
		payload["file_changes"] = json.RawMessage(req.FileChanges)
	}
	if len(req.AvailableDecisions) > 0 {
		payload["available_decisions"] = req.AvailableDecisions
	}
	if len(req.Raw) > 0 {
		payload["raw"] = json.RawMessage(req.Raw)
	}
	return payload
}

func normalizeAgentAIApprovalDecision(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "accept", "accepted", "approve", "approved", "allow", "allowed", "yes":
		return models.AgentAIApprovalDecisionAccept
	case "accept_for_session", "acceptforsession", "approved_for_session", "approve_for_session", "allow_for_session":
		return models.AgentAIApprovalDecisionAcceptForSession
	case "decline", "declined", "deny", "denied", "reject", "rejected", "no":
		return models.AgentAIApprovalDecisionDecline
	case "cancel", "abort", "timed_out", "timeout":
		return models.AgentAIApprovalDecisionCancel
	default:
		return ""
	}
}

func normalizeAgentAIApprovalScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "session":
		return "session"
	default:
		return "turn"
	}
}

func newAgentAIApprovalID(sessionID string, runSeq int) string {
	token, err := newAgentAIApprovalToken()
	if err != nil {
		return fmt.Sprintf("%s-%d-%d", strings.TrimSpace(sessionID), runSeq, time.Now().UnixNano())
	}
	return token
}

func newAgentAIApprovalToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate approval token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func marshalAgentAIRaw(raw interface{}) json.RawMessage {
	if raw == nil {
		return nil
	}
	if msg, ok := raw.(json.RawMessage); ok {
		return msg
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return encoded
}

func agentAssistantMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if strings.HasPrefix(messageID, "assistant_") {
		return messageID
	}
	return "assistant_" + messageID
}
