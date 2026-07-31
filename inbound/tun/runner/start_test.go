package runner

import (
	"syscall"
	"testing"
)

func TestDrainPendingTunSignalsRemovesPreviousLifecycleStops(t *testing.T) {
	PrepareStart()
	TunSignal <- syscall.SIGTERM

	PrepareStart()

	select {
	case signalValue := <-TunSignal:
		t.Fatalf("stale TUN signal remained queued: %v", signalValue)
	default:
	}
}

func TestStopIsIdempotentWhileSignalIsPending(t *testing.T) {
	PrepareStart()

	Stop()
	Stop()

	select {
	case signalValue := <-TunSignal:
		if signalValue != syscall.SIGTERM {
			t.Fatalf("unexpected TUN stop signal: %v", signalValue)
		}
	default:
		t.Fatal("expected a queued TUN stop signal")
	}

	select {
	case signalValue := <-TunSignal:
		t.Fatalf("duplicate TUN stop signal remained queued: %v", signalValue)
	default:
	}
}
