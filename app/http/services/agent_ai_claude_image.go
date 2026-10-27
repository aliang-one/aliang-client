package services

// Claude 图片附件 → stream-json image block。
//
// 为什么必须走 stdin 而不是 argv：claude --print 的 argv prompt 只能是纯文本，
// 图片要作为 user message 的 content block 传进去，而 stream-json 输入格式
// （`--input-format stream-json`）正好接受
//   {"type":"user","message":{"role":"user","content":[{"type":"text",...},
//    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}]}}
// 所以图片能力与 claudeSteer 通道共用同一条 stdin 管道。
//
// 体积：base64 会膨胀 4/3。Claude 单图上限 5MB 原始数据，这里取更保守的
// 7.5MiB 原始 / 10MiB base64 双闸；超限直接报错，绝不截断——半张图比没有图
// 更容易让模型得出错误结论。

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const (
	// 单图原始字节上限。7.5MiB base64 后约 10MiB。
	claudeImageRawMaxBytes = 7*1024*1024 + 512*1024
	// base64 后的硬上限，双保险（防止上面那条被改大）。
	claudeImageBase64MaxBytes = 10 * 1024 * 1024
)

// claudeImageMediaType 按扩展名推断 media type。Claude 只接受这四种。
func claudeImageMediaType(name string) (string, bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	default:
		return "", false
	}
}

// buildClaudeImageBlocks 把本地图片附件编码成 Claude content block。
// 文本附件不在这里处理（它们已经内联进 prompt 正文）。
func buildClaudeImageBlocks(attachments []agentAIAttachment) ([]map[string]interface{}, error) {
	var blocks []map[string]interface{}
	for _, attachment := range attachments {
		if attachment.Type != "image" {
			continue
		}
		if attachment.Path == "" {
			return nil, fmt.Errorf("image attachment %q has no local file", attachment.Name)
		}
		mediaType, ok := claudeImageMediaType(firstNonEmpty(attachment.Name, attachment.Path))
		if !ok {
			return nil, fmt.Errorf("unsupported image type for claude: %s", attachment.Name)
		}
		stat, err := os.Stat(attachment.Path)
		if err != nil {
			return nil, fmt.Errorf("read image attachment: %w", err)
		}
		if stat.Size() > claudeImageRawMaxBytes {
			return nil, fmt.Errorf("image attachment exceeds %d MiB: %s", claudeImageRawMaxBytes/(1024*1024), attachment.Name)
		}
		raw, err := os.ReadFile(attachment.Path)
		if err != nil {
			return nil, fmt.Errorf("read image attachment: %w", err)
		}
		// 先压再编：原始分辨率照片动辄 5MB，base64 后 6.7MB，而这份体积会跟着
		// 会话历史每一轮重传（见 agent_ai_image_compress.go 的取舍说明）。
		if compressed, compressedType, note := compressImageForModel(raw, mediaType); note != "" {
			raw, mediaType = compressed, compressedType
			logger.Warn(fmt.Sprintf("图片压缩 %s: %s", attachment.Name, note))
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		if len(encoded) > claudeImageBase64MaxBytes {
			return nil, fmt.Errorf("image attachment is too large after encoding: %s", attachment.Name)
		}
		blocks = append(blocks, map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": mediaType,
				"data":       encoded,
			},
		})
	}
	return blocks, nil
}

// buildSteerImageBlocks 给插话用的图片编码成 block。
//
// 与 run 路径的区别：run 的临时文件要活到子进程结束（codex/opencode 按路径读），
// 插话则是在这里就把 base64 编好塞进 steer 队列——排队期间 steer 可能还没写出，
// 若只存路径，清理早就把文件删了。所以下载完立即删，只留内存里的 block。
func buildSteerImageBlocks(ctx context.Context, attachments []agentAIAttachment, allowedHosts []string) ([]map[string]interface{}, error) {
	local, cleanup, err := downloadAgentAIAttachments(ctx, attachments, allowedHosts)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return buildClaudeImageBlocks(local)
}

// hasImageAttachments 报告是否有需要走 image block 的附件。
func hasImageAttachments(attachments []agentAIAttachment) bool {
	for _, attachment := range attachments {
		if attachment.Type == "image" {
			return true
		}
	}
	return false
}

// textOnlyAttachments 过滤出内联文本素材（steer 里单独拼进 content）。
func textOnlyAttachments(attachments []agentAIAttachment) []agentAIAttachment {
	var out []agentAIAttachment
	for _, attachment := range attachments {
		if attachment.Text != "" {
			out = append(out, attachment)
		}
	}
	return out
}
