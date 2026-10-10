package services

import (
	"reflect"
	"strings"
	"testing"
)

// 评审回归(2026-10-10):withAgentReadOnlyPolicy / withAgentAIAttachments /
// agentAIDiagnosticArgs 原本都假设 prompt 在 tool.args 末位;Windows .cmd 路径
// prompt 改走 stdin 后,该不变量失效——flags 会被插到最后一个 flag 值前、附件
// 后缀拼到 flag 值上、日志把 flag 值伪装成 prompt。本文件锁定 stdin 路径的
// 正确形态,同时护栏 argv 路径零变化。

func windowsCmdTool(prompt string) *agentAITool {
	return newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\AppData\Roaming\npm\claude.cmd`, prompt, "", "", "")
}

func windowsCmdToolWithEffort(prompt, effort string) *agentAITool {
	return newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\AppData\Roaming\npm\claude.cmd`, prompt, "sonnet", effort, "")
}

func containsExact(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestWithAgentReadOnlyPolicyStdinPromptAppendsFlags(t *testing.T) {
	tool := windowsCmdToolWithEffort("explore only", "high")
	pol := withAgentReadOnlyPolicy(tool)
	for i := 0; i+1 < len(pol.args); i++ {
		if pol.args[i] == "--effort" && pol.args[i+1] != "high" {
			t.Errorf("--effort 值被污染: %v", pol.args)
		}
		if pol.args[i] == "--model" && pol.args[i+1] != "sonnet" {
			t.Errorf("--model 值被污染: %v", pol.args)
		}
	}
	for _, want := range []string{"--permission-mode", "plan", "--disallowedTools", "Bash,Edit,Write,NotebookEdit", "--strict-mcp-config", "--mcp-config"} {
		if !containsExact(pol.args, want) {
			t.Errorf("缺只读 flag %s: %v", want, pol.args)
		}
	}
	if len(pol.args) > 0 && pol.args[len(pol.args)-1] == "--" {
		t.Errorf("stdin 路径不应以 -- 终止符结尾(其后无位置参数): %v", pol.args)
	}
	if pol.stdinPrompt != "explore only" {
		t.Errorf("stdinPrompt 不应被改动: %q", pol.stdinPrompt)
	}
}

func TestWithAgentReadOnlyPolicyStdinPromptNoTrailingFlags(t *testing.T) {
	// 无 model/effort:args 末位是 --append-system-prompt 的值,只读 flags 必须
	// 追加其后而不是插进该值前面
	tool := windowsCmdTool("explore only")
	pol := withAgentReadOnlyPolicy(tool)
	idx := -1
	for i := 0; i+1 < len(pol.args); i++ {
		if pol.args[i] == "--append-system-prompt" {
			idx = i
		}
	}
	if idx < 0 || pol.args[idx+1] != agentAIOptionSystemPrompt {
		t.Errorf("--append-system-prompt 值被污染: %v", pol.args)
	}
	if !containsExact(pol.args, "--permission-mode") {
		t.Errorf("缺只读 flags: %v", pol.args)
	}
}

func TestWithAgentReadOnlyPolicyArgvPromptUnchanged(t *testing.T) {
	// argv 路径护栏(.exe 原生形态):flags 插在 prompt 前,末位仍是 prompt,含 "--" 终止符
	tool := newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\.local\bin\claude.exe`, "real prompt", "sonnet", "high", "")
	pol := withAgentReadOnlyPolicy(tool)
	if len(pol.args) == 0 || pol.args[len(pol.args)-1] != "real prompt" {
		t.Errorf("argv 路径 prompt 应保持在末位: %v", pol.args)
	}
	for i := 0; i+1 < len(pol.args); i++ {
		if pol.args[i] == "--effort" && pol.args[i+1] != "high" {
			t.Errorf("--effort 值被污染: %v", pol.args)
		}
	}
	if !containsExact(pol.args, "--") {
		t.Errorf("argv 路径应保留 -- 终止符: %v", pol.args)
	}
}

func TestWithAgentAIAttachmentsStdinPromptSuffixRidesStdin(t *testing.T) {
	tool := windowsCmdTool("question")
	orig := append([]string(nil), tool.args...)
	got := withAgentAIAttachments(tool, []agentAIAttachment{{Type: "text", Path: "/tmp/notes.txt", Name: "notes.txt"}})
	if !reflect.DeepEqual(got.args, orig) {
		t.Errorf("stdin 路径 args 不应被改动: %v", got.args)
	}
	if !strings.Contains(got.stdinPrompt, "question") || !strings.Contains(got.stdinPrompt, "/tmp/notes.txt") {
		t.Errorf("附件清单应拼入 stdinPrompt: %q", got.stdinPrompt)
	}
	for _, a := range got.args {
		if strings.Contains(a, "notes.txt") {
			t.Errorf("附件路径泄漏进 argv: %v", got.args)
		}
	}
}

func TestWithAgentAIAttachmentsArgvPromptUnchanged(t *testing.T) {
	tool := newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\.local\bin\claude.exe`, "question", "", "", "")
	got := withAgentAIAttachments(tool, []agentAIAttachment{{Type: "text", Path: "/tmp/notes.txt", Name: "notes.txt"}})
	if len(got.args) == 0 || !strings.HasPrefix(got.args[len(got.args)-1], "question") {
		t.Errorf("argv 路径 prompt 应在末位且带附件后缀: %v", got.args)
	}
}

func TestAgentAIToolDiagnosticArgsStdinPrompt(t *testing.T) {
	tool := windowsCmdTool("user question") // 13 chars
	got := agentAIToolDiagnosticArgs(tool)
	joined := strings.Join(got, " ")
	if strings.Count(joined, "<prompt:") != 1 || !strings.Contains(joined, "<prompt:13 chars via stdin>") {
		t.Errorf("stdin 路径应恰好标注一次 via stdin 的 prompt 长度: %v", got)
	}
	if containsExact(got, "<prompt:46 chars>") {
		t.Errorf("普通 flag 值不应被伪装成 prompt: %v", got)
	}
}

func TestAgentAIToolDiagnosticArgsArgvPromptUnchanged(t *testing.T) {
	tool := newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\.local\bin\claude.exe`, "user question", "", "", "")
	got := agentAIToolDiagnosticArgs(tool)
	if len(got) == 0 || got[len(got)-1] != "<prompt:13 chars>" {
		t.Errorf("argv 路径末位应掩码为 prompt 长度: %v", got)
	}
}
