package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/outbound/proxy/ssh/sshtest"
	gossh "golang.org/x/crypto/ssh"
)

// assertNoGoroutineLeak 等待 goroutine 数回落到 baseline+slack 以内,
// 给「链路收尾后无泄漏」以硬证据。与 sshtest 包内同名范式等价(彼处未导出)。
func assertNoGoroutineLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= baseline+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine 泄漏: 收尾后 %d 个 > 基线 %d+2", now, baseline)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDialChainZeroHopPassword(t *testing.T) {
	srv, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	kh := newTestKnownHosts(t)
	d := newChainDialer(finalNode(addr, "u", "p", "", ""), nil, kh)

	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("dialChain: %v", err)
	}
	defer client.Close()

	// TOFU 写侧闭环:真实握手路径必须把 sshd 的 host key 指纹写进库
	//(known_hosts 单元测试只验证过回调本身,这里验证 dialChain 真接线)。
	entries := kh.List()
	want := gossh.FingerprintSHA256(srv.HostKey.PublicKey())
	if len(entries) != 1 {
		t.Fatalf("known_hosts entries = %d, want 1", len(entries))
	}
	if entries[0].Fingerprint != want {
		t.Fatalf("known_hosts fingerprint = %s, want %s", entries[0].Fingerprint, want)
	}
}

func TestDialChainWrongPassword(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	d := newChainDialer(finalNode(addr, "u", "WRONG", "", ""), nil, newTestKnownHosts(t))

	if _, err := d.dialChain(context.Background()); err == nil {
		t.Fatal("wrong password must fail")
	}
}

func TestDialChainHostKeyMismatch(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	kh := newTestKnownHosts(t)
	// 模拟「服务器换 key 后复连」:known_hosts 已记录一个非本 sshd 的指纹,
	// 复连时服务器呈现真实 key,必须拒连。(计划骨架的「两台 Start 模拟」
	// 不成立:两次 Start 各占随机端口,known_hosts 按 host:port 分键,
	// 第二台只是首连 TOFU,不会冲突;预写指纹才是等价且真实的场景。)
	other := testSigner(t)
	if err := kh.CallbackFor(addr)("h", &net.TCPAddr{}, other.PublicKey()); err != nil {
		t.Fatalf("seed known_hosts: %v", err)
	}
	d := newChainDialer(finalNode(addr, "u", "p", "", ""), nil, kh)
	if _, err := d.dialChain(context.Background()); err == nil {
		t.Fatal("host key mismatch must be rejected")
	}
}

// TestDialChainCascadeCloseNoLeak 钉住成功路径的级联关闭:建 2 节点链
// (跳板+终点)后只 Close 返回的 client——若 Close 不级联关闭跳板 client,
// 跳板 client 的 mux goroutine 与跳板 sshd 侧 handle goroutine 都会滞留,
// 泄漏断言必红。
// 基线在服务器起好之后取:排除 serve goroutine 的干扰,断言只对准
// dialChain 引入的链路 goroutine。断言前不 Close sshtest 服务器(其
// Close 会强关已接受连接,恰好掩盖客户端侧泄漏),服务器由 t.Cleanup 收尾。
func TestDialChainCascadeCloseNoLeak(t *testing.T) {
	_, jumpAddr := sshtest.Start(t, sshtest.WithUsername("ju"), sshtest.WithPassword("jp"))
	_, finalAddr := sshtest.Start(t, sshtest.WithUsername("fu"), sshtest.WithPassword("fp"))
	baseline := runtime.NumGoroutine()

	d := newChainDialer(finalNode(finalAddr, "fu", "fp", "", ""),
		[]nodeConfig{finalNode(jumpAddr, "ju", "jp", "", "")}, newTestKnownHosts(t))
	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("dialChain: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("cascade Close: %v", err)
	}
	// 幂等钉子:第二次 Close 返回与首次相同的结果(nil)且不 panic
	//(cascadeClient 文档承诺「重复 Close 返回首次结果」)。
	if err := client.Close(); err != nil {
		t.Fatalf("second Close must return the same nil result, got: %v", err)
	}
	assertNoGoroutineLeak(t, baseline)
}

// TestDialChainFailureClosesAllHopsNoLeak 钉住失败路径的全量清场:3 节点链
// (跳板×2+终点)在第 3 节点(终点)认证失败,dialChain 必须关闭全部已建
// clients——只关最近一跳会让第 1 跳板的 TCP 与两侧 goroutine 半开泄漏。
func TestDialChainFailureClosesAllHopsNoLeak(t *testing.T) {
	_, j1Addr := sshtest.Start(t, sshtest.WithUsername("j1"), sshtest.WithPassword("p1"))
	_, j2Addr := sshtest.Start(t, sshtest.WithUsername("j2"), sshtest.WithPassword("p2"))
	_, finalAddr := sshtest.Start(t, sshtest.WithUsername("fu"), sshtest.WithPassword("fp"))
	baseline := runtime.NumGoroutine()

	d := newChainDialer(finalNode(finalAddr, "fu", "WRONG", "", ""),
		[]nodeConfig{
			finalNode(j1Addr, "j1", "p1", "", ""),
			finalNode(j2Addr, "j2", "p2", "", ""),
		}, newTestKnownHosts(t))
	_, err := d.dialChain(context.Background())
	if err == nil {
		t.Fatal("wrong password at final hop must fail")
	}
	if !strings.Contains(err.Error(), "hop 2") {
		t.Fatalf("error must point at hop 2 (proving hops 0/1 succeeded), got: %v", err)
	}
	assertNoGoroutineLeak(t, baseline)
}

