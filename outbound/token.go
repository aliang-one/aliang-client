package outbound

import (
	"sync"
)

var (
	tokenMu  sync.RWMutex
	tokenVal string
)

// SetOutboundToken sets the outbound token
func SetOutboundToken(token string) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	tokenVal = token
}

// GetOutboundToken gets the outbound token
func GetOutboundToken() string {
	tokenMu.RLock()
	defer tokenMu.RUnlock()
	return tokenVal
}
