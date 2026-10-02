package services

// WS 握手 401 必须走会话自愈（SoftExpired 单飞刷新），而不是终态禁用+清凭据：
// 2026-10-02 两次实证"服务器滚动→握手 401→永久离线"（12:17 / 19:18，Mac agent
// 落入 refresh_invalid/auth_expired 后躺平数小时）。注册路径
// (register_auth_rejection → RecoverOrExpireLocalSession) 早已是同款自愈语义，
// 握手路径此前漏配。终态语义边界不变：logout/revoked/device_unbound 不受影响，
// HardInvalid（刷新令牌被永久拒绝）仍会清会话并让连接循环自然退出。

import (
	"testing"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandshakeTestService(t *testing.T) *AgentService {
	t.Helper()
	// 密闭：清掉共享磁盘/缓存里的登录态残留，否则 HardInvalid 用例中
	// effectiveUserAuthorizationLocked 会回退到残留凭据，shouldRun 无法归 false。
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	// GetCurrentAuthorizationHeader 有进程内存态（非 UA 运行时的兜底凭据源），
	// 磁盘重置清不掉；包内前置测试 SaveUserInfo 会污染它。
	if err := auth.DeleteUserInfo(); err != nil {
		t.Fatalf("DeleteUserInfo() error = %v", err)
	}
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
	})
	svc := &AgentService{}
	svc.mu.Lock()
	svc.ensureDeviceIdentityLocked()
	svc.mu.Unlock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.forwardedUserAuthorization = "Bearer stale_jwt"
	return svc
}

func TestHandleHandshakeUnauthorizedSelfHealsWithoutDisable(t *testing.T) {
	svc := newHandshakeTestService(t)

	var recorded []string
	previous := remoteWSAuthRecovery
	remoteWSAuthRecovery = func(reason string) {
		recorded = append(recorded, reason)
	}
	t.Cleanup(func() { remoteWSAuthRecovery = previous })

	retry := svc.handleHandshakeUnauthorized()

	assert.True(t, retry, "401 后应退避重拨而非退出循环")
	assert.Equal(t, []string{"websocket handshake 401"}, recorded, "应触发会话自愈刷新")

	svc.mu.Lock()
	defer svc.mu.Unlock()
	assert.True(t, svc.state.Enabled, "自愈路径不得禁用 agent")
	assert.True(t, svc.state.Registered, "自愈路径不得清除注册态")
	assert.Equal(t, "Bearer stale_jwt", svc.forwardedUserAuthorization, "自愈路径不得清空凭据")
	assert.Equal(t, "reauthenticating", svc.state.LastSyncStatus)
}

func TestHandleHandshakeUnauthorizedHardInvalidLetsLoopExit(t *testing.T) {
	svc := newHandshakeTestService(t)

	previous := remoteWSAuthRecovery
	remoteWSAuthRecovery = func(reason string) {
		// 模拟 HardInvalid：刷新令牌被永久拒绝，本地会话被清。
		svc.mu.Lock()
		svc.forwardedUserAuthorization = ""
		svc.mu.Unlock()
	}
	t.Cleanup(func() { remoteWSAuthRecovery = previous })

	retry := svc.handleHandshakeUnauthorized()
	require.True(t, retry)

	_, shouldRun := svc.remoteConnectionSnapshot()
	assert.False(t, shouldRun, "会话被清后循环应自然退出")
}
