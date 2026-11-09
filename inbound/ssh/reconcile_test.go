package ssh

// reconcile 单测(统一口收编后,spec §6.4):入口不再自持监听,期望态的
// 「起/停/重启/幂等」观测面从「端口监听+横幅」换成「IsRunning + 注入缝上
// 的真实握手」。port 相关断言随独立监听退役删除(customer.ssh_ingress.port
// 运行面忽略,见 config.go 废弃标注;schema 清理在下批次)。

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// bridgedPair 构造写解耦的双工对:两条 net.Pipe 经泵协程互转,任一侧的写
// 都被泵异步消费。裸 net.Pipe 是双向同步管道,而 gossh 两端握手都先写版本
// ——两侧同时阻塞在 Write 上互等,直接握手必死锁;桥接后与真实 TCP 的
// 缓冲语义对齐。
func bridgedPair() (client, server net.Conn) {
	c1, s1 := net.Pipe()
	c2, s2 := net.Pipe()
	go func() { _, _ = io.Copy(s2, s1); _ = s2.Close() }()
	go func() { _, _ = io.Copy(s1, s2); _ = s1.Close() }()
	return c1, c2
}

// handshakeViaIngress 经 HandleIngressConn 注入一条桥接连接并跑完整 gossh
// 握手(版本交换+KEX+认证)。nil=认证贯通。注入路径与旧监听路径走同一
// handleConn,故本 helper 即「服务面可用」的最强观测。
func handshakeViaIngress(t *testing.T, signer gossh.Signer) error {
	t.Helper()
	client, server := bridgedPair()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	go HandleIngressConn(server)
	cfg := &gossh.ClientConfig{
		User:            "ingress",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, _, _, err := gossh.NewClientConn(client, "127.0.0.1:0", cfg)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// injectInflightConn 注入一条「握手在途」的连接:客户端版本串已交服务端,
// 服务端已回写版本/KEXINIT(数据在途未被消费)——这是重启判定里「在途
// 连接」的最深可观测点。重启=快照拆除,连接死亡;不重启=数据仍可读。
func injectInflightConn(t *testing.T) net.Conn {
	t.Helper()
	client, server := bridgedPair()
	t.Cleanup(func() { _ = client.Close() })
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	go HandleIngressConn(server)
	if _, err := client.Write([]byte("SSH-2.0-reconcile-inflight\r\n")); err != nil {
		t.Fatalf("write client version: %v", err)
	}
	return client
}

// assertConnAlive 断言在途连接仍有数据可读(服务端版本首字节),即未被
// 期望态不变的重启拆除。
func assertConnAlive(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("in-flight conn must survive a no-op reconcile, read: %v", err)
	}
	if buf[0] != 'S' {
		t.Fatalf("expected SSH server banner, got %q", buf[0])
	}
}

// assertConnDead 断言在途连接已被拆除:先排干桥接泵的在途存量字节,随后
// 必须读到 EOF/断管(拆除信号)。静默超时不算拆除——连接仍开着等输入。
func assertConnDead(t *testing.T, conn net.Conn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buf := make([]byte, 1)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		_, err := conn.Read(buf)
		if err == nil {
			continue // 在途存量数据,继续等拆除信号
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue // 静默但未关:尚未拆除,继续等
		}
		return // EOF / closed pipe:已拆除
	}
	t.Fatal("in-flight conn must be torn down by a restart-inducing reconcile")
}

