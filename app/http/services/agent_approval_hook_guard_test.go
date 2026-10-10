package services

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// 护栏(2026-10-10 审计):--settings JSON 是唯一横穿 Windows cmd.exe 的自由
// 内容面。当前安全完全依赖两道隐式防线——①json.Marshal 的 escapeHTML 把
// & < > 转成 \uXXXX;②URL/JSON 内容本身不含 % ^ 等 cmd 元字符(% 展开在引号
// 区内也生效,无任何转义挡它)。任何人改 SetEscapeHTML(false)、换手拼 JSON、
// 或让含 % 的内容(ask 规则/插件名/模型名)流进 settings,Windows .cmd 链路
// 即断——本测试锁定这道不变量,破坏时必须显式重新评审。

func TestClaudeApprovalHookSettingsArgsFreeOfCmdMetachars(t *testing.T) {
	strategies := []claudeApprovalHookStrategy{
		claudeApprovalHookPreToolUseCommand,
		claudeApprovalHookPermissionRequestHTTP,
	}
	for _, strategy := range strategies {
		run := agentAIRun{sessionID: "sess-1", messageID: "msg-1", approvalToken: strings.Repeat("ab", 16)}
		settings, err := claudeApprovalHookSettings(strategy, run)
		if err != nil {
			t.Fatalf("strategy=%s 构建失败: %v", strategy, err)
		}
		// 必须走生产序列化出口(withClaudeApprovalHook 用的同一个函数):
		// 直接 json.Marshal 一遍再断言,未来生产改 SetEscapeHTML(false)
		// 测试也不会红——评审 2026-10-10 指出的护栏失效形态。
		raw, err := marshalClaudeApprovalHookSettings(settings)
		if err != nil {
			t.Fatalf("strategy=%s 序列化失败: %v", strategy, err)
		}
		for _, ch := range []byte{'&', '%', '|', '<', '>', '^'} {
			if bytes.IndexByte(raw, ch) >= 0 {
				t.Errorf("strategy=%s 的 --settings JSON 含字面 %q，Windows .cmd 链路会被 cmd.exe 解析: %s", strategy, string(ch), raw)
			}
		}
		// & 必须以 \u0026 形态出现(curl 策略的 URL 里有 2 个查询参数 &):
		// 锁定 escapeHTML 防线本身,防止未来被 SetEscapeHTML(false) 静默关掉
		if !bytes.Contains(raw, []byte(`\u0026`)) {
			t.Errorf("strategy=%s 的 --settings JSON 未观察到 & 的 HTML 转义(\\u0026)，escapeHTML 防线疑似被关闭: %s", strategy, raw)
		}
	}
}

// claudeApprovalHookCurlCommand 的命令词必须保持 "curl " 开头(darwin 等非
// Windows 平台零变化);Windows 的绝对路径解析行为由 TestClaudeHookCurlWordForOS 锁定。
func TestClaudeApprovalHookCurlCommandNonWindowsUnchanged(t *testing.T) {
	got := claudeApprovalHookCurlCommand("http://127.0.0.1:1/hook")
	if !strings.HasPrefix(got, "curl ") {
		t.Errorf("非 Windows 下命令词应保持裸 curl: %q", got)
	}
	if !strings.Contains(got, "-sS") || !strings.Contains(got, "--data-binary @-") {
		t.Errorf("curl 参数形态被改动: %q", got)
	}
}

// ---- claudeHookCurlWordForOS:Windows 解析 curl 绝对路径绕开 PowerShell 别名 ----

func TestClaudeHookCurlWordForOS(t *testing.T) {
	ok := func(p string) func(string) (string, error) {
		return func(string) (string, error) { return p, nil }
	}
	fail := func(string) (string, error) { return "", errors.New("not found") }
	cases := []struct {
		name string
		goos string
		look func(string) (string, error)
		want string
	}{
		{"windows System32 自带 curl → 绝对路径(正斜杠)", "windows", ok(`C:\Windows\System32\curl.exe`), "C:/Windows/System32/curl.exe"},
		{"windows 含空格路径(Git Bash)→ 保持裸名(bash/PS 引用语义无法两全)", "windows", ok(`C:\Program Files\Git\mingw64\bin\curl.exe`), "curl"},
		{"windows 找不到 curl → 保持裸名", "windows", fail, "curl"},
		{"windows 空结果 → 保持裸名", "windows", ok("  "), "curl"},
		{"非 Windows 零变化 → 恒裸名", "darwin", ok("/usr/bin/curl"), "curl"},
		{"linux 零变化 → 恒裸名", "linux", ok("/usr/bin/curl"), "curl"},
	}
	for _, c := range cases {
		if got := claudeHookCurlWordForOS(c.goos, c.look); got != c.want {
			t.Errorf("%s: claudeHookCurlWordForOS(%q) = %q，期望 %q", c.name, c.goos, got, c.want)
		}
	}
}

// 覆盖 permissions.ask 注入分支(非 full tier 才注入,评审指出原 fixture 从未
// 走到):ask 规则是工具匹配器,一旦出现 cmd.exe 元字符(如 %VAR%)必须在此
// 变红强制评审——% 没有 JSON 转义防线,只能在策略下发侧拒绝。
func TestClaudeApprovalHookSettingsPolicyAskBranchMetachars(t *testing.T) {
	run := agentAIRun{
		sessionID: "sess-1", messageID: "msg-1", approvalToken: strings.Repeat("ab", 16),
		claudePolicy: agentAIClaudeRemotePolicy{
			enabled:       true,
			trustTier:     "sanitized",
			permissionAsk: []string{"Bash(git push:*)", "Edit(.env:*)"},
		},
	}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, run)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	raw, err := marshalClaudeApprovalHookSettings(settings)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"permissions"`)) {
		t.Fatalf("期望 permissions.ask 分支被实际覆盖: %s", raw)
	}
	for _, ch := range []byte{'&', '%', '|', '<', '>', '^'} {
		if bytes.IndexByte(raw, ch) >= 0 {
			t.Errorf("ask 注入分支的 --settings JSON 含字面 %q: %s", string(ch), raw)
		}
	}
}
