package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/internal/runtimepath"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"github.com/google/shlex"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/sync/singleflight"
)

const (
	agentStatusDisabled = "disabled"
	agentStatusEnabled  = "enabled"

	agentStateFilename     = "agent_state.json"
	agentIdentityFilename  = "device_identity.json"
	agentDefaultLaunchMode = "external_terminal"
	agentHTTPTimeout       = 8 * time.Second
	agentAuthSyncTimeout   = 20 * time.Second
	agentLogoutTimeout     = 2 * time.Second
	agentStatusSyncPath    = "/api/agent/status"
	AgentRuntimeEnv        = "ALIANG_USER_AGENT_RUNTIME"

	AgentForwardedAuthorizationHeader = "X-Aliang-User-Authorization"
	AgentForwardedUserKeyHeader       = "X-Aliang-User-Key"
	AgentUserKeyHeader                = "X-Aliang-User-Key"
)

const (
	agentAuthSyncRetryWindow = 45 * time.Second
	agentAuthSyncRetryMin    = 500 * time.Millisecond
	agentAuthSyncRetryMax    = 2 * time.Second
)

var (
	agentAuthSyncGroup       singleflight.Group
	agentAuthSyncStateMu     sync.Mutex
	lastAgentSyncedAuth      string
	agentAuthSyncAttempt     = RequestUserAgentSyncAfterAuth
	agentAuthSyncSleep       = time.Sleep
	agentAuthRejectedHandler = func() {
		auth.RecoverOrExpireLocalSession("user agent rejected forwarded authorization")
	}
)

var (
	sharedAgentServiceMu sync.Mutex
	sharedAgentService   *AgentService

	localUserAgentBaseURL = UserAgentBaseURL
)

type agentState struct {
	Enabled         bool                `json:"enabled"`
	Device          *models.AgentDevice `json:"device,omitempty"`
	DeviceID        string              `json:"device_id,omitempty"`
	UniqueCode      string              `json:"unique_code,omitempty"`
	DeviceToken     string              `json:"device_token,omitempty"`
	RegisteredUser  string              `json:"registered_user,omitempty"`
	Registered      bool                `json:"registered"`
	RemoteConnected bool                `json:"remote_connected"`
	LastSyncAt      string              `json:"last_sync_at,omitempty"`
	LastSyncStatus  string              `json:"last_sync_status,omitempty"`
	LastSyncMessage string              `json:"last_sync_message,omitempty"`

	// ScanDirectories 限制上传到云端的扫描范围：开启后只有这些目录下的
	// 项目和会话才会被扫描并上传。空或 ScanDirectoriesEnabled=false 表示不过滤。
	ScanDirectories        []string `json:"scan_directories,omitempty"`
	ScanDirectoriesEnabled bool     `json:"scan_directories_enabled,omitempty"`
}

type AgentService struct {
	mu                    sync.Mutex
	state                 agentState
	identity              *agentDeviceIdentity
	policyByPath          map[string]*ApprovalPolicy
	policyLastCheckAtPath map[string]time.Time
	client                *http.Client
	terminal              *agentTerminalManager
	ai                    *agentAIManager
	// Forwarded user credentials are process-local only. The session owner sends
	// them through /api/agent/sync after login/restore/refresh; the agent never
	// reads or rotates the persisted refresh token.
	forwardedUserAuthorization string
	forwardedUserKey           string

	wsMu         sync.Mutex
	wsConnected  bool
	wsConnecting bool
	wsConn       *websocket.Conn

	// sessionRefreshSig is signalled (non-blocking) after a successful token
	// refresh; the remote-agent session loop drains it and pushes the current
	// access_token to PhoneServer as agent.session.refresh so the server's
	// recorded userTokenExp stays current. All WS writes stay within the loop.
	sessionRefreshSig chan struct{}

	// remoteWriter holds the live remote-connection writer. AI (and terminal)
	// events are emitted through currentRemoteWriter() rather than a closure
	// captured at message-arrival time, so a reconnect reattaches in-flight AI
	// runs to the new socket instead of streaming into a dead one.
	remoteWriter atomic.Pointer[agentTerminalWriter]
}

type agentDeviceTokenRejectedError struct {
	message string
}

func (e agentDeviceTokenRejectedError) Error() string {
	return e.message
}

// agentUserAuthRejectedError signals that the agent server rejected the USER
// authorization — a 401 on a call that carried the aliang JWT but no
// device_token (i.e. the device-register call). It is distinct from
// agentDeviceTokenRejectedError (device_token rejected) so the caller can
// recover the session before wiping it: a register 401 may be a stale/expired
// JWT rather than a truly dead session, and nuking the login on every such 401
// is what made "login expires very quickly".
type agentUserAuthRejectedError struct {
	status int
	body   string
}

func (e agentUserAuthRejectedError) Error() string {
	return fmt.Sprintf("agent server returned %d: %s", e.status, e.body)
}

func GetSharedAgentService() *AgentService {
	sharedAgentServiceMu.Lock()
	defer sharedAgentServiceMu.Unlock()
	if sharedAgentService == nil {
		sharedAgentService = NewAgentService()
	}
	return sharedAgentService
}

func NewAgentService() *AgentService {
	s := &AgentService{
		client:            &http.Client{Timeout: agentHTTPTimeout},
		terminal:          newAgentTerminalManager(),
		ai:                newAgentAIManager(),
		sessionRefreshSig: make(chan struct{}, 1),
	}
	s.ai.service = s
	s.ai.loadPendingTerminals()
	if err := s.loadState(); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] state_load failed error=%v", err))
	} else {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] state_load complete enabled=%t registered=%t has_device=%t has_token=%t agent_server=%s runtime=user_agent:%t",
			s.state.Enabled,
			s.state.Registered,
			s.state.Device != nil,
			strings.TrimSpace(s.state.DeviceToken) != "",
			currentAgentServerURL(),
			IsUserAgentRuntime(),
		))
	}
	return s
}

func IsUserAgentRuntime() bool {
	return strings.TrimSpace(os.Getenv(AgentRuntimeEnv)) == "1"
}

func UserAgentBaseURL() string {
	return "http://" + config.DefaultUserAgentAddr
}

func UserAgentOfflineStatus(err error) models.AgentStatusResponse {
	message := "User agent process is not running."
	if err != nil {
		message = message + " " + err.Error()
	}
	return models.AgentStatusResponse{
		Status:          "offline",
		Enabled:         false,
		Bound:           false,
		Registered:      false,
		RemoteConnected: false,
		BindingRequired: false,
		Platform:        runtime.GOOS,
		ProtocolVersion: models.AgentProtocolVersion,
		AgentServer:     currentAgentServerURL(),
		Runtime: &models.AgentRuntime{
			Online: false,
			Kind:   "user_agent",
			URL:    UserAgentBaseURL(),
		},
		Capabilities: agentCapabilities(),
		Tools:        []models.AgentTool{},
		History:      []models.AgentHistoryRoot{},
		Message:      message,
	}
}

func (s *AgentService) Status() models.AgentStatusResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()
	resp := s.statusLocked()

	status := resp.Status
	message := "Agent mode is disabled."
	if status == agentStatusEnabled {
		message = "Agent mode is enabled for this user device."
	} else if s.effectiveUserAuthorizationLocked("") == "" {
		message = "Log in before enabling Agent mode for this device."
	} else {
		message = "Agent mode can be enabled directly for this logged-in user."
	}
	resp.Message = message
	return resp
}

// VibeSessions returns the local AI coding sessions (Claude/Codex) as
// lightweight summaries, newest first. Transcript is stripped.
func (s *AgentService) VibeSessions() []models.AgentVibeSession {
	snapshot := agentSyncSnapshot{VibeSessions: summarizeAgentVibeSessions(collectAgentVibeSessions(nil))}
	if s == nil || s.ai == nil {
		return snapshot.VibeSessions
	}
	return overlayActiveAgentVibeSessions(snapshot, s.ai.activeVibeSessionsSnapshot(), nil).VibeSessions
}

// RuntimeSessions returns process-local sessions that are actively executing.
// It intentionally excludes resident but idle AI conversations and terminal history.
func (s *AgentService) RuntimeSessions() models.AgentRuntimeSnapshot {
	snapshot := models.AgentRuntimeSnapshot{
		AIConversations: []models.AgentVibeSession{},
		Terminals:       []models.AgentTerminalRuntime{},
		CollectedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if s == nil {
		return snapshot
	}
	if s.ai != nil {
		snapshot.AIConversations = s.ai.activeVibeSessionsSnapshot()
	}
	if s.terminal != nil {
		snapshot.Terminals = s.terminal.activeSessionsSnapshot()
	}
	return snapshot
}

// VibeSessionDetail returns a single session's transcript page for the
// read-only activity view. Assistant messages are capped to a one-line summary;
// system/tool messages are filtered. Cursor pagination via beforeMessageID.
func (s *AgentService) VibeSessionDetail(sessionID string, limit int, beforeMessageID string) (models.AgentVibeSession, error) {
	session := findAgentVibeSessionDetail(sessionID, "", "", agentVibeSessionReadOptions{
		Limit:           normalizeAgentVibeDetailLimit(limit),
		BeforeMessageID: beforeMessageID,
		IncludePageMeta: true,
	})
	if session.ID == "" {
		return models.AgentVibeSession{}, errors.New("vibe session not found")
	}
	session.Transcript = summarizeVibeTranscriptForDisplay(session.Transcript, agentVibeAssistantSummaryRunes)
	return session, nil
}

// ScanDirectoriesConfig returns the current scan-directory filter setting.
func (s *AgentService) ScanDirectoriesConfig() ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dirs := make([]string, len(s.state.ScanDirectories))
	copy(dirs, s.state.ScanDirectories)
	return dirs, s.state.ScanDirectoriesEnabled
}

// SetScanDirectoriesConfig updates the scan-directory filter and persists it.
// Directories are trimmed, de-duplicated, and empties dropped.
func (s *AgentService) SetScanDirectoriesConfig(dirs []string, enabled bool) ([]string, bool, error) {
	cleaned := make([]string, 0, len(dirs))
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		cleaned = append(cleaned, d)
	}
	s.mu.Lock()
	s.state.ScanDirectories = cleaned
	s.state.ScanDirectoriesEnabled = enabled
	err := s.saveStateLocked()
	s.mu.Unlock()
	return cleaned, enabled, err
}

