// sessions_mgmt_test.go — 会话管理能力（踢出/黑名单）的行为钉：
// ① 黑名单活视图在认证期拦截（mesh 按指纹/device_id、bare 按指纹），命中
//
//	不进台账；② 指纹从认证 Permissions 落台账；③ Kick 关连接落 Kicked
//	关闭历史；④ 同 mesh 节点的多条连接共存（台账纯观测，每连接一条会话）。
package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"

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

// TestMeshSameDeviceConnectionsCoexist 同一 mesh 设备的多条连接必须共存
// (2026-11-24 审计实证的缺陷):出口侧 ExitClient 对每条转发流各开一条 SSH
// 连接(「每条连接一次独立握手,无池」),而台账登记曾把「同 device_id 的新
// 连接」当作重连、认证瞬间关闭旧连接——用生产 ExitClient 实测:第 2 条流建立
// 后第 1 条 1~2ms 内 EOF,5 条并发只剩最后 1 条。也就是说转发端上任一对端
// 同一时刻至多 1 条转发生效流,并发请求/流式响应互相掐。
//
// 「重连替换旧会话」的前提是「一个节点一条连接」,与每流一连接模型不兼容;
// 台账回归纯观测(每条连接一条会话),管理端踢出走 Kick。
func TestMeshSameDeviceConnectionsCoexist(t *testing.T) {
	// 回显目标 + 管线桩:把注入通道接到回显服务(证明流端到端活着)。
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		up, err := net.Dial("tcp", echo.Addr().String())
		if err != nil {
			return err
		}
		defer up.Close()
		go func() { _, _ = io.Copy(up, conn) }()
		_, _ = io.Copy(conn, up)
		return nil
	})

	meshKey, meshSigner := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		MeshKeys:    []MeshKey{{Key: meshKey, DeviceID: "dev-b"}},
		ForwardGate: func() bool { return true },
	})

	openFlow := func(name string) (gossh.Conn, gossh.Channel) {
		t.Helper()
		conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
		if err != nil {
			t.Fatalf("dial %s: %v", name, err)
		}
		ch, _, err := conn.OpenChannel(channelMeshExit, meshTCPExtra("example.org", 443))
		if err != nil {
			t.Fatalf("open mesh-exit %s: %v", name, err)
		}
		return conn, ch
	}
	roundtrip := func(ch gossh.Channel, payload string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			if _, err := ch.Write([]byte(payload)); err != nil {
				done <- fmt.Errorf("write %q: %w", payload, err)
				return
			}
			buf := make([]byte, len(payload))
			if _, err := io.ReadFull(ch, buf); err != nil {
				done <- fmt.Errorf("read echo %q: %w", payload, err)
				return
			}
			if string(buf) != payload {
				done <- fmt.Errorf("echo mismatch: %q", buf)
				return
			}
			done <- nil
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("roundtrip %q did not complete (stream torn down or stalled)", payload)
		}
	}

	conn1, ch1 := openFlow("#1")
	roundtrip(ch1, "ping-1")
	waitSession(t, s, func(s Session) bool { return s.Form == FormMesh })

	// 第 2 条流:同一设备标签、同一把钥。
	conn2, ch2 := openFlow("#2")
	roundtrip(ch2, "ping-2")
	// 第 1 条流必须仍然活着(旧实现:第 2 条认证瞬间顶掉第 1 条)。
	roundtrip(ch1, "ping-1-after-#2")
	roundtrip(ch2, "ping-2-after-#2")

	// 第 3 条:只认证、不开任何通道,同样不得影响在途流。
	conn3, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("dial #3: %v", err)
	}
	_, _, _ = conn3.SendRequest("keepalive@openssh.com", true, nil)
	roundtrip(ch1, "ping-1-after-idle-#3")
	roundtrip(ch2, "ping-2-after-idle-#3")

	// 台账:三条在线、零关闭、零 kicked。
	deadline := time.Now().Add(3 * time.Second)
	for len(s.ActiveSessions()) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(s.ActiveSessions()); n != 3 {
		t.Fatalf("active sessions = %d, want 3 (one per connection)", n)
	}
	if closed := s.ClosedSessions(); len(closed) != 0 {
		t.Fatalf("closed history = %d, want 0 (no connection may be evicted)", len(closed))
	}
	_ = conn1.Close()
	_ = conn2.Close()
	_ = conn3.Close()
	waitClosed(t, s, 3)
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
