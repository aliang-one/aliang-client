package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/internal/runtimepath"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"github.com/google/shlex"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	agentStatusDisabled = "disabled"
	agentStatusPending  = "pending_bind"
	agentStatusEnabled  = "enabled"

	bindStatusPending = "pending"
	bindStatusBound   = "bound"
	bindStatusExpired = "expired"

	agentBindTTL           = 5 * time.Minute
	agentLocalMVPBindDelay = 6 * time.Second
	agentStateFilename     = "agent_state.json"
	agentDefaultLaunchMode = "external_terminal"
	agentHTTPTimeout       = 8 * time.Second
	AgentRuntimeEnv        = "ALIANG_USER_AGENT_RUNTIME"
)

var (
	sharedAgentServiceMu sync.Mutex
	sharedAgentService   *AgentService
)

type agentState struct {
	Enabled         bool                `json:"enabled"`
	Device          *models.AgentDevice `json:"device,omitempty"`
	DeviceID        string              `json:"device_id,omitempty"`
	UniqueCode      string              `json:"unique_code,omitempty"`
	DeviceToken     string              `json:"device_token,omitempty"`
	Registered      bool                `json:"registered"`
	RemoteConnected bool                `json:"remote_connected"`
	LastSyncAt      string              `json:"last_sync_at,omitempty"`
	LastSyncStatus  string              `json:"last_sync_status,omitempty"`
	LastSyncMessage string              `json:"last_sync_message,omitempty"`
}

type agentBindSessionState struct {
	ID          string
	Payload     string
	QRDataURL   string
	AgentSecret string
	PairingCode string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Status      string
	Remote      bool
}

type AgentService struct {
	mu       sync.Mutex
	state    agentState
	sessions map[string]*agentBindSessionState
	client   *http.Client

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
		sessions: make(map[string]*agentBindSessionState),
		client:   &http.Client{Timeout: agentHTTPTimeout},
	}
	_ = s.loadState()
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
		BindingRequired: true,
		Platform:        runtime.GOOS,
		AgentServer:     currentAgentServerURL(),
		Runtime: &models.AgentRuntime{
			Online: false,
			Kind:   "user_agent",
			URL:    UserAgentBaseURL(),
		},
		Tools:   []models.AgentTool{},
		History: []models.AgentHistoryRoot{},
		Message: message,
	}
}

func (s *AgentService) Status() models.AgentStatusResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()
	s.syncRuntimeDeviceStatusLocked()

	pending := s.latestPendingSessionLocked()
	status := agentStatusDisabled
	if s.isEnabledLocked() {
		status = agentStatusEnabled
	} else if pending != nil {
		status = agentStatusPending
	}

	message := "Agent mode is disabled."
	if status == agentStatusPending {
		message = "Waiting for QR scan confirmation."
	}
	if status == agentStatusEnabled {
		message = "Agent mode is enabled for this user device."
	}

	return models.AgentStatusResponse{
		Status:          status,
		Enabled:         s.isEnabledLocked(),
		Bound:           s.isBoundLocked(),
		Registered:      s.isRegisteredLocked(),
		BindingRequired: true,
		Platform:        runtime.GOOS,
		AgentServer:     currentAgentServerURL(),
		Runtime:         currentAgentRuntimeStatus(),
		Device:          s.state.Device,
		PendingBind:     pending,
		Tools:           detectAgentTools(),
		History:         collectAgentHistoryRoots(),
		LastSyncAt:      s.state.LastSyncAt,
		SyncStatus:      s.state.LastSyncStatus,
		SyncMessage:     s.state.LastSyncMessage,
		Message:         message,
	}
}

