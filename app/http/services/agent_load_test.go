package services

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentLoadSnapshotJSONShape(t *testing.T) {
	// A fully-populated snapshot must marshal every documented key.
	populated := agentLoadSnapshot{
		CPUUsagePct:    12.5,
		CPUCount:       8,
		LoadAvg1:       1.1,
		LoadAvg5:       1.2,
		LoadAvg15:      1.3,
		MemTotalBytes:  16 * 1024 * 1024 * 1024,
		MemUsedBytes:   4 * 1024 * 1024 * 1024,
		MemUsagePct:    25.0,
		DiskTotalBytes: 500 * 1024 * 1024 * 1024,
		DiskUsedBytes:  100 * 1024 * 1024 * 1024,
		DiskUsagePct:   20.0,
		AgentUptimeSec: 123,
		Goroutines:     42,
		HeapAllocBytes: 8 * 1024 * 1024,
		CollectedAt:    "2026-06-16T00:00:00Z",
	}
	raw, err := json.Marshal(populated)
	require.NoError(t, err)

	var asMap map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &asMap))

	for _, key := range []string{
		"cpu_usage_pct", "cpu_count",
		"load_avg_1", "load_avg_5", "load_avg_15",
		"mem_total_bytes", "mem_used_bytes", "mem_usage_pct",
		"disk_total_bytes", "disk_used_bytes", "disk_usage_pct",
		"agent_uptime_sec", "goroutines", "heap_alloc_bytes", "collected_at",
	} {
		assert.Contains(t, asMap, key, "populated snapshot should serialize %q", key)
	}

	// A zero-value snapshot must drop the omitempty fields (platform-dependent
	// metrics such as load average on Windows) while keeping the always-present
	// keys so the cloud observes a stable shape.
	zeroRaw, err := json.Marshal(agentLoadSnapshot{})
	require.NoError(t, err)
	var zeroMap map[string]interface{}
	require.NoError(t, json.Unmarshal(zeroRaw, &zeroMap))

	for _, key := range []string{"cpu_usage_pct", "cpu_count", "mem_total_bytes", "mem_used_bytes", "mem_usage_pct", "agent_uptime_sec", "collected_at"} {
		assert.Contains(t, zeroMap, key, "zero snapshot should still serialize %q (no omitempty)", key)
	}
	for _, key := range []string{"load_avg_1", "load_avg_5", "load_avg_15", "disk_total_bytes", "disk_used_bytes", "disk_usage_pct", "goroutines", "heap_alloc_bytes"} {
		assert.NotContains(t, zeroMap, key, "zero snapshot should omit %q (omitempty)", key)
	}
}

func TestCollectAgentLoadSnapshotNonBlocking(t *testing.T) {
	// collectAgentLoadSnapshot must read the cached CPU% rather than calling the
	// blocking cpu.Percent inline; two back-to-back calls must finish well under a
	// single sampling window.
	start := time.Now()
	first := collectAgentLoadSnapshot()
	second := collectAgentLoadSnapshot()
	elapsed := time.Since(start)

	assert.Less(t, elapsed, cpuSamplerInterval, "collectAgentLoadSnapshot should read the cache, not block on cpu.Percent")

	// On a real machine these whole-machine metrics must be populated.
	assert.Greater(t, first.CPUCount, 0, "CPUCount should be detected")
	assert.Greater(t, first.MemTotalBytes, uint64(0), "MemTotalBytes should be detected")
	assert.NotEmpty(t, first.CollectedAt, "CollectedAt should be set")

	// Uptime is relative to package init; it must be non-negative and increase.
	assert.GreaterOrEqual(t, second.AgentUptimeSec, first.AgentUptimeSec, "agent uptime should be monotonic")
}

func TestCPUSamplerOnceIdempotent(t *testing.T) {
	// Repeated starts must not spawn additional samplers (sync.Once). Reading the
	// cache twice within a sampling window returns the same cached value because
	// the sampler does not run inline.
	for i := 0; i < 10; i++ {
		startCPUSamplerOnce()
	}

	start := time.Now()
	a := collectAgentLoadSnapshot()
	b := collectAgentLoadSnapshot()
	elapsed := time.Since(start)

	assert.Equal(t, a.CPUUsagePct, b.CPUUsagePct, "cached CPU% should be stable within a sampling window")
	assert.Less(t, elapsed, 250*time.Millisecond, "cache reads should return promptly")
}

func TestAgentProcessStartedAtInPast(t *testing.T) {
	now := time.Now()
	assert.True(t, agentProcessStartedAt.Before(now) || agentProcessStartedAt.Equal(now), "agentProcessStartedAt should be at or before now")
	assert.GreaterOrEqual(t, time.Since(agentProcessStartedAt), time.Duration(0), "uptime since process start should be non-negative")
}
