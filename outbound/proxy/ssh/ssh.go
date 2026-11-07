// Package ssh 实现 SSH 出口代理:TCP 走 direct-tcpip 通道(等价 ssh -D 的
// TCP 能力),UDP SSH 协议不支持(返回 ErrUnsupported)。链=可选逐跳穿透
// (ProxyJump 语义),池=N 条独立链按活跃数分发。
package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
	gossh "golang.org/x/crypto/ssh"
)

var _ proxy.Proxy = (*SSH)(nil)

// SSH 是 SSH 出口代理:DialContext 从链池取一条 SSH 链(可选多跳),在其
// 最终跳上开 direct-tcpip 通道承载 TCP;UDP 不支持;Close 断开全部链并
// 终结实例(其后 DialContext 直接报错,不再懒重建)。
type SSH struct {
	*proxy.Base

	hosts  *KnownHosts
	pool   *pool
	closed atomic.Bool
}

// New 组装 SSH 出口;不连网(首次拨号懒建)。fail-fast:终点与全部跳板的
// 私钥在此解析,坏 PEM 即报错;hostsDir 为空或不可创建同样报错——否则
// 这些问题会推迟到首条连接的握手/落盘阶段才暴露。
// poolSize 语义(registry 契约):0 → 默认 3;负数钳到 1;>16 钳到 16;
// 显式 1..16 原样。
func New(final NodeConfig, poolSize int, jumps []NodeConfig, hostsDir string) (*SSH, error) {
	if hostsDir == "" {
		return nil, errors.New("ssh: hostsDir 不能为空(known_hosts 需要落盘目录)")
	}
	hostsPath := filepath.Join(hostsDir, "known_hosts")
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o755); err != nil {
		return nil, fmt.Errorf("ssh: 创建 hosts 目录: %w", err)
	}
	hosts := NewKnownHosts(hostsPath)
	// fail-fast:全部节点(跳板+终点)的私钥先解析一遍,坏 PEM 不进运行期。
	nodes := make([]NodeConfig, 0, len(jumps)+1)
	nodes = append(nodes, jumps...)
	nodes = append(nodes, final)
	for _, n := range nodes {
		if _, err := authMethods(n); err != nil {
			return nil, fmt.Errorf("node %s: %w", n.hostPort(), err)
		}
	}
	if poolSize == 0 {
		poolSize = 3
	}
	if poolSize < 1 {
		poolSize = 1
	}
	if poolSize > 16 {
		poolSize = 16
	}

	d := newChainDialer(final, jumps, hosts)
	s := &SSH{
		Base:  &proxy.Base{Address: final.hostPort(), Protocol: proto.SSH},
		hosts: hosts,
	}
	s.pool = newPool(poolSize, func(int) func(ctx context.Context) (*cascadeClient, error) {
		return d.dialChain
	})
	return s, nil
}

// directTCPIPReq 是 RFC 4254 §7.2 的 direct-tcpip 通道打开负载。
type directTCPIPReq struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

// DialContext 从池中取一条链,向 metadata 目标开 direct-tcpip 通道。
// 通道打开失败时换链重试一次(死链场景由另一条/重建的链接住;MaxSessions
// 等业务性失败重试一次也廉价,最终错误透传);acquire 错误(拨号失败/全池
// 退避)不重试——重选同链无用,健康分发靠池的选链逻辑(Task 6 审查拍板)。
func (s *SSH) DialContext(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	if s.closed.Load() {
		return nil, errors.New("ssh: outbound already closed")
	}
	req := directTCPIPReq{
		Addr:       metadata.HostName,
		Port:       uint32(metadata.DstPort),
		OriginAddr: "127.0.0.1",
		OriginPort: 0,
	}
	payload := gossh.Marshal(req)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		cl, c, err := s.pool.acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("ssh acquire chain: %w", err)
		}
		ch, in, err := cl.OpenChannel("direct-tcpip", payload)
		if err == nil {
			go gossh.DiscardRequests(in)
			return newTrackedChannel(ch, c), nil
		}
		// 打开失败:先归还 acquire 记下的活跃数(没有 trackedChannel 替我们
		// 还),再收走死链——drop 仅在 client 已被标记死亡时动作,业务性
		// 失败不废链。
		c.releaseActive()
		c.drop()
		lastErr = err
	}
	return nil, fmt.Errorf("ssh open channel %s:%d: %w", metadata.HostName, metadata.DstPort, lastErr)
}

// channelAddr 是 direct-tcpip 通道的占位 net.Addr(SSH 通道无真实
// 本地/远端地址语义)。
type channelAddr struct{}

func (channelAddr) Network() string { return "ssh-channel" }
func (channelAddr) String() string  { return "ssh-channel" }

// trackedChannel 把 gossh.Channel 适配成 net.Conn:关闭时向所属链归还
// 活跃计数(与 acquire 内的 addActive 一进一出配对,不重复计数)。
// deadline 为 no-op:SSH 通道无 deadline 语义,返回 nil 让调用方的良性
// 设置不失败(与 sshtest.chanConn 同口径)。
type trackedChannel struct {
	gossh.Channel
	c *chain
}

func newTrackedChannel(ch gossh.Channel, c *chain) *trackedChannel {
	return &trackedChannel{Channel: ch, c: c}
}

func (t *trackedChannel) Close() error {
	err := t.Channel.Close()
	t.c.releaseActive()
	return err
}

func (t *trackedChannel) LocalAddr() net.Addr              { return channelAddr{} }
func (t *trackedChannel) RemoteAddr() net.Addr             { return channelAddr{} }
func (t *trackedChannel) SetDeadline(time.Time) error      { return nil }
func (t *trackedChannel) SetReadDeadline(time.Time) error  { return nil }
func (t *trackedChannel) SetWriteDeadline(time.Time) error { return nil }

// DialUDP:SSH 协议无 UDP 转发能力(ssh -D 也仅 TCP),恒不支持。
func (s *SSH) DialUDP(*M.Metadata) (net.PacketConn, error) {
	return nil, errors.ErrUnsupported
}

// Close 断开全部链(pool.close 级联关全部跳)并终结本实例:其后
// DialContext 直接报错,不再懒重建——被替换的旧实例不能在残余流量下
// 复活出无人回收的链。幂等,可安全重复调用。
func (s *SSH) Close() error {
	s.closed.Store(true)
	s.pool.close()
	return nil
}
