package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// kernel_endpoint_resolver_test.go — 内核出口解析器(spec 二期 §5.0-§5.2/§5.4)
// 的行为面测试。真库路径用 newEndpointTestHarness(:memory:),出口行与粘性/TTL
// 用白盒(同包)与假时钟控制;「限流出口不触发任何候选读取」用 fakeKernelSvc
// 断言零调用。

// newKernelResolverHarness 起真 store 的 resolver:出口行读取走默认实现
// (ListEndpoints 过滤 EgressKernel),无需注入。
func newKernelResolverHarness(t *testing.T) (*kernelEndpointResolver, *EndpointService, *storage.EndpointStore) {
	t.Helper()
	svc, store := newEndpointTestHarness(t)
	r := newKernelEndpointResolver(svc, nil)
	return r, svc, store
}

// pinResolverNow 把 resolver 时钟钉在 t0,返回推进时钟的函数(TTL/过期语义用)。
func pinResolverNow(r *kernelEndpointResolver, t0 time.Time) func(time.Duration) {
	cur := t0
	r.now = func() time.Time { return cur }
	return func(d time.Duration) { cur = cur.Add(d) }
}

// stickySnapshot 白盒读粘性 map(加锁),供绑定断言。
func stickySnapshot(r *kernelEndpointResolver) map[string]stickyEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]stickyEntry, len(r.sticky))
	for k, v := range r.sticky {
		out[k] = v
	}
	return out
}

// createEgressRow 落一行 egress_kernel=1 的 anthropic 行(Validate 双层守卫:
// 出口只接受 anthropic,helper 直接按契约构造)。
func createEgressRow(t *testing.T, store *storage.EndpointStore, id string, priority int, enabled bool) {
	t.Helper()
	e := anthropicEP(id, priority)
	e.EgressKernel = true
	e.Enabled = enabled
	createEndpoint(t, store, e)
}

// ---- Resolve 四态 ------------------------------------------------------------

// 态 a:出口行 enabled=1 恒返回它——压过健康(锁存 disabled)、模型不匹配、
// 最差优先级、auto 池里的「完美行」(spec §5.0 1b/§5.4 用户配置压倒一切)。
func TestKernelEndpointResolverEgressEnabledOverridesAll(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	eg := anthropicEP("eg", 99)
	eg.EgressKernel = true
	eg.Models = `["other-model"]` // 请求模型不在限定内:出口照样命中
	createEndpoint(t, store, eg)
	// 出口行健康锁存(401:disabled + cooldown=0):照走,不回落。
	markHealth(t, store, "eg", storage.HealthUpdate{Status: "disabled", Failures: 3, CooldownUntil: 0, LastErr: "认证失败(401)"})
	good := anthropicEP("good", 5)
	createEndpoint(t, store, good)

	res, err := r.Resolve("sess", "claude-x")
	if err != nil {
		t.Fatalf("出口命中不应出错: %v", err)
	}
	if res.Endpoint == nil || res.Endpoint.ID != "eg" {
		t.Fatalf("出口指定应恒命中 eg, got %+v", res.Endpoint)
	}
	if !res.Egress {
		t.Fatalf("res.Egress 应为 true")
	}
	if len(stickySnapshot(r)) != 0 {
		t.Fatalf("出口命中不写粘性(auto 专属语义), 实际 %+v", stickySnapshot(r))
	}
}

// 态 b:出口行 enabled=0(暂停保留)→ 落 auto 调度(spec §5.0 1a)。
func TestKernelEndpointResolverEgressPausedFallsBackToAuto(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEgressRow(t, store, "eg", 99, false)
	good := anthropicEP("good", 5)
	createEndpoint(t, store, good)

	res, err := r.Resolve("sess", "claude-x")
	if err != nil {
		t.Fatalf("出口暂停应回落 auto: %v", err)
	}
	if res.Endpoint == nil || res.Endpoint.ID != "good" {
		t.Fatalf("出口暂停应落 auto 池 good, got %+v", res.Endpoint)
	}
	if res.Egress {
		t.Fatalf("回落 auto 后 res.Egress 应为 false")
	}
}

