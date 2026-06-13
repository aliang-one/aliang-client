package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BaseProxyConfig represents a proxy configuration
type BaseProxyConfig struct {
	Type string `json:"type"`
	// Aliang 代理专用
	CoreServer string `json:"core_server,omitempty"`
}

// VLESSConfig represents VLESS protocol configuration
type VLESSConfig struct {
	Server         string   `json:"server_host"`
	ServerPort     uint16   `json:"server_port"`
	UUID           string   `json:"uuid"`
	Flow           string   `json:"flow,omitempty"`
	TLSEnabled     bool     `json:"tls_enabled"`
	RealityEnabled bool     `json:"reality_enabled"`
	SNI            string   `json:"sni,omitempty"`
	PublicKey      string   `json:"public_key,omitempty"`
	ShortIDs       []string `json:"short_ids,omitempty"`
}

// ShadowsocksConfig represents Shadowsocks protocol configuration
type ShadowsocksConfig struct {
	Server     string `json:"server_host"`
	ServerPort uint16 `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
	Username   string `json:"username,omitempty"`
	ObfsMode   string `json:"obfs_mode,omitempty"`
	ObfsHost   string `json:"obfs_host,omitempty"`

	// ShadowTLS plugin support
	Plugin     string               `json:"plugin,omitempty"`
	PluginOpts *ShadowTLSPluginOpts `json:"plugin_opts,omitempty"`
}

// Socks5Config represents SOCKS5 protocol configuration
type Socks5Config struct {
	Server     string `json:"server_host"`
	ServerPort uint16 `json:"server_port"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
}

