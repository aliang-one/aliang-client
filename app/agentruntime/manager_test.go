package agentruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/processor/config"
)

func TestWaitForCurrentAgentAPIRetriesUntilProtocolIsReady(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocolPath {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&attempts, 1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": models.DefaultAgentProtocolContract(),
		})
	}))
	defer server.Close()

	originalAddr := config.DefaultUserAgentAddr
	config.DefaultUserAgentAddr = strings.TrimPrefix(server.URL, "http://")
	t.Cleanup(func() {
		config.DefaultUserAgentAddr = originalAddr
	})

	gotAttempts, err := waitForCurrentAgentAPI(time.Second, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForCurrentAgentAPI() error = %v", err)
	}
	if gotAttempts != 3 {
		t.Fatalf("waitForCurrentAgentAPI() attempts = %d, want 3", gotAttempts)
	}
}

func TestNeedsAuthenticatedSyncOnlyForRecoverableDisabledStates(t *testing.T) {
	status := models.AgentStatusResponse{SyncStatus: "logout"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != capabilityPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": status,
		})
	}))
	defer server.Close()

	originalAddr := config.DefaultUserAgentAddr
	config.DefaultUserAgentAddr = strings.TrimPrefix(server.URL, "http://")
	t.Cleanup(func() { config.DefaultUserAgentAddr = originalAddr })

	if !NeedsAuthenticatedSync(time.Second) {
		t.Fatal("logout state should request authenticated reconciliation")
	}
	status.SyncStatus = "auth_expired"
	if !NeedsAuthenticatedSync(time.Second) {
		t.Fatal("auth_expired state should request authenticated reconciliation")
	}
	status.SyncStatus = "disabled"
	if NeedsAuthenticatedSync(time.Second) {
		t.Fatal("manual disable must not request authenticated reconciliation")
	}
	status.SyncStatus = "logout"
	status.Enabled = true
	if NeedsAuthenticatedSync(time.Second) {
		t.Fatal("enabled Agent must not request authenticated reconciliation")
	}
}
