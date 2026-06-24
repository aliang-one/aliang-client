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
	RequestUserAgentDisableAfterLogout("auth_expired")

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
	// Self-heal: a successful refresh means we hold a valid JWT again. If the
	// agent was terminal-disabled by a prior auth_expired (typically a
	// transient refresh failure to backend.aliang.one), re-enable and
	// reconnect instead of staying offline until a manual re-login. No-op for
	// every other disable reason (logout / device_unbound / device_token_invalid).
	RequestUserAgentRecoverAfterAuthExpired()
	// Bind the device to the real JWT user if it registered under a fallback
	// identity (e.g. admin-console → platform_admin) or not at all before the
	// JWT was loaded. Only meaningful in the user-agent runtime, where the
	// agent state + WS connection live.
	if IsUserAgentRuntime() {
		GetSharedAgentService().ReRegisterIfUserIdentityChanged()
	}
}
