package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// 内置内核的路由判定。
//
// 这里只钉语义边界，不钉实现：显式 "agentcore" 恒走内核；"auto" 只在
// 「一个外部 CLI 都没有」时兜底；goal / 只读 run 一律不兜底（内核首期不注入
// ALIANG_GOAL_REPORT 强制段，兜底会让 goal 永远拿不到报告行）。

func overrideExternalCLIProbe(t *testing.T, available bool) {
	t.Helper()
	orig := agentAIExternalCLIProbe
	agentAIExternalCLIProbe = func() bool { return available }
	t.Cleanup(func() { agentAIExternalCLIProbe = orig })
}

func TestAgentAIUseAgentCoreMatchesOnlyExplicitProvider(t *testing.T) {
	for _, p := range []string{"agentcore", "AgentCore", "  AGENTCORE  "} {
		if !agentAIUseAgentCore(p) {
			t.Errorf("%q 应判定走内核", p)
		}
	}
	// auto 必须**不**命中：内置内核不得顶掉用户装了的 claude/codex。
	for _, p := range []string{"auto", "claude", "claudecode", "codex", "opencode", "", "agent-core"} {
		if agentAIUseAgentCore(p) {
			t.Errorf("%q 不应判定走内核", p)
		}
	}
}

func TestAgentAIUseAgentCoreFallback(t *testing.T) {
	// 无外部 CLI + auto → 兜底。
	overrideExternalCLIProbe(t, false)
	if !agentAIUseAgentCoreFallback(agentAIRun{provider: "auto"}) {
		t.Error("无外部 CLI 时 auto 应兜底到内核")
	}
	// 有外部 CLI → 不兜底（用户装了什么就用什么）。
	overrideExternalCLIProbe(t, true)
	if agentAIUseAgentCoreFallback(agentAIRun{provider: "auto"}) {
		t.Error("有外部 CLI 时不应兜底")
	}

	// 显式 provider 永不兜底——即使一个 CLI 都没有，也要按用户的选择报错，
	// 而不是静默换成内核。
	overrideExternalCLIProbe(t, false)
	for _, p := range []string{"claude", "claudecode", "codex", "opencode", "agentcore", ""} {
		if agentAIUseAgentCoreFallback(agentAIRun{provider: p}) {
			t.Errorf("显式 provider %q 不应兜底", p)
		}
	}

	// goal / 只读 run 不兜底：内核首期不注入 goal 报告强制段。
	overrideExternalCLIProbe(t, false)
	if agentAIUseAgentCoreFallback(agentAIRun{provider: "auto", readOnly: true}) {
		t.Error("只读 run 不应兜底")
	}
	if agentAIUseAgentCoreFallback(agentAIRun{provider: "auto", goalIdentity: map[string]interface{}{"goal_id": "g1"}}) {
		t.Error("goal run 不应兜底")
	}
}

// 手机下发的模型名是唯一来源：本机 env / settings 一律不得介入，也不再有任何
// 兜底常量。
//
// 为什么这么严：静默换模型的代价是「用户选了 A、实际跑在 B」——账单、上下文
// 窗口、视觉能力全部错配，而错配在日志里不留痕。宁可拒绝开跑（由 runAgentCore
// 报错让用户回手机选模型），也不要替他决定。
func TestAgentCoreModelResolution(t *testing.T) {
	// 隔离 ~/.claude/settings.json：agentCoreEnvOrSettings 会去读它。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_MODEL", "glm-5.3-from-env")
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "glm-5.3-from-settings")

	// 1) 手机传的模型恒胜出，且去掉首尾空白。
	for _, tc := range []struct{ phone, want string }{
		{"glm-5.3", "glm-5.3"},
		{"  glm-5.3-flash[1m]  ", "glm-5.3-flash[1m]"},
		{"glm-5.1", "glm-5.1"},
	} {
		if got := agentCoreModel(agentAIRun{model: tc.phone}); got != tc.want {
			t.Errorf("手机传 %q → %q, want %q（手机是权威来源）", tc.phone, got, tc.want)
		}
	}

	// 2) 手机没传、或传了不能当模型名的占位名 → 返回空串（调用方据此拒绝开跑）。
	//    env 里明明有值也不能被采纳：那正是旧行为里「悄悄换模型」的入口。
	for _, phone := range []string{"", "   ", "<synthetic>", "<SYNTHETIC>"} {
		if got := agentCoreModel(agentAIRun{model: phone}); got != "" {
			t.Errorf("手机传 %q → %q, want 空串（不兜底、不读 env）", phone, got)
		}
	}
}

