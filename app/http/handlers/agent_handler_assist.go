package handlers

// 远程协助 P1 HTTP 面(spec docs/superpowers/specs/2026-11-26-remote-assist-design.md
// §7.1):gates 总开关(Task 3)+ 客户端会话四端点 request/session/end/cancel
// (Task 6)。真身在 user agent runtime(状态机/终端腿/信令都在彼处),core
// 侧只做门+真源标记+proxyIfNeeded 透传;同路径双方法用方法前缀注册(core
// routes.go register / runtime server.go mux.HandleFunc,route_parity_test 兜底)。
//
// 会话四端点门矩阵(plan Task 6,照 web-terminal 双闸测试矩阵形态):
//   ①无 cookie 非 loopback → 401(guardWebTerminalGate,同每个 web-terminal handler)
//   ②loopback → 放行(双闸豁免腿)
//   ③同源 LAN + cookie → 放行
//   ④跨站 Origin → 403(assistSameOriginGuard,仅 core 腿——透传腿 r.Host 已
//     改写为拨号目标,Origin 比对必然失配,见函数注记)
//   ⑤总开关关 → 403(assistRuntimeGate,四端点统一;gates 端点刻意除外——
//     它是翻回开关的入口,必须常开)
//
// PROXY-FIRST:core 腿零 body 解析(body 原样流穿到 runtime 腿才解析,照 FS
// 写操作三端点的 parseWebTerminalFSBody 模式,无需回填 r.Body——runtime 腿
// 此后不再转发)。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

