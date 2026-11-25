package services

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// ---- 测试脚手架 ------------------------------------------------------------

// newEndpointTestHarness 起一个 :memory: 独立库的 store + 注入构造的 service,
// 完全不碰真实数据目录;Close 挂在 t.Cleanup 上。
func newEndpointTestHarness(t *testing.T) (*EndpointService, *storage.EndpointStore) {
	t.Helper()
	store, err := storage.NewEndpointStoreForTest()
	if err != nil {
		t.Fatalf("起测试 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newEndpointServiceWithStore(store), store
}

// createEndpoint 落一行;失败直接 Fatal(Create 的校验语义也是被测契约的一部分)。
func createEndpoint(t *testing.T, store *storage.EndpointStore, e *config.LLMEndpoint) {
	t.Helper()
	if err := store.Create(e); err != nil {
		t.Fatalf("create endpoint %s: %v", e.ID, err)
	}
}

// mustGet 按 id 取行,供测试断言落库健康态。
func mustGet(t *testing.T, store *storage.EndpointStore, id string) *config.LLMEndpoint {
	t.Helper()
	e, err := store.Get(id)
	if err != nil {
		t.Fatalf("get endpoint %s: %v", id, err)
	}
	return e
}

// anthropicEP 造一个默认可用的 anthropic BYO 行:回环 base_url(免 api_key 校验),
// 不限模型,进 auto 池,显式优先级(避开 Create 的 0→50 归一)。
func anthropicEP(id string, priority int) *config.LLMEndpoint {
	return &config.LLMEndpoint{
		ID:                id,
		Name:              "name-" + id,
		BaseURL:           "http://127.0.0.1:9/" + id,
		ProtocolAnthropic: true,
		AutoGroup:         true,
		Enabled:           true,
		Priority:          priority,
		Models:            "[]",
	}
}

// markHealth 走生产写入口(UpdateHealth)直接摆健康态,测过滤/恢复用。
func markHealth(t *testing.T, store *storage.EndpointStore, id string, u storage.HealthUpdate) {
	t.Helper()
	if err := store.UpdateHealth(id, u); err != nil {
		t.Fatalf("update health %s: %v", id, err)
	}
}

// waitUntil 轮询等待条件成立(并发测试里等队列登记/令牌流动),超时报 Fatal。
func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// ---- Select:协议过滤 / 优先级排序 -----------------------------------------

func TestEndpointService_SelectProtocolAndPriority(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	// epC 只开 openai 协议但优先级最高:anthropic 请求必须跳过它。
	epC := anthropicEP("ep-c", 5)
	epC.ProtocolAnthropic = false
	epC.ProtocolOpenAI = true
	createEndpoint(t, store, epC)
	epA := anthropicEP("ep-a", 10)
	createEndpoint(t, store, epA)
	epB := anthropicEP("ep-b", 20)
	createEndpoint(t, store, epB)

	// 内置行不进自动池:builtin 恒兜底,否则 priority=0 会把 BYO 全部饿死。
	got, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("select anthropic: %v", err)
	}
	if got.ID != "ep-a" {
		t.Fatalf("优先级排序应取 ep-a(priority 10), got %s", got.ID)
	}

	got, err = svc.Select("openai", "", "")
	if err != nil {
		t.Fatalf("select openai: %v", err)
	}
	if got.ID != "ep-c" {
		t.Fatalf("openai 协议过滤应取 ep-c, got %s", got.ID)
	}

	// 同优先级按 created_at 升序(gorm 对非零 CreatedAt 原样落库,可显式钉值)。
	// 两条都给 priority 5(低于 ep-a 的 10,保证决出的是这对同行)。
	tieB := anthropicEP("ep-tie-b", 5)
	tieB.CreatedAt = 2000
	createEndpoint(t, store, tieB)
	tieA := anthropicEP("ep-tie-a", 5)
	tieA.CreatedAt = 1000
	createEndpoint(t, store, tieA)
	got, err = svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("select tie: %v", err)
	}
	if got.ID != "ep-tie-a" {
		t.Fatalf("同优先级应按 created_at 升序取 ep-tie-a, got %s", got.ID)
	}
}

// ---- Select:模型过滤 -------------------------------------------------------

