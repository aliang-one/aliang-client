package services

import (
	"aliang.one/nursorgate/common/desktop"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func init() {
	auth.GetSessionAuthority().Subscribe(onSessionEvent)
	auth.SetAuthSuccessHandler(handleAuthRefreshed)
}

var (
	// softExpiryRecoveryStarter begins the SoftExpired recovery loop. Injectable
	// for tests. Defaults to auth.StartSoftExpiryRecovery.
	softExpiryRecoveryStarter = auth.StartSoftExpiryRecovery
	// proxyPausedForSoftExpiry records that WE paused the ingress proxy on
	// entering SoftExpired, so →Active resumes only what we paused (not a proxy
	// the user never started).
	proxyPausedForSoftExpiry bool
)

// onSessionEvent fans session-state transitions out to subsystems (the migration
// target of the old single authExpirationHandler, now extended to SoftExpired).
//
//   - →SoftExpired: pause the ingress proxy (no forwarding with a rejected
//     token — closes 缺口 B) and start the bounded recovery coordinator.
//   - →Active: mark the session ready; if we paused the proxy for SoftExpired,
//     resume it.
//   - →HardInvalid (non-logout): stop the proxy + clear UI state + notify.
//     Logout is skipped here — AuthService.LogoutUser owns its own teardown, and
//     the "认证已过期" wording would be wrong for a user-initiated logout.
func onSessionEvent(e auth.SessionEvent) {
	switch e.To {
	case auth.StateSoftExpired:
		if GetSharedRunService().StopIngressIfActive() {
			proxyPausedForSoftExpiry = true
			logger.Warn("SoftExpired: ingress proxy paused while access token is recovered")
		}
		softExpiryRecoveryStarter()
	case auth.StateActive:
		startupState := runtime.GetStartupState()
		startupState.SetFetchSuccess(true)
		startupState.SetStatus(runtime.READY)
		if proxyPausedForSoftExpiry {
			proxyPausedForSoftExpiry = false
			logger.Info("session recovered: resuming ingress proxy")
			GetSharedRunService().StartService()
		}
	case auth.StateHardInvalid:
		proxyPausedForSoftExpiry = false
		if e.Reason == auth.ReasonLogout {
			return
		}
		handleAuthExpired()
	}
}

func handleAuthExpired() {
	startupState := runtime.GetStartupState()
	startupState.SetFetchSuccess(false)
	startupState.SetStatus(runtime.UNCONFIGURED)
	// A user-session expiry is intentionally NOT a device deregistration. The
	// device_token is a per-device credential independent of the user JWT, so we
	// keep the registration intact and let the remote link drop on its own
	// (PhoneServer closes the WS when the user_token expires; the child then
	// reports connection_state=disconnected while registration_state stays
	// registered). The link is re-established when the session is restored — see
	// handleAuthRefreshed → RequestUserAgentEnsureConnection.
	//
	// Previously this called RequestUserAgentDisableAfterLogout("auth_expired"),
	// which terminal-disabled the agent: clearing device_token and writing a
	// sticky auth_expired. That is exactly what produced "logged in but agent
	// offline" — a transient session blip was promoted to a device deregistration
	// that session recovery never undid. Only a deliberate user logout
	// deregisters now (AuthService.logout → /api/agent/disable "logout").

	runService := GetSharedRunService()
	// Stop the ingress proxy based on its REAL listener state, not the
	// runService.isRunning flag — that flag can desync to false (mode switch,
	// daemon restart, activation rollback) while 56432 is still bound, which
	// previously left the proxy serving a dead token (cloud returning 401).
	if runService.StopIngressIfActive() {
		logger.Warn("Authentication expired, stopping ingress proxy")
		desktop.Notify("aliang-gateway", "认证已过期，代理服务已停止，请重新登录")
	} else {
		logger.Debug("Authentication expired; no active ingress proxy to stop")
	}
}

// handleAuthRefreshed fires after a successful token refresh (or login). The
// access_token now carries a fresh exp; push it to PhoneServer over the live
// agent WS so the server's recorded session expiry (userTokenExp) advances
// without a reconnect. No-op when the agent isn't connected (the next connect
// carries the current JWT via the user_token query param).
func handleAuthRefreshed() {
	GetSharedAgentService().PushSessionRefresh()
	// Session restored: ensure the remote link is up. If the expiry dropped the
	// WS, this reconnects it (idempotent — a no-op when already connected /
	// connecting, or when there is no device_token yet). The device_token was
	// preserved across the blip (handleAuthExpired no longer deregisters), so
	// this is the fast syncExistingRegisteredDevice path, not a full re-register.
	// This replaces the old RecoverIfAuthExpired nudge, which keyed off the
	// now-removed auth_expired terminal state.
	RequestUserAgentEnsureConnection()
	// Bind the device to the real JWT user if it registered under a fallback
	// identity (e.g. admin-console → platform_admin) or not at all before the
	// JWT was loaded. Only meaningful in the user-agent runtime, where the
	// agent state + WS connection live.
	if IsUserAgentRuntime() {
		GetSharedAgentService().ReRegisterIfUserIdentityChanged()
	}
}
