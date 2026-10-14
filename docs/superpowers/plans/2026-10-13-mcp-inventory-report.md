# MCP 能力清单上报 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把设备上 Claude/Codex 配置的 MCP 服务器清单(脱敏后)纳入 AgentTool 上报,随现有 tools[] 通道存到 PhoneServer。

**Architecture:** 客户端新增解析层 `agent_mcp_inventory.go`(claude 复用从 claude policy 拆出的纯读取层,codex 用 BurntSushi/toml),按「路径+mtime+size」缓存,挂在 `detectAgentToolsWith` 填充 `AgentTool.MCPServers`;服务端在 schemas.ts + normalize.ts 两处加白(注册/同步走 schema,hello 走 normalize),落 `devices.tools` JSON 列零迁移。

**Tech Stack:** Go(BurntSushi/toml v1.4.0 已在 go.mod)、TypeScript + zod v3 + vitest。

**Spec:** `docs/superpowers/specs/2026-10-13-mcp-inventory-report-design.md`

**仓库与分支:**
- 客户端:当前 worktree `~/MyProgram/GoProgram/nursor/alianggate-wt-mcp-report`,分支 `worktree-feat+mcp-inventory-report`
- 服务端:`~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`,Task 8 建 worktree `../AliangPhoneServer-wt-mcp-report`,分支 `feat/mcp-inventory-report`(基于 main)

**⚠️ 提交规则(项目 CLAUDE.md):** 全中文标题(「新增:」),提交前必须获得用户明确同意——执行本计划前先向用户请求一次性提交授权;未授权则只改码不提交,任务末尾的 commit 步骤挂起。所有 commit 末尾加:

```
🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
```

**已知陷阱:**
- 客户端 `go test ./app/http/services/` 全包在高并行下有 3 个预存 flake(agent_ai 子进程超时),单包复跑即绿,勿当回归
- 客户端纯逻辑测试文件**不带 GOOS 后缀**(否则非 Windows 静默跳过)
- 服务端仅 WS hello 走 normalize;注册/HTTP 同步的闸门是 zod schema——**两处都必须改**

---

## Task 1: 数据模型——AgentMCPServer 与 AgentTool.MCPServers

**Files:**
- Modify: `app/http/models/agent.go:77-89`(AgentTool)
- Test: `app/http/services/agent_mcp_inventory_test.go`(新建)

- [ ] **Step 1: 写失败测试(JSON 线格式契约)**

新建 `app/http/services/agent_mcp_inventory_test.go`(文件名无 GOOS 后缀):

```go
package services

import (
	"encoding/json"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 线格式契约:mcp_servers 是 snake_case 顶层键;空值时整键省略(omitempty)。
func TestAgentToolMCPServersJSONShape(t *testing.T) {
	with := models.AgentTool{ID: "claude", MCPServers: []models.AgentMCPServer{
		{Name: "serena", Transport: "stdio", Command: "uvx serena", Source: "user"},
	}}
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	servers, ok := m["mcp_servers"].([]interface{})
	if !ok || len(servers) != 1 {
		t.Fatalf("want mcp_servers array with 1 entry, got %s", raw)
	}
	first := servers[0].(map[string]interface{})
	if first["name"] != "serena" || first["transport"] != "stdio" || first["command"] != "uvx serena" {
		t.Fatalf("unexpected entry: %v", first)
	}
	if _, ok := first["url"]; ok {
		t.Fatalf("stdio entry must omit url")
	}

	bare, err := json.Marshal(models.AgentTool{ID: "opencode"})
	if err != nil {
		t.Fatalf("marshal bare: %v", err)
	}
	if containsKey(t, bare, "mcp_servers") {
		t.Fatalf("nil MCPServers must omit mcp_servers")
	}
}

func containsKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_, ok := m[key]
	return ok
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestAgentToolMCPServersJSONShape -v`
Expected: FAIL,编译错误 `undefined: models.AgentMCPServer`

- [ ] **Step 3: 实现**

`app/http/models/agent.go` 在 `Efforts` 字段之后(:89 附近)追加字段,并在 AgentTool 定义前新增类型:

```go
// AgentMCPServer 是设备上某 CLI 配置的一个 MCP 服务器摘要(脱敏后,设备级
// 能力清单)。env 值永不进入本结构;长 token/URL query 由采集层打码。
type AgentMCPServer struct {
	Name string `json:"name"`
	// Transport 是传输形态:stdio | http | sse(codex url 条目统一归 http)。
	Transport string `json:"transport"`
	// Command 是 stdio 形态的 argv join 命令行;http/sse 形态省略。
	Command string `json:"command,omitempty"`
	// URL 是 http/sse 形态的端点(可疑 query 值已打码);stdio 形态省略。
	URL string `json:"url,omitempty"`
	// Source 标记来源:claude 为 user|plugin,codex 为 config。
	Source string `json:"source,omitempty"`
}
```

AgentTool 结构体追加:

```go
	// MCPServers 是该 CLI 配置的 MCP 服务器清单(设备级,脱敏后)。nil=未
	// 采集、无配置或不适用(opencode/pi/gemini 不采集);消费方对 nil 不设限。
	MCPServers []AgentMCPServer `json:"mcp_servers,omitempty"`
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestAgentToolMCPServersJSONShape -v`
Expected: PASS

- [ ] **Step 5: Commit(须授权)**

```bash
git add app/http/models/agent.go app/http/services/agent_mcp_inventory_test.go
git commit -m "新增:AgentTool 增加 mcp_servers 字段与线格式契约测试"
```

---

## Task 2: 脱敏助手(redactMCPCommand / redactMCPURL)

**Files:**
- Create: `app/http/services/agent_mcp_inventory.go`
- Test: `app/http/services/agent_mcp_inventory_test.go`(追加)

- [ ] **Step 1: 写失败测试**(追加到 agent_mcp_inventory_test.go)

