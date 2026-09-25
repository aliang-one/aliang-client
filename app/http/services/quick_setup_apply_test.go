package services

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
)

// TestApplyWritesDiskBackup 锁定 Apply 备份先行契约（spec §6.3）：
// 1. 写配置前把磁盘上的原始内容落盘备份，并在响应中报告各文件状态；
// 2. first-backup-wins：二次 apply（磁盘已是我们的内容）不得覆盖原始备份；
// 3. manifest 损坏 → Apply 拒绝且零写入（fail-safe，防止把我们的配置当原始配置重新备份）。
func TestApplyWritesDiskBackup(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")

	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
	})

	svc := NewQuickSetupService()
	req := models.QuickSetupApplyRequest{
		Software: "codex",
		Files: []models.QuickSetupApplyFile{
			{Path: "~/.codex/config.toml", Content: "[model_providers.aliang]\nname = \"aliang gateway\"\n", Kind: "file"},
			{Path: "~/.codex/auth.json", Content: `{"OPENAI_API_KEY":"sk-test"}`, Kind: "file"},
		},
	}
	resp, err := svc.Apply(req)
	if err != nil {
		t.Fatal(err)
	}

	if len(resp.Backups) != 2 {
		t.Fatalf("backups in response: %+v", resp.Backups)
	}
	var configBackup, authBackup *models.QuickSetupBackupInfo
	for i := range resp.Backups {
		switch {
		case strings.HasSuffix(resp.Backups[i].OriginalPath, "/.codex/config.toml"):
			configBackup = &resp.Backups[i]
		case strings.HasSuffix(resp.Backups[i].OriginalPath, "/.codex/auth.json"):
			authBackup = &resp.Backups[i]
		}
	}
	if configBackup == nil || authBackup == nil {
		t.Fatalf("missing backup infos for config/auth: %+v", resp.Backups)
	}
	if !configBackup.ExistedBefore || configBackup.BackupPath == "" {
		t.Fatalf("config backup info = %+v, want existed_before with backup path", configBackup)
	}
	if got := readBackupFile(t, home, configBackup.BackupPath); got != "user original" {
		t.Fatalf("backed up content = %q, want %q", got, "user original")
	}
	if authBackup.ExistedBefore || authBackup.BackupPath != "" {
		t.Fatalf("auth backup info = %+v, want existed_before=false without backup file", authBackup)
	}

	// 二次 apply：磁盘已是我们的内容 → 原始备份不得被覆盖（first-backup-wins）
	if _, err := svc.Apply(req); err != nil {
		t.Fatal(err)
	}
	if got := readBackupFile(t, home, configBackup.BackupPath); got != "user original" {
		t.Fatalf("original backup clobbered by second apply: %q", got)
	}

	// manifest 损坏 → apply 拒绝且零写入。先移除 auth.json 模拟新建场景：
	// 被拒的 apply 不得创建任何新文件，也不得改动已有文件。
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aliang", "quick-setup", "backups", "manifest.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(req); err == nil {
		t.Fatal("corrupt manifest must block apply")
	}
	after, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("apply must be zero side-effect on backup failure")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(statErr) {
		t.Fatal("rejected apply must not create the new file")
	}
}

// TestApplyRejectsUnresolvedPlaceholders 锁定占位符兜底契约（spec §5）：
// 组合渲染在前端完成，后端 Apply 兜底拒绝未替换的 {{...}} 占位符——
// 1. config.toml 含 {{base_url}} → Apply 报错且零写入；
// 2. 全部替换为真实值后 → Apply 成功。
func TestApplyRejectsUnresolvedPlaceholders(t *testing.T) {
	home := t.TempDir()

	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
	})

	req := models.QuickSetupApplyRequest{
		Software: "codex",
		Files: []models.QuickSetupApplyFile{
			{Path: "~/.codex/config.toml", Content: "[model_providers.aliang]\nname = \"aliang gateway\"\nbase_url = \"{{base_url}}\"\n", Kind: "file"},
			{Path: "~/.codex/auth.json", Content: `{"OPENAI_API_KEY":"sk-test"}`, Kind: "file"},
		},
	}

	svc := NewQuickSetupService()
	_, err := svc.Apply(req)
	if err == nil {
		t.Fatal("Apply() succeeded despite unresolved {{base_url}} placeholder")
	}
	if !strings.Contains(err.Error(), "{{base_url}}") || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("error = %q, want it to mention {{base_url}} and placeholder", err.Error())
	}
	for _, p := range []string{filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".codex", "auth.json")} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Fatalf("rejected apply must not write %s: %v", p, statErr)
		}
	}

	// 全部替换为真实值 → Apply 成功
	req.Files[0].Content = "[model_providers.aliang]\nname = \"aliang gateway\"\nbase_url = \"https://api.aliang.one/v1\"\n"
	if _, err := svc.Apply(req); err != nil {
		t.Fatalf("Apply() with fully rendered content failed: %v", err)
	}
	if written, readErr := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); readErr != nil || !strings.Contains(string(written), "https://api.aliang.one/v1") {
		t.Fatalf("config.toml after successful apply = %q (err=%v), want rendered base_url", string(written), readErr)
	}
}