func (s *AgentService) StartBinding() (*models.AgentBindStartResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()

	agentServerConfigured := strings.TrimSpace(currentAgentServerURL()) != ""
	sessionID := uuid.NewString()
	expiresAt := time.Now().Add(agentBindTTL).UTC()
	deviceID := s.state.DeviceID
	payload := buildAgentBindPayload(sessionID, deviceID)
	agentSecret := ""
	pairingCode := ""
	remote := false
	if remoteSession, err := s.startRemoteBindingLocked(); err == nil && remoteSession != nil {
		sessionID = remoteSession.ID
		expiresAt = remoteSession.ExpiresAt
		payload = remoteSession.Payload
		agentSecret = remoteSession.AgentSecret
		pairingCode = remoteSession.PairingCode
		remote = true
	} else if err != nil {
		s.state.LastSyncStatus = "bind_server_unavailable"
		s.state.LastSyncMessage = err.Error()
		_ = s.saveStateLocked()
		if agentServerConfigured {
			return nil, err
		}
	}
	qrDataURL, err := generateQRCodeDataURL(payload)
	if err != nil {
		return nil, err
	}

	session := &agentBindSessionState{
		ID:          sessionID,
		Payload:     payload,
		QRDataURL:   qrDataURL,
		AgentSecret: agentSecret,
		PairingCode: pairingCode,
		CreatedAt:   time.Now(),
		ExpiresAt:   expiresAt,
		Status:      bindStatusPending,
		Remote:      remote,
	}
	s.sessions[sessionID] = session
	s.state.LastSyncStatus = "pairing_created"
	s.state.LastSyncMessage = ""
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	_ = s.saveStateLocked()

	return &models.AgentBindStartResponse{
		SessionID:   sessionID,
		PairingCode: pairingCode,
		QRPayload:   payload,
		QRDataURL:   qrDataURL,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		Status:      bindStatusPending,
		Message:     "Scan the QR code to bind this device.",
	}, nil
}

func (s *AgentService) BindingStatus(sessionID string) (*models.AgentBindStatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID = strings.TrimSpace(sessionID)
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("binding session not found")
	}

	now := time.Now()
	if now.After(session.ExpiresAt) {
		session.Status = bindStatusExpired
		return &models.AgentBindStatusResponse{
			SessionID: session.ID,
			Status:    bindStatusExpired,
			Bound:     false,
			Message:   "Binding QR code expired.",
			ExpiresAt: session.ExpiresAt.UTC().Format(time.RFC3339),
		}, nil
	}

	if session.Remote {
		if remote, err := s.pollRemoteBindingLocked(session); err == nil && remote != nil {
			return remote, nil
		} else if err != nil {
			s.state.LastSyncStatus = "bind_poll_failed"
			s.state.LastSyncMessage = err.Error()
			_ = s.saveStateLocked()
			return nil, err
		}
	}

	// Local MVP fallback: this simulates the cloud callback path so UI and
	// launcher flows can be developed before the production binding API exists.
	if session.Status == bindStatusPending && now.Sub(session.CreatedAt) >= agentLocalMVPBindDelay {
		device := &models.AgentDevice{
			ID:         s.state.DeviceID,
			DeviceID:   s.state.DeviceID,
			UniqueCode: s.state.UniqueCode,
			Name:       defaultAgentDeviceName(),
			Platform:   agentPlatform(),
			Status:     "online",
			BoundAt:    now.UTC().Format(time.RFC3339),
		}
		s.state.Enabled = true
		s.state.Device = device
		s.state.DeviceToken = "local-mvp-" + uuid.NewString()
		session.Status = bindStatusBound
		_ = s.saveStateLocked()
	}

	return &models.AgentBindStatusResponse{
		SessionID:   session.ID,
		PairingCode: session.PairingCode,
		Status:      session.Status,
		Bound:       session.Status == bindStatusBound,
		Device:      s.state.Device,
		Message:     bindingStatusMessage(session.Status),
		ExpiresAt:   session.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *AgentService) Disable() models.AgentStatusResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()
	s.state.Enabled = false
	s.state.Device = nil
	s.state.DeviceToken = ""
	s.state.Registered = false
	s.state.RemoteConnected = false
	_ = s.saveStateLocked()
	return s.statusLocked()
}

