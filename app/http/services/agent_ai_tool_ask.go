package services

// tool_ask bridge(agent 侧,2026-10-16 方案定稿):agent 自有 MCP server
// (mcp__aliang__*)作为 one-shot --print 下 Claude 的阻塞交互通道。CC 对普通
// MCP 工具同步等待 tool_result(墙钟默认≈27.8h),因此 handler 阻塞在
// "发 tool_ask.request → 等手机作答 → 回填结果"即可承载手机交互,不需要
// duplex 控制帧。requiresUserInteraction 绝不能设(headless 下直接
// user-rejected,工具体不执行)。
//
// 职责边界:本文件承载 MCP 宿主(官方 go-sdk)/作答等待/超时与 run 终结/
// per-run --mcp-config 注入与 token 门/审批 hook 短路。server 侧 registry
// 持久化、手机卡渲染与可靠送达是 server 仓批次。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	// agentToolAskDefaultSLA 手机应答 SLA:超时回 is_error tool_result 让模型
	// 自决(重问/降级),不死会话不杀 run。env ALIANG_AI_TOOL_ASK_SLA 可配。
	agentToolAskDefaultSLA = 25 * time.Minute
	// toolAskMCPServerName/toolAskMCPURLPrefix 自有 server 在 CC 侧的命名与
	// 本机回环路径前缀(工具名即 mcp__aliang__<tool>,白名单按精确名匹配)。
	toolAskMCPServerName = "aliang"
	toolAskMCPURLPrefix  = "/mcp/aliang/"
	// toolAskCCCallTimeoutMS 显式对齐 agent 侧 toolWaitCeiling(1h):CC 对
	// per-server tool-call 超时是硬墙(progress 不延长),须防机器级
	// MCP_TOOL_TIMEOUT 环境变量压小导致长往返被掐。
	toolAskCCCallTimeoutMS = 3600000
)

// agentToolAskRunContext 一次 claude run 与 bridge 的关联。token 是唯一的
// 关联+鉴权凭据(CC 的 MCP 配置里带着它),run 结束即注销。
type agentToolAskRunContext struct {
	token     string
	sessionID string
	runID     string
	deviceID  string
	writeJSON agentTerminalWriter
	nextSeq   atomic.Int64
}

type pendingToolAsk struct {
	run    *agentToolAskRunContext
	answer chan models.AgentToolAskResponse
}

type agentToolAskBridge struct {
	mu      sync.Mutex
	sla     time.Duration
	runs    map[string]*agentToolAskRunContext
	pending map[string]*pendingToolAsk
	http    http.Handler // 懒构建的 SDK StreamableHTTP handler
}

func (m *agentAIManager) ensureToolAskBridge() *agentToolAskBridge {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.toolAsk == nil {
		m.toolAsk = &agentToolAskBridge{
			sla:     resolveEnvDuration("ALIANG_AI_TOOL_ASK_SLA", agentToolAskDefaultSLA),
			runs:    make(map[string]*agentToolAskRunContext),
			pending: make(map[string]*pendingToolAsk),
		}
	}
	return m.toolAsk
}

// registerRun 登记 run 并签发 per-run token。writeJSON 是 run 的 WS 写通道
// (审批 hook 已有跨协程使用先例,见 requestApproval)。
func (b *agentToolAskBridge) registerRun(sessionID, runID string, writeJSON agentTerminalWriter) string {
	token := "tat_" + uuid.NewString()
	b.mu.Lock()
	b.runs[token] = &agentToolAskRunContext{
		token:     token,
		sessionID: sessionID,
		runID:     runID,
		deviceID:  currentAgentDeviceIDForNotify(),
		writeJSON: writeJSON,
	}
	b.mu.Unlock()
	return token
}

func (b *agentToolAskBridge) runLookup(token string) (*agentToolAskRunContext, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rc, ok := b.runs[token]
	return rc, ok
}

// unregisterRun run 终结:该 run 的全部 pending 以 run_ended 收场(发 cancel
// 帧 + 唤醒阻塞,回 is_error),绝不让旧 run 的 pending 混进新 run。
func (b *agentToolAskBridge) unregisterRun(token string) {
	b.mu.Lock()
	rc := b.runs[token]
	delete(b.runs, token)
	var doomed []*pendingToolAsk
	for id, p := range b.pending {
		if p.run == rc {
			delete(b.pending, id)
			doomed = append(doomed, p)
		}
	}
	b.mu.Unlock()
	for _, p := range doomed {
		b.emitCancel(p.run, "", models.AgentToolAskReasonRunEnded)
		b.deliverWaiter(p, models.AgentToolAskResponse{Status: models.AgentToolAskStatusExpired})
	}
}

