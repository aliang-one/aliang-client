package services

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/runtime"
)

type fakeRunModeSnapshotStore struct {
	saveErr        error
	latest         *models.SoftwareEffectiveConfigSnapshot
	savedSnapshots []models.SoftwareEffectiveConfigSnapshot
}

type fakeWintunDependencyController struct {
	status WintunDependencyStatus
}

func (f *fakeWintunDependencyController) Status() WintunDependencyStatus {
	return f.status
}

func (f *fakeWintunDependencyController) Refresh() WintunDependencyStatus {
	return f.status
}

func (f *fakeWintunDependencyController) StartInstall() WintunDependencyStatus {
	if !f.status.Available {
		f.status.Installing = true
		f.status.State = "queued"
	}
	return f.status
}

func (s *fakeRunModeSnapshotStore) SaveEffectiveConfigSnapshot(snapshot models.SoftwareEffectiveConfigSnapshot) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedSnapshots = append(s.savedSnapshots, snapshot)
	return nil
}

func (s *fakeRunModeSnapshotStore) GetLatestEffectiveConfigSnapshotBySoftwareAndName(software string, configName string) (*models.SoftwareEffectiveConfigSnapshot, error) {
	if s.latest == nil {
		return nil, errors.New("not found")
	}
	copyValue := *s.latest
	return &copyValue, nil
}

func seedActiveIngressSnapshot(t *testing.T, mode string) {
	t.Helper()
	config.ResetRoutingApplyStoreForTest()
	raw := []byte(fmt.Sprintf(`{
"version": 1,
"ingress": {"mode": %q},
"egress": {
  "direct": {"enabled": true},
  "toAliang": {"enabled": true},
  "toSocks": {"enabled": true, "upstream": {"type": "socks"}}
},
"routing": {"rules": []}
}`, mode))
	if _, err := config.GetRoutingApplyStore().Apply(raw, func(canonical *config.CanonicalRoutingSchema) (any, error) {
		return routing.CompileRuntimeSnapshot(canonical)
	}); err != nil {
		t.Fatalf("seed routing snapshot failed: %v", err)
	}
}

func resetRunServiceHooksForTest() {
	activeIngressModeResolver = activeIngressModeFromSnapshot
	applyIngressModeUpdater = applyIngressModeToSnapshot
	tunStartRunner = defaultStartTUN
	httpStartRunner = func() {}
	httpStopRunner = func() {}
	tunStopRunner = func() {}
	httpProxyIsRunningProbe = func() bool { return false }
	runModeStoreFactory = func() runModeSnapshotStore { return storage.NewSoftwareConfigStore() }
	aliangLinkStatusResolver = resolveAliangLinkStatus
	softwareUpdateStatusResolver = func() models.SoftwareVersionUpdateFrontendStatus {
		return models.SoftwareVersionUpdateFrontendStatus{}
	}
	setSharedWintunDependencyControllerForTest(nil)
}

func TestRunServiceStartServiceAlreadyRunning(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeHTTP))
	runtime.ResetGlobalStartupStateForTest()
	runtime.GetStartupState().SetStatus(runtime.READY)

	runService := NewRunService()
	runService.SetRunning(true)

	result := runService.StartService()

	if status, ok := result["status"].(string); !ok || status != "already_running" {
		t.Fatalf("expected status=already_running, got %#v", result["status"])
	}

	if message, ok := result["message"].(string); !ok || message == "" {
		t.Fatalf("expected non-empty message, got %#v", result["message"])
	}
}

func TestRunServiceCharacterization_StartServiceActivationGuard(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeHTTP))
	runtime.ResetGlobalStartupStateForTest()
	runtime.GetStartupState().SetStatus(runtime.UNCONFIGURED)

	runService := NewRunService()
	result := runService.StartService()

	if status, ok := result["status"].(string); !ok || status != "failed" {
		t.Fatalf("expected status=failed, got %#v", result["status"])
	}
	if errCode, ok := result["error"].(string); !ok || errCode != "activation_required" {
		t.Fatalf("expected error=activation_required, got %#v", result["error"])
	}
	if runService.IsRunning() {
		t.Fatalf("expected service to remain not running when activation guard rejects start")
	}
}

