package test

// mesh SSH 链两节点本地回环集成测试(SPEC §15.2,计划 Task H1):
// F1 删除了旧帧协议集成测试(1380 行),本文件按新模型(SSH mesh-exit 通道)
// 重建集成层。九个场景逐个 t.Run/独立函数,场景↔函数对照:
//
//	场景 1 单跳端到端        → TestMeshIntegrationSingleHop
//	场景 2 形态隔离矩阵      → TestMeshIntegrationFormMatrix(真 inbound/ssh 入口)
//	场景 3 ForwardGate 门禁  → TestMeshIntegrationForwardGate(真 inbound/ssh 入口)
//	场景 4 host key 冒充     → TestMeshIntegrationHostKeyImpostor(真 inbound/ssh 入口)
//	场景 5 握手挂死          → TestMeshIntegrationHandshakeHang(silent listener)
//	场景 6 并发不串流        → TestMeshIntegrationConcurrentPeers(fake piko dial)
//	场景 7 DNS 代转发往返    → TestMeshIntegrationDNSRoundTrip(自建服务端 dns 分支)
//	场景 8 失败语义          → TestMeshIntegrationFailureSemantics
//	场景 9 ingress 未运行    → TestMeshIntegrationIngressNotRunning(HandleIngressConn 缝)
//
// 黑盒约束:test 包只消费导出 API——inbound/ssh(NewServer/Start/Stop/Addr/
// RunConfig/MeshKey/Form*)、outbound/proxy/mesh(ExitClient/PeerSpec/
// ChannelMeshExit/SignerFromPEM/ExitPayloadForMetadata/DialMeshAddr)、gossh。
// B 侧收到注入后的验证不可走 processor/tcp 管线(GetHandler() 单例会把 tcp
// 注入真拨进外网),故「tcp 目标注入后」的验证改走 mesh-exit 服务端回显模式
// (自建纯 gossh 服务端,meshExitSSHD);真 inbound/ssh 入口只承担 Reject
// 路径与 host key 握手,A 侧 OpenChannel 收到的拒绝文案即断言面。
//
// 字面量互锁(C4 评审 M3):meshExitSSHD 只接受 "mesh-exit" 字面量 channel——
// 出口侧 mesh.ChannelMeshExit 漂移时场景 1 的常量断言先红,漂移后拨进来的
// channel 名也会被夹具拒;入口侧 channelMeshExit 漂移时场景 2 的矩阵用例必红
// (mesh 凭证开 mesh-exit 反被拒)。ExtraData 上限对齐走黑盒边界:513 字节
// 必拒 "payload too large"、512 字节过闸到 JSON 解析必拒 "bad payload"
// (闸门前置序一并钉住),两侧常量任一漂移即红。
//
// 超时纪律:出口侧 meshExitHandshakeTimeout 是未导出包级 var,不可从本包
// 覆写——所有挂死风险场景一律用局部 watchdog(bound 到点即 Fatal,不挂死
// 测试进程);场景 5 按出口默认 10s 窗口设 15s bound,deadline 丢失即红,
// go test -timeout 兜底。全套时长目标 <30s(场景 5 约 10s 为最大项)。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sshin "aliang.one/nursorgate/inbound/ssh"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	meshproto "aliang.one/nursorgate/outbound/meshproto"
	mesh "aliang.one/nursorgate/outbound/proxy/mesh"

	gossh "golang.org/x/crypto/ssh"
)

// ===== 夹具 =====

// meshKeyPair 生成一把真 ed25519 测试身份:PEM 交给 A 侧 ExitClient /
// 裸 gossh 客户端,signer/pub 供 B 侧准入比对——两侧同源,避免假钥被实现
// 改动绕过(与 outbound/proxy/mesh 包内 testIdentity 同款形态,test 包重写)。
func meshKeyPair(t *testing.T) (keyPEM string, signer gossh.Signer, pub gossh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	signer, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "mesh-integration-test-identity")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), signer, signer.PublicKey()
}

// meshExitSSHD 自建的最小真 sshd(B 侧替身,不经 inbound/ssh):公钥准入
// (命中 admittedPub 才放),channel 过滤只认 "mesh-exit" 字面量(互锁第
// 二道),ExtraData 逐条记录,onChannel 决定通道行为(回显/dns 帧)。形态
// 与 outbound/proxy/mesh 包内 meshTestSSHD 同源,test 包重写一份。
type meshExitSSHD struct {
	ln     net.Listener
	hostFP string // host key SHA256 指纹(PeerSpec.HostKey 直用的钉扎格式)

	mu       sync.Mutex
	payloads [][]byte // 每条 mesh-exit channel 的 ExtraData(到达序)
}

