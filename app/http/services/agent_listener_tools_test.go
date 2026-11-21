package services

// agent_listener_tools_test.go 监听器 LLM 工具面单测(specs/005-event-listener
// §5/FR-9,plan Task 12):纯函数处理器校验矩阵 + ViaCloud 调用形状(fake cloud
// 复用 agent_listener_api_test.go 脚手架)+ agentcore 桥装配/门控。
//
// 缝纪律(同 Task 7-11):测试串行,禁 t.Parallel;swap 期间不得有在跑
// watcher;Cleanup 还原全部 seam。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/processor/config"
)

// newListenerToolTestEnv 标准测试环境:已注册服务(dev-proxy-1)+ fake cloud
// (handler 可为 nil 用默认应答)。返回捕获器。
func newListenerToolTestEnv(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) *listenerCloudCapture {
	t.Helper()
	t.Setenv(AgentRuntimeEnv, "1")
	swapListenerSharedService(t, newListenerProxyTestService(true))
	cap := newListenerFakeCloud(t, handler)
	swapListenerCloudBase(t, cap.server.URL)
	return cap
}

// --- 1. listenerToolCreate 校验矩阵 ----------------------------------------

func TestListenerToolCreateValidationMatrix(t *testing.T) {
	validSource := func() map[string]interface{} {
		return map[string]interface{}{"path": "/tmp/build.log", "match": "ERROR"}
	}
	rc := listenerToolContext{sessionID: "sess-current"}

	t.Run("bad source type rejected locally", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		_, err := listenerToolCreate(context.Background(), rc, "盯日志", "cron", validSource(), "", "模板 {{event}}", "once", "")
		if err == nil || !strings.Contains(err.Error(), "source type") {
			t.Fatalf("非法 source.type 须本地拒绝: %v", err)
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("本地拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("file_tail foreign device rejected with clear message", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		_, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", validSource(), "dev-foreign-9", "模板 {{event}}", "once", "")
		if err == nil {
			t.Fatal("异机 device_id 必须被拒绝")
		}
		if !strings.Contains(err.Error(), "dev-proxy-1") || !strings.Contains(err.Error(), "dev-foreign-9") {
			t.Fatalf("拒绝文案须同时含本机设备与被拒设备(模型可自查): %v", err)
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("process_exit foreign device rejected too", func(t *testing.T) {
		newListenerToolTestEnv(t, nil)
		_, err := listenerToolCreate(context.Background(), rc, "等任务", "process_exit", map[string]interface{}{"command": "make"}, "dev-other", "等完 {{event}}", "once", "")
		if err == nil || !strings.Contains(err.Error(), "THIS device") {
			t.Fatalf("process_exit 异机同样拒绝: %v", err)
		}
	})

	t.Run("file_tail self device id accepted", func(t *testing.T) {
		newListenerToolTestEnv(t, nil)
		// M2:模型自查后显式回填本机 device_id 是等价合法形态,不得误伤。
		res, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", validSource(), "dev-proxy-1", "模板 {{event}}", "once", "sess-t")
		if err != nil {
			t.Fatalf("显式本机 device_id 应成功: %v", err)
		}
		if res["id"] != "L-agent" {
			t.Fatalf("result.id = %v", res["id"])
		}
	})

	t.Run("file_tail empty device defaults to self", func(t *testing.T) {
		var gotBody []byte
		newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.URL.Path == "/api/agent/listeners" {
				gotBody = body
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"listener":{"id":"L-agent","status":"active"}}`))
				return
			}
			http.NotFound(w, r)
		})
		res, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", validSource(), "", "模板 {{event}}", "recurring", "sess-target")
		if err != nil {
			t.Fatalf("空 device_id = 本机默认,应成功: %v", err)
		}
		if res["id"] != "L-agent" {
			t.Fatalf("result.id = %v", res["id"])
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(gotBody, &sent)
		var source struct {
			DeviceID string `json:"device_id"`
		}
		_ = json.Unmarshal(sent["source"], &source)
		if source.DeviceID != "dev-proxy-1" {
			t.Fatalf("device 空缺须由本层钉本机: %+v", source)
		}
	})

	t.Run("webhook device_id ignored and forced server-side", func(t *testing.T) {
		var gotBody []byte
		cap := newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.URL.Path == "/api/agent/listeners" {
				gotBody = body
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"listener":{"id":"L-agent","status":"active"}}`))
				return
			}
			http.NotFound(w, r)
		})
		// webhook 是 server 侧源:LLM 带了 device_id 也不报错、不透传语义
		// (wire 上 device_id 由 ViaCloud 统一钉本机,server 侧归一 "server")。
		res, err := listenerToolCreate(context.Background(), rc, "回调", "webhook", map[string]interface{}{}, "dev-foreign-9", "通知 {{event}}", "once", "")
		if err != nil {
			t.Fatalf("webhook 的 device_id 须被忽略而非拒绝: %v", err)
		}
		if res["status"] != "active" {
			t.Fatalf("result.status = %v", res["status"])
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(gotBody, &sent)
		var source struct {
			Type     string `json:"type"`
			DeviceID string `json:"device_id"`
		}
		_ = json.Unmarshal(sent["source"], &source)
		if source.Type != "webhook" || source.DeviceID != "dev-proxy-1" {
			t.Fatalf("webhook wire source 形状不符: %+v", source)
		}
		if n := len(cap.snapshot()); n != 1 {
			t.Fatalf("应恰一笔云端调用: %d", n)
		}
	})

	t.Run("webhook without params sends empty object not null", func(t *testing.T) {
		var gotBody []byte
		newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.URL.Path == "/api/agent/listeners" {
				gotBody = body
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"listener":{"id":"L-agent","status":"active"}}`))
				return
			}
			http.NotFound(w, r)
		})
		// 广告面上 params 省略是合法调用形态(bridge 侧缺键落 nil map):
		// wire 必须钉 "params":{},不得出 "params":null(I1)。
		if _, err := listenerToolCreate(context.Background(), rc, "回调", "webhook", nil, "", "通知 {{event}}", "once", "sess-t"); err != nil {
			t.Fatalf("webhook 无 params 应成功: %v", err)
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(gotBody, &sent)
		var source struct {
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(sent["source"], &source)
		if strings.TrimSpace(string(source.Params)) != "{}" {
			t.Fatalf("wire params 须为 {} 而非 null: %s", gotBody)
		}
	})

	t.Run("empty resolved target session fails fast locally", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		// I2:无会话上下文(rc.sessionID 空)且未显式传 target → 本地报错,
		// 省一次云端往返(1a 必填;1b 放开)。
		_, err := listenerToolCreate(context.Background(), listenerToolContext{}, "盯日志", "file_tail", validSource(), "", "{{event}}", "once", "")
		if err == nil || !strings.Contains(err.Error(), "target_session_id 必填") {
			t.Fatalf("解析后空 target 须本地拒绝: %v", err)
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("本地拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("prompt template missing event placeholder", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		_, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", validSource(), "", "没有占位符的模板", "once", "")
		if err == nil || !strings.Contains(err.Error(), "{{event}}") {
			t.Fatalf("缺 {{event}} 须本地报错: %v", err)
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("本地拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("invalid mode rejected", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		_, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", validSource(), "", "模板 {{event}}", "daily", "")
		if err == nil || !strings.Contains(err.Error(), "mode") {
			t.Fatalf("非法 mode 须本地拒绝: %v", err)
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("本地拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("target session default applied from run context", func(t *testing.T) {
		var gotBody []byte
		newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.URL.Path == "/api/agent/listeners" {
				gotBody = body
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"listener":{"id":"L-agent","status":"active"}}`))
				return
			}
			http.NotFound(w, r)
		})
		res, err := listenerToolCreate(context.Background(), rc, "盯日志", "http_poll", map[string]interface{}{"url": "https://a.example.com"}, "", "变化 {{event}}", "recurring", "")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(gotBody, &sent)
		if string(sent["target_session_id"]) != `"sess-current"` {
			t.Fatalf("target_session_id 缺省须回填当前会话: %s", gotBody)
		}
		if res["target_session_id"] != "sess-current" {
			t.Fatalf("result 须回显 target_session_id: %+v", res)
		}
	})
}

