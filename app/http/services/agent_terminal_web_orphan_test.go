package services

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// shrinkWebOrphanDelay points the web-leg orphan delay at a tiny test value and
// restores it when the test ends. Plain var (process-global), so no t.Parallel.
func shrinkWebOrphanDelay(t *testing.T, d time.Duration) {
	t.Helper()
	orig := agentWebOrphanCloseDelay
	agentWebOrphanCloseDelay = d
	t.Cleanup(func() { agentWebOrphanCloseDelay = orig })
}

// TestWebOrphanCloseKillsAfterDelay 连接退出调度的孤儿到期即杀。占坑根因:
// PTY 空闲无输出→写失败探测永不触发→旧死 WS 会话占 agentMaxTerminalSessions
// (全局 4 坑,手机共享)直到 30m idle——刷新恢复壳的并发 create 全撞上限。
func TestWebOrphanCloseKillsAfterDelay(t *testing.T) {
	shrinkWebOrphanDelay(t, 50*time.Millisecond)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-orphan", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	var framesOwn []map[string]interface{}
	m.mu.Lock()
	m.sessions["t-orphan"] = live
	m.mu.Unlock()
	live.swapConn(frameSink(&framesOwn), "connA") // 同连接身份:liveSink==from

	m.ScheduleWebOrphanClose("t-orphan", "connA")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(kills) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(kills); got == 0 {
		t.Fatalf("orphan reap must kill the session after the delay (kills=%d)", got)
	}
	m.mu.Lock()
	_, timerStill := m.orphanTimers["t-orphan"]
	m.mu.Unlock()
	if timerStill {
		t.Fatalf("fired timer entry must be dropped from orphanTimers")
	}
}

// TestWebOrphanCloseCancelLeavesSession 取消(显式 close 出册路径)后到期不杀:
// 会话走正常 close,孤儿回收不得误触。
func TestWebOrphanCloseCancelLeavesSession(t *testing.T) {
	shrinkWebOrphanDelay(t, 50*time.Millisecond)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-orphan-cancel", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-orphan-cancel"] = live
	m.mu.Unlock()

	m.ScheduleWebOrphanClose("t-orphan-cancel", "connA")
	m.cancelWebOrphanClose("t-orphan-cancel")

	time.Sleep(3 * agentWebOrphanCloseDelay)
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("cancelled orphan reap must not kill, kills=%d", got)
	}
	m.mu.Lock()
	_, timerStill := m.orphanTimers["t-orphan-cancel"]
	m.mu.Unlock()
	if timerStill {
		t.Fatalf("cancelled timer entry must be dropped")
	}
}

// TestWebOrphanTimerDroppedOnFreshCreate 同 ID 新建落地即清残留孤儿定时条目,
// 斩断「timer 回调删条目→get」与「create 注册会话」交错时回调误杀新会话的
// 竞态(create 在同锁内直清,不等回调)。
func TestWebOrphanTimerDroppedOnFreshCreate(t *testing.T) {
	warmAuthorizedDirs(t)
	m := newAgentTerminalManager()
	// 前会话已不在册(被回收),但孤儿定时条目因回调竞态尚未落地删除的场景。
	// 手工放一个永不触发的假条目(真 timer 到期即删,这里用长延迟模拟未到期)。
	m.mu.Lock()
	m.orphanTimers["t-fresh"] = time.AfterFunc(time.Hour, func() {})
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		if old := m.orphanTimers["t-fresh"]; old != nil {
			old.Stop()
		}
		m.mu.Unlock()
	})

	m.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		s := newAttachTestSession(sessionID, newTerminalRingBuffer(4096))
		s.shell = shell
		s.cwd = cwd
		return s, nil, nil
	}

	writeJSON := func(payload interface{}) error { return nil }
	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-fresh",
	}, writeJSON)

	m.mu.Lock()
	_, timerStill := m.orphanTimers["t-fresh"]
	_, sessionLive := m.sessions["t-fresh"]
	m.mu.Unlock()
	if timerStill {
		t.Fatalf("fresh create must drop the stale orphan timer entry")
	}
	if !sessionLive {
		t.Fatalf("fresh create must register the new session")
	}
}

