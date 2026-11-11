package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/config"
)

// agent-as-MCP 工具面三端点的 handler 层纪律（spec §7）。
//
// 全部用例要么 t.Setenv(AgentRuntimeEnv,"1") 让 proxyIfNeeded 直通，要么把
// config.DefaultUserAgentAddr 换成 httptest stub —— 本包没有 services 包那条
// 「把 UserAgentAddr 拧成 127.0.0.1:0」的 TestMain 护栏（2026-10-27 真出过
// 测试 POST 到 56433 掐断用户 AI run 的事故），漏掉任一条就会真的打到本机
// 活体 agent，而这批端点一旦接通就是真的起 run。

// aimcpDecodeEnvelope 解公共信封，返回 (code, data)。
func aimcpDecodeEnvelope(t *testing.T, body []byte) (int, map[string]interface{}) {
	t.Helper()
	var env struct {
		Code int                    `json:"code"`
		Msg  string                 `json:"msg"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v; body=%s", err, body)
	}
	return env.Code, env.Data
}

// TestHandleAIMCPMethodNotAllowed 钉 method 闸：非 POST 必须是真 405。
// common.Error 收业务码，直传 405 会被折成 400/500，所以这里必须是
// http.Error 的真实状态码（同 HandlePeerMcp 惯例）。
func TestHandleAIMCPMethodNotAllowed(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}

	for name, handler := range map[string]http.HandlerFunc{
		"start":   h.HandleAIMCPStart,
		"status":  h.HandleAIMCPStatus,
		"approve": h.HandleAIMCPApprove,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/agent/ai/mcp/"+name, nil)
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("GET code = %d, want 405; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHandleAIMCPOpSuccessEnvelope 钉成功信封形状：{code:0,data:{result:"<json字符串>"}}。
// 形状错整次工具调用报废 —— cmd/mcp.go 的 invokeTool 只解包 data.result 字符串。
func TestHandleAIMCPOpSuccessEnvelope(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}

	want := map[string]interface{}{"session_id": "x", "accepted": true}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/ai/mcp/start", strings.NewReader(`{"content":"hi"}`))
	rec := httptest.NewRecorder()
	h.handleAIMCPOp(rec, req, func(ctx context.Context, body map[string]string) (map[string]interface{}, int, error) {
		if body["content"] != "hi" {
			t.Fatalf("解码后的 body 丢了 content: %#v", body)
		}
		return want, http.StatusOK, nil
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	code, data := aimcpDecodeEnvelope(t, rec.Body.Bytes())
	if code != 0 {
		t.Fatalf("信封 code = %d, want 0", code)
	}
	raw, ok := data["result"].(string)
	if !ok {
		t.Fatalf("data.result 不是字符串: %#v（invokeTool 只解包字符串，形状错了整个工具调用报废）", data["result"])
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("data.result 二次 Unmarshal 失败: %v; raw=%s", err, raw)
	}
	if got["session_id"] != "x" || got["accepted"] != true {
		t.Fatalf("回解 payload = %#v, want %#v", got, want)
	}
}

// TestHandleAIMCPOpErrorStatusMapping 钉四档错误分流：op 返回的 HTTP 语义码
// 必须 1:1 变成真实状态码，尤其 503 不能落成 500、405（另有用例）不能落成 400。
func TestHandleAIMCPOpErrorStatusMapping(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")

	for _, want := range []int{
		http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusConflict,
		http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(want), func(t *testing.T) {
			h := &AgentHandler{}
			req := httptest.NewRequest(http.MethodPost, "/api/agent/ai/mcp/status", strings.NewReader(`{"session_id":"s"}`))
			rec := httptest.NewRecorder()
			h.handleAIMCPOp(rec, req, func(ctx context.Context, body map[string]string) (map[string]interface{}, int, error) {
				return nil, want, errors.New("boom")
			})
			if rec.Code != want {
				t.Fatalf("op status=%d → 实际 HTTP %d，want %d; body=%s", want, rec.Code, want, rec.Body.String())
			}
		})
	}
}

// TestHandleAIMCPOpBodyTooLarge 钉 64KiB 上限：超限由 MaxBytesReader 在解码
// 阶段报错，必须落 400 而不是 500。
func TestHandleAIMCPOpBodyTooLarge(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "1")
	h := &AgentHandler{}

	huge := `{"content":"` + strings.Repeat("a", 70<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent/ai/mcp/start", strings.NewReader(huge))
	rec := httptest.NewRecorder()
	h.handleAIMCPOp(rec, req, func(ctx context.Context, body map[string]string) (map[string]interface{}, int, error) {
		t.Fatal("超限请求体不应进入 op")
		return nil, 0, nil
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAIMCPOpProxiesToRuntime 钉 core 侧代理：非 runtime 进程必须把
// 请求（path/method/body 原样）转发到 config.DefaultUserAgentAddr，并把 stub
// 响应原样回给调用方。测完还原地址，防止污染同包其它用例。
func TestHandleAIMCPOpProxiesToRuntime(t *testing.T) {
	t.Setenv(services.AgentRuntimeEnv, "0")

	type captured struct {
		method string
		path   string
		body   string
	}
	got := make(chan captured, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- captured{method: r.Method, path: r.URL.Path, body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"result":"{\"session_id\":\"stub\"}"}}`))
	}))
	defer stub.Close()

	orig := config.DefaultUserAgentAddr
	config.DefaultUserAgentAddr = strings.TrimPrefix(stub.URL, "http://")
	t.Cleanup(func() { config.DefaultUserAgentAddr = orig })

	h := &AgentHandler{client: &http.Client{}}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/ai/mcp/start", strings.NewReader(`{"content":"hi","project_path":"/tmp"}`))
	rec := httptest.NewRecorder()
	h.handleAIMCPOp(rec, req, func(ctx context.Context, body map[string]string) (map[string]interface{}, int, error) {
		t.Fatal("core 侧请求不应在本进程内被处理（未走代理）")
		return nil, 0, nil
	})

	select {
	case c := <-got:
		if c.method != http.MethodPost {
			t.Fatalf("转发 method = %s, want POST", c.method)
		}
		if c.path != "/api/agent/ai/mcp/start" {
			t.Fatalf("转发 path = %s, want /api/agent/ai/mcp/start", c.path)
		}
		if !strings.Contains(c.body, `"content":"hi"`) {
			t.Fatalf("转发 body 丢了原文: %s", c.body)
		}
	default:
		t.Fatal("请求没有被转发到 stub")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `{\"session_id\":\"stub\"}`) {
		t.Fatalf("stub 响应没有原样回到调用方: %s", rec.Body.String())
	}
}
