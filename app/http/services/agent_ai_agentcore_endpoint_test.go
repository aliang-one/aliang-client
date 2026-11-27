package services

// 内核三解析点接 endpoint registry(spec 2026-11-25 §8 / Task 5)。
//
// 旧字段 EffectiveAgentCoreLLM(customer.agentcore_llm)已硬退役(spec 二期
// §9):出网只认 registry 出口解析,旧字段零影响,首次解析打一条幂等 Warn。
// GetEndpointService 单例 sync.Once 构造失败会缓存 nil(测试无法重置),故经
// agentCoreGetEndpointServiceFn 注入独立 service(形状对齐
// agentCoreGetUserAPIKeysFn)。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
	"aliang.one/nursorgate/processor/tcp"
)

// stubAgentCoreEndpointService 把内核的 registry 入口替换为独立 :memory:
// service;返回 nil 时模拟 GetEndpointService 构造失败降级。
func stubAgentCoreEndpointService(t *testing.T, svc *EndpointService) {
	t.Helper()
	orig := agentCoreGetEndpointServiceFn
	agentCoreGetEndpointServiceFn = func() *EndpointService { return svc }
	t.Cleanup(func() { agentCoreGetEndpointServiceFn = orig })
}

// isolateAgentCoreRegistryEnv 隔离继承链与全局配置:registry 未命中时内核
// 落回的环境/settings 必须可控(测试机可能有真配置)。ResetGlobalConfigForTest
// 同时复位出口链路委托(夹具 setAgentCoreDelegateProbeEnv 会开 mesh,委托激活
// 时探针被跳过,自愈链用例随之失真)。
func isolateAgentCoreRegistryEnv(t *testing.T) {
	t.Helper()
	isolateAgentCoreBaseURL(t)
	isolateAgentCoreToken(t)
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)
}

// ---- agentCoreBaseURL --------------------------------------------------------

// spec 二期 §9:旧字段已硬退役——enabled+base_url 配齐也不驱动出网,出网仍走
// registry 解析(选中 BYO 行 base_url),旧字段零影响。
func TestAgentCoreBaseURLOldFieldIgnoredRegistryDrives(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", "sk-byo"))

	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-x",
	})
	if got := agentCoreBaseURL(); got != "http://127.0.0.1:9101" {
		t.Errorf("→ %q, want registry BYO base_url(旧字段被忽略)", got)
	}
}

// registry 选中 BYO 行 → 其 base_url(不再落 56432 默认)。
func TestAgentCoreBaseURLRegistryBYO(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", "sk-byo"))

	if got := agentCoreBaseURL(); got != "http://127.0.0.1:9101" {
		t.Errorf("→ %q, want BYO base_url", got)
	}
}

// 无 BYO 候选 → Select 回落 builtin(base_url 空 = 平台隧道)→ 不改变现状,
// 落继承链(env 空)→ 默认平台代理地址。
func TestAgentCoreBaseURLBuiltinFallsToDefault(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	newAgentCoreEndpointRegistryStore(t)

	if got := agentCoreBaseURL(); got != agentCoreDefaultBaseURL {
		t.Errorf("→ %q, want %s(builtin 落继承链,env 空兜底默认)", got, agentCoreDefaultBaseURL)
	}
}

// registry service 缺失(构造失败降级)→ 原继承链逻辑(回环 env 原样采用)。
func TestAgentCoreBaseURLNilServiceDegradesToInherited(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	stubAgentCoreEndpointService(t, nil)
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9999")

	if got := agentCoreBaseURL(); got != "http://127.0.0.1:9999" {
		t.Errorf("→ %q, want 继承链回环地址(nil service 降级)", got)
	}
}

// ---- agentCoreAuthToken ------------------------------------------------------

