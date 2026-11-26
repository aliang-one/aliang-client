package services

// codex 会话活性与采集合并语义的测试族(2026-11-26 修复批)。
//
// 根因背景(手机零感知 codex 对话 + 会话内容陈旧):
//   - codex 条目状态三处硬编码 closed(~/.codex 无 per-pid 活体记录,claude 有
//     ~/.claude/sessions/<pid>.json),正在交互式终端里跑的 codex CLI 在清单/
//     推送/手机全链永远是「已关闭」,「正在发生」信号出生即死;
//   - :754 firstNonEmpty 让陈旧索引 updated_at 压掉新鲜 rollout mtime(session_index
//     .jsonl 自 codex 0.172.0 起停更,索引时间整体过时);
//   - :731 同 ID 双文件(base + resumed 续跑,文件名含旧_新双 ID、session_meta 均为
//     旧 ID)盲覆盖:walker 新→旧返回,resumed 先到、base 后到把续开内容冲掉。
//
// 修复语义:rollout 源文件 mtime 新鲜窗口(3min,与 claudeStatusFreshnessWindow 同源)
// → running;无 pid 门(claude 有 isPidAlive,codex 无活体记录可用),误报自衰减封顶;
// UpdatedAt 取两源更新者;同 ID 多文件文件时间新者胜;索引-only 行(源文件已丢)
// 无新鲜度条目,恒保持 closed,幽灵索引行不会误报。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCodexRolloutFixture 写一个 rollout 文件并返回其路径。session_meta 的
// payload.id 可与文件名 suffix 解耦(resumed 文件的 session_meta 仍持旧 ID,
// 实证见 rollout-<ts>-<old>_<new>.jsonl)。mtime 由调用方 os.Chtimes 控制。
func writeCodexRolloutFixture(t *testing.T, home, filenameTS, idSuffix, metaID string, messages int) string {
	t.Helper()
	rolloutDir := filepath.Join(home, ".codex", "sessions")
	require.NoError(t, os.MkdirAll(rolloutDir, 0o755))

	var b strings.Builder
	meta, err := json.Marshal(map[string]any{
		"timestamp": filenameTS + "Z",
		"type":      "session_meta",
		"payload":   map[string]any{"id": metaID, "cwd": "/projects/foo", "model": "gpt-5"},
	})
	require.NoError(t, err)
	b.Write(meta)
	for i := 0; i < messages; i++ {
		b.WriteString("\n")
		line, err := json.Marshal(map[string]any{
			"timestamp": filenameTS + "Z",
			"type":      "message",
			"payload":   map[string]any{"role": "user", "content": "第" + strconv.Itoa(i+1) + "条真实消息"},
		})
		require.NoError(t, err)
		b.Write(line)
	}
	b.WriteString("\n")

	path := filepath.Join(rolloutDir, "rollout-"+filenameTS+"-"+idSuffix+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	return path
}

func writeCodexIndexFixture(t *testing.T, home string, rows map[string]string) {
	t.Helper()
	codexDir := filepath.Join(home, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0o755))
	var b strings.Builder
	for id, updatedAt := range rows {
		line, err := json.Marshal(map[string]string{
			"id":          id,
			"thread_name": "索引标题-" + id[:8],
			"updated_at":  updatedAt,
		})
		require.NoError(t, err)
		b.Write(line)
		b.WriteString("\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(codexDir, "session_index.jsonl"), []byte(b.String()), 0o644))
}

func findCodexSession(sessions []models.AgentVibeSession, threadID string) *models.AgentVibeSession {
	for i := range sessions {
		if sessions[i].ID == "codex_"+threadID {
			return &sessions[i]
		}
	}
	return nil
}

// 活性:rollout 文件新鲜窗口内 → running。这是「正在发生的 codex 对话」在
// 清单里的唯一形态来源——手机端按 status 渲染,running 才会显「进行中」。
func TestCollectCodexVibeSessionsFreshRolloutIsRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	const id = "01afresh0-0000-4000-8000-000000000001"
	writeCodexIndexFixture(t, home, map[string]string{id: "2026-11-25T00:00:00Z"})
	path := writeCodexRolloutFixture(t, home, "2026-11-25T09-00-00", id, id, 1)
	fresh := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, fresh, fresh))

	sessions := collectCodexVibeSessions(nil)
	found := findCodexSession(sessions, id)
	require.NotNil(t, found)
	assert.Equal(t, "running", found.Status, "新鲜窗口内的 rollout 应判 running")
	// :754 联动:索引时间(11-25)陈旧,取更新者=文件 mtime;陈旧 updated_at 会让
	// PhoneServer 静默 sweeper 把 running 会话打成 timed out。
	assert.Equal(t, fresh.UTC().Format(time.RFC3339), found.UpdatedAt)
}

