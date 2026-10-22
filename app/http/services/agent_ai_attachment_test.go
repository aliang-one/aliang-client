package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAgentAIAttachmentsConfinesLocalFiles(t *testing.T) {
	project := t.TempDir()
	inside := filepath.Join(project, "screen.png")
	if err := os.WriteFile(inside, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachments, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"path": "screen.png"},
		map[string]interface{}{"type": "image", "url": "https://example.com/screen.png"},
	}, project)
	if err != nil {
		t.Fatalf("resolve attachments: %v", err)
	}
	wantInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 2 || attachments[0].Path != wantInside || attachments[0].Type != "image" {
		t.Fatalf("attachments = %#v", attachments)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAgentAIAttachments([]interface{}{map[string]interface{}{"path": outside}}, project); err == nil {
		t.Fatal("outside-project attachment should be rejected")
	}
}

func TestAgentAIAttachmentsMapToProviderInputs(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "screen.png")
	run := agentAIRun{
		prompt: "inspect", attachments: []agentAIAttachment{
			{Type: "image", Path: imagePath, Name: "screen.png"},
			{Type: "image", URL: "https://example.com/remote.png", Name: "remote.png"},
		},
	}
	input := codexTurnInput(run)
	if len(input) != 3 || input[1]["type"] != "localImage" || input[2]["type"] != "image" {
		t.Fatalf("codex input = %#v", input)
	}

	codex := withAgentAIAttachments(&agentAITool{
		outputFormat: agentAIOutputCodexJSON, args: []string{"exec", "--json", "prompt"},
	}, run.attachments)
	if joined := strings.Join(codex.args, "\x00"); !strings.Contains(joined, "--image\x00"+imagePath) {
		t.Fatalf("codex args = %#v", codex.args)
	}

	opencode := withAgentAIAttachments(&agentAITool{
		outputFormat: agentAIOutputOpenCodeJSON, args: []string{"run", "--format", "json", "prompt"},
	}, []agentAIAttachment{{Path: imagePath, Name: "screen.png"}})
	if joined := strings.Join(opencode.args, "\x00"); !strings.Contains(joined, "--file\x00"+imagePath) {
		t.Fatalf("opencode args = %#v", opencode.args)
	}
}

func TestCompactCodexThreadUsesNativeAppServerMethod(t *testing.T) {
	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "codex", `#!/bin/sh
if [ "$1" = "app-server" ] && [ "$2" = "--help" ]; then
  echo "codex app-server"
  exit 0
fi
while IFS= read -r line; do
  case "$line" in
    *thread/compact/start*) printf '%s\n' '{"id":1,"result":{}}'; exit 0 ;;
  esac
done
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := compactCodexThread(t.Context(), t.TempDir(), "thread-123"); err != nil {
		t.Fatalf("compactCodexThread: %v", err)
	}
}

func TestResolveAgentAIAttachmentsAcceptsTextVariant(t *testing.T) {
	project := t.TempDir()
	// 缩进与首尾空行是 ASCII 图的一部分，必须原样保留。
	body := "\n   +--+\n   |  |\n   +--+\n"
	attachments, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"type": "text", "name": "diagram.txt", "text": body},
		// 未标 type 但有 text：按文本素材处理，而不是当成缺 path/url 报错。
		map[string]interface{}{"name": "flow.txt", "text": "a -> b"},
	}, project)
	if err != nil {
		t.Fatalf("resolve text attachments: %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("attachments = %#v", attachments)
	}
	if attachments[0].Type != "text" || attachments[0].Text != body {
		t.Fatalf("text body was altered: %#v", attachments[0])
	}
	if attachments[1].Type != "text" || attachments[1].Name != "flow.txt" {
		t.Fatalf("inferred text attachment = %#v", attachments[1])
	}
}

func TestResolveAgentAIAttachmentsRejectsInvalidText(t *testing.T) {
	project := t.TempDir()
	if _, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"type": "text", "text": "   \n\t "},
	}, project); err == nil {
		t.Fatal("blank text attachment should be rejected")
	}
	// 上限按 rune 计：32768 个中文字符通过，32769 个拒绝。
	ok := strings.Repeat("图", 32*1024)
	if _, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"type": "text", "text": ok},
	}, project); err != nil {
		t.Fatalf("32Ki runes of CJK should pass: %v", err)
	}
	tooLong := strings.Repeat("图", 32*1024+1)
	if _, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"type": "text", "text": tooLong},
	}, project); err == nil {
		t.Fatal("over-limit text attachment should be rejected")
	}
}

func TestAgentAIAttachmentPromptSuffixInlinesText(t *testing.T) {
	suffix := agentAIAttachmentPromptSuffix([]agentAIAttachment{
		{Type: "image", Path: "/tmp/shot.png", Name: "shot.png"},
		{Type: "text", Name: "ascii.txt", Text: "+--+\n|  |\n+--+"},
	}, true)
	if !strings.Contains(suffix, "- shot.png: /tmp/shot.png") {
		t.Fatalf("image line missing: %q", suffix)
	}
	if !strings.Contains(suffix, "\"ascii.txt\" (text)") || !strings.Contains(suffix, "|  |") {
		t.Fatalf("text body not inlined: %q", suffix)
	}
	// includeImages=false（codex 走原生 image block）时仍要保留文本素材。
	codex := agentAIAttachmentPromptSuffix([]agentAIAttachment{
		{Type: "image", URL: "https://example.com/a.png", Name: "a.png"},
		{Type: "text", Name: "ascii.txt", Text: "x"},
	}, false)
	if strings.Contains(codex, "a.png") || !strings.Contains(codex, "ascii.txt") {
		t.Fatalf("codex suffix = %q", codex)
	}
}

func TestAgentAIAttachmentPromptSuffixFenceEscapesBackticks(t *testing.T) {
	suffix := agentAIAttachmentPromptSuffix([]agentAIAttachment{
		{Type: "text", Name: "code.txt", Text: "a\n```\nb"},
	}, true)
	if !strings.Contains(suffix, "````") {
		t.Fatalf("fence should grow past the longest backtick run: %q", suffix)
	}
}
