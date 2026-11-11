package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	gossh "golang.org/x/crypto/ssh"
)

// testIdentity 生成一把真 ed25519 测试身份:PEM 交给 ExitClient,signer/pub
// 供 sshd 侧准入比对——两侧同源,避免假钥被实现改动绕过。
func testIdentity(t *testing.T) (keyPEM string, signer gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	signer, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "mesh-test-identity")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

// meshTestSSHD 本包自建的最小真 sshd:固定 mesh 身份公钥准入(PublicKeyCallback
// 返回 nil error 即放行),收到 mesh-exit channel 时 Accept 并 goroutine 回显,
// ExtraData payload 逐条记录(SPEC §15.1 出口测试夹具)。channel 类型与
// ChannelMeshExit 字面量写死互锁:实现侧改常量值时本 helper 必红。
type meshTestSSHD struct {
	ln     net.Listener
	hostFP string // host key SHA256 指纹(PeerSpec.HostKey 直用的钉扎格式)

	mu       sync.Mutex
	payloads [][]byte // 每条 mesh-exit channel 的 ExtraData(到达序)
}

func newMeshTestSSHD(t *testing.T, admittedPub gossh.PublicKey) *meshTestSSHD {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader) // GenerateKey 返回 (pub, priv, err)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("new host signer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &meshTestSSHD{ln: ln, hostFP: gossh.FingerprintSHA256(hostSigner.PublicKey())}
	go s.serve(hostSigner, admittedPub)
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *meshTestSSHD) Addr() string { return s.ln.Addr().String() }

// HostKeyPinned 返回本 sshd host key 的 SHA256 指纹(SPEC §10.4 指纹格式)。
func (s *meshTestSSHD) HostKeyPinned() string { return s.hostFP }

// LastPayload 返回最近一条 mesh-exit ExtraData;尚无 channel 时返回 nil。
func (s *meshTestSSHD) LastPayload() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.payloads); n > 0 {
		return s.payloads[n-1]
	}
	return nil
}

func (s *meshTestSSHD) record(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, payload)
}

func (s *meshTestSSHD) serve(hostSigner gossh.Signer, admittedPub gossh.PublicKey) {
	for {
		tcp, err := s.ln.Accept()
		if err != nil {
			return // listener 已被 t.Cleanup 关闭
		}
		go s.handle(tcp, hostSigner, admittedPub)
	}
}

func (s *meshTestSSHD) handle(tcp net.Conn, hostSigner gossh.Signer, admittedPub gossh.PublicKey) {
	config := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), admittedPub.Marshal()) {
				return nil, errors.New("mesh test sshd: unknown client identity key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)
	_, chans, reqs, err := gossh.NewServerConn(tcp, config)
	if err != nil {
		_ = tcp.Close()
		return
	}
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "mesh-exit" {
			_ = newCh.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		// record 在 Accept 之前:OpenChannel 的确认由 Accept 发出,payload
		// 必然先于客户端 DialContext 返回被记录。
		s.record(newCh.ExtraData())
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go gossh.DiscardRequests(chReqs)
		go func() {
			_, _ = io.Copy(ch, ch) // 回显
			_ = ch.CloseWrite()
			_ = ch.Close()
		}()
	}
}

// silentListener 起一个 accept 后一字节不发的 TCP 监听(ssh banner 缺席:
// 客户端只能靠 SetDeadline 脱困,SPEC §5.1)。
func silentListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}

// TestExitClientDialTCP 矩阵 1:真 sshd 上完整走通 DialContext——ExtraData
// 原样到达服务端,双向字节回显(每连接独立 SSH 握手,无池,SPEC §5.1)。
func TestExitClientDialTCP(t *testing.T) {
	keyPEM, identity := testIdentity(t)
	sshd := newMeshTestSSHD(t, identity.PublicKey())

	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{sshd.Addr()},
		HostKey:  sshd.HostKeyPinned(),
		SSHUser:  "mesh",
	}, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	md := &M.Metadata{HostName: "example.com", DstPort: 443}
	conn, err := client.DialContext(context.Background(), md)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if got, want := sshd.LastPayload(), exitPayload(md); !bytes.Equal(got, want) {
		t.Fatalf("extra data = %s, want %s", got, want)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q", buf)
	}
}

