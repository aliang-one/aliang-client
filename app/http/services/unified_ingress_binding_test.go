package services

// ReconcileUnifiedIngressBinding 的语义钉子(spec §6.2 / D2「绑定跟随启用面」,
// 判据修订见 SPEC §5.5):
// 56432 的绑定地址由 ssh_ingress 开关决定(ssh 开 → 0.0.0.0,关/未配 →
// 回环),而 listener 只在 StartMitmHttp 的绑定瞬间读一次地址 —— 开/关入口
// 若只改配置不重绑,新暴露面/收窄面都不生效。
// 本文件钉三件事:该重绑时重绑、不该重绑时一动不动、以及「代理没在跑」时
// 绝不越权起口(那是 run service 的状态机职责,D4「停代理=停监听」)。

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// ingressBindingProbe 是可注入的运行面替身:running/addr 表当前 listener
// 实况,stops/starts 记 reconcile 的副作用次数。starts 走原子量,因为实现里
// 起口在 goroutine 中(start 是阻塞的 accept 循环)。
type ingressBindingProbe struct {
	mu      sync.Mutex
	running bool
	addr    string
	stops   int32
	starts  int32
}

// install 把包级缝换成替身,测试结束自动还原。起口缝注入 httpRestartRunner
// (评审 I1:reconcile 用容忍变体,不共用 boot 的 Fatal 路径)。
func (p *ingressBindingProbe) install(t *testing.T) {
	t.Helper()
	prevRunning, prevAddr := httpProxyIsRunningProbe, unifiedIngressListenAddrProbe
	prevStop, prevRestart := httpStopRunner, httpRestartRunner
	t.Cleanup(func() {
		httpProxyIsRunningProbe = prevRunning
		unifiedIngressListenAddrProbe = prevAddr
		httpStopRunner = prevStop
		httpRestartRunner = prevRestart
	})

	httpProxyIsRunningProbe = func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.running
	}
	unifiedIngressListenAddrProbe = func() string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.addr
	}
	httpStopRunner = func() {
		atomic.AddInt32(&p.stops, 1)
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}
	httpRestartRunner = func() {
		atomic.AddInt32(&p.starts, 1)
	}
}

// waitStarts 等 goroutine 里的起口落到原子量(至多 2s)。
func (p *ingressBindingProbe) waitStarts(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&p.starts) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("start 次数 = %d, 期望 %d", atomic.LoadInt32(&p.starts), want)
}

// meshConfigForBinding 构造只带 mesh 总开关的最小配置(ssh 未配=nil=关)。
func meshConfigForBinding(enabled *bool) *config.Config {
	return &config.Config{Customer: &config.CustomerConfig{
		Mesh: &config.MeshConfig{Enable: enabled},
	}}
}

// sshConfigForBinding 构造只带 ssh_ingress 开关的最小配置(绑定面判据,
// SPEC §5.5)。
func sshConfigForBinding(enabled *bool) *config.Config {
	return &config.Config{Customer: &config.CustomerConfig{
		SSHIngress: &config.SSHIngressConfig{Enable: enabled},
	}}
}

func boolPtrForBinding(v bool) *bool { return &v }

// TestReconcileUnifiedIngressBindingRebindsOnSSHIngressSwitch 绑定地址与期望
// 不符且 listener 在跑 → 停+起各一次(开入口把回环放宽到 0.0.0.0 的主路径)。
func TestReconcileUnifiedIngressBindingRebindsOnSSHIngressSwitch(t *testing.T) {
	probe := &ingressBindingProbe{}
	probe.install(t)
	probe.running = true
	probe.addr = "127.0.0.1:56432"

	ReconcileUnifiedIngressBinding(sshConfigForBinding(boolPtrForBinding(true)))

	if got := atomic.LoadInt32(&probe.stops); got != 1 {
		t.Fatalf("stop 次数 = %d, want 1", got)
	}
	probe.waitStarts(t, 1)
}

