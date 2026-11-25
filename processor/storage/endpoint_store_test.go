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
