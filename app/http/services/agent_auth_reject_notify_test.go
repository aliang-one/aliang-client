package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
)

// TestNotifyOwnerAuthRejectedTransitionEdge locks in the agent→owner
// credential-rejected notification contract: exactly one POST per ok→rejected
// transition edge (re-armed by register success), the full four-field body,
// and silent no-ops for an empty owner address.
func TestNotifyOwnerAuthRejectedTransitionEdge(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Setenv(AgentRuntimeEnv, "1")
	t.Setenv(SessionOwnerAddrEnv, "")
	resetOwnerAuthRejectedNotify()
	originalShared := sharedAgentService
	t.Cleanup(func() {
		resetOwnerAuthRejectedNotify()
		cache.ResetCacheDirForTest()
		sharedAgentServiceMu.Lock()
		sharedAgentService = originalShared
		sharedAgentServiceMu.Unlock()
	})

	var mu sync.Mutex
	var payloads []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/agent-auth-rejected" {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		var body map[string]interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		mu.Lock()
		payloads = append(payloads, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"applied": true})
	}))
	defer server.Close()

	received := func() []map[string]interface{} {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]interface{}, len(payloads))
		copy(out, payloads)
		return out
	}

	// 1) First rejection on the edge POSTs the full contract body.
	t.Setenv(SessionOwnerAddrEnv, server.URL)
	NotifyOwnerAuthRejected("test_register_rejected")

	got := received()
	if len(got) != 1 {
		t.Fatalf("owner received %d notifications after first rejection, want 1", len(got))
	}
	first := got[0]
	if first["reason"] != "test_register_rejected" {
		t.Fatalf("reason = %v, want test_register_rejected", first["reason"])
	}
	observedAt, ok := first["observed_at"].(float64)
	if !ok || observedAt <= 0 {
		t.Fatalf("observed_at = %v, want a positive unix timestamp", first["observed_at"])
	}
	if age := time.Since(time.Unix(int64(observedAt), 0)); age < -time.Minute || age > 5*time.Minute {
		t.Fatalf("observed_at = %v, want within 5 minutes of now (age %s)", first["observed_at"], age)
	}
	generation, ok := first["generation"].(float64)
	if !ok || generation < 0 {
		t.Fatalf("generation = %v, want a non-negative integer", first["generation"])
	}
	if _, ok := first["device_id"].(string); !ok {
		t.Fatalf("device_id = %v, want a string (empty allowed)", first["device_id"])
	}

	// 2) A consecutive rejection without an intervening register success must
	// not notify again (transition-edge idempotence).
	NotifyOwnerAuthRejected("test_register_rejected")
	if got := received(); len(got) != 1 {
		t.Fatalf("owner received %d notifications after repeat rejection, want still 1", len(got))
	}

	// 3) Re-arm the edge (what register success does) → the next rejection
	// notifies again.
	resetOwnerAuthRejectedNotify()
	NotifyOwnerAuthRejected("test_register_rejected")
	if got := received(); len(got) != 2 {
		t.Fatalf("owner received %d notifications after edge reset, want 2", len(got))
	}

	// 4) Empty owner address: silent no-op — no panic, no send.
	resetOwnerAuthRejectedNotify()
	t.Setenv(SessionOwnerAddrEnv, "")
	NotifyOwnerAuthRejected("test_register_rejected")
	if got := received(); len(got) != 2 {
		t.Fatalf("owner received %d notifications with empty owner address, want still 2", len(got))
	}
}

