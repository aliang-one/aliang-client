// mesh_inject_policy_test.go — mesh-exit 目标 ACL(2026-11-24 审计):转发端
// (forward_enable=true)此前对已准入对端开放任意目标,含本机回环服务——
// 56431 管理 API 的信任边界是「来源为回环」,而代拨发生在网关进程里,对端
// 经 mesh-exit 让转发端代连 127.0.0.1:56431 即可改写它的配置。
//
// 放行面刻意收窄到「出口链路委托的 AI base-URL 载荷」(目标=回环 56432):
// 链的末端设备据此在自己的管线上走 aliang 分支出网。其余回环/链路本地/
// 未指定目标一律在 Accept 前拒绝;公网与转发端局域网内的目标照常。
package ssh

import (
	"context"
	"net"
	"strings"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
)

func TestMeshExitDestinationPolicy(t *testing.T) {
	metaCh := make(chan *M.Metadata, 16)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		metaCh <- md
		return nil
	})

	cases := []struct {
		name       string
		host       string
		port       uint16
		wantAccept bool
	}{
		{"AI base-URL 形态(127.0.0.1:56432)放行——委托链的合法载荷", "127.0.0.1", config.DefaultHTTPProxyPort, true},
		{"AI base-URL 形态(localhost:56432)放行", "localhost", config.DefaultHTTPProxyPort, true},
		{"IPv6 回环的 AI base-URL 形态放行", "::1", config.DefaultHTTPProxyPort, true},
		{"回环管理 API(127.0.0.1:56431)被拒", "127.0.0.1", 56431, false},
		{"回环 agent runtime(127.0.0.1:56433)被拒", "127.0.0.1", 56433, false},
		{"localhost 的非 56432 端口被拒", "localhost", 56431, false},
		{"IPv6 回环其它端口被拒", "::1", 56431, false},
		{"IPv4-mapped 回环其它端口被拒", "::ffff:127.0.0.1", 56431, false},
		{"链路本地(元数据服务等)被拒", "169.254.169.254", 80, false},
		{"未指定地址被拒", "0.0.0.0", 80, false},
		{"公网域名放行", "example.com", 443, true},
		{"公网 IP 放行", "8.8.8.8", 443, true},
		{"转发端局域网内的目标放行(节点转发的本意)", "192.168.1.5", 80, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")
			nc := &fakeNewChannel{chType: channelMeshExit, extra: meshTCPExtra(tc.host, tc.port)}
			driveServeChannels(t, s, meshTestPerms(), nc)

			if !tc.wantAccept {
				if !nc.rejected || nc.accepted {
					t.Fatal("disallowed target must be rejected before Accept")
				}
				if nc.reason != 0 && !strings.Contains(nc.message, "target not allowed") {
					t.Fatalf("reject message %q must say target not allowed", nc.message)
				}
				select {
				case <-metaCh:
					t.Fatal("disallowed target must never reach the pipeline")
				default:
				}
				return
			}
			if nc.rejected || !nc.accepted {
				t.Fatalf("allowed target must be accepted, rejected=%v msg=%q", nc.rejected, nc.message)
			}
			md := waitInjected(t, metaCh)
			if md.HostName != tc.host || md.DstPort != tc.port {
				t.Fatalf("injected target = %s:%d, want %s:%d", md.HostName, md.DstPort, tc.host, tc.port)
			}
		})
	}
}

// dns 目标不经目标 ACL(B 侧代解析走系统 resolver,解析结果不用于回连)。
func TestMeshExitDestinationPolicySkipsDNS(t *testing.T) {
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")
	// 载荷只有类型键:代解析协程在管道上等首帧,由 cleanup 关对端收尾。
	nc := &fakeNewChannel{chType: channelMeshExit, extra: []byte(`{"t":"dns"}`)}
	driveServeChannels(t, s, meshTestPerms(), nc)
	if nc.rejected || !nc.accepted {
		t.Fatalf("dns target must not be subject to the destination policy: rejected=%v msg=%q", nc.rejected, nc.message)
	}
}
