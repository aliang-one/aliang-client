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
