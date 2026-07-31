package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	model "aliang.one/nursorgate/common/model"
	httpServer "aliang.one/nursorgate/inbound/http"
	"aliang.one/nursorgate/inbound/tun/engine"
	runner2 "aliang.one/nursorgate/inbound/tun/runner"
	"aliang.one/nursorgate/outbound"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/runtime"
)

var (
	activeIngressModeResolver = activeIngressModeFromSnapshot
	applyIngressModeUpdater   = applyIngressModeToSnapshot
	tunStartRunner            = defaultStartTUN
	tunPrepareStartRunner     = runner2.PrepareStart
	httpStartRunner           = httpServer.StartMitmHttp
	httpStopRunner            = httpServer.StopHttpProxy
	tunStopRunner             = runner2.Stop
	// httpProxyIsRunningProbe reports whether the HTTP proxy listener (56432) is
	// actually accepting connections. Injectable for tests. The auth-expiry stop
	// path keys off this rather than runService.isRunning, so a desynced flag
	// (after a mode switch / daemon restart / activation rollback) cannot leave a
	// live proxy serving a dead token.
	httpProxyIsRunningProbe      = httpServer.IsHttpRunning
	tunProxyIsRunningProbe       = engine.IsRunning
	runModeStoreFactory          = func() runModeSnapshotStore { return storage.NewSoftwareConfigStore() }
	aliangLinkStatusResolver     = resolveAliangLinkStatus
	softwareUpdateStatusResolver = func() models.SoftwareVersionUpdateFrontendStatus {
		return GetSharedSoftwareUpdateService().GetFrontendStatus()
	}
	sharedRunServiceMu sync.Mutex
	sharedRunService   *RunService
)

const (
	runModeSnapshotSoftware = "runtime"
	runModeSnapshotName     = "run-mode"
	runModeSnapshotPath     = "runtime://run-mode"
)

func runModeDisplayName(mode models.RunMode) string {
	switch mode {
	case models.ModeTUN:
		return "Deep Mode"
	case models.ModeHTTP:
		return "Regular Mode"
	default:
		return strings.ToUpper(string(mode))
	}
}

type runModeSnapshotStore interface {
	SaveEffectiveConfigSnapshot(snapshot models.SoftwareEffectiveConfigSnapshot) error
	GetLatestEffectiveConfigSnapshotBySoftwareAndName(software string, configName string) (*models.SoftwareEffectiveConfigSnapshot, error)
}

type aliangLinkStatusProvider interface {
	LinkStatusSnapshot() map[string]interface{}
	ProbeLink(ctx context.Context) map[string]interface{}
}

// RunService handles run/mode operations
type RunService struct {
	modeChangeMutex sync.RWMutex
	currentMode     models.RunMode
	isRunning       bool // 统一使用 isRunning 字段
	store           runModeSnapshotStore
}

// NewRunService creates a new run service instance
func NewRunService() *RunService {
	service := &RunService{
		currentMode: models.ModeHTTP,
		isRunning:   false,
		store:       runModeStoreFactory(),
	}
	service.restorePersistedMode()
	return service
}

// GetSharedRunService returns the process-wide run service used by runtime integrations
// such as the API server and the tray menu.
func GetSharedRunService() *RunService {
	sharedRunServiceMu.Lock()
	defer sharedRunServiceMu.Unlock()
	if sharedRunService == nil {
		sharedRunService = NewRunService()
	}
	return sharedRunService
}

// ResetSharedRunServiceForTest resets the shared run service singleton.
func ResetSharedRunServiceForTest() {
	sharedRunServiceMu.Lock()
	defer sharedRunServiceMu.Unlock()
	sharedRunService = nil
}

// GetCurrentMode returns the current operating mode
func (rs *RunService) GetCurrentMode() string {
	rs.modeChangeMutex.RLock()
	defer rs.modeChangeMutex.RUnlock()
	if mode, ok := activeIngressModeResolver(); ok {
		return string(mode)
	}
	return string(rs.currentMode)
}

