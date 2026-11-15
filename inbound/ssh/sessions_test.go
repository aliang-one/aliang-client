// sessions_test.go — 接入客户端台账（服务端「谁连进来了」）的行为钉：
// 用真实握手链路（serveConnVia 注入范式 + 新钥认证）验证登记/通道计数/
// 关闭历史三个生命周期段，以及 form/device_id 从认证 Permissions 到台账
// 的传递（bare 与 mesh 两种形态）。
package ssh

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"

	gossh "golang.org/x/crypto/ssh"
)

// waitSession 等待台账出现满足谓词的在线会话（握手→登记是异步链，有界等待）。
func waitSession(t *testing.T, s *Server, pred func(Session) bool) Session {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, sess := range s.ActiveSessions() {
			if pred(sess) {
				return sess
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session not found within deadline")
	return Session{}
}

func TestSessionRegisteredOnAuth(t *testing.T) {
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
	if sess.SourceIP == "" || sess.SourceIP != "127.0.0.1" {
		t.Fatalf("source_ip = %q, want 127.0.0.1", sess.SourceIP)
	}
	if sess.ClosedAt != 0 {
		t.Fatalf("live session must have closed_at=0, got %d", sess.ClosedAt)
	}
	if sess.Duration < 0 {
		t.Fatalf("live duration must be >= 0, got %d", sess.Duration)
	}
}

func TestSessionChannelsCountedAndClosed(t *testing.T) {
	humanPub, humanSigner := newTestKey(t)
	s := NewServer(RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 管线桩：读干通道（连接保持到客户端关闭）
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		_, _ = io.Copy(io.Discard, conn)
		return nil
	})

	addr := serveConnVia(t, s)
	conn, err := dialIngress(t, addr, gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch := openDirectTCPIP(t, conn, "127.0.0.1", 9)

	sess := waitSession(t, s, func(s Session) bool { return s.Channels == 1 })
	_ = sess

	// 通道关闭 → 计数归零
	ch.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		live := s.ActiveSessions()
		if len(live) == 1 && live[0].Channels == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 连接断开 → 会话转入关闭历史
	_ = conn.Close()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.ClosedSessions()) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	closed := s.ClosedSessions()
	if len(closed) != 1 {
		t.Fatalf("closed history = %d entries, want 1", len(closed))
	}
	if closed[0].ClosedAt == 0 || closed[0].Duration < 0 {
		t.Fatalf("closed session must have closed_at/duration set: %+v", closed[0])
	}
	if len(s.ActiveSessions()) != 0 {
		t.Fatalf("active list must be empty after disconnect, got %d", len(s.ActiveSessions()))
	}
}

func TestSessionMeshFormCarriesDeviceID(t *testing.T) {
	meshKey, meshSigner := newTestKey(t)
	s := NewServer(RunConfig{
		MeshKeys: []MeshKey{{Key: meshKey, DeviceID: "dev-win"}},
	}, filepath.Join(t.TempDir(), IngressHostKeyFileName))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	addr := serveConnVia(t, s)
	conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sess := waitSession(t, s, func(s Session) bool { return s.Form == FormMesh })
	if sess.DeviceID != "dev-win" {
		t.Fatalf("device_id = %q, want dev-win", sess.DeviceID)
	}
}
