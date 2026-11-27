package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/statistic"
)

// traySummaryTestGatherers 构造一套全部成功的假子源,供 handler 形状测试用。
func traySummaryTestGatherers() traySummaryGatherers {
	return traySummaryGatherers{
		run: func() *TrayRunSummary {
			return trayRunSummaryFromStatus(map[string]interface{}{
				"current_mode": "http",
				"is_running":   true,
				"description":  "Regular Mode is running",
			}, TrayRunBlocked{}, "127.0.0.1:56432")
		},
		ai: func() *TrayAISummary {
			return &TrayAISummary{
				Enabled: true,
				Providers: []TrayAIProvider{
					{Key: "anthropic", Label: "Claude", Detected: true, Detail: "检测到 2 次流量经此路由"},
				},
			}
		},
		agents: func() *TrayAgentsSummary {
			return &TrayAgentsSummary{
				Kernels: []TrayAgentKernel{
					{Name: "claude", Active: 1, Idle: 2, Total24h: 6,
						Sessions: []TrayAgentSession{{Title: "t", Source: "网页", Active: false, Ts: 1790000000}}},
				},
			}
		},
		endpoints: func() *TrayEndpointsSummary {
			return &TrayEndpointsSummary{
				Available: true,
				Groups: []TrayEndpointGroup{
					{Protocol: "anthropic", Current: "glm-coding-plan",
						Items: []TrayEndpointItem{{ID: "glm-1", Name: "glm-coding-plan", Rank: 1, State: "ok"}}},
				},
			}
		},
	}
}

// TestTraySummaryJSONShape 钉死面板读通道的 JSON 字段名:字段名漂移=面板整块
// 渲染失败,所以在信封 data 层逐字段核对契约(plan Task 5)。
func TestTraySummaryJSONShape(t *testing.T) {
	summary := buildTraySummary("v1.1.127-local", traySummaryTestGatherers())
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal summary: %v\npayload: %s", err, raw)
	}
	trayAssertKeys(t, data, "version", "run", "ai", "agents", "endpoints")
	if data["version"] != "v1.1.127-local" {
		t.Fatalf("version = %v, want v1.1.127-local", data["version"])
	}

	run := data["run"].(map[string]interface{})
	trayAssertKeys(t, run, "is_running", "mode", "status_desc", "listen_addr", "blocked")
	if run["is_running"] != true || run["mode"] != "http" || run["listen_addr"] != "127.0.0.1:56432" {
		t.Fatalf("run = %v, want is_running=true mode=http listen_addr=127.0.0.1:56432", run)
	}
	blocked := run["blocked"].(map[string]interface{})
	trayAssertKeys(t, blocked, "code", "msg")

	ai := data["ai"].(map[string]interface{})
	trayAssertKeys(t, ai, "enabled", "providers")
	if ai["enabled"] != true {
		t.Fatalf("ai.enabled = %v, want true", ai["enabled"])
	}
	providers := ai["providers"].([]interface{})
	p0 := providers[0].(map[string]interface{})
	trayAssertKeys(t, p0, "key", "label", "detected", "detail")

	agents := data["agents"].(map[string]interface{})
	trayAssertKeys(t, agents, "kernels")
	k0 := agents["kernels"].([]interface{})[0].(map[string]interface{})
	trayAssertKeys(t, k0, "name", "active", "idle", "total_24h", "sessions")
	s0 := k0["sessions"].([]interface{})[0].(map[string]interface{})
	trayAssertKeys(t, s0, "title", "source", "active", "ts")
	if _, ok := s0["ts"].(float64); !ok {
		t.Fatalf("sessions[0].ts 应为数字(unix 秒), got %T", s0["ts"])
	}

	endpoints := data["endpoints"].(map[string]interface{})
	trayAssertKeys(t, endpoints, "available", "groups")
	if endpoints["available"] != true {
		t.Fatalf("endpoints.available = %v, want true", endpoints["available"])
	}
	g0 := endpoints["groups"].([]interface{})[0].(map[string]interface{})
	trayAssertKeys(t, g0, "protocol", "current", "items")
	i0 := g0["items"].([]interface{})[0].(map[string]interface{})
	// id=钉选定位键(plan 2026-11-27 Task 1):radio 激活 POST 用它点名行。
	trayAssertKeys(t, i0, "id", "name", "rank", "state", "cooldown_remaining_sec")
	if i0["id"] != "glm-1" {
		t.Fatalf("items[0].id = %v, want glm-1(源行 ID 透传)", i0["id"])
	}
}

