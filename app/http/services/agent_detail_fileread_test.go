package services

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 2026-10-28 审计 #5:按 maxBytes 字节截断会把多字节字符切成半个,回包尾部
// 变乱码。trimTruncatedUTF8Tail 必须剥掉不完整的尾部序列;完整 UTF-8 原样。
func TestTrimTruncatedUTF8Tail(t *testing.T) {
	prefix := strings.Repeat("a", 8)
	hello := "你好" // 6 bytes
	raw := append([]byte(prefix), []byte(hello)...)

	// 完整 → 原样
	if got := string(trimTruncatedUTF8Tail(raw)); got != prefix+hello {
		t.Errorf("完整 UTF-8 被改动: %q", got)
	}
	// 切掉「好」的最后一个字节 → 剥掉剩余半个「好」
	cut := raw[:len(raw)-1]
	got := string(trimTruncatedUTF8Tail(cut))
	if !utf8.ValidString(got) {
		t.Errorf("截断修复后仍非法 UTF-8: %q", got)
	}
	if got != prefix+"你" {
		t.Errorf("截断修复结果 = %q, want %q", got, prefix+"你")
	}
	// 中段坏序列不属于尾部截断,原样返回(交由展示层容错)
	broken := []byte{'a', 0xff, 'b'}
	if got := string(trimTruncatedUTF8Tail(broken)); got != string(broken) {
		t.Errorf("中段坏序列被误改: %q", got)
	}
}

// 截断 3 字节(整字「你」)也必须收敛干净。
func TestTrimTruncatedUTF8TailWholeRune(t *testing.T) {
	prefix := strings.Repeat("a", 5)
	raw := append([]byte(prefix), []byte("你好")...)
	cut := raw[:len(raw)-3] // 「你」完整保留,「好」整字被切
	got := string(trimTruncatedUTF8Tail(cut))
	if got != prefix+"你" {
		t.Errorf("整字截断结果 = %q, want %q", got, prefix+"你")
	}
}