// --- 2. approval_warning ----------------------------------------------------

// 词表事实:会话级 approval_mode 词表是 ""/ask_all/bypass_all/read_only
// (agent_approval_policy.go),没有字面 "require" 挡位;'require' 是逐调用
// 策略决策。ask_all(每次必审)与 read_only(写/执行族必审)是会把无人值守
// 的注入回合困死在审批卡上的两个挡位 → 警示;默认 "" 走 balanced 策略(常见
// 操作自动放行)、bypass_all 恒放行 → 不警示。此映射是 Task 12 的文档化本地
// 决策(任务文本写的 'require' 在会话词表中不存在)。
func TestListenerToolCreateApprovalWarning(t *testing.T) {
	const wantWarning = "目标会话审批模式为 ask_all/read_only:监听触发的新回合无人审批会在超时后被判死,请保持设备在线或改用免审会话"

	t.Run("ask_all target session warns with exact text", func(t *testing.T) {
		newListenerToolTestEnv(t, nil)
		rc := listenerToolContext{
			sessionID:      "sess-current",
			approvalModeOf: func(string) string { return agentAIApprovalModeAskAll },
		}
		res, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", map[string]interface{}{"path": "/tmp/a"}, "", "{{event}}", "once", "")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if res["approval_warning"] != wantWarning {
			t.Fatalf("warning 文案不符: %v", res["approval_warning"])
		}
	})

	t.Run("read_only target session warns", func(t *testing.T) {
		newListenerToolTestEnv(t, nil)
		rc := listenerToolContext{
			sessionID:      "sess-current",
			approvalModeOf: func(string) string { return agentAIApprovalModeReadOnly },
		}
		res, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", map[string]interface{}{"path": "/tmp/a"}, "", "{{event}}", "once", "")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if res["approval_warning"] == nil {
			t.Fatal("read_only 也须警示(写/执行族会被压审批)")
		}
	})

	t.Run("bypass_all absent", func(t *testing.T) {
		newListenerToolTestEnv(t, nil)
		rc := listenerToolContext{
			sessionID:      "sess-current",
			approvalModeOf: func(string) string { return agentAIApprovalModeBypassAll },
		}
		res, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", map[string]interface{}{"path": "/tmp/a"}, "", "{{event}}", "once", "")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, has := res["approval_warning"]; has {
			t.Fatalf("bypass_all 不应警示: %+v", res)
		}
	})

	t.Run("resolver receives the effective target session id", func(t *testing.T) {
		newListenerToolTestEnv(t, nil)
		var gotTarget string
		rc := listenerToolContext{
			sessionID: "sess-current",
			approvalModeOf: func(target string) string {
				gotTarget = target
				return ""
			},
		}
		if _, err := listenerToolCreate(context.Background(), rc, "盯日志", "file_tail", map[string]interface{}{"path": "/tmp/a"}, "", "{{event}}", "once", "sess-foreign-7"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if gotTarget != "sess-foreign-7" {
			t.Fatalf("resolver 须收到显式目标会话: %q", gotTarget)
		}
	})
}