func TestEndpointService_SelectModelFilter(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	// ep-narrow 优先级最高但 models 不含 glm-4.6:请求 glm-4.6 时必须跳过。
	narrow := anthropicEP("ep-narrow", 5)
	narrow.Models = `["claude-sonnet-4"]`
	createEndpoint(t, store, narrow)
	wide := anthropicEP("ep-wide", 10)
	wide.Models = `["glm-4.6","kimi-k2"]`
	createEndpoint(t, store, wide)
	unlimited := anthropicEP("ep-unlimited", 20)
	unlimited.Models = "[]" // 不限制
	createEndpoint(t, store, unlimited)

	got, err := svc.Select("anthropic", "glm-4.6", "")
	if err != nil {
		t.Fatalf("select glm-4.6: %v", err)
	}
	if got.ID != "ep-wide" {
		t.Fatalf("模型过滤应命中 ep-wide, got %s", got.ID)
	}

	// 请求模型不在任何受限列表里:只剩不限模型的 ep-unlimited。
	got, err = svc.Select("anthropic", "unknown-model", "")
	if err != nil {
		t.Fatalf("select unknown model: %v", err)
	}
	if got.ID != "ep-unlimited" {
		t.Fatalf("未匹配模型应落不限模型的 ep-unlimited, got %s", got.ID)
	}

	// requestedModel 为空:跳过模型过滤,严格按优先级取 ep-narrow。
	got, err = svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("select empty model: %v", err)
	}
	if got.ID != "ep-narrow" {
		t.Fatalf("空 model 应跳过过滤取 ep-narrow, got %s", got.ID)
	}
}

// ---- Select:禁用 / 429 冷却过滤 ---------------------------------------------

func TestEndpointService_SelectSkipsDisabledAndRateLimited(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	disabled := anthropicEP("ep-disabled", 5)
	disabled.Enabled = false
	createEndpoint(t, store, disabled)

	rateLimited := anthropicEP("ep-429", 10)
	createEndpoint(t, store, rateLimited)
	future := time.Now().Add(10 * time.Minute).Unix()
	markHealth(t, store, "ep-429", storage.HealthUpdate{
		Status: "ok", Failures: 0, CooldownUntil: 0, RateLimitedUntil: future, LastErr: "429",
	})

	healthy := anthropicEP("ep-ok", 20)
	createEndpoint(t, store, healthy)

	got, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.ID != "ep-ok" {
		t.Fatalf("禁用行与 429 冷却中行都应被过滤, got %s", got.ID)
	}
}

// ---- 回落:builtin 兜底 ------------------------------------------------------

func TestEndpointService_FallbackToBuiltin(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	// 场景 1:无任何 BYO 候选(仅内置行在库)→ 回落 builtin。
	got, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("空池应回落 builtin: %v", err)
	}
	if got.ID != storage.BuiltinEndpointID {
		t.Fatalf("应回落内置行, got %s", got.ID)
	}
	if !got.Builtin || got.BaseURL != "" {
		t.Fatalf("回落行应为 builtin 语义(base_url 空), got %+v", got)
	}

	// 场景 2:BYO 候选全被禁用 + builtin 健康禁用(disabled 且冷却未到期)→ ErrNoEndpoint。
	byo := anthropicEP("ep-byo", 10)
	byo.Enabled = false
	createEndpoint(t, store, byo)
	future := time.Now().Add(10 * time.Minute).Unix()
	markHealth(t, store, storage.BuiltinEndpointID, storage.HealthUpdate{
		Status: "disabled", Failures: 3, CooldownUntil: future, LastErr: "boom",
	})
	if _, err := svc.Select("anthropic", "", ""); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("builtin 也冷却中应 ErrNoEndpoint, got %v", err)
	}

	// 场景 3:builtin 429 冷却中 → ErrNoEndpoint。
	markHealth(t, store, storage.BuiltinEndpointID, storage.HealthUpdate{
		Status: "ok", Failures: 0, CooldownUntil: 0, RateLimitedUntil: future, LastErr: "429",
	})
	if _, err := svc.Select("openai", "", ""); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("builtin 429 冷却中应 ErrNoEndpoint, got %v", err)
	}

	// 场景 4:builtin 被用户手动停用(enabled=0)→ 不回落。
	markHealth(t, store, storage.BuiltinEndpointID, storage.HealthUpdate{Status: "ok"})
	b := mustGet(t, store, storage.BuiltinEndpointID)
	b.Enabled = false
	if err := store.Update(b); err != nil {
		t.Fatalf("停用 builtin 行: %v", err)
	}
	if _, err := svc.Select("anthropic", "", ""); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("builtin 停用后应 ErrNoEndpoint, got %v", err)
	}

	// 场景 5:显式指定不存在的 endpoint 不回落 builtin(显式就是显式)。
	if _, err := svc.Select("anthropic", "", "no-such-id"); !errors.Is(err, ErrEndpointNotAvailable) {
		t.Fatalf("显式指定不存在的 id 应 ErrEndpointNotAvailable, got %v", err)
	}
}

