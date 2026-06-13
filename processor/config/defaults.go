package config

import (
	"os"
	"strings"
)

const (
	// DefaultManagementPort 管理面板 HTTP 端口
	DefaultManagementPort = 56431

	// DefaultHTTPProxyPort HTTP CONNECT 代理端口
	DefaultHTTPProxyPort = 56432

	// DefaultUserAgentPort 用户态 Agent 本地端口
	DefaultUserAgentPort = 56433

	// DefaultManagementAddr 管理面板监听地址
	DefaultManagementAddr = "127.0.0.1:56431"

	// DefaultHTTPProxyAddr HTTP CONNECT 代理监听地址
	DefaultHTTPProxyAddr = "127.0.0.1:56432"

	// DefaultAgentServerURL 用户态 Agent 独立服务端地址
	DefaultAgentServerURL = "http://localhost:4000"
)

// DefaultUserAgentAddr 用户态 Agent 本地监听地址
var DefaultUserAgentAddr = resolveDefaultUserAgentAddr()

func resolveDefaultUserAgentAddr() string {
	if addr := strings.TrimSpace(os.Getenv("ALIANG_USER_AGENT_ADDR")); addr != "" {
		return addr
	}
	return "127.0.0.1:56433"
}
