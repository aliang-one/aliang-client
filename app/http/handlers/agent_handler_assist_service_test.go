package handlers

// 远程协助 P2 Task 2:同构代理路由测试。判别测试是承重项:**前缀剥离**——
// GET /assist/service/assets/styles.css 断言上游收到 /assets/styles.css。
// 不剥则每个资产/API 请求 404,而 SPA 回退根 handler 对缺失资产返回
// 200+index.html(静默坏页),其余闸门/剥头测试都抓不住它的缺失,必须专测
// (plan Task 2 评审承重勘误)。闸矩阵照 assist 家族四段制;会话闸双腿各测
// (runtime 腿进程内快照真值,core 腿探针拨 runtime session 端点);剥头
// 断言钉 X-Frame-Options 全剥 + CSP 仅摘 frame-ancestors 指令。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

// assistServiceUpstream 本地 httptest 反代上游:记录收到的 RequestURI/方法/
// 体/透传腿真源标记头(判别测试、写方法流穿与标记背书断言),回预置响应头
// 与体。
type assistServiceUpstream struct {
	mu      sync.Mutex
	uris    []string
	methods []string
	bodies  []string
	markers []string
	header  http.Header
	body    string
}

func (u *assistServiceUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.uris = append(u.uris, r.URL.RequestURI())
		u.methods = append(u.methods, r.Method)
		u.bodies = append(u.bodies, string(raw))
		u.markers = append(u.markers, r.Header.Get(WebTerminalLoopbackHeader))
		u.mu.Unlock()
		for key, values := range u.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(u.body))
	})
}

func (u *assistServiceUpstream) requests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.uris...)
}

// lastHit 最近一次命中(方法/剥离后路径/体/真源标记——写方法与背书断言)。
func (u *assistServiceUpstream) lastHit(t *testing.T) (method, uri, body, marker string) {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.uris) == 0 {
		t.Fatalf("expected at least one upstream hit, got none")
	}
	i := len(u.uris) - 1
	return u.methods[i], u.uris[i], u.bodies[i], u.markers[i]
}

// newAssistServiceUpstream 起上游并接进生产解析链(services 覆盖位——
// SessionOwnerAddrOverride 是端口回退回灌写同一条链,反代上游由此指到
// 可断言的 httptest;Cleanup 复位,进程级缝纪律同 AssistSetFrameSink)。
func newAssistServiceUpstream(t *testing.T, header http.Header, body string) *assistServiceUpstream {
	t.Helper()
	up := &assistServiceUpstream{header: header, body: body}
	server := httptest.NewServer(up.handler())
	t.Cleanup(server.Close)
	services.SetSessionOwnerAddrOverride(server.URL)
	t.Cleanup(func() { services.SetSessionOwnerAddrOverride("") })
	return up
}

// stubAssistProbeTransport core 腿会话探针(h.client 拨 56433)的确定性应答。
// env pin 在此不可用(DefaultUserAgentAddr package init 已解析,t.Setenv 晚于
// init 无效),client 注入是同族测试的既有等价物(TestHandleAssistClientGateMatrix
// 评审 Fix 1 同款),并避免拨到本机真实 runtime。
type stubAssistProbeTransport struct {
	status int
	body   string
}

func (tr *stubAssistProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status := tr.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(tr.body)),
		Request:    req,
	}, nil
}

// assistServiceRequest 构造同源 LAN 形态代理请求(webTerminalGateRequest
// 委托,本文件语义别名——读测试时一眼看到被测路由)。
func assistServiceRequest(path string, cookie *http.Cookie) *http.Request {
	return webTerminalGateRequest(http.MethodGet, path, "", cookie)
}