// deliver 按 interaction_id 幂等投递:pop-on-delivery,首帧赢,迟到重复帧
// 直接忽略(契约:绝不产生第二个 tool_result)。
func (b *agentToolAskBridge) deliver(resp models.AgentToolAskResponse) {
	b.mu.Lock()
	p := b.pending[resp.InteractionID]
	delete(b.pending, resp.InteractionID)
	b.mu.Unlock()
	if p == nil {
		logger.Info(fmt.Sprintf("tool-ask: late/unknown response interaction=%s status=%s (ignored)", resp.InteractionID, resp.Status))
		return
	}
	b.deliverWaiter(p, resp)
}

// pendingSnapshot 测试/观测用:当前 pending 数。
func (b *agentToolAskBridge) pendingSnapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	return ids
}

// toolAskResponse 入站(server→agent):tool_ask.response。帧解析从简——
// 契约形状由 server 侧 zod schema 把关,这里只取三样:interaction_id/status/answer。
func (m *agentAIManager) toolAskResponse(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	b := m.ensureToolAskBridge()
	resp := models.AgentToolAskResponse{
		InteractionID: remoteString(msg, "interaction_id"),
		Status:        remoteString(msg, "status"),
	}
	if answer, ok := msg["answer"].(map[string]interface{}); ok {
		resp.Answer = &models.AgentToolAskAnswer{
			SelectedOptionID: remoteString(answer, "selected_option_id"),
			Message:          remoteString(answer, "message"),
		}
	}
	b.deliver(resp)
}

func (b *agentToolAskBridge) deliverWaiter(p *pendingToolAsk, resp models.AgentToolAskResponse) {
	select {
	case p.answer <- resp:
	default:
	}
}

func (b *agentToolAskBridge) emitCancel(rc *agentToolAskRunContext, interactionID, reason string) {
	if rc == nil {
		return
	}
	payload := map[string]interface{}{
		"type":           models.AgentEventToolAskCancel,
		"session_id":     rc.sessionID,
		"interaction_id": interactionID,
		"reason":         reason,
		"run_id":         rc.runID,
		"event_seq":      rc.nextSeq.Add(1),
	}
	_ = writeAgentAIApprovalRequest(context.Background(), rc.writeJSON, payload, 1, time.Second)
}

// serveHTTP 回环 MCP 端点:先验 token 再交官方 SDK handler,token 同时塞进
// request context 供 tool handler 取回自己的 run。token 承担关联(哪个 run
// 在调)与鉴权(外来调用 401),run 结束即失效。
func (b *agentToolAskBridge) serveHTTP(w http.ResponseWriter, r *http.Request) {
	b.tokenCheckedHandler().ServeHTTP(w, r)
}

// HandleToolAskMCP AgentService 出口:挂到两个 HTTP 服务面(主 server 与
// agentruntime)的 /mcp/aliang/ 前缀上。
func (s *AgentService) HandleToolAskMCP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.ai == nil {
		http.Error(w, "AI service is not ready", http.StatusServiceUnavailable)
		return
	}
	s.ai.ensureToolAskBridge().serveHTTP(w, r)
}

func (b *agentToolAskBridge) lazyHTTPHandler() http.Handler {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.http == nil {
		b.http = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return b.buildMCPServer()
		}, nil)
	}
	return b.http
}

func (b *agentToolAskBridge) buildMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "aliang-interact", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "ask_user",
		Description: "Ask the user a question with options and wait for their answer. Use this whenever the choice depends on user preference — do NOT proceed on assumption. Returns the selected option id and/or a free-text message.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"title":{"type":"string","description":"Short question text shown to the user."},
				"options":{"type":"array","maxItems":8,"items":{"type":"object","properties":{
					"id":{"type":"string","description":"Stable short slug for the option."},
					"label":{"type":"string","description":"Human-readable option label."},
					"description":{"type":"string","description":"Optional longer explanation."},
					"response":{"type":"string","description":"Optional text sent back as the answer when chosen."}
				},"required":["id","label"]}},
				"allow_custom":{"type":"boolean","description":"Whether the user may answer with free text instead of an option."},
				"multi":{"type":"boolean","description":"Whether multiple options may be selected."}
			},
			"required":["title"]
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		token := toolAskTokenFromContext(ctx)
		res, err := b.handleAskUser(ctx, string(req.Params.Arguments), token)
		if err != nil {
			return nil, err
		}
		return res, nil
	})
	return server
}

