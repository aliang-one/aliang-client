// Package meshproto 实现 mesh 转发帧协议:首行 JSON 头 + 原始字节。
// 复刻 app/tunnel/bridge.go tcpRouteHeader 的「一行 JSON + 透传」风格(design §3.1)。
package meshproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// 流量类别(design §4)
const (
	ClassSelf   = "self"   // 自身流量:LAN + 中继
	ClassAI     = "ai"     // AI 流量:LAN + 中继
	ClassCustom = "custom" // 用户自定义:仅 LAN
)

const (
	CarrierLAN   = "lan"
	CarrierRelay = "relay"
)

// 跳数上限:默认 5、硬上限 8,可配置项由 config 层 clamp 到 [1,8](design §2 行7)。
const (
	DefaultHops = 5
	MaxHopsCap  = 8
)

// MaxHeaderBytes 防畸形超大首行(参考 bridge.go readHeader 的 4096 上限)。
const MaxHeaderBytes = 4096

type Header struct {
	From   string   `json:"from"`   // 发起节点 device_id(链头)
	Class  string   `json:"class"`  // self|ai|custom
	Target string   `json:"target"` // host:port / 裸域名 / "dns:"
	Via    []string `json:"via"`    // path vector:已访 device_id(含链头)
	Hops   int      `json:"hops"`   // 已转发跳数
}

func (h *Header) Encode() (string, error) {
	if h.From == "" || h.Class == "" || h.Target == "" {
		return "", errors.New("meshproto: from/class/target required")
	}
	if h.Via == nil {
		h.Via = []string{h.From}
	}
	b, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("meshproto: marshal: %w", err)
	}
	if len(b)+1 > MaxHeaderBytes {
		return "", fmt.Errorf("meshproto: header too large (%d bytes)", len(b))
	}
	return string(b) + "\n", nil
}

// Parse 从流首解析一行 JSON 头,consumed 为含换行的字节数;余下字节为原始流量。
func Parse(data []byte) (*Header, int, error) {
	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		if len(data) > MaxHeaderBytes {
			return nil, 0, fmt.Errorf("meshproto: header exceeds %d bytes without newline", MaxHeaderBytes)
		}
		return nil, 0, errors.New("meshproto: header line incomplete")
	}
	if idx+1 > MaxHeaderBytes {
		return nil, 0, fmt.Errorf("meshproto: header too large (%d bytes)", idx+1)
	}
	var h Header
	if err := json.Unmarshal(data[:idx], &h); err != nil {
		return nil, 0, fmt.Errorf("meshproto: bad header json: %w", err)
	}
	if h.From == "" || h.Class == "" || h.Target == "" {
		return nil, 0, errors.New("meshproto: from/class/target required")
	}
	if h.Via == nil {
		h.Via = []string{h.From}
	}
	return &h, idx + 1, nil
}

// AppendHop 由每一跳在转发前调用:自查环路 + 递增跳数(design §7 层1/层2)。
// maxHops 由调用方从 config clamp 后传入(EffectiveMaxHops)。
func (h *Header) AppendHop(selfDeviceID string, maxHops int) error {
	for _, id := range h.Via {
		if id == selfDeviceID {
			return fmt.Errorf("meshproto: loop detected via %s", selfDeviceID)
		}
	}
	if h.Hops >= maxHops {
		return fmt.Errorf("meshproto: hop limit %d reached", maxHops)
	}
	h.Via = append(h.Via, selfDeviceID)
	h.Hops++
	return nil
}

// ClassCarrierAllowed 载体准入:用户自定义流量禁走中继(design §4,引擎层硬约束)。
func ClassCarrierAllowed(class, carrier string) bool {
	if carrier == CarrierLAN {
		return true
	}
	return class == ClassSelf || class == ClassAI
}