func newMeshExitSSHD(t *testing.T, admittedPub gossh.PublicKey, onChannel func(ch gossh.Channel, extra []byte)) *meshExitSSHD {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
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
	s := &meshExitSSHD{
		ln:     ln,
		hostFP: gossh.FingerprintSHA256(hostSigner.PublicKey()),
	}
	go s.serve(hostSigner, admittedPub, onChannel)
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *meshExitSSHD) Addr() string { return s.ln.Addr().String() }

// HostKeyPinned 返回本 sshd host key 的 SHA256 指纹(SPEC §10.4 指纹格式)。
func (s *meshExitSSHD) HostKeyPinned() string { return s.hostFP }

// LastPayload 返回最近一条 mesh-exit ExtraData;尚无 channel 时返回 nil。
func (s *meshExitSSHD) LastPayload() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.payloads); n > 0 {
		return s.payloads[n-1]
	}
	return nil
}

// PayloadCount 返回已收到的 mesh-exit channel 数(并发场景断言不串流用)。
func (s *meshExitSSHD) PayloadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.payloads)
}

func (s *meshExitSSHD) record(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, payload)
}

func (s *meshExitSSHD) serve(hostSigner gossh.Signer, admittedPub gossh.PublicKey, onChannel func(ch gossh.Channel, extra []byte)) {
	for {
		tcp, err := s.ln.Accept()
		if err != nil {
			return // listener 已被 t.Cleanup 关闭
		}
		go s.handle(tcp, hostSigner, admittedPub, onChannel)
	}
}

func (s *meshExitSSHD) handle(tcp net.Conn, hostSigner gossh.Signer, admittedPub gossh.PublicKey, onChannel func(ch gossh.Channel, extra []byte)) {
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
		// 字面量互锁:只认 "mesh-exit" 字面量,不引用 mesh.ChannelMeshExit
		// ——出口侧常量漂移时,拨进来的 channel 名对不上,本夹具必拒。
		if newCh.ChannelType() != "mesh-exit" {
			_ = newCh.Reject(gossh.UnknownChannelType, "mesh test sshd only accepts mesh-exit literal")
			continue
		}
		// record 在 Accept 之前:payload 必然先于客户端 DialContext 返回被记录。
		s.record(newCh.ExtraData())
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go gossh.DiscardRequests(chReqs)
		if onChannel != nil {
			onChannel(ch, newCh.ExtraData())
		}
	}
}

// meshEchoChannelHandler tcp 目标的替身行为:goroutine 回显后关通道
// (CloseWrite 半关让 io.ReadFull 读侧可尽)。
func meshEchoChannelHandler() func(ch gossh.Channel, extra []byte) {
	return func(ch gossh.Channel, _ []byte) {
		go func() {
			_, _ = io.Copy(ch, ch)
			_ = ch.CloseWrite()
			_ = ch.Close()
		}()
	}
}

// meshDNSChannelHandler dns 目标的替身行为:按 B 侧 dns 线格式读一条
// [2B 大端长度][查询] 帧,写回 [2B 大端长度][固定响应] 后关——fake
// resolver 不可从 test 包注入(inbound/ssh.meshDNSResolver 未导出),
// dns 分支的自建实现即场景 7 的往返载体。
func meshDNSChannelHandler(response []byte) func(ch gossh.Channel, extra []byte) {
	return func(ch gossh.Channel, _ []byte) {
		go func() {
			defer ch.Close()
			query, err := meshReadDNSFrame(ch)
			if err != nil {
				return
			}
			_ = query // 夹具不解析报文,帧纪律(先读后写)由读写序保证
			_ = meshWriteDNSFrame(ch, response)
		}()
	}
}

// meshSilentListener accept 后一字节不发的 TCP 监听(ssh banner 缺席:
// A 侧只能靠内置握手 deadline 脱困,场景 5)。
func meshSilentListener(t *testing.T) string {
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

// meshIngressNode 真 inbound/ssh 入口节点(B 侧真身):专用口监听地址 +
// host key 指纹。host key 先生成后落盘,指纹不经服务端生成路径推导,
// A 侧钉扎无竞速。
type meshIngressNode struct {
	addr   string // 专用口监听地址(A 侧 PeerSpec.LANAddrs 直用)
	hostFP string // host key SHA256 指纹
}

// meshGrabFreePort 抓一个 127.0.0.1 空闲端口(先 listen 后 close;与
// inbound/ssh 的专用口 Start 之间有极小抢占窗口,起服失败由重试覆盖)。
func meshGrabFreePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grab free port: %v", err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port
}