func TestHandleAssistServiceGateMatrix(t *testing.T) {
	// core 腿(未设 runtime env):门前语义全在探针/反代之前。透传腿拒绝
	// 拨号注入(家族惯例,评审 Fix 1 同款)——过闸臂确定性失败,不拨本机
	// 真实进程。
	h := NewAgentHandler(nil)
	h.client = &http.Client{Timeout: 150 * time.Millisecond, Transport: refusingRoundTripper{}}
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged-token")}

	// ①方法闸最前(先于鉴权,web-terminal 家族约定):白名单外方法 → 405
	// (评审修订①:白名单含 GET/HEAD/POST/PUT/PATCH/DELETE——镜像操作面
	// 写方法必须放行;此处钉白名单外的 CONNECT/TRACE)。
	for _, method := range []string{http.MethodConnect, http.MethodTrace, "OPTIONS"} {
		rec := httptest.NewRecorder()
		h.HandleAssistServiceProxy(rec, webTerminalGateRequest(method, "/assist/service/assets/styles.css", "", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 打代理路由应 405, got %d body=%s", method, rec.Code, rec.Body.String())
		}
	}

	// POST 已入白名单(评审修订①):无 cookie LAN 形态不再 405,而是走到
	// 鉴权闸 401(方法闸先行不越过鉴权,写方法的门收紧在会话闸,见
	// TestHandleAssistServiceWriteMethodsAndMarker 的 ended 态 403 臂)。
	rec := httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, webTerminalGateRequest(http.MethodPost, "/assist/service/api/dashboard/session", `{}`, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST 无 cookie 应走到鉴权 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	// ②无 cookie 非 loopback → 401;伪造 cookie → 401(同源 LAN 形态)。
	for name, cookie := range map[string]*http.Cookie{"无cookie": nil, "伪造cookie": forged} {
		rec := httptest.NewRecorder()
		h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
	}

	// ③跨站 Origin → 403(同源闸,持有效 cookie 也拒;loopback 腿同样吃)。
	rec = httptest.NewRecorder()
	req := assistServiceRequest("/assist/service/assets/styles.css", mintDashboardCookie(t))
	req.Header.Set("Origin", "http://evil.example:8080")
	h.HandleAssistServiceProxy(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin 应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Cross-origin") {
		t.Fatalf("403 语义应指向跨站: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = assistServiceRequest("/assist/service/assets/styles.css", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	req.Header.Set("Origin", "http://evil.example:8080")
	h.HandleAssistServiceProxy(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback 来源跨站 Origin 应 403, got %d body=%s", rec.Code, rec.Body.String())
	}

	// ④loopback 豁免腿放行(非 401/403 即证明;过闸后探针拨号被拒=503 信封)。
	rec = httptest.NewRecorder()
	req = assistServiceRequest("/assist/service/assets/styles.css", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleAssistServiceProxy(rec, req)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("loopback 豁免不应被门拒, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAssistServiceReentryGuard(t *testing.T) {
	// 回环守卫:嵌套前缀路径剥离一次后仍带 /assist/service,必 400 且零拨号
	// (守卫先于会话探针与反代——夹具不注入任何拨号通道,真拨了测试必挂)。
	// N 层嵌套=N 跳 loopback 自拨的放大面,靠前缀剥离无法自消,显式守卫兜。
	h := NewAgentHandler(nil)
	rec := httptest.NewRecorder()
	req := assistServiceRequest("/assist/service/assist/service/assets/x", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleAssistServiceProxy(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("嵌套前缀路径应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid assist service path") {
		t.Fatalf("400 语义应指向路径非法: %s", rec.Body.String())
	}
}

func TestHandleAssistServiceCoreSessionGate(t *testing.T) {
	// core 腿会话闸:会话真源在 user agent runtime,本路由经探针取值
	// (admin 隧道腿落在 core,spec §8.3)。上游=覆盖链指到的 httptest。
	up := newAssistServiceUpstream(t, nil, "asset-bytes")
	h := NewAgentHandler(nil)
	cookie := mintDashboardCookie(t)

	// 探针回 idle → 403 会话闸,上游零请求。
	h.client = &http.Client{Timeout: 2 * time.Second,
		Transport: &stubAssistProbeTransport{body: `{"code":0,"data":{"state":""}}`}}
	rec := httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("探针 idle 应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not active") {
		t.Fatalf("403 语义应指向会话不活跃: %s", rec.Body.String())
	}
	if got := up.requests(); len(got) != 0 {
		t.Fatalf("闸挡住时上游不得收到请求, got %v", got)
	}

	// 探针拨号失败 → 503 信封(CodeServiceUnavailable=201,HTTP 落 500,
	// 同 proxy-不可达既有惯例)。
	h.client = &http.Client{Timeout: 150 * time.Millisecond, Transport: refusingRoundTripper{}}
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if code, _ := assistEnvelope(t, rec); code != common.CodeServiceUnavailable || rec.Code != http.StatusInternalServerError {
		t.Fatalf("探针失败应 201 信封/HTTP 500, got http=%d body=%s", rec.Code, rec.Body.String())
	}

	// 探针遇 runtime 403(总开关关)→ 同归会话不活跃 403(不向观看端泄漏
	// 开关态细节,资产加载语境下粒度无差)。
	h.client = &http.Client{Timeout: 2 * time.Second,
		Transport: &stubAssistProbeTransport{status: http.StatusForbidden}}
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("探针 403 应同归会话闸 403, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 探针回 active → 反代放行。**前缀剥离判别测试**(承重):上游收到的
	// 必须是剥离后的 /assets/styles.css(带原 query),而非原样
	// /assist/service/assets/styles.css——不剥=上游 404 且 SPA 回退根
	// handler 对缺失资产静默回 200+index.html。
	h.client = &http.Client{Timeout: 2 * time.Second,
		Transport: &stubAssistProbeTransport{body: `{"code":0,"data":{"state":"active"}}`}}
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css?hash=abc", cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("active 会话放行应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := up.requests()
	if len(got) != 1 || got[0] != "/assets/styles.css?hash=abc" {
		t.Fatalf("判别失败:上游应恰收到剥离后路径 /assets/styles.css?hash=abc, got %v", got)
	}
	if rec.Body.String() != "asset-bytes" {
		t.Fatalf("上游响应体应透传, got %q", rec.Body.String())
	}

	// 前缀根 landing:/assist/service/ 归一到上游 /(镜像 iframe 的应用根)。
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/", cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("前缀根 landing 应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := up.requests(); len(got) != 2 || got[1] != "/" {
		t.Fatalf("前缀根应剥成上游 /, got %v", got)
	}

	// 真源标记背书(core 入腿,评审 Fix 2):cookie 入腿 → 上游见 cookie
	// 哨兵——core 自拨腿的上游 mark 只见本跳 RemoteAddr=拨号源(loopback),
	// 无哨兵会把 LAN cookie 真源洗成假 loopback(fs_*/lock 审计失真)。
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/api/agent/assist/session", cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie 入腿放行应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, _, _, marker := up.lastHit(t); marker != webTerminalLoopbackCookieMarker {
		t.Fatalf("core 腿 cookie 入腿应背书 cookie 哨兵, got %q", marker)
	}

	// loopback 入腿 → 上游见 "1"(豁免腿真源)。
	loopReq := assistServiceRequest("/assist/service/assets/styles.css", nil)
	loopReq.RemoteAddr = "127.0.0.1:55555"
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, loopReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback 入腿放行应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, _, _, marker := up.lastHit(t); marker != "1" {
		t.Fatalf("core 腿 loopback 入腿应背书 1, got %q", marker)
	}
}

func TestHandleAssistServiceRuntimeGateAndPrefixStrip(t *testing.T) {
	// runtime 腿端到端:总开关关 403 → idle 403 → requested 放行+判别 →
	// ended 403。会话真值用真身状态机驱动(帧缝夹具),不经任何探针。
	h, _ := newAssistSessionHandler(t)
	up := newAssistServiceUpstream(t, nil, "asset-bytes")
	cookie := mintDashboardCookie(t)

	// 总开关缺省关(零值纪律)→ 403 disabled。
	rec := httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("开关关应 403 disabled, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 开关开、idle → 403 会话闸,上游零请求。
	h.service.SetRemoteAssistEnabled(true)
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not active") {
		t.Fatalf("idle 应 403 not active, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := up.requests(); len(got) != 0 {
		t.Fatalf("闸挡住时上游不得收到请求, got %v", got)
	}

	// 申请进 requested(真身状态机,缝在=信令成功)→ 放行;**前缀剥离判别**。
	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"x"}`, cookie))
	if r1.Code != 200 {
		t.Fatalf("申请应 200, got %d body=%s", r1.Code, r1.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css?hash=abc", cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("requested 会话放行应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := up.requests()
	if len(got) != 1 || got[0] != "/assets/styles.css?hash=abc" {
		t.Fatalf("判别失败:上游应恰收到剥离后路径 /assets/styles.css?hash=abc, got %v", got)
	}
	if rec.Body.String() != "asset-bytes" {
		t.Fatalf("上游响应体应透传, got %q", rec.Body.String())
	}

	// 撤销进 ended → 403 会话闸(requested/active 才放行,spec §11)。
	r2 := httptest.NewRecorder()
	h.HandleAssistCancel(r2, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if r2.Code != 200 {
		t.Fatalf("撤销应 200, got %d body=%s", r2.Code, r2.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ended 会话应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := up.requests(); len(got) != 1 {
		t.Fatalf("ended 闸挡住时上游不得新增请求, got %v", got)
	}
}

func TestHandleAssistServiceWriteMethodsAndMarker(t *testing.T) {
	// 评审修订两件的 runtime 腿端到端:
	// ①写方法放行(评审修订①):POST/PUT/DELETE 经剥离反代,方法与体原样
	//   到达上游——镜像仪表盘的操作流量是 POST/写携带的(实测 agentApi.js
	//   PUT×3/DELETE×3/PATCH×1),GET-only 会把镜像操作面饿死;写请求与
	//   GET 同吃会话闸(ended 态 403 臂钉住)。
	// ②真源标记背书(评审修订②):cookie 入腿→上游见 cookie 哨兵(上游
	//   mark 采信跳过重标,不得假 loopback);core 背书来的 "1" 在 runtime
	//   透传腿原样转发(本跳 RemoteAddr 恒拨号源,无真源信息)。
	h, _ := newAssistSessionHandler(t)
	up := newAssistServiceUpstream(t, nil, "ok")
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)

	// 进 requested(写方法的会话闸前置态)。
	r0 := httptest.NewRecorder()
	h.HandleAssistRequest(r0, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"x"}`, cookie))
	if r0.Code != 200 {
		t.Fatalf("申请应 200, got %d body=%s", r0.Code, r0.Body.String())
	}

	// POST 经剥离反代:方法+剥离路径+体原样(判别+流穿一体断言)。
	h.HandleAssistServiceProxy(httptest.NewRecorder(),
		webTerminalGateRequest(http.MethodPost, "/assist/service/api/dashboard/session", `{"boot":true}`, cookie))
	method, uri, body, marker := up.lastHit(t)
	if method != http.MethodPost || uri != "/api/dashboard/session" || body != `{"boot":true}` {
		t.Fatalf("写方法流穿不符: method=%s uri=%s body=%s", method, uri, body)
	}
	if marker != webTerminalLoopbackCookieMarker {
		t.Fatalf("cookie 入腿上游真源标记应为 cookie 哨兵(不得假 loopback), got %q", marker)
	}

	// PUT / DELETE 同白名单。
	h.HandleAssistServiceProxy(httptest.NewRecorder(),
		webTerminalGateRequest(http.MethodPut, "/assist/service/api/agent/mcp/servers/x", `{"command":"y"}`, cookie))
	if method, uri, _, _ = up.lastHit(t); method != http.MethodPut || uri != "/api/agent/mcp/servers/x" {
		t.Fatalf("PUT 流穿不符: method=%s uri=%s", method, uri)
	}
	h.HandleAssistServiceProxy(httptest.NewRecorder(),
		webTerminalGateRequest(http.MethodDelete, "/assist/service/api/agent/mcp/servers/x", "", cookie))
	if method, uri, _, _ = up.lastHit(t); method != http.MethodDelete || uri != "/api/agent/mcp/servers/x" {
		t.Fatalf("DELETE 流穿不符: method=%s uri=%s", method, uri)
	}

	// runtime 透传腿背书转发:core 入腿背书来的 "1" 原样到上游(模拟
	// LAN→core→runtime 全链,core 入腿已对真源置 1)。
	marked := assistServiceRequest("/assist/service/assets/styles.css", cookie)
	marked.Header.Set(WebTerminalLoopbackHeader, "1")
	h.HandleAssistServiceProxy(httptest.NewRecorder(), marked)
	if _, _, _, marker = up.lastHit(t); marker != "1" {
		t.Fatalf("core 背书来的 loopback 标记应原样转发, got %q", marker)
	}

	// ended 态写方法同吃会话闸 403(写操作只在会话期放行)。
	rc := httptest.NewRecorder()
	h.HandleAssistCancel(rc, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if rc.Code != 200 {
		t.Fatalf("撤销应 200, got %d body=%s", rc.Code, rc.Body.String())
	}
	before := len(up.requests())
	rec := httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, webTerminalGateRequest(http.MethodPost, "/assist/service/api/dashboard/session", `{}`, cookie))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not active") {
		t.Fatalf("ended 态 POST 应 403 not active, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(up.requests()) != before {
		t.Fatalf("ended 闸挡住时上游不得新增请求, got %v", up.requests())
	}
}

func TestMarkWebTerminalLoopbackSourceCookieSentinel(t *testing.T) {
	// 评审 Fix 2 的闸根(core 腿形态):显式 cookie 哨兵=代理透传腿按入腿
	// 真源的背书,mark 采信跳过重标——否则上游本跳 RemoteAddr 恒拨号源,
	// 无条件重标必把 LAN cookie 真源洗成假 loopback。伪造纪律不变:伪 "1"
	// 照旧 Del-first;无头 loopback 来源照常置 1。
	sentinel := webTerminalGateRequest(http.MethodGet, "/x", "", nil)
	sentinel.Header.Set(WebTerminalLoopbackHeader, webTerminalLoopbackCookieMarker)
	markWebTerminalLoopbackSource(sentinel)
	if got := sentinel.Header.Get(WebTerminalLoopbackHeader); got != webTerminalLoopbackCookieMarker {
		t.Fatalf("cookie 哨兵应被 mark 采信保留(跳过重标), got %q", got)
	}

	forced := webTerminalGateRequest(http.MethodGet, "/x", "", nil)
	forced.Header.Set(WebTerminalLoopbackHeader, "1") // LAN 伪造 loopback
	markWebTerminalLoopbackSource(forced)
	if got := forced.Header.Get(WebTerminalLoopbackHeader); got != "" {
		t.Fatalf("伪 loopback 标记纪律不变(必须剥净), got %q", got)
	}

	loop := webTerminalGateRequest(http.MethodGet, "/x", "", nil)
	loop.RemoteAddr = "127.0.0.1:55555"
	markWebTerminalLoopbackSource(loop)
	if got := loop.Header.Get(WebTerminalLoopbackHeader); got != "1" {
		t.Fatalf("loopback 来源应照常置 1, got %q", got)
	}
}

func TestWebTerminalResetServiceGateDefeatsProxyJumpShape(t *testing.T) {
	// P2 评审 Critical 的永久回归钉(评审 scratch probe 落地):同构代理路由的
	// 自拨腿把 runtime 侧 RemoteAddr 恒呈现为 loopback,LAN cookie 持有者经
	// /assist/service/api/agent/web-terminal {"action":"reset"}(cookie 真源
	// 哨兵背书)即以「loopback 形」抵达服务层。reset 的权威闸必须在服务层按
	// 校验后的 loopback 布尔执法——handler 预检只看 RemoteAddr,被自拨腿穿透
	// (killall/lock 本就有服务级闸,reset 此前缺兜底=清库+重设攻击者 PIN)。
	// 三臂钉全判别语义:
	//   代理形(cookie 哨兵)→ 拒,库不清——本 Critical 的复现臂;
	//   直呼无头           → 拒(Finding B 保守口径,与 killall 无头空令牌
	//                        同语义;RemoteAddr 不再决定语义);
	//   core 置 1 透传形   → 放行清库——闸只对代理形拒绝,不一刀切,合法
	//                        本机 reset 链路(core 见证置 1)不受影响。
	// 夹具:独立 AgentService 实例(同 newAssistSessionHandler 隔离纪律,
	// vault 落临时目录),不碰 shared 单例;t.Cleanup 归位(gates 关+清库),
	// 不毒化包内其他 vault 用例。
	h, _ := newAssistSessionHandler(t)
	t.Cleanup(func() {
		disable := false
		h.service.WebTerminalRunCommand("gates", "", "", "", &disable, "", true)
		h.service.WebTerminalRunCommand("reset", "", "", "", nil, "", true)
	})

	// 开闸+置 PIN(gates 门语义:不要求令牌/loopback;setup 走 loopback 直呼)。
	enable := true
	h.service.WebTerminalRunCommand("gates", "", "", "", &enable, "", true)
	h.service.WebTerminalRunCommand("setup", "1234", "", "", nil, "", true)
	if set, _ := h.service.WebTerminalStatus()["pin_set"].(bool); !set {
		t.Fatalf("前置:PIN 库应已置好")
	}

	// 三臂驱动:请求恒取 loopback RemoteAddr(自拨腿形),唯一变量=真源标记头。
	runReset := func(marker string) (bool, string) {
		req := webTerminalGateRequest(http.MethodPost, "/api/agent/web-terminal", `{"action":"reset"}`, nil)
		req.RemoteAddr = "127.0.0.1:55555" // 代理自拨腿呈现的 loopback 形
		if marker != "" {
			req.Header.Set(WebTerminalLoopbackHeader, marker)
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

	// 臂①:代理跳形(cookie 哨兵背书)→ 服务层拒,库不清(Critical 复现)。
	if ok, reason := runReset(webTerminalLoopbackCookieMarker); ok || reason != "loopback_only" {
		t.Fatalf("代理形 reset 必须被服务层拒(loopback_only), got ok=%v reason=%q", ok, reason)
	}
	if set, _ := h.service.WebTerminalStatus()["pin_set"].(bool); !set {
		t.Fatalf("被拒的 reset 不得清库: pin_set 应仍为 true")
	}

	// 臂②:直呼 runtime 无头 → 保守按 cookie 拒(RemoteAddr 不再决定语义)。
	if ok, reason := runReset(""); ok || reason != "loopback_only" {
		t.Fatalf("无头直呼 reset 应拒(loopback_only), got ok=%v reason=%q", ok, reason)
	}
	if set, _ := h.service.WebTerminalStatus()["pin_set"].(bool); !set {
		t.Fatalf("被拒的 reset 不得清库: pin_set 应仍为 true")
	}

	// 臂③:core 置 1 的合法透传形 → 放行清库(闸判别的是代理形,不一刀切)。
	if ok, reason := runReset("1"); !ok || reason != "" {
		t.Fatalf("合法 loopback 链路 reset 应放行, got ok=%v reason=%q", ok, reason)
	}
	if set, _ := h.service.WebTerminalStatus()["pin_set"].(bool); set {
		t.Fatalf("放行的 reset 应已清库: pin_set 应为 false")
	}
}

func TestHandleAssistServiceFrameHeaderStrip(t *testing.T) {
	// 剥头断言(spec §8.3):X-Frame-Options 全剥;CSP 仅摘 frame-ancestors
	// 指令、其余指令逐字透传;第二条无 frame-ancestors 的 CSP 原样保留;
	// CSP-Report-Only 不拦嵌入,原样透传不碰。
	h, _ := newAssistSessionHandler(t)
	header := make(http.Header)
	header.Set("X-Frame-Options", "DENY")
	header.Set("Content-Security-Policy", "frame-ancestors 'self'; default-src 'self'")
	header.Add("Content-Security-Policy", "default-src 'none'")
	header.Set("Content-Security-Policy-Report-Only", "frame-ancestors 'none'")
	header.Set("Content-Type", "text/css")
	up := newAssistServiceUpstream(t, header, ".a{}")
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)

	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"x"}`, cookie))
	if r1.Code != 200 {
		t.Fatalf("申请应 200, got %d", r1.Code)
	}
	rec := httptest.NewRecorder()
	h.HandleAssistServiceProxy(rec, assistServiceRequest("/assist/service/assets/styles.css", cookie))
	if rec.Code != http.StatusOK {
		t.Fatalf("放行应 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options 应剥净, got %q", got)
	}
	csp := rec.Header().Values("Content-Security-Policy")
	if len(csp) != 2 {
		t.Fatalf("CSP 应余两条, got %v", csp)
	}
	if csp[0] != "default-src 'self'" {
		t.Fatalf("CSP#1 应只摘 frame-ancestors 保 default-src, got %q", csp[0])
	}
	if csp[1] != "default-src 'none'" {
		t.Fatalf("CSP#2 无 frame-ancestors 应原样透传, got %q", csp[1])
	}
	if got := rec.Header().Get("Content-Security-Policy-Report-Only"); got != "frame-ancestors 'none'" {
		t.Fatalf("CSP-Report-Only 应原样透传不碰, got %q", got)
	}
	if got := up.requests(); len(got) != 1 || got[0] != "/assets/styles.css" {
		t.Fatalf("上游应恰收到剥离后路径, got %v", got)
	}
}

func TestHandleAssistServiceFrameOnlyCSPDropped(t *testing.T) {
	// 纯 frame-ancestors 的 CSP 摘空 → 整头删除(空值 CSP 是畸形头)。
	header := make(http.Header)
	header.Set("Content-Security-Policy", "frame-ancestors 'self'")
	stripAssistFrameHeaders(header)
	if got := header.Values("Content-Security-Policy"); len(got) != 0 {
		t.Fatalf("摘空的 CSP 应整头删除, got %v", got)
	}
	// 指令名大小写不敏感;无 CSP 头时剥除是 no-op 不炸。
	header = make(http.Header)
	header.Set("Content-Security-Policy", "FRAME-ANCESTORS 'self'; img-src 'self'")
	stripAssistFrameHeaders(header)
	if got := header.Values("Content-Security-Policy"); len(got) != 1 || got[0] != "img-src 'self'" {
		t.Fatalf("大小写不敏感摘除失败, got %v", got)
	}
	empty := make(http.Header)
	stripAssistFrameHeaders(empty) // no-op,不 panic 即过
}

func TestAssistServiceStrippedPath(t *testing.T) {
	// 剥离函数表测(判别测试的单元臂):前缀根两形态归一 /、嵌套路径剥离、
	// 剥离后仍带前缀的形态原样返回交由回环守卫 400、非前缀 false。
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"/assist/service", "/", true},
		{"/assist/service/", "/", true},
		{"/assist/service/assets/styles.css", "/assets/styles.css", true},
		{"/assist/service/api/agent/assist/session", "/api/agent/assist/session", true},
		{"/assist/service/assist/service/x", "/assist/service/x", true}, // 回环守卫在 handler 层 400
		{"/assets/styles.css", "", false},
		{"/assist/serviceother/x", "", false}, // 子树注册不匹配的相邻字面量
	}
	for _, tc := range cases {
		got, ok := assistServiceStrippedPath(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("assistServiceStrippedPath(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
