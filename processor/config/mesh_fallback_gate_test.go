package config

// MeshFallbackActiveForClass 共享门控助手测试(计划 Task 12 评审钉死的三
// 消费方统一抽取):EffectiveEnabled 总开关 + Fallback.Enable + class ∈
// Classes + Addr 非空,四条件缺一不可。tcp 兜底闸与 egress 闸替换为调用
// 本助手后,既有包内回归见各自包测试;此处钉助手自身的语义边界。

import (
	"testing"

	"aliang.one/nursorgate/outbound/meshproto"
)

func boolPtrForMeshGate(v bool) *bool { return &v }

// meshGateConfig 构造给定总开关/兜底配置的最小 Config。
func meshGateConfig(master *bool, fb *MeshFallbackConfig) *Config {
	return &Config{Customer: &CustomerConfig{
		Mesh: &MeshConfig{Enable: master, Fallback: fb},
	}}
}

func aiFallbackEnabled() *MeshFallbackConfig {
	return &MeshFallbackConfig{
		Enable:  boolPtrForMeshGate(true),
		Classes: []string{meshproto.ClassAI, meshproto.ClassCustom},
		Addr:    "192.168.1.9:56440",
	}
}

// TestMeshFallbackActiveForClassPositive 四条件齐备(总开关默认 nil=true)
// → 返回规整后的兜底地址。
func TestMeshFallbackActiveForClassPositive(t *testing.T) {
	addr, ok := MeshFallbackActiveForClass(meshGateConfig(nil, aiFallbackEnabled()), meshproto.ClassAI)
	if !ok {
		t.Fatal("四条件齐备应激活")
	}
	if addr != "192.168.1.9:56440" {
		t.Fatalf("addr = %q, want 192.168.1.9:56440", addr)
	}
	// 另一勾选类别同样命中。
	if _, ok := MeshFallbackActiveForClass(meshGateConfig(nil, aiFallbackEnabled()), meshproto.ClassCustom); !ok {
		t.Fatal("Classes 含 custom 应命中")
	}
}

// TestMeshFallbackActiveForClassRespectsMasterSwitch mesh 总开关显式 false +
// fallback.enable=true → helper 返回 false。这是 tcp 侧兜底闸既有缺口
// (handler meshFallbackAddrForClass 原实现无 EffectiveEnabled 检查)修复的
// 钉子:总开关契约穿透所有 mesh 消费方。
func TestMeshFallbackActiveForClassRespectsMasterSwitch(t *testing.T) {
	addr, ok := MeshFallbackActiveForClass(meshGateConfig(boolPtrForMeshGate(false), aiFallbackEnabled()), meshproto.ClassAI)
	if ok {
		t.Fatal("mesh 总开关显式 false 时兜底门必须关闭")
	}
	if addr != "" {
		t.Fatalf("关闭时 addr = %q, want 空", addr)
	}
}

// TestMeshFallbackActiveForClassGateVariants 其余门控变体:Enable 显式
// false / 类别未勾选 / Addr 空 / 配置缺层。
func TestMeshFallbackActiveForClassGateVariants(t *testing.T) {
	t.Run("兜底Enable显式false", func(t *testing.T) {
		fb := aiFallbackEnabled()
		fb.Enable = boolPtrForMeshGate(false)
		if _, ok := MeshFallbackActiveForClass(meshGateConfig(nil, fb), meshproto.ClassAI); ok {
			t.Fatal("Enable=false 应关闭")
		}
	})
	t.Run("类别未勾选", func(t *testing.T) {
		fb := aiFallbackEnabled()
		fb.Classes = []string{meshproto.ClassSelf}
		if _, ok := MeshFallbackActiveForClass(meshGateConfig(nil, fb), meshproto.ClassAI); ok {
			t.Fatal("Classes 不含 ai 应关闭")
		}
	})
	t.Run("Addr空", func(t *testing.T) {
		fb := aiFallbackEnabled()
		fb.Addr = "   "
		if _, ok := MeshFallbackActiveForClass(meshGateConfig(nil, fb), meshproto.ClassAI); ok {
			t.Fatal("Addr 空白应关闭")
		}
	})
	t.Run("配置缺层", func(t *testing.T) {
		if _, ok := MeshFallbackActiveForClass(nil, meshproto.ClassAI); ok {
			t.Fatal("nil cfg 应关闭")
		}
		if _, ok := MeshFallbackActiveForClass(&Config{}, meshproto.ClassAI); ok {
			t.Fatal("nil Customer 应关闭")
		}
		if _, ok := MeshFallbackActiveForClass(&Config{Customer: &CustomerConfig{}}, meshproto.ClassAI); ok {
			t.Fatal("nil Mesh 应关闭")
		}
		if _, ok := MeshFallbackActiveForClass(meshGateConfig(nil, nil), meshproto.ClassAI); ok {
			t.Fatal("nil Fallback 应关闭")
		}
	})
}
