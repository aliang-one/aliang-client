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
}

func TestMeshValidateFallbackHostKeyRequiredWhenAddrSet(t *testing.T) {
	on := true
	c := &MeshConfig{Fallback: &MeshFallbackConfig{
		Enable: &on, Addr: "192.168.1.20:56432", HostKey: "",
	}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "fallback.host_key required") {
		t.Fatalf("want fallback.host_key required, got %v", err)
	}
	c.Fallback.HostKey = "SHA256:x"
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

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
