package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeBackupFixture(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func backupTestUser(home string) quickSetupTargetUser {
	return quickSetupTargetUser{homeDir: home}
}

func loadBackupManifestOrFail(t *testing.T, home string) quickSetupManifest {
	t.Helper()
	m, err := loadQuickSetupManifest(home)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func readBackupFile(t *testing.T, home, backupPath string) string {
	t.Helper()
	if !strings.HasPrefix(backupPath, "~/") {
		t.Fatalf("backup path %q is not in ~/ form", backupPath)
	}
	raw, err := os.ReadFile(filepath.Join(home, strings.TrimPrefix(backupPath, "~/")))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBackupQuickSetupFiles_FirstBackupWins(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	writeBackupFixture(t, home, ".codex/config.toml", "user original")

	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "aliang v1"}}
	if _, err := backupQuickSetupFiles(user, "codex", files); err != nil {
		t.Fatal(err)
	}

	// 第二次 apply 前磁盘已是我们的 v1 —— 不得覆盖原始备份
	writeBackupFixture(t, home, ".codex/config.toml", "aliang v1")
	files[0].content = "aliang v2"
	infos, err := backupQuickSetupFiles(user, "codex", files)
	if err != nil {
		t.Fatal(err)
	}
	m := loadBackupManifestOrFail(t, home)
	if len(m.Backups) != 1 {
		t.Fatalf("manifest %+v", m.Backups)
	}
	if got := readBackupFile(t, home, m.Backups[0].BackupPath); got != "user original" {
		t.Fatalf("original backup clobbered: %q", got)
	}
	base := filepath.Base(filepath.FromSlash(strings.TrimPrefix(m.Backups[0].BackupPath, "~/")))
	if len(base) != len("000000000000-config.toml") || !strings.HasSuffix(base, "-config.toml") {
		t.Fatalf("backup file name must be <hash12>-<base>, got %q", base)
	}
	if len(infos) != 1 || infos[0].ExistedBefore != true {
		t.Fatalf("unexpected infos %+v", infos)
	}
}

func TestBackupQuickSetupFiles_NewFileRecorded(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/auth.json"), code: "auth", content: "{}"}}
	infos, err := backupQuickSetupFiles(user, "codex", files)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ExistedBefore {
		t.Fatalf("unexpected %+v", infos)
	}
	if entries, err := os.ReadDir(filepath.Join(home, ".aliang/quick-setup/backups/codex")); err == nil && len(entries) > 0 {
		t.Fatalf("no backup file should exist for created file, got %d entries", len(entries))
	}
	m := loadBackupManifestOrFail(t, home)
	if len(m.Backups) != 1 || m.Backups[0].Kind != "original" || m.Backups[0].ExistedBefore {
		t.Fatalf("manifest %+v", m.Backups)
	}
}

func TestBackupQuickSetupFiles_SameBaseNameNoCollision(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	writeBackupFixture(t, home, ".codex/config.toml", "top original")
	writeBackupFixture(t, home, ".codex/sub/config.toml", "nested original")

	files := []quickSetupPreparedFile{
		{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "aliang"},
		{path: filepath.Join(home, ".codex/sub/config.toml"), code: "config", content: "aliang"},
	}
	if _, err := backupQuickSetupFiles(user, "codex", files); err != nil {
		t.Fatal(err)
	}
	m := loadBackupManifestOrFail(t, home)
	if len(m.Backups) != 2 {
		t.Fatalf("manifest %+v", m.Backups)
	}
	if m.Backups[0].BackupPath == m.Backups[1].BackupPath {
		t.Fatalf("same-basename files must not share a backup path: %q", m.Backups[0].BackupPath)
	}
	if got := readBackupFile(t, home, m.Backups[0].BackupPath); got != "top original" {
		t.Fatalf("top backup wrong content: %q", got)
	}
	if got := readBackupFile(t, home, m.Backups[1].BackupPath); got != "nested original" {
		t.Fatalf("nested backup wrong content: %q", got)
	}
}

func TestBackupQuickSetupFiles_SecondBackupOfCreatedFile(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/auth.json"), code: "auth", content: "{}"}}
	if _, err := backupQuickSetupFiles(user, "codex", files); err != nil {
		t.Fatal(err)
	}

	// apply 之后磁盘上是我们的配置；再次备份不得把我们的内容当成「原始配置」
	writeBackupFixture(t, home, ".codex/auth.json", "aliang v1")
	infos, err := backupQuickSetupFiles(user, "codex", files)
	if err != nil {
		t.Fatal(err)
	}
	m := loadBackupManifestOrFail(t, home)
	if len(m.Backups) != 1 || m.Backups[0].ExistedBefore {
		t.Fatalf("entry must stay ExistedBefore=false, got %+v", m.Backups)
	}
	if m.Backups[0].BackupPath != "" {
		t.Fatalf("entry must not gain a backup path, got %q", m.Backups[0].BackupPath)
	}
	if entries, err := os.ReadDir(filepath.Join(home, ".aliang/quick-setup/backups/codex")); err == nil && len(entries) > 0 {
		t.Fatalf("no backup file should be written for a file we created, got %d entries", len(entries))
	}
	if len(infos) != 1 || !infos[0].ExistedBefore || infos[0].BackupPath != "" {
		t.Fatalf("unexpected infos %+v", infos)
	}
}

func TestBackupQuickSetupFiles_CorruptManifestBlocks(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	writeBackupFixture(t, home, ".aliang/quick-setup/backups/manifest.json", "{broken")
	writeBackupFixture(t, home, ".codex/config.toml", "x")
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "y"}}
	if _, err := backupQuickSetupFiles(user, "codex", files); err == nil {
		t.Fatal("corrupt manifest must block backup")
	}
}

