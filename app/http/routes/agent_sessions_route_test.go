package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAgentVibeSessionRoutesRegistered verifies RegisterRoutes wires up the
// new read-only vibe session endpoints. ALIANG_USER_AGENT_RUNTIME=1 makes the
// test process handle agent requests directly (no proxy to an external
// runtime), so the test is hermetic.
func TestAgentVibeSessionRoutesRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	// sessions 列表与 tools 都应直接处理（非 404）；session 详情缺 id 时为 400。
	for _, tc := range []struct {
		path     string
		notFound bool
	}{
		{"/api/agent/sessions", false},
		{"/api/agent/session", false},
		{"/api/agent/tools", false},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("route %s returned 404 — not registered by RegisterRoutes", tc.path)
		}
	}
}
