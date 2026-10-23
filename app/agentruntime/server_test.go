package agentruntime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/common"
)

// TestRegisterAgentRoutesCoversDashboardDataEndpoints 钉死仪表盘数据面端点
// 必须在 user-agent runtime 自有 mux 上注册：核心进程会把这两个请求原样代理
// 过来，缺注册时 runtime 返回 mux 级 404 并被核心透传，仪表盘 Agent 页的
// 远程终端列表与「进行中」徽章因此永远为空（2026-10-21 liang-dev 实测）。
// ALIANG_USER_AGENT_RUNTIME=1 让请求在本进程内直接处理，测试自洽。
func TestRegisterAgentRoutesCoversDashboardDataEndpoints(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "runtime-sessions", path: "/api/agent/runtime-sessions"},
		{name: "terminal-scrollback", path: "/api/agent/terminal/session-1/scrollback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			body := rec.Body.String()
			if strings.Contains(body, "page not found") {
				t.Fatalf("route %s fell through to mux 404 — not registered by registerAgentRoutes", tc.path)
			}
			// runtime-sessions 应 200 且带 terminals 数组；scrollback 对未知会话
			// 返回 JSON 信封（HTTP 404 但走 handler），二者都不能是 mux 裸 404。
			if tc.path == "/api/agent/runtime-sessions" && rec.Code != http.StatusOK {
				t.Fatalf("runtime-sessions status = %d, want 200; body=%s", rec.Code, body)
			}
			if tc.path == "/api/agent/runtime-sessions" && !strings.Contains(body, `"terminals"`) {
				t.Fatalf("runtime-sessions body missing terminals field: %s", body)
			}
			if tc.path == "/api/agent/terminal/session-1/scrollback" {
				var envelope struct {
					Code int `json:"code"`
				}
				if err := common.DecodeJSON(rec.Body, &envelope); err != nil {
					t.Fatalf("scrollback response is not a JSON envelope: %v; body=%s", err, body)
				}
			}
		})
	}
}