// HandleAssistGates GET /api/agent/assist/gates → {enabled} 现值;
// POST {enabled:bool} → 翻转总开关并回显新值。照 HandlePeerGates:
// proxyIfNeeded 先行(普通 HTTP,PROXY-FIRST——core 腿零 body 解析,body
// 原样到达 runtime);POST 载荷须含显式布尔键 enabled(部分更新语义不适用
// ——单键契约,缺键 400,同 peer gates「至少一个显式布尔键」纪律)。
// 翻 false 的会话联动在 service 层(SetRemoteAssistEnabled)。
func (h *AgentHandler) HandleAssistGates(w http.ResponseWriter, r *http.Request) {
	if h.proxyIfNeeded(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		common.Success(w, map[string]interface{}{"enabled": h.service.RemoteAssistEnabled()})
	case http.MethodPost:
		// ≤4KiB 小 JSON 体(同 parseWebTerminalCommandBody 限额;本端点仅在
		// runtime 腿解析,proxy 之后,无需回填 r.Body)。
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || req.Enabled == nil {
			common.ErrorBadRequest(w, "Invalid request body: boolean key `enabled` required", nil)
			return
		}
		h.service.SetRemoteAssistEnabled(*req.Enabled)
		common.Success(w, map[string]interface{}{"enabled": h.service.RemoteAssistEnabled()})
	default:
		// 真实 405(common.Error 把 code 当业务码过 ErrorCodeToHTTPStatus,
		// 直传 http.StatusMethodNotAllowed 会落 500)——同 HandlePeerGates。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ——— 客户端会话四端点(Task 6)———

// assistSameOriginGuard 跨站 Origin 闸(门矩阵④,plan Task 6 明文「跨站
// Origin → 拒」):带 Origin 的请求必须同源(Origin 的 host:port 与 r.Host
// 精确比较,复用 aiStreamHostAllowed 的解析口径)。仅 core 腿执行:core→
// runtime 透传腿的 r.Host 已改写为拨号目标(127.0.0.1:56433),浏览器真源
// Origin 与之必然失配,在 runtime 腿照搬会把 LAN cookie 全链路误杀;runtime
// 直连腿绑定 127.0.0.1(DefaultUserAgentAddr),与本 web-terminal 家族既有
// 姿态一致(FS/命令端点均无 runtime 侧 Origin 闸)。
func assistSameOriginGuard(w http.ResponseWriter, r *http.Request) bool {
	if services.IsUserAgentRuntime() {
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" && !aiStreamHostAllowed(r) {
		common.Error(w, common.CodeForbidden, "Cross-origin requests are not allowed", nil)
		return false
	}
	return true
}

// assistRuntimeGate 运行时腿统一门(会话四端点过 proxy 之后先过它):nil
// 服务防御 + 总开关关=403。失败已写响应。gates 端点刻意不在此列(见文件头
// 门矩阵⑤注记)。
func (h *AgentHandler) assistRuntimeGate(w http.ResponseWriter, r *http.Request) bool {
	if h.service == nil {
		common.Error(w, common.CodeForbidden, "Remote assist unavailable", nil)
		return false
	}
	if !h.service.RemoteAssistEnabled() {
		common.ErrorForbidden(w, "Remote assist is disabled")
		return false
	}
	return true
}

// writeAssistServiceError 状态机哨兵 → HTTP 语义映射(plan Task 6 拍板):
//
//	ErrAssistCooldown     → 429(CodeTooManyRequests=105,冷却连发拦截;
//	                        码表原无 4xx 限频位,Task 6 补位——429 语义是
//	                        plan 明文,前端凭 code=105 区分冷却做倒计时提示)
//	ErrAssistBusy         → 409(已有活跃会话)
//	ErrAssistNotPending   → 409(cancel 时无待审批申请)
//	ErrAssistNothingToEnd → 409(end 时 idle/ended,plan「idle 时 409」)
//	其余(信令 writer 离线、半构造防御)→ CodeServiceUnavailable 信封
//	(201;HTTP 经 ErrorCodeToHTTPStatus 落 500,与 proxy-不可达同款既有惯例)
//	——「申请必须可见地失败」(spec Task 4:发不出去的申请永远不会进 admin
//	队列,静默挂起最伤用户)。
func writeAssistServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrAssistCooldown):
		common.Error(w, common.CodeTooManyRequests, "Assist request is in cooldown, try again later", nil)
	case errors.Is(err, services.ErrAssistBusy):
		common.ErrorConflict(w, "An assist session is already active")
	case errors.Is(err, services.ErrAssistNotPending):
		common.ErrorConflict(w, "No pending assist request to cancel")
	case errors.Is(err, services.ErrAssistNothingToEnd):
		common.ErrorConflict(w, "No assist session to end")
	default:
		common.ErrorServiceUnavailable(w, "Remote assist signaling is unavailable (device offline?)")
	}
}

// parseAssistRequestBody 解申请体(≤4KiB,宽容):{message?} 可缺省——空体/
// 无 message 键都合法(申请可以不带留言);>4KiB 显式 400(读 4KiB+1 字节
// 探溢出,不静默截断——截断的合法边界 JSON 会伪装成功);非 JSON 对象 400。
// 仅 runtime 腿执行(proxy 已返回 false),core 腿零 body 解析(PROXY-FIRST)。
func parseAssistRequestBody(r *http.Request) (message string, ok bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10+1))
	if err != nil {
		return "", false
	}
	if len(raw) > 4<<10 {
		return "", false
	}
	if len(raw) == 0 {
		return "", true
	}
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return "", false
	}
	if v, present := body["message"].(string); present {
		return strings.TrimSpace(v), true
	}
	return "", true
}

