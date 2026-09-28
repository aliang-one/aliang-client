package middleware

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
)

const (
	DashboardSessionCookieName = "aliang_dashboard_session"
	dashboardSessionTTL        = 24 * time.Hour
	dashboardSessionMaxActive  = 64
)

var dashboardSessionState struct {
	sync.Mutex
	sessions map[[sha256.Size]byte]time.Time
	// loaded 标记持久化文件是否已加载（惰性，首次访问时读入）。
	loaded bool
}

// dashboardSessionFilePathFn 是持久化文件路径的钩子（测试注入临时目录）。
var dashboardSessionFilePathFn = func() (string, error) {
	return cache.GetCacheFile("dashboard_sessions.json")
}

// IssueDashboardSession rotates the request-bound local management credential.
// Loopback may bootstrap it before upstream authentication; a remote browser
// may only receive it after a successful upstream login.
func IssueDashboardSession(w http.ResponseWriter, r *http.Request) error {
	if !isLoopbackRequest(r) {
		if auth.GetSessionAuthority().State() != auth.StateActive {
			return errors.New("cannot issue remote dashboard session without an active user session")
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate dashboard session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	dashboardSessionState.Lock()
	ensureDashboardSessionsLoadedLocked()
	if dashboardSessionState.sessions == nil {
		dashboardSessionState.sessions = make(map[[sha256.Size]byte]time.Time)
	}
	pruneDashboardSessionsLocked(now)
	for len(dashboardSessionState.sessions) >= dashboardSessionMaxActive {
		evictOldestDashboardSessionLocked()
	}
	dashboardSessionState.sessions[sha256.Sum256([]byte(token))] = now.Add(dashboardSessionTTL)
	persistDashboardSessionsLocked()
	dashboardSessionState.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     DashboardSessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(dashboardSessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r != nil && r.TLS != nil,
	})
	return nil
}

// RevokeDashboardSession explicitly clears the local management credential.
// Upstream logout does not call this: the local dashboard must remain able to
// observe the Unauthenticated snapshot and initiate a new login.
func RevokeDashboardSession(w http.ResponseWriter) {
	dashboardSessionState.Lock()
	clearDashboardSessionLocked()
	persistDashboardSessionsLocked()
	dashboardSessionState.Unlock()
	if w == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     DashboardSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func RequireDashboardSession(w http.ResponseWriter, r *http.Request) bool {
	if ValidateDashboardSession(r) {
		return true
	}
	common.ErrorUnauthorized(w, "Authenticated dashboard session is required")
	return false
}

func ValidateDashboardSession(r *http.Request) bool {
	if r == nil {
		return false
	}
	cookie, err := r.Cookie(DashboardSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	key := sha256.Sum256([]byte(strings.TrimSpace(cookie.Value)))
	now := time.Now()
	dashboardSessionState.Lock()
	ensureDashboardSessionsLoadedLocked()
	expiresAt, ok := dashboardSessionState.sessions[key]
	if ok && !now.Before(expiresAt) {
		delete(dashboardSessionState.sessions, key)
		persistDashboardSessionsLocked()
		ok = false
	}
	dashboardSessionState.Unlock()
	if !ok {
		return false
	}
	return true
}

// CanBootstrapDashboardSession allows persisted-session restoration only from
// loopback. Remote browsers must prove identity through an explicit login.
func CanBootstrapDashboardSession(r *http.Request) bool {
	if ValidateDashboardSession(r) {
		return true
	}
	return isLoopbackRequest(r)
}

func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func clearDashboardSessionLocked() {
	dashboardSessionState.sessions = nil
}

func pruneDashboardSessionsLocked(now time.Time) {
	for key, expiresAt := range dashboardSessionState.sessions {
		if !now.Before(expiresAt) {
			delete(dashboardSessionState.sessions, key)
		}
	}
}

func evictOldestDashboardSessionLocked() {
	var oldestKey [sha256.Size]byte
	var oldestExpiry time.Time
	found := false
	for key, expiresAt := range dashboardSessionState.sessions {
		if !found || expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = expiresAt
			found = true
		}
	}
	if found {
		delete(dashboardSessionState.sessions, oldestKey)
	}
}

func ResetDashboardSessionForTest() {
	dashboardSessionState.Lock()
	clearDashboardSessionLocked()
	dashboardSessionState.loaded = false
	dashboardSessionState.Unlock()
}

// ensureDashboardSessionsLoadedLocked 首次访问时从状态目录加载持久化会话
//（dashboard_sessions.json，0600 凭据文件）。文件缺失/损坏/无法定位一律按空表
// 启动——安全侧等价旧版「重启后需重新登录」，绝不因持久化层故障拒绝签发。
// 调用方必须持有 dashboardSessionState.Lock。
func ensureDashboardSessionsLoadedLocked() {
	if dashboardSessionState.loaded {
		return
	}
	dashboardSessionState.loaded = true
	path, err := dashboardSessionFilePathFn()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	var persisted map[string]string
	if json.Unmarshal(raw, &persisted) != nil {
		return
	}
	if dashboardSessionState.sessions == nil {
		dashboardSessionState.sessions = make(map[[sha256.Size]byte]time.Time)
	}
	now := time.Now()
	for hexKey, expiry := range persisted {
		rawKey, err := hex.DecodeString(hexKey)
		if err != nil || len(rawKey) != sha256.Size {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339, expiry)
		if err != nil || !expiresAt.After(now) {
			continue
		}
		var key [sha256.Size]byte
		copy(key[:], rawKey)
		dashboardSessionState.sessions[key] = expiresAt
	}
}

// persistDashboardSessionsLocked 把会话表原子落盘（临时文件 0600 + rename）。
// 尽力而为：失败仅意味着重启后需重新登录，不阻断签发/校验路径。
// 调用方必须持有 dashboardSessionState.Lock。
func persistDashboardSessionsLocked() {
	path, err := dashboardSessionFilePathFn()
	if err != nil {
		return
	}
	export := make(map[string]string, len(dashboardSessionState.sessions))
	for key, expiresAt := range dashboardSessionState.sessions {
		export[hex.EncodeToString(key[:])] = expiresAt.Format(time.RFC3339)
	}
	raw, err := json.Marshal(export)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dashboard_sessions-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	_, writeErr := tmp.Write(raw)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(name)
		return
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
	}
}
