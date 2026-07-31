package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aliang.one/nursorgate/app/http/middleware"
	auth "aliang.one/nursorgate/processor/auth"
)

func TestWriteAuthResultIssuesDashboardCookieOnSuccess(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	auth.SetCurrentUserInfo(&auth.UserInfo{ID: 11, Username: "liang", AccessToken: "access", TokenType: "Bearer"})
	authority.NotifyLoggedIn(auth.GetCurrentUserInfo())
	middleware.ResetDashboardSessionForTest()
	t.Cleanup(func() {
		auth.SetCurrentUserInfo(nil)
		auth.ResetSessionAuthorityForTest()
		middleware.ResetDashboardSessionForTest()
	})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "192.0.2.10:40000"
	rec := httptest.NewRecorder()
	writeAuthResult(rec, req, map[string]interface{}{"status": "success"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != middleware.DashboardSessionCookieName {
		t.Fatalf("successful login cookies = %#v", cookies)
	}
}

func TestAuthHandlerRejectsRemoteSessionBootstrapWithoutCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req.RemoteAddr = "192.0.2.10:40000"
	rec := httptest.NewRecorder()

	NewAuthHandler().HandleRestoreSession(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthHandlerBootstrapsLoopbackManagementSession(t *testing.T) {
	middleware.ResetDashboardSessionForTest()
	t.Cleanup(middleware.ResetDashboardSessionForTest)
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/session", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()

	NewAuthHandler().HandleDashboardSessionBootstrap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != middleware.DashboardSessionCookieName {
		t.Fatalf("bootstrap cookies = %#v", cookies)
	}
}