// spec 二期 §9:旧字段 key 已退役——registry BYO key 驱动出网,旧 key 零影响
// (也不经它短路 user-center)。
func TestAgentCoreAuthTokenOldFieldIgnoredRegistryDrives(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", " sk-byo "))
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		t.Error("BYO key 命中时不该打 user-center")
		return nil, nil
	})

	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: " sk-old ",
	})
	if got := agentCoreAuthToken(); got != "sk-byo" {
		t.Errorf("→ %q, want sk-byo(旧字段 key 被忽略,registry BYO 优先)", got)
	}
}

// registry 选中 BYO 行 → 其 api_key,优先于 env/settings 继承链。
func TestAgentCoreAuthTokenRegistryBYOBeatsInherited(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", " sk-byo "))
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-from-env")
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		t.Error("BYO key 命中时不该打 user-center")
		return nil, nil
	})

	if got := agentCoreAuthToken(); got != "sk-byo" {
		t.Errorf("→ %q, want sk-byo(trim 后,优先于 env)", got)
	}
}

// builtin 选中(空 key = 平台隧道走登录态)→ 现有链:env → user-center 缓存。
func TestAgentCoreAuthTokenBuiltinFallsToExistingChain(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	newAgentCoreEndpointRegistryStore(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-from-env")

	if got := agentCoreAuthToken(); got != "sk-from-env" {
		t.Errorf("→ %q, want sk-from-env(builtin 空 key 落现有链)", got)
	}
}

// nil service → 原链(user-center 兜底)。
func TestAgentCoreAuthTokenNilServiceDegradesToExistingChain(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	stubAgentCoreEndpointService(t, nil)
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		return []auth.UserAPIKey{{Key: "sk-user", SecretAvailable: true, Status: "active"}}, nil
	})

	if got := agentCoreAuthToken(); got != "sk-user" {
		t.Errorf("→ %q, want sk-user(nil service 降级)", got)
	}
}

// ---- agentCoreEnsureUpstream ---------------------------------------------------

