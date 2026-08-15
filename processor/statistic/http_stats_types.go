package statistic

import "time"

var PresetDomains = []string{
	"openai.com",
	"chatgpt.com",
	"claude.ai",
	"api.cursor.com",
	"copilot.microsoft.com",
}

type HTTPRequestRecord struct {
	ID                 string    `json:"id"`
	Timestamp          time.Time `json:"timestamp"`
	RequestCount       int       `json:"requestCount"`
	ResponseCount      int       `json:"responseCount"`
	Host               string    `json:"host"`
	Path               string    `json:"path"`
	Method             string    `json:"method"`
	StatusCode         int       `json:"statusCode"`
	UploadBytes        int64     `json:"uploadBytes"`
	DownloadBytes      int64     `json:"downloadBytes"`
	InputTokens        int       `json:"inputTokens"`
	OutputTokens       int       `json:"outputTokens"`
	TotalTokens        int       `json:"totalTokens"`
	Model              string    `json:"model"`
	FirstResponseAt    time.Time `json:"firstResponseAt"`
	CompleteResponseAt time.Time `json:"completeResponseAt"`
	Route              string    `json:"route"`
}

func (r *HTTPRequestRecord) Duration() time.Duration {
	if r.CompleteResponseAt.IsZero() {
		return 0
	}
	return r.CompleteResponseAt.Sub(r.Timestamp)
}

func (r *HTTPRequestRecord) FirstResponseLatency() time.Duration {
	if r.FirstResponseAt.IsZero() {
		return 0
	}
	return r.FirstResponseAt.Sub(r.Timestamp)
}

type DomainStats struct {
	Domain        string  `json:"domain"`
	RequestCount  int64   `json:"requestCount"`
	ResponseCount int64   `json:"responseCount"`
	UploadBytes   int64   `json:"uploadBytes"`
	DownloadBytes int64   `json:"downloadBytes"`
	InputTokens   int64   `json:"inputTokens"`
	OutputTokens  int64   `json:"outputTokens"`
	TrafficShare  float64 `json:"trafficShare"`
	RequestShare  float64 `json:"requestShare"`
	SuccessCount  int64   `json:"successCount"`
	ErrorCount    int64   `json:"errorCount"`
	AvgLatency    float64 `json:"avgLatency"`
}

type TrafficDataPoint struct {
	Timestamp     int64 `json:"timestamp"`
	UploadBytes   int64 `json:"uploadBytes"`
	DownloadBytes int64 `json:"downloadBytes"`
	InputTokens   int64 `json:"inputTokens"`
	OutputTokens  int64 `json:"outputTokens"`
	RequestCount  int64 `json:"requestCount"`
	ResponseCount int64 `json:"responseCount"`
}

type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

type OpenAIResponse struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type ClaudeResponse struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (r *OpenAIResponse) ToTokenUsage() *TokenUsage {
	return &TokenUsage{
		InputTokens:  r.Usage.PromptTokens,
		OutputTokens: r.Usage.CompletionTokens,
		TotalTokens:  r.Usage.TotalTokens,
	}
}

func (r *ClaudeResponse) ToTokenUsage() *TokenUsage {
	return &TokenUsage{
		InputTokens:  r.Usage.InputTokens,
		OutputTokens: r.Usage.OutputTokens,
		TotalTokens:  r.Usage.InputTokens + r.Usage.OutputTokens,
	}
}