// 态 c:无出口行 → auto 调度按 priority 分层取顶层。
func TestKernelEndpointResolverNoEgressAutoByPriority(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("low", 5))
	createEndpoint(t, store, anthropicEP("high", 1))

	res, err := r.Resolve("", "claude-x")
	if err != nil {
		t.Fatalf("auto 调度不应出错: %v", err)
	}
	if res.Endpoint == nil || res.Endpoint.ID != "high" {
		t.Fatalf("无出口应按优先级选 high, got %+v", res.Endpoint)
	}
}

// 态 d:出口行 rate_limited_until 未到 → 本地限流错误(含恢复时刻文案),
// 不 failover 到 auto 池(spec §5.4:不发请求、不硬打)。
func TestKernelEndpointResolverEgressRateLimitedLocalErrorNoFailover(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEgressRow(t, store, "eg", 99, true)
	until := time.Now().Add(2 * time.Hour).Unix()
	markHealth(t, store, "eg", storage.HealthUpdate{Status: "ok", RateLimitedUntil: until})
	good := anthropicEP("good", 5)
	createEndpoint(t, store, good)

	res, err := r.Resolve("sess", "claude-x")
	if !errors.Is(err, ErrKernelEgressRateLimited) {
		t.Fatalf("应返回出口限流哨兵, got %v", err)
	}
	if res.Endpoint != nil {
		t.Fatalf("限流错误不应携带可用行, got %+v", res.Endpoint)
	}
	if res.RateLimitedUntil != until {
		t.Fatalf("res.RateLimitedUntil 应透传截止 %d, got %d", until, res.RateLimitedUntil)
	}
	msg := err.Error()
	if !strings.Contains(msg, "出口限流中") || !strings.Contains(msg, "恢复于") {
		t.Fatalf("限流文案缺关键字: %q", msg)
	}
	wantHHMM := time.Unix(until, 0).Format("15:04")
	if !strings.Contains(msg, wantHHMM) {
		t.Fatalf("限流文案应含恢复时刻 %s: %q", wantHHMM, msg)
	}
	// 池里的 good 行没有被 failover 选中(错误路径直返)。
	if len(stickySnapshot(r)) != 0 {
		t.Fatalf("限流错误路径不应写粘性: %+v", stickySnapshot(r))
	}
}

// 态 d 佐证:限流出口路径不触发任何 svc.Select/候选读取(fake 断言零调用)。
func TestKernelEndpointResolverRateLimitedDoesNotConsultSelector(t *testing.T) {
	eg := anthropicEP("eg", 5)
	eg.EgressKernel = true
	eg.RateLimitedUntil = time.Now().Add(time.Hour).Unix()
	fake := &fakeKernelSvc{rows: map[string]*config.LLMEndpoint{"eg": eg}}
	r := newKernelEndpointResolver(fake, nil)

	_, err := r.Resolve("s", "m")
	if !errors.Is(err, ErrKernelEgressRateLimited) {
		t.Fatalf("应返回出口限流哨兵, got %v", err)
	}
	if fake.selectCalls != 0 {
		t.Fatalf("限流出口不应触发 Select/候选读取, 实际 %d 次", fake.selectCalls)
	}
}

// 出口行熔断(disabled + cooldown>0)仍照走——熔断是自动摘除,用户出口配置
// 压过它;与态 a 的锁存(disabled+cooldown=0)同走出口路径(spec §5.4)。
func TestKernelEndpointResolverEgressCircuitBreakStillWalks(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEgressRow(t, store, "eg", 99, true)
	markHealth(t, store, "eg", storage.HealthUpdate{
		Status: "disabled", Failures: 3, CooldownUntil: time.Now().Add(5 * time.Minute).Unix(), LastErr: "上游 502",
	})

	res, err := r.Resolve("sess", "claude-x")
	if err != nil {
		t.Fatalf("熔断出口仍应照走: %v", err)
	}
	if res.Endpoint == nil || res.Endpoint.ID != "eg" {
		t.Fatalf("熔断出口应恒命中 eg, got %+v", res.Endpoint)
	}
}

