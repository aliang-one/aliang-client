// Package mesh 承担 mesh 出口侧(SSH 代理链,A 侧)的公共小件:流量类别
// 判定与 context 助手(WithClass/InferClass/WithConnID)、拨号最小接口
// (Dialer)。真正的出口客户端是 ExitClient(SSH mesh-exit channel,见
// sshexit.go);旧 JSON 帧协议出口(Peer)与 B 侧继承帧头 context 缝
// (WithInheritedHeader,仅供帧协议注入消费)已随 SPEC §13.1 整体退役。
package mesh

import (
	"context"
	"net"
	"net/netip"
	"strings"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
)

// PikoDialFunc 拨 piko 端点(中继载体 A 侧,SPEC §4.2 载体二:每条拨号即
// 一个桥接流,yamux 由 piko 持有,A 侧零 mux 代码)。签名与 piko client
// v0.10.0 的 (*client.Dialer).Dial 完全一致(spike 2026-11-08 实测:进程内
// server + Upstream.Listen/Dialer.Dial 回环端到端双向字节通):
//
//	dialer := &pikoclient.Dialer{URL: proxyURL, Token: token} // proxy 端口
//	pikoDial := dialer.Dial                                   // 方法值直接赋
//
// 注入式接缝:生产实现由 app/tunnel 提供(manager.dialMeshEndpoint,每次
// 拨号读当前配置),经 processor/tcp 的 SetMeshTransportDialSource 注入缝
// 进入 ExitClient 构造(SPEC §7.3)。本包不 import piko,保持数据面拨号
// 路径依赖最小。
type PikoDialFunc func(ctx context.Context, endpointID string) (net.Conn, error)

// meshRelayEndpointSuffix 中继端点后缀:A 拨 <B_deviceID>.mesh(SPEC §4.2;
// B 侧以第二端点模式常驻 serve,见 app/tunnel manager.go)。
const meshRelayEndpointSuffix = ".mesh"

// Dialer 是 mesh 出口拨号的最小接口(SPEC 计划决策 #1 的承载形态):消费方
// (processor/tcp 链头、egress 兜底、TUN DNS 代转发、app/tunnel egressPeer
// 装配)一律面向本接口,不感知 ExitClient 的构造事实(私钥解析/钉扎/
// Transport 切片)。
type Dialer interface {
	DialContext(ctx context.Context, md *M.Metadata) (net.Conn, error)
}

// ExitClient 天然实现 Dialer(编译期钉死,防签名漂移)。
var _ Dialer = (*ExitClient)(nil)

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
// (processor/tcp dialByRoute)对离线兜底类别勾选(SPEC §7.2)的判定复用
// 本口径;self 类由 egress 发起点显式 WithClass 指定,推断面仅 ai/custom。
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

// WithConnID 把连接 ID 注入 ctx(由 ExitClient.DialContext 内部调用,生产
// PikoDial 经 ConnIDFromContext 读取做逐连接日志)。
func WithConnID(ctx context.Context, connID string) context.Context {
	return context.WithValue(ctx, connIDKey{}, connID)
}

// ConnIDFromContext 取透传的连接 ID,未携带返回空串。
func ConnIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(connIDKey{}).(string)
	return v
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
