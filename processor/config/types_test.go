package config

import (
	"encoding/json"
	"strings"
	"testing"
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
