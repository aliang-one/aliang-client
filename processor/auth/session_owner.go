package user

import (
	"errors"
	"sync/atomic"
)

var ErrSessionOwnerRequired = errors.New("session operation requires the session owner process")

// sessionOwnerProcess is true only in the process that owns the persisted user
// session. The user-agent process is a credential consumer: it receives fresh
// access tokens from the owner and must never rotate refresh tokens itself.
var sessionOwnerProcess atomic.Bool

func init() {
	sessionOwnerProcess.Store(true)
}

// SetSessionOwnerProcess configures whether this process may refresh or
// invalidate the persisted user session. Command entrypoints set this once
// before initializing authentication.
func SetSessionOwnerProcess(owner bool) {
	sessionOwnerProcess.Store(owner)
	if !owner {
		StopTokenRefresh()
	}
}

func IsSessionOwnerProcess() bool {
	return sessionOwnerProcess.Load()
}
