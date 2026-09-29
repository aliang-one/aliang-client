# 快速配置 v2（安装检测 + 备份/恢复 + 智能合并）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 快速配置按本机已安装 agent 动态展示；应用前把用户原始配置落盘备份到 `~/.aliang/quick-setup/backups/` 并记入 manifest；写入改为「Render 阶段智能合并预览 + Apply 逐字落盘」；新增磁盘配置常驻查看与一键恢复。

**Architecture:** 在现有 quick_setup HTTP 域内增量演进（spec 方案一）：`quick_setup_service.go`（1485 行）同包拆分为 7 个职责文件；合并发生在 Render（读磁盘→深合并→预览），Apply 保持既有「先全量校验→原子写→失败回滚」契约只追加备份先行；新增 `config-state` / `restore` 两个端点。

**Tech Stack:** Go（net/http、encoding/json、BurntSushi/toml 仅校验）、Vue 3（现有 QuickSetupModal 两栏弹窗迭代）、vitest。

**Spec:** `docs/superpowers/specs/2026-09-22-quick-config-agent-detection-design.md`

**约定（全计划生效）：**
- Go 测试命令：`go test ./app/http/services/ -run '<Pattern>' -v`（仓库根执行）；handler 测试在 `./app/http/handlers/`。
- 前端测试：`cd app/website && npx vitest run src/utils/quickSetupState.test.js`。
- 提交标题用全中文「新增：/修复：/重构：」前缀（仓库约定）。**CLAUDE.md 政策：每次实际 `git commit` 前必须获得用户明确同意**——执行开始时先向用户确认「按计划逐任务提交」一次即可覆盖全程。
- 工作流：先按 `worktree 开发工作流` 记忆建分支 worktree（`stash -u → git worktree add ../alianggate-quick-config-v2 -b feature/quick-config-v2 → stash pop`），spec 与本 plan 两个未跟踪文件随 stash 进入 worktree。
- 所有 `~` 路径写入均经现有 `resolveQuickSetupApplyPath`/home 展开逻辑，禁止直接拼 `$HOME`。

---

## 文件结构（谁负责什么）

```
app/http/services/
  quick_setup_service.go    仅保留：QuickSetupService 结构体、Catalog/Render/Models/Apply 入口签名、常量、钩子变量
  quick_setup_catalog.go    新增：software 定义 quickSetupSoftwares、findQuickSetupSoftware、安装检测
  quick_setup_render.go     迁入：renderCodexFiles/renderClaudeSettingsEnv(新)/renderOpenCodeFiles 及 helper
  quick_setup_merge.go      新增：JSON 深合并、codex TOML 拼接器、managed_by_aliang 判定
  quick_setup_apply.go      迁入：Apply 主体 + validate/rollback
  quick_setup_backup.go     新增：备份目录 + manifest 读写 + restore
  quick_setup_snapshot.go   新增：ConfigState 服务
app/http/handlers/quick_setup_handler.go   +HandleConfigState +HandleRestore
app/http/models/quick_setup.go             +Installed/MergedFromDisk/Mode/备份与 state 类型
app/http/routes/routes.go                  +2 路由
app/website/src/
  components/QuickSetupModal.vue           过滤/模式切换/合并横幅/结果页
  components/QuickSetupStatePanel.vue      新增：当前配置查看 + 备份卡片 + 恢复
  services/quickSetupApi.js                +fetchConfigState +restoreConfig
  utils/quickSetupState.js(+test)          +installed 过滤/模式纯函数
  i18n/zh.js / i18n/en.js                  +qs_* 新键
```

---

### Task 0: worktree 与分支准备

**Files:** 无代码变更。

- [ ] **Step 1: 携带未跟踪文档建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git stash push -u -m "quick-config-v2 docs"
git worktree add ../alianggate-quick-config-v2 -b feature/quick-config-v2
cd ../alianggate-quick-config-v2 && git stash pop
```

Expected: `docs/superpowers/specs/2026-09-22-*.md` 与 `docs/superpowers/plans/2026-09-22-*.md` 出现在 worktree 中（未跟踪状态）。

- [ ] **Step 2: 向用户确认逐任务提交授权**（CLAUDE.md 政策），后续 Commit 步骤才可执行。

---

### Task 1: 服务文件机械拆分（纯移动，零行为变更）

**Files:**
- Modify: `app/http/services/quick_setup_service.go`（只缩减）
- Create: `app/http/services/quick_setup_catalog.go`、`quick_setup_render.go`、`quick_setup_apply.go`

- [ ] **Step 1: 按上文「文件结构」移动函数**（同包剪切粘贴，不改任何一行逻辑；每个新文件带 `package services` 与所需 import）。迁移映射：
  - `quick_setup_catalog.go`：`quickSetupSoftwares`(501-564)、`findQuickSetupSoftware`(566-574)、`softwareSupportsProvider`(576-583)、`quickSetupAllowedRoot`(1349-1368)、`resolveQuickSetupApplyPath`(1272-1319)、`quickSetupBuiltInPathAllowed`(1331-1347)、`quickSetupDeclaredFileForPath`(389-400)、`resolveQuickSetupTargetUser`(1370-1417)、`adjustQuickSetupOwnership`(1419-1448)、`quickSetupLooksMaskedAPIKey`(633-639)
  - `quick_setup_render.go`：`Render` 及其全部渲染 helper（`renderCodexFiles`、`renderClaudeCodeFiles`、`renderOpenCodeFiles`、`renderCodexConfigTOML`、`renderCodexAuthJSON`、`quickSetupProviderBaseURL`、`quickSetupDefaultModel`、provider label/npm/model 表、`fetchQuickSetupModels`、`quickSetupModelListBaseURL`、`resolveQuickSetupInferenceBaseURL`、`quickSetupBaseURL`、opencode 校验 `validateQuickSetupOpenCode`、`toQuickSetupAPIKeys`）
  - `quick_setup_apply.go`：`Apply`、`validateQuickSetupApplyFile`、`validateQuickSetupJSON`、`rollbackQuickSetupFiles`(483-499)
  - `quick_setup_service.go` 保留：常量(63-68)、钩子变量(72-78)、`QuickSetupService`、`Catalog`(84-116)、`quickSetupOpenCodeConfig` 等结构体、`quickSetupControlPlaneHost`/`quickSetupInferenceHost`。
- [ ] **Step 2: 验证零行为变更**

Run: `gofmt -l app/http/services/ ; go build ./... ; go test ./app/http/services/ ./app/http/handlers/`
Expected: gofmt 无输出；build 成功；全部测试 PASS（数量与拆分前一致）。

- [ ] **Step 3: Commit（已获授权）**

```bash
git add -A app/http/services/ && git commit -m "重构：quick_setup_service 按职责拆分为 catalog/render/apply 三文件（纯移动）"
```

---

### Task 2: 引入 TOML 校验依赖

> **执行备注（2026-09-22 实测）：** `github.com/BurntSushi/toml` 已作为间接依赖存在于 go.mod（v1.2.1）；代码尚未 import 时 `go mod tidy` 会将其移除——单独提交依赖是空操作。**本任务已并入 Task 6**：实现 TOML 拼接器时在第一条 import 前执行 `go get github.com/BurntSushi/toml@v1.4.0`（此后不再跑 tidy），依赖随功能代码一起提交。

- [x] **Step 1:**（见上方备注，已并入 Task 6）

---

### Task 3: 安装检测（catalog）

**Files:**
- Modify: `app/http/models/quick_setup.go`（QuickSetupSoftware 加字段）
- Create: `app/http/services/quick_setup_catalog.go`（追加检测）
- Test: `app/http/services/quick_setup_catalog_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectQuickSetupInstalled(t *testing.T) {
	home := t.TempDir()
	stub := func(name string) (string, error) { return "", os.ErrNotExist }
	t.Run("cli hit", func(t *testing.T) {
		orig := quickSetupLookPathCLIFn
		quickSetupLookPathCLIFn = func(name string) (string, error) {
			if name == "codex" { return "/usr/local/bin/codex", nil }
			return "", os.ErrNotExist
		}
		defer func() { quickSetupLookPathCLIFn = orig }()
		if !detectQuickSetupInstalled("codex", home) { t.Fatal("codex should be installed via cli") }
	})
	t.Run("dir hit", func(t *testing.T) {
		orig := quickSetupLookPathCLIFn
		quickSetupLookPathCLIFn = stub
		defer func() { quickSetupLookPathCLIFn = orig }()
		if detectQuickSetupInstalled("codex", home) { t.Fatal("must not detect without dir") }
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil { t.Fatal(err) }
		if !detectQuickSetupInstalled("codex", home) { t.Fatal("codex should be installed via dir") }
	})
	t.Run("neither", func(t *testing.T) {
		orig := quickSetupLookPathCLIFn
		quickSetupLookPathCLIFn = stub
		defer func() { quickSetupLookPathCLIFn = orig }()
		if detectQuickSetupInstalled("opencode", home) { t.Fatal("opencode must not be detected") }
	})
	t.Run("empty home", func(t *testing.T) {
		if detectQuickSetupInstalled("codex", "") { t.Fatal("empty home must not detect") }
	})
}