// HTTPProxyConfig represents HTTP CONNECT proxy configuration.
type HTTPProxyConfig struct {
	Server     string `json:"server_host"`
	ServerPort uint16 `json:"server_port"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
}

// Validate validates SOCKS5 configuration
func (c *Socks5Config) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server_host is required")
	}
	if c.ServerPort == 0 {
		return fmt.Errorf("server_port is required")
	}
	if (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("username and password must be provided together")
	}
	return nil
}

// Validate validates HTTP proxy configuration.
func (c *HTTPProxyConfig) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server_host is required")
	}
	if c.ServerPort == 0 {
		return fmt.Errorf("server_port is required")
	}
	if (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("username and password must be provided together")
	}
	return nil
}

// ShadowTLSPluginOpts represents ShadowTLS plugin configuration
type ShadowTLSPluginOpts struct {
	Host     string `json:"host"`     // TLS camouflage domain (e.g., www.bing.com)
	Password string `json:"password"` // ShadowTLS authentication password
	Version  int    `json:"version"`  // Protocol version (1, 2, or 3)
}

// Validate validates ShadowTLS plugin options
func (o *ShadowTLSPluginOpts) Validate() error {
	if o == nil {
		return fmt.Errorf("plugin_opts is required when plugin='shadow-tls'")
	}
	if o.Host == "" {
		return fmt.Errorf("plugin_opts.host is required")
	}
	if o.Password == "" {
		return fmt.Errorf("plugin_opts.password is required and cannot be empty")
	}
	if len(o.Password) < 8 {
		return fmt.Errorf("plugin_opts.password must be at least 8 characters")
	}
	if o.Version != 1 && o.Version != 2 && o.Version != 3 {
		return fmt.Errorf("plugin_opts.version must be 1, 2, or 3")
	}
	return nil
}

// Validate validates Shadowsocks configuration including plugin settings
func (c *ShadowsocksConfig) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server_host is required")
	}
	if c.ServerPort == 0 {
		return fmt.Errorf("server_port is required")
	}
	if c.Method == "" {
		return fmt.Errorf("method is required")
	}
	if c.Password == "" {
		return fmt.Errorf("password is required")
	}

	// Validate plugin configuration
	if c.Plugin != "" {
		if c.Plugin == "shadow-tls" {
			if c.PluginOpts == nil {
				return fmt.Errorf("plugin_opts is required when plugin='shadow-tls'")
			}
			if err := c.PluginOpts.Validate(); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("unsupported plugin: %s", c.Plugin)
		}
	}

	return nil
}

// Validate validates the proxy configuration
func (c *BaseProxyConfig) Validate() error {
	if c.Type == "" {
		return fmt.Errorf("proxy type is required")
	}

	switch c.Type {
	case "direct":
		// Direct proxy doesn't require additional configuration
		// It connects directly without proxy
	case "aliang":
		// Aliang (mTLS) proxy - CoreServer is optional with default value
		// If not provided, default will be used in registry
	default:
		return fmt.Errorf("unsupported proxy type: %s", c.Type)
	}

	return nil
}

// DNSPreResolutionConfig DNS预解析配置
type DNSPreResolutionConfig struct {
	Enabled           bool   `json:"enabled"`           // 是否启用DNS预解析
	Timeout           string `json:"timeout"`           // 预解析超时时间（如 "10s"）
	ConcurrentLimit   int    `json:"concurrentLimit"`   // 并发解析限制
	RetryOnFailure    bool   `json:"retryOnFailure"`    // 失败时是否重试
	CacheResults      bool   `json:"cacheResults"`      // 是否缓存预解析结果
	PreferIPv4        bool   `json:"preferIPv4"`        // 优先使用IPv4地址
	ForceResolve      bool   `json:"forceResolve"`      // 强制解析（即使是IP也尝试）
	MaxCacheTTL       string `json:"maxCacheTTL"`       // 最大缓存TTL（如 "1h"）
	PrimaryDNS        string `json:"primaryDNS"`        // 主DNS服务器
	FallbackDNS       string `json:"fallbackDNS"`       // 回退DNS服务器
	SystemDNSFallback bool   `json:"systemDNSFallback"` // 是否回退到系统DNS
}

// GetDNSPreResolutionConfig 获取DNS预解析配置
func GetDNSPreResolutionConfig() *DNSPreResolutionConfig {
	return &DNSPreResolutionConfig{
		Enabled:           true,
		Timeout:           "10s",
		ConcurrentLimit:   10,
		RetryOnFailure:    true,
		CacheResults:      true,
		PreferIPv4:        true,
		ForceResolve:      false,
		MaxCacheTTL:       "1h",
		PrimaryDNS:        "8.8.8.8:53",
		FallbackDNS:       "223.5.5.5:53",
		SystemDNSFallback: true,
	}
}

// GetTimeout 解析超时时间
func (c *DNSPreResolutionConfig) GetTimeout() time.Duration {
	if c.Timeout == "" {
		return 10 * time.Second
	}
	if duration, err := time.ParseDuration(c.Timeout); err == nil {
		return duration
	}
	return 10 * time.Second
}

// GetMaxCacheTTL 解析最大缓存TTL
func (c *DNSPreResolutionConfig) GetMaxCacheTTL() time.Duration {
	if c.MaxCacheTTL == "" {
		return 1 * time.Hour
	}
	if duration, err := time.ParseDuration(c.MaxCacheTTL); err == nil {
		return duration
	}
	return 1 * time.Hour
}

// GetPrimaryDNS 获取主DNS服务器
func (c *DNSPreResolutionConfig) GetPrimaryDNS() string {
	if c.PrimaryDNS == "" {
		return "8.8.8.8:53"
	}
	return c.PrimaryDNS
}

// GetFallbackDNS 获取回退DNS服务器
func (c *DNSPreResolutionConfig) GetFallbackDNS() string {
	if c.FallbackDNS == "" {
		return "223.5.5.5:53"
	}
	return c.FallbackDNS
}

// Validate 验证DNS预解析配置
func (c *DNSPreResolutionConfig) Validate() error {
	if c.Enabled {
		if c.ConcurrentLimit <= 0 {
			return fmt.Errorf("concurrentLimit must be positive when DNS pre-resolution is enabled")
		}
		if c.ConcurrentLimit > 50 {
			return fmt.Errorf("concurrentLimit should not exceed 50")
		}
		if timeout := c.GetTimeout(); timeout < 1*time.Second || timeout > 60*time.Second {
			return fmt.Errorf("timeout should be between 1s and 60s")
		}
	}
	return nil
}

// Config 完整配置结构
type Config struct {
	Core     *CoreConfig     `json:"core,omitempty"`
	Customer *CustomerConfig `json:"customer,omitempty"`

	customerUnknownFields []string            `json:"-"`
	aiRuleUnknownFields   map[string][]string `json:"-"`
}

type CoreConfig struct {
	Engine       *CoreEngineConfig   `json:"engine,omitempty"`
	AliangServer *AliangServerConfig `json:"aliangServer,omitempty"`
	APIServer    string              `json:"api_server,omitempty"`
	AgentServer  string              `json:"agent_server,omitempty"`
}

type CoreEngineConfig struct {
	MTU                      int    `json:"mtu"`
	Mark                     int    `json:"fwmark"`
	RestAPI                  string `json:"restapi"`
	Device                   string `json:"device"`
	LogLevel                 string `json:"loglevel"`
	Interface                string `json:"interface"`
	TCPModerateReceiveBuffer bool   `json:"tcp-moderate-receive-buffer"`
	TCPSendBufferSize        string `json:"tcp-send-buffer-size"`
	TCPReceiveBufferSize     string `json:"tcp-receive-buffer-size"`
	MulticastGroups          string `json:"multicast-groups"`
	TUNPreUp                 string `json:"tun-pre-up"`
	TUNPostUp                string `json:"tun-post-up"`
	UDPTimeout               string `json:"udp-timeout"`
}

// TrafficMirrorConfig controls TCP-level traffic mirroring for matched domains.
type TrafficMirrorConfig struct {
	Enabled bool     `json:"enabled"`
	Target  string   `json:"target"`  // HTTP endpoint to POST StreamChunk to
	Domains []string `json:"domains"` // Domain patterns to mirror
}

// Validate validates traffic mirror configuration
func (c *TrafficMirrorConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Target == "" {
		return fmt.Errorf("traffic_mirror.target is required when enabled")
	}
	if len(c.Domains) == 0 {
		return fmt.Errorf("traffic_mirror.domains must not be empty when enabled")
	}
	return nil
}

const defaultHTTP1DropPathContains = "metric"

// HTTP1DropConfig controls local dropping of matched HTTP/1 requests before
// they are forwarded to the Aliang upstream.
type HTTP1DropConfig struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	PathContains []string `json:"path_contains,omitempty"`
}

func (c *HTTP1DropConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c *HTTP1DropConfig) EffectivePathContains() []string {
	if c == nil || c.PathContains == nil {
		return []string{defaultHTTP1DropPathContains}
	}
	return dedupeTrimmedStrings(c.PathContains)
}

func (c *HTTP1DropConfig) Validate() error {
	if c == nil || !c.IsEnabled() {
		return nil
	}
	for i, pattern := range c.PathContains {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("http1_drop.path_contains[%d] cannot be empty", i)
		}
	}
	return nil
}

type CustomerConfig struct {
	Proxy         *CustomerProxyConfig              `json:"proxy,omitempty"`
	AIRules       map[string]*CustomerAIRuleSetting `json:"ai_rules,omitempty"`
	ProxyRules    []string                          `json:"proxy_rules,omitempty"`
	TrafficMirror *TrafficMirrorConfig              `json:"traffic_mirror,omitempty"`
	HTTP1Drop     *HTTP1DropConfig                  `json:"http1_drop,omitempty"`
}

type AliangServerConfig struct {
	Type        string `json:"type"`
	CoreServer  string `json:"core_server,omitempty"`
	AgentServer string `json:"agent_server,omitempty"`
}

type CustomerProxyConfig struct {
	Enable   *bool  `json:"enable,omitempty"`
	Type     string `json:"type"`
	Server   string `json:"server,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func (c *CustomerProxyConfig) IsEnabled() bool {
	if c == nil {
		return false
	}
	if c.Enable == nil {
		return true
	}
	return *c.Enable
}

