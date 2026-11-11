package ssh

// meshkey_test.go — MeshKey 身份化(SPEC §10.2/§13.4):公钥 + 尾注释
// device_id 的解析/比较/标签回落。device_id 只进进程内 Permissions
// Extensions(extMeshDeviceID),不上线。

import (
	"net"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// stubConnMeta 是 publicKeyCallback 直测用的最小 ConnMetadata 桩(限速键
// 取 RemoteAddr,其余字段运行面不读)。
type stubConnMeta struct{}

func (stubConnMeta) User() string          { return "ingress" }
func (stubConnMeta) SessionID() []byte     { return []byte("test-session") }
func (stubConnMeta) ClientVersion() []byte { return []byte("SSH-2.0-test") }
func (stubConnMeta) ServerVersion() []byte { return []byte("SSH-2.0-test") }
func (stubConnMeta) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22222}
}
func (stubConnMeta) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 56432}
}

// TestMeshDeviceIDLabel 钉标签口径:DeviceID 非空(含首尾空格)→ TrimSpace
// 后原样透传(内部空格保留);空 → 回落指纹前 16 字符。
func TestMeshDeviceIDLabel(t *testing.T) {
	pub, _ := newTestKey(t)

	if got := meshDeviceIDLabel(MeshKey{Key: pub, DeviceID: " dev-b "}); got != "dev-b" {
		t.Fatalf("label with surrounding spaces = %q, want %q (TrimSpace only strips edges)", got, "dev-b")
	}
	if got := meshDeviceIDLabel(MeshKey{Key: pub, DeviceID: "dev b extra"}); got != "dev b extra" {
		t.Fatalf("label with inner spaces = %q, want %q (inner space preserved)", got, "dev b extra")
	}

	// 空 DeviceID:回落指纹前 16 字符(gossh.FingerprintSHA256 前缀截取)。
	fp := gossh.FingerprintSHA256(pub)
	want := fp
	if len(fp) > 16 {
		want = fp[:16]
	}
	got := meshDeviceIDLabel(MeshKey{Key: pub})
	if got != want {
		t.Fatalf("empty DeviceID fallback = %q, want fingerprint prefix %q", got, want)
	}
	if !strings.HasPrefix(fp, got) {
		t.Fatal("fallback label must be a prefix of the key fingerprint")
	}
}

// TestMeshKeyForMiss 不命中(含空列表)返回 ok=false,不产生假身份。
func TestMeshKeyForMiss(t *testing.T) {
	pub, _ := newTestKey(t)
	other, _ := newTestKey(t)

	if mk, ok := meshKeyFor(pub, nil); ok {
		t.Fatalf("empty list must miss, got %+v", mk)
	}
	if mk, ok := meshKeyFor(pub, []MeshKey{{Key: other, DeviceID: "dev-x"}}); ok {
		t.Fatalf("unrelated key must miss, got %+v", mk)
	}
}

// TestMeshKeyForCrossListPriority 钉双列表同钥的运行期取向:meshKeyFor 命中
// mesh 条目并带出其 DeviceID(publicKeyCallback 里 mesh 分支先于 bare 分支,
// 端到端优先级由 TestAuthDualListedKeyMeshWinsAtRuntime 背书)。
func TestMeshKeyForCrossListPriority(t *testing.T) {
	pub, _ := newTestKey(t)
	meshList := []MeshKey{{Key: pub, DeviceID: "dev-mesh"}}

	mk, ok := meshKeyFor(pub, meshList)
	if !ok {
		t.Fatal("key present in mesh list must hit")
	}
	if mk.DeviceID != "dev-mesh" {
		t.Fatalf("hit must carry the mesh entry DeviceID, got %q", mk.DeviceID)
	}
}

// TestMeshKeyDeviceIDPassthrough 直测认证回调:两把不同 mesh 钥(不同
// DeviceID)各自把 extMeshDeviceID 透传进 Permissions.Extensions;尾注释为
// 空时透传指纹前缀;裸钥命中不带该键(进程内数据不上线)。
func TestMeshKeyDeviceIDPassthrough(t *testing.T) {
	pubA, _ := newTestKey(t)
	pubB, _ := newTestKey(t)
	bare, _ := newTestKey(t)
	s := NewServer(RunConfig{
		AuthorizedKeys: []gossh.PublicKey{bare},
		MeshKeys: []MeshKey{
			{Key: pubA, DeviceID: "dev-a"},
			{Key: pubB},
		},
	}, "")

	perms, err := s.publicKeyCallback(stubConnMeta{}, pubA)
	if err != nil {
		t.Fatalf("mesh key A auth: %v", err)
	}
	if perms.Extensions[extIngressForm] != FormMesh {
		t.Fatalf("mesh key A form = %q, want %q", perms.Extensions[extIngressForm], FormMesh)
	}
	if perms.Extensions[extMeshDeviceID] != "dev-a" {
		t.Fatalf("mesh key A device id = %q, want dev-a", perms.Extensions[extMeshDeviceID])
	}

	perms, err = s.publicKeyCallback(stubConnMeta{}, pubB)
	if err != nil {
		t.Fatalf("mesh key B auth: %v", err)
	}
	wantFallback := gossh.FingerprintSHA256(pubB)
	if len(wantFallback) > 16 {
		wantFallback = wantFallback[:16]
	}
	if got := perms.Extensions[extMeshDeviceID]; got != wantFallback {
		t.Fatalf("mesh key B device id = %q, want fingerprint prefix %q", got, wantFallback)
	}

	// 裸钥命中:bare 形态且不带 mesh 身份键(键缺失而非空串——缺失与空串
	// 在 e2e 不可分,直打回调是唯一能钉死的地方)。
	perms, err = s.publicKeyCallback(stubConnMeta{}, bare)
	if err != nil {
		t.Fatalf("bare key auth: %v", err)
	}
	if perms.Extensions[extIngressForm] != FormBare {
		t.Fatalf("bare form = %q, want %q", perms.Extensions[extIngressForm], FormBare)
	}
	if _, present := perms.Extensions[extMeshDeviceID]; present {
		t.Fatalf("bare key must not carry %s at all", extMeshDeviceID)
	}
}

// TestMeshKeyForDuplicateKeepsFirstEntry mesh 列表同钥两次(不同 device_id)
// → 命中第一个条目:列表顺序决定,确定性;仅标签级影响(审计身份取首个),
// 可接受——直构造 RunConfig 绕过保存校验可达的运行期兜底取向。
func TestMeshKeyForDuplicateKeepsFirstEntry(t *testing.T) {
	pub, _ := newTestKey(t)
	list := []MeshKey{{Key: pub, DeviceID: "dev-first"}, {Key: pub, DeviceID: "dev-second"}}

	mk, ok := meshKeyFor(pub, list)
	if !ok {
		t.Fatal("duplicate-listed key must hit")
	}
	if mk.DeviceID != "dev-first" {
		t.Fatalf("first-listed entry must win the label: got %q, want dev-first", mk.DeviceID)
	}
}