// TestCreateReapsOrphansOnLimit 撞 agentMaxTerminalSessions 时 create 同步回收
// 网页孤儿腾坑重试:10s 宽容窗让位于等待中的 create(孤儿=网页死 WS 会话,
// 恢复壳永不 attach 旧 ID,让位无损)。无孤儿可回收(真满)则原 limit 错误照报。
func TestCreateReapsOrphansOnLimit(t *testing.T) {
	warmAuthorizedDirs(t)
	m := newAgentTerminalManager()
	// 填满 4 坑:2 个挂孤儿定时器(死 WS 会话),2 个不挂(占用但非孤儿)
	for _, id := range []string{"occ-1", "occ-2", "occ-3", "occ-4"} {
		s := newAttachTestSession(id, newTerminalRingBuffer(4096))
		killRecorder(s)
		m.mu.Lock()
		m.sessions[id] = s
		m.mu.Unlock()
	}
	m.ScheduleWebOrphanClose("occ-1", "old-a")
	m.ScheduleWebOrphanClose("occ-2", "old-b")

	m.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		s := newAttachTestSession(sessionID, newTerminalRingBuffer(4096))
		return s, nil, nil
	}

	var frames []map[string]interface{}
	writeJSON := func(payload interface{}) error {
		if f, ok := payload.(map[string]interface{}); ok {
			frames = append(frames, f)
		}
		return nil
	}
	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "occ-5",
	}, writeJSON)

	m.mu.Lock()
	_, occ1 := m.sessions["occ-1"]
	_, occ2 := m.sessions["occ-2"]
	_, occ5 := m.sessions["occ-5"]
	timersLeft := len(m.orphanTimers)
	m.mu.Unlock()
	if occ1 || occ2 {
		t.Fatalf("orphaned sessions must be reaped: occ1=%v occ2=%v", occ1, occ2)
	}
	if !occ5 {
		t.Fatalf("create must succeed after reaping orphans freed a slot")
	}
	if timersLeft != 0 {
		t.Fatalf("orphanTimers must be empty after reap, left=%d", timersLeft)
	}
	created := false
	for _, f := range frames {
		if f["type"] == models.AgentEventTerminalCreated && f["session_id"] == "occ-5" {
			created = true
		}
	}
	if !created {
		t.Fatalf("create must emit terminal.created for the new session, frames=%v", frames)
	}
}

// TestCreateLimitErrorWithoutOrphans 坑真满且零孤儿:limit 错误照报,不误杀
// 非孤儿占用会话(手机腿会话在此形态下零影响)。
func TestCreateLimitErrorWithoutOrphans(t *testing.T) {
	warmAuthorizedDirs(t)
	m := newAgentTerminalManager()
	for _, id := range []string{"full-1", "full-2", "full-3", "full-4"} {
		s := newAttachTestSession(id, newTerminalRingBuffer(4096))
		killRecorder(s)
		m.mu.Lock()
		m.sessions[id] = s
		m.mu.Unlock()
	}

	m.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		t.Fatalf("startProcess must not run when the limit persists with zero orphans")
		return nil, nil, nil
	}

	var lastErr string
	writeJSON := func(payload interface{}) error {
		if f, ok := payload.(map[string]interface{}); ok {
			if e, ok := f["error"].(string); ok { // payload 的 error 字段=err.Error() 字符串
				lastErr = e
			}
		}
		return nil
	}
	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "full-5",
	}, writeJSON)

	m.mu.Lock()
	_, stillFour := m.sessions["full-5"]
	count := len(m.sessions)
	m.mu.Unlock()
	if stillFour {
		t.Fatalf("create must fail when the limit holds with zero orphans")
	}
	if count != 4 {
		t.Fatalf("non-orphan occupants must be untouched, sessions=%d", count)
	}
	if lastErr == "" || !strings.Contains(lastErr, "limit reached") {
		t.Fatalf("want limit-reached error, got %q", lastErr)
	}
}

// warmAuthorizedDirs 预热授权目录缓存:create 的 CWD 解析(agentAuthorizedExecution
// Directories)冷缓存会触发全量会话转录扫描(实测 12s+),任何 create 类测试
// 不预热就撞超时。同包直写缓存,单飞刷新永不触发。
func warmAuthorizedDirs(t *testing.T) {
	t.Helper()
	setAgentAuthorizedExecutionDirectoriesCache([]string{"/tmp"})
}

// frameSink 帧收集器(直测形态:writeViaLive 全部发生在主 goroutine,无并发
// 写者,不需要锁——真实 copy 循环的并发面属既有覆盖,不在此混测)。
func frameSink(store *[]map[string]interface{}) agentTerminalWriter {
	return func(payload interface{}) error {
		if f, ok := payload.(map[string]interface{}); ok {
			*store = append(*store, f)
		}
		return nil
	}
}

func sinkHasOutput(store []map[string]interface{}, data string) bool {
	for _, f := range store {
		if f["type"] == models.AgentEventTerminalOutput && f["data"] == data {
			return true
		}
	}
	return false
}

