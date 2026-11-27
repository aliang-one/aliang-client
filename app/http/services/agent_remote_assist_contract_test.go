package services

// 远程协助 P1 Task 8:契约测试——模拟云端对端的全设备侧链。
//
// 契约主体(plan Task 8 脚本,全链一次跑通):
//
//	enabled=true → HTTP 申请 → 断言上行 assist.request
//	→ handleRemoteAgentMessage(assist.approved) → 断言终端创建(fake startProcess)
//	→ handleRemoteAgentMessage(terminal.input, session_id=assist-*) → fake PTY 收到字节
//	→ fake PTY 出帧 → 上行 writer 与 view sink 双双收到
//	→ handleRemoteAgentMessage(assist.end{admin_end}) → PTY 关、view 收 end、池释放
//	→ 60s 冷却内再申请 → 429(ErrAssistCooldown 哨兵)
//
// HTTP 腿的分层说明(刻意,非偷懒):services 内部测试(package services)
// import handlers 会被 Go 编译器拒绝——handlers(非测试代码)import services,
// 构成 "services → handlers → services" import cycle not allowed in test(本
// 分支实测);外部测试包(package services_test)虽可 import handlers,但拿不到
// 未导出的帧分派器 handleRemoteAgentMessage 与 assist 夹具,契约主体(云端帧→
// 设备)反而测不了。故本文件在两层驱动:
//   - 申请腿经 service 壳 AssistRequest——即 HandleAssistRequest 过完门矩阵
//     (cookie/loopback 双闸、跨站 Origin 闸、runtime 总开关 403 闸)之后调用的
//     **同一个函数**,一帧不差;
//   - 冷却 429 的 HTTP 映射(ErrAssistCooldown → 429/CodeTooManyRequests)由
//     handlers 包 agent_handler_assist_test.go 钉死(端到端用例断言
//     http.StatusTooManyRequests + 哨兵映射表);本文件钉 service 层哨兵
//     errors.Is(err, ErrAssistCooldown),两层合起来即完整 429 契约。
//
// 复用纪律:本文件只 compose 既有夹具(fakeAssistSpawner/assistViewProbe/
// assistHarness 及其 quiesce/read-only 读口/payloadCollector/waitUntil),
// 不新造假件——夹具语义已在 Task 2/5/7 各自用例里钉住。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// newAssistContractHarness 在 Task 5 夹具(newTestAssistManagerWithFakeTerminal)
// 上补两件契约测试必需的隔离,其余全盘复用:
//  1. ALIANG_CACHE_DIR→TempDir + cache 重置:handleRemoteAgentMessage 每帧分派
//     都会 setRemoteConnectionState → saveStateLocked → agentStatePath 落盘
//     agent state,不隔离会写进开发者真实缓存目录(Task 5 夹具不驱动分派器,
//     故从未踩到这条路径);
//  2. HOME→TempDir:newAgentAIManager 构造经 agentCoreJournalDir 读 $HOME/.aliang
//     (本目录只记路径不开文件,隔离属纵深防御)。
// ai 管理器必须非 nil:disabled-gate 腿的 forceDisconnectRemote 会调
// ai.closeAllWithReason(nil receiver 的 m.mu.Lock() 是裸 deref)。
func newAssistContractHarness(t *testing.T) *assistHarness {
	t.Helper()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	// reset 与 cleanup 再 reset 成对(照 agent_peer_contract_test.go 同款):reset
	// 只把包单例重新指到本测试 TempDir,t.Setenv 收尾还原的是 env 不是单例——
	// 单例若留着指向已删 TempDir,后续 GetCacheSubdir 消费者会静默 MkdirAll
	// 复活游离目录(cachedir.go MkdirAll 分支)。
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	t.Setenv("HOME", t.TempDir())

	h := newTestAssistManagerWithFakeTerminal(t)
	h.svc.ai = newAgentAIManager()
	return h
}

