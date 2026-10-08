package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func ok(map[string]interface{}) map[string]interface{} { return nil }

func TestNewRegistry_RejectsInvalidTool(t *testing.T) {
	cases := []struct {
		name string
		tool *Tool
	}{
		{"nil tool", nil},
		{"empty id", &Tool{Event: "x.y", Description: "d", Handler: ok}},
		{"empty event", &Tool{ID: "x", Description: "d", Handler: ok}},
		{"empty description", &Tool{ID: "x", Event: "x.y", Handler: ok}},
		{"nil handler", &Tool{ID: "x", Event: "x.y", Description: "d"}},
		{"nil parameters", &Tool{ID: "x", Event: "x.y", Description: "d", Handler: ok}},
		{"bad event charset", &Tool{ID: "x", Event: "X Y", Description: "d", Handler: ok}},
		{"id starts with digit", &Tool{ID: "1x", Event: "x.y", Description: "d", Handler: ok}},
		{"event starts with digit", &Tool{ID: "x", Event: "1y", Description: "d", Handler: ok}},
		{"id too long", &Tool{ID: strings.Repeat("a", 65), Event: "x.y", Description: "d", Handler: ok}},
		{"description too long", &Tool{ID: "x", Event: "x.y", Description: strings.Repeat("a", 501), Handler: ok}},
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
	p := map[string]interface{}{"type": "object"}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate event")
		}
	}()
	NewRegistry(1,
		&Tool{ID: "a", Event: "x.y", Description: "d", Parameters: p, Handler: h},
		&Tool{ID: "b", Event: "x.y", Description: "d", Parameters: p, Handler: h},
	)
}

func TestRegistry_DuplicateIDPanics(t *testing.T) {
	h := func(map[string]interface{}) map[string]interface{} { return nil }
	p := map[string]interface{}{"type": "object"}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate id")
		}
	}()
	NewRegistry(1,
		&Tool{ID: "a", Event: "x.a", Description: "d", Parameters: p, Handler: h},
		&Tool{ID: "a", Event: "x.b", Description: "d", Parameters: p, Handler: h},
	)
}

func TestRegistry_GetAndList(t *testing.T) {
	h := func(map[string]interface{}) map[string]interface{} { return nil }
	reg := NewRegistry(7,
		&Tool{ID: "b", Event: "x.b", Description: "db", Parameters: map[string]interface{}{"type": "object"}, Handler: h},
		&Tool{ID: "a", Event: "x.a", Description: "da", Parameters: map[string]interface{}{"type": "object"}, Handler: h},
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
	if reg.List()[0].ID != "a" {
		t.Fatalf("List should be sorted by ID, first = %s", reg.List()[0].ID)
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
