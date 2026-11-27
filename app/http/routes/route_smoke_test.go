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

// TestEndpointActivateRoutesSmoke 托盘激活路由的注册可达性冒烟(plan
// 2026-11-27 endpoint-pin-activation 评审增益;终审修复批来源闸改「回环 ∥
// dashboard 会话」双闸后语义同步):经完整 RegisterRoutes mux 验证「POST
// activate 精确注册在了、来源闸在 handler 内生效」。
//   - POST /api/endpoints/activate 经真网络(httptest.NewServer 的客户端
//     RemoteAddr 恒回环)无 cookie → 非 401 且非 405:回环免 cookie 直通是
//     面板受支持形态(面板与 core 同进程同源、从不自举 dashboard 会话,纯
//     session 闸下 radio 每次必 401;与 /api/tray/summary、聊天 WS 握手双闸
//     同档)。非 405 同时钉注册形态——注册行丢失时该路径已无任何 POST 可全
//     匹配的模式,只剩路径失配的 405/404。实测落点:本进程 registry 单例
//     seed 成功,「ep-x」不存在 → ErrEndpointNotAvailable → HTTP 404 信封
//     (registry 不可用的进程则落 nil service 降级 HTTP 500/业务码 201)——
//     两者都证明闸已放行并到达 service 层,故只闸 401/405 两个坏形态,不
//     耦合 registry 可用性。
//   - 非回环来源无 cookie → 401:httptest.NewRequest 手工构造
//     RemoteAddr=192.0.2.1:1234 直打 mux.ServeHTTP(不经网络),免 cookie 不
//     外溢到 LAN/远端——双闸的另一条腿。
//   - GET /api/endpoints/activate 无 cookie → 401:Go ServeMux 方法前缀
//     失配时回退 {id} 通配(实验实证:落 HandleGet,非 405/404),而
//     HandleGet 走家族既有 session 闸(管理面,不随 activate 双闸改动)先于
//     id 查找 → 401。断言鉴权类响应而非 404,钉死「activate 路径不会透传
//     成 404 语义」;若有人把精确注册改成裸路径(无 method 前缀),GET 会撞
//     进 HandleEndpointActivate 的方法闸 → 405,本断言即红——注册形态被锁
//     在 method 前缀精确模式。
func TestEndpointActivateRoutesSmoke(t *testing.T) {
	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// POST(真网络回环)无 cookie → 非 401/非 405(双闸放行回环 + 注册可达;
	// 闸先于 body 解码,带 body 是为顺带证明闸在解码前短路)。
	resp, err := http.Post(srv.URL+"/api/endpoints/activate", "application/json",
		bytes.NewBufferString(`{"protocol":"anthropic","id":"ep-x"}`))
	if err != nil {
		t.Fatalf("POST activate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("POST activate 回环无 cookie = 401, want 免 cookie 过闸(回环∥会话双闸,面板受支持形态)")
	}
	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("POST activate = 405, 精确注册丢失(路径失配才撞得出 405)")
	}

	// 非回环来源无 cookie → 401(手工请求直打 mux,不经网络;免 cookie 只限
	// 回环,LAN/远端来源仍须会话)。
	remote := httptest.NewRequest(http.MethodPost, "/api/endpoints/activate",
		bytes.NewBufferString(`{"protocol":"anthropic","id":"ep-x"}`))
	remote.RemoteAddr = "192.0.2.1:1234"
	recRemote := httptest.NewRecorder()
	mux.ServeHTTP(recRemote, remote)
	if recRemote.Code != http.StatusUnauthorized {
		t.Fatalf("非回环无 cookie POST activate = %d, want 401(双闸拦远端)", recRemote.Code)
	}

	// GET 无 cookie → 401(GET 侧经 {id} 通配落 HandleGet,家族 session 闸
	// 先于 id 查找;出现 404/405 即注册形态被改动)。
	resp2, err := http.Get(srv.URL + "/api/endpoints/activate")
	if err != nil {
		t.Fatalf("GET activate: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET activate 未登录 = %d, want 401(鉴权类响应,证 activate 路径不透传成 404)", resp2.StatusCode)
	}
}
