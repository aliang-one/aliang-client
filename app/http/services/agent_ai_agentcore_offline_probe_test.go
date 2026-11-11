package services

// AI 上游探针离线降级测试(计划 Task 12,design §9.4):离线 mesh 改道激活
// 时 agentCoreEnsureUpstream 跳过上游可达性探针(不拨回环探针、不触发自愈
// 拉起)——离线场景下探针要么误报 upstream 不可达拒跑 AI,要么拉起一个
// 没有平台出口的本地代理;改道后真实链路经 mesh 兜底节点,可达性由数据面
// 拨号失败语义天然兜底。在线时探针行为保持现状。判定与数据面改道同源同
// 真假:直接调 processor/tcp.MeshOfflineAIRedirectActive(同一函数)。

import (
	"context"
	"net"
	"testing"
	"time"

	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/tcp"
)

// setAgentCoreOfflineProbeEnv 组装探针测试环境:回环探针目标(listener,
// accept 即「探针拨过」)+ AI 类兜底勾选配置 + 指定离线信号真值。
func setAgentCoreOfflineProbeEnv(t *testing.T) (accepted chan net.Conn) {
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

	// AI 类离线兜底勾选(改道激活的配置半边;信号半边由 swap 注入)。
	// SSHIngress 必须一并开启(SPEC §10.3 依赖门,c5f2879 起
	// MeshEffectivelyEnabled 追加 SSHIngress 检查):夹具只配 Mesh 块时
	// MeshOfflineAIRedirectActive 恒假,本文件全部用例随之失真。
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			SSHIngress: &config.SSHIngressConfig{
				Enable: boolPtr(true),
			},
			Mesh: &config.MeshConfig{
				Fallback: &config.MeshFallbackConfig{
					Enable:  boolPtr(true),
					Addr:    "192.168.1.9:56440",
					Classes: []string{meshproto.ClassAI},
				},
			},
		},
	})

	t.Cleanup(func() { tcp.SetMeshOfflineSignal(nil) })
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

// TestAIUpstreamProbeSkippedWhenOfflineMeshActive 离线激活 →
// agentCoreEnsureUpstream 直接通过(探针未拨);在线 → 现状探针(探针拨到
// 回环目标)。
func TestAIUpstreamProbeSkippedWhenOfflineMeshActive(t *testing.T) {
	t.Run("离线激活跳过探针", func(t *testing.T) {
		accepted := setAgentCoreOfflineProbeEnv(t)
		tcp.SetMeshOfflineSignal(func() bool { return true })

		if !tcp.MeshOfflineAIRedirectActive() {
			t.Fatal("离线+AI 兜底勾选时改道判定应为真(前置自检)")
		}
		if err := agentCoreEnsureUpstream(context.Background()); err != nil {
			t.Fatalf("agentCoreEnsureUpstream() = %v, want 离线激活直接通过", err)
		}
		expectProbeNotDialed(t, accepted, "离线激活时探针")
	})

	t.Run("在线保持现状探针", func(t *testing.T) {
		accepted := setAgentCoreOfflineProbeEnv(t)
		tcp.SetMeshOfflineSignal(func() bool { return false })

		if tcp.MeshOfflineAIRedirectActive() {
			t.Fatal("在线时改道判定应为假(前置自检)")
		}
		if err := agentCoreEnsureUpstream(context.Background()); err != nil {
			t.Fatalf("agentCoreEnsureUpstream() = %v, want 在线探针可达通过", err)
		}
		expectProbeDialed(t, accepted, "在线时探针")
	})
}
