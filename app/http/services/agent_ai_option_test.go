package services

import (
	"os"
	"strings"
	"testing"
	"time"
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
