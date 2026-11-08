package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	pikoclient "github.com/andydunstall/piko/client"
	pikolog "github.com/andydunstall/piko/pkg/log"
	pikoserver "github.com/andydunstall/piko/server"
	pikoconfig "github.com/andydunstall/piko/server/config"

	"aliang.one/nursorgate/processor/tcp"
)

// restoreMeshDialSeams 归位 validateConfig 会写的两条全局注入缝(selfID +
// pikoDial,见 manager.go validateConfig),防止跨测试泄漏(与
// restoreMeshSelfIDSeam 同款约定)。
func restoreMeshDialSeams(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		tcp.SetMeshSelfIDSource(nil)
		tcp.SetMeshPikoDialSource(nil)
	})
}

// startTestPikoServer 启动进程内 piko server(Task 5 spike 配方:回环 :0 绑定
// 四端口 + NopLogger),返回代理面/上游面 URL。NewServer 会把 :0 绑定的实际
// 地址写回 cfg 的 AdvertiseAddr。
func startTestPikoServer(t *testing.T) (proxyURL, upstreamURL *url.URL) {
	t.Helper()
	cfg := pikoconfig.Default()
	cfg.Cluster.NodeID = fmt.Sprintf("test-%d", time.Now().UnixNano())
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
	proxyURL = &url.URL{Scheme: "http", Host: cfg.Proxy.AdvertiseAddr}
	upstreamURL = &url.URL{Scheme: "http", Host: cfg.Upstream.AdvertiseAddr}
	return proxyURL, upstreamURL
}

