# 快速配置 v3（配置组合管理）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 快速配置弹窗重构为「配置组合（套餐）管理」：每 agent 多个命名组合存 SQLite，组合文件用 `{{base_url}}/{{api_key}}/{{model}}` 占位符，configure 表单填变量即时预览，apply 逐字落盘（备份先行契约原样）；pi 成为第四个支持的 agent；v2 的服务端 render 链退役。

**Architecture:** 组合定义存 SQLite（新 store，复用 software_config_store 模式，JSON 列手动序列化——全仓无 serializer:json 先例）；前端拿到模板后本地替换占位符做零延迟预览；apply 复用现有端点（备份先行/原子写/回滚零改动）仅新增占位符校验；render/models 端点与渲染链、merge 引擎、custom-* 概念全部退役。

**Tech Stack:** Go（GORM+SQLite、net/http）、Vue 3（QuickSetupModal 重构）、vitest。

**Spec:** `docs/superpowers/specs/2026-09-23-config-combos-design.md`

**约定（全计划生效）：**
- Go 测试：仓库根 `go test -count=1 ./app/http/services/ ./app/http/handlers/ ./app/http/storage/`；前端 `cd app/website && npx vitest run`、构建 `npm run build`（build 后 `git checkout app/website/dist/` 还原被跟踪的 dist）。
- 提交标题全中文「新增：/修复：/重构：/测试：」；HEREDOC 提交，末尾空行 + `🤖 Generated with [Claude Code](https://claude.com/claude-code)` + `Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>`。**CLAUDE.md 政策：开工前向用户确认一次「逐任务提交」授权即可覆盖全程。**
- 工作流：Task 0 按 worktree 记忆流程建分支（spec/plan 未跟踪文件随 stash 进入）。
- 行号是 v2 合并后（master e400314）的参考锚点，**以函数名为准**。
- 已知预存 flake（agent_ai.go 子进程 2s/5s 硬超时三个测试）全仓高并行下偶败，单包复跑绿即通过，勿当回归、勿改 agent_ai.go。

---

## 文件结构（谁负责什么）

```
后端新增
  app/http/models/quick_setup_combo.go     Combo 存储模型（gorm 标量列 + JSON text 列）
  app/http/storage/quick_setup_combo_store.go  CRUD/事务设默认/计数（复用 store 单例模式）
  app/http/services/quick_setup_combo_service.go  组合业务：三入口创建/种子/更新/删除/设默认/占位符
  app/http/handlers/quick_setup_combo_handler.go  5 个组合端点 thin 包装
后端修改
  app/http/services/quick_setup_catalog.go     +pi 检测与 Files 声明 +空白模板表；-custom-* 分支
  app/http/services/quick_setup_apply.go       +占位符校验；-custom-* 分支；+承接 validateQuickSetupOpenCode(从 render.go 迁入)
  app/http/services/quick_setup_service.go     Catalog 增强（combos+presets）；render/models 入口删除
  app/http/services/quick_setup_support.go     新文件：render.go 退役后的幸存者（keys 转换/URL 派生/读盘 helper/presets）
  app/http/services/quick_setup_render.go      删除（幸存者迁走后）
  app/http/services/quick_setup_merge.go       删除（quickSetupCodexProviderID 迁至 catalog.go）
  app/http/models/quick_setup.go               +Combo 类型；-Render/Models 相关 DTO
  app/http/handlers/quick_setup_handler.go     HandleRender/HandleModels 删除
  app/http/routes/routes.go                    -render/models 路由；+combos 5 条
前端
  components/QuickSetupModal.vue               重构（组合 tabs/文件 tabs/渲染视图⇄模板编辑/configure/apply；-custom/-mode/-key 选择器/-render 调用）
  components/QuickSetupConfigurePanel.vue      新增（三变量表单）
  components/QuickSetupResultPanel.vue         复用不动
  components/QuickSetupStatePanel.vue          复用不动
  services/quickSetupApi.js                    +combos CRUD；-render/models
  utils/quickSetupState.js(+test)              +渲染/占位符纯函数；-renderGuard/modeState/custom 判定
  i18n/zh.js / en.js                           qs_ 键增删（现 94 个，位于 3-96 行连续块）
```

---

### Task 0: worktree 与提交授权

- [ ] **Step 1:** 主仓执行（未跟踪 spec/plan 随 stash 进入 worktree）：

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git stash push -u -m "config-combos docs"
git worktree add ../alianggate-config-combos -b feature/config-combos
cd ../alianggate-config-combos && git stash pop
```

- [ ] **Step 2:** 向用户确认「逐任务提交」授权（覆盖全程，含本计划所有 Commit 步骤）。

---

### Task 1: Combo 模型 + SQLite store

**Files:**
- Create: `app/http/models/quick_setup_combo.go`
- Create: `app/http/storage/quick_setup_combo_store.go`
- Test: `app/http/storage/quick_setup_combo_store_test.go`

- [ ] **Step 1: 写失败测试**（镜像 `software_config_store` 的测试注入模式；先读该文件与其 test）

```go
package storage

import (
	"path/filepath"
	"testing"
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
	if err := s.Create(c); err != nil { t.Fatal(err) }
	if c.ID == 0 { t.Fatal("id not set") }

	got, err := s.GetByID(c.ID)
	if err != nil { t.Fatal(err) }
	if got.Name != "default" || got.Variables["api_key"] != "sk-1" || len(got.Files) != 1 || got.Files[0].Content == "" {
		t.Fatalf("json round-trip broken: %+v", got)
	}

	got.Variables["model"] = "m2"
	got.Files[0].Content = "updated {{model}}"
	if err := s.Update(got); err != nil { t.Fatal(err) }
	got2, _ := s.GetByID(c.ID)
	if got2.Variables["model"] != "m2" || got2.Files[0].Content != "updated {{model}}" {
		t.Fatalf("update lost: %+v", got2)
	}

	if err := s.Delete(c.ID); err != nil { t.Fatal(err) }
	if _, err := s.GetByID(c.ID); err == nil { t.Fatal("deleted combo still readable") }
}

