package services

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCallAgentServerRetriesOnceWithRefreshedHeader(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	oldRefresher := agentChannelRefreshHeader
	agentChannelRefreshHeader = func() (string, error) { return "Bearer fresh", nil }
	t.Cleanup(func() { agentChannelRefreshHeader = oldRefresher })

	svc := &AgentService{client: srv.Client()}
	raw, err := svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, map[string]interface{}{"a": 1}, "Bearer stale")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty response body")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (401 then refreshed retry)", got)
	}
}

func TestCallAgentServerSetsEndpointBackoffAfterRepeated401(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	oldRefresher := agentChannelRefreshHeader
	agentChannelRefreshHeader = func() (string, error) { return "", nil }
	t.Cleanup(func() { agentChannelRefreshHeader = oldRefresher })

	svc := &AgentService{client: srv.Client()}
	for i := 0; i < agentChannelAlertThreshold; i++ {
		_, _ = svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, nil, "Bearer stale")
	}
	if agentChannelHealthInstance.allow(srv.URL, "Bearer stale") {
		t.Fatal("endpoint must be in backoff after repeated 401")
	}
	// 门面的 allow 闸门必须承重：首个 401 落退避后，循环内第 2、3 次已被闸门
	// 挡下（上游只收到 1 次请求），此处再经门面调用必须被 typed 退避错误直接
	// 拒绝，且上游计数不得增加。
	hitsAfterLoop := atomic.LoadInt32(&calls)
	_, err := svc.callAgentServerWithAuthorization(http.MethodGet, srv.URL, nil, "Bearer stale")
	var backoff agentChannelBackoffError
	if !errors.As(err, &backoff) {
		t.Fatalf("err = %v, want agentChannelBackoffError", err)
	}
	if hits := atomic.LoadInt32(&calls); hits != hitsAfterLoop {
		t.Fatalf("upstream calls = %d, want %d（退避期内不得再发请求）", hits, hitsAfterLoop)
	}
}

func TestAgentChannelBackoffBypassedAfterCredentialRotation(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	const endpoint = "https://api.example.com/rotated"
	for i := 0; i < agentChannelAlertThreshold; i++ {
		agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	}
	if agentChannelHealthInstance.allow(endpoint, "Bearer stale") {
		t.Fatalf("同凭据仍应被退避挡住")
	}
	if agentChannelHealthInstance.allow(endpoint, "") {
		t.Fatal("空凭据不得清掉退避")
	}
	if !agentChannelHealthInstance.allow(endpoint, "Bearer fresh") {
		t.Fatalf("凭据轮转后应放行")
	}
	if !agentChannelHealthInstance.allow(endpoint, "Bearer stale") {
		t.Fatalf("放行后应清掉该端点退避")
	}
}