func TestCatalogMarksInstalled(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil { t.Fatal(err) }
	// 注意：Catalog() 返回 map[string]interface{}（"status"/"data" 键）。keys stub 与
	// 全局 config 注入务必镜像现有测试 quick_setup_service_test.go:24-30 与 :483-515 的写法。
	origKeys, origHome, origCLI := quickSetupGetAPIKeysFn, quickSetupDetectionHomeFn, quickSetupLookPathCLIFn
	quickSetupGetAPIKeysFn = func() ([]auth.UserAPIKey, error) { return nil, nil } // 签名以现有测试 stub 为准
	quickSetupDetectionHomeFn = func() string { return home }
	quickSetupLookPathCLIFn = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { quickSetupGetAPIKeysFn, quickSetupDetectionHomeFn, quickSetupLookPathCLIFn = origKeys, origHome, origCLI }()

	catalog := (&QuickSetupService{}).Catalog()
	data, _ := catalog["data"].(models.QuickSetupCatalogResponse)
	found := map[string]bool{}
	for _, s := range data.Softwares {
		found[s.Code] = s.Installed
	}
	if !found["claude-code"] { t.Fatal("claude-code should be marked installed") }
	if found["codex"] || found["opencode"] { t.Fatal("codex/opencode must not be marked installed") }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/services/ -run 'TestDetectQuickSetupInstalled|TestCatalogMarksInstalled' -v`
Expected: FAIL（`quickSetupLookPathCLIFn`/`detectQuickSetupInstalled` 未定义）。

- [ ] **Step 3: 实现**

models（`QuickSetupSoftware` 追加）：
```go
	Installed bool `json:"installed"`
```

catalog 追加：
```go
// quickSetupLookPathCLIFn 是 lookPathCLI 的钩子变量，供单测注入。
var quickSetupLookPathCLIFn = lookPathCLI

// quickSetupDetectionHomeFn 返回安装检测用的家目录（root 场景解析桌面用户）。
var quickSetupDetectionHomeFn = func() string {
	if h, err := runtimepath.EffectiveAgentHome(); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			return h
		}
	}
	h, _ := runtimepath.UserHomeDir()
	return strings.TrimSpace(h)
}

type quickSetupDetectionRule struct {
	cliNames []string
	dirs     []string // 相对家目录，正斜杠书写
}

var quickSetupDetectionRules = map[string]quickSetupDetectionRule{
	"claude-code": {cliNames: []string{"claude"}, dirs: []string{".claude"}},
	"codex":       {cliNames: []string{"codex"}, dirs: []string{".codex"}},
	"opencode":    {cliNames: []string{"opencode"}, dirs: []string{".config/opencode", ".local/share/opencode", ".opencode"}},
}

