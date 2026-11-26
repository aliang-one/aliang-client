package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// 远程协助 P1 Task 3:gates 端点(GET 读 / POST 写总开关)。service 行为
// (零值纪律/持久化往返/关闸联动)由 services 层用例覆盖;这里钉端点契约:
// GET 回 {enabled},POST {enabled:bool} 翻转并回显新值,重启重载仍持久,
// 以及 400(缺键/坏体)/405 关卡。
//
// 隔离照 routes 包 runtime 角色用例惯例:t.Setenv(HOME)=TempDir +
// AgentRuntimeEnv=1——user-agent 分支的 agentStatePath 走 UserStateDir
// ($HOME/.aliang),不重定向 HOME 会写到开发者真实 ~/.aliang;且读写两侧
// (POST 落盘与重建重载)必须同角色,双分支路径不同(core=cache 目录,
// runtime=$HOME/.aliang),构造序保持 env 恒定。

func newAssistGatesHandler(t *testing.T) *AgentHandler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Setenv(services.AgentRuntimeEnv, "1")
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	service := services.NewAgentService()
	return NewAgentHandler(service)
}

// refusingRoundTripper 透传腿拒绝拨号的测试注入(评审 Fix 1 重做):注入
// handler.client 后,proxyIfNeeded 的 core 拨号确定性失败 → 500 信封
// (code=201 User agent process is not running)。动机见两个注入点的用例
// 注记(防拨到本机真实 runtime;env pin 在 package-init 后不可用)。
type refusingRoundTripper struct{}

func (refusingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("user agent dial refused by test")
}

func assistGatesBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Code != 0 {
		t.Fatalf("response code = %d, want 0 (body: %s)", resp.Code, rec.Body.String())
	}
	return resp.Data
}

func TestHandleAssistGatesToggle(t *testing.T) {
	h := newAssistGatesHandler(t)

	// GET 初始 false(零值纪律)。
	req := httptest.NewRequest("GET", "/api/agent/assist/gates", nil)
	rec := httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 200 {
		t.Fatalf("initial GET code = %d, want 200", rec.Code)
	}
	data := assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); enabled {
		t.Fatalf("initial GET enabled = %v, want false", data["enabled"])
	}

	// POST {enabled:true} → 200 回显 true。
	req = httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	data = assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("POST echo enabled = %v, want true", data["enabled"])
	}

	// GET 再读 → true。
	req = httptest.NewRequest("GET", "/api/agent/assist/gates", nil)
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	data = assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("GET after POST enabled = %v, want true", data["enabled"])
	}

	// 重启夹具:同角色同 HOME 上重建 service(runtime 分支同一路径),
	// GET 仍 true(持久化)。
	reloaded := services.NewAgentService()
	h2 := NewAgentHandler(reloaded)
	req = httptest.NewRequest("GET", "/api/agent/assist/gates", nil)
	rec = httptest.NewRecorder()
	h2.HandleAssistGates(rec, req)
	data = assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("GET after reload enabled = %v, want true (persistence)", data["enabled"])
	}
}

func TestHandleAssistGatesValidation(t *testing.T) {
	h := newAssistGatesHandler(t)

	// 空体 → 400。
	req := httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("empty body code = %d, want 400", rec.Code)
	}

	// 缺 enabled 键 → 400(契约:显式布尔键)。
	req = httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("missing key code = %d, want 400", rec.Code)
	}

	// 未知形状 → 400。
	req = httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(`{"on":true}`))
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("unknown shape code = %d, want 400", rec.Code)
	}

	// 非 GET/POST → 405(同 HandlePeerGates 的 http.Error 惯例)。
	req = httptest.NewRequest("DELETE", "/api/agent/assist/gates", nil)
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 405 {
		t.Fatalf("DELETE code = %d, want 405", rec.Code)
	}
}

// ——— Task 6:客户端会话面四端点(request/session/end/cancel)———
// 门矩阵(plan Task 6,照 web-terminal 双闸测试矩阵形态):
//   ①无 cookie 非 loopback → 401  ②loopback → 放行
//   ③同源 LAN + cookie → 放行     ④跨站 Origin → 403(同源闸,仅 core 腿)
//   ⑤总开关关 → 403(runtime 腿 runtime 门;gates 端点刻意除外)
// core 腿真源标记 + PROXY-FIRST(零 body 解析)由 marking 用例钉住(断言
// 头部突变,与响应码无关)。runtime 腿行为(状态机映射/审计/信令)用真身
// service + AssistSetFrameSink 缝驱动。

