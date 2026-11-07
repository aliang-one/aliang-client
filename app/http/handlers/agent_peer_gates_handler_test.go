package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
)

// 设备门禁开关面(2026-11-07):本机 peer_exec_enabled / peer_fetch_enabled 的
// GET 读 + POST 写。service 行为(读过滤/写部分更新)由 services 层用例覆盖,
// 这里只钉 400(空体/双 nil)/405 关卡,同 HandlePeerMcp 测试纪律。
// t.Setenv(AgentRuntimeEnv)=1 让 proxyIfNeeded 直通(零值 handler 无代理客户端)。
func TestHandlePeerGatesPostRequiresBooleanKeys(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}

	// 空体
	req := httptest.NewRequest("POST", "/api/agent/peer/gates", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.HandlePeerGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("空体 code = %d, want 400", rec.Code)
	}

	// 双 nil=无键可落(契约:至少一个显式布尔键)
	req = httptest.NewRequest("POST", "/api/agent/peer/gates", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	h.HandlePeerGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("双 nil code = %d, want 400", rec.Code)
	}

	// 旧形状(裸布尔非指针)也不收:契约是 {peer_exec_enabled?,peer_fetch_enabled?}
	req = httptest.NewRequest("POST", "/api/agent/peer/gates", strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	h.HandlePeerGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("未知键形状 code = %d, want 400", rec.Code)
	}
}

func TestHandlePeerGatesMethodNotAllowed(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}
	req := httptest.NewRequest("DELETE", "/api/agent/peer/gates", nil)
	rec := httptest.NewRecorder()
	h.HandlePeerGates(rec, req)
	if rec.Code != 405 {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
