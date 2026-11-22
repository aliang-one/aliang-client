package services

// agent_listener_api_test.go 监听器本地 REST 代理 + cloud 客户端缝单测
// (specs/005-event-listener §2/FR-4/FR-11,plan Task 11):httptest 假 cloud
// (按 PhoneServer listeners.ts 真实 wire 形态应答)+ 直接挂 handler 的
// 方法前缀 mux(routes.go 全量 harness 会拉起整套服务单例,过重——本文件
// 用镜像注册块的方式锁行为,注册存在性另由 TestListenerProxyRoutesWired
// 源码扫描钉死,同 cmd/boot_wiring_audit 先例)。
//
// 缝纪律(同 Task 7-10):测试串行,禁 t.Parallel;swap 期间不得有在跑
// watcher;Cleanup 还原全部 seam。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/common/cache"
)

// --- 测试脚手架 -----------------------------------------------------------

// swapListenerSharedService 换全局 AgentService 单例并登记还原(boot 测试
// 同款手法;身份材料经 forwardedUserAuthorization 注入,确定性可断言)。
func swapListenerSharedService(t *testing.T, svc *AgentService) {
	t.Helper()
	sharedAgentServiceMu.Lock()
	original := sharedAgentService
	sharedAgentService = svc
	sharedAgentServiceMu.Unlock()
	t.Cleanup(func() {
		sharedAgentServiceMu.Lock()
		sharedAgentService = original
		sharedAgentServiceMu.Unlock()
	})
}

// newListenerProxyTestService 造已注册(或未注册)形态的裸服务:client 必备
// (请求走 svc.client.Do),身份字段经 remoteConnectionSnapshot 读。
func newListenerProxyTestService(registered bool) *AgentService {
	svc := &AgentService{client: &http.Client{Timeout: agentHTTPTimeout}}
	if registered {
		svc.mu.Lock()
		svc.state.Enabled = true
		svc.state.Registered = true
		svc.state.DeviceID = "dev-proxy-1"
		svc.storeForwardedCredential("Bearer proxy-jwt")
		svc.mu.Unlock()
	}
	return svc
}

// newListenerCoreTestService core 形态测试服务(真实 core 注册状态镜像):
// 有本机设备 id(state.DeviceID),无云端授权(forwarded 凭据空——数据目录
// 分离恒空)→ 身份快照不满足但 device_id 覆盖仍可本地完成。
func newListenerCoreTestService() *AgentService {
	svc := &AgentService{client: &http.Client{Timeout: agentHTTPTimeout}}
	svc.mu.Lock()
	// Enabled/Registered 刻意保持 false:身份快照(含 auth 库回读)确定性
	// 失败、与运行环境无关——全量包跑时更早用例可能写入真实 auth 库
	// (2026-11-22 实证),快照成败不得依赖环境。DeviceID 保留=真实 core
	// 的「设备注册在 core、授权在 agent」拆分镜像。
	svc.state.DeviceID = "dev-proxy-1"
	svc.mu.Unlock()
	return svc
}

// swapListenerCloudBase 把 base 缝指到指定 URL 并登记还原。
func swapListenerCloudBase(t *testing.T, baseURL string) {
	t.Helper()
	old := listenerCloudBaseURL
	listenerCloudBaseURL = func() (string, error) { return baseURL, nil }
	t.Cleanup(func() { listenerCloudBaseURL = old })
}

// withListenerDashboardSessionCookie 签发一枚真 dashboard 会话 cookie:
// RequireDashboardSession 是本代理的本地鉴权闸(同 quick-setup 家族约定),
// 测试须经正规签发路径拿 cookie,不得绕过校验。持久化隔离到临时
// ALIANG_CACHE_DIR(签发会 best-effort 落盘,不碰真实 ~/.aliang)。
func withListenerDashboardSessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	t.Setenv(cache.CacheDirEnvVar, t.TempDir())
	cache.ResetCacheDirForTest()
	middleware.ResetDashboardSessionForTest()
	t.Cleanup(func() {
		middleware.ResetDashboardSessionForTest()
		cache.ResetCacheDirForTest()
	})

	rec := httptest.NewRecorder()
	issue := httptest.NewRequest(http.MethodGet, "/api/session/events", nil)
	issue.RemoteAddr = "127.0.0.1:60000" // loopback 签发闸(isLoopbackRequest)
	if err := middleware.IssueDashboardSession(rec, issue); err != nil {
		t.Fatalf("IssueDashboardSession: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == middleware.DashboardSessionCookieName {
			return c
		}
	}
	t.Fatal("签发响应未携带 dashboard 会话 cookie")
	return nil
}

// newListenerProxyMux 镜像 routes.go 的注册块(方法前缀模式,同 quick-setup
// combos 约定);routes.go 侧真注册由 TestListenerProxyRoutesWired 锁。
func newListenerProxyMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/listeners", HandleListenerList)
	mux.HandleFunc("POST /api/listeners", HandleListenerCreate)
	mux.HandleFunc("PATCH /api/listeners/{id}", HandleListenerPatch)
	mux.HandleFunc("DELETE /api/listeners/{id}", HandleListenerDelete)
	mux.HandleFunc("GET /api/listeners/{id}/events", HandleListenerEvents)
	return mux
}

// listenerCloudCapture 假 cloud 的请求捕获 + 可配置应答(server 持有
// httptest 实例供用例自行 Close 制造 connection-refused)。
type listenerCloudCapture struct {
	mu      sync.Mutex
	calls   []listenerCloudCall
	handler func(w http.ResponseWriter, r *http.Request, body []byte)
	server  *httptest.Server
}

type listenerCloudCall struct {
	Method string
	Path   string
	Auth   string
	Device string
	Body   []byte
}

func (c *listenerCloudCapture) record(r *http.Request, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, listenerCloudCall{
		Method: r.Method,
		Path:   r.URL.Path,
		Auth:   r.Header.Get("Authorization"),
		Device: r.Header.Get("X-Aliang-Device-ID"),
		Body:   body,
	})
}

func (c *listenerCloudCapture) snapshot() []listenerCloudCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]listenerCloudCall(nil), c.calls...)
}