// --- 3. list/update/delete 处理器 → ViaCloud 调用形状 -----------------------

func TestListenerToolListUpdateDeleteViaCloud(t *testing.T) {
	t.Run("list passthrough snake_case rows", func(t *testing.T) {
		newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/agent/listeners" {
				_, _ = w.Write([]byte(`{"listeners":[{"id":"L-a","source_type":"file_tail","status":"active","fired_count":2,"error_note":""}]}`))
				return
			}
			http.NotFound(w, r)
		})
		rows, err := listenerToolList(context.Background())
		if err != nil {
			t.Fatalf("listenerToolList: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d", len(rows))
		}
		for _, key := range []string{"id", "source_type", "status", "fired_count"} {
			if _, ok := rows[0][key]; !ok {
				t.Fatalf("行缺 snake_case 键 %q: %v", key, rows[0])
			}
		}
	})

	t.Run("update merges updated_at from local mirror (LWW)", func(t *testing.T) {
		withListenerMirrorHome(t, t.TempDir())
		if err := saveListenerMirrors([]ListenerMirror{sampleListenerMirror("L-a", "2026-11-21T00:00:00Z")}); err != nil {
			t.Fatalf("seed mirror: %v", err)
		}
		var patchBody []byte
		cap := newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodPatch {
				patchBody = body
				_, _ = w.Write([]byte(`{"listener":{"id":"L-a","status":"paused"}}`))
			}
		})
		res, err := listenerToolUpdate(context.Background(), "L-a", map[string]interface{}{"status": "paused"})
		if err != nil {
			t.Fatalf("listenerToolUpdate: %v", err)
		}
		if res["id"] != "L-a" {
			t.Fatalf("result = %+v", res)
		}
		var sent map[string]interface{}
		if err := json.Unmarshal(patchBody, &sent); err != nil {
			t.Fatalf("patch body 须 JSON: %v", err)
		}
		if sent["status"] != "paused" || sent["updated_at"] != "2026-11-21T00:00:00Z" {
			t.Fatalf("patch 须携带镜像行 updated_at 做 LWW: %s", patchBody)
		}
		calls := cap.snapshot()
		if len(calls) != 1 || calls[0].Method != http.MethodPatch || calls[0].Path != "/api/listeners/L-a" {
			t.Fatalf("调用形状不符: %+v", calls)
		}
	})

	t.Run("explicit updated_at wins over local mirror", func(t *testing.T) {
		withListenerMirrorHome(t, t.TempDir())
		if err := saveListenerMirrors([]ListenerMirror{sampleListenerMirror("L-a", "2026-11-21T00:00:00Z")}); err != nil {
			t.Fatalf("seed mirror: %v", err)
		}
		var patchBody []byte
		newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodPatch {
				patchBody = body
				_, _ = w.Write([]byte(`{}`))
			}
		})
		if _, err := listenerToolUpdate(context.Background(), "L-a", map[string]interface{}{"status": "paused", "updated_at": "2026-11-21T09:00:00Z"}); err != nil {
			t.Fatalf("listenerToolUpdate: %v", err)
		}
		var sent map[string]interface{}
		_ = json.Unmarshal(patchBody, &sent)
		if sent["updated_at"] != "2026-11-21T09:00:00Z" {
			t.Fatalf("显式 updated_at(如来自 listener_list 的 server 行,比镜像新)必须优先: %s", patchBody)
		}
	})

	t.Run("update without mirror row passes patch through", func(t *testing.T) {
		withListenerMirrorHome(t, t.TempDir())
		var patchBody []byte
		newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodPatch {
				patchBody = body
				_, _ = w.Write([]byte(`{}`))
			}
		})
		if _, err := listenerToolUpdate(context.Background(), "L-unknown", map[string]interface{}{"title": "x"}); err != nil {
			t.Fatalf("无镜像行也须透传(server 422 兜底): %v", err)
		}
		var sent map[string]interface{}
		_ = json.Unmarshal(patchBody, &sent)
		if sent["title"] != "x" {
			t.Fatalf("透传 body 不符: %s", patchBody)
		}
	})

	t.Run("update invalid id rejected locally", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		if _, err := listenerToolUpdate(context.Background(), "../etc", map[string]interface{}{"status": "paused"}); err == nil {
			t.Fatal("畸形 id 须本地拒绝(注入防线)")
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("本地拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("bad patch enums rejected locally", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, nil)
		if _, err := listenerToolUpdate(context.Background(), "L-a", map[string]interface{}{"status": "zombie"}); err == nil || !strings.Contains(err.Error(), "status") {
			t.Fatalf("非法 status 须本地拒绝: %v", err)
		}
		if _, err := listenerToolUpdate(context.Background(), "L-a", map[string]interface{}{"mode": "daily"}); err == nil || !strings.Contains(err.Error(), "mode") {
			t.Fatalf("非法 mode 须本地拒绝: %v", err)
		}
		if n := len(cap.snapshot()); n != 0 {
			t.Fatalf("本地拒绝不得打云端: %d calls", n)
		}
	})

	t.Run("delete calls user-face DELETE", func(t *testing.T) {
		cap := newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodDelete {
				_, _ = w.Write([]byte(`{}`))
			}
		})
		res, err := listenerToolDelete(context.Background(), "L-a")
		if err != nil {
			t.Fatalf("listenerToolDelete: %v", err)
		}
		if res["id"] != "L-a" {
			t.Fatalf("result = %+v", res)
		}
		calls := cap.snapshot()
		if len(calls) != 1 || calls[0].Method != http.MethodDelete || calls[0].Path != "/api/listeners/L-a" {
			t.Fatalf("调用形状不符: %+v", calls)
		}
	})
}

