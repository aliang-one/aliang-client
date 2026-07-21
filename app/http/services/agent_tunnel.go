package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tunnel"
	"aliang.one/nursorgate/common/logger"
)

func (s *AgentService) configureTunnel(msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := remoteString(msg, "request_id")
	deviceID := remoteString(msg, "device_id")
	expectedDeviceID := s.currentDeviceID()
	if requestID == "" {
		emitTunnelConfigureError(writeJSON, "", errors.New("request_id is required"))
		return
	}
	if deviceID == "" || deviceID != expectedDeviceID {
		emitTunnelConfigureError(writeJSON, requestID, errors.New("tunnel configuration targets another device"))
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, remoteString(msg, "expires_at"))
	if err != nil {
		emitTunnelConfigureError(writeJSON, requestID, errors.New("expires_at must be an RFC3339 timestamp"))
		return
	}
	if s.tunnel == nil {
		emitTunnelConfigureError(writeJSON, requestID, errors.New("tunnel manager is unavailable"))
		return
	}

	status, changed, err := s.tunnel.Configure(tunnel.Config{
		DeviceID:        deviceID,
		PikoUpstreamURL: remoteString(msg, "piko_upstream_url"),
		TunnelToken:     remoteString(msg, "tunnel_token"),
		RoutePublicKey:  remoteString(msg, "route_public_key"),
		ExpiresAt:       expiresAt,
	})
	if err != nil {
		emitTunnelConfigureError(writeJSON, requestID, err)
		return
	}
	if status.State != "connected" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		status, err = s.tunnel.WaitConnected(ctx, deviceID)
		if err != nil {
			emitTunnelConfigureError(writeJSON, requestID, err)
			return
		}
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventTunnelConfigured,
		"request_id": requestID,
		"device_id":  status.DeviceID,
		"state":      status.State,
		"changed":    changed,
	})
}

func (s *AgentService) emitTunnelStatus(status tunnel.Status) {
	writer := s.currentRemoteWriter()
	if writer == nil {
		return
	}
	payload := map[string]interface{}{
		"type":      models.AgentEventTunnelStatus,
		"device_id": status.DeviceID,
		"state":     status.State,
	}
	if message := strings.TrimSpace(status.Error); message != "" {
		payload["error"] = message
		logger.Warn("[AGENT-TUNNEL] state=failed")
	}
	_ = writer(payload)
}

func emitTunnelConfigureError(writeJSON func(interface{}) error, requestID string, err error) {
	payload := map[string]interface{}{
		"type":  models.AgentEventTunnelError,
		"error": err.Error(),
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	_ = writeJSON(payload)
}
