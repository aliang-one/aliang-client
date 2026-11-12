package config

import (
	"strings"
	"testing"
)

func TestMeshValidateHostKeyRequired(t *testing.T) {
	c := &MeshConfig{Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "   "}}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "host_key required") {
		t.Fatalf("want host_key required, got %v", err)
	}
}

func TestMeshValidateHostKeyTrimmed(t *testing.T) {
	// 计划原文 fixture 无 PrivateKey;但 Validate 要求「peers 配置了就必须有
	// private_key」(TestMeshValidatePrivateKeyRequired 钉死),不补这个字段
	// 本测试会以 private_key required 意外失败。
	c := &MeshConfig{PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: " SHA256:abc "}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.Peers[0].HostKey != "SHA256:abc" {
		t.Fatalf("host_key not trimmed in place: %q", c.Peers[0].HostKey)
	}
}

func TestMeshValidatePrivateKeyRequired(t *testing.T) {
	c := &MeshConfig{Enable: meshBoolPtr(true), Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "SHA256:abc"}}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "private_key required") {
		t.Fatalf("want private_key required, got %v", err)
	}
	c.PrivateKey = " "
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "private_key required") {
		t.Fatalf("trim-only blank must still fail with private_key required, got %v", err)
	}
	// 规整先于必填的又一实证:peers 非空时 " k " 经 TrimSpace 变 "k",
	// 规整后非空故不误触 required,且规整值就地写回。
	c.PrivateKey = " k "
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.PrivateKey != "k" {
		t.Fatalf("private_key not trimmed in place: %q", c.PrivateKey)
	}
}

// (旧 fallback.host_key 必填测试已随出口链路改造移除:next_hop 校验测试
// 见 mesh_fallback_gate_test.go TestMeshValidateFallbackNextHopRequired。)

func TestMeshValidatePassphraseTrimmed(t *testing.T) {
	c := &MeshConfig{PrivateKey: "k", KeyPassphrase: " p "}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.KeyPassphrase != "p" {
		t.Fatalf("passphrase not trimmed: %q", c.KeyPassphrase)
	}
}

// meshBoolPtr 避免与 types_test.go 既有的 boolPtr 重名(同包编译冲突)。
func meshBoolPtr(b bool) *bool { return &b }
