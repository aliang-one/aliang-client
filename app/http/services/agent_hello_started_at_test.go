package services

import (
	"testing"
	"time"
)

// TestAgentHelloStartedAtIsProcessStable 锁死 hello 的 started_at 语义:它必须是
// agent 进程的启动时刻,而不是本次 hello 的构建时刻。
//
// 服务端用它识别 agent 重启(RUN-01/02 的云端重启感知):同一进程的重连、
// digest 重发、心跳全量锚点都必须恒等。此前这里写的是 time.Now(),导致每次
// hello 的 started_at 都不同——服务端会把每一次重连都当成重启,把在途回合和
// 待审批误杀(2026-11-28 线上实测:服务端一次滚动更新后 4 台设备各被「检测
// 重启」2~3 次,实为重连)。
func TestAgentHelloStartedAtIsProcessStable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	s := NewAgentService()

	first := s.agentHelloPayload()
	firstAt, ok := first["started_at"].(string)
	if !ok || firstAt == "" {
		t.Fatalf("started_at 必须是非空字符串, got %#v", first["started_at"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, firstAt)
	if err != nil {
		t.Fatalf("started_at 必须可按 RFC3339Nano 解析: %v", err)
	}
	if !parsed.Equal(agentProcessStartedAt.UTC().Truncate(time.Nanosecond)) &&
		parsed.Unix() != agentProcessStartedAt.Unix() {
		t.Fatalf("started_at 应为进程启动时刻 %s, got %s", agentProcessStartedAt.UTC(), parsed)
	}

	// 间隔后再次构建:同一进程必须恒等。
	time.Sleep(1100 * time.Millisecond)
	second := s.agentHelloPayload()
	if second["started_at"] != firstAt {
		t.Fatalf("同一进程的两次 hello started_at 必须相同: first=%s second=%s", firstAt, second["started_at"])
	}
}