// ---- Select:显式指定(auto=0 行 / 协议校验 / 健康旁路) ----------------------

func TestEndpointService_SelectExplicitID(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	ep := anthropicEP("ep-manual", 10)
	ep.AutoGroup = false // auto=0 行只允许显式指定
	ep.MaxConcurrency = 3
	createEndpoint(t, store, ep)

	got, err := svc.Select("anthropic", "any-model", "ep-manual")
	if err != nil {
		t.Fatalf("显式指定 auto=0 行: %v", err)
	}
	if got.ID != "ep-manual" || got.MaxConcurrency != 3 {
		t.Fatalf("显式指定应返回该行本身, got %+v", got)
	}

	// 协议不匹配 → ErrEndpointNotAvailable(路由层映射 404)。
	if _, err := svc.Select("openai", "", "ep-manual"); !errors.Is(err, ErrEndpointNotAvailable) {
		t.Fatalf("协议不匹配应 ErrEndpointNotAvailable, got %v", err)
	}

	// 未启用 → ErrEndpointNotAvailable。
	ep.Enabled = false
	if err := store.Update(ep); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := svc.Select("anthropic", "", "ep-manual"); !errors.Is(err, ErrEndpointNotAvailable) {
		t.Fatalf("停用行显式指定应 ErrEndpointNotAvailable, got %v", err)
	}

	// 健康禁用行显式指定仍放行(显式指定只校验协议/启用,管理/强制用途)。
	ep.Enabled = true
	if err := store.Update(ep); err != nil {
		t.Fatalf("复用: %v", err)
	}
	markHealth(t, store, "ep-manual", storage.HealthUpdate{
		Status: "disabled", Failures: 3,
		CooldownUntil: time.Now().Add(10 * time.Minute).Unix(), LastErr: "boom",
	})
	if _, err := svc.Select("anthropic", "", "ep-manual"); err != nil {
		t.Fatalf("显式指定应旁路健康过滤, got %v", err)
	}
}

// ---- ReportFailure:计数 / 摘除 / 429 / 4xx / 成功 ---------------------------

func TestEndpointService_ReportFailureThresholdDisables(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-f", 10))

	// 默认策略 threshold=3:前两次只计数,第三次摘除。
	svc.ReportFailure("ep-f", 502, "bad gateway")
	svc.ReportFailure("ep-f", 503, "service unavailable")
	got := mustGet(t, store, "ep-f")
	if got.ConsecutiveFailures != 2 || got.HealthStatus != "ok" {
		t.Fatalf("未达阈值只应计数: failures=%d status=%s", got.ConsecutiveFailures, got.HealthStatus)
	}
	if got.LastError == "" {
		t.Fatalf("失败应记录 last_error 供面板展示")
	}

	before := time.Now().Unix()
	svc.ReportFailure("ep-f", 500, "internal error")
	got = mustGet(t, store, "ep-f")
	if got.HealthStatus != "disabled" || got.ConsecutiveFailures != 3 {
		t.Fatalf("达阈值应摘除: status=%s failures=%d", got.HealthStatus, got.ConsecutiveFailures)
	}
	if got.CooldownUntil <= before || got.CooldownUntil > before+301 {
		t.Fatalf("cooldown_until 应为 now+300s 附近, got %d (before %d)", got.CooldownUntil, before)
	}

	// 摘除后 Select 跳过它 → 回落 builtin。
	got2, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("摘除后应回落 builtin: %v", err)
	}
	if got2.ID != storage.BuiltinEndpointID {
		t.Fatalf("摘除后应回落内置行, got %s", got2.ID)
	}
}