// startIngressNode 起一台真 inbound/ssh 入口(专用口 Port>0,默认绑定
// ServiceBindHost=127.0.0.1,无环境变量依赖,无需 t.Setenv);Stop 挂
// t.Cleanup。RunConfig.Port 由本函数填(调用方留零值)。
func startIngressNode(t *testing.T, rc sshin.RunConfig) *meshIngressNode {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ingress host key: %v", err)
	}
	hostSigner, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("new ingress host signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(hostPriv, "mesh integration ingress host key")
	if err != nil {
		t.Fatalf("marshal ingress host key: %v", err)
	}
	hostKeyPath := filepath.Join(t.TempDir(), "ingress_host_key")
	if err := os.WriteFile(hostKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write ingress host key: %v", err)
	}

	var server *sshin.Server
	for attempt := 0; attempt < 3; attempt++ {
		rc.Port = meshGrabFreePort(t)
		server = sshin.NewServer(rc, hostKeyPath)
		if serr := server.Start(); serr == nil {
			break
		}
		_ = server.Stop() // 端口被抢占:重试
		server = nil
	}
	if server == nil {
		t.Fatal("ingress dedicated listener failed to start after retries")
	}
	t.Cleanup(func() { _ = server.Stop() })
	return &meshIngressNode{
		addr:   server.Addr(),
		hostFP: gossh.FingerprintSHA256(hostSigner.PublicKey()),
	}
}

// ===== 有界拨号/读写助手(挂死保护) =====

// meshDialBounded 在 bound 内等 ExitClient.DialContext 返回,到点即 Fatal
// (挂死保护,不做成功/失败断言——断言归调用方)。出口侧握手窗口
// (meshExitHandshakeTimeout,默认 10s)不可从本包覆写,watchdog bound
// 须大于该窗口:窗口语义断言(场景 5)依赖它,bound 即是断言本体。
func meshDialBounded(t *testing.T, client *mesh.ExitClient, ctx context.Context, md *M.Metadata, bound time.Duration) (net.Conn, error) {
	t.Helper()
	type dialResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, e := client.DialContext(ctx, md)
		ch <- dialResult{c, e}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-time.After(bound):
		t.Fatalf("DialContext did not return within %v (handshake deadline lost?)", bound)
		return nil, nil
	}
}

// meshRoundTripBounded 写 payload 并读回等长回显,bound 内未完成即 Fatal。
func meshRoundTripBounded(t *testing.T, conn net.Conn, payload []byte, bound time.Duration) []byte {
	t.Helper()
	type rtResult struct {
		got []byte
		err error
	}
	ch := make(chan rtResult, 1)
	go func() {
		if _, err := conn.Write(payload); err != nil {
			ch <- rtResult{nil, err}
			return
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			ch <- rtResult{nil, err}
			return
		}
		ch <- rtResult{buf, nil}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("round trip: %v", r.err)
		}
		return r.got
	case <-time.After(bound):
		t.Fatalf("round trip did not finish within %v", bound)
		return nil
	}
}

