package tls

import (
	"net/http"
	"net/url"
	"testing"

	"aliang.one/nursorgate/processor/config"
	"golang.org/x/net/http2/hpack"
)

func TestRewriteAliangHTTPRequestHost_OpenAI(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"openai": {
					Enble:   boolPtr(true),
					Include: []string{"openai.com", "chatgpt.com"},
				},
			},
		},
	})

	req := &http.Request{
		Host: "chatgpt.com",
		URL:  mustParseURL(t, "http://chatgpt.com/v1/chat/completions"),
	}

	if changed := rewriteAliangHTTPRequestHost(req); !changed {
		t.Fatal("rewriteAliangHTTPRequestHost() = false, want true")
	}
	if req.Host != "api.openai.com" {
		t.Fatalf("req.Host = %q, want api.openai.com", req.Host)
	}
	if req.URL.Host != "api.openai.com" {
		t.Fatalf("req.URL.Host = %q, want api.openai.com", req.URL.Host)
	}
}

func TestRewriteAliangHTTPRequestHost_AnthropicAlias(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"Anthropic": {
					Enble:   boolPtr(true),
					Include: []string{"anthropic.com", "claude.ai"},
				},
			},
		},
	})

	req := &http.Request{
		Host: "claude.ai",
		URL:  mustParseURL(t, "http://claude.ai/v1/messages"),
	}

	if changed := rewriteAliangHTTPRequestHost(req); !changed {
		t.Fatal("rewriteAliangHTTPRequestHost() = false, want true")
	}
	if req.Host != "api.anthropic.com" {
		t.Fatalf("req.Host = %q, want api.anthropic.com", req.Host)
	}
}

func TestRewriteAliangHTTPRequestHost_NoMatchKeepsOriginal(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"openai": {
					Enble:   boolPtr(true),
					Include: []string{"openai.com"},
				},
			},
		},
	})

	req := &http.Request{
		Host: "example.com",
		URL:  mustParseURL(t, "http://example.com/demo"),
	}

	if changed := rewriteAliangHTTPRequestHost(req); changed {
		t.Fatal("rewriteAliangHTTPRequestHost() = true, want false")
	}
	if req.Host != "example.com" {
		t.Fatalf("req.Host = %q, want example.com", req.Host)
	}
}

func TestAliangHTTPHostMatches_SupportsExactSuffixAndWildcard(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{name: "exact", pattern: "anthropic.com", host: "anthropic.com", want: true},
		{name: "suffix", pattern: "openai.com", host: "api.openai.com", want: true},
		{name: "wildcard", pattern: "*.githubcopilot.com", host: "api.githubcopilot.com", want: true},
		{name: "wildcard no root", pattern: "*.githubcopilot.com", host: "githubcopilot.com", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aliangHTTPHostMatches(tt.pattern, tt.host); got != tt.want {
				t.Fatalf("aliangHTTPHostMatches(%q, %q) = %v, want %v", tt.pattern, tt.host, got, tt.want)
			}
		})
	}
}

func TestRewriteAliangHTTP2HeaderFields_RewritesAuthorityAndHost(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"openai": {
					Enble:   boolPtr(true),
					Include: []string{"openai.com", "chatgpt.com"},
				},
			},
		},
	})

	fields := []hpack.HeaderField{
		{Name: ":method", Value: "POST"},
		{Name: ":authority", Value: "chatgpt.com"},
		{Name: "host", Value: "chatgpt.com"},
		{Name: ":path", Value: "/v1/chat/completions"},
	}

	rewritten, changed := rewriteAliangHTTP2HeaderFields(fields)
	if !changed {
		t.Fatal("rewriteAliangHTTP2HeaderFields() = false, want true")
	}

	if got, _ := getHTTP2HeaderFieldValue(rewritten, ":authority"); got != "api.openai.com" {
		t.Fatalf(":authority = %q, want api.openai.com", got)
	}
	if got, _ := getHTTP2HeaderFieldValue(rewritten, "host"); got != "api.openai.com" {
		t.Fatalf("host = %q, want api.openai.com", got)
	}
}

func TestRewriteAliangHTTP2HeaderFields_NoMatchKeepsOriginal(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"openai": {
					Enble:   boolPtr(true),
					Include: []string{"openai.com"},
				},
			},
		},
	})

	fields := []hpack.HeaderField{
		{Name: ":method", Value: "GET"},
		{Name: ":authority", Value: "example.com"},
		{Name: ":path", Value: "/"},
	}

	rewritten, changed := rewriteAliangHTTP2HeaderFields(fields)
	if changed {
		t.Fatal("rewriteAliangHTTP2HeaderFields() = true, want false")
	}
	if got, _ := getHTTP2HeaderFieldValue(rewritten, ":authority"); got != "example.com" {
		t.Fatalf(":authority = %q, want example.com", got)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return parsed
}
