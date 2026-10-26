package agentcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 图片输入的两条通路，各锁一组：
//
//	1. 用户上传的图片 → 请求体里的 image 块。内核只需正确**序列化**（宿主负责
//	   读文件、转 base64、拼块）。丢在这里不会有任何报错。
//	2. Read 工具读到的图片 → tool_result 里内联的 image 块。内核自己产，从
//	   字节到 wire 形状全链路都在本包内。
//
// 为什么值得单独钉：两条通路的失败表现完全一样——模型回一句「我看不到图片」，
// 用户看到的是「这个功能不存在」，而不是「出错了」。没有断言就只能靠真机猜。

// tinyPNG 是一段带 NUL 的假 PNG 头。
//
// 关键是那几个 0x00：真实 PNG 的 IHDR 长度字段就是 00 00 00 0D，前 8KB 必有
// NUL。这正好是 Read 必须按扩展名**先于**二进制探测分流的原因——顺序反了，
// 每张图都会被当成二进制拒掉。内核不解码像素，所以假字节足够。
var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x0d}

// ---------------------------------------------------------------------------
// ToolResult.ContentForModel：无图保持字符串形状
// ---------------------------------------------------------------------------

func TestToolResultContentForModel(t *testing.T) {
	img1 := ImageBlock("image/png", tinyPNG)
	img2 := ImageBlock("image/jpeg", []byte("jpeg"))

	t.Run("无图仍是字符串", func(t *testing.T) {
		got := ToolResult{Content: "纯文本结果"}.ContentForModel()
		s, ok := got.(string)
		if !ok {
			t.Fatalf("无图时 content 应当是 string，实际 %T", got)
		}
		if s != "纯文本结果" {
			t.Errorf("content = %q, want 纯文本结果", s)
		}
	})

	t.Run("文本加单图", func(t *testing.T) {
		blocks, ok := ToolResult{Content: "读到了", Images: []ContentBlock{img1}}.ContentForModel().([]ContentBlock)
		if !ok {
			t.Fatalf("有图时 content 应当是 []ContentBlock")
		}
		if len(blocks) != 2 {
			t.Fatalf("块数 = %d, want 2（文本 + 图）", len(blocks))
		}
		if blocks[0].Type != BlockTypeText || blocks[0].Text != "读到了" {
			t.Errorf("首块 = %+v, want 文本块", blocks[0])
		}
		if blocks[1].Type != BlockTypeImage || blocks[1].Source == nil {
			t.Errorf("次块 = %+v, want 图片块", blocks[1])
		}
	})

	t.Run("文本加多图", func(t *testing.T) {
		blocks := ToolResult{Content: "两张", Images: []ContentBlock{img1, img2}}.ContentForModel().([]ContentBlock)
		if len(blocks) != 3 {
			t.Fatalf("块数 = %d, want 3（文本 + 两张图）", len(blocks))
		}
		if blocks[2].Source.MediaType != "image/jpeg" {
			t.Errorf("第三块 media_type = %q, want image/jpeg", blocks[2].Source.MediaType)
		}
	})

	t.Run("空文本只有图", func(t *testing.T) {
		// 空白文本不占一个块：Anthropic 不接受空 text 块。
		blocks := ToolResult{Content: "   ", Images: []ContentBlock{img1}}.ContentForModel().([]ContentBlock)
		if len(blocks) != 1 {
			t.Fatalf("块数 = %d, want 1（空文本被丢掉）", len(blocks))
		}
		if blocks[0].Type != BlockTypeImage {
			t.Errorf("首块类型 = %q, want image", blocks[0].Type)
		}
	})
}

// ---------------------------------------------------------------------------
// Read 读图片
// ---------------------------------------------------------------------------

