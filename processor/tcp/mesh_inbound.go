// mesh_inbound 实现 B 侧 mesh 接收语义(design §3.3):收到转发流后,把去帧头
// 后的原始流当作 B 自己收到的一条新连接注入入站管线(与 TUN/HTTP 入站同一
// 处理链),继承 B 全部规则/customer.proxy/乃至 B 自己的 mesh 规则——多跳
// (A→B→C)由此免费获得。帧协议见 outbound/meshproto。
package tcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
)

// RouteMesh 是 mesh 注入连接的 Metadata.Route 初始值(design §3.3 溯源标记)。
// B 侧自己的规则引擎照常决定真实路由(处理中会覆写为 RouteToALiang/
// RouteToSocks/RouteDirect 等),此标记仅让注入流量可识别、可审计。
const RouteMesh = "RouteMesh"

// meshHeaderReadTimeout 界定帧头首行读取:防慢速/静默连接长期占住 goroutine
// (参考 app/tunnel/bridge.go handle 的 15s 先例)。var 而非 const 是刻意的
// 测试缝:测试覆写为短 deadline 断言超时拒绝路径(覆写+恢复,非并行安全)。
var meshHeaderReadTimeout = 15 * time.Second

// meshRejectWriteTimeout 界定拒绝回写的写入窗口:对端已失联时不因回写卡死。
const meshRejectWriteTimeout = 5 * time.Second

var meshInboundConnCounter uint64

// MeshInboundDeps 聚合 mesh 入站判定所需的全部外部依赖:测试注 fake,生产由
// app/mesh/relay 组装(ProductionDeps)——本包不硬编码全局单例。
type MeshInboundDeps struct {
	// SelfID 本机 device_id。为空 = 未注册首启,未注册设备不参与 mesh,
	// 一律拒收(M6 边界,计划风险区第 0 条:选「拒绝接收」,最简且安全)。
	SelfID string
	// MaxHops 跳数上限(customer.mesh.EffectiveMaxHops());<=0 按默认兜底。
	MaxHops int
	// ForwardEnabled 允许被其他节点转发(customer.mesh.EffectiveForwardEnabled(),
	// Mesh==nil → false:被转发是让渡型能力,默认关)。
	ForwardEnabled bool
	// PeerAllowed 转发方 ACL:from ∈ 本机 Mesh.Peers(对称组网假设,design
	// §8)。nil = 未配置 → 全拒(deny-by-default;门禁关时本就到不了这里)。
	PeerAllowed func(deviceID string) bool
	// Handler 注入 seam:去头后的 conn + 构造好的 Metadata 喂入。nil 视为
	// 组装错误,直接拒绝(不隐式回落全局单例——单例接线由组装层显式完成)。
	Handler TCPConnHandler
}

