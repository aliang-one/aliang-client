// mesh_inbound 实现 B 侧 mesh 接收语义(design §3.3):收到转发流后,把去帧头
// 后的原始流当作 B 自己收到的一条新连接注入入站管线(与 TUN/HTTP 入站同一
// 处理链),继承 B 全部规则/customer.proxy/乃至 B 自己的 mesh 规则——多跳
// (A→B→C)由此免费获得。帧协议见 outbound/meshproto。
package tcp

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
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
	"github.com/miekg/dns"
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
	// DNSResolver dns: 目标代解析所用的本地解析器(design §6 层1)。nil =
	// 系统解析器(&net.Resolver{},跨平台取舍见 LocalDNSResolver 注释)。
	// 测试注 fake,不发真查询。
	DNSResolver LocalDNSResolver
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
// (兜底/失败策略仅由链头就地执行,见 handler.go dialViaMesh;本侧注入连接
// 的中间跳拨出失败同样错误回源,不落策略)。
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

	// 客户端头行后紧跟的载荷可能已被 bufio 预读(头+载荷一次写出),必须经
	// WrappedConn 回放,注入管线才不丢首段字节(bridge.go readHeader 同款坑)。
	// dns: 目标同理:查询帧常与头行一次写出。
	injected := net.Conn(conn)
	if buffered := reader.Buffered(); buffered > 0 {
		leftover, _ := reader.Peek(buffered)
		injected = &WrappedConn{Conn: conn, Buf: append([]byte(nil), leftover...)}
	}

	// dns: 目标分流点(design §6 层1):代解析单问单答,不进注入管线。
	if meshproto.IsDNSTarget(hdr.Target) {
		return handleMeshDNSTarget(injected, hdr, deps)
	}

	md, err := meshMetadataFromTarget(hdr.Target, conn)
	if err != nil {
		logger.Debug(fmt.Sprintf("[MESH-IN] reject from=%s: %v", hdr.From, err))
		return reject("bad target %q: %v", hdr.Target, err)
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

// meshDNSResolveTimeout 界定 B 侧 dns: 代解析交换总窗口(查询帧读取+系统
// 解析+响应回写;A 侧同款 5s,dnsForwardExchangeTimeout)。var 而非 const 是
// 刻意的测试缝(覆写+恢复,非并行安全),先例 meshHeaderReadTimeout。
var meshDNSResolveTimeout = 5 * time.Second

// meshDNSAnswerTTL 是 B 侧合成响应的固定 TTL(design §6 层1 定案:30s,
// 短 TTL 让 A 侧客户端缓存快速过期,权威性由 B 侧系统解析器保证)。
const meshDNSAnswerTTL = 30

// LocalDNSResolver 抽象 B 侧 dns: 代解析所需的最小解析面(*net.Resolver 原生
// 满足,测试注 fake 不发真查询)。
//
// 取舍(design §6 层1 定案):走系统解析器(net.Resolver)而非探测系统上游
// DNS 地址再自办递归——Windows 无 stdlib 上游地址 API,net.Resolver 走系统
// 解析器全平台可用;代价是 B 侧解析结果不经过 B 的代理规则(与 TUN 本机
// DNS 走系统解析器同口径)。
type LocalDNSResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupSRV(ctx context.Context, service, proto, name string) (cname string, addrs []*net.SRV, err error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// handleMeshDNSTarget 处理 dns: 特殊目标(design §6 层1:每次查询独占一条
// mesh 连接,单问单答即关):读 [len][query] → miekg/dns 拆查询 → 系统解析器
// 按类型解析 → miekg 合成响应(TTL 固定 30s)→ 写 [len][response] → 关连接。
// 解析失败/超时/不支持类型 → SERVFAIL 回包(仍是合法 DNS 响应,错误不回源
// ——dns: 的错误语义由协议自身承载,不走 mesh 文本拒绝行)。帧格式与 A 侧
// dnsForwardPacketConn 对偶:2 字节大端长度前缀双向帧化。
func handleMeshDNSTarget(conn net.Conn, hdr *meshproto.Header, deps MeshInboundDeps) error {
	resolver := deps.DNSResolver
	if resolver == nil {
		resolver = &net.Resolver{}
	}

	_ = conn.SetDeadline(time.Now().Add(meshDNSResolveTimeout))
	query, err := readDNSLengthFrame(conn)
	if err != nil {
		logger.Debug(fmt.Sprintf("[MESH-IN] dns from=%s: read frame: %v", hdr.From, err))
		_ = conn.Close()
		return fmt.Errorf("mesh inbound: dns read frame: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), meshDNSResolveTimeout)
	defer cancel()
	response := resolveDNSQuery(ctx, resolver, query)

	if err := writeDNSLengthFrame(conn, response); err != nil {
		logger.Debug(fmt.Sprintf("[MESH-IN] dns from=%s: write frame: %v", hdr.From, err))
		_ = conn.Close()
		return fmt.Errorf("mesh inbound: dns write frame: %w", err)
	}
	_ = conn.Close()
	return nil
}

// readDNSLengthFrame 读一条 [2B 大端长度][载荷] 帧(dns: 目标专用线格式,
// design §6 层1)。零长度帧按协议错误处理(空 DNS 报文非法)。
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
// 对偶。
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

// resolveDNSQuery 把查询报文解析为响应报文(永不返回 nil:任何失败都折叠为
// SERVFAIL 响应——查询 ID 尽力回显,拆不开时以头两字节为 ID 合成裸头)。
func resolveDNSQuery(ctx context.Context, resolver LocalDNSResolver, query []byte) []byte {
	var req dns.Msg
	if err := req.Unpack(query); err != nil || len(req.Question) == 0 {
		return servfailDNSResponse(query)
	}
	resp := new(dns.Msg)
	resp.SetReply(&req)
	resp.RecursionAvailable = true

	answers, resolveErr := resolveDNSQuestion(ctx, resolver, req.Question[0])
	if resolveErr != nil {
		resp.SetRcode(&req, dnsRcodeForResolveError(resolveErr))
	} else {
		resp.Answer = answers
	}
	out, err := resp.Pack()
	if err != nil {
		return servfailDNSResponse(query)
	}
	return out
}

// dnsRcodeForResolveError 区分负应答与暂态错误:net.DNSError.IsNotFound
// (上游 NXDOMAIN/无此主机)是有效负应答,回 RcodeNameError 让客户端负缓存
// 不重试;其余错误 → SERVFAIL(暂态错误,客户端可重试)。离线冲浪的广告
// 域名是 NXDOMAIN 重灾区,折叠成 SERVFAIL 会引发重试风暴(质量评审精化)。
func dnsRcodeForResolveError(err error) int {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return dns.RcodeNameError
	}
	return dns.RcodeServerFailure
}

// resolveDNSQuestion 按查询类型分发(design §6 层1 定案的类型矩阵):
// A/AAAA→LookupIP;PTR→LookupAddr(查询名反算点分地址);MX/SRV/TXT→对应
// Lookup;其余类型一律报错(上层折叠为 SERVFAIL)。多 Question 查询按首个
// 处理(真实 DNS 客户端恒单问)。
func resolveDNSQuestion(ctx context.Context, resolver LocalDNSResolver, q dns.Question) ([]dns.RR, error) {
	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA:
		network := "ip4"
		qtype := dns.TypeA
		if q.Qtype == dns.TypeAAAA {
			network, qtype = "ip6", dns.TypeAAAA
		}
		ips, err := resolver.LookupIP(ctx, network, q.Name)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(ips))
		for _, ip := range ips {
			if q.Qtype == dns.TypeA && ip.To4() == nil {
				continue
			}
			if q.Qtype == dns.TypeAAAA && (ip.To4() != nil || ip.To16() == nil) {
				continue
			}
			if q.Qtype == dns.TypeA {
				answers = append(answers, &dns.A{Hdr: dnsRRHeader(q, qtype), A: ip.To4()})
			} else {
				answers = append(answers, &dns.AAAA{Hdr: dnsRRHeader(q, qtype), AAAA: ip.To16()})
			}
		}
		return answers, nil
	case dns.TypePTR:
		addr, ok := dnsPTRNameToAddr(q.Name)
		if !ok {
			return nil, fmt.Errorf("mesh dns: unparseable PTR name %q", q.Name)
		}
		names, err := resolver.LookupAddr(ctx, addr)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(names))
		for _, name := range names {
			answers = append(answers, &dns.PTR{Hdr: dnsRRHeader(q, dns.TypePTR), Ptr: dns.Fqdn(name)})
		}
		return answers, nil
	case dns.TypeMX:
		mxs, err := resolver.LookupMX(ctx, q.Name)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(mxs))
		for _, mx := range mxs {
			answers = append(answers, &dns.MX{
				Hdr:        dnsRRHeader(q, dns.TypeMX),
				Preference: mx.Pref,
				Mx:         dns.Fqdn(mx.Host),
			})
		}
		return answers, nil
	case dns.TypeSRV:
		service, proto, target := splitSRVName(q.Name)
		_, addrs, err := resolver.LookupSRV(ctx, service, proto, target)
		if err != nil {
			return nil, err
		}
		answers := make([]dns.RR, 0, len(addrs))
		for _, srv := range addrs {
			answers = append(answers, &dns.SRV{
				Hdr:      dnsRRHeader(q, dns.TypeSRV),
				Priority: srv.Priority,
				Weight:   srv.Weight,
				Port:     srv.Port,
				Target:   dns.Fqdn(srv.Target),
			})
		}
		return answers, nil
	case dns.TypeTXT:
		txts, err := resolver.LookupTXT(ctx, q.Name)
		if err != nil {
			return nil, err
		}
		return []dns.RR{&dns.TXT{Hdr: dnsRRHeader(q, dns.TypeTXT), Txt: txts}}, nil
	default:
		return nil, fmt.Errorf("mesh dns: unsupported qtype %d(%s)", q.Qtype, dns.TypeToString[q.Qtype])
	}
}

