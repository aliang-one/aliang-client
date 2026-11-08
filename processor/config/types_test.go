package config

import (
	"encoding/json"
	"strings"
	"testing"

	"aliang.one/nursorgate/processor/geoip"
)

// TestShadowTLSPluginOptsValidate tests ShadowTLSPluginOpts.Validate()
func TestShadowTLSPluginOptsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    *ShadowTLSPluginOpts
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid configuration",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "SecurePassword123",
				Version:  3,
			},
			wantErr: false,
		},
		{
			name:    "nil opts",
			opts:    nil,
			wantErr: true,
			errMsg:  "plugin_opts is required when plugin='shadow-tls'",
		},
		{
			name: "missing host",
			opts: &ShadowTLSPluginOpts{
				Password: "SecurePassword123",
				Version:  3,
			},
			wantErr: true,
			errMsg:  "plugin_opts.host is required",
		},
		{
			name: "empty password",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "",
				Version:  3,
			},
			wantErr: true,
			errMsg:  "plugin_opts.password is required and cannot be empty",
		},
		{
			name: "password too short",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "short",
				Version:  3,
			},
			wantErr: true,
			errMsg:  "plugin_opts.password must be at least 8 characters",
		},
		{
			name: "invalid version 0",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "SecurePassword123",
				Version:  0,
			},
			wantErr: true,
			errMsg:  "plugin_opts.version must be 1, 2, or 3",
		},
		{
			name: "invalid version 4",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "SecurePassword123",
				Version:  4,
			},
			wantErr: true,
			errMsg:  "plugin_opts.version must be 1, 2, or 3",
		},
		{
			name: "version 1 is valid",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "SecurePassword123",
				Version:  1,
			},
			wantErr: false,
		},
		{
			name: "version 2 is valid",
			opts: &ShadowTLSPluginOpts{
				Host:     "www.bing.com",
				Password: "SecurePassword123",
				Version:  2,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

// TestShadowsocksConfigValidate tests ShadowsocksConfig.Validate()
func TestShadowsocksConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *ShadowsocksConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config without plugin",
			config: &ShadowsocksConfig{
				Server:     "192.168.1.100",
				ServerPort: 8388,
				Method:     "aes-256-gcm",
				Password:   "MyPassword123",
			},
			wantErr: false,
		},
		{
			name: "valid config with shadow-tls plugin",
			config: &ShadowsocksConfig{
				Server:     "151.242.165.151",
				ServerPort: 443,
				Method:     "chacha20-ietf-poly1305",
				Password:   "I8U3GD4pziEyIeQwTqd52CGLisU5boCwg6FBU9KpARs=",
				Plugin:     "shadow-tls",
				PluginOpts: &ShadowTLSPluginOpts{
					Host:     "www.bing.com",
					Password: "I8U3GD4pziEyIeQwTqd52CGLisU5boCwg6FBU9KpARs=",
					Version:  3,
				},
			},
			wantErr: false,
		},
		{
			name: "missing server_host",
			config: &ShadowsocksConfig{
				ServerPort: 8388,
				Method:     "aes-256-gcm",
				Password:   "MyPassword123",
			},
			wantErr: true,
			errMsg:  "server_host is required",
		},
		{
			name: "missing server_port",
			config: &ShadowsocksConfig{
				Server:   "192.168.1.100",
				Method:   "aes-256-gcm",
				Password: "MyPassword123",
			},
			wantErr: true,
			errMsg:  "server_port is required",
		},
		{
			name: "missing method",
			config: &ShadowsocksConfig{
				Server:     "192.168.1.100",
				ServerPort: 8388,
				Password:   "MyPassword123",
			},
			wantErr: true,
			errMsg:  "method is required",
		},
		{
			name: "missing password",
			config: &ShadowsocksConfig{
				Server:     "192.168.1.100",
				ServerPort: 8388,
				Method:     "aes-256-gcm",
			},
			wantErr: true,
			errMsg:  "password is required",
		},
		{
			name: "shadow-tls plugin without plugin_opts",
			config: &ShadowsocksConfig{
				Server:     "151.242.165.151",
				ServerPort: 443,
				Method:     "chacha20-ietf-poly1305",
				Password:   "MyPassword123",
				Plugin:     "shadow-tls",
			},
			wantErr: true,
			errMsg:  "plugin_opts is required when plugin='shadow-tls'",
		},
		{
			name: "unsupported plugin",
			config: &ShadowsocksConfig{
				Server:     "151.242.165.151",
				ServerPort: 443,
				Method:     "chacha20-ietf-poly1305",
				Password:   "MyPassword123",
				Plugin:     "unsupported-plugin",
			},
			wantErr: true,
			errMsg:  "unsupported plugin: unsupported-plugin",
		},
		{
			name: "shadow-tls plugin with invalid plugin_opts",
			config: &ShadowsocksConfig{
				Server:     "151.242.165.151",
				ServerPort: 443,
				Method:     "chacha20-ietf-poly1305",
				Password:   "MyPassword123",
				Plugin:     "shadow-tls",
				PluginOpts: &ShadowTLSPluginOpts{
					Host:     "",
					Password: "short",
					Version:  3,
				},
			},
			wantErr: true,
			errMsg:  "plugin_opts.host is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

// TestSocks5ConfigValidate tests Socks5Config.Validate()
func TestSocks5ConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Socks5Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid without auth",
			config: &Socks5Config{
				Server:     "127.0.0.1",
				ServerPort: 1080,
			},
			wantErr: false,
		},
		{
			name: "valid with auth",
			config: &Socks5Config{
				Server:     "127.0.0.1",
				ServerPort: 1080,
				Username:   "user",
				Password:   "pass",
			},
			wantErr: false,
		},
		{
			name: "missing server_host",
			config: &Socks5Config{
				ServerPort: 1080,
			},
			wantErr: true,
			errMsg:  "server_host is required",
		},
		{
			name: "missing server_port",
			config: &Socks5Config{
				Server: "127.0.0.1",
			},
			wantErr: true,
			errMsg:  "server_port is required",
		},
		{
			name: "username without password",
			config: &Socks5Config{
				Server:     "127.0.0.1",
				ServerPort: 1080,
				Username:   "user",
			},
			wantErr: true,
			errMsg:  "username and password must be provided together",
		},
		{
			name: "password without username",
			config: &Socks5Config{
				Server:     "127.0.0.1",
				ServerPort: 1080,
				Password:   "pass",
			},
			wantErr: true,
			errMsg:  "username and password must be provided together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestConfigValidate_NewModelHelpers_ExposeRuntimeValues(t *testing.T) {
	payload := []byte(`{
		"core": {
			"api_server": "https://api.aliang.one",
			"aliangServer": {
				"type": "aliang",
				"core_server": "ai-gateway.aliang.one:443"
			}
		},
		"customer": {
			"proxy": {
				"type": "socks5",
				"server": "127.0.0.1:1080",
				"username": "u",
				"password": "p"
			},
			"ai_rules": {
				"openai": {
					"enble": true,
					"exclude": ["api.openai.com", "cdn.openai.com"]
				},
				"claude": {
					"enble": false,
					"exclude": ["claude.ai"]
				}
			},
			"proxy_rules": ["domains,cursor.com,proxy"]
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if cfg.EffectiveDefaultProxy() != "direct" {
		t.Fatalf("EffectiveDefaultProxy() = %q, want direct", cfg.EffectiveDefaultProxy())
	}
	socksCfg, err := cfg.EffectiveSocksProxy()
	if err != nil {
		t.Fatalf("EffectiveSocksProxy() error = %v", err)
	}
	if socksCfg == nil {
		t.Fatal("EffectiveSocksProxy() = nil, want derived socks config")
	}
	if socksCfg.Server != "127.0.0.1" || socksCfg.ServerPort != 1080 {
		t.Fatalf("EffectiveSocksProxy() = %#v, want host 127.0.0.1 port 1080", socksCfg)
	}
	if got := cfg.EffectiveAliangCoreServer(); got != "ai-gateway.aliang.one:443" {
		t.Fatalf("EffectiveAliangCoreServer() = %q", got)
	}
	if got := cfg.EffectiveAIAllowlist(); len(got) != 2 {
		t.Fatalf("EffectiveAIAllowlist len = %d, want 2", len(got))
	}

	if len(cfg.Customer.ProxyRules) != 1 || cfg.Customer.ProxyRules[0] != "domains,cursor.com,proxy" {
		t.Fatalf("ProxyRules = %v, want [domains,cursor.com,proxy]", cfg.Customer.ProxyRules)
	}
}

func TestConfigValidate_CustomerProxyTypeAcceptsSocks5(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "socks5",
				"server": "127.0.0.1:1080"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil for socks5", err)
	}
}

func TestConfigValidate_CustomerProxyTypeAcceptsHTTP(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "127.0.0.1:8080",
				"username": "u",
				"password": "p"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for http", err)
	}

	httpCfg, err := cfg.EffectiveHTTPProxy()
	if err != nil {
		t.Fatalf("EffectiveHTTPProxy() error = %v", err)
	}
	if httpCfg == nil {
		t.Fatal("EffectiveHTTPProxy() = nil, want derived http config")
	}
	if httpCfg.Server != "127.0.0.1" || httpCfg.ServerPort != 8080 {
		t.Fatalf("EffectiveHTTPProxy() = %#v, want host 127.0.0.1 port 8080", httpCfg)
	}
}

func TestConfigValidate_CustomerProxyAcceptsDomainServer(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "socks5",
				"server": "cd.liangsqrt.com:27750"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for domain host", err)
	}

	socksCfg, err := cfg.EffectiveSocksProxy()
	if err != nil {
		t.Fatalf("EffectiveSocksProxy() error = %v", err)
	}
	if socksCfg == nil {
		t.Fatal("EffectiveSocksProxy() = nil, want derived socks config")
	}
	if socksCfg.Server != "cd.liangsqrt.com" || socksCfg.ServerPort != 27750 {
		t.Fatalf("EffectiveSocksProxy() = %#v, want cd.liangsqrt.com:27750", socksCfg)
	}
}

func TestConfigValidate_CustomerProxyParsesAuthFromURLServer(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "http://user:pass@cd.liangsqrt.com:27750"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for URL proxy server", err)
	}

	httpCfg, err := cfg.EffectiveHTTPProxy()
	if err != nil {
		t.Fatalf("EffectiveHTTPProxy() error = %v", err)
	}
	if httpCfg == nil {
		t.Fatal("EffectiveHTTPProxy() = nil, want derived http config")
	}
	if httpCfg.Server != "cd.liangsqrt.com" || httpCfg.ServerPort != 27750 {
		t.Fatalf("EffectiveHTTPProxy() = %#v, want cd.liangsqrt.com:27750", httpCfg)
	}
	if httpCfg.Username != "user" || httpCfg.Password != "pass" {
		t.Fatalf("EffectiveHTTPProxy() auth = %q/%q, want user/pass", httpCfg.Username, httpCfg.Password)
	}
}

func TestConfigValidate_CustomerProxyExplicitAuthOverridesURLAuth(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "socks5",
				"server": "socks5://url-user:url-pass@cd.liangsqrt.com:27750",
				"username": "field-user",
				"password": "field-pass"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	socksCfg, err := cfg.EffectiveSocksProxy()
	if err != nil {
		t.Fatalf("EffectiveSocksProxy() error = %v", err)
	}
	if socksCfg.Username != "field-user" || socksCfg.Password != "field-pass" {
		t.Fatalf("EffectiveSocksProxy() auth = %q/%q, want field-user/field-pass", socksCfg.Username, socksCfg.Password)
	}
}

func TestConfigValidate_CustomerProxyRejectsMismatchedURLScheme(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "socks5://user:pass@cd.liangsqrt.com:27750"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want scheme mismatch error")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidate_CustomerHTTPProxyRequiresServer(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want missing server error")
	}
	if !strings.Contains(err.Error(), "customer.proxy.server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidate_ForbidUnknownCustomerField(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "127.0.0.1:1080"
			},
			"forbidden": true
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want forbidden field error")
	}
	if !strings.Contains(err.Error(), "customer.forbidden is forbidden") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidate_ForbidUnknownAIRulesField(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "127.0.0.1:1080"
			},
			"ai_rules": {
				"openai": {
					"enble": true,
					"exclude": ["api.openai.com"],
					"mode": "all"
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want forbidden ai_rules field error")
	}
	if !strings.Contains(err.Error(), "customer.ai_rules.openai.mode is forbidden") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidate_AIRulesEnableRequired(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "127.0.0.1:1080"
			},
			"ai_rules": {
				"openai": {
					"exclude": ["api.openai.com"]
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want missing enable error")
	}
	if !strings.Contains(err.Error(), "customer.ai_rules.openai.enble is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidate_AIRulesEditableAccepted(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "http",
				"server": "127.0.0.1:1080"
			},
			"ai_rules": {
				"openai": {
					"enble": true,
					"include": ["api.openai.com"],
					"editable": false
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	rule := cfg.Customer.AIRules["openai"]
	if rule == nil || rule.Editable == nil || *rule.Editable {
		t.Fatalf("rule.Editable = %+v, want false", rule)
	}
}

func TestConfigEffectiveHTTP1Drop_DefaultsToMetric(t *testing.T) {
	cfg := &Config{}
	http1Drop := cfg.EffectiveHTTP1Drop()
	if http1Drop == nil {
		t.Fatal("EffectiveHTTP1Drop() = nil, want default config")
	}
	if !http1Drop.IsEnabled() {
		t.Fatal("EffectiveHTTP1Drop().IsEnabled() = false, want true")
	}
	if got := http1Drop.EffectivePathContains(); len(got) != 1 || got[0] != "metric" {
		t.Fatalf("EffectivePathContains() = %v, want [metric]", got)
	}
}

func TestConfigValidate_HTTP1DropAcceptsCustomRules(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"http1_drop": {
				"enabled": true,
				"path_contains": ["metric", "telemetry"]
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	got := cfg.EffectiveHTTP1Drop().EffectivePathContains()
	if len(got) != 2 || got[0] != "metric" || got[1] != "telemetry" {
		t.Fatalf("EffectivePathContains() = %v, want [metric telemetry]", got)
	}
}

func TestConfigValidate_HTTP1DropRejectsBlankRule(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"http1_drop": {
				"enabled": true,
				"path_contains": ["metric", " "]
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want blank http1_drop rule error")
	}
	if !strings.Contains(err.Error(), "http1_drop.path_contains[1] cannot be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigEffectiveModelMapping_DisabledByDefault(t *testing.T) {
	cfg := &Config{}
	if got := cfg.EffectiveModelMapping(); got != nil {
		t.Fatalf("EffectiveModelMapping() = %v, want nil by default", got)
	}
}

func TestConfigValidate_ModelMappingAcceptsRules(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"model_mapping": {
				"enable": true,
				"rules": {"gpt-4": "gpt-4o", "  claude-3  ": "  claude-3-5  "}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	effective := cfg.EffectiveModelMapping()
	if effective == nil {
		t.Fatal("EffectiveModelMapping() = nil, want enabled config")
	}
	rules := effective.EffectiveRules()
	if rules["gpt-4"] != "gpt-4o" {
		t.Fatalf("rules[gpt-4] = %q, want gpt-4o", rules["gpt-4"])
	}
	if rules["claude-3"] != "claude-3-5" {
		t.Fatalf("trimmed rule = %q, want claude-3-5", rules["claude-3"])
	}

	roundTripRaw, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(roundTripRaw), `"enable":true`) {
		t.Fatalf("marshaled config = %s, want model_mapping.enable true", roundTripRaw)
	}
	if strings.Contains(string(roundTripRaw), `"enabled"`) {
		t.Fatalf("marshaled config = %s, want no legacy model_mapping.enabled key", roundTripRaw)
	}

	var roundTrip Config
	if err := json.Unmarshal(roundTripRaw, &roundTrip); err != nil {
		t.Fatalf("round-trip json.Unmarshal() error = %v", err)
	}
	if got := roundTrip.EffectiveModelMapping(); got == nil {
		t.Fatal("round-trip EffectiveModelMapping() = nil, want enabled config")
	}
}

func TestConfigValidate_ModelMappingAcceptsLegacyEnabledAlias(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"model_mapping": {
				"enabled": true,
				"rules": {"src": "dst"}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := cfg.EffectiveModelMapping(); got == nil {
		t.Fatal("EffectiveModelMapping() = nil, want legacy enabled alias to enable config")
	}

	roundTripRaw, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(roundTripRaw), `"enable":true`) {
		t.Fatalf("marshaled config = %s, want canonical model_mapping.enable true", roundTripRaw)
	}
	if strings.Contains(string(roundTripRaw), `"enabled"`) {
		t.Fatalf("marshaled config = %s, want no legacy model_mapping.enabled key", roundTripRaw)
	}
}

func TestConfigValidate_ModelMappingRejectsBlankRule(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"model_mapping": {
				"enable": true,
				"rules": {"gpt-4": "  "}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want blank model_mapping rule error")
	}
	if !strings.Contains(err.Error(), "model_mapping") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigValidate_ModelMappingDisabledSkipsValidation(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"model_mapping": {"enable": false, "rules": {"": ""}}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil when disabled", err)
	}
	if got := cfg.EffectiveModelMapping(); got != nil {
		t.Fatalf("EffectiveModelMapping() = %v, want nil when disabled", got)
	}
}

func TestConfigEffectiveCustomEnvVars(t *testing.T) {
	t.Run("nil by default", func(t *testing.T) {
		cfg := &Config{}
		if got := cfg.EffectiveCustomEnvVars(); got != nil {
			t.Fatalf("EffectiveCustomEnvVars() = %v, want nil by default", got)
		}
	})

	t.Run("returns cleaned vars when enabled", func(t *testing.T) {
		enabled := true
		cfg := &Config{Customer: &CustomerConfig{
			CustomEnvVars: &CustomEnvVarsConfig{
				Enabled: &enabled,
				Vars: map[string]string{
					"ANTHROPIC_BASE_URL": "https://gw.example",
					"  ":                 "dropped-empty-key",
					"FOO":                "bar=baz",
				},
			},
		}}
		got := cfg.EffectiveCustomEnvVars()
		if len(got) != 2 {
			t.Fatalf("EffectiveCustomEnvVars() = %v, want 2 entries (empty key dropped)", got)
		}
		if got["ANTHROPIC_BASE_URL"] != "https://gw.example" {
			t.Fatalf("ANTHROPIC_BASE_URL = %q, want https://gw.example", got["ANTHROPIC_BASE_URL"])
		}
		if got["FOO"] != "bar=baz" {
			t.Fatalf("FOO = %q, value with '=' must be preserved verbatim", got["FOO"])
		}
	})

	t.Run("nil when enabled but no vars", func(t *testing.T) {
		enabled := true
		cfg := &Config{Customer: &CustomerConfig{
			CustomEnvVars: &CustomEnvVarsConfig{Enabled: &enabled},
		}}
		if got := cfg.EffectiveCustomEnvVars(); got != nil {
			t.Fatalf("EffectiveCustomEnvVars() = %v, want nil when no vars", got)
		}
	})
}

func TestConfigValidate_CustomEnvVars(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string // substr; empty means no error expected
	}{
		{
			name:    "accepts valid vars including value with equals",
			payload: `{"core":{"api_server":"https://api.aliang.one"},"customer":{"custom_env_vars":{"enable":true,"vars":{"ANTHROPIC_BASE_URL":"https://x","FOO":"a=b"}}}}`,
		},
		{
			name:    "rejects empty variable name",
			payload: `{"core":{"api_server":"https://api.aliang.one"},"customer":{"custom_env_vars":{"enable":true,"vars":{"":"v"}}}}`,
			wantErr: "custom_env_vars",
		},
		{
			name:    "rejects variable name containing equals",
			payload: `{"core":{"api_server":"https://api.aliang.one"},"customer":{"custom_env_vars":{"enable":true,"vars":{"BAD=KEY":"v"}}}}`,
			wantErr: "custom_env_vars",
		},
		{
			name:    "disabled skips validation",
			payload: `{"core":{"api_server":"https://api.aliang.one"},"customer":{"custom_env_vars":{"enable":false,"vars":{"":"v"}}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tc.payload), &cfg); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Fatal("Validate() error = nil, want error")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Validate() error = %v, want substring %q", err, tc.wantErr)
				}
			}
		})
	}
}

