package services

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// 56432 run service 的运行态不持久化:core/owner 重启后若无人手动
// run/start,代理无监听,所有手机 AI run 的 claude 连 ANTHROPIC_BASE_URL
// 直接 Connection refused(10-13/10-15 两次事故)。本守卫在 spawn 前探测
// 上游可达性:不可达且指向本机回环(run-service 形态)时尝试自愈拉起一次,
// 仍失败则以人话快速失败——替代 CLI 侧 10 次重试 3 分钟后的
// error_during_execution/Connection refused。非回环上游(用户自配的远程
// 端点)的瞬态失败不归本守卫管,交由 CLI 既有重试。

type aiProxyGuardDeps struct {
	// dial 探测上游 TCP 可达性。err==nil 视为可达。
	dial func(ctx context.Context, hostPort string) error
	// heal 尝试自愈拉起本机 run service(POST 56431/api/run/start)。
	heal func() error
	// sleep 自愈后复测前的等待。
	sleep func(time.Duration)
}

// ensureAIUpstreamReachable 校验 baseURL 可达;本机回环代理不可达时自愈一次。
// baseURL 为空或无法解析时放行(provider 缺失/自配端点的失败由 CLI 自行报)。
func ensureAIUpstreamReachable(ctx context.Context, baseURL string, deps aiProxyGuardDeps) error {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return nil
	}
	if err := deps.dial(ctx, u.Host); err == nil {
		return nil
	}
	if isLoopbackHost(u.Hostname()) {
		_ = deps.heal()
		deps.sleep(500 * time.Millisecond)
		if err := deps.dial(ctx, u.Host); err == nil {
			return nil
		}
		return fmt.Errorf("平台 AI 代理未运行(%s 连接被拒,已尝试自动拉起)——请在设备仪表盘启动代理后重试", u.Host)
	}
	// 非回环上游(用户自配远程端点):失败属网络瞬态/远端问题,交由 CLI 重试。
	return nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// ensureAIUpstreamReachableForRun 是 runCLIPass 的生产入口:探测 claude 子进程
// 将要使用的 ANTHROPIC_BASE_URL(=平台 56432 代理),必要时自愈。
func ensureAIUpstreamReachableForRun(ctx context.Context) error {
	base, _ := agentChildProcessEnvValue("ANTHROPIC_BASE_URL")
	base = strings.TrimSpace(base)
	return ensureAIUpstreamReachable(ctx, base, aiProxyGuardDeps{
		dial: func(ctx context.Context, hostPort string) error {
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", hostPort)
			if err != nil {
				return err
			}
			return conn.Close()
		},
		heal: func() error {
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Post("http://"+config.DefaultManagementAddr+"/api/run/start", "application/json", nil)
			if err != nil {
				return err
			}
			resp.Body.Close()
			return nil
		},
		sleep: time.Sleep,
	})
}