// detectQuickSetupInstalled：CLI 二进制或配置目录任一命中即视为已安装（spec §5）。
func detectQuickSetupInstalled(softwareCode, homeDir string) bool {
	rule, ok := quickSetupDetectionRules[softwareCode]
	if !ok || strings.TrimSpace(homeDir) == "" {
		return false
	}
	for _, name := range rule.cliNames {
		if _, err := quickSetupLookPathCLIFn(name); err == nil {
			return true
		}
	}
	for _, dir := range rule.dirs {
		if info, err := os.Stat(filepath.Join(homeDir, filepath.FromSlash(dir))); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
```

`Catalog()`（quick_setup_service.go）在返回前填充：
```go
	softwares := quickSetupSoftwares()
	homeDir := quickSetupDetectionHomeFn()
	for i := range softwares {
		softwares[i].Installed = detectQuickSetupInstalled(softwares[i].Code, homeDir)
	}
```
（替换原 `Softwares: quickSetupSoftwares()` 一行；import `aliang.one/nursorgate/internal/runtimepath`。）

- [ ] **Step 4: 跑测试确认通过**（同 Step 2 命令，Expected: PASS）+ 全量 `go test ./app/http/services/ ./app/http/handlers/` 保持绿。

- [ ] **Step 5: Commit** `git commit -m "新增：快速配置 catalog 安装检测（CLI 或目录任一命中）"`

---

### Task 4: 备份模块（manifest + 落盘 + restore）

**Files:**
- Create: `app/http/services/quick_setup_backup.go`
- Test: `app/http/services/quick_setup_backup_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBackupFixture(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil { t.Fatal(err) }
}

func TestBackupQuickSetupFiles_FirstBackupWins(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")

	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/config.toml"), content: "aliang v1"}}
	if _, err := backupQuickSetupFiles(home, "codex", files); err != nil { t.Fatal(err) }

	// 第二次 apply 前磁盘已是我们的 v1 —— 不得覆盖原始备份
	writeBackupFixture(t, home, ".codex/config.toml", "aliang v1")
	files[0].content = "aliang v2"
	infos, err := backupQuickSetupFiles(home, "codex", files)
	if err != nil { t.Fatal(err) }
	raw, _ := os.ReadFile(filepath.Join(home, ".aliang/quick-setup/backups/codex/config.toml"))
	if string(raw) != "user original" { t.Fatalf("original backup clobbered: %q", raw) }
	if len(infos) != 1 || infos[0].ExistedBefore != true {
		t.Fatalf("unexpected infos %+v", infos)
	}
}

func TestBackupQuickSetupFiles_NewFileRecorded(t *testing.T) {
	home := t.TempDir()
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/auth.json"), content: "{}"}}
	infos, err := backupQuickSetupFiles(home, "codex", files)
	if err != nil { t.Fatal(err) }
	if len(infos) != 1 || infos[0].ExistedBefore { t.Fatalf("unexpected %+v", infos) }
	if _, err := os.Stat(filepath.Join(home, ".aliang/quick-setup/backups/codex/auth.json")); !os.IsNotExist(err) {
		t.Fatal("no backup file should exist for created file")
	}
	m, err := loadQuickSetupManifest(home)
	if err != nil { t.Fatal(err) }
	if len(m.Backups) != 1 || m.Backups[0].Kind != "original" || m.Backups[0].ExistedBefore {
		t.Fatalf("manifest %+v", m.Backups)
	}
}

func TestBackupQuickSetupFiles_CorruptManifestBlocks(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".aliang/quick-setup/backups/manifest.json", "{broken")
	writeBackupFixture(t, home, ".codex/config.toml", "x")
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/config.toml"), content: "y"}}
	if _, err := backupQuickSetupFiles(home, "codex", files); err == nil {
		t.Fatal("corrupt manifest must block backup")
	}
}

func TestRestoreQuickSetupSoftware(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")
	files := []quickSetupPreparedFile{
		{path: filepath.Join(home, ".codex/config.toml"), content: "aliang"},
		{path: filepath.Join(home, ".codex/auth.json"), content: "{}"},
	}
	if _, err := backupQuickSetupFiles(home, "codex", files); err != nil { t.Fatal(err) }

	res, err := restoreQuickSetupSoftware(home, "codex")
	if err != nil { t.Fatal(err) }
	raw, _ := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if string(raw) != "user original" { t.Fatalf("restore failed: %q", raw) }
	if _, err := os.Stat(filepath.Join(home, ".codex/auth.json")); !os.IsNotExist(err) {
		t.Fatal("created file must be deleted on restore")
	}
	if len(res.Restored) != 1 || len(res.Deleted) != 1 { t.Fatalf("unexpected %+v", res) }
	m, _ := loadQuickSetupManifest(home)
	if len(m.Backups) != 0 { t.Fatalf("manifest entries must be cleared, got %+v", m.Backups) }
}
```

- [ ] **Step 2: 确认失败**：`go test ./app/http/services/ -run TestBackup -v`、`-run TestRestoreQuickSetupSoftware` → FAIL（函数未定义）。

- [ ] **Step 3: 实现 `quick_setup_backup.go`**

```go
package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

const quickSetupManifestKindOriginal = "original"

type quickSetupManifestEntry struct {
	Software      string `json:"software"`
	FileCode      string `json:"file_code"`
	OriginalPath  string `json:"original_path"`  // "~/..." 形式
	BackupPath    string `json:"backup_path"`    // "~/..." 形式；existed_before=false 时为空
	BackedUpAt    string `json:"backed_up_at"`   // RFC3339
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Mode          uint32 `json:"mode"`
	ExistedBefore bool   `json:"existed_before"`
	Kind          string `json:"kind"`
}

type quickSetupManifest struct {
	Version int                      `json:"version"`
	Backups []quickSetupManifestEntry `json:"backups"`
}

func quickSetupManifestPath(homeDir string) string {
	return filepath.Join(homeDir, ".aliang", "quick-setup", "backups", "manifest.json")
}

func quickSetupContractPath(homeDir, absPath string) string {
	rel, err := filepath.Rel(homeDir, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return absPath
	}
	return "~/" + filepath.ToSlash(rel)
}

func quickSetupExpandPath(homeDir, contractPath string) string {
	if contractPath == "~" || strings.HasPrefix(contractPath, "~/") {
		return filepath.Join(homeDir, filepath.FromSlash(strings.TrimPrefix(contractPath, "~")))
	}
	return contractPath
}

// loadQuickSetupManifest：不存在返回空 manifest；存在但解析失败返回错误（fail-safe，
// 上游必须拒绝 apply，防止把我们的配置当「原始配置」重新备份，spec §6.3-5）。
func loadQuickSetupManifest(homeDir string) (quickSetupManifest, error) {
	m := quickSetupManifest{Version: 1}
	raw, err := os.ReadFile(quickSetupManifestPath(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return quickSetupManifest{Version: 1}, fmt.Errorf("quick setup backup manifest is corrupt (%s): %w", quickSetupManifestPath(homeDir), err)
	}
	return m, nil
}

func saveQuickSetupManifest(homeDir string, m quickSetupManifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(quickSetupManifestPath(homeDir), string(raw)+"\n")
}

func quickSetupManifestHasEntry(m quickSetupManifest, originalPath string) bool {
	for _, e := range m.Backups {
		if e.OriginalPath == originalPath {
			return true
		}
	}
	return false
}

// backupQuickSetupFiles 在写配置前落盘原始内容（first-backup-wins，spec §6.3-1）。
// 任何失败都必须让调用方在写配置前中止。
func backupQuickSetupFiles(homeDir string, software string, files []quickSetupPreparedFile) ([]models.QuickSetupBackupInfo, error) {
	m, err := loadQuickSetupManifest(homeDir)
	if err != nil {
		return nil, err
	}
	now := time.Now().Format(time.RFC3339)
	infos := make([]models.QuickSetupBackupInfo, 0, len(files))
	dirty := false
	for _, file := range files {
		contract := quickSetupContractPath(homeDir, file.path)
		existing, readErr := os.ReadFile(file.path)
		switch {
		case readErr == nil:
			if quickSetupManifestHasEntry(m, contract) {
				break // 已有原始备份，不覆盖
			}
			backupRel := filepath.Join(".aliang", "quick-setup", "backups", software, filepath.Base(file.path))
			backupAbs := filepath.Join(homeDir, backupRel)
			sum := sha256.Sum256(existing)
			entry := quickSetupManifestEntry{
				Software: software, FileCode: file.code,
				OriginalPath: contract, BackupPath: quickSetupContractPath(homeDir, backupAbs),
				BackedUpAt: now, SHA256: hex.EncodeToString(sum[:]),
				Size: int64(len(existing)), ExistedBefore: true, Kind: quickSetupManifestKindOriginal,
			}
			if st, statErr := os.Stat(file.path); statErr == nil {
				entry.Mode = uint32(st.Mode().Perm())
			}
			if err := writeConfigFile(backupAbs, string(existing)); err != nil {
				return nil, fmt.Errorf("backup %s failed: %w", contract, err)
			}
			m.Backups = append(m.Backups, entry)
			dirty = true
			infos = append(infos, models.QuickSetupBackupInfo{OriginalPath: contract, BackupPath: entry.BackupPath, ExistedBefore: true})
		case errors.Is(readErr, os.ErrNotExist):
			if quickSetupManifestHasEntry(m, contract) {
				break
			}
			m.Backups = append(m.Backups, quickSetupManifestEntry{
				Software: software, FileCode: file.code, OriginalPath: contract,
				BackedUpAt: now, ExistedBefore: false, Kind: quickSetupManifestKindOriginal,
			})
			dirty = true
			infos = append(infos, models.QuickSetupBackupInfo{OriginalPath: contract, ExistedBefore: false})
		default:
			return nil, fmt.Errorf("backup %s failed: %w", contract, readErr)
		}
	}
	if dirty {
		if err := saveQuickSetupManifest(homeDir, m); err != nil {
			return nil, fmt.Errorf("save backup manifest failed: %w", err)
		}
	}
	return infos, nil
}
```

注意：`quickSetupPreparedFile` 需要补 `code` 字段（在 Task 9 Step 3 的结构体改造中落地；先在本文件按 `file.code` 引用，Task 9 一并补齐）。若先行实现本 Task，可临时只赋 `Software/OriginalPath`，Task 9 时补齐 `FileCode`。

restore（同文件追加）：

```go
// restoreQuickSetupSoftware 按 software 整体还原（spec §6.3-3）：
// existed_before=true 复制备份回原路径；false 删除文件；成功条目从 manifest 清除并删除备份文件。
func restoreQuickSetupSoftware(homeDir, software string) (models.QuickSetupRestoreResponse, error) {
	resp := models.QuickSetupRestoreResponse{}
	m, err := loadQuickSetupManifest(homeDir)
	if err != nil {
		return resp, err
	}
	kept := m.Backups[:0]
	for _, entry := range m.Backups {
		if entry.Software != software {
			kept = append(kept, entry)
			continue
		}
		originalAbs := quickSetupExpandPath(homeDir, entry.OriginalPath)
		switch {
		case entry.ExistedBefore:
			raw, readErr := os.ReadFile(quickSetupExpandPath(homeDir, entry.BackupPath))
			if readErr != nil {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: readErr.Error()})
				kept = append(kept, entry)
				continue
			}
			mode := fs.FileMode(entry.Mode)
			if mode == 0 {
				mode = 0o600
			}
			if err := writeConfigFile(originalAbs, string(raw)); err != nil {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: err.Error()})
				kept = append(kept, entry)
				continue
			}
			_ = os.Chmod(originalAbs, mode)
			resp.Restored = append(resp.Restored, entry.OriginalPath)
		default:
			if err := os.Remove(originalAbs); err != nil && !errors.Is(err, os.ErrNotExist) {
				resp.Failed = append(resp.Failed, models.QuickSetupRestoreFailure{Path: entry.OriginalPath, Error: err.Error()})
				kept = append(kept, entry)
				continue
			}
			resp.Deleted = append(resp.Deleted, entry.OriginalPath)
		}
		if entry.BackupPath != "" {
			_ = os.Remove(quickSetupExpandPath(homeDir, entry.BackupPath))
		}
	}
	m.Backups = kept
	if err := saveQuickSetupManifest(homeDir, m); err != nil {
		return resp, err
	}
	return resp, nil
}
```

（import 需补 `"io/fs"`。）

models（`quick_setup.go` 追加）：
```go
type QuickSetupBackupInfo struct {
	OriginalPath  string `json:"original_path"`
	BackupPath    string `json:"backup_path,omitempty"`
	ExistedBefore bool   `json:"existed_before"`
}

type QuickSetupRestoreFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type QuickSetupRestoreResponse struct {
	Restored []string                    `json:"restored"`
	Deleted  []string                    `json:"deleted"`
	Failed   []QuickSetupRestoreFailure  `json:"failed"`
}
```

- [ ] **Step 4: 跑测试通过**（Step 2 命令 → PASS；全量回归绿）。
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置原始配置落盘备份与一键恢复（manifest first-backup-wins）"`

---

### Task 5: JSON 深合并引擎

**Files:**
- Create: `app/http/services/quick_setup_merge.go`
- Test: `app/http/services/quick_setup_merge_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil { t.Fatal(err) }
	return v
}

func TestMergeQuickSetupJSON(t *testing.T) {
	existing := mustJSON(t, `{"theme":"dark","mcp":{"fs":{"command":"x"}},"provider":{"old":{"npm":"@g/old"}}}`)
	incoming := mustJSON(t, `{"$schema":"https://opencode.ai/config.json","model":"aliang/main","provider":{"aliang":{"npm":"@g/aliang","options":{"baseURL":"https://api.aliang.one/v1"}}}}`)

	merged, ok := mergeQuickSetupJSONObjects(existing, incoming)
	if !ok { t.Fatal("merge should succeed") }
	out, _ := json.Marshal(merged)
	s := string(out)
	for _, want := range []string{`"theme":"dark"`, `"fs"`, `"old"`, `"aliang/main"`, `"baseURL":"https://api.aliang.one/v1"`} {
		if !strings.Contains(s, want) { t.Fatalf("missing %s in %s", want, s) }
	}
}

func TestMergeQuickSetupJSONPreservesAuthTokens(t *testing.T) {
	// codex auth.json：只动 OPENAI_API_KEY，保住 ChatGPT 登录态（spec §7）
	existing := mustJSON(t, `{"OPENAI_API_KEY":null,"tokens":{"access_token":"at","account_id":"acc"},"last_refresh":"2026-01-01"}`)
	incoming := mustJSON(t, `{"OPENAI_API_KEY":"sk-aliang"}`)
	merged, ok := mergeQuickSetupJSONObjects(existing, incoming)
	if !ok { t.Fatal("merge should succeed") }
	out, _ := json.Marshal(merged)
	if !strings.Contains(string(out), `"access_token":"at"`) { t.Fatalf("tokens lost: %s", out) }
}

func TestMergeQuickSetupJSONBrokenExisting(t *testing.T) {
	if _, ok := mergeQuickSetupJSONObjects(nil, mustJSON(t, `{"a":1}`)); !ok { t.Fatal("nil existing should merge cleanly") }
}
```

- [ ] **Step 2: 确认失败**（`mergeQuickSetupJSONObjects` 未定义）。
- [ ] **Step 3: 实现（quick_setup_merge.go）**

```go
package services

import "encoding/json"

// mergeQuickSetupJSONObjects 递归深合并 incoming 到 existing（incoming 的键胜出，
// existing 其余字段全保留）。两参均可为 nil。existing 必须：由调用方保证已成功解析。
// 返回合并后的 map；ok=false 仅在 Marshal 失败等内部错误时出现。
func mergeQuickSetupJSONObjects(existing, incoming map[string]interface{}) (map[string]interface{}, bool) {
	if existing == nil {
		existing = map[string]interface{}{}
	}
	mergeQuickSetupJSONInto(existing, incoming)
	return existing, true
}

func mergeQuickSetupJSONInto(dst, src map[string]interface{}) {
	for key, value := range src {
		srcObj, srcIsObj := value.(map[string]interface{})
		dstObj, dstIsObj := dst[key].(map[string]interface{})
		if srcIsObj && dstIsObj {
			mergeQuickSetupJSONInto(dstObj, srcObj)
			continue
		}
		dst[key] = value
	}
}
```

- [ ] **Step 4: 测试通过 + 全量回归。**
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置 JSON 深合并引擎（保留用户其余字段）"`

---

### Task 6: codex TOML 拼接器

**Files:**
- Modify: `app/http/services/quick_setup_merge.go`
- Test: `app/http/services/quick_setup_merge_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestMergeCodexTOML(t *testing.T) {
	existing := "# my codex config\nmodel = \"gpt-4o\"\nmodel_provider = \"openai\"\n\n[model_providers.openai]\nname = \"OpenAI\"\nbase_url = \"https://api.openai.com/v1\"\nwire_api = \"responses\"\n\n# user section\n[mcp_servers.fs]\ncommand = \"uvx\"\n"
	got, err := mergeCodexTOML(existing, "gpt-5.4", "https://api.aliang.one/v1")
	if err != nil { t.Fatal(err) }
	for _, want := range []string{
		`model = "gpt-5.4"`, `model_provider = "aliang"`,
		"[model_providers.aliang]", `base_url = "https://api.aliang.one/v1"`,
		"# my codex config",           // 顶部注释保留
		"[mcp_servers.fs]",            // 用户其他段保留
		"command = \"uvx\"",
	} {
		if !strings.Contains(got, want) { t.Fatalf("missing %q in:\n%s", want, got) }
	}
	// 用户自建的其他 provider 表必须原样保留（spec §7.2 惰性残留可接受）；
	// 只是 model_provider 改指 aliang 后不再被引用
	if !strings.Contains(got, "[model_providers.openai]") { t.Fatal("user's own table must be preserved") }
	if !strings.Contains(got, `model_provider = "aliang"`) { t.Fatal("provider switch missing") }
}

func TestMergeCodexTOML_AppendsWhenNoTables(t *testing.T) {
	got, err := mergeCodexTOML("", "gpt-5.4", "http://127.0.0.1:56432/v1")
	if err != nil { t.Fatal(err) }
	if !strings.Contains(got, "[model_providers.aliang]") { t.Fatal("section missing") }
}

func TestMergeCodexTOML_MultilineStringSafe(t *testing.T) {
	existing := "instructions = \"\"\"\n[model_providers.aliang]\nnot = \"a table\"\n\"\"\"\n"
	got, err := mergeCodexTOML(existing, "m", "http://x/v1")
	if err != nil { t.Fatal(err) }
	// 多行字符串内的伪 table 行不得被当作我们的段
	if !strings.Contains(got, "not = \"a table\"") { t.Fatalf("multiline content damaged:\n%s", got) }
}

func TestMergeCodexTOML_OutputParses(t *testing.T) {
	existing := "model = \"x\"\n[model_providers.openai]\nname=\"o\"\n[[array_of_tables]]\nkey=\"v\"\n"
	got, err := mergeCodexTOML(existing, "m", "http://x/v1")
	if err != nil { t.Fatal(err) }
	var v map[string]interface{}
	if err := toml.Unmarshal([]byte(got), &v); err != nil { t.Fatalf("output not valid TOML: %v\n%s", err, got) }
}
```

- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现（追加到 quick_setup_merge.go）**

设计：行级状态机。`tomlSpliceContext` 逐行扫描 existing：(a) 跟踪是否处于 `"""`/`'''` 多行字符串内（每行统计成对引号数奇偶切换）；(b) 非字符串内的 `^\[table\]$` 行切换当前表；(c) 顶层（当前表为空）`model =`/`model_provider =` 行改写值；(d) `[model_providers.aliang]` 表整体替换为新生成段；其余行原样保留。`model`/`model_provider` 缺失时插入到首个表头之前（无表头则文件尾）。最后 `toml.Unmarshal` 校验输出。