func TestRunServiceCharacterization_StopServiceWhenNotRunning(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeHTTP))
	runService := NewRunService()
	runService.SetCurrentMode(string(models.ModeHTTP))
	runService.SetRunning(false)

	result := runService.StopService()

	if status, ok := result["status"].(string); !ok || status != "failed" {
		t.Fatalf("expected status=failed, got %#v", result["status"])
	}
	if errCode, ok := result["error"].(string); !ok || errCode != "not_running" {
		t.Fatalf("expected error=not_running, got %#v", result["error"])
	}
}

func TestRunServiceCharacterization_GetStatusDescriptions(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		running         bool
		wantStatus      string
		wantDescription string
	}{
		{
			name:            "http running",
			mode:            string(models.ModeHTTP),
			running:         true,
			wantStatus:      "Regular Mode is running",
			wantDescription: "HTTP CONNECT proxy is running on port 56432.",
		},
		{
			name:            "http idle",
			mode:            string(models.ModeHTTP),
			running:         false,
			wantStatus:      "Regular Mode is selected, service not running",
			wantDescription: "Regular Mode is ready. Click start when you want to enable local proxying.",
		},
		{
			name:            "tun running",
			mode:            string(models.ModeTUN),
			running:         true,
			wantStatus:      "Deep Mode is running",
			wantDescription: "System traffic is being routed through the TUN interface.",
		},
		{
			name:            "tun idle",
			mode:            string(models.ModeTUN),
			running:         false,
			wantStatus:      "Deep Mode is selected, service not running",
			wantDescription: "Deep Mode is ready. Click start when you want to enable system-wide proxying.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer resetRunServiceHooksForTest()
			seedActiveIngressSnapshot(t, tt.mode)
			runService := NewRunService()
			runService.SetCurrentMode(tt.mode)
			runService.SetRunning(tt.running)

			status := runService.GetStatus()

			if got, ok := status["status"].(string); !ok || got != tt.wantStatus {
				t.Fatalf("status text mismatch: got=%#v want=%q", status["status"], tt.wantStatus)
			}
			if got, ok := status["description"].(string); !ok || got != tt.wantDescription {
				t.Fatalf("description mismatch: got=%#v want=%q", status["description"], tt.wantDescription)
			}
		})
	}
}

