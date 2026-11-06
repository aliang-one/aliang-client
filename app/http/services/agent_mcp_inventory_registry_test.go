package services

// 统一注册表进入清单的采集语义:claude 侧打 source=aliang + stamp;
// codex 侧过滤 aliang-mcp-* 托管块。涉缓存 swap 面,禁 t.Parallel。
import (
	"strings"
	"testing"
)

func TestClaudeInventoryLabelsRegistryAliang(t *testing.T) {
	home := t.TempDir()
	_ = upsertAliangMCPServerForTest(home, "blender", aliangMCPServerEntry{
		Type: "stdio", Command: "/usr/bin/uvx", Args: []string{"blender-mcp"},
	})
	out, stamps := claudeMCPServerInventoryIn(home)
	var found bool
	for _, s := range out {
		if s.Name == "blender" {
			found = true
			if s.Source != "aliang" {
				t.Fatalf("source = %s, want aliang", s.Source)
			}
			if s.Transport != "stdio" || s.Command == "" {
				t.Fatalf("摘要形态不对: %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("注册表条目必须出现在 claude 侧清单: %+v", out)
	}
	regStamp := stampAgentMCPFile(aliangMCPRegistryPath(home))
	hit := false
	for _, st := range stamps {
		if st == regStamp {
			hit = true
		}
	}
	if !hit {
		t.Fatal("注册表文件 stamp 必须并入缓存指纹(否则 PUT/DELETE 后清单不刷新)")
	}
}

func TestClaudeInventoryRegistryAloneNotEmptyCached(t *testing.T) {
	home := t.TempDir() // 无 claude.json 无插件,仅注册表
	_ = upsertAliangMCPServerForTest(home, "solo", aliangMCPServerEntry{Command: "x"})
	out, stamps := claudeMCPServerInventoryIn(home)
	if len(out) != 1 {
		t.Fatalf("没装 Claude 的机器注册表条目必须可见(自洽), out=%+v", out)
	}
	if stamps == nil {
		t.Fatal("stamps 两态契约恒非 nil")
	}
}

func TestCodexInventoryFiltersManagedBlocks(t *testing.T) {
	home := t.TempDir()
	writeCodexConfig(t, home, "[mcp_servers.aliang-mcp-blender]\ncommand = \"x\"\n\n[mcp_servers.mine]\ncommand = \"y\"\n")
	out, _ := codexMCPServerInventoryIn(home)
	for _, s := range out {
		if strings.HasPrefix(s.Name, aliangMCPCodexPrefix) {
			t.Fatalf("托管块不得进清单(注册表行已代表): %+v", out)
		}
	}
	if len(out) != 1 || out[0].Name != "mine" {
		t.Fatalf("用户自有 codex 条目保持不变: %+v", out)
	}
}

// 注册表对等钉住:env/headers 值不得泄漏进脱敏摘要。models.AgentMCPServer
// 结构上无 env/headers 字段(结构保证),本测试把该保证钉成回归防线——
// 若有人给摘要结构加字段或改转换器直传原始 map,这里先红。
func TestClaudeInventoryRegistrySecretsNotLeaked(t *testing.T) {
	home := t.TempDir()
	_ = upsertAliangMCPServerForTest(home, "secret-http", aliangMCPServerEntry{
		Type:    "http",
		URL:     "https://api.example.com/mcp",
		Env:     map[string]string{"API_KEY": "secret123"},
		Headers: map[string]string{"Authorization": "Bearer sk-xxx"},
	})
	out, _ := claudeMCPServerInventoryIn(home)
	var found bool
	for _, s := range out {
		if s.Name != "secret-http" {
			continue
		}
		found = true
		// 带 headers 的 http 条目仍可见:url 在、headers 不在
		if s.Transport != "http" || s.URL == "" {
			t.Fatalf("带 headers 的 http 条目须仍可见(url 在、headers 不在): %+v", s)
		}
		for _, field := range []string{s.Command, s.URL} {
			for _, secret := range []string{"secret123", "sk-xxx", "Bearer"} {
				if strings.Contains(field, secret) {
					t.Fatalf("敏感值 %q 泄漏进摘要: %+v", secret, s)
				}
			}
		}
	}
	if !found {
		t.Fatalf("注册表 http 条目必须出现在清单: %+v", out)
	}
}