// 活性衰减:窗口外回落 closed,不会「活进程==永远 running」(claude pre-437bc99 同教训)。
func TestCollectCodexVibeSessionsStaleRolloutStaysClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	const id = "01astale0-0000-4000-8000-000000000002"
	writeCodexIndexFixture(t, home, map[string]string{id: "2026-11-25T00:00:00Z"})
	path := writeCodexRolloutFixture(t, home, "2026-11-25T09-00-00", id, id, 1)
	stale := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, stale, stale))

	sessions := collectCodexVibeSessions(nil)
	found := findCodexSession(sessions, id)
	require.NotNil(t, found)
	assert.Equal(t, "closed", found.Status, "窗口外的 rollout 必须回落 closed")
}

// 幽灵防线:索引-only 行(源文件已丢)没有新鲜度条目,恒保持 closed——
// 0.172.0 停更后索引行整体过时,绝不许它们被误报成 running。
func TestCollectCodexVibeSessionsIndexOnlyStaysClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	const id = "01aghost0-0000-4000-8000-000000000003"
	writeCodexIndexFixture(t, home, map[string]string{id: time.Now().UTC().Format(time.RFC3339)})
	// 不写 rollout 文件

	sessions := collectCodexVibeSessions(nil)
	found := findCodexSession(sessions, id)
	require.NotNil(t, found, "索引-only 行仍应被采集")
	assert.Equal(t, "closed", found.Status, "无源文件支撑的索引行不得误报 running")
}

// :754 语义:索引 updated_at 陈旧(0.172.0 停更)时,新鲜的 rollout mtime 必须赢。
func TestCollectCodexVibeSessionsUpdatedAtPrefersFreshRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	const id = "01afresh1-0000-4000-8000-000000000004"
	writeCodexIndexFixture(t, home, map[string]string{id: "2026-11-20T00:00:00Z"})
	path := writeCodexRolloutFixture(t, home, "2026-11-20T09-00-00", id, id, 1)
	staleDay := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, staleDay, staleDay))

	sessions := collectCodexVibeSessions(nil)
	found := findCodexSession(sessions, id)
	require.NotNil(t, found)
	assert.Equal(t, staleDay.UTC().Format(time.RFC3339), found.UpdatedAt,
		"索引(11-20)比 rollout mtime(30 分钟前)旧,必须取 mtime")
}

// :731 语义:同 ID 双文件(base + resumed 续跑,session_meta 均持旧 ID)时,
// 文件时间新者胜——walker 新→旧返回,resumed 先到、旧 base 后到,盲覆盖会把
// 续开内容冲掉(内容陈旧家族)。断言经 UpdatedAt(=胜者文件 mtime)识别胜者。
func TestCollectCodexVibeSessionsResumedRolloutContentWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	const oldID = "01aold000-0000-4000-8000-000000000005"
	const newID = "01anew000-0000-4000-8000-000000000006"
	writeCodexIndexFixture(t, home, map[string]string{oldID: "2026-11-01T00:00:00Z"})

	basePath := writeCodexRolloutFixture(t, home, "2026-11-01T10-00-00", oldID, oldID, 1)
	baseTime := time.Date(2026, 11, 1, 10, 5, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(basePath, baseTime, baseTime))

	// resumed 文件名含 旧_新 双 ID,session_meta 仍是旧 ID(实证 0.172.x)
	resumedPath := writeCodexRolloutFixture(t, home, "2026-11-26T08-00-00", oldID+"_"+newID, oldID, 2)
	resumedTime := time.Date(2026, 11, 26, 8, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(resumedPath, resumedTime, resumedTime))

	sessions := collectCodexVibeSessions(nil)
	require.NotNil(t, findCodexSession(sessions, oldID), "同 ID 只应产出一条会话")
	assert.Nil(t, findCodexSession(sessions, newID), "resumed 的文件名新 ID 不得裂出第二张卡")
	found := findCodexSession(sessions, oldID)
	require.NotNil(t, found)
	assert.Equal(t, resumedTime.UTC().Format(time.RFC3339), found.UpdatedAt,
		"续开文件(resumed)必须胜出,旧 base 不得把它冲掉")
}

// 确定性:同盘态两次采集同果(活性 overlay 与合并语义都不得引入不稳定序)。
func TestCollectCodexVibeSessionsDeterministic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	const id = "01adeter0-0000-4000-8000-000000000007"
	writeCodexIndexFixture(t, home, map[string]string{id: "2026-11-25T00:00:00Z"})
	path := writeCodexRolloutFixture(t, home, "2026-11-25T09-00-00", id, id, 1)
	stale := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, stale, stale))

	first := collectCodexVibeSessions(nil)
	second := collectCodexVibeSessions(nil)
	assert.Equal(t, first, second)
}

