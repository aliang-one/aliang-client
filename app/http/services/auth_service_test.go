package services

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/runtime"
)

func TestAuthServiceRestoreSession_ReturnsSessionExpiredWhenRefreshTokenInvalid(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-auth-service-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))

	defer resetRunServiceHooksForTest()
	defer ResetSharedRunServiceForTest()
	defer auth.StopTokenRefresh()
	defer auth.ResetAuthPersistenceForTest()
	defer config.ResetGlobalConfigForTest()
	defer runtime.ResetGlobalStartupStateForTest()
	defer os.RemoveAll(baseDir)

	auth.ResetAuthPersistenceForTest()
	auth.StopTokenRefresh()
	config.ResetGlobalConfigForTest()
	runtime.ResetGlobalStartupStateForTest()
	ResetSharedRunServiceForTest()

	stoppedProxy := false
	httpStopRunner = func() {
		stoppedProxy = true
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"invalid refresh token","reason":"REFRESH_TOKEN_INVALID"}`))
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})
	config.SetHasLocalUserInfo(true)
	auth.SetCurrentUserInfo(&auth.UserInfo{Username: "stale-user"})
	runtime.GetStartupState().SetFetchSuccess(true)
	runtime.GetStartupState().SetStatus(runtime.READY)
	runService := GetSharedRunService()
	runService.SetCurrentMode("http")
	runService.SetRunning(true)

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "stale-access-token",
		RefreshToken: "stale-refresh-token",
		TokenType:    "Bearer",
		Username:     "stale-user",
		UpdatedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	result := NewAuthService().RestoreSession()

	if got := result["status"]; got != "no_session" {
		t.Fatalf("status = %#v, want no_session", got)
	}
	if got := result["error"]; got != "session_expired" {
		t.Fatalf("error = %#v, want session_expired", got)
	}
	if got := runtime.GetStartupState().GetStatus(); got != runtime.UNCONFIGURED {
		t.Fatalf("startup status = %s, want %s", got, runtime.UNCONFIGURED)
	}
	if got := auth.GetCurrentUserInfo(); got != nil {
		t.Fatalf("current user info = %#v, want nil", got)
	}
	if !stoppedProxy {
		t.Fatal("expected running proxy service to be stopped after session expiration")
	}
	if runService.IsRunning() {
		t.Fatal("expected shared run service to be marked stopped after session expiration")
	}
}

func TestAgentSyncResult_DoesNotBlockOnSync(t *testing.T) {
	// The /api/auth/session (and login/refresh/scan) HTTP response must NOT wait
	// for the PhoneServer sync side-effect — otherwise the page hangs (blank on
	// first load) whenever PhoneServer is slow or unreachable. agentSyncResult
	// must dispatch the sync asynchronously and return immediately.
	orig := agentSyncDispatch
	origHook := EnsureAgentAfterAuthHook
	t.Cleanup(func() { agentSyncDispatch = orig; EnsureAgentAfterAuthHook = origHook })

	fired := make(chan struct{}, 1)
	agentSyncDispatch = func(string) error { fired <- struct{}{}; return nil }
	// In production EnsureAgentAfterAuthHook is wired to agentruntime.EnsureStarted by
	// init(). Stub it to a no-op so the test never spawns a real user-agent child —
	// which would also block the goroutine past the 1s dispatch wait below.
	EnsureAgentAfterAuthHook = func() error { return nil }

	res := agentSyncResult("restore_session")

	if got := res["status"]; got != "async" {
		t.Fatalf("agentSyncResult status = %#v, want \"async\" (must not block the HTTP response on the PhoneServer sync)", got)
	}

	// The dispatch still happens, just off the request path.
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("agent sync dispatch never fired")
	}
}

func TestAuthServiceActivateScanLogin_RejectsMissingRefreshToken(t *testing.T) {
	result := NewAuthService().ActivateScanLogin("scan-session-token", " ")

	if got := result["status"]; got != "failed" {
		t.Fatalf("status = %#v, want failed", got)
	}
	if got := result["error"]; got != "refresh_token_required" {
		t.Fatalf("error = %#v, want refresh_token_required", got)
	}
}