type CustomerAIRuleSetting struct {
	Label    string   `json:"label,omitempty"`
	Enble    *bool    `json:"enble,omitempty"`
	Include  []string `json:"include,omitempty"`
	Editable *bool    `json:"editable,omitempty"`
}

func (c *CustomerAIRuleSetting) UnmarshalJSON(data []byte) error {
	type alias struct {
		Label    string   `json:"label,omitempty"`
		Enble    *bool    `json:"enble,omitempty"`
		Enable   *bool    `json:"enable,omitempty"`
		Include  []string `json:"include,omitempty"`
		Exclude  []string `json:"exclude,omitempty"` // legacy alias
		Editable *bool    `json:"editable,omitempty"`
	}

	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	c.Label = decoded.Label
	c.Enble = decoded.Enble
	if c.Enble == nil {
		c.Enble = decoded.Enable
	}
	c.Include = decoded.Include
	if len(c.Include) == 0 {
		c.Include = decoded.Exclude
	}
	c.Editable = decoded.Editable
	return nil
}

func (c CustomerAIRuleSetting) MarshalJSON() ([]byte, error) {
	type alias struct {
		Label    string   `json:"label,omitempty"`
		Enble    *bool    `json:"enble,omitempty"`
		Include  []string `json:"include,omitempty"`
		Editable *bool    `json:"editable,omitempty"`
	}
	return json.Marshal(alias{
		Label:    c.Label,
		Enble:    c.Enble,
		Include:  c.Include,
		Editable: c.Editable,
	})
}

