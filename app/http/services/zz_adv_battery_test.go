package services

// 对抗检查电池(2026-11-28,用户指令「从代码角度验证实现效果」):
// 专打跨任务接缝与两组并行批(endpoint-pin-activation × endpoint-phase2)共存
// 后的交互盲区——任务级评审各自只看过自己那片,这里全部用敌意用例钉一遍。
// 每个用例注释标注「攻击面」;跑 -race;失败=真缺陷,通过=实现扛住。

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/ratelimitwin"
	"aliang.one/nursorgate/processor/storage"
)

// ---- 脚手架 -----------------------------------------------------------------

func advHarness(t *testing.T) (*EndpointService, *storage.EndpointStore) {
	t.Helper()
	svc, err := NewEndpointServiceForTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, svc.store
}

func advRow(t *testing.T, s *storage.EndpointStore, id, name string, mut func(*config.LLMEndpoint)) {
	t.Helper()
	e := &config.LLMEndpoint{ID: id, Name: name, BaseURL: "http://127.0.0.1:9/" + id,
		APIKey: "sk-adv-" + id, ProtocolAnthropic: true, AutoGroup: true,
		Priority: 50, Enabled: true, Models: "[]"}
	if mut != nil {
		mut(e)
	}
	if err := s.Create(e); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// ---- 攻击 1:钉选×出口 互锁(两组并行批的最大接缝)--------------------------

// 攻击面:ActivateInGroup(托盘 radio,把组内其它行 enabled=0)打到 egress 行头上
// ——出口行被托盘互斥关停后,内核 Resolve 应「出口暂停→auto 兜住」;托盘把出口行
// 重新激活后,出口应原地复活。两批各自评审都没测过这条交互链。
func TestAdvActivationMutexDisablesEgressRowThenResume(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-x", "adv-x", nil)                 // 将被设为出口
	advRow(t, store, "adv-y", "adv-y", func(e *config.LLMEndpoint) { e.Priority = 60 })
	advRow(t, store, "adv-z", "adv-z", func(e *config.LLMEndpoint) { e.Priority = 70 })

	// 出口指定 X(直接走 store 事务 API,模拟 Task 6 REST 落库后的状态)。
	if err := store.SetEgressKernel("adv-x", true); err != nil {
		t.Fatal(err)
	}
	res := newKernelEndpointResolver(svc, nil)
	got, err := res.Resolve("sess-1", "")
	if err != nil || got.Endpoint == nil || got.Endpoint.ID != "adv-x" {
		t.Fatalf("出口生效应恒走 X: got=%v err=%v", got.Endpoint, err)
	}

	// 托盘钉选 Y:组内互斥把 X(与 Z)enabled=0。
	if err := svc.ActivateInGroup("anthropic", "adv-y"); err != nil {
		t.Fatal(err)
	}
	x, _ := store.Get("adv-x")
	if x.Enabled {
		t.Fatal("前置:托盘互斥应把出口行 X 停用(否则互斥语义没生效)")
	}
	// 攻击点:出口行被停用 → Resolve 不得再恒走 X,应回 auto 兜住(候选=只剩 Y)。
	got, err = res.Resolve("sess-2", "")
	if err != nil || got.Endpoint == nil || got.Endpoint.ID != "adv-y" {
		t.Fatalf("出口暂停应回 auto 且池里只剩 Y: got=%v err=%v", got.Endpoint, err)
	}
	// 暂停态语义:Resolution.Egress=false(= 本次解析非出口定向,真走的是 auto);
	// UI 三态读的是 endpoints 列表的 egress_kernel+enabled,不读本标记。
	if got.Egress {
		t.Fatalf("出口暂停态 Resolution.Egress 应为 false(非出口定向), got %+v", got)
	}

	// 托盘把 X 重新激活(再次 ActivateInGroup("anthropic","adv-x"))→ X enabled=1、
	// Y/Z 被关 → 出口复活:Resolve 又恒走 X。
	if err := svc.ActivateInGroup("anthropic", "adv-x"); err != nil {
		t.Fatal(err)
	}
	got, err = res.Resolve("sess-3", "")
	if err != nil || got.Endpoint == nil || got.Endpoint.ID != "adv-x" {
		t.Fatalf("托盘重激活后出口应复活: got=%v err=%v", got.Endpoint, err)
	}
}

// 攻击面:出口行同时兼任粘性行,ActivateInGroup 把它停用——粘性绑定应被
// 判不可用并重黏(日志+覆写),且粘性目标不得是被停用的行。
func TestAdvActivationMutexBreaksStickyAndResticks(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-s1", "adv-s1", func(e *config.LLMEndpoint) { e.Priority = 10 })
	advRow(t, store, "adv-s2", "adv-s2", func(e *config.LLMEndpoint) { e.Priority = 20 })

	res := newKernelEndpointResolver(svc, func() (*config.LLMEndpoint, error) {
		return nil, nil // 无出口行,纯 auto
	})
	g1, err := res.Resolve("sess-adv", "")
	if err != nil || g1.Endpoint == nil {
		t.Fatalf("首黏: %v", err)
	}
	// 托盘互斥:激活 s2 → s1(粘性/高优先行)被停用。
	if err := svc.ActivateInGroup("anthropic", "adv-s2"); err != nil {
		t.Fatal(err)
	}
	g2, err := res.Resolve("sess-adv", "")
	if err != nil || g2.Endpoint == nil {
		t.Fatalf("重黏: %v", err)
	}
	if g2.Endpoint.ID == g1.Endpoint.ID {
		t.Fatalf("粘性行已被互斥停用,重黏不得仍选它: got %s", g2.Endpoint.ID)
	}
	if g2.Endpoint.ID != "adv-s2" {
		t.Fatalf("池内唯一启用行是 s2,应重黏到 s2: got %s", g2.Endpoint.ID)
	}
}

// ---- 攻击 2:锁存终态 vs 混合失败序列 ---------------------------------------

// 攻击面:锁存(401)后混入 429 与 5xx 交替——429 写 rate_limited_until、5xx 走
// 计数路径,任何一步都不许给 cooldown_until 赋正值(否则锁存退化成可自愈)。
func TestAdvLatchSurvivesMixed429And5xxSequence(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-mix", "adv-mix", nil)

	svc.ReportFailure("adv-mix", 401, "unauthorized", 0) // 锁存
	for i := 0; i < 4; i++ {
		svc.ReportFailure("adv-mix", 429, "rate limited", time.Now().Unix()+120) // 真窗口
		svc.ReportFailure("adv-mix", 500, "boom", 0)                             // 计数路径
	}
	e, _ := store.Get("adv-mix")
	if e.HealthStatus != "disabled" || e.CooldownUntil != 0 {
		t.Fatalf("锁存必须在混合序列后保持终态: status=%s cooldown=%d", e.HealthStatus, e.CooldownUntil)
	}
	// 锁存行 + 429 窗口:Resolve 应命中限流本地错误(§5.4 出口语义)或至少不可选。
	res := newKernelEndpointResolver(svc, nil)
	// 把它设成出口,验证「锁存行 + 限流窗」时返回本地限流错误而非硬走。
	if err := store.SetEgressKernel("adv-mix", true); err != nil {
		t.Fatal(err)
	}
	_, err := res.Resolve("sess-mix", "")
	if !errors.Is(err, ErrKernelEgressRateLimited) {
		t.Fatalf("锁存出口行在限流窗内应返回本地限流错误, got %v", err)
	}
}

// 攻击面:ResetHealth 清锁存后,立即再来一次 401——应重新锁存(清零不是免疫)。
func TestAdvRelatchAfterResetHealth(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-r", "adv-r", nil)
	svc.ReportFailure("adv-r", 401, "unauthorized", 0)
	if err := svc.ResetHealth("adv-r"); err != nil {
		t.Fatal(err)
	}
	e, _ := store.Get("adv-r")
	if e.HealthStatus != "ok" {
		t.Fatalf("前置:重置应回 ok, got %s", e.HealthStatus)
	}
	svc.ReportFailure("adv-r", 401, "again", 0)
	if e, _ = store.Get("adv-r"); e.HealthStatus != "disabled" || e.CooldownUntil != 0 {
		t.Fatalf("重置后再 401 应重新锁存: status=%s cooldown=%d", e.HealthStatus, e.CooldownUntil)
	}
}

// ---- 攻击 3:管理面并发——SetEgressKernel × ActivateInGroup × Update --------

// 攻击面:两个管理面写路径(事务换绑 vs 白名单整行)并发交错,终态必须满足
// 「至多一行 egress=1」且行可用性自洽。-race + 并发交错。
func TestAdvConcurrentEgressAndActivation(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-c1", "adv-c1", nil)
	advRow(t, store, "adv-c2", "adv-c2", func(e *config.LLMEndpoint) { e.Priority = 60 })

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = store.SetEgressKernel("adv-c1", true) }()
		go func() { defer wg.Done(); _ = store.SetEgressKernel("adv-c2", true) }()
		go func() { defer wg.Done(); _ = svc.ActivateInGroup("anthropic", "adv-c1") }()
	}
	wg.Wait()

	list, _ := store.List()
	n := 0
	for _, e := range list {
		if e.EgressKernel {
			n++
		}
	}
	if n > 1 {
		t.Fatalf("并发交错后 egress=1 行数=%d,违反至多一行", n)
	}
	// 出口行(若存在)必须仍是 anthropic∧(启用与否均可,但协议不变量不得破)。
	for _, e := range list {
		if e.EgressKernel && !e.ProtocolAnthropic {
			t.Fatalf("egress 行 %s 协议被翻成非 anthropic,双层守卫失守", e.ID)
		}
	}
}

