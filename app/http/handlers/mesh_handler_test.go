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
