# 统一 MCP 注册表(along MCP Registry)实施计划

> **执行状态:已完成**(2026-11-06,Task 0-11 全勾;分支 feat/aliang-mcp-registry 14 commits 未推送;真机冒烟六项待部署后执行,PhoneServer source=aliang 核验为部署清单项)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 建 alianggate 第一方 MCP 注册表 `~/.aliang/mcp.json`,UI/API 增删改查启停,三个内核(agentcore 直读 / claudecode 宿主合并注入 / codex config.toml 托管块)一套配置生效;遗留源 `~/.claude.json` 继续读、注册表同名键胜出。

**Architecture:** 新文件 `agent_mcp_registry.go` 承载注册表文件层(读/写/校验/合并原语)与 codex 托管块同步;三个消费点各加一条合并源;清单采集器加 `aliang` 来源与 `aliang-mcp-*` 过滤;HTTP 三个端点走既有 `proxyIfNeeded` 写透传模式;前端在设置页 Agent → user-agent 子 tab 挂管理卡片。

**Tech Stack:** Go(stdlib net/http + BurntSushi/toml 已有依赖)、Vue 3 SFC + vitest、无新增第三方依赖。

**Spec:** `docs/superpowers/specs/2026-11-05-aliang-mcp-registry-design.md`(已过两轮评审,行号锚点均已核实)

---

## 全局约定与已知的坑(每个任务执行前先读这段)

1. **worktree 工作流**(memory `worktree-development-workflow`):所有开发在 worktree 切分支,不直接改 master 工作区。Task 0 负责建。
2. **worktree 首次构建**(memory `transfer-worktree-build-gotchas`):新 worktree 首次 `go build` 前必须从主检出 `cp -R app/website/dist app/website/`(embed 占位);gopls 对 worktree 的 undefined 误报一律无视,以 worktree 内 `go build`/`go test` 为准。
3. **提交纪律**(project CLAUDE.md + memory):全中文提交标题「新增：/修复：/测试：」,HEREDOC 多行,尾部 `🤖 Generated with [Claude Code](https://claude.com/claude-code)` 与 `Co-Authored-By: Claude Code <noreply@anthropic.com>`。每个任务一步提交,先跑该任务测试再提交。
4. **测试基线**(memory):`services` 包两枚预存 race flake(TestStartProxyWhenSessionReady / TestHandleAgentAuthRejectedNotificationRules)与 processor/auth TestSessionAuthorityProxyAdmissionLinearizesWithDemotion 是基线,复跑洗白禁止,勿当回归;跨平台测试名避开 GOOS 尾段;涉 `mcpInventoryFor` swap 的包内测试禁 `t.Parallel`(本计划不 swap 该注入点,但保持单测串行风格)。
5. **安全红线**(spec §9):env/headers 值不进日志;codex 非规范表头拒写;注册表键禁 `aliang-` 前缀;写 API 原子写。
6. **代码风格**:注释密度与周边对齐(中文注释、讲 why);`gofmt` 过;新 Go 文件头一段中文注释讲清本文件职责。

## 文件结构总览

```
新增  app/http/services/agent_mcp_registry.go            注册表文件层:类型/读/写/校验/合并原语/codex 托管块同步/自愈
新增  app/http/services/agent_mcp_registry_test.go       上述全部单测
改    app/http/services/agent_ai_agentcore_mcp.go        agentCoreMCPRawServersIn 加注册表源(约 :100-102 后)
改    app/http/services/agent_ai_claude_policy.go        claudeTierMCPArgs 合并序+两条快速路径(:183-217)
改    app/http/services/agent_ai_claude_policy.go        新增 home 注入点 claudeTierMCPHome + readPluginMCPServersIn
改    app/http/services/agent_mcp_inventory.go           claude 采集器加 aliang 源+stamp;codex 采集器过滤 aliang-mcp-*
改    app/http/services/agent_integrations.go            Notes 补一句注册表说明(可选)
新增  app/http/handlers/agent_mcp_registry_handler.go    GET/PUT/DELETE 三 handler(挂 AgentHandler)
改    app/http/handlers/agent_handler.go                 (若 handler 挂 AgentHandler 则只加方法)
改    app/http/routes/routes.go                          注册 3 条路由
改    app/agentruntime/server.go                         启动自愈挂钩(EnsurePeerMCPMatchesConfig 旁,:90 附近)
新增  app/website/src/components/settings/AgentMCPRegistrySettings.vue       管理卡片
新增  app/website/src/components/settings/AgentMCPRegistrySettings.test.js   vitest
改    app/website/src/components/settings/agent/AgentPage.vue                user-agent 分支挂载(:29-32)
改    app/website/src/services/agentApi.js               3 个 API client 函数
改    app/website/src/i18n/zh.js / en.js                 i18n 键
测试  go:单测随各任务;前端 vitest 随 Task 10;e2e 冒烟 Task 11
```

---

### Task 0: worktree 准备

**Files:** 无代码改动(环境准备)

- [x] **Step 1: 建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .worktrees/feat-aliang-mcp-registry -b feat/aliang-mcp-registry master
cd .worktrees/feat-aliang-mcp-registry
```

后续所有任务的工作目录都是这个 worktree(下文 `$WT`)。

- [x] **Step 2: embed 占位与首次构建**

```bash
cp -R /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/dist app/website/dist   # 若主检出存在
go build ./...
```

Expected: 构建通过(dist 缺失导致的 embed 报错=Step 2 第一条没做)。

---

### Task 1: 注册表文件层——类型/读取/合并原语

**Files:**
- Create: `app/http/services/agent_mcp_registry.go`
- Test: `app/http/services/agent_mcp_registry_test.go`

- [x] **Step 1: 写失败测试**

```go
package services

