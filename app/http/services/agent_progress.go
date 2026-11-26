package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/agentcore"
)

var progressAgentInstance = uuid.NewString()

const progressToolDescription = "Publish progress for a long task the user would benefit from following on another device. Use start once, then update when the phase or measured progress changes, and finish when the task ends. Omit totals you cannot measure. Skip short work and tasks already tracked by Aliang, such as device file transfers. Use short plain-text summaries without paths, commands, or secrets."

const shellProgressDelay = 500 * time.Millisecond

func (run agentAIRun) allowsOperationProgress() bool {
	return !run.readOnly && run.approvalMode != agentAIApprovalModeReadOnly
}

type agentProgressTransport struct {
	mu        sync.Mutex
	pending   map[string]chan map[string]interface{}
	supported atomic.Bool
	terminal  []pendingOperationFinish
}

type pendingOperationFinish struct {
	account    string
	frame      map[string]interface{}
	startFrame map[string]interface{}
}

func (s *AgentService) queueOperationFinish(account string, frame map[string]interface{}) {
	s.queuePendingOperationFinish(pendingOperationFinish{account: account, frame: frame})
}

func (s *AgentService) queuePendingOperationFinish(entry pendingOperationFinish) {
	s.progress.mu.Lock()
	defer s.progress.mu.Unlock()
	s.progress.terminal = append(s.progress.terminal, entry)
	if len(s.progress.terminal) > 128 {
		s.progress.terminal = s.progress.terminal[len(s.progress.terminal)-128:]
	}
}

func (s *AgentService) flushOperationFinishes(ctx context.Context) {
	account := s.operationAccount()
	s.progress.mu.Lock()
	pending := s.progress.terminal
	s.progress.terminal = nil
	s.progress.mu.Unlock()

	for _, entry := range pending {
		if entry.account != account || account != s.operationAccount() {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.reportPendingOperationFinish(attempt, &entry)
		cancel()
		if err != nil && entry.account == s.operationAccount() {
			s.queuePendingOperationFinish(entry)
		}
	}
}

func (s *AgentService) reportPendingOperationFinish(ctx context.Context, entry *pendingOperationFinish) error {
	if entry.startFrame != nil {
		// The original start may have been accepted before the connection was
		// lost. Reuse its idempotency key to recover the same operation ID.
		ack, err := s.reportOperation(ctx, entry.account, entry.startFrame)
		if err != nil {
			return err
		}
		id := remoteString(ack, "operation_id")
		if remoteString(ack, "status") != "accepted" || id == "" {
			return nil
		}
		entry.frame["operation_id"] = id
		entry.startFrame = nil
	}
	_, err := s.reportOperation(ctx, entry.account, entry.frame)
	return err
}

func (p *agentProgressTransport) acceptAck(frame map[string]interface{}) {
	p.mu.Lock()
	waiter := p.pending[remoteString(frame, "request_id")]
	delete(p.pending, remoteString(frame, "request_id"))
	p.mu.Unlock()
	if waiter != nil {
		waiter <- frame
	}
}

func (s *AgentService) reportOperation(ctx context.Context, account string, frame map[string]interface{}) (map[string]interface{}, error) {
	s.mu.Lock()
	if account != strings.TrimSpace(s.state.RegisteredUser) {
		s.mu.Unlock()
		return nil, errors.New("run account changed")
	}
	writer := s.currentRemoteWriter()
	s.mu.Unlock()
	if writer == nil || !s.progress.supported.Load() {
		return nil, errors.New("cloud progress is unavailable while the device is offline")
	}
	id := remoteString(frame, "request_id")
	ack := make(chan map[string]interface{}, 1)
	s.progress.mu.Lock()
	if s.progress.pending == nil {
		s.progress.pending = make(map[string]chan map[string]interface{})
	}
	s.progress.pending[id] = ack
	s.progress.mu.Unlock()
	defer func() {
		s.progress.mu.Lock()
		delete(s.progress.pending, id)
		s.progress.mu.Unlock()
	}()
	if err := writer(frame); err != nil {
		return nil, errors.New("unable to send progress to the cloud")
	}
	select {
	case result := <-ack:
		return result, nil
	case <-ctx.Done():
		return nil, errors.New("cloud progress acknowledgement timed out")
	}
}

type agentProgressTool struct {
	service   *AgentService
	sessionID string
	account   string
	instance  string
	runID     string
	mu        sync.Mutex
	sequence  atomic.Int64
	active    map[string]struct{}
	closed    bool
}

func newAgentProgressTool(service *AgentService, sessionID, runID string) *agentProgressTool {
	return &agentProgressTool{service: service, sessionID: sessionID, account: service.operationAccount(), instance: progressAgentInstance, runID: runID, active: make(map[string]struct{})}
}

func (*agentProgressTool) Name() string        { return "progress" }
func (*agentProgressTool) Description() string { return progressToolDescription }
func (*agentProgressTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object", "required": []string{"action"}, "additionalProperties": false,
		"properties": map[string]interface{}{
			"action":       map[string]interface{}{"type": "string", "enum": []string{"start", "update", "finish"}},
			"title":        map[string]interface{}{"type": "string", "maxLength": 120, "description": "Required for start."},
			"operation_id": map[string]interface{}{"type": "string", "description": "ID returned by start. Required for update and finish."},
			"kind":         map[string]interface{}{"type": "string", "description": "Optional short category, such as build or download."},
			"step":         map[string]interface{}{"type": "string", "maxLength": 160},
			"message":      map[string]interface{}{"type": "string", "maxLength": 240},
			"phase":        map[string]interface{}{"type": "string", "enum": []string{"queued", "running", "waiting"}},
			"outcome":      map[string]interface{}{"type": "string", "enum": []string{"completed", "failed", "cancelled"}, "description": "Required for finish."},
			"current":      map[string]interface{}{"type": "number", "minimum": 0},
			"total":        map[string]interface{}{"type": "number", "exclusiveMinimum": 0},
			"unit":         map[string]interface{}{"type": "string", "enum": []string{"bytes", "items", "steps", "files", "tests", "percent"}},
			"error_code":   map[string]interface{}{"type": "string"},
		},
	}
}

