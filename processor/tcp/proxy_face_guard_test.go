package tcp

// 代理面目标守卫测试:非回环来源经统一入口代理面(HTTP CONNECT/明文、socks5)
// 进来的连接,不得访问本机回环/内网目标,也不得使用「AI base-URL」形态
// (目标是回环 56432——那会带着本机登录态进平台隧道)。
//
// 背景(2026-11-24 审计,跨机实证):56432 面唯一的门是全机一个会话租约,没有
// 目标 ACL;回环/私网目标被特权网关进程直接代拨,于是 56431/56433 看到的
// 来源恒为回环——管理面所有「回环即信任」的判定对局域网客户端全部成立。

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
)

func TestCheckProxyFaceDestination(t *testing.T) {
	lanSrc := netip.MustParseAddr("192.168.1.20")
	loSrc := netip.MustParseAddr("127.0.0.1")
	mappedLoSrc := netip.MustParseAddr("::ffff:127.0.0.1")

	cases := []struct {
		name    string
		md      *M.Metadata
		wantErr bool
	}{
		{name: "nil 元数据", md: nil, wantErr: false},
		{
			name: "非代理面流量不受限(TUN/SSH 入口等,即使目标是回环)",
			md:   &M.Metadata{SrcIP: lanSrc, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56431},
		},
		{
			name: "回环来源的代理面可访问回环(本机应用既有用法)",
			md:   &M.Metadata{IngressProxyFace: true, SrcIP: loSrc, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56431},
		},
		{
			name: "IPv4-mapped 回环来源视为回环",
			md:   &M.Metadata{IngressProxyFace: true, SrcIP: mappedLoSrc, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56431},
		},
		{
			name: "来源未知(非网络连接)不受限",
			md:   &M.Metadata{IngressProxyFace: true, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56431},
		},
		{
			name:    "非回环来源的代理面访问回环被拒",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56431},
			wantErr: true,
		},
		{
			name:    "IPv6 回环目标同样被拒",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("::1"), DstPort: 56433},
			wantErr: true,
		},
		{
			name:    "IPv4-mapped 回环目标同样被拒",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("::ffff:127.0.0.1"), DstPort: 56431},
			wantErr: true,
		},
		{
			name:    "非回环来源的代理面访问私网被拒",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("192.168.1.5"), DstPort: 80},
			wantErr: true,
		},
		{
			name:    "非回环来源的代理面访问链路本地被拒(元数据服务等)",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("169.254.169.254"), DstPort: 80},
			wantErr: true,
		},
		{
			name:    "非回环来源的代理面访问 CGNAT 段被拒",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("100.64.0.1"), DstPort: 80},
			wantErr: true,
		},
		{
			name:    "非回环来源的代理面访问未指定地址被拒",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("0.0.0.0"), DstPort: 80},
			wantErr: true,
		},
		{
			name: "非回环来源的代理面访问公网放行",
			md:   &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, DstIP: netip.MustParseAddr("8.8.8.8"), DstPort: 443},
		},
		{
			name: "非回环来源的代理面访问公网域名放行",
			md:   &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, HostName: "example.com", DstPort: 443},
		},
		{
			name:    "AI base-URL 形态(localhost:56432)对非回环来源被拒——会带本机登录态进平台隧道",
			md:      &M.Metadata{IngressProxyFace: true, SrcIP: lanSrc, HostName: "localhost", DstPort: config.DefaultHTTPProxyPort},
			wantErr: true,
		},
		{
			name: "回环来源的 AI base-URL 形态放行",
			md:   &M.Metadata{IngressProxyFace: true, SrcIP: loSrc, HostName: "localhost", DstPort: config.DefaultHTTPProxyPort},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckProxyFaceDestination(tc.md)
			if tc.wantErr {
				if !errors.Is(err, ErrProxyFaceLocalDestination) {
					t.Fatalf("want ErrProxyFaceLocalDestination, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Handle 是所有入站的汇合点:守卫必须在协议分派之前生效(回环/私网目标的
// 直拨短路、AI base-URL 的强制平台路由都在其后面)。
func TestHandleRejectsProxyFaceLoopbackTargetBeforeDial(t *testing.T) {
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	ctx := context.Background()

	blocked := &M.Metadata{
		Network:          M.TCP,
		IngressProxyFace: true,
		SrcIP:            netip.MustParseAddr("192.168.1.20"),
		DstIP:            netip.MustParseAddr("127.0.0.1"),
		DstPort:          56431,
	}
	if err := handler.Handle(ctx, newFakeConn(nil), blocked); !errors.Is(err, ErrProxyFaceLocalDestination) {
		t.Fatalf("want ErrProxyFaceLocalDestination, got %v", err)
	}

	// 对照:回环来源走原路径(这里目标是 127.0.0.1:1,拨号失败属预期,
	// 只断言没有被守卫拒绝)。
	allowed := &M.Metadata{
		Network:          M.TCP,
		IngressProxyFace: true,
		SrcIP:            netip.MustParseAddr("127.0.0.1"),
		DstIP:            netip.MustParseAddr("127.0.0.1"),
		DstPort:          1,
	}
	err := handler.Handle(ctx, newFakeConn(nil), allowed)
	if errors.Is(err, ErrProxyFaceLocalDestination) {
		t.Fatal("回环来源的代理面连接不得被守卫拒绝")
	}
}

// 注入流(mesh-exit 转发而来)的域名目标在拨号时校验:域名解析到本机回环/
// 链路本地即拒——入站口的字面量 ACL 拦不住「localhost:56431」或 nip.io 类
// 指向 127.0.0.1 的名字。解析结果钉进 DstIP,杜绝 DNS 重绑定(校验一次解析、
// 拨号不再二次解析)。
func TestDialDirectVetsInjectedDomainTarget(t *testing.T) {
	config.ResetRoutingApplyStoreForTest()
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
	ctx := context.Background()

	blocked := &M.Metadata{
		Network:      M.TCP,
		MeshInjected: true,
		HostName:     "localhost",
		DstPort:      56431,
	}
	_, _, err := handler.handleNonTLS(ctx, newFakeConn(nil), blocked)
	if !errors.Is(err, ErrInjectedLocalDestination) {
		t.Fatalf("want ErrInjectedLocalDestination, got %v", err)
	}
	if blocked.DstIP.IsValid() {
		t.Fatalf("被拒的注入流不得留下钉住的 DstIP, got %s", blocked.DstIP)
	}

	// 对照:同样的域名目标,非注入流(本机应用访问 localhost)不受此校验——
	// 走既有路径(这里是回环目标 127.0.0.1:1,拨号失败属预期)。
	local := &M.Metadata{
		Network:  M.TCP,
		HostName: "localhost",
		DstPort:  1,
	}
	_, _, err = handler.handleNonTLS(ctx, newFakeConn(nil), local)
	if errors.Is(err, ErrInjectedLocalDestination) {
		t.Fatal("非注入流的域名目标不得被注入校验拒绝")
	}
}
