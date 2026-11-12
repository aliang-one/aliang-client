package mesh

// mesh 出口侧公共小件测试:类别判定、继承帧头 context 缝契约(B 侧专用,
// F1 删)、aiProxyPort 同步锚。出口拨号本体(ExitClient)的
// 全矩阵见 sshexit_test.go;目标构造见 payload_test.go。

import (
	"context"
	"net/netip"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/config"
)

// TestClassInference 类别判定:显式 WithClass 优先;默认推断走
// looksLikeAITraffic(56432 回环口径,与 processor/tcp route_override 同源)。
func TestClassInference(t *testing.T) {
	aiMD := &M.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 56432}
	if got := classFromContext(context.Background(), aiMD); got != meshproto.ClassAI {
		t.Fatalf("class = %q, want ai", got)
	}
	// 端口对但目标非回环 → custom
	if got := classFromContext(context.Background(), &M.Metadata{DstIP: netip.MustParseAddr("1.1.1.1"), DstPort: 56432}); got != meshproto.ClassCustom {
		t.Fatalf("class = %q, want custom", got)
	}
	// 回环但端口不对 → custom
	if got := classFromContext(context.Background(), &M.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: 8443}); got != meshproto.ClassCustom {
		t.Fatalf("class = %q, want custom", got)
	}
	// hostName=localhost 且端口对 → ai(HostName 回环判据)
	if got := classFromContext(context.Background(), &M.Metadata{HostName: "localhost", DstPort: 56432}); got != meshproto.ClassAI {
		t.Fatalf("class = %q, want ai", got)
	}
	// 显式指定优先于端口推断
	for _, class := range []string{meshproto.ClassSelf, meshproto.ClassAI, meshproto.ClassCustom} {
		ctx := WithClass(context.Background(), class)
		if got := classFromContext(ctx, aiMD); got != class {
			t.Fatalf("explicit class = %q, want %q", got, class)
		}
	}
	// 非法类别值按未指定处理,回落推断
	ctx := WithClass(context.Background(), "bogus")
	if got := classFromContext(ctx, aiMD); got != meshproto.ClassAI {
		t.Fatalf("bogus class should fall back to inference, got %q", got)
	}
}

// TestAIProxyPortSyncAnchor 锚定本地 aiProxyPort 与 processor/config 的
// DefaultHTTPProxyPort 同值(test-only import 无环已实测;两处判据须同步改,
// 见 mesh.go looksLikeAITraffic 注释)。
func TestAIProxyPortSyncAnchor(t *testing.T) {
	if aiProxyPort != config.DefaultHTTPProxyPort {
		t.Fatalf("aiProxyPort = %d, want %d (config.DefaultHTTPProxyPort)——两处判据失同步",
			aiProxyPort, config.DefaultHTTPProxyPort)
	}
}