// newListenerFakeCloud 起假 cloud:handler 为空时按 PhoneServer 真实形态默认
// 应答(GET /api/listeners → {listeners:[]};POST → 201 {listener:{}};其余 200)。
func newListenerFakeCloud(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) *listenerCloudCapture {
	t.Helper()
	cap := &listenerCloudCapture{handler: handler}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		cap.record(r, body)
		if handler != nil {
			handler(w, r, body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/listeners":
			_, _ = w.Write([]byte(`{"listeners":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/listeners":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"listener":{"id":"L-fake","status":"active"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/listeners":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"listener":{"id":"L-agent","status":"active"}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	// base 缝在测试体内另行 swap(有的用例要换错误 base),此处只回捕获器。
	cap.server = server
	return cap
}

// setupListenerProxyHarness 一把装好:env=user-agent runtime(身份确定性)、
// 已注册服务、base 指假 cloud、返回带 mux 的测试服务器与会话 cookie。
func setupListenerProxyHarness(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, *listenerCloudCapture, *http.Cookie) {
	t.Helper()
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(true))
	cap := newListenerFakeCloud(t, handler)
	swapListenerCloudBase(t, cap.server.URL)
	srv := httptest.NewServer(newListenerProxyMux())
	t.Cleanup(srv.Close)
	return srv, cap, withListenerDashboardSessionCookie(t)
}

// doListenerProxy 发带会话 cookie 的请求并回 (status, body)。
func doListenerProxy(t *testing.T, srv *httptest.Server, cookie *http.Cookie, method, path string, body []byte) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw
}

// --- 1. GET 透传:鉴权闸 + 云端 header 契约 + 信封化 body(C1) ----------------

// listenerEnvelopeBody 信封解码形状(C1):与网页 agentApi.js extractEnvelope
// 对齐。Code 刻意用 interface{} 解码——JSON number 解出 float64,字符串/缺省
// 解出 string/nil,据此锁死「code 须是 JSON number」(extractEnvelope 把非
// 数字 code 归 0,字符串 code 会假绿混过成功闸)。
type listenerEnvelopeBody struct {
	Code interface{}     `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// decodeListenerEnvelope 解本地代理应答信封:code 须是 JSON number(值由
// 调用方断言),msg 须是 string;返回原始 data 供逐项比对。
func decodeListenerEnvelope(t *testing.T, body []byte) listenerEnvelopeBody {
	t.Helper()
	var env listenerEnvelopeBody
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("本地应答须是信封 JSON: %s (%v)", body, err)
	}
	if _, ok := env.Code.(float64); !ok {
		t.Fatalf("信封 code 须是 JSON number(extractEnvelope 只认数字): %s", body)
	}
	return env
}

// decodeListenerJSON 把 RawMessage 解成通用对象(缺键/空体归 nil)。
func decodeListenerJSON(t *testing.T, raw json.RawMessage) interface{} {
	t.Helper()
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("信封 data 解析失败: %s (%v)", raw, err)
	}
	return parsed
}

func TestListenerProxyGetPassthrough(t *testing.T) {
	cloudBody := []byte(`{"listeners":[{"id":"L-1","title":"盯构建","status":"active"}]}`)
	srv, cap, cookie := setupListenerProxyHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cloudBody)
	})

	// 无会话 cookie:本地鉴权闸先拦(401),不打云端。
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/listeners", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no-cookie GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 dashboard 会话须 401,得到 %d", resp.StatusCode)
	}
	if calls := cap.snapshot(); len(calls) != 0 {
		t.Fatalf("401 路径不得触达云端,已触达: %+v", calls)
	}

	// 带会话:云端收到 Authorization + X-Aliang-Device-ID(与远端 WS 握手同源),
	// 本地应答=信封 {code:0,msg,data},data 与云端对象逐项一致(C1)。
	status, localBody := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/listeners status = %d", status)
	}
	env := decodeListenerEnvelope(t, localBody)
	if env.Code != float64(0) {
		t.Fatalf("2xx 信封 code 须为 number 0: %s", localBody)
	}
	if !reflect.DeepEqual(decodeListenerJSON(t, env.Data), decodeListenerJSON(t, cloudBody)) {
		t.Fatalf("信封 data 须逐项等于云端对象:\n got %s\nwant %s", env.Data, cloudBody)
	}
	calls := cap.snapshot()
	if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/api/listeners" {
		t.Fatalf("云端收到调用不符: %+v", calls)
	}
	if calls[0].Auth != "Bearer proxy-jwt" || calls[0].Device != "dev-proxy-1" {
		t.Fatalf("云端 header 契约破损: Authorization=%q Device=%q", calls[0].Auth, calls[0].Device)
	}
}

// --- 2. POST device_id 覆盖:body 伪造外机 id → 云端收到本机 id -------------

func TestListenerProxyCreateOverridesDeviceID(t *testing.T) {
	srv, cap, cookie := setupListenerProxyHarness(t, nil)

	forged := []byte(`{"title":"盯 build.log","source":{"type":"file_tail","device_id":"dev-foreign","params":{"path":"/tmp/a.log","match":"ERROR"}},"prompt_template":"命中:{{event}}","mode":"recurring","target_session_id":"sess-1"}`)
	status, body := doListenerProxy(t, srv, cookie, http.MethodPost, "/api/listeners", forged)
	if status != http.StatusOK {
		t.Fatalf("POST /api/listeners status = %d(云端 201 经 common.Success 拍平为 200),body=%s", status, body)
	}
	env := decodeListenerEnvelope(t, body)
	if env.Code != float64(0) {
		t.Fatalf("2xx 信封 code 须为 number 0: %s", body)
	}
	var respPayload struct {
		Listener struct {
			ID string `json:"id"`
		} `json:"listener"`
	}
	if err := json.Unmarshal(env.Data, &respPayload); err != nil || respPayload.Listener.ID != "L-fake" {
		t.Fatalf("信封 data.listener 须含云端回传对象: %s (%v)", env.Data, err)
	}

	calls := cap.snapshot()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/api/listeners" {
		t.Fatalf("云端收到调用不符: %+v", calls)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("转发 body 须是 JSON: %v", err)
	}
	source, ok := sent["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("转发 body 缺 source 对象: %s", calls[0].Body)
	}
	if source["device_id"] != "dev-proxy-1" {
		t.Fatalf("source.device_id 须被强制覆盖为本机 dev-proxy-1,得到 %v", source["device_id"])
	}
	// 覆盖只动 device_id,其余字段原样保留。
	if sent["title"] != "盯 build.log" || sent["mode"] != "recurring" || sent["target_session_id"] != "sess-1" {
		t.Fatalf("覆盖不得动其余字段: %s", calls[0].Body)
	}
	params, _ := source["params"].(map[string]interface{})
	if params["path"] != "/tmp/a.log" || params["match"] != "ERROR" {
		t.Fatalf("source.params 须原样保留: %s", calls[0].Body)
	}
}