// agent_mcp_registry_test.go 统一 MCP 注册表(~/.aliang/mcp.json)文件层单测。
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistryFile(t *testing.T, home, content string) {
	t.Helper()
	path := filepath.Join(home, ".aliang", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadAliangMCPRegistryIntoMergesEnabledEntries(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{
	  "version": 1,
	  "servers": {
	    "blender": {"type":"stdio","command":"/usr/bin/uvx","args":["blender-mcp"],"enabled":true},
	    "off":     {"type":"stdio","command":"x","enabled":false},
	    "weather": {"type":"http","url":"https://api.example.com/mcp"},
	    "bad":     {"type":"stdio"},
	    "future":  {"type":"weird","command":"x"}
	  }
	}`)
	dst := map[string]interface{}{"legacy": map[string]interface{}{"command": "keep"}}
	got := readAliangMCPRegistryInto(home, dst)
	if got != 2 {
		t.Fatalf("merged count = %d, want 2 (blender+weather)", got)
	}
	if _, ok := dst["off"]; ok {
		t.Fatal("enabled=false 条目不得并入")
	}
	if _, ok := dst["bad"]; ok {
		t.Fatal("缺 command 的 stdio 条目不得并入")
	}
	if _, ok := dst["future"]; ok {
		t.Fatal("未知 transport 条目不得并入")
	}
	b, ok := dst["blender"].(map[string]interface{})
	if !ok || b["command"] != "/usr/bin/uvx" {
		t.Fatalf("blender 条目形态不对: %#v", dst["blender"])
	}
	if _, has := b["enabled"]; has {
		t.Fatal("并入 claude 形态时必须剥掉注册表专属 enabled 键")
	}
	if _, ok := dst["legacy"]; !ok {
		t.Fatal("dst 既有键不得被破坏")
	}
}

func TestReadAliangMCPRegistryIntoRegistryWinsOverLegacy(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{"version":1,"servers":{"dup":{"command":"from-registry"}}}`)
	dst := map[string]interface{}{"dup": map[string]interface{}{"command": "from-claude"}}
	if got := readAliangMCPRegistryInto(home, dst); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	if m := dst["dup"].(map[string]interface{}); m["command"] != "from-registry" {
		t.Fatalf("注册表同名键必须覆盖遗留源, got %#v", dst["dup"])
	}
}

func TestReadAliangMCPRegistryIntoToleratesMissingAndBroken(t *testing.T) {
	home := t.TempDir() // 无文件
	dst := map[string]interface{}{}
	if got := readAliangMCPRegistryInto(home, dst); got != 0 || len(dst) != 0 {
		t.Fatalf("缺文件必须零并入, got %d %v", got, dst)
	}
	writeRegistryFile(t, home, `not json`)
	if got := readAliangMCPRegistryInto(home, dst); got != 0 {
		t.Fatalf("坏文件必须零并入(容忍不降级), got %d", got)
	}
	// version 前向兼容:未知更高版本照读,不拒读不迁移
	writeRegistryFile(t, home, `{"version":9,"servers":{"a":{"command":"x"}}}`)
	if got := readAliangMCPRegistryInto(home, map[string]interface{}{}); got != 1 {
		t.Fatalf("高 version 必须前向兼容读取, got %d", got)
	}
}

func TestAliangMCPRegistryPathUnderHome(t *testing.T) {
	p := aliangMCPRegistryPath("/h")
	if !strings.HasSuffix(filepath.ToSlash(p), "/h/.aliang/mcp.json") {
		t.Fatalf("path = %s", p)
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run 'TestReadAliangMCPRegistry|TestAliangMCPRegistryPath' -v 2>&1 | tail -5`
Expected: FAIL(undefined: readAliangMCPRegistryInto)

- [x] **Step 3: 实现(新文件)**

```go
package services

// agent_mcp_registry.go 统一 MCP 注册表(alianggate 第一方 MCP 资产)。
//
// 文件:<agentHome>/.aliang/mcp.json。三内核消费同一合并视图:
//   agentcore   直读(agentCoreMCPRawServersIn 加第三源,本文件同名键覆盖);
//   claudecode  claudeTierMCPArgs 合并注入(注册表不受 projectMCPTrusted 门控);
//   codex       applyAliangMCPCodexSync 维护 [mcp_servers.aliang-mcp-<name>] 托管块。
// 遗留源(~/.claude.json 顶层 + 插件 .mcp.json)继续生效,合并序:
//   claude.json → 插件 → 注册表(同名后者胜)→ 项目 .mcp.json(仅 claude 侧,信任闸不变)。
//
// 写盘纪律与 peer 注册同款:全量往返保留未知键(json.Decoder UseNumber)、
// SetEscapeHTML(false)、tmp+rename 原子写(writePeerMCPFileAtomic)、内容未变幂等跳写。
// 安全:env/headers 值只进 transport 构造,永不进日志;名称禁 aliang- 前缀(平台保留)。
//
// spec: docs/superpowers/specs/2026-11-05-aliang-mcp-registry-design.md

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const (
	// aliangMCPServerNameMax 对齐 sanitizeAgentCoreMCPSegment 的段宽口径(64)。
	aliangMCPServerNameMax = 64
	// aliangMCPServerMaxCount 对齐 agentCoreMCPServerCap(写通道防呆上限)。
	aliangMCPServerMaxCount = 24
	// aliangMCPReservedPrefix 平台保留前缀:peer 托管名 aliang-peer 与本文件
	// codex 托管名 aliang-mcp-* 都落在该族下,用户条目禁用避免双向踩踏。
	aliangMCPReservedPrefix = "aliang-"
	// aliangMCPCodexPrefix codex config.toml 托管块表头前缀。
	aliangMCPCodexPrefix = "aliang-mcp-"
	aliangMCPRegistryVersion = 1
)

var aliangMCPServerNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// aliangMCPServerEntry 一条注册表条目(写通道强类型形态)。
type aliangMCPServerEntry struct {
	Type    string            `json:"type,omitempty"`    // ""|stdio|http|sse
	Command string            `json:"command,omitempty"` // stdio 必填
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"` // http/sse 必填
	Headers map[string]string `json:"headers,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"` // nil=启用
}

func (e aliangMCPServerEntry) enabled() bool { return e.Enabled == nil || *e.Enabled }

// aliangMCPRegistryPath 注册表落盘路径。
func aliangMCPRegistryPath(home string) string {
	return filepath.Join(home, ".aliang", "mcp.json")
}

// loadAliangMCPRegistryRoot 读注册表整文件为通用 map(UseNumber 保数字字面量,
// 未知顶层键/未知 server 字段全量保留供往返)。文件缺失返回 existed=false。
func loadAliangMCPRegistryRoot(home string) (root map[string]interface{}, existed bool, err error) {
	path := aliangMCPRegistryPath(home)
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, true, nil
}

// aliangMCPEntryUsable 读通道形态判定:type 归一(stdio 缺省),stdio 需非空
// command,http/sse 需非空 url;其余形态 fail-closed 跳过。返回归一 type。
func aliangMCPEntryUsable(name string, raw map[string]interface{}) (string, bool) {
	typ := "stdio"
	if v, ok := raw["type"].(string); ok && strings.TrimSpace(v) != "" {
		typ = strings.ToLower(strings.TrimSpace(v))
	}
	switch typ {
	case "stdio":
		if cmd, _ := raw["command"].(string); strings.TrimSpace(cmd) != "" {
			return typ, true
		}
	case "http", "sse":
		if u, _ := raw["url"].(string); strings.TrimSpace(u) != "" {
			return typ, true
		}
	}
	return typ, false
}

// readAliangMCPRegistryInto 把注册表启用条目并入 claude 形态 dst(同名键覆盖
// 遗留源),返回并入条目数。语义:
//   - enabled=false / 畸形 / 未知 transport 条目跳过(Warn);
//   - 并入时剥掉注册表专属 enabled 键(claude 形态无此字段,语义已由跳过表达);
//   - version 非 1 仅 Warn(前向兼容:不拒读、不迁移、不改写);
//   - 文件缺失/坏文件零并入(容忍不降级,对齐 readMCPServersFile 姿态)。
// 消费方:agentCoreMCPRawServersIn / claudeTierMCPArgs / MCP 清单采集。
func readAliangMCPRegistryInto(home string, dst map[string]interface{}) int {
	if home == "" || dst == nil {
		return 0
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		logger.Warn(fmt.Sprintf("aliang-mcp: 注册表不可读,跳过该源: %v", err))
		return 0
	}
	if !existed {
		return 0
	}
	if v, has := root["version"]; has {
		if n, ok := v.(json.Number); ok && n.String() != fmt.Sprint(aliangMCPRegistryVersion) {
			logger.Warn(fmt.Sprintf("aliang-mcp: 注册表 version=%s 高于本程序,按前向兼容读取", n.String()))
		}
	}
	servers, _ := root["servers"].(map[string]interface{})
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	count := 0
	for _, name := range names {
		raw, ok := servers[name].(map[string]interface{})
		if !ok {
			continue
		}
		enabled := true
		if ev, has := raw["enabled"]; has {
			if b, ok := ev.(bool); ok {
				enabled = b
			}
		}
		if !enabled {
			continue
		}
		typ, usable := aliangMCPEntryUsable(name, raw)
		if !usable {
			logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 形态不合法(type=%s 缺 command/url),跳过", name, typ))
			continue
		}
		entry := make(map[string]interface{}, len(raw))
		for k, v := range raw {
			entry[k] = v
		}
		delete(entry, "enabled")
		dst[name] = entry
		count++
	}
	return count
}
```

- [x] **Step 4: 跑测试确认通过**

Run: `cd $WT && go test ./app/http/services/ -run 'TestReadAliangMCPRegistry|TestAliangMCPRegistryPath' -v 2>&1 | tail -8`
Expected: 全 PASS

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/http/services/agent_mcp_registry.go app/http/services/agent_mcp_registry_test.go
git commit -m "$(cat <<'EOF'
新增:统一 MCP 注册表文件层——读取与合并原语(readAliangMCPRegistryInto)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 注册表文件层——校验/写入/删除(幂等原子写)

**Files:**
- Modify: `app/http/services/agent_mcp_registry.go`(追加)
- Test: `app/http/services/agent_mcp_registry_test.go`(追加)

- [x] **Step 1: 写失败测试**

```go
func TestUpsertAliangMCPServerValidates(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name   string
		server string
		entry  aliangMCPServerEntry
	}{
		{"bad-name", "bad name!", aliangMCPServerEntry{Command: "x"}},
		{"reserved", "aliang-evil", aliangMCPServerEntry{Command: "x"}},
		{"stdio-no-command", "a", aliangMCPServerEntry{}},
		{"http-no-url", "a", aliangMCPServerEntry{Type: "http"}},
		{"bad-type", "a", aliangMCPServerEntry{Type: "weird", Command: "x"}},
	}
	for _, c := range cases {
		if err := upsertAliangMCPServerForTest(home, c.server, c.entry); err == nil {
			t.Fatalf("%s 必须被拒绝", c.name)
		}
	}
}

func TestUpsertDeleteRoundTripPreservesUnknowns(t *testing.T) {
	home := t.TempDir()
	writeRegistryFile(t, home, `{"version":1,"custom_top":"keepme","servers":{
	  "old":{"command":"keep-old","future_field":{"n":42}}}}`)
	disabled := false
	if err := upsertAliangMCPServerForTest(home, "blender", aliangMCPServerEntry{
		Type: "stdio", Command: "/usr/bin/uvx", Args: []string{"blender-mcp"},
		Env: map[string]string{"K": "v"}, Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		t.Fatalf("load: existed=%v err=%v", existed, err)
	}
	if root["custom_top"] != "keepme" {
		t.Fatalf("未知顶层键必须保留, got %#v", root["custom_top"])
	}
	servers := root["servers"].(map[string]interface{})
	old := servers["old"].(map[string]interface{})
	if old["future_field"] == nil {
		t.Fatal("未知 server 字段必须保留")
	}
	// 数字字面量:UseNumber 往返后 42 仍是 42(json.Number)
	if ff, ok := old["future_field"].(map[string]interface{}); !ok || ff["n"] != json.Number("42") {
		t.Fatalf("数字字面量必须保真, got %#v", old["future_field"])
	}
	if servers["blender"] == nil {
		t.Fatal("blender 未写入")
	}
	// enabled=false 显式落盘
	if b, _ := servers["blender"].(map[string]interface{})["enabled"].(bool); b {
		t.Fatal("显式 false 必须落盘")
	}
	// 删除
	existed, err = deleteAliangMCPServerForTest(home, "blender")
	if err != nil || !existed {
		t.Fatalf("delete: existed=%v err=%v", existed, err)
	}
	if existed, _ = func() (bool, error) { return deleteAliangMCPServerForTest(home, "blender") }(); existed {
		t.Fatal("二次删除必须报不存在")
	}
}

func TestUpsertIdempotentNoRewrite(t *testing.T) {
	home := t.TempDir()
	e := aliangMCPServerEntry{Command: "x"}
	if err := upsertAliangMCPServerForTest(home, "a", e); err != nil {
		t.Fatal(err)
	}
	p := aliangMCPRegistryPath(home)
	st1 := stampAgentMCPFile(p)
	if err := upsertAliangMCPServerForTest(home, "a", e); err != nil {
		t.Fatal(err)
	}
	if stampAgentMCPFile(p) != st1 {
		t.Fatal("同值重写必须幂等跳写(mtime 不变)")
	}
}

func TestUpsertCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	if err := upsertAliangMCPServerForTest(home, "a", aliangMCPServerEntry{Command: "x"}); err != nil {
		t.Fatal(err)
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		t.Fatalf("首次写必须建文件: existed=%v err=%v", existed, err)
	}
	if v, _ := root["version"].(json.Number); v == nil || v.String() != "1" {
		t.Fatalf("首建文件必须带 version=1, got %#v", root["version"])
	}
}

func TestUpsertCapExceededRejected(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < aliangMCPServerMaxCount; i++ {
		name := "sa" + fmt.Sprint(i) // 合法名 sa0..sa23
		if err := upsertAliangMCPServerForTest(home, name, aliangMCPServerEntry{Command: "x"}); err != nil {
			t.Fatalf("第 %d 条不应拒绝: %v", i, err)
		}
	}
	if err := upsertAliangMCPServerForTest(home, "overflow", aliangMCPServerEntry{Command: "x"}); err == nil {
		t.Fatal("第 25 条必须被拒(cap=24)")
	}
	// 已有条目的更新不受 cap 影响(替换语义)
	if err := upsertAliangMCPServerForTest(home, "s0", aliangMCPServerEntry{Command: "y"}); err != nil {
		t.Fatalf("替换既有条目不应受 cap 拦: %v", err)
	}
}

func TestRegistryRoundTripKeepsAngleBytes(t *testing.T) {
	// spec §10:SetEscapeHTML(false)——用户内容里的 <>& 不得被改写成 < 等
	home := t.TempDir()
	if err := upsertAliangMCPServerForTest(home, "esc", aliangMCPServerEntry{
		Command: `echo "<b>&</b>"`,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(aliangMCPRegistryPath(home))
	if err != nil {
		t.Fatal(err)
	}
	// 判别式用裸形态:SetEscapeHTML 开启时会产出 <b>&;引号本身
	// 会被 JSON 转义成 \" 属正常行为,不作为判别对象(评审第 3 轮实证)。
	if !strings.Contains(string(raw), `<b>&</b>`) {
		t.Fatalf("转义语义违例(HTML 转义不该发生): %s", raw)
	}
}
```

(import 需补 `"fmt"`。)

注意 import 增加 `"encoding/json"`。

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run 'TestUpsert|TestUpsertDelete|TestUpsertCreates|TestUpsertIdempotent' -v 2>&1 | tail -4`
Expected: FAIL(undefined)

- [x] **Step 3: 实现(追加到 agent_mcp_registry.go)**

```go
// validateAliangMCPEntry 写通道校验(spec §5.2)。读通道容忍、写通道 fail-closed。
func validateAliangMCPEntry(name string, e aliangMCPServerEntry) error {
	if !aliangMCPServerNameRe.MatchString(name) {
		return fmt.Errorf("server 名称须为 ^[a-zA-Z0-9_-]{1,64}$, got %q", name)
	}
	if strings.HasPrefix(name, aliangMCPReservedPrefix) {
		return fmt.Errorf("server 名称不允许保留前缀 %q", aliangMCPReservedPrefix)
	}
	if len(name) > aliangMCPServerNameMax {
		return fmt.Errorf("server 名称超长")
	}
	switch strings.ToLower(strings.TrimSpace(e.Type)) {
	case "", "stdio":
		if strings.TrimSpace(e.Command) == "" {
			return fmt.Errorf("stdio 条目必须提供 command")
		}
	case "http", "sse":
		if strings.TrimSpace(e.URL) == "" {
			return fmt.Errorf("%s 条目必须提供 url", strings.ToLower(strings.TrimSpace(e.Type)))
		}
	default:
		return fmt.Errorf("type 须为 stdio|http|sse, got %q", e.Type)
	}
	return nil
}

// aliangMCPEntryToMap 结构体→注册表 map 形态(仅非零字段;Enabled 仅显式非 nil
// 时落盘)。数字字段不存在于本结构,无 UseNumber 需求。
func aliangMCPEntryToMap(e aliangMCPServerEntry) map[string]interface{} {
	out := map[string]interface{}{}
	if strings.TrimSpace(e.Type) != "" {
		out["type"] = e.Type
	}
	if e.Command != "" {
		out["command"] = e.Command
	}
	if len(e.Args) > 0 {
		out["args"] = e.Args
	}
	if len(e.Env) > 0 {
		out["env"] = e.Env
	}
	if e.URL != "" {
		out["url"] = e.URL
	}
	if len(e.Headers) > 0 {
		out["headers"] = e.Headers
	}
	if e.Enabled != nil {
		out["enabled"] = *e.Enabled
	}
	return out
}

// saveAliangMCPRegistryRoot 全量往返落盘:SetEscapeHTML(false)+缩进 2 +
// tmp+rename 原子写 + 内容未变幂等跳写。目录不存在自动创建。
func saveAliangMCPRegistryRoot(home string, root map[string]interface{}) error {
	path := aliangMCPRegistryPath(home)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, buf.Bytes()) {
		return nil // 幂等跳写
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return writePeerMCPFileAtomic(path, buf.Bytes())
}

// upsertAliangMCPServerForTest 校验→全量往返→原子写(home 注入,测试与
// service 层共用本体)。version 键缺失时补 1,已有值(含更高版本)原样保留。
func upsertAliangMCPServerForTest(home, name string, entry aliangMCPServerEntry) error {
	if err := validateAliangMCPEntry(name, entry); err != nil {
		return err
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		return err // 坏文件不静默覆盖,让用户先修
	}
	if !existed {
		root = map[string]interface{}{"version": json.Number("1"), "servers": map[string]interface{}{}}
	}
	if _, has := root["version"]; !has {
		root["version"] = json.Number("1")
	}
	servers, _ := root["servers"].(map[string]interface{})
	if servers == nil {
		servers = map[string]interface{}{}
		root["servers"] = servers
	}
	if _, existed := servers[name]; !existed {
		// 只数现存键,与读通道的 enabled/畸形口径无关:写通道上限按条目数防呆
		names := 0
		for range servers {
			names++
		}
		if names >= aliangMCPServerMaxCount {
			return fmt.Errorf("注册表条目数已达上限 %d", aliangMCPServerMaxCount)
		}
	}
	servers[name] = aliangMCPEntryToMap(entry)
	return saveAliangMCPRegistryRoot(home, root)
}

// deleteAliangMCPServerForTest 删条目;返回是否存在。
func deleteAliangMCPServerForTest(home, name string) (bool, error) {
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		return false, err
	}
	servers, _ := root["servers"].(map[string]interface{})
	if _, ok := servers[name]; !ok {
		return false, nil
	}
	delete(servers, name)
	return true, saveAliangMCPRegistryRoot(home, root)
}

// aliangMCPEnabledEntries 读通道强类型视图:启用且形态合法的条目
// (codex 托管块同步的数据源)。畸形 Warn 跳过。
func aliangMCPEnabledEntries(home string) map[string]aliangMCPServerEntry {
	out := map[string]aliangMCPServerEntry{}
	raw := map[string]interface{}{}
	readAliangMCPRegistryInto(home, raw)
	for name, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var e aliangMCPServerEntry
		if err := json.Unmarshal(b, &e); err != nil {
			continue
		}
		out[name] = e
	}
	return out
}
```

注:`writePeerMCPFileAtomic` 与 `stampAgentMCPFile` 都是包内既有助手(peer 注册/清单采集),直接复用。

- [x] **Step 4: 跑测试确认通过**

Run: `cd $WT && go test ./app/http/services/ -run 'AliangMCP' -v 2>&1 | tail -12`
Expected: 全 PASS(含 Task 1 的用例)

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/http/services/agent_mcp_registry.go app/http/services/agent_mcp_registry_test.go
git commit -m "$(cat <<'EOF'
新增:统一 MCP 注册表写通道——校验/增删改/幂等原子写

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: codex 托管块同步(文本级多块 diff)

**Files:**
- Modify: `app/http/services/agent_mcp_registry.go`(追加)
- Test: `app/http/services/agent_mcp_registry_test.go`(追加)

- [x] **Step 1: 写失败测试**

```go
func writeCodexConfig(t *testing.T, home, content string) {
	t.Helper()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCodexConfigRaw(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCodexSyncCreatesManagedBlocks(t *testing.T) {
	home := t.TempDir()
	// aliang-peer 托管块共存(spec §8):peer 块对同步器而言就是「用户自有块」
	writeCodexConfig(t, home, "# user comment\n[mcp_servers.aliang-peer]\ncommand = \"peer\"\n\n[mcp_servers.mine]\ncommand = \"x\"\n")
	dis := false
	if err := upsertAliangMCPServerForTest(home, "blender", aliangMCPServerEntry{
		Type: "stdio", Command: "/usr/bin/uvx", Args: []string{"a", "b"},
		Env: map[string]string{"K": "V"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := upsertAliangMCPServerForTest(home, "off", aliangMCPServerEntry{Command: "x", Enabled: &dis}); err != nil {
		t.Fatal(err)
	}
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		t.Fatal(err)
	}
	out := readCodexConfigRaw(t, home)
	if !strings.Contains(out, "[mcp_servers.aliang-mcp-blender]") ||
		!strings.Contains(out, `command = "/usr/bin/uvx"`) ||
		!strings.Contains(out, `args = ["a", "b"]`) ||
		!strings.Contains(out, "env = {") {
		t.Fatalf("托管块缺失: %s", out)
	}
	if strings.Contains(out, "aliang-mcp-off") || strings.Contains(out, "aliang-mcp-blender.env") {
		t.Fatal("enabled=false 条目不得产出托管块;env 必须是 inline table(子表头会撕开块扫描)")
	}
	if !strings.Contains(out, "# user comment") || !strings.Contains(out, "[mcp_servers.mine]") {
		t.Fatalf("用户既有内容必须原样保留: %s", out)
	}
	if !strings.Contains(out, "[mcp_servers.aliang-peer]") {
		t.Fatalf("peer 托管块必须原样共存: %s", out)
	}
}

func TestCodexSyncIdempotent(t *testing.T) {
	home := t.TempDir()
	_ = upsertAliangMCPServerForTest(home, "a", aliangMCPServerEntry{Command: "x"})
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".codex", "config.toml")
	st := stampAgentMCPFile(p)
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		t.Fatal(err)
	}
	if stampAgentMCPFile(p) != st {
		t.Fatal("同步必须幂等(无变化不写盘)")
	}
}

func TestCodexSyncRemovesStaleBlocksAndKeepsOthers(t *testing.T) {
	home := t.TempDir()
	writeCodexConfig(t, home, "[mcp_servers.aliang-mcp-stale]\ncommand = \"x\"\n\n[mcp_servers.mine]\ncommand = \"y\"\n")
	_ = upsertAliangMCPServerForTest(home, "fresh", aliangMCPServerEntry{Command: "f"})
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		t.Fatal(err)
	}
	out := readCodexConfigRaw(t, home)
	if strings.Contains(out, "aliang-mcp-stale") {
		t.Fatalf("失配托管块必须删除: %s", out)
	}
	if !strings.Contains(out, "aliang-mcp-fresh") || !strings.Contains(out, "[mcp_servers.mine]") {
		t.Fatalf("新块与用户块必须共存: %s", out)
	}
}

func TestCodexSyncRefusesNonCanonicalHeader(t *testing.T) {
	home := t.TempDir()
	writeCodexConfig(t, home, "[mcp_servers.\"aliang-mcp-x\"]\ncommand = \"old\"\n")
	if err := applyAliangMCPCodexSyncForTest(home); err == nil {
		t.Fatal("TOML 可解析但表头非规范形态必须拒写")
	}
	// 纯托管块场景:注册表为空且文件无托管键 → 空转不报错
	home2 := t.TempDir()
	writeCodexConfig(t, home2, "[mcp_servers.mine]\ncommand = \"y\"\n")
	if err := applyAliangMCPCodexSyncForTest(home2); err != nil {
		t.Fatalf("空期望集空转不应报错: %v", err)
	}
}

func TestCodexSyncUnparseableConfigRefuses(t *testing.T) {
	home := t.TempDir()
	writeCodexConfig(t, home, "not toml [[[")
	_ = upsertAliangMCPServerForTest(home, "a", aliangMCPServerEntry{Command: "x"})
	if err := applyAliangMCPCodexSyncForTest(home); err == nil {
		t.Fatal("不可解析的用户文件必须拒写(fail-closed)")
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run 'TestCodexSync' -v 2>&1 | tail -4`
Expected: FAIL(undefined: applyAliangMCPCodexSyncForTest)

- [x] **Step 3: 实现(追加)**

```go
// tomlQuote TOML basic string 转义(控制字符 \uXXXX,其余原样)。
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04X`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// aliangMCPCodexBlockLines 生成一个托管块的行序列。codex 无 headers 通道,
// registry headers 不进 codex(spec §6.4);http/sse 只带 url;env 用 inline
// table 保持块内无子表头(块扫描按「[ 开头的行」定界,子表头会撕开块)。
func aliangMCPCodexBlockLines(key string, e aliangMCPServerEntry) []string {
	lines := []string{"[mcp_servers." + key + "]"}
	if strings.TrimSpace(e.URL) != "" {
		return append(lines, "url = "+tomlQuote(e.URL))
	}
	lines = append(lines, "command = "+tomlQuote(e.Command))
	if len(e.Args) > 0 {
		parts := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			parts = append(parts, tomlQuote(a))
		}
		lines = append(lines, "args = ["+strings.Join(parts, ", ")+"]")
	}
	if len(e.Env) > 0 {
		keys := make([]string, 0, len(e.Env))
		for k := range e.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, tomlQuote(k)+" = "+tomlQuote(e.Env[k]))
		}
		lines = append(lines, "env = {"+strings.Join(pairs, ", ")+"}")
	}
	return lines
}

// scanAliangMCPCanonicalManagedHeaders 文本扫描 config.toml 中规范形态的
// [mcp_servers.aliang-mcp-*] 表头(整行精确相等,与 peer 块拼接同一判据)。
func scanAliangMCPCanonicalManagedHeaders(raw string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[mcp_servers."+aliangMCPCodexPrefix) && strings.HasSuffix(trimmed, "]") && !strings.Contains(trimmed, "\"") {
			key := strings.TrimSuffix(strings.TrimPrefix(trimmed, "[mcp_servers."), "]")
			if !strings.Contains(key, ".") {
				out[key] = true
			}
		}
	}
	return out
}

