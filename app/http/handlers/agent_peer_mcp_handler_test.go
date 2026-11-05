package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
)

// POST 载荷契约(2026-11-05 per-kernel 细分开关):body 必须是
// {"peer_mcp":{...}} 对象——nil/旧布尔形状一律 400。合法对象分支会落到
// service 写真实家目录(handlers 包 stub 不了 services.peerMCPHome),
// 其行为已由 services 层用例覆盖,这里只钉 400/405 关卡。
// t.Setenv(AgentRuntimeEnv)=1 让 proxyIfNeeded 直通(零值 handler 无代理客户端)。
func TestHandlePeerMcpPostRequiresPeerMCPObject(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}

	// 空体
	req := httptest.NewRequest("POST", "/api/agent/peer/mcp", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.HandlePeerMcp(rec, req)
	if rec.Code != 400 {
		t.Fatalf("空体 code = %d, want 400", rec.Code)
	}

	// 旧布尔形状(契约已废弃)
	req = httptest.NewRequest("POST", "/api/agent/peer/mcp", strings.NewReader(`{"enable":true}`))
	rec = httptest.NewRecorder()
	h.HandlePeerMcp(rec, req)
	if rec.Code != 400 {
		t.Fatalf("旧布尔形状 code = %d, want 400", rec.Code)
	}
}

func TestHandlePeerMcpMethodNotAllowed(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}
	req := httptest.NewRequest("DELETE", "/api/agent/peer/mcp", nil)
	rec := httptest.NewRecorder()
	h.HandlePeerMcp(rec, req)
	if rec.Code != 405 {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
