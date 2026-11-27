package storage

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"aliang.one/nursorgate/processor/config"
)

func newTestStore(t *testing.T) *EndpointStore {
	t.Helper()
	s, err := NewEndpointStoreForTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEndpointStore_SeedBuiltin(t *testing.T) {
	s := newTestStore(t)
	es, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].ID != "builtin-aliang" {
		t.Fatalf("want builtin-aliang, got %+v", es)
	}
	if !es[0].ProtocolAnthropic || !es[0].ProtocolOpenAI {
		t.Fatal("builtin must support both protocols")
	}
	if es[0].BaseURL != "" || !es[0].Builtin || es[0].AutoGroup != true {
		t.Fatal("builtin shape wrong")
	}
}

func TestEndpointStore_DeleteBuiltinRejected(t *testing.T) {
	s := newTestStore(t)
	err := s.Delete(BuiltinEndpointID)
	if !errors.Is(err, ErrBuiltinImmutable) {
		t.Fatalf("want ErrBuiltinImmutable, got %v", err)
	}
}

func TestEndpointStore_CRUD(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "e1", Name: "prov", BaseURL: "https://x.example.com", APIKey: "sk-1",
		ProtocolAnthropic: true, AutoGroup: true, Priority: 10, Enabled: true}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("e1")
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "sk-1" {
		t.Fatal("key roundtrip failed")
	}
	got.Priority = 5
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("e1"); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointStore_Policy(t *testing.T) {
	s := newTestStore(t)
	p, err := s.GetPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !p.FailureDisable || p.FailureThreshold != 3 {
		t.Fatalf("default policy wrong: %+v", p)
	}
	p.FailureThreshold = 5
	if err := s.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	if p2, _ := s.GetPolicy(); p2.FailureThreshold != 5 {
		t.Fatal("policy not persisted")
	}
}

func TestEndpointStore_CreateDefaultsAndTimestamps(t *testing.T) {
	s := newTestStore(t)
	// 未给 Priority 与 Models:Create 归一(spec §5.1 默认 50 / models "[]")。
	e := &config.LLMEndpoint{ID: "d1", Name: "defaults", BaseURL: "https://d.example.com", APIKey: "sk-d",
		ProtocolAnthropic: true, Enabled: true}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	if e.Priority != 50 {
		t.Fatalf("non-builtin create must default priority to 50, got %d", e.Priority)
	}
	if e.Models != "[]" {
		t.Fatalf("models empty must normalize to [], got %q", e.Models)
	}
	if e.CreatedAt <= 0 || e.UpdatedAt <= 0 {
		t.Fatalf("timestamps must be filled on create: created=%d updated=%d", e.CreatedAt, e.UpdatedAt)
	}
	got, err := s.Get("d1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority != 50 || got.Models != "[]" {
		t.Fatalf("defaults not persisted: %+v", got)
	}
	if got.CreatedAt != e.CreatedAt {
		t.Fatalf("created_at persisted mismatch: %d vs %d", got.CreatedAt, e.CreatedAt)
	}
}

func TestEndpointStore_UpdateRefreshesUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "u1", Name: "upd", BaseURL: "https://u.example.com", APIKey: "sk-u",
		ProtocolAnthropic: true, Enabled: true, Priority: 20}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	before := e.CreatedAt
	// 把 updated_at 拨回过去再改:证明 Update 会重盖时间戳而不是透传调用方
	// 的旧值。unix 秒粒度下同秒内 updated==created,断言用 >= 而非严格 >
	// (严格 > 会随秒界 flaky)。
	e.UpdatedAt = 1
	if err := s.Update(e); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get("u1")
	if err != nil {
		t.Fatal(err)
	}
	if after.CreatedAt != before {
		t.Fatalf("created_at must not change on update: %d vs %d", after.CreatedAt, before)
	}
	if after.UpdatedAt <= 1 {
		t.Fatalf("updated_at must be refreshed on update, got %d", after.UpdatedAt)
	}
	if after.UpdatedAt < after.CreatedAt {
		t.Fatalf("updated_at must be >= created_at: %d vs %d", after.UpdatedAt, after.CreatedAt)
	}
}

