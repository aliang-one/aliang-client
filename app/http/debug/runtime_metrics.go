//go:build !prod

package debug

import (
	"log"
	"os"
	"runtime"
	"time"

	auth "aliang.one/nursorgate/processor/auth"
)

// Runtime-metrics logger: a one-line snapshot of goroutine count and heap
// usage every metricsInterval, so the TUN memory-growth suspects can be told
// apart from the log alone (pprof on :6060 gives the full picture; this gives
// the trend line without opening a browser):
//
//   - goroutines growing without bound  => goroutine/task leak (relay missing a
//     write deadline, or a blocked dial). Confirm with /debug/pprof/goroutine.
//   - goroutines plateau, heap_alloc keeps climbing => gvisor/runtime retention
//     (memory returned to the GC but not to the OS). Levers: GOMEMLIMIT / GOGC /
//     GODEBUG=madvdontneed=1, gvisor buffer-pool tuning.
//   - both plateau => high-water-mark baseline from full-traffic interception;
//     tune DefaultTCPWaitTimeout / the tcpLimiter cap, not a leak.
//
// Only compiled into non-prod builds (same gate as pprof). The package must be
// blank-imported by the main package for this init to run — see
// cmd/aliang/debug_enable_nonprod.go.

const (
	defaultMetricsInterval = 60 * time.Second
	envMetricsInterval     = "ALIANG_DEBUG_METRICS_INTERVAL"
)

var metricsInterval = loadMetricsInterval()

func loadMetricsInterval() time.Duration {
	if v := os.Getenv(envMetricsInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultMetricsInterval
}

// runtimeMetricsSnapshot captures a log-friendly summary of runtime memory and
// goroutine state. Pure (no I/O) so it can be unit-tested directly.
func runtimeMetricsSnapshot() (goroutines int, heapAllocMB, heapSysMB float64) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return runtime.NumGoroutine(), bytesToMB(m.HeapAlloc), bytesToMB(m.HeapSys)
}

func bytesToMB(b uint64) float64 {
	return float64(b) / (1024 * 1024)
}

func init() {
	go runRuntimeMetricsLogger()
}

func runRuntimeMetricsLogger() {
	log.Printf("runtime-metrics: starting (interval=%s); set %s to change", metricsInterval, envMetricsInterval)
	ticker := time.NewTicker(metricsInterval)
	defer ticker.Stop()
	for range ticker.C {
		g, heapMB, sysMB := runtimeMetricsSnapshot()
		authStats := auth.GetSessionAuthority().Stats()
		log.Printf(
			"runtime-metrics: goroutines=%d heap_alloc=%.1fMB heap_sys=%.1fMB proxy_active=%d proxy_rejected=%d proxy_forced_closed=%d auth_stale_commits=%d auth_stale_side_effects=%d",
			g,
			heapMB,
			sysMB,
			authStats.ActiveProxyFlows,
			authStats.RejectedProxyAdmissions,
			authStats.ForcedProxyFlowCloses,
			authStats.StaleOperationCommits,
			authStats.StaleSideEffects,
		)
	}
}