// SetCurrentMode sets the operating mode
func (rs *RunService) SetCurrentMode(mode string) {
	rs.modeChangeMutex.Lock()
	defer rs.modeChangeMutex.Unlock()
	rs.currentMode = models.RunMode(mode)
}

// IsRunning returns whether a service is currently running
func (rs *RunService) IsRunning() bool {
	rs.modeChangeMutex.RLock()
	defer rs.modeChangeMutex.RUnlock()
	return rs.isRunning
}

// SetRunning sets the running state
func (rs *RunService) SetRunning(running bool) {
	rs.modeChangeMutex.Lock()
	defer rs.modeChangeMutex.Unlock()
	rs.isRunning = running
}

// StartService starts the service for the current mode
func (rs *RunService) StartService() map[string]interface{} {
	if result := proxySessionStartFailure(); result != nil {
		return result
	}
	startupState := runtime.GetStartupState()
	if !canStartProxyWithStatus(startupState.GetStatus()) {
		return map[string]interface{}{
			"error":  "activation_required",
			"status": "failed",
			"msg":    "系统尚未准备好启动代理，请先完成登录或配置恢复。",
		}
	}
	updateStatus := softwareUpdateStatusResolver()
	if updateStatus.BlockingProxyStart {
		return map[string]interface{}{
			"error":         "force_update_required",
			"status":        "failed",
			"msg":           fmt.Sprintf("发现强制更新版本 %s，请先完成升级后再启动代理服务。", updateStatus.LatestVersion),
			"update_status": updateStatus,
		}
	}

	rs.modeChangeMutex.Lock()
	// Re-check after acquiring the mode lock. Logout closes the startup gate
	// before taking this lock; without this second check, a start request that
	// observed READY earlier could wait for logout to stop ingress and then start
	// it again immediately afterwards.
	if !canStartProxyWithStatus(runtime.GetStartupState().GetStatus()) {
		rs.modeChangeMutex.Unlock()
		return map[string]interface{}{
			"error":  "activation_required",
			"status": "failed",
			"msg":    "系统尚未准备好启动代理，请先完成登录或配置恢复。",
		}
	}
	if result := proxySessionStartFailure(); result != nil {
		rs.modeChangeMutex.Unlock()
		return result
	}

	// Check if already running
	if rs.isRunning {
		rs.modeChangeMutex.Unlock()
		return map[string]interface{}{
			"status":  "already_running",
			"message": "Service is already running",
		}
	}

	startMode := rs.resolveAuthoritativeModeLocked()
	if startMode == models.ModeTUN {
		wintunStatus := getSharedWintunDependencyController().Refresh()
		if wintunStatus.Supported && wintunStatus.Required && !wintunStatus.Available {
			rs.modeChangeMutex.Unlock()
			errorCode := "wintun_required"
			if wintunStatus.Installing {
				errorCode = "wintun_installing"
			}
			return map[string]interface{}{
				"error":      errorCode,
				"status":     "failed",
				"msg":        wintunStatus.Message,
				"dependency": wintunStatus,
			}
		}
	}
	if startMode == models.ModeTUN {
		tunPrepareStartRunner()
	}
	rs.isRunning = true // 先设置运行状态，避免并发启动
	rs.modeChangeMutex.Unlock()

	logger.Info("Starting " + string(startMode) + " service...")

	switch startMode {
	case models.ModeTUN:
		return rs.startTUN()
	case models.ModeHTTP:
		// HTTP 服务内部也有检查，但我们需要先设置状态
		startHTTP := httpStartRunner
		go func(runner func()) {
			runner()
			// 如果启动失败，HTTP 服务内部会处理，但我们需要确保状态同步
			// 注意：StartMitmHttp 是阻塞的，只有在停止时才会返回
		}(startHTTP)
		return map[string]interface{}{
			"status":  "success",
			"message": "HTTP proxy server is starting",
			"details": fmt.Sprintf("HTTP proxy server is starting on port %d", config.DefaultHTTPProxyPort),
			"port":    fmt.Sprintf("%d", config.DefaultHTTPProxyPort),
		}
	default:
		// 未知模式，回滚状态
		rs.modeChangeMutex.Lock()
		rs.isRunning = false
		rs.modeChangeMutex.Unlock()
		return map[string]interface{}{
			"error":  "unknown_mode",
			"status": "failed",
			"msg":    "Unknown mode: " + string(startMode),
		}
	}
}

