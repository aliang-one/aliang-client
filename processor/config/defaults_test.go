package config

import "testing"

func TestServiceAddressesDefaultToLoopback(t *testing.T) {
	if DefaultManagementAddr != "127.0.0.1:56431" {
		t.Fatalf("DefaultManagementAddr = %q, want loopback", DefaultManagementAddr)
	}
	if DefaultHTTPProxyAddr != "127.0.0.1:56432" {
		t.Fatalf("DefaultHTTPProxyAddr = %q, want loopback", DefaultHTTPProxyAddr)
	}

	resetServiceBindHost(t)
	if got := ManagementListenAddr(); got != DefaultManagementAddr {
		t.Fatalf("ManagementListenAddr() = %q, want %q", got, DefaultManagementAddr)
	}
	if got := HTTPProxyListenAddr(); got != DefaultHTTPProxyAddr {
		t.Fatalf("HTTPProxyListenAddr() = %q, want %q", got, DefaultHTTPProxyAddr)
	}
}

func TestSetServiceBindHostExposesBothServices(t *testing.T) {
	resetServiceBindHost(t)
	userAgentAddr := DefaultUserAgentAddr

	if err := SetServiceBindHost("0.0.0.0"); err != nil {
		t.Fatalf("SetServiceBindHost() error = %v", err)
	}
	if got := ManagementListenAddr(); got != "0.0.0.0:56431" {
		t.Fatalf("ManagementListenAddr() = %q", got)
	}
	if got := HTTPProxyListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("HTTPProxyListenAddr() = %q", got)
	}

	if DefaultUserAgentAddr != userAgentAddr {
		t.Fatalf("user agent address changed from %q to %q", userAgentAddr, DefaultUserAgentAddr)
	}
}

func TestSetServiceBindHostRejectsInvalidValueWithoutMutation(t *testing.T) {
	resetServiceBindHost(t)

	if err := SetServiceBindHost("0.0.0.0:56431"); err == nil {
		t.Fatal("SetServiceBindHost() accepted a host containing a port")
	}
	if got := ServiceBindHost(); got != DefaultServiceBindHost {
		t.Fatalf("ServiceBindHost() = %q after invalid update", got)
	}
}

func resetServiceBindHost(t *testing.T) {
	t.Helper()
	if err := SetServiceBindHost(DefaultServiceBindHost); err != nil {
		t.Fatalf("reset service bind host: %v", err)
	}
	t.Cleanup(func() {
		_ = SetServiceBindHost(DefaultServiceBindHost)
	})
}