// contractWaitUplinkTypeFor 等上行缝(sink=云端对端)出现指定类型+会话 id 的帧
// (create/exit 走异步腿,断言必须轮询而非裸读)。
func contractWaitUplinkTypeFor(t *testing.T, h *assistHarness, frameType string, sid string) {
	t.Helper()
	if !waitUntil(5*time.Second, func() bool {
		for _, f := range h.upOfTypes(frameType) {
			if f["session_id"] == sid {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("%s frame must reach the cloud peer via the uplink sink (up=%v)",
			frameType, h.upOfTypes(frameType))
	}
}

// TestAssistContractCloudPeerFullChain 主脚本:云端对端视角把一条申请从 HTTP
// 入口走到冷却,每一拍都对上双方都看得见的证据(上行帧/下行帧/PTY 物理状态)。
func TestAssistContractCloudPeerFullChain(t *testing.T) {
	h := newAssistContractHarness(t)
	s := h.svc

	// —— 前提:enabled=true(设备侧总开关;Enabled/Registered/RemoteAssistEnabled
	// 已由夹具置位)——
	if !s.RemoteAssistEnabled() {
		t.Fatal("harness must start with the assist master switch on")
	}

	// —— 步骤1:HTTP 申请(夹具经 service 壳直调,分层说明见文件头)——
	message := "请帮我看下配置"
	sid, err := s.AssistRequest(message)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !strings.HasPrefix(sid, assistIDPrefix) {
		t.Fatalf("session id must be prefixed %s: %q", assistIDPrefix, sid)
	}

	// —— 步骤2:断言上行 assist.request(admin 队列收到的是带留言的申请帧)——
	reqs := h.upOfTypes(models.AgentEventAssistRequestUp)
	if len(reqs) != 1 {
		t.Fatalf("uplink must carry exactly one assist.request, got %d (%v)", len(reqs), reqs)
	}
	if reqs[0]["session_id"] != sid || fmt.Sprint(reqs[0]["message"]) != message {
		t.Fatalf("assist.request frame = %v, want session=%s message=%q", reqs[0], sid, message)
	}

	// 云端下行连接的写口(回帧/错误帧落这里,等价 remote-WS 的 writeJSON)。
	cloud, cloudWrite := newPayloadCollector()

	// —— 步骤3:云端 approved → 设备自授权建终端(fake startProcess 实证)——
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistApproved,
		"session_id": sid,
	}, cloudWrite)
	if !waitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatalf("approval must create the assist terminal, ws frames=%v", cloud.snapshot())
	}
	if got := h.spawner.spawnedCount(); got != 1 {
		t.Fatalf("one approval must spawn exactly one PTY, spawned=%d", got)
	}
	// 终端 created 帧上行:admin(=sink 对端)知道终端已就绪。
	contractWaitUplinkTypeFor(t, h, models.AgentEventTerminalCreated, sid)

	// —— 步骤4:云端 terminal.input(session_id=assist-*)→ fake PTY 收到字节
	// (Task 5 前缀门从真正的云帧分派器进入,不是直调门函数)——
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventTerminalInput,
		"session_id": sid,
		"data":       "echo contract\r",
	}, cloudWrite)
	if !waitUntil(5*time.Second, func() bool {
		h.spawner.mu.Lock()
		defer h.spawner.mu.Unlock()
		buf := h.spawner.inputBufs[sid]
		return buf != nil && strings.Contains(buf.String(), "echo contract")
	}) {
		t.Fatal("cloud input must reach the assist PTY stdin")
	}

	// —— 步骤5:fake PTY 出帧 → 上行 writer 与 view sink 双双收到
	// (复合 writer 两路扇出的核心契约;view 走导出壳=生产路径)——
	probe := &assistViewProbe{}
	s.AssistRegisterViewSink(sid, "view-contract", probe.fan)
	h.fakeTerminalEmit(sid, "contract-output")
	if !waitUntil(5*time.Second, func() bool {
		return h.upGot("contract-output") && probe.Got("contract-output")
	}) {
		t.Fatalf("PTY output must fan out to both the uplink sink and the view sink (up=%v view=%d)",
			h.upOfTypes(models.AgentEventTerminalOutput), len(probe.frames))
	}

	// —— 步骤6:assist.end{admin_end} → PTY 关、view 收 end、池释放 ——
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistEnd,
		"session_id": sid,
		"reason":     string(AssistEndAdmin),
	}, cloudWrite)
	if !waitUntil(5*time.Second, func() bool { return !h.fakeTerminalAlive(sid) }) {
		t.Fatal("cloud end must close the assist PTY")
	}
	endFrame, ok := probe.endFrame()
	if !ok {
		t.Fatalf("view sink must receive assist.end, got %v", probe.frames)
	}
	if endFrame["reason"] != string(AssistEndAdmin) || endFrame["session_id"] != sid {
		t.Fatalf("assist.end to viewer = %v, want reason=%s session=%s", endFrame, AssistEndAdmin, sid)
	}
	if snap := s.assist.Snapshot(); snap.State != assistStateEnded || snap.EndReason != AssistEndAdmin {
		t.Fatalf("after cloud end snapshot = %+v, want ended/%s", snap, AssistEndAdmin)
	}
	// 池释放:假 PTY 的 kill 不归返 waiter(夹具注记),显式放行模拟进程死亡,
	// waitTerminal 才把会话移出 live 表、exit 帧上行——坑位真正归还。
	h.spawner.releaseExit()
	contractWaitUplinkTypeFor(t, h, models.AgentEventTerminalExit, sid)
	if s.terminal.get(sid) != nil {
		t.Fatal("ended session must leave the terminal live table (pool slot released)")
	}
	if !s.terminal.hasHeadroom() {
		t.Fatal("pool must have headroom after the assist session released its slot")
	}

	// —— 步骤6b(扩展:回放续屏):结束后的第二观看端收到 tombstone 回放
	// (Task 7 的 replay 契约在全链语境下的复检:status=exited + final 收口)——
	viewer2, viewer2Write := newPayloadCollector()
	s.AssistTerminalReplayTo(sid, viewer2Write)
	replay := viewer2.snapshot()
	if len(replay) == 0 {
		t.Fatal("post-end viewer must receive tombstone replay frames")
	}
	last := replay[len(replay)-1]
	if last["type"] != models.AgentEventTerminalReplay || last["status"] != terminalReplayStatusExited {
		t.Fatalf("tombstone replay last frame = %v, want terminal.replay/status=%s",
			last, terminalReplayStatusExited)
	}
	if final, _ := last["final"].(bool); !final {
		t.Fatalf("tombstone replay last frame must be final: %v", last)
	}
	if data, _ := last["data"].(string); !strings.Contains(data, "contract-output") {
		t.Fatalf("tombstone replay must carry the session scrollback, got %q", data)
	}

	// —— 步骤7:60s 冷却内再申请 → 429。时钟未推进(仍在冷却窗内),service 层
	// 哨兵 ErrAssistCooldown 由 HTTP 面(handler)映射 429/CodeTooManyRequests
	// ——该映射已在 handlers 包测试钉死,两层合起来是完整 429 契约——
	_, err = s.AssistRequest("again")
	if !errors.Is(err, ErrAssistCooldown) {
		t.Fatalf("re-request within the 60s cooldown must fail with ErrAssistCooldown, got %v", err)
	}
	// 冷却拒绝不得再上行 assist.request(对端队列零噪声)。
	if reqs := h.upOfTypes(models.AgentEventAssistRequestUp); len(reqs) != 1 {
		t.Fatalf("cooldown-rejected request must not uplink another assist.request, got %d", len(reqs))
	}

	// 全链干净信号:云端下行连接零错误帧(approved/input/end 全部静默受理)。
	if frames := cloud.snapshot(); len(frames) != 0 {
		t.Fatalf("happy chain must not write any frame back to the cloud connection, got %v", frames)
	}
}

