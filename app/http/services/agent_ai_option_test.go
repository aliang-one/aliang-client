package services

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func TestExtractAgentAIOptionBlocks(t *testing.T) {
	output := "我准备了两个方案：\n\n" +
		"```aliang-options\n" +
		`{"title":"选择方案","options":[{"id":"a","label":"方案A"},{"id":"b","label":"方案B","description":"更稳"}],"allow_custom":true,"multi":false}` + "\n```\n"
	got := extractAgentAIOptionBlocks(output)
	if len(got) != 1 {
		t.Fatalf("blocks = %d, want 1", len(got))
	}
	if got[0].Title != "选择方案" || !got[0].AllowCustom || len(got[0].Options) != 2 {
		t.Fatalf("parsed = %+v", got[0])
	}
	if got[0].Options[1].Description != "更稳" {
		t.Fatalf("opt[1].Description = %q", got[0].Options[1].Description)
	}
}

func TestExtractAgentAIOptionBlocksIgnoresMalformedAndEmpty(t *testing.T) {
	output := "```aliang-options\n{not json}\n```\n```aliang-options\n{\"options\":[]}\n```\n"
	if got := extractAgentAIOptionBlocks(output); len(got) != 0 {
		t.Fatalf("blocks = %d, want 0", len(got))
	}
	if got := extractAgentAIOptionBlocks("```go\nfmt.Println()\n```\n"); len(got) != 0 {
		t.Fatalf("plain code block should not match, got %d", len(got))
	}
}

func TestBuildAgentAIOptionFollowup(t *testing.T) {
	req := &agentAIOptionRequest{Title: "x", Options: []agentAIOptionChoice{
		{ID: "a", Label: "方案A"},
		{ID: "b", Label: "方案B", Description: "更稳"},
	}}
	got := buildAgentAIOptionFollowup(req, []string{"b"}, "顺便加日志")
	if !strings.Contains(got, "方案B") || !strings.Contains(got, "更稳") || !strings.Contains(got, "顺便加日志") {
		t.Fatalf("followup = %q", got)
	}
}

func TestExtractAgentAIOptionBlocksV2Fields(t *testing.T) {
	// v2 新字段（header/summary/v/response/previews）全链解析保真
	output := "```aliang-options\n" +
		`{"v":2,"title":"选择发布策略","header":"部署","summary":"两种路径","multi":false,"allow_custom":true,"options":[{"id":"blue-green","label":"蓝绿部署 (･ω･)ﾉ","description":"回滚最快","response":"已按蓝绿部署执行","previews":["kubectl apply -f a.yaml","kubectl rollout status"]}]}` + "\n```\n"
	got := extractAgentAIOptionBlocks(output)
	if len(got) != 1 {
		t.Fatalf("blocks = %d, want 1", len(got))
	}
	req := got[0]
	if req.Header != "部署" || req.Summary != "两种路径" || req.Version != 2 || !req.AllowCustom {
		t.Fatalf("v2 fields = %+v", req)
	}
	opt := req.Options[0]
	if opt.Response != "已按蓝绿部署执行" || len(opt.Previews) != 2 || opt.Previews[0] != "kubectl apply -f a.yaml" {
		t.Fatalf("choice v2 fields = %+v", opt)
	}
}

