package services

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// 2026-11-28「后台中」状态批(对抗检查定谳 rank5 的产品拍板形态):
// CC 2.1.286 主回合结束后(pid 冻结 busy 由 11-18 reconcile 收口 / 自写
// idle),进程内后台 lane(subagent)可能仍在干活 —— 转录写在
// <projects>/<dir>/<sid>/subagents/agent-*.jsonl(45c63fab 起 walk 选前
// 剔除,主新鲜度信号看不见;live 实测 1977/77058 两会话主转录早已
// turn_duration 而 lane 转录仍在实时写)。产品拍板:此时显示独立状态
// background,不笼统回 running、也不误报 idle。
// 铁律(沿用 2026-11-18 定案):reconcile 本体不动;background 只在
// 「回合已结束」之后产生;衰减窗与主新鲜度同款 3min,防后台僵尸家族复发。

// backgroundFixture 造临时 home:project transcript(+可选尾部行)+
// pid 记录(status 可配)+ 可选 subagent lane 文件(mtime 拨老 laneAge)。
func backgroundFixture(t *testing.T, status, transcriptTail string, writeLane bool, laneAge time.Duration) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	sid := "bg-status-1"
	projectPath := filepath.Join(home, "work", "proj")
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	transcript := `{"timestamp":"2026-11-28T01:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"提问"}]}}` + "\n"
	transcript += transcriptTail
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(transcript), 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
	stale := time.Now().Add(-time.Hour)
	rec := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"` + sid + `","name":"n","status":"` + status + `",` +
		`"updatedAt":` + msJSON(stale) + `,"statusUpdatedAt":` + msJSON(stale) + `}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "1.json"), []byte(rec), 0o600))

	if writeLane {
		laneDir := filepath.Join(claudeDir, sid, "subagents")
		require.NoError(t, os.MkdirAll(laneDir, 0o700))
		lane := filepath.Join(laneDir, "agent-deadbeef.jsonl")
		require.NoError(t, os.WriteFile(lane, []byte(`{"type":"assistant","timestamp":"2026-11-28T01:00:00Z"}`+"\n"), 0o600))
		mtime := time.Now().Add(-laneAge)
		require.NoError(t, os.Chtimes(lane, mtime, mtime))
	}
	return home, sid
}

func TestCollectClaudeBackgroundStatus(t *testing.T) {
	busyAt := time.Now().Add(-time.Hour)
	turnEnded := turnDurationLine(busyAt.Add(30 * time.Minute)) // 晚于 busy 写入:回合已收口

	cases := []struct {
		name       string
		status     string
		tail       string
		writeLane  bool
		laneAge    time.Duration
		wantStatus string
		wantReason string
	}{
		{
			name:       "busy+回合收口+lane 活跃:background",
			status:     "busy",
			tail:       turnEnded,
			writeLane:  true,
			laneAge:    30 * time.Second,
			wantStatus: "background",
			wantReason: "main turn ended but a subagent lane is still working — fine-grained background, not idle",
		},
		{
			name:       "busy+回合收口+无 lane:idle(11-18 语义回归)",
			status:     "busy",
			tail:       turnEnded,
			writeLane:  false,
			wantStatus: "idle",
			wantReason: "turn ended, no lane activity — today's reconcile semantics stand",
		},
		{
			name:       "busy+回合收口+lane 已衰减:回 idle",
			status:     "busy",
			tail:       turnEnded,
			writeLane:  true,
			laneAge:    claudeBackgroundFreshnessWindow + time.Minute,
			wantStatus: "idle",
			wantReason: "lane quiet past the window decays — no zombie background",
		},
		{
			name:       "CC 自写 idle+lane 活跃:background",
			status:     "idle",
			tail:       "",
			writeLane:  true,
			laneAge:    time.Minute,
			wantStatus: "background",
			wantReason: "CC-written idle means the main turn ended; a live lane still shows background",
		},
		{
			name:       "CC 自写 idle+无 lane:idle(437bc99 锚回归)",
			status:     "idle",
			tail:       "",
			writeLane:  false,
			wantStatus: "idle",
			wantReason: "prompt-idle TUI is NOT forever in progress",
		},
		{
			name:       "busy+主回合未收口+lane 活跃:running(lane 不降级主回合)",
			status:     "busy",
			tail:       "",
			writeLane:  true,
			laneAge:    30 * time.Second,
			wantStatus: "running",
			wantReason: "live main turn outranks lane state — never downgrade to background",
		},
		{
			name:       "busy+主回合未收口+无 lane:running(437bc99 锚回归)",
			status:     "busy",
			tail:       "",
			writeLane:  false,
			wantStatus: "running",
			wantReason: "a task is executing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backgroundFixture(t, tc.status, tc.tail, tc.writeLane, tc.laneAge)
			sessions := collectClaudeVibeSessions(nil)
			got := findCollectedSession(sessions, "bg-status-1")
			require.NotNil(t, got)
			assert.Equal(t, tc.wantStatus, got.Status, tc.wantReason)
		})
	}
}

// TestClaudeSubagentLaneActiveEdges pins the helper's guard rails directly.
func TestClaudeSubagentLaneActiveEdges(t *testing.T) {
	t.Run("transcript 缺席: false", func(t *testing.T) {
		assert.False(t, claudeSubagentLaneActive(t.TempDir(), "missing-sid"))
	})
	t.Run("home 为空: false", func(t *testing.T) {
		assert.False(t, claudeSubagentLaneActive("", "sid"))
	})
	t.Run("lane 目录不存在: false", func(t *testing.T) {
		home, _ := backgroundFixture(t, "idle", "", false, 0)
		assert.False(t, claudeSubagentLaneActive(home, "bg-status-1"))
	})
	t.Run("lane 目录里只有子目录: false", func(t *testing.T) {
		home, sid := backgroundFixture(t, "idle", "", false, 0)
		nested := filepath.Join(home, ".claude", "projects", "-work-proj", sid, "subagents", "nested")
		require.NoError(t, os.MkdirAll(nested, 0o700))
		assert.False(t, claudeSubagentLaneActive(home, sid))
	})
}

// TestCollectClaudeBackgroundDeterminism:同盘态连续两轮采集结论一致
// (agent 重启/重复扫描收敛,不振荡)。
func TestCollectClaudeBackgroundDeterminism(t *testing.T) {
	busyAt := time.Now().Add(-time.Hour)
	backgroundFixture(t, "busy", turnDurationLine(busyAt.Add(30*time.Minute)), true, 30*time.Second)
	first := findCollectedSession(collectClaudeVibeSessions(nil), "bg-status-1")
	second := findCollectedSession(collectClaudeVibeSessions(nil), "bg-status-1")
	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.Equal(t, "background", first.Status)
	assert.Equal(t, first.Status, second.Status)
}
