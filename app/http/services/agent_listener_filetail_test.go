package services

// agent_listener_filetail_test.go file_tail watcher 单测(specs/005-event-listener
// §3/FR-5,plan Task 9):启动幂等/行匹配上行帧契约/不命中静默/正则与坏正则
// 回退/截断重置 offset/洪峰合并/文件消失置 error/钩子生命周期/全停收敛。
// outbox 暂存与 reject 计数的测试在同文件 M2-B 段。
//
// 缝纪律(同 Task 7/8):测试串行,禁 t.Parallel;swap 期间不得有在跑 watcher
// ——统一经 setupFileTailTest,其单一 Cleanup 保证「先停 watcher、后还原缝」
// 的顺序(uplink/home 是普通包级 var,倒序还原会在 -race 下炸)。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// fileTailUplinkCapture 上行捕获器(带锁:watcher goroutine 写、测试读)。
// fail 模式可切换:置 true 后上行恒败(watch 走 outbox 路径)。
type fileTailUplinkCapture struct {
	mu     sync.Mutex
	frames []map[string]interface{}
	fail   bool
}

func (c *fileTailUplinkCapture) record(frame map[string]interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return fmt.Errorf("uplink offline (test)")
	}
	c.frames = append(c.frames, frame)
	return nil
}

func (c *fileTailUplinkCapture) setFail(fail bool) {
	c.mu.Lock()
	c.fail = fail
	c.mu.Unlock()
}

func (c *fileTailUplinkCapture) snapshot() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]interface{}(nil), c.frames...)
}

func (c *fileTailUplinkCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

// setupFileTailTest 统一测试装配:tempdir home + 上行捕获 + 本机 deviceID 缝
// (dev-local,与 fileTailMirror 默认行同机)+ 缩短轮询/去抖 + 清场。单一
// Cleanup 严格按「Stop watchers → 还原各缝」顺序执行(缝还原时必须已无在跑
// goroutine)。
func setupFileTailTest(t *testing.T) (string, *fileTailUplinkCapture) {
	t.Helper()
	home := t.TempDir()
	capture := &fileTailUplinkCapture{}

	oldHome := listenerMirrorHome
	listenerMirrorHome = func() string { return home }
	resetMirrorCache := func() {
		listenerMirrorMu.Lock()
		listenerMirrorCache = nil
		listenerMirrorLoaded = false
		listenerMirrorMu.Unlock()
	}
	resetMirrorCache()

	oldUplink := listenerEventUplink
	listenerEventUplink = capture.record
	oldDevID := listenerLocalDeviceID
	listenerLocalDeviceID = func() string { return "dev-local" }

	oldPoll := listenerTailPollInterval.Load()
	oldFlush := listenerTailOffsetFlushInterval.Load()
	listenerTailPollInterval.Store(15 * time.Millisecond)
	listenerTailOffsetFlushInterval.Store(5 * time.Millisecond)

	StopEventListenerWatchers() // 上一测试残留清场

	t.Cleanup(func() {
		StopEventListenerWatchers()
		listenerEventUplink = oldUplink
		listenerLocalDeviceID = oldDevID
		listenerTailPollInterval.Store(oldPoll)
		listenerTailOffsetFlushInterval.Store(oldFlush)
		listenerMirrorHome = oldHome
		resetMirrorCache()
	})
	return home, capture
}

// fileTailRegistryIDs 在册 watcher id 快照(锁内读)。
func fileTailRegistryIDs() []string {
	listenerWatcherMu.Lock()
	defer listenerWatcherMu.Unlock()
	ids := make([]string, 0, len(listenerWatchers))
	for id := range listenerWatchers {
		ids = append(ids, id)
	}
	return ids
}

// fileTailRegistryEntry 取在册 watcher(注册表锁内读;path/match 构造后不变,
// 经注册表互斥量发布,读安全)。
func fileTailRegistryEntry(id string) (*fileTailWatcher, bool) {
	listenerWatcherMu.Lock()
	defer listenerWatcherMu.Unlock()
	w, ok := listenerWatchers[id]
	return w, ok
}

// fileTailMirror 造一行 file_tail 镜像(最小有效形态;默认本机设备行,
// 与 setupFileTailTest 的 dev-local 缝一致)。
func fileTailMirror(id, path, match, updatedAt string) ListenerMirror {
	return ListenerMirror{
		ID:             id,
		SourceType:     "file_tail",
		SourceDeviceID: "dev-local",
		Status:         "active",
		UpdatedAt:      updatedAt,
		SourceParams:   json.RawMessage(fmt.Sprintf(`{"path":%q,"match":%q}`, path, match)),
	}
}

// appendFileTailLine 向文件追加一段内容(测试速写助手;os 包无 AppendFile)。
func appendFileTailLine(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, werr := f.WriteString(s)
	cerr := f.Close()
	require.NoError(t, werr)
	require.NoError(t, cerr)
}

// waitFileTailCondition 轮询等待条件成立(默认 5s 上限)。
func waitFileTailCondition(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 10*time.Millisecond)
}