// dnsRRHeader 构造答案 RR 头:名称回显查询名、TTL 固定 meshDNSAnswerTTL。
func dnsRRHeader(q dns.Question, qtype uint16) dns.RR_Header {
	return dns.RR_Header{Name: q.Name, Rrtype: qtype, Class: dns.ClassINET, Ttl: meshDNSAnswerTTL}
}

// dnsPTRNameToAddr 把 PTR 查询名还原为点分文本地址(net.Resolver.LookupAddr
// 的入参形态):v4 反转 in-addr.arpa 四段;v6 反转 ip6.arpa 32 个 nibble 并
// 按 4 位分组拼冒号十六进制。
func dnsPTRNameToAddr(name string) (string, bool) {
	labels := dns.SplitDomainName(name)
	if len(labels) < 3 {
		return "", false
	}
	switch suffix := labels[len(labels)-2:]; {
	case suffix[0] == "in-addr" && suffix[1] == "arpa":
		n := len(labels) - 2
		if n != 4 {
			return "", false
		}
		return labels[3] + "." + labels[2] + "." + labels[1] + "." + labels[0], true
	case suffix[0] == "ip6" && suffix[1] == "arpa":
		nibbles := labels[:len(labels)-2]
		if len(nibbles) != 32 {
			return "", false
		}
		var b strings.Builder
		for i := 31; i >= 0; i-- {
			b.WriteString(nibbles[i])
			if i > 0 && i%4 == 0 {
				b.WriteByte(':')
			}
		}
		return b.String(), true
	default:
		return "", false
	}
}

