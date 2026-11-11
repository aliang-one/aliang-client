package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
	"aliang.one/nursorgate/processor/config"
)

// pickFreeUDPPort 取一个当前空闲的 UDP 端口(收包注入用;bind 竞态由
// Start 失败兜底,此处单实例一次绑定,重试无必要)。
func pickFreeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatalf("pickFreeUDPPort: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// TestLANPeersHandlerReturnsCandidates handler 装配 beacon 后 GET 返回
// 候选:common.Success 信封 + data.peers[] 结构(device_id/mesh_port/
// cert_fingerprint/last_seen_unix)且按 last_seen 降序。
func TestLANPeersHandlerReturnsCandidates(t *testing.T) {
	port := pickFreeUDPPort(t)
	b := lanbeacon.NewBeacon("self-dev", 56441, port)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("beacon start: %v", err)
	}
	t.Cleanup(b.Stop)

	now := time.Now().Unix()
	inject := func(deviceID string, meshPort int, ts int64) {
		conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		data, _ := json.Marshal(map[string]any{
			"device_id":        deviceID,
			"mesh_port":        meshPort,
			"cert_fingerprint": "",
			"ts":               ts,
		})
		if _, err := conn.Write(data); err != nil {
			t.Fatalf("inject %s: %v", deviceID, err)
		}
	}
	inject("dev-old", 41001, now-1)
	inject("dev-new", 41002, now)

	h := &MeshHandler{beacon: func() *lanbeacon.Beacon { return b }}
	req := httptest.NewRequest(http.MethodGet, "/api/mesh/lan_peers", nil)

	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Peers []lanbeacon.LanPeerCandidate `json:"peers"`
		} `json:"data"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		rec := httptest.NewRecorder()
		h.HandleLANPeers(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET code = %d, want 200", rec.Code)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
		}
		if len(resp.Data.Peers) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("候选未就位: %+v", resp.Data)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if resp.Code != 0 || resp.Msg != "success" {
		t.Fatalf("信封 code/msg = %d/%q", resp.Code, resp.Msg)
	}
	if len(resp.Data.Peers) != 2 {
		t.Fatalf("peers = %d 条, want 2", len(resp.Data.Peers))
	}
	// last_seen 降序:新报文在前。
	newP, oldP := resp.Data.Peers[0], resp.Data.Peers[1]
	if newP.DeviceID != "dev-new" || newP.MeshPort != 41002 || newP.CertFingerprint != "" || newP.LastSeenUnix != now {
		t.Fatalf("dev-new 字段不符: %+v", newP)
	}
	if oldP.DeviceID != "dev-old" || oldP.MeshPort != 41001 || oldP.LastSeenUnix != now-1 {
		t.Fatalf("dev-old 字段不符: %+v", oldP)
	}
}

// TestLANPeersHandlerNilBeaconEmpty 未装配 beacon(Default()==nil)时返回
// 空数组 peers:[],不 500——离线仪表盘点选面板拿空表是合法态。
func TestLANPeersHandlerNilBeaconEmpty(t *testing.T) {
	h := NewMeshHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/mesh/lan_peers", nil)
	rec := httptest.NewRecorder()
	h.HandleLANPeers(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Peers []lanbeacon.LanPeerCandidate `json:"peers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Peers == nil || len(resp.Data.Peers) != 0 {
		t.Fatalf("未装配 beacon 时 peers = %#v, want 空数组", resp.Data.Peers)
	}
}

// TestLANPeersHandlerMethodNotAllowed 非 GET 405(同 HandlePeerGates 的
// http.Error 惯例)。
func TestLANPeersHandlerMethodNotAllowed(t *testing.T) {
	h := NewMeshHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/lan_peers", nil)
	rec := httptest.NewRecorder()
	h.HandleLANPeers(rec, req)
	if rec.Code != 405 {
		t.Fatalf("POST code = %d, want 405", rec.Code)
	}
}

