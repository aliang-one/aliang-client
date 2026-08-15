package runtime

import (
	"sync"
	"time"

	authuser "aliang.one/nursorgate/processor/auth"
)

// StartupStatus represents the current startup state
type StartupStatus string

const (
	UNCONFIGURED = StartupStatus("UNCONFIGURED") // No config, no token, no local user
	CONFIGURING  = StartupStatus("CONFIGURING")  // Token provided, activating
	CONFIGURED   = StartupStatus("CONFIGURED")   // User loaded but fetch failed
	READY        = StartupStatus("READY")        // Everything loaded and fetch succeeded
)

// StartupState manages the global startup state
type StartupState struct {
	mu           sync.RWMutex
	status       StartupStatus
	fetchSuccess bool
	timestamp    time.Time
}

var (
	globalStartupState *StartupState
	once               sync.Once
)

// GetStartupState returns the global startup state singleton
func GetStartupState() *StartupState {
	once.Do(func() {
		globalStartupState = &StartupState{
			status:       UNCONFIGURED,
			fetchSuccess: false,
			timestamp:    time.Now(),
		}
	})
	return globalStartupState
}

// SetStatus sets the current startup status
func (s *StartupState) SetStatus(status StartupStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.timestamp = time.Now()
}

// GetStatus returns the current startup status
func (s *StartupState) GetStatus() StartupStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// SetFetchSuccess marks whether user authentication is successful
func (s *StartupState) SetFetchSuccess(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetchSuccess = success
}

// GetFetchSuccess returns whether user authentication is successful
func (s *StartupState) GetFetchSuccess() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fetchSuccess
}

// GetTimestamp returns when the state was last updated
func (s *StartupState) GetTimestamp() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.timestamp
}

// ===== Test-Only Exports =====

// ResetGlobalStartupStateForTest resets the global startup state singleton for testing
// This allows tests to run in isolation without state pollution
func ResetGlobalStartupStateForTest() {
	authuser.SetCurrentUserInfo(nil)
	// Reset the singleton by setting it to nil
	// The next call to GetStartupState() will reinitialize it
	globalStartupState = nil
	// Also need to reset the sync.Once, but since we can't directly reset it,
	// we'll create a fresh state directly
	once.Do(func() {}) // Mark done = true if not already
	globalStartupState = &StartupState{
		status:       UNCONFIGURED,
		fetchSuccess: false,
		timestamp:    time.Now(),
	}
}
