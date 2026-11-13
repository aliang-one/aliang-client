package agentcore

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestPNG 生成 w×h 的纯色 PNG，用于构造不同尺寸的测试图。
func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func decodeSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err == nil {
		return img.Bounds().Dx(), img.Bounds().Dy()
	}
	jpg, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("无法解码测试图片: %v", err)
	}
	return jpg.Bounds().Dx(), jpg.Bounds().Dy()
}

// 小图必须原样通过：无谓的再编码只会引入 generation loss。
func TestDownscaleSkipsSmallImage(t *testing.T) {
	data := makeTestPNG(t, 320, 200)
	got, mediaType, scaled := downscaleImageIfNeeded(data, "image/png")
	if scaled {
		t.Fatal("长边 320px 的图不该被降采样")
	}
	if mediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", mediaType)
	}
	if !bytes.Equal(got, data) {
		t.Error("未降采样时字节必须原样透传")
	}
}

// makeNoisePNG 生成 w×h 的伪随机噪声 PNG。
//
// 关键是"不可压缩"：视频帧/照片类内容 PNG 压不动（remotion 帧 2MB+ 就是这个
// 形态），而平滑渐变 PNG 自带强压缩，会让「降采样后更小」的断言失真。
func makeNoisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223 // LCG：确定性且不可 deflate
			img.Set(x, y, color.RGBA{R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// 1080p 帧要被压到长边 1024 并转 JPEG，且体积必须显著下降（视频帧场景的核心收益）。
func TestDownscaleScales1080pTo1024(t *testing.T) {
	data := makeNoisePNG(t, 1920, 1080)
	got, mediaType, scaled := downscaleImageIfNeeded(data, "image/png")
	if !scaled {
		t.Fatal("长边 1920px 的图应当被降采样")
	}
	if mediaType != "image/jpeg" {
		t.Errorf("media_type = %q, want image/jpeg", mediaType)
	}
	w, h := decodeSize(t, got)
	if w != 1024 || h != 576 {
		t.Errorf("降采样后尺寸 = %dx%d, want 1024x576（等比）", w, h)
	}
	if len(got) >= len(data) {
		t.Errorf("降采样后 %d 字节应当小于原图 %d 字节", len(got), len(data))
	}
}

// 竖图同样等比。
func TestDownscaleScalesPortrait(t *testing.T) {
	data := makeTestPNG(t, 1080, 1920)
	got, _, scaled := downscaleImageIfNeeded(data, "image/png")
	if !scaled {
		t.Fatal("竖版 1080x1920 应当被降采样")
	}
	w, h := decodeSize(t, got)
	if w != 576 || h != 1024 {
		t.Errorf("降采样后尺寸 = %dx%d, want 576x1024", w, h)
	}
}

// JPEG 大图也要走缩放路径（相机原图常是 JPEG）。
func TestDownscaleJPEGSource(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2048, 1536))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	got, mediaType, scaled := downscaleImageIfNeeded(buf.Bytes(), "image/jpeg")
	if !scaled {
		t.Fatal("2048px 的 JPEG 应当被降采样")
	}
	if mediaType != "image/jpeg" {
		t.Errorf("media_type = %q, want image/jpeg", mediaType)
	}
	w, h := decodeSize(t, got)
	if w != 1024 || h != 768 {
		t.Errorf("降采样后尺寸 = %dx%d, want 1024x768", w, h)
	}
}

