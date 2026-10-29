package services

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// watcher 扇出测试的共同基建:线程安全帧收集器。
type emitterWatchRecorder struct {
	mu     sync.Mutex
	frames []map[string]interface{}
}

func (r *emitterWatchRecorder) write(value interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, value.(map[string]interface{}))
	return nil
}

func (r *emitterWatchRecorder) snapshot() []map[string]interface{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]interface{}, len(r.frames))
	copy(out, r.frames)
	return out
}

// watcherCopyForTest 持锁读取 emitter 的 watcher 拷贝(pump goroutine 会并发
// 删键,测试侧必须同锁)。
func watcherCopyForTest(e *agentAIRunEmitter) map[string]*agentSessionWatchSink {
	e.watchMu.Lock()
	defer e.watchMu.Unlock()
	out := make(map[string]*agentSessionWatchSink, len(e.watchers))
	for connID, sink := range e.watchers {
		out[connID] = sink
	}
	return out
}

// waitFor 斩轮询条件,超时即 Fatal(异步扇出的帧不保证同步可见)。
func waitForWatchCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

func emitterWatchPayload(deltaType string, sessionID string, n int) map[string]interface{} {
	return map[string]interface{}{
		"type":       deltaType,
		"session_id": sessionID,
		"message_id": "msg-1",
		"channel":    "text",
		"delta":      fmt.Sprintf("frame-%d", n),
	}
}

// ①播种收到帧:manager 注册表里的 watcher 被 runEmitter 构造时播种进拷贝,
// delta 与终态各恰好收到一份,且与属主拿到同一 run_id/event_seq 序列。
func TestAgentAIEmitterWatchersSeedAndFanout(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	watcher := &emitterWatchRecorder{}
	m.watchSession("s-1", "conn-a", watcher.write)

	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)
	if _, ok := watcherCopyForTest(emitter)["conn-a"]; !ok {
		t.Fatal("runEmitter did not seed the manager watcher registry into its copy")
	}

	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := emitter.emit(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": "s-1",
		"message_id": "msg-1",
	}); err != nil {
		t.Fatal(err)
	}

	waitForWatchCond(t, 2*time.Second, "watcher to receive the delta+done frames", func() bool {
		return len(watcher.snapshot()) == 2
	})

	ownerFrames := owner.snapshot()
	if len(ownerFrames) != 2 {
		t.Fatalf("owner frames = %d, want 2: %#v", len(ownerFrames), ownerFrames)
	}
	watchFrames := watcher.snapshot()
	if len(watchFrames) != 2 {
		t.Fatalf("watcher frames = %d, want exactly 2 (no dup/no loss): %#v", len(watchFrames), watchFrames)
	}
	for i, frame := range watchFrames {
		if frame["type"] != ownerFrames[i]["type"] {
			t.Fatalf("watcher frame %d type = %v, want %v", i, frame["type"], ownerFrames[i]["type"])
		}
		if frame["run_id"] != "run-1" {
			t.Fatalf("watcher frame %d run_id = %v, want run-1", i, frame["run_id"])
		}
		if frame["event_seq"] != ownerFrames[i]["event_seq"] {
			t.Fatalf("watcher frame %d event_seq = %v, want owner's %v", i, frame["event_seq"], ownerFrames[i]["event_seq"])
		}
	}
	if watchFrames[1]["type"] != models.AgentEventAIDone {
		t.Fatalf("watcher last frame = %v, want ai.done", watchFrames[1]["type"])
	}
}

