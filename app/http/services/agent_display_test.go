package services

import (
	"testing"

	"aliang.one/nursorgate/app/http/models"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeVibeTranscriptForDisplay(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}

	messages := []models.AgentVibeMessage{
		{ID: "u1", Role: "user", Content: "帮我写一个排序函数"},
		{ID: "a1", Role: "assistant", Content: long},
		{ID: "s1", Role: "system", Content: "tool result noise"},
		{ID: "a2", Role: "assistant", Content: "短回答"},
	}

	got := summarizeVibeTranscriptForDisplay(messages, 120)

	// system 被过滤
	assert.Len(t, got, 3)
	// user 全文保留
	assert.Equal(t, "帮我写一个排序函数", got[0].Content)
	assert.Equal(t, "user", got[0].Role)
	// assistant 长内容被截断到 <= 120 runes
	assert.LessOrEqual(t, len([]rune(got[1].Content)), 120)
	assert.NotEqual(t, long, got[1].Content)
	// assistant 短内容原样
	assert.Equal(t, "短回答", got[2].Content)
}

func TestSummarizeVibeTranscriptForDisplay_EmptyAndAllSystem(t *testing.T) {
	assert.Empty(t, summarizeVibeTranscriptForDisplay(nil, 120))
	assert.Empty(t, summarizeVibeTranscriptForDisplay(
		[]models.AgentVibeMessage{{Role: "system", Content: "x"}}, 120))
}

func TestSummarizeVibeTranscriptForDisplay_DoesNotMutateInput(t *testing.T) {
	original := []models.AgentVibeMessage{
		{ID: "a1", Role: "assistant", Content: "abcdefghijklmnop"},
	}
	_ = summarizeVibeTranscriptForDisplay(original, 5)
	// 输入切片的元素不应被修改
	assert.Equal(t, "abcdefghijklmnop", original[0].Content)
}