func (p *agentProgressTool) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) agentcore.ToolResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.service == nil || p.account == "" || p.account != p.service.operationAccount() {
		return agentcore.ErrorResult("Progress is unavailable for this run.")
	}
	action, _ := input["action"].(string)
	op, _ := input["operation_id"].(string)
	switch action {
	case "start":
		if title, _ := input["title"].(string); title == "" {
			return agentcore.ErrorResult("A title is required to start progress.")
		}
		if len(p.active) >= 16 {
			return agentcore.ErrorResult("Finish an active progress task before starting another.")
		}
	case "update", "finish":
		if _, ok := p.active[op]; !ok {
			return agentcore.ErrorResult("This run has no active task with that operation_id.")
		}
		outcome, _ := input["outcome"].(string)
		if action == "finish" && outcome != "completed" && outcome != "failed" && outcome != "cancelled" {
			return agentcore.ErrorResult("Finish requires completed, failed, or cancelled.")
		}
	default:
		return agentcore.ErrorResult("Progress action must be start, update, or finish.")
	}
	fields := make(map[string]interface{}, len(input))
	for _, key := range []string{"action", "title", "operation_id", "kind", "step", "message", "phase", "outcome", "current", "total", "unit", "error_code"} {
		if value, ok := input[key]; ok {
			fields[key] = value
		}
	}
	result, err := p.send(ctx, fields)
	if err != nil {
		return agentcore.ErrorResult("%s", err)
	}
	switch status := remoteString(result, "status"); status {
	case "accepted":
		if action == "start" {
			op = remoteString(result, "operation_id")
			if op == "" {
				return agentcore.ErrorResult("Cloud did not return a task ID.")
			}
			p.active[op] = struct{}{}
		} else if action == "finish" {
			delete(p.active, op)
		}
	case "throttled":
		body, _ := json.Marshal(map[string]interface{}{
			"operation_id": op, "status": "throttled",
			"message": "Report the next measured change after one second.",
		})
		return agentcore.ToolResult{Content: string(body)}
	default:
		return agentcore.ErrorResult("Cloud rejected the progress report (%s).", remoteString(result, "error_code"))
	}
	body, _ := json.Marshal(map[string]interface{}{"operation_id": op, "status": "accepted"})
	return agentcore.ToolResult{Content: string(body)}
}