// ---- 黏性(spec §5.1)---------------------------------------------------------

// 同 session 命中直接用且重刷 TTL;黏性压过更优 priority;TTL 过期懒清除后
// 重新调度。
func TestKernelEndpointResolverStickyBeatsPriorityAndRefreshesTTL(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	advance := pinResolverNow(r, time.Now())
	createEndpoint(t, store, anthropicEP("weak", 10))

	res1, err := r.Resolve("sess", "")
	if err != nil || res1.Endpoint.ID != "weak" || res1.Sticky {
		t.Fatalf("首次解析应新黏 weak(Sticky=false), got %+v err=%v", res1, err)
	}
	res2, err := r.Resolve("sess", "")
	if err != nil || res2.Endpoint.ID != "weak" || !res2.Sticky {
		t.Fatalf("同 session 应命中黏性, got %+v err=%v", res2, err)
	}

	// 更优 priority 的行进场,黏性仍压过重排(命中刷新 TTL)
	createEndpoint(t, store, anthropicEP("better", 1))
	advance(30 * time.Minute)
	res3, err := r.Resolve("sess", "")
	if err != nil || res3.Endpoint.ID != "weak" || !res3.Sticky {
		t.Fatalf("30min(<TTL) 应仍黏 weak, got %+v err=%v", res3, err)
	}
	advance(40 * time.Minute) // 距上次命中 40min,命中已把 TTL 刷到 30min 处
	res4, err := r.Resolve("sess", "")
	if err != nil || res4.Endpoint.ID != "weak" || !res4.Sticky {
		t.Fatalf("命中刷新 TTL 后 40min 仍应黏 weak, got %+v err=%v", res4, err)
	}

	// 距上次命中 61min > TTL 3600s → 懒清除,重新调度 → better
	advance(61 * time.Minute)
	res5, err := r.Resolve("sess", "")
	if err != nil || res5.Endpoint.ID != "better" || res5.Sticky {
		t.Fatalf("TTL 过期应重选 better(Sticky=false), got %+v err=%v", res5, err)
	}
}

// 黏住的行 enabled=0 → 重黏次优并覆写绑定(不清旧绑定直接换绑),打日志;
// 旧行恢复后不会弹回(绑定已覆写)。
func TestKernelEndpointResolverStickyRebindsWhenRowDisabled(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	a := anthropicEP("a", 1)
	createEndpoint(t, store, a)
	if res, err := r.Resolve("sess", ""); err != nil || res.Endpoint.ID != "a" {
		t.Fatalf("应黏上 a, got %+v err=%v", res, err)
	}

	a.Enabled = false
	if err := store.Update(a); err != nil {
		t.Fatalf("停用 a: %v", err)
	}
	createEndpoint(t, store, anthropicEP("b", 5))
	res2, err := r.Resolve("sess", "")
	if err != nil || res2.Endpoint.ID != "b" {
		t.Fatalf("a 停用应重黏 b, got %+v err=%v", res2, err)
	}
	if got := stickySnapshot(r)["sess"]; got.endpointID != "b" {
		t.Fatalf("绑定应覆写为 b, got %+v", got)
	}

	// a 恢复启用:绑定已在上一轮覆写为 b,不会弹回 a
	a.Enabled = true
	if err := store.Update(a); err != nil {
		t.Fatalf("恢复 a: %v", err)
	}
	res3, err := r.Resolve("sess", "")
	if err != nil || res3.Endpoint.ID != "b" || !res3.Sticky {
		t.Fatalf("覆写后应黏 b 不弹回 a, got %+v err=%v", res3, err)
	}
}

