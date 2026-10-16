package services

// tool_ask bridge(agent 侧)单元测试:阻塞核心(handleAskUser)的作答/超时/
// run 终结三态、应答幂等、HTTP token 门、per-run --mcp-config 注入与清理、
// 审批 hook 对白名单交互工具的短路。MCP wire 层(官方 SDK)留给真机 smoke。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"aliang.one/nursorgate/app/http/models"
)

// capturedWriter 记录所有发出的帧,并用 channel 通知等待方。
type capturedWriter struct {
	mu       sync.Mutex
	payloads []map[string]interface{}
	notify   chan struct{}
}

func newCapturedWriter() *capturedWriter {
	return &capturedWriter{notify: make(chan struct{}, 64)}
}

func (w *capturedWriter) write(v interface{}) error {
	payload, ok := v.(map[string]interface{})
	if !ok {
		return fmt.Errorf("unexpected payload type %T", v)
	}
	w.mu.Lock()
	w.payloads = append(w.payloads, payload)
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
	return nil
}

func (w *capturedWriter) frames() []map[string]interface{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]map[string]interface{}, len(w.payloads))
	copy(out, w.payloads)
	return out
}

func (w *capturedWriter) waitForFrames(n int, timeout time.Duration) []map[string]interface{} {
	deadline := time.After(timeout)
	for {
		if frames := w.frames(); len(frames) >= n {
			return frames
		}
		select {
		case <-w.notify:
		case <-deadline:
			return w.frames()
		}
	}
}

func newToolAskTestManager(t *testing.T, sla time.Duration) (*agentAIManager, *agentToolAskBridge) {
	t.Helper()
	m := newAgentAIManager()
	b := m.ensureToolAskBridge()
	b.sla = sla
	return m, b
}

func TestToolAskBridgeAskUserRoundTrip(t *testing.T) {
	m, b := newToolAskTestManager(t, 5*time.Second)
	writer := newCapturedWriter()
	token := b.registerRun("sess1", "run1", writer.write)
	defer b.unregisterRun(token)

	args := `{"title":"选个方案","options":[{"id":"a","label":"方案A"},{"id":"b","label":"方案B"}],"allow_custom":true}`

	type outcome struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := b.handleAskUser(context.Background(), args, token)
		done <- outcome{res, err}
	}()

	frames := writer.waitForFrames(1, 2*time.Second)
	if len(frames) != 1 {
		t.Fatalf("expected 1 request frame, got %d", len(frames))
	}
	reqFrame := frames[0]
	if reqFrame["type"] != models.AgentEventToolAskRequest {
		t.Fatalf("frame type = %v", reqFrame["type"])
	}
	interactionID, _ := reqFrame["interaction_id"].(string)
	if interactionID == "" {
		t.Fatalf("request frame missing interaction_id: %v", reqFrame)
	}
	if reqFrame["run_id"] != "run1" {
		t.Fatalf("run_id = %v", reqFrame["run_id"])
	}
	if reqFrame["session_id"] != "sess1" {
		t.Fatalf("session_id = %v", reqFrame["session_id"])
	}
	if _, ok := reqFrame["expires_at"]; !ok {
		t.Fatalf("request frame missing expires_at")
	}

	// 模拟 server 回答(经入站处理入口,幂等链同真实路径)。
	m.toolAskResponse(map[string]interface{}{
		"type":           models.AgentEventToolAskResponse,
		"interaction_id": interactionID,
		"delivery_id":    "dv_1",
		"attempt":        1,
		"status":         models.AgentToolAskStatusAnswered,
		"answer":         map[string]interface{}{"selected_option_id": "a", "message": "就这个"},
	}, nil)

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("handleAskUser: %v", out.err)
		}
		if out.res.IsError {
			t.Fatalf("answered result must not be IsError")
		}
		text := toolAskResultText(t, out.res)
		if !strings.Contains(text, `"status":"answered"`) || !strings.Contains(text, `"selected_option_id":"a"`) {
			t.Fatalf("result text missing answer fields: %s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handleAskUser did not return after answer")
	}
}

func TestToolAskBridgeTimeoutEmitsCancelAndIsError(t *testing.T) {
	m, b := newToolAskTestManager(t, 60*time.Millisecond)
	writer := newCapturedWriter()
	token := b.registerRun("sess1", "run1", writer.write)
	defer b.unregisterRun(token)

	res, err := b.handleAskUser(context.Background(), `{"title":"x"}`, token)
	if err != nil {
		t.Fatalf("handleAskUser: %v", err)
	}
	if !res.IsError {
		t.Fatalf("timeout result must be IsError")
	}
	frames := writer.frames()
	var cancelSeen bool
	for _, f := range frames {
		if f["type"] == models.AgentEventToolAskCancel && f["reason"] == models.AgentToolAskReasonTimeout {
			cancelSeen = true
		}
	}
	if !cancelSeen {
		t.Fatalf("timeout must emit tool_ask.cancel(reason=timeout), frames=%v", frames)
	}
	_ = m
}