func TestReconcileStartsStopsAndRestartsOnChange(t *testing.T) {
	hostKeyPath := filepath.Join(t.TempDir(), IngressHostKeyFileName)
	t.Cleanup(func() { Reconcile(nil, hostKeyPath, nil) })

	humanPub, humanSigner := newTestKey(t)
	otherPub, otherSigner := newTestKey(t)

	// 期望停(rc=nil):初始即未跑,no-op 不 panic。
	Reconcile(nil, hostKeyPath, nil)
	if IsRunning() {
		t.Fatal("reconciled ingress must start stopped")
	}

	// 期望跑:起后服务面贯通(注入连接+载荷公钥可认证)。
	rc := &RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}
	Reconcile(rc, hostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("reconciled ingress must run after non-nil rc")
	}
	if err := handshakeViaIngress(t, humanSigner); err != nil {
		t.Fatalf("handshake through reconciled ingress: %v", err)
	}

	// 幂等:同期望态重复 reconcile 不重启——在途握手连接存活。每次提交
	// 都传新 localNodeID 方法值/闭包实例,闭包身份不得触发重启。
	inflight := injectInflightConn(t)
	Reconcile(rc, hostKeyPath, func() string { return "node-unit-again" })
	if !IsRunning() {
		t.Fatal("identical reconcile must keep the ingress running")
	}
	assertConnAlive(t, inflight)

	// 凭证变更:重启——在途连接被拆除,新钥通、旧钥拒。顺序刻意先新后旧:
	// 旧钥被拒会计入限速退避,先做阴性检查会把紧随的阳性握手掐死在
	// 握手前(同一注入源 IP,限速键相同)。
	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{otherPub}}, hostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("credential change must restart into a running ingress")
	}
	assertConnDead(t, inflight)
	if err := handshakeViaIngress(t, otherSigner); err != nil {
		t.Fatalf("new credential must authenticate after rotation: %v", err)
	}
	if err := handshakeViaIngress(t, humanSigner); err == nil {
		t.Fatal("old credential must be rejected after key rotation")
	}

	// 期望停(rc=nil):停机+注入缝拒收+观测面归零。
	Reconcile(nil, hostKeyPath, nil)
	if IsRunning() {
		t.Fatal("reconciled ingress must stop on nil rc")
	}
	client, server := net.Pipe()
	defer client.Close()
	if HandleIngressConn(server) {
		t.Fatal("HandleIngressConn must report false after stop")
	}

	// 停后重复期望停:幂等 no-op。
	Reconcile(nil, hostKeyPath, nil)
	if IsRunning() {
		t.Fatal("repeated nil reconcile must stay stopped")
	}
}

// TestReconcileCredentialReorderDoesNotRestart 钉死凭证集合语义:同凭证集
// 不同列表顺序的两次 Reconcile 是同一期望态(Marshal 字节排序后比对),
// 不触发重启——在途连接存活。顺序敏感实现会在无害重排时掐断在途连接。
func TestReconcileCredentialReorderDoesNotRestart(t *testing.T) {
	hostKeyPath := filepath.Join(t.TempDir(), IngressHostKeyFileName)
	t.Cleanup(func() { Reconcile(nil, hostKeyPath, nil) })

	humanPub, _ := newTestKey(t)
	meshPub, _ := newTestKey(t)
	extraPub, _ := newTestKey(t)

	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub, extraPub}, MeshKeys: []gossh.PublicKey{meshPub}},
		hostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("reconciled ingress must be running")
	}
	inflight := injectInflightConn(t)

	// 同凭证集、列表顺序全反:期望态等价 → 不重启(在途连接存活)。
	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{extraPub, humanPub}, MeshKeys: []gossh.PublicKey{meshPub}},
		hostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("reconciled ingress must stay running across reorder")
	}
	assertConnAlive(t, inflight)
}

// TestReconcileFailedStartStaysStoppedAndRetries 钉住「失败仅日志不上抛、
// 失败不粘死」:起服失败(host key 目录不可创建——用普通文件冒充父目录)
// 停留止态;障碍消除后同期望态再 reconcile 正常起服。独立监听退役后,
// 绑冲突失败模式已不存在,host key 是仅存的 Start 失败路径。
func TestReconcileFailedStartStaysStoppedAndRetries(t *testing.T) {
	t.Cleanup(func() { Reconcile(nil, "", nil) })

	humanPub, humanSigner := newTestKey(t)

	// 父路径是普通文件 → MkdirAll 失败 → host key 无法落盘 → 起服失败。
	fileParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("create plain file: %v", err)
	}
	badHostKeyPath := filepath.Join(fileParent, IngressHostKeyFileName)
	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, badHostKeyPath, func() string { return "node-unit" })
	if IsRunning() {
		t.Fatal("reconcile with unusable host key dir must stay stopped")
	}

	// 障碍消除(换成可用路径)后同期望态再次 reconcile:重试起服。
	goodHostKeyPath := filepath.Join(t.TempDir(), IngressHostKeyFileName)
	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}, goodHostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("reconcile must retry start once the host key path is usable")
	}
	if err := handshakeViaIngress(t, humanSigner); err != nil {
		t.Fatalf("handshake through retried ingress: %v", err)
	}
}
