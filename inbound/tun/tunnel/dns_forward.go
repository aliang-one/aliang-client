// dns_forward 承载 TUN UDP/53 的 mesh DNS 代转发 A 侧(design §6 层1):
// 规则门控激活时,DNS 查询不再拨公网 UDP socket,而是每条查询独占一条
// mesh 连接(target=dns:)发送、由 B 侧代解析回包。门控判定(routing.
// MeshDNSForwardTarget)与 Peer 装配(SelfID/PikoDial)都在注入方(Task 9
// app/tunnel 组装),本包不摸全局配置——SetMeshDNSForwardPeerSource 是唯一
// 接线缝。帧格式与 B 侧(processor/tcp mesh_inbound.go 的 readDNSLengthFrame/
// writeDNSLengthFrame)对偶:2 字节大端长度前缀双向帧化。
package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"github.com/miekg/dns"
)

// dnsForwardExchangeTimeout 界定单次 DNS 交换(拨 mesh 连接+写查询+读响应)
// 的总窗口,与 B 侧 meshDNSResolveTimeout 同值(5s)。var 而非 const 是刻意
// 的测试缝(覆写+恢复,非并行安全),先例 meshHeaderReadTimeout。
var dnsForwardExchangeTimeout = 5 * time.Second

// dnsForwardPacketConn 实现 net.PacketConn:把 TUN 的 DNS 查询经 mesh 连接
// 单问单答地代转发(design §6 层1 线格式:每次查询独占一条 mesh 连接,
// [len][query] / [len][response],交换完成即关连接)。
//
// 会话语义(定案):
//   - WriteTo 触发整次同步交换(mesh 拨号+写查询+读响应),响应暂存后由
//     ReadFrom 交付;ReadFrom 返回的地址 = WriteTo 收到的目标地址(客户端
//     视角的 DNS 服务器,symmetricNAT 过滤按此对齐);
//   - 每会话串行交换(writeMu,一次一条在途查询)——真实 DNS 客户端单
//     socket 基本串行;并发多条在途查询的会话是已知限制(留档,不做并发
//     交换池);
//   - 交换失败/超时 → 就地合成 SERVFAIL 交付 ReadFrom(fail-closed:绝不
//     回落公网 DNS——本 conn 根本不持有公网 socket);WriteTo 本身恒成功
//     (会话不因单条查询失败而终止,后续查询可继续);
//   - QNAME 日志语义(logQuery seam)沿 dnsLoggingPacketConn 并入本 conn,
//     激活路径不再额外包 dnsLoggingPacketConn。
type dnsForwardPacketConn struct {
	peer     *meshproxy.Peer
	logQuery func(query []byte)

	// writeMu 串行化交换(一次一条在途查询)。
	writeMu sync.Mutex

	mu           sync.Mutex // 以下字段的全序锁
	remote       net.Addr   // ReadFrom 返回地址(= 客户端视角 DNS 服务器)
	resp         []byte     // 待交付响应(串行语义下最多一条,新交换覆盖旧未消费)
	readDeadline time.Time
	wake         chan struct{} // 响应就绪/deadline 变更的唤醒信号(close+renew)
	closeCh      chan struct{}
	closeOnce    sync.Once
}

var _ net.PacketConn = (*dnsForwardPacketConn)(nil)

// newDNSForwardPacketConn 构造 DNS 代转发 PacketConn。logQuery 在每次
// WriteTo 时收到查询原文(尽力解析 QNAME 记日志,原 dnsLoggingPacketConn
// 语义);nil = 不记。
func newDNSForwardPacketConn(peer *meshproxy.Peer, logQuery func(query []byte)) *dnsForwardPacketConn {
	if logQuery == nil {
		logQuery = func([]byte) {}
	}
	return &dnsForwardPacketConn{
		peer:     peer,
		logQuery: logQuery,
		wake:     make(chan struct{}),
		closeCh:  make(chan struct{}),
	}
}

// WriteTo 交付一条查询并同步完成整次 mesh 交换。addr 记为本会话的响应源
// (ReadFrom 返回值)。返回值恒为 len(b)/nil——交换失败折叠为 SERVFAIL
// 响应而不是错误,保住会话续用(见类型注释)。
func (c *dnsForwardPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closeCh:
		return 0, errors.New("mesh dns forward: closed")
	default:
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if addr != nil {
		c.mu.Lock()
		c.remote = addr
		c.mu.Unlock()
	}
	c.logQuery(b)

	resp, err := c.exchange(b)
	if err != nil {
		logger.Debug(fmt.Sprintf("[MESH-DNS] exchange via %s failed, synthesizing SERVFAIL: %v", c.peer.Addr(), err))
		resp = servfailForwardResponse(b)
	}
	if resp == nil {
		// 连 SERVFAIL 都合不成(<2 字节的畸形输入):不投放,等下一条查询。
		return len(b), nil
	}

	c.mu.Lock()
	c.resp = resp
	c.renewWakeLocked()
	c.mu.Unlock()
	return len(b), nil
}

