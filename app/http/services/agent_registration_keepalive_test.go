package services

// 会话外注册保活定时器测试。
//
// 不变量 1：从未注册的设备禁止自连——生产代码 state.Enabled=true 唯一写点是
// 注册成功（agent_service.go registerAndSyncLockedWithUserContext），因此
// "Enabled && DeviceID 非空" 严格蕴含"曾注册过"，保活循环永远不会替一台
// 从未注册的设备发起注册。
// 不变量 2：粘性禁用态（logout/refresh_invalid/…）必须重登；保活循环走
// SyncNowWithUserContext("","")，无显式 JWT，由 shouldBlockBackgroundSyncLocked
// 在 SyncNow 内部拦截。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func setupKeepaliveTest(t *testing.T) *AgentService {
	t.Helper()
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		ResetAgentRegistrationKeepaliveForTest()
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_keepalive",
		RefreshToken: "refresh_keepalive",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	return NewAgentService()
}

// countingRegisterServer 统计 /api/devices/register 被真实调用的次数。
func countingRegisterServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/devices/register":
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{"device_id": "dev_keepalive"},
			})
		case "/api/agent/status":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// setAgentState 在锁内改内存态（仅测试造态用）。
func setAgentState(t *testing.T, service *AgentService, mutate func(*agentState)) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	mutate(&service.state)
}

func TestRegistrationKeepaliveNeededTruthTable(t *testing.T) {
	service := setupKeepaliveTest(t)
	cases := []struct {
		name   string
		mutate func(*agentState)
		want   bool
	}{
		{"incident residue: enabled+auth+device but lost registration", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
		}, true},
		{"registered but disconnected", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, true, false, "dev_x"
		}, true},
		{"healthy", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, true, true, "dev_x"
		}, false},
		{"never enabled (fresh device)", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = false, false, false, "dev_x"
		}, false},
		{"enabled but no credentials", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
		}, false}, // 删凭据后 enabled+device 也不足以自连
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAgentState(t, service, tc.mutate)
			if tc.name == "enabled but no credentials" {
				if err := auth.DeleteUserInfo(); err != nil {
					t.Fatalf("DeleteUserInfo() error = %v", err)
				}
			}
			if got := service.registrationKeepaliveNeeded(); got != tc.want {
				t.Fatalf("registrationKeepaliveNeeded() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestKeepaliveRecoversLostRegistrationThenRests(t *testing.T) {
	service := setupKeepaliveTest(t)
	setAgentState(t, service, func(st *agentState) {
		st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
	})

	var calls atomic.Int64
	stop, nudge := make(chan struct{}), make(chan struct{}, 1)
	go runAgentRegistrationKeepalive(stop, nudge, 10*time.Millisecond, service,
		func(s *AgentService) error {
			calls.Add(1)
			setAgentState(t, s, func(st *agentState) { st.Registered, st.RemoteConnected = true, true })
			return nil
		})
	t.Cleanup(func() { close(stop) })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() < 1 {
		t.Fatal("keepalive did not trigger a recovery sync for lost registration")
	}
	time.Sleep(150 * time.Millisecond) // 恢复后应静默（needed=false）
	if after := calls.Load(); after != 1 {
		t.Fatalf("keepalive kept syncing after recovery: calls=%d, want exactly 1", after)
	}
}

// 不变量 1 端到端：从未 enable 的设备，保活循环绝不能替它发起注册。
func TestKeepaliveNeverSelfRegistersFreshDevice(t *testing.T) {
	service := setupKeepaliveTest(t) // Enabled=false，设备从未注册
	server, calls := countingRegisterServer(t)
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	startAgentRegistrationKeepalive(service, 10*time.Millisecond,
		func(s *AgentService) error { return s.SyncNowWithUserContext("", "") })
	time.Sleep(250 * time.Millisecond)

	if n := calls.Load(); n != 0 {
		t.Fatalf("register endpoint called %d times for a never-registered device — must be 0", n)
	}
}

// 不变量 2 端到端：粘性禁用态（logout）下保活循环可触发 SyncNow，但
// shouldBlockBackgroundSyncLocked 必须拦下注册（无显式 JWT 不得复活）。
func TestKeepaliveRespectsStickyDisable(t *testing.T) {
	service := setupKeepaliveTest(t)
	setAgentState(t, service, func(st *agentState) {
		st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
		st.LastSyncStatus = "logout"
	})
	server, calls := countingRegisterServer(t)
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	var syncInvocations atomic.Int64
	startAgentRegistrationKeepalive(service, 10*time.Millisecond,
		func(s *AgentService) error {
			syncInvocations.Add(1)
			return s.SyncNowWithUserContext("", "")
		})
	time.Sleep(250 * time.Millisecond)

	if syncInvocations.Load() == 0 {
		t.Fatal("keepalive loop never ran its sync path")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("register endpoint called %d times while sticky-disabled — must be 0", n)
	}
}

// 失败退避复用 handshakeRetryDelay 形状：第二次尝试距第一次 ≥ 2s。
func TestKeepaliveBacksOffAfterFailures(t *testing.T) {
	service := setupKeepaliveTest(t)
	setAgentState(t, service, func(st *agentState) {
		st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
	})

	var (
		mu       sync.Mutex
		stamps   []time.Time
	)
	startAgentRegistrationKeepalive(service, 10*time.Millisecond,
		func(s *AgentService) error {
			mu.Lock()
			stamps = append(stamps, time.Now())
			mu.Unlock()
			return errors.New("server still down")
		})

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(stamps)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stamps) < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", len(stamps))
	}
	gap := stamps[1].Sub(stamps[0])
	if min := handshakeRetryDelay(1); gap < min {
		t.Fatalf("second attempt %v after first, want >= handshakeRetryDelay(1)=%v", gap, min)
	}
}

func TestNudgeIsNoopWhenNotStarted(t *testing.T) {
	setupKeepaliveTest(t)
	// 未启动时调用不应 panic、不应有任何副作用。
	nudgeAgentRegistrationKeepalive("test_before_start")
}

func TestNudgeTriggersImmediateAction(t *testing.T) {
	service := setupKeepaliveTest(t)
	setAgentState(t, service, func(st *agentState) {
		st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
	})

	var calls atomic.Int64
	startAgentRegistrationKeepalive(service, 30*time.Second, // tick 极长：动作只能由 nudge 触发
		func(s *AgentService) error {
			calls.Add(1)
			return nil
		})
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("action fired before any nudge despite 30s tick")
	}
	nudgeAgentRegistrationKeepalive("test_nudge")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nudge did not trigger an immediate keepalive action")
}

// registrationLostRecoverable 真值表：WS 循环终态退出里的可恢复子场景
// （事故残留态），与粘性禁用/凭据缺失/正常在线严格区分。
func TestRegistrationLostRecoverableTruthTable(t *testing.T) {
	service := setupKeepaliveTest(t)
	cases := []struct {
		name   string
		mutate func(*agentState)
		want   bool
	}{
		{"incident residue: enabled+auth+device but lost registration", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, "dev_x"
		}, true},
		{"no credentials", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, false, false, ""
		}, false},
		{"disabled", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = false, false, false, "dev_x"
		}, false},
		{"registered (nothing lost)", func(st *agentState) {
			st.Enabled, st.Registered, st.RemoteConnected, st.DeviceID = true, true, false, "dev_x"
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAgentState(t, service, tc.mutate)
			if got := service.registrationLostRecoverable(); got != tc.want {
				t.Fatalf("registrationLostRecoverable() = %t, want %t", got, tc.want)
			}
		})
	}
}