// ②慢 watcher 不阻塞主写者:写桩永不返回时,主写者的下一条 emit 仍即时完成;
// 缓冲打满后慢 watcher 被摘除,不再拖累任何 emit。
func TestAgentAIEmitterSlowWatcherDoesNotBlockOwner(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	block := make(chan struct{})
	slow := agentTerminalWriter(func(value interface{}) error {
		<-block // 永不返回:模拟一个卡死的属主之外连接
		return nil
	})
	m.watchSession("s-1", "conn-slow", slow)
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)

	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	// 第二帧的 emit 耗时上界:同步扇出的实现会在这里永远卡住。
	start := time.Now()
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 2)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("second owner emit took %v; slow watcher is blocking the run", elapsed)
	}

	// 打满缓冲(64)后第 65 帧:emit 期摘除,慢 watcher 从拷贝与 manager 注册表同时消失。
	for n := 3; n <= agentAIWatcherQueueCap+2; n++ {
		if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", n)); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("emits after buffer overflow took %v total; slow watcher leaked into the run path", elapsed)
		}
	}
	waitForWatchCond(t, 2*time.Second, "slow watcher to be dropped from the emitter copy", func() bool {
		return len(watcherCopyForTest(emitter)) == 0
	})
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("manager registry still holds the slow watcher: %#v", snap)
	}
	if len(owner.snapshot()) != agentAIWatcherQueueCap+2 {
		t.Fatalf("owner frames = %d, want %d (owner writes never blocked)",
			len(owner.snapshot()), agentAIWatcherQueueCap+2)
	}
	close(block)
}

// ③写错误 watcher:第一次投递即报错 → 摘除(emitter 拷贝 + manager 注册表),
// 后续帧不再向它重投;摘除伴随的一次性 dropped 通知帧不计入投递(R1)。
func TestAgentAIEmitterWriteErrorWatcherDropped(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	var callMu sync.Mutex
	var calls []string
	failing := agentTerminalWriter(func(value interface{}) error {
		callMu.Lock()
		calls = append(calls, fmt.Sprint(value.(map[string]interface{})["type"]))
		callMu.Unlock()
		return fmt.Errorf("conn gone")
	})
	runFrameCalls := func() int {
		callMu.Lock()
		defer callMu.Unlock()
		n := 0
		for _, typeName := range calls {
			if typeName != models.AgentEventAISessionWatchDropped {
				n++
			}
		}
		return n
	}
	m.watchSession("s-1", "conn-err", failing)
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)

	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	waitForWatchCond(t, 2*time.Second, "failing watcher to be dropped from the emitter copy", func() bool {
		return len(watcherCopyForTest(emitter)) == 0
	})
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("manager registry still holds the failing watcher: %#v", snap)
	}

	// 摘除伴随的 dropped 通知到达(一次性,尽力而为)。
	waitForWatchCond(t, 2*time.Second, "dropped notification frame", func() bool {
		callMu.Lock()
		defer callMu.Unlock()
		for _, typeName := range calls {
			if typeName == models.AgentEventAISessionWatchDropped {
				return true
			}
		}
		return false
	})

	// 后续 emit 不再向已摘除的 watcher 重投(dropped 通知除外,且只此一次)。
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 2)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := runFrameCalls(); n != 1 {
		t.Fatalf("failing writer got %d run frames, want exactly 1 (no retry after drop)", n)
	}
	callMu.Lock()
	defer callMu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("writer called %d times total, want 2 (1 run frame + 1 dropped notice)", len(calls))
	}
}

// ④unwatch:manager 注册表移除后,下一个 run 的 emitter 不再播种该 watcher,
// 也不再向它投递;按连接反注销一次清掉该连接的全部 watch。
func TestAgentAIManagerUnwatchStopsSeeding(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	dropped := &emitterWatchRecorder{}
	kept := &emitterWatchRecorder{}
	m.watchSession("s-1", "conn-a", dropped.write)
	m.watchSession("s-1", "conn-b", kept.write)

	m.unwatchSession("s-1", "conn-a")
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 1 {
		t.Fatalf("snapshot after unwatch = %#v, want only conn-b", snap)
	}

	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)
	if _, ok := watcherCopyForTest(emitter)["conn-a"]; ok {
		t.Fatal("unwatched conn was still seeded into the new run emitter")
	}
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	waitForWatchCond(t, 2*time.Second, "kept watcher to receive the frame", func() bool {
		return len(kept.snapshot()) == 1
	})
	if got := len(dropped.snapshot()); got != 0 {
		t.Fatalf("unwatched watcher received %d frames, want 0", got)
	}

	// 按连接反注销:conn-b 只注册了 s-1,反注销后注册表为空。
	m.unwatchSessionConn("conn-b")
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("snapshot after unwatchSessionConn = %#v, want empty", snap)
	}
}

