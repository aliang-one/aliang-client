package services

// AI 上游探针降级测试(2026-11-12 出口链路改造,替代离线降级):出口链路
// 委托激活时 agentCoreEnsureUpstream 跳过上游可达性探针(不拨回环探针、
// 不触发自愈拉起)——委托激活时 AI 流量的真实出网路径是链上下一跳,本机
// 探针结果不再反映实际路径:要么误报 upstream 不可达拒跑 AI,要么拉起一个
// 没有平台出口的本地代理;委托关闭时探针行为保持现状。判定与数据面改道
// 同源同真假:直接调 processor/tcp.MeshDelegateActive(同一函数)。

import (
	"context"
	"net"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/tcp"
)

// setAgentCoreDelegateProbeEnv 组装探针测试环境:回环探针目标(listener,
// accept 即「探针拨过」)+ 出口链路开关开配置。
func setAgentCoreDelegateProbeEnv(t *testing.T) (accepted chan net.Conn) {
	t.Helper()

	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()

	// 探针目标 = 回环 listener(agentCoreIsLoopbackBase 放行,确保
	// agentCoreBaseURL 原样采用本值,探针是否拨过由 accept 观测)。
	t.Setenv("ANTHROPIC_BASE_URL", "http://"+ln.Addr().String())

	// 出口链路开关开(委托激活的配置半边):next_hop 指向已钉扎 peer。
	// SSHIngress 必须一并开启(SPEC §10.3 依赖门):夹具只配 Mesh 块时
	// MeshDelegateActive 恒假,本文件全部用例随之失真。
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			SSHIngress: &config.SSHIngressConfig{
				Enable: boolPtr(true),
			},
			Mesh: &config.MeshConfig{
				Peers: []config.MeshPeer{
					{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.9:56432"}, HostKey: "SHA256:AAAA"},
				},
				Fallback: &config.MeshFallbackConfig{
					Enable:  boolPtr(true),
					NextHop: "dev-b",
				},
			},
		},
	})

	return accepted
}

func expectProbeDialed(t *testing.T, accepted chan net.Conn, what string) {
	t.Helper()
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: 探针未拨(2s 超时)", what)
	}
}

func expectProbeNotDialed(t *testing.T, accepted chan net.Conn, what string) {
	t.Helper()
	select {
	case conn := <-accepted:
		_ = conn.Close()
		t.Fatalf("%s: 探针不应被拨", what)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestAIUpstreamProbeSkippedWhenDelegateActive 委托激活 →
// agentCoreEnsureUpstream 直接通过(探针未拨);委托关闭 → 现状探针(探针
// 拨到回环目标)。
func TestAIUpstreamProbeSkippedWhenDelegateActive(t *testing.T) {
	t.Run("委托激活跳过探针", func(t *testing.T) {
		accepted := setAgentCoreDelegateProbeEnv(t)

		if !tcp.MeshDelegateActive() {
			t.Fatal("开关开 + next_hop 齐备时委托判定应为真(前置自检)")
		}
		if err := agentCoreEnsureUpstream(context.Background()); err != nil {
			t.Fatalf("agentCoreEnsureUpstream() = %v, want 委托激活直接通过", err)
		}
		expectProbeNotDialed(t, accepted, "委托激活时探针")
	})

	t.Run("委托关闭保持现状探针", func(t *testing.T) {
		accepted := setAgentCoreDelegateProbeEnv(t)
		// 关掉开关(fallback.enable=false):委托判定翻假,探针恢复现状。
		swapGlobalCustomerMeshFallback(t, false)

		if tcp.MeshDelegateActive() {
			t.Fatal("开关关时委托判定应为假(前置自检)")
		}
		if err := agentCoreEnsureUpstream(context.Background()); err != nil {
			t.Fatalf("agentCoreEnsureUpstream() = %v, want 委托关闭探针可达通过", err)
		}
		expectProbeDialed(t, accepted, "委托关闭时探针")
	})
}

// swapGlobalCustomerMeshFallback 翻转当前全局配置里出口链路开关(读-改-写,
// 供「关闭态」对照用例在夹具之上做最小变异;测试非并行,直接换全局安全)。
func swapGlobalCustomerMeshFallback(t *testing.T, enable bool) {
	t.Helper()
	cfg := config.GetGlobalConfig()
	off := enable
	cfg.Customer.Mesh.Fallback.Enable = &off
	config.SetGlobalConfig(cfg)
	t.Cleanup(func() {})
}
