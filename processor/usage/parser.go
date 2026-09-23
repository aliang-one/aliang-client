// Package usage 感知用户机器上 Claude Code 的会话用量（token/请求次数），
// 聚合为小时粒度桶上报后台，作为 coding 力度 / 宠物活跃度材料。
// 隐私边界：解析器只反序列化白名单元数据字段，对话内容从类型层面不可达。
package usage

import (
	"encoding/json"
	"fmt"
	"time"
)

// UsageSample 是一条携带用量的 assistant 消息（≈ 一次上游 API 响应）。
type UsageSample struct {
	SessionID   string
	MessageUUID string
	Timestamp   time.Time
	Model       string

	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// jsonlLine 是 Claude Code 会话 JSONL 行的白名单视图。结构体刻意不声明
// message.content 等内容字段——未知字段被 encoding/json 忽略，内容永远
// 不会进入本包。
type jsonlLine struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseLine 解析一行 JSONL。返回 (nil, nil) 表示该行不携带用量（user 行、
// 无 usage 的 assistant 行、summary 行、全零 usage），调用方直接跳过；
// (nil, err) 表示行损坏，调用方同样跳过但可计数。
func ParseLine(line []byte) (*UsageSample, error) {
	var raw jsonlLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("usage: malformed jsonl line: %w", err)
	}
	if raw.Type != "assistant" || raw.Message == nil || raw.Message.Usage == nil {
		return nil, nil
	}
	u := raw.Message.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return nil, nil
	}
	ts := time.Now().UTC()
	if raw.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, raw.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("usage: bad timestamp %q: %w", raw.Timestamp, err)
		}
		ts = parsed
	}
	return &UsageSample{
		SessionID:   raw.SessionID,
		MessageUUID: raw.UUID,
		Timestamp:   ts,
		Model:       raw.Message.Model,

		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheReadTokens:     u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
	}, nil
}