// serveMeshEcho 在指定 piko server 的上游面监听 endpointID,每收到一段载荷即回
// marker+载荷(单读单写;调用方两次往返覆盖双向字节流)。返回停止函数。
func serveMeshEcho(t *testing.T, ctx context.Context, upstreamURL *url.URL, endpointID, marker string) {
	t.Helper()
	up := &pikoclient.Upstream{URL: upstreamURL, Token: "test-token"}
	ln, err := up.Listen(ctx, endpointID)
	if err != nil {
		t.Fatalf("upstream listen %s: %v", endpointID, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 512)
				for {
					_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
					n, err := c.Read(buf)
					if n > 0 {
						_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
						if _, werr := c.Write([]byte(marker + string(buf[:n]))); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
}

// newMeshDialManager 构造 run 打桩的 manager(不真连上游),并 Configure 到
// connected;返回 manager 与状态通道。
func newMeshDialManager(t *testing.T, config Config) (*Manager, <-chan Status) {
	t.Helper()
	statuses := make(chan Status, 16)
	manager := NewManager(func(status Status) { statuses <- status })
	manager.run = func(ctx context.Context, _ runConfig, ready func()) error {
		ready()
		<-ctx.Done()
		return nil
	}
	status, _, err := manager.Configure(config)
	if err != nil {
		t.Fatalf("configure: %v (status %+v)", err, status)
	}
	waitForState(t, statuses, "connected")
	t.Cleanup(manager.Stop)
	return manager, statuses
}

// TestDialPeerMeshRelayEndToEnd 档 a 真集成:进程内 piko server 上 A 侧
// DialPeerMesh 拨 <dev>.mesh 端点,B 侧 Upstream.Listen 回显——双向字节往返,
// 同时证明 endpointID 装配(<deviceID>.mesh)与 proxy URL 装配(拨的就是该
// server 的代理面)。
func TestDialPeerMeshRelayEndToEnd(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	proxyURL, upstreamURL := startTestPikoServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveMeshEcho(t, ctx, upstreamURL, "dev-b.mesh", "echo:")

	config := validTestConfig(t)
	config.PikoProxyURL = proxyURL.String()
	manager, _ := newMeshDialManager(t, config)

	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	conn, err := manager.DialPeerMesh(dialCtx, "dev-b")
	if err != nil {
		t.Fatalf("DialPeerMesh: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	for _, payload := range []string{"ping-a\n", "ping-b\n"} {
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("write %q: %v", payload, err)
		}
		want := "echo:" + payload
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("read after %q: %v", payload, err)
		}
		if string(got) != want {
			t.Fatalf("roundtrip = %q, want %q", got, want)
		}
	}
}

// TestDialPeerMeshReconfigureUsesCurrentProxyURL 钉死陈旧配置防线:中继拨号
// 必须读 manager 当前配置而非接线闭包捕获的某代配置——两台 piko server,续期
// Configure(仅换 piko_proxy_url,fingerprint 不含该键 → changed=false 幂等
// 路径)后新拨号必须落在新 URL 的 server 上。
func TestDialPeerMeshReconfigureUsesCurrentProxyURL(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	proxy1, upstream1 := startTestPikoServer(t)
	proxy2, upstream2 := startTestPikoServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveMeshEcho(t, ctx, upstream1, "dev-b.mesh", "one:")
	serveMeshEcho(t, ctx, upstream2, "dev-b.mesh", "two:")

	config := validTestConfig(t)
	config.PikoProxyURL = proxy1.String()
	manager, _ := newMeshDialManager(t, config)

	dialAndExpect := func(wantMarker string) {
		t.Helper()
		dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
		defer dialCancel()
		conn, err := manager.DialPeerMesh(dialCtx, "dev-b")
		if err != nil {
			t.Fatalf("DialPeerMesh (want %q): %v", wantMarker, err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("hi\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		want := wantMarker + "hi\n"
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("read (want %q): %v", wantMarker, err)
		}
		if string(got) != want {
			t.Fatalf("dial routed to %q, want marker %q", got, want)
		}
	}

	dialAndExpect("one:")

	config.PikoProxyURL = proxy2.String()
	if _, changed, err := manager.Configure(config); err != nil || changed {
		t.Fatalf("renewal Configure = (changed=%t, err=%v); proxy url must not be in the fingerprint", changed, err)
	}
	// 幂等续期不发新状态事件:直接核对 manager 状态仍为 connected。
	if state := manager.Status().State; state != "connected" {
		t.Fatalf("manager state after idempotent renewal = %q, want connected", state)
	}
	dialAndExpect("two:")
}

// TestDialPeerMeshUnavailableWithoutProxyURL 软失败契约:proxy url 缺省
// (平台未下发)时中继载体不可用,返回明确错误而非空指针。
func TestDialPeerMeshUnavailableWithoutProxyURL(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	manager, _ := newMeshDialManager(t, validTestConfig(t))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := manager.DialPeerMesh(ctx, "dev-b")
	if err == nil {
		t.Fatal("expected dial to fail without a configured proxy url")
	}
	if !strings.Contains(err.Error(), "mesh relay carrier unavailable") {
		t.Fatalf("error = %q, want mesh relay carrier unavailable", err)
	}
	if !strings.Contains(err.Error(), "proxy url not configured") {
		t.Fatalf("error = %q, want proxy url not configured", err)
	}
}

// TestValidateConfigStoresPikoProxyURLAndToken 白盒:合法 proxy url 与 token
// 原子存入 manager 当前配置(DialPeerMesh 的取读点)。
func TestValidateConfigStoresPikoProxyURLAndToken(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	manager := NewManager(nil)
	config := validTestConfig(t)
	config.PikoProxyURL = "https://piko.example.com:8000"
	if _, _, err := manager.validateConfig(config); err != nil {
		t.Fatal(err)
	}
	if got := manager.MeshRelayProxyURL(); got != "https://piko.example.com:8000" {
		t.Fatalf("MeshRelayProxyURL = %q", got)
	}
	manager.mu.Lock()
	token := manager.meshDial.token
	manager.mu.Unlock()
	if token != config.TunnelToken {
		t.Fatalf("mesh dial token = %q, want %q", token, config.TunnelToken)
	}
}

// TestValidateConfigSoftFailsIllegalProxyURL 非法 proxy url(非回环 http)不
// 拒绝整个 configure:中继载体软失败为不可用,隧道照常配置。
func TestValidateConfigSoftFailsIllegalProxyURL(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	manager := NewManager(nil)
	config := validTestConfig(t)
	config.PikoProxyURL = "http://192.168.1.5:8000"
	if _, _, err := manager.validateConfig(config); err != nil {
		t.Fatalf("illegal proxy url must soft-fail, got configure error: %v", err)
	}
	if got := manager.MeshRelayProxyURL(); got != "" {
		t.Fatalf("MeshRelayProxyURL = %q, want empty soft-fail", got)
	}
}

// TestManagerStopClearsMeshDial Stop 后中继载体随会话一并收口:当前配置清
// 空,拨号回落不可用错误。
func TestManagerStopClearsMeshDial(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	config := validTestConfig(t)
	config.PikoProxyURL = "https://piko.example.com:8000"
	manager, _ := newMeshDialManager(t, config)
	if got := manager.MeshRelayProxyURL(); got == "" {
		t.Fatal("proxy url must be configured before Stop")
	}
	manager.Stop()
	if got := manager.MeshRelayProxyURL(); got != "" {
		t.Fatalf("MeshRelayProxyURL after Stop = %q, want empty", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := manager.DialPeerMesh(ctx, "dev-b"); err == nil || !strings.Contains(err.Error(), "mesh relay carrier unavailable") {
		t.Fatalf("dial after Stop = %v, want unavailable error", err)
	}
}

// TestManagerStopAfterSelfFailureClearsMeshDial 会话自败(updateGeneration 已
// 清 cancel)后的 Stop 走早退路径,也必须收口中继载体——Stop 之后中继载体
// 一律不可用。
func TestManagerStopAfterSelfFailureClearsMeshDial(t *testing.T) {
	t.Parallel()
	restoreMeshDialSeams(t)
	statuses := make(chan Status, 16)
	manager := NewManager(func(status Status) { statuses <- status })
	manager.run = func(context.Context, runConfig, func()) error {
		return errors.New("upstream gone")
	}
	config := validTestConfig(t)
	config.PikoProxyURL = "https://piko.example.com:8000"
	if _, _, err := manager.Configure(config); err != nil {
		t.Fatal(err)
	}
	waitForState(t, statuses, "failed")
	if got := manager.MeshRelayProxyURL(); got == "" {
		t.Fatal("proxy url must remain configured while only the session failed")
	}
	manager.Stop()
	if got := manager.MeshRelayProxyURL(); got != "" {
		t.Fatalf("MeshRelayProxyURL after post-failure Stop = %q, want empty", got)
	}
}
