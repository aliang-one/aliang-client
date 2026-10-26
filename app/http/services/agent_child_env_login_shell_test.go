package services

import (
	"strings"
	"testing"
)

// 纯函数：解析探针输出。.zshrc 可能往 stdout 噪声输出，靠标记切片保证鲁棒。
func TestParseLoginShellPathOutput(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"干净输出", "__ALIANG_PATH__/usr/bin:/bin", "/usr/bin:/bin", true},
		{"前噪声", "loading profile...\n__ALIANG_PATH__/a:/b", "/a:/b", true},
		{"后换行", "__ALIANG_PATH__/a:/b\n", "/a:/b", true},
		{"中间空格", "__ALIANG_PATH__/a b:/c", "/a b:/c", true},
		{"无标记", "/a:/b", "", false},
		{"标记后为空", "__ALIANG_PATH__", "", false},
		{"标记后无冒号", "__ALIANG_PATH__/single", "", false},
	}
	for _, tc := range cases {
		got, ok := parseLoginShellPathOutput(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// 纯函数：合并语义——替换 PATH、保留其余、不产生重复键、PATH 恒唯一。
func TestMergeLoginShellPath(t *testing.T) {
	base := []string{"HOME=/x", "PATH=/usr/bin:/bin", "TERM=dumb"}
	got := mergeLoginShellPath(base, "/opt/homebrew/bin:/usr/bin:/bin")
	want := []string{"HOME=/x", "TERM=dumb", "PATH=/opt/homebrew/bin:/usr/bin:/bin"}
	if len(got) != len(want) {
		t.Fatalf("长度不等: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项: got %q want %q", i, got[i], want[i])
		}
	}
	// 探针失败（空串）必须原样返回，fail-open。
	if same := mergeLoginShellPath(base, ""); len(same) != 3 || same[1] != "PATH=/usr/bin:/bin" {
		t.Fatalf("探针失败应原样返回: %v", same)
	}
}

// 缓存包装器端到端：PATH 恒唯一（探针失败时原样保留）。
func TestAugmentEnvWithLoginShellPath(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "HOME=/x"}
	augmented := augmentEnvWithLoginShellPath(base)
	count := 0
	for _, kv := range augmented {
		if strings.HasPrefix(kv, "PATH=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("PATH 应恰有一条, got %d: %v", count, augmented)
	}
}