func TestNormalizeAgentAIOptionRequest(t *testing.T) {
	// 硬规范化：>8 截断；空 label 丢弃；id 缺失/非法/重复重造 option-N
	req := &agentAIOptionRequest{Title: "x", Options: []agentAIOptionChoice{
		{Label: "无id"},                       // 缺 id → option-1
		{ID: "Valid-Id_9", Label: "合法 slug"}, // 保留
		{ID: "含中文", Label: "非法 id"},          // → option-3
		{ID: "Valid-Id_9", Label: "重复 id"},   // → option-4
		{ID: "ok2", Label: "  "},             // 空 label → 丢弃
	}}
	for i := 0; i < 10; i++ {
		req.Options = append(req.Options, agentAIOptionChoice{ID: fmt.Sprintf("extra-%d", i), Label: fmt.Sprintf("多余%d", i)})
	}
	normalizeAgentAIOptionRequest(req)
	if len(req.Options) != 8 {
		t.Fatalf("options = %d, want 8 (截断到上限)", len(req.Options))
	}
	if req.Options[0].ID != "option-1" {
		t.Fatalf("缺 id 未重造: %q", req.Options[0].ID)
	}
	if req.Options[1].ID != "Valid-Id_9" {
		t.Fatalf("合法 slug 被改: %q", req.Options[1].ID)
	}
	if req.Options[2].ID != "option-3" || req.Options[3].ID != "option-4" {
		t.Fatalf("非法/重复 id 未重造: %q / %q", req.Options[2].ID, req.Options[3].ID)
	}
	for _, opt := range req.Options {
		if opt.Label == "  " {
			t.Fatalf("空 label 未丢弃: %+v", req.Options)
		}
	}
	ids := make(map[string]bool)
	for _, opt := range req.Options {
		if ids[opt.ID] {
			t.Fatalf("id 仍重复: %q", opt.ID)
		}
		ids[opt.ID] = true
		if !agentAIOptionIDRe.MatchString(opt.ID) {
			t.Fatalf("规范化后 id 仍非法: %q", opt.ID)
		}
	}
}

func TestBuildAgentAIOptionFollowupResponsePriority(t *testing.T) {
	// v2：response 优先原样回填；无 response 退回 label（description）旧形态
	req := &agentAIOptionRequest{Title: "x", Options: []agentAIOptionChoice{
		{ID: "a", Label: "方案A", Response: "已按方案A迁移完成"},
		{ID: "b", Label: "方案B", Description: "更稳"},
	}}
	got := buildAgentAIOptionFollowup(req, []string{"a", "b"}, "")
	if !strings.Contains(got, "已按方案A迁移完成") || strings.Contains(got, "选择了：方案A") {
		t.Fatalf("response 未优先回填: %q", got)
	}
	if !strings.Contains(got, "选择了：方案B（更稳）") {
		t.Fatalf("无 response 未退回 label 形态: %q", got)
	}
}

func TestEmitOptionRequestSendsEventAndStashesPending(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude", activity: newAgentAIActivity(),
	}
	manager.mu.Unlock()

	run := agentAIRun{sessionID: "s1", runSeq: 2, messageID: "m1", provider: "claude", activity: manager.sessions["s1"].activity}
	blocks := []agentAIOptionRequest{
		{Title: "选方案", Options: []agentAIOptionChoice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}, AllowCustom: true},
	}
	manager.emitOptionRequest(run, writer, blocks)

	req := lastAIEvent(mu, events, "ai.option.request")
	if req == nil {
		t.Fatal("expected ai.option.request emitted")
	}
	if req["session_id"] != "s1" || req["option_id"] == nil || req["option_id"] == "" {
		t.Fatalf("request = %+v", req)
	}
	if req["title"] != "选方案" || req["allow_custom"] != true {
		t.Fatalf("request fields = %+v", req)
	}
	manager.mu.Lock()
	pending := manager.sessions["s1"].pendingOption
	manager.mu.Unlock()
	if pending == nil || pending.Title != "选方案" {
		t.Fatalf("pendingOption = %+v", pending)
	}
}

func TestEmitOptionRequestSkipsStaleRunSeq(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{id: "s1", runSeq: 9, activity: newAgentAIActivity()}
	manager.mu.Unlock()
	run := agentAIRun{sessionID: "s1", runSeq: 2, messageID: "m1", activity: manager.sessions["s1"].activity}
	manager.emitOptionRequest(run, writer, []agentAIOptionRequest{
		{Options: []agentAIOptionChoice{{ID: "a", Label: "A"}}},
	})
	if got := findAIEvents(mu, events, "ai.option.request"); len(got) != 0 {
		t.Fatalf("expected no event for stale runSeq")
	}
}

