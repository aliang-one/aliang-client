package services

import (
	"os"
	"path/filepath"
	"testing"
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

func TestBackupQuickSetupFiles_FirstBackupWins(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")

	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "aliang v1"}}
	if _, err := backupQuickSetupFiles(home, "codex", files); err != nil {
		t.Fatal(err)
	}

	// 第二次 apply 前磁盘已是我们的 v1 —— 不得覆盖原始备份
	writeBackupFixture(t, home, ".codex/config.toml", "aliang v1")
	files[0].content = "aliang v2"
	infos, err := backupQuickSetupFiles(home, "codex", files)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".aliang/quick-setup/backups/codex/config.toml"))
	if string(raw) != "user original" {
		t.Fatalf("original backup clobbered: %q", raw)
	}
	if len(infos) != 1 || infos[0].ExistedBefore != true {
		t.Fatalf("unexpected infos %+v", infos)
	}
}

func TestBackupQuickSetupFiles_NewFileRecorded(t *testing.T) {
	home := t.TempDir()
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/auth.json"), code: "auth", content: "{}"}}
	infos, err := backupQuickSetupFiles(home, "codex", files)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ExistedBefore {
		t.Fatalf("unexpected %+v", infos)
	}
	if _, err := os.Stat(filepath.Join(home, ".aliang/quick-setup/backups/codex/auth.json")); !os.IsNotExist(err) {
		t.Fatal("no backup file should exist for created file")
	}
	m, err := loadQuickSetupManifest(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Backups) != 1 || m.Backups[0].Kind != "original" || m.Backups[0].ExistedBefore {
		t.Fatalf("manifest %+v", m.Backups)
	}
}

func TestBackupQuickSetupFiles_CorruptManifestBlocks(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".aliang/quick-setup/backups/manifest.json", "{broken")
	writeBackupFixture(t, home, ".codex/config.toml", "x")
	files := []quickSetupPreparedFile{{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "y"}}
	if _, err := backupQuickSetupFiles(home, "codex", files); err == nil {
		t.Fatal("corrupt manifest must block backup")
	}
}

func TestRestoreQuickSetupSoftware(t *testing.T) {
	home := t.TempDir()
	writeBackupFixture(t, home, ".codex/config.toml", "user original")
	files := []quickSetupPreparedFile{
		{path: filepath.Join(home, ".codex/config.toml"), code: "config", content: "aliang"},
		{path: filepath.Join(home, ".codex/auth.json"), code: "auth", content: "{}"},
	}
	if _, err := backupQuickSetupFiles(home, "codex", files); err != nil {
		t.Fatal(err)
	}

	res, err := restoreQuickSetupSoftware(home, "codex")
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
	m, _ := loadQuickSetupManifest(home)
	if len(m.Backups) != 0 {
		t.Fatalf("manifest entries must be cleared, got %+v", m.Backups)
	}
}
