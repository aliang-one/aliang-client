package handlers

// 网页终端 HTTP 面(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md §6.1):
// GET state + POST command。真身在 user agent runtime(保险箱状态与令牌),
// core 侧闸门②+proxyIfNeeded 透传;reset 额外 loopback-only(spec §2/§6.4,
// 只能在 core 拦——runtime 侧全是 loopback)。core 侧 vault 是构造期陈旧镜像,
// 状态/命令一律 proxy 之后由 runtime 角色处理,core 腿绝不直读。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

// WebTerminalTokenHeader 解锁令牌随 header 走(plain HTTP proxy 全量转发,
// 见 copyAgentProxyHeaders);WS 腿走首帧 auth,不经 header。
const WebTerminalTokenHeader = "X-Web-Terminal-Token"

func guardWebTerminalGate(w http.ResponseWriter, r *http.Request) bool {
	// 与 HandleAIStream 同款双闸:有效 dashboard session ∥ loopback。
	if !middleware.ValidateDashboardSession(r) && !middleware.IsLoopbackRequest(r) {
		middleware.RequireDashboardSession(w, r)
		return false
	}
	return true
}

// HandleWebTerminalState GET /api/agent/web-terminal → vault 状态快照。
func (h *AgentHandler) HandleWebTerminalState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	common.Success(w, h.service.WebTerminalStatus())
}

// parseWebTerminalCommandBody 读取并解析命令体(≤4KiB),随后**回填 r.Body**
// 供 core 腿 proxyIfNeeded 原样转发——io.ReadAll 会抽干 body,而
// proxyToUserAgentClient 直接转发 r.Body,不回填=runtime 收到空体→400,
// LAN setup/verify/enable 全链路死(评审 Critical)。走到回填时 raw 必为
// 完整合法 JSON,回填无损;任何解析失败返回 ok=false(400 由调用方落)。
func parseWebTerminalCommandBody(r *http.Request) (action, pin, oldPin, newPin string, enabled *bool, ok bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || len(raw) == 0 {
		return "", "", "", "", nil, false
	}
	var body struct {
		Action  string `json:"action"`
		Pin     string `json:"pin,omitempty"`
		OldPin  string `json:"old_pin,omitempty"`
		NewPin  string `json:"new_pin,omitempty"`
		Enabled *bool  `json:"enabled,omitempty"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "", "", "", nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw)) // 回填供 proxy 腿原样转发
	return body.Action, body.Pin, body.OldPin, body.NewPin, body.Enabled, true
}

// HandleWebTerminalCommand POST /api/agent/web-terminal {action,...}。
// 业务失败走 Success 信封 {ok:false, reason,...}(前端要结构化读锁定秒数/
// 剩余次数,Error 信封的 details 前端 request() 不透);真正的门(闸门②/reset
// loopback)才用 HTTP 错误码。
func (h *AgentHandler) HandleWebTerminalCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	action, pin, oldPin, newPin, enabled, ok := parseWebTerminalCommandBody(r)
	if !ok {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}
	// reset 仅 loopback(core 腿拦截;runtime 侧天然 loopback)
	if action == "reset" && !middleware.IsLoopbackRequest(r) {
		common.Error(w, common.CodeForbidden, "PIN reset is loopback only", nil)
		return
	}
	token := r.Header.Get(WebTerminalTokenHeader)
	// killall 的空令牌放行只该属于 loopback 直呼(runtime 侧 IsLoopbackRequest
	// 恒真,服务级闸不可达——终审 Finding A):HTTP 面非本机来源必须带令牌,
	// 预 proxy 拦截(与 reset 同款 core 腿执法)。令牌有效性仍在 runtime 校验。
	if action == "killall" && !middleware.IsLoopbackRequest(r) && token == "" {
		common.Error(w, common.CodeForbidden, "killall requires the unlock token", nil)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	resp := h.service.WebTerminalRunCommand(action, pin, oldPin, newPin, enabled, token, middleware.IsLoopbackRequest(r))
	common.Success(w, resp)
}

// HandleWebTerminalStream GET /api/agent/web-terminal/stream。
// core 角色:闸门②+同源闸+proxyAIStream TCP 透传(与聊天 WS 同管线,路径无关)。
// runtime 角色:双闸→upgrade→首帧强制 terminal.auth(15s 内未过=close 1008)
// →读循环每帧复验令牌(闲置重锁后死连接不得继续驱动 shell;复验即滑动续期)
// →DispatchTerminalFrame(msg, write, WebTerminalEnabled)。每会话一条 WS,
// 前端重连带 attach:true 续屏(manager 语义)。
func (h *AgentHandler) HandleWebTerminalStream(w http.ResponseWriter, r *http.Request) {
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !services.IsUserAgentRuntime() {
		proxyAIStream(w, r)
		return
	}
	// nil-service 防御(单测形态 NewAgentHandler(nil)):auth 校验前必须拦住,
	// 否则 nil receiver 解引用 panic(net/http 会 recover 但语义丢失)
	if h.service == nil {
		rejectBrowserWS(w, r, "web terminal unavailable")
		return
	}
	conn, err := aiStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	// 入站只有小帧(auth/input/resize/close),1MiB 足够;勿照抄远端 4MiB
	// (那是为 replay 批量出站留的,与本入站限制无关)
	conn.SetReadLimit(1 << 20)

	var writeMu sync.Mutex
	writeJSON := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(payload)
	}

	// 首帧强制鉴权
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var authMsg map[string]interface{}
	if err := conn.ReadJSON(&authMsg); err != nil {
		return
	}
	authToken := ""
	if webTerminalFrameString(authMsg, "type") == "terminal.auth" {
		authToken = webTerminalFrameString(authMsg, "token")
	}
	if !h.service.WebTerminalValidateToken(authToken) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "web terminal locked or token invalid"),
			time.Now().Add(2*time.Second))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg == nil {
			continue
		}
		// 每帧复验令牌:闲置重锁(15m)后,已建立的连接不得继续驱动 shell;
		// 复验同时即滑动续期(ValidateToken 语义)
		if !h.service.WebTerminalValidateToken(authToken) {
			_ = writeJSON(services.AgentTerminalErrorPayloadPublic("", errors.New("web terminal re-locked")))
			// 与首帧鉴权失败路径对称:补发 1008 关闭帧,免前端白发一轮重连
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "web terminal re-locked"),
				time.Now().Add(2*time.Second))
			return
		}
		frameType := webTerminalFrameString(msg, "type")
		switch frameType {
		case "terminal.create":
			h.service.WebTerminalAuditTerminal("terminal_create", webTerminalFrameString(msg, "session_id"))
		case "terminal.close":
			h.service.WebTerminalAuditTerminal("terminal_close", webTerminalFrameString(msg, "session_id"))
		}
		h.service.DispatchTerminalFrame(msg, writeJSON, h.service.WebTerminalEnabled)
	}
}

// webTerminalFrameString 帧字段安全取串(services 的 remoteString 不导出)。
func webTerminalFrameString(msg map[string]interface{}, key string) string {
	if msg == nil {
		return ""
	}
	v, _ := msg[key].(string)
	return v
}
