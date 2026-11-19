package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MeshCoreForwardHandler 透传契约(D3):真值住 core(56431),56433 侧
// 一律 forward-to-core;core 不可达=common.Error 不可达形态(HTTP 500,
// 与 proxyIfNeeded 一致),绝不本地伪答。
func TestMeshCoreForwardPassthrough(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != "v" {
			t.Errorf("query 未透传: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"beacon_running":true}}`))
	}))
	defer core.Close()

	h := NewMeshCoreForwardHandler()
	h.coreBase = func() string { return core.URL }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/status?k=v", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "beacon_running") {
		t.Fatalf("透传断裂: code=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type 未透传: got %q", ct)
	}
}

// core 回非 200(如方法闸 405)必须原样透传——锁定 WriteHeader(resp.StatusCode)
// 无条件透传契约,本 handler 不对上游状态码做二次裁决。
func TestMeshCoreForwardUpstreamNon200Passthrough(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"code":100,"msg":"Method not allowed"}`))
	}))
	defer core.Close()

	h := NewMeshCoreForwardHandler()
	h.coreBase = func() string { return core.URL }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("上游 405 应原样透传, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMeshCoreForwardCoreDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 立刻关掉制造不可达

	h := NewMeshCoreForwardHandler()
	h.coreBase = func() string { return deadURL }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil))
	// 断言 500 而非 503:common.Error 走 ErrorCodeToHTTPStatus,业务码
	// CodeServiceUnavailable 映射为 HTTP 500——与 proxyIfNeeded 对
	// user-agent 不可达的既有形态逐字节一致(评审实锤 errors.go 映射表),
	// 下游消费者(runbook/AI 面)只需认一种不可达错误形状。
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("core 不可达应回 common.Error 映射的 500, got %d body=%s", w.Code, w.Body.String())
	}
}
