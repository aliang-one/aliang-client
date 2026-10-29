package services

import (
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// 这一组锁死「压缩落库不得抹掉立身上下文」的契约：
//
//	历史首部的 system 条目（initial_context：fork 快照 / goal 上下文）只由
//	ai.session.create / ai.run.start 写入一次；压缩产物只含 user/assistant
//	（内核 messages 不收 system），原样落库会把它永久抹掉，而摘要由内核
//	messages 生成（不含 system），摘要补不回来。两层防御各有一条测试：
//	  1. replaceAgentCoreHistory 落库前把旧历史的非 user/assistant 条目拼回
//	     头部（守卫层）；
//	  2. agentCoreSystemContextFromHistory 历史缺 system 条目时回落
//	     session.initialContext（兜底层）。
//
// 变异守卫：摘掉 replaceAgentCoreHistory 的保留逻辑，第一条即红。

func newAgentCoreCompactTestSession() *agentAISession {
	return &agentAISession{
		id:             "s-compact",
		mode:           "vibe",
		projectPath:    "/proj",
		provider:       "agentcore",
		model:          "glm-5.3",
		initialContext: "目标：修复 compact 链路。约束：只改 agentcore。",
		runSeq:         3,
		history: []agentAIMessage{
			{Role: "system", MessageID: "initial_context", Content: "目标：修复 compact 链路。约束：只改 agentcore。", CreatedAt: time.Now().UTC()},
			{Role: "user", Content: "第一轮问题", CreatedAt: time.Now().UTC()},
			{Role: "assistant", Content: "第一轮回答", CreatedAt: time.Now().UTC()},
			{Role: "user", Content: "第二轮问题", CreatedAt: time.Now().UTC()},
		},
		activity: newAgentAIActivity(),
	}
}

// TestReplaceAgentCoreHistoryPreservesSystemEntries 锁守卫层：自动压缩落库后
// system 条目必须仍在历史首部，user/assistant 的压缩形状不受影响。
func TestReplaceAgentCoreHistoryPreservesSystemEntries(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newAgentCoreCompactTestSession()
	mgr.mu.Lock()
	mgr.sessions[sess.id] = sess
	mgr.mu.Unlock()

	// OnCompact 钩子落库用的就是这一入口（replaceAgentCoreHistory）。
	mgr.replaceAgentCoreHistory(sess.id, sess.runSeq, []agentcore.Message{
		agentcore.TextMessage("user", "以下是本会话此前内容的压缩摘要：\n\n- 用户在修 compact 链路"),
		agentcore.TextMessage("user", "第二轮问题"),
	})

	mgr.mu.Lock()
	got := append([]agentAIMessage(nil), sess.history...)
	mgr.mu.Unlock()

	if len(got) == 0 {
		t.Fatal("压缩后历史为空")
	}
	head := got[0]
	if head.Role != "system" || head.MessageID != "initial_context" {
		t.Fatalf("历史首部 = %q/%q, want system/initial_context（立身上下文被压缩落库抹掉）", head.Role, head.MessageID)
	}
	if head.Content != "目标：修复 compact 链路。约束：只改 agentcore。" {
		t.Fatalf("initial_context 内容被改写: %q", head.Content)
	}
	// 压缩产物原样跟在 system 条目之后。
	if len(got) != 3 {
		t.Fatalf("压缩后历史 %d 条, want 3（system + 摘要 + 尾部）", len(got))
	}
	if got[1].Role != "user" || !strings.Contains(got[1].Content, "压缩摘要") {
		t.Fatalf("第二条应是压缩摘要, got %q/%q", got[1].Role, got[1].Content)
	}
	if got[2].Content != "第二轮问题" {
		t.Fatalf("尾部原文丢失: %q", got[2].Content)
	}
}

// TestReplaceAgentCoreHistoryRunSeqGuardUnchanged 锁 runSeq 守卫语义不被本次
// 改动顺带破坏：runSeq 已前进时不得覆盖历史。
func TestReplaceAgentCoreHistoryRunSeqGuardUnchanged(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newAgentCoreCompactTestSession()
	mgr.mu.Lock()
	mgr.sessions[sess.id] = sess
	mgr.mu.Unlock()

	before := len(sess.history)
	mgr.replaceAgentCoreHistory(sess.id, sess.runSeq+1, []agentcore.Message{
		agentcore.TextMessage("user", "压缩摘要"),
	})
	mgr.mu.Lock()
	same := len(sess.history) == before && sess.history[0].MessageID == "initial_context"
	mgr.mu.Unlock()
	if !same {
		t.Fatal("runSeq 不匹配时不得覆盖历史")
	}
}

// TestAgentCoreSystemContextFromHistoryFallsBackToInitialContext 锁兜底层：
// 历史有 system 条目时以历史为准；历史缺失（被旧版本落库冲掉/尾部裁剪）时
// 回落 session.initialContext；两者都无时为空。
func TestAgentCoreSystemContextFromHistoryFallsBackToInitialContext(t *testing.T) {
	withSystem := []agentAIMessage{
		{Role: "system", MessageID: "initial_context", Content: "历史里的上下文"},
		{Role: "user", Content: "hi"},
	}
	if got := agentCoreSystemContextFromHistory(withSystem, "字段里的上下文"); got != "历史里的上下文" {
		t.Fatalf("历史有 system 条目时应以历史为准, got %q", got)
	}

	withoutSystem := []agentAIMessage{{Role: "user", Content: "hi"}}
	if got := agentCoreSystemContextFromHistory(withoutSystem, "字段里的上下文"); got != "字段里的上下文" {
		t.Fatalf("历史缺 system 条目时应回落 initialContext, got %q", got)
	}
	if got := agentCoreSystemContextFromHistory(withoutSystem, ""); got != "" {
		t.Fatalf("两者皆无时应为空, got %q", got)
	}
}

// TestAgentCoreMessagesNextRunSystemKeepsInitialContext 锁消费端全链：压缩
// 落库后，下一轮 run 取到的 System 上下文（agentCoreMessages 的第二返回值，
// 经 agentCoreSystemPromptWithContext 拼进内核 Config.System）仍含立身上下文。
func TestAgentCoreMessagesNextRunSystemKeepsInitialContext(t *testing.T) {
	mgr := newAgentAIManager()
	sess := newAgentCoreCompactTestSession()
	mgr.mu.Lock()
	mgr.sessions[sess.id] = sess
	mgr.mu.Unlock()

	// 自动压缩落库（同 OnCompact 路径）。
	mgr.replaceAgentCoreHistory(sess.id, sess.runSeq, []agentcore.Message{
		agentcore.TextMessage("user", "压缩摘要"),
		agentcore.TextMessage("user", "第二轮问题"),
	})

	msgs, sessionContext := mgr.agentCoreMessages(agentAIRun{sessionID: sess.id, runSeq: sess.runSeq})
	if !strings.Contains(sessionContext, "目标：修复 compact 链路") {
		t.Fatalf("压缩后下一轮 run 的 System 上下文丢失立身上下文: %q", sessionContext)
	}
	if len(msgs) == 0 || msgs[0].Role != "user" {
		t.Fatalf("压缩后下一轮 messages 首条应可发送: %+v", msgs)
	}
	// System prompt 全链：立身上下文进 <session-context> 块。
	prompt := agentCoreSystemPromptWithContext("/proj", sessionContext)
	if !strings.Contains(prompt, "<session-context>") || !strings.Contains(prompt, "目标：修复 compact 链路") {
		t.Fatal("System prompt 未包含立身上下文")
	}

	// 无 initial_context 的普通会话不受影响：压缩后无 system 条目、System
	// 上下文为空、prompt 无 <session-context> 块。
	plain := &agentAISession{
		id: "s-plain", mode: "vibe", projectPath: "/proj", provider: "agentcore",
		runSeq: 1, activity: newAgentAIActivity(),
		history: []agentAIMessage{
			{Role: "user", Content: "普通问题"},
			{Role: "assistant", Content: "普通回答"},
		},
	}
	mgr.mu.Lock()
	mgr.sessions[plain.id] = plain
	mgr.mu.Unlock()
	mgr.replaceAgentCoreHistory(plain.id, plain.runSeq, []agentcore.Message{
		agentcore.TextMessage("user", "压缩摘要"),
		agentcore.TextMessage("assistant", "普通回答"),
	})
	mgr.mu.Lock()
	plainHistory := append([]agentAIMessage(nil), plain.history...)
	mgr.mu.Unlock()
	for _, item := range plainHistory {
		if item.Role == "system" {
			t.Fatalf("无 initial_context 的会话压缩后不应出现 system 条目: %+v", plainHistory)
		}
	}
	if _, ctx := mgr.agentCoreMessages(agentAIRun{sessionID: plain.id, runSeq: plain.runSeq}); ctx != "" {
		t.Fatalf("普通会话的 System 上下文应为空, got %q", ctx)
	}
}

// ---------------------------------------------------------------------------
// 压缩进度活性帧（内核 system/compact_progress → 服务层 markProgress）
// ---------------------------------------------------------------------------

// agentCoreCompactProgressOnlyNDJSON 是内核压缩期间的**唯一**帧形（纯压缩流：
// 只有 compact_progress，无任何 text/thinking/retry 信号）。活性验收必须用这
// 条纯帧——混进正文增量的话，emitAIDelta 的 markProgress 会掩盖本接线的缺失，
// 变异验证就不红了（与 thinking/retry 验收同一道理）。
const agentCoreCompactProgressOnlyNDJSON = `{"type":"system","subtype":"compact_progress","summary_tokens":600,"session_id":"sess-k"}` + "\n"

// TestAgentCoreCompactProgressKeepsSlowCompactOffStaleScanner 是本任务的验收
// 核心：纯压缩流持续超过 stale 阈值时，compact_progress 帧必须把活性刷回
// 阈值之内、不被 stale 扫描器判死（8192 token 慢摘要 @20-25 tok/s > 5min 的
// 健康慢压缩不再误杀）。对照臂钉住旧内核行为（压缩全程零帧 → 零信号 →
// turn_stalled），保证健康臂不是扫描器坏掉带来的假绿。
//
// 变异守卫：摘掉 decoder 分支的 run.activity.markProgress()，健康臂即红。
func TestAgentCoreCompactProgressKeepsSlowCompactOffStaleScanner(t *testing.T) {
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	oldGrace := agentAIKillGrace
	agentAIKillGrace = 30 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	// 流的 wire 输出走独立丢弃 writer：guard 的 rec 只留给终态帧。
	streamOut := agentTerminalWriter(func(interface{}) error { return nil })

	// ---- 健康臂：纯 compact_progress 帧（无任何正文）必须独立撑住活性 ----
	healthy, _, healthyGuard, healthyRec := newGuardTestManager(t)
	backdateActivityStale(t, healthyGuard.activity)

	streamStructuredAIDelta(
		strings.NewReader(agentCoreCompactProgressOnlyNDJSON),
		agentAIOutputClaudeStreamJSON,
		agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: healthyGuard.activity},
		streamOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)

	if idle := healthyGuard.activity.idleFor(); idle >= agentAIStaleRunThreshold {
		t.Fatalf("compact_progress frame did not refresh liveness: idleFor=%v >= threshold=%v", idle, agentAIStaleRunThreshold)
	}
	if reason, dead := healthyGuard.activity.staleDeathReason(
		agentAIStaleRunThreshold, agentAIApprovalTimeout, agentAIToolWaitCeiling, agentAISubagentWaitCeiling,
	); dead {
		t.Fatalf("healthy slow-compact run judged dead: reason=%q", reason)
	}

	healthy.scanStaleRuns()
	// 扫描周期之外的收敛余量：误杀的话 killReason 同步写、终态帧在宽限期后
	// 出现，sleep 覆盖两者。
	time.Sleep(200 * time.Millisecond)
	if reason := healthyGuard.activity.killReasonOr(""); reason != "" {
		t.Fatalf("slow-compact run was killed by stale scanner, reason=%q", reason)
	}
	if got := len(healthyRec.snapshot()); got != 0 {
		t.Fatalf("slow-compact run produced %d terminal payloads, want 0", got)
	}

	// ---- 对照臂：旧内核压缩全程零帧 → 服务层零信号 → 必须判死 ----
	silent, _, silentGuard, silentRec := newGuardTestManager(t)
	backdateActivityStale(t, silentGuard.activity)

	streamStructuredAIDelta(
		strings.NewReader(""),
		agentAIOutputClaudeStreamJSON,
		agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: silentGuard.activity},
		streamOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)

	silent.scanStaleRuns()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if payloads := silentRec.snapshot(); len(payloads) == 1 {
			if got := payloads[0]["error_code"]; got != "turn_stalled" {
				t.Fatalf("control arm error_code = %v, want turn_stalled", got)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reason := silentGuard.activity.killReasonOr(""); reason == "" {
		t.Fatal("control arm (no frames) was not killed — stale scanner broken, the healthy arm proves nothing")
	}

	// ---- 副作用臂：compact_progress 帧不得产生任何正文/结构化副作用 ----
	// （不转发手机、不进正文、不发终态——它只是服务层活性信号。）
	mu, events, captureOut := captureAIWriter(t)
	streamStructuredAIDelta(
		strings.NewReader(agentCoreCompactProgressOnlyNDJSON),
		agentAIOutputClaudeStreamJSON,
		agentAIRun{sessionID: "sess-1", messageID: "msg-1", runSeq: 1, activity: newAgentAIActivity()},
		captureOut, nil, nil, nil, &claudeRetryInfo{}, nil,
	)
	if got := len(*events); got != 0 {
		t.Fatalf("compact_progress 帧产生 %d 个 wire 事件, want 0（只做 markProgress+日志，不转发手机）: %v", got, events)
	}
	if n := len(findAIEvents(mu, events, "ai.delta")); n != 0 {
		t.Fatalf("compact_progress 帧不得产生 ai.delta, got %d", n)
	}
}

// TestAgentCoreManualCompactHasNoStaleKillSurface 钉住 /compact 手动压缩路径
// 的「无误杀面」裁决（2026-10-28 复核）：手动压缩不在 run 内、不挂 guard，
// stale 扫描器（scanStaleRuns 只判 guard != nil 的在跑回合）扫不到它——压缩
// 期再静默也不会被判死，CompactOption/WithCompactProgress 在这条路径无处
// 安放（没有 run.activity 可刷），确认无需接帧。见
// handleAgentCoreCompactCommand 里的定谳注释。
//
// 执行必须真穿透 handleAgentCoreCompactCommand 里的 Compact 调用：非空历史 +
// 假上游回摘要 + 小 contextWindow（split 合法的最小前提），空历史会在早退
// 分支返回，测不到任何误杀面。对照臂证明扫描器本身没坏——同形态一旦挂上
// guard 就会被判死，主臂的存活只能归因于「无 guard」。
//
// 变异推演（变异位置 = 给手动压缩挂 guard 且不接帧）：主臂首断言
// （sess.guard != nil → Fatal）立即红；即便绕过该断言，回拨活性时钟 + 扫描器
// 在对照臂已证的判死路径下会 cancel 会话并发 ai.error 终态，同样红。
func TestAgentCoreManualCompactHasNoStaleKillSurface(t *testing.T) {
	oldThreshold := agentAIStaleRunThreshold
	agentAIStaleRunThreshold = 5 * time.Minute
	t.Cleanup(func() { agentAIStaleRunThreshold = oldThreshold })

	// 记录-2 对齐（同文件 TestAgentCoreCompactProgressKeepsSlowCompactOffStaleScanner
	// 先例）：收窄 kill 宽限期，变异若真造出误杀，强拆终态不必等 30s 默认值。
	oldGrace := agentAIKillGrace
	agentAIKillGrace = 30 * time.Millisecond
	t.Cleanup(func() { agentAIKillGrace = oldGrace })

	// 假上游：第 1 个请求就是压缩摘要调用（/compact 只发这一次模型请求）。
	up, srv := newAgentCoreUpstream(t, agentCoreTextTurn("早期内容摘要", ""))
	// agentCoreBaseURL 只接受回环地址；httptest 正好是 127.0.0.1:PORT。
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	mgr := newAgentAIManager()
	sess := newSlashTestSession()
	sess.provider = "agentcore"
	// 非空历史 + 小窗口：3 条各约 2k tokens 的消息，尾部预算 10000/4=2500
	// 装不下全部历史 → split 落在中间；转录预算 10000/2=5000 装得下前两条
	// → 摘要请求有料可带（窗口再小会先撞 ErrNothingToCompact 的空转录分支，
	// 同样测不到误杀面）。Compact 一定会发起真实的摘要请求。
	sess.history = []agentAIMessage{
		{Role: "user", Content: strings.Repeat("早", 2000)},
		{Role: "assistant", Content: strings.Repeat("复", 2000)},
		{Role: "user", Content: strings.Repeat("问", 2000)},
	}
	sess.contextWindow = 10000
	// 真实会话形态：cancel 已挂（会话起过跑）、activity 非空——唯独没有
	// guard。这样 scanStaleRuns 的跳过只能归因于 guard == nil 这一条判据。
	cancelled := false
	sess.cancel = func() { cancelled = true }
	mgr.sessions[sess.id] = sess

	mu, events, w := captureAIWriter(t)
	mgr.handleAgentCoreCompactCommand(sess, "msg_c1", w)

	if len(findAIEvents(mu, events, "ai.run.started")) != 1 {
		t.Fatalf("ai.run.started 数量异常: %v", events)
	}
	// 穿透证明：done 之前必须有一条「已压缩」delta——它只在 Compact 成功
	// 返回后发出；早退分支的提示语是「没有可压缩的上下文」。
	deltas := findAIEvents(mu, events, "ai.delta")
	if len(deltas) != 1 {
		t.Fatalf("ai.delta 数量 = %d, want 1（事件 %v）", len(deltas), events)
	}
	if delta, _ := deltas[0]["delta"].(string); !strings.Contains(delta, "已压缩") {
		t.Fatalf("未穿透 Compact 成功路径（delta = %q）——测试前提不成立", delta)
	}
	if len(findAIEvents(mu, events, "ai.done")) != 1 {
		t.Fatalf("ai.done 数量异常: %v", events)
	}
	if got := len(up.requests()); got != 1 {
		t.Fatalf("上游收到 %d 个请求, want 1（/compact 只发摘要调用）", got)
	}

	// 关键裁决：手动压缩全程不挂 guard——这是「扫描器扫不到」的结构性依据。
	if sess.guard != nil {
		t.Fatal("手动压缩不应挂 run guard（否则压缩期静默就落进了 stale 扫描器的射程）")
	}

	// 回拨活性时钟到 stale 阈值之外再扫：无 guard 会话必须被整体跳过——
	// 不判死、不强拆、不发终态。
	backdateActivityStale(t, sess.activity)
	mgr.scanStaleRuns()
	time.Sleep(150 * time.Millisecond) // 误杀的话 kill 同步写 + 宽限期后出终态

	if sess.guard != nil {
		t.Fatal("扫描器不应给无 guard 会话补挂 guard")
	}
	if cancelled {
		t.Fatal("无 guard 会话被 stale 扫描器强拆了——误杀面成立")
	}
	if got := len(findAIEvents(mu, events, "ai.error")); got != 0 {
		t.Fatalf("无 guard 会话收到 %d 个 ai.error 终态，want 0: %v", got, events)
	}

	// ---- 对照臂：同形态一旦挂上 guard 就会被判死 ----
	// 证明扫描器没坏、主臂的存活只能归因于「无 guard」。若日后真给手动压缩
	// 挂 guard（变异位置），主臂红的原因就是这条已验证的判死路径。
	guarded, _, guard, guardRec := newGuardTestManager(t)
	backdateActivityStale(t, guard.activity)
	guarded.scanStaleRuns()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if payloads := guardRec.snapshot(); len(payloads) == 1 {
			if got := payloads[0]["error_code"]; got != "turn_stalled" {
				t.Fatalf("对照臂 error_code = %v, want turn_stalled", got)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reason := guard.activity.killReasonOr(""); reason == "" {
		t.Fatal("对照臂（同形态 + guard）未被判死——扫描器坏了，主臂的存活证明不了任何事")
	}
}
