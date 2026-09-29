package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// setupReleaseTestEnv points AgentServer at an httptest server recording
// /api/v1/agent/devices/release hits, and seeds the persisted auth store with
// a session so effectiveUserAuthorizationLocked resolves a credential in
// non-user-agent (session-owner) mode — the same fallback the register path
// uses.
func setupReleaseTestEnv(t *testing.T) <-chan *http.Request {
	t.Helper()
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	if err := auth.SaveUserInfo(&auth.UserInfo{AccessToken: "logout-jwt", ID: 7, Email: "a@x.com"}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	releaseHits := make(chan *http.Request, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/devices/release" {
			select {
			case releaseHits <- r.Clone(r.Context()):
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]string{"status": "released"}})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})
	return releaseHits
}

func newReleaseTestService(t *testing.T) *AgentService {
	t.Helper()
	svc := NewAgentService()
	svc.mu.Lock()
	svc.state.DeviceID = "dev_test"
	svc.mu.Unlock()
	return svc
}

func assertNoRelease(t *testing.T, releaseHits <-chan *http.Request) {
	t.Helper()
	select {
	case req := <-releaseHits:
		t.Fatalf("unexpected release POST %s", req.URL.Path)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestLogoutDisableDispatchesDeviceRelease(t *testing.T) {
	releaseHits := setupReleaseTestEnv(t)
	svc := newReleaseTestService(t)

	svc.DisableWithReason("logout")

	select {
	case req := <-releaseHits:
		if got := req.Header.Get("Authorization"); got != "Bearer logout-jwt" {
			t.Errorf("release Authorization = %q, want Bearer logout-jwt", got)
		}
		if got := req.Header.Get("X-Aliang-Device-ID"); got != "dev_test" {
			t.Errorf("release X-Aliang-Device-ID = %q, want dev_test", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("logout did not dispatch the device release POST")
	}

	// Logout reaches disableWithReasonMessage twice (session-event forward +
	// /api/agent/disable retry). The fire-once guard must collapse them.
	svc.DisableWithReason("logout")
	assertNoRelease(t, releaseHits)
}

func TestNonLogoutDisableDoesNotRelease(t *testing.T) {
	releaseHits := setupReleaseTestEnv(t)
	svc := newReleaseTestService(t)

	// auth_expired / device_unbound / manual are NOT user intent — no release.
	svc.DisableWithReason("auth_expired")
	assertNoRelease(t, releaseHits)
	svc.DisableWithReason("device_unbound")
	assertNoRelease(t, releaseHits)
}

func TestLogoutReleaseServerDownStillDisables(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	if err := auth.SaveUserInfo(&auth.UserInfo{AccessToken: "logout-jwt", ID: 7, Email: "a@x.com"}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	// Closed server: the release POST fails in the background goroutine and
	// must be swallowed — logout itself completes and the state clears.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	svc := newReleaseTestService(t)
	svc.mu.Lock()
	svc.state.Registered = true
	svc.mu.Unlock()

	svc.DisableWithReason("logout")

	svc.mu.Lock()
	enabled := svc.state.Enabled
	registered := svc.state.Registered
	message := svc.state.LastSyncMessage
	svc.mu.Unlock()
	if enabled || registered {
		t.Fatalf("logout did not clear agent state: enabled=%t registered=%t", enabled, registered)
	}
	_ = message
}