// HandleAssistRequest POST /api/agent/assist/request {message?} → 200 快照
// {session_id,state:"requested",message,requested_at}。requested 态重复申请
// = 同态更新留言(session_id 不变,状态机语义);active 态 409;冷却 429;
// 信令失败 201 信封(见 writeAssistServiceError 映射表)。成功后审计
// assist_request(真源 origin 随标记头)。
func (h *AgentHandler) HandleAssistRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !assistSameOriginGuard(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // assist_request 审计真源:标记先于 proxy 透传
	if h.proxyIfNeeded(w, r) {
		return
	}
	if !h.assistRuntimeGate(w, r) {
		return
	}
	message, ok := parseAssistRequestBody(r)
	if !ok {
		common.ErrorBadRequest(w, "Invalid request body (JSON object, 4 KiB max)", nil)
		return
	}
	sid, err := h.service.AssistRequest(message)
	if err != nil {
		writeAssistServiceError(w, err)
		return
	}
	h.service.RemoteAssistAudit("request", fmt.Sprintf("session=%s msg_len=%d origin=%s",
		sid, len(strings.TrimSpace(message)), webTerminalLoopbackOrigin(r)))
	// 回快照而非手拼 {session_id,state}:前端拿全量(requested_at/message),
	// 且申请→轮询间可能插进的云端帧让快照如实反映当下(比硬编码
	// state=requested 诚实)。
	common.Success(w, h.service.AssistSnapshot())
}

// HandleAssistSession GET /api/agent/assist/session → 200 快照。前端轮询/推送
// 源;idle 时快照为零值(state 空串,session_id/message 键恒在——稳定形状
// 契约,前端 map ""→idle;end_reason 等 omitempty 键不保证在)。开关关=
// 403(与四端点同门,前端先探 gates 再轮询)。不审计(轮询噪声,plan Task 6)。
//
// 已知可接受卡顿(Task 5 质量评审拍板,有界且罕见):Snapshot 懒扫落终态时
// 终态钩子的协助 PTY 关闭路径含 2s waitForCreate 短窗(approve→end 边界竞态
// 下),本端点在该窗口内同步等待——bounded,不做异步化。
func (h *AgentHandler) HandleAssistSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !assistSameOriginGuard(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // 同链路统一语义(session 不审计,标记无害)
	if h.proxyIfNeeded(w, r) {
		return
	}
	if !h.assistRuntimeGate(w, r) {
		return
	}
	common.Success(w, h.service.AssistSnapshot())
}

// HandleAssistEnd POST /api/agent/assist/end——客户端主动结束(requested/
// active 均可,附录 A #4:客户端发起收口恒上行 assist.cancel);idle/ended
// 409(plan 明文;service 壳前置判防 ended 态空转上行噪声帧)。成功后审计
// assist_end(reason=client_end)。
func (h *AgentHandler) HandleAssistEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !assistSameOriginGuard(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // assist_end 审计真源:标记先于 proxy 透传
	if h.proxyIfNeeded(w, r) {
		return
	}
	if !h.assistRuntimeGate(w, r) {
		return
	}
	sid, err := h.service.AssistEnd()
	if err != nil {
		writeAssistServiceError(w, err)
		return
	}
	h.service.RemoteAssistAudit("end", fmt.Sprintf("session=%s reason=%s origin=%s",
		sid, services.AssistEndClient, webTerminalLoopbackOrigin(r)))
	common.Success(w, h.service.AssistSnapshot())
}

// HandleAssistCancel POST /api/agent/assist/cancel——撤销「待审批」申请
// (仅 requested 态;active 态走 end,idle/ended 409)。plan §7.1 端点清单
// 三端点之外的第四端点:审计清单(assist/cancel)与申请弹窗的取消按钮
// (Task 9)都点名它,状态机上 Cancel 本就是独立迁移(requested→ended)。
// 成功后审计 assist_cancel。
func (h *AgentHandler) HandleAssistCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !assistSameOriginGuard(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // assist_cancel 审计真源:标记先于 proxy 透传
	if h.proxyIfNeeded(w, r) {
		return
	}
	if !h.assistRuntimeGate(w, r) {
		return
	}
	if err := h.service.AssistCancel(); err != nil {
		writeAssistServiceError(w, err)
		return
	}
	snap := h.service.AssistSnapshot() // 粘性终态字段回填 session_id(cancel 不回 sid)
	h.service.RemoteAssistAudit("cancel", fmt.Sprintf("session=%s origin=%s",
		snap.SessionID, webTerminalLoopbackOrigin(r)))
	common.Success(w, snap)
}
