// status_ingress_handler_test.go — /api/status/ingress 的行为钉：
// 方法守卫 405（真实 HTTP 码）；本包测试环境无 reconcile 实例 → ok=false
// + 恒非 nil 空数组（前端「未运行」态渲染的数据契约）。
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIngressSessionsMethodGuard(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/status/ingress", nil)
	HandleIngressSessions(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestIngressSessionsNotRunningShape(t *testing.T) {
	// 测试进程未跑 SSH 入口 reconcile → ok=false；两列表恒非 null。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status/ingress", nil)
	HandleIngressSessions(rec, req)

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OK     bool              `json:"ok"`
			Active []json.RawMessage `json:"active"`
			Closed []json.RawMessage `json:"closed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if envelope.Data.OK {
		t.Fatal("ok must be false when ingress is not running in test process")
	}
	if envelope.Data.Active == nil || envelope.Data.Closed == nil {
		t.Fatal("active/closed must serialize as [] not null")
	}
}
