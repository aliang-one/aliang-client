// mesh_inject_test.go — mesh-exit 通道形态(形态②)入口面(SPEC §4/§4.1):
// serveChannels 的 form×channel 矩阵、ExtraData 上限/payload 校验、
// ForwardGate fail-closed、管线注入落点(Route=RouteMesh+MeshInjected)与
// 远端标签限速键(SPEC §8.2)。dns 代解析往返在 mesh_dns_test.go;ExitClient
// 端到端集成用例在 H1(本处单元面已覆盖到 injectMeshExitStream 调起)。
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"

	gossh "golang.org/x/crypto/ssh"
)

// meshTCPExtra 构造 mesh-exit tcp 目标的 ExtraData。
func meshTCPExtra(host string, port uint16) []byte {
	return []byte(fmt.Sprintf(`{"t":"tcp","h":%q,"p":%d}`, host, port))
}

// pipeChannel 把 net.Pipe 一端适配为 gossh.Channel:serveChannels 单元替身
// 与 dns 帧往返共用。CloseWrite/SendRequest 为空实现(替身无对端请求面),
// Stderr 走内存缓冲。
type pipeChannel struct {
	net.Conn
	stderr bytes.Buffer
}

func (c *pipeChannel) CloseWrite() error { return nil }
func (c *pipeChannel) SendRequest(string, bool, []byte) (bool, error) {
	return false, nil
}
func (c *pipeChannel) Stderr() io.ReadWriter { return &c.stderr }

// fakeNewChannel 是 gossh.NewChannel 测试替身:Reject 记录 reason/message
// 供矩阵断言;Accept 返回 acceptCh(缺省新建 net.Pipe 对,客户端侧存
// acceptEnd 供清理/读写)。
type fakeNewChannel struct {
	chType    string
	extra     []byte
	acceptCh  gossh.Channel
	acceptEnd net.Conn // Accept 缺省建管道时的客户端侧

	accepted bool
	rejected bool
	reason   gossh.RejectionReason
	message  string
}

func (c *fakeNewChannel) ChannelType() string { return c.chType }
func (c *fakeNewChannel) ExtraData() []byte   { return c.extra }

func (c *fakeNewChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	c.accepted = true
	if c.acceptCh == nil {
		client, server := net.Pipe()
		c.acceptEnd = client
		c.acceptCh = &pipeChannel{Conn: server}
	}
	reqs := make(chan *gossh.Request)
	close(reqs) // DiscardRequests 立即退出
	return c.acceptCh, reqs, nil
}

func (c *fakeNewChannel) Reject(reason gossh.RejectionReason, message string) error {
	c.rejected = true
	c.reason = reason
	c.message = message
	return nil
}

// meshTestPerms 构造 mesh form 的认证 Permissions(带 device_id 标签)。
func meshTestPerms() *gossh.Permissions {
	return &gossh.Permissions{Extensions: map[string]string{
		extIngressForm:  FormMesh,
		extMeshDeviceID: "dev-b",
	}}
}

// driveServeChannels 同步驱动 serveChannels 消费一条替身通道(不走真握手);
// remote 用回环 TCPAddr 供注入路径取源地址。Accept 建出的客户端侧管道在
// 清理时关闭(服务端侧由注入路径 defer Close)。
func driveServeChannels(t *testing.T, s *Server, perms *gossh.Permissions, nc *fakeNewChannel) {
	t.Helper()
	t.Cleanup(func() {
		if nc.acceptEnd != nil {
			_ = nc.acceptEnd.Close()
		}
	})
	chans := make(chan gossh.NewChannel, 1)
	chans <- nc
	close(chans)
	s.serveChannels(context.Background(), chans, perms, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40001})
}

// waitInjected 等待注入协程把 Metadata 送进管线桩(accept 后异步,有界等待)。
func waitInjected(t *testing.T, metaCh <-chan *M.Metadata) *M.Metadata {
	t.Helper()
	select {
	case md := <-metaCh:
		return md
	case <-time.After(3 * time.Second):
		t.Fatal("accepted channel must reach the pipeline handler")
		return nil
	}
}