// splitSRVName 拆 _service._proto.name 形态的 SRV 查询名为 LookupSRV 三元
// 组;不带下划线前缀的形态按整名直查(service/proto 传空,stdlib 直查语义)。
func splitSRVName(name string) (service, proto, target string) {
	labels := dns.SplitDomainName(name)
	if len(labels) >= 3 && strings.HasPrefix(labels[0], "_") && strings.HasPrefix(labels[1], "_") {
		return strings.TrimPrefix(labels[0], "_"), strings.TrimPrefix(labels[1], "_"), strings.Join(labels[2:], ".")
	}
	return "", "", strings.TrimSuffix(name, ".")
}

// servfailDNSResponse 为 query 合成 SERVFAIL 响应报文:能拆包则以 SetRcode
// 保留 Question 回显;拆不开(at least 头两字节可用)以查询头两字节为 ID
// 合成裸 SERVFAIL 头;连 2 字节都没有则返回裸空响应(上层仍会写出帧,
// 极端畸形输入的兜底)。
func servfailDNSResponse(query []byte) []byte {
	var req dns.Msg
	if err := req.Unpack(query); err == nil {
		resp := new(dns.Msg)
		resp.SetRcode(&req, dns.RcodeServerFailure)
		if out, err := resp.Pack(); err == nil {
			return out
		}
	}
	resp := new(dns.Msg)
	resp.Response = true
	resp.Rcode = dns.RcodeServerFailure
	if len(query) >= 2 {
		resp.Id = binary.BigEndian.Uint16(query[:2])
	}
	out, err := resp.Pack()
	if err != nil {
		return make([]byte, 12) // 12 字节 DNS 头裸 SERVFAIL(Id=0),不再向下失败
	}
	return out
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
