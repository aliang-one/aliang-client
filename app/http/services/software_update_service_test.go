package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/storage"
	appVersion "aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/processor/config"
)

func TestSoftwareUpdateService_TracksDismissalsAndForceUpdates(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	originalVersion := appVersion.Version
	appVersion.Version = "v1.0.0"
	t.Cleanup(func() {
		appVersion.Version = originalVersion
	})

	t.Setenv("ALIANG_UPDATE_SOFTWARE", "aliang-gateway")

	responseBody := `{
	  "software_name": "aliang-gateway",
	  "platform": "darwin",
	  "current_version": "v1.0.0",
	  "latest_version": "v1.1.0",
	  "download_url": "https://example.com/app-v1.1.0.dmg",
	  "file_type": "dmg",
	  "force_update": false,
	  "needs_update": true,
	  "changelog": "Bug fixes"
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/public/downloads/check" {
			t.Fatalf("unexpected update check path: %s", got)
		}
		if got := r.URL.Query().Get("platform"); got != "darwin" {
			t.Fatalf("unexpected platform query: %s", got)
		}
		if got := r.URL.Query().Get("version"); got != "v1.0.0" {
			t.Fatalf("unexpected version query: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{
			APIServer: server.URL,
		},
	})

	store, err := storage.NewSoftwareVersionUpdateStoreWithDBPath(t.TempDir() + "/updates.db")
	if err != nil {
		t.Fatalf("create version update store failed: %v", err)
	}

	currentTime := time.Now().UTC()
	service := NewSoftwareUpdateServiceWithStore(store)
	service.client = server.Client()
	service.now = func() time.Time { return currentTime }

	if err := service.RefreshNow(context.Background()); err != nil {
		t.Fatalf("initial refresh failed: %v", err)
	}

	status := service.GetFrontendStatus()
	if !status.NeedsUpdate || status.ForceUpdate {
		t.Fatalf("expected non-force update status, got %+v", status)
	}
	if !status.ShowModal || !status.IndicatorVisible {
		t.Fatalf("expected modal and indicator for new version, got %+v", status)
	}

	dismissedStatus, err := service.DismissCurrentUpdate()
	if err != nil {
		t.Fatalf("dismiss current update failed: %v", err)
	}
	if !dismissedStatus.Dismissed || dismissedStatus.ShowModal {
		t.Fatalf("expected dismissed update to stop auto modal, got %+v", dismissedStatus)
	}
	if dismissedStatus.IndicatorVisible {
		t.Fatalf("expected dismissed update to hide indicator, got %+v", dismissedStatus)
	}

	currentTime = currentTime.Add(10 * time.Minute)
	if err := service.RefreshNow(context.Background()); err != nil {
		t.Fatalf("refresh same version failed: %v", err)
	}
	sameVersionStatus := service.GetFrontendStatus()
	if !sameVersionStatus.Dismissed || sameVersionStatus.ShowModal {
		t.Fatalf("expected dismissal to persist for same latest version, got %+v", sameVersionStatus)
	}

	responseBody = `{
	  "software_name": "aliang-gateway",
	  "platform": "darwin",
	  "current_version": "v1.0.0",
	  "latest_version": "v1.2.0",
	  "download_url": "https://example.com/app-v1.2.0.dmg",
	  "file_type": "dmg",
	  "force_update": false,
	  "needs_update": true,
	  "changelog": "Feature release"
	}`
	currentTime = currentTime.Add(10 * time.Minute)
	if err := service.RefreshNow(context.Background()); err != nil {
		t.Fatalf("refresh newer non-force update failed: %v", err)
	}
	newerVersionStatus := service.GetFrontendStatus()
	if newerVersionStatus.Dismissed || !newerVersionStatus.ShowModal || newerVersionStatus.LatestVersion != "v1.2.0" {
		t.Fatalf("expected a newer version to reopen modal, got %+v", newerVersionStatus)
	}

	responseBody = `{
	  "software_name": "aliang-gateway",
	  "platform": "darwin",
	  "current_version": "v1.0.0",
	  "latest_version": "v2.0.0",
	  "download_url": "https://example.com/app-v2.0.0.dmg",
	  "file_type": "dmg",
	  "force_update": true,
	  "needs_update": true,
	  "changelog": "Security patch"
	}`
	currentTime = currentTime.Add(10 * time.Minute)
	if err := service.RefreshNow(context.Background()); err != nil {
		t.Fatalf("refresh force update failed: %v", err)
	}

	forceStatus := service.GetFrontendStatus()
	if !forceStatus.ForceUpdate || !forceStatus.BlockingProxyStart || !forceStatus.ShowModal {
		t.Fatalf("expected force update to block proxy startup, got %+v", forceStatus)
	}
	if _, err := service.DismissCurrentUpdate(); err == nil {
		t.Fatal("expected force update dismissal to be rejected")
	}

	responseBody = `{
	  "software_name": "aliang-gateway",
	  "platform": "darwin",
	  "current_version": "v1.0.0",
	  "latest_version": "v1.2.0",
	  "download_url": "https://example.com/app-v1.2.0.dmg",
	  "file_type": "dmg",
	  "force_update": true,
	  "needs_update": true,
	  "changelog": "Security patch"
	}`
	currentTime = currentTime.Add(10 * time.Minute)
	if err := service.RefreshNow(context.Background()); err != nil {
		t.Fatalf("refresh previously dismissed version as force update failed: %v", err)
	}

	dismissedForceStatus := service.GetFrontendStatus()
	if dismissedForceStatus.Dismissed {
		t.Fatalf("force update should ignore previous dismissal, got %+v", dismissedForceStatus)
	}
	if !dismissedForceStatus.ForceUpdate || !dismissedForceStatus.ShowModal || !dismissedForceStatus.IndicatorVisible {
		t.Fatalf("expected previously dismissed force update to show modal and indicator, got %+v", dismissedForceStatus)
	}
}

func TestSoftwareUpdateService_ClearsCachedUpdateWhenRuntimeVersionCaughtUp(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	originalVersion := appVersion.Version
	appVersion.Version = "v1.0.0"
	t.Cleanup(func() {
		appVersion.Version = originalVersion
	})

	t.Setenv("ALIANG_UPDATE_SOFTWARE", "aliang-gateway")

	responseStatus := http.StatusOK
	responseBody := `{
	  "software_name": "aliang-gateway",
	  "platform": "darwin",
	  "current_version": "v1.0.0",
	  "latest_version": "v1.1.0",
	  "download_url": "https://example.com/app-v1.1.0.dmg",
	  "file_type": "dmg",
	  "force_update": false,
	  "needs_update": true,
	  "changelog": "Bug fixes"
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(responseStatus)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{
			APIServer: server.URL,
		},
	})

	store, err := storage.NewSoftwareVersionUpdateStoreWithDBPath(t.TempDir() + "/updates.db")
	if err != nil {
		t.Fatalf("create version update store failed: %v", err)
	}

	currentTime := time.Now().UTC()
	service := NewSoftwareUpdateServiceWithStore(store)
	service.client = server.Client()
	service.now = func() time.Time { return currentTime }

	if err := service.RefreshNow(context.Background()); err != nil {
		t.Fatalf("initial refresh failed: %v", err)
	}
	initialStatus := service.GetFrontendStatus()
	if !initialStatus.NeedsUpdate || !initialStatus.ShowModal {
		t.Fatalf("expected initial update notice, got %+v", initialStatus)
	}

	appVersion.Version = "v1.1.0"
	responseStatus = http.StatusNotFound
	responseBody = "404 page not found"
	currentTime = currentTime.Add(10 * time.Minute)
	if err := service.RefreshNow(context.Background()); err == nil {
		t.Fatal("expected refresh failure after remote 404")
	}

	status := service.GetFrontendStatus()
	if status.CurrentVersion != "v1.1.0" || status.LatestVersion != "v1.1.0" {
		t.Fatalf("expected caught-up versions, got %+v", status)
	}
	if status.NeedsUpdate || status.ForceUpdate || status.ShowModal || status.IndicatorVisible || status.BlockingProxyStart {
		t.Fatalf("expected cached update prompt to be cleared after version caught up, got %+v", status)
	}
	if status.Status != "error" || status.LastError == "" {
		t.Fatalf("expected refresh error to remain visible for diagnostics, got %+v", status)
	}
}
