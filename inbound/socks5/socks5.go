// Package socks5 实现统一入口的 socks5 入站(spec §6.3):仅 CONNECT、仅
// no-auth,门禁=会话租约(与 HTTP CONNECT 完全同档)。UDP ASSOCIATE/BIND
// 显式 reply 0x07,不做半套。连接喂 processor/tcp 统一管线后本节点视作
// 出口——与 HTTP 路径同一条规则判断/转发逻辑,差别只在协议协商。
//
// 门禁顺序钉死:会话租约检查在成功应答之前——reply 对一条连接只能写一次,
// 先写成功再查租约会逼出「重写 reply」的歧义(RFC1928 无重答语义)。
package socks5

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/tcp"
)

// TCPHandleFunc 与 TCPConnHandler.Handle 同签名(含 ctx),测试注入缝。
type TCPHandleFunc func(ctx context.Context, conn net.Conn, md *M.Metadata) error

// TCPHandler 测试覆写用;nil=生产路径,调用期惰性走 tcp.GetHandler().Handle
// (不在包 init 期触发 tcp 单例装配——import 期装配会把测试拖进全量初始化)。
var TCPHandler TCPHandleFunc

func dialHandler() TCPHandleFunc {
	if TCPHandler != nil {
		return TCPHandler
	}
	return func(ctx context.Context, conn net.Conn, md *M.Metadata) error {
		return tcp.GetHandler().Handle(ctx, conn, md)
	}
}

// RFC1928 常量(本包只消费这一小角)。
const (
	socksVer      = 0x05
	authNone      = 0x00
	authNoAccept  = 0xFF // no acceptable methods
	cmdConnect    = 0x01
	repSuccess    = 0x00
	repNotAllowed = 0x02 // connection not allowed by ruleset(无会话租约)
	repCmdUnsupp  = 0x07 // command not supported

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04
)

// socksNegotiateTimeout 约束协商段(方法+请求)整段读取:慢速/挂起客户端
// 不允许无限占用连接;管线移交前清除,不污染后续隧道流量(SSH 入口
// handshakeTimeout 同款先设后清先例)。
const socksNegotiateTimeout = 10 * time.Second

// socksConnCounter ConnID 序号源:独立 "socks-N" 段,与 mesh-N/tun- 并列,
// 便于日志溯源入站协议族。
var socksConnCounter uint64

// Handler 处理一条嗅探为 socks 的连接(conn 已含嗅探回放缓存)。阻塞至
// 连接结束;关闭归本函数(defer),管线移交后 conn 所有权随 Metadata 交
// 处理器——双关幂等(与 inbound/http 同模式)。
func Handler(conn net.Conn) {
	defer conn.Close()
	if err := serve(conn); err != nil {
		logger.Debug(fmt.Sprintf("[SOCKS5] %s: %v", conn.RemoteAddr(), err))
	}
}

// serve 承载单条连接的协商+移交。错误只记 Debug:socks 客户端断连是常态
// (超时/拒收/畸形),不构成服务面告警。
func serve(conn net.Conn) error {
	// 协商段统一受 deadline 约束;移交管线前清除。
	_ = conn.SetDeadline(time.Now().Add(socksNegotiateTimeout))

	// 1) 方法协商:VER NMETHODS METHODS…;不含 no-auth → 0xFF 断连。
	method, err := negotiateMethod(conn)
	if err != nil {
		return fmt.Errorf("method negotiation: %w", err)
	}
	if err := writeMethodReply(conn, method); err != nil {
		return fmt.Errorf("method reply: %w", err)
	}
	if method != authNone {
		return fmt.Errorf("client offers no no-auth method (rep 0xFF)")
	}

	// 2) 请求:VER CMD RSV ATYP ADDR PORT。
	req, err := readRequest(conn)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	if req.cmd != cmdConnect {
		_ = writeReply(conn, repCmdUnsupp)
		return fmt.Errorf("command 0x%02X unsupported (rep 0x07)", req.cmd)
	}
	md, err := metadataFromRequest(conn, req)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}

	// 3) 会话租约(成功应答之前):无活跃会话 → rep 0x02 断连。
	lease, err := auth.GetSessionAuthority().AcquireProxyLease(func() { _ = conn.Close() })
	if err != nil {
		_ = writeReply(conn, repNotAllowed)
		return fmt.Errorf("proxy lease denied (rep 0x02)")
	}
	defer lease.Release()

	// 4) 成功应答(BND=0.0.0.0:0,统一口不代答本机地址)。
	if err := writeReply(conn, repSuccess); err != nil {
		return fmt.Errorf("connect reply: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	logger.Debug(fmt.Sprintf("[SOCKS5] CONNECT %s:%d (conn=%s)", targetString(md), md.DstPort, md.ConnID))
	// 5) 喂统一管线:本节点自此视作出口,规则判断/转发全部归 tcp 管线。
	return dialHandler()(context.Background(), conn, md)
}

// negotiateMethod 读方法协商段并返回选定方法:含 no-auth → authNone;
// 不含 → authNoAccept(调用方回 0xFF)。VER 非 5 视为协商畸形直接报错
// (嗅探层已确认首字节 0x05,此处异常只能是流被截断/伪造)。
func negotiateMethod(conn net.Conn) (byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, err
	}
	if head[0] != socksVer {
		return 0, fmt.Errorf("ver 0x%02X != 0x05", head[0])
	}
	nmethods := int(head[1])
	if nmethods == 0 {
		return authNoAccept, nil // 零方法=无可选,等价于不含 no-auth
	}
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return 0, err
	}
	for _, m := range methods {
		if m == authNone {
			return authNone, nil
		}
	}
	return authNoAccept, nil
}

