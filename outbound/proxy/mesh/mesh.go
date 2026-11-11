// Package mesh 实现到对端节点的 mesh 出口(design §3.4/§5):LAN 直连载体
// 优先、piko 中继载体兜底(仅自身/AI 类别准入)、首行 JSON 帧头写入、全部
// 失败错误回源不吞不回落。帧协议见 outbound/meshproto;B 侧接收与中间跳
// 转发属后续任务(Task 6/8)。
package mesh

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

// PikoDialFunc 拨 piko 端点(中继载体 A 侧,design §3.1 载体 A:每条拨号即
// 一个桥接流,yamux 由 piko 持有,A 侧零 mux 代码)。签名与 piko client
// v0.10.0 的 (*client.Dialer).Dial 完全一致(spike 2026-11-08 实测:进程内
// server + Upstream.Listen/Dialer.Dial 回环端到端双向字节通):
//
//	dialer := &pikoclient.Dialer{URL: proxyURL, Token: token} // proxy 端口
//	peer.PikoDial = dialer.Dial                               // 方法值直接赋
//
// 注入式接缝:测试注入 fake;生产已由 app/tunnel 接线(Task 9:configure 帧
// 可选键 piko_proxy_url → Manager.DialPeerMesh/dialMeshEndpoint,每次拨号读
// manager 当前配置)。本包不 import piko,保持数据面拨号路径依赖最小。
type PikoDialFunc func(ctx context.Context, endpointID string) (net.Conn, error)

// meshRelayEndpointSuffix 中继端点后缀:A 拨 <B_deviceID>.mesh(design
// §3.1 载体 A;B 侧以第二端点模式常驻 serve,见 app/tunnel manager.go)。
const meshRelayEndpointSuffix = ".mesh"

// PeerTarget 描述一个 mesh 对端,由转发层按 toNode:<deviceID> 决策后构造
// (Task 9 从 customer.mesh.peers 装配:LANAddrs 取 lan_addrs,PikoDial 由
// app/tunnel 统一封装)。
type PeerTarget struct {
	DeviceID string       // 对端 device_id;中继端点名 <DeviceID>.mesh
	LANAddrs []string     // LAN 直连地址 host:port(beacon 发现/手输兜底),顺序尝试
	Relay    bool         // 允许中继载体(平台端点白名单放行时置位;custom 类别仍被引擎层拒绝)
	PikoDial PikoDialFunc // 中继载体拨号(注入式)
	MaxHops  int          // 跳数上限;出口不消费(入站 AppendHop 与链头帧头各按消费侧语义取用),构造期归一暂存
	SelfID   string       // 本机 device_id,帧头 From/Via 链头;ctx 携带继承帧头时(中间跳续传)不被使用
}

// Peer 到单一对端的 mesh 出口,实现 proxy.Proxy(design §3.4 outbound 新增
// mesh 出口类型)。非注册表全局代理:按对端构造,由 mesh 转发层持有
// (注册表是名字→单例,一个 mesh 出口类型对应 N 个对端实例)。
type Peer struct {
	*proxy.Base
	target PeerTarget
}

var _ proxy.Proxy = (*Peer)(nil)

// NewPeer 构造对端出口。配置级校验(deviceID 非空/自指拒绝)由 config 层
// ValidateMesh 承担(Task 3),此处不重复;SelfID/DeviceID 为空时由帧头
// Encode 的非空校验在拨出期兜底报错。
func NewPeer(target PeerTarget) *Peer {
	if target.MaxHops <= 0 {
		target.MaxHops = meshproto.DefaultHops
	}
	if target.MaxHops > meshproto.MaxHopsCap {
		target.MaxHops = meshproto.MaxHopsCap
	}
	return &Peer{
		Base: &proxy.Base{
			Address:  target.DeviceID,
			Protocol: proto.Mesh,
		},
		target: target,
	}
}

