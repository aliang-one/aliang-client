package handlers

// 网页终端 HTTP 面的门前语义测试(Task 7 验收):401/403/405 全部发生在
// proxy 与 service 调用之前——NewAgentHandler(nil) 即可驱动(镜像
// aiStreamGate 测试形态;reset 的 403 若错位到 proxy 之后,core 测试语境会
// 变成拨号失败的 503,断言 403 即证明拦截点在前)。runtime 侧命令矩阵是
// services 层职责,任务 1-4 测试已覆盖;审计落点是结构性约定。

import (
	"encoding/json"
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
	// 有效 cookie 过闸门②后,reset 的 loopback-only 在 core 腿预 proxy 拦截
	// (便利层预检;权威闸在服务层 loopback 布尔,见 WebTerminalRunCommand
	// case "reset" 与 TestWebTerminalResetServiceGateDefeatsProxyJumpShape)。
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

// ——— 阶段 3:FS 四端点门前语义(阶段 3 验收:遍历攻击经 HTTP 全拒的门前
// 半场=401/405 矩阵;穿越本身的拒绝在 services 层 TestAgentFSResolve* 钉死,
// handler 把守卫错误映射 102/103——handlers 包无轻量真身构造器,按 P3.1/P3.2
// 拍板:HTTP 层只钉门前语义+runtime 门序,upload 的全链快乐路径留给真机)———

func TestWebTerminalFSGateMatrix(t *testing.T) {
	h := NewAgentHandler(nil)
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged-token")}

	// 无 cookie 非 loopback → 401;伪造 cookie → 401(四端点同门同治)。
	getHandlers := map[string]func(http.ResponseWriter, *http.Request){
		"list":     h.HandleWebTerminalFSList,
		"read":     h.HandleWebTerminalFSRead,
		"download": h.HandleWebTerminalFSDownload,
	}
	for name, fn := range getHandlers {
		target := "/api/agent/fs/" + name + "?path=x"
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodGet, target, "", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 无登录态应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodGet, target, "", forged))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 伪造 cookie 应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.HandleWebTerminalFSUpload(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/upload", "x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload 无登录态应 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleWebTerminalFSUpload(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/upload", "x", forged))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload 伪造 cookie 应 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 方法闸最前(先于鉴权,同 command/state 约定):GET 打 upload / POST 打 GET 端点 → 405。
	rec = httptest.NewRecorder()
	h.HandleWebTerminalFSUpload(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/fs/upload", "", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 打 upload 应 405, got %d", rec.Code)
	}
	for name, fn := range getHandlers {
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/"+name, "x", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST 打 %s 应 405, got %d", name, rec.Code)
		}
	}
}

