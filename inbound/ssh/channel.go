package ssh

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"

	gossh "golang.org/x/crypto/ssh"
)

// directTCPIPMsg 与 RFC 4254 §7.2 direct-tcpip 通道附加数据同构:
// Addr/Port=转发目标,OriginAddr/OriginPort=客户端自报的原始源四元组。
type directTCPIPMsg struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

// sshIngressConnCounter 生成裸流注入的 ConnID 序号(sshing-<N>,
// 镜像 inbound/tun 的 tun-<N> 口径)。
var sshIngressConnCounter uint64

// injectBareStream 是形态①(裸流)注入路径:解析 direct-tcpip payload →
// 构造 M.Metadata → 喂 processor/tcp 统一管线(路由决策+双向中继)。
// 注意 M=inbound/tun/metadata(processor/tcp/types.go 另有同名 Metadata、
// 无 ConnID,勿混)。
//
// 阻塞语义:Handle 阻塞直到连接结束,本函数须以独立 goroutine 驱动
// (与 drain 协程同边界:不入 WaitGroup);在途 Handle 靠连接级 ctx 取消
// +连接拆除有界退出,TestStopWithInflightChannel 钉住。Handle 返回后
// 通道关闭(接口约定管线侧也会关,defer 双保险,重复 Close 无害)。
func injectBareStream(ctx context.Context, ch gossh.Channel, connRemote net.Addr, payload []byte) {
	defer ch.Close()

	var msg directTCPIPMsg
	if err := gossh.Unmarshal(payload, &msg); err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress direct-tcpip payload unmarshal failed: %v", err))
		return
	}
	if msg.Addr == "" || msg.Port == 0 || msg.Port > 65535 {
		logger.Debug(fmt.Sprintf("SSH ingress direct-tcpip invalid target %s:%d", msg.Addr, msg.Port))
		return
	}

	metadata := &M.Metadata{
		Network: M.TCP, // uint8 iota(metadata 包 0=TCP),非字符串
		ConnID:  fmt.Sprintf("sshing-%d", atomic.AddUint64(&sshIngressConnCounter, 1)),
		// DstIP 不设(IP 字面量目标同样留空):与 inbound/http CONNECT 对
		// 不可解析域名的口径一致——管线在隧道期拨号时重试解析。
		HostName: msg.Addr,
		DstPort:  uint16(msg.Port),
	}
	// 源地址取 SSH 连接层 RemoteAddr:payload 的 Origin 字段是客户端
	// 自报值不可作记账依据;连接层地址才可与限速/统计关联。
	metadata.SrcIP, metadata.SrcPort = netAddrToIPPort(connRemote)

	logger.Debug(fmt.Sprintf("SSH ingress bare stream injected conn_id=%s target=%s:%d",
		metadata.ConnID, metadata.HostName, metadata.DstPort))
	if err := tcphandler.GetHandler().Handle(ctx, newChannelConn(ch, msg), metadata); err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress bare stream conn_id=%s handle: %v", metadata.ConnID, err))
	}
}

// channelConn 把 gossh.Channel 适配成 net.Conn 喂管线:v0.48 的
// gossh.Channel 接口本身无 LocalAddr/RemoteAddr/deadline 方法
// (net.Conn 适配 chanConn 未导出,仅客户端侧 Dial 路径可得),服务端
// 需自配(镜像出口侧 trackedChannel,但无连接池活跃计数诉求)。
// LocalAddr=direct-tcpip 目标、RemoteAddr=客户端自报源,取 payload
// 四元组(仅供管线日志/统计;权威记账用连接层地址,见 injectBareStream)。
// deadline 为 no-op:SSH 通道无 deadline 语义,返回 nil 让 relay 的良性
// 设置不失败——relay 依赖 SetReadDeadline 的「半关闭后不无限等」保护
// 对此入口静默失效,与出口侧已知限制同源
// (docs/superpowers/specs/2026-11-07-ssh-proxy-design.md 已知限制节)。
type channelConn struct {
	gossh.Channel
	local, remote net.Addr
}

func newChannelConn(ch gossh.Channel, msg directTCPIPMsg) *channelConn {
	return &channelConn{
		Channel: ch,
		local:   &net.TCPAddr{IP: net.ParseIP(msg.Addr), Port: int(msg.Port)},
		remote:  &net.TCPAddr{IP: net.ParseIP(msg.OriginAddr), Port: int(msg.OriginPort)},
	}
}

func (c *channelConn) LocalAddr() net.Addr  { return c.local }
func (c *channelConn) RemoteAddr() net.Addr { return c.remote }

func (c *channelConn) SetDeadline(time.Time) error      { return nil }
func (c *channelConn) SetReadDeadline(time.Time) error  { return nil }
func (c *channelConn) SetWriteDeadline(time.Time) error { return nil }

// netAddrToIPPort 把 net.Addr 换成 (netip.Addr, port);失败返回零值
// (源地址是尽力记账语义,管线路由不依赖它)。
func netAddrToIPPort(a net.Addr) (netip.Addr, uint16) {
	if a == nil {
		return netip.Addr{}, 0
	}
	host, portStr, err := net.SplitHostPort(a.String())
	if err != nil {
		return netip.Addr{}, 0
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return netip.Addr{}, 0
	}
	return addr.Unmap(), uint16(port)
}
