package cmd

// mesh 诊断读面四工具的表形状与调用形状单测(读面批 2026-11-20)。
//
// 契约类型(PeerMCPContractSpecs)只管参数面+描述关键词;method/path/timeoutMs
// 是纯 stdio 面字段,契约不管——本文件钉住它们,防两端点路径写歪(如 lan_peers
// 拼成 lan_candidates)或超时预算链断裂(probe 8s→forward 10s→tool 14s,tool
// 层若 <10s 会在 forward 完成前掐断、拿不到真实结论)。

import (
	"net/http"
	"strings"
	"testing"
)

func TestMeshDiagToolsTableShape(t *testing.T) {
	want := map[string]struct {
		method    string
		path      string
		timeoutMs int
	}{
		"mesh_readiness":      {method: http.MethodGet, path: "/api/mesh/diag", timeoutMs: 14_000},
		"mesh_identity":       {method: http.MethodGet, path: "/api/mesh/identity", timeoutMs: 14_000},
		"mesh_stun_probe":     {method: http.MethodPost, path: "/api/mesh/stun_probe", timeoutMs: 14_000},
		"mesh_lan_candidates": {method: http.MethodGet, path: "/api/mesh/lan_peers", timeoutMs: 14_000},
	}
	byName := map[string]peerMCPTool{}
	for _, tool := range peerMCPTools {
		byName[tool.name] = tool
	}
	for name, spec := range want {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("peerMCPTools 缺工具 %s", name)
		}
		if tool.method != spec.method || tool.path != spec.path {
			t.Fatalf("%s 路由漂移: got %s %s want %s %s", name, tool.method, tool.path, spec.method, spec.path)
		}
		if tool.timeoutMs != spec.timeoutMs {
			t.Fatalf("%s timeoutMs = %d, want %d(预算链第三层,不得小于 forward 10s)", name, tool.timeoutMs, spec.timeoutMs)
		}
		if !tool.rawData {
			t.Fatalf("%s 必须带 rawData 标记(mesh 读面 data 是结构化对象,非 {result:string})", name)
		}
		// 全零参:schema 只有 type+properties 且 properties 空;buildReq 返 nil
		//(GET 零体 + POST 零体共用同一条 invokeTool payload==nil 路径)。
		if len(tool.schema) != 2 {
			t.Fatalf("%s schema 键 = %v, want [type properties]", name, keysOf(tool.schema))
		}
		props, _ := tool.schema["properties"].(map[string]interface{})
		if props == nil || len(props) != 0 {
			t.Fatalf("%s properties = %v, want 空 map(零参工具)", name, tool.schema["properties"])
		}
		if _, hasRequired := tool.schema["required"]; hasRequired {
			t.Fatalf("%s 零参工具不应有 required 键", name)
		}
		if tool.buildReq == nil {
			t.Fatalf("%s buildReq 缺失", name)
		}
		if payload := tool.buildReq(nil); payload != nil {
			t.Fatalf("%s buildReq 必须返 nil(POST 零体靠 payload==nil 留空 body), got %v", name, payload)
		}
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 结构化透传:data 是对象而非 {result:string}——rawData 分支必须原样吐
// data JSON,不判 isError。旧逻辑对这类信封会静默解出空串,这是本批要钉死
// 的回归形状。
func TestMeshDiagCallReadiness_StructuredDataPassthrough(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"enable":true,"effective":false,"lan_peers":[],"beacon":{"beacon_running":false}}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"mesh_readiness","arguments":{}}}`)
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("结构化读面不得 isError: %q", text)
	}
	if text == "" {
		t.Fatal("工具结果不得为空串(旧 result 解包分支对结构化 data 会静默解空)")
	}
	for _, want := range []string{`"enable":true`, `"beacon_running":false`} {
		if !strings.Contains(text, want) {
			t.Fatalf("data JSON 原样透传缺失 %s: %s", want, text)
		}
	}
	method, path, body := rec.last()
	if method != http.MethodGet || path != "/api/mesh/diag" {
		t.Fatalf("call = %s %s", method, path)
	}
	if body != "" {
		t.Fatalf("GET 必须零体, got %s", body)
	}
}

// stun_probe:POST 零体 + busy 结构化回包照常透传(200+code0+busy=true 是
// 合法成功回包,「勿立即重试」契约在描述里,不在传输层判 isError)。
func TestMeshDiagCallStunProbe_PostNoBody_BusyStructured(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"busy":true,"note":"另一探测在跑,勿立即重试"}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":22,"method":"tools/call","params":{"name":"mesh_stun_probe","arguments":{}}}`)
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("busy 是合法成功回包,不得 isError: %q", text)
	}
	if !strings.Contains(text, `"busy":true`) {
		t.Fatalf("busy 结构化回包未透传: %q", text)
	}
	method, path, body := rec.last()
	if method != http.MethodPost || path != "/api/mesh/stun_probe" {
		t.Fatalf("call = %s %s", method, path)
	}
	if body != "" {
		t.Fatalf("POST 零体必须无 body, got %s", body)
	}
}

// lan_peers 的名字刻意与端点路径不同(工具名 lan_candidates / 路径 lan_peers),
// 这条钉住两者不写串;空数组是合法态(无候选≠故障)。
func TestMeshDiagCallLanCandidates_EmptyListIsSuccess(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"peers":[]}}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":23,"method":"tools/call","params":{"name":"mesh_lan_candidates","arguments":{}}}`)
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("空候选是合法态,不得 isError: %q", text)
	}
	if !strings.Contains(text, `"peers":[]`) {
		t.Fatalf("空数组应原样透传: %q", text)
	}
	if _, path, _ := rec.last(); path != "/api/mesh/lan_peers" {
		t.Fatalf("path = %s", path)
	}
}

// 非 2xx / 业务码非 0 → isError 文本透传,与既有 peer 工具同语义。
func TestMeshDiagCall_BackendErrorIsToolError(t *testing.T) {
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"msg":"stun probe 未装配(newConn/probe 注入缝缺失)","data":null}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":24,"method":"tools/call","params":{"name":"mesh_stun_probe","arguments":{}}}`)
	text, isErr := toolCallText(t, resp)
	if !isErr {
		t.Fatalf("500 必须映射成 isError: %q", text)
	}
	if !strings.Contains(text, "未装配") {
		t.Fatalf("服务端 msg 应原样透传, got %q", text)
	}
}

// tools/list 默认可见:mesh 读面免审(D-2),不在 ai_run_* 门控罩内——
// gate off 只收 ai_run_*,不得误伤 mesh 工具。
func TestMeshDiagToolsListedRegardlessOfAIMCPGate(t *testing.T) {
	pinAIMCPGateConfig(t, `{"customer":{"agentcore":{"ai_mcp":false}}}`)
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	byName := toolNamesFromList(t, feed(t, srv, `{"jsonrpc":"2.0","id":25,"method":"tools/list"}`))
	for _, name := range []string{"mesh_readiness", "mesh_identity", "mesh_stun_probe", "mesh_lan_candidates"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("gate off 不得误伤 mesh 读面工具 %q(现有 %v)", name, byName)
		}
	}
	if _, ok := byName["ai_run_start"]; ok {
		t.Fatal("gate off 应收走 ai_run_*")
	}
}
