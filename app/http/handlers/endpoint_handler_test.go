package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// ---- 脚手架 -----------------------------------------------------------------

// newEndpointHandlerHarness 起一个 :memory: 独立库的 endpoint service(经
// services 包测试注入缝)+ handler,并签发一枚真实 dashboard session cookie。
// 完全不碰真实数据目录,也不污染 GetEndpointService 单例。
func newEndpointHandlerHarness(t *testing.T) (*EndpointHandler, *http.Cookie) {
	_, h, cookie := newEndpointHandlerHarnessSvc(t)
	return h, cookie
}

// newEndpointHandlerHarnessSvc 同 newEndpointHandlerHarness,另返回底层
// service:动作端点测试(内置行真探测双分支)需要注入拨测地址。
func newEndpointHandlerHarnessSvc(t *testing.T) (*services.EndpointService, *EndpointHandler, *http.Cookie) {
	t.Helper()
	svc, err := services.NewEndpointServiceForTest()
	if err != nil {
		t.Fatalf("起测试 endpoint service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	mint := httptest.NewRecorder()
	mintReq := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", nil)
	mintReq.RemoteAddr = "127.0.0.1:55555"
	if err := middleware.IssueDashboardSession(mint, mintReq); err != nil {
		t.Fatalf("签发 dashboard session: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range mint.Result().Cookies() {
		if c.Name == middleware.DashboardSessionCookieName {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatal("未签发 dashboard session cookie")
	}
	t.Cleanup(middleware.ResetDashboardSessionForTest)
	return svc, NewEndpointHandler(svc), cookie
}

// endpointDo 发一个带 session 的请求并返回 recorder。{id} 段用占位符
// "__ID__" 表示,内部替换成真实 id 并 SetPathValue(模拟 ServeMux 通配注入)。
func endpointDo(t *testing.T, h *EndpointHandler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	id := ""
	if idx := strings.Index(path, "__ID__"); idx >= 0 {
		rest := path[idx+len("__ID__"):]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			id = rest[:slash]
			path = path[:idx] + id + rest[slash:]
		} else {
			id = rest
			path = path[:idx] + id
		}
	} else if strings.HasPrefix(path, "/api/endpoints/") {
		// 真实 id 直接写在 path 里(非占位符形态):末段(或倒数第二段,
		// 带 /action 后缀时)即 id。
		rest := strings.TrimPrefix(path, "/api/endpoints/")
		if slash := strings.Index(rest, "/"); slash >= 0 {
			id = rest[:slash]
		} else {
			id = rest
		}
	}
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.AddCookie(cookie)
	if id != "" {
		r.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	switch {
	case strings.HasSuffix(path, "/policy"):
		h.HandlePolicy(rec, r)
	case strings.HasSuffix(path, "/test"):
		h.HandleTest(rec, r)
	case strings.HasSuffix(path, "/fetch-models"):
		h.HandleFetchModels(rec, r)
	case strings.HasSuffix(path, "/probe-models"):
		h.HandleProbeModels(rec, r)
	case strings.HasSuffix(path, "/activate"):
		h.HandleEndpointActivate(rec, r)
	case strings.HasSuffix(path, "/reset-health"):
		h.HandleResetHealth(rec, r)
	case method == http.MethodGet && strings.HasSuffix(path, "/api/endpoints"):
		h.HandleList(rec, r)
	case method == http.MethodGet && strings.Contains(path, "/api/endpoints/"):
		h.HandleGet(rec, r)
	case method == http.MethodPost && strings.HasSuffix(path, "/api/endpoints"):
		h.HandleCreate(rec, r)
	case method == http.MethodPut:
		h.HandleUpdate(rec, r)
	case method == http.MethodDelete:
		h.HandleDelete(rec, r)
	default:
		t.Fatalf("endpointDo: 无法分派 %s %s", method, path)
	}
	return rec
}

// endpointDecode 解包 common 信封。
func endpointDecode(t *testing.T, rec *httptest.ResponseRecorder) (int, map[string]interface{}) {
	t.Helper()
	var env struct {
		Code int                    `json:"code"`
		Msg  string                 `json:"msg"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	return env.Code, env.Data
}

func endpointCreateBody(t *testing.T, h *EndpointHandler, cookie *http.Cookie, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	return endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints", string(raw))
}

// ---- 测试 -------------------------------------------------------------------

// 会话闸:无 dashboard session 的任何写操作必须 401,且不得触碰 service。
func TestEndpointHandlerRequiresDashboardSession(t *testing.T) {
	h := &EndpointHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/endpoints", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 session GET code = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// method 闸必须是真 405:common.Error 收业务码,直传 http 状态会被折成
// 400/500(同 agent_ai_mcp_handler_test 的教训)。
func TestEndpointHandlerMethodNotAllowed(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/endpoints", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 到 GET 端点 code = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
}

// GET 列表:内置行 + 掩码规则(长度≤8 全掩;>8 前4+***+后4)+ inflight 字段存在。
func TestEndpointHandlerListMasksAPIKey(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-mask", "name": "mask-co", "base_url": "https://up.example.com",
		"api_key": "sk-abcdefghij1234567890", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-short", "name": "short-co", "base_url": "https://up2.example.com",
		"api_key": "12345678", "protocol_openai": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create short code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	items, ok := data["endpoints"].([]interface{})
	if !ok || len(items) != 3 {
		t.Fatalf("列表应为 3 条(内置+2), got %#v", data)
	}
	byID := map[string]map[string]interface{}{}
	for _, it := range items {
		m := it.(map[string]interface{})
		byID[m["id"].(string)] = m
		if _, has := m["inflight"]; !has {
			t.Fatalf("条目缺 inflight 字段: %#v", m)
		}
	}
	if got := byID["ep-mask"]["api_key"]; got != "sk-a***7890" {
		t.Fatalf("长 key 掩码 = %v, want sk-a*******7890", got)
	}
	if got := byID["ep-short"]["api_key"]; got != "********" {
		t.Fatalf("短 key 掩码 = %v, want ********", got)
	}
	if got, has := byID["builtin-aliang"]["api_key"]; !has || got != "" {
		t.Fatalf("内置行 api_key 应为空串, got %#v has=%v", got, has)
	}
}

// POST 校验:缺 name / 双协议全关 / 非回环缺 key → 400。
func TestEndpointHandlerCreateValidation(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	cases := []map[string]interface{}{
		{"id": "v1", "base_url": "https://x.example.com", "protocol_anthropic": true},
		{"id": "v2", "name": "no-proto", "base_url": "https://x.example.com", "api_key": "k"},
		{"id": "v3", "name": "no-key", "base_url": "https://x.example.com", "protocol_anthropic": true},
		{"id": "v4", "name": "bad-url", "base_url": "ftp://x.example.com", "api_key": "k", "protocol_anthropic": true},
	}
	for _, body := range cases {
		rec := endpointCreateBody(t, h, cookie, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("校验用例 %v code=%d, want 400; body=%s", body["id"], rec.Code, rec.Body.String())
		}
	}
}

// PUT:api_key 空值=不改(旧值保留);builtin 禁改 base_url/auto_group → 400;
// 不存在的 id → 404;成功后 GET 验证落库(掩码不变)。
func TestEndpointHandlerUpdateSemantics(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-upd", "name": "upd", "base_url": "https://up.example.com",
		"api_key": "sk-oldkeyvalue123456", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}

	// api_key 空值 = 不改。
	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoints/ep-upd",
		`{"name":"upd2","api_key":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put code=%d body=%s", rec.Code, rec.Body.String())
	}

	// builtin 禁改 base_url。
	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoints/"+storage.BuiltinEndpointID,
		`{"base_url":"https://evil.example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("builtin 改 base_url code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// builtin 禁关 auto_group。
	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoints/"+storage.BuiltinEndpointID,
		`{"auto_group":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("builtin 关 auto code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// 不存在的 id → 404。
	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoints/ep-ghost", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在 id code=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	// GET 验证:名字已改、key 保留(掩码仍是旧 key 的形状)。
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	_, data := endpointDecode(t, rec)
	for _, it := range data["endpoints"].([]interface{}) {
		m := it.(map[string]interface{})
		if m["id"] == "ep-upd" {
			if m["name"] != "upd2" {
				t.Fatalf("name 未更新: %#v", m)
			}
			if m["api_key"] != "sk-o***3456" {
				t.Fatalf("api_key 空值应保留旧值, 掩码 = %v", m["api_key"])
			}
		}
	}
}

// DELETE:builtin → 409;普通行 → 200 且再 GET 消失。
func TestEndpointHandlerDeleteBuiltinConflict(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-del", "name": "del", "base_url": "https://up.example.com",
		"api_key": "sk-123456789012", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = endpointDo(t, h, cookie, http.MethodDelete, "/api/endpoints/"+storage.BuiltinEndpointID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("删 builtin code=%d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	rec = endpointDo(t, h, cookie, http.MethodDelete, "/api/endpoints/ep-del", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删普通行 code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	_, data := endpointDecode(t, rec)
	for _, it := range data["endpoints"].([]interface{}) {
		if it.(map[string]interface{})["id"] == "ep-del" {
			t.Fatalf("删除后列表仍有 ep-del")
		}
	}
}

// test / fetch-models / reset-health 三动作端点。
// (用带 svc 的 harness:builtin 行走真探测,需注入拨测地址做双分支断言。)
func TestEndpointHandlerActionEndpoints(t *testing.T) {
	svc, h, cookie := newEndpointHandlerHarnessSvc(t)

	// test:builtin 行走真探测(5a9ef7af),与 service 层双分支对齐——
	// 真监听入口 → 200 + ok=true + 可达文案;死端口 → 200 + ok=false +
	// 可自查文案。都不 5xx。(不可达的单测兜底另见 service 层 TestEndpointService_TestProbe。)
	reachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(reachable.Close)
	svc.SetBuiltinProbeAddrForTest(strings.TrimPrefix(reachable.URL, "http://"))
	rec := endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/"+storage.BuiltinEndpointID+"/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("builtin test(可达) code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	if ok, _ := data["ok"].(bool); !ok {
		t.Fatalf("内置行入口可达应 ok=true: %#v", data)
	}
	if summary, _ := data["error"].(string); !strings.Contains(summary, "平台隧道入口可达") {
		t.Fatalf("可达摘要文案不符: %#v", data)
	}

	// 死端口分支:ok=false + 摘要带「不可达」可自查(仍是 200 不是 5xx)。
	svc.SetBuiltinProbeAddrForTest("127.0.0.1:1")
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/"+storage.BuiltinEndpointID+"/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("builtin test(不可达) code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data = endpointDecode(t, rec)
	if ok, _ := data["ok"].(bool); ok {
		t.Fatalf("内置行入口不可达应 ok=false: %#v", data)
	}
	if summary, _ := data["error"].(string); !strings.Contains(summary, "不可达") {
		t.Fatalf("不可达摘要应可自查: %#v", data)
	}

	// fetch-models:fake 上游返回 OpenAI 形态列表,不落库。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-4.6"},{"id":"kimi-k2"}]}`))
	}))
	t.Cleanup(upstream.Close)

	rec = endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-fm", "name": "fm", "base_url": upstream.URL,
		"api_key": "sk-fmkey1234567", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create ep-fm code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/ep-fm/fetch-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch-models code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data = endpointDecode(t, rec)
	models, _ := data["models"].([]interface{})
	if len(models) != 2 || models[0] != "glm-4.6" {
		t.Fatalf("fetch-models 结果 = %#v", data)
	}

	// reset-health:清零后 health_status=ok。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/ep-fm/reset-health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset-health code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	_, data = endpointDecode(t, rec)
	for _, it := range data["endpoints"].([]interface{}) {
		m := it.(map[string]interface{})
		if m["id"] == "ep-fm" && m["health_status"] != "ok" {
			t.Fatalf("reset-health 后 health_status = %v", m["health_status"])
		}
	}
}

// 组策略 GET/PUT:默认值 → 修改 → 回读持久化。
func TestEndpointHandlerPolicy(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	rec := endpointDo(t, h, cookie, http.MethodGet, "/api/endpoint-groups/auto/policy", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get policy code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	if data["failure_threshold"] != float64(3) || data["failure_disable"] != true {
		t.Fatalf("默认策略 = %#v", data)
	}

	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoint-groups/auto/policy",
		`{"failure_disable":false,"failure_threshold":5,"cooldown_sec":120,"rate_limit_cooldown_sec":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put policy code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoint-groups/auto/policy", "")
	_, data = endpointDecode(t, rec)
	if data["failure_threshold"] != float64(5) || data["failure_disable"] != false || data["cooldown_sec"] != float64(120) {
		t.Fatalf("策略未持久化: %#v", data)
	}
}

// PUT 非法 JSON → 400;超大 body → 400(MaxBytesReader)。
func TestEndpointHandlerBadRequestShapes(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	rec := endpointDo(t, h, cookie, http.MethodPut, "/api/endpoints/ep-x", `{invalid`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	raw := []byte(`{"id":"big","name":"` + strings.Repeat("a", 3<<20) + `"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/endpoints", bytes.NewReader(raw))
	r.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.HandleCreate(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大 body code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// 信封形状:成功响应必须是 {code:0,msg:"success",data:...}(前端解包契约)。
func TestEndpointHandlerSuccessEnvelope(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != common.CodeSuccess || env.Msg != "success" {
		t.Fatalf("信封 = %+v", env)
	}
}

// GET 单条:存在 → 200 掩码形状;不存在 → 404(spec §11 权威条目)。
func TestEndpointHandlerGetSingle(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-one", "name": "one", "base_url": "https://up.example.com",
		"api_key": "sk-singlekey12345678", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints/ep-one", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	if data["id"] != "ep-one" || data["api_key"] != "sk-s***5678" {
		t.Fatalf("单条形状 = %#v", data)
	}
	if _, has := data["inflight"]; !has {
		t.Fatalf("单条缺 inflight: %#v", data)
	}
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints/ep-ghost", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在 id code=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// Create 重复 id → 409(store 哨兵 ErrEndpointIDExists)。
func TestEndpointHandlerCreateDuplicateConflict(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	body := map[string]interface{}{
		"id": "ep-dup", "name": "dup", "base_url": "https://up.example.com",
		"api_key": "sk-dupkey123456789", "protocol_anthropic": true,
	}
	if rec := endpointCreateBody(t, h, cookie, body); rec.Code != http.StatusOK {
		t.Fatalf("first create code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := endpointCreateBody(t, h, cookie, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 id code=%d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// final-review 必改 1:POST 不带 id(UI endpointToPayload 不下发 id)必须成功
// 创建——handler 层生成 uuid,返回视图 id 非空且入库可 GET。这是 UI↔Go 缝合
// 处的跨任务契约:两侧单测各自绿(handler 测试显式传 id、UI 测试 mock agentApi),
// 缺这条就零覆盖。
func TestEndpointHandlerCreateGeneratesIDWhenOmitted(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"name": "no-id-endpoint", "base_url": "https://up.example.com",
		"api_key": "sk-noidkey1234567", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("无 id 创建 code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	// 返回视图必须是单视图(与 HandleGet/HandleUpdate 一致,final-review
	// finding 4),且 id 非空。
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatalf("返回视图 id 为空: %#v", data)
	}
	if data["name"] != "no-id-endpoint" {
		t.Fatalf("返回视图 name = %v", data["name"])
	}
	if _, isList := data["endpoints"]; isList {
		t.Fatalf("Create 响应应为单视图而非数组信封: %#v", data)
	}

	// 入库可 GET(GET 单条 200 且同 id)。
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 新建行 code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data2 := endpointDecode(t, rec)
	if data2["id"] != id {
		t.Fatalf("GET 到的 id = %v, want %s", data2["id"], id)
	}
}

// 显式 id 仍允许(管理面测试用):带了就原样落库。
func TestEndpointHandlerCreateKeepsExplicitID(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-explicit", "name": "explicit", "base_url": "https://up.example.com",
		"api_key": "sk-explicit12345", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("显式 id 创建 code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	if data["id"] != "ep-explicit" {
		t.Fatalf("显式 id 应原样保留, got %v", data["id"])
	}
}

// policy PUT 负值 → 400;fetch-models 对 builtin(无 base_url)→ 400;
// DELETE 不存在 id → 200 幂等。
func TestEndpointHandlerPolicyNegativeAndIdempotentDelete(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	rec := endpointDo(t, h, cookie, http.MethodPut, "/api/endpoint-groups/auto/policy",
		`{"failure_threshold":-1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("负值 threshold code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoint-groups/auto/policy",
		`{"cooldown_sec":-5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("负值 cooldown code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/"+storage.BuiltinEndpointID+"/fetch-models", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("builtin fetch-models code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	rec = endpointDo(t, h, cookie, http.MethodDelete, "/api/endpoints/ep-never-existed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删除不存在 id code=%d, want 200 幂等; body=%s", rec.Code, rec.Body.String())
	}
}

// probe-models:按裸 base_url 探测(新建表单「自动获取」,行未落库)。
// 契约:合法 URL+fake 上游 → 模型列表;空/畸形 base_url → 400;上游 5xx → 400。
func TestEndpointHandlerProbeModels(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-probe-key-123" {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-4.6"},{"id":"kimi-k2"}]}`))
	}))
	t.Cleanup(upstream.Close)

	// 合法请求:返回模型列表。
	body := fmt.Sprintf(`{"base_url":%q,"api_key":"sk-probe-key-123"}`, upstream.URL)
	rec := endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/probe-models", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("probe-models code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	models, _ := data["models"].([]interface{})
	if len(models) != 2 || models[0] != "glm-4.6" {
		t.Fatalf("probe-models 结果 = %#v", data)
	}

	// 空 base_url → 400。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/probe-models", `{"base_url":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 base_url 应 400, got %d", rec.Code)
	}

	// 畸形 base_url(非 http/https)→ 400。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/probe-models", `{"base_url":"ftp://x.example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("畸形 base_url 应 400, got %d", rec.Code)
	}

	// 上游 401(链路通 key 拒)→ 400 带错误摘要(裸探测没有「算可达」语义,
	// 用户拿到摘要先修 key)。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/probe-models",
		fmt.Sprintf(`{"base_url":%q,"api_key":"sk-wrong"}`, upstream.URL))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("key 被拒应 400, got %d", rec.Code)
	}
}

// ---- POST /api/endpoints/activate(托盘组内互斥激活,plan 2026-11-27 Task 2)--

// TestEndpointHandlerActivateContract activate 全状态码契约:
// 200 成功信封(服务层真互斥落库)/ 404 行不存在 / 400 拒绝类(builtin)/
// 405 非 POST / 503 registry 降级。来源闸(401)由家族既有测试覆盖
// (TestEndpointHandlerRequiresDashboardSession 同族惯例)。
func TestEndpointHandlerActivateContract(t *testing.T) {
	svc, h, cookie := newEndpointHandlerHarnessSvc(t)
	for _, e := range []*config.LLMEndpoint{
		{ID: "ep-act-a", Name: "act-a", BaseURL: "http://127.0.0.1:9/a",
			ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 10, Models: "[]"},
		{ID: "ep-act-b", Name: "act-b", BaseURL: "http://127.0.0.1:9/b",
			ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 20, Models: "[]"},
	} {
		if err := svc.CreateEndpoint(e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	// 成功:200 信封 + 服务层真互斥(目标 on、同组其他 off)。
	rec := endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/activate", `{"protocol":"anthropic","id":"ep-act-b"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate code=%d body=%s", rec.Code, rec.Body.String())
	}
	code, data := endpointDecode(t, rec)
	if code != 0 || data["protocol"] != "anthropic" || data["id"] != "ep-act-b" {
		t.Fatalf("成功信封 = code=%d data=%v, want code=0 且 protocol/id 回显", code, data)
	}
	if got, err := svc.GetEndpoint("ep-act-b"); err != nil || !got.Enabled {
		t.Fatalf("目标行应 enabled=1: %v %+v", err, got)
	}
	if got, err := svc.GetEndpoint("ep-act-a"); err != nil || got.Enabled {
		t.Fatalf("同组行应被互斥关闭: %v enabled=%v", err, got.Enabled)
	}

	// 404:目标行不存在(ErrEndpointNotAvailable 哨兵映射)。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/activate", `{"protocol":"anthropic","id":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("行不存在应 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 400:拒绝类(builtin 兜底行不参与互斥,文案透传面板 toast)。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/activate", `{"protocol":"anthropic","id":"builtin-aliang"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("builtin 拒绝应 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 405:非 POST(真 405,不得折成 400/500)。
	req405 := httptest.NewRequest(http.MethodGet, "/api/endpoints/activate", nil)
	req405.AddCookie(cookie)
	rec405 := httptest.NewRecorder()
	h.HandleEndpointActivate(rec405, req405)
	if rec405.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET activate code=%d, want 405", rec405.Code)
	}

	// registry 降级(service nil):照本文件既有端点惯例(endpointService
	// Unavailable → 业务码 201)。实况:CodeServiceUnavailable=201 经
	// ErrorCodeToHTTPStatus 落 HTTP 500(本仓无字面 503 映射;托盘 Task 5
	// 的降级惯例是 available=false 的 200 信封)——本家族九个 handler 在
	// nil service 时行为一致,断言钉住家族真实契约而非任务文本的字面 503。
	h503 := NewEndpointHandler(nil)
	rec503 := endpointDo(t, h503, cookie, http.MethodPost, "/api/endpoints/activate", `{"protocol":"anthropic","id":"ep-act-b"}`)
	code503, _ := endpointDecode(t, rec503)
	if rec503.Code != http.StatusInternalServerError || code503 != 201 {
		t.Fatalf("service nil 应降级响应(HTTP 500/业务码 201), got http=%d code=%d", rec503.Code, code503)
	}

	// 400:请求体缺字段(protocol/id 均必填)。
	rec = endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/activate", `{"protocol":"anthropic"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 id 应 400, got %d", rec.Code)
	}
}

// TestEndpointHandlerActivateSourceGate activate 专用来源闸:回环 ∥ dashboard
// 会话双闸(终审修复批,先于实现写——纯 session 闸下回环无 cookie 必 401,本
// 测试红即坐实缺口)。面板(托盘开 127.0.0.1:56431/panel.html)与 core 同进程
// 同源、从不调 POST /api/dashboard/session 自举会话,纯 session 闸下 radio
// 每次必 401 且面板内无恢复通道。修法与 /api/tray/summary、聊天 WS 握手同档
// 「回环免 cookie ∥ 会话」双闸:回环直通;LAN/远端来源仍须会话。家族其余
// handler(列表/详情/增删改等管理面)保持 endpointRequireSession 不动,由
// TestEndpointHandlerRequiresDashboardSession 继续钉。
func TestEndpointHandlerActivateSourceGate(t *testing.T) {
	// 回环无 cookie → 过闸。用 nil service 证明放行落点:过闸后走降级路径
	// (HTTP 500 + 业务码 201);若闸没放行,看到的将是 401 而非 500。
	hNil := &EndpointHandler{}
	loopback := httptest.NewRequest(http.MethodPost, "/api/endpoints/activate",
		strings.NewReader(`{"protocol":"anthropic","id":"ep-x"}`))
	loopback.RemoteAddr = "127.0.0.1:55555"
	recLoop := httptest.NewRecorder()
	hNil.HandleEndpointActivate(recLoop, loopback)
	codeLoop, _ := endpointDecode(t, recLoop)
	if recLoop.Code != http.StatusInternalServerError || codeLoop != 201 {
		t.Fatalf("回环无 cookie activate = http=%d code=%d, want 过闸后 nil service 降级(HTTP 500/业务码 201);得 401 即双闸未生效",
			recLoop.Code, codeLoop)
	}

	// 非回环无 cookie(httptest.NewRequest 默认 RemoteAddr=192.0.2.1:1234)
	// → 401:免 cookie 只限回环,LAN/远端来源不得外溢。
	remote := httptest.NewRequest(http.MethodPost, "/api/endpoints/activate",
		strings.NewReader(`{"protocol":"anthropic","id":"ep-x"}`))
	recRemote := httptest.NewRecorder()
	hNil.HandleEndpointActivate(recRemote, remote)
	if recRemote.Code != http.StatusUnauthorized {
		t.Fatalf("非回环无 cookie activate = %d, want 401(免 cookie 闸只放回环)", recRemote.Code)
	}

	// 非回环 + 有效 session cookie → 过闸:真 service 正常激活路径走通
	// (200 信封 + 组内互斥落库),证 cookie 分支与回环分支同权。
	svc, h, cookie := newEndpointHandlerHarnessSvc(t)
	if err := svc.CreateEndpoint(&config.LLMEndpoint{
		ID: "ep-gate", Name: "gate-a", BaseURL: "http://127.0.0.1:9/g",
		ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 10, Models: "[]",
	}); err != nil {
		t.Fatalf("create ep-gate: %v", err)
	}
	if err := svc.CreateEndpoint(&config.LLMEndpoint{
		ID: "ep-gate-peer", Name: "gate-b", BaseURL: "http://127.0.0.1:9/h",
		ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 20, Models: "[]",
	}); err != nil {
		t.Fatalf("create ep-gate-peer: %v", err)
	}
	remoteAuthed := httptest.NewRequest(http.MethodPost, "/api/endpoints/activate",
		strings.NewReader(`{"protocol":"anthropic","id":"ep-gate"}`))
	remoteAuthed.AddCookie(cookie)
	recAuthed := httptest.NewRecorder()
	h.HandleEndpointActivate(recAuthed, remoteAuthed)
	if recAuthed.Code != http.StatusOK {
		t.Fatalf("非回环带会话 activate = %d body=%s, want 200(会话分支过闸)", recAuthed.Code, recAuthed.Body.String())
	}
	if got, err := svc.GetEndpoint("ep-gate-peer"); err != nil || got.Enabled {
		t.Fatalf("会话分支应真落库互斥: peer err=%v enabled=%v", err, got != nil && got.Enabled)
	}
}
