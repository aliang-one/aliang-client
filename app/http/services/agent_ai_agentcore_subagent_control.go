package services

// AliangCode（agentcore）子 agent 控制面（L3，2026-11-09）。
//
// L1（中断注入）解决「主 LLM 知道有子 agent 被中断了」，L2（sidechain journal）
// 解决「子 run 的成果不随进程蒸发」。但**控制手段**仍只有一个整回合 ai.stop：
//
//   - 并行批里某个子 agent 跑飞（比如死循环读一个巨大文件），只能连坐整回合，
//     其余跑得好好的子 agent 一并作废；
//   - 子 agent 跑偏方向（调研跑进无关目录）时，没有任何途径给它追加一句
//     「别管 X 了，去看 Y」——主 run 的 steer 通道只喂主 run。
//
// 本文件补的是「独立的」子 agent 控制面，两条能力：
//
//  1. per-task cancel：子 ctx 从父 ctx 派生（context.WithCancel），cancel 后子
//     run 走既有 interrupted 终态路径——父级取消仍级联，整回合停止语义分毫不变。
//  2. per-task steer 队列：每个子 run 一个**独立**队列，drain 只喂它自己。
//     spec §3 当初把子 run 的 Hooks 留成 nil，理由正是「复用父队列会让子 run
//     窃走主 run 待插话」（docs/superpowers/specs/2026-10-27-agentcore-subagents-design.md:61）。
//     独立队列既满足该约束，又把「边跑边改需求」还给子 agent。
//
// 谁在用：宿主 HTTP 面（仪表盘/手机经 PhoneServer 转发）按 session_id+task_id
// 下发。主 LLM 自己用不了——它在 task 工具的 Execute 里同步等结果，没有并发的
// 第二个工具调用窗口（这是内核工具模型的必然，不是缺口）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// agentCoreSubagentSteerContentCap 是单条追加指令的字节封顶（rune 安全截断）。
// 8KB 与 ai.steer 侧 agentAIMessageLimitBytes 同量级：够了，别让一条插话把
// 子 run 的下一轮请求撑爆。
const agentCoreSubagentSteerContentCap = 8 * 1024

// agentCoreSubagentSteerQueue 是单个子 run 的追加指令队列。
//
// 语义与主 run 的 agentAICoreSteerControl 一致：push 永远只是「排队」，真正
// 「进入上下文」发生在内核 GetSteeringMessages 的 drain 那一刻（下一次 LLM
// 调用前）。closed 之后 push 拒绝——子 run 已收工，指令没地方去。
type agentCoreSubagentSteerQueue struct {
	mu     sync.Mutex
	closed bool
	queue  []string
}

func newAgentCoreSubagentSteerQueue() *agentCoreSubagentSteerQueue {
	return &agentCoreSubagentSteerQueue{}
}

// push 排队一条追加指令。返回 false 表示队列已关闭（子 run 已结束）。
func (q *agentCoreSubagentSteerQueue) push(content string) bool {
	if q == nil {
		return false
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.queue = append(q.queue, content)
	return true
}

// drain 取走全部排队指令并转成内核消息。内核在**每次** LLM 调用前调用一次，
// 返回空切片时内核不做任何事（零副作用）。
func (q *agentCoreSubagentSteerQueue) drain() []agentcore.Message {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return nil
	}
	out := make([]agentcore.Message, 0, len(q.queue))
	for _, content := range q.queue {
		out = append(out, agentcore.TextMessage("user", content))
	}
	q.queue = nil
	return out
}

// close 关闭队列，后续 push 拒绝。幂等。
func (q *agentCoreSubagentSteerQueue) close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.queue = nil
}

// agentCoreSubagentHandle 是一个在跑子 agent 的控制句柄。
type agentCoreSubagentHandle struct {
	taskID    string
	name      string
	cancel    context.CancelFunc
	steer     *agentCoreSubagentSteerQueue
	startedAt time.Time
	// stopped 标记「本次停止由外部显式发起」（区别于父 ctx 级联）。仅用于
	// 诊断日志与 active() 的输出——终态相位统一由 bridge defer 判 ctx.Err()
	// 得出 interrupted，两条路径不需要分开写 journal。
	stopped atomic.Bool
}

// agentCoreSubagentControl 是一次主 run 内所有在跑子 agent 的控制表。
//
// 生命周期绑定主 run：runAgentCore 装配期建一份、挂到会话上供外部端点查找，
// 回合收尾时清掉。并发安全——并行批的多个子 run goroutine 同时 register/
// unregister，外部端点 goroutine 同时 stop/steerTo。
type agentCoreSubagentControl struct {
	mu    sync.Mutex
	seq   atomic.Int64
	subs  map[string]*agentCoreSubagentHandle
	order []string
	// sem 是并行子 run 的并发闸（2026-11-10 vm130 事故根治）。nil = 不设限
	// （配置关闸/测试路径）。容量按 customer.agentcore.subagent_max_parallel
	// 在 run 装配期定死——run 内固定、跨 run 热更新（新 run 读新配置）。
	// 闸在 LLM 请求之前拦：排队者不产生上游并发，上游 429 从结构上不可能。
	sem chan struct{}
}

func newAgentCoreSubagentControl() *agentCoreSubagentControl {
	return newAgentCoreSubagentControlWithLimit(0)
}

// newAgentCoreSubagentControlWithLimit 按上限建表。max<=0 不设闸（测试路径/
// 配置显式放开）。
func newAgentCoreSubagentControlWithLimit(max int) *agentCoreSubagentControl {
	c := &agentCoreSubagentControl{subs: make(map[string]*agentCoreSubagentHandle)}
	if max > 0 {
		c.sem = make(chan struct{}, max)
	}
	return c
}

