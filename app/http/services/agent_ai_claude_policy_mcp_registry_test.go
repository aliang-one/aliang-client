package services

// claudeTierMCPArgs 快速路径与注册表合并的单测(home 经 claudeTierMCPHome
// 注入;swap 期间禁并行,defer 还原)。
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func swapClaudeTierHome(t *testing.T, home string) {
	t.Helper()
	old := claudeTierMCPHome
	claudeTierMCPHome = func() string { return home }
	t.Cleanup(func() { claudeTierMCPHome = old })
}

func claudeTierPayloadServerNames(t *testing.T, flags []string, cleanup func()) map[string]map[string]interface{} {
	t.Helper()
	defer cleanup()
	if len(flags) == 0 {
		return nil
	}
	raw, err := os.ReadFile(flags[len(flags)-1])
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MCPServers map[string]map[string]interface{} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed.MCPServers
}

func TestClaudeTierMCPArgsUntrustedStillEmitsRegistry(t *testing.T) {
	home := t.TempDir()
	swapClaudeTierHome(t, home)
	// 项目不可信 + 仅注册表有条目:必须发 flags(claude 原生发现看不见注册表)
	policy := agentAIClaudeRemotePolicy{projectMCPTrusted: false}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"user-srv":{"command":"u"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	_ = os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{"proj-srv":{"command":"p"}}}`), 0o600)
	_ = upsertAliangMCPServerForTest(home, "reg-srv", aliangMCPServerEntry{Command: "r"})
	flags, cleanup, err := claudeTierMCPArgs(policy, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) == 0 {
		t.Fatal("不可信项目 + 注册表非空必须仍发 flags")
	}
	servers := claudeTierPayloadServerNames(t, flags, cleanup)
	if _, ok := servers["reg-srv"]; !ok {
		t.Fatal("payload 必须含注册表条目")
	}
	if _, ok := servers["user-srv"]; !ok {
		t.Fatal("payload 必须重含 user 域条目(strict 模式原生会抑制插件/user 需显式)")
	}
	if _, ok := servers["proj-srv"]; ok {
		t.Fatal("不可信项目时 payload 绝不含项目条目")
	}
}

func TestClaudeTierMCPArgsUntrustedNoRegistryStaysNil(t *testing.T) {
	home := t.TempDir()
	swapClaudeTierHome(t, home)
	policy := agentAIClaudeRemotePolicy{projectMCPTrusted: false}
	flags, cleanup, err := claudeTierMCPArgs(policy, t.TempDir())
	if err != nil || len(flags) != 0 {
		t.Fatalf("不可信且无注册表必须走原生发现(现状行为), flags=%v err=%v", flags, err)
	}
	cleanup()
}

func TestClaudeTierMCPArgsRegistryOverridesLegacy(t *testing.T) {
	home := t.TempDir()
	swapClaudeTierHome(t, home)
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"dup":{"command":"legacy"}}}`), 0o600)
	_ = upsertAliangMCPServerForTest(home, "dup", aliangMCPServerEntry{Command: "registry"})
	policy := agentAIClaudeRemotePolicy{projectMCPTrusted: true}
	flags, cleanup, err := claudeTierMCPArgs(policy, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	servers := claudeTierPayloadServerNames(t, flags, cleanup)
	if servers["dup"]["command"] != "registry" {
		t.Fatalf("注册表必须覆盖遗留同名键, got %v", servers["dup"])
	}
}

func TestClaudeTierMCPArgsTrustedAllEmptyStaysNil(t *testing.T) {
	home := t.TempDir()
	swapClaudeTierHome(t, home)
	policy := agentAIClaudeRemotePolicy{projectMCPTrusted: true}
	flags, cleanup, err := claudeTierMCPArgs(policy, t.TempDir())
	if err != nil || len(flags) != 0 {
		t.Fatalf("项目与注册表双双零贡献必须返 nil, flags=%v err=%v", flags, err)
	}
	cleanup()
}

func TestClaudeTierMCPArgsProjectWinsOverRegistry(t *testing.T) {
	home := t.TempDir()
	swapClaudeTierHome(t, home)
	// 全优先级链锁定:同名 dup 三源齐备,可信项目条目最后合并必须胜出
	// (user < plugin < registry < project 的合并序,防将来重构静默翻转)。
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"dup":{"command":"legacy"}}}`), 0o600)
	_ = upsertAliangMCPServerForTest(home, "dup", aliangMCPServerEntry{Command: "registry"})
	proj := t.TempDir()
	_ = os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{"dup":{"command":"project"}}}`), 0o600)
	policy := agentAIClaudeRemotePolicy{projectMCPTrusted: true}
	flags, cleanup, err := claudeTierMCPArgs(policy, proj)
	if err != nil {
		t.Fatal(err)
	}
	servers := claudeTierPayloadServerNames(t, flags, cleanup)
	if servers["dup"]["command"] != "project" {
		t.Fatalf("可信项目同名条目必须胜过注册表, got %v", servers["dup"])
	}
}
