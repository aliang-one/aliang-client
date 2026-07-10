package services

import (
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

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

func TestOnSessionEventHardInvalidLogoutSkipsTeardown(t *testing.T) {
	defer resetAuthHooksForTest()
	resetAuthHooksForTest()

	httpProxyIsRunningProbe = func() bool { return false }
	httpStopRunner = func() { t.Fatal("httpStopRunner must not run for logout") }

	rs := GetSharedRunService()
	rs.SetCurrentMode("http")
	rs.SetRunning(true)
	startup := runtime.GetStartupState()
	startup.SetStatus(runtime.READY)
	startup.SetFetchSuccess(true)

	onSessionEvent(auth.SessionEvent{To: auth.StateHardInvalid, Reason: auth.ReasonLogout})

	// Logout teardown is owned by AuthService.LogoutUser; the listener must not
	// touch the proxy or startup state.
	if !rs.IsRunning() {
		t.Fatal("listener must not stop the proxy on logout")
	}
	if startup.GetStatus() != runtime.READY {
		t.Fatalf("logout must not clear startup state via listener; status=%v", startup.GetStatus())
	}
}