// 公平采样单元:纯 recency 截断会因单一 provider 的会话洪峰把其它内核整族
// 挤出推送窗口(codex「出生即 closed+时间戳陈旧」曾因此被 200 硬截断砍光)。
// 轮询配额必须给每个 provider 保底代表,输出仍按 UpdatedAt 降序。
func TestTruncateAgentVibeSessionsFairRoundRobin(t *testing.T) {
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	sessions := make([]models.AgentVibeSession, 0, 750)
	for i := 0; i < 700; i++ {
		sessions = append(sessions, models.AgentVibeSession{
			ID:        fmt.Sprintf("claude_%03d", i),
			Provider:  "claude",
			Tool:      "claude",
			Status:    "closed",
			UpdatedAt: base.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}
	for i := 0; i < 50; i++ {
		sessions = append(sessions, models.AgentVibeSession{
			ID:        fmt.Sprintf("codex_%03d", i),
			Provider:  "codex",
			Tool:      "codex",
			Status:    "closed",
			UpdatedAt: base.Add(-time.Duration(700+i*2) * time.Minute).Format(time.RFC3339),
		})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt > sessions[j].UpdatedAt })

	out := truncateAgentVibeSessionsFair(sessions, 600)
	require.Len(t, out, 600)

	claudeCount, codexCount := 0, 0
	for _, s := range out {
		switch s.Provider {
		case "claude":
			claudeCount++
		case "codex":
			codexCount++
		}
	}
	assert.Equal(t, 50, codexCount, "50 条 codex 全部应保留(轮询配额,不被 claude 洪峰挤光)")
	assert.Equal(t, 550, claudeCount)
	assert.True(t, sort.SliceIsSorted(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	}), "选中集合必须仍按 UpdatedAt 降序输出")

	// 不足 cap 原样返回(零拷贝路径)
	under := truncateAgentVibeSessionsFair(sessions[:10], 600)
	assert.Len(t, under, 10)
}

// collect 截断挂点接线:601 条唯一 codex(400 索引行 + 201 个独立文件)必须被
// 截到新 cap,且旧 cap(200)时代此测试红。
func TestCollectAgentVibeSessionsCapWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	indexRows := make(map[string]string)
	for i := 0; i < agentVibeIndexMaxLines; i++ {
		id := fmt.Sprintf("01aidx%04d-0000-4000-8000-%012d", i, i)
		indexRows[id] = "2026-11-25T00:00:00Z"
	}
	writeCodexIndexFixture(t, home, indexRows)
	for i := 0; i < 201; i++ {
		id := fmt.Sprintf("01afil%04d-0000-4000-8000-%012d", i, i)
		path := writeCodexRolloutFixture(t, home, "2026-11-25T09-00-00", id, id, 1)
		stale := time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC)
		require.NoError(t, os.Chtimes(path, stale, stale))
	}

	total := agentVibeIndexMaxLines + 201
	require.Greater(t, total, agentVibeSummaryMaxSessions, "fixture 必须超过 cap 才能锁截断")

	sessions := collectAgentVibeSessions(nil)
	require.Len(t, sessions, agentVibeSummaryMaxSessions, "采集层截断必须生效于新 cap")
	for _, s := range sessions {
		assert.Equal(t, "closed", s.Status, "全 fixture mtime 压旧,不得误报 running")
	}
}