// HandleMeshConn 处理一条 mesh 转发连接(B 侧,design §3.3):
//
//	读帧头(meshproto.Parse) → AppendHop(selfID, maxHops) 防环
//	→ 门禁:ForwardEnabled + from ∈ Peers(对称组网 ACL)
//	→ 类别×载体准入:中继载体收 custom → 拒
//	→ 构 M.Metadata{HostName/DstIP/DstPort, Route:"RouteMesh"} → 注入 processor/tcp 既有入口
//
// 拒绝语义(design §3.3 定死):所有拒绝(防环/门禁/类别/解析失败)以一行
// 文本错误回写对端后关闭连接——错误回源,中间跳不执行任何兜底/失败策略
// (那是链头的事,Task 7)。
//
// 所有权:拒绝路径由本函数关连接;注入成功后连接移交 deps.Handler(按
// TCPConnHandler 契约由 handler 负责关闭,同 TUN/HTTP 入站)。
func HandleMeshConn(conn net.Conn, carrier string, deps MeshInboundDeps) error {
	reject := func(reason string, args ...any) error {
		err := fmt.Errorf("mesh inbound: "+reason, args...)
		writeMeshRejectLine(conn, err)
		_ = conn.Close()
		return err
	}

	if deps.Handler == nil {
		logger.Debug("[MESH-IN] reject: no inbound handler configured")
		return reject("no inbound handler configured")
	}

	_ = conn.SetReadDeadline(time.Now().Add(meshHeaderReadTimeout))
	reader := bufio.NewReaderSize(conn, meshproto.MaxHeaderBytes)
	line, readErr := reader.ReadSlice('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject: header read: %v", readErr))
		return reject("read header: %v", readErr)
	}
	hdr, _, parseErr := meshproto.Parse(line)
	if parseErr != nil {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject: bad header: %v", parseErr))
		return reject("bad header: %v", parseErr)
	}
	_ = conn.SetReadDeadline(time.Time{})

	// M6 边界(计划风险区第 0 条):selfID 为空(未注册首启)→ 未注册设备
	// 不参与 mesh,拒收。放在帧头读取之后:先消费对端已写字节再关闭,对端
	// 才能稳定读到拒绝行(FIN 而非 RST)。
	if deps.SelfID == "" {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: self device unregistered", hdr.From))
		return reject("self device unregistered (device must register before joining mesh)")
	}

	// 防环 + 跳数上限(design §7 层1/层2):AppendHop 自查 Via 含本机 ID 即
	// 环路,超上限即拒。成功的 Append(追加本机 ID、递增跳数)后的帧头挂入
	// 注入 ctx(Via/Hops 续传,Task 7):B 侧规则再决策 toNode 走 mesh 出口时,
	// 出口(mesh.Peer.DialContext 按 InheritedHeaderFromContext)用该帧头
	// 原样上线——多跳 A→B→C 的链路向量由此续传,出口不重置不追加。
	maxHops := deps.MaxHops
	if maxHops <= 0 {
		maxHops = meshproto.DefaultHops
	}
	if err := hdr.AppendHop(deps.SelfID, maxHops); err != nil {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: %v", hdr.From, err))
		return reject("%v", err)
	}

	// 门禁:允许被转发 + 转发方 ACL(对称组网:from 必须在本机 Mesh.Peers)。
	if !deps.ForwardEnabled {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: forward disabled", hdr.From))
		return reject("forward disabled (customer.mesh.forward_enable)")
	}
	if deps.PeerAllowed == nil || !deps.PeerAllowed(hdr.From) {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: not an allowed forwarder", hdr.From))
		return reject("forwarder %q not allowed", hdr.From)
	}

	// 类别×载体准入(design §4):用户自定义流量禁走中继(引擎层硬约束)。
	if !meshproto.ClassCarrierAllowed(hdr.Class, carrier) {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: class=%s on carrier=%s", hdr.From, hdr.Class, carrier))
		return reject("class %q not allowed on carrier %q", hdr.Class, carrier)
	}

	// dns: 目标分流点(design §6 层1):Task 8 落地代解析;当前为拒绝桩。
	if meshproto.IsDNSTarget(hdr.Target) {
		return handleMeshDNSTarget(conn, hdr, deps)
	}

	md, err := meshMetadataFromTarget(hdr.Target, conn)
	if err != nil {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: %v", hdr.From, err))
		return reject("bad target %q: %v", hdr.Target, err)
	}

	// 客户端头行后紧跟的载荷可能已被 bufio 预读(头+载荷一次写出),必须经
	// WrappedConn 回放,注入管线才不丢首段字节(bridge.go readHeader 同款坑)。
	injected := net.Conn(conn)
	if buffered := reader.Buffered(); buffered > 0 {
		leftover, _ := reader.Peek(buffered)
		injected = &WrappedConn{Conn: conn, Buf: append([]byte(nil), leftover...)}
	}

	targetDesc := md.HostName
	if targetDesc == "" {
		targetDesc = md.DstIP.String()
	}
	logger.Info(fmt.Sprintf("[MESH-IN] conn_id=%s injecting from=%s class=%s carrier=%s target=%s:%d via=%v",
		md.ConnID, hdr.From, hdr.Class, carrier, targetDesc, md.DstPort, hdr.Via))

	// 所有权移交 handler(契约同 TUN/HTTP 入站);返回值透传给 relay 记录。
	// ctx 携带 AppendHop 后帧头(Via/Hops 续传,见上方 AppendHop 注释)。
	return deps.Handler.Handle(meshproxy.WithInheritedHeader(context.Background(), hdr), injected, md)
}

