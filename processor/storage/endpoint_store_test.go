package storage

import (
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
	if err := s.Delete("builtin-aliang"); err == nil {
		t.Fatal("want error deleting builtin")
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
