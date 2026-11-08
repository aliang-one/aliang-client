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
// 注入式接缝:测试注入 fake;生产由 app/tunnel 封装(Task 9 接线)。本包
// 不 import piko,保持数据面拨号路径依赖最小。
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
	MaxHops  int          // 跳数上限;链头不消费(中间跳 relay 语义,Task 6),构造期归一暂存
	SelfID   string       // 本机 device_id,帧头 From/Via 链头
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
// 后续,本出口不吞不回落)。首行 JSON 帧头(design §3.1/§3.2)由链头写入:
// Via=[From]、Hops 由 0 起;中间跳 AppendHop 属 relay 侧语义(Task 6)。
func (p *Peer) DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error) {
	class := classFromContext(ctx, md)
	hdr := &meshproto.Header{
		From:   p.target.SelfID,
		Class:  class,
		Target: targetFromMetadata(md),
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
	// 顺序尝试,单地址失败不中断。
	var lastErr error
	for _, addr := range p.target.LANAddrs {
		conn, err := dialLAN(ctx, addr)
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

// lanDialTimeout 是 LAN 载体单地址拨号兜底超时(参考 agent_peer_lan.go
// agentLanDialTimeout 先例);ctx 自带 deadline 时以 ctx 为准(更紧者生效)。
const lanDialTimeout = 5 * time.Second

func dialLAN(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		d.Timeout = lanDialTimeout
	}
	return d.DialContext(ctx, "tcp", addr)
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

// aiProxyPort 与 processor/config.DefaultHTTPProxyPort(56432)同值。本地
// 常量避免数据面拨号路径反向依赖 processor 配置包(processor/tcp import 了
// outbound,判据口径见 looksLikeAITraffic)。
const aiProxyPort = 56432

// looksLikeAITraffic AI 流量判据,与 processor/tcp/route_override.go 的
// shouldForceAliangRoute 同源口径:DstPort==56432 且目标回环(直连回环 IP,
// 或 HostName 为 localhost/回环字面量)。processor/tcp import 了 outbound,
// 反向 import 会成环,故在此包一份本地实现——两处判据须同步修改。
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
func targetFromMetadata(md *M.Metadata) string {
	if md == nil {
		return ""
	}
	host := strings.TrimSpace(md.HostName)
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