// assistEnvelope 解错误信封(业务 code + msg;成功分支用 assistData)。
func assistEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return resp.Code, resp.Msg
}

// assistSessionPostHandlers POST 三端点分发表(request/end/cancel 契约体)。
func assistSessionPostHandlers(h *AgentHandler) map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"request": h.HandleAssistRequest,
		"end":     h.HandleAssistEnd,
		"cancel":  h.HandleAssistCancel,
	}
}

func TestHandleAssistClientGateMatrix(t *testing.T) {
	// core 角色(未设 runtime env):门前语义全在 proxy 之前,不触 service。
	// 透传腿拒绝拨号(评审 Fix 1 重做):向 handler 的控制面 client 注入
	// refusingRoundTripper——拨号确定性被拒,proxyIfNeeded 落 500 信封
	// (code=201 User agent process is not running)。不能省:③ 同源 LAN+cookie
	// 腿过闸后走 core proxy 真拨本机 runtime,本机若跑着带 assist 路由的
	// 新构建会真应答(403 disabled 或真身语义),「非 401/403」断言随构建
	// 版本漂移翻红。环境变量 pin 在此处不可用(DefaultUserAgentAddr 在
	// package init 时已解析,t.Setenv 晚于 init 无效),client 注入是同包
	// 可达的等价物且完全 hermetic。
	h := NewAgentHandler(nil)
	h.client = &http.Client{Timeout: 150 * time.Millisecond, Transport: refusingRoundTripper{}}
	forged := &http.Cookie{Name: middleware.DashboardSessionCookieName, Value: url.QueryEscape("forged-token")}

	// ① 无 cookie 非 loopback → 401;伪造 cookie → 401(四端点同门同治)。
	for name, fn := range assistSessionPostHandlers(h) {
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/"+name, `{"message":"x"}`, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 无登录态应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/"+name, `{"message":"x"}`, forged))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 伪造 cookie 应 401, got %d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session 无登录态应 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", forged))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session 伪造 cookie 应 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 方法闸最前(先于鉴权,同 web-terminal 约定):GET 打 POST 端点 / POST 打
	// session(GET)→ 405。
	for name, fn := range assistSessionPostHandlers(h) {
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/"+name, "", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET 打 %s 应 405, got %d", name, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/session", `{"x":1}`, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 打 session 应 405, got %d", rec.Code)
	}

	// ② loopback 豁免腿放行:非 401 即证明(过闸后 core 拨号 56433 失败=5xx
	// 信封;本机若有活 runtime 则真身应答,两种结局都不是门级)。用只读的
	// session 端点测,零副作用。
	rec = httptest.NewRecorder()
	req := webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleAssistSession(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("loopback 豁免不应 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	// ③ 同源 LAN + 有效 cookie 放行:非 401/403 即证明(同上,过闸到 proxy)。
	rec = httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", mintDashboardCookie(t)))
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("同源 LAN+cookie 不应被门拒, got %d body=%s", rec.Code, rec.Body.String())
	}

	// ④ 跨站 Origin → 403(同源闸):持有效 cookie 也拒——SameSite=Strict 下
	// 浏览器跨站带不上 cookie,此臂钉的是非浏览器伪造面(cookie+Origin 可全
	// 伪造);loopback 豁免腿同样吃同源闸(本地恶意页 CSRF 面收口)。
	rec = httptest.NewRecorder()
	req = webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", mintDashboardCookie(t))
	req.Header.Set("Origin", "http://evil.example:8080")
	h.HandleAssistSession(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin 应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Cross-origin") {
		t.Fatalf("403 语义应指向跨站: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	req.Header.Set("Origin", "http://evil.example:8080")
	h.HandleAssistSession(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback 来源的跨站 Origin 也应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAssistLoopbackMarking(t *testing.T) {
	// core 角色:四端点必须在 proxy 之前完成真源标记——LAN 伪造的
	// X-Aliang-WT-Loopback 必须剥净(否则经 proxy 透传可在 runtime 侧冒充
	// loopback),loopback 来源置 1。断言只看 handler 返回后 r.Header 的同步
	// 突变,不钉响应码(拨号被拒=500 信封,两者对头部真值均无影响)。core
	// 腿零 body 解析由本用例顺带钉住:坏 JSON 体也必须走到 marking+proxy
	// (若 core 腿抢先解析并 400,header 断言失败)。
	// 透传腿拒绝拨号(评审 Fix 1 重做,同 ClientGateMatrix 的机制):注入
	// refusingRoundTripper——loopback 豁免腿的 end/cancel 若真拨到本机活
	// runtime(新构建),可能改写真实协助会话/落真实审计行;注入后拨号确定
	// 性被拒,零副作用。环境变量 pin 不可用(DefaultUserAgentAddr package
	// init 时已解析,t.Setenv 晚于 init 无效)。
	h := NewAgentHandler(nil)
	h.client = &http.Client{Timeout: 150 * time.Millisecond, Transport: refusingRoundTripper{}}
	targets := map[string]struct {
		fn     func(http.ResponseWriter, *http.Request)
		method string
		body   string
	}{
		"request": {h.HandleAssistRequest, http.MethodPost, "garbage-not-json"},
		"session": {h.HandleAssistSession, http.MethodGet, ""},
		"end":     {h.HandleAssistEnd, http.MethodPost, ""},
		"cancel":  {h.HandleAssistCancel, http.MethodPost, ""},
	}
	for name, tg := range targets {
		req := webTerminalGateRequest(tg.method, "/api/agent/assist/"+name, tg.body, mintDashboardCookie(t))
		req.Header.Set(WebTerminalLoopbackHeader, "1") // LAN 伪造 loopback
		fn := tg.fn
		fn(httptest.NewRecorder(), req)
		if got := req.Header.Get(WebTerminalLoopbackHeader); got != "" {
			t.Fatalf("%s marking 必须剥净伪造真源头, got %q", name, got)
		}
	}
	// loopback 来源(豁免腿过闸):core 见证并置 1。
	req := webTerminalGateRequest(http.MethodPost, "/api/agent/assist/end", "", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	h.HandleAssistEnd(httptest.NewRecorder(), req)
	if got := req.Header.Get(WebTerminalLoopbackHeader); got != "1" {
		t.Fatalf("loopback 来源应被标记为 1, got %q", got)
	}
}

// ——— runtime 腿行为(真身 service + 帧缝)———

// assistFrameRecorder assist 帧缝收集器(AssistSetFrameSink 注入)。
type assistFrameRecorder struct {
	mu     sync.Mutex
	frames []map[string]interface{}
}

func (rec *assistFrameRecorder) record(frame map[string]interface{}) error {
	rec.mu.Lock()
	rec.frames = append(rec.frames, frame)
	rec.mu.Unlock()
	return nil
}

// frameTypes 收到的帧型序列(断言上行信令)。
func (rec *assistFrameRecorder) frameTypes() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	types := make([]string, 0, len(rec.frames))
	for _, f := range rec.frames {
		if v, _ := f["type"].(string); v != "" {
			types = append(types, v)
		}
	}
	return types
}

// lastFrame 最近一帧(无帧=t.Fatalf)。
func (rec *assistFrameRecorder) lastFrame(t *testing.T) map[string]interface{} {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.frames) == 0 {
		t.Fatalf("expected at least one uplink frame, got none")
	}
	return rec.frames[len(rec.frames)-1]
}

// newAssistSessionHandler runtime 角色夹具(Task 3 gates 夹具同款隔离)+帧缝:
// HOME=TempDir + AgentRuntimeEnv=1(读写两侧同角色,状态落临时目录),注入
// assistFrameSink 并在 Cleanup 恢复 nil(进程级全局缝纪律)。
func newAssistSessionHandler(t *testing.T) (*AgentHandler, *assistFrameRecorder) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Setenv(services.AgentRuntimeEnv, "1")
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	rec := &assistFrameRecorder{}
	services.AssistSetFrameSink(rec.record)
	t.Cleanup(func() { services.AssistSetFrameSink(nil) })
	service := services.NewAgentService()
	return NewAgentHandler(service), rec
}

// assistSessionData 解成功信封 data(map 形态,便于断言键存在性——snapshot
// 零值也要带全 session_id/state/message 三键,前端契约稳定)。
func assistSessionData(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Code != 0 {
		t.Fatalf("response code = %d, want 0 (body: %s)", resp.Code, rec.Body.String())
	}
	return resp.Data
}

func TestHandleAssistDisabledGate403(t *testing.T) {
	h, _ := newAssistSessionHandler(t)
	// 总开关缺省 false(零值纪律):四端点 runtime 门统一 403,gates 端点除外
	// (它是翻回开关的入口,必须常开)。
	for name, fn := range assistSessionPostHandlers(h) {
		rec := httptest.NewRecorder()
		fn(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/"+name, `{"message":"x"}`, mintDashboardCookie(t)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 开关关应 403, got %d body=%s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "disabled") {
			t.Fatalf("%s 403 语义应指向开关关: %s", name, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", mintDashboardCookie(t)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session 开关关应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAssistSessionIdleShape(t *testing.T) {
	h, _ := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	rec := httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", mintDashboardCookie(t)))
	if rec.Code != 200 {
		t.Fatalf("idle GET code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	data := assistSessionData(t, rec)
	// 稳定形状契约:idle 时 state 为空串但键恒在(session_id/message 同),
	// 前端据 state==""→idle 渲染;end_reason 等可省键 omitempty 不在此列。
	for _, key := range []string{"session_id", "state", "message"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("idle snapshot 缺键 %q (body: %s)", key, rec.Body.String())
		}
	}
	if data["state"] != "" {
		t.Fatalf("idle state = %v, want 空串(空串=idle 契约)", data["state"])
	}
}

func TestHandleAssistRequestOfflineWriterFails(t *testing.T) {
	// 信令发不出去(无缝无连接=writer nil)→ 申请必须可见地失败(plan Task 4:
	// 发不出去的申请永远不会进 admin 队列,静默挂起最伤用户)。映射:非状态
	// 机哨兵 → CodeServiceUnavailable 信封(201;HTTP 落 500,同 proxy-不可达
	// 既有惯例),且新建会话回滚到纯净 idle(不置粘性 ended/不设冷却锚)。
	h, _ := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	services.AssistSetFrameSink(nil) // 撤缝:writer 落生产默认(nil=离线)

	rec := httptest.NewRecorder()
	h.HandleAssistRequest(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"help"}`, mintDashboardCookie(t)))
	code, msg := assistEnvelope(t, rec)
	if code != common.CodeServiceUnavailable {
		t.Fatalf("离线 writer code = %d (http %d), want %d (msg=%s)", code, rec.Code, common.CodeServiceUnavailable, msg)
	}
}

func TestHandleAssistRequestAndCancelLifecycle(t *testing.T) {
	h, sink := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)

	// 申请 → 200 {session_id, state=requested},上行 assist.request。
	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"please help"}`, cookie))
	if r1.Code != 200 {
		t.Fatalf("request code = %d, want 200 (body: %s)", r1.Code, r1.Body.String())
	}
	data := assistSessionData(t, r1)
	sid, _ := data["session_id"].(string)
	if !strings.HasPrefix(sid, "assist-") {
		t.Fatalf("session_id = %v, want assist- 前缀", data["session_id"])
	}
	if data["state"] != "requested" {
		t.Fatalf("state = %v, want requested", data["state"])
	}
	frame := sink.lastFrame(t)
	if frame["type"] != "assist.request" || frame["session_id"] != sid || frame["message"] != "please help" {
		t.Fatalf("上行帧不符: %v", frame)
	}

	// session 轮询 → requested 形态(message/requested_at 可见)。
	r2 := httptest.NewRecorder()
	h.HandleAssistSession(r2, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", cookie))
	data = assistSessionData(t, r2)
	if data["state"] != "requested" || data["session_id"] != sid || data["message"] != "please help" {
		t.Fatalf("session snapshot 不符: %v", data)
	}
	if data["requested_at"] == "" {
		t.Fatalf("requested_at 应非空: %v", data)
	}

	// requested 态重复申请 = 同态更新留言,不新建(session_id 不变)。
	r3 := httptest.NewRecorder()
	h.HandleAssistRequest(r3, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"updated"}`, cookie))
	if r3.Code != 200 {
		t.Fatalf("重复 request code = %d, want 200 (body: %s)", r3.Code, r3.Body.String())
	}
	data = assistSessionData(t, r3)
	if data["session_id"] != sid {
		t.Fatalf("同态更新不得换 session_id: got %v want %v", data["session_id"], sid)
	}

	// 撤销 → 200,上行 assist.cancel;session → ended(client_end)。
	r4 := httptest.NewRecorder()
	h.HandleAssistCancel(r4, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if r4.Code != 200 {
		t.Fatalf("cancel code = %d, want 200 (body: %s)", r4.Code, r4.Body.String())
	}
	frame = sink.lastFrame(t)
	if frame["type"] != "assist.cancel" || frame["session_id"] != sid {
		t.Fatalf("cancel 上行帧不符: %v", frame)
	}
	r5 := httptest.NewRecorder()
	h.HandleAssistSession(r5, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", cookie))
	data = assistSessionData(t, r5)
	if data["state"] != "ended" || data["end_reason"] != "client_end" || data["session_id"] != sid {
		t.Fatalf("ended snapshot 不符: %v", data)
	}

	// 撤销后再撤销 → 409(无待审批申请);结束后再 end → 409(sticky ended
	// 态按「无会话可结束」处置,不得空转上行一帧空 sid 的 cancel)。
	before := len(sink.frameTypes())
	r6 := httptest.NewRecorder()
	h.HandleAssistCancel(r6, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if code, _ := assistEnvelope(t, r6); code != common.CodeConflict || r6.Code != 409 {
		t.Fatalf("二次 cancel 应 409/CodeConflict, got http=%d code=%d", r6.Code, code)
	}
	r7 := httptest.NewRecorder()
	h.HandleAssistEnd(r7, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/end", "", cookie))
	if code, _ := assistEnvelope(t, r7); code != common.CodeConflict || r7.Code != 409 {
		t.Fatalf("ended 态 end 应 409/CodeConflict, got http=%d code=%d", r7.Code, code)
	}
	if after := len(sink.frameTypes()); after != before {
		t.Fatalf("409 路径不得上行信令帧: before=%d after=%d", before, after)
	}
}

func TestHandleAssistEndFromRequested(t *testing.T) {
	// end 不止 active 态可用:requested(等待审批)同样「有会话可结束」——
	// 上行帧型按附录 A #4 恒 assist.cancel(客户端发起收口)。
	h, sink := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)
	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"x"}`, cookie))
	if r1.Code != 200 {
		t.Fatalf("request code = %d, want 200", r1.Code)
	}
	sid, _ := assistSessionData(t, r1)["session_id"].(string)

	r2 := httptest.NewRecorder()
	h.HandleAssistEnd(r2, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/end", "", cookie))
	if r2.Code != 200 {
		t.Fatalf("end from requested code = %d, want 200 (body: %s)", r2.Code, r2.Body.String())
	}
	frame := sink.lastFrame(t)
	if frame["type"] != "assist.cancel" || frame["session_id"] != sid {
		t.Fatalf("end 上行帧应为 assist.cancel 且带 sid: %v", frame)
	}
	data := assistSessionData(t, r2)
	if data["state"] != "ended" || data["end_reason"] != "client_end" {
		t.Fatalf("end snapshot 不符: %v", data)
	}
	// assist_end 审计臂(第三条写路径;request/cancel 两臂在
	// TestHandleAssistAuditLanding)。
	text := assistAuditFile(t)
	if strings.Count(text, `"tool":"assist.end"`) != 1 {
		t.Fatalf("assist.end 审计行应恰一条: %s", text)
	}
	if !strings.Contains(text, "session="+sid) || !strings.Contains(text, "reason=client_end") {
		t.Fatalf("assist.end 审计摘要缺 sid/reason: %s", text)
	}
}

func TestHandleAssistRequestCooldown429(t *testing.T) {
	// 撤销终态落 60s 冷却锚:立即重申 → 429(CodeTooManyRequests=105,新增
	// 码位——429 语义是 plan Task 6 明文,前端凭 code=105 区分冷却)。
	h, _ := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)

	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"x"}`, cookie))
	if r1.Code != 200 {
		t.Fatalf("request code = %d, want 200", r1.Code)
	}
	r2 := httptest.NewRecorder()
	h.HandleAssistCancel(r2, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if r2.Code != 200 {
		t.Fatalf("cancel code = %d, want 200", r2.Code)
	}
	r3 := httptest.NewRecorder()
	h.HandleAssistRequest(r3, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"again"}`, cookie))
	if r3.Code != http.StatusTooManyRequests {
		t.Fatalf("冷却重申 http = %d, want 429 (body: %s)", r3.Code, r3.Body.String())
	}
	if code, _ := assistEnvelope(t, r3); code != common.CodeTooManyRequests {
		t.Fatalf("冷却重申 code = %d, want %d", code, common.CodeTooManyRequests)
	}
}