func fileTailLineFingerprint(line string) string {
	sum := sha256.Sum256([]byte(line))
	return hex.EncodeToString(sum[:])
}

// 1. 启动:有效行起 watcher;无效行(缺 path/match/updated_at、非 file_tail、
// 非 active)跳过;二次调用幂等。
func TestFileTailStartSpawnsIdempotentAndSkipsInvalid(t *testing.T) {
	_, _ = setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "app.log")
	rows := []ListenerMirror{
		fileTailMirror("L-ok", logPath, "ERROR", "2026-11-21T08:00:00Z"),
		{ID: "L-nopath", SourceType: "file_tail", SourceDeviceID: "dev-local", Status: "active", UpdatedAt: "2026-11-21T08:00:00Z",
			SourceParams: json.RawMessage(`{"match":"ERROR"}`)},
		{ID: "L-nomatch", SourceType: "file_tail", SourceDeviceID: "dev-local", Status: "active", UpdatedAt: "2026-11-21T08:00:00Z",
			SourceParams: json.RawMessage(`{"path":"/tmp/x.log"}`)},
		{ID: "L-noupdatedat", SourceType: "file_tail", SourceDeviceID: "dev-local", Status: "active",
			SourceParams: json.RawMessage(`{"path":"/tmp/x.log","match":"ERROR"}`)},
		{ID: "L-wrongtype", SourceType: "http_poll", SourceDeviceID: "dev-local", Status: "active", UpdatedAt: "2026-11-21T08:00:00Z",
			SourceParams: json.RawMessage(`{"path":"/tmp/x.log","match":"ERROR"}`)},
		fileTailMirror("L-paused", logPath, "ERROR", "2026-11-21T08:00:00Z"), // 下方改 paused
	}
	rows[len(rows)-1].Status = "paused"
	require.NoError(t, saveListenerMirrors(rows))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool {
		w, ok := fileTailRegistryEntry("L-ok")
		return ok && w.path == logPath && w.match == "ERROR"
	})
	assert.ElementsMatch(t, []string{"L-ok"}, fileTailRegistryIDs(), "只有有效且 active 的行起 watcher")

	// 幂等:重复 Start 不增不换实例。
	before, _ := fileTailRegistryEntry("L-ok")
	StartEventListenerWatchers()
	after, ok := fileTailRegistryEntry("L-ok")
	require.True(t, ok)
	assert.Same(t, before, after, "同参数重复 Start 必须复用在跑 watcher(幂等)")
	assert.Len(t, fileTailRegistryIDs(), 1)
}

// 2. 追加命中行 → 上行帧契约:fingerprint=小写 hex sha256(行内容剥尾换行)、
// summary≤200/excerpt≤512 均剥控制字符、fired_at RFC3339 UTC Z、detail.path。
func TestFileTailAppendMatchEmitsFrameContract(t *testing.T) {
	_, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "app.log")
	require.NoError(t, os.WriteFile(logPath, []byte("boot ok\n"), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-emit", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-emit"); return ok })

	// 三条不同内容的命中行:基础 / 控制字符剥离 / 截断上限。
	lineA := "boom ERROR happens"
	lineB := "ctrl\x01ERROR\x02here"
	lineC := strings.Repeat("x", 100) + " ERROR " + strings.Repeat("y", 600)
	appendFileTailLine(t, logPath, lineA+"\n")
	appendFileTailLine(t, logPath, lineB+"\n")
	appendFileTailLine(t, logPath, lineC+"\n")

	waitFileTailCondition(t, func() bool { return capture.count() >= 3 })
	frames := capture.snapshot()
	require.Len(t, frames, 3, "三条不同 fingerprint 各一帧")

	for _, frame := range frames[:2] {
		assert.Equal(t, models.AgentEventListenerEvent, frame["type"])
		assert.Equal(t, "L-emit", frame["listener_id"])
		assert.Equal(t, "file_tail", frame["source_type"])
	}
	assert.Equal(t, fileTailLineFingerprint(lineA), frames[0]["fingerprint"], "fingerprint=小写hex(sha256(行内容))")
	assert.Equal(t, lineA, frames[0]["summary"])
	detailA, ok := frames[0]["detail"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, logPath, detailA["path"])
	assert.Equal(t, lineA, detailA["matched_excerpt"])
	firedAt, ok := frames[0]["fired_at"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasSuffix(firedAt, "Z"), "fired_at 须 UTC Z 形态: %s", firedAt)
	_, err := time.Parse(time.RFC3339, firedAt)
	require.NoError(t, err, "fired_at 须可按 RFC3339 解析")

	// 控制字符:summary/excerpt 剥离([\x00-\x1f\x7f]),fingerprint 仍是原始行内容。
	assert.Equal(t, fileTailLineFingerprint(lineB), frames[1]["fingerprint"])
	assert.Equal(t, "ctrlERRORhere", frames[1]["summary"])
	detailB := frames[1]["detail"].(map[string]interface{})
	assert.Equal(t, "ctrlERRORhere", detailB["matched_excerpt"])

	// 截断:summary 200 rune / excerpt 512 rune。
	wantSummary := strings.Repeat("x", 100) + " ERROR " + strings.Repeat("y", 93)
	wantExcerpt := strings.Repeat("x", 100) + " ERROR " + strings.Repeat("y", 405)
	assert.Equal(t, wantSummary, frames[2]["summary"])
	detailC := frames[2]["detail"].(map[string]interface{})
	assert.Equal(t, wantExcerpt, detailC["matched_excerpt"])
}

