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
