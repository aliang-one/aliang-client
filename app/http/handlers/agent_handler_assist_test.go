package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// 远程协助 P1 Task 3:gates 端点(GET 读 / POST 写总开关)。service 行为
// (零值纪律/持久化往返/关闸联动)由 services 层用例覆盖;这里钉端点契约:
// GET 回 {enabled},POST {enabled:bool} 翻转并回显新值,重启重载仍持久,
// 以及 400(缺键/坏体)/405 关卡。
//
// 隔离照 routes 包 runtime 角色用例惯例:t.Setenv(HOME)=TempDir +
// AgentRuntimeEnv=1——user-agent 分支的 agentStatePath 走 UserStateDir
// ($HOME/.aliang),不重定向 HOME 会写到开发者真实 ~/.aliang;且读写两侧
// (POST 落盘与重建重载)必须同角色,双分支路径不同(core=cache 目录,
// runtime=$HOME/.aliang),构造序保持 env 恒定。

func newAssistGatesHandler(t *testing.T) *AgentHandler {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Setenv(services.AgentRuntimeEnv, "1")
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	service := services.NewAgentService()
	return NewAgentHandler(service)
}

func assistGatesBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Code != 0 {
		t.Fatalf("response code = %d, want 0 (body: %s)", resp.Code, rec.Body.String())
	}
	return resp.Data
}

func TestHandleAssistGatesToggle(t *testing.T) {
	h := newAssistGatesHandler(t)

	// GET 初始 false(零值纪律)。
	req := httptest.NewRequest("GET", "/api/agent/assist/gates", nil)
	rec := httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 200 {
		t.Fatalf("initial GET code = %d, want 200", rec.Code)
	}
	data := assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); enabled {
		t.Fatalf("initial GET enabled = %v, want false", data["enabled"])
	}

	// POST {enabled:true} → 200 回显 true。
	req = httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	data = assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("POST echo enabled = %v, want true", data["enabled"])
	}

	// GET 再读 → true。
	req = httptest.NewRequest("GET", "/api/agent/assist/gates", nil)
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	data = assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("GET after POST enabled = %v, want true", data["enabled"])
	}

	// 重启夹具:同角色同 HOME 上重建 service(runtime 分支同一路径),
	// GET 仍 true(持久化)。
	reloaded := services.NewAgentService()
	h2 := NewAgentHandler(reloaded)
	req = httptest.NewRequest("GET", "/api/agent/assist/gates", nil)
	rec = httptest.NewRecorder()
	h2.HandleAssistGates(rec, req)
	data = assistGatesBody(t, rec)
	if enabled, _ := data["enabled"].(bool); !enabled {
		t.Fatalf("GET after reload enabled = %v, want true (persistence)", data["enabled"])
	}
}

func TestHandleAssistGatesValidation(t *testing.T) {
	h := newAssistGatesHandler(t)

	// 空体 → 400。
	req := httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("empty body code = %d, want 400", rec.Code)
	}

	// 缺 enabled 键 → 400(契约:显式布尔键)。
	req = httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("missing key code = %d, want 400", rec.Code)
	}

	// 未知形状 → 400。
	req = httptest.NewRequest("POST", "/api/agent/assist/gates", strings.NewReader(`{"on":true}`))
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 400 {
		t.Fatalf("unknown shape code = %d, want 400", rec.Code)
	}

	// 非 GET/POST → 405(同 HandlePeerGates 的 http.Error 惯例)。
	req = httptest.NewRequest("DELETE", "/api/agent/assist/gates", nil)
	rec = httptest.NewRecorder()
	h.HandleAssistGates(rec, req)
	if rec.Code != 405 {
		t.Fatalf("DELETE code = %d, want 405", rec.Code)
	}
}
