// Deterministic, no-network robustness tests for the goal planner loop. A mock
// callServer scripts the "model" responses turn-by-turn so we can exercise every
// branch of runGoalPlanLoop (valid submit, invalid-then-valid, never-converge,
// text-content fallback, gateway error) without touching a real LLM. These run
// in the normal `go test` suite — fast, deterministic, CI-friendly.
package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- helpers to build OpenAI-shaped assistant messages a real gateway returns ---

func assistantToolCalls(calls ...map[string]interface{}) map[string]interface{} {
	out := make([]interface{}, 0, len(calls))
	for i, c := range calls {
		out = append(out, map[string]interface{}{
			"id":   callID(i),
			"type": "function",
			"function": map[string]interface{}{
				"name":      c["name"],
				"arguments": c["arguments"],
			},
		})
	}
	return map[string]interface{}{"role": "assistant", "content": nil, "tool_calls": out}
}

func callID(i int) string { return "call-" + string(rune('a'+i)) }

func assistantContent(content string) map[string]interface{} {
	return map[string]interface{}{"role": "assistant", "content": content}
}

// validPlanProposalJSON returns a proposal that passes validGoalPlan, as the
// JSON string a submit_goal_plan tool call carries under "proposal".
func validPlanProposalJSON(objective string) string {
	plan := map[string]interface{}{
		"schema_version": 1,
		"objective":      objective,
		"tasks": []map[string]interface{}{
			{
				"key":           "do-the-thing",
				"title":         "Do the thing",
				"allowed_roots": []string{"/workspace/sample"},
				"checks": []map[string]interface{}{
					{"key": "verify", "required": true, "type": "command", "command": "npm test"},
				},
			},
		},
	}
	b, _ := json.Marshal(plan)
	return string(b)
}

// invalidPlanProposalJSON: missing checks → fails validGoalPlan (forces the loop
// to nudge and the model to re-emit).
func invalidPlanProposalJSON() string {
	plan := map[string]interface{}{
		"schema_version": 1,
		"objective":      "x",
		"tasks": []map[string]interface{}{
			{"key": "t", "title": "T", "allowed_roots": []string{"/workspace/sample"}}, // no checks
		},
	}
	b, _ := json.Marshal(plan)
	return string(b)
}

func submitCall(argsJSON string) map[string]interface{} {
	return map[string]interface{}{"name": "submit_goal_plan", "arguments": "{\"proposal\":" + argsJSON + "}"}
}

func readFileCall(path string) map[string]interface{} {
	return map[string]interface{}{"name": "read_file", "arguments": "{\"path\":\"" + path + "\"}"}
}

// tinyTempProject makes a 1-file repo so read_file tool execution has something
// to read in the "never converges" scenario.
func tinyTempProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# sample\nhello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestPlannerLoop_SubmitsValidPlanImmediately: model submits a valid plan on
// turn 1 → the loop returns it at once, no exploration, no nudge.
func TestPlannerLoop_SubmitsValidPlanImmediately(t *testing.T) {
	root := tinyTempProject(t)
	turn := 0
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		turn++
		return assistantToolCalls(submitCall(validPlanProposalJSON("ship it"))), "resp-1", nil
	}
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root,
		evidence:    map[string]interface{}{"note": "tiny"},
		callServer:  callServer,
		maxTurns:    5,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("invalid plan: %#v", proposal)
	}
	if turn != 1 {
		t.Fatalf("should converge on turn 1, used %d turn(s)", turn)
	}
}

// TestPlannerLoop_InvalidPlanRetriesThenSucceeds: model emits an INVALID plan
// (no checks) first → the loop nudges it and the second submission is valid.
// This is the real glm-5.2 failure mode (drops required fields); the loop must
// recover, not die.
func TestPlannerLoop_InvalidPlanRetriesThenSucceeds(t *testing.T) {
	root := tinyTempProject(t)
	turn := 0
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		turn++
		if turn == 1 {
			return assistantToolCalls(submitCall(invalidPlanProposalJSON())), "r1", nil
		}
		return assistantToolCalls(submitCall(validPlanProposalJSON("ship it"))), "r2", nil
	}
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: map[string]interface{}{}, callServer: callServer, maxTurns: 5,
	})
	if err != nil {
		t.Fatalf("invalid-then-valid should recover, got %v", err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("final plan invalid: %#v", proposal)
	}
	if turn != 2 {
		t.Fatalf("should take 2 turns (invalid→valid), took %d", turn)
	}
}

// TestPlannerLoop_NeverConvergesBudgetExceeded: model keeps exploring forever
// (only read_file calls, never submit_goal_plan) → the loop must stop at maxTurns
// and surface planner_budget_exceeded (the documented contract), not loop forever.
func TestPlannerLoop_NeverConvergesBudgetExceeded(t *testing.T) {
	root := tinyTempProject(t)
	turn := 0
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		turn++
		return assistantToolCalls(readFileCall(filepath.Join(root, "README.md"))), "r", nil
	}
	_, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: map[string]interface{}{}, callServer: callServer, maxTurns: 4,
	})
	if err == nil {
		t.Fatal("expected planner_budget_exceeded, got nil")
	}
	if err.Error() != "planner_budget_exceeded: max planning turns reached without a valid plan" {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn != 4 {
		t.Fatalf("should exhaust all 4 turns, used %d", turn)
	}
}