func TestToolAskBridgeRunEndedCancelsPending(t *testing.T) {
	_, b := newToolAskTestManager(t, 30*time.Second)
	writer := newCapturedWriter()
	token := b.registerRun("sess1", "run1", writer.write)

	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := b.handleAskUser(context.Background(), `{"title":"x"}`, token)
		done <- res
	}()
	writer.waitForFrames(1, time.Second)
	b.unregisterRun(token) // run 终结:翻卡 cancelled + 唤醒阻塞

	select {
	case res := <-done:
		if !res.IsError {
			t.Fatalf("run-ended result must be IsError")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handleAskUser did not return after run unregister")
	}
	frames := writer.frames()
	var cancelSeen bool
	for _, f := range frames {
		if f["type"] == models.AgentEventToolAskCancel && f["reason"] == models.AgentToolAskReasonRunEnded {
			cancelSeen = true
		}
	}
	if !cancelSeen {
		t.Fatalf("run unregister must emit tool_ask.cancel(reason=run_ended)")
	}
}

func TestToolAskBridgeDuplicateResponseIgnored(t *testing.T) {
	m, b := newToolAskTestManager(t, 5*time.Second)
	writer := newCapturedWriter()
	token := b.registerRun("sess1", "run1", writer.write)
	defer b.unregisterRun(token)

	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := b.handleAskUser(context.Background(), `{"title":"x"}`, token)
		done <- res
	}()
	frames := writer.waitForFrames(1, 2*time.Second)
	interactionID, _ := frames[0]["interaction_id"].(string)

	answer := func(optionID string) map[string]interface{} {
		return map[string]interface{}{
			"type":           models.AgentEventToolAskResponse,
			"interaction_id": interactionID,
			"status":         models.AgentToolAskStatusAnswered,
			"answer":         map[string]interface{}{"selected_option_id": optionID},
		}
	}
	m.toolAskResponse(answer("first"), nil)
	m.toolAskResponse(answer("second"), nil) // 迟到重复:必须被忽略(first-write-wins)

	select {
	case res := <-done:
		text := toolAskResultText(t, res)
		if !strings.Contains(text, `"selected_option_id":"first"`) {
			t.Fatalf("first answer must win, got: %s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handleAskUser did not return")
	}
}

func TestToolAskBridgeUnknownInteractionNoop(t *testing.T) {
	m, b := newToolAskTestManager(t, time.Second)
	_ = b
	// 未知 interaction 的迟到帧不得 panic / 不得建 pending。
	m.toolAskResponse(map[string]interface{}{
		"type":           models.AgentEventToolAskResponse,
		"interaction_id": "ta_nonexistent",
		"status":         models.AgentToolAskStatusAnswered,
		"answer":         map[string]interface{}{"message": "x"},
	}, nil)
	if len(b.pendingSnapshot()) != 0 {
		t.Fatalf("unknown interaction must not create pending")
	}
}

func TestToolAskBridgeTokenGate(t *testing.T) {
	_, b := newToolAskTestManager(t, time.Second)
	b.registerRun("sess1", "run1", newCapturedWriter().write)

	rec := httptest.NewRecorder()
	b.serveHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp/aliang/wrongtoken", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: status = %d, want 401", rec.Code)
	}
}

