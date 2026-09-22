package services

import (
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

	// manifest 损坏 → apply 拒绝且零写入
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
}