func TestRunServiceCharacterization_SwitchModeGuards(t *testing.T) {
	t.Run("invalid mode rejected", func(t *testing.T) {
		defer resetRunServiceHooksForTest()
		seedActiveIngressSnapshot(t, string(models.ModeHTTP))
		runService := NewRunService()
		result := runService.SwitchMode("udp")

		if status, ok := result["status"].(string); !ok || status != "failed" {
			t.Fatalf("expected status=failed, got %#v", result["status"])
		}
		if errCode, ok := result["error"].(string); !ok || errCode != "invalid_mode" {
			t.Fatalf("expected error=invalid_mode, got %#v", result["error"])
		}
	})

	t.Run("same mode while running returns unchanged", func(t *testing.T) {
		defer resetRunServiceHooksForTest()
		seedActiveIngressSnapshot(t, string(models.ModeTUN))
		runService := NewRunService()
		runService.SetCurrentMode(string(models.ModeTUN))
		runService.SetRunning(true)

		result := runService.SwitchMode(string(models.ModeTUN))

		if status, ok := result["status"].(string); !ok || status != "unchanged" {
			t.Fatalf("expected status=unchanged, got %#v", result["status"])
		}
		if currentMode, ok := result["current_mode"].(string); !ok || currentMode != string(models.ModeTUN) {
			t.Fatalf("expected current_mode=tun, got %#v", result["current_mode"])
		}
	})

	t.Run("switch to tun while idle does not auto-start", func(t *testing.T) {
		defer resetRunServiceHooksForTest()
		seedActiveIngressSnapshot(t, string(models.ModeHTTP))
		runModeStoreFactory = func() runModeSnapshotStore { return &fakeRunModeSnapshotStore{} }
		runService := NewRunService()
		runService.SetCurrentMode(string(models.ModeHTTP))
		runService.SetRunning(false)

		result := runService.SwitchMode(string(models.ModeTUN))

		if status, ok := result["status"].(string); !ok || status != "switched" {
			t.Fatalf("expected status=switched, got %#v", result["status"])
		}
		if runService.GetStatus()["is_running"].(bool) {
			t.Fatalf("expected switch-to-tun while idle to keep service stopped")
		}
	})

	t.Run("switch to tun is blocked while wintun is missing", func(t *testing.T) {
		defer resetRunServiceHooksForTest()
		seedActiveIngressSnapshot(t, string(models.ModeHTTP))
		setSharedWintunDependencyControllerForTest(&fakeWintunDependencyController{
			status: WintunDependencyStatus{
				Supported:  true,
				Required:   true,
				Available:  false,
				Installing: false,
				State:      "missing",
				Message:    "Wintun dependency is missing.",
			},
		})

		runService := NewRunService()
		runService.SetCurrentMode(string(models.ModeHTTP))
		runService.SetRunning(false)

		result := runService.SwitchMode(string(models.ModeTUN))
		if status, ok := result["status"].(string); !ok || status != "failed" {
			t.Fatalf("expected status=failed, got %#v", result["status"])
		}
		if errCode, ok := result["error"].(string); !ok || errCode != "wintun_required" {
			t.Fatalf("expected error=wintun_required, got %#v", result["error"])
		}
	})

	t.Run("switch initializes routing snapshot when missing", func(t *testing.T) {
		defer resetRunServiceHooksForTest()
		config.ResetRoutingApplyStoreForTest()
		runModeStoreFactory = func() runModeSnapshotStore { return &fakeRunModeSnapshotStore{} }

		runService := NewRunService()
		runService.SetCurrentMode(string(models.ModeHTTP))
		runService.SetRunning(false)

		result := runService.SwitchMode(string(models.ModeTUN))

		if status, ok := result["status"].(string); !ok || status != "switched" {
			t.Fatalf("expected status=switched, got %#v", result["status"])
		}
		if got := runService.GetCurrentMode(); got != string(models.ModeTUN) {
			t.Fatalf("current mode mismatch: got=%q want=%q", got, models.ModeTUN)
		}
		canonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
		if canonical == nil {
			t.Fatal("expected canonical routing schema to be initialized")
		}
		if canonical.Ingress.Mode != string(models.ModeTUN) {
			t.Fatalf("expected canonical ingress.mode=tun, got %q", canonical.Ingress.Mode)
		}
	})
}

func TestBootstrapCanonicalRoutingSchemaHonorsDisabledCustomerProxy(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Proxy: &config.CustomerProxyConfig{
				Enable: customerBoolPtr(false),
				Type:   "socks5",
				Server: "127.0.0.1:1080",
			},
			ProxyRules: []string{"domains,google.com"},
		},
	})

	canonical := bootstrapCanonicalRoutingSchema(models.ModeTUN)
	if canonical.Egress.ToSocks.Enabled {
		t.Fatal("bootstrap toSocks.enabled = true, want false when customer.proxy.enable=false")
	}
	if len(canonical.Routing.Rules) != 1 {
		t.Fatalf("bootstrap routing rules = %d, want 1", len(canonical.Routing.Rules))
	}
	if got := canonical.Routing.Rules[0].Target; got != string(routing.SnapshotActionDirect) {
		t.Fatalf("bootstrap proxy rule target = %q, want direct", got)
	}
}

