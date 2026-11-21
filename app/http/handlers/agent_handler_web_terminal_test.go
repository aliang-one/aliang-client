package handlers

// 网页终端 HTTP 面的门前语义测试(Task 7 验收):401/403/405 全部发生在
// proxy 与 service 调用之前——NewAgentHandler(nil) 即可驱动(镜像
// aiStreamGate 测试形态;reset 的 403 若错位到 proxy 之后,core 测试语境会
// 变成拨号失败的 503,断言 403 即证明拦截点在前)。runtime 侧命令矩阵是
// services 层职责,任务 1-4 测试已覆盖;审计落点是结构性约定。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

// webTerminalGateRequest 构造同源形态请求(Origin=Host,镜像 aiStreamGateRequest);
// body 非空时附 Content-Type。RemoteAddr 恒非 loopback:cookie 闸必须独立扛住
// (loopback 豁免是另一条腿,不在此测)。
func webTerminalGateRequest(method, target, body string, sessionCookie *http.Cookie) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", "http://127.0.0.1:56431")
	req.Host = "127.0.0.1:56431"
	req.RemoteAddr = "192.168.1.50:55555"
	if sessionCookie != nil {
		req.AddCookie(sessionCookie)
	}
	return req
}

func TestWebTerminalStateRejectsHandshakeWithoutSession(t *testing.T) {
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalState(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无登录态读状态应 401,got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "dashboard session") {
		t.Fatalf("错误语义应指向 dashboard session: %s", rec.Body.String())
	}
}

func TestWebTerminalCommandRejectsForgedCookie(t *testing.T) {
	h := NewAgentHandler(nil)
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged-token")}
	rec := httptest.NewRecorder()
	h.HandleWebTerminalCommand(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"state"}`, forged))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie 应 401,got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebTerminalCommandResetRequiresLoopback(t *testing.T) {
	// 有效 cookie 过闸门②后,reset 的 loopback-only 拦截必须在 proxyIfNeeded
	// 之前(core 腿拦截;runtime 侧天然 loopback 拦不住)。
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalCommand(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"reset"}`, mintDashboardCookie(t)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非 loopback reset 应 403,got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "loopback") {
		t.Fatalf("错误语义应指向 loopback only: %s", rec.Body.String())
	}
}