func TestEndpointService_ReportFailure429Cooldowns(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-r", 10))

	before := time.Now().Unix()
	svc.ReportFailure("ep-r", 429, "rate limited")
	got := mustGet(t, store, "ep-r")
	// 429 不计数、不摘除,只写 rate_limited_until。
	if got.RateLimitedUntil <= before || got.RateLimitedUntil > before+61 {
		t.Fatalf("rate_limited_until 应为 now+60s 附近, got %d", got.RateLimitedUntil)
	}
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" || got.CooldownUntil != 0 {
		t.Fatalf("429 不得计数/摘除: %+v", got)
	}

	// 429 冷却中 Select 跳过 → 回落 builtin。
	got2, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("429 冷却中应回落 builtin: %v", err)
	}
	if got2.ID != storage.BuiltinEndpointID {
		t.Fatalf("应回落内置行, got %s", got2.ID)
	}

	// 冷却到期后 Select 应恢复返回该行(过滤条件放行)。
	markHealth(t, store, "ep-r", storage.HealthUpdate{Status: "ok", RateLimitedUntil: time.Now().Unix() - 1})
	got3, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("429 到期后应可选: %v", err)
	}
	if got3.ID != "ep-r" {
		t.Fatalf("429 到期后应选回 ep-r, got %s", got3.ID)
	}
}

func TestEndpointService_ReportFailure4xxIgnored(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-x", 10))

	// 其它 4xx 不计数不落状态(401/403/404 反复打也不摘)。
	for i := 0; i < 5; i++ {
		svc.ReportFailure("ep-x", 401, "unauthorized")
	}
	got := mustGet(t, store, "ep-x")
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 {
		t.Fatalf("4xx 必须完全忽略: %+v", got)
	}

	// status=0(连接失败/超时)按 5xx 同口径计数。
	svc.ReportFailure("ep-x", 0, "dial tcp: connection refused")
	got = mustGet(t, store, "ep-x")
	if got.ConsecutiveFailures != 1 || got.LastError == "" {
		t.Fatalf("连接失败应计数: %+v", got)
	}
}

func TestEndpointService_ReportSuccessClears(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-s", 10))

	svc.ReportFailure("ep-s", 500, "boom")
	svc.ReportFailure("ep-s", 429, "rate")
	svc.ReportSuccess("ep-s")

	got := mustGet(t, store, "ep-s")
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("成功应清零全部健康态: %+v", got)
	}
}

func TestEndpointService_PoolModeWritesNothing(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-p", 10))

	// 池模式(failure_disable=0):失败不落任何状态(含 429)。
	if err := store.SetPolicy(&config.EndpointGroupPolicy{
		FailureDisable: false, FailureThreshold: 1, CooldownSec: 60, RateLimitCooldownSec: 60,
	}); err != nil {
		t.Fatal(err)
	}
	svc.ReportFailure("ep-p", 500, "boom")
	svc.ReportFailure("ep-p", 500, "boom")
	svc.ReportFailure("ep-p", 429, "rate limited")
	svc.ReportSuccess("ep-p") // 也不得写

	got := mustGet(t, store, "ep-p")
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("池模式不得写任何健康状态: %+v", got)
	}
}

// ---- Acquire:并发排队 -------------------------------------------------------

func TestEndpointService_AcquireQueuesUnderLimit(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	ep := anthropicEP("ep-c", 10)
	ep.MaxConcurrency = 2
	createEndpoint(t, store, ep)

	// max_concurrency=2:前两个立即拿到,第三个排队。
	rel1, err := svc.Acquire("ep-c", 0) // 0 = 以库里 max_concurrency 为准
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	rel2, err := svc.Acquire("ep-c", 2)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if n := svc.Inflight()["ep-c"]; n != 2 {
		t.Fatalf("inflight 应为 2, got %d", n)
	}

	acquired := make(chan func(), 1)
	go func() {
		rel3, err := svc.Acquire("ep-c", 2)
		if err != nil {
			acquired <- nil
			return
		}
		acquired <- rel3
	}()
	waitUntil(t, "第三个请求应进入排队", time.Second, func() bool { return svc.queueDepth("ep-c") == 1 })
	select {
	case rel3 := <-acquired:
		t.Fatalf("第三个请求不该在释放前拿到令牌, got %v", rel3 != nil)
	case <-time.After(80 * time.Millisecond):
	}

	rel1() // 释放一个令牌,排队的第三个应立即接上
	select {
	case rel3 := <-acquired:
		if rel3 == nil {
			t.Fatalf("排队者不应拿到 ErrQueueTimeout")
		}
		rel2()
		rel3()
	case <-time.After(2 * time.Second):
		t.Fatalf("释放后排队者未能接上")
	}
	if n := svc.Inflight()["ep-c"]; n != 0 {
		t.Fatalf("全部释放后 inflight 应清空, got %d", n)
	}
}