// TestMeshExitMatrix form×channel 穷举(SPEC §4.2 通道安全收敛矩阵):
// 公钥身份决定 form,form 决定唯一放行 channel;拒绝发生在 Accept 前,
// 观测面(observeForm/observeDeviceID)在拒绝判定之前留痕。
func TestMeshExitMatrix(t *testing.T) {
	metaCh := make(chan *M.Metadata, 4)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		metaCh <- md
		return nil
	})

	bareForm := &gossh.Permissions{Extensions: map[string]string{extIngressForm: FormBare}}
	meshForm := meshTestPerms()
	unknownForm := &gossh.Permissions{} // Extensions 无 ingress_form → 未知
	bareExtra := gossh.Marshal(directTCPIPMsg{Addr: "127.0.0.1", Port: 9, OriginAddr: "10.255.255.1", OriginPort: 0})

	cases := []struct {
		name         string
		perms        *gossh.Permissions
		chType       string
		extra        []byte
		wantReason   gossh.RejectionReason // 注入用例为 0(不接受拒绝)
		wantMsg      string
		wantInjected bool
	}{
		{"bare×direct-tcpip injects", bareForm, channelDirectTCPIP, bareExtra, 0, "", true},
		{"bare×mesh-exit rejects", bareForm, channelMeshExit, meshTCPExtra("example.org", 443), gossh.Prohibited, "channel not allowed", false},
		{"mesh×mesh-exit injects", meshForm, channelMeshExit, meshTCPExtra("example.org", 443), 0, "", true},
		{"mesh×direct-tcpip rejects", meshForm, channelDirectTCPIP, bareExtra, gossh.Prohibited, "channel not allowed", false},
		{"unknown form×direct-tcpip rejects", unknownForm, channelDirectTCPIP, bareExtra, gossh.UnknownChannelType, "unknown credential form", false},
		{"unknown form×mesh-exit rejects", unknownForm, channelMeshExit, meshTCPExtra("example.org", 443), gossh.UnknownChannelType, "unknown credential form", false},
		{"nil perms rejects", nil, channelDirectTCPIP, bareExtra, gossh.UnknownChannelType, "unknown credential form", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")
			nc := &fakeNewChannel{chType: tc.chType, extra: tc.extra}
			driveServeChannels(t, s, tc.perms, nc)

			// 观测面在拒绝判定之前:被拒通道同样留下 form 痕迹(先读 form
			// 再 Reject 的次序钉死)。
			wantForm := ""
			if tc.perms != nil {
				wantForm = tc.perms.Extensions[extIngressForm]
			}
			if got := s.observedForm(); got != wantForm {
				t.Fatalf("observed form = %q, want %q (observe precedes reject)", got, wantForm)
			}

			if !tc.wantInjected {
				if !nc.rejected {
					t.Fatalf("%s must be rejected", tc.chType)
				}
				if nc.accepted {
					t.Fatalf("%s must be rejected before Accept", tc.chType)
				}
				if nc.reason != tc.wantReason {
					t.Fatalf("reject reason = %v, want %v", nc.reason, tc.wantReason)
				}
				if !strings.Contains(nc.message, tc.wantMsg) {
					t.Fatalf("reject message %q must contain %q", nc.message, tc.wantMsg)
				}
				return
			}
			if nc.rejected {
				t.Fatalf("allowed channel must not be rejected: %q", nc.message)
			}
			if !nc.accepted {
				t.Fatal("allowed channel must be accepted")
			}
			md := waitInjected(t, metaCh)
			assertInjectedMetadata(t, md, tc.chType)
		})
	}
}

