package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func newTestComboStore(t *testing.T) *QuickSetupComboStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewQuickSetupComboStoreWithDBPath(filepath.Join(dir, "combos.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sampleCombo(software, name string) *models.QuickSetupCombo {
	return &models.QuickSetupCombo{
		Software: software, Name: name,
		Variables: map[string]string{"base_url": "http://x", "api_key": "sk-1", "model": "m1"},
		Files: []models.QuickSetupComboFile{
			{Code: "config", Content: "model = \"{{model}}\"\n"},
		},
	}
}

func TestComboStore_CRUD(t *testing.T) {
	s := newTestComboStore(t)
	c := sampleCombo("codex", "default")
	if err := s.Create(c); err != nil {
		t.Fatal(err)
	}
	if c.ID == 0 {
		t.Fatal("id not set")
	}

	got, err := s.GetByID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "default" || got.Variables["api_key"] != "sk-1" || len(got.Files) != 1 || got.Files[0].Content == "" {
		t.Fatalf("json round-trip broken: %+v", got)
	}

	got.Variables["model"] = "m2"
	got.Files[0].Content = "updated {{model}}"
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetByID(c.ID)
	if got2.Variables["model"] != "m2" || got2.Files[0].Content != "updated {{model}}" {
		t.Fatalf("update lost: %+v", got2)
	}

	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByID(c.ID); err == nil {
		t.Fatal("deleted combo still readable")
	}
}

func TestComboStore_UniqueSoftwareName(t *testing.T) {
	s := newTestComboStore(t)
	if err := s.Create(sampleCombo("codex", "a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(sampleCombo("codex", "a")); err == nil {
		t.Fatal("duplicate software+name must fail")
	}
	if err := s.Create(sampleCombo("claude-code", "a")); err != nil {
		t.Fatalf("same name under other software must pass: %v", err)
	}
}

func TestComboStore_SetDefaultExclusive(t *testing.T) {
	s := newTestComboStore(t)
	a, b := sampleCombo("codex", "a"), sampleCombo("codex", "b")
	s.Create(a)
	s.Create(b)
	if err := s.SetDefault("codex", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault("codex", b.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListBySoftware("codex")
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, c := range list {
		if c.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("default not exclusive: %+v", list)
	}
	// 跨 software 设默认必须失败（id 属于 codex，对 opencode 设默认 → 报错）
	s.Create(sampleCombo("opencode", "o1"))
	if err := s.SetDefault("opencode", a.ID); err == nil {
		t.Fatal("set default across software must fail")
	}
}

func TestComboStore_CapPerSoftware(t *testing.T) {
	s := newTestComboStore(t)
	for i := 0; i < quickSetupComboMaxPerSoftware; i++ {
		if err := s.Create(sampleCombo("codex", fmt.Sprintf("c%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Create(sampleCombo("codex", "overflow")); err == nil {
		t.Fatal("cap must block 51st combo")
	}
}
