package services

// file.write(手机端原生远程编辑,spec 2026-11-02 §D3):
// 内联 content 回写,路径守卫与 file.read 同源(resolveAgentProjectContentPath,
// TCC GuardPath+EvalSymlinks),原子写(同目录 tmp+rename),512KiB 上限与
// file.read 对称,expected_sha256 乐观锁防与 AI 回合并发互踩。错误细分码走
// file.error 的 error_code(契约已登记)。单文件毫秒级同步完成,不进单飞槽
// (原子 rename 天然并发安全,最后写赢;带 expected_sha256 者先到先得)。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/internal/tccguard"
)

// agentFileWriteMaxByte 与 file.read 的 agentProjectFileReadMaxByte(512KiB)
// 对称;大文件走互传通道(transfer.*),不走内联帧。
const agentFileWriteMaxByte = 512 * 1024

func agentFileWritePayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	fail := func(code string, err error) map[string]interface{} {
		return agentFileErrorPayloadCode(requestID, code, err)
	}

	projectPath := remoteString(msg, "project_path")
	rawPath := remoteString(msg, "path")
	if strings.TrimSpace(rawPath) == "" {
		return fail("bad_request", errors.New("path is required"))
	}
	// content 键缺失=畸形帧 → bad_request 早拒(否则 utf8 解码 0 字节会
	// 静默清空目标文件);显式空串=合法清空,放行。
	rawContent, hasContent := msg["content"].(string)
	if !hasContent {
		return fail("bad_request", errors.New("content is required"))
	}
	data, err := decodeAgentFileWriteContent(rawContent, remoteString(msg, "encoding"))
	if err != nil {
		return fail("bad_encoding", err)
	}
	if len(data) > agentFileWriteMaxByte {
		return fail("too_large", fmt.Errorf("content exceeds write limit (%d > %d bytes)", len(data), agentFileWriteMaxByte))
	}

	target, err := resolveAgentFileWritePath(projectPath, rawPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fail("target_not_found", err) // 父目录缺失(spec 错误码集)
		}
		return fail("path_forbidden", err)
	}

	if want := strings.TrimSpace(remoteString(msg, "expected_sha256")); want != "" {
		current, hashErr := hashAgentFileOnDisk(target)
		if hashErr != nil && !os.IsNotExist(hashErr) {
			return fail("io_error", hashErr)
		}
		if !strings.EqualFold(current, want) {
			return fail("conflict", fmt.Errorf("file changed on disk (expected sha256 %s, got %s)", want, current))
		}
	}

	written, err := atomicWriteAgentFile(target, data)
	if err != nil {
		return fail("io_error", err)
	}
	sum := sha256.Sum256(data)
	return map[string]interface{}{
		"type":       models.AgentEventFileWriteResult,
		"request_id": requestID,
		"ok":         true,
		"path":       filepath.ToSlash(target),
		"size_bytes": written,
		"sha256":     hex.EncodeToString(sum[:]),
	}
}

// decodeAgentFileWriteContent encoding=utf8(默认)|base64。
func decodeAgentFileWriteContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf8", "utf-8":
		return []byte(content), nil
	case "base64":
		return base64.StdEncoding.DecodeString(strings.TrimSpace(content))
	default:
		return nil, fmt.Errorf("unsupported encoding %q (want utf8|base64)", encoding)
	}
}

// resolveAgentFileWritePath 在 file.read 同源守卫之上补写侧语义:
// 已存在 → resolveAgentProjectContentPath 内部已做 EvalSymlinks+GuardPath,
// 但 EvalSymlinks 失败(悬空 symlink)说明落点不可验证,拒绝;
// 不存在 → 父目录必须存在且在守卫内(文件名组件原样使用,不递归 mkdir)。
func resolveAgentFileWritePath(projectPath, raw string) (string, error) {
	abs, err := resolveAgentProjectContentPath(projectPath, raw)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(abs); statErr == nil {
		if _, evalErr := filepath.EvalSymlinks(abs); evalErr != nil {
			return "", fmt.Errorf("target is a dangling symlink: %s", filepath.ToSlash(abs))
		}
		return abs, nil
	}
	dir := filepath.Dir(abs)
	resolved, evalErr := filepath.EvalSymlinks(dir)
	if evalErr != nil {
		if os.IsNotExist(evalErr) {
			// %w 包 os.ErrNotExist:handler 按 errors.Is 细分 target_not_found
			return "", fmt.Errorf("parent directory does not exist: %s: %w", filepath.ToSlash(dir), os.ErrNotExist)
		}
		return "", evalErr
	}
	if err := tccguard.GuardPath(resolved); err != nil {
		return "", err
	}
	return abs, nil
}

// hashAgentFileOnDisk 流式 sha256(预期校验面对的可能是超限大文件,不全量入内存)。
func hashAgentFileOnDisk(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// atomicWriteAgentFile 同目录 tmp+rename 原子写;已存在文件沿用原 perm,新建 0644。
// 返回 int64(契约 size_bytes 是 JSON number,仓内惯例 int64,防接口比较类型漂移)。
func atomicWriteAgentFile(target string, data []byte) (int64, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".aliang-filewrite-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return 0, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return 0, err
	}
	return int64(len(data)), nil
}