// --- 3. PATCH/DELETE/events 透传(method+path+body) ------------------------

func TestListenerProxyPatchDeleteEventsPassthrough(t *testing.T) {
	srv, cap, cookie := setupListenerProxyHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"listener":{"id":"L-9","status":"paused"}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			_, _ = w.Write([]byte(`{"events":[{"listener_id":"L-9","summary":"s"}]}`))
		default:
			http.NotFound(w, r)
		}
	})

	// PATCH:转发 body 原样(method+path+body 三要素在云端逐项可断言),
	// 应答信封化——data.listener.status=paused。
	patchBody := []byte(`{"title":"新标题","updated_at":"2026-11-21T00:00:00Z"}`)
	status, body := doListenerProxy(t, srv, cookie, http.MethodPatch, "/api/listeners/L-9", patchBody)
	env := decodeListenerEnvelope(t, body)
	if status != http.StatusOK || env.Code != float64(0) || !strings.Contains(string(env.Data), `"paused"`) {
		t.Fatalf("PATCH status=%d body=%s", status, body)
	}
	// DELETE:云端 {} → 信封 data 亦为空对象(C1 契约:{} for delete,
	// listenerDelete 直通不消费),code 0。
	status, body = doListenerProxy(t, srv, cookie, http.MethodDelete, "/api/listeners/L-9", nil)
	env = decodeListenerEnvelope(t, body)
	dataTrimmed := strings.TrimSpace(string(env.Data))
	if status != http.StatusOK || env.Code != float64(0) || (dataTrimmed != "{}" && dataTrimmed != "" && dataTrimmed != "null") {
		t.Fatalf("DELETE status=%d body=%s(云端空对象经信封化后 data 须为 {}/空)", status, body)
	}
	// events。
	status, body = doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners/L-9/events", nil)
	env = decodeListenerEnvelope(t, body)
	if status != http.StatusOK || env.Code != float64(0) || !strings.Contains(string(env.Data), `"listener_id":"L-9"`) {
		t.Fatalf("events status=%d body=%s", status, body)
	}

	calls := cap.snapshot()
	if len(calls) != 3 {
		t.Fatalf("云端须收到 3 笔调用,得到 %d: %+v", len(calls), calls)
	}
	want := []struct{ method, path string }{
		{http.MethodPatch, "/api/listeners/L-9"},
		{http.MethodDelete, "/api/listeners/L-9"},
		{http.MethodGet, "/api/listeners/L-9/events"},
	}
	for i, w := range want {
		if calls[i].Method != w.method || calls[i].Path != w.path {
			t.Fatalf("第 %d 笔云端调用不符: got %s %s want %s %s", i, calls[i].Method, calls[i].Path, w.method, w.path)
		}
	}
	if !bytes.Equal(calls[0].Body, patchBody) {
		t.Fatalf("PATCH body 须原样透传:\n got %s\nwant %s", calls[0].Body, patchBody)
	}
}

// --- 4. 云端不可达 → 502 listener_cloud_offline(绝不静默 200) -------------