func (s *AgentService) Enable() (models.AgentStatusResponse, error) {
	return s.EnableWithAuthorization("")
}

func (s *AgentService) EnableWithAuthorization(authHeader string) (models.AgentStatusResponse, error) {
	return s.enableWithUserContext(authHeader, "")
}

func (s *AgentService) EnableWithUserContext(authHeader string, userKey string) (models.AgentStatusResponse, error) {
	return s.enableWithUserContext(authHeader, userKey)
}

func (s *AgentService) enableWithUserContext(authHeader string, userKey string) (models.AgentStatusResponse, error) {
	s.mu.Lock()
	authHeader, userKey, _ = s.resolveForwardedUserContextLocked(authHeader, userKey)
	s.ensureDeviceIdentityLocked()
	if effectiveAgentRegisterAuthHeader(authHeader) == "" {
		err := errors.New("log in before enabling Agent mode for this device")
		s.state.Enabled = false
		s.state.Registered = false
		s.state.RemoteConnected = false
		s.state.LastSyncStatus = "login_required"
		s.state.LastSyncMessage = err.Error()
		_ = s.saveStateLocked()
		status := s.statusLocked()
		s.mu.Unlock()
		return status, err
	}
	if err := s.registerAndSyncLockedWithUserContext(authHeader, userKey); err != nil {
		var authRejectedErr agentUserAuthRejectedError
		isAuthRejectedErr := errors.As(err, &authRejectedErr)
		s.state.LastSyncStatus = "enable_failed"
		s.state.LastSyncMessage = err.Error()
		_ = s.saveStateLocked()
		status := s.statusLocked()
		s.mu.Unlock()
		// Register-path 401 during an explicit enable: still try a recovery
		// refresh before letting the session die (the JWT may be stale). Run
		// outside s.mu — see recoverOrExpireAfterRegisterAuthRejection.
		if isAuthRejectedErr {
			s.recoverOrExpireAfterRegisterAuthRejection(err)
		}
		return status, err
	}
	hasToken := strings.TrimSpace(s.state.DeviceToken) != ""
	status := s.statusLocked()
	s.mu.Unlock()
	if hasToken {
		go func() {
			if err := s.EnsureRemoteConnection(); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-BOOT] enable remote_connection_start_failed error=%v", err))
			}
		}()
	}
	return status, nil
}

func (s *AgentService) Disable() models.AgentStatusResponse {
	return s.DisableWithReason("manual")
}

func (s *AgentService) DisableWithReason(reason string) models.AgentStatusResponse {
	return s.disableWithReasonMessage(reason, "")
}

// disableWithReasonMessage is the implementation behind DisableWithReason. A
// non-empty message overrides the generic per-reason LastSyncMessage so callers
// can persist the server's actual rejection detail (e.g. the 401 payload),
// which surfaces as an informative registration_message. message empty falls
// back to agentDisableMessage(reason).
func (s *AgentService) disableWithReasonMessage(reason string, message string) models.AgentStatusResponse {
	reason = normalizeAgentDisableReason(reason)
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	s.state.Enabled = false
	s.state.Device = nil
	s.state.DeviceToken = ""
	s.state.RegisteredUser = ""
	s.state.Registered = false
	s.state.RemoteConnected = false
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = reason
	if reason == "logout" {
		s.forwardedUserAuthorization = ""
		s.forwardedUserKey = ""
	}
	if strings.TrimSpace(message) != "" {
		s.state.LastSyncMessage = message
	} else {
		s.state.LastSyncMessage = agentDisableMessage(reason)
	}
	_ = s.saveStateLocked()
	status := s.statusLocked()
	s.mu.Unlock()

	s.forceDisconnectRemote(reason)
	return status
}

// RecoverIfAuthExpired re-enables the agent if and only if it is currently
// disabled because of an auth_expired session. A successful token refresh
// (the caller of this method) means we hold a fresh, valid JWT again, so the
// reason for the terminal disable is gone: re-enabling re-registers the device
// with the fresh JWT (via Enable → registerAndSync) and reconnects the remote
// WS. This turns a one-hit "permanent offline until manual re-login" into a
// self-healing blip.
//
// It is a deliberate no-op for every other disable reason — logout is a
// deliberate user action, device_unbound means the device was unbound, and
// device_token_invalid means the token is still rejected; none are resolved by
// a refresh, so auto-re-enabling would undo an intentional state or loop.
func (s *AgentService) RecoverIfAuthExpired() (models.AgentStatusResponse, error) {
	s.mu.Lock()
	reason := normalizeAgentDisableReason(s.state.LastSyncStatus)
	enabled := s.state.Enabled
	status := s.statusLocked()
	s.mu.Unlock()
	if enabled || reason != "auth_expired" {
		return status, nil
	}
	logger.Info("[AGENT-BOOT] auth_recover re-enabling agent reason=auth_expired (refresh succeeded)")
	return s.Enable()
}

// ReRegisterIfUserIdentityChanged binds the device to the current JWT user when
// the persisted registration identity no longer matches. Corrects two cases that
// otherwise leave a device bound to the wrong (or no) owner:
//   - The agent started before the JWT was available and never registered
//     (login_required) — now the JWT is here, so register.
//   - The agent registered under a fallback identity (e.g. admin-console) before
//     the JWT loaded — clear that token and re-register under the real user.
//
// Called after a successful token refresh. No-op when already registered as the
// current user, or when there is still no JWT.
func (s *AgentService) ReRegisterIfUserIdentityChanged() {
	s.mu.Lock()
	authHeader, userKey, _ := s.resolveForwardedUserContextLocked("", "")
	if authHeader == "" {
		s.mu.Unlock()
		return // JWT still not available
	}
	currentUserKey := agentRegistrationUserKey(userKey, authHeader)
	registeredUser := strings.TrimSpace(s.state.RegisteredUser)
	hasToken := strings.TrimSpace(s.state.DeviceToken) != ""
	mismatch := hasToken && registeredUser != "" && registeredUser != currentUserKey
	if mismatch {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] identity_changed re-registering registered_user=%s current_user=%s device_id=%s",
			registeredUser, currentUserKey, s.state.DeviceID))
		s.state.Device = nil
		s.state.DeviceToken = ""
		s.state.Registered = false
		s.state.RemoteConnected = false
		s.state.RegisteredUser = ""
		_ = s.saveStateLocked()
	}
	s.mu.Unlock()
	if !hasToken || mismatch {
		if err := s.SyncNowWithUserContext(authHeader, userKey); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] identity_changed re-register failed error=%v", err))
		}
	}
}

func normalizeAgentDisableReason(reason string) string {
	reason = strings.TrimSpace(strings.ToLower(reason))
	switch reason {
	case "", "manual":
		return "disabled"
	case "logout":
		return "logout"
	case "auth_expired":
		return "auth_expired"
	case "device_token_invalid":
		return "device_token_invalid"
	case "device_unbound":
		return "device_unbound"
	default:
		return reason
	}
}

func agentDisableMessage(reason string) string {
	switch normalizeAgentDisableReason(reason) {
	case "logout":
		return "Agent mode was disabled after logout."
	case "auth_expired":
		return "Agent mode was disabled because the session expired."
	case "device_token_invalid":
		return "Agent mode was disabled because the device token was rejected."
	case "device_unbound":
		return "Agent mode was disabled because the device was unbound."
	default:
		return "Agent mode is disabled."
	}
}

func (s *AgentService) SyncNow() error {
	return s.SyncNowWithAuthorization("")
}

func (s *AgentService) SyncNowWithAuthorization(authHeader string) error {
	return s.SyncNowWithUserContext(authHeader, "")
}

// shouldBlockBackgroundSyncLocked reports whether a background sync (one with no
// forwarded JWT) must be skipped because the agent is in a deliberate or
// server-asserted sticky-disabled state. Caller holds s.mu.
//
// This is intentionally a NARROWER check than shouldPreserveDisabledStatus:
// that one (used by the remote-connection loop) treats a fresh/empty state as
// "disabled" via normalizeAgentDisableReason, which is harmless there because
// both loop branches exit anyway. Here it would be wrong — a fresh agent
// (LastSyncStatus="") must be allowed to perform its first register. So we test
// the already-normalized stored reason directly: only explicit
// disable/logout/rejection states block; empty (never synced) does not.
func (s *AgentService) shouldBlockBackgroundSyncLocked() bool {
	switch strings.TrimSpace(strings.ToLower(s.state.LastSyncStatus)) {
	case "disabled", "logout", "auth_expired", "device_token_invalid", "device_unbound":
		return true
	default:
		return false
	}
}

func (s *AgentService) SyncNowWithUserContext(authHeader string, userKey string) error {
	s.mu.Lock()
	hasExplicitAuth := strings.TrimSpace(authHeader) != ""
	authHeader, userKey, authChanged := s.resolveForwardedUserContextLocked(authHeader, userKey)
	s.ensureDeviceIdentityLocked()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] sync_now begin device_id=%s enabled=%t registered=%t has_token=%t agent_server=%s runtime=user_agent:%t",
		s.state.DeviceID,
		s.state.Enabled,
		s.state.Registered,
		strings.TrimSpace(s.state.DeviceToken) != "",
		currentAgentServerURL(),
		IsUserAgentRuntime(),
	))
	// A background sync must not undo a deliberate disable (logout / manual /
	// device_token_invalid / device_unbound / auth_expired) — those states are
	// sticky until cleared by an explicit action. The one exception is a sync
	// that carries an explicitly forwarded JWT (authHeader != ""): that means
	// the dashboard just authenticated the user (login / restore / refresh) and
	// is the legitimate re-enable signal. A sync with no forwarded JWT falls
	// back to the agent's OWN cached aliang.data token, which after a logout is
	// stale — letting it re-register is exactly the "agent reconnects after
	// logout" self-resurrection. Explicit Enable() bypasses this guard.
	if !hasExplicitAuth && s.shouldBlockBackgroundSyncLocked() {
		stickyState := normalizeAgentDisableReason(s.state.LastSyncStatus)
		s.mu.Unlock()
		logger.Info(fmt.Sprintf("[AGENT-BOOT] sync_now skipped reason=sticky_disabled_no_forwarded_auth state=%s", stickyState))
		return nil
	}
	if err := s.registerAndSyncLockedWithUserContext(authHeader, userKey); err != nil {
		var deviceTokenErr agentDeviceTokenRejectedError
		var authRejectedErr agentUserAuthRejectedError
		isDeviceTokenErr := errors.As(err, &deviceTokenErr)
		isAuthRejectedErr := errors.As(err, &authRejectedErr)
		// Neither a device-token rejection nor a user-auth rejection is a server
		// availability problem; leave their dedicated states untouched.
		if !isDeviceTokenErr && !isAuthRejectedErr {
			s.state.LastSyncStatus = "server_unavailable"
			s.state.LastSyncMessage = err.Error()
			_ = s.saveStateLocked()
		}
		s.mu.Unlock()
		// Register-path 401: try a recovery refresh before letting the session
		// die. Done outside s.mu — RefreshSession's auth-success hook re-takes
		// it (ReRegisterIfUserIdentityChanged).
		if isAuthRejectedErr {
			s.recoverOrExpireAfterRegisterAuthRejection(err)
		}
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] sync_now failed error=%v", err))
		return err
	}
	hasToken := strings.TrimSpace(s.state.DeviceToken) != ""
	enabled := s.state.Enabled
	registered := s.state.Registered
	deviceID := s.state.DeviceID
	s.mu.Unlock()
	if authChanged {
		s.PushSessionRefresh()
	}
	if !hasToken {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] sync_now complete remote_connection=skipped reason=no_device_token device_id=%s registered=%t enabled=%t", deviceID, registered, enabled))
		return nil
	}
	if err := s.EnsureRemoteConnection(); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] sync_now remote_connection_start_failed device_id=%s error=%v", deviceID, err))
		return err
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] sync_now complete remote_connection=start_requested device_id=%s registered=%t enabled=%t", deviceID, registered, enabled))
	return nil
}

