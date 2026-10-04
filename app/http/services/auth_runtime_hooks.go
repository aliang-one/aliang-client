package services

import (
	"fmt"
	"sync"

	"aliang.one/nursorgate/common/desktop"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func init() {
	auth.SubscribeGlobal(onSessionEvent)
	auth.SetAuthSuccessHandler(handleAuthRefreshed)
}

var (
	// softExpiryRecoveryStarter begins the SoftExpired recovery loop. Injectable
	// for tests. Defaults to auth.StartSoftExpiryRecovery.
	softExpiryRecoveryStarter = auth.StartSoftExpiryRecovery
	userAgentDisableRequester = RequestUserAgentDisableForSessionEnd
	// desktopNotifier 是会话过期桌面提醒的注入口(测试替身)。默认系统通知。
	desktopNotifier = desktop.Notify
	authRuntimeMu   sync.Mutex
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
//   - →HardInvalid: stop the proxy, disable Agent, and clear UI state. User logout
//     uses the same teardown without the "认证已过期" desktop notification.
func onSessionEvent(e auth.SessionEvent) {
	switch e.To {
	case auth.StateRestoring:
		startupState := runtime.GetStartupState()
		startupState.SetFetchSuccess(false)
		startupState.SetStatus(runtime.CONFIGURING)
		GetSharedRunService().StopIngressIfActive()
	case auth.StateSoftExpired:
		if GetSharedRunService().StopIngressIfActive() {
			authRuntimeMu.Lock()
			proxyPausedForSoftExpiry = true
			authRuntimeMu.Unlock()
			logger.Warn("SoftExpired: ingress proxy paused while access token is recovered")
		}
		softExpiryRecoveryStarter()
	case auth.StateActive:
		startupState := runtime.GetStartupState()
		startupState.SetFetchSuccess(true)
		startupState.SetStatus(runtime.READY)
		authRuntimeMu.Lock()
		shouldResume := proxyPausedForSoftExpiry
		proxyPausedForSoftExpiry = false
		authRuntimeMu.Unlock()
		if shouldResume {
			logger.Info("session recovered: resuming ingress proxy")
			GetSharedRunService().StartService()
		}
	case auth.StateHardInvalid:
		authRuntimeMu.Lock()
		proxyPausedForSoftExpiry = false
		authRuntimeMu.Unlock()
		if e.Reason == auth.ReasonLogout {
			handleLoggedOut()
			return
		}
		handleAuthExpired(e.Reason)
	case auth.StateUnauthenticated:
		authRuntimeMu.Lock()
		proxyPausedForSoftExpiry = false
		authRuntimeMu.Unlock()
		handleLoggedOut()
	}
}

func handleLoggedOut() {
	startupState := runtime.GetStartupState()
	startupState.SetFetchSuccess(false)
	startupState.SetStatus(runtime.UNCONFIGURED)
	mode, stopped := GetSharedRunService().StopIngressForLogout()
	if stopped {
		logger.Info(fmt.Sprintf("User logout stopped %s ingress", mode))
	} else {
		logger.Debug(fmt.Sprintf("User logout found no active %s ingress", mode))
	}
}

func handleAuthExpired(reason auth.SessionReason) {
	startupState := runtime.GetStartupState()
	startupState.SetFetchSuccess(false)
	startupState.SetStatus(runtime.UNCONFIGURED)
	// HardInvalid is a full local security boundary. The user-agent must lose its
	// forwarded access token and remote connection just like an explicit logout;
	// it may only reconnect after a later login/refresh emits Active.
	disableReason := string(reason)
	if disableReason == "" {
		disableReason = "auth_expired"
	}
	userAgentDisableRequester(disableReason)

	runService := GetSharedRunService()
	// Stop the ingress proxy based on its REAL listener state, not the
	// runService.isRunning flag — that flag can desync to false (mode switch,
	// daemon restart, activation rollback) while 56432 is still bound, which
	// previously left the proxy serving a dead token (cloud returning 401).
	if runService.StopIngressIfActive() {
		logger.Warn("Authentication expired, stopping ingress proxy")
	} else {
		logger.Debug("Authentication expired; no active ingress proxy to stop")
	}
	// 桌面提醒无条件发出(登出走 handleLoggedOut,不会到这里)。旧实现只在
	// 代理确实在跑时才提醒:用户没开代理时会话死亡全程静默,agent 落
	// refresh_invalid 粘性禁用也无人知晓(2026-10-02 生产实证,卡 3 天)。
	// 这条通知是把「静默卡死」变成「一次点击重登即恢复」的唯一用户可见链路。
	desktopNotifier("aliang-gateway", "认证已过期，代理与远程访问已停止，请重新登录")
}

// handleAuthRefreshed fires after a successful token refresh. It forwards the
// new access token to the user-agent process, which updates the live PhoneServer
// session or uses it on the next connection.
func handleAuthRefreshed() {
	// The dashboard/core process is the sole refresh-token owner. Forward the
	// freshly issued access token to the user-agent process; SyncNow installs it
	// in process-local memory, updates the live PhoneServer session, and reconnects
	// when needed. The agent never reads or rotates the persisted refresh token.
	snapshot := auth.GetSessionAuthority().Snapshot()
	generation := snapshot.Generation
	go func() {
		if !auth.GetSessionAuthority().GenerationActive(generation) {
			return
		}
		if err := SyncUserAgentAfterAuthWithRetry("session_refreshed"); err != nil {
			logger.Warn(fmt.Sprintf("Failed to forward refreshed session to user agent: %v", err))
		}
		if !auth.GetSessionAuthority().GenerationActive(generation) {
			RequestUserAgentDisableForSessionEnd("stale_auth_sync")
		}
	}()
}
