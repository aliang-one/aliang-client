package ssh

import (
	"errors"
	"testing"
)

// user_fallback_test.go — 私钥登录用户名兜底的单元测试:sanitizeUsername 合规化
// 规则 + resolveUsername 三分支(显式优先/密码路径透传/私钥路径兜底)。

func TestSanitizeUsername(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"合法主机名原样", "MacBook-Pro.local", "MacBook-Pro.local"},
		{"常规小写主机名", "macbook-air", "macbook-air"},
		{"空格转连字符", "mac book", "mac-book"},
		{"非ASCII全转连字符后修剪", "我的电脑", "aliang-node"},
		{"前后空白与非法字符", "  ..mac..!!  ", "mac"},
		{"纯非法字符回落固定名", "!!!", "aliang-node"},
		{"空串回落固定名", "", "aliang-node"},
		{"点号下划线保留", "node_01.lan", "node_01.lan"},
		{"截断边界落在点上同样修剪", "0123456789abcdef0123456789abcde.y", "0123456789abcdef0123456789abcde"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeUsername(tc.in); got != tc.want {
				t.Fatalf("sanitizeUsername(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeUsernameTruncatesLongHostname(t *testing.T) {
	long := "verylonghostnameprefix-" + "abcdefghijklmnopqrstuvwxyz0123456789"
	got := sanitizeUsername(long)
	if got == "" || len([]rune(got)) > usernameMaxLength {
		t.Fatalf("sanitizeUsername(long) = %q, want ≤%d runes", got, usernameMaxLength)
	}
}

func TestResolveUsername(t *testing.T) {
	restore := hostnameForFallback
	defer func() { hostnameForFallback = restore }()
	hostnameForFallback = func() (string, error) { return "My Mac Book", nil }

	t.Run("显式用户名优先,不兜底", func(t *testing.T) {
		if got := resolveUsername("deploy", "KEY"); got != "deploy" {
			t.Fatalf("resolveUsername(deploy, KEY) = %q, want deploy", got)
		}
	})
	t.Run("密码路径透传,空仍空(Validate 拦)", func(t *testing.T) {
		if got := resolveUsername("", ""); got != "" {
			t.Fatalf("resolveUsername(empty, empty) = %q, want empty", got)
		}
	})
	t.Run("私钥路径空用户名兜底本机名合规化", func(t *testing.T) {
		if got, want := resolveUsername("", "KEY"), "My-Mac-Book"; got != want {
			t.Fatalf("resolveUsername(empty, KEY) = %q, want %q", got, want)
		}
	})
	t.Run("本机名取不到回落固定名", func(t *testing.T) {
		hostnameForFallback = func() (string, error) { return "", errors.New("hostname unavailable") }
		if got := resolveUsername("", "KEY"); got != fallbackUsernameDefault {
			t.Fatalf("resolveUsername(empty, KEY) with hostname error = %q, want %q", got, fallbackUsernameDefault)
		}
	})
}

func TestNodeFromAddrPortAppliesFallback(t *testing.T) {
	restore := hostnameForFallback
	defer func() { hostnameForFallback = restore }()
	hostnameForFallback = func() (string, error) { return "mesh-node-1", nil }

	// 私钥 + 空用户名 → 兜底;显式用户名原样;密码路径空用户名不兜底
	n := NodeFromAddrPort("1.2.3.4", 22, "", "p", "KEY", "")
	if n.User != "mesh-node-1" {
		t.Fatalf("NodeFromAddrPort key-only User = %q, want mesh-node-1", n.User)
	}
	n = NodeFromAddrPort("1.2.3.4", 22, "root", "", "KEY", "")
	if n.User != "root" {
		t.Fatalf("NodeFromAddrPort explicit User = %q, want root", n.User)
	}
	n = NodeFromAddrPort("1.2.3.4", 22, "", "p", "", "")
	if n.User != "" {
		t.Fatalf("NodeFromAddrPort password-path User = %q, want empty", n.User)
	}
}
