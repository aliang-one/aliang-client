//go:build !unix && !windows

package services

import "errors"

// defaultExternalInterrupt has no portable interrupt primitive on this
// platform (mirrors isPidAlive's fallback in agent_rename_liveness_other.go);
// reporting failure degrades external stop to the legacy "stopped" reply.
func defaultExternalInterrupt(pid int) error {
	return errors.New("external interrupt is not supported on this platform")
}

// defaultExternalInterruptTargetMatches has no cheap identity probe on this
// platform; permissive-by-default keeps the guard from degrading the feature.
func defaultExternalInterruptTargetMatches(pid int) bool {
	return true
}
