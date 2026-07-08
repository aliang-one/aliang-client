package client

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	cert_config "aliang.one/nursorgate/processor/cert"
	"aliang.one/nursorgate/processor/cert/generator"
)

// CAManager 是 MITM CA 的单一管理组件。
//
// 设计目标：消除「装证书读文件 vs MITM 读内存缓存」的不一致。文件（mitm-ca.pem）是
// 唯一 source of truth，内存只是缓存，必须追随文件：
//   - Get() 每次校验文件 mtime，未变化命中缓存，变化则自动重载 —— 调用方无需关心刷新
//   - Reload() 主动重载（装证书/重新生成 CA 后调），即时生效，不等下次 Get
//   - CA 重载时联动清空 certCache（域名证书是旧 CA 签的，必须作废）
//
// 这样无论 mitm-ca.pem 因重装/重新生成/升级何时变化，内存都能第一时间感知，不再依赖
// 重启进程。
type CAManager struct {
	certPath string
	keyPath  string

	mu          sync.RWMutex
	cached      *tls.Certificate
	cachedMTime time.Time // 加载时的文件 mtime；零值表示未加载

	genMu sync.Mutex // 生成 CA 的互斥，防并发重复生成
}

// defaultCAManager 是进程级唯一实例，init 时绑定 mitm-ca 路径。
var defaultCAManager *CAManager

func init() {
	certPath, err := cert_config.GetCertPath(cert_config.CertTypeMitmCA)
	if err != nil {
		logger.Error(fmt.Sprintf("CAManager init: resolve cert path failed: %v", err))
		return
	}
	keyPath, err := cert_config.GetCertKeyPath(cert_config.CertTypeMitmCA)
	if err != nil {
		logger.Error(fmt.Sprintf("CAManager init: resolve key path failed: %v", err))
		return
	}
	defaultCAManager = &CAManager{certPath: certPath, keyPath: keyPath}
}

// DefaultCAManager 返回进程级 CAManager（MITM CA 的唯一入口）。
func DefaultCAManager() *CAManager { return defaultCAManager }

// Get 返回 MITM CA。校验文件 mtime：命中缓存则直接返回，变化则自动重载；文件不存在
// 则生成新 CA。这是「内存追随文件」的核心兜底——即使没人调 Reload，下次取 CA 也能
// 感知文件变化。
func (m *CAManager) Get() (*tls.Certificate, error) {
	if m == nil {
		return nil, fmt.Errorf("CAManager not initialized")
	}
	fi, err := os.Stat(m.certPath)
	if os.IsNotExist(err) {
		return m.regenerate()
	}
	if err != nil {
		return nil, fmt.Errorf("stat MITM CA cert: %w", err)
	}

	m.mu.RLock()
	if m.cached != nil && m.cachedMTime.Equal(fi.ModTime()) {
		c := m.cached
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()
	return m.reload(fi.ModTime())
}

// Reload 强制从文件重载 CA 并清空域名证书缓存。装证书 / 重新生成 CA 后主动调用，
// 确保即时生效（不等下次 Get）。文件不存在时触发生成。
func (m *CAManager) Reload() error {
	if m == nil {
		return fmt.Errorf("CAManager not initialized")
	}
	fi, err := os.Stat(m.certPath)
	if os.IsNotExist(err) {
		_, err := m.regenerate()
		return err
	}
	if err != nil {
		return fmt.Errorf("stat MITM CA cert: %w", err)
	}
	_, err = m.reload(fi.ModTime())
	return err
}

// reload 从文件重载 CA（按 mtime），重载后联动清空 certCache。
// 持写锁；double-check mtime 防并发重复加载。
func (m *CAManager) reload(mtime time.Time) (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil && m.cachedMTime.Equal(mtime) {
		return m.cached, nil
	}
	cert, err := tls.LoadX509KeyPair(m.certPath, m.keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load MITM CA certificate: %w", err)
	}
	m.cached = &cert
	m.cachedMTime = mtime
	clearCertCache()
	logger.Info(fmt.Sprintf("CAManager: reloaded MITM CA (mtime=%s); host cert cache cleared", mtime.Format(time.RFC3339)))
	return m.cached, nil
}

// regenerate 文件不存在时生成新 CA，再重载。genMu 防并发重复生成。
func (m *CAManager) regenerate() (*tls.Certificate, error) {
	m.genMu.Lock()
	defer m.genMu.Unlock()
	// double-check：并发场景下可能已被另一个 goroutine 生成
	if fi, err := os.Stat(m.certPath); err == nil {
		return m.reload(fi.ModTime())
	}
	logger.Warn("MITM CA certificate not found, generating new one")
	config := cert_config.GetCertConfig("mitm-ca")
	if config == nil {
		return nil, fmt.Errorf("MITM CA configuration not found")
	}
	if err := generator.GenerateCertificateFromConfig(config, m.certPath); err != nil {
		return nil, fmt.Errorf("failed to generate MITM CA certificate: %w", err)
	}
	fi, err := os.Stat(m.certPath)
	if err != nil {
		return nil, fmt.Errorf("stat after generate: %w", err)
	}
	return m.reload(fi.ModTime())
}

// clearCertCache 清空所有域名证书缓存。CA 变化时调用——旧 CA 签的域名证书对新 CA
// 来说全是无效的，必须整体作废，下次握手用新 CA 重签。
func clearCertCache() {
	certCache.Range(func(key, _ any) bool {
		certCache.Delete(key)
		certAccessTime.Delete(key)
		return true
	})
}