// AIRuleProviderPreset describes a known AI provider that can be configured.
type AIRuleProviderPreset struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	DefaultInclude []string `json:"default_include,omitempty"`
	Editable       bool     `json:"editable"`
}

// PresetAIRuleProviders is the system-known list of AI rule providers.
// Populated by init() in presets.go from the embedded default configuration.
var PresetAIRuleProviders []AIRuleProviderPreset

func (c *Config) UnmarshalJSON(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}

	customerUnknown, aiRuleUnknown, err := extractCustomerUnknownFields(root)
	if err != nil {
		return err
	}

	type configAlias Config
	var decoded configAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*c = Config(decoded)
	c.customerUnknownFields = customerUnknown
	c.aiRuleUnknownFields = aiRuleUnknown
	return nil
}

// GetTokenActivateURL returns the complete Token activation URL
func (c *Config) GetTokenActivateURL() string {
	return fmt.Sprintf("%s/api/user/auth/new/activate", c.APIBaseURL())
}

// GetPlanStatusURL returns the complete Plan status URL
func (c *Config) GetPlanStatusURL() string {
	return fmt.Sprintf("%s/api/user/auth/info/plan/info", c.APIBaseURL())
}

func (c *Config) GetAuthLoginURL() string {
	return fmt.Sprintf("%s/api/v1/auth/login", c.APIBaseURL())
}

func (c *Config) GetAuthRefreshURL() string {
	return fmt.Sprintf("%s/api/v1/auth/refresh", c.APIBaseURL())
}

func (c *Config) GetAuthLogoutURL() string {
	return fmt.Sprintf("%s/api/v1/auth/logout", c.APIBaseURL())
}

func (c *Config) GetAuthMeURL() string {
	return fmt.Sprintf("%s/api/v1/auth/me", c.APIBaseURL())
}

func (c *Config) GetUserProfileURL() string {
	return fmt.Sprintf("%s/api/v1/user/profile", c.APIBaseURL())
}

func (c *Config) GetUserUpdateURL() string {
	return fmt.Sprintf("%s/api/v1/user", c.APIBaseURL())
}