// TestApplyRollbackPreservesOriginalBackupAndRestoreWorks 锁定「回滚 × 备份 × 恢复」
// 组合不变量（spec §6.3）：写入失败回滚只还原磁盘文件，不得破坏备份先行的产物——
// 1. 回滚成功后 manifest 仍有 codex 条目，config.toml 回到用户原文；
// 2. 重试的 apply 成功后原始备份不被覆盖（first-backup-wins 跨越失败轮回）；
// 3. restore 仍按最初的备份语义工作：config.toml 还原原文、本轮创建的 auth.json 删除。
func TestApplyRollbackPreservesOriginalBackupAndRestoreWorks(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")

	previousAuth := quickSetupAuthorizationHeaderFn
	previousTargetUser := quickSetupTargetUserFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	quickSetupTargetUserFn = func() (quickSetupTargetUser, error) {
		return quickSetupTargetUser{homeDir: home}, nil
	}
	t.Cleanup(func() {
		quickSetupAuthorizationHeaderFn = previousAuth
		quickSetupTargetUserFn = previousTargetUser
	})

	req := models.QuickSetupApplyRequest{
		Software: "codex",
		Files: []models.QuickSetupApplyFile{
			{Path: "~/.codex/config.toml", Content: "[model_providers.aliang]\nname = \"aliang gateway\"\n", Kind: "file"},
			{Path: "~/.codex/auth.json", Content: `{"OPENAI_API_KEY":"sk-test"}`, Kind: "file"},
		},
	}

	// 第一次 Apply：第二个文件（auth.json）写入失败 → 整体回滚
	previousWriter := quickSetupWriteConfigFileFn
	t.Cleanup(func() { quickSetupWriteConfigFileFn = previousWriter })
	writes := 0
	quickSetupWriteConfigFileFn = func(path string, content string) error {
		writes++
		if err := writeConfigFile(path, content); err != nil {
			return err
		}
		if writes == 2 {
			return errors.New("injected write failure")
		}
		return nil
	}
	if _, err := NewQuickSetupService().Apply(req); err == nil {
		t.Fatal("Apply() succeeded despite injected write failure")
	}
	quickSetupWriteConfigFileFn = previousWriter

	rolledBack, readErr := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(rolledBack) != "user original" {
		t.Fatalf("config.toml after rollback = %q, want %q", rolledBack, "user original")
	}

	// 回滚不得抹掉备份先行的产物：manifest 仍有 codex 条目
	m := loadBackupManifestOrFail(t, home)
	codexEntries := 0
	for _, entry := range m.Backups {
		if entry.Software == "codex" {
			codexEntries++
		}
	}
	if codexEntries < 1 {
		t.Fatalf("manifest lost codex entries after rollback: %+v", m.Backups)
	}

	// 移除注入后重试 → 成功，且 first-backup-wins：备份文件仍是用户原文
	resp, err := NewQuickSetupService().Apply(req)
	if err != nil {
		t.Fatal(err)
	}
	var configBackupPath string
	for i := range resp.Backups {
		if strings.HasSuffix(resp.Backups[i].OriginalPath, "/.codex/config.toml") {
			configBackupPath = resp.Backups[i].BackupPath
		}
	}
	if configBackupPath == "" {
		t.Fatalf("missing config backup info: %+v", resp.Backups)
	}
	if got := readBackupFile(t, home, configBackupPath); got != "user original" {
		t.Fatalf("original backup clobbered by retried apply: %q", got)
	}

	// restore：config.toml 还原用户原文；本轮创建的 auth.json（existed_before=false）被删除
	if _, err := restoreQuickSetupSoftware(quickSetupTargetUser{homeDir: home}, "codex"); err != nil {
		t.Fatal(err)
	}
	restored, readErr := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restored) != "user original" {
		t.Fatalf("config.toml after restore = %q, want %q", restored, "user original")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".codex", "auth.json")); !os.IsNotExist(statErr) {
		t.Fatalf("created auth.json must be deleted on restore: %v", statErr)
	}
}