// ⑤终态只清 emitter 拷贝:终态后 emitter 拷贝为空,但 manager 注册表仍在,
// 下一个 run 播种仍有该 watcher(跨 run 持久)。
func TestAgentAIEmitterTerminalClearsCopyKeepsRegistry(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	watcher := &emitterWatchRecorder{}
	m.watchSession("s-1", "conn-a", watcher.write)
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)

	if err := emitter.emit(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": "s-1",
		"message_id": "msg-1",
	}); err != nil {
		t.Fatal(err)
	}

	waitForWatchCond(t, 2*time.Second, "watcher to receive the terminal frame", func() bool {
		return len(watcher.snapshot()) == 1
	})
	if got := watcher.snapshot()[0]["type"]; got != models.AgentEventAIDone {
		t.Fatalf("watcher terminal frame = %v, want ai.done", got)
	}
	waitForWatchCond(t, 2*time.Second, "emitter watcher copy to be cleared on terminal", func() bool {
		return len(watcherCopyForTest(emitter)) == 0
	})
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 1 {
		t.Fatalf("manager registry lost the watcher on terminal: %#v", snap)
	}

	// 下一 run 播种仍有。
	next := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-2", messageID: "msg-2"}, owner.write)
	if _, ok := watcherCopyForTest(next)["conn-a"]; !ok {
		t.Fatal("next run emitter did not re-seed the persisted watcher")
	}
}

// ⑦onTerminal 持久化失败(rememberPendingTerminal/identity 落盘出错)时,
// emit 提前返回错误、帧不上线(属主与 watcher 都收不到);但 watcher 拷贝
// 必须照样清理——否则每个 pump 永久阻塞,钉住整个 emitter(A1 泄漏回归钉)。
func TestAgentAIEmitterTerminalJournalErrorStillClearsWatchers(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	watcher := &emitterWatchRecorder{}
	m.watchSession("s-1", "conn-a", watcher.write)
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)
	emitter.onTerminal = func(map[string]interface{}) error {
		return errors.New("disk unavailable")
	}
	// 基线在 pump 启动之后:pump 退出才可能回到基线。
	baseline := runtime.NumGoroutine()

	if err := emitter.emit(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": "s-1",
		"message_id": "msg-1",
	}); err == nil {
		t.Fatal("terminal emit error = nil, want journal failure")
	}

	if got := len(watcherCopyForTest(emitter)); got != 0 {
		t.Fatalf("watcher copy survived the failed terminal: %d", got)
	}
	if frames := watcher.snapshot(); len(frames) != 0 {
		t.Fatalf("watcher received %d frames on an unpublished terminal, want 0", len(frames))
	}
	waitForWatchCond(t, 2*time.Second, "watcher pump goroutine to exit after failed terminal", func() bool {
		return runtime.NumGoroutine() <= baseline
	})
}

