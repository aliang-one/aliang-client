package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/middleware"
	auth "aliang.one/nursorgate/processor/auth"
)

const sessionSSEHeartbeatInterval = 15 * time.Second

// SessionEventBroker is a latest-value broadcaster. Each subscriber retains at
// most one complete immutable snapshot; a slow browser loses intermediate
// transitions but always converges to the newest authority revision.
type SessionEventBroker struct {
	mu      sync.Mutex
	clients map[uint64]chan auth.SessionSnapshot
	nextID  uint64
}

var sessionEventBroker = NewSessionEventBroker()

func NewSessionEventBroker() *SessionEventBroker {
	return &SessionEventBroker{clients: make(map[uint64]chan auth.SessionSnapshot)}
}

func init() {
	auth.SubscribeGlobal(func(event auth.SessionEvent) {
		sessionEventBroker.Publish(event.Snapshot)
	})
}

func (b *SessionEventBroker) Subscribe() (uint64, <-chan auth.SessionSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan auth.SessionSnapshot, 1)
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

func (b *SessionEventBroker) Publish(snapshot auth.SessionSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.clients {
		select {
		case ch <- snapshot:
			continue
		default:
		}

		// Replace the stale pending snapshot. Publish calls are serialized by b.mu,
		// so the single queued value cannot move backwards in revision order.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- snapshot:
		default:
		}
	}
}

func (b *SessionEventBroker) ClientCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// ServeSessionEvents subscribes before reading the initial authority snapshot.
// A transition racing that read is either reflected in the read or queued; the
// browser revision reducer safely ignores any duplicate/older queued snapshot.
func ServeSessionEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
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

	id, ch := sessionEventBroker.Subscribe()
	defer sessionEventBroker.Unsubscribe(id)

	if err := writeSessionSSE(w, BuildSessionSnapshotPayload(auth.GetSessionAuthority().Snapshot())); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(sessionSSEHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snapshot, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSessionSSE(w, BuildSessionSnapshotPayload(snapshot)); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSessionSSE(w http.ResponseWriter, payload SessionSnapshotPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}
