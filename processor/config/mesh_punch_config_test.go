package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// punch 配置面(Task 11):PunchEnable 三态指针语义与 ForwardEnable 同族
// (nil=false 让渡型默认关);依赖门挂 ValidateMeshDependency(保存期硬拒,
// 启动期不阻断——mesh↔SSH 依赖门同款分工)。

// (a) 默认关:零值配置 JSON round-trip 后 PunchEnable 生效值 = false。
func TestMeshPunchEnableDefaultOff(t *testing.T) {
	raw, err := json.Marshal(&MeshConfig{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back MeshConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.PunchEnable != nil {
		t.Fatalf("零值 round-trip 后 PunchEnable 应为 nil(默认关), got %v", *back.PunchEnable)
	}
	if back.EffectivePunchEnabled() {
		t.Fatal("零值 EffectivePunchEnabled 应为 false(让渡型能力默认关)")
	}
	var nilMesh *MeshConfig
	if nilMesh.EffectivePunchEnabled() {
		t.Fatal("nil receiver 的 EffectivePunchEnabled 应为 false(fail-closed)")
	}
}

// (b) 显式开:JSON 带 punch_enable:true → 生效 true;显式 false 亦落显式关。
func TestMeshPunchEnableExplicitTrue(t *testing.T) {
	var mesh MeshConfig
	if err := json.Unmarshal([]byte(`{"punch_enable":true}`), &mesh); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !mesh.EffectivePunchEnabled() {
		t.Fatal("punch_enable:true → EffectivePunchEnabled 应为 true")
	}
	var off MeshConfig
	if err := json.Unmarshal([]byte(`{"punch_enable":false}`), &off); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if off.EffectivePunchEnabled() {
		t.Fatal("punch_enable:false → EffectivePunchEnabled 应为 false")
	}
}

// (c) 依赖校验:mesh 总开关显式关 + punch 显式开 → 保存期硬拒(错误信息含
// punch);mesh 开 + punch 开(SSH 入口同开满足既有依赖门)→ 过。分工钉死:
// Config.Validate(启动期也跑)不得拒——依赖门只在保存通道,启动降级不阻断
// boot(与 mesh↔SSH 依赖门同款)。
func TestValidateMeshDependencyPunchRequiresMesh(t *testing.T) {
	on, off := true, false
	mk := func(meshEnable, punch, ssh *bool) *Config {
		return &Config{Customer: &CustomerConfig{
			Mesh:       &MeshConfig{Enable: meshEnable, PunchEnable: punch, PrivateKey: "k", Peers: []MeshPeer{{DeviceID: "d", HostKey: "h"}}},
			SSHIngress: &SSHIngressConfig{Enable: ssh},
		}}
	}
	// mesh 关 + punch 开 + ssh 开:既有 ssh 门不触发(总开关关),punch 门拒。
	err := mk(&off, &on, &on).ValidateMeshDependency()
	if err == nil || !strings.Contains(err.Error(), "punch") {
		t.Fatalf("mesh off + punch on must be rejected with punch in message, got %v", err)
	}
	// mesh 关 + punch 开 + ssh 关:punch 门同样拒(ssh 门因总开关关不触发)。
	if err := mk(&off, &on, &off).ValidateMeshDependency(); err == nil || !strings.Contains(err.Error(), "punch") {
		t.Fatalf("mesh off + punch on + ssh off must be rejected with punch in message, got %v", err)
	}
	// mesh 开 + punch 开 + ssh 开 → 过(合法组合)。
	if err := mk(&on, &on, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh on + punch on + ssh on: %v", err)
	}
	// mesh 开(Enable 未置=默认开)+ punch 开 + ssh 开 → 过。
	if err := mk(nil, &on, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh default-on + punch on + ssh on: %v", err)
	}
	// punch 关(缺键)不添乱:mesh 关 + ssh 开 → 过(punch 无从违规)。
	if err := mk(&off, nil, &on).ValidateMeshDependency(); err != nil {
		t.Fatalf("mesh off + punch unset: %v", err)
	}
	// 分工:Config.Validate(启动期也调用)不得以 punch 依赖拒——依赖门只在
	// 保存通道,手改配置文件的自相矛盾形态交给运行期装配门合取兜住,不阻断
	// boot。Validate 可能因与本用例无关的必填字段(如 api_server)报错,只
	// 断言错误里没有 punch(即依赖门确未挂进 Validate 链)。
	if err := mk(&off, &on, &on).Validate(); err != nil && strings.Contains(err.Error(), "punch") {
		t.Fatalf("Config.Validate must not reject punch dependency (save-only gate), got %v", err)
	}
}