// meshWriteDNSFrame 写一条 [2B 大端长度][载荷] 帧(B 侧 dns 线格式对偶)。
func meshWriteDNSFrame(w io.Writer, payload []byte) error {
	var head [2]byte
	binary.BigEndian.PutUint16(head[:], uint16(len(payload)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// meshReadDNSFrame 读一条 [2B 大端长度][载荷] 帧(零长度按协议错误)。
func meshReadDNSFrame(r io.Reader) ([]byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(head[:])
	if n == 0 {
		return nil, errors.New("mesh dns: empty frame")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// meshReadDNSFrameBounded 有界读一条 dns 帧(对端不回帧即 Fatal,不挂死)。
func meshReadDNSFrameBounded(t *testing.T, conn net.Conn, bound time.Duration) []byte {
	t.Helper()
	type frameResult struct {
		payload []byte
		err     error
	}
	ch := make(chan frameResult, 1)
	go func() {
		p, err := meshReadDNSFrame(conn)
		ch <- frameResult{p, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read dns frame: %v", r.err)
		}
		return r.payload
	case <-time.After(bound):
		t.Fatalf("dns frame did not arrive within %v", bound)
		return nil
	}
}

// meshDialIngressRaw 裸 gossh 客户端拨真 inbound/ssh 入口(形态矩阵与
// 门禁用例的 A 侧替身:矩阵行为断言只需要 OpenChannel 的 Reject 文案,
// 不需要 ExitClient 全链)。host key 按指纹钉扎(与 mesh 钉扎同格式)。
func meshDialIngressRaw(t *testing.T, addr, hostFP string, signer gossh.Signer) *gossh.Client {
	t.Helper()
	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("dial ingress %s: %v", addr, err)
	}
	hostCB := func(_ string, _ net.Addr, key gossh.PublicKey) error {
		if gossh.FingerprintSHA256(key) != hostFP {
			return fmt.Errorf("ingress host key mismatch (fingerprint %s)", gossh.FingerprintSHA256(key))
		}
		return nil
	}
	// 手动 deadline 罩握手(gossh.NewClientConn 不吃 ClientConfig.Timeout,
	// 与出口侧同款先手),返回即清。
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	cc, chans, reqs, err := gossh.NewClientConn(raw, addr, &gossh.ClientConfig{
		User:            "mesh-integration",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: hostCB,
	})
	_ = raw.SetDeadline(time.Time{})
	if err != nil {
		_ = raw.Close()
		t.Fatalf("ingress handshake to %s: %v", addr, err)
	}
	go gossh.DiscardRequests(reqs)
	return gossh.NewClient(cc, chans, reqs)
}

// meshExpectOpenRejected 断言 OpenChannel 被 B 侧拒绝且拒绝文案含全部
// want 子串(gossh.OpenChannelError.Error() 形如
// "ssh: rejected: <reason> (<message>)",B 侧 Reject reason 原样可达)。
func meshExpectOpenRejected(t *testing.T, client *gossh.Client, channelType string, payload []byte, want ...string) {
	t.Helper()
	ch, _, err := client.OpenChannel(channelType, payload)
	if err == nil {
		_ = ch.Close()
		t.Fatalf("open %q must be rejected by the ingress matrix", channelType)
	}
	var oce *gossh.OpenChannelError
	if !errors.As(err, &oce) {
		t.Fatalf("open %q: want *gossh.OpenChannelError, got %T: %v", channelType, err, err)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("open %q rejection = %q, want containing %q", channelType, err.Error(), w)
		}
	}
}

// meshTCPMD 造一个标准 tcp 目标 metadata(出口 payload 即
// {"t":"tcp","h":"x.com","p":443})。
func meshTCPMD() *M.Metadata {
	return &M.Metadata{HostName: "x.com", DstPort: 443}
}

// ===== 场景 1:单跳端到端 =====

// TestMeshIntegrationSingleHop A ExitClient(LAN 回环)→ 自建 mesh-exit
// 服务端:OpenChannel 成功、ExtraData 内容逐字节断言、双向字节回显。
// 同时钉两侧字面量互锁的第一道:ChannelMeshExit 常量 == "mesh-exit"。
func TestMeshIntegrationSingleHop(t *testing.T) {
	// 常量断言(互锁第一道):夹具只认 "mesh-exit" 字面量是第二道——常量
	// 漂移时这里先红;即使断言被改掉,漂移后的 channel 名也进不了夹具。
	if mesh.ChannelMeshExit != "mesh-exit" {
		t.Fatalf("mesh.ChannelMeshExit = %q, wire literal must stay %q", mesh.ChannelMeshExit, "mesh-exit")
	}

	keyPEM, _, aPub := meshKeyPair(t)
	b := newMeshExitSSHD(t, aPub, meshEchoChannelHandler())

	client, err := mesh.NewExitClient(mesh.PeerSpec{
		DeviceID: "node-b",
		LANAddrs: []string{b.Addr()},
		HostKey:  b.HostKeyPinned(),
	}, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}

	md := meshTCPMD()
	conn, dialErr := meshDialBounded(t, client, context.Background(), md, 8*time.Second)
	if dialErr != nil {
		t.Fatalf("DialContext: %v", dialErr)
	}
	defer conn.Close()

	want := mesh.ExitPayloadForMetadata(md)
	if !bytes.Equal(b.LastPayload(), want) {
		t.Fatalf("ExtraData = %s, want %s", b.LastPayload(), want)
	}
	if string(want) != `{"t":"tcp","h":"x.com","p":443}` {
		t.Fatalf("payload wire format drifted: %s", want)
	}
	got := meshRoundTripBounded(t, conn, []byte("ping"), 5*time.Second)
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
}

// ===== 场景 2:形态隔离(真 inbound/ssh 入口) =====

// TestMeshIntegrationFormMatrix 真 inbound/ssh 的 form×channel 收敛矩阵:
// mesh 凭证开 direct-tcpip 拒、bare 凭证开 mesh-exit 拒、未知 channel 拒;
// 附 ExtraData 上限互锁(黑盒边界:513 拒 payload too large、512 过闸到
// bad payload)。门禁恒关兜底——矩阵用例全走 Reject,tcp 目标不会进管线。
func TestMeshIntegrationFormMatrix(t *testing.T) {
	_, meshSigner, meshPub := meshKeyPair(t)
	_, bareSigner, barePub := meshKeyPair(t)

	b := startIngressNode(t, sshin.RunConfig{
		AuthorizedKeys: []gossh.PublicKey{barePub},
		MeshKeys:       []sshin.MeshKey{{Key: meshPub, DeviceID: "dev-a"}},
		ForwardGate:    func() bool { return false }, // 矩阵全 Reject,门禁恒关兜底
	})
	validTCP := []byte(`{"t":"tcp","h":"x.com","p":443}`)

	t.Run("mesh creds cannot open direct-tcpip", func(t *testing.T) {
		c := meshDialIngressRaw(t, b.addr, b.hostFP, meshSigner)
		defer c.Close()
		meshExpectOpenRejected(t, c, "direct-tcpip", nil, "not allowed")
	})
	t.Run("bare creds cannot open mesh-exit", func(t *testing.T) {
		c := meshDialIngressRaw(t, b.addr, b.hostFP, bareSigner)
		defer c.Close()
		meshExpectOpenRejected(t, c, mesh.ChannelMeshExit, validTCP, "not allowed")
	})
	t.Run("unknown channel rejected", func(t *testing.T) {
		c := meshDialIngressRaw(t, b.addr, b.hostFP, bareSigner)
		defer c.Close()
		meshExpectOpenRejected(t, c, "session", nil, "not allowed")
	})

	// ExtraData 上限互锁(B 侧常量黑盒边界):闸门检查先于 JSON 解析——
	// 513 字节必报 payload too large;512 字节过闸、JSON 解析必报
	// bad payload。任一侧 meshExitMaxExtra 常量漂移(或检查顺序倒置)即红。
	t.Run("oversized extra rejected before parse", func(t *testing.T) {
		c := meshDialIngressRaw(t, b.addr, b.hostFP, meshSigner)
		defer c.Close()
		meshExpectOpenRejected(t, c, mesh.ChannelMeshExit, bytes.Repeat([]byte("x"), 513), "payload too large")
	})
	t.Run("512-byte extra passes cap and hits payload parse", func(t *testing.T) {
		c := meshDialIngressRaw(t, b.addr, b.hostFP, meshSigner)
		defer c.Close()
		meshExpectOpenRejected(t, c, mesh.ChannelMeshExit, bytes.Repeat([]byte("x"), 512), "bad payload")
	})
}

// ===== 场景 3:ForwardGate 门禁 =====

// TestMeshIntegrationForwardGate gate=false 与 gate=nil(fail-closed)都必
// 拒 mesh-exit(SPEC §10.1 决策 #14:nil=装配层未接线=拒绝,非旧「全放行」)。
// 合法 tcp payload 走到门禁判定(闸门在 cap/parse 之后),拒绝文案含
// "forward disabled"。
func TestMeshIntegrationForwardGate(t *testing.T) {
	keyPEM, _, aPub := meshKeyPair(t)
	cases := []struct {
		name string
		gate func() bool
	}{
		{"gate false rejects", func() bool { return false }},
		{"gate nil fail-closed", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			b := startIngressNode(t, sshin.RunConfig{
				MeshKeys:    []sshin.MeshKey{{Key: aPub, DeviceID: "dev-a"}},
				ForwardGate: tc.gate,
			})
			client, err := mesh.NewExitClient(mesh.PeerSpec{
				DeviceID: "node-b",
				LANAddrs: []string{b.addr},
				HostKey:  b.hostFP,
			}, keyPEM, "", nil)
			if err != nil {
				t.Fatalf("NewExitClient: %v", err)
			}
			conn, dialErr := meshDialBounded(t, client, context.Background(), meshTCPMD(), 8*time.Second)
			if dialErr == nil {
				_ = conn.Close()
				t.Fatalf("gate %v must reject mesh-exit", tc.gate != nil)
			}
			if !strings.Contains(dialErr.Error(), "forward disabled") {
				t.Fatalf("want forward-disabled rejection, got %v", dialErr)
			}
		})
	}
}

// ===== 场景 4:host key 冒充 =====

// TestMeshIntegrationHostKeyImpostor A 钉钥乙、B 起钥甲 → 握手必败
// ("mismatch");钉扎正确 → 成功。成功腿用 dns 目标 payload({"t":"dns"})
// ——B 侧 dns 分支只读帧不进 tcp 管线,外呼面为零;tcp 目标会经
// GetHandler() 注入真管线,本测试包不引入。
func TestMeshIntegrationHostKeyImpostor(t *testing.T) {
	keyPEM, _, aPub := meshKeyPair(t)
	// 钥乙:只生成指纹、不上盘——B 实际握出的永远是钥甲。
	_, impostorPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate impostor key: %v", err)
	}
	impostorSigner, err := gossh.NewSignerFromKey(impostorPriv)
	if err != nil {
		t.Fatalf("new impostor signer: %v", err)
	}
	impostorFP := gossh.FingerprintSHA256(impostorSigner.PublicKey())

	b := startIngressNode(t, sshin.RunConfig{
		MeshKeys:    []sshin.MeshKey{{Key: aPub, DeviceID: "dev-a"}},
		ForwardGate: func() bool { return true }, // 成功腿需要闸门放行
	})

	t.Run("wrong pinned key fails handshake", func(t *testing.T) {
		client, err := mesh.NewExitClient(mesh.PeerSpec{
			DeviceID: "node-b",
			LANAddrs: []string{b.addr},
			HostKey:  impostorFP, // 钉错钥乙
		}, keyPEM, "", nil)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		conn, dialErr := meshDialBounded(t, client, context.Background(), meshTCPMD(), 8*time.Second)
		if dialErr == nil {
			_ = conn.Close()
			t.Fatal("impostor host key must fail the handshake")
		}
		if !strings.Contains(dialErr.Error(), "mismatch") {
			t.Fatalf("want host-key mismatch error, got %v", dialErr)
		}
	})
	t.Run("correct pinned key succeeds", func(t *testing.T) {
		client, err := mesh.NewExitClient(mesh.PeerSpec{
			DeviceID: "node-b",
			LANAddrs: []string{b.addr},
			HostKey:  b.hostFP, // 钉对钥甲
		}, keyPEM, "", nil)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		// dns 目标:B 侧 Accept 即成功语义,不进管线、不发真查询。
		md := &M.Metadata{HostName: meshproto.DNSTarget, DstPort: 53}
		conn, dialErr := meshDialBounded(t, client, context.Background(), md, 8*time.Second)
		if dialErr != nil {
			t.Fatalf("correct pinning must succeed: %v", dialErr)
		}
		_ = conn.Close() // B 侧 dns 分支等帧,关连即退(5s 窗口兜底)
	})
}

// ===== 场景 5:握手挂死 =====

// TestMeshIntegrationHandshakeHang 对端 accept 后不回 SSH banner,A 侧必须
// 在内置握手窗口(meshExitHandshakeTimeout=10s,未导出不可覆写)内失败
// 返回——watchdog bound 15s 即窗口语义断言:deadline 丢失则 DialContext
// 永久挂死,本用例 15s 到点红(go test -timeout 兜底)。
func TestMeshIntegrationHandshakeHang(t *testing.T) {
	silent := meshSilentListener(t)
	keyPEM, _, aPub := meshKeyPair(t)
	hostFP := gossh.FingerprintSHA256(aPub) // 挂死对端不回 banner,钉扎内容不参与

	client, err := mesh.NewExitClient(mesh.PeerSpec{
		DeviceID: "node-b",
		LANAddrs: []string{silent},
		HostKey:  hostFP,
	}, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}
	start := time.Now()
	conn, dialErr := meshDialBounded(t, client, context.Background(), meshTCPMD(), 15*time.Second)
	elapsed := time.Since(start)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("silent peer must fail the handshake")
	}
	if !strings.Contains(dialErr.Error(), "ssh handshake") {
		t.Fatalf("want handshake error, got %v", dialErr)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("handshake took %v, must abort within the built-in deadline", elapsed)
	}
}

