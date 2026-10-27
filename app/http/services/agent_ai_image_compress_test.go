package services

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"strings"
	"testing"
)

// noisePNG 生成指定尺寸的「照片级」噪声 PNG：噪声基本压不动，因此能代表手机
// 上传的真实照片（体积大、信息熵高），也保证压缩前后的体量差异是真实收益而
// 不是编码器运气。transparentCorner>0 时左上角挖一块全透明区域，用来验证
// JPEG 白底合成（真实场景：带透明度的截图/图标拼图）。
func noisePNG(t *testing.T, w, h, transparentCorner int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(w*7919 + h)))
	buf := make([]byte, 4*w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			buf[4*x] = byte(rng.Intn(256))
			buf[4*x+1] = byte(rng.Intn(256))
			buf[4*x+2] = byte(rng.Intn(256))
			if x < transparentCorner && y < transparentCorner {
				buf[4*x+3] = 0
			} else {
				buf[4*x+3] = 255
			}
		}
		copy(img.Pix[y*img.Stride:(y+1)*img.Stride], buf)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return out.Bytes()
}

func decodeConfig(t *testing.T, raw []byte) image.Config {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

// TestCompressImageShrinksLargePhoto：手机原图尺寸 → 降到长边 1568 的 JPEG，
// 体积显著变小，且长宽比不能走样（走样等于图变形，模型读到的内容就是错的）。
func TestCompressImageShrinksLargePhoto(t *testing.T) {
	raw := noisePNG(t, 3000, 2000, 0)
	out, mediaType, note := compressImageForModel(raw, "image/png")
	if note == "" {
		t.Fatalf("大图应被压缩，raw=%dKB", len(raw)/1024)
	}
	if mediaType != "image/jpeg" {
		t.Errorf("mediaType = %q, want image/jpeg", mediaType)
	}
	if len(out) >= len(raw) {
		t.Errorf("压缩后 %dKB 不小于原始 %dKB", len(out)/1024, len(raw)/1024)
	}
	cfg := decodeConfig(t, out)
	if cfg.Width != claudeImageLongEdgeMax {
		t.Errorf("宽 = %d, want %d", cfg.Width, claudeImageLongEdgeMax)
	}
	ratio := float64(cfg.Width) / float64(cfg.Height)
	want := 3000.0 / 2000.0
	if ratio < want-0.02 || ratio > want+0.02 {
		t.Errorf("长宽比 = %.3f, want ≈ %.3f", ratio, want)
	}
	if !strings.Contains(note, "→ image/jpeg") {
		t.Errorf("note 应记录压缩前后，实际 %q", note)
	}
}

// TestCompressImageSkipsSmallImage：小图不动。压缩小图的收益抵不上重编码损失，
// 而且图标/小截图转 JPEG 往往反而变大。
func TestCompressImageSkipsSmallImage(t *testing.T) {
	raw := noisePNG(t, 32, 32, 0)
	out, mediaType, note := compressImageForModel(raw, "image/png")
	if note != "" {
		t.Errorf("小图不该被压缩，note=%q", note)
	}
	if mediaType != "image/png" {
		t.Errorf("mediaType = %q, want image/png（原样透传）", mediaType)
	}
	if !bytes.Equal(out, raw) {
		t.Error("小图字节应原样返回")
	}
}

// TestCompressImageFlattensTransparencyToWhite：JPEG 没有 alpha 通道，透明区域
// 必须垫白底。不垫会变黑（或随机色），用户看到的截图就和模型看到的不是一张图。
func TestCompressImageFlattensTransparencyToWhite(t *testing.T) {
	raw := noisePNG(t, 2000, 2200, 400)
	out, mediaType, note := compressImageForModel(raw, "image/png")
	if note == "" {
		t.Fatalf("带透明区的大图仍应被处理，raw=%dKB", len(raw)/1024)
	}
	if mediaType != "image/jpeg" {
		t.Fatalf("mediaType = %q, want image/jpeg", mediaType)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode jpeg: %v", err)
	}
	// 取透明方块内部若干点（缩放到 1568 后仍有 ~300px 见方；避开边界一圈，
	// 那里有 JPEG 振铃）。
	for _, pt := range []image.Point{{X: 30, Y: 30}, {X: 200, Y: 60}, {X: 60, Y: 200}} {
		r, g, bl, _ := img.At(pt.X, pt.Y).RGBA()
		if r>>8 < 245 || g>>8 < 245 || bl>>8 < 245 {
			t.Errorf("透明区应垫成白底，%v = (%d,%d,%d)", pt, r>>8, g>>8, bl>>8)
		}
	}
}

// TestCompressImagePassesThroughGIFAndWebP：GIF 可能是动图（重编码丢帧），
// WebP 标准库没解码器。两者一律原样透传——压坏了图比不压更糟。
func TestCompressImagePassesThroughGIFAndWebP(t *testing.T) {
	var gifBuf bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 2400, 2400), color.Palette{color.Black, color.White})
	for i := range pal.Pix {
		pal.Pix[i] = byte(i % 2)
	}
	if err := gif.Encode(&gifBuf, pal, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	cases := []struct {
		name      string
		raw       []byte
		mediaType string
	}{
		{"gif", gifBuf.Bytes(), "image/gif"},
		{"webp", []byte("RIFF....WEBPVP8 假装是 webp"), "image/webp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, mt, note := compressImageForModel(c.raw, c.mediaType)
			if note != "" || mt != c.mediaType || !bytes.Equal(out, c.raw) {
				t.Errorf("应原样透传，得到 mt=%q note=%q", mt, note)
			}
		})
	}
}

// TestCompressImageIgnoresUndecodableBytes：解不出来的字节不许动，也不许报错
// 逃逸——附件管线不能因为一张坏图就整轮失败。
func TestCompressImageIgnoresUndecodableBytes(t *testing.T) {
	raw := bytes.Repeat([]byte{0x00, 0xFF, 0x13, 0x37}, 200*1024) // 800KB 垃圾
	out, mt, note := compressImageForModel(raw, "image/png")
	if note != "" || mt != "image/png" || !bytes.Equal(out, raw) {
		t.Errorf("垃圾字节应原样返回，得到 mt=%q note=%q len=%d", mt, note, len(out))
	}
}

// TestDownscaleAreaAveragesBox：面积平均的算术本身。2×2 合成 1×1 必须等于四
// 点均值；用最近邻取样会得到其中某一个角，这里能直接区分。
func TestDownscaleAreaAveragesBox(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	put := func(x, y int, r, g, b uint8) {
		i := y*src.Stride + x*4
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = r, g, b, 255
	}
	put(0, 0, 0, 0, 0)
	put(1, 0, 100, 100, 100)
	put(0, 1, 200, 200, 200)
	put(1, 1, 40, 40, 40)

	dst := downscaleArea(src, 1, 1)
	i := 0
	got := (uint32(dst.Pix[i]) + uint32(dst.Pix[i+1]) + uint32(dst.Pix[i+2])) / 3
	want := uint32((0 + 100 + 200 + 40) / 4)
	if got != want {
		t.Errorf("均值 = %d, want %d", got, want)
	}
	if dst.Pix[i+3] != 255 {
		t.Errorf("alpha = %d, want 255", dst.Pix[i+3])
	}
}