func canStartProxyWithStatus(status runtime.StartupStatus) bool {
	return status == runtime.READY || status == runtime.CONFIGURED
}

func proxySessionStartFailure() map[string]interface{} {
	snapshot := auth.GetSessionAuthority().Snapshot()
	if snapshot.State == auth.StateActive {
		return nil
	}
	errorCode := "session_invalid"
	message := "登录状态已失效，请重新登录后再启动代理。"
	if snapshot.State == auth.StateRestoring || snapshot.State == auth.StateSoftExpired {
		errorCode = "session_recovering"
		message = "登录状态正在恢复，恢复完成前不能启动代理。"
	}
	return map[string]interface{}{
		"error":  errorCode,
		"status": "failed",
		"msg":    message,
	}
}

// startTUN handles TUN mode startup
func (rs *RunService) startTUN() map[string]interface{} {
	res := tunStartRunner()

	rs.modeChangeMutex.Lock()
	result := rs.handleTUNStartResultLocked(res)
	rs.modeChangeMutex.Unlock()

	return result
}

// StopService stops the current running service
func (rs *RunService) StopService() map[string]interface{} {
	rs.modeChangeMutex.Lock()

	if !rs.isRunning {
		rs.modeChangeMutex.Unlock()
		return map[string]interface{}{
			"error":  "not_running",
			"status": "failed",
			"msg":    "No service is currently running",
		}
	}

	stoppedMode := rs.currentMode
	rs.isRunning = false
	rs.modeChangeMutex.Unlock()

	logger.Info("Stopping " + string(stoppedMode) + " service...")

	response := map[string]interface{}{
		"status":       "success",
		"message":      string(stoppedMode) + " service stopped successfully",
		"stopped_mode": stoppedMode,
	}

	switch stoppedMode {
	case models.ModeHTTP:
		logger.Info("Stopping HTTP proxy server...")
		httpStopRunner()
		response["details"] = fmt.Sprintf("HTTP proxy server on %s has been stopped", config.HTTPProxyListenAddr())

	case models.ModeTUN:
		logger.Info("Stopping TUN service...")
		tunStopRunner()
		response["details"] = "TUN interface service has been stopped"
	}

	return response
}

// StopIngressIfActive tears down the ingress proxy when the user session
// hard-expires. Unlike StopService, it stops based on the REAL listener state
// (httpProxyIsRunningProbe), so it still closes 56432 when runService.isRunning
// has desynced to false after a mode switch, daemon restart, or activation
// rollback. Returns whether anything was stopped.
func (rs *RunService) StopIngressIfActive() bool {
	rs.modeChangeMutex.Lock()
	mode := rs.resolveAuthoritativeModeLocked()
	wasRunning := rs.isRunning
	rs.modeChangeMutex.Unlock()

	// Stop based on the REAL listener state. The runService.isRunning flag can
	// desync false after a mode switch, daemon restart, or activation rollback
	// while 56432 is still bound — keying off only the flag leaves the proxy
	// serving a dead token (forwarding to a cloud that returns 401).
	actualRunning := httpProxyIsRunningProbe()
	if mode == models.ModeTUN {
		actualRunning = tunProxyIsRunningProbe()
	}
	if !wasRunning && !actualRunning {
		return false
	}

	switch mode {
	case models.ModeTUN:
		tunStopRunner()
	default:
		httpStopRunner()
	}
	rs.SetRunning(false)
	return true
}