// DialContext 实现 proxy.Proxy:LAN 载体优先、中继载体兜底、全部失败错误
// 回源(design §3.3:mesh 层失败一律错误回源,仅由链头的规则失败策略执行
// 后续,本出口不吞不回落)。首行 JSON 帧头(design §3.1/§3.2)按两种语义写入:
//   - 全新链头(ctx 无继承帧头):From=SelfID、Via=[SelfID]、Hops 由 0 起;
//   - 中间跳续传(ctx 携带 InheritedHeader,Task 7):入站帧头原样上线——
//     From/Class/Target/Via/Hops 全部继承,不再 AppendHop(B 侧 mesh_inbound
//     收流时已 Append 过本跳;环路在入站 Via 自查处拒绝,本路径不可达环路帧)。
//     继承帧头缺 From/Class/Target 时由 Encode 校验兜底报错(入站 Parse 已校验,
//     双保险防手挂畸形帧头)。
//
// Task 8 的 DNS 代转发复用本出口:target=dns: 原样透传(见 targetFromMetadata),
// B 侧按 meshproto.IsDNSTarget 识别并代解析。
func (p *Peer) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	class := classFromContext(ctx, md)
	hdr := &meshproto.Header{
		From:   p.target.SelfID,
		Class:  class,
		Target: targetFromMetadata(md),
	}
	if inherited, ok := InheritedHeaderFromContext(ctx); ok {
		// 浅拷贝防御:Encode 对 nil Via 会就地补 [From],不共享调用方帧头
		// 结构体;Via 切片只读共享(本路径不 Append,不改写)。
		cp := *inherited
		hdr = &cp
		// 载体准入/诊断沿用继承帧头的流量类别(design §4 约束按流量的原始
		// 类别判定,转发跳不得重推断改判):ai 流量经 B 转发不得因目标形态
		// 被推断成 custom 而禁中继。畸形类别值(Parse 只校验非空)回落本跳
		// 推断——custom 语义保守。
		if isValidClass(cp.Class) {
			class = cp.Class
		}
	}
	// 帧头一次性编码(链头帧 Via 补为 [From]、Hops 0):既 fail-fast——
	// From/Target 缺失在拨任何载体前就报错,也避免逐载体重复编码。
	line, err := hdr.Encode()
	if err != nil {
		return nil, fmt.Errorf("mesh: build header to %s: %w", p.target.DeviceID, err)
	}

	// Metadata.ConnID 经 context 透传给 PikoDial 生产实现(app/tunnel,
	// Task 9)做逐连接日志,参考 aliang 的 context 透传模式。
	if md != nil && md.ConnID != "" {
		ctx = WithConnID(ctx, md.ConnID)
	}

	// 载体一:LAN 直连(design §5 LAN 优先,同网段更快)——全类别可达,
	// 顺序尝试,单地址失败不中断。旧帧协议路径(Task E1 整体删除)临时
	// 内联拨号;新 Transport 抽象见 transport.go。
	var lastErr error
	for _, addr := range p.target.LANAddrs {
		var d net.Dialer
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			d.Timeout = 5 * time.Second
		}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		if err := writeHeader(conn, line); err != nil {
			_ = conn.Close()
			lastErr = fmt.Errorf("mesh: write header to %s: %w", addr, err)
			continue
		}
		proxy.SetKeepAlive(conn)
		return conn, nil
	}

	// 载体二:piko 中继(design §3.1 载体 A)——仅自身/AI 类别准入(design
	// §4 引擎层硬约束,不依赖前端)。custom 在准入处被拒,PikoDial 不会被调用。
	relayConfigured := p.target.Relay && p.target.PikoDial != nil
	if relayConfigured && meshproto.ClassCarrierAllowed(class, meshproto.CarrierRelay) {
		endpointID := p.target.DeviceID + meshRelayEndpointSuffix
		conn, err := p.target.PikoDial(ctx, endpointID)
		if err != nil {
			if lastErr != nil {
				// 保留「LAN 全败 → 中继也不可用」因果链:两段失败同报,
				// 便于链头失败策略与排障定位(如 LAN refused + 端点未放行)。
				return nil, fmt.Errorf("mesh: relay carrier to %s (%s) failed: %w (lan_last_err: %v)",
					p.target.DeviceID, endpointID, err, lastErr)
			}
			return nil, fmt.Errorf("mesh: relay carrier to %s (%s) failed: %w", p.target.DeviceID, endpointID, err)
		}
		if err := writeHeader(conn, line); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("mesh: write header via relay to %s: %w", p.target.DeviceID, err)
		}
		return conn, nil
	}

	// 全部载体不可用:错误回源。custom 禁中继/中继未接线时 LAN 的最后错误
	// 即根因,%w 保留供上层 errors.Is/As;无 LAN 地址也无可拨载体时如实说明。
	if lastErr == nil {
		return nil, fmt.Errorf("mesh: no carrier to %s (class=%s, lan addrs=%d, relay=%v)",
			p.target.DeviceID, class, len(p.target.LANAddrs), p.target.Relay)
	}
	return nil, fmt.Errorf("mesh: no carrier to %s (class=%s, %d lan addrs failed, relay=%v): %w",
		p.target.DeviceID, class, len(p.target.LANAddrs), p.target.Relay, lastErr)
}