// ⑥emit 期摘除同步回删 manager:缓冲打满触发摘除的那次 emit 返回后,
// manager 注册表里已经没有该 watcher(不等终态、不等下一个 run)。
func TestAgentAIEmitterBufferFullDropReachesManager(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	block := make(chan struct{})
	// 确定性握手:第一帧被 pump 取走并在写桩里卡住后,缓冲才从 0 开始可灌满
	// (否则 pump 取帧与灌满的先后顺序不确定)。
	entered := make(chan struct{})
	var once sync.Once
	stuck := agentTerminalWriter(func(value interface{}) error {
		once.Do(func() { close(entered) })
		<-block
		return nil
	})
	m.watchSession("s-1", "conn-stuck", stuck)
	emitter := m.runEmitter(agentAIRun{sessionID: "s-1", runID: "run-1", messageID: "msg-1"}, owner.write)

	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher pump never picked up the first frame")
	}

	// 恰好灌满缓冲:此时还不能摘。
	for n := 2; n <= agentAIWatcherQueueCap+1; n++ {
		if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", n)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := watcherCopyForTest(emitter)["conn-stuck"]; !ok {
		t.Fatal("watcher dropped before its queue actually overflowed")
	}
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 1 {
		t.Fatalf("manager registry lost the watcher before overflow: %#v", snap)
	}

	// 溢出帧:emit 返回时摘除必须已经落进 manager 注册表。
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", agentAIWatcherQueueCap+2)); err != nil {
		t.Fatal(err)
	}
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("manager registry kept the watcher after buffer-full drop: %#v", snap)
	}
	if _, ok := watcherCopyForTest(emitter)["conn-stuck"]; ok {
		t.Fatal("emitter copy kept the watcher after buffer-full drop")
	}
	close(block)
}

// ---------- Task 2: watch/unwatch 路由 + 审批全类帧扇出 ----------

// newWatchableTestSession 注册一个带在跑 emitter 的内存会话(不真跑 CLI),
// 供 watch/unwatch 处理器与审批帧测试复用。
func newWatchableTestSession(t *testing.T, m *agentAIManager, sessionID string) (*agentAIRunEmitter, *emitterWatchRecorder) {
	t.Helper()
	owner := &emitterWatchRecorder{}
	emitter := m.runEmitter(agentAIRun{sessionID: sessionID, runID: "run-" + sessionID, messageID: "msg-1"}, owner.write)
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.mu.Lock()
	m.sessions[sessionID] = &agentAISession{
		id:            sessionID,
		cancel:        cancel,
		activeWriter:  emitter.emit,
		activeEmitter: emitter,
		runSeq:        1,
		lastActiveAt:  time.Now(),
	}
	m.mu.Unlock()
	return emitter, owner
}

// insertPendingTestApproval 造一个挂起的审批 waiter(runSeq 须匹配会话),
// 让 sync/cancelled 路径有真实素材。
func insertPendingTestApproval(m *agentAIManager, sessionID, approvalID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.approvals == nil {
		m.approvals = make(map[string]*agentAIApprovalWaiter)
	}
	req := agentAIApprovalRequest{
		ID:        approvalID,
		SessionID: sessionID,
		Kind:      models.AgentAIApprovalKindTool,
		respond:   make(chan agentAIApprovalResponse, 1),
	}
	m.approvals[agentAIApprovalMapKey(sessionID, approvalID)] = &agentAIApprovalWaiter{
		sessionID: sessionID,
		runSeq:    1,
		request:   req,
	}
}

