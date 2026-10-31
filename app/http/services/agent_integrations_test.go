package services

// 「MCP 与技能」聚合的单元测试：全部用 t.TempDir 假家目录，绝不碰真机
// ~/.claude / ~/.codex（memory 教训：fixture 写真机家目录会污染开发者
// 仪表盘与真实 CLI 配置）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIntegrationsHome 在假家目录里铺一份最小可用的配置：
//   - ~/.claude.json: 两个 MCP server —— stdio（内核可注入）与 http+headers（v1 跳过）
//   - ~/.codex/config.toml: 一个 codex stdio server（不进内核，恒 not_collected）
//   - ~/.claude/skills/demo/SKILL.md: 一条 user 域 skill
func writeIntegrationsHome(t *testing.T, home string) {
	t.Helper()
	// 注意 URL query 的脱敏阈值是「值长 > 24」才打码（mcpURLQueryRedactMinLen，
	// 短值视为非密钥）——这里故意用一个 40 字符的 token 验证真实契约。
	claudeJSON := `{
  "mcpServers": {
    "shell": {"command": "npx", "args": ["-y", "shell-mcp"], "env": {"SECRET_TOKEN": "super-secret-value"}},
    "remote": {"type": "http", "url": "https://api.example.com/mcp?token=leaked-token-0123456789abcdefghij", "headers": {"Authorization": "Bearer leaked-bearer"}}
  }
}`
	writeIntegrationsFile(t, filepath.Join(home, ".claude.json"), claudeJSON)

	codexToml := "[mcp_servers.docs]\ncommand = \"docs-mcp\"\nargs = [\"--serve\"]\n"
	writeIntegrationsFile(t, filepath.Join(home, ".codex", "config.toml"), codexToml)

	skillMd := "---\nname: demo-skill\ndescription: 演示技能\n---\n\n正文\n"
	writeIntegrationsFile(t, filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"), skillMd)
}

func writeIntegrationsFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCollectAgentIntegrationsMergesAndMarksKernelState(t *testing.T) {
	home := t.TempDir()
	writeIntegrationsHome(t, home)

	report := collectAgentIntegrations(home)

	byName := map[string]AgentIntegrationMCPServer{}
	for _, srv := range report.MCPServers {
		byName[srv.Name] = srv
	}

	// 1) stdio server：内核采集得到 → injected
	shell, ok := byName["shell"]
	if !ok {
		t.Fatalf("缺少 shell 条目: %+v", report.MCPServers)
	}
	if shell.KernelState != agentIntegrationKernelInjected {
		t.Fatalf("shell 应为 injected，实际 %q", shell.KernelState)
	}
	if shell.Transport != "stdio" {
		t.Fatalf("shell transport 应为 stdio，实际 %q", shell.Transport)
	}
	if shell.ToolPrefix != "mcp__shell__" {
		t.Fatalf("tool_prefix 错误: %q", shell.ToolPrefix)
	}

	// 2) http + 自定义 headers：内核采集到但 v1 跳过 → skipped_headers
	remote, ok := byName["remote"]
	if !ok {
		t.Fatalf("缺少 remote 条目: %+v", report.MCPServers)
	}
	if remote.KernelState != agentIntegrationKernelSkippedHeaders {
		t.Fatalf("remote 应为 skipped_headers，实际 %q", remote.KernelState)
	}

	// 3) codex 配置不进内核 → not_collected
	docs, ok := byName["docs"]
	if !ok {
		t.Fatalf("缺少 docs 条目: %+v", report.MCPServers)
	}
	if docs.KernelState != agentIntegrationKernelNotCollected {
		t.Fatalf("docs 应为 not_collected，实际 %q", docs.KernelState)
	}
	if docs.Source == "" || !strings.Contains(docs.Source, "config") {
		t.Fatalf("docs 来源应含 config，实际 %q", docs.Source)
	}

	// 注入态排前面（排序契约）
	if report.MCPServers[0].KernelState != agentIntegrationKernelInjected {
		t.Fatalf("首条应为 injected，实际 %q", report.MCPServers[0].KernelState)
	}

	// Notes 非空（边界说明必须给用户看到）
	if len(report.Notes) == 0 {
		t.Fatal("Notes 不应为空")
	}
}

func TestCollectAgentIntegrationsRedactsSecrets(t *testing.T) {
	home := t.TempDir()
	writeIntegrationsHome(t, home)

	report := collectAgentIntegrations(home)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := string(raw)
	// env 值出不去、headers 值出不去、URL query 里的 token 被打码。
	for _, leaked := range []string{"super-secret-value", "leaked-bearer"} {
		if strings.Contains(payload, leaked) {
			t.Fatalf("报告泄漏了敏感值 %q: %s", leaked, payload)
		}
	}
	if strings.Contains(payload, "leaked-token-0123456789abcdefghij") {
		t.Fatalf("URL query 长 token 未打码: %s", payload)
	}
}

func TestCollectAgentIntegrationsSkills(t *testing.T) {
	home := t.TempDir()
	writeIntegrationsHome(t, home)

	report := collectAgentIntegrations(home)
	if len(report.Skills) != 1 {
		t.Fatalf("应扫到 1 条 user 域 skill，实际 %d: %+v", len(report.Skills), report.Skills)
	}
	skill := report.Skills[0]
	if skill.Name != "demo-skill" {
		t.Fatalf("skill 名应取自 frontmatter，实际 %q", skill.Name)
	}
	if skill.Scope != "user" {
		t.Fatalf("scope 应为 user，实际 %q", skill.Scope)
	}
	// 2026-10-31：内核已支持技能注入（agent_ai_agentcore_skills.go），
	// kernel_supported 改为反映全局开关。默认开启 ⇒ 应为 true；关掉总闸时
	// 应为 false。这里用默认配置断言「开着」这一态。
	if !skill.KernelSupported {
		t.Fatal("默认（skills_enabled 未配置）时 kernel_supported 应为 true——内核已支持技能注入")
	}
}

func TestCollectAgentIntegrationsEmptyHome(t *testing.T) {
	// 空家目录：不 panic，两个列表都空，Notes 仍给（用户需要知道为什么空）。
	report := collectAgentIntegrations(t.TempDir())
	if len(report.MCPServers) != 0 || len(report.Skills) != 0 {
		t.Fatalf("空家目录应无条目: %+v", report)
	}
	if len(report.Notes) == 0 {
		t.Fatal("空家目录下 Notes 仍应说明边界")
	}

	// home 为空串：直接返回空报告，不 panic。
	if empty := collectAgentIntegrations(""); len(empty.MCPServers) != 0 || len(empty.Skills) != 0 {
		t.Fatalf("空 home 应返回空报告: %+v", empty)
	}
}