// 3. 追加不命中行 → 无帧。
func TestFileTailAppendNonMatchNoFrame(t *testing.T) {
	_, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "app.log")
	require.NoError(t, os.WriteFile(logPath, []byte("init\n"), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-quiet", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-quiet"); return ok })
	appendFileTailLine(t, logPath, "all fine here\nnothing to see\n")

	require.Never(t, func() bool { return capture.count() > 0 }, 300*time.Millisecond, 20*time.Millisecond,
		"不命中行绝不可上行")
}

// 4. match 语义:可编译即正则(子串语义覆盖不到的形态能命中);编译失败
// 回退关键字包含。
func TestFileTailRegexAndBadRegexFallback(t *testing.T) {
	t.Run("regex match", func(t *testing.T) {
		_, capture := setupFileTailTest(t)
		logPath := filepath.Join(t.TempDir(), "app.log")
		require.NoError(t, os.WriteFile(logPath, []byte(""), 0o600))
		// "ERR-[0-9]+" 作为字面子串不会出现在行里,只有正则能命中。
		require.NoError(t, upsertListenerMirror(fileTailMirror("L-re", logPath, `ERR-[0-9]+`, "2026-11-21T08:00:00Z")))
		StartEventListenerWatchers()
		waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-re"); return ok })

		appendFileTailLine(t, logPath, "code ERR-42 found\n")
		waitFileTailCondition(t, func() bool { return capture.count() == 1 })
	})

	t.Run("bad regex falls back to keyword", func(t *testing.T) {
		_, capture := setupFileTailTest(t)
		logPath := filepath.Join(t.TempDir(), "app.log")
		require.NoError(t, os.WriteFile(logPath, []byte(""), 0o600))
		// "(unclosed" 编译必败 → 关键字包含语义。
		require.NoError(t, upsertListenerMirror(fileTailMirror("L-bad", logPath, "(unclosed", "2026-11-21T08:00:00Z")))
		StartEventListenerWatchers()
		waitFileTailCondition(t, func() bool {
			w, ok := fileTailRegistryEntry("L-bad")
			return ok && w.re == nil
		})

		appendFileTailLine(t, logPath, "has (unclosed inside\n")
		waitFileTailCondition(t, func() bool { return capture.count() == 1 })
	})
}

// 5. 截断/轮转:文件重写更短 → offset 越界归零重读,新内容命中不丢。
func TestFileTailTruncateResetsOffset(t *testing.T) {
	_, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "rotate.log")
	initial := strings.Repeat("old ", 40) + "\n" // 161 字节,无命中,推进 offset
	require.NoError(t, os.WriteFile(logPath, []byte(initial), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-trunc", logPath, "HIT", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-trunc"); return ok })
	// 等初始 offset 落盘(证明首轮读完,再截断)。
	offsetFile := listenerTailOffsetPath(logPath)
	waitFileTailCondition(t, func() bool {
		raw, err := os.ReadFile(offsetFile)
		return err == nil && strings.TrimSpace(string(raw)) == fmt.Sprintf("%d", len(initial))
	})

	// 重写更短(13 字节 < offset):归零重读 → "new HIT line" 命中。
	require.NoError(t, os.WriteFile(logPath, []byte("new HIT line\n"), 0o600))
	waitFileTailCondition(t, func() bool { return capture.count() == 1 })
	frame := capture.snapshot()[0]
	assert.Equal(t, fileTailLineFingerprint("new HIT line"), frame["fingerprint"])
}

// 6. 洪峰合并:同一内容连刷只上行一次(fingerprint 去重,不靠计时)。
func TestFileTailFloodCoalescing(t *testing.T) {
	_, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "flood.log")
	require.NoError(t, os.WriteFile(logPath, []byte(""), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-flood", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-flood"); return ok })

	for i := 0; i < 3; i++ {
		appendFileTailLine(t, logPath, "same ERROR spam\n")
		time.Sleep(30 * time.Millisecond) // 分批写,确保分轮读到仍被合并
	}
	time.Sleep(200 * time.Millisecond)

	frames := capture.snapshot()
	assert.Len(t, frames, 1, "与上次发出 fingerprint 相同的行必须跳过(本机防洪)")
	if len(frames) == 1 {
		assert.Equal(t, fileTailLineFingerprint("same ERROR spam"), frames[0]["fingerprint"])
	}
}

