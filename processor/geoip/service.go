package geoip

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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

// geoipDownloadURL 返回生效下载地址（env 可覆盖）。
func geoipDownloadURL() string {
	if u := os.Getenv("ALIANG_GEOIP_DOWNLOAD_URL"); u != "" {
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
// 并发调用会因共享 .tmp 临时文件互相碰撞并可能持久化损坏文件。
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

// downloadDatabase 从指定 URL 下载 GeoIP 数据库到目标路径
func downloadDatabase(url, destPath string) error {
	// 创建 HTTP 客户端，设置超时
	client := &http.Client{
		Timeout: 5 * time.Minute, // 5 分钟超时，数据库文件可能比较大
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

	// 创建临时文件
	tempFile := destPath + ".tmp"
	out, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file %s: %w", tempFile, err)
	}
	defer out.Close()

	// 写入数据，显示下载进度
	written, err := io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(tempFile) // 清理临时文件
		return fmt.Errorf("failed to save database: %w", err)
	}

	logger.Debug(fmt.Sprintf("Downloaded %d bytes", written))

	// 关闭文件句柄
	out.Close()

	// 重命名临时文件为最终文件
	if err := os.Rename(tempFile, destPath); err != nil {
		os.Remove(tempFile) // 清理临时文件
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