// applyAliangMCPCodexSyncForTest 把 config.toml 的 aliang-mcp-* 托管块集合
// 对齐到注册表启用条目集合(多块 diff:期望集内的块重写,期望集外的托管块
// 删除,其余内容逐字保留)。安全:
//   - 用户文件不可解析 → 拒写(codex 自身同样读不了,fail-closed);
//   - TOML 解析出托管键但文本扫描找不到规范表头(引号/空格/尾注释形态)
//     → 拒写,提示手工规范(防重复表定义打碎文件,peer 注册同款纪律);
//   - 字节无变化 → 幂等跳写;文件缺失且期望集为空 → 不建文件。
func applyAliangMCPCodexSyncForTest(home string) error {
	path := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	desired := map[string]aliangMCPServerEntry{}
	for name, e := range aliangMCPEnabledEntries(home) {
		desired[aliangMCPCodexPrefix+name] = e
	}
	hasFile := err == nil
	if !hasFile && len(desired) == 0 {
		return nil
	}
	if hasFile {
		var cfg codexConfigFile
		if _, perr := toml.Decode(string(raw), &cfg); perr != nil {
			return fmt.Errorf("parse %s: %w;拒绝在无法解析的用户文件上拼块,请先修复 config.toml", path, perr)
		}
		canonical := scanAliangMCPCanonicalManagedHeaders(string(raw))
		for key := range cfg.MCPServers {
			if !strings.HasPrefix(key, aliangMCPCodexPrefix) {
				continue
			}
			if !canonical[key] {
				return fmt.Errorf("codex config 的托管条目 %s 表头不是规范形态 [mcp_servers.%s];拒绝拼接以免打碎文件,请先手工规范表头", key, key)
			}
		}
	}
	// 摘除全部规范托管块 span,其余行原样保留。
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	var kept []string
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		key := ""
		if strings.HasPrefix(trimmed, "[mcp_servers."+aliangMCPCodexPrefix) && strings.HasSuffix(trimmed, "]") && !strings.Contains(trimmed, "\"") {
			k := strings.TrimSuffix(strings.TrimPrefix(trimmed, "[mcp_servers."), "]")
			if !strings.Contains(k, ".") {
				key = k
			}
		}
		if key == "" {
			kept = append(kept, lines[i])
			i++
			continue
		}
		i++ // 跳过表头行
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			i++ // 跳到下一表头/文件尾
		}
	}
	// 追加期望块(键序确定)。
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := kept
	for _, k := range keys {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, aliangMCPCodexBlockLines(k, desired[k])...)
	}
	if len(out) == 0 {
		if !hasFile {
			return nil
		}
		out = nil
	}
	content := strings.Join(out, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n" // 尾换行与 peer 的块构造口径一致
	}
	if hasFile && content == string(raw) {
		return nil // 幂等跳写
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return writePeerMCPFileAtomic(path, []byte(content))
}
```

注:`codexConfigFile` 在 `agent_mcp_inventory.go:342`,`toml` 依赖该文件已引入;`agent_mcp_registry.go` 需补 import `"github.com/BurntSushi/toml"`。

- [x] **Step 4: 跑测试确认通过**

Run: `cd $WT && go test ./app/http/services/ -run 'AliangMCP|TestCodexSync' 2>&1 | tail -4`
Expected: PASS

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/http/services/agent_mcp_registry.go app/http/services/agent_mcp_registry_test.go
git commit -m "$(cat <<'EOF'
新增:统一 MCP 注册表 codex 托管块同步——文本级多块 diff+非规范表头拒写

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: agentcore 合并源

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_mcp.go:94-102`(采集链)
- Test: `app/http/services/agent_ai_agentcore_mcp_test.go`(追加)