func (c *Config) GetSubscriptionsSummaryURL() string {
	return fmt.Sprintf("%s/api/v1/subscriptions/summary", c.APIBaseURL())
}

func (c *Config) GetSubscriptionsProgressURL() string {
	return fmt.Sprintf("%s/api/v1/subscriptions/progress", c.APIBaseURL())
}

func (c *Config) GetRedeemURL() string {
	return fmt.Sprintf("%s/api/v1/redeem", c.APIBaseURL())
}

func (c *Config) GetUserAPIKeysURL() string {
	return fmt.Sprintf("%s/api/v1/keys", c.APIBaseURL())
}

func (c *Config) GetAvailableGroupsURL() string {
	return fmt.Sprintf("%s/api/v1/groups/available", c.APIBaseURL())
}

// GetInboundsURL returns the complete Inbounds API URL
func (c *Config) GetInboundsURL() string {
	return fmt.Sprintf("%s/api/production/prod/sui/user/sui/inbounds", c.APIBaseURL())
}

// GetRemoteConfigURL returns the complete remote configuration URL
func (c *Config) GetRemoteConfigURL() string {
	return fmt.Sprintf("%s/api/config", c.APIBaseURL())
}

func (c *Config) GetAgentDeviceRegisterURL() string {
	return fmt.Sprintf("%s/api/devices/register", c.AgentBaseURL())
}

func (c *Config) GetAgentDeviceSyncURL(deviceID string) string {
	return fmt.Sprintf("%s/api/v1/agent/devices/%s/sync", c.AgentBaseURL(), url.PathEscape(strings.TrimSpace(deviceID)))
}

func (c *Config) GetAgentDevicesURL() string {
	return fmt.Sprintf("%s/api/devices", c.AgentBaseURL())
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if err := c.validateCustomerEditableSurface(); err != nil {
		return err
	}

	if strings.TrimSpace(c.APIBaseURL()) == "" {
		return fmt.Errorf("core.api_server is required in configuration")
	}

	// Validate DNS pre-resolution configuration
	if dnsCfg := c.EffectiveDNSPreResolution(); dnsCfg != nil {
		if err := dnsCfg.Validate(); err != nil {
			return fmt.Errorf("invalid DNS pre-resolution configuration: %w", err)
		}
	}

	if customerProxy, err := c.EffectiveCustomerProxy(); err != nil {
		return fmt.Errorf("invalid customer proxy configuration: %w", err)
	} else if customerProxy != nil {
		switch typedProxy := customerProxy.(type) {
		case *Socks5Config:
			if err := typedProxy.Validate(); err != nil {
				return fmt.Errorf("invalid socksProxy configuration: %w", err)
			}
		case *HTTPProxyConfig:
			if err := typedProxy.Validate(); err != nil {
				return fmt.Errorf("invalid httpProxy configuration: %w", err)
			}
		}
	}

	if mirror := c.EffectiveTrafficMirror(); mirror != nil {
		if err := mirror.Validate(); err != nil {
			return fmt.Errorf("invalid traffic_mirror configuration: %w", err)
		}
	}
	if http1Drop := c.EffectiveHTTP1Drop(); http1Drop != nil {
		if err := http1Drop.Validate(); err != nil {
			return fmt.Errorf("invalid http1_drop configuration: %w", err)
		}
	}

	return nil
}

