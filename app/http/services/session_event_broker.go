package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	auth "aliang.one/nursorgate/processor/auth"
)

// SessionEventBroker fans SessionAuthority transitions out to connected SSE
// clients (the browser dashboard). It bridges the in-process authority to a push
// channel so the UI reflects identity changes instantly instead of waiting for
// the 5s /api/startup/status poll.
type SessionEventBroker struct {
	mu      sync.RWMutex
	clients map[uint64]chan auth.SessionEvent
	nextID  uint64
}

var sessionEventBroker = NewSessionEventBroker()

func NewSessionEventBroker() *SessionEventBroker {
	return &SessionEventBroker{clients: make(map[uint64]chan auth.SessionEvent)}
}

func init() {
	// Broadcast every authority transition to connected SSE clients.
	auth.GetSessionAuthority().Subscribe(func(e auth.SessionEvent) {
		sessionEventBroker.Broadcast(e)
	})
}

func (b *SessionEventBroker) Subscribe() (uint64, <-chan auth.SessionEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan auth.SessionEvent, 16)
	b.clients[id] = ch
	return id, ch
}

func (b *SessionEventBroker) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.clients[id]; ok {
		delete(b.clients, id)
		close(ch)
	}
}

// Broadcast is non-blocking: a slow client whose buffer is full is dropped
// (it will re-sync on the next reconnect's snapshot).
func (b *SessionEventBroker) Broadcast(e auth.SessionEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.clients {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *SessionEventBroker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// ServeSessionEvents is the SSE endpoint for the browser dashboard.
// GET /api/session/events — sends a state snapshot on connect, then streams
// each transition until the client disconnects.
func ServeSessionEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	snapshot := map[string]any{
		"type":  "snapshot",
		"state": auth.GetSessionAuthority().State().String(),
	}
	if user := auth.GetCurrentUserInfo(); user != nil {
		snapshot["user"] = map[string]any{
			"id":       user.ID,
			"email":    user.Email,
			"username": user.Username,
			"role":     user.Role,
			"status":   user.Status,
		}
	}
	writeSSE(w, snapshot)
	flusher.Flush()

	id, ch := sessionEventBroker.Subscribe()
	defer sessionEventBroker.Unsubscribe(id)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, map[string]any{
				"type":   "transition",
				"from":   e.From.String(),
				"to":     e.To.String(),
				"reason": string(e.Reason),
			})
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}
