package services

import (
	"sort"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
)

// 契约测试(services 侧):agentcore 面五工具的参数面与关键行为约束必须与
// PeerMCPContractSpecs 一致。漂移即红——修法是把漂移一侧的描述/schema 对齐,
// 不是放宽契约。
func TestPeerMCPContractAgentCoreSide(t *testing.T) {
	m := newAgentAIManager()
	tools := m.agentCorePeerTools(agentAIRun{sessionID: "contract"})
	byName := map[string]agentcore.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}
	for _, spec := range PeerMCPContractSpecs() {
		tool, ok := byName[spec.CoreName]
		if !ok {
			t.Fatalf("agentcore 面缺少工具 %s", spec.CoreName)
		}
		assertPeerMCPSchemaShape(t, spec.CoreName, tool.InputSchema(), spec)
		for _, kw := range spec.Keywords {
			if !strings.Contains(tool.Description(), kw) {
				t.Errorf("%s 描述缺少关键约束 %q(两侧描述须语义等价)", spec.CoreName, kw)
			}
		}
	}
}

func assertPeerMCPSchemaShape(t *testing.T, label string, schema map[string]interface{}, spec PeerMCPContractSpec) {
	t.Helper()
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatalf("%s schema 缺 properties", label)
	}
	gotProps := make([]string, 0, len(props))
	for k := range props {
		gotProps = append(gotProps, k)
	}
	sort.Strings(gotProps)
	want := append(append([]string{}, spec.Required...), spec.Optional...)
	sort.Strings(want)
	if strings.Join(gotProps, ",") != strings.Join(want, ",") {
		t.Errorf("%s 参数面漂移: got [%s] want [%s]", label, gotProps, want)
	}
	var gotReq []string
	if rawReq, ok := schema["required"].([]interface{}); ok {
		for _, r := range rawReq {
			if s, ok := r.(string); ok {
				gotReq = append(gotReq, s)
			}
		}
	}
	sort.Strings(gotReq)
	wantReq := append([]string{}, spec.Required...)
	sort.Strings(wantReq)
	if strings.Join(gotReq, ",") != strings.Join(wantReq, ",") {
		t.Errorf("%s required 漂移: got [%s] want [%s]", label, gotReq, wantReq)
	}
}