// 9. 文件消失:连续缺失过阈值 → 摘注册表 + 本地镜像置 error(source_gone),
// 不上行。
func TestFileTailFileDeletedSourceGone(t *testing.T) {
	_, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "gone.log")
	require.NoError(t, os.WriteFile(logPath, []byte("seed\n"), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-gone", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	w, ok := fileTailRegistryEntry("L-gone")
	require.True(t, ok)
	// 等首轮读完落 offset(此前删文件会与首轮 stat 竞态,sawFile 未置即入
	// 「等创建」模式——生产语义如此,测试须等状态就绪再触发删除)。
	offsetFile := listenerTailOffsetPath(logPath)
	waitFileTailCondition(t, func() bool {
		raw, err := os.ReadFile(offsetFile)
		return err == nil && strings.TrimSpace(string(raw)) == "5" // "seed\n"
	})

	require.NoError(t, os.Remove(logPath))
	waitFileTailCondition(t, func() bool {
		_, running := fileTailRegistryEntry("L-gone")
		if running {
			return false
		}
		m, ok := getListenerMirror("L-gone")
		return ok && m.Status == "error" && m.ErrorNote == "source_gone"
	})
	// 自停路径无人等待 goroutine 退出:等 doneCh 落定,防 t.TempDir 清理与
	// run 退出时的 offset 兜底强刷互踩(unlinkat directory not empty 竞态)。
	select {
	case <-w.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("自停后 watcher goroutine 未退出")
	}
	assert.Equal(t, 0, capture.count(), "source_gone 是本地终态,不上行")
}

// 10. 钩子整链:listener.upsert 帧 → 自动起 watcher;参数变更 → 重启(新
// 实例+新参数);status→paused → 停;listener.remove 帧 → 停。
func TestFileTailHookLifecycleViaFrames(t *testing.T) {
	setupFileTailTest(t)
	logA := filepath.Join(t.TempDir(), "a.log")
	logB := filepath.Join(t.TempDir(), "b.log")
	require.NoError(t, os.WriteFile(logA, []byte("seed-a\n"), 0o600))
	require.NoError(t, os.WriteFile(logB, []byte("seed-b\n"), 0o600))

	// 新 file_tail 镜像经下行帧落地 → 钩子自动起 watcher。
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		fileTailMirror("L-hook", logA, "ERROR", "2026-11-21T08:00:00Z"))))
	waitFileTailCondition(t, func() bool {
		w, ok := fileTailRegistryEntry("L-hook")
		return ok && w.path == logA && w.match == "ERROR"
	})
	first, _ := fileTailRegistryEntry("L-hook")

	// 参数变更(path+match 换)→ 停旧起新。
	changed := fileTailMirror("L-hook", logB, "WARN", "2026-11-21T09:00:00Z")
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, changed)))
	waitFileTailCondition(t, func() bool {
		w, ok := fileTailRegistryEntry("L-hook")
		return ok && w != first && w.path == logB && w.match == "WARN"
	})

	// 同参数重放(平局新胜会再触发钩子)→ 幂等复用,不重启。
	replay := fileTailMirror("L-hook", logB, "WARN", "2026-11-21T09:00:00Z")
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, replay)))
	cur, ok := fileTailRegistryEntry("L-hook")
	require.True(t, ok)
	second, _ := fileTailRegistryEntry("L-hook")
	assert.Same(t, cur, second, "同参数重放不得重启 watcher")

	// status→paused → 停。
	paused := fileTailMirror("L-hook", logB, "WARN", "2026-11-21T10:00:00Z")
	paused.Status = "paused"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, paused)))
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-hook"); return !ok })

	// 重新激活 → 再起;listener.remove 帧 → 钩子停(幂等)。
	active := fileTailMirror("L-hook", logB, "WARN", "2026-11-21T11:00:00Z")
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, active)))
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-hook"); return ok })
	require.NoError(t, handleListenerRemoveFrame(map[string]interface{}{
		"type":     models.AgentEventListenerRemove,
		"listener": map[string]interface{}{"id": "L-hook", "updated_at": "2026-11-21T12:00:00Z"},
	}))
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-hook"); return !ok })
}

// 10b. 参数变更重启覆盖 max_bytes:同 path/match 但行限变化的 upsert →
// 停旧起新(readCap 生效为新归一值;path/match 未变也必须重启)。
func TestFileTailMaxBytesChangeRestarts(t *testing.T) {
	setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "cap.log")
	require.NoError(t, os.WriteFile(logPath, []byte("seed\n"), 0o600))
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		fileTailMirror("L-capchg", logPath, "ERROR", "2026-11-21T08:00:00Z"))))
	waitFileTailCondition(t, func() bool {
		w, ok := fileTailRegistryEntry("L-capchg")
		return ok && w.readCap == listenerTailDefaultMaxBytes
	})
	first, _ := fileTailRegistryEntry("L-capchg")

	changed := fileTailMirror("L-capchg", logPath, "ERROR", "2026-11-21T09:00:00Z")
	changed.SourceParams = json.RawMessage(fmt.Sprintf(`{"path":%q,"match":"ERROR","max_bytes":4096}`, logPath))
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, changed)))

	waitFileTailCondition(t, func() bool {
		w, ok := fileTailRegistryEntry("L-capchg")
		return ok && w != first && w.readCap == 4096
	})
}