// ===== 场景 6:并发不串行不串流 =====

// TestMeshIntegrationConcurrentPeers 两个 ExitClient(不同 peer)经 fake
// piko dial 同时连入各自的 mesh-exit 服务端:各自回显不串流,payload 各归
// 各的服务端,piko 拨号各计一次(SPEC §8.1)。
func TestMeshIntegrationConcurrentPeers(t *testing.T) {
	pem1, _, pub1 := meshKeyPair(t)
	pem2, _, pub2 := meshKeyPair(t)
	b1 := newMeshExitSSHD(t, pub1, meshEchoChannelHandler())
	b2 := newMeshExitSSHD(t, pub2, meshEchoChannelHandler())

	// 每 client 一把独立计数(sync/atomic):两个拨号并发在跑,共享 map
	// 会构成数据竞态;计数原子化后断言面不变(各自恰好 1 次、端点名各归各)。
	var calls1, calls2 atomic.Int32
	piko1 := func(ctx context.Context, endpointID string) (net.Conn, error) {
		if endpointID != "node-b1.mesh" {
			return nil, fmt.Errorf("unexpected endpoint %q", endpointID)
		}
		calls1.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", b1.Addr())
	}
	piko2 := func(ctx context.Context, endpointID string) (net.Conn, error) {
		if endpointID != "node-b2.mesh" {
			return nil, fmt.Errorf("unexpected endpoint %q", endpointID)
		}
		calls2.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", b2.Addr())
	}

	client1, err := mesh.NewExitClient(mesh.PeerSpec{
		DeviceID: "node-b1",
		LANAddrs: []string{"127.0.0.1:1"}, // LAN 必败,强制落 piko
		HostKey:  b1.HostKeyPinned(),
		Relay:    true,
	}, pem1, "", piko1)
	if err != nil {
		t.Fatalf("NewExitClient 1: %v", err)
	}
	client2, err := mesh.NewExitClient(mesh.PeerSpec{
		DeviceID: "node-b2",
		LANAddrs: []string{"127.0.0.1:1"},
		HostKey:  b2.HostKeyPinned(),
		Relay:    true,
	}, pem2, "", piko2)
	if err != nil {
		t.Fatalf("NewExitClient 2: %v", err)
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}
	res1 := make(chan dialResult, 1)
	res2 := make(chan dialResult, 1)
	go func() {
		c, e := client1.DialContext(mesh.WithClass(context.Background(), meshproto.ClassSelf),
			&M.Metadata{HostName: "one.test", DstPort: 443})
		res1 <- dialResult{c, e}
	}()
	go func() {
		c, e := client2.DialContext(mesh.WithClass(context.Background(), meshproto.ClassSelf),
			&M.Metadata{HostName: "two.test", DstPort: 443})
		res2 <- dialResult{c, e}
	}()

	// 显式 self 类:custom 禁公共中继,默认推断会让 piko 在 Admit 即被拦。
	var conn1, conn2 net.Conn
	got1, got2 := false, false
	watchdog := time.After(10 * time.Second)
	for !(got1 && got2) {
		select {
		case r := <-res1:
			got1 = true
			if r.err != nil {
				t.Fatalf("peer1 DialContext: %v", r.err)
			}
			conn1 = r.conn
		case r := <-res2:
			got2 = true
			if r.err != nil {
				t.Fatalf("peer2 DialContext: %v", r.err)
			}
			conn2 = r.conn
		case <-watchdog:
			t.Fatal("concurrent dials did not finish within 10s")
		}
	}
	defer conn1.Close()
	defer conn2.Close()

	got := meshRoundTripBounded(t, conn1, []byte("alpha"), 5*time.Second)
	if string(got) != "alpha" {
		t.Fatalf("peer1 echo = %q, want %q (streams crossed)", got, "alpha")
	}
	got = meshRoundTripBounded(t, conn2, []byte("beta"), 5*time.Second)
	if string(got) != "beta" {
		t.Fatalf("peer2 echo = %q, want %q (streams crossed)", got, "beta")
	}
	if want := []byte(`{"t":"tcp","h":"one.test","p":443}`); !bytes.Equal(b1.LastPayload(), want) || b1.PayloadCount() != 1 {
		t.Fatalf("peer1 server payload = %s (n=%d), want exactly %s", b1.LastPayload(), b1.PayloadCount(), want)
	}
	if want := []byte(`{"t":"tcp","h":"two.test","p":443}`); !bytes.Equal(b2.LastPayload(), want) || b2.PayloadCount() != 1 {
		t.Fatalf("peer2 server payload = %s (n=%d), want exactly %s", b2.LastPayload(), b2.PayloadCount(), want)
	}
	for id, calls := range map[string]*atomic.Int32{"node-b1.mesh": &calls1, "node-b2.mesh": &calls2} {
		if got := calls.Load(); got != 1 {
			t.Fatalf("endpoint %s dialed %d times, want exactly 1", id, got)
		}
	}
}

