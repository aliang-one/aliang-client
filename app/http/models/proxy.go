package models

// ProxySetRequest is the request body for setting current proxy
type ProxySetRequest struct {
	Name string `json:"name"`
}

// ProxyInfo contains information about a proxy
type ProxyInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Addr string `json:"addr"`
}

// ProxyRegistryGetRequest is the request to get specific proxy info
type ProxyRegistryGetRequest struct {
	Name string `json:"name"`
}

// ProxyRegistryRegisterRequest is the request body for registering a proxy
type ProxyRegistryRegisterRequest struct {
	Name   string      `json:"name"`
	Config interface{} `json:"config"`
}

// ProxyRegistryUnregisterRequest is the request body for unregistering a proxy
type ProxyRegistryUnregisterRequest struct {
	Name string `json:"name"`
}

// ProxyRegistrySetDefaultRequest is the request body for setting default proxy
type ProxyRegistrySetDefaultRequest struct {
	Name string `json:"name"`
}

// ProxyRegistrySwitchRequest is the request body for switching proxy
type ProxyRegistrySwitchRequest struct {
	Name string `json:"name"`
}
