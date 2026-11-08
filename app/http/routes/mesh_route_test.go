package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMeshLANPeersRouteRegistered /api/mesh/lan_peers 由 RegisterRoutes
// 注册:非 404、200、信封 data.peers 为空数组(beacon 未装配时合法态)。
// 环境变量同 TestAgentVibeSessionRoutesRegistered(构造 Agents 服务侧隔离)。
func TestMeshLANPeersRouteRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	req := httptest.NewRequest(http.MethodGet, "/api/mesh/lan_peers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route /api/mesh/lan_peers returned 404 — not registered by RegisterRoutes")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("route code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Peers []json.RawMessage `json:"peers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Data.Peers == nil {
		t.Fatalf("未装配 beacon 时 peers 应为空数组而非 null (body=%s)", rec.Body.String())
	}
}
