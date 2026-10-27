package agentcore

import (
	"errors"
	"strings"
)

// prompt cache（提示词缓存）支持。
//
// 问题的形状：内核每一轮都把整段历史原样重发，而 history 只增不减。2026-10-27
// 实测一次 30 分钟的 run 往上游推了 16.9MB——按 20 轮摊，每轮约 850KB ≈ 20 万
// token，正好等于上下文本身的体积。没有缓存时，上游每一轮都要把这 20 万 token
// 从头 prefill 一遍，于是「每轮 1–2 分钟、半小时跑不完一轮对话」。
//
// 断点策略与 Claude Code 一致，三处（Anthropic 上限是 4 处）：
//
//  1. 最后一个工具定义   —— 工具 schema 整段静态，一次都不变
//  2. system 末尾        —— 环境块每次 run 只变一次，run 内稳定
//  3. 最后一条消息的末块 —— 移动断点，让「历史 + 本轮输入」整体成为可复用前缀
//
// 第 3 处是收益的大头：它把随轮次增长的对话体也纳入缓存，而不是只缓存静态前言。
//
// 为什么断点位置要移动而不是固定在尾部历史：固定的写法要在历史块上留标记，
// 而历史是追加式增长的，标记会跟着往下沉，命中率反而会随轮次劣化。移动断点
// 每次只标记当前尾部，下一轮的「上一轮尾部」自然落在缓存前缀内部。
const cacheControlEphemeral = "ephemeral"

func ephemeralCache() *CacheControl {
	return &CacheControl{Type: cacheControlEphemeral}
}

// applyCacheBreakpoints 就地给请求挂上缓存断点。重复调用安全（已标记则跳过）。
func applyCacheBreakpoints(req *MessagesRequest) {
	if req == nil {
		return
	}

	// 1. 工具定义尾部。
	if n := len(req.Tools); n > 0 && req.Tools[n-1].CacheControl == nil {
		req.Tools[n-1].CacheControl = ephemeralCache()
	}

	// 2. system 末尾：纯字符串就地升级成单块数组。
	switch sys := req.System.(type) {
	case string:
		if strings.TrimSpace(sys) != "" {
			req.System = []SystemBlock{{Type: BlockTypeText, Text: sys, CacheControl: ephemeralCache()}}
		}
	case []SystemBlock:
		if n := len(sys); n > 0 && sys[n-1].CacheControl == nil {
			blocks := make([]SystemBlock, n)
			copy(blocks, sys)
			blocks[n-1].CacheControl = ephemeralCache()
			req.System = blocks
		}
	}

	// 3. 最后一条消息的最后一个块。
	n := len(req.Messages)
	if n == 0 {
		return
	}
	// req.Messages 常常就是 r.history 本体（buildRequest 直接赋引用）。就地写回
	// 会把断点永久烙进历史，导致被上游拒绝后锁存降级也摘不掉——所以先断开切片。
	msgs := make([]Message, n)
	copy(msgs, req.Messages)
	req.Messages = msgs
	last := msgs[n-1]
	switch content := last.Content.(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return
		}
		last.Content = []ContentBlock{{Type: BlockTypeText, Text: content, CacheControl: ephemeralCache()}}
	case []ContentBlock:
		if len(content) == 0 {
			return
		}
		// 复制切片：history 里的块是共享的，就地改会污染下一轮的历史。
		blocks := make([]ContentBlock, len(content))
		copy(blocks, content)
		if blocks[len(blocks)-1].CacheControl == nil {
			blocks[len(blocks)-1].CacheControl = ephemeralCache()
		}
		last.Content = blocks
	default:
		return
	}
	req.Messages[n-1] = last
}

// isCacheRejection 判定这次失败是不是「上游不认 cache_control」。
//
// 我们没有能力事先知道每个上游（GLM / deepseek / 以及后续接入的）是否实现了
// Anthropic 的缓存字段。与其靠配置开关让人猜，不如照 effort 那套做法：真正被
// 拒一次就锁存、摘掉断点重跑，把「不支持」这件事降级成一次性的 4xx 而不是整轮
// 失败。
//
// 只认明确点名 cache_control / cache 的 4xx，避免把无关的参数错误误伤成缓存问题。
func isCacheRejection(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		if statusErr.status < 400 || statusErr.status >= 500 {
			return false
		}
		return mentionsCacheControl(statusErr.body)
	}
	var upErr *upstreamError
	if errors.As(err, &upErr) {
		if !strings.Contains(strings.ToLower(upErr.kind), "invalid_request") {
			return false
		}
		return mentionsCacheControl(upErr.body + " " + upErr.message)
	}
	return false
}

func mentionsCacheControl(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "cache_control") || strings.Contains(t, "cache control")
}
