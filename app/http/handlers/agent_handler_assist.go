package handlers

// 远程协助 P1 总开关 gates 面(spec docs/superpowers/specs/2026-11-26-remote-assist-design.md
// §7.1,plan Task 3):GET 读 / POST 写 remote_assist_enabled。真身在 user agent
// runtime(状态机与终端接线都在彼处),core 侧照 HandlePeerGates 形只做
// proxyIfNeeded 透传;同路径双方法(method switch 在内)。本 Task 仅放 gates
// 端点;request/session/end 三端点是 Task 6,WS 腿是 Task 4/5。

import (
	"encoding/json"
	"io"
	"net/http"

	"aliang.one/nursorgate/app/http/common"
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
