package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/storage"
)

// ---- 脚手架 -----------------------------------------------------------------

// newEndpointHandlerHarness 起一个 :memory: 独立库的 endpoint service(经
// services 包测试注入缝)+ handler,并签发一枚真实 dashboard session cookie。
// 完全不碰真实数据目录,也不污染 GetEndpointService 单例。
func newEndpointHandlerHarness(t *testing.T) (*EndpointHandler, *http.Cookie) {
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
	return NewEndpointHandler(svc), cookie
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
	case strings.HasSuffix(path, "/reset-health"):
		h.HandleResetHealth(rec, r)
	case method == http.MethodGet && strings.HasSuffix(path, "/api/endpoints"):
		h.HandleList(rec, r)
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
func TestEndpointHandlerActionEndpoints(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	// test:内置行无独立探测目标 → 200 + ok=false + 摘要(不是 5xx)。
	rec := endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/"+storage.BuiltinEndpointID+"/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("builtin test code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	if ok, _ := data["ok"].(bool); ok {
		t.Fatalf("内置行 test 应 ok=false: %#v", data)
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
