package tunnel

import "testing"

func TestValidateTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		host string
		port int
		ok   bool
	}{
		{name: "localhost", host: "localhost", port: 3000, ok: true},
		{name: "ipv4 loopback", host: "127.0.0.1", port: 80, ok: true},
		{name: "ipv6 loopback", host: "::1", port: 8080, ok: true},
		{name: "private 10", host: "10.2.3.4", port: 443, ok: true},
		{name: "private 172", host: "172.16.10.3", port: 65535, ok: true},
		{name: "private 192", host: "192.168.1.8", port: 1, ok: true},
		{name: "dns rejected", host: "internal.example", port: 80},
		{name: "public rejected", host: "8.8.8.8", port: 53},
		{name: "link local rejected", host: "169.254.1.2", port: 80},
		{name: "ipv6 private rejected", host: "fd00::1", port: 80},
		{name: "bad port", host: "127.0.0.1", port: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarget(tt.host, tt.port)
			if (err == nil) != tt.ok {
				t.Fatalf("validateTarget(%q, %d) error = %v, want ok=%t", tt.host, tt.port, err, tt.ok)
			}
		})
	}
}
