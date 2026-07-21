package tunnel

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andydunstall/piko/client"
)

type Config struct {
	DeviceID        string
	PikoUpstreamURL string
	TunnelToken     string
	RoutePublicKey  string
	ExpiresAt       time.Time
}

type Status struct {
	DeviceID string
	State    string
	Error    string
}

type runConfig struct {
	deviceID        string
	pikoUpstreamURL *url.URL
	tunnelToken     string
	handler         http.Handler
}

type runFunc func(context.Context, runConfig, func()) error

type Manager struct {
	configureMu sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	fingerprint [sha256.Size]byte
	generation  uint64
	status      Status
	onStatus    func(Status)
	run         runFunc
}

func NewManager(onStatus func(Status)) *Manager {
	return &Manager{onStatus: onStatus, run: runTunnel}
}

func (m *Manager) Configure(config Config) (Status, bool, error) {
	m.configureMu.Lock()
	defer m.configureMu.Unlock()

	config = normalizeConfig(config)
	runtimeConfig, fingerprint, err := validateConfig(config)
	if err != nil {
		return Status{}, false, err
	}

	m.mu.Lock()
	if m.cancel != nil && m.fingerprint == fingerprint {
		status := m.status
		m.mu.Unlock()
		return status, false, nil
	}
	oldCancel := m.cancel
	oldDone := m.done
	if oldCancel != nil {
		m.generation++
		m.cancel = nil
		m.done = nil
		m.fingerprint = [sha256.Size]byte{}
	}
	m.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
		select {
		case <-oldDone:
		case <-time.After(10 * time.Second):
			return Status{}, false, errors.New("previous tunnel session did not stop in time")
		}
	}

	ctx, cancel := context.WithDeadline(context.Background(), config.ExpiresAt)
	done := make(chan struct{})
	m.mu.Lock()
	m.generation++
	generation := m.generation
	m.cancel = cancel
	m.done = done
	m.fingerprint = fingerprint
	m.status = Status{DeviceID: config.DeviceID, State: "connecting"}
	status := m.status
	m.mu.Unlock()

	m.emit(status)
	go m.runGeneration(ctx, done, generation, runtimeConfig)
	return status, true, nil
}

func normalizeConfig(config Config) Config {
	config.DeviceID = strings.TrimSpace(config.DeviceID)
	config.PikoUpstreamURL = strings.TrimSpace(config.PikoUpstreamURL)
	config.TunnelToken = strings.TrimSpace(config.TunnelToken)
	config.RoutePublicKey = strings.TrimSpace(config.RoutePublicKey)
	return config
}

func (m *Manager) Stop() {
	m.configureMu.Lock()
	defer m.configureMu.Unlock()

	m.mu.Lock()
	if m.cancel == nil {
		m.mu.Unlock()
		return
	}
	cancel := m.cancel
	done := m.done
	m.cancel = nil
	m.done = nil
	m.fingerprint = [sha256.Size]byte{}
	m.generation++
	m.status.State = "stopped"
	m.status.Error = ""
	status := m.status
	m.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		status.State = "failed"
		status.Error = "tunnel session did not stop in time"
	}
	m.emit(status)
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) WaitConnected(ctx context.Context, deviceID string) (Status, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := m.Status()
		if status.DeviceID != deviceID {
			return status, errors.New("tunnel session targets another device")
		}
		switch status.State {
		case "connected":
			return status, nil
		case "failed":
			if status.Error == "" {
				return status, errors.New("tunnel connection failed")
			}
			return status, errors.New(status.Error)
		case "stopped":
			return status, errors.New("tunnel session stopped before connecting")
		}
		select {
		case <-ctx.Done():
			return status, errors.New("timed out connecting tunnel data channel")
		case <-ticker.C:
		}
	}
}

func (m *Manager) runGeneration(ctx context.Context, done chan struct{}, generation uint64, config runConfig) {
	defer close(done)
	err := m.run(ctx, config, func() {
		m.updateGeneration(generation, "connected", "")
	})
	if ctx.Err() != nil {
		m.updateGeneration(generation, "stopped", "")
		return
	}
	if err != nil {
		m.updateGeneration(generation, "failed", err.Error())
		return
	}
	m.updateGeneration(generation, "stopped", "")
}

func (m *Manager) updateGeneration(generation uint64, state, message string) {
	m.mu.Lock()
	if generation != m.generation {
		m.mu.Unlock()
		return
	}
	m.status.State = state
	m.status.Error = message
	if state == "failed" || state == "stopped" {
		m.cancel = nil
		m.done = nil
		m.fingerprint = [sha256.Size]byte{}
	}
	status := m.status
	m.mu.Unlock()
	m.emit(status)
}

func (m *Manager) emit(status Status) {
	if m.onStatus != nil {
		m.onStatus(status)
	}
}

func validateConfig(config Config) (runConfig, [sha256.Size]byte, error) {
	if config.DeviceID == "" || config.PikoUpstreamURL == "" || config.TunnelToken == "" || config.RoutePublicKey == "" {
		return runConfig{}, [sha256.Size]byte{}, errors.New("device ID, Piko upstream URL, tunnel token, and route public key are required")
	}
	if config.ExpiresAt.IsZero() || !config.ExpiresAt.After(time.Now().Add(5*time.Second)) {
		return runConfig{}, [sha256.Size]byte{}, errors.New("tunnel configuration is expired or expires too soon")
	}

	parsedURL, err := url.Parse(config.PikoUpstreamURL)
	if err != nil || parsedURL.Host == "" {
		return runConfig{}, [sha256.Size]byte{}, errors.New("invalid Piko upstream URL")
	}
	if parsedURL.User != nil || parsedURL.Fragment != "" {
		return runConfig{}, [sha256.Size]byte{}, errors.New("Piko upstream URL must not contain user info or a fragment")
	}
	if parsedURL.Scheme != "https" && !(parsedURL.Scheme == "http" && isLoopbackHost(parsedURL.Hostname())) {
		return runConfig{}, [sha256.Size]byte{}, errors.New("Piko upstream URL must use HTTPS except for loopback development")
	}

	verifier, err := newRouteVerifier(config.RoutePublicKey)
	if err != nil {
		return runConfig{}, [sha256.Size]byte{}, err
	}
	handler, err := newHandler(config.DeviceID, verifier)
	if err != nil {
		return runConfig{}, [sha256.Size]byte{}, err
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{
		config.DeviceID,
		parsedURL.String(),
		config.TunnelToken,
		config.RoutePublicKey,
		config.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return runConfig{
		deviceID:        config.DeviceID,
		pikoUpstreamURL: parsedURL,
		tunnelToken:     config.TunnelToken,
		handler:         handler,
	}, fingerprint, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func runTunnel(ctx context.Context, config runConfig, ready func()) error {
	upstream := &client.Upstream{URL: config.pikoUpstreamURL, Token: config.tunnelToken}
	listener, err := upstream.Listen(ctx, config.deviceID)
	if err != nil {
		return fmt.Errorf("connect Piko upstream: %w", err)
	}
	defer listener.Shutdown()
	ready()

	server := &http.Server{
		Handler:           config.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case <-ctx.Done():
		// Shutdown the Piko session first so hijacked WebSocket streams are also
		// closed immediately on disable, credential rotation, or expiry.
		_ = listener.Shutdown()
		_ = server.Close()
		return nil
	case err := <-serveErr:
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, client.ErrClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("serve tunnel traffic: %w", err)
	}
}
