// sessions_mgmt_test.go — 会话管理能力（踢出/黑名单）的行为钉：
// ① 黑名单活视图在认证期拦截（mesh 按指纹/device_id、bare 按指纹），命中
//
//	不进台账；② 指纹从认证 Permissions 落台账；③ Kick 关连接落 Kicked
//	关闭历史；④ 同 mesh 节点重连替换旧会话（旧连接被断）。
package ssh

import (
	"path/filepath"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestBlockedMeshKeyRejectedAtAuth(t *testing.T) {
	meshKey, meshSigner := newTestKey(t)
	fp := gossh.FingerprintSHA256(meshKey)
	blocked := false
	s := NewServer(RunConfig{
		MeshKeys: []MeshKey{{Key: meshKey, DeviceID: "dev-win"}},
		// 活视图闭包：测试里翻转 blocked 变量模拟配置变更即时生效。
		Blocked: func(q BlockQuery) bool { return blocked },
	}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 未拉黑：认证通过、台账带指纹。
	addr := serveConnVia(t, s)
	conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("dial before block: %v", err)
	}
	sess := waitSession(t, s, func(s Session) bool { return s.Form == FormMesh })
	if sess.Fingerprint != fp {
		t.Fatalf("fingerprint = %q, want %q", sess.Fingerprint, fp)
	}
	_ = conn.Close()
	waitClosed(t, s, 1)

	// 拉黑（活视图翻转即生效，无需重建服务）：认证必败、台账无新增。
	blocked = true
	addr = serveConnVia(t, s)
	if _, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner)); err == nil {
		t.Fatal("dial after block must fail")
	}
	if n := len(s.ActiveSessions()); n != 0 {
		t.Fatalf("blocked key must not register, active=%d", n)
	}
	if n := len(s.ClosedSessions()); n != 1 {
		t.Fatalf("blocked key must not touch closed history, closed=%d", n)
	}

	// device_id 维度同样拦截（指纹放行、标签命中）。
	blocked = false
	s2 := NewServer(RunConfig{
		MeshKeys: []MeshKey{{Key: meshKey, DeviceID: "dev-win"}},
		Blocked:  func(q BlockQuery) bool { return q.DeviceID == "dev-win" },
	}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s2.Start(); err != nil {
		t.Fatalf("start s2: %v", err)
	}
	t.Cleanup(func() { _ = s2.Stop() })
	addr = serveConnVia(t, s2)
	if _, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner)); err == nil {
		t.Fatal("dial with blocked device_id must fail")
	}
}

func TestBlockedBareKeyRejectedAtAuth(t *testing.T) {
	humanPub, humanSigner := newTestKey(t)
	s := NewServer(RunConfig{
		AuthorizedKeys: []gossh.PublicKey{humanPub},
		Blocked:        func(q BlockQuery) bool { return q.Fingerprint == gossh.FingerprintSHA256(humanPub) },
	}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	addr := serveConnVia(t, s)
	if _, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner)); err == nil {
		t.Fatal("dial with blocked bare key must fail")
	}
	if n := len(s.ActiveSessions()); n != 0 {
		t.Fatalf("blocked bare key must not register, active=%d", n)
	}
}

func TestKickDisconnectsAndMarksHistory(t *testing.T) {
	humanPub, humanSigner := newTestKey(t)
	s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	addr := serveConnVia(t, s)
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sess := waitSession(t, s, func(s Session) bool { return s.Form == FormBare })
	if s.Kick("SHA256:nonexistent") {
		t.Fatal("kick with unknown fingerprint must return false")
	}
	if !s.Kick(sess.Fingerprint) {
		t.Fatal("kick by fingerprint must return true")
	}

	// 踢出后：客户端侧连接被断；会话转关闭历史且 Kicked=true。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.ClosedSessions()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	closed := s.ClosedSessions()
	if len(closed) != 1 {
		t.Fatalf("closed history = %d, want 1", len(closed))
	}
	if !closed[0].Kicked {
		t.Fatalf("kicked session must carry kicked=true: %+v", closed[0])
	}
	if n := len(s.ActiveSessions()); n != 0 {
		t.Fatalf("active after kick = %d, want 0", n)
	}

	// 客户端读侧最终会感知断开（有界等待）。
	done := make(chan struct{})
	go func() {
		conn.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client connection was not closed after kick")
	}
}

func TestMeshReconnectReplacesStaleSession(t *testing.T) {
	meshKey, meshSigner := newTestKey(t)
	s := NewServer(RunConfig{MeshKeys: []MeshKey{{Key: meshKey, DeviceID: "dev-win"}}},
		filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 第一条连接在线。
	addr := serveConnVia(t, s)
	conn1, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("dial #1: %v", err)
	}
	waitSession(t, s, func(s Session) bool { return s.Form == FormMesh })

	// 同节点重连：旧会话被替换（在线表恒 1 条），旧连接被断。
	conn2, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("dial #2: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.ClosedSessions()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	closed := s.ClosedSessions()
	if len(closed) != 1 {
		t.Fatalf("closed history = %d, want 1 (replaced stale session)", len(closed))
	}
	if !closed[0].Kicked {
		t.Fatal("replaced stale session must be marked kicked")
	}
	if n := len(s.ActiveSessions()); n != 1 {
		t.Fatalf("active after reconnect = %d, want 1", n)
	}
	_ = conn1.Close()
	_ = conn2.Close()
}

// waitClosed 有界等待关闭历史达到 n 条。
func waitClosed(t *testing.T, s *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.ClosedSessions()) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("closed history never reached %d", n)
}