// TestAssistContractDisabledDeviceRejectsCloudFrames 扩展①:enabled 门。设备被
// 禁用后 assist 信令帧一律标准错误帧拒绝(TestAgentServiceRejectsRemoteCommands
// AfterDisable 的 assist 族同款语义),状态机零污染、PTY 零创建。
func TestAssistContractDisabledDeviceRejectsCloudFrames(t *testing.T) {
	h := newAssistContractHarness(t)
	s := h.svc

	// 只翻 Enabled 维度(Registered 仍真):测的是「设备禁用」这一拍。
	s.mu.Lock()
	s.state.Enabled = false
	s.mu.Unlock()

	cloud, cloudWrite := newPayloadCollector()

	// assist.approved:enabled 门先于 assist 分派臂。
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistApproved,
		"session_id": "assist-disabled-probe",
	}, cloudWrite)
	frames := cloud.snapshot()
	if len(frames) != 1 || frames[0]["type"] != models.AgentEventError ||
		!strings.Contains(fmt.Sprint(frames[0]["error"]), "disabled") {
		t.Fatalf("disabled device must answer assist.approved with one standard error frame, got %v", frames)
	}

	// assist- 前缀终端帧同门(terminal.input 也在 requiresEnabledDevice 名单,
	// 门在分派器最顶上,不是 assist 臂内部)。
	cloud2, cloud2Write := newPayloadCollector()
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventTerminalInput,
		"session_id": "assist-disabled-probe",
		"data":       "x",
	}, cloud2Write)
	frames2 := cloud2.snapshot()
	if len(frames2) != 1 || frames2[0]["type"] != models.AgentEventError ||
		!strings.Contains(fmt.Sprint(frames2[0]["error"]), "disabled") {
		t.Fatalf("disabled device must answer assist-prefixed input with one standard error frame, got %v", frames2)
	}

	// 状态机与终端池零污染:无会话、无 PTY。
	if snap := s.assist.Snapshot(); snap.State != assistStateIdle {
		t.Fatalf("disabled device must not enter any assist state, got %+v", snap)
	}
	if got := h.spawner.spawnedCount(); got != 0 {
		t.Fatalf("disabled device must not spawn a PTY, spawned=%d", got)
	}
}

