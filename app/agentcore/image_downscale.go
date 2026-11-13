package agentcore

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"
)

// Read 工具的图片降采样。
//
// 为什么必须在本地做：remotion 渲染的 1080p 预览帧 PNG 单张 2MB 上下，两张
// 连续 Read 后原样 base64 内联，单个请求体直接到 5~6MB。网关转发这种大请求
// 到推理上游失败后回笼统的 502 upstream_error（2026-11-12~13 的视频会话连续
// 7 次失败全部踩在这里；同时段 100k+ 的纯文本会话全程正常，排除上游本身故障）。
// 而模型看预览/核对布局根本不需要 1080p——长边 1024 的 JPEG 足够，体积差一个
// 数量级。
//
// 策略：解码后长边超过 readImageMaxEdge 才动手，等比缩小并统一转 JPEG。
// 已经足够小的图原样通过，不做无谓的编解码（避免 generation loss 和 CPU 浪费）。
// 任何解码/缩放失败都原样返回——上面还有 readImageRawMaxBytes 的 5MiB 闸兜底，
// 宁可发原图也不要把一张能用的图拒之门外。

// readImageMaxEdge 是 Read 回填图片的长边上限（px）。
const readImageMaxEdge = 1024

// readImageJPEGQuality 是降采样后 JPEG 的编码质量。
const readImageJPEGQuality = 80

// downscaleImageIfNeeded 在图片长边超限时等比降采样并转 JPEG。
//
// 返回 (新字节, 新媒体类型, 是否降采样, 错误)。未降采样时原字节原类型透传。
// GIF 动图只取第一帧（模型核对内容用不到动画）；webp 交给 x/image/webp，
// 动画 webp 不支持解码时同样透传。
func downscaleImageIfNeeded(data []byte, mediaType string) ([]byte, string, bool) {
	var src image.Image
	var err error
	switch mediaType {
	case "image/png":
		src, err = png.Decode(bytes.NewReader(data))
	case "image/jpeg":
		src, err = jpeg.Decode(bytes.NewReader(data))
	case "image/gif":
		src, err = gif.Decode(bytes.NewReader(data))
	case "image/webp":
		src, err = xwebp.Decode(bytes.NewReader(data))
	default:
		return data, mediaType, false
	}
	if err != nil || src == nil {
		return data, mediaType, false
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	long := max(w, h)
	if long <= readImageMaxEdge {
		return data, mediaType, false
	}
	// 等比：长边贴到上限，短边按比例缩（至少 1px）。
	newW, newH := w*readImageMaxEdge/long, h*readImageMaxEdge/long
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	// 先垫白再缩：截图类 PNG 常带 alpha 通道，JPEG 没有 alpha，不垫白透明区域
	// 会变成黑底，模型会把截图看成反色。
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, xdraw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: readImageJPEGQuality}); err != nil {
		return data, mediaType, false
	}
	return buf.Bytes(), "image/jpeg", true
}