func (s *AgentService) SyncNow() error {
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	if err := s.registerAndSyncLocked(); err != nil {
		s.state.LastSyncStatus = "server_unavailable"
		s.state.LastSyncMessage = err.Error()
		_ = s.saveStateLocked()
		s.mu.Unlock()
		return err
	}
	hasToken := strings.TrimSpace(s.state.DeviceToken) != ""
	s.mu.Unlock()
	if !hasToken {
		return nil
	}
	return s.EnsureRemoteConnection()
}

func (s *AgentService) Tools() []models.AgentTool {
	return detectAgentTools()
}

func (s *AgentService) Launch(req models.AgentLaunchRequest) (*models.AgentLaunchResponse, error) {
	s.mu.Lock()
	enabled := s.isEnabledLocked()
	s.mu.Unlock()
	if !enabled {
		return nil, errors.New("agent mode is not enabled for this device")
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
		BindingRequired: true,
		Platform:        runtime.GOOS,
		AgentServer:     currentAgentServerURL(),
		Runtime:         currentAgentRuntimeStatus(),
		Device:          s.state.Device,
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
			ID:         s.state.DeviceID,
			DeviceID:   s.state.DeviceID,
			UniqueCode: s.state.UniqueCode,
			Name:       defaultAgentDeviceName(),
			Platform:   agentPlatform(),
			Status:     "offline",
			BoundAt:    s.state.LastSyncAt,
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

func (s *AgentService) latestPendingSessionLocked() *models.AgentBindSession {
	var latest *agentBindSessionState
	now := time.Now()
	for _, session := range s.sessions {
		if session.Status != bindStatusPending || now.After(session.ExpiresAt) {
			continue
		}
		if latest == nil || session.CreatedAt.After(latest.CreatedAt) {
			latest = session
		}
	}
	if latest == nil {
		return nil
	}
	return &models.AgentBindSession{
		SessionID:   latest.ID,
		PairingCode: latest.PairingCode,
		ExpiresAt:   latest.ExpiresAt.UTC().Format(time.RFC3339),
		Status:      latest.Status,
	}
}

func (s *AgentService) resolveDeviceIDLocked() string {
	s.ensureDeviceIdentityLocked()
	return s.state.DeviceID
}

func (s *AgentService) ensureDeviceIdentityLocked() {
	if strings.TrimSpace(s.state.DeviceID) == "" {
		s.state.DeviceID = "dev-" + uuid.NewString()
	}
	if strings.TrimSpace(s.state.UniqueCode) == "" {
		s.state.UniqueCode = computeAgentUniqueCode(s.state.DeviceID)
	}
	if s.state.Device != nil {
		if strings.TrimSpace(s.state.Device.ID) == "" {
			s.state.Device.ID = s.state.DeviceID
		}
		if strings.TrimSpace(s.state.Device.DeviceID) == "" {
			s.state.Device.DeviceID = s.state.Device.ID
		}
		if strings.TrimSpace(s.state.Device.UniqueCode) == "" {
			s.state.Device.UniqueCode = s.state.UniqueCode
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
	s.state = state
	return nil
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

func buildAgentBindPayload(sessionID string, deviceID string) string {
	values := url.Values{}
	values.Set("session", sessionID)
	values.Set("device", deviceID)
	values.Set("platform", runtime.GOOS)
	return "aliang-agent://bind?" + values.Encode()
}

func generateQRCodeDataURL(payload string) (string, error) {
	png, err := qrcode.Encode(payload, qrcode.Medium, 256)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func defaultAgentDeviceName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return runtime.GOOS + " device"
	}
	return host
}

func bindingStatusMessage(status string) string {
	switch status {
	case bindStatusBound:
		return "Device bound. Agent mode is enabled."
	case bindStatusExpired:
		return "Binding QR code expired."
	default:
		return "Waiting for QR scan confirmation."
	}
}

type agentRemoteBindSession struct {
	ID          string
	Payload     string
	AgentSecret string
	PairingCode string
	ExpiresAt   time.Time
}

type agentRegisterPayload struct {
	DeviceID   string                    `json:"device_id"`
	UniqueCode string                    `json:"unique_code"`
	DeviceName string                    `json:"device_name"`
	Platform   string                    `json:"platform"`
	Arch       string                    `json:"arch"`
	Username   string                    `json:"username,omitempty"`
	Hostname   string                    `json:"hostname,omitempty"`
	AppVersion string                    `json:"app_version,omitempty"`
	Tools      []models.AgentTool        `json:"tools"`
	History    []models.AgentHistoryRoot `json:"history"`
	StartedAt  string                    `json:"started_at"`
}

func (s *AgentService) registerAndSyncLocked() error {
	if strings.TrimSpace(s.state.DeviceToken) == "" {
		s.state.Registered = false
		s.state.RemoteConnected = false
		s.state.LastSyncStatus = "pairing_required"
		s.state.LastSyncMessage = "Pair this device before syncing with the agent server."
		return s.saveStateLocked()
	}

	s.syncRuntimeDeviceStatusLocked()
	s.state.Enabled = true
	s.state.Registered = true
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = "connecting"
	s.state.LastSyncMessage = ""
	return s.saveStateLocked()
}

func (s *AgentService) startRemoteBindingLocked() (*agentRemoteBindSession, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || strings.TrimSpace(cfg.AgentBaseURL()) == "" {
		return nil, errors.New("agent server is not configured")
	}

	payload := map[string]interface{}{
		"device_id":     s.state.DeviceID,
		"device_name":   defaultAgentDeviceName(),
		"platform":      agentPlatform(),
		"agent_version": version.String(),
		"capabilities":  agentCapabilities(),
	}
	raw, err := s.callAgentServer(http.MethodPost, cfg.GetAgentPairingTicketsURL(), payload)
	if err != nil {
		return nil, err
	}

	var resp struct {
		TicketID     string   `json:"ticket_id"`
		Status       string   `json:"status"`
		PairingCode  string   `json:"pairing_code"`
		DeviceName   string   `json:"device_name"`
		Platform     string   `json:"platform"`
		Capabilities []string `json:"capabilities"`
		QRPayload    string   `json:"qr_payload"`
		PairingURL   string   `json:"pairing_url"`
		AgentSecret  string   `json:"agent_secret"`
		ExpiresAt    string   `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(resp.TicketID)
	if sessionID == "" {
		return nil, errors.New("agent server pairing response missing ticket_id")
	}
	agentSecret := strings.TrimSpace(resp.AgentSecret)
	if agentSecret == "" {
		return nil, errors.New("agent server pairing response missing agent_secret")
	}
	payloadText := strings.TrimSpace(resp.QRPayload)
	if payloadText == "" {
		payloadText = strings.TrimSpace(resp.PairingURL)
	}
	if payloadText == "" {
		payloadText = buildAgentBindPayload(sessionID, s.state.DeviceID)
	}
	expiresAt := time.Now().Add(agentBindTTL).UTC()
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(resp.ExpiresAt)); err == nil {
		expiresAt = parsed.UTC()
	}
	return &agentRemoteBindSession{
		ID:          sessionID,
		Payload:     payloadText,
		AgentSecret: agentSecret,
		PairingCode: strings.TrimSpace(resp.PairingCode),
		ExpiresAt:   expiresAt,
	}, nil
}

func (s *AgentService) pollRemoteBindingLocked(session *agentBindSessionState) (*models.AgentBindStatusResponse, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || strings.TrimSpace(cfg.AgentBaseURL()) == "" {
		return nil, errors.New("agent server is not configured")
	}
	if strings.TrimSpace(session.AgentSecret) == "" {
		return nil, errors.New("binding session is missing agent_secret")
	}
	raw, err := s.callAgentServer(http.MethodGet, cfg.GetAgentPairingTicketResultURL(session.ID, session.AgentSecret), nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		TicketID    string `json:"ticket_id"`
		Status      string `json:"status"`
		DeviceID    string `json:"device_id"`
		ApprovedAt  string `json:"approved_at"`
		ExpiresAt   string `json:"expires_at"`
		DeviceToken string `json:"device_token"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}

	status := strings.TrimSpace(resp.Status)
	if status == "" {
		status = bindStatusPending
	}
	if status == "approved" {
		deviceID := strings.TrimSpace(resp.DeviceID)
		if deviceID == "" {
			deviceID = s.state.DeviceID
		}
		boundAt := strings.TrimSpace(resp.ApprovedAt)
		if boundAt == "" {
			boundAt = time.Now().UTC().Format(time.RFC3339)
		}
		device := &models.AgentDevice{
			ID:                    deviceID,
			DeviceID:              deviceID,
			UniqueCode:            s.state.UniqueCode,
			Name:                  defaultAgentDeviceName(),
			Platform:              agentPlatform(),
			AgentVersion:          version.String(),
			Status:                "offline",
			Capabilities:          agentCapabilities(),
			RemoteTerminalEnabled: true,
			AIControlEnabled:      true,
			BoundAt:               boundAt,
			PairedAt:              boundAt,
		}
		s.state.DeviceID = deviceID
		s.state.Enabled = true
		s.state.Registered = true
		s.state.Device = device
		deviceToken := strings.TrimSpace(resp.DeviceToken)
		if deviceToken != "" {
			s.state.DeviceToken = deviceToken
		}
		session.Status = bindStatusBound
		s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
		s.state.LastSyncStatus = "paired"
		s.state.LastSyncMessage = ""
		_ = s.saveStateLocked()
		status = bindStatusBound
		if deviceToken != "" {
			go func() {
				_ = s.EnsureRemoteConnection()
			}()
		}
	} else if status == "expired" {
		session.Status = bindStatusExpired
		status = bindStatusExpired
	}

	return &models.AgentBindStatusResponse{
		SessionID:   session.ID,
		PairingCode: session.PairingCode,
		Status:      status,
		Bound:       status == bindStatusBound,
		Device:      s.state.Device,
		Message:     bindingStatusMessage(status),
		ExpiresAt:   session.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *AgentService) callAgentServer(method string, endpoint string, payload interface{}) ([]byte, error) {
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
	if authHeader := strings.TrimSpace(auth.GetCurrentAuthorizationHeader()); authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if strings.TrimSpace(s.state.DeviceToken) != "" {
		req.Header.Set("X-Agent-Device-Token", strings.TrimSpace(s.state.DeviceToken))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("agent server returned %d: %s", resp.StatusCode, string(raw))
	}
	return unwrapAgentServerData(raw)
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

func buildAgentRegisterPayload(deviceID string, uniqueCode string) agentRegisterPayload {
	username := ""
	if current, err := osuser.Current(); err == nil && current != nil {
		username = current.Username
	}
	hostname, _ := os.Hostname()
	return agentRegisterPayload{
		DeviceID:   deviceID,
		UniqueCode: uniqueCode,
		DeviceName: defaultAgentDeviceName(),
		Platform:   runtime.GOOS,
		Arch:       runtime.GOARCH,
		Username:   username,
		Hostname:   hostname,
		AppVersion: version.String(),
		Tools:      detectAgentTools(),
		History:    collectAgentHistoryRoots(),
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	}
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

func computeAgentUniqueCode(deviceID string) string {
	hostname, _ := os.Hostname()
	username := ""
	if current, err := osuser.Current(); err == nil && current != nil {
		username = current.Username
	}
	raw := strings.Join([]string{deviceID, hostname, username, runtime.GOOS, runtime.GOARCH}, "|")
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:])[:24]
}

func currentAgentServerURL() string {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return ""
	}
	return cfg.AgentBaseURL()
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
