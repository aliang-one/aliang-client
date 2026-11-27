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

const progressToolDescription = "Publish progress for a long task the user would benefit from following on another device. Use start once, then update when the phase or measured progress changes, and finish when the task ends. Omit totals you cannot measure. Skip short work and tasks already tracked by Aliang, such as device file transfers. Use short plain-text summaries without paths, commands, or secrets. Bash and PowerShell already report long commands automatically. For a custom title for one command, start with kind shell and pass the returned operation_id as progress_operation_id; the command finishes the existing card."

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

type progressTask struct {
	kind      string
	attached  bool
	measured  bool
	reporting bool
}

type agentProgressTool struct {
	service   *AgentService
	sessionID string
	account   string
	instance  string
	runID     string
	mu        sync.Mutex
	sequence  atomic.Int64
	tasksMu   sync.Mutex
	active    map[string]progressTask
	closed    bool
}

func newAgentProgressTool(service *AgentService, sessionID, runID string) *agentProgressTool {
	return &agentProgressTool{service: service, sessionID: sessionID, account: service.operationAccount(), instance: progressAgentInstance, runID: runID,
		active: make(map[string]progressTask)}
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
		p.tasksMu.Lock()
		full := len(p.active) >= 16
		p.tasksMu.Unlock()
		if full {
			return agentcore.ErrorResult("Finish an active progress task before starting another.")
		}
	case "update", "finish":
		outcome, _ := input["outcome"].(string)
		if action == "finish" && outcome != "completed" && outcome != "failed" && outcome != "cancelled" {
			return agentcore.ErrorResult("Finish requires completed, failed, or cancelled.")
		}
		p.tasksMu.Lock()
		task, ok := p.active[op]
		if !ok || task.attached {
			p.tasksMu.Unlock()
			return agentcore.ErrorResult("This run has no model-controlled task with that operation_id.")
		}
		task.reporting = true
		p.active[op] = task
		p.tasksMu.Unlock()
		defer func(id string) {
			p.tasksMu.Lock()
			defer p.tasksMu.Unlock()
			if current, exists := p.active[id]; exists {
				current.reporting = false
				p.active[id] = current
			}
		}(op)
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
			kind, _ := input["kind"].(string)
			p.tasksMu.Lock()
			p.active[op] = progressTask{kind: kind, measured: input["current"] != nil || input["total"] != nil || input["unit"] != nil}
			p.tasksMu.Unlock()
		} else if action == "finish" {
			p.tasksMu.Lock()
			delete(p.active, op)
			p.tasksMu.Unlock()
		} else if input["current"] != nil || input["total"] != nil || input["unit"] != nil {
			p.tasksMu.Lock()
			task := p.active[op]
			task.measured = true
			p.active[op] = task
			p.tasksMu.Unlock()
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
	p.tasksMu.Lock()
	defer p.tasksMu.Unlock()
	if len(p.active) == 0 || p.service == nil {
		return
	}
	for id, task := range p.active {
		if task.attached {
			continue
		}
		p.service.queueOperationFinish(p.account, p.frame(map[string]interface{}{
			"action": "finish", "operation_id": id, "outcome": "interrupted",
		}))
	}
	p.active = nil
	go p.service.flushOperationFinishes(context.Background())
}

// A Shell invocation may claim a model-created Shell card in the same run.
func (p *agentProgressTool) claimShellOperation(id string) (progressTask, bool) {
	if id == "" {
		return progressTask{}, false
	}
	p.tasksMu.Lock()
	defer p.tasksMu.Unlock()
	task, ok := p.active[id]
	if !ok || task.kind != "shell" || task.attached || task.reporting {
		return progressTask{}, false
	}
	task.attached = true
	p.active[id] = task
	return task, true
}

