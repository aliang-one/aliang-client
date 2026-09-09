package mirror

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	forwarderChannelSize     = 1024
	forwarderHTTPTimeout     = 10 * time.Second
	forwarderMaxIdleConns    = 4
	forwarderIdleConnTimeout = 30 * time.Second

	batchMaxMessages   = 200
	batchMaxBytes      = 512 * 1024
	batchFlushInterval = 200 * time.Millisecond
)

// Forwarder asynchronously sends MirrorMessages to a target HTTP endpoint.
type Forwarder struct {
	target  string // derived batch endpoint (batchTarget(cfg.Target))
	client  *http.Client
	ch      chan MirrorMessage
	done    chan struct{}
	once    sync.Once
	dropped atomic.Uint64
}

var (
	globalForwarder *Forwarder
	globalMu        sync.RWMutex
)

// batchTarget derives the batch ingest endpoint from the configured target. Leading/trailing whitespace and trailing "/" are trimmed; "/batch" is appended unless already present.
func batchTarget(target string) string {
	t := strings.TrimRight(strings.TrimSpace(target), "/")
	if strings.HasSuffix(t, "/batch") {
		return t
	}
	return t + "/batch"
}

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
		target: batchTarget(cfg.Target),
		client: &http.Client{
			Timeout: forwarderHTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:       forwarderMaxIdleConns,
				IdleConnTimeout:    forwarderIdleConnTimeout,
				DisableCompression: true,
			},
		},
		ch:   make(chan MirrorMessage, forwarderChannelSize),
		done: make(chan struct{}),
	}

	go f.run()
	globalForwarder = f
	logger.Debug("[Mirror] Global forwarder initialized, target=" + f.target)
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

// Dropped returns the number of messages discarded because the queue was full.
func (f *Forwarder) Dropped() uint64 { return f.dropped.Load() }

// Enqueue adds a message to the forwarding queue. Non-blocking: drops if full.
func (f *Forwarder) Enqueue(msg MirrorMessage) {
	if f == nil || msg == nil {
		return
	}
	select {
	case f.ch <- msg:
	default:
		f.dropped.Add(1)
		logger.Debug("[Mirror] Forwarder queue full, dropping message")
	}
}

// batchEnvelope is the wire format of POST /mirror/batch.
type batchEnvelope struct {
	Messages []json.RawMessage `json:"messages"`
}

// run batches messages from the channel and POSTs them to the batch endpoint.
// Order is preserved: single worker, pending slice in arrival order.
func (f *Forwarder) run() {
	defer close(f.done)

	pending := make([]json.RawMessage, 0, batchMaxMessages)
	pendingBytes := 0
	timer := time.NewTimer(batchFlushInterval)
	defer timer.Stop()

	flush := func() {
		if len(pending) > 0 {
			body, err := json.Marshal(batchEnvelope{Messages: pending})
			if err != nil {
				logger.Debug("[Mirror] Failed to marshal batch: " + err.Error())
			} else {
				f.post(body)
			}
			pending = pending[:0]
			pendingBytes = 0
		}
		// Always re-arm the interval timer, even on an empty flush: otherwise
		// an idle fire leaves the timer spent and later messages below the
		// size thresholds would never flush on timeout. Safe since Go 1.23.
		timer.Reset(batchFlushInterval)
	}

	for {
		select {
		case msg, ok := <-f.ch:
			if !ok {
				flush() // channel closed by Stop(): drain the tail batch
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				logger.Debug("[Mirror] Failed to marshal message: " + err.Error())
				continue
			}
			pending = append(pending, json.RawMessage(data))
			pendingBytes += len(data)
			if len(pending) >= batchMaxMessages || pendingBytes >= batchMaxBytes {
				flush()
			}
		case <-timer.C:
			flush()
		}
	}
}

// post sends one batch body. Fire-and-forget: failures are logged and dropped.
func (f *Forwarder) post(body []byte) {
	resp, err := f.client.Post(f.target, "application/json", bytes.NewReader(body))
	if err != nil {
		logger.Debug("[Mirror] Failed to send batch: " + err.Error())
		return
	}
	// Drain a bounded amount of the response body (the /mirror/batch ack is
	// tiny) so the keep-alive connection is reused; larger bodies would force
	// the connection closed, which is acceptable here.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}
