package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// 换桩纪律:peerMCPHome 指向 t.TempDir(),严禁读写真实家目录;t.Cleanup 恢复;
// 同包换桩禁 t.Parallel。
func swapPeerMCPHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	orig := peerMCPHome
	peerMCPHome = func() string { return home }
	t.Cleanup(func() { peerMCPHome = orig })
	return home
}

func TestApplyPeerMCPClaudeRegistrationPreservesUserContent(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".claude.json")
	existing := `{"num":1700000000000,"mcpServers":{"shared":{"command":"user-cmd"}},"projects":{"p":{"history":true}}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPeerMCPClaudeRegistration(home, true); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 其他顶层键与既有 server 必须保留
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, ok := root["projects"]; !ok {
		t.Fatal("其他顶层键丢失")
	}
	if !strings.Contains(string(raw), `"shared"`) {
		t.Fatal("既有 server 条目丢失")
	}
	// UseNumber 保证数字字面量原样回写
	if !strings.Contains(string(raw), "1700000000000") {
		t.Fatal("数字字面量被改写")
	}
	// aliang-peer 条目形状
	var servers struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &servers); err != nil {
		t.Fatal(err)
	}
	entry := servers.MCPServers["aliang-peer"]
	if entry.Command == "" || len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Fatalf("aliang-peer 条目形状不符: %+v", entry)
	}
	// 幂等:再应用一次字节不变
	first, _ := os.ReadFile(path)
	if err := applyPeerMCPClaudeRegistration(home, true); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("重复注册不幂等")
	}
	// 注销:只删本键
	if err := applyPeerMCPClaudeRegistration(home, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "aliang-peer") {
		t.Fatal("注销后仍残留 aliang-peer")
	}
	if !strings.Contains(string(raw), `"shared"`) || !strings.Contains(string(raw), `"projects"`) {
		t.Fatal("注销误伤用户内容")
	}
}

func TestApplyPeerMCPCodexRegistrationPreservesUserContent(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "# my gateway\nmodel = \"gpt\"\n\n[model_providers.aliang]\nname = \"Aliang Gateway\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPeerMCPCodexRegistration(home, true); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, keep := range []string{"# my gateway", "model = \"gpt\"", "[model_providers.aliang]", "Aliang Gateway"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("用户内容丢失: %q", keep)
		}
	}
	if !strings.Contains(text, "[mcp_servers.aliang-peer]") {
		t.Fatal("托管块未写入")
	}
	if !strings.Contains(text, "command = \"") || !strings.Contains(text, `args = ["mcp"]`) {
		t.Fatalf("托管块形状不符:\n%s", text)
	}
	// 幂等
	first, _ := os.ReadFile(path)
	if err := applyPeerMCPCodexRegistration(home, true); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatalf("重复注册不幂等:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// 注销:只删托管块
	if err := applyPeerMCPCodexRegistration(home, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	text = string(raw)
	if strings.Contains(text, "aliang-peer") {
		t.Fatal("注销后仍残留托管块")
	}
	for _, keep := range []string{"# my gateway", "model = \"gpt\"", "[model_providers.aliang]"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("注销误伤用户内容: %q", keep)
		}
	}
}

func TestPeerMCPKernelStateTransitions(t *testing.T) {
	home := swapPeerMCPHome(t)
	// 初始:missing
	if st, err := peerMCPRegistrationStates(home); err != nil {
		t.Fatal(err)
	} else {
		for _, r := range st {
			if r.State != PeerMCPStateMissing || r.Error != "" {
				t.Fatalf("初始态应为 missing 无错: %+v", r)
			}
		}
	}
	if results := ApplyPeerMCPRegistration(home, true); len(results) != 2 {
		t.Fatalf("应返回两内核结果: %+v", results)
	} else {
		for _, r := range results {
			if r.State != PeerMCPStateRegistered || r.Error != "" {
				t.Fatalf("注册后应 registered: %+v", r)
			}
		}
	}
	// 已 registered 再 Apply=幂等跳过(不重写文件);注销后应 missing 无错
	for _, r := range ApplyPeerMCPRegistration(home, false) {
		if r.State != PeerMCPStateMissing || r.Error != "" {
			t.Fatalf("注销后应 missing 无错: %+v", r)
		}
	}
}

func TestPeerMCPStaleDetection(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// 手写的旧 command(模拟用户按文档手工注册过)
	if err := os.WriteFile(path, []byte("[mcp_servers.aliang-peer]\ncommand = \"/old/aliang\"\nargs = [\"mcp\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := ApplyPeerMCPRegistration(home, true)
	for _, r := range results {
		if r.Kernel == "codex" && (r.State != PeerMCPStateRegistered || r.Error != "") {
			t.Fatalf("stale 应被自愈为 registered: %+v", r)
		}
	}
}

func TestEnsurePeerMCPMatchesConfig(t *testing.T) {
	home := swapPeerMCPHome(t)
	off := false
	configSetGlobalForTest(t, &off)
	EnsurePeerMCPMatchesConfigForTest(home)
	// 关:两内核注册态应为 missing(没注册过,注销幂等)
	st, err := peerMCPRegistrationStates(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st {
		if r.State != PeerMCPStateMissing {
			t.Fatalf("关闭态自愈后应 missing: %+v", r)
		}
	}
	on := true
	configSetGlobalForTest(t, &on)
	EnsurePeerMCPMatchesConfigForTest(home)
	st, err = peerMCPRegistrationStates(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st {
		if r.State != PeerMCPStateRegistered {
			t.Fatalf("开启态自愈后应 registered: %+v", r)
		}
	}
}

// configSetGlobalForTest 与包内约定一致(agent_ai_agentcore_test.go 等):
// save-old/restore-old,不用 ResetGlobalConfigForTest 清零。
func configSetGlobalForTest(t *testing.T, enable *bool) {
	t.Helper()
	old := config.GetGlobalConfig()
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{PeerMCP: &config.PeerMCPConfig{Enable: enable}}})
	t.Cleanup(func() { config.SetGlobalConfig(old) })
}

func TestPeerMCPStaleDetectionClaude(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".claude.json")
	stale := `{"mcpServers":{"aliang-peer":{"command":"/old/aliang","args":["mcp"]}}}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := peerMCPKernelState(home, "claude"); err != nil {
		t.Fatal(err)
	} else if st != PeerMCPStateStale {
		t.Fatalf("应用前应为 stale: %s", st)
	}
	for _, r := range ApplyPeerMCPRegistration(home, true) {
		if r.Kernel == "claude" && (r.State != PeerMCPStateRegistered || r.Error != "") {
			t.Fatalf("claude stale 应被自愈为 registered: %+v", r)
		}
	}
}