// handleMeshDNSTarget 处理 dns: 特殊目标(design §6 层1:每次查询独占一条
// mesh 连接,单问单答)。分流点:Task 8 在此落地 miekg/dns 拆查询→B 本机
// 解析→回包;当前阶段一律拒绝并回写桩信息。
func handleMeshDNSTarget(conn net.Conn, hdr *meshproto.Header, deps MeshInboundDeps) error {
	_ = hdr
	_ = deps
	logger.Debug("[MESH-IN] dns target rejected: not implemented yet (Task 8)")
	err := errors.New("mesh inbound: dns forward not implemented")
	writeMeshRejectLine(conn, err)
	_ = conn.Close()
	return err
}

// writeMeshRejectLine 以一行文本回写错误(design §3.3 错误回源),随后由
// 调用方关连接。错误文本压成单行,防对端把多行当多次拒绝。
func writeMeshRejectLine(conn net.Conn, err error) {
	msg := strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
	_ = conn.SetWriteDeadline(time.Now().Add(meshRejectWriteTimeout))
	_, _ = conn.Write([]byte(msg + "\n"))
}

// meshMetadataFromTarget 按帧头 Target 构造注入 Metadata(design §3.2/§6 层2):
// 域名→HostName(域名直转语义,B 侧免解析,规则按域名命中、拨号期再解析);
// IP→DstIP;裸域名/裸 IP 无端口按 443 兜底(meshproto.SplitTarget 契约)。
// Route 置 RouteMesh 溯源;ConnID 走独立序号段(mesh-N),与 tun-/tcp- 段并列。
func meshMetadataFromTarget(target string, conn net.Conn) (*M.Metadata, error) {
	md := &M.Metadata{
		Network: M.TCP,
		ConnID:  fmt.Sprintf("mesh-%d", atomic.AddUint64(&meshInboundConnCounter, 1)),
		Route:   RouteMesh,
	}
	// 源地址尽力而为:LAN 载体是 TCPAddr;中继载体(piko 流)可能不是。
	// conn 为 nil 时跳过(单元测试直测 target 拆解形态)。
	if conn != nil {
		if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
			if addr, err := netip.ParseAddr(remoteAddr.IP.String()); err == nil {
				md.SrcIP = addr
			}
			md.SrcPort = uint16(remoteAddr.Port)
		}
	}

	host, portStr, isDomain, err := meshproto.SplitTarget(target)
	if err != nil {
		return nil, err
	}
	if portStr == "" {
		portStr = "443"
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid port %q", portStr)
	}
	md.DstPort = uint16(port)

	if isDomain {
		// 直接赋值而非 SetHostName:现有 BindingSource 枚举(sni/http/dns/
		// connect)没有 mesh 来源,伪源会污染 DNS 绑定缓存(StoreBinding 按
		// BindingSource 写缓存);DNSInfo 留 nil 与「未经本机解析」的事实一致。
		md.HostName = host
		return md, nil
	}
	ip, parseErr := netip.ParseAddr(host)
	if parseErr != nil {
		// 理论不可达(SplitTarget 只对 IP 字面量返回 isDomain=false),防御
		// 手改协议字段的畸形帧。
		return nil, fmt.Errorf("host %q is neither domain nor IP", host)
	}
	md.DstIP = ip
	return md, nil
}
