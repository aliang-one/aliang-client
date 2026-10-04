package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestHandleAgentAuthRejectedNotificationRules(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	t.Cleanup(func() {
		recoverOrExpireLocalSession = auth.RecoverOrExpireLocalSession
		lastAppliedNotifyUnix.Store(0)
		auth.ResetSessionAuthorityForTest()
		auth.SetCurrentUserInfo(nil)
	})

	var recoverCalls []string
	recoverOrExpireLocalSession = func(reason string) { recoverCalls = append(recoverCalls, reason) }

	// resetApplyDedup clears the 60s apply-dedup window so a subtest that
	// expects an immediate apply is not rate-limited by a previous subtest.
	resetApplyDedup := func() { lastAppliedNotifyUnix.Store(0) }

	loginActive := func() {
		auth.SetCurrentUserInfo(&auth.UserInfo{ID: 11, Username: "liang", AccessToken: "access", TokenType: "Bearer"})
		authority.NotifyLoggedIn(auth.GetCurrentUserInfo())
	}

	postNotification := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/agent-auth-rejected", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:40000"
		rec := httptest.NewRecorder()
		NewAuthHandler().HandleAgentAuthRejected(rec, req)
		return rec
	}

	decodeApplied := func(t *testing.T, rec *httptest.ResponseRecorder) (applied bool, ignored string) {
		t.Helper()
		var envelope struct {
			Code int `json:"code"`
			Data struct {
				Applied bool   `json:"applied"`
				Ignored string `json:"ignored"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("failed to decode response %q: %v", rec.Body.String(), err)
		}
		if envelope.Code != 0 {
			t.Fatalf("unexpected error envelope %q", rec.Body.String())
		}
		return envelope.Data.Applied, envelope.Data.Ignored
	}

	t.Run("cross-process generation mismatch applies despite stale copy", func(t *testing.T) {
		// 跨进程语义回归（生产实证 2026-09-22 12:44 的反演）：agent 子进程的
		// session authority 懒初始化把 generation 定格在 1 且 non-owner 从不
		// publish；owner 每次登录/刷新 publish 时 +1。owner 若用 generation
		// 等值门，agent 的 generation=1 通知被恒判 stale 丢弃，通知链
		// dead-on-arrival。现契约：generation 仅是日志信息，Active 门+时间窗
		// 承担防回退——陈旧代的真拒绝必须被应用。
		recoverCalls = nil
		resetApplyDedup()
		loginActive()
		loginActive()
		loginActive() // owner authority 已推到 generation ≥ 3
		if gen := authority.Snapshot().Generation; gen < 3 {
			t.Fatalf("owner generation = %d, want >= 3", gen)
		}

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":1}`, time.Now().Unix()))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if !applied || ignored != "" {
			t.Fatalf("applied=%v ignored=%q, want applied=true without ignore reason; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 1 || recoverCalls[0] != "agent register 401" {
			t.Fatalf("recoverCalls = %v, want exactly [agent register 401]", recoverCalls)
		}
	})

	t.Run("restoring snapshot applies a recovery kick", func(t *testing.T) {
		// 2026-10-02 生产实证的反演:owner 卡在 Restoring 数日(恢复在途或
		// 恢复循环静默卡死),agent 的凭据拒绝通知被 Active 门一刀切判
		// stale_notification 拒绝,agent 落 refresh_invalid 粘性禁用 3 天、
		// 全程无用户可见提示。恢复链幂等且被 arbiter 串行化,Restoring 下
		// 放行是安全的「踢一脚」:刷新成功→Active→自动转发新会话;刷新
		// 401→HardInvalid→禁用 agent+桌面提醒。无论哪个分支都比静默强。
		recoverCalls = nil
		resetApplyDedup()
		// Fresh authority: StateRestoring — the owner never reached Active.
		authority = auth.ResetSessionAuthorityForTest()
		if state := authority.Snapshot().State; state != auth.StateRestoring {
			t.Fatalf("authority state = %v, want restoring", state)
		}

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":1}`, time.Now().Unix()))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if !applied || ignored != "" {
			t.Fatalf("applied=%v ignored=%q, want applied=true without ignore reason; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 1 || recoverCalls[0] != "agent register 401" {
			t.Fatalf("recoverCalls = %v, want exactly [agent register 401]", recoverCalls)
		}
	})

	t.Run("generation-carrying notification with stale observation ignored", func(t *testing.T) {
		recoverCalls = nil
		resetApplyDedup()
		loginActive()
		generation := authority.Snapshot().Generation
		sixMinutesAgo := time.Now().Add(-6 * time.Minute).Unix()

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":%d}`, sixMinutesAgo, generation))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if applied || ignored != "stale_notification" {
			t.Fatalf("applied=%v ignored=%q, want applied=false ignored=stale_notification; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 0 {
			t.Fatalf("recoverCalls = %v, want none", recoverCalls)
		}
	})

	t.Run("active generation applies", func(t *testing.T) {
		recoverCalls = nil
		resetApplyDedup()
		loginActive()
		generation := authority.Snapshot().Generation

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":%d}`, time.Now().Unix(), generation))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if !applied || ignored != "" {
			t.Fatalf("applied=%v ignored=%q, want applied=true without ignore reason; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 1 || recoverCalls[0] != "agent register 401" {
			t.Fatalf("recoverCalls = %v, want exactly [agent register 401]", recoverCalls)
		}
	})

	t.Run("missing generation applies when snapshot active and observation fresh", func(t *testing.T) {
		recoverCalls = nil
		resetApplyDedup()
		loginActive()

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":0}`, time.Now().Unix()))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, _ := decodeApplied(t, rec)
		if !applied {
			t.Fatalf("applied=false, want true; body=%s", rec.Body.String())
		}
		if len(recoverCalls) != 1 || recoverCalls[0] != "agent register 401" {
			t.Fatalf("recoverCalls = %v, want exactly [agent register 401]", recoverCalls)
		}
	})

	t.Run("second applicable notification within window rate limited", func(t *testing.T) {
		recoverCalls = nil
		resetApplyDedup()
		loginActive()
		generation := authority.Snapshot().Generation

		first := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":%d}`, time.Now().Unix(), generation))
		if recCode := first.Code; recCode != http.StatusOK {
			t.Fatalf("first notification status = %d, want 200; body=%s", recCode, first.Body.String())
		}
		applied, ignored := decodeApplied(t, first)
		if !applied || ignored != "" {
			t.Fatalf("first notification applied=%v ignored=%q, want applied=true without ignore reason; body=%s", applied, ignored, first.Body.String())
		}

		second := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":%d}`, time.Now().Unix(), generation))
		if recCode := second.Code; recCode != http.StatusOK {
			t.Fatalf("second notification status = %d, want 200; body=%s", recCode, second.Body.String())
		}
		applied, ignored = decodeApplied(t, second)
		if applied || ignored != "rate_limited" {
			t.Fatalf("second notification applied=%v ignored=%q, want applied=false ignored=rate_limited; body=%s", applied, ignored, second.Body.String())
		}
		if len(recoverCalls) != 1 {
			t.Fatalf("recoverCalls = %v, want exactly one recovery", recoverCalls)
		}
	})

	t.Run("missing generation with stale observation ignored", func(t *testing.T) {
		recoverCalls = nil
		loginActive()
		sixMinutesAgo := time.Now().Add(-6 * time.Minute).Unix()

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":0}`, sixMinutesAgo))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if applied || ignored != "stale_notification" {
			t.Fatalf("applied=%v ignored=%q, want applied=false ignored=stale_notification; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 0 {
			t.Fatalf("recoverCalls = %v, want none", recoverCalls)
		}
	})

	t.Run("missing generation with future observation ignored", func(t *testing.T) {
		recoverCalls = nil
		resetApplyDedup()
		loginActive()
		tenMinutesAhead := time.Now().Add(10 * time.Minute).Unix()

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":0}`, tenMinutesAhead))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if applied || ignored != "stale_notification" {
			t.Fatalf("applied=%v ignored=%q, want applied=false ignored=stale_notification; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 0 {
			t.Fatalf("recoverCalls = %v, want none", recoverCalls)
		}
	})

	t.Run("soft-expired snapshot applies a recovery kick", func(t *testing.T) {
		// SoftExpired 的恢复协调器可能已在退避等待;agent 的拒绝通知此刻到达
		// 应立即触发一次恢复(幂等、单飞),而不是等下一个退避周期。
		recoverCalls = nil
		resetApplyDedup()
		loginActive()
		authority.NotifyAccessRejected("probe access rejection")
		if state := authority.Snapshot().State; state != auth.StateSoftExpired {
			t.Fatalf("authority state = %v, want soft_expired", state)
		}

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":%d}`, time.Now().Unix(), authority.Snapshot().Generation))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if !applied || ignored != "" {
			t.Fatalf("applied=%v ignored=%q, want applied=true without ignore reason; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 1 || recoverCalls[0] != "agent register 401" {
			t.Fatalf("recoverCalls = %v, want exactly [agent register 401]", recoverCalls)
		}
	})

	t.Run("unauthenticated snapshot ignored", func(t *testing.T) {
		// 登出是用户的显式决定:Unauthenticated 无会话可恢复,agent 通知不得
		// 把登出翻案成恢复(否则一次迟到的通知就能复活已注销的会话)。
		recoverCalls = nil
		authority = auth.ResetSessionAuthorityForTest()
		authority.NotifyLoggedOut()
		if state := authority.Snapshot().State; state != auth.StateUnauthenticated {
			t.Fatalf("authority state = %v, want unauthenticated", state)
		}

		rec := postNotification(fmt.Sprintf(`{"reason":"agent register 401","device_id":"dev-1","observed_at":%d,"generation":0}`, time.Now().Unix()))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		applied, ignored := decodeApplied(t, rec)
		if applied || ignored != "stale_notification" {
			t.Fatalf("applied=%v ignored=%q, want applied=false ignored=stale_notification; body=%s", applied, ignored, rec.Body.String())
		}
		if len(recoverCalls) != 0 {
			t.Fatalf("recoverCalls = %v, want none", recoverCalls)
		}
	})

	t.Run("invalid body rejected", func(t *testing.T) {
		recoverCalls = nil
		loginActive()

		rec := postNotification("{not-json")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed JSON status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}

		rec = postNotification(`{"reason":"agent register 401","device_id":"dev-1","observed_at":0,"generation":0}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("missing observed_at status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if len(recoverCalls) != 0 {
			t.Fatalf("recoverCalls = %v, want none", recoverCalls)
		}
	})
}