// acquireRun 排队拿一个子 run 并发槽。ctx 取消（主回合停止/超时）时放弃排队
// ——排队者不该在主 run 死后还占着 tool slot 空等。nil 表/nil 闸直接放行。
func (c *agentCoreSubagentControl) acquireRun(ctx context.Context) error {
	if c == nil || c.sem == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.sem <- struct{}{}:
		return nil
	}
}

// errQueuedStopped 排队等待被 per-task stop 打断（区别于父 ctx 级联取消）。
var errQueuedStopped = errors.New("subagent queued wait stopped")

// acquireRunQueued 排队拿并发槽（审计 2026-11-10 ③）：等待同时听**两个**取消
// 源——parent（父回合）死=回合取消；queued（该子 agent 的子 ctx）死=排队期
// 被单独 stop。错误区分来源，调用方据此发不同语义的终态帧。
func (c *agentCoreSubagentControl) acquireRunQueued(parent, queued context.Context) error {
	if c == nil || c.sem == nil {
		return nil
	}
	select {
	case <-parent.Done():
		return parent.Err()
	case <-queued.Done():
		return errQueuedStopped
	case c.sem <- struct{}{}:
		return nil
	}
}

// releaseRun 归还并发槽。幂等安全由 chan 语义保证（多还只会 panic 于容量外
// ——桥 Execute 的 defer 恰好一次，与 acquire 严格配对）。
func (c *agentCoreSubagentControl) releaseRun() {
	if c == nil || c.sem == nil {
		return
	}
	<-c.sem
}

// register 登记一个在跑子 agent。taskID 为空时自动发号（mirror=nil 的测试
// 路径不产出 taskID，但控制面仍要能按下标停下它）。handle 非 nil 即登记成功。
func (c *agentCoreSubagentControl) register(taskID, name string, cancel context.CancelFunc) *agentCoreSubagentHandle {
	if c == nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = fmt.Sprintf("subagent-%d", c.seq.Add(1))
	}
	h := &agentCoreSubagentHandle{
		taskID:    taskID,
		name:      name,
		cancel:    cancel,
		steer:     newAgentCoreSubagentSteerQueue(),
		startedAt: time.Now(),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.subs[taskID]; ok {
		// 同名 task_id 重入（理论上不该发生：task_id 含回合 messageID+seq）。
		// 保留旧句柄的控制权，新句柄不接管——否则旧子 run 失联。
		return existing
	}
	c.subs[taskID] = h
	c.order = append(c.order, taskID)
	return h
}

// unregister 摘除句柄并关掉它的 steer 队列（子 run 已收工，迟到的插话拒绝）。
// 幂等；未登记过是 no-op。
func (c *agentCoreSubagentControl) unregister(taskID string) {
	if c == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	c.mu.Lock()
	h := c.subs[taskID]
	delete(c.subs, taskID)
	for i, id := range c.order {
		if id == taskID {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	if h != nil {
		h.steer.close()
	}
}

// stop 停止一个（task_id 非空）或全部（task_id 空）在跑子 agent。返回实际
// 停止的个数。已收工的 task_id 返回 0——调用方据此回「没有这个子 agent」。
//
// cancel 在锁外调用：cancel 会级联到子 run 的整条 ctx 链，同步在锁内做等于
// 让子 run 的收尾路径（defer unregister 要拿同一把锁）反过来等我们。
func (c *agentCoreSubagentControl) stop(taskID string) int {
	if c == nil {
		return 0
	}
	taskID = strings.TrimSpace(taskID)
	var targets []*agentCoreSubagentHandle
	c.mu.Lock()
	if taskID == "" {
		targets = make([]*agentCoreSubagentHandle, 0, len(c.order))
		for _, id := range c.order {
			if h := c.subs[id]; h != nil {
				targets = append(targets, h)
			}
		}
	} else if h := c.subs[taskID]; h != nil {
		targets = append(targets, h)
	}
	c.mu.Unlock()
	for _, h := range targets {
		h.stopped.Store(true)
		if h.cancel != nil {
			h.cancel()
		}
	}
	return len(targets)
}

// steerTo 给一个在跑子 agent 追加一句指令。返回 false 表示该 task_id 不在跑
// （已收工/不存在）或队列已关。
func (c *agentCoreSubagentControl) steerTo(taskID, content string) bool {
	if c == nil {
		return false
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}
	c.mu.Lock()
	h := c.subs[taskID]
	c.mu.Unlock()
	if h == nil {
		return false
	}
	return h.steer.push(truncateAgentCoreMCPOutputRuneSafe(content, agentCoreSubagentSteerContentCap))
}

// active 快照在跑子 agent（诊断/列表端点用）。按登记序输出，字段与 ai.subagent
// 帧同族但更薄——这里只报「能对哪个 task_id 下哪种控制」，不重复面板的进度面。
func (c *agentCoreSubagentControl) active() []map[string]interface{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.order) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(c.order))
	for _, id := range c.order {
		h := c.subs[id]
		if h == nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"task_id":    h.taskID,
			"name":       h.name,
			"started_at": h.startedAt.UTC().Format(time.RFC3339),
			"stoppable":  h.cancel != nil,
			"steerable":  true,
		})
	}
	return out
}

// count 返回在跑子 agent 数（无锁快路径供日志/守卫用，非精确快照可接受）。
func (c *agentCoreSubagentControl) count() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.order)
}
