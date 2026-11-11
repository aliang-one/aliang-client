package agentruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
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

// TestRegisterAgentRoutesIncludesAIStream 钉死仪表盘聊天 WS 端点：core 会把
// /api/agent/ai/stream 透传到本 runtime（2026-10-28 起仪表盘聊天统一走
// agent），缺注册=404 透传、仪表盘聊天全挂。ALIANG_USER_AGENT_RUNTIME=1 +
// 独立 HOME 让请求在本进程内直接处理（与上方用例同范式，避免 dev 机 56433
// 有活 agent 时握手后永久泵帧、测试挂到超时）；普通 GET 请求由 upgrader 以
// 400 拒绝并直接返回，绝不会是 mux 裸 404——404 即漏注册。
func TestRegisterAgentRoutesIncludesAIStream(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/agent/ai/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("/api/agent/ai/stream status = 404：registerAgentRoutes 漏注册")
	}
}

// TestRegisterAgentRoutesIncludesAuthRecover 钉死 auth_expired 自愈链端点：
// core 在每次 token 刷新成功后经 requestLocalUserAgentRecoverAfterAuthExpired
// 直连 POST 本端点（agent_service.go），仪表盘侧则经 HandleAuthRecover 的
// proxyIfNeeded 转发过来。缺注册时两路都 404，auth_expired 终态在 core+user
// agent 双进程拓扑下永不自愈（2026-10-29 修复，双注册教训同族）。RecoverIfAuthExpired
// 自门控（非 auth_expired 态为纯读 no-op），走到 handler 即成功，绝不应当是
// mux 裸 404。
func TestRegisterAgentRoutesIncludesAuthRecover(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/agent/auth-recover", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("/api/agent/auth-recover status = 404：registerAgentRoutes 漏注册，auth_expired 自愈链断")
	}
}

// TestRegisterAgentRoutesCoversAgentMemoryEndpoints 双注册另一半的真函数钉死:
// 直接调 registerAgentRoutes(零依赖本地 mux),探记忆六 pattern 各得预期码。
// handlers 包的手抄 mux 测试与生产注册不再是恒等价闭包(手抄侧按 core routes.go
// 真实 register 形态),本侧漏注册/注册冲突 panic 在此显形(双注册老坑同族)。
func TestRegisterAgentRoutesCoversAgentMemoryEndpoints(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")
	t.Setenv("HOME", t.TempDir())
	// store 目录换 TempDir:真 registerAgentRoutes 打真 handler,直写真
	// agentHome() 会污染本机 ~/.aliang/memory。
	restore := services.AgentCoreMemory.SetDirForTest(t.TempDir())
	defer restore()

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	// POST 铺一条拿 id,驱动 PUT/DELETE 单条探针。
	postRec := httptest.NewRecorder()
	mux.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/api/agent/memory",
		strings.NewReader(`{"project":"/tmp/runtime-probe","content":"运行时探针"}`)))
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST /api/agent/memory status = %d, want 200; body=%s", postRec.Code, postRec.Body.String())
	}
	var postEnv struct {
		Data struct {
			Entry struct {
				ID int `json:"id"`
			} `json:"entry"`
		} `json:"data"`
	}
	if err := json.NewDecoder(postRec.Body).Decode(&postEnv); err != nil {
		t.Fatalf("POST body not JSON envelope: %v; body=%s", err, postRec.Body.String())
	}
	if postEnv.Data.Entry.ID <= 0 {
		t.Fatalf("POST entry id = %d, want > 0; body=%s", postEnv.Data.Entry.ID, postRec.Body.String())
	}
	id := postEnv.Data.Entry.ID

	for _, probe := range []struct {
		name   string
		method string
		target string
		body   string
		want   int
	}{
		{name: "list", method: http.MethodGet, target: "/api/agent/memory?project=/tmp/runtime-probe", want: http.StatusOK},
		{name: "projects", method: http.MethodGet, target: "/api/agent/memory/projects", want: http.StatusOK},
		{name: "put", method: http.MethodPut, target: "/api/agent/memory/" + strconv.Itoa(id), body: `{"project":"/tmp/runtime-probe","content":"运行时改写"}`, want: http.StatusOK},
		{name: "delete_single", method: http.MethodDelete, target: "/api/agent/memory/" + strconv.Itoa(id) + "?project=/tmp/runtime-probe", want: http.StatusOK},
		{name: "delete_clear", method: http.MethodDelete, target: "/api/agent/memory?project=/tmp/runtime-probe", want: http.StatusOK},
	} {
		t.Run(probe.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(probe.method, probe.target, strings.NewReader(probe.body)))
			if rec.Code != probe.want {
				t.Fatalf("%s %s status = %d, want %d (mux 裸 404 即漏注册); body=%s",
					probe.method, probe.target, rec.Code, probe.want, rec.Body.String())
			}
		})
	}
}

// TestRegisterAgentRoutesCoversAIMCPEndpoints 钉「双注册另一半」：agent-as-MCP
// 工具面三端点真身在本 runtime，核心侧 routes.go 同名注册只做 proxy 透传。
// 缺这里 → 核心转发过来撞 mux 裸 404 并原样透传，外部 MCP 客户端的
// ai_run_start/status/approve 永远拿 404（2026-10-21 liang-dev 双注册教训）。
// 只断言「不是 404」：200/400/403/503 都是 handler 真实出手的证明。
func TestRegisterAgentRoutesCoversAIMCPEndpoints(t *testing.T) {
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	registerAgentRoutes(mux)

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "start", path: "/api/agent/ai/mcp/start", body: `{}`},
		{name: "status", path: "/api/agent/ai/mcp/status", body: `{}`},
		{name: "approve", path: "/api/agent/ai/mcp/approve", body: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("POST %s status = 404：registerAgentRoutes 漏注册 agent-as-MCP 端点，外部 MCP 工具面全链断", tc.path)
			}
		})
	}
}
