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

	// handleAuthRefreshed only forwards while its captured session generation
	// is Active. Establish a self-contained Active authority so the test no
	// longer depends on whichever state earlier tests happened to leave behind
	// (standalone -run runs previously timed out on a Restoring authority).
	auth.ResetSessionAuthorityForTest().NotifyLoggedIn(&auth.UserInfo{ID: 42, Username: "refreshed-user"})
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })

	// The /api/agent/sync response is held open until the test has reset the
	// session authority below. handleAuthRefreshed's goroutine re-checks
	// GenerationActive AFTER the sync returns; holding the response pins that
	// second read to AFTER the reset, so the goroutine deterministically takes
	// its stale path (/api/agent/disable) — whose arrival proves the goroutine
	// (and its session-authority singleton reads) finished before this test
	// ends. Without this handshake the leaked read races the authority resets
	// in subsequent tests' setups under -race.
	received := make(chan string, 1)
	releaseSync := make(chan struct{})
	disableSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/sync":
			select {
			case received <- r.Header.Get(AgentForwardedAuthorizationHeader):
			default:
			}
			<-releaseSync
		case "/api/agent/disable":
			select {
			case disableSeen <- struct{}{}:
			default:
			}
		default:
			t.Errorf("path = %q, want /api/agent/sync or /api/agent/disable", r.URL.Path)
		}
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

	// Invalidate the captured generation, then let the sync response through:
	// the goroutine's follow-up check must now see a stale generation and
	// notify the user agent of the dead session instead of staying silent.
	auth.ResetSessionAuthorityForTest()
	close(releaseSync)
	select {
	case <-disableSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("stale refreshed-session sync never triggered the user-agent disable")
	}
}

func resetAuthHooksForTest() {
	resetRunServiceHooksForTest()
	ResetSharedRunServiceForTest()
	proxyPausedForSoftExpiry = false
	softExpiryRecoveryStarter = auth.StartSoftExpiryRecovery
	userAgentDisableRequester = RequestUserAgentDisableForSessionEnd
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
	auth.ResetSessionAuthorityForTest().NotifyLoggedIn(&auth.UserInfo{ID: 1, Username: "recovered-user"})
	t.Cleanup(func() { auth.ResetSessionAuthorityForTest() })

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
	var disableReasons []string
	userAgentDisableRequester = func(reason string) { disableReasons = append(disableReasons, reason) }

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
	if len(disableReasons) != 1 || disableReasons[0] != "refresh_invalid" {
		t.Fatalf("agent disable reasons = %v, want [refresh_invalid]", disableReasons)
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