// 11. 全停:注册表清空、goroutine 真退出(doneCh 关闭)、二次调用不 panic。
func TestFileTailStopAllShutdown(t *testing.T) {
	setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "stop.log")
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-stop1", logPath, "ERROR", "2026-11-21T08:00:00Z")))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-stop2", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { return len(fileTailRegistryIDs()) == 2 })
	w1, ok := fileTailRegistryEntry("L-stop1")
	require.True(t, ok)

	StopEventListenerWatchers()
	assert.Empty(t, fileTailRegistryIDs())
	select {
	case <-w1.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher goroutine 未随全停退出(doneCh 未关闭)")
	}

	require.NotPanics(t, StopEventListenerWatchers, "二次全停须幂等无 panic")
}

// ---------------------------------------------------------------------------
// M2-B:断联暂存 outbox + reject 计数
// ---------------------------------------------------------------------------

// readOutboxLines 直读 outbox jsonl(测试断言「文件里有」)。
func readOutboxLines(t *testing.T, home string) []map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".aliang", "listener-outbox.jsonl"))
	if err != nil {
		return nil
	}
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &obj))
		out = append(out, obj)
	}
	return out
}

// 7a. 上行失败入暂存;通道恢复后 drain 按序补发并清账。
func TestFileTailOutboxFailThenDrain(t *testing.T) {
	home, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "outbox.log")
	require.NoError(t, os.WriteFile(logPath, []byte("seed\n"), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-ob", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-ob"); return ok })

	// 断联:上行恒败 → 命中帧入 outbox。
	capture.setFail(true)
	appendFileTailLine(t, logPath, "offline ERROR one\n")
	waitFileTailCondition(t, func() bool { return len(readOutboxLines(t, home)) == 1 })
	lines := readOutboxLines(t, home)
	assert.Equal(t, models.AgentEventListenerEvent, lines[0]["type"])
	assert.Equal(t, "L-ob", lines[0]["listener_id"])
	assert.Equal(t, fileTailLineFingerprint("offline ERROR one"), lines[0]["fingerprint"],
		"outbox 存的是完整 listener.event 帧")
	assert.True(t, listenerOutboxHasFrames())
	assert.Equal(t, 0, capture.count(), "失败期间直发通道零帧")

	// 恢复:drain 按序补发,成功移除,文件清空。
	capture.setFail(false)
	drainListenerOutbox()
	waitFileTailCondition(t, func() bool { return len(readOutboxLines(t, home)) == 0 })
	frames := capture.snapshot()
	require.Len(t, frames, 1, "drain 须补发暂存帧")
	assert.Equal(t, fileTailLineFingerprint("offline ERROR one"), frames[0]["fingerprint"])
	assert.False(t, listenerOutboxHasFrames())
}

// 7b. 溢出:超上限丢最旧 + 记摘要行;重放首败即停保留余量。
func TestFileTailOutboxCapAndPartialDrain(t *testing.T) {
	home, capture := setupFileTailTest(t)
	_ = home

	// 直接入队 52 帧:留 50,丢 2,附 {"dropped":2} 摘要行。
	for i := 0; i < 52; i++ {
		enqueueListenerOutbox(map[string]interface{}{
			"type":        models.AgentEventListenerEvent,
			"listener_id": "L-cap",
			"fingerprint": fmt.Sprintf("fp-%02d", i),
			"fired_at":    "2026-11-21T08:00:00Z",
			"summary":     fmt.Sprintf("frame %d", i),
			"source_type": "file_tail",
			"detail":      map[string]interface{}{"path": "/tmp/x.log", "matched_excerpt": "e"},
		})
	}
	lines := readOutboxLines(t, home)
	require.Len(t, lines, 51, "50 帧 + 恰 1 行累计摘要行")
	frames, markers, droppedTotal := 0, 0, 0
	for _, obj := range lines {
		if _, isFrame := obj["type"]; isFrame {
			frames++
			// 丢最旧:fp-00/fp-01 不在,fp-02 起保留。
			fp, _ := obj["fingerprint"].(string)
			assert.NotEqual(t, "fp-00", fp)
			assert.NotEqual(t, "fp-01", fp)
		} else {
			markers++
			d, _ := obj["dropped"].(float64)
			droppedTotal += int(d)
		}
	}
	assert.Equal(t, 50, frames)
	assert.Equal(t, 1, markers, "多次溢出合并为恰一行摘要(不逐次追加)")
	assert.Equal(t, 2, droppedTotal, "摘要行=累计丢弃总数(该会话两次溢出各丢 1)")

	// 半通道恢复:队首两帧(fp-02/fp-03)成功,fp-04 起恒败 → drain 首败即停。
	listenerEventUplink = func(frame map[string]interface{}) error {
		fp, _ := frame["fingerprint"].(string)
		idx, err := strconv.Atoi(strings.TrimPrefix(fp, "fp-"))
		if err != nil || idx >= 4 {
			return fmt.Errorf("still offline")
		}
		return capture.record(frame)
	}
	drainListenerOutbox()
	require.Len(t, capture.snapshot(), 2, "只有首两帧补发成功")
	lines = readOutboxLines(t, home)
	remaining := 0
	for _, obj := range lines {
		if _, isFrame := obj["type"]; isFrame {
			remaining++
		}
	}
	assert.Equal(t, 48, remaining, "首败即停:余下 48 帧原样保留")
	assert.True(t, listenerOutboxHasFrames())

	// 部分重放后的第二轮溢出:摘要行仍恰一行,总量继续累加(2+1=3)。
	for i := 100; i < 104; i++ { // 48+4=52 → 再丢 2?不:48 帧入 4 → 52 超 2 → 丢 2。
		enqueueListenerOutbox(map[string]interface{}{
			"type": models.AgentEventListenerEvent, "listener_id": "L-cap",
			"fingerprint": fmt.Sprintf("fp-%03d", i), "fired_at": "2026-11-21T08:00:00Z",
			"summary": "later", "source_type": "file_tail",
			"detail": map[string]interface{}{"path": "/tmp/x.log", "matched_excerpt": "e"},
		})
	}
	markerCount, markerTotal := 0, 0
	for _, obj := range readOutboxLines(t, home) {
		if _, isFrame := obj["type"]; !isFrame {
			markerCount++
			d, _ := obj["dropped"].(float64)
			markerTotal += int(d)
		}
	}
	assert.Equal(t, 1, markerCount, "跨溢出波次仍恰一行摘要")
	assert.Equal(t, 4, markerTotal, "累计总量 = 2(首轮)+2(次轮)")
}

