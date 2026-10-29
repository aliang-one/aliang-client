package services

import (
	"errors"
	"fmt"
	"runtime"
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
// 后续帧不再向它重投。
func TestAgentAIEmitterWriteErrorWatcherDropped(t *testing.T) {
	m := newAgentAIManager()
	owner := &emitterWatchRecorder{}
	var calls int
	var callMu sync.Mutex
	failing := agentTerminalWriter(func(value interface{}) error {
		callMu.Lock()
		calls++
		callMu.Unlock()
		return fmt.Errorf("conn gone")
	})
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

	// 后续 emit 不再向已摘除的 watcher 重投。
	if err := emitter.emit(emitterWatchPayload(models.AgentEventAIDelta, "s-1", 2)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	callMu.Lock()
	defer callMu.Unlock()
	if calls != 1 {
		t.Fatalf("failing writer called %d times, want exactly 1 (no retry after drop)", calls)
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