// assertInjectedMetadata 断言两条注入路径共有的记账面:源取连接层
// (回环 40001),目标按各自形态拆解。
func assertInjectedMetadata(t *testing.T, md *M.Metadata, chType string) {
	t.Helper()
	if md.Network != M.TCP {
		t.Fatalf("Network = %v, want M.TCP", md.Network)
	}
	if !strings.HasPrefix(md.ConnID, "sshing-") {
		t.Fatalf("ConnID = %q, want prefix sshing-", md.ConnID)
	}
	if !md.SrcIP.IsValid() || md.SrcIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("SrcIP = %q, want 127.0.0.1 (from serveChannels remote)", md.SrcIP)
	}
	if md.SrcPort != 40001 {
		t.Fatalf("SrcPort = %d, want 40001", md.SrcPort)
	}
	switch chType {
	case channelDirectTCPIP:
		if md.HostName != "127.0.0.1" || md.DstPort != 9 {
			t.Fatalf("bare target = %s:%d, want 127.0.0.1:9", md.HostName, md.DstPort)
		}
	case channelMeshExit:
		if md.HostName != "example.org" || md.DstPort != 443 {
			t.Fatalf("mesh-exit target = %s:%d, want example.org:443", md.HostName, md.DstPort)
		}
		if md.Route != tcphandler.RouteMesh {
			t.Fatalf("Route = %q, want %q (mesh-exit provenance marker)", md.Route, tcphandler.RouteMesh)
		}
		if !md.MeshInjected {
			t.Fatal("MeshInjected must be set (loop guard, SPEC §6.2)")
		}
	}
}

// TestMeshExitExtraDataCap 钉 ExtraData 上限(SPEC §4):>meshExitMaxExtra
// 拒且发生在 payload 解析与门禁之前(cap 检查最靠前,gate=nil 也拦得住);
// 恰在 cap 上的合法 payload 放行(判据是 > 而非 >=)。
func TestMeshExitExtraDataCap(t *testing.T) {
	s := NewServer(RunConfig{}, "") // gate=nil:cap 先于门禁判定

	nc := &fakeNewChannel{chType: channelMeshExit, extra: bytes.Repeat([]byte("a"), meshExitMaxExtra+1)}
	driveServeChannels(t, s, meshTestPerms(), nc)
	if !nc.rejected || nc.accepted {
		t.Fatal("extra over cap must be rejected before Accept")
	}
	if nc.reason != gossh.Prohibited || !strings.Contains(nc.message, "payload too large") {
		t.Fatalf("reject = %v %q, want Prohibited payload too large", nc.reason, nc.message)
	}

	// 恰好 512 字节且仍是合法 tcp payload:放行(注入到达)。
	metaCh := make(chan *M.Metadata, 1)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		metaCh <- md
		return nil
	})
	padded := fmt.Sprintf(`{"t":"tcp","h":"%s.example.org","p":443}`,
		strings.Repeat("a", meshExitMaxExtra-len(`{"t":"tcp","h":".example.org","p":443}`)))
	if len(padded) != meshExitMaxExtra {
		t.Fatalf("fixture length = %d, want exactly %d", len(padded), meshExitMaxExtra)
	}
	s2 := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")
	atCap := &fakeNewChannel{chType: channelMeshExit, extra: []byte(padded)}
	driveServeChannels(t, s2, meshTestPerms(), atCap)
	if atCap.rejected {
		t.Fatalf("extra exactly at cap must pass, rejected %q", atCap.message)
	}
	md := waitInjected(t, metaCh)
	if !strings.HasSuffix(md.HostName, ".example.org") {
		t.Fatalf("HostName = %q, want padded host", md.HostName)
	}
}

// TestMeshExitBadPayload 钉 payload 校验面:非 JSON / 未知 t / tcp 缺 h 或
// p=0 全拒(Reject reason 回给 A 侧 OpenChannel,Accept 前完成)。
func TestMeshExitBadPayload(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantMsg string
	}{
		{"non json", "this is not json", "bad payload"},
		{"unknown t", `{"t":"weird"}`, "unknown payload type"},
		{"tcp missing h", `{"t":"tcp","p":443}`, "h/p required"},
		{"tcp zero port", `{"t":"tcp","h":"example.org"}`, "h/p required"},
		{"tcp empty host", `{"t":"tcp","h":"","p":443}`, "h/p required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")
			nc := &fakeNewChannel{chType: channelMeshExit, extra: []byte(tc.extra)}
			driveServeChannels(t, s, meshTestPerms(), nc)
			if !nc.rejected || nc.accepted {
				t.Fatalf("bad payload %s must be rejected before Accept", tc.extra)
			}
			if nc.reason != gossh.Prohibited {
				t.Fatalf("reject reason = %v, want Prohibited", nc.reason)
			}
			if !strings.Contains(nc.message, tc.wantMsg) {
				t.Fatalf("reject message %q must contain %q", nc.message, tc.wantMsg)
			}
		})
	}
}