func TestOptionResponseClearsPendingAndDispatchesRun(t *testing.T) {
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "claude", "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"session_id\":\"option-session\"}'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude", mode: "vibe",
		projectPath: t.TempDir(), activity: newAgentAIActivity(),
		pendingOption: &agentAIOptionRequest{
			ID: "opt_s1_2", MessageID: "m_asst", Options: []agentAIOptionChoice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
		},
	}
	manager.mu.Unlock()

	manager.optionResponse(map[string]interface{}{
		"type": "ai.option.response", "session_id": "s1", "option_id": "opt_s1_2",
		"selected": []interface{}{"b"}, "custom_text": "",
	}, writer)

	// runCLI is dispatched in a goroutine; ai.run.started (or ai.error) is
	// emitted synchronously at dispatch. Poll briefly for the event before
	// asserting, since the assertion requires the run to have actually started.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if lastAIEvent(mu, events, "ai.run.started") != nil || lastAIEvent(mu, events, "ai.error") != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	manager.mu.Lock()
	pending := manager.sessions["s1"].pendingOption
	runSeq := manager.sessions["s1"].runSeq
	manager.mu.Unlock()
	if pending != nil {
		t.Fatalf("pendingOption not cleared, got %+v", pending)
	}
	if runSeq != 3 {
		t.Fatalf("runSeq = %d, want 3 (new run dispatched)", runSeq)
	}
	started := lastAIEvent(mu, events, "ai.run.started")
	errEv := lastAIEvent(mu, events, "ai.error")
	if started == nil && errEv == nil {
		t.Fatal("expected either ai.run.started or ai.error after option followup dispatch")
	}
}

func TestOptionResponseNoMatchIsHarmless(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)
	manager.mu.Lock()
	manager.sessions["s1"] = &agentAISession{id: "s1", runSeq: 2}
	manager.mu.Unlock()
	manager.optionResponse(map[string]interface{}{
		"type": "ai.option.response", "session_id": "s1", "option_id": "opt_unknown",
		"selected": []interface{}{"a"},
	}, writer)
	if got := findAIEvents(mu, events, "ai.run.started"); len(got) != 0 {
		t.Fatalf("expected no run dispatched for unmatched option")
	}
}

// --- 2026-11-08 决策跨端同步：option 作答必须广播 resolved 同型帧 ---
//
// 根因：optionResponse 整函数零帧发出——手机端选了电脑卡永 pending（对称方向
// 同理），PhoneServer 派生审批也无从 settle。镜像审批族 status:"resolved"
// 同型约定（requestApproval 的 <-respond 分支重发 ai.approval.request）。

// fence 分支正常作答：resolved 帧形状（option_id/selected/message_id 齐全，
// pendingOption 清空，续跑 run 照常发起）。
func TestOptionResponseEmitsResolvedFrame(t *testing.T) {
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "claude", "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"session_id\":\"option-session\"}'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	mu, events, writer := captureAIWriter(t)

	m.mu.Lock()
	m.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude", mode: "vibe",
		projectPath: t.TempDir(), activity: newAgentAIActivity(),
		originSocket: "aisock-7",
		pendingOption: &agentAIOptionRequest{
			ID: "opt_s1_2", MessageID: "m_asst",
			Options: []agentAIOptionChoice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
		},
	}
	m.mu.Unlock()

	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_s1_2",
		"selected": []interface{}{"b"}, "custom_text": "",
		agentAIOriginSocketField: "aisock-7",
	}, writer)

	resolved := lastAIEvent(mu, events, models.AgentEventAIOptionRequest)
	if resolved == nil {
		t.Fatal("expected resolved ai.option.request frame emitted")
	}
	if resolved["status"] != "resolved" {
		t.Fatalf("resolved frame status = %v, want resolved", resolved["status"])
	}
	if resolved["option_id"] != "opt_s1_2" || resolved["session_id"] != "s1" {
		t.Fatalf("resolved frame ids = %+v", resolved)
	}
	sel, _ := resolved["selected"].([]string)
	if len(sel) != 1 || sel[0] != "b" {
		t.Fatalf("resolved frame selected = %v, want [b]", resolved["selected"])
	}
	if resolved["message_id"] != "m_asst" {
		t.Fatalf("resolved frame message_id = %v, want m_asst", resolved["message_id"])
	}
	if resolved["provider"] != "claude" {
		t.Fatalf("resolved frame provider = %v, want claude", resolved["provider"])
	}
	m.mu.Lock()
	pending := m.sessions["s1"].pendingOption
	m.mu.Unlock()
	if pending != nil {
		t.Fatalf("pendingOption not cleared after resolved emit: %+v", pending)
	}
}

