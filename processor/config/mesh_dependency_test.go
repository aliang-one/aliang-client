package config

import "testing"

func meshCfgFor(on bool) *Config {
	sshOn := on
	return &Config{Customer: &CustomerConfig{
		Mesh:       &MeshConfig{Enable: &sshOn, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "dev-b", HostKey: "h"}}},
		SSHIngress: &SSHIngressConfig{Enable: &sshOn},
	}}
}

func TestMeshEffectivelyEnabledFollowsSSHIngress(t *testing.T) {
	if !meshCfgFor(true).MeshEffectivelyEnabled() {
		t.Fatal("both on → effective")
	}
	if meshCfgFor(false).MeshEffectivelyEnabled() {
		t.Fatal("ssh off → degraded")
	}
	var nilCfg *Config
	if nilCfg.MeshEffectivelyEnabled() {
		t.Fatal("nil config → false")
	}
}

func TestValidateMeshDependencyFourCombos(t *testing.T) {
	on, off := true, false
	mk := func(mesh, ssh *bool) *Config {
		return &Config{Customer: &CustomerConfig{
			Mesh:       &MeshConfig{Enable: mesh, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "d", HostKey: "h"}}},
			SSHIngress: &SSHIngressConfig{Enable: ssh},
		}}
	}
	if err := mk(&on, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh+ssh: %v", err)
	}
	if err := mk(&off, &off).ValidateMeshDependency(); err != nil {
		t.Fatalf("both off: %v", err)
	}
	if err := mk(&off, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh off: %v", err)
	}
	if err := mk(&on, &off).ValidateMeshDependency(); err == nil {
		t.Fatal("mesh on + ssh off must be rejected at save time")
	}
}

func TestMeshFallbackHostKey(t *testing.T) {
	on := true
	cfg := &Config{Customer: &CustomerConfig{Mesh: &MeshConfig{
		Fallback: &MeshFallbackConfig{Enable: &on, Addr: "1.2.3.4:56432", HostKey: " SHA256:x "},
	}}}
	if got := MeshFallbackHostKey(cfg); got != "SHA256:x" {
		t.Fatalf("want trimmed SHA256:x, got %q", got)
	}
	if got := MeshFallbackHostKey(nil); got != "" {
		t.Fatalf("nil cfg → empty, got %q", got)
	}
}
