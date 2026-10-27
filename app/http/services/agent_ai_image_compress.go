package services

// 图片附件的体积压缩。
//
// 为什么必须做：手机上传的照片常常是 3–6MB 的原始分辨率照片（iPhone 4032×3024）。
// 编码成 base64 还要再乘 4/3，一张图就是 4–8MB 的请求体，而且这份体积会跟着
// 会话历史在**每一轮**重新上传（见 app/agentcore/media.go 的占位符化，那解决
// 的是第二轮之后；这里解决的是第一轮本身）。2026-10-27 实测一次 30 分钟的 run
// 往上游推了 16.9MB，其中一大块就是同一张照片的反复重传。
//
// 压缩的两重收益：
//  1. 带宽：长边 1568px + JPEG q80，5MB 照片通常落到 200–400KB；
//  2. token：图片 token 随像素数增长，降分辨率就直接省 prefill。
//
// 长边 1568 不是随便挑的：这是 Anthropic 文档给的推荐上限，超过之后模型看到的
// 细节不再增加，纯粹白烧 token。
//
// 保守原则：只在「确实变小」时才替换。WebP 标准库不能解码、GIF 可能是动图，
// 两者一律原样透传——压坏了图比不压更糟。

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"

	// 解码器注册：image.Decode 走注册表，PNG/JPEG/GIF 各自在自己的 init 里注册。
	// PNG 必须显式引（否则手机截图这类最常见的输入解不出来），GIF 只为识别格式。
	_ "image/gif"
	_ "image/png"
)

const (
	// claudeImageLongEdgeMax 是送给模型的图片长边上限（像素）。
	claudeImageLongEdgeMax = 1568
	// claudeImageJPEGQuality 是重编码质量。80 是「肉眼看不出差别」和
	// 「体积降到 1/10」之间的常用折中。
	claudeImageJPEGQuality = 80
	// claudeImageCompressMinBytes 以下不再尝试压缩：小图的收益抵不上重编码
	// 带来的画质损失与 CPU。
	claudeImageCompressMinBytes = 256 * 1024
)

// compressImageForModel 尝试把一张图片压小，返回（可能替换过的）字节与 media type。
//
// 只在结果确实更小时才替换输入；任何一步失败都原样返回。compressed 用于日志。
func compressImageForModel(raw []byte, mediaType string) (out []byte, outMediaType string, note string) {
	switch mediaType {
	case "image/gif", "image/webp":
		// GIF 可能是动图（重编码会丢帧），WebP 标准库没有解码器。原样透传。
		return raw, mediaType, ""
	}

	src, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw, mediaType, ""
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return raw, mediaType, ""
	}

	longEdge := w
	if h > longEdge {
		longEdge = h
	}
	needsResize := longEdge > claudeImageLongEdgeMax
	if !needsResize && len(raw) <= claudeImageCompressMinBytes {
		return raw, mediaType, ""
	}

	// 统一转成不透明的 RGBA 再处理：JPEG 没有 alpha 通道，垫白底比让透明区域
	// 变黑（或变成随机色）更接近用户看到的截图。
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, canvas.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), src, bounds.Min, draw.Over)

	target := canvas
	tw, th := w, h
	if needsResize {
		scale := float64(claudeImageLongEdgeMax) / float64(longEdge)
		tw = int(float64(w)*scale + 0.5)
		th = int(float64(h)*scale + 0.5)
		if tw < 1 {
			tw = 1
		}
		if th < 1 {
			th = 1
		}
		target = downscaleArea(canvas, tw, th)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, target, &jpeg.Options{Quality: claudeImageJPEGQuality}); err != nil {
		return raw, mediaType, ""
	}
	// 只有真的更小才换。小 PNG（图标、截图）重编码成 JPEG 常常反而变大。
	if buf.Len() >= len(raw) {
		return raw, mediaType, ""
	}

	note = fmt.Sprintf("%s %d×%d %dKB → image/jpeg %d×%d %dKB",
		orDefault(format, "image"), w, h, len(raw)/1024, tw, th, buf.Len()/1024)
	return buf.Bytes(), "image/jpeg", note
}

// downscaleArea 用面积平均（box filter）缩放。
//
// 不用最近邻：缩到 1/2.5 以下时最近邻会丢掉细线（截图里的 1px 边框、代码里的
// 下划线），而这些细节恰好是模型要读的东西。面积平均对每个目标像素取源区域
// 均值，等价于先做一次低通再抽样。
func downscaleArea(src *image.RGBA, dstW, dstH int) *image.RGBA {
	sb := src.Bounds()
	srcW, srcH := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	scaleX := float64(srcW) / float64(dstW)
	scaleY := float64(srcH) / float64(dstH)

	for dy := 0; dy < dstH; dy++ {
		y0 := float64(dy) * scaleY
		y1 := y0 + scaleY
		if y1 > float64(srcH) {
			y1 = float64(srcH)
		}
		syStart, syEnd := int(y0), int(y1+0.5)
		if syEnd > srcH {
			syEnd = srcH
		}
		if syEnd <= syStart {
			syEnd = syStart + 1
			if syEnd > srcH {
				syEnd = srcH
			}
		}
		for dx := 0; dx < dstW; dx++ {
			x0 := float64(dx) * scaleX
			x1 := x0 + scaleX
			if x1 > float64(srcW) {
				x1 = float64(srcW)
			}
			sxStart, sxEnd := int(x0), int(x1+0.5)
			if sxEnd > srcW {
				sxEnd = srcW
			}
			if sxEnd <= sxStart {
				sxEnd = sxStart + 1
				if sxEnd > srcW {
					sxEnd = srcW
				}
			}

			var sr, sg, sb2, sa, count uint64
			for sy := syStart; sy < syEnd; sy++ {
				rowOff := sy * src.Stride
				for sx := sxStart; sx < sxEnd; sx++ {
					i := rowOff + sx*4
					sr += uint64(src.Pix[i])
					sg += uint64(src.Pix[i+1])
					sb2 += uint64(src.Pix[i+2])
					sa += uint64(src.Pix[i+3])
					count++
				}
			}
			di := dy*dst.Stride + dx*4
			if count == 0 {
				continue
			}
			dst.Pix[di] = uint8(sr / count)
			dst.Pix[di+1] = uint8(sg / count)
			dst.Pix[di+2] = uint8(sb2 / count)
			dst.Pix[di+3] = uint8(sa / count)
		}
	}
	return dst
}

// orDefault 给空串一个显示用的默认值（只用于日志文案）。
func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
