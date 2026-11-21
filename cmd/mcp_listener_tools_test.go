package cmd

// 监听器工具族 stdio 面单测(specs/005-event-listener §5,plan Task 13)。
// 钉三层:
//  1. tools/list 四枚在列 + 表形状(路径/超时/rawData/buildReq,读面批同款);
//  2. listener_list 结构化透传 + 调用形状(mock 本地 agent,mesh 读面同 idiom);
//  3. 真链 fail-fast:create 缺 target_session_id → Task 12 共享处理器的 1a
//     必填错误,经 agent 侧端点信封如实映射成 isError(挂真 handlers+services
//     链,http_poll 源不触设备闸 → 零云端调用,零缝注入)。

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/handlers"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/config"
)

func TestListenerToolsListedWithSchemas(t *testing.T) {
	pinAIMCPGateConfig(t, "") // hermetic: 环境真配置若关则此存在性测试环境性红
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list must not touch the local agent")
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":31,"method":"tools/list"}`)
	tools := toolNamesFromList(t, resp)

	create, ok := tools["listener_create"]
	if !ok {
		t.Fatal("stdio 面缺少 listener_create")
	}
	for _, name := range []string{"listener_list", "listener_update", "listener_delete"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("stdio 面缺少 %s", name)
		}
	}
	// stdio 面无环境会话:create 描述必须明示 target_session_id 在本面必填。
	desc, _ := create["description"].(string)
	if !strings.Contains(desc, "REQUIRED") || !strings.Contains(desc, "target_session_id") {
		t.Fatalf("create 描述必须声明 target_session_id REQUIRED on this face: %q", desc)
	}
	schema, _ := create["inputSchema"].(map[string]interface{})
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatal("listener_create schema 缺 properties")
	}
	// required 集与 agentcore 面一致(target_session_id 刻意不进 schema.required,
	// 必填语义由描述+运行时 1a fail-fast 承载,契约行两面共用一份 Required)。
	var gotReq []string
	if raw, ok := schema["required"].([]interface{}); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				gotReq = append(gotReq, s)
			}
		}
	}
	sort.Strings(gotReq)
	if strings.Join(gotReq, ",") != "mode,prompt_template,source,title" {
		t.Fatalf("listener_create required 漂移: got [%s]", strings.Join(gotReq, ","))
	}
}

// 表形状:路径/超时/rawData 是纯 stdio 面字段(契约不管),防两端点路径写歪
// 或预算链断裂(云端单跳 8s → tool 层 14s 兜底,mesh 读面同款分层)。
func TestListenerToolsTableShape(t *testing.T) {
	want := map[string]struct {
		path      string
		timeoutMs int
	}{
		"listener_create": {path: "/api/agent/listeners/mcp/create", timeoutMs: 14_000},
		"listener_list":   {path: "/api/agent/listeners/mcp/list", timeoutMs: 14_000},
		"listener_update": {path: "/api/agent/listeners/mcp/update", timeoutMs: 14_000},
		"listener_delete": {path: "/api/agent/listeners/mcp/delete", timeoutMs: 14_000},
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
		if tool.method != http.MethodPost || tool.path != spec.path {
			t.Fatalf("%s 路由漂移: got %s %s want POST %s", name, tool.method, tool.path, spec.path)
		}
		if tool.timeoutMs != spec.timeoutMs {
			t.Fatalf("%s timeoutMs = %d, want %d(云端单跳 8s,ctx 只兜底不抢先)", name, tool.timeoutMs, spec.timeoutMs)
		}
		if !tool.rawData {
			t.Fatalf("%s 必须带 rawData 标记(结果 data 是结构化对象,非 {result:string})", name)
		}
		if tool.buildReq == nil {
			t.Fatalf("%s buildReq 缺失", name)
		}
		// 零参调用(MCP 客户端省略 arguments → args nil)必须归一为空对象,
		// 不得把 nil map 序列化成字面 null 送上 wire。
		if payload := tool.buildReq(nil); payload == nil {
			t.Fatalf("%s buildReq(nil) 必须归一为空对象", name)
		}
	}
}

