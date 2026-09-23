package services

import (
	"path/filepath"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/usage"
)

// usageRuntime 持有 user-agent 进程内的用量采集/上报循环。只在
// IsUserAgentRuntime() 进程启动，保证单进程采集（避免多进程重复计数）。
var usageRuntime struct {
	mu       sync.Mutex
	started  bool
	stop     chan struct{}
	reporter *usage.Reporter
}

// StartUsageTrackerRuntime 启动采集与周期上报（幂等；非 user-agent 进程 no-op）。
func StartUsageTrackerRuntime() {
	if !IsUserAgentRuntime() {
		return
	}
	usageRuntime.mu.Lock()
	if usageRuntime.started {
		usageRuntime.mu.Unlock()
		return
	}
	// agentHome() 返回 string（agent_home.go），无法解析时为空串
	home := agentHome()
	if home == "" {
		usageRuntime.mu.Unlock()
		logger.Warn("[USAGE] start skipped: agent home unavailable")
		return
	}
	store, err := usage.OpenDefaultStore()
	if err != nil {
		usageRuntime.mu.Unlock()
		logger.Warn("[USAGE] start skipped: open store: " + err.Error())
		return
	}
	tz := usage.TZName()
	tracker := usage.NewTracker(store, []string{filepath.Join(home, ".claude", "projects")}, usageCollectionAllowed)
	reporter := usage.NewReporter(store, func() string { return GetSharedAgentService().currentDeviceID() }, tz)

	stop := make(chan struct{})
	usageRuntime.started = true
	usageRuntime.stop = stop
	usageRuntime.reporter = reporter
	usageRuntime.mu.Unlock()

	go func() {
		scanTicker := time.NewTicker(30 * time.Second)
		defer scanTicker.Stop()
		flushTicker := time.NewTicker(5 * time.Minute)
		defer flushTicker.Stop()
		lastHour := time.Now().Local().Hour()
		for {
			select {
			case <-stop:
				return
			case <-scanTicker.C:
				if err := tracker.ScanOnce(); err != nil {
					logger.Debug("[USAGE] scan: " + err.Error())
				}
				// 跨本地小时边界立即 flush（spec §5.2）
				if h := time.Now().Local().Hour(); h != lastHour {
					lastHour = h
					if usageCollectionAllowed() {
						if err := reporter.FlushDirty(currentRemoteWriterFunc()); err != nil {
							logger.Debug("[USAGE] hour-boundary flush: " + err.Error())
						}
					}
				}
			case <-flushTicker.C:
				if !usageCollectionAllowed() {
					continue
				}
				if err := reporter.FlushDirty(currentRemoteWriterFunc()); err != nil {
					logger.Debug("[USAGE] flush dirty: " + err.Error())
				}
			}
		}
	}()
	logger.Info("[USAGE] tracker started tz=" + tz)
}

// StopUsageTrackerRuntime 停止采集循环（进程退出时调用）。
func StopUsageTrackerRuntime() {
	usageRuntime.mu.Lock()
	defer usageRuntime.mu.Unlock()
	if !usageRuntime.started {
		return
	}
	close(usageRuntime.stop)
	usageRuntime.started = false
}

// usageFlushAllNow 在 WS 注册成功后补推未确认桶（registered 钩子调用）。
// dirty 集合即未确认全集：推送成功即清除，离线累积保持 dirty。
func usageFlushAllNow(write func(interface{}) error) {
	usageRuntime.mu.Lock()
	reporter := usageRuntime.reporter
	usageRuntime.mu.Unlock()
	if reporter == nil || !usageCollectionAllowed() {
		return
	}
	if err := reporter.FlushDirty(write); err != nil {
		logger.Debug("[USAGE] flush dirty after register: " + err.Error())
	}
}

// usageCollectionAllowed 是采集总开关：本地配置开启 且 agent 处于 enable 态
// （spec §5.3：disable 暂停采集与上报；离线不影响采集）。
func usageCollectionAllowed() bool {
	cfg := config.GetGlobalConfig()
	if cfg != nil && cfg.Core != nil && cfg.Core.UsageTracker != nil && !cfg.Core.UsageTracker.IsEnabled() {
		return false
	}
	return GetSharedAgentService().agentEnabled()
}

// currentRemoteWriterFunc 返回当前 WS writer（未连接返回 nil）。
// 刻意先判 nil 再返回 writer 本身——不能包一层闭包，否则断连时会拿到
// 非 nil 的包装函数，绕过 reporter 的 write==nil 检查并在调用时 panic。
func currentRemoteWriterFunc() func(interface{}) error {
	if w := GetSharedAgentService().currentRemoteWriter(); w != nil {
		return w
	}
	return nil
}