// 7c. 坏行容忍:outbox 里的非法 JSON 行在读取时丢弃,不堵其后的重放。
func TestFileTailOutboxCorruptLineDropped(t *testing.T) {
	home, capture := setupFileTailTest(t)
	path := filepath.Join(home, ".aliang", "listener-outbox.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not json\n"+`{"type":"listener.event","listener_id":"L-bad","fingerprint":"fp-ok"}`+"\n"), 0o600))

	drainListenerOutbox()
	require.Len(t, capture.snapshot(), 1, "坏行丢弃,好帧照常补发")
	assert.Len(t, readOutboxLines(t, home), 0, "补发成功后文件清空")
}

// 8. reject 分档:device_unregistered counted×5 → 本地置 error 停 watcher;
// fired/expired → 停 + 镜像置终态;deleted/paused → 停不计数;counted=false
// 与未知 note 不计数不误伤。
func TestFileTailRejectHandling(t *testing.T) {
	setupFileTailTest(t)
	newWatcher := func(t *testing.T, id string) {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), id+".log")
		require.NoError(t, os.WriteFile(logPath, []byte("seed\n"), 0o600))
		require.NoError(t, upsertListenerMirror(fileTailMirror(id, logPath, "ERROR", "2026-11-21T08:00:00Z")))
		StartEventListenerWatchers()
		waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry(id); return ok })
	}
	rejectFrame := func(id, note string, counted bool) map[string]interface{} {
		return map[string]interface{}{
			"type": models.AgentEventListenerEventReject, "listener_id": id,
			"permanent": true, "note": note, "counted": counted,
		}
	}

	t.Run("device_unregistered counted x5", func(t *testing.T) {
		id := "L-rej"
		newWatcher(t, id)
		for i := 1; i <= 4; i++ {
			handleListenerEventRejectFrame(rejectFrame(id, "device_unregistered", true))
			m, ok := getListenerMirror(id)
			require.True(t, ok)
			assert.Equal(t, int64(i), m.RejectCount, "counted 拒绝逐次 +1")
			assert.Equal(t, "active", m.Status, "未达上限不打 error")
		}
		_, running := fileTailRegistryEntry(id)
		assert.True(t, running, "未达上限 watcher 不得停")

		handleListenerEventRejectFrame(rejectFrame(id, "device_unregistered", true))
		waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry(id); return !ok })
		m, ok := getListenerMirror(id)
		require.True(t, ok)
		assert.Equal(t, int64(5), m.RejectCount)
		assert.Equal(t, "error", m.Status)
		assert.Equal(t, "rejected_5x", m.ErrorNote)
	})

	t.Run("fired stops and marks terminal", func(t *testing.T) {
		id := "L-fired"
		newWatcher(t, id)
		handleListenerEventRejectFrame(rejectFrame(id, "fired", false))
		waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry(id); return !ok })
		m, _ := getListenerMirror(id)
		assert.Equal(t, "fired", m.Status)
		assert.Equal(t, int64(0), m.RejectCount, "终态兜底不计数")
	})

	t.Run("expired stops and marks terminal", func(t *testing.T) {
		id := "L-exp"
		newWatcher(t, id)
		handleListenerEventRejectFrame(rejectFrame(id, "expired", false))
		waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry(id); return !ok })
		m, _ := getListenerMirror(id)
		assert.Equal(t, "expired", m.Status)
	})

	t.Run("deleted paused stop without counting", func(t *testing.T) {
		for _, note := range []string{"deleted", "paused"} {
			id := "L-" + note
			newWatcher(t, id)
			handleListenerEventRejectFrame(rejectFrame(id, note, false))
			waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry(id); return !ok })
			m, ok := getListenerMirror(id)
			require.True(t, ok)
			assert.Equal(t, int64(0), m.RejectCount, "%s 不计数", note)
			assert.Equal(t, "active", m.Status, "%s 不改镜像状态(upsert 帧自会收敛)", note)
		}
	})

	t.Run("not counted and unknown note ignored", func(t *testing.T) {
		id := "L-quiet"
		newWatcher(t, id)
		handleListenerEventRejectFrame(rejectFrame(id, "device_unregistered", false))
		handleListenerEventRejectFrame(rejectFrame(id, "mystery_note", true))
		handleListenerEventRejectFrame(map[string]interface{}{"type": models.AgentEventListenerEventReject}) // 缺 id
		m, ok := getListenerMirror(id)
		require.True(t, ok)
		assert.Equal(t, int64(0), m.RejectCount)
		assert.Equal(t, "active", m.Status)
		_, running := fileTailRegistryEntry(id)
		assert.True(t, running, "未计数/未知 note 不得停 watcher")
	})
}