// listener_list:结构化 data 原样透传(rawData 分支),POST 到 agent 侧端点。
func TestListenerCallList_MockAgentPassthrough(t *testing.T) {
	pinAIMCPGateConfig(t, "") // hermetic: 环境真配置若关则此存在性测试环境性红
	rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":[{"id":"L-1","title":"盯日志","source_type":"file_tail","status":"active","target_session_id":"sess-9"}]}`))
	})
	srv := newMCPServerForTest(t, rec)

	resp := feed(t, srv, `{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"name":"listener_list","arguments":{}}}`)
	text, isErr := toolCallText(t, resp)
	if isErr {
		t.Fatalf("结构化列表不得 isError: %q", text)
	}
	for _, want := range []string{`"L-1"`, `"file_tail"`, `"target_session_id":"sess-9"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("data JSON 原样透传缺失 %s: %s", want, text)
		}
	}
	method, path, body := rec.last()
	if method != http.MethodPost || path != "/api/agent/listeners/mcp/list" {
		t.Fatalf("call = %s %s", method, path)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("POST 必须带请求体(空参归一 {})")
	}
}

// 真链 fail-fast:挂真 handlers 壳 + 真 services 共享处理器(零缝注入——
// http_poll 源不触设备归属闸,缺 target_session_id 时 1a fail-fast 在任何
// 云端调用之前触发),agent 侧端点回 400 JSON 信封,stdio 面必须把 msg 原样
// 映射成 isError(纯文本 body 会被 invokeTool 判成「不是 JSON」丢掉 msg——
// 这也是 agent 侧错误分支必须走 JSON 信封、不走 http.Error 的原因)。
//
// 注意不能用 newMCPTestServer 挂真链:它的录制器会先抽干 r.Body 再转交
// handler(mesh 族 POST 全零体从未踩到;本面 create 的参数全在 body 里),
// 这里用回填 body 的本地包装。
func TestListenerCallCreate_MissingTargetSessionIDFailsFast(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1") // proxyIfNeeded 就地执行,不外代理
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agent/listeners/mcp/create", handlers.NewAgentHandler(&services.AgentService{}).HandleListenerMCPCreate)
	srv := &peerMCPServer{baseURL: newBodyPreservingServer(t, mux.ServeHTTP).URL, client: &http.Client{}}

	args := `{"title":"盯构建","source":{"type":"http_poll","params":{"url":"https://example.com/health"}},"prompt_template":"服务状态:{{event}}","mode":"once"}`
	resp := feed(t, srv, `{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"listener_create","arguments":`+args+`}}`)
	text, isErr := toolCallText(t, resp)
	if !isErr {
		t.Fatalf("缺 target_session_id 必须 isError: %q", text)
	}
	if !strings.Contains(text, "target_session_id") || !strings.Contains(text, "必填") {
		t.Fatalf("1a fail-fast 错误须原文透传: %q", text)
	}
}

// customer.event_listeners 关闸(= 常驻侧 EffectiveEventListenersEnabled 的
// 冷子进程镜像,文件直读)时:tools/list 剥四件 + tools/call 落标准 unknown
// tool 错误——与 ai_mcp 门控同款两条出口,文案同款(模型对「不存在」换路子,
// 对「被禁用」会复读)。显式 true / 缺省 nil = 在列;ai_mcp 关闸不误伤监听器。
func TestListenerToolsGatedByEventListenersSwitch(t *testing.T) {
	listNames := func(t *testing.T) map[string]map[string]interface{} {
		t.Helper()
		rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("tools/list must not touch the local agent")
		})
		srv := newMCPServerForTest(t, rec)
		return toolNamesFromList(t, feed(t, srv, `{"jsonrpc":"2.0","id":41,"method":"tools/list"}`))
	}

	t.Run("gate off hides tools and call reports unknown tool", func(t *testing.T) {
		pinAIMCPGateConfig(t, `{"customer":{"event_listeners":{"enable":false}}}`)
		tools := listNames(t)
		for _, name := range []string{"listener_create", "listener_list", "listener_update", "listener_delete"} {
			if _, ok := tools[name]; ok {
				t.Fatalf("关闸时 %s 不得在列", name)
			}
		}
		// 其余组不受此闸:mesh/peer 仍在。
		for _, name := range []string{"mesh_readiness", "peer_exec", "ai_run_start"} {
			if _, ok := tools[name]; !ok {
				t.Fatalf("event_listeners 闸不得误伤 %s", name)
			}
		}
		rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("关闸的 tools/call 不得触达本地 agent")
		})
		srv := newMCPServerForTest(t, rec)
		resp := feed(t, srv, `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"listener_list","arguments":{}}}`)
		text, isErr := toolCallText(t, resp)
		if !isErr || text != "unknown tool: listener_list" {
			t.Fatalf("关闸调用须落标准 unknown tool 错误: isErr=%v %q", isErr, text)
		}
	})

	t.Run("gate explicitly on keeps tools", func(t *testing.T) {
		pinAIMCPGateConfig(t, `{"customer":{"event_listeners":{"enable":true}}}`)
		tools := listNames(t)
		for _, name := range []string{"listener_create", "listener_list", "listener_update", "listener_delete"} {
			if _, ok := tools[name]; !ok {
				t.Fatalf("显式 true 时 %s 必须在列", name)
			}
		}
	})

	t.Run("ai_mcp gate off must not affect listener tools", func(t *testing.T) {
		pinAIMCPGateConfig(t, `{"customer":{"agentcore":{"ai_mcp":false},"event_listeners":{"enable":true}}}`)
		tools := listNames(t)
		if _, ok := tools["ai_run_start"]; ok {
			t.Fatal("ai_mcp 关闸必须收走 ai_run_*")
		}
		if _, ok := tools["listener_create"]; !ok {
			t.Fatal("ai_mcp 闸不得误伤监听器工具")
		}
	})
}

// 错误信封分支钉住(I2):agent 侧端点经 common.Error 出的两条非默认分支——
// 业务码 201(不可达,ErrorCodeToHTTPStatus 落 HTTP 500)与 104(409 冲突,
// 落 HTTP 409)——stdio 面都必须 isError=true 且 msg 原文透传。
func TestListenerCallErrorEnvelopeBranches(t *testing.T) {
	pinAIMCPGateConfig(t, "") // hermetic: 环境真配置若关则此存在性测试环境性红
	cases := []struct {
		name       string
		httpStatus int
		envelope   string
		wantMsg    string
	}{
		{
			name:       "unavailable business code 201 maps to HTTP 500",
			httpStatus: http.StatusInternalServerError,
			envelope:   `{"code":201,"msg":"listener cloud offline: dial tcp 127.0.0.1:1: connect: connection refused","data":null}`,
			wantMsg:    "listener cloud offline: dial tcp",
		},
		{
			name:       "conflict business code 104 maps to HTTP 409",
			httpStatus: http.StatusConflict,
			envelope:   `{"code":104,"msg":"listener 409: updated_at 不匹配(LWW),取最新值重试","data":null}`,
			wantMsg:    "updated_at 不匹配",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte(tc.envelope))
			})
			srv := newMCPServerForTest(t, rec)
			resp := feed(t, srv, `{"jsonrpc":"2.0","id":43,"method":"tools/call","params":{"name":"listener_update","arguments":{"id":"L-1","status":"paused"}}}`)
			text, isErr := toolCallText(t, resp)
			if !isErr {
				t.Fatalf("错误信封必须 isError: %q", text)
			}
			if !strings.Contains(text, tc.wantMsg) {
				t.Fatalf("信封 msg 须原文透传: %q", text)
			}
		})
	}
}

// M1:stdio 描述符与 agentcore 面 schema 的深度对账(契约行只钉参数名集,
// 这里补钉嵌套 source 形状与属性文本)。唯一白名单差异:create 的
// target_session_id 描述(stdio 面声明本面必填,agentcore 面是缺省回填语义)。
func TestListenerSchemaDeepParityWithAgentCoreFace(t *testing.T) {
	config.ResetGlobalConfigForTest() // event_listeners 默认开,防前序用例残留关闸剥空
	t.Cleanup(config.ResetGlobalConfigForTest)
	core := services.ListenerCoreToolSchemas()
	if len(core) != 4 {
		t.Fatalf("agentcore 面 schema 锚点 = %d 个工具, want 4", len(core))
	}
	byName := map[string]peerMCPTool{}
	for _, tool := range peerMCPTools {
		byName[tool.name] = tool
	}
	for _, name := range []string{"listener_create", "listener_list", "listener_update", "listener_delete"} {
		coreSchema, ok := core[name]
		if !ok {
			t.Fatalf("agentcore 面缺 %s schema", name)
		}
		stdio := byName[name]
		coreProps, _ := coreSchema["properties"].(map[string]interface{})
		stdioProps, _ := stdio.schema["properties"].(map[string]interface{})
		if len(coreProps) != len(stdioProps) {
			t.Fatalf("%s properties 数漂移: core=%d stdio=%d", name, len(coreProps), len(stdioProps))
		}
		for key, cv := range coreProps {
			sv, ok := stdioProps[key]
			if !ok {
				t.Fatalf("%s.%s 仅 stdio 面缺失", name, key)
			}
			if name == "listener_create" && key == "target_session_id" {
				// 白名单差异:必填语义只写在 stdio 描述;类型仍须一致。
				cs, _ := cv.(map[string]interface{})
				ss, _ := sv.(map[string]interface{})
				if cs["type"] != ss["type"] {
					t.Fatalf("target_session_id 类型漂移: %v vs %v", cs["type"], ss["type"])
				}
				if desc, _ := ss["description"].(string); !strings.Contains(desc, "REQUIRED") {
					t.Fatalf("stdio target_session_id 描述必须声明 REQUIRED: %q", desc)
				}
				continue
			}
			if !reflect.DeepEqual(cv, sv) {
				t.Fatalf("%s.%s 描述符漂移(两面须手同步):\ncore : %v\nstdio: %v", name, key, cv, sv)
			}
		}
		if got, want := normalizeRequiredForParity(stdio.schema), normalizeRequiredForParity(coreSchema); got != want {
			t.Fatalf("%s required 漂移: stdio=[%s] core=[%s]", name, got, want)
		}
	}
}

// normalizeRequiredForParity 把两侧 required(stdio=[]string / agentcore=
// []interface{})归一成排序逗号串,只比集合不比类型/顺序。
func normalizeRequiredForParity(schema map[string]interface{}) string {
	var names []string
	switch raw := schema["required"].(type) {
	case []string:
		names = append(names, raw...)
	case []interface{}:
		for _, v := range raw {
			if s, ok := v.(string); ok {
				names = append(names, s)
			}
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// M2:四个 tools/call 各自经真 handlers 壳路由到「自己的」 invoke——每个 op
// 用只有该 op 会产生的本地错误/离线文本做指纹,端点↔invoke 接错线即刻红。
// delete/list 会触达惰性共享服务(无凭据→离线哨兵),零网络外呼。
func TestListenerCallOps_RoutedToOwnInvoke(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	mux := http.NewServeMux()
	h := handlers.NewAgentHandler(&services.AgentService{})
	mux.HandleFunc("POST /api/agent/listeners/mcp/create", h.HandleListenerMCPCreate)
	mux.HandleFunc("POST /api/agent/listeners/mcp/list", h.HandleListenerMCPList)
	mux.HandleFunc("POST /api/agent/listeners/mcp/update", h.HandleListenerMCPUpdate)
	mux.HandleFunc("POST /api/agent/listeners/mcp/delete", h.HandleListenerMCPDelete)
	srv := &peerMCPServer{baseURL: newBodyPreservingServer(t, mux.ServeHTTP).URL, client: &http.Client{}}

	invoke := func(t *testing.T, id int, name, args string) (string, bool) {
		t.Helper()
		line := `{"jsonrpc":"2.0","id":` + string(rune('0'+id)) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
		return toolCallText(t, feed(t, srv, line))
	}

	// create 指纹:source 枚举预检(仅 create 路径会先撞 source type)。
	if text, isErr := invoke(t, 5, "listener_create", `{}`); !isErr || !strings.Contains(text, "invalid source type") {
		t.Fatalf("create 路由指纹失败: isErr=%v %q", isErr, text)
	}
	// update 指纹:合法 id + 零补丁键 → empty patch(仅 update 产生)。
	if text, isErr := invoke(t, 6, "listener_update", `{"id":"L-1"}`); !isErr || !strings.Contains(text, "empty patch") {
		t.Fatalf("update 路由指纹失败: isErr=%v %q", isErr, text)
	}
	// delete 指纹:本地闸全过 → 直达云端 → 离线哨兵(若接错到 create/update
	// 会先撞各自的本地预检,不会出现离线文本)。
	if text, isErr := invoke(t, 7, "listener_delete", `{"id":"L-1"}`); !isErr || !strings.Contains(text, "listener cloud offline") {
		t.Fatalf("delete 路由指纹失败: isErr=%v %q", isErr, text)
	}
	// list 指纹:零本地闸 → 云端 → 离线哨兵(接错到 update/delete 会得
	// invalid listener id,接到 create 会得 source type)。
	if text, isErr := invoke(t, 8, "listener_list", `{}`); !isErr || !strings.Contains(text, "listener cloud offline") {
		t.Fatalf("list 路由指纹失败: isErr=%v %q", isErr, text)
	}
}

// newBodyPreservingServer 起「录制但回填请求体」的测试服务器:先读全量
// body 记录,再以 NopCloser 回填给下游 handler。stdio 面的监听器工具参数
// 全在 POST body 里,录制器抽干 body 会把真链测试喂成空参。
func newBodyPreservingServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(raw))
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}
