package services

import (
	"encoding/json"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

func TestAgentToolRegistry_ContainsLegacyFour(t *testing.T) {
	reg := agentToolRegistry()
	want := map[string]string{
		"list_dir":   models.AgentEventFileList,
		"read_file":  models.AgentEventFileRead,
		"git_status": models.AgentEventGitStatus,
		"env_info":   models.AgentEventEnvInfo,
	}
	for id, event := range want {
		tool, ok := reg.Get(event)
		if !ok {
			t.Fatalf("tool %s (event %s) not registered", id, event)
		}
		if tool.ID != id {
			t.Fatalf("event %s mapped to %s, want %s", event, tool.ID, id)
		}
		if !tool.Caps.ReadOnly {
			t.Fatalf("tool %s must be read_only", id)
		}
		if tool.Handler == nil {
			t.Fatalf("tool %s handler nil", id)
		}
	}
}

func TestAgentToolRegistry_EventsAreLegacyStrings(t *testing.T) {
	// 兼容红线：注册表的 Event 必须就是老 server 在用的旧事件字符串。
	if models.AgentEventFileList != "file.list" || models.AgentEventFileRead != "file.read" ||
		models.AgentEventGitStatus != "git.status" || models.AgentEventEnvInfo != "env.info" {
		t.Fatalf("legacy event constants drifted: %s %s %s %s",
			models.AgentEventFileList, models.AgentEventFileRead, models.AgentEventGitStatus, models.AgentEventEnvInfo)
	}
}

func TestAgentToolRegistry_DescriptorsShape(t *testing.T) {
	descs := agentToolRegistry().Descriptors()
	if len(descs) < 4 {
		t.Fatalf("descriptor count = %d, want >= 4", len(descs))
	}
	raw, _ := json.Marshal(descs[0])
	for _, k := range []string{`"id"`, `"event"`, `"description"`, `"parameters"`, `"caps"`, `"read_only"`} {
		if !strings.Contains(string(raw), k) {
			t.Fatalf("descriptor missing %s: %s", k, raw)
		}
	}
}

func TestAgentToolRegistry_HelloCarriesTools(t *testing.T) {
	// hello 载荷必须带 agent_tools + agent_tools_rev（Task 6 实现，本测试先行）。
	// 密闭：agentHelloPayload 会扫 agentHome() 下的 ~/.claude / ~/.codex 并落设备身份，
	// 把 HOME / ALIANG_DATA_DIR 指到临时目录，避免单测读真实家目录、写真实数据目录
	// （EffectiveAgentHome 非 root 时直读 $HOME，无需给 agent_home.go 加注入点）。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	// ResetCacheDirForTest 把包级 cache 目录单例重算到本测试的 temp 目录；
	// t.Cleanup（LIFO，先于 t.Setenv 还原 env）再清一次，避免单例指着已被
	// 删除的 temp 目录泄漏给同包后续测试（对齐 common/cache/cachedir_test.go 的写法）。
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	svc := &AgentService{}
	payload := svc.agentHelloPayload()
	if payload["agent_tools"] == nil {
		t.Fatal("hello payload missing agent_tools")
	}
	if _, ok := payload["agent_tools_rev"]; !ok {
		t.Fatal("hello payload missing agent_tools_rev")
	}
}