func TestListenerProxyCloudUnreachable502(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		srv, cap, cookie := setupListenerProxyHarness(t, nil)
		cap.server.Close() // 关掉假 cloud:拨号即拒
		status, body := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
		if status != http.StatusBadGateway {
			t.Fatalf("云端不可达须 502,得到 %d(不得静默 200)", status)
		}
		assertListenerOfflineBody(t, body)
	})
	t.Run("identity unavailable", func(t *testing.T) {
		t.Setenv(AgentRuntimeEnv, "1")
		swapListenerSharedService(t, newListenerProxyTestService(false)) // 未注册/无凭据
		cap := newListenerFakeCloud(t, nil)
		swapListenerCloudBase(t, cap.server.URL)
		muxSrv := httptest.NewServer(newListenerProxyMux())
		t.Cleanup(muxSrv.Close)
		cookie := withListenerDashboardSessionCookie(t)
		status, body := doListenerProxy(t, muxSrv, cookie, http.MethodGet, "/api/listeners", nil)
		if status != http.StatusBadGateway {
			t.Fatalf("无身份须 502,得到 %d", status)
		}
		assertListenerOfflineBody(t, body)
		if calls := cap.snapshot(); len(calls) != 0 {
			t.Fatalf("无身份必须先于拨号短路,已触达云端: %+v", calls)
		}
	})
	t.Run("cloud 4xx passthrough not offline", func(t *testing.T) {
		// 云端业务错误(422 listener_validation_error 族)≠离线:云端状态保留,
		// 应答信封化 {code:422,msg:error 字段,data:null}——浏览器 request()
		// 抛携带可读文案的 Error(I1),而非 "Request failed with HTTP 422"。
		srv, _, cookie := setupListenerProxyHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"listener_validation_error"}`))
		})
		status, body := doListenerProxy(t, srv, cookie, http.MethodPost, "/api/listeners", []byte(`{"title":"x"}`))
		env := decodeListenerEnvelope(t, body)
		if status != http.StatusUnprocessableEntity || env.Code != float64(422) || env.Msg != "listener_validation_error" {
			t.Fatalf("云端 422 须信封化透传(状态保留+code=422+msg=error 字段),得到 %d %s", status, body)
		}
	})
}

// assertListenerOfflineBody 断言 502 body 形状:{code:'listener_cloud_offline'}。
func assertListenerOfflineBody(t *testing.T, body []byte) {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("502 body 须是 JSON: %s (%v)", body, err)
	}
	if payload.Code != "listener_cloud_offline" {
		t.Fatalf("502 body code 须为 listener_cloud_offline,得到 %q (body=%s)", payload.Code, body)
	}
	if strings.TrimSpace(payload.Msg) == "" {
		t.Fatalf("502 body 须带非空 msg: %s", body)
	}
	// Task 16 pre-step 0:msg 须含哨兵子串——request() 抛的 Error 只带 msg,
	// 前端 isListenerCloudOffline 的主匹配臂靠它,锁死不得回退成裸 prose。
	if !strings.Contains(payload.Msg, "listener_cloud_offline") {
		t.Fatalf("502 body msg 须含哨兵 listener_cloud_offline,得到 %q (body=%s)", payload.Msg, body)
	}
}

// --- 4b. 浏览器信封缝(C1 新增):代理 2xx 输出 = extractEnvelope 可解形状 --
//
// 镜像 app/website/src/services/agentApi.js:
//	extractEnvelope(json): code 非数字归 0 / msg 须 string / data 缺省 null;
//	request(): !response.ok || code!==0 → throw Error(msg);成功返回 envelope.data。
// 本测试锁三端零口径差:云端 wire 对象 → 代理信封 data → 浏览器拿到的
// data,逐项 round-trip 一致;code 必须是 JSON number(字符串 code 会被
// extractEnvelope 归 0,把错误应答假绿成成功)。

func TestListenerProxyBrowserEnvelopeSeam(t *testing.T) {
	srv, _, cookie := setupListenerProxyHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/listeners":
			_, _ = w.Write([]byte(`{"listeners":[{"id":"L-1","status":"active"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/listeners":
			w.WriteHeader(http.StatusCreated) // 云端 201:本地须拍平为信封化 200
			_, _ = w.Write([]byte(`{"listener":{"id":"L-fake","status":"active"}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{}`)) // 云端空对象:DELETE 面空 data 契约
		default:
			http.NotFound(w, r)
		}
	})

	t.Run("GET list: code number 0 + msg string + data round-trip", func(t *testing.T) {
		status, body := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		env := decodeListenerEnvelope(t, body)
		if env.Code != float64(0) || strings.TrimSpace(env.Msg) == "" {
			t.Fatalf("2xx 信封须 {code:number 0, msg:非空 string}: %s", body)
		}
		want := decodeListenerJSON(t, json.RawMessage(`{"listeners":[{"id":"L-1","status":"active"}]}`))
		if !reflect.DeepEqual(decodeListenerJSON(t, env.Data), want) {
			t.Fatalf("data round-trip 破损(extractEnvelope 返回值≠云端对象):\n got %s", env.Data)
		}
	})

	t.Run("POST create: 201 拍平 200,data.listener round-trip", func(t *testing.T) {
		status, body := doListenerProxy(t, srv, cookie, http.MethodPost, "/api/listeners", []byte(`{"title":"x"}`))
		if status != http.StatusOK {
			t.Fatalf("云端 201 须拍平为信封化 200(浏览器只认 ok+code 0),得到 %d", status)
		}
		env := decodeListenerEnvelope(t, body)
		if env.Code != float64(0) {
			t.Fatalf("信封 code 须 number 0: %s", body)
		}
		want := decodeListenerJSON(t, json.RawMessage(`{"listener":{"id":"L-fake","status":"active"}}`))
		if !reflect.DeepEqual(decodeListenerJSON(t, env.Data), want) {
			t.Fatalf("data.listener round-trip 破损: %s", env.Data)
		}
	})

	t.Run("DELETE: 空 body/空对象 → data {} (listenerDelete 直通不消费)", func(t *testing.T) {
		status, body := doListenerProxy(t, srv, cookie, http.MethodDelete, "/api/listeners/L-fake", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		env := decodeListenerEnvelope(t, body)
		if env.Code != float64(0) {
			t.Fatalf("信封 code 须 number 0: %s", body)
		}
		trimmed := strings.TrimSpace(string(env.Data))
		if trimmed != "{}" && trimmed != "" && trimmed != "null" {
			t.Fatalf("DELETE 空 data 契约破损(须 {} / 缺省 / null): %s", env.Data)
		}
	})
}

// --- 5. 空列表归一:listeners/events 为 null → [](前端可迭代契约) ----------

func TestListenerProxyEmptyListNormalization(t *testing.T) {
	cases := []struct {
		name      string
		cloudBody string
		localBody string
	}{
		{
			name:      "listeners null",
			cloudBody: `{"listeners":null}`,
			localBody: `{"listeners":[]}`,
		},
		{
			name:      "whole body null",
			cloudBody: `null`,
			localBody: `{"listeners":[]}`,
		},
		{
			name:      "listeners empty array unchanged",
			cloudBody: `{"listeners":[]}`,
			localBody: `{"listeners":[]}`,
		},
		{
			name:      "nonempty passthrough",
			cloudBody: `{"listeners":[{"id":"L-1"}]}`,
			localBody: `{"listeners":[{"id":"L-1"}]}`,
		},
		{
			name:      "events null",
			cloudBody: `{"events":null}`,
			localBody: `{"events":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, cookie := setupListenerProxyHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.cloudBody))
			})
			method, path := http.MethodGet, "/api/listeners"
			if strings.Contains(tc.name, "events") {
				path = "/api/listeners/L-9/events"
			}
			status, body := doListenerProxy(t, srv, cookie, method, path, nil)
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			env := decodeListenerEnvelope(t, body)
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(env.Data, &decoded); err != nil {
				t.Fatalf("信封 data 须是 JSON 对象: %s (%v)", env.Data, err)
			}
			var want map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.localBody), &want); err != nil {
				t.Fatalf("want body 解析失败: %v", err)
			}
			for k, wantRaw := range want {
				gotRaw, ok := decoded[k]
				if !ok {
					t.Fatalf("local body 缺键 %q: %s", k, body)
				}
				var gotVal, wantVal interface{}
				_ = json.Unmarshal(gotRaw, &gotVal)
				_ = json.Unmarshal(wantRaw, &wantVal)
				if fmt.Sprint(gotVal) != fmt.Sprint(wantVal) {
					t.Fatalf("键 %q 归一不符:\n got %s\nwant %s", k, body, tc.localBody)
				}
			}
			// null 归一核心断言:键值不得是 null。
			for k, raw := range decoded {
				if strings.EqualFold(strings.TrimSpace(string(raw)), "null") {
					t.Fatalf("键 %q 不得为 null(前端可迭代契约): %s", k, body)
				}
			}
		})
	}
}

// --- 6. ListenerCreateViaCloud:body 形状 + id/status 回传 + 4xx 错误映射 ----

func TestListenerCreateViaCloud(t *testing.T) {
	t.Run("body shape and 201 result", func(t *testing.T) {
		t.Setenv(AgentRuntimeEnv, "1")
		swapListenerSharedService(t, newListenerProxyTestService(true))
		var gotBody []byte
		cap := newListenerFakeCloud(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/agent/listeners" {
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"listener":{"id":"L-agent-42","status":"active","title":"盯构建"}}`))
				return
			}
			http.NotFound(w, r)
		})
		swapListenerCloudBase(t, cap.server.URL)

		id, status, err := ListenerCreateViaCloud(context.Background(),
			"盯 build.log",
			ListenerCloudSource{Type: "file_tail", Params: json.RawMessage(`{"path":"/tmp/build.log","match":"ERROR"}`)},
			"监听器命中:{{event}}",
			"recurring",
			"sess-current",
			"",
		)
		if err != nil {
			t.Fatalf("ListenerCreateViaCloud: %v", err)
		}
		if id != "L-agent-42" || status != "active" {
			t.Fatalf("id/status 回传不符: id=%q status=%q", id, status)
		}

		// body 形状 = server listenerCreateSchema 字段名(snake_case,恰好五键)。
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(gotBody, &sent); err != nil {
			t.Fatalf("create body 须是 JSON: %v", err)
		}
		if len(sent) != 5 {
			t.Fatalf("create body 须恰含 title/source/prompt_template/mode/target_session_id 五键: %s", gotBody)
		}
		for _, key := range []string{"title", "source", "prompt_template", "mode", "target_session_id"} {
			if _, ok := sent[key]; !ok {
				t.Fatalf("create body 缺 %q: %s", key, gotBody)
			}
		}
		var source struct {
			Type     string          `json:"type"`
			DeviceID string          `json:"device_id"`
			Params   json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sent["source"], &source); err != nil {
			t.Fatalf("source 解析失败: %v", err)
		}
		if source.Type != "file_tail" || source.DeviceID != "dev-proxy-1" {
			t.Fatalf("source 形状不符(设备面仍须本机 device_id): %+v", source)
		}
		var params map[string]interface{}
		_ = json.Unmarshal(source.Params, &params)
		if params["path"] != "/tmp/build.log" || params["match"] != "ERROR" {
			t.Fatalf("source.params 须原样: %s", source.Params)
		}
		if string(sent["mode"]) != `"recurring"` || string(sent["target_session_id"]) != `"sess-current"` {
			t.Fatalf("mode/target_session_id 不符: %s", gotBody)
		}
		// 设备面 header 契约同 GET 面。
		calls := cap.snapshot()
		if len(calls) != 1 || calls[0].Auth != "Bearer proxy-jwt" || calls[0].Device != "dev-proxy-1" {
			t.Fatalf("设备面 header 契约破损: %+v", calls)
		}
	})

	t.Run("session_id provenance: non-empty goes on the wire, blank stays off", func(t *testing.T) {
		t.Setenv(AgentRuntimeEnv, "1")
		swapListenerSharedService(t, newListenerProxyTestService(true))
		var gotBody []byte
		cap := newListenerFakeCloud(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/agent/listeners" {
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"listener":{"id":"L-agent-43","status":"active"}}`))
				return
			}
			http.NotFound(w, r)
		})
		swapListenerCloudBase(t, cap.server.URL)

		// 非空:第六键 session_id 随 body 上 wire(SV-3 溯源,server 写
		// created_by=ai:<session_id>)。
		if _, _, err := ListenerCreateViaCloud(context.Background(), "盯",
			ListenerCloudSource{Type: "file_tail", Params: json.RawMessage(`{}`)},
			"p {{event}}", "once", "sess-target", "sess-run"); err != nil {
			t.Fatalf("create with session_id: %v", err)
		}
		var withSession map[string]json.RawMessage
		if err := json.Unmarshal(gotBody, &withSession); err != nil {
			t.Fatalf("create body 须是 JSON: %v", err)
		}
		if string(withSession["session_id"]) != `"sess-run"` {
			t.Fatalf("非空 session_id 须上 wire: %s", gotBody)
		}

		// 空白串(TrimSpace 归一后为空,stdio 无会话面形态):键省略,
		// server 兜底 user:<deviceId>——不产 "session_id":"" 形态。
		if _, _, err := ListenerCreateViaCloud(context.Background(), "盯",
			ListenerCloudSource{Type: "file_tail", Params: json.RawMessage(`{}`)},
			"p {{event}}", "once", "sess-target", "  "); err != nil {
			t.Fatalf("create without session_id: %v", err)
		}
		var withoutSession map[string]json.RawMessage
		if err := json.Unmarshal(gotBody, &withoutSession); err != nil {
			t.Fatalf("create body 须是 JSON: %v", err)
		}
		if _, has := withoutSession["session_id"]; has {
			t.Fatalf("空白 session_id 须省略键: %s", gotBody)
		}
		if n := len(cap.snapshot()); n != 2 {
			t.Fatalf("应恰两笔云端调用: %d", n)
		}
	})

	t.Run("cloud 4xx maps to error not offline", func(t *testing.T) {
		t.Setenv(AgentRuntimeEnv, "1")
		swapListenerSharedService(t, newListenerProxyTestService(true))
		cap := newListenerFakeCloud(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"listener_source_not_in_1a"}`))
		})
		swapListenerCloudBase(t, cap.server.URL)
		_, _, err := ListenerCreateViaCloud(context.Background(), "x", ListenerCloudSource{Type: "http_poll", Params: json.RawMessage(`{"url":"https://a"}`)}, "p", "recurring", "sess", "")
		if err == nil {
			t.Fatal("云端 400 必须映射为错误,不得假成功")
		}
		if strings.Contains(err.Error(), "listener cloud offline") {
			t.Fatalf("云端业务 4xx 不是离线哨兵: %v", err)
		}
	})

	t.Run("offline sentinel", func(t *testing.T) {
		t.Setenv(AgentRuntimeEnv, "1")
		swapListenerSharedService(t, newListenerProxyTestService(false))
		cap := newListenerFakeCloud(t, nil)
		swapListenerCloudBase(t, cap.server.URL)
		_, _, err := ListenerCreateViaCloud(context.Background(), "x", ListenerCloudSource{Type: "file_tail", Params: json.RawMessage(`{}`)}, "p", "once", "sess", "")
		if err == nil || !strings.Contains(err.Error(), "listener cloud offline") {
			t.Fatalf("无身份须回离线哨兵错误: %v", err)
		}
	})
}

