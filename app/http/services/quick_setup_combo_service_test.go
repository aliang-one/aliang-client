package services

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/processor/config"
)

// stubComboServiceEnv 注入组合服务全链路依赖（镜像 quick_setup_render_test.go 的
// 既有钩子模式）：全局 config 的 api_server（base_url 预设来源）+ 临时路径 SQLite
// store（经 quickSetupComboStoreFn 钩子，绝不碰真实库）。
// 预设断言值：https://api.example.com 的 host 非控制面域名 → 原样透传，
// codex/opencode 追加 /v1，claude-code/pi 不追加。
func stubComboServiceEnv(t *testing.T) (*QuickSetupComboService, *storage.QuickSetupComboStore) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.example.com"},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)

	store, err := storage.NewQuickSetupComboStoreWithDBPath(filepath.Join(t.TempDir(), "combos.db"))
	if err != nil {
		t.Fatal(err)
	}
	previousStore := quickSetupComboStoreFn
	quickSetupComboStoreFn = func() *storage.QuickSetupComboStore { return store }
	t.Cleanup(func() { quickSetupComboStoreFn = previousStore })

	return NewQuickSetupComboService(), store
}

// stubComboTargetHome 注入目标用户家目录（disk 入口读盘来源）。
func stubComboTargetHome(t *testing.T, home string) {
	t.Helper()
	previousUser := quickSetupTargetUserFn
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	t.Cleanup(func() { quickSetupTargetUserFn = previousUser })
}