func TestComboStore_UniqueSoftwareName(t *testing.T) {
	s := newTestComboStore(t)
	if err := s.Create(sampleCombo("codex", "a")); err != nil { t.Fatal(err) }
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
	s.Create(a); s.Create(b)
	if err := s.SetDefault("codex", a.ID); err != nil { t.Fatal(err) }
	if err := s.SetDefault("codex", b.ID); err != nil { t.Fatal(err) }
	list, err := s.ListBySoftware("codex")
	if err != nil { t.Fatal(err) }
	defaults := 0
	for _, c := range list {
		if c.IsDefault { defaults++ }
	}
	if defaults != 1 || list[0].IsDefault == list[1].IsDefault {
		t.Fatalf("default not exclusive: %+v", list)
	}
	// 跨 software 互不影响
	s.Create(sampleCombo("opencode", "o1"))
	if err := s.SetDefault("opencode", list[0].ID /* 越界 software 组合应报错 */); err == nil {
		t.Fatal("set default across software must fail")
	}
}

func TestComboStore_CapPerSoftware(t *testing.T) {
	s := newTestComboStore(t)
	for i := 0; i < quickSetupComboMaxPerSoftware; i++ {
		if err := s.Create(sampleCombo("codex", fmt.Sprintf("c%d", i))); err != nil { t.Fatal(err) }
	}
	if err := s.Create(sampleCombo("codex", "overflow")); err == nil {
		t.Fatal("cap must block 51st combo")
	}
}
```

- [ ] **Step 2:** `go test ./app/http/storage/ -run TestComboStore -v` → FAIL（类型未定义）

- [ ] **Step 3: 实现 models**（`app/http/models/quick_setup_combo.go`；gorm 标量列风格对齐 models/config.go；**JSON 列用 text + 手动序列化，不用 serializer:json——全仓无先例**）：

```go
package models

import "time"

