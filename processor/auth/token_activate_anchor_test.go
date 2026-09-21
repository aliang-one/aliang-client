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
