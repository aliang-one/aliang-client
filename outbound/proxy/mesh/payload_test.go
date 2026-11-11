package mesh

import (
	"net/netip"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// TestHostPortFromMetadataFourForms 钉 SPEC §15.1 的四种输入形态:CONNECT
// 路径的 HostName 已带端口、裸域名、裸 IPv4、裸 IPv6(无中括号——
// net.SplitHostPort 不认,必须落回 DstPort 兜底而不是被误拆)。
func TestHostPortFromMetadataFourForms(t *testing.T) {
	cases := []struct {
		name     string
		hostName string
		dstIP    netip.Addr
		dstPort  uint16
		wantHost string
		wantPort string
	}{
		{"bare domain", "x.com", netip.Addr{}, 443, "x.com", "443"},
		{"host with port (CONNECT)", "x.com:8443", netip.Addr{}, 8443, "x.com", "8443"},
		{"bare ipv4", "1.2.3.4", netip.MustParseAddr("1.2.3.4"), 0, "1.2.3.4", "443"},
		{"bare ipv6 no brackets", "::1", netip.MustParseAddr("::1"), 0, "::1", "443"},
		// DstIP 直取形态(HostName 空):显式端口不落 443 兜底。
		{"empty host + dstip explicit port", "", netip.MustParseAddr("5.6.7.8"), 53, "5.6.7.8", "53"},
		// 纯空白 HostName 经 TrimSpace 等价空串,同样落 DstIP 分支。
		{"whitespace host falls to dstip", "   ", netip.MustParseAddr("10.0.0.1"), 8443, "10.0.0.1", "8443"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port, ok := hostPortFromMetadata(&M.Metadata{HostName: c.hostName, DstIP: c.dstIP, DstPort: c.dstPort})
			if !ok || host != c.wantHost || port != c.wantPort {
				t.Fatalf("got (%q,%q,%v) want (%q,%q,true)", host, port, ok, c.wantHost, c.wantPort)
			}
		})
	}
}

// TestHostPortFromMetadataUnbuildable 不可构造形态:md=nil 与
// HostName 空 + DstIP 无效都必须 ok=false,由调用方按「无法构造目标」拒绝。
func TestHostPortFromMetadataUnbuildable(t *testing.T) {
	if _, _, ok := hostPortFromMetadata(nil); ok {
		t.Fatal("nil md must be unbuildable")
	}
	if _, _, ok := hostPortFromMetadata(&M.Metadata{}); ok {
		t.Fatal("empty host + invalid DstIP must be unbuildable")
	}
}

// TestExitPayloadVariants 钉 mesh-exit channel ExtraData 的编码形态:
// tcp 带 h/p(JSON 数字端口)、dns 无 h/p、不可构造目标一律 nil。
func TestExitPayloadVariants(t *testing.T) {
	if got := exitPayload(&M.Metadata{HostName: "x.com", DstPort: 443}); string(got) != `{"t":"tcp","h":"x.com","p":443}` {
		t.Fatalf("tcp payload = %s", got)
	}
	if got := exitPayload(&M.Metadata{HostName: "dns:"}); string(got) != `{"t":"dns"}` {
		t.Fatalf("dns payload = %s", got)
	}
	if got := exitPayload(nil); got != nil {
		t.Fatalf("nil md must yield nil payload, got %s", got)
	}
	if got := exitPayload(&M.Metadata{HostName: "   "}); got != nil {
		t.Fatalf("unbuildable target must yield nil, got %s", got)
	}
	if got := exitPayload(&M.Metadata{HostName: "x.com:99999", DstPort: 0}); got != nil {
		t.Fatalf("port overflow must yield nil, got %s", got)
	}
	// 带中括号的 IPv6:SplitHostPort 剥括号,host 落裸 "::1"。
	if got := exitPayload(&M.Metadata{HostName: "[::1]:8443"}); string(got) != `{"t":"tcp","h":"::1","p":8443}` {
		t.Fatalf("bracketed ipv6 payload = %s", got)
	}
	// 非数字端口:host:port 形态解析成功但端口转 uint16 失败 ⇒ 不可构造 nil。
	if got := exitPayload(&M.Metadata{HostName: "x.com:abc"}); got != nil {
		t.Fatalf("non-numeric port must yield nil, got %s", got)
	}
	// 导出缝与内部实现同义(processor/tcp 兜底拨号路径消费,Task E1)。
	if got := ExitPayloadForMetadata(&M.Metadata{HostName: "x.com", DstPort: 443}); string(got) != `{"t":"tcp","h":"x.com","p":443}` {
		t.Fatalf("exported alias payload = %s", got)
	}
}