// StopIngressForLogout stops the authoritative ingress mode when either local
// bookkeeping or the mode-specific resource probe says it is active. Explicit
// logout is a security boundary, but an already inactive TUN must not receive a
// stop signal that could leak into its next lifecycle.
func (rs *RunService) StopIngressForLogout() (models.RunMode, bool) {
	rs.modeChangeMutex.Lock()
	mode := rs.resolveAuthoritativeModeLocked()
	wasRunning := rs.isRunning
	rs.isRunning = false
	rs.modeChangeMutex.Unlock()

	actualRunning := httpProxyIsRunningProbe()
	if mode == models.ModeTUN {
		actualRunning = tunProxyIsRunningProbe()
	}
	if !wasRunning && !actualRunning {
		return mode, false
	}

	switch mode {
	case models.ModeTUN:
		tunStopRunner()
	default:
		mode = models.ModeHTTP
		httpStopRunner()
	}
	return mode, true
}

// GetStatus returns the current service status
func (rs *RunService) GetStatus() map[string]interface{} {
	rs.modeChangeMutex.RLock()
	defer rs.modeChangeMutex.RUnlock()
	mode := rs.currentMode
	if authoritativeMode, ok := activeIngressModeResolver(); ok {
		mode = authoritativeMode
	}

	response := map[string]interface{}{
		"current_mode": string(mode),
		"is_running":   rs.isRunning,
		"available_modes": []string{
			string(models.ModeHTTP),
			string(models.ModeTUN),
		},
		"wintun_dependency": getSharedWintunDependencyController().Status(),
		"tun_startup":       runner2.GetStartupProgress(),
	}

	switch mode {
	case models.ModeTUN:
		if rs.isRunning {
			response["status"] = "Deep Mode is running"
			response["description"] = "System traffic is being routed through the TUN interface."
		} else {
			response["status"] = "Deep Mode is selected, service not running"
			response["description"] = "Deep Mode is ready. Click start when you want to enable system-wide proxying."
		}
	case models.ModeHTTP:
		if rs.isRunning {
			response["status"] = "Regular Mode is running"
			response["description"] = fmt.Sprintf("HTTP CONNECT proxy is running on port %d.", config.DefaultHTTPProxyPort)
		} else {
			response["status"] = "Regular Mode is selected, service not running"
			response["description"] = "Regular Mode is ready. Click start when you want to enable local proxying."
		}
	}

	return response
}

// GetAliangLinkStatus returns the current mTLS link status for the aliang outbound.
func (rs *RunService) GetAliangLinkStatus(ctx context.Context, probe bool) map[string]interface{} {
	return aliangLinkStatusResolver(ctx, probe)
}

func GetTUNStartupStatus() map[string]interface{} {
	progress := runner2.GetStartupProgress()
	return map[string]interface{}{
		"active":              progress.Active,
		"status":              progress.Status,
		"phase":               progress.Phase,
		"progress_percent":    progress.Progress,
		"message":             progress.Message,
		"error":               progress.Error,
		"errors":              progress.Errors,
		"retry_count":         progress.RetryCount,
		"max_retries":         progress.MaxRetries,
		"permission_required": progress.PermissionRequired,
		"updated_at":          progress.UpdatedAt,
	}
}