// 广播性：作答 socket 内联之外，run 发起 socket（手机模拟）也必须收到
// resolved 帧——ai.option.request 在审批族白名单里，fanOutWrite 全端可达。
func TestOptionResolvedFrameFansOutToRunSocket(t *testing.T) {
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "claude", "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"session_id\":\"option-session\"}'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	runCh := make(chan map[string]interface{}, 16)
	decideCh := make(chan map[string]interface{}, 16)
	runSock := m.registerAISocket("remote", func(p interface{}) error { runCh <- p.(map[string]interface{}); return nil })
	decideSock := m.registerAISocket("local", func(p interface{}) error { decideCh <- p.(map[string]interface{}); return nil })

	m.mu.Lock()
	m.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude", mode: "vibe",
		projectPath: t.TempDir(), activity: newAgentAIActivity(),
		originSocket: runSock.id,
		pendingOption: &agentAIOptionRequest{
			ID: "opt_x", MessageID: "m1",
			Options: []agentAIOptionChoice{{ID: "a", Label: "A"}},
		},
	}
	m.mu.Unlock()

	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_x",
		"selected":               []interface{}{"a"},
		agentAIOriginSocketField: decideSock.id,
	}, func(p interface{}) error { decideCh <- p.(map[string]interface{}); return nil })

	// run 发起通道（手机模拟）必须收到 resolved 帧（fanOut 广播）。
	drainedResolved := false
	waitUntilFor(t, 2*time.Second, func() bool {
		for len(runCh) > 0 {
			if p := <-runCh; p["type"] == models.AgentEventAIOptionRequest && p["status"] == "resolved" {
				drainedResolved = true
				return true
			}
		}
		return false
	})
	if !drainedResolved {
		t.Fatal("run 发起通道未收到 resolved ai.option.request（fanOut 广播缺失）")
	}
}

// codex waiter 分支：喂 waiter 之后同样广播 resolved（此前零帧提前 return）。
func TestOptionResponseCodexWaiterEmitsResolvedFrame(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	mu, events, writer := captureAIWriter(t)
	respond := make(chan agentAICodexInputAnswer, 1)
	m.mu.Lock()
	m.codexInputs[agentAICodexInputMapKey("s1", "opt_c")] = &agentAICodexInputWaiter{
		sessionID: "s1", runSeq: 1, optionID: "opt_c", respond: respond,
	}
	m.sessions["s1"] = &agentAISession{id: "s1", runSeq: 1}
	m.mu.Unlock()

	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_c",
		"selected": []interface{}{"a"}, "custom_text": "备注",
		agentAIOriginSocketField: "aisock-5",
	}, writer)

	if got := <-respond; len(got.selected) != 1 || got.selected[0] != "a" || got.custom != "备注" {
		t.Fatalf("waiter 未收到答案: %+v", got)
	}
	resolved := lastAIEvent(mu, events, models.AgentEventAIOptionRequest)
	if resolved == nil || resolved["status"] != "resolved" {
		t.Fatalf("codex 作答应发 resolved 帧, got %+v", resolved)
	}
	if resolved["provider"] != "codex" {
		t.Fatalf("codex resolved 帧 provider = %v, want codex", resolved["provider"])
	}
}

// sync_only（PhoneServer 手机决议旁路）：只清 pendingOption + 广播翻卡帧，
// 不得起续跑 run——手机决议已由 PhoneServer dispatchUserAiMessage 起新回合，
// 再走续跑路径会双重注入同一选择。
func TestOptionResponseSyncOnlyClearsWithoutRun(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	mu, events, writer := captureAIWriter(t)
	m.mu.Lock()
	m.sessions["s1"] = &agentAISession{
		id: "s1", runSeq: 2, provider: "claude",
		activity: newAgentAIActivity(),
		pendingOption: &agentAIOptionRequest{
			ID: "opt_s", MessageID: "m1",
			Options: []agentAIOptionChoice{{ID: "a", Label: "A"}},
		},
	}
	m.mu.Unlock()

	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_s",
		"selected": []interface{}{"a"}, "sync_only": true,
	}, writer)

	m.mu.Lock()
	pending := m.sessions["s1"].pendingOption
	m.mu.Unlock()
	if pending != nil {
		t.Fatalf("sync_only 未清 pendingOption: %+v", pending)
	}
	resolved := lastAIEvent(mu, events, models.AgentEventAIOptionRequest)
	if resolved == nil || resolved["status"] != "resolved" || resolved["option_id"] != "opt_s" {
		t.Fatalf("sync_only 应发 resolved 帧, got %+v", resolved)
	}
	if got := findAIEvents(mu, events, "ai.run.started"); len(got) != 0 {
		t.Fatalf("sync_only 不得起续跑 run, got %d", len(got))
	}
}

