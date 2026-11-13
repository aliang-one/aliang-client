package mesh

import (
	"context"
	"net"
	"testing"

	"aliang.one/nursorgate/outbound/meshproto"
	gossh "golang.org/x/crypto/ssh"
)

// TestUDPTransportAdmitMatrix 钉住 UDP 载体的准入语义(SPEC §7.1 载体表:
// 打洞承载全类别,SPEC:560 预留语义已拍板)与 gate 关防御分支:
// 构造期 gate 关不进切片是第一层把关(NewExitClient),零值 Transport 的
// Admit 恒 false 是第二层——双层把关,评审要求直断言。
func TestUDPTransportAdmitMatrix(t *testing.T) {
	udp := UDPTransport{dial: func(context.Context, string) (net.Conn, error) { return nil, nil }}
	for _, class := range []string{meshproto.ClassSelf, meshproto.ClassAI, meshproto.ClassCustom} {
		if !udp.Admit(class, PeerSpec{}) {
			t.Fatalf("udp transport must admit class %q", class)
		}
	}

	// gate 关构造变体(dial=nil,零值):即使有人绕过 NewExitClient 的装配
	// 门手工塞进切片,也不得放行任何类别(含非法空类别)。
	gateClosed := UDPTransport{}
	for _, class := range []string{meshproto.ClassSelf, meshproto.ClassAI, meshproto.ClassCustom, ""} {
		if gateClosed.Admit(class, PeerSpec{}) {
			t.Fatalf("gate-closed UDPTransport must admit nothing, class %q admitted", class)
		}
	}

	// IsRelay=false:打洞是直连语义,拨号失败进 lan 桶(DialContext 错误
	// 分桶的判据),不与中继错误混报。
	if udp.IsRelay() || gateClosed.IsRelay() {
		t.Fatal("udp transport is direct-connect semantics, IsRelay must be false")
	}
}

// TestUDPTransportDialInjectsPeerID 钉住注入缝:dial func 收裸
// peer.DeviceID(无 .mesh 后缀——那是 piko 专属拼装),返回 conn 原样透传。
func TestUDPTransportDialInjectsPeerID(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	var got string
	udp := UDPTransport{dial: func(_ context.Context, peerID string) (net.Conn, error) {
		got = peerID
		return c1, nil
	}}
	conn, err := udp.DialSSHD(context.Background(), PeerSpec{DeviceID: "dev-b"})
	if err != nil {
		t.Fatalf("DialSSHD: %v", err)
	}
	if got != "dev-b" {
		t.Fatalf("peer id = %q, want bare DeviceID %q", got, "dev-b")
	}
	if conn != net.Conn(c1) {
		t.Fatal("DialSSHD must return the injected conn untouched")
	}

	// gate 关变体的 DialSSHD 防御:返回错误,而不是 nil dial func panic
	// (正常路径被 Admit=false 挡住,此处钉住「直接误调也不炸」)。
	if _, err := (UDPTransport{}).DialSSHD(context.Background(), PeerSpec{DeviceID: "dev-b"}); err == nil {
		t.Fatal("gate-closed UDPTransport.DialSSHD must return an error, not nil-panic")
	}
}

// TestNewExitClientTransportOrder 钉住装配序(SPEC §7.3):LAN 恒前、
// piko 恒后,UDP 打洞开时居中;punch gate 关(punchDial=nil)与 piko 未接线
// 各自只摘掉自己一格,既有行为零变化。
func TestNewExitClientTransportOrder(t *testing.T) {
	keyPEM, _ := testIdentity(t)
	hostFP := gossh.FingerprintSHA256(genPubKey(t)) // 只构造不握手,任意真钉扎即可

	names := func(t *testing.T, c *ExitClient) []string {
		t.Helper()
		return c.TransportNames() // 载体观测面(Task 12 导出,跨包测试共用)
	}
	assertNames := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("transports = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("transports = %v, want %v", got, want)
			}
		}
	}
	pikoDial := func(context.Context, string) (net.Conn, error) { return nil, nil }
	punchDial := func(context.Context, string) (net.Conn, error) { return nil, nil }
	relaySpec := PeerSpec{DeviceID: "dev-b", HostKey: hostFP, SSHUser: "mesh", Relay: true}

	t.Run("punch and relay both wired", func(t *testing.T) {
		client, err := NewExitClient(relaySpec, keyPEM, "", pikoDial, punchDial)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		assertNames(t, names(t, client), []string{"lan", "udp", "piko"})
	})
	t.Run("punch gate closed keeps todays behavior", func(t *testing.T) {
		client, err := NewExitClient(relaySpec, keyPEM, "", pikoDial, nil)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		assertNames(t, names(t, client), []string{"lan", "piko"})
	})
	t.Run("piko unwired leaves udp in place", func(t *testing.T) {
		client, err := NewExitClient(relaySpec, keyPEM, "", nil, punchDial)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		assertNames(t, names(t, client), []string{"lan", "udp"})
	})
	t.Run("relay off and punch closed is lan only", func(t *testing.T) {
		noRelay := relaySpec
		noRelay.Relay = false
		client, err := NewExitClient(noRelay, keyPEM, "", nil, nil)
		if err != nil {
			t.Fatalf("NewExitClient: %v", err)
		}
		assertNames(t, names(t, client), []string{"lan"})
	})
}

// TestPeerSpecCacheKeyPunchBit 钉住 CacheKey 的 punch 位:tcp 主路径按
// CacheKey 缓存 ExitClient,运行期翻 punch 开关必须产出新键→缓存重建
// (开关静默失效到进程重启是本位要堵的洞;工厂置位归 Task 12)。
func TestPeerSpecCacheKeyPunchBit(t *testing.T) {
	base := PeerSpec{
		DeviceID: "dev-b",
		LANAddrs: []string{"1.1.1.1:56432"},
		HostKey:  "h",
		SSHUser:  "mesh",
		Relay:    true,
		Punch:    false,
	}
	on := base
	on.Punch = true
	if base.CacheKey("pk", "") == on.CacheKey("pk", "") {
		t.Fatal("punch bit change must change key (翻开关须重建缓存客户端)")
	}
	if base.CacheKey("pk", "") != base.CacheKey("pk", "") {
		t.Fatal("identical specs must share a key")
	}
}