func RequestUserAgentSyncAfterAuth(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "auth"
	}
	authHeader := effectiveAgentRegisterAuthHeader("")
	if authHeader == "" {
		err := errors.New("user authorization is not available for agent device registration")
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_sync skipped reason=%s error=%v", reason, err))
		return err
	}

	logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_sync begin reason=%s runtime=user_agent:%t", reason, IsUserAgentRuntime()))
	var err error
	userKey := CurrentAgentRegisterUserKey()
	if IsUserAgentRuntime() {
		err = GetSharedAgentService().SyncNowWithUserContext(authHeader, userKey)
	} else {
		err = requestLocalUserAgentSyncAfterAuth(reason, authHeader, userKey)
	}
	if err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_sync failed reason=%s error=%v", reason, err))
		return err
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_sync success reason=%s", reason))
	return nil
}

// SyncUserAgentAfterAuthWithRetry is the session-owner side of the agent
// bootstrap handshake. Login can be handled by the root core before the
// login-user watchdog has started the user-agent process, so a single POST to
// 56433 is not sufficient. Concurrent login/restore/refresh triggers collapse
// into one flight, and transient local/remote failures are retried until the
// user-agent has had enough time to become ready.
func SyncUserAgentAfterAuthWithRetry(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "auth"
	}

	_, err, _ := agentAuthSyncGroup.Do("session-owner-agent-sync", func() (interface{}, error) {
		authHeader := effectiveAgentRegisterAuthHeader("")
		if authHeader == "" {
			return nil, errors.New("user authorization is not available for agent device registration")
		}

		force := shouldForceAgentAuthSync(reason)
		agentAuthSyncStateMu.Lock()
		alreadySynced := !force && lastAgentSyncedAuth == authHeader
		agentAuthSyncStateMu.Unlock()
		if alreadySynced {
			logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_sync coalesced reason=%s", reason))
			return nil, nil
		}

		deadline := time.Now().Add(agentAuthSyncRetryWindow)
		delay := agentAuthSyncRetryMin
		attempt := 0
		for {
			attempt++
			err := agentAuthSyncAttempt(reason)
			if err == nil {
				agentAuthSyncStateMu.Lock()
				lastAgentSyncedAuth = effectiveAgentRegisterAuthHeader("")
				agentAuthSyncStateMu.Unlock()
				logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_sync converged reason=%s attempts=%d", reason, attempt))
				return nil, nil
			}

			if isForwardedUserAuthorizationRejected(err) && auth.IsSessionOwnerProcess() {
				logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_sync owner_recovery_requested reason=%s error=%v", reason, err))
				agentAuthRejectedHandler()
				return nil, err
			}
			if !isRetryableAgentAuthSyncError(err) || time.Now().Add(delay).After(deadline) {
				return nil, err
			}

			logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_sync retrying reason=%s attempt=%d delay=%s error=%v", reason, attempt, delay, err))
			agentAuthSyncSleep(delay)
			delay *= 2
			if delay > agentAuthSyncRetryMax {
				delay = agentAuthSyncRetryMax
			}
		}
	})
	return err
}

func shouldForceAgentAuthSync(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "agent_started") ||
		strings.Contains(reason, "agent_restarted") ||
		strings.Contains(reason, "watchdog")
}

func isRetryableAgentAuthSyncError(err error) bool {
	if err == nil {
		return false
	}
	var responseErr *localUserAgentResponseError
	if errors.As(err, &responseErr) {
		return responseErr.StatusCode >= http.StatusInternalServerError
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused",
		"connection reset",
		"broken pipe",
		"unexpected eof",
		"deadline exceeded",
		"timeout",
		"no such host",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isForwardedUserAuthorizationRejected(err error) bool {
	var responseErr *localUserAgentResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusUnauthorized {
		return false
	}
	text := strings.ToLower(responseErr.Body)
	return strings.Contains(text, "authentication_required") ||
		strings.Contains(text, "missing_bearer_token") ||
		strings.Contains(text, "user authorization")
}

func RequestUserAgentStartupSync(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "startup"
	}
	authHeader := effectiveAgentRegisterAuthHeader("")
	userKey := CurrentAgentRegisterUserKey()
	if authHeader == "" {
		userKey = agentRegistrationUserKey(userKey, authHeader)
	}

	logger.Info(fmt.Sprintf("[AGENT-BOOT] startup_sync_request begin reason=%s runtime=user_agent:%t has_auth=%t admin_console_fallback=%t",
		reason,
		IsUserAgentRuntime(),
		authHeader != "",
		shouldUseAdminConsoleAgentRegistration(authHeader, false),
	))
	var err error
	if IsUserAgentRuntime() {
		err = GetSharedAgentService().SyncNowWithUserContext(authHeader, userKey)
	} else {
		err = requestLocalUserAgentSyncAfterAuth(reason, authHeader, userKey)
	}
	if err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] startup_sync_request failed reason=%s error=%v", reason, err))
		return err
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] startup_sync_request success reason=%s", reason))
	return nil
}

func RequestUserAgentDisableAfterLogout(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "logout"
	}
	go func() {
		if IsUserAgentRuntime() {
			_ = GetSharedAgentService().DisableWithReason(reason)
		} else if err := requestLocalUserAgentDisableAfterLogout(reason); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_disable local_user_agent_failed reason=%s error=%v", reason, err))
			return
		}
		logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_disable applied reason=%s runtime=user_agent:%t", reason, IsUserAgentRuntime()))
	}()
}

// RequestUserAgentRecoverAfterAuthExpired is the self-heal counterpart to
// RequestUserAgentDisableAfterLogout. It is invoked from the auth-success hook
// (handleAuthRefreshed) after a token refresh succeeds: if the agent was left
// in the terminal auth_expired state by a prior refresh failure, a fresh valid
// JWT means that state is stale — re-enable and reconnect. Cross-process safe,
// mirroring the disable dispatcher (in-process under the user-agent runtime,
// otherwise an HTTP POST to the local user-agent server).
func RequestUserAgentRecoverAfterAuthExpired() {
	go func() {
		if IsUserAgentRuntime() {
			if _, err := GetSharedAgentService().RecoverIfAuthExpired(); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_recover failed error=%v", err))
				return
			}
			logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_recover applied runtime=user_agent:%t", IsUserAgentRuntime()))
		} else if err := requestLocalUserAgentRecoverAfterAuthExpired(); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_recover local_user_agent_failed error=%v", err))
		}
	}()
}

// RequestUserAgentEnsureConnection (re)establishes the remote agent link after
// the user session is restored (token refresh / login). Idempotent: a no-op when
// the WS is already connected or connecting, or when there is no device_token /
// the agent is disabled. Cross-process safe — mirrors the disable/sync
// dispatchers (in-process under the user-agent runtime, otherwise an HTTP POST
// to the local user-agent server's /api/agent/reconnect).
func RequestUserAgentEnsureConnection() {
	go func() {
		if IsUserAgentRuntime() {
			if err := GetSharedAgentService().EnsureRemoteConnection(); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_connection failed error=%v", err))
				return
			}
			logger.Info("[AGENT-BOOT] ensure_connection applied runtime=user_agent:true")
			return
		}
		if err := requestLocalUserAgentEnsureConnection(); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_connection local_user_agent_failed error=%v", err))
		}
	}()
}

func (s *AgentService) Tools() []models.AgentTool {
	return detectAgentTools()
}

func (s *AgentService) Launch(req models.AgentLaunchRequest) (*models.AgentLaunchResponse, error) {
	s.mu.Lock()
	enabled := s.isEnabledLocked()
	terminalEnabled := s.state.Device == nil || s.state.Device.RemoteTerminalEnabled
	s.mu.Unlock()
	if !enabled {
		return nil, errors.New("agent mode is not enabled for this device")
	}
	if !terminalEnabled {
		return nil, errors.New("remote terminal is disabled for this device")
	}

	spec, err := resolveAgentLaunchSpec(req)
	if err != nil {
		return nil, err
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = agentDefaultLaunchMode
	}
	if mode != agentDefaultLaunchMode {
		return nil, fmt.Errorf("unsupported launch mode: %s", mode)
	}

	cwd := strings.TrimSpace(req.CWD)
	if cwd != "" {
		if expanded, err := cache.ExpandHomePath(cwd); err == nil {
			cwd = expanded
		}
		if stat, err := os.Stat(cwd); err != nil || !stat.IsDir() {
			return nil, fmt.Errorf("working directory is not available: %s", cwd)
		}
	}

	sessionID := "agent-" + uuid.NewString()
	if err := launchInExternalTerminal(spec.Path, spec.Args, cwd); err != nil {
		return nil, err
	}

	return &models.AgentLaunchResponse{
		SessionID: sessionID,
		Tool:      spec.ToolID,
		Command:   commandDisplay(spec.Path, spec.Args),
		CWD:       cwd,
		Mode:      mode,
		Status:    "launched",
		Message:   "Command launched in a user terminal.",
	}, nil
}