// TestTraySummarySubSourcePanicDegradesToNull 单源 panic 不能拖死端点:
// panic 的子源降级为该字段 null,其余子源照常输出。
func TestTraySummarySubSourcePanicDegradesToNull(t *testing.T) {
	g := traySummaryTestGatherers()
	g.ai = func() *TrayAISummary { panic("boom: ai 子源炸了") }

	summary := buildTraySummary("v1.1.127-local", g)
	if summary.AI != nil {
		t.Fatalf("panic 的 ai 子源应降级为 nil, got %+v", summary.AI)
	}
	if summary.Run == nil || summary.Agents == nil || summary.Endpoints == nil {
		t.Fatalf("其余子源不应被 panic 波及: run=%v agents=%v endpoints=%v",
			summary.Run, summary.Agents, summary.Endpoints)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"ai":null`) {
		t.Fatalf("JSON 应含 \"ai\":null, got %s", raw)
	}
}

// TestServeTraySummaryHandler 回环免 cookie 直接放行并回信封;非回环且无
// dashboard 会话的来源 401。
func TestServeTraySummaryHandler(t *testing.T) {
	fake := traySummaryTestGatherers()

	req := httptest.NewRequest(http.MethodGet, "/api/tray/summary", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	serveTraySummaryWith(rec, req, fake)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int          `json:"code"`
		Data *TraySummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v; body=%s", err, rec.Body.String())
	}
	if envelope.Code != 0 || envelope.Data == nil || envelope.Data.Version == "" {
		t.Fatalf("envelope = %+v, want code=0 且 data.version 非空", envelope)
	}

	remote := httptest.NewRequest(http.MethodGet, "/api/tray/summary", nil)
	remote.RemoteAddr = "203.0.113.9:4444"
	recRemote := httptest.NewRecorder()
	serveTraySummaryWith(recRemote, remote, fake)
	if recRemote.Code != http.StatusUnauthorized {
		t.Fatalf("remote no-cookie: code = %d, want 401", recRemote.Code)
	}
}

// TestTrayAgentKernelsAggregation agents 子源聚合语义:24h 窗口过滤、
// 活跃/空闲计数、来源映射、cap 20、空内核不产出。
func TestTrayAgentKernelsAggregation(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }

	sessions := []models.AgentVibeSession{
		{ID: "c1", Provider: "claude", Title: "活跃会话", Status: "running", Origin: "dashboard", UpdatedAt: rfc(-5 * time.Minute)},
		{ID: "c2", Provider: "claude", Title: "网页空闲", Status: "idle", Origin: "dashboard", UpdatedAt: rfc(-2 * time.Hour)},
		{ID: "c3", Provider: "claude", Title: "手机会话", Status: "idle", Origin: "phone", UpdatedAt: rfc(-3 * time.Hour)},
		{ID: "c4", Provider: "claude", Title: "终端本地", Status: "idle", Origin: "", UpdatedAt: rfc(-4 * time.Hour)},
		{ID: "c5", Provider: "claude", Title: "窗口外旧会话", Status: "idle", Origin: "", UpdatedAt: rfc(-25 * time.Hour)},
		{ID: "c6", Provider: "claude", Title: "坏时间戳", Status: "idle", Origin: "", UpdatedAt: "not-a-time"},
		{ID: "x1", Provider: "codex", Title: "codex 空闲", Status: "idle", Origin: "", UpdatedAt: rfc(-23 * time.Hour)},
	}
	kernels := aggregateTrayAgentKernels(sessions, now)

	if len(kernels) != 2 {
		t.Fatalf("kernels = %+v, want claude+codex 两个(窗口外/坏时间戳不计入,空内核不产出)", kernels)
	}
	if kernels[0].Name != "claude" || kernels[1].Name != "codex" {
		t.Fatalf("内核顺序应确定性排序, got %s,%s", kernels[0].Name, kernels[1].Name)
	}
	claude := kernels[0]
	if claude.Active != 1 || claude.Idle != 3 || claude.Total24h != 4 {
		t.Fatalf("claude 计数 = active=%d idle=%d total=%d, want 1/3/4", claude.Active, claude.Idle, claude.Total24h)
	}
	if len(claude.Sessions) != 4 {
		t.Fatalf("claude 明细应 4 条(窗口外与坏时间戳剔除), got %d", len(claude.Sessions))
	}
	// 明细按新→旧排序
	if claude.Sessions[0].Title != "活跃会话" || !claude.Sessions[0].Active {
		t.Fatalf("明细首条应为最新活跃会话, got %+v", claude.Sessions[0])
	}
	// 来源映射:网页/手机/终端(spec §4.5 三态)
	if claude.Sessions[0].Source != "网页" || claude.Sessions[2].Source != "手机" || claude.Sessions[3].Source != "终端" {
		t.Fatalf("来源映射不符: %+v", claude.Sessions)
	}
	// ts 是 unix 秒
	wantTs := now.Add(-5 * time.Minute).Unix()
	if claude.Sessions[0].Ts != wantTs {
		t.Fatalf("ts = %d, want %d(unix 秒)", claude.Sessions[0].Ts, wantTs)
	}
	if kernels[1].Total24h != 1 || kernels[1].Active != 0 || kernels[1].Idle != 1 {
		t.Fatalf("codex 计数 = %+v, want active=0 idle=1 total=1", kernels[1])
	}
}

// TestTrayAgentKernelsCap20PerKernel 每内核明细最多 20 条,total_24h 不受 cap
// 影响(洪峰窗口内兜底,窗口语义归 total)。
func TestTrayAgentKernelsCap20PerKernel(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var sessions []models.AgentVibeSession
	for i := 0; i < 25; i++ {
		sessions = append(sessions, models.AgentVibeSession{
			ID:        "s",
			Provider:  "claude",
			Title:     "会话",
			Status:    "idle",
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute).UTC().Format(time.RFC3339),
		})
	}
	kernels := aggregateTrayAgentKernels(sessions, now)
	if len(kernels) != 1 {
		t.Fatalf("kernels = %d, want 1", len(kernels))
	}
	if kernels[0].Total24h != 25 {
		t.Fatalf("total_24h = %d, want 25(cap 不改窗口总量)", kernels[0].Total24h)
	}
	if len(kernels[0].Sessions) != 20 {
		t.Fatalf("明细 = %d 条, want 上限 20", len(kernels[0].Sessions))
	}
}

// trayEndpointFixture 构造覆盖各映射分支的 endpoint 行集。
func trayEndpointFixture() []*config.LLMEndpoint {
	now := time.Now().Unix()
	return []*config.LLMEndpoint{
		{ID: "builtin-aliang", Name: "内置隧道", Enabled: true, Builtin: true,
			ProtocolAnthropic: true, ProtocolOpenAI: true, AutoGroup: true, Priority: 0,
			HealthStatus: "ok"},
		// P2:两协议都在组,健康 ok
		{ID: "a", Name: "glm-coding-plan", Enabled: true,
			ProtocolAnthropic: true, ProtocolOpenAI: true, AutoGroup: true, Priority: 50,
			HealthStatus: "ok"},
		// P1:仅 anthropic,冷却中(剩余 120s)
		{ID: "b", Name: "packy-code", Enabled: true,
			ProtocolAnthropic: true, AutoGroup: true, Priority: 10,
			HealthStatus: "cooldown", CooldownUntil: now + 120},
		// auto_group=0 → rank 0「—」
		{ID: "c", Name: "manual-only", Enabled: true,
			ProtocolAnthropic: true, AutoGroup: false, Priority: 5,
			HealthStatus: "ok"},
		// enabled=0 → 不进 items
		{ID: "d", Name: "user-disabled", Enabled: false,
			ProtocolAnthropic: true, ProtocolOpenAI: true, AutoGroup: true, Priority: 1,
			HealthStatus: "ok"},
		// 摘除态
		{ID: "e", Name: "removed", Enabled: true,
			ProtocolOpenAI: true, AutoGroup: true, Priority: 20,
			HealthStatus: "disabled"},
	}
}

// TestTrayEndpointGroupsMapping endpoint 子源映射(spec §4.4):组=协议 toggle、
// rank=priority 升序(auto_group=0/未设优先级→0)、cooldown→state+剩余秒、
// disabled→state、enabled=0 剔除、current=组内 rank 最小的 ok 行。
func TestTrayEndpointGroupsMapping(t *testing.T) {
	now := time.Now().Unix()
	summary := trayEndpointSummary(trayEndpointFixture(), nil, now)

	if !summary.Available {
		t.Fatalf("registry 可读时 available 应为 true")
	}
	if len(summary.Groups) != 2 {
		t.Fatalf("groups = %d, want anthropic+openai 两组", len(summary.Groups))
	}

	anthropic := summary.Groups[0]
	openai := summary.Groups[1]
	if anthropic.Protocol != "anthropic" || openai.Protocol != "openai" {
		t.Fatalf("组协议顺序 = %s,%s", anthropic.Protocol, openai.Protocol)
	}

	byName := map[string]TrayEndpointItem{}
	for _, item := range anthropic.Items {
		byName[item.Name] = item
	}
	// enabled=0 不进 items
	if _, ok := byName["user-disabled"]; ok {
		t.Fatalf("enabled=0 行不应进 items")
	}
	if len(anthropic.Items) != 4 {
		t.Fatalf("anthropic items = %d (%+v), want 4", len(anthropic.Items), anthropic.Items)
	}
	// rank:pri 10>50,auto_group=0 与 priority=0 均为 0
	if byName["packy-code"].Rank != 1 || byName["glm-coding-plan"].Rank != 2 {
		t.Fatalf("rank 应按 priority 升序: packy=1,glm=2, got %d,%d",
			byName["packy-code"].Rank, byName["glm-coding-plan"].Rank)
	}
	if byName["manual-only"].Rank != 0 || byName["内置隧道"].Rank != 0 {
		t.Fatalf("auto_group=0/priority=0 应 rank=0, got manual=%d builtin=%d",
			byName["manual-only"].Rank, byName["内置隧道"].Rank)
	}
	// id=源行 ID 透传(钉选定位键,plan 2026-11-27 Task 1):按名字断言易撞
	// 重名行,radio 激活必须按 id 点名。
	if byName["packy-code"].ID != "b" || byName["glm-coding-plan"].ID != "a" ||
		byName["manual-only"].ID != "c" || byName["内置隧道"].ID != "builtin-aliang" {
		t.Fatalf("items 的 id 应透传源行 ID: %+v", anthropic.Items)
	}
	// cooldown → state=cooldown 且剩余秒>0
	if byName["packy-code"].State != "cooldown" || byName["packy-code"].CooldownRemainingSec <= 0 {
		t.Fatalf("packy-code = %+v, want state=cooldown 且剩余秒>0", byName["packy-code"])
	}
	if byName["glm-coding-plan"].State != "ok" {
		t.Fatalf("glm-coding-plan = %+v, want state=ok", byName["glm-coding-plan"])
	}
	// current:组内 rank 最小的 ok 行(rank1 在冷却 → 落 rank2)
	if anthropic.Current != "glm-coding-plan" {
		t.Fatalf("anthropic.current = %q, want glm-coding-plan(rank1 冷却跳过)", anthropic.Current)
	}

	if len(openai.Items) != 3 {
		t.Fatalf("openai items = %d (%+v), want 3", len(openai.Items), openai.Items)
	}
	openByName := map[string]TrayEndpointItem{}
	for _, item := range openai.Items {
		openByName[item.Name] = item
	}
	if openByName["removed"].State != "disabled" {
		t.Fatalf("removed = %+v, want state=disabled", openByName["removed"])
	}
	// openai 组 rank 只看 auto 池成员的 priority 升序:摘除行(e,pri 20)仍是
	// 池成员照拿徽标(rank 1),glm(pri 50)=rank 2;内置行 pri 0 不参 rank。
	// current 不受影响:摘除行不可选,跳过落 glm。
	if openByName["removed"].Rank != 1 || openByName["glm-coding-plan"].Rank != 2 {
		t.Fatalf("openai rank = removed:%d glm:%d, want removed:1 glm:2",
			openByName["removed"].Rank, openByName["glm-coding-plan"].Rank)
	}
	if openai.Current != "glm-coding-plan" {
		t.Fatalf("openai.current = %q, want glm-coding-plan", openai.Current)
	}
}

// TestTrayEndpointCooldownExpiryLazyRecover 冷却时间戳已过期 → 展示态回 ok
// (与 selectAuto 的懒恢复口径一致,面板不再显示「冷却 0s」)。
func TestTrayEndpointCooldownExpiryLazyRecover(t *testing.T) {
	now := time.Now().Unix()
	list := []*config.LLMEndpoint{
		{ID: "x", Name: "past-cooldown", Enabled: true, ProtocolAnthropic: true,
			AutoGroup: true, Priority: 10, HealthStatus: "cooldown", CooldownUntil: now - 30},
	}
	summary := trayEndpointSummary(list, nil, now)
	item := summary.Groups[0].Items[0]
	if item.State != "ok" || item.CooldownRemainingSec != 0 {
		t.Fatalf("过期冷却 = %+v, want state=ok 剩余 0", item)
	}
}

// TestTrayEndpointRateLimitedRowShowsCooldown 429 限流行(HealthStatus 恒 ok,
// endpoint_service 刻意「429 不改健康状态」)在限流窗口内必须按 cooldown 展示
// ——selectAuto 本尊会跳过它,面板若显示 ok 并选为 current 即失真。
func TestTrayEndpointRateLimitedRowShowsCooldown(t *testing.T) {
	now := time.Now().Unix()
	list := []*config.LLMEndpoint{
		// 健康位 ok 但 RateLimitedUntil 在未来:面板态应 cooldown+剩余秒>0,
		// 且不进 current(selectAuto 同窗口内会跳过它)。
		{ID: "rl", Name: "rate-limited", Enabled: true, ProtocolAnthropic: true,
			AutoGroup: true, Priority: 10, HealthStatus: "ok", RateLimitedUntil: now + 90},
		{ID: "ok1", Name: "healthy", Enabled: true, ProtocolAnthropic: true,
			AutoGroup: true, Priority: 20, HealthStatus: "ok"},
	}
	summary := trayEndpointSummary(list, nil, now)
	group := summary.Groups[0]
	item := group.Items[0]
	if item.State != "cooldown" || item.CooldownRemainingSec <= 0 {
		t.Fatalf("429 限流行 = %+v, want state=cooldown 且剩余秒>0", item)
	}
	if group.Current != "healthy" {
		t.Fatalf("current = %q, want healthy(限流行不可被选为当前激活)", group.Current)
	}
}

// TestTrayAgentKernelProviderTrimmed 内核键 trim 对齐:Provider 带首尾空格的
// 会话与干净键归并同一内核,卡名不留空格(map 键若用原始值会劈成两张卡)。
func TestTrayAgentKernelProviderTrimmed(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	sessions := []models.AgentVibeSession{
		{ID: "a", Provider: " claude ", Title: "带空格", Status: "idle", UpdatedAt: rfc(-time.Minute)},
		{ID: "b", Provider: "claude", Title: "干净键", Status: "idle", UpdatedAt: rfc(-2 * time.Minute)},
	}
	kernels := aggregateTrayAgentKernels(sessions, now)
	if len(kernels) != 1 {
		t.Fatalf("kernels = %d 张卡, want 1(带空格 Provider 应与干净键归并)", len(kernels))
	}
	if kernels[0].Name != "claude" {
		t.Fatalf("内核名 = %q, want claude(无首尾空格)", kernels[0].Name)
	}
	if kernels[0].Total24h != 2 {
		t.Fatalf("total_24h = %d, want 2(两会话同卡)", kernels[0].Total24h)
	}
}

// TestTrayEndpointSummaryUnavailable registry 不可用(单例 nil / 读库失败 /
// 空表)→ available=false,面板整区不渲染(spec §4.4 降级)。
func TestTrayEndpointSummaryUnavailable(t *testing.T) {
	if s := trayEndpointSummary(nil, errors.New("db down"), time.Now().Unix()); s.Available {
		t.Fatalf("读库失败应 available=false")
	}
	if s := trayEndpointSummary(nil, nil, time.Now().Unix()); s.Available {
		t.Fatalf("空表应 available=false")
	}
	if s := trayEndpointSummary(nil, nil, 0); s.Groups == nil {
		t.Fatalf("不可用时 groups 应为空数组而非 nil(JSON 需渲染 [])")
	}
}

// TestTrayAIProvidersMapping ai 子源映射:只列启用规则的 provider、label 回落
// preset、流量检测点亮 detected 并带 detail、全关时 enabled=false。
func TestTrayAIProvidersMapping(t *testing.T) {
	yes := true
	no := false
	rules := map[string]*config.CustomerAIRuleSetting{
		"Anthropic": {Label: "Claude", Enble: &yes},
		"openai":    {Enble: &yes},
		"cursor":    {Label: "Cursor", Enble: &no},
	}
	presets := []config.AIRuleProviderPreset{
		{Key: "anthropic", Label: "Anthropic"},
		{Key: "openai", Label: "OpenAI"},
		{Key: "cursor", Label: "Cursor"},
	}
	traffic := []*statistic.AIActivityDetection{
		{ProviderKey: "anthropic", HitCount: 2, Active: true},
	}

	providers := trayAIProviders(rules, presets, traffic)
	if len(providers) != 2 {
		t.Fatalf("providers = %+v, want 2(禁用的 cursor 剔除)", providers)
	}
	if providers[0].Key != "anthropic" || providers[0].Label != "Claude" {
		t.Fatalf("providers[0] = %+v, want anthropic/Claude", providers[0])
	}
	if !providers[0].Detected || !strings.Contains(providers[0].Detail, "2") {
		t.Fatalf("anthropic 应点亮且 detail 带命中数, got %+v", providers[0])
	}
	if providers[1].Detected || providers[1].Label != "OpenAI" {
		t.Fatalf("openai 无流量应灰且 label 回落 preset, got %+v", providers[1])
	}

	// 全关 → 空 providers(上层据此置 enabled=false)
	if got := trayAIProviders(map[string]*config.CustomerAIRuleSetting{"cursor": {Enble: &no}}, presets, nil); len(got) != 0 {
		t.Fatalf("全关应无 provider, got %+v", got)
	}
	// 规则表为空 → 空(nil rules 不炸)
	if got := trayAIProviders(nil, presets, nil); len(got) != 0 {
		t.Fatalf("nil rules 应得空 providers, got %+v", got)
	}
}

// TestTrayRunSummaryFromStatus run 子源映射:GetStatus 的 map 键 → 结构体字段,
// blocked 透传登出/恢复中闸码。
func TestTrayRunSummaryFromStatus(t *testing.T) {
	s := trayRunSummaryFromStatus(map[string]interface{}{
		"current_mode": "tun",
		"is_running":   false,
		"description":  "Deep Mode is selected, service not running",
	}, TrayRunBlocked{Code: "session_recovering", Msg: "登录恢复中"}, "127.0.0.1:56432")

	if s.Mode != "tun" || s.IsRunning || s.StatusDesc != "Deep Mode is selected, service not running" {
		t.Fatalf("run = %+v", s)
	}
	if s.ListenAddr != "127.0.0.1:56432" {
		t.Fatalf("listen_addr = %q, want 127.0.0.1:56432", s.ListenAddr)
	}
	if s.Blocked.Code != "session_recovering" || s.Blocked.Msg != "登录恢复中" {
		t.Fatalf("blocked = %+v, want 透传", s.Blocked)
	}
}

// TestServeTraySummaryMethodNotAllowed 非 GET 拒绝(对齐 agent_handler.go
// 的 405 惯例):面板只读通道,写方法不应漏过来源闸白耗鉴权。
func TestServeTraySummaryMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/tray/summary", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	serveTraySummaryWith(rec, req, traySummaryTestGatherers())
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: code = %d, want 405", rec.Code)
	}
}

// TestTrayAgentsSnapshotCacheTTL TTL 备忘:窗口内重复调用不再触发底层采集
// (面板 2s 轮询 × 2s TTL ≈ 每两次轮询最多一次真实扫描),过期后重扫。
func TestTrayAgentsSnapshotCacheTTL(t *testing.T) {
	var calls atomic.Int64
	collect := func() *TrayAgentsSummary {
		calls.Add(1)
		return &TrayAgentsSummary{Kernels: []TrayAgentKernel{{Name: "claude", Total24h: 1}}}
	}
	cache := newTrayAgentsSnapshotCache(50*time.Millisecond, collect)

	first := cache.get()
	if first == nil || len(first.Kernels) != 1 || first.Kernels[0].Name != "claude" {
		t.Fatalf("首采结果 = %+v, want claude 内核", first)
	}
	second := cache.get()
	if calls.Load() != 1 {
		t.Fatalf("TTL 内第二次调用采集次数 = %d, want 1(备忘快照生效)", calls.Load())
	}
	if second != first {
		t.Fatalf("TTL 内应返回同一份快照(零拷贝复用)")
	}

	time.Sleep(60 * time.Millisecond)
	cache.get()
	if calls.Load() != 2 {
		t.Fatalf("TTL 过期后采集次数 = %d, want 2(应重扫)", calls.Load())
	}
}

// TestTrayAgentsSnapshotCacheSingleflight 冷缓存并发 N 调用只触发一次采集
// (singleflight 合并,等待者共享结果)——慢机器上 2s 轮询堆叠不叠加全量扫描。
func TestTrayAgentsSnapshotCacheSingleflight(t *testing.T) {
	var calls atomic.Int64
	collect := func() *TrayAgentsSummary {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond) // 拉长采集窗,让并发调用全部落进同一航班
		return &TrayAgentsSummary{Kernels: []TrayAgentKernel{{Name: "codex"}}}
	}
	cache := newTrayAgentsSnapshotCache(time.Second, collect)

	const n = 10
	results := make([]*TrayAgentsSummary, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = cache.get()
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("并发 %d 调用采集次数 = %d, want 1(singleflight 合并)", n, got)
	}
	for i, v := range results {
		if v == nil || len(v.Kernels) != 1 || v.Kernels[0].Name != "codex" {
			t.Fatalf("等待者 %d 未共享采集结果: %+v", i, v)
		}
	}
}

// trayAssertKeys 断言 map 的键集合恰为期望集(多键=契约外泄,少键=契约缺失)。
func trayAssertKeys(t *testing.T, m map[string]interface{}, want ...string) {
	t.Helper()
	if len(m) != len(want) {
		t.Fatalf("键数 = %d (%v), want %v", len(m), trayMapKeys(m), want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺键 %q; 实际 = %v", k, trayMapKeys(m))
		}
	}
}

func trayMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
