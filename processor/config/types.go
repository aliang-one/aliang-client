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

	gossh "golang.org/x/crypto/ssh"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/match"
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

// SSHProxyConfig represents SSH outbound configuration.
type SSHProxyConfig struct {
	Server        string          `json:"server_host"`
	ServerPort    uint16          `json:"server_port"`
	Username      string          `json:"username,omitempty"`
	Password      string          `json:"password,omitempty"`
	PrivateKey    string          `json:"private_key,omitempty"`
	KeyPassphrase string          `json:"key_passphrase,omitempty"`
	PoolSize      int             `json:"pool_size,omitempty"`
	JumpServers   []SSHJumpServer `json:"jump_servers,omitempty"`
}

// SSHJumpServer 表示一跳跳板(ProxyJump 链上按序穿透的一个节点)。
type SSHJumpServer struct {
	Server        string `json:"server_host"`
	ServerPort    uint16 `json:"server_port"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
	KeyPassphrase string `json:"key_passphrase,omitempty"`
}

// Validate validates SSH proxy configuration.
func (c *SSHProxyConfig) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server_host is required")
	}
	if c.ServerPort == 0 {
		return fmt.Errorf("server_port is required")
	}
	if c.Username == "" {
		return fmt.Errorf("ssh proxy requires username")
	}
	if c.Password == "" && c.PrivateKey == "" {
		return fmt.Errorf("ssh proxy requires password or private_key")
	}
	for i := range c.JumpServers {
		jump := &c.JumpServers[i]
		if jump.Server == "" || jump.ServerPort == 0 {
			return fmt.Errorf("jump_servers[%d] requires server_host and server_port", i)
		}
		if jump.Username == "" {
			return fmt.Errorf("jump_servers[%d] requires username", i)
		}
		if jump.Password == "" && jump.PrivateKey == "" {
			return fmt.Errorf("jump_servers[%d] requires password or private_key", i)
		}
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
	UsageTracker *UsageTrackerConfig `json:"usage_tracker,omitempty"`
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
	for i, domain := range c.Domains {
		if kind, _, err := match.ClassifyEntry(domain); err != nil {
			return fmt.Errorf("traffic_mirror.domains[%d] invalid %q: %v", i, domain, err)
		} else if kind == match.EntryGeoIP {
			return fmt.Errorf("traffic_mirror.domains[%d] invalid %q: 镜像列表不支持 geoip 条目", i, domain)
		}
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

// UsageTrackerConfig 控制本地 Claude Code 用量采集上报（core.usage_tracker）。
// 默认开启；关闭后 tracker 不扫描、reporter 不推送（spec §5.3）。
type UsageTrackerConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (c *UsageTrackerConfig) IsEnabled() bool {
	return c == nil || c.Enabled == nil || *c.Enabled
}

// ModelMappingConfig rewrites the top-level "model" field of forwarded HTTP/1
// AI request bodies (e.g. OpenAI /v1/chat/completions, Anthropic /v1/messages)
// from the original value to the mapped value before sending to the upstream.
// Models absent from Rules are forwarded unchanged. The feature is opt-in and
// only applies to HTTP/1 AI-accelerated traffic.
type ModelMappingConfig struct {
	Enabled *bool             `json:"enable,omitempty"`
	Rules   map[string]string `json:"rules,omitempty"` // 原始 model -> 替换 model
}

func (c *ModelMappingConfig) UnmarshalJSON(data []byte) error {
	type alias struct {
		Enabled       *bool             `json:"enable,omitempty"`
		LegacyEnabled *bool             `json:"enabled,omitempty"`
		Rules         map[string]string `json:"rules,omitempty"`
	}

	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	c.Enabled = decoded.Enabled
	if c.Enabled == nil {
		c.Enabled = decoded.LegacyEnabled
	}
	c.Rules = decoded.Rules
	return nil
}

// IsEnabled reports whether model rewriting is enabled. The feature is opt-in:
// it defaults to disabled when Enabled is unset.
func (c *ModelMappingConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// EffectiveRules returns the cleaned mapping (trimmed keys/values, empty
// entries dropped). It returns nil when there are no usable rules.
func (c *ModelMappingConfig) EffectiveRules() map[string]string {
	if c == nil || len(c.Rules) == 0 {
		return nil
	}
	rules := make(map[string]string, len(c.Rules))
	for rawFrom, rawTo := range c.Rules {
		from := strings.TrimSpace(rawFrom)
		to := strings.TrimSpace(rawTo)
		if from == "" || to == "" {
			continue
		}
		rules[from] = to
	}
	if len(rules) == 0 {
		return nil
	}
	return rules
}

func (c *ModelMappingConfig) Validate() error {
	if c == nil || !c.IsEnabled() {
		return nil
	}
	for rawFrom, rawTo := range c.Rules {
		from := strings.TrimSpace(rawFrom)
		to := strings.TrimSpace(rawTo)
		if from == "" {
			return fmt.Errorf("model_mapping.rules cannot have an empty source model")
		}
		if to == "" {
			return fmt.Errorf("model_mapping.rules[%q] cannot map to an empty target model", from)
		}
	}
	return nil
}

// CustomEnvVarsConfig overrides environment variables for AI CLI child processes
// (claude/codex) spawned by the agent. When enabled, each KEY in Vars replaces any
// same-named variable inherited from the agent process environment before the child
// is exec'd; variables absent from Vars are inherited unchanged. The feature is opt-in.
type CustomEnvVarsConfig struct {
	Enabled *bool             `json:"enable,omitempty"`
	Vars    map[string]string `json:"vars,omitempty"` // 环境变量 KEY -> VALUE
}

func (c *CustomEnvVarsConfig) UnmarshalJSON(data []byte) error {
	type alias struct {
		Enabled       *bool             `json:"enable,omitempty"`
		LegacyEnabled *bool             `json:"enabled,omitempty"`
		Vars          map[string]string `json:"vars,omitempty"`
	}

	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	c.Enabled = decoded.Enabled
	if c.Enabled == nil {
		c.Enabled = decoded.LegacyEnabled
	}
	c.Vars = decoded.Vars
	return nil
}

// IsEnabled reports whether custom env-var injection is enabled. The feature is
// opt-in: it defaults to disabled when Enabled is unset.
func (c *CustomEnvVarsConfig) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return false
	}
	return *c.Enabled
}

// EffectiveVars returns the cleaned overrides (trimmed keys, empty keys dropped).
// Values are preserved verbatim — they may legitimately be empty or contain '='.
// It returns nil when there are no usable entries.
func (c *CustomEnvVarsConfig) EffectiveVars() map[string]string {
	if c == nil || len(c.Vars) == 0 {
		return nil
	}
	vars := make(map[string]string, len(c.Vars))
	for rawKey, rawValue := range c.Vars {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		vars[key] = rawValue
	}
	if len(vars) == 0 {
		return nil
	}
	return vars
}

func (c *CustomEnvVarsConfig) Validate() error {
	if c == nil || !c.IsEnabled() {
		return nil
	}
	for rawKey := range c.Vars {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return fmt.Errorf("custom_env_vars.vars cannot have an empty variable name")
		}
		if strings.Contains(key, "=") {
			return fmt.Errorf("custom_env_vars.vars[%q] cannot contain '=' in the variable name", key)
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
	ModelMapping  *ModelMappingConfig               `json:"model_mapping,omitempty"`
	CustomEnvVars *CustomEnvVarsConfig              `json:"custom_env_vars,omitempty"`
	GeoIP         *CustomerGeoIPSettings            `json:"geoip,omitempty"`
	// AgentCoreLLM 是 AliangCode 内核的显式 LLM 端点设置(customer.agentcore_llm)。
	// nil 或 Enabled=false 时整层不生效,行为回落继承链(env/settings→兜底)。
	AgentCoreLLM *AgentCoreLLMConfig `json:"agentcore_llm,omitempty"`
	// AgentCore 是 AliangCode 内核的行为调优设置(customer.agentcore)。nil 时
	// 全部字段走内核默认值——这是未配置时的线上形态,不许有隐式副作用。
	AgentCore *AgentCoreTuningConfig `json:"agentcore,omitempty"`
	// PeerMCP 是 aliang-peer 跨设备 MCP 工具面的总开关(customer.peer_mcp)。
	// nil 时 Enable 视为 true——对齐 agentcore peer 注入的既有现状(默认开),
	// 升级不产生行为回退。它门控三处:agentcore 注入、claude/codex 注册。
	PeerMCP *PeerMCPConfig `json:"peer_mcp,omitempty"`
	// Mesh 是节点转发网设置(customer.mesh,design docs/mesh-forwarding-network-design.md)。
	// nil 时整层不生效:不主动经 mesh 出口,也不接受被其他节点转发。
	Mesh *MeshConfig `json:"mesh,omitempty"`
	// SSHIngress 是 SSH 入口服务面(customer.ssh_ingress)。nil 时服务关闭——
	// 默认关,升级零回退。与组网分支的 MeshConfig 为相邻行合并关系(双方尾部追加)。
	SSHIngress *SSHIngressConfig `json:"ssh_ingress,omitempty"`
}

// CustomerGeoIPSettings 自定义 GeoIP 国家库下载源（customer.geoip）。
// DownloadURL 非空时必须为 https URL；运行时优先级 env
// ALIANG_GEOIP_DOWNLOAD_URL（部署面覆盖）> 此处配置 > 默认 COS 源。
type CustomerGeoIPSettings struct {
	DownloadURL string `json:"download_url,omitempty"`
}

// EffectiveDownloadURL 返回 trim 后的下载源；空串表示未配置（用默认源）。
func (c *CustomerGeoIPSettings) EffectiveDownloadURL() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.DownloadURL)
}

func (c *CustomerGeoIPSettings) Validate() error {
	if c == nil {
		return nil
	}
	u := c.EffectiveDownloadURL()
	if u == "" {
		return nil
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("geoip.download_url must be an https URL, got %q", c.DownloadURL)
	}
	return nil
}

// AgentCoreLLMConfig 显式 LLM 端点。仅支持 Anthropic Messages 兼容端点
// (内核协议见 app/agentcore/client.go);BaseURL 远端时必须配 APIKey——
// 远端直连没有平台 relay 注入凭据,裸 401 无法自查。
type AgentCoreLLMConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

// EffectiveAgentCoreLLM 返回显式设置的副本;未配置/未启用/URL 空/校验不过返回
// nil,调用方按整层关闭处理。形状对齐 EffectiveCustomEnvVars 的 nil-guard。
func (c *Config) EffectiveAgentCoreLLM() *AgentCoreLLMConfig {
	if c == nil || c.Customer == nil || c.Customer.AgentCoreLLM == nil {
		return nil
	}
	llm := c.Customer.AgentCoreLLM
	if !llm.Enabled || strings.TrimSpace(llm.BaseURL) == "" {
		return nil
	}
	cfg := *llm
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	// 读取端防御(spec §3.1):手改配置文件可绕过保存校验,这里兜底——非法
	// 形态整层关闭 + Warn,不静默、不 crash、不把坏配置放进读取链。每回合
	// 多次读取会重复 Warn 数行,属可接受噪声(用户必须修配置),不做去重。
	if err := cfg.Validate(); err != nil {
		logger.Warn("agentcore_llm 配置非法,本层关闭: " + err.Error())
		return nil
	}
	return &cfg
}

// Validate 保存通道(Config.Validate)调用:URL 必须可解析;非回环 ⇒ key 必填。
func (cfg *AgentCoreLLMConfig) Validate() error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("agentcore_llm.base_url 必须是合法 URL(含 scheme 与 host), got %q", cfg.BaseURL)
	}
	// scheme 只放行 http/https(内核 HTTP 客户端不支持 ftp 等其他 scheme)。
	// 有意放行远端 http://:家庭内网网关如 http://192.168.x.x 无 TLS 可言,属合理形态,不强推 https。
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("agentcore_llm.base_url 仅支持 http/https, got %q", cfg.BaseURL)
	}
	if !isLoopbackHostName(u.Hostname()) && strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("agentcore_llm.api_key 必填: 非回环地址 %s 无平台 relay 注入凭据", strings.TrimSpace(cfg.BaseURL))
	}
	return nil
}

// AgentCoreTuningConfig 是 AliangCode 内核的行为调优设置(customer.agentcore)。
// 与 agentcore_llm(端点)分开:端点是「连到哪」,这里是「行为阈值」。
type AgentCoreTuningConfig struct {
	// MaxIdenticalToolTurns 是无进展守卫「连续 N 轮发起完全相同的工具调用
	// (名字+参数一致)即判定打转并终止」的阈值。0 = 用内核默认
	// (defaultMaxIdenticalToolTurns);上限 50——再大守卫就形同虚设,烧 token
	// 的空转跑满 50 轮才停,不如直接关掉。
	MaxIdenticalToolTurns int `json:"max_identical_tool_turns,omitempty"`
	// MCPEnabled 是 MCP 工具注入的全局开关（仪表盘「能力接入 › MCP 全局开关」）。
	// 用指针是为了区分三态：nil = 未配置（默认开启，保持既有行为）、
	// true = 显式开启、false = 显式关闭（内核一个 MCP server 都不连）。
	// 只作用于 agentcore 内核；claude/codex 走各自 CLI 的 MCP 配置，不受它影响。
	MCPEnabled *bool `json:"mcp_enabled,omitempty"`
	// SkillsEnabled 是技能注入的全局开关（仪表盘「能力接入 › 技能全局开关」。
	// 同样用指针保留三态：nil = 未配置（默认开启）、true/false 显式。只作用于
	// agentcore 内核。
	SkillsEnabled *bool `json:"skills_enabled,omitempty"`
	// SubagentMaxParallel 是并行派发子 agent 的并发上限（2026-11-10 vm130 事故
	// 根治）。主 LLM 自己占一个上游并发槽，同回合并行 4 个子 agent = 5 路并发
	// ——上游按账号并发计费/限额时直接 429「Concurrency limit exceeded」，
	// 且重试者自身也是并发源，退避窗口内兄弟子 agent 还在打，重试耗尽只能
	// 判死。闸的语义：超出上限的子 run 在宿主侧排队（ctx 感知），拿到槽才
	// 起跑——排队代替撞限流。0 = 未配置（走默认 2，保守值宁慢勿死）；
	// 1..8 = 显式上限；负数非法。想拿回 4 路并行的账号并发充裕用户显式配 4。
	SubagentMaxParallel int `json:"subagent_max_parallel,omitempty"`
	// MemoryEnabled 是记忆层总开关（提取+注入）。三态指针沿 MCPEnabled/SkillsEnabled
	// 先例：nil=未配置（默认开启）、true/false 显式。只作用于 agentcore 内核。
	// 关闭时不提取、不注入，但 API 仍可读可管（手动管理既有记忆不被开关锁死）。
	MemoryEnabled *bool `json:"memory_enabled,omitempty"`
	// AIMCP 是 agent-as-MCP 工具面的总开关（customer.ai_mcp）。三态指针沿
	// MCPEnabled/SkillsEnabled/MemoryEnabled 先例：nil=未配置（默认开启）、
	// true/false 显式。只门控 ai_run_* 三工具（aliang mcp stdio 第 6 工具组），
	// 不碰 peer 工具面。stdio 同用户信任模型下不引入新权限（同用户本可直打
	// 管理口），独立键只为可关。
	AIMCP *bool `json:"ai_mcp,omitempty"`
}

// Validate 保存通道(Config.Validate)调用。0 合法(=未配置,走默认);
// 上限 50 是防呆,不是业务约束。
func (c *AgentCoreTuningConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.MaxIdenticalToolTurns < 0 || c.MaxIdenticalToolTurns > 50 {
		return fmt.Errorf("agentcore.max_identical_tool_turns 必须在 [0,50] 内(0=用内核默认), got %d", c.MaxIdenticalToolTurns)
	}
	if c.SubagentMaxParallel < 0 || c.SubagentMaxParallel > agentCoreSubagentMaxParallelCeiling {
		// 措辞与 EffectiveAgentCoreSubagentMaxParallel 同口径：0 不是「关闸」
		// （≤0 一律回落默认，闸不可关），是「未配置」——此前报错文案写
		// 「0=关闸」，与实现矛盾（vm129 审计 2026-11-10）。数字引常量不手抄
		// （本批立项理由正是文案与运行时漂移）。
		return fmt.Errorf("agentcore.subagent_max_parallel 必须在 [0,%d] 内(0=未配置走默认 %d), got %d",
			agentCoreSubagentMaxParallelCeiling, agentCoreSubagentMaxParallelDefault, c.SubagentMaxParallel)
	}
	return nil
}

// EffectiveAgentCoreMCPEnabled 返回 MCP 注入是否开启。
//
// 默认开启：MCP 是既有能力，加开关不能把没配过的人的功能关掉。只有显式
// 写成 false 才关闭。nil（未配置）与 true 等价。
func (c *Config) EffectiveAgentCoreMCPEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return true
	}
	if c.Customer.AgentCore.MCPEnabled == nil {
		return true
	}
	return *c.Customer.AgentCore.MCPEnabled
}

// EffectiveAgentCoreSkillsEnabled 返回技能注入是否开启。默认开启（同 MCP）。
func (c *Config) EffectiveAgentCoreSkillsEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return true
	}
	if c.Customer.AgentCore.SkillsEnabled == nil {
		return true
	}
	return *c.Customer.AgentCore.SkillsEnabled
}

// EffectiveAgentCoreMemoryEnabled 返回记忆层是否开启。默认开启（同 MCP/技能）：
// 记忆是加法能力，开关不能把没配过的人的功能关掉。nil（未配置）与 true 等价。
func (c *Config) EffectiveAgentCoreMemoryEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return true
	}
	if c.Customer.AgentCore.MemoryEnabled == nil {
		return true
	}
	return *c.Customer.AgentCore.MemoryEnabled
}

// EffectiveAgentCoreAIMCPEnabled 返回 agent-as-MCP 工具面是否开启。
// 默认开启(同家族):nil(未配置)与 true 等价。
func (c *Config) EffectiveAgentCoreAIMCPEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return true
	}
	if c.Customer.AgentCore.AIMCP == nil {
		return true
	}
	return *c.Customer.AgentCore.AIMCP
}

// PeerMCPConfig 控制 aliang-peer 跨设备工具面(customer.peer_mcp)。三态指针
// 语义与 agentcore.mcp_enabled 同族:nil=未配置(默认开)、true/false=显式。
type PeerMCPConfig struct {
	Enable           *bool `json:"enable,omitempty"`            // 总开关:关=三面全关
	AgentcoreEnabled *bool `json:"agentcore_enabled,omitempty"` // agentcore 工具面注入;nil=跟随总开关
	ClaudeEnabled    *bool `json:"claude_enabled,omitempty"`    // claudecode 自动注册;nil=跟随总开关
	CodexEnabled     *bool `json:"codex_enabled,omitempty"`     // codex 自动注册;nil=跟随总开关
}

// EffectivePeerMCPEnabled 返回 aliang-peer 跨设备工具面是否开启。默认开启:
// peer 工具是既有能力(agentcore 已默认注入),加开关不能把没配过的人的功能
// 关掉。只有显式写成 false 才关闭。
func (c *Config) EffectivePeerMCPEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.PeerMCP == nil || c.Customer.PeerMCP.Enable == nil {
		return true
	}
	return *c.Customer.PeerMCP.Enable
}

// effectivePeerMCPSub 总开关与子键的合取:master(默认真) && (sub nil=跟随→真)。
func (c *Config) effectivePeerMCPSub(sub *bool) bool {
	if !c.EffectivePeerMCPEnabled() {
		return false
	}
	if sub == nil {
		return true
	}
	return *sub
}

// EffectivePeerMCPAgentCoreEnabled agentcore 工具面注入开关(peer 五件套 per-run 注入)。
func (c *Config) EffectivePeerMCPAgentCoreEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.PeerMCP == nil {
		return true
	}
	return c.effectivePeerMCPSub(c.Customer.PeerMCP.AgentcoreEnabled)
}

// EffectivePeerMCPClaudeEnabled claudecode 平台托管注册开关(启动自愈+apply 对齐读点)。
func (c *Config) EffectivePeerMCPClaudeEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.PeerMCP == nil {
		return true
	}
	return c.effectivePeerMCPSub(c.Customer.PeerMCP.ClaudeEnabled)
}

// EffectivePeerMCPCodexEnabled codex 平台托管注册开关(启动自愈+apply 对齐读点)。
func (c *Config) EffectivePeerMCPCodexEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.PeerMCP == nil {
		return true
	}
	return c.effectivePeerMCPSub(c.Customer.PeerMCP.CodexEnabled)
}

// MeshConfig 节点转发网(design docs/mesh-forwarding-network-design.md)。
type MeshConfig struct {
	Enable        *bool `json:"enable,omitempty"`
	ForwardEnable *bool `json:"forward_enable,omitempty"`
	// PunchEnable UDP 打洞直连(mesh-udp-punch plan Task 11)。默认关:打洞
	// 要本机直接对公网暴露 UDP 口,是让渡型能力,必须显式开启(nil=false,
	// 与 ForwardEnable 同族三态)。Task 12 起被工厂/装配门消费。
	PunchEnable *bool               `json:"punch_enable,omitempty"`
	MaxHops     int                 `json:"max_hops,omitempty"` // 链式转发经停上限(via 链长):0=默认,>8 收口;接收端拒绝过长经停链
	Peers       []MeshPeer          `json:"peers,omitempty"`
	Fallback    *MeshFallbackConfig `json:"fallback,omitempty"`
	// PrivateKey 本节点的 mesh 身份私钥(PEM,OpenSSH 或 PKCS#8)。
	// 公钥分发到各对端的 ssh_ingress.mesh_authorized_keys(尾注释=本机 device_id)。
	// 空 = 本节点不能作为转发发起方(仍可作为被转发方)。
	PrivateKey string `json:"private_key,omitempty"`
	// KeyPassphrase 私钥口令。Validate 会 TrimSpace:首尾真有空格的 passphrase
	// 会被静默改掉导致解不开钥(与 proxy.key_passphrase 既有口径一致,接受)。
	KeyPassphrase string `json:"key_passphrase,omitempty"`
}

type MeshPeer struct {
	DeviceID string   `json:"device_id"`
	LANAddrs []string `json:"lan_addrs,omitempty"`
	Relay    *bool    `json:"relay,omitempty"`
	// HostKey 对端 ssh_ingress 的 host key 钉扎(authorized_keys 行或 SHA256: 指纹)。
	// 空 = 该 peer 不可用(fail-closed,不做 TOFU,SPEC §10.4)。
	HostKey string `json:"host_key,omitempty"`
}

type MeshFallbackConfig struct {
	Enable *bool `json:"enable,omitempty"`
	// NextHop 出口链路的下一跳设备(enable=true 时必填):须为 peers 中已
	// 钉扎(host_key 非空)的节点。启用后本机对 Aliang 云与内部接口的出口
	// 流量常驻交给它;下一跳也启用时继续链向它的下一跳(via 防环),直到
	// 某台未启用的设备直接出网。旧版字段(classes/addr/host_key)已废弃:
	// 地址与钉扎改从 peers 条目继承,存量配置中的旧键在加载时被忽略
	// (发布说明 §升级)。
	NextHop string `json:"next_hop,omitempty"`
}

// EffectiveEnabled mesh 出口总开关:三态指针语义与 peer_mcp.enable 同族,
// nil=默认开——mesh 是既有直连形态之上的增量出口,加开关不能让升级把
// 已配好的 mesh 静默关掉;只有显式 false 才关。
func (c *MeshConfig) EffectiveEnabled() bool { return c == nil || c.Enable == nil || *c.Enable }

// EffectiveForwardEnabled 允许被其他节点转发(design §8)。默认关:被转发
// 要消耗本机带宽,是让渡型能力,必须显式开启(nil=false),与总开关的
// 默认开语义刻意相反。
func (c *MeshConfig) EffectiveForwardEnabled() bool {
	return c != nil && c.ForwardEnable != nil && *c.ForwardEnable // 默认关(design §8)
}

// EffectivePunchEnabled UDP 打洞直连开关。默认关(nil=false):打洞直连
// 要本机对公网暴露 UDP 口,是让渡型能力,必须显式开启——与
// EffectiveForwardEnabled 同款「默认关」三态,而与总开关的默认开语义相反。
// 依赖:生效前提是 mesh 总开关开着(保存期硬拒见 ValidateMeshDependency,
// 运行期装配门 Task 12 与 EffectiveEnabled 合取)。
func (c *MeshConfig) EffectivePunchEnabled() bool {
	return c != nil && c.PunchEnable != nil && *c.PunchEnable // 默认关
}

// EffectiveMaxHops 跳数上限:0/负数=默认 5,>8 收口到 8(读取端钳制防御,
// 手改配置文件可绕过保存校验,越界只会被夹回合法域,不会 crash)。
func (c *MeshConfig) EffectiveMaxHops() int {
	if c == nil || c.MaxHops <= 0 {
		return meshproto.DefaultHops
	}
	if c.MaxHops > meshproto.MaxHopsCap {
		return meshproto.MaxHopsCap
	}
	return c.MaxHops
}

// MeshDelegateNextHop 出口链路下一跳(2026-11-12 用户拍板,替代离线兜底):
// fallback.enable 显式 true 且 next_hop 指向 peers 中已钉扎的节点 → 返回该
// peer。mesh 生效依赖(SSH 入口开启)由 MeshEffectivelyEnabled 承担——委托
// 走的也是 mesh 通道,SSH 入口关了载体不存在,能力随之降级。
//
// 触发为常驻语义:不做「本机能否直连平台」的在线判定(用户明确要求去掉
// 离线门控)——开关即无条件改道。下一跳故障时对外流量失败、错误回源,
// 这是接受的取舍(发布说明已写明)。
func (c *Config) MeshDelegateNextHop() (*MeshPeer, bool) {
	if c == nil || c.Customer == nil || c.Customer.Mesh == nil || c.Customer.Mesh.Fallback == nil {
		return nil, false
	}
	fb := c.Customer.Mesh.Fallback
	if fb.Enable == nil || !*fb.Enable {
		return nil, false
	}
	next := strings.TrimSpace(fb.NextHop)
	if next == "" || !c.MeshEffectivelyEnabled() {
		return nil, false
	}
	for i := range c.Customer.Mesh.Peers {
		p := &c.Customer.Mesh.Peers[i]
		if strings.TrimSpace(p.DeviceID) == next {
			if strings.TrimSpace(p.HostKey) == "" {
				return nil, false // 未钉扎:能力不成立(fail-closed)
			}
			return p, true
		}
	}
	return nil, false
}

// MeshEffectivelyEnabled mesh 出口的生效开关 = 用户开关 ∧ SSH 入口可用
// (SPEC §10.3)。依赖降级:SSH 入口未启用时 mesh 无出口载体,恒 false——
// 存量配置(mesh 开、ssh 未配)升级后 mesh 静默降级为关,而不是启动失败。
// 保存期硬拒由 ValidateMeshDependency 承担(customer 配置提交路径);本方法
// 只做启动/运行期降级判定,两者不重复报错。
func (c *Config) MeshEffectivelyEnabled() bool {
	if c == nil || c.Customer == nil {
		return false
	}
	if !c.Customer.Mesh.EffectiveEnabled() {
		return false
	}
	return c.Customer.SSHIngress.IsEnabled()
}

// ValidateMeshDependency 保存期依赖硬拒:开 mesh 必须开 SSH 入口
// (SPEC §10.3);开 punch(UDP 打洞直连)必须开 mesh 总开关。启动期不调用
// 本函数(Config.Validate 也不调)——启动降级走 MeshEffectivelyEnabled,
// 绝不阻断 boot。
func (c *Config) ValidateMeshDependency() error {
	// Mesh 块缺失(JSON 无 mesh 键 = 用户从未触及 mesh)直接过:硬拒只在
	// 保存携带 mesh 块时触发;启动期的对应降级由 MeshEffectivelyEnabled 兜住。
	if c == nil || c.Customer == nil || c.Customer.Mesh == nil {
		return nil
	}
	if c.Customer.Mesh.EffectiveEnabled() && !c.Customer.SSHIngress.IsEnabled() {
		return fmt.Errorf("组网依赖 SSH 入口(ssh_ingress.enable):开启 customer.mesh.enable 前必须先开启 SSH 入口")
	}
	// punch 是 mesh 出口内的载体:总开关显式关而 punch 显式开 = 自相矛盾
	// 配置,保存期硬拒(与 mesh↔SSH 依赖门同款只挂保存通道)。运行期装配门
	// (Task 12)另与总开关合取,手改配置文件绕过校验也不会让 punch 单飞。
	if c.Customer.Mesh.EffectivePunchEnabled() && !c.Customer.Mesh.EffectiveEnabled() {
		return fmt.Errorf("打洞直连依赖组网总开关(customer.mesh.enable):开启 customer.mesh.punch_enable 前必须先开启组网总开关")
	}
	return nil
}

// Validate 保存通道(Config.Validate)调用:peers/兜底/跳数的形态校验。
// 自指拒绝不在这层——config 包拿不到本机 device_id(反向依赖会成环),
// 由提交路径显式调 ValidateMesh(spec §3.4/§7 层0)。
func (c *MeshConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.MaxHops < 0 || c.MaxHops > meshproto.MaxHopsCap {
		return fmt.Errorf("customer.mesh.max_hops 必须在 [1,%d] 内(0=默认 %d), got %d", meshproto.MaxHopsCap, meshproto.DefaultHops, c.MaxHops)
	}
	for i := range c.Peers {
		peer := &c.Peers[i]
		// 就地规整(同 proxy_rules/ai_rules 的 TrimSpace 先例),保存后落盘
		// 的是规整值,ValidateMesh 的精确比对才不会被空白变体绕过。
		peer.DeviceID = strings.TrimSpace(peer.DeviceID)
		if peer.DeviceID == "" {
			return fmt.Errorf("customer.mesh.peers[%d] device_id required", i)
		}
		for j := range peer.LANAddrs {
			// 与 device_id 同款就地规整:校验与落盘同一份值,不留给
			// 下游「校验过的形态≠存储的形态」的缝。
			peer.LANAddrs[j] = strings.TrimSpace(peer.LANAddrs[j])
			if _, _, err := net.SplitHostPort(peer.LANAddrs[j]); err != nil {
				return fmt.Errorf("customer.mesh.peers[%d].lan_addrs[%d] invalid %q: must be host:port", i, j, peer.LANAddrs[j])
			}
		}
		peer.HostKey = strings.TrimSpace(peer.HostKey)
		if peer.HostKey == "" {
			return fmt.Errorf("customer.mesh.peers[%d].host_key required (peer host key must be pinned, no TOFU)", i)
		}
	}
	if c.Fallback != nil && c.Fallback.Enable != nil && *c.Fallback.Enable {
		c.Fallback.NextHop = strings.TrimSpace(c.Fallback.NextHop)
		if c.Fallback.NextHop == "" {
			return fmt.Errorf("customer.mesh.fallback.next_hop required when fallback enabled")
		}
		nextPeer := -1
		for i := range c.Peers {
			if c.Peers[i].DeviceID == c.Fallback.NextHop {
				nextPeer = i
				break
			}
		}
		if nextPeer < 0 {
			return fmt.Errorf("customer.mesh.fallback.next_hop %q must reference a configured peer", c.Fallback.NextHop)
		}
		if c.Peers[nextPeer].HostKey == "" {
			return fmt.Errorf("customer.mesh.fallback.next_hop %q peer must have host_key pinned (no TOFU)", c.Fallback.NextHop)
		}
	}
	c.PrivateKey = strings.TrimSpace(c.PrivateKey)
	c.KeyPassphrase = strings.TrimSpace(c.KeyPassphrase)
	if len(c.Peers) > 0 && c.PrivateKey == "" {
		return fmt.Errorf("customer.mesh.private_key required when peers configured (node identity key)")
	}
	return nil
}

// ValidateMesh 自指拒绝(spec §3.4/§7 层0):peers 指向自己即报错——把自己
// 列为可转发目标只会造出 1 跳自环。selfDeviceID 为空(未注册首启)时跳过
// 检查。出口链路 next_hop 指向自己也在此层拒绝:下一跳=自己等于没换出口,
// 且链式转发里必自指成环。
func (c *Config) ValidateMesh(selfDeviceID string) error {
	if c == nil || c.Customer == nil || c.Customer.Mesh == nil {
		return nil
	}
	// 两侧都规整后比对:手改配置文件可绕过保存期的就地规整,调用方也可能
	// 传入带空白的 selfID——任一侧的空白变体都不许漏检。
	self := strings.TrimSpace(selfDeviceID)
	if self == "" {
		return nil
	}
	for i, peer := range c.Customer.Mesh.Peers {
		if strings.TrimSpace(peer.DeviceID) == self {
			return fmt.Errorf("customer.mesh.peers[%d] device_id %q is self: mesh peers must not reference this device", i, peer.DeviceID)
		}
	}
	if fb := c.Customer.Mesh.Fallback; fb != nil && fb.Enable != nil && *fb.Enable {
		if strings.TrimSpace(fb.NextHop) == self {
			return fmt.Errorf("customer.mesh.fallback.next_hop %q is self: the fallback chain must point at another device", fb.NextHop)
		}
	}
	return nil
}

// SSHIngressDefaultPort 是 SSH 入口的旧默认监听口(与启用段 56431-56433 避让)。
// 56442 历史上是独立监听口;统一口收编后它退化为「旧默认」哨兵——配置翻译
// 层(services.buildSSHIngressRunConfig)把 0 与 56442 一律映射为跟随统一口
// 56432,只有其他显式端口才开专用监听口(存量配置升级不凭空开专用口);
// 另避开 mesh 提案的 beacon UDP 56440。未发布,零兼容负担。
const SSHIngressDefaultPort uint16 = 56442

// SSHIngressConfig 控制 SSH 入口服务面(customer.ssh_ingress)。
// 双密钥准入:authorized_keys=人用凭证(裸流 direct-tcpip 形态);
// mesh_authorized_keys=节点互连凭证(mesh-exit channel(ExtraData)形态)。
// 禁密码。端口默认 56442。
type SSHIngressConfig struct {
	Enable             *bool    `json:"enable,omitempty"` // 三态:nil=false
	Port               uint16   `json:"port,omitempty"`
	AuthorizedKeys     []string `json:"authorized_keys,omitempty"`
	MeshAuthorizedKeys []string `json:"mesh_authorized_keys,omitempty"`
}

// IsEnabled 返回 SSH 入口是否开启。与 PeerMCP 的三态指针同族,但默认反向:
// 入口是新增暴露面,nil=false——总开关默认关,升级零回退。
func (c *SSHIngressConfig) IsEnabled() bool {
	if c == nil || c.Enable == nil {
		return false
	}
	return *c.Enable
}

// Validate 保存通道(Config.Validate)调用。未启用直接过;启用则:
// 两列表合计非空(至少一把准入钥匙,否则开了门谁也进不来);逐条按
// authorized_keys 行格式(gossh.ParseAuthorizedKey,[host:port] 证书主体
// 前缀等合法变体由其原生容忍)校验;同一公钥(TrimSpace 后逐字节同)跨
// 两列表=形态歧义(裸流 direct-tcpip vs mesh-exit 形态),保存时拦——运行期 mesh 优先只是
// 第二道防御(inbound/ssh),配置层先拒绝歧义本身。
// port:uint16 类型上界即 65535,越界值(如 70000)在 JSON 反序列化层即被拒;
// 0=未配,由 EffectiveSSHIngress 归一为默认 56442,因此这里没有端口分支。
func (c *SSHIngressConfig) Validate() error {
	if c == nil || !c.IsEnabled() {
		return nil
	}
	if len(c.AuthorizedKeys) == 0 && len(c.MeshAuthorizedKeys) == 0 {
		return fmt.Errorf("ssh_ingress requires at least one authorized key when enabled (authorized_keys or mesh_authorized_keys)")
	}
	if err := validateSSHIngressKeyLines("authorized_keys", c.AuthorizedKeys); err != nil {
		return err
	}
	if err := validateSSHIngressKeyLines("mesh_authorized_keys", c.MeshAuthorizedKeys); err != nil {
		return err
	}
	meshSeen := make(map[string]struct{}, len(c.MeshAuthorizedKeys))
	for _, key := range c.MeshAuthorizedKeys {
		meshSeen[strings.TrimSpace(key)] = struct{}{}
	}
	for i, key := range c.AuthorizedKeys {
		trimmed := strings.TrimSpace(key)
		if _, ok := meshSeen[trimmed]; ok {
			return fmt.Errorf("ssh_ingress.authorized_keys[%d] key is listed in both authorized_keys and mesh_authorized_keys", i)
		}
	}
	return nil
}

// validateSSHIngressKeyLines 逐条校验 authorized_keys 行:空串/纯空白拒绝
// (Effective 归一虽会剔除空行,但保存通道对显式空条目从严——前端数组残留
// 空串应在保存时报错,不是悄悄落库)。
func validateSSHIngressKeyLines(field string, lines []string) error {
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			return fmt.Errorf("ssh_ingress.%s[%d] cannot be empty", field, i)
		}
		if _, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line)); err != nil {
			return fmt.Errorf("ssh_ingress.%s[%d] is not a valid authorized key line: %v", field, i, err)
		}
	}
	return nil
}

// EffectiveSSHIngress 返回 SSH 入口服务面的归一化运行副本;未配置/未启用
// 返回 nil(服务保持关闭)。归一化:port 0→默认 56442;公钥逐条 TrimSpace、
// 剔空行;切片深拷贝,调用方改副本不影响配置原值。读取端不重复跑 Validate
// (保存通道已拦;手改文件的坏值由 inbound/ssh 装配层的公钥解析兜底报错)。
func (c *Config) EffectiveSSHIngress() *SSHIngressConfig {
	if c == nil || c.Customer == nil || c.Customer.SSHIngress == nil {
		return nil
	}
	src := c.Customer.SSHIngress
	if !src.IsEnabled() {
		return nil
	}
	enable := true
	cfg := SSHIngressConfig{
		Enable:             &enable,
		Port:               src.Port,
		AuthorizedKeys:     normalizeSSHIngressKeyLines(src.AuthorizedKeys),
		MeshAuthorizedKeys: normalizeSSHIngressKeyLines(src.MeshAuthorizedKeys),
	}
	if cfg.Port == 0 {
		cfg.Port = SSHIngressDefaultPort
	}
	return &cfg
}

// normalizeSSHIngressKeyLines TrimSpace 后剔空行,返回新切片(nil 安全)。
func normalizeSSHIngressKeyLines(lines []string) []string {
	normalized := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		normalized = append(normalized, line)
	}
	return normalized
}

// agentCoreSubagentMaxParallelDefault 是并行子 agent 并发闸的默认值（2026-11-10
// vm130 事故）。保守值：主 run + 2 子 agent = 3 路并发，主流账号限额都装得下；
// 账号并发充裕的用户显式配大（如 4）拿回满速并行。
const agentCoreSubagentMaxParallelDefault = 2

// agentCoreSubagentMaxParallelCeiling 是并发闸的防御性上界（保存校验与读取端
// 钳制共用）。Validate 报错文案同样引它，杜绝文案手抄数字漂移（vm129 审计
// 2026-11-10 本批立项理由）。
const agentCoreSubagentMaxParallelCeiling = 8

// EffectiveAgentCoreSubagentMaxParallel 返回并行子 agent 并发闸上限。0 = 未配置
// 走默认 2；负数（手改配置文件绕过保存校验）与越界值同样防御性钳制。
func (c *Config) EffectiveAgentCoreSubagentMaxParallel() int {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return agentCoreSubagentMaxParallelDefault
	}
	v := c.Customer.AgentCore.SubagentMaxParallel
	if v <= 0 {
		return agentCoreSubagentMaxParallelDefault
	}
	if v > agentCoreSubagentMaxParallelCeiling {
		return agentCoreSubagentMaxParallelCeiling
	}
	return v
}

// EffectiveAgentCoreMaxIdenticalToolTurns 返回打转守卫阈值;未配置(0/nil/负数)
// 返回 0,由内核按 defaultMaxIdenticalToolTurns 兜底。读取端做钳制防御:手改
// 配置文件可绕过保存校验,>50 的值收口到 50——越界只会让守卫变弱,不会 crash。
func (c *Config) EffectiveAgentCoreMaxIdenticalToolTurns() int {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return 0
	}
	v := c.Customer.AgentCore.MaxIdenticalToolTurns
	if v < 0 {
		return 0
	}
	if v > 50 {
		return 50
	}
	return v
}

// isLoopbackHostName 判断主机名是否本机回环(IP 回环段或 localhost)。
// 注意:仓库里有三处回环判定——services 的 isLoopbackHost
// (agent_ai_proxy_guard.go:69,窄口径:127.0.0.1/::1/localhost 字面量)、
// 本函数(config 包,宽口径:全回环段+localhost)、前端的
// utils/agentCoreSettings.js isLoopbackHost(与本函数同口径)。口径分歧是
// 有意的:本函数与前端同源(校验语义),services 那个只服务 56432 特判。
func isLoopbackHostName(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type AliangServerConfig struct {
	Type        string `json:"type"`
	CoreServer  string `json:"core_server,omitempty"`
	AgentServer string `json:"agent_server,omitempty"`
}

type CustomerProxyConfig struct {
	Enable        *bool              `json:"enable,omitempty"`
	Type          string             `json:"type"`
	Server        string             `json:"server,omitempty"`
	Username      string             `json:"username,omitempty"`
	Password      string             `json:"password,omitempty"`
	PrivateKey    string             `json:"private_key,omitempty"`
	KeyPassphrase string             `json:"key_passphrase,omitempty"`
	PoolSize      int                `json:"pool_size,omitempty"`
	JumpServers   []SSHJumpServerRaw `json:"jump_servers,omitempty"`
}

// SSHJumpServerRaw 是 customer.proxy.jump_servers 的原始(未解析)形态:
// server 允许 ssh://user:pass@host:port URL 形态或 host:port 裸形态,
// EffectiveSSHProxy 统一解析成 SSHJumpServer(裸/解析分型,与
// CustomerProxyConfig vs SSHProxyConfig 的分层同构——裸形态 server 是一个
// 串,解析形态是 host+port 拆分,共用一型会让同一字段承载两种语义)。
type SSHJumpServerRaw struct {
	Server        string `json:"server,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
	KeyPassphrase string `json:"key_passphrase,omitempty"`
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

// GetAuthScanInitURL 扫码登录：PC 端初始化（换取 device_code + 二维码 scan_code）。
// 注意：官方后端的扫码路由挂在 /auth/scan/*（与 /api/v1/auth/* 同主机），不带 /api/v1 前缀。
func (c *Config) GetAuthScanInitURL() string {
	return fmt.Sprintf("%s/auth/scan/init", c.APIBaseURL())
}

// GetAuthScanStatusURL 扫码登录：PC 端按 device_code 轮询状态（authorized 时下发 st_ + refresh_token）。
func (c *Config) GetAuthScanStatusURL() string {
	return fmt.Sprintf("%s/auth/scan/status", c.APIBaseURL())
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

func (c *Config) GetAgentDeviceReleaseURL() string {
	return fmt.Sprintf("%s/api/v1/agent/devices/release", c.AgentBaseURL())
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
		case *SSHProxyConfig:
			if err := typedProxy.Validate(); err != nil {
				return fmt.Errorf("invalid sshProxy configuration: %w", err)
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
	if c.Customer != nil && c.Customer.AgentCoreLLM != nil {
		if err := c.Customer.AgentCoreLLM.Validate(); err != nil {
			return fmt.Errorf("invalid agentcore_llm configuration: %w", err)
		}
	}
	if c.Customer != nil && c.Customer.AgentCore != nil {
		if err := c.Customer.AgentCore.Validate(); err != nil {
			return fmt.Errorf("invalid agentcore configuration: %w", err)
		}
	}
	if c.Customer != nil {
		if modelMapping := c.Customer.ModelMapping; modelMapping != nil {
			if err := modelMapping.Validate(); err != nil {
				return fmt.Errorf("invalid model_mapping configuration: %w", err)
			}
		}
		if customEnvVars := c.Customer.CustomEnvVars; customEnvVars != nil {
			if err := customEnvVars.Validate(); err != nil {
				return fmt.Errorf("invalid custom_env_vars configuration: %w", err)
			}
		}
		if geoipSettings := c.Customer.GeoIP; geoipSettings != nil {
			if err := geoipSettings.Validate(); err != nil {
				return fmt.Errorf("invalid geoip configuration: %w", err)
			}
		}
		if mesh := c.Customer.Mesh; mesh != nil {
			if err := mesh.Validate(); err != nil {
				return fmt.Errorf("invalid customer.mesh configuration: %w", err)
			}
		}
		if sshIngress := c.Customer.SSHIngress; sshIngress != nil {
			if err := sshIngress.Validate(); err != nil {
				return fmt.Errorf("invalid ssh_ingress configuration: %w", err)
			}
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
		case "http", "socks5", "ssh":
		default:
			return fmt.Errorf("customer.proxy.type must be one of [http socks5 ssh], got %q", c.Customer.Proxy.Type)
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
			if _, _, err := match.ClassifyEntry(rule.Include[i]); err != nil {
				return fmt.Errorf("customer.ai_rules.%s.include[%d] invalid %q: %v", provider, i, rule.Include[i], err)
			}
		}
	}

	for i := range c.Customer.ProxyRules {
		c.Customer.ProxyRules[i] = strings.TrimSpace(c.Customer.ProxyRules[i])
		if c.Customer.ProxyRules[i] == "" {
			return fmt.Errorf("customer.proxy_rules[%d] cannot be empty", i)
		}
		if _, _, err := match.ClassifyEntry(c.Customer.ProxyRules[i]); err != nil {
			return fmt.Errorf("customer.proxy_rules[%d] invalid %q: %v", i, c.Customer.ProxyRules[i], err)
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
		return fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop model_mapping custom_env_vars geoip agentcore_llm agentcore peer_mcp mesh ssh_ingress]", sorted[0])
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
		case "proxy", "ai_rules", "proxy_rules", "traffic_mirror", "http1_drop", "model_mapping", "custom_env_vars", "geoip", "agentcore_llm", "agentcore", "peer_mcp", "mesh", "ssh_ingress":
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
	case "ssh":
		if normalizedScheme != "ssh" {
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

// EffectiveSSHProxy derives the SSH outbound config from customer.proxy.
// pool_size 解析层保持原值(0 即 0):默认 3 与 1..16 钳位由 ssh.New 承担
// (registry 契约)——解析层不偷偷改值,保证配置回显与用户输入一致。
func (c *Config) EffectiveSSHProxy() (*SSHProxyConfig, error) {
	if c == nil || c.Customer == nil || c.Customer.Proxy == nil {
		return nil, nil
	}
	if !c.Customer.Proxy.IsEnabled() {
		return nil, nil
	}
	if strings.ToLower(strings.TrimSpace(c.Customer.Proxy.Type)) != "ssh" {
		return nil, nil
	}

	raw := c.Customer.Proxy
	serverHost, serverPort, username, password, err := parseCustomerProxyServer(
		"ssh",
		raw.Server,
		raw.Username,
		raw.Password,
	)
	if err != nil {
		return nil, fmt.Errorf("customer.proxy.server: %w", err)
	}

	jumps := make([]SSHJumpServer, 0, len(raw.JumpServers))
	for i, rawJump := range raw.JumpServers {
		jumpHost, jumpPort, jumpUser, jumpPass, err := parseCustomerProxyServer(
			"ssh",
			rawJump.Server,
			rawJump.Username,
			rawJump.Password,
		)
		if err != nil {
			return nil, fmt.Errorf("customer.proxy.jump_servers[%d].server: %w", i, err)
		}
		jumps = append(jumps, SSHJumpServer{
			Server:        jumpHost,
			ServerPort:    jumpPort,
			Username:      jumpUser,
			Password:      jumpPass,
			PrivateKey:    rawJump.PrivateKey,
			KeyPassphrase: rawJump.KeyPassphrase,
		})
	}

	return &SSHProxyConfig{
		Server:        serverHost,
		ServerPort:    serverPort,
		Username:      username,
		Password:      password,
		PrivateKey:    raw.PrivateKey,
		KeyPassphrase: raw.KeyPassphrase,
		PoolSize:      raw.PoolSize,
		JumpServers:   jumps,
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
	case "ssh":
		return c.EffectiveSSHProxy()
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

// EffectiveModelMapping returns the model mapping config when rewriting is
// enabled, otherwise nil. Callers should also handle an empty EffectiveRules
// result as a no-op.
func (c *Config) EffectiveModelMapping() *ModelMappingConfig {
	if c == nil || c.Customer == nil || c.Customer.ModelMapping == nil {
		return nil
	}
	if !c.Customer.ModelMapping.IsEnabled() {
		return nil
	}
	cfg := *c.Customer.ModelMapping
	return &cfg
}

// EffectiveCustomEnvVars returns the cleaned custom env-var overrides to apply to
// AI CLI child processes (claude/codex) when the feature is enabled, otherwise nil.
// Callers should treat a nil/empty result as a no-op.
func (c *Config) EffectiveCustomEnvVars() map[string]string {
	if c == nil || c.Customer == nil || c.Customer.CustomEnvVars == nil {
		return nil
	}
	if !c.Customer.CustomEnvVars.IsEnabled() {
		return nil
	}
	return c.Customer.CustomEnvVars.EffectiveVars()
}

// EffectiveGeoIPDownloadURL returns the configured GeoIP database download URL
// (customer.geoip.download_url, trimmed), or "" when unset. The env override
// ALIANG_GEOIP_DOWNLOAD_URL keeps higher priority at consumption time.
func (c *Config) EffectiveGeoIPDownloadURL() string {
	if c == nil || c.Customer == nil || c.Customer.GeoIP == nil {
		return ""
	}
	return c.Customer.GeoIP.EffectiveDownloadURL()
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
