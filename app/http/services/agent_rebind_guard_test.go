package services

import (
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

// NET-01 验收②:非本机属主的账号在这台仪表盘登录后,不能把这台设备换绑走。
//
// 此前 ReRegisterIfUserIdentityChanged 检测到登录身份与已绑定账号不一致时,
// 无条件清空注册状态并用新身份重新注册——任何能在这台仪表盘登录自己账号的
// 人都能把设备绑走。现在它只记 PendingRebindUser 并断开远端,等属主经
// /api/agent/confirm-rebind 显式放行。

func seedBoundAgent(t *testing.T, registeredUser string) *AgentService {
	t.Helper()
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	auth.ResetAuthPersistenceForTest()
	t.Cleanup(auth.ResetAuthPersistenceForTest)
	s := NewAgentService()
	s.mu.Lock()
	s.state.Enabled = true
	s.state.Registered = true
	s.state.RegisteredUser = registeredUser
	s.state.DeviceID = "dev-rebind"
	s.mu.Unlock()
	return s
}

func loginAs(t *testing.T, id int64, email string) {
	t.Helper()
	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken: "jwt-" + email,
		ID:          id,
		Email:       email,
	}); err != nil {
		t.Fatalf("SaveUserInfo: %v", err)
	}
}

func TestRebind_DifferentAccountPendsInsteadOfSwitching(t *testing.T) {
	s := seedBoundAgent(t, "id:1")
	// 另一个人在这台仪表盘登录自己的账号。
	loginAs(t, 2, "attacker@example.com")

	s.ReRegisterIfUserIdentityChanged()

	s.mu.Lock()
	pending, regUser, enabled := s.state.PendingRebindUser, s.state.RegisteredUser, s.state.Enabled
	s.mu.Unlock()
	if pending != "id:2" {
		t.Fatalf("PendingRebindUser = %q, want the attacker identity id:2", pending)
	}
	if regUser != "id:1" {
		t.Fatalf("registered_user was switched to %q; the device must stay bound to its owner", regUser)
	}
	// enabled 必须为 false:待换绑期间设备不得以任何身份上线。
	// registered 刻意保持 true——它反映「服务端仍把这台设备注册在原账号下」,
	// 清掉会让状态失真(换绑被拒后无法据此恢复)。
	if enabled {
		t.Fatal("agent must not stay enabled while a rebind is pending")
	}

	// 前端靠 Status() 带出的两个字段显示「本机绑定 X,刚登录的是 Y」。
	// 此前这两个字段只在 agentState 上,没进响应——前端拿不到,提示区显示空值。
	resp := s.Status()
	if resp.PendingRebindUser != "id:2" {
		t.Fatalf("Status().PendingRebindUser = %q, want id:2", resp.PendingRebindUser)
	}
	if resp.RegisteredUser != "id:1" {
		t.Fatalf("Status().RegisteredUser = %q, want id:1", resp.RegisteredUser)
	}
	if resp.SyncStatus != "rebind_pending" {
		t.Fatalf("Status().SyncStatus = %q, want rebind_pending", resp.SyncStatus)
	}
}

func TestRebind_SameAccountStillCorrectsImmediately(t *testing.T) {
	// 同一账号内的身份修正(fallback 身份 → 真 JWT)必须照旧即时生效,
	// 不能被新守卫误伤。
	s := seedBoundAgent(t, "id:7")
	loginAs(t, 7, "owner@example.com")
	s.ReRegisterIfUserIdentityChanged()
	s.mu.Lock()
	pending := s.state.PendingRebindUser
	s.mu.Unlock()
	if pending != "" {
		t.Fatalf("same-account correction must not pend: PendingRebindUser=%q", pending)
	}
}

func TestRebind_RejectKeepsOriginalBinding(t *testing.T) {
	s := seedBoundAgent(t, "id:1")
	loginAs(t, 2, "attacker@example.com")
	s.ReRegisterIfUserIdentityChanged()

	s.RejectRebind()

	s.mu.Lock()
	pending, regUser := s.state.PendingRebindUser, s.state.RegisteredUser
	s.mu.Unlock()
	if pending != "" {
		t.Fatalf("PendingRebindUser after reject = %q", pending)
	}
	if regUser != "id:1" {
		t.Fatalf("reject must keep owner binding, got %q", regUser)
	}
}

func TestRebind_RejectIsNoopWhenNothingPending(t *testing.T) {
	s := seedBoundAgent(t, "id:1")
	s.RejectRebind()
	s.RejectRebind()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.PendingRebindUser != "" {
		t.Fatalf("PendingRebindUser = %q", s.state.PendingRebindUser)
	}
	if s.state.LastSyncStatus == "rebind_rejected" {
		t.Fatalf("no-op reject must not stamp rebind_rejected")
	}
}

func TestRebind_ReturningToOwnerClearsPending(t *testing.T) {
	// 属主重新登录回自己的账号:待换绑标记应自动清除。
	s := seedBoundAgent(t, "id:1")
	loginAs(t, 2, "attacker@example.com")
	s.ReRegisterIfUserIdentityChanged()
	loginAs(t, 1, "owner@example.com")
	s.ReRegisterIfUserIdentityChanged()
	s.mu.Lock()
	pending := s.state.PendingRebindUser
	s.mu.Unlock()
	if pending != "" {
		t.Fatalf("PendingRebindUser after owner re-login = %q, want empty", pending)
	}
}