- [x] **Step 1: 写失败测试**

```go
func TestAgentCoreMCPRawServersInIncludesRegistry(t *testing.T) {
	home := t.TempDir()
	// 遗留源与注册表同名:注册表胜
	writeRegistryFile(t, home, `{"version":1,"servers":{
	  "dup":{"command":"registry-cmd"},
	  "blend":{"type":"stdio","command":"uvx","args":["blender-mcp"]}}}`)
	claudeJSON := filepath.Join(home, ".claude.json")
	_ = os.WriteFile(claudeJSON, []byte(`{"mcpServers":{"dup":{"command":"claude-cmd","args":["x"]}}}`), 0o600)

	raws := agentCoreMCPRawServersIn(home)
	byName := map[string]agentCoreMCPRawServer{}
	for _, r := range raws {
		byName[r.Name] = r
	}
	if byName["dup"].Command != "registry-cmd" {
		t.Fatalf("注册表同名键必须覆盖遗留源, got %+v", byName["dup"])
	}
	if byName["blend"].Command != "uvx" || len(byName["blend"].Args) != 1 {
		t.Fatalf("注册表 stdio 条目必须进采集: %+v", byName["blend"])
	}
}
```

(文件头若无 `os/filepath` import 则补;`writeRegistryFile` 在同包测试文件已定义。)

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run TestAgentCoreMCPRawServersInIncludesRegistry 2>&1 | tail -3`
Expected: FAIL(dup=claude-cmd)

- [x] **Step 3: 实现**

`agentCoreMCPRawServersIn` 插件循环后加一行(spec §6.2:注册表最后合并胜出;后续 headers 预筛/门控/cap 全部不动):

```go
	// 统一注册表(~/.aliang/mcp.json):遗留源之后合并,同名键注册表胜出
	// (enabled/畸形条目在读通道已跳过)。
	readAliangMCPRegistryInto(home, raw)