func (c *Config) validateCustomerEditableSurface() error {
	if c == nil || c.Customer == nil {
		return nil
	}

	if c.Customer.Proxy != nil {
		switch c.Customer.Proxy.Type {
		case "http", "socks5":
		default:
			return fmt.Errorf("customer.proxy.type must be one of [http socks5], got %q", c.Customer.Proxy.Type)
		}
	}

	for provider, rule := range c.Customer.AIRules {
		if strings.TrimSpace(provider) == "" {
			return fmt.Errorf("customer.ai_rules provider key cannot be empty")
		}
		if rule == nil {
			return fmt.Errorf("customer.ai_rules.%s must be an object with editable fields [enble include editable]", provider)
		}
		if rule.Enble == nil {
			return fmt.Errorf("customer.ai_rules.%s.enble is required and editable", provider)
		}
		for i := range rule.Include {
			rule.Include[i] = strings.TrimSpace(rule.Include[i])
			if rule.Include[i] == "" {
				return fmt.Errorf("customer.ai_rules.%s.include[%d] cannot be empty", provider, i)
			}
		}
	}

	for i := range c.Customer.ProxyRules {
		c.Customer.ProxyRules[i] = strings.TrimSpace(c.Customer.ProxyRules[i])
		if c.Customer.ProxyRules[i] == "" {
			return fmt.Errorf("customer.proxy_rules[%d] cannot be empty", i)
		}
	}

	if err := c.customerUnknownKeyErrors(); err != nil {
		return err
	}

	return nil
}

func (c *Config) customerUnknownKeyErrors() error {
	if len(c.customerUnknownFields) > 0 {
		sorted := append([]string(nil), c.customerUnknownFields...)
		sort.Strings(sorted)
		return fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop]", sorted[0])
	}

	providers := make([]string, 0, len(c.aiRuleUnknownFields))
	for provider := range c.aiRuleUnknownFields {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		unknown := c.aiRuleUnknownFields[provider]
		if len(unknown) == 0 {
			continue
		}
		sortedUnknown := append([]string(nil), unknown...)
		sort.Strings(sortedUnknown)
		return fmt.Errorf("customer.ai_rules.%s.%s is forbidden: editable ai_rules fields are [enble include editable label]", provider, sortedUnknown[0])
	}

	return nil
}

func extractCustomerUnknownFields(root map[string]json.RawMessage) ([]string, map[string][]string, error) {
	unknownCustomer := make([]string, 0)
	unknownAIRules := make(map[string][]string)

	rawCustomer, ok := root["customer"]
	if !ok {
		return unknownCustomer, unknownAIRules, nil
	}

	var customerRoot map[string]json.RawMessage
	if err := json.Unmarshal(rawCustomer, &customerRoot); err != nil {
		return nil, nil, fmt.Errorf("customer must be an object")
	}

	for key := range customerRoot {
		switch key {
		case "proxy", "ai_rules", "proxy_rules", "traffic_mirror", "http1_drop":
		default:
			unknownCustomer = append(unknownCustomer, key)
		}
	}

	rawAIRules, ok := customerRoot["ai_rules"]
	if !ok {
		return unknownCustomer, unknownAIRules, nil
	}

	var aiRoot map[string]json.RawMessage
	if err := json.Unmarshal(rawAIRules, &aiRoot); err != nil {
		return nil, nil, fmt.Errorf("customer.ai_rules must be an object")
	}

	for provider, rawProvider := range aiRoot {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawProvider, &fields); err != nil {
			return nil, nil, fmt.Errorf("customer.ai_rules.%s must be an object with editable fields [enble include editable]", provider)
		}
		for key := range fields {
			switch key {
			case "enble", "enable", "include", "exclude", "editable", "label":
			default:
				unknownAIRules[provider] = append(unknownAIRules[provider], key)
			}
		}
	}

	return unknownCustomer, unknownAIRules, nil
}

func parseHostPort(server string) (string, uint16, error) {
	host, portRaw, err := net.SplitHostPort(strings.TrimSpace(server))
	if err != nil {
		return "", 0, fmt.Errorf("must be host:port, got %q", server)
	}
	host = strings.TrimSpace(host)
	portRaw = strings.TrimSpace(portRaw)
	if host == "" {
		return "", 0, fmt.Errorf("host is required")
	}
	if portRaw == "" {
		return "", 0, fmt.Errorf("port is required")
	}

	port, err := strconv.Atoi(portRaw)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q", portRaw)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port must be in range 1-65535")
	}

	return host, uint16(port), nil
}

