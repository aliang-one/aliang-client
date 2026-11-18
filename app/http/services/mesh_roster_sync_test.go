package services

// mesh roster 同步单测(自动组网批次):缓存读写/准入组合闸三段
// (auto_peers 开关→beacon 四要素→roster 核验)的排布语义钉。
// 组合闸本体在 tunnel.WireMeshAutoPeerProbe 闭包(依赖 config/tunnel 双
// 层,单测以 MeshAutoPeerFor nil 语义+探针镜像复刻排布);roster 缓存/
// 访问器在此直测。

import (
	"testing"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	"aliang.one/nursorgate/processor/config"
)

func TestMeshRosterEntryFor(t *testing.T) {
	meshRosterMu.Lock()
	meshRosterCache = map[string]meshRosterEntry{
		"dev-real": {PubKeyLine: "ssh-ed25519 AAAA_real", HostKeyFp: "SHA256:real"},
	}
	meshRosterMu.Unlock()
	t.Cleanup(func() { seedMeshRosterForTest(nil) })

	if pubLine, fp, ok := MeshRosterEntryFor("dev-real"); !ok || pubLine != "ssh-ed25519 AAAA_real" || fp != "SHA256:real" {
		t.Fatalf("命中条目字段不符: %q %q %v", pubLine, fp, ok)
	}
	if _, _, ok := MeshRosterEntryFor("dev-miss"); ok {
		t.Fatal("未命中应 false")
	}
}

// TestMeshAutoPeerForNoProbeConfig 探针未注入 = MeshAutoPeerFor 恒 false
// (config 层缝的 nil 语义——routing/tcp/delegate 三面 fail-closed 的根基)。
func TestMeshAutoPeerForNoProbeConfig(t *testing.T) {
	config.SetMeshAutoPeerProbe(nil)
	t.Cleanup(func() { config.SetMeshAutoPeerProbe(nil) })
	if _, ok := config.MeshAutoPeerFor("dev-any"); ok {
		t.Fatal("探针未注入应恒未命中")
	}
}

// TestAutoPeersViewPromotion 派生视图 promotion 语义(与
// lanbeacon.AutoPeers 同源;此处以 services 视角复验 beacon 侧调用路径的
// 数据形状——四要素闸的单元钉在 lanbeacon 包,此处只验装配投影)。
func TestAutoPeersViewPromotion(t *testing.T) {
	if got := lanbeacon.AutoPeers(); got == nil {
		t.Fatal("Default() 未装配时 AutoPeers 应返回空数组而非 nil(/api 序列化契约)")
	}
}
