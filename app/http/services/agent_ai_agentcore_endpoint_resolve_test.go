package services

// 内核出口解析的宿主接线（spec 二期 §5.0：每 run 单次解析 / §5.2 Acquire per-run
// + 排队超时 exclude 重选 / §5.4 出口限流本地错误 / §5.7 MarkUsed 打点 /
// §5.3a+§5.5 OnRequestError 失败上报缝）。
//
// 分层：resolver 本体行为在 kernel_endpoint_resolver_test.go；本文件只测宿主侧
// 的接线语义（resolve helper / run 级 Acquire / 打点 / 失败上报闭包 / 第四调用点
// 的粘性键选择）。registry 经 stubAgentCoreEndpointService 注入 :memory: 独立
// service，kernelEndpointResolverFn 按 svc 指针变化自动重建 resolver。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// ---- agentCoreResolveEndpointRow ------------------------------------------------

// 不变量 9：旧字段 EffectiveAgentCoreLLM 配置命中时 registry 整体不参与——
// 即便池里有完美候选也返回 nil（出网由 FromRow(nil) 落旧字段）。
func TestAgentCoreResolveRowOldFieldSkipsRegistry(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", "sk-byo"))
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-old",
	})

	if row := agentCoreResolveEndpointRow("sess", "claude-x"); row != nil {
		t.Fatalf("旧字段命中时 registry 不应参与, got row %s", row.ID)
	}
	// 出网地址由兼容壳链路落旧字段（FromRow(nil) 读旧字段）。
	if got := agentCoreBaseURL(); got != "https://gw.example.com" {
		t.Errorf("agentCoreBaseURL → %q, want 旧字段地址", got)
	}
}

// registry 构造失败（svc nil）→ nil 行,落继承链;不允许 panic。
func TestAgentCoreResolveRowNilServiceDegrades(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	stubAgentCoreEndpointService(t, nil)

	if row := agentCoreResolveEndpointRow("sess", "m"); row != nil {
		t.Fatalf("svc 缺失应降级 nil 行, got %s", row.ID)
	}
}

// 正常池 → 解析出 BYO 整行,行字段可直接驱动出网构造。
func TestAgentCoreResolveRowReturnsBYORow(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", "sk-byo"))

	row := agentCoreResolveEndpointRow("sess", "claude-x")
	if row == nil || row.ID != "ep-byo" {
		t.Fatalf("应解析出 ep-byo, got %+v", row)
	}
	if got := agentCoreBaseURLFromRow(row); got != "http://127.0.0.1:9101" {
		t.Errorf("FromRow base → %q, want BYO base_url", got)
	}
	if got := agentCoreAuthTokenFromRow(row); got != "sk-byo" {
		t.Errorf("FromRow key → %q, want sk-byo", got)
	}
}

// 粘性键由调用方决定:同键两次解析黏同一行(粘性 map 键=传入键),未用过的键
// 不出现。这是「compact 用 session.id / memory extract 用 projectPath」契约的
// 直接证据(键选择发生在各调用点,这里钉住 helper 忠实透传键)。
func TestAgentCoreResolveRowStickyKeyIsCallerChosen(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-a", "http://127.0.0.1:9101", "sk-a"))
	createEndpoint(t, store, byoEndpoint("ep-b", "http://127.0.0.1:9102", "sk-b"))

	r := kernelEndpointResolverFn()
	if r == nil {
		t.Fatal("resolver 应可用")
	}
	first := agentCoreResolveEndpointRow("sess-1", "m")
	if first == nil {
		t.Fatal("首次解析应命中池内行")
	}
	snap := stickySnapshot(r)
	if ent, ok := snap["sess-1"]; !ok || ent.endpointID != first.ID {
		t.Fatalf("粘性键 sess-1 应绑定到首解析行, 实际 %+v", snap)
	}
	if _, ok := snap["proj-x"]; ok {
		t.Fatalf("未用过的键不应出现在粘性 map: %+v", snap)
	}
	// 同键再次解析:黏住同一行(TTL 内黏性优先,与池内排序无关)。
	again := agentCoreResolveEndpointRow("sess-1", "m")
	if again == nil || again.ID != first.ID {
		t.Fatalf("同键二次解析应黏同一行 %s, got %+v", first.ID, again)
	}
}

