package ssh

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// freeTCPPort 预约一个空闲回环端口(listen :0 取口后立即关)。TOCTOU 窗口
// 极小,仅测试选口用;端口变更/占位用例靠它避开固定口与并行环境的碰撞。
func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

// loopbackDialAddr 把监听地址归一到 127.0.0.1:host(ServiceBindHost 默认即
// 回环,显式归一让 --host 覆盖环境也稳——同 server_test.go dialAddr 口径)。
func loopbackDialAddr(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr %s: %v", addr, err)
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// assertSSHListening 断言地址在监听且是 SSH 服务(连接后读到版本横幅首字节)。
func assertSSHListening(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s must succeed: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read SSH banner from %s: %v", addr, err)
	}
	if buf[0] != 'S' {
		t.Fatalf("unexpected banner start %q from %s, want 'S' (SSH-...)", buf[0], addr)
	}
}

// assertDialRefused 断言地址连接层拒绝(未监听)。
func assertDialRefused(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("dial %s must be refused, got connection", addr)
	}
}

func TestReconcileStartsStopsAndRestartsOnChange(t *testing.T) {
	hostKeyPath := filepath.Join(t.TempDir(), IngressHostKeyFileName)
	t.Cleanup(func() { Reconcile(nil, hostKeyPath, nil) })

	humanPub, humanSigner := newTestKey(t)
	otherPub, _ := newTestKey(t)

	// 期望停(rc=nil):初始即未跑,no-op 不 panic。
	Reconcile(nil, hostKeyPath, nil)
	if IsRunning() {
		t.Fatal("reconciled ingress must start stopped")
	}

	// 期望跑:Port=0 随机口,起后可拨且读到 SSH 横幅。
	rc := &RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub}}
	Reconcile(rc, hostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("reconciled ingress must run after non-nil rc")
	}
	addr1 := Addr()
	if addr1 == "" {
		t.Fatal("running ingress must expose listen addr")
	}
	assertSSHListening(t, loopbackDialAddr(t, addr1))

	// 幂等:同期望态重复 reconcile 不重启(监听口不变——每次提交都传新
	// localNodeID 方法值/闭包实例,闭包身份不得触发重启)。
	Reconcile(rc, hostKeyPath, func() string { return "node-unit-again" })
	if got := Addr(); got != addr1 {
		t.Fatalf("identical reconcile must not restart: addr %q -> %q", addr1, got)
	}

	// 凭证变更:重启——Port=0 下新实例换随机口,Addr 变化且旧口失效。
	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{otherPub}}, hostKeyPath, func() string { return "node-unit" })
	addr2 := Addr()
	if addr2 == "" || addr2 == addr1 {
		t.Fatalf("credential change must restart with a new listener: %q -> %q", addr1, addr2)
	}
	assertDialRefused(t, loopbackDialAddr(t, addr1))
	assertSSHListening(t, loopbackDialAddr(t, addr2))

	// 端口变更:重启到指定端口;重启后的实例服务面完整(合法凭证可认证)。
	fixed := freeTCPPort(t)
	Reconcile(&RunConfig{Port: fixed, AuthorizedKeys: []gossh.PublicKey{humanPub}}, hostKeyPath, func() string { return "node-unit" })
	addr3 := Addr()
	if addr3 == "" || addr3 == addr2 {
		t.Fatalf("port change must restart: %q -> %q", addr2, addr3)
	}
	assertSSHListening(t, loopbackDialAddr(t, addr3))
	conn, err := dialIngress(t, loopbackDialAddr(t, addr3), gossh.PublicKeys(humanSigner))
	if err != nil {
		t.Fatalf("authenticated dial after port-change restart: %v", err)
	}
	_ = conn.Close()

	// 期望停(rc=nil):停机+连接层拒绝+观测面归零。
	Reconcile(nil, hostKeyPath, nil)
	if IsRunning() {
		t.Fatal("reconciled ingress must stop on nil rc")
	}
	if got := Addr(); got != "" {
		t.Fatalf("stopped ingress must report empty addr, got %q", got)
	}
	assertDialRefused(t, loopbackDialAddr(t, addr3))

	// 停后重复期望停:幂等 no-op。
	Reconcile(nil, hostKeyPath, nil)
	if IsRunning() {
		t.Fatal("repeated nil reconcile must stay stopped")
	}
}

// TestReconcileCredentialReorderDoesNotRestart 钉死凭证集合语义:同凭证集
// 不同列表顺序的两次 Reconcile 是同一期望态(Marshal 字节排序后比对),
// 不触发重启——监听口不变。顺序敏感实现会在无害重排时掐断在途连接。
func TestReconcileCredentialReorderDoesNotRestart(t *testing.T) {
	hostKeyPath := filepath.Join(t.TempDir(), IngressHostKeyFileName)
	t.Cleanup(func() { Reconcile(nil, hostKeyPath, nil) })

	humanPub, _ := newTestKey(t)
	meshPub, _ := newTestKey(t)
	extraPub, _ := newTestKey(t)

	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{humanPub, extraPub}, MeshKeys: []gossh.PublicKey{meshPub}},
		hostKeyPath, func() string { return "node-unit" })
	addr1 := Addr()
	if addr1 == "" || !IsRunning() {
		t.Fatal("reconciled ingress must be running")
	}

	// 同凭证集、列表顺序全反:期望态等价 → 不重启(Addr 不变)。
	Reconcile(&RunConfig{AuthorizedKeys: []gossh.PublicKey{extraPub, humanPub}, MeshKeys: []gossh.PublicKey{meshPub}},
		hostKeyPath, func() string { return "node-unit" })
	if got := Addr(); got != addr1 {
		t.Fatalf("credential reorder must not restart: addr %q -> %q", addr1, got)
	}
	if !IsRunning() {
		t.Fatal("reconciled ingress must stay running across reorder")
	}
}

func TestReconcileFailedStartStaysStoppedAndRetries(t *testing.T) {
	hostKeyPath := filepath.Join(t.TempDir(), IngressHostKeyFileName)
	t.Cleanup(func() { Reconcile(nil, hostKeyPath, nil) })

	humanPub, _ := newTestKey(t)
	occupied := freeTCPPort(t)

	// 占住端口:期望态绑不上 → 停留止态(不上抛、不 panic)。
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(occupied))))
	if err != nil {
		t.Fatalf("occupy port %d: %v", occupied, err)
	}
	Reconcile(&RunConfig{Port: occupied, AuthorizedKeys: []gossh.PublicKey{humanPub}}, hostKeyPath, func() string { return "node-unit" })
	if IsRunning() {
		t.Fatal("reconcile with unbindable port must stay stopped")
	}

	// 释放端口后同期望态再次 reconcile:重试起服(失败不粘死)。
	_ = l.Close()
	Reconcile(&RunConfig{Port: occupied, AuthorizedKeys: []gossh.PublicKey{humanPub}}, hostKeyPath, func() string { return "node-unit" })
	if !IsRunning() {
		t.Fatal("reconcile must retry start after the bind conflict is gone")
	}
	addr := Addr()
	if addr == "" {
		t.Fatal("retried ingress must expose listen addr")
	}
	assertSSHListening(t, loopbackDialAddr(t, addr))
}
