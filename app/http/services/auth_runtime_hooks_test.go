package services

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func TestHandleAuthRefreshedForwardsFreshAccessTokenToUserAgent(t *testing.T) {
	auth.SetSessionOwnerProcess(true)
	auth.SetCurrentUserInfo(&auth.UserInfo{
		AccessToken: "fresh-access",
		TokenType:   "Bearer",
		ID:          42,
	})
	t.Cleanup(func() { auth.SetCurrentUserInfo(nil) })

	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/sync" {
			t.Errorf("path = %q, want /api/agent/sync", r.URL.Path)
		}
		received <- r.Header.Get(AgentForwardedAuthorizationHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	originalBaseURL := localUserAgentBaseURL
	localUserAgentBaseURL = func() string { return server.URL }
	t.Cleanup(func() { localUserAgentBaseURL = originalBaseURL })

	handleAuthRefreshed()
	select {
	case header := <-received:
		if header != "Bearer fresh-access" {
			t.Fatalf("forwarded authorization = %q, want Bearer fresh-access", header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for refreshed session to reach user agent")
	}
}

func resetAuthHooksForTest() {
	resetRunServiceHooksForTest()
	ResetSharedRunServiceForTest()
	proxyPausedForSoftExpiry = false
	softExpiryRecoveryStarter = auth.StartSoftExpiryRecovery
}

func TestOnSessionEventSoftExpiredPausesProxyAndStartsRecovery(t *testing.T) {
	defer resetAuthHooksForTest()
	resetAuthHooksForTest()

	httpProxyIsRunningProbe = func() bool { return false }
	var httpStops int
	httpStopRunner = func() { httpStops++ }
	softExpiryRecoveryStarter = func() {} // avoid real recovery goroutine

	rs := GetSharedRunService()
	rs.SetCurrentMode("http")
	rs.SetRunning(true)

	onSessionEvent(auth.SessionEvent{To: auth.StateSoftExpired, Reason: auth.ReasonAccessRejected})

	if httpStops != 1 {
		t.Fatalf("expected ingress paused (httpStopRunner once), got %d", httpStops)
	}
	if !proxyPausedForSoftExpiry {
		t.Fatal("expected proxyPausedForSoftExpiry=true after pausing on SoftExpired")
	}
	if rs.IsRunning() {
		t.Fatal("expected isRunning=false after pause")
	}
}

func TestOnSessionEventActiveResumesPausedProxy(t *testing.T) {
	defer resetAuthHooksForTest()
	resetAuthHooksForTest()

	httpProxyIsRunningProbe = func() bool { return false }
	httpStopRunner = func() {}
	httpStartRunner = func() {}
	softExpiryRecoveryStarter = func() {}

	rs := GetSharedRunService()
	rs.SetCurrentMode("http")
	rs.SetRunning(true)

	// Enter SoftExpired (pauses proxy), then recover to Active.
	onSessionEvent(auth.SessionEvent{To: auth.StateSoftExpired})
	onSessionEvent(auth.SessionEvent{To: auth.StateActive, Reason: auth.ReasonRefreshed})

	if runtime.GetStartupState().GetStatus() != runtime.READY {
		t.Fatalf("startup status=%v want READY after Active", runtime.GetStartupState().GetStatus())
	}
	if proxyPausedForSoftExpiry {
		t.Fatal("expected proxyPausedForSoftExpiry cleared after resume")
	}
	if !rs.IsRunning() {
		t.Fatal("expected ingress resumed (isRunning=true) after Active")
	}
}

func TestOnSessionEventHardInvalidRunsTeardown(t *testing.T) {
	defer resetAuthHooksForTest()
	resetAuthHooksForTest()

	httpProxyIsRunningProbe = func() bool { return false }
	var httpStops int
	httpStopRunner = func() { httpStops++ }

	rs := GetSharedRunService()
	rs.SetCurrentMode("http")
	rs.SetRunning(true)

	onSessionEvent(auth.SessionEvent{To: auth.StateHardInvalid, Reason: auth.ReasonRefreshInvalid})

	if httpStops != 1 {
		t.Fatalf("expected teardown to stop ingress once, got %d", httpStops)
	}
	if runtime.GetStartupState().GetStatus() != runtime.UNCONFIGURED {
		t.Fatalf("startup status=%v want UNCONFIGURED after HardInvalid", runtime.GetStartupState().GetStatus())
	}
	if rs.IsRunning() {
		t.Fatal("expected isRunning=false after HardInvalid teardown")
	}
}

func TestOnSessionEventHardInvalidLogoutStopsIngressWithoutExpiryNotification(t *testing.T) {
	defer resetAuthHooksForTest()
	resetAuthHooksForTest()

	httpProxyIsRunningProbe = func() bool { return false }
	var httpStops int
	httpStopRunner = func() { httpStops++ }

	rs := GetSharedRunService()
	rs.SetCurrentMode("http")
	rs.SetRunning(true)
	startup := runtime.GetStartupState()
	startup.SetStatus(runtime.READY)
	startup.SetFetchSuccess(true)

	onSessionEvent(auth.SessionEvent{To: auth.StateHardInvalid, Reason: auth.ReasonLogout})

	if httpStops != 1 {
		t.Fatalf("logout http stops = %d, want 1", httpStops)
	}
	if rs.IsRunning() {
		t.Fatal("logout listener left ingress marked running")
	}
	if startup.GetStatus() != runtime.UNCONFIGURED || startup.GetFetchSuccess() {
		t.Fatalf("logout startup state = %v fetch=%t, want UNCONFIGURED/false", startup.GetStatus(), startup.GetFetchSuccess())
	}
}
