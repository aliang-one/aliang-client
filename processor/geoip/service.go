package geoip

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"github.com/oschwald/geoip2-golang"
)

// Service provides GeoIP lookup functionality using MaxMind GeoLite2 database
type Service struct {
	mu      sync.RWMutex
	reader  *geoip2.Reader
	enabled bool
	dbPath  string
}

const (
	// DefaultGeoIPDownloadURL 用户自有 COS 分发（git.io 短链已失效）。
	DefaultGeoIPDownloadURL  = "https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/static/GeoLite2-Country.mmdb"
	DefaultGeoIPDatabaseFile = "GeoLite2-Country.mmdb"
)

// downloadURLOverride 保存配置层（customer.geoip.download_url）注入的下载源。
// 本包不 import config（依赖方向：config → geoip），注入由 config 包在
// Validate/Apply 成功后经 SetDownloadURLOverride 完成。
var downloadURLOverride atomic.Value // string

// SetDownloadURLOverride 注入配置层下载源（空串 = 清除注入，回落默认 COS 源）。
// env（ALIANG_GEOIP_DOWNLOAD_URL，部署面覆盖）优先级仍最高——env 存在时本值被忽略。
func SetDownloadURLOverride(u string) {
	downloadURLOverride.Store(strings.TrimSpace(u))
}

// EffectiveDownloadURL 返回当前生效下载源（env > config 注入 > 默认），供诊断观测。
func EffectiveDownloadURL() string {
	return geoipDownloadURL()
}

// geoipDownloadURL 返回生效下载地址（env > config 注入 > 默认）。
func geoipDownloadURL() string {
	if u := os.Getenv("ALIANG_GEOIP_DOWNLOAD_URL"); u != "" {
		return u
	}
	if u, _ := downloadURLOverride.Load().(string); u != "" {
		return u
	}
	return DefaultGeoIPDownloadURL
}

var (
	defaultService *Service
	once           sync.Once
)

// GetService returns the singleton GeoIP service instance
func GetService() *Service {
	once.Do(func() {
		defaultService = &Service{
			enabled: false,
		}
	})
	return defaultService
}

// DefaultDatabasePath returns the canonical path for the local GeoIP database.
func DefaultDatabasePath() (string, error) {
	geoipDir, err := cache.GetCacheSubdir("geoip")
	if err != nil {
		return "", fmt.Errorf("failed to resolve geoip directory: %w", err)
	}
	return filepath.Join(geoipDir, DefaultGeoIPDatabaseFile), nil
}

// downloadMu 是下载互斥（single-flight）：EnsureDownloaded 已导出为编排积木，
// 并发调用会重复触发完整下载（随机临时文件名虽不再互相碰撞，但重复下载
// 浪费带宽且装载窗口拉长）。
var downloadMu sync.Mutex