// writeHeader 写入首行 JSON 帧头行(design §3.1/§3.2):一行 JSON + 剩余字节
// 原始透传,复刻 app/tunnel/bridge.go tcpRouteHeader 风格。line 由调用方
// 经 Header.Encode 预编码(链头帧 Via 补为 [From]、Hops 保持 0)。
func writeHeader(conn net.Conn, line string) error {
	if _, err := conn.Write([]byte(line)); err != nil {
		return err
	}
	return nil
}

// meshClassKey 是 mesh 流量类别的 context key。不改公共 M.Metadata——类别
// 是 mesh 出口的私语(参考 aliang handshaker 的 context 透传模式)。
type meshClassKey struct{}

// WithClass 供 egress/AI 侧显式指定流量类别(meshproto.ClassSelf|ClassAI|
// ClassCustom)。ClassSelf 无推断路径——自身流量发起点(登录/用量/更新)必须
// 显式携带;未指定/非法值时走默认推断:56432 回环→AI,其余→custom。
func WithClass(ctx context.Context, class string) context.Context {
	return context.WithValue(ctx, meshClassKey{}, class)
}

func classFromContext(ctx context.Context, md *M.Metadata) string {
	if v, ok := ctx.Value(meshClassKey{}).(string); ok && isValidClass(v) {
		return v
	}
	if looksLikeAITraffic(md) {
		return meshproto.ClassAI
	}
	return meshproto.ClassCustom
}

// InferClass 返回连接的 mesh 流量类别(显式 WithClass 优先,否则按
// looksLikeAITraffic 推断:56432 回环 → ai,其余 → custom)。链头执行层
// (processor/tcp dialByRoute)对离线兜底类别勾选(design §9.4)的判定复用
// 本口径;self 类由 egress 发起点显式 WithClass 指定(Task 11 接入),当前
// 推断面仅 ai/custom。
func InferClass(ctx context.Context, md *M.Metadata) string {
	return classFromContext(ctx, md)
}

func isValidClass(class string) bool {
	switch class {
	case meshproto.ClassSelf, meshproto.ClassAI, meshproto.ClassCustom:
		return true
	}
	return false
}

// connIDKey 与 aliangContextConnIDKey 同模式:Metadata.ConnID 经 context
// 透传给 PikoDial 生产实现。
type connIDKey struct{}

// WithConnID 把连接 ID 注入 ctx(由 DialContext 内部调用,生产 PikoDial
// 经 ConnIDFromContext 读取做逐连接日志)。
func WithConnID(ctx context.Context, connID string) context.Context {
	return context.WithValue(ctx, connIDKey{}, connID)
}

