package geoip

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
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

func TestEnsureLoadedAsync_LocalFileSyncLoad(t *testing.T) {
	p := os.Getenv("GEOIP_TEST_MMDB")
	if p == "" {
		t.Skip("GEOIP_TEST_MMDB not set")
	}
	// EnsureLoadedAsync 解析的是 state 目录路径——必须把 fixture 放到隔离 state 的 geoip 子目录。
	// state 目录解析读 ALIANG_CACHE_DIR（runtimepath.ResolveStateDir），且 cache 目录为包级
	// 单例：设 env 后须重置再解析，cleanup 再重置一次以免污染同包后续测试。
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	dest, err := cache.GetCacheSubdir("geoip")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, DefaultGeoIPDatabaseFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := GetService()
	defer s.Disable()
	if err := s.EnsureLoadedAsync(); err != nil {
		t.Fatalf("EnsureLoadedAsync: %v", err)
	}
	if !s.IsEnabled() {
		t.Fatal("expected enabled (local file)")
	}
}

func TestEnsureLoadedAsync_MissingFileReturnsImmediately(t *testing.T) {
	// 隔离 state 目录（文件不存在）→ 应立即返回，下载在后台失败仅日志
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "http://127.0.0.1:1/never.mmdb") // 快速失败地址
	s := GetService()
	defer s.Disable()
	// 下载 seam：被调用即通知、随即失败；同步等它被调用完，避免后台 goroutine 泄漏
	// （downloadFunc 的读取发生在调用前，<-called 后再无共享状态访问）
	called := make(chan struct{})
	orig := downloadFunc
	t.Cleanup(func() { downloadFunc = orig })
	downloadFunc = func(url, dest string) error {
		close(called)
		return fmt.Errorf("offline")
	}
	start := time.Now()
	if err := s.EnsureLoadedAsync(); err != nil {
		t.Fatalf("EnsureLoadedAsync: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("EnsureLoadedAsync blocked on missing file: %v", elapsed)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("background download never started")
	}
	// 下载失败后 goroutine 仅剩 Warn 日志尾；稍候让其退出
	time.Sleep(100 * time.Millisecond)
}

func TestEnsureDownloadedSingleFlight(t *testing.T) {
	// 质量审查 Important#1：并发双下载 tmp 碰撞 → single-flight 互斥后 downloadFunc 只调一次
	dir := t.TempDir()
	target := filepath.Join(dir, "db.mmdb")
	orig := downloadFunc
	t.Cleanup(func() { downloadFunc = orig })
	var calls int32
	downloadFunc = func(url, dest string) error {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond)
		return os.WriteFile(dest, []byte("x"), 0o644)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = EnsureDownloaded(target)
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected single-flight download (1 call), got %d", n)
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
