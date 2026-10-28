package agentcore

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// streamWriter 把内核事件翻译成 Claude Code 的 stream-json 帧。
//
// 为什么复用这套帧而不是自定义协议：宿主侧 streamStructuredAIDelta
// （app/http/services/agent_ai.go:5840）只认 Claude 的 stream-json。把内核事件
// 写成这套帧，delta 去重、工具活动追踪、session_id 捕获、ai.command 配对、
// 终止收敛全部免费复用，宿主一行都不用改。
//
// 一次 run 的帧序列：
//
//	system/init
//	├─ 回合 1
//	│  stream_event message_start
//	│  stream_event content_block_start / content_block_delta… / content_block_stop
//	│  assistant（finalized —— 工具活动追踪只认这一帧）
//	│  user（tool_result 回填）
//	├─ 回合 2 …
//	result
//
// 两条容易踩的坑：
//
//  1. assistant 帧必须带完整的 tool_use 块。宿主 updateClaudeToolUseActivity
//     （agent_ai.go:6052）只从 assistant/user 帧的 message.content 数组里登记
//     待办工具，纯 stream_event 帧不算数——少了它，空闲看门狗会在等工具的长
//     回合里误杀会话。
//  2. message.model 绝不能是 "<synthetic>"，宿主 extractClaudeStreamTexts
//     （agent_ai.go:6350）会把它当失败吞掉。
//
// 非并发安全由内部互斥锁兜住；emit 永远只由单线程写。
type streamWriter struct {
	emit      func([]byte) error
	sessionID string

	mu        sync.Mutex
	initSent  bool
	model     string
	messageID string
}

// newStreamWriter 构造写入端。emit 为 nil 时全部写入都是空操作（便于单测只
// 验内核状态）。
func newStreamWriter(emit func([]byte) error, sessionID string) *streamWriter {
	return &streamWriter{emit: emit, sessionID: sessionID}
}

// writeFrame 序列化一帧并补 session_id。调用方必须已持有 w.mu。
func (w *streamWriter) writeFrame(frame map[string]interface{}) error {
	if w.emit == nil {
		return nil
	}
	// 每一帧都必须带 session_id：宿主从任意帧捕获它回填 resume 句柄，
	// 漏一帧就可能让某条事件落不进会话。
	if _, ok := frame["session_id"]; !ok {
		frame["session_id"] = w.sessionID
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("agentcore: 序列化事件帧失败: %w", err)
	}
	// 宿主按行读（bufio.Scanner）。json.Marshal 会把字符串里的换行转义，
	// 所以追加一个 '\n' 就是完整的一帧。
	raw = append(raw, '\n')
	return w.emit(raw)
}

// Init 发一次 system/init。重复调用是空操作。
func (w *streamWriter) Init() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.initSent {
		return nil
	}
	w.initSent = true
	return w.writeFrame(map[string]interface{}{
		"type":    "system",
		"subtype": "init",
		// 首期没有斜杠命令：空数组让宿主 onClaudeInit 拿到确定值而不是 nil。
		"slash_commands":      []string{},
		"claude_code_version": "agentcore/1",
	})
}

// CompactBoundary 发一次 system/compact_boundary，告知宿主上下文刚被压缩。
//
// 帧形状对齐 claude CLI 的同名帧（compact_metadata 里 trigger/pre_tokens/
// post_tokens），宿主的 claude 流解析器不用为内核另开一套识别路径。
func (w *streamWriter) CompactBoundary(trigger string, preTokens, postTokens int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(trigger) == "" {
		trigger = "auto"
	}
	return w.writeFrame(map[string]interface{}{
		"type":    "system",
		"subtype": "compact_boundary",
		"compact_metadata": map[string]interface{}{
			"trigger":     trigger,
			"pre_tokens":  preTokens,
			"post_tokens": postTokens,
		},
	})
}

// RetryNotice 发一次 system/api_retry：上游瞬态失败、内核即将重试。
//
// 帧形逐字段对齐真 claude-code 的同名帧（宿主 app/http/services/
// agent_ai_retry_test.go 里锁着逐字样例：{"type":"system","subtype":"api_retry",
// attempt, max_retries, retry_delay_ms, error_status, error}）。宿主 decoder
// 对它本来就有完整处理：markProgress 刷活性 + ai.run.progress(retry_active)
// 转发手机 + 失败时把 error_status/error 类别并进终态 ai.error。内核重试原本
// 全程零帧，纯推理后的首字重试链（最坏 4×120s + 退避 ≈ 8min）会被 stale
// 扫描器 5min 判死——这帧是活性链路的必要环节，不是锦上添花。
//
// 空字段刻意不发：decoder 对缺失字段按零值跳过，与 CLI 帧的稀疏形态一致。
func (w *streamWriter) RetryNotice(n RetryNotice) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	frame := map[string]interface{}{
		"type":        "system",
		"subtype":     "api_retry",
		"attempt":     n.Attempt,
		"max_retries": maxRetries,
	}
	if n.Delay > 0 {
		frame["retry_delay_ms"] = n.Delay.Milliseconds()
	}
	if n.ErrorStatus > 0 {
		frame["error_status"] = n.ErrorStatus
	}
	if n.ErrorKind != "" {
		frame["error"] = n.ErrorKind
	}
	return w.writeFrame(frame)
}