// socksRequest 是解析后的请求段(ATYP 决定 addr 语义)。
type socksRequest struct {
	cmd   byte
	atyp  byte
	host  string // domain 形态的目标主机
	ip    netip.Addr
	port  uint16
	valid bool // ip 是否有效(ATYP=domain 时为 false)
}

// readRequest 读请求段:VER CMD RSV ATYP ADDR PORT,ADDR 长度随 ATYP
// (IPv4 4B / domain 1B 长度前缀 / IPv6 16B)。畸形(长度截断/未知 ATYP)
// 返回错误,调用方直接断连——畸形请求无 reply 语义可表(RFC1928 未定义),
// 静默断连与既有透明代理读失败路径同口径。
func readRequest(conn net.Conn) (*socksRequest, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, err
	}
	if head[0] != socksVer {
		return nil, fmt.Errorf("request ver 0x%02X != 0x05", head[0])
	}
	req := &socksRequest{cmd: head[1], atyp: head[3]}
	switch req.atyp {
	case atypIPv4:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return nil, err
		}
		ip, ok := netip.AddrFromSlice(addr)
		if !ok {
			return nil, fmt.Errorf("bad ipv4 addr")
		}
		req.ip, req.valid = ip.Unmap(), true
	case atypDomain:
		lenByte := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenByte); err != nil {
			return nil, err
		}
		domain := make([]byte, int(lenByte[0]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return nil, err
		}
		req.host = string(domain)
		if req.host == "" {
			return nil, fmt.Errorf("empty domain")
		}
	case atypIPv6:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return nil, err
		}
		ip, ok := netip.AddrFromSlice(addr)
		if !ok {
			return nil, fmt.Errorf("bad ipv6 addr")
		}
		req.ip, req.valid = ip, true
	default:
		return nil, fmt.Errorf("unknown atyp 0x%02X", req.atyp)
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return nil, err
	}
	req.port = binary.BigEndian.Uint16(portBytes)
	return req, nil
}

// metadataFromRequest 构注入 Metadata:IP→DstIP;域名→HostName 直转(不
// SetHostName——BindingSource 枚举无 socks 来源,伪源会污染 DNS 绑定缓存,
// 与 mesh 路径同款纪律);源地址从 conn.RemoteAddr 尽力提取。ConnID 走
// 独立 "socks-N" 序号段。
func metadataFromRequest(conn net.Conn, req *socksRequest) (*M.Metadata, error) {
	md := &M.Metadata{
		Network: M.TCP,
		ConnID:  fmt.Sprintf("socks-%d", atomic.AddUint64(&socksConnCounter, 1)),
		DstPort: req.port,
	}
	if req.valid {
		md.DstIP = req.ip
	} else {
		md.HostName = req.host
	}
	if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if ip, err := netip.ParseAddr(remoteAddr.IP.String()); err == nil {
			md.SrcIP = ip
		}
		md.SrcPort = uint16(remoteAddr.Port)
	}
	return md, nil
}

// writeMethodReply 写方法选择应答:RFC1928 §3 固定 2 字节(VER+METHOD)。
// 与请求级应答(writeReply,10 字节)是两种报文,长度不可混用——协商死锁
// 教训:服务端多写的字节会让同步管道下的客户端停在半读状态。
func writeMethodReply(conn net.Conn, method byte) error {
	_, err := conn.Write([]byte{socksVer, method})
	return err
}

// writeReply 写 10 字节请求级应答(BND 恒 0.0.0.0:0):本入口不代答监听
// 地址,客户端不消费 BND(CONNECT 语义)。
func writeReply(conn net.Conn, rep byte) error {
	reply := []byte{socksVer, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(reply)
	return err
}

// targetString 日志用目标描述(IP 或域名)。
func targetString(md *M.Metadata) string {
	if md.HostName != "" {
		return md.HostName
	}
	return strconv.Quote(md.DstIP.String())
}
