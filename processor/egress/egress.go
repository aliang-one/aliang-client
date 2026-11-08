// Package egress 统一自身流量(登录/扫码/激活/刷新/用量/软件更新)的 HTTP
// 出口(design docs/mesh-forwarding-network-design.md §9.4 兜底式的 egress
// 变体,计划 Task 11):默认纯直连(与裸 http.Client 在线行为零变化),
// 仅当离线信号为真、离线兜底配置显式使能、self 类别勾选、且有可用 mesh
// peer 时(§9.4「平台不可达才兜底」),平台流量才优先经 B 节点出口
// (仍然到达平台,经 B 中转)——这是 opt-in 配置的行为契约;未配置兜底
// 的设备完全不受影响。
package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
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

// offlineSignal 离线信号注入缝(与 peerSource 同模式):design §9.4 定死
// 「平台不可达才兜底」——在线设备即使配好兜底也一律直连。信号未接线
// (nil)视为在线(保守:不激活 mesh 轨,不吞直连路径)。生产接线 Task 14
// 从隧道状态取;**共用契约:Task 12 的 AI 离线改道消费同一信号源**,
// 两处判定必须同源同真假,否则会出现「自身流量经 B 而 AI 流量直连」的
// 分裂形态。
var offlineSignal atomic.Value // func() bool

// SetOfflineSignal 注入离线信号提供器(true=平台不可达,激活兜底轨);
// nil = 清除(未接线 = 视为在线 = 纯 direct)。并发安全。
func SetOfflineSignal(fn func() bool) {
	if fn == nil {
		fn = func() bool { return false }
	}
	offlineSignal.Store(fn)
}

func isOffline() bool {
	fn, _ := offlineSignal.Load().(func() bool)
	if fn == nil {
		return false
	}
	return fn()
}

// OfflineSignalForTest / PeerSourceForTest 读回两条注入缝的当前值:生产读方
// 走 isOffline/peerFromSource;导出仅供 app/tunnel 装配断言测试把「两缝同源
// 同真假」「peer 源已接线」从注释升级为测试钉子(Task 14)。
func OfflineSignalForTest() bool    { return isOffline() }
func PeerSourceForTest() *mesh.Peer { return peerFromSource() }

// meshFallbackSelfActive 兜底门(design §9.4「平台不可达才兜底」),逐连接
// 读取。配置四条件(总开关 EffectiveEnabled + Fallback.Enable + self ∈
// Classes + Addr 非空)统一走共享助手 config.MeshFallbackActiveForClass
// (计划 Task 12 评审钉死的三消费方抽取;对等消费方见 processor/tcp
// meshFallbackAddrForClass 与 AI 离线改道 meshOfflineRedirectForAI,
// processor/routing meshForwardAvailable 语义不同不并入),再叠加本消费方
// 特有的离线信号(信号驱动,接线归 Task 14,与 processor/tcp
// SetMeshOfflineSignal 共用同一 manager 状态 getter)。
//
// 抽取带来的语义收紧:助手含 Addr 非空检查(原 egress 闸无此条)——
// Enable=true 而 Addr 空的配置本就被 Config.Validate 拒绝,此处只是防御
// 手改配置文件的漏网形态,行为面不变。兜底随信号起落:平台恢复可达 →
// 信号翻假 → mesh 轨自动退位回直连。peer 的可用性归注入缝的提供器判定。
func meshFallbackSelfActive() bool {
	_, ok := config.MeshFallbackActiveForClass(config.GetGlobalConfig(), meshproto.ClassSelf)
	return ok && isOffline()
}

// platformDialContext 双轨判定(design §9.4 兜底式):
//
//	离线信号真 + 兜底使能 + self 勾选 + 有可用 peer → 先 mesh
//	  (mesh.Peer.DialContext,WithClass(ClassSelf),target=域名:端口——域名
//	  直转语义:不本地解析,主机名原样进帧,末跳代解析);mesh 任一失败 →
//	  direct 重试一次,失败即失败(不循环);
//	其余(在线/未配置/无 peer)→ 纯 direct(现状,在线行为零变化)。
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
		// mesh 失败 → direct 重试一次:兜底是可用性增强,不能把可达的
		// 平台流量拖死在坏 mesh 路径上;direct 再失败即如实失败。交界处
		// Warn 一条——离线排障是本特性存在的理由,mesh 轨为何没接住
		// 必须在日志里可见。
		dconn, derr := directDialer.DialContext(ctx, network, addr)
		if derr != nil {
			return nil, fmt.Errorf("egress: mesh 路由失败: %w; direct 兜底亦失败: %w", err, derr)
		}
		logger.Warn(fmt.Sprintf("egress: mesh 路由失败,回落 direct(target=%s): %v", addr, err))
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
