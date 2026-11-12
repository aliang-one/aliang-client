// Package egress 统一自身流量(登录/扫码/激活/刷新/用量/软件更新)的 HTTP
// 出口。出口链路形态(2026-11-12,替代旧「离线兜底」):开关开
// (customer.mesh.fallback.enable + next_hop 指向已钉扎 peer)时,平台流量
// 常驻经链上下一跳出口(下一跳也开着就继续往前链)——触发无条件,不按
// 本机能否直连平台做限制(离线信号缝已随旧门控移除);下一跳故障时
// 对外流量如实失败、错误回源,不回落 direct(开关=出口完全依赖下一跳,
// 发布说明已写明取舍)。开关关 = 纯直连,与裸 http.Client 在线行为零变化;
// 未配置的设备完全不受影响。
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
// meshTransportDialSource 先例):egress 不摸全局单例链,peer 由组装层提供。
// 提供器返回 mesh.Dialer(生产 = ExitClient,app/tunnel egressPeer 从
// customer.mesh.Peers[0] 装配;返回 nil 表示当前无可用 peer)。未注入 =
// 纯 direct。
var peerSource atomic.Value // func() mesh.Dialer

// SetPeerSource 注入 mesh peer 提供器;nil = 清除(回到未接线语义)。
// 并发安全(生产写方为隧道续期路径,读方为每次平台拨号)。
func SetPeerSource(fn func() mesh.Dialer) {
	if fn == nil {
		fn = func() mesh.Dialer { return nil }
	}
	peerSource.Store(fn)
}

func peerFromSource() mesh.Dialer {
	fn, _ := peerSource.Load().(func() mesh.Dialer)
	if fn == nil {
		return nil
	}
	return fn()
}

// PeerSourceForTest 读回注入缝的当前值:生产读方走 peerFromSource;导出
// 仅供 app/tunnel 装配断言测试把「peer 源已接线」从注释升级为测试钉子。
func PeerSourceForTest() mesh.Dialer { return peerFromSource() }

// meshDelegateActive 出口链路门(2026-11-12,替代旧兜底门),逐连接读取:
// config.MeshDelegateNextHop 判定(开关开 ∧ next_hop 指向已钉扎 peer ∧
// mesh 生效依赖满足)。触发为常驻语义——开关即无条件改道,不叠加任何
// 在线/离线信号。
func meshDelegateActive() bool {
	_, ok := config.GetGlobalConfig().MeshDelegateNextHop()
	return ok
}

// platformDialContext 出口链路判定(2026-11-12):
//
//	开关关/下一跳不合法 → 纯 direct(旧行为零变化);
//	开关开 → mesh 委托(ExitClient.DialContext,WithClass(ClassSelf),
//	  target=域名:端口——域名直转语义:不本地解析,主机名原样进 payload,
//	  末跳代解析);拨号失败如实失败,不回落 direct。
//
// 域名直转的 TLS 语义:mesh 拨出的是到目标 443 的原始 TCP 流,TLS 握手
// 在 http.Transport 层照常发生(SNI=平台域名)——B 侧按其规则处理,链路
// 成立(与注入管线同构),无需特殊处理。
func platformDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !meshDelegateActive() {
		return directDialer.DialContext(ctx, network, addr)
	}
	peer := peerFromSource()
	if peer == nil {
		// 门开但 peer 源未接线/构造失败:开关语义=出口完全依赖下一跳,
		// 不回落 direct(静默绕链会让「开了开关却没走链」不可见),错误
		// 回源让装配断链立刻暴露(fail-loud)。
		return nil, fmt.Errorf("egress: delegate active but peer source unavailable (target=%s)", addr)
	}
	host, portStr := hostPortWithDefault(addr)
	port, _ := strconv.Atoi(portStr) // 拆分失败/非数字时留 0,由 mesh 侧 443 兜底
	conn, err := peer.DialContext(
		mesh.WithClass(ctx, meshproto.ClassSelf),
		&M.Metadata{HostName: host, DstPort: uint16(port)},
	)
	if err != nil {
		return nil, fmt.Errorf("egress: delegate dial via mesh failed (target=%s): %w", addr, err)
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
// Transport 共享,出口链路判定见 platformDialContext。
func PlatformClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: platformTransport, Timeout: timeout}
}
