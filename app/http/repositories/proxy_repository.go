package repositories

import (
	"errors"

	proxyRegistry "aliang.one/nursorgate/outbound"
)

// ProxyRepositoryImpl provides access to proxy functionality
type ProxyRepositoryImpl struct{}

// NewProxyRepository creates a new proxy repository instance
func NewProxyRepository() *ProxyRepositoryImpl {
	return &ProxyRepositoryImpl{}
}

// GetCurrentProxy gets the current default proxy (backward compatibility)
func (pr *ProxyRepositoryImpl) GetCurrentProxy() (map[string]interface{}, error) {
	registry := proxyRegistry.GetRegistry()
	currentProxy, err := registry.GetHardcodedDefault()
	if err != nil {
		return map[string]interface{}{
			"error": "No proxy configured",
		}, nil
	}

	return map[string]interface{}{
		"name": "direct",
		"type": currentProxy.Proto().String(),
		"addr": currentProxy.Addr(),
	}, nil
}

// SetCurrentProxy sets the current default proxy (backward compatibility)
func (pr *ProxyRepositoryImpl) SetCurrentProxy(name string) (map[string]interface{}, error) {
	_ = name
	return nil, errors.New("setting current proxy is no longer supported")
}

// ListProxies lists all registered proxies
func (pr *ProxyRepositoryImpl) ListProxies() (map[string]interface{}, error) {
	registry := proxyRegistry.GetRegistry()
	info := registry.ListWithInfo()
	return map[string]interface{}{
		"proxies": info,
		"count":   len(info),
	}, nil
}

// GetProxy gets a specific proxy by name
func (pr *ProxyRepositoryImpl) GetProxy(name string) (interface{}, error) {
	registry := proxyRegistry.GetRegistry()
	info := registry.ListWithInfo()
	proxyInfo, exists := info[name]
	if !exists {
		return nil, errors.New("proxy not found")
	}
	return proxyInfo, nil
}

// Unregister removes a proxy from the registry
func (pr *ProxyRepositoryImpl) Unregister(name string) error {
	registry := proxyRegistry.GetRegistry()
	return registry.Unregister(name)
}

// GetByName gets a specific proxy by name
func (pr *ProxyRepositoryImpl) GetByName(name string) (interface{}, error) {
	if name == "" {
		return nil, errors.New("proxy name cannot be empty")
	}

	registry := proxyRegistry.GetRegistry()
	return registry.Get(name)
}