func TestRunServiceSwitchModeStopsRunningServiceWithoutAutoStart(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeHTTP))
	runModeStoreFactory = func() runModeSnapshotStore { return &fakeRunModeSnapshotStore{} }

	runtime.ResetGlobalStartupStateForTest()
	runtime.GetStartupState().SetStatus(runtime.READY)

	events := make([]string, 0, 8)
	httpStopRunner = func() {
		events = append(events, "http:stop")
	}

	runService := NewRunService()
	runService.SetCurrentMode(string(models.ModeHTTP))
	runService.SetRunning(true)

	result := runService.SwitchMode(string(models.ModeTUN))
	if status, _ := result["status"].(string); status != "switched" {
		t.Fatalf("expected switched status, got %#v", result)
	}
	if got := runService.GetCurrentMode(); got != string(models.ModeTUN) {
		t.Fatalf("current mode mismatch: got=%q want=%q", got, models.ModeTUN)
	}
	status := runService.GetStatus()
	if current, _ := status["current_mode"].(string); current != string(models.ModeTUN) {
		t.Fatalf("status current_mode mismatch: got=%q want=%q", current, models.ModeTUN)
	}
	if running, _ := status["is_running"].(bool); running {
		t.Fatalf("expected running=false after mode switch, got %#v", status["is_running"])
	}

	if len(events) != 1 {
		t.Fatalf("unexpected event count: got=%d events=%v", len(events), events)
	}
	if events[0] != "http:stop" {
		t.Fatalf("expected current running service to stop before switching, events=%v", events)
	}
}

func TestRunServiceSwitchModePersistsModeSnapshot(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeHTTP))

	store := &fakeRunModeSnapshotStore{}
	runModeStoreFactory = func() runModeSnapshotStore { return store }

	runService := NewRunService()
	runService.SetCurrentMode(string(models.ModeHTTP))
	runService.SetRunning(false)

	result := runService.SwitchMode(string(models.ModeTUN))
	if status, _ := result["status"].(string); status != "switched" {
		t.Fatalf("expected switched status, got %#v", result)
	}
	if len(store.savedSnapshots) != 1 {
		t.Fatalf("expected one persisted run mode snapshot, got %d", len(store.savedSnapshots))
	}
	if store.savedSnapshots[0].Software != runModeSnapshotSoftware || store.savedSnapshots[0].ConfigName != runModeSnapshotName {
		t.Fatalf("unexpected persisted snapshot metadata: %+v", store.savedSnapshots[0])
	}
	if store.savedSnapshots[0].SnapshotJSON != `{"mode":"tun"}` {
		t.Fatalf("unexpected persisted run mode payload: %s", store.savedSnapshots[0].SnapshotJSON)
	}
}

func TestRunServiceStartServiceBlocksMissingWintun(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeTUN))
	runtime.ResetGlobalStartupStateForTest()
	runtime.GetStartupState().SetStatus(runtime.READY)
	setSharedWintunDependencyControllerForTest(&fakeWintunDependencyController{
		status: WintunDependencyStatus{
			Supported:  true,
			Required:   true,
			Available:  false,
			Installing: true,
			State:      "installing",
			Message:    "Installing Wintun dependency.",
		},
	})

	runService := NewRunService()
	runService.SetCurrentMode(string(models.ModeTUN))
	runService.SetRunning(false)

	result := runService.StartService()
	if status, ok := result["status"].(string); !ok || status != "failed" {
		t.Fatalf("expected status=failed, got %#v", result["status"])
	}
	if errCode, ok := result["error"].(string); !ok || errCode != "wintun_installing" {
		t.Fatalf("expected error=wintun_installing, got %#v", result["error"])
	}
}

func TestRunServiceStartServiceBlocksForcedSoftwareUpdate(t *testing.T) {
	defer resetRunServiceHooksForTest()
	seedActiveIngressSnapshot(t, string(models.ModeHTTP))
	runtime.ResetGlobalStartupStateForTest()
	runtime.GetStartupState().SetStatus(runtime.READY)
	softwareUpdateStatusResolver = func() models.SoftwareVersionUpdateFrontendStatus {
		return models.SoftwareVersionUpdateFrontendStatus{
			NeedsUpdate:        true,
			ForceUpdate:        true,
			LatestVersion:      "v2.0.0",
			BlockingProxyStart: true,
		}
	}

	runService := NewRunService()
	result := runService.StartService()

	if status, ok := result["status"].(string); !ok || status != "failed" {
		t.Fatalf("expected status=failed, got %#v", result["status"])
	}
	if errCode, ok := result["error"].(string); !ok || errCode != "force_update_required" {
		t.Fatalf("expected error=force_update_required, got %#v", result["error"])
	}
	if runService.IsRunning() {
		t.Fatal("expected service to remain stopped when forced update blocks startup")
	}
}

