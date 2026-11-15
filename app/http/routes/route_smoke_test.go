// route_smoke_test.go — 客户端连接状态功能两个新路由的冒烟钉：经完整
// RegisterRoutes mux 验证真实可达、方法分流与未运行态响应形状（与单
// handler 测试互补：这里锁的是「注册了」且「方法写对」，防未来重构路由
// 表时静默丢注册——2026-10-21 双注册 404 同族事故的护栏）。
package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIngressAndSSHTestRoutesSmoke(t *testing.T) {
	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// GET /api/status/ingress：测试进程未跑 reconcile → HTTP 200 + ok=false
	// + 两列表恒非 null（前端「未运行」态渲染的数据契约）。
	resp, err := http.Get(srv.URL + "/api/status/ingress")
	if err != nil {
		t.Fatalf("GET ingress: %v", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OK     bool          `json:"ok"`
			Active []interface{} `json:"active"`
			Closed []interface{} `json:"closed"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != 200 || envelope.Code != 0 {
		t.Fatalf("status=%d code=%d, want 200/0", resp.StatusCode, envelope.Code)
	}
	if envelope.Data.OK || envelope.Data.Active == nil || envelope.Data.Closed == nil {
		t.Fatalf("not-running shape broken: %+v", envelope.Data)
	}

	// POST /api/proxy/ssh/test 空载荷 → 400（完整性校验,不打网络）。
	resp2, err := http.Post(srv.URL+"/api/proxy/ssh/test", "application/json",
		bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST test: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 400 {
		t.Fatalf("empty payload status = %d, want 400", resp2.StatusCode)
	}

	// GET /api/proxy/ssh/test → 405（POST-only,真实 HTTP 码非业务码映射）。
	resp3, err := http.Get(srv.URL + "/api/proxy/ssh/test")
	if err != nil {
		t.Fatalf("GET test: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 405 {
		t.Fatalf("GET on POST-only = %d, want 405", resp3.StatusCode)
	}
}
