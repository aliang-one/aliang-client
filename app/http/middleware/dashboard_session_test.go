package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

// TestMain 把会话持久化文件重定向到临时目录：本包任何测试都不得触碰真实 ~/.aliang 状态。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dashboard-session-test")
	if err != nil {
		panic(err)
	}
	previous := dashboardSessionFilePathFn
	dashboardSessionFilePathFn = func() (string, error) {
		return filepath.Join(dir, "dashboard_sessions.json"), nil
	}
	code := m.Run()
	dashboardSessionFilePathFn = previous
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestDashboardSessionIsIndependentFromUpstreamAuthState(t *testing.T) {
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
	if !ValidateDashboardSession(request) {
		t.Fatal("hard-invalid upstream session discarded local management authorization")
	}
}

func TestIssueDashboardSessionAllowsLoopbackWhileRestoring(t *testing.T) {
	auth.ResetSessionAuthorityForTest()
	ResetDashboardSessionForTest()
	t.Cleanup(ResetDashboardSessionForTest)

	request := httptest.NewRequest(http.MethodPost, "/api/dashboard/session", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	if err := IssueDashboardSession(recorder, request); err != nil {
		t.Fatalf("loopback bootstrap while restoring: %v", err)
	}
	request.AddCookie(recorder.Result().Cookies()[0])
	if !ValidateDashboardSession(request) {
		t.Fatal("restoring dashboard bootstrap cookie was rejected")
	}
}

func TestDashboardSessionsFromTwoClientsRemainValid(t *testing.T) {
	auth.ResetSessionAuthorityForTest()
	ResetDashboardSessionForTest()
	t.Cleanup(ResetDashboardSessionForTest)

	first := httptest.NewRequest(http.MethodPost, "/api/dashboard/session", nil)
	first.RemoteAddr = "127.0.0.1:40001"
	firstRecorder := httptest.NewRecorder()
	if err := IssueDashboardSession(firstRecorder, first); err != nil {
		t.Fatalf("issue first dashboard session: %v", err)
	}
	first.AddCookie(firstRecorder.Result().Cookies()[0])

	second := httptest.NewRequest(http.MethodPost, "/api/dashboard/session", nil)
	second.RemoteAddr = "127.0.0.1:40002"
	secondRecorder := httptest.NewRecorder()
	if err := IssueDashboardSession(secondRecorder, second); err != nil {
		t.Fatalf("issue second dashboard session: %v", err)
	}
	second.AddCookie(secondRecorder.Result().Cookies()[0])

	if !ValidateDashboardSession(first) {
		t.Fatal("issuing a second client session revoked the first client")
	}
	if !ValidateDashboardSession(second) {
		t.Fatal("second client dashboard session was rejected")
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


// issueDashboardSessionCookie 签发会话并返回 cookie（辅助）。
func issueDashboardSessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/quick-setup/catalog", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	if err := IssueDashboardSession(recorder, request); err != nil {
		t.Fatalf("IssueDashboardSession() error = %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("issued cookies = %#v", cookies)
	}
	return cookies[0]
}

// TestDashboardSessionPersistsAcrossRestart：会话落盘后，模拟进程重启
//（Reset 清内存与加载标记，文件保留）→ 同一 cookie 仍有效。
func TestDashboardSessionPersistsAcrossRestart(t *testing.T) {
	ResetDashboardSessionForTest()
	cookie := issueDashboardSessionCookie(t)

	// 模拟重启：清内存 + 清加载标记；持久化文件保留
	ResetDashboardSessionForTest()

	request := httptest.NewRequest(http.MethodGet, "/api/quick-setup/catalog", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	request.AddCookie(cookie)
	if !ValidateDashboardSession(request) {
		t.Fatal("persisted dashboard session was rejected after restart")
	}
}

// TestDashboardSessionExpiredEntriesDroppedOnLoad：过期条目加载即剔除。
func TestDashboardSessionExpiredEntriesDroppedOnLoad(t *testing.T) {
	ResetDashboardSessionForTest()
	cookie := issueDashboardSessionCookie(t)

	// 把文件中的有效期改写为过去
	path, err := dashboardSessionFilePathFn()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]string
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	for key := range persisted {
		persisted[key] = "2020-01-01T00:00:00Z"
	}
	rewritten, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}

	ResetDashboardSessionForTest()
	request := httptest.NewRequest(http.MethodGet, "/api/quick-setup/catalog", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	request.AddCookie(cookie)
	if ValidateDashboardSession(request) {
		t.Fatal("expired persisted session was accepted")
	}
}

// TestDashboardSessionCorruptFileStartsEmpty：损坏文件按空表启动（安全侧），
// 且此后签发正常工作。
func TestDashboardSessionCorruptFileStartsEmpty(t *testing.T) {
	ResetDashboardSessionForTest()
	path, err := dashboardSessionFilePathFn()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	ResetDashboardSessionForTest()
	cookie := issueDashboardSessionCookie(t)
	request := httptest.NewRequest(http.MethodGet, "/api/quick-setup/catalog", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	request.AddCookie(cookie)
	if !ValidateDashboardSession(request) {
		t.Fatal("issue after corrupt-file recovery was rejected")
	}
}

// TestDashboardSessionRevocationPersists：显式撤销跨重启生效（文件清空）。
func TestDashboardSessionRevocationPersists(t *testing.T) {
	ResetDashboardSessionForTest()
	cookie := issueDashboardSessionCookie(t)

	recorder := httptest.NewRecorder()
	RevokeDashboardSession(recorder)

	ResetDashboardSessionForTest()
	request := httptest.NewRequest(http.MethodGet, "/api/quick-setup/catalog", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	request.AddCookie(cookie)
	if ValidateDashboardSession(request) {
		t.Fatal("revoked dashboard session survived restart")
	}
}

// TestDashboardSessionFilePermissions：持久化文件必须 0600（凭据文件不可被其他本机用户读取）。
func TestDashboardSessionFilePermissions(t *testing.T) {
	ResetDashboardSessionForTest()
	issueDashboardSessionCookie(t)

	path, err := dashboardSessionFilePathFn()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("dashboard session file perm = %o, want 600", perm)
	}
}
