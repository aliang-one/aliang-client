package user

import (
	"encoding/json"
	"testing"
)

// RestoreSession 成功路径直接返回 RefreshSession 的结果，
// 故恢复/注入会话自动获得上游锚点；这是对 2026-09-20 事故（13:41 恢复后被错误计时）的回归防线。
func TestEffectiveRefreshExpiresInPrefersUpstreamAnchor(t *testing.T) {
	if got := effectiveRefreshExpiresIn(86400, 27000); got != 27000 {
		t.Fatalf("upstream anchor should win: got %d", got)
	}
	if got := effectiveRefreshExpiresIn(86400, 0); got != 86400 {
		t.Fatalf("missing anchor must fall back to server expires_in: got %d", got)
	}
	if got := effectiveRefreshExpiresIn(86400, -5); got != 86400 {
		t.Fatalf("invalid anchor must fall back: got %d", got)
	}
	if got := effectiveRefreshExpiresIn(0, 0); got != 0 {
		t.Fatalf("both absent stays zero (caller falls back to constant): got %d", got)
	}
}

// TestAuthTokenEnvelopeUpstreamExpiresInWire 防线：JSON tag 拼错会让整个锚点修复静默 no-op
// （字段恒为零值，回退到服务端 expires_in），故直接对 wire 格式反序列化做断言。
func TestAuthTokenEnvelopeUpstreamExpiresInWire(t *testing.T) {
	var envelope authTokenEnvelope
	body := []byte(`{"data":{"access_token":"st_x","refresh_token":"st_x","expires_in":86400,"token_type":"Bearer","upstream_expires_in":27000}}`)
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Data.UpstreamExpiresIn != 27000 {
		t.Fatalf("upstream_expires_in tag broken: got %d, want 27000", envelope.Data.UpstreamExpiresIn)
	}
	if envelope.Data.ExpiresIn != 86400 {
		t.Fatalf("expires_in: got %d, want 86400", envelope.Data.ExpiresIn)
	}
}

// TestScanStatusResultUpstreamExpiresInWire 扫码登录路径的同名防线：official-website 在
// authorized 状态响应顶层下发 upstream_expires_in；tag 拼错则恒为 0，激活时静默回退 24h
// 常量（2026-09-20 事故残余窗口照旧），故对 wire 格式反序列化直接断言。
func TestScanStatusResultUpstreamExpiresInWire(t *testing.T) {
	var res ScanStatusResult
	body := []byte(`{"status":"authorized","expires_in":299,"interval":2,"session_token":"st_x","refresh_token":"st_x","upstream_expires_in":10800,"user":{"id":1,"email":"a@b.c","name":"A","role":"user"}}`)
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.UpstreamExpiresIn != 10800 {
		t.Fatalf("upstream_expires_in tag broken: got %d, want 10800", res.UpstreamExpiresIn)
	}
	if res.SessionToken != "st_x" || res.Status != "authorized" || res.User == nil || res.User.ID != 1 {
		t.Fatalf("adjacent fields degraded: %+v", res)
	}

	// 旧服务端不下发该键：解析成功且为 0（→ 激活时回退常量，行为兼容）。
	var legacy ScanStatusResult
	if err := json.Unmarshal([]byte(`{"status":"pending","expires_in":299,"interval":2}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.UpstreamExpiresIn != 0 {
		t.Fatalf("legacy server must parse as 0, got %d", legacy.UpstreamExpiresIn)
	}
}