// watch 内存解析不到的 id → ai.error session not found(磁盘解析 Task 3 才
// 开放);缺 session_id/conn_id → ai.error;内存命中则登记 + 立即补种在跑
// emitter 拷贝(R2);unwatch 幂等且连在跑拷贝一起裁剪。
func TestAgentAIWatchHandlerMemoryResolveAndError(t *testing.T) {
	m := newAgentAIManager()
	rec := &emitterWatchRecorder{}

	// 未解析:拒绝且不留注册痕迹。
	m.watch(map[string]interface{}{"session_id": "ghost", "conn_id": "c1"}, rec.write)
	frames := rec.snapshot()
	if len(frames) != 1 || frames[0]["type"] != models.AgentEventAIError {
		t.Fatalf("watch ghost = %#v, want single ai.error", frames)
	}
	if !strings.Contains(fmt.Sprint(frames[0]["error"]), "session not found") {
		t.Fatalf("error = %v, want session not found", frames[0]["error"])
	}
	if snap := m.sessionWatchersSnapshot("ghost"); snap != nil {
		t.Fatalf("ghost watch left registry: %#v", snap)
	}

	emitter, owner := newWatchableTestSession(t, m, "s-1")
	// 缺 conn_id:拒绝。
	m.watch(map[string]interface{}{"session_id": "s-1"}, rec.write)
	if n := len(rec.snapshot()); n != 2 {
		t.Fatalf("missing conn_id watch wrote %d frames total, want 2 (1 new error)", n)
	}
	// 命中:登记 + 补种在跑 emitter。
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	if n := len(rec.snapshot()); n != 2 {
		t.Fatalf("successful watch wrote extra frames: %d", n-2)
	}
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 1 {
		t.Fatalf("registry after watch = %#v", snap)
	}
	if _, ok := watcherCopyForTest(emitter)["c1"]; !ok {
		t.Fatal("watching a running session did not attach to its live emitter (R2)")
	}
	// R6:重复 watch 不产生重复投递。
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	waitForWatchCond(t, 2*time.Second, "watcher to receive the frame", func() bool {
		return len(rec.snapshot()) == 3
	})
	if got := len(rec.snapshot()); got != 3 {
		t.Fatalf("duplicate watch produced %d frames total, want exactly 3 (no dup)", got)
	}
	if len(owner.snapshot()) != 1 {
		t.Fatalf("owner frames = %d, want 1", len(owner.snapshot()))
	}

	// unwatch:注册表与在跑拷贝同时消失,无错误帧。
	m.unwatch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	if n := len(rec.snapshot()); n != 3 {
		t.Fatalf("unwatch wrote extra frames: %d", n-3)
	}
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("registry after unwatch = %#v", snap)
	}
	if _, ok := watcherCopyForTest(emitter)["c1"]; ok {
		t.Fatal("unwatch did not prune the running emitter copy")
	}
	// 幂等:再 unwatch 不报错。
	m.unwatch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	if n := len(rec.snapshot()); n != 3 {
		t.Fatalf("idempotent unwatch wrote frames: %d", n-3)
	}
	_ = emitter
}