// 内核出网凭据的解析。
//
// 为什么必须有这一层：网关 /v1/messages 的 key 解析先看请求自带的 candidate
// key，命中用户自己的 key 就直接用（source=user-api-key，不做分组门禁）；不带
// 才落到 anthropic-duration 分组的兜底 key——不在该组的新账号会被 403 挡下。
// CLI provider 因为 quick-setup 把 ANTHROPIC_AUTH_TOKEN 写成用户自己的
// {{api_key}}，所以从不出这个问题；内核必须与它同口径。

// isolateAgentCoreToken 隔离外部状态：本机 ~/.claude/settings.json 里可能真的
// 有一份 token，测试不能读到它，否则断言会随开发机而变。
func isolateAgentCoreToken(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	agentCoreResetTokenCache()
	t.Cleanup(agentCoreResetTokenCache)
}

// stubAgentCoreUserAPIKeys 替换 user-center 入口，返回一个调用计数指针。
func stubAgentCoreUserAPIKeys(t *testing.T, fn func() ([]auth.UserAPIKey, error)) *int {
	t.Helper()
	calls := 0
	orig := agentCoreGetUserAPIKeysFn
	agentCoreGetUserAPIKeysFn = func() ([]auth.UserAPIKey, error) {
		calls++
		return fn()
	}
	t.Cleanup(func() { agentCoreGetUserAPIKeysFn = orig })
	return &calls
}

func TestAgentCoreTokenPrefersAnthropicAuthToken(t *testing.T) {
	isolateAgentCoreToken(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "  sk-from-settings  ")
	calls := stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		t.Error("env 有 token 时不该打 user-center")
		return nil, nil
	})

	if got := agentCoreAuthToken(); got != "sk-from-settings" {
		t.Errorf("→ %q, want sk-from-settings（首尾空白已去掉）", got)
	}
	if *calls != 0 {
		t.Errorf("user-center 被调用 %d 次，应为 0", *calls)
	}
}

func TestAgentCoreTokenFallsBackToUserAPIKeys(t *testing.T) {
	isolateAgentCoreToken(t)
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			// 不可用/已停用的一律跳过，哪怕平台对。
			{Key: "sk-masked", Provider: "anthropic", Status: "active", SecretAvailable: false},
			{Key: "sk-inactive", Provider: "anthropic", Status: "inactive", SecretAvailable: true},
			{Key: "", Provider: "anthropic", Status: "active", SecretAvailable: true},
			// 非 anthropic 平台先记作后备，不抢占 anthropic。
			{Key: "sk-openai", Provider: "openai", Status: "active", SecretAvailable: true},
			{Key: "  sk-anthropic  ", Provider: "Anthropic", Status: "active", SecretAvailable: true},
		}, nil
	})

	if got := agentCoreAuthToken(); got != "sk-anthropic" {
		t.Errorf("→ %q, want sk-anthropic（优先 anthropic 平台且去空白）", got)
	}
}

func TestAgentCoreTokenUsesAnyActiveKeyWhenNoAnthropicKey(t *testing.T) {
	isolateAgentCoreToken(t)
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{Key: "sk-openai", Provider: "openai", Status: "active", SecretAvailable: true},
		}, nil
	})

	// 没有 anthropic key 时退回任意可用 key：总比不带强（不带是必然的
	// 分组门禁 403）。
	if got := agentCoreAuthToken(); got != "sk-openai" {
		t.Errorf("→ %q, want sk-openai", got)
	}
}

func TestAgentCoreTokenEmptyWhenNothingUsable(t *testing.T) {
	// 拿不到 key 不能让这一轮直接失败：返回空串，内核照旧出网，行为退回
	// 「由代理按分组兜底」。
	for name, fn := range map[string]func() ([]auth.UserAPIKey, error){
		"报错":  func() ([]auth.UserAPIKey, error) { return nil, errors.New("network down") },
		"空列表": func() ([]auth.UserAPIKey, error) { return nil, nil },
		"全部不可用": func() ([]auth.UserAPIKey, error) {
			return []auth.UserAPIKey{
				{Key: "sk-x", Provider: "anthropic", Status: "inactive", SecretAvailable: true},
			}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolateAgentCoreToken(t)
			stubAgentCoreUserAPIKeys(t, fn)
			if got := agentCoreAuthToken(); got != "" {
				t.Errorf("→ %q, want 空串（不带凭据照旧出网）", got)
			}
		})
	}
}

