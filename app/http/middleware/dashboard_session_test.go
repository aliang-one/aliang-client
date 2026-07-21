package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

func TestDashboardSessionRequiresIssuedCookieAndRevokesOnHardInvalid(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	auth.SetCurrentUserInfo(&auth.UserInfo{ID: 7, Username: "liang", AccessToken: "access", TokenType: "Bearer"})
	authority.NotifyLoggedIn(auth.GetCurrentUserInfo())
	ResetDashboardSessionForTest()
	t.Cleanup(func() {
		auth.SetCurrentUserInfo(nil)
		auth.ResetSessionAuthorityForTest()
		ResetDashboardSessionForTest()
	})

	request := httptest.NewRequest(http.MethodGet, "/api/quick-setup/catalog", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	if ValidateDashboardSession(request) {
		t.Fatal("request without dashboard cookie was authorized")
	}

	recorder := httptest.NewRecorder()
	if err := IssueDashboardSession(recorder, request); err != nil {
		t.Fatalf("IssueDashboardSession() error = %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != DashboardSessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("issued cookies = %#v", cookies)
	}
	request.AddCookie(cookies[0])
	if !ValidateDashboardSession(request) {
		t.Fatal("issued dashboard session was rejected")
	}
	authority.NotifyAccessRejected("temporary upstream failure")
	if !ValidateDashboardSession(request) {
		t.Fatal("soft-expired user session discarded dashboard authorization")
	}

	authority.NotifyRefreshFailed(true, auth.ReasonRefreshInvalid)
	if ValidateDashboardSession(request) {
		t.Fatal("hard-invalid session retained dashboard authorization")
	}
}

func TestCanBootstrapDashboardSessionOnlyFromLoopback(t *testing.T) {
	loopback := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	loopback.RemoteAddr = "[::1]:40000"
	if !CanBootstrapDashboardSession(loopback) {
		t.Fatal("loopback request cannot bootstrap dashboard session")
	}

	remote := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	remote.RemoteAddr = "192.0.2.10:40000"
	if CanBootstrapDashboardSession(remote) {
		t.Fatal("remote request without cookie can bootstrap dashboard session")
	}
}
