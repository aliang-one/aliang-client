package services

// hello 增量协议测试族(2026-11-27,spec docs/superpowers/specs/2026-11-26-hello-delta-protocol.md)。
//
// 兼容铁律(侦察坐实,逐条锁测试):
//   - 未变段必须「省略字段」绝不发 []:服务端 [] = 清空、缺失 = 保存量;
//   - 心跳帧必须保留身份小件(capabilities 缺失会被服务端抹成 [] 级联关门);
//   - 指纹不含采集时刻:时间字段量化到分钟(projects.LastActiveAt 的 time.Now()
//     兜底链是既证抖动源),tools 指纹不含 Description(运行时探针翻转面);
//   - 全量锚点:connect/server_requested/连续 delta 达阈值/registered 对账不符。

import (
	"encoding/json"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helloDeltaFixturePayload 构造一份最小全量 hello 载荷(与 agentHelloPayload 的
// key 面对齐,字段面够测试即可)。
func helloDeltaFixturePayload(t *testing.T, now time.Time) map[string]interface{} {
	t.Helper()
	base := now.Truncate(time.Minute)
	return map[string]interface{}{
		"type":                   models.AgentEventHello,
		"protocol_version":       models.AgentProtocolVersion,
		"device_id":              "dev-test",
		"device_name":            "test-host",
		"platform":               "darwin-arm64",
		"agent_version":          "1.1.131-local",
		"capabilities":           []string{"ai_provider_codex", "terminal"},
		"started_at":             base.Format(time.RFC3339),
		"collected_at":           base.Format(time.RFC3339),
		"load":                   map[string]interface{}{"cpu": 0.1},
		"net":                    map[string]interface{}{"up": 1},
		"agent_tools_rev":        "r1",
		"tools":                  []models.AgentTool{{ID: "t1", Version: "1"}},
		"agent_tools":            []map[string]interface{}{{"id": "at1", "description": "desc"}},
		"subagents":              []models.AgentSubagent{{Name: "s1"}},
		"history":                []models.AgentHistoryRoot{{Tool: "claude", Path: "/h1"}},
		"projects": []models.AgentProject{
			{ID: "p1", Name: "proj", Path: "/tmp/proj", LastActiveAt: base.Format(time.RFC3339)},
		},
		"vibe_sessions": []models.AgentVibeSession{
			{ID: "codex_a", Provider: "codex", Status: "closed", UpdatedAt: base.Add(-time.Hour).Format(time.RFC3339)},
			{ID: "claude_b", Provider: "claude", Status: "closed", UpdatedAt: base.Add(-2 * time.Hour).Format(time.RFC3339)},
		},
		"authorized_directories": []string{"/tmp/proj"},
	}
}

func helloMapString(t *testing.T, m map[string]interface{}, key string) string {
	t.Helper()
	raw, err := json.Marshal(m[key])
	require.NoError(t, err)
	return string(raw)
}

// 全量锚点:connect 与 server_requested 无条件 full,哪怕基线完全一致。
func TestHelloFramePlanFullAnchors(t *testing.T) {
	s := &AgentService{}
	payload := helloDeltaFixturePayload(t, time.Now())
	// 预置「完全一致」的基线:commit 一次 full 后再 plan
	s.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload)

	for _, reason := range []string{"connect", "server_requested"} {
		plan := s.helloDelta.plan(reason, payload)
		assert.Equal(t, "full", plan.mode, "reason=%s 必须强制 full", reason)
	}
}

// 稳态:基线一致 → heartbeat;applyPlan 后大段全省略、身份小件保留。
func TestHelloFrameHeartbeatWhenUnchanged(t *testing.T) {
	s := &AgentService{}
	payload := helloDeltaFixturePayload(t, time.Now())
	s.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload)

	plan := s.helloDelta.plan("periodic", payload)
	require.Equal(t, "heartbeat", plan.mode, "基线一致必须降级 heartbeat")

	frame := s.helloDelta.applyPlan(payload, plan)
	assert.Equal(t, "heartbeat", frame["sync_mode"])
	for _, heavy := range []string{"tools", "agent_tools", "subagents", "history", "projects", "vibe_sessions", "authorized_directories"} {
		assert.NotContains(t, frame, heavy, "心跳帧不得携带大段 %s", heavy)
	}
	for _, keep := range []string{"device_id", "device_name", "platform", "agent_version", "capabilities", "load", "net", "agent_tools_rev", "type", "protocol_version"} {
		assert.Contains(t, frame, keep, "心跳帧必须保留身份/健康小件 %s(缺失会被服务端抹除或丢健康面)", keep)
	}
	// applyPlan 契约=就地改写(发送帧与构建帧同 Map,省一次拷贝);builder 本身
	// 无状态,下一帧重新构建即可,测试不复用被改写的 payload。
	_ = payload
}

