package geoip

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadLocal(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.mmdb")
	s := &Service{}
	if err := s.LoadLocal(missing); err == nil {
		t.Fatal("expected error for missing file")
	}
	// 有效库场景：GEOIP_TEST_MMDB 提供真实 mmdb 路径时才跑（CI 无网可跳）
	if p := os.Getenv("GEOIP_TEST_MMDB"); p != "" {
		s2 := &Service{}
		if err := s2.LoadLocal(p); err != nil {
			t.Fatalf("LoadLocal: %v", err)
		}
		if !s2.IsEnabled() {
			t.Fatal("expected enabled after load")
		}
		if info, err := s2.LookupCountry(net.ParseIP("114.114.114.114")); err != nil || info == nil || info.ISOCode != "CN" {
			t.Fatalf("lookup CN: %v %+v", err, info)
		}
	} else {
		t.Skip("GEOIP_TEST_MMDB not set")
	}
}

func TestLoadLocalCorruptFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.mmdb")
	os.WriteFile(corrupt, []byte("not-a-mmdb"), 0o644)
	s := &Service{}
	if err := s.LoadLocal(corrupt); err == nil {
		t.Fatal("expected error for corrupt file")
	}
	if s.IsEnabled() {
		t.Fatal("must not be enabled after failed load")
	}
}

func TestEnsureDownloadedUsesOverriddenURLAndSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "db.mmdb")
	os.WriteFile(existing, []byte("placeholder"), 0o644)
	origURL := os.Getenv("ALIANG_GEOIP_DOWNLOAD_URL")
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "http://127.0.0.1:1/never.mmdb") // 必失败地址
	if err := EnsureDownloaded(existing); err != nil {
		t.Fatalf("existing file must skip download: %v", err)
	}
	if origURL != "" {
		t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", origURL)
	}
}

func TestLookupDuringDownloadMissesImmediately(t *testing.T) {
	// seam：downloadFunc 替换为慢写入器；并发 Lookup 必须立即不命中而非阻塞
	s := &Service{}
	dir := t.TempDir()
	target := filepath.Join(dir, "db.mmdb")
	orig := downloadFunc
	t.Cleanup(func() { downloadFunc = orig })
	release := make(chan struct{})
	downloadFunc = func(url, dest string) error {
		os.WriteFile(dest, []byte("pending"), 0o644) // 非法内容
		<-release
		os.WriteFile(dest, []byte("still-invalid"), 0o644)
		return nil
	}
	finished := make(chan struct{})
	go func() { // LoadLocal 会因内容非法报错——测试只断言 lookup 不阻塞
		defer close(finished)
		_ = EnsureDownloaded(target)
		_ = s.LoadLocal(target)
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		done := make(chan struct{})
		go func() { _, _ = s.LookupCountry(net.ParseIP("1.2.3.4")); close(done) }()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("LookupCountry blocked while download in progress")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	<-finished // 等后台 goroutine 退出后再返回，避免 t.Cleanup 恢复 downloadFunc 与其竞争
}