// 攻击面:粘性 map 并发写+超阈清扫(4096)同时进行——race 探测器下不得死锁/
// 丢绑定语义错乱。
func TestAdvStickySweepUnderConcurrency(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-w", "adv-w", nil)
	res := newKernelEndpointResolver(svc, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 600; j++ {
				_, _ = res.Resolve(fmt.Sprintf("sess-%d-%d", worker, j), "")
			}
		}(i)
	}
	wg.Wait()
	// 清扫后 map 不得无界膨胀:超 4096 的清扫已由 Resolve 内部触发,这里只
	// 断言 Resolve 仍可正常出结果(没被打挂/死锁就是胜利,-race 负责其余)。
	got, err := res.Resolve("sess-final", "")
	if err != nil || got.Endpoint == nil {
		t.Fatalf("并发清扫后 Resolve 失效: %v", err)
	}
}

// ---- 攻击 4:store 层双层守卫与边界 ----------------------------------------

// 攻击面:绕过 REST,直接 store.Create 一条 egress=1∧非 anthropic 行——
// Validate 出口规则(store.Update 链已守)对 Create 路径同样要拦。
func TestAdvStoreCreateRejectsEgressNonAnthropic(t *testing.T) {
	_, store := advHarness(t)
	e := &config.LLMEndpoint{ID: "adv-oai", Name: "oai", BaseURL: "https://x.example.com",
		APIKey: "sk-1", ProtocolOpenAI: true, EgressKernel: true, Enabled: true}
	if err := store.Create(e); err == nil {
		t.Fatal("Create egress=1∧非 anthropic 应被 Validate 拦(双层守卫的 store 半边)")
	}
}