// --- 6b. list/update/delete 客户端最小面(Task 12/13 消费) ------------------

func TestListenerCloudClientListUpdateDelete(t *testing.T) {
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(true))
	cap := newListenerFakeCloud(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agent/listeners":
			_, _ = w.Write([]byte(`{"listeners":[{"id":"L-a","source_type":"file_tail","updated_at":"2026-11-21T00:00:00Z"},{"id":"L-b","updated_at":"2026-11-21T01:00:00Z"}]}`))
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"listener":{"id":"L-a","status":"paused"}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	})
	swapListenerCloudBase(t, cap.server.URL)

	rows, err := ListenerListViaCloud(context.Background())
	if err != nil {
		t.Fatalf("ListenerListViaCloud: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "L-a" || rows[0].SourceType != "file_tail" {
		t.Fatalf("list 解析不符(server {listeners:[...]} 真实 wire 形态): %+v", rows)
	}

	if err := ListenerUpdateViaCloud(context.Background(), "L-a", map[string]interface{}{"status": "paused", "updated_at": "2026-11-21T00:00:00Z"}); err != nil {
		t.Fatalf("ListenerUpdateViaCloud: %v", err)
	}
	if err := ListenerDeleteViaCloud(context.Background(), "L-a"); err != nil {
		t.Fatalf("ListenerDeleteViaCloud: %v", err)
	}
	calls := cap.snapshot()
	if len(calls) != 3 {
		t.Fatalf("客户端三笔调用不符: %+v", calls)
	}
	if calls[1].Method != http.MethodPatch || calls[1].Path != "/api/listeners/L-a" {
		t.Fatalf("update 调用不符: %+v", calls[1])
	}
	if calls[2].Method != http.MethodDelete || calls[2].Path != "/api/listeners/L-a" {
		t.Fatalf("delete 调用不符: %+v", calls[2])
	}
	var patchSent map[string]interface{}
	if err := json.Unmarshal(calls[1].Body, &patchSent); err != nil || patchSent["status"] != "paused" {
		t.Fatalf("update body 不符: %s (%v)", calls[1].Body, err)
	}
}

