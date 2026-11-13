package mesh

import (
	"context"
	"net"
	"time"

	"aliang.one/nursorgate/outbound/meshproto"
)

// lanDialTimeout 是 LAN 载体单地址拨号兜底超时;ctx 自带 deadline 时以 ctx
// 为准(更紧者生效)。
const lanDialTimeout = 5 * time.Second

// Transport 提供一条到对端 sshd 的、尚未握手的 net.Conn。无状态,可跨拨号复用。
type Transport interface {
	Name() string                           // "lan" / "udp" / "piko"
	Admit(class string, peer PeerSpec) bool // 载体准入:custom 禁走公共中继;LAN 还需有地址
	IsRelay() bool                          // 是否中继载体:ExitClient.DialContext 错误分桶的判据
	DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error)
}

// LANTransport LAN 直连载体:peer.LANAddrs 顺序尝试,全类别可达,但无地址
// 时 admit=false(空地址 peer 落到 errNoTransportAdmits,而不是报一个含
// 0 个地址的费解错误,SPEC §7.1;errNoTransportAdmits 由 Task C4 定义)。
type LANTransport struct{}

func (LANTransport) Name() string { return "lan" }

func (LANTransport) Admit(_ string, peer PeerSpec) bool { return len(peer.LANAddrs) > 0 }

func (LANTransport) IsRelay() bool { return false }

func (LANTransport) DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error) {
	var lastErr error
	for _, addr := range peer.LANAddrs {
		conn, err := dialLAN(ctx, addr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return nil, lastErr
}

// PikoTransport piko 中继载体:endpointID 由本 Transport 拼装
// (`<deviceID>.mesh`),注入的 dial func 只收拼好的 endpointID。类别准入:
// 仅 self/ai(custom 禁公共中继,SPEC §4.3/§7.1);relay 未接线时
// ExitClient 构造期就不会把本 Transport 放进切片(SPEC §7.3)。
type PikoTransport struct {
	dial PikoDialFunc
}

func (PikoTransport) Name() string { return "piko" }

func (PikoTransport) Admit(class string, _ PeerSpec) bool {
	return class == meshproto.ClassSelf || class == meshproto.ClassAI
}

func (PikoTransport) IsRelay() bool { return true }

func (t PikoTransport) DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error) {
	return t.dial(ctx, peer.DeviceID+meshRelayEndpointSuffix)
}

func dialLAN(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		d.Timeout = lanDialTimeout
	}
	return d.DialContext(ctx, "tcp", addr)
}
