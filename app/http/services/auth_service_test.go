package services

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
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

func TestAuthServiceLogoutStopsHTTPBeforeRemoteRevokeCompletes(t *testing.T) {
	defer resetRunServiceHooksForTest()
	defer ResetSharedRunServiceForTest()
	auth.ResetAuthPersistenceForTest()
	auth.StopTokenRefresh()
	ResetSharedRunServiceForTest()

	originalRemoteLogout := remoteLogoutDispatch
	originalAgentBaseURL := localUserAgentBaseURL
	t.Cleanup(func() {
		remoteLogoutDispatch = originalRemoteLogout
		localUserAgentBaseURL = originalAgentBaseURL
		auth.ResetAuthPersistenceForTest()
	})

	agentRequests := make(chan string, 4)
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentRequests <- r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer agentServer.Close()
	localUserAgentBaseURL = func() string { return agentServer.URL }

	remoteStarted := make(chan string, 1)
	remoteRelease := make(chan struct{})
	remoteFinished := make(chan struct{})
	remoteLogoutDispatch = func(token string) error {
		remoteStarted <- token
		<-remoteRelease
		close(remoteFinished)
		return nil
	}

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "logout-access",
		RefreshToken: "logout-refresh",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	var httpStops int
	httpStopRunner = func() { httpStops++ }
	httpProxyIsRunningProbe = func() bool { return false }
	activeIngressModeResolver = func() (models.RunMode, bool) { return models.ModeHTTP, true }
	runService := GetSharedRunService()
	runService.SetCurrentMode("http")
	runService.SetRunning(true)
	runtime.GetStartupState().SetFetchSuccess(true)
	runtime.GetStartupState().SetStatus(runtime.READY)

	resultCh := make(chan map[string]interface{}, 1)
	go func() { resultCh <- NewAuthService().LogoutUser("") }()

	var result map[string]interface{}
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		close(remoteRelease)
		t.Fatal("local logout blocked on remote revoke")
	}
	if result["status"] != "success" {
		t.Fatalf("logout status = %#v, want success", result["status"])
	}
	if httpStops != 1 || runService.IsRunning() {
		t.Fatalf("HTTP ingress teardown stops=%d running=%t, want 1/false", httpStops, runService.IsRunning())
	}
	if current := auth.GetCurrentUserInfo(); current != nil {
		t.Fatalf("current user after logout = %#v, want nil", current)
	}
	if persisted, err := auth.HasPersistedUserInfo(); err != nil || persisted {
		t.Fatalf("persisted auth after logout = %t, err=%v", persisted, err)
	}
	if status := runtime.GetStartupState(); status.GetStatus() != runtime.UNCONFIGURED || status.GetFetchSuccess() {
		t.Fatalf("startup after logout = %v fetch=%t", status.GetStatus(), status.GetFetchSuccess())
	}

	select {
	case token := <-remoteStarted:
		if token != "logout-access" {
			t.Fatalf("remote revoke token = %q, want local logout-access", token)
		}
	case <-time.After(time.Second):
		close(remoteRelease)
		t.Fatal("remote revoke was not queued")
	}

	// The structured logout event is synchronous; the explicit retry is async.
	select {
	case requestURI := <-agentRequests:
		if !strings.HasPrefix(requestURI, "/api/agent/session-event") {
			t.Fatalf("first agent logout request = %q, want session-event", requestURI)
		}
	case <-time.After(time.Second):
		close(remoteRelease)
		t.Fatal("agent did not receive logout session event")
	}
	select {
	case requestURI := <-agentRequests:
		if !strings.HasPrefix(requestURI, "/api/agent/disable?reason=logout") {
			t.Fatalf("agent logout retry = %q, want disable?reason=logout", requestURI)
		}
	case <-time.After(time.Second):
		close(remoteRelease)
		t.Fatal("agent did not receive logout disable retry")
	}

	close(remoteRelease)
	select {
	case <-remoteFinished:
	case <-time.After(time.Second):
		t.Fatal("remote revoke goroutine did not finish")
	}
}

func TestAuthServiceLogoutStopsTUNIngress(t *testing.T) {
	defer resetRunServiceHooksForTest()
	defer ResetSharedRunServiceForTest()
	auth.ResetAuthPersistenceForTest()
	ResetSharedRunServiceForTest()

	originalRemoteLogout := remoteLogoutDispatch
	originalAgentBaseURL := localUserAgentBaseURL
	remoteLogoutDispatch = func(string) error { return nil }
	agentRequests := make(chan struct{}, 2)
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		agentRequests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer agentServer.Close()
	localUserAgentBaseURL = func() string { return agentServer.URL }
	t.Cleanup(func() {
		remoteLogoutDispatch = originalRemoteLogout
		localUserAgentBaseURL = originalAgentBaseURL
		auth.ResetAuthPersistenceForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	var tunStops int
	tunStopRunner = func() { tunStops++ }
	httpStopRunner = func() { t.Fatal("HTTP stopper called for TUN logout") }
	httpProxyIsRunningProbe = func() bool { return false }
	activeIngressModeResolver = func() (models.RunMode, bool) { return models.ModeTUN, true }
	runService := GetSharedRunService()
	runService.SetCurrentMode("tun")
	runService.SetRunning(true)

	result := NewAuthService().LogoutUser("")
	if result["status"] != "success" || tunStops != 1 || runService.IsRunning() {
		t.Fatalf("TUN logout result=%#v stops=%d running=%t", result, tunStops, runService.IsRunning())
	}
	for i := 0; i < 2; i++ {
		select {
		case <-agentRequests:
		case <-time.After(time.Second):
			t.Fatalf("received %d/2 TUN logout agent requests", i)
		}
	}
}

func TestAuthServiceRepeatedLogoutStillStopsIngress(t *testing.T) {
	defer resetRunServiceHooksForTest()
	defer ResetSharedRunServiceForTest()
	auth.ResetAuthPersistenceForTest()
	ResetSharedRunServiceForTest()

	originalRemoteLogout := remoteLogoutDispatch
	originalAgentBaseURL := localUserAgentBaseURL
	remoteLogoutDispatch = func(string) error { return nil }
	agentRequests := make(chan struct{}, 8)
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		agentRequests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer agentServer.Close()
	localUserAgentBaseURL = func() string { return agentServer.URL }
	t.Cleanup(func() {
		remoteLogoutDispatch = originalRemoteLogout
		localUserAgentBaseURL = originalAgentBaseURL
		auth.ResetAuthPersistenceForTest()
	})

	var httpStops int
	httpStopRunner = func() { httpStops++ }
	httpProxyIsRunningProbe = func() bool { return false }
	activeIngressModeResolver = func() (models.RunMode, bool) { return models.ModeHTTP, true }
	runService := GetSharedRunService()
	runService.SetCurrentMode("http")
	runService.SetRunning(true)
	NewAuthService().LogoutUser("")

	// Simulate a leaked/restarted listener while the authority is already
	// HardInvalid. A repeated explicit logout must force-fire teardown again.
	runService.SetRunning(true)
	NewAuthService().LogoutUser("")

	if httpStops != 2 || runService.IsRunning() {
		t.Fatalf("repeated logout stops=%d running=%t, want 2/false", httpStops, runService.IsRunning())
	}
	// Wait for both synchronous session events and both asynchronous retries so
	// cleanup cannot restore the real test endpoint while a goroutine is pending.
	for i := 0; i < 4; i++ {
		select {
		case <-agentRequests:
		case <-time.After(time.Second):
			t.Fatalf("received %d/4 agent logout requests", i)
		}
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