// 审批四类帧全到达 watcher:pending/resolved 走在跑 emitter(Task 1 emit 扇
// 出自动覆盖,本测试钉住该契约),sync 与 close 的 cancelled 走显式旁路扇出。
func TestAgentAIWatchApprovalFourFrameClasses(t *testing.T) {
	m := newAgentAIManager()
	emitter, owner := newWatchableTestSession(t, m, "s-1")
	rec := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	insertPendingTestApproval(m, "s-1", "ap-1")

	// pending:审批请求帧经 emitter 属主写路径发出。
	if err := emitter.emit(map[string]interface{}{
		"type":        models.AgentEventAIApprovalRequest,
		"session_id":  "s-1",
		"message_id":  "msg-1",
		"approval_id": "ap-1",
		"kind":        models.AgentAIApprovalKindTool,
	}); err != nil {
		t.Fatal(err)
	}
	// resolved:属主响应后 run 侧回写的 resolved 帧,同样经 emitter。
	if err := emitter.emit(map[string]interface{}{
		"type":        models.AgentEventAIApprovalRequest,
		"session_id":  "s-1",
		"message_id":  "msg-1",
		"approval_id": "ap-1",
		"status":      "resolved",
		"decision":    models.AgentAIApprovalDecisionAccept,
	}); err != nil {
		t.Fatal(err)
	}
	waitForWatchCond(t, 2*time.Second, "watcher to receive pending+resolved via emitter", func() bool {
		return len(rec.snapshot()) == 2
	})

	// sync:socket 级对账帧,显式旁路扇出到 watcher。
	syncRec := &emitterWatchRecorder{}
	m.emitApprovalSync(syncRec.write)
	if n := len(syncRec.snapshot()); n != 1 {
		t.Fatalf("owner sync frames = %d, want 1", n)
	}
	waitForWatchCond(t, 2*time.Second, "watcher to receive ai.approval.sync", func() bool {
		for _, frame := range rec.snapshot() {
			if frame["type"] == models.AgentEventAIApprovalSync {
				return true
			}
		}
		return false
	})

	// cancelled + 终态:close 用裸 writer 写 cancelled(显式扇出),终态经
	// activeWriter=emitter(自动扇出)。
	closer := &emitterWatchRecorder{}
	m.close(map[string]interface{}{"session_id": "s-1"}, closer.write)
	waitForWatchCond(t, 2*time.Second, "watcher to receive ai.approval.cancelled and ai.session.closed", func() bool {
		var cancelled, closed bool
		for _, frame := range rec.snapshot() {
			switch frame["type"] {
			case models.AgentEventAIApprovalCancelled:
				cancelled = true
			case models.AgentEventAISessionClosed:
				closed = true
			}
		}
		return cancelled && closed
	})
	var cancelledIdx, closedIdx = -1, -1
	for i, frame := range rec.snapshot() {
		switch frame["type"] {
		case models.AgentEventAIApprovalCancelled:
			cancelledIdx = i
			ids, _ := frame["approval_ids"].([]string)
			if len(ids) != 1 || ids[0] != "ap-1" {
				t.Fatalf("cancelled approval_ids = %#v, want [ap-1]", frame["approval_ids"])
			}
		case models.AgentEventAISessionClosed:
			closedIdx = i
		}
	}
	if cancelledIdx == -1 || closedIdx == -1 {
		t.Fatalf("cancelled idx=%d closed idx=%d, both required", cancelledIdx, closedIdx)
	}
	// 同一 sink 队列入队即保序:watcher 先见 cancelled 再见终态。
	if cancelledIdx > closedIdx {
		t.Fatalf("watcher saw cancelled (%d) after session closed (%d)", cancelledIdx, closedIdx)
	}
	if len(owner.snapshot()) == 0 {
		t.Fatal("owner received nothing from the approval/close flow")
	}
}

// unwatch 即时停帧:unwatch 后在跑 emitter 的下一帧不得再到达(unwatch 处理
// 器补裁剪在跑拷贝——「unwatch 即时停帧」严格成立)。
func TestAgentAIUnwatchPrunesRunningEmitterCopy(t *testing.T) {
	m := newAgentAIManager()
	emitter, _ := newWatchableTestSession(t, m, "s-1")
	rec := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	waitForWatchCond(t, 2*time.Second, "first frame", func() bool { return len(rec.snapshot()) == 1 })

	m.unwatch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 2)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("watcher received %d frames after unwatch, want 1 (immediate stop)", got)
	}
}

// R1 弱网语义:缓冲打满双摘后,该连接收到一次性 ai.session.watch.dropped
// 通知帧(尽力而为,收到即知该重新 watch)。
func TestAgentAIWatchDroppedNotificationOnQueueFull(t *testing.T) {
	m := newAgentAIManager()
	emitter, _ := newWatchableTestSession(t, m, "s-1")
	gate := make(chan struct{})
	rec := &emitterWatchRecorder{}
	slow := agentTerminalWriter(func(value interface{}) error {
		if value.(map[string]interface{})["type"] == models.AgentEventAISessionWatchDropped {
			rec.write(value) // 通知帧直达,不参与限流
			return nil
		}
		<-gate
		rec.write(value)
		return nil
	})
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, slow)

	// 第 1 帧进 pump 并卡在写桩;灌满 64 帧缓冲,第 66 帧触发满即摘。
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 1)); err != nil {
		t.Fatal(err)
	}
	waitForWatchCond(t, 2*time.Second, "pump to pick up the first frame", func() bool {
		emitter.watchMu.Lock()
		defer emitter.watchMu.Unlock()
		sink, ok := emitter.watchers["c1"]
		return ok && len(sink.ch) == 0
	})
	for n := 2; n <= agentAIWatcherQueueCap+2; n++ {
		if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", n)); err != nil {
			t.Fatal(err)
		}
	}
	waitForWatchCond(t, 2*time.Second, "dropped notification frame", func() bool {
		for _, frame := range rec.snapshot() {
			if frame["type"] == models.AgentEventAISessionWatchDropped {
				return true
			}
		}
		return false
	})
	for _, frame := range rec.snapshot() {
		if frame["type"] == models.AgentEventAISessionWatchDropped {
			if frame["session_id"] != "s-1" {
				t.Fatalf("dropped frame session_id = %v, want s-1", frame["session_id"])
			}
			if !strings.Contains(fmt.Sprint(frame["reason"]), "queue full") {
				t.Fatalf("dropped frame reason = %v, want queue full", frame["reason"])
			}
		}
	}
	close(gate)
}