func TestEndpointService_AcquireQueueFullRejects(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	ep := anthropicEP("ep-q", 10)
	ep.MaxConcurrency = 1
	createEndpoint(t, store, ep)

	rel0, err := svc.Acquire("ep-q", 1)
	if err != nil {
		t.Fatalf("acquire hold: %v", err)
	}

	// 32 个排队者:队列深度的硬上限。
	drained := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func() {
			rel, err := svc.Acquire("ep-q", 1)
			if err != nil {
				drained <- err
				return
			}
			rel()
			drained <- nil
		}()
	}
	waitUntil(t, "32 个排队者应全部登记", 2*time.Second, func() bool { return svc.queueDepth("ep-q") == 32 })

	// 第 33 个等待者直接拒绝,不排队不等待。
	if _, err := svc.Acquire("ep-q", 1); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("第 33 个等待者应 ErrQueueTimeout, got %v", err)
	}

	rel0() // 放行,32 个排队者应依次全部拿到并释放
	for i := 0; i < 32; i++ {
		select {
		case err := <-drained:
			if err != nil {
				t.Fatalf("排队者不应失败, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("排队者未全部排空(第 %d 个)", i+1)
		}
	}
}

func TestEndpointService_AcquireTimeout(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	ep := anthropicEP("ep-t", 10)
	ep.MaxConcurrency = 1
	createEndpoint(t, store, ep)
	svc.waitTimeout = 120 * time.Millisecond // 测试注入短超时,免等 30s

	rel, err := svc.Acquire("ep-t", 1)
	if err != nil {
		t.Fatalf("acquire hold: %v", err)
	}
	defer rel()

	start := time.Now()
	if _, err := svc.Acquire("ep-t", 1); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("等待超时应 ErrQueueTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("超时来得太快(未等满注入的 waitTimeout): %v", elapsed)
	}
}

func TestEndpointService_AcquireUnlimited(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-u", 10)) // max_concurrency=0 = 不限

	// 不限并发:任意多个 Acquire 都立即返回,release 只减 inflight。
	var rels []func()
	for i := 0; i < 8; i++ {
		rel, err := svc.Acquire("ep-u", 0)
		if err != nil {
			t.Fatalf("acquire %d(不限): %v", i, err)
		}
		rels = append(rels, rel)
	}
	if n := svc.Inflight()["ep-u"]; n != 8 {
		t.Fatalf("不限并发 inflight 应为 8, got %d", n)
	}
	for _, rel := range rels {
		rel()
	}
	if _, ok := svc.Inflight()["ep-u"]; ok {
		t.Fatalf("释放干净后 inflight 不应残留该 id")
	}
}

// ---- 懒恢复 / 重置健康 ------------------------------------------------------

func TestEndpointService_LazyRecoveryOnSelect(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-l", 10))

	// 摘除态:disabled + 计数 3 + 冷却已过期(unix 秒设在过去,不做 mock clock)。
	markHealth(t, store, "ep-l", storage.HealthUpdate{
		Status: "disabled", Failures: 3, CooldownUntil: time.Now().Unix() - 5, LastErr: "boom",
	})

	got, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("冷却到期应懒恢复并可被选中: %v", err)
	}
	if got.ID != "ep-l" {
		t.Fatalf("应选中恢复后的 ep-l, got %s", got.ID)
	}
	gotRow := mustGet(t, store, "ep-l")
	if gotRow.HealthStatus != "ok" || gotRow.ConsecutiveFailures != 0 ||
		gotRow.CooldownUntil != 0 || gotRow.LastError != "" {
		t.Fatalf("懒恢复应回 ok 并清零计数: %+v", gotRow)
	}
}

