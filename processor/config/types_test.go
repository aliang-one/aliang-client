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