// newAuthRejectedCountingOwner 起一个假 session owner：只接受
// POST /api/auth/agent-auth-rejected，按 status 应答并统计收到次数。
func newAuthRejectedCountingOwner(status int) (*httptest.Server, func() int) {
	var mu sync.Mutex
	var count int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/agent-auth-rejected" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		http.Error(w, "modeled response", status)
	}))
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestNotifyOwnerAuthRejectedEdgeConsumptionOnFailure locks in the edge
// consumption semantics: the ok→rejected transition edge is consumed only on
// successful delivery (2xx) or a permanent 4xx contract rejection. Transient
// failures — owner unreachable (transport error) or 5xx — retain the edge so
// the next 401 retry path notifies again; a permanent 4xx consumes the edge and
// repeated calls do not re-POST.
func TestNotifyOwnerAuthRejectedEdgeConsumptionOnFailure(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Setenv(AgentRuntimeEnv, "1")
	t.Setenv(SessionOwnerAddrEnv, "")
	resetOwnerAuthRejectedNotify()
	originalShared := sharedAgentService
	t.Cleanup(func() {
		resetOwnerAuthRejectedNotify()
		cache.ResetCacheDirForTest()
		sharedAgentServiceMu.Lock()
		sharedAgentService = originalShared
		sharedAgentServiceMu.Unlock()
	})

	t.Run("owner_unreachable_edge_retained", func(t *testing.T) {
		// Owner address points at an already-closed server: immediate
		// connection refused. The call must not panic and must NOT consume
		// the edge.
		dead, _ := newAuthRejectedCountingOwner(http.StatusOK)
		dead.Close()
		t.Setenv(SessionOwnerAddrEnv, dead.URL)
		resetOwnerAuthRejectedNotify()
		NotifyOwnerAuthRejected("test_register_rejected")

		// Swap in a live owner and call again WITHOUT an edge reset: the
		// retained edge must allow the retry to deliver exactly once.
		live, liveCount := newAuthRejectedCountingOwner(http.StatusOK)
		defer live.Close()
		t.Setenv(SessionOwnerAddrEnv, live.URL)
		NotifyOwnerAuthRejected("test_register_rejected")
		if got := liveCount(); got != 1 {
			t.Fatalf("live owner received %d notifications after unreachable retry, want 1", got)
		}
	})

	t.Run("owner_5xx_edge_retained", func(t *testing.T) {
		// A 503 from the owner is a transient server-side failure: the edge
		// must be retained.
		failing, failCount := newAuthRejectedCountingOwner(http.StatusServiceUnavailable)
		defer failing.Close()
		t.Setenv(SessionOwnerAddrEnv, failing.URL)
		resetOwnerAuthRejectedNotify()
		NotifyOwnerAuthRejected("test_register_rejected")
		if got := failCount(); got != 1 {
			t.Fatalf("failing owner received %d notifications, want 1", got)
		}

		// Swap in a healthy owner and call again WITHOUT an edge reset.
		live, liveCount := newAuthRejectedCountingOwner(http.StatusOK)
		defer live.Close()
		t.Setenv(SessionOwnerAddrEnv, live.URL)
		NotifyOwnerAuthRejected("test_register_rejected")
		if got := liveCount(); got != 1 {
			t.Fatalf("live owner received %d notifications after 503 retry, want 1", got)
		}
	})

	t.Run("owner_4xx_edge_consumed", func(t *testing.T) {
		// A 400 is a permanent contract-level rejection: retrying is
		// pointless, so the edge is consumed even though delivery "failed".
		owner, count := newAuthRejectedCountingOwner(http.StatusBadRequest)
		defer owner.Close()
		t.Setenv(SessionOwnerAddrEnv, owner.URL)
		resetOwnerAuthRejectedNotify()
		NotifyOwnerAuthRejected("test_register_rejected")
		if got := count(); got != 1 {
			t.Fatalf("owner received %d notifications after first call, want 1", got)
		}

		// Same address, no edge reset: the consumed edge suppresses further
		// POSTs.
		NotifyOwnerAuthRejected("test_register_rejected")
		if got := count(); got != 1 {
			t.Fatalf("owner received %d notifications after repeat call, want still 1", got)
		}
	})
}

// TestNotifyOwnerAuthRejectedSkipsNonAgentRuntime locks in that the session
// owner process itself (IsUserAgentRuntime()==false) never notifies — it runs
// its own local recovery chain instead.
func TestNotifyOwnerAuthRejectedSkipsNonAgentRuntime(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Setenv(AgentRuntimeEnv, "0")
	t.Cleanup(cache.ResetCacheDirForTest)

	var mu sync.Mutex
	var count int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"applied": true})
	}))
	defer server.Close()

	t.Setenv(SessionOwnerAddrEnv, server.URL)
	resetOwnerAuthRejectedNotify()
	t.Cleanup(resetOwnerAuthRejectedNotify)

	NotifyOwnerAuthRejected("test_register_rejected")

	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Fatalf("owner received %d notifications in non-agent runtime, want 0", count)
	}
}
