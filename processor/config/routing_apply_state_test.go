package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/common/cache"
)

// isolateRoutingPersistence 把持久化文件指向独立临时状态目录：
// cache.GetCacheDir 是包级 sync.Once，须 Reset 后再解析才受本测试 env 影响。
func isolateRoutingPersistence(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	t.Setenv("ALIANG_DATA_DIR", stateDir)
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	return stateDir
}

func testSnapshotBuilder(cfg *CanonicalRoutingSchema) (any, error) {
	return "snapshot:" + cfg.Ingress.Mode, nil
}

func persistedFilePath(t *testing.T) string {
	t.Helper()
	path, err := routingPersistFilePath()
	if err != nil {
		t.Fatalf("routingPersistFilePath() error = %v", err)
	}
	return path
}

func mustApplyCanonical(t *testing.T, raw []byte) *RoutingApplyResult {
	t.Helper()
	result, err := GetRoutingApplyStore().Apply(raw, testSnapshotBuilder)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return result
}

const toSocksDisabledCanonical = `{
	"version": 1,
	"ingress": {"mode": "tun"},
	"egress": {
		"direct": {"enabled": true},
		"toAliang": {"enabled": true},
		"toSocks": {"enabled": false, "upstream": {"type": "socks"}}
	},
	"routing": {"rules": [], "default_egress": "direct"}
}`

func TestApplyPersistsCanonicalRoutingFileRoundTrip(t *testing.T) {
	isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	mustApplyCanonical(t, []byte(toSocksDisabledCanonical))

	path := persistedFilePath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persisted routing file should exist after Apply: %v", err)
	}

	// 持久化内容必须能被同一 normalize 路径接受（DisallowUnknownFields 兼容）
	var canonical CanonicalRoutingSchema
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatalf("persisted content is not valid canonical JSON: %v\n%s", err, raw)
	}
	if canonical.Egress.ToSocks.Enabled {
		t.Fatalf("persisted toSocks.enabled = true, want false")
	}

	// 模拟进程重启：清空内存 store 后从文件恢复，开关状态保持关闭（核心验收）
	ResetRoutingApplyStoreForTest()
	if GetRoutingApplyStore().ActiveCanonicalSchema() != nil {
		t.Fatal("store should be empty after reset")
	}

	loaded, err := GetRoutingApplyStore().LoadPersisted(testSnapshotBuilder)
	if err != nil {
		t.Fatalf("LoadPersisted() error = %v", err)
	}
	if !loaded {
		t.Fatal("LoadPersisted() loaded = false, want true when file exists")
	}

	restored := GetRoutingApplyStore().ActiveCanonicalSchema()
	if restored == nil {
		t.Fatal("restored canonical is nil")
	}
	if restored.Egress.ToSocks.Enabled {
		t.Fatal("toSocks.enabled should stay false across restart (core acceptance)")
	}
	if !restored.Egress.ToAliang.Enabled {
		t.Fatal("toAliang.enabled should stay true across restart")
	}
	if restored.Ingress.Mode != "tun" {
		t.Fatalf("ingress.mode = %q, want tun", restored.Ingress.Mode)
	}

	aliangEnabled, socksEnabled, ok := GetRoutingApplyStore().ActiveEgressSwitches()
	if !ok || !aliangEnabled || socksEnabled {
		t.Fatalf("ActiveEgressSwitches() = (%v,%v,%v), want (true,false,true)", aliangEnabled, socksEnabled, ok)
	}
}

func TestLoadPersisted_MissingFileReturnsNotLoaded(t *testing.T) {
	isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	loaded, err := GetRoutingApplyStore().LoadPersisted(testSnapshotBuilder)
	if err != nil {
		t.Fatalf("LoadPersisted() error = %v, want nil for missing file", err)
	}
	if loaded {
		t.Fatal("LoadPersisted() loaded = true, want false when file missing")
	}
	if GetRoutingApplyStore().ActiveCanonicalSchema() != nil {
		t.Fatal("store must stay empty when no persisted file (bootstrap derivation applies)")
	}
}

func TestLoadPersisted_CorruptedFileKeepsStoreEmpty(t *testing.T) {
	stateDir := isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	path := filepath.Join(stateDir, routingPersistFileName)
	if err := os.WriteFile(path, []byte(`{"version": 1, "ingress": {"mode": "tun"}, "unknown_key": true}`), 0o666); err != nil {
		t.Fatalf("write corrupted file failed: %v", err)
	}

	loaded, err := GetRoutingApplyStore().LoadPersisted(testSnapshotBuilder)
	if err == nil {
		t.Fatal("LoadPersisted() error = nil, want error for corrupted file")
	}
	if loaded {
		t.Fatal("LoadPersisted() loaded = true, want false for corrupted file")
	}
	if GetRoutingApplyStore().ActiveCanonicalSchema() != nil {
		t.Fatal("store must stay empty when persisted file is corrupted")
	}
}

func TestLoadPersisted_EmptyFileKeepsStoreEmpty(t *testing.T) {
	stateDir := isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	path := filepath.Join(stateDir, routingPersistFileName)
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatalf("write empty file failed: %v", err)
	}

	loaded, err := GetRoutingApplyStore().LoadPersisted(testSnapshotBuilder)
	if err == nil || loaded {
		t.Fatalf("LoadPersisted() = (%v, %v), want (false, error) for empty file", loaded, err)
	}
	if GetRoutingApplyStore().ActiveCanonicalSchema() != nil {
		t.Fatal("store must stay empty when persisted file is empty")
	}
}

