package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"aliang.one/nursorgate/common/cache"
)

// 2026-10-31 生产事故(transfer 401 持续):用户重登后 core 拿到新凭证并两次
// auth_sync success,但 transfer 客户端的本地续期被「token 未临近过期,或被
// 服务端吊销但未过期」闸门短路,永远返回同一头——通道 401 无限退避,owner 的
// 恢复链(真刷新+重推)从未被触发。修复:通道 401 且续期未轮转时,带指纹去重
// 地通知 owner 走恢复链(POST /api/auth/agent-auth-rejected,reason=
// agent_channel_rejected)。本文件锁定通知的触发、指纹风暴闸与沿语义。

func resetChannelOwnerNotifyForTest() {
	resetOwnerChannelNotifyForTest()
}

func newChannelNotifyTestEnv(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Setenv(AgentRuntimeEnv, "1")
	t.Setenv(SessionOwnerAddrEnv, "")
	resetChannelOwnerNotifyForTest()
	resetOwnerAuthRejectedNotify()
	originalShared := sharedAgentService
	t.Cleanup(func() {
		resetChannelOwnerNotifyForTest()
		resetOwnerAuthRejectedNotify()
		cache.ResetCacheDirForTest()
		sharedAgentServiceMu.Lock()
		sharedAgentService = originalShared
		sharedAgentServiceMu.Unlock()
	})
}

// TestCallAgentServerNotifiesOwnerOn401WithoutRotation 锁定:401 且本地续期
// 未轮转(同头返回)时,必须恰好通知 owner 一次,reason=agent_channel_rejected
// ——这是「重登成功但 transfer 仍 401」唯一能触达恢复链的入口。
func TestCallAgentServerNotifiesOwnerOn401WithoutRotation(t *testing.T) {
	newChannelNotifyTestEnv(t)

	var mu sync.Mutex
	var reasons []string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/agent-auth-rejected" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		reasons = append(reasons, body["reason"].(string))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{"applied": true, "ignored": ""},
		})
	}))
	defer owner.Close()
	t.Setenv(SessionOwnerAddrEnv, owner.URL)

	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	oldRefresher := agentChannelRefreshHeader
	// 本地续期返回同头 = 「吊销但未过期」场景(2026-10-31 生产实锤)。
	agentChannelRefreshHeader = func() (string, error) { return "Bearer stale", nil }
	t.Cleanup(func() {
		agentChannelRefreshHeader = oldRefresher
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	svc := &AgentService{client: srv.Client()}
	_, _ = svc.callAgentServerWithAuthorization(http.MethodGet, srv.URL, nil, "Bearer stale")

	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 {
		t.Fatalf("owner received %d notifications after 401 without rotation, want 1 (reasons=%v)", len(reasons), reasons)
	}
	if reasons[0] != agentAuthRejectedReasonChannel {
		t.Fatalf("reason = %v, want %v", reasons[0], agentAuthRejectedReasonChannel)
	}
}

// TestChannelOwnerNotifyFingerprintGate 锁定风暴闸:同一凭据指纹的后续 401
// 不再重复通知(退避循环不得变成恢复链风暴);指纹变化(凭据被替换)后重新
// 武装——owner 重推的新凭证若仍被拒,允许再次触发恢复。
func TestChannelOwnerNotifyFingerprintGate(t *testing.T) {
	newChannelNotifyTestEnv(t)

	var mu sync.Mutex
	var count int
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/agent-auth-rejected" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{"applied": true, "ignored": ""},
		})
	}))
	defer owner.Close()
	t.Setenv(SessionOwnerAddrEnv, owner.URL)
	svc := &AgentService{}

	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-one")
	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-one")
	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-one")
	mu.Lock()
	if count != 1 {
		mu.Unlock()
		t.Fatalf("same-fingerprint notifications = %d, want 1", count)
	}
	mu.Unlock()

	// 指纹变化 → 重新武装 → 恰好再通知一次。
	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-two")
	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-two")
	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Fatalf("notifications after fingerprint rotation = %d, want 2", count)
	}
}

// TestChannelOwnerNotifyEdgeRetention 锁定沿语义(与 register/WS 路径同款):
// owner 暂不可达(传输错)沿保留——换活 owner 后同一指纹的下次 401 仍能送达;
// applied=true 消耗沿——同指纹重复调用不再 POST。
func TestChannelOwnerNotifyEdgeRetention(t *testing.T) {
	newChannelNotifyTestEnv(t)

	var mu sync.Mutex
	var count int
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	dead.Close() // 立即连接拒绝 = 传输错误
	t.Setenv(SessionOwnerAddrEnv, dead.URL)
	svc := &AgentService{}

	// 沿保留:不可达不消耗——死 server 收不到任何请求(count=0),
	// 「沿仍在」由下一步换活 owner 后无需复位即可送达来证明。
	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-a")
	mu.Lock()
	if count != 0 {
		t.Fatalf("dead owner requests = %d, want 0 (server closed, nothing to receive)", count)
	}
	mu.Unlock()

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/agent-auth-rejected" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{"applied": true, "ignored": ""},
		})
	}))
	defer live.Close()
	t.Setenv(SessionOwnerAddrEnv, live.URL)
	svc2 := &AgentService{}

	// 换活 owner,同指纹未消耗的沿允许送达一次。
	svc2.NotifyOwnerAuthRejectedForChannel("Bearer fp-a")
	mu.Lock()
	if count != 1 {
		mu.Unlock()
		t.Fatalf("live owner notifications = %d, want 1 (dead attempt contributed 0)", count)
	}
	mu.Unlock()

	// applied=true 已消耗沿:同指纹重复调用静默。
	svc2.NotifyOwnerAuthRejectedForChannel("Bearer fp-a")
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("notifications after applied consumed edge, want still 1, got %d", count)
	}
}

// TestChannelOwnerNotifySkipsNonAgentRuntime 锁定:非 agent 运行时(session
// owner 本进程)永不通知——它自己就是恢复链的执行者。
func TestChannelOwnerNotifySkipsNonAgentRuntime(t *testing.T) {
	newChannelNotifyTestEnv(t)
	t.Setenv(AgentRuntimeEnv, "0")

	var mu sync.Mutex
	var count int
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	defer owner.Close()
	t.Setenv(SessionOwnerAddrEnv, owner.URL)
	resetChannelOwnerNotifyForTest()
	svc := &AgentService{}

	svc.NotifyOwnerAuthRejectedForChannel("Bearer fp-x")

	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Fatalf("owner received %d notifications in non-agent runtime, want 0", count)
	}
}