// 整链:reject 帧经 handleRemoteAgentMessage 派发生效;agent.registered 到达
// 触发 outbox 重放钩子(重连补发接线的 ws 侧证据)。
func TestFileTailRejectDispatchAndReplayHook(t *testing.T) {
	home, _ := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "dispatch.log")
	require.NoError(t, os.WriteFile(logPath, []byte("seed\n"), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-disp", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-disp"); return ok })

	var written []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			written = append(written, m)
		}
		return nil
	}

	// registered 帧 → 重放钩子必须被调(此处 swap 记录调用以证 ws 侧接线存在;
	// 默认接线为 safeGo drain,真实补发路径由 7a/7b 覆盖)。
	replayed := make(chan struct{}, 1)
	oldHook := listenerOutboxReplayHook
	listenerOutboxReplayHook = func() { replayed <- struct{}{} }
	t.Cleanup(func() { listenerOutboxReplayHook = oldHook })
	service.handleRemoteAgentMessage(context.Background(),
		map[string]interface{}{"type": models.AgentEventRegistered, "device_id": "dev-1"}, writeJSON)
	select {
	case <-replayed:
	case <-time.After(2 * time.Second):
		t.Fatal("agent.registered 必须触发 outbox 重放钩子")
	}

	// reject 帧经派发(read loop 同路径)→ watcher 停止 + 镜像置终态。
	service.handleRemoteAgentMessage(context.Background(),
		map[string]interface{}{
			"type": models.AgentEventListenerEventReject, "listener_id": "L-disp",
			"permanent": true, "note": "fired", "counted": false,
		}, writeJSON)
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-disp"); return !ok })
	m, ok := getListenerMirror("L-disp")
	require.True(t, ok)
	assert.Equal(t, "fired", m.Status)
	// registered 分支自身的 terminal.sessions/ai.approval.sync 汇报是既有
	// 语义;此处只钉 listener 帧族不产生 agent.error 回帧(畸形不回错)。
	for _, frame := range written {
		assert.NotEqual(t, models.AgentEventError, frame["type"],
			"reject/registered 帧族不得产生 error 回帧")
	}
}

// 12. 跨设备 spawn 门:他机行(source_device_id ≠ 本机)镜像保留但不起
// watcher——boot 全量拉取是账号级的,本机文件内容不得按他人 listener_id
// 上行(跨设备误归因);本机行照常起。经 handleListenerUpsertFrame 整链
// (upsert 路径与 boot Start 路径共用 syncListenerWatcherForMirror 单一门)。
func TestFileTailForeignDeviceRowNotSpawned(t *testing.T) {
	setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "foreign.log")
	require.NoError(t, os.WriteFile(logPath, []byte("foreign ERROR line\n"), 0o600))

	foreign := fileTailMirror("L-foreign", logPath, "ERROR", "2026-11-21T08:00:00Z")
	foreign.SourceDeviceID = "dev-other"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, foreign)))

	_, running := fileTailRegistryEntry("L-foreign")
	assert.False(t, running, "他机行不得入本地 watch")
	_, mirrored := getListenerMirror("L-foreign")
	require.True(t, mirrored, "跨设备行镜像仍保留(供本机 UI 展示)")

	// 本机行(同路径同参数)→ 正常起。
	own := fileTailMirror("L-own", logPath, "ERROR", "2026-11-21T08:00:00Z")
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, own)))
	waitFileTailCondition(t, func() bool {
		w, ok := fileTailRegistryEntry("L-own")
		return ok && w.path == logPath
	})
	_, running = fileTailRegistryEntry("L-foreign")
	assert.False(t, running, "本机行起 watcher 与他机行不 spawn 互不影响")

	// 设备重指派收敛:本机在跑行被改成他机 → watcher 停。
	reassigned := fileTailMirror("L-own", logPath, "ERROR", "2026-11-21T09:00:00Z")
	reassigned.SourceDeviceID = "dev-other"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, reassigned)))
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-own"); return !ok })
}