// 黏住的行限流 → 重黏次优(候选闸覆盖限流分支)。
func TestKernelEndpointResolverStickyRebindsWhenRowRateLimited(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	a := anthropicEP("a", 1)
	createEndpoint(t, store, a)
	if res, err := r.Resolve("sess", ""); err != nil || res.Endpoint.ID != "a" {
		t.Fatalf("应黏上 a, got %+v err=%v", res, err)
	}
	markHealth(t, store, "a", storage.HealthUpdate{Status: "ok", RateLimitedUntil: time.Now().Add(time.Hour).Unix()})
	createEndpoint(t, store, anthropicEP("b", 5))
	res, err := r.Resolve("sess", "")
	if err != nil || res.Endpoint.ID != "b" {
		t.Fatalf("a 限流应重黏 b, got %+v err=%v", res, err)
	}
}

// map 超过 4096 项时全量清扫过期项(懒触发:写绑定瞬间)。
func TestKernelEndpointResolverSweepsExpiredWhenOver4096(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("good", 5))
	now := r.now().Unix()
	r.mu.Lock()
	for i := 0; i < kernelStickyMaxEntries; i++ {
		r.sticky[fmt.Sprintf("old-%d", i)] = stickyEntry{endpointID: "x", lastHit: now - kernelStickyTTL - 1}
	}
	r.mu.Unlock()

	if _, err := r.Resolve("fresh", ""); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	snap := stickySnapshot(r)
	if _, ok := snap["fresh"]; !ok {
		t.Fatalf("新绑定应写入")
	}
	if len(snap) != 1 {
		t.Fatalf("超限应全量清扫过期项, 实际剩 %d 项", len(snap))
	}
}

// ---- 层内最少负载(spec §5.2)-------------------------------------------------

// 同优先级层内选 in-flight 最少者;不同层之间负载不跨层比(优先级先决)。
func TestKernelEndpointResolverLayerMinInflight(t *testing.T) {
	r, svc, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("a", 5))
	createEndpoint(t, store, anthropicEP("b", 5))

	// 占住 a 的 3 个并发槽(用真 Acquire,保证 Inflight 是真数)
	r1, _ := svc.Acquire("a", 8)
	r2, _ := svc.Acquire("a", 8)
	r3, _ := svc.Acquire("a", 8)
	defer r1()
	defer r2()
	defer r3()

	res, err := r.Resolve("s1", "")
	if err != nil || res.Endpoint.ID != "b" {
		t.Fatalf("层内应选 in-flight 最少的 b, got %+v err=%v", res, err)
	}
}

// 优先级分层先决:顶层行即使满载也压过低层空闲行(spec §5.2「按 priority 分层」)。
func TestKernelEndpointResolverPriorityLayerBeatsLoad(t *testing.T) {
	r, svc, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("top", 1))
	createEndpoint(t, store, anthropicEP("lowp", 5))
	hold1, _ := svc.Acquire("top", 8)
	hold2, _ := svc.Acquire("top", 8)
	defer hold1()
	defer hold2()

	res, err := r.Resolve("s1", "")
	if err != nil || res.Endpoint.ID != "top" {
		t.Fatalf("顶层行应压过低层空闲行, got %+v err=%v", res, err)
	}
}

// 层内并列(同优先级同负载)随机:多次解析不是恒选同一个(统计断言放宽)。
func TestKernelEndpointResolverLayerTieBreakIsRandom(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("a", 5))
	createEndpoint(t, store, anthropicEP("b", 5))

	seen := map[string]int{}
	for i := 0; i < 40; i++ {
		res, err := r.Resolve(fmt.Sprintf("s%d", i), "")
		if err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		seen[res.Endpoint.ID]++
	}
	if len(seen) < 2 {
		t.Fatalf("层内并列应随机分布, 40 次解析恒选 %v", seen)
	}
}

// ---- model 空合法 -----------------------------------------------------------