// ReadFrom 交付暂存响应;无响应时阻塞至响应就绪、deadline 到期或关闭。
func (c *dnsForwardPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		if c.resp != nil {
			n := copy(p, c.resp)
			from := c.remote
			c.resp = nil
			c.mu.Unlock()
			return n, from, nil
		}
		dl := c.readDeadline
		wake := c.wake
		c.mu.Unlock()

		var timer *time.Timer
		var timeoutCh <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, dnsForwardTimeoutError{}
			}
			timer = time.NewTimer(d)
			timeoutCh = timer.C
		}

		select {
		case <-c.closeCh:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, errors.New("mesh dns forward: closed")
		case <-timeoutCh:
			return 0, nil, dnsForwardTimeoutError{}
		case <-wake:
			// 响应就绪或 deadline 被移动:重读状态。
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (c *dnsForwardPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return nil
}

func (c *dnsForwardPacketConn) LocalAddr() net.Addr { return &net.UDPAddr{} }

func (c *dnsForwardPacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *dnsForwardPacketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.renewWakeLocked() // 唤醒阻塞中的 ReadFrom 重评新 deadline
	c.mu.Unlock()
	return nil
}

func (c *dnsForwardPacketConn) SetWriteDeadline(time.Time) error { return nil }

// renewWakeLocked 唤醒阻塞中的 ReadFrom(调用方持 c.mu)。
func (c *dnsForwardPacketConn) renewWakeLocked() {
	close(c.wake)
	c.wake = make(chan struct{})
}

// exchange 打开 mesh 连接完成单问单答:target=dns:、class=custom(TUN DNS
// 一律 custom,design §2 行12——仅 LAN 载体,custom 类别由 mesh 出口的
// 载体准入在引擎层拒绝中继),写 [len][query]、读 [len][response] 后由
// defer 关连接(单问单答即关)。
func (c *dnsForwardPacketConn) exchange(query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsForwardExchangeTimeout)
	defer cancel()

	// HostName="dns:" 经 mesh 出口 targetFromMetadata 识别为 dns: 特殊目标
	// 原样透传(不拼端口)。
	md := &M.Metadata{
		Network:  M.UDP,
		HostName: meshproto.DNSTarget,
		DstPort:  53,
	}
	conn, err := c.peer.DialContext(meshproxy.WithClass(ctx, meshproto.ClassCustom), md)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := writeDNSLengthFrame(conn, query); err != nil {
		return nil, err
	}
	return readDNSLengthFrame(conn)
}

// dnsForwardTimeoutError 读超时(net.Error 语义:copyPacketData 对 Timeout
// 错误按会话空闲正常收尾,不记错误流)。
type dnsForwardTimeoutError struct{}

func (dnsForwardTimeoutError) Error() string   { return "mesh dns forward: i/o timeout" }
func (dnsForwardTimeoutError) Timeout() bool   { return true }
func (dnsForwardTimeoutError) Temporary() bool { return true }

// servfailForwardResponse 为查询合成 SERVFAIL 响应(fail-closed 兜底):
// 能拆包则 SetRcode 保留 Question 回显;拆不开则以查询头两字节为 ID 合成
// 裸 SERVFAIL 头;不足 2 字节返回 nil(无法构造任何可辨识响应)。
func servfailForwardResponse(query []byte) []byte {
	var req dns.Msg
	if err := req.Unpack(query); err == nil {
		resp := new(dns.Msg)
		resp.SetRcode(&req, dns.RcodeServerFailure)
		if out, err := resp.Pack(); err == nil {
			return out
		}
	}
	if len(query) < 2 {
		return nil
	}
	resp := new(dns.Msg)
	resp.Id = binary.BigEndian.Uint16(query[:2])
	resp.Response = true
	resp.Rcode = dns.RcodeServerFailure
	out, err := resp.Pack()
	if err != nil {
		return nil
	}
	return out
}

// readDNSLengthFrame 读一条 [2B 大端长度][载荷] 帧(dns: 目标专用线格式)。
// 与 processor/tcp mesh_inbound.go 的同名实现对偶——两侧各持本地实现,
// meshproto 不承载帧化属 Task 8 文件清单的既定取舍,线格式契约由两侧
// 测试互锁钉死。零长度帧按协议错误处理(空 DNS 报文非法)。
func readDNSLengthFrame(r io.Reader) ([]byte, error) {
	var frame [2]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(frame[:])
	if n == 0 {
		return nil, errors.New("mesh dns: empty frame")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// writeDNSLengthFrame 写一条 [2B 大端长度][载荷] 帧,与 readDNSLengthFrame
// 对偶(见其注释)。
func writeDNSLengthFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > math.MaxUint16 {
		return fmt.Errorf("mesh dns: frame payload size %d out of range", len(payload))
	}
	var frame [2]byte
	binary.BigEndian.PutUint16(frame[:], uint16(len(payload)))
	if _, err := w.Write(frame[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// SetMeshDNSForwardPeerSource 注入 DNS 代转发对端提供器(Task 8 A 侧接线
// 缝):返回 nil = 门未激活(UDP/53 走既有直连透传,fail-open)。生产由
// app/tunnel 组装(Task 9):routing.MeshDNSForwardTarget() 取规则驱动目标
// (10s memo),配注册态 SelfID 与 PikoDial 构 mesh.Peer(Relay:false——
// TUN DNS 一律 custom 仅 LAN 载体);本包不 import processor/routing、不摸
// 全局配置。nil 入参 = 清除(回到未接线语义)。并发安全(与 SetDialer 同款)。
func (t *Tunnel) SetMeshDNSForwardPeerSource(fn func() *meshproxy.Peer) {
	t.meshDNSMu.Lock()
	t.meshDNSForwardFn = fn
	t.meshDNSMu.Unlock()
}

// meshDNSForwardPeer 取当前 DNS 代转发对端;未接线/门未激活返回 nil。
func (t *Tunnel) meshDNSForwardPeer() *meshproxy.Peer {
	t.meshDNSMu.Lock()
	fn := t.meshDNSForwardFn
	t.meshDNSMu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}