func TestRunServiceNewRunServiceRestoresPersistedMode(t *testing.T) {
	defer resetRunServiceHooksForTest()
	config.ResetRoutingApplyStoreForTest()

	store := &fakeRunModeSnapshotStore{
		latest: &models.SoftwareEffectiveConfigSnapshot{
			Software:     runModeSnapshotSoftware,
			ConfigName:   runModeSnapshotName,
			ConfigFormat: models.ConfigFormatJSON,
			SnapshotJSON: `{"mode":"tun"}`,
		},
	}
	runModeStoreFactory = func() runModeSnapshotStore { return store }

	runService := NewRunService()
	if got := runService.GetCurrentMode(); got != string(models.ModeTUN) {
		t.Fatalf("restored current mode mismatch: got=%q want=%q", got, models.ModeTUN)
	}
}

func TestRunServiceGetAliangLinkStatus(t *testing.T) {
	defer resetRunServiceHooksForTest()

	calls := make([]bool, 0, 2)
	aliangLinkStatusResolver = func(ctx context.Context, probe bool) map[string]interface{} {
		calls = append(calls, probe)
		return map[string]interface{}{
			"state":      "connected",
			"latency_ms": int64(123),
		}
	}

	runService := NewRunService()

	snapshot := runService.GetAliangLinkStatus(context.Background(), false)
	if got := snapshot["state"]; got != "connected" {
		t.Fatalf("unexpected snapshot state: %#v", got)
	}

	probed := runService.GetAliangLinkStatus(context.Background(), true)
	if got := probed["latency_ms"]; got != int64(123) {
		t.Fatalf("unexpected probed latency: %#v", got)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 resolver calls, got %d", len(calls))
	}
	if calls[0] {
		t.Fatalf("expected first resolver call to be snapshot mode")
	}
	if !calls[1] {
		t.Fatalf("expected second resolver call to be probe mode")
	}
}

// TestStopIngressIfActiveStopsDespiteRunningFlagDesync reproduces the
// auth-expiry bug observed in aliang_core.log: runService.isRunning had
// desynced to false (after a mode switch / restart / rollback) while the HTTP
// proxy listener (56432) was still up, so handleAuthExpired's
// "if IsRunning()" guard skipped the stop and 56432 kept serving a dead token.
// The fix must stop based on the REAL listener state, not the flag.
func TestStopIngressIfActiveStopsDespiteRunningFlagDesync(t *testing.T) {
	defer resetRunServiceHooksForTest()

	var httpStops, tunStops int
	httpStopRunner = func() { httpStops++ }
	tunStopRunner = func() { tunStops++ }
	// The real listener is up even though the flag says otherwise.
	httpProxyIsRunningProbe = func() bool { return true }

	rs := NewRunService()
	rs.SetCurrentMode(string(models.ModeHTTP))
	rs.SetRunning(false) // desynced: flag=false, listener=true

	stopped := rs.StopIngressIfActive()

	if !stopped {
		t.Fatal("expected ingress torn down despite isRunning=false (listener still up)")
	}
	if httpStops != 1 {
		t.Fatalf("expected httpStopRunner called once in http mode, got %d", httpStops)
	}
	if tunStops != 0 {
		t.Fatalf("expected tunStopRunner NOT called in http mode, got %d", tunStops)
	}
	if rs.IsRunning() {
		t.Fatal("expected isRunning to remain false after stop")
	}
}

func TestStopIngressIfActiveUsesTunStopperInTunModeEvenWhenDesynced(t *testing.T) {
	defer resetRunServiceHooksForTest()

	var httpStops, tunStops int
	httpStopRunner = func() { httpStops++ }
	tunStopRunner = func() { tunStops++ }
	httpProxyIsRunningProbe = func() bool { return true }

	rs := NewRunService()
	rs.SetCurrentMode(string(models.ModeTUN))
	rs.SetRunning(false) // desynced

	if !rs.StopIngressIfActive() {
		t.Fatal("expected ingress torn down in tun mode despite isRunning=false")
	}
	if tunStops != 1 {
		t.Fatalf("expected tunStopRunner called once in tun mode, got %d", tunStops)
	}
	if httpStops != 0 {
		t.Fatalf("expected httpStopRunner NOT called directly in tun mode, got %d", httpStops)
	}
}