// ConnIDFromContext 取透传的连接 ID,未携带返回空串。
func ConnIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(connIDKey{}).(string)
	return v
}

// inheritedHeaderKey 继承帧头的 context key(Task 7 Via/Hops 续传)。
type inheritedHeaderKey struct{}

// WithInheritedHeader 把入站帧头(mesh_inbound AppendHop 后)挂进 ctx——
// 中间跳续传:B 侧注入连接经规则决策再拨 mesh 出口时,出口用继承帧头
// 原样上线(Via/Hops 单调不重置,不再 AppendHop)。生产注入点为
// processor/tcp HandleMeshConn;测试经本函数直接构造。
func WithInheritedHeader(ctx context.Context, hdr *meshproto.Header) context.Context {
	return context.WithValue(ctx, inheritedHeaderKey{}, hdr)
}

// InheritedHeaderFromContext 取透传的继承帧头;未携带或挂了 nil 返回
// ok=false(后者按「无继承」处理,走全新链头语义)。
func InheritedHeaderFromContext(ctx context.Context) (*meshproto.Header, bool) {
	hdr, ok := ctx.Value(inheritedHeaderKey{}).(*meshproto.Header)
	if !ok || hdr == nil {
		return nil, false
	}
	return hdr, true
}

// aiProxyPort 与 processor/config.DefaultHTTPProxyPort(56432)同值,由
// mesh_test.go 的 TestAIProxyPortSyncAnchor 锚定。本地常量的真实理由是
// 数据面拨号路径不引入 processor 配置依赖子树(processor/config 的闭包
// 含 geoip/match/cache 等,与成环无关——config 不依赖 outbound/proxy,
// test-only import 已实测无环);判据口径见 looksLikeAITraffic。
const aiProxyPort = 56432

// looksLikeAITraffic AI 流量判据,与 processor/tcp/route_override.go 的
// shouldForceAliangRoute 同源口径:DstPort==56432 且目标回环(直连回环 IP,
// 或 HostName 为 localhost/回环字面量)。本地实现而非 import 的理由见
// aiProxyPort 注释(数据面依赖最小化)——两处判据须同步修改。
func looksLikeAITraffic(md *M.Metadata) bool {
	if md == nil || md.DstPort != aiProxyPort {
		return false
	}
	if md.DstIP.IsValid() && md.DstIP.IsLoopback() {
		return true
	}
	host := strings.ToLower(strings.TrimSpace(md.HostName))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return true
	}
	return false
}

// targetFromMetadata 构造帧头目标(design §3.2):HostName 优先(域名直转
// 语义,B 侧免解析),空则回落 DstIP;已带端口的 HostName 直用,其余
// JoinHostPort,DstPort 零值按 443 兜底——与 meshproto.SplitTarget 的拆分
// 口径互逆(拆分侧的裸域名 443 兜底由 B 侧消费,Task 6)。
// Task 8 的 DNS 代转发复用本出口:target=dns: 原样透传(HostName="dns:",
// 不拼端口),B 侧按 meshproto.IsDNSTarget 识别代解析。
func targetFromMetadata(md *M.Metadata) string {
	if md == nil {
		return ""
	}
	host := strings.TrimSpace(md.HostName)
	if meshproto.IsDNSTarget(host) {
		// dns: 特殊目标原样透传:落入下方 JoinHostPort 会被拼成 "[dns:]:53"。
		return meshproto.DNSTarget
	}
	if host == "" {
		if !md.DstIP.IsValid() {
			return ""
		}
		host = md.DstIP.String()
	}
	if _, port, _, err := meshproto.SplitTarget(host); err == nil && port != "" {
		return host // HostName 已含端口(如 CONNECT 的 host:port 形态),直用
	}
	port := int(md.DstPort)
	if port == 0 {
		port = 443
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
