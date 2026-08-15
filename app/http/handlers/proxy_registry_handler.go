package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/repositories"
	"aliang.one/nursorgate/outbound"
	proxyConfig "aliang.one/nursorgate/processor/config"
)

// ProxyRegistryHandler handles HTTP requests for proxy registry operations
type ProxyRegistryHandler struct {
	proxyRepository *repositories.ProxyRepositoryImpl
}

// NewProxyRegistryHandler creates a new proxy registry handler instance with dependency injection
func NewProxyRegistryHandler(proxyRepository *repositories.ProxyRepositoryImpl) *ProxyRegistryHandler {
	return &ProxyRegistryHandler{
		proxyRepository: proxyRepository,
	}
}

// HandleProxyRegistryList handles GET /api/proxy/registry/list
func (prh *ProxyRegistryHandler) HandleProxyRegistryList(w http.ResponseWriter, r *http.Request) {
	result, err := prh.proxyRepository.ListProxies()
	if err != nil {
		common.ErrorInternalServer(w, "Failed to list proxies", nil)
		return
	}

	registry := outbound.GetRegistry()
	responseData := map[string]interface{}{
		"proxies": result["proxies"],
		"count":   result["count"],
	}

	if defaultProxy, err := registry.GetHardcodedDefault(); err == nil {
		responseData["current_proxy"] = map[string]interface{}{
			"name": "direct",
			"type": defaultProxy.Proto().String(),
			"addr": defaultProxy.Addr(),
		}
	}

	common.Success(w, responseData)
}

// HandleProxyRegistryGet handles GET /api/proxy/registry/get
// Query parameter: name (required) - get specific proxy by name
// Supported formats:
//   - "direct" - direct proxy
//   - "aliang" - aliang proxy
//   - "custom_name" - custom proxy
func (prh *ProxyRegistryHandler) HandleProxyRegistryGet(w http.ResponseWriter, r *http.Request) {
	name := common.GetQueryParamString(r, "name", "")
	if name == "" {
		common.ErrorBadRequest(w, "name parameter is required", nil)
		return
	}

	// First, try to get complete configuration information from global config
	configInfo, err := proxyConfig.GetProxyConfigInfo(name)
	if err != nil {
		// If not found in config, try to get from registry (for dynamically created proxies)
		proxyInstance, repoErr := prh.proxyRepository.GetByName(name)
		if repoErr != nil {
			common.ErrorNotFound(w, "proxy not found in configuration or registry")
			return
		}

		// Build fallback proxy info from runtime instance
		proxyInfo := map[string]interface{}{
			"name": name,
		}

		// Get basic info from proxy instance
		if baseProxy, ok := proxyInstance.(interface {
			Addr() string
			Proto() interface{ String() string }
		}); ok {
			proxyInfo["type"] = baseProxy.Proto().String()
			proxyInfo["addr"] = baseProxy.Addr()
		} else {
			proxyInfo["type"] = "unknown"
			proxyInfo["error"] = "could not extract proxy details"
		}
		proxyInfo["source"] = "runtime"

		common.Success(w, proxyInfo)
		return
	}

	// Also fetch runtime proxy info to include address and runtime details
	registry := outbound.GetRegistry()
	proxyInstance, err := registry.Get(name)
	if err == nil && proxyInstance != nil {
		// Add runtime information to config info
		configInfo["addr"] = proxyInstance.Addr()
		configInfo["proto"] = proxyInstance.Proto().String()
	}

	common.Success(w, configInfo)
}