func TestApplyFailureDoesNotOverwritePersistedFile(t *testing.T) {
	isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	mustApplyCanonical(t, []byte(toSocksDisabledCanonical))
	goodRaw, err := os.ReadFile(persistedFilePath(t))
	if err != nil {
		t.Fatalf("read persisted file failed: %v", err)
	}

	// 非法 payload（disabled_target_branch）：Apply 失败不得触碰已落盘内容
	invalid := []byte(`{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": true, "upstream": {"type": "socks"}}
		},
		"routing": {"rules": [
			{"id":"r1","type":"domain","condition":"example.com","enabled":true,"target":"toAliangX"}
		], "default_egress": "direct"}
	}`)
	if _, err := GetRoutingApplyStore().Apply(invalid, testSnapshotBuilder); err == nil {
		t.Fatal("Apply(invalid) error = nil, want validation failure")
	}

	currentRaw, err := os.ReadFile(persistedFilePath(t))
	if err != nil {
		t.Fatalf("read persisted file after failed apply failed: %v", err)
	}
	if string(currentRaw) != string(goodRaw) {
		t.Fatalf("persisted file changed after failed Apply:\nbefore=%s\nafter=%s", goodRaw, currentRaw)
	}
}

func TestLoadPersisted_DoesNotOverrideActiveState(t *testing.T) {
	stateDir := isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	// 磁盘上是 toSocks=false 的旧状态
	if err := os.WriteFile(filepath.Join(stateDir, routingPersistFileName), []byte(toSocksDisabledCanonical), 0o666); err != nil {
		t.Fatalf("write persisted file failed: %v", err)
	}

	// 内存里已有较新的运行态（toSocks=true）：加载不得回退
	mustApplyCanonical(t, []byte(`{
		"version": 1,
		"ingress": {"mode": "http"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": true, "upstream": {"type": "socks"}}
		},
		"routing": {"rules": [], "default_egress": "direct"}
	}`))

	loaded, err := GetRoutingApplyStore().LoadPersisted(testSnapshotBuilder)
	if err != nil {
		t.Fatalf("LoadPersisted() error = %v", err)
	}
	if !loaded {
		t.Fatal("LoadPersisted() loaded = false, want true when store already active")
	}

	active := GetRoutingApplyStore().ActiveCanonicalSchema()
	if active == nil || !active.Egress.ToSocks.Enabled || active.Ingress.Mode != "http" {
		t.Fatalf("active state was overridden by disk: %+v", active)
	}
}

func TestLoadPersistedIntoStore_NoFileIsSilent(t *testing.T) {
	isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	if err := LoadPersistedIntoStore(testSnapshotBuilder); err != nil {
		t.Fatalf("LoadPersistedIntoStore() error = %v, want nil for missing file", err)
	}
	if GetRoutingApplyStore().ActiveCanonicalSchema() != nil {
		t.Fatal("store must stay empty when no persisted file")
	}
}

func TestLoadPersistedIntoStore_CorruptedFileWarnsNotFatal(t *testing.T) {
	stateDir := isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	if err := os.WriteFile(filepath.Join(stateDir, routingPersistFileName), []byte("not-json"), 0o666); err != nil {
		t.Fatalf("write corrupted file failed: %v", err)
	}

	// 失败须可被调用方忽略（Warn 不阻断）：返回 error 但不 panic
	_ = LoadPersistedIntoStore(testSnapshotBuilder)

	if GetRoutingApplyStore().ActiveCanonicalSchema() != nil {
		t.Fatal("store must stay empty when persisted file is corrupted")
	}
}

func TestActiveEgressSwitches_EmptyStoreReturnsNotOK(t *testing.T) {
	ResetRoutingApplyStoreForTest()

	_, _, ok := GetRoutingApplyStore().ActiveEgressSwitches()
	if ok {
		t.Fatal("ActiveEgressSwitches() ok = true, want false for empty store")
	}
}

func TestApplyPersistsRuleListRoundTrip(t *testing.T) {
	isolateRoutingPersistence(t)
	ResetRoutingApplyStoreForTest()

	mustApplyCanonical(t, []byte(`{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": true, "upstream": {"type": "http"}}
		},
		"routing": {"rules": [
			{"id":"ai_0","type":"domain","condition":"api.openai.com","enabled":true,"target":"toAliang"},
			{"id":"px_0","type":"domain","condition":"example.com","enabled":true,"target":"toSocks"}
		], "default_egress": "direct"}
	}`))

	ResetRoutingApplyStoreForTest()
	loaded, err := GetRoutingApplyStore().LoadPersisted(testSnapshotBuilder)
	if err != nil || !loaded {
		t.Fatalf("LoadPersisted() = (%v, %v), want (true, nil)", loaded, err)
	}

	restored := GetRoutingApplyStore().ActiveCanonicalSchema()
	if restored == nil {
		t.Fatal("restored canonical is nil")
	}
	if len(restored.Routing.Rules) != 2 {
		t.Fatalf("restored rules = %d, want 2", len(restored.Routing.Rules))
	}
	if restored.Egress.ToSocks.Upstream.Type != "http" {
		t.Fatalf("restored upstream type = %q, want http", restored.Egress.ToSocks.Upstream.Type)
	}
}