// QuickSetupCombo 是一个配置组合（套餐）的存储行：变量值 + 文件模板。
// Variables/Files 以 JSON 文本列存储（Store 层负责序列化，全仓无 serializer:json 先例）。
type QuickSetupCombo struct {
	ID            int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Software      string `json:"software" gorm:"type:varchar(64);not null;uniqueIndex:idx_combo_sw_name"`
	Name          string `json:"name" gorm:"type:varchar(128);not null;uniqueIndex:idx_combo_sw_name"`
	IsDefault     bool   `json:"is_default" gorm:"not null;default:false"`
	VariablesJSON string `json:"-" gorm:"type:text;not null"`
	FilesJSON     string `json:"-" gorm:"type:text;not null"`
	CreatedAt     time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

// QuickSetupComboFile 是组合内的一个文件模板，Code 对应 software 声明的 file code。
type QuickSetupComboFile struct {
	Code    string `json:"code"`
	Content string `json:"content"` // 含 {{base_url}}/{{api_key}}/{{model}} 占位符
}

// QuickSetupComboView 是组合的 API 视图（Variables/Files 已反序列化）。
type QuickSetupComboView struct {
	ID        int64                 `json:"id"`
	Software  string                `json:"software"`
	Name      string                `json:"name"`
	IsDefault bool                  `json:"is_default"`
	Variables map[string]string     `json:"variables"`
	Files     []QuickSetupComboFile `json:"files"`
}
```

常量 `quickSetupComboMaxPerSoftware = 50` 放 store 文件（测试引用它）。

- [ ] **Step 4: 实现 store**（`quick_setup_combo_store.go`，镜像 software_config_store 的单例 + WithDBPath + ResetForTest 三件套）：
  - `NewQuickSetupComboStore()`（sync.Once + `cache.GetUnifiedDataDBPath()`）、`NewQuickSetupComboStoreWithDBPath(dbPath)`、`ResetQuickSetupComboStoreForTest()`
  - `openQuickSetupComboStore`: `gorm.Open(sqlite.Open(abs))` + `AutoMigrate(&models.QuickSetupCombo{})`
  - `Create(c *models.QuickSetupCombo) error`：`CountBySoftware` ≥ 上限 → error「combo cap exceeded」；`VariablesJSON,_ = json.Marshal(c.Variables)`（nil → `{}`）、`FilesJSON` 同理（nil → `[]`）后 `db.Create`
  - `Update(c *models.QuickSetupCombo) error`：先 Marshal 再 `db.Model(&models.QuickSetupCombo{ID: c.ID}).Updates(map[string]interface{}{"name":..., "is_default":..., "variables_json":..., "files_json":..., "updated_at": time.Now()})`（Updates 用 map 避免 gorm 零值跳过）
  - `Delete(id int64) error`、`GetByID(id) (*models.QuickSetupCombo, error)`（gorm ErrRecordNotFound 上抛）
  - `ListBySoftware(software) ([]models.QuickSetupCombo, error)`（ORDER BY is_default DESC, id ASC）
  - `SetDefault(software string, id int64) error`：`db.Transaction` 内先 `Model.Where("software = ?").Update("is_default", false)` 再 `Where("id = ? AND software = ?").Update("is_default", true)`，`RowsAffected == 0` → error（跨 software/不存在）
  - marshaling helper：`comboToView(row *models.QuickSetupCombo) (*models.QuickSetupComboView, error)`（JSON Unmarshal，失败返回错误）
- [ ] **Step 5:** 测试 PASS + 全量三包 `go build ./... && go test -count=1 ./app/http/storage/` 绿。
- [ ] **Step 6: Commit** `git commit -m "新增：配置组合 SQLite 存储模型与 CRUD（唯一约束/默认互斥/50 上限）"`

---

### Task 2: pi agent 接入 catalog 声明

**Files:**
- Modify: `app/http/services/quick_setup_catalog.go`（quickSetupSoftwares :31-95 增 pi；quickSetupDetectionRules :356-360 增 pi）
- Test: `app/http/services/quick_setup_catalog_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestQuickSetupSoftwares_PiDeclared(t *testing.T) {
	sw, ok := findQuickSetupSoftware("pi")
	if !ok { t.Fatal("pi missing") }
	if len(sw.Files) != 2 { t.Fatalf("pi files: %+v", sw.Files) }
	// models.json + settings.json，format 均 json
	if sw.Files[0].DefaultPath != "~/.pi/agent/models.json" || sw.Files[1].DefaultPath != "~/.pi/agent/settings.json" {
		t.Fatalf("pi paths: %+v", sw.Files)
	}
}

func TestDetectQuickSetupInstalled_Pi(t *testing.T) {
	home := t.TempDir()
	orig := quickSetupLookPathCLIFn
	quickSetupLookPathCLIFn = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { quickSetupLookPathCLIFn = orig }()
	if detectQuickSetupInstalled("pi", home) { t.Fatal("must not detect") }
	if err := os.MkdirAll(filepath.Join(home, ".pi"), 0o700); err != nil { t.Fatal(err) }
	if !detectQuickSetupInstalled("pi", home) { t.Fatal("pi should be installed via ~/.pi") }
}
```

- [ ] **Step 2:** 确认 FAIL。
- [ ] **Step 3: 实现**：pi software 声明（Name "Pi"，SupportedProviders `["anthropic","openai"]`，Files：`code:"models"` label/models.json → `~/.pi/agent/models.json` format json；`code:"settings"` → `~/.pi/agent/settings.json` format json）；检测规则 `pi: {cliNames:["pi"], dirs:[".pi"]}`。注意 catalog 测试（TestCatalogMarksInstalled 等）会受新增 software 影响——installed 断言补 pi。
- [ ] **Step 4:** 定向 PASS + 全量两包绿（预期个别既有 catalog 测试需补 pi 的 installed=false 断言——语义不变仅扩集合）。
- [ ] **Step 5: Commit** `git commit -m "新增：pi 成为第四个快速配置支持的 agent（检测规则与文件声明）"`

---

### Task 3: 空白模板表 + 占位符纯函数（后端侧）

**Files:**
- Modify: `app/http/services/quick_setup_catalog.go`（追加 `quickSetupComboBlankTemplates`；`quickSetupCodexProviderID` 常量从 merge.go 迁入此处——snapshot.go 的 managed 判定引用它，merge.go 删除前必须落位）
- Test: `app/http/services/quick_setup_catalog_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestQuickSetupComboBlankTemplates(t *testing.T) {
	for _, code := range []string{"claude-code", "codex", "opencode", "pi"} {
		files := quickSetupComboBlankTemplates(code)
		if len(files) == 0 { t.Fatalf("%s: empty templates", code) }
		sw, _ := findQuickSetupSoftware(code)
		if len(files) != len(sw.Files) { t.Fatalf("%s: file count mismatch", code) }
		for i, f := range files {
			if f.Code != sw.Files[i].Code { t.Fatalf("%s: code mismatch", code) }
			if !strings.Contains(f.Content, "{{base_url}}") || !strings.Contains(f.Content, "{{api_key}}") || !strings.Contains(f.Content, "{{model}}") {
				t.Fatalf("%s/%s: placeholders missing:\n%s", code, f.Code, f.Content)
			}
		}
	}
	if quickSetupComboBlankTemplates("nope") != nil { t.Fatal("unknown software must yield nil") }
}
```

- [ ] **Step 2:** 确认 FAIL。
- [ ] **Step 3: 实现**（每 agent 模板对照 spec §6.3；claude/pi 的 base_url **不带 /v1**，codex/opencode 带；codex 用 `quickSetupCodexProviderID`）：

```go
// quickSetupComboBlankTemplates 返回 software 的空白组合模板（含三占位符）。
func quickSetupComboBlankTemplates(softwareCode string) []models.QuickSetupComboFile { … }
```

  claude-code：`{"env": {"ANTHROPIC_BASE_URL": "{{base_url}}", "ANTHROPIC_AUTH_TOKEN": "{{api_key}}", "ANTHROPIC_MODEL": "{{model}}"}}`（MarshalIndent 2 空格）；codex config.toml：`model = "{{model}}"\nmodel_provider = "aliang"\napproval_policy = "never"\n\n[model_providers.aliang]\nname = "Aliang Gateway"\nbase_url = "{{base_url}}"\nenv_key = "OPENAI_API_KEY"\nwire_api = "responses"\n` + auth.json `{"OPENAI_API_KEY": "{{api_key}}"}`；opencode：`{"$schema":"https://opencode.ai/config.json","model":"aliang/{{model}}","provider":{"aliang":{"npm":"<npm 字面量>","options":{"baseURL":"{{base_url}}","apiKey":"{{api_key}}"}}}}`（npm 包名从现 render.go `quickSetupOpenCodeProviderNPM`(:471) 抄成**字面量**——该函数随 Task 9 删除，不得引用；anthropic/openai 各自的既有值照抄）；pi models.json：`{"providers":{"aliang":{"name":"Aliang Gateway","baseUrl":"{{base_url}}","apiKey":"{{api_key}}","api":"anthropic-messages","models":[{"id":"{{model}}","name":"{{model}}"}]}}}` + settings.json `{"defaultProvider":"aliang","defaultModel":"{{model}}"}`。
- [ ] **Step 4:** PASS + 全量绿。**注意**：此时 merge.go 仍在（常量迁移用「原处删除 + catalog.go 新增」，编译不破）。
- [ ] **Step 5: Commit** `git commit -m "新增：四 agent 组合空白模板与占位符（pi 模板按 anthropic-messages 形态）"`

---

### Task 4: Combo service——三入口创建 + 种子

**Files:**
- Create: `app/http/services/quick_setup_combo_service.go`
- Test: `app/http/services/quick_setup_combo_service_test.go`

- [ ] **Step 1: 写失败测试**（store 用真 SQLite 注入临时路径：service 构造接受 store；测试里 `NewQuickSetupComboStoreWithDBPath` + 钩子 `quickSetupComboStoreFn`；targetUser/authorization 钩子沿用现有模式）

```go
func TestComboService_CreateBlank(t *testing.T) {
	// create combo {software:"codex", name:"我的套餐", source:"blank"}
	// 断言：files = quickSetupComboBlankTemplates("codex") 且 variables 里 base_url 预填公网预设
	//（预设值来自 quickSetupPresets()，见 Task 6；此处断言 base_url == resolveQuickSetupInferenceBaseURL(quickSetupBaseURL())）
	// api_key/model 变量为空串（占位符未填状态）
}

func TestComboService_CreateCopy(t *testing.T) {
	// 先建源组合（含变量+文件），source:"copy", copy_from_id=源ID, name:"副本测试"
	// 断言：新组合变量/文件与源一致；名字为 "副本测试"
}

func TestComboService_CreateDiskImport(t *testing.T) {
	// home 预置 ~/.codex/config.toml + auth.json 真实内容（含用户注释）
	// source:"disk" → files 内容与磁盘逐字节一致（无占位符）；variables 为空 map
	// 再测文件不存在场景 → 返回错误且信息含路径
}

func TestComboService_SeedIdempotent(t *testing.T) {
	// software="claude-code" 无组合 → SeedIfEmpty 创建「默认」组合（is_default=true）
	// 再调一次 → 不重复创建；手工删光后重调 → 重新种子
}
```

- [ ] **Step 2:** 确认 FAIL。
- [ ] **Step 3: 实现**（`QuickSetupComboService` 结构持有 store；方法签名）：

```go
type QuickSetupComboService struct{ store *storage.QuickSetupComboStore }
func NewQuickSetupComboService() *QuickSetupComboService

func (s *QuickSetupComboService) Create(software, name, source string, copyFromID int64, vars map[string]string, files []models.QuickSetupComboFile) (models.QuickSetupComboView, error)
func (s *QuickSetupComboService) SeedIfEmpty(software string) error   // 「默认」组合，is_default=true
```

  - 入口分发：blank → `quickSetupComboBlankTemplates`；copy → `GetByID` 深拷贝（name 冲突时自动加「副本」后缀重试一次）；disk → `quickSetupTargetUserFn` 家目录 + 对声明文件逐个 `quickSetupReadExistingFile`（render.go :714，ok 态才收；missing/unreadable → error 含路径）
  - 公共校验：software 合法（findQuickSetupSoftware）、name TrimSpace 非空 ≤128、上限由 store 把关
  - base_url 预填：`quickSetupPresets()`（Task 6 实现；本任务先建该函数返回 `map[string]string{"base_url_local": "http://"+config.DefaultHTTPProxyAddr, "base_url_public": resolveQuickSetupInferenceBaseURL(quickSetupBaseURL())}`，放 quick_setup_support.go 新文件或暂置 combo service 文件内——Task 8 清理时归位 support 文件）
  - 种子：「默认」名 + blank 模板 + is_default=true + base_url=公网预设；`SeedIfEmpty` 用 `ListBySoftware` 空判断 + Create；**并发双种子时输家的 Create 撞唯一约束返回错误——该错误视为「已种子」吞掉**（返回 nil），其余错误照常上抛（否则会把 Catalog 打成 failed）
  - 视图转换：store 行 → `models.QuickSetupComboView`（JSON 反序列化）
- [ ] **Step 4:** PASS + 全量绿。
- [ ] **Step 5: Commit** `git commit -m "新增：组合三入口创建与默认组合幂等种子"`

---

### Task 5: Combo service——更新/删除/设默认

**Files:** Modify: `quick_setup_combo_service.go`；Test: 同文件测试追加

- [ ] **Step 1: 写失败测试**

```go
func TestComboService_Update(t *testing.T) {
	// 更新 name/variables/files → 视图反映全部变更；改名撞已有名 → 错误
	// files 的 code 不在 software 声明内 → 错误（"file code is not valid"）
}
func TestComboService_Delete(t *testing.T) { … }
func TestComboService_SetDefault(t *testing.T) {
	// 设默认后该 software 恰一个 default；对不存在 id → 错误
}
func TestComboService_ListBySoftware(t *testing.T) {
	// 返回 []QuickSetupComboView，default 排最前（store ORDER BY 已保证）
}
```

- [ ] **Step 2-4:** FAIL → 实现（`Update(id, name *string, vars map, files []file)`：逐项可选；files code 校验对照声明 → PASS → 全量绿。
- [ ] **Step 5: Commit** `git commit -m "新增：组合更新/删除/设默认/列表服务"`

---

### Task 6: Catalog 增强（combos + presets + 种子）

**Files:**
- Modify: `app/http/services/quick_setup_service.go`（Catalog :73-111）、`app/http/models/quick_setup.go`（QuickSetupCatalogResponse +combos/presets）、`quick_setup_support.go` 新建（放 presets 派生）
- Test: `quick_setup_service_test.go`（追加）

- [ ] **Step 1: 写失败测试**（镜像现有 Catalog 测试 :483-515 的 stub 模式 + combos store 注入）

```go
func TestCatalogCombosAndPresets(t *testing.T) {
	// stub：keys/global config/lookPath(命中 .claude)/targetUser→TempDir home
	// 首次 Catalog → data.Combos 含 claude-code 的「默认」组合（种子触发）；data.Presets.base_url_local == "http://127.0.0.1:56432"
	// （local 值经 config.DefaultHTTPProxyAddr 派生，测试断言用常量值字符串即可）
	// 二次 Catalog → Combos 数量不变（种子幂等）
	// presets.base_url_public == resolveQuickSetupInferenceBaseURL(quickSetupBaseURL())
}
```

- [ ] **Step 2:** 确认 FAIL。
- [ ] **Step 3: 实现**：
  - `app/http/services/quick_setup_support.go` 新建（本任务先放 presets；Task 8 清理时 render.go 幸存者迁入）：

```go
// quickSetupPresets 返回 configure 下拉与种子用的 base_url 预设（spec §4.0 单一事实源）。
func quickSetupPresets() (map[string]string, error) {
	root, err := quickSetupBaseURL()
	if err != nil { return nil, err }
	return map[string]string{
		"base_url_local":  "http://" + config.DefaultHTTPProxyAddr,
		"base_url_public": resolveQuickSetupInferenceBaseURL(root),
	}, nil
}
```

  - `QuickSetupCatalogResponse` 加 `Combos []models.QuickSetupComboView `json:"combos"`` + `Presets map[string]string `json:"presets"``
  - Catalog() 成功分支：对每个 installed software 调 `SeedIfEmpty` → `ListBySoftware` 汇总 Combos；`quickSetupPresets()` 失败 → status failed（与 api_server 缺失同路径）
  - combo service/store 实例：包级钩子 `quickSetupComboStoreFn = storage.NewQuickSetupComboStore`（测试注入；首次调用缓存）
- [ ] **Step 4:** PASS + 全量绿。
- [ ] **Step 5: Commit** `git commit -m "新增：catalog 下发组合列表与 base_url 预设（触发默认组合种子）"`

---

### Task 7: 组合 CRUD 端点 + 路由

**Files:**
- Create: `app/http/handlers/quick_setup_combo_handler.go`
- Modify: `app/http/handlers/quick_setup_handler.go`（NewQuickSetupHandler :23 组装 combo service）、`app/http/routes/routes.go`（:254 后追加 5 条）、`app/http/models/quick_setup.go`（请求 DTO）
- Test: `app/http/handlers/quick_setup_combo_handler_test.go` + 401 表更新

- [ ] **Step 1: 写失败测试**：401 枚举表（quick_setup_handler_test.go :41-53 的表驱动切片）追加 4 行（combos POST、combos/{id} PUT/DELETE、combos/{id}/default POST——路径带 id 的用占位 id，方法+路径+handler 对齐）；authenticated happy path：POST /combos blank → 200 响应 `{combo:{...}}`；PUT 保存 → `{combo}`；set-default → `{combos}`；DELETE → `{id}`；重名 → 400。
- [ ] **Step 2:** 确认 FAIL。
- [ ] **Step 3: 实现**：
  - models：`QuickSetupComboCreateRequest{Software, Name, Source, CopyFromID int64, Variables, Files}`、`QuickSetupComboUpdateRequest{Name *string, Variables map, Files []QuickSetupComboFile}`
  - handler 方法（thin，镜像 HandleRestore :173 风格：method 检查 → RequireDashboardSession → MaxBytesReader(:13 常量复用) → decode → service → writeJSON）：
    - `HandleCombosCreate`（POST /api/quick-setup/combos）→ `{combo}`
    - `HandleCombosUpdate`（PUT /api/quick-setup/combos/{id}）→ `{combo}`；`HandleCombosDelete`（DELETE）→ `{id}`
    - `HandleCombosSetDefault`（POST /api/quick-setup/combos/{id}/default）→ `{combos}`
  - 路由：`register("/api/quick-setup/combos", h.QuickSetupCombos.HandleCreate, http.MethodPost)` 等；`{id}` 路径参数用 `r.PathValue("id")`（Go 1.22+ ServeMux，先确认 routes.go 现有 mux 是否 `http.NewServeMux` 且已有 PathValue 先例——若无，用 `/api/quick-setup/combos/` 前缀 handler 内手动截取 id，镜像仓内既有风格）
  - `id` 解析失败 → 400；service 错误含 "not valid"/"is required" → 400，其余 500（镜像 isBadRequestError :270 分类）
- [ ] **Step 4:** PASS + 全量绿。
- [ ] **Step 5: Commit** `git commit -m "新增：配置组合 CRUD 端点（创建/保存/删除/设默认）"`

---

### Task 8: Apply 占位符校验

**Files:**
- Modify: `app/http/services/quick_setup_apply.go`（:120 空检查后插入，custom 分支删除前后皆可——Task 9 一并删）
- Test: `quick_setup_apply_test.go` 追加

- [ ] **Step 1: 写失败测试**

```go
func TestApplyRejectsUnresolvedPlaceholders(t *testing.T) {
	// 构造合法 codex apply，config.toml content 含 "base_url = \"{{base_url}}\""
	// → 错误信息含 "{{base_url}}" 与 "placeholder"
	// 全部替换后（用真实值）→ 通过校验正常写入
}
```

- [ ] **Step 2:** FAIL。
- [ ] **Step 3: 实现**（validateQuickSetupApplyFile :120 空检查之后、format 归一化之前，两分支共享）：

```go
var quickSetupPlaceholderRe = regexp.MustCompile(`\{\{\s*[a-zA-Z_][a-zA-Z0-9_]*\s*\}\}`)

// 占位符未替换即拒绝（spec §5）：组合渲染在前端完成，此处兜底。
if m := quickSetupPlaceholderRe.FindString(content); m != "" {
	return models.QuickSetupSoftwareFile{}, fmt.Errorf("file content is not valid: unresolved variable placeholder %s", m)
}
```

- [ ] **Step 4:** PASS + 全量绿。
- [ ] **Step 5: Commit** `git commit -m "新增：apply 占位符未替换校验（组合渲染兜底）"`

---

### Task 9: v2 退役大清理（render/models/merge/custom-*）

**Files:**
- Modify: `app/http/services/quick_setup_render.go`（删除文件）、`quick_setup_merge.go`（删除文件）、`quick_setup_apply.go`（-custom 分支 + 迁入校验器）、`quick_setup_catalog.go`（-custom 分支）、`quick_setup_service.go`（-Render/-Models 入口、-render 相关 DTO 结构体）、`quick_setup_backup.go`（:55 注释更新）、`app/http/models/quick_setup.go`（-Render/Models DTO）、`app/http/handlers/quick_setup_handler.go`（-HandleRender/HandleModels）、`app/http/routes/routes.go`（-:250-251）
- 测试同步：render/merge 相关测试删除；apply/catalog 测试中 custom 相关断言删除

- [ ] **Step 1: 迁出幸存者（删除前先建新家）**
  1. `quick_setup_support.go` 新文件，从 render.go **原样迁入**（零逻辑改动）：`toQuickSetupAPIKeys`(:193)、`quickSetupAPIKeyHasPlainSecret`(:236)、`quickSetupBaseURL`(:1022)、`resolveQuickSetupInferenceBaseURL`(:1034)、`quickSetupReadState`(:701)、`quickSetupReadExistingFile`(:714)、`quickSetupTargetIsNonRegular`(:755)、`quickSetupLeafFileName`(:769)、`quickSetupPresets`（Task 6 建的，若在别处则迁入）+ 文件头职责注释「keys 转换 / URL 派生 / 读盘 helper / presets（v2 render 链退役后的幸存者）」
  2. `validateQuickSetupOpenCode`(:125) + `validateQuickSetupOpenCodeModelRef`(:169) **迁入 quick_setup_apply.go**（apply 唯一调用方；依赖的 quickSetupOpenCodeConfig/Provider 结构体在 quick_setup_service.go :34 留守）
  3. `quickSetupCodexProviderID`(:38 of merge.go) **迁入 quick_setup_catalog.go**（snapshot.go managed 判定 + codex 空白模板引用；**Task 3 已迁过——此处仅验证落位，勿二次迁移**）
  4. 相关测试删除——显式清单：quick_setup_merge_test.go 与 quick_setup_render_test.go 整文件；quick_setup_service_test.go 中的 `TestQuickSetupService_Render_*`（:39/:176/:263/:337/:433/:553/:590/:618 八个）、`TestQuickSetupService_Models_FetchesFromSelectedKeyBaseURL`(:72)、`TestQuickSetupDefaultModel_UsesVerifiedOpenCodeModelIDs`(:499)、`TestQuickSetupProviderBaseURL_UsesSingleVersionPrefix`(:543)；`TestResolveQuickSetupInferenceBaseURL_*`(:315) 因函数迁 support **保留**；`quickSetupReadExistingFile` 相关测试随文件迁 support 测试文件
- [ ] **Step 2: 删除**：quick_setup_render.go、quick_setup_merge.go 两文件；`Render()`(:20 service.go) 与 `Models()` 入口；孤儿钩子 `quickSetupModelsHTTPClient`（service.go :63，unused var 不破编译、grep 清单抓不到，须显式删）及其 `net/http`/`time` import 清理；HandleRender/HandleModels；routes.go :250-251；models 中 `QuickSetupRenderRequest/OpenCodeRenderSpec/QuickSetupPreviewFile/QuickSetupVariant/QuickSetupRenderResponse/QuickSetupModelsRequest/QuickSetupModel/QuickSetupModelsResponse`；service.go 里 render 专用结构体（quickSetupModelEntry 等随文件已删）
- [ ] **Step 3: 删 custom-\\***：apply.go :124-132 分支与 :58/:115 注释；catalog.go :155-163（resolveQuickSetupApplyPath 的 custom 跳过——删除后所有 software 一律走 quickSetupBuiltInPathAllowed）、:209-218（allowedRoot 的 default custom 分支改为返回 "software is not valid" 错误）；backup.go :55 注释措辞更新；前端 custom 概念在 Task 12 处理
- [ ] **Step 4: 验证**：`gofmt -l app/http/services/` 无输出；`go build ./...`；`go vet ./app/...`；全量三包测试绿（render/merge 测试已删，其余契约——apply 备份/回滚/路径安全/snapshot/restore/handler——**必须全绿**）；grep 确认无残留引用：`grep -rn "renderQuickSetupFiles\|mergeCodexTOML\|mergeQuickSetupJSONObjects\|fetchQuickSetupModels\|HandleRender\|custom-" app/http/ --include="*.go" | grep -v _test` → 仅允许命中 blank 模板/注释性历史说明
- [ ] **Step 5: Commit** `git commit -m "重构：退役 v2 服务端渲染链与 custom 软件（组合体系取代；幸存 helper 迁入 support）"`

---

### Task 10: 后端全量回归

- [ ] **Step 1:** `gofmt -l app/ cmd/ internal/ processor/`（processor 预存 4 文件不属本分支）+ `go vet ./app/...` + `go test -count=1 ./app/http/...`（flake 政策见约定）
- [ ] **Step 2:** 定向 `go test -count=1 ./app/http/storage/ -run TestComboStore -v` 全 PASS。
- [ ] **Step 3:** 如有修补 → Commit `git commit -m "测试：配置组合后端回归通过"`（否则跳过）。

---

### Task 11: 前端 API 与纯函数

**Files:**
- Modify: `app/website/src/services/quickSetupApi.js`（-renderQuickSetup :48 / getQuickSetupModels :62；+combos CRUD）、`app/website/src/utils/quickSetupState.js`（+渲染/校验纯函数；-createLatestRenderGuard/-createQuickSetupModeState/-isBuiltInQuickSetupSoftware/-snapshotQuickSetupFiles 中过时者）、`utils/quickSetupState.test.js`
- Test: vitest

- [ ] **Step 1: 写失败测试**

```js
describe('renderComboContent', () => {
  it('replaces all three placeholders and does not rescan values', () => {
    const out = renderComboContent('u={{base_url}} k={{api_key}} m={{model}}', { base_url: 'http://127.0.0.1:56432', api_key: 'sk-{{x}}', model: 'm' });
    expect(out).toBe('u=http://127.0.0.1:56432 k=sk-{{x}} m=m'); // 值内占位符不再扫描
  });
});
describe('findUnresolvedPlaceholders', () => {
  it('lists remaining placeholders', () => {
    expect(findUnresolvedPlaceholders('a {{api_key}} b {{ model }} c {{model}')).toEqual(['api_key', 'model']);
  });
});
```

- [ ] **Step 2:** FAIL。
- [ ] **Step 3: 实现**：

```js
export function renderComboContent(content, variables) {
  let out = String(content ?? '');
  for (const [key, value] of Object.entries(variables || {})) {
    out = out.split(`{{${key}}}`).join(String(value ?? ''));
  }
  return out;
}
export function findUnresolvedPlaceholders(content) {
  const re = /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g;
  const names = []; let m;
  while ((m = re.exec(String(content ?? ''))) !== null) names.push(m[1]);
  return names;
}
```

  api.js：`createCombo(payload)` POST、`updateCombo(id, payload)` PUT、`deleteCombo(id)` DELETE、`setComboDefault(id)` POST——全部走 rawRequest（:13）；**删** `renderQuickSetup`/`getQuickSetupModels`。
  state.js：**删** `createLatestRenderGuard`、`createQuickSetupModeState`、`isBuiltInQuickSetupSoftware`、`snapshotQuickSetupFiles`（apply 快照逻辑随 Modal 重构内联）；`filterInstalledQuickSetupSoftwares` 简化为 `installed === true`（custom 概念移除）。
  同步改测试：删除 renderGuard/mode/custom 相关用例，新增上述两组。
- [ ] **Step 4:** vitest 全绿。
- [ ] **Step 5: Commit** `git commit -m "新增：组合渲染与占位符前端纯函数 + combos API（移除 render/models/guard/mode）"`

---

### Task 12: QuickSetupConfigurePanel.vue

**Files:** Create: `app/website/src/components/QuickSetupConfigurePanel.vue`

- [ ] **Step 1: 实现组件**（风格对齐 QuickSetupResultPanel/StatePanel：`<script setup>` + useI18n + tailwind）：
  - props：`{ variables: Object, presets: Object, apiKeys: Array }`；emits：`['confirm', 'cancel']`
  - 三字段：base_url（select 三选项：`presets.base_url_local` 标签「本地加速」/ `presets.base_url_public`「公网直连」/ 自定义输入——选「自定义」时出现 text input）、api_key（password 式 input + 「从密钥列表选」下拉，选中即填充 `apiKeys[].key`）、model（text input）
  - 确定按钮 emit `confirm`（携带新 variables 对象）；取消 emit `cancel`
  - i18n 键：`qs_cfg_title / qs_cfg_base_url / qs_cfg_base_url_custom / qs_cfg_api_key / qs_cfg_pick_key / qs_cfg_model / qs_cfg_confirm / qs_cfg_cancel`（zh/en 成对，qs_ 区追加）
  - 锚点备注：MaxBytesReader 常量在 quick_setup_handler.go :17
- [ ] **Step 2:** vitest（不受影响仍绿）+ `npm run build` 成功 + dist 还原。
- [ ] **Step 3: Commit** `git commit -m "新增：configure 变量表单组件（base_url 预设/api_key 选择/model）"`

---

### Task 13: QuickSetupModal 重构

**Files:** Modify: `app/website/src/components/QuickSetupModal.vue`（现 1175 行；v2 结构→v3 结构的手术，按下列清单执行）；Modify: `quickSetupApi.js` 无；i18n 补组合相关键

- [ ] **Step 1: 删除（以行为锚点，函数名为准）**
  - custom 概念全删：模板 :38/:54/:174/:211/:426/:455/:464/:544；脚本 :606(customSoftwares)/:618(合并)/:654/:829/:886/:927/:945-946/:953/:959/:967/:975/:1096/:1132；新建/删除 custom 的整套逻辑
  - render 链全删：`renderSelectedKey`(:867)、`scheduleOpenCodeRender`(:828)、`clearCurrentRender`(:819)、`createLatestRenderGuard` 引用、`setQuickSetupMode`(:926)、`modeState`/`quickSetupMode`(:614-615)、模板中接入模式分段控件与 key/model 选择区（opencode 的模型拉取 UI 全套）、`renderQuickSetup`/`getQuickSetupModels` 调用
  - opencode 专属渲染分支（isOpenCodeSelected 等）随 render 删除
- [ ] **Step 2: 新增状态与数据流**
  - `loadCatalog`(:840)：响应新增 `combos`/`presets` 存 ref；对选中 agent 计算 `agentCombos = computed(() => combos.filter(c => c.software === selectedSoftware))`；预选：`is_default` 优先，否则第一个
  - `activeCombo` ref + `activeFileCode` ref（文件 tabs 数据源 = `quickSetupFiles(activeCombo)` 即 software 声明 + combo.files 的 code 集合）
  - 渲染视图 computed：`renderedContent = renderComboContent(activeFile.content, activeCombo.variables)`；「编辑模板」切换 `templateEditing` ref（textarea 绑定模板原文，显式「保存组合」调 `updateCombo`）
  - `configureOpen` ref → `<QuickSetupConfigurePanel :variables="activeCombo.variables" :presets="catalogPresets" :api-keys="catalogKeys" @confirm="onConfigureConfirm" />`；`onConfigureConfirm` = `updateCombo(id, {variables})` → 响应 `{combo}` 就地替换 + 关闭弹窗（渲染视图即时刷新）
  - apply：`applyCombo()` = 组合每文件 `renderComboContent` → `applyQuickSetup({software, files:[{path: 声明 DefaultPath, content, format, kind}]})`（路径取 catalog 声明）；前置 `findUnresolvedPlaceholders` 任一命中 → 禁用 apply 按钮 + 提示；成功 → 既有 `applyResult`/`QuickSetupResultPanel` 流（:1035 区域改造，backups 字段不变）
  - **「插入变量」按钮（spec §6.2）**：模板编辑模式下 textarea 上方工具条，三个小按钮分别插入 `{{base_url}}`/`{{api_key}}`/`{{model}}` 到光标处（i18n 键 `qs_tpl_insert_var` 一个即可，zh「插入变量」/en "Insert variable"，按钮 title 用具体占位符文本）
  - 组合 tab 菜单（重命名/设默认/删除）+ 「+新建」三入口弹窗（blank 无需额外输入、copy 列现有组合、disk 确认提示「将读取磁盘当前配置」）→ 全部调 CRUD API → 用响应就地更新本地 combos
  - 切 agent：预选 default/第一个组合；切组合/文件 tab 有未保存模板编辑 → 提示
- [ ] **Step 3: 验证**：vitest 全绿 + build 成功 + dist 还原；代码走查清单——(a) 打开弹窗只见 installed agents 的组合；(b) 改 configure 确认后渲染视图立即变化且无需再请求；(c) 占位符未填时 apply 禁用；(d) apply 成功结果页复用正常；(e) StatePanel 页签（当前配置/恢复）不受影响；(f) 无任何 renderQuickSetup/getQuickSetupModels/custom 残留引用（grep 验证）
- [ ] **Step 4: Commit** `git commit -m "重构：快速配置弹窗改为配置组合管理（组合 tabs/渲染预览/configure/apply）"`

---

### Task 14: i18n 键清理与补齐

**Files:** Modify: `app/website/src/i18n/zh.js`、`en.js`

- [ ] **Step 1:** 删除失引用键（`qs_customBadge`、`qs_mode_*`、`qs_opencode*` 模型族、`qs_rendering*` 等——以 grep 组件引用为准，逐键确认零引用再删）；补齐 Task 11-13 新增键（combos/configure/模板编辑/三入口）zh/en 成对；程序化校验两文件键集合一致（node 脚本比对后删除）。
- [ ] **Step 2:** vitest + build + dist 还原。
- [ ] **Step 3: Commit** `git commit -m "测试：i18n 组合管理键增删与中英对齐"`

---

### Task 15: 全量回归与冒烟清单

- [ ] **Step 1:** `go test -count=1 ./app/http/... ./app/http/storage/`（flake 政策照旧）+ `cd app/website && npx vitest run` + `npm run build`（dist 还原）。
- [ ] **Step 2:** DoD 对照（spec 逐条）+ 产出用户手动冒烟清单（操作→预期两列）：组合创建三入口 / configure 填变量即时预览 / 占位符未填禁 apply / apply 后文件落盘且备份卡片出现 / 组合 A→B 切换不覆盖原始备份 / 恢复原始配置 / manifest 损坏拒绝 apply / pi 安装后出现在列表。
- [ ] **Step 3:** 有修补则 Commit，否则结束。

## 完成定义（DoD）

1. 四 agent（含 pi）按「CLI ∪ 目录」检测过滤展示；custom-* 概念前后端移除。
2. 组合 CRUD 全可用（三入口/重命名/删除/设默认），定义存 SQLite（唯一约束/默认互斥/50 上限），种子幂等。
3. configure 三变量表单生效：渲染视图即时预览；占位符未替换前端禁 apply、后端 400 双保险。
4. apply 逐字落盘，v2 备份三层保障原样（first-backup-wins/A→B 不覆盖/manifest 损坏拒绝/恢复含删新建）。
5. base_url 预设由 catalog `presets` 下发，前后端零硬编码。
6. render/models 端点与渲染链、merge 引擎删除；apply/snapshot/restore/备份契约既有测试全绿。