func TestPeerMCPRegistrationSkipsWriteWhenAlreadyRegistered(t *testing.T) {
	home := swapPeerMCPHome(t)
	for _, r := range ApplyPeerMCPRegistration(home, true) {
		if r.State != PeerMCPStateRegistered || r.Error != "" {
			t.Fatalf("前置注册应成功: %+v", r)
		}
	}
	// 把两内核文件 mtime 拨到一小时前;再次 register 若真跳过写盘,
	// mtime 必须原样(静默重写会刷新 mtime,字节比对抓不住)。
	past := time.Now().Add(-time.Hour)
	files := []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex", "config.toml"),
	}
	for _, p := range files {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range ApplyPeerMCPRegistration(home, true) {
		if r.State != PeerMCPStateRegistered || r.Error != "" {
			t.Fatalf("已满足的内核应幂等跳过: %+v", r)
		}
	}
	for _, p := range files {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(past) {
			t.Fatalf("重写了已满足的内核文件: %s mtime=%v want=%v", p, info.ModTime(), past)
		}
	}
}

func TestPeerMCPCodexEquivalentHeaderRefusesBlindSplice(t *testing.T) {
	home := swapPeerMCPHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// 引号表头是 toml 的等价形态;文本拼接不认,盲追加会造重复表定义打碎文件
	quoted := "[mcp_servers.\"aliang-peer\"]\ncommand = \"/old/aliang\"\n"
	if err := os.WriteFile(path, []byte(quoted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPeerMCPCodexRegistration(home, true); err == nil {
		t.Fatal("等价表头形态注册应拒写")
	}
	if err := applyPeerMCPCodexRegistration(home, false); err == nil {
		t.Fatal("等价表头形态注销同样应拒写(防假幂等)")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != quoted {
		t.Fatalf("拒写时不得改写文件:\n%s", raw)
	}
}

func TestAgentCorePeerToolsGatedByPeerMCPEnabled(t *testing.T) {
	swapPeerMCPHome(t) // 防 agentHome 意外落真实家目录(本测试不写文件,防御性)
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)
	m := newAgentAIManager()
	run := agentAIRun{sessionID: "gate"}
	if len(m.agentCorePeerTools(run)) == 0 {
		t.Fatal("默认(未配置)应注入 peer 工具")
	}
	off := false
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{PeerMCP: &config.PeerMCPConfig{Enable: &off}}})
	if got := m.agentCorePeerTools(run); len(got) != 0 {
		t.Fatalf("显式关闭后不应注入 peer 工具, got %d", len(got))
	}
}