// TestExitClientLanPreferredOverRelay 矩阵 2:relay=true 且 LAN 可达时只拨
// LAN,piko 拨号计数必须为 0(SPEC §7.3 LAN 优先)。
func TestExitClientLanPreferredOverRelay(t *testing.T) {
	keyPEM, identity := testIdentity(t)
	sshd := newMeshTestSSHD(t, identity.PublicKey())

	pikoCalls := 0
	pikoDial := func(context.Context, string) (net.Conn, error) {
		pikoCalls++
		return nil, errors.New("piko must not be dialed when LAN reachable")
	}
	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{sshd.Addr()},
		HostKey:  sshd.HostKeyPinned(),
		Relay:    true,
	}, keyPEM, "", pikoDial)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	// 显式 self 类是必须的:默认推断 custom 会让 piko 在 Admit 即被拦
	// (custom 禁公共中继),本用例就退化成「custom 禁中继」而非「LAN 优先
	// 于中继」——即使 transports 顺序被改成 [Piko, LAN] 它也照样绿。
	ctx := WithClass(context.Background(), meshproto.ClassSelf)
	conn, err := client.DialContext(ctx, &M.Metadata{HostName: "x.com:443"})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if pikoCalls != 0 {
		t.Fatalf("piko dialed %d times; LAN must win", pikoCalls)
	}
	if sshd.LastPayload() == nil {
		t.Fatal("sshd must have received the mesh-exit channel over LAN")
	}
}

// TestExitClientLanFailFallsBackToRelay 矩阵 3:LAN 全 refused → 落 piko
// (计数 1),整体错误为 nil;兜底连接同样完成握手与 channel open。
func TestExitClientLanFailFallsBackToRelay(t *testing.T) {
	keyPEM, identity := testIdentity(t)
	sshd := newMeshTestSSHD(t, identity.PublicKey())

	pikoCalls := 0
	pikoDial := func(ctx context.Context, endpointID string) (net.Conn, error) {
		pikoCalls++
		if endpointID != "dev-b.mesh" {
			return nil, fmt.Errorf("unexpected endpoint id %q", endpointID)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", sshd.Addr())
	}
	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{"127.0.0.1:1"}, // 必然 connection refused
		HostKey:  sshd.HostKeyPinned(),
		Relay:    true,
	}, keyPEM, "", pikoDial)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	// 显式 self 类:默认推断会是 custom(custom 禁中继,见矩阵 4/9),
	// 兜底路径只有 self/ai 类可达。
	ctx := WithClass(context.Background(), meshproto.ClassSelf)
	conn, err := client.DialContext(ctx, &M.Metadata{HostName: "x.com:443"})
	if err != nil {
		t.Fatalf("LAN refused must fall back to relay: %v", err)
	}
	defer conn.Close()
	if pikoCalls != 1 {
		t.Fatalf("piko calls = %d, want 1", pikoCalls)
	}
	if sshd.LastPayload() == nil {
		t.Fatal("sshd must have received the mesh-exit channel via relay")
	}
}