// --- 7. routes.go 注册存在性(2026-10-21 漏注册 404 同族护栏) ----------------

func TestListenerProxyRoutesWired(t *testing.T) {
	raw, err := os.ReadFile("../routes/routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	for _, registration := range []string{
		`register("GET /api/listeners",`,
		`register("POST /api/listeners",`,
		`register("PATCH /api/listeners/{id}",`,
		`register("DELETE /api/listeners/{id}",`,
		`register("GET /api/listeners/{id}/events",`,
	} {
		if !strings.Contains(code.String(), registration) {
			t.Fatalf("routes.go 缺注册 %s(漏注册=404,2026-10-21 同族)", registration)
		}
	}

	// 三层拓扑回环转发的另一侧:runtime mux 同名端点(2026-11-22 补丁)。
	// core 侧身份恒空必走转发,缺 runtime 侧注册=core 转发恒 404(双注册
	// 教训,2026-10-21 同族)。
	agentRaw, err := os.ReadFile("../../agentruntime/server.go")
	if err != nil {
		t.Fatalf("read ../../agentruntime/server.go: %v", err)
	}
	var agentCode strings.Builder
	for _, line := range strings.Split(string(agentRaw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		agentCode.WriteString(line)
		agentCode.WriteByte('\n')
	}
	for _, registration := range []string{
		`mux.HandleFunc("GET /api/listeners", services.HandleListenerListAgent)`,
		`mux.HandleFunc("POST /api/listeners", services.HandleListenerCreateAgent)`,
		`mux.HandleFunc("PATCH /api/listeners/{id}", services.HandleListenerPatchAgent)`,
		`mux.HandleFunc("DELETE /api/listeners/{id}", services.HandleListenerDeleteAgent)`,
		`mux.HandleFunc("GET /api/listeners/{id}/events", services.HandleListenerEventsAgent)`,
	} {
		if !strings.Contains(agentCode.String(), registration) {
			t.Fatalf("agentruntime/server.go 缺注册 %s(缺=core 转发恒 404)", registration)
		}
	}
}

// --- 8. I1/M2:id 闸(handler 面 + 客户端面 + HTTP 面;畸形 id 零云端调用) ---

func TestListenerIDGuard(t *testing.T) {
	// 8a. listenerPathID 直连:empty/../?/空格/斜杠 一律 400 false。
	for _, id := range []string{"", "..", " .. ", "a?b", "a b", "a/b"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/listeners/x", nil)
		r.SetPathValue("id", id)
		got, ok := listenerPathID(w, r)
		if ok || got != "" || w.Code != http.StatusBadRequest {
			t.Fatalf("id %q 须 400 拒绝,得到 ok=%t id=%q code=%d", id, ok, got, w.Code)
		}
	}

	// 8b. 客户端面(Update/Delete 直通,Task 12/13 模型可控入参):畸形 id →
	// 普通错误(非离线哨兵),且绝不触达云端。
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(true))
	cap := newListenerFakeCloud(t, nil)
	swapListenerCloudBase(t, cap.server.URL)
	for _, id := range []string{"L-a/b", "..", "L-a?b"} {
		if err := ListenerUpdateViaCloud(context.Background(), id, map[string]interface{}{"status": "paused"}); err == nil || strings.Contains(err.Error(), "listener cloud offline") {
			t.Fatalf("Update id=%q 须普通错误(非哨兵): %v", id, err)
		}
		if err := ListenerDeleteViaCloud(context.Background(), id); err == nil || strings.Contains(err.Error(), "listener cloud offline") {
			t.Fatalf("Delete id=%q 须普通错误(非哨兵): %v", id, err)
		}
	}
	if calls := cap.snapshot(); len(calls) != 0 {
		t.Fatalf("畸形 id 不得触达云端: %+v", calls)
	}

	// 8c. HTTP 面:URL 编码混进 {id} 的空格/? 同样 400,零云端调用
	// (.. 走 dot-segment 清洗由 ServeMux 先行,直连 8a 已覆盖)。
	srv, cap2, cookie := setupListenerProxyHarness(t, nil)
	for _, enc := range []string{"a%20b", "a%3Fb"} {
		status, _ := doListenerProxy(t, srv, cookie, http.MethodPatch, "/api/listeners/"+enc, []byte(`{}`))
		if status != http.StatusBadRequest {
			t.Fatalf("PATCH id=%s 须 400,得到 %d", enc, status)
		}
	}
	if calls := cap2.snapshot(); len(calls) != 0 {
		t.Fatalf("畸形 id 的 HTTP 面不得触达云端: %+v", calls)
	}
}