func TestWithAgentToolAskBridgeInjectsMCPConfig(t *testing.T) {
	m := newAgentAIManager()
	writer := newCapturedWriter()
	tool := &agentAITool{id: "claude", args: []string{"--print", "--verbose"}}
	run := agentAIRun{sessionID: "sess1", runID: "run1"}

	injected, cleanup, err := m.withAgentToolAskBridge(tool, run, writer.write)
	if err != nil {
		t.Fatalf("withAgentToolAskBridge: %v", err)
	}
	if cleanup == nil {
		t.Fatalf("cleanup must be provided")
	}
	// args 里出现 --mcp-config <file>
	idx := -1
	for i, a := range injected.args {
		if a == "--mcp-config" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(injected.args) {
		t.Fatalf("args missing --mcp-config <file>: %v", injected.args)
	}
	cfgPath := injected.args[idx+1]
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Timeout int64  `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse mcp config: %v", err)
	}
	entry, ok := cfg.MCPServers["aliang"]
	if !ok {
		t.Fatalf("merged config missing aliang server: %s", raw)
	}
	if entry.Type != "http" || !strings.Contains(entry.URL, "/mcp/aliang/") || entry.Timeout != 3600000 {
		t.Fatalf("aliang entry malformed: %+v", entry)
	}
	// registry:token 可查到 run
	token := entry.URL[strings.LastIndex(entry.URL, "/")+1:]
	if _, ok := m.toolAsk.runLookup(token); !ok {
		t.Fatalf("registry missing token %q", token)
	}
	// 原有 args 保留
	if len(injected.args) < 3 || injected.args[len(injected.args)-1] != cfgPath {
		t.Fatalf("injected args malformed: %v", injected.args)
	}

	cleanup()
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remove tmpfile")
	}
	if _, ok := m.toolAsk.runLookup(token); ok {
		t.Fatalf("cleanup must unregister run")
	}
}

func TestWithAgentToolAskBridgeMergesExistingMCPConfig(t *testing.T) {
	m := newAgentAIManager()
	writer := newCapturedWriter()
	// sanitized 档已有 tier tmpfile:commander variadic 语义下重复 --mcp-config
	// 会覆盖,必须合并进同一文件而非追加第二个 flag。
	existing := filepath.Join(t.TempDir(), "tier-mcp.json")
	if err := os.WriteFile(existing, []byte(`{"mcpServers":{"proj":{"command":"uvx","args":["srv"]}}}`), 0o600); err != nil {
		t.Fatalf("seed tier config: %v", err)
	}
	tool := &agentAITool{id: "claude", args: []string{"--strict-mcp-config", "--mcp-config", existing}}
	run := agentAIRun{sessionID: "sess1", runID: "run1"}

	injected, cleanup, err := m.withAgentToolAskBridge(tool, run, writer.write)
	if err != nil {
		t.Fatalf("withAgentToolAskBridge: %v", err)
	}
	defer cleanup()

	count := 0
	var mergedPath string
	for i, a := range injected.args {
		if a == "--mcp-config" && i+1 < len(injected.args) {
			count++
			mergedPath = injected.args[i+1]
		}
	}
	if count != 1 {
		t.Fatalf("exactly one --mcp-config flag allowed, got %d (%v)", count, injected.args)
	}
	if mergedPath == existing {
		t.Fatalf("must write a merged tmpfile, not mutate the tier file")
	}
	raw, err := os.ReadFile(mergedPath)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	if _, ok := cfg.MCPServers["proj"]; !ok {
		t.Fatalf("merged config lost tier server proj: %s", raw)
	}
	if _, ok := cfg.MCPServers["aliang"]; !ok {
		t.Fatalf("merged config missing aliang: %s", raw)
	}
	// 原档位文件不被破坏
	rawExisting, err := os.ReadFile(existing)
	if err != nil || !strings.Contains(string(rawExisting), `"proj"`) {
		t.Fatalf("tier file must stay intact: %v %s", err, rawExisting)
	}
}

func TestClaudeApprovalHookAutoApprovesInteractionTools(t *testing.T) {
	m := newAgentAIManager()
	writer := newCapturedWriter()
	m.mu.Lock()
	m.sessions["sess1"] = &agentAISession{
		id:            "sess1",
		approvalToken: "tok",
		activeWriter:  writer.write,
		cancel:        func() {},
	}
	m.mu.Unlock()

	decision, err := m.handleClaudeApprovalHook(context.Background(), "sess1", "", "tok", map[string]interface{}{
		"hook_event_name": "PermissionRequest",
		"tool_name":       "mcp__aliang__ask_user",
		"tool_input":      map[string]interface{}{"title": "x"},
	})
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if !claudeApprovalDecisionAllows(decision) {
		t.Fatalf("interaction tool must be auto-approved, got %v", decision)
	}
	// 白名单之外照常走原路径(存量行为,未改动;落穿会阻塞在 requestApproval
	// 等待手机应答,不适合单测,由 IsAgentToolAskTool 白名单测试覆盖过滤逻辑)。
}

func toolAskResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

// claudeApprovalDecisionAllows 从 hook 决策载荷里判断是否放行(三种 hook 形态:
// PreToolUse 的 permissionDecision、PermissionRequest 的 decision.behavior)。
func claudeApprovalDecisionAllows(decision map[string]interface{}) bool {
	specific, ok := decision["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		return false
	}
	if d, _ := specific["permissionDecision"].(string); d != "" {
		return d == "allow"
	}
	if inner, ok := specific["decision"].(map[string]interface{}); ok {
		if behavior, _ := inner["behavior"].(string); behavior != "" {
			return behavior == "allow"
		}
	}
	return false
}