// 攻击面:BumpCounters 空串 last_error 语义——池模式 ReportSuccess 用它清
// last_error;若上游错误后紧跟成功,last_error 必须被清(不是保留旧错)。
func TestAdvBumpCountersClearsLastErrorOnSuccess(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-cls", "adv-cls", nil)
	svc.ReportFailure("adv-cls", 500, "boom", 0)
	svc.ReportSuccess("adv-cls")
	e, _ := store.Get("adv-cls")
	if e.LastError != "" {
		t.Fatalf("成功后 last_error 应被清空, got %q", e.LastError)
	}
	if e.TotalRequests != 2 || e.FailedRequests != 1 {
		t.Fatalf("计数: total=%d failed=%d, want 2/1", e.TotalRequests, e.FailedRequests)
	}
}

// 攻击面:池模式(failure_disable=0)下 ReportFailure 只记账不落健康;切换回
// 严格模式后一次 5xx 即正常计数——模式切换不留幽灵态。
func TestAdvPoolModeGateThenStrictModeResumes(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-pool", "adv-pool", nil)
	pol, _ := svc.GetPolicy()
	pol.FailureDisable = false
	if err := svc.SetPolicy(pol); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		svc.ReportFailure("adv-pool", 500, "boom", 0)
	}
	e, _ := store.Get("adv-pool")
	if e.HealthStatus != "ok" || e.ConsecutiveFailures != 0 {
		t.Fatalf("池模式不得落健康列: status=%s failures=%d", e.HealthStatus, e.ConsecutiveFailures)
	}
	if e.TotalRequests != 5 || e.FailedRequests != 5 {
		t.Fatalf("池模式必须照记计数: total=%d failed=%d", e.TotalRequests, e.FailedRequests)
	}
	// 切回严格:一次 5xx → 1 次;三次 → 摘除。
	pol.FailureDisable = true
	_ = svc.SetPolicy(pol)
	svc.ReportFailure("adv-pool", 500, "boom", 0)
	e, _ = store.Get("adv-pool")
	if e.HealthStatus == "disabled" {
		t.Fatalf("严格模式首败(1/3)不应立即摘除")
	}
	svc.ReportFailure("adv-pool", 500, "boom", 0)
	svc.ReportFailure("adv-pool", 500, "boom", 0)
	if e, _ = store.Get("adv-pool"); e.HealthStatus != "disabled" || e.CooldownUntil <= 0 {
		t.Fatalf("严格模式达阈值应摘除+冷却: status=%s cooldown=%d", e.HealthStatus, e.CooldownUntil)
	}
}