func TestLoadQuickSetupManifest_VersionGate(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".aliang/quick-setup/backups/manifest.json", `{"version":2,"backups":[]}`)
	if _, err := loadQuickSetupManifest(home); err == nil {
		t.Fatal("unsupported manifest version must be rejected")
	}
	// JSON null 解析后 version 为 0，同样必须被门禁挡下
	writeBackupFixture(t, home, ".aliang/quick-setup/backups/manifest.json", `null`)
	if _, err := loadQuickSetupManifest(home); err == nil {
		t.Fatal("null manifest must be rejected")
	}
}

func TestRestoreQuickSetupSoftware(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	writeBackupFixture(t, home, ".codex/config.toml", "user original")
	files := []quickSetupPreparedFile{
		{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "aliang"},
		{path: filepath.Join(home, ".codex/auth.json"), code: "auth", content: "{}"},
	}
	if _, err := backupQuickSetupFiles(user, "codex", files); err != nil {
		t.Fatal(err)
	}
	m := loadBackupManifestOrFail(t, home)
	configBackupPath := m.Backups[0].BackupPath

	res, err := restoreQuickSetupSoftware(user, "codex")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if string(raw) != "user original" {
		t.Fatalf("restore failed: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex/auth.json")); !os.IsNotExist(err) {
		t.Fatal("created file must be deleted on restore")
	}
	if len(res.Restored) != 1 || len(res.Deleted) != 1 {
		t.Fatalf("unexpected %+v", res)
	}
	if _, err := os.Stat(filepath.Join(home, strings.TrimPrefix(configBackupPath, "~/"))); !os.IsNotExist(err) {
		t.Fatal("backup file must be deleted after successful manifest save")
	}
	reloaded := loadBackupManifestOrFail(t, home)
	if len(reloaded.Backups) != 0 {
		t.Fatalf("manifest entries must be cleared, got %+v", reloaded.Backups)
	}
}

func TestRestoreQuickSetupSoftware_MissingBackupFileFails(t *testing.T) {
	home := t.TempDir()
	user := backupTestUser(home)
	m := quickSetupManifest{Version: quickSetupManifestVersion, Backups: []quickSetupManifestEntry{{
		Software: "codex", FileCode: "config",
		OriginalPath:  "~/.codex/config.toml",
		BackupPath:    "~/.aliang/quick-setup/backups/codex/000000000000-config.toml",
		BackedUpAt:    time.Now().Format(time.RFC3339),
		ExistedBefore: true, Kind: quickSetupManifestKindOriginal,
	}}}
	if err := saveQuickSetupManifest(user, m); err != nil {
		t.Fatal(err)
	}

	res, err := restoreQuickSetupSoftware(user, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || res.Failed[0].Path != "~/.codex/config.toml" {
		t.Fatalf("expected the entry to fail, got %+v", res.Failed)
	}
	if len(res.Restored) != 0 || len(res.Deleted) != 0 {
		t.Fatalf("unexpected restore result %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".codex/config.toml")); !os.IsNotExist(statErr) {
		t.Fatal("original file must not be created from a missing backup")
	}
	reloaded := loadBackupManifestOrFail(t, home)
	if len(reloaded.Backups) != 1 || reloaded.Backups[0].OriginalPath != "~/.codex/config.toml" {
		t.Fatalf("failed entry must be kept in manifest, got %+v", reloaded.Backups)
	}
}