// TestAssistContractSessionIDMismatchRejected 扩展②:session_id 串会话链。
// 错 id 的 approved → 守卫拒(id mismatch);真 id approved 落地后,打到别的
// assist- id 的 terminal.input → 前缀门 active+id 匹配拒(not active),
// 真身 PTY 零污染。
func TestAssistContractSessionIDMismatchRejected(t *testing.T) {
	h := newAssistContractHarness(t)
	s := h.svc
	cloud, cloudWrite := newPayloadCollector()

	sid, err := s.AssistRequest("mismatch chain")
	if err != nil {
		t.Fatal(err)
	}

	// 错 id 的 approved:CloudApproved 守卫 errAssistIDMismatch → 分派臂转
	// agent.error 上抛,状态机停在 requested。
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistApproved,
		"session_id": "assist-bogus-id",
	}, cloudWrite)
	if frames := cloud.snapshot(); len(frames) != 1 || frames[0]["type"] != models.AgentEventError ||
		!strings.Contains(fmt.Sprint(frames[0]["error"]), "session id mismatch") {
		t.Fatalf("bogus-id approved must be rejected with the id-mismatch error, got %v", cloud.snapshot())
	}

	// 真 id approved → 终端落地(active)。
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistApproved,
		"session_id": sid,
	}, cloudWrite)
	if !waitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("approved session must create its terminal")
	}

	// 串会话的 terminal.input(别的 assist- id):assist 前缀归协助腿门,
	// isActiveSession 不匹配 → not active 拒帧。
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventTerminalInput,
		"session_id": "assist-rogue-id",
		"data":       "rogue\r",
	}, cloudWrite)
	errs := cloud.ofTypes(models.AgentEventTerminalError)
	if len(errs) != 1 || !strings.Contains(fmt.Sprint(errs[0]["error"]), "not active") {
		t.Fatalf("rogue-id input must be rejected with the not-active error, got %v", errs)
	}
	if errs[0]["session_id"] != "assist-rogue-id" {
		t.Fatalf("rejection must echo the rogue session id, got %v", errs[0])
	}

	// 真身 PTY 零污染:stdin 里没有 rogue 字节(持 spawner.mu 读,Task 7 竞态纪律)。
	h.spawner.mu.Lock()
	stdin := ""
	if buf := h.spawner.inputBufs[sid]; buf != nil {
		stdin = buf.String()
	}
	h.spawner.mu.Unlock()
	if strings.Contains(stdin, "rogue") {
		t.Fatalf("rogue input must never reach the real PTY stdin, got %q", stdin)
	}

	// 全程 agent.error 恰一条(错 id approved);真 approved 静默受理。
	if got := len(cloud.ofTypes(models.AgentEventError)); got != 1 {
		t.Fatalf("exactly one agent.error expected (bogus approved), got %d", got)
	}
}

