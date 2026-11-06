# AliangCode 内核 LLM 自定义设置 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 仪表盘新增 AliangCode 内核 LLM 设置(Base URL + API Key),本机全局生效,显式配置优先于 claudecode 继承链。

**Architecture:** `customer.agentcore_llm` 字段走既有 `/api/config/customer` 通道;`agentCoreBaseURL`/`agentCoreAuthToken` 顶部插显式层(显式 → env/settings 继承 → 兜底);显式 BaseURL 绕过回环闸,非回环必须配 key(保存端+读取端双保险);自定义端点拨测不走 56432 自愈。

**Tech Stack:** Go(`processor/config` + `app/http/services`)+ Vue 3(Options API)+ vitest。

**Spec:** `docs/superpowers/specs/2026-10-28-agentcore-llm-settings-design.md`

**⚠️ 全局约束(执行时必读):**

1. **提交需用户明示同意**(项目 CLAUDE.md 规则)。每个任务末尾的 commit 步骤在执行会话中先征得用户同意再执行;未获同意则把改动留在工作区,勾选跳过该步。提交标题用全中文「新增:/修复:」格式,不用 feat:/fix:。
2. **白名单 4 处逻辑 + 3 处错误文案必须同一提交内全部扩展**(Task 1+2),漏一处 = 保存 400 或字段被静默剥离。
3. 测试只跑**目标包**(services 包有预存子进程超时 flake、`-race` 有预存失败,见仓库记忆),不加 `-race`、不跑全仓。
4. worktree 内 gopls 误报一律无视,以 `go build`/`go test` 实际结果为准。

---

### Task 0: worktree 准备

**Files:** 无代码改动。

- [ ] **Step 1: 建 worktree**(用户工作流:所有开发在 worktree,不直接改 master 工作区)

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-llm-settings -b feature/agentcore-llm-settings master
```

- [ ] **Step 2: 拷贝 dist 占位**(新 worktree 首次 go build 必需,embed 占位)

```bash
cp -R /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/dist \
      /Users/mac/MyProgram/GoProgram/nursor/alianggate-llm-settings/app/website/dist
```

- [ ] **Step 3: 验证基线构建**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-llm-settings && go build ./...
```

Expected: 构建成功,无输出。

---

### Task 1: Config 模型与取值器(`processor/config`)

**Files:**
- Modify: `processor/config/types.go:518-527`(CustomerConfig 加字段)、`types.go:877`(错误文案)、`types.go:760+`(Config.Validate 加校验)、`types.go:914+`(白名单 switch)
- Create: `processor/config/agentcore_llm_config_test.go`

- [ ] **Step 1: 写失败测试**

新建 `processor/config/agentcore_llm_config_test.go`(package 与 `types_test.go` 一致,先看其首行 package 名):

```go
package config

import (
	"strings"
	"testing"
)

func TestEffectiveAgentCoreLLM(t *testing.T) {
	if got := (&Config{}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("空 Config → %v, want nil", got)
	}
	if got := (&Config{Customer: &CustomerConfig{}}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("无字段 → %v, want nil", got)
	}
	if got := (&Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{}}}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("未启用 → %v, want nil", got)
	}
	if got := (&Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{Enabled: true}}}).EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("启用但 URL 空 → %v, want nil(整层关闭)", got)
	}
	cfg := &Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{
		Enabled: true, BaseURL: " https://gw.example.com ", APIKey: "sk-x",
	}}}
	got := cfg.EffectiveAgentCoreLLM()
	if got == nil || got.BaseURL != "https://gw.example.com" {
		t.Errorf("→ %+v, want trim 后的配置副本", got)
	}
	// 返回副本:改副本不影响原配置
	got.BaseURL = " mutated "
	if cfg.Customer.AgentCoreLLM.BaseURL != " https://gw.example.com " {
		t.Error("EffectiveAgentCoreLLM 必须返回副本,不能暴露内部指针")
	}
}

// 读取端防御(spec §3.1):手改配置文件绕过保存校验时,非法形态整层关闭+Warn,
// 绝不把坏配置放进读取链(否则复刻「远端缺 key 静默 403」的谜团)。
func TestEffectiveAgentCoreLLMRejectsInvalid(t *testing.T) {
	noKey := &Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", // 远端+无 key:非法
	}}}
	if got := noKey.EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("远端缺 key 应整层关闭, got %+v", got)
	}
	malformed := &Config{Customer: &CustomerConfig{AgentCoreLLM: &AgentCoreLLMConfig{
		Enabled: true, BaseURL: "not a url",
	}}}
	if got := malformed.EffectiveAgentCoreLLM(); got != nil {
		t.Errorf("URL 畸形应整层关闭, got %+v", got)
	}
}

func TestAgentCoreLLMValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *AgentCoreLLMConfig
		wantErr string // 空串 = 期望通过
	}{
		{"nil 不拦", nil, ""},
		{"未启用不拦", &AgentCoreLLMConfig{}, ""},
		{"启用+URL 空", &AgentCoreLLMConfig{Enabled: true}, "base_url"},
		{"启用+URL 畸形", &AgentCoreLLMConfig{Enabled: true, BaseURL: "not a url"}, "base_url"},
		{"启用+回环+无 key 合法", &AgentCoreLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:8082"}, ""},
		{"启用+localhost+无 key 合法", &AgentCoreLLMConfig{Enabled: true, BaseURL: "http://localhost:8082"}, ""},
		{"启用+远端+无 key 拒", &AgentCoreLLMConfig{Enabled: true, BaseURL: "https://gw.example.com"}, "api_key"},
		{"启用+远端+有 key 合法", &AgentCoreLLMConfig{Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-x"}, ""},
	}
	for _, tc := range cases {
		err := tc.cfg.Validate()
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: 未期望错误 %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: got %v, want 含 %q", tc.name, err, tc.wantErr)
		}
	}
}

// 白名单 switch(types.go extractCustomerUnknownFields)必须放行 agentcore_llm,
// 否则加载即报 "customer.agentcore_llm is forbidden"。
func TestCustomerUnknownKeyAllowsAgentCoreLLM(t *testing.T) {
	root := map[string]json.RawMessage{
		"customer": json.RawMessage(`{"agentcore_llm":{"enabled":true,"base_url":"http://127.0.0.1:8082"}}`),
	}
	unknown, _, err := extractCustomerUnknownFields(root)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("agentcore_llm 被当成未知键: %v", unknown)
	}
}
```

