package services

import (
	"strings"
	"testing"
)

func TestGoalPlanningReadOnlyClaudeHasNoEmptySettingSources(t *testing.T) {
	tool := &agentAITool{id: "claude", args: []string{"--print", "PROMPT"}}
	out := withGoalPlanningReadOnly(tool)
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
	out := withGoalPlanningReadOnly(tool)
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

func TestGoalEmissionOnlyLocksAllTools(t *testing.T) {
	tool := &agentAITool{id: "claude", args: []string{"--print", "--tools", "default", "PROMPT"}}
	out := withGoalEmissionOnly(tool)
	if out == nil {
		t.Fatal("expected non-nil emission tool")
	}
	hasEmptyTools, hasTerm, hasAgentDisallow := false, false, false
	for i, a := range out.args {
		if a == "--tools" && i+1 < len(out.args) && out.args[i+1] == "" {
			hasEmptyTools = true
		}
		if a == "--" {
			hasTerm = true
		}
		if a == "--disallowedTools" && i+1 < len(out.args) && strings.Contains(out.args[i+1], "Agent") {
			hasAgentDisallow = true
		}
	}
	if !hasEmptyTools {
		t.Fatalf("emission must set --tools \"\" to disable all tools, args=%v", out.args)
	}
	if !hasTerm {
		t.Fatalf("emission must terminate with -- so no variadic flag eats the prompt, args=%v", out.args)
	}
	if !hasAgentDisallow {
		t.Fatalf("emission must disallow the Agent (subagent) tool to prevent divergence, args=%v", out.args)
	}
}
