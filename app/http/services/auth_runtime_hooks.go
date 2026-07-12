package services

import (
	"fmt"

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
//   - →HardInvalid: stop the proxy and clear UI state. User logout uses the same
//     teardown without the "认证已过期" desktop notification.
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
			handleLoggedOut()
			return
		}
		handleAuthExpired()
	}
}

func handleLoggedOut() {
	startupState := runtime.GetStartupState()
	startupState.SetFetchSuccess(false)
	startupState.SetStatus(runtime.UNCONFIGURED)
	if GetSharedRunService().StopIngressIfActive() {
		logger.Info("User logout stopped the active ingress proxy")
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

// handleAuthRefreshed fires after a successful token refresh. It forwards the
// new access token to the user-agent process, which updates the live PhoneServer
// session or uses it on the next connection.
func handleAuthRefreshed() {
	// The dashboard/core process is the sole refresh-token owner. Forward the
	// freshly issued access token to the user-agent process; SyncNow installs it
	// in process-local memory, updates the live PhoneServer session, and reconnects
	// when needed. The agent never reads or rotates the persisted refresh token.
	go func() {
		if err := SyncUserAgentAfterAuthWithRetry("session_refreshed"); err != nil {
			logger.Warn(fmt.Sprintf("Failed to forward refreshed session to user agent: %v", err))
		}
	}()
}