// TestReconcileUnifiedIngressBindingNarrowsLoopbackWhenMeshOnSSHOff SPEC §5.5
// 的语义反转点:mesh 开 + ssh 关时组网在新模型下本就不生效
// (MeshEffectivelyEnabled 恒 false),旧判据(mesh 开 → 0.0.0.0)下这个
// 组合会白绑 0.0.0.0 白暴露 HTTP 代理面;新判据把它收回回环。
func TestReconcileUnifiedIngressBindingNarrowsLoopbackWhenMeshOnSSHOff(t *testing.T) {
	probe := &ingressBindingProbe{}
	probe.install(t)
	probe.running = true
	probe.addr = "0.0.0.0:56432"

	ReconcileUnifiedIngressBinding(meshConfigForBinding(boolPtrForBinding(true)))

	if got := atomic.LoadInt32(&probe.stops); got != 1 {
		t.Fatalf("mesh 开 + ssh 关应收窄回环并重绑,stop 次数 = %d", got)
	}
	probe.waitStarts(t, 1)
}

// TestReconcileUnifiedIngressBindingNoopWhenAddrMatches 地址一致时绝不抖动:
// 重复提交同配置是最常见路径,重启会无谓断开 accept 面。
func TestReconcileUnifiedIngressBindingNoopWhenAddrMatches(t *testing.T) {
	probe := &ingressBindingProbe{}
	probe.install(t)
	probe.running = true
	probe.addr = "0.0.0.0:56432"

	ReconcileUnifiedIngressBinding(sshConfigForBinding(boolPtrForBinding(true)))

	time.Sleep(50 * time.Millisecond) // 给可能的异步起口留窗口,再断言为零
	if got := atomic.LoadInt32(&probe.stops); got != 0 {
		t.Fatalf("地址一致时不应 stop,实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&probe.starts); got != 0 {
		t.Fatalf("地址一致时不应 start,实际 %d 次", got)
	}
}

// TestReconcileUnifiedIngressBindingDoesNotStartWhenProxyStopped 代理没在跑
// (运行态清单里就没有 56432)→ 不越权起口:起口归 run service,否则「停代理
// =停监听」的 D4 契约会被配置提交悄悄破坏。
func TestReconcileUnifiedIngressBindingDoesNotStartWhenProxyStopped(t *testing.T) {
	probe := &ingressBindingProbe{}
	probe.install(t)
	probe.running = false
	probe.addr = "" // 未绑定

	ReconcileUnifiedIngressBinding(sshConfigForBinding(boolPtrForBinding(true)))

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&probe.starts); got != 0 {
		t.Fatalf("代理未运行时不应越权起口,实际 start %d 次", got)
	}
	if got := atomic.LoadInt32(&probe.stops); got != 0 {
		t.Fatalf("代理未运行时不应 stop,实际 %d 次", got)
	}
}

// TestReconcileUnifiedIngressBindingNilConfigFallsBackToLoopback nil 配置
// (未配置 mesh)按「不开组网」处理:期望回环,若当前绑在 0.0.0.0 则收窄重绑。
func TestReconcileUnifiedIngressBindingNilConfigFallsBackToLoopback(t *testing.T) {
	probe := &ingressBindingProbe{}
	probe.install(t)
	probe.running = true
	probe.addr = "0.0.0.0:56432"

	ReconcileUnifiedIngressBinding(nil)

	if got := atomic.LoadInt32(&probe.stops); got != 1 {
		t.Fatalf("nil 配置下应收窄回环并重绑,stop 次数 = %d", got)
	}
	probe.waitStarts(t, 1)
}

// ===== 可选专用口:翻译层哨兵映射(spec §6.4 修订) =====

// TestEffectivePortForRunConfigSentinel 端口哨兵翻译:0/56442(旧默认)→0
// (跟随统一口,存量配置升级不凭空开专用口);其他显式值原样(开专用口)。
func TestEffectivePortForRunConfigSentinel(t *testing.T) {
	cases := []struct {
		name string
		in   uint16
		want uint16
	}{
		{"未配归零", 0, 0},
		{"旧默认哨兵归零", 56442, 0},
		{"显式专用口原样", 56443, 56443},
		{"其他显式值原样", 2222, 2222},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectivePortForRunConfig(tc.in); got != tc.want {
				t.Fatalf("effectivePortForRunConfig(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