func TestHandleAssistRequestBodyValidation(t *testing.T) {
	h, _ := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)

	// 坏 JSON → 400。
	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", "not-json", cookie))
	if r1.Code != 400 {
		t.Fatalf("坏 JSON code = %d, want 400", r1.Code)
	}
	// >4KiB → 400(限额显式拒,不静默截断)。
	big := `{"message":"` + strings.Repeat("a", 5000) + `"}`
	r2 := httptest.NewRecorder()
	h.HandleAssistRequest(r2, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", big, cookie))
	if r2.Code != 400 {
		t.Fatalf("超 4KiB code = %d, want 400", r2.Code)
	}
	// 空体合法({message?} 可缺省):创建申请(缝在,信令成功)→ 200。
	r3 := httptest.NewRecorder()
	h.HandleAssistRequest(r3, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", "", cookie))
	if r3.Code != 200 {
		t.Fatalf("空体 code = %d, want 200 (body: %s)", r3.Code, r3.Body.String())
	}
}

func TestWriteAssistServiceErrorMapping(t *testing.T) {
	// 映射表直驱:ErrAssistBusy(409)在 handler 端到端里不可达(active 态需
	// 云端 approved 帧,无导出缝,刻意不加纯测试面),经本用例钉映射;其余
	// 哨兵在生命周期用例里已端到端覆盖,此处作回归锚。
	cases := []struct {
		err      error
		httpCode int
		bizCode  int
	}{
		{services.ErrAssistBusy, 409, common.CodeConflict},
		{services.ErrAssistNotPending, 409, common.CodeConflict},
		{services.ErrAssistNothingToEnd, 409, common.CodeConflict},
		{services.ErrAssistCooldown, 429, common.CodeTooManyRequests},
		{errors.New("agent assist: remote writer offline"), 500, common.CodeServiceUnavailable},
	}
	for i, tc := range cases {
		rec := httptest.NewRecorder()
		writeAssistServiceError(rec, tc.err)
		if rec.Code != tc.httpCode {
			t.Fatalf("case %d: http = %d, want %d (body=%s)", i, rec.Code, tc.httpCode, rec.Body.String())
		}
		if code, _ := assistEnvelope(t, rec); code != tc.bizCode {
			t.Fatalf("case %d: biz code = %d, want %d", i, code, tc.bizCode)
		}
	}
}

// assistAuditFile 读本夹具 HOME 下的协助审计文件(HOME=TempDir 隔离;文件
// 缺失/不可读=t.Fatalf——落盘语义本身是被测契约)。
func assistAuditFile(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".aliang", "ai-sessions", "remote-assist.jsonl"))
	if err != nil {
		t.Fatalf("读审计文件: %v", err)
	}
	return string(raw)
}

