package handlers

import (
	"net/http/httptest"
	"testing"
)

// 405 / 缺 sessionId 两个分支在触碰 service 之前返回，零值 handler 即可测。
func TestHandleTerminalScrollbackMethodNotAllowed(t *testing.T) {
	h := &AgentHandler{}
	req := httptest.NewRequest("POST", "/api/agent/terminal/x/scrollback", nil)
	rec := httptest.NewRecorder()
	h.HandleTerminalScrollback(rec, req)
	if rec.Code != 405 {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestHandleTerminalScrollbackRequiresSessionID(t *testing.T) {
	h := &AgentHandler{}
	req := httptest.NewRequest("GET", "/api/agent/terminal//scrollback", nil)
	rec := httptest.NewRecorder()
	h.HandleTerminalScrollback(rec, req)
	if rec.Code != 400 {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}