// TestAssistContractDuplicateCloudEndSwallowed 扩展③:云端重发 assist.end。
// cloudEndOnce 幂等收敛——同因/换因重发均吞掉:无错误帧风暴、无二次 kill、
// 粘性终态原样。
func TestAssistContractDuplicateCloudEndSwallowed(t *testing.T) {
	h := newAssistContractHarness(t)
	s := h.svc
	cloud, cloudWrite := newPayloadCollector()

	sid, err := s.AssistRequest("")
	if err != nil {
		t.Fatal(err)
	}
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistApproved,
		"session_id": sid,
	}, cloudWrite)
	if !waitUntil(5*time.Second, func() bool { return h.fakeTerminalCWD(sid) != "" }) {
		t.Fatal("approved session must create its terminal")
	}

	// 首发云端 end(admin_end)→ PTY 关,恰一次 kill。
	s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":       models.AgentEventAssistEnd,
		"session_id": sid,
		"reason":     string(AssistEndAdmin),
	}, cloudWrite)
	if !waitUntil(5*time.Second, func() bool { return !h.fakeTerminalAlive(sid) }) {
		t.Fatal("cloud end must close the assist PTY")
	}
	h.spawner.mu.Lock()
	kills := h.spawner.killed
	h.spawner.mu.Unlock()
	if kills != 1 {
		t.Fatalf("first cloud end must kill the PTY exactly once, killed=%d", kills)
	}

	// 重发:同因 + 换因各一次,均为 dup 吞。
	for _, reason := range []string{string(AssistEndAdmin), string(AssistEndDurationCap)} {
		s.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
			"type":       models.AgentEventAssistEnd,
			"session_id": sid,
			"reason":     reason,
		}, cloudWrite)
	}

	// 无错误帧风暴:云端下行连接整场零帧(dup 分支静默)。
	if frames := cloud.snapshot(); len(frames) != 0 {
		t.Fatalf("duplicate cloud end must be swallowed silently, got %v", frames)
	}
	// 无二次 kill(PTY 不会因重发帧再被关一次)。
	h.spawner.mu.Lock()
	killsAfter := h.spawner.killed
	h.spawner.mu.Unlock()
	if killsAfter != kills {
		t.Fatalf("duplicate end must not re-kill the PTY, killed=%d", killsAfter)
	}
	// 粘性终态原样:不被重发帧的 reason 覆写。
	if snap := s.assist.Snapshot(); snap.State != assistStateEnded || snap.EndReason != AssistEndAdmin {
		t.Fatalf("sticky end state must survive duplicate ends, got %+v", snap)
	}
}