func TestHandleAssistAuditLanding(t *testing.T) {
	// 审计落点:assist_request/assist_cancel 在 runtime 腿成功后落 journal
	// (remote-assist.jsonl,幽灵防御约定:只有 kind=tool 行,不冒会话卡);
	// origin 随真源标记头(cookie——夹具请求来源是 LAN 形态);session 轮询
	// 刻意不审(噪声)。
	h, _ := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	cookie := mintDashboardCookie(t)

	r1 := httptest.NewRecorder()
	h.HandleAssistRequest(r1, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"please help"}`, cookie))
	if r1.Code != 200 {
		t.Fatalf("request code = %d, want 200 (body: %s)", r1.Code, r1.Body.String())
	}
	sid, _ := assistSessionData(t, r1)["session_id"].(string)
	r2 := httptest.NewRecorder()
	h.HandleAssistCancel(r2, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if r2.Code != 200 {
		t.Fatalf("cancel code = %d, want 200 (body: %s)", r2.Code, r2.Body.String())
	}
	// session 轮询两发:不得产生审计行。
	for i := 0; i < 2; i++ {
		r := httptest.NewRecorder()
		h.HandleAssistSession(r, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", cookie))
		if r.Code != 200 {
			t.Fatalf("session code = %d, want 200", r.Code)
		}
	}

	text := assistAuditFile(t)
	if strings.Count(text, `"tool":"assist.request"`) != 1 || strings.Count(text, `"tool":"assist.cancel"`) != 1 {
		t.Fatalf("审计行数量不符(request/cancel 各一): %s", text)
	}
	if strings.Contains(text, "assist.session") {
		t.Fatalf("session 轮询不得产生审计行: %s", text)
	}
	for _, needle := range []string{"session=" + sid, "origin=cookie", "msg_len=11"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("审计摘要缺 %q: %s", needle, text)
		}
	}
}