func TestEndpointService_ResetHealth(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-h", 10))

	// 打满阈值摘除。
	for i := 0; i < 3; i++ {
		svc.ReportFailure("ep-h", 500, "boom")
	}
	if got := mustGet(t, store, "ep-h"); got.HealthStatus != "disabled" {
		t.Fatalf("前置:应已摘除, got %s", got.HealthStatus)
	}

	// 手动重置:立即清零且立即可选。
	if err := svc.ResetHealth("ep-h"); err != nil {
		t.Fatalf("reset health: %v", err)
	}
	got := mustGet(t, store, "ep-h")
	if got.HealthStatus != "ok" || got.ConsecutiveFailures != 0 ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("重置健康应立即清零: %+v", got)
	}
	got2, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("重置后应立即可选: %v", err)
	}
	if got2.ID != "ep-h" {
		t.Fatalf("重置后应选中 ep-h, got %s", got2.ID)
	}
}

// ---- Inflight ---------------------------------------------------------------

func TestEndpointService_Inflight(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-i", 10))
	createEndpoint(t, store, anthropicEP("ep-j", 20))

	relI, _ := svc.Acquire("ep-i", 0)
	relI2, _ := svc.Acquire("ep-i", 0)
	relJ, _ := svc.Acquire("ep-j", 0)

	snap := svc.Inflight()
	if snap["ep-i"] != 2 || snap["ep-j"] != 1 {
		t.Fatalf("inflight 计数不符: %+v", snap)
	}

	// 返回的是快照副本,调用方改它不得污染内部状态。
	snap["ep-i"] = 99
	if n := svc.Inflight()["ep-i"]; n != 2 {
		t.Fatalf("Inflight 必须返回副本防污染, got %d", n)
	}

	relI()
	relI2()
	relJ()
	if len(svc.Inflight()) != 0 {
		t.Fatalf("全部释放后 Inflight 应为空: %+v", svc.Inflight())
	}
}

// ---- FetchModels / Test 探测 -------------------------------------------------

// modelsServer 起一个 /v1/models 假上游,记录收到的 Authorization 头。
func modelsServer(t *testing.T, status int, body string) (*httptest.Server, *string) {
	t.Helper()
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &gotAuth
}

func TestEndpointService_FetchModels(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	// OpenAI 风格 {data:[{id}]} + Bearer key 透传。
	srv, gotAuth := modelsServer(t, http.StatusOK,
		`{"object":"list","data":[{"id":"glm-4.6","object":"model"},{"id":"kimi-k2","object":"model"}]}`)
	ep := anthropicEP("ep-m", 10)
	ep.BaseURL = srv.URL
	ep.APIKey = "sk-test-key"
	createEndpoint(t, store, ep)

	ids, err := svc.FetchModels("ep-m")
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}
	if len(ids) != 2 || ids[0] != "glm-4.6" || ids[1] != "kimi-k2" {
		t.Fatalf("应解析出两个模型 id, got %v", ids)
	}
	if *gotAuth != "Bearer sk-test-key" {
		t.Fatalf("应带 Bearer key, got %q", *gotAuth)
	}

	// Anthropic/另一些网关的 {models:[{id}]} 形态也要能解析。
	srv2, _ := modelsServer(t, http.StatusOK, `{"models":[{"id":"deepseek-v3"},{"id":"glm-4.6"}]}`)
	ep2 := anthropicEP("ep-m2", 11)
	ep2.BaseURL = srv2.URL
	createEndpoint(t, store, ep2)
	ids2, err := svc.FetchModels("ep-m2")
	if err != nil {
		t.Fatalf("fetch models(models 形态): %v", err)
	}
	if len(ids2) != 2 || ids2[0] != "deepseek-v3" || ids2[1] != "glm-4.6" {
		t.Fatalf("models 形态应解析出 id 列表, got %v", ids2)
	}

	// 非 200 报错,且绝不能自动写库(models 落库由 PUT 显式确认)。
	srv3, _ := modelsServer(t, http.StatusForbidden, `{"error":"denied"}`)
	ep3 := anthropicEP("ep-m3", 12)
	ep3.BaseURL = srv3.URL
	createEndpoint(t, store, ep3)
	if _, err := svc.FetchModels("ep-m3"); err == nil {
		t.Fatalf("非 200 应报错")
	}
	if got := mustGet(t, store, "ep-m3"); got.Models != "[]" {
		t.Fatalf("FetchModels 绝不能自动写库, models=%q", got.Models)
	}

	// 内置行无 base_url,拉模型必须给清晰报错而不是打空 URL。
	if _, err := svc.FetchModels(storage.BuiltinEndpointID); err == nil {
		t.Fatalf("内置行拉模型应报错")
	}
}

