package services

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCallAgentServerRetriesOnceWithRefreshedHeader(t *testing.T) {
	agentChannelHealthInstance.reset()

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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if agentChannelHealthInstance.allow(srv.URL) {
		t.Fatal("endpoint must be in backoff after repeated 401")
	}
}