```go
const quickSetupCodexProviderID = "aliang"

// mergeCodexTOML 把我们管理的顶层键与 [model_providers.aliang] 段合并进 existing，
// 其余字节（注释/格式/其他表）原样保留（spec §7）。输出必须能通过 toml.Unmarshal。
func mergeCodexTOML(existing, model, baseURL string) (string, error) {
	section := buildCodexAliangSection(model, baseURL)
	lines := splitLines(existing)
	out := make([]string, 0, len(lines)+len(section)+4)
	inBlock := false
	currentTable := ""
	wroteTopKeys := false
	replacedSection := false
	pendingSection := true // 我们段尚未写入
	insertTopAt := -1      // 顶层键需要插入的位置（首个表头前）

	flushSection := func(out *[]string) {
		if pendingSection && !replacedSection {
			*out = append(*out, "")
			*out = append(*out, section...)
			pendingSection = false
		}
	}

	for _, line := range lines {
		if tomlToggleBlock(line) {
			inBlock = !inBlock
			out = append(out, line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !inBlock && strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && !strings.HasPrefix(trimmed, "[[") {
			table := strings.TrimSpace(strings.Trim(trimmed, "[]"))
			if insertTopAt < 0 {
				insertTopAt = len(out) // 首个表头位置：顶层键插到这之前
			}
			if table == "model_providers."+quickSetupCodexProviderID {
				out = append(out, section...) // 替换我们的段
				pendingSection = false
				replacedSection = true
				currentTable = table
				continue
			}
			currentTable = table
			out = append(out, line)
			continue
		}
		if !inBlock && currentTable == "" && isTOMLTopKey(trimmed, "model", "model_provider") {
			// 顶层键：跳过原行，值统一在文件头部重写
			continue
		}
		out = append(out, line)
	}
	if insertTopAt < 0 {
		insertTopAt = len(out)
	}
	top := []string{fmt.Sprintf("model = %q", model), fmt.Sprintf("model_provider = %q", quickSetupCodexProviderID)}
	out = append(out[:insertTopAt], append(top, out[insertTopAt:]...)...)
	flushSection(&out)
	merged := strings.TrimLeft(strings.Join(out, "\n"), "\n")
	var check map[string]interface{}
	if err := toml.Unmarshal([]byte(merged), &check); err != nil {
		return "", fmt.Errorf("merged codex config is not valid TOML: %w", err)
	}
	return merged, nil
}
```

配套 helper（同文件）：`splitLines`（strings.Split "\n"，保留空行）、`tomlToggleBlock`（统计该行 `"""` 与 `'''` 出现次数之和的奇偶，奇数返回 true）、`isTOMLTopKey`（`^key\s*=` 前缀匹配）、`buildCodexAliangSection`（返回 []string：

```toml
[model_providers.aliang]
name = "Aliang Gateway"
base_url = "<baseURL>"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
```

）。注意实现细节以测试为准：数组表 `[[...]]` 行必须原样保留（上面的分支已排除 `[[` 前缀）。若测试 `TestMergeCodexTOML` 中 stale openai 表断言与实现冲突——用户自建的 `[model_providers.openai]` 段**保留**（我们只替换自己的 `aliang` 段与顶层两个键；`model_provider = "aliang"` 使旧 openai 表不再被引用，属可接受的惰性残留，spec §7.2）。

- [ ] **Step 4: 测试通过 + 全量回归。**
- [ ] **Step 5: Commit** `git commit -m "新增：codex config.toml 行级拼接器（保留用户注释与其他段）"`

---

### Task 7: claude settings.json 渲染器 + software 定义切换

**Files:**
- Modify: `app/http/services/quick_setup_catalog.go`（claude-code Files 定义 + `quickSetupAllowedRoot` 的 claude-code 分支）、`quick_setup_render.go`
- Test: `app/http/services/quick_setup_render_test.go`（新文件）+ 调整 `quick_setup_service_test.go` 中 env.sh 相关断言

- [ ] **Step 1: 写失败测试**

```go
func TestRenderClaudeSettingsEnv(t *testing.T) {
	content := renderClaudeSettingsEnv("sk-aliang", "claude-sonnet-4-5-20250929", "https://api.aliang.one")
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil { t.Fatal(err) }
	if parsed.Env["ANTHROPIC_BASE_URL"] != "https://api.aliang.one" { t.Fatalf("base url: %v", parsed.Env) }
	if parsed.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-aliang" { t.Fatal("auth token missing") }
	if _, has := parsed.Env["ANTHROPIC_API_KEY"]; has { t.Fatal("must use AUTH_TOKEN, not API_KEY") }
	if parsed.Env["ANTHROPIC_MODEL"] != "claude-sonnet-4-5-20250929" { t.Fatal("model missing") }
}

func TestQuickSetupSoftwares_ClaudeUsesSettingsJSON(t *testing.T) {
	sw, ok := findQuickSetupSoftware("claude-code")
	if !ok { t.Fatal("claude-code missing") }
	if len(sw.Files) != 1 || sw.Files[0].DefaultPath != "~/.claude/settings.json" || sw.Files[0].Format != "json" {
		t.Fatalf("unexpected files %+v", sw.Files)
	}
	if got := quickSetupAllowedRoot("claude-code", "/home/u"); got != filepath.Join("/home/u", ".claude") {
		t.Fatalf("allowed root: %s", got)
	}
}
```

- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现**
  1. catalog 中 claude-code 定义替换（code `command`→`settings`，path `~/.claude/settings.json`，format `json`，description 说明 env 块合并）。
  2. `quickSetupAllowedRoot`：claude-code 分支返回 `~/.claude`（原 `~/.claude-code`）。
  3. `quick_setup_render.go`：新增 `renderClaudeSettingsEnv(apiKey, model, baseURLNoV1 string) string`——输出 `{"env":{"ANTHROPIC_BASE_URL":...,"ANTHROPIC_AUTH_TOKEN":...,"ANTHROPIC_MODEL":...}}`（MarshalIndent）。删除 `renderClaudeCodeFiles` 的 env.sh 逻辑，改为返回单文件 `settings`，内容 = `mergeClaudeSettingsJSON(diskContent, envPayload)`（Task 9 接入 disk 前，先直接返回 payload 本身，保证本 Task 独立可测）。同步删除已无引用的 env.sh helper。
  4. 更新 `quick_setup_service_test.go` 中引用 `env.sh`/`~/.claude-code` 的既有断言为 settings.json 契约（这是 spec 明确的行为变更，测试随契约更新）。
- [ ] **Step 4: 全量测试绿**（`./app/http/services/ ./app/http/handlers/`）。
- [ ] **Step 5: Commit** `git commit -m "重构：claude-code 快速配置从 env.sh 切换为 ~/.claude/settings.json env 块（ANTHROPIC_AUTH_TOKEN）"`

---

### Task 8: 模式（local/public）→ baseURL

**Files:**
- Modify: `app/http/models/quick_setup.go`（RenderRequest 加 Mode）、`quick_setup_render.go`
- Test: `quick_setup_render_test.go` 追加

- [ ] **Step 1: 写失败测试**

```go
func TestQuickSetupModeRoot(t *testing.T) {
	if got := quickSetupModeRoot("local", "https://backend.aliang.one"); got != "http://127.0.0.1:56432" {
		t.Fatalf("local root: %s", got)
	}
	if got := quickSetupModeRoot("public", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("public root: %s", got)
	}
	if got := quickSetupModeRoot("", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("default must be public: %s", got)
	}
	if got := quickSetupModeRoot("bogus", "https://backend.aliang.one"); got != "https://api.aliang.one" {
		t.Fatalf("unknown mode falls back to public: %s", got)
	}
}

func TestQuickSetupV1SuffixPerSoftware(t *testing.T) {
	// codex/opencode: root + /v1；claude: root 原样（行为变更，spec §7.1）
	if got := quickSetupProviderBaseURL("anthropic", "http://127.0.0.1:56432"); got != "http://127.0.0.1:56432/v1" {
		t.Fatalf("codex/opencode: %s", got)
	}
}
```

- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现**

```go
// quickSetupModeRoot 把接入模式换算成 host 根（spec §7.1）：
// local → 本地推理代理（引用 defaults 常量，禁止硬编码）；public/未知 → 推理面域名。
func quickSetupModeRoot(mode, apiRoot string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "local") {
		return "http://" + config.DefaultHTTPProxyAddr
	}
	return resolveQuickSetupInferenceBaseURL(apiRoot)
}
```

- `QuickSetupRenderRequest` 加 `Mode string \`json:"mode,omitempty"\``；`Render()` 解析 mode 并把 `quickSetupModeRoot(mode, apiRoot)` 作为各渲染器的 root：codex/opencode 经 `quickSetupProviderBaseURL(provider, modeRoot)`（自动 +`/v1`）；claude 用 `modeRoot` 原样。import `aliang.one/nursorgate/processor/config`。
- `QuickSetupPreviewFile` 加 `MergedFromDisk bool \`json:"merged_from_disk,omitempty"\``（本 Task 先置 false，Task 9 填充）。
- 既有 render 测试中 baseURL 断言补 mode 缺省 = public 的新契约（原断言期望值不变，因为 public 即现行为）。

- [ ] **Step 4: 全量测试绿。**
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置接入模式 local/public（本地 56432 代理 / 公网推理域名）"`

---

### Task 9: Render 读磁盘智能合并

**Files:**
- Modify: `app/http/services/quick_setup_render.go`、`quick_setup_service.go`（prepared file 结构）、`quick_setup_merge.go`
- Test: `quick_setup_render_test.go` 追加

- [ ] **Step 1: 写失败测试**

```go
func TestRenderMergesFromDisk(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "# user\nmodel = \"gpt-4o\"\n[mcp_servers.fs]\ncommand=\"uvx\"\n")
	origHome, origAuth := quickSetupDetectionHomeFn, quickSetupAuthorizationHeaderFn
	quickSetupDetectionHomeFn = func() string { return home }
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test" }
	defer func() { quickSetupDetectionHomeFn, quickSetupAuthorizationHeaderFn = origHome, origAuth }()

	// Render 走 targetUser：钩子注入
	origUser := quickSetupTargetUserFn
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	defer func() { quickSetupTargetUserFn = origUser }()

	resp, err := (&QuickSetupService{}).Render(models.QuickSetupRenderRequest{
		Software: "codex", KeyIDs: []int64{1}, Mode: "public",
	})
	// 注意：Render 还依赖 quickSetupGetAPIKeysFn 与全局 config（api_server），
	// stub 注入务必镜像现有测试 quick_setup_service_test.go:24-30 / :483-515 的写法，
	// 否则 Render 在走到合并逻辑前就返回 unauthenticated/failed。
	if err != nil { t.Fatal(err) }
	if len(resp.Variants) == 0 { t.Fatal("no variants") }
	cfg := resp.Variants[0].Files[0]
	if !cfg.MergedFromDisk { t.Fatal("config.toml should be merged from disk") }
	if !strings.Contains(cfg.Content, "# user") || !strings.Contains(cfg.Content, "mcp_servers.fs") {
		t.Fatalf("user content lost:\n%s", cfg.Content)
	}
	// auth.json 无磁盘文件 → merged_from_disk=false
	auth := resp.Variants[0].Files[1]
	if auth.MergedFromDisk { t.Fatal("auth.json has no disk file") }
}

func TestRenderFallsBackOnBrokenDiskJSON(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".config/opencode/opencode.json", "{broken")
	// …钩子注入同上，software=opencode…
	// 断言：不报错；文件内容为模板兜底；MergedFromDisk=false；notes 含合并警告
}
```

（opencode 用例按 codex 用例的钩子模式补全。）

- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现**
  1. `quickSetupPreparedFile` 增加 `code string` 字段；构造处（`renderCodexFiles`/`renderClaudeSettings`/`renderOpenCodeFiles` 内 QuickSetupPreviewFile→prepared 的映射点，以及 Apply 的 prepared 构造）一并赋值。
  2. Render 流程：解析 targetUser（`quickSetupTargetUserFn`）→ 对每个待预览文件：`resolveQuickSetupApplyPath` 得绝对路径 → `os.ReadFile`：
     - 读取失败/超 1MB → 模板兜底，`MergedFromDisk=false`，notes 追加 `"Could not read the existing file; showing template instead."`
     - json 格式：`json.Unmarshal` existing 失败 → 同上兜底；成功 → `mergeQuickSetupJSONObjects(existing, templateMap)` 输出，`MergedFromDisk=true`
     - toml 格式（codex config）：`mergeCodexTOML(existing, model, baseURL)`，`MergedFromDisk=true`；**磁盘无文件/读取失败时也走 `mergeCodexTOML("", model, baseURL)` 作为模板形态**（保证全新安装同样产出统一 `[model_providers.aliang]` 段，而非旧版 openai/gateway 表——DoD #4 与 config-state 的 managed 判定都依赖这一点；此时 `MergedFromDisk=false`）。旧 `renderCodexConfigTOML` 随之删除
     - claude settings.json：模板 payload map 与 existing 深合并后，**额外删除 `env.ANTHROPIC_API_KEY`**（单鉴权源规则，spec §7），`MergedFromDisk=true`
  3. 读取用户家目录失败（`quickSetupTargetUserFn` 出错）→ 全部模板兜底，不报错（Render 不应因本机环境失败）。
- [ ] **Step 4: 全量测试绿。**
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置 Render 读磁盘智能合并预览（JSON 深合并/TOML 拼接/损坏降级）"`

---

### Task 10: Apply 备份先行

**Files:**
- Modify: `app/http/services/quick_setup_apply.go`、`app/http/models/quick_setup.go`（ApplyResponse 加 Backups）
- Test: `quick_setup_apply_test.go`（新文件，或并入 service 测试）

- [ ] **Step 1: 写失败测试**

```go
func TestApplyWritesDiskBackup(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")
	// 钩子注入：auth header、targetUser、writeConfigFileFn 用真实现（默认），keys 用 stub
	// …构造合法 codex apply 请求（两文件、合法内容）…
	resp, err := svc.Apply(req)
	if err != nil { t.Fatal(err) }
	raw, _ := os.ReadFile(filepath.Join(home, ".aliang/quick-setup/backups/codex/config.toml"))
	if string(raw) != "user original" { t.Fatal("disk backup missing") }
	if len(resp.Backups) != 2 { t.Fatalf("backups in response: %+v", resp.Backups) }

	// 再 apply 一次（磁盘已是我们的内容）→ 原始备份不得被覆盖
	raw2, _ := os.ReadFile(filepath.Join(home, ".aliang/quick-setup/backups/codex/config.toml"))
	if string(raw2) != "user original" { t.Fatal("first-backup-wins violated") }

	// manifest 损坏 → apply 拒绝且零写入
	writeBackupFixture(t, home, ".aliang/quick-setup/backups/manifest.json", "{bad")
	before := countWrites(t, home) // 简化：记录 config.toml 内容
	if _, err := svc.Apply(req); err == nil { t.Fatal("corrupt manifest must block apply") }
	raw3, _ := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if string(raw3) != prevContent(t, home, ".codex/config.toml", before) { t.Fatal("apply must be zero side-effect on backup failure") }
}
```

（写测试时按现有 `quick_setup_service_test.go` 的钩子模式补全 stub；`before` 断言可简化为「错误返回 + config.toml 内容不变」。）

- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现**：`Apply()` 在既有内存备份（qs:304-317 区域，回滚缓冲保留不动）之后、`quickSetupWriteConfigFileFn` 循环之前插入：