func TestWebTerminalFSListLoopbackExemptionPasses(t *testing.T) {
	// 无 cookie 但 loopback 来源:双闸豁免腿放行(与 state 端点同款);core
	// 测试语境过闸后走 proxy 拨号(503 信封或真身应答),绝不能 401。只测只读
	// 端点 list——upload 的豁免腿会真发 POST 给本机 56433,测试不打活体。
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	req := webTerminalGateRequest(http.MethodGet, "/api/agent/fs/list", "", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleWebTerminalFSList(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("loopback 豁免不应 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebTerminalFSRuntimeGateDirect(t *testing.T) {
	// 直驱 runtime 门:无令牌必 403。NewAgentHandler(nil) 会兜底到 shared
	// service(恒非 nil),故 403 只可能来自主开关(disabled)或令牌(invalid
	// token)分支——body 断言把这一点钉死,防 nil-service 防御分支被误信为
	// 本用例的覆盖(两分支的字面见 webTerminalFSRuntimeGate;enabled/token
	// 语义本身在 services 层 vault 测试覆盖)。
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	if h.webTerminalFSRuntimeGate(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/fs/list", "", nil)) {
		t.Fatal("无令牌不应过 runtime 门")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无令牌 runtime 门应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Web terminal is disabled") && !strings.Contains(body, "Invalid web terminal token") {
		t.Fatalf("403 应来自主开关/令牌分支(shared service 恒非 nil), got %s", body)
	}
}

func TestWebTerminalFSRuntimeLegGateBeforeBodyParse(t *testing.T) {
	// 本用例钉的是**门先于 body 解析的顺序**,不是某个 403 分支:body 故意
	// 喂坏 multipart,若 runtime 门错位到 ParseMultipartForm 之后,这里会是
	// 400 而非 403。403 来源与 Direct 用例同断言(enabled/token 分支;nil-
	// service 分支经 NewAgentHandler 不可达,service 兜底恒非 nil)。构造先
	// 于 env 切换:shared service 以 core 角色建好,避免 runtime 角色构造
	// 触发 boot 重连兜底。
	h := NewAgentHandler(nil)
	t.Setenv(services.AgentRuntimeEnv, "1")
	// list:过双闸→非 proxy(runtime 角色)→runtime 门 403。
	rec := httptest.NewRecorder()
	h.HandleWebTerminalFSList(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/fs/list", "", mintDashboardCookie(t)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("runtime 腿 list 无令牌应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Web terminal is disabled") && !strings.Contains(body, "Invalid web terminal token") {
		t.Fatalf("403 应来自主开关/令牌分支, got %s", body)
	}
	// upload:403 必须发生在任何 body 解析之前(见上)。
	rec = httptest.NewRecorder()
	h.HandleWebTerminalFSUpload(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/upload", "garbage-not-multipart", mintDashboardCookie(t)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("runtime 腿 upload 无令牌应 403(且先于 body 解析), got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "Web terminal is disabled") && !strings.Contains(body, "Invalid web terminal token") {
		t.Fatalf("upload 403 应来自主开关/令牌分支且先于 body 解析, got %s", body)
	}
	// 写操作三端点(文件管理批 Task B):403 同样先于 body 解析(body 故意
	// 喂坏 JSON,若门错位到解析之后会是 400 而非 403——PROXY-FIRST 契约的
	// runtime 半场)。
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"mkdir":  h.HandleWebTerminalFSMkdir,
		"rename": h.HandleWebTerminalFSRename,
		"delete": h.HandleWebTerminalFSDelete,
	} {
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/"+name, "garbage-not-json", mintDashboardCookie(t)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("runtime 腿 %s 无令牌应 403(且先于 body 解析), got %d body=%s", name, rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, "Web terminal is disabled") && !strings.Contains(body, "Invalid web terminal token") {
			t.Fatalf("%s 403 应来自主开关/令牌分支且先于 body 解析, got %s", name, body)
		}
	}
}

// ——— FS 写操作三端点(文件管理批 Task B):门前语义 + marking ———

func TestWebTerminalFSMutationGateMatrix(t *testing.T) {
	h := NewAgentHandler(nil)
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged-token")}
	mutationHandlers := map[string]func(http.ResponseWriter, *http.Request){
		"mkdir":  h.HandleWebTerminalFSMkdir,
		"rename": h.HandleWebTerminalFSRename,
		"delete": h.HandleWebTerminalFSDelete,
	}
	bodyBy := map[string]string{
		"mkdir":  `{"dir":".","name":"x"}`,
		"rename": `{"dir":".","oldName":"a","newName":"b"}`,
		"delete": `{"path":"a"}`,
	}
	for name, fn := range mutationHandlers {
		// 无 cookie 非 loopback → 401;伪造 cookie → 401(与四端点同门同治)。
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/"+name, bodyBy[name], nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 无登录态应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/fs/"+name, bodyBy[name], forged))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 伪造 cookie 应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
		// 方法闸最前(先于鉴权,同 upload/list 约定):GET 打 POST 端点 → 405。
		rec = httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/fs/"+name, "", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET 打 %s 应 405, got %d", name, rec.Code)
		}
	}
}