func TestStopIngressIfActiveNoopWhenNothingRunning(t *testing.T) {
	defer resetRunServiceHooksForTest()

	httpStopRunner = func() { t.Fatal("httpStopRunner must not be called when nothing is running") }
	tunStopRunner = func() { t.Fatal("tunStopRunner must not be called when nothing is running") }
	httpProxyIsRunningProbe = func() bool { return false }

	rs := NewRunService()
	rs.SetCurrentMode(string(models.ModeHTTP))
	rs.SetRunning(false)

	if rs.StopIngressIfActive() {
		t.Fatal("expected no stop when neither flag nor listener is active")
	}
}

// TestStopIngressIfActiveStillStopsOnNormalRunningCase guards the non-desync
// path so the fix does not regress the ordinary expiry-while-running case.
func TestStopIngressIfActiveStillStopsOnNormalRunningCase(t *testing.T) {
	defer resetRunServiceHooksForTest()

	var httpStops int
	httpStopRunner = func() { httpStops++ }
	httpProxyIsRunningProbe = func() bool { return false } // listener state irrelevant when flag is authoritative

	rs := NewRunService()
	rs.SetCurrentMode(string(models.ModeHTTP))
	rs.SetRunning(true)

	if !rs.StopIngressIfActive() {
		t.Fatal("expected ingress torn down when isRunning=true")
	}
	if httpStops != 1 {
		t.Fatalf("expected httpStopRunner called once, got %d", httpStops)
	}
	if rs.IsRunning() {
		t.Fatal("expected isRunning=false after stop")
	}
}

func TestStopIngressForLogoutForceStopsDesyncedTUNWithoutHTTPListener(t *testing.T) {
	defer resetRunServiceHooksForTest()

	var httpStops, tunStops int
	httpStopRunner = func() { httpStops++ }
	tunStopRunner = func() { tunStops++ }
	httpProxyIsRunningProbe = func() bool { return false }
	activeIngressModeResolver = func() (models.RunMode, bool) { return models.ModeTUN, true }

	rs := NewRunService()
	rs.SetCurrentMode(string(models.ModeHTTP)) // stale local mode
	rs.SetRunning(false)                       // stale running flag

	if mode := rs.StopIngressForLogout(); mode != models.ModeTUN {
		t.Fatalf("stopped mode = %q, want tun", mode)
	}
	if tunStops != 1 || httpStops != 0 {
		t.Fatalf("logout stoppers http=%d tun=%d, want 0/1", httpStops, tunStops)
	}
}

func TestStartServiceRechecksActivationAfterWaitingForModeLock(t *testing.T) {
	defer resetRunServiceHooksForTest()
	runtime.ResetGlobalStartupStateForTest()
	startup := runtime.GetStartupState()
	startup.SetStatus(runtime.READY)

	reachedPreLockCheck := make(chan struct{})
	softwareUpdateStatusResolver = func() models.SoftwareVersionUpdateFrontendStatus {
		close(reachedPreLockCheck)
		return models.SoftwareVersionUpdateFrontendStatus{}
	}
	var httpStarts int
	httpStartRunner = func() { httpStarts++ }

	rs := NewRunService()
	rs.SetCurrentMode(string(models.ModeHTTP))
	rs.modeChangeMutex.Lock()
	resultCh := make(chan map[string]interface{}, 1)
	go func() { resultCh <- rs.StartService() }()

	<-reachedPreLockCheck
	startup.SetStatus(runtime.UNCONFIGURED)
	rs.modeChangeMutex.Unlock()

	result := <-resultCh
	if result["error"] != "activation_required" {
		t.Fatalf("start result = %#v, want activation_required", result)
	}
	if httpStarts != 0 || rs.IsRunning() {
		t.Fatalf("logout-racing start escaped gate: starts=%d running=%t", httpStarts, rs.IsRunning())
	}
}
