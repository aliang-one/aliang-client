package user

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifyAccessTokenFailure_KnownPatterns(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{
			name:       "TOKEN_REVOKED code",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"TOKEN_REVOKED","message":"token revoked"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "TOKEN_REVOKED reason",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":0,"reason":"TOKEN_REVOKED","message":"revoked"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "token has been revoked in message",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"OTHER","message":"Token has been revoked (password changed)"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "ACCESS_TOKEN_INVALID code",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"ACCESS_TOKEN_INVALID","message":"bad token"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "ACCESS_TOKEN_INVALID reason",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":0,"reason":"ACCESS_TOKEN_INVALID","message":"bad"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "invalid access token in message",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"OTHER","message":"invalid access token"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "TOKEN_EXPIRED code",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"TOKEN_EXPIRED","message":"expired"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "TOKEN_EXPIRED reason",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":0,"reason":"TOKEN_EXPIRED","message":"expired"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "token expired in message",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"OTHER","message":"token expired"}`,
			wantErr:    ErrSessionExpired,
		},
		{
			name:       "expired token in message",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"OTHER","message":"expired token"}`,
			wantErr:    ErrSessionExpired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyAccessTokenFailure(tt.statusCode, []byte(tt.body))
			if !errors.Is(got, tt.wantErr) {
				t.Errorf("classifyAccessTokenFailure() = %v, want %v", got, tt.wantErr)
			}
		})
	}
}

func TestClassifyAccessTokenFailure_InvalidAuthentication(t *testing.T) {
	body := `{"code":0,"message":"invalid authentication"}`
	got := classifyAccessTokenFailure(http.StatusUnauthorized, []byte(body))
	if !errors.Is(got, ErrSessionExpired) {
		t.Errorf("classifyAccessTokenFailure() = %v, want ErrSessionExpired", got)
	}
}

func TestClassifyAccessTokenFailure_UnknownJSON401(t *testing.T) {
	body := `{"code":"SOME_UNKNOWN_CODE","message":"something unexpected"}`
	got := classifyAccessTokenFailure(http.StatusUnauthorized, []byte(body))
	if !errors.Is(got, ErrSessionExpired) {
		t.Errorf("classifyAccessTokenFailure() = %v, want ErrSessionExpired (fallback)", got)
	}
}

func TestClassifyAccessTokenFailure_Non401(t *testing.T) {
	got := classifyAccessTokenFailure(http.StatusForbidden, []byte(`{"code":"TOKEN_REVOKED"}`))
	if got != nil {
		t.Errorf("classifyAccessTokenFailure() = %v, want nil for non-401", got)
	}

	got = classifyAccessTokenFailure(http.StatusOK, nil)
	if got != nil {
		t.Errorf("classifyAccessTokenFailure() = %v, want nil for 200", got)
	}
}

func TestClassifyAccessTokenFailure_InvalidJSON401(t *testing.T) {
	got := classifyAccessTokenFailure(http.StatusUnauthorized, []byte(`this is not json`))
	if !errors.Is(got, ErrSessionExpired) {
		t.Errorf("classifyAccessTokenFailure() = %v, want ErrSessionExpired for unparseable 401", got)
	}
}

func TestClassifyAccessTokenFailure_EmptyBody401(t *testing.T) {
	got := classifyAccessTokenFailure(http.StatusUnauthorized, []byte(``))
	if !errors.Is(got, ErrSessionExpired) {
		t.Errorf("classifyAccessTokenFailure() = %v, want ErrSessionExpired for empty 401 body", got)
	}
}

func TestClassifyRefreshSessionFailure_KnownPatterns(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{
			name:       "REFRESH_TOKEN_INVALID reason",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":401,"reason":"REFRESH_TOKEN_INVALID","message":"invalid refresh token"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "invalid refresh token in message",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":401,"reason":"OTHER","message":"invalid refresh token"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "non-401 returns nil",
			statusCode: http.StatusForbidden,
			body:       `{"code":401,"reason":"REFRESH_TOKEN_INVALID"}`,
			wantErr:    nil,
		},
		{
			name:       "unknown 401 is terminal",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":401,"reason":"OTHER","message":"something else"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "invalid JSON 401 is terminal",
			statusCode: http.StatusUnauthorized,
			body:       `not json`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		// The auth backend's scan/st_ refresh path returns a bare {"error": ...}
		// with no code/reason/message fields. The classifier must still recognize
		// it as a terminal refresh-token rejection — otherwise RestoreSession
		// silently serves stale cached user info and the UI shows logged-in
		// forever while every background refresh 401s.
		{
			name:       "error field: refresh token is no longer valid",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"refresh token is no longer valid"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "error field: refresh token expired",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"refresh token expired"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "error field: refresh token has been revoked",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"refresh token has been revoked"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "error field: local session is no longer valid",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"local session is no longer valid"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
		{
			name:       "code string REFRESH_TOKEN_INVALID",
			statusCode: http.StatusUnauthorized,
			body:       `{"code":"REFRESH_TOKEN_INVALID","error":"bad"}`,
			wantErr:    ErrRefreshTokenInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyRefreshSessionFailure(tt.statusCode, []byte(tt.body))
			if tt.wantErr == nil {
				if got != nil {
					t.Errorf("classifyRefreshSessionFailure() = %v, want nil", got)
				}
			} else if !errors.Is(got, tt.wantErr) {
				t.Errorf("classifyRefreshSessionFailure() = %v, want %v", got, tt.wantErr)
			}
		})
	}
}