```go
	backupInfos, err := backupQuickSetupFiles(targetUser.homeDir, software, prepared)
	if err != nil {
		return nil, err // 备份失败 → 零写入（spec §6.3-4）
	}
```

`QuickSetupApplyResponse` 加 `Backups []models.QuickSetupBackupInfo \`json:"backups"\``。注意 prepared 需含 code（Task 9 已加）。

- [ ] **Step 4: 全量测试绿（现有回滚/零副作用契约不得破坏）。**
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置应用前原始配置落盘备份（备份失败零写入）"`

---

### Task 11: ConfigState 服务 + 端点

**Files:**
- Create: `app/http/services/quick_setup_snapshot.go`
- Modify: `app/http/models/quick_setup.go`、`app/http/handlers/quick_setup_handler.go`、`app/http/routes/routes.go`
- Test: `quick_setup_snapshot_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestConfigState(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "model=\"x\"\n[model_providers.aliang]\nname=\"Aliang Gateway\"\n")
	// 钩子注入 targetUser + auth header
	state, err := (&QuickSetupService{}).ConfigState("codex")
	if err != nil { t.Fatal(err) }
	if len(state.Files) != 2 { t.Fatalf("files: %+v", state.Files) }
	cfg := state.Files[0] // 顺序与软件定义一致
	if !cfg.Exists || !cfg.ManagedByAliang { t.Fatalf("config state: %+v", cfg) }
	auth := state.Files[1]
	if auth.Exists { t.Fatal("auth.json should not exist") }
}
```

- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现**

models：
```go
type QuickSetupConfigStateFile struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	Size            int64  `json:"size,omitempty"`
	ModifiedAt      string `json:"modified_at,omitempty"`
	Format          string `json:"format"`
	Content         string `json:"content,omitempty"`
	ManagedByAliang bool   `json:"managed_by_aliang"`
}

type QuickSetupConfigStateBackup struct {
	OriginalPath string `json:"original_path"`
	BackupPath   string `json:"backup_path,omitempty"`
	BackedUpAt   string `json:"backed_up_at"`
	SHA256       string `json:"sha256,omitempty"`
	Kind         string `json:"kind"`
}

type QuickSetupConfigStateResponse struct {
	Software string                          `json:"software"`
	Files    []QuickSetupConfigStateFile     `json:"files"`
	Backups  []QuickSetupConfigStateBackup   `json:"backups"`
}
```

服务（snapshot.go）：`ConfigState(softwareCode string)` —— 归一化 code → `findQuickSetupSoftware` → 鉴权检查（同 Apply：`quickSetupAuthorizationHeaderFn` 空 → `ErrQuickSetupUnauthenticated`）→ `quickSetupTargetUserFn` 家目录 → 逐声明文件 stat/read（>1MB 只给 size 不给 content）→ `quickSetupIsManagedByAliang(software, format, content)`（claude：`env.ANTHROPIC_BASE_URL` host ∈ {`127.0.0.1:56432`, `localhost:56432`, `api.aliang.one`, `backend.aliang.one`}；codex toml：含 `[model_providers.aliang]`；codex auth.json：跟随 config.toml 判定结果；opencode：任一 provider 的 baseURL host ∈ 上述集合）→ manifest 过滤 software 输出 backups。 software 不存在 → bad-request 错误。

Handler（GET，query 参数 `software`）+ 路由：紧邻现有 quick-setup 路由注册 `GET /api/quick-setup/config-state`。
- [ ] **Step 4: 测试通过 + handler 鉴权测试**（仿 `quick_setup_handler_test.go:29-62` 端点表把新端点加入 401 枚举）。
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置 config-state 端点（磁盘配置实时读取 + 备份清单）"`

---

### Task 12: Restore 端点

**Files:**
- Modify: `app/http/services/quick_setup_snapshot.go`（Restore 服务方法）、`quick_setup_handler.go`、`routes.go`
- Test: `quick_setup_snapshot_test.go` 追加 + handler 401 枚举

- [ ] **Step 1: 写失败测试**：service 层——备份后 restore，断言响应 restored/deleted 与磁盘状态（可复用 Task 4 的 restore 测试逻辑，走服务方法 + 鉴权钩子）；handler 层——新端点无 session 返回 401。
- [ ] **Step 2: 确认失败。**
- [ ] **Step 3: 实现**：`Restore(softwareCode string)`（鉴权 → findQuickSetupSoftware → targetUser → **`quickSetupApplyMu.Lock()` 全程持有**（与 Apply 互斥，防 manifest 并发写坏，spec §6.3-6）→ `restoreQuickSetupSoftware`；software 不存在 → bad-request）；handler `HandleRestore`（POST，body `{"software": "..."}`，MaxBytesReader 同现有）；路由 `POST /api/quick-setup/restore`。
- [ ] **Step 4: 全量测试绿。**
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置一键恢复原始配置端点"`

---

### Task 13: 后端全量回归

- [ ] **Step 1:** `gofmt -l app/ ; go vet ./app/... ; go test ./app/http/...`
Expected: 全部 PASS，无 vet 告警。
- [ ] **Step 2: Commit（如有 fmt/vet 修补）** `git commit -m "修复：快速配置 v2 后端回归修补"`

---

### Task 14: 前端 API 与纯函数

**Files:**
- Modify: `app/website/src/services/quickSetupApi.js`、`app/website/src/utils/quickSetupState.js`
- Test: `app/website/src/utils/quickSetupState.test.js`

- [ ] **Step 1: 写失败测试**（追加到现有测试文件，沿用其风格）：

```js
describe('filterInstalledQuickSetupSoftwares', () => {
  it('keeps installed built-ins and all customs, drops uninstalled built-ins', () => {
    const list = [
      { code: 'opencode', installed: true },
      { code: 'codex', installed: false },
      { code: 'claude-code', installed: true },
      { code: 'custom-abc', isCustom: true },
    ];
    expect(filterInstalledQuickSetupSoftwares(list).map((s) => s.code))
      .toEqual(['opencode', 'claude-code', 'custom-abc']);
  });
  it('returns customs even when nothing is installed', () => {
    expect(filterInstalledQuickSetupSoftwares([{ code: 'codex', installed: false }, { code: 'custom-x', isCustom: true }]).map((s) => s.code)).toEqual(['custom-x']);
  });
});
describe('quickSetupMode', () => {
  it('defaults to public and toggles per software', () => {
    const state = createQuickSetupModeState();
    expect(state.modeOf('codex')).toBe('public');
    state.setMode('codex', 'local');
    expect(state.modeOf('codex')).toBe('local');
    expect(state.modeOf('opencode')).toBe('public');
  });
});
```

- [ ] **Step 2:** `npx vitest run src/utils/quickSetupState.test.js` → FAIL。
- [ ] **Step 3: 实现**：`quickSetupState.js` 追加 `filterInstalledQuickSetupSoftwares(list)`（内置 = `!isCustom`，保留 `installed===true`；custom 全保留）与 `createQuickSetupModeState()`（Map 封装，`modeOf(code)` 缺省 `'public'`，`setMode(code, mode)` 仅接受 `'local'|'public'`）。`quickSetupApi.js` 追加：

```js
export async function fetchConfigState(software) {
  return rawRequest(`/api/quick-setup/config-state?software=${encodeURIComponent(software)}`);
}
export async function restoreConfig(software) {
  return rawRequest('/api/quick-setup/restore', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ software }),
  });
}
```

（helper 名以文件内实际为准——现为 `rawRequest`（quickSetupApi.js:13）；对照 `quickSetupApi.js:35-76` 现有四个函数的写法保持一致。）
- [ ] **Step 4: vitest 绿。**
- [ ] **Step 5: Commit** `git commit -m "新增：快速配置前端 installed 过滤与接入模式纯函数 + config-state/restore API"`

---

### Task 15: Modal——installed 过滤 + 空状态 + 模式切换 + 合并横幅

**Files:**
- Modify: `app/website/src/components/QuickSetupModal.vue`、`i18n/zh.js`、`i18n/en.js`

