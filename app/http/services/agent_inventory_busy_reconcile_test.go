package services

import (
	"fmt"
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

// 2026-11-18 外部 TUI 族「进行中」僵尸修复批（已定案，勿重新诊断）：CC CLI
// 2.1.286 回合结束只落 transcript 的 turn_duration 行，漏写 pid 记录的
// busy→idle——inventory 按 record.Status==busy 镜像成永动 running。
// 修复 = busy 映射前做内容证据复核：transcript 尾的回合结束标记时间戳晚于
// busy 写入时刻 → 按 idle 展示（只影响列表映射，不回写 CC 文件、不发帧）。
// 铁律：禁 mtime 停更判据（活回合合法停更可超 1h）；turn_duration 早于 busy
// 时刻（同文件新回合已开跑）保持 busy；找不到标记维持现状（busy）。

// busyReconcileFixture 造一个临时 home：pid 记录（busy + statusUpdatedAt）+
// project transcript 尾部行。返回 home 与 native sessionId。
func busyReconcileFixture(t *testing.T, pidRecord string, transcriptTail string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()

	sid := "busy-recon-1"
	projectPath := filepath.Join(home, "work", "proj")
	encodedCwd := "-" + strings.ReplaceAll(strings.Trim(projectPath, string(filepath.Separator)), string(filepath.Separator), "-")
	claudeDir := filepath.Join(home, ".claude", "projects", encodedCwd)
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	// 首行真实 user 消息（readAgentVibeClaudeSummary 摘要与幽灵校验所需）
	transcript := `{"timestamp":"2026-11-17T01:00:00Z","type":"user","cwd":"` + projectPath +
		`","sessionId":"` + sid + `","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"提问"}]}}` + "\n"
	transcript += transcriptTail
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, sid+".jsonl"), []byte(transcript), 0o600))

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700))
	rec := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"sessionId":"` + sid + `","name":"n","status":"busy",` + pidRecord + `}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "sessions", "1.json"), []byte(rec), 0o600))
	return home, sid
}

// turnDurationLine 造一条 turn_duration 行（真实 2.1.286 字段形态实测校准）。
func turnDurationLine(ts time.Time) string {
	return `{"parentUuid":"x","isSidechain":false,"type":"system","subtype":"turn_duration",` +
		`"durationMs":1234,"messageCount":9,"timestamp":"` + ts.UTC().Format("2006-01-02T15:04:05.000Z07:00") +
		`","uuid":"u-turn","isMeta":false,"userType":"external","entrypoint":"cli"}` + "\n"
}

func TestCollectClaudeVibeSessionsStaleBusyReconcile(t *testing.T) {
	busyWriteAt := time.UnixMilli(1794000000000) // 固定锚点：busy 写入时刻
	after := busyWriteAt.Add(3 * time.Minute)    // 回合结束后落盘的 turn_duration
	before := busyWriteAt.Add(-time.Minute)      // 同文件更早回合的 turn_duration

	cases := []struct {
		name           string
		pidRecord      string
		transcriptTail string
		wantStatus     string
		wantReason     string
	}{
		{
			name:           "turn_duration 晚于 busy 写入:CC 漏写 idle,按 idle 展示",
			pidRecord:      `"updatedAt":` + msJSON(busyWriteAt) + `,"statusUpdatedAt":` + msJSON(busyWriteAt),
			transcriptTail: turnDurationLine(after),
			wantStatus:     "idle",
			wantReason:     "turn ended after the busy write = CC missed the busy→idle rewrite",
		},
		{
			name:           "turn_duration 早于 busy 写入:同文件新回合已开跑,保持 running",
			pidRecord:      `"updatedAt":` + msJSON(busyWriteAt) + `,"statusUpdatedAt":` + msJSON(busyWriteAt),
			transcriptTail: turnDurationLine(before),
			wantStatus:     "running",
			wantReason:     "a marker older than the busy write means a newer turn started",
		},
		{
			name:           "无 turn_duration(旧版本/老格式):维持现状语义 busy→running",
			pidRecord:      `"updatedAt":` + msJSON(busyWriteAt) + `,"statusUpdatedAt":` + msJSON(busyWriteAt),
			transcriptTail: "",
			wantStatus:     "running",
			wantReason:     "no marker = no content evidence = keep today's semantics",
		},
		{
			name:           "缺 statusUpdatedAt 回落 updatedAt:晚标记仍判 idle",
			pidRecord:      `"updatedAt":` + msJSON(busyWriteAt),
			transcriptTail: turnDurationLine(after),
			wantStatus:     "idle",
			wantReason:     "older CC builds omit statusUpdatedAt; updatedAt is the same anchor",
		},
		{
			name:           "毫秒数值形态(真实 2.1.286 记录):晚标记判 idle",
			pidRecord:      `"updatedAt":` + msJSON(busyWriteAt) + `,"statusUpdatedAt":` + msJSON(busyWriteAt),
			transcriptTail: turnDurationLine(after),
			wantStatus:     "idle",
			wantReason:     "real-record wire shape (UNIX ms numbers)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			busyReconcileFixture(t, tc.pidRecord, tc.transcriptTail)
			sessions := collectClaudeVibeSessions(nil)
			got := findCollectedSession(sessions, "busy-recon-1")
			require.NotNil(t, got)
			assert.Equal(t, tc.wantStatus, got.Status, tc.wantReason)
		})
	}
}

