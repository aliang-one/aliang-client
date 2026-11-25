package storage

import (
	"errors"
	"strings"
	"testing"

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
	if err := s.UpdateHealth("h1", "cooldown", 3, 12345, 67890, "boom"); err != nil {
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