// ---- agentCoreRunEgress ---------------------------------------------------------

// newRunEgressHarness 起 stub registry + resolver 就绪 + 排队快速失败的
// run egress 测试环境。
func newRunEgressHarness(t *testing.T) (*EndpointService, *storage.EndpointStore) {
	t.Helper()
	isolateAgentCoreRegistryEnv(t)
	svc, store := newEndpointTestHarness(t)
	stubAgentCoreEndpointService(t, svc)
	svc.waitTimeout = 10 * time.Millisecond // 排队超时快速失败(生产 30s)
	return svc, store
}

// 命中行 Acquire 一个 run 级槽位 + MarkUsed 打点;release 归还槽位。
// spec §5.0(每 run 一次)+§5.7(选中即打点)。
func TestAgentCoreRunEgressAcquiresAndMarksUsed(t *testing.T) {
	svc, store := newRunEgressHarness(t)
	createEndpoint(t, store, byoEndpoint("ep-run", "http://127.0.0.1:9101", "sk-run"))

	row, release, err := agentCoreRunEgress("sess-run", "")
	if err != nil {
		t.Fatalf("正常池不应出错: %v", err)
	}
	if row == nil || row.ID != "ep-run" {
		t.Fatalf("应命中 ep-run, got %+v", row)
	}
	if release == nil {
		t.Fatal("成功解析必须返回 release(run 终结释放槽位)")
	}
	if n := svc.Inflight()["ep-run"]; n != 1 {
		t.Fatalf("run 级槽位应占用 1, got %d", n)
	}
	if got := mustGet(t, store, "ep-run").LastUsedAt; got <= 0 {
		t.Fatalf("MarkUsed 应已打点(last_used_at=%d)", got)
	}

	release()
	if n := svc.Inflight()["ep-run"]; n != 0 {
		t.Fatalf("release 后槽位应归还, got %d", n)
	}
}

// §5.2:并发排队超时不计失败,exclude 满行重选下一候选。
// 夹具:高优先级行 max_concurrency=1 且唯一令牌被占,低优先级行不限并发。
func TestAgentCoreRunEgressQueueTimeoutExcludesAndFailsOver(t *testing.T) {
	svc, store := newRunEgressHarness(t)
	full := byoEndpoint("ep-full", "http://127.0.0.1:9101", "sk-full")
	full.Priority = 1
	full.MaxConcurrency = 1
	createEndpoint(t, store, full)
	idle := byoEndpoint("ep-idle", "http://127.0.0.1:9102", "sk-idle")
	idle.Priority = 2
	createEndpoint(t, store, idle)

	// 占死 ep-full 的唯一令牌(模拟另一条 run 正在占用)。
	held, err := svc.Acquire("ep-full", 1)
	if err != nil {
		t.Fatalf("占位失败: %v", err)
	}
	defer held()

	row, release, err := agentCoreRunEgress("sess-failover", "")
	if err != nil {
		t.Fatalf("有替补候选时不应整体失败: %v", err)
	}
	if row == nil || row.ID != "ep-idle" {
		t.Fatalf("满行应被 exclude,改选 ep-idle, got %+v", row)
	}
	if release == nil {
		t.Fatal("改选成功同样要返回 release")
	}
	release()
	// 满行未被计失败(spec §5.2:排队超时不动健康/计数)。
	fr := mustGet(t, store, "ep-full")
	if fr.ConsecutiveFailures != 0 || fr.RateLimitedUntil != 0 {
		t.Errorf("排队超时不应记失败, got failures=%d rl_until=%d",
			fr.ConsecutiveFailures, fr.RateLimitedUntil)
	}
}