// Derived registration/connection health enums. These are the authoritative
// answers to "is this device correctly registered with the server?" and "what
// is the live link state?". The legacy booleans (Registered/RemoteConnected) are
// optimistic and kept only for backward compatibility.
const (
	registrationNotConfigured = "not_configured" // agent server URL is empty
	registrationLoginRequired = "login_required" // no user JWT (or session expired)
	registrationRejected      = "rejected"       // server explicitly refused the binding/token
	registrationRegistered    = "registered"     // server accepted, token cached and not rejected
	registrationUnregistered  = "unregistered"   // never registered (no token, no explicit refusal)

	connectionConnected    = "connected"    // websocket link is up
	connectionConnecting   = "connecting"   // dialing / handshaking
	connectionError        = "error"        // link failed or dropped with an error
	connectionDisconnected = "disconnected" // idle / not connected
)

// deriveRegistrationStateLocked maps the raw agent state to the registration
// health enum. Registration is a property of the DEVICE credential
// (device_token), NOT of the user session: a cached device_token means the
// server issued and has not withdrawn this device's credential, so the device is
// registered regardless of the current user-session status. This is the fix for
// the "logged in but agent offline" inconsistency — a transient user-session
// expiry (LastSyncStatus auth_expired / login_required) no longer downgrades a
// registered device, because session expiry is no longer allowed to clear the
// device_token (handleAuthExpired keeps registration intact). Only an explicit
// device-side refusal (device_token_invalid / device_unbound / device_id_conflict
// — which clear the token) yields `rejected`, and only when there is no token.
func (s *AgentService) deriveRegistrationStateLocked() (string, string) {
	syncStatus := strings.TrimSpace(s.state.LastSyncStatus)
	syncMessage := s.state.LastSyncMessage

	if strings.TrimSpace(currentAgentServerURL()) == "" {
		return registrationNotConfigured, "Agent server is not configured."
	}

	// A device_token is present ⇒ the server accepted this device and has not
	// rejected the credential. The user-session status (auth_expired /
	// login_required) is a connection concern, not a registration concern, so it
	// must NOT override "registered".
	if strings.TrimSpace(s.state.DeviceToken) != "" {
		return registrationRegistered, ""
	}

	// No device_token: explain why, using the last sync outcome.
	switch syncStatus {
	case "device_token_invalid", "device_unbound", "device_id_conflict":
		return registrationRejected, agentHealthMessage(syncMessage, "The agent server rejected this device's registration.")
	case "enable_failed":
		// enable_failed is set whenever a register/sync call errored; only treat
		// it as a refusal when the cause looks like an auth rejection, otherwise
		// it is a transient/network failure and the device is simply not yet
		// registered (connection_state will flag the reachability problem).
		if agentMessageIndicatesAuthError(syncMessage) {
			return registrationRejected, syncMessage
		}
	case "login_required", "auth_expired":
		// Genuine first-time state: never registered because no user JWT was
		// available to register with. (After the session/deregistration split, a
		// real device_token survives session blips, so this only appears before
		// the first successful registration.)
		return registrationLoginRequired, agentHealthMessage(syncMessage, "Log in before registering this device with the agent server.")
	}
	if agentMessageIndicatesAuthError(syncMessage) {
		return registrationRejected, syncMessage
	}
	return registrationUnregistered, agentHealthMessage(syncMessage, "This device is not registered with the agent server.")
}

// deriveConnectionStateLocked maps the raw agent state to the live-link health
// enum. Derived from the live RemoteConnected flag (set by the remote-WS loop)
// plus the LastSyncStatus the loop writes (online/connecting/connect_failed/
// disconnected). A user-session expiry (auth_expired / login_required) reads as
// `disconnected` — "waiting for sign-in, reconnects automatically" — NOT `error`:
// the link itself is fine, it is just idle until the user re-authenticates.
// Race-free under s.mu and survives restarts.
func (s *AgentService) deriveConnectionStateLocked() (string, string) {
	if s.state.RemoteConnected {
		return connectionConnected, ""
	}
	switch strings.TrimSpace(s.state.LastSyncStatus) {
	case "connecting":
		return connectionConnecting, ""
	case "auth_expired", "login_required":
		return connectionDisconnected, agentHealthMessage(s.state.LastSyncMessage, "Waiting for sign-in; the link reconnects automatically after login.")
	case "connect_failed", "disconnected", "server_unavailable":
		return connectionError, agentHealthMessage(s.state.LastSyncMessage, "Connection to the agent server failed; retrying.")
	}
	return connectionDisconnected, ""
}

// agentHealthMessage returns msg if non-empty, otherwise def.
func agentHealthMessage(msg, def string) string {
	if strings.TrimSpace(msg) != "" {
		return msg
	}
	return def
}

// agentMessageIndicatesAuthError reports whether a sync message looks like a
// server-side authentication refusal (covers the 401/authentication_required
// cases that may not always flow through a named disable reason).
func agentMessageIndicatesAuthError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "authentication_required") || strings.Contains(m, "returned 401") || strings.Contains(m, "unauthorized")
}

func (s *AgentService) statusLocked() models.AgentStatusResponse {
	s.syncRuntimeDeviceStatusLocked()
	status := agentStatusDisabled
	if s.isEnabledLocked() {
		status = agentStatusEnabled
	}

	regState, regMsg := s.deriveRegistrationStateLocked()
	connState, connMsg := s.deriveConnectionStateLocked()
	connectedAt := ""
	if connState == connectionConnected {
		connectedAt = s.state.LastSyncAt
	}

	return models.AgentStatusResponse{
		Status:          status,
		Enabled:         s.isEnabledLocked(),
		Bound:           s.isBoundLocked(),
		Registered:      s.isRegisteredLocked(),
		RemoteConnected: s.state.RemoteConnected,
		BindingRequired: false,
		Platform:        runtime.GOOS,
		ProtocolVersion: models.AgentProtocolVersion,
		AgentServer:     currentAgentServerURL(),
		Runtime:         currentAgentRuntimeStatus(),
		Device:          s.state.Device,
		Capabilities:    agentCapabilities(),
		Tools:           detectAgentTools(),
		History:         collectAgentHistoryRoots(),
		LastSyncAt:      s.state.LastSyncAt,
		SyncStatus:      s.state.LastSyncStatus,
		SyncMessage:     s.state.LastSyncMessage,

		RegistrationState:   regState,
		RegistrationMessage: regMsg,
		ConnectionState:     connState,
		ConnectionMessage:   connMsg,
		ConnectedAt:         connectedAt,
	}
}

func (s *AgentService) isRegisteredLocked() bool {
	return s.state.Registered || strings.TrimSpace(s.state.DeviceToken) != ""
}

func (s *AgentService) isBoundLocked() bool {
	return s.state.Device != nil || strings.TrimSpace(s.state.DeviceToken) != ""
}

func (s *AgentService) isEnabledLocked() bool {
	return s.state.Enabled && s.isBoundLocked()
}

func (s *AgentService) syncRuntimeDeviceStatusLocked() {
	if s.state.Device == nil && strings.TrimSpace(s.state.DeviceToken) != "" {
		s.state.Device = &models.AgentDevice{
			ID:                    s.state.DeviceID,
			DeviceID:              s.state.DeviceID,
			UniqueCode:            s.state.UniqueCode,
			Name:                  defaultAgentDeviceName(),
			Platform:              agentPlatform(),
			Status:                "offline",
			RemoteTerminalEnabled: true,
			AIControlEnabled:      true,
			BoundAt:               s.state.LastSyncAt,
		}
	}
	if s.state.Device == nil {
		return
	}
	if strings.TrimSpace(s.state.Device.ID) == "" {
		s.state.Device.ID = s.state.DeviceID
	}
	if strings.TrimSpace(s.state.Device.DeviceID) == "" {
		s.state.Device.DeviceID = s.state.Device.ID
	}
	if strings.TrimSpace(s.state.Device.UniqueCode) == "" {
		s.state.Device.UniqueCode = s.state.UniqueCode
	}
	if strings.TrimSpace(s.state.Device.Platform) == "" {
		s.state.Device.Platform = agentPlatform()
	}
	if s.state.RemoteConnected {
		s.state.Device.Status = "online"
		if strings.TrimSpace(s.state.Device.LastSeenAt) == "" {
			s.state.Device.LastSeenAt = time.Now().UTC().Format(time.RFC3339)
		}
	} else if strings.TrimSpace(s.state.Device.Status) == "" {
		s.state.Device.Status = "offline"
	}
}

func (s *AgentService) resolveDeviceIDLocked() string {
	s.ensureDeviceIdentityLocked()
	return s.state.DeviceID
}

func (s *AgentService) ensureDeviceIdentityLocked() {
	beforeID := s.state.DeviceID
	beforeUC := s.state.UniqueCode
	// The device identity is permanent and client-owned: resolve it from the
	// dedicated identity file (creating it once if needed) and pin it into
	// state, never generating or adopting a server-provided id here.
	s.pinPermanentDeviceIDLocked()
	changed := s.state.DeviceID != beforeID || s.state.UniqueCode != beforeUC
	if s.state.Device != nil {
		if strings.TrimSpace(s.state.Device.ID) == "" {
			s.state.Device.ID = s.state.DeviceID
			changed = true
		}
		if strings.TrimSpace(s.state.Device.DeviceID) == "" {
			s.state.Device.DeviceID = s.state.Device.ID
			changed = true
		}
		if strings.TrimSpace(s.state.Device.UniqueCode) == "" {
			s.state.Device.UniqueCode = s.state.UniqueCode
			changed = true
		}
	}
	if changed {
		if err := s.saveStateLocked(); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] persist_device_identity failed error=%v", err))
		}
	}
}

func (s *AgentService) loadState() error {
	path, err := agentStatePath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state agentState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	applyAgentDeviceFeatureDefaults(raw, &state)
	s.state = state
	return nil
}