// ===== 场景 7:DNS 代转发往返 =====

// TestMeshIntegrationDNSRoundTrip A 发 {"t":"dns"} payload → 自建服务端
// dns 分支读 [2B 长度][查询] 帧 → 写 [2B 长度][固定响应] → A 读到原样。
// fake resolver 不可从外部注入(inbound/ssh.meshDNSResolver 未导出),
// 线格式往返由本场景自建实现承载(B 侧真实分支的解析面在包内单测覆盖)。
func TestMeshIntegrationDNSRoundTrip(t *testing.T) {
	keyPEM, _, aPub := meshKeyPair(t)
	response := []byte("fake-dns-answer-bytes")
	b := newMeshExitSSHD(t, aPub, meshDNSChannelHandler(response))

	client, err := mesh.NewExitClient(mesh.PeerSpec{
		DeviceID: "node-b",
		LANAddrs: []string{b.Addr()},
		HostKey:  b.HostKeyPinned(),
	}, keyPEM, "", nil)
	if err != nil {
		t.Fatalf("NewExitClient: %v", err)
	}

	// HostName 恰为 "dns:" → exitPayload 输出 {"t":"dns"}(无 h/p)。
	md := &M.Metadata{HostName: meshproto.DNSTarget, DstPort: 53}
	conn, dialErr := meshDialBounded(t, client, context.Background(), md, 8*time.Second)
	if dialErr != nil {
		t.Fatalf("DialContext: %v", dialErr)
	}
	defer conn.Close()
	if want := []byte(`{"t":"dns"}`); !bytes.Equal(b.LastPayload(), want) {
		t.Fatalf("dns ExtraData = %s, want %s", b.LastPayload(), want)
	}

	// 手工 DNS 查询报文(id 0x1234,rd=1,qdcount=1,example.com A IN)——
	// 夹具不解析,但保持真实形态便于人工核对。
	query := []byte{
		0x12, 0x34, // id
		0x01, 0x00, // flags: recursion desired
		0x00, 0x01, // qdcount
		0x00, 0x00, // ancount
		0x00, 0x00, // nscount
		0x00, 0x00, // arcount
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		3, 'c', 'o', 'm',
		0,
		0, 1, // qtype A
		0, 1, // qclass IN
	}
	if err := meshWriteDNSFrame(conn, query); err != nil {
		t.Fatalf("write query frame: %v", err)
	}
	got := meshReadDNSFrameBounded(t, conn, 5*time.Second)
	if !bytes.Equal(got, response) {
		t.Fatalf("dns round trip = %q, want %q", got, response)
	}
}

