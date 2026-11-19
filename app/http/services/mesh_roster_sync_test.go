package services

// mesh roster 同步单测(自动组网批次):缓存读写/准入组合闸三段
// (auto_peers 开关→beacon 四要素→roster 核验)的排布语义钉。
// 组合闸本体在 tunnel.WireMeshAutoPeerProbe 闭包(依赖 config/tunnel 双
// 层,单测以 MeshAutoPeerFor nil 语义+探针镜像复刻排布);roster 缓存/
// 访问器在此直测。

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestMeshRosterSyncStatusThreeStates 三态齐全(D2):从未同步(synced=false)
// / 同步过且健康(last_error 空)/ 同步过但上一轮失败(last_error 非空)。
// 末段再真走一次 fetchMeshRoster 成功 fetch(包内先例:直调
// callAgentServer+httptest,零外部依赖),钉死「成功轮清零 last_error」行
// ——否则该清零行零测试路径经过。
func TestMeshRosterSyncStatusThreeStates(t *testing.T) {
	t.Cleanup(func() {
		meshRosterMu.Lock()
		meshRosterCache = nil
		meshRosterFetchedAt = time.Time{}
		meshRosterLastError = ""
		meshRosterMu.Unlock()
	})

	// 态 1:从未同步(D1 未接线即此态)
	meshRosterMu.Lock()
	meshRosterCache = nil
	meshRosterFetchedAt = time.Time{}
	meshRosterLastError = ""
	meshRosterMu.Unlock()
	st := MeshRosterSyncStatus()
	if st.Synced || st.LastSyncAt != "" || st.LastError != "" {
		t.Fatalf("从未同步态应为全零: %+v", st)
	}

	// 态 2:同步过且健康
	seedMeshRosterForTest(map[string]meshRosterEntry{"dev-a": {PubKeyLine: "k", HostKeyFp: "f"}})
	st = MeshRosterSyncStatus()
	if !st.Synced || st.LastSyncAt == "" || st.LastError != "" {
		t.Fatalf("健康态应为 synced=true+时间+无错: %+v", st)
	}
	if _, err := time.Parse(time.RFC3339, st.LastSyncAt); err != nil {
		t.Fatalf("last_sync_at 应为 RFC3339: %v", err)
	}

	// 态 3:上一轮拉取失败(旧缓存仍在)。记账 helper 同包直调(零 HTTP
	// 依赖);先手工归零,保证非空是 helper 写入的功劳而非态 1 遗留。
	meshRosterMu.Lock()
	meshRosterLastError = ""
	meshRosterMu.Unlock()
	recordMeshRosterFetchError(errors.New("502 bad gateway"))
	st = MeshRosterSyncStatus()
	if !st.Synced || st.LastError == "" {
		t.Fatalf("失败态应为 synced=true+last_error 非空: %+v", st)
	}
	if st.LastError != "502 bad gateway" {
		t.Fatalf("last_error 应贯通记账原文: %q", st.LastError)
	}

	// 态 4(清零行钉扎):真走一次 fetchMeshRoster 成功 fetch——态 3 留下的
	// 错误必须被成功轮清零;响应字段带前导空白,顺带钉死清洗循环的 TrimSpace。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roster":[{"device_id":"dev-x","mesh_pub_key":" kx","host_key_fingerprint":" fx"}]}`))
	}))
	defer srv.Close()
	svc := &AgentService{client: &http.Client{Timeout: 2 * time.Second}}
	fetchMeshRoster(svc, &config.Config{Core: &config.CoreConfig{AgentServer: srv.URL}})
	st = MeshRosterSyncStatus()
	if !st.Synced || st.LastError != "" {
		t.Fatalf("成功轮应清零 last_error: %+v", st)
	}
	if pubLine, fp, ok := MeshRosterEntryFor("dev-x"); !ok || pubLine != "kx" || fp != "fx" {
		t.Fatalf("成功轮应落清洗后缓存: %q %q %v", pubLine, fp, ok)
	}
}