// TestApplyPersistsAppliedSnapshot 锁定「上次应用快照」契约（v3.1）：
//  1. combo_id 指向 software 匹配的组合 → apply 全部成功后组合行持久化本次
//     实际写入的每文件 {code, content}（后端权威）+ RFC3339 applied_at；
//  2. combo_id=0（非组合路径）→ 组合快照不动；
//  3. combo_id 指向他 software 组合 → apply 仍成功但该组合快照不动（仅记日志）；
//  4. combo_id 指向不存在的组合 → apply 仍成功（快照旁路失败不连坐）。
func TestApplyPersistsAppliedSnapshot(t *testing.T) {
	comboSvc, store := stubComboServiceEnv(t)

	home := t.TempDir()
	stubComboTargetHome(t, home)
	previousAuth := quickSetupAuthorizationHeaderFn
	quickSetupAuthorizationHeaderFn = func() string { return "Bearer test-access" }
	t.Cleanup(func() { quickSetupAuthorizationHeaderFn = previousAuth })

	codexCombo, err := comboSvc.Create("codex", "套餐A", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	claudeCombo, err := comboSvc.Create("claude-code", "别的软件", "blank", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	const configContent = "[model_providers.aliang]\nname = \"aliang gateway\"\nbase_url = \"https://api.example.com/v1\"\n"
	applyFiles := func(authKey string) []models.QuickSetupApplyFile {
		return []models.QuickSetupApplyFile{
			{Path: "~/.codex/config.toml", Content: configContent, Kind: "file"},
			{Path: "~/.codex/auth.json", Content: `{"OPENAI_API_KEY":"` + authKey + `"}`, Kind: "file"},
		}
	}
	svc := NewQuickSetupService()

	// 1. combo_id 匹配 → 快照 = 实际落盘内容。
	req := models.QuickSetupApplyRequest{Software: "codex", ComboID: codexCombo.ID, Files: applyFiles("sk-snap")}
	if _, err := svc.Apply(req); err != nil {
		t.Fatal(err)
	}
	view := comboViewByID(t, store, codexCombo.ID)
	wantApplied := []models.QuickSetupComboFile{
		{Code: "config", Content: configContent},
		{Code: "auth", Content: `{"OPENAI_API_KEY":"sk-snap"}`},
	}
	if !reflect.DeepEqual(view.Applied, wantApplied) {
		t.Fatalf("applied mismatch:\ngot  %+v\nwant %+v", view.Applied, wantApplied)
	}
	if _, err := time.Parse(time.RFC3339, view.AppliedAt); err != nil {
		t.Fatalf("applied_at = %q, want RFC3339 timestamp: %v", view.AppliedAt, err)
	}

	// 2. combo_id=0（非组合路径）→ 组合快照不动。
	req0 := models.QuickSetupApplyRequest{Software: "codex", Files: applyFiles("sk-other")}
	if _, err := svc.Apply(req0); err != nil {
		t.Fatal(err)
	}
	after := comboViewByID(t, store, codexCombo.ID)
	if !reflect.DeepEqual(after.Applied, wantApplied) || after.AppliedAt != view.AppliedAt {
		t.Fatalf("combo_id=0 must not touch snapshot: applied=%+v applied_at=%q", after.Applied, after.AppliedAt)
	}

	// 3. software 不匹配 → apply 仍成功，claude-code 组合快照保持为空。
	reqMismatch := models.QuickSetupApplyRequest{Software: "codex", ComboID: claudeCombo.ID, Files: applyFiles("sk-cross")}
	if _, err := svc.Apply(reqMismatch); err != nil {
		t.Fatal(err)
	}
	claudeView := comboViewByID(t, store, claudeCombo.ID)
	if len(claudeView.Applied) != 0 || claudeView.AppliedAt != "" {
		t.Fatalf("cross-software combo snapshot must stay empty: applied=%+v applied_at=%q", claudeView.Applied, claudeView.AppliedAt)
	}
	final := comboViewByID(t, store, codexCombo.ID)
	if !reflect.DeepEqual(final.Applied, wantApplied) || final.AppliedAt != view.AppliedAt {
		t.Fatalf("cross-software apply must not touch codex snapshot: applied=%+v applied_at=%q", final.Applied, final.AppliedAt)
	}

	// 4. combo_id 指向不存在的组合 → apply 仍成功。
	reqMissing := models.QuickSetupApplyRequest{Software: "codex", ComboID: codexCombo.ID + 9999, Files: applyFiles("sk-missing")}
	if _, err := svc.Apply(reqMissing); err != nil {
		t.Fatalf("apply must succeed even when combo is missing: %v", err)
	}
}

func comboViewByID(t *testing.T, store *storage.QuickSetupComboStore, id int64) models.QuickSetupComboView {
	t.Helper()
	row, err := store.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	view, err := storage.ComboToView(row)
	if err != nil {
		t.Fatal(err)
	}
	return *view
}