// TestExitClientNoTransportAdmits 矩阵 4:三种无载体准入形态都返回
// errNoTransportAdmits 且零拨号(准入在拨号前判定,SPEC §5.1)。
func TestExitClientNoTransportAdmits(t *testing.T) {
	keyPEM, _ := testIdentity(t)
	hostFP := gossh.FingerprintSHA256(genPubKey(t)) // 本矩阵不握手,任意真钉扎即可
	pikoCalls := 0
	pikoDial := func(context.Context, string) (net.Conn, error) {
		pikoCalls++
		return nil, errors.New("piko must not be dialed")
	}
	md := &M.Metadata{HostName: "x.com:443"}
	emptyLAN := PeerSpec{DeviceID: "dev-b", HostKey: hostFP, SSHUser: "mesh"}

	t.Run("empty lan relay off", func(t *testing.T) {
		client, err := NewExitClient(emptyLAN, keyPEM, "", pikoDial)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		if _, err := client.DialContext(context.Background(), md); !errors.Is(err, errNoTransportAdmits) {
			t.Fatalf("want errNoTransportAdmits, got %v", err)
		}
	})
	t.Run("empty lan custom cannot ride relay", func(t *testing.T) {
		client, err := NewExitClient(PeerSpec{DeviceID: "dev-b", HostKey: hostFP, SSHUser: "mesh", Relay: true}, keyPEM, "", pikoDial)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		ctx := WithClass(context.Background(), meshproto.ClassCustom)
		if _, err := client.DialContext(ctx, md); !errors.Is(err, errNoTransportAdmits) {
			t.Fatalf("custom must not ride relay, want errNoTransportAdmits, got %v", err)
		}
	})
	t.Run("empty lan ai but relay unwired", func(t *testing.T) {
		client, err := NewExitClient(PeerSpec{DeviceID: "dev-b", HostKey: hostFP, SSHUser: "mesh", Relay: true}, keyPEM, "", nil)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		ctx := WithClass(context.Background(), meshproto.ClassAI)
		if _, err := client.DialContext(ctx, md); !errors.Is(err, errNoTransportAdmits) {
			t.Fatalf("unwired relay must degrade to no transport, got %v", err)
		}
	})
	if pikoCalls != 0 {
		t.Fatalf("piko dialed %d times across no-admit cases; must be 0", pikoCalls)
	}
}

// TestExitClientUnbuildableTarget 矩阵 5:md 无法构造目标 → ErrUnbuildableTarget
// 且零拨号(不发空 payload,SPEC §5.1.1)。
func TestExitClientUnbuildableTarget(t *testing.T) {
	keyPEM, _ := testIdentity(t)
	pikoCalls := 0
	pikoDial := func(context.Context, string) (net.Conn, error) {
		pikoCalls++
		return nil, errors.New("piko must not be dialed")
	}
	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{"127.0.0.1:1"},
		HostKey:  gossh.FingerprintSHA256(genPubKey(t)),
		Relay:    true,
	}, keyPEM, "", pikoDial)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	conn, err := client.DialContext(context.Background(), &M.Metadata{}) // 空 HostName/DstIP
	if err == nil || !errors.Is(err, ErrUnbuildableTarget()) {
		t.Fatalf("want ErrUnbuildableTarget, got %v", err)
	}
	if conn != nil {
		_ = conn.Close()
	}
	if pikoCalls != 0 {
		t.Fatalf("piko dialed %d times; unbuildable must reject before dialing", pikoCalls)
	}
}

// TestExitClientPikoNotInSliceWhenUnwired 矩阵 6:Relay=true 但 pikoDial=nil
// ⇒ 构造期不放 PikoTransport,LAN 可达时正常拨号且不 panic(SPEC §7.3)。
func TestExitClientPikoNotInSliceWhenUnwired(t *testing.T) {
	keyPEM, identity := testIdentity(t)
	sshd := newMeshTestSSHD(t, identity.PublicKey())

	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{sshd.Addr()},
		HostKey:  sshd.HostKeyPinned(),
		Relay:    true, // 允许中继,但未接线 ⇒ 仅剩 LAN 载体
	}, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	conn, err := client.DialContext(context.Background(), &M.Metadata{HostName: "x.com:443"})
	if err != nil {
		t.Fatalf("unwired relay must not panic and LAN must serve: %v", err)
	}
	defer conn.Close()
	if sshd.LastPayload() == nil {
		t.Fatal("payload must arrive over LAN")
	}
}