// ===== 场景 8:失败语义 =====

// TestMeshIntegrationFailureSemantics LAN 全不可达 + piko 未接线 → 错误回源
// (含 "no SSH transport",不吞不静默);对端进程被杀语义以「连到已关闭
// listener」替代(refused)。
func TestMeshIntegrationFailureSemantics(t *testing.T) {
	keyPEM, _, aPub := meshKeyPair(t)
	hostFP := gossh.FingerprintSHA256(aPub) // 两腿都拨不进对端,钉扎不参与

	newClient := func(t *testing.T, lan []string) *mesh.ExitClient {
		t.Helper()
		client, err := mesh.NewExitClient(mesh.PeerSpec{
			DeviceID: "node-b",
			LANAddrs: lan,
			HostKey:  hostFP,
			// Relay=false + pikoDial=nil:piko transport 不进切片,
			// 错误必须是 LAN 失败回源而非 no-admit 短路。
		}, keyPEM, "", nil)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		return client
	}

	t.Run("unreachable lan addr", func(t *testing.T) {
		client := newClient(t, []string{"127.0.0.1:1"}) // 必然 connection refused
		conn, dialErr := meshDialBounded(t, client, context.Background(), meshTCPMD(), 8*time.Second)
		if dialErr == nil {
			_ = conn.Close()
			t.Fatal("unreachable peer must fail with an error")
		}
		if !strings.Contains(dialErr.Error(), "no SSH transport") {
			t.Fatalf("want no-transport error, got %v", dialErr)
		}
	})
	t.Run("closed listener (peer killed)", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close() // 对端进程被杀:端口不再有人 accept
		client := newClient(t, []string{addr})
		conn, dialErr := meshDialBounded(t, client, context.Background(), meshTCPMD(), 8*time.Second)
		if dialErr == nil {
			_ = conn.Close()
			t.Fatal("dead peer must fail with an error")
		}
		if !strings.Contains(dialErr.Error(), "no SSH transport") {
			t.Fatalf("want no-transport error, got %v", dialErr)
		}
	})
}

