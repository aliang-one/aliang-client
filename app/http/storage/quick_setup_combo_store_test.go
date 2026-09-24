package storage

import (
	"errors"
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
	got2, err := s.GetByID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Variables["model"] != "m2" || got2.Files[0].Content != "updated {{model}}" {
		t.Fatalf("update lost: %+v", got2)
	}

	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByID(c.ID); err == nil {
		t.Fatal("deleted combo still readable")
	}
	// 更新已删除的 id：RowsAffected==0 → ErrComboNotFound
	if err := s.Update(got); !errors.Is(err, ErrComboNotFound) {
		t.Fatalf("update deleted id must return ErrComboNotFound, got %v", err)
	}
}

func TestComboStore_UniqueSoftwareName(t *testing.T) {
	s := newTestComboStore(t)
	if err := s.Create(sampleCombo("codex", "a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(sampleCombo("codex", "a")); !errors.Is(err, ErrComboNameTaken) {
		t.Fatalf("duplicate software+name must fail with ErrComboNameTaken, got %v", err)
	}
	if err := s.Create(sampleCombo("claude-code", "a")); err != nil {
		t.Fatalf("same name under other software must pass: %v", err)
	}
}

func TestComboStore_SetDefaultExclusive(t *testing.T) {
	s := newTestComboStore(t)
	a, b := sampleCombo("codex", "a"), sampleCombo("codex", "b")
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(b); err != nil {
		t.Fatal(err)
	}
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
	// 跨 software 设默认必须失败（id 属于 codex，对 opencode 设默认 → ErrComboNotFound）
	if err := s.Create(sampleCombo("opencode", "o1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault("opencode", a.ID); !errors.Is(err, ErrComboNotFound) {
		t.Fatalf("set default across software must fail with ErrComboNotFound, got %v", err)
	}
	// 失败的 SetDefault 事务回滚：原 default（b）必须保留
	list2, err := s.ListBySoftware("codex")
	if err != nil {
		t.Fatal(err)
	}
	var stillDefault int64
	defaults = 0
	for _, c := range list2 {
		if c.IsDefault {
			defaults++
			stillDefault = c.ID
		}
	}
	if defaults != 1 || stillDefault != b.ID {
		t.Fatalf("failed SetDefault must preserve original default b=%d, got %d (defaults=%d)", b.ID, stillDefault, defaults)
	}
}

func TestComboStore_CapPerSoftware(t *testing.T) {
	s := newTestComboStore(t)
	for i := 0; i < quickSetupComboMaxPerSoftware; i++ {
		if err := s.Create(sampleCombo("codex", fmt.Sprintf("c%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Create(sampleCombo("codex", "overflow")); !errors.Is(err, ErrComboCapExceeded) {
		t.Fatalf("cap must block 51st combo with ErrComboCapExceeded, got %v", err)
	}
}

func TestComboStore_ViewHydratePath(t *testing.T) {
	// 已填充行（hydrate 路径）：瞬态字段非 nil，视图直接采用瞬态值，JSON 列不参与。
	row := &models.QuickSetupCombo{
		ID: 1, Software: "codex", Name: "a",
		Variables:     map[string]string{"model": "transient"},
		Files:         []models.QuickSetupComboFile{{Code: "config", Content: "transient"}},
		VariablesJSON: `{"model":"stale"}`,
		FilesJSON:     `[{"code":"config","content":"stale"}]`,
	}
	view, err := ComboToView(row)
	if err != nil {
		t.Fatal(err)
	}
	if view.Variables["model"] != "transient" || len(view.Variables) != 1 {
		t.Fatalf("hydrated variables must win over json column: %+v", view.Variables)
	}
	if len(view.Files) != 1 || view.Files[0].Content != "transient" {
		t.Fatalf("hydrated files must win over json column: %+v", view.Files)
	}
}

func TestComboStore_ViewFallbackPath(t *testing.T) {
	// 裸行（回退路径）：瞬态字段 nil，回退到 JSON 列反序列化。
	row := &models.QuickSetupCombo{
		ID: 1, Software: "codex", Name: "a",
		VariablesJSON: `{"api_key":"sk-9"}`,
		FilesJSON:     `[{"code":"config","content":"c"}]`,
	}
	view, err := ComboToView(row)
	if err != nil {
		t.Fatal(err)
	}
	if view.Variables["api_key"] != "sk-9" || view.Variables == nil {
		t.Fatalf("bare row must fall back to json column with non-nil map: %+v", view.Variables)
	}
	if len(view.Files) != 1 || view.Files[0].Code != "config" || view.Files == nil {
		t.Fatalf("bare row must fall back to json column with non-nil slice: %+v", view.Files)
	}
}

func TestComboStore_ViewIntentionalEmpty(t *testing.T) {
	// 有意为空：非 nil 空 map/slice 必须被尊重，陈旧 JSON 不得经回退路径泄漏。
	row := &models.QuickSetupCombo{
		ID: 1, Software: "codex", Name: "a",
		Variables:     map[string]string{},
		Files:         []models.QuickSetupComboFile{},
		VariablesJSON: `{"stale":"leak"}`,
		FilesJSON:     `[{"code":"stale","content":"leak"}]`,
	}
	view, err := ComboToView(row)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Variables) != 0 {
		t.Fatalf("intentionally empty variables must stay empty, got %+v", view.Variables)
	}
	if len(view.Files) != 0 {
		t.Fatalf("intentionally empty files must stay empty, got %+v", view.Files)
	}
}
