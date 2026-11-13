package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// 锚点纪律回归测试(2026-11-12 Mac 登出事故):
// UserInfo.UpdatedAt 是续期计时锚(isTokenExpired:UpdatedAt+ExpiresIn-2h lead),
// 语义=「服务端最近一次签发/续期会话的时刻」。任何不代表服务端延期的本地写入
// (改资料、restore 时仅同步 profile)都不得改写锚点,否则续期窗口被推到服务端
// 死线之后,ticker 按被污染的锚点准点触发、撞零宽限 401 → 粘性登出。

const anchorTestProfilePayload = `{"data":{"id":1,"email":"user@example.com","username":"user","role":"member","balance":12.5,"concurrency":2,"status":"active","allowed_groups":[1,2],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-04-09T00:00:00Z"}}`

func anchorTestSetup(t *testing.T) {
	t.Helper()
	ResetAuthPersistenceForTest()
	StopTokenRefresh()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		StopTokenRefresh()
		// 共享测试库的行会跨测试存活(Reset 只重开连接不删行),必须显式删除,
		// 否则残留的 access_token 会渗进后续测试的鉴权断言。
		_ = DeleteUserInfo()
		ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
}

// SaveUserInfo 必须保留调用方给定的锚点,不得替调用方改写为 now。
func TestSaveUserInfoPreservesCallerAnchor(t *testing.T) {
	anchorTestSetup(t)

	anchor := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	if err := SaveUserInfo(&UserInfo{
		AccessToken: "st-anchor",
		ExpiresIn:   86400,
		UpdatedAt:   anchor,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	loaded, err := LoadUserInfo()
	if err != nil {
		t.Fatalf("LoadUserInfo() error = %v", err)
	}
	if loaded.UpdatedAt.Sub(anchor) > 2*time.Second || anchor.Sub(loaded.UpdatedAt) > 2*time.Second {
		t.Fatalf("SaveUserInfo rewrote the anchor: got %v, want ~%v (decoupled local writes would push the refresh window past server expiry)", loaded.UpdatedAt, anchor)
	}
}

// RestoreSession 的 profile 同步路径(refresh 失败、旧 token 拉 profile 成功)
// 不得滑动锚点:此路径服务端没有延长会话。
func TestRestoreSessionProfileSyncKeepsAnchor(t *testing.T) {
	anchorTestSetup(t)

	anchor := time.Now().Add(-22 * time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			// 非终态失败(5xx):走 profile 同步路径,但不延长服务端会话
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"upstream unavailable"}`))
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(anchorTestProfilePayload))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken: "st-old",
		ExpiresIn:   86400,
		UpdatedAt:   anchor,
		Username:    "user",
		Email:       "user@example.com",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	info, err := RestoreSession()
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if info == nil {
		t.Fatal("RestoreSession() returned nil info")
	}
	if info.UpdatedAt.Sub(anchor) > 2*time.Second || anchor.Sub(info.UpdatedAt) > 2*time.Second {
		t.Fatalf("RestoreSession profile-sync path slid the anchor: got %v, want ~%v (server did NOT extend the session here)", info.UpdatedAt, anchor)
	}

	loaded, err := LoadUserInfo()
	if err != nil {
		t.Fatalf("LoadUserInfo() error = %v", err)
	}
	if loaded.UpdatedAt.Sub(anchor) > 2*time.Second || anchor.Sub(loaded.UpdatedAt) > 2*time.Second {
		t.Fatalf("persisted anchor slid after restore profile-sync: got %v, want ~%v", loaded.UpdatedAt, anchor)
	}
}

// 改用户名不延长服务端会话,不得滑动锚点。
func TestUpdateUserProfileKeepsAnchor(t *testing.T) {
	anchorTestSetup(t)

	anchor := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/user" {
			w.Header().Set("Content-Type", "application/json")
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			payload := anchorTestProfilePayload
			if username := req["username"]; username != "" {
				payload = `{"data":{"id":1,"email":"user@example.com","username":"` + username + `","role":"member","balance":12.5,"concurrency":2,"status":"active","allowed_groups":[1,2]}}`
			}
			_, _ = w.Write([]byte(payload))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{APIServer: server.URL}})

	if err := SaveUserInfo(&UserInfo{
		AccessToken: "st-anchor",
		ExpiresIn:   86400,
		UpdatedAt:   anchor,
		Username:    "user",
		Email:       "user@example.com",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	if _, err := UpdateUserProfile("renamed"); err != nil {
		t.Fatalf("UpdateUserProfile() error = %v", err)
	}

	current := GetCurrentUserInfo()
	if current == nil {
		t.Fatal("GetCurrentUserInfo() returned nil")
	}
	if current.Username != "renamed" {
		t.Fatalf("username not updated: %q", current.Username)
	}
	if current.UpdatedAt.Sub(anchor) > 2*time.Second || anchor.Sub(current.UpdatedAt) > 2*time.Second {
		t.Fatalf("UpdateUserProfile slid the anchor: got %v, want ~%v (a profile rename does not extend the server session)", current.UpdatedAt, anchor)
	}
}
