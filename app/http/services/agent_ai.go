package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

type agentAIManager struct {
	mu       sync.Mutex
	sessions map[string]*agentAISession
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
}

type agentAITool struct {
	id           string
	path         string
	args         []string
	outputFormat agentAIOutputFormat
}

type agentAIOutputFormat string

const (
	agentAIOutputText             agentAIOutputFormat = "text"
	agentAIOutputCodexJSON        agentAIOutputFormat = "codex_json"
	agentAIOutputClaudeStreamJSON agentAIOutputFormat = "claude_stream_json"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), agentAIRunTimeout)
	session.cancel = cancel
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
	}
	m.sessions = make(map[string]*agentAISession)
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

func (m *agentAIManager) clearRunning(sessionID string, runSeq int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session != nil && session.runSeq == runSeq {
		session.cancel = nil
		session.history = trimAgentAIHistory(session.history)
	}
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

	cmd := exec.CommandContext(ctx, tool.path, tool.args...)
	cmd.Dir = run.projectPath
	cmd.Env = os.Environ()

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
	limiter := &agentAIOutputLimiter{limit: agentAIOutputLimitBytes}
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamAgentAIStdout(stdout, tool.outputFormat, run, writeJSON, limiter, func(text string) {
			outMu.Lock()
			output.WriteString(text)
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
			_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("AI output exceeded %d bytes", agentAIOutputLimitBytes)))
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

type agentAIOutputLimiter struct {
	mu       sync.Mutex
	limit    int
	used     int
	exceeded bool
}

func (l *agentAIOutputLimiter) Reserve(n int) int {
	if l == nil {
		return n
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exceeded || l.limit <= 0 {
		l.exceeded = true
		return 0
	}
	remaining := l.limit - l.used
	if remaining <= 0 {
		l.exceeded = true
		return 0
	}
	if n > remaining {
		l.used = l.limit
		l.exceeded = true
		return remaining
	}
	l.used += n
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
	caps := []string{"ai_chat", "ai_chat_context", "ai_stream", "vibe_session"}
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

func agentAssistantMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if strings.HasPrefix(messageID, "assistant_") {
		return messageID
	}
	return "assistant_" + messageID
}