// TestCollectClaudeVibeSessionsStaleBusyTailTruncation pins the tail-read
// contract: the marker lives far beyond the 64KB read window's start, and the
// file is padded so the boundary can land mid-line — the reconcile must still
// find it (partial leading lines fail to unmarshal and are skipped).
func TestCollectClaudeVibeSessionsStaleBusyTailTruncation(t *testing.T) {
	busyWriteAt := time.UnixMilli(1794000000000)
	after := busyWriteAt.Add(3 * time.Minute)
	// ~70KB 填充行（assistant 元数据行，无 turn 语义）把 turn_duration 顶到
	// 64KB 窗口之外的开头之后
	pad := `{"parentUuid":"p","isSidechain":false,"type":"assistant","uuid":"pad-1",` +
		`"message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 70*1024) + `"}]}}` + "\n"
	busyReconcileFixture(t, `"updatedAt":`+msJSON(busyWriteAt)+`,"statusUpdatedAt":`+msJSON(busyWriteAt), pad+turnDurationLine(after))
	sessions := collectClaudeVibeSessions(nil)
	got := findCollectedSession(sessions, "busy-recon-1")
	require.NotNil(t, got)
	assert.Equal(t, "idle", got.Status, "tail read must reach the marker beyond the 64KB boundary")
}

// TestClaudeBusyTurnEndedAfterRecordEdges pins the helper's guard rails
// directly (collectClaudeVibeSessions 的宿主测试跑不到的零值/缺文件分支)。
func TestClaudeBusyTurnEndedAfterRecordEdges(t *testing.T) {
	t.Run("busyAt 零值:直接 false,不读盘", func(t *testing.T) {
		assert.False(t, claudeBusyTurnEndedAfterRecord(t.TempDir(), "sid", time.Time{}))
	})
	t.Run("transcript 缺席: false(维持 busy)", func(t *testing.T) {
		home := t.TempDir()
		assert.False(t, claudeBusyTurnEndedAfterRecord(home, "missing-sid", time.Now()))
	})
	t.Run("home 为空: false", func(t *testing.T) {
		assert.False(t, claudeBusyTurnEndedAfterRecord("", "sid", time.Now()))
	})
	t.Run("非回合结束 system 行不算证据", func(t *testing.T) {
		home, _ := busyReconcileFixture(t,
			`"updatedAt":1,"statusUpdatedAt":1`,
			`{"type":"system","subtype":"compact_boundary","timestamp":"2099-01-01T00:00:00Z"}`+"\n")
		assert.False(t, claudeBusyTurnEndedAfterRecord(home, "busy-recon-1", time.Unix(1, 0)))
	})
	t.Run("最后一条标记决定结论:全部早于 busy 写入(新回合已开跑)保持 busy", func(t *testing.T) {
		busy := time.UnixMilli(1794000000000)
		older := busy.Add(-5 * time.Minute)
		last := busy.Add(-time.Minute) // 文件序单调:最后一条仍早于 busy
		home, _ := busyReconcileFixture(t,
			`"updatedAt":`+msJSON(busy)+`,"statusUpdatedAt":`+msJSON(busy),
			turnDurationLine(older)+turnDurationLine(last))
		assert.False(t, claudeBusyTurnEndedAfterRecord(home, "busy-recon-1", busy),
			"last marker earlier than busy write wins: a newer turn started on this file")
	})
	t.Run("sidechain 行跳过", func(t *testing.T) {
		busy := time.UnixMilli(1794000000000)
		line := `{"isSidechain":true,"type":"system","subtype":"turn_duration","timestamp":"2099-01-01T00:00:00Z"}` + "\n"
		home, _ := busyReconcileFixture(t, `"updatedAt":`+msJSON(busy)+`,"statusUpdatedAt":`+msJSON(busy), line)
		assert.False(t, claudeBusyTurnEndedAfterRecord(home, "busy-recon-1", busy))
	})
}

// msJSON 把时间渲染成真实 pid 记录的 UNIX 毫秒 JSON 数值形态。
func msJSON(ts time.Time) string {
	return fmt.Sprintf("%d", ts.UnixMilli())
}
