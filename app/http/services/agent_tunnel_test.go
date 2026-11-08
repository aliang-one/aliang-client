package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	pikolog "github.com/andydunstall/piko/pkg/log"
	pikoconfig "github.com/andydunstall/piko/server/config"
	pikoserver "github.com/andydunstall/piko/server"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tunnel"
)

func TestConfigureTunnelRejectsAnotherDevice(t *testing.T) {
	svc := &AgentService{
		identity: &agentDeviceIdentity{DeviceID: "dev_local"},
		state:    agentState{DeviceID: "dev_local", Enabled: true, Registered: true},
	}
	var response map[string]interface{}
	svc.configureTunnel(map[string]interface{}{
		"request_id": "req_1",
		"device_id":  "dev_other",
	}, func(payload interface{}) error {
		response = payload.(map[string]interface{})
		return nil
	})
	if response["type"] != models.AgentEventTunnelError {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response["request_id"] != "req_1" {
		t.Fatalf("missing request correlation: %#v", response)
	}
}

// startTunnelTestPikoServer 启动进程内 piko server(app/tunnel e2e 同款
// 配方),返回上游面 URL——帧测试只需上游真连通让 configure 快速落 connected;
// 帧里的 piko_proxy_url 在 configure 期间从不被拨,取任意值都安全。
func startTunnelTestPikoServer(t *testing.T) string {
	t.Helper()
	cfg := pikoconfig.Default()
	cfg.Cluster.NodeID = fmt.Sprintf("svc-test-%d", time.Now().UnixNano())
	cfg.Proxy.BindAddr = "127.0.0.1:0"
	cfg.Upstream.BindAddr = "127.0.0.1:0"
	cfg.Admin.BindAddr = "127.0.0.1:0"
	cfg.Cluster.Gossip.BindAddr = "127.0.0.1:0"
	cfg.GracePeriod = 500 * time.Millisecond
	srv, err := pikoserver.NewServer(cfg, pikolog.NewNopLogger())
	if err != nil {
		t.Fatalf("piko server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("piko server start: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	return "http://" + cfg.Upstream.AdvertiseAddr
}

// configureTunnelWithPikoProxyURL 跑一遍 configure 帧(上游指进程内 piko
// server,秒级 connected),返回 manager 供读回帧里的 piko_proxy_url 是否
// 进了 manager 当前配置。
func configureTunnelWithPikoProxyURL(t *testing.T, proxyURL string) *tunnel.Manager {
	t.Helper()
	manager := tunnel.NewManager(nil)
	t.Cleanup(manager.Stop)
	svc := &AgentService{
		identity: &agentDeviceIdentity{DeviceID: "dev_local"},
		state:    agentState{DeviceID: "dev_local", Enabled: true, Registered: true},
		tunnel:   manager,
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc.configureTunnel(map[string]interface{}{
		"request_id":        "req_1",
		"device_id":         "dev_local",
		"piko_upstream_url": startTunnelTestPikoServer(t),
		"piko_proxy_url":    proxyURL,
		"tunnel_token":      "test-token",
		"route_public_key":  base64.StdEncoding.EncodeToString(publicKey),
		"expires_at":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}, func(payload interface{}) error { return nil })
	return manager
}

// TestConfigureTunnelPassesPikoProxyURLToManager configure 帧可选键
// piko_proxy_url 必须进 manager 当前配置(Task 9:中继载体 A 侧)。
func TestConfigureTunnelPassesPikoProxyURLToManager(t *testing.T) {
	manager := configureTunnelWithPikoProxyURL(t, "https://piko.example.com:8000")
	if got := manager.MeshRelayProxyURL(); got != "https://piko.example.com:8000" {
		t.Fatalf("MeshRelayProxyURL = %q, want the framed piko_proxy_url", got)
	}
}

// TestConfigureTunnelSoftFailsIllegalPikoProxyURL 非法 proxy url(非回环
// http)不得拒绝整个 configure:中继载体软失败为不可用。
func TestConfigureTunnelSoftFailsIllegalPikoProxyURL(t *testing.T) {
	manager := configureTunnelWithPikoProxyURL(t, "http://192.168.1.5:8000")
	if got := manager.MeshRelayProxyURL(); got != "" {
		t.Fatalf("MeshRelayProxyURL = %q, want empty soft-fail for illegal proxy url", got)
	}
}

func TestTunnelConfigureRequiresEnabledDevice(t *testing.T) {
	if !remoteAgentMessageRequiresEnabledDevice(models.AgentEventTunnelConfigure) {
		t.Fatal("tunnel.configure must require an enabled, registered device")
	}
}

func TestAgentCapabilitiesAdvertiseHTTPAndWebSocketTunnels(t *testing.T) {
	capabilities := agentCapabilities()
	if !stringSliceContains(capabilities, "http_tunnel_v1") {
		t.Fatal("http_tunnel_v1 capability is missing")
	}
	if !stringSliceContains(capabilities, "websocket_tunnel_v1") {
		t.Fatal("websocket_tunnel_v1 capability is missing")
	}
}