func TestAgentCoreTokenCachesUserAPIKeys(t *testing.T) {
	isolateAgentCoreToken(t)
	calls := stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{
			{Key: "sk-cached", Provider: "anthropic", Status: "active", SecretAvailable: true},
		}, nil
	})

	for i := 0; i < 3; i++ {
		if got := agentCoreAuthToken(); got != "sk-cached" {
			t.Fatalf("第 %d 次 → %q, want sk-cached", i+1, got)
		}
	}
	// user-center 是网络调用（分页拉全量，两次请求），绝不能每轮 run 都打。
	if *calls != 1 {
		t.Errorf("user-center 被调用 %d 次，应为 1（命中缓存）", *calls)
	}
}

// 内核工具子进程必须有 TERM/LANG 兜底：GUI/systemd 启动的 agent 进程两者皆缺，
// 无 TERM 部分命令行工具降级，无 LANG 按 C locale 跑（中文/排序失真）。
// 顺序敏感：先断言兜底（TERM/LANG 空），再改值断言不覆盖（t.Setenv 逐层还原）。
func TestAgentCoreToolEnvTTYDefaults(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("TERM", "")
	t.Setenv("LANG", "")
	env := agentCoreToolEnv()
	if v, ok := scanEnvValue(env, "TERM"); !ok || v != "xterm-256color" {
		t.Fatalf("TERM 兜底缺失: %q", v)
	}
	if v, ok := scanEnvValue(env, "LANG"); !ok || v != "en_US.UTF-8" {
		t.Fatalf("LANG 兜底缺失: %q", v)
	}

	t.Setenv("TERM", "screen")
	env = agentCoreToolEnv()
	if v, _ := scanEnvValue(env, "TERM"); v != "screen" {
		t.Fatalf("TERM 不应被覆盖: %q", v)
	}

	// LC_ALL 有值（非空）时 LANG 兜底让位：setlocale 以 LC_ALL 为准，再补 LANG 无意义。
	t.Setenv("LC_ALL", "C")
	env = agentCoreToolEnv()
	if _, ok := scanEnvValue(env, "LANG"); ok {
		t.Fatalf("LC_ALL=C 在场时不应再补 LANG")
	}
}

// 系统提示必须带环境块：CLI provider 由 claude CLI 自动注入，内核没有 CLI，
// 不补齐则模型对平台/cwd/日期全盲，只能靠（可能失真的）Bash 探测猜环境。
func TestAgentCoreSystemPromptIncludesEnvBlock(t *testing.T) {
	dir := t.TempDir()
	prompt := agentCoreSystemPrompt(dir)
	if !strings.Contains(prompt, "Working directory: "+dir) {
		t.Fatalf("缺 cwd: %s", prompt)
	}
	if !strings.Contains(prompt, "Is a git repository: no") {
		t.Fatalf("临时目录应判非 git: %s", prompt)
	}
	if !strings.Contains(prompt, "Platform: "+runtime.GOOS) {
		t.Fatalf("缺平台: %s", prompt)
	}
	if !strings.Contains(prompt, "Today's date: "+time.Now().Format("2006-01-02")) {
		t.Fatalf("缺日期: %s", prompt)
	}
	if !strings.Contains(prompt, "OS Version: ") {
		t.Fatalf("缺 OS 版本: %s", prompt)
	}
	// aliang-options 卡片协议必须保留（手机方案选择依赖）。
	if !strings.Contains(prompt, agentAIOptionSystemPrompt) {
		t.Fatalf("options 协议丢失")
	}

	gitDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agentCoreSystemPrompt(gitDir), "Is a git repository: yes") {
		t.Fatalf("git 目录应判 yes")
	}
}

// 内核读取链的显式层(customer.agentcore_llm)。
//
// 优先级约定:显式设置 > 继承链(env → settings.json)> 回环闸/兜底。回环闸
// 只拦**继承链**里意外带进来的远端地址;用户在仪表盘里显式填远端是知情行为,
// 直接放行。显式 key 不进 token 缓存(改配置下一回合立即生效);显式端点没有
// 「自愈」概念(POST /api/run/start 拉不活别人的进程),只做拨测。

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