// overlay 二次截断挂点接线:活跃 run 叠加后的再截断必须走同一公平采样出口,
// 否则活跃洪峰会把提额与公平性成果再砍一遍。
func TestOverlayActiveTruncatesFair(t *testing.T) {
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	sessions := make([]models.AgentVibeSession, 0, 751)
	for i := 0; i < 700; i++ {
		sessions = append(sessions, models.AgentVibeSession{
			ID:        fmt.Sprintf("claude_%03d", i),
			Provider:  "claude",
			Status:    "closed",
			UpdatedAt: base.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
	}
	for i := 0; i < 50; i++ {
		sessions = append(sessions, models.AgentVibeSession{
			ID:        fmt.Sprintf("codex_%03d", i),
			Provider:  "codex",
			Status:    "closed",
			UpdatedAt: base.Add(-time.Duration(700+i*2) * time.Minute).Format(time.RFC3339),
		})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt > sessions[j].UpdatedAt })

	active := []models.AgentVibeSession{{
		ID:        "claude_active_run",
		Provider:  "claude",
		Status:    "running",
		UpdatedAt: base.Add(time.Minute).Format(time.RFC3339),
	}}

	out := overlayActiveAgentVibeSessions(agentSyncSnapshot{VibeSessions: sessions}, active, nil)
	require.LessOrEqual(t, len(out.VibeSessions), agentVibeSummaryMaxSessions, "overlay 侧截断必须生效")

	codexSeen := false
	for _, s := range out.VibeSessions {
		if s.Provider == "codex" {
			codexSeen = true
			break
		}
	}
	assert.True(t, codexSeen, "overlay 再截断不得破坏公平采样,codex 必须保有代表")
}

// —— 15 天时间窗(2026-11-26 拍板:时间窗是产品语义,cap 降级为兜底)——

// 窗口过滤:超窗条目(含无时间/解析失败)从清单出局;窗内照常。过滤挂在
// collect 唯一咽喉,且必须在 codex :754 时间戳修正之后跑——resumed 老会话
// (索引钟陈旧、rollout mtime 新鲜)靠修正后的时间回窗,不需要特判。
func TestCollectAgentVibeSessionsRecentWindowOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	fresh := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	stale := time.Now().Add(-20 * 24 * time.Hour)

	// codex: 新鲜(rollout mtime 新)+ 陈旧(索引行,rollout mtime 也旧)+ 无时间索引行
	freshID := "01awin000-0000-4000-8000-000000000010"
	staleID := "01aoldwin-0000-4000-8000-000000000011"
	writeCodexIndexFixture(t, home, map[string]string{
		freshID: stale.Format(time.RFC3339), // 索引钟陈旧,靠 :754 修正回窗
		staleID: stale.Format(time.RFC3339),
	})
	freshPath := writeCodexRolloutFixture(t, home, "2026-11-26T09-00-00", freshID, freshID, 1)
	require.NoError(t, os.Chtimes(freshPath, fresh, fresh))
	stalePath := writeCodexRolloutFixture(t, home, "2026-11-01T09-00-00", staleID, staleID, 1)
	require.NoError(t, os.Chtimes(stalePath, stale, stale))

	// claude: 一新一旧
	projectPath := filepath.Join(home, "work", "winproj")
	require.NoError(t, os.MkdirAll(projectPath, 0o700))
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	claudeFresh := "claude-win-fresh"
	claudeStale := "claude-win-stale"
	for _, tc := range []struct {
		id   string
		mod  time.Time
		body string
	}{{claudeFresh, fresh, `{"timestamp":"` + fresh.Format(time.RFC3339) + `","type":"user","cwd":"` + projectPath + `","sessionId":"` + claudeFresh + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"window fresh"}]}}` + "\n"},
		{claudeStale, stale, `{"timestamp":"` + stale.Format(time.RFC3339) + `","type":"user","cwd":"` + projectPath + `","sessionId":"` + claudeStale + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"window stale"}]}}` + "\n"}} {
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, tc.id+".jsonl"), []byte(tc.body), 0o600))
		require.NoError(t, os.Chtimes(filepath.Join(claudeDir, tc.id+".jsonl"), tc.mod, tc.mod))
	}

	sessions := collectAgentVibeSessions(nil)
	assert.NotNil(t, findCodexSession(sessions, freshID), "索引钟陈旧但 rollout 新鲜的会话必须回窗")
	assert.Nil(t, findCodexSession(sessions, staleID), "全钟皆陈旧的 codex 会话必须出窗")
	byID := map[string]bool{}
	for _, s := range sessions {
		byID[s.ID] = true
	}
	assert.True(t, byID["claude_"+claudeFresh], "claude 新会话必须在窗内")
	assert.False(t, byID["claude_"+claudeStale], "claude 陈旧会话必须出窗")
}

// 活跃豁免:窗口外的老会话今天续跑,活跃 overlay 必须让它可见(「正在发生」
// 压倒窗口)。
func TestOverlayActiveBypassesRecentWindow(t *testing.T) {
	old := time.Now().Add(-20 * 24 * time.Hour).Format(time.RFC3339)
	active := []models.AgentVibeSession{{
		ID:        "claude_resumed_old",
		Provider:  "claude",
		Status:    "running",
		UpdatedAt: old,
	}}
	out := overlayActiveAgentVibeSessions(agentSyncSnapshot{VibeSessions: nil}, active, nil)
	found := false
	for _, s := range out.VibeSessions {
		if s.ID == "claude_resumed_old" && s.Status == "running" {
			found = true
		}
	}
	assert.True(t, found, "活跃 run 不得被时间窗误杀")
}
