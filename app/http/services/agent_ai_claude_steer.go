package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// 本文件实现 Claude Code headless 路径的 steer（回合中插话）通道。
//
// 与 Codex 的 app-server JSON-RPC `turn/steer` 不同，Claude 的 steer 就是
// 「往仍然活着的 `claude --print --input-format stream-json` 子进程 stdin
// 再写一行 user 消息」。CLI 会把这条消息在下一个模型 round-trip（工具边界）
// 折叠进当前 turn —— 即 queue-and-fold，不打断正在跑的工具；若消息在纯文本
// 流中途到达，当前 turn 结束后会另起一个 turn（2026-10-20 用 claude 2.1.286
// 实测：mid-tool 注入后模型改口 STEERED、只有 1 条 result；纯文本流中途注入
// 则产生 2 条 result）。
//
// 生命周期与 run 同寿：
//   - runCLIPass 建好 stdin 管道后立刻 attach，随后 start 写入首条 prompt；
//   - 流循环收到第一个 `result` 事件即 closeInput —— 关闭 stdin 后 CLI 约
//     0.5s 自然退出；实测「已进入管道」的 steer 仍会被 CLI 读完并执行，
//     所以这里不需要等待队列排空；
//   - run 结束由 clearRunning 兜底 close，把仍未投递的 steer 逐个 ack，
//     避免手机气泡永远停在 pending。

// agentAIClaudeSteerControl 是 Claude headless run 的 steer 通道。
type agentAIClaudeSteerControl struct {
	mu        sync.Mutex
	sessionID string
	runSeq    int
	stdin     io.WriteCloser
	write     agentTerminalWriter
	started   bool
	closed    bool
	queue     []agentAISteerMessage
}

func newAgentAIClaudeSteerControl(run agentAIRun, stdin io.WriteCloser, write agentTerminalWriter) *agentAIClaudeSteerControl {
	return &agentAIClaudeSteerControl{
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		stdin:     stdin,
		write:     write,
	}
}

// start 写入首条 prompt（stream-json 输入下 prompt 不再走 argv），随后把
// 启动前排队的 steer 一次性冲刷。必须在 cmd.Start() 之后调用。
func (c *agentAIClaudeSteerControl) start(prompt string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if err := c.writeUserLineLocked(prompt); err != nil {
		c.closed = true
		queued := c.queue
		c.queue = nil
		c.mu.Unlock()
		for _, item := range queued {
			emitAgentAISteerAck(c.write, c.sessionID, item.MessageID, "error",
				"claude stdin unavailable: "+err.Error(), "send_failed")
		}
		return
	}
	c.started = true
	queued := c.queue
	c.queue = nil
	c.mu.Unlock()
	for _, item := range queued {
		result, err := c.deliver(item)
		if err != nil {
			emitAgentAISteerAck(c.write, c.sessionID, item.MessageID, "error", err.Error(), "send_failed")
			continue
		}
		emitAgentAISteerAck(c.write, c.sessionID, item.MessageID, result, "", "")
	}
}

// writeUserLineLocked 把一条用户消息编码成 stream-json 输入行。
// 调用方必须持有 c.mu。
func (c *agentAIClaudeSteerControl) writeUserLineLocked(text string) error {
	if c.stdin == nil {
		return errors.New("claude stdin is unavailable")
	}
	payload := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "text", "text": text},
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = c.stdin.Write(raw)
	return err
}

// deliver 投递一条 steer：started 之前排队，closed 之后拒绝。
// 返回的 result 与 Codex 通道同口径（queued / applied / error）。
func (c *agentAIClaudeSteerControl) deliver(msg agentAISteerMessage) (string, error) {
	if c == nil {
		return "", errors.New("claude steer control is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "not_running", errors.New("claude turn ended before steer was applied")
	}
	if !c.started {
		c.queue = append(c.queue, msg)
		return "queued", nil
	}
	if err := c.writeUserLineLocked(msg.Content); err != nil {
		// 管道已断（CLI 已退出 / 被 kill）：关闸，后续 steer 直接走 not_running。
		c.closed = true
		return "error", err
	}
	return "applied", nil
}

// closeInput 在收到第一个 result 事件时关闭 stdin，让 CLI 自然退出。
// 已进入管道的 steer 仍会被 CLI 读完（实测），因此不必等待队列。
func (c *agentAIClaudeSteerControl) closeInput() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
}

// close 是 run 结束时的兜底：关闭 stdin 并把仍在排队的 steer 逐个 ack。
func (c *agentAIClaudeSteerControl) close(result, errMsg, code string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	queued := c.queue
	c.queue = nil
	c.closed = true
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.mu.Unlock()
	for _, item := range queued {
		emitAgentAISteerAck(c.write, c.sessionID, item.MessageID, result, errMsg, code)
	}
}

var claudeStreamJSONInputProbeCache sync.Map

// claudeStreamJSONInputAvailable 报告该 claude 二进制是否支持
// `--input-format stream-json` —— Claude steer 的物理前提。
//
// 探测失败一律视为不支持，退回旧的 argv prompt 路径：宁可不 steer，也不能
// 让普通回合因为一个未知 flag 起不来。缓存 key 含符号链接解析 + mtime + size
// （见 executableProbeCacheKey），CLI 升级后自动失效。
func claudeStreamJSONInputAvailable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := claudeStreamJSONInputProbeCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := newBackgroundCommandContext(ctx, path, "--help").CombinedOutput()
	available := err == nil && strings.Contains(string(output), "--input-format")
	claudeStreamJSONInputProbeCache.Store(cacheKey, available)
	return available
}
