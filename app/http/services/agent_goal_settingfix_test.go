package services

import "testing"

func TestGoalPlanningReadOnlyClaudeHasNoEmptySettingSources(t *testing.T) {
	tool := &agentAITool{id: "claude", args: []string{"--print", "PROMPT"}}
	out := withAgentReadOnlyPolicy(tool)
	if out == nil {
		t.Fatal("expected non-nil tool")
	}
	for i, a := range out.args {
		if a == "--setting-sources" {
			if i+1 >= len(out.args) || out.args[i+1] == "" {
				t.Fatalf("--setting-sources must have a valid non-empty value, args=%v", out.args)
			}
		}
	}
}

func TestGoalPlanningReadOnlyClaudeTerminatesBeforePrompt(t *testing.T) {
	tool := &agentAITool{id: "claude", args: []string{"--print", "PROMPT"}}
	out := withAgentReadOnlyPolicy(tool)
	if out == nil {
		t.Fatal("expected non-nil tool")
	}
	hasTerm := false
	for _, a := range out.args {
		if a == "--" {
			hasTerm = true
		}
	}
	if !hasTerm {
		t.Fatalf("planning flags must terminate with -- so variadic --mcp-config does not eat the prompt, args=%v", out.args)
	}
}