(测试文件需 `import "encoding/json"`。)

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./processor/config/ -run 'TestEffectiveAgentCoreLLM|TestAgentCoreLLMValidate|TestCustomerUnknownKeyAllowsAgentCoreLLM' -v
```

Expected: FAIL(undefined: AgentCoreLLMConfig 等)。

- [ ] **Step 3: 实现**

`types.go` CustomerConfig(:518)追加字段:

```go
type CustomerConfig struct {
	// ...既有字段...
	CustomEnvVars *CustomEnvVarsConfig              `json:"custom_env_vars,omitempty"`
	GeoIP         *CustomerGeoIPSettings            `json:"geoip,omitempty"`
	// AgentCoreLLM 是 AliangCode 内核的显式 LLM 端点设置(customer.agentcore_llm)。
	// nil 或 Enabled=false 时整层不生效,行为回落继承链(env/settings→兜底)。
	AgentCoreLLM *AgentCoreLLMConfig `json:"agentcore_llm,omitempty"`
}

// AgentCoreLLMConfig 显式 LLM 端点。仅支持 Anthropic Messages 兼容端点
// (内核协议见 app/agentcore/client.go);BaseURL 远端时必须配 APIKey——
// 远端直连没有平台 relay 注入凭据,裸 401 无法自查。
type AgentCoreLLMConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

// EffectiveAgentCoreLLM 返回显式设置的副本;未配置/未启用/URL 空/校验不过返回
// nil,调用方按整层关闭处理。形状对齐 EffectiveCustomEnvVars 的 nil-guard。
func (c *Config) EffectiveAgentCoreLLM() *AgentCoreLLMConfig {
	if c == nil || c.Customer == nil || c.Customer.AgentCoreLLM == nil {
		return nil
	}
	llm := c.Customer.AgentCoreLLM
	if !llm.Enabled || strings.TrimSpace(llm.BaseURL) == "" {
		return nil
	}
	cfg := *llm
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	// 读取端防御(spec §3.1):手改配置文件可绕过保存校验,这里兜底——非法
	// 形态整层关闭 + Warn,不静默、不 crash、不把坏配置放进读取链。每回合
	// 多次读取会重复 Warn 数行,属可接受噪声(用户必须修配置),不做去重。
	if err := cfg.Validate(); err != nil {
		logger.Warn("agentcore_llm 配置非法,本层关闭: " + err.Error())
		return nil
	}
	return &cfg
}

// Validate 保存通道(Config.Validate)调用:URL 必须可解析;非回环 ⇒ key 必填。
func (cfg *AgentCoreLLMConfig) Validate() error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("agentcore_llm.base_url 必须是合法 URL(含 scheme 与 host), got %q", cfg.BaseURL)
	}
	if !isLoopbackHostName(u.Hostname()) && strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("agentcore_llm.api_key 必填: 非回环地址 %s 无平台 relay 注入凭据", strings.TrimSpace(cfg.BaseURL))
	}
	return nil
}

