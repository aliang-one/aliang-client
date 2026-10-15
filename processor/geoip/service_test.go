package geoip

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// minimalMMDB 构造可被 geoip2.Open 接受的最小 MaxMind DB 字节：
// 16 字节数据段分隔零 + 元数据标记 + {database_type, record_size} 元数据 map
// （node_count 缺省为 0 → 空 SearchTree；maxminddb 校验 marker 存在、metadata
// 可解、searchTreeSize+16 ≤ marker 起点、record_size ∈ {24,28,32}；geoip2
// 另要求 database_type 在其支持清单内）。
func minimalMMDB() []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 16))                      // data section separator（16 零字节）
	buf.Write([]byte{0xAB, 0xCD, 0xEF})              // metadata marker 前缀
	buf.WriteString("MaxMind.com")                   // metadata marker 余段
	buf.WriteByte(0xE2)                              // map，2 个键值对
	buf.WriteByte(0x40 | byte(len("database_type"))) // utf8 string，长度 13
	buf.WriteString("database_type")
	buf.WriteByte(0x40 | byte(len("GeoLite2-Country"))) // utf8 string，长度 16
	buf.WriteString("GeoLite2-Country")
	buf.WriteByte(0x40 | byte(len("record_size"))) // utf8 string，长度 12
	buf.WriteString("record_size")
	buf.WriteByte(0xA1) // uint16，1 字节载荷
	buf.WriteByte(24)   // record_size = 24
	return buf.Bytes()
}

// TestDownloadRedirectPolicy 缺陷猎捕 important#2：零配置 http.Client 会跟随
// 任意重定向。策略修复后：非 https 的重定向目标一律拒绝（统一 scheme 闸门，
// 同时覆盖跨主机 http→http 与 https→http 降级两类），跳数 ≤3。
// 注意：恶意目标必须可达——否则"连接拒绝"会让无修复的实现也假性通过。
func TestDownloadRedirectPolicy(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("evil-content"))
	}))
	defer evil.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 重定向到另一主机（端口不同即不同 authority），且目标可达
		http.Redirect(w, r, evil.URL+"/evil.mmdb", http.StatusFound)
	}))
	defer srv.Close()

	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", srv.URL)
	dir := t.TempDir()
	dest := filepath.Join(dir, "db.mmdb")
	err := EnsureDownloaded(dest)
	if err == nil {
		t.Fatal("expected cross-host/insecure redirect to be rejected")
	}
	if !strings.Contains(err.Error(), "refusing insecure redirect") {
		t.Fatalf("expected redirect policy rejection, got: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("nothing should be written to dest on rejected redirect")
	}
}

// TestDownloadSizeLimit 缺陷猎捕 minor#3：io.Copy 无上限可写满磁盘。
// 修复后：超过 64MiB 上限即失败并清理临时文件，不留 *.tmp 垃圾。
func TestDownloadSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1024*1024) // 1MB × 128 次写出 → 128MB，chunked 无 Content-Length
		for i := 0; i < 128; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()

	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", srv.URL)
	dir := t.TempDir()
	dest := filepath.Join(dir, "db.mmdb")
	err := EnsureDownloaded(dest)
	if err == nil {
		t.Fatal("expected oversized download to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("expected size-limit rejection, got: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("oversized download must not land at dest")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) > 0 {
		t.Fatalf("temp files must be cleaned up on rejection, found: %v", leftovers)
	}
}

// TestTmpIsNotSymlinkFollowing 缺陷猎捕 minor#4：固定 .tmp 名 + os.Create 跟随
// 符号链接。修复后：随机临时文件（O_EXCL）→ 预置符号链接失效，victim 不被写，
// 落地文件为常规文件而非符号链接。下载载荷须是合法 mmdb（落地校验通过）。
func TestTmpIsNotSymlinkFollowing(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "db.mmdb.tmp")); err != nil {
		t.Fatal(err)
	}

	payload := minimalMMDB()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", srv.URL)
	if err := EnsureDownloaded(filepath.Join(dir, "db.mmdb")); err != nil {
		t.Fatalf("EnsureDownloaded: %v", err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "ORIGINAL" {
		t.Fatalf("symlink .tmp was followed — victim content = %q (err=%v)", got, err)
	}
	fi, err := os.Lstat(filepath.Join(dir, "db.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("dest must be a regular file, got mode %v", fi.Mode())
	}
}
