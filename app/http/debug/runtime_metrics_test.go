//go:build !prod

package debug

import "testing"

// TestRuntimeMetricsSnapshotReportsPositiveValues covers the pure snapshot
// helper that the periodic logger delegates to. The curve of these values is
// what distinguishes the TUN memory-growth suspects (see runtime_metrics.go).
func TestRuntimeMetricsSnapshotReportsPositiveValues(t *testing.T) {
	goroutines, heapAllocMB, heapSysMB := runtimeMetricsSnapshot()
	if goroutines < 1 {
		t.Fatalf("goroutines=%d, want >=1", goroutines)
	}
	if heapAllocMB <= 0 {
		t.Fatalf("heap_alloc=%.3fMB, want >0", heapAllocMB)
	}
	if heapSysMB < heapAllocMB {
		t.Fatalf("heap_sys=%.3fMB < heap_alloc=%.3fMB", heapSysMB, heapAllocMB)
	}
}
