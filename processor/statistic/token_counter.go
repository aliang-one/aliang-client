package statistic

import (
	"encoding/json"
	"strings"
)

type TokenCounter struct{}

func NewTokenCounter() *TokenCounter {
	return &TokenCounter{}
}

func (tc *TokenCounter) ParseTokenUsage(host string, body []byte) (usage *TokenUsage, model string) {
	if len(body) == 0 {
		return nil, ""
	}

	host = strings.ToLower(host)

	if strings.Contains(host, "openai.com") || strings.Contains(host, "chatgpt.com") {
		return tc.parseOpenAI(body)
	}

	if strings.Contains(host, "claude.ai") || strings.Contains(host, "anthropic.com") {
		return tc.parseClaude(body)
	}

	if strings.Contains(host, "cursor.com") || strings.Contains(host, "copilot.microsoft.com") {
		return tc.parseOpenAI(body)
	}

	return tc.parseGeneric(body)
}

func (tc *TokenCounter) parseOpenAI(body []byte) (usage *TokenUsage, model string) {
	var resp OpenAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, ""
	}

	if resp.Usage.TotalTokens == 0 {
		return nil, ""
	}

	return resp.ToTokenUsage(), resp.Model
}

func (tc *TokenCounter) parseClaude(body []byte) (usage *TokenUsage, model string) {
	var resp ClaudeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, ""
	}

	if resp.Usage.InputTokens == 0 && resp.Usage.OutputTokens == 0 {
		return nil, ""
	}

	return resp.ToTokenUsage(), resp.Model
}

func (tc *TokenCounter) parseGeneric(body []byte) (usage *TokenUsage, model string) {
	var resp struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, ""
	}

	if resp.Usage.TotalTokens > 0 {
		return &TokenUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}, resp.Model
	}

	if resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0 {
		return &TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}, resp.Model
	}

	return nil, ""
}

func (tc *TokenCounter) ExtractModelFromRequest(host string, body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var req struct {
		Model string `json:"model"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	return req.Model
}