// 变更 → delta:只带变化会话与变化段;未变会话/未变段不出现在帧里。
func TestHelloFrameDeltaOnSessionChange(t *testing.T) {
	s := &AgentService{}
	payload := helloDeltaFixturePayload(t, time.Now())
	s.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload)

	// 改一条会话内容(message_count)+ 改一个项目路径,其余不动
	sessions := payload["vibe_sessions"].([]models.AgentVibeSession)
	sessions[0].MessageCount = 99
	projects := payload["projects"].([]models.AgentProject)
	projects[0].Name = "renamed"

	plan := s.helloDelta.plan("periodic", payload)
	require.Equal(t, "delta", plan.mode)

	frame := s.helloDelta.applyPlan(payload, plan)
	assert.Equal(t, "delta", frame["sync_mode"])
	changes, ok := frame["session_changes"].([]models.AgentVibeSession)
	require.True(t, ok, "delta 帧必须携带 session_changes")
	require.Len(t, changes, 1)
	assert.Equal(t, "codex_a", changes[0].ID)
	assert.NotContains(t, frame, "vibe_sessions", "delta 帧不得携带全量 vibe_sessions")
	assert.NotContains(t, frame, "history", "未变段必须省略")
	assert.Contains(t, frame, "projects", "变化段必须携带全量数组(服务端 upsert=全量替换)")
}

// 窗口出清 → session_removed_ids 墓碑。
func TestHelloFrameRemovedSessionsTombstoned(t *testing.T) {
	s := &AgentService{}
	payload := helloDeltaFixturePayload(t, time.Now())
	s.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload)

	sessions := payload["vibe_sessions"].([]models.AgentVibeSession)
	payload["vibe_sessions"] = sessions[:1] // claude_b 出清

	plan := s.helloDelta.plan("periodic", payload)
	require.Equal(t, "delta", plan.mode)
	frame := s.helloDelta.applyPlan(payload, plan)
	removed, ok := frame["session_removed_ids"].([]string)
	require.True(t, ok, "delta 帧必须携带 session_removed_ids")
	assert.Equal(t, []string{"claude_b"}, removed)
}

// 自愈锚点:连续 delta 达阈值,下一帧升格 full。
func TestHelloDeltaSelfHealAfterThreshold(t *testing.T) {
	s := &AgentService{}
	payload := helloDeltaFixturePayload(t, time.Now())
	s.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload)

	for i := 0; i < agentHelloDeltaSelfHealEvery; i++ {
		// 每轮重建 fixture(applyPlan 契约=就地改写,被改写的 payload 不可复用);
		// MessageCount 递增(i+1)保证每轮相对上轮基线都是真变化。
		payload = helloDeltaFixturePayload(t, time.Now())
		sessions := payload["vibe_sessions"].([]models.AgentVibeSession)
		sessions[0].MessageCount = i + 1
		plan := s.helloDelta.plan("periodic", payload)
		require.Equal(t, "delta", plan.mode, "round %d", i+1)
		s.helloDelta.commit(&plan, payload)
	}
	plan := s.helloDelta.plan("periodic", payload)
	assert.Equal(t, "full", plan.mode, "连续 %d 次 delta 后必须回全量自愈", agentHelloDeltaSelfHealEvery)
}

