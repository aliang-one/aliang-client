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
	// DefaultGeoIPDownloadURL 默认的 GeoIP 数据库下载地址
	DefaultGeoIPDownloadURL  = "https://git.io/GeoLite2-Country.mmdb"
	DefaultGeoIPDatabaseFile = "GeoLite2-Country.mmdb"
)

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

// LoadDatabase loads the MaxMind GeoLite2 database from the specified path
// If the database file doesn't exist, it will automatically download from DefaultGeoIPDownloadURL
// Supports ~ expansion (e.g., ~/.aliang/GeoLite2-Country.mmdb)
func (s *Service) LoadDatabase(path string) error {
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

	// 检查数据库文件是否存在
	if _, err := os.Stat(expandedPath); os.IsNotExist(err) {
		logger.Info(fmt.Sprintf("GeoIP database not found at %s, downloading from %s", expandedPath, DefaultGeoIPDownloadURL))

		// 确保目标目录存在
		dir := filepath.Dir(expandedPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}

		// 下载数据库文件
		if err := downloadDatabase(DefaultGeoIPDownloadURL, expandedPath); err != nil {
			return fmt.Errorf("failed to download GeoIP database: %w", err)
		}

		logger.Info(fmt.Sprintf("GeoIP database downloaded successfully to %s", expandedPath))
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
