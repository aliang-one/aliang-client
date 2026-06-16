package services

import (
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// agentProcessStartedAt is captured at package init to report the agent
// subprocess uptime. It reflects when this binary started, not when the remote
// WS session connected, which is the meaningful "agent uptime" value.
var agentProcessStartedAt = time.Now()

const (
	// cpuSamplerInterval is the sampling window for the background CPU sampler.
	// It must be shorter than the heartbeat cadence (10s) so the cached value is
	// always fresh relative to a heartbeat read, and not equal to it (which would
	// risk phase-locking). cpu.Percent blocks for this duration on each call.
	cpuSamplerInterval = 3 * time.Second
)

// Background CPU sampler state. CPU usage is a rate computed from the delta
// between two samples, and cpu.Percent blocks for the sample window, so it
// cannot be read inline on the heartbeat goroutine. A single sync.Once-guarded
// goroutine continuously samples and caches the latest overall CPU% under a
// RWMutex; every heartbeat/hello reads the cache. The sampler is process-lifetime
// (no stop channel): the agent is a long-lived subprocess and sync.Once lets WS
// reconnects reuse the same running sampler.
var (
	cpuSamplerOnce  sync.Once
	cpuSamplerMu    sync.RWMutex
	cpuSamplerValue float64
)

// agentLoadSnapshot is the device-load payload embedded as the "load" key in the
// heartbeat and hello messages. Whole-machine metrics (CPU/mem/load/disk) come
// from gopsutil; goroutines/heap are the agent process's own runtime stats.
// load_avg_* and disk_* use omitempty because they are unavailable on some
// platforms (Windows has no load average) or may fail best-effort; cpu_usage_pct
// is always present so the cloud sees the key consistently even before the first
// sample completes.
type agentLoadSnapshot struct {
	CPUUsagePct    float64 `json:"cpu_usage_pct"`
	CPUCount       int     `json:"cpu_count"`
	LoadAvg1       float64 `json:"load_avg_1,omitempty"`
	LoadAvg5       float64 `json:"load_avg_5,omitempty"`
	LoadAvg15      float64 `json:"load_avg_15,omitempty"`
	MemTotalBytes  uint64  `json:"mem_total_bytes"`
	MemUsedBytes   uint64  `json:"mem_used_bytes"`
	MemUsagePct    float64 `json:"mem_usage_pct"`
	DiskTotalBytes uint64  `json:"disk_total_bytes,omitempty"`
	DiskUsedBytes  uint64  `json:"disk_used_bytes,omitempty"`
	DiskUsagePct   float64 `json:"disk_usage_pct,omitempty"`
	AgentUptimeSec int64   `json:"agent_uptime_sec"`
	Goroutines     int     `json:"goroutines,omitempty"`
	HeapAllocBytes uint64  `json:"heap_alloc_bytes,omitempty"`
	CollectedAt    string  `json:"collected_at"`
}

// startCPUSamplerOnce lazily starts the background CPU sampler the first time it
// is called. Subsequent calls (including across WS reconnects) are no-ops.
func startCPUSamplerOnce() {
	cpuSamplerOnce.Do(func() {
		go cpuSamplerLoop()
	})
}

// cpuSamplerLoop continuously samples overall CPU usage, blocking for
// cpuSamplerInterval on each call (the call itself is the sleep) and caching the
// latest value. On error it sleeps briefly to avoid hot-spinning a persistent
// failure, then retries.
func cpuSamplerLoop() {
	for {
		percents, err := cpu.Percent(cpuSamplerInterval, false)
		if err == nil && len(percents) > 0 {
			cpuSamplerMu.Lock()
			cpuSamplerValue = percents[0]
			cpuSamplerMu.Unlock()
			continue
		}
		time.Sleep(time.Second)
	}
}

// readCachedCPUPercent returns the most recent overall CPU% sampled by the
// background goroutine. It may be 0 until the first sampling window completes.
func readCachedCPUPercent() float64 {
	cpuSamplerMu.RLock()
	defer cpuSamplerMu.RUnlock()
	return cpuSamplerValue
}

// collectAgentLoadSnapshot assembles the current device-load snapshot. It is
// best-effort: every gopsutil call that fails is skipped, so a metrics failure
// on any platform never blocks the heartbeat/hello. CPU comes from the cached
// sampler (non-blocking); mem/load/disk are instant queries. runtime.ReadMemStats
// triggers a sub-millisecond STW, negligible at the 10s heartbeat cadence.
func collectAgentLoadSnapshot() agentLoadSnapshot {
	startCPUSamplerOnce()

	snap := agentLoadSnapshot{
		CPUUsagePct:    readCachedCPUPercent(),
		AgentUptimeSec: int64(time.Since(agentProcessStartedAt).Seconds()),
		CollectedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	if cores, err := cpu.Counts(true); err == nil && cores > 0 {
		snap.CPUCount = cores
	}

	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		snap.MemTotalBytes = vm.Total
		snap.MemUsedBytes = vm.Used
		snap.MemUsagePct = vm.UsedPercent
	}

	// load.Avg has no native equivalent on Windows and returns an error there;
	// the omitempty tags drop the zero-valued fields so the JSON shape adapts.
	if avg, err := load.Avg(); err == nil && avg != nil {
		snap.LoadAvg1 = avg.Load1
		snap.LoadAvg5 = avg.Load5
		snap.LoadAvg15 = avg.Load15
	}

	// Report usage of the filesystem holding the user's home directory as the
	// "primary disk". gopsutil resolves the path to its underlying mountpoint,
	// so this works uniformly across darwin/linux/windows.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if usage, err := disk.Usage(home); err == nil && usage != nil {
			snap.DiskTotalBytes = usage.Total
			snap.DiskUsedBytes = usage.Used
			snap.DiskUsagePct = usage.UsedPercent
		}
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	snap.HeapAllocBytes = memStats.HeapAlloc
	snap.Goroutines = runtime.NumGoroutine()

	return snap
}