// SwitchMode switches the operating mode
func (rs *RunService) SwitchMode(targetMode string) map[string]interface{} {
	rs.modeChangeMutex.Lock()
	defer rs.modeChangeMutex.Unlock()

	targetModeEnum := models.RunMode(targetMode)

	// Validate target mode
	if targetModeEnum != models.ModeHTTP && targetModeEnum != models.ModeTUN {
		return map[string]interface{}{
			"error":  "invalid_mode",
			"status": "failed",
			"msg":    "Invalid target mode: " + targetMode + ". Must be 'http' or 'tun'",
		}
	}

	authoritativeMode := rs.resolveAuthoritativeModeLocked()

	if targetModeEnum == models.ModeTUN {
		wintunStatus := getSharedWintunDependencyController().Refresh()
		if wintunStatus.Supported && wintunStatus.Required && !wintunStatus.Available {
			errorCode := "wintun_required"
			if wintunStatus.Installing {
				errorCode = "wintun_installing"
			}
			return map[string]interface{}{
				"error":      errorCode,
				"status":     "failed",
				"msg":        wintunStatus.Message,
				"dependency": wintunStatus,
			}
		}
	}

	previousMode := authoritativeMode
	wasRunning := rs.isRunning
	if previousMode == targetModeEnum {
		message := "Run mode is already set to " + runModeDisplayName(targetModeEnum) + "."
		if wasRunning {
			message += " The current proxy keeps running until you stop it."
		} else {
			message += " Call start to activate the proxy."
		}
		return map[string]interface{}{
			"status":       "unchanged",
			"current_mode": string(authoritativeMode),
			"is_running":   rs.isRunning,
			"message":      message,
		}
	}

	if wasRunning {
		logger.Info("Stopping " + string(previousMode) + " service before applying " + string(targetModeEnum) + " mode...")
		rs.isRunning = false
		rs.stopServiceSync(previousMode)
	}

	if err := applyIngressModeUpdater(targetModeEnum); err != nil {
		rs.rollbackToPreviousModeLocked(previousMode, wasRunning)
		return map[string]interface{}{
			"error":          "switch_failed",
			"status":         "failed",
			"msg":            fmt.Sprintf("failed to activate ingress mode %s: %v", targetModeEnum, err),
			"current_mode":   string(rs.resolveAuthoritativeModeLocked()),
			"rollback_state": map[string]interface{}{"mode": string(previousMode), "running": rs.isRunning},
		}
	}

	rs.currentMode = targetModeEnum
	if err := rs.persistModeLocked(targetModeEnum); err != nil {
		rs.rollbackToPreviousModeLocked(previousMode, wasRunning)
		return map[string]interface{}{
			"error":          "switch_failed",
			"status":         "failed",
			"msg":            fmt.Sprintf("failed to persist ingress mode %s: %v", targetModeEnum, err),
			"current_mode":   string(rs.resolveAuthoritativeModeLocked()),
			"rollback_state": map[string]interface{}{"mode": string(previousMode), "running": rs.isRunning},
		}
	}

	logger.Info("Switching to " + string(targetModeEnum) + " mode")

	response := map[string]interface{}{
		"status":                  "switched",
		"target_mode":             targetModeEnum,
		"current_mode":            string(targetModeEnum),
		"is_running":              rs.isRunning,
		"stopped_running_service": wasRunning,
		"previous_mode":           string(previousMode),
	}

	switch targetModeEnum {
	case models.ModeHTTP:
		// 检查是否已经在运行 HTTP 服务
		response["message"] = "Switched to Regular Mode. Proxy remains stopped until start is called."
		response["usage"] = "POST /api/run/start after restoring an authenticated session"
		if wasRunning {
			response["details"] = "The previously running " + runModeDisplayName(previousMode) + " service was stopped. Call start to launch Regular Mode."
		} else {
			response["details"] = "Regular Mode is selected and ready to start."
		}
		response["next_action"] = "Call start to activate Regular Mode"

	case models.ModeTUN:
		response["message"] = "Switched to Deep Mode. Proxy remains stopped until start is called."
		response["usage"] = "POST /api/run/start after restoring an authenticated session"
		if wasRunning {
			response["details"] = "The previously running " + runModeDisplayName(previousMode) + " service was stopped. Call start to launch Deep Mode."
		} else {
			response["details"] = "Deep Mode is selected and ready to start."
		}
		response["next_step"] = "Call start to initialize and launch Deep Mode"
	}

	return response
}