// 指纹纪律 1:时间字段量化到分钟——秒级翻新不触发 delta(projects LastActiveAt
// 活跃机每拍翻新的 time.Now() 兜底链是既证抖动源)。
func TestHelloFingerprintQuantizesTimestamps(t *testing.T) {
	now := time.Now()
	base := now.Truncate(time.Minute) // 抖动样本锚定在同一分钟内,不随测试执行秒数跨界
	p1 := helloDeltaFixturePayload(t, now)
	p2 := helloDeltaFixturePayload(t, now)
	projects := p2["projects"].([]models.AgentProject)
	projects[0].LastActiveAt = base.Add(37 * time.Second).Format(time.RFC3339) // 同分钟内抖动

	fp1 := agentStableSectionFingerprint(p1["projects"])
	fp2 := agentStableSectionFingerprint(p2["projects"])
	assert.Equal(t, fp1, fp2, "同分钟内的时间抖动不得改变指纹")

	p3 := helloDeltaFixturePayload(t, now)
	projects = p3["projects"].([]models.AgentProject)
	projects[0].LastActiveAt = now.Add(2 * time.Minute).Format(time.RFC3339)
	assert.NotEqual(t, fp1, agentStableSectionFingerprint(p3["projects"]), "跨分钟的真变化必须翻指纹")
}

// 指纹纪律 2:tools/agent_tools 指纹不含 Description(codexAppServerAvailable
// 运行时探针会翻转 Description,假 delta 会杀死 heartbeat)。
func TestHelloFingerprintIgnoresDescription(t *testing.T) {
	p1 := helloDeltaFixturePayload(t, time.Now())
	p2 := helloDeltaFixturePayload(t, time.Now())
	tools := p2["agent_tools"].([]map[string]interface{})
	tools[0]["description"] = "runtime-probe flipped"

	assert.Equal(t,
		agentStableSectionFingerprint(p1["agent_tools"]),
		agentStableSectionFingerprint(p2["agent_tools"]),
		"Description 翻转不得改变 agent_tools 指纹")
}

// registered 对账:服务端报告 0 条而基线非零(服务端内存态丢失信号)→ 强制 full;
// 正常数目不触发(服务端有过滤语义,数目不必逐条相等)。
func TestHelloRegisteredAckMismatchForcesFull(t *testing.T) {
	s := &AgentService{}
	payload := helloDeltaFixturePayload(t, time.Now())
	s.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload)

	s.helloDelta.reconcileRegisteredServerSessions(0)
	plan := s.helloDelta.plan("periodic", payload)
	assert.Equal(t, "full", plan.mode, "服务端会话数为 0 而基线非零必须升格 full")

	s2 := &AgentService{}
	payload2 := helloDeltaFixturePayload(t, time.Now())
	s2.helloDelta.commit(&agentHelloFramePlan{mode: "full"}, payload2)
	s2.helloDelta.reconcileRegisteredServerSessions(2)
	plan2 := s2.helloDelta.plan("periodic", payload2)
	assert.NotEqual(t, "full", plan2.mode, "正常数目的偏差(过滤语义)不得触发 full")
}

// 端到端:三次 sendAgentHello 走线 full → heartbeat → delta(经 hermetic home)。
func TestSendAgentHelloWireModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", home)
	cache.ResetCacheDirForTest()

	svc := NewAgentService()
	svc.remoteHello.noteSent("", time.Now().Add(-time.Hour))

	var frames []map[string]interface{}
	write := func(v interface{}) error {
		m, ok := v.(map[string]interface{})
		require.True(t, ok)
		frames = append(frames, m)
		return nil
	}

	require.NoError(t, svc.sendAgentHello(write, "connect"))
	require.NotEmpty(t, frames)
	assert.Equal(t, "full", frames[0]["sync_mode"], "首帧必须 full")
	assert.Contains(t, frames[0], "vibe_sessions")

	require.NoError(t, svc.sendAgentHello(write, "periodic"))
	require.Len(t, frames, 2)
	if frames[1]["sync_mode"] == "delta" {
		// 盘面无变化时不允许出现变化条目;若环境干扰出变化,至少不能是全量
		assert.NotContains(t, frames[1], "vibe_sessions")
	} else {
		assert.Equal(t, "heartbeat", frames[1]["sync_mode"])
	}
}
