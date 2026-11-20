package ingressfwd

import (
	"net"
	"testing"
	"time"

	"aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
)

// sshRunningForTest 测试进程通常无 reconcile(单例缺席),但防御其他测试
// 意外起服导致决策矩阵不可测——此时跳过而非误报。
func sshRunningForTest() bool { return ssh.IsRunning() }

func enabledCfg(port uint16) *config.Config {
	enable := true
	return &config.Config{
		Customer: &config.CustomerConfig{
			SSHIngress: &config.SSHIngressConfig{
				Enable: &enable,
				Port:   port,
			},
		},
	}
}

func TestTarget(t *testing.T) {
	t.Parallel()
	if got := Target(nil); got != "" {
		t.Fatalf("nil cfg → 空目标, got %q", got)
	}
	if got := Target(&config.Config{}); got != "" {
		t.Fatalf("入口未启用 → 空目标(无处可转), got %q", got)
	}
	disabled := false
	if got := Target(&config.Config{Customer: &config.CustomerConfig{
		SSHIngress: &config.SSHIngressConfig{Enable: &disabled},
	}}); got != "" {
		t.Fatalf("显式关 → 空目标, got %q", got)
	}
	// 默认形态:跟随统一口 56432
	if got := Target(enabledCfg(0)); got != "127.0.0.1:56432" {
		t.Fatalf("port=0 → 跟随统一口, got %q", got)
	}
	// 专用口形态:core 侧专用 listener
	if got := Target(enabledCfg(56499)); got != "127.0.0.1:56499" {
		t.Fatalf("专用口 → 127.0.0.1:56499, got %q", got)
	}
}

// Splice 契约:c ↔ addr 双向透传;对端关闭后 c 随之关闭;无数据损耗。
func TestSpliceBidirectional(t *testing.T) {
	t.Parallel()
	// 上游 = 本地 echo server(扮演 core 统一入口的 SSH ingress)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upConnCh := make(chan net.Conn, 1)
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		upConnCh <- c
		// echo 一切回 c
		buf := make([]byte, 512)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	clientSide, peerSide := net.Pipe()
	errCh := make(chan error, 1)
	go func() { errCh <- Splice(clientSide, upstream.Addr().String()) }()

	up := <-upConnCh
	// c → 上游 → echo → c
	if _, err := peerSide.Write([]byte("ping")); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	peerSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	n, err := peerSide.Read(buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("echo mismatch: %q", buf[:n])
	}
	// 上游主动关 → Splice 收尾 → c 关闭
	_ = up.Close()
	select {
	case err := <-errCh:
		if err == nil {
			t.Log("splice returned nil on upstream close(允许)")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("splice 未在上游关闭后收尾")
	}
	peerSide.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peerSide.Read(buf); err == nil {
		t.Fatal("splice 收尾后 c 仍可读 = 未关闭")
	}
}

// ServeOrForward 决策矩阵:入口未启用 → 关闭(读侧立即见 EOF)。
func TestServeOrForwardDropsWhenDisabled(t *testing.T) {
	t.Parallel()
	if sshRunningForTest() {
		t.Skip("本测试进程恰有 ingress 单例在跑,决策矩阵不可测")
	}
	clientSide, peerSide := net.Pipe()
	got := ServeOrForward(clientSide, &config.Config{})
	if got {
		t.Fatal("入口未启用应返回 false(未在本进程 serve)")
	}
	peerSide.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if _, err := peerSide.Read(buf); err == nil {
		t.Fatal("drop 路径未关闭连接")
	}
}

// ServeOrForward 转发路径:目标=本地 echo,写入即应回(证明字节过了 splice)。
func TestServeOrForwardForwardsToCore(t *testing.T) {
	t.Parallel()
	if sshRunningForTest() {
		t.Skip("本测试进程恰有 ingress 单例在跑,决策矩阵不可测")
	}
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 512)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	// 伪 core 统一入口地址:借 config 携带专用口 = echo 端口
	port := uint16(upstream.Addr().(*net.TCPAddr).Port)
	clientSide, peerSide := net.Pipe()
	got := ServeOrForward(clientSide, enabledCfg(port))
	if got {
		t.Fatal("本进程无单例,应走转发分支返回 false")
	}
	if _, err := peerSide.Write([]byte("relay-ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	peerSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 32)
	n, err := peerSide.Read(buf)
	if err != nil {
		t.Fatalf("read forwarded echo: %v", err)
	}
	if string(buf[:n]) != "relay-ping" {
		t.Fatalf("forwarded payload mismatch: %q", buf[:n])
	}
}