// ---- 攻击 5:ratelimitwin 边界 ----------------------------------------------

// 攻击面:解析器对非法/极端输入的鲁棒性(过去时间/未来过远/nil)——不得 panic、
// 不得溢出为负、必须收敛到 [now+60, now+21600]。
func TestAdvRatelimitWinExtremeInputs(t *testing.T) {
	now := time.Now().Unix()
	h := http.Header{}
	if got := ratelimitwin.Parse(429, h); got != 0 {
		t.Fatalf("无头应 0, got %d", got)
	}
	// 过远的未来 unified-reset:收敛到 now+21600。
	h1 := http.Header{"anthropic-ratelimit-unified-reset": {fmt.Sprint(now + 86400*30)}}
	got := ratelimitwin.Parse(429, h1)
	if got > now+21600+2 {
		t.Fatalf("超远窗口应 clamp 到 6h: got %d (now=%d)", got, now)
	}
	// 过去的 Retry-After(HTTP-date):地板 now+60。
	past := time.Unix(now-3600, 0).UTC().Format(http.TimeFormat)
	h2 := http.Header{"Retry-After": {past}}
	got2 := ratelimitwin.Parse(429, h2)
	if got2 < now+58 || got2 > now+62 {
		t.Fatalf("过去 HTTP-date 应地板 now+60: got %d (now=%d)", got2, now)
	}
}

// ---- 攻击 6:激活清零与生命周期计数(一期遗留质疑)--------------------------

// 攻击面:禁用→启用循环三次,生命周期计数必须存活且健康清零不衰减。
func TestAdvEnableCyclePreservesLifetimeCounters(t *testing.T) {
	svc, store := advHarness(t)
	advRow(t, store, "adv-cyc", "adv-cyc", nil)
	svc.ReportFailure("adv-cyc", 500, "a", 0)
	svc.ReportSuccess("adv-cyc")
	svc.ReportFailure("adv-cyc", 500, "b", 0)
	// total=3 failed=2
	for round := 0; round < 3; round++ {
		cur, _ := store.Get("adv-cyc")
		cur.Enabled = false
		if err := svc.UpdateEndpoint(cur); err != nil {
			t.Fatal(err)
		}
		cur2, _ := store.Get("adv-cyc")
		cur2.Enabled = true
		if err := svc.UpdateEndpoint(cur2); err != nil {
			t.Fatal(err)
		}
		e, _ := store.Get("adv-cyc")
		if e.HealthStatus != "ok" || e.ConsecutiveFailures != 0 || e.CooldownUntil != 0 {
			t.Fatalf("round %d 启用清零失败: %+v", round, e)
		}
		if e.TotalRequests != 3 || e.FailedRequests != 2 {
			t.Fatalf("round %d 生命周期计数被侵蚀: total=%d failed=%d", round, e.TotalRequests, e.FailedRequests)
		}
	}
}
