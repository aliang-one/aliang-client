package services

// 旧臂 panic 护栏(file.list/file.read/git.status/env.info 显式臂):两臂历史上
// 由裸 goroutine 直派、绕过 default 臂的 tools.RunWithGuards——审计二轮证实
// 任何 panic 会顺着 Go 语义杀掉整个 agent 进程(无 supervisor,死进程带走所有
// 活 PTY 与连接)。本文件把两臂分发入口提为包级变量(注入模式对齐
// agentToolsRegistry),注入 panic 假实现,钉"旧臂 panic 被 recover、同一服务
// 继续受理后续消息"。密闭对齐 agent_remote_ws_tools_test.go:
// setRemoteConnectionState → saveStateLocked 落状态文件,HOME/ALIANG_DATA_DIR
// 指临时目录 + 重算 cache 单例。

import (
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
)

func TestLegacyArmPanicDoesNotKillProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	origDetail, origEnv := dispatchDetailWithAI, dispatchEnvTools
	t.Cleanup(func() { dispatchDetailWithAI, dispatchEnvTools = origDetail, origEnv })

	panicked := make(chan struct{})
	dispatchDetailWithAI = func(msg map[string]interface{}, writeJSON func(interface{}) error, manager *agentAIManager) {
		defer close(panicked)
		panic("boom: injected legacy-arm panic")
	}

	svc := &AgentService{}
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.mu.Unlock()
	writeJSON := func(p interface{}) error { return nil }

	// ① detail 臂 panic:必须被 recover——裸 go 下本测试二进制直接崩,go test
	// 以非零退出报告(panic 不是断言失败,恰是本测试要钉死的事故形态)。
	svc.handleRemoteAgentMessage(map[string]interface{}{"type": "file.read", "request_id": "r1"}, writeJSON)
	select {
	case <-panicked:
	case <-time.After(3 * time.Second):
		t.Fatal("detail arm did not run")
	}

	// ② panic 之后同一 svc 继续受理后续消息:env 臂假实现应答,证明分发未被执行
	// 上一条消息的 panic 拖死(读循环存活语义)。
	answered := make(chan map[string]interface{}, 1)
	dispatchEnvTools = func(msg map[string]interface{}, writeJSON func(interface{}) error) {
		answered <- map[string]interface{}{"type": "env.info.result", "request_id": msg["request_id"]}
	}
	svc.handleRemoteAgentMessage(map[string]interface{}{"type": "env.info", "request_id": "r2"}, writeJSON)
	select {
	case m := <-answered:
		if m["request_id"] != "r2" {
			t.Fatalf("request_id = %v, want r2", m["request_id"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("env arm did not answer after a legacy-arm panic")
	}
}