// model 为空 = 跳过模型过滤(spec §5.0 候选闸 models=[]∨model 命中;空请求模型
// 对「限定模型」的行同样放行)。
func TestKernelEndpointResolverEmptyModelSkipsModelFilter(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	picky := anthropicEP("picky", 5)
	picky.Models = `["glm-4.6"]`
	createEndpoint(t, store, picky)

	res, err := r.Resolve("s", "")
	if err != nil || res.Endpoint.ID != "picky" {
		t.Fatalf("空 model 应放行限定模型的行, got %+v err=%v", res, err)
	}
}

// ---- exclude（4b 宿主:Acquire 排队超时 exclude 重选,§5.2）--------------------

// exclude 命中 auto 池行:被排除行本回合不再入选,改选下一候选;粘性键对被排除
// 行视同未黏(落到首黏路径并覆写绑定)。
func TestKernelEndpointResolverExcludeSkipsRowAndSticky(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("ex-a", 1))
	createEndpoint(t, store, anthropicEP("ex-b", 2))

	// 先黏 ex-a(最高优先级,自然首黏)。
	if res, err := r.Resolve("sess-ex", "m"); err != nil || res.Endpoint.ID != "ex-a" {
		t.Fatalf("首解析应黏 ex-a, got %+v err=%v", res, err)
	}
	// 排除 ex-a 重选:改选 ex-b,粘性覆写。
	res, err := r.Resolve("sess-ex", "m", "ex-a")
	if err != nil || res.Endpoint.ID != "ex-b" {
		t.Fatalf("排除 ex-a 应改选 ex-b, got %+v err=%v", res, err)
	}
	if ent := stickySnapshot(r)["sess-ex"]; ent.endpointID != "ex-b" {
		t.Fatalf("粘性应覆写为 ex-b, 实际 %+v", stickySnapshot(r))
	}
	// 不带 exclude 的下一次解析:黏回重黏后的 ex-b(TTL 内)。
	again, err := r.Resolve("sess-ex", "m")
	if err != nil || again.Endpoint.ID != "ex-b" {
		t.Fatalf("覆写后应黏 ex-b, got %+v err=%v", again, err)
	}
}

// exclude 命中出口指定行:本回合当无出口落 auto(下一 run 不带 exclude 时出口
// 恢复首选);出口行自身不被写入粘性。
func TestKernelEndpointResolverExcludeSkipsEgressRow(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEgressRow(t, store, "eg-ex", 99, true)
	createEndpoint(t, store, anthropicEP("plain", 5))

	res, err := r.Resolve("sess-eg", "m", "eg-ex")
	if err != nil || res.Endpoint.ID != "plain" {
		t.Fatalf("出口被排除应落 auto plain, got %+v err=%v", res, err)
	}
	if res.Egress {
		t.Fatal("排除出口后 res.Egress 应为 false")
	}
	// 不带 exclude:出口恢复首选。
	res, err = r.Resolve("sess-eg", "m")
	if err != nil || res.Endpoint.ID != "eg-ex" || !res.Egress {
		t.Fatalf("出口应恢复首选, got %+v err=%v", res, err)
	}
}

// 全部候选被排除 → 池空 → svc.Select 兜底(exclude 集透传,builtin 为唯一落点)。
func TestKernelEndpointResolverExcludeAllFallsToBuiltin(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("only-ex", 5))

	res, err := r.Resolve("sess-all", "m", "only-ex")
	if err != nil {
		t.Fatalf("排除唯一候选应回落 builtin 而非报错: %v", err)
	}
	if res.Endpoint == nil || !res.Endpoint.Builtin {
		t.Fatalf("应兜底 builtin 行, got %+v", res.Endpoint)
	}
	if ent, ok := stickySnapshot(r)["sess-all"]; ok {
		t.Fatalf("兜底行不写粘性, 实际 %+v", ent)
	}
}

// ---- builtin 兜底 -----------------------------------------------------------