// §5.2 收敛:候选满行 exclude 后落 builtin 兜底;builtin 也满(其并发窗被占死)
// 时重选循环耗尽 → 终止 run 的明确错误(不硬等不硬打)。
func TestAgentCoreRunEgressAllFullHardFails(t *testing.T) {
	svc, store := newRunEgressHarness(t)
	only := byoEndpoint("ep-only", "http://127.0.0.1:9101", "sk-only")
	only.MaxConcurrency = 1
	createEndpoint(t, store, only)
	// builtin 兜底行的并发窗同样占死(兜底行默认不限并发,显式设 1 才能测到
	// 重选循环耗尽这一收敛分支)。
	builtin := mustGet(t, store, storage.BuiltinEndpointID)
	builtin.MaxConcurrency = 1
	if err := store.Update(builtin); err != nil {
		t.Fatalf("设 builtin 并发: %v", err)
	}
	held, err := svc.Acquire("ep-only", 1)
	if err != nil {
		t.Fatalf("占位失败: %v", err)
	}
	defer held()
	heldBuiltin, err := svc.Acquire(storage.BuiltinEndpointID, 1)
	if err != nil {
		t.Fatalf("占位 builtin 失败: %v", err)
	}
	defer heldBuiltin()

	row, release, err := agentCoreRunEgress("sess-full", "")
	if err == nil {
		t.Fatal("全部候选(含 builtin)并发满应终止 run")
	}
	if row != nil || release != nil {
		t.Fatalf("失败路径不应返回行/槽位, got %+v release=%v", row, release != nil)
	}
	if !strings.Contains(err.Error(), "并发") {
		t.Errorf("错误应可自查(提及并发), got %v", err)
	}
}

// §5.2 语义补证:候选满但 builtin 兜底空闲 → exclude 重选收敛到兜底行,
// 不终止 run(兜底行是最后的可用落点)。
func TestAgentCoreRunEgressFullRowFallsToBuiltin(t *testing.T) {
	svc, store := newRunEgressHarness(t)
	only := byoEndpoint("ep-only2", "http://127.0.0.1:9101", "sk-only")
	only.MaxConcurrency = 1
	createEndpoint(t, store, only)
	held, err := svc.Acquire("ep-only2", 1)
	if err != nil {
		t.Fatalf("占位失败: %v", err)
	}
	defer held()

	row, release, err := agentCoreRunEgress("sess-builtin2", "")
	if err != nil {
		t.Fatalf("builtin 兜底可用时不应失败: %v", err)
	}
	if row == nil || !row.Builtin {
		t.Fatalf("应收敛到 builtin 兜底行, got %+v", row)
	}
	if release == nil {
		t.Fatal("兜底命中同样返回 release")
	}
	release()
}

// §5.4:出口指定行限流窗内 → ErrKernelEgressRateLimited 本地错误,不发请求
// 不 failover;错误文案含恢复提示。
func TestAgentCoreRunEgressRateLimitedHardFails(t *testing.T) {
	_, store := newRunEgressHarness(t)
	eg := anthropicEP("eg-rl", 99)
	eg.EgressKernel = true
	createEndpoint(t, store, eg)
	until := time.Now().Add(2 * time.Hour).Unix()
	markHealth(t, store, "eg-rl", storage.HealthUpdate{RateLimitedUntil: until})

	row, release, err := agentCoreRunEgress("sess-rl", "")
	if !errors.Is(err, ErrKernelEgressRateLimited) {
		t.Fatalf("应返回 ErrKernelEgressRateLimited, got %v", err)
	}
	if !strings.Contains(err.Error(), "可稍后重试") {
		t.Errorf("错误文案应含恢复提示, got %v", err)
	}
	if row != nil || release != nil {
		t.Fatalf("限流错误路径不应返回行/槽位, got row=%+v release=%v", row, release != nil)
	}
}