```go
func TestRedactMCPCommand(t *testing.T) {
	// 短 token 原样保留
	if got := redactMCPCommand([]string{"uvx", "serena"}); got != "uvx serena" {
		t.Fatalf("short tokens kept: %q", got)
	}
	// 长 token(>32 且无空白)打码
	long := strings.Repeat("a", 40)
	got := redactMCPCommand([]string{"node", long})
	if !strings.HasPrefix(got, "node <redacted:40>") {
		t.Fatalf("long token redacted, got %q", got)
	}
	// 含空白的长参数不打码(自然语句),但整体截 512
	spacy := strings.Repeat("word ", 120)
	if len(redactMCPCommand([]string{"echo", spacy})) > 512 {
		t.Fatalf("command truncated to 512")
	}
}

func TestRedactMCPURL(t *testing.T) {
	if got := redactMCPURL("https://mcp.example.com/mcp"); got != "https://mcp.example.com/mcp" {
		t.Fatalf("plain url kept: %q", got)
	}
	long := strings.Repeat("k", 30)
	got := redactMCPURL("https://mcp.example.com/mcp?key=" + long)
	if strings.Contains(got, long) {
		t.Fatalf("long query value must be masked: %q", got)
	}
	if !strings.Contains(got, "key=%3Credacted%3E") && !strings.Contains(got, "key=<redacted>") {
		t.Fatalf("masked value present: %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestRedactMCP' -v`
Expected: FAIL,`undefined: redactMCPCommand`

- [ ] **Step 3: 实现**——新建 `app/http/services/agent_mcp_inventory.go`

```go
package services

// agent_mcp_inventory.go 采集设备级 MCP 服务器清单(claude/codex),脱敏后
// 挂到 AgentTool.MCPServers 随 tools[] 上报(spec:
// docs/superpowers/specs/2026-10-13-mcp-inventory-report-design.md)。
// env 值在任何路径都不进入返回结果;解析失败 fail-soft 返回 nil。

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	mcpNameMaxLen           = 64
	mcpCommandMaxLen        = 512
	mcpURLMaxLen            = 512
	mcpServerMaxCount       = 24
	mcpTokenRedactMinLen    = 32 // argv token 超过此长度且无空白 → 打码
	mcpURLQueryRedactMinLen = 24 // URL query 值超过此长度 → 打码
)

// redactMCPToken 把疑似密钥形态的 argv token 打码——宁可错杀:MCP args 里
// 带 API key 是常态,只有「短」或「含空白(自然语句)」才放行。
func redactMCPToken(token string) string {
	if len(token) > mcpTokenRedactMinLen && !strings.ContainsAny(token, " \t") {
		return fmt.Sprintf("<redacted:%d>", len(token))
	}
	return token
}

// redactMCPCommand 把 argv join 成命令行,逐 token 脱敏,整体截 512。
func redactMCPCommand(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, redactMCPToken(arg))
	}
	joined := strings.Join(parts, " ")
	if len(joined) > mcpCommandMaxLen {
		joined = joined[:mcpCommandMaxLen]
	}
	return joined
}

// redactMCPURL 打码 query 中超过阈值长度的值(键保留),整体截 512。
func redactMCPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		if len(raw) > mcpURLMaxLen {
			raw = raw[:mcpURLMaxLen]
		}
		return raw
	}
	q := u.Query()
	for key, vals := range q {
		for i, v := range vals {
			if len(v) > mcpURLQueryRedactMinLen {
				vals[i] = "<redacted>"
			}
		}
		q[key] = vals
	}
	u.RawQuery = q.Encode()
	out := u.String()
	if len(out) > mcpURLMaxLen {
		out = out[:mcpURLMaxLen]
	}
	return out
}

func truncateMCPName(name string) string {
	if len(name) > mcpNameMaxLen {
		return name[:mcpNameMaxLen]
	}
	return name
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestRedactMCP' -v`
Expected: PASS(测试文件需追加 import "strings";后续 Task 4/6/7 再陆续补 os/filepath/time/fmt,编译器会逐一提示)

- [ ] **Step 5: Commit(须授权)**

```bash
git add app/http/services/agent_mcp_inventory.go app/http/services/agent_mcp_inventory_test.go
git commit -m "新增:MCP 清单脱敏助手(长 token/URL query 打码)"
```

---

## Task 3: 收编数据源——readMCPServers 闭包升为包级函数

**Files:**
- Modify: `app/http/services/agent_ai_claude_policy.go:160-186`(claudeTierMCPArgs 内的 readMCPServers 闭包)
- Test: 既有 `agent_ai_claude_policy_test.go` 全绿即验收(行为不变重构,无新测试)

- [ ] **Step 1: 基线——先跑既有测试确认绿**

Run: `go test ./app/http/services/ -run TestClaudeTierMCPArgs -v`
Expected: PASS(记录基线)

- [ ] **Step 2: 把 claudeTierMCPArgs 里的 readMCPServers 闭包原样升为包级函数**

删除闭包定义(`readMCPServers := func(path string, dst map[string]interface{}) error {...}`),在 claudeTierMCPArgs 之前新增同体包级函数(注释一并带走):

```go
// readMCPServersFile 读一个 claude 形态 {"mcpServers":{...}} JSON 文件并入
// dst。文件缺失/不可读静默容忍;解析失败记日志且仅跳过该源(容忍,不降级)。
// 原 claudeTierMCPArgs 内闭包,收编为包级以便 MCP 清单采集复用同一数据源。
func readMCPServersFile(path string, dst map[string]interface{}) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err // absent/unreadable source is tolerated silently
	}
	var parsed struct {
		MCPServers map[string]interface{} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable MCP source %s: %v", path, err))
		return err // tolerate, don't degrade: skip this source only
	}
	for name, server := range parsed.MCPServers {
		dst[name] = server
	}
	return nil
}
```

claudeTierMCPArgs 内两处调用点(policy.go:184、:188 的 `readMCPServers(...)` → `readMCPServersFile(...)`;:186 的 readPluginMCPServers 是另一个函数,勿动)同步改名。

- [ ] **Step 3: 跑既有测试确认仍绿**

Run: `go test ./app/http/services/ -run TestClaudeTierMCPArgs -v`
Expected: PASS(与基线一致)

- [ ] **Step 4: Commit(须授权)**

```bash
git add app/http/services/agent_ai_claude_policy.go
git commit -m "新增:MCP 读取层收编——readMCPServers 闭包升为包级 readMCPServersFile(行为不变)"
```

---

## Task 4: Claude 清单解析(user + plugin → AgentMCPServer)