func parseCustomerProxyServer(proxyType, server, username, password string) (string, uint16, string, string, error) {
	raw := strings.TrimSpace(server)
	if raw == "" {
		return "", 0, "", "", fmt.Errorf("server is required")
	}

	address := raw
	urlUsername := ""
	urlPassword := ""
	urlLike := strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "@")

	if urlLike {
		parseRaw := raw
		if strings.Contains(raw, "@") && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "//") {
			parseRaw = "proxy://" + raw
		}
		parsed, err := url.Parse(parseRaw)
		if err != nil {
			return "", 0, "", "", fmt.Errorf("invalid proxy URL %q: %w", server, err)
		}
		if parsed.Scheme != "" && parsed.Scheme != "proxy" {
			if err := validateCustomerProxyScheme(proxyType, parsed.Scheme); err != nil {
				return "", 0, "", "", err
			}
		}
		if parsed.Host == "" {
			return "", 0, "", "", fmt.Errorf("proxy URL must include host:port")
		}
		address = parsed.Host
		if parsed.User != nil {
			urlUsername = parsed.User.Username()
			if pass, ok := parsed.User.Password(); ok {
				urlPassword = pass
			}
		}
	}

	host, port, err := parseHostPort(address)
	if err != nil {
		return "", 0, "", "", err
	}

	effectiveUsername := strings.TrimSpace(urlUsername)
	effectivePassword := urlPassword
	if strings.TrimSpace(username) != "" {
		effectiveUsername = strings.TrimSpace(username)
	}
	if password != "" {
		effectivePassword = password
	}

	return host, port, effectiveUsername, effectivePassword, nil
}

func validateCustomerProxyScheme(proxyType, scheme string) error {
	normalizedType := strings.ToLower(strings.TrimSpace(proxyType))
	normalizedScheme := strings.ToLower(strings.TrimSpace(scheme))

	switch normalizedType {
	case "http":
		if normalizedScheme != "http" {
			return fmt.Errorf("customer.proxy.server scheme %q does not match proxy type %q", scheme, proxyType)
		}
	case "socks5":
		if normalizedScheme != "socks5" && normalizedScheme != "socks" {
			return fmt.Errorf("customer.proxy.server scheme %q does not match proxy type %q", scheme, proxyType)
		}
	}

	return nil
}

func (c *Config) APIBaseURL() string {
	if c == nil || c.Core == nil {
		return ""
	}
	return strings.TrimSpace(c.Core.APIServer)
}

func (c *Config) AgentBaseURL() string {
	if c == nil || c.Core == nil {
		return ""
	}
	if server := strings.TrimSpace(c.Core.AgentServer); server != "" {
		return strings.TrimRight(server, "/")
	}
	if c.Core.AliangServer != nil {
		if server := strings.TrimSpace(c.Core.AliangServer.AgentServer); server != "" {
			return strings.TrimRight(server, "/")
		}
	}
	return DefaultAgentServerURL
}

func (c *Config) EffectiveAliangCoreServer() string {
	if c != nil && c.Core != nil && c.Core.AliangServer != nil && strings.TrimSpace(c.Core.AliangServer.CoreServer) != "" {
		return strings.TrimSpace(c.Core.AliangServer.CoreServer)
	}
	return "ai-gateway.aliang.one:443"
}

func (c *Config) EffectiveDefaultProxy() string {
	// Customer proxy availability enables the toSocks branch, but unmatched
	// traffic should still use direct egress unless a routing rule says
	// otherwise.
	return "direct"
}

func (c *Config) EffectiveSocksProxy() (*Socks5Config, error) {
	if c == nil || c.Customer == nil || c.Customer.Proxy == nil {
		return nil, nil
	}
	if !c.Customer.Proxy.IsEnabled() {
		return nil, nil
	}
	if strings.ToLower(strings.TrimSpace(c.Customer.Proxy.Type)) != "socks5" {
		return nil, nil
	}

	serverHost, serverPort, username, password, err := parseCustomerProxyServer(
		"socks5",
		c.Customer.Proxy.Server,
		c.Customer.Proxy.Username,
		c.Customer.Proxy.Password,
	)
	if err != nil {
		return nil, fmt.Errorf("customer.proxy.server: %w", err)
	}

	return &Socks5Config{
		Server:     serverHost,
		ServerPort: serverPort,
		Username:   username,
		Password:   password,
	}, nil
}

