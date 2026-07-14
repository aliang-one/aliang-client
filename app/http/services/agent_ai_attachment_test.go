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
