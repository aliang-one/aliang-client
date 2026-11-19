package routes

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/handlers"
	"aliang.one/nursorgate/app/mesh/punch"
)

// TestMeshLANPeersRouteRegistered /api/mesh/lan_peers 由 RegisterRoutes
// 注册:非 404、200、信封 data.peers 为空数组(beacon 未装配时合法态)。
// 环境变量同 TestAgentVibeSessionRoutesRegistered(构造 Agents 服务侧隔离)。
func TestMeshLANPeersRouteRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	req := httptest.NewRequest(http.MethodGet, "/api/mesh/lan_peers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route /api/mesh/lan_peers returned 404 — not registered by RegisterRoutes")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("route code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Peers []json.RawMessage `json:"peers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Data.Peers == nil {
		t.Fatalf("未装配 beacon 时 peers 应为空数组而非 null (body=%s)", rec.Body.String())
	}
}

// TestMeshDiagRouteRegistered /api/mesh/diag 由 RegisterRoutes 注册(读面批
// Task 5):非 404、200、信封 code=0、恒数组非 null、运行态真值双布尔=false
// (未装配环境合法态)——验证 NewMeshDiagHandler 生产钉装配链(全缝生产源
// 在未配置环境零 panic 直答)。
func TestMeshDiagRouteRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	req := httptest.NewRequest(http.MethodGet, "/api/mesh/diag", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route /api/mesh/diag returned 404 — not registered by RegisterRoutes")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("route code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			StunServers []json.RawMessage `json:"stun_servers"`
			LANRunning  bool              `json:"lan_running"`
			Beacon      struct {
				LANPeers      []json.RawMessage `json:"lan_peers"`
				BeaconRunning bool              `json:"beacon_running"`
			} `json:"beacon"`
			Punch struct {
				Classify struct {
					SideA []json.RawMessage `json:"side_a"`
				} `json:"classify"`
				Sessions struct {
					Memo struct {
						SideA []json.RawMessage `json:"side_a"`
					} `json:"memo"`
				} `json:"sessions"`
			} `json:"punch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code = %d, want 0", resp.Code)
	}
	if resp.Data.LANRunning || resp.Data.Beacon.BeaconRunning {
		t.Fatalf("未装配环境运行态真值应 false: lan_running=%v beacon_running=%v",
			resp.Data.LANRunning, resp.Data.Beacon.BeaconRunning)
	}
	for name, v := range map[string][]json.RawMessage{
		"stun_servers":         resp.Data.StunServers,
		"beacon.lan_peers":     resp.Data.Beacon.LANPeers,
		"classify.side_a":      resp.Data.Punch.Classify.SideA,
		"sessions.memo.side_a": resp.Data.Punch.Sessions.Memo.SideA,
	} {
		if v == nil {
			t.Fatalf("%s = null, want 空数组 (body=%s)", name, rec.Body.String())
		}
	}
}

// TestMeshIdentityRouteRegistered /api/mesh/identity 由 RegisterRoutes 注册
// (读面批 Task 6):非 404、200、信封 code=0、mismatches 恒数组非 null;
// 未装配环境各源零值合法态(device_id/identity_file_path 空,roster 在册
// false)——验证 NewMeshDiagHandler 身份四缝生产钉装配链(真实单例/文件
// 读/beacon Default/MeshRosterEntryFor 全走一遍,HTTP 形状+生产缝装配零
// panic 直答;「不生成」反证在 services 包访问器直测钉)。
func TestMeshIdentityRouteRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	req := httptest.NewRequest(http.MethodGet, "/api/mesh/identity", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route /api/mesh/identity returned 404 — not registered by RegisterRoutes")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("route code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			DeviceID             string   `json:"device_id"`
			IdentityFilePath     string   `json:"identity_file_path"`
			IdentityFileDeviceID string   `json:"identity_file_device_id"`
			BeaconSelfDeviceID   string   `json:"beacon_self_device_id"`
			RosterSelfPresent    bool     `json:"roster_self_present"`
			Mismatches           []string `json:"mismatches"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code = %d, want 0", resp.Code)
	}
	if resp.Data.Mismatches == nil {
		t.Fatalf("mismatches = null, want 空数组 (body=%s)", rec.Body.String())
	}
	if resp.Data.RosterSelfPresent {
		t.Fatal("未装配环境 roster_self_present 应 false")
	}
}

// TestMeshStunProbeRouteRegistered /api/mesh/stun_probe 由 RegisterRoutes
// 注册(显式活探测批 Task 7):注入装配假 handler(照 NewMeshCoreForward
// HandlerWithBase 注入先例,真探测语义归 handlers 包自测)经真 RegisterRoutes
// 断双形态——GET 非 404(已注册)且 405(handler 方法闸真拒,非 200)、POST
// 腿(装配假缝)非 404 且 200+results 恒数组非 null(POST 腿漏注册/方法
// 前缀写错在此现形,双注册教训同族)。
func TestMeshStunProbeRouteRegistered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")

	h := NewHandlers()
	// 假缝的 conn 用 loopback 占位 socket(绝不发真 STUN 包:probe 缝为假)。
	loopback, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("占 loopback 口失败: %v", err)
	}
	t.Cleanup(func() { _ = loopback.Close() })
	h.MeshProbe = handlers.NewMeshStunProbeHandlerWithSeams(
		func() []string { return []string{"stun.smoke.test:3478"} },
		func() (net.PacketConn, error) { return loopback, nil },
		func(net.PacketConn, []string, time.Duration) punch.ClassifyResult {
			return punch.ClassifyResult{Results: []punch.StunProbeEntry{}}
		},
	)
	mux := http.NewServeMux()
	RegisterRoutes(h, mux)

	// GET:非 404=已注册;405=方法闸真拒(断 405 而非 200,顺带钉 method 闸)。
	req := httptest.NewRequest(http.MethodGet, "/api/mesh/stun_probe", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route /api/mesh/stun_probe returned 404 — not registered by RegisterRoutes")
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code = %d, want 405 (body=%s)", rec.Code, rec.Body.String())
	}

	// POST(零体,假缝直答):非 404=POST 腿真注册;200+results 恒数组非 null。
	req = httptest.NewRequest(http.MethodPost, "/api/mesh/stun_probe", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("route POST /api/mesh/stun_probe returned 404 — POST leg not registered")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("POST code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Results []json.RawMessage `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code = %d, want 0", resp.Code)
	}
	if resp.Data.Results == nil {
		t.Fatalf("假缝空结果 results = null, want 恒数组 (body=%s)", rec.Body.String())
	}
}