// BYO 行 HTTP 探测:200 可达;401 也算可达(链路通、key 问题留给请求本身报)。
func TestAgentCoreEnsureUpstreamRegistryBYOProbe(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)

	t.Run("200 可达", func(t *testing.T) {
		store := newAgentCoreEndpointRegistryStore(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				t.Errorf("探测路径 = %q, want /v1/models", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer sk-probe" {
				t.Errorf("Authorization = %q, want Bearer sk-probe", got)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		createEndpoint(t, store, byoEndpoint("ep-ok", srv.URL, "sk-probe"))

		if err := agentCoreEnsureUpstream(context.Background()); err != nil {
			t.Fatalf("200 应判可达: %v", err)
		}
	})

	t.Run("401 算可达", func(t *testing.T) {
		store := newAgentCoreEndpointRegistryStore(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		createEndpoint(t, store, byoEndpoint("ep-401", srv.URL, "sk-bad"))

		if err := agentCoreEnsureUpstream(context.Background()); err != nil {
			t.Fatalf("401 应判可达(链路通): %v", err)
		}
	})

	t.Run("500 报可自查错误", func(t *testing.T) {
		store := newAgentCoreEndpointRegistryStore(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		createEndpoint(t, store, byoEndpoint("ep-500", srv.URL, "sk-x"))

		err := agentCoreEnsureUpstream(context.Background())
		if err == nil || !containsProbeErr(err.Error(), "自定义 LLM 端点") {
			t.Errorf("→ %v, want 含「自定义 LLM 端点」的可自查文案", err)
		}
	})

	t.Run("TCP 不可达报错", func(t *testing.T) {
		store := newAgentCoreEndpointRegistryStore(t)
		createEndpoint(t, store, byoEndpoint("ep-dead", "http://127.0.0.1:1", "sk-x"))

		err := agentCoreEnsureUpstream(context.Background())
		if err == nil || !containsProbeErr(err.Error(), "自定义 LLM 端点不可达") {
			t.Errorf("→ %v, want 含「自定义 LLM 端点不可达」", err)
		}
	})
}

// spec 二期 §9:旧字段已退役——registry BYO(活服务)被探测,旧字段(死端口)
// 零影响:若旧字段仍被探测必报错,断言通过即证明被忽略。
func TestAgentCoreEnsureUpstreamOldFieldIgnoredRegistryDrives(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	createEndpoint(t, store, byoEndpoint("ep-live", srv.URL, "sk-live"))

	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:1"})

	if err := agentCoreEnsureUpstream(context.Background()); err != nil {
		t.Fatalf("registry BYO 应被探测(可达),旧字段死端口不应参与: %v", err)
	}
}

// builtin 选中(无 BYO 候选)→ 维持原自愈链:委托关闭时回环探针拨达即通过。
// 夹具先开 mesh(委托激活),再用 swapGlobalCustomerMeshFallback 关掉——顺序
// 不能反:setAgentCoreDelegateProbeEnv 会重置全局配置并重新开委托。
func TestAgentCoreEnsureUpstreamBuiltinKeepsSelfHealChain(t *testing.T) {
	accepted := setAgentCoreDelegateProbeEnv(t)
	swapGlobalCustomerMeshFallback(t, false) // 委托关闭 → 探针恢复
	newAgentCoreEndpointRegistryStore(t)     // registry 仅 builtin 行

	if tcp.MeshDelegateActive() {
		t.Fatal("委托应已关闭(前置自检)")
	}
	if err := agentCoreEnsureUpstream(context.Background()); err != nil {
		t.Fatalf("builtin 自愈链探针可达应通过: %v", err)
	}
	expectProbeDialed(t, accepted, "builtin 自愈链探针")
}

// nil service → 原逻辑(回环 env 探针目标被拨)。
func TestAgentCoreEnsureUpstreamNilServiceDegrades(t *testing.T) {
	accepted := setAgentCoreDelegateProbeEnv(t)
	swapGlobalCustomerMeshFallback(t, false)
	stubAgentCoreEndpointService(t, nil)

	if err := agentCoreEnsureUpstream(context.Background()); err != nil {
		t.Fatalf("nil service 应走原自愈链且探针可达: %v", err)
	}
	expectProbeDialed(t, accepted, "nil service 降级探针")
}

// ---- 旧字段退役告警(spec 二期 §9)----------------------------------------------

// newLegacyLLMWarnCapture 换上告警观测缝并复位幂等闸,返回消息收集器;
// cleanup 恢复生产缝与闸。
func newLegacyLLMWarnCapture(t *testing.T) *[]string {
	t.Helper()
	resetAgentCoreLegacyLLMWarnForTest()
	msgs := &[]string{}
	orig := agentCoreLegacyLLMWarnHook
	agentCoreLegacyLLMWarnHook = func(msg string) { *msgs = append(*msgs, msg) }
	t.Cleanup(func() {
		agentCoreLegacyLLMWarnHook = orig
		resetAgentCoreLegacyLLMWarnForTest()
	})
	return msgs
}

// 幂等:旧字段在配置里时首次解析打一条 Warn,同进程后续解析(任一解析点)不再
// 重复——防每 run 刷屏。
func TestAgentCoreLegacyLLMWarnFiresOncePerProcess(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	newAgentCoreEndpointRegistryStore(t)
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-old",
	})
	msgs := newLegacyLLMWarnCapture(t)

	_ = agentCoreBaseURL()   // 首次解析:触发
	_ = agentCoreBaseURL()   // 同解析点重复:静默
	_ = agentCoreAuthToken() // 另一解析点:静默

	if len(*msgs) != 1 {
		t.Fatalf("Warn 应只打一次, got %d: %v", len(*msgs), *msgs)
	}
	if !strings.Contains((*msgs)[0], "已忽略") || !strings.Contains((*msgs)[0], "endpoint") {
		t.Fatalf("文案应说明「旧内核端点配置已忽略，出网走 endpoint 管理」, got %q", (*msgs)[0])
	}
}

// 未配置旧字段 → 不告警,且幂等闸不被消耗(中途清掉再配回来仍会提醒一次)。
func TestAgentCoreLegacyLLMNoWarnWithoutOldField(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	newAgentCoreEndpointRegistryStore(t)
	msgs := newLegacyLLMWarnCapture(t)

	_ = agentCoreBaseURL()
	_ = agentCoreAuthToken()

	if len(*msgs) != 0 {
		t.Fatalf("无旧字段不应告警, got %v", *msgs)
	}
}

// ---- Select 返回错误 → 三处解析点全部回落继承链 -------------------------------

// makeRegistrySelectError 起一个 registry 但让 Select("anthropic") 返回
// ErrNoEndpoint：内置行 disabled + 远期冷却，auto 池无其他候选。
func makeRegistrySelectError(t *testing.T) *EndpointService {
	t.Helper()
	svc, err := NewEndpointServiceForTest()
	if err != nil {
		t.Fatalf("起测试 registry: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	stubAgentCoreEndpointService(t, svc)
	now := time.Now().Unix()
	if err := svc.store.UpdateHealth(storage.BuiltinEndpointID, storage.HealthUpdate{
		Status:           "disabled",
		Failures:         1,
		CooldownUntil:    now + 86400,
		RateLimitedUntil: 0,
		LastErr:          "test select error",
	}); err != nil {
		t.Fatalf("标内置行 disabled: %v", err)
	}
	return svc
}

// Select error → baseURL 落继承链(env 回环原样采用)。
func TestAgentCoreBaseURLSelectErrorFallsToInherited(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	makeRegistrySelectError(t)
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9999")

	if got := agentCoreBaseURL(); got != "http://127.0.0.1:9999" {
		t.Errorf("→ %q, want 继承链 env 值(Select 失败回落)", got)
	}
}

// Select error → auth 落继承链(env，不打 user-center)。
func TestAgentCoreAuthTokenSelectErrorFallsToInherited(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	makeRegistrySelectError(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-from-env")
	stubAgentCoreUserAPIKeys(t, func() ([]auth.UserAPIKey, error) {
		t.Error("Select 失败且 env 有值时不该打 user-center")
		return nil, nil
	})

	if got := agentCoreAuthToken(); got != "sk-from-env" {
		t.Errorf("→ %q, want sk-from-env(Select 失败回落)", got)
	}
}

// Select error → ensure upstream 落原自愈链(回环探针被拨)。
func TestAgentCoreEnsureUpstreamSelectErrorFallsToSelfHeal(t *testing.T) {
	makeRegistrySelectError(t)
	accepted := setAgentCoreDelegateProbeEnv(t)
	swapGlobalCustomerMeshFallback(t, false)

	if err := agentCoreEnsureUpstream(context.Background()); err != nil {
		t.Fatalf("Select 失败应走原自愈链且探针可达: %v", err)
	}
	expectProbeDialed(t, accepted, "Select 失败降级探针")
}

// ---- 共用脚手架 ---------------------------------------------------------------

// newAgentCoreEndpointRegistryStore 起 registry 并返回 store(不注入 service,
// 供需要自行组装的用例)。
func newAgentCoreEndpointRegistryStore(t *testing.T) *storage.EndpointStore {
	t.Helper()
	svc, err := NewEndpointServiceForTest()
	if err != nil {
		t.Fatalf("起测试 registry: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	stubAgentCoreEndpointService(t, svc)
	return svc.store
}

// byoEndpoint 造一个 anthropic BYO 行:回环 base_url(免 api_key 校验)、
// 进 auto 池、显式优先级(避开 Create 的 0→50 归一)。
func byoEndpoint(id, baseURL, apiKey string) *config.LLMEndpoint {
	return &config.LLMEndpoint{
		ID:                id,
		Name:              "name-" + id,
		BaseURL:           baseURL,
		APIKey:            apiKey,
		ProtocolAnthropic: true,
		AutoGroup:         true,
		Enabled:           true,
		Priority:          10,
		Models:            "[]",
	}
}

// containsProbeErr 断言错误文案包含子串。
func containsProbeErr(s, sub string) bool { return strings.Contains(s, sub) }