func writeComboDiskFixture(t *testing.T, home, rel, content string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestComboService_CreateBlank 锁定 blank 入口契约（spec §6.1）：文件 = 空白模板，
// variables 预填 base_url 公网预设（codex 带 /v1）+ 空 api_key/model。
func TestComboService_CreateBlank(t *testing.T) {
	svc, _ := stubComboServiceEnv(t)

	view, err := svc.Create("codex", " 我的套餐 ", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Software != "codex" {
		t.Fatalf("software = %q, want codex", view.Software)
	}
	if view.Name != "我的套餐" {
		t.Fatalf("name = %q, want trimmed 我的套餐", view.Name)
	}
	if view.IsDefault {
		t.Fatal("blank combo must not be default")
	}
	if want := quickSetupComboBlankTemplates("codex"); !reflect.DeepEqual(view.Files, want) {
		t.Fatalf("files mismatch:\ngot  %+v\nwant %+v", view.Files, want)
	}
	if got := view.Variables["base_url"]; got != "https://api.example.com/v1" {
		t.Fatalf("base_url = %q, want codex public preset with /v1", got)
	}
	if view.Variables["api_key"] != "" || view.Variables["model"] != "" {
		t.Fatalf("api_key/model must be empty, got %+v", view.Variables)
	}
}

// TestComboService_CreateCopy 锁定 copy 入口契约：深拷贝源组合 variables/files，
// 名字取请求值且非默认；copy_from_id 不存在 → ErrComboNotFound。
func TestComboService_CreateCopy(t *testing.T) {
	svc, store := stubComboServiceEnv(t)

	source, err := svc.Create("codex", "源组合", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 改源组合为非默认内容，验证拷贝取的是最新值而非 blank 预设。
	row, err := store.GetByID(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.Variables = map[string]string{"base_url": "https://custom.example/v1", "api_key": "sk-custom", "model": "gpt-custom"}
	row.Files = []models.QuickSetupComboFile{{Code: "config", Content: "# custom content"}}
	if err := store.Update(row); err != nil {
		t.Fatal(err)
	}

	view, err := svc.Create("codex", "副本测试", "copy", source.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "副本测试" {
		t.Fatalf("name = %q, want 副本测试", view.Name)
	}
	if view.IsDefault {
		t.Fatal("copy must not inherit default flag")
	}
	wantVars := map[string]string{"base_url": "https://custom.example/v1", "api_key": "sk-custom", "model": "gpt-custom"}
	if !reflect.DeepEqual(view.Variables, wantVars) {
		t.Fatalf("variables mismatch:\ngot  %+v\nwant %+v", view.Variables, wantVars)
	}
	wantFiles := []models.QuickSetupComboFile{{Code: "config", Content: "# custom content"}}
	if !reflect.DeepEqual(view.Files, wantFiles) {
		t.Fatalf("files mismatch:\ngot  %+v\nwant %+v", view.Files, wantFiles)
	}

	if _, err := svc.Create("codex", "不存在的源", "copy", source.ID+9999, nil, nil); !errors.Is(err, storage.ErrComboNotFound) {
		t.Fatalf("copy from missing id must return ErrComboNotFound, got %v", err)
	}
}

// TestComboService_CreateDiskImport 锁定 disk 入口契约（spec §6.1）：磁盘内容逐字节
// 入库（无占位符反推）、variables 为空 map；任一声明文件 missing/unreadable → 报错
// 点名文件。
func TestComboService_CreateDiskImport(t *testing.T) {
	svc, _ := stubComboServiceEnv(t)

	home := t.TempDir()
	configContent := "# user original\nmodel = \"gpt-5\"\n"
	authContent := "{\"OPENAI_API_KEY\":\"sk-disk\"}\n"
	writeComboDiskFixture(t, home, ".codex/config.toml", configContent)
	writeComboDiskFixture(t, home, ".codex/auth.json", authContent)
	stubComboTargetHome(t, home)

	view, err := svc.Create("codex", "磁盘导入", "disk", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := []models.QuickSetupComboFile{
		{Code: "config", Content: configContent},
		{Code: "auth", Content: authContent},
	}
	if !reflect.DeepEqual(view.Files, wantFiles) {
		t.Fatalf("files mismatch:\ngot  %+v\nwant %+v", view.Files, wantFiles)
	}
	if len(view.Variables) != 0 {
		t.Fatalf("disk import variables must be empty, got %+v", view.Variables)
	}

	// 删掉 auth.json → 报错点名该文件。
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create("codex", "再导一次", "disk", 0, nil, nil)
	if err == nil {
		t.Fatal("disk import must fail when a declared file is missing")
	}
	if !strings.Contains(err.Error(), "auth.json") {
		t.Fatalf("error must name auth.json, got %v", err)
	}
}

// TestComboService_SeedIfEmpty 锁定种子契约：空库创建「默认」组合（is_default=true、
// blank 模板、claude-code 的 base_url 公网预设不带 /v1）；幂等；删光后重调重新种子。
func TestComboService_SeedIfEmpty(t *testing.T) {
	svc, store := stubComboServiceEnv(t)

	if err := svc.SeedIfEmpty("claude-code"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListBySoftware("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("seeded combos = %d, want 1", len(rows))
	}
	seeded := rows[0]
	if seeded.Name != "默认" {
		t.Fatalf("seeded name = %q, want 默认", seeded.Name)
	}
	if !seeded.IsDefault {
		t.Fatal("seeded combo must be default")
	}
	if want := quickSetupComboBlankTemplates("claude-code"); !reflect.DeepEqual(seeded.Files, want) {
		t.Fatalf("seeded files mismatch:\ngot  %+v\nwant %+v", seeded.Files, want)
	}
	if got := seeded.Variables["base_url"]; got != "https://api.example.com" {
		t.Fatalf("claude-code base_url = %q, want public preset without /v1", got)
	}

	// 幂等：再调一次不新增。
	if err := svc.SeedIfEmpty("claude-code"); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListBySoftware("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("seed must be idempotent, got %d combos", len(rows))
	}

	// 手工删光后重调 → 重新种子。
	for _, row := range rows {
		if err := store.Delete(row.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SeedIfEmpty("claude-code"); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListBySoftware("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].IsDefault || rows[0].Name != "默认" {
		t.Fatalf("re-seed after delete-all broken: %+v", rows)
	}
}

// TestComboService_Update 锁定部分更新契约（spec §6.2）：name/vars/files 可选（nil 不动）、
// files code 必须在声明内、撞名/不存在分别上抛 ErrComboNameTaken/ErrComboNotFound。
func TestComboService_Update(t *testing.T) {
	svc, _ := stubComboServiceEnv(t)

	view, err := svc.Create("codex", "combo-a", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create("codex", "combo-b", "blank", 0, nil, nil); err != nil {
		t.Fatal(err)
	}

	newName := "combo-a-renamed"
	newVars := map[string]string{"base_url": "http://127.0.0.1:56432/v1", "api_key": "sk-new", "model": "gpt-x"}
	newFiles := []models.QuickSetupComboFile{{Code: "config", Content: "new template {{model}}"}}
	updated, err := svc.Update(view.ID, &newName, newVars, newFiles)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "combo-a-renamed" || updated.Variables["api_key"] != "sk-new" || updated.Files[0].Content != "new template {{model}}" {
		t.Fatalf("update lost: %+v", updated)
	}

	// 部分更新：只改名，vars/files 不被清掉
	partialName := "combo-a-renamed-2"
	if _, err := svc.Update(view.ID, &partialName, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Update(view.ID, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != partialName || after.Variables["api_key"] != "sk-new" || after.Files[0].Content != "new template {{model}}" {
		t.Fatalf("partial update clobbered: %+v", after)
	}

	// 撞名
	dup := "combo-b"
	if _, err := svc.Update(view.ID, &dup, nil, nil); !errors.Is(err, storage.ErrComboNameTaken) {
		t.Fatalf("want ErrComboNameTaken, got %v", err)
	}

	// files code 越界
	badFiles := []models.QuickSetupComboFile{{Code: "nope", Content: "x"}}
	if _, err := svc.Update(view.ID, nil, nil, badFiles); err == nil || !strings.Contains(err.Error(), "file code is not valid") {
		t.Fatalf("want file code error, got %v", err)
	}

	// 不存在 id
	missingID := int64(999999)
	if _, err := svc.Update(missingID, nil, nil, nil); !errors.Is(err, storage.ErrComboNotFound) {
		t.Fatalf("want ErrComboNotFound, got %v", err)
	}
}

func TestComboService_Delete(t *testing.T) {
	svc, _ := stubComboServiceEnv(t)
	view, err := svc.Create("codex", "to-delete", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(view.ID); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListBySoftware("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == view.ID {
			t.Fatal("deleted combo still listed")
		}
	}
	if err := svc.Delete(view.ID); !errors.Is(err, storage.ErrComboNotFound) {
		t.Fatalf("want ErrComboNotFound on re-delete, got %v", err)
	}
}

func TestComboService_SetDefault(t *testing.T) {
	svc, _ := stubComboServiceEnv(t)
	if _, err := svc.Create("codex", "a", "blank", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create("codex", "b", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDefault(b.ID); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListBySoftware("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || !list[0].IsDefault || list[0].ID != b.ID {
		t.Fatalf("default not first/exclusive: %+v", list)
	}
	for _, c := range list {
		if c.ID != b.ID && c.IsDefault {
			t.Fatalf("old default still set: %+v", list)
		}
	}
	if err := svc.SetDefault(999999); !errors.Is(err, storage.ErrComboNotFound) {
		t.Fatalf("want ErrComboNotFound, got %v", err)
	}
}

// TestComboService_SetDefaultAndList 锁定 handler set-default 端点依赖的组合
// 方法：设默认后一次返回该 software 全部组合（默认在前，目标独占 default）。
func TestComboService_SetDefaultAndList(t *testing.T) {
	svc, _ := stubComboServiceEnv(t)
	if _, err := svc.Create("codex", "a", "blank", 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create("codex", "b", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	combos, err := svc.SetDefaultAndList(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 2 {
		t.Fatalf("combos = %d, want 2: %+v", len(combos), combos)
	}
	if !combos[0].IsDefault || combos[0].ID != b.ID {
		t.Fatalf("default not first: %+v", combos)
	}
	for _, c := range combos {
		if c.ID != b.ID && c.IsDefault {
			t.Fatalf("default not exclusive: %+v", combos)
		}
	}

	if _, err := svc.SetDefaultAndList(999999); !errors.Is(err, storage.ErrComboNotFound) {
		t.Fatalf("want ErrComboNotFound, got %v", err)
	}
}
