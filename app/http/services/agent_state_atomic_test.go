package services

// saveStateLocked 特征化测试：落盘产物必须是合法 JSON、权限 0600、与内存态
// 一致，且目录内不留临时文件残渣。原子化重构（tmp+rename）前后行为一致。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestSaveStateProducesCleanAtomicArtifact(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	service := NewAgentService()
	service.mu.Lock()
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceID = "dev_atomic"
	service.state.LastSyncStatus = "connecting"
	saveErr := service.saveStateLocked()
	statePath, pathErr := agentStatePath()
	service.mu.Unlock()
	if saveErr != nil {
		t.Fatalf("saveStateLocked() error = %v", saveErr)
	}
	if pathErr != nil {
		t.Fatalf("agentStatePath() error = %v", pathErr)
	}

	dir := filepath.Dir(statePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("temporary file residue left behind: %s", entry.Name())
		}
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statePath, err)
	}
	var persisted agentState
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted agent_state.json is not valid JSON: %v", err)
	}
	if !persisted.Registered || persisted.DeviceID != "dev_atomic" {
		t.Fatalf("persisted state mismatch: registered=%t device_id=%q", persisted.Registered, persisted.DeviceID)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", statePath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
}
