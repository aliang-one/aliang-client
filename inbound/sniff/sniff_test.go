package sniff

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// pipeWith 起一条 net.Pipe,写端写完即关——net.Pipe 无 EOF 除非对端关闭,
// 回放断言(io.ReadAll)必须等写端关闭才返回。
func pipeWith(t *testing.T, input []byte) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		_, _ = client.Write(input)
		_ = client.Close()
	}()
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestSniffClassifiesAndReplays(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  Kind
	}{
		{"ssh banner", []byte("SSH-2.0-OpenSSH_9\r\n"), KindSSH},
		{"socks greeting", []byte{0x05, 0x02, 0x00, 0x02}, KindSocks},
		{"http request", []byte("POST /v1/messages HTTP/1.1\r\nHost: x\r\n\r\n"), KindHTTP},
		{"unknown binary", []byte{0xFF, 0xFE, 0x00, 0x01}, KindHTTP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := pipeWith(t, tc.input)
			kind, c, err := Sniff(conn)
			if err != nil {
				t.Fatalf("Sniff: %v", err)
			}
			if kind != tc.want {
				t.Fatalf("kind = %v, want %v", kind, tc.want)
			}
			got, err := io.ReadAll(c)
			if err != nil {
				t.Fatalf("replay read: %v", err)
			}
			if !bytes.Equal(got, tc.input) {
				t.Fatalf("replay mismatch:\n got %q\nwant %q", got, tc.input)
			}
		})
	}
}

func TestSniffTimeoutReturnsError(t *testing.T) {
	old := sniffTimeout
	sniffTimeout = 50 * time.Millisecond
	defer func() { sniffTimeout = old }()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	start := time.Now()
	kind, c, err := Sniff(server)
	if err == nil {
		t.Fatal("expected timeout error when peer sends nothing")
	}
	if kind != KindUnknown || c != nil {
		t.Fatalf("timeout must yield (KindUnknown, nil), got (%v, %v)", kind, c)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout not honoured, elapsed %v", elapsed)
	}
}

func TestKindString(t *testing.T) {
	cases := map[Kind]string{
		KindUnknown: "unknown",
		KindSSH:     "ssh",
		KindSocks:   "socks",
		KindHTTP:    "http",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Fatalf("Kind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestClassifyShortHead(t *testing.T) {
	// 不足 4 字节的首段不得越界:单字节 '{' 按 HTTP 兜底(帧协议退役后无
	// mesh 分派面),单字节 0x05 不入 socks(方法数字节缺失),其余按 HTTP。
	if got := classify([]byte("{")); got != KindHTTP {
		t.Fatalf("single '{' = %v, want KindHTTP", got)
	}
	if got := classify([]byte{0x05}); got != KindHTTP {
		t.Fatalf("single 0x05 = %v, want KindHTTP", got)
	}
	if got := classify([]byte("SS")); got != KindHTTP {
		t.Fatalf("short 'SS' = %v, want KindHTTP", got)
	}
}