// 带 alpha 通道的截图缩放后不能变黑底：透明区域垫白。
func TestDownscalePadsAlphaWithWhite(t *testing.T) {
	// 整张图全透明 + 一个不透明像素。JPEG 化后角落应当接近白而不是黑。
	img := image.NewRGBA(image.Rect(0, 0, 2048, 64))
	for x := 0; x < 8; x++ {
		img.Set(x, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	got, _, scaled := downscaleImageIfNeeded(buf.Bytes(), "image/png")
	if !scaled {
		t.Fatal("2048px 的图应当被降采样")
	}
	jpg, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("解码输出: %v", err)
	}
	// 右侧远端一个像素：垫了白应接近 255；不垫白（合成黑）会接近 0。
	r, g, b, _ := jpg.At(jpg.Bounds().Max.X-1, 0).RGBA()
	if r < 65000 || g < 65000 || b < 65000 {
		t.Errorf("透明区域垫白失败: rgb=(%d,%d,%d)，偏黑说明 alpha 处理有问题", r>>8, g>>8, b>>8)
	}
}

// 解码失败必须原样透传，不能把图弄丢——5MiB 闸在上游兜底。
func TestDownscalePassthroughOnGarbage(t *testing.T) {
	junk := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 'j', 'u', 'n', 'k'}
	got, mediaType, scaled := downscaleImageIfNeeded(junk, "image/png")
	if scaled {
		t.Fatal("解码失败的图不该标记为已降采样")
	}
	if !bytes.Equal(got, junk) {
		t.Error("解码失败时字节必须原样透传")
	}
	if mediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", mediaType)
	}
}

// 未知媒体类型直接透传。
func TestDownscaleUnknownMediaType(t *testing.T) {
	data := []byte("whatever")
	got, mediaType, scaled := downscaleImageIfNeeded(data, "image/x-odd")
	if scaled || !bytes.Equal(got, data) || mediaType != "image/x-odd" {
		t.Fatal("未知类型必须原样透传")
	}
}

// Read 工具读大图：回填的是降采样后的 JPEG，文本说明带「已压缩」。
func TestReadToolDownscalesLargeImage(t *testing.T) {
	env, root := projectEnv(t)
	// 噪声图取 1200x800：落在真实故障区间（>1024 触发降采样，文件 ~3.8MB
	// 低于 5MiB 硬闸——5MiB 以上走的是拒绝路径，TestReadToolRejectsOversizedImage 已钉）。
	if err := os.WriteFile(filepath.Join(root, "frame.png"), makeNoisePNG(t, 1200, 800), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := (&readTool{}).Execute(t.Context(), map[string]interface{}{"file_path": "frame.png"}, env)
	if res.IsError {
		t.Fatalf("读大图不应失败: %s", res.Content)
	}
	if len(res.Images) != 1 {
		t.Fatalf("Images 数量 = %d, want 1", len(res.Images))
	}
	src := res.Images[0].Source
	if src.MediaType != "image/jpeg" {
		t.Errorf("media_type = %q, want image/jpeg（已降采样）", src.MediaType)
	}
	raw, err := base64.StdEncoding.DecodeString(src.Data)
	if err != nil {
		t.Fatalf("source.data 不是合法 base64: %v", err)
	}
	w, h := decodeSize(t, raw)
	if w > readImageMaxEdge && h > readImageMaxEdge {
		t.Errorf("降采样未生效: %dx%d", w, h)
	}
	if !strings.Contains(res.Content, "已压缩") || !strings.Contains(res.Content, "frame.png") {
		t.Errorf("Content = %q, 应说明已压缩并保留文件名", res.Content)
	}
	if got := len(raw); got > 512*1024 {
		t.Errorf("降采样后仍有 %d 字节，压缩未生效", got)
	}
}

// 小图路径保持 byte-for-byte 兼容（占位符/cache 等下游逻辑依赖确定形状）。
func TestReadToolSmallImageUnchanged(t *testing.T) {
	env, root := projectEnv(t)
	if err := os.WriteFile(filepath.Join(root, "small.png"), makeTestPNG(t, 64, 48), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := (&readTool{}).Execute(t.Context(), map[string]interface{}{"file_path": "small.png"}, env)
	if res.IsError {
		t.Fatalf("读小图不应失败: %s", res.Content)
	}
	src := res.Images[0].Source
	if src.MediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png（小图不转码）", src.MediaType)
	}
	if strings.Contains(res.Content, "压缩") {
		t.Errorf("Content = %q, 小图不应提示压缩", res.Content)
	}
}
