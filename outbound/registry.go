package outbound

import (
	"fmt"
	"net"
	"sync"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/aliang"
	"aliang.one/nursorgate/outbound/proxy/direct"
	httpproxy "aliang.one/nursorgate/outbound/proxy/http"
	"aliang.one/nursorgate/outbound/proxy/socks5"
	sshout "aliang.one/nursorgate/outbound/proxy/ssh"
	proxyConfig "aliang.one/nursorgate/processor/config"
)

// Registry 代理注册中心，线程安全
type Registry struct {
	mu      sync.RWMutex
	proxies map[string]proxy.Proxy // 代理实例映射，key 为代理名称
}

var (
	globalRegistry *Registry
	once           sync.Once
)

// GetRegistry 获取全局代理注册中心（单例）
func GetRegistry() *Registry {
	once.Do(func() {
		globalRegistry = &Registry{
			proxies: make(map[string]proxy.Proxy),
		}
	})
	return globalRegistry
}

// RegisterDefault 注册默认的 direct 代理
// 如果已经存在，则不覆盖
func (r *Registry) RegisterDefault() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.proxies["direct"]; exists {
		return nil
	}

	directProxy := direct.NewDirect()
	r.proxies["direct"] = directProxy
	logger.Debug("Default direct proxy registered")
	return nil
}

