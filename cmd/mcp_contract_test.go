package cmd

// 契约测试(cmd 侧):stdio 面五工具与 services.PeerMCPContractSpecs 对照。
// 两侧描述是手维护近似副本,本测试钉参数面与关键行为约束的语义等价。
// 形状断言 helper 与 services 侧重复是有意的:两包各自独立红/绿,不引入
// 跨包测试耦合。

import (
	"sort"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
)

func TestPeerMCPContractStdioSide(t *testing.T) {
	byName := map[string]peerMCPTool{}
	for _, tool := range peerMCPTools {
		byName[tool.name] = tool
	}
	for _, spec := range services.PeerMCPContractSpecs() {
		tool, ok := byName[spec.StdioName]
		if !ok {
			t.Fatalf("stdio 面缺少工具 %s", spec.StdioName)
		}
		assertPeerMCPStdioSchemaShape(t, spec.StdioName, tool.schema, spec)
		for _, kw := range spec.Keywords {
			if !strings.Contains(tool.description, kw) {
				t.Errorf("%s 描述缺少关键约束 %q", spec.StdioName, kw)
			}
		}
	}
}

func assertPeerMCPStdioSchemaShape(t *testing.T, label string, schema map[string]interface{}, spec services.PeerMCPContractSpec) {
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
	if rawReq, ok := schema["required"].([]string); ok {
		gotReq = rawReq
	}
	sort.Strings(gotReq)
	wantReq := append([]string{}, spec.Required...)
	sort.Strings(wantReq)
	if strings.Join(gotReq, ",") != strings.Join(wantReq, ",") {
		t.Errorf("%s required 漂移: got [%s] want [%s]", label, gotReq, wantReq)
	}
}
