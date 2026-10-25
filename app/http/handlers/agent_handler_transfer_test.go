package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本地触发端点加固（审计 P2）：强制 loopback + 拒绝跨源 Origin。
func TestHandleTransferCreateLoopbackOnly(t *testing.T) {
	h := NewAgentHandler(nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/transfers", strings.NewReader(`{"source_path":"/tmp/a","target_device_id":"d1"}`))
	req.RemoteAddr = "10.0.0.5:1234" // 非 loopback
	h.HandleTransferCreate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-loopback must be 403, got %d", rr.Code)
	}
}

func TestHandleTransferCreateRejectsCrossOrigin(t *testing.T) {
	h := NewAgentHandler(nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/transfers", strings.NewReader(`{"source_path":"/tmp/a","target_device_id":"d1"}`))
	req.RemoteAddr = "127.0.0.1:55555"
	req.Header.Set("Origin", "https://evil.example.com") // 浏览器 CSRF 盲触发形态
	h.HandleTransferCreate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin must be 403, got %d", rr.Code)
	}
}

func TestHandleTransferCreateAllowsLoopbackScript(t *testing.T) {
	h := NewAgentHandler(nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/transfers", strings.NewReader(`{"source_path":"/tmp/a","target_device_id":"d1"}`))
	req.RemoteAddr = "127.0.0.1:55555" // curl/脚本：无 Origin
	h.HandleTransferCreate(rr, req)
	if rr.Code == http.StatusForbidden {
		t.Fatal("loopback script without Origin must not be rejected by the fence")
	}
	// 走到转发段（无服务端配置时会以非 403 的服务错误收场），围栏本身放行即视为通过
}