// 13. 行限(read-span cap):无换行巨文件内存有界——超限行整段跳过不发事件、
// offset 越过;其后正常追加行照常匹配。max_bytes 显式设置同样生效。
func TestFileTailOversizedLineSkipped(t *testing.T) {
	t.Run("default cap no-newline file", func(t *testing.T) {
		_, capture := setupFileTailTest(t)
		logPath := filepath.Join(t.TempDir(), "huge.log")
		// 2MiB 无换行(内嵌 ERROR),默认行限 1MiB → 整段跳过。
		big := strings.Repeat("a", 1<<20) + "ERROR" + strings.Repeat("b", 1<<20)
		require.NoError(t, os.WriteFile(logPath, []byte(big), 0o600))
		require.NoError(t, upsertListenerMirror(fileTailMirror("L-huge", logPath, "ERROR", "2026-11-21T08:00:00Z")))

		StartEventListenerWatchers()
		// offset 落到 EOF(2MiB+5)即证跳过完成;期间零事件。
		offsetFile := listenerTailOffsetPath(logPath)
		waitFileTailCondition(t, func() bool {
			raw, err := os.ReadFile(offsetFile)
			return err == nil && strings.TrimSpace(string(raw)) == fmt.Sprintf("%d", len(big))
		})
		assert.Equal(t, 0, capture.count(), "超限无换行巨行不得发事件")

		// 后续正常追加(补换行终结残行 + 正常命中行)照常工作。
		appendFileTailLine(t, logPath, "\ntail ERROR ok\n")
		waitFileTailCondition(t, func() bool { return capture.count() == 1 })
		assert.Equal(t, fileTailLineFingerprint("tail ERROR ok"), capture.snapshot()[0]["fingerprint"])
	})

	t.Run("explicit max_bytes honored", func(t *testing.T) {
		_, capture := setupFileTailTest(t)
		logPath := filepath.Join(t.TempDir(), "capped.log")
		require.NoError(t, os.WriteFile(logPath, []byte(""), 0o600))
		row := fileTailMirror("L-capped", logPath, "ERROR", "2026-11-21T08:00:00Z")
		row.SourceParams = json.RawMessage(fmt.Sprintf(`{"path":%q,"match":"ERROR","max_bytes":4096}`, logPath))
		require.NoError(t, upsertListenerMirror(row))

		StartEventListenerWatchers()
		w, ok := fileTailRegistryEntry("L-capped")
		require.True(t, ok)
		assert.Equal(t, 4096, w.readCap, "显式 max_bytes 生效为行限")

		// 6KiB 单行(含命中词)超限整段丢弃;紧随的正常行照常命中。
		appendFileTailLine(t, logPath, strings.Repeat("z", 6000)+"ERROR\n")
		appendFileTailLine(t, logPath, "small ERROR line\n")
		waitFileTailCondition(t, func() bool { return capture.count() == 1 })
		assert.Equal(t, fileTailLineFingerprint("small ERROR line"), capture.snapshot()[0]["fingerprint"],
			"超限行丢弃,正常行照发")
	})
}

// 14. 多字节截断:CJK/emoji 混合行过 200-rune summary 边界 → 恰 200 个合法
// rune(无乱码截断),matched_excerpt 同理按 rune 截 512。
func TestFileTailMultibyteSummaryClip(t *testing.T) {
	_, capture := setupFileTailTest(t)
	logPath := filepath.Join(t.TempDir(), "cjk.log")
	require.NoError(t, os.WriteFile(logPath, []byte(""), 0o600))
	require.NoError(t, upsertListenerMirror(fileTailMirror("L-cjk", logPath, "ERROR", "2026-11-21T08:00:00Z")))

	StartEventListenerWatchers()
	waitFileTailCondition(t, func() bool { _, ok := fileTailRegistryEntry("L-cjk"); return ok })

	line := strings.Repeat("错", 150) + " ERROR " + strings.Repeat("🎉", 100) // 257 rune
	appendFileTailLine(t, logPath, line+"\n")
	waitFileTailCondition(t, func() bool { return capture.count() == 1 })

	frame := capture.snapshot()[0]
	wantSummary := strings.Repeat("错", 150) + " ERROR " + strings.Repeat("🎉", 43)
	summary, _ := frame["summary"].(string)
	assert.Equal(t, wantSummary, summary, "summary 按 rune 截断,恰 200 个合法 rune")
	assert.Equal(t, 200, utf8.RuneCountInString(summary))
	assert.Equal(t, fileTailLineFingerprint(line), frame["fingerprint"], "指纹仍是完整原始行")
	detail, _ := frame["detail"].(map[string]interface{})
	excerpt, _ := detail["matched_excerpt"].(string)
	assert.Equal(t, 257, utf8.RuneCountInString(excerpt), "257 rune 行未过 512 上限,excerpt 全量保留")
	assert.Equal(t, line, excerpt)
}
