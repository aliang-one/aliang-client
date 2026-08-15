package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	appVersion "aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/processor/config"
)

const (
	defaultSoftwareUpdateName  = "aliang-gateway"
	softwareUpdatePollInterval = time.Hour
)

var versionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

type softwareUpdateStore interface {
	UpsertSnapshot(snapshot models.SoftwareVersionUpdateSnapshot) error
	GetSnapshot(software string, platform string) (*models.SoftwareVersionUpdateSnapshot, error)
	UpsertDismissal(dismissal models.SoftwareVersionUpdateDismissal) error
	GetDismissal(software string, platform string, latestVersion string) (*models.SoftwareVersionUpdateDismissal, error)
}

type softwareUpdateRemotePayload struct {
	SoftwareName   string `json:"software_name"`
	Platform       string `json:"platform"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	DownloadURL    string `json:"download_url"`
	FileType       string `json:"file_type"`
	ForceUpdate    bool   `json:"force_update"`
	NeedsUpdate    bool   `json:"needs_update"`
	Changelog      string `json:"changelog"`
}

type softwareUpdateEnvelope struct {
	Code int                         `json:"code"`
	Msg  string                      `json:"msg"`
	Data softwareUpdateRemotePayload `json:"data"`
}

type softwareUpdateRuntimeInfo struct {
	software string
	platform string
	version  string
}

type softwareUpdateStatusProvider interface {
	GetFrontendStatus() models.SoftwareVersionUpdateFrontendStatus
}

type SoftwareUpdateService struct {
	store      softwareUpdateStore
	client     *http.Client
	now        func() time.Time
	startOnce  sync.Once
	refreshMu  sync.Mutex
	triggerCh  chan struct{}
	stopCh     chan struct{}
	dismissals map[string]bool
	dismissMu  sync.RWMutex
}

var (
	sharedSoftwareUpdateServiceMu sync.Mutex
	sharedSoftwareUpdateService   *SoftwareUpdateService
	softwareUpdateStoreFactory    = func() softwareUpdateStore { return storage.NewSoftwareVersionUpdateStore() }
	softwareUpdateNow             = time.Now
	softwareUpdateHTTPClient      = &http.Client{Timeout: 10 * time.Second}
)

func NewSoftwareUpdateService() *SoftwareUpdateService {
	return &SoftwareUpdateService{
		store:      softwareUpdateStoreFactory(),
		client:     softwareUpdateHTTPClient,
		now:        softwareUpdateNow,
		triggerCh:  make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		dismissals: make(map[string]bool),
	}
}

func NewSoftwareUpdateServiceWithStore(store softwareUpdateStore) *SoftwareUpdateService {
	if store == nil {
		store = softwareUpdateStoreFactory()
	}
	return &SoftwareUpdateService{
		store:      store,
		client:     softwareUpdateHTTPClient,
		now:        softwareUpdateNow,
		triggerCh:  make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		dismissals: make(map[string]bool),
	}
}

func GetSharedSoftwareUpdateService() *SoftwareUpdateService {
	sharedSoftwareUpdateServiceMu.Lock()
	defer sharedSoftwareUpdateServiceMu.Unlock()
	if sharedSoftwareUpdateService == nil {
		sharedSoftwareUpdateService = NewSoftwareUpdateService()
	}
	return sharedSoftwareUpdateService
}

func ResetSharedSoftwareUpdateServiceForTest() {
	sharedSoftwareUpdateServiceMu.Lock()
	defer sharedSoftwareUpdateServiceMu.Unlock()
	if sharedSoftwareUpdateService != nil {
		close(sharedSoftwareUpdateService.stopCh)
	}
	sharedSoftwareUpdateService = nil
}

func StartSoftwareUpdateChecker() {
	GetSharedSoftwareUpdateService().Start()
}

func (s *SoftwareUpdateService) Start() {
	if s == nil {
		return
	}

	s.startOnce.Do(func() {
		go s.loop()
		s.TriggerRefresh()
	})
}

func (s *SoftwareUpdateService) loop() {
	ticker := time.NewTicker(softwareUpdatePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-s.triggerCh:
			if err := s.RefreshNow(context.Background()); err != nil {
				logger.Warn(fmt.Sprintf("software update refresh failed: %v", err))
			}
		case <-ticker.C:
			if err := s.RefreshNow(context.Background()); err != nil {
				logger.Warn(fmt.Sprintf("software update scheduled refresh failed: %v", err))
			}
		}
	}
}

func (s *SoftwareUpdateService) TriggerRefresh() {
	if s == nil {
		return
	}

	select {
	case s.triggerCh <- struct{}{}:
	default:
	}
}

func (s *SoftwareUpdateService) RefreshNow(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("software update service is nil")
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	runtimeInfo, err := resolveSoftwareUpdateRuntimeInfo()
	if err != nil {
		return s.persistFailureState(softwareUpdateRuntimeInfo{
			software: resolveSoftwareUpdateSoftwareName(),
			platform: runtime.GOOS,
		}, err)
	}

	payload, err := s.fetchRemoteUpdate(ctx, runtimeInfo)
	if err != nil {
		return s.persistFailureState(runtimeInfo, err)
	}

	now := s.now()
	existing, lookupErr := s.store.GetSnapshot(runtimeInfo.software, runtimeInfo.platform)
	if lookupErr != nil {
		return lookupErr
	}

	currentVersion := coalesceString(normalizeVersionString(payload.CurrentVersion), runtimeInfo.version)
	latestVersion := coalesceString(normalizeVersionString(payload.LatestVersion), currentVersion, runtimeInfo.version)
	needsUpdate := payload.NeedsUpdate && isSoftwareUpdateNewer(latestVersion, currentVersion)
	forceUpdate := needsUpdate && payload.ForceUpdate

	firstSeenAt := time.Time{}
	lastSeenAt := time.Time{}
	if needsUpdate {
		firstSeenAt = now
		if existing != nil && existing.NeedsUpdate && strings.EqualFold(existing.LatestVersion, payload.LatestVersion) && !existing.FirstSeenAt.IsZero() {
			firstSeenAt = existing.FirstSeenAt
		}
		lastSeenAt = now
	}

	snapshot := models.SoftwareVersionUpdateSnapshot{
		Software:       runtimeInfo.software,
		Platform:       runtimeInfo.platform,
		CurrentVersion: currentVersion,
		LatestVersion:  latestVersion,
		DownloadURL:    strings.TrimSpace(payload.DownloadURL),
		FileType:       strings.TrimSpace(payload.FileType),
		Changelog:      strings.TrimSpace(payload.Changelog),
		NeedsUpdate:    needsUpdate,
		ForceUpdate:    forceUpdate,
		Status:         softwareUpdateStatusLabel(needsUpdate, forceUpdate, ""),
		LastError:      "",
		CheckedAt:      now,
		FirstSeenAt:    firstSeenAt,
		LastSeenAt:     lastSeenAt,
	}

	if snapshot.CurrentVersion == "" {
		snapshot.CurrentVersion = runtimeInfo.version
	}
	if snapshot.LatestVersion == "" {
		snapshot.LatestVersion = runtimeInfo.version
	}

	if err := s.store.UpsertSnapshot(snapshot); err != nil {
		return err
	}

	return nil
}

func (s *SoftwareUpdateService) GetFrontendStatus() models.SoftwareVersionUpdateFrontendStatus {
	runtimeInfo, runtimeErr := resolveSoftwareUpdateRuntimeInfo()
	if runtimeErr != nil {
		return models.SoftwareVersionUpdateFrontendStatus{
			Software:         resolveSoftwareUpdateSoftwareName(),
			Platform:         runtime.GOOS,
			Status:           "error",
			LastError:        runtimeErr.Error(),
			IndicatorVisible: false,
		}
	}

	snapshot, err := s.store.GetSnapshot(runtimeInfo.software, runtimeInfo.platform)
	if err != nil {
		return models.SoftwareVersionUpdateFrontendStatus{
			Software:         runtimeInfo.software,
			Platform:         runtimeInfo.platform,
			CurrentVersion:   runtimeInfo.version,
			Status:           "error",
			LastError:        err.Error(),
			IndicatorVisible: false,
		}
	}

	if snapshot == nil {
		return models.SoftwareVersionUpdateFrontendStatus{
			Software:         runtimeInfo.software,
			Platform:         runtimeInfo.platform,
			CurrentVersion:   runtimeInfo.version,
			Status:           "unknown",
			IndicatorVisible: false,
		}
	}

	currentVersion := coalesceString(normalizeVersionString(snapshot.CurrentVersion), runtimeInfo.version)
	latestVersion := coalesceString(normalizeVersionString(snapshot.LatestVersion), currentVersion)
	needsUpdate := snapshot.NeedsUpdate && isSoftwareUpdateNewer(latestVersion, currentVersion)
	forceUpdate := needsUpdate && snapshot.ForceUpdate

	dismissed := false
	if !forceUpdate {
		dismissed = s.isDismissed(runtimeInfo.software, runtimeInfo.platform, latestVersion)
	}
	showModal := needsUpdate && (forceUpdate || !dismissed)

	status := models.SoftwareVersionUpdateFrontendStatus{
		Software:           snapshot.Software,
		Platform:           snapshot.Platform,
		CurrentVersion:     currentVersion,
		LatestVersion:      latestVersion,
		DownloadURL:        snapshot.DownloadURL,
		FileType:           snapshot.FileType,
		Changelog:          snapshot.Changelog,
		NeedsUpdate:        needsUpdate,
		ForceUpdate:        forceUpdate,
		Dismissed:          dismissed,
		ShowModal:          showModal,
		IndicatorVisible:   needsUpdate && (forceUpdate || !dismissed),
		BlockingProxyStart: needsUpdate && forceUpdate,
		Status:             snapshot.Status,
		LastError:          snapshot.LastError,
		CheckedAtUnix:      snapshot.CheckedAt.Unix(),
		FirstSeenAtUnix:    snapshot.FirstSeenAt.Unix(),
		LastSeenAtUnix:     snapshot.LastSeenAt.Unix(),
	}
	return status
}

func (s *SoftwareUpdateService) DismissCurrentUpdate() (models.SoftwareVersionUpdateFrontendStatus, error) {
	runtimeInfo, err := resolveSoftwareUpdateRuntimeInfo()
	if err != nil {
		return models.SoftwareVersionUpdateFrontendStatus{}, err
	}

	snapshot, err := s.store.GetSnapshot(runtimeInfo.software, runtimeInfo.platform)
	if err != nil {
		return models.SoftwareVersionUpdateFrontendStatus{}, err
	}
	if snapshot == nil || !snapshot.NeedsUpdate {
		return s.GetFrontendStatus(), nil
	}
	if snapshot.ForceUpdate {
		return models.SoftwareVersionUpdateFrontendStatus{}, fmt.Errorf("forced update cannot be dismissed")
	}
	if strings.TrimSpace(snapshot.LatestVersion) == "" {
		return models.SoftwareVersionUpdateFrontendStatus{}, fmt.Errorf("latest version is empty")
	}

	s.setDismissed(runtimeInfo.software, runtimeInfo.platform, snapshot.LatestVersion)

	return s.GetFrontendStatus(), nil
}

func (s *SoftwareUpdateService) persistFailureState(runtimeInfo softwareUpdateRuntimeInfo, refreshErr error) error {
	now := s.now()
	snapshot, err := s.store.GetSnapshot(runtimeInfo.software, runtimeInfo.platform)
	if err != nil {
		return err
	}

	next := models.SoftwareVersionUpdateSnapshot{
		Software:       runtimeInfo.software,
		Platform:       runtimeInfo.platform,
		CurrentVersion: runtimeInfo.version,
		Status:         "error",
		LastError:      refreshErr.Error(),
		CheckedAt:      now,
	}

	if snapshot != nil {
		next.LatestVersion = snapshot.LatestVersion
		next.DownloadURL = snapshot.DownloadURL
		next.FileType = snapshot.FileType
		next.Changelog = snapshot.Changelog
		next.NeedsUpdate = snapshot.NeedsUpdate
		next.ForceUpdate = snapshot.ForceUpdate
		next.FirstSeenAt = snapshot.FirstSeenAt
		next.LastSeenAt = snapshot.LastSeenAt
		if next.CurrentVersion == "" {
			next.CurrentVersion = snapshot.CurrentVersion
		}
	} else {
		next.LatestVersion = runtimeInfo.version
	}
	if !isSoftwareUpdateNewer(next.LatestVersion, next.CurrentVersion) {
		next.NeedsUpdate = false
		next.ForceUpdate = false
		next.FirstSeenAt = time.Time{}
		next.LastSeenAt = time.Time{}
	}

	if err := s.store.UpsertSnapshot(next); err != nil {
		return err
	}

	return refreshErr
}

func (s *SoftwareUpdateService) fetchRemoteUpdate(ctx context.Context, runtimeInfo softwareUpdateRuntimeInfo) (*softwareUpdateRemotePayload, error) {
	endpoint, err := buildSoftwareUpdateCheckURL(runtimeInfo)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call software update API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read software update response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("software update API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rawPayload softwareUpdateRemotePayload
	if err := json.Unmarshal(body, &rawPayload); err == nil && (rawPayload.LatestVersion != "" || rawPayload.CurrentVersion != "" || rawPayload.SoftwareName != "") {
		normalizeSoftwareUpdateRemotePayload(&rawPayload, runtimeInfo)
		return &rawPayload, nil
	}

	var envelope softwareUpdateEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse software update response: %w", err)
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("software update API returned code %d: %s", envelope.Code, envelope.Msg)
	}

	payload := envelope.Data
	normalizeSoftwareUpdateRemotePayload(&payload, runtimeInfo)
	return &payload, nil
}

func normalizeSoftwareUpdateRemotePayload(payload *softwareUpdateRemotePayload, runtimeInfo softwareUpdateRuntimeInfo) {
	if payload == nil {
		return
	}

	payload.SoftwareName = coalesceString(strings.TrimSpace(payload.SoftwareName), runtimeInfo.software)
	payload.Platform = coalesceString(strings.TrimSpace(payload.Platform), runtimeInfo.platform)
	payload.CurrentVersion = coalesceString(normalizeVersionString(payload.CurrentVersion), runtimeInfo.version)
	payload.LatestVersion = coalesceString(normalizeVersionString(payload.LatestVersion), payload.CurrentVersion)
}

func resolveSoftwareUpdateRuntimeInfo() (softwareUpdateRuntimeInfo, error) {
	software := resolveSoftwareUpdateSoftwareName()
	currentVersion := normalizeVersionString(strings.TrimSpace(appVersion.Version))
	if currentVersion == "" {
		currentVersion = normalizeVersionString(strings.TrimSpace(os.Getenv("ALIANG_VERSION")))
	}
	if currentVersion == "" {
		return softwareUpdateRuntimeInfo{}, fmt.Errorf("software version is not available for update checks")
	}

	return softwareUpdateRuntimeInfo{
		software: software,
		platform: runtime.GOOS,
		version:  currentVersion,
	}, nil
}

func resolveSoftwareUpdateSoftwareName() string {
	if override := strings.TrimSpace(os.Getenv("ALIANG_UPDATE_SOFTWARE")); override != "" {
		return override
	}
	return defaultSoftwareUpdateName
}

func buildSoftwareUpdateCheckURL(runtimeInfo softwareUpdateRuntimeInfo) (string, error) {
	globalCfg := config.GetGlobalConfig()
	if globalCfg == nil {
		return "", fmt.Errorf("global config is not initialized")
	}

	baseURL := strings.TrimSpace(globalCfg.APIBaseURL())
	if baseURL == "" {
		baseURL = "https://www.aliang.one"
	}
	if baseURL == "" {
		return "", fmt.Errorf("api base url is not configured")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid api base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid api base url: %s", baseURL)
	}

	query := url.Values{}
	query.Set("platform", runtimeInfo.platform)
	query.Set("version", runtimeInfo.version)
	// if strings.TrimSpace(os.Getenv("ALIANG_UPDATE_SOFTWARE")) != "" {
	// 	query.Set("software", runtimeInfo.software)
	// }

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/public/downloads/check"
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func normalizeVersionString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "v") && versionPattern.MatchString(trimmed) {
		return trimmed
	}
	if versionPattern.MatchString("v" + trimmed) {
		return "v" + trimmed
	}
	return ""
}

func isSoftwareUpdateNewer(latestVersion string, currentVersion string) bool {
	latest := normalizeVersionString(latestVersion)
	current := normalizeVersionString(currentVersion)
	if latest == "" || current == "" {
		return false
	}

	latestParts := strings.Split(strings.TrimPrefix(latest, "v"), ".")
	currentParts := strings.Split(strings.TrimPrefix(current, "v"), ".")
	if len(latestParts) != 3 || len(currentParts) != 3 {
		return false
	}

	for i := 0; i < 3; i++ {
		latestValue, latestErr := strconv.Atoi(latestParts[i])
		currentValue, currentErr := strconv.Atoi(currentParts[i])
		if latestErr != nil || currentErr != nil {
			return false
		}
		if latestValue > currentValue {
			return true
		}
		if latestValue < currentValue {
			return false
		}
	}
	return false
}

func softwareUpdateStatusLabel(needsUpdate bool, forceUpdate bool, lastError string) string {
	if strings.TrimSpace(lastError) != "" {
		return "error"
	}
	if forceUpdate && needsUpdate {
		return "force_update"
	}
	if needsUpdate {
		return "update_available"
	}
	return "up_to_date"
}

func coalesceString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func dismissalKey(software, platform, latestVersion string) string {
	return software + ":" + platform + ":" + latestVersion
}

func (s *SoftwareUpdateService) isDismissed(software, platform, latestVersion string) bool {
	s.dismissMu.RLock()
	defer s.dismissMu.RUnlock()
	return s.dismissals[dismissalKey(software, platform, latestVersion)]
}

func (s *SoftwareUpdateService) setDismissed(software, platform, latestVersion string) {
	s.dismissMu.Lock()
	defer s.dismissMu.Unlock()
	s.dismissals[dismissalKey(software, platform, latestVersion)] = true
}