```

- [x] **Step 4: 跑测试确认通过(含既有回归)**

Run: `cd $WT && go test ./app/http/services/ -run 'TestAgentCoreMCPRawServersIn' -v 2>&1 | tail -6`
Expected: 新旧全 PASS

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/http/services/agent_ai_agentcore_mcp.go app/http/services/agent_ai_agentcore_mcp_test.go
git commit -m "$(cat <<'EOF'
新增:agentcore 注入链接入统一 MCP 注册表源(同名键注册表胜出)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: claudecode 合并源(claudeTierMCPArgs 快速路径改造)

**Files:**
- Modify: `app/http/services/agent_ai_claude_policy.go:183-217`
- Test: 新 `app/http/services/agent_ai_claude_policy_mcp_registry_test.go`

前置事实:`claudeTierMCPArgs` 内部读 `agentHome()` 与 `readPluginMCPServers(merged)`,均不可注入。先加两个注入口:

- [x] **Step 1: 写失败测试**

```go
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
```

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run TestClaudeTierMCPArgs 2>&1 | tail -3`
Expected: FAIL(undefined: claudeTierMCPHome)

- [x] **Step 3: 实现**

3a. `agent_ai_claude_policy.go` 包级新增注入点与 home 化插件读取(先 Read `readPluginMCPServers` 原体,新函数必须行为一致仅 home 注入化):

```go
// claudeTierMCPHome 是 claudeTierMCPArgs 取 home 的注入点(测试替换);
// swap 期间该包测试禁 t.Parallel。
var claudeTierMCPHome = agentHome

// readPluginMCPServersIn 与 readPluginMCPServers(agent_ai_claude_policy.go:338)
// 行为一致,仅 home 注入化;插件复合键 plugin:<name>:<server> 语义不变。
func readPluginMCPServersIn(home string, dst map[string]interface{}) int {
	added := 0
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		added += readPluginMCPDeclaration(inst.installPath, inst.pluginName, dst)
	}
	return added
}
```

3b. `claudeTierMCPArgs` 主体替换(spec §6.3:合并序 + 两条快速路径新条件):

```go
func claudeTierMCPArgs(policy agentAIClaudeRemotePolicy, projectPath string) ([]string, func(), error) {
	merged := map[string]interface{}{}
	home := claudeTierMCPHome()
	if home != "" {
		readMCPServersFile(filepath.Join(home, ".claude.json"), merged)
		readPluginMCPServersIn(home, merged)
	}
	// 统一注册表:claude 原生发现看不见 ~/.aliang/mcp.json,有任意启用条目
	// 就必须发 flags;同名键在此覆盖 user/plugin 条目。raw 计数(覆盖型条目
	// 也算贡献,防「同名 shadow 被当零贡献」误判)。
	registryCount := 0
	if home != "" {
		registryCount = readAliangMCPRegistryInto(home, merged)
	}
	// 项目 .mcp.json 仅在项目 MCP 可信时并入(信任闸语义不变,项目条目仍最后
	// 合并、同名仍项目胜)。
	projectCount := 0
	if policy.projectMCPTrusted {
		beforeProject := len(merged)
		readMCPServersFile(filepath.Join(projectPath, ".mcp.json"), merged)
		projectCount = len(merged) - beforeProject
	}
	if projectCount == 0 && registryCount == 0 {
		// 双零贡献:走 claude 原生 user 域发现(原两条快速路径的并集)。
		return nil, func() {}, nil
	}
	payload, err := json.Marshal(map[string]interface{}{"mcpServers": merged})
	if err != nil {
		return nil, func() {}, err
	}
	// ……以下 temp-file 尾段与原实现逐字一致(:198-216),不改。
}
```

- [x] **Step 4: 跑测试确认通过(含既有 claude policy 回归)**

Run: `cd $WT && go test ./app/http/services/ -run 'TestClaudeTier' -v 2>&1 | tail -8`
Expected: 新旧全 PASS。⚠️ 既有测试甄别(第 2 轮评审实锤,两测情况不同):
- `TestClaudeTierMCPArgsInactive`(agent_ai_claude_policy_test.go:877)不设 HOME、读真机家目录——今天通过只是因为真机还没有 `~/.aliang/mcp.json`;冒烟创建该文件后会变红。处置:给它 swap `claudeTierMCPHome` 到空 TempDir(修测试,不改实现)。
- `TestClaudeTierMCPArgsCollisionOnlyProjectIsNoop`(:894)**已经** `t.Setenv("HOME"/"USERPROFILE")` 指向自己的 TempDir,读的是自设 home,**不需要也不得改**——给它 swap 空 TempDir 反而会弄断它(它自己的 `.claude.json` 断链→projectCount 误判→flags 断言失败)。保持原样。

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/http/services/agent_ai_claude_policy.go app/http/services/agent_ai_claude_policy_mcp_registry_test.go
git commit -m "$(cat <<'EOF'
新增:claudecode 注入链接入统一 MCP 注册表——快速路径改造(不可信项目仍发 flags)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 集成清单——aliang 来源 + codex 托管块过滤

