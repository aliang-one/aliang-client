package config

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

const (
	// DefaultManagementPort 管理面板 HTTP 端口
	DefaultManagementPort = 56431

	// DefaultHTTPProxyPort HTTP CONNECT 代理端口
	DefaultHTTPProxyPort = 56432

	// DefaultUserAgentPort 用户态 Agent 本地端口
	DefaultUserAgentPort = 56433

	// DefaultServiceBindHost 管理面板和 HTTP 代理的安全默认监听主机。
	DefaultServiceBindHost = "127.0.0.1"

	// DefaultManagementAddr 管理面板默认监听地址。
	DefaultManagementAddr = "127.0.0.1:56431"

	// DefaultHTTPProxyAddr HTTP CONNECT 代理监听地址
	DefaultHTTPProxyAddr = "127.0.0.1:56432"

	// DefaultAgentServerURL 用户态 Agent 独立服务端地址
	DefaultAgentServerURL = "http://localhost:4000"
)

var (
	// DefaultUserAgentAddr 用户态 Agent 本地监听地址
	DefaultUserAgentAddr = resolveDefaultUserAgentAddr()

	serviceBindHostMu sync.RWMutex
	serviceBindHost   = DefaultServiceBindHost
)

// SetServiceBindHost sets the host shared by the management and HTTP proxy
// listeners. The CLI calls this before starting any service.
func SetServiceBindHost(host string) error {
	host = strings.TrimSpace(host)
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid --host %q: expected an IP address without a port", host)
	}

	serviceBindHostMu.Lock()
	serviceBindHost = ip.String()
	serviceBindHostMu.Unlock()
	return nil
}

// ServiceBindHost returns the configured host shared by public-facing services.
func ServiceBindHost() string {
	serviceBindHostMu.RLock()
	defer serviceBindHostMu.RUnlock()
	return serviceBindHost
}

// ManagementListenAddr returns the management server's runtime listen address.
func ManagementListenAddr() string {
	return net.JoinHostPort(ServiceBindHost(), fmt.Sprintf("%d", DefaultManagementPort))
}

// HTTPProxyListenAddr returns the HTTP proxy's runtime listen address.
func HTTPProxyListenAddr() string {
	return net.JoinHostPort(ServiceBindHost(), fmt.Sprintf("%d", DefaultHTTPProxyPort))
}

// UnifiedIngressListenAddr 统一入口(56432,HTTP CONNECT/SSH/socks5/mesh 四
// 族共用一个嗅探口)的运行期绑定地址(spec D2「绑定跟随启用面」)。
//
// mesh 总开关生效(EffectiveEnabled,三态 nil=开)→ 0.0.0.0:组网流量靠对端
// LAN 拨入才可达,绑回环等于把 LAN 载体静默废掉——与退役前的常驻 LAN 口
// (56441 无主机限制)behavior parity。否则沿用 ServiceBindHost 的安全默认
// 回环:不开组网就不放大本机暴露面。
//
// 每次调用现读全局配置与 serviceBindHost,故配置提交后本函数的返回值可能
// 变化——绑定变更由 app 层 reconcile 钩子比对并重启 listener(在途连接不
// 受影响),本函数只负责给出「此刻应有的地址」。
func UnifiedIngressListenAddr() string {
	return UnifiedIngressListenAddrFor(GetGlobalConfig())
}

// UnifiedIngressListenAddrFor 与 UnifiedIngressListenAddr 同口径,但显式接收
// 配置对象:配置提交协调路径(ReconcileUnifiedIngressBinding)在提交回调里
// 拿到的是即将生效的 cfg,显式传入可避开「SetGlobalConfig 与 reconcile 的
// 先后顺序」依赖——两处口径永远同源。
func UnifiedIngressListenAddrFor(cfg *Config) string {
	host := ServiceBindHost()
	if cfg != nil && cfg.Customer != nil && cfg.Customer.Mesh != nil && cfg.Customer.Mesh.EffectiveEnabled() {
		host = "0.0.0.0" // mesh 开 = LAN 组网可达(退役前 LAN 载体 parity)
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", DefaultHTTPProxyPort))
}

func resolveDefaultUserAgentAddr() string {
	if addr := strings.TrimSpace(os.Getenv("ALIANG_USER_AGENT_ADDR")); addr != "" {
		return addr
	}
	return "127.0.0.1:56433"
}