func (p *agentProgressTool) shellObserver() func(string, string, func() int64) func(agentcore.ShellOutcome) {
	// At most one automatic shell report is in flight per run. Explicitly
	// associated operations are already subject to the model tool's active cap.
	busy := make(chan struct{}, 1)
	return func(tool, requestedID string, outputBytes func() int64) func(agentcore.ShellOutcome) {
		done := make(chan agentcore.ShellOutcome, 1)
		task, attached := p.claimShellOperation(requestedID)
		go func() {
			ctx := context.Background()
			operationID := requestedID
			var startFrame map[string]interface{}
			var startErr error
			if attached {
				defer func() {
					p.tasksMu.Lock()
					delete(p.active, requestedID)
					p.tasksMu.Unlock()
				}()
			} else {
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
				if !p.service.progress.supported.Load() || p.service.currentRemoteWriter() == nil {
					return
				}
				startFrame = p.frame(map[string]interface{}{
					"action": "start", "title": tool, "kind": "shell",
				})
				ack, err := p.sendFrame(ctx, startFrame)
				startErr = err
				operationID = remoteString(ack, "operation_id")
				if err == nil && (remoteString(ack, "status") != "accepted" || operationID == "") {
					return
				}
			}
			var outcome agentcore.ShellOutcome
			if startErr == nil && !task.measured && outputBytes != nil {
				outcome = p.observeShellOutput(ctx, operationID, outputBytes, done)
			} else {
				outcome = <-done
			}
			finish := p.frame(shellFinishFields(operationID, outcome))
			if startErr != nil {
				if p.account == p.service.operationAccount() {
					p.service.queuePendingOperationFinish(pendingOperationFinish{
						account: p.account, frame: finish, startFrame: startFrame,
					})
				}
				return
			}
			if _, err := p.sendFrame(ctx, finish); err != nil {
				p.service.queueOperationFinish(p.account, finish)
			}
		}()
		return func(outcome agentcore.ShellOutcome) { done <- outcome }
	}
}

func shellFinishFields(operationID string, outcome agentcore.ShellOutcome) map[string]interface{} {
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
	return fields
}

// The automatic shell card carries a bounded, fixed-vocabulary stage summary
// next to the byte count. Everything on the card is derived from output
// receipt activity alone: the command line, paths, tokens and the raw
// stdout/stderr never leave this process, and no output content is inspected
// to pick a stage.
const (
	shellStageStreaming = "receiving command output"
	shellStageWaiting   = "waiting for command output"

	// Consecutive quiet seconds before the card is demoted to waiting. Three
	// ticks keep a slow-but-alive producer (a quiet build) on the running
	// stage instead of flapping.
	shellWaitingAfterTicks = 3
)

func shellStageFields(stage string, count int64) map[string]interface{} {
	phase := "running"
	if stage == shellStageWaiting {
		phase = "waiting"
	}
	return map[string]interface{}{
		"action": "update", "current": count, "unit": "bytes",
		"phase": phase, "step": stage,
	}
}

func (p *agentProgressTool) observeShellOutput(ctx context.Context, operationID string, outputBytes func() int64, done <-chan agentcore.ShellOutcome) agentcore.ShellOutcome {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var reported int64
	quiet := 0
	stage := shellStageStreaming
	send := func(stage string, count int64) bool {
		// A stalled network must not delay the terminal frame for the full
		// retry window. The next tick samples the cumulative count again.
		attempt, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		fields := shellStageFields(stage, count)
		fields["operation_id"] = operationID
		ack, err := p.service.reportOperation(attempt, p.account, p.frame(fields))
		cancel()
		if err == nil && remoteString(ack, "status") == "accepted" {
			reported = count
			return true
		}
		return false
	}
	for {
		select {
		case outcome := <-done:
			return outcome
		case <-ticker.C:
			count := outputBytes()
			if count > reported {
				quiet = 0
				stage = shellStageStreaming
				send(stage, count)
				continue
			}
			if stage == shellStageWaiting {
				continue
			}
			quiet++
			if quiet < shellWaitingAfterTicks {
				continue
			}
			// Output stopped arriving: demote the card once, then stay quiet
			// until output resumes, so a hung command does not tick per second.
			if send(shellStageWaiting, count) {
				stage = shellStageWaiting
			}
		}
	}
}