// --- 4. agentcore 桥装配 ----------------------------------------------------

func TestAgentCoreListenerToolsBridges(t *testing.T) {
	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	tools := m.agentCoreListenerTools(agentAIRun{sessionID: "sess-bridge"})
	if len(tools) != 4 {
		t.Fatalf("tools = %d, want 4", len(tools))
	}
	names := map[string]agentcore.Tool{}
	for _, tool := range tools {
		names[tool.Name()] = tool
	}
	for _, want := range []string{"listener_create", "listener_list", "listener_update", "listener_delete"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("缺桥工具 %s (have %v)", want, names)
		}
	}
	for name := range names {
		if agentCoreKernelBuiltinTools[name] {
			t.Fatalf("桥工具 %s 与内核内置名冲突", name)
		}
	}

	// create 桥 schema:spec §5 形状。
	create := names["listener_create"]
	schema := create.InputSchema()
	if schema["type"] != "object" {
		t.Fatalf("create schema.type = %v", schema["type"])
	}
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatalf("create schema.properties 缺失: %v", schema)
	}
	for _, key := range []string{"title", "source", "prompt_template", "mode", "target_session_id"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("create schema 缺 %q: %v", key, props)
		}
	}
	req, _ := schema["required"].([]interface{})
	reqSet := map[string]bool{}
	for _, r := range req {
		reqSet[r.(string)] = true
	}
	for _, key := range []string{"title", "source", "prompt_template", "mode"} {
		if !reqSet[key] {
			t.Fatalf("create required 缺 %q: %v", key, req)
		}
	}

	// create 桥 execute 端到端:input 形状 → 云端 body → 结果 JSON。
	cap := newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path == "/api/agent/listeners" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"listener":{"id":"L-br","status":"active"}}`))
		}
	})
	res := create.Execute(context.Background(), map[string]interface{}{
		"title":           "盯构建",
		"source":          map[string]interface{}{"type": "file_tail", "device_id": "", "params": map[string]interface{}{"path": "/tmp/b.log", "match": "ERROR"}},
		"prompt_template": "命中了 {{event}} 请处理",
		"mode":            "recurring",
	}, nil)
	if res.IsError {
		t.Fatalf("bridge create execute 失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"L-br"`) {
		t.Fatalf("结果须含 id: %s", res.Content)
	}
	calls := cap.snapshot()
	if len(calls) != 1 {
		t.Fatalf("应恰一笔云端调用: %+v", calls)
	}
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(calls[0].Body, &sent)
	if string(sent["target_session_id"]) != `"sess-bridge"` {
		t.Fatalf("桥面 create 须回填 run 会话 id: %s", calls[0].Body)
	}

	// list 桥 execute:空表也须是合法 JSON 数组。
	list := names["listener_list"]
	res = list.Execute(context.Background(), map[string]interface{}{}, nil)
	if res.IsError || !strings.HasPrefix(strings.TrimSpace(res.Content), "[") {
		t.Fatalf("list execute = %s (isErr=%v)", res.Content, res.IsError)
	}
}

