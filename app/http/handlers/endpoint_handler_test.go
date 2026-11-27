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
	case strings.HasSuffix(path, "/probe-models"):
		h.HandleProbeModels(rec, r)
	case strings.HasSuffix(path, "/reset-health"):
		h.HandleResetHealth(rec, r)
	case strings.HasSuffix(path, "/set-egress-kernel"):
		h.HandleSetEgressKernel(rec, r)
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
func TestEndpointHandlerActionEndpoints(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)

	// test:内置行拨平台代理入口(本机 56432 常驻 → ok=true + 语义摘要)。
	// 不可达路径由 TestEndpointService_TestProbe 的死址注入单测覆盖。
	rec := endpointDo(t, h, cookie, http.MethodPost, "/api/endpoints/"+storage.BuiltinEndpointID+"/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("builtin test code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	if ok, _ := data["ok"].(bool); !ok {
		t.Fatalf("内置行 test 应 ok=true(平台入口可达): %#v", data)
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

// ---- 二期 Task 6:set-egress-kernel + 视图运行态字段 --------------------------

// endpointEgressMap 读全列表,汇成 id→egress_kernel 快照(断言「至多一行=1」用)。
func endpointEgressMap(t *testing.T, h *EndpointHandler, cookie *http.Cookie) map[string]bool {
	t.Helper()
	rec := endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data := endpointDecode(t, rec)
	out := map[string]bool{}
	for _, it := range data["endpoints"].([]interface{}) {
		m := it.(map[string]interface{})
		v, _ := m["egress_kernel"].(bool)
		out[m["id"].(string)] = v
	}
	return out
}

// TestEndpointHandlerSetEgressKernel 覆盖 spec 二期 §8 的全部契约:
// 事务换绑幂等(设 A→设 B 后 A=false B=true)、builtin 行允许、非 anthropic
// →400(ErrNotAnthropic 映射)、不存在 id→404、set=false 取消(幂等)。
func TestEndpointHandlerSetEgressKernel(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	for _, body := range []map[string]interface{}{
		{"id": "ep-eg-a", "name": "eg-a", "base_url": "https://a.example.com",
			"api_key": "sk-egakey1234567", "protocol_anthropic": true},
		{"id": "ep-eg-b", "name": "eg-b", "base_url": "https://b.example.com",
			"api_key": "sk-egbkey1234567", "protocol_anthropic": true},
		{"id": "ep-eg-oai", "name": "eg-oai", "base_url": "https://o.example.com",
			"api_key": "sk-egokey1234567", "protocol_openai": true},
	} {
		if rec := endpointCreateBody(t, h, cookie, body); rec.Code != http.StatusOK {
			t.Fatalf("create %v code=%d body=%s", body["id"], rec.Code, rec.Body.String())
		}
	}
	setEgress := func(id string, set bool) *httptest.ResponseRecorder {
		return endpointDo(t, h, cookie, http.MethodPost,
			"/api/endpoints/"+id+"/set-egress-kernel", fmt.Sprintf(`{"set":%t}`, set))
	}
	assertOne := func(wantID string) {
		t.Helper()
		m := endpointEgressMap(t, h, cookie)
		trues := 0
		for id, on := range m {
			if on {
				trues++
				if id != wantID {
					t.Fatalf("出口行应是 %s, 实际 %s 也 =true: %v", wantID, id, m)
				}
			}
		}
		if trues != 1 {
			t.Fatalf("全表应恰有一行 egress_kernel=1(%s), got %d: %v", wantID, trues, m)
		}
	}

	// 设 A → A 是唯一出口行。
	if rec := setEgress("ep-eg-a", true); rec.Code != http.StatusOK {
		t.Fatalf("set A code=%d body=%s", rec.Code, rec.Body.String())
	}
	assertOne("ep-eg-a")

	// 事务换绑:设 B → A 自动熄灭,仍恰一行(幂等换绑,不是双出口)。
	if rec := setEgress("ep-eg-b", true); rec.Code != http.StatusOK {
		t.Fatalf("set B code=%d body=%s", rec.Code, rec.Body.String())
	}
	assertOne("ep-eg-b")

	// 重复设同一行:幂等,不变量不破。
	if rec := setEgress("ep-eg-b", true); rec.Code != http.StatusOK {
		t.Fatalf("重复 set B code=%d body=%s", rec.Code, rec.Body.String())
	}
	assertOne("ep-eg-b")

	// builtin 行允许(spec §4:内置链路本身就是 anthropic);设 builtin 后旧行熄灭。
	if rec := setEgress(storage.BuiltinEndpointID, true); rec.Code != http.StatusOK {
		t.Fatalf("set builtin code=%d body=%s", rec.Code, rec.Body.String())
	}
	assertOne(storage.BuiltinEndpointID)

	// 非 anthropic 行 → 400。
	if rec := setEgress("ep-eg-oai", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("openai 行 set code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// 换绑失败不产生副作用:builtin 仍是唯一出口行。
	assertOne(storage.BuiltinEndpointID)

	// 不存在的 id:set=true 与 set=false 都 404(store 对两个方向一致做存在性检查)。
	if rec := setEgress("ep-eg-ghost", true); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost set=true code=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if rec := setEgress("ep-eg-ghost", false); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost set=false code=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	// 取消:set=false → 全表 0 行出口;重复取消幂等。
	if rec := setEgress(storage.BuiltinEndpointID, false); rec.Code != http.StatusOK {
		t.Fatalf("unset builtin code=%d body=%s", rec.Code, rec.Body.String())
	}
	if m := endpointEgressMap(t, h, cookie); func() bool {
		for _, on := range m {
			if on {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("取消后不应有出口行: %v", m)
	}
	if rec := setEgress(storage.BuiltinEndpointID, false); rec.Code != http.StatusOK {
		t.Fatalf("重复 unset code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 缺 set 字段 → 400(不猜默认值:设/取消语义相反,不能静默二选一)。
	rec := endpointDo(t, h, cookie, http.MethodPost,
		"/api/endpoints/ep-eg-a/set-egress-kernel", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 set 字段 code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// GET 视图必须带 6 个运行态字段(spec 二期 §8):egress_kernel/last_used_at/
// total_requests/failed_requests(嵌入结构体 json tag 出面)+
// cooldown_until/rate_limited_until(原 store json:"-" 被 endpointView 补出面,
// store 结构体 tag 不动)。列表与单条同形状。
func TestEndpointHandlerViewRuntimeFields(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-view", "name": "view", "base_url": "https://up.example.com",
		"api_key": "sk-viewkey12345678", "protocol_anthropic": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 摆一份运行态:一次 403 失败(计数路径,total+1/failed+1,阈值 3 不摘除)+
	// 一次内核打点(last_used_at)。证明视图带的是活值不是恒零占位。
	h.service.ReportFailure("ep-view", http.StatusForbidden, "403 Forbidden", 0)
	h.service.MarkUsed("ep-view")

	check := func(data map[string]interface{}, where string) {
		t.Helper()
		for _, key := range []string{"egress_kernel", "last_used_at", "total_requests",
			"failed_requests", "cooldown_until", "rate_limited_until"} {
			if _, has := data[key]; !has {
				t.Fatalf("%s 缺字段 %s: %v", where, key, data)
			}
		}
		if v, _ := data["egress_kernel"].(bool); v {
			t.Fatalf("%s egress_kernel 应为 false: %v", where, data)
		}
		if got, _ := data["total_requests"].(float64); got != 1 {
			t.Fatalf("%s total_requests = %v, want 1(403 归计数路径)", where, got)
		}
		if got, _ := data["failed_requests"].(float64); got != 1 {
			t.Fatalf("%s failed_requests = %v, want 1", where, got)
		}
		if got, _ := data["last_used_at"].(float64); got <= 0 {
			t.Fatalf("%s last_used_at = %v, want >0(打点后)", where, got)
		}
		// 一次失败未达阈值 3:健康态不变,两路冷却仍 0。
		if got, _ := data["cooldown_until"].(float64); got != 0 {
			t.Fatalf("%s cooldown_until = %v, want 0", where, got)
		}
		if got, _ := data["rate_limited_until"].(float64); got != 0 {
			t.Fatalf("%s rate_limited_until = %v, want 0", where, got)
		}
	}

	// 列表条目。
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints", "")
	_, data := endpointDecode(t, rec)
	for _, it := range data["endpoints"].([]interface{}) {
		m := it.(map[string]interface{})
		if m["id"] == "ep-view" {
			check(m, "列表条目")
		}
	}
	// 单条。
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints/ep-view", "")
	_, data = endpointDecode(t, rec)
	check(data, "单条")
}

// PUT enabled 0→1 触发激活清零(spec 二期 §5.6,Task 2 已在 service 实现):
// 本用例断言 handler 的 GET→merge→PUT 路径贯通该逻辑——响应视图与再 GET 都
// 是清零后的形状,且生命周期计数不清。
func TestEndpointHandlerUpdateEnabledActivationClearsHealth(t *testing.T) {
	h, cookie := newEndpointHandlerHarness(t)
	rec := endpointCreateBody(t, h, cookie, map[string]interface{}{
		"id": "ep-act", "name": "act", "base_url": "https://up.example.com",
		"api_key": "sk-actkey123456789", "protocol_anthropic": true, "enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 摆陈旧健康态(默认策略阈值 3:三次 5xx → disabled+冷却)+计数。
	for i := 0; i < 3; i++ {
		h.service.ReportFailure("ep-act", http.StatusInternalServerError, "boom", 0)
	}
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints/ep-act", "")
	_, data := endpointDecode(t, rec)
	if data["health_status"] != "disabled" {
		t.Fatalf("前置:三次失败后应 disabled, got %v (%v)", data["health_status"], data)
	}
	if got, _ := data["cooldown_until"].(float64); got <= 0 {
		t.Fatalf("前置:disabled 行应有冷却, cooldown_until=%v", got)
	}

	// PUT enabled 0→1:响应视图即清零后形状(service 同步回写 merged 行)。
	rec = endpointDo(t, h, cookie, http.MethodPut, "/api/endpoints/ep-act", `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put enabled code=%d body=%s", rec.Code, rec.Body.String())
	}
	_, data = endpointDecode(t, rec)
	if data["health_status"] != "ok" {
		t.Fatalf("PUT 响应视图 health_status = %v, want ok(激活清零贯通)", data["health_status"])
	}
	if got, _ := data["cooldown_until"].(float64); got != 0 {
		t.Fatalf("PUT 响应视图 cooldown_until = %v, want 0", got)
	}

	// 再 GET:落库形状一致;计数只累计不清零(三次失败 total=3 failed=3)。
	rec = endpointDo(t, h, cookie, http.MethodGet, "/api/endpoints/ep-act", "")
	_, data = endpointDecode(t, rec)
	if data["health_status"] != "ok" || data["cooldown_until"] != float64(0) {
		t.Fatalf("激活后再 GET health_status=%v cooldown_until=%v, want ok/0",
			data["health_status"], data["cooldown_until"])
	}
	if got, _ := data["total_requests"].(float64); got != 3 {
		t.Fatalf("激活清零不得动计数, total_requests = %v, want 3", got)
	}
}