// EnsureDownloaded 确保本地存在数据库文件；缺失则下载（绝不持 s.mu）。
// 支持 ~ 展开；path 为空时使用 DefaultDatabasePath；并发调用 single-flight。
func EnsureDownloaded(path string) error {
	if path == "" {
		defaultPath, err := DefaultDatabasePath()
		if err != nil {
			return err
		}
		path = defaultPath
	}

	// 展开 ~ 路径（例如 ~/.aliang/GeoLite2-Country.mmdb）
	expandedPath, err := cache.ExpandHomePath(path)
	if err != nil {
		return fmt.Errorf("failed to expand path %s: %w", path, err)
	}

	// 已存在则零网络直接返回（无锁快路径）
	if _, err := os.Stat(expandedPath); err == nil {
		return nil
	}

	downloadMu.Lock()
	defer downloadMu.Unlock()

	// double-check：拿到锁后再查一次（可能别的调用者已下载完）
	if _, err := os.Stat(expandedPath); err == nil {
		return nil
	}

	logger.Info(fmt.Sprintf("GeoIP database not found at %s, downloading from %s", expandedPath, geoipDownloadURL()))

	// 确保目标目录存在
	dir := filepath.Dir(expandedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// 下载数据库文件
	if err := downloadFunc(geoipDownloadURL(), expandedPath); err != nil {
		return fmt.Errorf("failed to download GeoIP database: %w", err)
	}

	logger.Info(fmt.Sprintf("GeoIP database downloaded successfully to %s", expandedPath))
	return nil
}

// LoadLocal 仅打开本地数据库并原子替换 reader；缺失即错、不下载。
// 持 s.mu 只做本地 IO 与 reader 原子 swap。path 为空时使用 DefaultDatabasePath。
func (s *Service) LoadLocal(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if path == "" {
		defaultPath, err := DefaultDatabasePath()
		if err != nil {
			return err
		}
		path = defaultPath
	}

	// 展开 ~ 路径（例如 ~/.aliang/GeoLite2-Country.mmdb）
	expandedPath, err := cache.ExpandHomePath(path)
	if err != nil {
		return fmt.Errorf("failed to expand path %s: %w", path, err)
	}

	logger.Debug(fmt.Sprintf("Loading GeoIP database from: %s (expanded from: %s)", expandedPath, path))

	// 缺失即错，不触发下载
	if _, err := os.Stat(expandedPath); os.IsNotExist(err) {
		return fmt.Errorf("GeoIP database not found at %s", expandedPath)
	}

	// 打开数据库
	reader, err := geoip2.Open(expandedPath)
	if err != nil {
		return fmt.Errorf("failed to load GeoIP database from %s: %w", expandedPath, err)
	}

	// Close old reader if exists
	if s.reader != nil {
		s.reader.Close()
	}

	s.reader = reader
	s.dbPath = expandedPath
	s.enabled = true

	return nil
}

// EnsureLoadedAsync 本地已有数据库则同步装载；缺失则后台下载后装载（不阻塞调用方）。
// start/tray/core 三条启动路径共用。返回 nil 仅代表编排已启动。
func (s *Service) EnsureLoadedAsync() error {
	path, err := DefaultDatabasePath()
	if err != nil {
		return err
	}
	expanded, err := cache.ExpandHomePath(path)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(expanded); statErr == nil {
		return s.LoadLocal(path) // 本地 IO，同步完成
	}
	logger.Info("GeoIP database not found, downloading in background: " + geoipDownloadURL())
	go func() {
		if err := EnsureDownloaded(path); err != nil {
			logger.Warn(fmt.Sprintf("GeoIP database background download failed (will retry next startup): %v", err))
			return
		}
		if err := s.LoadLocal(path); err != nil {
			logger.Warn(fmt.Sprintf("GeoIP database load failed after download: %v", err))
			return
		}
		logger.Info("GeoIP database ready; geoip rules now active")
	}()
	return nil
}

// downloadFunc 是下载 seam，测试可替换以避免真实网络。
var downloadFunc = downloadDatabase

// maxDownloadBytes 是单次下载数据库的体积上限（64MiB）。GeoLite2-Country
// 当前约 10MB，留足余量；防止恶意/误配的下载源以无限流写满磁盘分区。
const maxDownloadBytes = 64 << 20

// downloadDatabase 从指定 URL 下载 GeoIP 数据库到目标路径。
// 安全约束：重定向目标必须 https 且至多 3 跳；体积超过 maxDownloadBytes
// 拒绝；临时文件随机名创建（O_EXCL，预置 .tmp 符号链接/同名抢占失效）；
// rename 前用 geoip2.Open 试开校验，损坏内容（如 HTTP 200 的 HTML 错误页）
// 不落地。
func downloadDatabase(url, destPath string) error {
	// 创建 HTTP 客户端，设置超时与重定向策略
	client := &http.Client{
		Timeout: 5 * time.Minute, // 5 分钟超时，数据库文件可能比较大
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			// 初始 URL 可由 env 覆盖（operator 控制），scheme 校验只约束
			// 重定向链：非 https 目标一律拒绝，同时阻断跨主机明文跳转
			// 与 https→http 降级。
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing insecure redirect to %s", req.URL)
			}
			return nil
		},
	}

	// 发送 GET 请求
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status code %d", resp.StatusCode)
	}

	// 服务端已声明体积超限则提前失败，不必拉取
	if resp.ContentLength > maxDownloadBytes {
		return fmt.Errorf("declared download size %d exceeds maximum size of %d bytes",
			resp.ContentLength, maxDownloadBytes)
	}

	// 随机名临时文件（O_EXCL）：不触碰任何已存在路径，预置 .tmp 符号链接
	// 或同名合法 mmdb 的抢占攻击失效（原实现固定名 + os.Create 会跟随符号链接）
	tmp, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file for %s: %w", destPath, err)
	}
	tmpPath := tmp.Name()

	// 写入数据并施加体积上限：请求 maxDownloadBytes+1 字节——读满仍未 EOF
	// 即实际体积超限（CopyN 对更短的 body 返回 io.EOF 表示正常收尾）
	written, err := io.CopyN(tmp, resp.Body, maxDownloadBytes+1)
	if err == nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download exceeds maximum size of %d bytes", maxDownloadBytes)
	}
	if !errors.Is(err, io.EOF) {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to save database: %w", err)
	}

	logger.Debug(fmt.Sprintf("Downloaded %d bytes", written))

	// 关闭文件句柄
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// 落地校验：rename 前试开一次，坏内容不落地（顺带修复「损坏文件无自愈」
	// 缺陷家族的主症状：HTTP-200-HTML 不再砖化）
	validate, err := geoip2.Open(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("downloaded database failed validation: %w", err)
	}
	_ = validate.Close()

	// 重命名临时文件为最终文件
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath) // 清理临时文件
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// LookupCountry performs a country-level GeoIP lookup for the given IP address
func (s *Service) LookupCountry(ip net.IP) (*CountryInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.enabled || s.reader == nil {
		return nil, fmt.Errorf("GeoIP service not initialized")
	}

	record, err := s.reader.Country(ip)
	if err != nil {
		return nil, fmt.Errorf("GeoIP lookup failed for %s: %w", ip.String(), err)
	}

	return &CountryInfo{
		ISOCode: record.Country.IsoCode,
		Name:    record.Country.Names["en"],
	}, nil
}

// IsChina checks if the given IP address is located in China
func (s *Service) IsChina(ip net.IP) bool {
	country, err := s.LookupCountry(ip)
	if err != nil {
		return false
	}
	return country.ISOCode == "CN"
}

// IsEnabled returns whether the GeoIP service is currently enabled
func (s *Service) IsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// GetDatabasePath returns the path to the currently loaded database
func (s *Service) GetDatabasePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbPath
}

// Close closes the GeoIP database reader
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.enabled = false

	if s.reader != nil {
		err := s.reader.Close()
		s.reader = nil
		return err
	}
	return nil
}

// Disable disables the GeoIP service without closing the database
func (s *Service) Disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
}

// Enable enables the GeoIP service if a database is loaded
func (s *Service) Enable() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reader == nil {
		return fmt.Errorf("cannot enable GeoIP service: no database loaded")
	}

	s.enabled = true
	return nil
}
