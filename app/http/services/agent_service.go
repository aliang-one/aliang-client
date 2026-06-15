package services

import (
	"bytes"
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
)

const (
	agentStatusDisabled = "disabled"
	agentStatusEnabled  = "enabled"

	agentStateFilename     = "agent_state.json"
	agentDefaultLaunchMode = "external_terminal"
	agentHTTPTimeout       = 8 * time.Second
	agentStatusSyncPath    = "/api/agent/status"
	AgentRuntimeEnv        = "ALIANG_USER_AGENT_RUNTIME"

	AgentForwardedAuthorizationHeader = "X-Aliang-User-Authorization"
	AgentForwardedUserKeyHeader       = "X-Aliang-User-Key"
	AgentUserKeyHeader                = "X-Aliang-User-Key"
	AgentUserEmailHeader              = "X-Aliang-User-Email"
	AgentUsernameHeader               = "X-Aliang-Username"
	AgentUserRoleHeader               = "X-Aliang-User-Role"
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
}

type AgentService struct {
	mu       sync.Mutex
	state    agentState
	client   *http.Client
	terminal *agentTerminalManager
	ai       *agentAIManager

	wsMu         sync.Mutex
	wsConnected  bool
	wsConnecting bool
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
		client:   &http.Client{Timeout: agentHTTPTimeout},
		terminal: newAgentTerminalManager(),
		ai:       newAgentAIManager(),
	}
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
	s.syncRuntimeDeviceStatusLocked()

	status := agentStatusDisabled
	if s.isEnabledLocked() {
		status = agentStatusEnabled
	}

	message := "Agent mode is disabled."
	if status == agentStatusEnabled {
		message = "Agent mode is enabled for this user device."
	} else if strings.TrimSpace(auth.GetCurrentAuthorizationHeader()) == "" {
		message = "Log in before enabling Agent mode for this device."
	} else {
		message = "Agent mode can be enabled directly for this logged-in user."
	}

	return models.AgentStatusResponse{
		Status:          status,
		Enabled:         s.isEnabledLocked(),
		Bound:           s.isBoundLocked(),
		Registered:      s.isRegisteredLocked(),
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
		Message:         message,
	}
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
		s.state.LastSyncStatus = "enable_failed"
		s.state.LastSyncMessage = err.Error()
		_ = s.saveStateLocked()
		status := s.statusLocked()
		s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()
	s.state.Enabled = false
	s.state.Device = nil
	s.state.DeviceToken = ""
	s.state.RegisteredUser = ""
	s.state.Registered = false
	s.state.RemoteConnected = false
	_ = s.saveStateLocked()
	return s.statusLocked()
}

func (s *AgentService) SyncNow() error {
	return s.SyncNowWithAuthorization("")
}

func (s *AgentService) SyncNowWithAuthorization(authHeader string) error {
	return s.SyncNowWithUserContext(authHeader, "")
}

func (s *AgentService) SyncNowWithUserContext(authHeader string, userKey string) error {
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] sync_now begin device_id=%s enabled=%t registered=%t has_token=%t agent_server=%s runtime=user_agent:%t",
		s.state.DeviceID,
		s.state.Enabled,
		s.state.Registered,
		strings.TrimSpace(s.state.DeviceToken) != "",
		currentAgentServerURL(),
		IsUserAgentRuntime(),
	))
	if err := s.registerAndSyncLockedWithUserContext(authHeader, userKey); err != nil {
		s.state.LastSyncStatus = "server_unavailable"
		s.state.LastSyncMessage = err.Error()
		_ = s.saveStateLocked()
		s.mu.Unlock()
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] sync_now failed error=%v", err))
		return err
	}
	hasToken := strings.TrimSpace(s.state.DeviceToken) != ""
	enabled := s.state.Enabled
	registered := s.state.Registered
	deviceID := s.state.DeviceID
	s.mu.Unlock()
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
			_ = GetSharedAgentService().Disable()
		} else if err := requestLocalUserAgentDisableAfterLogout(reason); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] auth_disable local_user_agent_failed reason=%s error=%v", reason, err))
		}
		logger.Info(fmt.Sprintf("[AGENT-BOOT] auth_disable applied reason=%s runtime=user_agent:%t", reason, IsUserAgentRuntime()))
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

