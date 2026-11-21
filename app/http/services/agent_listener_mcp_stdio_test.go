package services

// agent_listener_mcp_stdio_test.go 监听器 stdio MCP 面 invoke 层单测(plan
// Task 13):真链 invoke 函数 → Task 12 共享处理器 → fake cloud(复用
// agent_listener_api_test.go 脚手架)。钉三件事:
//  1. create 参数抽取形状(source.params 对象透传 / device 归本机 / 云端
//     调用形状 / 零上下文不产 approval_warning);
//  2. 1a 必填错误带 400 档位(供 handlers 层选信封码);
//  3. list/update/delete 的透传形状(patch 白名单、id 进路径)。
//
// 缝纪律(同 Task 7-12):测试串行,禁 t.Parallel;swap 期间不得有在跑
// watcher;Cleanup 还原全部 seam。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// I2:错误档位分类器全分支表测(档位供 handlers 层选信封码,msg 恒为原文)。
func TestListenerMCPErrorStatusMatrix(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"offline sentinel", errListenerCloudOffline, http.StatusServiceUnavailable},
		{"offline wrapped keeps sentinel", fmt.Errorf("wrap: %w: detail", errListenerCloudOffline), http.StatusServiceUnavailable},
		{"cloud 409 conflict", &listenerCloudStatusError{status: http.StatusConflict, body: []byte(`{"msg":"lww"}`)}, http.StatusConflict},
		{"cloud 422 schema", &listenerCloudStatusError{status: 422, body: []byte(`{"msg":"schema"}`)}, http.StatusBadRequest},
		{"cloud 500 upstream", &listenerCloudStatusError{status: http.StatusInternalServerError, body: []byte(`{}`)}, http.StatusServiceUnavailable},
		{"local validation", fmt.Errorf("invalid mode"), http.StatusBadRequest},
		{"1a required", fmt.Errorf("target_session_id 必填"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := listenerMCPErrorStatus(tc.err); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

// M3:云端业务错误文本携带 body 摘录——两个 LLM 面的模型靠它看到 LWW 409 /
// 422 schema 的真实原因,而不是裸状态码复读。空白压平、按 rune 截 200、
// 空 body 不留悬空冒号。
func TestListenerCloudStatusErrorExcerpt(t *testing.T) {
	e := &listenerCloudStatusError{status: 409, body: []byte("{\n  \"msg\": \"updated_at mismatch\",\n  \"listener\": \"L-1\"\n}")}
	got := e.Error()
	if !strings.Contains(got, "status 409") || !strings.Contains(got, `"msg": "updated_at mismatch"`) {
		t.Fatalf("摘录须含状态码与压平后的 body: %q", got)
	}

	long := strings.Repeat("字", 350)
	e = &listenerCloudStatusError{status: 422, body: []byte(long)}
	got = e.Error()
	idx := strings.LastIndex(got, ": ")
	if idx < 0 {
		t.Fatalf("截断形态错误: %q", got)
	}
	if r := []rune(got[idx+2:]); len(r) > 200 {
		t.Fatalf("摘录超长: %d runes", len(r))
	}

	e = &listenerCloudStatusError{status: 500}
	if got := e.Error(); got != "agent server listeners: status 500" {
		t.Fatalf("空 body 不得留悬空冒号: %q", got)
	}
}

func TestListenerMCPInvokeCreate_ArgExtractionAndCloudShape(t *testing.T) {
	var gotBody []byte
	var gotAuth, gotDevice string
	newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path == "/api/agent/listeners" {
			gotBody = body
			gotAuth = r.Header.Get("Authorization")
			gotDevice = r.Header.Get("X-Aliang-Device-ID")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"listener":{"id":"L-agent","status":"active"}}`))
			return
		}
		http.NotFound(w, r)
	})

	res, status, err := ListenerMCPCreate(context.Background(), map[string]interface{}{
		"title":             "盯服务",
		"source":            map[string]interface{}{"type": "http_poll", "device_id": "dev-ignored", "params": map[string]interface{}{"url": "https://example.com/health", "interval_s": 30}},
		"prompt_template":   "服务状态:{{event}}",
		"mode":              "recurring",
		"target_session_id": "sess-target-1",
	})
	if err != nil {
		t.Fatalf("invoke create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("成功档位 = %d, want 200", status)
	}
	result, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("create 结果须为 map: %T", res)
	}
	if result["id"] != "L-agent" || result["status"] != "active" || result["target_session_id"] != "sess-target-1" {
		t.Fatalf("create 结果形状: %+v", result)
	}
	// 零上下文(approvalModeOf=nil)永不产 approval_warning:审批策略警示是
	// agentcore 会话内语义,stdio 面无会话可查,不猜。
	if _, has := result["approval_warning"]; has {
		t.Fatal("stdio 零上下文不得附 approval_warning")
	}
	// 云端调用形状:设备面 POST + 与远端握手同源凭据。
	if gotAuth != "Bearer proxy-jwt" || gotDevice != "dev-proxy-1" {
		t.Fatalf("云端凭据头 = %q / %q", gotAuth, gotDevice)
	}
	var sent struct {
		Title  string `json:"title"`
		Source struct {
			Type     string                 `json:"type"`
			DeviceID string                 `json:"device_id"`
			Params   map[string]interface{} `json:"params"`
		} `json:"source"`
		Mode            string `json:"mode"`
		TargetSessionID string `json:"target_session_id"`
	}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode cloud body: %v", err)
	}
	if sent.Title != "盯服务" || sent.Source.Type != "http_poll" || sent.Mode != "recurring" || sent.TargetSessionID != "sess-target-1" {
		t.Fatalf("云端 body 抽取形状: %+v", sent)
	}
	// source.params 对象整体透传(interval_s 数值保真),device 由 ViaCloud 层
	// 钉本机(http_poll 的入参 device_id 被忽略——server 侧源,无设备语义)。
	if sent.Source.DeviceID != "dev-proxy-1" {
		t.Fatalf("device_id 须由 ViaCloud 钉本机: %q", sent.Source.DeviceID)
	}
	if sent.Source.Params["url"] != "https://example.com/health" || sent.Source.Params["interval_s"] != float64(30) {
		t.Fatalf("params 对象须透传: %+v", sent.Source.Params)
	}
}

func TestListenerMCPInvokeCreate_MissingTargetSessionID(t *testing.T) {
	newListenerToolTestEnv(t, nil)
	_, status, err := ListenerMCPCreate(context.Background(), map[string]interface{}{
		"title":           "盯构建",
		"source":          map[string]interface{}{"type": "http_poll", "params": map[string]interface{}{"url": "https://example.com/health"}},
		"prompt_template": "构建状态:{{event}}",
		"mode":            "once",
	})
	if err == nil || !strings.Contains(err.Error(), "target_session_id") {
		t.Fatalf("stdio 面零上下文缺 target_session_id 须触发 1a fail-fast: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("1a fail-fast 档位 = %d, want 400", status)
	}
}

func TestListenerMCPInvokeListRowsPassthrough(t *testing.T) {
	newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/agent/listeners" {
			_, _ = w.Write([]byte(`{"listeners":[{"id":"L-1","title":"盯日志","source_type":"file_tail","status":"active","updated_at":"2026-11-21T00:00:00Z"}]}`))
			return
		}
		http.NotFound(w, r)
	})
	res, status, err := ListenerMCPList(context.Background(), nil)
	if err != nil {
		t.Fatalf("invoke list: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("成功档位 = %d", status)
	}
	rows, ok := res.([]map[string]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("list 结果须为行数组: %T %+v", res, res)
	}
	if rows[0]["id"] != "L-1" || rows[0]["source_type"] != "file_tail" || rows[0]["updated_at"] != "2026-11-21T00:00:00Z" {
		t.Fatalf("镜像类型钉契约的键须保真: %+v", rows[0])
	}
}

func TestListenerMCPInvokeUpdatePatchWhitelistAndDelete(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, body
		_, _ = w.Write([]byte(`{}`))
	})

	res, status, err := ListenerMCPUpdate(context.Background(), map[string]interface{}{
		"id":    "L-9",
		"title": "新标题",
		"junk":  "白名单外的键必须被丢弃",
	})
	if err != nil {
		t.Fatalf("invoke update: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("成功档位 = %d", status)
	}
	if m, ok := res.(map[string]interface{}); !ok || m["id"] != "L-9" || m["updated"] != true {
		t.Fatalf("update 结果形状: %+v", res)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/listeners/L-9" {
		t.Fatalf("update 云端调用 = %s %s", gotMethod, gotPath)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if sent["title"] != "新标题" {
		t.Fatalf("patch 键须保留: %+v", sent)
	}
	if _, has := sent["junk"]; has {
		t.Fatal("白名单外的键必须被丢弃(共享处理器白名单闸)")
	}
	if _, has := sent["id"]; has {
		t.Fatal("id 不得混进 patch body")
	}

	// delete:id 进路径、用户面 DELETE、结果 {id, deleted:true}。
	res, status, err = ListenerMCPDelete(context.Background(), map[string]interface{}{"id": "L-9"})
	if err != nil {
		t.Fatalf("invoke delete: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("成功档位 = %d", status)
	}
	if m, ok := res.(map[string]interface{}); !ok || m["id"] != "L-9" || m["deleted"] != true {
		t.Fatalf("delete 结果形状: %+v", res)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/listeners/L-9" {
		t.Fatalf("delete 云端调用 = %s %s", gotMethod, gotPath)
	}
}