// TestPlannerLoop_TextContentFallback: model returns the plan as message TEXT
// (a JSON object in content), not as a submit_goal_plan tool call. The loop's
// text-fallback path must parse + validate it → success. (Weaker models that
// ignore the tool-call instruction land here.)
func TestPlannerLoop_TextContentFallback(t *testing.T) {
	root := tinyTempProject(t)
	turn := 0
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		turn++
		// Bare plan object as text content (no tool_calls, no {proposal:...} wrapper).
		return assistantContent(validPlanProposalJSON("ship it")), "r1", nil
	}
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: map[string]interface{}{}, callServer: callServer, maxTurns: 5,
	})
	if err != nil {
		t.Fatalf("text fallback should succeed, got %v", err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("parsed plan invalid: %#v", proposal)
	}
	if turn != 1 {
		t.Fatalf("text fallback should converge turn 1, used %d", turn)
	}
}

// TestPlannerLoop_TextFallbackInvalidThenNudges: text content that ISN'T a valid
// plan must NOT be accepted; the loop nudges (so a model that returns prose
// doesn't get a free pass) and only succeeds when a real plan arrives.
func TestPlannerLoop_TextFallbackInvalidThenNudges(t *testing.T) {
	root := tinyTempProject(t)
	turn := 0
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		turn++
		if turn == 1 {
			return assistantContent("Sure, I'll plan this out — let me think..."), "r1", nil // prose, not JSON
		}
		return assistantToolCalls(submitCall(validPlanProposalJSON("ship it"))), "r2", nil
	}
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: map[string]interface{}{}, callServer: callServer, maxTurns: 5,
	})
	if err != nil {
		t.Fatalf("should recover after nudge, got %v", err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("final plan invalid: %#v", proposal)
	}
	if turn != 2 {
		t.Fatalf("should nudge once then succeed, took %d", turn)
	}
}

// TestPlannerLoop_GatewayErrorPropagates: a gateway error (5xx / network) from
// callServer must bubble up as the loop result, NOT be swallowed into a misleading
// budget_exceeded.
func TestPlannerLoop_GatewayErrorPropagates(t *testing.T) {
	root := tinyTempProject(t)
	sentinel := errSentinel("gateway 502: upstream bad gateway")
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		return nil, "", sentinel
	}
	_, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: map[string]interface{}{}, callServer: callServer, maxTurns: 5,
	})
	if err != sentinel {
		t.Fatalf("gateway error must propagate verbatim, got %v", err)
	}
}

// TestPlannerLoop_ExploreThenSubmit: a realistic shape — model reads a file
// (tool executed locally), then submits a valid plan. Proves the read-only tool
// result is threaded back into the conversation and the loop converges.
func TestPlannerLoop_ExploreThenSubmit(t *testing.T) {
	root := tinyTempProject(t)
	turn := 0
	callServer := func(_ int, _ []map[string]interface{}) (map[string]interface{}, string, error) {
		turn++
		if turn == 1 {
			return assistantToolCalls(readFileCall(filepath.Join(root, "README.md"))), "r1", nil
		}
		return assistantToolCalls(submitCall(validPlanProposalJSON("ship it"))), "r2", nil
	}
	proposal, _, err := runGoalPlanLoop(goalPlanLoopInput{
		projectRoot: root, evidence: map[string]interface{}{}, callServer: callServer, maxTurns: 5,
	})
	if err != nil {
		t.Fatalf("explore-then-submit should succeed, got %v", err)
	}
	if !validGoalPlan(proposal) {
		t.Fatalf("final plan invalid: %#v", proposal)
	}
	if turn != 2 {
		t.Fatalf("should be explore(1)+submit(2), took %d", turn)
	}
}

// errSentinel is a distinct error type so we can assert the loop returns the
// exact gateway error (not a wrapped/aggregated one).
type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// TestDescribeGoalPlanAssistantAction pins the per-turn capture formatting (the
// production trace operators see in the agent log). If this regresses, the
// "model wants: ..." lines become unreadable and the interaction capture is
// useless.
func TestDescribeGoalPlanAssistantAction(t *testing.T) {
	cases := []struct {
		name string
		msg  map[string]interface{}
		want string
	}{
		{"read_file tool call",
			assistantToolCalls(map[string]interface{}{"name": "read_file", "arguments": `{"path":"src/app.ts"}`}),
			`tools=[read_file({"path":"src/app.ts"})]`},
		{"submit_goal_plan collapses args",
			assistantToolCalls(submitCall(`{}`)),
			"tools=[submit_goal_plan(<proposal>)]"},
		{"text content",
			assistantContent("here is my plan"),
			"content=here is my plan"},
		{"empty assistant message",
			map[string]interface{}{"role": "assistant"},
			"(no tool_calls, no content)"},
	}
	for _, c := range cases {
		got := describeGoalPlanAssistantAction(c.msg)
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