func TestReadImageMediaType(t *testing.T) {
	cases := map[string]string{
		"a.png":  "image/png",
		"a.PNG":  "image/png",
		"a.jpg":  "image/jpeg",
		"a.jpeg": "image/jpeg",
		"a.gif":  "image/gif",
		"a.webp": "image/webp",
	}
	for name, want := range cases {
		got, ok := readImageMediaType(name)
		if !ok {
			t.Errorf("%s 应当被识别为图片", name)
			continue
		}
		if got != want {
			t.Errorf("%s media_type = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"a.txt", "a.bmp", "a.svg", "a.tiff", "noext"} {
		if _, ok := readImageMediaType(name); ok {
			t.Errorf("%s 不该被当成图片（Anthropic 不支持该格式）", name)
		}
	}
}

func TestReadToolReturnsImage(t *testing.T) {
	env, root := projectEnv(t)
	if err := os.WriteFile(filepath.Join(root, "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{"file_path": "shot.png"}, env)
	if res.IsError {
		t.Fatalf("读图片不应失败: %s", res.Content)
	}
	if len(res.Images) != 1 {
		t.Fatalf("Images 数量 = %d, want 1", len(res.Images))
	}
	src := res.Images[0].Source
	if src == nil {
		t.Fatal("图片块缺少 source")
	}
	if src.Type != "base64" {
		t.Errorf("source.type = %q, want base64", src.Type)
	}
	if src.MediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", src.MediaType)
	}
	if src.Data != base64.StdEncoding.EncodeToString(tinyPNG) {
		t.Errorf("source.data 不是文件的 base64")
	}
	// 文本摘要要能让模型对上号，否则它不知道这张图是哪个文件。
	if !strings.Contains(res.Content, "shot.png") || !strings.Contains(res.Content, "image/png") {
		t.Errorf("Content = %q, 应包含文件名与 media_type", res.Content)
	}
}

func TestReadToolRejectsOversizedImage(t *testing.T) {
	env, root := projectEnv(t)
	p := filepath.Join(root, "huge.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Truncate 建稀疏文件，不真写 5MB。
	if err := f.Truncate(readImageRawMaxBytes + 1); err != nil {
		f.Close()
		t.Fatalf("Truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{"file_path": "huge.png"}, env)
	if !res.IsError {
		t.Fatal("超过上限的图片应当被拒绝")
	}
	if len(res.Images) != 0 {
		t.Errorf("被拒时不该回填图片，实际 %d 张", len(res.Images))
	}
	// 提示必须可执行：只说「太大」模型会原样重试，说「缩小/换小截图」才有下一步。
	if !strings.Contains(res.Content, "太大") || !strings.Contains(res.Content, "缩小") {
		t.Errorf("Content = %q, 应说明超限并给出缩小建议", res.Content)
	}
}

func TestReadToolImageIgnoresOffsetLimit(t *testing.T) {
	env, root := projectEnv(t)
	if err := os.WriteFile(filepath.Join(root, "a.png"), tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// 模型常把 offset/limit 一起带上；报错只会让它白跑一轮，应当静默忽略。
	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.png",
		"offset":    float64(100),
		"limit":     float64(10),
	}, env)
	if res.IsError {
		t.Fatalf("offset/limit 对图片应被忽略，不该报错: %s", res.Content)
	}
	if len(res.Images) != 1 {
		t.Fatalf("Images 数量 = %d, want 1", len(res.Images))
	}
}

// ---------------------------------------------------------------------------
// 入站：SSE 里的 image 块
// ---------------------------------------------------------------------------

func TestSSEParsesInboundImageBlock(t *testing.T) {
	// 形状照 Anthropic 的 content_block_start 来。sseEvent.ContentBlock 是
	// *ContentBlock，Source 加上 json tag 后这里就该直接解析出来。
	raw := `{"type":"content_block_start","index":1,"content_block":` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}}`
	var ev sseEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if ev.ContentBlock == nil || ev.ContentBlock.Source == nil {
		t.Fatalf("image 块没解析出 source: %+v", ev.ContentBlock)
	}
	if ev.ContentBlock.Source.MediaType != "image/png" || ev.ContentBlock.Source.Data != "aGk=" {
		t.Errorf("source = %+v, want media_type=image/png data=aGk=", ev.ContentBlock.Source)
	}
}

func TestTranslateEventImageBlock(t *testing.T) {
	ev := &sseEvent{
		Type:  "content_block_start",
		Index: 2,
		ContentBlock: &ContentBlock{
			Type:   BlockTypeImage,
			Source: &ImageSource{Type: "base64", MediaType: "image/webp", Data: "aGk="},
		},
	}
	got, err := translateEvent(ev)
	if err != nil {
		t.Fatalf("translateEvent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("事件数 = %d, want 1", len(got))
	}
	if got[0].Kind != EventImage {
		t.Fatalf("Kind = %q, want %q", got[0].Kind, EventImage)
	}
	if got[0].Index != 2 {
		t.Errorf("Index = %d, want 2", got[0].Index)
	}
	if got[0].Image == nil || got[0].Image.MediaType != "image/webp" {
		t.Errorf("Image = %+v, want media_type=image/webp", got[0].Image)
	}
}

func TestTranslateEventImageWithoutSourceIsDropped(t *testing.T) {
	// 没有 source 的 image 块是残块，产出去只会在下游变成
	// {"type":"image"} 这种谁也认不出的东西。
	got, err := translateEvent(&sseEvent{
		Type:         "content_block_start",
		ContentBlock: &ContentBlock{Type: BlockTypeImage},
	})
	if err != nil {
		t.Fatalf("translateEvent: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("事件数 = %d, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// 出站：blockToMap
// ---------------------------------------------------------------------------

func TestBlockToMapImageOmitsIrrelevantSourceFields(t *testing.T) {
	base64Out := blockToMap(ContentBlock{
		Type:   BlockTypeImage,
		Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: "aGk="},
	})
	src, ok := base64Out["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("source 不是对象: %v", base64Out["source"])
	}
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "aGk=" {
		t.Errorf("source = %+v, 字段不全", src)
	}
	if _, has := src["url"]; has {
		t.Error("base64 source 不应带 url（上游对多余字段容忍度不一致）")
	}

	urlOut := blockToMap(ImageURLBlock("https://example.com/a.png", "image/png"))
	urlSrc, ok := urlOut["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("url source 不是对象: %v", urlOut["source"])
	}
	if urlSrc["type"] != "url" || urlSrc["url"] != "https://example.com/a.png" {
		t.Errorf("source = %+v, url 型字段不对", urlSrc)
	}
	if _, has := urlSrc["data"]; has {
		t.Error("url source 不应带 data")
	}
}

func TestBlockToMapImageWithoutSourceOmitsField(t *testing.T) {
	out := blockToMap(ContentBlock{Type: BlockTypeImage})
	if _, has := out["source"]; has {
		t.Errorf("无 source 时不该写出 source 字段: %+v", out)
	}
}

// ---------------------------------------------------------------------------
// 端到端：工具回填的图片真的进了下一次 LLM 请求
// ---------------------------------------------------------------------------

func TestRunToolResultCarriesImage(t *testing.T) {
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{steerTurn1(), steerTurn2()}))

	stub := &stubTool{
		name: "Stub",
		result: ToolResult{
			Content: "已读取 shot.png（image/png，12 字节）",
			Images:  []ContentBlock{ImageBlock("image/png", tinyPNG)},
		},
	}
	registry := NewRegistry()
	registry.Register(stub)

	frameRec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Emit:      frameRec.emit,
		SessionID: "sess-img",
		Tools:     registry,
	}
	res, err := Run(context.Background(), cfg, []Message{TextMessage("user", "看看这张截图")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("run 不该以错误收尾: %s", res.Text)
	}

	// 1. 内核记录里保留了图片（宿主做审计/展示时还能拿到）。
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls 数量 = %d, want 1", len(res.ToolCalls))
	}
	if len(res.ToolCalls[0].Result.Images) != 1 {
		t.Fatalf("ToolCallResult.Images 数量 = %d, want 1", len(res.ToolCalls[0].Result.Images))
	}

	// 2. user 帧（tool_result 回填）里是「文本块 + 图片块」数组。
	uf := userFrame(t, frameRec.frames(t))
	content := messageContent(t, uf)
	if len(content) != 1 {
		t.Fatalf("user 帧块数 = %d, want 1（一条 tool_result）", len(content))
	}
	tr, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_result 不是对象: %v", content[0])
	}
	if tr["type"] != BlockTypeToolResult {
		t.Fatalf("块类型 = %v, want tool_result", tr["type"])
	}
	inner, ok := tr["content"].([]interface{})
	if !ok {
		t.Fatalf("带图时 tool_result.content 应当是数组，实际 %T", tr["content"])
	}
	if len(inner) != 2 {
		t.Fatalf("tool_result.content 块数 = %d, want 2（文本 + 图）", len(inner))
	}
	if first, _ := inner[0].(map[string]interface{}); first["type"] != BlockTypeText {
		t.Errorf("首块 = %+v, want 文本块", inner[0])
	}
	imgBlock, _ := inner[1].(map[string]interface{})
	if imgBlock["type"] != BlockTypeImage {
		t.Fatalf("次块 = %+v, want 图片块", inner[1])
	}
	imgSrc, _ := imgBlock["source"].(map[string]interface{})
	if imgSrc["media_type"] != "image/png" {
		t.Errorf("图片 media_type = %v, want image/png", imgSrc["media_type"])
	}
	if imgSrc["data"] != base64.StdEncoding.EncodeToString(tinyPNG) {
		t.Errorf("图片 data 不是原始字节的 base64")
	}

	// 3. 最关键的一条：第二次请求（下一轮 LLM 调用）真的把图带上了。
	//    帧正确但请求体丢了图，模型照样看不到——这两件事是分开的。
	snaps := rec.snapshot()
	if len(snaps) < 2 {
		t.Fatalf("请求次数 = %d, want >= 2", len(snaps))
	}
	last := snaps[1].Messages[len(snaps[1].Messages)-1]
	if last.Role != "user" {
		t.Fatalf("末条消息角色 = %q, want user", last.Role)
	}
	lastBlocks, ok := last.Content.([]interface{})
	if !ok {
		// 请求体经 JSON 往返，[]ContentBlock 会解成 []interface{}。
		t.Fatalf("末条消息 content 应当是数组，实际 %T", last.Content)
	}
	found := false
	for _, raw := range lastBlocks {
		block, _ := raw.(map[string]interface{})
		if block == nil || block["type"] != BlockTypeToolResult {
			continue
		}
		parts, ok := block["content"].([]interface{})
		if !ok {
			continue
		}
		for _, part := range parts {
			p, _ := part.(map[string]interface{})
			if p == nil || p["type"] != BlockTypeImage {
				continue
			}
			src, _ := p["source"].(map[string]interface{})
			if src["data"] == base64.StdEncoding.EncodeToString(tinyPNG) {
				found = true
			}
		}
	}
	if !found {
		t.Error("下一轮请求体的 tool_result 里没有图片块——模型看不到图")
	}
}

func TestRunToolResultTextStaysString(t *testing.T) {
	// 形状回归守卫：无图工具的 tool_result.content 必须还是**字符串**。
	// 升级成数组不会报错，但会改变所有既有工具的请求形状，属于不必要的面。
	rec := &requestRecorder{}
	srv := sseServer(t, rec.handler(t, []string{steerTurn1(), steerTurn2()}))

	stub := &stubTool{name: "Stub", result: ToolResult{Content: "ok"}}
	registry := NewRegistry()
	registry.Register(stub)

	frameRec := &frameRecorder{}
	cfg := &Config{
		Client:    NewClient(srv.URL),
		Model:     "claude-x",
		Emit:      frameRec.emit,
		SessionID: "sess-txt",
		Tools:     registry,
	}
	if _, err := Run(context.Background(), cfg, []Message{TextMessage("user", "跑个工具")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	uf := userFrame(t, frameRec.frames(t))
	content := messageContent(t, uf)
	tr, _ := content[0].(map[string]interface{})
	if _, ok := tr["content"].(string); !ok {
		t.Errorf("无图时 tool_result.content 应当是字符串，实际 %T", tr["content"])
	}

	snaps := rec.snapshot()
	if len(snaps) < 2 {
		t.Fatalf("请求次数 = %d, want >= 2", len(snaps))
	}
	last := snaps[1].Messages[len(snaps[1].Messages)-1]
	lastBlocks, _ := last.Content.([]interface{})
	if len(lastBlocks) == 0 {
		t.Fatalf("末条消息 content 形状异常: %T", last.Content)
	}
	block, _ := lastBlocks[0].(map[string]interface{})
	if _, ok := block["content"].(string); !ok {
		t.Errorf("下一轮请求里 tool_result.content 应当是字符串，实际 %T", block["content"])
	}
}
