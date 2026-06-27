package services

import (
	"fmt"

	"aliang.one/nursorgate/common/desktop"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func init() {
	auth.SetAuthExpirationHandler(handleAuthExpired)
	auth.SetAuthSuccessHandler(handleAuthRefreshed)
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
	if runService.IsRunning() {
		logger.Warn("Authentication expired, stopping running proxy service")
		desktop.Notify("aliang-gateway", "认证已过期，代理服务已停止，请重新登录")
		result := runService.StopService()
		logger.Info(fmt.Sprintf("Proxy stop result after authentication expiration: %+v", result))
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
