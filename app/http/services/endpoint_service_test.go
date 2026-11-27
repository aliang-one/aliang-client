package services

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

// ---- Select:重试排除(excludeID,final-review 必改 2)-----------------------

// excludeID 语义(「失败切下一 endpoint 重试一次」的需求拍板):
//   - 自动选择路径排除该 id(重试不得再选同一 endpoint 两枪);
//   - exclude 后仍有候选 → 取下一优先级;
//   - exclude 后空候选 → 照旧回落 builtin;
//   - explicitID 显式路径不受 exclude 影响(显式点名就是显式)。
func TestEndpointService_SelectExcludeIDSkipsRow(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-first", 10))
	createEndpoint(t, store, anthropicEP("ep-second", 20))

	got, err := svc.Select("anthropic", "", "")
	if err != nil || got.ID != "ep-first" {
		t.Fatalf("基线应取 ep-first, got %s err=%v", got.ID, err)
	}

	// 排除首选 → 命中次选(真切换,而非同 endpoint 重打)。
	got, err = svc.Select("anthropic", "", "", "ep-first")
	if err != nil {
		t.Fatalf("exclude 后 select: %v", err)
	}
	if got.ID != "ep-second" {
		t.Fatalf("排除 ep-first 应取 ep-second, got %s", got.ID)
	}

	// 排除到只剩无候选 → 回落 builtin(不是 ErrNoEndpoint)。
	got, err = svc.Select("anthropic", "", "", "ep-first", "ep-second")
	if err != nil {
		t.Fatalf("exclude 全部后应回落 builtin: %v", err)
	}
	if got.ID != storage.BuiltinEndpointID {
		t.Fatalf("exclude 全部后应回落内置行, got %s", got.ID)
	}

	// exclude 未知 id:不影响正常选择。
	got, err = svc.Select("anthropic", "", "", "no-such-id")
	if err != nil || got.ID != "ep-first" {
		t.Fatalf("exclude 不存在的 id 应保持首选, got %s err=%v", got.ID, err)
	}
}

// explicitID 显式路径不受 exclude 影响:点名 ep-first 时即使 excludeID ==
// ep-first 也照样返回它(重试排除只作用于自动选择)。
func TestEndpointService_SelectExcludeIDIgnoredForExplicit(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-manual-x", 10))

	got, err := svc.Select("anthropic", "", "ep-manual-x", "ep-manual-x")
	if err != nil {
		t.Fatalf("显式点名不受 exclude 影响: %v", err)
	}
	if got.ID != "ep-manual-x" {
		t.Fatalf("显式点名应返回该行, got %s", got.ID)
	}
}

// ---- ReportFailure:计数 / 摘除 / 429 / 4xx / 成功 ---------------------------