func (rs *RunService) restorePersistedMode() {
	if rs == nil || rs.store == nil {
		return
	}

	snapshot, err := rs.store.GetLatestEffectiveConfigSnapshotBySoftwareAndName(runModeSnapshotSoftware, runModeSnapshotName)
	if err != nil || snapshot == nil || strings.TrimSpace(snapshot.SnapshotJSON) == "" {
		return
	}

	var payload struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(snapshot.SnapshotJSON), &payload); err != nil {
		logger.Warn(fmt.Sprintf("Failed to decode persisted run mode snapshot: %v", err))
		return
	}

	mode := models.RunMode(strings.ToLower(strings.TrimSpace(payload.Mode)))
	if mode != models.ModeHTTP && mode != models.ModeTUN {
		return
	}

	rs.currentMode = mode
}

func (rs *RunService) persistModeLocked(mode models.RunMode) error {
	if rs == nil || rs.store == nil {
		return nil
	}

	content, err := json.Marshal(map[string]string{
		"mode": string(mode),
	})
	if err != nil {
		return fmt.Errorf("marshal run mode snapshot: %w", err)
	}

	return rs.store.SaveEffectiveConfigSnapshot(models.SoftwareEffectiveConfigSnapshot{
		Software:       runModeSnapshotSoftware,
		ConfigUUID:     uuid.NewString(),
		ConfigName:     runModeSnapshotName,
		ConfigFilePath: runModeSnapshotPath,
		ConfigVersion:  "",
		ConfigFormat:   models.ConfigFormatJSON,
		SnapshotJSON:   string(content),
	})
}

// stopServiceSync synchronously stops a service
func (rs *RunService) stopServiceSync(mode models.RunMode) {
	switch mode {
	case models.ModeHTTP:
		logger.Info("Stopping HTTP proxy server...")
		httpStopRunner()
		logger.Info("HTTP proxy server stopped")

	case models.ModeTUN:
		logger.Info("Stopping TUN service...")
		tunStopRunner()
		logger.Info("TUN service stopped")
	}
}

func (rs *RunService) resolveAuthoritativeModeLocked() models.RunMode {
	if mode, ok := activeIngressModeResolver(); ok {
		rs.currentMode = mode
		return mode
	}
	return rs.currentMode
}

func (rs *RunService) rollbackToPreviousModeLocked(previousMode models.RunMode, shouldRun bool) {
	if err := applyIngressModeUpdater(previousMode); err != nil {
		logger.Error(fmt.Sprintf("Failed to roll back ingress mode to %s: %v", previousMode, err))
	}
	rs.currentMode = previousMode
	if shouldRun {
		rs.isRunning = true
		rs.startModeAsync(previousMode)
	}
}

func (rs *RunService) rollbackFromActivationFailureLocked(previousMode models.RunMode, targetResult map[string]interface{}) {
	rs.isRunning = false
	if err := applyIngressModeUpdater(previousMode); err != nil {
		logger.Error(fmt.Sprintf("Failed to roll back ingress mode after activation failure: %v", err))
	}
	rs.currentMode = previousMode
	rs.isRunning = true
	rs.startModeAsync(previousMode)
	logger.Error(fmt.Sprintf("Hot switch activation failed, rolled back to %s mode: %+v", previousMode, targetResult))
}

func (rs *RunService) startModeAsync(mode models.RunMode) {
	switch mode {
	case models.ModeHTTP:
		go func() {
			logger.Info("Starting HTTP proxy server...")
			httpStartRunner()
		}()
	case models.ModeTUN:
		go func() {
			res := rs.startTUN()
			if status, ok := res["status"].(string); ok && status == "failed" {
				logger.Error(fmt.Sprintf("Rollback TUN restart failed: %+v", res))
			}
		}()
	}
}

func activeIngressModeFromSnapshot() (models.RunMode, bool) {
	active := config.GetRoutingApplyStore().ActiveSnapshot()
	snapshot, ok := active.(*routing.RuntimeSnapshot)
	if !ok || snapshot == nil {
		return "", false
	}
	mode := models.RunMode(strings.ToLower(strings.TrimSpace(snapshot.IngressMode())))
	if mode != models.ModeHTTP && mode != models.ModeTUN {
		return "", false
	}
	return mode, true
}

