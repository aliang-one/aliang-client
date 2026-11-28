package services

// 远程协助 P3:setRemoteConnectionState dirty-check——状态与消息均未变化时
// 不落盘(家族级 perf:handleRemoteAgentMessage 几乎每条下行帧都先调一次
// setRemoteConnectionState → saveStateLocked 全量重写 agent_state.json,
// terminal.input 每击键 / rrweb 页面镜像每帧 / heartbeat.ack 每拍都在白写)。
//
// 断言手法:对 agentStatePath 文件做 mtime+size 指纹对比(照
// agent_state_atomic_test.go 的 env 隔离与路径解析)。「跳过落盘」是负向断言,
// 文件内容比对会被 RFC3339 秒级 LastSyncAt 掩盖(同秒内两次落盘字节相同,
// 假阴性),mtime+size 对「文件是否被 tmp+rename 重写」是精确信号——没写就
// 没动,写了 rename 必换 mtime。
//
// 锁纪律(2026-11-22 死锁定案同源):s.mu 非重入,setRemoteConnectionState
// 自取锁——测试调用它时**不得**持 s.mu;只有直读/直改 state 字段才持锁。

import (
	"encoding/json"
	"os"
	"testing"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func newDirtyCheckService(t *testing.T) *AgentService {
	t.Helper()
	// 隔离:agentStatePath 非 user-agent 分支走 cache 目录,ALIANG_DATA_DIR/
	// ALIANG_CACHE_DIR 直指 TempDir + cache 单例重置(照
	// agent_state_atomic_test.go / assistRrEnvHygiene 同款;Cleanup LIFO 先于
	// t.Setenv 还原,防单例指着已删 temp 目录)。
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		cache.ResetCacheDirForTest()
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	return NewAgentService()
}

func dirtyCheckStatePath(t *testing.T) string {
	t.Helper()
	path, err := agentStatePath()
	if err != nil {
		t.Fatalf("agentStatePath() error = %v", err)
	}
	return path
}

// stateFileFingerprint 落盘指纹:mtime + size。「未重写」判据见文件头注。
func stateFileFingerprint(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	return info
}

func requireSameFile(t *testing.T, path string, before os.FileInfo, why string) {
	t.Helper()
	after := stateFileFingerprint(t, path)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("%s: agent_state.json must not be rewritten (before mtime=%v size=%d, after mtime=%v size=%d)",
			why, before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

func requirePersistedState(t *testing.T, path string, check func(*agentState)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	var persisted agentState
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted agent_state.json is not valid JSON: %v (raw=%s)", err, string(raw))
	}
	check(&persisted)
}

func TestSetRemoteConnectionStateSkipsSaveWhenUnchanged(t *testing.T) {
	s := newDirtyCheckService(t)
	path := dirtyCheckStatePath(t)

	// 首次迁移(false/空 → false/probe):真实变化,必须落盘,建立基线文件。
	s.setRemoteConnectionState(false, "probe", "m1")
	base := stateFileFingerprint(t, path)
	requirePersistedState(t, path, func(p *agentState) {
		if p.LastSyncMessage != "m1" {
			t.Fatalf("persisted last_sync_message = %q, want m1", p.LastSyncMessage)
		}
	})

	// 同三元组重复调用(连接活性拍/每击键/每帧的实态):内存戳照常刷新,
	// 但文件必须一个字节都不动。
	s.setRemoteConnectionState(false, "probe", "m1")
	s.mu.Lock()
	if s.state.LastSyncStatus != "probe" || s.state.LastSyncMessage != "m1" || s.state.RemoteConnected {
		t.Fatalf("in-memory triple must stay current after skipped save: %+v", s.state)
	}
	if s.state.LastSyncAt == "" {
		t.Fatal("in-memory LastSyncAt must still refresh on skipped save")
	}
	s.mu.Unlock()
	requireSameFile(t, path, base, "unchanged triple repeat")

	// 第三次(仍同三元组)继续跳过:高频臂上只该有一次基线写。
	s.setRemoteConnectionState(false, "probe", "m1")
	requireSameFile(t, path, base, "unchanged triple repeat 2")
}

func TestSetRemoteConnectionStateSavesOnSemanticChange(t *testing.T) {
	s := newDirtyCheckService(t)
	path := dirtyCheckStatePath(t)

	s.setRemoteConnectionState(false, "probe", "m1")

	// 状态变化(connected 翻转)→ 必须落盘。
	s.setRemoteConnectionState(true, "online", "")
	requirePersistedState(t, path, func(p *agentState) {
		if !p.RemoteConnected {
			t.Fatal("persisted remote_connected = false, want true")
		}
		if p.LastSyncStatus != "online" {
			t.Fatalf("persisted last_sync_status = %q, want online", p.LastSyncStatus)
		}
	})

	// 消息变化(status 同、message 换)→ 必须落盘。
	s.setRemoteConnectionState(true, "online", "handshake 401; session refresh in flight")
	requirePersistedState(t, path, func(p *agentState) {
		if p.LastSyncMessage != "handshake 401; session refresh in flight" {
			t.Fatalf("persisted last_sync_message = %q", p.LastSyncMessage)
		}
	})

	// 状态变化(offline 断连)→ 必须落盘,且不回写 Registered(粘性)。
	s.setRemoteConnectionState(false, "disconnected", "read loop closed")
	s.mu.Lock()
	sticky := s.state.Registered
	s.mu.Unlock()
	if !sticky {
		t.Fatal("Registered must stay sticky across disconnect")
	}
	requirePersistedState(t, path, func(p *agentState) {
		if p.RemoteConnected {
			t.Fatal("persisted remote_connected = true, want false")
		}
		if p.LastSyncStatus != "disconnected" {
			t.Fatalf("persisted last_sync_status = %q, want disconnected", p.LastSyncStatus)
		}
	})
}

func TestSetRemoteConnectionStateRegisteredFlipForcesSave(t *testing.T) {
	s := newDirtyCheckService(t)
	path := dirtyCheckStatePath(t)

	// 语义边界夹具:connected=true 已在内存(如 touchRemoteConnectionOnline
	// 免落盘臂先到),但 Registered 尚未翻(true 连接须置位并持久化)。
	// 三元组全同,唯一变化是 Registered 翻转——dirty 判别必须把它算作变化。
	s.mu.Lock()
	s.state.RemoteConnected = true
	s.state.LastSyncStatus = "online"
	s.state.LastSyncMessage = ""
	s.state.Registered = false
	if err := s.saveStateLocked(); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	s.mu.Unlock()

	s.setRemoteConnectionState(true, "online", "")
	requirePersistedState(t, path, func(p *agentState) {
		if !p.Registered {
			t.Fatal("persisted registered = false, want true (flip must force a save)")
		}
	})

	// 翻转落盘后再来同三元组:Registered 已真,纯无变化 → 回到跳过。
	after := stateFileFingerprint(t, path)
	s.setRemoteConnectionState(true, "online", "")
	requireSameFile(t, path, after, "post-flip unchanged repeat")
}