**Files:**
- Modify: `app/http/services/agent_mcp_inventory.go:289-329(claude 采集器)、:356-410(codex 采集器)`
- Test: 新 `app/http/services/agent_mcp_inventory_registry_test.go`

- [x] **Step 1: 写失败测试**

```go
package services

// 统一注册表进入清单的采集语义:claude 侧打 source=aliang + stamp;
// codex 侧过滤 aliang-mcp-* 托管块。涉缓存 swap 面,禁 t.Parallel。
import (
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
		if len(s.Name) > 11 && s.Name[:11] == "aliang-mcp-" {
			t.Fatalf("托管块不得进清单(注册表行已代表): %+v", out)
		}
	}
	if len(out) != 1 || out[0].Name != "mine" {
		t.Fatalf("用户自有 codex 条目保持不变: %+v", out)
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run 'TestClaudeInventory|TestCodexInventory' 2>&1 | tail -3`
Expected: FAIL

- [x] **Step 3: 实现**

3a. `claudeMCPServerInventoryIn`(:299-329)四处改动:

```go
	aliangJSON := aliangMCPRegistryPath(home)
	aliangStamp := stampAgentMCPFile(aliangJSON)
	stamps := []agentMCPFileStamp{claudeStamp, settingsStamp, registryStamp, aliangStamp}
	// 早退条件收紧:注册表单独存在也必须出清单(没装 Claude 的机器自洽)
	if !claudeStamp.exists && !registryStamp.exists && !aliangStamp.exists {
		return nil, stamps
	}
	// ……userRaw/pluginRaw 采集不变……
	out := agentMCPServersFromClaudeRaw(userRaw, "user")
	out = append(out, agentMCPServersFromClaudeRaw(pluginRaw, "plugin")...)
	// 统一注册表:enabled+合法条目(source=aliang);readAliangMCPRegistryInto
	// 输出的就是 claude 形态,脱敏复用同一转换器。
	registryRaw := map[string]interface{}{}
	readAliangMCPRegistryInto(home, registryRaw)
	out = append(out, agentMCPServersFromClaudeRaw(registryRaw, "aliang")...)
```

3b. `codexMCPServerInventoryIn` 循环体内、cap 判定之前加:

```go
		// 平台托管块不入清单:同名注册表条目已在 claude 侧以 source=aliang
		// 呈现,不过滤会出现一行双身份(评审第 1 轮实锤)。peer 的 aliang-peer
		// 行为保持现状不动。
		if strings.HasPrefix(name, aliangMCPCodexPrefix) {
			continue
		}
```

- [x] **Step 4: 跑测试确认通过(含清单既有回归)**

Run: `cd $WT && go test ./app/http/services/ -run 'Inventory|MCP' 2>&1 | tail -4`
Expected: 全 PASS(既有 mcp inventory 测试不动)

- [x] **Step 5: 集成视图联动验证(agent_integrations.go 无需改码,跑聚合测试)**

`collectAgentIntegrationMCPServers` 里 `kernel := agentCoreMCPRawServersIn(home)` 已自动包含注册表条目(Task 4),清单行来自 claude 采集器(Task 6)——kernel_state 会判为 injected。跑:

Run: `cd $WT && go test ./app/http/services/ -run 'TestCollectAgentIntegration' -v 2>&1 | tail -4`
Expected: PASS(若该名字不存在,`grep -rn "collectAgentIntegrationMCPServers" app/http/services/*_test.go` 找既有测试名跑)

- [x] **Step 6: 提交**

```bash
cd $WT && git add app/http/services/agent_mcp_inventory.go app/http/services/agent_mcp_inventory_registry_test.go
git commit -m "$(cat <<'EOF'
新增:集成清单接入统一 MCP 注册表——source=aliang+缓存 stamp;codex 侧过滤托管块

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 启动自愈挂点

**Files:**
- Modify: `app/http/services/agent_mcp_registry.go`(追加包装)
- Modify: `app/agentruntime/server.go:90` 附近
- Test: `app/http/services/agent_mcp_registry_test.go`(追加)

- [x] **Step 1: 写失败测试**

```go
func TestEnsureAliangMCPCodexMatchesRegistry(t *testing.T) {
	home := t.TempDir()
	// 注册表有条目 + codex 块缺失 → 自愈补齐,不 panic 不返回错误
	_ = upsertAliangMCPServerForTest(home, "a", aliangMCPServerEntry{Command: "x"})
	EnsureAliangMCPCodexMatchesRegistryForTest(home)
	out := readCodexConfigRaw(t, home)
	if !strings.Contains(out, "aliang-mcp-a") {
		t.Fatalf("自愈必须补齐托管块: %s", out)
	}
	// 非规范表头场景:拒写只 Warn 不抛(自愈永不阻断启动)
	writeCodexConfig(t, home, "[mcp_servers.\"aliang-mcp-a\"]\ncommand = \"old\"\n")
	EnsureAliangMCPCodexMatchesRegistryForTest(home) // 不 panic 即通过
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run TestEnsureAliangMCPCodex 2>&1 | tail -3`
Expected: FAIL(undefined)

- [x] **Step 3: 实现**

`agent_mcp_registry.go` 追加(对齐 `EnsurePeerMCPMatchesConfig` 形态,agent_peer_mcp.go:235-246):

```go
// EnsureAliangMCPCodexMatchesRegistryForTest 是 home 可注入的自愈本体:
// 纠正「注册表已写但 codex 拼块被拒后用户已手工修复」与「用户手删托管块」
// 的漂移。只 Warn 不返回错误——自愈永不阻断启动。
func EnsureAliangMCPCodexMatchesRegistryForTest(home string) {
	if home == "" {
		logger.Warn("aliang-mcp: startup codex sync skipped, agent home unavailable")
		return
	}
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		logger.Warn(fmt.Sprintf("aliang-mcp: startup codex sync failed (注册表不受影响,修复 config.toml 后下次启动补齐): %v", err))
	}
}
```

`app/agentruntime/server.go` 在 `services.EnsurePeerMCPMatchesConfig()` 调用之后追加:

```go
	// 统一 MCP 注册表 codex 托管块自愈(同 peer 自愈纪律:只 Warn 不阻断)。
	services.EnsureAliangMCPCodexMatchesRegistry()
