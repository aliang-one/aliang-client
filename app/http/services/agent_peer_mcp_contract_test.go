package services

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/processor/config"
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

// meshRecServer 记录请求形状的 mock 本机回环 mesh 端点,返回 canned 信封;
// errStatus 非 0 时改回错误信封(测非 2xx 分支)。
type meshRecServer struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	method    string
	path      string
	body      string
	errStatus int
	errBody   string
}

func newMeshRecServer(t *testing.T, dataJSON string) *meshRecServer {
	t.Helper()
	rec := &meshRecServer{t: t}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.method, rec.path, rec.body = r.Method, r.URL.Path, string(raw)
		status, respBody := rec.errStatus, rec.errBody
		rec.mu.Unlock()
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(respBody))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":` + dataJSON + `}`))
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *meshRecServer) last() (method, path, body string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.path == "" {
		rec.t.Fatal("no request reached the mock mesh endpoint")
	}
	return rec.method, rec.path, rec.body
}

// mesh 诊断读面(agentcore 面)的执行形状:回环 base 走 seam、GET/POST 零体、
// 结构化 data 原样透传、busy 不判 isError、错误信封判 isError。这是 stdio 面
// rawData 分支的 agentcore 同语义镜像。
func TestMeshDiagAgentCoreBridgeExecute(t *testing.T) {
	config.ResetGlobalConfigForTest() // 门控 nil 默认开;防前序用例残留关闸态
	t.Cleanup(config.ResetGlobalConfigForTest)

	readiness := newMeshRecServer(t, `{"enable":true,"beacon_running":false}`)
	lanPeers := newMeshRecServer(t, `{"peers":[]}`)
	busy := newMeshRecServer(t, `{"busy":true,"note":"另一探测在跑,勿立即重试"}`)

	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}

	origBase := agentMeshLocalBaseURL
	t.Cleanup(func() { agentMeshLocalBaseURL = origBase })

	tools := m.agentCorePeerTools(agentAIRun{sessionID: "mesh"})
	byName := map[string]agentcore.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}

	agentMeshLocalBaseURL = func() string { return readiness.srv.URL }
	res := byName[AgentPeerToolEventMeshReadiness].Execute(context.Background(), map[string]interface{}{}, nil)
	if res.IsError || res.Content != `{"enable":true,"beacon_running":false}` {
		t.Fatalf("readiness execute = %+v", res)
	}
	if method, path, body := readiness.last(); method != http.MethodGet || path != "/api/mesh/diag" || body != "" {
		t.Fatalf("readiness call = %s %s body=%q", method, path, body)
	}

	agentMeshLocalBaseURL = func() string { return lanPeers.srv.URL }
	res = byName[AgentPeerToolEventMeshLanCandidates].Execute(context.Background(), nil, nil)
	if res.IsError || res.Content != `{"peers":[]}` {
		t.Fatalf("lan_candidates execute = %+v(零参工具对 nil input 也不该报错)", res)
	}
	if method, path, body := lanPeers.last(); method != http.MethodGet || path != "/api/mesh/lan_peers" || body != "" {
		t.Fatalf("lan_candidates call = %s %s body=%q", method, path, body)
	}

	agentMeshLocalBaseURL = func() string { return busy.srv.URL }
	res = byName[AgentPeerToolEventMeshStunProbe].Execute(context.Background(), map[string]interface{}{}, nil)
	if res.IsError || !strings.Contains(res.Content, `"busy":true`) {
		t.Fatalf("stun_probe busy 应为合法成功回包: %+v", res)
	}
	if method, path, body := busy.last(); method != http.MethodPost || path != "/api/mesh/stun_probe" || body != "" {
		t.Fatalf("stun_probe call = %s %s body=%q(POST 零体)", method, path, body)
	}

	// 500 → isError 且 msg 原样透传。
	agentMeshLocalBaseURL = func() string { return lanPeers.srv.URL }
	lanPeers.errStatus = http.StatusInternalServerError
	lanPeers.errBody = `{"code":500,"msg":"forward failed","data":null}`
	res = byName[AgentPeerToolEventMeshIdentity].Execute(context.Background(), map[string]interface{}{}, nil)
	if !res.IsError || !strings.Contains(res.Content, "forward failed") {
		t.Fatalf("500 必须映射 isError 且 msg 透传: %+v", res)
	}
}