// TestExitClientHandshakeDeadline 矩阵 7:对端 accept 后不发 SSH banner,
// 必须在覆写的握手窗口(300ms)内返回错误——gossh.NewClientConn 不吃
// ClientConfig.Timeout,deadline 必须手动设置(SPEC §5.1)。
// 覆写包级 var,defer 还原;不并行(var 为包级状态)。
func TestExitClientHandshakeDeadline(t *testing.T) {
	silent := silentListener(t)
	keyPEM, _ := testIdentity(t)

	const shortTimeout = 300 * time.Millisecond
	orig := meshExitHandshakeTimeout
	meshExitHandshakeTimeout = shortTimeout // 必须先于 NewExitClient:构造期读取
	defer func() { meshExitHandshakeTimeout = orig }()

	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{silent},
		HostKey:  gossh.FingerprintSHA256(genPubKey(t)),
	}, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	start := time.Now()
	_, err = client.DialContext(context.Background(), &M.Metadata{HostName: "x.com:443"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("silent peer must fail handshake")
	}
	if !strings.Contains(err.Error(), "ssh handshake") {
		t.Fatalf("want handshake error, got %v", err)
	}
	// 3 倍余量容忍 CI 抖动;若 deadline 未生效会等默认 10s,必红。
	if elapsed > 3*shortTimeout {
		t.Fatalf("handshake took %v, must abort within overridden %v", elapsed, shortTimeout)
	}
}

// TestDialMeshAddrRejectsEmptyHostKey 矩阵 8:兜底拨号对空 host key
// fail-closed(连真 sshd 期望握手被拒);一并钉住 nil 身份/nil payload 两守卫。
func TestDialMeshAddrRejectsEmptyHostKey(t *testing.T) {
	_, identity := testIdentity(t)
	sshd := newMeshTestSSHD(t, identity.PublicKey())
	payload := exitPayload(&M.Metadata{HostName: "x.com:443"})

	t.Run("empty host key fail-closed", func(t *testing.T) {
		conn, err := DialMeshAddr(context.Background(), sshd.Addr(), "", identity, payload)
		if err == nil {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatal("empty host key must fail closed")
		}
		if !strings.Contains(err.Error(), "fail-closed") {
			t.Fatalf("want fail-closed rejection, got %v", err)
		}
		if sshd.LastPayload() != nil {
			t.Fatal("no channel must open on fail-closed")
		}
	})
	t.Run("nil identity rejected", func(t *testing.T) {
		if _, err := DialMeshAddr(context.Background(), sshd.Addr(), sshd.HostKeyPinned(), nil, payload); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("nil identity must be rejected, got %v", err)
		}
	})
	t.Run("nil payload rejected", func(t *testing.T) {
		if _, err := DialMeshAddr(context.Background(), sshd.Addr(), sshd.HostKeyPinned(), identity, nil); !errors.Is(err, ErrUnbuildableTarget()) {
			t.Fatalf("nil payload must be unbuildable, got %v", err)
		}
	})
}

// TestExitClientCustomClassDialsLanOnly 矩阵 9:custom 类别 + relay 已接线
// + LAN 可达 ⇒ 只走 LAN(fake piko 计数 0;custom 禁公共中继,SPEC §4.3/§7.1)。
func TestExitClientCustomClassDialsLanOnly(t *testing.T) {
	keyPEM, identity := testIdentity(t)
	sshd := newMeshTestSSHD(t, identity.PublicKey())

	pikoCalls := 0
	pikoDial := func(context.Context, string) (net.Conn, error) {
		pikoCalls++
		return nil, errors.New("custom must not ride relay")
	}
	client, err := NewExitClient(PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{sshd.Addr()},
		HostKey:  sshd.HostKeyPinned(),
		Relay:    true,
	}, keyPEM, "", pikoDial)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	ctx := WithClass(context.Background(), meshproto.ClassCustom)
	conn, err := client.DialContext(ctx, &M.Metadata{HostName: "x.com:443"})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	if pikoCalls != 0 {
		t.Fatalf("piko dialed %d times; custom must be LAN-only", pikoCalls)
	}
	if sshd.LastPayload() == nil {
		t.Fatal("payload must arrive over LAN")
	}
}
