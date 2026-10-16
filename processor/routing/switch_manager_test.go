package routing

import (
	"testing"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/processor/config"
)

// isolateStateDir 把持久化文件指向独立临时目录（Apply 会落盘 routing.json，
// 本包无 TestMain 重定向，须逐测试隔离）。
func isolateStateDir(t *testing.T) {
	t.Helper()
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
}

func applyCanonicalRaw(t *testing.T, raw string) {
	t.Helper()
	if _, err := config.GetRoutingApplyStore().Apply([]byte(raw), func(cfg *config.CanonicalRoutingSchema) (any, error) {
		return cfg, nil
	}); err != nil {
		t.Fatalf("apply canonical failed: %v", err)
	}
}

// 决策层读源统一：store 有 canonical 时，开关状态必须从 egress 段派生，
// 而不是 SwitchManager 内存默认值。
func TestEffectiveSwitchStatus_DerivesFromStoreCanonical(t *testing.T) {
	isolateStateDir(t)
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(config.ResetRoutingApplyStoreForTest)

	applyCanonicalRaw(t, `{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": false},
			"toSocks": {"enabled": false, "upstream": {"type": "socks"}}
		},
		"routing": {"rules": [], "default_egress": "direct"}
	}`)

	status := EffectiveSwitchStatus()
	if status.AliangEnabled {
		t.Fatal("AliangEnabled = true, want false (derived from egress.toAliang)")
	}
	if status.SocksEnabled {
		t.Fatal("SocksEnabled = true, want false (derived from egress.toSocks)")
	}
}

// store 为空时回落 SwitchManager 内存态（true/true 默认）——即今天的行为。
func TestEffectiveSwitchStatus_FallsBackWhenStoreEmpty(t *testing.T) {
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(config.ResetRoutingApplyStoreForTest)

	status := EffectiveSwitchStatus()
	if !status.AliangEnabled || !status.SocksEnabled {
		t.Fatalf("EffectiveSwitchStatus() = %+v, want default true/true when store empty", status)
	}
}

// GetStatus 回显不撒谎：store 存在时回显 store 派生值。
func TestSwitchManagerGetStatus_DerivesFromStoreWhenPresent(t *testing.T) {
	isolateStateDir(t)
	config.ResetRoutingApplyStoreForTest()
	t.Cleanup(config.ResetRoutingApplyStoreForTest)

	applyCanonicalRaw(t, `{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": false, "upstream": {"type": "socks"}}
		},
		"routing": {"rules": [], "default_egress": "direct"}
	}`)

	status := GetSwitchManager().GetStatus()
	if !status.AliangEnabled {
		t.Fatal("GetStatus().AliangEnabled = false, want true from store")
	}
	if status.SocksEnabled {
		t.Fatal("GetStatus().SocksEnabled = true, want false from store")
	}
}