// handleAskUser 阻塞核心:校验入参 → 发 tool_ask.request → 等作答/SLA/run 终结。
// interaction_id 由 bridge 生成(MCP tools/call 拿不到 CC 的 tool_use_id),
// 它就是全链幂等键。
func (b *agentToolAskBridge) handleAskUser(ctx context.Context, rawArgs, token string) (*mcp.CallToolResult, error) {
	b.mu.Lock()
	rc := b.runs[token]
	b.mu.Unlock()
	if rc == nil {
		return nil, fmt.Errorf("tool ask run not registered (token expired or run ended)")
	}
	input, err := parseAskUserInput(rawArgs)
	if err != nil {
		return toolAskErrorResult(fmt.Sprintf("invalid ask_user input: %v", err)), nil
	}

	interactionID := "ta_" + uuid.NewString()
	pending := &pendingToolAsk{run: rc, answer: make(chan models.AgentToolAskResponse, 1)}
	b.mu.Lock()
	b.pending[interactionID] = pending
	b.mu.Unlock()
	popPending := func() {
		b.mu.Lock()
		delete(b.pending, interactionID)
		b.mu.Unlock()
	}

	payload := map[string]interface{}{
		"type":           models.AgentEventToolAskRequest,
		"session_id":     rc.sessionID,
		"interaction_id": interactionID,
		"device_id":      rc.deviceID,
		"tool_name":      models.AgentToolAskTools[0],
		"tool_input":     json.RawMessage(rawArgs),
		"expires_at":     time.Now().Add(b.sla).UnixMilli(),
		"run_id":         rc.runID,
		"event_seq":      rc.nextSeq.Add(1),
	}
	logger.Info(fmt.Sprintf("tool-ask: request session=%s run=%s interaction=%s title=%q sla=%s", rc.sessionID, rc.runID, interactionID, input.Title, b.sla))
	if err := writeAgentAIApprovalRequest(ctx, rc.writeJSON, payload, 3, time.Second); err != nil {
		popPending()
		return toolAskErrorResult(fmt.Sprintf("failed to deliver ask to user: %v", err)), nil
	}

	timer := time.NewTimer(b.sla)
	defer timer.Stop()
	select {
	case resp := <-pending.answer:
		switch resp.Status {
		case models.AgentToolAskStatusAnswered:
			body := map[string]interface{}{"status": "answered"}
			if resp.Answer != nil {
				if resp.Answer.SelectedOptionID != "" {
					body["selected_option_id"] = resp.Answer.SelectedOptionID
				}
				if resp.Answer.Message != "" {
					body["message"] = resp.Answer.Message
				}
			}
			out, _ := json.Marshal(body)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}, nil
		default: // expired(run 终结/服务端过期)
			return toolAskErrorResult("user did not answer in time"), nil
		}
	case <-timer.C:
		popPending()
		b.emitCancel(rc, interactionID, models.AgentToolAskReasonTimeout)
		return toolAskErrorResult("user did not respond in time"), nil
	case <-ctx.Done():
		popPending()
		b.emitCancel(rc, interactionID, models.AgentToolAskReasonClaudeErr)
		return toolAskErrorResult("ask aborted: claude run ended"), nil
	}
}

type agentAskUserInput struct {
	Title   string `json:"title"`
	Options []struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Description string `json:"description"`
		Response    string `json:"response"`
	} `json:"options"`
	AllowCustom bool `json:"allow_custom"`
	Multi       bool `json:"multi"`
}

func parseAskUserInput(raw string) (*agentAskUserInput, error) {
	var input agentAskUserInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if len(input.Options) > 8 {
		return nil, fmt.Errorf("too many options (max 8)")
	}
	for _, opt := range input.Options {
		if strings.TrimSpace(opt.ID) == "" || strings.TrimSpace(opt.Label) == "" {
			return nil, fmt.Errorf("every option needs id and label")
		}
	}
	return &input, nil
}

func toolAskErrorResult(msg string) *mcp.CallToolResult {
	out, _ := json.Marshal(map[string]interface{}{"status": "error", "error": msg})
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
	}
}

// toolAskTokenFromContext:SDK 不会替我们把 HTTP 请求上下文传进 tool handler
// 的参数里,serveHTTP 在验完 token 后把它塞进 request context。
type toolAskTokenKeyType struct{}

