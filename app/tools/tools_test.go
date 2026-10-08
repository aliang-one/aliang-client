package tools

import (
	"encoding/json"
	"testing"
)

func TestNewRegistry_RejectsInvalidTool(t *testing.T) {
	cases := []struct {
		name string
		tool *Tool
	}{
		{"empty id", &Tool{Event: "x.y", Description: "d"}},
		{"empty event", &Tool{ID: "x", Description: "d"}},
		{"empty description", &Tool{ID: "x", Event: "x.y"}},
		{"nil handler", &Tool{ID: "x", Event: "x.y", Description: "d"}},
		{"bad event charset", &Tool{ID: "x", Event: "X Y", Description: "d", Handler: func(map[string]interface{}) map[string]interface{} { return nil }}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic for %s", tc.name)
				}
			}()
			NewRegistry(1, tc.tool)
		})
	}
}

func TestRegistry_DuplicateEventPanics(t *testing.T) {
	h := func(map[string]interface{}) map[string]interface{} { return nil }
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate event")
		}
	}()
	NewRegistry(1,
		&Tool{ID: "a", Event: "x.y", Description: "d", Handler: h},
		&Tool{ID: "b", Event: "x.y", Description: "d", Handler: h},
	)
}

func TestRegistry_GetAndList(t *testing.T) {
	h := func(map[string]interface{}) map[string]interface{} { return nil }
	reg := NewRegistry(7,
		&Tool{ID: "a", Event: "x.a", Description: "da", Handler: h},
		&Tool{ID: "b", Event: "x.b", Description: "db", Handler: h},
	)
	if reg.Rev() != 7 {
		t.Fatalf("rev = %d, want 7", reg.Rev())
	}
	if got, ok := reg.Get("x.a"); !ok || got.ID != "a" {
		t.Fatalf("Get(x.a) = %v, %v", got, ok)
	}
	if _, ok := reg.Get("x.zzz"); ok {
		t.Fatal("Get(unknown) should miss")
	}
	if len(reg.List()) != 2 {
		t.Fatalf("List len = %d, want 2", len(reg.List()))
	}
}

func TestTool_DescriptorJSON(t *testing.T) {
	h := func(map[string]interface{}) map[string]interface{} { return nil }
	tool := &Tool{
		ID: "list_dir", Event: "file.list", Description: "List entries.",
		Parameters: map[string]interface{}{"type": "object"},
		Caps:       Caps{ReadOnly: true, NeedsProject: true, TimeoutMs: 10000, MaxOutputBytes: 131072},
		Handler:    h,
	}
	d := tool.DescriptorJSON()
	raw, _ := json.Marshal(d)
	var back map[string]interface{}
	_ = json.Unmarshal(raw, &back)
	for _, k := range []string{"id", "event", "name", "description", "parameters", "caps"} {
		if _, ok := back[k]; !ok {
			t.Fatalf("descriptor missing key %q: %s", k, raw)
		}
	}
	caps := back["caps"].(map[string]interface{})
	if caps["read_only"] != true || caps["timeout_ms"] != float64(10000) {
		t.Fatalf("caps mismatch: %v", caps)
	}
	if back["name"] != "list_dir" {
		t.Fatalf("name should equal id, got %v", back["name"])
	}
}