func TestWebTerminalCommandKillallRequiresTokenForLAN(t *testing.T) {
	// 终审 Finding A:runtime 侧 IsLoopbackRequest 恒真(core 透传腿来源恒
	// 127.0.0.1),服务级「非 loopback 空令牌拒」经 HTTP 不可达——killall 的
	// 空令牌放行必须像 reset 一样在 core 腿预 proxy 拦截,否则持有效 cookie
	// 的 LAN 会话可无令牌触发会话杀伤。
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalCommand(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"killall"}`, mintDashboardCookie(t)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("LAN 无令牌 killall 应 403,got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unlock token") {
		t.Fatalf("错误语义应指向 unlock token: %s", rec.Body.String())
	}
	// 带令牌的 LAN killall 不在 core 腿拦(有效性由 runtime 校验):不应 403
	rec = httptest.NewRecorder()
	req := webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"killall"}`, mintDashboardCookie(t))
	req.Header.Set("X-Web-Terminal-Token", "some-token")
	h.HandleWebTerminalCommand(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("LAN 带令牌 killall 不应在 core 腿被拒,got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebTerminalCommandRejectsWrongMethod(t *testing.T) {
	h := NewAgentHandler(nil)
	// GET 打 command(要求 POST)→ 405(方法闸最前,先于鉴权)。
	rec := httptest.NewRecorder()
	h.HandleWebTerminalCommand(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal", "", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 打 command 应 405,got %d body=%s", rec.Code, rec.Body.String())
	}
	// POST 打 state(要求 GET)→ 405。
	rec2 := httptest.NewRecorder()
	h.HandleWebTerminalState(rec2, webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"state"}`, nil))
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 打 state 应 405,got %d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestWebTerminalGateLoopbackExemptionPasses(t *testing.T) {
	// 无 cookie 但 loopback 来源:双闸的 loopback 豁免腿放行(与 HandleAIStream
	// 同款的另一半)。过闸后 core 测试语境走 proxy 拨号 → 503 信封;断言
	// 非 401 即证明豁免(与 ai-stream 闸测同款约定)。
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	req := webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal", "", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleWebTerminalState(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("loopback 豁免不应 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestParseWebTerminalCommandBodyRestoresBodyForProxy(t *testing.T) {
	// 评审 Critical 的回归钉:core 腿命令 POST 在 proxyIfNeeded 之前要先读
	// body 判 reset,读后必须回填——proxyToUserAgentClient 直接转发 r.Body,
	// 不回填=runtime 收空体 400,LAN 全链路死。此处直接钉住「解析成功且
	// body 可整读还原」不变量,不依赖 proxy 机制。
	raw := `{"action":"change","pin":"","old_pin":"1234","new_pin":"5678"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent/web-terminal", strings.NewReader(raw))
	action, pin, oldPin, newPin, enabled, ok := parseWebTerminalCommandBody(req)
	if !ok {
		t.Fatal("合法体应 ok=true")
	}
	if action != "change" || pin != "" || oldPin != "1234" || newPin != "5678" || enabled != nil {
		t.Fatalf("字段解析不符: action=%q pin=%q old=%q new=%q enabled=%v", action, pin, oldPin, newPin, enabled)
	}
	again, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("重读回填 body: %v", err)
	}
	if string(again) != raw {
		t.Fatalf("回填 body 与原始字节不符: got %q want %q", again, raw)
	}

	// 解析失败路径:坏 JSON → ok=false(400 语义由 handler 落)。
	bad := httptest.NewRequest(http.MethodPost, "/api/agent/web-terminal", strings.NewReader("not-json"))
	if _, _, _, _, _, ok := parseWebTerminalCommandBody(bad); ok {
		t.Fatal("坏 JSON 应 ok=false")
	}
	empty := httptest.NewRequest(http.MethodPost, "/api/agent/web-terminal", strings.NewReader(""))
	if _, _, _, _, _, ok := parseWebTerminalCommandBody(empty); ok {
		t.Fatal("空 body 应 ok=false")
	}
}

func TestWebTerminalStreamGateMatrix(t *testing.T) {
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleWebTerminalStream(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal/stream", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无登录态 WS 握手应 401, got %d", rec.Code)
	}
	cookie := mintDashboardCookie(t)
	rec = httptest.NewRecorder()
	h.HandleWebTerminalStream(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/web-terminal/stream", "", cookie))
	// 门过:非 runtime 角色→透传拨号失败(502/1011 形态);绝不能 401
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("持 session 握手不应 401, got %d", rec.Code)
	}
}

func TestWebTerminalStreamRejectsBadAuthFrame(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1") // 强制 runtime 角色:auth 逻辑在本腿
	h := NewAgentHandler(nil)               // service=nil:nil-service 守卫应拦截(1011)
	srv := httptest.NewServer(http.HandlerFunc(h.HandleWebTerminalStream))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent/web-terminal/stream"
	// Origin 必须取 srv.URL(含随机端口)——同源闸是精确 host:port 比较,
	// 写死 56431 会对不上 httptest 的随机端口,直接被 403
	dialHeader := http.Header{"Origin": []string{srv.URL}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, dialHeader)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]interface{}{"type": "terminal.auth", "token": "bogus"})
	_, _, err = conn.ReadMessage()
	if err == nil {
		// nil-service 路径经 rejectBrowserWS 是 close 1011;真实 service 下坏令牌是 1008
		t.Fatal("坏令牌/无服务应被服务端断开,不应收到任何业务帧")
	}
}