```

并在 `agent_mcp_registry.go` 补生产包装:

```go
// EnsureAliangMCPCodexMatchesRegistry 生产入口(agentHome 注入)。
func EnsureAliangMCPCodexMatchesRegistry() {
	EnsureAliangMCPCodexMatchesRegistryForTest(agentHome())
}
```

- [x] **Step 4: 跑测试确认通过 + 构建验证**

Run: `cd $WT && go test ./app/http/services/ -run TestEnsureAliangMCPCodex -v 2>&1 | tail -3 && go build ./...`
Expected: PASS + 构建通过

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/http/services/agent_mcp_registry.go app/http/services/agent_mcp_registry_test.go app/agentruntime/server.go
git commit -m "$(cat <<'EOF'
新增:统一 MCP 注册表 codex 托管块启动自愈(只 Warn 不阻断)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: HTTP API(服务方法 + 3 handler + 路由)

**Files:**
- Modify: `app/http/services/agent_mcp_registry.go`(追加 service 方法)
- Create: `app/http/handlers/agent_mcp_registry_handler.go`
- Modify: `app/http/routes/routes.go`(注册路由,放 :298 integrations 旁)
- Test: `app/http/services/agent_mcp_registry_test.go`(追加 service 层用例)

- [x] **Step 1: 写失败测试(service 层)**

```go
func TestAgentServiceMCPRegistryFlow(t *testing.T) {
	home := t.TempDir()
	old := mcpRegistryHome
	mcpRegistryHome = func() string { return home }
	t.Cleanup(func() { mcpRegistryHome = old })
	svc := &AgentService{}

	// 初始空表
	reg, err := svc.AliangMCPRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg["version"] == nil {
		t.Fatalf("空表必须返回 version 视图, got %#v", reg)
	}
	// upsert + 同步结果
	res := svc.UpsertAliangMCPServer("web", AliangMCPServerRequest{
		Type: "http", URL: "https://api.example.com/mcp",
	})
	if res.RegistryErr != "" {
		t.Fatalf("registry err: %s", res.RegistryErr)
	}
	if res.CodexSync.Error != "" {
		t.Fatalf("空 codex 场景同步不应报错: %s", res.CodexSync.Error)
	}
	// 校验失败路径
	res = svc.UpsertAliangMCPServer("bad name!", AliangMCPServerRequest{Command: "x"})
	if res.RegistryErr == "" {
		t.Fatal("非法名必须失败且带原因")
	}
	// 删除
	res = svc.DeleteAliangMCPServer("web")
	if res.RegistryErr != "" || !res.Existed {
		t.Fatalf("delete: %+v", res)
	}
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `cd $WT && go test ./app/http/services/ -run TestAgentServiceMCPRegistryFlow 2>&1 | tail -3`
Expected: FAIL(undefined)

- [x] **Step 3: 实现 service 层(追加到 agent_mcp_registry.go)**

注意 `agentHome` 也要注入口(与 `claudeTierMCPHome` 同款):

```go
// mcpRegistryHome 是本文件生产包装取 home 的注入点(测试 swap,禁并行)。
var mcpRegistryHome = agentHome

// AliangMCPServerRequest 是 PUT 通道的请求形态(handler DTO)。
type AliangMCPServerRequest struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Enabled *bool             `json:"enabled"`
}

func (r AliangMCPServerRequest) entry() aliangMCPServerEntry {
	return aliangMCPServerEntry(r)
}

// AliangMCPSyncResult 是注册表写 API 的统一响应骨架。两段式:第一段(注册表
// 落盘)失败在 RegistryErr;第二段(codex 托管块)失败在 CodexSync.Error 且
// 不回滚第一段(spec §7.1——注册表已是真值,自愈兜底)。
type AliangMCPSyncResult struct {
	Existed     bool              `json:"existed,omitempty"`
	RegistryErr string            `json:"registry_error,omitempty"`
	CodexSync   AliangMCPCodexSync `json:"codex_sync"`
}

type AliangMCPCodexSync struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AliangMCPRegistry GET 视图:注册表原文(本地管理须见原文;文件在用户自己
// 机器上)。缺文件返回空表视图。
func (s *AgentService) AliangMCPRegistry() (map[string]interface{}, error) {
	home := mcpRegistryHome()
	if home == "" {
		return nil, fmt.Errorf("agent home unavailable")
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		return nil, err
	}
	if !existed {
		return map[string]interface{}{"version": json.Number("1"), "servers": map[string]interface{}{}}, nil
	}
	return root, nil
}

// UpsertAliangMCPServer PUT 语义:新增或整体替换。
func (s *AgentService) UpsertAliangMCPServer(name string, req AliangMCPServerRequest) AliangMCPSyncResult {
	res := AliangMCPSyncResult{}
	home := mcpRegistryHome()
	if home == "" {
		res.RegistryErr = "agent home unavailable"
		return res
	}
	if err := upsertAliangMCPServerForTest(home, name, req.entry()); err != nil {
		res.RegistryErr = err.Error()
		return res
	}
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		res.CodexSync = AliangMCPCodexSync{OK: false, Error: err.Error()}
		return res
	}
	res.CodexSync = AliangMCPCodexSync{OK: true}
	return res
}

// DeleteAliangMCPServer DELETE 语义。
func (s *AgentService) DeleteAliangMCPServer(name string) AliangMCPSyncResult {
	res := AliangMCPSyncResult{}
	home := mcpRegistryHome()
	if home == "" {
		res.RegistryErr = "agent home unavailable"
		return res
	}
	existed, err := deleteAliangMCPServerForTest(home, name)
	if err != nil {
		res.RegistryErr = err.Error()
		return res
	}
	res.Existed = existed
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		res.CodexSync = AliangMCPCodexSync{OK: false, Error: err.Error()}
		return res
	}
	res.CodexSync = AliangMCPCodexSync{OK: true}
	return res
}
```

⚠️ 实现时先核对 `AgentService` 的既有构造/单例获取方式(grep `func (s *AgentService)` 现有方法与 `newAgentService`),保持同族;若 `AgentService` 是接口/包装,则方法挂同一 receiver。

- [x] **Step 4: 实现 handler(新文件)**

```go
package handlers

// agent_mcp_registry_handler.go 统一 MCP 注册表 API(GET/PUT/DELETE)。
// 写面属设备本地配置读写,与 peer/mcp 同族走 proxyIfNeeded 落到 user-agent
// runtime(core 进程只是透传,注册表文件在 agent 家目录)。
import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

const aliangMCPRequestMaxBytes = 64 << 10 // 64KB,防呆

// HandleAliangMCPRegistry GET /api/agent/mcp — 注册表原文。
func (h *AgentHandler) HandleAliangMCPRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	reg, err := h.service.AliangMCPRegistry()
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, reg)
}

// HandleAliangMCPServerPut PUT /api/agent/mcp/servers/{name}
// 校验失败=400 带原因(spec §7.1);成功 200,body 携带 codex_sync 两段式结果。
func (h *AgentHandler) HandleAliangMCPServerPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		common.ErrorBadRequest(w, "server name required", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, aliangMCPRequestMaxBytes)
	var req services.AliangMCPServerRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}
	res := h.service.UpsertAliangMCPServer(name, req)
	if res.RegistryErr != "" {
		common.ErrorBadRequest(w, res.RegistryErr, nil)
		return
	}
	common.Success(w, res)
}

// HandleAliangMCPServerDelete DELETE /api/agent/mcp/servers/{name}
// 不存在=404(spec §7.1);成功 200(body.existed=true)。
func (h *AgentHandler) HandleAliangMCPServerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		common.ErrorBadRequest(w, "server name required", nil)
		return
	}
	res := h.service.DeleteAliangMCPServer(name)
	if res.RegistryErr != "" {
		common.ErrorBadRequest(w, res.RegistryErr, nil)
		return
	}
	if !res.Existed {
		// common.Error 收业务码(100-299),直传 http 状态会映射成 500——
		// 真实 404 须用 http.Error(peer/mcp 的 405 同款惯例)。
		http.Error(w, "server not found: "+name, http.StatusNotFound)
		return
	}
	common.Success(w, res)
}
```

⚠️ 实现时注意:① `proxyIfNeeded` 与 `proxyDataIfNeeded` 的分工——peer/mcp 用前者,integrations 用后者;注册表属「触碰 agent 家目录配置文件」同 peer/mcp 族,用 `proxyIfNeeded`,并先读两函数实现确认语义一致;② DTO `AliangMCPServerRequest` 定在 services 包(`entry()` 转换就地),handler 经 services 包引用,import 别名以 agent_handler.go 现状对齐。

- [x] **Step 5: 注册路由(routes.go :298 旁)**

```go
	// 统一 MCP 注册表(第一方资产,三内核消费)。{name} 用方法前缀模式注册
	// (同 quick-setup combos 约定,避免 ServeMux 双注册 panic)。
	register("/api/agent/mcp", h.Agent.HandleAliangMCPRegistry, http.MethodGet)
	register("PUT /api/agent/mcp/servers/{name}", h.Agent.HandleAliangMCPServerPut, http.MethodPut)
	register("DELETE /api/agent/mcp/servers/{name}", h.Agent.HandleAliangMCPServerDelete, http.MethodDelete)
```

- [x] **Step 6: 跑测试确认通过 + 构建验证**

Run: `cd $WT && go test ./app/http/services/ -run 'AliangMCP|TestAgentServiceMCPRegistryFlow' 2>&1 | tail -4 && go build ./... && go vet ./app/http/... 2>&1 | tail -3`
Expected: PASS + 构建/vet 干净

- [x] **Step 7: 提交**

```bash
cd $WT && git add app/http/services/agent_mcp_registry.go app/http/services/agent_mcp_registry_test.go app/http/handlers/agent_mcp_registry_handler.go app/http/routes/routes.go
git commit -m "$(cat <<'EOF'
新增:统一 MCP 注册表 HTTP API——GET 原文/PUT 增改/DELETE 删,两段式 codex 同步

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 前端 API client + 来源徽章 + i18n

**Files:**
- Modify: `app/website/src/services/agentApi.js`(:65 附近)
- Modify: `app/website/src/i18n/zh.js` / `en.js`
- Test: 无(随 Task 10 组件测试覆盖)

- [x] **Step 1: agentApi.js 追加**

```js
// 统一 MCP 注册表管理(第一方资产,三内核消费)。
export async function getAliangMCPRegistry() {
  return request('/api/agent/mcp', { method: 'GET' });
}

export async function putAliangMCPServer(name, entry) {
  return request(`/api/agent/mcp/servers/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(entry),
  });
}

export async function deleteAliangMCPServer(name) {
  return request(`/api/agent/mcp/servers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  });
}
```

(先对照 `request` 封装的既有调用形态——如 body/headers 是否由封装统一处理——保持一致。)

- [x] **Step 2: i18n 键(zh.js / en.js 各追加一段)**

zh.js:
```js
  // 统一 MCP 注册表管理卡
  agent_mcp_registry_title: 'MCP 服务器',
  agent_mcp_registry_hint: 'aliang 自管的 MCP 服务器,保存后对所有内核即时生效(AliangCode / Claude / Codex)。',
  agent_mcp_registry_add: '新增服务器',
  agent_mcp_registry_edit: '编辑',
  agent_mcp_registry_delete: '删除',
  agent_mcp_registry_name: '名称',
  agent_mcp_registry_type: '类型',
  agent_mcp_registry_command: '命令',
  agent_mcp_registry_args: '参数(每行一个)',
  agent_mcp_registry_url: 'URL',
  agent_mcp_registry_env: '环境变量(每行 KEY=VALUE)',
  agent_mcp_registry_headers: '请求头(每行 KEY: VALUE,仅 http/sse)',
  agent_mcp_registry_enabled: '启用',
  agent_mcp_registry_name_invalid: '名称仅允许字母/数字/下划线/连字符(≤64),且不能以 aliang- 开头',
  agent_mcp_registry_delete_confirm: '删除后将从所有内核移除(Codex 托管块同步删除)。确定删除「{name}」?',
  agent_mcp_registry_codex_sync_failed: '注册表已保存,但 Codex 配置同步失败:{error}(修复 config.toml 表头后重启 agent 会自动补齐)',
  agent_mcp_registry_empty: '尚无自管 MCP 服务器',
  agent_mcp_registry_shadow_warning: '该名称与 Claude 配置中的遗留条目同名,注册表版本优先生效',