**Files:**
- Modify: `app/http/services/agent_ai_claude_policy.go`(readPluginMCPServers 拆出 enabledUserScopePluginInstalls)
- Modify: `app/http/services/agent_mcp_inventory.go`(追加解析)
- Test: `app/http/services/agent_mcp_inventory_test.go`(追加)

- [ ] **Step 1: 写失败测试**

追加到 agent_mcp_inventory_test.go:

```go
func TestAgentMCPServersFromClaudeRaw(t *testing.T) {
	raw := map[string]interface{}{
		"b-http": map[string]interface{}{"type": "http", "url": "https://x.example/mcp?token=" + strings.Repeat("t", 30)},
		"a-stdio": map[string]interface{}{
			"type":    "stdio",
			"command": "uvx",
			"args":    []interface{}{"--from", "git+https://example/serena", strings.Repeat("s", 40)},
		},
		"no-cmd":  map[string]interface{}{"type": "stdio"},
		"weird":   map[string]interface{}{"type": "websocket", "url": "wss://x"},
		"not-map": "oops",
	}
	out := agentMCPServersFromClaudeRaw(raw, "user")
	if len(out) != 2 {
		t.Fatalf("want 2 valid entries (sorted a-stdio,b-http), got %d: %+v", len(out), out)
	}
	// 名字按字典序,保证输出确定
	if out[0].Name != "a-stdio" || out[1].Name != "b-http" {
		t.Fatalf("want sorted names, got %+v", out)
	}
	if out[0].Transport != "stdio" || !strings.HasPrefix(out[0].Command, "uvx --from") {
		t.Fatalf("stdio entry wrong: %+v", out[0])
	}
	if strings.Contains(out[0].Command, strings.Repeat("s", 40)) {
		t.Fatalf("long arg token must be redacted: %q", out[0].Command)
	}
	if out[1].Transport != "http" || !strings.HasPrefix(out[1].URL, "https://x.example/mcp") {
		t.Fatalf("http entry wrong: %+v", out[1])
	}
	if strings.Contains(out[1].URL, strings.Repeat("t", 30)) {
		t.Fatalf("url query must be redacted: %q", out[1].URL)
	}
	for _, s := range out {
		if s.Source != "user" {
			t.Fatalf("source tag wrong: %+v", s)
		}
	}
}

func TestClaudeMCPServerInventoryIn(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(
		`{"mcpServers":{"context7":{"type":"http","url":"https://mcp.context7/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 启用一个插件 + 禁用一个插件(装了但 enabledPlugins 没开 → 不采)
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(`{
		"plugins":{"superplugin@market":[{"scope":"user","installPath":"`+filepath.Join(home, "plug-super")+`"}],
		           "sleepyplugin@market":[{"scope":"user","installPath":"`+filepath.Join(home, "plug-sleepy")+`"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(
		`{"enabledPlugins":{"superplugin@market":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, body := range map[string]string{
		"plug-super": `{"mcpServers":{"super-srv":{"command":"npx","args":["-y","super-mcp"]}}}`,
		"plug-sleepy": `{"mcpServers":{"sleepy-srv":{"command":"npx","args":["sleepy"]}}}`,
	} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, dir, ".mcp.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, stamps := claudeMCPServerInventoryIn(home)
	if len(out) != 2 {
		t.Fatalf("want user context7 + plugin super-srv, got %+v", out)
	}
	if out[0].Name != "context7" || out[0].Source != "user" {
		t.Fatalf("user entry wrong: %+v", out[0])
	}
	if out[1].Name != "plugin:superplugin:super-srv" || out[1].Source != "plugin" {
		t.Fatalf("plugin entry wrong: %+v", out[1])
	}
	// stamps 必须覆盖全部被读文件(含被禁插件?不——只覆盖实际读了的:
	// settings/registry/claude.json + 已启用插件 .mcp.json)
	if len(stamps) != 4 {
		t.Fatalf("want 4 stamps (claude.json+settings+registry+enabled plugin decl), got %d", len(stamps))
	}
}

func TestClaudeMCPServerInventoryInMissingHome(t *testing.T) {
	out, _ := claudeMCPServerInventoryIn(filepath.Join(t.TempDir(), "nonexistent"))
	if out != nil {
		t.Fatalf("empty home must yield nil, got %+v", out)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestAgentMCPServersFromClaudeRaw|TestClaudeMCPServerInventory' -v`
Expected: FAIL,`undefined: agentMCPServersFromClaudeRaw`

- [ ] **Step 3: 实现**

3a. `agent_ai_claude_policy.go`:把 readPluginMCPServers 的「注册表×enabledPlugins → user scope 安装路径」遍历拆出(原函数改为消费它,行为不变——键序仍 sort.Strings 确定性):

```go
// claudePluginInstall 是一个已启用 user scope 插件安装项。
type claudePluginInstall struct {
	key         string // "<plugin>@<market>"
	pluginName  string
	installPath string
}

// enabledUserScopePluginInstallsIn 按 <home> 下 installed_plugins.json(键
// 排序)× settings.json enabledPlugins 显式启用,枚举 user scope 安装项。
// 所有读取以传入 home 为根(可测试);容忍姿态与 readPluginMCPServers
// 一致:注册表缺失/畸形返回 nil(无事发生)。
func enabledUserScopePluginInstallsIn(home string) []claudePluginInstall {
	if home == "" {
		return nil
	}
	registryPath := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return nil
	}
	var registry struct {
		Plugins map[string][]map[string]interface{} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable plugin registry %s: %v", registryPath, err))
		return nil
	}
	enabledPlugins := claudeEnabledPluginsIn(home)
	pluginKeys := make([]string, 0, len(registry.Plugins))
	for key := range registry.Plugins {
		pluginKeys = append(pluginKeys, key)
	}
	sort.Strings(pluginKeys)
	var installs []claudePluginInstall
	for _, key := range pluginKeys {
		if !claudePluginEnabled(enabledPlugins, key) {
			continue
		}
		pluginName, _, _ := strings.Cut(key, "@")
		if pluginName == "" {
			continue
		}
		for _, install := range registry.Plugins[key] {
			scope, _ := install["scope"].(string)
			installPath, _ := install["installPath"].(string)
			if scope != "user" || installPath == "" {
				continue
			}
			installs = append(installs, claudePluginInstall{key: key, pluginName: pluginName, installPath: installPath})
		}
	}
	return installs
}

// enabledUserScopePluginInstalls 是无参包装(readPluginMCPServers 消费)。
func enabledUserScopePluginInstalls() []claudePluginInstall {
	return enabledUserScopePluginInstallsIn(agentHome())
}
```

同时把 claudeEnabledPlugins 拆 In 变体(settings.json 从传入 home 读,原行为不变):

```go
// claudeEnabledPluginsIn 同 claudeEnabledPlugins,但 home 由调用方给定
// (MCP 清单采集的可测试入口)。
func claudeEnabledPluginsIn(home string) map[string]interface{} {
	if home == "" {
		return nil
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		logger.Warn(fmt.Sprintf("claude-policy: unparseable settings %s: %v", settingsPath, err))
		return nil
	}
	enabled, _ := settings["enabledPlugins"].(map[string]interface{})
	return enabled
}

func claudeEnabledPlugins() map[string]interface{} {
	return claudeEnabledPluginsIn(agentHome())
}
```

(原 claudeEnabledPlugins 函数体删除,由上面两个替换;签名不变,既有调用方零改动。)`enabledUserScopePluginInstalls()` 与 `enabledUserScopePluginInstallsIn(home)` 的关系:无参版 `return enabledUserScopePluginInstallsIn(agentHome())`。

readPluginMCPServers 主体改为:

```go
func readPluginMCPServers(dst map[string]interface{}) int {
	added := 0
	for _, inst := range enabledUserScopePluginInstalls() {
		added += readPluginMCPDeclaration(inst.installPath, inst.pluginName, dst)
	}
	return added
}
```

(原函数头注释保留在 readPluginMCPServers 上;跑 `go test ./app/http/services/ -run TestReadPluginMCPServers -v` 确认既有插件测试全绿。)

3b. `agent_mcp_inventory.go` 追加:

```go
// agentMCPServersFromClaudeRaw 把 claude 形态的原始 mcpServers 条目转成
// 脱敏摘要。名字按字典序输出(确定性);非法/缺字段的条目逐条丢弃;
// 未知 transport 丢弃(fail-closed);数量截 mcpServerMaxCount。
func agentMCPServersFromClaudeRaw(named map[string]interface{}, source string) []models.AgentMCPServer {
	names := make([]string, 0, len(named))
	for name := range named {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]models.AgentMCPServer, 0, len(names))
	for _, name := range names {
		if len(out) >= mcpServerMaxCount {
			break
		}
		v, ok := named[name].(map[string]interface{})
		if !ok {
			continue
		}
		typ, _ := v["type"].(string)
		if typ == "" {
			typ = "stdio"
		}
		switch typ {
		case "stdio":
			command, _ := v["command"].(string)
			if command == "" {
				continue
			}
			argv := []string{command}
			if rawArgs, ok := v["args"].([]interface{}); ok {
				for _, a := range rawArgs {
					if s, ok := a.(string); ok {
						argv = append(argv, s)
					}
				}
			}
			out = append(out, models.AgentMCPServer{
				Name: truncateMCPName(name), Transport: "stdio",
				Command: redactMCPCommand(argv), Source: source,
			})
		case "http", "sse":
			u, _ := v["url"].(string)
			if u == "" {
				continue
			}
			out = append(out, models.AgentMCPServer{
				Name: truncateMCPName(name), Transport: typ,
				URL: redactMCPURL(u), Source: source,
			})
		default:
			continue // 未知 transport:宁可少报(fail-closed)
		}
	}
	return out
}

// claudeMCPServerInventoryIn 采集 <home> 下的 Claude 设备级 MCP 清单:
// ~/.claude.json 顶层 mcpServers(source=user)+ 已启用插件 MCP
// (source=plugin)。项目级 .mcp.json 不采集(随 cwd 变化,非设备能力)。
// 返回值同时给出本次实际读取的全部文件 stamp(供缓存失效判定——复合源
// 任一变更即失效)。
func claudeMCPServerInventoryIn(home string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
	claudeJSON := filepath.Join(home, ".claude.json")
	settingsJSON := filepath.Join(home, ".claude", "settings.json")
	registryJSON := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if _, err := os.Stat(claudeJSON); err != nil {
		if _, err2 := os.Stat(registryJSON); err2 != nil {
			return nil, nil // 无任何 claude 配置:无事发生
		}
	}
	stamps := []agentMCPFileStamp{
		stampAgentMCPFile(claudeJSON),
		stampAgentMCPFile(settingsJSON),
		stampAgentMCPFile(registryJSON),
	}
	userRaw := map[string]interface{}{}
	_ = readMCPServersFile(claudeJSON, userRaw)
	pluginRaw := map[string]interface{}{}
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		declPath := filepath.Join(inst.installPath, ".mcp.json")
		stamps = append(stamps, stampAgentMCPFile(declPath))
		readPluginMCPDeclaration(inst.installPath, inst.pluginName, pluginRaw)
	}
	out := agentMCPServersFromClaudeRaw(userRaw, "user")
	out = append(out, agentMCPServersFromClaudeRaw(pluginRaw, "plugin")...)
	if len(out) > mcpServerMaxCount {
		out = out[:mcpServerMaxCount]
	}
	if len(out) == 0 {
		return nil, stamps
	}
	return out, stamps
}
```

注意:为可测试,enabledUserScopePluginInstalls 与 claudeEnabledPlugins 都拆了 In 变体——**In 变体内所有读取(settings.json/registry/声明文件)都以传入 home 为根**,不得回落 agentHome(),否则测试 fixture 不生效。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestAgentMCPServersFromClaudeRaw|TestClaudeMCPServerInventory|TestReadPluginMCPServers|TestClaudeTierMCPArgs' -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit(须授权)**

```bash
git add app/http/services/agent_ai_claude_policy.go app/http/services/agent_mcp_inventory.go app/http/services/agent_mcp_inventory_test.go
git commit -m "新增:Claude 设备级 MCP 清单解析(user+plugin,项目级不采)"
```

---

## Task 5: Codex 清单解析(config.toml)

**Files:**
- Modify: `app/http/services/agent_mcp_inventory.go`
- Test: `app/http/services/agent_mcp_inventory_test.go`(追加)

- [ ] **Step 1: 写失败测试**

```go
func TestCodexMCPServerInventoryIn(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configTOML := `
[mcp_servers.serena]
command = "uvx"
args = ["--from", "git+https://example/serena", "serena", "start-mcp-server"]
env = { SECRET_TOKEN = "` + strings.Repeat("x", 40) + `" }

[mcp_servers.webmcp]
url = "https://mcp.example/stream"

[mcp_servers.broken]
url = ""
`
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(configTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	out, stamps := codexMCPServerInventoryIn(home)
	if len(out) != 2 {
		t.Fatalf("want serena+webmcp, got %+v", out)
	}
	if out[0].Name != "serena" || out[0].Transport != "stdio" {
		t.Fatalf("serena wrong: %+v", out[0])
	}
	if !strings.HasPrefix(out[0].Command, "uvx --from") {
		t.Fatalf("serena command wrong: %q", out[0].Command)
	}
	if strings.Contains(out[0].Command, strings.Repeat("x", 40)) {
		t.Fatalf("env 值绝不能出现在 command: %q", out[0].Command)
	}
	if out[1].Name != "webmcp" || out[1].Transport != "http" || out[1].URL != "https://mcp.example/stream" {
		t.Fatalf("webmcp wrong: %+v", out[1])
	}
	if len(stamps) != 1 {
		t.Fatalf("want 1 stamp, got %d", len(stamps))
	}
	// 缺文件也要交指纹(out==nil 且 stamps len==1),否则缓存永驻空结果
	missingOut, missingStamps := codexMCPServerInventoryIn(t.TempDir())
	if missingOut != nil || len(missingStamps) != 1 {
		t.Fatalf("missing config must yield nil + 1 stamp, got %+v %+v", missingOut, missingStamps)
	}
}

func TestCodexMCPServerInventoryInMalformed(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("not [ valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := codexMCPServerInventoryIn(home)
	if out != nil {
		t.Fatalf("malformed toml must fail soft, got %+v", out)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestCodexMCPServerInventory -v`
Expected: FAIL,`undefined: codexMCPServerInventoryIn`

- [ ] **Step 3: 实现**(agent_mcp_inventory.go 追加)

```go
// codexMCPConfig 对应 ~/.codex/config.toml 的 [mcp_servers.<name>] 表。
// Env 读取即弃:字段只是把 env 消化掉,值永不进入输出。
type codexMCPConfig struct {
	Command string                 `toml:"command"`
	Args    []string               `toml:"args"`
	URL     string                 `toml:"url"`
	Env     map[string]interface{} `toml:"env"`
}

type codexConfigFile struct {
	MCPServers map[string]codexMCPConfig `toml:"mcp_servers"`
}

// codexMCPServerInventoryIn 采集 ~/.codex/config.toml 的 MCP 清单。
// command(+args)→ stdio;url → 统一归类 http(codex 配置不区分 sse/http);
// 两者皆无的条目丢弃;畸形 TOML fail-soft 返回 nil。
func codexMCPServerInventoryIn(home string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
	configPath := filepath.Join(home, ".codex", "config.toml")
	stamp := stampAgentMCPFile(configPath)
	if !stamp.exists {
		// 缺失也要交指纹,否则缓存永驻空结果(配置后来出现感知不到)
		return nil, []agentMCPFileStamp{stamp}
	}
	var cfg codexConfigFile
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		logger.Warn(fmt.Sprintf("mcp-inventory: unparseable codex config %s: %v", configPath, err))
		return nil, []agentMCPFileStamp{stamp}
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]models.AgentMCPServer, 0, len(names))
	for _, name := range names {
		if len(out) >= mcpServerMaxCount {
			break
		}
		srv := cfg.MCPServers[name]
		switch {
		case srv.URL != "":
			out = append(out, models.AgentMCPServer{
				Name: truncateMCPName(name), Transport: "http",
				URL: redactMCPURL(srv.URL), Source: "config",
			})
		case srv.Command != "":
			out = append(out, models.AgentMCPServer{
				Name: truncateMCPName(name), Transport: "stdio",
				Command: redactMCPCommand(append([]string{srv.Command}, srv.Args...)),
				Source:  "config",
			})
		default:
			continue
		}
	}
	if len(out) == 0 {
		return nil, []agentMCPFileStamp{stamp}
	}
	return out, []agentMCPFileStamp{stamp}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run TestCodexMCPServerInventory -v`
Expected: PASS

- [ ] **Step 5: Commit(须授权)**

```bash
git add app/http/services/agent_mcp_inventory.go app/http/services/agent_mcp_inventory_test.go
git commit -m "新增:Codex config.toml MCP 清单解析(url 统一归 http,env 读取即弃)"
```

---

## Task 6: 缓存与调度(agentMCPServersFor)

**Files:**
- Modify: `app/http/services/agent_mcp_inventory.go`
- Test: `app/http/services/agent_mcp_inventory_test.go`(追加)

- [ ] **Step 1: 写失败测试**

```go
func TestCachedAgentMCPServersInvalidation(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(configPath, []byte("[mcp_servers.a]\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parseCalls := 0
	parse := func(h string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
		parseCalls++
		return codexMCPServerInventoryIn(h)
	}
	first := cachedAgentMCPServers(home, "codex", parse)
	second := cachedAgentMCPServers(home, "codex", parse)
	if parseCalls != 1 {
		t.Fatalf("unchanged files must hit cache, parseCalls=%d", parseCalls)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Name != "a" {
		t.Fatalf("cached results wrong: %+v %+v", first, second)
	}
	// 文件变更 → 缓存失效重解析
	if err := os.WriteFile(configPath, []byte("[mcp_servers.b]\ncommand = \"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// mtime 粒度问题:确保 mtime 变化
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(configPath, future, future); err != nil {
		t.Fatal(err)
	}
	third := cachedAgentMCPServers(home, "codex", parse)
	if parseCalls != 2 {
		t.Fatalf("changed file must invalidate cache, parseCalls=%d", parseCalls)
	}
	if third[0].Name != "b" {
		t.Fatalf("fresh parse result wrong: %+v", third)
	}
}

func TestAgentMCPServersForDispatch(t *testing.T) {
	// 非采集对象一律 nil(不触达文件系统)
	for _, id := range []string{"opencode", "pi", "gemini", "", "  "} {
		if got := agentMCPServersFor(id); got != nil {
			t.Fatalf("tool %q must yield nil, got %+v", id, got)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestCachedAgentMCPServers|TestAgentMCPServersForDispatch' -v`
Expected: FAIL,`undefined: cachedAgentMCPServers`

- [ ] **Step 3: 实现**(agent_mcp_inventory.go 追加)

```go
// agentMCPFileStamp 是缓存失效判定的文件指纹。零值 comparable,直接 == 比较。
type agentMCPFileStamp struct {
	path    string
	exists  bool
	modTime time.Time
	size    int64
}

func stampAgentMCPFile(path string) agentMCPFileStamp {
	if st, err := os.Stat(path); err == nil {
		return agentMCPFileStamp{path: path, exists: true, modTime: st.ModTime(), size: st.Size()}
	}
	return agentMCPFileStamp{path: path}
}

type agentMCPInventoryCacheEntry struct {
	stamps []agentMCPFileStamp
	result []models.AgentMCPServer
}

var (
	agentMCPInventoryMu     sync.Mutex
	agentMCPInventoryByHome = map[string]map[string]agentMCPInventoryCacheEntry{} // home -> toolID -> entry
)

// agentMCPStampsUnchanged 对 stamp 清单逐项重 stat;任一指纹变化(出现/
// 消失/mtime/size)即失效。复合源(claude 多文件)任一变更都会触发重解析。
func agentMCPStampsUnchanged(stamps []agentMCPFileStamp) bool {
	for _, s := range stamps {
		if stampAgentMCPFile(s.path) != s {
			return false
		}
	}
	return true
}

// cachedAgentMCPServers 以「home+toolID」为槽缓存解析结果;命中时返回副本
// (防调用方改写缓存)。parse 只在缓存失效时被调。
func cachedAgentMCPServers(home, toolID string, parse func(home string) ([]models.AgentMCPServer, []agentMCPFileStamp)) []models.AgentMCPServer {
	agentMCPInventoryMu.Lock()
	defer agentMCPInventoryMu.Unlock()
	byTool := agentMCPInventoryByHome[home]
	if byTool == nil {
		byTool = map[string]agentMCPInventoryCacheEntry{}
		agentMCPInventoryByHome[home] = byTool
	}
	entry, ok := byTool[toolID]
	if ok && agentMCPStampsUnchanged(entry.stamps) {
		return append([]models.AgentMCPServer(nil), entry.result...)
	}
	result, stamps := parse(home)
	byTool[toolID] = agentMCPInventoryCacheEntry{stamps: stamps, result: result}
	return append([]models.AgentMCPServer(nil), result...)
}

// agentMCPServersFor 是 detectAgentToolsWith 的 MCP 采集入口:仅 claude/
// claudecode/codex 采集(其余 CLI 返回 nil);agentHome 不可得返回 nil。
func agentMCPServersFor(toolID string) []models.AgentMCPServer {
	home := agentHome()
	if home == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(toolID)) {
	case "claude", "claudecode":
		return cachedAgentMCPServers(home, "claude", claudeMCPServerInventoryIn)
	case "codex":
		return cachedAgentMCPServers(home, "codex", codexMCPServerInventoryIn)
	default:
		return nil
	}
}
```

同时给 `claudeMCPServerInventoryIn` 的函数签名保持不变(它已返回 `(result, stamps)`,直接可作 parse 传入)。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/services/ -run 'TestCachedAgentMCPServers|TestAgentMCPServersForDispatch' -v`
Expected: PASS

- [ ] **Step 5: Commit(须授权)**

```bash
git add app/http/services/agent_mcp_inventory.go app/http/services/agent_mcp_inventory_test.go
git commit -m "新增:MCP 清采集缓存(复合源指纹失效)与 CLI 调度"
```

---

## Task 7: 接线 detectAgentToolsWith

**Files:**
- Modify: `app/http/services/agent_service.go:1473-1530`(detectAgentToolsWith 尾部)
- Test: `app/http/services/agent_mcp_inventory_test.go`(追加)

- [ ] **Step 1: 写失败测试**

```go
func TestDetectAgentToolsWithFillsMCPServers(t *testing.T) {
	fake := map[string][]models.AgentMCPServer{
		// claudecode 单独映射:接线循环按 defs[i].ID 原样调 mcpInventoryFor,
		// 「别名同源」的归属在 agentMCPServersFor 的 switch 里,fake 绕过它。
		"claude":     {{Name: "ctx", Transport: "http", URL: "https://x"}},
		"claudecode": {{Name: "ctx", Transport: "http", URL: "https://x"}},
		"codex":      {{Name: "serena", Transport: "stdio", Command: "uvx"}},
	}
	orig := mcpInventoryFor
	mcpInventoryFor = func(toolID string) []models.AgentMCPServer { return fake[toolID] }
	t.Cleanup(func() { mcpInventoryFor = orig })

	tools := detectAgentToolsWith(func(cmd string) (string, error) {
		return "/fake/bin/" + cmd, nil
	})
	byID := map[string]models.AgentTool{}
	for _, tool := range tools {
		byID[tool.ID] = tool
	}
	if len(byID["claude"].MCPServers) != 1 {
		t.Fatalf("claude must carry mcp list")
	}
	if len(byID["claudecode"].MCPServers) != 1 {
		t.Fatalf("claudecode alias must share claude list")
	}
	if len(byID["codex"].MCPServers) != 1 {
		t.Fatalf("codex must carry mcp list")
	}
	for _, id := range []string{"opencode", "pi", "gemini"} {
		if byID[id].MCPServers != nil {
			t.Fatalf("%s must not carry mcp list", id)
		}
	}
}

func TestDetectAgentToolsWithSkipsUnavailableMCPServers(t *testing.T) {
	orig := mcpInventoryFor
	mcpInventoryFor = func(toolID string) []models.AgentMCPServer {
		return []models.AgentMCPServer{{Name: "x", Transport: "stdio", Command: "x"}}
	}
	t.Cleanup(func() { mcpInventoryFor = orig })

	// 无任何 CLI 可用 → 不采集 MCP(能力=CLI 存在才有意义)
	tools := detectAgentToolsWith(func(cmd string) (string, error) {
		return "", fmt.Errorf("not found")
	})
	for _, tool := range tools {
		if tool.MCPServers != nil {
			t.Fatalf("unavailable tool %s must not carry mcp list", tool.ID)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run TestDetectAgentToolsWith -v`
Expected: FAIL,`undefined: mcpInventoryFor`

- [ ] **Step 3: 实现**

3a. agent_mcp_inventory.go 追加注入点:

```go
// mcpInventoryFor 是 detectAgentToolsWith 的 MCP 采集注入点(测试替换)。
var mcpInventoryFor = agentMCPServersFor
```

3b. agent_service.go 的 detectAgentToolsWith,在 Version/Efforts 循环之后、`return defs` 之前追加:

```go
	// MCP 能力清单:仅在 CLI 可用时采集(配置挂在不存在的 CLI 上没有意义);
	// claudecode 别名与 claude 同源同清单;opencode/pi/gemini 不采集。
	for i := range defs {
		if !defs[i].Available {
			continue
		}
		if mcp := mcpInventoryFor(defs[i].ID); len(mcp) > 0 {
			defs[i].MCPServers = mcp
		}
	}
```

(测试文件需 import fmt。)

- [ ] **Step 4: 跑测试确认通过,再跑全包回归**

Run: `go test ./app/http/services/ -run TestDetectAgentToolsWith -v`
Expected: PASS

Run: `go test ./app/http/services/`
Expected: PASS(若遇 agent_ai 子进程 2s/5s 超时 flake,单包复跑即绿——预存,勿当回归)

- [ ] **Step 5: Commit(须授权)**

```bash
git add app/http/services/agent_service.go app/http/services/agent_mcp_inventory.go app/http/services/agent_mcp_inventory_test.go
git commit -m "新增:detectAgentToolsWith 填充 MCP 能力清单(claude/claudecode/codex)"
```

---

## Task 8: 服务端仓库准备 + types/schemas

> **【2026-10-14 作废】** 本任务与 Task 9 已由并行会话在服务端主仓直接实现并合入 main
> (commit 3eb11fa,merge 2069464「设备能力面——MCP 清单双闸」):agentToolSchema 增
> mcpServerSchema(name≤64/transport enum/command·url≤512/env 剥除/数组 max24)+ 
> normalizeAgentTools 白名单透传(五字段/截 512/截 24),与本计划 §4.1 双闸分工一致。
> 差异:server 侧 source max(16)(计划为 64;客户端实际值 user/plugin/config ≤6 字符,无冲突)。
> 以下原文仅存档。

**Files:**
- Worktree: `git -C ~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer worktree add ../AliangPhoneServer-wt-mcp-report -b feat/mcp-inventory-report main`
- Modify: `server/src/types.ts:154-172`(AgentTool 类型)
- Modify: `server/src/schemas.ts:14-30`(agentToolSchema)
- Test: `server/test/modules/device/normalize.test.ts`(追加 schema 用例)

- [ ] **Step 1: 建服务端 worktree**

```bash
git -C /Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer status --porcelain
# 期望:空(干净)再继续
git -C /Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer worktree add ../AliangPhoneServer-wt-mcp-report -b feat/mcp-inventory-report main
```

后续文件路径均基于 `/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer-wt-mcp-report/`。

- [ ] **Step 2: 写失败测试**(server/test/modules/device/normalize.test.ts 顶部 import 区追加 agentToolSchema,文件末尾追加)

```ts
import { agentToolSchema } from '../../../src/schemas.js';

describe('agentToolSchema mcp_servers', () => {
  it('accepts well-formed mcp_servers', () => {
    const parsed = agentToolSchema.parse({
      id: 'claude',
      mcp_servers: [
        { name: 'serena', transport: 'stdio', command: 'uvx serena', source: 'user' },
        { name: 'web', transport: 'http', url: 'https://x/mcp' },
      ],
    });
    expect(parsed.mcp_servers).toHaveLength(2);
    expect(parsed.mcp_servers?.[0]).toMatchObject({ name: 'serena', transport: 'stdio' });
  });

  it('rejects invalid transport and over-long name', () => {
    expect(
      agentToolSchema.safeParse({ id: 'x', mcp_servers: [{ name: 'a', transport: 'ws' }] }).success,
    ).toBe(false);
    expect(
      agentToolSchema.safeParse({ id: 'x', mcp_servers: [{ name: 'n'.repeat(65), transport: 'stdio' }] }).success,
    ).toBe(false);
  });

  it('is optional — legacy agents without the field pass', () => {
    expect(agentToolSchema.parse({ id: 'x' }).mcp_servers).toBeUndefined();
  });
});
```

- [ ] **Step 3: 跑测试确认失败**

Run(仓库根): `npx vitest run server/test/modules/device/normalize.test.ts`
Expected: FAIL,import 报 `agentToolSchema` 不存在导出成员或 mcp_servers 断言失败

- [ ] **Step 4: 实现**

4a. `server/src/types.ts` 在 AgentTool 类型(:154)前新增,并在 AgentTool 里加字段:

```ts
export type AgentMCPServer = {
  name: string;
  transport: 'stdio' | 'http' | 'sse';
  /** stdio 形态的 argv join 命令行(已由 agent 脱敏)。 */
  command?: string;
  /** http/sse 端点(agent 侧已对可疑 query 值打码)。 */
  url?: string;
  /** 来源:claude 为 user|plugin,codex 为 config。 */
  source?: string;
};
```

AgentTool 追加:

```ts
  /** 设备级 MCP 服务器清单(agent 采集,脱敏后)。缺省=未采集/不适用。 */
  mcp_servers?: AgentMCPServer[];
```

4b. `server/src/schemas.ts` 在 agentToolSchema 之前新增:

```ts
// Agent 上报的 MCP 服务器摘要(脱敏由 agent 侧完成,服务端只做结构上限)。
export const agentMCPServerSchema = z.object({
  name: z.string().min(1).max(64),
  transport: z.enum(['stdio', 'http', 'sse']),
  command: z.string().max(512).optional(),
  url: z.string().max(512).optional(),
  source: z.string().max(64).optional()
});
```

agentToolSchema 在 efforts 之后追加:

```ts
  // MCP 服务器清单(设备级能力,spec 2026-10-13-mcp-inventory-report)。
  // 上限与 normalize.ts 的 TOOL_MCP_* 一致;注册/HTTP 同步路径的唯一闸门
  // 就是本 schema(hello 另走 normalizeAgentTools 白名单),两处都须维护。
  mcp_servers: z.array(agentMCPServerSchema).max(24).optional()
```

- [ ] **Step 5: 跑测试确认通过**

Run: `npx vitest run server/test/modules/device/normalize.test.ts`
Expected: PASS

- [ ] **Step 6: Commit(须授权,在服务端 worktree)**

```bash
git add server/src/types.ts server/src/schemas.ts server/test/modules/device/normalize.test.ts
git commit -m "新增:agentToolSchema 接收 mcp_servers(注册/同步路径闸门)"
```

---

## Task 9: 服务端 normalize 白名单(hello 路径)

> **【2026-10-14 作废】** 同 Task 8:并行会话已在服务端 main 落地(3eb11fa)。以下原文仅存档。

**Files:**
- Modify: `server/src/modules/device/normalize.ts`(TOOL_* 常量区 + normalizeAgentTools)
- Test: `server/test/modules/device/normalize.test.ts`(追加)

- [ ] **Step 1: 写失败测试**

```ts
describe('normalizeAgentMCPServers (via normalizeAgentTools)', () => {
  const mcpTool = (mcp: unknown) => normalizeAgentTools([{ id: 'claude', mcp_servers: mcp }]);

  it('maps and whitelists fields', () => {
    const out = mcpTool([
      { name: 'serena', transport: 'stdio', command: 'uvx serena', url: 'http://ignored-for-stdio', source: 'user', rogue: 'x' },
    ]);
    expect(out?.[0].mcp_servers).toEqual([
      { name: 'serena', transport: 'stdio', command: 'uvx serena', url: 'http://ignored-for-stdio', source: 'user' },
    ]);
  });

  it('drops entries without name or unknown transport', () => {
    expect(mcpTool([{ transport: 'stdio' }, { name: 'a', transport: 'ws' }, 'oops'])?.[0].mcp_servers).toBeUndefined();
  });

  it('caps at 24 entries and truncates long strings', () => {
    const many = Array.from({ length: 30 }, (_, i) => ({ name: `s${i}`, transport: 'stdio', command: 'c'.repeat(600) }));
    const out = mcpTool(many);
    expect(out?.[0].mcp_servers).toHaveLength(24);
    expect(out?.[0].mcp_servers?.[0].command).toHaveLength(512);
  });

  it('undefined when absent', () => {
    const out = normalizeAgentTools([{ id: 'codex' }]);
    expect(out?.[0].mcp_servers).toBeUndefined();
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npx vitest run server/test/modules/device/normalize.test.ts`
Expected: FAIL,mcp_servers 为 undefined

- [ ] **Step 3: 实现**

3a. normalize.ts 常量区(TOOL_EFFORT_MAX_ITEMS 之后)追加:

```ts
const TOOL_MCP_NAME_MAX_CHARS = 64;
const TOOL_MCP_FIELD_MAX_CHARS = 512;
const TOOL_MCP_MAX_ITEMS = 24;
const TOOL_MCP_TRANSPORTS = new Set(['stdio', 'http', 'sse']);
```

3b. 新增函数(normalizeAgentTools 之前):

```ts
export function normalizeAgentMCPServers(
  value: unknown
): AgentMCPServer[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const servers: AgentMCPServer[] = [];
  for (const item of value) {
    if (servers.length >= TOOL_MCP_MAX_ITEMS) break;
    if (!item || typeof item !== 'object') continue;
    const raw = item as Record<string, unknown>;
    const name = optionalBoundedString(raw.name, TOOL_MCP_NAME_MAX_CHARS);
    const transport = optionalString(raw.transport);
    if (!name || !transport || !TOOL_MCP_TRANSPORTS.has(transport)) continue;
    servers.push({
      name,
      transport: transport as AgentMCPServer['transport'],
      command: optionalBoundedString(raw.command, TOOL_MCP_FIELD_MAX_CHARS),
      url: optionalBoundedString(raw.url, TOOL_MCP_FIELD_MAX_CHARS),
      source: optionalBoundedString(raw.source, TOOL_MCP_NAME_MAX_CHARS)
    });
  }
  return servers.length ? servers : undefined;
}
```

3c. `normalizeAgentTools` 的 tools.push 里(commands 之后)追加一行:

```ts
      mcp_servers: normalizeAgentMCPServers(raw.mcp_servers),
```

3d. 文件顶部 type import 追加 `AgentMCPServer`。

- [ ] **Step 4: 跑测试确认通过**

Run: `npx vitest run server/test/modules/device/normalize.test.ts`
Expected: PASS

- [ ] **Step 5: Commit(须授权)**

```bash
git add server/src/modules/device/normalize.ts server/test/modules/device/normalize.test.ts
git commit -m "新增:normalizeAgentTools 白名单透传 mcp_servers(hello 路径)"
```

---

## Task 10: 全量回归 + 收尾

- [ ] **Step 1: 客户端全包回归**

Run(客户端 worktree): `go build ./... && go test ./app/http/services/ ./app/http/models/`
Expected: PASS(遇预存 flake 单包复跑)

- [ ] **Step 2: 服务端全量测试**

Run(服务端 worktree): `npx vitest run`
Expected: PASS

- [ ] **Step 3: 手动冒烟(可选,真实设备验证时做)**

在开发机上运行 agent,观察 hello payload(dev 日志)中 tools[] 里 claude 的 mcp_servers 是否为本机真实清单;`curl -s 127.0.0.1:56433/api/agent/tools | jq` 目检脱敏效果。

- [ ] **Step 4: 汇报状态**

向用户汇报:两侧分支各自的 commit 列表、测试结果、部署顺序提醒(先服务端后客户端,实际任意顺序安全)。
