package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAIMCPRoutesRegisteredWithMethodPrefix 锁死 core 进程侧 agent-as-MCP 三条
// 路由的双注册与「方法前缀写进 pattern」两点。
//
// 为什么必须单独测 core 侧：真身在 user agent runtime，但 core 侧是外部 MCP
// 客户端 `aliang mcp` 的唯一入口——缺这三条注册就是 404（2026-10-21 双注册
// 事故的实录形态，只测 runtime 侧覆盖不到）。
//
// 为什么必须断言「方法前缀」：register 是裸 pattern 透传，`register("/api/...")`
// 也能注册成功且测试全绿，但 ServeMux 就退回「任何方法都进 handler」的老语义，
// 同时 OpenAPI catalog 里的 path 会带着/不带 `POST ` 前缀二义（/api/agent/memory
// 六端点先例）。这里用两路独立信号钉死：
//   - mux.Handler 返回的 pattern 必须自带 `POST ` 前缀（裸注册时返回裸路径）；
//   - GET 必须被 ServeMux 兜底成 405 且带 Allow: POST（裸注册时 GET 会进
//     handler，由 handler 自己的方法闸回 405 且没有 Allow 头）。
func TestAIMCPRoutesRegisteredWithMethodPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	paths := []string{
		"/api/agent/ai/mcp/start",
		"/api/agent/ai/mcp/status",
		"/api/agent/ai/mcp/approve",
	}
	for _, path := range paths {
		// 1) 注册存在性 + 方法前缀：pattern 必须是 "POST <path>"。
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Fatalf("%s 未注册（mux.Handler 无匹配 pattern）", path)
		}
		if pattern != http.MethodPost+" "+path {
			t.Fatalf("%s 的 pattern = %q，want %q —— register 是裸 pattern 透传，方法前缀必须写进 path",
				path, pattern, http.MethodPost+" "+path)
		}

		// 2) POST 真能到达 handler（不是 mux 的 404，也不是 mux 的 405）。
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("POST %s 返回 404 —— core 侧路由未注册", path)
		}
		if rec.Code == http.StatusMethodNotAllowed {
			t.Fatalf("POST %s 返回 405 —— pattern 带错方法前缀", path)
		}

		// 3) GET 被 ServeMux 兜底（Allow: POST），而不是漏进 handler。
		getReq := httptest.NewRequest(http.MethodGet, path, nil)
		getRec := httptest.NewRecorder()
		mux.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s code = %d，want 405（带方法前缀的 pattern 由 ServeMux 兜底）",
				path, getRec.Code)
		}
		if allow := getRec.Header().Get("Allow"); !strings.Contains(allow, http.MethodPost) {
			t.Fatalf("GET %s 的 Allow = %q，应含 POST —— 空 Allow 说明 pattern 没带方法前缀，GET 漏进了 handler",
				path, allow)
		}
	}
}
