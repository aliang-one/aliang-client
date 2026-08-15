package mirror

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	forwarderChannelSize = 1024
	forwarderHTTPTimeout = 10 * time.Second
	forwarderMaxIdleConns = 4
	forwarderIdleConnTimeout = 30 * time.Second
)

// Forwarder asynchronously sends MirrorMessages to a target HTTP endpoint.
type Forwarder struct {
	target string
	client *http.Client
	ch     chan MirrorMessage
	done   chan struct{}
	once   sync.Once
}

var (
	globalForwarder *Forwarder
	globalMu        sync.RWMutex
)

// InitGlobalForwarder creates or recreates the global forwarder based on current config.
// It is safe to call this multiple times; each call stops the previous forwarder.
func InitGlobalForwarder() {
	globalMu.Lock()
	defer globalMu.Unlock()

	// Stop existing forwarder
	if globalForwarder != nil {
		globalForwarder.Stop()
		globalForwarder = nil
	}

	cfg := loadMirrorConfig()
	if cfg == nil {
		return
	}

	f := &Forwarder{
		target: cfg.Target,
		client: &http.Client{
			Timeout: forwarderHTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        forwarderMaxIdleConns,
				IdleConnTimeout:     forwarderIdleConnTimeout,
				DisableCompression:  true,
			},
		},
		ch:   make(chan MirrorMessage, forwarderChannelSize),
		done: make(chan struct{}),
	}

	go f.run()
	globalForwarder = f
	logger.Debug("[Mirror] Global forwarder initialized, target=" + cfg.Target)
}

// GetGlobalForwarder returns the current global forwarder, or nil.
func GetGlobalForwarder() *Forwarder {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalForwarder
}

// Stop gracefully shuts down the forwarder, draining remaining chunks.
func (f *Forwarder) Stop() {
	f.once.Do(func() {
		close(f.ch)
		<-f.done // wait for worker to finish
	})
}

// Enqueue adds a message to the forwarding queue. Non-blocking: drops if full.
func (f *Forwarder) Enqueue(msg MirrorMessage) {
	if f == nil || msg == nil {
		return
	}
	select {
	case f.ch <- msg:
	default:
		// Channel full — drop message
		logger.Debug("[Mirror] Forwarder queue full, dropping message")
	}
}

// run is the worker goroutine that reads messages and POSTs them.
func (f *Forwarder) run() {
	defer close(f.done)

	for msg := range f.ch {
		f.send(msg)
	}
}

// send performs an HTTP POST of a single MirrorMessage.
func (f *Forwarder) send(msg MirrorMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		logger.Debug("[Mirror] Failed to marshal message: " + err.Error())
		return
	}

	resp, err := f.client.Post(f.target, "application/json", bytes.NewReader(data))
	if err != nil {
		logger.Debug("[Mirror] Failed to send message: " + err.Error())
		return
	}
	resp.Body.Close()
}
