package services

import (
	"context"
	"time"
)

// StartProxyWhenSessionReady 平台代理自愈:56432 run service 运行态不持久化,
// core/owner 重启后若无人手动 run/start,所有手机 AI run 会 Connection refused
// (10-13/10-15 两次事故)。轮询等待会话权威具备代理能力(认证就绪)后拉起一次;
// ctx 取消(core 关停)时静默退出。仅 boot 后首次就绪的边沿触发——此后用户
// 显式 stop 不被对抗。
func StartProxyWhenSessionReady(ctx context.Context, interval time.Duration, canProxy func() bool, start func()) {
	if canProxy() {
		start()
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if canProxy() {
				start()
				return
			}
		}
	}
}