func (s *AgentService) statusLocked() models.AgentStatusResponse {
	s.syncRuntimeDeviceStatusLocked()
	status := agentStatusDisabled
	if s.isEnabledLocked() {
		status = agentStatusEnabled
	}
	return models.AgentStatusResponse{
		Status:          status,
		Enabled:         s.isEnabledLocked(),
		Bound:           s.isBoundLocked(),
		Registered:      s.isRegisteredLocked(),
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
	changed := false
	if strings.TrimSpace(s.state.DeviceID) == "" {
		s.state.DeviceID = "dev-" + uuid.NewString()
		changed = true
	}
	if strings.TrimSpace(s.state.UniqueCode) == "" {
		s.state.UniqueCode = newAgentUniqueDeviceCode()
		changed = true
	}
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
		if path, err := exec.LookPath(defs[i].Command); err == nil {
			defs[i].Path = path
			defs[i].Available = true
		}
	}
	for i := range defs {
		if defs[i].ID != "claudecode" || defs[i].Available {
			continue
		}
		if path, err := exec.LookPath("claude"); err == nil {
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

	authHeader = effectiveAgentRegisterAuthHeader(authHeader)
	if authHeader == "" && !shouldUseAdminConsoleAgentRegistration(authHeader, false) {
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
			s.rotateDeviceIdentityLocked()
			payload = buildAgentRegisterPayload(s.state.DeviceID, s.state.UniqueCode)
			logger.Info(fmt.Sprintf("[AGENT-BOOT] register_sync retry endpoint=%s device_id=%s unique_code=%s reason=device_id_already_bound",
				sanitizeAgentEndpoint(endpoint),
				s.state.DeviceID,
				s.state.UniqueCode,
			))
			raw, err = s.callAgentServer(http.MethodPost, endpoint, payload, authHeader)
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
	s.state.DeviceID = device.DeviceID
	s.state.UniqueCode = device.UniqueCode
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

func (s *AgentService) rotateDeviceIdentityLocked() {
	oldDeviceID := s.state.DeviceID
	s.state.DeviceID = "dev-" + uuid.NewString()
	s.state.Device = nil
	s.state.DeviceToken = ""
	s.state.RegisteredUser = ""
	s.state.Registered = false
	s.state.RemoteConnected = false
	logger.Warn(fmt.Sprintf("[AGENT-BOOT] register_sync device_id_conflict rotating_device_id old_device_id=%s new_device_id=%s", oldDeviceID, s.state.DeviceID))
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
	if !hasDeviceToken {
		for key, value := range CurrentAgentRegisterIdentityHeaders(agentRegistrationUserKey("", authHeader)) {
			req.Header.Set(key, value)
		}
	}
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
		return nil, fmt.Errorf("agent server returned %d: %s", resp.StatusCode, string(raw))
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] agent_server_call success method=%s endpoint=%s status=%d", method, sanitizeAgentEndpoint(endpoint), resp.StatusCode))
	return unwrapAgentServerData(raw)
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
		s.state.DeviceID = firstNonEmpty(device.DeviceID, device.ID, s.state.DeviceID)
		s.state.UniqueCode = firstNonEmpty(s.state.UniqueCode, device.UniqueCode)
		device.UniqueCode = s.state.UniqueCode
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

func CurrentAgentRegisterIdentityHeaders(userKey string) map[string]string {
	headers := make(map[string]string)
	userKey = agentRegistrationUserKey(userKey, "")
	if userKey != "" {
		headers[AgentUserKeyHeader] = userKey
	}
	if current := auth.GetCurrentUserInfoOrLoad(); current != nil {
		if email := strings.TrimSpace(current.Email); email != "" {
			headers[AgentUserEmailHeader] = email
		}
		if username := strings.TrimSpace(current.Username); username != "" {
			headers[AgentUsernameHeader] = username
		}
		if role := strings.TrimSpace(current.Role); role != "" {
			headers[AgentUserRoleHeader] = role
		}
	}
	return headers
}

func effectiveAgentRegisterAuthHeader(authHeader string) string {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(auth.GetCurrentAuthorizationHeader())
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
	return requestLocalUserAgentPost(endpoint, authHeader, userKey)
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
	return requestLocalUserAgentPost(endpoint, "", "")
}

func requestLocalUserAgentPost(endpoint string, authHeader string, userKey string) error {
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

	client := &http.Client{Timeout: agentHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("local user agent returned %d: %s", resp.StatusCode, string(raw))
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
	deviceID := remoteString(rawDevice, "device_id")
	if deviceID == "" {
		deviceID = remoteString(rawDevice, "id")
	}
	if deviceID != "" {
		s.state.Device.ID = deviceID
		s.state.Device.DeviceID = deviceID
		s.state.DeviceID = deviceID
	}
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
	snapshot := collectAgentSyncSnapshot()
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
	}
	result := make([]models.AgentHistoryRoot, 0, len(roots))
	for _, root := range roots {
		result = append(result, summarizeAgentHistoryRoot(root.tool, root.path))
	}
	return result
}

func summarizeAgentHistoryRoot(tool string, rawPath string) models.AgentHistoryRoot {
	expanded, err := cache.ExpandHomePath(rawPath)
	if err != nil {
		expanded = rawPath
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
