package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

// tcpBridge serves raw TCP mappings on the "<deviceID>.tcp" piko endpoint.
// Each incoming stream carries a single-line JSON route header followed by
// transparent bidirectional bytes:
//
//	{"ticket":"<ed25519 route ticket>","target_host":"127.0.0.1","target_port":22}\n
//	<raw bytes both ways>
//
// The ticket is signed by the tunnel gateway per connection (same Ed25519 key
// and claims as HTTP route tickets, with UpstreamScheme "tcp") so an attacker
// who can reach piko still cannot bridge into arbitrary local ports.
type tcpBridge struct {
	deviceID     string
	verifier     *routeVerifier
	allowPrivate bool
	dialTimeout  time.Duration
}

func newTCPBridge(deviceID string, verifier *routeVerifier, allowPrivate bool) *tcpBridge {
	return &tcpBridge{
		deviceID:     deviceID,
		verifier:     verifier,
		allowPrivate: allowPrivate,
		dialTimeout:  10 * time.Second,
	}
}

type tcpRouteHeader struct {
	Ticket     string `json:"ticket"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
}

// Serve blocks serving connections from listener until it closes.
func (b *tcpBridge) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go b.handle(conn)
	}
}

func (b *tcpBridge) handle(downstream net.Conn) {
	defer downstream.Close()
	_ = downstream.SetDeadline(time.Now().Add(15 * time.Second))

	// The reader may buffer bytes past the header line (a client that writes
	// header and payload back to back); copying must drain it too.
	reader := bufio.NewReader(downstream)
	header, err := b.readHeader(reader)
	if err != nil {
		log.Printf("[AGENT-TCP] header read failed: %v", err)
		return
	}
	log.Printf("[AGENT-TCP] header ok: target=%s:%d", header.TargetHost, header.TargetPort)
	claims, err := b.verifier.verify(header.Ticket)
	if err != nil {
		log.Printf("[AGENT-TCP] ticket verify failed: %v", err)
		return
	}
	if claims.DeviceID != b.deviceID {
		return
	}
	if claims.UpstreamScheme != "tcp" {
		return
	}
	if claims.TargetHost != header.TargetHost || claims.TargetPort != header.TargetPort {
		// Header and ticket must describe the same target; the ticket wins as
		// the signed authority, and a mismatch means a tampered header.
		header.TargetHost = claims.TargetHost
		header.TargetPort = claims.TargetPort
	}
	if err := validateTarget(claims.TargetHost, claims.TargetPort, b.allowPrivate); err != nil {
		return
	}

	_ = downstream.SetDeadline(time.Time{})
	dialCtx, cancel := context.WithTimeout(context.Background(), b.dialTimeout)
	defer cancel()
	var dialer net.Dialer
	upstream, err := dialer.DialContext(dialCtx, "tcp", targetAddress(claims.TargetHost, claims.TargetPort))
	if err != nil {
		log.Printf("[AGENT-TCP] target dial failed: %v", err)
		return
	}
	log.Printf("[AGENT-TCP] bridged to target %s:%d", claims.TargetHost, claims.TargetPort)
	defer upstream.Close()

	// Either direction finishing must not kill the bridge outright: a client
	// that half-closes after its request still expects the response bytes.
	// After the first direction ends, the other keeps copying until it goes
	// idle (no bytes for bridgeIdleTimeout) or the peer closes cleanly.
	doneUp := make(chan struct{})
	doneDown := make(chan struct{})
	go func() {
		copyIdle(upstream, reader, func(t time.Time) { _ = downstream.SetReadDeadline(t) })
		close(doneUp)
	}()
	go func() {
		copyIdle(downstream, upstream, func(t time.Time) { _ = upstream.SetReadDeadline(t) })
		close(doneDown)
	}()
	<-doneUp
	<-doneDown
}

// bridgeIdleTimeout bounds how long a half-finished bridge waits for the
// remaining direction. Generous enough for multi-hop public chains
// (client → VPS → frps → gateway → piko → agent) where the last hop lags.
const bridgeIdleTimeout = 10 * time.Second

func copyIdle(dst io.Writer, src io.Reader, setReadDeadline func(time.Time)) {
	buf := make([]byte, 32*1024)
	for {
		setReadDeadline(time.Now().Add(bridgeIdleTimeout))
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return // EOF, reset, or idle timeout
		}
	}
}

func (b *tcpBridge) readHeader(reader *bufio.Reader) (*tcpRouteHeader, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read route header: %w", err)
	}
	if len(line) > 4096 {
		return nil, errors.New("route header too large")
	}
	header := &tcpRouteHeader{}
	if err := json.Unmarshal([]byte(line), header); err != nil {
		return nil, fmt.Errorf("parse route header: %w", err)
	}
	if header.Ticket == "" || header.TargetHost == "" || header.TargetPort == 0 {
		return nil, errors.New("route header missing fields")
	}
	return header, nil
}

// TCPEndpointID is the piko endpoint raw TCP mappings route through. It is a
// separate endpoint from the HTTP one so the bridge's binary-ish framing
// never meets the HTTP server.
func TCPEndpointID(deviceID string) string {
	return deviceID + ".tcp"
}