func (c *Config) EffectiveHTTPProxy() (*HTTPProxyConfig, error) {
	if c == nil || c.Customer == nil || c.Customer.Proxy == nil {
		return nil, nil
	}
	if !c.Customer.Proxy.IsEnabled() {
		return nil, nil
	}
	if strings.ToLower(strings.TrimSpace(c.Customer.Proxy.Type)) != "http" {
		return nil, nil
	}

	serverHost, serverPort, username, password, err := parseCustomerProxyServer(
		"http",
		c.Customer.Proxy.Server,
		c.Customer.Proxy.Username,
		c.Customer.Proxy.Password,
	)
	if err != nil {
		return nil, fmt.Errorf("customer.proxy.server: %w", err)
	}

	return &HTTPProxyConfig{
		Server:     serverHost,
		ServerPort: serverPort,
		Username:   username,
		Password:   password,
	}, nil
}

func (c *Config) EffectiveCustomerProxy() (interface{}, error) {
	if c == nil || c.Customer == nil || c.Customer.Proxy == nil {
		return nil, nil
	}
	if !c.Customer.Proxy.IsEnabled() {
		return nil, nil
	}

	switch strings.ToLower(strings.TrimSpace(c.Customer.Proxy.Type)) {
	case "socks5":
		return c.EffectiveSocksProxy()
	case "http":
		return c.EffectiveHTTPProxy()
	default:
		return nil, nil
	}
}

func (c *Config) EffectiveAIAllowlist() []string {
	if c == nil || c.Customer == nil || len(c.Customer.AIRules) == 0 {
		return nil
	}

	allowlist := make([]string, 0)
	for _, provider := range sortedMapKeys(c.Customer.AIRules) {
		rule := c.Customer.AIRules[provider]
		if rule == nil || rule.Enble == nil || !*rule.Enble {
			continue
		}
		allowlist = append(allowlist, rule.Include...)
	}

	return dedupeTrimmedDomains(allowlist)
}

func NormalizeAIProviderKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func FindCustomerAIRule(rules map[string]*CustomerAIRuleSetting, key string) (*CustomerAIRuleSetting, bool) {
	if len(rules) == 0 {
		return nil, false
	}
	if rule, ok := rules[key]; ok {
		return rule, true
	}

	normalizedKey := NormalizeAIProviderKey(key)
	for existingKey, rule := range rules {
		if NormalizeAIProviderKey(existingKey) == normalizedKey {
			return rule, true
		}
	}
	return nil, false
}

func (c *Config) EffectiveDNSPreResolution() *DNSPreResolutionConfig {
	return GetDNSPreResolutionConfig()
}

// EffectiveTrafficMirror returns the traffic mirror config if mirroring is enabled.
func (c *Config) EffectiveTrafficMirror() *TrafficMirrorConfig {
	if c == nil || c.Customer == nil || c.Customer.TrafficMirror == nil {
		return nil
	}
	if !c.Customer.TrafficMirror.Enabled {
		return nil
	}
	return c.Customer.TrafficMirror
}

func (c *Config) EffectiveHTTP1Drop() *HTTP1DropConfig {
	if c == nil || c.Customer == nil || c.Customer.HTTP1Drop == nil {
		return &HTTP1DropConfig{
			PathContains: []string{defaultHTTP1DropPathContains},
		}
	}

	cfg := *c.Customer.HTTP1Drop
	return &cfg
}

func sortedMapKeys(values map[string]*CustomerAIRuleSetting) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func dedupeTrimmedDomains(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func dedupeTrimmedStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