// TestMeshExitForwardGateFailClosed 钉门禁(SPEC §10.1 决策 #14):nil=装配
// 未接线=拒绝(fail-closed,不再全放行);false=总开关关=拒绝;true=过闸
// 注入。全部发生在 Accept 前(拒)或紧随 Accept(过)。
func TestMeshExitForwardGateFailClosed(t *testing.T) {
	metaCh := make(chan *M.Metadata, 1)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		metaCh <- md
		return nil
	})

	cases := []struct {
		name         string
		gate         func() bool
		wantInjected bool
	}{
		{"nil gate rejects (fail-closed)", nil, false},
		{"gate false rejects", func() bool { return false }, false},
		{"gate true injects", func() bool { return true }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(RunConfig{ForwardGate: tc.gate}, "")
			nc := &fakeNewChannel{chType: channelMeshExit, extra: meshTCPExtra("example.org", 443)}
			driveServeChannels(t, s, meshTestPerms(), nc)
			if !tc.wantInjected {
				if !nc.rejected || nc.accepted {
					t.Fatal("closed gate must reject before Accept")
				}
				if !strings.Contains(nc.message, "forward disabled") {
					t.Fatalf("reject message %q must contain forward disabled", nc.message)
				}
				return
			}
			if nc.rejected || !nc.accepted {
				t.Fatal("open gate must accept")
			}
			md := waitInjected(t, metaCh)
			if md.Route != tcphandler.RouteMesh || !md.MeshInjected {
				t.Fatalf("Route/MeshInjected = %q/%v, want %q/true", md.Route, md.MeshInjected, tcphandler.RouteMesh)
			}
		})
	}
}

// TestMeshExitInjectsPipeline 线协议端到端小环(真 sshd+真客户端):mesh 凭证
// 开 mesh-exit 通道、JSON ExtraData 携带 tcp 目标 → 管线桩收到 Route=RouteMesh
// 与 MeshInjected=true 的 Metadata → 桩返回后通道关闭(客户端读到 EOF)。
// ExitClient 出口侧全链(A 侧发起)归 H1 集成测试。
func TestMeshExitInjectsPipeline(t *testing.T) {
	metaCh := make(chan *M.Metadata, 1)
	handleDone := make(chan struct{})
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		defer close(handleDone)
		metaCh <- md
		return nil
	})

	meshPub, meshSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{
		MeshKeys:    []MeshKey{{Key: meshPub, DeviceID: "dev-b"}},
		ForwardGate: func() bool { return true },
	})
	conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("mesh key must authenticate: %v", err)
	}
	ch, _, err := conn.OpenChannel(channelMeshExit, meshTCPExtra("example.org", 443))
	if err != nil {
		t.Fatalf("mesh-exit open: %v", err)
	}

	md := waitInjected(t, metaCh)
	if md.HostName != "example.org" || md.DstPort != 443 {
		t.Fatalf("target = %s:%d, want example.org:443", md.HostName, md.DstPort)
	}
	if md.Route != tcphandler.RouteMesh {
		t.Fatalf("Route = %q, want %q", md.Route, tcphandler.RouteMesh)
	}
	if !md.MeshInjected {
		t.Fatal("MeshInjected must be set")
	}
	if !md.SrcIP.IsValid() || md.SrcIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("SrcIP = %q, want 127.0.0.1", md.SrcIP)
	}
	if md.SrcPort == 0 {
		t.Fatal("SrcPort must carry the mesh node ephemeral port, got 0")
	}
	if !strings.HasPrefix(md.ConnID, "sshing-") {
		t.Fatalf("ConnID = %q, want prefix sshing-", md.ConnID)
	}

	// 收尾:桩返回→注入路径关通道→客户端读到 EOF。
	if _, err := ch.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("channel must be closed after pipeline handler returned, got %v", err)
	}
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline handler must return after channel teardown")
	}
}