// readOnly run 只保读面 list(create/update/delete 剥离)——任务拍板的
// 细粒度门控,宽于 peer 族「readOnly 全剥」惯例,注入点因此在 readOnly 块外。
func TestAgentCoreListenerToolsReadOnlyKeepsListOnly(t *testing.T) {
	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	tools := m.agentCoreListenerTools(agentAIRun{sessionID: "sess-ro", readOnly: true})
	if len(tools) != 1 || tools[0].Name() != "listener_list" {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name())
		}
		t.Fatalf("readOnly 须只剩 listener_list: %v", names)
	}
}

// 审批模式解析:内存会话表优先,journal 头行兜底(重启后会话未回内存)——
// 与聊天层 create/message 的取法同源(agent_ai.go approval_mode 回填)。
func TestListenerToolApprovalModeResolution(t *testing.T) {
	t.Run("memory registry hit", func(t *testing.T) {
		m := &agentAIManager{
			sessions: map[string]*agentAISession{"sess-1": {approvalMode: agentAIApprovalModeAskAll}},
		}
		if got := m.listenerToolApprovalModeOf("sess-1"); got != agentAIApprovalModeAskAll {
			t.Fatalf("mode = %q", got)
		}
	})

	t.Run("journal head fallback after restart", func(t *testing.T) {
		journal := newAgentCoreJournal(t.TempDir())
		journal.ensureSessionHeader("sess-2", "/p", "m", "", "2026-11-21T00:00:00Z", "phone", nil, nil, agentAIApprovalModeAskAll)
		m := &agentAIManager{coreJournal: journal}
		if got := m.listenerToolApprovalModeOf("sess-2"); got != agentAIApprovalModeAskAll {
			t.Fatalf("journal 兜底 mode = %q", got)
		}
	})

	t.Run("unknown session resolves empty", func(t *testing.T) {
		m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
		if got := m.listenerToolApprovalModeOf("sess-nope"); got != "" {
			t.Fatalf("未知会话须空串(不猜): %q", got)
		}
	})
}