func (p *agentProgressTool) frame(fields map[string]interface{}) map[string]interface{} {
	id := uuid.NewString()
	fields["type"] = "operation.progress"
	fields["session_id"] = p.sessionID
	fields["run_id"] = p.runID
	fields["agent_instance_id"] = p.instance
	fields["request_id"] = id
	fields["source_sequence"] = p.sequence.Add(1)
	if fields["action"] == "start" {
		fields["idempotency_key"] = id
	}
	return fields
}

func (p *agentProgressTool) send(ctx context.Context, fields map[string]interface{}) (map[string]interface{}, error) {
	return p.sendFrame(ctx, p.frame(fields))
}

func (p *agentProgressTool) sendFrame(ctx context.Context, frame map[string]interface{}) (map[string]interface{}, error) {
	if p.account != p.service.operationAccount() {
		return nil, errors.New("run account changed")
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := p.service.reportOperation(timeout, p.account, frame)
	if err == nil || ctx.Err() != nil || p.account != p.service.operationAccount() ||
		p.service.currentRemoteWriter() == nil {
		return result, err
	}
	retry, retryCancel := context.WithTimeout(ctx, 3*time.Second)
	defer retryCancel()
	return p.service.reportOperation(retry, p.account, frame)
}

func (p *agentProgressTool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if len(p.active) == 0 || p.service == nil {
		return
	}
	for id := range p.active {
		p.service.queueOperationFinish(p.account, p.frame(map[string]interface{}{
			"action": "finish", "operation_id": id, "outcome": "interrupted",
		}))
	}
	p.active = nil
	go p.service.flushOperationFinishes(context.Background())
}

func (p *agentProgressTool) shellObserver() func(string) func(agentcore.ShellOutcome) {
	// At most one shell report is in flight per run. Network acknowledgements
	// stay off the tool execution path.
	busy := make(chan struct{}, 1)
	return func(tool string) func(agentcore.ShellOutcome) {
		done := make(chan agentcore.ShellOutcome, 1)
		go func() {
			timer := time.NewTimer(shellProgressDelay)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
			}
			select {
			case <-done:
				return
			case busy <- struct{}{}:
				defer func() { <-busy }()
			default:
				return
			}

			ctx := context.Background()
			if !p.service.progress.supported.Load() || p.service.currentRemoteWriter() == nil {
				return
			}
			startFrame := p.frame(map[string]interface{}{
				"action": "start", "title": tool, "kind": "shell",
			})
			ack, err := p.sendFrame(ctx, startFrame)
			operationID := remoteString(ack, "operation_id")
			if err == nil && (remoteString(ack, "status") != "accepted" || operationID == "") {
				return
			}

			outcome := <-done
			fields := map[string]interface{}{"action": "finish", "operation_id": operationID, "outcome": string(outcome)}
			switch outcome {
			case agentcore.ShellTimedOut:
				fields["outcome"] = "failed"
				fields["error_code"] = "tool_timeout"
				fields["message"] = "Shell command timed out."
			case agentcore.ShellFailed:
				fields["error_code"] = "tool_failed"
				fields["message"] = "Shell command failed. See the conversation for details."
			}
			frame := p.frame(fields)
			if err != nil {
				if p.account == p.service.operationAccount() {
					p.service.queuePendingOperationFinish(pendingOperationFinish{
						account: p.account, frame: frame, startFrame: startFrame,
					})
				}
				return
			}
			if _, err := p.sendFrame(ctx, frame); err != nil {
				p.service.queueOperationFinish(p.account, frame)
			}
		}()
		return func(outcome agentcore.ShellOutcome) { done <- outcome }
	}
}