func TestEndpointService_TestProbe(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	// 200 + 模型列表 → ok, count。
	srv, _ := modelsServer(t, http.StatusOK, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
	ep := anthropicEP("ep-ok", 10)
	ep.BaseURL = srv.URL
	createEndpoint(t, store, ep)
	ok, count, summary := svc.Test("ep-ok")
	if !ok || count != 2 || summary != "" {
		t.Fatalf("健康上游应 ok/2/空: %v %d %q", ok, count, summary)
	}

	// 401 也算可达(链路通,key 问题留给请求本身报,口径同 §8 内核探针)。
	srv401, _ := modelsServer(t, http.StatusUnauthorized, `{"error":"auth"}`)
	ep401 := anthropicEP("ep-401", 11)
	ep401.BaseURL = srv401.URL
	createEndpoint(t, store, ep401)
	ok, count, summary = svc.Test("ep-401")
	if !ok || count != 0 || summary == "" {
		t.Fatalf("401 应算可达但模型数 0: %v %d %q", ok, count, summary)
	}

	// 死端口 → 不可达,错误摘要非空。
	epDead := anthropicEP("ep-dead", 12)
	epDead.BaseURL = "http://127.0.0.1:1"
	createEndpoint(t, store, epDead)
	ok, count, summary = svc.Test("ep-dead")
	if ok || count != 0 || summary == "" {
		t.Fatalf("死端口应不可达且带摘要: %v %d %q", ok, count, summary)
	}

	// 内置行无探测目标 → 明确报错。
	ok, _, summary = svc.Test(storage.BuiltinEndpointID)
	if ok || summary == "" {
		t.Fatalf("内置行探测应返回不可达与摘要: %v %q", ok, summary)
	}
}

// ---- ModelsUnion -------------------------------------------------------------

func TestEndpointService_ModelsUnion(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	a := anthropicEP("ep-a", 10)
	a.Models = `["glm-4.6","kimi-k2"]`
	createEndpoint(t, store, a)
	b := anthropicEP("ep-b", 20)
	b.Models = `["kimi-k2","deepseek-v3"]`
	createEndpoint(t, store, b)
	c := anthropicEP("ep-c", 30)
	c.Models = "[]" // 不限制:不贡献具体条目
	createEndpoint(t, store, c)
	d := anthropicEP("ep-d", 40)
	d.Models = `["ghost-model"]`
	d.Enabled = false // 禁用行不贡献
	createEndpoint(t, store, d)

	got := svc.ModelsUnion()
	want := []ModelOwner{
		{ID: "deepseek-v3", Owner: "ep-b"},
		{ID: "glm-4.6", Owner: "ep-a"},
		{ID: "kimi-k2", Owner: "ep-a"}, // 重复声明取第一个(优先级最高)声明者
	}
	if len(got) != len(want) {
		t.Fatalf("并集应去重排序且排除不限/禁用/内置, got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("并集条目不符 want %+v got %+v", want, got)
		}
	}
}

// ---- 错误哨兵(路由层 Task 3 的 404/503 映射依赖) -----------------------------

func TestEndpointService_ErrorSentinels(t *testing.T) {
	if ErrNoEndpoint.Error() == "" {
		t.Fatalf("ErrNoEndpoint 文案异常: %v", ErrNoEndpoint)
	}
	if ErrEndpointNotAvailable.Error() == "" || ErrEndpointNotAvailable.Error() == ErrNoEndpoint.Error() {
		t.Fatalf("ErrEndpointNotAvailable 必须是独立哨兵(404 映射依赖): %v", ErrEndpointNotAvailable)
	}
	if ErrQueueTimeout.Error() == "" {
		t.Fatalf("ErrQueueTimeout 文案异常: %v", ErrQueueTimeout)
	}
}

// ---- quality fix:并发 Select ‖ ReportFailure 交错(Important 1)----------------

func TestEndpointService_SelectDoesNotReviveConcurrentDisable(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-race", 10))

	// 先把 endpoint 打成「disabled + 冷却已过期」:这正是流量恢复再失败的
	// designed operating point——Select 的懒恢复与 ReportFailure 的摘除会
	// 真实交错。循环多轮放大交错窗口。
	for round := 0; round < 50; round++ {
		markHealth(t, store, "ep-race", storage.HealthUpdate{
			Status: "disabled", Failures: 3, CooldownUntil: time.Now().Unix() - 1, LastErr: "boom",
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = svc.Select("anthropic", "", "") }()
		go func() {
			defer wg.Done()
			// 阈值 3、当前计数 3:再失败一次即再次摘除。
			svc.ReportFailure("ep-race", 500, "boom again")
		}()
		wg.Wait()

		// 不变量:交错结束后不得停在「ok 且计数 0」的复活态——要么仍在冷却
		// (摘除被恢复覆盖的反例),要么计数>0(摘除进行中)。两种都说明恢复
		// 把并发写入的摘除抹掉了。
		got := mustGet(t, store, "ep-race")
		if got.HealthStatus == "ok" && got.ConsecutiveFailures == 0 {
			t.Fatalf("round %d: 懒恢复覆盖并发摘除(endpoint 被复活): %+v", round, got)
		}
	}
}

// ---- quality fix:补充边界用例(Minor 5)--------------------------------------

func TestEndpointService_AcquireGateRebuildOnLimitChange(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	ep := anthropicEP("ep-rebuild", 10)
	ep.MaxConcurrency = 1
	createEndpoint(t, store, ep)

	// 旧闸 cap=1:拿满后释放,让闸回到完全空闲(通道满、无排队)。
	rel1, err := svc.Acquire("ep-rebuild", 1)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	rel1()
	waitUntil(t, "闸回到空闲", time.Second, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		g := svc.sems["ep-rebuild"]
		return g != nil && g.waiting == 0 && len(g.sem) == cap(g.sem) && cap(g.sem) == 1
	})

	// 用户把 max_concurrency 改成 2:下一次 Acquire(0=以库里为准)应重建闸。
	ep.MaxConcurrency = 2
	if err := store.Update(ep); err != nil {
		t.Fatal(err)
	}
	rel2, err := svc.Acquire("ep-rebuild", 0)
	if err != nil {
		t.Fatalf("acquire after rebuild: %v", err)
	}
	rel3, err := svc.Acquire("ep-rebuild", 0)
	if err != nil {
		t.Fatalf("acquire 3 after rebuild: %v", err)
	}
	svc.mu.Lock()
	g := svc.sems["ep-rebuild"]
	svc.mu.Unlock()
	if cap(g.sem) != 2 {
		t.Fatalf("闸未按新 max_concurrency 重建, cap=%d", cap(g.sem))
	}
	rel2()
	rel3()
}

func TestEndpointService_FetchModelsMalformedJSON(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	// 200 但 body 不是 JSON → 报错,不返回半成品列表。
	srv, _ := modelsServer(t, http.StatusOK, `not-json-at-all`)
	ep := anthropicEP("ep-bad", 10)
	ep.BaseURL = srv.URL
	createEndpoint(t, store, ep)
	if _, err := svc.FetchModels("ep-bad"); err == nil {
		t.Fatalf("畸形 JSON 应报错")
	}

	// 200 + 合法 JSON 但无可提取 id(data/models 都缺)→ 空列表不报错。
	srv2, _ := modelsServer(t, http.StatusOK, `{"object":"list"}`)
	ep2 := anthropicEP("ep-empty", 11)
	ep2.BaseURL = srv2.URL
	createEndpoint(t, store, ep2)
	ids, err := svc.FetchModels("ep-empty")
	if err != nil {
		t.Fatalf("无可提取 id 应返回空列表: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("应返回空列表, got %v", ids)
	}
}

func TestEndpointService_ReportFailureDeletedID(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-del", 10))

	// 行被删后失败回报到达:不 panic、不造行,静默记日志放行。
	if err := store.Delete("ep-del"); err != nil {
		t.Fatal(err)
	}
	svc.ReportFailure("ep-del", 500, "boom")
	svc.ReportSuccess("ep-del")

	if _, err := store.Get("ep-del"); err == nil {
		t.Fatalf("ReportFailure/ReportSuccess 不得重建已删行")
	}
}