// 不变量 9:旧字段命中 → run egress 完全不参与 registry(无槽位无打点无错误)。
func TestAgentCoreRunEgressOldFieldSkipsRegistry(t *testing.T) {
	_, store := newRunEgressHarness(t)
	createEndpoint(t, store, byoEndpoint("ep-byo", "http://127.0.0.1:9101", "sk-byo"))
	setTestGlobalConfigAgentCoreLLM(t, &config.AgentCoreLLMConfig{
		Enabled: true, BaseURL: "https://gw.example.com", APIKey: "sk-old",
	})

	row, release, err := agentCoreRunEgress("sess-old", "")
	if err != nil || row != nil || release != nil {
		t.Fatalf("旧字段命中应 (nil,nil,nil), got row=%v release=%v err=%v", row, release != nil, err)
	}
}

// builtin 兜底行命中同样 Acquire + 打点(spec §5.7:builtin 可为选中行)。
func TestAgentCoreRunEgressMarksUsedBuiltinFallback(t *testing.T) {
	svc, store := newRunEgressHarness(t)
	// 池内行停用 → resolver 走 svc.Select 的 builtin 兜底。
	disabled := byoEndpoint("ep-off", "http://127.0.0.1:9101", "sk-off")
	disabled.Enabled = false
	createEndpoint(t, store, disabled)

	row, release, err := agentCoreRunEgress("sess-builtin", "")
	if err != nil {
		t.Fatalf("builtin 兜底不应失败: %v", err)
	}
	if row == nil || !row.Builtin {
		t.Fatalf("应命中 builtin 兜底行, got %+v", row)
	}
	if release == nil {
		t.Fatal("builtin 命中同样返回 release")
	}
	if got := mustGet(t, store, storage.BuiltinEndpointID).LastUsedAt; got <= 0 {
		t.Fatalf("builtin 行也应打点(last_used_at=%d)", got)
	}
	release()
	if n := svc.Inflight()[storage.BuiltinEndpointID]; n != 0 {
		t.Errorf("release 后 builtin 槽位应归还, got %d", n)
	}
}

// ---- agentCoreRequestErrorReporter ----------------------------------------------

// newReporterHarness 起一套 reporter 测试环境:store 与被 stub 的 registry
// service 必须同源(上报经 agentCoreGetEndpointServiceFn 取单例落库)。
func newReporterHarness(t *testing.T) (*EndpointService, *storage.EndpointStore) {
	t.Helper()
	isolateAgentCoreRegistryEnv(t)
	svc, store := newEndpointTestHarness(t)
	stubAgentCoreEndpointService(t, svc)
	return svc, store
}

// §5.3a+§5.5:429 用响应头解析 until(ratelimitwin),随 ReportFailure 落库;
// 不改健康状态。
func TestAgentCoreRequestErrorReporter429ParsesUntil(t *testing.T) {
	_, store := newReporterHarness(t)
	createEndpoint(t, store, byoEndpoint("ep-429", "http://127.0.0.1:9101", "sk-429"))

	reporter := agentCoreRequestErrorReporter(mustGet(t, store, "ep-429"))
	if reporter == nil {
		t.Fatal("BYO 行应接上报缝")
	}
	h := http.Header{}
	h.Set("Retry-After", "120")
	before := time.Now().Unix()
	reporter(http.StatusTooManyRequests, h)

	row := mustGet(t, store, "ep-429")
	if row.RateLimitedUntil < before+115 || row.RateLimitedUntil > before+140 {
		t.Fatalf("rate_limited_until = %d, want 约 now+120s(窗口 [%d,%d])",
			row.RateLimitedUntil, before+115, before+140)
	}
	if row.HealthStatus != "ok" {
		t.Errorf("429 不改健康状态, got %s", row.HealthStatus)
	}
}

// §5.3:401 首次即锁存(disabled + 无冷却),恢复只能靠重置健康或真实成功。
func TestAgentCoreRequestErrorReporter401Latches(t *testing.T) {
	_, store := newReporterHarness(t)
	createEndpoint(t, store, byoEndpoint("ep-401", "http://127.0.0.1:9101", "sk-bad"))

	reporter := agentCoreRequestErrorReporter(mustGet(t, store, "ep-401"))
	reporter(http.StatusUnauthorized, nil)

	row := mustGet(t, store, "ep-401")
	if row.HealthStatus != "disabled" || row.CooldownUntil != 0 {
		t.Fatalf("401 应锁存(disabled+无冷却), got status=%s cooldown=%d",
			row.HealthStatus, row.CooldownUntil)
	}
	if row.TotalRequests != 1 || row.FailedRequests != 1 {
		t.Errorf("锁存也应计数, got total=%d failed=%d", row.TotalRequests, row.FailedRequests)
	}
}

