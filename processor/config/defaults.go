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

func resolveDefaultUserAgentAddr() string {
	if addr := strings.TrimSpace(os.Getenv("ALIANG_USER_AGENT_ADDR")); addr != "" {
		return addr
	}
	return "127.0.0.1:56433"
}
