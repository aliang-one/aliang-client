package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"

	pikoclient "github.com/andydunstall/piko/client"

	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
)

// errMeshRelayUnavailable 中继载体软失败契约(design §10「平台不可达」):
// proxy url 未下发(或 Stop 后已清空)时,中继载体明确不可用——mesh 出口层
// 由此回落「无可用载体」错误回源,LAN 载体不受影响。
var errMeshRelayUnavailable = errors.New("mesh relay carrier unavailable: proxy url not configured")

// meshDialConfig 是中继载体 A 侧拨号的当前配置快照(Manager.mu 保护)。由
// validateConfig 在每次 Configure(含幂等续期)写入、Stop 清空;DialPeerMesh
// 每次拨号原子读取——Configure 每次续期整体重置配置,拨号闭包若捕获某一代
// 配置的值而非读 manager 存储,续期后就会带着陈旧 proxy url/token 拨号。
type meshDialConfig struct {
	proxyURL *url.URL
	token    string
}

// setMeshDial 原子更新当前中继拨号配置(nil proxyURL = 中继载体不可用)。
// 新配置整体生效语义:畸形 proxy url(软失败为 nil)会抹掉上一个可用值——
// 续期帧是整体快照而非增量,平台侧下发须自证合法;last-known-good 保留为
// 可选后续优化,当前不做。
func (m *Manager) setMeshDial(proxyURL *url.URL, token string) {
	m.mu.Lock()
	m.meshDial = meshDialConfig{proxyURL: proxyURL, token: token}
	m.mu.Unlock()
}

// clearMeshDialLocked 随会话停止一并收口中继载体(Stop 路径;调用方持有
// Manager.mu)。
func (m *Manager) clearMeshDialLocked() {
	m.meshDial = meshDialConfig{}
}

// meshDialSnapshot 取当前中继拨号配置快照(短临界区,构造 Dialer 在锁外)。
func (m *Manager) meshDialSnapshot() meshDialConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meshDial
}

// MeshRelayProxyURL 返回当前配置的 piko 代理面 URL(""=中继载体不可用)。
// 诊断/测试读回:平台侧核对 piko_proxy_url 是否已随 configure 帧生效。
func (m *Manager) MeshRelayProxyURL() string {
	snapshot := m.meshDialSnapshot()
	if snapshot.proxyURL == nil {
		return ""
	}
	return snapshot.proxyURL.String()
}

// DialPeerMesh 拨对端的 "<deviceID>.mesh" piko 端点(design §3.1 载体 A 的
// A 侧;B 侧由 manager 恒接线 serve,见 manager.go runTunnel)。每次拨号从
// manager 当前配置原子读取 proxy url+token 构造 piko client.Dialer(spike
// 2026-11-08:拨的是 piko 代理面端口,与上游监听面是同 server 的两个端口,
// 由 configure 帧的 piko_proxy_url 单独下发)。proxy url 未配置 → 软失败
// 错误,LAN 载体不受影响。
func (m *Manager) DialPeerMesh(ctx context.Context, deviceID string) (net.Conn, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, errors.New("mesh relay carrier: empty device id")
	}
	return m.dialMeshEndpoint(ctx, MeshEndpointID(deviceID))
}

// dialMeshEndpoint 按 piko 端点 id 拨中继载体。该方法是 processor/tcp 注入缝
// (SetMeshTransportDialSource)返回的生产拨号函数本体:mesh 出口以
// "<deviceID>.mesh" 端点名调用;每次调用读当前配置,闭包捕获的是 manager
// 本体而非某代配置值。成功路径不逐连接打日志(噪声),失败带 conn_id 记录
// 便于排障(conn id 经 ctx 由 mesh 出口透传,见 mesh.WithConnID)。
func (m *Manager) dialMeshEndpoint(ctx context.Context, endpointID string) (net.Conn, error) {
	snapshot := m.meshDialSnapshot()
	if snapshot.proxyURL == nil {
		return nil, errMeshRelayUnavailable
	}
	dialer := &pikoclient.Dialer{URL: snapshot.proxyURL, Token: snapshot.token}
	conn, err := dialer.Dial(ctx, endpointID)
	if err != nil {
		if connID := meshproxy.ConnIDFromContext(ctx); connID != "" {
			log.Printf("[AGENT-TUNNEL] mesh relay dial conn_id=%s endpoint=%s failed: err=%v", connID, endpointID, err)
		} else {
			log.Printf("[AGENT-TUNNEL] mesh relay dial endpoint=%s failed: err=%v", endpointID, err)
		}
		return nil, fmt.Errorf("mesh relay carrier dial %s: %w", endpointID, err)
	}
	return conn, nil
}

// validatePikoProxyURL 解析 configure 帧的可选键 piko_proxy_url:空 = 未下发
// (nil, nil;中继载体不可用);非法 = 软失败(nil, err)——不拒绝整个
// configure,隧道与 LAN 载体照常。规则与上游 piko_upstream_url 同源(见
// validateConfig):host 非空、无 userinfo/fragment、https(回环 http 开发
// 例外)。
func validatePikoProxyURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("invalid Piko proxy URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("Piko proxy URL must not contain user info or a fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("Piko proxy URL must use HTTPS except for loopback development")
	}
	return parsed, nil
}
