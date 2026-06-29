package config

import (
	"net"
	"testing"
)

// TestDefaultManagementAddrNotLoopback 守护管理面板的默认监听地址。
// headless/服务器部署依赖它绑在 0.0.0.0 才能从外部访问；若被改回
// loopback（127.0.0.1），外部将再次无法访问，且不会产生任何运行时报错，
// 因此用测试固定这一不变量。
func TestDefaultManagementAddrNotLoopback(t *testing.T) {
	host, port, err := net.SplitHostPort(DefaultManagementAddr)
	if err != nil {
		t.Fatalf("DefaultManagementAddr %q 不是合法的 host:port: %v", DefaultManagementAddr, err)
	}
	if port != "56431" {
		t.Errorf("管理端口应为 56431，实际 %q", port)
	}
	if host == "" {
		return // ":port" 形式等同于 0.0.0.0，可接受
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		t.Errorf("DefaultManagementAddr 绑定在 loopback %q，headless 部署将从外部不可达", host)
	}
}