// --- 9. I2:入站 body 超限 → 本地 400,零云端调用 ----------------------------

func TestListenerProxyBodyOverLimitRejected(t *testing.T) {
	srv, cap, cookie := setupListenerProxyHarness(t, nil)
	huge := bytes.Repeat([]byte("x"), listenerProxyBodyLimit+1)
	status, body := doListenerProxy(t, srv, cookie, http.MethodPost, "/api/listeners", huge)
	if status != http.StatusBadRequest {
		t.Fatalf("超限 POST 须本地 400,得到 %d body=%s", status, body)
	}
	status, _ = doListenerProxy(t, srv, cookie, http.MethodPatch, "/api/listeners/L-ok", huge)
	if status != http.StatusBadRequest {
		t.Fatalf("超限 PATCH 须本地 400,得到 %d", status)
	}
	if calls := cap.snapshot(); len(calls) != 0 {
		t.Fatalf("超限 body 不得触达云端: %+v", calls)
	}
}

// --- 10. M1:云端响应超限(截断 JSON)→ 离线哨兵族,不透传腐败载荷 ------------

func TestListenerCloudResponseOverLimitOffline(t *testing.T) {
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(true))
	cap := newListenerFakeCloud(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), listenerCloudFetchBodyLimit+1024))
	})
	swapListenerCloudBase(t, cap.server.URL)
	_, _, err := listenerCloudRequest(context.Background(), http.MethodGet, "/api/listeners", nil)
	if err == nil || !strings.Contains(err.Error(), "listener cloud offline") {
		t.Fatalf("响应超限须回离线哨兵族错误: %v", err)
	}
}

// --- 11. M7:非 JSON create body → 云端 422 原样透传(代理不抢答 schema 错) ---

func TestListenerProxyCreateNonJSONPassthrough422(t *testing.T) {
	srv, _, cookie := setupListenerProxyHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"listener_validation_error"}`))
	})
	status, body := doListenerProxy(t, srv, cookie, http.MethodPost, "/api/listeners", []byte("not-json"))
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(body), "listener_validation_error") {
		t.Fatalf("非 JSON create body 须由云端 422 兜底透传,得到 %d %s", status, body)
	}
}

// --- 10. 三层拓扑回环转发(Mac/双单元补丁):core 身份恒空 → 转发 user agent --

// listenerAgentCapture 假 user agent runtime mux 的请求捕获 + 可配置应答:
// 形状镜像 listenerCloudCapture,Auth/Device 断言转发链不携带身份头
// (身份注入是 runtime 侧直拨的事,转发面只搬运载荷)。
type listenerAgentCapture struct {
	mu      sync.Mutex
	calls   []listenerCloudCall
	handler func(w http.ResponseWriter, r *http.Request, body []byte)
}

func (c *listenerAgentCapture) record(r *http.Request, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, listenerCloudCall{
		Method: r.Method,
		Path:   r.URL.Path,
		Auth:   r.Header.Get("Authorization"),
		Device: r.Header.Get("X-Aliang-Device-ID"),
		Body:   body,
	})
}

func (c *listenerAgentCapture) snapshot() []listenerCloudCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]listenerCloudCall(nil), c.calls...)
}

// setupListenerCoreForwardHarness core 形态一把装好:不设 runtime env
// (t.Setenv 未触即 unset)、共享服务未注册(身份快照空 → 转发分支)、
// listenerUserAgentBase 缝指假 agent、返回带 mux 的测试服务器/假 agent
// 捕获器/会话 cookie。
func setupListenerCoreForwardHarness(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*httptest.Server, *listenerAgentCapture, *http.Cookie) {
	t.Helper()
	if os.Getenv(AgentRuntimeEnv) != "" {
		t.Fatalf("core 形态 harness 要求 %s 未设置", AgentRuntimeEnv)
	}
	swapListenerSharedService(t, newListenerProxyTestService(false))
	cap := &listenerAgentCapture{handler: handler}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		cap.record(r, body)
		if handler != nil {
			handler(w, r, body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/listeners":
			_, _ = w.Write([]byte(`{"listeners":[]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(agent.Close)
	swapListenerUserAgentBase(t, agent.URL)
	srv := httptest.NewServer(newListenerProxyMux())
	t.Cleanup(srv.Close)
	return srv, cap, withListenerDashboardSessionCookie(t)
}

// swapListenerUserAgentBase 换回环转发基址缝并登记还原。
func swapListenerUserAgentBase(t *testing.T, base string) {
	t.Helper()
	old := listenerUserAgentBase
	listenerUserAgentBase = func() string { return base }
	t.Cleanup(func() { listenerUserAgentBase = old })
}

