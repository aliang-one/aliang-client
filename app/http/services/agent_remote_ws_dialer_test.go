package services

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 2026-11-22 生产事故拨号面回归。事实核对(对抗评审纠正):gorilla/websocket
// v1.5.3(已钉版)的 DefaultDialer 本就带 Proxy: http.ProxyFromEnvironment 与
// HandshakeTimeout: 45s——当日「第 5 次 dialing 后 2h+ 无 connected/dial_failed」
// 并非零超时挂死,而是 WS 会话 goroutine 停在被焊死的 AgentService.s.mu 上
// (handleHandshakeUnauthorized→NotifyOwnerAuthRejected→currentAgentDeviceIDFor
// Notify(修复前 s.mu.Lock)),由锁纪律修复(见 agent_channel_lock_reentry_test.go)
// 根治。本文件钉死显式拨号器的两条不变量:
// ① 代理语义必须保留(ProxyFromEnvironment)——否则仅代理可达出口的环境会出现
//   「REST 注册成功 / WS 永久重连失败」的不对称;
// ② 握手超时必须为正且在装聋 server 上真的会触发(防回归回退成无界 Dial)。

// TestAgentRemoteWSDialerHasHandshakeTimeoutAndProxy 结构级:生产拨号器必须
// 带正握手超时与代理语义(与 DefaultDialer 同形,显式书写防静默回退)。
func TestAgentRemoteWSDialerHasHandshakeTimeoutAndProxy(t *testing.T) {
	if agentRemoteWSDialer.HandshakeTimeout <= 0 {
		t.Fatalf("agentRemoteWSDialer.HandshakeTimeout = %v, 必须为正(无界 Dial 在握手挂死时永久 park)", agentRemoteWSDialer.HandshakeTimeout)
	}
	if agentRemoteWSDialer.Proxy == nil {
		t.Fatal("agentRemoteWSDialer.Proxy = nil:静默丢掉代理语义,仅代理可达出口的环境 REST 成功而 WS 永久重连失败(2026-11-22 评审 must_fix)")
	}
}

// TestAgentRemoteWSDialerHandshakeTimeoutActuallyFires 行为级:accept 后装聋
// (不回 HTTP 升级响应),Dial 必须在配置超时+余量内失败返回,而非挂死。
func TestAgentRemoteWSDialerHandshakeTimeoutActuallyFires(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // 收下连接后装聋:不读不写,HTTP 升级握手永不完成
		}
	}()

	d := newAgentRemoteWSDialer(300 * time.Millisecond)
	start := time.Now()
	_, _, err = d.Dial("ws://"+ln.Addr().String(), nil)
	if err == nil {
		t.Fatal("装聋 server 上 Dial 竟然成功,握手超时未生效")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Dial 耗时 %v 才返回,握手超时未按配置生效", elapsed)
	}
}

// TestAgentRemoteWSDialUsesExplicitDialer 源码级接线审计(route_parity 同款
// 手法):生产拨号点必须走 agentRemoteWSDialer,不得静默回退 DefaultDialer——
// 回退会同时丢掉本文件的显式超时与审计锚点。
func TestAgentRemoteWSDialUsesExplicitDialer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("agent_remote_ws.go"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	if re := regexp.MustCompile(`websocket\.DefaultDialer\.Dial\(`); re.MatchString(code.String()) {
		t.Fatal("agent_remote_ws.go 仍在使用 websocket.DefaultDialer.Dial:静默回退,拨号器不变量失去绑定")
	}
	if re := regexp.MustCompile(`agentRemoteWSDialer\.Dial\(`); !re.MatchString(code.String()) {
		t.Fatal("agent_remote_ws.go 未走 agentRemoteWSDialer.Dial:接线漂移")
	}
}
