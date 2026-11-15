package tcp

import (
	"context"
	"net"
	"net/netip"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// dialDirect 域名回退回归钉：SSH 入口 direct-tcpip 注入与 http CONNECT
// 解析失败路径都会产出「DstIP 无效、仅 HostName」的元数据
// （inbound/ssh/channel.go 注释口径：管线在隧道期拨号时重试解析）。
// 此前空 DstIP 经 String() 变 "invalid IP" 拨号秒败，SSH 隧道内直连全断。
func TestDialDirectFallbackToHostNameWhenDstIPInvalid(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	h := NewTCPConnectionHandler(nil, nil, nil, nil)
	ctx := context.Background()

	dialAndAccept := func(t *testing.T, metadata *M.Metadata) net.Conn {
		t.Helper()
		conn, err := h.dialDirect(ctx, metadata)
		if err != nil {
			t.Fatalf("dialDirect(%+v): %v", metadata, err)
		}
		serverConn, err := ln.Accept()
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		t.Cleanup(func() {
			_ = serverConn.Close()
			_ = conn.Close()
		})
		return conn
	}

	t.Run("invalid-dstip-falls-back-to-hostname", func(t *testing.T) {
		// SSH 注入形态：只有 HostName，DstIP 零值无效
		metadata := &M.Metadata{HostName: "127.0.0.1", DstPort: port}
		if metadata.DstIP.IsValid() {
			t.Fatalf("precondition: zero DstIP must be invalid, got %q", metadata.DstIP)
		}
		dialAndAccept(t, metadata)
	})

	t.Run("valid-dstip-still-dials-ip", func(t *testing.T) {
		// 原路径不回归：DstIP 有效时仍按 IP 拨号
		metadata := &M.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: port}
		dialAndAccept(t, metadata)
	})

	t.Run("no-destination-errors-explicitly", func(t *testing.T) {
		// IP 与域名皆空：显式报错，不得再产出 "invalid IP:port" 地址
		metadata := &M.Metadata{DstPort: port}
		if _, err := h.dialDirect(ctx, metadata); err == nil {
			t.Fatal("dialDirect with invalid IP and empty hostname should error")
		}
	})
}
