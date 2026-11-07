package services

// 设备门禁开关（peer_exec_enabled / peer_fetch_enabled）本地读写面。
//
// 真值源 = PhoneServer 设备注册表（spec 2026-10-29-device-peer-link-and-profile
// §5.2）：GET /api/devices 列表带旗标，PATCH /api/devices/:deviceId/settings
// 写（写通道服务端已落地，settings schema 双旗标 optional）。本机 = 自身
// deviceId（device_identity.json）；鉴权走 user-JWT + X-Aliang-Device-ID
// （doAgentServerJSON 同族，不触 callAgentServer 的 401 自愈链——凭据自愈
// 交给心跳/同步，避免把开关操作拖进重试风暴）。写后以 GET 回读为准（载荷
// 即真相，不依赖 PATCH 响应形状）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PeerGateStatus 返回本机设备的两枚门禁旗标。本机不在列表（未注册/刚被
// 摘除）时 found=false，由前端呈现「设备未同步」态而不是误报关。
func (s *AgentService) PeerGateStatus(ctx context.Context) (map[string]interface{}, error) {
	raw, err := s.doAgentServerJSON(ctx, http.MethodGet, agentPeerServerURL()+"/api/devices", nil)
	if err != nil {
		return nil, err
	}
	var devices []map[string]interface{}
	if err := json.Unmarshal(raw, &devices); err != nil {
		return nil, fmt.Errorf("device list response is not an array: %w", err)
	}
	selfID := strings.TrimSpace(s.currentDeviceID())
	if selfID == "" {
		return nil, errors.New("device identity unavailable")
	}
	for _, d := range devices {
		if peerAPIString(d, "id", "device_id") != selfID {
			continue
		}
		return map[string]interface{}{
			"device_id":          selfID,
			"found":              true,
			"peer_exec_enabled":  peerAPIBool(d, "peer_exec_enabled", "peerExecEnabled"),
			"peer_fetch_enabled": peerAPIBool(d, "peer_fetch_enabled", "peerFetchEnabled"),
		}, nil
	}
	return map[string]interface{}{"device_id": selfID, "found": false}, nil
}

// PeerGateUpdate 写本机门禁旗标（nil = 不动该键），成功后回读最新状态。
func (s *AgentService) PeerGateUpdate(ctx context.Context, exec, fetch *bool) (map[string]interface{}, error) {
	deviceID := strings.TrimSpace(s.currentDeviceID())
	if deviceID == "" {
		return nil, errors.New("device identity unavailable")
	}
	payload := map[string]interface{}{}
	if exec != nil {
		payload["peer_exec_enabled"] = *exec
	}
	if fetch != nil {
		payload["peer_fetch_enabled"] = *fetch
	}
	if len(payload) == 0 {
		return nil, errors.New("no gate flag in request")
	}
	endpoint := agentPeerServerURL() + "/api/devices/" + url.PathEscape(deviceID) + "/settings"
	if _, err := s.doAgentServerJSON(ctx, http.MethodPatch, endpoint, payload); err != nil {
		return nil, err
	}
	return s.PeerGateStatus(ctx)
}