// R5:注册表超 cap 走 LRU 摘除,被摘连接收到 dropped 通知(否则它无从知道
// 自己已失联,页面会静默黑屏到下一次重连)。
func TestAgentAIWatcherRegistryCapLRUEvictsOldest(t *testing.T) {
	m := newAgentAIManager()
	newWatchableTestSession(t, m, "s-1")
	evicted := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "conn-0"}, evicted.write)
	fill := &emitterWatchRecorder{}
	for i := 1; i <= agentAIWatcherRegistryCap-1; i++ {
		m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": fmt.Sprintf("conn-%d", i)}, fill.write)
	}
	snap := m.sessionWatchersSnapshot("s-1")
	if len(snap) != agentAIWatcherRegistryCap {
		t.Fatalf("registry size = %d, want cap %d", len(snap), agentAIWatcherRegistryCap)
	}
	if _, ok := snap["conn-0"]; !ok {
		t.Fatal("conn-0 evicted before cap was exceeded")
	}

	// 超 cap:最老的 conn-0 被摘,收到 dropped 通知;新 conn 留下。
	fresh := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "conn-new"}, fresh.write)
	snap = m.sessionWatchersSnapshot("s-1")
	if _, ok := snap["conn-0"]; ok {
		t.Fatal("oldest watcher survived over-cap registration")
	}
	if _, ok := snap["conn-new"]; !ok {
		t.Fatal("newest watcher missing after LRU eviction")
	}
	waitForWatchCond(t, 2*time.Second, "evicted conn to receive the dropped notification", func() bool {
		return len(evicted.snapshot()) > 0
	})
	if frame := evicted.snapshot()[0]; frame["type"] != models.AgentEventAISessionWatchDropped {
		t.Fatalf("evicted frame type = %v, want %s", frame["type"], models.AgentEventAISessionWatchDropped)
	}
}

// ---------- Y1 + J2: TUI 清理补齐 activeEmitter;fallback 与断线反注销钉 ----------

// Y1 回归钉:TUI 注入回合收尾清理(cancel/activeWriter/activity/tuiTurn/
// activeRunID)必须连同 activeEmitter 一起清——否则回合结束后、下一 run 前的
// 无界窗口里,任何 ai.session.watch 都会 attachWatch 到已终态、拷贝已清空的
// 旧 emitter:removed 永不关,常驻 pump 永久 park(每次页面刷新泄一条
// goroutine + 64 槽)。
func TestAgentAITUITurnClearReleasesActiveEmitter(t *testing.T) {
	m := newAgentAIManager()
	emitter, _ := newWatchableTestSession(t, m, "s-1")

	m.mu.Lock()
	session := m.sessions["s-1"]
	m.mu.Unlock()
	m.clearTUITurnSessionState(session)

	m.mu.Lock()
	released := session.activeEmitter == nil && session.activeWriter == nil
	m.mu.Unlock()
	if !released {
		t.Fatal("clearTUITurnSessionState left activeEmitter/activeWriter set")
	}

	// 行为钉:清理后 watch 仍登记(为下一 run 播种),但不得挂上旧 emitter 拷贝。
	rec := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c-late"}, rec.write)
	if _, ok := watcherCopyForTest(emitter)["c-late"]; ok {
		t.Fatal("watch after TUI cleanup attached to the stale emitter copy (pump would park forever)")
	}
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 1 {
		t.Fatalf("registry after late watch = %#v, want the watcher kept for the next run", snap)
	}
}

