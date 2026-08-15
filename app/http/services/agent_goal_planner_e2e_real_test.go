//go:build e2e

// Real-project-scale planner test + per-turn interaction capture. Points the
// planner at the ACTUAL AliangBoard repo (hundreds of files) with an open-ended
// "compare and plan" objective — the shape that was timing out / running out of
// turns in production — and TRACES every turn (what the model read/grepped, what
// it got back) so the multi-round interaction is observable, not a black box.
//
// Run on a host that can reach both the gateway AND the real project:
//
//	ALIANG_TEST_PLANNER_BASE_URL=http://sub2api.liang.home \
//	ALIANG_TEST_PLANNER_API_KEY=sk-... \
//	ALIANG_TEST_PLANNER_MODEL=glm-5.2 \
//	ALIANG_TEST_PLANNER_REAL_PROJECT=/home/liang/MyProgram/AiProject/aliangboard \
//	go test -tags e2e -run TestPlannerE2E_RealProjectScale -v -timeout 30m ./app/http/services/
package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// makeTracingCallServer is makeDirectOpenAICallServer plus a full per-turn trace:
// each turn it logs (a) the previous turn's tool RESULT (what the model just
// learned) and (b) the model's new tool calls WITH arguments (what it wants to
// do next). This is the capture the production loop lacks — it proves the
// interaction is fully observable (the data flows through callServer), and for
// the real-project test it shows exactly how glm-5.2 explores a large repo.
func makeTracingCallServer(t *testing.T, objective string) goalPlanTurnCaller {
	t.Helper()
	baseURL := os.Getenv("ALIANG_TEST_PLANNER_BASE_URL")
	apiKey := os.Getenv("ALIANG_TEST_PLANNER_API_KEY")
	model := os.Getenv("ALIANG_TEST_PLANNER_MODEL")
	endpoint := plannerChatCompletionsURL(baseURL)
	system := plannerSystemMessage(objective)
	tools := plannerCanonicalTools()
	client := &http.Client{Timeout: 180 * time.Second}

	return func(turn int, messages []map[string]interface{}) (map[string]interface{}, string, error) {
		// (a) capture what the model learned last turn: the trailing tool message.
		if turn > 1 && len(messages) > 0 {
			last := messages[len(messages)-1]
			if role, _ := last["role"].(string); role == "tool" {
				t.Logf("turn %d | prev tool result: %s", turn, snippet(fmt.Sprintf("%v", last["content"]), 200))
			}
		}

		full := make([]map[string]interface{}, 0, len(messages)+1)
		full = append(full, map[string]interface{}{"role": "system", "content": system})
		full = append(full, messages...)
		body := map[string]interface{}{
			"model": model, "stream": false, "temperature": 0, "max_tokens": 8000,
			"tools": tools, "messages": full,
		}
		buf, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(buf))
		req.Header.Set("authorization", "Bearer "+apiKey)
		req.Header.Set("content-type", "application/json")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("turn %d fetch (%s): %w", turn, time.Since(start), err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("turn %d http %d: %s", turn, resp.StatusCode, snippet(string(rb), 300))
		}
		var cr struct {
			ID      string `json:"id"`
			Choices []struct {
				Message map[string]interface{} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(rb, &cr); err != nil {
			return nil, "", fmt.Errorf("turn %d decode: %v body=%s", turn, err, snippet(string(rb), 200))
		}
		if len(cr.Choices) == 0 {
			return nil, cr.ID, fmt.Errorf("turn %d: no choices", turn)
		}
		msg := cr.Choices[0].Message
		// (b) capture what the model wants now: tool calls with their arguments.
		t.Logf("turn %d | %s | model wants: %s", turn, time.Since(start), describeAssistantAction(msg))
		return msg, cr.ID, nil
	}
}

// describeAssistantAction renders an assistant message's tool_calls (name + the
// path/pattern it asked for) or a content snippet — the human-readable form of
// "what the model did this turn", which is exactly what production should persist.
func describeAssistantAction(msg map[string]interface{}) string {
	if raw, ok := msg["tool_calls"].([]interface{}); ok && len(raw) > 0 {
		parts := make([]string, 0, len(raw))
		for _, c := range raw {
			call, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			fn, _ := call["function"].(map[string]interface{})
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			// Pull the interesting bit out of the args (path / pattern) for readability.
			interesting := args
			if name == "submit_goal_plan" {
				interesting = "<proposal>"
			}
			parts = append(parts, fmt.Sprintf("%s(%s)", name, snippet(interesting, 80)))
		}
		return "tools=[" + strings.Join(parts, ", ") + "]"
	}
	if c, _ := msg["content"].(string); strings.TrimSpace(c) != "" {
		return "content=" + snippet(c, 120)
	}
	return "(no tool_calls, no content)"
}

func snippet(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestPlannerE2E_RealProjectScale — the production failure, replicated locally.
// Real AliangBoard repo + open-ended "compare and plan" objective + generous
// budget. Reports turn count + wall time + whether it converged, so we know what
// the production budgets actually need to be. Each turn is traced above.
func TestPlannerE2E_RealProjectScale(t *testing.T) {
	skipIfNoPlannerE2E(t)
	root := os.Getenv("ALIANG_TEST_PLANNER_REAL_PROJECT")
	if root == "" {
		root = "/home/liang/MyProgram/AiProject/aliangboard"
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Skipf("real project not present at %s (set ALIANG_TEST_PLANNER_REAL_PROJECT)", root)
	}

	objective := "系统比对 src 目录下的实现和 kuboard 的功能差距，梳理出主要功能差异，给出一份补齐核心差距的工程执行计划（任务要可验证、范围收敛，不要试图一次做完）。"

	callServer := makeTracingCallServer(t, objective)
	t.Logf("building planning context on real project %s (this can take a few s on a big repo)...", root)
	contextStart := time.Now()
	evidence, err := buildGoalPlanContext(root, nil)
	if err != nil {
		t.Fatalf("buildGoalPlanContext on real project: %v", err)
	}
	t.Logf("context built in %s", time.Since(contextStart))

	const maxTurns = 30
	start := time.Now()
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: evidence, callServer: callServer, maxTurns: maxTurns,
	})
	elapsed := time.Since(start)

	if err != nil {
		// budget_exceeded at this scale is itself the key finding — surface it
		// loudly with the measured numbers so the production budget can be sized.
		t.Fatalf("real-project objective did NOT converge within %d turns / %s: %v\n"+
			"➜ production needs a higher planner budget than %d turns; raise GOAL_PLAN_MAX_TURNS + planner timeout.",
			maxTurns, elapsed, err, maxTurns)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("converged but INVALID plan after %s: %#v", elapsed, proposal)
	}
	tasks, _ := proposal["tasks"].([]interface{})
	obj, _ := proposal["objective"].(string)
	t.Logf("✅ real-project objective CONVERGED: turns≤%d elapsed=%s tasks=%d objective=%q",
		maxTurns, elapsed, len(tasks), obj)
	if len(tasks) == 0 {
		t.Fatal("plan has no tasks")
	}
}
