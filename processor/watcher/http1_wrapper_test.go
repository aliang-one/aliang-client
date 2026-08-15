package tls

import (
	"io"
	"net"
	"strings"
	"testing"

	user "aliang.one/nursorgate/processor/auth"
)

func TestWatcherWrapConn_HTTP1KeepAliveInjectsAuthorizationInnerForEveryRequest(t *testing.T) {
	user.ResetAuthPersistenceForTest()
	t.Cleanup(user.ResetAuthPersistenceForTest)
	user.SetCurrentUserInfo(&user.UserInfo{
		AccessToken: "test-access-token",
		TokenType:   "Bearer",
	})

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	wrapConn := NewWatcherWrapConn(serverConn)
	defer serverConn.Close()

	req1Body := `{"a":1}`
	req2Body := `{"b":2}`
	req1 := "" +
		"POST /v1/responses HTTP/1.1\r\n" +
		"Host: 127.0.0.1:56432\r\n" +
		"Content-Type: application/json\r\n" +
		"Content-Length: 7\r\n" +
		"\r\n" +
		req1Body
	req2 := "" +
		"POST /v1/responses HTTP/1.1\r\n" +
		"Host: 127.0.0.1:56432\r\n" +
		"Content-Type: application/json\r\n" +
		"Content-Length: 7\r\n" +
		"\r\n" +
		req2Body

	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(clientConn, req1+req2)
		_ = clientConn.Close()
		writeDone <- err
	}()

	data, err := io.ReadAll(wrapConn)
	if err != nil {
		t.Fatalf("io.ReadAll(wrapConn) error = %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("writer error = %v", err)
	}

	output := string(data)
	if got := strings.Count(output, "Authorization-Inner: Bearer test-access-token\r\n"); got != 2 {
		t.Fatalf("expected 2 injected Authorization-Inner headers, got %d\noutput:\n%s", got, output)
	}
	if got := strings.Count(output, "POST /v1/responses HTTP/1.1\r\n"); got != 2 {
		t.Fatalf("expected 2 rewritten HTTP/1 requests, got %d\noutput:\n%s", got, output)
	}
}