// 10a. GET 转发:core 身份空 → 同 (method,path) 打到假 agent,载荷逐项
// 一致、转发链无身份头;本地面侧契约不变(200 信封,data=agent 对象)。
func TestListenerProxyCoreTopologyForwardsToUserAgent(t *testing.T) {
	agentBody := []byte(`{"listeners":[{"id":"L-9","title":"盯发布","status":"active"}]}`)
	srv, cap, cookie := setupListenerCoreForwardHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(agentBody)
	})

	status, localBody := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
	if status != http.StatusOK {
		t.Fatalf("转发 GET status = %d,body=%s", status, localBody)
	}
	env := decodeListenerEnvelope(t, localBody)
	if env.Code != float64(0) {
		t.Fatalf("转发 2xx 信封 code 须为 number 0: %s", localBody)
	}
	if !reflect.DeepEqual(decodeListenerJSON(t, env.Data), decodeListenerJSON(t, agentBody)) {
		t.Fatalf("信封 data 须逐项等于 agent 对象:\n got %s\nwant %s", env.Data, agentBody)
	}
	calls := cap.snapshot()
	if len(calls) != 1 {
		t.Fatalf("假 agent 应恰收到 1 次转发,得到 %+v", calls)
	}
	if calls[0].Method != http.MethodGet || calls[0].Path != "/api/listeners" {
		t.Fatalf("转发 (method,path) 不符: %+v", calls[0])
	}
	if calls[0].Auth != "" || calls[0].Device != "" {
		t.Fatalf("转发链不得携带身份头: Auth=%q Device=%q", calls[0].Auth, calls[0].Device)
	}
}

// 10b. POST 转发载荷逐字节一致(device_id 覆盖后仍是原始 body 过转发链——
// 覆盖发生在本进程 listenerCreate,agent 侧直拨前还会再覆盖,幂等无害)。
func TestListenerProxyCoreTopologyForwardsCreateBody(t *testing.T) {
	srv, cap, cookie := setupListenerCoreForwardHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"listener":{"id":"L-new","status":"active"}}`))
	})
	// 真实 core 镜像:有设备 id 无授权 → 覆盖在本进程完成,转发 body 已携带。
	swapListenerSharedService(t, newListenerCoreTestService())
	payload := []byte(`{"title":"盯构建","source":{"type":"file_tail","params":{},"device_id":"forged"}}`)

	status, localBody := doListenerProxy(t, srv, cookie, http.MethodPost, "/api/listeners", payload)
	if status != http.StatusOK {
		t.Fatalf("转发 POST status = %d,body=%s", status, localBody)
	}
	calls := cap.snapshot()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/api/listeners" {
		t.Fatalf("假 agent 收到调用不符: %+v", calls)
	}
	// device_id 已在本进程覆盖为本机测试值(dev-proxy-1),转发 body 须携带。
	if !strings.Contains(string(calls[0].Body), `"dev-proxy-1"`) {
		t.Fatalf("转发 body 须为覆盖后的载荷: %s", calls[0].Body)
	}
}

// 10c. agent 侧业务错误(非 2xx)照透传:状态与可读文案不走离线哨兵。
func TestListenerProxyCoreTopologyRelaysBusinessError(t *testing.T) {
	srv, cap, cookie := setupListenerCoreForwardHarness(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"listener_validation_error"}`))
	})
	_ = cap

	status, localBody := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(localBody), "listener_validation_error") {
		t.Fatalf("agent 侧 422 须原样透传,得到 %d %s", status, localBody)
	}
}

// 10d. runtime 未运行(转发基址不可达)→ 离线哨兵族 502,横幅语义不回退。
func TestListenerProxyCoreTopologyAgentUnreachableOffline(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 先关再挂缝=确定性 connection refused
	swapListenerSharedService(t, newListenerProxyTestService(false))
	swapListenerUserAgentBase(t, deadURL)
	srv := httptest.NewServer(newListenerProxyMux())
	t.Cleanup(srv.Close)
	cookie := withListenerDashboardSessionCookie(t)

	status, localBody := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
	if status != http.StatusBadGateway || !strings.Contains(string(localBody), "listener_cloud_offline") {
		t.Fatalf("agent 不可达须 502 离线哨兵,得到 %d %s", status, localBody)
	}
}

// 10e. runtime 进程自身(单进程形态)身份缺失 → 哨兵原路,不得转发(防循环
// +真离线语义不漂移):假 agent 恒零调用。
func TestListenerProxyRuntimeIdentityMissingStaysSentinel(t *testing.T) {
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(false))
	cap := &listenerAgentCapture{}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.record(r, body)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(agent.Close)
	swapListenerUserAgentBase(t, agent.URL)
	srv := httptest.NewServer(newListenerProxyMux())
	t.Cleanup(srv.Close)
	cookie := withListenerDashboardSessionCookie(t)

	status, localBody := doListenerProxy(t, srv, cookie, http.MethodGet, "/api/listeners", nil)
	if status != http.StatusBadGateway || !strings.Contains(string(localBody), "listener_cloud_offline") {
		t.Fatalf("runtime 形态身份缺失须 502 哨兵原路,得到 %d %s", status, localBody)
	}
	if calls := cap.snapshot(); len(calls) != 0 {
		t.Fatalf("runtime 形态不得回环转发,已触达: %+v", calls)
	}
}

// 10f. runtime 面收尾契约(双重信封修复,2026-11-22):agent 面应答必须是
// 云端原始形态(裸 {listeners:[…]},无 {code,msg,data} 信封)——信封只由
// core 侧转发消费者打一次;若 runtime 面也信封化,浏览器取 listeners 恒空。
func TestListenerAgentFaceRawPassthroughNoEnvelope(t *testing.T) {
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(true))
	cloudBody := []byte(`{"listeners":[{"id":"L-1","title":"盯构建","status":"active"}]}`)
	cap := newListenerFakeCloud(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cloudBody)
	})
	swapListenerCloudBase(t, cap.server.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/listeners", nil)
	HandleListenerListAgent(rec, req) // runtime 面:无会话闸
	if rec.Code != http.StatusOK {
		t.Fatalf("agent 面 status = %d", rec.Code)
	}
	raw := rec.Body.Bytes()
	if strings.Contains(string(raw), `"code":`) {
		t.Fatalf("agent 面不得信封化(双层包裹实测病),得到 %s", raw)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("agent 面 body 须为合法 JSON: %v", err)
	}
	if _, ok := got["listeners"]; !ok {
		t.Fatalf("agent 面 body 须为云端原始形态(含 listeners 键): %s", raw)
	}
	if !reflect.DeepEqual(decodeListenerJSON(t, raw), decodeListenerJSON(t, cloudBody)) {
		t.Fatalf("agent 面 body 须逐项等于云端对象:\n got %s\nwant %s", raw, cloudBody)
	}
}
