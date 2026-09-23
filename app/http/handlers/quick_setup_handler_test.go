package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/middleware"
	auth "aliang.one/nursorgate/processor/auth"
)

func TestQuickSetupHandlerRejectsOversizedRequestBeforeDecode(t *testing.T) {
	body := `{"software":"opencode","files":[{"path":"~/.config/opencode/opencode.json","content":"` + strings.Repeat("a", quickSetupRequestMaxBytes) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/apply", strings.NewReader(body))
	req.AddCookie(issueQuickSetupTestSession(t))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleApply(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("body = %s, want request size error", rec.Body.String())
	}
}

func TestQuickSetupHandlerRequiresDashboardSessionForEveryEndpoint(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	auth.SetCurrentUserInfo(&auth.UserInfo{ID: 10, Username: "global-user", AccessToken: "global-access", TokenType: "Bearer"})
	authority.NotifyLoggedIn(auth.GetCurrentUserInfo())
	middleware.ResetDashboardSessionForTest()
	t.Cleanup(func() {
		auth.SetCurrentUserInfo(nil)
		auth.ResetSessionAuthorityForTest()
		middleware.ResetDashboardSessionForTest()
	})

	handler := NewQuickSetupHandler()
	tests := []struct {
		name   string
		method string
		path   string
		handle http.HandlerFunc
	}{
		{name: "catalog", method: http.MethodGet, path: "/api/quick-setup/catalog", handle: handler.HandleCatalog},
		{name: "models", method: http.MethodPost, path: "/api/quick-setup/models", handle: handler.HandleModels},
		{name: "render", method: http.MethodPost, path: "/api/quick-setup/render", handle: handler.HandleRender},
		{name: "apply", method: http.MethodPost, path: "/api/quick-setup/apply", handle: handler.HandleApply},
		{name: "config-state", method: http.MethodGet, path: "/api/quick-setup/config-state", handle: handler.HandleConfigState},
		{name: "restore", method: http.MethodPost, path: "/api/quick-setup/restore", handle: handler.HandleRestore},
		{name: "combos-create", method: http.MethodPost, path: "/api/quick-setup/combos", handle: handler.HandleCombosCreate},
		{name: "combos-update", method: http.MethodPut, path: "/api/quick-setup/combos/999", handle: handler.HandleCombosUpdate},
		{name: "combos-delete", method: http.MethodDelete, path: "/api/quick-setup/combos/999", handle: handler.HandleCombosDelete},
		{name: "combos-set-default", method: http.MethodPost, path: "/api/quick-setup/combos/999/default", handle: handler.HandleCombosSetDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			tt.handle(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestQuickSetupHandlerRestoreRejectsOversizedRequestBeforeDecode 镜像 apply 的
// 认证用例（镜像现有 handler 测试模式）：MaxBytesReader 必须在 decode 前生效。
func TestQuickSetupHandlerRestoreRejectsOversizedRequestBeforeDecode(t *testing.T) {
	body := `{"software":"` + strings.Repeat("a", quickSetupRequestMaxBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/restore", strings.NewReader(body))
	req.AddCookie(issueQuickSetupTestSession(t))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleRestore(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("body = %s, want request size error", rec.Body.String())
	}
}

// TestQuickSetupHandlerRestoreRequiresSoftware 认证会话下空 software → 400。
func TestQuickSetupHandlerRestoreRequiresSoftware(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/restore", strings.NewReader(`{"software":"  "}`))
	req.AddCookie(issueQuickSetupTestSession(t))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleRestore(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "software is required") {
		t.Fatalf("body = %s, want software required error", rec.Body.String())
	}
}

// TestQuickSetupHandlerCombosCreateRejectsUnsupportedSoftware 认证会话下非法
// software → 400。service 在触库前完成 software 校验，本用例无需 store 注入。
func TestQuickSetupHandlerCombosCreateRejectsUnsupportedSoftware(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/combos", strings.NewReader(`{"software":"nope","name":"套餐","source":"blank"}`))
	req.AddCookie(issueQuickSetupTestSession(t))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleCombosCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "is not supported") {
		t.Fatalf("body = %s, want unsupported software error", rec.Body.String())
	}
}

// TestQuickSetupHandlerCombosCreateRequiresName 认证会话下空白组合名 → 400
// （service 在触库前报 "combo name is required"，无需 store 注入）。
func TestQuickSetupHandlerCombosCreateRequiresName(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/combos", strings.NewReader(`{"software":"codex","name":"   ","source":"blank"}`))
	req.AddCookie(issueQuickSetupTestSession(t))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleCombosCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "combo name is required") {
		t.Fatalf("body = %s, want combo name required error", rec.Body.String())
	}
}

// TestQuickSetupHandlerCombosCreateRejectsEmptyBody 认证会话下空 body → 400
// （decode 失败分支）。
func TestQuickSetupHandlerCombosCreateRejectsEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/quick-setup/combos", strings.NewReader(""))
	req.AddCookie(issueQuickSetupTestSession(t))
	rec := httptest.NewRecorder()

	NewQuickSetupHandler().HandleCombosCreate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid request body") {
		t.Fatalf("body = %s, want invalid body error", rec.Body.String())
	}
}

// TestQuickSetupHandlerCombosRejectNonNumericID 认证会话下 {id} 非数字 → 400，
// 覆盖 update/delete/set-default 三个带 {id} 路由的解析分支（无需触库）。
func TestQuickSetupHandlerCombosRejectNonNumericID(t *testing.T) {
	handler := NewQuickSetupHandler()
	tests := []struct {
		name   string
		method string
		path   string
		handle http.HandlerFunc
	}{
		{name: "update", method: http.MethodPut, path: "/api/quick-setup/combos/abc", handle: handler.HandleCombosUpdate},
		{name: "delete", method: http.MethodDelete, path: "/api/quick-setup/combos/abc", handle: handler.HandleCombosDelete},
		{name: "set-default", method: http.MethodPost, path: "/api/quick-setup/combos/abc/default", handle: handler.HandleCombosSetDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{}`))
			req.SetPathValue("id", "abc")
			req.AddCookie(issueQuickSetupTestSession(t))
			rec := httptest.NewRecorder()
			tt.handle(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "id") {
				t.Fatalf("body = %s, want invalid id error", rec.Body.String())
			}
		})
	}
}

func issueQuickSetupTestSession(t *testing.T) *http.Cookie {
	t.Helper()
	authority := auth.ResetSessionAuthorityForTest()
	auth.SetCurrentUserInfo(&auth.UserInfo{ID: 9, Username: "tester", AccessToken: "access", TokenType: "Bearer"})
	authority.NotifyLoggedIn(auth.GetCurrentUserInfo())
	middleware.ResetDashboardSessionForTest()
	t.Cleanup(func() {
		auth.SetCurrentUserInfo(nil)
		auth.ResetSessionAuthorityForTest()
		middleware.ResetDashboardSessionForTest()
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	if err := middleware.IssueDashboardSession(rec, req); err != nil {
		t.Fatalf("issue dashboard session: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("issued cookies = %#v", cookies)
	}
	return cookies[0]
}
