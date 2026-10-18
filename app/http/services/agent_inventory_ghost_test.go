package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// 2026-10-18 TUI 同步审计(S2 幽灵/空会话)的两道采集回归:
//
// 1) 幽灵会话工厂:Claude Code 2026-08-24 起停更 sessions-index.json 且
//    30 天清理删光旧转录后,索引残留条目被无条件构造为会话上报——本机
//    实锤 226/226 条目的 jsonl 全部不存在,详情永远 not found,手机列表
//    一半是点开无内容的死卡片。索引通道必须校验 <sid>.jsonl 存在。
//
// 2) /clear 残壳:仅含 harness 工件行(meta/commandRecord)的 jsonl 全程
//    无一条可渲染消息,reader 的 Transcript 热窗为空——游走通道不得把
//    这类壳上报为会话(实测 15 个,MessageCount 因工件行自增为 2-8 但
//    可见消息为零)。

// ghostHuskFixtureHome 建一个最小 ~/.claude/projects 工程,返回
// (工程目录, 真实工程路径)。
func ghostHuskFixtureHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	projectPath := filepath.Join(home, "work", "myproject")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encoded := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	dir := filepath.Join(home, ".claude", "projects", encoded)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir, projectPath
}

// ghostHuskRow 构造一行 jsonl 记录。reader 从行内取 sessionId/cwd,extra
// 允许附加字段(isMeta 等);content 允许字符串或数组两种形态。
func ghostHuskRow(t *testing.T, sid, cwd, ts, role string, content interface{}, extra map[string]interface{}) string {
	t.Helper()
	payload := map[string]interface{}{
		"timestamp": ts,
		"type":      role,
		"sessionId": sid,
		"cwd":       cwd,
		"message": map[string]interface{}{
			"role":    role,
			"content": content,
		},
	}
	for k, v := range extra {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(raw)
}

func ghostHuskWriteLines(t *testing.T, path string, lines []string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// writeIndexSupportTranscript 为 sessions-index.json 条目补最小转录支撑
// 文件:2026-10-18 起索引通道校验 <sid>.jsonl 存在性(幽灵拦截),纯索引
// fixture 不再被采集。sid 与索引条目一致——游走通道 seen 去重,索引数据
// 形态(标题/summary 断言)保持原语义。
func writeIndexSupportTranscript(t *testing.T, claudeDir, sid, projectPath string) {
	t.Helper()
	line := `{"timestamp":"2026-06-19T00:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","message":{"role":"user","content":[{"type":"text","text":"fixture support message"}]}}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(line+"\n"), 0o600))
}

func collectedClaudeIDs(t *testing.T) []string {
	t.Helper()
	sessions, _ := collectClaudeVibeSessionsWithStats(nil)
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestCollectClaudeVibeSessionsSkipsIndexEntriesWithoutTranscriptFile(t *testing.T) {
	dir, projectPath := ghostHuskFixtureHome(t)
	index := map[string]interface{}{
		"originalPath": projectPath,
		"entries": []map[string]interface{}{
			{
				"sessionId":    "sid-ghost",
				"firstPrompt":  "ghost conversation",
				"messageCount": 12,
				"modified":     "2026-10-17T00:00:00Z",
			},
			{
				"sessionId":    "sid-real",
				"firstPrompt":  "real conversation",
				"messageCount": 3,
				"modified":     "2026-10-18T00:00:00Z",
			},
		},
	}
	raw, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions-index.json"), raw, 0o600))

	// 只有 sid-real 的转录文件存在。
	ghostHuskWriteLines(t, filepath.Join(dir, "sid-real.jsonl"), []string{
		ghostHuskRow(t, "sid-real", projectPath, "2026-10-18T00:00:01Z", "user", "hello", nil),
		ghostHuskRow(t, "sid-real", projectPath, "2026-10-18T00:00:02Z", "assistant",
			[]map[string]interface{}{{"type": "text", "text": "hi"}}, nil),
	})

	ids := collectedClaudeIDs(t)
	assert.NotContains(t, ids, "claude_sid-ghost", "索引残留条目(jsonl 已被清理)必须被存在性校验拦截")
	assert.Contains(t, ids, "claude_sid-real", "jsonl 真实存在的索引条目必须保留")
}

func TestCollectClaudeVibeSessionsSkipsHuskWithoutRenderableMessages(t *testing.T) {
	dir, projectPath := ghostHuskFixtureHome(t)
	// 残壳:仅 harness 工件行(isMeta 的 caveat 回显 + /clear 命令回显),
	// 全程无一条可渲染消息。
	ghostHuskWriteLines(t, filepath.Join(dir, "husk-sid.jsonl"), []string{
		ghostHuskRow(t, "husk-sid", projectPath, "2026-10-18T00:00:01Z", "user",
			[]map[string]interface{}{{"type": "text", "text": "<local-command-caveat>This command loads a skill.</local-command-caveat>"}},
			map[string]interface{}{"isMeta": true}),
		ghostHuskRow(t, "husk-sid", projectPath, "2026-10-18T00:00:02Z", "user",
			[]map[string]interface{}{{"type": "text", "text": "<command-name>/clear</command-name>"}}, nil),
	})
	ghostHuskWriteLines(t, filepath.Join(dir, "real-sid.jsonl"), []string{
		ghostHuskRow(t, "real-sid", projectPath, "2026-10-18T00:00:03Z", "user", "real question", nil),
	})

	ids := collectedClaudeIDs(t)
	assert.NotContains(t, ids, "claude_husk-sid", "零可渲染消息的残壳会话不得进入上报快照")
	assert.Contains(t, ids, "claude_real-sid", "正常会话不受残壳过滤影响")
}

func findCollectedClaudeSession(t *testing.T, id string) *models.AgentVibeSession {
	t.Helper()
	sessions, _ := collectClaudeVibeSessionsWithStats(nil)
	for i := range sessions {
		if sessions[i].ID == id {
			return &sessions[i]
		}
	}
	return nil
}

// 2026-10-18 事故回归:索引通道只知道 MessageCount(命令行/工件行也占位),
// reader 才知道"手机可渲染几条"。残壳在索引里 messageCount 非零,若 reader
// 的已知 0 不回灌,服务端会把残壳当有历史的会话——刷新成功却只有空页
// (b6d5e509 / b9abc112 实锤)。
func TestCollectClaudeVibeSessionsPropagatesKnownZeroDisplayCountForHusk(t *testing.T) {
	dir, projectPath := ghostHuskFixtureHome(t)
	index := map[string]interface{}{
		"originalPath": projectPath,
		"entries": []map[string]interface{}{
			{
				"sessionId":    "husk-sid",
				"firstPrompt":  "husk conversation",
				"messageCount": 5,
				"modified":     "2026-10-18T00:00:00Z",
			},
		},
	}
	raw, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions-index.json"), raw, 0o600))

	// 残壳:仅 harness 工件行,无可渲染消息。
	ghostHuskWriteLines(t, filepath.Join(dir, "husk-sid.jsonl"), []string{
		ghostHuskRow(t, "husk-sid", projectPath, "2026-10-18T00:00:01Z", "user",
			[]map[string]interface{}{{"type": "text", "text": "<local-command-caveat>This command loads a skill.</local-command-caveat>"}},
			map[string]interface{}{"isMeta": true}),
		ghostHuskRow(t, "husk-sid", projectPath, "2026-10-18T00:00:02Z", "user",
			[]map[string]interface{}{{"type": "text", "text": "<command-name>/clear</command-name>"}}, nil),
	})

	got := findCollectedClaudeSession(t, "claude_husk-sid")
	require.NotNil(t, got, "索引通道已入列的残壳会话仍在上报快照里(本测试钉显示计数回灌)")
	assert.Equal(t, 5, got.MessageCount, "MessageCount 保持索引口径(含工件行),不得被改写")
	require.NotNil(t, got.DisplayMessageCount, "reader 的已知 0 必须回灌;nil 会让服务端回落到 MessageCount")
	assert.Equal(t, 0, *got.DisplayMessageCount)
}

// 索引条目 + 真实转录:MessageCount 保持索引值,DisplayMessageCount 由 reader
// 回灌(排除 tool_result 工件)。
func TestCollectClaudeVibeSessionsBackfillsDisplayCountOnIndexEntry(t *testing.T) {
	dir, projectPath := ghostHuskFixtureHome(t)
	index := map[string]interface{}{
		"originalPath": projectPath,
		"entries": []map[string]interface{}{
			{
				"sessionId":    "sid-real",
				"firstPrompt":  "real conversation",
				"messageCount": 4,
				"modified":     "2026-10-18T00:00:00Z",
			},
		},
	}
	raw, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions-index.json"), raw, 0o600))
	ghostHuskWriteLines(t, filepath.Join(dir, "sid-real.jsonl"), []string{
		ghostHuskRow(t, "sid-real", projectPath, "2026-10-18T00:00:01Z", "user", "real question", nil),
		ghostHuskRow(t, "sid-real", projectPath, "2026-10-18T00:00:02Z", "assistant",
			[]map[string]interface{}{{"type": "text", "text": "real answer"}}, nil),
		ghostHuskRow(t, "sid-real", projectPath, "2026-10-18T00:00:03Z", "user",
			[]map[string]interface{}{{"type": "tool_result", "content": "tool output"}}, nil),
		ghostHuskRow(t, "sid-real", projectPath, "2026-10-18T00:00:04Z", "assistant",
			[]map[string]interface{}{{"type": "text", "text": "after tool"}}, nil),
	})

	got := findCollectedClaudeSession(t, "claude_sid-real")
	require.NotNil(t, got)
	assert.Equal(t, 4, got.MessageCount)
	require.NotNil(t, got.DisplayMessageCount)
	assert.Equal(t, 3, *got.DisplayMessageCount, "tool_result 工件占索引位但不计入展示数")
}

// 活跃快照合并不得把已知 0 抹成 nil(未知)——否则一次转弯就会让残壳重新
// "看起来有历史"。
func TestMergeActiveAgentVibeSessionPreservesKnownZeroDisplayCount(t *testing.T) {
	zero := 0
	existing := models.AgentVibeSession{ID: "claude_sid", MessageCount: 5, DisplayMessageCount: &zero}
	active := models.AgentVibeSession{ID: "claude_sid"}
	merged := mergeActiveAgentVibeSession(existing, active)
	require.NotNil(t, merged.DisplayMessageCount)
	assert.Equal(t, 0, *merged.DisplayMessageCount)

	// active 自带计数时不被 existing 覆盖。
	two := 2
	active.DisplayMessageCount = &two
	merged = mergeActiveAgentVibeSession(existing, active)
	require.NotNil(t, merged.DisplayMessageCount)
	assert.Equal(t, 2, *merged.DisplayMessageCount)
}