// TestMeshStatusHandlerAggregatesState GET /api/mesh/status 聚合:本机
// device_id、总开关/forward_enable 现值、lan 监听口端口、peers、beacon 候选
// (Task 14 仪表盘组网页数据源;写路径走既有 /api/config/customer,本端点只读)。
func TestMeshStatusHandlerAggregatesState(t *testing.T) {
	forwardOn := true
	meshCfg := &config.MeshConfig{
		ForwardEnable: &forwardOn,
		Peers: []config.MeshPeer{
			{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56441"}},
		},
	}
	h := &MeshHandler{
		beacon: func() *lanbeacon.Beacon { return nil }, // 候选面由 lan_peers 用例钉,此处 nil=空表
		config: func() *config.Config {
			return &config.Config{Customer: &config.CustomerConfig{Mesh: meshCfg}}
		},
		deviceID: func() string { return "dev-a" },
		lanPort:  func() int { return 56432 }, // 统一口承载(MeshLANPort 常量化,假缝同步)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil)
	rec := httptest.NewRecorder()
	h.HandleStatus(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET code = %d, want 200", rec.Code)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			DeviceID      string                       `json:"device_id"`
			Enable        bool                         `json:"enable"`
			ForwardEnable bool                         `json:"forward_enable"`
			LANListenPort int                          `json:"lan_listen_port"`
			Peers         []config.MeshPeer            `json:"peers"`
			LANPeers      []lanbeacon.LanPeerCandidate `json:"lan_peers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("信封 code = %d", resp.Code)
	}
	d := resp.Data
	if d.DeviceID != "dev-a" || !d.Enable || !d.ForwardEnable || d.LANListenPort != 56432 {
		t.Fatalf("标量字段不符: %+v", d)
	}
	if len(d.Peers) != 1 || d.Peers[0].DeviceID != "dev-b" || len(d.Peers[0].LANAddrs) != 1 {
		t.Fatalf("peers = %+v", d.Peers)
	}
	if d.LANPeers == nil || len(d.LANPeers) != 0 {
		t.Fatalf("未装配 beacon 时 lan_peers = %#v, want 空数组", d.LANPeers)
	}
}

// TestMeshStatusHandlerUnconfiguredDefaults 未配置 mesh 层的合法态:总开关
// EffectiveEnabled(nil=默认开)、forward_enable 默认关、peers/lan_peers 空数组、
// lan 端口 0(未装配)——离线面板拿空表现状,不 5xx。
func TestMeshStatusHandlerUnconfiguredDefaults(t *testing.T) {
	h := &MeshHandler{
		beacon:   func() *lanbeacon.Beacon { return nil },
		config:   func() *config.Config { return nil },
		deviceID: func() string { return "" },
		lanPort:  func() int { return 0 },
	}
	req := httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil)
	rec := httptest.NewRecorder()
	h.HandleStatus(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var resp struct {
		Data struct {
			Enable        bool              `json:"enable"`
			ForwardEnable bool              `json:"forward_enable"`
			LANListenPort int               `json:"lan_listen_port"`
			Peers         []config.MeshPeer `json:"peers"`
			LANPeers      []interface{}     `json:"lan_peers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Data.Enable || resp.Data.ForwardEnable || resp.Data.LANListenPort != 0 {
		t.Fatalf("未配置默认值不符: %+v", resp.Data)
	}
	if resp.Data.Peers == nil || len(resp.Data.Peers) != 0 {
		t.Fatalf("peers = %#v, want 空数组", resp.Data.Peers)
	}
	if resp.Data.LANPeers == nil {
		t.Fatal("lan_peers 为 null, want 空数组")
	}
}

// meshStatusEffectiveProbe 是 effective 字段测试的响应解码面(只取两个开关
// 字段;其余标量由 Aggregate/Unconfigured 用例钉)。
type meshStatusEffectiveProbe struct {
	Code int `json:"code"`
	Data struct {
		Enable    bool `json:"enable"`
		Effective bool `json:"effective"`
	} `json:"data"`
}

func meshStatusEffective(t *testing.T, cfg *config.Config) (enable, effective bool) {
	t.Helper()
	h := &MeshHandler{
		beacon:   func() *lanbeacon.Beacon { return nil },
		config:   func() *config.Config { return cfg },
		deviceID: func() string { return "" },
		lanPort:  func() int { return 0 },
	}
	rec := httptest.NewRecorder()
	h.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mesh/status", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var resp meshStatusEffectiveProbe
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return resp.Data.Enable, resp.Data.Effective
}

func meshStatusTestBool(v bool) *bool { return &v }

// TestMeshStatusHandlerEffectiveFollowsSSHIngress effective 字段两向钉 +
// 守卫出位回归(SPEC §10.3 依赖降级一处生效的前端观感面):
//   - mesh 开 + ssh 开 → enable=true 且 effective=true(生效);
//   - mesh 开 + ssh 关 → enable=true 但 effective=false(依赖降级,前端凭它
//     区分「开关开了但 SSH 入口关着导致 mesh 未生效」);
//   - 回归钉:mesh 键缺失(Customer.Mesh=nil,合法态)+ ssh 开 → effective
//     必须 true——Effective 的守卫只有 cfg 非 nil(EffectiveEnabled 对 nil
//     mesh 安全);曾误锁进 Mesh!=nil 守卫时本向恒 false,制造「已生效却被
//     报未生效」的反向误判。
func TestMeshStatusHandlerEffectiveFollowsSSHIngress(t *testing.T) {
	meshOnSSHOn := &config.Config{Customer: &config.CustomerConfig{
		Mesh:       &config.MeshConfig{Enable: meshStatusTestBool(true)},
		SSHIngress: &config.SSHIngressConfig{Enable: meshStatusTestBool(true)},
	}}
	enable, effective := meshStatusEffective(t, meshOnSSHOn)
	if !enable || !effective {
		t.Fatalf("mesh开+ssh开: enable=%v effective=%v, want true/true", enable, effective)
	}

	meshOnSSHOff := &config.Config{Customer: &config.CustomerConfig{
		Mesh:       &config.MeshConfig{Enable: meshStatusTestBool(true)},
		SSHIngress: &config.SSHIngressConfig{Enable: meshStatusTestBool(false)},
	}}
	enable, effective = meshStatusEffective(t, meshOnSSHOff)
	if !enable || effective {
		t.Fatalf("mesh开+ssh关: enable=%v effective=%v, want true/false", enable, effective)
	}

	meshNilSSHOn := &config.Config{Customer: &config.CustomerConfig{
		SSHIngress: &config.SSHIngressConfig{Enable: meshStatusTestBool(true)},
	}}
	enable, effective = meshStatusEffective(t, meshNilSSHOn)
	if !enable || !effective {
		t.Fatalf("mesh键缺失+ssh开: enable=%v effective=%v, want true/true(守卫出位回归)", enable, effective)
	}
}

// TestMeshStatusHandlerMethodNotAllowed 非 GET 405。
func TestMeshStatusHandlerMethodNotAllowed(t *testing.T) {
	h := NewMeshHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/status", nil)
	rec := httptest.NewRecorder()
	h.HandleStatus(rec, req)
	if rec.Code != 405 {
		t.Fatalf("POST code = %d, want 405", rec.Code)
	}
}