func applyAgentDeviceFeatureDefaults(raw []byte, state *agentState) {
	if state == nil || state.Device == nil {
		return
	}
	var persisted struct {
		Device map[string]json.RawMessage `json:"device"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil || persisted.Device == nil {
		return
	}
	if _, ok := persisted.Device["remote_terminal_enabled"]; !ok {
		state.Device.RemoteTerminalEnabled = true
	}
	if _, ok := persisted.Device["ai_control_enabled"]; !ok {
		state.Device.AIControlEnabled = true
	}
}

func (s *AgentService) saveStateLocked() error {
	path, err := agentStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// agentDeviceIdentity is the installation-permanent device identity: the
// device_id and its pairing unique_code. It is generated ONCE, persisted to a
// dedicated file (device_identity.json), and never mutated afterwards — not by
// server responses, not by auth/user-session changes, not by registration
// conflicts. It is the single source of truth for who this device is.
type agentDeviceIdentity struct {
	DeviceID   string `json:"device_id"`
	UniqueCode string `json:"unique_code"`
	CreatedAt  string `json:"created_at,omitempty"`
}

func agentIdentityPath() (string, error) {
	statePath, err := agentStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), agentIdentityFilename), nil
}

func loadAgentDeviceIdentity() agentDeviceIdentity {
	path, err := agentIdentityPath()
	if err != nil {
		return agentDeviceIdentity{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return agentDeviceIdentity{}
	}
	var id agentDeviceIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return agentDeviceIdentity{}
	}
	return id
}

// saveAgentDeviceIdentity writes the identity atomically (temp file + rename)
// so a torn write can never corrupt the permanent identity.
func saveAgentDeviceIdentity(id agentDeviceIdentity) error {
	path, err := agentIdentityPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// permanentDeviceIdentityLocked resolves the installation's device identity
// from its dedicated file, generating it once if absent. On first encounter
// after an upgrade (file missing) it ADOPTS any device_id already present in
// session state so the server-registered id is preserved rather than
// regenerated. The result is cached for the process lifetime. Callers must
// hold s.mu.
func (s *AgentService) permanentDeviceIdentityLocked() agentDeviceIdentity {
	if s.identity != nil {
		return *s.identity
	}
	id := loadAgentDeviceIdentity()
	if strings.TrimSpace(id.DeviceID) == "" {
		if existing := strings.TrimSpace(s.state.DeviceID); existing != "" {
			// Migration: promote the existing session device_id to the permanent
			// identity instead of minting a new one.
			id.DeviceID = existing
			id.UniqueCode = firstNonEmpty(strings.TrimSpace(s.state.UniqueCode), newAgentUniqueDeviceCode())
		} else {
			id.DeviceID = "dev-" + uuid.NewString()
			id.UniqueCode = newAgentUniqueDeviceCode()
		}
		if strings.TrimSpace(id.CreatedAt) == "" {
			id.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if err := saveAgentDeviceIdentity(id); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] persist_device_identity failed error=%v", err))
		}
	}
	s.identity = &id
	return id
}

// pinPermanentDeviceIDLocked re-asserts the permanent device identity over the
// in-memory session state and (if present) the device record, discarding any
// device_id a remote exchange may have written. The device_id is owned by the
// client and must never be overridden by the server. Callers must hold s.mu.
func (s *AgentService) pinPermanentDeviceIDLocked() {
	id := s.permanentDeviceIdentityLocked()
	s.state.DeviceID = id.DeviceID
	s.state.UniqueCode = id.UniqueCode
	if s.state.Device != nil {
		s.state.Device.ID = id.DeviceID
		s.state.Device.DeviceID = id.DeviceID
		s.state.Device.UniqueCode = id.UniqueCode
	}
}

type agentLaunchSpec struct {
	ToolID string
	Path   string
	Args   []string
}

func resolveAgentLaunchSpec(req models.AgentLaunchRequest) (*agentLaunchSpec, error) {
	toolID := strings.ToLower(strings.TrimSpace(req.Tool))
	if toolID == "" && strings.TrimSpace(req.CommandLine) != "" {
		toolID = "command"
	}

	switch toolID {
	case "codex", "claude", "claudecode", "opencode":
		tool, ok := findAgentTool(toolID)
		if !ok || !tool.Available {
			return nil, fmt.Errorf("%s is not available in PATH", toolID)
		}
		return &agentLaunchSpec{
			ToolID: tool.ID,
			Path:   tool.Path,
			Args:   normalizeArgs(req.Args),
		}, nil
	case "command":
		parts, err := shlex.Split(strings.TrimSpace(req.CommandLine))
		if err != nil {
			return nil, fmt.Errorf("invalid command line: %w", err)
		}
		if len(parts) == 0 {
			return nil, errors.New("command line is required")
		}
		path, err := exec.LookPath(parts[0])
		if err != nil {
			return nil, fmt.Errorf("command not found in PATH: %s", parts[0])
		}
		return &agentLaunchSpec{
			ToolID: "command",
			Path:   path,
			Args:   parts[1:],
		}, nil
	default:
		return nil, fmt.Errorf("unsupported tool: %s", req.Tool)
	}
}

func detectAgentTools() []models.AgentTool {
	defs := []models.AgentTool{
		{ID: "codex", Name: "Codex", Command: "codex", Description: "OpenAI Codex CLI"},
		{ID: "claude", Name: "Claude Code", Command: "claude", Description: "Claude Code CLI"},
		{ID: "claudecode", Name: "Claude Code", Command: "claudecode", Description: "Claude Code CLI alias"},
		{ID: "opencode", Name: "OpenCode", Command: "opencode", Description: "OpenCode CLI"},
	}
	for i := range defs {
		if path, err := lookPathCLI(defs[i].Command); err == nil {
			defs[i].Path = path
			defs[i].Available = true
		}
	}
	for i := range defs {
		if defs[i].ID != "claudecode" || defs[i].Available {
			continue
		}
		if path, err := lookPathCLI("claude"); err == nil {
			defs[i].Command = "claude"
			defs[i].Path = path
			defs[i].Available = true
			defs[i].Description = "Claude Code CLI via claude"
		}
	}
	return defs
}

func findAgentTool(id string) (models.AgentTool, bool) {
	normalized := strings.ToLower(strings.TrimSpace(id))
	for _, tool := range detectAgentTools() {
		if tool.ID == normalized {
			return tool, true
		}
	}
	return models.AgentTool{}, false
}

func defaultAgentDeviceName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return runtime.GOOS + " device"
	}
	return host
}

type agentRegisterPayload struct {
	DeviceID   string `json:"device_id"`
	UniqueCode string `json:"unique_code"`
}

type agentRegisterResponse struct {
	DeviceToken string                    `json:"device_token"`
	AgentToken  string                    `json:"agent_token"`
	Token       string                    `json:"token"`
	AccessToken string                    `json:"access_token"`
	DeviceID    string                    `json:"device_id"`
	UniqueCode  string                    `json:"unique_code"`
	DeviceName  string                    `json:"device_name"`
	Name        string                    `json:"name"`
	Platform    string                    `json:"platform"`
	Status      string                    `json:"status"`
	Device      *models.AgentDevice       `json:"device"`
	UserID      string                    `json:"user_id"`
	User        *models.AgentUserIdentity `json:"user"`
	CreatedAt   string                    `json:"created_at"`
	PairedAt    string                    `json:"paired_at"`
	BoundAt     string                    `json:"bound_at"`
	LastSeenAt  string                    `json:"last_seen_at"`
}

type agentStatusSyncPayload struct {
	DeviceID              string                    `json:"device_id"`
	Status                string                    `json:"status"`
	UniqueCode            string                    `json:"unique_code,omitempty"`
	DeviceName            string                    `json:"device_name"`
	Platform              string                    `json:"platform"`
	AgentVersion          string                    `json:"agent_version,omitempty"`
	Host                  string                    `json:"host,omitempty"`
	Capabilities          []string                  `json:"capabilities"`
	Tools                 []models.AgentTool        `json:"tools"`
	History               []models.AgentHistoryRoot `json:"history"`
	Projects              []models.AgentProject     `json:"projects"`
	VibeSessions          []models.AgentVibeSession `json:"vibe_sessions"`
	AuthorizedDirectories []string                  `json:"authorized_directories,omitempty"`
	StartedAt             string                    `json:"started_at"`
	CollectedAt           string                    `json:"collected_at,omitempty"`
}

type agentStatusSyncResponse struct {
	Status           string              `json:"status"`
	Device           *models.AgentDevice `json:"device"`
	ProjectCount     int                 `json:"project_count"`
	VibeSessionCount int                 `json:"vibe_session_count"`
}

func (s *AgentService) registerAndSyncLocked() error {
	return s.registerAndSyncLockedWithAuthorization("")
}

func (s *AgentService) registerAndSyncLockedWithAuthorization(authHeader string) error {
	return s.registerAndSyncLockedWithUserContext(authHeader, "")
}

func (s *AgentService) registerAndSyncLockedWithUserContext(authHeader string, userKey string) error {
	s.ensureDeviceIdentityLocked()

	cfg := config.GetGlobalConfig()
	if cfg == nil || strings.TrimSpace(cfg.AgentBaseURL()) == "" {
		s.state.Registered = false
		s.state.RemoteConnected = false
		s.state.LastSyncStatus = "agent_server_not_configured"
		s.state.LastSyncMessage = "Agent server is not configured."
		return s.saveStateLocked()
	}

	authHeader = s.effectiveUserAuthorizationLocked(authHeader)
	// Registration requires a valid aliang JWT — it is the sole identity source
	// for binding a device to an owner. No JWT → login_required. NEVER fall back
	// to an admin-console / platform identity (that is what bound devices to
	// platform_admin when the agent started before the JWT was loaded). The
	// agent retries on the next sync once the JWT is available; see
	// ReRegisterIfUserIdentityChanged.
	if authHeader == "" {
		s.state.Enabled = false
		s.state.Registered = false
		s.state.RemoteConnected = false
		s.state.LastSyncStatus = "login_required"
		s.state.LastSyncMessage = "Log in before registering this device with the agent server."
		logger.Info(fmt.Sprintf("[AGENT-BOOT] register_sync skipped reason=login_required device_id=%s agent_server=%s register_url=%s",
			s.state.DeviceID,
			currentAgentServerURL(),
			agentRegisterURLForLog(),
		))
		return s.saveStateLocked()
	}
	currentUserKey := agentRegistrationUserKey(userKey, authHeader)
	s.resetRegisteredDeviceIfUserChangedLocked(currentUserKey)

	if strings.TrimSpace(s.state.DeviceToken) != "" {
		if s.state.RegisteredUser == "" {
			s.state.RegisteredUser = currentUserKey
		}
		return s.syncExistingRegisteredDeviceLocked()
	}

	endpoint := cfg.GetAgentDeviceRegisterURL()
	payload := buildAgentRegisterPayload(s.state.DeviceID, s.state.UniqueCode)
	logger.Info(fmt.Sprintf("[AGENT-BOOT] register_sync begin endpoint=%s device_id=%s unique_code=%s",
		sanitizeAgentEndpoint(endpoint),
		s.state.DeviceID,
		s.state.UniqueCode,
	))
	raw, err := s.callAgentServer(http.MethodPost, endpoint, payload, authHeader)
	if err != nil {
		if isDeviceIDAlreadyBoundError(err) {
			// The device_id is permanent and client-owned: never rotate it. The
			// server already holds a binding for this id; surface a conflict so
			// the operator can resolve it (unbind / re-login) instead of
			// fragmenting device history with a fresh id.
			s.pinPermanentDeviceIDLocked()
			s.state.Registered = false
			s.state.RemoteConnected = false
			s.state.LastSyncStatus = "device_id_conflict"
			s.state.LastSyncMessage = "Device id is already bound on the agent server."
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] register_sync device_id_conflict keeping_device_id device_id=%s", s.state.DeviceID))
			_ = s.saveStateLocked()
			return err
		}
	}
	if err != nil {
		s.state.Registered = false
		s.state.RemoteConnected = false
		return err
	}

	var resp agentRegisterResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return err
	}
	deviceToken := firstNonEmpty(resp.DeviceToken, resp.AgentToken, resp.Token)
	if deviceToken == "" {
		return errors.New("agent server register response missing device_token")
	}

	device := normalizeRegisteredAgentDevice(resp, s.state.DeviceID, s.state.UniqueCode)
	// device_id / unique_code are client-owned and permanent: ignore whatever
	// the server returned and re-pin to the installation identity.
	s.pinPermanentDeviceIDLocked()
	device.ID = s.state.DeviceID
	device.DeviceID = s.state.DeviceID
	device.UniqueCode = s.state.UniqueCode
	s.state.DeviceToken = deviceToken
	s.state.RegisteredUser = currentUserKey
	s.state.Device = device
	s.state.Enabled = true
	s.state.Registered = true
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = "connecting"
	s.state.LastSyncMessage = ""
	logger.Info(fmt.Sprintf("[AGENT-BOOT] register_sync registered device_id=%s agent_server=%s register_url=%s has_device_token=true",
		s.state.DeviceID,
		currentAgentServerURL(),
		agentRegisterURLForLog(),
	))
	if err := s.saveStateLocked(); err != nil {
		return err
	}
	if err := s.syncAgentInventoryLocked("register"); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] inventory_sync failed reason=register device_id=%s error=%v", s.state.DeviceID, err))
	}
	return nil
}

func (s *AgentService) syncExistingRegisteredDeviceLocked() error {
	s.state.Enabled = true
	s.state.Registered = true
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = "connecting"
	s.state.LastSyncMessage = ""
	s.syncRuntimeDeviceStatusLocked()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] register_sync skipped reason=existing_device_token device_id=%s agent_server=%s register_url=%s has_device_token=true",
		s.state.DeviceID,
		currentAgentServerURL(),
		agentRegisterURLForLog(),
	))
	if err := s.saveStateLocked(); err != nil {
		return err
	}
	if err := s.syncAgentInventoryLocked("existing_device_token"); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] inventory_sync failed reason=existing_device_token device_id=%s error=%v", s.state.DeviceID, err))
	}
	return nil
}

func (s *AgentService) resetRegisteredDeviceIfUserChangedLocked(currentUserKey string) {
	currentUserKey = strings.TrimSpace(currentUserKey)
	if currentUserKey == "" || strings.TrimSpace(s.state.DeviceToken) == "" || strings.TrimSpace(s.state.RegisteredUser) == "" {
		return
	}
	if s.state.RegisteredUser == currentUserKey {
		return
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] register_sync reset_device_token reason=user_changed device_id=%s", s.state.DeviceID))
	s.state.Device = nil
	s.state.DeviceToken = ""
	s.state.Registered = false
	s.state.RemoteConnected = false
}

func isDeviceIDAlreadyBoundError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "device_id_already_bound") {
		return true
	}
	return strings.Contains(text, "returned 409") &&
		strings.Contains(text, "device") &&
		strings.Contains(text, "already") &&
		strings.Contains(text, "bound")
}

func (s *AgentService) callAgentServer(method string, endpoint string, payload interface{}, authHeader string) ([]byte, error) {
	return s.callAgentServerWithAuthorization(method, endpoint, payload, effectiveAgentRegisterAuthHeader(authHeader), false)
}

func (s *AgentService) callAgentServerWithDeviceToken(method string, endpoint string, payload interface{}, deviceToken string) ([]byte, error) {
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return nil, errors.New("device token is empty")
	}
	authHeader := deviceToken
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		authHeader = "Bearer " + authHeader
	}
	return s.callAgentServerWithAuthorization(method, endpoint, payload, authHeader, true)
}

func (s *AgentService) callAgentServerWithAuthorization(method string, endpoint string, payload interface{}, authHeader string, hasDeviceToken bool) ([]byte, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, errors.New("agent server endpoint is empty")
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	authHeader = strings.TrimSpace(authHeader)
	useAdminConsoleFallback := shouldUseAdminConsoleAgentRegistration(authHeader, hasDeviceToken)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if useAdminConsoleFallback {
		req.Header.Set("X-Admin-Console", "1")
	}
	// Identity is carried solely by the standard `Authorization: <aliang JWT>`
	// header above. The PhoneServer decodes it (shared HS256 secret) → canonical
	// user_id, so the device binds to the real account and stays in sync with the
	// phone. The legacy X-Aliang-User-* headers were intentionally dropped: they
	// produced synthetic `agent_user_<sha1(key)>` accounts that drifted from the
	// logged-in user whenever the key changed, hiding devices from the phone.
	logger.Info(fmt.Sprintf("[AGENT-BOOT] agent_server_call begin method=%s endpoint=%s has_auth=%t has_device_token=%t admin_console_fallback=%t",
		method,
		sanitizeAgentEndpoint(endpoint),
		strings.TrimSpace(req.Header.Get("Authorization")) != "",
		hasDeviceToken,
		useAdminConsoleFallback,
	))
	resp, err := s.client.Do(req)
	if err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] agent_server_call failed method=%s endpoint=%s error=%v", method, sanitizeAgentEndpoint(endpoint), err))
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] agent_server_call non_2xx method=%s endpoint=%s status=%d", method, sanitizeAgentEndpoint(endpoint), resp.StatusCode))
		if resp.StatusCode == http.StatusUnauthorized {
			if hasDeviceToken {
				s.handleAgentDeviceTokenRejected(string(raw))
				return nil, agentDeviceTokenRejectedError{message: fmt.Sprintf("agent server returned %d: %s", resp.StatusCode, string(raw))}
			} else if authHeader != "" {
				// Register-path 401: the user JWT was rejected. This MAY be a
				// stale/expired token rather than a truly dead session, so do
				// NOT wipe the login here — return a typed error and let the
				// caller attempt a recovery refresh first
				// (recoverOrExpireAfterRegisterAuthRejection). Wiping inline
				// also ran under s.mu, which a recovery refresh cannot do: a
				// successful RefreshSession fires the auth-success hook whose
				// handler re-takes s.mu.
				return nil, agentUserAuthRejectedError{status: resp.StatusCode, body: string(raw)}
			}
		}
		return nil, fmt.Errorf("agent server returned %d: %s", resp.StatusCode, string(raw))
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] agent_server_call success method=%s endpoint=%s status=%d", method, sanitizeAgentEndpoint(endpoint), resp.StatusCode))
	return unwrapAgentServerData(raw)
}

func (s *AgentService) handleAgentDeviceTokenRejected(body string) {
	body = strings.TrimSpace(body)
	logger.Warn(fmt.Sprintf("[AGENT-BOOT] device_token rejected by agent server body=%s", body))
	// Persist the server's rejection detail so registration_state=rejected carries
	// an informative registration_message (the 401 body) instead of the generic
	// per-reason text. Still dispatched via a goroutine: this runs on the s.mu
	// critical path (callAgentServer ← registerAndSync), so disabling inline would
	// self-deadlock.
	detail := agentDisableMessage("device_token_invalid")
	if body != "" {
		snippet := body
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		detail = fmt.Sprintf("%s Server response: %s", detail, snippet)
	}
	go s.disableWithReasonMessage("device_token_invalid", detail)
}

// registerAuthRecoveryInFlight prevents a register-auth-rejection recovery from
// recursing into itself. A successful RefreshSession fires the auth-success hook
// (handleAuthRefreshed → ReRegisterIfUserIdentityChanged → SyncNow); if that
// re-register 401s again we must NOT start another recovery — that loops
// refresh→re-register→401→recover→refresh. The try-lock fails for the re-entrant
// call on the same goroutine stack, so the inner 401 becomes a plain failure
// resolved by the outer (in-flight) recovery.
var registerAuthRecoveryInFlight sync.Mutex

// recoverOrExpireAfterRegisterAuthRejection handles a register-path 401 by
// attempting a recovery refresh before wiping the session. No-op for any error
// that is not an agentUserAuthRejectedError. MUST run without s.mu held: a
// successful RefreshSession fires the auth-success hook whose handler
// re-acquires s.mu (ReRegisterIfUserIdentityChanged).
func (s *AgentService) recoverOrExpireAfterRegisterAuthRejection(err error) {
	var rejected agentUserAuthRejectedError
	if !errors.As(err, &rejected) {
		return
	}
	if !registerAuthRecoveryInFlight.TryLock() {
		logger.Info("[AGENT-BOOT] register_auth_rejection recovery skipped reason=reentrancy_guarded")
		return
	}
	defer registerAuthRecoveryInFlight.Unlock()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] register_auth_rejection recovery_begin status=%d", rejected.status))
	if IsUserAgentRuntime() {
		logger.Warn("[AGENT-BOOT] register_auth_rejection delegated_to_session_owner")
		return
	}
	auth.RecoverOrExpireLocalSession("agent server rejected user authorization")
}

func (s *AgentService) syncAgentInventoryLocked(reason string) error {
	cfg := config.GetGlobalConfig()
	if cfg == nil || strings.TrimSpace(cfg.AgentBaseURL()) == "" {
		return errors.New("agent server is not configured")
	}
	deviceToken := strings.TrimSpace(s.state.DeviceToken)
	if deviceToken == "" {
		return errors.New("device token is not available")
	}
	payload := s.buildAgentStatusSyncPayloadLocked("online")
	endpoint := currentAgentStatusSyncURL()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] inventory_sync begin reason=%s endpoint=%s device_id=%s projects=%d vibe_sessions=%d dirs=%d",
		strings.TrimSpace(reason),
		sanitizeAgentEndpoint(endpoint),
		payload.DeviceID,
		len(payload.Projects),
		len(payload.VibeSessions),
		len(payload.AuthorizedDirectories),
	))
	raw, err := s.callAgentServerWithDeviceToken(http.MethodPost, endpoint, payload, deviceToken)
	if err != nil {
		return err
	}
	var resp agentStatusSyncResponse
	if err := json.Unmarshal(raw, &resp); err == nil && resp.Device != nil {
		device := *resp.Device
		fillAgentDeviceDefaults(&device, time.Now().UTC().Format(time.RFC3339))
		s.state.Device = &device
		// device_id is permanent and client-owned: never adopt the server's id.
		s.pinPermanentDeviceIDLocked()
	}
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	logger.Info(fmt.Sprintf("[AGENT-BOOT] inventory_sync success reason=%s device_id=%s projects=%d vibe_sessions=%d server_projects=%d server_vibe_sessions=%d",
		strings.TrimSpace(reason),
		payload.DeviceID,
		len(payload.Projects),
		len(payload.VibeSessions),
		resp.ProjectCount,
		resp.VibeSessionCount,
	))
	return s.saveStateLocked()
}

func unwrapAgentServerData(raw []byte) ([]byte, error) {
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 {
		if envelope.Code != 0 {
			return nil, fmt.Errorf("agent server error: %s", envelope.Msg)
		}
		return envelope.Data, nil
	}
	return raw, nil
}

func CurrentAgentRegisterAuthorizationHeader() string {
	return effectiveAgentRegisterAuthHeader("")
}

func CurrentAgentRegisterUserKey() string {
	return agentRegistrationUserKey("", "")
}

func effectiveAgentRegisterAuthHeader(authHeader string) string {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(auth.GetCurrentAuthorizationHeader())
}

// resolveForwardedUserContextLocked installs an explicitly forwarded identity
// and returns the best process-local identity for subsequent background work.
// Caller holds s.mu.
func (s *AgentService) resolveForwardedUserContextLocked(authHeader string, userKey string) (string, string, bool) {
	authHeader = strings.TrimSpace(authHeader)
	userKey = strings.TrimSpace(userKey)
	if !IsUserAgentRuntime() {
		if authHeader == "" {
			authHeader = strings.TrimSpace(auth.GetCurrentAuthorizationHeader())
		}
		return authHeader, userKey, false
	}

	authChanged := false
	if authHeader != "" {
		authChanged = authHeader != s.forwardedUserAuthorization
		s.forwardedUserAuthorization = authHeader
	}
	if userKey != "" {
		s.forwardedUserKey = userKey
	}
	if authHeader == "" {
		authHeader = s.forwardedUserAuthorization
	}
	if userKey == "" {
		userKey = s.forwardedUserKey
	}
	return authHeader, userKey, authChanged
}

// effectiveUserAuthorizationLocked returns the cached forwarded credential in
// the user-agent process. Only a session-owner process may fall back to the
// persisted auth store. Caller holds s.mu.
func (s *AgentService) effectiveUserAuthorizationLocked(authHeader string) string {
	authHeader, _, _ = s.resolveForwardedUserContextLocked(authHeader, "")
	return authHeader
}

// currentAccessToken returns the logged-in user's raw access_token (the HS256
// JWT PhoneServer decodes for identity + exp), without the "Bearer " prefix.
// Empty when no session is loaded.
func (s *AgentService) currentAccessToken() string {
	s.mu.Lock()
	authHeader := s.effectiveUserAuthorizationLocked("")
	s.mu.Unlock()
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return ""
	}
	parts := strings.Fields(authHeader)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return authHeader
}

// PushSessionRefresh signals the remote-agent session loop to push the current
// access_token to PhoneServer (agent.session.refresh). Non-blocking and bounded:
// at most one update is queued until the loop drains it. Invoked after the
// session owner forwards a changed access token.
func (s *AgentService) PushSessionRefresh() {
	select {
	case s.sessionRefreshSig <- struct{}{}:
	default:
	}
}

func CanUseAdminConsoleAgentRegistration() bool {
	return shouldUseAdminConsoleAgentRegistration("", false)
}

func shouldUseAdminConsoleAgentRegistration(authHeader string, hasDeviceToken bool) bool {
	if hasDeviceToken || strings.TrimSpace(authHeader) != "" {
		return false
	}
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return false
	}
	return isLoopbackAgentServer(cfg.AgentBaseURL())
}

func isLoopbackAgentServer(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func agentRegistrationUserKey(userKey string, authHeader string) string {
	if key := currentAgentUserKey(userKey, authHeader); key != "" {
		return key
	}
	if shouldUseAdminConsoleAgentRegistration(authHeader, false) {
		return "admin-console:" + currentAgentServerURL()
	}
	return ""
}

func currentAgentUserKey(userKey string, authHeader string) string {
	if userKey = strings.TrimSpace(userKey); userKey != "" {
		return userKey
	}
	if current := auth.GetCurrentUserInfoOrLoad(); current != nil {
		switch {
		case current.ID != 0:
			return fmt.Sprintf("id:%d", current.ID)
		case strings.TrimSpace(current.Email) != "":
			return "email:" + strings.ToLower(strings.TrimSpace(current.Email))
		case strings.TrimSpace(current.Username) != "":
			return "username:" + strings.ToLower(strings.TrimSpace(current.Username))
		}
	}
	authHeader = strings.TrimSpace(authHeader)
	if authHeader != "" {
		sum := sha256.Sum256([]byte(authHeader))
		return fmt.Sprintf("auth:%x", sum[:8])
	}
	return ""
}

func requestLocalUserAgentSyncAfterAuth(reason string, authHeader string, userKey string) error {
	values := url.Values{}
	if reason = strings.TrimSpace(reason); reason != "" {
		values.Set("reason", reason)
	}
	endpoint := strings.TrimRight(localUserAgentBaseURL(), "/") + "/api/agent/sync"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return requestLocalUserAgentPostWithTimeout(endpoint, authHeader, userKey, agentAuthSyncTimeout)
}

func requestLocalUserAgentDisableAfterLogout(reason string) error {
	values := url.Values{}
	if reason = strings.TrimSpace(reason); reason != "" {
		values.Set("reason", reason)
	}
	endpoint := strings.TrimRight(localUserAgentBaseURL(), "/") + "/api/agent/disable"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return requestLocalUserAgentPostWithTimeout(endpoint, "", "", agentLogoutTimeout)
}

func requestLocalUserAgentRecoverAfterAuthExpired() error {
	// /api/agent/auth-recover is self-gating (RecoverIfAuthExpired is a no-op
	// unless the agent is currently disabled with reason auth_expired), so this
	// is a safe idempotent nudge from the main process after a refresh.
	endpoint := strings.TrimRight(localUserAgentBaseURL(), "/") + "/api/agent/auth-recover"
	return requestLocalUserAgentPost(endpoint, "", "")
}

func requestLocalUserAgentEnsureConnection() error {
	// /api/agent/reconnect is idempotent (EnsureRemoteConnection is a no-op when
	// already connected/connecting, or when there is no device_token), so this is
	// a safe nudge from the main process after every successful token refresh.
	endpoint := strings.TrimRight(localUserAgentBaseURL(), "/") + "/api/agent/reconnect"
	return requestLocalUserAgentPost(endpoint, "", "")
}

func requestLocalUserAgentPost(endpoint string, authHeader string, userKey string) error {
	return requestLocalUserAgentPostWithTimeout(endpoint, authHeader, userKey, agentHTTPTimeout)
}

type localUserAgentResponseError struct {
	StatusCode int
	Body       string
}

func (e *localUserAgentResponseError) Error() string {
	return fmt.Sprintf("local user agent returned %d: %s", e.StatusCode, e.Body)
}

func requestLocalUserAgentPostWithTimeout(endpoint string, authHeader string, userKey string, timeout time.Duration) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Aliang-Agent-Proxy", "auth")
	if authHeader = effectiveAgentRegisterAuthHeader(authHeader); authHeader != "" {
		req.Header.Set(AgentForwardedAuthorizationHeader, authHeader)
	}
	if userKey = currentAgentUserKey(userKey, authHeader); userKey != "" {
		req.Header.Set(AgentForwardedUserKeyHeader, userKey)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &localUserAgentResponseError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	return nil
}

func (s *AgentService) applyRemoteDeviceSettings(msg map[string]interface{}) {
	rawDevice, ok := msg["device"].(map[string]interface{})
	if !ok {
		return
	}
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	if s.state.Device == nil {
		s.syncRuntimeDeviceStatusLocked()
	}
	if s.state.Device == nil {
		s.mu.Unlock()
		return
	}
	terminalDisabled := false
	aiDisabled := false
	if name := remoteString(rawDevice, "name"); name != "" {
		s.state.Device.Name = name
	}
	// device_id is permanent and client-owned: never adopt the server's id.
	// (ensureDeviceIdentityLocked at the top of this handler already pinned it.)
	if platform := remoteString(rawDevice, "platform"); platform != "" {
		s.state.Device.Platform = platform
	}
	if status := remoteString(rawDevice, "status"); status != "" {
		s.state.Device.Status = status
	}
	if agentVersion := remoteString(rawDevice, "agent_version"); agentVersion != "" {
		s.state.Device.AgentVersion = agentVersion
	}
	if lastSeenAt := remoteString(rawDevice, "last_seen_at"); lastSeenAt != "" {
		s.state.Device.LastSeenAt = lastSeenAt
	}
	if pairedAt := remoteString(rawDevice, "paired_at"); pairedAt != "" {
		s.state.Device.PairedAt = pairedAt
	}
	if boundAt := remoteString(rawDevice, "bound_at"); boundAt != "" {
		s.state.Device.BoundAt = boundAt
	}
	if capabilities := remoteStringSlice(rawDevice, "capabilities"); capabilities != nil {
		s.state.Device.Capabilities = capabilities
	}
	if value, ok := rawDevice["remote_terminal_enabled"].(bool); ok {
		s.state.Device.RemoteTerminalEnabled = value
		terminalDisabled = !value
	}
	if value, ok := rawDevice["ai_control_enabled"].(bool); ok {
		s.state.Device.AIControlEnabled = value
		aiDisabled = !value
	}
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = "settings_updated"
	s.state.LastSyncMessage = ""
	_ = s.saveStateLocked()
	s.mu.Unlock()

	if terminalDisabled {
		s.terminal.closeAll()
	}
	if aiDisabled {
		s.ai.closeAll()
	}
}

// applyRemoteProjectSettings handles a project.settings.updated push: the server
// signals a project's approval_policy.hash changed. When it differs from the
// locally-effective policy for that path, reset that path's sync throttle so the
// next ensurePolicyBeforeRun pulls immediately (this push is the primary update
// path; the pre-run hash check is just a backstop). No-op when path/hash absent.
func (s *AgentService) applyRemoteProjectSettings(msg map[string]interface{}) {
	path := normalizePolicyPath(remoteString(msg, "path"))
	remoteHash := remoteApprovalPolicyHash(msg, nil)
	if path == "" || remoteHash == "" {
		return
	}
	if current := s.effectiveApprovalPolicyForPath(path); current.Hash == remoteHash {
		return
	}
	s.mu.Lock()
	s.resetPolicySyncThrottleLocked(path)
	s.mu.Unlock()
	logger.Info(fmt.Sprintf("approval-policy: project.settings.updated push path=%q hash=%q -> refetch", path, remoteHash))
	go s.ensurePolicyBeforeRun(context.Background(), path)
}

func remoteStringSlice(msg map[string]interface{}, key string) []string {
	raw, ok := msg[key]
	if !ok || raw == nil {
		return nil
	}
	values, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func buildAgentRegisterPayload(deviceID string, uniqueCode string) agentRegisterPayload {
	return agentRegisterPayload{
		DeviceID:   deviceID,
		UniqueCode: uniqueCode,
	}
}

func (s *AgentService) buildAgentStatusSyncPayloadLocked(status string) agentStatusSyncPayload {
	s.ensureDeviceIdentityLocked()
	status = strings.TrimSpace(status)
	if status == "" {
		status = "online"
	}
	scanDirs := activeScanDirs(s.state.ScanDirectories, s.state.ScanDirectoriesEnabled)
	snapshot := s.collectAgentSyncSnapshotWithActiveRuns(scanDirs)
	return agentStatusSyncPayload{
		DeviceID:              s.state.DeviceID,
		Status:                status,
		UniqueCode:            s.state.UniqueCode,
		DeviceName:            snapshot.DeviceName,
		Platform:              snapshot.Platform,
		AgentVersion:          snapshot.AgentVersion,
		Host:                  snapshot.Host,
		Capabilities:          snapshot.Capabilities,
		Tools:                 snapshot.Tools,
		History:               snapshot.History,
		Projects:              snapshot.Projects,
		VibeSessions:          snapshot.VibeSessions,
		AuthorizedDirectories: snapshot.AuthorizedDirectories,
		StartedAt:             time.Now().UTC().Format(time.RFC3339),
		CollectedAt:           snapshot.CollectedAt,
	}
}

func (s *AgentService) collectAgentSyncSnapshotWithActiveRuns(scanDirs []string) agentSyncSnapshot {
	snapshot := collectAgentSyncSnapshot(scanDirs)
	if s == nil || s.ai == nil {
		return snapshot
	}
	return overlayActiveAgentVibeSessions(snapshot, s.ai.activeVibeSessionsSnapshot(), scanDirs)
}

func normalizeRegisteredAgentDevice(resp agentRegisterResponse, fallbackDeviceID string, fallbackUniqueCode string) *models.AgentDevice {
	now := time.Now().UTC().Format(time.RFC3339)
	if resp.Device != nil {
		device := *resp.Device
		if strings.TrimSpace(device.ID) == "" {
			device.ID = firstNonEmpty(resp.DeviceID, fallbackDeviceID)
		}
		if strings.TrimSpace(device.DeviceID) == "" {
			device.DeviceID = firstNonEmpty(resp.DeviceID, device.ID, fallbackDeviceID)
		}
		device.UniqueCode = firstNonEmpty(fallbackUniqueCode, device.UniqueCode, resp.UniqueCode)
		if strings.TrimSpace(device.UserID) == "" {
			device.UserID = firstNonEmpty(resp.UserID, userIDFromIdentity(resp.User))
		}
		if device.User == nil {
			device.User = resp.User
		}
		fillAgentDeviceDefaults(&device, now)
		return &device
	}

	deviceID := firstNonEmpty(resp.DeviceID, fallbackDeviceID)
	uniqueCode := firstNonEmpty(fallbackUniqueCode, resp.UniqueCode)
	device := &models.AgentDevice{
		ID:                    deviceID,
		DeviceID:              deviceID,
		UserID:                firstNonEmpty(resp.UserID, userIDFromIdentity(resp.User)),
		User:                  resp.User,
		UniqueCode:            uniqueCode,
		Name:                  firstNonEmpty(resp.DeviceName, resp.Name, defaultAgentDeviceName()),
		Platform:              firstNonEmpty(resp.Platform, agentPlatform()),
		AgentVersion:          agentVersion(),
		Status:                firstNonEmpty(resp.Status, "offline"),
		Capabilities:          agentCapabilities(),
		RemoteTerminalEnabled: true,
		AIControlEnabled:      true,
		CreatedAt:             resp.CreatedAt,
		PairedAt:              resp.PairedAt,
		BoundAt:               firstNonEmpty(resp.BoundAt, now),
		LastSeenAt:            resp.LastSeenAt,
	}
	fillAgentDeviceDefaults(device, now)
	return device
}

func fillAgentDeviceDefaults(device *models.AgentDevice, now string) {
	if device == nil {
		return
	}
	if strings.TrimSpace(device.ID) == "" {
		device.ID = device.DeviceID
	}
	if strings.TrimSpace(device.DeviceID) == "" {
		device.DeviceID = device.ID
	}
	if strings.TrimSpace(device.UserID) == "" && device.User != nil {
		device.UserID = strings.TrimSpace(device.User.ID)
	}
	if strings.TrimSpace(device.UniqueCode) == "" {
		device.UniqueCode = newAgentUniqueDeviceCode()
	}
	if strings.TrimSpace(device.Name) == "" {
		device.Name = defaultAgentDeviceName()
	}
	if strings.TrimSpace(device.Platform) == "" {
		device.Platform = agentPlatform()
	}
	if strings.TrimSpace(device.AgentVersion) == "" {
		device.AgentVersion = agentVersion()
	}
	if strings.TrimSpace(device.Status) == "" {
		device.Status = "offline"
	}
	if device.Capabilities == nil {
		device.Capabilities = agentCapabilities()
	}
	if !device.RemoteTerminalEnabled && !device.AIControlEnabled {
		device.RemoteTerminalEnabled = true
		device.AIControlEnabled = true
	}
	if strings.TrimSpace(device.BoundAt) == "" {
		device.BoundAt = now
	}
}

func userIDFromIdentity(user *models.AgentUserIdentity) string {
	if user == nil {
		return ""
	}
	return strings.TrimSpace(user.ID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func agentVersion() string {
	if value := strings.TrimSpace(version.String()); value != "" {
		return value
	}
	return version.BuildString()
}

func collectAgentHistoryRoots() []models.AgentHistoryRoot {
	roots := []struct {
		tool string
		path string
	}{
		{tool: "codex", path: "~/.codex"},
		{tool: "codex", path: "~/.codex/sessions"},
		{tool: "claude", path: "~/.claude"},
		{tool: "claude", path: "~/.claude/projects"},
		{tool: "opencode", path: "~/.config/opencode"},
		{tool: "opencode", path: "~/.local/share/opencode"},
		{tool: "opencode", path: "~/.opencode"},
	}
	result := make([]models.AgentHistoryRoot, 0, len(roots))
	for _, root := range roots {
		result = append(result, summarizeAgentHistoryRoot(root.tool, root.path))
	}
	return result
}

func summarizeAgentHistoryRoot(tool string, rawPath string) models.AgentHistoryRoot {
	expanded := rawPath
	if home := agentHome(); home != "" && strings.HasPrefix(rawPath, "~") {
		expanded = filepath.Join(home, rawPath[1:])
	}
	summary := models.AgentHistoryRoot{Tool: tool, Path: expanded}
	stat, err := os.Stat(expanded)
	if err != nil {
		return summary
	}
	summary.Exists = true
	if !stat.IsDir() {
		summary.FileCount = 1
		summary.TotalSize = stat.Size()
		summary.UpdatedAt = stat.ModTime().UTC().Format(time.RFC3339)
		return summary
	}
	latest := stat.ModTime()
	_ = filepath.WalkDir(expanded, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if summary.FileCount >= 2000 {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		summary.FileCount++
		summary.TotalSize += info.Size()
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	summary.UpdatedAt = latest.UTC().Format(time.RFC3339)
	return summary
}

func newAgentUniqueDeviceCode() string {
	return "adc-" + uuid.NewString()
}

func currentAgentServerURL() string {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return ""
	}
	return cfg.AgentBaseURL()
}

func agentRegisterURLForLog() string {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return ""
	}
	return sanitizeAgentEndpoint(cfg.GetAgentDeviceRegisterURL())
}

func currentAgentStatusSyncURL() string {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(cfg.AgentBaseURL(), "/") + agentStatusSyncPath
}

func sanitizeAgentEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return strings.TrimSpace(endpoint)
	}
	values := parsed.Query()
	for _, key := range []string{"token", "agent_secret", "device_token"} {
		if values.Has(key) {
			values.Set(key, "<redacted>")
		}
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func currentAgentRuntimeStatus() *models.AgentRuntime {
	kind := "embedded"
	if IsUserAgentRuntime() {
		kind = "user_agent"
	}
	return &models.AgentRuntime{
		Online: true,
		Kind:   kind,
		URL:    UserAgentBaseURL(),
		PID:    os.Getpid(),
	}
}

func agentStatePath() (string, error) {
	if IsUserAgentRuntime() {
		stateDir, err := runtimepath.UserStateDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(stateDir, "agent", agentStateFilename), nil
	}
	dir, err := cache.GetCacheSubdir("agent")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentStateFilename), nil
}

func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, strings.TrimSpace(arg))
	}
	return out
}

func commandDisplay(path string, args []string) string {
	parts := append([]string{path}, args...)
	return strings.Join(parts, " ")
}
