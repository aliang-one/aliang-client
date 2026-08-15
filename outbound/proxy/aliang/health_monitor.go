package aliang

import (
	"context"
	"fmt"
	"time"

	"aliang.one/nursorgate/common/logger"
)

func (c *Aliang) healthMonitorHealthyInterval() time.Duration {
	if c.config != nil && c.config.LinkHealthInterval > 0 {
		return c.config.LinkHealthInterval
	}
	return defaultHealthMonitorInterval
}

func (c *Aliang) healthMonitorRecoveryInterval() time.Duration {
	if c.config != nil && c.config.LinkHealthRecoveryInterval > 0 {
		return c.config.LinkHealthRecoveryInterval
	}
	return defaultHealthRecoveryInterval
}

// StartHealthMonitor launches the background link self-heal loop. It is a no-op
// if already running or after Close. Constructors do not start it (starting a
// network goroutine from a constructor is a Go anti-pattern and would make
// short-lived/test instances dial real relays); the production refresh path
// calls this explicitly.
func (c *Aliang) StartHealthMonitor() {
	c.monitorMu.Lock()
	defer c.monitorMu.Unlock()
	if c.monitorCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.monitorCtx = ctx
	c.monitorCancel = cancel
	c.monitorDone = done
	go c.runHealthMonitor(ctx, done)
}

// stopHealthMonitor cancels the monitor and waits for it to exit. Safe to call
// when not running.
func (c *Aliang) stopHealthMonitor() {
	c.monitorMu.Lock()
	cancel := c.monitorCancel
	done := c.monitorDone
	c.monitorCancel = nil
	c.monitorDone = nil
	c.monitorMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

// runHealthMonitor keeps the link status fresh and self-heals it after a
// disconnect. While healthy it probes at a leisurely cadence (a backstop for
// when there is no traffic and no dashboard open); while disconnected/unknown
// it re-probes on a short cadence so the moment the relay is reachable again
// the state flips back to connected — without a manual reconnect click.
//
// done is captured locally (not re-read from the field) so stopHealthMonitor
// can nil out the field before this goroutine exits without panicking on a
// close of a nil channel.
func (c *Aliang) runHealthMonitor(ctx context.Context, done chan struct{}) {
	defer close(done)
	healthyWait := c.healthMonitorHealthyInterval()
	recoveryWait := c.healthMonitorRecoveryInterval()
	logger.Debug(fmt.Sprintf("[AliangGate] health monitor started (healthy=%s recovery=%s)", healthyWait, recoveryWait))
	for {
		state := c.status.state()
		wait := healthyWait
		if isRecoverable(state) {
			c.ProbeLink(ctx)
			wait = recoveryWait
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// isRecoverable reports whether the monitor should actively re-probe to heal
// the link. Connected/degraded are left alone (real traffic + the healthy
// cadence keep them fresh); unknown/connecting/disconnected mean we do not yet
// trust the link is up, so we probe on the recovery cadence.
func isRecoverable(state string) bool {
	switch state {
	case LinkStateConnected, LinkStateDegraded:
		return false
	default:
		return true
	}
}