// --- 5. config 总闸 ----------------------------------------------------------

func TestAgentCoreListenerToolsConfigGate(t *testing.T) {
	// 默认开:nil/未配置与 true 等价(逐字对齐 EffectivePeerMCPEnabled 家族)。
	var c config.Config
	if !c.EffectiveEventListenersEnabled() {
		t.Fatal("nil Config 应默认开启")
	}
	c.Customer = &config.CustomerConfig{}
	if !c.EffectiveEventListenersEnabled() {
		t.Fatal("未配置应默认开启")
	}
	off := false
	c.Customer.EventListeners = &config.EventListenersConfig{Enable: &off}
	if c.EffectiveEventListenersEnabled() {
		t.Fatal("显式 false 应关闭")
	}
	on := true
	c.Customer.EventListeners = &config.EventListenersConfig{Enable: &on}
	if !c.EffectiveEventListenersEnabled() {
		t.Fatal("显式 true 应开启")
	}

	// 关闸 → 注入面返回空(零工具)。
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{EventListeners: &config.EventListenersConfig{Enable: &off}}})
	t.Cleanup(config.ResetGlobalConfigForTest)
	m := &agentAIManager{coreJournal: newAgentCoreJournal(t.TempDir())}
	if tools := m.agentCoreListenerTools(agentAIRun{sessionID: "sess-off"}); len(tools) != 0 {
		t.Fatalf("关闸须零注入: %d tools", len(tools))
	}
}