// J2:fanOutSessionWatchFrame 的 registry-direct fallback(run 间隙:会话在、
// 无在跑 emitter)把旁路帧送达 watcher;nil payload 是 no-op。
func TestAgentAIFanOutSessionFrameFallbackWithoutEmitter(t *testing.T) {
	m := newAgentAIManager()
	rec := &emitterWatchRecorder{}
	// run 间隙形态:会话在注册表,但没有 activeEmitter/activeWriter。
	m.mu.Lock()
	m.sessions["s-gap"] = &agentAISession{id: "s-gap", runSeq: 1, lastActiveAt: time.Now()}
	m.mu.Unlock()
	m.watch(map[string]interface{}{"session_id": "s-gap", "conn_id": "c1"}, rec.write)

	m.fanOutSessionWatchFrame("s-gap", nil) // no-op,不 panic
	m.fanOutSessionWatchFrame("s-gap", map[string]interface{}{
		"type":       models.AgentEventAIApprovalCancelled,
		"session_id": "s-gap",
	})
	waitForWatchCond(t, 2*time.Second, "fallback fanned frame to reach the watcher", func() bool {
		return len(rec.snapshot()) == 1
	})
	if frame := rec.snapshot()[0]; frame["type"] != models.AgentEventAIApprovalCancelled {
		t.Fatalf("fanned frame type = %v", frame["type"])
	}

	// 无 watcher 的会话:fallback 静默(空注册表快照,零 goroutine)。
	m.fanOutSessionWatchFrame("s-unknown", map[string]interface{}{"type": "x"})
}

// J2:断线反注销两层——manager.unwatchSessionConn 一次清掉该连接对全部会话的
// 注册与在跑 emitter 拷贝;service.UnwatchLocalAIConn 透传且 nil ai 安全。
func TestAgentAIUnwatchSessionConnDisconnectCleanup(t *testing.T) {
	m := newAgentAIManager()
	emitter, _ := newWatchableTestSession(t, m, "s-1")
	rec := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c1"}, rec.write)
	// 同一连接的第二个会话注册(无 emitter 的 run 间隙会话)。
	m.mu.Lock()
	m.sessions["s-2"] = &agentAISession{id: "s-2", runSeq: 1, lastActiveAt: time.Now()}
	m.mu.Unlock()
	m.watch(map[string]interface{}{"session_id": "s-2", "conn_id": "c1"}, rec.write)
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 1 {
		t.Fatalf("s-1 registry before disconnect = %#v", snap)
	}

	// manager 层:断线一次清全部会话 + 在跑 emitter 拷贝。
	m.unwatchSessionConn("c1")
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("s-1 registry after disconnect = %#v", snap)
	}
	if snap := m.sessionWatchersSnapshot("s-2"); len(snap) != 0 {
		t.Fatalf("s-2 registry after disconnect = %#v", snap)
	}
	if _, ok := watcherCopyForTest(emitter)["c1"]; ok {
		t.Fatal("disconnect did not prune the running emitter copy")
	}

	// service 层:透传生效;nil ai 静默不 panic。
	svcWatch := &emitterWatchRecorder{}
	m.watch(map[string]interface{}{"session_id": "s-1", "conn_id": "c2"}, svcWatch.write)
	svc := &AgentService{ai: m}
	svc.UnwatchLocalAIConn("c2")
	if snap := m.sessionWatchersSnapshot("s-1"); len(snap) != 0 {
		t.Fatalf("s-1 registry after service unwatch = %#v", snap)
	}
	(&AgentService{}).UnwatchLocalAIConn("c2") // nil ai:不得 panic
}
