package handlers

import (
	"net/http/httptest"
	"testing"
)

// 405 关卡:错误方法在触碰 service/proxy 之前就返回——零值 handler 即可钉住
// (service 为 nil,若方法检查被挪到 proxy 之后会在此炸,测试随之变红)。
// 同时锁住「common.Error 收业务码(100-299),裸 http 状态须用 http.Error」
// 的陷阱:直传 http.StatusMethodNotAllowed 给 common.Error 会被映射成 500。
func TestHandleAliangMCPRegistryMethodNotAllowed(t *testing.T) {
	h := &AgentHandler{}
	req := httptest.NewRequest("POST", "/api/agent/mcp", nil)
	rec := httptest.NewRecorder()
	h.HandleAliangMCPRegistry(rec, req)
	if rec.Code != 405 {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
