package services

import (
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 组网诊断读面批 Task 6:mesh 身份只读访问器直测。
// 铁律=两访问器真零副作用(无生成路径):StateDeviceIDReadonly 绝不触发
// ensureDeviceIdentityLocked(CurrentDeviceID 的文档措辞陷阱),身份缺失返回
// 空串且盘上不留痕。mismatches 判定逻辑经 handler seam 注入测试(handlers
// 包),不依赖真实文件系统;本文件只钉访问器三态与「不生成」反证。
// ---------------------------------------------------------------------------

// TestLoadAgentIdentityFileReadonlyMissing 文件缺失 → ("", "")(路径本身是
// 诊断信息,缺失=空串,消费方以空判「无常驻身份文件」)。
func TestLoadAgentIdentityFileReadonlyMissing(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	path, deviceID := LoadAgentIdentityFileReadonly()
	if path != "" || deviceID != "" {
		t.Fatalf("缺失态 = (%q, %q), want (\"\", \"\")", path, deviceID)
	}
}

// TestLoadAgentIdentityFileReadonlyPresent 文件在册 → (路径, 文件内
// device_id);路径与 agentStatePath 同目录同款分叉(缓存态单进程)。
func TestLoadAgentIdentityFileReadonlyPresent(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	identityPath := agentIdentityFilePathForTest(t)
	if err := os.WriteFile(identityPath, []byte(`{"device_id":"dev-file-1","created_at":"2026-11-20T08:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	path, deviceID := LoadAgentIdentityFileReadonly()
	if deviceID != "dev-file-1" {
		t.Fatalf("deviceID = %q, want dev-file-1", deviceID)
	}
	if path != identityPath {
		t.Fatalf("path = %q, want %q", path, identityPath)
	}
}

// TestLoadAgentIdentityFileReadonlyTrimsWhitespace device_id 做 TrimSpace
// 归一(比对用,空白值视同空,防比对面伪 mismatch)。
func TestLoadAgentIdentityFileReadonlyTrimsWhitespace(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	identityPath := agentIdentityFilePathForTest(t)
	if err := os.WriteFile(identityPath, []byte(`{"device_id":"  dev-pad  "}`), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	if _, deviceID := LoadAgentIdentityFileReadonly(); deviceID != "dev-pad" {
		t.Fatalf("deviceID = %q, want dev-pad(已归一)", deviceID)
	}
}

// TestLoadAgentIdentityFileReadonlyCorrupt 解析败 → (路径, "")(文件在册
// 是事实,device_id 不可得=空串,两个信号都留给消费方)。
func TestLoadAgentIdentityFileReadonlyCorrupt(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	identityPath := agentIdentityFilePathForTest(t)
	if err := os.WriteFile(identityPath, []byte(`{not-json`), 0o600); err != nil {
		t.Fatalf("write corrupt identity file: %v", err)
	}
	path, deviceID := LoadAgentIdentityFileReadonly()
	if path != identityPath || deviceID != "" {
		t.Fatalf("解析败态 = (%q, %q), want (%q, \"\")", path, deviceID, identityPath)
	}
}

// TestStateDeviceIDReadonlyNoGenerate 「不生成」反证:全新环境(无 state/
// 无 identity 文件)调 StateDeviceIDReadonly → 空串,且盘上不落 identity
// 文件、state 文件也不新增——这正是 CurrentDeviceID 反向陷阱(读路径铸新
// 身份)的守门断言。
func TestStateDeviceIDReadonlyNoGenerate(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	svc := NewAgentService()
	if got := svc.StateDeviceIDReadonly(); got != "" {
		t.Fatalf("空环境 StateDeviceIDReadonly = %q, want 空串(不生成)", got)
	}
	identityPath := agentIdentityFilePathForTest(t)
	if _, err := os.Stat(identityPath); !os.IsNotExist(err) {
		t.Fatalf("只读调用后 identity 文件不应存在: %v", err)
	}
	// state 文件同样不应被写(ensureDeviceIdentityLocked 才会 saveStateLocked)。
	statePath, err := agentStatePath()
	if err != nil {
		t.Fatalf("agentStatePath: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("只读调用后 state 文件不应存在: %v", err)
	}
}

// TestStateDeviceIDReadonlyReadsState 持锁读 state.DeviceID:种入值 → 原样
// 读回;TrimSpace 归一同身份文件侧。
func TestStateDeviceIDReadonlyReadsState(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	svc := NewAgentService()
	svc.mu.Lock()
	svc.state.DeviceID = " dev-state-9 "
	svc.mu.Unlock()
	if got := svc.StateDeviceIDReadonly(); got != "dev-state-9" {
		t.Fatalf("StateDeviceIDReadonly = %q, want dev-state-9", got)
	}
}

// TestLoadAgentIdentityFileReadonlyValidJSONNoSideEffect 补一刀:文件内容
// 是合法 JSON 但 device_id 为空 → (路径, ""),且不触发生成(与
// loadAgentDeviceIdentity 零值语义同款)。
func TestLoadAgentIdentityFileReadonlyValidJSONNoSideEffect(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	identityPath := agentIdentityFilePathForTest(t)
	if err := os.WriteFile(identityPath, []byte(`{"device_id":""}`), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	path, deviceID := LoadAgentIdentityFileReadonly()
	if path != identityPath || deviceID != "" {
		t.Fatalf("空 device_id 态 = (%q, %q), want (%q, \"\")", path, deviceID, identityPath)
	}
	// 返回值结构自查:不误报路径为空导致消费方把「文件在但 id 空」误读成
	// 「无文件」。
	if strings.TrimSpace(path) == "" {
		t.Fatal("文件在册时 path 不应为空")
	}
}