// status<=0(连接层失败/2xx 体内信封)按普通失败上报——TCP/协议层失败也要进
// 摘除计数;状态落在计数与健康列,不是 429 冷却。
func TestAgentCoreRequestErrorReporterStatusZeroCounts(t *testing.T) {
	_, store := newReporterHarness(t)
	createEndpoint(t, store, byoEndpoint("ep-conn", "http://127.0.0.1:9101", "sk-conn"))

	reporter := agentCoreRequestErrorReporter(mustGet(t, store, "ep-conn"))
	reporter(0, nil)

	row := mustGet(t, store, "ep-conn")
	if row.ConsecutiveFailures != 1 {
		t.Fatalf("status=0 应计连续失败 1, got %d", row.ConsecutiveFailures)
	}
	if row.RateLimitedUntil != 0 {
		t.Errorf("status=0 不该写 429 冷却, got %d", row.RateLimitedUntil)
	}
}

// builtin 行与 nil row 缝不接:builtin/继承链流量经 56432 回环代理,失败由路由
// 层上报,宿主再报会对同一行双计数。
func TestAgentCoreRequestErrorReporterNilOrBuiltinDisabled(t *testing.T) {
	if agentCoreRequestErrorReporter(nil) != nil {
		t.Fatal("nil row 应返回 nil(不接缝)")
	}
	builtin := &config.LLMEndpoint{ID: storage.BuiltinEndpointID, Builtin: true}
	if agentCoreRequestErrorReporter(builtin) != nil {
		t.Fatal("builtin 行应返回 nil(路由层已观测,宿主双计数)")
	}
}

// ---- 第四调用点:memory extract 粘性键 = projectPath ------------------------------

// 提取真身按 projectPath 解析(粘性 map 出现 projectPath 键);测试注入
// coreMemoryCallForTest 时完全绕过解析(现状不变)。
func TestAgentCoreMemoryExtractResolvesWithProjectPathKey(t *testing.T) {
	isolateAgentCoreRegistryEnv(t)
	store := newAgentCoreEndpointRegistryStore(t)
	createEndpoint(t, store, byoEndpoint("ep-mem", "http://127.0.0.1:9", "sk-mem")) // 死端口:流必然失败
	m := &agentAIManager{}

	// 真身路径:解析发生在流调用之前——流失败(死端口)不影响粘性绑定已写入。
	if _, err := m.agentCoreMemoryExtractCall(context.Background(), "/tmp/proj-mem", "m1", "", "sys", "usr"); err == nil {
		t.Fatal("死端口上游应返回错误(证明走了真身而非假缝)")
	}
	r := kernelEndpointResolverFn()
	if r == nil {
		t.Fatal("resolver 应可用")
	}
	snap := stickySnapshot(r)
	if ent, ok := snap["/tmp/proj-mem"]; !ok || ent.endpointID != "ep-mem" {
		t.Fatalf("粘性键应为 projectPath(/tmp/proj-mem→ep-mem), 实际 %+v", snap)
	}

	// 注入假缝:完全不触碰 resolver(粘性 map 不再新增条目)。
	orig := m.coreMemoryCallForTest
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		return `{"ops":[]}`, "end_turn", nil
	}
	t.Cleanup(func() { m.coreMemoryCallForTest = orig })
	if _, err := m.agentCoreMemoryExtractCall(context.Background(), "/tmp/proj-fake", "m1", "", "sys", "usr"); err != nil {
		t.Fatalf("假缝路径不应失败: %v", err)
	}
	snap = stickySnapshot(r)
	if _, ok := snap["/tmp/proj-fake"]; ok {
		t.Fatalf("假缝注入不应触发解析, 实际 %+v", snap)
	}
}