func TestEndpointService_ReportFailureThresholdDisables(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-f", 10))

	// 默认策略 threshold=3:前两次只计数,第三次摘除。
	svc.ReportFailure("ep-f", 502, "bad gateway", 0)
	svc.ReportFailure("ep-f", 503, "service unavailable", 0)
	got := mustGet(t, store, "ep-f")
	if got.ConsecutiveFailures != 2 || got.HealthStatus != "ok" {
		t.Fatalf("未达阈值只应计数: failures=%d status=%s", got.ConsecutiveFailures, got.HealthStatus)
	}
	if got.LastError == "" {
		t.Fatalf("失败应记录 last_error 供面板展示")
	}
	// 计数列:每次失败 total+1 failed+1。
	if got.TotalRequests != 2 || got.FailedRequests != 2 {
		t.Fatalf("两次失败后计数应为 total=2 failed=2, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}

	before := time.Now().Unix()
	svc.ReportFailure("ep-f", 500, "internal error", 0)
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
	svc.ReportFailure("ep-r", 429, "rate limited", 0)
	got := mustGet(t, store, "ep-r")
	// 429 不计连续失败、不摘除,只写 rate_limited_until;请求计数照记(total/failed 各 +1)。
	if got.RateLimitedUntil <= before || got.RateLimitedUntil > before+61 {
		t.Fatalf("rate_limited_until 应为 now+60s 附近, got %d", got.RateLimitedUntil)
	}
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" || got.CooldownUntil != 0 {
		t.Fatalf("429 不计连续失败/不摘除: %+v", got)
	}
	if got.TotalRequests != 1 || got.FailedRequests != 1 {
		t.Fatalf("429 应记 total=1 failed=1, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
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

	// 其它 4xx(400/413/422/404 等)不计数不落状态(不变量 5)。
	for _, status := range []int{400, 413, 422, 404} {
		svc.ReportFailure("ep-x", status, "client error", 0)
	}
	got := mustGet(t, store, "ep-x")
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 {
		t.Fatalf("其它 4xx 必须完全忽略: %+v", got)
	}
	if got.TotalRequests != 0 || got.FailedRequests != 0 {
		t.Fatalf("被忽略的 4xx 不得计数: total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}

	// status=0(连接失败/超时)按 5xx 同口径计数。
	svc.ReportFailure("ep-x", 0, "dial tcp: connection refused", 0)
	got = mustGet(t, store, "ep-x")
	if got.ConsecutiveFailures != 1 || got.LastError == "" {
		t.Fatalf("连接失败应计数: %+v", got)
	}
	if got.TotalRequests != 1 || got.FailedRequests != 1 {
		t.Fatalf("连接失败应记 total=1 failed=1, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
}

// 401/402 锁存(spec §5.3):首次即 disabled + cooldown_until=0(无冷却不自愈),
// last_error 记认证失败/欠费;计数照记。
func TestEndpointService_ReportFailure401Latch(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-401", 10))

	svc.ReportFailure("ep-401", 401, "invalid api key", 0)
	got := mustGet(t, store, "ep-401")
	if got.HealthStatus != "disabled" {
		t.Fatalf("401 应锁存为 disabled, got %s", got.HealthStatus)
	}
	if got.CooldownUntil != 0 {
		t.Fatalf("锁存必须无冷却(cooldown_until=0,不自愈), got %d", got.CooldownUntil)
	}
	if !strings.Contains(got.LastError, "认证失败") {
		t.Fatalf("last_error 应记认证失败, got %q", got.LastError)
	}
	if got.TotalRequests != 1 || got.FailedRequests != 1 {
		t.Fatalf("锁存应记 total=1 failed=1, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}

	// 无冷却不自愈:Select 不得返回锁存行(回落 builtin),且不得清状态。
	sel, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("锁存后应回落 builtin: %v", err)
	}
	if sel.ID != storage.BuiltinEndpointID {
		t.Fatalf("锁存行不应被选中, got %s", sel.ID)
	}
	got = mustGet(t, store, "ep-401")
	if got.HealthStatus != "disabled" || got.CooldownUntil != 0 {
		t.Fatalf("Select 不得自愈锁存态: %+v", got)
	}
}

func TestEndpointService_ReportFailure402Latch(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-402", 10))

	svc.ReportFailure("ep-402", 402, "insufficient balance", 0)
	got := mustGet(t, store, "ep-402")
	if got.HealthStatus != "disabled" || got.CooldownUntil != 0 {
		t.Fatalf("402 应锁存 disabled+cooldown=0: %+v", got)
	}
	if !strings.Contains(got.LastError, "欠费") {
		t.Fatalf("last_error 应记欠费, got %q", got.LastError)
	}
	if got.TotalRequests != 1 || got.FailedRequests != 1 {
		t.Fatalf("锁存应记 total=1 failed=1, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
}

// 池模式下 401/402 也锁存(锁存绕过池模式门控,spec §5.3/§9.1 显式豁免)。
func TestEndpointService_ReportFailureLatchBypassesPoolMode(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-latch-pool", 10))
	if err := store.SetPolicy(&config.EndpointGroupPolicy{
		FailureDisable: false, FailureThreshold: 3, CooldownSec: 300, RateLimitCooldownSec: 60,
	}); err != nil {
		t.Fatal(err)
	}

	svc.ReportFailure("ep-latch-pool", 401, "bad key", 0)
	got := mustGet(t, store, "ep-latch-pool")
	if got.HealthStatus != "disabled" || got.CooldownUntil != 0 {
		t.Fatalf("池模式下 401 仍须锁存 disabled+cooldown=0: %+v", got)
	}
	if !strings.Contains(got.LastError, "认证失败") {
		t.Fatalf("池模式锁存也应记认证失败, got %q", got.LastError)
	}
}

// 回归(quality-fix Important 1):锁存落地后,计数路径(5xx/连接失败/403)不得给
// 锁存行赋冷却——否则 cooldown>0 就不再满足「锁存」特征,冷却到期会被
// recoverIfNeeded 自愈回 ok,违反不变量「永不自愈」。可达场景=锁存落地时已在途
// 的请求、出口/显式行(selectExplicit 不受健康态拦)。
func TestEndpointService_LatchedRowSurvivesFailureCountPath(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-latch-5xx", 10))

	// 401 锁存。
	svc.ReportFailure("ep-latch-5xx", 401, "bad key", 0)
	// 锁存后连续 3 次 5xx(达默认阈值 3):必须仍保持 disabled+cooldown=0。
	for i := 0; i < 3; i++ {
		svc.ReportFailure("ep-latch-5xx", 500, "late in-flight", 0)
	}

	got := mustGet(t, store, "ep-latch-5xx")
	if got.HealthStatus != "disabled" || got.CooldownUntil != 0 {
		t.Fatalf("锁存行经计数路径后应仍保持 disabled+cooldown=0: %+v", got)
	}
	// 仍不自愈:Select 不返回它(回落 builtin),且 Select 不得改状态。
	sel, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("锁存行不应可被选中(应回落 builtin): %v", err)
	}
	if sel.ID != storage.BuiltinEndpointID {
		t.Fatalf("锁存行经计数路径后不得被选中, got %s", sel.ID)
	}
	got = mustGet(t, store, "ep-latch-5xx")
	if got.HealthStatus != "disabled" || got.CooldownUntil != 0 {
		t.Fatalf("Select 后锁存态不得自愈: %+v", got)
	}
	// 计数照记:1 次 401 + 3 次 500 = total 4 / failed 4。
	if got.TotalRequests != 4 || got.FailedRequests != 4 {
		t.Fatalf("计数应照记 total=4 failed=4, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
}

// 401 锁存后的 ResetHealth 清零断言(Minor 4):与 500 熔断的 ResetHealth 测试区分,
// 确认锁存终态同样可经手动重置恢复。
func TestEndpointService_ResetHealthClearsLatch(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-reset-latch", 10))

	svc.ReportFailure("ep-reset-latch", 401, "bad key", 0)
	if got := mustGet(t, store, "ep-reset-latch"); got.HealthStatus != "disabled" {
		t.Fatalf("前置:401 应已锁存, got %s", got.HealthStatus)
	}

	if err := svc.ResetHealth("ep-reset-latch"); err != nil {
		t.Fatalf("reset health: %v", err)
	}
	got := mustGet(t, store, "ep-reset-latch")
	if got.HealthStatus != "ok" || got.CooldownUntil != 0 ||
		got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("ResetHealth 应清回锁存态: %+v", got)
	}
	sel, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("重置后应可选: %v", err)
	}
	if sel.ID != "ep-reset-latch" {
		t.Fatalf("重置后应选中 ep-reset-latch, got %s", sel.ID)
	}
}

// 403 归计数路径(语义歧义按普通失败计):连续失败累加,达阈值摘除。
func TestEndpointService_ReportFailure403CountPath(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-403", 10))

	svc.ReportFailure("ep-403", 403, "forbidden", 0)
	got := mustGet(t, store, "ep-403")
	if got.ConsecutiveFailures != 1 || got.HealthStatus != "ok" {
		t.Fatalf("403 应计入连续失败: %+v", got)
	}
	if got.TotalRequests != 1 || got.FailedRequests != 1 {
		t.Fatalf("403 应记 total=1 failed=1, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}

	// 达默认阈值 3 → 摘除。
	svc.ReportFailure("ep-403", 403, "forbidden", 0)
	svc.ReportFailure("ep-403", 403, "forbidden", 0)
	got = mustGet(t, store, "ep-403")
	if got.HealthStatus != "disabled" || got.CooldownUntil == 0 {
		t.Fatalf("403 连续失败达阈值应摘除(带冷却): %+v", got)
	}
}

// 池模式下 403:计数列与 last_error 照记,健康四列不动(不变量 6 修订)。
func TestEndpointService_ReportFailure403PoolModeRecordsOnly(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-403-pool", 10))
	if err := store.SetPolicy(&config.EndpointGroupPolicy{
		FailureDisable: false, FailureThreshold: 1, CooldownSec: 300, RateLimitCooldownSec: 60,
	}); err != nil {
		t.Fatal(err)
	}

	svc.ReportFailure("ep-403-pool", 403, "forbidden", 0)
	got := mustGet(t, store, "ep-403-pool")
	// 只门控动作:即便 threshold=1 也不得摘除/写冷却。
	if got.HealthStatus != "ok" || got.ConsecutiveFailures != 0 ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 {
		t.Fatalf("池模式 403 不得写健康四列: %+v", got)
	}
	if got.LastError != "forbidden" {
		t.Fatalf("池模式 403 必须照记 last_error, got %q", got.LastError)
	}
	if got.TotalRequests != 1 || got.FailedRequests != 1 {
		t.Fatalf("池模式 403 应记 total=1 failed=1, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
}

func TestEndpointService_ReportSuccessClears(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-s", 10))

	svc.ReportFailure("ep-s", 500, "boom", 0)
	svc.ReportFailure("ep-s", 429, "rate", 0)
	svc.ReportSuccess("ep-s")

	got := mustGet(t, store, "ep-s")
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("成功应清零全部健康态: %+v", got)
	}
	// 计数:两次失败(total+2/failed+2)+ 一次成功(total+1/failed+0)。
	if got.TotalRequests != 3 || got.FailedRequests != 2 {
		t.Fatalf("成功路径计数应为 total=3 failed=2, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
}

// ReportSuccess 是 401/402 锁存的恢复路径之一(另一条是 ResetHealth)。
func TestEndpointService_ReportSuccessClearsLatch(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-latch-ok", 10))

	svc.ReportFailure("ep-latch-ok", 401, "bad key", 0)
	if got := mustGet(t, store, "ep-latch-ok"); got.HealthStatus != "disabled" {
		t.Fatalf("前置:401 应已锁存, got %s", got.HealthStatus)
	}

	svc.ReportSuccess("ep-latch-ok")
	got := mustGet(t, store, "ep-latch-ok")
	if got.HealthStatus != "ok" || got.CooldownUntil != 0 || got.LastError != "" {
		t.Fatalf("ReportSuccess 应清回锁存态: %+v", got)
	}
	// 恢复后应可被 Select 选中。
	sel, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("解除锁存后应可选: %v", err)
	}
	if sel.ID != "ep-latch-ok" {
		t.Fatalf("解除锁存后应选中 ep-latch-ok, got %s", sel.ID)
	}
}

// 429 服务面行为(spec §5.5):until>0 覆盖组策略;until=0/负回落组策略。
func TestEndpointService_ReportFailure429Until(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-until", 10))

	// until>0:直接采用调用方解析出的截止(覆盖组策略默认 60s)。
	want := time.Now().Unix() + 999
	svc.ReportFailure("ep-until", 429, "rate", want)
	if got := mustGet(t, store, "ep-until"); got.RateLimitedUntil != want {
		t.Fatalf("until>0 应采用调用方值 %d, got %d", want, got.RateLimitedUntil)
	}

	// until=0:回落组策略 RateLimitCooldownSec(默认 60s)。
	before := time.Now().Unix()
	svc.ReportFailure("ep-until", 429, "rate", 0)
	got := mustGet(t, store, "ep-until")
	if got.RateLimitedUntil <= before || got.RateLimitedUntil > before+61 {
		t.Fatalf("until=0 应回落 now+60s, got %d (before %d)", got.RateLimitedUntil, before)
	}

	// until<0(非法头):不炸,按 0 处理 → 回落组策略。
	before = time.Now().Unix()
	svc.ReportFailure("ep-until", 429, "rate", -123)
	got = mustGet(t, store, "ep-until")
	if got.RateLimitedUntil <= before || got.RateLimitedUntil > before+61 {
		t.Fatalf("until<0 应按 0 回落 now+60s, got %d (before %d)", got.RateLimitedUntil, before)
	}

	// 自定义组策略 429 冷却时长:until=0 时回落该值。
	if err := store.SetPolicy(&config.EndpointGroupPolicy{
		FailureDisable: true, FailureThreshold: 3, CooldownSec: 300, RateLimitCooldownSec: 120,
	}); err != nil {
		t.Fatal(err)
	}
	before = time.Now().Unix()
	svc.ReportFailure("ep-until", 429, "rate", 0)
	got = mustGet(t, store, "ep-until")
	if got.RateLimitedUntil < before+119 || got.RateLimitedUntil > before+122 {
		t.Fatalf("until=0 应回落组策略 120s, got %d (before %d)", got.RateLimitedUntil, before)
	}
}

// 池模式(failure_disable=0)二期语义(spec §9.1 修订不变量 6):只门控动作
// (disabled 迁移/冷却写库),计数列与 last_error 照记——「指标永远记,只门控动作」。
func TestEndpointService_PoolModeRecordsCountersOnly(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-p", 10))

	if err := store.SetPolicy(&config.EndpointGroupPolicy{
		FailureDisable: false, FailureThreshold: 1, CooldownSec: 60, RateLimitCooldownSec: 60,
	}); err != nil {
		t.Fatal(err)
	}
	svc.ReportFailure("ep-p", 500, "boom", 0)
	svc.ReportFailure("ep-p", 500, "boom", 0)
	svc.ReportFailure("ep-p", 429, "rate limited", 0)

	// 健康四列不动(即便 threshold=1 也不摘除/不写冷却),但计数与摘要照记。
	got := mustGet(t, store, "ep-p")
	if got.ConsecutiveFailures != 0 || got.HealthStatus != "ok" ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 {
		t.Fatalf("池模式不得写健康四列: %+v", got)
	}
	if got.LastError != "rate limited" {
		t.Fatalf("池模式必须照记 last_error, got %q", got.LastError)
	}
	if got.TotalRequests != 3 || got.FailedRequests != 3 {
		t.Fatalf("池模式失败应记 total=3 failed=3, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}

	// 池模式成功:只记计数(total+1)+清 last_error,不动健康列。
	svc.ReportSuccess("ep-p")
	got = mustGet(t, store, "ep-p")
	if got.HealthStatus != "ok" || got.ConsecutiveFailures != 0 ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("池模式成功不得写健康列: %+v", got)
	}
	if got.TotalRequests != 4 || got.FailedRequests != 3 {
		t.Fatalf("池模式成功应记 total=4 failed=3, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
}

// 激活迁移清零(spec §5.6):PUT 路径 enabled 0→1 执行 ResetHealth 同款清零。
func TestEndpointService_UpdateEnabledActivationClearsHealth(t *testing.T) {
	svc, store := newEndpointTestHarness(t)

	ep := anthropicEP("ep-activate", 10)
	ep.Enabled = false
	createEndpoint(t, store, ep)
	// 摆一份陈旧健康态 + 生命周期计数。
	markHealth(t, store, "ep-activate", storage.HealthUpdate{
		Status: "disabled", Failures: 3, CooldownUntil: time.Now().Unix() + 300,
		RateLimitedUntil: time.Now().Unix() + 60, LastErr: "old boom",
	})
	if err := store.BumpCounters("ep-activate", 7, 4, "old boom"); err != nil {
		t.Fatal(err)
	}

	// enabled 0→1:配置更新 + 健康清零。
	upd := *ep
	upd.Enabled = true
	if err := svc.UpdateEndpoint(&upd); err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	got := mustGet(t, store, "ep-activate")
	if !got.Enabled {
		t.Fatalf("enabled 应落库为 true")
	}
	if got.HealthStatus != "ok" || got.ConsecutiveFailures != 0 ||
		got.CooldownUntil != 0 || got.RateLimitedUntil != 0 || got.LastError != "" {
		t.Fatalf("激活 0→1 应清零健康态: %+v", got)
	}
	// 生命周期计数只累计不清零。
	if got.TotalRequests != 7 || got.FailedRequests != 4 {
		t.Fatalf("激活清零不得动生命周期计数, got total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
	// 入参行同步刷新:handler 用同一指针构造 PUT 响应视图。
	if upd.HealthStatus != "ok" || upd.CooldownUntil != 0 || upd.LastError != "" {
		t.Fatalf("激活清零应同步回写入参行(供 PUT 响应): %+v", upd)
	}
}

// 非激活(1→1 或 1→0)更新不得清健康——清零只由真正的 0→1 迁移触发。
func TestEndpointService_UpdateEnabledNoResetWithoutActivation(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-noact", 10))
	markHealth(t, store, "ep-noact", storage.HealthUpdate{
		Status: "disabled", Failures: 2, CooldownUntil: time.Now().Unix() + 300, LastErr: "keep me",
	})

	// 1→1(仍启用)更新:健康态原样保留。
	ep := anthropicEP("ep-noact", 10)
	ep.Name = "renamed"
	if err := svc.UpdateEndpoint(ep); err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	got := mustGet(t, store, "ep-noact")
	if got.HealthStatus != "disabled" || got.ConsecutiveFailures != 2 || got.LastError != "keep me" {
		t.Fatalf("非激活更新不得清健康态: %+v", got)
	}
}

// final-review finding 5(严格不变量 6):池模式(failure_disable=0)下
// recoverIfNeeded 也不得写库——「池模式不写任何健康状态」含清理陈旧态。
// 场景:池模式 + 残留 disabled 行 + 冷却过期 → Select 应返回该行(可用性
// 优先:过滤语义上 status!=ok 且 cooldown 已过期即视为可用),
// 但健康列零写入(禁用残留原样保留,直到管理员显式重置或切回摘除模式)。
func TestEndpointService_PoolModeLazyRecoveryWritesNothing(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, anthropicEP("ep-pool", 10))

	// 切池模式。
	if err := store.SetPolicy(&config.EndpointGroupPolicy{
		FailureDisable: false, FailureThreshold: 3, CooldownSec: 300, RateLimitCooldownSec: 60,
	}); err != nil {
		t.Fatal(err)
	}

	// 残留态:disabled + 冷却已过期(池模式前留下的陈旧摘除)。
	markHealth(t, store, "ep-pool", storage.HealthUpdate{
		Status: "disabled", Failures: 3, CooldownUntil: time.Now().Unix() - 5, LastErr: "old boom",
	})

	got, err := svc.Select("anthropic", "", "")
	if err != nil {
		t.Fatalf("池模式下过期残留行应可选: %v", err)
	}
	if got.ID != "ep-pool" {
		t.Fatalf("应选中 ep-pool, got %s", got.ID)
	}

	// 健康列零写入:disabled 残留原样保留(不得被懒恢复清成 ok)。
	row := mustGet(t, store, "ep-pool")
	if row.HealthStatus != "disabled" || row.ConsecutiveFailures != 3 || row.LastError != "old boom" {
		t.Fatalf("池模式懒恢复不得写健康列(不变量 6 含清理陈旧态): %+v", row)
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
		svc.ReportFailure("ep-h", 500, "boom", 0)
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

	// 内置行:拨测平台代理入口。注入死地址 → 不可达+可自查摘要;
	// 真实回环入口由 TestEndpointService_TestBuiltinProbeReachable 单测覆盖。
	svc.builtinProbeAddr = "127.0.0.1:1"
	ok, _, summary = svc.Test(storage.BuiltinEndpointID)
	if ok || summary == "" {
		t.Fatalf("内置行入口不可达应返回失败与摘要: %v %q", ok, summary)
	}
}

func TestEndpointService_TestBuiltinProbeReachable(t *testing.T) {
	svc, _ := newEndpointTestHarness(t)

	// 起一个真 TCP 监听当假「平台代理入口」,拨测应通且摘要说明语义。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	svc.builtinProbeAddr = ln.Addr().String()
	ok, count, summary := svc.Test(storage.BuiltinEndpointID)
	if !ok || count != 0 || summary == "" {
		t.Fatalf("内置行入口可达应 ok 且带语义摘要: %v %d %q", ok, count, summary)
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
			svc.ReportFailure("ep-race", 500, "boom again", 0)
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
	svc.ReportFailure("ep-del", 500, "boom", 0)
	svc.ReportSuccess("ep-del")

	if _, err := store.Get("ep-del"); err == nil {
		t.Fatalf("ReportFailure/ReportSuccess 不得重建已删行")
	}
}
