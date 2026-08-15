package client

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/common/cache"
	cert_config "aliang.one/nursorgate/processor/cert"
	"aliang.one/nursorgate/processor/cert/generator"
)

func TestCAManagerResolvesRuntimePathLazilyAndClearsHostCache(t *testing.T) {
	originalCacheDir := os.Getenv("ALIANG_CACHE_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("ALIANG_CACHE_DIR", originalCacheDir)
		cache.ResetCacheDirForTest()
	})

	firstDir := t.TempDir()
	secondDir := t.TempDir()
	generateManagerTestCA(t, firstDir, "first-root")
	generateManagerTestCA(t, secondDir, "second-root")
	manager := &CAManager{}

	_ = os.Setenv("ALIANG_CACHE_DIR", firstDir)
	cache.ResetCacheDirForTest()
	first, err := manager.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := certificateCommonName(t, first.Certificate[0]); got != "first-root" {
		t.Fatalf("first certificate CN = %q", got)
	}

	certCache.Store("cached.example", *first)
	certAccessTime.Store("cached.example", first.Leaf)
	_ = os.Setenv("ALIANG_CACHE_DIR", secondDir)
	cache.ResetCacheDirForTest()
	second, err := manager.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := certificateCommonName(t, second.Certificate[0]); got != "second-root" {
		t.Fatalf("second certificate CN = %q", got)
	}
	if _, ok := certCache.Load("cached.example"); ok {
		t.Fatal("host certificate cache was not cleared after CA path changed")
	}
}

func generateManagerTestCA(t *testing.T, stateDir, commonName string) {
	t.Helper()
	path := filepath.Join(stateDir, "certs", "mitm-ca.pem")
	config := cert_config.MitmCAConfig
	config.CN = commonName
	config.Issuer = commonName
	if err := generator.GenerateCertificateFromConfig(&config, path); err != nil {
		t.Fatal(err)
	}
}

func certificateCommonName(t *testing.T, der []byte) string {
	t.Helper()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate.Subject.CommonName
}
