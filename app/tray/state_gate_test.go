package tray

import "testing"

// 变更检测闸的用例锚定 2026-11-20 0.196 事故:syncStateLoop 每 2s 无差别
// 重绘托盘可见面,systray v1.2.2 MenuItem.SetIcon 每次新建 HBITMAP 且旧句
// 柄不销毁,~83 分钟耗尽 Windows 10k GDI 配额(WER GDIObjectLeak ×3 实锤)。
// 签名不变即整轮跳过,泄漏只发生在可见面真翻转时。

func TestAIProvidersDigestStableForIdenticalProviders(t *testing.T) {
	providers := []AIProviderStatus{
		{Key: "claude", Label: "Claude", Enabled: true, Active: true},
		{Key: "codex", Label: "Codex", Enabled: true, Detected: true},
		{Key: "openai", Label: "OpenAI", Enabled: true},
	}

	first := aiProvidersDigest(providers)
	second := aiProvidersDigest(providers)
	if first == "" {
		t.Fatal("digest of enabled providers should not be empty")
	}
	if first != second {
		t.Fatalf("digest unstable for identical providers: %q vs %q", first, second)
	}
}

func TestAIProvidersDigestChangesOnActivityFlip(t *testing.T) {
	idle := []AIProviderStatus{{Key: "claude", Label: "Claude", Enabled: true}}
	active := []AIProviderStatus{{Key: "claude", Label: "Claude", Enabled: true, Active: true}}

	if aiProvidersDigest(idle) == aiProvidersDigest(active) {
		t.Fatal("digest must change when provider flips active (menu title ✅/⏳ and icon both change)")
	}
}

func TestAIProvidersDigestSkipsDisabledProviders(t *testing.T) {
	disabled := []AIProviderStatus{{Key: "claude", Label: "Claude", Enabled: false}}
	missing := []AIProviderStatus(nil)

	if aiProvidersDigest(disabled) != aiProvidersDigest(missing) {
		t.Fatal("disabled provider renders hidden just like an absent one; digests must match")
	}
	if aiProvidersDigest(missing) != "" {
		t.Fatalf("digest of no visible providers = %q, want empty", aiProvidersDigest(missing))
	}
}

func TestBuildTrayViewKeyDiscriminatesVisibleState(t *testing.T) {
	providers := []AIProviderStatus{{Key: "claude", Label: "Claude", Enabled: true, Active: true}}

	base := buildTrayViewKey("tun", true, "", providers)

	cases := []struct {
		name string
		key  string
	}{
		{"running flip", buildTrayViewKey("tun", false, "", providers)},
		{"mode change", buildTrayViewKey("http", true, "", providers)},
		{"blocked code appears", buildTrayViewKey("tun", false, "not_logged_in", providers)},
		{"provider activity flip", buildTrayViewKey("tun", true, "", []AIProviderStatus{{Key: "claude", Label: "Claude", Enabled: true}})},
	}

	for _, tc := range cases {
		if tc.key == base {
			t.Fatalf("%s: key %q collides with base %q", tc.name, tc.key, base)
		}
	}

	if again := buildTrayViewKey("tun", true, "", providers); again != base {
		t.Fatalf("key unstable for identical inputs: %q vs %q", again, base)
	}
}

func TestBuildTrayUnavailableViewKey(t *testing.T) {
	if buildTrayUnavailableViewKey("ipc down") == buildTrayUnavailableViewKey("other reason") {
		t.Fatal("different unavailable reasons must produce different keys")
	}
	if buildTrayUnavailableViewKey("ipc down") != buildTrayUnavailableViewKey("ipc down") {
		t.Fatal("same unavailable reason must produce a stable key")
	}
	if buildTrayUnavailableViewKey("ipc down") == buildTrayViewKey("tun", true, "", nil) {
		t.Fatal("unavailable key must never collide with a running/stopped view key")
	}
}