func TestConfigAgentBaseURL(t *testing.T) {
	cfg := &Config{Core: &CoreConfig{APIServer: "https://api.example.com/"}}
	if got := cfg.AgentBaseURL(); got != DefaultAgentServerURL {
		t.Fatalf("AgentBaseURL() fallback = %q, want %s", got, DefaultAgentServerURL)
	}

	cfg.Core.AgentServer = "https://agent.example.com/"
	if got := cfg.AgentBaseURL(); got != "https://agent.example.com" {
		t.Fatalf("AgentBaseURL() explicit = %q, want https://agent.example.com", got)
	}

	cfg.Core.AgentServer = ""
	cfg.Core.AliangServer = &AliangServerConfig{AgentServer: "http://localhost:4000/"}
	if got := cfg.AgentBaseURL(); got != "http://localhost:4000" {
		t.Fatalf("AgentBaseURL() aliangServer fallback = %q, want http://localhost:4000", got)
	}
}

func TestConfigValidateAllowsMinimalAgentConfigWithoutCustomer(t *testing.T) {
	cfg := &Config{
		Core: &CoreConfig{
			APIServer:   "http://127.0.0.1:4106",
			AgentServer: "http://127.0.0.1:4106",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for minimal agent config", err)
	}
}

func TestConfigValidate_AIRulesIncludeRejectsInvalidIPEntry(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"ai_rules": {
				"openai": {
					"enble": true,
					"include": ["api.openai.com", "ip-cidr:abc"]
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want invalid include entry error")
	}
	if !strings.Contains(err.Error(), `customer.ai_rules.openai.include[1] invalid "ip-cidr:abc"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
	if !strings.Contains(err.Error(), "非法 IP 条件") {
		t.Fatalf("error should carry the underlying reason: %v", err)
	}
}

func TestConfigValidate_AIRulesExcludeAliasFoldsIntoIncludeValidation(t *testing.T) {
	// legacy "exclude" key is folded into Include when include is absent
	// (CustomerAIRuleSetting.UnmarshalJSON), so its entries must go through
	// the same include-entry validation.
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"ai_rules": {
				"openai": {
					"enble": true,
					"exclude": ["a..b"]
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want invalid exclude-alias entry error")
	}
	if !strings.Contains(err.Error(), `customer.ai_rules.openai.include[0] invalid "a..b"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
	if !strings.Contains(err.Error(), "空标签") {
		t.Fatalf("error should carry the underlying reason: %v", err)
	}
}

func TestConfigValidate_ProxyRulesRejectMalformedDomain(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy_rules": ["domains,google.com", "a..b"]
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want malformed proxy_rules entry error")
	}
	if !strings.Contains(err.Error(), `customer.proxy_rules[1] invalid "a..b"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
	if !strings.Contains(err.Error(), "空标签") {
		t.Fatalf("error should carry the underlying reason: %v", err)
	}
}

func TestConfigValidate_ProxyRulesRejectReversedIPRange(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy_rules": ["ip-from:1.2.3.254,ip-to:1.2.3.1"]
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want reversed ip range error")
	}
	if !strings.Contains(err.Error(), `customer.proxy_rules[0] invalid "ip-from:1.2.3.254,ip-to:1.2.3.1"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
	if !strings.Contains(err.Error(), "起点大于终点") {
		t.Fatalf("error should carry the underlying reason: %v", err)
	}
}

func TestConfigValidate_ProxyRulesAcceptMixedLegacyAndNewSyntax(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy_rules": ["domains,google.com", "aliang.*", "ip-cidr,121.32.12.0/24,proxy"],
			"ai_rules": {
				"openai": {
					"enble": true,
					"include": ["api.openai.com", "*.cursor.sh", "ip-cidr:121.32.12.0/24", "1.2.3.4"]
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for mixed legal entries", err)
	}
}

func TestConfigValidate_CustomerRulesAcceptGeoIPEntries(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy_rules": ["geoip:CN+HK+US", "GEOIP,CN,proxy"],
			"ai_rules": {
				"openai": {
					"enble": true,
					"include": ["geoip:JP"]
				}
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for legal geoip entries on customer surface", err)
	}
}

func TestConfigValidate_CustomerRulesRejectInvalidCountryCode(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy_rules": ["geoip:CHN"]
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want invalid country code error")
	}
	if !strings.Contains(err.Error(), `customer.proxy_rules[0] invalid "geoip:CHN"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
	if !strings.Contains(err.Error(), "非法国家码") {
		t.Fatalf("error should carry the underlying reason: %v", err)
	}
}

func TestTrafficMirrorConfigValidate_RejectsMalformedDomain(t *testing.T) {
	cfg := &TrafficMirrorConfig{
		Enabled: true,
		Target:  "http://127.0.0.1:1/mirror",
		Domains: []string{"*.cursor.sh", "a..b"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want malformed mirror domain error")
	}
	if !strings.Contains(err.Error(), `traffic_mirror.domains[1] invalid "a..b"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
	if !strings.Contains(err.Error(), "空标签") {
		t.Fatalf("error should carry the underlying reason: %v", err)
	}
}

func TestTrafficMirrorConfigValidate_AcceptsGlobAndIPEntries(t *testing.T) {
	cfg := &TrafficMirrorConfig{
		Enabled: true,
		Target:  "http://127.0.0.1:1/mirror",
		Domains: []string{"*.cursor.sh", "api.cursor.com", "121.32.12.0/24"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for legal glob/IP domains", err)
	}
}

func TestTrafficMirrorConfigValidate_RejectsGeoIPEntry(t *testing.T) {
	// 冒号形态与 Clash 逗号形态都必须真正走到拒绝分支（各自独立构造，防止前一形态提前返回掩盖后者）。
	for _, entry := range []string{"geoip:CN", "GEOIP,CN,proxy"} {
		cfg := &TrafficMirrorConfig{
			Enabled: true,
			Target:  "http://127.0.0.1:1/mirror",
			Domains: []string{"*.cursor.sh", entry},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("Validate() error = nil for %q, want geoip mirror entry rejection", entry)
		}
		if !strings.Contains(err.Error(), `traffic_mirror.domains[1] invalid "`+entry+`"`) {
			t.Fatalf("error should carry entry index and raw text: %v", err)
		}
		if !strings.Contains(err.Error(), "镜像列表不支持 geoip 条目") {
			t.Fatalf("error should state mirror list does not support geoip entries: %v", err)
		}
	}
}

func TestConfigValidate_TrafficMirrorDomainsValidatedThroughChain(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"traffic_mirror": {
				"enabled": true,
				"target": "http://127.0.0.1:1/mirror",
				"domains": ["a..b"]
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want traffic_mirror domain error surfaced through Config.Validate")
	}
	if !strings.Contains(err.Error(), "invalid traffic_mirror configuration") {
		t.Fatalf("error should be wrapped by the traffic_mirror chain: %v", err)
	}
	if !strings.Contains(err.Error(), `traffic_mirror.domains[0] invalid "a..b"`) {
		t.Fatalf("error should carry entry index and raw text: %v", err)
	}
}

func TestConfigValidate_CustomerGeoIPDownloadURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string // 空 = 期望通过
	}{
		{name: "https accepted", url: "https://mirrors.example.com/GeoLite2-Country.mmdb"},
		{name: "empty accepted", url: ""},
		{name: "blank accepted", url: "   "},
		{name: "http rejected with original url", url: "http://mirrors.example.com/x.mmdb", wantErr: "http://mirrors.example.com/x.mmdb"},
		{name: "ftp rejected", url: "ftp://mirrors.example.com/x.mmdb", wantErr: "ftp://mirrors.example.com/x.mmdb"},
		{name: "schemeless rejected", url: "mirrors.example.com/x.mmdb", wantErr: "mirrors.example.com/x.mmdb"},
		{name: "hostless rejected", url: "https:///x.mmdb", wantErr: "https:///x.mmdb"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{
				"core": {"api_server": "https://api.aliang.one"},
				"customer": {"geoip": {"download_url": "` + tc.url + `"}}
			}`)
			var cfg Config
			if err := json.Unmarshal(payload, &cfg); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() error = nil, want geoip download_url error")
			}
			if !strings.Contains(err.Error(), "geoip") || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigValidate_CustomerGeoIPAcceptedAsEditableField(t *testing.T) {
	// geoip 此前属 customer 未知键（被白名单拒绝）；加入白名单后整段通过校验
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {"geoip": {"download_url": "https://mirrors.example.com/GeoLite2-Country.mmdb"}}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if got := cfg.EffectiveGeoIPDownloadURL(); got != "https://mirrors.example.com/GeoLite2-Country.mmdb" {
		t.Fatalf("EffectiveGeoIPDownloadURL() = %q", got)
	}
}

func TestConfigEffectiveGeoIPDownloadURL(t *testing.T) {
	var nilCfg *Config
	if got := nilCfg.EffectiveGeoIPDownloadURL(); got != "" {
		t.Fatalf("nil config EffectiveGeoIPDownloadURL() = %q, want empty", got)
	}

	cfg := &Config{}
	if got := cfg.EffectiveGeoIPDownloadURL(); got != "" {
		t.Fatalf("empty config EffectiveGeoIPDownloadURL() = %q, want empty", got)
	}

	cfg.Customer = &CustomerConfig{}
	if got := cfg.EffectiveGeoIPDownloadURL(); got != "" {
		t.Fatalf("nil geoip EffectiveGeoIPDownloadURL() = %q, want empty", got)
	}

	cfg.Customer.GeoIP = &CustomerGeoIPSettings{DownloadURL: "  https://mirrors.example.com/x.mmdb  "}
	if got := cfg.EffectiveGeoIPDownloadURL(); got != "https://mirrors.example.com/x.mmdb" {
		t.Fatalf("EffectiveGeoIPDownloadURL() = %q, want trimmed url", got)
	}
}

func TestSetGlobalConfigAppliesGeoIPDownloadURLOverride(t *testing.T) {
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "") // 中和 env 层，锁定 config→geoip 接线
	t.Cleanup(func() { geoip.SetDownloadURLOverride("") })

	cfg := &Config{Customer: &CustomerConfig{
		GeoIP: &CustomerGeoIPSettings{DownloadURL: "https://mirrors.example.com/GeoLite2-Country.mmdb"},
	}}
	SetGlobalConfig(cfg)
	if got := geoip.EffectiveDownloadURL(); got != "https://mirrors.example.com/GeoLite2-Country.mmdb" {
		t.Fatalf("geoip.EffectiveDownloadURL() = %q, want config url", got)
	}

	// env 存在时 config 被忽略（env 层最高）
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "https://env.example.com/x.mmdb")
	SetGlobalConfig(cfg)
	if got := geoip.EffectiveDownloadURL(); got != "https://env.example.com/x.mmdb" {
		t.Fatalf("geoip.EffectiveDownloadURL() = %q, want env url", got)
	}

	// 置空全局配置 = 清除注入，回落默认
	t.Setenv("ALIANG_GEOIP_DOWNLOAD_URL", "")
	SetGlobalConfig(nil)
	if got := geoip.EffectiveDownloadURL(); got != geoip.DefaultGeoIPDownloadURL {
		t.Fatalf("geoip.EffectiveDownloadURL() = %q, want default", got)
	}
}

func TestEffectiveSSHProxy(t *testing.T) {
	t.Run("URL 形态解析与字段透传", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "ssh", Server: "ssh://root:pw@1.2.3.4:2222",
			PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\n...", PoolSize: 5,
		}}}
		got, err := cfg.EffectiveSSHProxy()
		if err != nil {
			t.Fatalf("EffectiveSSHProxy() error = %v", err)
		}
		if got == nil {
			t.Fatal("EffectiveSSHProxy() = nil, want derived ssh config")
		}
		if got.Server != "1.2.3.4" || got.ServerPort != 2222 || got.Username != "root" || got.Password != "pw" || got.PoolSize != 5 {
			t.Fatalf("EffectiveSSHProxy() = %#v, want 1.2.3.4:2222 root/pw pool 5", got)
		}
		if got.PrivateKey != "-----BEGIN OPENSSH PRIVATE KEY-----\n..." {
			t.Fatalf("EffectiveSSHProxy() private key = %q, want passthrough", got.PrivateKey)
		}
	})

	t.Run("非 URL 形态 host:port 也支持", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "ssh", Server: "1.2.3.4:2222", Username: "u", Password: "p",
		}}}
		got, err := cfg.EffectiveSSHProxy()
		if err != nil {
			t.Fatalf("EffectiveSSHProxy() error = %v", err)
		}
		if got.Server != "1.2.3.4" || got.ServerPort != 2222 || got.Username != "u" || got.Password != "p" {
			t.Fatalf("EffectiveSSHProxy() = %#v, want 1.2.3.4:2222 u/p", got)
		}
	})

	t.Run("URL scheme 非 ssh 报错", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "ssh", Server: "socks5://1.2.3.4:1080", Password: "p",
		}}}
		if _, err := cfg.EffectiveSSHProxy(); err == nil {
			t.Fatal("EffectiveSSHProxy() error = nil, want scheme mismatch error")
		}
	})

	t.Run("pool_size 解析层保持原值(0 即 0,默认 3 由 ssh.New 钳位)", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "ssh", Server: "1.2.3.4:22", Password: "p",
		}}}
		got, err := cfg.EffectiveSSHProxy()
		if err != nil {
			t.Fatalf("EffectiveSSHProxy() error = %v", err)
		}
		if got.PoolSize != 0 {
			t.Fatalf("EffectiveSSHProxy() PoolSize = %d, want 0 (clamp lives in ssh.New)", got.PoolSize)
		}
	})

	t.Run("跳板逐项解析并透传认证材料", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "ssh", Server: "1.2.3.4:22", Password: "p",
			JumpServers: []SSHJumpServerRaw{
				{Server: "ssh://hop1:hp1@10.0.0.1:2201", PrivateKey: "key1"},
				{Server: "10.0.0.2:2202", Username: "hop2", Password: "hp2"},
			},
		}}}
		got, err := cfg.EffectiveSSHProxy()
		if err != nil {
			t.Fatalf("EffectiveSSHProxy() error = %v", err)
		}
		if len(got.JumpServers) != 2 {
			t.Fatalf("EffectiveSSHProxy() jumps = %d, want 2", len(got.JumpServers))
		}
		j0, j1 := got.JumpServers[0], got.JumpServers[1]
		if j0.Server != "10.0.0.1" || j0.ServerPort != 2201 || j0.Username != "hop1" || j0.Password != "hp1" || j0.PrivateKey != "key1" {
			t.Fatalf("jump[0] = %#v", j0)
		}
		if j1.Server != "10.0.0.2" || j1.ServerPort != 2202 || j1.Username != "hop2" || j1.Password != "hp2" {
			t.Fatalf("jump[1] = %#v", j1)
		}
	})

	t.Run("跳板 scheme 不匹配报错并带索引", func(t *testing.T) {
		cfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "ssh", Server: "1.2.3.4:22", Password: "p",
			JumpServers: []SSHJumpServerRaw{{Server: "http://10.0.0.1:2201", Username: "hop", Password: "hp"}},
		}}}
		_, err := cfg.EffectiveSSHProxy()
		if err == nil {
			t.Fatal("EffectiveSSHProxy() error = nil, want jump scheme mismatch error")
		}
		if !strings.Contains(err.Error(), "jump_servers[0]") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("非 ssh 类型与未启用返回 nil", func(t *testing.T) {
		httpCfg := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Type: "http", Server: "127.0.0.1:8080",
		}}}
		if got, err := httpCfg.EffectiveSSHProxy(); err != nil || got != nil {
			t.Fatalf("EffectiveSSHProxy() = %v, %v; want nil, nil for http type", got, err)
		}
		off := false
		disabled := &Config{Customer: &CustomerConfig{Proxy: &CustomerProxyConfig{
			Enable: &off, Type: "ssh", Server: "1.2.3.4:22", Password: "p",
		}}}
		if got, err := disabled.EffectiveSSHProxy(); err != nil || got != nil {
			t.Fatalf("EffectiveSSHProxy() = %v, %v; want nil, nil when disabled", got, err)
		}
	})
}

func TestValidateCustomerEditableSurfaceSSH(t *testing.T) {
	payload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "ssh",
				"server": "ssh://root:pw@1.2.3.4:2222"
			}
		}
	}`)

	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	// type=ssh 必须过 editable surface:不放行则配置保存不进(spec §2 触点)。
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for ssh", err)
	}

	ftpPayload := []byte(`{
		"core": {"api_server": "https://api.aliang.one"},
		"customer": {
			"proxy": {
				"type": "ftp",
				"server": "1.2.3.4:21"
			}
		}
	}`)
	var ftpCfg Config
	if err := json.Unmarshal(ftpPayload, &ftpCfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := ftpCfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want type whitelist rejection for ftp")
	}
}

func TestConfigValidateSSHProxy(t *testing.T) {
	t.Run("缺认证报错而非静默跳过", func(t *testing.T) {
		payload := []byte(`{
			"core": {"api_server": "https://api.aliang.one"},
			"customer": {
				"proxy": {
					"type": "ssh",
					"server": "1.2.3.4:2222"
				}
			}
		}`)
		var cfg Config
		if err := json.Unmarshal(payload, &cfg); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("Validate() error = nil, want missing auth error")
		}
		if !strings.Contains(err.Error(), "sshProxy") || !strings.Contains(err.Error(), "password or private_key") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("合法配置通过并经 EffectiveCustomerProxy 路由", func(t *testing.T) {
		payload := []byte(`{
			"core": {"api_server": "https://api.aliang.one"},
			"customer": {
				"proxy": {
					"type": "ssh",
					"server": "1.2.3.4:2222",
					"username": "root",
					"password": "pw",
					"pool_size": 5,
					"jump_servers": [
						{"server": "10.0.0.1:2201", "username": "hop", "password": "hp"}
					]
				}
			}
		}`)
		var cfg Config
		if err := json.Unmarshal(payload, &cfg); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		got, err := cfg.EffectiveCustomerProxy()
		if err != nil {
			t.Fatalf("EffectiveCustomerProxy() error = %v", err)
		}
		sshCfg, ok := got.(*SSHProxyConfig)
		if !ok {
			t.Fatalf("EffectiveCustomerProxy() = %T, want *SSHProxyConfig", got)
		}
		if sshCfg.Server != "1.2.3.4" || sshCfg.ServerPort != 2222 || sshCfg.PoolSize != 5 {
			t.Fatalf("EffectiveCustomerProxy() = %#v", sshCfg)
		}
		if len(sshCfg.JumpServers) != 1 || sshCfg.JumpServers[0].Server != "10.0.0.1" || sshCfg.JumpServers[0].ServerPort != 2201 {
			t.Fatalf("EffectiveCustomerProxy() jumps = %#v", sshCfg.JumpServers)
		}
	})
}

func TestSSHProxyConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *SSHProxyConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid with password",
			config:  &SSHProxyConfig{Server: "1.2.3.4", ServerPort: 22, Username: "root", Password: "pw"},
			wantErr: false,
		},
		{
			name:    "valid with private key only",
			config:  &SSHProxyConfig{Server: "1.2.3.4", ServerPort: 22, PrivateKey: "key"},
			wantErr: false,
		},
		{
			name:    "missing server_host",
			config:  &SSHProxyConfig{ServerPort: 22, Password: "pw"},
			wantErr: true,
			errMsg:  "server_host is required",
		},
		{
			name:    "missing server_port",
			config:  &SSHProxyConfig{Server: "1.2.3.4", Password: "pw"},
			wantErr: true,
			errMsg:  "server_port is required",
		},
		{
			name:    "missing auth",
			config:  &SSHProxyConfig{Server: "1.2.3.4", ServerPort: 22, Username: "root"},
			wantErr: true,
			errMsg:  "ssh proxy requires password or private_key",
		},
		{
			name: "valid with jump",
			config: &SSHProxyConfig{
				Server: "1.2.3.4", ServerPort: 22, Password: "pw",
				JumpServers: []SSHJumpServer{{Server: "10.0.0.1", ServerPort: 2201, Username: "hop", Password: "hp"}},
			},
			wantErr: false,
		},
		{
			name: "jump missing server and port",
			config: &SSHProxyConfig{
				Server: "1.2.3.4", ServerPort: 22, Password: "pw",
				JumpServers: []SSHJumpServer{{Username: "hop", Password: "hp"}},
			},
			wantErr: true,
			errMsg:  "jump_servers[0] requires server_host and server_port",
		},
		{
			name: "jump missing username",
			config: &SSHProxyConfig{
				Server: "1.2.3.4", ServerPort: 22, Password: "pw",
				JumpServers: []SSHJumpServer{{Server: "10.0.0.1", ServerPort: 2201, Password: "hp"}},
			},
			wantErr: true,
			errMsg:  "jump_servers[0] requires username",
		},
		{
			name: "jump missing auth",
			config: &SSHProxyConfig{
				Server: "1.2.3.4", ServerPort: 22, Password: "pw",
				JumpServers: []SSHJumpServer{{Server: "10.0.0.1", ServerPort: 2201, Username: "hop"}},
			},
			wantErr: true,
			errMsg:  "jump_servers[0] requires password or private_key",
		},
		{
			name: "jump index reported",
			config: &SSHProxyConfig{
				Server: "1.2.3.4", ServerPort: 22, Password: "pw",
				JumpServers: []SSHJumpServer{
					{Server: "10.0.0.1", ServerPort: 2201, Username: "hop", Password: "hp"},
					{Server: "10.0.0.2", ServerPort: 2202, Username: "hop2"},
				},
			},
			wantErr: true,
			errMsg:  "jump_servers[1] requires password or private_key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && err.Error() != tt.errMsg {
				t.Errorf("Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestValidateCustomerProxySchemeSSH(t *testing.T) {
	if err := validateCustomerProxyScheme("ssh", "ssh"); err != nil {
		t.Fatalf("validateCustomerProxyScheme(ssh, ssh) = %v, want nil", err)
	}
	if err := validateCustomerProxyScheme("ssh", "SSH"); err != nil {
		t.Fatalf("validateCustomerProxyScheme(ssh, SSH) = %v, want nil (case-insensitive)", err)
	}
	for _, scheme := range []string{"socks5", "socks", "http"} {
		if err := validateCustomerProxyScheme("ssh", scheme); err == nil {
			t.Fatalf("validateCustomerProxyScheme(ssh, %s) = nil, want mismatch error", scheme)
		}
	}
}