// 迟到/重复 sync 幂等：卡已不在场（另一端已消费）仍发 resolved 帧——
// 发起端的翻卡信号不能因 agent 侧状态而丢。
func TestOptionResponseSyncOnlyUnknownIdIsIdempotent(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	mu, events, writer := captureAIWriter(t)
	m.mu.Lock()
	m.sessions["s1"] = &agentAISession{id: "s1", runSeq: 1}
	m.mu.Unlock()
	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_gone",
		"selected": []interface{}{"a"}, "sync_only": true,
	}, writer)
	resolved := lastAIEvent(mu, events, models.AgentEventAIOptionRequest)
	if resolved == nil || resolved["status"] != "resolved" || resolved["option_id"] != "opt_gone" {
		t.Fatalf("迟到 sync_only 应幂等发 resolved 帧, got %+v", resolved)
	}
	if got := findAIEvents(mu, events, "ai.run.started"); len(got) != 0 {
		t.Fatalf("unknown sync_only 不得起 run")
	}
}

// codex waiter + sync_only：只发翻卡帧，不喂 waiter——活跑的 requestUserInput
// 等待态不被旁路注入答案（答案的续跑由发起端自行负责）。
func TestOptionResponseCodexSyncOnlyDoesNotFeedWaiter(t *testing.T) {
	m := newTestAISocketsManager(t)
	defer m.closeAll()
	mu, events, writer := captureAIWriter(t)
	respond := make(chan agentAICodexInputAnswer, 1)
	m.mu.Lock()
	m.codexInputs[agentAICodexInputMapKey("s1", "opt_c")] = &agentAICodexInputWaiter{
		sessionID: "s1", runSeq: 1, optionID: "opt_c", respond: respond,
	}
	m.sessions["s1"] = &agentAISession{id: "s1", runSeq: 1}
	m.mu.Unlock()

	m.optionResponse(map[string]interface{}{
		"type": models.AgentEventAIOptionResponse, "session_id": "s1", "option_id": "opt_c",
		"selected": []interface{}{"a"}, "sync_only": true,
	}, writer)

	select {
	case got := <-respond:
		t.Fatalf("sync_only 不得喂 waiter, got %+v", got)
	default:
	}
	resolved := lastAIEvent(mu, events, models.AgentEventAIOptionRequest)
	if resolved == nil || resolved["status"] != "resolved" {
		t.Fatalf("codex sync_only 应发 resolved 帧, got %+v", resolved)
	}
}

func TestOptionCancelledEmittedOnClose(t *testing.T) {
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	manager.mu.Lock()
	manager.sessions["s5"] = &agentAISession{id: "s5", runSeq: 1, pendingOption: &agentAIOptionRequest{ID: "opt_c"}}
	manager.mu.Unlock()

	manager.close(map[string]interface{}{"type": "ai.session.close", "session_id": "s5"}, writer)

	cancelled := lastAIEvent(mu, events, "ai.option.cancelled")
	if cancelled == nil || cancelled["session_id"] != "s5" {
		t.Fatalf("expected ai.option.cancelled on close, got %+v", cancelled)
	}
	ids, _ := cancelled["option_ids"].([]string)
	if len(ids) != 1 || ids[0] != "opt_c" {
		t.Fatalf("cancelled option_ids = %v, want [opt_c]", ids)
	}
	// close() deletes the session entry, so pendingOption can no longer be read;
	// the cancelled event + option_ids above are the contract under test.
}