// TurnStart 发 message_start，确立本回合的 provider 消息 id。
func (w *streamWriter) TurnStart(messageID, model string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messageID = messageID
	if m := strings.TrimSpace(model); m != "" && m != "<synthetic>" {
		w.model = m
	}
	return w.writeFrame(map[string]interface{}{
		"type": "stream_event",
		"event": map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":    messageID,
				"type":  "message",
				"role":  "assistant",
				"model": w.model,
			},
		},
	})
}

// ContentBlockStart 发 content_block_start。
func (w *streamWriter) ContentBlockStart(index int, block map[string]interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeFrame(map[string]interface{}{
		"type": "stream_event",
		"event": map[string]interface{}{
			"type":          "content_block_start",
			"index":         index,
			"content_block": block,
		},
	})
}

// TextBlockStart 是文本块的 content_block_start 便捷方法。
func (w *streamWriter) TextBlockStart(index int) error {
	return w.ContentBlockStart(index, map[string]interface{}{
		"type": BlockTypeText,
		"text": "",
	})
}

// ThinkingBlockStart 是思考块的 content_block_start 便捷方法。
//
// 与 TextBlockStart 一样等到第一段 delta 才发（见 streamTurn 的 started 表）；
// 宿主 emitClaudeStructuredEvents 靠 content_block_start 记住块类型，收
// content_block_stop 时才知道要补一发 ai.thinking active:false，所以这个
// start 不能省。
func (w *streamWriter) ThinkingBlockStart(index int) error {
	return w.ContentBlockStart(index, map[string]interface{}{
		"type":     BlockTypeThinking,
		"thinking": "",
	})
}

// ToolUseBlockStart 是工具块的 content_block_start 便捷方法。
func (w *streamWriter) ToolUseBlockStart(index int, id, name string) error {
	return w.ContentBlockStart(index, map[string]interface{}{
		"type":  BlockTypeToolUse,
		"id":    id,
		"name":  name,
		"input": map[string]interface{}{},
	})
}

// TextDelta 发一段文本增量。
func (w *streamWriter) TextDelta(index int, text string) error {
	if text == "" {
		return nil
	}
	return w.ContentBlockDelta(index, map[string]interface{}{
		"type": "text_delta",
		"text": text,
	})
}

// ThinkingDelta 发一段思考增量。字段名 thinking 对齐真 claude-code 的帧：
// 宿主 emitClaudeStructuredEvents 只认 delta.type=thinking_delta、取
// delta.thinking，翻成 ai.thinking 并 markProgress——纯推理期间这是服务层
// 唯一的活性信号（L3 stale 扫描器 5 分钟就判死），内核必须原样透传而不是丢。
func (w *streamWriter) ThinkingDelta(index int, text string) error {
	if text == "" {
		return nil
	}
	return w.ContentBlockDelta(index, map[string]interface{}{
		"type":     "thinking_delta",
		"thinking": text,
	})
}

// ToolInputDelta 发一段工具参数增量。宿主不消费它，但保留是为了与真
// claude-code 的帧序一致，将来手机端要做参数流式预览时不用再改内核。
func (w *streamWriter) ToolInputDelta(index int, partialJSON string) error {
	if partialJSON == "" {
		return nil
	}
	return w.ContentBlockDelta(index, map[string]interface{}{
		"type":         "input_json_delta",
		"partial_json": partialJSON,
	})
}

// ContentBlockDelta 发 content_block_delta。
func (w *streamWriter) ContentBlockDelta(index int, delta map[string]interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeFrame(map[string]interface{}{
		"type": "stream_event",
		"event": map[string]interface{}{
			"type":  "content_block_delta",
			"index": index,
			"delta": delta,
		},
	})
}

// ContentBlockStop 发 content_block_stop。
func (w *streamWriter) ContentBlockStop(index int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeFrame(map[string]interface{}{
		"type": "stream_event",
		"event": map[string]interface{}{
			"type":  "content_block_stop",
			"index": index,
		},
	})
}