// auto 池全空 → builtin 兜底(照旧),兜底行不写粘性(避免 BYO 恢复后会话
// 被黏在兜底行一整个 TTL)。
func TestKernelEndpointResolverBuiltinFallbackWhenPoolEmpty(t *testing.T) {
	r, _, store := newKernelResolverHarness(t)
	_ = store // builtin 行由 store 种子提供,无需再建行

	res, err := r.Resolve("sess", "any-model")
	if err != nil {
		t.Fatalf("builtin 兜底不应出错: %v", err)
	}
	if res.Endpoint == nil || res.Endpoint.ID != storage.BuiltinEndpointID {
		t.Fatalf("空池应兜底 builtin, got %+v", res.Endpoint)
	}
	if len(stickySnapshot(r)) != 0 {
		t.Fatalf("兜底行不写粘性, 实际 %+v", stickySnapshot(r))
	}
}

// ---- Acquire 语义 ------------------------------------------------------------

// resolver.Acquire 包装 svc.Acquire,id 与 maxConcurrency 原样透传(fake 断言)。
func TestKernelEndpointResolverAcquireWrapsService(t *testing.T) {
	ep := anthropicEP("a", 5)
	fake := &fakeKernelSvc{rows: map[string]*config.LLMEndpoint{"a": ep}}
	r := newKernelEndpointResolver(fake, nil)

	release, err := r.Acquire(ep, 7)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if len(fake.acquireCalls) != 1 || fake.acquireCalls[0] != "a/7" {
		t.Fatalf("Acquire 应透传 (id,maxConcurrency)=(a,7), 实际 %v", fake.acquireCalls)
	}
	release()
	if _, err := r.Acquire(nil, 1); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("nil 行应返回 ErrNoEndpoint, got %v", err)
	}
}

// 真 service 语义冒烟:Acquire 占 inflight,release 归零(per-run 一次的计数底座,
// 接线在 4b)。
func TestKernelEndpointResolverAcquireCountsInflight(t *testing.T) {
	r, svc, store := newKernelResolverHarness(t)
	createEndpoint(t, store, anthropicEP("a", 5))
	res, err := r.Resolve("s", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	release, err := r.Acquire(res.Endpoint, 0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := svc.Inflight()["a"]; got != 1 {
		t.Fatalf("acquire 后 inflight 应为 1, got %d", got)
	}
	release()
	if got := svc.Inflight()["a"]; got != 0 {
		t.Fatalf("release 后 inflight 应为 0, got %d", got)
	}
}

// ---- fake -------------------------------------------------------------------

// fakeKernelSvc 是 resolver 依赖面(kernelEndpointSvc)的最小假实现:记录调用,
// 供「零调用/透传参数」类断言;其余方法按需最小实现。
type fakeKernelSvc struct {
	rows         map[string]*config.LLMEndpoint
	selectCalls  int
	inflightSnap map[string]int
	acquireCalls []string
}

func (f *fakeKernelSvc) Select(protocol, model, explicitID string, excludeIDs ...string) (*config.LLMEndpoint, error) {
	f.selectCalls++
	return nil, ErrNoEndpoint
}

func (f *fakeKernelSvc) ListEndpoints() ([]*config.LLMEndpoint, error) {
	out := make([]*config.LLMEndpoint, 0, len(f.rows))
	for _, e := range f.rows {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeKernelSvc) GetEndpoint(id string) (*config.LLMEndpoint, error) {
	if e, ok := f.rows[id]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("fake: %s 不存在", id)
}

func (f *fakeKernelSvc) Inflight() map[string]int {
	if f.inflightSnap == nil {
		return map[string]int{}
	}
	return f.inflightSnap
}

func (f *fakeKernelSvc) Acquire(id string, maxConcurrency int) (func(), error) {
	f.acquireCalls = append(f.acquireCalls, fmt.Sprintf("%s/%d", id, maxConcurrency))
	return func() {}, nil
}
