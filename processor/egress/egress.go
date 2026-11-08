// Package egress 统一自身流量(登录/扫码/激活/刷新/用量/软件更新)的 HTTP
// 出口(design docs/mesh-forwarding-network-design.md §9.4 兜底式的 egress
// 变体,计划 Task 11):默认纯直连(与裸 http.Client 在线行为零变化),
// 仅当离线兜底配置显式使能、self 类别勾选、且有可用 mesh peer 时,平台
// 流量优先经 B 节点出口(仍然到达平台,经 B 中转)——这是 opt-in 配置的
// 行为契约;未配置兜底的设备完全不受影响。
package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
	mesh "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
)

// directDialer direct 轨道拨号器,参数与 http.DefaultTransport 内建拨号器
// 一致(Timeout/KeepAlive 30s):兜底未配置时的在线行为与裸
// http.Client(走 DefaultTransport)逐字节对齐。
var directDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

// peerSource mesh peer 注入缝(atomic.Value,同 processor/tcp 的
// meshSelfIDSource/meshPikoDialSource 先例):egress 不摸全局单例链,peer
// 由组装层提供。生产接线留 Task 14(app/tunnel 组装层取 Peers[0]/配置
// 解析:兜底地址解析为 LANAddrs、PikoDial 封装、返回 nil 表示当前无可用
// peer)。未注入 = 纯 direct。
var peerSource atomic.Value // func() *mesh.Peer

// SetPeerSource 注入 mesh peer 提供器;nil = 清除(回到未接线语义)。
// 并发安全(生产写方为隧道续期路径,读方为每次平台拨号)。
func SetPeerSource(fn func() *mesh.Peer) {
	if fn == nil {
		fn = func() *mesh.Peer { return nil }
	}
	peerSource.Store(fn)
}

func peerFromSource() *mesh.Peer {
	fn, _ := peerSource.Load().(func() *mesh.Peer)
	if fn == nil {
		return nil
	}
	return fn()
}

// meshFallbackSelfActive 兜底门(design §9.4):customer.mesh.fallback 使能
// (Enable 显式 true)且 self ∈ Classes。逐连接读取——配置变更即刻生效,
// 恢复在线后兜底自动退位。Addr/peer 的可用性归注入缝的提供器判定,此处
// 不重复。
func meshFallbackSelfActive() bool {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.Mesh == nil {
		return false
	}
	fb := cfg.Customer.Mesh.Fallback
	if fb == nil || fb.Enable == nil || !*fb.Enable {
		return false
	}
	for _, class := range fb.Classes {
		if class == meshproto.ClassSelf {
			return true
		}
	}
	return false
}

// platformDialContext 双轨判定(design §9.4 兜底式):
//
//	兜底使能 + self 勾选 + 有可用 peer → 先 mesh(mesh.Peer.DialContext,
//	  WithClass(ClassSelf),target=域名:端口——域名直转语义:不本地解析,
//	  主机名原样进帧,末跳代解析);mesh 任一失败 → direct 重试一次,失败
//	  即失败(不循环);
//	兜底未配置 / 无 peer → 纯 direct(现状,在线行为零变化)。
//
// 域名直转的 TLS 语义:mesh 拨出的是到目标 443 的原始 TCP 流,TLS 握手
// 在 http.Transport 层照常发生(SNI=平台域名)——B 侧按其规则处理,链路
// 成立(与 Task 6 注入管线同构),无需特殊处理。
func platformDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !meshFallbackSelfActive() {
		return directDialer.DialContext(ctx, network, addr)
	}
	peer := peerFromSource()
	if peer == nil {
		return directDialer.DialContext(ctx, network, addr)
	}
	host, portStr := hostPortWithDefault(addr)
	port, _ := strconv.Atoi(portStr) // 拆分失败/非数字时留 0,由 mesh 侧 443 兜底
	conn, err := peer.DialContext(
		mesh.WithClass(ctx, meshproto.ClassSelf),
		&M.Metadata{HostName: host, DstPort: uint16(port)},
	)
	if err != nil {
		// mesh 失败 → direct 重试一次:兜底是可用性增强,不能把在线可达的
		// 平台流量拖死在坏 mesh 路径上;direct 再失败即如实失败。
		dconn, derr := directDialer.DialContext(ctx, network, addr)
		if derr != nil {
			return nil, fmt.Errorf("egress: mesh 路由失败(%v), direct 兜底亦失败: %w", err, derr)
		}
		return dconn, nil
	}
	return conn, nil
}

// hostPortWithDefault 拆 addr 的 host:port;无端口形态按 443 兜底(自身
// 流量主体是平台 https)。http.Transport 恒传带端口的地址(scheme 已折算:
// https→443/http→80),443 兜底仅是防御。
func hostPortWithDefault(addr string) (host, port string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return addr, "443"
	}
	return host, port
}

// platformTransport 全局共享 Transport(Clone 自 http.DefaultTransport:
// 连接复用/TLS 握手超时/Happy Eyeballs 等语义全部保留),仅覆写 DialContext。
// Proxy 显式保留 ProxyFromEnvironment——既有 env 代理语义(HTTP_PROXY/
// HTTPS_PROXY/NO_PROXY)不变。
var platformTransport = func() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	tr.DialContext = platformDialContext
	return tr
}()

// PlatformClient 返回自身流量(登录/用量/更新等平台调用)统一出口的
// HTTP 客户端:Timeout 语义与裸 http.Client 一致,由调用方传入;底层
// Transport 共享,双轨判定见 platformDialContext。
func PlatformClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: platformTransport, Timeout: timeout}
}