// ===== 场景 9:ingress 未运行 WARN =====

// TestMeshIntegrationIngressNotRunning 入口未配置/未启动时,统一口分派缝
// HandleIngressConn 必须返回 false 且立刻关连接(不回显任何协议字节,
// 与「入口未启动则无人监听」同观感,SPEC §8.2)。
func TestMeshIntegrationIngressNotRunning(t *testing.T) {
	// 防御性清场:期望停(幂等)。测试包内无人起 reconcile 实例,此行把
	// 前置条件钉死,不依赖文件内测试顺序。
	sshin.Reconcile(nil, "", nil)
	if sshin.IsRunning() {
		t.Fatal("precondition: reconciled ingress must not be running")
	}

	server, client := net.Pipe()
	defer client.Close()
	handled := make(chan bool, 1)
	go func() { handled <- sshin.HandleIngressConn(server) }()
	select {
	case ok := <-handled:
		if ok {
			t.Fatal("HandleIngressConn must return false when ingress is not running")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HandleIngressConn must return promptly when ingress is not running")
	}

	// 连接被关:client 端一次 Read 立即得到 EOF(net.Pipe 对端 Close)。
	buf := make([]byte, 1)
	readErr := make(chan error, 1)
	go func() { _, err := client.Read(buf); readErr <- err }()
	select {
	case err := <-readErr:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read after reject = %v, want io.EOF (conn must be closed)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("conn must be closed after HandleIngressConn(false)")
	}
}
