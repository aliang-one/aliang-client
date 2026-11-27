package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestInitializeUserWithoutTokenStartsTokenRefreshFromPersistedSession(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "aliang-user-init-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	// 注册最早 = 执行最晚:InitializeUser 拉起的后台协程(auth_sync 重试、
	// 日志刷盘)在本用例函数返回、各 defer 跑完之后仍可能往临时目录写文件,
	// Linux 上 t.TempDir 的 RemoveAll 撞「directory not empty」会把用例误判
	// 失败(2026-11-27 v1.1.128 CI 实证);删除改 best-effort,不影响断言。
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })
	t.Setenv("HOME", filepath.Join(baseDir, "home"))
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(baseDir, "cache"))

	defer auth.StopTokenRefresh()
	defer auth.ResetAuthPersistenceForTest()
	defer config.ResetGlobalConfigForTest()

	auth.ResetAuthPersistenceForTest()
	auth.StopTokenRefresh()
	config.ResetGlobalConfigForTest()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"access_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":3600,"token_type":"Bearer"}}`))
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"tester@example.com","username":"tester","role":"user","status":"active","allowed_groups":[1],"created_at":"2026-04-09T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "stale-access",
		RefreshToken: "stale-refresh",
		TokenType:    "Bearer",
		Username:     "tester",
		UpdatedAt:    time.Now().Add(-2 * time.Hour),
		ExpiresIn:    60,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	if err := InitializeUser(""); err != nil {
		t.Fatalf("InitializeUser() error = %v", err)
	}

	refresher := auth.GetTokenRefresher()
	if refresher == nil || !refresher.IsRunning() {
		t.Fatal("expected token refresher to be running after restoring persisted session at startup")
	}

	current := auth.GetCurrentUserInfo()
	if current == nil {
		t.Fatal("expected current user info to be loaded")
	}
	if current.AccessToken != "fresh-access" {
		t.Fatalf("access token = %q, want fresh-access", current.AccessToken)
	}
}
