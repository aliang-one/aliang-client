package agentcore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// liveAuthToken 取本机用户的平台 API key，口径与宿主侧 agentCoreAuthToken
// 的第一顺位一致：进程 env → ~/.claude/settings.json 的 env 块。
//
// 冒烟测试必须带上它，否则测的就不是生产那条路：网关对**不带** candidate key
// 的请求会落到 anthropic-duration 分组兜底，不在该组的账号直接 403。测试包
// 不能 import services（依赖方向反过来），所以这里就地读一遍。
func liveAuthToken() string {
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	return strings.TrimSpace(settings.Env["ANTHROPIC_AUTH_TOKEN"])
}

// 这一组是**真出网**的冒烟测试，默认跳过：它要求本机 56432 本地代理在跑，
// 并且会真的花一次模型调用。用 AGENTCORE_LIVE=1 打开。
//
// 为什么值得留：单元测试全部用 httptest 假 SSE，能证明「我们按契约解析」，
// 但不能证明「真实网关吐的帧我们吃得下」。两者之间最容易出问题的差异是
// 真实 SSE 带 `event: ` 行、带 `ping` 事件、`message_stop` 在最后——
// 假服务器永远不会自己冒出这些形状。真出网挂了，先跑这个。
func TestLiveSmokeThroughLocalProxy(t *testing.T) {
	if os.Getenv("AGENTCORE_LIVE") != "1" {
		t.Skip("设置 AGENTCORE_LIVE=1 才跑真出网冒烟（需要 56432 本地代理）")
	}
	model := strings.TrimSpace(os.Getenv("AGENTCORE_LIVE_MODEL"))
	if model == "" {
		model = "glm-5.2"
	}

	project := t.TempDir()
	const marker = "aliang-agentcore-live-ok"
	if err := os.WriteFile(filepath.Join(project, "hello.txt"), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	var mu sync.Mutex
	var frames []string
	// 与生产接线同一条路：带上用户自己的 key 走 user-api-key 通道。
	apiKey := liveAuthToken()
	if apiKey == "" {
		t.Log("未找到 ANTHROPIC_AUTH_TOKEN，本次不带 candidate key（会走分组兜底）")
	}
	cfg := &Config{
		Client:    NewClient("http://127.0.0.1:56432").WithAPIKey(apiKey),
		Model:     model,
		System:    "你是一个只会用工具办事的助手。",
		Tools:     DefaultRegistry(),
		Env:       &ToolEnv{ProjectPath: project},
		SessionID: "live-smoke",
		Emit: func(frame []byte) error {
			mu.Lock()
			frames = append(frames, string(frame))
			mu.Unlock()
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := Run(ctx, cfg, []Message{TextMessage("user",
		"用 Read 工具读取 hello.txt，然后只用一句话告诉我里面的内容。")})
	if err != nil {
		t.Fatalf("Run: %v\n帧序列:\n%s", err, strings.Join(snapshotFrames(&mu, frames), ""))
	}
	if res.IsError {
		t.Fatalf("终止帧是错误\n帧序列:\n%s", strings.Join(snapshotFrames(&mu, frames), ""))
	}
	if !strings.Contains(res.Text, marker) {
		t.Errorf("回复里没有读到文件内容 %q，实际 Text=%q", marker, res.Text)
	}
	if len(res.ToolCalls) == 0 {
		t.Errorf("模型没有调用任何工具，Turns=%d", res.Turns)
	}
	t.Logf("模型=%s 回合=%d 工具调用=%d 文本=%q", model, res.Turns, len(res.ToolCalls), res.Text)
}

func snapshotFrames(mu *sync.Mutex, frames []string) []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), frames...)
}