func applyIngressModeToSnapshot(mode models.RunMode) error {
	store := config.GetRoutingApplyStore()
	canonical := store.ActiveCanonicalSchema()
	if canonical == nil {
		canonical = bootstrapCanonicalRoutingSchema(mode)
	} else {
		canonical.Ingress.Mode = string(mode)
	}

	raw, err := json.Marshal(canonical)
	if err != nil {
		return fmt.Errorf("marshal canonical routing schema failed: %w", err)
	}

	_, err = store.Apply(raw, func(cfg *config.CanonicalRoutingSchema) (any, error) {
		return routing.CompileRuntimeSnapshot(cfg)
	})
	if err != nil {
		return fmt.Errorf("apply ingress mode snapshot failed: %w", err)
	}
	return nil
}

func bootstrapCanonicalRoutingSchema(mode models.RunMode) *config.CanonicalRoutingSchema {
	globalCfg := config.GetGlobalConfig()
	upstreamType := "socks"
	toSocksEnabled := false
	if globalCfg != nil && globalCfg.Customer != nil && globalCfg.Customer.Proxy != nil {
		proxyType := strings.ToLower(strings.TrimSpace(globalCfg.Customer.Proxy.Type))
		if globalCfg.Customer.Proxy.IsEnabled() && (proxyType == "http" || proxyType == "socks5") {
			toSocksEnabled = strings.TrimSpace(globalCfg.Customer.Proxy.Server) != ""
			if proxyType == "http" {
				upstreamType = "http"
			}
		}
	}

	canonical := &config.CanonicalRoutingSchema{
		Version: config.CanonicalRoutingSchemaVersion,
		Ingress: config.CanonicalIngressConfig{Mode: string(mode)},
		Egress: config.CanonicalEgressConfig{
			Direct:   config.CanonicalEgressBranch{Enabled: true},
			ToAliang: config.CanonicalEgressBranch{Enabled: true},
			ToSocks: config.CanonicalSocksEgressBranch{
				Enabled:  toSocksEnabled,
				Upstream: config.CanonicalSocksUpstream{Type: upstreamType},
			},
		},
		Routing: config.CanonicalRoutingConfig{Rules: []config.CanonicalRoutingRule{}},
	}

	if globalCfg != nil {
		if compiled, err := routing.CompileCanonicalRoutingFromRuntimeInputs(globalCfg, model.RulesSettings{
			AliangEnabled: true,
			SocksEnabled:  toSocksEnabled,
		}); err == nil && compiled != nil {
			compiled.Ingress.Mode = string(mode)
			return compiled
		} else if err != nil {
			logger.Warn(fmt.Sprintf("Failed to bootstrap routing schema from runtime config, using minimal schema: %v", err))
		}
	}

	return canonical
}

func defaultStartTUN() map[string]string {
	go runner2.Start()
	return <-runner2.RunStatusChan
}

func (rs *RunService) handleTUNStartResultLocked(res map[string]string) map[string]interface{} {
	if status, ok := res["status"]; ok {
		switch status {
		case "failed":
			rs.isRunning = false
		case "success":
			rs.isRunning = true
		}
	}

	result := make(map[string]interface{}, len(res))
	for k, v := range res {
		result[k] = v
	}
	return result
}

func resolveAliangLinkStatus(ctx context.Context, probe bool) map[string]interface{} {
	registry := outbound.GetRegistry()
	aliangProxy, err := registry.GetAliang()
	if err != nil {
		return map[string]interface{}{
			"state":      "disconnected",
			"latency_ms": int64(0),
			"last_error": err.Error(),
		}
	}

	reporter, ok := aliangProxy.(aliangLinkStatusProvider)
	if !ok {
		return map[string]interface{}{
			"server_addr": aliangProxy.Addr(),
			"state":       "unknown",
			"latency_ms":  int64(0),
			"last_error":  "aliang outbound does not expose link status",
		}
	}

	if probe {
		return reporter.ProbeLink(ctx)
	}
	return reporter.LinkStatusSnapshot()
}