func TestWebTerminalFSMutationLoopbackMarking(t *testing.T) {
	// core 角色(未设 runtime env):三 POST 端点必须在 proxy 之前完成真源
	// 标记——LAN 来源的伪造 X-Aliang-WT-Loopback 必须被剥净(否则经 proxy
	// 透传可在 runtime 侧冒充 loopback),loopback 来源置 1。断言只看 handler
	// 返回后 r.Header 的同步突变,不钉响应码:56433 拨号被拒=503;本机若有
	// 活 runtime,请求不带令牌其 runtime 门也在触盘前 403(无副作用),两
	// 种结局对头部真值均无影响。
	h := NewAgentHandler(nil)
	mutationHandlers := map[string]func(http.ResponseWriter, *http.Request){
		"mkdir":  h.HandleWebTerminalFSMkdir,
		"rename": h.HandleWebTerminalFSRename,
		"delete": h.HandleWebTerminalFSDelete,
	}
	for name, fn := range mutationHandlers {
		req := webTerminalGateRequest(http.MethodPost, "/api/agent/fs/"+name, `{"dir":".","name":"x"}`, mintDashboardCookie(t))
		req.Header.Set(WebTerminalLoopbackHeader, "1") // LAN 伪造 loopback
		fn(httptest.NewRecorder(), req)
		if got := req.Header.Get(WebTerminalLoopbackHeader); got != "" {
			t.Fatalf("%s marking 必须剥净伪造真源头, got %q", name, got)
		}
	}
	// loopback 来源(loopback 豁免腿过闸):core 见证并置 1,runtime 侧审计
	// origin 据此为真。
	req := webTerminalGateRequest(http.MethodPost, "/api/agent/fs/mkdir", `{"dir":".","name":"x"}`, nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleWebTerminalFSMkdir(httptest.NewRecorder(), req)
	if got := req.Header.Get(WebTerminalLoopbackHeader); got != "1" {
		t.Fatalf("loopback 来源应被标记为 1, got %q", got)
	}
}

// ——— P3.6 审计真源标记(阶段 1 终审 Finding B 残余)———
// X-Aliang-WT-Loopback:core 腿 proxy 前见证真实来源并编码进头,runtime 腿
// 只认头(透传腿 RemoteAddr 恒 127.0.0.1,IsLoopbackRequest 不可用)。

func TestWebTerminalLoopbackSourceHeaderMarking(t *testing.T) {
	// core 角色(未设 runtime env):LAN 来源+伪造同名头 → 必须剥净(否则经
	// proxy 透传可在 runtime 侧冒充 loopback);loopback 来源 → 置 1。
	req := webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"state"}`, nil)
	req.Header.Set(WebTerminalLoopbackHeader, "1") // LAN 伪造
	markWebTerminalLoopbackSource(req)
	if got := req.Header.Get(WebTerminalLoopbackHeader); got != "" {
		t.Fatalf("LAN 来源的伪造真源头必须被剥净, got %q", got)
	}

	req = webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"state"}`, nil)
	req.RemoteAddr = "127.0.0.1:55555"
	markWebTerminalLoopbackSource(req)
	if got := req.Header.Get(WebTerminalLoopbackHeader); got != "1" {
		t.Fatalf("loopback 来源应被标记为 1, got %q", got)
	}

	// runtime 角色:头是 core 透传来的真源凭证,不得按本跳 RemoteAddr 重写
	// (proxy 拨号恒 loopback,重写会把 LAN 又误标回 loopback——Finding B 本体)。
	t.Setenv(services.AgentRuntimeEnv, "1")
	req = webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"state"}`, nil)
	req.Header.Set(WebTerminalLoopbackHeader, "1") // 模拟 core 透传的 LAN→loopback 见证
	markWebTerminalLoopbackSource(req)
	if got := req.Header.Get(WebTerminalLoopbackHeader); got != "1" {
		t.Fatalf("runtime 腿不得重写真源头, got %q", got)
	}
}

func TestWebTerminalCommandLoopbackHeaderSemantics(t *testing.T) {
	// 透传模拟:runtime 角色 + 手动预置 X-Aliang-WT-Loopback=1(等价 core 腿
	// 对 loopback 来源的见证标记经 proxy 透传)。可观测语义=killall 空令牌:
	// 有真源头放行(ok:true),无头拒(bad_token)。请求取 loopback RemoteAddr
	// ——过闸/过 core 式 killall 前置检查后,到达 RunCommand 时唯一变量就是头:
	// 修正前 runtime 腿 IsLoopbackRequest(RemoteAddr)恒真两条都放行(Finding B
	// 本体);修正后 RemoteAddr 不再决定语义,只有 core 见证的头算数(直呼
	// runtime 的本机调用无头=保守按 cookie 拒)。shared service 无终端会话,
	// closeAll 空转无副作用。
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := NewAgentHandler(nil)

	run := func(withHeader bool) (bool, string) {
		req := webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"killall"}`, nil)
		req.RemoteAddr = "127.0.0.1:55555" // 过闸豁免腿;语义变量只剩真源头
		if withHeader {
			req.Header.Set(WebTerminalLoopbackHeader, "1")
		}
		rec := httptest.NewRecorder()
		h.HandleWebTerminalCommand(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("业务命令应 HTTP 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		var envelope struct {
			Data struct {
				OK     bool   `json:"ok"`
				Reason string `json:"reason"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("解析信封: %v body=%s", err, rec.Body.String())
		}
		return envelope.Data.OK, envelope.Data.Reason
	}

	ok, reason := run(true) // 真源=loopback(core 见证标记透传)
	if !ok {
		t.Fatalf("真源头=1 时 killall 空令牌应放行(loopback 语义), reason=%s", reason)
	}
	ok, reason = run(false) // 无头:RemoteAddr 虽 loopback 但无 core 见证
	if ok || reason != "bad_token" {
		t.Fatalf("无真源头时 killall 空令牌应拒(bad_token), got ok=%v reason=%s", ok, reason)
	}
}
