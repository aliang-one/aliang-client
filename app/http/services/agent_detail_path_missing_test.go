package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 手机端 humanizeFileError 以语义码 project_path_missing 匹配并渲染
// 「路径不存在」错误卡;目录被移动/删除的幽灵项目(会话历史残留路径)
// 打开时 agent 必须回带该码的错误,而不是裸 os.Stat 文案。
func TestResolveAgentProjectPathMissingEmitsSemanticCode(t *testing.T) {
	_, err := resolveAgentProjectPath(filepath.Join(t.TempDir(), "gone"))
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
	if !strings.Contains(err.Error(), "project_path_missing") {
		t.Fatalf("error should carry project_path_missing code, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "gone") {
		t.Fatalf("error should carry the requested path, got: %q", err.Error())
	}
}

// 非目录路径(存在但是文件)不属于「路径不存在」,保持原有文案不被误标。
func TestResolveAgentProjectPathNotADirectoryKeepsOriginalError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAgentProjectPath(file)
	if err == nil {
		t.Fatal("expected error for non-directory path")
	}
	if strings.Contains(err.Error(), "project_path_missing") {
		t.Fatalf("non-directory path must not be tagged project_path_missing, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("original not-a-directory message expected, got: %q", err.Error())
	}
}

// 线上手机走 file.list 拉目录;错误必须以 file.error 帧回到 request_id。
func TestAgentFileListPayloadMissingProjectPathReturnsFileErrorFrame(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "aliang-know-you-plan")
	payload := agentFileListPayload(map[string]interface{}{
		"request_id":   "req-ghost-1",
		"project_path": missing,
	})
	if got := payload["type"]; got != "file.error" {
		t.Fatalf("expected file.error frame, got type=%v", got)
	}
	if got := payload["request_id"]; got != "req-ghost-1" {
		t.Fatalf("request_id not echoed, got %v", got)
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "project_path_missing") {
		t.Fatalf("file.error should carry project_path_missing code, got: %q", message)
	}
	if !strings.Contains(message, "aliang-know-you-plan") {
		t.Fatalf("file.error should carry the project path, got: %q", message)
	}
}