func toolAskTokenWith(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, toolAskTokenKeyType{}, token)
}

func toolAskTokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(toolAskTokenKeyType{}).(string)
	return v
}

func (b *agentToolAskBridge) tokenCheckedHandler() http.Handler {
	inner := b.lazyHTTPHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, toolAskMCPURLPrefix)
		if _, ok := b.runLookup(token); !ok {
			logger.Warn(fmt.Sprintf("tool-ask: unauthorized MCP request tokenLen=%d path=%s", len(token), r.URL.Path))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r.WithContext(toolAskTokenWith(r.Context(), token)))
	})
}

// toolAskBaseURL 复用审批 hook 的回环 base(setAgentAIApprovalHookBaseURL 在
// 两个 HTTP 服务启动时都会设置),保证 MCP 端点与 hook 同一监听面。
func toolAskBaseURL() string {
	agentAIApprovalHookBaseURLMu.RLock()
	base := agentAIApprovalHookBaseURL
	agentAIApprovalHookBaseURLMu.RUnlock()
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = UserAgentBaseURL()
	}
	return base
}

// withAgentToolAskBridge 给 claude stream-json run 注入自有交互 MCP server:
// 生成 per-run token → 登记 → 写/合并 --mcp-config tmpfile → 返回清理函数。
// commander 的 variadic flag 重复出现会覆盖,所以必须与档位 tmpfile 合并为
// 同一文件,绝不追加第二个 --mcp-config。
func (m *agentAIManager) withAgentToolAskBridge(tool *agentAITool, run agentAIRun, writeJSON agentTerminalWriter) (*agentAITool, func(), error) {
	if tool == nil {
		return tool, func() {}, nil
	}
	b := m.ensureToolAskBridge()
	runID := run.runID
	if strings.TrimSpace(runID) == "" {
		runID = run.messageID
	}
	token := b.registerRun(run.sessionID, runID, writeJSON)
	entry := map[string]interface{}{
		"type":    "http",
		"url":     toolAskBaseURL() + toolAskMCPURLPrefix + token,
		"timeout": toolAskCCCallTimeoutMS,
	}

	copied := *tool
	copied.args = append([]string(nil), tool.args...)
	mergeInto := ""
	for i, a := range copied.args {
		if a == "--mcp-config" && i+1 < len(copied.args) {
			mergeInto = copied.args[i+1]
			break
		}
	}

	servers := map[string]json.RawMessage{}
	if mergeInto != "" {
		raw, err := os.ReadFile(mergeInto)
		if err == nil {
			var parsed struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if json.Unmarshal(raw, &parsed) == nil && parsed.MCPServers != nil {
				servers = parsed.MCPServers
			}
		}
	}
	entryRaw, err := json.Marshal(entry)
	if err != nil {
		b.unregisterRun(token)
		return tool, func() {}, fmt.Errorf("marshal aliang mcp entry: %w", err)
	}
	servers[toolAskMCPServerName] = entryRaw
	merged, err := json.Marshal(map[string]interface{}{"mcpServers": servers})
	if err != nil {
		b.unregisterRun(token)
		return tool, func() {}, fmt.Errorf("marshal merged mcp config: %w", err)
	}

	tmp, err := os.CreateTemp("", "aliang-toolask-mcp-*.json")
	if err != nil {
		b.unregisterRun(token)
		return tool, func() {}, fmt.Errorf("write tool-ask mcp config: %w", err)
	}
	if _, err := tmp.Write(merged); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		b.unregisterRun(token)
		return tool, func() {}, fmt.Errorf("write tool-ask mcp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		b.unregisterRun(token)
		return tool, func() {}, fmt.Errorf("write tool-ask mcp config: %w", err)
	}

	if mergeInto != "" {
		for i, a := range copied.args {
			if a == "--mcp-config" && i+1 < len(copied.args) && copied.args[i+1] == mergeInto {
				copied.args[i+1] = tmp.Name()
				break
			}
		}
	} else {
		copied.args = append(copied.args, "--mcp-config", tmp.Name())
	}
	logger.Info(fmt.Sprintf("tool-ask: injected mcp server session=%s run=%s config=%s", run.sessionID, runID, tmp.Name()))
	cleanup := func() {
		_ = os.Remove(tmp.Name())
		b.unregisterRun(token)
	}
	return &copied, cleanup, nil
}
