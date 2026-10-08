package services

import (
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tools"
	"aliang.one/nursorgate/common/cache"
)

func TestRemoteAgentMessageRequiresEnabledDevice_ToolsList(t *testing.T) {
	if !remoteAgentMessageRequiresEnabledDevice(models.AgentEventToolsList) {
		t.Fatal("tools.list must be gated on enabled device")
	}
}

func TestRemoteAgentMessageRequiresEnabledDevice_RegistryTools(t *testing.T) {
	for _, ev := range []string{"file.list", "file.read", "git.status", "env.info"} {
		if !remoteAgentMessageRequiresEnabledDevice(ev) {
			t.Fatalf("registry tool %s must be gated", ev)
		}
	}
}

func TestHandleRemoteAgentMessage_ToolsList(t *testing.T) {
	// tools.list 分支会 setRemoteConnectionState → saveStateLocked 落 agent
	// 状态文件（路径经 cache 目录 / HOME 解析）。密闭对齐 Task 4 的
	// HelloCarriesTools：HOME / ALIANG_DATA_DIR 指到临时目录，并重算包级
	// cache 目录单例（t.Cleanup LIFO 先于 t.Setenv 还原 env 再清一次，
	// 防止单例指着已删除的 temp 目录泄漏给同包后续测试）。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	var wrote interface{}
	writeJSON := func(p interface{}) error { wrote = p; return nil }
	svc := &AgentService{}
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.mu.Unlock()
	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":       models.AgentEventToolsList,
		"request_id": "req_tools1",
	}, writeJSON)
	m, ok := wrote.(map[string]interface{})
	if !ok {
		t.Fatalf("no payload written: %v", wrote)
	}
	if m["type"] != models.AgentEventToolsListResult {
		t.Fatalf("type = %v", m["type"])
	}
	if m["request_id"] != "req_tools1" {
		t.Fatalf("request_id = %v", m["request_id"])
	}
	if _, ok := m["tools"].([]map[string]interface{}); !ok {
		t.Fatalf("tools missing or wrong shape: %T", m["tools"])
	}
	if m["rev"] != agentToolRegistryRev {
		t.Fatalf("rev = %v, want %d", m["rev"], agentToolRegistryRev)
	}
}

func TestHandleRemoteAgentMessage_DefaultArmRegistryDispatch(t *testing.T) {
	// 交换注册表为含假工具的实例，验证 default 臂的注册表分发端到端可达
	// （当前四个旧事件被显式臂遮蔽，此路径在 Phase 2 前无生产流量）。
	// 密闭对齐 ToolsList：default 臂 setRemoteConnectionState → saveStateLocked
	// 落 agent 状态文件，HOME / ALIANG_DATA_DIR 指临时目录 + 重算 cache 单例。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	orig := agentToolsRegistry
	fake := &tools.Tool{
		ID: "fake_probe", Event: "fake.probe", Description: "test fake",
		Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		Caps:       tools.Caps{ReadOnly: true, TimeoutMs: 5000, MaxOutputBytes: 4096},
		Handler: func(msg map[string]interface{}) map[string]interface{} {
			return map[string]interface{}{"type": "fake.probe.result", "request_id": msg["request_id"], "ok": true}
		},
	}
	agentToolsRegistry = tools.NewRegistry(99, fake)
	t.Cleanup(func() { agentToolsRegistry = orig })

	done := make(chan map[string]interface{}, 1)
	writeJSON := func(p interface{}) error {
		if m, ok := p.(map[string]interface{}); ok {
			done <- m
		}
		return nil
	}
	svc := &AgentService{}
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.mu.Unlock()
	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":       "fake.probe",
		"request_id": "req_fake1",
	}, writeJSON)
	select {
	case m := <-done:
		if m["type"] != "fake.probe.result" || m["request_id"] != "req_fake1" || m["ok"] != true {
			t.Fatalf("unexpected payload: %v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("default-arm registry dispatch did not respond in time")
	}
}
