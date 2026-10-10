package services

import (
	"strings"
	"testing"
)

// ---- claudePromptViaStdinForOS:Windows 批处理 shim 的 prompt 走 stdin ----

func TestClaudePromptViaStdinForOS(t *testing.T) {
	cases := []struct {
		goos, path string
		want       bool
	}{
		{"windows", `C:\Users\u\AppData\Roaming\npm\claude.cmd`, true},
		{"windows", `C:\Users\u\AppData\Roaming\npm\CLAUDE.CMD`, true},
		{"windows", `C:\Users\u\run.bat`, true},
		{"windows", `C:\Users\u\.local\bin\claude.exe`, false},
		{"windows", `C:\Users\u\claude`, false},
		{"windows", "", false},
		{"darwin", "/Users/u/AppData/Roaming/npm/claude.cmd", false},
		{"linux", "/usr/local/bin/claude.cmd", false},
	}
	for _, c := range cases {
		if got := claudePromptViaStdinForOS(c.goos, c.path); got != c.want {
			t.Errorf("claudePromptViaStdinForOS(%q, %q) = %v，期望 %v", c.goos, c.path, got, c.want)
		}
	}
}

// Windows + .cmd：prompt 是不可信自由文本（引号/%VAR%/& 等经 cmd.exe 解析会
// 被重切或展开），必须改走 stdin，绝不进 argv。
func TestNewClaudeCodeAIToolForOSWindowsCmdPromptRidesStdin(t *testing.T) {
	prompt := `运行 "npm run build" 并解释 %APPDATA% 与 a & b`
	tool := newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\AppData\Roaming\npm\claude.cmd`, prompt, "", "", "")
	for _, arg := range tool.args {
		if arg == prompt {
			t.Errorf("prompt 不得出现在 argv: %v", tool.args)
		}
		if strings.Contains(arg, "%APPDATA%") {
			t.Errorf("prompt 片段泄漏进 argv: %q", arg)
		}
	}
	if tool.stdinPrompt != prompt {
		t.Errorf("prompt 应完整走 stdin，got %q", tool.stdinPrompt)
	}
	// CLI 语义参数保持不变
	for _, flag := range []string{"--print", "--verbose", "--output-format", "stream-json"} {
		found := false
		for _, arg := range tool.args {
			if arg == flag {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少 CLI 参数 %s: %v", flag, tool.args)
		}
	}
}

// Windows + 原生 .exe：Win32 参数转义由 Go 正确处理，prompt 保持 argv 原样。
func TestNewClaudeCodeAIToolForOSWindowsExeKeepsArgvPrompt(t *testing.T) {
	prompt := "hello world"
	tool := newClaudeCodeAIToolForOS("windows", "claude", `C:\Users\u\.local\bin\claude.exe`, prompt, "", "", "")
	if len(tool.args) == 0 || tool.args[len(tool.args)-1] != prompt {
		t.Errorf(".exe 应保持 prompt 在 argv 末位，got %v", tool.args)
	}
	if tool.stdinPrompt != "" {
		t.Errorf(".exe 不应设置 stdinPrompt，got %q", tool.stdinPrompt)
	}
}

// 非 Windows 零变化护栏：prompt 照旧走 argv。
func TestNewClaudeCodeAIToolForOSNonWindowsKeepsArgvPrompt(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		tool := newClaudeCodeAIToolForOS(goos, "claude", "/bin/claude.cmd", "hello world", "", "", "")
		if len(tool.args) == 0 || tool.args[len(tool.args)-1] != "hello world" {
			t.Errorf("%s 应保持 prompt 在 argv 末位，got %v", goos, tool.args)
		}
		if tool.stdinPrompt != "" {
			t.Errorf("%s 不应设置 stdinPrompt，got %q", goos, tool.stdinPrompt)
		}
	}
}