// TestDialChainOneJumpEcho 单跳板端到端:client → 跳板 sshd(direct-tcpip)
// → 终点 sshd → direct-tcpip → 回显服务器,验证 ProxyJump 语义与数据面通。
// 两台 sshd 都用默认转发(direct-tcpip → net.Dial):跳板转发到终点 sshd、
// 终点转发到回显目标,loopback 下与计划骨架里的 WithNextDial 等价且更简。
func TestDialChainOneJumpEcho(t *testing.T) {
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()

	_, jumpAddr := sshtest.Start(t, sshtest.WithUsername("ju"), sshtest.WithPassword("jp"))
	_, finalAddr := sshtest.Start(t, sshtest.WithUsername("fu"), sshtest.WithPassword("fp"))

	d := newChainDialer(finalNode(finalAddr, "fu", "fp", "", ""),
		[]nodeConfig{finalNode(jumpAddr, "ju", "jp", "", "")}, newTestKnownHosts(t))
	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("1-hop dialChain: %v", err)
	}
	defer client.Close()

	// 在最终 client 上开 direct-tcpip 到回显服务器,验证端到端。
	ch, err := client.Dial("tcp", echoAddr)
	if err != nil {
		t.Fatalf("final client.Dial: %v", err)
	}
	if _, err := ch.Write([]byte("ping")); err != nil {
		t.Fatalf("echo write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(ch, buf); err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q", buf)
	}
	_ = ch.Close()
}

// TestDialChainTwoJumpsEcho 双跳板端到端:client → 跳板1 → 跳板2 → 终点
// sshd → direct-tcpip → 回显,验证两级嵌套 direct-tcpip 穿透。
func TestDialChainTwoJumpsEcho(t *testing.T) {
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()

	_, j1Addr := sshtest.Start(t, sshtest.WithUsername("j1"), sshtest.WithPassword("p1"))
	_, j2Addr := sshtest.Start(t, sshtest.WithUsername("j2"), sshtest.WithPassword("p2"))
	_, finalAddr := sshtest.Start(t, sshtest.WithUsername("fu"), sshtest.WithPassword("fp"))

	d := newChainDialer(finalNode(finalAddr, "fu", "fp", "", ""),
		[]nodeConfig{
			finalNode(j1Addr, "j1", "p1", "", ""),
			finalNode(j2Addr, "j2", "p2", "", ""),
		}, newTestKnownHosts(t))
	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("2-jump dialChain: %v", err)
	}
	defer client.Close()

	ch, err := client.Dial("tcp", echoAddr)
	if err != nil {
		t.Fatalf("final client.Dial: %v", err)
	}
	payload := []byte("two-jump ping")
	if _, err := ch.Write(payload); err != nil {
		t.Fatalf("echo write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(ch, buf); err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo = %q", buf)
	}
	_ = ch.Close()
}

// TestDialChainPrivateKeyAuth 私钥认证:ed25519 → MarshalPrivateKey 出 PEM,
// sshd 用 WithAuthorized 收公钥,dialChain 纯私钥(无密码)认证成功。
func TestDialChainPrivateKeyAuth(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(block))

	_, addr := sshtest.Start(t, sshtest.WithUsername("ku"), sshtest.WithAuthorized(signer.PublicKey()))
	d := newChainDialer(finalNode(addr, "ku", "", pemStr, ""), nil, newTestKnownHosts(t))
	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("private key auth: %v", err)
	}
	defer client.Close()
}

// TestDialChainPrivateKeyWithPassphrase 带 passphrase 的加密私钥:
// MarshalPrivateKeyWithPassphrase 出加密 PEM,authMethods 走
// ParsePrivateKeyWithPassphrase 分支(v0.48 有此函数,已核实)。
func TestDialChainPrivateKeyWithPassphrase(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(block))

	_, addr := sshtest.Start(t, sshtest.WithUsername("ku"), sshtest.WithAuthorized(signer.PublicKey()))
	d := newChainDialer(finalNode(addr, "ku", "", pemStr, "secret"), nil, newTestKnownHosts(t))
	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("passphrase private key auth: %v", err)
	}
	defer client.Close()
}

// TestDialChainBadPrivateKey 坏 PEM fail-fast:authMethods 解析失败在建链
// 前即报错(不发起任何网络连接)。
func TestDialChainBadPrivateKey(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	d := newChainDialer(finalNode(addr, "u", "", "not a pem", ""), nil, newTestKnownHosts(t))
	_, err := d.dialChain(context.Background())
	if err == nil {
		t.Fatal("bad private key must fail dialChain")
	}
	if !strings.Contains(err.Error(), "parse private key") {
		t.Fatalf("error must mention parse private key, got: %v", err)
	}
}