// RegisterAliang 注册默认的 aliang 代理
func (r *Registry) RegisterAliang(serverAddr string) error {
	r.mu.RLock()
	if _, exists := r.proxies["aliang"]; exists {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	return r.RefreshAliang(serverAddr)
}

// RefreshAliang rebuilds the Aliang outbound entry from the latest core config.
func (r *Registry) RefreshAliang(serverAddr string) error {
	if serverAddr == "" {
		serverAddr = "ai-gateway.aliang.one:443"
		logger.Debug("Using default aliang server address")
	}

	config := aliang.DefaultConfig(serverAddr)
	aliangProxy, err := aliang.NewAliang(config)
	if err != nil {
		return fmt.Errorf("failed to create aliang proxy: %w", err)
	}

	r.mu.Lock()
	oldProxy := r.proxies["aliang"]
	r.proxies["aliang"] = aliangProxy
	r.mu.Unlock()

	closeProxyIfSupported(oldProxy)
	logger.Debug(fmt.Sprintf("Aliang proxy refreshed (server: %s)", serverAddr))
	return nil
}

// CreateSocksProxy creates a SOCKS5 proxy instance from address and optional auth.
func CreateSocksProxy(addr, username, password string) (proxy.Proxy, error) {
	if addr == "" {
		return nil, fmt.Errorf("socks proxy addr cannot be empty")
	}
	return socks5.New(addr, username, password)
}

// CreateHTTPProxy creates an HTTP CONNECT proxy instance from address and optional auth.
func CreateHTTPProxy(addr, username, password string) (proxy.Proxy, error) {
	if addr == "" {
		return nil, fmt.Errorf("http proxy addr cannot be empty")
	}
	return httpproxy.NewHTTP(addr, username, password)
}

// RefreshCustomerProxy rebuilds the runtime customer proxy entries from config.
// It removes stale SOCKS/HTTP/SSH entries first so type/address/auth changes take
// effect immediately after a customer config commit.
func (r *Registry) RefreshCustomerProxy(cfg *proxyConfig.Config) error {
	customerProxy, err := cfg.EffectiveCustomerProxy()
	if err != nil {
		return fmt.Errorf("invalid customer proxy config: %w", err)
	}

	r.UnregisterCustomerProxies()
	if customerProxy == nil {
		logger.Debug("No customer proxy configured")
		return nil
	}

	switch proxyCfg := customerProxy.(type) {
	case *proxyConfig.Socks5Config:
		if err := proxyCfg.Validate(); err != nil {
			return fmt.Errorf("invalid socks proxy config: %w", err)
		}

		addr := net.JoinHostPort(proxyCfg.Server, fmt.Sprintf("%d", proxyCfg.ServerPort))
		socksProxy, err := CreateSocksProxy(addr, proxyCfg.Username, proxyCfg.Password)
		if err != nil {
			return fmt.Errorf("failed to create socks proxy: %w", err)
		}

		if err := r.Register("socks", socksProxy); err != nil {
			return fmt.Errorf("failed to register socks proxy: %w", err)
		}

		logger.Debug(fmt.Sprintf("SOCKS proxy registered at %s", addr))
	case *proxyConfig.HTTPProxyConfig:
		if err := proxyCfg.Validate(); err != nil {
			return fmt.Errorf("invalid http proxy config: %w", err)
		}

		addr := net.JoinHostPort(proxyCfg.Server, fmt.Sprintf("%d", proxyCfg.ServerPort))
		httpProxy, err := CreateHTTPProxy(addr, proxyCfg.Username, proxyCfg.Password)
		if err != nil {
			return fmt.Errorf("failed to create http proxy: %w", err)
		}

		if err := r.Register("http", httpProxy); err != nil {
			return fmt.Errorf("failed to register http proxy: %w", err)
		}

		logger.Debug(fmt.Sprintf("HTTP proxy registered at %s", addr))
	case *proxyConfig.SSHProxyConfig:
		if err := proxyCfg.Validate(); err != nil {
			return fmt.Errorf("invalid ssh proxy config: %w", err)
		}
		addr := net.JoinHostPort(proxyCfg.Server, fmt.Sprintf("%d", proxyCfg.ServerPort))
		hostsDir, err := cache.GetCacheSubdir("ssh")
		if err != nil {
			return fmt.Errorf("resolve ssh known_hosts dir: %w", err)
		}
		final := sshout.NodeFromAddrPort(proxyCfg.Server, proxyCfg.ServerPort, proxyCfg.Username,
			proxyCfg.Password, proxyCfg.PrivateKey, proxyCfg.KeyPassphrase)
		jumps := make([]sshout.NodeConfig, 0, len(proxyCfg.JumpServers))
		for _, j := range proxyCfg.JumpServers {
			jumps = append(jumps, sshout.NodeFromAddrPort(j.Server, j.ServerPort, j.Username, j.Password, j.PrivateKey, j.KeyPassphrase))
		}
		sshProxy, err := sshout.New(final, proxyCfg.PoolSize, jumps, hostsDir)
		if err != nil {
			return fmt.Errorf("failed to create ssh proxy: %w", err)
		}
		if err := r.Register("ssh", sshProxy); err != nil {
			return fmt.Errorf("failed to register ssh proxy: %w", err)
		}
		// 日志打钳位后的有效池大小(钳位权威在 ssh.New:0→3、<1→1、>16→16);
		// 否则未设 pool_size 时日志说 pool=0,而实际池是 3。
		effectivePool := proxyCfg.PoolSize
		if effectivePool == 0 {
			effectivePool = 3
		}
		if effectivePool < 1 {
			effectivePool = 1
		}
		if effectivePool > 16 {
			effectivePool = 16
		}
		logger.Debug(fmt.Sprintf("SSH proxy registered at %s (pool=%d, jumps=%d)", addr, effectivePool, len(jumps)))
	default:
		return fmt.Errorf("unsupported customer proxy config type %T", customerProxy)
	}

	return nil
}

// Register 注册一个代理实例
func (r *Registry) Register(name string, p proxy.Proxy) error {
	if name == "" {
		return fmt.Errorf("proxy name cannot be empty")
	}
	if p == nil {
		return fmt.Errorf("proxy instance cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.proxies[name]; exists {
		logger.Warn(fmt.Sprintf("Proxy '%s' already exists, will be replaced", name))
	}

	r.proxies[name] = p
	logger.Debug(fmt.Sprintf("Proxy '%s' registered successfully (type: %s, addr: %s)",
		name, p.Proto().String(), p.Addr()))
	return nil
}

// Unregister 注销一个代理
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.proxies[name]; !exists {
		return fmt.Errorf("proxy '%s' not found", name)
	}

	delete(r.proxies, name)
	logger.Debug(fmt.Sprintf("Proxy '%s' unregistered", name))
	return nil
}

// UnregisterCustomerProxies removes the dynamic customer proxy entries.
// 被移除的实例若实现 Close(如 SSH 的链池)则一并关闭——否则配置刷新或停用后,
// 旧实例已脱离 registry 但链与监控协程仍活着,泄漏直至进程退出(socks/http
// 无 Close 方法,closeProxyIfSupported 对它们是 no-op)。
func (r *Registry) UnregisterCustomerProxies() {
	r.mu.Lock()
	removed := make([]proxy.Proxy, 0, 3)
	for _, name := range []string{"socks", "http", "ssh"} {
		if p, exists := r.proxies[name]; exists {
			removed = append(removed, p)
			delete(r.proxies, name)
		}
	}
	r.mu.Unlock()

	for _, p := range removed {
		closeProxyIfSupported(p)
	}
	logger.Debug("Customer proxy entries unregistered")
}

func closeProxyIfSupported(p proxy.Proxy) {
	if p == nil {
		return
	}
	if closeable, ok := p.(interface{ Close() error }); ok {
		if err := closeable.Close(); err != nil {
			logger.Warn(fmt.Sprintf("failed to close replaced proxy %s: %v", p.Addr(), err))
		}
	}
}

// startHealthMonitorIfSupported starts the link self-heal loop on proxies that
// expose one (the aliang outbound). Paired with closeProxyIfSupported so every
// refresh both retires the old monitor (via Close) and starts a fresh one.
func startHealthMonitorIfSupported(p proxy.Proxy) {
	if p == nil {
		return
	}
	if starter, ok := p.(interface{ StartHealthMonitor() }); ok {
		starter.StartHealthMonitor()
	}
}

// StartAliangHealthMonitor starts the aliang outbound's background link
// self-heal loop. Called from the production refresh path after RefreshAliang
// so the monitor rides along with the current aliang instance across config
// changes; tests that exercise the registry directly stay network-free.
func (r *Registry) StartAliangHealthMonitor() {
	r.mu.RLock()
	p := r.proxies["aliang"]
	r.mu.RUnlock()
	startHealthMonitorIfSupported(p)
}

// Get 根据名称获取代理实例
func (r *Registry) Get(name string) (proxy.Proxy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, exists := r.proxies[name]
	if exists {
		return p, nil
	}
	return nil, fmt.Errorf("proxy '%s' not found", name)
}

// GetHardcodedDefault 始终返回 direct 代理作为硬编码的默认值
func (r *Registry) GetHardcodedDefault() (proxy.Proxy, error) {
	return r.Get("direct")
}

// GetAliang 获取 aliang 代理
func (r *Registry) GetAliang() (proxy.Proxy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, exists := r.proxies["aliang"]
	if !exists {
		return nil, fmt.Errorf("aliang proxy not found, please register it first")
	}
	return p, nil
}

// List 列出所有已注册的代理名称
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.proxies))
	for name := range r.proxies {
		names = append(names, name)
	}
	return names
}

// ListWithInfo 列出所有代理及其信息
func (r *Registry) ListWithInfo() map[string]ProxyInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info := make(map[string]ProxyInfo)
	for name, p := range r.proxies {
		info[name] = ProxyInfo{
			Name:       name,
			Type:       p.Proto().String(),
			Addr:       p.Addr(),
			Latency:    0,
			LastUpdate: 0,
			Status:     "unknown",
		}
	}
	return info
}

// ProxyInfo 代理信息
type ProxyInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Addr       string `json:"addr"`
	Latency    int64  `json:"latency"`
	LastUpdate int64  `json:"last_update"`
	Status     string `json:"status"`
}

// Count 返回已注册的代理数量
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.proxies)
}

// Clear 清空所有代理（谨慎使用）
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.proxies = make(map[string]proxy.Proxy)
	logger.Warn("All proxies cleared")
}

// GetProxyConfigInfo retrieves complete proxy configuration information from global configuration.
func (r *Registry) GetProxyConfigInfo(proxyName string) (map[string]interface{}, error) {
	return proxyConfig.GetProxyConfigInfo(proxyName)
}