// AssistantMessage 发 finalized 的 assistant 帧。
//
// 这一帧是宿主工具活动追踪的唯一入口：content 里必须带完整的 tool_use 块
// （id / name / input 全齐），否则 updateClaudeToolUseActivity 登记不到待办，
// 等工具的空闲回合会被看门狗误判成卡死。
func (w *streamWriter) AssistantMessage(blocks []ContentBlock) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	content := make([]interface{}, 0, len(blocks))
	for _, b := range blocks {
		content = append(content, blockToMap(b))
	}
	return w.writeFrame(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"id":      w.messageID,
			"type":    "message",
			"role":    "assistant",
			"model":   w.model,
			"content": content,
		},
	})
}

// ToolResults 发 tool_result 回填的 user 帧。
//
// 宿主 updateClaudeToolUseActivity 的 "user" 分支靠它把 pending 工具清掉，
// 漏发会让活动指示一直停在「执行中」。
func (w *streamWriter) ToolResults(results []ContentBlock) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(results) == 0 {
		return nil
	}
	content := make([]interface{}, 0, len(results))
	for _, b := range results {
		content = append(content, blockToMap(b))
	}
	return w.writeFrame(map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role":    "user",
			"content": content,
		},
	})
}

// 终止子类型。error_ 前缀会让宿主 claudeResultError（agent_ai.go:6399）
// 把它转成 ai.error，因此失败必须用 error_ 开头的 subtype 且 is_error=true。
const (
	ResultSubtypeSuccess       = "success"
	ResultSubtypeErrorMaxTurns = "error_max_turns"
	ResultSubtypeErrorRun      = "error_during_execution"
	// ResultSubtypeInterrupted 是「用户/宿主主动停止」。
	//
	// 它刻意不带 error_ 前缀、也不置 is_error：宿主的 claudeResultError 只把
	// is_error==true 或 error_* 子类型当失败并转成 ai.error。用户点停止不该
	// 变成一条错误气泡——宿主会另发 ai.status(stopped)，那才是正确的收敛。
	ResultSubtypeInterrupted = "interrupted"
)

// Result 发终止帧。一次 run 只能发一次。
func (w *streamWriter) Result(subtype, text string, isError bool, usage *Usage, numTurns int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if subtype == "" {
		if isError {
			subtype = ResultSubtypeErrorRun
		} else {
			subtype = ResultSubtypeSuccess
		}
	}
	frame := map[string]interface{}{
		"type":      "result",
		"subtype":   subtype,
		"is_error":  isError,
		"result":    text,
		"num_turns": numTurns,
	}
	if usage != nil {
		frame["usage"] = map[string]interface{}{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		}
	}
	return w.writeFrame(frame)
}

// blockToMap 把 ContentBlock 转成 wire 形状。
//
// 不能直接 json.Marshal(ContentBlock)：omitempty 会把空 input 整个丢掉，
// 而宿主 claudeToolUseEvents 读 input 时依赖它至少是个对象。
func blockToMap(b ContentBlock) map[string]interface{} {
	out := map[string]interface{}{"type": b.Type}
	switch b.Type {
	case BlockTypeText:
		out["text"] = b.Text
	case BlockTypeImage:
		// 图片块没有 delta，source 就是全部内容。assistant 帧里几乎不会出现
		// （模型不产图），但 blockToMap 是通用映射，漏掉会写出 {"type":"image"}
		// 这种宿主无法识别的残块。
		//
		// 只写该 source 类型有意义的字段：base64 带 data 不带 url，url 反之。
		// 空字符串照样会被序列化出去，上游对多余字段的容忍度不一致。
		if b.Source != nil {
			src := map[string]interface{}{"type": b.Source.Type}
			if b.Source.MediaType != "" {
				src["media_type"] = b.Source.MediaType
			}
			if b.Source.Data != "" {
				src["data"] = b.Source.Data
			}
			if b.Source.URL != "" {
				src["url"] = b.Source.URL
			}
			out["source"] = src
		}
	case BlockTypeToolUse:
		out["id"] = b.ID
		out["name"] = b.Name
		input := b.Input
		if input == nil {
			input = map[string]interface{}{}
		}
		out["input"] = input
	case BlockTypeToolResult:
		out["tool_use_id"] = b.ToolUseID
		if b.Content != nil {
			out["content"] = b.Content
		} else {
			out["content"] = ""
		}
		if b.IsError {
			out["is_error"] = true
		}
	}
	return out
}
