package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
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