```

en.js 同键英文。**验收哨兵**(memory `local-pkg-build-gotchas`):`strings` grep 键存在性——`grep -c "agent_mcp_registry_" app/website/src/i18n/zh.js` ≥ 17。

- [x] **Step 3: 提交**

```bash
cd $WT && git add app/website/src/services/agentApi.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
新增:统一 MCP 注册表前端 API client 与 i18n 文案

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 前端管理卡片组件 + 挂载

**Files:**
- Create: `app/website/src/components/settings/AgentMCPRegistrySettings.vue`
- Create: `app/website/src/components/settings/AgentMCPRegistrySettings.test.js`
- Modify: `app/website/src/components/settings/agent/AgentPage.vue:29-32,108,134`

- [x] **Step 1: 组件(完整 SFC)**

实现要点(写代码时逐条落实;视觉与交互细节对照 `AgentIntegrationsSettings.vue` 的卡片风格、触控 ≥40px、`dark:` 双主题,遵守 memory `dashboard-mobile-responsive-pattern`):

- 表格数据源 = `getAliangMCPRegistry()`(注册表原文,可编辑行),**不**从集成清单派生;
- 并行取 `getAgentIntegrations()`,仅为「来源」列计算:行 `name` 在清单中命中且 source 含其他来源时,列显示 `aliang` + 附加来源(如 `aliang + user`)并出 `agent_mcp_registry_shadow_warning` 提示;判断用 **source 字符串逗号拆分后包含**,不全等(评审第 2 轮建议);
- 「新增服务器」按钮开弹窗表单:`type` select(stdio/http/sse)联动字段组(stdio→command/args/env;http|sse→url/headers);客户端校验镜像服务端规则(名称正则 `^[a-zA-Z0-9_-]{1,64}$` + 禁 `aliang-` 前缀;stdio 必填 command;http/sse 必填 url);
- 行内 enabled 开关 → `putAliangMCPServer(name, {...entry, enabled: next})`;
- 编辑(回填表单)与删除(confirm 后 `deleteAliangMCPServer`);
- 响应 `codex_sync.ok === false` 时展示 `agent_mcp_registry_codex_sync_failed`(带 error);`registry_error` 存在时展示之;
- 保存/删除成功后**同时刷新**注册表与集成清单两份本地数据(集成清单缓存 stamp 已由 Task 6 服务端解决,前端只需重拉);
- 全部文案走 `t('agent_mcp_registry_*')`;空态显示 `agent_mcp_registry_empty`。

- [x] **Step 2: vitest(完整测试文件)**

先读 `AgentIntegrationsSettings.test.js` 与 `PeerAccessSettings.test.js` 的 mock/mount 风格,保持同族。用例至少覆盖:
1. 挂载后渲染注册表行(名称/类型/入口);
2. 编辑弹窗 type 切 stdio→http 时字段组联动(url 出现、command 消失);
3. 客户端校验:非法名提交不发 PUT 且显示 `agent_mcp_registry_name_invalid`;
4. 删除确认后调用 DELETE 并刷新;
5. `codex_sync.ok=false` 响应显示同步失败文案。

- [x] **Step 3: AgentPage.vue 挂载**

:29-32 `user-agent` 模板分支内、`<AgentIntegrationsSettings />` 之前插 `<AgentMCPRegistrySettings />`;:108 import;:134 components 注册。

- [x] **Step 4: 跑前端测试**

Run: `cd $WT/app/website && npx vitest run src/components/settings/AgentMCPRegistrySettings.test.js 2>&1 | tail -6`
Expected: 全 PASS

- [x] **Step 5: 提交**

```bash
cd $WT && git add app/website/src/components/settings/AgentMCPRegistrySettings.vue app/website/src/components/settings/AgentMCPRegistrySettings.test.js app/website/src/components/settings/agent/AgentPage.vue
git commit -m "$(cat <<'EOF'
新增:设置页 MCP 服务器管理卡片——增删改/启停/类型联动表单+来源提示

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: 全量回归 + 冒烟收尾

**Files:** 无新改动(验证与收尾)

- [x] **Step 1: Go 全量回归**

```bash
cd $WT && gofmt -l app/http/services/ app/http/handlers/ | head -3   # 期望空
go build ./...
go test ./app/http/services/ ./processor/config/ 2>&1 | tail -6
```

Expected: 全 PASS。基线 flake(§全局约定 4)出现时对照基线判断,勿当回归。

- [x] **Step 2: 前端全量回归**

```bash
cd $WT/app/website && npx vitest run 2>&1 | tail -6
```

Expected: 全 PASS(基线 ~268 用例 + 新增)。

- [x] **Step 3: e2e 冒烟(按仓内既有 e2e 设施)**

先定位既有 e2e 套件(`find . -name "*.spec.mjs" -o -name "*.spec.js" -not -path "*/node_modules/*"`,memory 提到过 mobile-responsive 哨兵)。若套件可本地起服,加一条冒烟:UI 新增 `blender`(stdio, 假命令)→ 集成清单出现 `source=aliang` 行;若 e2e 依赖不可用,以 Step 4 手工冒烟替代并在完成记录中注明。

- [x] **Step 4: 真机手工冒烟清单(记录到完成总结)**

```bash
cd $WT && go build -o /tmp/aliang-registry-test .   # 或仓内既有本地出包方式
# 1. 启动后 curl 三件套:
curl -s localhost:56431/api/agent/mcp | head -3                       # 空表视图
curl -s -X PUT localhost:56431/api/agent/mcp/servers/blender -d '{"type":"stdio","command":"/opt/homebrew/bin/uvx","args":["blender-mcp"]}'
curl -s localhost:56431/api/agent/integrations | python3 -m json.tool | grep -A3 blender   # source=aliang, kernel_state=injected
# 2. cat ~/.codex/config.toml 尾部应有 [mcp_servers.aliang-mcp-blender]
# 3. 仪表盘设置页 Agent → user-agent:管理卡片可见、启停、编辑、删除全链
# 4. aliangcode 会话里让内核调 mcp__blender__get_scene_info(需 Blender+addon 运行)→ 手机审批卡 → 通过
# 5. DELETE blender 后 codex 托管块消失
# 5b. 状态码契约(spec §7.1):
#    curl -s -o /dev/null -w "%{http_code}\n" -X PUT localhost:56431/api/agent/mcp/servers/bad%20name -d '{"command":"x"}'   # 400
#    curl -s -o /dev/null -w "%{http_code}\n" -X DELETE localhost:56431/api/agent/mcp/servers/nonexistent                   # 404
# 6. 跨仓验证点(spec §7.1):设备上报 mcp_servers[] 现在带 source="aliang" 新枚举值
#    ——核对 PhoneServer 侧对 source 的 schema 是否容忍新值(zod 严格枚举会整包拒,
#    静默杀掉无关功能);必要时 PhoneServer 侧同步扩枚举后再部署
```

- [x] **Step 5: spec/plan 状态收尾 + 最终提交**

两个文档状态行更新(spec 状态加「已实施」;plan 勾选完全)。若有改动:

```bash
cd $WT && git add docs/superpowers/specs/2026-11-05-aliang-mcp-registry-design.md docs/superpowers/plans/2026-11-05-aliang-mcp-registry.md
git commit -m "$(cat <<'EOF'
文档:统一 MCP 注册表 spec/plan 状态收尾

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

**不 push、不合 master**——完成后向用户汇报,合并/推送/部署由用户决定(仓库纪律)。
