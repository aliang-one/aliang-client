package services

import (
	"bytes"
	"strings"
	"testing"
)

// isLikelyAgentBinary 的中文回归（2026-10-22 手机「base64 content returned
// by Agent」事故）：旧实现按字节统计 >126 的「不可打印」比例，而中文 UTF-8
// 的每个字节都 >126，整篇中文被误判成二进制、走 base64 通道，手机端只能
// 显示占位文案。
func TestIsLikelyAgentBinaryChineseTextIsText(t *testing.T) {
	chinese := strings.Repeat("这是一段用于验证的中文内容，包含标点与换行。\n", 200)
	if isLikelyAgentBinary([]byte(chinese)) {
		t.Fatalf("pure Chinese text must be detected as text (utf8), got binary")
	}

	mixed := "# 项目说明\n\n这是一个中文为主的 Markdown 文件，mixed with english words.\n"
	if isLikelyAgentBinary([]byte(mixed)) {
		t.Fatalf("mixed Chinese/English text must be detected as text")
	}

	if isLikelyAgentBinary([]byte("plain ascii text\nwith lines\n")) {
		t.Fatalf("ascii text must be detected as text")
	}
}

func TestIsLikelyAgentBinaryStillDetectsBinary(t *testing.T) {
	// NUL 字节是最强二进制信号
	if !isLikelyAgentBinary([]byte("text\x00with nul")) {
		t.Fatalf("NUL-containing content must be binary")
	}
	// 高位非法 UTF-8 序列（0xFF 重复）
	if !isLikelyAgentBinary(bytes.Repeat([]byte{0xFF}, 512)) {
		t.Fatalf("invalid high-byte payload must be binary")
	}
	// UTF-16LE BOM + 内容（合法场景：判二进制走 base64）
	utf16le := []byte{0xFF, 0xFE, 0x41, 0x00, 0x42, 0x00, 0x43, 0x00}
	if !isLikelyAgentBinary(utf16le) {
		t.Fatalf("UTF-16 content must be binary")
	}
	// PNG magic
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}, bytes.Repeat([]byte{0xA1}, 64)...)
	if !isLikelyAgentBinary(png) {
		t.Fatalf("png-like payload must be binary")
	}
}

// 文件头按 maxBytes 截断时，末尾可能恰好切开一个多字节中文字符——截断的
// 半个字符不能让整份文件被判成二进制。
func TestIsLikelyAgentBinaryToleratesTruncatedTail(t *testing.T) {
	text := []byte(strings.Repeat("中文内容", 1024)) // 16KB 中文
	for cut := 1; cut <= 3; cut++ {
		truncated := text[:len(text)-cut]
		if isLikelyAgentBinary(truncated) {
			t.Fatalf("chinese text truncated by %d tail byte(s) must still be text", cut)
		}
	}
	// 截断后再叠加中段破坏：真非法序列必须如实判二进制
	broken := append([]byte{}, text[:1024]...)
	broken[500] = 0xFF
	broken[501] = 0xFF
	if !isLikelyAgentBinary(broken) {
		t.Fatalf("mid-file invalid sequence must be binary")
	}
}

// 空内容与既有行为保持一致。
func TestIsLikelyAgentBinaryEmpty(t *testing.T) {
	if isLikelyAgentBinary(nil) {
		t.Fatalf("empty content must be text")
	}
}
