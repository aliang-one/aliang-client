package agentcore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 缓存断点必须挂在三个位置：最后一个工具、system 末尾、最后一条消息的末块。
//
// 这三个位置一起构成「静态前言 + 增长中的对话体」的完整前缀。少挂最后一个
// （消息末块），缓存就只能覆盖 system/tools，对话体每轮仍要全量 prefill——
// 而后者才是体积的大头。
func TestApplyCacheBreakpoints(t *testing.T) {
	req := &MessagesRequest{
		Model:     "glm-5.3[1m]",
		MaxTokens: 32000,
		System:    "你是 AliangCode。",
		Messages: []Message{
			TextMessage("user", "帮我看看这个 bug"),
			TextMessage("assistant", "好，我先读一下"),
		},
		Tools: []ToolDef{{Name: "read"}, {Name: "bash"}},
	}

	applyCacheBreakpoints(req)

	if got := req.Tools[len(req.Tools)-1].CacheControl; got == nil || got.Type != cacheControlEphemeral {
		t.Errorf("最后一个工具没有断点: %+v", got)
	}
	if req.Tools[0].CacheControl != nil {
		t.Error("只有最后一个工具该挂断点，第一个不该挂")
	}

	blocks, ok := req.System.([]SystemBlock)
	if !ok {
		t.Fatalf("system 应该被升级成块数组，实际 %T", req.System)
	}
	if len(blocks) != 1 || blocks[0].Text != "你是 AliangCode。" {
		t.Fatalf("system 块内容不对: %+v", blocks)
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != cacheControlEphemeral {
		t.Error("system 块没有断点")
	}

	last := req.Messages[len(req.Messages)-1]
	lastBlocks, ok := last.Content.([]ContentBlock)
	if !ok {
		t.Fatalf("末条消息应该被升级成块数组，实际 %T", last.Content)
	}
	if lastBlocks[len(lastBlocks)-1].CacheControl == nil {
		t.Error("末条消息的末块没有断点")
	}
	if lastBlocks[len(lastBlocks)-1].Type != BlockTypeText {
		t.Errorf("升级后的块类型应为 text，实际 %q", lastBlocks[len(lastBlocks)-1].Type)
	}

	// 断点数量不超过 Anthropic 的 4 个上限。
	if n := countBreakpoints(req); n != 3 {
		t.Errorf("断点数 = %d, want 3", n)
	}
}

// 重复调用必须安全：每轮 buildRequest 之后都会挂一次，不能越挂越多。
func TestApplyCacheBreakpointsIdempotent(t *testing.T) {
	req := &MessagesRequest{
		System:   "sys",
		Messages: []Message{TextMessage("user", "hi")},
		Tools:    []ToolDef{{Name: "read"}},
	}
	applyCacheBreakpoints(req)
	applyCacheBreakpoints(req)
	applyCacheBreakpoints(req)
	if n := countBreakpoints(req); n != 3 {
		t.Errorf("三次挂断点后断点数 = %d, want 3", n)
	}
}

// 首块形态的 JSON 必须是网关认的 Anthropic 形状——这里直接比对序列化结果，
// 因为「字段名对不对」是这一层唯一真正对外可见的契约。
func TestCacheBreakpointJSONShape(t *testing.T) {
	req := &MessagesRequest{
		Model:    "m",
		System:   "sys",
		Messages: []Message{TextMessage("user", "hi")},
	}
	applyCacheBreakpoints(req)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(raw)
	for _, want := range []string{
		`"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}]`,
		`"cache_control":{"type":"ephemeral"}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("序列化结果缺少 %s\n实际: %s", want, s)
		}
	}
}

// 没有任何内容时不挂断点，也不要把空 system 升级成块数组（那会被上游当成
// 一个空的 system 块，某些实现会直接 400）。
func TestApplyCacheBreakpointsSkipsEmpty(t *testing.T) {
	req := &MessagesRequest{Messages: []Message{{Role: "user"}}}
	applyCacheBreakpoints(req)
	if req.System != nil {
		t.Errorf("空 system 不该被升级，实际 %#v", req.System)
	}
	if n := countBreakpoints(req); n != 0 {
		t.Errorf("空请求不该有断点，实际 %d", n)
	}
}

// 上游不认 cache_control 时必须能被识别出来（据此降级重试）。
func TestIsCacheRejection(t *testing.T) {
	yes := []error{
		&httpStatusError{status: 400, body: `{"error":{"message":"messages.0.content.0: Extra inputs are not permitted: cache_control"}}`},
		&httpStatusError{status: 422, body: `cache control is not supported`},
		&upstreamError{kind: "invalid_request_error", message: "unknown field cache_control"},
	}
	for _, err := range yes {
		if !isCacheRejection(err) {
			t.Errorf("应判为缓存拒绝: %v", err)
		}
	}

	no := []error{
		nil,
		&httpStatusError{status: 500, body: "cache_control"},           // 5xx 不是参数问题
		&httpStatusError{status: 400, body: "model is required"},       // 无关参数
		&upstreamError{kind: "rate_limit_error", message: "slow down"}, // 限流
		errors.New("cache_control mentioned in local error only"),      // 非上游错误类型
	}
	for _, err := range no {
		if isCacheRejection(err) {
			t.Errorf("不该判为缓存拒绝: %v", err)
		}
	}
}

// 图片占位符化：第一轮之后历史里的图片块必须换成文字，且不再持有 base64。
func TestStubHistoricalImages(t *testing.T) {
	r := &runner{
		writer: newStreamWriter(nil, "sess"),
		history: []Message{
			BlocksMessage("user", []ContentBlock{
				{Type: BlockTypeText, Text: "这张图里有什么"},
				{Type: BlockTypeImage, Source: &ImageSource{
					Type: "base64", MediaType: "image/jpeg", Data: strings.Repeat("A", 4096),
				}},
			}),
			TextMessage("assistant", "一台笔记本"),
		},
	}

	r.stubHistoricalImages()

	blocks, ok := r.history[0].Content.([]ContentBlock)
	if !ok {
		t.Fatalf("历史消息应保持块形态，实际 %T", r.history[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("块数应保持 2，实际 %d", len(blocks))
	}
	if blocks[1].Type != BlockTypeText {
		t.Errorf("图片块应被换成文本块，实际 %q", blocks[1].Type)
	}
	if blocks[1].Source != nil && blocks[1].Source.Data != "" {
		t.Error("占位化后不应再持有 base64 数据")
	}
	if !strings.Contains(blocks[1].Text, "image/jpeg") {
		t.Errorf("占位符应保留媒介类型，实际 %q", blocks[1].Text)
	}
	if blocks[0].Text != "这张图里有什么" {
		t.Error("文本块不该被改动")
	}
}

// 共享底层数组不得被隔空改写：ask 侧的 history 与本轮已发出的 req.Messages
// 可能指向同一片内存。
func TestStubHistoricalImagesDoesNotMutateSharedBlocks(t *testing.T) {
	orig := []ContentBlock{
		{Type: BlockTypeText, Text: "看这张"},
		{Type: BlockTypeImage, Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "QUJD"}},
	}
	r := &runner{
		writer:  newStreamWriter(nil, "sess"),
		history: []Message{BlocksMessage("user", orig)},
	}
	r.stubHistoricalImages()

	if orig[1].Type != BlockTypeImage {
		t.Error("原始切片被就地改写了（应与 history 解耦）")
	}
}

// countBreakpoints 数一个请求里的缓存断点总数（测试辅助）。
func countBreakpoints(req *MessagesRequest) int {
	n := 0
	for _, tool := range req.Tools {
		if tool.CacheControl != nil {
			n++
		}
	}
	if blocks, ok := req.System.([]SystemBlock); ok {
		for _, b := range blocks {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	for _, msg := range req.Messages {
		if blocks, ok := msg.Content.([]ContentBlock); ok {
			for _, b := range blocks {
				if b.CacheControl != nil {
					n++
				}
			}
		}
	}
	return n
}