func TestEndpointStore_BuiltinInvariants(t *testing.T) {
	s := newTestStore(t)
	// Create:任意 id 伪装 builtin 插队 → 拒(Validate 放行空 base_url,所以
	// 这一枪必须由 store 层不变量拦)。
	impersonator := &config.LLMEndpoint{ID: "fake-builtin", Name: "fake", Builtin: true,
		AutoGroup: true, ProtocolAnthropic: true, Enabled: true}
	if err := s.Create(impersonator); err == nil {
		t.Fatal("create with builtin:true and foreign id must be rejected")
	}
	// Create:builtin 行 auto_group 被关 → 拒(spec §5.1: auto_group 恒 1)。
	noAuto := &config.LLMEndpoint{ID: BuiltinEndpointID, Name: "x", Builtin: true, AutoGroup: false,
		ProtocolAnthropic: true, Enabled: true}
	if err := s.Create(noAuto); err == nil {
		t.Fatal("builtin with auto_group=false must be rejected")
	}
	// Create:builtin 行 priority 被抬高 → 拒(恒 0 永远第一)。
	hiPri := &config.LLMEndpoint{ID: BuiltinEndpointID, Name: "x", Builtin: true, AutoGroup: true,
		Priority: 10, ProtocolAnthropic: true, Enabled: true}
	if err := s.Create(hiPri); err == nil {
		t.Fatal("builtin with priority!=0 must be rejected")
	}
	// Update:把用户行翻成 builtin(空 base_url 绕过 Validate,只能靠 store
	// 不变量)白嫖平台 relay 凭据 → 拒。
	user := &config.LLMEndpoint{ID: "u3", Name: "user", ProtocolAnthropic: true,
		Enabled: true, Priority: 30}
	if err := s.Create(user); err != nil {
		t.Fatal(err)
	}
	user.Builtin = true
	if err := s.Update(user); err == nil {
		t.Fatal("update flipping user row to builtin must be rejected")
	}
	// Update:把内置行的 builtin 摘掉(反向;否则 Delete 按 flag 判定就失守)→ 拒。
	builtin, err := s.Get(BuiltinEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	builtin.Builtin = false
	if err := s.Update(builtin); err == nil {
		t.Fatal("un-builtin-ing the builtin row must be rejected")
	}
	// 内置行本身允许改协议开关(spec §5.1: protocol_openai 可改)→ 过。
	builtin2, err := s.Get(BuiltinEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	builtin2.ProtocolOpenAI = false
	if err := s.Update(builtin2); err != nil {
		t.Fatalf("editing builtin protocol toggles must be allowed, got %v", err)
	}
}

func TestEndpointStore_UpdateHealthNarrowWrite(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "h1", Name: "health", BaseURL: "https://h.example.com", APIKey: "sk-h",
		ProtocolAnthropic: true, Enabled: true, Priority: 30, HealthStatus: "ok"}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	// Task 4 的 GET 快照:此刻健康态还是 ok,随后服务面改健康,REST 面拿
	// 快照做全行 PUT——健康列必须保住服务面的值,不被快照盖回。
	stale := *e
	stale.Name = "renamed"
	stale.Priority = 99
	stale.Enabled = false // bool 零值也要能写穿(Select+struct 语义),停用开关靠它
	if err := s.UpdateHealth("h1", HealthUpdate{Status: "cooldown", Failures: 3, CooldownUntil: 12345, RateLimitedUntil: 67890, LastErr: "boom"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("h1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HealthStatus != "cooldown" || got.ConsecutiveFailures != 3 ||
		got.CooldownUntil != 12345 || got.RateLimitedUntil != 67890 || got.LastError != "boom" {
		t.Fatalf("health columns not written by UpdateHealth: %+v", got)
	}
	if got.Priority != 30 || got.Name != "health" || !got.Enabled {
		t.Fatalf("UpdateHealth must not touch config columns: %+v", got)
	}
	// REST 语义:全行 Update(带陈旧健康快照)不碰健康列。
	if err := s.Update(&stale); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get("h1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "renamed" || after.Priority != 99 || after.Enabled {
		t.Fatalf("config columns must follow the full-row update: %+v", after)
	}
	if after.HealthStatus != "cooldown" || after.ConsecutiveFailures != 3 ||
		after.CooldownUntil != 12345 || after.RateLimitedUntil != 67890 || after.LastError != "boom" {
		t.Fatalf("full-row update must not clobber health columns: %+v", after)
	}
}

func TestEndpointStore_DuplicateIDRejected(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "dup", Name: "first", BaseURL: "https://d.example.com", APIKey: "k",
		ProtocolAnthropic: true, Enabled: true, Priority: 10}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	dup := &config.LLMEndpoint{ID: "dup", Name: "second", BaseURL: "https://d2.example.com", APIKey: "k2",
		ProtocolOpenAI: true, Enabled: true, Priority: 20}
	err := s.Create(dup)
	if err == nil {
		t.Fatal("duplicate id must be rejected")
	}
	// 「可映射」= 我们自己的文案(上层可据此回 409),不是透传 sqlite 约束错。
	if !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("duplicate id must map to a friendly error, got %q", err)
	}
}

// ---- 二期增量(spec 2026-11-27 §4/§5.7):egress_kernel 出口指定 + 计数列 ----

// egressCount 数当前 egress_kernel=1 的行数——「全表至多一行=1」不变量的直接
// 证据:换绑/取消只要漏改任何一行,行数断言就会现形,比逐行比对更硬。
func egressCount(t *testing.T, s *EndpointStore) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(&config.LLMEndpoint{}).Where("egress_kernel = ?", true).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func newAnthropicEndpoint(t *testing.T, s *EndpointStore, id string) {
	t.Helper()
	if err := s.Create(&config.LLMEndpoint{ID: id, Name: id, BaseURL: "https://" + id + ".example.com",
		APIKey: "sk-" + id, ProtocolAnthropic: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// 换绑唯一性(spec §4):设 A→设 B,旧出口行必须熄灭,全表恒至多一行=1;
// 取消后全表归零。
func TestEndpointStore_SetEgressKernelRebindUniqueness(t *testing.T) {
	s := newTestStore(t)
	newAnthropicEndpoint(t, s, "eg-a")
	newAnthropicEndpoint(t, s, "eg-b")

	if err := s.SetEgressKernel("eg-a", true); err != nil {
		t.Fatal(err)
	}
	if n := egressCount(t, s); n != 1 {
		t.Fatalf("after set A want exactly 1 egress row, got %d", n)
	}
	// 换绑 B:A 必须被同一事务清掉。
	if err := s.SetEgressKernel("eg-b", true); err != nil {
		t.Fatal(err)
	}
	ga, err := s.Get("eg-a")
	if err != nil {
		t.Fatal(err)
	}
	gb, err := s.Get("eg-b")
	if err != nil {
		t.Fatal(err)
	}
	if ga.EgressKernel || !gb.EgressKernel {
		t.Fatalf("rebind must move the flag A=0 B=1, got A=%v B=%v", ga.EgressKernel, gb.EgressKernel)
	}
	if n := egressCount(t, s); n != 1 {
		t.Fatalf("at most one egress row after rebind, got %d", n)
	}
	// 取消:全表 0。
	if err := s.SetEgressKernel("eg-b", false); err != nil {
		t.Fatal(err)
	}
	if n := egressCount(t, s); n != 0 {
		t.Fatalf("cancel must clear the flag, got %d rows still set", n)
	}
}

// 非 anthropic 行拒设(spec §4):错误必须哨兵化(ErrNotAnthropic,handler 据此
// 映射 400),且拒设不得留下半态;取消路径不需要 anthropic 前提——协议被翻转
// 的旧行也要有逃生通道。
func TestEndpointStore_SetEgressKernelNonAnthropicRejected(t *testing.T) {
	s := newTestStore(t)
	if err := s.Create(&config.LLMEndpoint{ID: "eg-o", Name: "openai-only",
		BaseURL: "https://o.example.com", APIKey: "sk-o", ProtocolOpenAI: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	err := s.SetEgressKernel("eg-o", true)
	if !errors.Is(err, ErrNotAnthropic) {
		t.Fatalf("want ErrNotAnthropic, got %v", err)
	}
	got, err := s.Get("eg-o")
	if err != nil {
		t.Fatal(err)
	}
	if got.EgressKernel {
		t.Fatal("rejected set must leave the row untouched")
	}
	if err := s.SetEgressKernel("eg-o", false); err != nil {
		t.Fatalf("cancel must not require anthropic protocol, got %v", err)
	}
}

// builtin 行可设(spec §4/§8:内置链路本身就是 anthropic),且可取消回落。
func TestEndpointStore_SetEgressKernelBuiltinAllowed(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetEgressKernel(BuiltinEndpointID, true); err != nil {
		t.Fatalf("builtin row must be settable as egress, got %v", err)
	}
	got, err := s.Get(BuiltinEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EgressKernel {
		t.Fatal("builtin egress flag must persist")
	}
	if n := egressCount(t, s); n != 1 {
		t.Fatalf("want exactly 1 egress row, got %d", n)
	}
	if err := s.SetEgressKernel(BuiltinEndpointID, false); err != nil {
		t.Fatal(err)
	}
	if n := egressCount(t, s); n != 0 {
		t.Fatalf("builtin cancel must clear the flag, got %d", n)
	}
}

// 不存在的行:set=true/false 都报「不存在」(gorm.ErrRecordNotFound 包裹),
// handler(Task 6)据此统一映射 404——取消一个拼错的 id 不该被静默吞掉。
func TestEndpointStore_SetEgressKernelMissingRow(t *testing.T) {
	s := newTestStore(t)
	for _, set := range []bool{true, false} {
		err := s.SetEgressKernel("no-such-row", set)
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("set=%v: want ErrRecordNotFound, got %v", set, err)
		}
	}
}

// 停用行与出口标记共存(spec §4 用户拍板:出口「暂停保留」——enabled=0 时
// 内核跳过该行回 auto 调度,重新激活后出口恢复,不撤标记)。
func TestEndpointStore_SetEgressKernelCoexistsWithDisabled(t *testing.T) {
	s := newTestStore(t)
	if err := s.Create(&config.LLMEndpoint{ID: "eg-d", Name: "paused",
		BaseURL: "https://d.example.com", APIKey: "sk-d", ProtocolAnthropic: true, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEgressKernel("eg-d", true); err != nil {
		t.Fatalf("disabled row must keep the egress flag (pause-not-revoke), got %v", err)
	}
	got, err := s.Get("eg-d")
	if err != nil {
		t.Fatal(err)
	}
	if !got.EgressKernel || got.Enabled {
		t.Fatalf("egress=1 must coexist with enabled=0, got egress=%v enabled=%v",
			got.EgressKernel, got.Enabled)
	}
}

// MarkUsed 窄写(spec §5.7):打点只动 last_used_at,config 列与健康列原值
// 一概不动——用整行逐字段相等证明,任何漏守的列都会现形。
func TestEndpointStore_MarkUsedNarrowWrite(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "mu1", Name: "used", BaseURL: "https://mu.example.com", APIKey: "sk-mu",
		ProtocolAnthropic: true, Enabled: true, Priority: 42}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	// 先把健康态写成非默认值,证明打点不碰健康列。
	if err := s.UpdateHealth("mu1", HealthUpdate{Status: "cooldown", Failures: 2,
		CooldownUntil: 999, LastErr: "boom"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get("mu1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUsed("mu1"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get("mu1")
	if err != nil {
		t.Fatal(err)
	}
	// 打点目标:last_used_at 落在当下(unix 秒粒度,放宽 1s 防秒界毛刺)。
	now := time.Now().Unix()
	if after.LastUsedAt < now-1 || after.LastUsedAt > now+1 {
		t.Fatalf("last_used_at must be ~now, got %d (now %d)", after.LastUsedAt, now)
	}
	// 窄写契约:除 last_used_at 外整行相等。gorm 会顺带刷 updated_at(记账列,
	// 与 UpdateHealth 同款行为),一并排除。
	b2 := *before
	b2.LastUsedAt, b2.UpdatedAt = 0, 0
	a2 := *after
	a2.LastUsedAt, a2.UpdatedAt = 0, 0
	if b2 != a2 {
		t.Fatalf("MarkUsed must only write last_used_at, row changed: %+v -> %+v", b2, a2)
	}
}

// HealthUpdate 计数列扩展(spec §5.7):增量落列且在原值上累加(ReportSuccess/
// ReportFailure 是事件不是快照);last_used_at 显式值落列,0 是「不动」哨兵
// (unix 秒恒 >0,不可能是合法打点值——不加守卫会把 MarkUsed 刚写的时刻冲回 0)。
func TestEndpointStore_UpdateHealthCounterColumns(t *testing.T) {
	s := newTestStore(t)
	e := &config.LLMEndpoint{ID: "uc1", Name: "counters", BaseURL: "https://uc.example.com", APIKey: "sk-uc",
		ProtocolAnthropic: true, Enabled: true}
	if err := s.Create(e); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHealth("uc1", HealthUpdate{Status: "ok",
		TotalDelta: 5, FailedDelta: 2, LastUsedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("uc1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalRequests != 5 || got.FailedRequests != 2 {
		t.Fatalf("counter deltas must land: total=%d failed=%d", got.TotalRequests, got.FailedRequests)
	}
	if got.LastUsedAt != 1700000000 {
		t.Fatalf("last_used_at must land when given, got %d", got.LastUsedAt)
	}
	// 二写:增量累加;LastUsedAt=0 不冲旧值;健康列照旧窄写。
	if err := s.UpdateHealth("uc1", HealthUpdate{Status: "cooldown", Failures: 1,
		CooldownUntil: 555, TotalDelta: 1}); err != nil {
		t.Fatal(err)
	}
	got2, err := s.Get("uc1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.TotalRequests != 6 || got2.FailedRequests != 2 {
		t.Fatalf("deltas must accumulate: total=%d failed=%d", got2.TotalRequests, got2.FailedRequests)
	}
	if got2.LastUsedAt != 1700000000 {
		t.Fatalf("last_used_at=0 must be a no-op sentinel, got %d", got2.LastUsedAt)
	}
	if got2.HealthStatus != "cooldown" || got2.ConsecutiveFailures != 1 || got2.CooldownUntil != 555 {
		t.Fatalf("existing health columns must keep working: %+v", got2)
	}
}

// egress_kernel/last_used_at 不进 endpointConfigColumns(spec §4 评审修正③):
// 全行 PUT 带陈旧快照(egress_kernel=false)不得把出口标记载掉——白名单是
// 硬防线,注释说了不算,列名审计 + 功能双证。
func TestEndpointStore_EgressColumnsOutsideConfigWhitelist(t *testing.T) {
	s := newTestStore(t)
	newAnthropicEndpoint(t, s, "wl1")
	if err := s.SetEgressKernel("wl1", true); err != nil {
		t.Fatal(err)
	}
	// 模拟 GET-merge-PUT:调用方手里的快照没有出口字段,PUT 回去整行落库。
	stale := &config.LLMEndpoint{ID: "wl1", Name: "wl1", BaseURL: "https://wl1.example.com",
		APIKey: "sk-wl1", ProtocolAnthropic: true, Enabled: true, Priority: 50}
	if err := s.Update(stale); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get("wl1")
	if err != nil {
		t.Fatal(err)
	}
	if !after.EgressKernel {
		t.Fatal("full-row PUT with stale snapshot must not clear egress_kernel")
	}
	// 列名审计:防将来有人「顺手」把这两列加进白名单。
	for _, c := range endpointConfigColumns {
		if c == "egress_kernel" || c == "last_used_at" {
			t.Fatalf("column %q must stay out of endpointConfigColumns (spec §4)", c)
		}
	}
}

// DDL 实证:四条新列必须以 spec §4 的精确列名落表——gorm 蛇形纪律一期踩过
// (ProtocolOpenAI → protocol_open_ai),显式 column tag 钉住后用真 DDL 收口,
// 不赌 NamingStrategy。
func TestEndpointStore_EgressColumnsDDL(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Raw("PRAGMA table_info(llm_endpoints)").Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"egress_kernel", "last_used_at", "total_requests", "failed_requests"} {
		if !cols[want] {
			t.Fatalf("column %q missing from llm_endpoints DDL, got %v", want, cols)
		}
	}
}
