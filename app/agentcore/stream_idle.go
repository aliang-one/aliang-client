package agentcore

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// streamIdleDefault 是 SSE 两次字节到达之间的最长静默时间默认值。
//
// 上游协议带 ping 保活，正常的长时间回合不会静默这么久；一旦窗口内一个字节
// 都没到，说明连接已半开或上游停滞，必须中断读取——否则 Read 会永久阻塞，
// 整轮 run 卡死在 agentcore.Run 内部（2026-10-27 事故形态）。
const streamIdleDefault = 90 * time.Second

// streamIdleEnvKey 是覆盖上面默认值的环境变量（time.ParseDuration 语法）。
// 0=禁用（仅测试用）。
const streamIdleEnvKey = "ALIANG_AGENTCORE_STREAM_IDLE_TIMEOUT"

// currentStreamIdleWindow 在每次构造 Client 时解析窗口，而不是在包初始化时
// 求值一次：值同样是环境变量，但初始化时读死会让同进程内的用例无法用
// t.Setenv 缩短窗口（只能真等 90s），端到端回归就此不可写。
func currentStreamIdleWindow() time.Duration {
	return agentcoreEnvDuration(streamIdleEnvKey, streamIdleDefault)
}

func agentcoreEnvDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// streamIdleError 表示「上游流停滞」：窗口内没有收到任何字节。
//
// 它被显式标记为不可重试（见 isRetryable）。原因：此时本尝试可能已经把
// start/text 等事件帧下发给了宿主，整轮重跑会让手机看到重复内容。
type streamIdleError struct{ window time.Duration }

func (e *streamIdleError) Error() string {
	return fmt.Sprintf("agentcore: 上游流停滞（%s 内未收到任何数据）", e.window)
}

// idleTimeoutReader 在两次成功读取之间超过 window 没有字节到达时关闭底层
// body，让阻塞中的 Read 立即返回错误。
//
// 为什么不用 SetReadDeadline：io.Reader 没有 deadline 概念，而 resp.Body 的
// 具体类型随 transport（h1/h2/代理）变化，拿不到稳定的 net.Conn。关闭 body
// 是唯一与传输实现无关的解除手段。
//
// 读到字节即重置计时器；timeout 触发时先置位再关闭，Read 侧据置位把关闭导致的
// 错误翻译成 *streamIdleError。
type idleTimeoutReader struct {
	body   io.ReadCloser
	window time.Duration

	mu       sync.Mutex
	timedOut bool
	closed   bool
	timer    *time.Timer
}

func newIdleTimeoutReader(body io.ReadCloser, window time.Duration) *idleTimeoutReader {
	r := &idleTimeoutReader{body: body, window: window}
	if window > 0 {
		r.timer = time.AfterFunc(window, r.fire)
	}
	return r
}

func (r *idleTimeoutReader) fire() {
	r.mu.Lock()
	r.timedOut = true
	r.mu.Unlock()
	// 关闭底层 body 会解开阻塞中的 Read（http 连接随即拆掉）。
	_ = r.body.Close()
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		r.reset()
	}
	if err != nil {
		r.mu.Lock()
		timedOut := r.timedOut
		r.mu.Unlock()
		if timedOut {
			return n, &streamIdleError{window: r.window}
		}
	}
	return n, err
}

func (r *idleTimeoutReader) reset() {
	if r.timer == nil {
		return
	}
	r.timer.Reset(r.window)
}

func (r *idleTimeoutReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	if r.timer != nil {
		r.timer.Stop()
	}
	return r.body.Close()
}