// TestAttachLiveSwapsLiveWriterToNewConnection 会话保活主断链修复的回归钉:
// create 时输出道初值=创建连接;attachLive 换道后 writeViaLive 只达新连接。
// (直测间接层——真实 copy 循环的 gate/meter 交互属既有覆盖,不在此混测。)
func TestAttachLiveSwapsLiveWriterToNewConnection(t *testing.T) {
	warmAuthorizedDirs(t)
	shrinkWebOrphanDelay(t, 50*time.Millisecond)
	m := newAgentTerminalManager()
	shellAlive := make(chan struct{})
	defer close(shellAlive)
	m.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		s := newAttachTestSession(sessionID, newTerminalRingBuffer(4096))
		s.waiter = func() (int, error) {
			<-shellAlive // waitTerminal 立即返回会删册,attach 就 miss 了
			return 0, nil
		}
		return s, nil, nil // 无 readers:copy 循环不孵化,纯测换道语义
	}

	var framesA, framesB []map[string]interface{}
	wA := frameSink(&framesA)
	wB := frameSink(&framesB)

	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "win-swap",
	}, wA)

	m.mu.Lock()
	sess := m.sessions["win-swap"]
	m.mu.Unlock()
	if sess == nil {
		t.Fatal("create must register the session")
	}
	// create 后输出道初值=A:writeViaLive 只达 A
	if err := sess.writeViaLive(map[string]interface{}{
		"type": models.AgentEventTerminalOutput, "session_id": "win-swap", "data": "before-attach",
	}); err != nil {
		t.Fatalf("writeViaLive before attach: %v", err)
	}
	if !sinkHasOutput(framesA, "before-attach") {
		t.Fatal("create 后输出必须到达创建连接 A")
	}

	if !m.attachLive("win-swap", 24, 80, "", wB, "connB") {
		t.Fatal("attachLive must hit the live session")
	}
	if err := sess.writeViaLive(map[string]interface{}{
		"type": models.AgentEventTerminalOutput, "session_id": "win-swap", "data": "after-swap",
	}); err != nil {
		t.Fatalf("writeViaLive after attach: %v", err)
	}
	if !sinkHasOutput(framesB, "after-swap") {
		t.Fatal("attach 后输出必须换道到新连接 B")
	}
	if stale := sinkHasOutput(framesA, "after-swap"); stale {
		t.Fatal("attach 后输出不得继续流向旧连接 A")
	}
}

// TestWebOrphanCallbackSparesReattachedSession 反向竞态钉:attach 重附着之后,
// 旧连接迟到的孤儿调度不得重打 detached 戳、回调不得杀掉正被使用的会话。
func TestWebOrphanCallbackSparesReattachedSession(t *testing.T) {
	shrinkWebOrphanDelay(t, 50*time.Millisecond)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-orphan-reattach", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-orphan-reattach"] = live
	m.mu.Unlock()

	m.ScheduleWebOrphanClose("t-orphan-reattach", "old-conn") // 旧连接断开:arm(from=旧 sink)
	// 模拟重附着(attachLive 清戳+删定时器;直接真调走完整路径)
	var frames []map[string]interface{}
	if !m.attachLive("t-orphan-reattach", 24, 80, "", frameSink(&frames), "new-conn") {
		t.Fatal("attachLive must hit")
	}
	m.mu.Lock()
	stampZero := live.detachedAt.IsZero()
	m.mu.Unlock()
	t.Logf("DEBUG after attachLive: stampZero=%v", stampZero)
	// 旧连接的清理迟到:重 arm——新语义下不得重打戳
	m.ScheduleWebOrphanClose("t-orphan-reattach", "old-conn") // 迟到的旧连接清理:身份已让位
	m.mu.Lock()
	stampZero2 := live.detachedAt.IsZero()
	m.mu.Unlock()
	t.Logf("DEBUG after stale re-arm: stampZero=%v", stampZero2)

	time.Sleep(3 * agentWebOrphanCloseDelay)
	m.mu.Lock()
	timersLeft := len(m.orphanTimers)
	m.mu.Unlock()
	t.Logf("DEBUG kills=%d timersLeft=%d detachedZero=%v liveStillReg=%v", atomic.LoadInt32(kills), timersLeft, live.detachedAt.IsZero(), func() bool { m.mu.Lock(); defer m.mu.Unlock(); _, ok := m.sessions["t-orphan-reattach"]; return ok }())
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("re-attached session must survive stale orphan arm, kills=%d", got)
	}
}

// TestReapSkipsAttachedSession 撞坑回收只取无主孤儿:已被重附着(戳清零)的
// 会话即使定时器还挂着,也不得被 reapWebOrphansNow 波及。
func TestReapSkipsAttachedSession(t *testing.T) {
	m := newAgentTerminalManager()
	orphan := newAttachTestSession("t-reap-orphan", newTerminalRingBuffer(4096))
	attached := newAttachTestSession("t-reap-attached", newTerminalRingBuffer(4096))
	orphanKills := killRecorder(orphan)
	attachedKills := killRecorder(attached)
	m.mu.Lock()
	m.sessions["t-reap-orphan"] = orphan
	m.sessions["t-reap-attached"] = attached
	m.mu.Unlock()
	m.ScheduleWebOrphanClose("t-reap-orphan", orphan.writeViaLive)
	m.ScheduleWebOrphanClose("t-reap-attached", attached.writeViaLive)
	// attached 被重附着:清戳(attachLive 同锁语义)
	m.mu.Lock()
	attached.detachedAt = time.Time{}
	m.mu.Unlock()

	n := m.reapWebOrphansNow()
	if n != 1 {
		t.Fatalf("reap must take exactly the orphaned one, took=%d", n)
	}
	if atomic.LoadInt32(orphanKills) == 0 {
		t.Fatal("orphan must be killed")
	}
	if atomic.LoadInt32(attachedKills) != 0 {
		t.Fatal("re-attached session must survive the reap")
	}
}
