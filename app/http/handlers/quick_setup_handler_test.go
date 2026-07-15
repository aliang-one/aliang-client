package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuickSetupHandlerRejectsOversizedRequestBeforeDecode(t *testing.T) {
	body := `{"software":"opencode","files":[{"path":"~/.config/opencode/opencode.json","content":"` + strings.Repeat("a", quickSetupRequestMaxBytes) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/apply", strings.NewReader(body))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleApply(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("body = %s, want request size error", rec.Body.String())
	}
}
