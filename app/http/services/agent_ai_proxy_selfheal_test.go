package services

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 56432 run service 运行态不持久化:core/owner 重启后无监听,所有手机 AI run
// 连接被拒(10-13/10-15 两次事故)。spawn 前置守卫:探测平台代理,不可达先
// 自愈拉起一次,仍失败则人话快速失败——替代 CLI 侧 10 次重试 3 分钟后的
// error_during_execution/Connection refused。

func TestEnsureAIUpstreamReachableFirstDialOK(t *testing.T) {
	heals := 0
	err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", aiProxyGuardDeps{
		dial:  func(context.Context, string) error { return nil },
		heal:  func() error { heals++; return nil },
		sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("可达时应直接放行: %v", err)
	}
	if heals != 0 {
		t.Errorf("可达时不应触发自愈: %d", heals)
	}
}

func TestEnsureAIUpstreamReachableHealsLoopbackRefusal(t *testing.T) {
	refusals := 0
	heals := 0
	err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", aiProxyGuardDeps{
		dial: func(context.Context, string) error {
			refusals++
			if refusals == 1 {
				return context.DeadlineExceeded // 首测拒绝(代理未起)
			}
			return nil // 自愈后恢复
		},
		heal:  func() error { heals++; return nil },
		sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("自愈成功应放行: %v", err)
	}
	if heals != 1 || refusals != 2 {
		t.Errorf("应恰好自愈一次并复测: heals=%d dials=%d", heals, refusals)
	}
}

func TestEnsureAIUpstreamReachableHealFailsFastWithHumanMessage(t *testing.T) {
	err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", aiProxyGuardDeps{
		dial:  func(context.Context, string) error { return context.DeadlineExceeded },
		heal:  func() error { return nil },
		sleep: func(time.Duration) {},
	})
	if err == nil {
		t.Fatal("自愈后仍不可达应报错")
	}
	if !strings.Contains(err.Error(), "平台 AI 代理未运行") {
		t.Errorf("错误应为人话(平台 AI 代理未运行): %v", err)
	}
	if !strings.Contains(err.Error(), "56432") {
		t.Errorf("错误应包含目标地址便于定位: %v", err)
	}
}

func TestEnsureAIUpstreamReachableSkipsNonLoopback(t *testing.T) {
	heals := 0
	err := ensureAIUpstreamReachable(context.Background(), "https://open.bigmodel.cn/api", aiProxyGuardDeps{
		dial:  func(context.Context, string) error { return context.DeadlineExceeded },
		heal:  func() error { heals++; return nil },
		sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Errorf("非回环上游的瞬态失败应交由 CLI 重试,不应快速失败: %v", err)
	}
	if heals != 0 {
		t.Errorf("非回环上游不属于本机 run service,不应自愈: %d", heals)
	}
}

func TestEnsureAIUpstreamReachableToleratesUnparsableAndEmpty(t *testing.T) {
	heals := 0
	deps := aiProxyGuardDeps{
		dial:  func(context.Context, string) error { return nil },
		heal:  func() error { heals++; return nil },
		sleep: func(time.Duration) {},
	}
	if err := ensureAIUpstreamReachable(context.Background(), "", deps); err != nil || heals != 0 {
		t.Errorf("空 baseURL 应放行且不自愈: err=%v heals=%d", err, heals)
	}
	if err := ensureAIUpstreamReachable(context.Background(), "::::not-a-url", deps); err != nil || heals != 0 {
		t.Errorf("无法解析的 baseURL 应放行且不自愈: err=%v heals=%d", err, heals)
	}
}

func TestStartProxyWhenSessionReady(t *testing.T) {
	t.Run("认证就绪即拉起且只拉一次", func(t *testing.T) {
		ready := false
		starts := 0
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(20 * time.Millisecond); ready = true }()
		StartProxyWhenSessionReady(ctx, time.Millisecond, func() bool { return ready }, func() { starts++ })
		cancel()
		if starts != 1 {
			t.Errorf("应恰好拉起一次, got %d", starts)
		}
	})
	t.Run("始终未就绪则等 ctx 退出且不拉起", func(t *testing.T) {
		starts := 0
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(30 * time.Millisecond); cancel() }()
		StartProxyWhenSessionReady(ctx, time.Millisecond, func() bool { return false }, func() { starts++ })
		if starts != 0 {
			t.Errorf("未就绪不应拉起: %d", starts)
		}
	})
}