// isLoopbackHostName 判断主机名是否本机回环(IP 回环段或 localhost)。
// 注意:仓库里有三处回环判定——services 的 isLoopbackHost
// (agent_ai_proxy_guard.go:69,窄口径:127.0.0.1/::1/localhost 字面量)、
// 本函数(config 包,宽口径:全回环段+localhost)、前端的
// utils/agentCoreSettings.js isLoopbackHost(与本函数同口径)。口径分歧是
// 有意的:本函数与前端同源(校验语义),services 那个只服务 56432 特判。
func isLoopbackHostName(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

`Config.Validate()`(:760)在 http1Drop 校验块之后追加:

```go
	if c.Customer != nil && c.Customer.AgentCoreLLM != nil {
		if err := c.Customer.AgentCoreLLM.Validate(); err != nil {
			return fmt.Errorf("invalid agentcore_llm configuration: %w", err)
		}
	}
```

白名单 switch(`extractCustomerUnknownFields`,:914 附近)的 case 列表追加 `"agentcore_llm"`:

```go
		switch key {
		case "proxy", "ai_rules", "proxy_rules", "traffic_mirror", "http1_drop", "model_mapping", "custom_env_vars", "geoip", "agentcore_llm":
```

错误文案(`customerUnknownKeyErrors`,:877)的列表串同步追加 `agentcore_llm`:

```go
		return fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop model_mapping custom_env_vars geoip agentcore_llm]", sorted[0])
```

imports 按需补 `aliang.one/nursorgate/common/logger`(`net`/`net/url`/`fmt`/`strings` 已在 types.go;logger 被 processor/auth 等同层包引用,无循环依赖)。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./processor/config/ -run 'TestEffectiveAgentCoreLLM|TestAgentCoreLLMValidate|TestCustomerUnknownKeyAllowsAgentCoreLLM' -v
```

Expected: 全 PASS。再跑整包确认无回归:`go test ./processor/config/`。

---

### Task 2: 保存通道白名单(`customer_config_service.go`)

**Files:**
- Modify: `app/http/services/customer_config_service.go:410-416`(switch)、`:383`、`:412`(两处错误文案)
- Modify: `app/http/services/customer_config_service_test.go`(追加用例)

- [ ] **Step 1: 写失败测试**(追加到 `customer_config_service_test.go`,沿用文件内既有 import)

```go
// agentcore_llm 必须在可编辑白名单里,否则保存 400。
func TestValidateEditableCustomerKeysAllowsAgentCoreLLM(t *testing.T) {
	raw := map[string]json.RawMessage{
		"agentcore_llm": json.RawMessage(`{"enabled":true,"base_url":"http://127.0.0.1:8082"}`),
	}
	if err := validateEditableCustomerKeys(raw); err != nil {
		t.Fatalf("agentcore_llm 应放行, got %v", err)
	}
	err := validateEditableCustomerKeys(map[string]json.RawMessage{
		"no_such_key": json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "agentcore_llm") {
		t.Errorf("拒绝文案应含更新后的字段清单, got %v", err)
	}
}
```

另追加**保存路径 400 用例**,命名 `TestSaveCustomerConfigRejectsAgentCoreLLMRemoteNoKey`(spec §3.5「customer_config_service 保存校验」的落点,白名单用例盖不到语义校验):照抄本测试文件里既有保存路径用例的最小合法 Config 装配(含 core 字段——`Config.Validate` 先跑 `validateCustomerEditableSurface()`(白名单,届时已放行),随后 :765 查 `core.api_server`,不配会先报那个错),把 `customer` 里的 `agentcore_llm` 换成 `{"enabled":true,"base_url":"https://gw.example.com"}`(远端+无 key),断言保存返回错误且信息含 `agentcore_llm.api_key`。本任务 Step 2/4 的 `-run` 模式相应扩为 `'TestValidateEditableCustomerKeysAllowsAgentCoreLLM|TestSaveCustomerConfigRejectsAgentCoreLLMRemoteNoKey'`(`go test -run` 不匹配的名字会被静默跳过)。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestValidateEditableCustomerKeysAllowsAgentCoreLLM|TestSaveCustomerConfigRejectsAgentCoreLLMRemoteNoKey' -v
```

Expected: FAIL(文案不含 agentcore_llm;保存 400 用例编译不过)。

- [ ] **Step 3: 实现**

`validateEditableCustomerKeys`(:410)case 列表追加 `"agentcore_llm"`;同文件两处错误文案(:383、:412)的 `[...]` 列表末尾追加 `agentcore_llm`(三处串与 types.go:877 的串逐字保持一致)。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestValidateEditableCustomerKeysAllowsAgentCoreLLM|TestSaveCustomerConfigRejectsAgentCoreLLMRemoteNoKey' -v
```

Expected: PASS。

---

### Task 3: 读取链优先级与拨测分叉(`agent_ai_agentcore.go`)

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go:128-133`(agentCoreAuthToken)、`:203-213`(agentCoreBaseURL)、`:264-285`(agentCoreEnsureUpstream)
- Modify: `app/http/services/agent_ai_agentcore_test.go`(追加用例与 helper)

- [ ] **Step 1: 写失败测试**(追加到 `agent_ai_agentcore_test.go`)

```go
// setTestGlobalConfigAgentCoreLLM 临时替换带 AgentCoreLLM 的全局配置,还原走
// t.Cleanup(形状对齐 agent_ai_child_env_test.go 的 setTestGlobalConfigCustomEnv)。
func setTestGlobalConfigAgentCoreLLM(t *testing.T, llm *config.AgentCoreLLMConfig) {
	t.Helper()
	old := config.GetGlobalConfig()
	next := &config.Config{}
	if old != nil {
		*next = *old
	}
	next.Customer = &config.CustomerConfig{AgentCoreLLM: llm}
	t.Cleanup(func() { config.SetGlobalConfig(old) })
	config.SetGlobalConfig(next)
}

func isolateAgentCoreBaseURL(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // settings.json 缺席,继承链只剩 env
	t.Setenv("ANTHROPIC_BASE_URL", "")
}

func TestAgentCoreBaseURLCustomOverridesAll(t *testing.T) {
	isolateAgentCoreBaseURL(t)
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9999") // 继承链给的回环值
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-x",
	})
	if got := agentCoreBaseURL(); got != "https://gw.example.com" {
		t.Errorf("→ %q, want 显式远端地址(绕过回环闸)", got)
	}
}

func TestAgentCoreBaseURLCustomDisabledFallsToInheritedGate(t *testing.T) {
	isolateAgentCoreBaseURL(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.aliang.one") // 继承链远端:必须被回环闸拒
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{Enabled: false})
	if got := agentCoreBaseURL(); got != agentCoreDefaultBaseURL {
		t.Errorf("→ %q, want %s(闸语义不变)", got, agentCoreDefaultBaseURL)
	}
}

func TestAgentCoreAuthTokenCustomSkipsCacheAndFallback(t *testing.T) {
	isolateAgentCoreToken(t)
	calls := stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		t.Error("显式 key 存在时不该打 user-center")
		return nil, nil
	})
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: " sk-custom ",
	})
	if got := agentCoreAuthToken(); got != "sk-custom" {
		t.Errorf("→ %q, want sk-custom(trim 后)", got)
	}
	// 显式 key 不进缓存:改配置立即生效
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-two",
	})
	if got := agentCoreAuthToken(); got != "sk-two" {
		t.Errorf("→ %q, want sk-two(不走缓存)", got)
	}
	if *calls != 0 {
		t.Errorf("user-center 被调用 %d 次,应为 0", *calls)
	}
}

func TestAgentCoreAuthTokenCustomLoopbackNoKeyFallsThrough(t *testing.T) {
	isolateAgentCoreToken(t)
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{{Key: "sk-user", SecretAvailable: true, Status: "active"}}, nil
	})
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "") // 隔离 env 继承
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:8082", // 回环+无 key:合法,key 落继承链
	})
	if got := agentCoreAuthToken(); got != "sk-user" {
		t.Errorf("→ %q, want sk-user(用户 API key 兜底)", got)
	}
}

// 自定义端点拨测:可达 → nil;不可达 → 明确报错(不碰 56432 自愈)。
func TestAgentCoreEnsureUpstreamCustomEndpoint(t *testing.T) {
	isolateAgentCoreBaseURL(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{Enabled: true, BaseURL: srv.URL})
	if err := agentCoreEnsureUpstream(context.Background()); err != nil {
		t.Fatalf("可达端点不应报错: %v", err)
	}

	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1",
	})
	err := agentCoreEnsureUpstream(context.Background())
	if err == nil || !strings.Contains(err.Error(), "自定义 LLM 端点不可达") {
		t.Errorf("→ %v, want 含「自定义 LLM 端点不可达」", err)
	}
}

// 无端口 URL 是 UI 示例的主形态(https://your-gateway.example.com):
// u.Host 不带端口,直接拿去 dial 会报 "missing port in address"。
// 拨测目标必须补默认端口。
func TestAgentCoreDialHost(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://gw.example.com", "gw.example.com:443"},
		{"http://gw.example.com", "gw.example.com:80"},
		{"http://127.0.0.1:8082", "127.0.0.1:8082"},
		{"https://[::1]:9443", "[::1]:9443"},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got := agentCoreDialHost(u); got != tc.want {
			t.Errorf("%s → %q, want %q", tc.raw, got, tc.want)
		}
	}
}
```

(若文件缺 import,补 `context`/`net`/`net/http`/`net/http/httptest`/`net/url`/`strings`/`aliang.one/nursorgate/processor/config`——以文件现有 import 块为准。)

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentCoreBaseURLCustom|TestAgentCoreAuthTokenCustom|TestAgentCoreEnsureUpstreamCustomEndpoint|TestAgentCoreDialHost' -v
```

Expected: FAIL(显式层不存在,行为仍是继承链;TestAgentCoreDialHost 因函数未定义编译失败)。

- [ ] **Step 3: 实现**

`agentCoreBaseURL`(:203)函数体首部插入显式层:

```go
func agentCoreBaseURL() string {
	// 显式设置(仪表盘 customer.agentcore_llm)最优先:回环/远端都放行——
	// 用户显式担责,回环闸只拦继承链里意外带进来的远端地址。
	if custom := config.GetGlobalConfig().EffectiveAgentCoreLLM(); custom != nil {
		return custom.BaseURL
	}
	// ...以下现有逻辑原样...
}
```

`agentCoreAuthToken`(:128)函数体首部插入:

```go
func agentCoreAuthToken() string {
	// 显式 key 最优先;不进 token 缓存(改配置下一回合立即生效,无需等 TTL)。
	if custom := config.GetGlobalConfig().EffectiveAgentCoreLLM(); custom != nil {
		if custom.APIKey != "" {
			return custom.APIKey
		}
		// 回环+空 key 是合法形态:key 落继承链(URL 已显式)。
	}
	// ...以下现有逻辑原样...
}
```

`agentCoreEnsureUpstream`(:264)函数体首部插入自定义分叉(run 与 /compact 两条路径都先经这里,生效日志也放这里,每回合恰好一行):

```go
// agentCoreDialHost 返回拨测目标 host:port。URL 无显式端口时按 scheme 补
// 默认值——u.Host 对 "https://gw.example.com" 是裸域名,直接 dial 会报
// "missing port in address"。
func agentCoreDialHost(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func agentCoreEnsureUpstream(ctx context.Context) error {
	// 显式端点:没有「自愈」概念(不是我们的进程,POST /api/run/start 拉不活
	// 它),只做同款 2s TCP 拨测,失败给可自查的文案。
	if custom := config.GetGlobalConfig().EffectiveAgentCoreLLM(); custom != nil {
		logger.Info("agentcore: 使用自定义 LLM 端点 " + custom.BaseURL)
		u, err := url.Parse(custom.BaseURL)
		if err != nil || u.Host == "" {
			return nil // 形态已被 Effective/Validate 挡住;探测逻辑自身不挂 run
		}
		d := net.Dialer{Timeout: 2 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", agentCoreDialHost(u))
		if err != nil {
			return fmt.Errorf("自定义 LLM 端点不可达(%s: %v)——请检查地址与服务状态后重试", custom.BaseURL, err)
		}
		conn.Close()
		return nil
	}
	// ...以下现有 56432 探测+自愈逻辑原样...
}
```

(不打 key。两个内核客户端构造点 :668/:1168 共用同一取值函数,无需分别改。)

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentCoreBaseURLCustom|TestAgentCoreAuthTokenCustom|TestAgentCoreEnsureUpstreamCustomEndpoint|TestAgentCoreDialHost' -v
go test ./app/http/services/ -run 'TestAgentCore' -v
```

Expected: 全 PASS(第二跑覆盖既有 agentcore 用例,确认无回归)。

---

### Task 4: 前端校验纯函数 + vitest

**Files:**
- Create: `app/website/src/utils/agentCoreSettings.js`
- Create: `app/website/src/utils/agentCoreSettings.test.js`

- [ ] **Step 1: 写失败测试**(风格对齐 `chatSettings.test.js`)

```js
import { describe, expect, it } from 'vitest';
import {
  normalizeAgentCoreLLMConfig,
  validateAgentCoreLLMConfig,
} from './agentCoreSettings';

describe('validateAgentCoreLLMConfig', () => {
  it('未启用不校验', () => {
    expect(validateAgentCoreLLMConfig({ enable: false, baseUrl: '', apiKey: '' })).toBe('');
  });

  it('启用+URL 空', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: '  ', apiKey: '' }))
      .toBe('agentCore_error_urlRequired');
  });

  it('启用+URL 畸形', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'not a url', apiKey: 'k' }))
      .toBe('agentCore_error_urlInvalid');
  });

  it('启用+回环+无 key 合法', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'http://127.0.0.1:8082', apiKey: '' }))
      .toBe('');
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'http://localhost:8082', apiKey: '' }))
      .toBe('');
  });

  it('启用+远端+无 key 拒', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'https://gw.example.com', apiKey: '' }))
      .toBe('agentCore_error_keyRequired');
  });

  it('启用+远端+有 key 合法', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'https://gw.example.com', apiKey: 'sk-x' }))
      .toBe('');
  });
});

describe('normalizeAgentCoreLLMConfig', () => {
  it('脏输入安全回落默认', () => {
    expect(normalizeAgentCoreLLMConfig(undefined)).toEqual({ enabled: false, base_url: '', api_key: '' });
    expect(normalizeAgentCoreLLMConfig('x')).toEqual({ enabled: false, base_url: '', api_key: '' });
  });

  it('读服务端线格式 enabled,输出保持线格式(可直进 patch)', () => {
    expect(normalizeAgentCoreLLMConfig({ enabled: true, base_url: ' https://a.b ', api_key: 'sk=1 2' }))
      .toEqual({ enabled: true, base_url: ' https://a.b ', api_key: 'sk=1 2' });
  });

  it('线格式守卫:输出键必须是 enabled,绝不含 enable', () => {
    const out = normalizeAgentCoreLLMConfig({ enable: true });
    expect(out.enabled).toBe(false);
    expect(Object.keys(out)).not.toContain('enable');
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd app/website && npx vitest run src/utils/agentCoreSettings.test.js
```

Expected: FAIL(模块不存在)。

- [ ] **Step 3: 实现** `app/website/src/utils/agentCoreSettings.js`

```js
// AliangCode 内核 LLM 设置(仪表盘)纯逻辑:校验与归一。零依赖,vitest 直接
// 覆盖;UI 装配在 settings/AgentCoreSettings.vue。校验规则与后端
// AgentCoreLLMConfig.Validate(processor/config/types.go)同源,两端不许分叉。

function isLoopbackHost(hostname) {
  const host = String(hostname || '').trim().toLowerCase();
  if (!host) return false;
  if (host === 'localhost' || host.endsWith('.localhost')) return true;
  if (host === '::1' || /^\[::1\]$/.test(host)) return true;
  return /^127\.\d+\.\d+\.\d+$/.test(host);
}

// 返回 ''(合法)或 i18n 键。规则与后端一致:启用时 URL 必填可解析;
// 非回环 ⇒ key 必填。
export function validateAgentCoreLLMConfig({ enable, baseUrl, apiKey } = {}) {
  if (!enable) return '';
  const url = String(baseUrl || '').trim();
  if (!url) return 'agentCore_error_urlRequired';
  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return 'agentCore_error_urlInvalid';
  }
  if (!parsed.hostname) return 'agentCore_error_urlInvalid';
  if (!isLoopbackHost(parsed.hostname) && !String(apiKey || '').trim()) {
    return 'agentCore_error_keyRequired';
  }
  return '';
}

// 服务端载荷 → 归一配置。⚠️ 输出必须是线格式(`enabled`,Go tag
// json:"enabled"):本函数的输出会经 SettingsPage 的 normalizeCustomerConfig →
// buildCustomerConfigPatch 原样进 POST patch——布尔键在这里走样成 `enable` 的
// 话,后端 encoding/json 会静默丢弃它,层永远激活不了(且保存+回读后面板
// 显示会翻回关闭)。面板表单内部状态键用 enable,由组件自行映射;
// custom_env_vars 的 enable 惯例不可照搬(Go 侧有 UnmarshalJSON 别名,本结构没有)。
export function normalizeAgentCoreLLMConfig(source) {
  if (!source || typeof source !== 'object' || Array.isArray(source)) {
    return { enabled: false, base_url: '', api_key: '' };
  }
  return {
    enabled: typeof source.enabled === 'boolean' ? source.enabled : false,
    base_url: typeof source.base_url === 'string' ? source.base_url : '',
    api_key: typeof source.api_key === 'string' ? source.api_key : '',
  };
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd app/website && npx vitest run src/utils/agentCoreSettings.test.js
```

Expected: 全 PASS。

---

### Task 5: 面板组件 + 注册 + i18n + 清空可达

**Files:**
- Create: `app/website/src/components/settings/AgentCoreSettings.vue`
- Modify: `app/website/src/components/SettingsPage.vue`(:262 createDefaultCustomerConfig、:342 normalizeCustomerConfig、:374 buildCustomerConfigPatch、agent 区块模板 ~:189、import :256、components :425)
- Modify: `app/website/src/i18n/zh.js`、`app/website/src/i18n/en.js`(customEnv_* 键块之后)

- [ ] **Step 1: i18n 键**(`zh.js` 在 `customEnv_saving` 行后插入;`en.js` 同位置对应英文)

```js
  // -- AgentCoreSettings --
  agentCore_title: 'AliangCode 内核 LLM 设置',
  agentCore_description: '为本机全部 AliangCode 会话指定显式 LLM 端点（Anthropic Messages 兼容）。未启用时沿用 Claude Code 同款配置与平台代理。',
  agentCore_enable: '启用自定义 LLM 端点',
  agentCore_baseUrl: 'Base URL',
  agentCore_baseUrlPlaceholder: '例如 https://your-gateway.example.com',
  agentCore_apiKey: 'API Key',
  agentCore_apiKeyPlaceholder: '非回环地址必填',
  agentCore_showKey: '显示',
  agentCore_hideKey: '隐藏',
  agentCore_hint: '保存后下一个回合生效，无需重启。仅支持 Anthropic Messages 兼容端点；非回环地址必须填写 API Key。',
  agentCore_error_urlRequired: '启用时必须填写 Base URL',
  agentCore_error_urlInvalid: 'Base URL 必须是合法 URL（含协议与主机名）',
  agentCore_error_keyRequired: '非回环地址必须填写 API Key',
  agentCore_save: '保存',
  agentCore_saving: '保存中…',
```

英文版对应直译(`agentCore_title: 'AliangCode Kernel LLM Settings'` 等)。

- [ ] **Step 2: 新组件** `AgentCoreSettings.vue`(结构对齐 CustomEnvSettings:同 props/emits/watch 契约;校验不过禁用保存)

```vue
<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">hub</span>
      </div>
      <div class="min-w-0">
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agentCore_title') }}</h2>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('agentCore_description') }}</p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span class="block text-xs font-semibold">{{ t('agentCore_enable') }}</span>
      <input
        type="checkbox"
        class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="form.enable"
        :disabled="loading || saving"
        @change="form.enable = $event.target.checked"
      />
    </label>

    <div class="mt-4 flex flex-col gap-3">
      <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
        <span>{{ t('agentCore_baseUrl') }}</span>
        <input
          v-model="form.baseUrl"
          type="text"
          :placeholder="t('agentCore_baseUrlPlaceholder')"
          :disabled="loading || saving"
          class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
      </label>
      <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
        <span>{{ t('agentCore_apiKey') }}</span>
        <span class="relative mt-1.5 block">
          <input
            v-model="form.apiKey"
            :type="showKey ? 'text' : 'password'"
            :placeholder="t('agentCore_apiKeyPlaceholder')"
            :disabled="loading || saving"
            class="h-10 w-full rounded-lg border border-slate-200 bg-white px-3 pr-16 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 text-[11px] font-medium text-slate-400 hover:text-primary"
            @click="showKey = !showKey"
          >
            {{ showKey ? t('agentCore_hideKey') : t('agentCore_showKey') }}
          </button>
        </span>
      </label>
    </div>

    <p class="mt-3 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('agentCore_hint') }}</p>

    <div v-if="localError" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ localError }}</div>
    <div v-else-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving || !!localError"
      @click="save"
    >
      <span class="material-symbols-outlined text-base">save</span>
      {{ saving ? t('agentCore_saving') : t('agentCore_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { normalizeAgentCoreLLMConfig, validateAgentCoreLLMConfig } from '../../utils/agentCoreSettings';

export default {
  name: 'AgentCoreSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  data() {
    return {
      form: this.formFromConfig(this.config),
      showKey: false
    };
  },
  computed: {
    localError() {
      return validateAgentCoreLLMConfig({
        enable: this.form.enable,
        baseUrl: this.form.baseUrl,
        apiKey: this.form.apiKey
      }) ? this.t(validateAgentCoreLLMConfig({
        enable: this.form.enable,
        baseUrl: this.form.baseUrl,
        apiKey: this.form.apiKey
      })) : '';
    }
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form = this.formFromConfig(nextConfig);
      }
    }
  },
  methods: {
    formFromConfig(config) {
      const llm = normalizeAgentCoreLLMConfig(config?.agentcore_llm);
      // llm 是线格式(enabled);面板表单内部状态键用 enable
      return { enable: llm.enabled, baseUrl: llm.base_url, apiKey: llm.api_key };
    },
    save() {
      if (this.localError) return;
      const nextConfig = {
        ...(this.config || {}),
        agentcore_llm: {
          // 线格式开关键是 enabled(Go tag);面板内部状态键是 enable,见
          // utils/agentCoreSettings.js 的注释。发错键会被 encoding/json 静默
          // 丢弃,层永远激活不了。
          enabled: !!this.form.enable,
          base_url: String(this.form.baseUrl || ''),
          api_key: String(this.form.apiKey || '')
        }
      };
      this.$emit('save', nextConfig);
    }
  }
};
</script>
```

- [ ] **Step 3: 清空可达纯函数**(先落 utils,Task 6 直接测它;比照 `proxy.username/password` 的 force-include 模式)

`utils/agentCoreSettings.js` 追加:

```js
// agentcore_llm 清空必须可达(spec §3.3):buildConfigPatch 对空串返回
// undefined(键不进 patch),后端 deepMergeJSONObjects 只增不减——不显式写
// 空值,旧 key 会永久留存。enabled 是布尔,走 Object.is 比较,天然可达。
// 返回 patch,存在清空项时写入 patch.agentcore_llm。
export function applyAgentCoreLLMClearing(patch, nextLLM, currentLLM) {
  const next = nextLLM || {};
  const current = currentLLM || {};
  for (const key of ['base_url', 'api_key']) {
    if (!String(next[key] ?? '').trim() && String(current[key] ?? '').trim()) {
      patch.agentcore_llm = patch.agentcore_llm && typeof patch.agentcore_llm === 'object'
        ? patch.agentcore_llm
        : {};
      patch.agentcore_llm[key] = '';
    }
  }
  return patch;
}
```

`SettingsPage.vue` 的 `buildCustomerConfigPatch()`(:374)在 username/password force-include 循环之后追加调用:

```js
  applyAgentCoreLLMClearing(patch, normalizedNext.agentcore_llm, normalizedCurrent.agentcore_llm);
```

(import 加在 SettingsPage.vue 头部 import 区——即 Step 4 第 4 点同一处接线,与 `normalizeAgentCoreLLMConfig` 合并成一条 import 语句。)

- [ ] **Step 4: SettingsPage.vue 接线(6 处)**

1. import(:256 `import CustomEnvSettings ...` 之后):`import AgentCoreSettings from './settings/AgentCoreSettings.vue';`
2. components(:425 块内):加 `AgentCoreSettings,`
3. `createDefaultCustomerConfig()`(:262):`custom_env_vars` 之后加

```js
    agentcore_llm: {
      enabled: false,
      base_url: '',
      api_key: ''
    }
```

(默认 shape 必须与 `normalizeAgentCoreLLMConfig` 的输出即线格式一致——`enabled` 键。)

4. `normalizeCustomerConfig()`(:342 返回对象):`custom_env_vars` 行后加

```js
    agentcore_llm: normalizeAgentCoreLLMConfig(payload?.agentcore_llm),
```

并在文件头部 import 区加 `import { normalizeAgentCoreLLMConfig } from '../utils/agentCoreSettings';`(确认相对路径与该文件既有 import 风格一致)。

5. 模板 agent 区块(~:189):`<AgentScanDirectoriesSettings />` 所在 grid 的 `</div>` **之后**(grid 外独占一行,避免挤进两列 grid 剩半格):

```html
              <AgentCoreSettings
                :config="customerConfig"
                :loading="isLoadingCustomerConfig"
                :saving="isSavingCustomerConfig"
                :error="customerConfigError"
                :success-message="customerConfigSuccess"
                @save="saveCustomerConfig"
              />
```

6. **清空可达**:已在 Step 3 落为 `applyAgentCoreLLMClearing` 并在 `buildCustomerConfigPatch()`(:374,username/password force-include 循环之后)调用,此处无需再改——列出仅为核对接线完整。

- [ ] **Step 5: 构建验证**

```bash
cd app/website && npm run build
```

Expected: 构建成功(vite 构建含模板/导入校验)。

---

### Task 6: 清空往返回归测试 + 全量验证

**Files:**
- Modify: `app/website/src/utils/agentCoreSettings.test.js`(或就近对 `buildCustomerConfigPatch` 的既有测试文件——若 SettingsPage 有配套 .test.js 则加在那里)

- [ ] **Step 1: 写清空往返测试**(测 Task 5 Step 3 已抽出的 `applyAgentCoreLLMClearing`,加进 `utils/agentCoreSettings.test.js`)

```js
import { applyAgentCoreLLMClearing } from './agentCoreSettings';

describe('applyAgentCoreLLMClearing', () => {
  it('next 空且 current 非空 → 显式写空值', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: '', api_key: '' }, { base_url: 'https://a.b', api_key: 'sk-x' });
    expect(patch.agentcore_llm).toEqual({ base_url: '', api_key: '' });
  });

  it('next 有值 → 不动 patch', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: 'https://c.d', api_key: '' }, { base_url: 'https://a.b', api_key: 'sk-x' });
    expect(patch.agentcore_llm).toEqual({ api_key: '' }); // 只有清空的那维
  });

  it('双方都空 → 不产生键', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: '', api_key: '' }, { base_url: '', api_key: '' });
    expect(patch.agentcore_llm).toBeUndefined();
  });
});
```

- [ ] **Step 2: 跑全部前端测试**

```bash
cd app/website && npm test
```

Expected: 全 PASS(含既有 chatSettings 等用例)。

- [ ] **Step 3: 全量 Go 验证**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-llm-settings
go build ./...
go test ./processor/config/ ./app/http/services/ -run 'AgentCore|Customer' -v
```

Expected: 构建成功;目标用例全 PASS。(services 包预存 flake 见仓库记忆:偶败单包复跑即绿,不当回归。)

- [ ] **Step 4: 手工冒烟清单**(部署后执行,记录到任务完成报告)

1. 仪表盘 → 设置 → Agent 页:新面板可见,默认关闭
2. 启用 + 填远端 URL 不填 key → 保存按钮禁用,就地提示
3. 填 key 保存 → GET `/api/config/customer` 回读含 `agentcore_llm`
4. 发一条 AliangCode 消息 → 日志见「agentcore: 使用自定义 LLM 端点」,流量打到自定义地址
5. 停掉自定义端点 → 发消息 → 报「自定义 LLM 端点不可达」
6. 清空 key 保存 → 回读 `api_key` 为空(非残留旧值)
7. 关闭开关保存 → 行为回落平台代理

---

## 完成定义

- [ ] Task 0-6 全部勾完,冒烟清单逐项有结论
- [ ] 白名单 4 处逻辑 + 3 处文案确认同一提交(或连续提交)内完成
- [ ] 提交经用户逐次同意,标题全中文「新增:」格式
- [ ] 真机/liang-dev 部署与真机冒烟为后续独立事项,不在本计划内