// TestServeChannelsCarriesMeshDeviceID 通道层身份透传(D1 回调直测之外的
// 补钉):双 mesh 凭证各自连接的 extMeshDeviceID 经 serveChannels 读出并
// 记入观测面(mesh-exit 通道是否被拒不影响留痕——观测先于拒绝判定)。
func TestServeChannelsCarriesMeshDeviceID(t *testing.T) {
	pubA, signerA := newTestKey(t)
	pubB, signerB := newTestKey(t)
	s, addr := startTestServer(t, RunConfig{
		MeshKeys: []MeshKey{
			{Key: pubA, DeviceID: "dev-a"},
			{Key: pubB, DeviceID: "dev-b"},
		},
		// gate 缺省 nil=fail-closed:mesh-exit 一律拒,本用例只看观测面。
	})

	connA, err := dialIngress(t, addr, gossh.PublicKeys(signerA))
	if err != nil {
		t.Fatalf("mesh key A must authenticate: %v", err)
	}
	if _, _, aerr := connA.OpenChannel(channelMeshExit, meshTCPExtra("example.org", 443)); aerr == nil {
		t.Fatal("mesh-exit must be rejected with fail-closed gate")
	}
	if got := s.observedForm(); got != FormMesh {
		t.Fatalf("form = %q, want %q", got, FormMesh)
	}
	if got := s.observedDeviceID(); got != "dev-a" {
		t.Fatalf("device id = %q, want dev-a", got)
	}

	connB, err := dialIngress(t, addr, gossh.PublicKeys(signerB))
	if err != nil {
		t.Fatalf("mesh key B must authenticate: %v", err)
	}
	if _, _, berr := connB.OpenChannel(channelMeshExit, meshTCPExtra("example.org", 443)); berr == nil {
		t.Fatal("mesh-exit must be rejected with fail-closed gate")
	}
	if got := s.observedDeviceID(); got != "dev-b" {
		t.Fatalf("device id = %q, want dev-b (second connection overwrites the last-channel observation)", got)
	}
}

// TestWithRemoteLabelRateLimitKey 钉远端标签(SPEC §8.2):包装后
// remoteIP 返回 label 原样(限速按 piko 对端身份串桶隔离,修 yamux 流探测
// 到底层 websocket 对端导致的共用限速键);netAddrToIPPort 对 label 地址
// 解析失败 → SrcIP/SrcPort 归零(已知副作用钉住,peer 真实来源在统计面
// 不可知)。label 禁含冒号:含冒号会被 SplitHostPort 成功拆分(host=冒号
// 前段),修法完全失效——此处实证该陷阱以钉住调用方契约。
func TestWithRemoteLabelRateLimitKey(t *testing.T) {
	c, _ := net.Pipe()
	defer func() { _ = c.Close() }()

	labeled := WithRemoteLabel(c, "piko-dev-b.mesh")
	if labeled == nil {
		t.Fatal("non-nil conn must be wrapped")
	}
	if got := remoteIP(labeled.RemoteAddr()); got != "piko-dev-b.mesh" {
		t.Fatalf("remoteIP = %q, want label verbatim %q", got, "piko-dev-b.mesh")
	}
	if got := labeled.RemoteAddr().String(); got != "piko-dev-b.mesh" {
		t.Fatalf("RemoteAddr().String() = %q, want label verbatim", got)
	}

	// nil conn 原样透传。
	if WithRemoteLabel(nil, "x") != nil {
		t.Fatal("nil conn must pass through unchanged")
	}

	// 无冒号契约的陷阱实证:冒号 label 被拆出 host 段,限速键退化为前缀。
	if got := remoteIP(&remoteLabelAddr{label: "piko:dev-b"}); got != "piko" {
		t.Fatalf("colon label splits to host %q (the trap the no-colon contract forbids)", got)
	}

	// 统计面副作用钉住:label 地址进不了 netAddrToIPPort,归零。
	ip, port := netAddrToIPPort(labeled.RemoteAddr())
	if ip.IsValid() || port != 0 {
		t.Fatalf("netAddrToIPPort = (%v,%d), want zero values (SPEC §8.2 known side effect)", ip, port)
	}
}