- [ ] **Step 1: i18n 键**（zh/en 同步，英文给合理翻译）：`qs_mode_local`「本地加速」、`qs_mode_public`「公网直连」、`qs_mode_local_hint`「依赖本软件的本地推理代理（登录后自动启动）」、`qs_merged_banner`「已合并你现有的配置，其余设置将保留」、`qs_no_agents_detected`「未检测到已安装的 AI Agent」、`qs_no_agents_desc`「安装 Claude Code / Codex / OpenCode 任一后，这里会出现对应的快速配置。」、`qs_result_title`「配置已应用」、`qs_backed_up`「已备份原始配置」、`qs_view_current`「查看当前配置」、`qs_state_title`「当前配置」、`qs_state_managed`「已由 Aliang 管理」、`qs_state_unmanaged`「未管理」、`qs_state_refresh`「刷新」、`qs_backup_original`「原始备份」、`qs_restore`「恢复原始配置」、`qs_restore_confirm_title`「恢复原始配置？」、`qs_restore_confirm_desc`「将把以下文件还原为你应用 Aliang 配置之前的状态；由 Aliang 新建的文件会被删除。」、`qs_restore_success`「已恢复原始配置」、`qs_restore_failed`「部分文件恢复失败」、`qs_state_loading`「正在读取配置…」

- [ ] **Step 2: 模板侧栏过滤**（`allSoftwares` 计算属性，QuickSetupModal.vue:487-490）：改为 `filterInstalledQuickSetupSoftwares(...)`。**接线提示**：modal 里 custom 模板是独立的 `customSoftwares` ref（:481），调用前先把 `softwares`（服务端返回，已带 `installed` 字段）与 `customSoftwares` 两个数组合并成 `filterInstalledQuickSetupSoftwares` 的入参形状（或把实现里的 `isCustom` 判定改为 `code.startsWith('custom-')`，二选一，保持测试契约语义）；三内置均未装且无 custom 时侧栏显示空状态块（`qs_no_agents_detected` + `qs_no_agents_desc`）。
- [ ] **Step 3: 模式切换**：配置面板 key 选择区上方插入分段控件（两个按钮，`:class` 按选中态高亮，样式对齐现有按钮族）；状态 `const modeState = createQuickSetupModeState()`；切换时触发对应 software 的重新 render（复用现有 `renderSelectedKey` / `scheduleOpenCodeRender` 路径，把 `mode: modeState.modeOf(software)` 传给 `renderQuickSetup` payload）；选中 local 时显示 `qs_mode_local_hint` 提示行。
- [ ] **Step 4: 合并横幅**：预览文件区顶部 `v-if="activeFileMergedFromDisk"`（取当前 tab 文件的 `merged_from_disk`）渲染淡色横幅 `qs_merged_banner`。
- [ ] **Step 5: 手动验证**：`cd app/website && npm run dev`，检查三状态（全未装空态 / 部分安装过滤 / 模式切换重渲染 + 横幅）。
- [ ] **Step 6: Commit** `git commit -m "新增：快速配置弹窗按已安装过滤 + 本地/公网模式切换 + 合并提示横幅"`

---

### Task 16: 应用结果页

**Files:** Modify: `QuickSetupModal.vue`

- [ ] **Step 1: 实现**：`applyCurrentVariant` 成功分支（现 :879-902）不再只写 statusMessage——新增 `applyResult` 响应式状态（存 `written` 路径数组 + `backups` 数组 + 各文件最终 content），模板加结果视图块（覆盖预览区）：标题 `qs_result_title`、written 文件路径列表、`qs_backed_up` 卡片（`original_path → backup_path`，`existed_before=false` 显示「新建文件」）、按钮「返回编辑」（复位 `applyResult`）与「查看当前配置」（切到 StatePanel 页签）。`result.written` 明细与 `result.backups` 全量使用（不再只取 length）。
- [ ] **Step 2: 手动验证**：本机（有真实 ~/.claude）走一遍 apply，确认结果页两卡片内容正确。
- [ ] **Step 3: Commit** `git commit -m "新增：快速配置应用结果页（写入清单 + 原始备份卡片）"`

---

### Task 17: QuickSetupStatePanel 常驻查看器 + 恢复

**Files:**
- Create: `app/website/src/components/QuickSetupStatePanel.vue`
- Modify: `QuickSetupModal.vue`（引入为第三个页签）

- [ ] **Step 1: 新组件**（结构对齐现有组件风格：`<script setup>` + i18n `useI18n` + tailwind；props: `software`，emit 无）：

```vue
<script setup>
import { ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { fetchConfigState, restoreConfig } from '../services/quickSetupApi';

const props = defineProps({ software: { type: String, required: true } });
const { t } = useI18n();
const state = ref(null);
const loading = ref(false);
const error = ref('');
const restoring = ref(false);
const confirmOpen = ref(false);

async function load() {
  loading.value = true; error.value = '';
  try { state.value = await fetchConfigState(props.software); }
  catch (e) { error.value = e?.message || String(e); }
  finally { loading.value = false; }
}
async function doRestore() {
  restoring.value = true;
  try { await restoreConfig(props.software); await load(); confirmOpen.value = false; }
  catch (e) { error.value = e?.message || String(e); }
  finally { restoring.value = false; }
}
watch(() => props.software, load, { immediate: true });
</script>
```

模板三区：(a) 文件列表——每个 file 卡片：路径、`managed_by_aliang` 徽标（绿 `qs_state_managed` / 灰 `qs_state_unmanaged`）、mtime、`<pre>` 内容（`exists` 为 false 显示「（不存在）」）；刷新按钮 `qs_state_refresh`。(b) `qs_backup_original` 区——backups 卡片：original_path、backed_up_at、kind 徽标；`qs_restore` 按钮 → confirm 弹窗（列 `existed_before` 分组：还原/将删除）→ `doRestore`。(c) loading/error 态。
- [ ] **Step 2: 接入 Modal**：右侧面板文件预览区上方加页签行「配置预览 | 当前配置」（`qs_state_title`），StatePanel 仅在当前 software 为内置（非 custom）时显示；「查看当前配置」按钮（Task 16）切到该页签。
- [ ] **Step 3: 手动验证**：查看内容与磁盘一致、刷新生效、恢复后文件回滚/删除、restore 失败路径（手动改坏 manifest）显示错误。
- [ ] **Step 4: Commit** `git commit -m "新增：快速配置当前配置常驻查看器与一键恢复 UI"`

---

### Task 18: 全量回归与构建

- [ ] **Step 1:** `go test ./...`（全仓）+ `cd app/website && npx vitest run`
- [ ] **Step 2:** 前端构建 `cd app/website && npm run build`（产物进 `app/website/dist`，embed 走 `app/embed.go`）。
- [ ] **Step 3:** 端到端冒烟（本机）：启动应用 → 快速配置弹窗只显示已装 agent → 对 claude-code 走 local 模式 apply → 验证 `~/.claude/settings.json` env 块合并且原 hooks 保留 → 恢复原始配置 → 文件回到 apply 前。
- [ ] **Step 4: Commit** `git commit -m "测试：快速配置 v2 全量回归与构建通过"`

---

## 完成定义（DoD）

1. 三内置 agent 按「CLI ∪ 目录」检测过滤展示，未装不显示；custom 模板不受影响。
2. 任何 apply 前原始配置落盘 `~/.aliang/quick-setup/backups/`（first-backup-wins），manifest 标记 `kind: "original"`；manifest 损坏阻断 apply。
3. Render 预览即磁盘合并结果；用户其余设置（MCP/主题/hooks/ChatGPT tokens）保留。
4. claude-code 写 `~/.claude/settings.json` env 块（AUTH_TOKEN，无 /v1）；codex 统一 `[model_providers.aliang]`；模式 local/public 每 agent 独立。
5. 结果页 + 常驻查看器 + 一键恢复（含删除新建文件）可用。
6. 全部既有测试绿 + 新增测试覆盖 spec §11 全部条目。
